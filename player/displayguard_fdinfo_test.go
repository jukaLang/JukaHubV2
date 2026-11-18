package main

import "testing"

func TestDisplayGuardFDInfoReadOnly(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{
		{"pos:\t0\nflags:\t02404000\nmnt_id:\t14\n", true}, // FFmpeg fbdev input on Smart Pro
		{"flags:\t0100002\n", false},                       // writable framebuffer
		{"flags:\t0100001\n", false},                       // write-only framebuffer
		{"flags:\tbogus\n", false},
		{"mnt_id:\t14\n", false},
	} {
		if got := displayGuardFDInfoReadOnly(tc.info); got != tc.want {
			t.Errorf("fdinfo %q: read-only = %v, want %v", tc.info, got, tc.want)
		}
	}
}
