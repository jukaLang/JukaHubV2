package main

import (
	"strings"
	"testing"
)

// TestCoreTrimuiSuperseded pins which cores the handheld leaves out because a
// faster emulator for their console plays there (coretrimui.go), and that a PC
// — and a console no better emulator covers — keeps them.
func TestCoreTrimuiSuperseded(t *testing.T) {
	cases := []struct {
		core     string
		handheld bool
		ppsspp   bool
		want     bool
	}{
		// N64: Mupen64Plus-Next is the faster core on this chip.
		{"parallel_n64", true, false, true},
		{"mupen64plus", true, false, true},
		{"mupen64plus_next", true, true, false},
		{"mupen64plus_next_gles2", true, true, false},
		// PSP: JukaHub's own PPSSPP is newer than the core, where it installs.
		{"ppsspp", true, true, true},
		{"ppsspp", true, false, false},
		// Everything else is offered as before.
		{"snes9x", true, true, false},
		{"pcsx_rearmed", true, true, false},
		{"dolphin", true, true, false},
		{"", true, true, false},
		// A PC keeps what the buildbot publishes.
		{"parallel_n64", false, true, false},
		{"ppsspp", false, true, false},
	}
	for _, c := range cases {
		got := coreTrimuiSuperseded(c.core, c.handheld, c.ppsspp) != ""
		if got != c.want {
			t.Errorf("coreTrimuiSuperseded(%q, handheld=%v, ppsspp=%v) left out = %v, want %v",
				c.core, c.handheld, c.ppsspp, got, c.want)
		}
	}
	// The reason names what plays the games instead, so the log reads as advice.
	for core, name := range map[string]string{
		"parallel_n64": "Mupen64Plus-Next",
		"mupen64plus":  "Mupen64Plus-Next",
		"ppsspp":       "PPSSPP",
	} {
		if why := coreTrimuiSuperseded(core, true, true); !strings.Contains(why, name) {
			t.Errorf("%s: reason %q does not name %s", core, why, name)
		}
	}
}
