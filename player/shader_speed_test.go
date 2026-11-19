package main

import (
	"math"
	"testing"
)

func TestShaderSpeedParsesRetroArchLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		log     string
		hz      float64
		dev     float64
		samples int
		reason  string
		ok      bool
	}{
		{"current", "[INFO] [Video]: Average monitor Hz: 59.941540 Hz. (0.512 % frame time deviation, 2048 samples).\n", 59.94154, 0.512, 2048, "", true},
		{"based on", "[INFO] [Video]: Average monitor Hz: 47.123000 Hz. (19.027 % frame time deviation, based on 2048 last samples).", 47.123, 19.027, 2048, "", true},
		{"old prefix", "RetroArch [INFO] :: Average monitor Hz: 60.000000 Hz. (1.000 % frame time deviation, 1024 samples).", 60, 1, 1024, "", true},
		{"rate only", "Average monitor Hz: 50.000000 Hz.", 50, 0, 0, "", true},
		{"threaded", "[INFO] [Video]: Monitor FPS estimation is disabled for threaded video.", 0, 0, 0, "threaded", true},
		{"short", "[INFO] [Video]: Does not have enough samples for monitor refresh rate estimation. Requires to run for at least 4096 frames.", 0, 0, 0, "short", true},
		{"most samples win", "Average monitor Hz: 30.0 Hz. (1.0 % frame time deviation, 100 samples).\r\nAverage monitor Hz: 59.9 Hz. (2.5 % frame time deviation, 2048 samples).\r\n[INFO] [Video]: Does not have enough samples for monitor refresh rate estimation.\r\n", 59.9, 2.5, 2048, "", true},
		{"not a rate", "Average monitor Hz: NaN Hz.\nAverage monitor Hz: fast Hz.\nAverage monitor Hz: -3 Hz.\n[INFO] [Content]: Loading content file.", 0, 0, 0, "", false},
		{"empty", "", 0, 0, 0, "", false},
	} {
		got, ok := shaderSpeedParseLog(tc.log)
		if ok != tc.ok || got.Reason != tc.reason || got.Samples != tc.samples ||
			math.Abs(got.Hz-tc.hz) > 1e-9 || math.Abs(got.Deviation-tc.dev) > 1e-9 {
			t.Errorf("%s: got %+v, %v", tc.name, got, ok)
		}
	}
	if shaderSpeedTarget("Game (Europe).sfc") != 50 || shaderSpeedTarget("Game (USA).sfc") != 60 {
		t.Fatal("PAL and NTSC targets differ")
	}
}

// The look presets and shaders come from libretro's pack, GitHub and the
// card: their parsers (shader_looks.go) and the pack path check.
func TestLookPresetAndParameterParsing(t *testing.T) {
	p := lookParsePreset("\ufeff# a comment\r\nshaders = 2\r\nshader0 = \"shaders/zfast_crt.glsl\"\r\nfilter_linear0 = true\r\n" +
		"shader1 = shaders/second.glsl # trailing\r\nshader5 = ignored.glsl\r\ntextures = \"LUT;Mask\"\r\nLUT = \"../luts/a.png\"\r\nMask = mask.png\r\nBLURSCALE = \"0.5\"\r\n")
	if len(p.Shaders) != 2 || p.Shaders[0] != "shaders/zfast_crt.glsl" || p.Shaders[1] != "shaders/second.glsl" ||
		len(p.Textures) != 2 || p.Textures[0] != "../luts/a.png" || p.Values["BLURSCALE"] != "0.5" || p.Reference != "" {
		t.Fatalf("full preset: %+v", p)
	}
	simple := lookParsePreset("#reference \"../crt/zfast-crt.glslp\"\nHIGHSCANAMOUNT1 = \"0.400000\"\n")
	if simple.Reference != "../crt/zfast-crt.glslp" || len(simple.Shaders) != 0 || simple.Values["HIGHSCANAMOUNT1"] != "0.400000" {
		t.Fatalf("simple preset: %+v", simple)
	}
	params := lookParseParams("#pragma parameter PT_GAME_SYSTEM \"Game system\" 2.0 1.0 4.0 1.0\n" +
		"  #pragma  parameter GRID \"Grid line opacity [Grid]\" 0.5 0.0 1.0 0.05\r\n" +
		"#pragma parameter NOSTEP \"No step\" 5 0 10\n" +
		"#pragma parameter GRID \"duplicate\" 1 0 1 1\n" +
		"#pragma parameter BAD-NAME \"x\" 1 0 1 1\n" +
		"#pragma parameter BACKWARDS \"x\" 1 2 0 1\n" +
		"#pragma parameter SHORT \"x\" 1 0\n" +
		"#pragma parameterX \"x\" 1 0 1 1\n" +
		"#pragma parameter CLAMPED \"Clamped\" 9.0f 0.0f 1.0f 0.1f\n")
	want := []lookParam{
		{Name: "PT_GAME_SYSTEM", Label: "Game system", Default: 2, Min: 1, Max: 4, Step: 1},
		{Name: "GRID", Label: "Grid line opacity [Grid]", Default: 0.5, Min: 0, Max: 1, Step: 0.05},
		{Name: "NOSTEP", Label: "No step", Default: 5, Min: 0, Max: 10, Step: 1},
		{Name: "CLAMPED", Label: "Clamped", Default: 1, Min: 0, Max: 1, Step: 0.1},
	}
	if len(params) != len(want) {
		t.Fatalf("params: %+v", params)
	}
	for i, w := range want {
		g := params[i]
		if g.Name != w.Name || g.Label != w.Label || math.Abs(g.Default-w.Default) > 1e-9 || g.Min != w.Min || g.Max != w.Max || math.Abs(g.Step-w.Step) > 1e-9 {
			t.Errorf("param %d: got %+v, want %+v", i, g, w)
		}
	}
	for _, tc := range []struct {
		dir, ref, want string
		ok             bool
	}{
		{"handheld", "shaders/PT_SkyWalker541.glsl", "handheld/shaders/PT_SkyWalker541.glsl", true},
		{"handheld", "../crt/shaders/zfast_crt.glsl", "crt/shaders/zfast_crt.glsl", true},
		{"crt", "../../etc/passwd", "", false},
		{"crt", "/etc/passwd", "", false},
		{"crt", `C:\Windows\x.glsl`, "", false},
		{"crt", `..\..\x.glsl`, "", false},
	} {
		got, ok := lookRepoJoin(tc.dir, tc.ref)
		if ok != tc.ok || got != tc.want {
			t.Errorf("lookRepoJoin(%q, %q) = %q, %v", tc.dir, tc.ref, got, ok)
		}
	}
}
