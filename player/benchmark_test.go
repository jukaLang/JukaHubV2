package main

// benchmark_test.go — the Benchmark screen has one Run button, and that one
// press measures both phases.
//
// The regression this pins: Run and Random were separate buttons running
// separate tests. The sequential pass recorded the board and the random pass
// only ever ran if someone pressed the second button afterwards, so the screen
// routinely reported half of what it claims to measure — and the second press
// restarted the panel's text instead of adding to it.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// withBenchPhases swaps the two measurements and restores them afterwards.
func withBenchPhases(t *testing.T,
	seq func(context.Context) (benchOutcome, error),
	rnd func(context.Context) (benchOutcome, error)) {
	t.Helper()
	prevSeq, prevRnd := benchSequentialPhase, benchRandomPhase
	benchSequentialPhase, benchRandomPhase = seq, rnd
	t.Cleanup(func() { benchSequentialPhase, benchRandomPhase = prevSeq, prevRnd })
}

// inTempCwd runs the benchmark from a scratch directory so the board entry and
// the temporary files land there instead of in the checkout.
func inTempCwd(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func runBenchForTest(t *testing.T, config *Config) string {
	t.Helper()
	// Leaving a scene cancels its work generation in place (by design, so work
	// started mid-fade is still cancelled), so a benchmark that starts after a
	// test which simulated a transition would be handed a dead context and stop
	// at "Running benchmark…". This run gets its own generation.
	enterSceneWork("benchmark-test")
	t.Cleanup(waitForSceneWork)
	runBenchmark(config)
	waitForSceneWork()
	drainCustom(config)
	text, _ := config.Variables.Custom["benchmark_text"].(string)
	return text
}

// One Run press measures the sequential and the random phase, in that order,
// and reports all four numbers together.
func TestBenchmarkRunMeasuresBothPhases(t *testing.T) {
	inTempCwd(t)
	var order []string
	withBenchPhases(t,
		func(context.Context) (benchOutcome, error) {
			order = append(order, "sequential")
			return benchOutcome{write: 120.5, read: 210.25}, nil
		},
		func(context.Context) (benchOutcome, error) {
			order = append(order, "random")
			return benchOutcome{write: 3.5, read: 7.75}, nil
		})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	text := runBenchForTest(t, config)

	if len(order) != 2 || order[0] != "sequential" || order[1] != "random" {
		t.Fatalf("phases ran %v, want sequential then random", order)
	}
	for _, want := range []string{"Write: 120.5 MB/s", "Read:  210.2 MB/s", "Random write: 3.5 MB/s", "Random read:  7.8 MB/s"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report is missing %q:\n%s", want, text)
		}
	}
}

// A failing sequential phase must not hide the random one: the panel reports
// the failure and still measures the rest.
func TestBenchmarkRunKeepsGoingAfterAFailedPhase(t *testing.T) {
	inTempCwd(t)
	withBenchPhases(t,
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{}, errors.New("disk is full")
		},
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{write: 2, read: 4}, nil
		})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	text := runBenchForTest(t, config)

	if !strings.Contains(text, "Sequential: disk is full") {
		t.Errorf("the failed phase is not reported:\n%s", text)
	}
	if !strings.Contains(text, "Random write: 2.0 MB/s") {
		t.Errorf("the random phase did not run after the failure:\n%s", text)
	}
}

// Leaving the screen cancels the run: nothing is published over the next
// screen's panel.
func TestBenchmarkRunPublishesNothingAfterCancel(t *testing.T) {
	inTempCwd(t)
	withBenchPhases(t,
		func(ctx context.Context) (benchOutcome, error) {
			// The screen was left mid-run, which is what cancelSceneWork does.
			cancelSceneWork()
			return benchOutcome{}, ctx.Err()
		},
		func(context.Context) (benchOutcome, error) {
			t.Error("the random phase ran after the run was cancelled")
			return benchOutcome{}, nil
		})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	text := runBenchForTest(t, config)
	if strings.Contains(text, "Random") || strings.Contains(text, "Write:") {
		t.Fatalf("a cancelled run published results:\n%s", text)
	}
	// Reopen a usable task scope for whatever runs next.
	t.Cleanup(func() { enterSceneWork("Benchmark") })
}

