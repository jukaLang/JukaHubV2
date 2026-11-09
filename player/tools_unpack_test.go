package main

// Tests for the in-process tool unpacker and the FFmpeg bundle install policy.
// The handheld bundle is a .tar.xz that the TrimUI firmware cannot unpack with
// its own tools, which made "Could not install ffplay" permanent on a Brick, so
// the decoder, the damaged-archive classification, the archive path and link
// safety and the leftover cleanup are pinned here without any network access.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

// randomBytes returns n deterministic bytes that do not compress, so archive
// sizes in the tests stay close to their content sizes.
func randomBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// buildToolTarXZ writes a .tar.xz at path holding headers in order. Regular
// files take their body from bodies; other entry types are written as given.
func buildToolTarXZ(t *testing.T, path string, headers []*tar.Header, bodies map[string][]byte) {
	t.Helper()
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for _, hdr := range headers {
		body := bodies[hdr.Name]
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xw.Write(data.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExtractToolFilesTarXZ unpacks a BtbN-shaped handheld bundle with the
// built-in decoder: only the wanted binaries land in the tools folder, a link
// that shares a wanted name is never materialised, and progress is reported
// against the archive's size.
func TestExtractToolFilesTarXZ(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg-master-latest-linuxarm64-gpl.tar.xz")
	const root = "ffmpeg-master-latest-linuxarm64-gpl/"
	bodies := map[string][]byte{}
	bodies[root+"doc/ffmpeg.html"] = []byte("<html>docs</html>")
	bodies[root+"bin/ffplay"] = randomBytes(1, 200<<10)
	bodies[root+"bin/ffmpeg"] = append([]byte("\x7fELF-ffmpeg"), randomBytes(2, 64<<10)...)
	bodies[root+"bin/ffprobe"] = append([]byte("\x7fELF-ffprobe"), randomBytes(3, 64<<10)...)
	bodies[root+"LICENSE.txt"] = []byte("GPL")
	buildToolTarXZ(t, archive, []*tar.Header{
		{Name: root, Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: root + "doc/ffmpeg.html", Typeflag: tar.TypeReg, Mode: 0o644},
		// A symlink called ffmpeg must not stand in for the real binary.
		{Name: root + "lib/ffmpeg", Typeflag: tar.TypeSymlink, Linkname: "../bin/ffmpeg", Mode: 0o777},
		{Name: root + "bin/ffplay", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: root + "bin/ffmpeg", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: root + "bin/ffprobe", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: root + "LICENSE.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, bodies)
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	var lastDone, lastTotal int64
	found, err := extractToolFilesCtx(context.Background(), archive, out, []string{"ffmpeg", "ffprobe"}, func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("extractToolFilesCtx(tar.xz): %v", err)
	}
	if len(found) != 2 || found[0] != "ffmpeg" || found[1] != "ffprobe" {
		t.Fatalf("found = %v, want [ffmpeg ffprobe]", found)
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("%s not installed: %v", name, err)
		}
		if !bytes.Equal(data, bodies[root+"bin/"+name]) {
			t.Errorf("%s content differs from the archive (%d bytes, want %d)", name, len(data), len(bodies[root+"bin/"+name]))
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "ffmpeg" && e.Name() != "ffprobe" {
			t.Errorf("unexpected %q left in the tools folder (ffplay, docs and the stage must not be)", e.Name())
		}
	}
	if lastTotal != info.Size() {
		t.Errorf("progress total = %d, want the archive size %d", lastTotal, info.Size())
	}
	if lastDone <= 0 || lastDone > lastTotal {
		t.Errorf("progress done = %d, want within (0, %d]", lastDone, lastTotal)
	}
}

// TestExtractToolFilesTarXZDamaged: a download that is not an xz stream, or
// one cut off in the middle of a binary, is reported as damaged (so the caller
// deletes it) and leaves nothing half-written behind.
func TestExtractToolFilesTarXZDamaged(t *testing.T) {
	dir := t.TempDir()

	garbage := filepath.Join(dir, "garbage.tar.xz")
	if err := os.WriteFile(garbage, []byte("this is not an xz stream at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := extractToolFiles(garbage, out, []string{"ffmpeg"}); !errors.Is(err, errBadToolArchive) {
		t.Fatalf("garbage archive: err = %v, want errBadToolArchive", err)
	}

	whole := filepath.Join(dir, "whole.tar.xz")
	buildToolTarXZ(t, whole, []*tar.Header{
		{Name: "pkg/bin/ffmpeg", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string][]byte{"pkg/bin/ffmpeg": randomBytes(4, 256<<10)})
	data, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(dir, "cut.tar.xz")
	if err := os.WriteFile(cut, data[:len(data)*6/10], 0o644); err != nil {
		t.Fatal(err)
	}
	out = t.TempDir()
	if _, err := extractToolFiles(cut, out, []string{"ffmpeg"}); !errors.Is(err, errBadToolArchive) {
		t.Fatalf("truncated archive: err = %v, want errBadToolArchive", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("a damaged unpack left %d entries in the tools folder", len(entries))
	}
}

// Unknown formats are refused instead of being unpacked by an external tar,
// which cannot enforce our regular-file-only extraction policy.
func TestExtractToolFilesRefusesUnsupportedArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bundle.unknown")
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractToolFiles(archive, t.TempDir(), []string{"ffmpeg"}); !errors.Is(err, errBadToolArchive) {
		t.Fatalf("unsupported archive: %v, want errBadToolArchive", err)
	}
}

// TestFFmpegBundleWanted: an install unpacks ffmpeg and ffprobe but never
// ffplay, and a tool already installed is never replaced.
func TestFFmpegBundleWanted(t *testing.T) {
	dir := t.TempDir()
	same := func(got, want []string) bool {
		return strings.Join(got, ",") == strings.Join(want, ",")
	}
	if got := ffmpegBundleWanted(dir, "ffmpeg", "linux"); !same(got, []string{"ffmpeg", "ffprobe"}) {
		t.Errorf("ffmpeg install unpacks %v, want [ffmpeg ffprobe]", got)
	}
	if got := ffmpegBundleWanted(dir, "ffprobe", "linux"); !same(got, []string{"ffprobe", "ffmpeg"}) {
		t.Errorf("ffprobe install unpacks %v, want [ffprobe ffmpeg]", got)
	}
	if got := ffmpegBundleWanted(dir, "ffmpeg.exe", "windows"); !same(got, []string{"ffmpeg.exe", "ffprobe.exe"}) {
		t.Errorf("windows install unpacks %v, want [ffmpeg.exe ffprobe.exe]", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ffmpegBundleWanted(dir, "ffprobe", "linux"); !same(got, []string{"ffprobe"}) {
		t.Errorf("with ffmpeg present the install unpacks %v, want [ffprobe]", got)
	}
}

// TestPurgeStaleToolScratch: leftovers of a killed install are removed, while
// fresh ones (an install running right now), the kept bundle and the tools
// themselves stay.
func TestPurgeStaleToolScratch(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	mk := func(name string, isDir bool, mtime time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if isDir {
			if err := os.MkdirAll(filepath.Join(p, "inner"), 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk(".tool-stage-111", true, old)
	mk(".ffmpeg-download-222.tar.xz", false, old)
	mk(".yt-dlp-333.part", false, old)
	mk(".tool-stage-444", true, time.Now())
	mk(".ffmpeg-bundle.tar.xz", false, old)
	mk("ffmpeg", false, old)

	purgeStaleToolScratch(dir, time.Now())

	for _, name := range []string{".tool-stage-111", ".ffmpeg-download-222.tar.xz", ".yt-dlp-333.part"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("leftover %s should have been removed (stat err %v)", name, err)
		}
	}
	for _, name := range []string{".tool-stage-444", ".ffmpeg-bundle.tar.xz", "ffmpeg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s must be kept: %v", name, err)
		}
	}
}

func TestExtractToolFilesDoesNotFollowArchivePathsOrLinks(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bundle.tar.xz")
	buildToolTarXZ(t, archive, []*tar.Header{
		{Name: "../escape/ffmpeg", Typeflag: tar.TypeSymlink, Linkname: "../../../victim"},
		{Name: "../../ffmpeg", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string][]byte{"../../ffmpeg": []byte("binary")})
	out := filepath.Join(dir, "out")
	_, err := extractToolFiles(archive, out, []string{"ffmpeg"})
	if !errors.Is(err, errBadToolArchive) {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ffmpeg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive wrote outside staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "ffmpeg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive installed after rejection: %v", err)
	}
}

func TestExtractToolFilesZipSkipsSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bundle.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		h := &zip.FileHeader{Name: "link/ffmpeg", Method: zip.Store}
		h.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("../../victim")); err != nil {
			t.Fatal(err)
		}
		w, err = zw.Create("bin/ffmpeg")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("binary")); err != nil {
			t.Fatal(err)
		}
	})
	out := t.TempDir()
	if _, err := extractToolFiles(archive, out, []string{"ffmpeg"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "ffmpeg"))
	if err != nil || string(got) != "binary" {
		t.Fatalf("installed %q: %v", got, err)
	}
}

func TestUnsafeArchiveName(t *testing.T) {
	for _, name := range []string{"../evil", "pkg/../../evil", `..\evil`, `pkg\..\..\evil`, "/etc/passwd", `C:\Windows\System32\evil.dll`, "C:ffmpeg", "/..", "pkg/.."} {
		if !unsafeArchiveName(name) {
			t.Errorf("unsafeArchiveName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "ffmpeg", "bin/ffmpeg", "ffmpeg-7.1-linuxarm64/bin/ffprobe", "a..b/ffmpeg", "bin/..ffmpeg", "pkg/C:ffmpeg"} {
		if unsafeArchiveName(name) {
			t.Errorf("unsafeArchiveName(%q) = true", name)
		}
	}
}
