package main

// psremote_stream_opts.go — everything JukaHub tells chiaki-stream besides the
// pairing keys and the picture size.
//
// The session file used to say only which console and how big. It now carries
// the whole stream setup (native/README.md lists the keys): the console's name
// for the helper's on-screen display, the video codec, the PS login passcode
// when the user saved one, what Select sends, whether the D-pad drives the left
// stick (the Brick has no sticks), the rumble motor, and where the helper
// reports how the stream ended — psremote_lastrun.go reads that report on the
// next start. Every key is optional on the helper's side, so a helper that
// predates one simply ignores it.
//
// It also picks the address. A console paired at home keeps its LAN address;
// one with an internet address (a router port forward, or the console's VPN
// address) is streamed from there when the home address does not answer a
// discovery probe. That is the whole of "does it work on another network"
// today: Sony's PSN relay, which needs no port forward, is not part of this
// client.

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// Video codec settings. Auto is H.265 where the CPU decodes it in
	// software with room to spare (the Smart Pro S) and H.264 everywhere
	// else; a PS4 only sends H.264.
	psCodecAuto = "auto"
	psCodecH264 = "h264"
	psCodecH265 = "h265"

	// What Select sends in a stream; Menu + Select sends the other one.
	psSelectTouchpad = "touchpad"
	psSelectCreate   = "create"

	// psLastRunFileName is the helper's report beside psremote.json.
	psLastRunFileName = "psremote-last.txt"

	// psStreamProbeTimeout bounds the home-network probe made before falling
	// back to a console's internet address. A console on the LAN answers in a
	// few milliseconds, in rest mode as well.
	psStreamProbeTimeout = 700 * time.Millisecond

	// psStreamNameMax keeps a console name (it comes off the network) to what
	// the helper's display has room for.
	psStreamNameMax = 40
)

// psStreamOptions is what the session file carries beyond psSessionFile.
type psStreamOptions struct {
	Name         string
	Codec        string // psCodecH264 or psCodecH265, already resolved
	LoginPIN     string
	SelectButton string // psSelectTouchpad or psSelectCreate
	DpadStick    bool
	Rumble       bool
	Motor        psStreamMotor
	StatusFile   string
	Hints        bool
	Remote       bool
}

// psStreamMotor is the vibration motor JukaHub's haptics found, in the
// helper's terms: the GPIO that switches it and the node that sets its
// strength, with that node's range.
type psStreamMotor struct {
	GPIO     string
	Level    string
	LevelMin int
	LevelMax int
}

var (
	// psStreamProbe asks whether a console answers discovery at an address;
	// psStreamMotorLookup finds the motor. Both are variables so tests stay off
	// the network and sysfs.
	psStreamProbe       = psStreamProbeLAN
	psStreamMotorLookup = psStreamMotorFromHaptics
)

// psPrepareStream is the stream's setup: the address to use (home or
// internet) and the options the session file carries. It is called on the
// event thread right before the hand-off; the only wait in it is the home
// probe, and only for a console that has an internet address.
func psPrepareStream(c psConsole) (psConsole, psStreamOptions) {
	host, remote := psChooseStreamHost(c)
	if host != "" {
		c.Host = host
	}
	// The address goes into the session file's host= line like every other
	// value: a hand-edited psremote.json must not be able to start a line.
	c.Host = psSessionValue(c.Host)
	opts := psStreamOptionsFor(c, loadPSHosts().Settings, DetectDevice(), remote)
	opts.Motor = psStreamMotorLookup()
	opts.StatusFile = psLastRunPath()
	return c, opts
}

// psStreamOptionsFor turns the stored settings and one console into the
// stream's options. Pure: the motor and the report path are filled in by
// psPrepareStream.
func psStreamOptionsFor(c psConsole, s psRemoteSettings, device DeviceModel, remote bool) psStreamOptions {
	return psStreamOptions{
		Name:         psStreamName(c),
		Codec:        psResolveCodec(s.Codec, psConsoleIsPS5(c), device),
		LoginPIN:     psNormalizeLoginPIN(c.LoginPIN),
		SelectButton: psResolveSelectButton(s.SelectButton),
		DpadStick:    psDpadStickDefault(device),
		Rumble:       !s.NoRumble,
		Hints:        true,
		Remote:       remote,
	}
}

