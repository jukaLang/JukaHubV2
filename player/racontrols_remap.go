package main

// racontrols_remap.go — the one-launch RetroArch remap file behind the
// Buttons choice (racontrols.go).
//
// RetroArch loads a remap file by itself when a game starts (with "Load Remap
// Files Automatically", auto_remaps_enable, on — the default) from
// <input_remapping_directory>/<core's library_name>/, most specific first:
//
//	<ROM name without extension>.rmp   the game
//	<ROM's folder name>.rmp            the content folder (RetroArch 1.9+)
//	<library_name>.rmp                 the core
//
// (config_load_remap in RetroArch's configuration.c; libretro docs, "Input
// remapping"). The file is RetroArch's config format, and for each player
// holds, per RetroPad button, the RetroPad button the core is handed instead
// (input_remapping.c, the same in RetroArch 1.9 to 1.20):
//
//	input_remap_port_p1 = "0"
//	input_player1_btn_b = "8"        B presses the game's A
//	input_player1_btn_a = "0"        A presses the game's B
//
// The buttons are numbered as RETRO_DEVICE_ID_JOYPAD is: b 0, y 1, select 2,
// start 3, up 4, down 5, left 6, right 7, a 8, x 9, l 10, r 11, l2 12, r2 13,
// l3 14, r3 15; -1 is "nothing". A button left out keeps its own meaning,
// which is also how RetroArch saves the file (only the changed buttons).
// A remap changes what the core sees, not RetroArch's own menu or hotkeys.
//
// A launch with Buttons other than Normal writes the game's file for that
// launch only, with a marker line on top, and sets for the launch (through
// raScopeApply) auto_remaps_enable = true, remap_save_on_exit = false (newer
// builds otherwise save the loaded remap again as the game closes) and
// input_remapping_directory when retroarch.cfg leaves it empty. Like the
// video filter's preset (videofilter.go) and the cheats' file (cheats.go) it
// is recorded in JukaHub's data folder (remaps-pending.json) before it is
// written, removed once RetroArch has exited, and removed at the next start
// when JukaHub died mid-game. A file counts as JukaHub's when it carries the
// marker, or when RetroArch saved it again with exactly the buttons written.
//
// A remap the user made in RetroArch — for the game, its folder or its core —
// is never overwritten, deleted or shadowed by JukaHub's more specific game
// file: that launch keeps the user's remap, the log says so, and the game
// options sheet's Buttons row shows it.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	raRemapPendingName = "remaps-pending.json"
	// raRemapMarker is the first line of every remap file JukaHub writes.
	// RetroArch's config reader skips "#" lines.
	raRemapMarker = "# JukaHub: written for one game started from JukaHub; removed when RetroArch exits"
	// raRemapMaxBytes bounds a remap file read: RetroArch's own are a few KB.
	raRemapMaxBytes = 64 << 10

	raRemapStartWait = 30 * time.Second
	raRemapWatchPoll = 2 * time.Second
	raRemapWatchMax  = 8 * time.Hour
)

// raRemapButtonNames are RetroArch's names for the RetroPad buttons, indexed
// by their RETRO_DEVICE_ID_JOYPAD number.
var raRemapButtonNames = [...]string{
	"b", "y", "select", "start", "up", "down", "left", "right",
	"a", "x", "l", "r", "l2", "r2", "l3", "r3",
}

// raRemapButtonID is a button's number, -1 for a name RetroArch does not use.
func raRemapButtonID(name string) int {
	for i, n := range raRemapButtonNames {
		if n == name {
			return i
		}
	}
	return -1
}

// raRemapPreset is what a Buttons choice changes: button name → the RetroPad
// button the core is handed. Normal changes nothing.
func raRemapPreset(mode string) map[string]int {
	swapAB := map[string]int{"a": raRemapButtonID("b"), "b": raRemapButtonID("a")}
	swapXY := map[string]int{"x": raRemapButtonID("y"), "y": raRemapButtonID("x")}
	switch mode {
	case gameButtonsSwapAB:
		return swapAB
	case gameButtonsSwapXY:
		return swapXY
	case gameButtonsSwapBoth:
		for k, v := range swapXY {
			swapAB[k] = v
		}
		return swapAB
	}
	return nil
}

