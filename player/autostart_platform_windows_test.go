//go:build windows

package main

import "testing"

func TestWindowsAutoStartCommand(t *testing.T) {
	got := windowsAutoStartCommand(`C:\Program Files\JukaHub\JukaHub.exe`)
	want := `"C:\Program Files\JukaHub\JukaHub.exe" --boot`
	if got != want {
		t.Fatalf("windowsAutoStartCommand = %q, want %q", got, want)
	}
}
