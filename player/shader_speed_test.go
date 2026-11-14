package main

import "testing"

func TestShaderSpeedReadsOnlyPlayedFrameCounter(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  uint64
		ok    bool
	}{
		{"frame_count\t1492", 1492, true},
		{"frame_count 0", 0, true},
		{"GET_STATUS PLAYING", 0, false},
		{"frame_count not-a-number", 0, false},
	} {
		got, ok := shaderSpeedParseFrameCount(tc.reply)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q = %d, %v", tc.reply, got, ok)
		}
	}
	if shaderSpeedTarget("Game (Europe).sfc") != 50 || shaderSpeedTarget("Game (USA).sfc") != 60 {
		t.Fatal("PAL and NTSC targets differ")
	}
	for _, reply := range []string{"fast_forward: true", "fast_forward 1", "fast_forward on"} {
		if !shaderSpeedFastForward(reply) {
			t.Errorf("missed fast forward reply %q", reply)
		}
	}
	if shaderSpeedFastForward("fast_forward false") || shaderSpeedFastForward("unknown on") {
		t.Fatal("normal play was mistaken for fast forward")
	}
}
