package main

// Tests for the in-process tool unpacker and the FFmpeg bundle install policy.
// The handheld bundle is a .tar.xz that the TrimUI firmware cannot unpack with
// its own tools, which made "Could not install ffplay" permanent on a Brick, so
// the decoder, the damaged-archive classification, the download kept for a
// retry and the leftover cleanup are pinned here without any network access.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
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

// buildToolZip writes a zip at path holding name -> body.
func buildToolZip(t *testing.T, path string, bodies map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range bodies {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
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

// TestExtractToolFilesCancelled: a cancel stops the unpack with the context's
// error, not a damaged-archive verdict that would throw the download away.
func TestExtractToolFilesCancelled(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bundle.tar.xz")
	buildToolTarXZ(t, archive, []*tar.Header{
		{Name: "pkg/bin/ffmpeg", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string][]byte{"pkg/bin/ffmpeg": randomBytes(5, 64<<10)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := t.TempDir()
	_, err := extractToolFilesCtx(ctx, archive, out, []string{"ffmpeg"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, errBadToolArchive) {
		t.Fatal("a cancelled unpack must not call the archive damaged")
	}
}

// TestPickReleaseAssetPrefersOrder: the wanted names are a preference order,
// whatever order the release lists its files in.
func TestPickReleaseAssetPrefersOrder(t *testing.T) {
	assets := []releaseAsset{
		{Name: "ffmpeg-master-latest-linuxarm64-lgpl.tar.xz", URL: "lgpl", Size: 1},
		{Name: "FFmpeg-Master-Latest-LinuxArm64-GPL.tar.xz", URL: "gpl", Size: 2},
	}
	got, ok := pickReleaseAsset(assets, []string{"ffmpeg-master-latest-linuxarm64-gpl.tar.xz", "ffmpeg-master-latest-linuxarm64-lgpl.tar.xz"})
	if !ok || got.URL != "gpl" || got.Size != 2 {
		t.Fatalf("picked %+v (ok %v), want the GPL asset", got, ok)
	}
	if _, ok := pickReleaseAsset(assets, []string{"missing.zip"}); ok {
		t.Fatal("an asset the release does not have must not match")
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

// TestAutoInstallToolsSkipFFplay: ffplay is not fetched any more, on launch or
// on request — nothing runs it, and BtbN's build cannot start without X11 on
// a handheld.
func TestAutoInstallToolsSkipFFplay(t *testing.T) {
	have := map[string]bool{}
	for _, tool := range autoInstallTools {
		have[tool] = true
	}
	if have["ffplay"] {
		t.Fatal("ffplay must not be auto-installed")
	}
	// Refused before any network or install-card work starts.
	if err := EnsureHelperTool("ffplay", nil); err == nil || !strings.Contains(err.Error(), "unsupported helper tool") {
		t.Fatalf("EnsureHelperTool(ffplay) = %v, want an unsupported-tool error", err)
	}
	if _, err := helperExecutable("ffplay", nil); err == nil {
		t.Fatal("helperExecutable must not resolve ffplay")
	}
	for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if !have[tool] {
			t.Errorf("%s must still be auto-installed", tool)
		}
	}
	for tool, want := range map[string]bool{"ffmpeg": true, "FFprobe.exe": true, "ffplay": true, "yt-dlp": false} {
		if got := ffmpegFamilyTool(tool); got != want {
			t.Errorf("ffmpegFamilyTool(%q) = %v, want %v", tool, got, want)
		}
	}
}

// TestFFmpegBundleCacheUsable: only a complete, recent download is unpacked
// again instead of being fetched anew.
func TestFFmpegBundleCacheUsable(t *testing.T) {
	dir := t.TempDir()
	path := ffmpegBundleCachePath(dir, ".tar.xz")
	now := time.Now()
	if ffmpegBundleCacheUsable(path, now) {
		t.Fatal("a missing bundle is not usable")
	}
	if err := os.WriteFile(path, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ffmpegBundleCacheUsable(path, now) {
		t.Fatal("a few bytes are not a bundle")
	}
	if err := os.Truncate(path, 2<<20); err != nil {
		t.Fatal(err)
	}
	if !ffmpegBundleCacheUsable(path, now) {
		t.Fatal("a fresh 2 MiB bundle should be reused")
	}
	if !ffmpegBundleCacheUsable(path, now.Add(ffmpegBundleCacheMaxAge-time.Minute)) {
		t.Fatal("a bundle inside the age limit should be reused")
	}
	if ffmpegBundleCacheUsable(path, now.Add(ffmpegBundleCacheMaxAge+time.Hour)) {
		t.Fatal("a bundle past the age limit should be downloaded again")
	}
}

// TestFFmpegBundleSpaceNeeded: the estimate counts the download only when it
// is not on disk yet, plus one archive's worth per binary.
func TestFFmpegBundleSpaceNeeded(t *testing.T) {
	const mb = int64(1 << 20)
	for _, tc := range []struct {
		size     int64
		have     bool
		binaries int
		want     int64
	}{
		{128 * mb, false, 2, 384 * mb},
		{128 * mb, true, 2, 256 * mb},
		{0, false, 2, 0},
		{128 * mb, false, 0, 0},
	} {
		if got := ffmpegBundleSpaceNeeded(tc.size, tc.have, tc.binaries); got != tc.want {
			t.Errorf("ffmpegBundleSpaceNeeded(%d, %v, %d) = %d, want %d", tc.size, tc.have, tc.binaries, got, tc.want)
		}
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

// TestStripURLs: the install card's one row shows the reason, not the URL.
func TestStripURLs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{
			"extract ffmpeg from https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/x.tar.xz: could not unpack x.tar.xz: exit status 1",
			"extract ffmpeg: could not unpack x.tar.xz: exit status 1",
		},
		{
			"download https://example.invalid/a.zip: unexpected status 404",
			"download: unexpected status 404",
		},
		{
			`Get "https://api.github.com/repos/BtbN/FFmpeg-Builds/releases/latest": dial tcp: lookup api.github.com: no such host`,
			"Get: dial tcp: lookup api.github.com: no such host",
		},
		{"no address in this one", "no address in this one"},
	} {
		if got := stripURLs(tc.in); got != tc.want {
			t.Errorf("stripURLs(%q)\n = %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestToolInstallFailureReason: the card names the cause in words a handheld
// user can act on.
func TestToolInstallFailureReason(t *testing.T) {
	netMon.mu.Lock()
	prevChecked, prevOnline := netMon.checked, netMon.online
	netMon.checked, netMon.online = true, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.checked, netMon.online = prevChecked, prevOnline
		netMon.mu.Unlock()
	})

	reasoned := fmt.Errorf("missing ffmpeg and auto-download failed: %w",
		&toolInstallError{reason: "Download was damaged — retry downloads it again", err: errBadToolArchive})
	if got := toolInstallFailureReason(reasoned); got != "Download was damaged — retry downloads it again" {
		t.Errorf("reasoned error shows %q", got)
	}
	if !errors.Is(reasoned, errBadToolArchive) {
		t.Error("toolInstallError must keep the underlying error reachable")
	}
	if got := toolInstallFailureReason(errors.New("write /mnt/SDCARD/required/ffmpeg: no space left on device")); !strings.HasPrefix(got, "SD card is full") {
		t.Errorf("a full card shows %q", got)
	}
	if got := toolInstallFailureReason(errors.New("download ffmpeg from https://example.invalid/x.tar.xz: unexpected status 503")); got != "download ffmpeg: unexpected status 503" {
		t.Errorf("a URL error shows %q", got)
	}
}

// toolBundleFixture builds this platform's FFmpeg bundle in dir under the name
// a kept download has, with ffmpeg, ffprobe and ffplay inside. The binaries
// carry this platform's executable header and are incompressible, so the
// bundle is over the 1 MiB a kept download needs to be reused.
func toolBundleFixture(t *testing.T, dir string) (cache, suffix string) {
	t.Helper()
	assets, err := ffmpegAssetNames(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no FFmpeg bundle for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	header := []byte("\x7fELF")
	if runtime.GOOS == "windows" {
		suffix, header = ".exe", []byte("MZ")
	}
	cache = ffmpegBundleCachePath(dir, ffmpegArchiveExt(assets[0]))
	bodies := map[string][]byte{}
	for i, name := range []string{"ffmpeg", "ffprobe", "ffplay"} {
		bodies["ffmpeg-master-latest/bin/"+name+suffix] = append(append([]byte{}, header...), randomBytes(int64(10+i), 400<<10)...)
	}
	if strings.HasSuffix(cache, ".zip") {
		buildToolZip(t, cache, bodies)
	} else {
		headers := make([]*tar.Header, 0, len(bodies))
		for _, name := range []string{"ffplay", "ffmpeg", "ffprobe"} {
			headers = append(headers, &tar.Header{Name: "ffmpeg-master-latest/bin/" + name + suffix, Typeflag: tar.TypeReg, Mode: 0o755})
		}
		buildToolTarXZ(t, cache, headers, bodies)
	}
	return cache, suffix
}

// TestFFmpegBundleReusesKeptDownload drives the real install with a bundle
// kept from an earlier attempt: no network is touched, ffmpeg and ffprobe are
// installed, ffplay stays in the archive, and the kept download is removed
// once it has been used.
func TestFFmpegBundleReusesKeptDownload(t *testing.T) {
	resetInstallBar()
	t.Cleanup(resetInstallBar)
	dir := t.TempDir()
	cache, suffix := toolBundleFixture(t, dir)

	if err := downloadFFmpegBundleCtx(context.Background(), filepath.Join(dir, "ffmpeg"+suffix), nil); err != nil {
		t.Fatalf("install from the kept bundle: %v", err)
	}
	for _, name := range []string{"ffmpeg" + suffix, "ffprobe" + suffix} {
		if err := verifyHelperTool(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not installed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ffplay"+suffix)); err == nil {
		t.Error("ffplay should have stayed in the archive")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the kept download should be gone after a good install (stat err %v)", err)
	}
}

// TestFFmpegBundleDropsDamagedKeptDownload: a kept download that turns out to
// be damaged is deleted, so the retry fetches a new one instead of failing on
// the same bytes forever, and the card says so.
func TestFFmpegBundleDropsDamagedKeptDownload(t *testing.T) {
	resetInstallBar()
	t.Cleanup(resetInstallBar)
	dir := t.TempDir()
	cache, suffix := toolBundleFixture(t, dir)
	if err := os.WriteFile(cache, randomBytes(20, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	err := downloadFFmpegBundleCtx(context.Background(), filepath.Join(dir, "ffmpeg"+suffix), nil)
	var reasoned *toolInstallError
	if !errors.As(err, &reasoned) || !errors.Is(err, errBadToolArchive) {
		t.Fatalf("err = %v, want a damaged-download toolInstallError", err)
	}
	if !strings.Contains(reasoned.reason, "damaged") {
		t.Errorf("card reason = %q, want it to say the download was damaged", reasoned.reason)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the damaged download should have been deleted (stat err %v)", err)
	}
}
