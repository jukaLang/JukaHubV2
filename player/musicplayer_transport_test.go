package main

// The audio player's button row is planned from measured text, because the
// static version broke in three separate ways that a device pass would have
// caught: at the 1280 canvas "Repeat Off" (120 px) was ellipsized to
// "Repeat O…" inside a 124 px button, and on narrower canvases the fixed
// x offsets (+290/+402/+534) ran the mode buttons straight underneath the
// volume cluster — Fav sat under the "-" button at 960 and off the card
// entirely on an 800 px window.
//
// These checks pin the geometry at every supported canvas: nothing overlaps,
// nothing leaves the card, and the primary canvas still shows all three modes.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func overlapArea(a, b sdl.Rect) int32 {
	x := a.X
	if b.X > x {
		x = b.X
	}
	y := a.Y
	if b.Y > y {
		y = b.Y
	}
	right := a.X + a.W
	if b.X+b.W < right {
		right = b.X + b.W
	}
	bottom := a.Y + a.H
	if b.Y+b.H < bottom {
		bottom = b.Y + b.H
	}
	if right <= x || bottom <= y {
		return 0
	}
	return (right - x) * (bottom - y)
}

// TestMusicTransportRowFitsEveryCanvas renders the player at the wide device
// canvas, the Brick's compact canvas and the smallest supported window, then
// checks every drawn control against its neighbours.
func TestMusicTransportRowFitsEveryCanvas(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	newProbeFrame(t)
	renderer := newKeyboardTestRenderer(t)
	prevTracks := music.tracks
	t.Cleanup(func() { music.tracks = prevTracks })
	music.tracks = nil
	music.cursor = 0

	for _, size := range []struct {
		name string
		w, h int32
	}{
		{"SmartPro 1280x720", 1280, 720},
		{"Brick 960x720", 960, 720},
		{"window 800x480", 800, 480},
	} {
		withScreen(t, size.w, size.h)
		ClearTextCache()
		musicRects.rows = musicRects.rows[:0]
		renderMusicPlayer(renderer, config)

		card := sdl.Rect{X: 24, Y: 76, W: size.w - 48, H: 292}
		buttons := []struct {
			name string
			rect sdl.Rect
		}{
			{"prev", musicRects.prev},
			{"play/pause", musicRects.playPause},
			{"next", musicRects.next},
			{"stop", musicRects.stop},
			{"shuffle", musicRects.shuffle},
			{"repeat", musicRects.repeat},
			{"favorite", musicRects.favorite},
			{"vol-", musicRects.volDown},
			{"vol+", musicRects.volUp},
		}

		for _, b := range buttons {
			if b.rect.W <= 0 || b.rect.H <= 0 {
				t.Errorf("%s: %s was not drawn", size.name, b.name)
				continue
			}
			if b.rect.X < card.X-1 || b.rect.Y < card.Y-1 ||
				b.rect.X+b.rect.W > card.X+card.W+1 || b.rect.Y+b.rect.H > card.Y+card.H-4 {
				t.Errorf("%s: %s %+v leaves the card %+v", size.name, b.name, b.rect, card)
			}
		}

		for i := 0; i < len(buttons); i++ {
			for j := i + 1; j < len(buttons); j++ {
				if area := overlapArea(buttons[i].rect, buttons[j].rect); area > 0 {
					t.Errorf("%s: %s %+v overlaps %s %+v by %d px²",
						size.name, buttons[i].name, buttons[i].rect,
						buttons[j].name, buttons[j].rect, area)
				}
			}
		}
	}

	// The label that started this: it has to fit, not be ellipsized away.
	withScreen(t, uiCanvasWideWidth, uiCanvasWideHeight)
	ClearTextCache()
	renderMusicPlayer(renderer, config)
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatalf("no font available for the label measurement: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	for _, mode := range []musicRepeatMode{musicRepeatOff, musicRepeatAll, musicRepeatOne} {
		label := "Repeat " + mode.short()
		tw, _, err := font.SizeUTF8(label)
		if err != nil {
			t.Fatalf("measure %q: %v", label, err)
		}
		if int32(tw)+2*SpaceSM > musicRects.repeat.W {
			t.Errorf("repeat button %d px cannot hold %q (%d px + padding)",
				musicRects.repeat.W, label, tw)
		}
	}
}
