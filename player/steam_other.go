//go:build !windows

package main

// steam_other.go — the Steam app on a Linux desktop. A TrimUI handheld has
// none: steamRoot finds nothing there, and a game press goes to a paired PC
// instead (steam.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// steamRootDirs lists where the Steam app keeps its files on Linux: the
// native install and the Flatpak one.
func steamRootDirs() []string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}
}

// steamClientBinary is the steam launcher on PATH, or "" (always on TrimUI).
func steamClientBinary() string {
	if steamRoot() == "" {
		return ""
	}
	path, err := exec.LookPath("steam")
	if err != nil {
		return ""
	}
	return path
}

// steamOpenURI passes a steam:// link to the steam launcher as one argument;
// steamRunURI only ever builds steam://rungameid/<n> or steam://install/<n>.
func steamOpenURI(uri string) error {
	bin := steamClientBinary()
	if bin == "" {
		return errSteamNoClient
	}
	cmd := exec.Command(bin, uri)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
