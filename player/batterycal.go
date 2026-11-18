package main

// batterycal.go — battery calibration: one full charge, one full drain, and a
// correction learned from them.
//
// A fuel gauge guesses the charge from a model of the cell. When the model is
// off — an aged pack, a swapped cell, a gauge that never saw the cell full and
// empty — the percentage jumps, or the handheld dies at 15%. ArkOS-style
// firmwares offer a "battery calibration" for that, and what it really does is
// make the gauge see one full charge and one full drain, so it can relearn the
// cell's capacity. This file runs that cycle and also measures, from the drain
// itself, how far the percentage was off, so JukaHub can show a corrected one.
//
// Nothing here writes to the PMIC: no register pokes, no "reset gauge" sysfs
// files. Those differ between AXP drivers and kernels and a wrong write can
// leave a device that will not charge. Everything is read-only sysfs plus two
// files in the data folder.
//
// The cycle has three steps. Step 1 waits on the cable for "Full" (or 100% with
// the charge current near zero for ten minutes). Step 2 starts when the cable
// comes out: a goroutine — not a scene, so it keeps going behind games, where
// the Go process keeps running with its presentation parked — reads the raw
// gauge every minute and appends it to a JSON-lines file with an fsync, so a
// power cut keeps every earlier reading. Step 3 runs on the next start after
// the battery gave out: the reading before that start was low (5% or less, or a
// voltage near the cutoff), or the user says the handheld switched itself off.
//
// The learned mapping is time-based. Over the drain, the true charge left at a
// reading is the share of the run's use still ahead of it: (end − t) / (end −
// start). That assumes a roughly steady load, and the screen says so. Time is
// counted as "use": the monotonic gap between readings, capped at five minutes;
// a longer gap (a suspend, JukaHub not running, a reboot) counts for what the
// level dropped across it at the run's own pace, so a wrong wall clock — TrimUI
// boots before NTP — never stretches or shrinks the run. The readings are
// binned at the 21 gauge values 0, 5, … 100, made monotonic with
// pool-adjacent-violators, and the table is read off that curve linearly at
// each of the 21 points (each bin placed at its own mean reading, so a coarse
// bin does not shift the curve), then clamped.
//
// batteryCalCorrect is the hook the shown percentage goes through. It reads a
// table published through an atomic pointer: no lock, no file, no allocation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// batteryCalEvery is the sampler's cadence: often enough to resolve every
	// percentage step of a several-hour drain, rare enough to cost nothing.
	batteryCalEvery = time.Minute
	// batteryCalMaxGap is the most one gap between readings counts as use when
	// nothing better is known.
	batteryCalMaxGap = 5 * time.Minute
	// batteryCalFullHold is how long 100% with almost no charge current must
	// last before step 1 counts the battery as full without a "Full" status.
	batteryCalFullHold = 10 * time.Minute
	// batteryCalFullCurrentMA is "almost no charge current".
	batteryCalFullCurrentMA = 200
	// batteryCalEmptyPct and batteryCalEmptyUV are a last reading that counts
	// as the battery giving out.
	batteryCalEmptyPct = 5
	batteryCalEmptyUV  = 3550000
	// batteryCalRechargedPts is how far the level must climb on the cable for
	// a drain to count as interrupted (a charger catching up, not noise).
	batteryCalRechargedPts = 3
	// batteryCalTolerance is the error, in points, below which the gauge is
	// called accurate and the correction stays off.
	batteryCalTolerance = 4
	// A run must have at least this much behind it to be learned from.
	batteryCalMinSamples = 20
	batteryCalMinSpan    = 30 * time.Minute
	batteryCalMinDrop    = 30
	// batteryCalRoughPts is the level drop across estimated gaps past which
	// the result is called rough and the correction is not turned on by itself.
	batteryCalRoughPts = 10
	// batteryCalMaxSamples caps the readings file: past it every other reading
	// is dropped (about 50 hours of use at one a minute before the first cut).
	batteryCalMaxSamples = 3000
	// batteryCalGrid is the number of table points: gauge 0, 5, … 100.
	batteryCalGrid = 21

	batteryCalStateFile   = "battery_cal.json"
	batteryCalSamplesFile = "battery_cal_samples.jsonl"
)

// Steps of the cycle. Step 3 is not a stored step: it is the question asked
// while step 2 waits (AskBoot), and the finishing pass itself.
const (
	batteryCalIdle   = 0
	batteryCalCharge = 1
	batteryCalDrain  = 2
)

// batteryCalSample is one reading of the drain. Awake is the use counted since
// the drain began, in seconds; Unix is for display only.
type batteryCalSample struct {
	Unix  int64   `json:"t"`
	Awake float64 `json:"a"`
	Pct   int     `json:"p"`
	UV    int     `json:"v,omitempty"`
	Boot  int     `json:"b"`
	Est   bool    `json:"e,omitempty"`
}

