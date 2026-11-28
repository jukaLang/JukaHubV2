//go:build linux

package main

// hotspot_linux.go — the hotspot's backend on the handheld (hotspot.go has the
// switch, the defaults and the pure rules). Every tool comes from a fixed list
// of names found in fixed directories, every call is bounded, and every step is
// logged under "hotspot", so a hotspot that will not come up on some firmware
// can be read off the log.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// hotspotSupported reports whether this build can have a hotspot at all: the
// Linux handheld. A Linux desktop has its own network manager for that.
func hotspotSupported() bool { return P().IsHandheld() }

// hotspotToolDirs are where the firmware keeps the tools; /usr/trimui/bin is
// the TrimUI images' own directory, first so a vendor build wins.
var hotspotToolDirs = []string{"/usr/trimui/bin", "/sbin", "/usr/sbin", "/usr/local/sbin", "/bin", "/usr/bin"}

// hotspotFindTool is the path of a firmware tool, or "" when the device has
// none. name is always one of this file's constants.
func hotspotFindTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range hotspotToolDirs {
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return cand
		}
	}
	return ""
}

// hotspotToolSet is what the firmware offers, probed once: tools do not appear
// while JukaHub runs.
var hotspotToolSet struct {
	once       sync.Once
	softapUp   string
	softapDown string
	hostapd    string
	hostapdCLI string
}

func hotspotToolsProbe() {
	hotspotToolSet.once.Do(func() {
		hotspotToolSet.softapUp = hotspotFindTool("softap_up")
		hotspotToolSet.softapDown = hotspotFindTool("softap_down")
		hotspotToolSet.hostapd = hotspotFindTool("hostapd")
		hotspotToolSet.hostapdCLI = hotspotFindTool("hostapd_cli")
		logInfo("hotspot", "tools: softap_up=%q softap_down=%q hostapd=%q hostapd_cli=%q",
			hotspotToolSet.softapUp, hotspotToolSet.softapDown, hotspotToolSet.hostapd, hotspotToolSet.hostapdCLI)
	})
}

func hotspotTools() (softapUp, softapDown, hostapd string) {
	hotspotToolsProbe()
	return hotspotToolSet.softapUp, hotspotToolSet.softapDown, hotspotToolSet.hostapd
}

// hotspotToolsLabel names how this firmware makes a hotspot, for the screen;
// ok is false when it has no way at all.
func hotspotToolsLabel() (label string, ok bool) {
	if !hotspotSupported() {
		return "", false
	}
	up, _, hostapd := hotspotTools()
	switch {
	case up != "":
		return "The TrimUI firmware's hotspot (softap_up)", true
	case hostapd != "":
		return "The firmware's hostapd, run by JukaHub", true
	}
	return "", false
}

// errHotspotNoTools is the honest answer on a firmware without either tool.
var errHotspotNoTools = errors.New("this firmware has no hotspot tools (no softap_up or hostapd)")

// hotspotExec runs one tool, bounded; the output is logged by the caller.
// WaitDelay matters here: a vendor script that leaves a child holding its
// output pipe (softap_up backgrounding hostapd without daemonising) would
// otherwise keep CombinedOutput waiting past the timeout, the start would never
// finish, and Wi-Fi would stay blocked behind it.
func hotspotExec(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenCommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The tool itself finished; a child it left behind still holds the
		// pipe. Its output so far is all there is.
		err = nil
	}
	return string(out), err
}

// hotspotSoftAPDownTimeout is softap_down's leash: shorter while quitting.
func hotspotSoftAPDownTimeout() time.Duration {
	if hotspotQuick.Load() {
		return 3 * time.Second
	}
	return 15 * time.Second
}

// hotspotMarkerFile names the backend a running hotspot uses, so the next
// start after a crash knows how to end it (hotspotStartupCleanup).
func hotspotMarkerFile() string { return filepath.Join(hotspotRunDir(), "method") }

