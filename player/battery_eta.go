package main

// battery_eta.go — how long the battery lasts, and how long it takes to fill.
//
// The percentage alone does not answer "can I finish this level?": 40 % is
// hours on the menu and well under that in a PSP game or a Remote Play stream.
// This file turns the readings JukaHub already takes into a time, and it only
// shows one when there is evidence for it. In order of trust:
//
//  1. the kernel's own time_to_empty / time_to_full (the _avg files first),
//     once the driver's unit is known (most report seconds, the Smart Pro's
//     AXP2202 reports minutes) and while the figure is possible at this level;
//  2. the charge (or energy) left divided by a smoothed current (or power)
//     draw — only while unplugged;
//  3. the speed of the percentage itself: the moments each new low (or high)
//     level was first seen, over the last 45 minutes (at least three marks),
//     once the level has moved at least 2 points over at least 6 minutes;
//  4. a learned rate from earlier runs (%/h, smoothed, kept in the data
//     folder), so a fresh start can give a rough figure before it has measured
//     anything itself — for a while only: if the level does not confirm it,
//     the figure goes away instead of lingering.
//
// The first two are checked against the third on slow averages, so a change of
// activity (the current answers at once, the level lags) is not mistaken for a
// broken driver; a source that stays far off is dropped for as long as JukaHub
// runs, because that is a fact about the driver, not about one run.
//
// A run starts over when the cable goes in or out, after a silence longer than
// three minutes (a suspend, or JukaHub not running), when the level jumps the
// way a recalibrating fuel gauge makes it jump, and when it turns round on the
// cable (a weak charger catching up once the load drops). The figure on screen
// is smoothed toward each new estimate and counts down between them, so a load
// change does not make it flicker.
//
// Two kinds of figure reach the screen: a measured one, "about 3 h 10 min
// left", and a hedged one — earlier use, a driver figure not yet confirmed, a
// few minutes of evidence — written "roughly 3 h 30 min left" ("~3h 30m" when
// short) and rounded coarser. The low-battery and saver toasts only ever carry
// a measured figure.
//
// Sampling rides widgetsTick: the level and the cable state it already reads
// once a second, plus — every 20 seconds and on a goroutine, because a fuel
// gauge read can be an I2C transfer — the optional status, current, power,
// charge, energy and kernel-estimate files. Readers (Quick Settings, Settings →
// Power & battery, the low-battery and saver toasts, the plugin "power" line,
// Home widgets) only take the mutex and do arithmetic: nothing here touches a
// file on the render path.

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// batteryETARichEvery is how often the optional sysfs files are read.
	batteryETARichEvery = 20 * time.Second
	// batteryETAGap is the silence that ends a run: the device slept, or
	// JukaHub was not running, so what drained meanwhile is unknown.
	batteryETAGap = 3 * time.Minute
	// batteryETAWindow is how far back the percentage trend looks, so the
	// estimate follows a change of activity within the hour.
	batteryETAWindow = 45 * time.Minute
	// batteryETAMinMarks is how many marks the window keeps however old they
	// are: a drain or charge under ~3 points an hour still gets two points of
	// movement to measure instead of never qualifying.
	batteryETAMinMarks = 3
	// batteryETAMinSpan and batteryETAMinMove are the least evidence the trend
	// needs before it gives a number.
	batteryETAMinSpan = 6 * time.Minute
	batteryETAMinMove = 2
	// The learned rate is only taught by a trend with this much behind it, and
	// at most once per batteryETALearnEvery of one run.
	batteryETALearnSpan  = 15 * time.Minute
	batteryETALearnMove  = 5
	batteryETALearnEvery = 15 * time.Minute
	// batteryETALearnedPatience is the least time a learned figure stays up
	// without the level confirming it; past max(this, three points at the
	// learned pace) it goes away.
	batteryETALearnedPatience = 10 * time.Minute
	// batteryETASaveEvery rate-limits writes of the learned rate to the card.
	batteryETASaveEvery = 10 * time.Minute
	// batteryETALoadRetry is how long a missing data folder waits before the
	// learned rates are looked for again.
	batteryETALoadRetry = time.Minute
	// batteryETARefresh is how often a tick recomputes without new evidence.
	batteryETARefresh = 10 * time.Second
	// batteryETASmoothing is the time constant the shown figure follows a new
	// estimate with; batteryETADrawSmoothing smooths the current/power draw it
	// is computed from, batteryETASlowSmoothing the draw (and the kernel's
	// figure) that is compared with the level's 45-minute trend.
	batteryETASmoothing     = 90 * time.Second
	batteryETADrawSmoothing = 4 * time.Minute
	batteryETASlowSmoothing = 25 * time.Minute
	// batteryETAStaleDraw is the oldest current reading still used: the
	// reader runs every 20 s, so this is two missed reads.
	batteryETAStaleDraw = 45 * time.Second
	// batteryETASuspectHold is how long a source may stay well off the trend
	// before it is dropped (a source far off is dropped at once).
	batteryETASuspectHold = 10 * time.Minute
	// batteryETARecalHold is how long the level must sit two points the wrong
	// way (a climb unplugged, a turn on the cable) before a run starts over,
	// so a gauge that bounces with the load does not wipe the evidence.
	batteryETARecalHold = time.Minute
	// Estimates past these are not believable for a handheld.
	batteryETAMaxEmpty = 30 * time.Hour
	batteryETAMaxFull  = 24 * time.Hour
	// No handheld drains faster than batteryETAMaxDrainPerHour or charges
	// faster than batteryETAMaxChargePerHour; a figure that says so is not one.
	batteryETAMaxDrainPerHour  = 100.0
	batteryETAMaxChargePerHour = 300.0
	// batteryETAFloorPct is the charge counted as unusable: handhelds switch off
	// around there, not at an exact zero.
	batteryETAFloorPct = 1.0
	// batteryETABandOffset places a mark inside its percentage step. A new low
	// N was first shown at the top of N's band (just under N+1 on a gauge that
	// rounds down, N+0.5 on one that rounds to the nearest), a new high at the
	// bottom; half a point splits the difference, whichever rounding the gauge
	// uses.
	batteryETABandOffset = 0.5
	// Above batteryETATaperPct a charger switches to constant voltage and the
	// level climbs at roughly batteryETATaperSpeed of its earlier pace.
	batteryETATaperPct   = 80.0
	batteryETATaperSpeed = 0.5
	// batteryETAMarksMax bounds the per-run history.
	batteryETAMarksMax = 160
	// batteryETAFileName is the learned rates' file in the data folder.
	batteryETAFileName = "battery_eta.json"
)

