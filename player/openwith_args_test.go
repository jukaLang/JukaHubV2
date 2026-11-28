package main

import "testing"

func TestParseOpenWithArgs(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		scene, file string
		wantErr     bool
	}{
		{[]string{"--scene", "Media"}, "Media", "", false},
		{[]string{"--scene=Games"}, "Games", "", false},
		{[]string{"--scene", "Media", "/roms/game.zip"}, "Media", "/roms/game.zip", false},
		{[]string{"/roms/game.zip"}, "", "/roms/game.zip", false},
		{[]string{"--other", "x", "--scene=Home"}, "Home", "", false},
		{[]string{"--scene"}, "", "", true},
		{[]string{"--scene="}, "", "", true},
	} {
		scene, file, err := parseOpenWithArgs(tc.args)
		if (err != nil) != tc.wantErr || scene != tc.scene || file != tc.file {
			t.Errorf("parseOpenWithArgs(%q) = (%q,%q,%v), want (%q,%q,err=%v)",
				tc.args, scene, file, err, tc.scene, tc.file, tc.wantErr)
		}
	}
}
