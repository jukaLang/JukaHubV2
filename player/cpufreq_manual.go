package main

// cpufreq_manual.go — the CPU frequency, set by hand.
//
// Juka (2026-11-25): "In settings, add ability to adjust CPU frequency
// manually". Until now every clock on the handheld was JukaHub's own pick:
// Automatic CPU speed (autoperf.go) follows the app in front, CPU boost in
// games (cpuboost.go) and the heavy-game boost (xboxboost.go) hold the top
// for the heavy consoles, Faster menus (uiboost.go) and Faster game starts
// (faststart.go) lift it for a moment, and a game's own options
// (gameoptions.go) pick a profile or a limit for that one game.
//
// Settings → Power & battery → "CPU frequency" (the same rows sit under
// Automatic CPU speed in Games & emulators) is the manual override:
//
//   - Automatic (the default) leaves all of the above as it was;
//   - a clock from the chip's own table (the fastest policy's operating
//     points, in MHz from the top down) holds the CPU there: the ceiling at
//     that clock and, unless "Slow down when idle" is on, the floor too, so no
//     governor runs it slower or faster. The A523's second cluster gets the
//     same share of its own range, as with Automatic CPU speed;
//   - "CPU frequency for" picks where: menus and games (the default), only
//     JukaHub's own screens, or only the games, ports and apps it starts.
//     Elsewhere the clock stays automatic.
//
// A clock set by hand is an Automatic CPU speed session of its own kind
// (autoPerfSession.fixed): the same writes, compare-and-restore, hand-overs
// between screens and crash marker, with the boosts standing aside and
// whether Automatic CPU speed is on or off. Three things still win over it:
//
//   - the heat guard (heatguard.go): its dial lowers any clock while the chip
//     or the battery is hot and gives it back once they cool. As with the fan,
//     no setting may cook the SoC;
//   - a game's own CPU options (its profile or its CPU limit), the more
//     specific choice;
//   - the idle screens (saver_power.go), which lower the clock a few seconds
//     after they come up and put it back on wake, and the voice assistant's
//     short boost (voiceboost.go).
//
// A game's session begins cpuBoostSettle after its start, once the launch
// script has set its own clock, so the values that go back afterwards are the
// script's; its first seconds run under the launch profile (powerprofile.go).
// A change made in Settings is in force at once in the hub (cpuFreqApplyNow).
// Neither the "Full speed while playing" ceiling nor powerProfileMaxFreq caps
// a clock picked here: the row only offers what the chip has. The governor is
// never touched. Only on a TrimUI handheld whose firmware lets apps write the
// CPU speed; elsewhere the row says why.

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// cpuFreqKey is the row: cpuFreqAuto (the default) or a clock, read like
	// powerProfileMaxFreq ("1416 MHz", "1.4 GHz", 1416000 in kHz).
	cpuFreqKey  = "cpuFrequency"
	cpuFreqAuto = "auto"

	// cpuFreqUseKey is "CPU frequency for": cpuFreqUseAll (the default),
	// cpuFreqUseHub or cpuFreqUseGames.
	cpuFreqUseKey   = "cpuFrequencyUse"
	cpuFreqUseAll   = "all"
	cpuFreqUseHub   = "hub"
	cpuFreqUseGames = "games"

	// cpuFreqIdleKey is "Slow down when idle" (off unless set): the ceiling
	// only, so a governor that follows the load may run the CPU lower.
	cpuFreqIdleKey = "cpuFrequencyIdle"
)

// The scopes as the ticker reads them (cpuFreqScope).
const (
	cpuFreqScopeAll int32 = iota
	cpuFreqScopeHub
	cpuFreqScopeGames
)

// cpuFreqFixed is the clock set by hand for one session: khz 0 is none (the
// clock is automatic there); idle is "Slow down when idle".
type cpuFreqFixed struct {
	khz  int64
	idle bool
}

// What the rows say, published by the main thread (cpuFreqPublish) for
// Automatic CPU speed's ticker: the clock in kHz, already one of the chip's
// (0 = Automatic), where it holds, and "Slow down when idle".
var (
	cpuFreqSetKHz atomic.Int64
	cpuFreqScope  atomic.Int32
	cpuFreqIdleOn atomic.Bool
	// cpuFreqDirty is a change the ticker has not put in force yet.
	cpuFreqDirty atomic.Bool
)