// Where an estimate came from; Settings words its detail line after it.
const (
	batterySourceKernel  = "kernel"
	batterySourceCurrent = "current"
	batterySourceTrend   = "trend"
	batterySourceLearned = "learned"
)

// batteryEstimate is the answer readers get. Left is meaningful only with OK.
type batteryEstimate struct {
	OK       bool
	Charging bool // Left is the time to full, not to empty
	Full     bool // on the cable and full: nothing to wait for
	Holding  bool // on the cable, neither charging nor draining
	OnCable  bool // a cable is in (a weak charger can still drain)
	Live     bool // the tracker is current: OnCable is its reading, not a guess
	Left     time.Duration
	Hedged   bool // a guess (earlier use, an unconfirmed driver figure, little evidence)
	Source   string
	Basis    time.Duration // how much history a trend estimate rests on
	Pct      int
}

// batteryRich is what one pass over the optional power-supply files found. A
// zero field means the file was absent or unusable.
type batteryRich struct {
	status        string  // the kernel's status, lower-cased
	currentUA     float64 // |current|, microamps
	powerUW       float64 // |power|, microwatts
	chargeNowUAh  float64
	chargeFullUAh float64
	energyNowUWh  float64
	energyFullUWh float64
	tteRaw        float64 // kernel time to empty, in the driver's unit
	ttfRaw        float64 // kernel time to full, in the driver's unit
	// readAt is when the pass began; a pass that began before the run it is
	// handed to describes the run before it and is dropped. Zero means now.
	readAt time.Time
}

// batteryMark is the moment a new low (or high) level was first seen.
type batteryMark struct {
	at  time.Time
	pct int
}

// batteryLearned is the persisted memory of how fast this handheld drains and
// charges, as percentage points per hour. The Taught counters say how many
// measured stretches went into each rate (at most one per 15 minutes).
type batteryLearned struct {
	Version          int       `json:"version"`
	DischargePerHour float64   `json:"discharge_pct_per_hour,omitempty"`
	DischargeTaught  int       `json:"discharge_taught,omitempty"`
	ChargePerHour    float64   `json:"charge_pct_per_hour,omitempty"`
	ChargeTaught     int       `json:"charge_taught,omitempty"`
	Updated          time.Time `json:"updated"`
}

// batteryDriver is what JukaHub learns about the battery driver itself. It
// outlives a run: a run ends with a cable change or a suspend, the driver's
// habits do not.
type batteryDriver struct {
	// kernelUnit is the seconds per unit of time_to_*: 1 or 60, 0 while
	// undecided; it is decided by batteryETAUnitVotes readings in a row.
	kernelUnit float64
	unitVote   float64
	unitVotes  int
	kernelBad  bool // its time_to_* figures stayed far from the level's pace
	kernelSure bool // the level once agreed with it: no longer hedged
	drawBad    bool // its current/power stayed far from the level's pace
}

// batteryETAUnitVotes is how many readings in a row must agree on the
// kernel's unit before it is settled for good.
const batteryETAUnitVotes = 3

// batteryShown is the smoothed figure and when it was last set; between
// refreshes it counts down by the time that passed.
type batteryShown struct {
	have     bool
	charging bool
	minutes  float64
	at       time.Time
}

// batteryETATracker is the whole estimator. It holds no globals and reads no
// files, so every case is a table row in battery_eta_test.go.
type batteryETATracker struct {
	started bool
	cable   bool
	runAt   time.Time
	lastAt  time.Time
	lastPct int

	startPct int
	low      int
	high     int
	lows     []batteryMark // new lows of this run, oldest first
	highs    []batteryMark // new highs of this run, oldest first
	offSince time.Time     // since when the level sits two points the wrong way

	rich   batteryRich
	richAt time.Time

	drawUA     float64 // smoothed discharge current (the estimate)
	drawUW     float64 // smoothed discharge power (the estimate)
	drawSlowUA float64 // slowly smoothed current (the cross-check)
	drawSlowUW float64 // slowly smoothed power (the cross-check)
	drawN      int
	drawSince  time.Time
	drawAt     time.Time
	drawOff    time.Time // since when the slow draw is well off the trend

	kernelSlowH  float64 // slowly smoothed kernel figure, hours (the cross-check)
	kernelSlowAt time.Time
	kernelOff    time.Time // since when it is well off the trend

	driver     batteryDriver
	refreshAt  time.Time
	learnedAt  time.Time
	learned    batteryLearned
	dirty      bool // learned changed since the last save
	shown      batteryShown
	est        batteryEstimate
	lastSource string

	// The figure readers last got, in display minutes, for the hysteresis
	// that stops it flipping between two rounding steps.
	dispMin      int
	dispSet      bool
	dispCharging bool
}

// reset starts a new run at this reading. What was learned, and what is known
// about the driver, survive.
func (t *batteryETATracker) reset(at time.Time, pct int, cable bool) {
	learned, dirty, driver := t.learned, t.dirty, t.driver
	*t = batteryETATracker{learned: learned, dirty: dirty, driver: driver}
	if pct < 0 {
		return
	}
	t.started = true
	t.cable = cable
	t.runAt, t.lastAt, t.lastPct = at, at, pct
	t.startPct, t.low, t.high = pct, pct, pct
}

// batteryETAGapped reports a silence long enough to end a run. Go's monotonic
// clock stops while a Linux device is suspended, so the wall clock is checked
// as well; a wall clock set back counts as a gap too.
func batteryETAGapped(prev, now time.Time) bool {
	if prev.IsZero() {
		return false
	}
	wall := now.Round(0).Sub(prev.Round(0))
	return now.Sub(prev) > batteryETAGap || wall > batteryETAGap || wall < -batteryETAGap
}

// note takes one reading from the once-a-second tick.
func (t *batteryETATracker) note(at time.Time, pct int, cable bool) {
	if pct < 0 {
		t.reset(at, pct, cable)
		t.est = batteryEstimate{Pct: pct}
		return
	}
	if !t.started || cable != t.cable || batteryETAGapped(t.lastAt, at) || t.jumped(at, pct) {
		t.reset(at, pct, cable)
		t.refresh(at)
		return
	}
	moved := false
	if pct < t.low {
		t.low = pct
		t.lows = batteryETAAppendMark(t.lows, batteryMark{at: at, pct: pct})
		moved = true
	}
	if pct > t.high {
		t.high = pct
		t.highs = batteryETAAppendMark(t.highs, batteryMark{at: at, pct: pct})
		moved = true
	}
	t.lastAt, t.lastPct = at, pct
	if moved || t.refreshAt.IsZero() || at.Sub(t.refreshAt) >= batteryETARefresh {
		t.refresh(at)
	}
}

