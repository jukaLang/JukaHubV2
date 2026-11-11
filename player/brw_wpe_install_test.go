package main

// brw_wpe_install_test.go — Get/Update WPE Browser without a network or a
// device: release selection and verification, the streamed download (resume,
// a server that ignores Range, a damaged file), finding the app in the zip's
// layouts, and the swap that must keep the user's files and never lose the old
// app.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wpeTestZip builds a zip from {name, body} pairs, in order; a name ending in
// "/" is a folder.
func wpeTestZip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		name, body := f[0], f[1]
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatalf("zip write %s: %v", name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func wpeTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readWpeTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestBrwWpePickAsset(t *testing.T) {
	assets := []brwWpeAsset{
		{Name: "checksums.txt", URL: "https://github.com/x/checksums.txt"},
		{Name: "extras.zip", URL: "https://github.com/x/extras.zip"},
		{Name: "WPE-0.3.1.zip", URL: "https://github.com/x/WPE-0.3.1.zip"},
		{Name: "notes.tar.gz", URL: "https://github.com/x/notes.tar.gz"},
	}
	got, ok := brwWpePickAsset(assets)
	if !ok || got.Name != "WPE-0.3.1.zip" {
		t.Fatalf("picked %q (%v), want WPE-0.3.1.zip", got.Name, ok)
	}
	got, ok = brwWpePickAsset(assets[:2])
	if !ok || got.Name != "extras.zip" {
		t.Fatalf("a lone zip should still be picked, got %q (%v)", got.Name, ok)
	}
	if _, ok := brwWpePickAsset([]brwWpeAsset{{Name: "WPE.tar.gz", URL: "https://github.com/x"}}); ok {
		t.Fatalf("a release without a zip must not yield an asset")
	}
	if _, ok := brwWpePickAsset([]brwWpeAsset{{Name: "WPE.zip"}}); ok {
		t.Fatalf("an asset without a download address must not be picked")
	}
}

func TestBrwWpeDigestHex(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if got, err := brwWpeDigestHex("SHA256:" + strings.ToUpper(good)); err != nil || got != good {
		t.Fatalf("brwWpeDigestHex = %q, %v; want %q", got, err, good)
	}
	for _, bad := range []string{"", "sha256:", "sha256:abc", "md5:" + good, good, "sha256:" + strings.Repeat("zz", 32)} {
		if _, err := brwWpeDigestHex(bad); err == nil {
			t.Errorf("brwWpeDigestHex(%q) accepted", bad)
		}
	}
}

func wpeReleaseJSON(tag, name, url, digest string, size int64) []byte {
	return []byte(fmt.Sprintf(`{"tag_name":%q,"assets":[{"name":"source.txt","browser_download_url":"https://github.com/x/source.txt","size":10},`+
		`{"name":%q,"browser_download_url":%q,"digest":%q,"size":%d}]}`, tag, name, url, digest, size))
}

func TestBrwWpeParseRelease(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0f", 32)
	url := "https://github.com/matweew/wpe-tsp/releases/download/v0.3.1/WPE-0.3.1.zip"
	rel, err := brwWpeParseRelease(wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", url, digest, 270<<20))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rel.Tag != "v0.3.1" || rel.Asset.Name != "WPE-0.3.1.zip" || rel.Asset.Size != 270<<20 {
		t.Fatalf("parsed %+v", rel)
	}
	refused := map[string][]byte{
		"no digest":       wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", url, "", 100),
		"bad tag":         wpeReleaseJSON("v0.3.1; rm -rf", "WPE-0.3.1.zip", url, digest, 100),
		"no zip":          wpeReleaseJSON("v0.3.1", "WPE-0.3.1.tar.gz", url, digest, 100),
		"zero size":       wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", url, digest, 0),
		"too large":       wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", url, digest, brwWpeMaxDownload+1),
		"host not listed": wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", "https://example.com/WPE.zip", digest, 100),
		"plain http":      wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", "http://github.com/WPE.zip", digest, 100),
		"not json":        []byte("<html>rate limited</html>"),
	}
	for name, data := range refused {
		if _, err := brwWpeParseRelease(data); err == nil {
			t.Errorf("%s: release accepted", name)
		}
	}
}

func TestBrwWpeRangeStartsAt(t *testing.T) {
	if !brwWpeRangeStartsAt("bytes 100-999/1000", 100) {
		t.Errorf("a matching range was refused")
	}
	for _, header := range []string{"", "bytes 0-999/1000", "bytes */1000", "items 100-999/1000", "bytes x-9/10"} {
		if brwWpeRangeStartsAt(header, 100) {
			t.Errorf("Content-Range %q accepted for offset 100", header)
		}
	}
}

// wpeAssetServer serves body at /WPE.zip with Range support (http.ServeContent)
// unless ignoreRange, counting requests and remembering the last Range header.
type wpeAssetServer struct {
	srv         *httptest.Server
	requests    atomic.Int32
	lastRange   atomic.Value
	ignoreRange bool
}

func newWpeAssetServer(t *testing.T, body []byte, ignoreRange bool) *wpeAssetServer {
	t.Helper()
	s := &wpeAssetServer{ignoreRange: ignoreRange}
	s.lastRange.Store("")
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.lastRange.Store(r.Header.Get("Range"))
		if s.ignoreRange {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
			return
		}
		http.ServeContent(w, r, "WPE.zip", time.Unix(1700000000, 0), bytes.NewReader(body))
	}))
	t.Cleanup(s.srv.Close)
	allowUpdateTestServer(t, s.srv.URL)
	return s
}

