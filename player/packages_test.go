package main

// packages_test.go — the Packages store's trust edges: version ordering, a
// catalog parser that survives a malformed entry, folder names that cannot
// leave the packages folder, and downloads that refuse an error page.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Model ────────────────────────────────────────────────────────────────

func TestPkgCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.0", "1.0", 1},
		{"1.10", "1.9", 1},
		{"v2.0", "2.0.0", 0},
		{"2024.9.13.1", "2024.9.13", 1},
		{"1.0", "1.0-beta", 1},
		{"1.0-beta", "1.0-alpha", 1},
		{"", "", 0},
		{"1.2.3", "1.2.4", -1},
	}
	for _, c := range cases {
		if got := pkgCompareVersions(c.a, c.b); got != c.want {
			t.Errorf("pkgCompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// One malformed entry no longer empties the store: a field of the wrong type
// is zeroed, an entry with no name or a duplicate is dropped, and the optional
// store metadata is read whatever shape it comes in.
func TestPkgParseCatalogIsLenient(t *testing.T) {
	raw := `[
		{"name": "Beta", "version": "1.0", "device": "trimui_smart_pro", "category": ["Themes"], "icon": "https://example.com/beta.png", "size": "1.5 MB", "changelog": ["Faster", "Smaller"], "featured": "yes"},
		{"name": "alpha", "version": 2, "size": 2048},
		{"name": ""},
		"not an object",
		{"name": "Beta", "version": "9.9"},
		{"name": "Gamma", "homepage": "https://example.com/gamma"}
	]`
	entries, err := pkgParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("%d entries, want alpha, Beta and Gamma", len(entries))
	}
	if entries[0].Pkg.Name != "alpha" || entries[1].Pkg.Name != "Beta" || entries[2].Pkg.Name != "Gamma" {
		t.Fatalf("entries not sorted by name: %q %q %q", entries[0].Pkg.Name, entries[1].Pkg.Name, entries[2].Pkg.Name)
	}
	beta := entries[1]
	if beta.Pkg.Version != "1.0" {
		t.Errorf("the first Beta should win over the duplicate, got version %q", beta.Pkg.Version)
	}
	if beta.Pkg.Device != nil {
		t.Errorf("a string where an array belongs should be zeroed, got %v", beta.Pkg.Device)
	}
	if beta.Extra.Category != "Themes" || beta.Extra.Size != 1572864 || !beta.Extra.Featured {
		t.Errorf("extras not read: %+v", beta.Extra)
	}
	if beta.Extra.Changelog != "• Faster\n• Smaller" {
		t.Errorf("changelog = %q", beta.Extra.Changelog)
	}
	if beta.Extra.Icon != "https://example.com/beta.png" {
		t.Errorf("icon = %q", beta.Extra.Icon)
	}
	if entries[0].Extra.Size != 2048 || entries[0].Pkg.Version != "" {
		t.Errorf("alpha: size %d version %q", entries[0].Extra.Size, entries[0].Pkg.Version)
	}
	if entries[2].Extra.Homepage != "https://example.com/gamma" {
		t.Errorf("homepage = %q", entries[2].Extra.Homepage)
	}
	if _, err := pkgParseCatalog([]byte(`{"oops": true}`)); err == nil {
		t.Error("a registry that is not a list must be an error")
	}
}

// A registry entry can never name a folder outside the packages folder.
func TestPackageDirNameCannotLeaveTheFolder(t *testing.T) {
	for _, name := range []string{"..", ".", "../etc", `a\b`, "a/b", ".hidden", "", "x:y*z"} {
		got := packageDirName(name)
		if got == "" || got == "." || got == ".." || strings.HasPrefix(got, ".") || strings.ContainsAny(got, `/\:*`) {
			t.Errorf("packageDirName(%q) = %q", name, got)
		}
		if filepath.Base(got) != got {
			t.Errorf("packageDirName(%q) = %q is not a single folder name", name, got)
		}
	}
	if got := packageDirName("Backup Config Files"); got != "Backup_Config_Files" {
		t.Errorf("an ordinary name changed folder: %q", got)
	}
	if err := pkgRemoveDir("../x"); err == nil {
		t.Error("pkgRemoveDir accepted a path")
	}
}

// Run (pkglaunch.go) starts only the installed folder's own program: an app
// kept under Apps/<Name>/ is found through its config.json, a copy in the
// store's staging, rollback or trash folder (or a loose file in the packages
// folder) is never launchable, a folder with no program offers no Run, and a
// bundle of apps offers each one on the Apps screen instead of guessing.
func TestPkgRunStaysInTheInstalledFolder(t *testing.T) {
	root := t.TempDir()
	old, oldScripts := pkgInstallDirOverride, pkgRunScripts
	pkgInstallDirOverride, pkgRunScripts = root, true
	t.Cleanup(func() { pkgInstallDirOverride, pkgRunScripts = old, oldScripts })
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("Blips/Apps/Blips/config.json", `{"label":"Blips","launch":"launch.sh"}`)
	write("Blips/Apps/Blips/launch.sh", "#!/bin/sh\n")
	write("Blips/Apps/Blips/lib/launch.sh", "#!/bin/sh\n")
	write("Bundle/Apps/One/config.json", `{"label":"One"}`)
	write("Bundle/Apps/One/launch.sh", "#!/bin/sh\n")
	write("Bundle/Apps/Two/config.json", `{"label":"Two"}`)
	write("Bundle/Apps/Two/launch.sh", "#!/bin/sh\n")
	write("Docs/README.txt", "")
	write("loose.sh", "#!/bin/sh\n")

	want := filepath.Join(root, "Blips", "Apps", "Blips", "launch.sh")
	if !pkgLaunchablePath(want) {
		t.Fatal("an installed package's program was refused")
	}
	for _, dot := range []string{pkgStagingDir, pkgRollbackDir, pkgTrashDir} {
		if p := filepath.Join(root, dot, "Blips", "launch.sh"); pkgLaunchablePath(p) {
			t.Errorf("%q in the store's own folder was launchable", p)
		}
	}
	if pkgLaunchablePath(root) || pkgLaunchablePath(filepath.Join(root, "loose.sh")) ||
		pkgLaunchablePath(filepath.Join(root, "..", "evil.sh")) {
		t.Error("the packages folder, a loose file in it or a path out of it was launchable")
	}
	if _, ok := pkgFindInstalledLaunch(filepath.Join(root, "Docs"), "Docs"); ok {
		t.Error("a folder with no program offered Run")
	}
	if _, ok := pkgFindInstalledLaunch(filepath.Join(root, "Bundle"), "Bundle"); ok {
		t.Error("a bundle of apps offered Run for one of them")
	}
	got, ok := pkgFindInstalledLaunch(filepath.Join(root, "Blips"), "Blips")
	if !pkgRunnableHere(want) {
		// Windows: a TrimUI app's launch.sh starts arm64 Linux programs.
		if ok {
			t.Errorf("Run offered %q on this system", got.Path)
		}
		return
	}
	if !ok || got.Path != want {
		t.Fatalf("Run starts %q (ok %v), want %q", got.Path, ok, want)
	}
	var names []string
	for _, e := range pkgAppEntries() {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "Blips,One,Two" {
		t.Errorf("Apps rows = %v, want Blips, One, Two", names)
	}
}

// ── Download ─────────────────────────────────────────────────────────────

// pkgDownloadTo refuses an error page instead of saving it as the package, and
// streams a real one to the file it was given.
func TestPkgDownloadRefusesErrorPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("PK\x03\x04payload"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "pkg.download")
	if _, err := pkgDownloadTo(context.Background(), srv.URL+"/missing.zip", dest, Package{}, nil); err == nil {
		t.Fatal("a 404 page was accepted as the package")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a refused download left a file behind: %v", err)
	}
	res, err := pkgDownloadTo(context.Background(), srv.URL+"/ok.zip", dest, Package{}, nil)
	data, _ := os.ReadFile(dest)
	if err != nil || string(data) != "PK\x03\x04payload" || !res.Zip || res.Verified {
		t.Fatalf("download = %q, %+v, %v", data, res, err)
	}
}
