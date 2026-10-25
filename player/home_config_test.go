package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// homeTile builds a Main-scene tile element the way the configs spell them.
func homeTile(text string, x, y, w, h int32, trigger string) Element {
	return Element{
		Type: "button", Style: "tile", Text: text,
		X: x, Y: y, Width: StringOrInt(fmt.Sprint(w)), Height: StringOrInt(fmt.Sprint(h)),
		Trigger: trigger,
	}
}

func TestHomeLayoutAppliesConfigOverrides(t *testing.T) {
	h := NewHomeLayout(1280, 720)
	baseline := copyHomeItems(h)

	h.ApplyConfig([]Element{
		homeTile("Media", 100, 200, 300, 150, "change_scene:Tube"),
		func() Element {
			// Empty size falls back, but subtitle and icon still ride along.
			el := homeTile("Apps", 1200, 300, 0, 0, "change_scene:Apps")
			el.Placeholder = "Run my tools"
			el.Icon = "browser"
			return el
		}(),
	})

	media := h.Items[HomeFocusMedia]
	if media.Rect.X != 100 || media.Rect.Y != 200 || media.Rect.W != 300 || media.Rect.H != 150 {
		t.Fatalf("Media rect %+v, want the config's 100,200,300x150", media.Rect)
	}
	apps := h.Items[HomeFocusApps]
	if apps.Rect != baseline[HomeFocusApps].Rect {
		t.Fatalf("Apps geometry must fall back to the built-in card on an empty size, got %+v", apps.Rect)
	}
	if apps.Subtitle != "Run my tools" || apps.Icon != "browser" {
		t.Fatalf("Apps subtitle/icon not applied: %q %q", apps.Subtitle, apps.Icon)
	}
	if apps.Title != "Apps" {
		t.Fatalf("Apps title changed unexpectedly: %q", apps.Title)
	}

	// Untouched cards keep their built-in slot and title.
	if files := h.Items[HomeFocusFiles]; files != baseline[HomeFocusFiles] {
		t.Fatalf("Files card was disturbed: %+v, want %+v", files, baseline[HomeFocusFiles])
	}
}

func TestHomeLayoutConfigFallbacks(t *testing.T) {
	h := NewHomeLayout(1280, 720)
	baseline := copyHomeItems(h)

	h.ApplyConfig([]Element{
		// Outside the canvas.
		homeTile("Media", 2000, 0, 300, 121, ""),
		// Zero and negative sizes.
		homeTile("Chat", 0, 0, 0, 158, ""),
		homeTile("Apps", 0, 0, -50, 121, ""),
		// Unrecognized titles are ignored entirely.
		homeTile("Widget", 10, 10, 40, 40, ""),
		// A header chip tile must never move the measured header rows.
		homeTile("Clock", 0, 600, 200, 60, ""),
	})

	for _, id := range []HomeFocusID{HomeFocusMedia, HomeFocusChat, HomeFocusApps, HomeFocusClock} {
		if got, want := h.Items[id].Rect, baseline[id].Rect; got != want {
			t.Fatalf("%s override leaked: %+v, want built-in %+v", id, got, want)
		}
	}
	if _, ok := h.Items[HomeFocusID("widget")]; ok {
		t.Fatalf("unknown config tile created a home item")
	}
}

// TestBuildHomeTileMapAndSignature pins the index -> card mapping the legacy
// press/click path resolves tiles with: shipped-style tile names map to the
// right cards, non-tiles and unknown names map to nothing, and any change to
// the tile set changes the signature that forces a rebuild after a hot reload.
func TestBuildHomeTileMapAndSignature(t *testing.T) {
	h := NewHomeLayout(1280, 720)
	els := []Element{
		{Type: "button", Style: "tile", Text: "Continue", Trigger: "change_scene:Tube"},
		{Type: "button", Style: "tile", Text: "Media"},
		{Type: "button", Style: "tile", Text: "Search / Tools"},
		{Type: "button", Style: "tile", Text: "Mystery", Trigger: "change_scene:FileExplorer"},
		// Not a tile: the home layout does not draw it, so it must not map.
		{Type: "button", Style: "card", Text: "Chat"},
	}

	m := buildHomeTileMap(els, h)
	if m[0] != HomeFocusContinue || m[1] != HomeFocusMedia || m[2] != HomeFocusTools {
		t.Fatalf("tile map mismatch: 0=%q 1=%q 2=%q", m[0], m[1], m[2])
	}
	if id, ok := m[3]; ok {
		t.Fatalf("unknown tile %q mapped to %q", "Mystery", id)
	}
	if id, ok := m[4]; ok {
		t.Fatalf("style %q element mapped to %q", "card", id)
	}

	sig := homeTileSignature(els)
	// Renaming, moving or adding a tile must change the fingerprint so a hot
	// reload rebuilds the map instead of pointing at the previous config.
	els[2].Text = "Utilities"
	if homeTileSignature(els) == sig {
		t.Fatal("renaming a tile did not change the tile signature")
	}
	els[2], els[1] = els[1], els[2]
	if homeTileSignature(els) == sig {
		t.Fatal("reordering tiles did not change the tile signature")
	}
}

