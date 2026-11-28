package main

// openwith_args.go — "Open with JukaHub" from a desktop file association or
// shortcut: this binary can start with a scene name and/or a file to open.
// The parsing is pure so the odd forms a shell produces can be tested without
// a display. The Open with sheet itself lives in openwith.go / openwith_ui.go.

import (
	"fmt"
	"strings"
)

// openWithFile is set before SDL starts and cleared when the UI is ready.
var openWithFile string

// parseOpenWithArgs reads --scene NAME, --scene=NAME and one file argument.
// Unknown flags are left to the app's other command handlers; only `--scene`
// belongs here.
func parseOpenWithArgs(args []string) (scene, file string, err error) {
	skipNext := false
	for i := 0; i < len(args); i++ {
		if skipNext {
			skipNext = false
			continue
		}
		a := strings.TrimSpace(args[i])
		switch {
		case a == "--scene":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", "", fmt.Errorf("--scene needs a scene name")
			}
			scene = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(a, "--scene="):
			scene = strings.TrimSpace(strings.TrimPrefix(a, "--scene="))
			if scene == "" {
				return "", "", fmt.Errorf("--scene needs a scene name")
			}
		case strings.HasPrefix(a, "-") || a == "":
			// Another app's flag (and possibly its value), or an empty
			// argument: never mistake it for the file to open.
			if strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
				skipNext = true
			}
		case file == "":
			file = a
		}
	}
	return scene, file, nil
}

// applyOpenWithFile hands the file to the existing opener on the main thread.
func applyOpenWithFile(config *Config) {
	file := strings.TrimSpace(openWithFile)
	openWithFile = ""
	if file == "" {
		return
	}
	tbxPost(func() {
		if strings.HasPrefix(strings.ToLower(file), "http://") || strings.HasPrefix(strings.ToLower(file), "https://") {
			playSmartURL(config, file)
			return
		}
		filesOpenWithSystem(file)
	})
}