// psSessionExtras renders the options as session-file lines. Values pass
// through psSessionValue, so nothing in them — a console name is whatever the
// network said — can start a line of its own.
func psSessionExtras(o psStreamOptions) string {
	var b strings.Builder
	line := func(key, value string) {
		value = psSessionValue(value)
		if value == "" {
			return
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	line("name", o.Name)
	line("codec", o.Codec)
	if pin := psNormalizeLoginPIN(o.LoginPIN); psLoginPINValid(pin) {
		line("login_pin", pin)
	}
	line("select_button", o.SelectButton)
	line("dpad_stick", psBit(o.DpadStick))
	line("rumble", psBit(o.Rumble))
	if o.Motor.GPIO != "" {
		line("motor_gpio", o.Motor.GPIO)
		if o.Motor.Level != "" && o.Motor.LevelMax > o.Motor.LevelMin {
			line("motor_level", o.Motor.Level)
			line("motor_level_min", strconv.Itoa(o.Motor.LevelMin))
			line("motor_level_max", strconv.Itoa(o.Motor.LevelMax))
		}
	}
	line("status_file", o.StatusFile)
	line("hints", psBit(o.Hints))
	line("remote", psBit(o.Remote))
	return b.String()
}

// psSessionValue makes one value safe for a key=value line: control
// characters (a newline above all) become spaces, and the ends are trimmed.
func psSessionValue(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, value))
}

func psBit(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// psStreamName is the console's name for the helper's display: what the
// console calls itself, or PS5/PS4 when it never said.
func psStreamName(c psConsole) string {
	name := psSessionValue(c.Name)
	if name == "" {
		if psConsoleIsPS5(c) {
			return "PS5"
		}
		return "PS4"
	}
	runes := []rune(name)
	if len(runes) > psStreamNameMax {
		// ASCII: the helper's display font has no "…".
		name = strings.TrimSpace(string(runes[:psStreamNameMax-3])) + "..."
	}
	return name
}

// psNormalizeCodec reads a stored or typed codec name: "h264" and "h265" in
// their usual spellings, anything else is auto.
func psNormalizeCodec(setting string) string {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "h264", "h.264", "avc", "x264":
		return psCodecH264
	case "h265", "h.265", "hevc", "x265":
		return psCodecH265
	}
	return psCodecAuto
}

// psResolveCodec is the codec a stream asks for. A PS4 only sends H.264.
// Auto is H.265 on the Smart Pro S, whose eight cores decode it with room to
// spare, and H.264 on the Smart Pro, Brick, Brick Pro and Hammer, where H.265
// in software is the heavier load at the same picture size.
func psResolveCodec(setting string, ps5 bool, device DeviceModel) string {
	if !ps5 {
		return psCodecH264
	}
	switch psNormalizeCodec(setting) {
	case psCodecH264:
		return psCodecH264
	case psCodecH265:
		return psCodecH265
	}
	if device == DeviceTrimuiSmartProS {
		return psCodecH265
	}
	return psCodecH264
}

// psCodecLabel is a codec as the panel prints it.
func psCodecLabel(codec string) string {
	if codec == psCodecH265 {
		return "H.265"
	}
	return "H.264"
}

// psCodecRowStatus is the Video codec row's value: the setting, and for Auto
// what it means on this handheld for a PS5.
func psCodecRowStatus(setting string, device DeviceModel) string {
	switch psNormalizeCodec(setting) {
	case psCodecH264:
		return "H.264"
	case psCodecH265:
		return "H.265"
	}
	return "Auto (" + psCodecLabel(psResolveCodec(psCodecAuto, true, device)) + ")"
}

