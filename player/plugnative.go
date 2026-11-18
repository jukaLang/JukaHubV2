package main

// plugnative.go — the bundled plugins, written in Go.
//
// Every plugin that ships with JukaHub is a shell script, and Windows has no
// sh unless Git for Windows happens to be installed. So on Windows most of the
// bundled plugins said "Can't run here", their buttons were greyed out, and
// switching one on or off visibly changed nothing. The same scripts on the
// handheld also paid a fork of sh (plus awk, sed, date…) per press.
//
// Each bundled plugin's actions now also exist here. The runner (plugExecute)
// uses the Go version when the plugin is a stock JukaHub one and either its
// interpreter is missing or its script is still the one JukaHub shipped. A
// script the user edited keeps running as a script wherever sh exists, so a
// plugin stays hackable; the .sh files remain the reference for people writing
// their own.
//
// Nothing here touches the interface: these run on the plugin goroutine and
// only return text. Actions that change JukaHub itself (apply a theme, open
// the game library…) are in plughooks.go and run on the main thread. The two
// bigger tools — Save guard and Library doctor — are in plugnative_tools.go.

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// plugNativeAuthor is the author every bundled manifest carries. A user's own
// plugin that happens to share a bundled folder name ("notes") is not taken
// over by the Go version unless it also claims to be JukaHub's.
const plugNativeAuthor = "JukaHub"

// plugNativeFunc runs one action. dir is the plugin's folder (the notes
// plugin keeps notes.txt there); vars are the custom variables, copied.
type plugNativeFunc func(action string, vars map[string]interface{}, dir string) (string, error)

// plugNativeRunners maps a bundled plugin's folder name to its Go version.
// Filled in init so the table can refer to functions that refer back to it.
var plugNativeRunners map[string]plugNativeFunc

func init() {
	plugNativeRunners = map[string]plugNativeFunc{
		"classicpack":   plugNativeClassicPack,
		"gamepick":      plugNativeGamePick,
		"librarydoctor": plugNativeLibraryDoctor,
		"neontheme":     plugNativeNeonTheme,
		"notes":         plugNativeNotes,
		"padprofiles":   plugNativePadProfiles,
		"retroformats":  plugNativeRetroFormats,
		"saveguard":     plugNativeSaveGuard,
		"sysinfo":       plugNativeSysinfo,
	}
}

// plugNativeFor returns the Go version of a plugin, or nil when it has none.
func plugNativeFor(info PluginInfo) plugNativeFunc {
	if strings.TrimSpace(info.Author) != plugNativeAuthor {
		return nil
	}
	return plugNativeRunners[info.Name]
}

// plugPreferNative reports whether an action of info should run in Go rather
// than as its script: there is a Go version, and either the script cannot run
// here or it is still byte-for-byte the one JukaHub shipped.
func plugPreferNative(info PluginInfo, root string) bool {
	if plugNativeFor(info) == nil {
		return false
	}
	if _, err := plugLookPath(plugInterpreter(info.Entry)); err != nil {
		return true
	}
	return plugScriptIsStock(info, root)
}

