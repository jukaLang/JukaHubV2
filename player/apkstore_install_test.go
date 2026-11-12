package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestApkStoreCheckInstall(t *testing.T) {
	e := apkStoreCatalogEntry{
		ID: "native-demo", Title: "Native demo", Package: "org.example.native",
		Kind: "title", Status: apkStatusExperimental,
		URL:    "https://github.com/example/native/releases/download/v1/demo.apk",
		SHA256: strings.Repeat("ab", 32), Size: 1024,
	}
	info := apkStoreInfo{
		Manifest:  apkManifest{Package: e.Package, Launcher: "android.app.NativeActivity"},
		ChosenABI: "arm64-v8a", EntryLib: "lib/arm64-v8a/libdemo.so",
		Tier: apkTierNative, Verdict: "Native APK — try it (experimental)",
	}
	if err := apkStoreCheckInstall(e, info); err != nil {
		t.Fatalf("native APK rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*apkStoreCatalogEntry, *apkStoreInfo)
	}{
		{"missing hash", func(e *apkStoreCatalogEntry, _ *apkStoreInfo) { e.SHA256 = "" }},
		{"different package", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.Manifest.Package = "org.other" }},
		{"unreadable manifest", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.ManifestErr = "bad XML" }},
		{"split APK", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.Manifest.Split = "config.arm64" }},
		{"Java launcher", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.Manifest.Launcher = "org.example.MainActivity" }},
		{"Java app", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.Tier = apkTierJava }},
		{"32-bit APK", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.ChosenABI = "armeabi-v7a" }},
		{"missing library", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) { i.EntryLib = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			badEntry, badInfo := e, info
			tc.edit(&badEntry, &badInfo)
			if err := apkStoreCheckInstall(badEntry, badInfo); err == nil {
				t.Error("accepted an APK that cannot be installed")
			}
		})
	}
}

func TestApkStorePinDownloads(t *testing.T) {
	pinned := apkStoreCatalogEntry{
		ID: "demo", Title: "Demo", Package: "org.example.demo", Kind: "title", Status: apkStatusExperimental,
		URL:    "https://github.com/example/demo/releases/download/v1/demo.apk",
		SHA256: strings.Repeat("ab", 32), Size: 1024,
	}
	builtin := apkStoreCatalogData{Entries: []apkStoreCatalogEntry{pinned}}
	remote := apkStoreCatalogData{Entries: []apkStoreCatalogEntry{
		{ID: "demo", Title: "Impostor", Package: pinned.Package, Kind: "title", Status: apkStatusWorks,
			URL: "https://github.com/attacker/app/releases/download/v1/app.apk", SHA256: strings.Repeat("cd", 32), Size: 1024},
		{ID: "new", Title: "New", Package: "org.example.new", Kind: "title", Status: apkStatusWorks,
			URL: "https://github.com/attacker/app/releases/download/v1/app.apk", SHA256: strings.Repeat("cd", 32), Size: 1024},
	}}
	got := apkStorePinDownloads(remote, builtin)
	if got.Entries[0] != pinned {
		t.Errorf("remote changed a pinned app: %+v", got.Entries[0])
	}
	if got.Entries[1].downloadable() {
		t.Error("remote introduced an executable download")
	}
	full := apkStoreCatalogData{Entries: make([]apkStoreCatalogEntry, apkStoreCatalogMax)}
	for i := range full.Entries {
		full.Entries[i].ID = "remote-" + strconv.Itoa(i)
	}
	got = apkStorePinDownloads(full, builtin)
	found := false
	for _, e := range got.Entries {
		if e.ID == pinned.ID && e == pinned {
			found = true
		}
	}
	if !found {
		t.Error("a full remote catalogue hid the built-in APK")
	}
}
