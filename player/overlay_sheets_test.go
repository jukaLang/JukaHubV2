package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// TestSettingsAndChatSheetsStayAboveFooter draws the three modal sheets and
// checks the geometry they leave for taps. A sheet's own card, its field rows,
// buttons and scroll viewport must all end above the footer band; a row that
// ran past the card would be unreachable even though the card itself fit.
func TestSettingsAndChatSheetsStayAboveFooter(t *testing.T) {
	legibleTestInitFonts(t)
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		t.Fatalf("initialize SDL video: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() {
		screenWidth, screenHeight = oldW, oldH
		clockSheet.open, tzPicker.open, chatFileSheet.open = false, false, false
	})

	wide, okWide := deviceScreenProfileFor(DeviceTrimuiSmartPro)
	compact, okCompact := deviceScreenProfileFor(DeviceTrimuiBrick)
	if !okWide || !okCompact {
		t.Fatal("the supported device profiles are missing")
	}
	cfg := &Config{}
	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", wide.CanvasW, wide.CanvasH},
		{"compact", compact.CanvasW, compact.CanvasH},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			surface, err := sdl.CreateRGBSurface(0, c.width, c.height, 32, 0, 0, 0, 0)
			if err != nil {
				t.Fatalf("create test surface: %v", err)
			}
			renderer, err := sdl.CreateSoftwareRenderer(surface)
			if err != nil {
				surface.Free()
				t.Fatalf("create software renderer: %v", err)
			}
			defer func() {
				renderer.Destroy()
				surface.Free()
			}()
			footer := sceneFooterBand().Y
			check := func(what string, r sdl.Rect) {
				if r.W <= 0 || r.H <= 0 {
					t.Fatalf("%s has no rect: %+v", what, r)
				}
				if r.X < 0 || r.X+r.W > c.width || r.Y < 0 || r.Y+r.H > footer {
					t.Fatalf("%s = %+v, outside the %dx%d canvas above the footer at %d", what, r, c.width, c.height, footer)
				}
			}

			clockSheetOpen()
			clockSheetDraw(renderer, cfg)
			check("clock sheet", clockSheet.card)
			for i, hit := range clockSheet.hits {
				if hit.row.W <= 0 || hit.row.H <= 0 {
					t.Fatalf("clock field %d has no hit row", i)
				}
				check("clock field", hit.row)
				if hit.row.Y+hit.row.H > clockSheet.card.Y+clockSheet.card.H {
					t.Fatalf("clock field ends at %d, past the sheet at %d", hit.row.Y+hit.row.H, clockSheet.card.Y+clockSheet.card.H)
				}
			}
			clockSheet.open = false

			tzPickerOpen(cfg)
			tzPickerDraw(renderer, cfg)
			check("time zone sheet", tzPicker.card)
			check("time zone search field", tzPicker.field)
			check("time zone viewport", tzPicker.view)
			if tzPicker.view.Y+tzPicker.view.H > tzPicker.card.Y+tzPicker.card.H {
				t.Fatalf("time zone viewport ends at %d, past the sheet at %d", tzPicker.view.Y+tzPicker.view.H, tzPicker.card.Y+tzPicker.card.H)
			}
			if len(tzPicker.rects) == 0 {
				t.Fatal("time zone sheet drew no rows")
			}
			tzPicker.open = false

			setSheetOpen(&chatFileSheet, "3 files", "From jukalang", []setOption{
				{Value: "0", Label: "clip-2026-11-15.mp4", Note: "12.4 MB · local · 0:18"},
				{Value: "1", Label: "jukahub-debug-2026-11-15.log", Note: "1.2 MB · local"},
				{Value: "2", Label: "screenshot.png", Note: "840 KB · local"},
				{Value: "3", Label: "settings-backup.json", Note: "24 KB · local"},
				{Value: "4", Label: "voice-memo.ogg", Note: "2.1 MB · local · 0:34"},
				{Value: "5", Label: "another-recording-with-a-long-name.mp4", Note: "18.9 MB · local · 1:42"},
			}, "", nil)
			setSheetDraw(renderer, cfg, &chatFileSheet)
			check("chat file sheet", chatFileSheet.card)
			check("chat file viewport", chatFileSheet.view)
			for i, r := range chatFileSheet.rects {
				if r.W <= 0 || r.H <= 0 {
					t.Fatalf("chat file row %d has no rect", i)
				}
				if r.Y+r.H > chatFileSheet.view.Y+chatFileSheet.view.H {
					t.Fatalf("chat file row %d ends at %d, past the viewport at %d", i, r.Y+r.H, chatFileSheet.view.Y+chatFileSheet.view.H)
				}
			}
			chatFileSheet.open = false
		})
	}
}
