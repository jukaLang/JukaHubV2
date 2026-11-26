package main

import (
	"strings"
	"testing"
)

func TestWineKeyboardWrapPreservesTargetArgsAndEnv(t *testing.T) {
	launch := wineLaunch{
		Exe:  "/mnt/SDCARD/My Apps/notepad++.exe",
		Args: []string{"", "one two", `C:\dir with space\`, `said "hi"`},
		Env: map[string]string{
			"WINEPREFIX":        "/tmp/prefix",
			"jukahub_vk_target": "stale value",
		},
	}
	got, err := wineKeyboardWrap(launch, "/tmp/helpers/keyboard.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got.Exe != "/tmp/helpers/keyboard.exe" || len(got.Args) != 0 {
		t.Fatalf("wrapped program = %#v", got)
	}
	if got.Env[wineKeyboardTargetEnv] != `Z:\mnt\SDCARD\My Apps\notepad++.exe` {
		t.Fatalf("target = %q", got.Env[wineKeyboardTargetEnv])
	}
	wantCommand := `"Z:\mnt\SDCARD\My Apps\notepad++.exe" "" "one two" "C:\dir with space\\" "said \"hi\""`
	if got.Env[wineKeyboardCommandEnv] != wantCommand {
		t.Fatalf("command line = %q, want %q", got.Env[wineKeyboardCommandEnv], wantCommand)
	}
	if got.Env["WINEPREFIX"] != "/tmp/prefix" || got.Env["jukahub_vk_target"] != "" {
		t.Fatalf("environment = %#v", got.Env)
	}
	if launch.Env["jukahub_vk_target"] != "stale value" || len(launch.Args) != 4 {
		t.Fatalf("original launch changed: %#v", launch)
	}
}

func TestWineKeyboardWrapRejectsUnpassableArgs(t *testing.T) {
	for _, launch := range []wineLaunch{
		{Exe: "relative/app.exe"},
		{Exe: "/mnt/app.exe", Args: []string{"bad\x00arg"}},
		{Exe: "/mnt/app.exe", Args: []string{strings.Repeat("👋", 2048)}},
	} {
		if _, err := wineKeyboardWrap(launch, "/tmp/keyboard.exe"); err == nil {
			t.Fatalf("accepted unpassable launch: %#v", launch)
		}
	}
}
