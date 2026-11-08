package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// themeCatalogTestTheme is a complete, valid theme file: every colour the
// loader requires, in the exact form a contribution would ship.
func themeCatalogTestTheme() themeFile {
	return themeFile{
		Name:          "Terminal Amber",
		Background:    "#0a0805",
		ButtonColor:   "#1c1508",
		LabelColor:    "#ffb000",
		InputColor:    "#14100a",
		Surface:       "#0d0a06",
		SurfaceAlt:    "#171207",
		SurfaceRaised: "#241b0a",
		TextPrimary:   "#ffc65c",
		TextSecondary: "#d79a3a",
		TextTertiary:  "#96703a",
		BorderSubtle:  "#ffb00018",
		BorderDefault: "#ffb00033",
		BorderFocus:   "#ffb000",
		Success:       "#6ed47a",
		Warning:       "#ffc65c",
		Danger:        "#e05252",
		Info:          "#59b6e8",
		Overlay:       "#0a0805cc",
	}
}

// One contributor's typo must not hide the rest of the library: bad entries are
// skipped and reported, the valid ones still load.
func TestParseCommunityCatalogSkipsBrokenEntries(t *testing.T) {
	good := communityTheme{
		ID:          "Terminal-Amber",
		Name:        "Terminal Amber",
		Author:      "@pixel_rune",
		Description: "Amber on near-black",
		Tags:        []string{"Dark", "Retro"},
		Theme:       themeCatalogTestTheme(),
	}
	badColour := good
	badColour.ID = "bad-colour"
	badColour.Theme = themeCatalogTestTheme()
	badColour.Theme.Background = "not-a-colour"

	duplicate := good
	duplicate.ID = "terminal-amber"

	escaping := good
	escaping.ID = "../escape"

	raw, err := json.Marshal(communityCatalogFile{Version: 1, Themes: []communityTheme{good, badColour, duplicate, escaping}})
	if err != nil {
		t.Fatal(err)
	}
	entries, skipped, err := parseCommunityCatalog(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "terminal-amber" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Theme.Name != "Terminal Amber" || entries[0].Author != "@pixel_rune" {
		t.Fatalf("entry lost its byline: %+v", entries[0])
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped = %q, want three problems", skipped)
	}
}

func TestParseCommunityCatalogRejectsBrokenJSON(t *testing.T) {
	if _, _, err := parseCommunityCatalog([]byte("{not json")); err == nil {
		t.Fatal("broken JSON was accepted as an index")
	}
}

func TestCommunityThemeSlug(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{" Terminal Amber ", "terminal-amber"},
		{"terminal-amber", "terminal-amber"},
		{"Aurora_Bento", ""},
		{"../escape", ""},
		{"x", ""},
	}
	for _, tc := range cases {
		got, err := communityThemeSlug(tc.raw)
		if tc.want == "" {
			if err == nil {
				t.Fatalf("%q was accepted as %q", tc.raw, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q -> %q, %v", tc.raw, got, err)
		}
	}
}

func TestFetchCommunityCatalog(t *testing.T) {
	payload, err := json.Marshal(communityCatalogFile{
		Version: 1,
		Themes:  []communityTheme{{ID: "seafoam", Name: "Seafoam", Theme: themeCatalogTestTheme()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
	}))
	defer server.Close()
	entries, _, err := fetchCommunityCatalog(server.Client(), server.URL+"/gallery/community.json")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "seafoam" {
		t.Fatalf("entries = %+v", entries)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer missing.Close()
	if _, _, err := fetchCommunityCatalog(missing.Client(), missing.URL); err == nil {
		t.Fatal("a 404 was accepted as an index")
	}
}

func TestInstallCommunityThemeWritesAndApplies(t *testing.T) {
	t.Chdir(t.TempDir())
	previousDir := themeDir
	themeDir = filepath.Join(t.TempDir(), "themes")
	id := "test-community-amber"
	// installCommunityTheme applies the entry's palette to the package globals.
	// Without this the tests that measure the shipped dark palette, and the
	// keyboard probe, ran against whatever amber the last install left behind.
	palette := capturePalette()
	t.Cleanup(func() { restorePalette(palette) })
	t.Cleanup(func() {
		themeDir = previousDir
		forgetThemePreset(id)
	})

	config := &Config{}
	entry := communityTheme{ID: id, Name: "Terminal Amber", Theme: themeCatalogTestTheme()}
	if err := installCommunityTheme(config, entry); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(themeDir, id+".json")); err != nil {
		t.Fatalf("no theme file was written: %v", err)
	}
	if _, ok := themePresets[id]; !ok {
		t.Fatal("the installed theme was not registered")
	}
	if got := themePresetName(config); got != id {
		t.Fatalf("active theme = %q, want %q", got, id)
	}
	if got, _ := config.Variables.Custom["theme_background"].(string); got != entry.Theme.Background {
		t.Fatalf("background = %q", got)
	}

	// Saving the same entry again replaces it rather than listing it twice.
	if err := installCommunityTheme(config, entry); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	count := 0
	for _, name := range themePresetOrder {
		if name == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reinstall listed the theme %d times", count)
	}
}

// A theme installed on an earlier run is on disk and registered in
// themePresets, exactly as LoadThemeFiles leaves it after a restart. Reinstalling
// it has to work: the built-in check must look at the compiled palettes, not at
// what happens to be registered this session.
func TestInstallCommunityThemeUpdatesThemeFromPreviousSession(t *testing.T) {
	t.Chdir(t.TempDir())
	previousDir := themeDir
	themeDir = filepath.Join(t.TempDir(), "themes")
	id := "test-previous-session"
	palette := capturePalette()
	t.Cleanup(func() { restorePalette(palette) })
	t.Cleanup(func() {
		themeDir = previousDir
		forgetThemePreset(id)
	})

	// Write the file the way an install does, then load it the way startup
	// does: after this, themePresets has the id and no session memory does.
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := themeCatalogTestTheme()
	old.LabelColor = "#111111"
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(themeDir, id+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, failed := LoadThemeFiles(themeDir); failed[path] != nil {
		t.Fatalf("the earlier theme did not load: %v", failed[path])
	}
	if _, ok := themePresets[id]; !ok {
		t.Fatal("the earlier theme was not registered")
	}

	entry := communityTheme{ID: id, Name: "Terminal Amber", Theme: themeCatalogTestTheme()}
	if err := installCommunityTheme(&Config{}, entry); err != nil {
		t.Fatalf("updating a theme from a previous session failed: %v", err)
	}
	if preset, ok := themePresets[id]; !ok || preset.LabelColor != themeCatalogTestTheme().LabelColor {
		t.Fatalf("the update did not reach the registered preset: %+v", preset)
	}
}

// An entry cannot take over a shipped palette: the id is a file name and a
// built-in id already has pixels drawn from the compiled preset.
func TestInstallCommunityThemeRefusesBuiltInID(t *testing.T) {
	t.Chdir(t.TempDir())
	previousDir := themeDir
	themeDir = filepath.Join(t.TempDir(), "themes")
	t.Cleanup(func() { themeDir = previousDir })

	entry := communityTheme{ID: "dark", Name: "Fake Dark", Theme: themeCatalogTestTheme()}
	if err := installCommunityTheme(&Config{}, entry); err == nil {
		t.Fatal("a community entry replaced the built-in dark palette")
	}
	if _, err := os.Stat(filepath.Join(themeDir, "dark.json")); err == nil {
		t.Fatal("the refused entry still wrote a file")
	}
}
