package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerDBIsPinnedAndParses(t *testing.T) {
	if got := controllerDBHash(controllerDBText); got != controllerDBSum {
		t.Fatalf("embedded controller database hash = %s, want %s", got, controllerDBSum)
	}
	lines := controllerDBLines(controllerDBText)
	if len(lines) < 1000 {
		t.Fatalf("controller database has %d mapping lines, want the community table", len(lines))
	}
	if !strings.Contains(controllerDBText, "platform:Windows") {
		t.Fatal("controller database has no Windows mappings")
	}
}

func TestControllerMappingLine(t *testing.T) {
	buttons := map[string]uint8{}
	for i, step := range controllerMapSteps {
		buttons[step.Token] = uint8(i)
	}
	line, ok := controllerMappingLine("030000005E0400008E02000014010000", "Test, Pad", "Windows", buttons)
	if !ok {
		t.Fatal("a complete recording was refused")
	}
	want := "030000005e0400008e02000014010000,Test Pad,a:b0,b:b1,x:b2,y:b3,back:b4,start:b5," +
		"dpup:b6,dpdown:b7,dpleft:b8,dpright:b9,leftshoulder:b10,rightshoulder:b11,platform:Windows,"
	if line != want {
		t.Fatalf("mapping = %q, want %q", line, want)
	}
	delete(buttons, "start")
	if _, ok := controllerMappingLine("030000005e0400008e02000014010000", "Pad", "Windows", buttons); ok {
		t.Fatal("an incomplete recording was accepted")
	}
	buttons["start"] = 4 // same button as back
	if _, ok := controllerMappingLine("030000005e0400008e02000014010000", "Pad", "Windows", buttons); ok {
		t.Fatal("a duplicate button was accepted")
	}
	if _, ok := controllerMappingLine("not-a-guid", "Pad", "Windows", buttons); ok {
		t.Fatal("a bad GUID was accepted")
	}
}

func TestControllerUserDBRoundTrip(t *testing.T) {
	prevDir := bkupDataDirOverride
	bkupDataDirOverride = t.TempDir()
	defer func() { bkupDataDirOverride = prevDir }()

	line := "030000005e0400008e02000014010000,Test Pad,a:b0,b:b1,x:b2,y:b3," +
		"back:b4,start:b5,dpup:b6,dpdown:b7,dpleft:b8,dpright:b9,leftshoulder:b10," +
		"rightshoulder:b11,platform:Windows,"
	if err := controllerDBAppendUser(line); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(bkupDataDirOverride, "gamecontrollerdb-user.txt")); err != nil || !strings.Contains(string(got), line) {
		t.Fatalf("saved user database = %q, %v", got, err)
	}
	prevAdd := controllerAddMapping
	var seen []string
	controllerAddMapping = func(mapping string) int {
		seen = append(seen, mapping)
		return 0
	}
	defer func() { controllerAddMapping = prevAdd }()
	if n := controllerDBLoadUser(); n != 1 || len(seen) != 1 || seen[0] != line {
		t.Fatalf("loaded %d user mappings (%q), want the recorded one", n, seen)
	}
}

func TestControllerHIDAPIHintsCoverTheNamedPads(t *testing.T) {
	have := map[string]bool{}
	for _, name := range controllerHIDAPIHintNames {
		have[name] = true
	}
	for _, want := range []string{
		"SDL_JOYSTICK_HIDAPI_PS4", "SDL_JOYSTICK_HIDAPI_PS5",
		"SDL_JOYSTICK_HIDAPI_SWITCH", "SDL_JOYSTICK_HIDAPI_XBOX",
	} {
		if !have[want] {
			t.Errorf("%s is not set before SDL init", want)
		}
	}
}

func TestCtrldiagMapFlowWritesAMapping(t *testing.T) {
	prevDir := bkupDataDirOverride
	bkupDataDirOverride = t.TempDir()
	defer func() { bkupDataDirOverride = prevDir }()
	prevAdd := controllerAddMapping
	var installed string
	controllerAddMapping = func(mapping string) int {
		installed = mapping
		return 0
	}
	defer func() { controllerAddMapping = prevAdd }()
	defer ctrldiagMapStop()

	ctrldiagMap = ctrldiagMapState{
		active:  true,
		guid:    "030000005e0400008e02000014010000",
		name:    "Test Pad",
		buttons: map[string]uint8{},
	}
	for i := 1; i <= len(controllerMapSteps); i++ {
		ctrldiagMapButton(i)
	}
	if ctrldiagMap.active {
		t.Fatal("the recording did not finish after every step")
	}
	if !strings.Contains(installed, "a:b1") || !strings.Contains(installed, "rightshoulder:b12") {
		t.Fatalf("installed mapping = %q", installed)
	}
	data, err := os.ReadFile(controllerDBUserPath())
	if err != nil || !strings.Contains(string(data), installed) {
		t.Fatalf("saved mapping = %q, %v", data, err)
	}
}
