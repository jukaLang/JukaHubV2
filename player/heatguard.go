package main

// heatguard.go — the heat guard: whoever sets the CPU's clock, the handheld
// does not overheat.
//
// Juka (2026-11-21): "boost performance when running emulator by adjusting
// CPU frequency safely...make sure to not overheat device by checking
// temperature (All Trimui devices)". CPU boost in games (cpuboost.go) is the
// boost; this file is the "safely", and it watches every session, boosted or
// not.
//
// Before it, only Automatic CPU speed (autoperf.go) looked at the temperature,
// and only for the sessions it clocked itself: no step up from 80 °C, a step
// down from 88 °C, the same on every model. A game with its own CPU profile or
// limit, an Xbox-boosted game, a launch script that set its own ceiling, or
// Automatic CPU speed switched off ran at whatever clock it had however hot
// the chip got, with the kernel's own trip points the only brake.
//
// The guard is one dial: the highest clock the heat allows right now. Every
// session obeys it:
//
//   - Automatic CPU speed and CPU boost keep their clock at or under it. The
//     guard runs in their 2 s tick, under autoPerf.mu, so there is one writer
//     of scaling_max_freq and one crash marker;
//   - a session JukaHub does not clock (its own CPU setting, another tool's
//     ceiling, Automatic CPU speed off) gets its ceiling lowered to the dial
//     while the dial is under it, and its floor too where a script raised one
//     over it (the kernel refuses a ceiling under the floor), and both put
//     back once the dial lifts, only where the value is still the guard's
//     (heatHoldLocked).
//
// What moves the dial is the hottest CPU/GPU zone (hardware.go cpuTempZones)
// and the battery's own reading, against limits made for each model
// (heatBaseLimits): the Brick's small metal back is its heat sink and gets
// uncomfortable sooner than the Smart Pro's bigger body, and the Smart Pro S
// has a fan (fan.go runs it flat out by 75 °C). Settings → Games & emulators
// → Heat limit moves them (Cooler / Standard / Warmer), never to within
// heatKernelMarginC of the kernel's own critical trip, read from the zones.
// One tick:
//
//	cool      under the soft limit by heatHystC: after heatRiseAfter, one
//	          step back up per heatRiseEvery, never sooner than
//	          heatRiseAfterDown after a step down, and into a level that
//	          proved too hot only every heatRetryHot;
//	warm      just under the soft limit: hold, and Automatic CPU speed steps
//	          no higher;
//	hot       the soft limit: one step under the clock in front at once, then
//	          one more per heatSoftWait while the reading is not falling;
//	very hot  the hard limit: one step per heatHardEvery;
//	critical  straight to heatFloorKHz, then lower while it stays there.
//
// Hot and very hot steps stop at the first operating point at or above
// heatFloorKHz (critical ones go on to heatLastKHz): the game slows, it does
// not stop. A RetroArch game gets one line on its message line when the guard
// first slows it, one at critical and one when it is back at full speed
// (heatGuardFrame); the hub gets a toast. The log's `heat` lines name every
// step, and each game's peak when it ends.
//
// There is deliberately no Off: as with the fan, no setting may cook the SoC.
// JUKAHUB_HEAT_GUARD=0 turns it off for one run, for a tester comparing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// heatLimitKey is the Settings row: heatLimitCool, heatLimitStandard (the
	// default) or heatLimitWarm.
	heatLimitKey      = "heatLimit"
	heatLimitCool     = "cool"
	heatLimitStandard = "standard"
	heatLimitWarm     = "warm"

	// "Cool" is this far under the soft limit, so a reading that sits on the
	// limit holds the dial instead of rocking it.
	heatHystC     = 3.0
	heatBattHystC = 2.0
	// heatKernelMarginC keeps the guard's critical limit this far under the
	// kernel's own hot or critical trip, where the kernel acts on its own.
	heatKernelMarginC = 10.0

	// The dial's pace. Heat moves slowly through a metal back: a hot chip
	// gets one step at once, then time to answer before the next; a level
	// that proved too hot is tried again only every heatRetryHot, until the
	// chip has been cool for heatForgetAfter (tuned on a lumped thermal model
	// of the Brick and the Smart Pro: it settles on one clock instead of
	// rocking between three).
	heatSoftWait      = 20 * time.Second
	heatSoftCoolingC  = 0.5
	heatHardEvery     = 4 * time.Second
	heatRiseAfter     = 20 * time.Second
	heatRiseEvery     = 15 * time.Second
	heatRiseAfterDown = 45 * time.Second
	heatRetryHot      = 2 * time.Minute
	heatForgetAfter   = 3 * time.Minute
	// heatTickSlack absorbs the ticker's jitter in the waits above.
	heatTickSlack = 300 * time.Millisecond

	// Hot and very hot steps stop at the first operating point at or above
	// heatFloorKHz (1.0 GHz on the A133P); a critical chip's at heatLastKHz.
	heatFloorKHz = 1_000_000
	heatLastKHz  = 600_000

	// heatMsgTTL drops a game line that could not be shown in time: a paused
	// game, or one with no RetroArch message line.
	heatMsgTTL = 30 * time.Second
	// heatBattEvery is how often a missing battery entry is looked for again.
	heatBattEvery = time.Minute
)

