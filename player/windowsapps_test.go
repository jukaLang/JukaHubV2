package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func testWineHost(env map[string]string, weston string) wineHost {
	return wineHost{
		getenv: func(key string) string { return env[key] },
		weston: weston,
	}
}

func TestResolveWineDisplaySelectsBackend(t *testing.T) {
	weston := filepath.Join(t.TempDir(), "westonwrap.sh")
	if err := os.WriteFile(weston, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		requested string
		env       map[string]string
		weston    string
		want      string
		wantErr   string
	}{
		{
			name: "auto prefers x11 when both are present",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			want: windowsDisplayX11,
		},
		{
			name: "auto uses x11",
			env:  map[string]string{"DISPLAY": ":0"},
			want: windowsDisplayX11,
		},
		{
			name: "auto uses wayland without x11",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			want: windowsDisplayWayland,
		},
		{
			name:   "auto falls back to westonpack",
			weston: weston,
			want:   windowsDisplayWeston,
		},
		{
			name:    "auto refuses a bare framebuffer",
			wantErr: "no display server",
		},
		{
			name:      "explicit weston needs a wrapper",
			requested: windowsDisplayWeston,
			wantErr:   "westonwrap.sh",
		},
		{
			name:      "explicit x11 needs DISPLAY",
			requested: windowsDisplayX11,
			wantErr:   "DISPLAY",
		},
		{
			name:      "explicit wayland needs WAYLAND_DISPLAY",
			requested: windowsDisplayWayland,
			wantErr:   "WAYLAND_DISPLAY",
		},
		{
			name:      "headless is explicit",
			requested: windowsDisplayHeadless,
			want:      windowsDisplayHeadless,
		},
		{
			name:      "unknown mode is rejected",
			requested: "framebuffer",
			wantErr:   "unknown display",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWineDisplay(tc.requested, testWineHost(tc.env, tc.weston))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("display = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveWineDisplayHonoursEnvironmentDefault(t *testing.T) {
	host := testWineHost(map[string]string{"DISPLAY": ":0", "JUKAHUB_WINE_DISPLAY": "x11"}, "")
	got, err := resolveWineDisplay("", host)
	if err != nil {
		t.Fatal(err)
	}
	if got != windowsDisplayX11 {
		t.Errorf("display = %q, want x11 override", got)
	}
}

func TestWrapWineCommandUsesWestonPack(t *testing.T) {
	host := testWineHost(nil, "/tmp/weston/westonwrap.sh")
	env := map[string]string{
		"WINEPREFIX":       "/data/prefix",
		"WINEDLLOVERRIDES": "d3d9=n,b",
	}
	command, args, err := wrapWineCommand(windowsDisplayWeston, "wine", []string{"/apps/notepad.exe", "-x"}, env, host)
	if err != nil {
		t.Fatal(err)
	}
	if command != host.weston {
		t.Fatalf("command = %q, want the WestonPack wrapper", command)
	}
	want := []string{
		"drm", "gl", "kiosk", "system",
		"WINEDLLOVERRIDES=d3d9=n,b", "WINEPREFIX=/data/prefix",
		"wine", "/apps/notepad.exe", "-x",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", args, want)
	}
}

func TestWrapWineCommandHeadlessClearsDisplay(t *testing.T) {
	host := testWineHost(map[string]string{"DISPLAY": ":0"}, "")
	env := map[string]string{"WINEPREFIX": "/data/prefix"}
	command, args, err := wrapWineCommand(windowsDisplayHeadless, "wine", []string{"/apps/tool.exe"}, env, host)
	if err != nil {
		t.Fatal(err)
	}
	if command != "wine" || len(args) != 1 || args[0] != "/apps/tool.exe" {
		t.Fatalf("headless command = %q %v", command, args)
	}
	if env["DISPLAY"] != "" || env["WAYLAND_DISPLAY"] != "" {
		t.Errorf("headless env still has a display: %#v", env)
	}
}

func TestResolveDXVKRequiresRealDLLs(t *testing.T) {
	dir := t.TempDir()
	if dxvkBuild(dir) {
		t.Fatal("an empty directory was accepted as a DXVK build")
	}
	if err := os.MkdirAll(filepath.Join(dir, "x64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x64", "d3d11.dll"), []byte("dxvk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !dxvkBuild(dir) {
		t.Fatal("x64/d3d11.dll was not detected")
	}
	t.Setenv("JUKAHUB_DXVK", dir)
	got, err := resolveDXVK()
	if err != nil {
		t.Fatalf("resolveDXVK: %v", err)
	}
	if got != dir {
		t.Fatalf("resolveDXVK = %q, want %q", got, dir)
	}

	t.Setenv("JUKAHUB_DXVK", filepath.Join(dir, "missing"))
	if _, err := resolveDXVK(); err == nil {
		t.Fatal("a missing JUKAHUB_DXVK override was silently ignored")
	}
}

func TestApplyWineCompatibilityEnvOnlyUsesDXVKWithVulkan(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x64", "d3d11.dll"), []byte("dxvk"), 0o644); err != nil {
		t.Fatal(err)
	}
	vulkan := filepath.Join(t.TempDir(), "libvulkan.so.1")
	if err := os.WriteFile(vulkan, []byte("vulkan"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUKAHUB_DXVK", dir)
	t.Setenv("JUKAHUB_VULKAN", vulkan)

	env := map[string]string{"WINEPREFIX": "/tmp/prefix"}
	detected, hasVulkan := applyWineCompatibilityEnv(env)
	if detected != dir || !hasVulkan {
		t.Fatalf("applyWineCompatibilityEnv = %q, %v", detected, hasVulkan)
	}
	if env["WINEDLLOVERRIDES"] != "dxgi,d3d9,d3d10core,d3d11,d3d12=n,b" {
		t.Fatalf("WINEDLLOVERRIDES = %q", env["WINEDLLOVERRIDES"])
	}

	// Existing overrides are the user's working setup and must survive.
	custom := map[string]string{"WINEDLLOVERRIDES": "d3d11=b"}
	applyWineCompatibilityEnv(custom)
	if custom["WINEDLLOVERRIDES"] != "d3d11=b" {
		t.Fatalf("custom DLL overrides were replaced: %q", custom["WINEDLLOVERRIDES"])
	}

	// DXVK without a Vulkan loader would fail at DLL load time, so keep Wine's
	// built-in D3D path instead of forcing an override.
	t.Setenv("JUKAHUB_VULKAN", filepath.Join(t.TempDir(), "missing"))
	noVulkan := map[string]string{}
	if _, hasVulkan := applyWineCompatibilityEnv(noVulkan); hasVulkan {
		t.Fatal("missing Vulkan loader was reported as ready")
	}
	if _, ok := noVulkan["WINEDLLOVERRIDES"]; ok {
		t.Fatal("DXVK override was applied without a Vulkan loader")
	}
}

func TestEnsureWinePrefixCreatesRuntimePrefix(t *testing.T) {
	windowsRuntimeDirOverride = t.TempDir()
	t.Cleanup(func() { windowsRuntimeDirOverride = "" })
	env := map[string]string{}
	if err := ensureWinePrefix(env); err != nil {
		t.Fatal(err)
	}
	if env["WINEPREFIX"] == "" {
		t.Fatal("WINEPREFIX was not set")
	}
	info, err := os.Stat(env["WINEPREFIX"])
	if err != nil || !info.IsDir() {
		t.Fatalf("prefix was not created: %v", err)
	}
}

func TestScanWindowsAppsFindsExecutables(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Apps", "Notepad", "notepad.exe"), "")
	writeTestFile(t, filepath.Join(root, "Games", "Doom", "DOOM.EXE"), "")
	writeTestFile(t, filepath.Join(root, "Games", "Doom", "readme.txt"), "")
	writeTestFile(t, filepath.Join(root, "windows", "system32", "cmd.exe"), "")
	writeTestFile(t, filepath.Join(root, "Apps", "Notepad", "unins000.exe"), "")
	deep := filepath.Join(root, "a", "b", "c", "d", "deep.exe")
	writeTestFile(t, deep, "")

	entries, issues := scanWindowsApps([]string{root})
	if len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	if len(entries) != 2 {
		t.Fatalf("found %d entries, want 2: %#v", len(entries), entries)
	}
	if entries[0].Title != "DOOM" || entries[0].Rel != "Games/Doom/DOOM.EXE" {
		t.Errorf("first entry = %#v", entries[0])
	}
	if entries[1].Title != "notepad" {
		t.Errorf("second entry = %#v", entries[1])
	}
	for _, entry := range entries {
		if !filepath.IsAbs(entry.Path) {
			t.Errorf("entry path is not absolute: %q", entry.Path)
		}
	}
}

func TestWindowsAppTitleCleansFileName(t *testing.T) {
	if got := windowsAppTitle("my_cool-app.EXE"); got != "my cool app" {
		t.Errorf("title = %q", got)
	}
	if got := windowsAppTitle(".exe"); got != ".exe" {
		t.Errorf("empty title = %q", got)
	}
}

func TestWindowsAppDisplayValidation(t *testing.T) {
	profile := GameProfile{
		SchemaVersion: 1,
		ID:            "notepad",
		Runtime:       gameRuntimeWine,
		Dir:           "Games/Notepad",
		Executable:    "notepad.exe",
		Display:       "framebuffer",
	}
	if err := validateGameProfile(&profile); err == nil || !strings.Contains(err.Error(), "display") {
		t.Fatalf("error = %v, want an invalid display error", err)
	}
	profile.Display = windowsDisplayWeston
	if err := validateGameProfile(&profile); err != nil {
		t.Fatalf("valid weston display rejected: %v", err)
	}
}

func TestWindowsAppsPanelRendersRows(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Apps", "Notepad", "notepad.exe"), "")
	writeTestFile(t, filepath.Join(root, "Apps", "Calc", "calc.exe"), "")
	windowsAppsRootsOverride = []string{root}
	winAppsPanel = windowsAppsPanelState{open: true}
	t.Cleanup(func() {
		windowsAppsRootsOverride = nil
		winAppsPanel = windowsAppsPanelState{}
		windowsAppRowHits = nil
		windowsAppPanelRect = sdl.Rect{}
		invalidateWindowsAppsCache()
	})
	invalidateWindowsAppsCache()

	snapshotHomeGlobals(t)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderWindowsAppsPanel(renderer, &Config{})

	if windowsAppPanelRect.W <= 0 || windowsAppPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", windowsAppPanelRect)
	}
	if len(windowsAppRowHits) != 2 {
		t.Fatalf("rendered %d rows, want 2", len(windowsAppRowHits))
	}
	for _, hit := range windowsAppRowHits {
		r := hit.rect
		if r.X < windowsAppPanelRect.X || r.Y < windowsAppPanelRect.Y ||
			r.X+r.W > windowsAppPanelRect.X+windowsAppPanelRect.W ||
			r.Y+r.H > windowsAppPanelRect.Y+windowsAppPanelRect.H {
			t.Errorf("row %+v escapes panel %+v", r, windowsAppPanelRect)
		}
	}
}

func TestWindowsAppsPanelRendersSetupPage(t *testing.T) {
	windowsAppsRootsOverride = []string{t.TempDir()}
	winAppsPanel = windowsAppsPanelState{open: true, help: true}
	t.Cleanup(func() {
		windowsAppsRootsOverride = nil
		winAppsPanel = windowsAppsPanelState{}
		windowsAppRowHits = nil
		windowsAppPanelRect = sdl.Rect{}
		invalidateWindowsAppsCache()
	})
	invalidateWindowsAppsCache()

	snapshotHomeGlobals(t)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderWindowsAppsPanel(renderer, &Config{})
	if windowsAppPanelRect.W <= 0 || len(windowsAppRowHits) != 0 {
		t.Fatalf("setup panel = %+v, rows = %v", windowsAppPanelRect, windowsAppRowHits)
	}
}

func TestWindowsAppsPanelReturnsToEmulators(t *testing.T) {
	emuPanel = emuPanelState{open: true, cursor: 3, scroll: 1}
	t.Cleanup(func() { emuPanel = emuPanelState{} })

	openWindowsAppsPanel(true)
	if !winAppsPanel.open || emuPanel.open {
		t.Fatal("opening Windows Apps did not replace the emulator list")
	}
	closeWindowsAppsPanel()
	if winAppsPanel.open || !emuPanel.open || emuPanel.cursor != 3 || emuPanel.scroll != 1 {
		t.Fatalf("closing did not restore the emulator list: panel=%+v emu=%+v", winAppsPanel, emuPanel)
	}
}

func TestWindowsAppsEmulatorEntryIsPinned(t *testing.T) {
	windowsAppsRootsOverride = []string{t.TempDir()}
	t.Cleanup(func() {
		windowsAppsRootsOverride = nil
		invalidateWindowsAppsCache()
	})
	invalidateWindowsAppsCache()
	entry := windowsAppsEmuEntry()
	if entry.Name != windowsAppsEntryName || !entry.Pinned {
		t.Fatalf("entry = %+v, want a pinned Windows Apps row", entry)
	}
}