// copyHomeItems snapshots the card map so override tests can diff against the
// built-in layout.
func copyHomeItems(h *HomeLayout) map[HomeFocusID]HomeItem {
	out := make(map[HomeFocusID]HomeItem, len(h.Items))
	for id, item := range h.Items {
		out[id] = item
	}
	return out
}

// TestHomeLayoutShippedConfigCoversHomeCards proves the shipped jukaconfig.json
// and the device config's Main scene customize every menu card with geometry
// that actually lands in the canvas — the "config matches the rendered home
// screen" invariant the config-driven layout now depends on.
func TestHomeLayoutShippedConfigCoversHomeCards(t *testing.T) {
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var cfg Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			sceneIdx := -1
			for i, s := range cfg.Scenes {
				if s.Name == "Main" {
					sceneIdx = i
					break
				}
			}
			if sceneIdx < 0 {
				t.Fatalf("%s has no Main scene", path)
			}
			scene := cfg.Scenes[sceneIdx]

			canvasW, canvasH := uiCanvasWideWidth, uiCanvasWideHeight
			if strings.Contains(path, "device") {
				// The handheld profile squeezes element coordinates into the
				// compact canvas at load, exactly as applyUIProfileForDevice
				// does before the home layout reads them.
				canvasW, canvasH = uiCanvasCompactW, uiCanvasCompactH
				scaleSceneGeometry(&cfg, canvasDimension(cfg.Width), canvasDimension(cfg.Height), canvasW, canvasH)
			}

			h := NewHomeLayout(canvasW, canvasH)
			h.ApplyConfig(scene.Elements)
			// Every menu card must be matched by exactly one config tile, and
			// the applied geometry must stay inside the canvas. Most shipped
			// tiles mirror the built-in geometry on purpose, so "rect differs
			// from default" cannot prove ownership — matching does.
			matched := map[HomeFocusID]int{}
			for _, el := range scene.Elements {
				if el.Type != "button" || strings.TrimSpace(el.Text) == "" {
					continue
				}
				id := h.configItemFor(strings.TrimSpace(el.Text))
				if id == "" || !homeConfigOverlayables()[id] {
					continue
				}
				matched[id]++
				r := h.Items[id].Rect
				if r.X < 0 || r.Y < 0 || r.X+r.W > canvasW || r.Y+r.H > canvasH {
					t.Errorf("tile %q: card %s geometry escapes the %dx%d canvas: %+v", el.Text, id, canvasW, canvasH, r)
				}
			}
			menuCards := []HomeFocusID{HomeFocusContinue, HomeFocusMedia, HomeFocusFiles, HomeFocusPackages,
				HomeFocusApps, HomeFocusFavorites, HomeFocusBrowser, HomeFocusChat,
				HomeFocusTools, HomeFocusEmulator, HomeFocusSettings, HomeFocusExit}
			for _, id := range menuCards {
				if matched[id] != 1 {
					t.Errorf("card %s: shipped config matches it %d times, want exactly once", id, matched[id])
				}
			}
			if v := h.BoundsViolations(); len(v) > 0 {
				t.Fatalf("layout violations with the shipped config applied: %v", v)
			}

			// Every configured trigger must name a scene that exists.
			for _, el := range scene.Elements {
				target := strings.TrimPrefix(el.Trigger, "change_scene:")
				if target == el.Trigger || el.Trigger == "" {
					continue
				}
				if sceneIndexExact(&cfg, target) < 0 {
					t.Errorf("tile %q targets missing scene %q", el.Text, target)
				}
			}
		})
	}
}
