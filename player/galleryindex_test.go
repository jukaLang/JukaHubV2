package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The published index is the one file a contributor edits, and it is served as
// is. A missing, malformed or shadowing entry is a repository problem, so it is
// caught here rather than as an empty library on someone's handheld.
func TestCommunityGalleryIndex(t *testing.T) {
	path := filepath.Join("..", "gallery", "community.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the published gallery index is missing: %v", err)
	}
	entries, skipped, err := parseCommunityCatalog(data)
	if err != nil {
		t.Fatalf("gallery index: %v", err)
	}
	if len(skipped) > 0 {
		t.Fatalf("the published index has entries the app would skip: %v", skipped)
	}
	if len(entries) == 0 {
		t.Fatal("the published index lists no themes")
	}
	for _, entry := range entries {
		if isBuiltinThemeID(entry.ID) {
			t.Errorf("theme %q would shadow a built-in palette", entry.ID)
		}
	}

	var file struct {
		Layouts []struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Config json.RawMessage `json:"config"`
		} `json:"layouts"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("gallery index: %v", err)
	}
	// The bundled layouts win on a duplicate id, so a community layout that
	// reuses one would silently never appear.
	bundled := map[string]bool{}
	if js, err := os.ReadFile(filepath.Join("..", "gallery", "layouts.js")); err == nil {
		for _, match := range regexp.MustCompile(`(?m)^\s*id: '([^']+)'`).FindAllStringSubmatch(string(js), -1) {
			bundled[match[1]] = true
		}
	}
	seen := map[string]bool{}
	for _, layout := range file.Layouts {
		if layout.ID == "" || layout.Name == "" || len(layout.Config) == 0 {
			t.Errorf("layout %q is missing an id, a name or a config", layout.ID)
			continue
		}
		if seen[layout.ID] {
			t.Errorf("layout id %q repeats", layout.ID)
		}
		seen[layout.ID] = true
		if bundled[layout.ID] {
			t.Errorf("layout id %q shadows a bundled layout", layout.ID)
		}
	}
}
