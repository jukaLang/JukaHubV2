package main

// SND-11 — the Windows release carries the game-audio plugin.
//
// The plugin itself and the Windows loader path already existed
// (spatialfx/build.sh TARGET=windows, defaultSpatialPluginLocate); the release
// archive did not carry the DLL, so Games spatial audio said it was not in the
// Windows build. The release job now runs spatialfx/stage-windows.sh, which
// builds the DLL through build.sh's own checks and puts it at lib/ beside the
// exe. The static half keeps the workflow wired that way; the behavioural half
// runs the same script into a temporary release folder and checks the PE file.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWindowsReleaseStagesTheGameAudioPlugin(t *testing.T) {
	script := filepath.Join("spatialfx", "stage-windows.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the Windows staging script is missing: %v", err)
	}
	wf, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "docker-image.yml"))
	if err != nil {
		t.Fatalf("read the Windows release workflow: %v", err)
	}
	workflow := string(wf)
	step := strings.Index(workflow, "CROSS=x86_64-w64-mingw32- sh player/spatialfx/stage-windows.sh JukaHub-Trimui-Windows")
	if step < 0 {
		t.Fatal("the Windows release job does not stage the game-audio plugin")
	}
	zipAt := strings.Index(workflow[step:], "zip -r JukaHub-Windows.zip")
	if zipAt < 0 {
		t.Fatal("the Windows release job no longer creates JukaHub-Windows.zip after the plugin step")
	}
	stepBody := workflow[step : step+zipAt]
	if !strings.Contains(stepBody, "test -f JukaHub-Trimui-Windows/lib/jukahub-spatial.dll") {
		t.Fatal("the Windows release job does not check the plugin is in the archive folder before zipping it")
	}

	cc, cross := "gcc", ""
	if runtime.GOOS != "windows" {
		cc, cross = "x86_64-w64-mingw32-gcc", "x86_64-w64-mingw32-"
	}
	if _, err := exec.LookPath(cc); err != nil {
		t.Skipf("no Windows compiler (%s) on this host", cc)
	}
	dest := t.TempDir()
	cmd := exec.Command("sh", script, dest)
	cmd.Env = append(os.Environ(), "CROSS="+cross, "CC="+cc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage-windows.sh failed: %v\n%s", err, out)
	}
	dll := filepath.Join(dest, "lib", "jukahub-spatial.dll")
	data, err := os.ReadFile(dll)
	if err != nil {
		t.Fatalf("the release folder has no lib/jukahub-spatial.dll: %v", err)
	}
	if len(data) < 2 || string(data[:2]) != "MZ" {
		t.Fatalf("lib/jukahub-spatial.dll is not a PE image")
	}
	if _, err := os.Stat(filepath.Join(dest, ".spatial-build")); !os.IsNotExist(err) {
		t.Fatalf("the staging build folder was left in the release folder")
	}
	objdump := "objdump"
	if cross != "" {
		objdump = cross + "objdump"
	}
	dump, err := exec.Command(objdump, "-p", dll).CombinedOutput()
	if err != nil {
		t.Fatalf("%s could not read the staged DLL: %v", objdump, err)
	}
	for _, sym := range []string{"dspfilter_get_implementation", "ladspa_descriptor"} {
		if !bytes.Contains(dump, []byte(sym)) {
			t.Errorf("the staged DLL does not export %s", sym)
		}
	}
}
