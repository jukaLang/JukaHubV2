package main

import (
	"archive/zip"
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"jukahub/JukaHub/internal/jdelta"
)

// releaseFile is one file of a test release.
type releaseFile struct {
	name string
	data []byte
}

func randomData(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}

// writeReleaseZip writes files under the JukaHub-Trimui/ folder the release
// workflow wraps its archive in.
func writeReleaseZip(t *testing.T, path string, files []releaseFile) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, file := range files {
		w, err := zw.Create("JukaHub-Trimui/" + file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeReleaseDir writes an unpacked release.
func writeReleaseDir(t *testing.T, dir string, files []releaseFile) {
	t.Helper()
	for _, file := range files {
		p := filepath.Join(dir, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, file.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRunBuildsAVerifiedDelta is the release step end to end: two archives in,
// one small, correctly named bundle out, with the binary patched and the JSON
// shipped whole.
func TestRunBuildsAVerifiedDelta(t *testing.T) {
	oldBin := randomData(1, 300<<10)
	newBin := append(append(append([]byte(nil), oldBin[:100000]...), randomData(2, 4000)...), oldBin[100000:]...)
	dir := t.TempDir()
	oldZip := filepath.Join(dir, "old", "JukaHub-Trimui.zip")
	newZip := filepath.Join(dir, "new", "JukaHub-Trimui.zip")
	writeReleaseZip(t, oldZip, []releaseFile{
		{"JukaHub", oldBin},
		{"jukaconfig.json", []byte(`{"Version":"0.4.0"}`)},
		{"release-info.json", []byte(`{"tag":"JukaHub-20261017120000"}`)},
		{"required/yt-dlp", []byte("a tool the update never replaces")},
	})
	writeReleaseZip(t, newZip, []releaseFile{
		{"JukaHub", newBin},
		{"jukaconfig.json", []byte(`{"Version":"0.5.0"}`)},
		{"Inter-Regular.ttf", randomData(3, 2000)},
		{"release-info.json", []byte(`{"tag":"JukaHub-20261101120000"}`)},
		{"required/yt-dlp", []byte("a tool the update never replaces")},
	})
	outDir := filepath.Join(dir, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"-old", oldZip, "-new", newZip, "-out", outDir}, &stdout); err != nil {
		t.Fatalf("run: %v\n%s", err, stdout.String())
	}
	bundle := filepath.Join(outDir, "JukaHub-Trimui-20261017120000.jkdelta")
	b, err := jdelta.ReadBundleFile(bundle)
	if err != nil {
		t.Fatalf("the delta was not written where the updater looks: %v", err)
	}
	if b.FromTag != "JukaHub-20261017120000" || b.ToTag != "JukaHub-20261101120000" || b.Platform != "Trimui" {
		t.Fatalf("bundle metadata = %+v", b)
	}
	want := []struct {
		name, base string
	}{
		{"JukaHub", jdelta.BaseInstalled},
		{"jukaconfig.json", jdelta.BaseNone},
		{"Inter-Regular.ttf", jdelta.BaseNone},
		{"release-info.json", jdelta.BaseNone},
	}
	if len(b.Files) != len(want) {
		t.Fatalf("the bundle carries %d files, want %d (required/ is never part of an update)", len(b.Files), len(want))
	}
	for _, w := range want {
		found := false
		for _, f := range b.Files {
			if f.Name == w.name {
				found = true
				if f.Base != w.base {
					t.Errorf("%s ships as %q, want %q", w.name, f.Base, w.base)
				}
			}
		}
		if !found {
			t.Errorf("the bundle has no %s", w.name)
		}
	}
	if info, err := os.Stat(bundle); err != nil || info.Size() > 64<<10 {
		t.Errorf("a 4 KiB change made a bundle of %v bytes (%v)", info, err)
	}
}

// Unpacked folders work too, and the flags override what release-info.json
// says.
func TestRunAcceptsFoldersAndOverrides(t *testing.T) {
	dir := t.TempDir()
	oldBin := randomData(4, 100<<10)
	newBin := append(append([]byte(nil), oldBin...), randomData(5, 300)...)
	writeReleaseDir(t, filepath.Join(dir, "old"), []releaseFile{{"JukaHub.exe", oldBin}})
	writeReleaseDir(t, filepath.Join(dir, "new"), []releaseFile{{"JukaHub.exe", newBin}})
	out := filepath.Join(dir, "custom.jkdelta")
	var stdout bytes.Buffer
	args := []string{
		"-old", filepath.Join(dir, "old"), "-new", filepath.Join(dir, "new"),
		"-platform", "Windows", "-from-tag", "JukaHub-1", "-to-tag", "JukaHub-2", "-out", out,
	}
	if err := run(args, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, err := jdelta.ReadBundleFile(out)
	if err != nil {
		t.Fatalf("no bundle at -out: %v", err)
	}
	if b.Platform != "Windows" || b.FromTag != "JukaHub-1" || b.ToTag != "JukaHub-2" {
		t.Fatalf("bundle metadata = %+v", b)
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join(dir, "rel")
	writeReleaseDir(t, rel, []releaseFile{
		{"JukaHub", randomData(6, 1000)},
		{"release-info.json", []byte(`{"tag":"JukaHub-20261017120000"}`)},
	})
	empty := filepath.Join(dir, "empty")
	writeReleaseDir(t, empty, []releaseFile{{"readme.txt", []byte("no executable here")}})
	cases := []struct {
		name string
		args []string
	}{
		{"no old release", []string{"-new", rel}},
		{"same release", []string{"-old", rel, "-new", rel, "-platform", "Trimui"}},
		{"missing archive", []string{"-old", filepath.Join(dir, "nope.zip"), "-new", rel, "-platform", "Trimui"}},
		{"no platform", []string{"-old", rel, "-new", rel, "-to-tag", "JukaHub-2"}},
		{"no executable", []string{"-old", empty, "-new", rel, "-platform", "Trimui"}},
	}
	for _, tc := range cases {
		var stdout bytes.Buffer
		if err := run(append(tc.args, "-out", filepath.Join(dir, "out.jkdelta")), &stdout); err == nil {
			t.Errorf("%s: run accepted it", tc.name)
		}
	}
}