// heatLevel is where the readings stand against the limits.
type heatLevel int

const (
	heatCool     heatLevel = iota // under the soft limit by the hysteresis: the dial may rise
	heatWarm                      // just under the soft limit: hold
	heatSoft                      // the soft limit: step down, then wait
	heatHard                      // the hard limit: a step every heatHardEvery
	heatCritical                  // the critical limit: straight to the floor
)

// heatLimits are the guard's limits in °C: the chip's hottest zone and the
// battery each have a soft, a hard and a critical one.
type heatLimits struct {
	Soft, Hard, Critical             float64
	BattSoft, BattHard, BattCritical float64
}

// heatAdapt is the dial's memory between ticks.
type heatAdapt struct {
	prev       heatLevel // the last tick's level
	prevBatt   bool      // the battery set it
	coolSince  time.Time // cool since; zero while not cool
	stepAt     time.Time // the last step, either way
	downAt     time.Time // the last step down
	probeAt    time.Time // the last try of a level that had proved too hot
	tempAtStep float64   // the deciding reading at the last step down
	tooHot     int       // the lowest level that proved too hot, plus one; 0 none
}

// heatSession is the record of the session in front: its log line when it
// ends, and which of a game's lines went out.
type heatSession struct {
	active    bool
	label     string
	game      bool // a game, emulator, port or app the hub launched
	peakC     float64
	battPeakC float64
	held      time.Duration // how long the dial held its clock down
	lowestKHz int64         // the lowest the dial went meanwhile
	last      time.Time
	slowed    bool // its "slowed" line was queued
	critical  bool // its critical line was queued
	back      bool // its "full speed again" line was queued
}

// heatGuard is the dial and what moves it. Under autoPerf.mu: the guard runs
// in Automatic CPU speed's tick (autoPerfTickOnce), whether or not that is on.
var heatGuard struct {
	inited bool
	off    bool // JUKAHUB_HEAT_GUARD=0
	model  DeviceModel
	mode   string // the Heat limit stop lim was made for
	lim    heatLimits
	kPass  float64 // the kernel's lowest passive trip on a chip zone; 0 none
	kCrit  float64 // its lowest hot or critical trip; 0 none

	levels []int64 // the fastest policy's operating points, ascending
	dial   int     // an index into levels: the last one is "no limit"
	adapt  heatAdapt

	level   heatLevel
	byBatt  bool // the battery set the level
	unknown bool // neither reading: the dial holds
	chipC   float64
	chipOK  bool
	battC   float64
	battOK  bool

	sess    heatSession
	hubSaid bool // the hub's toast went out for this hot spell
}

// heatShown is the guard's last tick, for the Settings rows, the in-game
// menu's system line and the drivers page. Any goroutine.
var heatShown struct {
	mu      sync.Mutex
	seen    bool
	off     bool
	chipC   float64
	chipOK  bool
	battC   float64
	battOK  bool
	level   heatLevel
	byBatt  bool
	unknown bool
	capKHz  int64 // the dial; 0 = no limit
	holding bool  // the dial holds the clock in front down now
	lim     heatLimits
}

// heatLimitNow is the Heat limit stop, published by the main thread
// (heatGuardFrame) for the ticker.
var heatLimitNow atomic.Value // string

// heatMsg is the line waiting for the main thread: a game's message line, or
// a toast over the hub.
var heatMsg struct {
	mu   sync.Mutex
	text string
	game bool
	at   time.Time
}

// heatTrips caches the kernel's trip points, read once.
var heatTrips struct {
	once     sync.Once
	passive  float64
	critical float64
}

// heatBatt is the battery entry the guard reads. Ticker only.
var heatBatt struct {
	dir    string
	lookAt time.Time
}

// ── Limits ───────────────────────────────────────────────────────────────

// heatLimitMode reads the Heat limit stop; Standard unless set.
func heatLimitMode(c *Config) string {
	if c == nil {
		return heatLimitStandard
	}
	raw, _ := customString(c.Variables.Custom, heatLimitKey)
	switch m := strings.ToLower(strings.TrimSpace(raw)); m {
	case heatLimitCool, heatLimitWarm:
		return m
	}
	return heatLimitStandard
}

// heatLimitLoad is the stop the main thread last published.
func heatLimitLoad() string {
	if v, ok := heatLimitNow.Load().(string); ok && v != "" {
		return v
	}
	return heatLimitStandard
}

func heatLimitLabel(m string) string {
	switch m {
	case heatLimitCool:
		return "Cooler"
	case heatLimitWarm:
		return "Warmer"
	}
	return "Standard"
}

