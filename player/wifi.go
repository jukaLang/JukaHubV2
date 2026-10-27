package main

// wifi.go — the Wi-Fi control layer behind the network picker.
//
// The firmware ships wpa_supplicant and wpa_cli but no network manager this app
// can call, so joining a new network meant leaving JukaHub for a settings
// screen some images do not even include. This file owns the backend; the
// panel lives in wifi_ui.go.
//
// The same rules as systemcontrol.go apply:
//
//   - every binary comes from a fixed set (wpa_cli, udhcpc, dhcpcd, dhclient)
//     and no config value is ever used as a command name or a path;
//   - every call is bounded, and a failure is returned to the caller instead of
//     being swallowed;
//   - the passphrase is never an argument. wpa_cli re-parses the whole command
//     line, so the PSK is written to its stdin: it cannot show up in `ps`, in
//     the log, or in this app's error messages;
//   - missing hardware is a normal state. "No wireless interface" and "wpa_cli
//     is not installed" are answers, not errors to retry.
//
// Parsing, validation and the signal mapping are pure functions so the picker's
// behaviour can be tested on a machine that has no Wi-Fi at all.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// wifiCompleteState is the wpa_supplicant state that means associated.
	wifiCompleteState = "COMPLETED"

	// wifiCommandTimeout bounds one wpa_cli call. A call that has not answered
	// by then is a stuck supplicant, not a slow network.
	wifiCommandTimeout = 5 * time.Second
	// wifiAssociatePoll is the interval between association checks.
	wifiAssociatePoll = 400 * time.Millisecond
	// wifiScanWaitTimeout is how long the picker waits for scan_results after
	// requesting a scan. A scan that returns nothing by then gets a message.
	wifiScanWaitTimeout  = 8 * time.Second
	wifiScanPoll         = 400 * time.Millisecond
	wifiDHCPTimeout      = 12 * time.Second
	wifiMaxSSIDBytes     = 32
	wifiMinPassphraseLen = 8
	wifiMaxPassphraseLen = 63

	// wifiProbeTimeout bounds one discovery call. Discovery is read while the
	// status bar draws, so the supplicant probe must fail fast rather than hold
	// a frame for the five seconds a join is allowed.
	wifiProbeTimeout = 2 * time.Second
	// wifiProbeBudget bounds the whole probe, listing and sweep together: the
	// answer is cached, but the one call that fills the cache still happens on
	// the caller's thread.
	wifiProbeBudget = 3 * time.Second
	// wifiProbeMaxCandidates bounds the netdev sweep the probe falls back to.
	wifiProbeMaxCandidates = 4
	// wifiDrivenTTL is how long the supplicant's answer is trusted. It has to be
	// long enough that the status bar never forks a process per frame and short
	// enough that a hot-plugged adapter is noticed without a restart.
	wifiDrivenTTL = time.Minute
)

// wifiAssociateTimeout is how long association may take before the join is
// reported as failed. WPA2 on a weak handheld radio is slow, not broken. It is
// a variable only so a test can prove the failure paths (rejected password,
// stuck supplicant) without waiting out the real seconds.
var wifiAssociateTimeout = 15 * time.Second

// The supplicant probe's cached answer. Discovery runs on the render thread (the
// status bar asks for the interface), so the probe behind it is remembered: a
// per-frame caller must never fork wpa_cli once a frame.
var (
	wifiDrivenMu    sync.Mutex
	wifiDrivenName  string
	wifiDrivenAt    time.Time
	wifiDrivenKnown bool
	// wifiDrivenNow is the cache clock, swappable so a test can age the answer
	// without sleeping a minute.
	wifiDrivenNow = time.Now
)

// wifiNetwork is one visible access point after merging the scan with the
// saved-network list and the current status.
type wifiNetwork struct {
	SSID    string
	BSSID   string
	Signal  int // dBm, negative
	Secured bool
	Saved   bool
	Active  bool
	SaveID  int // network id in wpa_supplicant, 0 when not saved
}

// wifiStatus is the part of `wpa_cli status` the picker shows.
type wifiStatus struct {
	State     string
	SSID      string
	IP        string
	Connected bool
}

// savedNetwork is one row of `wpa_cli list_networks`.
type savedNetwork struct {
	ID    int
	SSID  string
	Flags string
}

// wifiJoinResult reports what happened after a join, so the panel can say that
// a network works for this session but could not be saved.
type wifiJoinResult struct {
	SSID  string
	IP    string
	Saved bool
	Warn  string
}

// ── Command execution ──────────────────────────────────────────────────────

// wifiRunner runs one allowlisted command. Tests replace it, so no Wi-Fi
// hardware is ever touched by a test run.
var wifiRunner = func(timeout time.Duration, stdin, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s timed out after %s", name, timeout)
	}
	return string(out), err
}

// wifiToolDirs are the standard binary locations a launcher's inherited PATH
// can miss. Firmware images keep wpa_cli, udhcpc and rfkill in sbin directories
// that a minimal PATH may not include, and the result would be "wpa_cli is not
// installed" for a tool that is on the card.
var wifiToolDirs = []string{"/sbin", "/usr/sbin", "/usr/local/sbin", "/bin", "/usr/bin"}

