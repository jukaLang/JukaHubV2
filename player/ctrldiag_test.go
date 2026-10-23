package main

// Tests for the controller test panel: the drift verdict, press bookkeeping,
// the hold-to-close escape hatch, the Power tools entry and the rendered layout.
// The panel reads a replaceable clock, so a 900ms rest window is a variable
// change rather than a sleep.

import (
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// withDiagClock swaps the panel clock and returns a pointer the test moves.
func withDiagClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	prev := ctrldiagClock
	now := start
	ctrldiagClock = func() time.Time { return now }
	t.Cleanup(func() { ctrldiagClock = prev })
	return &now
}

// withDiagPanel restores the panel globals after a test.
func withDiagPanel(t *testing.T) {
	t.Helper()
	prev := ctrldiag
	prevRect := ctrldiagPanelRect
	t.Cleanup(func() {
		ctrldiag = prev
		ctrldiagPanelRect = prevRect
	})
}

func TestCtrldiagDriftVerdict(t *testing.T) {
	withDiagPanel(t)
	now := withDiagClock(t, time.Unix(1000, 0))

	ctrldiag.open = true
	ctrldiagReset()

	ctrldiagAxisEvent(0, 0)
	*now = now.Add(2 * time.Second)
	if msg, bad := ctrldiagAxisProblem(0, *now); msg != "centred" || bad {
		t.Fatalf("centred axis = %q, %v", msg, bad)
	}

	// 4000/32767 ≈ 0.12: real drift, but still inside the app's deadzone.
	ctrldiagAxisEvent(0, 4000)
	if msg, bad := ctrldiagAxisProblem(0, *now); msg != "moving" || bad {
		t.Fatalf("freshly moved axis = %q, %v; a moving stick is not a fault", msg, bad)
	}
	*now = now.Add(2 * time.Second)
	msg, bad := ctrldiagAxisProblem(0, *now)
	if !bad || !strings.Contains(msg, "inside the deadzone") {
		t.Fatalf("small drift = %q, %v; want a warned inside-deadzone read", msg, bad)
	}

	// 20000/32767 ≈ 0.61: past the deadzone, so the cursor really moves.
	ctrldiagAxisEvent(0, 20000)
	*now = now.Add(2 * time.Second)
	msg, bad = ctrldiagAxisProblem(0, *now)
	if !bad || !strings.Contains(msg, "past the deadzone") {
		t.Fatalf("large drift = %q, %v; want a warned past-deadzone read", msg, bad)
	}

	if got := ctrldiagProblems(); len(got) != 1 || !strings.Contains(got[0], "Left X") {
		t.Fatalf("problems = %v, want one Left X entry", got)
	}
	if summary := ctrldiagSummary(); !strings.Contains(summary, "Drift") {
		t.Fatalf("summary = %q, want it to name the drift", summary)
	}
}

func TestCtrldiagUnseenAxis(t *testing.T) {
	withDiagPanel(t)
	now := withDiagClock(t, time.Unix(1000, 0))
	ctrldiagReset()

	// No sample yet: the panel must say so instead of calling it centred.
	if msg, bad := ctrldiagAxisProblem(2, *now); msg != "no data" || bad {
		t.Fatalf("unseen axis = %q, %v", msg, bad)
	}
	if got := ctrldiagProblems(); len(got) != 0 {
		t.Fatalf("problems on a fresh panel = %v, want none", got)
	}
}

func TestCtrldiagButtonPressAndRelease(t *testing.T) {
	withDiagPanel(t)
	now := withDiagClock(t, time.Unix(1000, 0))
	ctrldiagReset()

	press := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	release := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: sdl.CONTROLLER_BUTTON_A}

	ctrldiagButtonEvent(press)
	b := ctrldiag.buttons[sdl.CONTROLLER_BUTTON_A]
	if !b.Held || b.Presses != 1 {
		t.Fatalf("after press: held=%v presses=%d", b.Held, b.Presses)
	}
	if got := ctrldiagHeldCount(); got != 1 {
		t.Fatalf("held count = %d, want 1", got)
	}

	// A repeated down event while held is not a second press.
	ctrldiagButtonEvent(press)
	if b.Presses != 1 {
		t.Fatalf("double down counted as %d presses", b.Presses)
	}

	*now = now.Add(120 * time.Millisecond)
	ctrldiagButtonEvent(release)
	if b.Held {
		t.Fatal("button still held after release")
	}
	if b.LastHeld != 120*time.Millisecond {
		t.Fatalf("last held = %v, want 120ms", b.LastHeld)
	}
	if got := ctrldiagHeldCount(); got != 0 {
		t.Fatalf("held count after release = %d, want 0", got)
	}
}

