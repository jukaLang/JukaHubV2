package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Platform path selection used to be covered only indirectly, through callers
// that happened to use P(). These tests pin the per-implementation behaviour
// directly, so a change to one platform's paths is caught here rather than on
// the device.

// TestPlatformInitSelectsByGOOS: the selected implementation must match the OS
// the binary is running on. InitPlatform is idempotent and re-selects, so this
// is safe to call directly.
func TestPlatformInitSelectsByGOOS(t *testing.T) {
	InitPlatform()
	got := P().Name()
	want := "windows"
	if runtime.GOOS != "windows" {
		want = "trimui-linux"
	}
	if got != want {
		t.Fatalf("P().Name() = %q on %s, want %q", got, runtime.GOOS, want)
	}
}

// TestPlatformConfigPathsAreDistinctAndRelative: both platforms resolve config
// next to the working directory, and the two files never collide.
func TestPlatformConfigPathsAreDistinctAndRelative(t *testing.T) {
	for _, p := range []Platform{tspPlatform{}, windowsPlatform{}} {
		cfg, user := p.ConfigPath(), p.UserConfigPath()
		if cfg == "" || user == "" {
			t.Fatalf("%s: empty config path %q/%q", p.Name(), cfg, user)
		}
		if cfg == user {
			t.Fatalf("%s: config and user config collide at %q", p.Name(), cfg)
		}
		if filepath.Base(cfg) != "jukaconfig.json" {
			t.Fatalf("%s: config base = %q, want jukaconfig.json", p.Name(), filepath.Base(cfg))
		}
		if filepath.Base(user) != "jukauser.json" {
			t.Fatalf("%s: user config base = %q, want jukauser.json", p.Name(), filepath.Base(user))
		}
	}
}

// TestPlatformHandheldFlags: the handheld/desktop split drives frame cadence,
// memory budgets and window defaults, so it must be stable per platform.
func TestPlatformHandheldFlags(t *testing.T) {
	tsp := tspPlatform{}
	win := windowsPlatform{}
	if !tsp.IsHandheld() {
		t.Fatal("tspPlatform must report handheld")
	}
	if win.IsHandheld() {
		t.Fatal("windowsPlatform must not report handheld")
	}
	// Handhelds default to fullscreen and are not resizable; desktop is the
	// opposite. These pairs are what main.go uses to open the window.
	if !tsp.FullscreenDefault() || tsp.ResizableDefault() {
		t.Fatal("tspPlatform window defaults changed (want fullscreen, fixed)")
	}
	if win.FullscreenDefault() || !win.ResizableDefault() {
		t.Fatal("windowsPlatform window defaults changed (want windowed, resizable)")
	}
}

// TestPlatformRequiredDirMatchesToolsDir: install and lookup must agree on one
// directory, or a tool installed is never found. Both platforms alias
// RequiredDir to ToolsDir; pin that so a future edit cannot split them.
func TestPlatformRequiredDirMatchesToolsDir(t *testing.T) {
	for _, p := range []Platform{tspPlatform{}, windowsPlatform{}} {
		if p.RequiredDir() != p.ToolsDir() {
			t.Fatalf("%s: RequiredDir %q != ToolsDir %q", p.Name(), p.RequiredDir(), p.ToolsDir())
		}
	}
}

// TestPlatformToolsDirIsNamedRequired: the directory the tools resolve into is
// the well-known "required" folder, which the SD-card packaging script and the
// install bar both rely on.
func TestPlatformToolsDirIsNamedRequired(t *testing.T) {
	for _, p := range []Platform{tspPlatform{}, windowsPlatform{}} {
		if filepath.Base(p.ToolsDir()) != "required" {
			t.Fatalf("%s: ToolsDir base = %q, want required", p.Name(), filepath.Base(p.ToolsDir()))
		}
	}
}

// TestPlatformLookPathFindsRequiredTool: LookPath prefers a binary planted in
// the platform's own required/ directory over a bare PATH miss. Both
// implementations resolve required/ next to the running executable, so plant
// the probe there and assert it is found. The test binary's directory is the
// only place both sides agree on.
func TestPlatformLookPathFindsRequiredTool(t *testing.T) {
	exeDir, err := (&tspPlatform{}).ExecutableDir()
	if err != nil {
		t.Fatalf("executable dir: %v", err)
	}
	// A name unlikely to exist on PATH, planted in required/.
	name := "jukahub-platform-probe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(exeDir, "required")
	if err := (&tspPlatform{}).MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir required: %v", err)
	}
	full := filepath.Join(dir, name)
	if err := (&tspPlatform{}).WriteFile(full, []byte("x"), 0o755); err != nil {
		t.Fatalf("plant tool: %v", err)
	}
	for _, p := range []Platform{tspPlatform{}, windowsPlatform{}} {
		got, err := p.LookPath(name)
		if err != nil {
			t.Fatalf("%s: LookPath(%q) error: %v", p.Name(), name, err)
		}
		if filepath.Base(got) != name {
			t.Fatalf("%s: LookPath(%q) = %q, want the planted tool", p.Name(), name, got)
		}
	}
}

// TestWindowsLookPathAcceptsBareName: the Windows implementation adds the .exe
// suffix so a caller can pass "yt-dlp" and still find "yt-dlp.exe". This is the
// platform-specific selection the indirect coverage missed.
func TestWindowsLookPathAcceptsBareName(t *testing.T) {
	exeDir, err := (&windowsPlatform{}).ExecutableDir()
	if err != nil {
		t.Fatalf("executable dir: %v", err)
	}
	bare := "jukahub-bare-probe"
	planted := bare + ".exe"
	dir := filepath.Join(exeDir, "required")
	if err := (&windowsPlatform{}).MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir required: %v", err)
	}
	full := filepath.Join(dir, planted)
	if err := (&windowsPlatform{}).WriteFile(full, []byte("x"), 0o755); err != nil {
		t.Fatalf("plant tool: %v", err)
	}

	// Ask for the bare name; the platform should find the .exe it planted.
	got, err := (&windowsPlatform{}).LookPath(bare)
	if err != nil {
		t.Fatalf("LookPath(%q) error: %v", bare, err)
	}
	if filepath.Base(got) != planted {
		t.Fatalf("LookPath(%q) = %q, want %q (auto .exe suffix)", bare, filepath.Base(got), planted)
	}
}

// TestTrimuiLookPathDoesNotAddExe: the Linux side must NOT invent an .exe
// suffix — the handheld tool names have no extension.
func TestTrimuiLookPathDoesNotAddExe(t *testing.T) {
	exeDir, err := (&tspPlatform{}).ExecutableDir()
	if err != nil {
		t.Fatalf("executable dir: %v", err)
	}
	bare := "jukahub-noexe-probe"
	// Plant only a ".exe" decoy; the bare lookup must not find it.
	dir := filepath.Join(exeDir, "required")
	if err := (&tspPlatform{}).MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir required: %v", err)
	}
	if err := (&tspPlatform{}).WriteFile(filepath.Join(dir, bare+".exe"), []byte("x"), 0o755); err != nil {
		t.Fatalf("plant decoy: %v", err)
	}
	if got, err := (&tspPlatform{}).LookPath(bare); err == nil && strings.HasSuffix(got, ".exe") {
		t.Fatalf("tsp LookPath(%q) returned an .exe (%q); Linux must not add a suffix", bare, got)
	}
}
