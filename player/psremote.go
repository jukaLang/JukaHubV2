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
//       width, height, fps, bitrate, volume — the file is deleted by the helper.
//   chiaki-regist INPUT_FILE RESULT_FILE     (PS5: host, pin, account_id)
//       result: target, rp_key_type, regist_key, rp_key, server_mac (hex).
//
// PS4 pairing is pure protocol and lives in Go (psremote_ps4.go); PS5 pairing
// needs the console's PSN Account-ID and stays in the native regist helper.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	raw, err := os.ReadFile(psHostsPath())
	if err != nil {
		return state
	}
	var parsed psHostsFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		log.Printf("[psremote] ignoring %s: %v", psHostsPath(), err)
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

func savePSHosts(state psHostsFile) error {
	path := psHostsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// upsertPSConsole merges a discovered or freshly paired console into the file.
// Credentials are never overwritten by discovery: a scan only fills in the name,
// target and last-seen time of a console that is already paired.
func upsertPSConsole(update psConsole) psHostsFile {
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
	if err := savePSHosts(state); err != nil {
		log.Printf("[psremote] saving hosts failed: %v", err)
	}
	return state
}

func removePSConsole(host string) psHostsFile {
	state := loadPSHosts()
	kept := state.Hosts[:0]
	for _, c := range state.Hosts {
		if c.Host != host {
			kept = append(kept, c)
		}
	}
	state.Hosts = kept
	if err := savePSHosts(state); err != nil {
		log.Printf("[psremote] saving hosts failed: %v", err)
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

// psRPKeyFromHex converts the helper's hex output into the base64 form the
// session file needs.
func psRPKeyFromHex(value string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("rp_key is not hex: %w", err)
	}
	if len(raw) != 16 {
		return "", fmt.Errorf("rp_key is %d bytes, expected 16", len(raw))
	}
	return base64.StdEncoding.EncodeToString(raw), nil
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
// Apps/JukaHub/bin; there is no dependency on a separately installed Chiaki-ng
// app, and the legacy port folder is deliberately not searched.
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
	var roots []string
	if dir := psBinDir(); dir != "" {
		roots = append(roots, dir)
	}
	for _, root := range roots {
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

	quality := psQualityPresets[0]
	if settings.Quality >= 0 && settings.Quality < len(psQualityPresets) {
		quality = psQualityPresets[settings.Quality]
	}

	path, err := psWriteSessionFile(c, quality, settings.Volume)
	if err != nil {
		return err
	}
	if err := psStartHandoff(engine, path, c.label()); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// psWriteSessionFile writes the helper's input through a 0600 temp file.
func psWriteSessionFile(c psConsole, q psQuality, volume int) (string, error) {
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
	if _, err := f.WriteString(psSessionFile(c, q, volume)); err != nil {
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

// psStartHandoff writes and starts the detached handoff script.
func psStartHandoff(engine psEngine, sessionPath, label string) error {
	if strings.TrimSpace(engine.Stream) == "" {
		return errors.New("no streaming helper")
	}
	path := psHandoffPath()
	script := psHandoffScript(os.Getpid(), engine, sessionPath, label)
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
// the script stops the firmware UI, runs the helper, then restores the menu.
func psHandoffScript(pid int, engine psEngine, sessionPath, label string) string {
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
	b.WriteString(`
# Wait for JukaHub to release SDL and the framebuffer (60 s cap).
i=0
while kill -0 "$JUKAHUB_PID" 2>/dev/null; do
    i=$((i+1))
    [ "$i" -ge 600 ] && break
    sleep 0.1
done

sleep 3
if command -v pkill >/dev/null 2>&1; then
    pkill -STOP runtrimui.sh 2>/dev/null
    pkill -STOP trimui_launcher 2>/dev/null
fi
UI_PID="$(pidof MainUI 2>/dev/null)"
if [ -n "$UI_PID" ]; then
    kill "$UI_PID" 2>/dev/null
    sleep 1
    UI_PID="$(pidof MainUI 2>/dev/null)"
    [ -n "$UI_PID" ] && kill -9 "$UI_PID" 2>/dev/null
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

if command -v pkill >/dev/null 2>&1; then
    pkill -CONT runtrimui.sh 2>/dev/null
    pkill -CONT trimui_launcher 2>/dev/null
fi
sleep 2
if [ -z "$(pidof MainUI 2>/dev/null)" ]; then
    for UI in /usr/trimui/bin/MainUI /usr/bin/MainUI /usr/local/bin/MainUI \
        /mnt/SDCARD/System/usr/trimui/bin/MainUI
    do
        if [ -x "$UI" ]; then
            (nohup "$UI" >/dev/null 2>&1 &)
            break
        fi
    done
fi

rm -f "$0" 2>/dev/null
exit 0
`)
	return b.String()
}
