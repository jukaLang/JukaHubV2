package main

// spatial_games.go — spatial audio in RetroArch games.
//
// JukaHub's own media is spatialised inside the player (spatial.go,
// spatial_dsp.go). A game is a separate process with its own audio, so for
// games the same DSP ships as a small native library, player/spatialfx: one
// .so that is both a RetroArch audio DSP filter and a LADSPA plugin, built by
// CI into the handheld package as Apps/JukaHub/lib/jukahub-spatial.so.
//
// RetroArch loads DSP filters itself: audio_dsp_plugin names a .dsp preset,
// and RetroArch loads the filter libraries from that preset's own folder. So
// at each RetroArch launch from the library, with "Spatial audio in games" on
// (spatialSettings.Games) and a mode in force, prepareSpatialAudio:
//
//  1. finds the library (JUKAHUB_SPATIAL_PLUGIN, else lib/ beside JukaHub);
//  2. skips a RetroArch known not to load DSP filter libraries — a probe of
//     the binary for the two strings that support needs, run in the
//     background and cached per binary (path, size, time), on disk too. Only
//     "the setting is there but the library loader is not" is a confident no:
//     a binary with neither string (a wrapper script, a packed binary) or one
//     that could not be read proves nothing, and the launch tries;
//  3. leaves alone every config where the user picked a filter of their own;
//  4. copies the library into <data>/spatial-audio/ beside a freshly written
//     JukaHub-Spatial.dsp with the mode, strength and room of the moment
//     ("auto" resolved by the output in use as the game starts) — only when a
//     config will take it;
//  5. sets audio_dsp_plugin to that preset for this launch only, through the
//     launch journal (racfgscope.go): the user's value goes back when the game
//     exits.
//
// A JukaHub preset already in retroarch.cfg is never the user's value: a lost
// journal, or a RetroArch that rewrote the path on exit, can leave one behind,
// and the journal would then record it as "the user had this" and put it back
// after every game. So it is removed before the launch's own value is set (the
// journal records "the user had none"), and while the setting is off — or the
// plugin or RetroArch cannot work — every launch from JukaHub removes it, so
// it stops filtering games the firmware's menu starts too.
//
// It never blocks or fails a launch: anything missing is logged once and the
// game starts as it would have. A RetroArch that cannot load the filter only
// logs "[DSP]: Failed to initialize DSP filter" and plays normally.
//
// On the handheld the library's LADSPA side carries everything else — the
// standalone emulators, ports, apps, and RetroArch builds that cannot load a
// filter — through ALSA (spatial_alsa.go). Where that route is proven, this
// file only arms the filter for a RetroArch whose binary shows it can load it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// spatialPluginEnv points at a plugin library to use instead of the
	// shipped one (a development build, a test).
	spatialPluginEnv = "JUKAHUB_SPATIAL_PLUGIN"
	// spatialGamesDirName is the folder under the data folder that holds the
	// preset and the copy of the library RetroArch loads.
	spatialGamesDirName = "spatial-audio"
	// spatialPresetName is the preset RetroArch's audio_dsp_plugin names.
	spatialPresetName = "JukaHub-Spatial.dsp"
	// spatialProbeCacheName remembers which RetroArch binaries were probed.
	spatialProbeCacheName = "retroarch-probe.json"
	// spatialDSPConfigKey is RetroArch's setting for the DSP preset.
	spatialDSPConfigKey = "audio_dsp_plugin"
	// spatialFilterIdent is the plugin's short_ident, what filter0 names.
	spatialFilterIdent = "jhspatial"
	// spatialProbeMaxBytes bounds the read of a RetroArch binary (ra64.trimui
	// is about 20 MB); spatialPluginMaxBytes bounds the library copy (ours is
	// about 30 KB).
	spatialProbeMaxBytes  = 96 << 20
	spatialPluginMaxBytes = 8 << 20
	// spatialProbeCacheMax bounds the remembered binaries.
	spatialProbeCacheMax = 8
	// spatialProbeErrRetry is how long a probe that could not read the binary
	// stands before the next ask probes again (a card still mounting, a file
	// being replaced).
	spatialProbeErrRetry = time.Minute
)

// The strings a RetroArch binary holds when it can load DSP filters from
// libraries: the setting's name, and the entry-point name it looks up in each
// library (a NUL-bounded string literal; a build with only built-in filters
// has no such literal).
var (
	spatialProbeSetting = []byte(spatialDSPConfigKey)
	spatialProbeDylib   = []byte("\x00dspfilter_get_implementation\x00")
)

