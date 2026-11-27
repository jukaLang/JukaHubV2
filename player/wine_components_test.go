package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadProgressWriterReportsAboutOncePerMegabyte(t *testing.T) {
	var buf strings.Builder
	reports := 0
	var lastGot int64
	w := &progressWriter{w: &buf, total: 2 << 20, report: func(got, total int64) {
		reports++
		lastGot = got
		if total != 2<<20 {
			t.Errorf("total = %d", total)
		}
	}}
	chunk := make([]byte, 512<<10)
	for i := 0; i < 4; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if reports < 1 || reports > 2 || lastGot != 2<<20 {
		t.Fatalf("reports = %d, last = %d", reports, lastGot)
	}
}

func TestWineVerifyNeverDeletesAUserSuppliedFile(t *testing.T) {
	dir := t.TempDir()
	old := wineComponentsDir
	wineComponentsDir = func() string { return dir }
	defer func() { wineComponentsDir = old }()

	// A user who names the cache file as their local source puts the file in
	// our cache path. A bad digest is their file to fix, not ours to remove.
	path := filepath.Join(dir, "same.bin")
	if err := os.WriteFile(path, []byte("changed by the user"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := wineComponent{Name: "Same", File: "same.bin", Local: "same.bin", SHA256: strings.Repeat("0", 64)}
	if err := wineComponentVerify(c); err == nil {
		t.Fatal("a wrong digest was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the user's file was deleted: %v", err)
	}
}

func TestWineComponentsLocalFileAndTemplate(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldValidate := wineComponentsDir, wineComponentsValidateURL
	wineComponentsDir = func() string { return dir }
	wineComponentsValidateURL = func(raw string) error { return errors.New("no network in this test") }
	defer func() { wineComponentsDir, wineComponentsValidateURL = oldDir, oldValidate }()

	// The example list is safe: no SHA-256 means nothing can be fetched.
	if err := wineComponentsCreateTemplate(); err != nil {
		t.Fatal(err)
	}
	list, err := wineLoadComponents()
	if err != nil || len(list) != 1 || list[0].Problem == "" {
		t.Fatalf("template = %+v, %v", list, err)
	}
	if err := wineComponentsCreateTemplate(); err == nil {
		t.Fatal("the template overwrote an existing list")
	}

	// A file the user copied to the data folder needs no network at all.
	body := []byte("a component supplied locally")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "supplied.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"components":[{"name":"Local","url":"","sha256":"` + digest +
		`","file":"cached.bin","local":"supplied.bin"}]}`
	if err := os.WriteFile(filepath.Join(dir, wineComponentsFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err = wineLoadComponents()
	if err != nil || len(list) != 1 || list[0].Problem != "" {
		t.Fatalf("local entry = %+v, %v", list, err)
	}
	if err := wineComponentFetch(context.Background(), list[0]); err != nil {
		t.Fatal(err)
	}
	if !wineComponentInstalled(list[0]) {
		t.Fatal("the local component was not cached")
	}
	if size := wineComponentsCachedBytes(); size <= 0 {
		t.Fatalf("cached size = %d", size)
	}
	if err := wineComponentRemove(list[0]); err != nil {
		t.Fatal(err)
	}
	if wineComponentInstalled(list[0]) {
		t.Fatal("the component was not removed")
	}
	if err := wineComponentRemove(list[0]); err == nil {
		t.Fatal("removing twice should fail")
	}
}

func TestWineComponentsValidateDownloadAndEnv(t *testing.T) {
	dir := t.TempDir()
	body := []byte("an optional Wine component")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good.bin" {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	oldDir, oldValidate, oldDownload := wineComponentsDir, wineComponentsValidateURL, wineComponentDownload
	wineComponentsDir = func() string { return dir }
	wineComponentsValidateURL = func(raw string) error { return nil }
	wineComponentDownload = func(ctx context.Context, client *http.Client, raw, dest, sha string, max int64,
		report func(got, total int64)) error {
		resp, err := client.Get(raw)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > max {
			return errors.New("too large")
		}
		return os.WriteFile(dest, data, 0o644)
	}
	defer func() {
		wineComponentsDir, wineComponentsValidateURL, wineComponentDownload = oldDir, oldValidate, oldDownload
	}()

	manifest := `{"version":1,"components":[
	  {"name":"Good","url":"` + srv.URL + `/good.bin","sha256":"` + digest + `","file":"good.bin","envDir":"WINE_TEST_CACHE_DIR","detail":"A test component"},
	  {"name":"No digest","url":"` + srv.URL + `/good.bin","sha256":"","file":"bad.bin"},
	  {"name":"Bad path","url":"` + srv.URL + `/good.bin","sha256":"` + digest + `","file":"../escape.bin"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, wineComponentsFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := wineLoadComponents()
	if err != nil || len(list) != 3 {
		t.Fatalf("components = %v, %v", list, err)
	}
	if list[1].Problem == "" || list[2].Problem == "" {
		t.Fatalf("bad entries were not flagged: %+v", list)
	}
	if got := wineComponentsMissing(); len(got) != 1 || got[0].Name != "Good" {
		t.Fatalf("missing = %+v", got)
	}
	if line := wineComponentsSummaryNow(); !strings.Contains(line, "not installed") {
		t.Fatalf("setup line = %q", line)
	}
	if err := wineComponentFetch(context.Background(), list[0]); err != nil {
		t.Fatal(err)
	}
	if !wineComponentInstalled(list[0]) {
		t.Fatal("the component was not cached")
	}
	if err := wineComponentFetch(context.Background(), list[0]); err == nil {
		t.Fatal("installing twice should fail")
	}
	if got := wineComponentsEnv(); got["WINE_TEST_CACHE_DIR"] != dir {
		t.Fatalf("component env = %+v", got)
	}
	env := map[string]string{}
	wineApplyDefaultEnv(env, filepath.Join(dir, "prefix"))
	if env["WINE_TEST_CACHE_DIR"] != dir {
		t.Fatalf("launch env = %+v", env)
	}
	if err := wineComponentVerify(list[0]); err != nil {
		t.Fatalf("verify = %v", err)
	}
	cached := filepath.Join(dir, "good.bin")
	if err := os.WriteFile(cached, []byte("tampered after download"), 0o644); err != nil {
		t.Fatal(err)
	}
	if wineComponentInstalled(list[0]) {
		t.Fatal("a stamped file that changed is still trusted")
	}
	if err := wineComponentVerify(list[0]); err == nil {
		t.Fatal("verify accepted a tampered file")
	}
	if wineComponentInstalled(list[0]) {
		t.Fatal("a rejected file is still cached")
	}
	if err := wineComponentFetch(context.Background(), wineComponent{
		Name: "Wrong", URL: srv.URL + "/good.bin", SHA256: strings.Repeat("0", 64), File: "wrong.bin",
	}); err == nil {
		t.Fatal("a wrong digest was accepted")
	}
	if wineComponentInstalled(wineComponent{File: "wrong.bin"}) {
		t.Fatal("a rejected download left a cached file")
	}
	lines := strings.Join(wineComponentsLines(0), "\n")
	for _, want := range []string{"Good", "a SHA-256 is required", "bad file name", "Nothing is downloaded", "from 127.0.0.1", "Files downloaded:"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("card has no %q:\n%s", want, lines)
		}
	}
	wineComponentsProgress.mu.Lock()
	wineComponentsProgress.active, wineComponentsProgress.name = true, "Big component"
	wineComponentsProgress.got, wineComponentsProgress.total = 50<<20, 100<<20
	wineComponentsProgress.mu.Unlock()
	progressLines := strings.Join(wineComponentsLines(0), "\n")
	for _, want := range []string{"50%", "Downloading Big component", "======------"} {
		if !strings.Contains(progressLines, want) {
			t.Fatalf("progress card has no %q:\n%s", want, progressLines)
		}
	}
	wineComponentsProgress.mu.Lock()
	wineComponentsProgress.active = false
	wineComponentsProgress.mu.Unlock()
}
