package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// apkStoreTestSHA256 is the SHA-256 of b as SHA256File writes it.
func apkStoreTestSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The built-in catalogue parses, is not empty, and — honestly — carries no
// download nobody could verify.
func TestApkStoreBuiltinCatalogue(t *testing.T) {
	data, err := parseApkStoreCatalog(apkStoreCatalogJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Entries) < 4 {
		t.Fatalf("only %d entries", len(data.Entries))
	}
	ids := map[string]bool{}
	for _, e := range data.Entries {
		if e.downloadable() {
			t.Errorf("%s ships a download; the built-in list must not carry unverified ones", e.ID)
		}
		if e.Status == apkStatusWontRun && e.Route == "" {
			t.Errorf("%s says it won't run but names no route that works", e.ID)
		}
		ids[e.ID] = true
	}
	for _, want := range []string{"minecraft-bedrock", "terraria-mobile", "unity-games", "java-apps"} {
		if !ids[want] {
			t.Errorf("missing entry %s", want)
		}
	}
}

// The copy at the repository root is what generate.jukalang.com serves; it
// must stay the same list.
func TestApkStoreRootCatalogueMatches(t *testing.T) {
	root, err := os.ReadFile(filepath.Join("..", "apk-catalog.json"))
	if err != nil {
		t.Skip("repository root not available:", err)
	}
	a, errA := parseApkStoreCatalog(root)
	b, errB := parseApkStoreCatalog(apkStoreCatalogJSON)
	if errA != nil || errB != nil {
		t.Fatalf("parse: %v / %v", errA, errB)
	}
	if a.Updated != b.Updated || len(a.Entries) != len(b.Entries) {
		t.Errorf("root apk-catalog.json (%s, %d entries) differs from player/apkstore_catalog.json (%s, %d)",
			a.Updated, len(a.Entries), b.Updated, len(b.Entries))
	}
}