// wifiToolCommand is the argv0 a Wi-Fi tool runs from: the bare allowlisted
// name when PATH has it, the absolute path when PATH misses it but a system
// directory has it, and the bare name when it exists nowhere (the caller's
// existence check decides that; the runner's own error stays the truth for
// stubbed tests).
func wifiToolCommand(name string) string {
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return name
	}
	for _, dir := range wifiToolDirs {
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return name
}

// wifiToolAvailable reports whether a firmware tool exists on this device,
// whether or not the process PATH lists the directory it lives in.
func wifiToolAvailable(name string) bool {
	if name == "" {
		return false
	}
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	return wifiToolCommand(name) != name
}

// wifiToolExists is the presence probe behind a variable so tests can pretend
// the firmware tools are present (or absent) deterministically.
var wifiToolExists = wifiToolAvailable

// wifiRadioState reads the radio through systemcontrol behind a variable: the
// scan uses it to explain a soft-blocked adapter instead of surfacing wpa_cli's
// bare FAIL, and tests can set the state without rfkill hardware.
var wifiRadioState = wifiEnabled

// wifiSysfsNetRoot, wifiProcWirelessPath and wifiSupplicantDirs are the three
// places an interface can be named. They are variables so a test can point the
// probe at a fixture tree: /sys and /proc are not writable, and a desktop has
// neither a radio nor a supplicant.
var (
	wifiSysfsNetRoot     = "/sys/class/net"
	wifiProcWirelessPath = "/proc/net/wireless"
	// wpa_supplicant's control directory, wherever a firmware build put it.
	// Embedded images with a read-only root often point ctrl_interface at /tmp,
	// and an older image uses /var/run directly.
	wifiSupplicantDirs = []string{
		"/run/wpa_supplicant",
		"/var/run/wpa_supplicant",
		"/tmp/wpa_supplicant",
		"/tmp/run/wpa_supplicant",
	}
)

// sysfsNetdevs lists the entry names under a sysfs net root, sorted so a device
// exposing two radios always resolves to the same one.
func sysfsNetdevs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// isWirelessNetdev reports whether one entry under a sysfs net root is a
// wireless device.
//
// The directory test goes through os.Stat on purpose: /sys/class/net/<name> is a
// symlink into the device tree on every kernel that exposes netdevs the normal
// way, and DirEntry.IsDir reports the *link*, not its target. Testing the entry
// itself therefore called every real radio a non-directory, which is why the
// picker answered "No wireless network interface on this device" on a handheld
// that was online.
//
// "wireless" is the wireless-extensions marker an older or vendor driver
// creates; "phy80211" is the cfg80211 one. Accepting both keeps the probe
// honest across firmware generations.
func isWirelessNetdev(root, name string) bool {
	base := filepath.Join(root, name)
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, marker := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join(base, marker)); err == nil {
			return true
		}
	}
	return false
}