// hotspotBackendStart brings the access point up. Any goroutine but the main
// thread: it blocks for seconds.
func hotspotBackendStart(ssid, pass string) (hotspotRun, error) {
	if err := validateWiFiSSID(ssid); err != nil {
		return hotspotRun{}, err
	}
	if err := validateWiFiPassphrase(pass); err != nil {
		return hotspotRun{}, err
	}
	up, down, hostapd := hotspotTools()
	if up == "" && hostapd == "" {
		return hotspotRun{}, errHotspotNoTools
	}
	iface := wifiInterface()
	if iface == "" || !validIfaceName(iface) {
		return hotspotRun{}, errors.New("no Wi-Fi radio found")
	}
	if on, known := airplaneEnabled(); known && on {
		return hotspotRun{}, errors.New("airplane mode is on")
	}
	// base is what a stop or a failed start gives back (hotspotGiveBackRadio).
	base := hotspotRun{iface: iface}
	if on, known := wifiRadioState(); known && !on {
		logInfo("hotspot", "switching the Wi-Fi radio on first")
		if err := setWifiEnabled(true); err != nil {
			return hotspotRun{}, fmt.Errorf("the Wi-Fi radio is off and could not be switched on: %v", err)
		}
		base.radioWasOff = true
		time.Sleep(time.Second)
	}
	// Under the supplicant start's own lock: a start already running finishes
	// first and is then stopped here; one that waits sees the hotspot holding
	// the radio and does nothing (ensureWifiBackendRetry).
	// The unlock is deferred inside a closure so a panic here (recovered by
	// hotspotStartAsync) can never leave Wi-Fi's start lock held.
	func() {
		ensureWifiBackendMu.Lock()
		defer ensureWifiBackendMu.Unlock()
		base.hadStation = supplicantPIDFor(iface) > 0
		hotspotStationOff(iface)
	}()
	withBase := func(run hotspotRun) hotspotRun {
		run.hadStation, run.radioWasOff = base.hadStation, base.radioWasOff
		return run
	}
	if up != "" {
		run, err := hotspotStartSoftAP(up, down, iface, ssid, pass)
		if err == nil {
			return withBase(run), nil
		}
		logWarn("hotspot", "softap_up did not bring a hotspot up: %v", err)
		if hostapd == "" {
			return base, err
		}
		logInfo("hotspot", "trying the firmware's hostapd instead")
	}
	run, err := hotspotStartHostapd(hostapd, iface, ssid, pass)
	if err != nil {
		return base, err
	}
	return withBase(run), nil
}

// hotspotStationOff takes the radio away from station mode: the DHCP client
// first (its lease-loss script would wipe the hotspot's address), then
// wpa_supplicant (it would keep scanning and fight hostapd for the radio).
func hotspotStationOff(iface string) {
	for i := 0; i < 3; i++ {
		pid := udhcpcPIDFor(iface)
		if pid <= 0 {
			break
		}
		logInfo("hotspot", "stopping udhcpc (pid %d) on %s", pid, iface)
		hotspotKill(pid, "udhcpc")
	}
	for i := 0; i < 2; i++ {
		pid := supplicantPIDFor(iface)
		if pid <= 0 {
			break
		}
		logInfo("hotspot", "stopping wpa_supplicant (pid %d) on %s", pid, iface)
		hotspotKill(pid, "wpa_supplicant")
	}
	// The station's lease goes too: the softap path reads the radio's address
	// to know the access point is up, and must not find the old LAN one.
	hotspotFlushAddr(iface)
	resetWiFiInterfaceCache()
}