// jumped reports a level change that ends the run: one no battery makes in a
// tick (a fuel gauge recalibrating, a charge JukaHub could not see), or the
// level sitting two points the wrong way for batteryETARecalHold. Unplugged
// the level does not climb, so two points over the run's low is a
// recalibration. On the cable it can go either way, but one run measures one
// direction: two points back up from its low (a weak charger winning once the
// load drops) or down from its high (the load outgrowing the charger) starts
// a new run, so the two directions' marks never mix.
func (t *batteryETATracker) jumped(at time.Time, pct int) bool {
	if d := pct - t.lastPct; d >= 5 || d <= -5 {
		return true
	}
	wrongWay := pct >= t.low+2
	if t.cable {
		wrongWay = (t.low < t.startPct && pct >= t.low+2) || (t.high > t.startPct && pct <= t.high-2)
	}
	if !wrongWay {
		t.offSince = time.Time{}
		return false
	}
	if t.offSince.IsZero() {
		t.offSince = at
		return false
	}
	return at.Sub(t.offSince) >= batteryETARecalHold
}

func batteryETAAppendMark(marks []batteryMark, m batteryMark) []batteryMark {
	marks = append(marks, m)
	if len(marks) > batteryETAMarksMax {
		marks = append(marks[:0], marks[len(marks)-batteryETAMarksMax:]...)
	}
	return marks
}

// noteRich takes one pass over the optional files.
func (t *batteryETATracker) noteRich(at time.Time, r batteryRich) {
	if !t.started {
		return
	}
	if !r.readAt.IsZero() && r.readAt.Before(t.runAt) {
		return
	}
	t.rich, t.richAt = r, at
	if !t.cable {
		current := r.currentUA
		if current < 5e3 || current > 1e7 { // under 5 mA or over 10 A: not a reading
			current = 0
		}
		power := r.powerUW
		if power < 5e4 || power > 1e8 { // under 50 mW or over 100 W
			power = 0
		}
		if current > 0 || power > 0 {
			if t.drawN == 0 || at.Sub(t.drawAt) > batteryETAGap {
				t.drawUA, t.drawUW, t.drawN, t.drawSince = current, power, 1, at
				t.drawSlowUA, t.drawSlowUW = current, power
			} else {
				dt := at.Sub(t.drawAt).Seconds()
				fast := 1 - math.Exp(-dt/batteryETADrawSmoothing.Seconds())
				slow := 1 - math.Exp(-dt/batteryETASlowSmoothing.Seconds())
				t.drawUA = batteryETABlend(t.drawUA, current, fast)
				t.drawUW = batteryETABlend(t.drawUW, power, fast)
				t.drawSlowUA = batteryETABlend(t.drawSlowUA, current, slow)
				t.drawSlowUW = batteryETABlend(t.drawSlowUW, power, slow)
				t.drawN++
			}
			t.drawAt = at
		}
	}
	t.refresh(at)
}

// batteryETABlend moves old toward v by a; a missing side takes the other.
func batteryETABlend(old, v, a float64) float64 {
	switch {
	case v <= 0:
		return old
	case old <= 0:
		return v
	}
	return old + a*(v-old)
}

// richStatus is the kernel's status while the last pass is recent.
func (t *batteryETATracker) richStatus(now time.Time) string {
	if t.richAt.IsZero() || now.Sub(t.richAt) > 2*batteryETARichEvery+5*time.Second {
		return ""
	}
	return t.rich.status
}

// direction answers which way the level is going. Unplugged it falls. On the
// cable the level's own movement since the cable went in decides first — the
// Smart Pro's AXP2202 does not reliably say "Charging" — and while it has not
// moved, the kernel's status does: "Discharging" is a charger too weak for the
// load, "Not charging" a battery held where it is. A level that fell on the
// cable only counts as draining while it is still at its lowest: back up a
// point it is the charger catching up (and two points start a new run).
func (t *batteryETATracker) direction(now time.Time) (charging, full, holding bool) {
	if !t.cable {
		return false, false, false
	}
	status := t.richStatus(now)
	switch {
	case status == "full" || t.lastPct >= 100:
		return true, true, false
	case t.lastPct > t.startPct:
		return true, false, false
	case t.lastPct < t.startPct && t.lastPct <= t.low:
		return false, false, false
	case t.lastPct < t.startPct:
		return true, false, false
	case status == "discharging":
		return false, false, false
	case status == "not charging":
		return true, false, true
	}
	return true, false, false
}

// batteryMarkWindow is the stretch of a run's marks the trend uses: the newest
// mark and the oldest one still inside batteryETAWindow of it.
type batteryMarkWindow struct {
	oldest, newest batteryMark
	moved          int
	span           time.Duration
}

// rate is the speed the window shows, in points per hour.
func (w batteryMarkWindow) rate() float64 {
	if w.span <= 0 {
		return 0
	}
	return float64(w.moved) / w.span.Hours()
}

// batteryMarksWindow picks that stretch and reports whether it is evidence
// enough: at least batteryETAMinMove points over at least batteryETAMinSpan.
// Marks older than batteryETAWindow drop out, but batteryETAMinMarks stay.
func batteryMarksWindow(marks []batteryMark) (batteryMarkWindow, bool) {
	if len(marks) < 2 {
		return batteryMarkWindow{}, false
	}
	newest := marks[len(marks)-1]
	first := 0
	for first < len(marks)-batteryETAMinMarks && newest.at.Sub(marks[first].at) > batteryETAWindow {
		first++
	}
	w := batteryMarkWindow{oldest: marks[first], newest: newest, span: newest.at.Sub(marks[first].at)}
	w.moved = newest.pct - w.oldest.pct
	if w.moved < 0 {
		w.moved = -w.moved
	}
	return w, w.moved >= batteryETAMinMove && w.span >= batteryETAMinSpan
}

// batteryMarksFrom is the tail of a run's rising marks from level up;
// batteryMarksUpTo the head up to and including level.
func batteryMarksFrom(highs []batteryMark, level int) []batteryMark {
	for i, m := range highs {
		if m.pct >= level {
			return highs[i:]
		}
	}
	return nil
}

func batteryMarksUpTo(highs []batteryMark, level int) []batteryMark {
	for i, m := range highs {
		if m.pct > level {
			return highs[:i]
		}
	}
	return highs
}

