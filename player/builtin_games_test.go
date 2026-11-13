package main

import "testing"

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

	var lib gameLibraryReport
	libraryAddJukaLand(&lib, true)
	if lib.Total != 1 || len(lib.Systems) != 1 || lib.Systems[0].Name != builtInLibrarySystem ||
		len(lib.Systems[0].Games) != 1 || !isJukaLandLibraryGame(lib.Systems[0].Games[0]) {
		t.Fatalf("built-in library entry = %+v", lib)
	}
	if with, total := libraryCoverTotals(lib); with != 0 || total != 0 {
		t.Errorf("JukaLand was counted as downloadable box art: %d/%d", with, total)
	}
	if found := filterLibrarySystems(lib.Systems, "jukaland"); len(found) != 1 || len(found[0].Games) != 1 {
		t.Errorf("searching for JukaLand found %+v", found)
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