// findWirelessInterface picks the wireless netdev under root. Entries are
// sorted so a device exposing two radios always resolves to the same one.
func findWirelessInterface(root string) (string, error) {
	names, err := sysfsNetdevs(root)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if isWirelessNetdev(root, name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no wireless interface under %s", root)
}

// parseWirelessProcIfaces returns the interface names listed in
// /proc/net/wireless. That file is the kernel's own list of interfaces with
// wireless extensions, so it names the radio even when the driver creates
// neither a "wireless" nor a "phy80211" attribute — the case a vendor SDIO
// driver built without cfg80211 produces. The header lines carry no colon, so
// only data lines are read.
func parseWirelessProcIfaces(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if !validIfaceName(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// supplicantInterfaceNames reads the interfaces wpa_supplicant is managing. The
// control directory holds one socket per interface; it names a radio the
// supplicant can actually drive, whatever sysfs says about the driver.
// wpa_supplicant's own config file is a directory entry too, and validIfaceName
// filters it out (its name is longer than an interface name can be).
func supplicantInterfaceNames(dirs []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !validIfaceName(name) || seen[name] {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil || info.IsDir() {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// parseWpaCliInterfaceList reads the interfaces wpa_cli reports. The
// supplicant answers the global INTERFACE command with one "IFNAME=<name>" line
// per interface; wpa_cli's interactive listing prints "Available interfaces
// are: <name> [<name>...]" and, once one is selected, "Selected interface
// '<name>'". Both shapes are read, and anything that is not a valid interface
// name is ignored, so a FAIL line or a sentence cannot become a device name.
func parseWpaCliInterfaceList(out string) []string {
	seen := map[string]bool{}
	var names []string
	add := func(candidate string) {
		name := strings.Trim(strings.TrimSpace(candidate), "'\"")
		if !validIfaceName(name) || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "IFNAME="):
			add(strings.TrimPrefix(t, "IFNAME="))
		case strings.HasPrefix(t, "Available interfaces are:"):
			for _, f := range strings.Fields(strings.TrimPrefix(t, "Available interfaces are:")) {
				add(f)
			}
		case strings.HasPrefix(t, "Selected interface"):
			for _, f := range strings.Fields(strings.TrimPrefix(t, "Selected interface")) {
				add(f)
			}
		}
	}
	sort.Strings(names)
	return names
}

// wpaCliDrivesInterface reports whether the supplicant answers for one netdev.
// A status reply is a `key=value` list and always names a state; wpa_cli prints
// FAIL, without one, for an interface that is not its own.
func wpaCliDrivesInterface(wpa, name string) bool {
	out, err := wifiRunner(wifiProbeTimeout, "", wpa, "-i", name, "status")
	if err != nil {
		return false
	}
	return strings.Contains(out, "wpa_state=")
}

// wpaCliDrivenInterface asks the supplicant which netdev it is actually
// driving. It is the fifth source, and the only one that is ground truth: an
// image can point ctrl_interface at a directory none of wifiSupplicantDirs cover
// and still name a radio in neither sysfs nor /proc, and then the tool that owns
// the radio is the one thing that knows its name.
//
// The listing comes first because it is one call and answers outright. Its
// output shape differs between wpa_cli builds, and on an image whose supplicant
// does not serve a global control socket the command answers nothing usable at
// all, so the fallback sweeps the netdevs in turn asking each for its status: a
// bounded number of them, since a desktop can list several.
func wpaCliDrivenInterface() string {
	if !wifiToolExists("wpa_cli") {
		return ""
	}
	wpa := wifiToolCommand("wpa_cli")
	if out, err := wifiRunner(wifiProbeTimeout, "", wpa, "interface"); err == nil {
		if names := parseWpaCliInterfaceList(out); len(names) > 0 {
			return names[0]
		}
	}
	names, _ := sysfsNetdevs(wifiSysfsNetRoot)
	tried := 0
	started := time.Now()
	for _, name := range names {
		if name == "lo" || !validIfaceName(name) {
			continue
		}
		if tried >= wifiProbeMaxCandidates || time.Since(started) > wifiProbeBudget {
			break
		}
		tried++
		if wpaCliDrivesInterface(wpa, name) {
			return name
		}
	}
	return ""
}

// supplicantDrivenInterfaceCached is wpaCliDrivenInterface behind a TTL cache.
// Discovery is read while the status bar draws, so an uncached probe would fork
// wpa_cli on every frame — and a negative answer has to be remembered too, or a
// device with no radio would pay for a process per frame forever.
func supplicantDrivenInterfaceCached() string {
	wifiDrivenMu.Lock()
	defer wifiDrivenMu.Unlock()
	if wifiDrivenKnown && wifiDrivenNow().Sub(wifiDrivenAt) < wifiDrivenTTL {
		return wifiDrivenName
	}
	wifiDrivenName = wpaCliDrivenInterface()
	wifiDrivenAt = wifiDrivenNow()
	wifiDrivenKnown = true
	return wifiDrivenName
}

// resetSupplicantInterfaceCache forgets the remembered answer so the next
// lookup asks the supplicant again. Opening the picker calls it, which is when a
// person would have plugged an adapter in; tests call it to keep fixtures apart.
func resetSupplicantInterfaceCache() {
	wifiDrivenMu.Lock()
	defer wifiDrivenMu.Unlock()
	wifiDrivenName = ""
	wifiDrivenAt = time.Time{}
	wifiDrivenKnown = false
}

// looksLikeWirelessName is the last resort: no marker, no proc entry, no
// control socket, and the netdev is named the way a Wi-Fi interface is. An image
// whose vendor driver is built without wireless extensions and without cfg80211
// attributes still has a radio that `wpa_cli -i wlan0` drives; nothing in sysfs
// says which one it is, but the name does.
func looksLikeWirelessName(name string) bool {
	for _, prefix := range []string{"wlan", "wlp", "wlx", "ra0", "rai", "ath", "p2p"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// wirelessInterfaceNames names every candidate radio, strongest evidence first:
// a sysfs wireless marker, then the kernel's own /proc list, then a
// wpa_supplicant control socket, then the supplicant's own answer, then the
// naming convention. Each source is sorted, so a device with two radios always
// resolves to the same one.
func wirelessInterfaceNames() []string {
	names, _ := sysfsNetdevs(wifiSysfsNetRoot)
	var marked, conventional []string
	for _, name := range names {
		if isWirelessNetdev(wifiSysfsNetRoot, name) {
			marked = append(marked, name)
			continue
		}
		if looksLikeWirelessName(name) {
			conventional = append(conventional, name)
		}
	}
	if len(marked) > 0 {
		return marked
	}
	if data, err := os.ReadFile(wifiProcWirelessPath); err == nil {
		if listed := parseWirelessProcIfaces(string(data)); len(listed) > 0 {
			return listed
		}
	}
	if viaSupplicant := supplicantInterfaceNames(wifiSupplicantDirs); len(viaSupplicant) > 0 {
		return viaSupplicant
	}
	// Nothing on the card named the radio. Ask the tool that drives it: this is
	// the only source that cannot be wrong about which netdev the supplicant
	// holds, and it is cached, so a per-frame reader pays for it once a minute.
	if driven := supplicantDrivenInterfaceCached(); driven != "" {
		return []string{driven}
	}
	return conventional
}

// validIfaceName keeps an environment override from becoming an option or a
// second argument to wpa_cli.
func validIfaceName(name string) bool {
	if name == "" || len(name) > 15 || name[0] == '-' || name[0] == '.' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// wifiInterface is the netdev to control. JUKAHUB_WIFI_IFACE exists for images
// whose radio is not named wlan* and for the desktop test rig.
func wifiInterface() string {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_WIFI_IFACE")); validIfaceName(override) {
		return override
	}
	names := wirelessInterfaceNames()
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// wifiControlAvailable reports whether this device can be driven through
// wpa_cli at all.
func wifiControlAvailable() bool {
	return wifiInterface() != "" && wifiToolExists("wpa_cli")
}

// wifiUnavailableReason explains an unavailable backend in one line.
func wifiUnavailableReason() string {
	switch {
	case wifiInterface() == "":
		return "No wireless network interface on this device"
	case !wifiToolExists("wpa_cli"):
		return "Wi-Fi control needs wpa_cli, which is not installed"
	}
	return ""
}

// wifiFailureMessage turns wpa_cli's FAIL output into the line the user sees.
func wifiFailureMessage(out string) string {
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "FAIL" || strings.HasPrefix(t, "FAIL-") {
			return t
		}
	}
	return "wpa_cli reported a failure"
}

// wifiCommandFailed reports whether wpa_cli printed a failure for a command.
// wpa_cli exits 0 even when the supplicant rejects a command, so the output is
// the only reliable signal.
func wifiCommandFailed(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "FAIL" || strings.HasPrefix(t, "FAIL-") {
			return true
		}
	}
	return false
}

// wifiWPA runs one wpa_cli command against the detected interface.
func wifiWPA(args ...string) (string, error) {
	iface := wifiInterface()
	if iface == "" {
		return "", errors.New(wifiUnavailableReason())
	}
	if !wifiToolExists("wpa_cli") {
		return "", errors.New(wifiUnavailableReason())
	}
	full := append([]string{"-i", iface}, args...)
	return wifiRunner(wifiCommandTimeout, "", wifiToolCommand("wpa_cli"), full...)
}

// wifiWPAChecked is wifiWPA that also treats a printed FAIL as an error.
func wifiWPAChecked(args ...string) (string, error) {
	out, err := wifiWPA(args...)
	if err != nil {
		return out, err
	}
	if wifiCommandFailed(out) {
		return out, errors.New(wifiFailureMessage(out))
	}
	return out, nil
}

// ── Parsing ────────────────────────────────────────────────────────────────

// parseWPAStatus reads `wpa_cli status` output. Values may contain '=' (an SSID
// can), so each line is split on the first one only.
func parseWPAStatus(out string) wifiStatus {
	st := wifiStatus{}
	for _, line := range strings.Split(out, "\n") {
		// Only the line ending and the key are trimmed: an SSID may legally
		// start or end with a space, and trimming the value would turn it into
		// a different network.
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "wpa_state":
			st.State = v
		case "ssid":
			st.SSID = v
		case "ip_address":
			st.IP = v
		}
	}
	st.Connected = st.State == wifiCompleteState && st.SSID != ""
	return st
}

// parseWPANetworks reads `wpa_cli list_networks`. The first line is the header;
// a row is id, ssid, bssid, flags. A flags field may be absent on old builds.
func parseWPANetworks(out string) []savedNetwork {
	nets := make([]savedNetwork, 0, 4)
	for i, line := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		n := savedNetwork{ID: id, SSID: fields[1]}
		if len(fields) > 3 {
			n.Flags = fields[3]
		}
		nets = append(nets, n)
	}
	return nets
}

// parseWPAScanResults reads `wpa_cli scan_results`. Rows are tab-separated:
// bssid, frequency, signal level, flags, ssid. Hidden networks (empty SSID) are
// dropped: the picker cannot show a name for them, and offering a blank row the
// user cannot distinguish from another blank row would be worse than omitting
// them.
func parseWPAScanResults(out string) []wifiNetwork {
	nets := make([]wifiNetwork, 0, 16)
	for i, line := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			continue
		}
		ssid := strings.TrimRight(fields[4], "\r\x00")
		if strings.TrimSpace(ssid) == "" {
			continue
		}
		signal, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			continue
		}
		nets = append(nets, wifiNetwork{
			SSID:    ssid,
			BSSID:   strings.TrimSpace(fields[0]),
			Signal:  signal,
			Secured: wifiNeedsPassphrase(fields[3]),
		})
	}
	return nets
}