// raRemapText is the remap file for player 1: the marker, the port and the
// changed buttons in RetroPad order, then any extra keys (Turbo Fire for the
// builds that read it from remaps). Pure.
func raRemapText(buttons map[string]int, extra []raScopeValue) string {
	var b strings.Builder
	b.WriteString(raRemapMarker + "\n")
	b.WriteString("input_remap_port_p1 = \"0\"\n")
	for i, name := range raRemapButtonNames {
		if to, ok := buttons[name]; ok && to != i {
			b.WriteString("input_player1_btn_" + name + " = \"" + strconv.Itoa(to) + "\"\n")
		}
	}
	for _, v := range extra {
		b.WriteString(v.Key + " = \"" + v.Value + "\"\n")
	}
	return b.String()
}

// raRemapParse reads player 1's buttons from a remap file: button name → the
// RetroPad button it sends, for every input_player1_btn_* key with a number
// (quoted or bare; the first line for a key counts, as in RetroArch). Pure.
func raRemapParse(data []byte) map[string]int {
	if len(data) > raRemapMaxBytes {
		data = data[:raRemapMaxBytes]
	}
	out := map[string]int{}
	const prefix = "input_player1_btn_"
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		key, value, _, ok := raConfigLine(line)
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		name := key[len(prefix):]
		if raRemapButtonID(name) < 0 {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < -1 || n > 255 {
			continue
		}
		out[name] = n
	}
	return out
}

// raRemapFingerprint names a mapping by its changed buttons ("a=0,b=8"): the
// part RetroArch keeps when it saves the file again. Pure.
func raRemapFingerprint(buttons map[string]int) string {
	var parts []string
	for name, to := range buttons {
		if to == raRemapButtonID(name) {
			continue
		}
		parts = append(parts, name+"="+strconv.Itoa(to))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// ── Where RetroArch looks ────────────────────────────────────────────────

// raRemapDir is the remap folder a retroarch.cfg uses, and whether the launch
// has to set input_remapping_directory because the file leaves it empty
// (RetroArch's built-in default differs between builds): then it is the
// "remaps" folder in the menu config folder, RetroArch's usual place.
func raRemapDir(cfgText, cfgPath string) (string, bool) {
	if v, ok := raConfigGet(cfgText, "input_remapping_directory"); ok {
		if dir := videoFilterExpandDir(v, cfgPath); dir != "" {
			return dir, false
		}
	}
	return filepath.Join(raTuneMenuConfigDir(cfgText, cfgPath), "remaps"), true
}

// raRemapCandidates are the files RetroArch would load for a game, most
// specific first: the game's, the content folder's, the core's. Pure.
func raRemapCandidates(dir, folder, romPath string) []string {
	var out []string
	stem := gameResetStem(romPath)
	parent := filepath.Base(filepath.Dir(filepath.Clean(romPath)))
	for _, name := range []string{stem, parent, folder} {
		if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
			continue
		}
		out = append(out, filepath.Join(dir, folder, name+".rmp"))
	}
	return out
}

// raRemapPrepare writes the game's remap for one launch and sets the keys
// that make RetroArch load it. It returns the note the sheet shows when the
// buttons could not be applied, "" when they were.
func raRemapPrepare(launcher string, game libraryGame, targets []string, mode string, turbo map[string][]raScopeValue) string {
	buttons := raRemapPreset(mode)
	if len(buttons) == 0 {
		return ""
	}
	core := gameCoreFromLauncher(launcher, game.System)
	folders := resetCoreFolders(core)
	if len(folders) == 0 || gameResetStem(game.Path) == "" {
		logWarn("controls", "cannot tell which core plays %s; launching with its own buttons", game.Path)
		return "Buttons not applied last time: JukaHub could not tell the RetroArch core"
	}
	// Every config's folder is checked before anything is written: a user's
	// file under any name the core has gone by is theirs, and wins.
	type remapTarget struct {
		cfgPath, dir string
		setDir       bool
	}
	var plan []remapTarget
	for _, cfgPath := range targets {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		dir, setDir := raRemapDir(string(raw), cfgPath)
		for _, folder := range folders {
			for _, p := range raRemapCandidates(dir, folder, game.Path) {
				if raRemapUserFile(p) {
					logInfo("controls", "%s is the user's own RetroArch remap; it is kept and used for this launch", p)
					return "Your own RetroArch remap file is used instead"
				}
			}
		}
		plan = append(plan, remapTarget{cfgPath: cfgPath, dir: dir, setDir: setDir})
	}
	fp := raRemapFingerprint(buttons)
	written := map[string]bool{}
	applied := false
	for _, t := range plan {
		path := filepath.Join(t.dir, folders[0], gameResetStem(game.Path)+".rmp")
		key := strings.ToLower(filepath.Clean(path))
		if !written[key] {
			if !raRemapWrite(path, raRemapText(buttons, turbo[t.cfgPath]), fp) {
				continue
			}
			written[key] = true
		}
		values := []raScopeValue{
			{Key: "auto_remaps_enable", Value: "true"},
			{Key: "remap_save_on_exit", Value: "false"},
		}
		if t.setDir {
			values = append(values, raScopeValue{Key: "input_remapping_directory", Value: t.dir})
		}
		if _, err := raScopeApply(t.cfgPath, values); err != nil {
			logWarn("controls", "cannot switch on remap loading in %s: %v", t.cfgPath, err)
			continue
		}
		applied = true
		Log().Info("buttons applied for this launch", "buttons", mode, "file", path, "config", t.cfgPath)
	}
	if !applied {
		return "Buttons not applied last time: the remap file could not be written"
	}
	return ""
}

// ── The one-launch file ──────────────────────────────────────────────────

// raRemapPending guards the record of written files and the watcher that
// removes them (cheats.go's cheatsPending, for remap files).
var raRemapPending struct {
	mu      sync.Mutex
	gen     int
	running bool
}

type raRemapPendingEntry struct {
	Path    string `json:"path"`
	Buttons string `json:"buttons"`
}

type raRemapPendingFile struct {
	Files []raRemapPendingEntry `json:"files"`
}

// raRemapPendingPath is the record in JukaHub's data folder; a variable for
// tests.
var raRemapPendingPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, raRemapPendingName)
}

