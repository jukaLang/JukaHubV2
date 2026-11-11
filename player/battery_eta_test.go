package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// batteryETABase is a wall-clock start with no monotonic reading, so the
// tests control every gap.
var batteryETABase = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// batteryETAFeed ticks the tracker every step for d, the level following
// from + perHour·hours (displayed as its floor, the way fuel gauges step), and
// returns the time after the last tick. rich, when set, is fed every 20 s.
func batteryETAFeed(tr *batteryETATracker, start time.Time, from, perHour float64, d, step time.Duration, cable bool, rich func(level float64) batteryRich) time.Time {
	return batteryETAFeedFunc(tr, start, func(el time.Duration) float64 { return from + perHour*el.Hours() }, d, step, cable, rich)
}

// batteryETAFeedFunc is batteryETAFeed with the level as any function of the
// time elapsed.
func batteryETAFeedFunc(tr *batteryETATracker, start time.Time, level func(time.Duration) float64, d, step time.Duration, cable bool, rich func(level float64) batteryRich) time.Time {
	now := start
	lastRich := time.Time{}
	for el := time.Duration(0); el <= d; el += step {
		now = start.Add(el)
		l := level(el)
		pct := int(math.Floor(l))
		if pct > 100 {
			pct = 100
		}
		tr.note(now, pct, cable)
		if rich != nil && (lastRich.IsZero() || now.Sub(lastRich) >= batteryETARichEvery) {
			tr.noteRich(now, rich(l))
			lastRich = now
		}
	}
	return now
}

func batteryETAWithin(t *testing.T, what string, got time.Duration, wantMin, tolerance float64) {
	t.Helper()
	if math.Abs(got.Minutes()-wantMin) > tolerance*wantMin {
		t.Fatalf("%s: got %.1f min, want %.1f min ±%.0f%%", what, got.Minutes(), wantMin, tolerance*100)
	}
}

func TestBatteryETASteadyDrain(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 80.5, -20, 30*time.Minute, 10*time.Second, false, nil)
	e := tr.current(end)
	if !e.OK || e.Charging || e.Source != batterySourceTrend {
		t.Fatalf("estimate = %+v, want a trend drain", e)
	}
	// 70.5 % left at 20 %/h, less the 1 % floor: 208.5 min.
	batteryETAWithin(t, "steady drain", e.Left, 208.5, 0.04)
	if e.Hedged {
		t.Fatalf("27 minutes of evidence should not be hedged: %+v", e)
	}
	if got := batteryETAPhraseFor(e); !strings.HasPrefix(got, "about 3 h") || !strings.HasSuffix(got, " left") {
		t.Fatalf("phrase = %q", got)
	}
}

func TestBatteryETANeedsEvidence(t *testing.T) {
	var tr batteryETATracker
	// One new level after four minutes: not enough to say anything.
	end := batteryETAFeed(&tr, batteryETABase, 80.5, -20, 4*time.Minute, 10*time.Second, false, nil)
	if e := tr.current(end); e.OK {
		t.Fatalf("estimate after 4 minutes = %+v, want none", e)
	}
	// Two levels, but only three minutes apart: still nothing.
	var tr2 batteryETATracker
	end = batteryETAFeed(&tr2, batteryETABase, 80.5, -40, 4*time.Minute, 10*time.Second, false, nil)
	if e := tr2.current(end); e.OK {
		t.Fatalf("estimate from a 3-minute stretch = %+v, want none", e)
	}
}

func TestBatteryETAKernelEstimate(t *testing.T) {
	cases := []struct {
		name string
		pct  int
		tte  float64
		ok   bool
	}{
		// 7200 at 60 % is two hours in seconds; in minutes it would be 120 h.
		{"seconds", 60, 7200, true},
		// 120 at 50 % is two hours in minutes (the AXP2202's unit); in seconds
		// it would be a 50-point drain in two minutes.
		{"minutes", 50, 120, true},
		{"gauge unknown 65535", 60, 65535, false},
		{"impossible either way", 60, 30, false},
		{"over 30 hours", 60, 31 * 3600, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tr batteryETATracker
			tr.note(batteryETABase, c.pct, false)
			tr.noteRich(batteryETABase, batteryRich{status: "discharging", tteRaw: c.tte})
			e := tr.current(batteryETABase)
			if e.OK != c.ok {
				t.Fatalf("OK = %v, want %v (%+v)", e.OK, c.ok, e)
			}
			if !c.ok {
				return
			}
			if e.Source != batterySourceKernel || e.Left != 120*time.Minute {
				t.Fatalf("estimate = %+v, want the kernel's 2 h", e)
			}
			// Nothing has checked the driver's figure yet: it is a guess.
			if !e.Hedged {
				t.Fatalf("an unconfirmed kernel figure should be hedged: %+v", e)
			}
		})
	}
}

