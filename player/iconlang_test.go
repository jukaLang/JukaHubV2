package main

// iconlang_test.go — the icon table and its language: every canonical name is
// drawn, every source parses, every glyph paints sensibly at every size, and
// strokes land crisp on the pixel grid. Pure Go: no renderer.

import (
	"math"
	"strings"
	"testing"
)

// iconCanonicalNames is the contract every area draws against (the icon list
// in the agents' shared rules). A name here must never fall back.
var iconCanonicalNames = []string{
	// Home and apps.
	"continue", "media", "files", "packages", "chat", "favorites", "apps", "settings", "tools",
	"browser", "emulator", "exit", "play", "pause", "back", "search", "mic", "clock", "alarm",
	"music", "video", "image", "pdf", "text", "folder", "folder-up", "download", "upload",
	"trash", "star", "heart", "share", "copy", "paste", "edit", "info", "warning", "check",
	"close", "plus", "minus", "refresh", "lock", "unlock", "sun", "moon", "battery", "wifi",
	"wifi-off", "bluetooth", "globe", "link", "home", "user", "users", "bell", "calendar",
	"camera", "grid", "list", "rows", "filter", "sort", "keyboard", "language", "sparkles",
	"trophy", "gamepad", "juka",
	// Tools.
	"terminal", "schedule", "startup", "hardware", "disk", "benchmark", "logs", "broom",
	"update", "power", "disc", "calculator", "bug", "community", "network", "party", "toolbox",
	"shield", "audit", "server", "key", "phone", "radar", "route", "dns", "devices",
	"public-ip", "plug", "id-card", "code", "clock-check", "subnet", "speed", "qr", "cpu",
	"memory", "thermometer", "plugin", "theme", "palette", "backup", "sync", "cloud", "rom",
	"scan",
	// Emulators and systems.
	"emu-retroarch", "emu-nes", "emu-snes", "emu-n64", "emu-gb", "emu-gbc", "emu-gba",
	"emu-nds", "emu-3ds", "emu-gc", "emu-wii", "emu-wiiu", "emu-switch", "emu-ps1", "emu-ps2",
	"emu-ps3", "emu-psp", "emu-vita", "emu-genesis", "emu-sms", "emu-gg", "emu-dc",
	"emu-saturn", "emu-xbox", "emu-arcade", "emu-pce", "emu-neogeo", "emu-dos", "emu-scummvm",
	"emu-atari", "emu-generic",
}

func TestIconTableParses(t *testing.T) {
	lib := iconLibGet()
	for _, err := range lib.errs {
		t.Error(err)
	}
	if len(lib.glyphs) != len(iconDefs) {
		t.Errorf("%d glyphs parsed from %d definitions", len(lib.glyphs), len(iconDefs))
	}
	if _, ok := lib.glyphs[iconFallbackName]; !ok {
		t.Fatalf("the fallback glyph %q is not defined", iconFallbackName)
	}
	for _, d := range iconDefs {
		if d.name != strings.ToLower(strings.TrimSpace(d.name)) || d.name == "" {
			t.Errorf("icon name %q must be lower-case without padding", d.name)
		}
	}
}

func TestIconCanonicalNamesAreDrawn(t *testing.T) {
	for _, name := range iconCanonicalNames {
		if got := iconResolve(name); got != name {
			t.Errorf("iconResolve(%q) = %q, want the glyph itself", name, got)
		}
	}
}