// cpuFreqTable caches the clocks the row offers.
var cpuFreqTable struct {
	mu     sync.Mutex
	at     time.Time
	levels []int64
}

// cpuFreqNow caches the clock the row's line shows.
var cpuFreqNow struct {
	mu  sync.Mutex
	at  time.Time
	khz int64
}

// ── Settings values ──────────────────────────────────────────────────────

// cpuFreqStoredKHz is the row's clock as stored, in kHz; 0 is Automatic.
func cpuFreqStoredKHz(c *Config) int64 {
	if c == nil {
		return 0
	}
	v, ok := customValue(c.Variables.Custom, cpuFreqKey)
	if !ok {
		return 0
	}
	return parseProfileMaxFreqKHz(v)
}

// cpuFreqUse reads "CPU frequency for"; menus and games unless set.
func cpuFreqUse(c *Config) string {
	if c == nil {
		return cpuFreqUseAll
	}
	raw, _ := customString(c.Variables.Custom, cpuFreqUseKey)
	switch u := strings.ToLower(strings.TrimSpace(raw)); u {
	case cpuFreqUseHub, cpuFreqUseGames:
		return u
	}
	return cpuFreqUseAll
}

// cpuFreqIdle reads "Slow down when idle"; off unless set.
func cpuFreqIdle(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, cpuFreqIdleKey, false)
}

// cpuFreqHolds reports whether a clock set by hand holds the games, ports and
// apps JukaHub starts (game) or its own screens: the rows of the features
// that stand aside for it. Main thread.
func cpuFreqHolds(c *Config, game bool) bool {
	if !autoPerfSupported() || cpuFreqStoredKHz(c) <= 0 {
		return false
	}
	switch cpuFreqUse(c) {
	case cpuFreqUseHub:
		return !game
	case cpuFreqUseGames:
		return game
	}
	return true
}

// ── The chip's clocks ────────────────────────────────────────────────────

// cpuFreqLevels is the fastest policy's operating points, ascending: the
// clocks the row offers and the ones a session holds (autoPerfLevelTable).
// Read again at most every minute (a parked cluster hides its policy), every
// few seconds while none were read. Callers must not change the slice. Any
// goroutine.
func cpuFreqLevels() []int64 {
	if !autoPerfSupported() {
		return nil
	}
	cpuFreqTable.mu.Lock()
	defer cpuFreqTable.mu.Unlock()
	every := time.Minute
	if len(cpuFreqTable.levels) == 0 {
		every = 5 * time.Second
	}
	if now := time.Now(); cpuFreqTable.at.IsZero() || now.Sub(cpuFreqTable.at) >= every {
		cpuFreqTable.at = now
		if dirs := autoPerfPolicyDirs(); len(dirs) > 0 {
			if levels := autoPerfLevelTable(dirs[0], 0); len(levels) > 0 {
				cpuFreqTable.levels = levels
			}
		}
	}
	return cpuFreqTable.levels
}

// cpuFreqSnap is the clock of levels nearest khz (the lower of two as near),
// or khz itself without a table. Pure.
func cpuFreqSnap(levels []int64, khz int64) int64 {
	if khz <= 0 || len(levels) == 0 {
		return khz
	}
	best := levels[0]
	for _, f := range levels[1:] {
		if abs64(f-khz) < abs64(best-khz) {
			best = f
		}
	}
	return best
}

// cpuFreqLabel is a clock as the row writes it: "1416 MHz". Pure.
func cpuFreqLabel(khz int64) string {
	if khz%1000 == 0 {
		return strconv.FormatInt(khz/1000, 10) + " MHz"
	}
	return strconv.FormatFloat(float64(khz)/1000, 'f', 1, 64) + " MHz"
}

// cpuFreqNowKHz is the fastest policy's clock right now, read at most once a
// second (the row's line is drawn every frame).
func cpuFreqNowKHz() int64 {
	cpuFreqNow.mu.Lock()
	defer cpuFreqNow.mu.Unlock()
	if now := time.Now(); cpuFreqNow.at.IsZero() || now.Sub(cpuFreqNow.at) >= time.Second {
		cpuFreqNow.at = now
		cpuFreqNow.khz = cpufreqCurrentKHz(cpufreqRoot)
	}
	return cpuFreqNow.khz
}