// hotspotStartSoftAP runs Allwinner's softap_up and checks that it worked:
// hostapd running and an address on the radio. The tool's exit status alone
// is not trusted (its NAT step can fail while the AP runs, and the reverse).
func hotspotStartSoftAP(up, down, iface, ssid, pass string) (hotspotRun, error) {
	logInfo("hotspot", "running %s for %q", up, ssid)
	out, err := hotspotExec(25*time.Second, up, ssid, pass, "broadcast")
	logInfo("hotspot", "softap_up said: %s", hotspotOutLine(out))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if procPIDMatching(hotspotIsHostapd) > 0 {
			if ip := hotspotIfaceIPv4(iface); ip != "" {
				hotspotWriteMarker(hotspotMethodSoftAP)
				return hotspotRun{method: hotspotMethodSoftAP, iface: iface, ip: ip, ssid: ssid}, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if down != "" {
		dout, _ := hotspotExec(hotspotSoftAPDownTimeout(), down)
		logInfo("hotspot", "softap_down said: %s", hotspotOutLine(dout))
	}
	if err != nil {
		return hotspotRun{}, fmt.Errorf("softap_up failed: %s", setOr(hotspotOutLine(out), firstLine(err.Error())))
	}
	return hotspotRun{}, errors.New("softap_up ran, but no access point came up")
}

// hotspotWriteMarker records the running backend in the run dir.
func hotspotWriteMarker(method string) {
	if err := os.MkdirAll(hotspotRunDir(), 0o700); err != nil {
		logWarn("hotspot", "could not make %s: %v", hotspotRunDir(), err)
		return
	}
	if err := os.WriteFile(hotspotMarkerFile(), []byte(method+"\n"), 0o600); err != nil {
		logWarn("hotspot", "could not record the hotspot's method: %v", err)
	}
}

// hotspotReadMarker is the backend an earlier run recorded, or "".
func hotspotReadMarker() string {
	raw, err := os.ReadFile(hotspotMarkerFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// hotspotStartHostapd is the hotspot JukaHub runs itself on the firmware's
// hostapd: config in the run dir, hostapd daemonised with a pid file, the
// address on the radio, then DHCP.
func hotspotStartHostapd(hostapd, iface, ssid, pass string) (run hotspotRun, err error) {
	if hostapd == "" {
		return run, errHotspotNoTools
	}
	dir := hotspotRunDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return run, fmt.Errorf("could not make %s: %v", dir, err)
	}
	ctrl := filepath.Join(dir, "ctrl")
	conf := filepath.Join(dir, "hostapd.conf")
	pidf := filepath.Join(dir, "hostapd.pid")
	_ = os.Remove(pidf)
	if err := os.WriteFile(conf, []byte(hotspotHostapdConf(iface, ssid, pass, ctrl, true)), 0o600); err != nil {
		return run, fmt.Errorf("could not write the hostapd config: %v", err)
	}
	run = hotspotRun{method: hotspotMethodHostapd, iface: iface, ip: hotspotIP, ssid: ssid}
	// Whatever fails below undoes what came before it.
	defer func() {
		if err != nil {
			hotspotBackendStop(run)
		}
	}()
	hotspotFlushAddr(iface)
	logInfo("hotspot", "starting %s on %s", hostapd, iface)
	out, herr := hotspotExec(15*time.Second, hostapd, "-B", "-P", pidf, conf)
	if herr != nil {
		// Some drivers only change mode with the link down, and an old
		// hostapd or a plain driver refuses 802.11n: the retry is minimal.
		logWarn("hotspot", "hostapd failed (%s); retrying plain 802.11g with %s down", hotspotOutLine(out), iface)
		_ = os.Remove(pidf)
		if werr := os.WriteFile(conf, []byte(hotspotHostapdConf(iface, ssid, pass, ctrl, false)), 0o600); werr != nil {
			return run, fmt.Errorf("could not write the hostapd config: %v", werr)
		}
		hotspotLinkUp(iface, false)
		out, herr = hotspotExec(15*time.Second, hostapd, "-B", "-P", pidf, conf)
	}
	if herr != nil {
		return run, fmt.Errorf("hostapd could not start: %s", setOr(hotspotOutLine(out), firstLine(herr.Error())))
	}
	if !hotspotWaitPIDFile(pidf, "hostapd", 4*time.Second) {
		return run, errors.New("hostapd started, then stopped: see the log")
	}
	if err := hotspotAddAddr(iface); err != nil {
		return run, err
	}
	run.addedIP = true
	dhcp, derr := hotspotStartDHCP(iface, dir)
	if derr != nil {
		return run, derr
	}
	run.dhcp = dhcp
	hotspotWriteMarker(hotspotMethodHostapd)
	return run, nil
}

// hotspotStartDHCP starts a DHCP server for the hotspot: busybox udhcpd, else
// dnsmasq, else the built-in one. It names what it started.
func hotspotStartDHCP(iface, dir string) (string, error) {
	if name, prefix := hotspotUdhcpdCommand(); name != "" {
		conf := filepath.Join(dir, "udhcpd.conf")
		leases := filepath.Join(dir, "udhcpd.leases")
		if err := os.WriteFile(conf, []byte(hotspotUdhcpdConf(iface, dir)), 0o600); err == nil {
			_ = os.WriteFile(leases, nil, 0o600)
			args := append(append([]string(nil), prefix...), conf)
			out, err := hotspotExec(8*time.Second, name, args...)
			if err == nil && hotspotWaitPIDFile(filepath.Join(dir, "udhcpd.pid"), "udhcpd", 3*time.Second) {
				logInfo("hotspot", "udhcpd serves %s", iface)
				return "udhcpd", nil
			}
			logWarn("hotspot", "udhcpd did not start: %s", setOr(hotspotOutLine(out), hotspotErrLine(err)))
		}
	}
	if dnsmasq := hotspotFindTool("dnsmasq"); dnsmasq != "" {
		pidf := filepath.Join(dir, "dnsmasq.pid")
		args := []string{
			"--no-resolv", "--no-hosts", "--port=0", "--bind-interfaces",
			"--interface=" + iface, "--except-interface=lo",
			"--dhcp-range=" + hotspotPoolIP(hotspotPoolFirst) + "," + hotspotPoolIP(hotspotPoolLast) + "," + hotspotNetmask + ",12h",
			"--dhcp-option=3", "--dhcp-option=6",
			"--dhcp-leasefile=" + filepath.Join(dir, "dnsmasq.leases"),
			"--pid-file=" + pidf,
			// A busybox image may have no "nobody" user to drop to.
			"--user=root",
		}
		out, err := hotspotExec(8*time.Second, dnsmasq, args...)
		if err == nil && hotspotWaitPIDFile(pidf, "dnsmasq", 3*time.Second) {
			logInfo("hotspot", "dnsmasq serves %s", iface)
			return "dnsmasq", nil
		}
		logWarn("hotspot", "dnsmasq did not start: %s", setOr(hotspotOutLine(out), hotspotErrLine(err)))
	}
	if err := hotspotBuiltinDHCPStart(iface); err != nil {
		return "", fmt.Errorf("no DHCP server could start: %v", err)
	}
	logInfo("hotspot", "JukaHub's own DHCP server serves %s", iface)
	return "built-in", nil
}

func hotspotErrLine(err error) string {
	if err == nil {
		return ""
	}
	return firstLine(err.Error())
}

// hotspotBusyboxApplets caches whether busybox has udhcpd.
var hotspotBusyboxApplets struct {
	once    sync.Once
	busybox string
	udhcpd  bool
}

// hotspotUdhcpdCommand is how udhcpd runs here: its own binary, or
// "busybox udhcpd"; "" when neither exists.
func hotspotUdhcpdCommand() (string, []string) {
	if p := hotspotFindTool("udhcpd"); p != "" {
		return p, nil
	}
	hotspotBusyboxApplets.once.Do(func() {
		bb := hotspotFindTool("busybox")
		if bb == "" {
			return
		}
		out, err := hotspotExec(3*time.Second, bb, "--list")
		if err != nil {
			return
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == "udhcpd" {
				hotspotBusyboxApplets.busybox, hotspotBusyboxApplets.udhcpd = bb, true
				return
			}
		}
	})
	if hotspotBusyboxApplets.udhcpd {
		return hotspotBusyboxApplets.busybox, []string{"udhcpd"}
	}
	return "", nil
}

// hotspotBackendStop undoes a start: the softap tool's own stop, or the
// processes JukaHub started, the built-in DHCP server, the address and the run
// dir. Safe after a partial start.
func hotspotBackendStop(run hotspotRun) {
	dir := hotspotRunDir()
	switch run.method {
	case hotspotMethodSoftAP:
		if _, down, _ := hotspotTools(); down != "" {
			out, err := hotspotExec(hotspotSoftAPDownTimeout(), down)
			logInfo("hotspot", "softap_down said: %s", setOr(hotspotOutLine(out), hotspotErrLine(err)))
		} else if pid := procPIDMatching(hotspotIsHostapd); pid > 0 {
			hotspotKill(pid, "hostapd")
		}
	default:
		hotspotBuiltinDHCPStop()
		hotspotKillPIDFile(filepath.Join(dir, "udhcpd.pid"), "udhcpd")
		hotspotKillPIDFile(filepath.Join(dir, "dnsmasq.pid"), "dnsmasq")
		hotspotKillPIDFile(filepath.Join(dir, "hostapd.pid"), "hostapd")
	}
	if run.iface != "" {
		hotspotFlushAddr(run.iface)
		hotspotLinkUp(run.iface, true)
	}
	if err := os.RemoveAll(dir); err != nil {
		logWarn("hotspot", "could not remove %s: %v", dir, err)
	}
}

// hotspotBackendAlive reports whether the access point still runs.
func hotspotBackendAlive(run hotspotRun) bool {
	switch run.method {
	case hotspotMethodHostapd:
		return hotspotPIDFileAlive(filepath.Join(hotspotRunDir(), "hostapd.pid"), "hostapd")
	case hotspotMethodSoftAP:
		return procPIDMatching(hotspotIsHostapd) > 0
	}
	return false
}

// hotspotBackendClients lists who is on the hotspot: hostapd's station list
// where hostapd_cli answers, joined with the ARP table for addresses and the
// DHCP server's names.
func hotspotBackendClients(run hotspotRun) []hotspotClient {
	if run.iface == "" {
		return nil
	}
	data, _ := os.ReadFile("/proc/net/arp")
	clients := hotspotParseARP(string(data), run.iface)
	if stations, ok := hotspotListStations(run); ok {
		clients = hotspotMergeStations(clients, stations)
	}
	names := map[string]string{}
	switch run.dhcp {
	case "dnsmasq":
		if raw, err := os.ReadFile(filepath.Join(hotspotRunDir(), "dnsmasq.leases")); err == nil {
			names = hotspotParseDnsmasqLeases(string(raw))
		}
	case "built-in":
		names = hotspotBuiltinDHCPNames()
	}
	hotspotNameClients(clients, names)
	return clients
}

// hotspotListStations asks hostapd_cli for the associated stations' MACs; ok
// is false when there is no hostapd_cli or it does not answer.
func hotspotListStations(run hotspotRun) ([]string, bool) {
	hotspotToolsProbe()
	cli := hotspotToolSet.hostapdCLI
	if cli == "" {
		return nil, false
	}
	args := []string{"-i", run.iface, "list_sta"}
	if run.method == hotspotMethodHostapd {
		args = append([]string{"-p", filepath.Join(hotspotRunDir(), "ctrl")}, args...)
	}
	out, err := hotspotExec(2*time.Second, cli, args...)
	if err != nil {
		return nil, false
	}
	var macs []string
	for _, line := range strings.Split(out, "\n") {
		if hw, err := net.ParseMAC(strings.TrimSpace(line)); err == nil && len(hw) == 6 {
			macs = append(macs, hw.String())
		}
	}
	return macs, true
}

// hotspotMergeStations keeps the ARP clients hostapd still has associated and
// adds stations with no address yet (still asking DHCP). Pure.
func hotspotMergeStations(arp []hotspotClient, stations []string) []hotspotClient {
	on := make(map[string]bool, len(stations))
	for _, m := range stations {
		on[strings.ToLower(m)] = true
	}
	out := make([]hotspotClient, 0, len(stations))
	seen := map[string]bool{}
	for _, c := range arp {
		if on[c.MAC] {
			out = append(out, c)
			seen[c.MAC] = true
		}
	}
	for _, m := range stations {
		m = strings.ToLower(m)
		if !seen[m] {
			out = append(out, hotspotClient{MAC: m})
			seen[m] = true
		}
	}
	return out
}

// hotspotStartupCleanup ends a hotspot an earlier run left behind (a crash or
// a kill while it was on) and gives the radio back to Wi-Fi. It claims the
// switch like a stop, so a user start waits for it and the keeper stands
// aside meanwhile.
func hotspotStartupCleanup() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("hotspot", "startup cleanup failed: %v", r)
		}
	}()
	if !hotspotSupported() {
		return
	}
	dir := hotspotRunDir()
	if _, err := os.Stat(dir); err != nil {
		return
	}
	hotspotState.mu.Lock()
	if hotspotState.busy || hotspotState.phase != hotspotOff {
		hotspotState.mu.Unlock()
		return
	}
	hotspotState.busy = true
	hotspotSetPhaseLocked(hotspotStopping)
	hotspotState.mu.Unlock()
	release := func() {
		hotspotState.mu.Lock()
		hotspotState.busy = false
		hotspotSetPhaseLocked(hotspotOff)
		hotspotState.mu.Unlock()
	}
	method := hotspotReadMarker()
	iface := wifiInterface()
	if !validIfaceName(iface) {
		iface = ""
	}
	apUp := hotspotPIDFileAlive(filepath.Join(dir, "hostapd.pid"), "hostapd") ||
		(method == hotspotMethodSoftAP && procPIDMatching(hotspotIsHostapd) > 0)
	if !apUp {
		// The access point is gone, but a DHCP server it left would hand
		// 192.168.43.x leases into whatever network the radio joins next.
		dhcpUp := hotspotPIDFileAlive(filepath.Join(dir, "udhcpd.pid"), "udhcpd") ||
			hotspotPIDFileAlive(filepath.Join(dir, "dnsmasq.pid"), "dnsmasq")
		hotspotKillPIDFile(filepath.Join(dir, "udhcpd.pid"), "udhcpd")
		hotspotKillPIDFile(filepath.Join(dir, "dnsmasq.pid"), "dnsmasq")
		if dhcpUp && iface != "" && hotspotIfaceIPv4(iface) == hotspotIP {
			hotspotFlushAddr(iface)
		}
		_ = os.RemoveAll(dir)
		release()
		if dhcpUp {
			logInfo("hotspot", "stopped a DHCP server an earlier hotspot left behind")
			hotspotGiveBackRadio(hotspotRun{iface: iface, hadStation: true}, hotspotGiveBackPrior)
		}
		return
	}
	if method == "" {
		method = hotspotMethodHostapd
	}
	logInfo("hotspot", "a hotspot from an earlier run (%s) is still up; turning it off", method)
	hotspotBackendStop(hotspotRun{method: method, iface: iface})
	release()
	hotspotGiveBackRadio(hotspotRun{iface: iface, hadStation: true}, hotspotGiveBackPrior)
}