// trend is the percentage-speed estimate in hours, and the history it rests
// on. Between two marks the battery is part-way through its current level, so
// the time already spent there is taken off. When the next mark is overdue the
// level is taken to be at the edge of its step and the speed at most what the
// whole stretch would show with that mark arriving now, so a late step lowers
// the speed a little instead of throwing the figure out by half.
//
// A charge above the taper point is measured on the marks above it once there
// are enough; until then the pace from below it is slowed by the taper factor.
func (t *batteryETATracker) trend(now time.Time, charging bool) (hours float64, basis time.Duration, ok bool) {
	marks := t.lows
	if charging {
		marks = t.highs
	}
	w, ok := batteryMarksWindow(marks)
	if !ok || w.rate() <= 0 {
		return 0, 0, false
	}
	newest := w.newest
	rate := w.rate()
	tapered := false
	if charging && float64(newest.pct) > batteryETATaperPct {
		taper := int(batteryETATaperPct)
		if wu, oku := batteryMarksWindow(batteryMarksFrom(marks, taper)); oku && wu.rate() > 0 {
			w, rate = wu, wu.rate()
		} else if wl, okl := batteryMarksWindow(batteryMarksUpTo(marks, taper)); okl && wl.rate() > 0 {
			w, rate, tapered = wl, wl.rate()*batteryETATaperSpeed, true
		}
	}
	since := now.Sub(newest.at)
	if since < 0 {
		since = 0
	}
	step := since.Hours() * rate
	if step > 1 {
		if tapered {
			rate = math.Min(rate, 1/since.Hours())
		} else if whole := now.Sub(w.oldest.at).Hours(); whole > 0 {
			rate = math.Min(rate, float64(w.moved+1)/whole)
		}
		step = 1
	}
	basis = now.Sub(w.oldest.at)
	if charging {
		level := math.Min(float64(newest.pct)-batteryETABandOffset+step, 100)
		belowTaper := !tapered && float64(w.newest.pct) <= batteryETATaperPct
		return batteryHoursToFull(level, rate, belowTaper), basis, true
	}
	level := float64(newest.pct) + batteryETABandOffset - step
	return math.Max(0, level-batteryETAFloorPct) / rate, basis, true
}

// batteryHoursToFull is the time from level to 100 % at rate points per hour.
// A rate measured below the taper point is taken at batteryETATaperSpeed for
// the part above it, since chargers slow down for the last fifth.
func batteryHoursToFull(level, rate float64, measuredBelowTaper bool) float64 {
	if rate <= 0 {
		return -1
	}
	if level >= 100 {
		return 0
	}
	if !measuredBelowTaper {
		return (100 - level) / rate
	}
	if level >= batteryETATaperPct {
		return (100 - level) / (rate * batteryETATaperSpeed)
	}
	return (batteryETATaperPct-level)/rate + (100-batteryETATaperPct)/(rate*batteryETATaperSpeed)
}

// batteryETAPlausible bounds a time at this level, in hours: no handheld
// drains faster than batteryETAMaxDrainPerHour or charges faster than
// batteryETAMaxChargePerHour, and none lasts past batteryETAMaxEmpty (or takes
// longer than batteryETAMaxFull to fill).
func batteryETAPlausible(pct float64, charging bool) (lo, hi float64) {
	if charging {
		return math.Max(0, 100-pct) / batteryETAMaxChargePerHour, batteryETAMaxFull.Hours()
	}
	return math.Max(0, pct-batteryETAFloorPct) / batteryETAMaxDrainPerHour, batteryETAMaxEmpty.Hours()
}

// batteryETAUnitFor reads a kernel figure v as seconds (1) or minutes (60):
// the only reading the level allows, or — where both are possible — the one
// the trend is closer to, if it is within 3x. 0 is no answer.
func batteryETAUnitFor(v, lo, hi, trendH float64, trendOK bool) float64 {
	fits := func(h float64) bool { return h >= lo && h <= hi }
	asSeconds, asMinutes := fits(v/3600), fits(v/60)
	switch {
	case asSeconds && !asMinutes:
		return 1
	case asMinutes && !asSeconds:
		return 60
	case asSeconds && asMinutes && trendOK && trendH > 0:
		offSec := math.Abs(math.Log(v / 3600 / trendH))
		offMin := math.Abs(math.Log(v / 60 / trendH))
		unit, off := 1.0, offSec
		if offMin < offSec {
			unit, off = 60, offMin
		}
		if off <= math.Log(3) {
			return unit
		}
	}
	return 0
}

// noteKernelUnit counts one reading's unit; batteryETAUnitVotes in a row
// settle it.
func (d *batteryDriver) noteKernelUnit(unit float64) {
	if unit != d.unitVote {
		d.unitVote, d.unitVotes = unit, 0
	}
	d.unitVotes++
	if d.unitVotes >= batteryETAUnitVotes {
		d.kernelUnit = unit
	}
}

// kernelHours is the driver's own estimate in hours when it is believable.
// Until the unit is settled each reading is read in the unit the level allows
// for it (and votes for that unit); a figure the settled unit makes
// impossible is skipped, not believed.
func (t *batteryETATracker) kernelHours(now time.Time, charging bool, trendH float64, trendOK bool) (float64, bool) {
	if t.driver.kernelBad || t.richAt.IsZero() || now.Sub(t.richAt) > 2*batteryETARichEvery+5*time.Second {
		return 0, false
	}
	v := t.rich.tteRaw
	if charging {
		v = t.rich.ttfRaw
	}
	// 65535 is the "unknown" some gauges report.
	if v <= 0 || v == 65535 {
		return 0, false
	}
	lo, hi := batteryETAPlausible(float64(t.lastPct), charging)
	unit := t.driver.kernelUnit
	if unit == 0 {
		unit = batteryETAUnitFor(v, lo, hi, trendH, trendOK)
		if unit == 0 {
			return 0, false
		}
		t.driver.noteKernelUnit(unit)
	}
	h := v * unit / 3600
	if h < lo || h > hi {
		return 0, false
	}
	return h, true
}

// drawHours is charge (or energy) left over a draw (ua microamps, uw
// microwatts), unplugged only. Counters that do not match the percentage (a
// driver reporting mAh as µAh, a stale charge_now), a draw older than
// batteryETAStaleDraw and an answer the level makes impossible are not used.
func (t *batteryETATracker) drawHours(now time.Time, ua, uw float64) (float64, bool) {
	if t.cable || t.driver.drawBad || t.drawN < 3 || now.Sub(t.drawAt) > batteryETAStaleDraw {
		return 0, false
	}
	pct := float64(t.lastPct)
	lo, _ := batteryETAPlausible(pct, false)
	plausible := func(hours float64) (float64, bool) {
		if hours <= 0 || hours < lo {
			return 0, false
		}
		return hours, true
	}
	r := t.rich
	if ua > 0 {
		if left, ok := batteryETALeftCounter(r.chargeNowUAh, r.chargeFullUAh, pct, 1e5, 5e7); ok {
			return plausible(left / ua)
		}
	}
	if uw > 0 {
		if left, ok := batteryETALeftCounter(r.energyNowUWh, r.energyFullUWh, pct, 3e5, 2e8); ok {
			return plausible(left / uw)
		}
	}
	return 0, false
}

