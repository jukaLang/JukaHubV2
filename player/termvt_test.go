package main

import (
	"strings"
	"testing"
)

// TestVTTermParsesProgramOutput feeds the emulator what real programs send
// (and some garbage) and checks the screen it ends up with. Each case starts
// on a fresh 10x4 terminal.
func TestVTTermParsesProgramOutput(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		lines []string // expected main-screen text, trailing blank lines dropped
		check func(*vtTerm) string
	}{
		{name: "plain text and CRLF", in: "ab\r\ncd", lines: []string{"ab", "cd"}},
		{name: "autowrap with pending wrap", in: "0123456789X", lines: []string{"0123456789", "X"}},
		{name: "cursor position and erase line", in: "hello\x1b[1;3H\x1b[K", lines: []string{"he"}},
		{name: "erase display", in: "aa\r\nbb\x1b[2J\x1b[Hc", lines: []string{"c"}},
		{name: "insert and delete chars", in: "abcd\x1b[1;2H\x1b[2@\x1b[1;5H\x1b[1P", lines: []string{"a  bd"}},
		{name: "scroll region", in: "1\r\n2\r\n3\r\n4\x1b[2;3r\x1b[3;1H\n", lines: []string{"1", "3", "", "4"}},
		{name: "UTF-8 split and wide", in: "\xc3", check: func(v *vtTerm) string {
			v.Feed([]byte("\xa9\xe4\xb8\xad|"))
			if got := v.textLines(); len(got) != 1 || got[0] != "é中|" {
				return "got " + strings.Join(got, "/")
			}
			if v.main[0][1].w != 2 || v.main[0][2].w != 0 || v.x != 4 {
				return "wide cell layout wrong"
			}
			return ""
		}},
		{name: "DEC line drawing", in: "\x1b(0lqk\x1b(Bx", lines: []string{"┌─┐x"}},
		{name: "SGR colours", in: "\x1b[1;38;5;196;48;2;1;2;3mR\x1b[0mn", check: func(v *vtTerm) string {
			c := v.main[0][0]
			if c.attr&vtAttrBold == 0 || c.fg != vtPaletteColor(196) || c.bg != vtTrueColor(1, 2, 3) {
				return "styled cell wrong"
			}
			if v.main[0][1].attr != 0 || v.main[0][1].fg != vtColorDefault {
				return "reset did not reset"
			}
			return ""
		}},
		{name: "alternate screen keeps the main one", in: "main\x1b[?1049h\x1b[2Jalt\x1b[?1049l", lines: []string{"main"}},
		{name: "ED 3 keeps the screen, 1047 clears the alternate one on exit", in: "m\x1b[3J\x1b[?1047hx\x1b[?1047l\x1b[?1047h", lines: []string{"m"}, check: func(v *vtTerm) string {
			for _, line := range v.alt {
				for _, c := range line {
					if c.r == 'x' {
						return "alternate screen kept its text"
					}
				}
			}
			return ""
		}},
		{name: "title and DSR reply", in: "\x1b]2;my title\x07\x1b[6n", check: func(v *vtTerm) string {
			if v.title != "my title" {
				return "title " + v.title
			}
			return ""
		}},
		{name: "garbage never panics", in: "\x1b[999999;-5H\x1b[?;;;;;;;;;h\x1b]" + strings.Repeat("x", 9000) + "\x1b\\\x1bP1$q\x1b\\\x1b[38;2m\x1b[r\x1b[0;0r\x9b\xff\xfe\x1b(\x1b#8", check: func(v *vtTerm) string {
			if v.x < 0 || v.x >= v.cols || v.y < 0 || v.y >= v.rows {
				return "cursor out of the grid"
			}
			return ""
		}},
		{name: "scrollback and resize", in: "1\r\n2\r\n3\r\n4\r\n5\r\n6", check: func(v *vtTerm) string {
			if v.sbLen != 2 {
				return "scrollback length wrong"
			}
			v.Resize(6, 2)
			if got := v.textLines(); len(got) != 2 || got[0] != "5" || got[1] != "6" {
				return "after resize " + strings.Join(got, "/")
			}
			return ""
		}},
	}
	for _, tc := range cases {
		var replies []string
		v := newVTTerm(10, 4, func(b []byte) { replies = append(replies, string(b)) })
		v.Feed([]byte(tc.in))
		if tc.lines != nil {
			if got := v.textLines(); strings.Join(got, "\n") != strings.Join(tc.lines, "\n") {
				t.Errorf("%s: screen %q, want %q", tc.name, got, tc.lines)
			}
		}
		if tc.check != nil {
			if msg := tc.check(v); msg != "" {
				t.Errorf("%s: %s", tc.name, msg)
			}
		}
		if tc.name == "title and DSR reply" && (len(replies) != 1 || replies[0] != "\x1b[1;1R") {
			t.Errorf("DSR reply %q", replies)
		}
	}
}