// cpuFreqWhyNot says why the rows cannot work here, "" when they can.
func cpuFreqWhyNot() string {
	autoPerfShown.mu.Lock()
	reason := autoPerfShown.reason
	autoPerfShown.mu.Unlock()
	switch {
	case reason != "":
		return reason
	case len(cpuFreqLevels()) == 0:
		return "this device has no CPU speed control"
	}
	return ""
}

// ── The ticker's side (autoperf.go) ──────────────────────────────────────

// cpuFreqPublish tells Automatic CPU speed's ticker what the rows say and
// wakes it when that changed. Main thread: autoPerfFrame, twice a second
// (so a value the phone page or Juka wrote is seen too), and the rows.
func cpuFreqPublish(c *Config) {
	khz := cpuFreqSnap(cpuFreqLevels(), cpuFreqStoredKHz(c))
	scope := cpuFreqScopeAll
	switch cpuFreqUse(c) {
	case cpuFreqUseHub:
		scope = cpuFreqScopeHub
	case cpuFreqUseGames:
		scope = cpuFreqScopeGames
	}
	idle := cpuFreqIdle(c)
	changed := cpuFreqSetKHz.Swap(khz) != khz
	changed = cpuFreqScope.Swap(scope) != scope || changed
	changed = cpuFreqIdleOn.Swap(idle) != idle || changed
	if changed {
		cpuFreqDirty.Store(true)
		uiBoostWake()
	}
}

// cpuFreqFixedFor is the clock set by hand for the session in front: a game,
// port or app JukaHub started (game), or one of JukaHub's own screens. The
// zero value when the clock is automatic there. Any goroutine.
func cpuFreqFixedFor(game bool) cpuFreqFixed {
	khz := cpuFreqSetKHz.Load()
	if khz <= 0 {
		return cpuFreqFixed{}
	}
	switch cpuFreqScope.Load() {
	case cpuFreqScopeHub:
		if game {
			return cpuFreqFixed{}
		}
	case cpuFreqScopeGames:
		if !game {
			return cpuFreqFixed{}
		}
	}
	return cpuFreqFixed{khz: khz, idle: cpuFreqIdleOn.Load()}
}

// cpuFreqApplyNow puts a change of the rows in force at once while the hub is
// in front, rather than at the ticker's next sample; a game's session takes it
// at its next tick. Automatic CPU speed's ticker goroutine (autoPerfLoop),
// woken by cpuFreqPublish.
func cpuFreqApplyNow(now time.Time) {
	if !cpuFreqDirty.Swap(false) {
		return
	}
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	if autoPerf.stopped || autoPerf.denied || !autoPerfHubInFrontLocked() {
		return
	}
	if !heatGuard.inited {
		// The heat guard reads the chip first, so a clock set on a hot one
		// starts under its dial.
		heatGuardTickLocked(now)
	}
	autoPerfFollowLocked(now, 0, 0, false)
}

// ── Settings → Power & battery → CPU frequency ───────────────────────────

// cpuFreqOptions are the row's stops: Automatic, then the chip's clocks from
// the top down (a press past Automatic must not drop to the slowest one).
func cpuFreqOptions(*Config) []setOption {
	levels := cpuFreqLevels()
	out := make([]setOption, 0, len(levels)+1)
	out = append(out, setOption{Value: cpuFreqAuto, Label: "Automatic",
		Note: "JukaHub picks: slower on menus, faster for games, cooler when hot"})
	if len(levels) == 0 {
		return out
	}
	top := levels[len(levels)-1]
	for i := len(levels) - 1; i >= 0; i-- {
		label := cpuFreqLabel(levels[i])
		out = append(out, setOption{Value: label, Label: label, Note: cpuFreqNote(levels[i], top)})
	}
	return out
}