// batteryETALeftCounter is the usable charge (or energy) left: the "now"
// counter when it agrees with the level, else the level's share of "full",
// less the unusable floor. lo and hi bound a believable full battery; a "now"
// counter below lo's share of the level, with no "full" to check it against,
// is a driver writing mAh into a µAh file.
func batteryETALeftCounter(now, full, pct, lo, hi float64) (float64, bool) {
	if full < lo || full > hi {
		full = 0
	}
	if now > hi {
		now = 0
	}
	if now > 0 && full > 0 && math.Abs(now/full*100-pct) > 25 {
		now = 0
	}
	if now > 0 && full == 0 && now < lo*pct/100 {
		now = 0
	}
	left := now
	if left <= 0 && full > 0 {
		left = full * pct / 100
	}
	if left <= 0 {
		return 0, false
	}
	if full > 0 {
		left -= full * batteryETAFloorPct / 100
	}
	return left, left > 0
}

// batteryETADisagree reports two estimates more than factor apart.
func batteryETADisagree(a, b, factor float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	return a > b*factor || b > a*factor
}

// batteryETAJudge weighs a source against the trend: dropped at once when it
// is more than gross times off, dropped once it has stayed more than limit
// times off for batteryETASuspectHold, and suspect (hedged) while it is off.
// since remembers when it went off; it clears when the source agrees again.
func batteryETAJudge(since *time.Time, now time.Time, value, trendH float64, trendOK bool, limit, gross float64) (drop, suspect bool) {
	if !trendOK || value <= 0 || trendH <= 0 {
		*since = time.Time{}
		return false, false
	}
	ratio := math.Max(value/trendH, trendH/value)
	switch {
	case ratio > gross:
		return true, true
	case ratio > limit:
		if since.IsZero() {
			*since = now
		}
		return now.Sub(*since) >= batteryETASuspectHold, true
	}
	*since = time.Time{}
	return false, false
}

// raw picks the best available estimate, without smoothing.
func (t *batteryETATracker) raw(now time.Time) batteryEstimate {
	e := batteryEstimate{Pct: t.lastPct, OnCable: t.cable}
	if !t.started {
		return e
	}
	charging, full, holding := t.direction(now)
	e.Charging, e.Full, e.Holding = charging, full, holding
	if full || holding {
		return e
	}
	trendH, basis, trendOK := t.trend(now, charging)
	set := func(source string, hours float64, hedged bool) batteryEstimate {
		limit := batteryETAMaxEmpty
		if charging {
			limit = batteryETAMaxFull
		}
		if hours < 0 || math.IsNaN(hours) || math.IsInf(hours, 0) || hours > limit.Hours() {
			return e
		}
		e.OK, e.Source, e.Hedged = true, source, hedged
		e.Left = time.Duration(hours * float64(time.Hour))
		if source == batterySourceTrend {
			e.Basis = basis
		}
		return e
	}
	if h, ok := t.kernelHours(now, charging, trendH, trendOK); ok {
		if t.kernelSlowAt.IsZero() || t.kernelSlowH <= 0 {
			t.kernelSlowH = h
		} else {
			a := 1 - math.Exp(-now.Sub(t.kernelSlowAt).Seconds()/batteryETASlowSmoothing.Seconds())
			t.kernelSlowH += a * (h - t.kernelSlowH)
		}
		t.kernelSlowAt = now
		drop, suspect := batteryETAJudge(&t.kernelOff, now, t.kernelSlowH, trendH, trendOK, 3, 6)
		if drop {
			t.driver.kernelBad = true
		} else {
			if trendOK && !batteryETADisagree(h, trendH, 1.5) {
				t.driver.kernelSure = true
			}
			return set(batterySourceKernel, h, suspect || !t.driver.kernelSure)
		}
	}
	if !charging {
		if h, ok := t.drawHours(now, t.drawUA, t.drawUW); ok {
			slow, _ := t.drawHours(now, t.drawSlowUA, t.drawSlowUW)
			drop, suspect := batteryETAJudge(&t.drawOff, now, slow, trendH, trendOK, 2, 4)
			if drop {
				t.driver.drawBad = true
			} else {
				return set(batterySourceCurrent, h, suspect || now.Sub(t.drawSince) < 5*time.Minute)
			}
		}
	}
	if trendOK {
		return set(batterySourceTrend, trendH, basis < 10*time.Minute)
	}
	// Only earlier use is left, and only until the level should have shown
	// that pace. On the cable it is only trusted once the level has started to
	// climb, or the battery says it is charging and the level has not fallen.
	patient := func(perHour float64) bool {
		wait := time.Duration(3 / perHour * float64(time.Hour))
		if wait < batteryETALearnedPatience {
			wait = batteryETALearnedPatience
		}
		return now.Sub(t.runAt) <= wait
	}
	if charging {
		if t.learned.ChargeTaught > 0 && t.learned.ChargePerHour > 0 &&
			(t.high > t.startPct || (t.richStatus(now) == "charging" && t.low == t.startPct)) &&
			patient(t.learned.ChargePerHour) {
			return set(batterySourceLearned, batteryHoursToFull(float64(t.lastPct), t.learned.ChargePerHour, true), true)
		}
		return e
	}
	if !t.cable && t.learned.DischargeTaught > 0 && t.learned.DischargePerHour > 0 && patient(t.learned.DischargePerHour) {
		return set(batterySourceLearned, math.Max(0, float64(t.lastPct)-batteryETAFloorPct)/t.learned.DischargePerHour, true)
	}
	return e
}

// learn feeds a well-founded trend into the learned rates. A drain is only
// learned unplugged and a charge only below the taper, so a weak charger or a
// slow last fifth does not teach the wrong pace.
func (t *batteryETATracker) learn(now time.Time) {
	if !t.started || (!t.learnedAt.IsZero() && now.Sub(t.learnedAt) < batteryETALearnEvery) {
		return
	}
	charging, full, holding := t.direction(now)
	if full || holding {
		return
	}
	marks := t.lows
	if charging {
		marks = t.highs
	} else if t.cable {
		return
	}
	w, ok := batteryMarksWindow(marks)
	if !ok || w.span < batteryETALearnSpan || w.moved < batteryETALearnMove {
		return
	}
	if charging && w.newest.pct > int(batteryETATaperPct) {
		return
	}
	t.learned.teach(charging, w.rate(), now)
	t.learnedAt = now
	t.dirty = true
}

