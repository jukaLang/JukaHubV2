package main

// Tests for psremote_helpers_install.go: which release the Remote Play helpers
// come from, what makes a download unusable, that an install only ever adds
// what is missing, what a refusal by the firmware does, and what happens to a
// Play press or a PIN prompt that waits for the install. A TLS test server
// stands in for GitHub; the start-it-once probe is replaced, since the
// packaged "helpers" here are shell scripts.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	psHelperTestStream = "#!/bin/sh\necho 'usage: chiaki-stream SESSION_FILE' >&2\nexit 2\n"
	psHelperTestRegist = "#!/bin/sh\necho 'usage: chiaki-regist INPUT_FILE RESULT_FILE' >&2\nexit 2\n"
	psHelperTestInfo   = "chiaki-ng: a9a2805\n"
	psHelperTestTag    = "JukaHub-20261110120000"
	psHelperTestGlibc  = "needs GLIBC_2.34; this firmware's libc.so.6 has up to GLIBC_2.33"
)

// psHelperTestPackage is the helper package as CI builds it.
func psHelperTestPackage() map[string]string {
	return map[string]string{
		"bin/chiaki-stream": psHelperTestStream,
		"bin/chiaki-regist": psHelperTestRegist,
		"bin/BUILDINFO.txt": psHelperTestInfo,
		"SHA256SUMS": shaOf(psHelperTestInfo) + "  bin/BUILDINFO.txt\n" +
			shaOf(psHelperTestRegist) + "  bin/chiaki-regist\n" +
			shaOf(psHelperTestStream) + "  bin/chiaki-stream\n",
	}
}

// psHelperTestZip zips files (name → content).
func psHelperTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// psHelperTestServer is GitHub for one test: the release list (by page) and
// every file a release links to, by path.
type psHelperTestServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	// gate, when set, holds every package download until it is closed.
	gate chan struct{}
	// downloads counts requests for a helper package.
	downloads int
}

func (s *psHelperTestServer) url(path string) string { return s.srv.URL + path }

func (s *psHelperTestServer) put(path string, body []byte) {
	s.mu.Lock()
	s.files[path] = body
	s.mu.Unlock()
}

func (s *psHelperTestServer) downloadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downloads
}

// releasesPage publishes one page of the release list.
func (s *psHelperTestServer) releasesPage(t *testing.T, page int, rels []psHelperRelease) {
	t.Helper()
	data, err := json.Marshal(rels)
	if err != nil {
		t.Fatal(err)
	}
	s.put("/releases?page="+strconv.Itoa(page), data)
}

// releases publishes the release list's first page.
func (s *psHelperTestServer) releases(t *testing.T, rels []psHelperRelease) {
	t.Helper()
	s.releasesPage(t, 1, rels)
}

// asset publishes a helper package for tag and returns its release entry.
// digest "" is the right one, "none" leaves it out, anything else is used.
func (s *psHelperTestServer) asset(tag string, body []byte, digest string) githubAsset {
	path := "/download/" + tag + "/" + psHelperAsset
	s.put(path, body)
	switch digest {
	case "":
		digest = "sha256:" + shaOf(string(body))
	case "none":
		digest = ""
	}
	return githubAsset{Name: psHelperAsset, BrowserDownloadURL: s.url(path), Size: int64(len(body)), Digest: digest}
}

// gateDownloads holds every package download until the returned func runs
// (the test's cleanup runs it too).
func (s *psHelperTestServer) gateDownloads(t *testing.T) func() {
	t.Helper()
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release
}

// psHelperResetJob forgets every install, finished or not.
func psHelperResetJob() {
	psHelperJob.mu.Lock()
	psHelperJob.view = psHelperView{}
	psHelperJob.run = nil
	psHelperJob.running = false
	psHelperJob.endedAt = time.Time{}
	psHelperJob.waiters = 0
	psHelperJob.next, psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.nextLabel = nil, "", "", ""
	psHelperJob.mu.Unlock()
	psHelpersForgetPresence()
}