// Sequence E of the review: an AXP2202 reports time_to_empty in minutes. The
// figure must read as 3 h 20, never as "under 5 min", and the unit must stay
// known after the run starts over.
func TestBatteryETAKernelMinutesStayKnown(t *testing.T) {
	var tr batteryETATracker
	now := batteryETABase
	tr.note(now, 60, false)
	for i := 0; i < batteryETAUnitVotes; i++ {
		tr.noteRich(now, batteryRich{status: "discharging", tteRaw: 200})
		now = now.Add(20 * time.Second)
		tr.note(now, 60, false)
	}
	e := tr.current(now)
	if !e.OK || e.Source != batterySourceKernel {
		t.Fatalf("estimate = %+v, want the kernel's figure", e)
	}
	batteryETAWithin(t, "kernel minutes", e.Left, 200, 0.02)
	// Unconfirmed, so hedged: "roughly", in half hours past three hours.
	if got := batteryETAPhraseFor(e); got != "roughly 3 h 30 min left" {
		t.Fatalf("phrase = %q", got)
	}
	if tr.driver.kernelUnit != 60 {
		t.Fatalf("unit = %v, want minutes settled after %d readings", tr.driver.kernelUnit, batteryETAUnitVotes)
	}
	// Cable in and out: a new run, but the driver is the same driver.
	now = now.Add(10 * time.Second)
	tr.note(now, 60, true)
	now = now.Add(10 * time.Second)
	tr.note(now, 60, false)
	if tr.driver.kernelUnit != 60 {
		t.Fatalf("unit after a new run = %v, want it kept", tr.driver.kernelUnit)
	}
	// A reading that is only possible in seconds is now impossible, not seconds.
	tr.noteRich(now, batteryRich{tteRaw: 7200})
	if e := tr.current(now); e.OK && e.Source == batterySourceKernel {
		t.Fatalf("a settled minutes driver read 7200 as %v", e.Left)
	}
}

// Sequence E3: time_to_full in minutes while charging.
func TestBatteryETAKernelTimeToFull(t *testing.T) {
	var tr batteryETATracker
	tr.note(batteryETABase, 40, true)
	tr.noteRich(batteryETABase, batteryRich{status: "charging", ttfRaw: 105})
	e := tr.current(batteryETABase)
	if !e.OK || !e.Charging || e.Left != 105*time.Minute || e.Source != batterySourceKernel {
		t.Fatalf("estimate = %+v, want 105 min to full from the kernel", e)
	}
	if got := batteryETAPhraseFor(e); got != "full in roughly 1 h 45 min" {
		t.Fatalf("phrase = %q", got)
	}
	// The same driver in seconds.
	var sec batteryETATracker
	sec.note(batteryETABase, 40, true)
	sec.noteRich(batteryETABase, batteryRich{status: "charging", ttfRaw: 3000})
	if e := sec.current(batteryETABase); !e.OK || e.Left != 50*time.Minute {
		t.Fatalf("estimate = %+v, want 50 min to full", e)
	}
}

func TestBatteryETAKernelConfirmedByTrend(t *testing.T) {
	var tr batteryETATracker
	// The kernel agrees with a 20 %/h drain: once the trend has measured it,
	// the figure is no longer a guess.
	rich := func(level float64) batteryRich {
		return batteryRich{status: "discharging", tteRaw: (level - 1) / 20 * 3600}
	}
	end := batteryETAFeed(&tr, batteryETABase, 80.5, -20, 20*time.Minute, 10*time.Second, false, rich)
	e := tr.current(end)
	if !e.OK || e.Source != batterySourceKernel || e.Hedged {
		t.Fatalf("estimate = %+v, want a confirmed kernel figure", e)
	}
	if !tr.driver.kernelSure {
		t.Fatal("agreement with the trend should confirm the driver")
	}
	// A kernel that claims six times the trend is dropped at once.
	var bad batteryETATracker
	wild := func(level float64) batteryRich {
		return batteryRich{status: "discharging", tteRaw: 6.5 * (level - 1) / 20 * 3600}
	}
	end = batteryETAFeed(&bad, batteryETABase, 80.5, -20, 20*time.Minute, 10*time.Second, false, wild)
	if e := bad.current(end); !e.OK || e.Source != batterySourceTrend || !bad.driver.kernelBad {
		t.Fatalf("estimate = %+v bad=%v, want the trend and the kernel dropped", e, bad.driver.kernelBad)
	}
}

func TestBatteryETACurrentBased(t *testing.T) {
	var tr batteryETATracker
	rich := func(float64) batteryRich {
		return batteryRich{status: "discharging", currentUA: 1e6, chargeFullUAh: 5e6, chargeNowUAh: 2.5e6}
	}
	// The level holds at 50 %: only the current can answer, after three reads.
	end := batteryETAFeed(&tr, batteryETABase, 50.9, 0, 30*time.Second, 10*time.Second, false, rich)
	if e := tr.current(end); e.OK {
		t.Fatalf("estimate after two reads = %+v, want none yet", e)
	}
	end = batteryETAFeed(&tr, end.Add(10*time.Second), 50.9, 0, 50*time.Second, 10*time.Second, false, rich)
	e := tr.current(end)
	if !e.OK || e.Source != batterySourceCurrent {
		t.Fatalf("estimate = %+v, want current-based", e)
	}
	// 2.5 Ah left, less 1 % of 5 Ah, at 1 A: 2.45 h.
	batteryETAWithin(t, "current", e.Left, 147, 0.02)
	if !e.Hedged {
		t.Fatalf("a minute of current readings should be hedged: %+v", e)
	}
	// The reader stops: two missed reads later the draw is no evidence.
	later := end.Add(time.Minute)
	for at := end.Add(10 * time.Second); !at.After(later); at = at.Add(10 * time.Second) {
		tr.note(at, 50, false)
	}
	if e := tr.current(later); e.OK && e.Source == batterySourceCurrent {
		t.Fatalf("a stale draw still answered %+v", e)
	}
}

func TestBatteryETACurrentUnitsMustMatchLevel(t *testing.T) {
	for _, pct := range []float64{50.9, 9.9} {
		var tr batteryETATracker
		// A driver that writes mAh into a µAh file: 2500 (or 450) "µAh" is not
		// this battery at any level, and "gone in seconds" must not reach the
		// screen.
		now := pct * 50
		rich := func(float64) batteryRich {
			return batteryRich{currentUA: 1e6, chargeNowUAh: now}
		}
		end := batteryETAFeed(&tr, batteryETABase, pct, 0, 2*time.Minute, 10*time.Second, false, rich)
		if e := tr.current(end); e.OK {
			t.Fatalf("at %.0f %%: estimate = %+v, want none from a mismatched counter", pct, e)
		}
	}
}

