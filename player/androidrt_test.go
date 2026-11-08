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
	profilesDir := t.TempDir()
	prevDir := gameProfilesDirOverride
	gameProfilesDirOverride = profilesDir
	t.Cleanup(func() { gameProfilesDirOverride = prevDir })
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
		// Saves live with the profile catalog, never in the tmpfs stage
		// that is deleted after every exit.
		{"ANDROIDPAK_DATA", filepath.Join(profilesDir, "androidpak-data", "tier1")},
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

	// A profile that points its data elsewhere keeps that choice.
	custom := map[string]string{"ANDROIDPAK_DATA": "/mnt/SDCARD/Saves/tier1"}
	if _, _, err := buildAndroidPakCommand(profile, apk, stage, libs, custom); err != nil {
		t.Fatalf("buildAndroidPakCommand with custom data dir: %v", err)
	}
	if custom["ANDROIDPAK_DATA"] != "/mnt/SDCARD/Saves/tier1" {
		t.Errorf("ANDROIDPAK_DATA = %q, want the profile's own value", custom["ANDROIDPAK_DATA"])
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

	// The log is appended to on every launch: the run that just ended is the
	// one that counts, not the oldest failure in the file.
	writeLog("rerun", "\n=== rerun start 2026-11-01T10:00:00Z ===\n"+
		"[androidrt] could not load /stage/libgame.so: undefined symbol: ATrace_beginSection\nexit: exit status 1\n"+
		"\n=== rerun start 2026-11-01T10:05:00Z ===\n"+
		"[androidrt] could not load /stage/libgame.so: undefined symbol: AChoreographer_getInstance\nexit: exit status 1\n")
	if got := androidPakFirstRunSymbol("rerun"); got != "AChoreographer_getInstance" {
		t.Errorf("symbol = %q, want the latest run's AChoreographer_getInstance", got)
	}

	// The runner's own loader: the trap names the function the game really
	// called, out of everything the report listed.
	writeLog("trap", "=== trap start 2026-11-08T10:00:00Z ===\n"+
		"[androidrt] undefined symbols: 2 (2 functions abort when called, 0 data zero-filled)\n"+
		"[androidrt] undefined symbol: ASensorManager_getInstanceForPackage (func, needed by libgame.so, from libandroid.so@LIBANDROID)\n"+
		"[androidrt] undefined symbol: AChoreographer_postVsyncCallback (func, needed by libgame.so, from libandroid.so@LIBANDROID)\n"+
		"[androidrt] trap: libgame.so called unimplemented AChoreographer_postVsyncCallback — aborting (ANDROIDPAK_MISSING=zero returns 0 instead)\n"+
		"exit: signal: aborted\n")
	if got := androidPakFirstRunSymbol("trap"); got != "AChoreographer_postVsyncCallback" {
		t.Errorf("symbol = %q, want the trapped AChoreographer_postVsyncCallback", got)
	}

	// A strict run that refused to start names the first missing symbol.
	writeLog("strict", "[androidrt] undefined symbol: AImageDecoder_create (func, needed by libgame.so)\n"+
		"[androidrt] refusing to start: 1 undefined symbols and ANDROIDPAK_MISSING=strict\n"+
		"[androidrt] could not load /stage/lib/arm64-v8a/libgame.so: 1 undefined symbols and ANDROIDPAK_MISSING=strict\n")
	if got := androidPakFirstRunSymbol("strict"); got != "AImageDecoder_create" {
		t.Errorf("symbol = %q, want AImageDecoder_create from the strict refusal", got)
	}

	// Symbols listed but never called do not explain a crash elsewhere.
	writeLog("listed", "[androidrt] undefined symbol: AImageDecoder_create (func, needed by libgame.so)\n"+
		"exit: signal: segmentation fault\n")
	if got := androidPakFirstRunSymbol("listed"); got != "" {
		t.Errorf("symbol = %q, want empty: nothing proves the listed symbol stopped the run", got)
	}
}