// batteryCalResult is what a finished run learned. Table holds the true charge
// for gauge readings 0, 5, … 100; it is immutable once published.
type batteryCalResult struct {
	Finished int64 `json:"finished,omitempty"`
	Table    []int `json:"table"`
	FromPct  int   `json:"from_pct"`
	EndPct   int   `json:"end_pct"`
	EndUV    int   `json:"end_uv,omitempty"`
	MaxErr   int   `json:"max_err"`
	Awake    int64 `json:"awake_s"`
	Samples  int   `json:"samples"`
	Rough    bool  `json:"rough,omitempty"`
	CapStart int   `json:"cap_start_mah,omitempty"`
	CapEnd   int   `json:"cap_end_mah,omitempty"`
}

// batteryCalState is the persisted cycle. Full means step 1 saw a full battery
// and is waiting for the cable to come out. Boot counts JukaHub starts during a
// drain; AskBoot, when set, is the start after which the user is asked whether
// the handheld switched itself off.
type batteryCalState struct {
	Version  int               `json:"version"`
	Step     int               `json:"step"`
	Full     bool              `json:"full,omitempty"`
	Boot     int               `json:"boot,omitempty"`
	AskBoot  int               `json:"ask_boot,omitempty"`
	Started  int64             `json:"started,omitempty"`
	CapStart int               `json:"cap_start_mah,omitempty"`
	Correct  bool              `json:"correct,omitempty"`
	Result   *batteryCalResult `json:"result,omitempty"`
}

// batteryCalReading is one look at the battery for the screen and the sampler.
// Zero values with their Have flag false mean "not reported".
type batteryCalReading struct {
	Present     bool // a battery directory exists
	Pct         int  // raw gauge, -1 unknown
	Status      string
	Cable       bool
	VoltageUV   int
	CurrentMA   int
	HaveCurrent bool
	TempDeci    int
	HaveTemp    bool
	Health      batteryHealth
}

// ── Pure parts ───────────────────────────────────────────────────────────

// parseBatteryCalSamples reads the JSON-lines file. A torn last line (a power
// cut mid-write) or a damaged one is skipped, as is a reading whose use counter
// goes backwards, so whatever survived is still one ordered run.
func parseBatteryCalSamples(data []byte) []batteryCalSample {
	var out []batteryCalSample
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var s batteryCalSample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		if s.Pct < 0 || s.Pct > 100 || math.IsNaN(s.Awake) || math.IsInf(s.Awake, 0) || s.Awake < 0 {
			continue
		}
		if n := len(out); n > 0 && s.Awake < out[n-1].Awake {
			continue
		}
		out = append(out, s)
	}
	return out
}

// batteryCalCredit is how much use, in seconds, one gap between readings
// counts for. A short monotonic gap is taken as it is (the monotonic clock
// stops in suspend, so a sleep costs nothing). A gap without one — JukaHub
// restarted, or the wall clock says much more passed than the monotonic clock
// while the level fell — counts for the drop at the run's pace (rate, points
// per second of use), never more than the wall clock allows when that looks
// sane. estimated reports that the figure is a guess.
func batteryCalCredit(mono time.Duration, monoOK bool, wall time.Duration, drop int, rate float64) (secs float64, estimated bool) {
	if monoOK && mono >= 0 && mono <= batteryCalMaxGap && (drop < 2 || wall <= mono+batteryCalMaxGap) {
		return mono.Seconds(), false
	}
	if drop > 0 && rate > 0 {
		s := float64(drop) / rate
		if wall > 0 && wall.Seconds() < s {
			s = wall.Seconds()
		}
		return s, true
	}
	if drop <= 0 {
		// Switched off or asleep: nothing was used.
		return 0, true
	}
	if wall > 0 && wall <= batteryCalMaxGap {
		return wall.Seconds(), true
	}
	return batteryCalMaxGap.Seconds(), true
}

// batteryCalRate is the run's pace so far in points per second of use, or 0
// while there is too little to tell (5 points over 10 minutes).
func batteryCalRate(samples []batteryCalSample) float64 {
	if len(samples) < 2 {
		return 0
	}
	first, last := samples[0], samples[len(samples)-1]
	drop, span := first.Pct-last.Pct, last.Awake-first.Awake
	if drop < 5 || span < 600 {
		return 0
	}
	return float64(drop) / span
}

// batteryCalLooksEmpty reports whether a last reading is a battery giving out.
func batteryCalLooksEmpty(s batteryCalSample) bool {
	return s.Pct <= batteryCalEmptyPct || (s.UV > 0 && s.UV <= batteryCalEmptyUV)
}

