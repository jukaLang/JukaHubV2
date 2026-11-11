package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestMagnifierGeometryFollowsFocus(t *testing.T) {
	oldA11y, oldW, oldH := a11y, screenWidth, screenHeight
	defer func() { a11y, screenWidth, screenHeight = oldA11y, oldW, oldH; magnifierReset() }()
	screenWidth, screenHeight = 1280, 720
	resetA11yForTest()

	focus := sdl.Rect{X: 400, Y: 200, W: 400, H: 60}
	a11y.magnifierZoom = 2
	src := magnifierSourceRect(focus)
	if src.W <= 0 || src.H <= 0 {
		t.Fatalf("no source for a normal focus: %+v", src)
	}
	if src.X+src.W/2 != focus.X+focus.W/2 || src.Y+src.H/2 != focus.Y+focus.H/2 {
		t.Fatalf("source %+v is not centred on the focus %+v", src, focus)
	}
	lens, ok := magnifierLensRect(focus, src)
	if !ok {
		t.Fatal("no lens beside a normal focus")
	}
	if lens.Y < src.Y+src.H {
		t.Fatalf("lens %+v is not clear of its source %+v", lens, src)
	}
	if lens.Y+lens.H > screenHeight || lens.X+lens.W > screenWidth {
		t.Fatalf("lens %+v runs off the screen", lens)
	}
	if magnifierOverlap(lens, src) {
		t.Fatalf("lens %+v covers the source it magnifies", lens)
	}

	// 3× divides the same lens into a smaller region: the lens itself stays
	// put, so the control's text gets bigger.
	a11y.magnifierZoom = 3
	src3 := magnifierSourceRect(focus)
	if src3.W >= src.W || src3.H >= src.H {
		t.Fatalf("3× source %+v is not smaller than 2× %+v", src3, src)
	}
	if lens3, _ := magnifierLensRect(focus, src3); lens3.W != lens.W || lens3.H != lens.H {
		t.Fatalf("lens changed size with the zoom: %+v vs %+v", lens3, lens)
	}

	// A control on the bottom edge puts the lens above it instead of off the
	// bottom of the screen.
	a11y.magnifierZoom = 2
	low := sdl.Rect{X: 400, Y: 640, W: 400, H: 50}
	lowSrc := magnifierSourceRect(low)
	lowLens, ok := magnifierLensRect(low, lowSrc)
	if !ok {
		t.Fatal("no lens for a focus on the bottom edge")
	}
	if lowLens.Y+lowLens.H > low.Y {
		t.Fatalf("lens %+v is not above the bottom focus %+v", lowLens, low)
	}
	if magnifierOverlap(lowLens, lowSrc) {
		t.Fatalf("bottom lens %+v covers its source %+v", lowLens, lowSrc)
	}

	// A focus that leaves no room either side has no lens at all, rather than
	// one that covers the picture it is copying.
	tall := sdl.Rect{X: 0, Y: 40, W: 1280, H: 660}
	if _, ok := magnifierLensRect(tall, magnifierSourceRect(tall)); ok {
		t.Fatal("a full-height focus still got a lens")
	}

	// Near the right edge the lens is kept on screen.
	right := sdl.Rect{X: 1000, Y: 200, W: 260, H: 60}
	rightLens, ok := magnifierLensRect(right, magnifierSourceRect(right))
	if !ok || rightLens.X < 0 || rightLens.X+rightLens.W > screenWidth {
		t.Fatalf("lens %+v is not on screen (ok=%v)", rightLens, ok)
	}
}

func TestMagnifierSettingsPersistAndReset(t *testing.T) {
	old := a11y
	defer func() { a11y = old; magnifierReset() }()
	resetA11yForTest()

	cfg := &Config{}
	if !a11ySetMagnifier(true) || !a11y.magnifier {
		t.Fatal("the magnifier switch did not turn on")
	}
	if !a11ySetMagnifierZoom(3) || a11y.magnifierZoom != 3 {
		t.Fatalf("zoom = %d", a11y.magnifierZoom)
	}
	a11yStore(cfg)
	if cfg.Variables.Custom[a11yKeyMagnifier] != true || cfg.Variables.Custom[a11yKeyMagnifierZoom] != 3 {
		t.Fatalf("magnifier not stored: %v", cfg.Variables.Custom)
	}

	a11y = a11ySettings{magnifierZoom: 2}
	a11yLoad(cfg)
	if !a11y.magnifier || a11y.magnifierZoom != 3 {
		t.Fatalf("magnifier did not load: on=%v zoom=%d", a11y.magnifier, a11y.magnifierZoom)
	}

	// A hand-edited factor outside 2×/3× is clamped to the strongest lens.
	cfg.Variables.Custom[a11yKeyMagnifierZoom] = 7
	a11yLoad(cfg)
	if a11y.magnifierZoom != 3 {
		t.Fatalf("zoom 7 loaded as %d, want 3", a11y.magnifierZoom)
	}

	a11yResetDefaults(cfg)
	if a11y.magnifier || a11y.magnifierZoom != 2 {
		t.Fatalf("reset left the magnifier on=%v zoom=%d", a11y.magnifier, a11y.magnifierZoom)
	}
	if cfg.Variables.Custom[a11yKeyMagnifier] != false {
		t.Fatalf("reset not stored: %v", cfg.Variables.Custom[a11yKeyMagnifier])
	}
}

func TestMagnifierQuickSettingsToggleAndRows(t *testing.T) {
	oldA11y, oldQS, oldConfig := a11y, qs, appConfig
	defer func() {
		a11y, qs, appConfig = oldA11y, oldQS, oldConfig
		a11yChordReset()
		magnifierReset()
	}()
	resetA11yForTest()
	qsInit()
	appConfig = &Config{}

	idx := -1
	for i := range qs.toggles {
		if qs.toggles[i].Action == qsActionMagnifier {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Quick Settings has no Magnifier switch")
	}
	if !qs.toggles[idx].Available {
		t.Fatal("the Magnifier switch is unavailable")
	}
	if err := qsApplyToggle(&qs.toggles[idx], true); err != nil {
		t.Fatalf("turning the magnifier on: %v", err)
	}
	if !a11y.magnifier {
		t.Fatal("the Quick Settings switch did not reach the lens")
	}
	if appConfig.Variables.Custom[a11yKeyMagnifier] != true {
		t.Fatalf("the switch did not store its value: %v", appConfig.Variables.Custom)
	}
	if err := qsApplyToggle(&qs.toggles[idx], false); err != nil {
		t.Fatalf("turning the magnifier off: %v", err)
	}
	if a11y.magnifier {
		t.Fatal("the Quick Settings switch did not turn the lens off")
	}

	rows := a11ySettingsSectionRows(t, appConfig)
	find := func(id string) setRow {
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("no %q row in Settings → Accessibility", id)
		return setRow{}
	}
	find("magnifier").Set(appConfig, true)
	if !a11y.magnifier {
		t.Fatal("the Settings switch did not reach the lens")
	}
	find("magnifierZoom").Choose(appConfig, "3")
	if a11y.magnifierZoom != 3 {
		t.Fatalf("zoom = %d, want 3", a11y.magnifierZoom)
	}
}
