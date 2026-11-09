//go:build windows

package main

// Windows-only tests for winBatchCommand (winbatch_windows.go): the program
// it starts and the raw command line it hands CreateProcess, for an app and
// for a game launched through a batch file. Nothing is started.

import (
	"errors"
	"strings"
	"testing"
)

func TestWinBatchCommandSetsPathAndCmdLine(t *testing.T) {
	const comspec = `C:\Windows\system32\cmd.exe`
	t.Setenv("ComSpec", comspec)
	cmd, err := winBatchCommand(`C:\Emus\SNES (x86)\launch.bat`, `D:\Roms\Tom&calc&.sfc`, "--appendconfig=a|b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cmd.Path, comspec) {
		t.Errorf("Path = %q, want %q", cmd.Path, comspec)
	}
	want := `"` + comspec + `" /d /v:off /s /c ""C:\Emus\SNES (x86)\launch.bat" "D:\Roms\Tom&calc&.sfc" "--appendconfig=a|b""`
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("CmdLine = %+v\nwant %s", cmd.SysProcAttr, want)
	}

	// An unusable ComSpec is replaced once, for the program and the line alike.
	t.Setenv("ComSpec", `C:\x"y\cmd.exe`)
	t.Setenv("SystemRoot", `C:\Windows`)
	cmd, err = winBatchCommand(`C:\Apps\Game.bat`)
	if err != nil {
		t.Fatal(err)
	}
	fallback := `C:\Windows\System32\cmd.exe`
	if !strings.EqualFold(cmd.Path, fallback) || !strings.HasPrefix(cmd.SysProcAttr.CmdLine, `"`+fallback+`" /d `) {
		t.Errorf("with a bad ComSpec: Path %q, CmdLine %s", cmd.Path, cmd.SysProcAttr.CmdLine)
	}

	if _, err := winBatchCommand(`C:\Apps\Game.bat`, `D:\Roms\100%.sfc`); !errors.Is(err, errWinBatchUnsafe) {
		t.Errorf("a %% in an argument: err = %v, want errWinBatchUnsafe", err)
	}
}

// A game whose launcher is a .bat starts through cmd.exe with the ROM path
// quoted, from the launcher's folder; one whose name cmd.exe cannot take
// literally is refused with a message.
func TestGameLaunchExecQuotesBatchLaunchers(t *testing.T) {
	t.Setenv("ComSpec", `C:\Windows\system32\cmd.exe`)
	launcher := `C:\Emus\SNES\launch.bat`
	name, args := gameLaunchCommandArgs(launcher, `D:\Roms\Mario & Luigi (USA).sfc`, []string{"--fast"})
	cmd, err := gameLaunchExec(launcher, name, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cmd.SysProcAttr.CmdLine, `/c ""C:\Emus\SNES\launch.bat" "D:\Roms\Mario & Luigi (USA).sfc" "--fast""`) {
		t.Errorf("CmdLine = %s", cmd.SysProcAttr.CmdLine)
	}
	name, args = gameLaunchCommandArgs(launcher, `D:\Roms\100%.sfc`, nil)
	if _, err := gameLaunchExec(launcher, name, args); err == nil || !strings.Contains(err.Error(), "launch.bat") {
		t.Errorf("a %% in the ROM name: err = %v, want a refusal naming the launcher", err)
	}
}
