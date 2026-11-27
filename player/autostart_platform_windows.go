//go:build windows

package main

// autostart_platform_windows.go — Windows start-at-login without a launcher
// file. A .cmd in the Startup folder needed a console for a moment; a value
// under HKCU\...\Run starts JukaHub.exe itself, and the GUI-subsystem build
// opens no console at all. The old .cmd is still removed when the setting is
// changed, so an upgrade leaves one start entry, not two.

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// windowsAutoStartCommand is the command line Windows runs at logon.
func windowsAutoStartCommand(exe string) string {
	return quoteWindowsArg(exe) + " --boot"
}

// setPlatformAutoStart handles the Windows Run key. It reports handled=false on
// other platforms.
func setPlatformAutoStart(on bool) (handled bool, err error) {
	legacy, legacyErr := autoStartLauncherPath()
	if on {
		if legacyErr != nil {
			return true, legacyErr
		}
		exe, err := autostartExeFn()
		if err != nil {
			return true, err
		}
		if strings.TrimSpace(exe) == "" {
			return true, errors.New("cannot locate the JukaHub executable")
		}
		// Remove the old launcher first: two entries would start JukaHub
		// twice, and the .cmd is the one that flashes a console.
		if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true, err
		}
		key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartWindowsRunKey,
			registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			return true, err
		}
		defer key.Close()
		if err := key.SetStringValue(autostartWindowsRunValue, windowsAutoStartCommand(exe)); err != nil {
			return true, err
		}
		return true, nil
	}
	var errs []error
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartWindowsRunKey, registry.SET_VALUE)
	if err == nil {
		if err := key.DeleteValue(autostartWindowsRunValue); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		key.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if legacyErr != nil {
		errs = append(errs, legacyErr)
	} else if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return true, errors.Join(errs...)
}

// platformAutoStartInstalled reports whether the Run value would start
// JukaHub at logon.
func platformAutoStartInstalled() (handled, installed bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartWindowsRunKey, registry.QUERY_VALUE)
	if err == nil {
		value, _, valueErr := key.GetStringValue(autostartWindowsRunValue)
		key.Close()
		if valueErr == nil && strings.TrimSpace(value) != "" {
			return true, true
		}
	}
	return true, false
}

// platformAutoStartStale reports an old Startup-folder launcher that an
// upgrade left behind. reconcileAutoStart replaces or removes it.
func platformAutoStartStale() (handled, stale bool) {
	path, err := autoStartLauncherPath()
	if err != nil {
		return true, false
	}
	_, statErr := os.Stat(path)
	return true, statErr == nil
}

// platformAutoStartCommand is what the Scheduled tasks screen shows for
// JukaHub's own entry: the Run value and its location, or the command a fresh
// enable would write.
func platformAutoStartCommand() (command, source string, handled bool) {
	source = `HKCU\` + autostartWindowsRunKey
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartWindowsRunKey, registry.QUERY_VALUE)
	if err == nil {
		value, _, valueErr := key.GetStringValue(autostartWindowsRunValue)
		key.Close()
		if valueErr == nil && strings.TrimSpace(value) != "" {
			return value, source, true
		}
	}
	exe, exeErr := autostartExeFn()
	if exeErr != nil || strings.TrimSpace(exe) == "" {
		return "JukaHub --boot", source, true
	}
	return windowsAutoStartCommand(exe), source, true
}
