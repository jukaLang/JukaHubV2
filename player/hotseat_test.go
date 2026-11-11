package main

import "testing"

// A script forwards extra options only when they stay separate arguments. The
// stock and CrossMix launchers end in `... -L <core> "$*"`, which joins the ROM
// and every option into one argument: appending --appendconfig there broke the
// launch, so those scripts must count as not forwarding.
func TestHotseatScriptForwards(t *testing.T) {
	cases := []struct {
		name  string
		head  string
		batch bool
		want  bool
	}{
		{"quoted at", "#!/bin/sh\nexec retroarch -L core.so \"$@\"\n", false, true},
		{"braced at", "#!/bin/sh\nexec retroarch -L core.so \"${@}\"\n", false, true},
		{"bare star", "#!/bin/sh\nexec retroarch -L core.so $*\n", false, true},
		{"quoted star (stock TrimUI)", "#!/bin/sh\necho $0 $*\nHOME=$RA_DIR/ $RA_DIR/ra64.trimui -v -L $RA_DIR/.retroarch/cores/mgba_libretro.so \"$*\"\n", false, false},
		{"braced quoted star", "#!/bin/sh\nexec retroarch -L core.so \"${*}\"\n", false, false},
		{"first argument only", "#!/bin/sh\nexec retroarch -L core.so \"$1\"\n", false, false},
		// JukaHub's PPSSPP launcher logs with "$*" and runs the emulator with "$@".
		{"at wins over a logged star", "#!/bin/sh\nnote() { printf '%s\\n' \"$*\" >>log; }\nexec ppsspp --pause-menu-exit \"$@\"\n", false, true},
		{"batch forwards", "@echo off\r\nretroarch.exe -L core.dll %*\r\n", true, true},
		{"batch first only", "@echo off\r\nretroarch.exe -L core.dll %1\r\n", true, false},
	}
	for _, tc := range cases {
		if got := hotseatScriptForwards(tc.head, tc.batch); got != tc.want {
			t.Errorf("%s: forwards = %t, want %t", tc.name, got, tc.want)
		}
	}
}