// heatBaseLimits are a model's Standard limits. The Smart Pro keeps the
// thresholds Automatic CPU speed always used (no faster from 80 °C, down from
// 88 °C); the Brick family's smaller body, whose metal back is the heat sink,
// starts earlier; the Smart Pro S has its fan. An unidentified TrimUI gets the
// Brick's, the smallest body. The battery's are the same everywhere: a
// lithium cell should stay under about 45 °C, and well under 60 °C.
func heatBaseLimits(d DeviceModel) heatLimits {
	l := heatLimits{BattSoft: 45, BattHard: 50, BattCritical: 55}
	switch d {
	case DeviceTrimuiSmartPro:
		l.Soft, l.Hard, l.Critical = 80, 87, 95
	case DeviceTrimuiSmartProS:
		l.Soft, l.Hard, l.Critical = 82, 88, 95
	case DeviceTrimuiBrickPro:
		l.Soft, l.Hard, l.Critical = 76, 83, 92
	default:
		l.Soft, l.Hard, l.Critical = 75, 82, 92
	}
	return l
}

// heatLimitsFor is a model's limits for a Heat limit stop, under the kernel's
// own hot or critical trip by heatKernelMarginC (0: none read), each limit at
// least a few degrees under the next. Pure.
func heatLimitsFor(d DeviceModel, mode string, kernelCritC float64) heatLimits {
	l := heatBaseLimits(d)
	switch mode {
	case heatLimitCool:
		l.Soft, l.Hard, l.Critical = l.Soft-6, l.Hard-5, l.Critical-3
		l.BattSoft, l.BattHard = l.BattSoft-3, l.BattHard-2
	case heatLimitWarm:
		l.Soft, l.Hard = l.Soft+4, l.Hard+4
		l.BattSoft, l.BattHard = l.BattSoft+2, l.BattHard+2
	}
	if kernelCritC > 0 && l.Critical > kernelCritC-heatKernelMarginC {
		l.Critical = kernelCritC - heatKernelMarginC
	}
	l.Hard = min(l.Hard, l.Critical-3)
	l.Soft = min(l.Soft, l.Hard-3)
	l.BattHard = min(l.BattHard, l.BattCritical-2)
	l.BattSoft = min(l.BattSoft, l.BattHard-2)
	return l
}

// heatKernelTrips reads, once, the chip zones' trip points: the lowest
// passive one (where the kernel starts to throttle) and the lowest hot or
// critical one (where it acts on its own, up to a shutdown). 0 = none read.
func heatKernelTrips() (passive, critical float64) {
	heatTrips.once.Do(func() {
		for _, z := range cpuTempZones() {
			types, _ := filepath.Glob(filepath.Join(filepath.Dir(z.path), "trip_point_*_type"))
			for _, t := range types {
				v, ok := readThermalMilliC(strings.TrimSuffix(t, "_type") + "_temp")
				if !ok || v < 40 {
					continue
				}
				switch strings.ToLower(strings.TrimSpace(fanReadString(t))) {
				case "passive":
					if heatTrips.passive == 0 || v < heatTrips.passive {
						heatTrips.passive = v
					}
				case "hot", "critical":
					if heatTrips.critical == 0 || v < heatTrips.critical {
						heatTrips.critical = v
					}
				}
			}
		}
	})
	return heatTrips.passive, heatTrips.critical
}

// heatBatteryC is the battery's own temperature: the power_supply "temp" file,
// in tenths of a degree. A gauge without one reports false. Ticker only.
func heatBatteryC(now time.Time) (float64, bool) {
	if heatBatt.dir == "" {
		if !heatBatt.lookAt.IsZero() && now.Sub(heatBatt.lookAt) < heatBattEvery {
			return 0, false
		}
		heatBatt.lookAt = now
		if heatBatt.dir = batterySysfsDir(); heatBatt.dir == "" {
			return 0, false
		}
	}
	v, ok := readBatteryCounter(filepath.Join(heatBatt.dir, "temp"))
	if !ok || v <= -200 || v >= 900 {
		return 0, false
	}
	return float64(v) / 10, true
}

// heatLevelOf is where one reading stands. Pure.
func heatLevelOf(t, soft, hard, critical, hyst float64) heatLevel {
	switch {
	case t >= critical:
		return heatCritical
	case t >= hard:
		return heatHard
	case t >= soft:
		return heatSoft
	case t >= soft-hyst:
		return heatWarm
	}
	return heatCool
}

// ── The dial ─────────────────────────────────────────────────────────────