// Opening the Benchmark scene starts the measurement on its own: the screen
// exists to report numbers, and arriving to an empty panel meant pressing Run to
// find out what the device does. The panel is the key, not the scene's name, so
// a renamed scene that keeps benchmark_text still measures.
func TestBenchmarkScreenMeasuresOnOpen(t *testing.T) {
	inTempCwd(t)
	withBenchPhases(t,
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{write: 100, read: 200}, nil
		},
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{write: 1, read: 2}, nil
		})

	config := &Config{
		Variables: Variables{Custom: map[string]interface{}{}},
		Scenes: []SceneConfig{{
			Name:     "Benchmark",
			Elements: []Element{{Type: "textlog", Variable: "benchmark_text"}},
		}},
	}
	enterSceneWork("Benchmark")
	t.Cleanup(waitForSceneWork)

	if !benchmarkAutoStartOnOpen(config, config.Scenes[0]) {
		t.Fatal("opening the Benchmark scene did not start a measurement")
	}
	waitForSceneWork()
	drainCustom(config)
	text, _ := config.Variables.Custom["benchmark_text"].(string)
	if !strings.Contains(text, "Write: 100.0 MB/s") || !strings.Contains(text, "Random read:  2.0 MB/s") {
		t.Fatalf("the auto-started run did not report both phases:\n%s", text)
	}

	// A scene without the panel must not measure: the key is the panel, and
	// every other screen would otherwise pay for a 100 MB pass on entry.
	if benchmarkAutoStartOnOpen(config, SceneConfig{Name: "Home"}) {
		t.Fatal("a scene without the benchmark panel started a measurement")
	}
}

// While a run is live, opening the screen again (or pressing Run) must not add a
// second 100 MB pass over the first: two runs would report each other's
// contention as the device's speed.
func TestBenchmarkRunDoesNotOverlap(t *testing.T) {
	inTempCwd(t)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	withBenchPhases(t,
		func(context.Context) (benchOutcome, error) {
			calls.Add(1)
			started <- struct{}{}
			<-release
			return benchOutcome{write: 1, read: 1}, nil
		},
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{write: 1, read: 1}, nil
		})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	enterSceneWork("benchmark-test")
	t.Cleanup(waitForSceneWork)

	runBenchmark(config)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the benchmark never started")
	}
	runBenchmark(config) // the live run's progress line is the answer
	close(release)
	waitForSceneWork()

	if got := calls.Load(); got != 1 {
		t.Fatalf("the sequential phase ran %d times, want 1: a second run started over the first", got)
	}
}

// The panel's live line names the measurement under way and how far it has got,
// and never reports more than 100%.
func TestBenchProgressTextNamesTheStep(t *testing.T) {
	got := benchProgressText(benchProgress{label: "Sequential write", done: 1, total: 4})
	if !strings.Contains(got, "Sequential write: 25%") {
		t.Fatalf("progress text = %q, want the phase and its percentage", got)
	}
	if got := benchProgressText(benchProgress{label: "Random read", done: 9, total: 8}); !strings.Contains(got, "Random read: 100%") {
		t.Fatalf("progress text = %q, want it clamped to 100%%", got)
	}
}

// The shipped configs offer a single button, so no screen can ask for half a
// measurement again.
func TestBenchmarkScreenHasOneRunButton(t *testing.T) {
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		idx := sceneIndexExact(&cfg, "Benchmark")
		if idx < 0 {
			t.Fatalf("%s does not ship the Benchmark scene", path)
		}
		var triggers []string
		for _, el := range cfg.Scenes[idx].Elements {
			if el.Type == "button" {
				triggers = append(triggers, el.Trigger)
			}
		}
		if len(triggers) != 1 || triggers[0] != "benchmark_run" {
			t.Errorf("%s: benchmark buttons = %v, want the single %q", path, triggers, "benchmark_run")
		}
		// The log panel still has to be there: it is where the numbers land.
		var hasLog bool
		for _, el := range cfg.Scenes[idx].Elements {
			if el.Type == "textlog" && el.Variable == "benchmark_text" {
				hasLog = true
			}
		}
		if !hasLog {
			t.Errorf("%s: the Benchmark scene has no benchmark_text panel", path)
		}
	}
}

// shippedBenchmarkRun loads a shipped config and returns the Benchmark screen's
// Run button, failing when the scene or the button is missing.
func shippedBenchmarkRun(t *testing.T, path string) Element {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	idx := sceneIndexExact(&cfg, "Benchmark")
	if idx < 0 {
		t.Fatalf("%s does not ship the Benchmark scene", path)
	}
	for _, el := range cfg.Scenes[idx].Elements {
		if el.Type == "button" && el.Trigger == "benchmark_run" {
			return el
		}
	}
	t.Fatalf("%s: the Benchmark scene has no benchmark_run button", path)
	return Element{}
}

