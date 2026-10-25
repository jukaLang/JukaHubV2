package main

// Tests for the AndroidPak tier: APK triage, staging bounds, profile
// validation and command planning. The Android runtime itself is not
// installed in CI, so planning asserts the exact failure message rather than
// a launched process.

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestApk builds a synthetic APK in dir and returns its path.
func writeTestApk(t *testing.T, dir string, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, "game.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

func sampleApkEntries() map[string][]byte {
	return map[string][]byte{
		"AndroidManifest.xml":        []byte("binary manifest"),
		"resources.arsc":             []byte("resources"),
		"lib/arm64-v8a/libsmall.so":  []byte(strings.Repeat("a", 10)),
		"lib/arm64-v8a/libgame.so":   []byte(strings.Repeat("g", 100)), // largest → main lib
		"lib/armeabi-v7a/libgame.so": []byte(strings.Repeat("m", 40)),
		"lib/x86/libgame.so":         []byte(strings.Repeat("x", 5)),
		"assets/data.bin":            []byte("asset data"),
		"res/values/strings.xml":     []byte("strings"),
		"classes.dex":                []byte("dex"),
		"META-INF/MANIFEST.MF":       []byte("signature"),
	}
}

func TestInspectAndroidPakChoosesArm64AndLargestLib(t *testing.T) {
	apk := writeTestApk(t, t.TempDir(), sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	if libs.Chosen != "arm64-v8a" {
		t.Fatalf("chosen ABI = %q, want arm64-v8a", libs.Chosen)
	}
	if libs.MainLib != "lib/arm64-v8a/libgame.so" {
		t.Fatalf("main lib = %q, want lib/arm64-v8a/libgame.so", libs.MainLib)
	}
	if len(libs.Abis) != 3 {
		t.Fatalf("abis = %v, want 3 shipped ABIs", libs.Abis)
	}
}

func TestInspectAndroidPakRequestedMainLib(t *testing.T) {
	apk := writeTestApk(t, t.TempDir(), sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "libsmall.so")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	if libs.MainLib != "lib/arm64-v8a/libsmall.so" {
		t.Fatalf("main lib = %q, want lib/arm64-v8a/libsmall.so", libs.MainLib)
	}
	if _, err := inspectAndroidPak(apk, "libmissing.so"); err == nil {
		t.Fatal("missing main lib must be refused")
	}
}

func TestInspectAndroidPakRefusesNonAPKAndDalvikOnly(t *testing.T) {
	dir := t.TempDir()
	notApk := writeTestApk(t, dir, map[string][]byte{"random.bin": []byte("x")})
	if _, err := inspectAndroidPak(notApk, ""); err == nil || !strings.Contains(err.Error(), "not an APK") {
		t.Fatalf("want not-an-APK error, got %v", err)
	}
	dalvik := writeTestApk(t, dir, map[string][]byte{
		"AndroidManifest.xml": []byte("m"),
		"classes.dex":         []byte("d"),
	})
	if _, err := inspectAndroidPak(dalvik, ""); err == nil || !strings.Contains(err.Error(), "no native libraries") {
		t.Fatalf("want no-native-libs error, got %v", err)
	}
}

func TestStageAndroidPakExtractsSubsetAndCleansOldStage(t *testing.T) {
	t.Setenv("JUKAHUB_ANDROID_STAGE", t.TempDir())
	apk := writeTestApk(t, t.TempDir(), sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	stageDir, libDir, err := stageAndroidPak("tsp-test", apk, libs)
	if err != nil {
		t.Fatalf("stageAndroidPak: %v", err)
	}
	for _, want := range []string{
		filepath.Join(libDir, "libgame.so"),
		filepath.Join(libDir, "libsmall.so"),
		filepath.Join(stageDir, "assets", "data.bin"),
		filepath.Join(stageDir, "res", "values", "strings.xml"),
		filepath.Join(stageDir, "AndroidManifest.xml"),
		filepath.Join(stageDir, "resources.arsc"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("missing staged file %s", want)
		}
	}
	for _, absent := range []string{"classes.dex", "META-INF", "x86"} {
		if _, err := os.Stat(filepath.Join(stageDir, absent)); err == nil {
			t.Errorf("%s must not be staged", absent)
		}
	}
	// arm64 only: the other ABIs stay in the APK.
	if _, err := os.Stat(filepath.Join(stageDir, "lib", "armeabi-v7a", "libgame.so")); err == nil {
		t.Error("armeabi-v7a libs must not be staged for an arm64 run")
	}
	// A stale file from an earlier run must not survive.
	extra := filepath.Join(stageDir, "stale.bin")
	if err := os.WriteFile(extra, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageAndroidPak("tsp-test", apk, libs); err != nil {
		t.Fatalf("re-stage: %v", err)
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Error("old stage contents must be cleared before staging anew")
	}
	removeAndroidPakStage(stageDir)
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Error("stage must be removed")
	}
}

func TestStageAndroidPakRejectsHostileEntry(t *testing.T) {
	t.Setenv("JUKAHUB_ANDROID_STAGE", t.TempDir())
	apk := writeTestApk(t, t.TempDir(), map[string][]byte{
		"AndroidManifest.xml":        []byte("m"),
		"lib/arm64-v8a/lib.so":       []byte("l"),
		"assets/../../../etc/escape": []byte("nope"),
	})
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	if _, _, err := stageAndroidPak("tsp-evil", apk, libs); err == nil {
		t.Fatal("hostile entry must be refused")
	}
	removeAndroidPakStage(androidPakStageDir("tsp-evil"))
}

// preflightMemInfoTemp stores a meminfo file for a test and points the
// pre-flight at it (restored at the end).
func preflightMemInfoTemp(t *testing.T, availableKB int64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "meminfo")
	if availableKB < 0 {
		// No file at all: the unreadable-meminfo case.
		p = filepath.Join(p, "missing")
	} else if err := os.WriteFile(p, []byte(fmt.Sprintf("MemTotal: 1000000 kB\nMemFree: 1000 kB\nMemAvailable: %d kB\n", availableKB)), 0o644); err != nil {
		t.Fatalf("write meminfo: %v", err)
	}
	old := androidPakMemInfoPath
	androidPakMemInfoPath = p
	t.Cleanup(func() { androidPakMemInfoPath = old })
}

func TestAndroidPakPreflightRefusesTightMemory(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestApk(t, dir, sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	footprint, err := androidPakStageFootprint(apk, libs)
	if err != nil {
		t.Fatalf("footprint: %v", err)
	}
	// Fill up exactly to the floor with the hub reserve squeezed to zero,
	// then drop one byte — the launch must be refused with numbers.
	t.Setenv("JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB", "0")
	preflightMemInfoTemp(t, (footprint+androidPakRuntimeOverhead)>>10-1)
	err = androidPakPreflight(apk, libs)
	if err == nil {
		t.Fatal("launch with MemAvailable below the floor must be refused")
	}
	if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("refusal must name the numbers, got %q", err)
	}
	// One more byte of room and the same launch goes through.
	preflightMemInfoTemp(t, (footprint+androidPakRuntimeOverhead)>>10+2)
	if err := androidPakPreflight(apk, libs); err != nil {
		t.Fatalf("passing preflight: %v", err)
	}
}

func TestAndroidPakPreflightFootprintMatchesStage(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestApk(t, dir, sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	// The sample stages the arm64 libs only; the footprints of other ABIs,
	// classes.dex and META-INF must not count, exactly as the real staging
	// work skips them.
	footprint, err := androidPakStageFootprint(apk, libs)
	if err != nil {
		t.Fatalf("footprint: %v", err)
	}
	want := int64(10 + 100 + len("asset data") + len("strings") + len("binary manifest") + len("resources"))
	if footprint != want {
		t.Errorf("footprint = %d, want %d", footprint, want)
	}
}

func TestAndroidPakPreflightSkipsUnreadableMeminfo(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestApk(t, dir, sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	preflightMemInfoTemp(t, -1)
	if err := androidPakPreflight(apk, libs); err != nil {
		t.Fatalf("unknown memory must not block a launch, got %v", err)
	}
}

func TestAndroidPakPreflightReserveOverride(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestApk(t, dir, sampleApkEntries())
	libs, err := inspectAndroidPak(apk, "")
	if err != nil {
		t.Fatalf("inspectAndroidPak: %v", err)
	}
	footprint, err := androidPakStageFootprint(apk, libs)
	if err != nil {
		t.Fatalf("footprint: %v", err)
	}
	// Room only up to footprint + overhead + 1 MiB: the default reserve is
	// 96 MiB, so only the override brings this launch into range.
	preflightMemInfoTemp(t, (footprint+androidPakRuntimeOverhead+1<<20)>>10+4)
	if err := androidPakPreflight(apk, libs); err == nil {
		t.Fatal("default-reserve room is smaller than the floor, launch must be refused")
	}
	t.Setenv("JUKAHUB_ANDROIDPAK_MEM_RESERVE_MB", "1")
	if err := androidPakPreflight(apk, libs); err != nil {
		t.Fatalf("reserved-MiB override must let the launch through, got %v", err)
	}
}

func TestAndroidPakProfileValidation(t *testing.T) {
	profile := GameProfile{
		SchemaVersion: gameProfileSchemaVersion,
		ID:            "tier1-game",
		Title:         "A Tier 1 Game",
		Runtime:       gameRuntimeAndroidPak,
		Dir:           "Roms/Android",
		Executable:    "game.apk",
		MainLib:       "libgame.so",
	}
	if err := validateGameProfile(&profile); err != nil {
		t.Fatalf("valid androidpak profile rejected: %v", err)
	}
	bad := profile
	bad.MainLib = "../weaponized.so"
	if err := validateGameProfile(&bad); err == nil {
		t.Fatal("path-like mainLib must be refused")
	}
	bad = profile
	bad.Executable = "game.xyz"
	if err := validateGameProfile(&bad); err == nil || !strings.Contains(err.Error(), ".apk") {
		t.Fatalf("non-apk executable must be refused, got %v", err)
	}
	if got := gameProfileRuntimeLabel(gameRuntimeAndroidPak); got != "ANDROID" {
		t.Fatalf("label = %q, want ANDROID", got)
	}
}

func TestAndroidPakRuntimePlanFailsWithoutRuntime(t *testing.T) {
	if os.Getenv("JUKAHUB_ANDROIDRT") != "" {
		t.Skip("JUKAHUB_ANDROIDRT set on this host")
	}
	profile := GameProfile{Runtime: gameRuntimeAndroidPak}
	_, _, err := planGameRuntime(profile, "/fake/game.apk", func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if err == nil || !strings.Contains(err.Error(), "android runtime not found") {
		t.Fatalf("want exact runtime-missing message, got %v", err)
	}
}

func TestAndroidRuntimeCommandForms(t *testing.T) {
	rt := t.TempDir()
	if _, _, err := androidRuntimeCommand(rt, func(string) (string, error) { return "/bin/sh", nil }); err == nil {
		t.Fatal("empty runtime dir must be refused")
	}
	loader := filepath.Join(rt, "loader")
	if err := os.WriteFile(loader, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	command, prefix, err := androidRuntimeCommand(rt, func(string) (string, error) { return "/bin/sh", nil })
	if err != nil || command != loader || len(prefix) != 0 {
		t.Fatalf("loader form = (%q, %v, %v)", command, prefix, err)
	}
	if err := os.Remove(loader); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(rt, "launch.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command, prefix, err = androidRuntimeCommand(rt, func(string) (string, error) { return "/bin/sh", nil })
	if err != nil || command != "/bin/sh" || len(prefix) != 1 || prefix[0] != script {
		t.Fatalf("launch.sh form = (%q, %v, %v)", command, prefix, err)
	}
}
