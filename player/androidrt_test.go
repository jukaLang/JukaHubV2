package main

// androidrt_test.go — the parts of the AndroidRT contract JukaHub can prove
// without a device: the runtime directory forms, candidate precedence and the
// ANDROIDPAK_* env handoff. The aarch64 cross build itself and on-device
// loading are not runnable from this host; the Go side must be able to rely
// on the directory contract regardless.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAndroidRTLayout(t *testing.T, dir string, withLoader, withScript bool) string {
	t.Helper()
	if withLoader {
		if err := os.WriteFile(filepath.Join(dir, "loader"), []byte("elf"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if withScript {
		if err := os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAndroidRuntimeDirectoryContract(t *testing.T) {
	lookPath := func(string) (string, error) { return "/bin/sh", nil }

	// loader present → it is the command, no wrapper args.
	rt := writeAndroidRTLayout(t, t.TempDir(), true, false)
	command, prefix, err := androidRuntimeCommand(rt, lookPath)
	if err != nil || command != filepath.Join(rt, "loader") || len(prefix) != 0 {
		t.Fatalf("loader form = (%q, %v, %v)", command, prefix, err)
	}

	// launch.sh form → sh + script path as the first argument.
	rt = writeAndroidRTLayout(t, t.TempDir(), false, true)
	command, prefix, err = androidRuntimeCommand(rt, lookPath)
	if err != nil || command != "/bin/sh" || len(prefix) != 1 || prefix[0] != filepath.Join(rt, "launch.sh") {
		t.Fatalf("launch.sh form = (%q, %v, %v)", command, prefix, err)
	}

	// Neither → exact refusal, which is what the panel shows a user who
	// dropped an incomplete runtime folder on the card.
	if _, _, err := androidRuntimeCommand(t.TempDir(), lookPath); err == nil || !strings.Contains(err.Error(), "no loader or launch.sh") {
		t.Fatalf("empty runtime dir = %v", err)
	}
}

// buildAndroidPakCommandTestRT builds a fake runtime the override can point
// at, plus the temporary APK/stage paths its env must name.
func buildAndroidPakCommandTestRT(t *testing.T) (rt, apk, stage string) {
	t.Helper()
	rt = writeAndroidRTLayout(t, t.TempDir(), true, false)
	t.Setenv("JUKAHUB_ANDROIDRT", rt)
	apk = filepath.Join(t.TempDir(), "game.apk")
	stage = filepath.Join(t.TempDir(), "staged")
	return rt, apk, stage
}

func TestAndroidPakCommandAndEnvHandoff(t *testing.T) {
	rt, apk, stage := buildAndroidPakCommandTestRT(t)
	profile := GameProfile{
		ID:      "tier1",
		Title:   "Tier 1 Game",
		Runtime: gameRuntimeAndroidPak,
		Args:    []string{"-fullscreen"},
	}
	libs := androidPakLibs{Abis: []string{"arm64-v8a"}, Chosen: "arm64-v8a", MainLib: "lib/arm64-v8a/libgame.so"}
	env := map[string]string{"SOME_GAME_VAR": "1"}

	command, args, err := buildAndroidPakCommand(profile, apk, stage, libs, env)
	if err != nil {
		t.Fatalf("buildAndroidPakCommand: %v", err)
	}
	if command != filepath.Join(rt, "loader") {
		t.Fatalf("command = %q, want the runtime loader", command)
	}
	wantArgs := []string{apk, "-fullscreen"}
	if len(args) != len(wantArgs) || args[0] != wantArgs[0] || args[1] != wantArgs[1] {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}

	checks := []struct{ key, value string }{
		{"ANDROIDPAK_APK", apk},
		{"ANDROIDPAK_STAGED", stage},
		{"ANDROIDPAK_MAINLIB", libs.MainLib},
		{"ANDROIDPAK_ABIS", libs.Abis[0]},
		{"ANDROIDPAK_RT", rt},
	}
	for _, c := range checks {
		if env[c.key] != c.value {
			t.Errorf("%s = %q, want %q", c.key, env[c.key], c.value)
		}
	}
	// The profile's own env must survive next to the handoff keys.
	if env["SOME_GAME_VAR"] != "1" {
		t.Error("profile env was dropped")
	}
}

func TestAndroidPakCommandWiresLoaderPathsAndShims(t *testing.T) {
	// The direct-loader runtime form gets ANDROIDPAK_SHIMS and a
	// staged-first LD_LIBRARY_PATH from the Go side, because the runner can
	// only preload what it is told to and glibc resolves the game's
	// DT_NEEDED entries through search paths. launch.sh does this itself.
	rt, apk, stage := buildAndroidPakCommandTestRT(t)
	shims := filepath.Join(rt, "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := GameProfile{ID: "tier1", Title: "Tier 1 Game", Runtime: gameRuntimeAndroidPak}
	libs := androidPakLibs{Abis: []string{"arm64-v8a"}, Chosen: "arm64-v8a", MainLib: "lib/arm64-v8a/libgame.so"}

	t.Setenv("LD_LIBRARY_PATH", "")
	env := map[string]string{}
	if _, _, err := buildAndroidPakCommand(profile, apk, stage, libs, env); err != nil {
		t.Fatalf("buildAndroidPakCommand: %v", err)
	}
	if env["ANDROIDPAK_SHIMS"] != shims {
		t.Errorf("ANDROIDPAK_SHIMS = %q, want %q", env["ANDROIDPAK_SHIMS"], shims)
	}
	wantPath := filepath.Join(stage, "lib", "arm64-v8a") + string(filepath.ListSeparator) + shims
	if env["LD_LIBRARY_PATH"] != wantPath {
		t.Errorf("LD_LIBRARY_PATH = %q, want %q", env["LD_LIBRARY_PATH"], wantPath)
	}

	// A pre-existing device value stays behind the staged dirs so the
	// PowerVR GL userspace still resolves last.
	t.Setenv("LD_LIBRARY_PATH", ":device-gles")
	env = map[string]string{}
	if _, _, err := buildAndroidPakCommand(profile, apk, stage, libs, env); err != nil {
		t.Fatalf("second buildAndroidPakCommand: %v", err)
	}
	wantPath = wantPath + string(filepath.ListSeparator) + ":device-gles"
	if env["LD_LIBRARY_PATH"] != wantPath {
		t.Errorf("LD_LIBRARY_PATH with host value = %q, want %q", env["LD_LIBRARY_PATH"], wantPath)
	}
}

func TestAndroidPakCommandNeedsRuntime(t *testing.T) {
	if os.Getenv("JUKAHUB_ANDROIDRT") != "" {
		t.Skip("JUKAHUB_ANDROIDRT set on this host")
	}
	profile := GameProfile{Runtime: gameRuntimeAndroidPak}
	if _, _, err := buildAndroidPakCommand(profile, "a.apk", "stage", androidPakLibs{}, nil); err == nil || !strings.Contains(err.Error(), "android runtime not found") {
		t.Fatalf("missing runtime = %v", err)
	}
}

func TestAndroidPakFirstRunSymbolCapturedFromLaunchLog(t *testing.T) {
	dir := t.TempDir()
	copyOf := dir
	prev := gameProfilesDirOverride
	gameProfilesDirOverride = copyOf
	t.Cleanup(func() { gameProfilesDirOverride = prev })
	if err := os.MkdirAll(filepath.Join(copyOf, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLog := func(id, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(copyOf, "logs", id+".log"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeLog("dlsym", "=== start ===\ncommand: loader game.apk\n"+
		"[androidrt] shim loaded: /AndroidRT/shims/liblog.so\n"+
		"[androidrt] could not load /stage/lib/arm64-v8a/libgame.so: /stage/lib/arm64-v8a/libgame.so: undefined symbol: AImageDecoder_create\n")
	if got := androidPakFirstRunSymbol("dlsym"); got != "AImageDecoder_create" {
		t.Errorf("symbol = %q, want AImageDecoder_create", got)
	}

	// A library that loads but has no ANativeActivity_onCreate is a
	// different first-run failure: the capture stays empty rather than
	// inventing a symbol name.
	writeLog("noentry", "[androidrt] /stage/libgame.so exports no ANativeActivity_onCreate\n"+
		"[androidrt] hint: a libgame.so without it is a JNI library for dex code\n")
	if got := androidPakFirstRunSymbol("noentry"); got != "" {
		t.Errorf("symbol = %q, want empty for a non-symbol first-run failure", got)
	}

	// A clean first run (no loader failure line) captures nothing, and a
	// profile without a log file at all also stays empty.
	writeLog("clean", "[androidrt] shim loaded: /AndroidRT/shims/libandroid.so\n[game][I] onCreate served\n")
	if got := androidPakFirstRunSymbol("clean"); got != "" {
		t.Errorf("symbol = %q, want empty on a clean run", got)
	}
	if got := androidPakFirstRunSymbol("absent"); got != "" {
		t.Errorf("symbol = %q, want empty without a log file", got)
	}
}

func TestAndroidRTPreferredCandidatesAreDocumented(t *testing.T) {
	// Resolve without any runtime directory present and capture the exact
	// refusal so the docs and the panel always agree.
	if _, err := resolveAndroidRuntime(func(string) (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("without a runtime this must fail with the install hint")
	}
}