// batteryCalCompute learns the mapping from one drain. reason is the plain
// answer when the run cannot be used.
func batteryCalCompute(samples []batteryCalSample) (r batteryCalResult, reason string) {
	if len(samples) < batteryCalMinSamples {
		return r, "The run had too few readings to learn from."
	}
	first, last := samples[0], samples[len(samples)-1]
	span := last.Awake - first.Awake
	if span < batteryCalMinSpan.Seconds() {
		return r, "The run was shorter than half an hour of use."
	}
	if first.Pct-last.Pct < batteryCalMinDrop {
		return r, fmt.Sprintf("The level only fell from %d%% to %d%%.", first.Pct, last.Pct)
	}

	// Each reading lands in the bin of the nearest table point; a bin keeps
	// the mean gauge reading of its readings as well as their mean true
	// charge, so the table is read off the data where it actually lies.
	var sumX, sumY, weight [batteryCalGrid]float64
	estDrop := 0
	for i, s := range samples {
		g := (s.Pct + 2) / 5
		sumX[g] += float64(s.Pct)
		sumY[g] += 100 * (last.Awake - s.Awake) / span
		weight[g]++
		if s.Est && i > 0 && samples[i-1].Pct > s.Pct {
			estDrop += samples[i-1].Pct - s.Pct
		}
	}

	// Pool adjacent violators: the true charge may not fall while the gauge
	// reading rises. Pooling and binning are all the smoothing there is.
	type block struct {
		lo, hi int
		val, w float64
	}
	var blocks []block
	for g := 0; g < batteryCalGrid; g++ {
		if weight[g] == 0 {
			continue
		}
		blocks = append(blocks, block{lo: g, hi: g, val: sumY[g] / weight[g], w: weight[g]})
		for n := len(blocks); n >= 2 && blocks[n-2].val > blocks[n-1].val; n = len(blocks) {
			a, b := blocks[n-2], blocks[n-1]
			w := a.w + b.w
			blocks[n-2] = block{lo: a.lo, hi: b.hi, val: (a.val*a.w + b.val*b.w) / w, w: w}
			blocks = blocks[:n-1]
		}
	}
	// The curve's points: the last reading is empty by definition, every bin
	// sits at its mean reading, and a full gauge reads full.
	var xs, ys []float64
	for _, b := range blocks {
		for g := b.lo; g <= b.hi; g++ {
			if weight[g] > 0 {
				xs = append(xs, sumX[g]/weight[g])
				ys = append(ys, b.val)
			}
		}
	}
	if float64(last.Pct) < xs[0] {
		xs = append([]float64{float64(last.Pct)}, xs...)
		ys = append([]float64{0}, ys...)
	}
	if xs[len(xs)-1] < 100 {
		xs = append(xs, 100)
		ys = append(ys, 100)
	}
	table := make([]int, batteryCalGrid)
	floor := 0
	for g := range table {
		// Below the lowest reading the handheld was alive at, it is empty.
		x, y := float64(5*g), 0.0
		switch {
		case x == xs[0]:
			y = ys[0]
		case x > xs[0]:
			k := 1
			for k < len(xs)-1 && xs[k] < x {
				k++
			}
			y = ys[k]
			if xs[k] > xs[k-1] {
				y = ys[k-1] + (ys[k]-ys[k-1])*(x-xs[k-1])/(xs[k]-xs[k-1])
			}
		}
		v := max(floor, min(100, int(math.Round(y))))
		table[g] = v
		floor = v
	}
	table[batteryCalGrid-1] = 100

	maxErr := last.Pct
	for g := range table {
		if weight[g] == 0 {
			continue
		}
		if d := table[g] - 5*g; d > maxErr || -d > maxErr {
			maxErr = max(d, -d)
		}
	}
	r = batteryCalResult{
		Table:   table,
		FromPct: first.Pct,
		EndPct:  last.Pct,
		EndUV:   last.UV,
		MaxErr:  maxErr,
		Awake:   int64(span),
		Samples: len(samples),
		Rough:   estDrop > batteryCalRoughPts,
	}
	return r, ""
}

// batteryCalApply maps a raw gauge reading through a table, linearly between
// its points.
func batteryCalApply(table []int, pct int) int {
	if pct < 0 || len(table) != batteryCalGrid {
		return pct
	}
	if pct >= 100 {
		return table[batteryCalGrid-1]
	}
	i, f := pct/5, pct%5
	return (table[i]*(5-f) + table[i+1]*f + 2) / 5
}

// batteryCalTableValid reports whether a stored table can be applied: 21
// points within 0..100 that never fall, so a damaged state file cannot push the
// shown % out of range.
func batteryCalTableValid(table []int) bool {
	if len(table) != batteryCalGrid {
		return false
	}
	for i, v := range table {
		if v < 0 || v > 100 || (i > 0 && v < table[i-1]) {
			return false
		}
	}
	return true
}

// batteryCalFindings is the result in plain words, most important first.
func batteryCalFindings(r *batteryCalResult) []string {
	if r == nil {
		return nil
	}
	var out []string
	if r.MaxErr <= batteryCalTolerance {
		out = append(out, fmt.Sprintf("The gauge is accurate within %d%% — no correction needed.", max(r.MaxErr, 1)))
	} else {
		if r.EndPct > batteryCalEmptyPct {
			out = append(out, fmt.Sprintf("Your battery read %d%% when it actually shut down.", r.EndPct))
		}
		out = append(out, fmt.Sprintf("The %% was off by up to %d points during the run.", r.MaxErr))
	}
	out = append(out, fmt.Sprintf("Measured over %s of use, from %d%% down to %d%%.",
		batteryETADuration(int(r.Awake/60), false), r.FromPct, r.EndPct))
	if r.CapStart > 0 && r.CapEnd > 0 && r.CapStart != r.CapEnd {
		out = append(out, fmt.Sprintf("The battery's own capacity figure went from %d to %d mAh.", r.CapStart, r.CapEnd))
	}
	if r.Rough {
		out = append(out, "Part of the run happened while JukaHub was not running, so the figures are rough.")
	}
	return out
}

