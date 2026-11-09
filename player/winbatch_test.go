package main

// Tests for the batch-file command line (winbatch.go): every piece is quoted,
// cmd.exe's metacharacters stay inside quotes, and what quotes cannot hold is
// refused. Pure string building — nothing is started.

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

const testComspec = `C:\Windows\system32\cmd.exe`

func TestWinBatchCommandLineQuotesEveryPiece(t *testing.T) {
	prefix := `"` + testComspec + `" /d /v:off /s /c `
	cases := []struct {
		script string
		args   []string
		want   string
	}{
		{`C:\Apps\Game.bat`, nil, prefix + `""C:\Apps\Game.bat""`},
		{`C:\Apps\Game (USA) & Friends.bat`, nil, prefix + `""C:\Apps\Game (USA) & Friends.bat""`},
		{`C:\Emus\run.cmd`, []string{`D:\Roms\Mario & Luigi (USA).gba`, "--fullscreen", ""},
			prefix + `""C:\Emus\run.cmd" "D:\Roms\Mario & Luigi (USA).gba" "--fullscreen" """`},
		{`C:\Wow!\a^b.bat`, []string{"a|b", "<in", ">out", "@echo"},
			prefix + `""C:\Wow!\a^b.bat" "a|b" "<in" ">out" "@echo""`},
	}
	for _, tc := range cases {
		got, err := winBatchCommandLine(testComspec, tc.script, tc.args)
		if err != nil {
			t.Errorf("winBatchCommandLine(%q, %q): %v", tc.script, tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("winBatchCommandLine(%q, %q)\n got %s\nwant %s", tc.script, tc.args, got, tc.want)
		}
	}
}

// cmd.exe reads & | < > ( ) ^ @ as syntax only outside quotes. With /s it
// strips the outer pair and parses the rest, so walk that rest the way it
// does and check every such character sits inside a quoted piece.
func TestWinBatchCommandLineKeepsMetacharactersQuoted(t *testing.T) {
	line, err := winBatchCommandLine(testComspec, `C:\A&B (1)\x^y.cmd`, []string{"a&b", "c|d", "(e)", "f>g", "h<i", "^j", "@k"})
	if err != nil {
		t.Fatal(err)
	}
	marker := ` /c "`
	at := strings.Index(line, marker)
	if at < 0 || !strings.HasSuffix(line, `"`) {
		t.Fatalf("no /c \"…\" part in %s", line)
	}
	inner := line[at+len(marker) : len(line)-1]
	quoted := false
	for i, r := range inner {
		if r == '"' {
			quoted = !quoted
			continue
		}
		if strings.ContainsRune("&|<>()^@", r) && !quoted {
			t.Fatalf("%q at %d of %s is outside quotes", r, i, inner)
		}
	}
	if quoted {
		t.Fatalf("unbalanced quotes in %s", inner)
	}
}

// A quote would end the quoted piece, % expands even inside quotes, and a line
// break ends the command: none can be made literal, so they are refused.
func TestWinBatchCommandLineRefusesWhatQuotesCannotHold(t *testing.T) {
	for _, bad := range []string{`100%.bat`, `%PATH%`, `a"b`, "a\r\nb", "a\nb", "a\x00b"} {
		if _, err := winBatchCommandLine(testComspec, `C:\Apps\`+bad, nil); !errors.Is(err, errWinBatchUnsafe) {
			t.Errorf("script %q: err = %v, want errWinBatchUnsafe", bad, err)
		}
		if _, err := winBatchCommandLine(testComspec, `C:\Apps\ok.bat`, []string{"fine", bad}); !errors.Is(err, errWinBatchUnsafe) {
			t.Errorf("argument %q: err = %v, want errWinBatchUnsafe", bad, err)
		}
	}
	if _, err := winBatchCommandLine(testComspec, "  ", nil); err == nil {
		t.Error("an empty script path was accepted")
	}
	// A ComSpec that is empty or carries a quote falls back to plain cmd.exe.
	for _, comspec := range []string{"", `C:\x"y\cmd.exe`} {
		got, err := winBatchCommandLine(comspec, `C:\Apps\ok.bat`, nil)
		if err != nil || !strings.HasPrefix(got, `"cmd.exe" /d /v:off /s /c `) {
			t.Errorf("comspec %q: %s, %v", comspec, got, err)
		}
	}
}

// The cmd.exe started is %ComSpec% only when quoting can hold it and it names
// a drive; otherwise System32's under %SystemRoot%, never one found on PATH.
func TestWinBatchComspecFallsBackToSystem32(t *testing.T) {
	cases := []struct{ comspec, systemRoot, want string }{
		{testComspec, `C:\Windows`, testComspec},
		{` D:\Win\system32\cmd.exe `, `C:\Windows`, `D:\Win\system32\cmd.exe`},
		{"", `D:\Win\`, `D:\Win\System32\cmd.exe`},
		{"cmd.exe", `C:\Windows`, `C:\Windows\System32\cmd.exe`},
		{`C:\x"y\cmd.exe`, `C:\Windows`, `C:\Windows\System32\cmd.exe`},
		{`C:\100%\cmd.exe`, `C:\Win%x%`, `C:\Windows\System32\cmd.exe`},
		{`\\server\share\cmd.exe`, "", `C:\Windows\System32\cmd.exe`},
	}
	for _, tc := range cases {
		if got := winBatchComspec(tc.comspec, tc.systemRoot); got != tc.want {
			t.Errorf("winBatchComspec(%q, %q) = %q, want %q", tc.comspec, tc.systemRoot, got, tc.want)
		}
	}
}

// A game whose launcher is a batch file never reaches a shell unquoted: off
// Windows it is refused outright, and the other launchers are unchanged.
// (winbatch_windows_test.go pins the Windows command line.)
func TestGameLaunchExecRoutesBatchFiles(t *testing.T) {
	if !gameLauncherIsBatch(`C:\Emus\SNES\Launch.CMD`) || gameLauncherIsBatch("/emus/GBA/launch.sh") {
		t.Fatal("gameLauncherIsBatch misreads the extension")
	}
	name, args := gameLaunchCommandArgs("/emus/GBA/launch.sh", "/roms/GBA/a.gba", nil)
	cmd, err := gameLaunchExec("/emus/GBA/launch.sh", name, args)
	if err != nil || cmd == nil || len(cmd.Args) != 3 || cmd.Args[1] != "/emus/GBA/launch.sh" {
		t.Fatalf("shell launcher: %v, %v", cmd, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	launcher := `C:\Emus\SNES\launch.bat`
	name, args = gameLaunchCommandArgs(launcher, `D:\Roms\Tom&calc&.sfc`, nil)
	if cmd, err := gameLaunchExec(launcher, name, args); err == nil || cmd != nil {
		t.Fatalf("a batch launcher off Windows = %v, %v; want a refusal", cmd, err)
	}
}