// teach blends one measured rate into the learned ones.
func (l *batteryLearned) teach(charging bool, rate float64, now time.Time) {
	if charging {
		rate = batteryETAClamp(rate, 2, 300)
		if l.ChargeTaught <= 0 || l.ChargePerHour <= 0 {
			l.ChargePerHour = rate
		} else {
			l.ChargePerHour = 0.75*l.ChargePerHour + 0.25*rate
		}
		if l.ChargeTaught < 1000 {
			l.ChargeTaught++
		}
	} else {
		rate = batteryETAClamp(rate, 0.5, 100)
		if l.DischargeTaught <= 0 || l.DischargePerHour <= 0 {
			l.DischargePerHour = rate
		} else {
			l.DischargePerHour = 0.75*l.DischargePerHour + 0.25*rate
		}
		if l.DischargeTaught < 1000 {
			l.DischargeTaught++
		}
	}
	l.Version = 1
	l.Updated = now.Round(0).UTC()
}

// sanitize drops what a hand-edited or damaged file could put in range of the
// estimator.
func (l *batteryLearned) sanitize() {
	bad := func(v, lo, hi float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi }
	if l.DischargeTaught <= 0 || bad(l.DischargePerHour, 0.5, 100) {
		l.DischargePerHour, l.DischargeTaught = 0, 0
	}
	if l.ChargeTaught <= 0 || bad(l.ChargePerHour, 2, 300) {
		l.ChargePerHour, l.ChargeTaught = 0, 0
	}
	if l.DischargeTaught > 1000 {
		l.DischargeTaught = 1000
	}
	if l.ChargeTaught > 1000 {
		l.ChargeTaught = 1000
	}
}

func batteryETAClamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// refresh recomputes the estimate and moves the shown figure toward it. A
// jump of more than 40 % (a new source, a new activity) is taken at once;
// anything smaller is followed with batteryETASmoothing.
func (t *batteryETATracker) refresh(now time.Time) {
	t.refreshAt = now
	e := t.raw(now)
	t.learn(now)
	if !e.OK {
		t.est = e
		t.shown = batteryShown{}
		t.dispSet = false
		return
	}
	minutes := e.Left.Minutes()
	if t.shown.have && t.shown.charging == e.Charging && e.Source == t.lastSource {
		elapsed := now.Sub(t.shown.at)
		if elapsed < 0 {
			elapsed = 0
		}
		predicted := math.Max(0, t.shown.minutes-elapsed.Minutes())
		if math.Abs(minutes-predicted) <= 0.4*math.Max(predicted, 10) {
			a := 1 - math.Exp(-elapsed.Seconds()/batteryETASmoothing.Seconds())
			minutes = predicted + a*(minutes-predicted)
		}
	}
	t.lastSource = e.Source
	t.shown = batteryShown{have: true, charging: e.Charging, minutes: minutes, at: now}
	e.Left = time.Duration(minutes * float64(time.Minute))
	t.est = e
}

// current is the estimate as of now: the shown figure counted down by the time
// since it was set, in whole display minutes. It moves to a new rounding step
// only once the figure is well inside it, so a value sitting on a boundary
// does not flip between "3 h" and "3 h 5 min" from one refresh to the next. A
// tracker that has not heard from the tick for longer than a gap answers
// "unknown" rather than a stale figure.
func (t *batteryETATracker) current(now time.Time) batteryEstimate {
	e := t.est
	if !t.started || batteryETAGapped(t.lastAt, now) {
		return batteryEstimate{Pct: e.Pct}
	}
	e.Live = true
	if !e.OK || !t.shown.have {
		return e
	}
	elapsed := now.Sub(t.shown.at)
	if elapsed < 0 {
		elapsed = 0
	}
	left := math.Max(0, t.shown.minutes-elapsed.Minutes())
	r := batteryETAMinutes(time.Duration(left * float64(time.Minute)))
	if t.dispSet && t.dispCharging == e.Charging && r != t.dispMin {
		step := 5.0
		if r < 60 || t.dispMin < 60 {
			step = 1
		}
		if math.Abs(left-float64(t.dispMin)) < 0.6*step {
			r = t.dispMin
		}
	}
	t.dispMin, t.dispSet, t.dispCharging = r, true, e.Charging
	e.Left = time.Duration(r) * time.Minute
	return e
}

// ── Text ──────────────────────────────────────────────────────────────────

// batteryETAMinutes rounds for display: 5-minute steps from an hour up, whole
// minutes below.
func batteryETAMinutes(d time.Duration) int {
	m := d.Minutes()
	r := int(math.Round(m))
	if r >= 60 {
		r = int(math.Round(m/5)) * 5
	}
	if r < 0 {
		r = 0
	}
	return r
}

// batteryETAShownMinutes is the figure a phrase writes: the display minutes,
// or for a hedged estimate coarser steps — 10 minutes under an hour, 15 up to
// three hours, 30 beyond — so a guess does not look precise.
func batteryETAShownMinutes(e batteryEstimate) int {
	m := batteryETAMinutes(e.Left)
	if !e.Hedged {
		return m
	}
	step := 10
	switch {
	case m >= 180:
		step = 30
	case m >= 60:
		step = 15
	}
	r := int(math.Round(float64(m)/float64(step))) * step
	if r < step {
		r = step
	}
	return r
}