// wifiNeedsPassphrase classifies the flags column. OWE is deliberately treated
// as open: it needs no shared secret, and the picker must not ask for a
// password that does not exist.
func wifiNeedsPassphrase(flags string) bool {
	f := strings.ToUpper(flags)
	for _, tag := range []string{"WPA", "RSN", "SAE", "PSK", "WEP"} {
		if strings.Contains(f, tag) {
			return true
		}
	}
	return false
}

// mergeWiFiNetworks marks saved and current networks on the scan result and
// keeps one row per SSID (the strongest BSSID). Two rows with the same name
// would be indistinguishable to the user.
func mergeWiFiNetworks(scanned []wifiNetwork, saved []savedNetwork, st wifiStatus) []wifiNetwork {
	bySSID := make(map[string]wifiNetwork, len(scanned))
	for _, n := range scanned {
		if prev, ok := bySSID[n.SSID]; ok && prev.Signal >= n.Signal {
			continue
		}
		bySSID[n.SSID] = n
	}
	for _, s := range saved {
		n, ok := bySSID[s.SSID]
		if !ok || s.SSID == "" {
			continue
		}
		n.Saved = true
		n.SaveID = s.ID
		if s.SSID == st.SSID && st.Connected {
			n.Active = true
		}
		bySSID[s.SSID] = n
	}
	if st.Connected && st.SSID != "" {
		if n, ok := bySSID[st.SSID]; ok {
			n.Active = true
			bySSID[st.SSID] = n
		}
	}
	nets := make([]wifiNetwork, 0, len(bySSID))
	for _, n := range bySSID {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Active != nets[j].Active {
			return nets[i].Active
		}
		if nets[i].Signal != nets[j].Signal {
			return nets[i].Signal > nets[j].Signal
		}
		return nets[i].SSID < nets[j].SSID
	})
	return nets
}

