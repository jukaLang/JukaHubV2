package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLaunchFailureSuggestion(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing launcher", exec.ErrNotFound, "missing"},
		{"permission denied", os.ErrPermission, "permissions"},
		{"other failure", errTestLaunch, "required BIOS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchFailureSuggestion(tc.err); !strings.Contains(strings.ToLower(got), strings.ToLower(tc.want)) {
				t.Fatalf("suggestion = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

type launchTestError string

func (e launchTestError) Error() string { return string(e) }

var errTestLaunch error = launchTestError("unexpected emulator exit")
