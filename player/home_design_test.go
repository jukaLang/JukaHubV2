package main

// The home screen's geometry has two sources: the design grid in
// HomeLayout.rebuildItems and the Main scene's tiles in jukaconfig.json, which
// ApplyConfig makes authoritative. That second source is how the screen kept
// rendering the old layout while the code drew the new one: the config still
// held the previous coordinates, so every card landed where it used to. These
// tests keep the two in agreement.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// homeDesignCardIDs are the cards the shipped config places.
var homeDesignCardIDs = []HomeFocusID{
	HomeFocusContinue, HomeFocusMedia, HomeFocusFiles, HomeFocusPackages,
	HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser, HomeFocusChat,
	HomeFocusTools, HomeFocusEmulator, HomeFocusSettings, HomeFocusExit,
}

// TestShippedHomeConfigMatchesTheDesignGrid loads the real jukaconfig.json and
// checks that applying its Main tiles changes nothing: the shipped config has to
// describe the same grid the code draws, or the app silently shows the old
// screen no matter what the renderer does.
func TestShippedHomeConfigMatchesTheDesignGrid(t *testing.T) {
	config, path, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		t.Fatalf("the shipped jukaconfig.json did not load: %v", err)
	}
	var elements []Element
	for _, scene := range config.Scenes {
		if scene.Layout == "home" {
			elements = scene.Elements
			break
		}
	}
	if len(elements) == 0 {
		t.Fatalf("%s has no home-layout scene with tiles", path)
	}

	layout := NewHomeLayout(HomeBaseWidth, HomeBaseHeight)
	design := make(map[HomeFocusID]sdl.Rect, len(homeDesignCardIDs))
	for _, id := range homeDesignCardIDs {
		design[id] = layout.Items[id].Rect
	}
	layout.ApplyConfig(elements)

	for _, id := range homeDesignCardIDs {
		if got := layout.Items[id].Rect; got != design[id] {
			t.Errorf("%s: the config places the card at %+v, the design grid has %+v — update the Main scene in jukaconfig.json to match",
				id, got, design[id])
		}
	}
}

// TestHomeDesignGridIsCoherent asserts the properties the design sheet relies
// on: every card is inside the canvas, no two cards overlap, the hero spans the
// right column's two rows, and the bottom row is a single row of six.
func TestHomeDesignGridIsCoherent(t *testing.T) {
	layout := NewHomeLayout(HomeBaseWidth, HomeBaseHeight)
	if problems := layout.BoundsViolations(); len(problems) > 0 {
		t.Fatalf("the built-in grid has layout problems: %v", problems)
	}

	cont := layout.Items[HomeFocusContinue].Rect
	media := layout.Items[HomeFocusMedia].Rect
	if cont.Y != media.Y || cont.H != media.H+homeGutter+152 {
		t.Fatalf("hero rows do not line up: Continue %+v, Media %+v", cont, media)
	}

	var rowY int32
	for _, id := range []HomeFocusID{
		HomeFocusFavorites, HomeFocusBrowser, HomeFocusChat,
		HomeFocusTools, HomeFocusEmulator, HomeFocusSettings,
	} {
		r := layout.Items[id].Rect
		if rowY == 0 {
			rowY = r.Y
		}
		if r.Y != rowY || r.H != 152 {
			t.Fatalf("%s is not on the six-tile row: %+v (row y=%d)", id, r, rowY)
		}
		if r.W < 180 || r.W > 192 {
			t.Fatalf("%s is %dpx wide, want an equal sixth of the 1216px content box", id, r.W)
		}
	}

	// The Exit pill rides the footer band, not the card grid.
	exit := layout.Items[HomeFocusExit].Rect
	if exit.Y < 664 || exit.Y+exit.H > HomeBaseHeight {
		t.Fatalf("Exit %+v is not on the footer band", exit)
	}
}
