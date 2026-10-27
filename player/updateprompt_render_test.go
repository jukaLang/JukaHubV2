package main

// Offscreen check for the update modal's "what's new" preview. It renders the
// real overlay with the software renderer, so the notes and the buttons have to
// share the card without touching: the card grows for the notes rather than
// letting the text run under the button row.

import (
	"os"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// withPromptNotes installs release notes for the duration of a test.
func withPromptNotes(t *testing.T, text string) {
	t.Helper()
	patchStateMu.Lock()
	prevTag, prevText := patchState.ReleaseNotesTag, patchState.ReleaseNotesText
	patchState.ReleaseNotesTag, patchState.ReleaseNotesText = "v9.9.9", text
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		patchState.ReleaseNotesTag, patchState.ReleaseNotesText = prevTag, prevText
		patchStateMu.Unlock()
	})
}

func TestUpdatePromptPreviewRendersAboveButtons(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	withPromptNotes(t, "## Fixes\n- Patch keeps the swap script on a full card\n- Chat keyboard wraps long drafts\n- Music search no longer skips the first track\n")

	f := newProbeFrame(t)
	config := &Config{}
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	prevOpen, prevFocus := updatePrompt.open, updatePrompt.focus
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	updatePrompt.open, updatePrompt.focus = true, 0
	t.Cleanup(func() {
		updatePrompt.open, updatePrompt.focus = prevOpen, prevFocus
		updatePrompt.nowBtn, updatePrompt.laterBtn = sdl.Rect{}, sdl.Rect{}
		screenWidth, screenHeight = prevW, prevH
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderUpdatePrompt(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	now, later := updatePrompt.nowBtn, updatePrompt.laterBtn
	if now.W == 0 || later.W == 0 {
		t.Fatalf("prompt published empty buttons: now=%+v later=%+v", now, later)
	}
	if now.X+now.W > later.X {
		t.Errorf("buttons overlap: now ends at %d, later starts at %d", now.X+now.W, later.X)
	}
	if now.Y+now.H > probeH || now.X < 0 || now.Y < 0 {
		t.Errorf("buttons escape the screen: now=%+v later=%+v", now, later)
	}

	// Ask the same layout the renderer used, so the check is about the text and
	// not a guess at the line height.
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = font
	}
	m := layoutUpdatePrompt(font, medium)
	if len(m.notes) == 0 {
		t.Fatal("layout dropped the cached release notes")
	}
	if m.noteBottom >= m.buttonsY {
		t.Errorf("notes end at %d, buttons start at %d: text would run under the button row", m.noteBottom, m.buttonsY)
	}
	for _, line := range append(append([]string{}, m.bodyLines...), m.hintLines...) {
		if w, _, _ := font.SizeUTF8(line); int32(w) > m.card.W-56 {
			t.Errorf("line %q is %dpx wide, card text box is %dpx", line, w, m.card.W-56)
		}
	}

	// And confirm it on the pixels: the band between the last note line and the
	// button row must contain note text and stay clear of the button fill.
	bright := 0
	for y := m.noteLabelY; y < m.buttonsY-2; y++ {
		for x := m.card.X + 28; x < m.card.X+m.card.W-28; x++ {
			if lumaAt(f, x, y) > 90 {
				bright++
			}
		}
	}
	if bright < 200 {
		t.Errorf("expected release-note text above the buttons, found %d bright pixels", bright)
	}

	if os.Getenv("JUKA_PREVIEW") != "" {
		saveProbePNG(t, f, "update-prompt.png")
	}
}

// lumaAt returns a rough brightness for one probe pixel.
func lumaAt(f *probeFrame, x, y int32) int {
	if x < 0 || y < 0 || x >= probeW || y >= probeH {
		return 0
	}
	i := (y*probeW + x) * 4
	c := f.buf[i : i+4]
	return (int(c[2])*299 + int(c[1])*587 + int(c[0])*114) / 1000
}
