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
		{"name": "Beta", "version": "1.0", "device": "trimui_smart_pro", "category": ["Themes"], "size": "1.5 MB", "changelog": ["Faster", "Smaller"], "featured": "yes"},
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
