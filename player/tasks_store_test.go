package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTasksLoadNormalize reads tasks.json the way the app does and checks
// what it starts with: the default lists on a first run, and a file with no
// version, clashing ids, a task on a list that is gone and inconsistent
// fields made safe.
func TestTasksLoadNormalize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")

	var doc tasksDoc
	if _, err := readJSONWithBackup(path, &doc); err == nil {
		t.Fatal("a missing file read without an error")
	}
	tasksNormalize(&doc)
	if doc.Version != tasksFileVersion || len(doc.Lists) != 2 || doc.Lists[0] != (taskList{ID: 1, Name: "Inbox"}) ||
		doc.Lists[1] != (taskList{ID: 2, Name: "Shopping"}) || doc.NextID != 3 || len(doc.Items) != 0 {
		t.Fatalf("first run: %+v", doc)
	}

	raw := `{"lists":[{"id":4,"name":"  Home\tjobs "},{"id":4,"name":""}],
	"items":[{"id":4,"list":4,"text":"Clash"},
		{"id":7,"list":99,"text":"Orphan\u0007 task","due":"2026-13-40","remind":true},
		{"id":8,"list":4,"text":"   "},
		{"id":9,"list":4,"text":"Call","due":"2026-11-20","alarm":true,"remind":true,"repeat":"hourly"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	doc = tasksDoc{}
	if _, err := readJSONWithBackup(path, &doc); err != nil {
		t.Fatal(err)
	}
	tasksNormalize(&doc)
	if doc.Version != tasksFileVersion || doc.NextID != 12 {
		t.Fatalf("version %d, next id %d", doc.Version, doc.NextID)
	}
	if len(doc.Lists) != 2 || doc.Lists[0] != (taskList{ID: 4, Name: "Home jobs"}) || doc.Lists[1] != (taskList{ID: 10, Name: "List"}) {
		t.Fatalf("lists: %+v", doc.Lists)
	}
	want := []taskItem{
		{ID: 11, List: 4, Text: "Clash"},
		{ID: 7, List: 4, Text: "Orphan task"},
		{ID: 9, List: 4, Text: "Call", Due: "2026-11-20", Remind: true},
	}
	if len(doc.Items) != len(want) {
		t.Fatalf("items: %+v", doc.Items)
	}
	for i := range want {
		if doc.Items[i] != want[i] {
			t.Errorf("item %d: got %+v, want %+v", i, doc.Items[i], want[i])
		}
	}
}