// plugScriptIsStock reports whether the plugin's entry script on disk is the
// one built into this binary.
func plugScriptIsStock(info PluginInfo, root string) bool {
	entry := strings.TrimSpace(info.Entry)
	if entry == "" || strings.ContainsAny(entry, `/\`) {
		return false
	}
	shipped, err := fs.ReadFile(plugBundleFS, path.Join("plugins", info.Name, entry))
	if err != nil {
		return false
	}
	onDisk, err := os.ReadFile(filepath.Join(root, entry))
	if err != nil {
		return false
	}
	return plugSameText(shipped, onDisk)
}

// plugSameText compares two text files ignoring CR, since a checkout or an
// editor on Windows may have turned the script's line endings into CRLF.
func plugSameText(a, b []byte) bool {
	strip := func(p []byte) string { return strings.ReplaceAll(string(p), "\r", "") }
	return strip(a) == strip(b)
}

// plugRunNative runs a Go action and formats its result like a script's.
func plugRunNative(fn plugNativeFunc, action string, vars map[string]interface{}, dir string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("Plugin error: %v", r)
		}
	}()
	text, err := fn(action, vars, dir)
	if err != nil {
		return "Plugin error: " + err.Error()
	}
	return text
}

// ── Helpers ───────────────────────────────────────────────────────────────

// plugVar reads one custom variable as text, "" when it is not set.
func plugVar(vars map[string]interface{}, key string) string {
	v, ok := vars[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// plugUnknown is the error for an action a plugin does not have.
func plugUnknown(action, try string) error {
	return fmt.Errorf("unknown action %q (try %s)", action, try)
}

// plugHumanBytes turns a byte count into a short size: 2.0G, 512M, 80K.
func plugHumanBytes(b uint64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return strconv.FormatFloat(float64(b)/(k*k*k), 'f', 1, 64) + "G"
	case b >= k*k:
		return strconv.FormatUint(b/(k*k), 10) + "M"
	case b >= k:
		return strconv.FormatUint(b/k, 10) + "K"
	default:
		return strconv.FormatUint(b, 10) + "B"
	}
}

// plugClip shortens s to n runes with an ellipsis.
func plugClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ── Random game ───────────────────────────────────────────────────────────
//
// "Pick a game for me" is a JukaHub action (@random-game, plughooks.go): it
// rolls in the library itself and lands the highlight on the pick, so A plays
// it. What is left here is the count, and the old toast-only pick for config
// buttons that still name it.

// plugGameSkipExt are files in a Roms folder that are not games.
var plugGameSkipExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".txt": true, ".xml": true, ".db": true,
	".json": true, ".cfg": true, ".ini": true, ".srm": true, ".sav": true, ".log": true,
	".bak": true, ".nfo": true, ".md": true, ".dat": true, ".webp": true, ".gif": true,
}

// plugGameSkipDir are artwork and metadata folders inside a system folder,
// by lower-case name (".media" is NextUI's, ".res" MinUI's).
var plugGameSkipDir = map[string]bool{"imgs": true, "media": true, ".media": true, ".res": true, "images": true, "boxart": true, "snap": true, "snaps": true}

// plugGameScanCap bounds the walk on a huge card.
const plugGameScanCap = 60000

// plugGame is one game file found by the walk.
type plugGame struct {
	Name, System string
}

func plugNativeGamePick(action string, vars map[string]interface{}, _ string) (string, error) {
	roots := plugGameRoots(plugVar(vars, "gamepick_dir"))
	switch action {
	case "count":
		n := plugWalkGames(roots, nil)
		switch n {
		case 0:
			return "No games found in the Roms folders", nil
		case 1:
			return "1 game on the card", nil
		}
		return fmt.Sprintf("%d games on the card", n), nil
	case "pick":
		var chosen plugGame
		seen := 0
		plugWalkGames(roots, func(g plugGame) {
			// Reservoir sampling: one pass, every game equally likely.
			seen++
			if rand.IntN(seen) == 0 {
				chosen = g
			}
		})
		if seen == 0 {
			return "No games found in the Roms folders", nil
		}
		return fmt.Sprintf("Try: %s (%s)", chosen.Name, chosen.System), nil
	}
	return "", plugUnknown(action, "count, pick")
}

// plugGameRoots is where games are looked for: the variable when it is set,
// else the game library's own folders and ~/Roms.
func plugGameRoots(custom string) []string {
	if custom != "" {
		return []string{custom}
	}
	roots := romLibraryRoots()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, "Roms"))
	}
	return roots
}

// plugWalkGames counts the games under roots (Roms/SYSTEM/game or
// Roms/SYSTEM/Game/disc) and hands each one to visit when it is not nil.
func plugWalkGames(roots []string, visit func(plugGame)) int {
	n := 0
	seenRoot := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if seenRoot[root] {
			continue
		}
		seenRoot[root] = true
		systems, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, sys := range systems {
			if !sys.IsDir() || strings.HasPrefix(sys.Name(), ".") {
				continue
			}
			n += plugWalkSystem(filepath.Join(root, sys.Name()), sys.Name(), 1, visit, plugGameScanCap-n)
			if n >= plugGameScanCap {
				return n
			}
		}
	}
	return n
}

// plugWalkSystem lists the games in one system folder, one level of
// sub-folders deep, and returns how many it found (at most budget).
func plugWalkSystem(dir, system string, depth int, visit func(plugGame), budget int) int {
	if budget <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if skipScanEntryName(name) {
			continue
		}
		if e.IsDir() {
			if depth < 2 && !plugGameSkipDir[strings.ToLower(name)] {
				n += plugWalkSystem(filepath.Join(dir, name), system, depth+1, visit, budget-n)
			}
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if plugGameSkipExt[ext] || strings.Contains(strings.ToLower(name), ".state") {
				continue
			}
			n++
			if visit != nil {
				visit(plugGame{Name: strings.TrimSuffix(name, filepath.Ext(name)), System: system})
			}
		}
		if n >= budget {
			break
		}
	}
	return n
}

// ── Notes ─────────────────────────────────────────────────────────────────

// plugNotesFile is the pad inside the plugin's folder.
func plugNotesFile(dir string) string { return filepath.Join(dir, "notes.txt") }

// plugNotesShown is how many notes "All notes" lists, newest first.
const plugNotesShown = 12

// plugNotesLines reads the saved notes, oldest first.
func plugNotesLines(dir string) []string {
	data, err := os.ReadFile(plugNotesFile(dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// plugNotesAdd appends one note with the date and time it was written.
func plugNotesAdd(dir, text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", errors.New("nothing to add: the note is empty")
	}
	f, err := os.OpenFile(plugNotesFile(dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("could not open the notes file: %v", err)
	}
	_, werr := fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("Jan 2 15:04"), text)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return "", errors.New("could not save the note")
	}
	return "Saved: " + plugClip(text, 38), nil
}

// plugNotesCount is "No notes yet", "1 note saved", "4 notes saved".
func plugNotesCount(n int) string {
	switch n {
	case 0:
		return "No notes yet"
	case 1:
		return "1 note saved"
	}
	return fmt.Sprintf("%d notes saved", n)
}

// plugNotesClear empties the pad.
func plugNotesClear(dir string) (string, error) {
	if err := os.WriteFile(plugNotesFile(dir), nil, 0o644); err != nil {
		return "", fmt.Errorf("could not clear the notes: %v", err)
	}
	return "Notes cleared", nil
}

func plugNativeNotes(action string, vars map[string]interface{}, dir string) (string, error) {
	switch action {
	case "add":
		return plugNotesAdd(dir, plugVar(vars, "notes_text"))
	case "last":
		lines := plugNotesLines(dir)
		if len(lines) == 0 {
			return "No notes yet · Add note opens the keyboard", nil
		}
		return plugClip(lines[len(lines)-1], 60), nil
	case "all":
		// The count is the first line, so it is what the toast says; the
		// Plugins page shows the list under it, newest first.
		lines := plugNotesLines(dir)
		if len(lines) == 0 {
			return "No notes yet · Add note opens the keyboard", nil
		}
		out := []string{plugNotesCount(len(lines)) + ", newest first"}
		for i := len(lines) - 1; i >= 0 && len(out) <= plugNotesShown; i-- {
			out = append(out, plugClip(lines[i], 80))
		}
		if len(lines) > plugNotesShown {
			out = append(out, fmt.Sprintf("…and %d older (notes.txt in the plugin's folder has them all)", len(lines)-plugNotesShown))
		}
		return strings.Join(out, "\n"), nil
	case "count":
		return plugNotesCount(len(plugNotesLines(dir))), nil
	case "clear":
		return plugNotesClear(dir)
	}
	return "", plugUnknown(action, "add, last, all, count, clear")
}

// ── System monitor ────────────────────────────────────────────────────────

// plugMemory is the available and total memory in bytes, 0s when unknown.
func plugMemory() (free, total uint64) {
	defer func() {
		if recover() != nil {
			free, total = 0, 0
		}
	}()
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil {
		return 0, 0
	}
	return vm.Available, vm.Total
}

// plugDisk is the free and total space where JukaHub lives, 0s when unknown.
func plugDisk() (free, total uint64) {
	total, free, err := sysToolVolumeUsage(sysToolAppVolumePath())
	if err != nil {
		return 0, 0
	}
	return free, total
}

// plugUptime asks gopsutil, which can panic on unusual systems.
func plugUptime() (d time.Duration, err error) {
	defer func() {
		if r := recover(); r != nil {
			d, err = 0, fmt.Errorf("uptime: %v", r)
		}
	}()
	secs, err := host.Uptime()
	if err != nil {
		return 0, err
	}
	return time.Duration(secs) * time.Second, nil
}

// plugDuration prints "2d 3h 4m", "3h 4m" or "4m".
func plugDuration(d time.Duration) string {
	mins := int(d / time.Minute)
	days, hours, m := mins/1440, (mins%1440)/60, mins%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, m)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, m)
	}
	return fmt.Sprintf("%dm", m)
}

// plugBatteryLine is "Battery 80%", "Battery 80% · charging", with the time
// left or to full once battery_eta.go has one ("Battery 80% · about 3 h
// left"), or a plain sentence when there is no battery to read.
func plugBatteryLine() string {
	pct := getBatteryPercent()
	if pct < 0 {
		return "No battery data"
	}
	return batteryETALevelLine(pct, getBatteryCharging(), "charging", "on cable", batteryETA(), nil)
}

// plugUsedLine is "RAM 412M free of 1.0G (60% used)".
func plugUsedLine(label string, free, total uint64) string {
	if total == 0 || free > total {
		return label + " " + plugHumanBytes(free) + " free"
	}
	used := int((total - free) * 100 / total)
	return fmt.Sprintf("%s %s free of %s (%d%% used)", label, plugHumanBytes(free), plugHumanBytes(total), used)
}

// plugHeatLine is the chip temperature and how long the device has been on.
func plugHeatLine() string {
	var parts []string
	if c, ok := cpuTempValueC(); ok {
		note := ""
		switch {
		case c >= 80:
			note = " · hot: a lighter Power mode cools it down"
		case c >= 70:
			note = " · warm"
		}
		parts = append(parts, fmt.Sprintf("CPU %.0f °C%s", c, note))
	}
	if up, err := plugUptime(); err == nil && up > 0 {
		parts = append(parts, "on for "+plugDuration(up))
	}
	if len(parts) == 0 {
		return "Temperature and uptime are not reported here"
	}
	return strings.Join(parts, " · ")
}

func plugNativeSysinfo(action string, _ map[string]interface{}, _ string) (string, error) {
	switch action {
	case "memory":
		if free, total := plugMemory(); free > 0 {
			return plugUsedLine("RAM", free, total), nil
		}
		return "Memory info unavailable", nil
	case "disk":
		if free, total := plugDisk(); free > 0 {
			return plugUsedLine("Card", free, total), nil
		}
		return "Disk info unavailable", nil
	case "power":
		return plugBatteryLine(), nil
	case "heat", "uptime":
		return plugHeatLine(), nil
	case "status":
		// The first line is the toast, so it carries the short form of
		// everything; the Plugins page shows the detail lines under it.
		ram, ramTotal := plugMemory()
		disk, diskTotal := plugDisk()
		var short, long []string
		if ram > 0 {
			short = append(short, "RAM "+plugHumanBytes(ram)+" free")
			long = append(long, plugUsedLine("RAM", ram, ramTotal))
		}
		if disk > 0 {
			short = append(short, "Card "+plugHumanBytes(disk)+" free")
			long = append(long, plugUsedLine("Card", disk, diskTotal))
		}
		if pct := getBatteryPercent(); pct >= 0 {
			short = append(short, fmt.Sprintf("Battery %d%%", pct))
		}
		if c, ok := cpuTempValueC(); ok {
			short = append(short, fmt.Sprintf("%.0f °C", c))
		}
		if len(short) == 0 {
			return "System info unavailable", nil
		}
		long = append(long, plugBatteryLine(), plugHeatLine())
		return strings.Join(short, " · ") + "\n" + strings.Join(long, "\n"), nil
	}
	return "", plugUnknown(action, "status, power, heat, memory, disk")
}

// ── The extension plugins' descriptions ──────────────────────────────────
//
// Their real work is declarative (plugx.go) or happens on the main thread
// (plughooks.go); these are the lines their "about" buttons print, written so
// the first line reads well as a toast when the plugin is switched on.

func plugNativeClassicPack(action string, _ map[string]interface{}, _ string) (string, error) {
	if action != "about" {
		return "", plugUnknown(action, "about, apply, undo")
	}
	return "Pocket Classic is in Tools › Experience packs: press Apply it now to try it.\n" +
		"The Game Boy palette, the hero-tile Home, the Gaming controls and an Arcade Corner screen.\n" +
		"Undo puts your previous look back.", nil
}

func plugNativeNeonTheme(action string, _ map[string]interface{}, _ string) (string, error) {
	if action != "about" {
		return "", plugUnknown(action, "about, apply, restore")
	}
	return "Neon Grid is in Theme presets: press Use it now to switch.\n" +
		"Lime light on a deep teal night. Back to my theme switches back.", nil
}

func plugNativePadProfiles(action string, _ map[string]interface{}, _ string) (string, error) {
	if action != "about" {
		return "", plugUnknown(action, "about, use, default")
	}
	return "Couch remote is in Settings › Controls: press Use Couch remote to switch.\n" +
		"L1 and R1 page through lists, hold Y to search, hold Start for Quick Settings,\n" +
		"double-tap Select for a screenshot.", nil
}

func plugNativeRetroFormats(action string, _ map[string]interface{}, _ string) (string, error) {
	if action != "list" {
		return "", plugUnknown(action, "list, library")
	}
	return "The game library now knows 6 more formats.\n" +
		"Pokémon mini (.min)\nWatara Supervision (.sv)\nVectrex (.vec, .gam)\n" +
		"Atari Lynx (.lnx)\nArduboy (.arduboy)\nWASM-4 (.wasm)", nil
}