// psHelperTestEnv points the installer at a test server and an empty
// Apps/JukaHub/bin, online, with Remote Play supported and a probe that
// accepts.
func psHelperTestEnv(t *testing.T) *psHelperTestServer {
	t.Helper()
	s := &psHelperTestServer{files: map[string][]byte{}}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if key == "/releases" {
			page := r.URL.Query().Get("page")
			if page == "" {
				page = "1"
			}
			key += "?page=" + page
		}
		isPackage := strings.HasSuffix(r.URL.Path, "/"+psHelperAsset)
		s.mu.Lock()
		body, ok := s.files[key]
		gate := s.gate
		if isPackage {
			s.downloads++
		}
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if gate != nil && isPackage {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.srv.Close)
	allowLoopbackPatchHost(t)
	resetConditionalCacheForTest()
	clearToastAfterTest(t)

	root := t.TempDir()
	prevDir, prevAPI, prevClient := psHelperInstallDirOverride, psHelperReleasesAPI, psHelperClientFn
	prevSupported, prevProbe := psHelperSupportedFn, psHelperProbeFn
	prevStream, prevRegist := psEngineStreamOverride, psEngineRegistOverride
	prevOpen, prevCode := chiakiPanel.open, chiakiPanel.code
	// Online, whatever another test left the monitor at: offline presses are
	// refused before any install starts.
	netMon.mu.Lock()
	prevChecked, prevOnline := netMon.checked, netMon.online
	netMon.checked, netMon.online = true, true
	netMon.mu.Unlock()
	psHelperInstallDirOverride = filepath.Join(root, "Apps", "JukaHub", "bin")
	psHelperReleasesAPI = s.url("/releases")
	psHelperClientFn = func() *http.Client { return s.srv.Client() }
	psHelperSupportedFn = func() bool { return true }
	psHelperProbeFn = func(stream, regist, binDir string) string { return "" }
	psEngineStreamOverride, psEngineRegistOverride = "", ""
	chiakiPanel.open = false
	t.Setenv("JUKAHUB_CHIAKI_STREAM", "")
	t.Setenv("JUKAHUB_SD_ROOT", root)
	psHelperResetJob()
	t.Cleanup(func() {
		// An install a failed test left behind ends, its result posted,
		// before the folders and the seams it uses go away.
		psHelperJob.mu.Lock()
		run := psHelperJob.run
		psHelperJob.mu.Unlock()
		if run != nil {
			select {
			case <-run.done:
			case <-time.After(30 * time.Second):
				t.Error("an install was still running at the end of the test")
			}
		}
		psHelperInstallDirOverride, psHelperReleasesAPI, psHelperClientFn = prevDir, prevAPI, prevClient
		psHelperSupportedFn, psHelperProbeFn = prevSupported, prevProbe
		psEngineStreamOverride, psEngineRegistOverride = prevStream, prevRegist
		chiakiPanel.open = prevOpen
		psPanelMu.Lock()
		chiakiPanel.code = prevCode
		psPanelMu.Unlock()
		netMon.mu.Lock()
		netMon.checked, netMon.online = prevChecked, prevOnline
		netMon.mu.Unlock()
		psHelperResetJob()
		tbxDrainUI()
	})
	return s
}

// psHelperTestOffline turns the network monitor's answer to offline.
func psHelperTestOffline() {
	netMon.mu.Lock()
	netMon.online = false
	netMon.mu.Unlock()
}

// psHelperTestNothingInstalled fails when any helper file reached bin/.
func psHelperTestNothingInstalled(t *testing.T) {
	t.Helper()
	bin := psHelperInstallDir()
	for _, name := range []string{"chiaki-stream", "chiaki-regist", "BUILDINFO.txt", ".chiaki-stream.new", ".chiaki-regist.new"} {
		if _, err := os.Stat(filepath.Join(bin, name)); err == nil {
			t.Errorf("%s was installed", name)
		}
	}
}

// psHelperTestWait waits for the running install and drains its toast.
func psHelperTestWait(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := psHelpersWait(ctx)
	tbxDrainUI()
	return err
}

// psHelperTestToast is the toast on screen now.
func psHelperTestToast() string {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.Message
}

// psHelperTestClearToast takes the toast off the screen.
func psHelperTestClearToast() {
	currentToastMu.Lock()
	currentToast = ToastState{}
	currentToastMu.Unlock()
}

// psHelperTestOneRelease publishes the package on one release.
func psHelperTestOneRelease(t *testing.T, s *psHelperTestServer) {
	t.Helper()
	a := s.asset(psHelperTestTag, psHelperTestZip(t, psHelperTestPackage()), "")
	s.releases(t, []psHelperRelease{{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{a}}})
}

// psHelperTestRefuseStream is a probe that refuses chiaki-stream only.
func psHelperTestRefuseStream(stream, regist, binDir string) string {
	if stream != "" {
		return "chiaki-stream: " + psHelperTestGlibc
	}
	return ""
}

