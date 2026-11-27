package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotesCheckboxShown(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"- [ ] buy milk", "• ☐   buy milk", true},
		{"- [x] done", "• ☑   done", true},
		{"* [X] shipped", "• ☑   shipped", true},
		{"- [y] not a task", "", false},
		{"- plain bullet", "", false},
		{"[ ] no bullet", "", false},
	}
	for _, tc := range cases {
		got, ok := notesCheckboxShown(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("notesCheckboxShown(%q) = %q,%v, want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
		if ok && len([]rune(got)) != len([]rune(tc.in)) {
			t.Fatalf("notesCheckboxShown(%q) changed the rune width: %q vs %q", tc.in, got, tc.in)
		}
	}
}

func TestNotesStoreTravelsThroughSaveSync(t *testing.T) {
	oldDir := notesDataDirOverride
	notesDataDirOverride = t.TempDir()
	t.Cleanup(func() { notesDataDirOverride = oldDir })

	oldPath, oldStore := notes.storePath, notes.store
	t.Cleanup(func() {
		notesMu.Lock()
		notes.storePath, notes.store = oldPath, oldStore
		notesMu.Unlock()
	})

	if err := os.MkdirAll(notesDataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	notes.storePath = notesStorePath()
	if notes.storePath != filepath.Join(notesDataDir(), "notes.json") {
		t.Fatalf("notes store path = %q, want %q", notes.storePath, filepath.Join(notesDataDir(), "notes.json"))
	}
	notesMu.Lock()
	notes.store = noteStore{NextID: 2, Notes: []noteEntry{{
		ID: 1, Title: "Tasks", Content: "- [ ] ship it", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}}
	notesMu.Unlock()
	notesSave()

	if _, err := os.Stat(notes.storePath); err != nil {
		t.Fatalf("notes store was not written to its sync path: %v", err)
	}
	if !hasSaveRoot(saveRootsFor("", "", ""), "JukaHub notes", notesDataDir()) {
		t.Fatalf("notes folder is not a save root: %+v", saveRootsFor("", "", ""))
	}

	man, err := ssyncBuildManifest(context.Background(), []saveRoot{{Name: "JukaHub notes", Path: notesDataDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Roots) != 1 || man.Roots[0].Key != "jukahub-notes" {
		t.Fatalf("notes roots = %+v, want the jukahub-notes key", man.Roots)
	}
	found := false
	for _, f := range man.Files {
		if f.Root == "jukahub-notes" && f.Path == "notes.json" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("notes.json is not in the save-sync manifest: %+v", man.Files)
	}
}