// psNextCodec cycles Auto → H.264 → H.265 → Auto (delta -1 goes back).
func psNextCodec(setting string, delta int) string {
	order := []string{psCodecAuto, psCodecH264, psCodecH265}
	cur := 0
	for i, c := range order {
		if c == psNormalizeCodec(setting) {
			cur = i
			break
		}
	}
	next := (cur + delta%len(order) + len(order)) % len(order)
	return order[next]
}

// psResolveSelectButton is what Select sends: the touchpad click unless the
// user chose Create.
func psResolveSelectButton(setting string) string {
	if strings.EqualFold(strings.TrimSpace(setting), psSelectCreate) {
		return psSelectCreate
	}
	return psSelectTouchpad
}

// psSelectRowStatus and psSelectRowDetail describe the Select button row.
func psSelectRowStatus(setting string) string {
	if psResolveSelectButton(setting) == psSelectCreate {
		return "Create"
	}
	return "Touchpad"
}

func psSelectRowDetail(setting string) string {
	if psResolveSelectButton(setting) == psSelectCreate {
		return "Select = Create · Menu + Select = touchpad click · Menu = PS button"
	}
	return "Select = touchpad click · Menu + Select = Create · Menu = PS button"
}

// psDpadStickDefault reports whether a stream starts with the D-pad driving
// the left stick: on the Brick and the Hammer, which have no sticks at all
// (their F1/F2 keys are the stick clicks). Menu + L3 toggles it in a stream.
func psDpadStickDefault(device DeviceModel) bool {
	return device == DeviceTrimuiBrick || device == DeviceTrimuiHammer
}

// ── Address: home or internet ───────────────────────────────────────────

// psChooseStreamHost picks the address a stream connects to. Without an
// internet address it is the paired (home) address, as it always was. With
// one, the home address gets a short discovery probe first: a console that
// answers — awake or in rest mode — is on this network; one that does not is
// reached over the internet.
func psChooseStreamHost(c psConsole) (host string, remote bool) {
	lan := strings.TrimSpace(c.Host)
	internet := strings.TrimSpace(c.RemoteHost)
	if internet == "" || strings.EqualFold(internet, lan) {
		return lan, false
	}
	if lan == "" {
		return internet, true
	}
	if psStreamProbe(lan, psConsoleIsPS5(c), psStreamProbeTimeout) {
		return lan, false
	}
	logInfo("psremote", "console not on this network; streaming to its internet address")
	return internet, true
}

// psStreamProbeLAN sends one discovery request straight to host and reports
// whether a discovery answer came back before the timeout. A refused port (an
// ICMP reply from some other machine at that address) ends it early.
func psStreamProbeLAN(host string, ps5 bool, timeout time.Duration) bool {
	port, version := psRemotePortPS4, psProtoVersionPS4
	if ps5 {
		port, version = psRemotePortPS5, psProtoVersionPS5
	}
	return psStreamProbeAt(net.JoinHostPort(host, strconv.Itoa(port)), version, timeout)
}

// psStreamProbeAt is psStreamProbeLAN for an explicit host:port, so a test can
// answer on a local port.
func psStreamProbeAt(address, version string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = psStreamProbeTimeout
	}
	deadline := time.Now().Add(timeout)
	// The dialer's timeout covers a host-name lookup too.
	conn, err := (&net.Dialer{Timeout: timeout}).Dial("udp", address)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(psDiscoveryPacket(version)); err != nil {
		return false
	}
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		if err != nil {
			return false
		}
		if _, ok := psParseDiscoveryResponse(buf[:n], conn.RemoteAddr()); ok {
			return true
		}
	}
	return false
}

// ── Motor, report path, helper permissions ──────────────────────────────

// psStreamMotorFromHaptics hands the stream the motor JukaHub vibrates with:
// the one haptics.go already found, or a fresh look for it (same detector,
// same Brick Pro ceiling). Off the handheld there is none.
func psStreamMotorFromHaptics() psStreamMotor {
	if !psRemoteSupported() {
		return psStreamMotor{}
	}
	hapticsMu.Lock()
	dev := hapticDev
	hapticsMu.Unlock()
	motor, _ := dev.(*sysfsMotor)
	if motor == nil {
		motor = detectSysfsMotor("/")
		capMotorForDevice(motor, DetectDevice())
	}
	if motor == nil {
		return psStreamMotor{}
	}
	return psStreamMotor{GPIO: motor.gpio, Level: motor.levelPath, LevelMin: motor.levelMin, LevelMax: motor.levelMax}
}

