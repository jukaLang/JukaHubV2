package main

// Offscreen check for the Patch confirmation dialog. It is the one place where
// a truncated message can hide the reason a destructive action is asking, so
// the long body has to fit the card instead of ending in "..." after two lines.

import (
	"os"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestPatchModalRendersLongBody(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	f := newProbeFrame(t)
	config := &Config{}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	if medium == nil {
		medium = font
	}

	prev := patchModal
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	// Four wrapped lines at this width; the old two-line split lost half of it.
	body := "This update replaces the launcher, the player binary and the bundled launch script. " +
		"Patch verified the archive digest and the release signature before staging it, and keeps a " +
		"rollback copy of every replaced file. The device restarts once the swap script finishes."
	patchModal = &patchModalState{
		ID:      "test",
		Title:   "Apply staged update?",
		Body:    body,
		Confirm: "Install",
	}
	t.Cleanup(func() {
		patchModal = prev
		screenWidth, screenHeight = prevW, prevH
		patchMenuRects = nil
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderPatchModal(renderer, config, font, medium)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	// Ask the layout the renderer used: every wrapped line has to be on the
	// card, and the card has to stay on screen.
	layout := layoutPatchModal(font, patchModal.Body)
	card := layout.card
	if card.Y < 0 || card.Y+card.H > probeH {
		t.Fatalf("card escapes the screen: %+v", card)
	}
	if len(layout.lines) < 4 {
		t.Fatalf("a four-line body wrapped to %d lines: %#v", len(layout.lines), layout.lines)
	}
	textRows := 0
	for row := 0; row < len(layout.lines); row++ {
		bright := 0
		top := layout.textY + int32(row)*layout.lineH
		for y := top; y < top+layout.lineH; y++ {
			for x := card.X + 24; x < card.X+card.W-24; x++ {
				if lumaAt(f, x, y) > 110 {
					bright++
				}
			}
		}
		if bright > 40 {
			textRows++
		}
	}
	if textRows != len(layout.lines) {
		t.Errorf("drew text on %d of %d body rows", textRows, len(layout.lines))
	}
	// The body must end above the button row.
	if bodyBottom := layout.textY + int32(len(layout.lines))*layout.lineH; bodyBottom > card.Y+card.H-64 {
		t.Errorf("body ends at %d, button row starts at %d", bodyBottom, card.Y+card.H-64)
	}

	if os.Getenv("JUKA_PREVIEW") != "" {
		saveProbePNG(t, f, "patch-confirm.png")
	}
}
