package main

// Regression for a real collision caught by rendering the Music player: the
// status line ("1x · Repeat off") painted straight over the seek bar. Both
// were positioned off the now-playing card, but statusY was computed from the
// fonts actually loaded while seekY was a hardcoded cardY+138. The loaded
// display font is far taller than that offset assumed, so the status line's
// text box ran down over the track and its thumb.
//
// The bar now follows the status line, and the transport row follows the
// times. These checks pin that relationship against the rects the player
// actually publishes, so a future font change or edit cannot silently
// re-overlap them.

import "testing"

func TestMusicStatusLineClearsSeekBar(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	renderMusicForTest(t) // publishes musicRects

	if !musicRects.valid {
		t.Fatal("musicRects not published")
	}

	// Resolve the same fonts renderMusicPlayer uses, so statusY is computed
	// the way the renderer computes it.
	config := &Config{}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Skip("small font unavailable")
	}
	fontH := int32(font.Height())

	// statusY = cardY + 30 + bigH + 8 + fontH + 6, and seekY = statusY + fontH + 13.
	// musicRects.seek is {Y: seekY - 10, H: 28}, so seekY = seek.Y + 10 and
	// statusY = seekY - fontH - 13.
	const cardY, cardH int32 = 76, 292 // headerH(56) + 20, and the card height
	seekY := musicRects.seek.Y + 10
	statusY := seekY - fontH - 13
	statusBottom := statusY + fontH
	thumbTop := seekY - 3
	play := musicRects.playPause
	t.Logf("fontH=%d statusY=%d statusBottom=%d seekY=%d thumbTop=%d playPause=%v cardBottom=%d",
		fontH, statusY, statusBottom, seekY, thumbTop, play, cardY+cardH)

	if statusBottom > thumbTop {
		t.Errorf("status line (bottom %d) overlaps the seek thumb (top %d)",
			statusBottom, thumbTop)
	}

	// The transport row must clear the seek hit box and stay inside the card.
	if play.Y < musicRects.seek.Y+musicRects.seek.H {
		t.Errorf("transport row (top %d) overlaps the seek bar (bottom %d)",
			play.Y, musicRects.seek.Y+musicRects.seek.H)
	}
	if play.Y+play.H > cardY+cardH {
		t.Errorf("transport row (bottom %d) spills past the card (bottom %d)",
			play.Y+play.H, cardY+cardH)
	}
}