// heatNextDial is one tick of the dial, pure. dial and cur are indexes into the
// level table: the dial, and the clock in front now; top is the table's last
// index ("no limit"), floor the lowest point a hot or very hot reading takes
// the dial to and last the lowest a critical one does. temp is the deciding
// reading and byBatt whether the battery's set the level. Steps down go from
// the lower of the dial and the clock in front, so a game already running
// under the dial slows at the first step.
func heatNextDial(dial, cur, top, floor, last int, level heatLevel, temp float64, byBatt bool, now time.Time, st *heatAdapt) int {
	if top < 0 {
		return dial
	}
	dial = clampInt(dial, 0, top)
	floor = clampInt(floor, 0, top)
	last = clampInt(last, 0, floor)
	base := min(dial, clampInt(cur, 0, top))
	entering := level > st.prev || byBatt != st.prevBatt
	st.prev, st.prevBatt = level, byBatt
	since := func(t time.Time, d time.Duration) bool { return t.IsZero() || now.Sub(t) >= d-heatTickSlack }
	down := func(to int) int {
		st.stepAt, st.downAt, st.tempAtStep = now, now, temp
		if level >= heatSoft && (st.tooHot == 0 || base < st.tooHot-1) {
			st.tooHot = base + 1 // base proved too hot
		}
		return to
	}
	if level != heatCool {
		st.coolSince = time.Time{}
	}
	switch level {
	case heatCritical:
		switch {
		case base > floor:
			return down(floor)
		case base > last && (entering || since(st.stepAt, autoPerfTick)):
			return down(base - 1)
		}
		return dial
	case heatHard:
		if base > floor && (entering || since(st.stepAt, heatHardEvery)) {
			return down(base - 1)
		}
		return dial
	case heatSoft:
		if base > floor && (entering || (since(st.stepAt, heatSoftWait) && temp > st.tempAtStep-heatSoftCoolingC)) {
			return down(base - 1)
		}
		return dial
	case heatWarm:
		return dial
	}
	// Cool: back up one step at a time. A level that proved too hot is tried
	// again only every heatRetryHot, until the chip has been cool so long that
	// whatever heated it has gone.
	if st.coolSince.IsZero() {
		st.coolSince = now
	}
	if since(st.coolSince, heatForgetAfter) {
		st.tooHot = 0
	}
	if dial >= top || !since(st.coolSince, heatRiseAfter) || !since(st.downAt, heatRiseAfterDown) || !since(st.stepAt, heatRiseEvery) {
		return dial
	}
	if st.tooHot != 0 && dial+1 >= st.tooHot-1 {
		if !since(st.downAt, heatRetryHot) || !since(st.probeAt, heatRetryHot) {
			return dial
		}
		st.probeAt, st.tooHot = now, dial+3 // the level over the one tried stays in doubt
	}
	st.stepAt = now
	return dial + 1
}

// heatGuardInitLocked reads the model, the kernel's trips and the level table,
// once, on the ticker's first pass.
func heatGuardInitLocked() {
	g := &heatGuard
	g.inited = true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_HEAT_GUARD"))) {
	case "0", "off", "false", "no":
		g.off = true
		logWarn("heat", "heat guard off for this run (JUKAHUB_HEAT_GUARD)")
		heatShown.mu.Lock()
		heatShown.off, heatShown.seen = true, true
		heatShown.mu.Unlock()
		return
	}
	g.model = DetectDevice()
	g.kPass, g.kCrit = heatKernelTrips()
	g.mode = heatLimitLoad()
	g.lim = heatLimitsFor(g.model, g.mode, g.kCrit)
	heatGuardRefreshLevelsLocked()
	kernel := "no kernel trip points"
	switch {
	case g.kPass > 0 && g.kCrit > 0:
		kernel = fmt.Sprintf("kernel throttles from %.0f °C, critical %.0f °C", g.kPass, g.kCrit)
	case g.kCrit > 0:
		kernel = fmt.Sprintf("kernel critical %.0f °C", g.kCrit)
	case g.kPass > 0:
		kernel = fmt.Sprintf("kernel throttles from %.0f °C", g.kPass)
	}
	clock := "no CPU speed table"
	if n := len(g.levels); n > 0 {
		clock = "clock " + formatFrequencyKHz(g.levels[0]) + "–" + formatFrequencyKHz(g.levels[n-1])
	}
	logInfo("heat", "guard on %s (%s): chip %.0f/%.0f/%.0f °C, battery %.0f/%.0f/%.0f °C; %s; %s",
		deviceDisplayName(g.model), heatLimitLabel(g.mode), g.lim.Soft, g.lim.Hard, g.lim.Critical,
		g.lim.BattSoft, g.lim.BattHard, g.lim.BattCritical, kernel, clock)
}

// heatGuardRefreshLevelsLocked reads the fastest policy's table again (a
// boost that brings a parked cluster online can change which one that is),
// keeping the dial at the same clock.
func heatGuardRefreshLevelsLocked() {
	g := &heatGuard
	dirs := autoPerfPolicyDirs()
	if len(dirs) == 0 {
		return
	}
	levels := autoPerfLevelTable(dirs[0], 0)
	if len(levels) == 0 {
		return
	}
	capKHz := heatCapKHzLocked()
	g.levels, g.dial = levels, len(levels)-1
	if capKHz > 0 {
		g.dial = autoPerfIndexAtMost(levels, capKHz)
	}
}

// heatCapKHzLocked is the dial as a clock of the fastest policy, 0 when it
// limits nothing.
func heatCapKHzLocked() int64 {
	g := &heatGuard
	if g.off || g.dial < 0 || g.dial >= len(g.levels)-1 {
		return 0
	}
	return g.levels[g.dial]
}

