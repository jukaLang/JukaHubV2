package main

// logsfolder.go — where JukaHub's logs live, and the one press that opens
// them.
//
// On Windows the log folder is %LOCALAPPDATA%\JukaHub\logs: errors.txt (the
// warnings and errors of the session, logfile.go) and the crash reports from
// crashreport.go. Before this, the only way to reach it was to know the path.
// Settings → Help → "Open logs folder" opens it in Explorer, and Report a bug
// and the diagnostics row name it, so a user asked for "the logs" can find
// them.

import (
	"errors"
	"fmt"
	"os/exec"
)

// logsFolderSummary is the folder the logs are in, or where to find them when
// this install keeps no separate folder.
func logsFolderSummary() string {
	if dir, err := crashLogDir(); err == nil && dir != "" {
		return dir
	}
	if IsTrimuiDevice() {
		return "errors.txt next to JukaHub"
	}
	return "the app's log folder"
}

// logsFolderDetail is the row's second line: what is in the folder.
func logsFolderDetail(*Config) string {
	return "errors.txt and crash reports · " + logsFolderSummary()
}

// openLogsFolder opens the log folder in the desktop's file manager. When
// there is none (the handheld), the toast names the folder instead of failing
// silently.
func openLogsFolder() {
	dir, err := crashLogDir()
	if err != nil || dir == "" {
		ShowToast("The logs folder is not ready yet", ToastKindWarning)
		return
	}
	if err := openFolderAt(dir); err != nil {
		ShowToast("Logs: "+dir, ToastKindInfo)
		return
	}
	ShowToast("Opened "+dir, ToastKindSuccess)
}

// openFolderAt starts the platform's file manager on dir. It is separate from
// openInExplorer (favorites.go), which opens a file's folder: here the folder
// itself is the target, and the handheld has no file manager to start.
func openFolderAt(dir string) error {
	if dir == "" {
		return errors.New("no folder to open")
	}
	var cmd *exec.Cmd
	switch {
	case IsWindows():
		cmd = hiddenCommand("explorer.exe", dir)
	case IsTrimuiDevice():
		return errors.New("no file manager on this device")
	default:
		opener, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("no file manager")
		}
		cmd = hiddenCommand(opener, dir)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the file manager: %w", err)
	}
	// Reap the shell process; a zombie would stay on the handheld's Linux
	// until JukaHub exits.
	go func() { _ = cmd.Wait() }()
	return nil
}

// bugrepLogToggleDetail names the file the recent log comes from, so a report's
// "include the log" switch says where the rest of the story is.
func bugrepLogToggleDetail() string {
	const base = "The last few hundred lines, with credentials and personal details removed"
	if path := logFilePath(); path != "" && IsWindows() {
		return base + " · " + path
	}
	return base
}
