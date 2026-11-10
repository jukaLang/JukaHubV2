package main

// plugnative.go — the bundled plugins, written in Go.
//
// Every plugin that ships with JukaHub is a shell script, and Windows has no
// sh unless Git for Windows happens to be installed. So on Windows nine of the
// thirteen bundled plugins said "Can't run here", their buttons were greyed
// out, and switching one on or off visibly changed nothing. The same scripts
// on the handheld also paid a fork of sh (plus awk, sed, date…) per press.
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
// the game library…) are in plughooks.go and run on the main thread.

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
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// plugNativeAuthor is the author every bundled manifest carries. A user's own
// plugin that happens to share a bundled folder name ("clock") is not taken
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
		"classicpack":  plugNativeClassicPack,
		"clock":        plugNativeClock,
		"dice":         plugNativeDice,
		"gamepick":     plugNativeGamePick,
		"hello":        plugNativeHello,
		"neontheme":    plugNativeNeonTheme,
		"notes":        plugNativeNotes,
		"padprofiles":  plugNativePadProfiles,
		"quote":        plugNativeQuote,
		"retroformats": plugNativeRetroFormats,
		"sysinfo":      plugNativeSysinfo,
		"tips":         plugNativeTips,
		"worldclock":   plugNativeWorldClock,
	}
}

// plugNativeClock12 mirrors the user's 12-hour clock setting for the plugin
// goroutine, which must not read the config. The main thread stores it every
// time it starts an action (plugDispatch).
var plugNativeClock12 atomic.Bool

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

