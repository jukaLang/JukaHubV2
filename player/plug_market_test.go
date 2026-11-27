package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginCatalogScanInstallAndTrustLines(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	plugins := filepath.Join(root, "plugins")
	source := filepath.Join(catalog, "clock")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"clock","title":"Clock","version":"1.0","author":"Somebody","description":"Shows the time","entry":"run.sh","enabled":false}`
	files := map[string]string{
		filepath.Join(source, "plugin.json"):            manifest,
		filepath.Join(source, "run.sh"):                 "#!/bin/sh\necho tick\n",
		filepath.Join(catalog, "broken", "plugin.json"): "{not json",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldDir, oldPlug := plugMarketDir, pluginDir
	plugMarketDir = func() string { return catalog }
	pluginDir = plugins
	defer func() { plugMarketDir, pluginDir = oldDir, oldPlug }()

	items := plugMarketScan()
	if len(items) != 2 {
		t.Fatalf("catalog = %d entries, want 2", len(items))
	}
	var clock bool
	for _, item := range items {
		if item.ID == "clock" {
			clock = true
			if item.Title != "Clock" || item.Version != "1.0" || item.Problem != "" || item.Installed {
				t.Fatalf("clock = %+v", item)
			}
		}
		if item.ID == "broken" && item.Problem == "" {
			t.Fatal("a broken manifest was not reported")
		}
	}
	if !clock {
		t.Fatalf("clock not found: %+v", items)
	}
	lines := strings.Join(plugMarketLines(items, 0), "\n")
	for _, want := range []string{"not sandboxed", "Nothing here is downloaded", "Clock v1.0"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("card has no %q:\n%s", want, lines)
		}
	}

	if err := plugMarketInstall("clock"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plugins, "clock", "run.sh")); err != nil {
		t.Fatalf("installed entry missing: %v", err)
	}
	if err := plugMarketInstall("clock"); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("second install = %v, want already installed", err)
	}
	if err := plugMarketInstall("../escape"); err == nil {
		t.Fatal("a path in an id was accepted")
	}
	after := plugMarketScan()
	found := false
	for _, item := range after {
		if item.ID == "clock" {
			found = item.Installed
		}
	}
	if !found {
		t.Fatalf("installed plugin is not marked installed: %+v", after)
	}
}