// psLastRunPathOverride replaces psLastRunPath in tests.
var psLastRunPathOverride string

// psLastRunPath is where the helper writes how the stream ended: beside
// psremote.json, so it survives JukaHub's restart after the stream.
func psLastRunPath() string {
	if p := strings.TrimSpace(psLastRunPathOverride); p != "" {
		return p
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, psLastRunFileName)
	}
	return filepath.Join(os.TempDir(), psLastRunFileName)
}

// psEnsureExecutable gives a helper back its execute bits when an archive or
// a copy dropped them. Best effort: on a FAT card chmod does nothing and the
// mount's own mode decides, and any real problem is the one exec reports.
func psEnsureExecutable(path string) {
	if strings.TrimSpace(path) == "" || runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if info.Mode().Perm()&0o111 == 0o111 {
		return
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o755); err != nil {
		logWarn("psremote", "could not make %s executable: %v", filepath.Base(path), err)
	}
}

// ── Stored stream settings, for the panel ───────────────────────────────

// psStreamSettingsCache keeps the panel's rows off the disk: chiakiRows runs
// every frame, and these three settings change only through the setters here.
// The panel fills it as it opens (psStreamSettingsSet, from the psremote.json
// it reads anyway) and every save here refreshes it.
var psStreamSettingsCache struct {
	mu     sync.Mutex
	loaded bool
	s      psRemoteSettings
}

// psPanelStreamSettings returns the stored codec, Select and rumble settings
// from the cache. It reads psremote.json only when nothing filled the cache
// yet, which an open panel always has.
func psPanelStreamSettings() psRemoteSettings {
	psStreamSettingsCache.mu.Lock()
	defer psStreamSettingsCache.mu.Unlock()
	if !psStreamSettingsCache.loaded {
		psStreamSettingsCache.s = loadPSHosts().Settings
		psStreamSettingsCache.loaded = true
	}
	return psStreamSettingsCache.s
}

// psStreamSettingsSet fills the cache from settings just read from disk.
func psStreamSettingsSet(s psRemoteSettings) {
	psStreamSettingsCache.mu.Lock()
	psStreamSettingsCache.s, psStreamSettingsCache.loaded = s, true
	psStreamSettingsCache.mu.Unlock()
}

// psSaveStreamSettings applies change to the stored settings and saves them.
// It reads the file first, so the panel's own fields (quality, volume, the
// Account-ID) are kept as they are on disk. A save that fails changes nothing:
// the cache keeps what the file still says, so the row and the next stream
// agree, and the error goes back to the caller to say so.
func psSaveStreamSettings(change func(*psRemoteSettings)) (psRemoteSettings, error) {
	// One load-change-save under psHostsMu, like every writer of psremote.json:
	// a pairing saved in between is not written over (psremote.go).
	psHostsMu.Lock()
	state := loadPSHosts()
	before := state.Settings
	change(&state.Settings)
	err := savePSHostsLocked(state)
	psHostsMu.Unlock()
	if err != nil {
		logWarn("psremote", "saving stream settings failed: %v", err)
		psStreamSettingsSet(before)
		return before, err
	}
	psStreamSettingsSet(state.Settings)
	return state.Settings, nil
}

// ── Login passcode ──────────────────────────────────────────────────────

// psNormalizeLoginPIN drops the spaces and dashes around a typed passcode.
func psNormalizeLoginPIN(pin string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' {
			return -1
		}
		return r
	}, pin)
}

// psLoginPINValid accepts what the console asks for: exactly four digits.
func psLoginPINValid(pin string) bool {
	if len(pin) != 4 {
		return false
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return false
		}
	}
	return true
}
