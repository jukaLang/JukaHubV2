package main

// Render-level checks for the shared component primitives: each one must put
// pixels where its contract says, so scenes can trust them without snapshot
// tests of their own.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// componentFrame renders a draw callback over a flat background and captures
// the result.
func componentFrame(t *testing.T, draw func(*sdl.Renderer)) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	draw(renderer)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	return f
}

func TestUIProgressBarFillsFraction(t *testing.T) {
	fill := sdl.Color{R: 200, G: 60, B: 60, A: 255}
	track := sdl.Color{R: 40, G: 44, B: 52, A: 255}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIProgressBar(r, sdl.Rect{X: 100, Y: 100, W: 200, H: 12}, 0.5, track, fill)
	})
	if got := f.at(150, 106); got.R < 180 || got.G > 90 {
		t.Errorf("progress fill pixel = %+v, want the fill colour", got)
	}
	if got := f.at(250, 106); got.R > 60 {
		t.Errorf("progress track pixel = %+v, want the track colour", got)
	}
	// Clamping: a fraction over 1 fills the whole bar.
	f = componentFrame(t, func(r *sdl.Renderer) {
		UIProgressBar(r, sdl.Rect{X: 100, Y: 100, W: 200, H: 12}, 4, track, fill)
	})
	if got := f.at(296, 106); got.R < 180 {
		t.Errorf("clamped progress left the right edge empty: %+v", got)
	}
}

func TestUICardDrawsSurface(t *testing.T) {
	fill := sdl.Color{R: 12, G: 90, B: 160, A: 255}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UICard(r, sdl.Rect{X: 60, Y: 60, W: 300, H: 180}, UICardStyle{
			Fill:   fill,
			Border: ColorBorder,
			Radius: RadiusLG,
		})
	})
	if got := f.at(210, 150); got.B < 140 || got.R > 40 {
		t.Errorf("card centre = %+v, want the fill colour", got)
	}
	if f.at(210, 60) == f.bg {
		t.Error("card border was not drawn along the top edge")
	}
}

func TestUIButtonDrawsSharedStates(t *testing.T) {
	config := &Config{}
	font := FontForRole(config, TypeBody)
	if font == nil {
		t.Skip("body font unavailable")
	}
	rect := sdl.Rect{X: 100, Y: 100, W: 200, H: 60}
	fill := sdl.Color{R: 20, G: 120, B: 220, A: 255}
	rest := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{})
	})
	if got := rest.at(110, 130); got.B < 180 || got.R > 60 {
		t.Errorf("resting button pixel = %+v, want the custom fill", got)
	}
	// The label must sit on top of the fill. contrastText picks dark ink on
	// this bright blue, so count pixels darker than both the fill and shadow.
	ink := 0
	for y := int32(110); y < 150; y++ {
		for x := int32(120); x < 280; x++ {
			if got := rest.at(x, y); got.R < 90 && got.G < 90 && got.B < 90 {
				ink++
			}
		}
	}
	if ink < 20 {
		t.Errorf("button label drew only %d ink pixels", ink)
	}

	focused := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{
			Variant:   UIButtonCustom,
			Fill:      fill,
			FocusFill: sdl.Color{R: 220, G: 60, B: 60, A: 255},
			Font:      font,
			Radius:    RadiusMD,
		}, UIButtonState{Focused: true})
	})
	if got := focused.at(110, 130); got.R < 180 || got.B > 120 {
		t.Errorf("focused button pixel = %+v, want the focus fill", got)
	}
	if focused.at(rect.X-FocusRing, 130) == focused.bg {
		t.Error("focused button did not draw its outer focus ring")
	}

	disabled := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{Disabled: true})
	})
	if disabled.at(110, 130) == rest.at(110, 130) {
		t.Error("disabled button looks identical to its resting state")
	}

	pressed := componentFrame(t, func(r *sdl.Renderer) {
		UIButton(r, config, rect, "Save", UIButtonStyle{Variant: UIButtonCustom, Fill: fill, Font: font, Radius: RadiusMD}, UIButtonState{Pressed: true})
	})
	if pressed.at(110, 130).B >= rest.at(110, 130).B {
		t.Error("pressed button was not darkened")
	}
}