// While a measurement runs, the screen's one Run button says so and is drawn
// disabled, and a second press is refused outright — no activate tap, no second
// pass. The screen starts measuring as it opens, so without this the only
// control on it looked live for the whole of a slow card's 100 MB pass.
func TestBenchmarkRunButtonIsBusyWhileMeasuring(t *testing.T) {
	runBtn := shippedBenchmarkRun(t, "jukaconfig.json")
	if label, busy := benchmarkButtonFace(runBtn); busy || label != runBtn.Text {
		t.Fatalf("idle Run button = %q busy=%v, want its own label %q and no busy mark", label, busy, runBtn.Text)
	}

	// The run stays in the checkout, so the render probes below find the shipped
	// font: the button's label is part of what this checks. Neither stub touches
	// the disk, and the sequential phase reports an error rather than numbers, so
	// nothing is recorded in the board and no scratch file is left behind.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	releasePhase := func() {
		if !released {
			released = true
			close(release)
		}
	}
	t.Cleanup(releasePhase)
	var calls atomic.Int32
	withBenchPhases(t,
		func(context.Context) (benchOutcome, error) {
			if calls.Add(1) == 1 {
				started <- struct{}{}
			}
			<-release
			return benchOutcome{}, errors.New("the button probe does not measure the disk")
		},
		func(context.Context) (benchOutcome, error) {
			return benchOutcome{}, errors.New("the button probe does not measure the disk")
		})

	dev := &fakeHaptics{}
	installFakeHaptics(t, dev)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	enterSceneWork("benchmark-test")
	t.Cleanup(func() {
		releasePhase()
		waitForSceneWork()
	})

	// Drawn as it arrives: the label the user pressed, at the button's own size.
	idle := componentFrame(t, func(r *sdl.Renderer) {
		renderButtonElement(r, config, runBtn, 0, false, false, 0, false)
	})

	// An idle press is a press: it reaches the trigger and taps the motor.
	handleTrigger(nil, config, runBtn)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pressing Run while idle did not start a measurement")
	}
	if len(dev.pulses) != 1 {
		t.Fatalf("an idle press produced %d pulses, want 1", len(dev.pulses))
	}

	label, busy := benchmarkButtonFace(runBtn)
	if !busy {
		t.Fatal("the Run button is not busy while a measurement is running")
	}
	if label != benchRunLabel {
		t.Fatalf("busy Run button reads %q, want %q", label, benchRunLabel)
	}

	busyFrame := componentFrame(t, func(r *sdl.Renderer) {
		renderButtonElement(r, config, runBtn, 0, false, false, 0, false)
	})
	pressedFrame := componentFrame(t, func(r *sdl.Renderer) {
		renderButtonElement(r, config, runBtn, 0, true, false, 0, true)
	})

	labelFont, _ := getCachedFont(config, runBtn.Font)
	btnW, btnH := buttonHitSize(runBtn, labelFont)
	rect := sdl.Rect{X: runBtn.X, Y: runBtn.Y, W: btnW, H: btnH}
	brightness := func(f *probeFrame) float64 {
		var sum, n float64
		for y := rect.Y + 6; y < rect.Y+rect.H-6; y++ {
			for x := rect.X + 6; x < rect.X+rect.W-6; x++ {
				c := f.at(x, y)
				sum += float64(c.R) + float64(c.G) + float64(c.B)
				n++
			}
		}
		return sum / n
	}
	if idleBright, busyBright := brightness(idle), brightness(busyFrame); busyBright >= idleBright {
		t.Errorf("busy Run button is as bright as the idle one (%.1f vs %.1f): it still looks pressable", busyBright, idleBright)
	}
	// A press that landed as the run began must not leave the button looking
	// pressed: the busy face is the same whichever way it is drawn.
	for y := rect.Y + 2; y < rect.Y+rect.H-2; y++ {
		for x := rect.X + 2; x < rect.X+rect.W-2; x++ {
			if pressedFrame.at(x, y) != busyFrame.at(x, y) {
				t.Fatalf("a press moved the busy button at (%d,%d); it must not look pressable", x, y)
			}
		}
	}

	// And the press itself is refused at the control: no activate tap, no
	// second measurement.
	handleTrigger(nil, config, runBtn)
	if len(dev.pulses) != 1 {
		t.Errorf("pressing the busy Run button tapped the motor %d times, want it refused", len(dev.pulses)-1)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("pressing the busy Run button started %d measurements, want 1", got)
	}
}