func TestParseAndroidPakRunReport(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want androidPakRunReport
	}{
		{
			name: "clean run",
			log:  "[androidrt] undefined symbols: 0 — every import resolved\n[androidrt] lifecycle: done, exit status 0\n",
		},
		{
			name: "listed and trapped",
			log: "[androidrt] undefined symbol: A_one (func, needed by libgame.so)\n" +
				"  [androidrt] undefined symbol: B_data (data, needed by libfmod.so, from libc.so@LIBC)\n" +
				"[androidrt] trap: libgame.so called unimplemented A_one — aborting\n" +
				"[androidrt] trap: libgame.so called unimplemented B_two — aborting\n",
			want: androidPakRunReport{Trap: "A_one", Undefined: []string{"A_one", "B_data"}},
		},
		{
			name: "legacy glibc dlerror with a version",
			log:  "[androidrt] could not load /s/libgame.so: /s/libgame.so: undefined symbol: memcpy, version LIBC\n",
			want: androidPakRunReport{Symbol: "memcpy"},
		},
		{
			name: "32-bit library",
			log:  "[androidrt] could not load /s/lib/armeabi-v7a/libgame.so: libgame.so: 32-bit ELF (ELFCLASS32): this runner loads 64-bit (arm64-v8a) libraries only\n",
			want: androidPakRunReport{LoadFailure: "libgame.so: 32-bit ELF (ELFCLASS32): this runner loads 64-bit (arm64-v8a) libraries only"},
		},
		{
			name: "strict refusal",
			log: "[androidrt] undefined symbol: X (func, needed by libgame.so)\n" +
				"[androidrt] refusing to start: 1 undefined symbols and ANDROIDPAK_MISSING=strict\n" +
				"[androidrt] could not load /s/libgame.so: 1 undefined symbols and ANDROIDPAK_MISSING=strict\n",
			want: androidPakRunReport{Undefined: []string{"X"}, Refused: true, LoadFailure: "1 undefined symbols and ANDROIDPAK_MISSING=strict"},
		},
		{
			name: "game log lines are not runner lines",
			log:  "[game][Tag][I] [androidrt] undefined symbol: fake\n",
		},
	}
	for _, c := range cases {
		got := parseAndroidPakRunReport(c.log)
		if got.Trap != c.want.Trap || got.Refused != c.want.Refused || got.LoadFailure != c.want.LoadFailure ||
			got.Symbol != c.want.Symbol || strings.Join(got.Undefined, ",") != strings.Join(c.want.Undefined, ",") {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAndroidPakRunMessage(t *testing.T) {
	cases := []struct {
		name      string
		report    androidPakRunReport
		wantToast bool
		wantIn    []string
	}{
		{"trap", androidPakRunReport{Trap: "A_one", Undefined: []string{"A_one", "B"}}, true,
			[]string{"the game called A_one", "2 undefined symbols listed", "game.log"}},
		{"legacy", androidPakRunReport{Symbol: "memcpy"}, true, []string{"undefined symbol: memcpy"}},
		{"strict", androidPakRunReport{Refused: true, Undefined: []string{"X", "Y"}}, true,
			[]string{"refused to start: 2 undefined symbols (first: X)"}},
		{"load failure", androidPakRunReport{LoadFailure: strings.Repeat("r", 400)}, true,
			[]string{"could not load the game: rrrr", "…"}},
		{"crash with listed symbols", androidPakRunReport{Undefined: []string{"X"}}, false,
			[]string{"tier1 first run failed (1 undefined symbols listed)"}},
		{"nothing known", androidPakRunReport{}, false, []string{"tier1 first run failed; loader output is in"}},
	}
	for _, c := range cases {
		msg, toast := androidPakRunMessage("tier1", c.report, "/data/game-profiles/logs/game.log")
		if toast != c.wantToast {
			t.Errorf("%s: toast = %v, want %v", c.name, toast, c.wantToast)
		}
		for _, want := range c.wantIn {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message %q lacks %q", c.name, msg, want)
			}
		}
		if len([]rune(msg)) > 320 {
			t.Errorf("%s: message is %d runes; a toast must stay readable", c.name, len([]rune(msg)))
		}
	}
}

func TestAndroidRTPreferredCandidatesAreDocumented(t *testing.T) {
	// Resolve without any runtime directory present and capture the exact
	// refusal so the docs and the panel always agree.
	if _, err := resolveAndroidRuntime(func(string) (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("without a runtime this must fail with the install hint")
	}
}