// heatGuardHoldsLocked reports a warm chip or battery: Automatic CPU speed
// steps no higher.
func heatGuardHoldsLocked() bool {
	g := &heatGuard
	return g.inited && !g.off && !g.unknown && g.level >= heatWarm
}

// heatGuardCurIdxLocked is the clock in front as an index into the dial's
// table: the level Automatic CPU speed or the boost holds, else the fastest
// policy's ceiling as it reads now.
func heatGuardCurIdxLocked() int {
	g := &heatGuard
	top := len(g.levels) - 1
	s := &autoPerf.sess
	if !s.active {
		return top
	}
	if !s.manual && !s.foreign && s.idx >= 0 && s.idx < len(s.levels) {
		return autoPerfIndexAtMost(g.levels, s.levels[s.idx])
	}
	if len(s.dirs) > 0 {
		if v := readKHzFile(filepath.Join(s.dirs[0], "scaling_max_freq")); v > 0 {
			return autoPerfIndexAtMost(g.levels, v)
		}
	}
	return top
}

// heatGuardTickLocked reads the chip and the battery and moves the dial one
// tick. autoPerfTickOnce calls it first, every tick.
func heatGuardTickLocked(now time.Time) {
	g := &heatGuard
	if !g.inited {
		heatGuardInitLocked()
	}
	if g.off {
		return
	}
	if mode := heatLimitLoad(); mode != g.mode {
		g.mode = mode
		g.lim = heatLimitsFor(g.model, mode, g.kCrit)
		logInfo("heat", "limits %s: chip %.0f/%.0f/%.0f °C, battery %.0f/%.0f °C", heatLimitLabel(mode),
			g.lim.Soft, g.lim.Hard, g.lim.Critical, g.lim.BattSoft, g.lim.BattHard)
	}
	if len(g.levels) == 0 {
		heatGuardRefreshLevelsLocked()
	}
	g.chipC, g.chipOK = cpuTempValueC()
	g.battC, g.battOK = heatBatteryC(now)
	g.unknown = !g.chipOK && !g.battOK
	level, byBatt, temp := heatCool, false, g.chipC
	if g.chipOK {
		level = heatLevelOf(g.chipC, g.lim.Soft, g.lim.Hard, g.lim.Critical, heatHystC)
	}
	if g.battOK {
		if b := heatLevelOf(g.battC, g.lim.BattSoft, g.lim.BattHard, g.lim.BattCritical, heatBattHystC); b > level {
			level, byBatt, temp = b, true, g.battC
		}
	}
	if g.unknown {
		level = heatWarm // nothing to go by: the dial holds where it is
	}
	before, wasLevel := g.dial, g.level
	g.level, g.byBatt = level, byBatt
	if len(g.levels) == 0 {
		return
	}
	g.dial = heatNextDial(g.dial, heatGuardCurIdxLocked(), len(g.levels)-1,
		autoPerfIndexAtLeast(g.levels, heatFloorKHz), autoPerfIndexAtLeast(g.levels, heatLastKHz),
		level, temp, byBatt, now, &g.adapt)
	if level == heatCritical && wasLevel != heatCritical {
		logWarn("heat", "%s: critical (limit %.0f °C)", heatReadingWord(byBatt, temp), heatLimitOf(g.lim, byBatt, heatCritical))
	}
	if g.dial == before {
		return
	}
	from, to := heatDialWord(g.levels, before), heatDialWord(g.levels, g.dial)
	if g.dial < before {
		logInfo("heat", "%s (%s, limit %.0f °C): CPU limit %s → %s", heatReadingWord(byBatt, temp),
			heatLevelWord(level), heatLimitOf(g.lim, byBatt, level), from, to)
	} else {
		logInfo("heat", "%s, cooled: CPU limit %s → %s", heatReadingWord(byBatt, temp), from, to)
	}
}

