package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Text fields accept whatever the on-screen keyboard or a pasted string puts
// in them, so deleting one character must delete one rune. These tests pin the
// helper and, more importantly, stop a future handler from going back to
// slicing bytes (which left half a rune behind and looked like a dead key).
func TestTrimLastRune(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "ab"},
		{"café", "caf"},
		{"日本語", "日本"},
		{"e\u0301", "e"},   // combining acute: one grapheme, two runes
		{"a\xff", "a"},     // invalid trailing byte is dropped whole
		{"\xff", ""},       // ...and never leaves an empty-but-broken field
		{"line\n", "line"}, // newline deletes like any other rune
		{"🌙", ""},          // astral plane rune
		{"🌙🌙x", "🌙🌙"},
	}
	for _, c := range cases {
		if got := trimLastRune(c.in); got != c.want {
			t.Errorf("trimLastRune(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestTrimLastRuneIsRepeatable models holding backspace: repeatedly deleting
// from a multi-byte string must empty it in exactly the number of runes, never
// stall on a byte it cannot remove.
func TestTrimLastRuneIsRepeatable(t *testing.T) {
	for _, s := range []string{"café", "日本語のテキスト", "e\u0301e\u0301", "🌙ok"} {
		remaining := s
		steps := 0
		for remaining != "" {
			next := trimLastRune(remaining)
			if next == remaining {
				t.Fatalf("trimLastRune(%q) made no progress", remaining)
			}
			remaining = next
			if steps++; steps > len(s) {
				t.Fatalf("trimLastRune did not terminate on %q", s)
			}
		}
	}
}

// TestNoByteSlicedTextEdits walks the package for `x = x[:len(x)-1]` outside
// tests and fails on any hit. That pattern is only safe on pure ASCII, and the
// UI has no way to prove a buffer is ASCII: every text field is reachable from
// the on-screen keyboard, which inserts arbitrary runes.
func TestNoByteSlicedTextEdits(t *testing.T) {
	selfSlice := regexp.MustCompile(`([\w.]+)\s*=\s*([\w.]+)\[:len\(([\w.]+)\)-1\]`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	// Dropping the last element of a line/point list is not text editing; each
	// entry here is (file:variable) -> the element type, so a new hit forces a
	// classification instead of quietly widening the rule.
	containerSlices := map[string]string{
		"changelog.go:out":            "[]string release-note lines",
		"chat.go:runes":               "[]rune, already rune-based",
		"core.go:sceneHistory":        "[]int scene ids",
		"extras.go:parts":             "[]string measured words",
		"games.go:snakeBody":          "[]point",
		"ppsspp_calibration.go:lines": "[]string config lines",
		"webpage.go:p.flexStack":      "[]webFlexFrame open flex containers, not text",
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			m := selfSlice.FindStringSubmatch(line)
			if m == nil || m[1] != m[2] || m[2] != m[3] {
				continue
			}
			if _, ok := containerSlices[name+":"+m[1]]; ok {
				continue // drops one element from a slice of lines/points, not a string
			}
			t.Errorf("%s:%d slices a buffer by one byte (%s); use trimLastRune", name, i+1, m[0])
		}
	}
}

// TestWrapTextLines pins the word wrapper used by the update modal and the
// Patch confirmation: it breaks at spaces, honours explicit newlines, never
// overflows the box, and marks a cut instead of silently losing the tail.
func TestWrapTextLines(t *testing.T) {
	newProbeFrame(t) // initialises SDL_ttf, which loading a font needs
	config := &Config{}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}
	measure := func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}

	lines := wrapTextLines(font, "one two three four five six seven eight nine ten", 120, 10)
	if len(lines) < 2 {
		t.Fatalf("expected the sentence to wrap, got %#v", lines)
	}
	for _, l := range lines {
		if measure(l) > 120 {
			t.Errorf("line %q is %dpx, limit 120", l, measure(l))
		}
		if strings.Contains(strings.TrimSpace(l), "  ") {
			t.Errorf("line %q kept a double space", l)
		}
	}
	if !strings.HasPrefix(lines[0], "one two") {
		t.Errorf("first line = %q, want it to start at the first word", lines[0])
	}

	// Newlines are paragraph breaks, not glyphs.
	paras := wrapTextLines(font, "alpha beta\ngamma delta", 600, 4)
	if len(paras) != 2 || paras[0] != "alpha beta" || paras[1] != "gamma delta" {
		t.Fatalf("paragraph wrap = %#v", paras)
	}

	// A body longer than the budget keeps a readable ellipsis on the last line.
	long := wrapTextLines(font, strings.Repeat("word ", 400), 300, 3)
	if len(long) != 3 {
		t.Fatalf("maxLines 3 produced %d lines", len(long))
	}
	if !strings.HasSuffix(long[2], "…") {
		t.Errorf("last line %q is missing the cut marker", long[2])
	}
	if measure(long[2]) > 300 {
		t.Errorf("last line %q is %dpx, limit 300", long[2], measure(long[2]))
	}

	// A single unbreakable token still fits the budget via the ellipsis.
	wordy := wrapTextLines(font, strings.Repeat("x", 500), 200, 2)
	for _, l := range wordy {
		if measure(l) > 200 {
			t.Errorf("long token line %q is %dpx, limit 200", l, measure(l))
		}
	}
}