// ── Processes ────────────────────────────────────────────────────────────

func hotspotIsHostapd(argv []string) bool {
	return len(argv) > 0 && path.Base(argv[0]) == "hostapd"
}

// hotspotPIDIs reports whether pid is running and its command line names tool
// (directly or as a busybox applet), so a recycled pid is never signalled.
func hotspotPIDIs(pid int, tool string) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(raw) == 0 {
		return false
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if path.Base(argv[0]) == tool {
		return true
	}
	return path.Base(argv[0]) == "busybox" && len(argv) > 1 && path.Base(argv[1]) == tool
}

// hotspotReadPIDFile is the pid a daemon wrote, or 0.
func hotspotReadPIDFile(file string) int {
	raw, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

func hotspotPIDFileAlive(file, tool string) bool {
	return hotspotPIDIs(hotspotReadPIDFile(file), tool)
}

// hotspotWaitPIDFile waits for a daemon's pid file to name a live tool.
func hotspotWaitPIDFile(file, tool string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if hotspotPIDFileAlive(file, tool) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func hotspotKillPIDFile(file, tool string) {
	if pid := hotspotReadPIDFile(file); pid > 0 && hotspotPIDIs(pid, tool) {
		logInfo("hotspot", "stopping %s (pid %d)", tool, pid)
		hotspotKill(pid, tool)
	}
}

// hotspotKill ends pid: SIGTERM, up to 2 s (0.4 s while quitting), then
// SIGKILL. Only a pid whose command line still names tool is signalled.
func hotspotKill(pid int, tool string) {
	if !hotspotPIDIs(pid, tool) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	grace := 2 * time.Second
	if hotspotQuick.Load() {
		grace = 400 * time.Millisecond
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !hotspotPIDIs(pid, tool) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hotspotPIDIs(pid, tool) {
		logWarn("hotspot", "%s (pid %d) ignored SIGTERM; killing it", tool, pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// ── Addresses ────────────────────────────────────────────────────────────

// hotspotIfaceIPv4 is iface's first IPv4 address, or "".
func hotspotIfaceIPv4(iface string) string {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				return ip4.String()
			}
		}
	}
	return ""
}

// hotspotAddAddr puts hotspotIP on the radio and brings the link up: ip, else
// ifconfig.
func hotspotAddAddr(iface string) error {
	cidr := hotspotIP + "/" + strconv.Itoa(hotspotPrefixLen)
	if ip := hotspotFindTool("ip"); ip != "" {
		out, err := hotspotExec(5*time.Second, ip, "addr", "add", cidr, "dev", iface)
		if err == nil || strings.Contains(out, "File exists") {
			_, _ = hotspotExec(5*time.Second, ip, "link", "set", iface, "up")
			logInfo("hotspot", "%s has %s", iface, cidr)
			return nil
		}
		logWarn("hotspot", "ip addr add failed: %s", setOr(hotspotOutLine(out), hotspotErrLine(err)))
	}
	if ifc := hotspotFindTool("ifconfig"); ifc != "" {
		out, err := hotspotExec(5*time.Second, ifc, iface, hotspotIP, "netmask", hotspotNetmask, "up")
		if err == nil {
			logInfo("hotspot", "%s has %s (ifconfig)", iface, cidr)
			return nil
		}
		return fmt.Errorf("could not give %s its address: %s", iface, setOr(hotspotOutLine(out), hotspotErrLine(err)))
	}
	return errors.New("no ip or ifconfig on this firmware to set the hotspot's address")
}

// hotspotFlushAddr removes every IPv4 address from the radio: the station's
// lease before the hotspot, the hotspot's after it (the station's DHCP adds
// its own again).
func hotspotFlushAddr(iface string) {
	if ip := hotspotFindTool("ip"); ip != "" {
		if _, err := hotspotExec(5*time.Second, ip, "-4", "addr", "flush", "dev", iface); err == nil {
			return
		}
	}
	if ifc := hotspotFindTool("ifconfig"); ifc != "" {
		_, _ = hotspotExec(5*time.Second, ifc, iface, "0.0.0.0")
	}
}

// hotspotLinkUp sets the radio's link up or down.
func hotspotLinkUp(iface string, up bool) {
	state := "down"
	if up {
		state = "up"
	}
	if ip := hotspotFindTool("ip"); ip != "" {
		if _, err := hotspotExec(5*time.Second, ip, "link", "set", iface, state); err == nil {
			return
		}
	}
	if ifc := hotspotFindTool("ifconfig"); ifc != "" {
		_, _ = hotspotExec(5*time.Second, ifc, iface, state)
	}
}

// hotspotOutLine is a tool's last non-empty output line, the one that says
// what went wrong, trimmed for a toast.
func hotspotOutLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 160 {
				l = l[:160]
			}
			return l
		}
	}
	return ""
}

