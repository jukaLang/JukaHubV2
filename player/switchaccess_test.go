package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// switchProbeScene is a scene with three controls a scan can walk.
func switchProbeScene(name string) SceneConfig {
	return SceneConfig{Name: name, Elements: []Element{
		{Type: "button", Text: "First", X: 100, Y: 200},
		{Type: "button", Text: "Second", X: 500, Y: 200},
		{Type: "button", Text: "Third", X: 900, Y: 200},
	}}
}

func TestScanStepReadsTheFocusGraphInOrder(t *testing.T) {
	fe := NewFocusEngine()
	scene := switchProbeScene("ScanProbe")
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	first, ok := fe.Current()
	if !ok {
		t.Fatalf("no focus in %+v", fe.BuildGraph(scene))
	}
	n := fe.NodeCount()
	if n < len(scene.Elements) {
		t.Fatalf("the graph has %d nodes for %d controls", n, len(scene.Elements))
	}
	// Node order is the scene's declared order, so one full turn visits every
	// node once and lands back on the control the scan started from.
	visited := map[int]bool{first.Index: true}
	for i := 1; i < n; i++ {
		idx := fe.ScanStep(1)
		if idx < 0 {
			t.Fatalf("step %d lost the focus", i)
		}
		visited[idx] = true
	}
	if len(visited) != n {
		t.Fatalf("the scan visited %d of %d nodes", len(visited), n)
	}
	if idx := fe.ScanStep(1); idx != first.Index {
		t.Fatalf("a full turn ended at %d, not %d", idx, first.Index)
	}
	// Backwards steps wrap through the start, never off the graph.
	if idx := fe.ScanStep(-1); idx < 0 {
		t.Fatal("a backwards step lost the focus")
	}
	// An engine with no graph scans nowhere instead of panicking.
	if got := NewFocusEngine().ScanStep(1); got != -1 {
		t.Fatalf("an empty engine stepped to %d", got)
	}
}

func TestSwitchAccessSettingsPersistAndReset(t *testing.T) {
	old := a11y
	defer func() { a11y = old; switchScanReset() }()
	resetA11yForTest()

	cfg := &Config{}
	if !a11ySetSwitchMode(a11ySwitchTwo) || a11ySwitchModeValue(a11y.switchMode) != a11ySwitchTwo {
		t.Fatal("the switch access setting did not take")
	}
	if !a11ySetSwitchInterval(1600) || a11y.switchIntervalMs != 1600 {
		t.Fatalf("interval = %d", a11y.switchIntervalMs)
	}
	a11yStore(cfg)
	if got, _ := customString(cfg.Variables.Custom, a11yKeySwitchMode); got != a11ySwitchTwo {
		t.Fatalf("stored mode = %q", got)
	}
	if got := a11yInt(cfg, a11yKeySwitchInterval, 0, 0, 10000); got != 1600 {
		t.Fatalf("stored interval = %d", got)
	}

	a11y = a11ySettings{switchMode: a11ySwitchOff, switchIntervalMs: a11ySwitchDefaultIntervalMS}
	a11yLoad(cfg)
	if a11ySwitchModeValue(a11y.switchMode) != a11ySwitchTwo || a11y.switchIntervalMs != 1600 {
		t.Fatalf("loaded %q/%d", a11y.switchMode, a11y.switchIntervalMs)
	}

	// An unknown stored mode falls back to off rather than leaving the pad in
	// a scanning state nothing can leave.
	cfg.Variables.Custom[a11yKeySwitchMode] = "bogus"
	a11yLoad(cfg)
	if a11ySwitchModeValue(a11y.switchMode) != a11ySwitchOff {
		t.Fatalf("an unknown mode stayed %q", a11y.switchMode)
	}

	a11yResetDefaults(cfg)
	if a11y.switchMode != a11ySwitchOff || a11y.switchIntervalMs != a11ySwitchDefaultIntervalMS {
		t.Fatalf("reset left %q/%d", a11y.switchMode, a11y.switchIntervalMs)
	}
}