// cpuFreqNote is a clock's line in the list, by its share of the top. Pure.
func cpuFreqNote(khz, top int64) string {
	if top <= 0 {
		return ""
	}
	if khz >= top {
		return "Top speed: the most heat and battery use"
	}
	pct := khz * 100 / top
	switch {
	case pct >= 75:
		return "Fast: for heavy games"
	case pct >= 50:
		return "Medium: most games and the menus"
	case pct >= 30:
		return "Slow: light games and the menus, saves battery"
	}
	return "Slowest: saves the most battery, may stutter"
}

// cpuFreqCurrent is the stop the row shows: the chip's clock nearest the one
// stored, the one the sessions hold.
func cpuFreqCurrent(c *Config) int {
	khz := cpuFreqStoredKHz(c)
	if khz <= 0 {
		return 0
	}
	levels := cpuFreqLevels()
	snap := cpuFreqSnap(levels, khz)
	for i, f := range levels {
		if f == snap {
			return len(levels) - i // Automatic first, then the top down
		}
	}
	return -1
}

// cpuFreqSheetValue is the stop the list marks.
func cpuFreqSheetValue(c *Config) string {
	opts := cpuFreqOptions(c)
	if i := cpuFreqCurrent(c); i >= 0 && i < len(opts) {
		return opts[i].Value
	}
	return ""
}

// cpuFreqValue is the row's value when the stored clock is not a stop (no
// table was read).
func cpuFreqValue(c *Config) string {
	if khz := cpuFreqStoredKHz(c); khz > 0 {
		return cpuFreqLabel(khz)
	}
	return "Automatic"
}

// cpuFreqChoose stores a stop and puts it in force.
func cpuFreqChoose(c *Config, value string) {
	if c == nil {
		return
	}
	opts := cpuFreqOptions(c)
	i := ledOptionIndex(opts, value)
	if i < 0 {
		return
	}
	setStoreString(c, cpuFreqKey, opts[i].Value)
	cpuFreqPublish(c)
	if i == 0 {
		logInfo("power", "CPU frequency: automatic again")
		setToast("CPU frequency: Automatic")
		return
	}
	logInfo("power", "CPU frequency set by hand: %s (for %s, slow down when idle %s)",
		opts[i].Label, cpuFreqUse(c), setOnOff(cpuFreqIdle(c)))
	setToast("CPU frequency: " + opts[i].Label)
}

// cpuFreqSettingsRow is "CPU frequency": in Power & battery, under Battery,
// and in Games & emulators, under Automatic CPU speed; on a TrimUI handheld.
func cpuFreqSettingsRow() setRow {
	return setRow{
		ID: cpuFreqKey, Label: "CPU frequency", Kind: setKindChoice, Clamp: true,
		Visible:    func(*Config) bool { return autoPerfSupported() },
		Disabled:   cpuFreqUnavailable,
		Detail:     cpuFreqSettingDetail,
		Options:    cpuFreqOptions,
		Current:    cpuFreqCurrent,
		Sheet:      cpuFreqOptions,
		SheetValue: cpuFreqSheetValue,
		SheetTitle: "CPU frequency",
		Value:      cpuFreqValue,
		Choose:     cpuFreqChoose,
	}
}

// cpuFreqUnavailable greys the rows out where they cannot work.
func cpuFreqUnavailable(*Config) bool { return cpuFreqWhyNot() != "" }

// cpuFreqManualRowVisible shows the two rows under "CPU frequency" once a
// clock is set by hand.
func cpuFreqManualRowVisible(c *Config) bool {
	return autoPerfSupported() && cpuFreqStoredKHz(c) > 0
}

// cpuFreqSettingDetail is the row's second line. Cheap: drawn every frame
// (the table and the clock are cached).
func cpuFreqSettingDetail(c *Config) string {
	if !autoPerfSupported() {
		return "Only on the handheld"
	}
	if why := cpuFreqWhyNot(); why != "" {
		return "Not available: " + why
	}
	now := ""
	if cur := cpuFreqNowKHz(); cur > 0 {
		now = " · now " + strconv.FormatInt((cur+500)/1000, 10) + " MHz"
	}
	khz := cpuFreqStoredKHz(c)
	if khz <= 0 {
		return "Automatic: JukaHub sets the speed for what is on screen" + now
	}
	held := cpuFreqSnap(cpuFreqLevels(), khz)
	line := cpuFreqLabel(held) + " "
	switch cpuFreqUse(c) {
	case cpuFreqUseHub:
		line += "in the menus"
	case cpuFreqUseGames:
		line += "in games and apps"
	default:
		line += "in menus and games"
	}
	if cpuFreqIdle(c) {
		line = "Up to " + line
	}
	heatShown.mu.Lock()
	holding, capKHz := heatShown.holding, heatShown.capKHz
	heatShown.mu.Unlock()
	if holding && capKHz > 0 && capKHz < held {
		return line + " · held at " + cpuFreqLabel(capKHz) + " while it cools"
	}
	return line + now
}