// plugVarOff reports whether a variable is set to an "off" word.
func plugVarOff(vars map[string]interface{}, key string) bool {
	switch strings.ToLower(plugVar(vars, key)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// plugVarOn reports whether a variable is set to an "on" word.
func plugVarOn(vars map[string]interface{}, key string) bool {
	switch strings.ToLower(plugVar(vars, key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// plugUnknown is the error for an action a plugin does not have.
func plugUnknown(action, try string) error {
	return fmt.Errorf("unknown action %q (try %s)", action, try)
}

// plugClockLayout is the time layout for a plugin: the user's clock setting,
// unless the plugin's own variable (clock_24h, worldclock_24h) says otherwise.
func plugClockLayout(vars map[string]interface{}, key string) string {
	h12 := plugNativeClock12.Load()
	if plugVarOff(vars, key) {
		h12 = true
	} else if plugVarOn(vars, key) {
		h12 = false
	}
	if h12 {
		return "3:04 PM"
	}
	return "15:04"
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

// ── Clock ─────────────────────────────────────────────────────────────────

func plugNativeClock(action string, vars map[string]interface{}, _ string) (string, error) {
	now := time.Now()
	layout := plugClockLayout(vars, "clock_24h")
	switch action {
	case "now":
		return now.Format(layout), nil
	case "date":
		return now.Format("Mon 02 Jan 2006"), nil
	case "both":
		return now.Format("Mon 02 Jan · " + layout), nil
	case "uptime":
		up, err := plugUptime()
		if err != nil || up <= 0 {
			return "Uptime unavailable on this system", nil
		}
		return "Up " + plugDuration(up), nil
	}
	return "", plugUnknown(action, "now, date, both, uptime")
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

// ── Dice ──────────────────────────────────────────────────────────────────

func plugNativeDice(action string, vars map[string]interface{}, _ string) (string, error) {
	switch action {
	case "roll":
		sides := 6
		if s := plugVar(vars, "dice_sides"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				return "", fmt.Errorf("sides must be a number, not %s", s)
			}
			sides = n
		}
		if sides < 2 || sides > 1000 {
			return "", errors.New("sides must be between 2 and 1000")
		}
		return fmt.Sprintf("Rolled %d of %d", rand.IntN(sides)+1, sides), nil
	case "coin":
		if rand.IntN(2) == 0 {
			return "Heads", nil
		}
		return "Tails", nil
	case "pick":
		list := plugVar(vars, "dice_list")
		if list == "" {
			list = "heads,tails"
		}
		items := strings.Split(list, ",")
		chosen := strings.TrimSpace(items[rand.IntN(len(items))])
		if chosen == "" {
			chosen = "(empty entry)"
		}
		return "Picked: " + chosen, nil
	}
	return "", plugUnknown(action, "roll, coin, pick")
}

// ── Random game ───────────────────────────────────────────────────────────

// plugGameSkipExt are files in a Roms folder that are not games.
var plugGameSkipExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".txt": true, ".xml": true, ".db": true,
	".json": true, ".cfg": true, ".ini": true, ".srm": true, ".sav": true, ".log": true,
	".bak": true, ".nfo": true, ".md": true, ".dat": true, ".webp": true, ".gif": true,
}

// plugGameSkipDir are artwork and metadata folders inside a system folder.
var plugGameSkipDir = map[string]bool{"imgs": true, "media": true, "images": true, "boxart": true, "snap": true, "snaps": true}

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
	return "", plugUnknown(action, "pick, count")
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
		if strings.HasPrefix(name, ".") {
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

// ── Hello ─────────────────────────────────────────────────────────────────

func plugNativeHello(action string, vars map[string]interface{}, _ string) (string, error) {
	if action != "greet" {
		return "", plugUnknown(action, "greet")
	}
	name := plugVar(vars, "plugin_name")
	if name == "" {
		name = "friend"
	}
	greeting := "Hello"
	switch h := time.Now().Hour(); {
	case h >= 5 && h < 12:
		greeting = "Good morning"
	case h >= 12 && h < 18:
		greeting = "Good afternoon"
	case h >= 18 && h < 23:
		greeting = "Good evening"
	}
	return greeting + ", " + name + "!", nil
}

// ── Notes ─────────────────────────────────────────────────────────────────

// plugNotesFile is the pad inside the plugin's folder.
func plugNotesFile(dir string) string { return filepath.Join(dir, "notes.txt") }

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

// plugNotesAdd appends one note with the time it was written.
func plugNotesAdd(dir, text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", errors.New("nothing to add: the note is empty")
	}
	f, err := os.OpenFile(plugNotesFile(dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("could not open the notes file: %v", err)
	}
	_, werr := fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04"), text)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return "", errors.New("could not save the note")
	}
	return "Saved: " + plugClip(text, 38), nil
}

// plugClip shortens s to n runes with an ellipsis.
func plugClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func plugNativeNotes(action string, vars map[string]interface{}, dir string) (string, error) {
	switch action {
	case "add":
		return plugNotesAdd(dir, plugVar(vars, "notes_text"))
	case "last":
		lines := plugNotesLines(dir)
		if len(lines) == 0 {
			return "No notes yet", nil
		}
		return plugClip(lines[len(lines)-1], 60), nil
	case "count":
		switch n := len(plugNotesLines(dir)); n {
		case 0:
			return "No notes yet", nil
		case 1:
			return "1 note saved", nil
		default:
			return fmt.Sprintf("%d notes saved", n), nil
		}
	case "clear":
		if err := os.WriteFile(plugNotesFile(dir), nil, 0o644); err != nil {
			return "", fmt.Errorf("could not clear the notes: %v", err)
		}
		return "Notes cleared", nil
	}
	return "", plugUnknown(action, "add, last, count, clear")
}

// ── Daily quote ───────────────────────────────────────────────────────────

// plugQuotes are public-domain, one toast line each (quote.sh has the same).
var plugQuotes = []string{
	"Well begun is half done. — Aristotle",
	"The journey of a thousand miles begins with one step. — Lao Tzu",
	"It does not matter how slowly you go, as long as you do not stop. — Confucius",
	"Fortune favours the bold. — Virgil",
	"Knowledge is power. — Francis Bacon",
	"Patience is bitter, but its fruit is sweet. — Jean-Jacques Rousseau",
	"The best way out is always through. — Robert Frost",
	"Fall seven times, stand up eight. — Japanese proverb",
	"Energy and persistence conquer all things. — Benjamin Franklin",
	"Well done is better than well said. — Benjamin Franklin",
	"Lost time is never found again. — Benjamin Franklin",
	"One today is worth two tomorrows. — Benjamin Franklin",
	"Waste no more time arguing what a good person should be. Be one. — Marcus Aurelius",
	"Dwell on the beauty of life. — Marcus Aurelius",
	"No great thing is created suddenly. — Epictetus",
	"First say to yourself what you would be; then do what you have to do. — Epictetus",
	"All that glitters is not gold. — William Shakespeare",
	"A merry heart goes all the day. — William Shakespeare",
	"Hope is the thing with feathers. — Emily Dickinson",
	"The only way to have a friend is to be one. — Ralph Waldo Emerson",
	"The harder the conflict, the more glorious the triumph. — Thomas Paine",
	"Turn your face to the sun and the shadows fall behind you. — Maori proverb",
	"Game over is just a long loading screen. Press Start. — JukaHub",
}

// plugQuoteOfDay is the same quote all day, as quote.sh picks it.
func plugQuoteOfDay(t time.Time) string {
	return plugQuotes[t.YearDay()%len(plugQuotes)]
}

func plugNativeQuote(action string, _ map[string]interface{}, _ string) (string, error) {
	switch action {
	case "today":
		return plugQuoteOfDay(time.Now()), nil
	case "random":
		return plugQuotes[rand.IntN(len(plugQuotes))], nil
	}
	return "", plugUnknown(action, "today, random")
}

// ── System info ───────────────────────────────────────────────────────────

// plugFreeRAM is the available memory in bytes, 0 when unknown.
func plugFreeRAM() (b uint64) {
	defer func() {
		if recover() != nil {
			b = 0
		}
	}()
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil {
		return 0
	}
	return vm.Available
}

// plugFreeDisk is the free space where JukaHub lives, 0 when unknown.
func plugFreeDisk() uint64 {
	_, free, err := sysToolVolumeUsage(sysToolAppVolumePath())
	if err != nil {
		return 0
	}
	return free
}

// plugBatteryLine is "Battery 80%", "Battery 80% · charging" or a plain
// sentence when there is no battery to read.
func plugBatteryLine() string {
	pct := getBatteryPercent()
	if pct < 0 {
		return "No battery data"
	}
	line := fmt.Sprintf("Battery %d%%", pct)
	if getBatteryCharging() {
		line += " · charging"
	}
	return line
}

func plugNativeSysinfo(action string, _ map[string]interface{}, _ string) (string, error) {
	switch action {
	case "memory":
		if ram := plugFreeRAM(); ram > 0 {
			return "RAM " + plugHumanBytes(ram) + " free", nil
		}
		return "Memory info unavailable", nil
	case "disk":
		if disk := plugFreeDisk(); disk > 0 {
			return "Disk " + plugHumanBytes(disk) + " free", nil
		}
		return "Disk info unavailable", nil
	case "power":
		return plugBatteryLine(), nil
	case "status":
		ram, disk := plugFreeRAM(), plugFreeDisk()
		switch {
		case ram > 0 && disk > 0:
			return "RAM " + plugHumanBytes(ram) + " free | Disk " + plugHumanBytes(disk) + " free", nil
		case ram > 0:
			return "RAM " + plugHumanBytes(ram) + " free", nil
		case disk > 0:
			return "Disk " + plugHumanBytes(disk) + " free", nil
		}
		return "System info unavailable", nil
	}
	return "", plugUnknown(action, "status, memory, disk, power")
}

// ── Tips ──────────────────────────────────────────────────────────────────

var plugTips = []string{
	"Settings › Power & battery: the battery saver steps in by itself at 20%.",
	"Settings › Display & theme: lower the brightness to make a charge last longer.",
	"Settings › Display & theme: Reduce motion is calmer and lighter on the battery.",
	"Settings › General: switch Weather off if you never look at it, and nothing polls.",
	"Theme presets › Total conversion repaints and rearranges the whole interface.",
	"Settings › Network › Phone companion lets you type on your phone instead.",
	"Settings › Games & emulators: Video snaps preview your games in the library.",
	"Plugins you switch on put their buttons in the Start menu, under Plugins.",
	"Settings › Power & battery: a shorter screensaver delay saves the most power.",
	"Settings › Privacy & data: saved browser sign-ins can expire on their own.",
}

// plugBatteryAdvice is the tips plugin's battery line for a reading.
func plugBatteryAdvice(pct int, charging bool) string {
	switch {
	case pct < 0:
		return "No battery here: plugged in for good, nothing to save."
	case charging:
		return fmt.Sprintf("Battery %d%% and charging: a good time for updates.", pct)
	case pct <= 15:
		return fmt.Sprintf("Battery %d%%: save your game and plug in soon.", pct)
	case pct <= 30:
		return fmt.Sprintf("Battery %d%%: Power mode › Battery Saver buys you time.", pct)
	case pct <= 60:
		return fmt.Sprintf("Battery %d%%: dim the screen a step to stretch it.", pct)
	}
	return fmt.Sprintf("Battery %d%%: plenty left. Enjoy!", pct)
}

func plugNativeTips(action string, _ map[string]interface{}, _ string) (string, error) {
	switch action {
	case "tip":
		return plugTips[rand.IntN(len(plugTips))], nil
	case "battery":
		return plugBatteryAdvice(getBatteryPercent(), getBatteryCharging()), nil
	}
	return "", plugUnknown(action, "tip, battery")
}

// ── World clock ───────────────────────────────────────────────────────────

// plugTZRule is a POSIX daylight-saving transition "Mm.w.d/h": the w-th
// weekday d (0 = Sunday; w 5 = the last) of month m, at h o'clock local time.
type plugTZRule struct {
	Month time.Month
	Week  int
	Day   time.Weekday
	Hour  int
}

// plugTZ is one city's zone as a POSIX TZ rule, so no time zone database is
// needed (Windows builds have none unless Go's own is installed).
type plugTZ struct {
	City       string
	Std, Dst   int // offsets east of UTC, in hours
	Start, End *plugTZRule
}

var (
	plugTZEurope = [2]plugTZRule{{time.March, 5, time.Sunday, 2}, {time.October, 5, time.Sunday, 3}}
	plugTZUS     = [2]plugTZRule{{time.March, 2, time.Sunday, 2}, {time.November, 1, time.Sunday, 2}}
	plugTZZones  = map[string]plugTZ{
		"Tokyo":       {City: "Tokyo", Std: 9, Dst: 9},
		"Berlin":      {City: "Berlin", Std: 1, Dst: 2, Start: &plugTZEurope[0], End: &plugTZEurope[1]},
		"London":      {City: "London", Std: 0, Dst: 1, Start: &plugTZRule{time.March, 5, time.Sunday, 1}, End: &plugTZRule{time.October, 5, time.Sunday, 2}},
		"New York":    {City: "New York", Std: -5, Dst: -4, Start: &plugTZUS[0], End: &plugTZUS[1]},
		"Los Angeles": {City: "Los Angeles", Std: -8, Dst: -7, Start: &plugTZUS[0], End: &plugTZUS[1]},
		"Sydney":      {City: "Sydney", Std: 10, Dst: 11, Start: &plugTZRule{time.October, 1, time.Sunday, 2}, End: &plugTZRule{time.April, 1, time.Sunday, 3}},
		"UTC":         {City: "UTC"},
	}
)

// plugTZDate is the day of month a rule falls on in year y.
func plugTZDate(y int, r plugTZRule) int {
	first := time.Date(y, r.Month, 1, 0, 0, 0, 0, time.UTC)
	day := 1 + (int(r.Day)-int(first.Weekday())+7)%7 + (r.Week-1)*7
	last := time.Date(y, r.Month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	for day > last {
		day -= 7
	}
	return day
}

// plugTZAt is the instant a rule fires in year y, given the offset (hours)
// in force just before it.
func plugTZAt(y int, r plugTZRule, before int) time.Time {
	return time.Date(y, r.Month, plugTZDate(y, r), r.Hour, 0, 0, 0, time.UTC).Add(-time.Duration(before) * time.Hour)
}

// In returns t on the zone's wall clock.
func (z plugTZ) In(t time.Time) time.Time {
	off := z.Std
	if z.Start != nil && z.End != nil {
		y := t.UTC().Year()
		start := plugTZAt(y, *z.Start, z.Std)
		end := plugTZAt(y, *z.End, z.Dst)
		var dst bool
		if start.Before(end) {
			dst = !t.Before(start) && t.Before(end)
		} else {
			// Southern hemisphere: summer spans the new year.
			dst = !t.Before(start) || t.Before(end)
		}
		if dst {
			off = z.Dst
		}
	}
	return t.In(time.FixedZone(z.City, off*3600))
}

// plugWorldLine prints "Tokyo 08:41 · Berlin 00:41 …" for the given cities.
func plugWorldLine(t time.Time, layout string, cities ...string) string {
	parts := make([]string, 0, len(cities))
	for _, c := range cities {
		parts = append(parts, c+" "+plugTZZones[c].In(t).Format(layout))
	}
	return strings.Join(parts, " · ")
}

func plugNativeWorldClock(action string, vars map[string]interface{}, _ string) (string, error) {
	layout := plugClockLayout(vars, "worldclock_24h")
	now := time.Now()
	switch action {
	case "cities":
		return plugWorldLine(now, layout, "Tokyo", "Berlin", "London", "New York"), nil
	case "pacific":
		return plugWorldLine(now, layout, "Sydney", "Tokyo", "Los Angeles"), nil
	case "utc":
		return plugWorldLine(now, layout, "UTC"), nil
	}
	return "", plugUnknown(action, "cities, pacific, utc")
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