func TestBatteryETACurrentDisagreeingWithTrendIsDropped(t *testing.T) {
	var tr batteryETATracker
	// The current claims ~2 h while the level falls at 60 %/h (well under an
	// hour left). Three times off is suspect at first and dropped once it has
	// stayed off for batteryETASuspectHold.
	rich := func(level float64) batteryRich {
		return batteryRich{currentUA: 1e6, chargeFullUAh: 5e6, chargeNowUAh: 5e6 * level / 100}
	}
	mid := batteryETAFeed(&tr, batteryETABase, 60.5, -60, 9*time.Minute, 10*time.Second, false, rich)
	if e := tr.current(mid); !e.OK || e.Source != batterySourceCurrent || !e.Hedged || tr.driver.drawBad {
		t.Fatalf("estimate = %+v bad=%v, want a hedged current figure while it is only suspect", e, tr.driver.drawBad)
	}
	end := batteryETAFeed(&tr, mid.Add(10*time.Second), 60.5-9, -60, 12*time.Minute, 10*time.Second, false, rich)
	e := tr.current(end)
	if !e.OK || e.Source != batterySourceTrend || !tr.driver.drawBad {
		t.Fatalf("estimate = %+v bad=%v, want the trend once the current stayed off", e, tr.driver.drawBad)
	}
	// What is known about the driver outlives the run.
	tr.note(end.Add(10*time.Second), 40, true)
	if !tr.driver.drawBad {
		t.Fatal("a new run forgot the bad current")
	}
}

// Sequence L: forty minutes on the menu, then a game. The current answers at
// once and the level lags; the current must not be thrown out for it.
func TestBatteryETACurrentFollowsALoadChange(t *testing.T) {
	var tr batteryETATracker
	const capacity = 5e6 // µAh
	menu, game := 0.25e6, 1.5e6 // µA: 5 %/h and 30 %/h of 5 Ah
	switchAt := 45 * time.Minute
	level := func(el time.Duration) float64 {
		if el <= switchAt {
			return 90.5 - 5*el.Hours()
		}
		return 90.5 - 5*switchAt.Hours() - 30*(el-switchAt).Hours()
	}
	lastLevel := 0.0
	rich := func(l float64) batteryRich {
		lastLevel = l
		draw := menu
		if l < 90.5-5*switchAt.Hours() {
			draw = game
		}
		return batteryRich{status: "discharging", currentUA: draw, chargeFullUAh: capacity, chargeNowUAh: capacity * l / 100}
	}
	for _, at := range []time.Duration{switchAt + 15*time.Minute, switchAt + 20*time.Minute} {
		tr = batteryETATracker{}
		end := batteryETAFeedFunc(&tr, batteryETABase, level, at, 10*time.Second, false, rich)
		e := tr.current(end)
		if !e.OK || e.Source != batterySourceCurrent || tr.driver.drawBad {
			t.Fatalf("%v into the game: estimate = %+v bad=%v, want the current", at-switchAt, e, tr.driver.drawBad)
		}
		truth := (lastLevel - 1) / 30 * 60
		batteryETAWithin(t, "after the load change", e.Left, truth, 0.1)
	}
}

func TestBatteryETANoisyPercent(t *testing.T) {
	var tr batteryETATracker
	now := batteryETABase
	// A gauge sitting on a boundary bounces by one point; only new lows count.
	seq := []int{60, 59, 60, 59, 59, 60, 58, 59, 58, 57, 58, 57, 56}
	for _, pct := range seq {
		for i := 0; i < 6; i++ { // a minute per entry
			tr.note(now, pct, false)
			now = now.Add(10 * time.Second)
		}
	}
	if !tr.started || tr.runAt != batteryETABase {
		t.Fatal("a one-point bounce must not restart the run")
	}
	if len(tr.lows) != 4 {
		t.Fatalf("lows = %v, want 59, 58, 57, 56", tr.lows)
	}
	if e := tr.current(now.Add(-10 * time.Second)); !e.OK {
		t.Fatalf("estimate = %+v, want one from the new lows", e)
	}
}

func TestBatteryETACableChangesStartOver(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 50.5, -20, 20*time.Minute, 10*time.Second, false, nil)
	if !tr.current(end).OK {
		t.Fatal("want a drain estimate before the cable goes in")
	}
	// Cable in: the drain history is gone, and there is no charge estimate yet.
	in := end.Add(10 * time.Second)
	tr.note(in, 43, true)
	if e := tr.current(in); e.OK || !e.Charging || len(tr.lows) != 0 {
		t.Fatalf("after the cable went in: %+v lows=%v, want no estimate yet", e, tr.lows)
	}
	// Charging at 40 %/h from 43.5 %: after 12 minutes a time to full.
	end = batteryETAFeed(&tr, in.Add(10*time.Second), 43.5, 40, 12*time.Minute, 10*time.Second, true, nil)
	e := tr.current(end)
	if !e.OK || !e.Charging || e.Source != batterySourceTrend {
		t.Fatalf("estimate = %+v, want a charging trend", e)
	}
	// Level ≈ 51.5: (80-51.5)/40 h + 20/(40·0.5) h ≈ 0.71 + 1.0 h ≈ 103 min.
	batteryETAWithin(t, "time to full", e.Left, 103, 0.05)
	if got := batteryETAPhraseFor(e); !strings.HasPrefix(got, "full in about 1 h") {
		t.Fatalf("phrase = %q", got)
	}
}

