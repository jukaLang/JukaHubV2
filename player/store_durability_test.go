package main

// store_durability_test.go — the JSON stores outside jukauser.json.
//
// Notes, alarms and the IPTV list are the stores a user builds by hand and
// cannot regenerate, and all three used to be written with a bare
// os.WriteFile: a power cut or a full SD card truncated them, and the loaders
// then started empty without a word. These tests pin the two halves of the fix
// — the write is atomic and leaves a .bak, and a damaged store comes back from
// that .bak rather than looking deleted.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotesSaveIsAtomicAndLeavesABackup checks the write path: no .tmp is left
// behind, and the .bak holds the version that was live before the save.
func TestNotesSaveIsAtomicAndLeavesABackup(t *testing.T) {
	dir := t.TempDir()
	notes.storePath = filepath.Join(dir, "notes.json")
	notes.store = noteStore{NextID: 2}
	notes.store.Notes = []noteEntry{{ID: 1, Title: "first", Folder: "Personal"}}
	notesSave()

	notes.store.Notes = append(notes.store.Notes, noteEntry{ID: 2, Title: "second", Folder: "Ideas"})
	notesSave()

	if _, err := os.Stat(notes.storePath + ".tmp"); !os.IsNotExist(err) {
		t.Error("a temp file was left behind by the atomic save")
	}
	backup, err := os.ReadFile(notes.storePath + ".bak")
	if err != nil {
		t.Fatalf("no backup beside the notes store: %v", err)
	}
	if !strings.Contains(string(backup), `"first"`) || strings.Contains(string(backup), `"second"`) {
		t.Errorf("backup is not the previous version:\n%s", backup)
	}
}

// TestNotesLoadRecoversFromBackup is the user-visible half: a truncated
// notes.json must not read as "all notes deleted" while a good backup sits
// next to it.
func TestNotesLoadRecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	notes.storePath = filepath.Join(dir, "notes.json")
	notes.store = noteStore{NextID: 1}
	notes.store.Notes = []noteEntry{{ID: 1, Title: "keep me", Folder: "Personal"}}
	notesSave()
	// Second save makes the first version the backup.
	notesSave()

	// Simulate a power cut mid-write: the live file is half a document.
	if err := os.WriteFile(notes.storePath, []byte(`{"notes":[{"id":1,"tit`), 0644); err != nil {
		t.Fatal(err)
	}

	notes.store = noteStore{}
	notesLoad()
	if len(notes.store.Notes) != 1 || notes.store.Notes[0].Title != "keep me" {
		t.Fatalf("notes did not come back from the backup: %+v", notes.store.Notes)
	}
}

// TestNotesLoadWithoutAnyFileStartsEmpty keeps the recovery path from making a
// first run on a fresh card depend on anything: no file, no backup, empty store.
func TestNotesLoadWithoutAnyFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	notes.storePath = filepath.Join(dir, "notes.json")
	notes.store = noteStore{NextID: 7}
	if err := os.WriteFile(notes.storePath, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	notesLoad()
	if len(notes.store.Notes) != 0 {
		t.Errorf("a corrupt store with no backup produced notes: %+v", notes.store.Notes)
	}
}

// TestAlarmSaveAndLoadRoundTrip covers the alarm store end to end, including the
// recovery that keeps a saved alarm alive after a damaged write.
func TestAlarmSaveAndLoadRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	alarmStore = alarmList{}
	alarmNextID = 1
	alarmStore.Alarms = []alarmEntry{{ID: 1, Hour: 7, Minute: 30, Label: "wake"}}
	alarmSave()
	alarmSave() // the previous version becomes the backup

	alarmStore = alarmList{}
	alarmNextID = 1
	if err := os.WriteFile("jukaalarm.json", []byte(`{"alarms":[`), 0644); err != nil {
		t.Fatal(err)
	}
	alarmLoad()
	if len(alarmStore.Alarms) != 1 || alarmStore.Alarms[0].Label != "wake" {
		t.Fatalf("alarm did not survive a damaged store: %+v", alarmStore.Alarms)
	}
	if alarmNextID != 2 {
		t.Errorf("alarmNextID = %d after recovery, want 2", alarmNextID)
	}
}

// TestReadJSONWithBackupIgnoresBackupWhenStoreIsGone covers the first-run case:
// a store the user deleted ("start over") must not be resurrected from the
// backup that still sits beside it.
func TestReadJSONWithBackupIgnoresBackupWhenStoreIsGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path+".bak", []byte(`{"value":"stale"}`), 0644); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Value string `json:"value"`
	}
	recovered, err := readJSONWithBackup(path, &got)
	if err == nil {
		t.Fatal("a missing store read successfully")
	}
	if recovered || got.Value != "" {
		t.Errorf("missing store came back as %q (recovered=%v)", got.Value, recovered)
	}
}

// TestReadJSONWithBackupPrefersTheLiveFile guards the ordering: a healthy store
// must never be replaced by a stale backup.
func TestReadJSONWithBackupPrefersTheLiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"value":"live"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", []byte(`{"value":"stale"}`), 0644); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Value string `json:"value"`
	}
	recovered, err := readJSONWithBackup(path, &got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if recovered {
		t.Error("a healthy store reported a recovery")
	}
	if got.Value != "live" {
		t.Errorf("value = %q, want the live file", got.Value)
	}
}