func TestSwitchScanTickHonoursTheDwell(t *testing.T) {
	old := a11y
	defer func() { a11y = old; switchScanReset(); focusEngine.Clear() }()
	resetA11yForTest()

	scene := switchProbeScene("ScanTickProbe")
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if !a11ySetSwitchMode(a11ySwitchOne) {
		t.Fatal("switch access did not turn on")
	}
	if !a11ySetSwitchInterval(1000) {
		t.Fatal("the scan interval did not take")
	}
	switchScanReset()
	if switchScanTickAt(nil, 100) {
		t.Fatal("the scan moved on its arming tick")
	}
	start, _ := focusEngine.Current()
	if switchScanTickAt(nil, 900) {
		t.Fatal("the scan moved inside the dwell")
	}
	if !switchScanTickAt(nil, 1100) {
		t.Fatal("the scan did not move once the dwell elapsed")
	}
	next, _ := focusEngine.Current()
	if next.Index == start.Index {
		t.Fatal("the focus did not move")
	}
	if !a11ySetSwitchMode(a11ySwitchOff) {
		t.Fatal("switch access did not turn off")
	}
	if switchScanTickAt(nil, 10000) {
		t.Fatal("the scan moved while the mode was off")
	}
}

func TestSwitchAccessRowsAndPadPress(t *testing.T) {
	old := a11y
	oldScene, oldPending := currentSceneIndex, pendingSceneIndex
	defer func() {
		a11y = old
		currentSceneIndex, pendingSceneIndex = oldScene, oldPending
		switchScanReset()
		focusEngine.Clear()
	}()
	resetA11yForTest()

	target := SceneConfig{Name: "ScanTarget"}
	cfg := &Config{Scenes: []SceneConfig{{Name: "ScanPadProbe"}, target}}
	rows := a11ySettingsRows()
	find := func(id string) setRow {
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("the Accessibility page has no %q row", id)
		return setRow{}
	}

	mode := find("switchAccess")
	if mode.Kind != setKindChoice {
		t.Fatalf("switch access row kind = %v", mode.Kind)
	}
	if got := mode.Current(cfg); got != 0 {
		t.Fatalf("switch access starts at option %d", got)
	}
	mode.Choose(cfg, a11ySwitchTwo)
	if a11ySwitchModeValue(a11y.switchMode) != a11ySwitchTwo {
		t.Fatalf("the row left the mode at %q", a11y.switchMode)
	}
	interval := find("switchInterval")
	if interval.Disabled(cfg) {
		t.Fatal("the interval row is disabled while scanning is on")
	}
	interval.Choose(cfg, "1600")
	if a11y.switchIntervalMs != 1600 {
		t.Fatalf("the row left the interval at %d", a11y.switchIntervalMs)
	}

	// Two buttons: B steps the scan, and it is taken from the screen's own B.
	currentSceneIndex = 0
	// Both controls go to the same place, so whichever one the scan pressed
	// proves the activation whichever step landed on it.
	scene := SceneConfig{Name: "ScanPadProbe", Elements: []Element{
		{Type: "button", Text: "Go", Trigger: "change_scene:ScanTarget"},
		{Type: "button", Text: "Go too", Trigger: "change_scene:ScanTarget"},
	}}
	cfg.Scenes[0] = scene
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	before, _ := focusEngine.Current()
	b := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}
	if !switchScanPadPress(nil, b, cfg) {
		t.Fatal("two-button mode did not take B")
	}
	after, _ := focusEngine.Current()
	if after.Index == before.Index {
		t.Fatal("B did not step the scan")
	}

	// One button: B keeps its back meaning, so only A is the scan's.
	if !a11ySetSwitchMode(a11ySwitchOne) {
		t.Fatal("switch access did not switch to one button")
	}
	if switchScanPadPress(nil, b, cfg) {
		t.Fatal("one-button mode took B from the screen")
	}

	// A presses the highlighted control through the real activation path.
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !switchScanPadPress(nil, a, cfg) {
		t.Fatal("A did not press the highlighted control")
	}
	// A scene change fades: the press stages the target for the transition.
	if pendingSceneIndex != 1 {
		t.Fatalf("the pressed control staged scene %d, not %d (%q)", pendingSceneIndex, 1, target.Name)
	}
	if switchScanPadPress(nil, a, nil) {
		t.Fatal("a nil config was scanned")
	}
}