// ── The built-in DHCP server's socket ────────────────────────────────────

var hotspotBuiltinDHCP struct {
	mu   sync.Mutex
	conn net.PacketConn
	pool *hsDHCPPool
}

// hotspotBuiltinDHCPStart listens on UDP 67, bound to iface, and answers with
// hsDHCPPool. Root on the handheld, which JukaHub is.
func hotspotBuiltinDHCPStart(iface string) error {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); serr != nil {
				return
			}
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); serr != nil {
				return
			}
			serr = syscall.BindToDevice(int(fd), iface)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	conn, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:67")
	if err != nil {
		return err
	}
	pool := newHSDHCPPool()
	hotspotBuiltinDHCP.mu.Lock()
	if hotspotBuiltinDHCP.conn != nil {
		_ = hotspotBuiltinDHCP.conn.Close()
	}
	hotspotBuiltinDHCP.conn, hotspotBuiltinDHCP.pool = conn, pool
	hotspotBuiltinDHCP.mu.Unlock()
	go hotspotBuiltinDHCPServe(conn, pool)
	return nil
}

func hotspotBuiltinDHCPServe(conn net.PacketConn, pool *hsDHCPPool) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("hotspot", "DHCP server stopped: %v", r)
		}
	}()
	buf := make([]byte, 1500)
	bcast := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		req, ok := hsDHCPParse(buf[:n])
		if !ok {
			continue
		}
		if reply := pool.handle(req); reply != nil {
			if _, err := conn.WriteTo(reply, bcast); err != nil {
				logDebug("hotspot", "DHCP reply to %s failed: %v", req.mac, err)
			}
		}
	}
}

func hotspotBuiltinDHCPStop() {
	hotspotBuiltinDHCP.mu.Lock()
	conn := hotspotBuiltinDHCP.conn
	hotspotBuiltinDHCP.conn, hotspotBuiltinDHCP.pool = nil, nil
	hotspotBuiltinDHCP.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func hotspotBuiltinDHCPNames() map[string]string {
	hotspotBuiltinDHCP.mu.Lock()
	pool := hotspotBuiltinDHCP.pool
	hotspotBuiltinDHCP.mu.Unlock()
	if pool == nil {
		return map[string]string{}
	}
	return pool.namesByMAC()
}