func raRemapPendingLoadLocked() []raRemapPendingEntry {
	path := raRemapPendingPath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return nil
	}
	var file raRemapPendingFile
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	return file.Files
}

func raRemapPendingSaveLocked(files []raRemapPendingEntry) error {
	path := raRemapPendingPath()
	if path == "" {
		if len(files) == 0 {
			return nil
		}
		return os.ErrNotExist
	}
	if len(files) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(raRemapPendingFile{Files: files}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// raRemapOurs reports whether a remap file is one JukaHub wrote: it carries
// the marker, or RetroArch saved it again with exactly the buttons written
// (fp). Pure.
func raRemapOurs(raw []byte, fp string) bool {
	if strings.HasPrefix(strings.TrimPrefix(string(raw), "\ufeff"), raRemapMarker) {
		return true
	}
	return fp != "" && raRemapFingerprint(raRemapParse(raw)) == fp
}

func raRemapReadSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, raRemapMaxBytes+1))
}

// raRemapUserFile reports whether path is a remap file JukaHub did not write.
func raRemapUserFile(path string) bool {
	if !regularFile(path) {
		return false
	}
	raw, err := raRemapReadSmall(path)
	if err != nil {
		return true // there, but unreadable: not JukaHub's to touch
	}
	if raRemapOurs(raw, "") {
		return false
	}
	raRemapPending.mu.Lock()
	defer raRemapPending.mu.Unlock()
	for _, e := range raRemapPendingLoadLocked() {
		if strings.EqualFold(filepath.Clean(e.Path), filepath.Clean(path)) && raRemapOurs(raw, e.Buttons) {
			return false
		}
	}
	return true
}

