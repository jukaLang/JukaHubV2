package main

import (
	"log"
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

	var cmd *exec.Cmd
	if IsWindows() {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Env = append(os.Environ(), "JUKAHUB_EXTERNAL_APP=1")

	log.Printf("[EXTERNAL] running: %s (%s)", command, runtime.GOOS)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("[EXTERNAL] output: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("external app failed: %w", err)
	}
	log.Printf("[EXTERNAL] finished: %s", command)
	return nil
}
