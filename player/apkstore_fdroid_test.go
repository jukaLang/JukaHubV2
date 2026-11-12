package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFDroidSignedEntry(t *testing.T) {
	raw, err := os.ReadFile("testdata/fdroid/entry.json")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile("testdata/fdroid/entry.json.asc")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := fdroidVerifyEntry(raw, sig)
	if err != nil {
		t.Fatalf("official signed entry: %v", err)
	}
	if entry.Index.Name != "/index-v2.json" || entry.Index.Size <= 0 {
		t.Fatalf("unexpected signed index: %+v", entry.Index)
	}
	changed := bytes.Replace(raw, []byte("index-v2.json"), []byte("index-v1.json"), 1)
	if bytes.Equal(changed, raw) {
		t.Fatal("fixture did not contain index name")
	}
	if _, err := fdroidVerifyEntry(changed, sig); err == nil {
		t.Fatal("altered signed entry was accepted")
	}
}

func TestFDroidRefreshLive(t *testing.T) {
	if os.Getenv("JUKAHUB_FDROID_LIVE_TEST") != "1" {
		t.Skip("set JUKAHUB_FDROID_LIVE_TEST=1 to check the current signed repository")
	}
	fdroidCachePathOverride = t.TempDir() + "/catalog.json"
	defer func() { fdroidCachePathOverride = "" }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	list, err := fdroidRefresh(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Apps) < 4000 {
		t.Fatalf("only %d apps in current F-Droid index", len(list.Apps))
	}
	t.Logf("loaded %d apps from F-Droid's signed index", len(list.Apps))
	if _, err := fdroidReadCache(); err != nil {
		t.Fatalf("saved compact list: %v", err)
	}
	found := false
	for _, app := range list.Apps {
		if app.Package == "org.fdroid.fdroid" {
			found = true
			if !app.canDownload() {
				t.Fatal("F-Droid app has no verified download metadata")
			}
		}
	}
	if !found {
		t.Fatal("F-Droid app missing from its own repository")
	}
	var small fdroidApp
	for _, app := range list.Apps {
		if app.canDownload() && app.hasTrimuiABI() && app.Size > 0 && app.Size < 50000 &&
			(small.Package == "" || app.Size < small.Size) {
			small = app
		}
	}
	if small.Package == "" {
		t.Fatal("no small APK available to check the download path")
	}
	dest := filepath.Join(t.TempDir(), "fdroid-test.apk")
	if err := downloadToFile(ctx, fdroidClient(), small.asCatalogEntry().URL, dest, small.SHA256, small.Size); err != nil {
		t.Fatalf("download %s: %v", small.Package, err)
	}
	info := apkStoreAnalyze(dest, false)
	if info.Manifest.Package != small.Package {
		t.Fatalf("verified APK package = %q, want %q: %s", info.Manifest.Package, small.Package, info.ManifestErr)
	}
}

func TestFDroidParseIndexAndSelection(t *testing.T) {
	const raw = `{"repo":{},"packages":{"org.example.game":{"metadata":{"name":{"en-US":"Example Game"},"summary":{"en-US":"A game"},"categories":["Action Game"]},"versions":{"old":{"file":{"name":"/old.apk","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":100},"manifest":{"versionName":"1","versionCode":1,"nativecode":["armeabi-v7a"]}},"new":{"file":{"name":"/new.apk","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":200},"manifest":{"versionName":"2","versionCode":2,"nativecode":["arm64-v8a"]}}}},"org.example.bad":{"metadata":{"name":{"en-US":"Bad"}},"versions":{"x":{"file":{"name":"/../escape.apk","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":1},"manifest":{"versionCode":1}}}}}}`
	d, err := fdroidParseIndex(strings.NewReader(raw), 123, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Apps) != 1 || d.Apps[0].Name != "Example Game" || d.Apps[0].VersionCode != 2 ||
		!d.Apps[0].isGame() || !d.Apps[0].hasTrimuiABI() {
		t.Fatalf("wrong app selected: %+v", d.Apps)
	}
}
