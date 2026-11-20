//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
)

// The update and rollback both use this command. A path with cmd.exe syntax
// must reach it as one quoted file name, without starting a real update.
func TestSwapHelperCommandQuotesWindowsPath(t *testing.T) {
	t.Setenv("ComSpec", testComspec)
	script := `C:\Juka Hub (beta)&friends\apply-update.bat`
	cmd, err := swapHelperCommand(script)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cmd.Path, testComspec) {
		t.Errorf("Path = %q, want %q", cmd.Path, testComspec)
	}
	want := `"` + testComspec + `" /d /v:off /s /c ""` + script + `""`
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("CmdLine = %+v\nwant %s", cmd.SysProcAttr, want)
	}
	if _, err := swapHelperCommand(`C:\Juka%TEMP%\apply-update.bat`); !errors.Is(err, errWinBatchUnsafe) {
		t.Errorf("unsafe path: err = %v, want errWinBatchUnsafe", err)
	}
}
