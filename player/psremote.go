package main

// psremote.go — JukaHub's own PS Remote Play client.
//
// The protocol work stays native: chiaki-ng's libchiaki does discovery,
// registration crypto, H.264/HEVC decode, Opus audio and input, and this
// handheld has neither Vulkan nor the memory for the Qt client upstream ships.
// What JukaHub owns is everything above it — scanning, pairing, the console
// list, quality, logs and the launch. That is what removes the Python port (and
// its Vietnamese-only menus) from the flow: the only pieces kept from it are the
// two native helpers, `chiaki-stream` and `chiaki-regist`, which JukaHub drives
// itself through files that are documented here.
//
//   chiaki-stream SESSION_FILE
//       host, regist_key, rp_key, ps5, target, rp_version, verbose,
//       width, height, fps, bitrate, volume — the file is deleted by the helper —
//       plus the optional stream keys of psremote_stream_opts.go (name, codec,
//       login_pin, select_button, dpad_stick, rumble, motor_*, status_file,
//       hints, remote); the helper reports how the stream ended in status_file.
//   chiaki-regist INPUT_FILE RESULT_FILE     (PS5: host, pin, account_id)
//       result: target, rp_key_type, regist_key, rp_key, server_mac (hex).
//
// PS4 pairing is pure protocol and lives in Go (psremote_ps4.go); PS5 pairing
// needs the console's PSN Account-ID and stays in the native regist helper.

