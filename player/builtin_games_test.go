package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestJukaLandIsReachableUnderBuiltInGames(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"},
		{Name: "Games", Icon: "G"},
		{Name: "JukaLand", Icon: "G"},
	}}
	menuGames := gameMenuEntries(config)
	if got := menuGames[len(menuGames)-1]; got.name != "JukaLand" || got.scene != "JukaLand" {
		t.Fatalf("Games screen ends with %+v, want a playable JukaLand card", got)
	}

	startRows := 0
	for _, group := range startMenuGroups(config) {
		for _, row := range group.items {
			if row.scene != "JukaLand" {
				continue
			}
			startRows++
			if group.name != "Games" || row.badge != "Built-in" {
				t.Errorf("JukaLand appears in %q with badge %q", group.name, row.badge)
			}
		}
	}
	if startRows != 1 {
		t.Errorf("Start menu has %d JukaLand rows, want one", startRows)
	}

	// The library lists JukaLand, the Synth, Solitaire and the seven arcade
	// minigames under "Built-in games", each with its embedded cover
	// installed as a file the cover views can load.
	var lib gameLibraryReport
	coverRoot := t.TempDir()
	libraryAddBuiltInGames(&lib, func(string) bool { return true }, coverRoot)
	if lib.Total != len(builtInLibraryGames) || len(lib.Systems) != 1 || lib.Systems[0].Name != builtInLibrarySystem ||
		len(lib.Systems[0].Games) != 10 {
		t.Fatalf("built-in library entries = %+v", lib)
	}
	for i, want := range []string{"JukaLand", synthSceneName, solitaireSceneName} {
		game := lib.Systems[0].Games[i]
		builtIn, ok := builtInLibraryGameFor(game)
		if !ok || builtIn.scene != want || !game.HasArt || !artworkLocalPNG(game.Art) {
			t.Errorf("built-in game %d = %+v (scene %q), want %s with a PNG cover", i, game, builtIn.scene, want)
		}
		if builtInCoverData(builtInMenuCover(gameEntry{scene: want})) == nil {
			t.Errorf("%s has no embedded cover", want)
		}
	}
	for _, game := range lib.Systems[0].Games[3:] {
		if builtIn, ok := builtInLibraryGameFor(game); !ok || builtIn.arcade == gameNone || !game.HasArt || !artworkLocalPNG(game.Art) {
			t.Errorf("arcade game %+v has no PNG cover or no game to start", game)
		}
	}
	if builtInMenuCover(gameEntry{scene: "Games"}) != "" {
		t.Error("the Games screen took an arcade game's cover as its own")
	}
	// Every card on the Games screen draws a cover.
	for _, g := range gameList {
		if builtInCoverData(builtInMenuCover(g)) == nil {
			t.Errorf("Games screen card %s has no embedded cover", g.name)
		}
	}
	if with, total := libraryCoverTotals(lib); with != 0 || total != 0 {
		t.Errorf("built-in games were counted as downloadable box art: %d/%d", with, total)
	}
	if found := filterLibrarySystems(lib.Systems, "jukaland"); len(found) != 1 || len(found[0].Games) != 1 {
		t.Errorf("searching for JukaLand found %+v", found)
	}
	// JukaLand and the Games screen's seven arcade games, no Synth or Solitaire.
	var onlyJukaLand gameLibraryReport
	libraryAddBuiltInGames(&onlyJukaLand, builtInScenesPresent(config), "")
	if onlyJukaLand.Total != 8 || onlyJukaLand.Systems[0].Games[0].Path != jukaLandLibraryPath || onlyJukaLand.Systems[0].Games[0].HasArt {
		t.Errorf("config without the Synth scene lists %+v", onlyJukaLand)
	}

	withoutJukaLand := &Config{Scenes: config.Scenes[:2]}
	if got := gameMenuEntries(withoutJukaLand); len(got) != len(menuGames)-1 {
		t.Errorf("config without JukaLand still shows %d game cards", len(got))
	}

	// The Synth sits right under JukaLand, on the Games screen and in Start → Games.
	withSynth := &Config{Scenes: append(append([]SceneConfig(nil), config.Scenes...), SceneConfig{Name: synthSceneName, Icon: "S"})}
	if got := gameMenuEntries(withSynth); got[len(got)-1].scene != synthSceneName || got[len(got)-2].scene != "JukaLand" {
		t.Errorf("Games screen does not end JukaLand, Synth: %+v", got[len(got)-2:])
	}
	var gamesRows []string
	for _, group := range startMenuGroups(withSynth) {
		for _, row := range group.items {
			if row.scene == synthSceneName && group.name != "Games" {
				t.Errorf("Synth listed under %q", group.name)
			}
			if group.name == "Games" {
				gamesRows = append(gamesRows, row.scene)
			}
		}
	}
	if len(gamesRows) < 3 || gamesRows[1] != "JukaLand" || gamesRows[2] != synthSceneName {
		t.Errorf("Start → Games begins %v, want Games, JukaLand, Synth", gamesRows)
	}
}

// TestPadsProjectLoadsAHandEditedFile: pads.json is the user's work. A file
// edited by hand or written by another build must load clamped (no pad,
// step or pattern out of range for the engine) and survive a save.
func TestPadsProjectLoadsAHandEditedFile(t *testing.T) {
	raw := `{"bpm": 999, "current": 42, "song": [0, 9, -3], "stickFx": [99],
		"pads": [{"sample": "kit:kick", "start": 0.9, "end": 0.2, "vol": 7, "pitch": -80, "choke": 12}],
		"patterns": [{"steps": 20, "hits": [{"p": 0, "s": 3}, {"p": 0, "s": 3}, {"p": 70, "s": 1}, {"p": 1, "s": 40}]}]}`
	p := padsDefaultProject()
	p.Patterns = [padsPatterns]padsPattern{}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	p.sanitize()
	if p.BPM != padsMaxBPM || p.Current != padsPatterns-1 || p.FX[0] != padsFxCount-1 {
		t.Errorf("bpm %d, current %d, stick fx %d: not clamped", p.BPM, p.Current, p.FX[0])
	}
	if got, want := fmt.Sprint(p.Song), fmt.Sprint([]int{0, padsPatterns - 1, 0}); got != want {
		t.Errorf("song %s, want %s", got, want)
	}
	if d := p.Pads[0]; d.Start != 0 || d.End != 1 || d.Volume != 1 || d.Pitch != -padsMaxNote || d.Choke != padsChokes {
		t.Errorf("pad A1 not clamped: %+v", d)
	}
	pat := p.Patterns[0]
	if pat.Steps != 32 || len(pat.Hits) != 1 || pat.Hits[0] != (padsHit{Pad: 0, Step: 3}) {
		t.Errorf("pattern 1 = %d steps, hits %+v; want 32 steps and the one valid hit", pat.Steps, pat.Hits)
	}
	data, err := json.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	var q padsProject
	if err := json.Unmarshal(data, &q); err != nil {
		t.Fatal(err)
	}
	q.sanitize()
	if q.BPM != p.BPM || q.Pads[0] != p.Pads[0] || len(q.Patterns[0].Hits) != 1 {
		t.Errorf("a saved project did not load back the same")
	}
}
