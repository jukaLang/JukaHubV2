package main

// autostart.go implements the "Start JukaHub at login" setting.
//
// The setting is off by default and never changes anything on disk until the
// user turns it on: enabling it writes a launcher into the platform's standard
// startup location, disabling it removes that launcher again. On the handheld
// (TrimUI) the OS launcher owns autostart, so the setting reports that instead
// of pretending to work.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Autostart file names. One launcher per platform, named so the app can find
// and remove exactly its own entry and nothing else.
const (
	autostartWindowsFile = "JukaHub.cmd"
	autostartLinuxFile   = "jukahub.desktop"
)

// Overridable for tests: where the launcher lives and which executable it runs.
var (
	autostartDirFn = defaultAutostartDir
	autostartExeFn = func() (string, error) { return os.Executable() }
)

// defaultAutostartDir returns the platform's per-user startup directory.
func defaultAutostartDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), nil
	case "linux", "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "autostart"), nil
	}
	return "", fmt.Errorf("autostart is not supported on %s", runtime.GOOS)
}

// autostartSupported reports whether this platform has a startup location the
// app can write to. The handheld is excluded: its launcher is part of the
// firmware's app list, not something JukaHub may edit.
func autostartSupported() bool {
	if IsTrimuiDevice() {
		return false
	}
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
		return true
	}
	return false
}

// autoStartEnabled reads the persisted setting (default off).
func autoStartEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "autoStart"); ok {
		return coerceBool(v)
	}
	return false
}

// autoStartLauncherPath returns the launcher file for this platform.
func autoStartLauncherPath() (string, error) {
	return autoStartLauncherPathFor(runtime.GOOS)
}

// autoStartLauncherPathFor is the platform-parameterised form, so both formats
// stay testable from any host.
func autoStartLauncherPathFor(goos string) (string, error) {
	dir, err := autostartDirFn()
	if err != nil {
		return "", err
	}
	name := autostartWindowsFile
	if goos != "windows" {
		name = autostartLinuxFile
	}
	return filepath.Join(dir, name), nil
}

// autoStartLauncher writes what the platform starts at login.
func autoStartLauncher(exe string) string {
	return autoStartLauncherFor(runtime.GOOS, exe)
}

// autoStartLauncherFor builds the launcher for a named OS.
func autoStartLauncherFor(goos, exe string) string {
	if goos == "windows" {
		// `start "" "<exe>"` keeps the console window from sticking around.
		return "@echo off\r\nstart \"\" " + quoteWindowsArg(exe) + "\r\n"
	}
	// Freedesktop autostart entry (GNOME/KDE/XFCE and the TrimUI desktop all
	// honour this file).
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=JukaHub\n" +
		"Comment=Launch JukaHub at login\n" +
		"Exec=" + quoteDesktopExecArg(exe) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// quoteDesktopExecArg quotes a path for the Exec key of a .desktop file. The
// freedesktop spec understands double quotes only (a single-quoted argument is
// not shell-parsed), and inside them " ` $ \ have to be escaped. A literal %
// must be doubled, otherwise it is read as a field code.
func quoteDesktopExecArg(p string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setAutoStart installs or removes the startup launcher. It reports a friendly
// error the settings toggle can show without leaving the switch in the wrong
// state.
func setAutoStart(on bool) error {
	if !autostartSupported() {
		return fmt.Errorf("this device manages startup apps itself")
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := autostartExeFn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(exe) == "" {
		return fmt.Errorf("cannot locate the JukaHub executable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(autoStartLauncher(exe)), 0o644)
}

// autoStartInstalled reports whether the launcher exists on disk, which is the
// ground truth the setting is reconciled against at startup.
func autoStartInstalled() bool {
	path, err := autoStartLauncherPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// reconcileAutoStart applies the saved setting on startup: if the user enabled
// it and the launcher is missing (new install, moved SD card), it is written
// again; if the user disabled it, a leftover launcher is removed.
func reconcileAutoStart(config *Config) {
	if !autostartSupported() {
		return
	}
	want := autoStartEnabled(config)
	if want == autoStartInstalled() {
		return
	}
	if err := setAutoStart(want); err != nil {
		logPatch("autostart: could not reconcile (%v)", err)
	}
}