func TestBrwWpeDownloadStreamsVerifiesAndResumes(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 40000) // 640 KB
	srv := newWpeAssetServer(t, body, false)
	asset := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Digest: wpeTestDigest(body), Size: int64(len(body))}
	dir := t.TempDir()
	dest := filepath.Join(dir, "wpe.zip")

	var last, total int64
	progress := func(done, all int64) { last, total = done, all }
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), asset, dest, progress); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := readWpeTestFile(t, dest); got != string(body) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(body))
	}
	if last != total || total != int64(len(body)) {
		t.Fatalf("progress ended at %d/%d", last, total)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("the part file was left behind: %v", err)
	}

	// The file is already there with the right digest: no second request.
	before := srv.requests.Load()
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), asset, dest, nil); err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if srv.requests.Load() != before {
		t.Fatalf("a verified file on the card was downloaded again")
	}

	// A part from an interrupted attempt resumes with a Range request.
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	half := len(body) / 2
	if err := os.WriteFile(dest+".part", body[:half], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), asset, dest, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := srv.lastRange.Load().(string); got != fmt.Sprintf("bytes=%d-", half) {
		t.Fatalf("resume sent Range %q", got)
	}
	if got := readWpeTestFile(t, dest); got != string(body) {
		t.Fatalf("the resumed file differs from the asset")
	}
}

func TestBrwWpeDownloadRestartsWhenRangeIsIgnored(t *testing.T) {
	body := bytes.Repeat([]byte("wpe!"), 50000)
	srv := newWpeAssetServer(t, body, true)
	asset := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Digest: wpeTestDigest(body), Size: int64(len(body))}
	dest := filepath.Join(t.TempDir(), "wpe.zip")
	if err := os.WriteFile(dest+".part", body[:1000], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), asset, dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := readWpeTestFile(t, dest); got != string(body) {
		t.Fatalf("a whole-file answer to a Range request was appended to the part")
	}
}