// ── Reading the battery ──────────────────────────────────────────────────

// batteryCalRawPercent is the gauge's own figure, never the corrected one:
// the drain must measure the gauge, not JukaHub's correction of it. The
// capacity file is read directly; the charge-counter fallback goes through
// batteryPercentIn, which stays raw as long as the correction hook sits in
// getBatteryPercent (see notes).
func batteryCalRawPercent(dir string) int {
	if dir == "" {
		return -1
	}
	if pct, ok := readBatteryCounter(filepath.Join(dir, "capacity")); ok {
		return max(0, min(100, pct))
	}
	return batteryPercentIn(dir)
}

// batteryCalRead takes one look at the battery directory. Units differ between
// drivers: voltage under 100000 is taken as millivolts, current under 20000 in
// magnitude as milliamps (no handheld draws 20 A, and none runs on 20 mA).
func batteryCalRead(dir string) batteryCalReading {
	r := batteryCalReading{Pct: -1}
	if dir == "" {
		return r
	}
	r.Present = true
	r.Pct = batteryCalRawPercent(dir)
	r.Status = batteryStatusIn(dir)
	r.Cable = getBatteryCharging()
	if v, ok := readBatteryCounter(filepath.Join(dir, "voltage_now")); ok && v > 0 {
		if v < 100000 {
			v *= 1000
		}
		r.VoltageUV = v
	}
	if v, ok := readBatteryCounter(filepath.Join(dir, "current_now")); ok {
		if v >= 20000 || v <= -20000 {
			v /= 1000
		}
		r.CurrentMA, r.HaveCurrent = v, true
	}
	if v, ok := readBatteryCounter(filepath.Join(dir, "temp")); ok && v > -400 && v < 1000 {
		r.TempDeci, r.HaveTemp = v, true
	}
	r.Health = parseBatteryHealth(readBatteryHealthFile(dir))
	return r
}

// ── Live state ───────────────────────────────────────────────────────────

// batteryCal is the cycle in memory. mu guards it; batteryCalIO serialises the
// file writes and is always taken before mu, never while holding it.
var batteryCal struct {
	mu        sync.Mutex
	loaded    bool
	st        batteryCalState
	samples   []batteryCalSample
	gen       int       // bumped to retire a running sampler
	sampling  bool      // a sampler goroutine is running for gen
	lastMono  time.Time // when this process took the last reading
	paused    bool      // step 2 with the cable in
	fullSince time.Time // step 1: since when 100% with almost no current
	live      batteryCalReading
	liveAt    time.Time
	liveBusy  bool
}

var (
	batteryCalIO       sync.Mutex
	batteryCalLoadOnce sync.Once
	batteryCalBootOnce sync.Once
	batteryCalLoadKick atomic.Bool
	batteryCalLoaded   atomic.Bool
	// batteryCalTable is the active correction; nil means none.
	batteryCalTable atomic.Pointer[[]int]
)

// batteryCalPathOverride points the files elsewhere (tests).
var batteryCalPathOverride string

func batteryCalPath(name string) string {
	if dir := strings.TrimSpace(batteryCalPathOverride); dir != "" {
		return filepath.Join(dir, name)
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, name)
	}
	return ""
}

// batteryCalCorrect is the shown percentage for a raw gauge reading: the
// learned correction while one is switched on, else pct unchanged. It is
// called once a second from getBatteryPercent and costs one atomic load; the
// first call starts the state loading on a goroutine and answers raw until it
// is in.
func batteryCalCorrect(pct int) int {
	if pct < 0 {
		return pct
	}
	if !batteryCalLoaded.Load() {
		if batteryCalLoadKick.CompareAndSwap(false, true) {
			go batteryCalLoad()
		}
		return pct
	}
	if t := batteryCalTable.Load(); t != nil {
		return batteryCalApply(*t, pct)
	}
	return pct
}

// batteryCalLoad reads the state and the drain's readings once.
func batteryCalLoad() {
	batteryCalLoadOnce.Do(func() {
		var st batteryCalState
		var samples []batteryCalSample
		if path := batteryCalPath(batteryCalStateFile); path != "" {
			if _, err := readJSONWithBackup(path, &st); err != nil && !errors.Is(err, os.ErrNotExist) {
				Log().Warn("battery calibration: state unreadable, starting over", "err", err)
				st = batteryCalState{}
			}
		}
		if st.Step == batteryCalDrain {
			if path := batteryCalPath(batteryCalSamplesFile); path != "" {
				if data, err := os.ReadFile(path); err == nil {
					samples = parseBatteryCalSamples(data)
				}
			}
		}
		if st.Step < batteryCalIdle || st.Step > batteryCalDrain {
			st.Step = batteryCalIdle
		}
		if st.Result != nil && !batteryCalTableValid(st.Result.Table) {
			st.Result, st.Correct = nil, false
		}
		b := &batteryCal
		b.mu.Lock()
		b.st, b.samples, b.loaded = st, samples, true
		batteryCalPublishLocked()
		b.mu.Unlock()
		batteryCalLoaded.Store(true)
	})
}

