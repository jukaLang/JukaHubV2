package main

// androidpak_splits_test.go — split APKs (AND-18): a base APK and the
// split_config pieces beside it are one app. AndroidPak finds the native
// libraries in the matching piece and stages them with the base's manifest,
// and the store lists the app once.

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSplitTestAPK writes a zip APK with a manifest and the given files.
func writeSplitTestAPK(t *testing.T, path string, manifest []byte, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create manifest entry: %v", err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close APK zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write APK: %v", err)
	}
}

// splitTestManifest is a split piece's manifest: same package and version as
// the base, with the split's own name.
func splitTestManifest(pkg string, version uint32, split string) []byte {
	root := axEl("manifest", []axAttr{
		{name: "package", typ: apkxTypeString, str: pkg},
		axInt("versionCode", axIDVersionCode, version),
		{name: "split", typ: apkxTypeString, str: split},
	}, axEl("application", nil))
	return buildAXML(root, false, false)
}

// writeSplitGame writes a base.apk (manifest and NativeActivity, no libraries)
// and split_config.arm64_v8a.apk (the game's library) into a temp folder.
func writeSplitGame(t *testing.T) (dir, base, split string) {
	t.Helper()
	dir = t.TempDir()
	base = filepath.Join(dir, "base.apk")
	split = filepath.Join(dir, "split_config.arm64_v8a.apk")
	writeSplitTestAPK(t, base, buildAXML(axTestManifest("com.example.splitgame", "Split Game", false, "game"), false, false), nil)
	writeSplitTestAPK(t, split, splitTestManifest("com.example.splitgame", 42, "config.arm64_v8a"), map[string][]byte{
		"lib/arm64-v8a/libgame.so": []byte("split-native-library"),
	})
	return dir, base, split
}

func TestAndroidPakStagesSplitNativeLibraries(t *testing.T) {
	_, base, split := writeSplitGame(t)

	libs, err := inspectAndroidPak(base, "")
	if err != nil {
		t.Fatalf("inspect base with its split: %v", err)
	}
	if libs.Chosen != "arm64-v8a" {
		t.Fatalf("chosen ABI = %q, want arm64-v8a", libs.Chosen)
	}
	if filepath.Base(libs.MainLib) != "libgame.so" {
		t.Fatalf("main library = %q, want libgame.so", libs.MainLib)
	}
	if len(libs.Splits) != 1 || libs.Splits[0] != split {
		t.Fatalf("splits = %v, want [%s]", libs.Splits, split)
	}

	stage := filepath.Join(t.TempDir(), "stage")
	libDir, err := stageAndroidPakInto(stage, base, libs)
	if err != nil {
		t.Fatalf("stage base with its split: %v", err)
	}
	if libDir != filepath.Join(stage, "lib", "arm64-v8a") {
		t.Fatalf("library dir = %q", libDir)
	}
	got, err := os.ReadFile(filepath.Join(stage, "lib", "arm64-v8a", "libgame.so"))
	if err != nil || string(got) != "split-native-library" {
		t.Fatalf("staged library = %q (err %v), want the split's bytes", got, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "AndroidManifest.xml")); err != nil {
		t.Fatalf("the base's manifest was not staged: %v", err)
	}
}

func TestAndroidPakIgnoresAnotherVersionsSplits(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.apk")
	writeSplitTestAPK(t, base, buildAXML(axTestManifest("com.example.splitgame", "Split Game", false, "game"), false, false), nil)
	// Same package, a different version: not a piece of this app.
	writeSplitTestAPK(t, filepath.Join(dir, "split_config.arm64_v8a.apk"),
		splitTestManifest("com.example.splitgame", 7, "config.arm64_v8a"), map[string][]byte{
			"lib/arm64-v8a/libgame.so": []byte("other-version"),
		})

	libs, err := inspectAndroidPak(base, "")
	if err == nil || !strings.Contains(err.Error(), "no native libraries") {
		t.Fatalf("inspect = %v (libs %+v), want the base read on its own", err, libs)
	}
}

func TestApkStoreGroupsBaseAndItsSplits(t *testing.T) {
	_, base, split := writeSplitGame(t)

	baseInfo := apkStoreAnalyze(base, false)
	splitInfo := apkStoreAnalyze(split, false)
	if splitInfo.Manifest.Split == "" {
		t.Fatalf("the split's manifest was not read: %+v", splitInfo.Manifest)
	}
	if !baseInfo.Tier.canAdd() {
		t.Fatalf("base with its pieces = %v (%q), want an app that can be added", baseInfo.Tier, baseInfo.Verdict)
	}

	grouped := apkStoreGroupSplits([]apkStoreInfo{baseInfo, splitInfo})
	if len(grouped) != 1 {
		t.Fatalf("grouped rows = %d, want one app row", len(grouped))
	}
	if got := grouped[0]; got.Path != base || got.SplitCount != 1 {
		t.Fatalf("grouped row = %s with %d splits, want %s with 1", got.Path, got.SplitCount, base)
	}

	// A piece whose base is not there keeps its own row and its own verdict.
	alone := apkStoreGroupSplits([]apkStoreInfo{splitInfo})
	if len(alone) != 1 || alone[0].Tier != apkTierSplit {
		t.Fatalf("split without its base = %d rows, tier %v; want its own split row", len(alone), alone[0].Tier)
	}
}