// Sequence C: 40 %/h up to 80 %, then the charger tapers to 20 %/h. Crossing
// 80 % must not halve the figure.
func TestBatteryETAChargeTaper(t *testing.T) {
	reach80 := time.Duration((80 - 60.2) / 40 * float64(time.Hour))
	level := func(el time.Duration) float64 {
		if el <= reach80 {
			return 60.2 + 40*el.Hours()
		}
		return 80 + 20*(el-reach80).Hours()
	}
	cases := []struct {
		at   time.Duration
		want float64 // true minutes to full
	}{
		{reach80 + 30*time.Second, 60 - 0.5},
		{reach80 + 3*time.Minute + 30*time.Second, 57 - 0.5},
		{reach80 + 18*time.Minute + 30*time.Second, 42 - 0.5},
	}
	for _, c := range cases {
		var tr batteryETATracker
		end := batteryETAFeedFunc(&tr, batteryETABase, level, c.at, 10*time.Second, true, nil)
		e := tr.current(end)
		if !e.OK || !e.Charging {
			t.Fatalf("at %.1f %%: estimate = %+v, want a time to full", level(c.at), e)
		}
		batteryETAWithin(t, "taper", e.Left, c.want, 0.1)
	}
}

// Sequence B: one late step must not throw the figure out by half.
func TestBatteryETALateStepDoesNotJump(t *testing.T) {
	var tr batteryETATracker
	pctAt := func(el time.Duration) int {
		switch {
		case el < time.Minute:
			return 70
		case el < 4*time.Minute:
			return 69
		case el < 7*time.Minute:
			return 68
		case el < 11*time.Minute: // due at 10 min, a minute late
			return 67
		}
		return 66
	}
	prev := 0.0
	for el := time.Duration(0); el <= 13*time.Minute; el += 10 * time.Second {
		now := batteryETABase.Add(el)
		tr.note(now, pctAt(el), false)
		e := tr.current(now)
		if !e.OK {
			continue
		}
		raw := tr.raw(now)
		got := raw.Left.Minutes()
		if prev > 0 && math.Abs(got-prev) > 0.03*prev {
			t.Fatalf("at %v the estimate jumped %.1f -> %.1f min", el, prev, got)
		}
		prev = got
	}
	if prev < 180 || prev > 240 {
		t.Fatalf("estimate settled at %.1f min, want about 3 h 40", prev)
	}
}

// A drain under three points an hour still gets an estimate once three marks
// are in, however far apart.
func TestBatteryETASlowDrainQualifies(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 30.9, -2.5, 75*time.Minute, 10*time.Second, false, nil)
	e := tr.current(end)
	if !e.OK || e.Source != batterySourceTrend {
		t.Fatalf("estimate = %+v, want a slow-drain trend", e)
	}
	// ~28 % left at 2.5 %/h: about 11 h.
	batteryETAWithin(t, "slow drain", e.Left, (28.0-1)/2.5*60, 0.1)
}

func TestBatteryETAFullAndHolding(t *testing.T) {
	var tr batteryETATracker
	tr.note(batteryETABase, 100, true)
	if e := tr.current(batteryETABase); e.OK || !e.Full {
		t.Fatalf("full on the cable = %+v", e)
	}
	var hold batteryETATracker
	hold.note(batteryETABase, 80, true)
	hold.noteRich(batteryETABase, batteryRich{status: "not charging"})
	if e := hold.current(batteryETABase); e.OK || !e.Holding {
		t.Fatalf("held at 80 %% = %+v", e)
	}
}

func TestBatteryETAWeakChargerStillDrains(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 40.5, -20, 15*time.Minute, 10*time.Second, true, nil)
	e := tr.current(end)
	if !e.OK || e.Charging || !e.OnCable {
		t.Fatalf("estimate = %+v, want a drain on the cable", e)
	}
	if got := batteryETAPhraseFor(e); !strings.HasPrefix(got, "still draining, about ") {
		t.Fatalf("phrase = %q", got)
	}
	// It is not learned as the handheld's drain: a weak charger is a mix.
	if tr.learned.DischargeTaught != 0 {
		t.Fatalf("learned from a cable-in run: %+v", tr.learned)
	}
}

// Sequence M: a weak charger drains during a game, then catches up once the
// load drops. The level turning round must not keep reading "still draining".
func TestBatteryETAWeakChargerCatchesUp(t *testing.T) {
	var tr batteryETATracker
	level := func(el time.Duration) float64 {
		if el <= 30*time.Minute {
			return 52.5 - 12*el.Hours()
		}
		return 46.5 + 5*(el-30*time.Minute).Hours()
	}
	end := batteryETAFeedFunc(&tr, batteryETABase, level, 30*time.Minute, 10*time.Second, true, nil)
	if e := tr.current(end); !e.OK || e.Charging {
		t.Fatalf("while draining: %+v, want a drain on the cable", e)
	}
	// Once the level is back up a point it no longer reads as draining; two
	// points up (for a minute) start a new run that measures the climb.
	low := tr.low
	for el := 30*time.Minute + 10*time.Second; el <= 65*time.Minute; el += 10 * time.Second {
		now := batteryETABase.Add(el)
		pct := int(math.Floor(level(el)))
		tr.note(now, pct, true)
		if pct <= low {
			continue
		}
		e := tr.current(now)
		if e.OK && !e.Charging {
			t.Fatalf("at %.2f %% (%v) the climb still reads as a drain: %+v", level(el), el, e)
		}
		if got := batteryETALevelLine(e.Pct, true, "Charging", "On cable", e, nil); strings.Contains(got, "draining") {
			t.Fatalf("at %v: %q", el, got)
		}
	}
	if tr.startPct < 48 {
		t.Fatalf("the turn should have started a new run (start %d)", tr.startPct)
	}
}