// batteryETADuration writes minutes as "3 h 10 min" (or "3h 10m" short).
func batteryETADuration(minutes int, short bool) string {
	h, m := minutes/60, minutes%60
	switch {
	case short && h > 0 && m == 0:
		return strconv.Itoa(h) + "h"
	case short && h > 0:
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	case short:
		return strconv.Itoa(m) + "m"
	case h > 0 && m == 0:
		return strconv.Itoa(h) + " h"
	case h > 0:
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(m) + " min"
}

// batteryETAWriter writes the numbers of a read-out. Its worst form writes
// every digit as an 8 and every duration with two-digit minutes (keeping the
// hours' digit count), which is as wide as that read-out gets while the
// figure counts down: a layout chosen on it does not flip with the digits.
type batteryETAWriter struct{ worst bool }

func (x batteryETAWriter) num(n int) string {
	s := strconv.Itoa(n)
	if x.worst {
		return strings.Repeat("8", len(s))
	}
	return s
}

func (x batteryETAWriter) dur(minutes int, short bool) string {
	if !x.worst {
		return batteryETADuration(minutes, short)
	}
	hours := ""
	if h := minutes / 60; h > 0 {
		hours = x.num(h)
	}
	switch {
	case short && hours != "":
		return hours + "h 88m"
	case short:
		return "88m"
	case hours != "":
		return hours + " h 88 min"
	}
	return "88 min"
}

// left is the to-empty phrase: "about 25 min left", "roughly 3 h left".
func (x batteryETAWriter) left(e batteryEstimate) string {
	exact, m := batteryETAMinutes(e.Left), batteryETAShownMinutes(e)
	switch {
	case e.Hedged && exact < 10:
		return "under 10 min left"
	case e.Hedged:
		return "roughly " + x.dur(m, false) + " left"
	case exact < 5:
		return "under 5 min left"
	}
	return "about " + x.dur(m, false) + " left"
}

// phrase is the estimate to append to a level: "about 3 h 10 min left",
// "full in about 40 min", "almost full", "still draining, about 2 h left" for
// a cable that cannot keep up, "roughly …" when hedged.
func (x batteryETAWriter) phrase(e batteryEstimate) string {
	if !e.OK {
		return ""
	}
	if e.Charging {
		m := batteryETAShownMinutes(e)
		switch {
		case e.Pct >= 97 || batteryETAMinutes(e.Left) < 5:
			return "almost full"
		case e.Hedged:
			return "full in roughly " + x.dur(m, false)
		}
		return "full in about " + x.dur(m, false)
	}
	if e.OnCable {
		return "still draining, " + x.left(e)
	}
	return x.left(e)
}

// short is the compact form: "3h 10m left", "full in 40m", "almost full",
// "<5m left", "~3h 30m left" when hedged.
func (x batteryETAWriter) short(e batteryEstimate) string {
	if !e.OK {
		return ""
	}
	exact, m := batteryETAMinutes(e.Left), batteryETAShownMinutes(e)
	mark := ""
	if e.Hedged {
		mark = "~"
	}
	if e.Charging {
		if e.Pct >= 97 || exact < 5 {
			return "almost full"
		}
		return "full in " + mark + x.dur(m, true)
	}
	s := mark + x.dur(m, true) + " left"
	switch {
	case e.Hedged && exact < 10:
		s = "<10m left"
	case !e.Hedged && exact < 5:
		s = "<5m left"
	}
	if e.OnCable {
		s = "draining, " + s
	}
	return s
}

// batteryETALeftFor is the to-empty phrase alone ("about 25 min left") for
// the low-battery and saver toasts, or "" when the estimate is not a measured
// drain: a toast that tells someone how long they have must not guess.
func batteryETALeftFor(e batteryEstimate) string {
	if !e.OK || e.Charging || e.Hedged {
		return ""
	}
	return batteryETAWriter{}.left(e)
}

// batteryETAPhraseFor is the estimate as a phrase to append to a level.
func batteryETAPhraseFor(e batteryEstimate) string { return batteryETAWriter{}.phrase(e) }

// batteryETAShortFor is the compact form for tight spaces.
func batteryETAShortFor(e batteryEstimate) string { return batteryETAWriter{}.short(e) }

// batteryETACapitalize upper-cases the first letter of a phrase.
func batteryETACapitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// batteryETALevelForms lists the read-outs of the level, the cable and the
// estimate from the most to the least detailed, e.g. "Battery 72% · about 3 h
// 10 min left", "Battery 72% · 3h 10m left", "72% · 3h 10m left", "Battery
// 72%", "72%". chargingWord and cableWord are the caller's words for a cable
// that charges and one that does not ("Charging" / "On cable").
func batteryETALevelForms(pct int, cable bool, chargingWord, cableWord string, e batteryEstimate, x batteryETAWriter) []string {
	p := x.num(pct) + "%"
	level := "Battery " + p
	draining := e.OK && !e.Charging
	word := ""
	if cable {
		word = chargingWord
		if draining || e.Holding {
			word = cableWord
		}
	}
	with := func(s string) string {
		if word == "" {
			return s
		}
		return s + " · " + word
	}
	var forms []string
	if e.OK {
		long, short := x.phrase(e), x.short(e)
		switch {
		case e.Charging:
			forms = append(forms, with(level)+" · "+long, with(level)+" · "+short,
				level+" · "+short, p+" · "+short)
		case cable:
			// The cable word already says it is plugged in.
			short = strings.TrimPrefix(short, "draining, ")
			forms = append(forms, with(level)+" · "+long, with(level)+" · "+short,
				with(p)+" · "+short, p+" · "+short)
		default:
			forms = append(forms, level+" · "+long, level+" · "+short, p+" · "+short)
		}
	}
	return append(forms, with(level), with(p), p)
}

// batteryETALevelLine is a one-line read-out of the level, the cable and the
// estimate: "Battery 72% · about 3 h 10 min left", "Battery 72% · Charging ·
// full in about 40 min", "Battery 40% · On cable · still draining, about 2 h
// left". fits, when set, picks the most detailed form whose widest rendering
// (batteryETAWriter's worst form) and actual text both fit, so the choice
// holds while the digits change; the level alone is the last resort. The
// cable state is the estimate's own while the tracker is current, so the
// words and the figure always describe the same moment.
func batteryETALevelLine(pct int, cableIn bool, chargingWord, cableWord string, e batteryEstimate, fits func(string) bool) string {
	cable := cableIn
	if e.Live {
		cable = e.OnCable
	}
	forms := batteryETALevelForms(pct, cable, chargingWord, cableWord, e, batteryETAWriter{})
	if fits == nil {
		return forms[0]
	}
	worst := batteryETALevelForms(pct, cable, chargingWord, cableWord, e, batteryETAWriter{worst: true})
	for i, form := range forms {
		if i < len(worst) && fits(worst[i]) && fits(form) {
			return form
		}
	}
	return forms[len(forms)-1]
}

// ── Live state ────────────────────────────────────────────────────────────

// batteryETAState is the tracker the tick feeds and the screens read.
var batteryETAState struct {
	mu        sync.Mutex
	t         batteryETATracker
	loaded    bool // the learned rates were read (or found absent) from the data folder
	loading   bool
	loadTryAt time.Time
	richBusy  bool
	richAt    time.Time
	dir       string
	dirAt     time.Time
	saving    bool
	savedAt   time.Time
}

// batteryETAPathOverride points the learned rates elsewhere (tests).
var batteryETAPathOverride string

// batteryETAPath is the learned rates' file, "" when there is no data folder.
func batteryETAPath() string {
	if p := strings.TrimSpace(batteryETAPathOverride); p != "" {
		return p
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, batteryETAFileName)
	}
	return ""
}

// batteryETALoad reads the learned rates; a missing or damaged file is a
// handheld that has learned nothing yet.
func batteryETALoad(path string) batteryLearned {
	var l batteryLearned
	if path == "" {
		return l
	}
	if _, err := readJSONWithBackup(path, &l); err != nil {
		return batteryLearned{}
	}
	l.sanitize()
	return l
}

