//go:build windows

package main

// steam_windows.go — where the Steam app lives on Windows, and how a steam://
// link reaches it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// steamRootDirs lists the folders the Steam app may be installed in: the one
// Steam records in the registry first, then the default install folders.
func steamRootDirs() []string {
	var dirs []string
	read := func(root registry.Key, path, name string) {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue(name); err == nil && strings.TrimSpace(v) != "" {
			dirs = append(dirs, filepath.Clean(filepath.FromSlash(strings.TrimSpace(v))))
		}
	}
	read(registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath")
	read(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath")
	read(registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath")
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			dirs = append(dirs, filepath.Join(v, "Steam"))
		}
	}
	return dirs
}

// steamClientBinary is steam.exe, or "" when the Steam app is not installed.
func steamClientBinary() string {
	root := steamRoot()
	if root == "" {
		return ""
	}
	exe := filepath.Join(root, "steam.exe")
	if info, err := os.Stat(exe); err == nil && info.Mode().IsRegular() {
		return exe
	}
	return ""
}

// steamOpenURI hands a steam:// link to the protocol handler Steam registered.
// rundll32 gets the link as one argument: no shell ever parses it, and
// steamRunURI only ever builds steam://rungameid/<n> or steam://install/<n>.
func steamOpenURI(uri string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", uri)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
