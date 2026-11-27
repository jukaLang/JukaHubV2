package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The update handshake is a comment line in launch.sh plus marker files
// (patch_swap.go). Reading the wrong one costs a user two UIs drawing over each
// other: the launcher that does not know the protocol hands the screen back to
// MainUI while the new build starts; the one that does waits for a verdict.
func TestRestartHandshakeMarkers(t *testing.T) {
	cases := []struct {
		name     string
		launcher string // "" leaves no launch.sh in the install folder
		started  string // JUKAHUB_LAUNCHED_BY
		want     bool
	}{
		{"protocol launcher that started JukaHub", "#!/bin/sh\n# jukahub-restart-protocol: 1\n", "launch.sh", true},
		{"protocol launcher, started by hand", "#!/bin/sh\n# jukahub-restart-protocol: 1\n", "", false},
		{"launcher older than the protocol", "#!/bin/sh\n", "launch.sh", false},
		{"no launcher at all (desktop)", "", "launch.sh", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("JUKAHUB_LAUNCHED_BY", tc.started)
			if tc.launcher != "" {
				if err := os.WriteFile(filepath.Join(dir, "launch.sh"), []byte(tc.launcher), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			marker := prepareLauncherRestart(dir)
			if got := marker != ""; got != tc.want {
				t.Fatalf("prepareLauncherRestart = %q, want marker: %v", marker, tc.want)
			}
			leftover := filepath.Join(dir, restartPendingName)
			if !tc.want {
				if _, err := os.Stat(leftover); !os.IsNotExist(err) {
					t.Fatalf("a marker was written without a launcher to honour it: %v", err)
				}
				return
			}
			if filepath.Base(marker) != restartPendingName {
				t.Errorf("marker = %q, want the %s name the scripts read", marker, restartPendingName)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("the returned marker does not exist: %v", err)
			}
		})
	}
}

// A killed run leaves markers behind. Left there, they make the next update's
// helper skip its relaunch and wait for a launcher loop that is not running.
func TestClearStaleRestartMarkers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{restartPendingName, restartSwappedName, restartFailedName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "JukaHub")
	if err := os.WriteFile(keep, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	clearStaleRestartMarkersIn(dir)
	for _, name := range []string{restartPendingName, restartSwappedName, restartFailedName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the cleanup", name)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the cleanup removed a file that is not a marker: %v", err)
	}
	// Nothing to clear is not an error: every ordinary start calls this.
	clearStaleRestartMarkersIn(t.TempDir())
}
