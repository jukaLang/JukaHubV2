package main

// unzip_bounds_test.go covers the File Explorer unzip: an archive the user
// picked is not signed or checked by anything, so the entry count and the
// declared expansion are the only things standing between it and the card.

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUnzipTestArchive(t *testing.T, path string, fill func(zw *zip.Writer)) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	fill(zw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// unzipTestDest is where unzipFileBounded puts the tree: beside the archive,
// with the extension replaced by "_unzipped".
func unzipTestDest(archive string) string {
	return strings.TrimSuffix(archive, filepath.Ext(archive)) + "_unzipped"
}

func TestUnzipFileBoundedRefusesTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "many.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	})

	if err := unzipFileBounded(archive, 3, 1<<20); err == nil {
		t.Fatal("an archive over the entry limit must be refused")
	}
	if _, err := os.Stat(unzipTestDest(archive)); err == nil {
		t.Fatal("a refused archive still created its folder")
	}
}

func TestUnzipFileBoundedRefusesArchiveThatExpandsPastCap(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "big.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"a.bin", "b.bin"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(make([]byte, 600<<10)); err != nil {
				t.Fatal(err)
			}
		}
	})

	if err := unzipFileBounded(archive, 100, 1<<20); err == nil {
		t.Fatal("an archive that expands past the cap must be refused")
	}
	if _, err := os.Stat(filepath.Join(unzipTestDest(archive), "b.bin")); err == nil {
		t.Fatal("an entry over the cap was written")
	}
}

func TestUnzipFileBoundedSkipsSymlinkEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "links.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		hdr := &zip.FileHeader{Name: "escape", Method: zip.Store}
		hdr.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		// A link entry carries its target as content, not file data.
		if _, err := w.Write([]byte("../outside")); err != nil {
			t.Fatal(err)
		}
		real, err := zw.Create("real.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := real.Write([]byte("kept")); err != nil {
			t.Fatal(err)
		}
	})

	if err := unzipFileBounded(archive, 100, 1<<20); err != nil {
		t.Fatalf("a zip with a link entry should unpack its regular files: %v", err)
	}
	dest := unzipTestDest(archive)
	if _, err := os.Stat(filepath.Join(dest, "escape")); err == nil {
		t.Fatal("a link entry was written as a file")
	}
	body, err := os.ReadFile(filepath.Join(dest, "real.txt"))
	if err != nil {
		t.Fatalf("regular entry missing: %v", err)
	}
	if string(body) != "kept" {
		t.Fatalf("regular entry content = %q, want %q", body, "kept")
	}
}

func TestUnzipFileBoundedStillRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "slip.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		w, err := zw.Create("../escape.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("boom")); err != nil {
			t.Fatal(err)
		}
	})

	if err := unzipFileBounded(archive, 100, 1<<20); err == nil {
		t.Fatal("a traversal entry must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("traversal file was written")
	}
}

// An experience pack is a zip someone else made: every entry name must stay
// inside the pack, whatever prefix or separator it was written with.
func TestXpackZipEntryNamesStayInsideThePack(t *testing.T) {
	cases := []struct {
		name, prefix, want string
		bad                bool
	}{
		{"pack.json", "", "pack.json", false},
		{"art/wall.png", "", "art/wall.png", false},
		{"mypack/art/wall.png", "mypack/", "art/wall.png", false},
		{"other/file.txt", "mypack/", "", false},
		{"art/", "", "", false},
		{"../evil", "", "", true},
		{"/etc/passwd", "", "", true},
		{`a\..\b`, "", "", true},
		{"C:/x", "", "", true},
		{"a/../../b", "", "", true},
	}
	for _, c := range cases {
		got, err := xpackZipEntryName(c.name, c.prefix)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("xpackZipEntryName(%q, %q) = %q, %v; want %q, refused %v", c.name, c.prefix, got, err, c.want, c.bad)
		}
	}
}