// wifiSignalBars maps dBm to a 1..4 bar reading. 0 is "unknown" and shows one
// bar rather than a full-strength lie.
func wifiSignalBars(rssi int) int {
	if rssi >= 0 {
		// 0 means the scan did not report a level; do not draw full bars.
		return 1
	}
	switch {
	case rssi >= -55:
		return 4
	case rssi >= -67:
		return 3
	case rssi >= -78:
		return 2
	default:
		return 1
	}
}

// wifiRowBars is the bar count for one picker row. The connected row is the one
// link the app can measure directly, so it takes the status meter's reading
// through the same seam the meter uses — /proc/net/wireless, then
// `wpa_cli signal_poll` — instead of the scan's level for our own AP, which can
// be absent, stale, or a driver's 0. That is what makes a /proc-less image show
// real bars on its own network in the picker, not just in the corner meter.
// Every other row keeps the scan's RSSI, and a meter that reports "unknown" or
// "no link" leaves the scan value alone rather than drawing a state the row has
// no room to explain.
func wifiRowBars(n wifiNetwork, meterLevel func() int) int {
	if n.Active && meterLevel != nil {
		if level := meterLevel(); level >= 1 && level <= 4 {
			return level
		}
	}
	return wifiSignalBars(n.Signal)
}

// wifiSignalSample is what `wpa_cli signal_poll` says about the associated
// link. It is the supplicant's own view, so it stays available on images whose
// kernel was built without wireless extensions and therefore has no
// /proc/net/wireless at all.
type wifiSignalSample struct {
	// RSSI is the signal level in dBm: always negative, -50 is strong and
	// below -85 is barely usable.
	RSSI int
	// LinkKbps is the negotiated link rate in kbit/s, 0 when not reported.
	LinkKbps int
	// Noise is the noise floor in dBm when the driver reports it.
	Noise int
	// FreqMHz is the channel centre frequency in MHz.
	FreqMHz int
}

// wifiSignalPoll asks wpa_supplicant for the current signal strength. It errors
// with the reason a caller can show (no interface, no wpa_cli, FAIL, or a
// report without a usable RSSI).
func wifiSignalPoll() (wifiSignalSample, error) {
	out, err := wifiWPA("signal_poll")
	if err != nil {
		return wifiSignalSample{}, err
	}
	if wifiCommandFailed(out) {
		return wifiSignalSample{}, errors.New(wifiFailureMessage(out))
	}
	return parseWPASignalPoll(out)
}

// parseWPASignalPoll reads `wpa_cli signal_poll` output. The report is
// key=value lines (some builds prefix a "Selected interface 'wlan0'" line), and
// a non-negative RSSI is not a measurement: the supplicant prints 0 or -1 when
// it has no number, and drawing bars from that is the lie this parser exists to
// avoid.
func parseWPASignalPoll(out string) (wifiSignalSample, error) {
	var sample wifiSignalSample
	sawRSSI := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "RSSI":
			if n, err := strconv.Atoi(value); err == nil {
				sample.RSSI = n
				sawRSSI = true
			}
		case "LINKSPEED":
			if n, err := strconv.Atoi(value); err == nil {
				sample.LinkKbps = n
			}
		case "NOISE":
			if n, err := strconv.Atoi(value); err == nil {
				sample.Noise = n
			}
		case "FREQUENCY":
			if n, err := strconv.Atoi(value); err == nil {
				sample.FreqMHz = n
			}
		}
	}
	if !sawRSSI || sample.RSSI >= 0 || sample.RSSI < -110 {
		return sample, errors.New("signal_poll reported no usable RSSI")
	}
	return sample, nil
}

