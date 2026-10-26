package main

// onboarding_render_test.go — the tour's card as the handhelds take it. A photo
// of the first run on a Brick caught two defects in the same frame: the
// controls hint line was drawn across the card's bottom border and against the
// Next button (the band under the buttons is 28px, the line is 22–30px tall),
// and the body split a word over the line break ("th" / "n shows"). Both are
// pinned here against the recorded layout, so the card cannot quietly go back.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// renderTourOn draws one tour step on the given canvas with the entrance
// settled, and reports the frame it painted.
func renderTourOn(t *testing.T, w, h int32, step int) *probeFrame {
	t.Helper()
	if onboardingCoveredByOverlay() {
		t.Fatal("a modal from another test is still open, the tour cannot draw")
	}
	prevW, prevH := screenWidth, screenHeight
	prevOnboarding := onboarding
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		onboarding = prevOnboarding
	})
	screenWidth, screenHeight = w, h
	onboardingStart()
	onboarding.step = step

	cfg := onboardingTestConfig()
	cfg.Variables.ReducedMotion = true
	cfg.Variables.Custom["reducedMotion"] = "true"
	return renderFunProbe(t, func(r *sdl.Renderer) { renderOnboarding(r, cfg) })
}

// TestTourHintLineSitsBelowTheCard is the fix for the screenshot: on the
// Brick's compact canvas and on the 480p panel every step's controls hint is
// below the card's border, on screen, clear of the Next button, and actually
// drawn there.
func TestTourHintLineSitsBelowTheCard(t *testing.T) {
	for _, canvas := range []struct{ w, h int32 }{
		{uiCanvasCompactW, uiCanvasCompactH}, // Brick
		{800, 480},                           // the small panel, where the screen-edge clamp earns its keep
	} {
		for step := range onboardingSteps {
			f := renderTourOn(t, canvas.w, canvas.h, step)
			card, footer := onboarding.card, onboarding.footer
			where := fmt.Sprintf("%dx%d step %d", canvas.w, canvas.h, step+1)
			if card.W == 0 || card.H == 0 {
				t.Fatalf("%s: no card rect was recorded", where)
			}
			if footer.W == 0 || footer.H == 0 {
				t.Fatalf("%s: no hint-line rect was recorded", where)
			}
			if footer.Y < card.Y+card.H {
				t.Errorf("%s: hint line at y=%d crosses the card's bottom edge at %d",
					where, footer.Y, card.Y+card.H)
			}
			if bottom := onboarding.next.Y + onboarding.next.H; onboarding.next.W > 0 && footer.Y < bottom {
				t.Errorf("%s: hint line at y=%d overlaps the Next button ending at %d",
					where, footer.Y, bottom)
			}
			if footer.Y+footer.H > screenHeight-4 {
				t.Errorf("%s: hint line runs off the screen (%d + %d > %d)",
					where, footer.Y, footer.H, screenHeight)
			}
			if footer.X < card.X || footer.X+footer.W > card.X+card.W {
				t.Errorf("%s: hint line (%d..%d) is not inside the card's width (%d..%d)",
					where, footer.X, footer.X+footer.W, card.X, card.X+card.W)
			}
			// And it is really drawn at that rect, not just recorded: the band
			// below the card is dim overlay, so bright pixels there are the text.
			lit := 0
			for y := footer.Y; y < footer.Y+footer.H; y++ {
				for x := footer.X; x < footer.X+footer.W; x++ {
					c := f.at(x, y)
					if int(c.R)+int(c.G)+int(c.B) > 150 {
						lit++
					}
				}
			}
			if lit < 20 {
				t.Errorf("%s: the hint line's rect holds %d lit pixels: the text was not drawn there",
					where, lit)
			}
		}
	}
}

// TestTourBodyWrapsOnWordBoundaries pins the body's wrap at the call site: every
// line except the (possibly ellipsized) last one holds whole words from the
// step's own text, which the old rune cut could not promise — it filled by
// pixels and broke "then" between "th" and "n" on the Brick.
func TestTourBodyWrapsOnWordBoundaries(t *testing.T) {
	for step := range onboardingSteps {
		renderTourOn(t, uiCanvasCompactW, uiCanvasCompactH, step)
		lines := onboarding.bodyLines
		if len(lines) < 2 {
			continue // one line (or the checklist step, which draws rows)
		}
		joined := strings.Fields(strings.Join(lines[:len(lines)-1], " "))
		want := strings.Fields(onboardingSteps[step].body)
		if len(joined) > len(want) {
			t.Fatalf("step %d: the wrap produced more words than the body has", step+1)
		}
		for i := range joined {
			if joined[i] != want[i] {
				t.Errorf("step %d: line break splits a word: got %q where the body has %q\nlines: %q",
					step+1, joined[i], want[i], lines)
				break
			}
		}
	}
}