// Test seams.
var (
	spatialRetroArchCandidates = retroArchConfigCandidates
	spatialPluginLocate        = defaultSpatialPluginLocate
	spatialDataDir             = func() (string, error) { return P().DataDir() }
	spatialLookPath            = exec.LookPath
	spatialScan                = spatialScanBinary
	spatialNow                 = time.Now
	// spatialWinRetroArch finds a Windows RetroArch the emulator catalog knows
	// (a portable or installed one keeps retroarch.cfg beside retroarch.exe).
	spatialWinRetroArch = func() string { return emuWinExe("RetroArch") }
)

// spatialPluginFileName is the library's name on this platform.
func spatialPluginFileName() string {
	if runtime.GOOS == "windows" {
		return "jukahub-spatial.dll"
	}
	return "jukahub-spatial.so"
}

// defaultSpatialPluginLocate finds the library: the override, else lib/
// beside the binary (where the handheld package puts it), the binary's own
// folder, and a development checkout's build output. "" when there is none.
func defaultSpatialPluginLocate() string {
	if p := strings.TrimSpace(os.Getenv(spatialPluginEnv)); p != "" {
		if regularFile(p) {
			return p
		}
		return ""
	}
	dir := exeDir()
	name := spatialPluginFileName()
	for _, cand := range []string{
		filepath.Join(dir, "lib", name),
		filepath.Join(dir, name),
		filepath.Join(dir, "spatialfx", "build", name),
	} {
		if regularFile(cand) {
			return cand
		}
	}
	return ""
}

// spatialRetroArchNames are the RetroArch binaries looked for beside a config,
// best first: TrimUI's own builds before a file named retroarch, which on a
// CrossMix or stock card can be a wrapper script that holds neither probe
// string.
func spatialRetroArchNames() []string {
	names := []string{"ra64.trimui", "ra32.trimui"}
	for _, name := range append(append([]string(nil), netplayRetroArchNames...), "retroarch.exe", "retroarch64", "retroarch") {
		dup := false
		for _, have := range names {
			dup = dup || have == name
		}
		if !dup {
			names = append(names, name)
		}
	}
	return names
}

// spatialRetroArchBinaryFor is the RetroArch binary a launch (or, with
// launcher "", the installed RetroArch) runs: the launcher itself when it is
// RetroArch, the retroarch.exe above a Windows core, the binary beside the
// config (ra64.trimui on the handheld), else retroarch on PATH. "" when none.
func spatialRetroArchBinaryFor(launcher, cfgPath string) string {
	if launcher != "" {
		base := strings.ToLower(filepath.Base(launcher))
		if base == "retroarch" || base == "retroarch.exe" || strings.HasPrefix(base, "ra64.") || strings.HasPrefix(base, "ra32.") {
			return launcher
		}
		if strings.EqualFold(filepath.Ext(launcher), ".dll") {
			if exe := emuRetroArchForCore(launcher); exe != "" {
				return exe
			}
		}
	}
	if cfgPath != "" {
		dir := raInstallDir(cfgPath)
		for _, name := range spatialRetroArchNames() {
			if p := filepath.Join(dir, name); regularFile(p) {
				return p
			}
		}
	}
	if spatialLookPath != nil {
		if p, err := spatialLookPath("retroarch"); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// ── Probing RetroArch ─────────────────────────────────────────────────────

// spatialProbeEntry is one probed binary, as the cache file keeps it.
type spatialProbeEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModUnix int64  `json:"modUnix"`
	Setting bool   `json:"setting"`
	Dylib   bool   `json:"dylib"`
	// Err is why the binary could not be read. Such an answer is never
	// written to the card and stands for spatialProbeErrRetry after ErrAt.
	Err   string    `json:"-"`
	ErrAt time.Time `json:"-"`
}

// spatialProbe is the cache: what is known, what is being probed, whether the
// file on the card has been read yet, and a generation for the writes.
var spatialProbe struct {
	mu      sync.Mutex
	known   []spatialProbeEntry
	running map[string]bool
	loaded  bool
	gen     int
}

// spatialProbeWG counts the background probes still running (the tests wait
// for them before their folders go).
var spatialProbeWG sync.WaitGroup

// spatialProbeSaved serialises the cache file's writes; gen is the newest
// generation written, so a slower writer never puts an older list back.
var spatialProbeSaved struct {
	mu  sync.Mutex
	gen int
}

// spatialScanBinary reports which of the two support strings the file holds.
// It reads at most spatialProbeMaxBytes, in chunks, so a string that straddles
// a chunk boundary is still found.
func spatialScanBinary(path string) (setting, dylib bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	const chunk = 1 << 20
	overlap := len(spatialProbeDylib)
	if len(spatialProbeSetting) > overlap {
		overlap = len(spatialProbeSetting)
	}
	buf := make([]byte, chunk+overlap)
	carry, total := 0, 0
	for {
		n, rerr := io.ReadFull(f, buf[carry:carry+chunk])
		data := buf[:carry+n]
		if !setting && bytes.Contains(data, spatialProbeSetting) {
			setting = true
		}
		if !dylib && bytes.Contains(data, spatialProbeDylib) {
			dylib = true
		}
		total += n
		if (setting && dylib) || total >= spatialProbeMaxBytes {
			return setting, dylib, nil
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				return setting, dylib, nil
			}
			return setting, dylib, rerr
		}
		keep := overlap - 1
		if keep > len(data) {
			keep = len(data)
		}
		copy(buf, data[len(data)-keep:])
		carry = keep
	}
}