// sameWiFiScan reports whether two scans saw the same access points with the
// same strengths. It is how the scan wait tells a fresh result from the stale
// list that is still on the supplicant.
func sameWiFiScan(a, b []wifiNetwork) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(nets []wifiNetwork) []string {
		out := make([]string, 0, len(nets))
		for _, n := range nets {
			out = append(out, n.BSSID+"|"+strconv.Itoa(n.Signal))
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// ── Validation ─────────────────────────────────────────────────────────────

// validateWiFiSSID enforces the 802.11 limits and keeps control characters out
// of the supplicant configuration.
func validateWiFiSSID(ssid string) error {
	if ssid == "" {
		return errors.New("the network name is empty")
	}
	if len(ssid) > wifiMaxSSIDBytes {
		return fmt.Errorf("the network name is longer than %d bytes", wifiMaxSSIDBytes)
	}
	for _, r := range ssid {
		if r < 0x20 || r == 0x7f {
			return errors.New("the network name contains a control character")
		}
	}
	return nil
}

// validateWiFiPassphrase accepts the WPA2 rules: 8..63 characters, or exactly
// 64 hexadecimal digits (the raw-key form).
func validateWiFiPassphrase(pass string) error {
	if pass == "" {
		return errors.New("enter the network password")
	}
	if len(pass) == 64 {
		for _, r := range pass {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return errors.New("a 64-character password must be hexadecimal")
			}
		}
		return nil
	}
	if len(pass) < wifiMinPassphraseLen || len(pass) > wifiMaxPassphraseLen {
		return fmt.Errorf("the password must be %d–%d characters", wifiMinPassphraseLen, wifiMaxPassphraseLen)
	}
	for _, r := range pass {
		if r < 0x20 || r == 0x7f {
			return errors.New("the password contains a control character")
		}
	}
	return nil
}

// wpaQuote renders a value as the double-quoted string wpa_cli expects inside
// set_network. Backslashes and quotes are escaped; validation has already
// rejected newlines, which would end the command.
func wpaQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// ── Reads ──────────────────────────────────────────────────────────────────

// wifiReadStatus returns the current association state.
func wifiReadStatus() (wifiStatus, error) {
	out, err := wifiWPA("status")
	if err != nil {
		return wifiStatus{}, err
	}
	return parseWPAStatus(out), nil
}

// wifiReadSavedNetworks returns the networks wpa_supplicant remembers.
func wifiReadSavedNetworks() ([]savedNetwork, error) {
	out, err := wifiWPA("list_networks")
	if err != nil {
		return nil, err
	}
	return parseWPANetworks(out), nil
}

// wifiReadScanResults returns the last scan without starting a new one.
func wifiReadScanResults() ([]wifiNetwork, error) {
	out, err := wifiWPA("scan_results")
	if err != nil {
		return nil, err
	}
	return parseWPAScanResults(out), nil
}

// wifiScanNetworks asks for a fresh scan and waits until scan_results differs
// from the stale list. A supplicant that answers "OK" but never refreshes is
// common enough that returning the old list silently would be misleading.
func wifiScanNetworks() ([]wifiNetwork, error) {
	before, _ := wifiReadScanResults()
	if _, err := wifiWPAChecked("scan"); err != nil {
		// A soft-blocked radio is the common "Wi-Fi stopped working" state
		// and wpa_cli's only answer is FAIL — name it instead.
		if radioOn, radioOK := wifiRadioState(); radioOK && !radioOn {
			return nil, errors.New("Wi-Fi is turned off")
		}
		return nil, err
	}
	deadline := time.Now().Add(wifiScanWaitTimeout)
	for {
		nets, err := wifiReadScanResults()
		if err == nil && len(nets) > 0 && !sameWiFiScan(before, nets) {
			return nets, nil
		}
		if time.Now().After(deadline) {
			if len(nets) > 0 {
				return nets, nil
			}
			return nil, errors.New("the scan found no networks")
		}
		time.Sleep(wifiScanPoll)
	}
}

// wifiSnapshot is everything the panel needs for one refresh.
type wifiSnapshot struct {
	Status   wifiStatus
	Networks []wifiNetwork
	Saved    []savedNetwork
	RadioOn  bool
	RadioOK  bool
}

// wifiReadSnapshot merges status, saved networks and the last scan. It does not
// trigger a scan, so it is cheap enough to call after a join.
func wifiReadSnapshot() (wifiSnapshot, error) {
	st, err := wifiReadStatus()
	if err != nil {
		return wifiSnapshot{}, err
	}
	saved, _ := wifiReadSavedNetworks()
	scanned, _ := wifiReadScanResults()
	snap := wifiSnapshot{
		Status:   st,
		Saved:    saved,
		Networks: mergeWiFiNetworks(scanned, saved, st),
	}
	snap.RadioOn, snap.RadioOK = wifiRadioState()
	return snap, nil
}

// ── Writes ─────────────────────────────────────────────────────────────────

// wifiNextNetworkID returns the id of the network just added. `add_network`
// prints it, but the number is also the highest id in list_networks; using the
// list avoids depending on the CLI's output format across versions.
func wifiNextNetworkID(addOutput string) (int, error) {
	nets, err := wifiReadSavedNetworks()
	if err == nil && len(nets) > 0 {
		maxID := -1
		for _, n := range nets {
			if n.ID > maxID {
				maxID = n.ID
			}
		}
		if maxID >= 0 {
			return maxID, nil
		}
	}
	// Fallback: the last integer printed by add_network.
	for _, tok := range strings.Fields(addOutput) {
		if id, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil && id >= 0 {
			return id, nil
		}
	}
	return 0, errors.New("could not determine the new network id")
}

// wifiSetPassphrase sends the PSK on stdin. This is the one call that must not
// put the secret in argv: `ps` lists every argument of every process.
func wifiSetPassphrase(id int, pass string) (string, error) {
	iface := wifiInterface()
	if iface == "" {
		return "", errors.New(wifiUnavailableReason())
	}
	script := fmt.Sprintf("set_network %d psk %s\n", id, wpaQuote(pass))
	out, err := wifiRunner(wifiCommandTimeout, script, wifiToolCommand("wpa_cli"), "-i", iface)
	if err != nil {
		return out, err
	}
	if wifiCommandFailed(out) {
		return out, errors.New(wifiFailureMessage(out))
	}
	return out, nil
}

// wifiWaitForAssociation polls until the supplicant reports COMPLETED on the
// SSID that was requested. Checking the SSID matters: a failed join leaves the
// previous network COMPLETED, which would otherwise read as success.
func wifiWaitForAssociation(ssid string, timeout time.Duration) (wifiStatus, error) {
	deadline := time.Now().Add(timeout)
	last := wifiStatus{}
	lastErr := error(nil)
	for {
		st, err := wifiReadStatus()
		if err == nil {
			last = st
			if st.Connected && st.SSID == ssid {
				return st, nil
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(wifiAssociatePoll)
	}
	if last.State == "4WAY_HANDSHAKE" || last.State == "GROUP_HANDSHAKE" {
		return last, fmt.Errorf("could not join %s: the password was rejected", ssid)
	}
	if last.State == "" && lastErr != nil {
		return last, fmt.Errorf("could not join %s: %s", ssid, firstLine(lastErr.Error()))
	}
	state := last.State
	if state == "" {
		state = "no association"
	}
	return last, fmt.Errorf("could not join %s (%s)", ssid, strings.ToLower(state))
}

// wifiObtainAddress asks the firmware's DHCP client for a lease when the
// supplicant did not already get one. It is best-effort: a connected network
// without an address is still worth reporting as connected.
func wifiObtainAddress(iface string) (string, error) {
	if st, err := wifiReadStatus(); err == nil && st.IP != "" {
		return st.IP, nil
	}
	type dhcpClient struct {
		name string
		args []string
	}
	attempts := make([]dhcpClient, 0, 3)
	if wifiToolExists("udhcpc") {
		attempts = append(attempts, dhcpClient{"udhcpc", []string{"-i", iface, "-n", "-q", "-t", "5"}})
	}
	if wifiToolExists("dhcpcd") {
		attempts = append(attempts, dhcpClient{"dhcpcd", []string{"-n", "-t", "8", iface}})
	}
	// dhclient is the lease-seeker of standard distributions; an image that
	// ships only it used to end in "no IP address was assigned".
	if wifiToolExists("dhclient") {
		attempts = append(attempts, dhcpClient{"dhclient", []string{"-1", iface}})
	}
	for _, c := range attempts {
		if _, err := wifiRunner(wifiDHCPTimeout, "", wifiToolCommand(c.name), c.args...); err != nil {
			continue
		}
		if st, err := wifiReadStatus(); err == nil && st.IP != "" {
			return st.IP, nil
		}
	}
	return "", errors.New("no IP address was assigned")
}

// wifiJoin creates a network, optionally sets its PSK, selects it and saves it.
// secured tells key_mgmt what to use, so an open network is not asked for a
// password and a WPA network is never configured as open.
func wifiJoin(ssid, passphrase string, secured bool) (wifiJoinResult, error) {
	res := wifiJoinResult{SSID: ssid}
	if err := validateWiFiSSID(ssid); err != nil {
		return res, err
	}
	if secured {
		if err := validateWiFiPassphrase(passphrase); err != nil {
			return res, err
		}
	}
	if !wifiControlAvailable() {
		return res, errors.New(wifiUnavailableReason())
	}
	// The supplicant may already hold an entry for this SSID with a password
	// the router no longer accepts. Joining must not leave both: the stale
	// entry would win every later lookup. It is remembered here and removed
	// only after the new join works, so a failed attempt keeps the old key.
	preSaved, _ := wifiReadSavedNetworks()
	prevID, hadPrev := wifiSavedID(ssid, preSaved)

	addOut, err := wifiWPAChecked("add_network")
	if err != nil {
		return res, err
	}
	id, err := wifiNextNetworkID(addOut)
	if err != nil {
		return res, err
	}
	if _, err := wifiWPAChecked("set_network", strconv.Itoa(id), "ssid", wpaQuote(ssid)); err != nil {
		return res, err
	}
	if secured {
		if _, err := wifiSetPassphrase(id, passphrase); err != nil {
			return res, err
		}
		// WPA3-personal routers will not join with the default WPA-PSK-only
		// key management, and a router that requires protected management
		// frames will not join without them. Offering SAE alongside WPA-PSK
		// covers transition and WPA3-only networks; optional PMF keeps every
		// WPA2 network working. A supplicant built without either answers
		// FAIL — that is not an error here: the default configuration stays,
		// which is exactly what shipped before.
		_, _ = wifiWPAChecked("set_network", strconv.Itoa(id), "key_mgmt", "WPA-PSK SAE")
		_, _ = wifiWPAChecked("set_network", strconv.Itoa(id), "ieee80211w", "1")
	} else if _, err := wifiWPAChecked("set_network", strconv.Itoa(id), "key_mgmt", "NONE"); err != nil {
		return res, err
	}
	if _, err := wifiWPAChecked("select_network", strconv.Itoa(id)); err != nil {
		return res, err
	}
	// save_config fails when the supplicant runs with update_config=0. The
	// connection still works, so this is a warning rather than a failure.
	saved := false
	if out, err := wifiWPA("save_config"); err != nil || wifiCommandFailed(out) {
		saved = false
	} else {
		saved = true
	}

	st, err := wifiWaitForAssociation(ssid, wifiAssociateTimeout)
	if err != nil {
		// Do not leave a half-configured entry behind: repeated failed attempts
		// would otherwise fill the supplicant's network list with duplicates.
		_, _ = wifiWPA("remove_network", strconv.Itoa(id))
		if saved {
			_, _ = wifiWPA("save_config")
		}
		return res, err
	}
	res.Saved = saved
	warnings := make([]string, 0, 2)
	if !saved {
		warnings = append(warnings, "this network could not be saved for the next boot")
	}
	if ip, ipErr := wifiObtainAddress(wifiInterface()); ipErr == nil {
		res.IP = ip
	} else if st.IP != "" {
		res.IP = st.IP
	} else {
		warnings = append(warnings, "no IP address was assigned; check DHCP on this network")
	}
	// The entry this join replaced goes now that the new one works: removing
	// it earlier would trade a working (if stale) key for a failed attempt.
	if hadPrev && prevID != id {
		if _, err := wifiWPA("remove_network", strconv.Itoa(prevID)); err == nil && saved {
			_, _ = wifiWPA("save_config")
		}
	}
	res.Warn = strings.Join(warnings, "; ")
	return res, nil
}

// wifiSelectSaved reconnects a saved network without asking for its password
// again. The supplicant already holds the PSK.
func wifiSelectSaved(id int) (wifiJoinResult, error) {
	res := wifiJoinResult{Saved: true}
	if !wifiControlAvailable() {
		return res, errors.New(wifiUnavailableReason())
	}
	nets, err := wifiReadSavedNetworks()
	if err != nil {
		return res, err
	}
	ssid := ""
	for _, n := range nets {
		if n.ID == id {
			ssid = n.SSID
			break
		}
	}
	if ssid == "" {
		return res, fmt.Errorf("network %d is no longer saved", id)
	}
	res.SSID = ssid
	if _, err := wifiWPAChecked("select_network", strconv.Itoa(id)); err != nil {
		return res, err
	}
	if _, err := wifiWaitForAssociation(ssid, wifiAssociateTimeout); err != nil {
		return res, err
	}
	if ip, ipErr := wifiObtainAddress(wifiInterface()); ipErr == nil {
		res.IP = ip
	} else {
		res.Warn = "no IP address was assigned; check DHCP on this network"
	}
	return res, nil
}

// wifiDisconnect drops the current association but keeps the saved network.
func wifiDisconnect() error {
	_, err := wifiWPAChecked("disconnect")
	return err
}

// wifiReconnect drops the current association and joins the same network again.
// select_network on its own is a no-op while the supplicant still believes it
// is connected, which is exactly the stale-link case a reconnect is for
// (associated but no address after a suspend, or a router that rebooted), so
// the association is dropped first and the saved key does the joining.
func wifiReconnect() (wifiJoinResult, error) {
	if !wifiControlAvailable() {
		return wifiJoinResult{}, errors.New(wifiUnavailableReason())
	}
	status, err := wifiReadStatus()
	if err != nil {
		return wifiJoinResult{}, err
	}
	if !status.Connected || status.SSID == "" {
		return wifiJoinResult{}, errors.New("not connected to a network")
	}
	saved, err := wifiReadSavedNetworks()
	if err != nil {
		return wifiJoinResult{}, err
	}
	id, ok := wifiSavedID(status.SSID, saved)
	if !ok {
		return wifiJoinResult{}, fmt.Errorf("%s is not saved, so it needs its password again", status.SSID)
	}
	if err := wifiDisconnect(); err != nil {
		return wifiJoinResult{}, err
	}
	// Give the driver a moment to leave the old association; selecting a
	// network while the disconnect is still settling re-associates to the
	// link we just dropped.
	time.Sleep(300 * time.Millisecond)
	return wifiSelectSaved(id)
}

// wifiForget removes a saved network. The caller confirms first; this function
// only refuses when the name is not actually saved.
func wifiForget(ssid string) error {
	nets, err := wifiReadSavedNetworks()
	if err != nil {
		return err
	}
	id := -1
	for _, n := range nets {
		if n.SSID == ssid {
			id = n.ID
			break
		}
	}
	if id < 0 {
		return fmt.Errorf("%s is not saved on this device", ssid)
	}
	if _, err := wifiWPAChecked("remove_network", strconv.Itoa(id)); err != nil {
		return err
	}
	if out, err := wifiWPA("save_config"); err != nil || wifiCommandFailed(out) {
		return errors.New("the network was removed for this session but the saved list could not be rewritten")
	}
	// Removing the network that is currently associated does not drop the
	// connection by itself; the key is gone, so leaving it up would be a lie.
	if st, err := wifiReadStatus(); err == nil && st.SSID == ssid {
		_, _ = wifiWPA("disconnect")
	}
	return nil
}

// wifiSavedID returns the supplicant id for a saved SSID.
func wifiSavedID(ssid string, saved []savedNetwork) (int, bool) {
	for _, n := range saved {
		if n.SSID == ssid {
			return n.ID, true
		}
	}
	return 0, false
}