// batteryCalEnsureLoaded starts the load without waiting for it.
func batteryCalEnsureLoaded() {
	if !batteryCalLoaded.Load() && batteryCalLoadKick.CompareAndSwap(false, true) {
		go batteryCalLoad()
	}
}

// batteryCalPublishLocked makes the correction match the state. Caller holds mu.
func batteryCalPublishLocked() {
	st := &batteryCal.st
	if st.Correct && st.Result != nil && len(st.Result.Table) == batteryCalGrid {
		t := st.Result.Table
		batteryCalTable.Store(&t)
		return
	}
	batteryCalTable.Store(nil)
}

// batteryCalStartup is called once at start on every OS. On a goroutine it
// loads the state, finishes a drain that ended in the battery giving out (or
// asks about one that ended otherwise), and resumes the sampler.
func batteryCalStartup() {
	go batteryCalBootOnce.Do(batteryCalBoot)
}

func batteryCalBoot() {
	batteryCalLoadKick.Store(true)
	batteryCalLoad()
	health := batteryHealthReport()
	batteryCalIO.Lock()
	defer batteryCalIO.Unlock()
	b := &batteryCal
	b.mu.Lock()
	toast, kind := "", ToastKindInfo
	changed := false
	if b.st.Step == batteryCalDrain {
		// The readings carry their start's number: if saving the state failed
		// at an earlier start, they are ahead of it and this start must be past
		// them.
		if n := len(b.samples); n > 0 {
			b.st.Boot = max(b.st.Boot, b.samples[n-1].Boot)
		}
		b.st.Boot++
		changed = true
		if n := len(b.samples); n > 0 && b.samples[n-1].Boot < b.st.Boot {
			last := b.samples[n-1]
			if batteryCalLooksEmpty(last) {
				toast, kind = batteryCalFinishLocked(b.samples, health.FullCapacity)
			} else {
				b.st.AskBoot = b.st.Boot
				toast = "Battery calibration: did the handheld switch itself off? Answer in Tools → Battery calibration."
			}
		}
	}
	if changed {
		batteryCalWriteStateLocked()
	}
	start := b.st.Step == batteryCalCharge || b.st.Step == batteryCalDrain
	b.mu.Unlock()
	if start {
		batteryCalStartSampler()
	}
	if toast != "" {
		ShowToast(toast, kind)
	}
}

// batteryCalWriteStateLocked writes the state atomically. The caller holds
// batteryCalIO and mu (the state is small; the write is one rename).
func batteryCalWriteStateLocked() {
	path := batteryCalPath(batteryCalStateFile)
	if path == "" {
		return
	}
	st := batteryCal.st
	st.Version = 1
	data, err := json.MarshalIndent(st, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		Log().Warn("battery calibration: saving the state failed", "err", err)
	}
}

// batteryCalPersist saves the current state off the caller's thread.
func batteryCalPersist() {
	go func() {
		batteryCalIO.Lock()
		defer batteryCalIO.Unlock()
		batteryCal.mu.Lock()
		batteryCalWriteStateLocked()
		batteryCal.mu.Unlock()
	}()
}

// batteryCalFinishLocked learns from samples and ends the cycle. It returns
// the toast to show. Caller holds batteryCalIO and mu.
func batteryCalFinishLocked(samples []batteryCalSample, capEnd int) (string, ToastKind) {
	st := &batteryCal.st
	res, reason := batteryCalCompute(samples)
	st.Step, st.Full, st.Boot, st.AskBoot = batteryCalIdle, false, 0, 0
	batteryCalStopLocked()
	if reason != "" {
		batteryCalPublishLocked()
		return "Battery calibration could not finish: " + reason + " Start it again to retry.", ToastKindWarning
	}
	res.Finished = time.Now().Unix()
	res.CapStart, res.CapEnd = st.CapStart, capEnd
	st.Result = &res
	st.Correct = res.MaxErr > batteryCalTolerance && !res.Rough
	batteryCalPublishLocked()
	msg := "Battery calibrated — " + batteryCalFindings(&res)[0]
	if st.Correct {
		msg += " The % is corrected from now on."
	}
	return msg, ToastKindSuccess
}

// batteryCalStartSampler starts the sampler goroutine unless one is running.
func batteryCalStartSampler() {
	b := &batteryCal
	b.mu.Lock()
	if b.sampling {
		b.mu.Unlock()
		return
	}
	b.gen++
	gen := b.gen
	b.sampling = true
	b.mu.Unlock()
	go func() {
		defer func() {
			b.mu.Lock()
			if b.gen == gen {
				b.sampling = false
			}
			b.mu.Unlock()
		}()
		ticker := time.NewTicker(batteryCalEvery)
		defer ticker.Stop()
		for batteryCalTick(gen) {
			<-ticker.C
		}
	}()
}

