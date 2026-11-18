package main

import (
	"strings"
	"testing"
)

// The deck builds shell lines from names found on the card: quoting is the
// security boundary between a file name and a command.
func TestCmdDeckQuoteKeepsNamesArguments(t *testing.T) {
	sh := []struct{ in, want string }{
		{"/mnt/SDCARD/Roms", "/mnt/SDCARD/Roms"},
		{"/mnt/SDCARD/Game Boy", "'/mnt/SDCARD/Game Boy'"},
		{"$(reboot).zip", "'$(reboot).zip'"},
		{"a'b`c`;rm -rf ~", `'a'\''b` + "`c`" + `;rm -rf ~'`},
		{"", "''"},
	}
	for _, c := range sh {
		got, ok := cmdDeckQuote(c.in, cmdQuoteSh)
		if !ok || got != c.want {
			t.Errorf("sh quote %q = %q,%v; want %q", c.in, got, ok, c.want)
		}
	}
	if got, ok := cmdDeckQuote(`C:\Games\A & B`, cmdQuoteCmd); !ok || got != `"C:\Games\A & B"` {
		t.Errorf("cmd quote = %q,%v", got, ok)
	}
	for _, bad := range []string{`C:\%PATH%`, `a"b`, "line\nbreak"} {
		if _, ok := cmdDeckQuote(bad, cmdQuoteCmd); ok {
			t.Errorf("cmd quote accepted %q", bad)
		}
		if _, ok := cmdDeckQuote(bad, cmdQuotePS); ok {
			t.Errorf("ps quote accepted %q", bad)
		}
	}
	// PowerShell treats the typographic single quotes as quotes too.
	if got, ok := cmdDeckQuote("it’s 'x'", cmdQuotePS); !ok || got != "it’’s ''x''" {
		t.Errorf("ps quote = %q,%v", got, ok)
	}
}

func TestCmdDeckExpandRefusesBadSlotValues(t *testing.T) {
	slots := []cmdSlot{
		{label: "Lines", kind: cmdSlotInt, min: 1, max: 100},
		{label: "File", kind: cmdSlotFile},
	}
	line, ok, _ := cmdDeckExpand("tail -n {1} {2}", slots, []string{"40", "/x/my log.txt"}, nil, cmdQuoteSh)
	if !ok || line != "tail -n 40 '/x/my log.txt'" {
		t.Fatalf("expand = %q,%v", line, ok)
	}
	for _, n := range []string{"0", "101", "4;reboot", ""} {
		if _, ok, missing := cmdDeckExpand("tail -n {1} {2}", slots, []string{n, "/x"}, nil, cmdQuoteSh); ok || missing != 0 {
			t.Errorf("lines %q accepted (missing %d)", n, missing)
		}
	}
	choice := []cmdSlot{{label: "Host", kind: cmdSlotChoice, choices: []string{"1.1.1.1", "@gw"}}}
	if _, ok, _ := cmdDeckExpand("ping {1}", choice, []string{"evil.example;reboot"}, nil, cmdQuoteSh); ok {
		t.Error("a choice outside the list was accepted")
	}
	if line, ok, _ := cmdDeckExpand("ping {1}", choice, []string{"@gw"}, map[string]string{"gw": "192.168.1.1"}, cmdQuoteSh); !ok || line != "ping 192.168.1.1" {
		t.Errorf("router choice = %q,%v", line, ok)
	}
	// PowerShell's own braces are not placeholders.
	if line, ok, _ := cmdDeckExpand("x @{n='a';e={1}} {@app}", nil, nil, map[string]string{"app": "/opt/j"}, cmdQuoteSh); !ok || line != "x @{n='a';e={1}} /opt/j" {
		t.Errorf("braces = %q,%v", line, ok)
	}
}

// The favorites file is read back from the card: caps, control characters and
// junk must not get through.
func TestParseCmdDeckStoreSanitizes(t *testing.T) {
	long := strings.Repeat("x", cmdFavMaxLineBytes+1)
	data := `{"version":1,"favorites":[
		{"name":"df","lines":["df -h"]},
		{"name":"dup","lines":["df -h"]},
		{"name":"","lines":["ls","  ","bad\u0007bell","` + long + `","pwd"]},
		{"name":"empty","lines":[]}
	],"recent":{"folder":["/a","/b","/c","/d","/e","/f"],"x\ny":["z"]}}`
	s, err := parseCmdDeckStore([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Favorites) != 2 {
		t.Fatalf("favorites = %+v", s.Favorites)
	}
	if got := s.Favorites[1].Lines; len(got) != 2 || got[0] != "ls" || got[1] != "pwd" {
		t.Errorf("macro lines = %q", got)
	}
	if s.Favorites[1].Name == "" {
		t.Error("unnamed favorite got no name")
	}
	if len(s.Recent["folder"]) != cmdRecentMaxValues || len(s.Recent) != 1 {
		t.Errorf("recent = %v", s.Recent)
	}
	if _, err := parseCmdDeckStore([]byte("{not json")); err == nil {
		t.Error("corrupt file parsed")
	}
	if s, err := parseCmdDeckStore(nil); err != nil || len(s.Favorites) != 0 {
		t.Error("empty file is not an empty store")
	}
	out, err := formatCmdDeckStore(s)
	if err != nil || !strings.Contains(string(out), `"version": 1`) {
		t.Errorf("format = %s, %v", out, err)
	}
}
