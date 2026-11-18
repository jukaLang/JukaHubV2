package main

import (
	"encoding/json"
	"testing"
)

// TestThemeUpdateEditedDecision checks the over-the-air rule on real file
// bytes: a theme written by the installer and read back hashes the same (so
// re-indenting is not an edit), and only an unedited file may be replaced.
func TestThemeUpdateEditedDecision(t *testing.T) {
	entry := communityTheme{ID: "seafoam", Name: "Seafoam", Theme: themeFile{Background: "#eef7f4", Info: "#59b6e8"}}
	written := themePayloadHash(communityThemePayload(entry))
	data, err := json.MarshalIndent(communityThemePayload(entry), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var onDisk themeFile
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if got := themePayloadHash(onDisk); got != written {
		t.Fatalf("read-back hash %s, written %s", got, written)
	}
	onDisk.Info = "#ff0000"
	edited := themePayloadHash(onDisk)
	newer := entry
	newer.Theme.Background = "#000000"
	catalog := themePayloadHash(communityThemePayload(newer))

	cases := []struct {
		name                         string
		installed, recorded, catalog string
		want                         themeUpdateKind
	}{
		{"same as gallery", written, written, written, themeUpdateCurrent},
		{"new version, untouched", written, written, catalog, themeUpdateAvailable},
		{"new version, edited", edited, written, catalog, themeUpdateEdited},
		{"adopted file that differs", written, "", catalog, themeUpdateEdited},
		{"unreadable file", themeUpdateUnreadable, written, catalog, themeUpdateEdited},
	}
	for _, tc := range cases {
		if got := themeUpdateDecide(tc.installed, tc.recorded, tc.catalog); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
