package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPPSSPPTrimuiHomeRepairsKeyboardAndGraphics(t *testing.T) {
	sys := t.TempDir()
	graphics := filepath.Join(sys, "ppsspp.ini")
	controls := filepath.Join(sys, "controls.ini")
	if err := os.WriteFile(graphics, []byte("[General]\nLanguage = en_US\n[Graphics]\nGraphicsBackend = 3 (VULKAN)\nDisplayAspectRatio = 0.848000\nFailedGraphicsBackends = OPENGL\nDisabledGraphicsBackends = DIRECT3D11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PPSSPP's crash memory (2026-11-25, JH-CA8ZMU): it sent every start to
	// Vulkan, which the firmware's SDL cannot give it.
	crashMemory := filepath.Join(sys, ppssppFailedBackendsFile)
	if err := os.WriteFile(crashMemory, []byte("OPENGL"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pause = 10-109 is what an older JukaHub wrote: a code PPSSPP's SDL front
	// end never sends, so Menu (10-4) is added beside it.
	if err := os.WriteFile(controls, []byte("[ControlMapping]\nCross = 1-44\nCircle = 1-27\nPause = 10-109\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppPrepareTrimuiHome(sys, "tg5040")
	gotGraphics, _ := os.ReadFile(graphics)
	if !strings.Contains(string(gotGraphics), "GraphicsBackend = 0 (OPENGL)") || !strings.Contains(string(gotGraphics), "DisplayAspectRatio = 0.848000") || !strings.Contains(string(gotGraphics), "Language = en_US") {
		t.Fatalf("graphics were not repaired without losing settings: %s", gotGraphics)
	}
	if !strings.Contains(string(gotGraphics), "DisabledGraphicsBackends = DIRECT3D11,VULKAN") ||
		strings.Contains(string(gotGraphics), "FailedGraphicsBackends = OPENGL") || regularFile(crashMemory) {
		t.Fatalf("Vulkan not disabled or the crash memory kept: %s", gotGraphics)
	}
	gotControls, _ := os.ReadFile(controls)
	if !strings.Contains(string(gotControls), "Cross = 1-44,10-189") || !strings.Contains(string(gotControls), "An.Up = 10-4003") ||
		!strings.Contains(string(gotControls), "Pause = 10-109,10-4") {
		t.Fatalf("keyboard and pad were not both mapped: %s", gotControls)
	}
	ppssppPrepareTrimuiHome(sys, "tg5040")
	again, _ := os.ReadFile(controls)
	if string(again) != string(gotControls) {
		t.Fatal("the mapping was appended a second time")
	}
	if !regularFile(controls+".bak") || !regularFile(graphics+".bak") {
		t.Fatal("the previous settings were not backed up")
	}
}

func TestPPSSPPFirstManagedLaunchRepairsExistingHome(t *testing.T) {
	root, _ := ppssppTestEnv(t)
	b := ppssppTestBuild(t, root, "6.2.0", "1.20.3")
	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	st.Builds, st.Active = []ppssppBuild{b}, b.Tag
	ppssppMgr.mu.Unlock()
	cmd := hiddenCommand("sh", b.script())
	ppssppPrepareManagedLaunch(b.script(), cmd)
	// The video driver is set in the script, on PPSSPP's command alone
	// (TestPPSSPPLauncherScript): the launch keeps the environment JukaHub
	// gives every game, so a fallback sees the firmware's.
	if cmd.Env != nil {
		t.Fatal("the PSP launch got its own environment")
	}
	if !regularFile(filepath.Join(b.memstick(), "SYSTEM", "controls.ini")) ||
		!regularFile(filepath.Join(b.memstick(), "SYSTEM", "ppsspp.ini")) {
		t.Fatal("the first game did not prepare the managed PPSSPP before launch")
	}
	other := hiddenCommand("sh", "other.sh")
	ppssppPrepareManagedLaunch("/outside/jukahub-ppsspp.sh", other)
	if other.Env != nil {
		t.Fatal("another emulator inherited PPSSPP's video driver")
	}
}

// A PSP picture that reaches the A133P's panel (2026-11-23): the opaque shader
// ends PPSSPP's own chain — global ini and a game's own, after the player's
// shaders, once, line endings kept — the speed level skips no GPU read-backs
// and no frames under Skip buffer effects, and the framebuffer's alpha byte is
// read from the driver's description.
func TestPPSSPPPictureSettings(t *testing.T) {
	stick := t.TempDir()
	sys := filepath.Join(stick, "SYSTEM")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(sys, "ppsspp.ini")
	game := filepath.Join(sys, "ULUS10041_ppsspp.ini")
	if err := os.WriteFile(global, []byte("[Graphics]\r\nGraphicsBackend = 0 (OPENGL)\r\n[PostShaderList]\r\nPostShader2 = FXAA\r\nPostShader1 = JukaHubOpaque\r\nPostShader3 = Off\r\n[Upgrade]\r\nDismissed = 1\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(game, []byte("[Graphics]\nFrameSkip = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppEnsureOpaquePicture(stick, "tg5040")
	got, _ := os.ReadFile(global)
	if !strings.Contains(string(got), "[PostShaderList]\r\nPostShader1 = FXAA\r\nPostShader2 = JukaHubOpaque\r\n[Upgrade]\r\n") {
		t.Fatalf("the global chain does not end with the opaque shader: %q", got)
	}
	if own, _ := os.ReadFile(game); !strings.HasSuffix(string(own), "FrameSkip = 0\n[PostShaderList]\nPostShader1 = JukaHubOpaque\n") {
		t.Fatalf("the game's own ini has no chain of its own: %q", own)
	}
	for _, name := range []string{"jukahub-opaque.ini", "jukahub-opaque.fsh", "jukahub-opaque.vsh"} {
		if !regularFile(filepath.Join(stick, "shaders", name)) {
			t.Fatalf("shaders/%s was not written", name)
		}
	}
	ppssppEnsureOpaquePicture(stick, "tg5040")
	if again, _ := os.ReadFile(global); string(again) != string(got) {
		t.Fatal("a second pass changed the chain again")
	}
	other := t.TempDir()
	ppssppEnsureOpaquePicture(other, "tg5050")
	if dirExists(filepath.Join(other, "shaders")) {
		t.Fatal("the Smart Pro S's build got the A133P's shader")
	}

	ini := "/x/ppsspp.ini"
	lines := []string{"[Graphics]", "FrameSkip = 0", "AutoFrameSkip = False", "SkipGPUReadbackMode = 0", "AnisotropyLevel = 4"}
	for _, e := range emuSpeedPPSSPPPlan(ini, lines, emuSpeedFastest) {
		if strings.HasPrefix(e.Key, "SkipGPUReadback") {
			t.Fatalf("Fastest skips GPU read-backs: %+v", e)
		}
	}
	for _, e := range emuSpeedPPSSPPPlan(ini, append(lines, "SkipBufferEffects = True"), emuSpeedFast) {
		if e.Key == "FrameSkip" || e.Key == "AutoFrameSkip" {
			t.Fatalf("frames are skipped under Skip buffer effects: %+v", e)
		}
	}
	if !emuSpeedPPSSPPKnown(emuSpeedINIEdit{Path: ini, Section: "Graphics", Key: "SkipGPUReadbackMode", Wrote: "1"}, ini) {
		t.Fatal("an older journal's read-back skip would not be put back")
	}

	v := make([]byte, 160)
	for off, n := range map[int]uint32{24: 32, 68: 24, 72: 8} {
		binary.LittleEndian.PutUint32(v[off:], n)
	}
	if got := ppssppPictureAlphaFromVar(v); got != 3 {
		t.Fatalf("ARGB8888's alpha byte = %d, want 3", got)
	}
	binary.LittleEndian.PutUint32(v[72:], 0)
	if got := ppssppPictureAlphaFromVar(v); got != -1 {
		t.Fatalf("XRGB8888's alpha byte = %d, want none", got)
	}
}

func TestPPSSPPTrimuiHomeKeepsPackagedControllerMap(t *testing.T) {
	sys := t.TempDir()
	controls := filepath.Join(sys, "controls.ini")
	packaged := "\ufeff[ControlMapping]\nUp = 10-19\nCross = 10-189\n"
	if err := os.WriteFile(controls, []byte(packaged), 0o644); err != nil {
		t.Fatal(err)
	}
	ppssppPrepareTrimuiHome(sys, "tg5040")
	got, _ := os.ReadFile(controls)
	if !strings.Contains(string(got), "Up = 10-19") || !strings.Contains(string(got), "Cross = 10-189") || !strings.Contains(string(got), "Down = 10-20") || strings.Count(string(got), "[ControlMapping]") != 1 {
		t.Fatalf("the packaged mapping was not preserved and completed: %q", got)
	}
	graphics, _ := os.ReadFile(filepath.Join(sys, "ppsspp.ini"))
	if !strings.Contains(string(graphics), "GraphicsBackend = 0 (OPENGL)") {
		t.Fatal("a build without an ini got no safe graphics backend")
	}
}