// spatialProbeSaysNo reports whether a probe is a confident "this RetroArch
// cannot load JukaHub's filter", with the reason in one sentence. Only one
// answer is: the binary has the audio_dsp_plugin setting but not the library
// loader (a build with built-in filters only). A binary that could not be
// read, or holds neither string, proves nothing — the launch tries, and a
// RetroArch that cannot load the filter plays normally.
func spatialProbeSaysNo(e spatialProbeEntry) (bool, string) {
	if e.Err == "" && e.Setting && !e.Dylib {
		return true, "This RetroArch has only its built-in audio filters, so it cannot load JukaHub's"
	}
	return false, ""
}

// spatialProbeNote is the log line for a finished probe.
func spatialProbeNote(e spatialProbeEntry) string {
	switch {
	case e.Err != "":
		return "could not be read to check its audio filter support (" + e.Err + "); launches try anyway"
	case e.Setting && e.Dylib:
		return "can load audio filter libraries"
	case e.Setting:
		return "has only its built-in audio filters; spatial audio skips it"
	}
	return "holds neither audio filter string (a wrapper or packed binary?); launches try anyway"
}

func spatialProbeCachePath() string {
	dir, err := spatialDataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, spatialGamesDirName, spatialProbeCacheName)
}

// spatialProbeLoadLocked reads the cache file once per run.
func spatialProbeLoadLocked() {
	if spatialProbe.loaded {
		return
	}
	spatialProbe.loaded = true
	path := spatialProbeCachePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return
	}
	var file struct {
		Binaries []spatialProbeEntry `json:"binaries"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	for _, e := range file.Binaries {
		if e.Path != "" && len(spatialProbe.known) < spatialProbeCacheMax {
			spatialProbe.known = append(spatialProbe.known, e)
		}
	}
}

// spatialProbeWrite writes the cache file (never under spatialProbe.mu: a
// slow card must not stall the panel); a card that refuses only costs a probe
// at the next start.
func spatialProbeWrite(gen int, entries []spatialProbeEntry) {
	spatialProbeSaved.mu.Lock()
	defer spatialProbeSaved.mu.Unlock()
	if gen <= spatialProbeSaved.gen {
		return
	}
	spatialProbeSaved.gen = gen
	path := spatialProbeCachePath()
	if path == "" {
		return
	}
	var keep []spatialProbeEntry
	for _, e := range entries {
		if e.Err == "" {
			keep = append(keep, e)
		}
	}
	data, err := json.MarshalIndent(struct {
		Binaries []spatialProbeEntry `json:"binaries"`
	}{keep}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := spatialWriteFile(path, append(data, '\n')); err != nil {
		logDebug("spatial", "cannot remember the RetroArch probe in %s: %v", path, err)
	}
}

// spatialFilterSupport answers for bin from the cache. Unknown, it either
// probes now (wait) or starts a background probe and reports known=false. A
// remembered read error stands for spatialProbeErrRetry, then probes again.
func spatialFilterSupport(bin string, wait bool) (entry spatialProbeEntry, known bool) {
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() {
		return spatialProbeEntry{}, false
	}
	key := spatialProbeEntry{Path: bin, Size: info.Size(), ModUnix: info.ModTime().Unix()}
	spatialProbe.mu.Lock()
	spatialProbeLoadLocked()
	for _, e := range spatialProbe.known {
		if e.Path != key.Path || e.Size != key.Size || e.ModUnix != key.ModUnix {
			continue
		}
		if e.Err != "" && spatialNow().Sub(e.ErrAt) >= spatialProbeErrRetry {
			break // stale read error: probe again
		}
		spatialProbe.mu.Unlock()
		return e, true
	}
	if spatialProbe.running == nil {
		spatialProbe.running = map[string]bool{}
	}
	if spatialProbe.running[bin] && !wait {
		spatialProbe.mu.Unlock()
		return spatialProbeEntry{}, false
	}
	spatialProbe.running[bin] = true
	spatialProbe.mu.Unlock()

	run := func() (spatialProbeEntry, bool) {
		e := key
		setting, dylib, err := spatialScan(bin)
		if err != nil {
			e.Err = err.Error()
			e.ErrAt = spatialNow()
		} else {
			e.Setting, e.Dylib = setting, dylib
		}
		spatialProbe.mu.Lock()
		delete(spatialProbe.running, bin)
		kept := spatialProbe.known[:0]
		for _, old := range spatialProbe.known {
			if old.Path != e.Path {
				kept = append(kept, old)
			}
		}
		kept = append(kept, e)
		if len(kept) > spatialProbeCacheMax {
			kept = kept[len(kept)-spatialProbeCacheMax:]
		}
		spatialProbe.known = kept
		spatialProbe.gen++
		gen := spatialProbe.gen
		snapshot := append([]spatialProbeEntry(nil), kept...)
		spatialProbe.mu.Unlock()
		if err == nil {
			spatialProbeWrite(gen, snapshot)
		}
		logInfo("spatial", "RetroArch %s %s", bin, spatialProbeNote(e))
		return e, true
	}
	if wait {
		return run()
	}
	spatialProbeWG.Add(1)
	go func() {
		defer spatialProbeWG.Done()
		run()
	}()
	return spatialProbeEntry{}, false
}

// ── The user's own filter ─────────────────────────────────────────────────

// spatialOwnFilterIn is the audio filter the user picked in RetroArch, read
// from a config's text, or "" when there is none (or it is JukaHub's own
// preset, which is never the user's).
func spatialOwnFilterIn(cfg string) string {
	if cur, ok := raConfigGet(cfg, spatialDSPConfigKey); ok && strings.TrimSpace(cur) != "" && !spatialIsOurPreset(cur) {
		return strings.TrimSpace(cur)
	}
	return ""
}

// spatialOwnFilterCache keeps the last config read for the Audio panel's row,
// which asks every two seconds while it is on screen: the file is read again
// only when its size or time changes. The launch reads the file itself.
var spatialOwnFilterCache struct {
	mu      sync.Mutex
	path    string
	size    int64
	modNano int64
	value   string
}

// spatialOwnFilterCached is spatialOwnFilterIn for the config at path, for the
// panel's row; "" when the file cannot be read.
func spatialOwnFilterCached(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	c := &spatialOwnFilterCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == path && c.size == info.Size() && c.modNano == info.ModTime().UnixNano() {
		return c.value
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	c.path, c.size, c.modNano, c.value = path, info.Size(), info.ModTime().UnixNano(), spatialOwnFilterIn(string(raw))
	return c.value
}

// ── Availability (the Audio panel's games row) ────────────────────────────

// spatialGamesRetroArchOnly starts the reason of an answer that is "yes, but
// only RetroArch": the system route (spatial_alsa.go) said no on a device that
// uses it. The Audio panel's row then reads "On · RetroArch only".
const spatialGamesRetroArchOnly = "RetroArch games only: "

// spatialGamesAvailability says whether games can be spatialised here, and if
// not, why, in one plain sentence for the Audio panel's row (spatial_ui.go
// caches the answer for two seconds). On the handheld either route will do:
// the system route carries every game and app, RetroArch's filter its games.
func spatialGamesAvailability() (ok bool, reason string) {
	raOK, raWhy := spatialRetroArchAvailability()
	if !spatialSystemRouteApplies() {
		return raOK, raWhy
	}
	sysOK, sysWhy, known := spatialSystemRouteState()
	switch {
	case sysOK:
		return true, ""
	case !known:
		// The probe starts with the switch and a mode (spatialBind); the row
		// may be looked at before both are on.
		spatialSystemRouteKick()
		if raOK {
			return true, ""
		}
		return false, "Checking whether games can play through the spatial plugin…"
	case raOK:
		return true, spatialGamesRetroArchOnly + sysWhy
	}
	return false, sysWhy
}

// spatialRetroArchAvailability says whether RetroArch games can be
// spatialised through RetroArch's own filter, and if not, why. Cheap: a few
// file checks; the RetroArch probe runs in the background the first time.
func spatialRetroArchAvailability() (ok bool, reason string) {
	switch runtime.GOOS {
	case "linux", "windows":
	default:
		return false, "Spatial audio in games works on the handheld and on Windows"
	}
	if spatialPluginLocate() == "" {
		if strings.TrimSpace(os.Getenv(spatialPluginEnv)) != "" {
			return false, spatialPluginEnv + " does not point at a file"
		}
		if runtime.GOOS == "windows" {
			return false, "The game audio plugin is not in the Windows build yet; JukaHub's own media is still spatialised"
		}
		return false, "The game audio plugin (lib/" + spatialPluginFileName() + ") is not installed; it comes with the handheld package"
	}
	candidates := spatialRetroArchCandidates()
	if runtime.GOOS == "windows" && spatialWinRetroArch != nil {
		// A portable or installed RetroArch keeps its config beside the exe,
		// which only the launch of one of its cores would otherwise find.
		if exe := spatialWinRetroArch(); exe != "" {
			candidates = append([]string{filepath.Join(filepath.Dir(exe), "retroarch.cfg")}, candidates...)
		}
	}
	cfgs := raScopeTargets(candidates)
	if len(cfgs) == 0 {
		return false, "RetroArch was not found, so there is nothing to add the plugin to"
	}
	if own := spatialOwnFilterCached(cfgs[0]); own != "" {
		return false, "RetroArch already uses its own audio filter (" + filepath.Base(filepath.FromSlash(strings.ReplaceAll(own, "\\", "/"))) +
			"); JukaHub leaves it, so games keep that one"
	}
	bin := spatialRetroArchBinaryFor("", cfgs[0])
	if bin == "" {
		// Nothing to check: the launch tries, and a RetroArch that cannot load
		// the filter plays normally.
		return true, ""
	}
	// The first probe runs in the background; until it answers the reason
	// starts with "Checking", which the panel shows as a pending state
	// (spatial_ui.go spatialGamesChecking), not as "not available".
	entry, known := spatialFilterSupport(bin, false)
	if !known {
		return false, "Checking whether RetroArch can load audio filters…"
	}
	if no, why := spatialProbeSaysNo(entry); no {
		return false, why
	}
	return true, ""
}

// ── The launch ────────────────────────────────────────────────────────────

// spatialLogged keeps the one-per-run notes.
var spatialLogged struct {
	mu   sync.Mutex
	seen map[string]bool
}

func spatialLogOnce(key, format string, args ...any) {
	spatialLogged.mu.Lock()
	if spatialLogged.seen == nil {
		spatialLogged.seen = map[string]bool{}
	}
	first := !spatialLogged.seen[key]
	spatialLogged.seen[key] = true
	spatialLogged.mu.Unlock()
	if first {
		logInfo("spatial", format, args...)
	}
}

// spatialPresetText is the .dsp preset: one filter, the plugin's own
// settings. RetroArch reads it at the game's start.
func spatialPresetText(mode, room int, strength float64) string {
	if !(strength >= 0) { // also NaN
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}
	var b strings.Builder
	b.WriteString("# JukaHub Spatial for RetroArch: written by JukaHub for the game it starts\n")
	b.WriteString("# (spatial_games.go) and rewritten before the next one. Edits here are lost;\n")
	b.WriteString("# change the settings in JukaHub (Tools -> Audio).\n")
	b.WriteString("filters = 1\n")
	b.WriteString("filter0 = " + spatialFilterIdent + "\n")
	fmt.Fprintf(&b, "%s_mode = \"%d\"\n", spatialFilterIdent, mode)
	fmt.Fprintf(&b, "%s_strength = \"%s\"\n", spatialFilterIdent, strconv.FormatFloat(strength, 'f', 2, 64))
	fmt.Fprintf(&b, "%s_room = \"%d\"\n", spatialFilterIdent, room)
	return b.String()
}

// spatialWriteFile replaces path with data through a temporary file beside
// it. No .bak: these files are regenerated, and RetroArch must not find a
// second library in the folder.
func spatialWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// spatialFileDigest hashes a file of at most limit bytes.
func spatialFileDigest(path string, limit int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if n > limit {
		return nil, n, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return h.Sum(nil), n, nil
}

// spatialStageMu serialises spatialStagePlugin.
var spatialStageMu sync.Mutex

// spatialStagePlugin copies the library into <data>/spatial-audio/ when the
// copy there differs (an update shipped a new one) and returns that folder.
// RetroArch's preset and ALSA's "ladspa" PCM (spatial_alsa.go) both load it
// from there: a folder holding nothing but this library, because the ladspa
// PCM opens every .so in the folder it is given.
func spatialStagePlugin(plugin string) (string, error) {
	// The probe's goroutine and a launch may stage at once; two writers of
	// the same temporary file could leave a torn library.
	spatialStageMu.Lock()
	defer spatialStageMu.Unlock()
	base, err := spatialDataDir()
	if err != nil {
		return "", fmt.Errorf("no data folder: %w", err)
	}
	if base == "" {
		return "", errors.New("no data folder")
	}
	dir := filepath.Join(base, spatialGamesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, size, err := spatialFileDigest(plugin, spatialPluginMaxBytes)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, spatialPluginFileName())
	if have, haveSize, err := spatialFileDigest(dst, spatialPluginMaxBytes); err != nil || haveSize != size || !bytes.Equal(have, src) {
		raw, err := os.ReadFile(plugin)
		if err != nil {
			return "", err
		}
		if err := spatialWriteFile(dst, raw); err != nil {
			return "", fmt.Errorf("copy the plugin: %w", err)
		}
	}
	return dir, nil
}

// spatialStage puts the library and a fresh preset into the data folder and
// returns the preset's path. The library is copied only when the copy there
// differs (an update shipped a new one).
func spatialStage(plugin string, mode, room int, strength float64) (string, error) {
	dir, err := spatialStagePlugin(plugin)
	if err != nil {
		return "", err
	}
	preset := filepath.Join(dir, spatialPresetName)
	if err := spatialWriteFile(preset, []byte(spatialPresetText(mode, room, strength))); err != nil {
		return "", fmt.Errorf("write the preset: %w", err)
	}
	return preset, nil
}

// spatialIsOurPreset reports whether a configured audio_dsp_plugin is the
// preset JukaHub writes, wherever it was written from (another install, an
// older folder): only its name counts.
func spatialIsOurPreset(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	return strings.EqualFold(filepath.Base(filepath.FromSlash(value)), spatialPresetName) ||
		strings.HasSuffix(strings.ToLower(value), "/"+strings.ToLower(spatialPresetName))
}

// spatialClearLeftover removes a JukaHub preset from the configs at paths (a
// user's own filter is never touched). It runs under the launch journal's
// lock, like every other edit of retroarch.cfg, and reports how many configs
// changed. Removing is the user's own state: JukaHub only ever sets the key
// where the user had no filter of their own.
func spatialClearLeftover(paths []string) int {
	cleared := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if cur, ok := raConfigGet(string(raw), spatialDSPConfigKey); !ok || !spatialIsOurPreset(cur) {
			continue
		}
		raScope.mu.Lock()
		changed, err := editRetroArchConfig(path, func(cfg string) (string, bool) {
			if cur, ok := raConfigGet(cfg, spatialDSPConfigKey); !ok || !spatialIsOurPreset(cur) {
				return cfg, false
			}
			return raConfigUnset(cfg, spatialDSPConfigKey)
		})
		raScope.mu.Unlock()
		if err != nil {
			logWarn("spatial", "cannot remove JukaHub's audio filter from %s: %v", path, err)
			continue
		}
		if changed {
			cleared++
			logInfo("spatial", "removed a JukaHub audio filter preset left in %s", path)
		}
	}
	return cleared
}

// spatialClearLeftoverFor is the launch path when there is nothing to add: a
// JukaHub preset left in the launch's RetroArch configs goes, so it stops
// filtering this game and the ones the firmware's menu starts. The script and
// config reads are the ones the other launch-time edits make too (the OS has
// them cached by then).
func spatialClearLeftoverFor(launcher string) {
	if !retroArchLauncher(launcher) {
		return
	}
	spatialClearLeftover(raScopeTargets(raLaunchCandidates(launcher, spatialRetroArchCandidates())))
}

// prepareSpatialAudio is the launch-time entry point, beside prepareRecordings
// (game_library.go). It reports whether a RetroArch config got the preset for
// this launch, and remembers that for the child's environment
// (spatialChildEnv), which must not add the system route on top.
func prepareSpatialAudio(launcher string) bool {
	spatialDSPArm("")
	if !prepareSpatialAudioDSP(launcher) {
		return false
	}
	spatialDSPArm(launcher)
	return true
}

// prepareSpatialAudioDSP sets RetroArch's filter for this launch when it is
// the route to use (prepareSpatialAudio).
func prepareSpatialAudioDSP(launcher string) bool {
	// The settings first: they are a few map lookups, the launcher check reads
	// the script.
	s := spatialSettingsFor(appConfig)
	mode := spatialDSPOff
	if s.Games {
		mode = spatialEffectiveMode(s)
	}
	if mode == spatialDSPOff {
		spatialClearLeftoverFor(launcher)
		return false
	}
	if !retroArchLauncher(launcher) {
		return false
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, spatialRetroArchCandidates()))
	if len(targets) == 0 {
		return false
	}
	plugin := spatialPluginLocate()
	if plugin == "" {
		spatialLogOnce("no-plugin", "spatial audio in games is on, but the game audio plugin (%s) is not installed", spatialPluginFileName())
		spatialClearLeftover(targets)
		return false
	}
	// Where the system route (spatial_alsa.go) works, it carries every game,
	// RetroArch included, so the filter is only used by a RetroArch proven to
	// load it: an in-process filter beats an extra ALSA stage, and the two
	// together would spatialise the game twice (spatialChildEnv skips a child
	// whose launch armed the filter).
	systemRoute := spatialSystemRouteReady()
	confirmed := false
	if bin := spatialRetroArchBinaryFor(launcher, targets[0]); bin != "" {
		// Only a confident no skips: an unread binary, or one without either
		// string, proves nothing.
		if entry, known := spatialFilterSupport(bin, false); known {
			if no, why := spatialProbeSaysNo(entry); no {
				if !systemRoute {
					spatialLogOnce("unsupported:"+bin, "spatial audio in games skipped for %s: %s", bin, why)
				}
				spatialClearLeftover(targets)
				return false
			}
			confirmed = entry.Err == "" && entry.Setting && entry.Dylib
		}
	}
	// A 32-bit RetroArch is one the system route leaves alone
	// (spatialLaunchLooks32): it keeps the filter.
	if systemRoute && !confirmed && !spatialLaunchLooks32(&exec.Cmd{Path: launcher, Dir: filepath.Dir(launcher)}) {
		spatialClearLeftover(targets)
		return false
	}
	// The configs that take the preset: a filter the user chose in RetroArch
	// is theirs, never replaced. Nothing is staged when none will use it.
	var takers []string
	for _, path := range targets {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if own := spatialOwnFilterIn(string(raw)); own != "" {
			spatialLogOnce("own-filter:"+path, "RetroArch already uses the audio filter %s (%s); JukaHub does not replace it", own, path)
			continue
		}
		takers = append(takers, path)
	}
	if len(takers) == 0 {
		return false
	}
	preset, err := spatialStage(plugin, mode, spatialRoomIndex(s.Room), s.Strength)
	if err != nil {
		logWarn("spatial", "cannot prepare spatial audio for this game: %v", err)
		return false
	}
	// A JukaHub value already there is not the user's: removed first, so the
	// journal records "the user had none" and the game's exit removes the key.
	spatialClearLeftover(takers)
	values := []raScopeValue{{Key: spatialDSPConfigKey, Value: filepath.ToSlash(preset)}}
	configured := false
	for _, path := range takers {
		changed, err := raScopeApply(path, values)
		if err != nil {
			logWarn("spatial", "cannot set the audio filter in %s: %v", path, err)
			continue
		}
		configured = true
		if changed {
			Log().Info("spatial audio filter set for this launch", "config", path, "preset", preset, "mode", mode)
		}
	}
	return configured
}
