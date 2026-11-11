package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidPakDetectTitle(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		abi     string
		title   string
		pkg     string
		errHas  string
	}{
		{"plain NDK game", []string{"AndroidManifest.xml", "lib/arm64-v8a/libgame.so"}, "arm64-v8a", "", "", ""},
		{"Minecraft arm64", []string{"lib/arm64-v8a/libminecraftpe.so", "lib/arm64-v8a/libfmod.so"}, "arm64-v8a",
			"Minecraft", "com.mojang.minecraftpe", ""},
		{"Minecraft 32-bit only", []string{"lib/armeabi-v7a/libminecraftpe.so"}, "armeabi-v7a", "", "",
			"arm64-v8a build"},
		{"Xamarin app", []string{"lib/arm64-v8a/libmonodroid.so", "lib/arm64-v8a/libmonosgen-2.0.so"}, "arm64-v8a",
			"", "", "NDK (native C/C++) games only"},
		{"Terraria", []string{"lib/arm64-v8a/libmonodroid.so", "assemblies/Terraria.dll"}, "arm64-v8a", "", "",
			"Terraria's mobile edition"},
	}
	for _, c := range cases {
		got, err := androidPakDetectTitle(c.entries, c.abi)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%s: error = %v, want one containing %q", c.name, err, c.errHas)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got.Name != c.title || got.Package != c.pkg {
			t.Errorf("%s: title %q / package %q, want %q / %q", c.name, got.Name, got.Package, c.title, c.pkg)
		}
	}
}

// A folder that cannot start games (an empty Apps/AndroidRT) must not hide the
// copy JukaHub bundles; a user's working copy wins over it; with neither, the
// error is the "not installed" one the APK Store offers to put right.
func TestFindAndroidRuntimeOrder(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "Apps", "AndroidRT")
	bundled := filepath.Join(root, "JukaHub", "androidrt")
	for _, dir := range []string{user, bundled} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundled, "loader"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUKAHUB_ANDROIDRT", "")
	oldUser, oldBundled, oldDownload := androidRuntimeUserDirs, androidRuntimeBundledDirFn, androidRuntimeDownloadDirFn
	t.Cleanup(func() {
		androidRuntimeUserDirs, androidRuntimeBundledDirFn, androidRuntimeDownloadDirFn = oldUser, oldBundled, oldDownload
	})
	androidRuntimeUserDirs = []string{user}
	androidRuntimeBundledDirFn = func() string { return bundled }
	androidRuntimeDownloadDirFn = func() string { return "" }

	found, err := findAndroidRuntime(nil)
	if err != nil || found.Dir != bundled || found.Source != androidRTSourceBundled || len(found.Skipped) != 1 {
		t.Fatalf("empty user folder: got %+v, %v; want the bundled copy, the user folder passed over", found, err)
	}

	if err := os.WriteFile(filepath.Join(user, "launch.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, err := findAndroidRuntime(nil); err != nil || found.Dir != user || found.Source != androidRTSourceUser {
		t.Fatalf("working user folder: got %+v, %v; want the user's copy", found, err)
	}

	androidRuntimeUserDirs = nil
	androidRuntimeBundledDirFn = func() string { return "" }
	if _, err := findAndroidRuntime(nil); !errors.Is(err, errAndroidRuntimeMissing) {
		t.Fatalf("no runtime anywhere: error %v, want errAndroidRuntimeMissing", err)
	}
}