// batteryCalStopLocked retires the sampler. Caller holds mu.
func batteryCalStopLocked() {
	batteryCal.gen++
	batteryCal.sampling = false
	batteryCal.paused = false
	batteryCal.fullSince = time.Time{}
}

// batteryCalTick takes one reading and moves the cycle on. It reports false
// when the sampler should stop.
func batteryCalTick(gen int) bool {
	r := batteryCalRead(batterySysfsDir())
	now := time.Now()
	batteryCalIO.Lock()
	b := &batteryCal
	b.mu.Lock()
	if b.gen != gen || (b.st.Step != batteryCalCharge && b.st.Step != batteryCalDrain) {
		b.mu.Unlock()
		batteryCalIO.Unlock()
		return false
	}
	b.live, b.liveAt = r, now
	toast, kind := "", ToastKindInfo
	var add *batteryCalSample
	switch {
	case r.Pct < 0:
		// No reading this time; the next minute tries again.
	case b.st.Step == batteryCalCharge:
		toast = batteryCalChargeTickLocked(r, now)
	case b.st.Step == batteryCalDrain:
		toast, kind, add = batteryCalDrainTickLocked(r, now)
	}
	more := b.st.Step == batteryCalCharge || b.st.Step == batteryCalDrain
	b.mu.Unlock()
	// The minute's append and its fsync run outside mu, which the screen
	// takes every frame; batteryCalIO still keeps the writes in order.
	if add != nil {
		batteryCalAppendLocked(*add)
	}
	batteryCalIO.Unlock()
	if toast != "" {
		ShowToast(toast, kind)
	}
	return more
}

// batteryCalChargeTickLocked is step 1's reading: full, then the cable out,
// starts the drain.
func batteryCalChargeTickLocked(r batteryCalReading, now time.Time) string {
	b := &batteryCal
	toast := ""
	if !b.st.Full && batteryCalFullNow(r, now) {
		b.st.Full = true
		batteryCalWriteStateLocked()
		toast = "Battery full — unplug it and play as usual until it switches itself off."
	}
	if b.st.Full && !r.Cable {
		b.st.Step, b.st.Boot, b.st.AskBoot = batteryCalDrain, 1, 0
		b.st.Started = now.Unix()
		b.st.CapStart = r.Health.FullCapacity
		first := batteryCalSample{Unix: now.Unix(), Pct: r.Pct, UV: r.VoltageUV, Boot: 1}
		b.samples = []batteryCalSample{first}
		b.lastMono, b.paused = now, false
		batteryCalRewriteSamplesLocked()
		batteryCalWriteStateLocked()
		toast = "Battery calibration step 2: play as usual until the handheld switches itself off."
	}
	return toast
}

// batteryCalFullNow reports a full battery: "Full" on the cable (or at 99%+,
// for gauges that keep the word after the cable is out), or 100% on the cable
// with almost no charge current for batteryCalFullHold. Caller holds mu.
func batteryCalFullNow(r batteryCalReading, now time.Time) bool {
	if r.Status == "full" && (r.Cable || r.Pct >= 99) {
		return true
	}
	quiet := !r.HaveCurrent || (r.CurrentMA <= batteryCalFullCurrentMA && r.CurrentMA >= -batteryCalFullCurrentMA)
	if !r.Cable || r.Pct < 100 || !quiet {
		batteryCal.fullSince = time.Time{}
		return false
	}
	if batteryCal.fullSince.IsZero() {
		batteryCal.fullSince = now
	}
	return now.Sub(batteryCal.fullSince) >= batteryCalFullHold
}

// batteryCalDrainTickLocked is step 2's reading. On the cable nothing is
// logged: after a last reading at the bottom that is the finish. A level
// that climbed back — on the cable, or charged while switched off — means the
// drain was interrupted and step 1 starts over. While the "did it switch
// itself off?" question is open nothing is thrown away, because the answer
// may still finish the run on the readings before it. A reading to append is
// returned for the caller to write once mu is released.
//
// On the cable the handheld is still alive, so only a gauge reading within
// batteryCalTolerance of 0 counts as the end there: a 5% reading or a voltage
// sag under load, with charge still left, would teach an accurate gauge a
// correction it does not need.
func batteryCalDrainTickLocked(r batteryCalReading, now time.Time) (string, ToastKind, *batteryCalSample) {
	b := &batteryCal
	n := len(b.samples)
	if r.Cable && n > 0 && b.st.AskBoot == 0 && b.samples[n-1].Pct <= batteryCalTolerance {
		toast, kind := batteryCalFinishLocked(b.samples, r.Health.FullCapacity)
		batteryCalWriteStateLocked()
		return toast, kind, nil
	}
	// Unplugged, only the first reading of a start can show a charge (made
	// while switched off); later a climb is the gauge bouncing with the load,
	// which the mapping's pooling absorbs.
	recharged := n > 0 && r.Pct >= batteryCalLowest(b.samples)+batteryCalRechargedPts
	if recharged && b.st.AskBoot > 0 {
		b.paused = true
		b.lastMono = now
		return "", ToastKindInfo, nil
	}
	if recharged && (r.Cable || b.lastMono.IsZero()) {
		batteryCalRestartLocked()
		return "Battery calibration: it charged before running out. Charge it to full, then unplug to start the drain again.", ToastKindWarning, nil
	}
	if r.Cable {
		b.paused = true
		b.lastMono = now // time on the cable is not use
		return "", ToastKindInfo, nil
	}
	b.paused = false
	s := batteryCalSample{Unix: now.Unix(), Pct: r.Pct, UV: r.VoltageUV, Boot: b.st.Boot}
	if n > 0 {
		prev := b.samples[n-1]
		wall := time.Duration(now.Unix()-prev.Unix) * time.Second
		credit, est := batteryCalCredit(now.Sub(b.lastMono), !b.lastMono.IsZero(), wall, prev.Pct-r.Pct, batteryCalRate(b.samples))
		s.Awake, s.Est = prev.Awake+credit, est
	}
	b.lastMono = now
	b.samples = append(b.samples, s)
	if len(b.samples) > batteryCalMaxSamples {
		b.samples = batteryCalThin(b.samples)
		batteryCalRewriteSamplesLocked()
		return "", ToastKindInfo, nil
	}
	return "", ToastKindInfo, &s
}