// ── … → CPU frequency for ────────────────────────────────────────────────

func cpuFreqUseOptions(*Config) []setOption {
	return []setOption{
		{Value: cpuFreqUseAll, Label: "Menus and games", Note: "Everywhere: Home, the menus, games, ports and apps"},
		{Value: cpuFreqUseHub, Label: "Menus only", Note: "JukaHub's own screens, video and music; games stay automatic"},
		{Value: cpuFreqUseGames, Label: "Games only", Note: "Every game, port and app JukaHub starts; the menus stay automatic"},
	}
}

// cpuFreqUseSettingsRow is "CPU frequency for", under "CPU frequency" once a
// clock is set.
func cpuFreqUseSettingsRow() setRow {
	return setRow{
		ID: cpuFreqUseKey, Label: "CPU frequency for", Kind: setKindChoice,
		Visible:  cpuFreqManualRowVisible,
		Disabled: cpuFreqUnavailable,
		Detail:   cpuFreqUseDetail,
		Options:  cpuFreqUseOptions,
		Current:  func(c *Config) int { return ledOptionIndex(cpuFreqUseOptions(c), cpuFreqUse(c)) },
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			opts := cpuFreqUseOptions(c)
			i := ledOptionIndex(opts, value)
			if i < 0 {
				return
			}
			setStoreString(c, cpuFreqUseKey, opts[i].Value)
			cpuFreqPublish(c)
			setToast("CPU frequency for: " + opts[i].Label)
		},
	}
}

// cpuFreqUseDetail is the row's second line.
func cpuFreqUseDetail(c *Config) string {
	switch cpuFreqUse(c) {
	case cpuFreqUseHub:
		return "Games, ports and apps keep the automatic speed"
	case cpuFreqUseGames:
		return "Home and the menus keep the automatic speed"
	}
	return "Everywhere, except games with their own CPU options"
}

// ── … → Slow down when idle ──────────────────────────────────────────────

// cpuFreqIdleSettingsRow is "Slow down when idle", under "CPU frequency for".
func cpuFreqIdleSettingsRow() setRow {
	return setRow{
		ID: cpuFreqIdleKey, Label: "Slow down when idle", Kind: setKindToggle,
		Visible:  cpuFreqManualRowVisible,
		Disabled: cpuFreqUnavailable,
		Detail:   cpuFreqIdleDetail,
		Get:      cpuFreqIdle,
		Set: func(c *Config, on bool) {
			setStoreBool(c, cpuFreqIdleKey, on)
			cpuFreqPublish(c)
			setToast("Slow down when idle: " + setOnOff(on))
		},
	}
}

// cpuFreqIdleDetail is the row's second line.
func cpuFreqIdleDetail(c *Config) string {
	clock := cpuFreqLabel(cpuFreqSnap(cpuFreqLevels(), cpuFreqStoredKHz(c)))
	if cpuFreqIdle(c) {
		return "Up to " + clock + ": the CPU slows down when there is little to do"
	}
	return "Off: the CPU stays at " + clock
}

// ── Juka (aitools_games.go performance_set) ──────────────────────────────

// cpuFreqOptionFor maps what someone asked for — "auto", "1.4 GHz", "1416",
// "1416 MHz" — to the row's stop: Automatic, or the chip's clock nearest it.
// Any goroutine.
func cpuFreqOptionFor(v string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(v))
	switch raw {
	case "auto", "automatic", "default":
		return cpuFreqAuto, true
	}
	khz := parseProfileMaxFreqKHz(raw)
	levels := cpuFreqLevels()
	if khz <= 0 || len(levels) == 0 {
		return "", false
	}
	return cpuFreqLabel(cpuFreqSnap(levels, khz)), true
}