// heatGuardAfterLocked runs at the end of the tick, once the session in front
// has been clocked: what the dial holds down, the session's record, the lines
// for the player, and the snapshot the rows read.
func heatGuardAfterLocked(now time.Time) {
	g := &heatGuard
	if !g.inited || g.off {
		return
	}
	s := &autoPerf.sess
	capKHz := heatCapKHzLocked()
	holding := false
	if s.active && capKHz > 0 {
		if !s.manual && !s.foreign && len(s.levels) > 0 {
			// The dial is what keeps the level under the session's top (a
			// level the load chose under the dial is not held).
			holding = s.idx < s.top && s.idx >= autoPerfHeatIdxLocked(s)
		} else {
			holding = heatHoldsCeiling(s.heat)
		}
	}
	if st := &g.sess; st.active {
		if g.chipOK && g.chipC > st.peakC {
			st.peakC = g.chipC
		}
		if g.battOK && g.battC > st.battPeakC {
			st.battPeakC = g.battC
		}
		if holding {
			if !st.last.IsZero() {
				st.held += now.Sub(st.last)
			}
			if st.lowestKHz == 0 || capKHz < st.lowestKHz {
				st.lowestKHz = capKHz
			}
		}
		st.last = now
		temp := g.chipC
		if g.byBatt {
			temp = g.battC
		}
		switch {
		case !st.game:
		case g.level == heatCritical && holding && !st.critical:
			st.critical, st.slowed = true, true
			heatQueueMsg(heatSayWord(true, g.byBatt, temp)+": CPU at "+formatFrequencyKHz(capKHz)+", save and let it cool", true)
		case holding && !st.slowed:
			st.slowed = true
			heatQueueMsg(heatSayWord(false, g.byBatt, temp)+": CPU slowed to "+formatFrequencyKHz(capKHz)+" to cool down", true)
		case capKHz == 0 && st.slowed && !st.back:
			st.back = true
			heatQueueMsg("Cooled down: full CPU speed again", true)
		}
		if !st.game && holding && !g.hubSaid {
			g.hubSaid = true
			heatQueueMsg(heatSayWord(false, g.byBatt, temp)+": JukaHub slowed the CPU to "+formatFrequencyKHz(capKHz)+" to cool down", false)
		}
	}
	if capKHz == 0 {
		g.hubSaid = false
	}
	heatShown.mu.Lock()
	heatShown.seen = true
	heatShown.chipC, heatShown.chipOK = g.chipC, g.chipOK
	heatShown.battC, heatShown.battOK = g.battC, g.battOK
	heatShown.level, heatShown.byBatt, heatShown.unknown = g.level, g.byBatt, g.unknown
	heatShown.capKHz, heatShown.holding, heatShown.lim = capKHz, holding, g.lim
	heatShown.mu.Unlock()
}

// heatHoldsCeiling reports whether the guard holds a ceiling under the value
// it found.
func heatHoldsCeiling(writes []saverSysfsWrite) bool {
	for _, w := range writes {
		if filepath.Base(w.Path) != "scaling_max_freq" {
			continue
		}
		o, err1 := strconv.ParseInt(w.Original, 10, 64)
		n, err2 := strconv.ParseInt(w.Written, 10, 64)
		if err1 == nil && err2 == nil && n < o {
			return true
		}
	}
	return false
}

// ── Sessions JukaHub does not clock ──────────────────────────────────────

// heatHoldLocked keeps a session JukaHub does not clock under the dial: while
// the dial is under a policy's ceiling, the ceiling goes down to it (the floor
// first where it is over the new ceiling: the kernel refuses a ceiling under
// the floor); once the dial is back over the value found, that value goes
// back. A value someone else writes meanwhile is theirs: it becomes the one
// that goes back, and is lowered again only while it is over the dial. Under
// autoPerf.mu.
func heatHoldLocked(s *autoPerfSession, capKHz int64) {
	if !s.active || heatGuard.off || (len(s.heat) == 0 && capKHz <= 0) {
		return
	}
	var refTop int64
	if len(s.dirs) > 1 {
		refTop = advertisedMaxKHz(s.dirs[0])
	}
	for _, dir := range s.dirs {
		maxPath := filepath.Join(dir, "scaling_max_freq")
		minPath := filepath.Join(dir, "scaling_min_freq")
		curMax := saverReadTrimmed(maxPath)
		if curMax == "" {
			continue
		}
		curMin := saverReadTrimmed(minPath)
		wantMax, err := strconv.ParseInt(heatFoundLocked(s, maxPath, curMax), 10, 64)
		if err != nil || wantMax <= 0 {
			continue
		}
		if capKHz > 0 {
			if t := cpufreqScaleKHz(dir, capKHz, refTop); t > 0 && t < wantMax {
				wantMax = t
			}
		}
		wantMin := int64(-1)
		if curMin != "" {
			if v, err := strconv.ParseInt(heatFoundLocked(s, minPath, curMin), 10, 64); err == nil && v >= 0 {
				wantMin = min(v, wantMax)
			}
		}
		if _, err := autoPerfSetPairLocked(&s.heat, maxPath, curMax, wantMax, minPath, curMin, wantMin); err != nil && !s.heatErr {
			s.heatErr = true
			logWarn("heat", "could not hold %s under the heat limit: %v", s.label, err)
		}
	}
	// A value back where it was found is no longer held.
	kept := s.heat[:0]
	for _, w := range s.heat {
		if w.Original != w.Written {
			kept = append(kept, w)
		}
	}
	if len(kept) != len(s.heat) {
		s.heat = kept
		autoPerfSaveMarkerLocked()
	}
}

// heatFoundLocked is the value path goes back to: the one found before the
// guard wrote it, or a newer one someone else wrote since, which is theirs
// from then on.
func heatFoundLocked(s *autoPerfSession, path, cur string) string {
	i := autoPerfRecordIndex(s.heat, path)
	if i < 0 {
		return cur
	}
	if !autoPerfOurs(cur, s.heat[i].Written) {
		s.heat[i].Original, s.heat[i].Written = cur, cur
		return cur
	}
	return s.heat[i].Original
}

// ── A session's record ───────────────────────────────────────────────────

