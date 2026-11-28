//go:build !windows

package main

// steam_other.go — the Steam app on a Linux desktop or a Steam Deck. A TrimUI
// handheld has none: steamRoot finds nothing there, and a game press goes to
// a paired PC instead (steam.go).
//
// Linux has Steam in several packages, each with its own folder and its own
// way in. The native one (the distribution's package, Valve's .deb, SteamOS)
// keeps ~/.local/share/Steam (~/.steam/steam links there) and puts `steam` on
// PATH; the Flatpak lives in ~/.var/app/com.valvesoftware.Steam and starts
// through `flatpak run`; the Snap keeps ~/snap/steam/common and starts through
// `snap run steam`. A native folder with no `steam` on PATH (a hand-made
// install) has its own steam.sh, and the desktop's link handler (xdg-open) is
// the last way, when a steam.desktop registers Steam for steam:// links.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// steamFlatpakID is the Flatpak application id of Steam (Flathub).
const steamFlatpakID = "com.valvesoftware.Steam"

// steamRootDirs lists where the Steam app keeps its files on Linux: the
// native install, the Flatpak one and the Snap one.
func steamRootDirs() []string {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".var", "app", steamFlatpakID, ".local", "share", "Steam"),
		filepath.Join(home, "snap", "steam", "common", ".local", "share", "Steam"),
		filepath.Join(home, "snap", "steam", "common", ".steam", "steam"),
	}
}

// steamLauncher is the command that hands Steam a link: the program and the
// arguments that go before the link. "" when no Steam app here can start
// games (always on TrimUI: there is no Steam folder).
func steamLauncher() (string, []string) {
	root := steamRoot()
	if root == "" {
		return "", nil
	}
	if p, err := exec.LookPath("steam"); err == nil {
		return p, nil
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home != "" {
		if dirExists(filepath.Join(home, ".var", "app", steamFlatpakID)) {
			if p, err := exec.LookPath("flatpak"); err == nil {
				return p, []string{"run", steamFlatpakID}
			}
		}
		if dirExists(filepath.Join(home, "snap", "steam")) {
			if p, err := exec.LookPath("snap"); err == nil {
				return p, []string{"run", "steam"}
			}
			if steamExecutable("/snap/bin/steam") {
				return "/snap/bin/steam", nil
			}
		}
	}
	// A sandboxed package's own steam.sh cannot run outside its sandbox.
	sandboxed := home != "" && (isPathWithin(root, filepath.Join(home, ".var", "app")) || isPathWithin(root, filepath.Join(home, "snap")))
	if sh := filepath.Join(root, "steam.sh"); !sandboxed && steamExecutable(sh) {
		return sh, nil
	}
	if steamLinkHandlerRegistered(home) {
		if p, err := exec.LookPath("xdg-open"); err == nil {
			return p, nil
		}
	}
	return "", nil
}

// steamExecutable reports whether path is a regular file anyone may run.
func steamExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// steamLinkHandlerRegistered reports whether a desktop entry of Steam (native,
// Flatpak or Snap) is installed, so xdg-open has a program for steam:// links.
func steamLinkHandlerRegistered(home string) bool {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share"),
			filepath.Join(home, ".local", "share", "flatpak", "exports", "share"))
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_DIRS"))
	if data == "" {
		data = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, filepath.SplitList(data)...)
	dirs = append(dirs, "/var/lib/flatpak/exports/share", "/var/lib/snapd/desktop")
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range []string{"steam.desktop", steamFlatpakID + ".desktop", "steam_steam.desktop"} {
			if regularFile(filepath.Join(dir, "applications", name)) {
				return true
			}
		}
	}
	return false
}

// steamClientBinary is the program steamOpenURI runs, or "" (always on TrimUI).
func steamClientBinary() string {
	bin, _ := steamLauncher()
	return bin
}

// steamOpenURI passes a steam:// link to Steam as one argument, after the
// launcher's own (flatpak run com.valvesoftware.Steam, snap run steam); no
// shell parses it, and steamRunURI only ever builds steam://rungameid/<n>,
// steam://install/<n> or steam://uninstall/<n>.
func steamOpenURI(uri string) error {
	bin, args := steamLauncher()
	if bin == "" {
		return errSteamNoClient
	}
	cmd := hiddenCommand(bin, append(args, uri)...)
	// Steam's runtime minds the library path: it gets the session's own
	// environment back, not the one JukaHub runs with (childenv.go), and its
	// own session, so a Steam this call started keeps running when JukaHub
	// quits.
	cmd.Env = screenChildEnvFor(os.Environ(), childEnvAppDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
