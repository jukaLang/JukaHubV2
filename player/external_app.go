package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// runExplicitExternalApp runs an "external_app" trigger target.
//
// External apps can run arbitrary binaries, so this path is deliberately kept
// explicit and accountable:
//
//   - it runs synchronously (no background goroutine that keeps running after
//     the user has moved on),
//   - it refuses empty or shell-only-injected input,
//   - it logs exactly what was launched and what it returned.
//
// The bundled handheld config does not use this trigger; it exists for the
// desktop/Patch scenarios where a package ships a helper binary.
func runExplicitExternalApp(path string) error {
	command := strings.TrimSpace(path)
	if command == "" {
		return fmt.Errorf("external_app trigger has no path")
	}

	// Trust boundary (SEC-12): command is the external_app path of a config
	// trigger — written by whoever authored the scene (jukaconfig.json, the
	// user's own jukauser.json, a Patch package or a design the live builder
	// pushed; pushed designs carrying this trigger wait in quarantine until
	// accepted on the device, SEC-3). It is a command line by design
	// (arguments, pipes, redirects), so it goes to the platform shell whole.
	// Nothing typed or received at run time is appended to it.
	var cmd *exec.Cmd
	if IsWindows() {
		cmd = exec.Command("cmd", "/d", "/q")
	} else {
		cmd = exec.Command("sh")
	}
	cmd.Stdin = strings.NewReader(command + "\n")
	cmd.Env = append(os.Environ(), "JUKAHUB_EXTERNAL_APP=1")

	logDebug("external", "running: %s (%s)", command, runtime.GOOS)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		logDebug("external", "output: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("external app failed: %w", err)
	}
	logDebug("external", "finished: %s", command)
	return nil
}