// heatGuardSessionBeginLocked starts the record of the session in front.
func heatGuardSessionBeginLocked(label string, class autoPerfClass) {
	heatGuard.sess = heatSession{active: true, label: label, game: class == autoPerfClassGame}
}

// heatGuardSessionEndLocked closes the record. A game that got warm, or that
// the dial held down, gets its log line; the return is the peak, for the
// boost's Settings line ("" when nothing was read).
func heatGuardSessionEndLocked() string {
	st := heatGuard.sess
	heatGuard.sess = heatSession{}
	if !st.active || st.peakC <= 0 {
		return ""
	}
	peak := fmt.Sprintf("peak %.0f °C", st.peakC)
	if !st.game {
		return peak
	}
	line := st.label + ": " + peak
	if st.battPeakC > 0 {
		line += fmt.Sprintf(" (battery %.0f °C)", st.battPeakC)
	}
	if st.held > 0 {
		line += fmt.Sprintf("; held down by the heat for %s, lowest %s", st.held.Round(time.Second), formatFrequencyKHz(st.lowestKHz))
		logInfo("heat", "%s", line)
	} else if st.peakC >= heatGuard.lim.Soft-heatHystC {
		logInfo("heat", "%s", line)
	}
	if st.held > 0 {
		peak += ", slowed to " + formatFrequencyKHz(st.lowestKHz) + " for a while"
	}
	return peak
}

// ── Lines for the player ─────────────────────────────────────────────────

// heatQueueMsg leaves a line for the main thread: on a RetroArch game's
// message line (game) or as a toast over the hub.
func heatQueueMsg(text string, game bool) {
	heatMsg.mu.Lock()
	heatMsg.text, heatMsg.game, heatMsg.at = text, game, time.Now()
	heatMsg.mu.Unlock()
	logInfo("heat", "line for the player: %s", text)
}

// heatMsgClear drops the waiting line if it is still text.
func heatMsgClear(text string) {
	heatMsg.mu.Lock()
	if heatMsg.text == text {
		heatMsg.text = ""
	}
	heatMsg.mu.Unlock()
}

// heatGuardFrame is the main thread's half, from autoPerfFrame twice a
// second: it publishes the Heat limit stop for the ticker and shows the line
// waiting, if any. A RetroArch game's line waits while the game is paused
// (gameHudHeld: a line sent then pops up later out of date); a game without a
// RetroArch message line never shows it, and the line is dropped after
// heatMsgTTL (the log has it).
func heatGuardFrame(config *Config, now time.Time) {
	heatLimitNow.Store(heatLimitMode(config))
	heatMsg.mu.Lock()
	text, game, at := heatMsg.text, heatMsg.game, heatMsg.at
	heatMsg.mu.Unlock()
	if text == "" {
		return
	}
	if now.Sub(at) > heatMsgTTL {
		heatMsgClear(text)
		return
	}
	if !game {
		heatMsgClear(text)
		ShowToast(text, ToastKindWarning)
		return
	}
	if gameHudHeld() || !retroArchRunningCached() {
		return
	}
	heatMsgClear(text)
	port := raLibraryPort()
	if port <= 0 {
		if cfg, _, ok := retroArchConfig(); ok && networkCmdEnabled(cfg) {
			port = networkCmdPort(cfg)
		}
	}
	if port <= 0 {
		logDebug("heat", "no RetroArch command port for the line")
		return
	}
	go func() {
		if err := sendRetroArchNetCmd(port, "SHOW_MSG "+text); err != nil {
			logDebug("heat", "line not sent: %v", err)
		}
	}()
}

// ── Words ────────────────────────────────────────────────────────────────

// heatReadingWord names the deciding reading for the log: "84 °C" or
// "battery 47 °C".
func heatReadingWord(byBatt bool, temp float64) string {
	if byBatt {
		return fmt.Sprintf("battery %.0f °C", temp)
	}
	return fmt.Sprintf("%.0f °C", temp)
}

// heatSayWord starts a line for the player: "Hot (84 °C)", "Battery hot
// (47 °C)", or with very "Very hot (95 °C)".
func heatSayWord(very, byBatt bool, temp float64) string {
	word := "hot"
	if very {
		word = "very hot"
	}
	if byBatt {
		return fmt.Sprintf("Battery %s (%.0f °C)", word, temp)
	}
	return fmt.Sprintf("%s%s (%.0f °C)", strings.ToUpper(word[:1]), word[1:], temp)
}

func heatLevelWord(l heatLevel) string {
	switch l {
	case heatWarm:
		return "warm"
	case heatSoft:
		return "hot"
	case heatHard:
		return "very hot"
	case heatCritical:
		return "critical"
	}
	return "cool"
}

// heatLimitOf is the limit a level crossed, for the log.
func heatLimitOf(l heatLimits, byBatt bool, level heatLevel) float64 {
	soft, hard, critical := l.Soft, l.Hard, l.Critical
	if byBatt {
		soft, hard, critical = l.BattSoft, l.BattHard, l.BattCritical
	}
	switch level {
	case heatCritical:
		return critical
	case heatHard:
		return hard
	}
	return soft
}