func TestUIBadgeMeasuresAndDraws(t *testing.T) {
	config := &Config{}
	w, h := UIBadgeSize(config, "3")
	if w <= 0 || h <= 0 {
		t.Fatalf("badge size = %dx%d", w, h)
	}
	if emptyW, _ := UIBadgeSize(config, ""); emptyW != 0 {
		t.Errorf("empty badge width = %d, want 0", emptyW)
	}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIBadge(r, config, 100, 100, "3", sdl.Color{R: 220, G: 60, B: 60, A: 255}, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	})
	// The pill's middle is red regardless of the digit glyph on top.
	if got := f.at(100+4, 100+h/2); got.R < 150 {
		t.Errorf("badge pixel = %+v, want the badge background", got)
	}
}

func TestUIEmptyStateDrawsText(t *testing.T) {
	config := &Config{}
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIEmptyState(r, config, sdl.Rect{X: 100, Y: 100, W: 400, H: 220}, "(*)", "Nothing here", "Check back later.")
	})
	ink := 0
	for y := int32(100); y < 320; y++ {
		for x := int32(100); x < 500; x++ {
			if !f.isBG(x, y) {
				ink++
			}
		}
	}
	if ink < 40 {
		t.Errorf("empty-state block drew only %d non-background pixels", ink)
	}
}

// The header's contract is its return value: a caller lays content out at the y
// it gets back, so nothing may be painted below that line and the title must be
// inside the band.
func TestPageHeaderMeasuresWhatItDraws(t *testing.T) {
	config := &Config{}
	// The fonts have to exist before metrics mean anything: the band is
	// measured from them, so this initialises SDL_ttf first.
	newProbeFrame(t)
	glyph := func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
		fillRoundedRect(r, box.X, box.Y, box.W, box.H, 4, sdl.Color{R: 240, G: 240, B: 240, A: 255})
	}
	bands := []UIPageHeader{
		{X: 60, Y: 40, W: 620, Title: "Tools", Subtitle: "Diagnostics and transfers", Status: "Offline"},
		{X: 60, Y: 40, W: 620, Title: "Tools", Subtitle: "Diagnostics and transfers", Status: "Offline", Glyph: glyph},
		{X: 60, Y: 40, W: 620, Title: "JukaHub", TitleRole: "big", Subtitle: "v0.4.0", Status: "Your device. Your way.", Centered: true},
	}

	for i := range bands {
		h := bands[i]
		m := uiPageHeaderMetricsFor(config, h)
		if m.Height <= 0 {
			t.Fatalf("case %d: header height = %d", i, m.Height)
		}
		if h.Glyph != nil && m.WellBox <= m.TitleH {
			t.Fatalf("case %d: well %d is not taller than the title %d", i, m.WellBox, m.TitleH)
		}

		f := componentFrame(t, func(r *sdl.Renderer) {
			if got := UIPageHeaderDraw(r, config, h); got != h.Y+m.Height {
				t.Errorf("case %d: draw returned %d, metrics say %d", i, got, h.Y+m.Height)
			}
		})

		ink := 0
		for y := h.Y; y < h.Y+m.Height; y++ {
			for x := h.X; x < h.X+h.W; x++ {
				if !f.isBG(x, y) {
					ink++
				}
			}
		}
		if ink < 20 {
			t.Fatalf("case %d: the band drew only %d pixels", i, ink)
		}

		// Nothing may spill into the row the content is about to occupy. Two
		// pixels of slack cover anti-aliasing on the last text row.
		for y := h.Y + m.Height + 2; y < h.Y+m.Height+8; y++ {
			for x := h.X; x < h.X+h.W; x++ {
				if !f.isBG(x, y) {
					t.Fatalf("case %d: header ink at (%d,%d), below the height it reports", i, x, y)
				}
			}
		}
	}
}

