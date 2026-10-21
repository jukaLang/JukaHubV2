package main

// autostart.go implements the "Start JukaHub at login" setting.
//
// The setting is off by default and never changes anything on disk until the
// user turns it on: enabling it writes a launcher into the platform's standard
// startup location, disabling it removes that launcher again. Only JukaHub's own
// file is ever written or removed.
//
// Two kinds of hook are supported:
//   - Desktop: a freedesktop .desktop file (or a .cmd on Windows) in the
//     per-user autostart folder.
//   - TrimUI: an SD-card boot script in System/starts, which the firmware's
//     runtrimui.sh runs at boot. That directory is the community-documented
//     hook for stock and CrossMix firmware; the app creates it when missing and
//     never touches any other script in it.

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
	// trimuiAutostartFile lives in <SD>/System/starts. The name marks it as
	// ours so the setting can remove exactly this file later.
	trimuiAutostartFile = "jukahub-autostart.sh"
)

// Overridable for tests: where the launcher lives, which executable it runs,
// whether this is a handheld, and where its SD card is mounted.
var (
	autostartDirFn    = defaultAutostartDir
	autostartExeFn    = func() (string, error) { return os.Executable() }
	autostartIsTrimui = func() bool { return IsTrimuiDevice() }
	trimuiSDRootFn    = defaultTrimuiSDRoot
)

// defaultTrimuiSDRoot finds the SD card the app is installed on. JukaHub lives
// in <SD>/Apps/JukaHub, so the executable path already names the root; the
// environment override exists for a symlinked binary or a test.
func defaultTrimuiSDRoot() string {
	if root := strings.TrimSpace(os.Getenv("JUKAHUB_SD_ROOT")); root != "" {
		return root
	}
	exe, err := autostartExeFn()
	if err != nil {
		return ""
	}
	appDir := filepath.Dir(exe)
	appsDir := filepath.Dir(appDir)
	if !strings.EqualFold(filepath.Base(appsDir), "Apps") {
		return ""
	}
	return filepath.Dir(appsDir)
}

// trimuiAutostartPath is the boot hook's location on the SD card.
func trimuiAutostartPath() (string, error) {
	root := trimuiSDRootFn()
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("cannot find the SD card")
	}
	return filepath.Join(root, "System", "starts", trimuiAutostartFile), nil
}

// trimuiAutostartScript is the hook itself. Everything happens in a background
// subshell and the script returns immediately, so the firmware's boot sequence
// is never blocked on the app: a hung launch must not keep MainUI from coming
// up. The subshell first waits (at most 30 s) for the stock launcher, because
// the boot hook may run before MainUI is on screen; starting earlier either
// races the launcher or grabs a display that is not ready yet. launch.sh then
// stops MainUI and hands the display back when the app exits, so no fixed delay
// is needed here — that is what the old hand-written hooks used sleep 10 for.
func trimuiAutostartScript(appDir string) string {
	return "#!/bin/sh\n" +
		"# Written by JukaHub's \"Start JukaHub at login\" setting.\n" +
		"# Delete this file (or turn the setting off) to stop it launching at boot.\n" +
		"APP_DIR=" + shellQuote(appDir) + "\n" +
		"[ -d \"$APP_DIR\" ] || exit 0\n" +
		"(\n" +
		"  # Wait for the stock launcher, so the display is ready and launch.sh can\n" +
		"  # take it over cleanly. Bounded: with no launcher we start anyway.\n" +
		"  if command -v pidof >/dev/null 2>&1; then\n" +
		"    n=0\n" +
		"    while [ \"$n\" -lt 30 ] && ! pidof MainUI >/dev/null 2>&1; do\n" +
		"      n=$((n + 1))\n" +
		"      sleep 1\n" +
		"    done\n" +
		"  fi\n" +
		"  /bin/sh \"$APP_DIR/launch.sh\" >>\"$APP_DIR/autostart.log\" 2>&1\n" +
		") >/dev/null 2>&1 &\n" +
		"exit 0\n"
}

// shellQuote wraps a path for a POSIX shell. Single quotes are the only form
// every sh understands, and a literal quote is closed and escaped around.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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
// app can write to. On the handheld that is the SD card's boot script folder;
// if the card cannot be located the setting honestly reports itself unusable
// rather than writing somewhere arbitrary.
func autostartSupported() bool {
	if autostartIsTrimui() {
		return strings.TrimSpace(trimuiSDRootFn()) != ""
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
	if autostartIsTrimui() {
		return trimuiAutostartPath()
	}
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
	if autostartIsTrimui() {
		// The boot script has to be executable: runtrimui.sh runs the files in
		// that folder, and some card filesystems mount without the bit.
		if err := os.WriteFile(path, []byte(trimuiAutostartScript(filepath.Dir(exe))), 0o755); err != nil {
			return err
		}
		return os.Chmod(path, 0o755)
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
