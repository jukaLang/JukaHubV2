package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// withBrightnessBackend pins the brightness controller to backend b for one
// test: the real probe is marked as run (it would read /dev/disp, or start
// PowerShell on Windows), the cache starts empty, and both are put back after.
func withBrightnessBackend(t *testing.T, b string, trimui bool) {
	t.Helper()
	brightnessAvail.once.Do(func() {})
	brightnessAvail.mu.Lock()
	prevBackend := brightnessAvail.backend
	brightnessAvail.backend = b
	brightnessAvail.mu.Unlock()
	prevTrimui := brightnessOnTrimui
	brightnessOnTrimui = func() bool { return trimui }
	resetBrightnessLevelForTest()
	t.Cleanup(func() {
		brightnessWaitQuiet()
		brightnessAvail.mu.Lock()
		brightnessAvail.backend = prevBackend
		brightnessAvail.mu.Unlock()
		brightnessOnTrimui = prevTrimui
		brightnessBorrowed.Store(false)
		brightnessSoftDim.Store(0)
		brightnessLastRaw.Store(0)
		resetBrightnessLevelForTest()
	})
}

func resetBrightnessLevelForTest() {
	brightnessLevel.mu.Lock()
	defer brightnessLevel.mu.Unlock()
	brightnessLevel.pct, brightnessLevel.known, brightnessLevel.restore = 0, false, 0
	brightnessLevel.pollAt = time.Time{}
	brightnessLevel.polling, brightnessLevel.rerun = false, false
	brightnessLevel.seenStep, brightnessLevel.seenRaw = 0, 0
	// The counters only ever grow: the worker may still finish an older
	// change, and a reset to 0 would let it look newer than the next one.
	brightnessLevel.appliedGen = brightnessLevel.editGen
}

// brightnessWaitApplied waits until the worker has written every change asked
// for so far.
func brightnessWaitApplied(t *testing.T) {
	t.Helper()
	if !brightnessWaitQuiet() {
		t.Fatal("the brightness worker did not apply the last change")
	}
}