func TestBrwWpeDownloadRefusesDamagedFiles(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 100000)
	srv := newWpeAssetServer(t, body, false)
	dest := filepath.Join(t.TempDir(), "wpe.zip")

	wrong := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Digest: wpeTestDigest([]byte("other")), Size: int64(len(body))}
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), wrong, dest, nil); err == nil {
		t.Fatalf("a file with the wrong digest was accepted")
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was kept after a digest mismatch", p)
		}
	}

	short := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Digest: wpeTestDigest(body), Size: int64(len(body)) - 10}
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), short, dest, nil); err == nil {
		t.Fatalf("a file larger than the release says was accepted")
	}

	noDigest := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Size: int64(len(body))}
	if err := brwWpeDownload(context.Background(), srv.srv.Client(), noDigest, dest, nil); err == nil {
		t.Fatalf("a file without a digest was downloaded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	good := brwWpeAsset{Name: "WPE.zip", URL: srv.srv.URL + "/WPE.zip", Digest: wpeTestDigest(body), Size: int64(len(body))}
	if err := brwWpeDownload(ctx, srv.srv.Client(), good, dest, nil); err == nil {
		t.Fatalf("a cancelled download reported success")
	}
}

func TestBrwWpeFindPackageLayouts(t *testing.T) {
	layouts := []struct {
		want  string
		files []string
	}{
		{"Apps/WPE", []string{"Apps/WPE/launch.sh", "Apps/WPE/bin/wpe", "Apps/WPE/config.json"}},
		{"WPE", []string{"WPE/launch.sh", "WPE/lib/libWPEWebKit.so"}},
		{".", []string{"launch.sh", "bin/wpe"}},
		{"WPE-0.3.1/Apps/WPE", []string{"WPE-0.3.1/Apps/WPE/launch.sh"}},
		{"Apps/WPE", []string{"Apps/WPE/launch.sh", "Apps/Other/launch.sh"}},
	}
	for _, layout := range layouts {
		root := t.TempDir()
		for _, f := range layout.files {
			writeWpeTestFile(t, filepath.Join(root, filepath.FromSlash(f)), "x")
		}
		got, err := brwWpeFindPackage(root)
		if err != nil {
			t.Errorf("%s: %v", layout.want, err)
			continue
		}
		wantDir := filepath.Join(root, filepath.FromSlash(layout.want))
		if got != wantDir {
			t.Errorf("layout %s: found %s, want %s", layout.want, got, wantDir)
		}
	}
	empty := t.TempDir()
	writeWpeTestFile(t, filepath.Join(empty, "README.txt"), "x")
	if _, err := brwWpeFindPackage(empty); err == nil {
		t.Errorf("a zip without launch.sh was accepted")
	}
	two := t.TempDir()
	writeWpeTestFile(t, filepath.Join(two, "A", "launch.sh"), "x")
	writeWpeTestFile(t, filepath.Join(two, "B", "launch.sh"), "x")
	if _, err := brwWpeFindPackage(two); err == nil {
		t.Errorf("two unnamed apps side by side were accepted")
	}
}

func TestBrwWpeCarryOver(t *testing.T) {
	manifest := map[string]bool{"lib/libold.so": true, "launch.sh": true}
	cases := []struct {
		rel      string
		inNew    bool
		manifest map[string]bool
		want     bool
	}{
		{"settings.conf", false, manifest, true},
		{"settings.conf", true, manifest, true},
		{"launch.sh", true, manifest, false},
		{"lib/libold.so", false, manifest, false},
		{"lib/libold.so", false, nil, true},
		{"favorites.json", false, manifest, true},
		{"data/cookies.db", false, manifest, true},
		{brwWpeReleaseFile, false, manifest, false},
		{brwWpeFilesFile, false, nil, false},
	}
	for _, c := range cases {
		if got := brwWpeCarryOver(c.rel, c.inNew, c.manifest); got != c.want {
			t.Errorf("brwWpeCarryOver(%q, %v, manifest=%v) = %v, want %v", c.rel, c.inNew, c.manifest != nil, got, c.want)
		}
	}
}

func TestBrwWpeSwapUpdateKeepsUserFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Apps", "WPE")
	stage := filepath.Join(root, "Apps", brwWpeStageDir)
	pkg := filepath.Join(stage, "unpack", "WPE")

	// The old install: a package JukaHub installed (manifest) plus the user's
	// settings and favourites.
	writeWpeTestFile(t, filepath.Join(target, "launch.sh"), "old launcher")
	writeWpeTestFile(t, filepath.Join(target, "lib", "libold.so"), "old lib")
	writeWpeTestFile(t, filepath.Join(target, "settings.conf"), "START_PAGE=last")
	writeWpeTestFile(t, filepath.Join(target, "data", "favorites.json"), "[1,2]")
	writeWpeTestFile(t, filepath.Join(target, brwWpeFilesFile), "launch.sh\nlib/libold.so\n")
	writeWpeTestFile(t, filepath.Join(target, brwWpeReleaseFile), "v0.3.0\n")
	// The new package ships its own settings.conf default too.
	writeWpeTestFile(t, filepath.Join(pkg, "launch.sh"), "new launcher")
	writeWpeTestFile(t, filepath.Join(pkg, "lib", "libnew.so"), "new lib")
	writeWpeTestFile(t, filepath.Join(pkg, "settings.conf"), "START_PAGE=home")
	writeWpeTestFile(t, filepath.Join(pkg, "settings.default.conf"), "START_PAGE=home")

	if err := brwWpeSwap(pkg, target, filepath.Join(stage, "WPE.old")); err != nil {
		t.Fatalf("swap: %v", err)
	}
	checks := map[string]string{
		"launch.sh":             "new launcher",
		"lib/libnew.so":         "new lib",
		"settings.conf":         "START_PAGE=last",
		"settings.default.conf": "START_PAGE=home",
		"data/favorites.json":   "[1,2]",
	}
	for rel, want := range checks {
		if got := readWpeTestFile(t, filepath.Join(target, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "lib", "libold.so")); !os.IsNotExist(err) {
		t.Errorf("a file the old release brought and the new one dropped was kept")
	}
	if _, err := os.Stat(filepath.Join(stage, "WPE.old")); !os.IsNotExist(err) {
		t.Errorf("the old folder was left beside the new one")
	}
	if _, err := os.Stat(pkg); !os.IsNotExist(err) {
		t.Errorf("the unpacked package is still in the stage")
	}
}

func TestBrwWpeSwapFreshAndBlocked(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Apps", "WPE")
	pkg := filepath.Join(root, "stage", "WPE")
	writeWpeTestFile(t, filepath.Join(pkg, "launch.sh"), "launcher")
	if err := brwWpeSwap(pkg, target, filepath.Join(root, "stage", "WPE.old")); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if got := readWpeTestFile(t, filepath.Join(target, "launch.sh")); got != "launcher" {
		t.Fatalf("fresh install wrote %q", got)
	}

	// A file where the folder belongs is refused, and the package stays.
	blocked := filepath.Join(root, "Apps2", "WPE")
	writeWpeTestFile(t, blocked, "not a folder")
	pkg2 := filepath.Join(root, "stage2", "WPE")
	writeWpeTestFile(t, filepath.Join(pkg2, "launch.sh"), "launcher")
	if err := brwWpeSwap(pkg2, blocked, filepath.Join(root, "stage2", "WPE.old")); err == nil {
		t.Fatalf("a file named WPE was replaced")
	}
	if got := readWpeTestFile(t, blocked); got != "not a folder" {
		t.Fatalf("the blocking file was changed")
	}
}

func TestBrwWpeRecoverParkedFinishesTheSwap(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Apps", "WPE")
	stage := filepath.Join(root, "Apps", brwWpeStageDir)
	parked := filepath.Join(stage, "WPE.old")

	// Cut short between the swap's two renames: the old app goes back whole.
	writeWpeTestFile(t, filepath.Join(parked, "launch.sh"), "old launcher")
	writeWpeTestFile(t, filepath.Join(parked, "settings.conf"), "START_PAGE=last")
	if err := brwWpeRecoverParked(target, stage); err != nil {
		t.Fatalf("recover the parked app: %v", err)
	}
	if got := readWpeTestFile(t, filepath.Join(target, "settings.conf")); got != "START_PAGE=last" {
		t.Fatalf("the parked app did not come back: settings.conf = %q", got)
	}

	// Cut short during the carry: the new app is in place and the rest of the
	// user's files are still in WPE.old, which the stage clean-up would delete.
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	writeWpeTestFile(t, filepath.Join(target, "launch.sh"), "new launcher")
	writeWpeTestFile(t, filepath.Join(target, "settings.conf"), "START_PAGE=home")
	writeWpeTestFile(t, filepath.Join(target, "data", "carried.json"), "already moved")
	writeWpeTestFile(t, filepath.Join(parked, "launch.sh"), "old launcher")
	writeWpeTestFile(t, filepath.Join(parked, "lib", "libold.so"), "old lib")
	writeWpeTestFile(t, filepath.Join(parked, "settings.conf"), "START_PAGE=last")
	writeWpeTestFile(t, filepath.Join(parked, "data", "favorites.json"), "[1,2]")
	writeWpeTestFile(t, filepath.Join(parked, brwWpeFilesFile), "launch.sh\nlib/libold.so\n")
	if err := brwWpeRecoverParked(target, stage); err != nil {
		t.Fatalf("finish the carry: %v", err)
	}
	checks := map[string]string{
		"launch.sh":           "new launcher",
		"settings.conf":       "START_PAGE=last",
		"data/favorites.json": "[1,2]",
		"data/carried.json":   "already moved",
	}
	for rel, want := range checks {
		if got := readWpeTestFile(t, filepath.Join(target, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "lib", "libold.so")); !os.IsNotExist(err) {
		t.Errorf("a file the old release brought was carried into the new one")
	}

	// A finished install (its file list written) is not carried into again.
	writeWpeTestFile(t, filepath.Join(target, brwWpeFilesFile), "launch.sh\n")
	writeWpeTestFile(t, filepath.Join(parked, "stray.txt"), "left over")
	if err := brwWpeRecoverParked(target, stage); err != nil {
		t.Fatalf("leftovers of a finished swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "stray.txt")); !os.IsNotExist(err) {
		t.Errorf("leftovers of a finished swap were carried into the app")
	}
}

func TestBrwWpeInstallArchiveAndMarkers(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "Apps", brwWpeStageDir)
	target := filepath.Join(root, "Apps", "WPE")
	data := wpeTestZip(t, [][2]string{
		{"Apps/", ""},
		{"Apps/WPE/", ""},
		{"Apps/WPE/launch.sh", "#!/bin/sh\nexec ./bin/wpe \"$1\"\n"},
		{"Apps/WPE/config.json", `{"label":"WPE Browser"}`},
		{"Apps/WPE/bin/wpe", "ELF"},
		{"Apps/WPE/settings.default.conf", "HOME_URL=https://duckduckgo.com"},
	})
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(stage, "wpe.zip")
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := brwWpeInstallArchive(context.Background(), archive, stage, target, "v0.3.1"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := brwWpeReadTag(target); got != "v0.3.1" {
		t.Fatalf("recorded tag %q", got)
	}
	manifest := brwWpeReadManifest(target)
	for _, rel := range []string{"launch.sh", "config.json", "bin/wpe", "settings.default.conf"} {
		if !manifest[rel] {
			t.Errorf("manifest is missing %s: %v", rel, manifest)
		}
	}
	if manifest[brwWpeReleaseFile] || manifest[brwWpeFilesFile] {
		t.Errorf("the manifest lists JukaHub's own markers: %v", manifest)
	}
}

func TestBrwWpeRunInstallEndToEnd(t *testing.T) {
	zipData := wpeTestZip(t, [][2]string{
		{"WPE/launch.sh", "#!/bin/sh\n"},
		{"WPE/bin/wpe", strings.Repeat("E", 5000)},
		{"WPE/config.json", "{}"},
	})
	var releaseHits atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	allowUpdateTestServer(t, srv.URL)
	mux.HandleFunc("/repos/matweew/wpe-tsp/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		releaseHits.Add(1)
		_, _ = w.Write(wpeReleaseJSON("v0.3.1", "WPE-0.3.1.zip", srv.URL+"/download/WPE-0.3.1.zip", wpeTestDigest(zipData), int64(len(zipData))))
	})
	mux.HandleFunc("/download/WPE-0.3.1.zip", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "WPE-0.3.1.zip", time.Unix(1700000000, 0), bytes.NewReader(zipData))
	})
	prevURL := brwWpeReleaseURL
	brwWpeReleaseURL = srv.URL + "/repos/matweew/wpe-tsp/releases/latest"
	t.Cleanup(func() {
		brwWpeReleaseURL = prevURL
		// The install records the tag it read; later tests start without it.
		brwWpeLatest.mu.Lock()
		brwWpeLatest.tag, brwWpeLatest.checkedAt = "", time.Time{}
		brwWpeLatest.mu.Unlock()
	})

	root := t.TempDir()
	tag, err := brwWpeRunInstall(context.Background(), srv.Client(), root, false)
	if err != nil || tag != "v0.3.1" {
		t.Fatalf("install: tag %q, %v", tag, err)
	}
	target := filepath.Join(root, "Apps", "WPE")
	if !regularFile(filepath.Join(target, "launch.sh")) || brwWpeReadTag(target) != "v0.3.1" {
		t.Fatalf("WPE is not in place at %s", target)
	}
	if _, err := os.Stat(filepath.Join(root, "Apps", brwWpeStageDir)); !os.IsNotExist(err) {
		t.Fatalf("the staging folder was left on the card")
	}
	if brwWpeLatestTag() != "v0.3.1" {
		t.Fatalf("the install did not record the latest tag")
	}

	// The same release again: nothing to do.
	if _, err := brwWpeRunInstall(context.Background(), srv.Client(), root, true); !errors.Is(err, errBrwWpeUpToDate) {
		t.Fatalf("update with the same tag: %v, want errBrwWpeUpToDate", err)
	}
	if releaseHits.Load() != 2 {
		t.Fatalf("release endpoint asked %d times, want 2", releaseHits.Load())
	}
}

func TestBrwWpeSpaceNeeded(t *testing.T) {
	if got := brwWpeSpaceNeeded(100 << 20); got != 300<<20+brwWpeSpaceMargin {
		t.Fatalf("brwWpeSpaceNeeded(100 MB) = %d", got)
	}
	if got := brwWpeSpaceNeeded(0); got != brwWpeSpaceMargin {
		t.Fatalf("brwWpeSpaceNeeded(0) = %d", got)
	}
}
