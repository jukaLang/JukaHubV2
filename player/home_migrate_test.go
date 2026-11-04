package main

import (
	"strconv"
	"strings"
	"testing"
)

// legacyHomeScene builds the Main scene exactly as jukaconfig.json shipped it
// in 0.4.0, the file an update leaves in place on a device.
func legacyHomeScene() SceneConfig {
	scene := SceneConfig{Name: "Main", Layout: "home"}
	for _, tile := range legacyHomeTiles {
		scene.Elements = append(scene.Elements, Element{
			Type:        "button",
			Style:       "tile",
			Text:        tile.text,
			X:           tile.x,
			Y:           tile.y,
			Width:       StringOrInt(strconv.Itoa(int(tile.w))),
			Height:      StringOrInt(strconv.Itoa(int(tile.h))),
			Placeholder: tile.placeholder,
		})
	}
	return scene
}

func TestLegacyHomeLayoutMovesToTheRedesign(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{legacyHomeScene()}}
	if !migrateLegacyHomeLayout(config) {
		t.Fatal("the shipped 0.4.0 home layout was not migrated")
	}
	for i, want := range currentHomeTiles {
		el := config.Scenes[0].Elements[i]
		if !homeTileIs(el, want) {
			t.Errorf("%s: got %d,%d %sx%s, want %d,%d %dx%d", want.text, el.X, el.Y, el.Width, el.Height,
				want.x, want.y, want.w, want.h)
		}
		if el.Placeholder != want.placeholder {
			t.Errorf("%s: subtitle %q, want %q", want.text, el.Placeholder, want.placeholder)
		}
	}
	// A second pass finds nothing old left to move.
	if migrateLegacyHomeLayout(config) {
		t.Error("an already current layout was migrated a second time")
	}
}

func TestEditedHomeLayoutIsLeftAlone(t *testing.T) {
	scene := legacyHomeScene()
	scene.Elements[3].X += 10 // the owner moved one card
	config := &Config{Scenes: []SceneConfig{scene}}
	if migrateLegacyHomeLayout(config) {
		t.Fatal("an edited home layout was overwritten")
	}
	if got := config.Scenes[0].Elements[3].X; got != legacyHomeTiles[3].x+10 {
		t.Fatalf("the moved card is at x=%d, want the owner's %d", got, legacyHomeTiles[3].x+10)
	}
}

func TestLegacyMigrationKeepsCustomSubtitles(t *testing.T) {
	scene := legacyHomeScene()
	scene.Elements[5].Placeholder = "Starred things"
	config := &Config{Scenes: []SceneConfig{scene}}
	if !migrateLegacyHomeLayout(config) {
		t.Fatal("a layout that only changed a subtitle was not migrated")
	}
	if got := config.Scenes[0].Elements[5].Placeholder; got != "Starred things" {
		t.Fatalf("the owner's subtitle was replaced with %q", got)
	}
}

// Both shipped configs carry the redesigned grid, so a fresh install and the
// handheld package draw the same Home as a migrated device.
func TestShippedHomeLayoutsAreCurrent(t *testing.T) {
	for _, name := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		config, err := loadConfig(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var home *SceneConfig
		for i := range config.Scenes {
			if strings.EqualFold(config.Scenes[i].Layout, "home") {
				home = &config.Scenes[i]
				break
			}
		}
		if home == nil {
			t.Fatalf("%s has no home scene", name)
		}
		tiles, ok := homeTileIndex(home)
		if !ok {
			t.Fatalf("%s: duplicate home tile titles", name)
		}
		for _, want := range currentHomeTiles {
			idx, found := tiles[want.text]
			if !found {
				t.Errorf("%s: no %s tile", name, want.text)
				continue
			}
			if !homeTileIs(home.Elements[idx], want) {
				el := home.Elements[idx]
				t.Errorf("%s: %s is at %d,%d %sx%s, want the redesigned %d,%d %dx%d", name, want.text,
					el.X, el.Y, el.Width, el.Height, want.x, want.y, want.w, want.h)
			}
		}
	}
}