func TestBatteryETARisingLevelOverrulesDischargingStatus(t *testing.T) {
	var tr batteryETATracker
	rich := func(float64) batteryRich { return batteryRich{status: "discharging"} }
	end := batteryETAFeed(&tr, batteryETABase, 50.5, 30, 15*time.Minute, 10*time.Second, true, rich)
	if e := tr.current(end); !e.OK || !e.Charging {
		t.Fatalf("estimate = %+v, want charging: the level climbs on the cable", e)
	}
}

func TestBatteryETASuspendGap(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 70.5, -20, 20*time.Minute, 10*time.Second, false, nil)
	if !tr.current(end).OK {
		t.Fatal("want an estimate before the gap")
	}
	if tr.learned.DischargeTaught == 0 {
		t.Fatalf("18 minutes and 6 points should have taught a rate: %+v", tr.learned)
	}
	// Thirty minutes of silence: a suspend. Nothing measured before counts.
	after := end.Add(30 * time.Minute)
	if e := tr.current(after); e.OK {
		t.Fatalf("a stale tracker answered %+v", e)
	}
	tr.note(after, 60, false)
	if len(tr.lows) != 0 || tr.runAt != after {
		t.Fatalf("after the gap: run %v lows=%v, want a fresh run", tr.runAt, tr.lows)
	}
	// What the earlier run taught still gives a hedged answer straight away.
	e := tr.current(after)
	if !e.OK || e.Source != batterySourceLearned || !e.Hedged {
		t.Fatalf("estimate = %+v, want the learned rate, hedged", e)
	}
	batteryETAWithin(t, "learned", e.Left, 59.0/tr.learned.DischargePerHour*60, 0.03)
	// Sequence T2: a guess never reaches a toast.
	if got := batteryETALeftFor(e); got != "" {
		t.Fatalf("toast text from a learned guess = %q", got)
	}
	if got := batteryETAPhraseFor(e); !strings.HasPrefix(got, "roughly ") {
		t.Fatalf("hedged phrase = %q", got)
	}
}

// Sequence D: a learned figure the level does not confirm goes away.
func TestBatteryETALearnedFigureExpires(t *testing.T) {
	var tr batteryETATracker
	tr.learned = batteryLearned{ChargePerHour: 40, ChargeTaught: 2}
	rich := func(float64) batteryRich { return batteryRich{status: "charging"} }
	// +1 %/h on a PC port, the status saying "Charging" all along.
	at5 := batteryETAFeed(&tr, batteryETABase, 50.2, 1, 5*time.Minute, 10*time.Second, true, rich)
	if e := tr.current(at5); !e.OK || e.Source != batterySourceLearned {
		t.Fatalf("at 5 min: %+v, want the learned figure for now", e)
	}
	at15 := batteryETAFeed(&tr, at5.Add(10*time.Second), 50.2+1*at5.Sub(batteryETABase).Hours(), 1, 10*time.Minute, 10*time.Second, true, rich)
	if e := tr.current(at15); e.OK {
		t.Fatalf("at 15 min: %+v, want no figure once the level did not confirm it", e)
	}
	// Three hours on, the trend measures 1 %/h: 47 h is past what is believable.
	end := batteryETAFeed(&tr, at15.Add(10*time.Second), 50.2+1*at15.Sub(batteryETABase).Hours(), 1, 165*time.Minute, 10*time.Second, true, rich)
	if e := tr.current(end); e.OK {
		t.Fatalf("after 3 h at 1 %%/h: %+v, want none", e)
	}
}

func TestBatteryETALevelJumpStartsOver(t *testing.T) {
	var tr batteryETATracker
	end := batteryETAFeed(&tr, batteryETABase, 70.5, -20, 15*time.Minute, 10*time.Second, false, nil)
	run := tr.runAt
	low := tr.low
	// Unplugged, up two points for twenty seconds and back: a bounce.
	now := end
	for i := 0; i < 2; i++ {
		now = now.Add(10 * time.Second)
		tr.note(now, low+2, false)
	}
	now = now.Add(10 * time.Second)
	tr.note(now, low, false)
	if tr.runAt != run {
		t.Fatal("a twenty-second bounce restarted the run")
	}
	// Up two points and staying there: the gauge recalibrated.
	for i := 0; i < 8; i++ {
		now = now.Add(10 * time.Second)
		tr.note(now, low+2, false)
	}
	if tr.runAt == run {
		t.Fatal("two points up for over a minute while unplugged should restart the run")
	}
	// A five-point drop in one tick is a recalibration at once.
	run = tr.runAt
	now = now.Add(10 * time.Second)
	tr.note(now, tr.lastPct-5, false)
	if tr.runAt == run {
		t.Fatal("a five-point drop in one tick should restart the run")
	}
}

func TestBatteryETAStaleRichReadIsDropped(t *testing.T) {
	var tr batteryETATracker
	tr.note(batteryETABase, 60, false)
	began := batteryETABase.Add(5 * time.Second)
	// The cable goes in while a read that began unplugged is still running.
	tr.note(batteryETABase.Add(10*time.Second), 60, true)
	tr.noteRich(batteryETABase.Add(12*time.Second), batteryRich{status: "discharging", readAt: began})
	if tr.rich.status != "" {
		t.Fatalf("a read from before the run was applied: %+v", tr.rich)
	}
	tr.noteRich(batteryETABase.Add(20*time.Second), batteryRich{status: "charging", readAt: batteryETABase.Add(15 * time.Second)})
	if tr.rich.status != "charging" {
		t.Fatalf("a read from this run was dropped: %+v", tr.rich)
	}
}

