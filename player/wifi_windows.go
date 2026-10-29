package main

// wifi_windows.go — the Windows Wi-Fi backend behind the network picker.
//
// wifi.go drives a handheld's radio through wpa_supplicant: wpa_cli, a control
// socket, /sys/class/net and /proc/net/wireless. None of that exists on
// Windows, so every probe there answered "No wireless network interface on this
// device" and the picker could neither list a network nor join one. Windows
// exposes the same operations through netsh, and this file is that
// translation.
//
// The rules are wifi.go's, adapted to netsh:
//
//   - the only binary is netsh, which ships with Windows. No path or config
//     value is ever used as a command name;
//   - the passphrase never reaches a command line. A join writes a WLAN profile
//     XML to a private temp file, adds it with netsh, and deletes it again;
//     only the SSID, which is not a secret, is passed as an argument;
//   - netsh prints its labels in the system language, so no decision here
//     depends on an English word when the output can be judged structurally:
//     a BSSID line means associated, a percentage means signal, and an SSID
//     line starts a network. The English labels are used where they are the
//     only source, with the structural reading as the fallback.
//
// Every parser is pure, so the Windows backend is testable on a machine that
// has no radio and no netsh at all.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// wifiPOSIXForced pins the wpa_supplicant backend. It is true everywhere but
// Windows, and the test binary sets it on Windows too: the suite stubs wpa_cli
// output and must never be answered by the real netsh on the machine running
// it. Tests that cover this file call the winWifi* functions directly, or flip
// the flag to prove the dispatch.
var wifiPOSIXForced = runtime.GOOS != "windows"

// winWifiActive reports whether the picker should talk to netsh instead of
// wpa_supplicant.
func winWifiActive() bool { return !wifiPOSIXForced }

// netshIface is one interface block of `netsh wlan show interfaces`.
type netshIface struct {
	Name      string
	SSID      string
	State     string // the raw value, in whatever language Windows printed
	BSSID     string
	Signal    int     // percent, 0..100; 0 when Windows reported none
	RxMbps    float64 // receive rate, 0 when not reported
	Connected bool
}

// ── Running netsh ──────────────────────────────────────────────────────────

// winWifiRun runs one netsh wlan command through the same stubbable runner the
// supplicant backend uses, so a test can answer it with captured output.
func winWifiRun(args ...string) (string, error) {
	return wifiRunner(wifiCommandTimeout, "", "netsh", args...)
}

// winWifiError turns netsh's own words into the error the panel shows. netsh
// prints the reason and still exits non-zero, so the first line of output is a
// better message than "exit status 1".
func winWifiError(out string, err error) error {
	if line := wifiFirstOutputLine(out); line != "" {
		return errors.New(line)
	}
	if err != nil {
		return fmt.Errorf("netsh did not answer (%w)", err)
	}
	return nil
}

// ── Parsers ────────────────────────────────────────────────────────────────