// raRemapWrite writes the game's remap for one launch. It is recorded before
// it is written, so a power cut in between leaves something to clean up rather
// than swapped buttons in every game started from the firmware.
func raRemapWrite(path, text, fp string) bool {
	raRemapPending.mu.Lock()
	defer raRemapPending.mu.Unlock()
	files := raRemapPendingLoadLocked()
	listed := false
	for i := range files {
		if strings.EqualFold(filepath.Clean(files[i].Path), filepath.Clean(path)) {
			files[i].Buttons = fp
			listed = true
		}
	}
	if !listed {
		files = append(files, raRemapPendingEntry{Path: path, Buttons: fp})
	}
	if err := raRemapPendingSaveLocked(files); err != nil {
		logWarn("controls", "cannot record the one-launch remap file (%v); launching with the game's own buttons", err)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logWarn("controls", "cannot create %s: %v", filepath.Dir(path), err)
		return false
	}
	tmp := path + ".jukahub-tmp"
	if err := writeFileSynced(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("controls", "cannot write %s: %v", path, err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("controls", "cannot write %s: %v", path, err)
		return false
	}
	raRemapPending.gen++
	if !raRemapPending.running {
		raRemapPending.running = true
		go raRemapWatchExit()
	}
	return true
}

// raRemapCleanupLocked removes every recorded file that is still JukaHub's. A
// file the user saved over in RetroArch's menu is theirs now and stays. The
// core folder goes too when the file was the last thing in it.
func raRemapCleanupLocked() {
	files := raRemapPendingLoadLocked()
	if len(files) == 0 {
		return
	}
	var keep []raRemapPendingEntry
	for _, e := range files {
		raw, err := raRemapReadSmall(e.Path)
		if err != nil {
			if !os.IsNotExist(err) {
				keep = append(keep, e) // a card not mounted yet: try again later
			}
			continue
		}
		if !raRemapOurs(raw, e.Buttons) {
			logInfo("controls", "%s was saved again in RetroArch; it is the user's now and stays", e.Path)
			continue
		}
		if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
			logWarn("controls", "cannot remove the one-launch remap file %s: %v", e.Path, err)
			keep = append(keep, e)
			continue
		}
		_ = os.Remove(filepath.Dir(e.Path)) // only succeeds when empty
		logDebug("controls", "removed the one-launch remap file %s", e.Path)
	}
	if err := raRemapPendingSaveLocked(keep); err != nil {
		logWarn("controls", "cannot update the remap file record: %v", err)
	}
}

// raRemapCleanup is the startup repair: files a crash or power cut left
// behind go, so a game started from the firmware has its own buttons.
func raRemapCleanup() {
	if retroArchRunning() {
		return // JukaHub restarted under a live game: the next launch's watcher cleans up
	}
	raRemapPending.mu.Lock()
	defer raRemapPending.mu.Unlock()
	if raRemapPending.running {
		return // a launch is in flight; its watcher cleans up
	}
	raRemapCleanupLocked()
}

// raRemapWatchExit waits for the game to take the screen and then for it, and
// RetroArch itself, to be gone, then removes the file. A launch that never
// takes the screen is over after raRemapStartWait.
func raRemapWatchExit() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "watching the remap file failed: %v", r)
			raRemapPending.mu.Lock()
			raRemapPending.running = false
			raRemapPending.mu.Unlock()
		}
	}()
	for {
		raRemapPending.mu.Lock()
		gen := raRemapPending.gen
		raRemapPending.mu.Unlock()

		start := time.Now()
		for !gameScreenSuspended() && !retroArchRunning() && time.Since(start) < raRemapStartWait {
			time.Sleep(raRemapWatchPoll / 4)
		}
		for (gameScreenSuspended() || retroArchRunning()) && time.Since(start) < raRemapWatchMax {
			time.Sleep(raRemapWatchPoll)
		}

		raRemapPending.mu.Lock()
		if gen != raRemapPending.gen {
			raRemapPending.mu.Unlock()
			continue // a newer launch wrote a file meanwhile: wait for that one
		}
		raRemapCleanupLocked()
		raRemapPending.running = false
		raRemapPending.mu.Unlock()
		return
	}
}

func init() {
	// After the first frame, beside the cheats' file repair.
	deferStartupTask("remaps", func() { go raRemapCleanup() })
}
