package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeApp(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"label":"X","launch":"launch.sh"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("v1"), 0o644)
}

func TestPkgPublishApps(t *testing.T) {
	pkg, apps := t.TempDir(), t.TempDir()
	writeApp(t, filepath.Join(pkg, "DNSChanger"))
	writeApp(t, filepath.Join(pkg, "Apps", "One"))
	writeApp(t, filepath.Join(pkg, "Apps", "Two"))
	os.MkdirAll(filepath.Join(pkg, "Logs"), 0o755)
	os.WriteFile(filepath.Join(pkg, "Readme.txt"), []byte("hi"), 0o644)
	// A folder the user made must never be touched.
	writeApp(t, filepath.Join(apps, "Two"))

	got, err := pkgPublishApps(pkg, apps, "DNS_Changer")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("published %v, want DNSChanger and One", got)
	}
	st, err := os.Stat(filepath.Join(apps, "DNSChanger", "launch.sh"))
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("launch.sh missing or not executable: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(apps, "Two", pkgAppMarker)); err == nil {
		t.Fatal("replaced a folder this package does not own")
	}

	// An update keeps what the app wrote beside its own files.
	os.WriteFile(filepath.Join(apps, "DNSChanger", "settings.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(pkg, "DNSChanger", "data.txt"), []byte("v2"), 0o644)
	if _, err := pkgPublishApps(pkg, apps, "DNS_Changer"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(apps, "DNSChanger", "data.txt")); string(b) != "v2" {
		t.Fatalf("update did not copy: %q", b)
	}
	if _, err := os.Stat(filepath.Join(apps, "DNSChanger", "settings.json")); err != nil {
		t.Fatal("update deleted the app's own file")
	}

	pkgUnpublishApps(apps, "DNS_Changer")
	for _, n := range []string{"DNSChanger", "One"} {
		if _, err := os.Stat(filepath.Join(apps, n)); err == nil {
			t.Fatalf("%s still there after removal", n)
		}
	}
	if _, err := os.Stat(filepath.Join(apps, "Two", "config.json")); err != nil {
		t.Fatal("removal deleted a folder it does not own")
	}
}

func TestPkgFindStockAppsTopLevelAndNone(t *testing.T) {
	top := t.TempDir()
	writeApp(t, top)
	if got := pkgFindStockApps(top); len(got) != 1 || got[0] != "." {
		t.Fatalf("got %v", got)
	}
	empty := t.TempDir()
	os.MkdirAll(filepath.Join(empty, "bin"), 0o755)
	if got := pkgFindStockApps(empty); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got, err := pkgPublishApps(empty, filepath.Join(empty, "missing"), "k"); got != nil || err != nil {
		t.Fatalf("no Apps folder must be a quiet no-op, got %v %v", got, err)
	}
}