func TestBatteryETALearnsAndPersists(t *testing.T) {
	var tr batteryETATracker
	batteryETAFeed(&tr, batteryETABase, 90.5, -20, 40*time.Minute, 10*time.Second, false, nil)
	if tr.learned.DischargeTaught == 0 || math.Abs(tr.learned.DischargePerHour-20) > 1.5 {
		t.Fatalf("learned = %+v, want about 20 %%/h", tr.learned)
	}
	if !tr.dirty {
		t.Fatal("a new learned rate must be marked for saving")
	}
	path := filepath.Join(t.TempDir(), batteryETAFileName)
	if err := batteryETASave(path, tr.learned); err != nil {
		t.Fatal(err)
	}
	back := batteryETALoad(path)
	if back.DischargeTaught != tr.learned.DischargeTaught || math.Abs(back.DischargePerHour-tr.learned.DischargePerHour) > 1e-9 {
		t.Fatalf("round trip = %+v, want %+v", back, tr.learned)
	}
	// Garbage and out-of-range rates read as nothing learned.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".bak"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if got := batteryETALoad(path); got.DischargeTaught != 0 || got.DischargePerHour != 0 {
		t.Fatalf("damaged file = %+v, want empty", got)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"discharge_pct_per_hour":900,"discharge_taught":3,"charge_pct_per_hour":40,"charge_taught":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := batteryETALoad(path)
	if got.DischargeTaught != 0 || got.ChargeTaught != 2 || got.ChargePerHour != 40 {
		t.Fatalf("sanitized = %+v, want the drain dropped and the charge kept", got)
	}
	if missing := batteryETALoad(filepath.Join(t.TempDir(), "absent.json")); missing.DischargeTaught != 0 {
		t.Fatalf("absent file = %+v", missing)
	}
}

func TestBatteryETALearnedChargeNeedsAClimb(t *testing.T) {
	var tr batteryETATracker
	tr.learned = batteryLearned{ChargePerHour: 40, ChargeTaught: 2}
	tr.note(batteryETABase, 50, true)
	if e := tr.current(batteryETABase); e.OK {
		t.Fatalf("cable just in, nothing climbing: %+v, want no estimate", e)
	}
	tr.noteRich(batteryETABase.Add(10*time.Second), batteryRich{status: "charging"})
	e := tr.current(batteryETABase.Add(10 * time.Second))
	if !e.OK || !e.Charging || e.Source != batterySourceLearned || !e.Hedged {
		t.Fatalf("estimate = %+v, want the learned charge rate, hedged", e)
	}
	// 30 points to 80 % at 40 %/h, then 20 at 20 %/h: 45 + 60 min.
	batteryETAWithin(t, "learned charge", e.Left, 105, 0.02)
}

func TestBatteryETASmoothingAndHysteresis(t *testing.T) {
	var tr batteryETATracker
	now := batteryETABase
	tr.note(now, 60, false)
	// The kernel's figure swings by ±5 % from one read to the next; the figure
	// on screen must move far less than that per read.
	shown := []float64{}
	for i := 0; i < 12; i++ {
		tte := 7200.0
		if i%2 == 1 {
			tte = 7560
		}
		tr.noteRich(now, batteryRich{tteRaw: tte})
		shown = append(shown, tr.shown.minutes)
		now = now.Add(20 * time.Second)
		tr.note(now, 60, false)
	}
	for i := 1; i < len(shown); i++ {
		if math.Abs(shown[i]-shown[i-1]) > 2.5 {
			t.Fatalf("shown figure jumped %.2f -> %.2f min on a 6-minute swing", shown[i-1], shown[i])
		}
	}

	// Hysteresis: once 185 is on screen, 182.4 stays 185 and 181.9 moves on;
	// back up to 182.6 it stays at 180 rather than flipping.
	h := batteryETATracker{started: true, lastAt: batteryETABase, est: batteryEstimate{OK: true, Pct: 60}}
	set := func(m float64) int {
		h.shown = batteryShown{have: true, minutes: m, at: batteryETABase}
		return batteryETAMinutes(h.current(batteryETABase).Left)
	}
	for _, step := range []struct {
		minutes float64
		want    int
	}{{182.6, 185}, {182.4, 185}, {181.9, 180}, {182.6, 180}, {188.2, 190}, {59.6, 60}, {59.3, 59}} {
		if got := set(step.minutes); got != step.want {
			t.Fatalf("%.1f min shows %d, want %d", step.minutes, got, step.want)
		}
	}
}

func TestBatteryETACountsDownBetweenRefreshes(t *testing.T) {
	var tr batteryETATracker
	tr.note(batteryETABase, 60, false)
	tr.noteRich(batteryETABase, batteryRich{tteRaw: 3000})
	tr.lastAt = batteryETABase.Add(2 * time.Minute) // the tick is alive
	if got := tr.current(batteryETABase.Add(2 * time.Minute)).Left; got != 48*time.Minute {
		t.Fatalf("two minutes later = %v, want 48m", got)
	}
}

