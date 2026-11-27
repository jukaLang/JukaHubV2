//go:build !windows

package main

// setPlatformAutoStart is Windows-only; the desktop .desktop file and the
// TrimUI boot hook are written by autostart.go.
func setPlatformAutoStart(on bool) (handled bool, err error) {
	return false, nil
}

// platformAutoStartInstalled leaves the check to the launcher file.
func platformAutoStartInstalled() (handled, installed bool) {
	return false, false
}

// platformAutoStartStale is Windows-only; other platforms have no old
// launcher format to migrate.
func platformAutoStartStale() (handled, stale bool) {
	return false, false
}

// platformAutoStartCommand leaves the command to the launcher path.
func platformAutoStartCommand() (command, source string, handled bool) {
	return "", "", false
}