// batteryCalLowest is the lowest level of the drain so far.
func batteryCalLowest(samples []batteryCalSample) int {
	lowest := 100
	for _, s := range samples {
		lowest = min(lowest, s.Pct)
	}
	return lowest
}

// batteryCalRestartLocked sends an interrupted drain back to step 1. Caller
// holds batteryCalIO and mu.
func batteryCalRestartLocked() {
	b := &batteryCal
	b.st.Step, b.st.Full, b.st.Boot, b.st.AskBoot = batteryCalCharge, false, 0, 0
	b.samples, b.paused, b.fullSince = nil, false, time.Time{}
	batteryCalRemoveSamplesLocked()
	batteryCalWriteStateLocked()
}

// batteryCalThin keeps every other reading, and always the first and last.
func batteryCalThin(samples []batteryCalSample) []batteryCalSample {
	out := make([]batteryCalSample, 0, len(samples)/2+2)
	for i, s := range samples {
		if i%2 == 0 || i == len(samples)-1 {
			out = append(out, s)
		}
	}
	return out
}

// batteryCalAppendLocked appends one reading and syncs it to the card. If a
// power cut tore the last line, a newline goes first so the new reading does
// not join the torn one. Caller holds batteryCalIO.
func batteryCalAppendLocked(s batteryCalSample) {
	path := batteryCalPath(batteryCalSamplesFile)
	if path == "" {
		return
	}
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		Log().Warn("battery calibration: cannot open the readings file", "err", err)
		return
	}
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); (err == nil || errors.Is(err, io.EOF)) && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, err = f.Write(append(line, '\n'))
	if syncErr := f.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		Log().Warn("battery calibration: saving a reading failed", "err", err)
	}
}

// batteryCalRewriteSamplesLocked writes the whole run atomically (the first
// reading of a drain, and after thinning). Caller holds batteryCalIO and mu.
func batteryCalRewriteSamplesLocked() {
	path := batteryCalPath(batteryCalSamplesFile)
	if path == "" {
		return
	}
	var buf []byte
	for _, s := range batteryCal.samples {
		line, err := json.Marshal(s)
		if err != nil {
			continue
		}
		buf = append(append(buf, line...), '\n')
	}
	if err := AtomicWrite(path, buf, 0o644); err != nil {
		Log().Warn("battery calibration: saving the readings failed", "err", err)
	}
}

// batteryCalRemoveSamplesLocked deletes the readings file and the backup
// AtomicWrite left beside it. Caller holds batteryCalIO.
func batteryCalRemoveSamplesLocked() {
	path := batteryCalPath(batteryCalSamplesFile)
	if path == "" {
		return
	}
	for _, p := range []string{path, path + ".bak"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			Log().Warn("battery calibration: removing the readings failed", "err", err)
		}
	}
}

// ── Actions (from the screen; each runs its I/O on a goroutine) ──────────

// batteryCalBegin starts a cycle at step 1. A battery already full on the
// cable passes step 1 on the sampler's first reading.
func batteryCalBegin() {
	go func() {
		batteryCalIO.Lock()
		b := &batteryCal
		b.mu.Lock()
		batteryCalStopLocked()
		b.st.Step, b.st.Full, b.st.Boot, b.st.AskBoot = batteryCalCharge, false, 0, 0
		b.st.Started, b.st.CapStart = time.Now().Unix(), 0
		b.samples = nil
		batteryCalRemoveSamplesLocked()
		batteryCalWriteStateLocked()
		b.mu.Unlock()
		batteryCalIO.Unlock()
		batteryCalStartSampler()
	}()
}