// brightnessWaitQuiet is the wait without a verdict, for the cleanup.
func brightnessWaitQuiet() bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		brightnessLevel.mu.Lock()
		done := brightnessLevel.appliedGen >= brightnessLevel.editGen
		brightnessLevel.mu.Unlock()
		if done {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestBrightnessSnapAndNormalize(t *testing.T) {
	for _, c := range []struct{ in, grid, want int }{
		{0, 5, 0}, {2, 5, 0}, {3, 5, 5}, {37, 5, 35}, {38, 5, 40}, {99, 5, 100}, {-5, 5, 0}, {130, 5, 100},
		{44, 10, 40}, {45, 10, 50}, {96, 10, 100}, {7, 1, 7},
	} {
		if got := brightnessSnapTo(c.in, c.grid); got != c.want {
			t.Errorf("brightnessSnapTo(%d, %d) = %d, want %d", c.in, c.grid, got, c.want)
		}
	}
	prev := brightnessOnTrimui
	defer func() { brightnessOnTrimui = prev }()
	brightnessOnTrimui = func() bool { return false }
	for _, c := range []struct{ in, want int }{{0, 5}, {2, 5}, {7, 5}, {8, 10}, {42, 40}, {100, 100}, {160, 100}} {
		if got := brightnessNormalize(brightnessBackendSoft, c.in); got != c.want {
			t.Errorf("soft: brightnessNormalize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	// On a TrimUI the display driver walks the firmware's tenths, never under
	// its first step; the software dimmer keeps the 5% grid.
	brightnessOnTrimui = func() bool { return true }
	for _, c := range []struct{ in, want int }{{0, 10}, {4, 10}, {14, 10}, {15, 20}, {44, 40}, {95, 100}} {
		if got := brightnessNormalize(brightnessBackendDisp, c.in); got != c.want {
			t.Errorf("TrimUI disp: brightnessNormalize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	if brightnessGridFor(brightnessBackendSoft) != brightnessStep || !brightnessOwnedByJukaHub(brightnessBackendSoft) {
		t.Fatal("the software dimmer is JukaHub's own, on the 5% grid, on a TrimUI too")
	}
	if brightnessOwnedByJukaHub(brightnessBackendDisp) || brightnessOwnedByJukaHub(brightnessBackendWMI) {
		t.Fatal("the TrimUI firmware and Windows keep their own level: nothing is restored over it")
	}
	brightnessOnTrimui = func() bool { return false }
	if !brightnessOwnedByJukaHub(brightnessBackendSysfs) || brightnessFirmwareSync(brightnessBackendSysfs) {
		t.Fatal("a plain Linux backlight is JukaHub's own and has no firmware file")
	}
}

// Every level the card sets reads back as itself from the display driver's
// 0..255 scale, even from a driver that counts its stored level from 1.
func TestBrightnessDispScaleRoundTrip(t *testing.T) {
	for pct := 5; pct <= 100; pct += 5 {
		raw := brightnessRawFor(pct, dispBrightnessMax)
		if raw < 1 || raw > dispBrightnessMax {
			t.Fatalf("%d%% became raw %d, outside 1..255", pct, raw)
		}
		if back := brightnessPercentFor(raw, dispBrightnessMax); back != pct {
			t.Errorf("%d%% (raw %d) reads back as %d%%", pct, raw, back)
		}
		plusOne := raw + 1
		if plusOne > dispBrightnessMax {
			plusOne = dispBrightnessMax
		}
		if back := brightnessSnapTo(brightnessPercentFor(plusOne, dispBrightnessMax), brightnessStep); back != pct {
			t.Errorf("%d%% stored as raw %d reads back as %d%%", pct, plusOne, back)
		}
	}
	if raw := brightnessRawFor(0, dispBrightnessMax); raw < 1 {
		t.Fatalf("0%% became raw %d: raw 0 switches the backlight off", raw)
	}
}

func TestBrightnessSoftAlphaAndGlyph(t *testing.T) {
	if a := brightnessSoftAlpha(100); a != 0 {
		t.Fatalf("full brightness draws a layer of alpha %d", a)
	}
	if a := brightnessSoftAlpha(qsBrightnessFloorPct); a < 150 || a > 210 {
		t.Fatalf("the floor's layer is alpha %d: too light to dim or too dark to read", a)
	}
	prev := uint8(255)
	for pct := 0; pct <= 100; pct += 5 {
		a := brightnessSoftAlpha(pct)
		if a > prev {
			t.Fatalf("the layer got darker from %d%% up", pct)
		}
		prev = a
	}
	for _, c := range []struct {
		pct   int
		known bool
		want  string
	}{{0, false, "sun"}, {30, true, "sun-low"}, {49, true, "sun-low"}, {50, true, "sun"}, {100, true, "sun"}} {
		if got := brightnessGlyph(c.pct, c.known); got != c.want {
			t.Errorf("brightnessGlyph(%d, %v) = %q, want %q", c.pct, c.known, got, c.want)
		}
	}
}

func TestParseWMIBrightnessAndDDC(t *testing.T) {
	for _, c := range []struct {
		out          string
		want, levels int
		ok           bool
	}{
		{"70\r\n", 70, 0, true}, {"\r\n  45 \r\n\r\n", 45, 0, true}, {"WARNING: something\r\n80 101\r\n", 80, 101, true},
		{"30 11", 30, 11, true}, {"30 x", 30, 0, true}, {"80 11\r\nWARNING: trailing note\r\n", 80, 11, true},
		{"", 0, 0, false}, {"abc", 0, 0, false}, {"101", 0, 0, false}, {"-1", 0, 0, false},
	} {
		got, levels, ok := parseWMIBrightness(c.out)
		if got != c.want || levels != c.levels || ok != c.ok {
			t.Errorf("parseWMIBrightness(%q) = %d, %d, %v; want %d, %d, %v", c.out, got, levels, ok, c.want, c.levels, c.ok)
		}
	}
	for _, c := range []struct{ levels, grid int }{{0, 0}, {1, 0}, {2, 10}, {11, 10}, {16, 0}, {101, 0}} {
		if got := brightnessWMIGridFor(c.levels); got != c.grid {
			t.Errorf("brightnessWMIGridFor(%d) = %d, want %d", c.levels, got, c.grid)
		}
	}
	prevGrid := brightnessWMIGrid.Load()
	brightnessWMIGrid.Store(10)
	if brightnessGridFor(brightnessBackendWMI) != 10 || brightnessFloorFor(brightnessBackendWMI) != 10 ||
		brightnessNormalize(brightnessBackendWMI, 0) != 10 || brightnessNormalize(brightnessBackendWMI, 46) != 50 {
		t.Fatal("a panel with eleven levels must move, and bottom out, in tenths")
	}
	brightnessWMIGrid.Store(prevGrid)
	for pct := 0; pct <= 100; pct++ {
		if back := ddcPercentFor(ddcRawFor(pct, 0, 100), 0, 100); back != pct {
			t.Fatalf("DDC 0..100: %d%% reads back as %d%%", pct, back)
		}
	}
	if ddcRawFor(100, 0, 50) != 50 || ddcRawFor(0, 10, 60) != 10 || ddcPercentFor(25, 0, 50) != 50 {
		t.Fatal("a monitor's own range is not used")
	}
	if ddcRawFor(40, 7, 7) != 7 || ddcPercentFor(7, 7, 7) != 0 {
		t.Fatal("an empty range must not divide by zero")
	}
}

func TestBrightnessPollInterval(t *testing.T) {
	if _, polls := brightnessPollInterval("", true); polls {
		t.Fatal("nothing is polled before the probe answers")
	}
	for _, slow := range []string{brightnessBackendWMI, brightnessBackendDDC} {
		if _, polls := brightnessPollInterval(slow, false); polls {
			t.Fatalf("%s is polled with the card closed (a PowerShell run or a bus exchange each time)", slow)
		}
		if d, polls := brightnessPollInterval(slow, true); !polls || d != brightnessPollSlowOpen {
			t.Fatalf("%s with the card open: %v %v", slow, d, polls)
		}
	}
	if d, polls := brightnessPollInterval(brightnessBackendDisp, false); !polls || d != brightnessPollIdle {
		t.Fatalf("disp idle: %v %v", d, polls)
	}
	if d, polls := brightnessPollInterval(brightnessBackendSoft, true); !polls || d != brightnessPollOpen {
		t.Fatalf("soft open: %v %v", d, polls)
	}
}

// The controller on the software dimmer: levels snap to the grid and keep the
// floor, Dim goes to the floor and back, a step that changes nothing writes
// nothing, and the QR card's borrow leaves the user's level in the cache and
// gives it back.
func TestBrightnessControllerSoft(t *testing.T) {
	withBrightnessBackend(t, brightnessBackendSoft, false)
	if got := brightnessApplyLevel(42); got != 40 {
		t.Fatalf("42%% applied as %d, want the grid's 40", got)
	}
	brightnessWaitApplied(t)
	if pct, ok := brightnessHardwareLevel(brightnessBackendSoft); !ok || pct != 40 {
		t.Fatalf("the dimmer reads %d%%, %v", pct, ok)
	}
	if got := brightnessApplyLevel(1); got != qsBrightnessFloorPct {
		t.Fatalf("1%% applied as %d, want the floor", got)
	}
	brightnessApplyLevel(70)
	if got := brightnessToggleDim(nil); got != qsBrightnessFloorPct {
		t.Fatalf("Dim went to %d", got)
	}
	if !brightnessDimmed(brightnessBase(), true) {
		t.Fatal("at the floor the card must say dimmed")
	}
	if got := brightnessToggleDim(nil); got != 70 {
		t.Fatalf("Brighten returned to %d, want 70", got)
	}
	brightnessLevel.mu.Lock()
	gen := brightnessLevel.editGen
	brightnessLevel.mu.Unlock()
	brightnessApplyLevel(100)
	before, after := brightnessStepBy(nil, brightnessStep)
	brightnessLevel.mu.Lock()
	wrote := brightnessLevel.editGen - gen
	brightnessLevel.mu.Unlock()
	if before != 100 || after != 100 || wrote != 1 {
		t.Fatalf("a step past the top: %d -> %d with %d writes, want no change and only the earlier write", before, after, wrote)
	}
	if _, after := brightnessStepBy(nil, -brightnessStep); after != 95 {
		t.Fatalf("a step down from 100 reached %d", after)
	}

	brightnessWaitApplied(t)
	if !brightnessBorrow(brightnessFullPct) {
		t.Fatal("95% must be borrowed up to full")
	}
	brightnessWaitApplied(t)
	if pct, _ := brightnessHardwareLevel(brightnessBackendSoft); pct != 100 {
		t.Fatalf("while borrowed the screen is at %d%%", pct)
	}
	if pct, _ := brightnessCachedLevelForTest(); pct != 95 {
		t.Fatalf("the borrow changed the user's level to %d", pct)
	}
	brightnessGiveBack()
	brightnessWaitApplied(t)
	if pct, _ := brightnessHardwareLevel(brightnessBackendSoft); pct != 95 {
		t.Fatalf("after the give-back the screen is at %d%%", pct)
	}
	brightnessGiveBack() // a second give-back is a no-op
	brightnessApplyLevel(100)
	if brightnessBorrow(brightnessFullPct) {
		t.Fatal("a screen already at full has nothing to borrow")
	}
}

func brightnessCachedLevelForTest() (int, bool) {
	brightnessLevel.mu.Lock()
	defer brightnessLevel.mu.Unlock()
	return brightnessLevel.pct, brightnessLevel.known
}

// A reading is kept only when it began after the newest change landed, and it
// is snapped onto the grid; only a level above the floor becomes the one
// Brighten returns to.
func TestBrightnessReadingRules(t *testing.T) {
	withBrightnessBackend(t, brightnessBackendSoft, false)
	brightnessLevel.mu.Lock()
	defer brightnessLevel.mu.Unlock()
	gen := brightnessLevel.editGen
	reading := func(pct int) brightnessReading { return brightnessReading{pct: pct, ok: true, step: -1} }
	brightnessNoteReadingLocked(brightnessBackendSoft, reading(51), gen, gen)
	if brightnessLevel.pct != 50 || !brightnessLevel.known || brightnessLevel.restore != 50 {
		t.Fatalf("a current reading of 51 became %+v", brightnessLevel.pct)
	}
	brightnessNoteReadingLocked(brightnessBackendSoft, reading(3), gen, gen)
	if brightnessLevel.pct != qsBrightnessFloorPct || brightnessLevel.restore != 50 {
		t.Fatalf("a reading under the floor: pct %d restore %d", brightnessLevel.pct, brightnessLevel.restore)
	}
	brightnessNoteReadingLocked(brightnessBackendSoft, reading(80), gen-1, gen-1)
	if brightnessLevel.pct == 80 {
		t.Fatal("a reading that began before the newest change must be dropped")
	}
}

// Quick Settings moves its sliders by 5%; on the TrimUI's 10% grid a step
// down used to round back up to where it was, so the slider could only rise.
// Every step now moves at least one grid step, and stops at the ends.
func TestBrightnessStepTargetOnACoarseGrid(t *testing.T) {
	prev := brightnessOnTrimui
	defer func() { brightnessOnTrimui = prev }()
	brightnessOnTrimui = func() bool { return true }
	b := brightnessBackendDisp
	for _, c := range []struct{ from, delta, want int }{
		{50, -5, 40}, {50, 5, 60}, {40, -5, 30}, {10, -5, 10}, {100, 5, 100}, {50, -10, 40}, {50, 20, 70}, {50, 0, 50},
	} {
		if got := brightnessStepTarget(b, c.from, c.delta); got != c.want {
			t.Errorf("TrimUI: step %+d from %d = %d, want %d", c.delta, c.from, got, c.want)
		}
	}
	brightnessOnTrimui = func() bool { return false }
	if got := brightnessStepTarget(brightnessBackendSoft, 50, -5); got != 45 {
		t.Fatalf("a 5%% step on the 5%% grid went to %d", got)
	}
}

// The TrimUI firmware's own curve: a step is written with the raw level the
// panel was seen to have at that step (linear until then), read back to the
// same step, and learned only from a reading that was current.
func TestBrightnessFirmwareCurve(t *testing.T) {
	withBrightnessBackend(t, brightnessBackendDisp, true)
	defer func() {
		for i := range brightnessFirmwareCurve {
			brightnessFirmwareCurve[i].Store(0)
		}
	}()
	b := brightnessBackendDisp
	if raw := brightnessRawForLevel(b, 50, dispBrightnessMax); raw != brightnessRawFor(50, dispBrightnessMax) {
		t.Fatalf("an unseen step is written as raw %d, want the linear %d", raw, brightnessRawFor(50, dispBrightnessMax))
	}
	brightnessLevel.mu.Lock()
	gen := brightnessLevel.editGen
	seen := brightnessReading{pct: 50, ok: true, step: 5, raw: 48}
	brightnessNoteReadingLocked(b, seen, gen, gen)
	learnedAfterOne := brightnessFirmwareCurve[5].Load()
	brightnessNoteReadingLocked(b, seen, gen, gen)
	brightnessNoteReadingLocked(b, brightnessReading{pct: 70, ok: true, step: 7, raw: 99}, gen-1, gen-1)
	brightnessNoteReadingLocked(b, brightnessReading{pct: 70, ok: true, step: 7, raw: 99}, gen-1, gen-1)
	brightnessLevel.mu.Unlock()
	if learnedAfterOne != 0 {
		t.Fatal("one reading must not teach the curve: it may have caught the firmware between panel and file")
	}
	if raw := brightnessRawForLevel(b, 50, dispBrightnessMax); raw != 48 {
		t.Fatalf("a step seen twice is written as raw %d, want the firmware's 48", raw)
	}
	if raw := brightnessRawForLevel(b, 70, dispBrightnessMax); raw == 99 {
		t.Fatal("a stale reading must not teach the curve")
	}
	if pct := brightnessLevelForRaw(b, 48, dispBrightnessMax); pct != 50 {
		t.Fatalf("the firmware's raw level for step 5 reads back as %d%%", pct)
	}
	if pct := brightnessLevelForRaw(b, brightnessRawFor(80, dispBrightnessMax), dispBrightnessMax); pct != 80 {
		t.Fatalf("an unseen step's linear raw level reads back as %d%%", pct)
	}
	if pct := brightnessLevelForRaw(b, 1, dispBrightnessMax); pct != 10 {
		t.Fatalf("the firmware's dimmest level reads as %d%%, want the floor", pct)
	}
	brightnessLearnFirmwareStep(11, 200)
	brightnessLearnFirmwareStep(3, 0)
	if brightnessFirmwareCurve[3].Load() != 0 {
		t.Fatal("a raw level of 0 (backlight off) is not a step of the curve")
	}

	// JukaHub's own write is never learned back, nor a level one off it.
	brightnessLastRaw.Store(60)
	defer brightnessLastRaw.Store(0)
	brightnessLevel.mu.Lock()
	for _, raw := range []int{60, 60, 61, 61} {
		brightnessNoteReadingLocked(b, brightnessReading{pct: 60, ok: true, step: 6, raw: raw}, gen, gen)
	}
	ownLearned := brightnessFirmwareCurve[6].Load()
	for i := 0; i < 2; i++ {
		brightnessNoteReadingLocked(b, brightnessReading{pct: 60, ok: true, step: 6, raw: 70}, gen, gen)
	}
	brightnessLevel.mu.Unlock()
	if ownLearned != 0 {
		t.Fatalf("JukaHub's own raw level was learned as the firmware's (%d)", ownLearned)
	}
	if got := brightnessFirmwareCurve[6].Load(); got != 70 {
		t.Fatalf("the firmware's own level for step 6 was not learned: %d", got)
	}
}

// The firmware's own level: read from system.json in tenths, not while a
// level of JukaHub's is on its way there, written with every other key left
// exactly as it was, and left alone altogether when the file keeps it on some
// other scale.
func TestTrimuiSystemBrightness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	doc := "{\n\t\"vol\":\t7,\n\t\"brightness\":\t6,\n\t\"lumination\":\t7,\n\t\"hue\":\t10\n}\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = path
	defer func() {
		trimuiSystemJSONPath = prev
		trimuiBrightnessScaleBad.Store(false)
	}()
	trimuiBrightnessScaleBad.Store(false)
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 6 {
		t.Fatalf("the firmware's level reads %d, %v; want 6", step, ok)
	}
	trimuiBrightnessSyncLater(30)
	if _, ok := trimuiSystemBrightnessStep(); ok {
		t.Fatal("while a level of JukaHub's is on its way the file is stale")
	}
	trimuiBrightnessFlush()
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 3 {
		t.Fatalf("after the write the level reads %d, %v; want 3", step, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(doc, "\"brightness\":\t6", "\"brightness\":\t3", 1); string(data) != want {
		t.Fatalf("the rewrite touched more than brightness:\n%s", data)
	}
	// The volume's sync shares the writer and leaves the brightness alone.
	if err := trimuiSystemVolumeWrite(12); err != nil {
		t.Fatal(err)
	}
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 3 {
		t.Fatalf("a volume write moved the brightness to %d, %v", step, ok)
	}

	if err := os.WriteFile(path, []byte(`{"vol": 7, "brightness": 15}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := trimuiSystemBrightnessLevel(); ok || !trimuiBrightnessScaleBad.Load() {
		t.Fatal("a level outside 0..10 is some other scale: neither read nor trusted")
	}
	trimuiBrightnessSyncLater(50)
	if pending, _ := trimuiBrightnessSyncState(); pending {
		t.Fatal("nothing may be written to a file on an unknown scale")
	}
	for _, c := range []struct{ pct, step int }{{0, 0}, {10, 1}, {44, 4}, {45, 5}, {100, 10}, {130, 10}} {
		if got := trimuiBrightnessStepFor(c.pct); got != c.step {
			t.Errorf("trimuiBrightnessStepFor(%d) = %d, want %d", c.pct, got, c.step)
		}
	}
}

// The firmware's file is rewritten on its own inode (keymon's inotify watch is
// on it), never left shorter than it was with a stale tail, and not written at
// all while it does not parse (the firmware may be mid-write).
func TestTrimuiSystemJSONRewriteInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	doc := "{\"brightness\": 10, \"vol\": 15}\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = path
	defer func() { trimuiSystemJSONPath = prev }()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	set := func(v int) error {
		return trimuiSystemJSONRewrite(func(data []byte) ([]byte, bool) {
			return trimuiSystemJSONWithInt(data, "brightness", v, trimuiBrightnessSteps)
		})
	}
	if err := set(4); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the file was replaced: keymon's watch on it would be lost")
	}
	data, _ := os.ReadFile(path)
	if len(data) != len(doc) || !strings.HasPrefix(string(data), "{\"brightness\": 4, \"vol\": 15}\n") {
		t.Fatalf("a shorter document must be padded to the old length, got %q", data)
	}
	if v, ok, _ := trimuiSystemJSONIntFrom(data, "brightness", trimuiBrightnessSteps); !ok || v != 4 {
		t.Fatalf("the padded file reads brightness %d, %v", v, ok)
	}
	// Growing again uses up the padding instead of growing the file.
	if err := set(10); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != doc {
		t.Fatalf("back at 10 the file is %q, want %q", data, doc)
	}

	if err := os.WriteFile(path, []byte(`{"brightness": 6, "vol`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := set(3); !errors.Is(err, errTrimuiSystemJSONChanged) {
		t.Fatalf("a half-written file must be retried, got %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"brightness": 6, "vol` {
		t.Fatalf("a half-written file was written over: %q", data)
	}
	if got := string(trimuiFitLength([]byte("ab"), 4)); got != "ab  " {
		t.Fatalf("trimuiFitLength pad = %q", got)
	}
	if got := string(trimuiFitLength([]byte("abc  "), 4)); got != "abc " {
		t.Fatalf("trimuiFitLength trim = %q", got)
	}
	if got := string(trimuiFitLength([]byte("abcde"), 4)); got != "abcde" {
		t.Fatalf("trimuiFitLength must not cut the document itself, got %q", got)
	}
}

func TestTrimuiSystemJSONWithInt(t *testing.T) {
	doc := []byte(`{"screen_brightness": 2, "brightness": 6, "lumination": 7}`)
	out, changed := trimuiSystemJSONWithInt(doc, "brightness", 4, trimuiBrightnessSteps)
	if !changed || string(out) != `{"screen_brightness": 2, "brightness": 4, "lumination": 7}` {
		t.Fatalf("rewrite = %s, %v", out, changed)
	}
	if _, changed := trimuiSystemJSONWithInt(out, "brightness", 4, trimuiBrightnessSteps); changed {
		t.Fatal("the same level must not rewrite the file")
	}
	if _, changed := trimuiSystemJSONWithInt([]byte(`{"brightness": 3, "x": {"brightness": 4}}`), "brightness", 5, 10); changed {
		t.Fatal("two \"brightness\" members are ambiguous and must be left alone")
	}
	if _, changed := trimuiSystemJSONWithInt([]byte(`{"vol": 3}`), "brightness", 5, 10); changed {
		t.Fatal("a file without the key gets no key added")
	}
	if v, ok, inRange := trimuiSystemJSONIntFrom([]byte(`{"brightness": 12.6}`), "brightness", 10); !ok || inRange || v != 10 {
		t.Fatalf("12.6 on a 0..10 scale read as %d, ok %v, inRange %v", v, ok, inRange)
	}
}

// The sun, the speaker and the meter sit side by side: every gap between two
// of them is split, so each tap belongs to exactly one, each target covers its
// chip, the leading sun still grows to a finger's width and the meter stops
// short of the forecast.
func TestGrowStatusButtonRectsThreeButtons(t *testing.T) {
	saved := [3]sdl.Rect{statusBrightnessRect, statusVolumeRect, statusWiFiRect}
	defer func() {
		statusBrightnessRect, statusVolumeRect, statusWiFiRect = saved[0], saved[1], saved[2]
	}()
	bri := sdl.Rect{X: 860, Y: 10, W: 28, H: 30}
	vol := sdl.Rect{X: 900, Y: 10, W: 28, H: 30}
	wifi := sdl.Rect{X: 940, Y: 10, W: 30, H: 30}
	weather := sdl.Rect{X: 984, Y: 10, W: 80, H: 30}
	statusBrightnessRect, statusVolumeRect, statusWiFiRect = bri, vol, wifi
	growStatusButtonRects(56, weather)
	gb, gv, gw := statusBrightnessRect, statusVolumeRect, statusWiFiRect
	if gb.X+gb.W != gv.X || gv.X+gv.W != gw.X {
		t.Fatalf("gaps not split exactly: sun %+v, speaker %+v, meter %+v", gb, gv, gw)
	}
	for _, c := range []struct{ grown, chip sdl.Rect }{{gb, bri}, {gv, vol}, {gw, wifi}} {
		if c.grown.X > c.chip.X || c.grown.X+c.grown.W < c.chip.X+c.chip.W {
			t.Fatalf("target %+v is smaller than its chip %+v", c.grown, c.chip)
		}
	}
	if gb.W < 44 {
		t.Fatalf("the leading sun's target %+v is under 44px", gb)
	}
	if gw.X+gw.W > weather.X {
		t.Fatalf("the meter reaches the forecast: %+v", gw)
	}

	// Without a mixer the sun and the meter are neighbours: their targets may
	// not overlap, and the missing speaker gets none.
	statusBrightnessRect, statusVolumeRect, statusWiFiRect = bri, sdl.Rect{}, wifi
	growStatusButtonRects(56, weather)
	if statusBrightnessRect.X+statusBrightnessRect.W > statusWiFiRect.X || statusVolumeRect.W != 0 {
		t.Fatalf("sun %+v and meter %+v overlap (speaker %+v)", statusBrightnessRect, statusWiFiRect, statusVolumeRect)
	}
}

// A crowded bar gives up the sun after the network meter, the forecast and
// the offline badge, and before the speaker.
func TestStatusDropIndexBrightnessBeforeVolume(t *testing.T) {
	parts := []string{statusBrightnessPart, statusVolumePart, statusWiFiMarker, "21C / 12C", "12:40", "87%"}
	var order []string
	for len(parts) > 2 {
		i := statusDropIndex(parts)
		order = append(order, parts[i])
		parts = append(parts[:i:i], parts[i+1:]...)
	}
	want := []string{statusWiFiMarker, "21C / 12C", statusBrightnessPart, statusVolumePart}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("dropped %q, want %q", order, want)
	}
	if w := statusClusterPartWidth(nil, statusBrightnessPart); w != statusClusterPartWidth(nil, statusVolumePart) || w <= 0 {
		t.Fatalf("the sun is %dpx, not the speaker's box", w)
	}
	if !isBrightnessStatusPart(statusBrightnessPart) || isBrightnessStatusPart(statusVolumePart) || isVolumeStatusPart(statusBrightnessPart) {
		t.Fatal("the two markers must not be confused")
	}
}

func TestBrightnessPercentAtX(t *testing.T) {
	track := sdl.Rect{X: 100, Y: 0, W: 200, H: 6}
	for _, c := range []struct {
		x    int32
		want int
	}{{100, 0}, {200, 50}, {300, 100}, {40, 0}, {400, 100}} {
		if got := brightnessPercentAtX(track, c.x); got != c.want {
			t.Errorf("brightnessPercentAtX(%d) = %d, want %d", c.x, got, c.want)
		}
	}
	if brightnessPercentAtX(sdl.Rect{}, 10) != 0 {
		t.Fatal("an unmeasured track must not divide by zero")
	}
}

// The frame tick closes a header card only when something full screen covers
// it: an open card is on the shared "owns the input" lists itself, and must
// not count as its own cover (it closed the frame it opened).
func TestHeaderCardsCloseOnlyWhenCovered(t *testing.T) {
	withBrightnessBackend(t, brightnessBackendSoft, false)
	prevViewer := imageViewerPath
	defer func() {
		brightnessPopoverOpen, volumePopoverOpen = false, false
		imageViewerPath = prevViewer
	}()
	imageViewerPath = ""
	brightnessPopoverOpen = true
	brightnessFrameTick()
	if !brightnessPopoverOpen {
		t.Fatal("an open brightness card closed itself")
	}
	brightnessPopoverOpen, volumePopoverOpen = false, true
	brightnessFrameTick()
	if !volumePopoverOpen {
		t.Fatal("an open volume card closed itself")
	}
	imageViewerPath = "picture.png"
	brightnessFrameTick()
	if volumePopoverOpen || brightnessPopoverOpen {
		t.Fatal("the image viewer covers the header: the card must close")
	}
}

// The QR card borrows the backlight while it is up and gives it back once,
// on whichever path closes it; with nothing borrowed there is nothing to give.
func TestQRZoomBorrowsTheBacklight(t *testing.T) {
	prevBorrow, prevGive := qrZoomBorrowBrightness, qrZoomGiveBackBrightness
	defer func() {
		qrZoomBorrowBrightness, qrZoomGiveBackBrightness = prevBorrow, prevGive
		qrZoom = qrZoomState{}
	}()
	borrows, gives, grant := 0, 0, true
	qrZoomBorrowBrightness = func(pct int) bool {
		borrows++
		if pct != qrZoomBrightness {
			t.Fatalf("borrowed %d%%, want %d", pct, qrZoomBrightness)
		}
		return grant
	}
	qrZoomGiveBackBrightness = func() { gives++ }
	if !qrZoomOpenCard([][]bool{{true}}, "Pair") {
		t.Fatal("the card did not open")
	}
	qrZoomCloseCard()
	qrZoomCloseCard()
	if borrows != 1 || gives != 1 {
		t.Fatalf("borrows %d, give-backs %d; want one of each", borrows, gives)
	}
	grant = false
	qrZoomOpenCard([][]bool{{true}}, "Pair")
	qrZoomCloseCard()
	if gives != 1 {
		t.Fatal("a card that borrowed nothing must give nothing back")
	}
}
