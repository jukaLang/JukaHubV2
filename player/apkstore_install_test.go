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
	// A Java subclass may supply the Android launcher while the library still
	// exports an entry AndroidRT can call directly.
	subclass := info
	subclass.Manifest.Launcher = "org.example.NativeLauncher"
	subclass.Entry = apkEntryFound
	subclass.Tier = apkTierMaybe
	if err := apkStoreCheckInstall(e, subclass); err != nil {
		t.Fatalf("native entry behind a Java launcher rejected: %v", err)
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
		{"Java launcher without native entry", func(_ *apkStoreCatalogEntry, i *apkStoreInfo) {
			i.Manifest.Launcher, i.Tier, i.Entry = "org.example.MainActivity", apkTierMaybe, apkEntryNone
		}},
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

func TestApkStoreCatalogMatchOnCard(t *testing.T) {
	e := apkStoreCatalogEntry{Package: "org.example.demo", Kind: "title"}
	apks := []apkStoreInfo{
		{Path: "java.apk", Manifest: apkManifest{Package: e.Package, VersionCode: 9}, Tier: apkTierJava},
		{Path: "other.apk", Manifest: apkManifest{Package: "org.other", VersionCode: 99}, Tier: apkTierNative},
		{Path: "native-old.apk", Manifest: apkManifest{Package: e.Package, VersionCode: 1}, Tier: apkTierNative},
		{Path: "native-new.apk", Manifest: apkManifest{Package: e.Package, VersionCode: 2}, Tier: apkTierNative},
	}
	match := apkStoreCatalogMatchOnCard(e, apks, nil)
	if !match.Found || match.Info.Path != "native-new.apk" || match.InGames {
		t.Fatalf("preferred local native APK: %+v", match)
	}
	games := map[string]apkStoreGameState{"native-old.apk": {ID: "demo"}}
	match = apkStoreCatalogMatchOnCard(e, apks, games)
	if !match.InGames || match.Info.Path != "native-old.apk" {
		t.Fatalf("preferred the copy already in Games: %+v", match)
	}
	e.Kind = "category"
	if got := apkStoreCatalogMatchOnCard(e, apks, games); got.Found {
		t.Fatalf("category matched a title: %+v", got)
	}
}

func TestApkStoreEntriesIncludeNativeLocalPackages(t *testing.T) {
	catalog := []apkStoreCatalogEntry{{ID: "known", Package: "org.example.known", Kind: "title"}}
	apks := []apkStoreInfo{
		{Path: "known.apk", Manifest: apkManifest{Package: "org.example.known"}, Tier: apkTierNative},
		{Path: "native.apk", Label: "Native", Manifest: apkManifest{Package: "org.example.native"}, Tier: apkTierNative},
		{Path: "native-copy.apk", Manifest: apkManifest{Package: "org.example.native"}, Tier: apkTierNative},
		{Path: "wrong-cpu.apk", Label: "Older native game", Manifest: apkManifest{Package: "org.example.arm32", HasCode: false, Launcher: "android.app.NativeActivity"}, ABIs: []string{"armeabi-v7a"}, Tier: apkTierWrongCPU, Verdict: "Wrong CPU"},
		{Path: "wrong-cpu-subclass.apk", Label: "Older native subclass", Manifest: apkManifest{Package: "org.example.arm32subclass", HasCode: true, Launcher: "org.example.NativeLauncher", NativeLib: "game"}, ABIs: []string{"armeabi-v7a"}, Tier: apkTierWrongCPU},
		{Path: "split.apk", Label: "Split native game", Manifest: apkManifest{Package: "org.example.split", HasCode: true, Launcher: "com.google.androidgamesdk.GameActivity"}, Tier: apkTierSplit},
		{Path: "java.apk", Manifest: apkManifest{Package: "org.example.java", HasCode: true, Launcher: "org.example.Main"}, ABIs: []string{"arm64-v8a"}, Tier: apkTierJava},
	}
	entries := apkStoreEntries(catalog, apks)
	if len(entries) != 5 || entries[0].ID != "known" || entries[1].Package != "org.example.native" ||
		entries[2].Package != "org.example.arm32" || entries[3].Package != "org.example.arm32subclass" ||
		entries[4].Package != "org.example.split" {
		t.Fatalf("Store local candidates: %+v", entries)
	}
	if entries[2].Get == "" || entries[2].Status != apkStatusExperimental {
		t.Fatalf("non-installable native game lacks a listing: %+v", entries[2])
	}
}

func TestApkStorePinBuiltins(t *testing.T) {
	pinned := apkStoreCatalogEntry{
		ID: "demo", Title: "Demo", Package: "org.example.demo", Kind: "title", Status: apkStatusExperimental,
		URL:    "https://github.com/example/demo/releases/download/v1/demo.apk",
		SHA256: strings.Repeat("ab", 32), Size: 1024,
	}
	listed := apkStoreCatalogEntry{ID: "listed", Title: "Known game", Kind: "title", Status: apkStatusExperimental}
	blocked := apkStoreCatalogEntry{ID: "blocked", Title: "Blocked game", Kind: "title", Status: apkStatusWontRun, Route: "Android"}
	builtin := apkStoreCatalogData{Entries: []apkStoreCatalogEntry{pinned, listed, blocked}}
	remote := apkStoreCatalogData{Entries: []apkStoreCatalogEntry{
		{ID: "demo", Title: "Impostor", Package: pinned.Package, Kind: "title", Status: apkStatusWorks,
			URL: "https://github.com/attacker/app/releases/download/v1/app.apk", SHA256: strings.Repeat("cd", 32), Size: 1024},
		{ID: "blocked", Title: "Fake fix", Kind: "title", Status: apkStatusWorks},
		{ID: "new", Title: "New", Package: "org.example.new", Kind: "title", Status: apkStatusWorks,
			URL: "https://github.com/attacker/app/releases/download/v1/app.apk", SHA256: strings.Repeat("cd", 32), Size: 1024},
	}}
	got := apkStorePinBuiltins(remote, builtin)
	if got.Entries[0] != pinned {
		t.Errorf("remote changed a pinned app: %+v", got.Entries[0])
	}
	if got.Entries[1] != blocked {
		t.Errorf("remote changed a built-in blocker: %+v", got.Entries[1])
	}
	if got.Entries[2].downloadable() {
		t.Error("remote introduced an executable download")
	}
	if got.Entries[3] != listed {
		t.Errorf("remote hid a built-in native game: %+v", got.Entries[3])
	}
	full := apkStoreCatalogData{Entries: make([]apkStoreCatalogEntry, apkStoreCatalogMax)}
	for i := range full.Entries {
		full.Entries[i].ID = "remote-" + strconv.Itoa(i)
	}
	got = apkStorePinBuiltins(full, builtin)
	found := map[string]bool{}
	for _, e := range got.Entries {
		for _, want := range builtin.Entries {
			if e == want {
				found[e.ID] = true
			}
		}
	}
	for _, want := range builtin.Entries {
		if !found[want.ID] {
			t.Errorf("a full remote catalogue hid built-in title %s", want.ID)
		}
	}
}