// A tile's label lives inside the tile, and the focused tile is visibly
// different from a resting one. The heading and the overlay both draw through
// this, so the check is on the recipe itself.
func TestCategoryTileDrawsInsideItsRect(t *testing.T) {
	config := &Config{}
	rect := sdl.Rect{X: 100, Y: 100, W: 200, H: 120}
	glyph := func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
		fillCircle(r, box.X+box.W/2, box.Y+box.H/2, box.W/3, tint)
	}
	rest := componentFrame(t, func(r *sdl.Renderer) {
		UICategoryTileDraw(r, config, UICategoryTile{Rect: rect, Label: "Terminal", Glyph: glyph})
	})
	focused := componentFrame(t, func(r *sdl.Renderer) {
		UICategoryTileDraw(r, config, UICategoryTile{Rect: rect, Label: "Terminal", Glyph: glyph, Focused: true})
	})

	// The label is drawn under the well, inside the card.
	ink := 0
	for y := rect.Y + rect.H/2; y < rect.Y+rect.H-4; y++ {
		for x := rect.X + 4; x < rect.X+rect.W-4; x++ {
			if !rest.isBG(x, y) {
				ink++
			}
		}
	}
	if ink < 20 {
		t.Fatalf("tile drew only %d label pixels", ink)
	}
	if focused.at(rect.X+4, rect.Y+rect.H/2) == rest.at(rect.X+4, rect.Y+rect.H/2) {
		t.Error("focused tile looks identical to a resting one")
	}
	// The focus ring sits outside the card, which is where the grid's gaps are.
	if focused.at(rect.X-FocusRing, rect.Y+rect.H/2) == focused.bg {
		t.Error("focused tile did not draw its outer ring")
	}
	// A status dot has to be visible on the corner it claims.
	dotted := componentFrame(t, func(r *sdl.Renderer) {
		UICategoryTileDraw(r, config, UICategoryTile{
			Rect: rect, Label: "Terminal", Glyph: glyph,
			Status: sdl.Color{R: 240, G: 60, B: 60, A: 255},
		})
	})
	if got := dotted.at(rect.X+rect.W-13, rect.Y+13); got.R < 200 || got.G > 90 {
		t.Errorf("status dot pixel = %+v, want the dot colour", got)
	}
}

// The page header is one recipe now, so the two Tools screens and About must
// report the same band for the same title: a difference here is the drift the
// component exists to prevent.
func TestPageHeaderIsSharedAcrossScreens(t *testing.T) {
	config := &Config{}
	newProbeFrame(t)
	scene := SceneConfig{Name: "Tools"}
	overlay := toolsHeader(config, 24, 40, 800)
	page := menuTilesHeader(config, scene, 24, 40, 800)
	if overlay.Title != page.Title {
		t.Fatalf("overlay title %q, page title %q", overlay.Title, page.Title)
	}
	if overlay.Glyph == nil || page.Glyph == nil {
		t.Fatal("a header is missing its glyph well")
	}
	om, pm := uiPageHeaderMetricsFor(config, overlay), uiPageHeaderMetricsFor(config, page)
	if om.Height != pm.Height || om.WellBox != pm.WellBox {
		t.Fatalf("the two Tools headers measure differently: %+v vs %+v", om, pm)
	}

	// About uses the same component in its centred form: every line has to be
	// centred in the band it was given. The whole band is scanned, because the
	// status line is the widest of the three.
	centred := UIPageHeader{
		X: 100, Y: 40, W: 600, Title: "JukaHub", TitleRole: "big",
		Subtitle: "v0.4.0", SubtitleRole: "medium", Status: "Your device. Your way.", Centered: true,
	}
	cm := uiPageHeaderMetricsFor(config, centred)
	f := componentFrame(t, func(r *sdl.Renderer) {
		UIPageHeaderDraw(r, config, centred)
	})
	left, right := int32(-1), int32(-1)
	for x := int32(100); x < 700; x++ {
		for y := centred.Y; y < centred.Y+cm.Height; y++ {
			if !f.isBG(x, y) {
				if left < 0 {
					left = x
				}
				right = x
				break
			}
		}
	}
	if left < 0 {
		t.Fatal("the centred header drew nothing")
	}
	centre := (left + right) / 2
	if centre < 385 || centre > 415 {
		t.Fatalf("centred header ink spans %d..%d (centre %d), want it around 400", left, right, centre)
	}
}

func TestUILoadingSkeletonPulses(t *testing.T) {
	first := componentFrame(t, func(r *sdl.Renderer) {
		UILoadingSkeleton(r, sdl.Rect{X: 100, Y: 100, W: 300, H: 24}, 0)
	})
	second := componentFrame(t, func(r *sdl.Renderer) {
		UILoadingSkeleton(r, sdl.Rect{X: 100, Y: 100, W: 300, H: 24}, 0.25)
	})
	if first.at(250, 112) == second.at(250, 112) {
		t.Error("skeleton did not change with its phase")
	}
}