// batteryCalCancel stops the cycle; an earlier result and its correction stay.
func batteryCalCancel() {
	go func() {
		batteryCalIO.Lock()
		defer batteryCalIO.Unlock()
		b := &batteryCal
		b.mu.Lock()
		defer b.mu.Unlock()
		batteryCalStopLocked()
		b.st.Step, b.st.Full, b.st.Boot, b.st.AskBoot = batteryCalIdle, false, 0, 0
		b.samples = nil
		batteryCalRemoveSamplesLocked()
		batteryCalWriteStateLocked()
	}()
}

// batteryCalReset forgets the result and the correction, and any cycle.
func batteryCalReset() {
	go func() {
		batteryCalIO.Lock()
		defer batteryCalIO.Unlock()
		b := &batteryCal
		b.mu.Lock()
		defer b.mu.Unlock()
		batteryCalStopLocked()
		b.st = batteryCalState{}
		b.samples = nil
		batteryCalPublishLocked()
		batteryCalRemoveSamplesLocked()
		batteryCalWriteStateLocked()
	}()
}

// batteryCalSetCorrect switches the correction; only a result can be applied.
func batteryCalSetCorrect(on bool) {
	b := &batteryCal
	b.mu.Lock()
	if b.st.Result == nil {
		on = false
	}
	b.st.Correct = on
	batteryCalPublishLocked()
	b.mu.Unlock()
	batteryCalPersist()
}

// batteryCalAnswer is the user's answer to "did it switch itself off?". Yes
// finishes the run on the readings before that start; no keeps it going.
func batteryCalAnswer(switchedOff bool) {
	go func() {
		health := batteryHealthReport()
		batteryCalIO.Lock()
		b := &batteryCal
		b.mu.Lock()
		toast, kind := "", ToastKindInfo
		if b.st.Step == batteryCalDrain && b.st.AskBoot > 0 {
			if switchedOff {
				var run []batteryCalSample
				for _, s := range b.samples {
					if s.Boot < b.st.AskBoot {
						run = append(run, s)
					}
				}
				toast, kind = batteryCalFinishLocked(run, health.FullCapacity)
			} else if n := len(b.samples); n > 0 && b.live.Pct >= batteryCalLowest(b.samples)+batteryCalRechargedPts {
				// Charged meanwhile: the drain cannot carry on from here.
				batteryCalRestartLocked()
				toast, kind = "Battery calibration: it was charged since, so the drain starts over. Charge it to full, then unplug.", ToastKindWarning
			} else {
				b.st.AskBoot = 0
				toast = "Calibration continues — keep playing until it switches itself off."
			}
			batteryCalWriteStateLocked()
		}
		b.mu.Unlock()
		batteryCalIO.Unlock()
		if toast != "" {
			ShowToast(toast, kind)
		}
	}()
}

// batteryCalRefreshLive keeps the screen's reading under two seconds old,
// reading on a goroutine.
func batteryCalRefreshLive() {
	b := &batteryCal
	b.mu.Lock()
	want := !b.liveBusy && time.Since(b.liveAt) >= 2*time.Second
	if want {
		b.liveBusy = true
	}
	b.mu.Unlock()
	if !want {
		return
	}
	go func() {
		r := batteryCalRead(batterySysfsDir())
		b.mu.Lock()
		b.live, b.liveAt, b.liveBusy = r, time.Now(), false
		b.mu.Unlock()
	}()
}

// batteryCalView is a copy of everything the screen shows.
type batteryCalView struct {
	loaded   bool
	st       batteryCalState
	live     batteryCalReading
	liveOK   bool
	paused   bool
	count    int
	last     batteryCalSample
	used     float64 // seconds of use logged in this drain
	lastBoot batteryCalSample
}

func batteryCalSnapshot() batteryCalView {
	b := &batteryCal
	b.mu.Lock()
	defer b.mu.Unlock()
	v := batteryCalView{loaded: b.loaded, st: b.st, live: b.live, liveOK: !b.liveAt.IsZero(), paused: b.paused, count: len(b.samples)}
	if n := len(b.samples); n > 0 {
		v.last = b.samples[n-1]
		v.used = v.last.Awake - b.samples[0].Awake
		for i := n - 1; i >= 0; i-- {
			if b.st.AskBoot == 0 || b.samples[i].Boot < b.st.AskBoot {
				v.lastBoot = b.samples[i]
				break
			}
		}
	}
	return v
}

// batteryCalTileValue is the Tools hub tile's line. No file is read.
func batteryCalTileValue() string {
	batteryCalEnsureLoaded()
	b := &batteryCal
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return ""
	}
	switch {
	case b.st.Step == batteryCalCharge:
		return "Step 1 of 3 — charge to full"
	case b.st.Step == batteryCalDrain && b.st.AskBoot > 0:
		return "Step 3 of 3 — one question"
	case b.st.Step == batteryCalDrain:
		return "Step 2 of 3 — run it down"
	case b.st.Result == nil:
		return "Not calibrated"
	}
	s := "Calibrated"
	if f := time.Unix(b.st.Result.Finished, 0); b.st.Result.Finished > 0 && f.Year() >= 2025 {
		s += " " + formatDateShort(nil, f)
	}
	if b.st.Correct {
		return s + " · corrected"
	}
	return s
}
