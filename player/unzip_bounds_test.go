package main

// unzip_bounds_test.go covers the bounds of the Files extraction
// (files_extract.go): an archive the user picked is not signed or checked by
// anything, so the entry count, the space it unpacks to and its names are what
// stand between it and the card. Every refusal must leave the folder exactly
// as it was — no output and no staging folder.

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
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

// unzipTestRun extracts archive beside itself the way the queue does.
func unzipTestRun(archive string, mode feExtractMode) feExtractOutcome {
	return feExtractRun(context.Background(), feExtractJob{Archive: archive, Dest: filepath.Dir(archive), Mode: mode}, nil)
}

// unzipTestNames lists dir, sorted.
func unzipTestNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// unzipTestOnly fails unless dir holds exactly want.
func unzipTestOnly(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := unzipTestNames(t, dir)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s holds %q, want %q", dir, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s holds %q, want %q", dir, got, want)
		}
	}
}

func TestUnzipRefusesTooManyEntries(t *testing.T) {
	prev := feExtractMaxEntries
	feExtractMaxEntries = 3
	t.Cleanup(func() { feExtractMaxEntries = prev })

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

	if out := unzipTestRun(archive, feExtractSmart); out.Err == nil {
		t.Fatal("an archive over the entry limit must be refused")
	}
	unzipTestOnly(t, dir, "many.zip")
}

func TestUnzipRefusesArchiveThatDoesNotFit(t *testing.T) {
	prev := feExtractFreeSpace
	feExtractFreeSpace = func(string) (int64, error) { return feExtractSpaceMargin + 1<<20, nil }
	t.Cleanup(func() { feExtractFreeSpace = prev })

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

	out := unzipTestRun(archive, feExtractSmart)
	var space feNoSpaceError
	if !errors.As(out.Err, &space) {
		t.Fatalf("an archive bigger than the free space must be refused, got %v", out.Err)
	}
	unzipTestOnly(t, dir, "big.zip")
}

func TestUnzipSkipsSymlinkEntries(t *testing.T) {
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

	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatalf("a zip with a link entry should unpack its regular files: %v", out.Err)
	}
	if out.Stats.Skips.Links != 1 {
		t.Fatalf("skipped links = %d, want 1", out.Stats.Skips.Links)
	}
	// One regular file is one top-level item: it lands beside the archive.
	unzipTestOnly(t, dir, "links.zip", "real.txt")
	body, err := os.ReadFile(filepath.Join(dir, "real.txt"))
	if err != nil {
		t.Fatalf("regular entry missing: %v", err)
	}
	if string(body) != "kept" {
		t.Fatalf("regular entry content = %q, want %q", body, "kept")
	}
}

func TestUnzipNeverWritesOutsideTheFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "slip.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"../escape.txt", "../../escape2.txt", "/abs.txt", "ok.txt"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("boom")); err != nil {
				t.Fatal(err)
			}
		}
	})

	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatalf("the safe entries should still unpack: %v", out.Err)
	}
	if out.Stats.Skips.Unsafe != 2 {
		t.Fatalf("unsafe entries skipped = %d, want 2", out.Stats.Skips.Unsafe)
	}
	for _, escaped := range []string{filepath.Join(root, "escape.txt"), filepath.Join(filepath.Dir(root), "escape2.txt")} {
		if _, err := os.Stat(escaped); err == nil {
			t.Fatalf("traversal file %s was written", escaped)
		}
	}
	// "/abs.txt" loses its slash and lands inside, beside ok.txt, so the two
	// top-level items go into a folder named after the archive.
	unzipTestOnly(t, root, "inner")
	unzipTestOnly(t, dir, "slip", "slip.zip")
	unzipTestOnly(t, filepath.Join(dir, "slip"), "abs.txt", "ok.txt")
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
