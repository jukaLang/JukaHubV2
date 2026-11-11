package main

// Kept from the chat-file tests when the suite was cut to its core: the
// upload name is the one part a remote server sees verbatim.

import "testing"

// Upload names keep letters (any script), digits, dots and dashes, turn runs
// of anything else into one underscore, and never carry a path or a line
// break into the part header.
func TestChatUploadName(t *testing.T) {
	cases := map[string]string{
		"my save (1).srm":     "my_save_1.srm",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\game.gba`: "game.gba",
		"Покемон.sav":         "Покемон.sav",
		"a\r\nb.txt":          "a_b.txt",
		"":                    "file",
		"  .bashrc":           "file.bashrc",
		"report.final.v2.log": "report.final.v2.log",
	}
	for in, want := range cases {
		if got := chatUploadName(in); got != want {
			t.Errorf("chatUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}