var psHelperTestConsole = psConsole{Host: "192.168.1.50", Name: "Den", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="}

func TestPSHelperPickPrefersTheNewestStableRelease(t *testing.T) {
	pkg := func(url string) githubAsset {
		return githubAsset{Name: psHelperAsset, BrowserDownloadURL: url, Size: 100}
	}
	rels := []psHelperRelease{
		{TagName: "beta", Prerelease: true, PublishedAt: "2026-11-12T00:00:00Z", Assets: []githubAsset{pkg("https://example.invalid/beta")}},
		{TagName: "draft", Draft: true, PublishedAt: "2026-11-13T00:00:00Z", Assets: []githubAsset{pkg("https://example.invalid/draft")}},
		{TagName: "old", PublishedAt: "2026-11-01T00:00:00Z", Assets: []githubAsset{pkg("https://example.invalid/old")}},
		{TagName: "new", PublishedAt: "2026-11-10T00:00:00Z", Assets: []githubAsset{pkg("https://example.invalid/new")}},
		{TagName: "none", PublishedAt: "2026-11-11T00:00:00Z", Assets: []githubAsset{{Name: "JukaHub-Trimui.zip", BrowserDownloadURL: "https://example.invalid/t", Size: 5}}},
		{TagName: "huge", PublishedAt: "2026-11-11T12:00:00Z", Assets: []githubAsset{{Name: psHelperAsset, BrowserDownloadURL: "https://example.invalid/huge", Size: psHelperMaxAsset + 1}}},
	}
	rel, asset, ok := psHelperPick(rels)
	if !ok || rel.TagName != "new" || asset.BrowserDownloadURL != "https://example.invalid/new" {
		t.Fatalf("picked %q (%s, %v), want the newest stable release with the package", rel.TagName, asset.BrowserDownloadURL, ok)
	}
	var order []string
	for _, c := range psHelperCandidates(rels) {
		order = append(order, c.rel.TagName)
	}
	if strings.Join(order, ",") != "new,old,beta" {
		t.Errorf("candidates = %v, want new, old, beta", order)
	}
	if rel, _, ok := psHelperPick(rels[:2]); !ok || rel.TagName != "beta" {
		t.Errorf("with only a beta carrying it: picked %q, %v; want the beta", rel.TagName, ok)
	}
	if _, _, ok := psHelperPick(rels[1:2]); ok {
		t.Error("a draft release was picked")
	}
	if _, _, ok := psHelperPick(nil); ok {
		t.Error("something was picked from no releases")
	}
}

func TestPSHelpersInstallFromTheNewestReleaseThatHasThem(t *testing.T) {
	s := psHelperTestEnv(t)
	pkg := psHelperTestZip(t, psHelperTestPackage())
	older := s.asset("JukaHub-20261109120000", pkg, "")
	s.releases(t, []psHelperRelease{
		// The newest release's helper build failed: it has no package.
		{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{{Name: "JukaHub-Trimui.zip", BrowserDownloadURL: s.url("/download/x/JukaHub-Trimui.zip"), Size: 10}}},
		{TagName: "JukaHub-20261109120000", PublishedAt: "2026-11-09T12:05:00Z", Assets: []githubAsset{older}},
	})
	var mu sync.Mutex
	var probed []string
	psHelperProbeFn = func(stream, regist, binDir string) string {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range []string{stream, regist} {
			if p != "" {
				probed = append(probed, filepath.Base(p)+"@"+binDir)
			}
		}
		return ""
	}
	var phases []psHelperPhase
	tag, err := psInstallHelpers(context.Background(), func(v psHelperView) {
		mu.Lock()
		phases = append(phases, v.phase)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if tag != "JukaHub-20261109120000" {
		t.Errorf("installed from %q, want the older release that has the package", tag)
	}
	bin := psHelperInstallDir()
	for name, want := range map[string]string{"chiaki-stream": psHelperTestStream, "chiaki-regist": psHelperTestRegist, "BUILDINFO.txt": psHelperTestInfo} {
		got, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want the packaged file", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, psHelperWorkDirName)); !os.IsNotExist(err) {
		t.Errorf("the work folder was left behind: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(probed, ",") != "chiaki-stream@"+bin+",chiaki-regist@"+bin {
		t.Errorf("the probe saw %q, want each unpacked helper on its own, with the install folder", probed)
	}
	if len(phases) == 0 || phases[0] != psHelperLooking || phases[len(phases)-1] != psHelperChecking {
		t.Errorf("progress = %v, want looking first and checking last", phases)
	}
	psHelpersForgetPresence()
	engine := psRemoteEngine()
	if engine.Stream != filepath.Join(bin, "chiaki-stream") || engine.Regist != filepath.Join(bin, "chiaki-regist") {
		t.Errorf("engine = %+v, want the installed helpers", engine)
	}
	if psHelpersNeeded() {
		t.Error("the helpers are still needed after the install")
	}
}

func TestPSHelpersLookPastAFullPageOfReleasesWithoutThem(t *testing.T) {
	s := psHelperTestEnv(t)
	var first []psHelperRelease
	for i := 0; i < psHelperPerPage; i++ {
		first = append(first, psHelperRelease{TagName: fmt.Sprintf("JukaHub-202611101200%02d", i), PublishedAt: "2026-11-10T12:05:00Z"})
	}
	s.releasesPage(t, 1, first)
	a := s.asset("JukaHub-20261101120000", psHelperTestZip(t, psHelperTestPackage()), "")
	s.releasesPage(t, 2, []psHelperRelease{{TagName: "JukaHub-20261101120000", PublishedAt: "2026-11-01T12:05:00Z", Assets: []githubAsset{a}}})
	tag, err := psInstallHelpers(context.Background(), nil)
	if err != nil || tag != "JukaHub-20261101120000" {
		t.Fatalf("install = %q, %v; want the release on the second page", tag, err)
	}
}

func TestPSHelpersRefuseAnArchiveThatDoesNotMatchItsDigest(t *testing.T) {
	s := psHelperTestEnv(t)
	pkg := psHelperTestZip(t, psHelperTestPackage())
	a := s.asset(psHelperTestTag, pkg, "sha256:"+shaOf("another archive"))
	s.releases(t, []psHelperRelease{{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{a}}})
	_, err := psInstallHelpers(context.Background(), nil)
	if !errors.Is(err, errPSHelpersUnverified) {
		t.Fatalf("err = %v, want errPSHelpersUnverified", err)
	}
	psHelperTestNothingInstalled(t)
	if _, err := os.Stat(filepath.Join(psHelperInstallDir(), psHelperWorkDirName)); !os.IsNotExist(err) {
		t.Errorf("bytes that did not verify were kept for a resume: %v", err)
	}
}

func TestPSHelpersRefuseFilesThatDoNotMatchSHA256SUMS(t *testing.T) {
	s := psHelperTestEnv(t)
	files := psHelperTestPackage()
	files["bin/chiaki-stream"] = "#!/bin/sh\nexit 0\n"
	a := s.asset(psHelperTestTag, psHelperTestZip(t, files), "")
	s.releases(t, []psHelperRelease{{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{a}}})
	_, err := psInstallHelpers(context.Background(), nil)
	if !errors.Is(err, errPSHelpersUnverified) || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want a SHA256SUMS refusal", err)
	}
	psHelperTestNothingInstalled(t)
}

func TestPSHelpersInstallWhatTheFirmwareRunsAndRefuseTheRest(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	psHelperProbeFn = psHelperTestRefuseStream
	_, err := psInstallHelpers(context.Background(), nil)
	if !errors.Is(err, errPSHelpersWontRun) {
		t.Fatalf("err = %v, want errPSHelpersWontRun", err)
	}
	if text := psHelpersFailText(err); !strings.Contains(text, "chiaki-stream: needs GLIBC_2.34 · firmware has GLIBC_2.33") {
		t.Errorf("fail text = %q, want the loader's reason, shortened", text)
	}
	bin := psHelperInstallDir()
	if _, err := os.Stat(filepath.Join(bin, "chiaki-stream")); err == nil {
		t.Error("the refused chiaki-stream was installed")
	}
	if got, _ := os.ReadFile(filepath.Join(bin, "chiaki-regist")); string(got) != psHelperTestRegist {
		t.Errorf("chiaki-regist, which runs, = %q; want it installed", got)
	}
	if _, err := os.Stat(filepath.Join(bin, psHelperWorkDirName)); !os.IsNotExist(err) {
		t.Errorf("a refused package was kept: %v", err)
	}
	if tag, problem, ok := psHelperLoadRefusal(); !ok || tag != psHelperTestTag || !strings.Contains(problem, "chiaki-stream") {
		t.Errorf("remembered refusal = %q / %q / %v", tag, problem, ok)
	}
}

func TestPSHelpersAreNotRetriedAfterARefusalExceptByRetry(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	psHelperProbeFn = psHelperTestRefuseStream
	if !psHelpersStart(psStartAuto) {
		t.Fatal("the first automatic start did not start")
	}
	if err := psHelperTestWait(t); !errors.Is(err, errPSHelpersWontRun) {
		t.Fatalf("err = %v, want errPSHelpersWontRun", err)
	}
	psHelperJob.mu.Lock()
	psHelperJob.endedAt = time.Now().Add(-time.Hour)
	psHelperJob.mu.Unlock()
	if psHelpersStart(psStartAuto) {
		t.Error("the panel's opening tried a refused package again")
	}
	if psHelpersStart(psStartPress) {
		t.Error("a press tried a refused package again")
	}
	if psHelpersHoldPlay(psHelperTestConsole) && psHelpersRunning() {
		t.Error("Play started a download of a refused package")
	}
	if msg := psHelperTestToast(); !strings.Contains(msg, "won't run on this firmware") {
		t.Errorf("toast for Play after a refusal = %q", msg)
	}
	rows := psHelperRows()
	if len(rows) != 1 || rows[0].label != "Remote Play won't run on this firmware" || rows[0].action != psActionInstallHelpers || rows[0].status != "Retry" {
		t.Errorf("rows after a refusal = %+v", rows)
	}
	downloads := s.downloadCount()
	if !psHelpersStart(psStartRetry) {
		t.Fatal("Retry did not try again")
	}
	_ = psHelperTestWait(t)
	if s.downloadCount() != downloads+1 {
		t.Error("Retry did not download again")
	}
}

func TestPSRemoteEngineFindsALonePairingHelper(t *testing.T) {
	psHelperTestEnv(t)
	bin := psHelperInstallDir()
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "chiaki-regist"), []byte(psHelperTestRegist), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := psRemoteEngine()
	if engine.installed() || engine.Regist != filepath.Join(bin, "chiaki-regist") {
		t.Errorf("engine = %+v, want chiaki-regist on its own and no stream", engine)
	}
	psHelpersForgetPresence()
	if stream, regist := psHelpersPresent(); stream || !regist {
		t.Errorf("present = %v, %v; want only the pairing helper", stream, regist)
	}
}

func TestPSHelpersRememberARefusalAcrossStarts(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	psHelperProbeFn = psHelperTestRefuseStream
	if _, err := psInstallHelpers(context.Background(), nil); !errors.Is(err, errPSHelpersWontRun) {
		t.Fatalf("err = %v", err)
	}
	// A new start of JukaHub: no install in memory, the note on the card.
	psHelperResetJob()
	downloads := s.downloadCount()
	psHelpersAutoStart()
	if psHelpersRunning() || s.downloadCount() != downloads {
		t.Error("the panel's opening downloaded the refused package again")
	}
	v := psHelpersView()
	if v.phase != psHelperFailed || v.refused != "stream" || !strings.Contains(v.reason, "GLIBC_2.34") {
		t.Errorf("view = %+v, want the remembered refusal", v)
	}
	if !psHelpersStart(psStartRetry) {
		t.Fatal("Retry did not start")
	}
	_ = psHelperTestWait(t)
	psHelperProbeFn = func(stream, regist, binDir string) string { return "" }
	if !psHelpersStart(psStartRetry) {
		t.Fatal("a second Retry did not start")
	}
	if err := psHelperTestWait(t); err != nil {
		t.Fatalf("install once the firmware runs it: %v", err)
	}
	if _, _, ok := psHelperLoadRefusal(); ok {
		t.Error("the refusal is still remembered after a good install")
	}
}

func TestPSHelpersRefuseABadSignedManifest(t *testing.T) {
	s := psHelperTestEnv(t)
	a := s.asset(psHelperTestTag, psHelperTestZip(t, psHelperTestPackage()), "")
	s.put("/download/"+psHelperTestTag+"/release.json", []byte("{not a manifest"))
	manifest := githubAsset{Name: "release.json", BrowserDownloadURL: s.url("/download/" + psHelperTestTag + "/release.json"), Size: 15}
	s.releases(t, []psHelperRelease{{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{a, manifest}}})
	_, err := psInstallHelpers(context.Background(), nil)
	if !errors.Is(err, errPSHelpersUnverified) {
		t.Fatalf("err = %v, want errPSHelpersUnverified", err)
	}
	if n := s.downloadCount(); n != 0 {
		t.Errorf("the package was downloaded %d time(s) although its release manifest did not verify", n)
	}
	psHelperTestNothingInstalled(t)
}

func TestPSHelpersSayWhenNoReleaseHasThem(t *testing.T) {
	s := psHelperTestEnv(t)
	draft := s.asset("draft", psHelperTestZip(t, psHelperTestPackage()), "")
	s.releases(t, []psHelperRelease{
		{TagName: psHelperTestTag, Assets: []githubAsset{{Name: "JukaHub-Trimui.zip", BrowserDownloadURL: s.url("/download/x/JukaHub-Trimui.zip"), Size: 10}}},
		{TagName: "draft", Draft: true, Assets: []githubAsset{draft}},
	})
	_, err := psInstallHelpers(context.Background(), nil)
	if !errors.Is(err, errPSHelpersNotPublished) {
		t.Fatalf("err = %v, want errPSHelpersNotPublished", err)
	}
	if text := psHelpersFailText(err); !strings.Contains(text, "no recent JukaHub release") {
		t.Errorf("fail text = %q", text)
	}
	psHelperTestNothingInstalled(t)
}

func TestPSHelpersNeverReplaceOneThatIsThere(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	bin := psHelperInstallDir()
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	own := "#!/bin/sh\n# a build of my own\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "chiaki-stream"), []byte(own), 0o755); err != nil {
		t.Fatal(err)
	}
	var probedStream, probedRegist []string
	psHelperProbeFn = func(stream, regist, binDir string) string {
		if stream != "" {
			probedStream = append(probedStream, stream)
		}
		if regist != "" {
			probedRegist = append(probedRegist, regist)
		}
		return ""
	}
	if _, err := psInstallHelpers(context.Background(), nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(bin, "chiaki-stream")); string(got) != own {
		t.Errorf("chiaki-stream was replaced: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(bin, "chiaki-regist")); string(got) != psHelperTestRegist {
		t.Errorf("chiaki-regist = %q, want the packaged one", got)
	}
	if _, err := os.Stat(filepath.Join(bin, "BUILDINFO.txt")); err == nil {
		t.Error("BUILDINFO.txt, which describes the packaged pair, was installed beside a helper of another build")
	}
	if len(probedStream) != 0 || len(probedRegist) != 1 {
		t.Errorf("probed %q and %q, want only the chiaki-regist about to be installed", probedStream, probedRegist)
	}
}

func TestPSHelpersUseACompletePartialDownload(t *testing.T) {
	s := psHelperTestEnv(t)
	pkg := psHelperTestZip(t, psHelperTestPackage())
	a := s.asset(psHelperTestTag, pkg, "")
	s.releases(t, []psHelperRelease{{TagName: psHelperTestTag, PublishedAt: "2026-11-10T12:05:00Z", Assets: []githubAsset{a}}})
	// A power cut after the last byte, before the rename.
	work := filepath.Join(psHelperInstallDir(), psHelperWorkDirName)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(work, sanitizeFileName(psHelperTestTag)+"-"+psHelperAsset+".part")
	if err := os.WriteFile(part, pkg, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := psInstallHelpers(context.Background(), nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if n := s.downloadCount(); n != 0 {
		t.Errorf("the package was downloaded %d time(s) although the card had all of it", n)
	}
	if got, _ := os.ReadFile(filepath.Join(psHelperInstallDir(), "chiaki-stream")); string(got) != psHelperTestStream {
		t.Errorf("chiaki-stream = %q", got)
	}
}

func TestPSHelpersHoldPlayUntilInstalled(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	release := s.gateDownloads(t)

	c := psHelperTestConsole
	if !psHelpersHoldPlay(c) {
		t.Fatal("Play was not held while chiaki-stream is missing")
	}
	if v := psHelpersView(); v.phase != psHelperLooking && v.phase != psHelperDownloading {
		t.Errorf("phase = %v while the download is held", v.phase)
	}
	if rows := psHelperRows(); len(rows) != 1 || !strings.HasPrefix(rows[0].label, "Installing Remote Play") || rows[0].action != psActionNone {
		t.Errorf("rows while installing = %+v", rows)
	}
	if note, ok := psHelperConsoleNote(c); !ok || !strings.HasPrefix(note, "Streams by itself when Remote Play is installed") {
		t.Errorf("the console's row while its Play waits = %q, %v", note, ok)
	}
	if !psHelpersHoldPlay(c) || !strings.HasPrefix(psHelperTestToast(), "Still installing") {
		t.Errorf("a second Play during the install: toast %q", psHelperTestToast())
	}
	if !psHelpersPressHeld() {
		t.Error("no press is held")
	}
	release()
	if err := psHelperTestWait(t); err != nil {
		t.Fatalf("install: %v", err)
	}
	// The panel is closed in the test, so the held Play only says so.
	if msg := psHelperTestToast(); !strings.Contains(msg, "installed") || !strings.Contains(msg, "Den") {
		t.Errorf("toast after the held Play = %q, want it to say Remote Play is installed and name the console", msg)
	}
	if psHelpersPressHeld() {
		t.Error("the press is still held after the install")
	}
	if rows := psHelperRows(); len(rows) != 1 || rows[0].status != "Done" || rows[0].action != psActionNone {
		t.Errorf("rows after the install = %+v, want one Done line", rows)
	}
	psHelpersAutoStart()
	if rows := psHelperRows(); rows != nil {
		t.Errorf("rows when the panel opens again = %+v, want none", rows)
	}
	if psHelpersHoldPlay(c) {
		t.Error("Play was held although chiaki-stream is there now")
	}
	if n := s.downloadCount(); n != 1 {
		t.Errorf("the package was downloaded %d times, want once", n)
	}
}

func TestPSHelpersLetGoOfAHeldPressOnB(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	release := s.gateDownloads(t)

	if !psHelpersHoldPIN("192.168.1.50", "Account-ID saved. ", true) {
		t.Fatal("the PS5's PIN prompt was not held while chiaki-regist is missing")
	}
	if msg := psHelperTestToast(); !strings.HasPrefix(msg, "Account-ID saved. Installing Remote Play first") || !strings.Contains(msg, "PIN keyboard opens when it is done") {
		t.Errorf("toast = %q", msg)
	}
	if note, ok := psHelperConsoleNote(psConsole{Host: "192.168.1.50", PS5: true}); !ok || !strings.HasPrefix(note, "PIN keyboard opens") {
		t.Errorf("the PS5's row while its PIN waits = %q, %v", note, ok)
	}
	if !psHelpersCancelHeld(true) {
		t.Fatal("B did not let go of the held PIN prompt")
	}
	if psHelpersPressHeld() || psHelpersCancelHeld(true) {
		t.Error("the prompt is still held after B")
	}
	if !psHelpersRunning() {
		t.Error("B stopped the install itself")
	}
	other := psConsole{Host: "192.168.1.51", Name: "Attic", PS5: true, RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA=="}
	if !psHelpersHoldPlay(other) {
		t.Fatal("Play was not held")
	}
	if !psHelpersCancelHeld(false) || !strings.Contains(psHelperTestToast(), "Attic") {
		t.Errorf("closing the panel let go of the held Play without saying so: %q", psHelperTestToast())
	}
	// What is on screen now must not be taken for a press that came back.
	psHelperTestClearToast()
	release()
	if err := psHelperTestWait(t); err != nil {
		t.Fatalf("install: %v", err)
	}
	if msg := psHelperTestToast(); strings.Contains(msg, "Attic") || strings.Contains(msg, "pair it") {
		t.Errorf("a press that was let go came back: %q", msg)
	}
}

func TestPSHelpersAPressReplacesAnotherAndSaysSo(t *testing.T) {
	s := psHelperTestEnv(t)
	psHelperTestOneRelease(t, s)
	release := s.gateDownloads(t)
	if !psHelpersHoldPlay(psHelperTestConsole) {
		t.Fatal("Play was not held")
	}
	if !psHelpersHoldPIN("192.168.1.60", "", true) {
		t.Fatal("the PIN prompt was not held")
	}
	if msg := psHelperTestToast(); !strings.HasPrefix(msg, "Den · PS5 won't start by itself now") {
		t.Errorf("toast = %q, want it to say the held Play is gone", msg)
	}
	release()
	_ = psHelperTestWait(t)
}

func TestPSHelpersTypedAddressIsAskedAtOnceWhenNoInstallCanCome(t *testing.T) {
	psHelperTestEnv(t)
	psHelperTestOffline()
	if psHelpersHoldPIN("192.168.1.70", "", false) {
		t.Error("an address that may be a PS4 waited for an install that cannot come")
	}
	if psHelpersRunning() || psHelpersPressHeld() {
		t.Error("an offline prompt started an install or was kept")
	}
	if !psHelpersHoldPIN("192.168.1.70", "", true) {
		t.Error("a known PS5 was asked for a PIN it cannot use")
	}
	if msg := psHelperTestToast(); !strings.Contains(msg, "no internet") {
		t.Errorf("toast = %q", msg)
	}
}

func TestPSHelpersHeldPINSaysTheInstallFailed(t *testing.T) {
	s := psHelperTestEnv(t)
	s.releases(t, []psHelperRelease{})
	// Held or refused at once, depending on how fast the install fails:
	// either way the PIN is not asked for, and the toast says why.
	if !psHelpersHoldPIN("192.168.1.50", "", true) {
		t.Fatal("the PIN was asked for although the pairing helper is missing")
	}
	_ = psHelperTestWait(t)
	if msg := psHelperTestToast(); !strings.Contains(msg, "Could not install Remote Play") || !strings.Contains(msg, "no recent JukaHub release") {
		t.Errorf("toast after a failed install = %q", msg)
	}
}

func TestPSHelpersRefuseAPressWhileOffline(t *testing.T) {
	psHelperTestEnv(t)
	psHelperTestOffline()
	if !psHelpersHoldPlay(psHelperTestConsole) {
		t.Fatal("an offline Play went on to a stream with no helper")
	}
	if psHelpersRunning() || psHelpersPressHeld() {
		t.Error("an offline Play started an install or was kept")
	}
	if msg := psHelperTestToast(); !strings.Contains(msg, "no internet right now") {
		t.Errorf("toast = %q", msg)
	}
	rows := psHelperRows()
	if len(rows) != 1 || !strings.HasPrefix(rows[0].detail, "No internet right now") {
		t.Errorf("rows offline = %+v", rows)
	}
	if note, ok := psHelperConsoleNote(psHelperTestConsole); !ok || !strings.Contains(note, "no internet") {
		t.Errorf("a paired console's row offline = %q, %v", note, ok)
	}
}

func TestPSHelpersAutoStartLeavesARecentFailureToRetry(t *testing.T) {
	s := psHelperTestEnv(t)
	s.releases(t, []psHelperRelease{})
	if !psHelpersStart(psStartAuto) {
		t.Fatal("the panel's first opening did not start an install")
	}
	if err := psHelperTestWait(t); !errors.Is(err, errPSHelpersNotPublished) {
		t.Fatalf("err = %v, want errPSHelpersNotPublished", err)
	}
	if psHelpersStart(psStartAuto) {
		t.Error("the panel's opening repeated a failure seconds old")
	}
	rows := psHelperRows()
	if len(rows) != 1 || rows[0].status != "Retry" || rows[0].action != psActionInstallHelpers || !strings.Contains(rows[0].detail, "no recent JukaHub release") {
		t.Errorf("rows after the failure = %+v, want one Retry row with the reason", rows)
	}
	if !strings.Contains(psHelperSubtitle(), "not installed") {
		t.Errorf("subtitle = %q", psHelperSubtitle())
	}
	if !psHelpersStart(psStartPress) {
		t.Error("a press did not start another install after a failure")
	}
	_ = psHelperTestWait(t)
}

func TestPSHelpersWaitReturnsItsOwnRun(t *testing.T) {
	s := psHelperTestEnv(t)
	s.releases(t, []psHelperRelease{})
	psHelpersStart(psStartPress)
	if err := psHelperTestWait(t); !errors.Is(err, errPSHelpersNotPublished) {
		t.Fatalf("first: %v", err)
	}
	// Ended, not retried: the answer is the last install's.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := psHelpersWait(ctx); !errors.Is(err, errPSHelpersNotPublished) {
		t.Errorf("waiting on an ended install = %v, want its error", err)
	}
}

func TestPSHelpersForPS5PairingSaysWhyTheInstallFailed(t *testing.T) {
	s := psHelperTestEnv(t)
	s.releases(t, nil)
	err := psHelpersForPS5Pairing()
	if !errors.Is(err, errPSPairNoHelpers) || !strings.Contains(err.Error(), "no recent JukaHub release") {
		t.Fatalf("err = %v, want the install's reason", err)
	}
	toast, code, ok := psPairNoHelpersText(err)
	if !ok || !strings.HasPrefix(toast, "Could not pair — Remote Play is not installed yet") || code != psPairCodeNoHelpers {
		t.Errorf("pairing texts = %q / %q / %v", toast, code, ok)
	}
	if toast, code, ok := psPairNoHelpersText(errPSPairStillInstalling); !ok || !strings.HasPrefix(toast, "Not paired yet") || code != psPairCodeStillInstalling {
		t.Errorf("still-installing texts = %q / %q / %v", toast, code, ok)
	}
	if _, _, ok := psPairNoHelpersText(errors.New("PS5 pairing failed at protocol: wrong PIN")); ok {
		t.Error("a handshake's own error was taken for a missing helper")
	}
	psPanelMu.Lock()
	chiakiPanel.code = code
	psPanelMu.Unlock()
	psPanelClearHelperCode()
	if chiakiPanel.code != "" {
		t.Errorf("a finished install left the Last error %q", chiakiPanel.code)
	}
}

func TestPSHelperRowsOnlyWhereRemotePlayRuns(t *testing.T) {
	psHelperTestEnv(t)
	psHelperSupportedFn = func() bool { return false }
	if rows := psHelperRows(); rows != nil {
		t.Errorf("rows where Remote Play cannot run = %+v", rows)
	}
	if line := psHelperSubtitle(); line != "" {
		t.Errorf("subtitle where Remote Play cannot run = %q", line)
	}
	if psHelpersStart(psStartRetry) {
		t.Error("an install started where Remote Play cannot run")
	}
	if psHelpersHoldPlay(psHelperTestConsole) {
		t.Error("Play was held where Remote Play cannot run")
	}
	if psHelpersHoldPIN("192.168.1.50", "", true) {
		t.Error("a PIN prompt was held where Remote Play cannot run")
	}
	if _, ok := psHelperConsoleNote(psHelperTestConsole); ok {
		t.Error("a console row changed where Remote Play cannot run")
	}
	psHelperSupportedFn = func() bool { return true }
	rows := psHelperRows()
	if len(rows) != 1 || rows[0].status != "Install" || rows[0].action != psActionInstallHelpers {
		t.Errorf("rows with nothing installed = %+v, want the Install offer", rows)
	}
	if note, ok := psHelperConsoleNote(psHelperTestConsole); !ok || !strings.HasPrefix(note, "Needs Remote Play installed first") {
		t.Errorf("a paired console with no stream helper = %q, %v", note, ok)
	}
}

func TestPSHelperPercent(t *testing.T) {
	cases := []struct {
		v    psHelperView
		want string
	}{
		{psHelperView{phase: psHelperDownloading, got: 50, total: 200}, "25%"},
		{psHelperView{phase: psHelperDownloading, got: 200, total: 200}, "99%"},
		{psHelperView{phase: psHelperLooking}, "…"},
		{psHelperView{phase: psHelperChecking, got: 10, total: 10}, "…"},
	}
	for _, c := range cases {
		if got := psHelperPercent(c.v); got != c.want {
			t.Errorf("psHelperPercent(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestPSHelperFindFilesRefusesTwoCopies(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"bin/chiaki-stream", "bin/chiaki-regist", "other/chiaki-stream"} {
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := psHelperFindFiles(dir); !errors.Is(err, errPSHelpersUnverified) {
		t.Errorf("err = %v, want a refusal of two chiaki-stream files", err)
	}
	if err := os.Remove(filepath.Join(dir, "other", "chiaki-stream")); err != nil {
		t.Fatal(err)
	}
	files, err := psHelperFindFiles(dir)
	if err != nil || filepath.Base(files.stream) != "chiaki-stream" || filepath.Base(files.regist) != "chiaki-regist" {
		t.Errorf("files = %+v, %v", files, err)
	}
}

func TestPSHelperNetErrSaysWhatToDo(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{&net.DNSError{Err: "no such host", Name: "api.github.com"}, errPSHelpersOffline},
		{fmt.Errorf("Get \"https://api.github.com/\": %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: network is unreachable")}), errPSHelpersOffline},
		{errors.New("HTTP 403 from https://api.github.com/repos/jukaLang/JukaHubV2/releases"), errPSHelpersRateLimited},
		{errors.New("tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 1970-01-01T00:00:12Z is before 2026-01-01T00:00:00Z"), errPSHelpersClock},
		{errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), errPSHelpersTrust},
		{errors.New("download failed after 3.1 MB: unexpected EOF"), errPSHelpersCutOff},
		{errors.New("the partial download does not match https://github.com/x; retry to start over"), errPSHelpersCutOff},
		{errors.New("write /mnt/SDCARD/Apps/JukaHub/bin/.remote-play-install/x.part: no space left on device"), errPSHelpersNoSpace},
		{errors.New("open /mnt/SDCARD/Apps/JukaHub/bin/.remote-play-install/x.part: read-only file system"), errPSHelpersSDWrite},
	}
	for _, c := range cases {
		if got := psHelperNetErr("listing", c.err); !errors.Is(got, c.want) {
			t.Errorf("psHelperNetErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	other := psHelperNetErr("listing", errors.New("HTTP 500 from https://api.github.com/"))
	if errors.Is(other, errPSHelpersOffline) || errors.Is(other, errPSHelpersRateLimited) {
		t.Errorf("a server error read as %v", other)
	}
	if got := psHelperNetErr("listing", context.Canceled); got != context.Canceled {
		t.Errorf("a cancel became %v", got)
	}
}

func TestPSHelperFailTextsFitARow(t *testing.T) {
	for _, err := range []error{errPSHelpersOffline, errPSHelpersNoInternet, errPSHelpersCutOff, errPSHelpersRateLimited,
		errPSHelpersClock, errPSHelpersTrust, errPSHelpersSDWrite, errPSHelpersNotPublished, errPSHelpersUnverified,
		context.DeadlineExceeded, psHelperNoSpace(40<<20, 12<<20)} {
		if text := psHelpersFailText(err); len([]rune(text)) > 62 {
			t.Errorf("%q is %d characters, more than a Smart Pro row shows", text, len([]rune(text)))
		}
	}
	if got := psHelperCompact(psHelperTestGlibc); got != "needs GLIBC_2.34 · firmware has GLIBC_2.33" {
		t.Errorf("compact = %q", got)
	}
	if got := psHelperFailToast("Account-ID saved. ", errPSHelpersNotPublished); !strings.HasPrefix(got, "Account-ID saved, but Remote Play could not install") {
		t.Errorf("a press's news was lost: %q", got)
	}
}