func TestBatteryETAText(t *testing.T) {
	mins := func(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }
	cases := []struct {
		e     batteryEstimate
		long  string
		short string
		toast string
	}{
		{batteryEstimate{OK: true, Left: mins(187), Pct: 70}, "about 3 h 5 min left", "3h 5m left", "about 3 h 5 min left"},
		{batteryEstimate{OK: true, Left: mins(180), Pct: 70}, "about 3 h left", "3h left", "about 3 h left"},
		{batteryEstimate{OK: true, Left: mins(59.6), Pct: 20}, "about 1 h left", "1h left", "about 1 h left"},
		{batteryEstimate{OK: true, Left: mins(44.4), Pct: 15}, "about 44 min left", "44m left", "about 44 min left"},
		{batteryEstimate{OK: true, Left: mins(4), Pct: 2}, "under 5 min left", "<5m left", "under 5 min left"},
		{batteryEstimate{OK: true, Left: mins(120), Pct: 40, OnCable: true}, "still draining, about 2 h left", "draining, 2h left", "about 2 h left"},
		{batteryEstimate{OK: true, Charging: true, Left: mins(40), Pct: 70}, "full in about 40 min", "full in 40m", ""},
		{batteryEstimate{OK: true, Charging: true, Left: mins(12), Pct: 97}, "almost full", "almost full", ""},
		// Hedged: "roughly", "~", coarser steps, and never in a toast.
		{batteryEstimate{OK: true, Hedged: true, Left: mins(205), Pct: 70}, "roughly 3 h 30 min left", "~3h 30m left", ""},
		{batteryEstimate{OK: true, Hedged: true, Left: mins(100), Pct: 30}, "roughly 1 h 45 min left", "~1h 45m left", ""},
		{batteryEstimate{OK: true, Hedged: true, Left: mins(44), Pct: 10}, "roughly 40 min left", "~40m left", ""},
		{batteryEstimate{OK: true, Hedged: true, Left: mins(8), Pct: 2}, "under 10 min left", "<10m left", ""},
		{batteryEstimate{OK: true, Hedged: true, Charging: true, Left: mins(100), Pct: 50}, "full in roughly 1 h 45 min", "full in ~1h 45m", ""},
		{batteryEstimate{OK: false, Pct: 50}, "", "", ""},
	}
	for _, c := range cases {
		if got := batteryETAPhraseFor(c.e); got != c.long {
			t.Errorf("phrase(%+v) = %q, want %q", c.e, got, c.long)
		}
		if got := batteryETAShortFor(c.e); got != c.short {
			t.Errorf("short(%+v) = %q, want %q", c.e, got, c.short)
		}
		if got := batteryETALeftFor(c.e); got != c.toast {
			t.Errorf("toast(%+v) = %q, want %q", c.e, got, c.toast)
		}
	}
	if got := batteryETACapitalize("about 3 h left"); got != "About 3 h left" {
		t.Errorf("capitalized = %q", got)
	}
}

func TestBatteryETALevelLine(t *testing.T) {
	drain := batteryEstimate{OK: true, Left: 190 * time.Minute, Pct: 72}
	if got := batteryETALevelLine(72, false, "Charging", "On cable", drain, nil); got != "Battery 72% · about 3 h 10 min left" {
		t.Errorf("long = %q", got)
	}
	bytes := func(limit int) func(string) bool { return func(s string) bool { return len(s) <= limit } }
	if got := batteryETALevelLine(72, false, "Charging", "On cable", drain, bytes(28)); got != "Battery 72% · 3h 10m left" {
		t.Errorf("narrow = %q", got)
	}
	if got := batteryETALevelLine(72, false, "Charging", "On cable", drain, bytes(20)); got != "72% · 3h 10m left" {
		t.Errorf("compact = %q", got)
	}
	if got := batteryETALevelLine(72, false, "Charging", "On cable", drain, bytes(14)); got != "Battery 72%" {
		t.Errorf("tiny = %q", got)
	}
	charge := batteryEstimate{OK: true, Charging: true, OnCable: true, Left: 40 * time.Minute, Pct: 72}
	if got := batteryETALevelLine(72, true, "Charging", "On cable", charge, nil); got != "Battery 72% · Charging · full in about 40 min" {
		t.Errorf("charging = %q", got)
	}
	if got := batteryETALevelLine(72, true, "Charging", "On cable", charge, bytes(24)); got != "72% · full in 40m" {
		t.Errorf("charging, compact = %q", got)
	}
	weak := batteryEstimate{OK: true, OnCable: true, Left: 120 * time.Minute, Pct: 40}
	if got := batteryETALevelLine(40, true, "Charging", "On cable", weak, nil); got != "Battery 40% · On cable · still draining, about 2 h left" {
		t.Errorf("weak charger = %q", got)
	}
	// "Battery 40% · On cable · 2h left" is 34 bytes, but the same line at
	// its widest ("Battery 88% · On cable · 8h 88m left") is not: the choice
	// must hold while the minutes count down.
	if got := batteryETALevelLine(40, true, "Charging", "On cable", weak, bytes(34)); got != "40% · On cable · 2h left" {
		t.Errorf("weak charger, narrow = %q", got)
	}
	if got := batteryETALevelLine(55, true, "charging", "on cable", batteryEstimate{Pct: 55}, nil); got != "Battery 55% · charging" {
		t.Errorf("no estimate = %q", got)
	}
	// A live estimate's cable state wins over a caller's reading that is a
	// moment behind: words and figure describe the same moment.
	live := batteryEstimate{OK: true, Left: 190 * time.Minute, Pct: 72, Live: true}
	if got := batteryETALevelLine(72, true, "Charging", "On cable", live, nil); got != "Battery 72% · about 3 h 10 min left" {
		t.Errorf("live, cable just pulled = %q", got)
	}
	if got := batteryETALevelLine(100, true, "Charging", "On cable", batteryEstimate{Pct: 80, Holding: true, OnCable: true, Live: true}, nil); got != "Battery 100% · On cable" {
		t.Errorf("holding = %q", got)
	}
}