// A pad the panel has never seen still records, so extra paddles are not lost.
func TestCtrldiagUnknownButtonIsRecorded(t *testing.T) {
	withDiagPanel(t)
	withDiagClock(t, time.Unix(1000, 0))
	ctrldiagReset()

	const paddle = 200
	ctrldiagButtonEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: paddle})
	b := ctrldiag.buttons[paddle]
	if b == nil || b.Presses != 1 {
		t.Fatalf("unknown button not recorded: %+v", b)
	}
	if label := ctrldiagButtonLabel(paddle); label == "" {
		t.Fatal("unknown button has no label")
	}
}

func TestCtrldiagHoldSelectCloses(t *testing.T) {
	withDiagPanel(t)
	now := withDiagClock(t, time.Unix(1000, 0))
	ctrldiagReset()

	if ctrldiagBackHeld(*now) {
		t.Fatal("SELECT counts as held before it is pressed")
	}
	ctrldiagButtonEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_BACK})
	if ctrldiagBackHeld(*now) {
		t.Fatal("a fresh SELECT press already closes the panel")
	}
	*now = now.Add(diagBackHold + 100*time.Millisecond)
	if !ctrldiagBackHeld(*now) {
		t.Fatal("holding SELECT past the threshold did not arm the close")
	}
	ctrldiagButtonEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: sdl.CONTROLLER_BUTTON_BACK})
	if ctrldiagBackHeld(*now) {
		t.Fatal("releasing SELECT still counts as a hold")
	}
}

func TestCtrldiagMenuRowOpensPanel(t *testing.T) {
	prevOpen, prevPage, prevCursor := powerTools.open, powerTools.page, powerTools.cursor
	withDiagPanel(t)
	t.Cleanup(func() {
		powerTools.open, powerTools.page, powerTools.cursor = prevOpen, prevPage, prevCursor
	})

	rows := powerToolRows(&Config{})
	idx := -1
	for i, row := range rows {
		if row.action == powerActionOpenCtrlDiag {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no Controller test row in the Power tools menu: %+v", rows)
	}

	powerTools.open = true
	powerTools.page = powerPageMenu
	powerTools.cursor = idx
	ctrldiag.open = false
	powerActivate(&Config{})

	if !ctrldiag.open {
		t.Fatal("activating the row did not open the Controller test panel")
	}
	if powerTools.open {
		t.Fatal("Power tools stayed open behind the panel")
	}
}

// The panel keeps the scene from reacting to the pad while it is up.
func TestCtrldiagBlocksModals(t *testing.T) {
	withDiagPanel(t)
	ctrldiag.open = true
	if !modalPanelBlocksShoulders() {
		t.Fatal("shoulders leaked through the controller test")
	}
	if !ambientEntryBlocked(&Config{}) {
		t.Fatal("the ambient screen could cover the controller test")
	}
}

func renderCtrldiagProbe(t *testing.T, config *Config) (*probeFrame, sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}

	prev := ctrldiag
	prevW, prevH := screenWidth, screenHeight
	prevRect := ctrldiagPanelRect
	t.Cleanup(func() {
		ctrldiag = prev
		screenWidth, screenHeight = prevW, prevH
		ctrldiagPanelRect = prevRect
	})

	now := withDiagClock(t, time.Unix(1000, 0))
	screenWidth, screenHeight = probeW, probeH
	ctrldiag.open = true
	ctrldiagReset()
	// A drifting left stick and one pressed button, so the warning path and the
	// held-chip path are both drawn.
	ctrldiagAxisEvent(0, 20000)
	ctrldiagAxisEvent(1, 0)
	ctrldiagButtonEvent(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A})
	*now = now.Add(2 * time.Second)

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderCtrldiagPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	return f, ctrldiagPanelRect
}

func TestCtrldiagPanelRenderLayout(t *testing.T) {
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	config := &Config{}
	f, panel := renderCtrldiagProbe(t, config)

	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("panel not laid out: %+v", panel)
	}
	if panel.X < 0 || panel.X+panel.W > probeW || panel.Y < 0 || panel.Y+panel.H > probeH {
		t.Fatalf("panel out of bounds: %+v", panel)
	}
	if !ctrldiag.open {
		t.Fatal("the panel closed itself while rendering")
	}

	// The drift warning is painted, not just computed: the subtitle and the
	// left-stick dot both use the warn colour.
	if n := f.countColor(ColorToastWarn, panel.X, panel.Y, panel.X+panel.W, panel.Y+panel.H); n < 20 {
		t.Fatalf("drift warning not drawn (only %d warn-coloured pixels)", n)
	}

	// The reset chip sits inside the panel and near its bottom edge.
	if ctrldiagResetRect.W <= 0 ||
		ctrldiagResetRect.X < panel.X || ctrldiagResetRect.X+ctrldiagResetRect.W > panel.X+panel.W ||
		ctrldiagResetRect.Y < panel.Y || ctrldiagResetRect.Y+ctrldiagResetRect.H > panel.Y+panel.H {
		t.Fatalf("reset chip %+v escapes panel %+v", ctrldiagResetRect, panel)
	}
}