// heatDialWord is a dial position for the log: a clock, or "none" at the top.
func heatDialWord(levels []int64, i int) string {
	if i < 0 || i >= len(levels)-1 {
		return "none"
	}
	return formatFrequencyKHz(levels[i])
}

// ── Settings → Games & emulators → Heat limit ────────────────────────────

func heatLimitOptions(*Config) []setOption {
	d := DetectDevice()
	_, kc := heatKernelTrips()
	note := func(mode, tail string) string {
		l := heatLimitsFor(d, mode, kc)
		return fmt.Sprintf("Slows the CPU from %.0f °C, faster from %.0f °C%s", l.Soft, l.Hard, tail)
	}
	return []setOption{
		{Value: heatLimitCool, Label: "Cooler", Note: note(heatLimitCool, ": cooler to hold, a little slower")},
		{Value: heatLimitStandard, Label: "Standard", Note: note(heatLimitStandard, ", made for this model")},
		{Value: heatLimitWarm, Label: "Warmer", Note: note(heatLimitWarm, ": more speed for longer")},
	}
}

// heatLimitSettingsRow is Settings → Games & emulators → Heat limit, under
// CPU boost in games, on a TrimUI handheld.
func heatLimitSettingsRow() setRow {
	return setRow{
		ID: heatLimitKey, Label: "Heat limit", Kind: setKindChoice,
		Visible: func(*Config) bool { return autoPerfSupported() },
		Detail:  heatLimitSettingDetail,
		Options: heatLimitOptions,
		Current: func(c *Config) int { return ledOptionIndex(heatLimitOptions(c), heatLimitMode(c)) },
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			opts := heatLimitOptions(c)
			i := ledOptionIndex(opts, value)
			if i < 0 {
				return
			}
			setStoreString(c, heatLimitKey, opts[i].Value)
			heatLimitNow.Store(opts[i].Value)
			setToast("Heat limit: " + opts[i].Label)
		},
	}
}

// heatLimitSettingDetail is the row's second line, from the last tick. Cheap:
// drawn every frame.
func heatLimitSettingDetail(*Config) string {
	if !autoPerfSupported() {
		return "Only on the handheld"
	}
	autoPerfShown.mu.Lock()
	reason := autoPerfShown.reason
	autoPerfShown.mu.Unlock()
	heatShown.mu.Lock()
	seen, off, unknown := heatShown.seen, heatShown.off, heatShown.unknown
	chipC, chipOK, battC, battOK := heatShown.chipC, heatShown.chipOK, heatShown.battC, heatShown.battOK
	level, capKHz, holding, soft := heatShown.level, heatShown.capKHz, heatShown.holding, heatShown.lim.Soft
	what := "it cools"
	if heatShown.byBatt {
		what = "the battery cools"
	}
	heatShown.mu.Unlock()
	switch {
	case off:
		return "Off for this run (JUKAHUB_HEAT_GUARD=0)"
	case reason != "":
		return "Not available: " + reason
	case !seen:
		return "Slows the CPU before the handheld gets too hot, in every game"
	case unknown:
		return "No temperature reading on this firmware: only the kernel's own limits apply"
	}
	now := ""
	if chipOK {
		now = fmt.Sprintf("%.0f °C", chipC)
	}
	if battOK && battC >= 40 {
		if now != "" {
			now += " · "
		}
		now += fmt.Sprintf("battery %.0f °C", battC)
	}
	if now != "" {
		now += " · "
	}
	switch {
	case level == heatCritical && capKHz > 0:
		return now + "very hot: CPU at " + formatFrequencyKHz(capKHz) + " until " + what
	case holding:
		return now + "CPU held at " + formatFrequencyKHz(capKHz) + " until " + what
	case capKHz > 0:
		return now + "cooling: up to " + formatFrequencyKHz(capKHz) + " for now"
	case level == heatWarm:
		return now + "warm: no faster for now"
	}
	return fmt.Sprintf("%sfull speed · slows from %.0f °C", now, soft)
}

// heatGuardDriverNote is the Thermal line's tail on Settings → Drivers &
// hardware (thermal.go).
func heatGuardDriverNote() string {
	heatShown.mu.Lock()
	defer heatShown.mu.Unlock()
	switch {
	case heatShown.off:
		return " · JukaHub's heat guard off (JUKAHUB_HEAT_GUARD)"
	case !heatShown.seen || heatShown.unknown:
		return ""
	case heatShown.holding:
		return " · JukaHub holds the CPU at " + formatFrequencyKHz(heatShown.capKHz)
	}
	return fmt.Sprintf(" · JukaHub slows the CPU from %.0f °C", heatShown.lim.Soft)
}

// heatGuardQuickNote is what the in-game menu's system line adds after the
// temperature while the dial holds the game down (gamemenu_quick.go).
func heatGuardQuickNote() string {
	heatShown.mu.Lock()
	defer heatShown.mu.Unlock()
	if heatShown.holding {
		return " (cooling)"
	}
	return ""
}