// The Quick Settings footer on the Smart Pro has ~264 px for the line at a
// 24 px font; the choice of form must give it the estimate, and must not
// change while the figure counts down through every minute of an hour.
func TestBatteryETALevelLineHoldsItsForm(t *testing.T) {
	// A stand-in for Inter at 24 px: 11 px a letter, 15 px a digit.
	width := func(s string) int {
		w := 0
		for _, r := range s {
			if r >= '0' && r <= '9' {
				w += 15
			} else {
				w += 11
			}
		}
		return w
	}
	for _, room := range []int{264, 286} {
		fits := func(s string) bool { return width(s) <= room }
		var form string
		for m := 3*60 + 55; m >= 3*60; m-- {
			e := batteryEstimate{OK: true, Left: time.Duration(m) * time.Minute, Pct: 72, Live: true}
			line := batteryETALevelLine(72, false, "Charging", "On cable", e, fits)
			if !strings.Contains(line, "left") {
				t.Fatalf("room %d: %q has no estimate", room, line)
			}
			kind := strings.SplitN(line, "%", 2)[0]
			if form == "" {
				form = kind
			} else if kind != form {
				t.Fatalf("room %d: form changed from %q to %q at %d min", room, form, kind, m)
			}
			if width(line) > room {
				t.Fatalf("room %d: %q is %d wide", room, line, width(line))
			}
		}
	}
	if n := utf8.RuneCountInString(batteryETALevelLine(72, false, "Charging", "On cable",
		batteryEstimate{OK: true, Left: 190 * time.Minute, Pct: 72}, func(string) bool { return false })); n == 0 {
		t.Fatal("the last resort must still be the level")
	}
}

func TestBatteryETADetail(t *testing.T) {
	cases := []struct {
		e    batteryEstimate
		want string
	}{
		{batteryEstimate{Full: true}, "Fully charged"},
		{batteryEstimate{Holding: true}, "On the cable, holding its charge"},
		{batteryEstimate{Charging: true}, "Measuring how fast it charges"},
		{batteryEstimate{OK: true, Source: batterySourceTrend, Basis: 25 * time.Minute}, "From the last 25 min of use"},
		{batteryEstimate{OK: true, Charging: true, Source: batterySourceTrend, Basis: 25 * time.Minute}, "From the last 25 min of charging"},
		{batteryEstimate{OK: true, Source: batterySourceKernel, Hedged: true}, "As the battery reports it — still checking it against the level"},
		{batteryEstimate{OK: true, Source: batterySourceKernel}, "As the battery itself reports it"},
	}
	for _, c := range cases {
		if got := batteryETADetailFor(c.e); got != c.want {
			t.Errorf("detail(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestBatteryETAReadRich(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("status", "Discharging\n")
	write("current_now", "-850000\n")
	write("charge_full_design", "5000000\n")
	write("time_to_empty_avg", "0\n")
	write("time_to_empty_now", "5400\n")
	r := batteryETAReadRich(dir)
	if r.status != "discharging" || r.currentUA != 850000 || r.chargeFullUAh != 5e6 || r.tteRaw != 5400 {
		t.Fatalf("rich = %+v", r)
	}
	if empty := batteryETAReadRich(""); empty != (batteryRich{}) {
		t.Fatalf("no directory = %+v", empty)
	}
}

// batteryETAStateSwap gives a test a clean live state and puts the old one
// back afterwards.
func batteryETAStateSwap(t *testing.T) {
	t.Helper()
	savedPath := batteryETAPathOverride
	batteryETAPathOverride = filepath.Join(t.TempDir(), batteryETAFileName)
	s := &batteryETAState
	s.mu.Lock()
	beforeT, beforeLoaded, beforeLoading, beforeTry := s.t, s.loaded, s.loading, s.loadTryAt
	beforeRichAt, beforeSavedAt := s.richAt, s.savedAt
	s.t, s.loaded, s.loading, s.loadTryAt = batteryETATracker{}, false, false, time.Time{}
	s.richAt, s.savedAt = time.Time{}, time.Time{}
	s.mu.Unlock()
	t.Cleanup(func() {
		// Let goroutines started by the test finish before the state goes back.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			busy := s.loading || s.richBusy || s.saving
			s.mu.Unlock()
			if !busy {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.mu.Lock()
		s.t, s.loaded, s.loading, s.loadTryAt = beforeT, beforeLoaded, beforeLoading, beforeTry
		s.richAt, s.savedAt = beforeRichAt, beforeSavedAt
		s.mu.Unlock()
		batteryETAPathOverride = savedPath
	})
}

func TestBatteryETANoteWithoutBattery(t *testing.T) {
	batteryETAStateSwap(t)
	batteryETANote(-1, false)
	if e := batteryETA(); e.OK {
		t.Fatalf("no battery answered %+v", e)
	}
	if got := batteryETAPhrase(); got != "" {
		t.Fatalf("no battery phrase = %q", got)
	}
	batteryETAState.mu.Lock()
	busy, loading := batteryETAState.richBusy, batteryETAState.loading
	batteryETAState.mu.Unlock()
	if busy || loading {
		t.Fatal("no battery must not start a sysfs read or a load")
	}
}

func TestBatteryETALoadsOffTheTickAndSavesOnlyAfter(t *testing.T) {
	batteryETAStateSwap(t)
	want := batteryLearned{Version: 1, DischargePerHour: 12, DischargeTaught: 4}
	if err := batteryETASave(batteryETAPathOverride, want); err != nil {
		t.Fatal(err)
	}
	s := &batteryETAState
	// Something taught before the file was read must not be saved over it.
	s.mu.Lock()
	s.loading = true // a load in flight
	s.t.dirty = true
	s.mu.Unlock()
	batteryETANote(60, false)
	s.mu.Lock()
	saving := s.saving
	s.loading, s.t.dirty = false, false
	s.mu.Unlock()
	if saving {
		t.Fatal("a save started before the learned rates were read")
	}
	// The first reading starts the load; it lands without the tick waiting.
	batteryETANote(60, false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		loaded, got := s.loaded, s.t.learned
		s.mu.Unlock()
		if loaded {
			if got.DischargeTaught != 4 || got.DischargePerHour != 12 {
				t.Fatalf("loaded %+v, want %+v", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the learned rates were never loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
