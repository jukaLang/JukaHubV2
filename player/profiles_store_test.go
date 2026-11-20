package main

import (
	"encoding/json"
	"testing"
)

// The profile list is read from the card: an id that could leave the profiles
// folder, a duplicate, an empty name, a colour off the palette and an Active
// that names nobody are mended, and a file from before versions is brought to
// this one and written back.
func TestProfilesParseMendsAndMigrates(t *testing.T) {
	raw := `{"owner_colour": 99, "profiles": [
		{"id": "../../etc", "name": "Evil"},
		{"id": "p0123abcd", "name": "  Mia  ", "colour": 3},
		{"id": "p0123abcd", "name": "Dup"},
		{"id": "pdeadbeef", "name": "", "colour": -1}
	], "active": "pffffffff"}`
	doc, changed, err := profilesParse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || doc.Version != profilesVersion {
		t.Fatalf("an old, damaged list must be mended and written back: changed=%v version=%d", changed, doc.Version)
	}
	if len(doc.Profiles) != 2 {
		t.Fatalf("profiles = %+v, want Mia and the unnamed one", doc.Profiles)
	}
	if p := doc.Profiles[0]; p.ID != "p0123abcd" || p.Name != "Mia" || p.Colour != 3 {
		t.Fatalf("first profile = %+v", p)
	}
	if p := doc.Profiles[1]; p.ID != "pdeadbeef" || p.Name == "" || p.Colour != 0 {
		t.Fatalf("second profile = %+v", p)
	}
	if doc.OwnerColour != 0 || doc.Active != "" {
		t.Fatalf("owner colour %d, active %q", doc.OwnerColour, doc.Active)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	again, changed, err := profilesParse(data)
	if err != nil || changed || len(again.Profiles) != 2 {
		t.Fatalf("a current list must read back as it is: changed=%v err=%v %+v", changed, err, again)
	}
}

// The list survives a restart: what profilesUpdate wrote is what the next
// load reads.
func TestProfilesListPersists(t *testing.T) {
	prevRoot := profilesRootOverride
	profilesRootOverride = t.TempDir()
	profilesState.mu.Lock()
	prevLoaded, prevDoc := profilesState.loaded, profilesState.doc
	profilesState.loaded, profilesState.doc = false, profilesDoc{}
	profilesState.mu.Unlock()
	t.Cleanup(func() {
		profilesRootOverride = prevRoot
		profilesState.mu.Lock()
		profilesState.loaded, profilesState.doc = prevLoaded, prevDoc
		profilesState.mu.Unlock()
	})

	err := profilesUpdate(func(doc *profilesDoc) error {
		doc.OwnerName = "Juka"
		doc.Profiles = append(doc.Profiles, profileEntry{ID: "p00000001", Name: "Leo", Colour: 2})
		doc.Active, doc.AskAtStart = "p00000001", true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	profilesState.mu.Lock()
	profilesState.loaded, profilesState.doc = false, profilesDoc{}
	profilesState.mu.Unlock()

	doc := profilesSnapshot()
	if doc.OwnerName != "Juka" || !doc.AskAtStart || doc.Active != "p00000001" || len(doc.Profiles) != 1 {
		t.Fatalf("reloaded list = %+v", doc)
	}
	if p := doc.Profiles[0]; p.Name != "Leo" || p.Colour != 2 {
		t.Fatalf("reloaded profile = %+v", p)
	}
}
