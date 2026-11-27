package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivacyNetworkInventoryCoversEveryNetworkSurface(t *testing.T) {
	want := []string{
		"updates", "downloads", "catalog", "artwork", "weather", "browser", "media",
		"cloud", "devicesync", "romm", "retroachievements", "companion", "remoteplay",
		"steam", "smarthome", "assistant", "mail", "chat", "diagnostics", "reports",
		"netplay", "servers", "vpn", "community",
	}
	features := privacyNetworkFeatures()
	seen := map[string]bool{}
	for _, f := range features {
		if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Label) == "" ||
			strings.TrimSpace(f.Sends) == "" || strings.TrimSpace(f.Where) == "" {
			t.Errorf("feature %+v lacks an id, label, payload or destination", f)
		}
		if seen[f.ID] {
			t.Errorf("feature %q is listed twice", f.ID)
		}
		seen[f.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("the privacy page does not name the %q network feature", id)
		}
	}

	rows := privacyNetworkRows()
	if len(rows) != len(features) {
		t.Fatalf("%d network rows for %d features", len(rows), len(features))
	}
	weather := &Config{Variables: Variables{Custom: map[string]interface{}{weatherEnabledKey: false}}}
	for _, row := range rows {
		if row.Kind != setKindInfo || row.Value == nil || row.Detail == nil {
			t.Errorf("network row %q is not a read-out", row.ID)
		}
		if !strings.Contains(row.Detail(weather), " · ") {
			t.Errorf("network row %q does not say what it sends and where", row.ID)
		}
		if strings.TrimSpace(row.Value(weather)) == "" {
			t.Errorf("network row %q has no on/off answer", row.ID)
		}
	}
	if got := privacyOnOff(weatherEnabledFor(weather)); got != "Off" {
		t.Fatalf("weather off reads %q", got)
	}
	weather.Variables.Custom[weatherEnabledKey] = true
	if got := privacyOnOff(weatherEnabledFor(weather)); got != "On" {
		t.Fatalf("weather on reads %q", got)
	}

	// The rows are actually on the Settings → Privacy & data page, with the
	// export action beside them.
	idx := setSectionIndex("privacy")
	if idx < 0 {
		t.Fatal("there is no Privacy & data section")
	}
	sectionRows := setSectionRows(&Config{}, setSections()[idx])
	foundExport, foundNetwork := false, false
	for _, row := range sectionRows {
		switch row.ID {
		case "exportData":
			foundExport = row.Kind == setKindAction && row.Open != nil
		case "net_weather":
			foundNetwork = true
		}
	}
	if !foundExport {
		t.Error("Privacy & data has no working Export my data action")
	}
	if !foundNetwork {
		t.Error("Privacy & data does not list the network inventory")
	}
}

func TestPrivacyExportDataWritesTheUserFiles(t *testing.T) {
	root := bkupTestPlaces(t)
	files := map[string]string{
		"app/jukauser.json":            `{"variables": {"weatherUnit": "F"}}`,
		"app/notes.json":               `{"notes": [{"title": "Trip"}]}`,
		"app/plugins/demo/plugin.json": `{"title": "Demo"}`,
		"app/plugins/demo/run.sh":      "#!/bin/sh\necho hi\n",
		"data/vk-words.json":           `{"words": []}`,
		"saves/RetroArch/game.srm":     "SAVE",
		"patch/db/db.json":             `{"installed": []}`,
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, m, err := privacyExportData(context.Background(), filepath.Join(root, "out"), "JukaHub data test.zip", []byte(`{"schemaVersion": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".zip") || !strings.HasPrefix(filepath.Base(path), "JukaHub data test") {
		t.Fatalf("export named %s, want a .zip called JukaHub data test", filepath.Base(path))
	}
	if len(m.Categories) != len(bkupCategoryOrder) {
		t.Fatalf("export categories = %v, want every category", m.Categories)
	}
	verified, err := bkupVerify(path)
	if err != nil {
		t.Fatalf("the exported zip does not verify: %v", err)
	}
	targets := map[string]bool{}
	for _, f := range verified.Files {
		targets[f.Target] = true
	}
	for _, want := range []string{
		"app:jukauser.json", "app:notes.json", "plugins:demo/plugin.json", "plugins:demo/run.sh",
		"data:vk-words.json", "saves/retroarch:game.srm", "patch:db/db.json",
	} {
		if !targets[want] {
			t.Errorf("the data export leaves out %s", want)
		}
	}
}