// batteryETASave writes the learned rates atomically.
func batteryETASave(path string, l batteryLearned) error {
	if path == "" {
		return nil
	}
	l.Version = 1
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// batteryETAReadRich reads the optional files of one battery directory. Every
// one is optional; the _avg forms are preferred where a driver has both.
func batteryETAReadRich(dir string) batteryRich {
	var r batteryRich
	if dir == "" {
		return r
	}
	r.status = batteryStatusIn(dir)
	first := func(names ...string) float64 {
		for _, name := range names {
			if v, ok := readBatteryCounter(filepath.Join(dir, name)); ok && v != 0 {
				return math.Abs(float64(v))
			}
		}
		return 0
	}
	r.currentUA = first("current_avg", "current_now")
	r.powerUW = first("power_avg", "power_now")
	r.chargeNowUAh = first("charge_now")
	r.chargeFullUAh = first("charge_full", "charge_full_design")
	r.energyNowUWh = first("energy_now")
	r.energyFullUWh = first("energy_full", "energy_full_design")
	r.tteRaw = first("time_to_empty_avg", "time_to_empty_now")
	r.ttfRaw = first("time_to_full_avg", "time_to_full_now")
	return r
}

// batteryETANote is widgetsTick's once-a-second reading. It only does
// arithmetic under the lock: the learned rates are read once on a goroutine
// (and nothing is saved before they were, so a fresh start cannot overwrite
// what earlier runs taught), the optional files are read on one every
// batteryETARichEvery, and the learned rates are saved on one, rate-limited.
func batteryETANote(pct int, cableIn bool) {
	now := time.Now()
	s := &batteryETAState
	s.mu.Lock()
	wantLoad := pct >= 0 && !s.loaded && !s.loading &&
		(s.loadTryAt.IsZero() || now.Sub(s.loadTryAt) >= batteryETALoadRetry)
	if wantLoad {
		s.loading, s.loadTryAt = true, now
	}
	s.t.note(now, pct, cableIn)
	wantRich := pct >= 0 && !s.richBusy && (s.richAt.IsZero() || now.Sub(s.richAt) >= batteryETARichEvery)
	if wantRich {
		s.richBusy, s.richAt = true, now
	}
	var save *batteryLearned
	if s.loaded && s.t.dirty && !s.saving && (s.savedAt.IsZero() || now.Sub(s.savedAt) >= batteryETASaveEvery) {
		learned := s.t.learned
		save = &learned
		s.t.dirty, s.saving, s.savedAt = false, true, now
	}
	dir, dirStale := s.dir, s.dir == "" || now.Sub(s.dirAt) > 5*time.Minute
	s.mu.Unlock()

	if wantLoad {
		go func() {
			path := batteryETAPath()
			var learned batteryLearned
			if path != "" {
				learned = batteryETALoad(path)
			}
			s.mu.Lock()
			s.loading = false
			if path != "" {
				s.loaded = true
				// A rate taught while the file was being read is newer than
				// the file; it stays, and the next save writes it.
				if !s.t.dirty {
					s.t.learned = learned
				}
			}
			s.mu.Unlock()
		}()
	}
	if wantRich {
		go func() {
			began := time.Now()
			if dirStale {
				dir = batterySysfsDir()
			}
			rich := batteryETAReadRich(dir)
			rich.readAt = began
			at := time.Now()
			s.mu.Lock()
			if dirStale {
				s.dir, s.dirAt = dir, at
			}
			s.richBusy = false
			s.t.noteRich(at, rich)
			s.mu.Unlock()
		}()
	}
	if save != nil {
		go func(l batteryLearned) {
			err := batteryETASave(batteryETAPath(), l)
			s.mu.Lock()
			s.saving = false
			if err != nil {
				s.t.dirty = true
			}
			s.mu.Unlock()
			if err != nil {
				Log().Warn("battery estimate: saving the learned rates failed", "err", err)
			}
		}(*save)
	}
}

// batteryETA is the current estimate. Cheap: no file is read.
func batteryETA() batteryEstimate {
	s := &batteryETAState
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t.current(time.Now())
}

// The accessors below are the estimate for other screens — Quick Settings and
// Settings use the level line and the read-outs further down; the Home
// Battery widget can take batteryETAShort() as its caption.

// batteryETAPhrase is the live estimate as a phrase to append to a level:
// "about 3 h 10 min left", "full in about 40 min", "almost full", "roughly
// 3 h left"; "" when there is no estimate yet.
func batteryETAPhrase() string { return batteryETAPhraseFor(batteryETA()) }

// batteryETALine is the same as a sentence: "About 3 h 10 min left".
func batteryETALine() string { return batteryETACapitalize(batteryETAPhrase()) }

// batteryETAShort is the compact form: "3h 10m left", "full in 40m", "~3h left".
func batteryETAShort() string { return batteryETAShortFor(batteryETA()) }

// batteryETALeft is only a measured time to empty ("about 25 min left"), for
// the low-battery and saver toasts, which must never promise a full charge or
// pass a guess off as a measurement.
func batteryETALeft() string { return batteryETALeftFor(batteryETA()) }

// batteryETASettingsValue is Settings → Power & battery's "Battery" read-out.
func batteryETASettingsValue(*Config) string {
	pct := getBatteryPercent()
	if pct < 0 {
		return ""
	}
	e := batteryETA()
	switch {
	case e.Full:
		return strconv.Itoa(pct) + "% · charged"
	case e.OK:
		return strconv.Itoa(pct) + "% · " + batteryETAPhraseFor(e)
	}
	return strconv.Itoa(pct) + "%"
}

// batteryETASettingsDetail says what the read-out rests on, so a rough figure
// reads as one.
func batteryETASettingsDetail(*Config) string {
	return batteryETADetailFor(batteryETA())
}

// batteryETADetailFor is batteryETASettingsDetail for one estimate.
func batteryETADetailFor(e batteryEstimate) string {
	switch {
	case e.Full:
		return "Fully charged"
	case e.Holding:
		return "On the cable, holding its charge"
	case !e.OK && e.Charging:
		return "Measuring how fast it charges"
	case !e.OK:
		return "Measuring how fast it drains — give it a few minutes"
	}
	switch e.Source {
	case batterySourceKernel:
		if e.Hedged {
			return "As the battery reports it — still checking it against the level"
		}
		return "As the battery itself reports it"
	case batterySourceCurrent:
		return "From the power it is drawing right now"
	case batterySourceTrend:
		what := "use"
		if e.Charging {
			what = "charging"
		}
		return "From the last " + batteryETADuration(batteryETAMinutes(e.Basis), false) + " of " + what
	case batterySourceLearned:
		return "From earlier use — sharpens as it measures"
	}
	return ""
}