import (
	"encoding/base64"
	"encoding/json"
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

// ChiakiTarget values. A PS5 target is >= 1000000; anything lower is a PS4
// firmware generation.
const (
	psTargetPS4FW8    = 800
	psTargetPS4FW9    = 900
	psTargetPS4FW10   = 1000
	psTargetPS5       = 1000000
	psTargetPS5_1     = 1000100
	psRemoteFileVer   = 1
	psRemoteMaxHosts  = 16
	psRemotePortPS4   = 987
	psRemotePortPS5   = 9302
	psProtoVersionPS4 = "00020020"
	psProtoVersionPS5 = "00030010"
)

// psConsole is one console JukaHub knows about: discovered, paired, or both.
type psConsole struct {
	// Host is the LAN address and the key the list is merged on.
	Host string `json:"host"`
	Name string `json:"name,omitempty"`
	// Target is the chiaki protocol target the console answered with.
	Target int  `json:"target,omitempty"`
	PS5    bool `json:"ps5,omitempty"`
	// RegistKey and RPKey are the credentials a stream needs. They are only
	// written by a successful pairing, never by discovery.
	RegistKey string    `json:"regist_key,omitempty"`
	RPKey     string    `json:"rp_key,omitempty"`
	RPKeyType int       `json:"rp_key_type,omitempty"`
	ServerMAC string    `json:"server_mac,omitempty"`
	PairedAt  time.Time `json:"paired_at,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	// RemoteHost is the console's address from outside its network (a router
	// port forward or a VPN address). A stream uses it when Host does not
	// answer (psremote_stream_opts.go); discovery never sets it.
	RemoteHost string `json:"remote_host,omitempty"`
	// LoginPIN is the 4-digit PS login passcode the user saved, entered by
	// the helper when the console asks for it.
	LoginPIN string `json:"login_pin,omitempty"`
}

func (c psConsole) paired() bool {
	return c.RegistKey != "" && c.RPKey != ""
}

// label is what the panel prints for the console.
func (c psConsole) label() string {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = strings.TrimSpace(c.Host)
	}
	kind := "PS4"
	if c.PS5 || c.Target >= psTargetPS5 {
		kind = "PS5"
	}
	return name + " · " + kind
}

// psQuality is the stream profile the helper is started with. The presets are
// the same ones the handhelds were tuned with: the Smart Pro S decodes 720p30
// comfortably, a Brick Pro is happiest at 540p30.
type psQuality struct {
	Label   string
	Width   int
	Height  int
	FPS     int
	Bitrate int
}

var psQualityPresets = []psQuality{
	{Label: "720p30", Width: 1280, Height: 720, FPS: 30, Bitrate: 8000},
	{Label: "540p30", Width: 960, Height: 540, FPS: 30, Bitrate: 4000},
	{Label: "540p30 eco", Width: 960, Height: 540, FPS: 30, Bitrate: 3000},
	{Label: "720p60", Width: 1280, Height: 720, FPS: 60, Bitrate: 12000},
	{Label: "1080p30", Width: 1920, Height: 1080, FPS: 30, Bitrate: 15000},
}

// psRemoteSettings is the small amount of state that is not per console.
type psRemoteSettings struct {
	Quality int    `json:"quality"`
	Volume  int    `json:"volume"`
	Account string `json:"psn_account_id,omitempty"`
	// Codec is "auto", "h264" or "h265" (psResolveCodec); empty reads as auto.
	Codec string `json:"codec,omitempty"`
	// SelectButton is what Select sends in a stream: "touchpad" (the default)
	// or "create"; Menu + Select sends the other.
	SelectButton string `json:"select_button,omitempty"`
	// NoRumble turns game rumble off; absent means on.
	NoRumble bool `json:"no_rumble,omitempty"`
}

type psHostsFile struct {
	Version  int              `json:"version"`
	Settings psRemoteSettings `json:"settings"`
	Hosts    []psConsole      `json:"hosts"`
}

// ── Storage ──────────────────────────────────────────────────────────────

var (
	psHostsPathOverride    string
	psEngineStreamOverride string
	psEngineRegistOverride string
)

func psHostsPath() string {
	if p := strings.TrimSpace(psHostsPathOverride); p != "" {
		return p
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "psremote.json")
	}
	return "psremote.json"
}

func loadPSHosts() psHostsFile {
	state := psHostsFile{Version: psRemoteFileVer}
	state.Settings.Quality = 0
	state.Settings.Volume = 80
	var parsed psHostsFile
	// The pairing keys are not regenerable without the console in hand, so a
	// damaged psremote.json comes back from the .bak the atomic save leaves
	// beside it rather than reading as "no consoles paired".
	if _, err := readJSONWithBackup(psHostsPath(), &parsed); err != nil {
		return state
	}
	state = parsed
	state.Version = psRemoteFileVer
	if state.Settings.Volume <= 0 || state.Settings.Volume > 100 {
		state.Settings.Volume = 80
	}
	if state.Settings.Quality < 0 || state.Settings.Quality >= len(psQualityPresets) {
		state.Settings.Quality = 0
	}
	// A half-written credential is worse than no console, so drop anything that
	// cannot stream and let discovery offer it again.
	hosts := state.Hosts[:0]
	for _, c := range state.Hosts {
		if strings.TrimSpace(c.Host) == "" {
			continue
		}
		c.RegistKey = strings.TrimSpace(c.RegistKey)
		c.RPKey = strings.TrimSpace(c.RPKey)
		if !c.paired() {
			c.RegistKey, c.RPKey = "", ""
		}
		hosts = append(hosts, c)
	}
	state.Hosts = hosts
	return state
}

// psHostsMu serialises the writers of psremote.json. AtomicWrite stages every
// save under the one name psremote.json.tmp, so two saves at once (the pairing
// goroutine, a scan following a console to its new address, the panel) could
// rename a half-written file into place; and a load-change-save that holds it
// (upsertPSConsole, removePSConsole, psRehomeStored) cannot lose a pairing or
// undo a Forget that another writer saved in between.
var psHostsMu sync.Mutex

func savePSHosts(state psHostsFile) error {
	psHostsMu.Lock()
	defer psHostsMu.Unlock()
	return savePSHostsLocked(state)
}

// savePSHostsLocked is savePSHosts for a caller that holds psHostsMu.
func savePSHostsLocked(state psHostsFile) error {
	path := psHostsPath()
	state.Version = psRemoteFileVer
	if len(state.Hosts) > psRemoteMaxHosts {
		state.Hosts = state.Hosts[len(state.Hosts)-psRemoteMaxHosts:]
	}
	sort.SliceStable(state.Hosts, func(i, j int) bool {
		return state.Hosts[i].LastSeen.After(state.Hosts[j].LastSeen)
	})
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// 0600: this file holds the console's registration key. The atomic write
	// applies the mode itself and keeps the previous pairing as .bak.
	return AtomicWrite(path, append(data, '\n'), 0o600)
}

// upsertPSConsole merges a discovered or freshly paired console into the file.
// Credentials are never overwritten by discovery: a scan only fills in the name,
// target and last-seen time of a console that is already paired.
func upsertPSConsole(update psConsole) psHostsFile {
	psHostsMu.Lock()
	defer psHostsMu.Unlock()
	state := loadPSHosts()
	idx := -1
	for i, c := range state.Hosts {
		if c.Host == update.Host {
			idx = i
			break
		}
	}
	if idx < 0 {
		if !update.paired() {
			// Unpaired discoveries are not persisted; the panel lists them from
			// the scan result so a stale entry cannot look like a console.
			return state
		}
		state.Hosts = append(state.Hosts, update)
	} else {
		merged := state.Hosts[idx]
		if update.Name != "" {
			merged.Name = update.Name
		}
		if update.Target != 0 {
			merged.Target = update.Target
		}
		merged.PS5 = merged.PS5 || update.PS5
		if update.paired() {
			merged.RegistKey = update.RegistKey
			merged.RPKey = update.RPKey
			merged.RPKeyType = update.RPKeyType
			merged.ServerMAC = update.ServerMAC
			merged.PairedAt = update.PairedAt
		}
		if !update.LastSeen.IsZero() {
			merged.LastSeen = update.LastSeen
		}
		state.Hosts[idx] = merged
	}
	if err := savePSHostsLocked(state); err != nil {
		logWarn("psremote", "saving hosts failed: %v", err)
	}
	return state
}

func removePSConsole(host string) psHostsFile {
	psHostsMu.Lock()
	defer psHostsMu.Unlock()
	state := loadPSHosts()
	kept := state.Hosts[:0]
	for _, c := range state.Hosts {
		if c.Host != host {
			kept = append(kept, c)
		}
	}
	state.Hosts = kept
	if err := savePSHostsLocked(state); err != nil {
		logWarn("psremote", "saving hosts failed: %v", err)
	}
	return state
}

// ── Credentials ──────────────────────────────────────────────────────────

// psRegistKeyValid accepts only what the helper can use: 1-8 hex characters.
// Anything longer is a hex dump of the key rather than the key, which the
// stream helper rejects with "invalid or incomplete session file".
func psRegistKeyValid(key string) bool {
	if len(key) < 1 || len(key) > 8 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// psRPKeyValid reports whether rp_key is base64 for exactly 16 bytes, which is
// the session key the console derives ("morning" in chiaki's terms).
func psRPKeyValid(key string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	return err == nil && len(raw) == 16
}

// psRPVersion is the RP-Version string for a target: the PS4 path sent it with
// registration, the PS5 handshake announces 1.0.
func psRPVersion(target int) string {
	switch {
	case target == psTargetPS4FW8:
		return "8.0"
	case target == psTargetPS4FW9:
		return "9.0"
	case target >= psTargetPS5:
		return "1.0"
	default:
		return "10.0"
	}
}

// psTargetFromSystemVersion mirrors chiaki's discovery mapping, so the panel
// labels and the session file agree with libchiaki.
func psTargetFromSystemVersion(systemVersion string, ps5 bool) int {
	version, _ := strconv.Atoi(strings.TrimSpace(systemVersion))
	switch {
	case version >= 8050001 && ps5:
		return psTargetPS5_1
	case version >= 8050000 && ps5:
		return psTargetPS5
	case version >= 8000000:
		return psTargetPS4FW10
	case version >= 7000000:
		return psTargetPS4FW9
	case version > 0:
		return psTargetPS4FW8
	}
	return 0
}

// psSessionFile renders the key=value file chiaki-stream consumes. Field order
// and names follow the helper's parser; unknown keys are ignored by it, so the
// extra rp_version is harmless on builds that predate it.
func psSessionFile(c psConsole, q psQuality, volume int) string {
	if volume <= 0 {
		volume = 80
	}
	if volume > 100 {
		volume = 100
	}
	target := c.Target
	if target == 0 {
		if c.PS5 {
			target = psTargetPS5_1
		} else {
			target = psTargetPS4FW10
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "host=%s\n", c.Host)
	fmt.Fprintf(&b, "regist_key=%s\n", c.RegistKey)
	fmt.Fprintf(&b, "rp_key=%s\n", c.RPKey)
	ps5 := 0
	if c.PS5 || target >= psTargetPS5 {
		ps5 = 1
	}
	fmt.Fprintf(&b, "ps5=%d\n", ps5)
	fmt.Fprintf(&b, "target=%d\n", target)
	fmt.Fprintf(&b, "rp_version=%s\n", psRPVersion(target))
	fmt.Fprintf(&b, "verbose=0\n")
	fmt.Fprintf(&b, "width=%d\n", q.Width)
	fmt.Fprintf(&b, "height=%d\n", q.Height)
	fmt.Fprintf(&b, "fps=%d\n", q.FPS)
	fmt.Fprintf(&b, "bitrate=%d\n", q.Bitrate)
	fmt.Fprintf(&b, "volume=%d\n", volume)
	return b.String()
}

// ── Engine ───────────────────────────────────────────────────────────────

// psEngine is the native half of the client: the two helper binaries and the
// folder that owns their runtime libraries.
type psEngine struct {
	Stream string
	Regist string
	Root   string
}

// installed reports whether streaming is possible. Registration has its own
// fallback (PS4 pairs in Go), so it is reported separately.
func (e psEngine) installed() bool { return e.Stream != "" }

// psRemoteEngine finds the helpers. They are JukaHub's own build in
// Apps/JukaHub/bin (bin/ beside the binary, where an update and the helper
// installer put them: psHelperDirs); there is no dependency on a separately
// installed Chiaki-ng app, and the legacy port folder is deliberately not
// searched.
func psRemoteEngine() psEngine {
	if path := strings.TrimSpace(os.Getenv("JUKAHUB_CHIAKI_STREAM")); path != "" {
		engine := psEngine{Stream: path, Regist: strings.TrimSpace(os.Getenv("JUKAHUB_CHIAKI_REGIST")), Root: filepath.Dir(path)}
		if !regularFile(engine.Regist) {
			engine.Regist = ""
		}
		return engine
	}
	if path := strings.TrimSpace(psEngineStreamOverride); path != "" {
		engine := psEngine{Stream: path, Regist: strings.TrimSpace(psEngineRegistOverride), Root: filepath.Dir(path)}
		if !regularFile(engine.Regist) {
			engine.Regist = ""
		}
		return engine
	}
	for _, root := range psHelperDirs() {
		stream := filepath.Join(root, "chiaki-stream")
		if !regularFile(stream) {
			continue
		}
		engine := psEngine{Stream: stream, Root: root}
		regist := filepath.Join(root, "chiaki-regist")
		if regularFile(regist) {
			engine.Regist = regist
		}
		return engine
	}
	return psEngine{}
}

// psEngineRuntimeDir is the folder whose libraries the helper must load. The
// Brick Pro needs its bundled FFmpeg/OpenSSL; the Smart Pro uses the firmware's.
func psEngineRuntimeDir(engine psEngine) string {
	roots := []string{engine.Root, filepath.Dir(engine.Root)}
	if sd := psRemoteSDRootDir(); sd != "" {
		roots = append(roots, filepath.Join(sd, "Apps", "JukaHub"))
	}
	for _, root := range roots {
		candidates := []string{
			filepath.Join(root, "libs", "brick-stock"),
			filepath.Join(root, "libs"),
		}
		for _, dir := range candidates {
			if !psRemoteUsesBrickRuntime() {
				break
			}
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return dir
			}
		}
	}
	return ""
}

// ── Launch ───────────────────────────────────────────────────────────────

// psSessionDir is where the short-lived session file is written. It is tmpfs,
// so the credentials never survive a reboot and cannot accumulate.
func psSessionDir() string {
	if dir := os.TempDir(); dir != "" {
		return dir
	}
	return "/tmp"
}

// psRemoteStream starts the native client for a paired console. Like the old
// install path, the screen is handed over: JukaHub releases SDL and exits, the
// script waits for that, stops the firmware UI and runs the helper.
func psRemoteStream(c psConsole, settings psRemoteSettings) error {
	if !c.paired() {
		return errors.New("pair this console first")
	}
	if !psRegistKeyValid(c.RegistKey) {
		return errors.New("the saved pairing key is invalid — pair again")
	}
	if !psRPKeyValid(c.RPKey) {
		return errors.New("the saved session key is invalid — pair again")
	}
	engine := psRemoteEngine()
	if !engine.installed() {
		return errors.New("the streaming helper is missing (see docs/REMOTE_PLAY.md)")
	}
	if !psRemoteSupported() {
		return errors.New("Remote Play runs on the TrimUI Linux build")
	}
	// A helper this firmware's loader would refuse is reported now, while
	// JukaHub is still on screen, not after it has quit (psremote_preflight.go).
	// The execute bits go on first: without them the check cannot start it.
	psEnsureExecutable(engine.Stream)
	if problem := psStreamHelperProblem(engine); problem != "" {
		return errors.New(problem)
	}

	quality := psQualityPresets[0]
	if settings.Quality >= 0 && settings.Quality < len(psQualityPresets) {
		quality = psQualityPresets[settings.Quality]
	}

	// The address (home, or the console's internet address when home does not
	// answer) and everything the helper is told beyond the keys and the
	// picture: psremote_stream_opts.go.
	c, opts := psPrepareStream(c)
	path, err := psWriteSessionFile(c, quality, settings.Volume, psSessionExtras(opts))
	if err != nil {
		return err
	}
	// The helper replaces this note with how the stream ended, and the next
	// start of JukaHub reads it (psremote_lastrun.go).
	psMarkStreamStarted(opts.StatusFile, c.Host)
	if err := psStartHandoff(engine, path, c.label()); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(opts.StatusFile)
		return err
	}
	return nil
}

// psWriteSessionFile writes the helper's input through a 0600 temp file:
// psSessionFile's keys, then extras (psSessionExtras).
func psWriteSessionFile(c psConsole, q psQuality, volume int, extras string) (string, error) {
	f, err := os.CreateTemp(psSessionDir(), "jukahub-ps-*.conf")
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := f.WriteString(psSessionFile(c, q, volume) + extras); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// psHandoffPath is on tmpfs so it never survives a reboot.
func psHandoffPath() string {
	return filepath.Join(os.TempDir(), "jukahub-psremote-handoff.sh")
}

// The screen-handoff protocol with launch.sh (see the header of that script).
// A launch.sh that speaks it exports JUKAHUB_SCREEN_HANDOFF=1 — the running
// launcher, not the file on disk, is what has to wait, and an update can put a
// newer launch.sh on the card under an older one that is still running. When
// it is there, JukaHub leaves the helper's pid in .screen-handoff beside the
// binary as it quits; launch.sh then keeps the firmware UI stopped until that
// pid is gone and starts JukaHub again, so the helper itself never touches
// MainUI or the supervisors. That is what keeps MainUI from coming back over
// the stream and the user from landing in the stock menu when it ends.
const (
	psScreenHandoffEnv    = "JUKAHUB_SCREEN_HANDOFF"
	psScreenHandoffMarker = ".screen-handoff"
)

// psLauncherWaitsForHandoff reports whether this run's launcher waits for a
// screen helper.
func psLauncherWaitsForHandoff() bool {
	return strings.TrimSpace(os.Getenv(psScreenHandoffEnv)) == "1" && launchedByDeviceLauncher()
}

// psScreenHandoffMarkerPath is the marker beside the binary, where launch.sh
// looks for it ("" when the binary's folder is unknown).
func psScreenHandoffMarkerPath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), psScreenHandoffMarker)
}

// psStartHandoff writes and starts the detached handoff script.
func psStartHandoff(engine psEngine, sessionPath, label string) error {
	if strings.TrimSpace(engine.Stream) == "" {
		return errors.New("no streaming helper")
	}
	marker := ""
	if psLauncherWaitsForHandoff() {
		marker = psScreenHandoffMarkerPath()
	}
	path := psHandoffPath()
	script := psHandoffScript(os.Getpid(), engine, sessionPath, label, marker != "")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return fmt.Errorf("write handoff script: %w", err)
	}
	cmd := exec.Command("sh", path)
	cmd.Dir = filepath.Dir(engine.Stream)
	configureChiakiDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("start handoff: %w", err)
	}
	if marker != "" {
		// The script was written for a launcher that waits; without the marker
		// nobody would keep MainUI away from the stream, so a marker that cannot
		// be written stops the hand-off instead of half-doing it.
		pid := []byte(strconv.Itoa(cmd.Process.Pid) + "\n")
		if err := os.WriteFile(marker, pid, 0o644); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = os.Remove(path)
			return fmt.Errorf("hand the screen over: %w", err)
		}
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// psLogPath is where the helper's diagnostics land.
func psLogPath() string {
	if sd := psRemoteSDRootDir(); sd != "" {
		return filepath.Join(sd, "Apps", "JukaHub", "psremote.log")
	}
	return filepath.Join(os.TempDir(), "psremote.log")
}

// psHandoffScript hands the display to `chiaki-stream SESSION`: JukaHub exits,
// the script runs the helper. With launcherWaits the launcher keeps the
// firmware UI stopped and brings JukaHub back afterwards (the screen-handoff
// protocol above), so the script only makes sure no MainUI is up. Without it
// (an older launch.sh) the script stops the firmware UI itself and restores the
// menu when the stream ends.
func psHandoffScript(pid int, engine psEngine, sessionPath, label string, launcherWaits bool) string {
	runtime := psEngineRuntimeDir(engine)
	logPath := psLogPath()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by JukaHub — display handoff to the native Remote Play client. Do not edit.\n")
	fmt.Fprintf(&b, "JUKAHUB_PID=%d\n", pid)
	fmt.Fprintf(&b, "BIN=%s\n", psShellQuote(engine.Stream))
	fmt.Fprintf(&b, "SESSION=%s\n", psShellQuote(sessionPath))
	fmt.Fprintf(&b, "LOG=%s\n", psShellQuote(logPath))
	fmt.Fprintf(&b, "LABEL=%s\n", psShellQuote(label))
	if runtime != "" {
		fmt.Fprintf(&b, "RUNTIME=%s\n", psShellQuote(runtime))
	} else {
		b.WriteString("RUNTIME=\"\"\n")
	}
	for _, kv := range psHandoffEnv(engine.Root) {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", kv[:i], psShellQuote(kv[i+1:]))
	}
	if launcherWaits {
		b.WriteString("LAUNCHER_WAITS=1\n")
	} else {
		b.WriteString("LAUNCHER_WAITS=0\n")
	}
	b.WriteString(`
# Wait for JukaHub to release SDL and the framebuffer (60 s cap).
i=0
while kill -0 "$JUKAHUB_PID" 2>/dev/null; do
    i=$((i+1))
    [ "$i" -ge 600 ] && break
    sleep 0.1
done

if [ "$LAUNCHER_WAITS" = "1" ]; then
    # launch.sh keeps the supervisors paused and MainUI stopped until this
    # script ends, then starts JukaHub again: only the settle is needed here.
    sleep 1
else
    # An older launch.sh hands the screen back as JukaHub exits; take it again.
    sleep 3
    if command -v pkill >/dev/null 2>&1; then
        pkill -STOP runtrimui.sh 2>/dev/null
        pkill -STOP trimui_launcher 2>/dev/null
    fi
fi
# Never two programs on the framebuffer. pidof can name several MainUIs, so the
# list is passed unquoted: one argument per pid.
UI_PID="$(pidof MainUI 2>/dev/null)"
if [ -n "$UI_PID" ]; then
    kill $UI_PID 2>/dev/null
    sleep 1
    UI_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$UI_PID" ] && kill -9 $UI_PID 2>/dev/null
fi
sleep 1

if [ -n "$RUNTIME" ]; then
    if [ -n "${LD_LIBRARY_PATH:-}" ]; then
        LD_LIBRARY_PATH="${LD_LIBRARY_PATH%:}:$RUNTIME"
    else
        LD_LIBRARY_PATH="$RUNTIME"
    fi
    export LD_LIBRARY_PATH
    OPENSSL_PRELOAD="$RUNTIME/libcrypto.so.1.1:$RUNTIME/libssl.so.1.1"
    export OPENSSL_PRELOAD
fi

{
    echo ""
    echo "=== JukaHub Remote Play $(date 2>/dev/null) ==="
} >> "$LOG" 2>/dev/null
chmod +x "$BIN" 2>/dev/null || true
"$BIN" "$SESSION" >> "$LOG" 2>&1
RC=$?
echo "=== $LABEL exited with code $RC ===" >> "$LOG" 2>/dev/null
rm -f "$SESSION" 2>/dev/null

if [ "$LAUNCHER_WAITS" != "1" ]; then
    if command -v pkill >/dev/null 2>&1; then
        pkill -CONT runtrimui.sh 2>/dev/null
        pkill -CONT trimui_launcher 2>/dev/null
    fi
    # The supervisor's MainUI was the one stopped above, and the CONT lets it
    # start a new one. Starting another here as well put two MainUIs on the
    # framebuffer (the glitchy screen), so one is started only if none has
    # appeared after ten seconds.
    n=0
    while [ "$n" -lt 10 ] && [ -z "$(pidof MainUI 2>/dev/null)" ]; do
        n=$((n + 1))
        sleep 1
    done
    if [ -z "$(pidof MainUI 2>/dev/null)" ]; then
        for UI in /usr/trimui/bin/MainUI /usr/bin/MainUI /usr/local/bin/MainUI \
            /mnt/SDCARD/System/usr/trimui/bin/MainUI
        do
            if [ -x "$UI" ]; then
                (cd "$(dirname "$UI")" 2>/dev/null; nohup "$UI" >/dev/null 2>&1 &)
                break
            fi
        done
    fi
fi

rm -f "$0" 2>/dev/null
exit 0
`)
	return b.String()
}