func TestApkStoreCatalogueValidation(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	raw := `{"schemaVersion":1,"updated":"2026-12-01","entries":[
	 {"id":"good","title":"Good","status":"works","summary":"Fine.","url":"https://github.com/o/r/releases/download/v1/good.apk","sha256":"` + sum + `","size":1000},
	 {"id":"nohash","title":"No hash","status":"works","summary":"Fine.","url":"https://github.com/o/r/releases/download/v1/x.apk","size":1000},
	 {"id":"http","title":"Plain HTTP","status":"works","summary":"Fine.","url":"http://github.com/o/r/x.apk","sha256":"` + sum + `","size":10},
	 {"id":"host","title":"Other host","status":"works","summary":"Fine.","url":"https://example.com/x.apk","sha256":"` + sum + `","size":10},
	 {"id":"notapk","title":"Zip","status":"works","summary":"Fine.","url":"https://github.com/o/r/x.zip","sha256":"` + sum + `","size":10},
	 {"id":"huge","title":"Huge","status":"works","summary":"Fine.","url":"https://github.com/o/r/x.apk","sha256":"` + sum + `","size":99999999999},
	 {"id":"Bad ID","title":"x","status":"works","summary":"x"},
	 {"id":"badstatus","title":"x","status":"perhaps","summary":"x"},
	 {"id":"notitle","title":"","status":"works","summary":"x"},
	 {"id":"good","title":"Duplicate","status":"works","summary":"x"},
	 {"id":"ctl","title":"Line\nbreak\u0007","status":"WontRun","summary":"x","route":"r"}
	]}`
	data, err := parseApkStoreCatalog([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]apkStoreCatalogEntry{}
	for _, e := range data.Entries {
		byID[e.ID] = e
	}
	if !byID["good"].downloadable() || byID["good"].Title != "Good" {
		t.Errorf("good entry: %+v", byID["good"])
	}
	for _, id := range []string{"nohash", "http", "host", "notapk", "huge"} {
		e, ok := byID[id]
		if !ok {
			t.Errorf("%s: the entry itself should stay as information", id)
			continue
		}
		if e.downloadable() || e.URL != "" {
			t.Errorf("%s: an invalid download survived: %+v", id, e)
		}
	}
	for _, id := range []string{"Bad ID", "badstatus", "notitle"} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s: an invalid entry was kept", id)
		}
	}
	if c := byID["ctl"]; c.Title != "Line break" || c.Status != apkStatusWontRun {
		t.Errorf("control characters / status case: %+v", c)
	}
	for _, bad := range []string{`{"schemaVersion":2,"updated":"2026-12-01","entries":[]}`, `{"schemaVersion":1,"entries":[]}`, `not json`} {
		if _, err := parseApkStoreCatalog([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestApkStoreRefreshCatalogue(t *testing.T) {
	dir := t.TempDir()
	oldPath := apkStoreCatalogPathOverride
	apkStoreCatalogPathOverride = filepath.Join(dir, "catalog.json")
	apkStoreCatalogState.mu.Lock()
	oldLoaded, oldData, oldSource := apkStoreCatalogState.loaded, apkStoreCatalogState.data, apkStoreCatalogState.source
	apkStoreCatalogState.loaded = false
	apkStoreCatalogState.mu.Unlock()
	// The lists below are dated 2999: the clock says so too, so they are not
	// "in the future".
	oldNow := apkStoreCatalogNow
	apkStoreCatalogNow = func() time.Time { return time.Date(2999, 1, 2, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		apkStoreCatalogPathOverride = oldPath
		apkStoreCatalogNow = oldNow
		apkStoreCatalogState.mu.Lock()
		apkStoreCatalogState.loaded, apkStoreCatalogState.data, apkStoreCatalogState.source = oldLoaded, oldData, oldSource
		apkStoreCatalogState.mu.Unlock()
	})
	builtin, _ := apkStoreCatalog()
	newer := `{"schemaVersion":1,"updated":"2999-01-01","entries":[{"id":"x","title":"X","status":"experimental","summary":"New."}]}`
	older := `{"schemaVersion":1,"updated":"2000-01-01","entries":[{"id":"x","title":"X","status":"experimental","summary":"Old."}]}`
	body := older
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	updated, err := apkStoreRefreshCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil || updated {
		t.Fatalf("an older list replaced the built-in one: updated=%v err=%v", updated, err)
	}
	if data, _ := apkStoreCatalog(); data.Updated != builtin.Updated {
		t.Errorf("list in use = %s", data.Updated)
	}
	body = newer
	updated, err = apkStoreRefreshCatalog(context.Background(), srv.Client(), srv.URL)
	if err != nil || !updated {
		t.Fatalf("a newer list was not taken: updated=%v err=%v", updated, err)
	}
	data, src := apkStoreCatalog()
	if data.Updated != "2999-01-01" || !strings.Contains(src, "2999-01-01") {
		t.Errorf("list in use = %s (%s)", data.Updated, src)
	}
	// It survives a restart: the saved copy is read back.
	apkStoreCatalogState.mu.Lock()
	apkStoreCatalogState.loaded = false
	apkStoreCatalogState.mu.Unlock()
	if data, _ := apkStoreCatalog(); data.Updated != "2999-01-01" {
		t.Errorf("saved copy not used after reload: %s", data.Updated)
	}
	body = "garbage"
	if _, err := apkStoreRefreshCatalog(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Error("a broken list was accepted")
	}
	// Dated far past today, a list would pin itself over every later one.
	body = `{"schemaVersion":1,"updated":"3999-12-31","entries":[{"id":"x","title":"X","status":"experimental","summary":"Pinned."}]}`
	if updated, err := apkStoreRefreshCatalog(context.Background(), srv.Client(), srv.URL); updated || err == nil {
		t.Errorf("a list from the far future was taken: updated=%v err=%v", updated, err)
	}
	// Newer, but most of its entries are unreadable to this build.
	body = `{"schemaVersion":1,"updated":"2999-01-02","entries":[
	 {"id":"ok","title":"OK","status":"experimental","summary":"Fine."},
	 {"id":"a","title":"A","status":"someday","summary":"New status."},
	 {"id":"b","title":"B","status":"someday","summary":"New status."}]}`
	if updated, _ := apkStoreRefreshCatalog(context.Background(), srv.Client(), srv.URL); updated {
		t.Error("a list that lost most of its entries replaced a whole one")
	}
}

// A handheld whose clock was never set thinks it is 1970: the built-in list's
// date stands in for today, so a list published after it still counts.
func TestApkStoreCatalogTooNew(t *testing.T) {
	oldNow := apkStoreCatalogNow
	t.Cleanup(func() { apkStoreCatalogNow = oldNow })
	apkStoreCatalogNow = func() time.Time { return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC) }
	if apkStoreCatalogTooNew("2026-11-12", "2026-11-10") {
		t.Error("a list two days after the built-in one was refused on an unset clock")
	}
	if !apkStoreCatalogTooNew("2027-06-01", "2026-11-10") {
		t.Error("a list months ahead of both was taken")
	}
	apkStoreCatalogNow = func() time.Time { return time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC) }
	if apkStoreCatalogTooNew("2027-06-01", "2026-11-10") || apkStoreCatalogTooNew("2027-06-03", "2026-11-10") {
		t.Error("a list dated today (or two days ahead) was refused")
	}
	if !apkStoreCatalogTooNew("not a date", "2026-11-10") {
		t.Error("an unreadable date passed")
	}
}

// A download never replaces a file it did not write: a different file with
// the entry's name pushes it to <id>-<hash>.apk, and the right file already
// there is used as it is.
func TestApkStoreDownloadDest(t *testing.T) {
	dir := t.TempDir()
	content := []byte("the real apk")
	sum := apkStoreTestSHA256(content)
	e := apkStoreCatalogEntry{ID: "game", SHA256: sum}
	dest, done, err := apkStoreDownloadDest(dir, e)
	if err != nil || done || filepath.Base(dest) != "game.apk" {
		t.Fatalf("empty folder: %q %v %v", dest, done, err)
	}
	if err := os.WriteFile(dest, []byte("the user's own file"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest, done, err = apkStoreDownloadDest(dir, e)
	if err != nil || done || filepath.Base(dest) != "game-"+sum[:8]+".apk" {
		t.Fatalf("name taken: %q %v %v", dest, done, err)
	}
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if dest2, done, err := apkStoreDownloadDest(dir, e); err != nil || !done || dest2 != dest {
		t.Errorf("verified file present: %q %v %v", dest2, done, err)
	}
	if err := os.WriteFile(dest, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := apkStoreDownloadDest(dir, e); err == nil {
		t.Error("both names held other files, and one would have been replaced")
	}
}

func TestApkStoreDownloadRefusesUnverified(t *testing.T) {
	e := apkStoreCatalogEntry{ID: "x", Title: "X", URL: "https://github.com/o/r/x.apk", Size: 10}
	if _, err := apkStoreDownload(context.Background(), e); err == nil {
		t.Error("a download without a SHA-256 was attempted")
	}
	if got := apkStoreDownloadName(apkStoreCatalogEntry{ID: "minecraft-bedrock"}); got != "minecraft-bedrock.apk" {
		t.Errorf("download name = %q", got)
	}
}