func TestIconAliases(t *testing.T) {
	lib := iconLibGet()
	seen := map[string]bool{}
	for _, pair := range iconAliasPairs {
		alias, target := pair[0], pair[1]
		if seen[alias] {
			t.Errorf("alias %q is listed twice", alias)
		}
		seen[alias] = true
		if len(alias) < 2 {
			t.Errorf("alias %q is a single letter; one-letter icons are monograms", alias)
		}
		if _, ok := lib.glyphs[alias]; ok {
			t.Errorf("alias %q shadows a glyph of the same name", alias)
		}
		if _, ok := lib.glyphs[target]; !ok {
			t.Errorf("alias %q points at %q, which is not a glyph", alias, target)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"emulators", "gamepad"},
		{"controller", "gamepad"},
		{"tube", "media"},
		{"  Settings ", "settings"},
		{"GEAR", "settings"},
		{"box", "packages"},
		{"snes", "emu-snes"},
		{"", iconFallbackName},
		{"definitely-not-an-icon", iconFallbackName},
	} {
		if got := iconResolve(tc.in); got != tc.want {
			t.Errorf("iconResolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if iconKnown("definitely-not-an-icon") || !iconKnown("Tube") || !iconKnown(iconFallbackName) {
		t.Error("iconKnown disagrees with the table")
	}
}

// Every glyph paints a visible picture inside its box at every size the shell
// uses, without flooding it.
func TestIconEveryGlyphRendersAtEverySize(t *testing.T) {
	var cv vecCanvas
	var path vecPath
	for _, name := range iconLibGet().order {
		for _, size := range []int{14, 17, 18, 22, 24, 32, 40, 48, 64} {
			buf := make([]uint8, size*size)
			iconRasterize(name, size, buf, &cv, &path)
			ink, solid := 0.0, 0
			for _, v := range buf {
				ink += float64(v) / 255
				if v == 255 {
					solid++
				}
			}
			area := float64(size * size)
			// The sparsest glyph, minus, inks about 4% of its box.
			if ink < area*0.025 || solid == 0 {
				t.Errorf("%s at %dpx: ink %.0f of %.0f px, %d solid — too faint", name, size, ink, area, solid)
			}
			if ink > area*0.85 {
				t.Errorf("%s at %dpx: ink %.0f of %.0f px — a blob, not a glyph", name, size, ink, area)
			}
			for i := 0; i < len(cv.alpha) && i < size*size; i++ {
				if v := float64(cv.alpha[i]); math.IsNaN(v) || v < 0 || v > 1.0001 {
					t.Fatalf("%s at %dpx: coverage %v out of range", name, size, v)
				}
			}
		}
	}
}

// The base stroke is a whole number of pixels from 22px up and keeps a
// two-unit weight below that.
func TestIconStrokeWidths(t *testing.T) {
	for _, tc := range []struct {
		size int
		want float64
	}{
		{12, 1}, {18, 1.5}, {21, 1.75}, {22, 2}, {24, 2}, {32, 2}, {36, 3}, {40, 3}, {48, 3}, {56, 4}, {64, 4},
	} {
		if got := iconStrokePx(tc.size); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("iconStrokePx(%d) = %v, want %v", tc.size, got, tc.want)
		}
	}
	for _, tc := range []struct{ w, want float64 }{{1, 0.5}, {1.5, 0.5}, {2, 0}, {2.5, 0}, {3, 0.5}, {4, 0}} {
		if got := iconStrokeParity(tc.w); got != tc.want {
			t.Errorf("iconStrokeParity(%v) = %v, want %v", tc.w, got, tc.want)
		}
	}
}

// Snapping keeps a horizontal stroke a solid band exactly the stroke wide,
// with no half-covered rows, at sizes where a grid unit is not a whole pixel.
func TestIconStrokesSnapToThePixelGrid(t *testing.T) {
	var cv vecCanvas
	var path vecPath
	for _, size := range []int{24, 32, 40, 48, 64} {
		buf := make([]uint8, size*size)
		iconRasterize("minus", size, buf, &cv, &path)
		solidRows, softRows := 0, 0
		mid := size / 2
		for y := 0; y < size; y++ {
			switch v := buf[y*size+mid]; {
			case v == 255:
				solidRows++
			case v != 0:
				softRows++
			}
		}
		if want := int(iconStrokePx(size)); solidRows != want || softRows != 0 {
			t.Errorf("minus at %dpx: %d solid and %d soft rows through the middle, want %d solid and none soft",
				size, solidRows, softRows, want)
		}
	}
}

func TestIconParseErrors(t *testing.T) {
	for _, src := range []string{
		"",
		"M1 2",
		"s:",
		"s: M1",
		"s: Q1 2 3",
		"z: M1 2",
		"sw: M1 2",
		"sy: M1 2",
		"s: M1 2 s:",
		"s: M1 2 X3 4",
		"s: 1 2",
		"s: M1.2.3 4",
	} {
		if _, err := iconParse(src); err == nil {
			t.Errorf("iconParse(%q) succeeded, want an error", src)
		}
	}
	ops, err := iconParse("Dw1.5o.4ebn: M1 2 3 4 Z sa: O12 12 2 U1 1 1 0 90")
	if err != nil {
		t.Fatalf("valid source failed: %v", err)
	}
	if len(ops) != 2 || ops[0].kind != 'D' || ops[0].width != 1.5 || ops[0].opacity != 0.4 ||
		!ops[0].evenOdd || ops[0].cap != vecCapButt || !ops[0].noSnap || !ops[1].align {
		t.Fatalf("parsed ops = %+v", ops)
	}
	// M's extra coordinate pairs continue as lines.
	if c := ops[0].cmds; len(c) != 3 || c[0].op != 'M' || c[1].op != 'L' || c[2].op != 'Z' {
		t.Fatalf("M1 2 3 4 Z parsed as %+v", c)
	}
}