// parseNetshInterfaces reads `netsh wlan show interfaces`.
//
// Blocks are separated by a blank line, which is structural: the labels inside
// them are localized, so the only English the parser insists on is "name" (the
// adapter's name, which Windows prints untranslated in practice) and, when it
// is there, "state". Being associated is decided by the BSSID line, which no
// locale translates, so a German or Japanese Windows still reads as connected.
func parseNetshInterfaces(out string) []netshIface {
	var ifaces []netshIface
	for _, block := range strings.Split(toLF(out), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			label, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			label = strings.ToLower(strings.TrimSpace(label))
			if label == "" {
				continue
			}
			fields[label] = strings.TrimSpace(value)
		}
		// The header ("There is 1 interface on the system:") and the trailing
		// hosted-network block are their own blocks; neither names an adapter.
		name := fields["name"]
		state := fields["state"]
		bssid := fields["bssid"]
		if name == "" || (state == "" && bssid == "") {
			continue
		}
		iface := netshIface{
			Name:      name,
			SSID:      fields["ssid"],
			State:     state,
			BSSID:     bssid,
			Connected: strings.EqualFold(state, "connected") || bssid != "",
		}
		for label, value := range fields {
			switch {
			case iface.Signal == 0 && strings.HasSuffix(value, "%"):
				if pct, err := strconv.Atoi(strings.TrimSuffix(value, "%")); err == nil {
					iface.Signal = pct
				}
			case iface.RxMbps == 0 && strings.HasPrefix(label, "receive rate"):
				iface.RxMbps = leadingFloat(value)
			}
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces
}

// parseNetshProfiles reads the profile names out of `netsh wlan show profiles`.
// The list holds a group-policy section and a user section that can name the
// same profile twice, so the order is kept and the duplicates dropped.
func parseNetshProfiles(out string) []string {
	seen := map[string]bool{}
	names := make([]string, 0, 8)
	for _, line := range strings.Split(toLF(out), "\n") {
		_, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.TrimSpace(value)
		// "<None>" is what the group-policy section prints when it is empty.
		if name == "" || strings.HasPrefix(name, "<") || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// parseNetshNetworks reads `netsh wlan show networks mode=bssid` into the picker
// rows. One row per SSID is kept, the strongest BSSID winning, because two rows
// with the same name would be indistinguishable on screen. Hidden networks
// (an SSID line with no name) are dropped for the same reason wifi.go drops
// them.
func parseNetshNetworks(out string) []wifiNetwork {
	// One entry is being assembled at a time. Its BSSIDs are kept apart because
	// a network with two access points prints two signal lines, and the row has
	// to report the strength and the address of the *same* radio.
	type accessPoint struct {
		bssid  string
		signal int
	}
	var (
		ssid    string
		auth    string
		enc     string
		aps     []accessPoint
		current = -1
	)
	nets := make([]wifiNetwork, 0, 16)
	index := map[string]int{}

	flush := func() {
		defer func() { ssid, auth, enc, aps, current = "", "", "", nil, -1 }()
		if ssid == "" {
			return
		}
		n := wifiNetwork{SSID: ssid, Secured: netshSecured(auth, enc)}
		for _, ap := range aps {
			// Signal is dBm: the larger (less negative) value is stronger.
			if n.BSSID == "" || ap.signal > n.Signal {
				n.BSSID, n.Signal = ap.bssid, ap.signal
			}
		}
		if i, ok := index[n.SSID]; ok {
			if n.Signal > nets[i].Signal {
				nets[i] = n
			}
			return
		}
		index[n.SSID] = len(nets)
		nets = append(nets, n)
	}

	for _, line := range strings.Split(toLF(out), "\n") {
		label, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(label))
		value = strings.TrimSpace(value)
		switch {
		case isNetshSSIDLabel(key):
			flush()
			ssid = value
		case ssid == "":
			// Rows before the first SSID line are the scan header.
		case strings.HasPrefix(key, "bssid"):
			aps = append(aps, accessPoint{bssid: value})
			current = len(aps) - 1
		case key == "authentication":
			auth = value
		case key == "encryption":
			enc = value
		case current >= 0 && aps[current].signal == 0 && strings.HasSuffix(value, "%"):
			if pct, err := strconv.Atoi(strings.TrimSuffix(value, "%")); err == nil {
				aps[current].signal = winSignalDBM(pct)
			}
		}
	}
	flush()
	return nets
}

// isNetshSSIDLabel recognises the line that starts a network. netsh numbers the
// rows ("SSID 1 : Home"); the count is dropped before the comparison so a
// network preceded by more than nine others still parses.
func isNetshSSIDLabel(key string) bool {
	if key == "ssid" {
		return true
	}
	if !strings.HasPrefix(key, "ssid ") {
		return false
	}
	for _, r := range strings.TrimPrefix(key, "ssid ") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// netshSecured classifies a scan row from its authentication and encryption
// columns. An unknown authentication is treated as secured: asking for a
// password that turns out to be unnecessary is a recoverable annoyance, while
// configuring a WPA network as open silently fails to join.
func netshSecured(auth, enc string) bool {
	a := strings.ToLower(auth)
	e := strings.ToLower(enc)
	if a == "" {
		return true
	}
	if strings.Contains(a, "open") {
		return false
	}
	if e == "" || strings.Contains(e, "none") {
		return false
	}
	return true
}

// winSignalDBM converts Windows' 0..100% signal quality onto the dBm scale the
// picker and the status meter already speak. Windows reports a quality, not a
// level; the conventional linear mapping is -100 dBm at 0% and -50 dBm at 100%.
// 0 stays 0, which both readers treat as "no measurement" rather than as the
// weakest possible link.
func winSignalDBM(pct int) int {
	if pct <= 0 {
		return 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct/2 - 100
}

// toLF normalises netsh's CRLF output so every parser splits one way.
func toLF(out string) string { return strings.ReplaceAll(out, "\r\n", "\n") }

// leadingFloat reads the number at the start of a netsh value such as
// "144.4 Mbps".
func leadingFloat(value string) float64 {
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.' || value[end] == ',') {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(value[:end], ",", "."), 64)
	if err != nil {
		return 0
	}
	return n
}

// ── Interface discovery ────────────────────────────────────────────────────

// The adapter answer is cached like the supplicant's: wifiControlAvailable is
// read while the status bar draws, and spawning netsh per frame would cost a
// frame. A negative answer is remembered too, or a desktop without a radio
// would pay for a process every frame forever.
var (
	winWifiIfaceMu    sync.Mutex
	winWifiIfaceVal   netshIface
	winWifiIfaceFound bool
	winWifiIfaceAt    time.Time
	winWifiIfaceKnown bool
)

// winWifiProbeInterfaces asks netsh which wireless adapters this machine has.
func winWifiProbeInterfaces() []netshIface {
	out, err := winWifiRun("wlan", "show", "interfaces")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	return parseNetshInterfaces(out)
}

// winWifiIfaceCached is the first adapter, remembered for wifiDrivenTTL.
func winWifiIfaceCached() (netshIface, bool) {
	winWifiIfaceMu.Lock()
	defer winWifiIfaceMu.Unlock()
	if winWifiIfaceKnown && wifiDrivenNow().Sub(winWifiIfaceAt) < wifiDrivenTTL {
		return winWifiIfaceVal, winWifiIfaceFound
	}
	ifaces := winWifiProbeInterfaces()
	winWifiIfaceVal, winWifiIfaceFound = netshIface{}, false
	if len(ifaces) > 0 {
		winWifiIfaceVal, winWifiIfaceFound = ifaces[0], true
	}
	winWifiIfaceAt = wifiDrivenNow()
	winWifiIfaceKnown = true
	return winWifiIfaceVal, winWifiIfaceFound
}

// resetWinWifiIfaceCache forgets the adapter answer, so the next lookup asks
// Windows again. Opening the picker calls it, which is when a person may have
// plugged a dongle in.
func resetWinWifiIfaceCache() {
	winWifiIfaceMu.Lock()
	winWifiIfaceVal = netshIface{}
	winWifiIfaceFound = false
	winWifiIfaceAt = time.Time{}
	winWifiIfaceKnown = false
	winWifiIfaceMu.Unlock()
}

// winWifiInterface is the adapter name wifiInterface returns on Windows.
func winWifiInterface() string {
	iface, ok := winWifiIfaceCached()
	if !ok {
		return ""
	}
	return iface.Name
}

// winWifiControlAvailable reports whether netsh can drive a radio here.
func winWifiControlAvailable() bool {
	if !wifiToolExists("netsh") {
		return false
	}
	_, ok := winWifiIfaceCached()
	return ok
}

// winWifiUnavailableReason explains an unavailable backend in one line.
func winWifiUnavailableReason() string {
	switch {
	case !wifiToolExists("netsh"):
		return "Wi-Fi control on Windows needs netsh"
	case winWifiInterface() == "":
		return "Windows reports no wireless network interface"
	}
	return ""
}

// ── Reads ──────────────────────────────────────────────────────────────────

// winWifiStatus reads the current association. Windows owns DHCP, so an address
// is read from the adapter itself rather than requested from a lease client.
func winWifiStatus() (wifiStatus, error) {
	iface, ok := winWifiIfaceCached()
	if !ok {
		return wifiStatus{}, errors.New(winWifiUnavailableReason())
	}
	// The cached block names the adapter; the association is read fresh, since
	// this is what a join waits on and a minute-old answer would never change.
	ifaces := winWifiProbeInterfaces()
	if len(ifaces) > 0 {
		iface = ifaces[0]
	}
	st := wifiStatus{
		State:     winWifiStateWord(iface),
		SSID:      iface.SSID,
		Connected: iface.Connected,
	}
	if st.Connected {
		st.IP = winWifiIPv4(iface.Name)
	}
	return st, nil
}

// winWifiStateWord maps Windows' state onto the supplicant words the panel and
// the join wait already understand. "authenticating" stays close to the
// suppressor's handshake state so a timed-out join can still say the password
// was rejected rather than blaming the radio.
func winWifiStateWord(iface netshIface) string {
	switch {
	case iface.Connected:
		return wifiCompleteState
	case strings.Contains(strings.ToLower(iface.State), "authentic"):
		return "4WAY_HANDSHAKE"
	}
	return "INACTIVE"
}

// winWifiIPv4 is the adapter's first non-loopback IPv4 address, or "" while
// Windows is still acquiring one.
func winWifiIPv4(name string) string {
	if name == "" {
		return ""
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() {
				return v4.String()
			}
		}
	}
	return ""
}

// winWifiSavedNetworks lists the profiles Windows remembers. Profiles have no
// numeric id, so the position in the list is the id the picker passes back;
// wifiSelectSaved resolves it again before using it.
func winWifiSavedNetworks() ([]savedNetwork, error) {
	out, err := winWifiRun("wlan", "show", "profiles")
	if err != nil {
		return nil, winWifiError(out, err)
	}
	names := parseNetshProfiles(out)
	nets := make([]savedNetwork, 0, len(names))
	for i, name := range names {
		nets = append(nets, savedNetwork{ID: i, SSID: name})
	}
	return nets, nil
}

// winWifiScanResults lists the networks Windows has seen.
func winWifiScanResults() ([]wifiNetwork, error) {
	out, err := winWifiRun("wlan", "show", "networks", "mode=bssid")
	if err != nil {
		return nil, winWifiError(out, err)
	}
	nets := parseNetshNetworks(out)
	if len(nets) == 0 {
		return nil, errors.New("the scan found no networks")
	}
	return nets, nil
}

// winWifiScanNetworks is the scan the picker's refresh asks for. wpa_cli needs
// a scan request and a wait because it only reports what it was told to look
// for; Windows keeps its own list current while the radio is on, so reading it
// is the scan. Waiting here would only make the panel slower to open on a
// machine that has nothing in range.
func winWifiScanNetworks() ([]wifiNetwork, error) {
	return winWifiScanResults()
}

// winWifiSignalPoll reads the link's strength. Windows reports a quality
// percentage and a receive rate, which is all the status meter needs.
func winWifiSignalPoll() (wifiSignalSample, error) {
	ifaces := winWifiProbeInterfaces()
	if len(ifaces) == 0 {
		return wifiSignalSample{}, errors.New(winWifiUnavailableReason())
	}
	iface := ifaces[0]
	if !iface.Connected {
		return wifiSignalSample{}, errors.New("not connected to a network")
	}
	rssi := winSignalDBM(iface.Signal)
	if rssi >= 0 {
		return wifiSignalSample{}, errors.New("Windows reported no signal quality")
	}
	return wifiSignalSample{
		RSSI:     rssi,
		LinkKbps: int(iface.RxMbps * 1000),
	}, nil
}

// winWifiRadioState reports the radio. netsh can show whether the radio is on
// but cannot turn it on — airplane mode and the hardware switch belong to
// Windows — so the panel is told there is nothing for it to toggle.
func winWifiRadioState() (on bool, known bool) {
	if _, ok := winWifiIfaceCached(); !ok {
		return false, false
	}
	return true, false
}

// winWifiLinkUp reports whether Windows has an adapter associated, which is the
// meter's last-resort answer on a platform with no /proc/net/wireless.
func winWifiLinkUp() bool {
	for _, iface := range winWifiProbeInterfaces() {
		if iface.Connected {
			return true
		}
	}
	return false
}

// winWifiReadSnapshot is everything the panel needs for one refresh.
func winWifiReadSnapshot() (wifiSnapshot, error) {
	st, err := winWifiStatus()
	if err != nil {
		return wifiSnapshot{}, err
	}
	saved, _ := winWifiSavedNetworks()
	scanned, _ := winWifiScanResults()
	snap := wifiSnapshot{
		Status:   st,
		Saved:    saved,
		Networks: mergeWiFiNetworks(scanned, saved, st),
	}
	snap.RadioOn, snap.RadioOK = winWifiRadioState()
	return snap, nil
}

// ── Writes ─────────────────────────────────────────────────────────────────

// winWifiProfileXML builds the WLAN profile netsh imports. The XML encoder does
// the escaping, so an SSID with an ampersand and a key with a `<` in it survive
// intact instead of breaking the profile.
func winWifiProfileXML(ssid, passphrase string, secured bool) ([]byte, error) {
	auth, enc := "open", "none"
	if secured {
		auth, enc = "WPA2PSK", "AES"
	}
	doc := winWlanProfile{
		XMLNS:          "http://www.microsoft.com/networking/WLAN/profile/v1",
		Name:           ssid,
		SSIDConfig:     winWlanSSIDConfig{SSID: winWlanSSID{Name: ssid}},
		ConnectionType: "ESS",
		ConnectionMode: "auto",
		MSM: winWlanMSM{Security: winWlanSecurity{
			AuthEncryption: winWlanAuthEncryption{
				Authentication: auth,
				Encryption:     enc,
				UseOneX:        "false",
			},
		}},
	}
	if secured {
		doc.MSM.Security.SharedKey = &winWlanSharedKey{
			KeyType:     "passPhrase",
			Protected:   "false",
			KeyMaterial: passphrase,
		}
	}
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("could not build the Wi-Fi profile: %w", err)
	}
	return append([]byte(xml.Header), data...), nil
}

// winWlanProfile is the profile XML netsh wlan add profile expects.
type winWlanProfile struct {
	XMLName        xml.Name          `xml:"WLANProfile"`
	XMLNS          string            `xml:"xmlns,attr"`
	Name           string            `xml:"name"`
	SSIDConfig     winWlanSSIDConfig `xml:"SSIDConfig"`
	ConnectionType string            `xml:"connectionType"`
	ConnectionMode string            `xml:"connectionMode"`
	MSM            winWlanMSM        `xml:"MSM"`
}

type winWlanSSIDConfig struct {
	SSID winWlanSSID `xml:"SSID"`
}

type winWlanSSID struct {
	Name string `xml:"name"`
}

type winWlanMSM struct {
	Security winWlanSecurity `xml:"security"`
}

type winWlanSecurity struct {
	AuthEncryption winWlanAuthEncryption `xml:"authEncryption"`
	SharedKey      *winWlanSharedKey     `xml:"sharedKey,omitempty"`
}

type winWlanAuthEncryption struct {
	Authentication string `xml:"authentication"`
	Encryption     string `xml:"encryption"`
	UseOneX        string `xml:"useOneX"`
}

type winWlanSharedKey struct {
	KeyType     string `xml:"keyType"`
	Protected   string `xml:"protected"`
	KeyMaterial string `xml:"keyMaterial"`
}

// winWifiWriteProfile stores the profile in a private temp file for the length
// of one netsh call. The file holds the key, so it is created 0600 and removed
// by the caller as soon as netsh has read it.
func winWifiWriteProfile(data []byte) (string, error) {
	f, err := os.CreateTemp("", "jukahub-wlan-*.xml")
	if err != nil {
		return "", fmt.Errorf("could not stage the Wi-Fi profile: %w", err)
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("could not protect the Wi-Fi profile: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("could not write the Wi-Fi profile: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("could not write the Wi-Fi profile: %w", err)
	}
	return name, nil
}

// winWifiJoin adds the profile and connects to it. Windows keeps the profile
// once it is added, so a successful join is always a saved one.
func winWifiJoin(ssid, passphrase string, secured bool) (wifiJoinResult, error) {
	res := wifiJoinResult{SSID: ssid}
	if err := validateWiFiSSID(ssid); err != nil {
		return res, err
	}
	if secured {
		if err := validateWiFiPassphrase(passphrase); err != nil {
			return res, err
		}
	}
	if reason := winWifiUnavailableReason(); reason != "" {
		return res, errors.New(reason)
	}
	profile, err := winWifiProfileXML(ssid, passphrase, secured)
	if err != nil {
		return res, err
	}
	path, err := winWifiWriteProfile(profile)
	if err != nil {
		return res, err
	}
	defer os.Remove(path)

	if out, err := winWifiRun("wlan", "add", "profile", "filename="+path, "user=current"); err != nil {
		return res, winWifiError(out, err)
	}
	if out, err := winWifiRun("wlan", "connect", "name="+ssid); err != nil {
		return res, winWifiError(out, err)
	}
	st, err := wifiWaitForAssociation(ssid, wifiAssociateTimeout)
	if err != nil {
		return res, winWifiJoinError(ssid, err)
	}
	res.Saved = true
	res.IP = st.IP
	if res.IP == "" {
		res.Warn = "no IP address yet; Windows may still be acquiring one"
	}
	// The adapter answer changed with the association; the cache is stale.
	resetWinWifiIfaceCache()
	return res, nil
}

// winWifiJoinError explains a join that Windows never completed. The supplicant
// backend can tell a rejected key from an out-of-range network and says so; on
// Windows both look like a link that never comes up, so the message names both
// possibilities instead of blaming one.
func winWifiJoinError(ssid string, err error) error {
	if strings.Contains(err.Error(), "password was rejected") {
		return fmt.Errorf("could not join %s: the password was rejected", ssid)
	}
	return fmt.Errorf("could not join %s: Windows did not associate — check the password and that the network is in range", ssid)
}

// winWifiSelectSaved reconnects a saved profile without asking for its key.
func winWifiSelectSaved(id int) (wifiJoinResult, error) {
	res := wifiJoinResult{Saved: true}
	nets, err := winWifiSavedNetworks()
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
	if out, err := winWifiRun("wlan", "connect", "name="+ssid); err != nil {
		return res, winWifiError(out, err)
	}
	st, err := wifiWaitForAssociation(ssid, wifiAssociateTimeout)
	if err != nil {
		return res, winWifiJoinError(ssid, err)
	}
	res.IP = st.IP
	if res.IP == "" {
		res.Warn = "no IP address yet; Windows may still be acquiring one"
	}
	resetWinWifiIfaceCache()
	return res, nil
}

// winWifiDisconnect drops the association but keeps the profile.
func winWifiDisconnect() error {
	out, err := winWifiRun("wlan", "disconnect")
	if err != nil {
		return winWifiError(out, err)
	}
	resetWinWifiIfaceCache()
	return nil
}

// winWifiReconnect reconnects the network that is up now. The disconnect comes
// first for the same reason as in wifi.go: Windows answers "already connected"
// while it still believes in a stale link, which is the case a reconnect is for.
func winWifiReconnect() (wifiJoinResult, error) {
	status, err := winWifiStatus()
	if err != nil {
		return wifiJoinResult{}, err
	}
	if !status.Connected || status.SSID == "" {
		return wifiJoinResult{}, errors.New("not connected to a network")
	}
	ssid := status.SSID
	if out, err := winWifiRun("wlan", "disconnect"); err != nil {
		return wifiJoinResult{}, winWifiError(out, err)
	}
	// Give the driver a moment to leave the old association; connecting while
	// the disconnect is still settling re-associates to the link just dropped.
	time.Sleep(300 * time.Millisecond)
	if out, err := winWifiRun("wlan", "connect", "name="+ssid); err != nil {
		return wifiJoinResult{}, winWifiError(out, err)
	}
	st, err := wifiWaitForAssociation(ssid, wifiAssociateTimeout)
	if err != nil {
		return wifiJoinResult{}, winWifiJoinError(ssid, err)
	}
	resetWinWifiIfaceCache()
	res := wifiJoinResult{SSID: ssid, Saved: true, IP: st.IP}
	if res.IP == "" {
		res.Warn = "no IP address yet; Windows may still be acquiring one"
	}
	return res, nil
}

// winWifiForget deletes a saved profile. Its key goes with it, so a connection
// that is still up is dropped rather than left looking saved.
func winWifiForget(ssid string) error {
	saved, err := winWifiSavedNetworks()
	if err != nil {
		return err
	}
	found := false
	for _, n := range saved {
		if n.SSID == ssid {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s is not saved on this device", ssid)
	}
	if out, err := winWifiRun("wlan", "delete", "profile", "name="+ssid); err != nil {
		return winWifiError(out, err)
	}
	if st, err := winWifiStatus(); err == nil && st.SSID == ssid {
		_, _ = winWifiRun("wlan", "disconnect")
	}
	resetWinWifiIfaceCache()
	return nil
}
