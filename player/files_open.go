package main

// files_open.go — what A does on a file in Files, for everything that has no
// dedicated branch in feEnterFocused (features.go).
//
// The rule is the one desktop file managers follow: a file with a viewer of its
// own (pictures, music and video, PDF/EPUB, a game, a script, an archive) opens
// there, and everything else opens in the built-in text editor — or, when the
// bytes are not text, in its read-only hex view with a warning instead of a
// screen of garbage. Before this, any extension the editor did not list
// answered "No handler for …", which on a handheld with no other app meant the
// file could not be looked at at all.

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// filesROMExtSystem maps the extensions only one console uses onto the
// romSystemAliases key for that console. Shared container formats (.zip, .7z,
// .bin, .iso, .cue, .chd …) are deliberately absent: for those only the console
// folder the file sits in can say which system it belongs to.
var filesROMExtSystem = map[string]string{
	".nes": "nes",
	".fc":  "nes",
	".sfc": "sfc",
	".smc": "sfc",
	".gb":  "gb",
	".gbc": "gbc",
	".gba": "gba",
	".gen": "md",
	".n64": "n64",
	".z64": "n64",
	".v64": "n64",
	".nds": "nds",
}

// filesROMSystem names the console a file is a game for, or "" when it is not
// one. The console folder it sits in (Roms/GBA, Roms/PS/Game/disc.cue) is the
// strongest sign; an extension only one system uses is the other. It reads no
// disk, because the Files rows ask it for their icon on every frame.
func filesROMSystem(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if !romFileExtensions[ext] {
		if _, unique := filesROMExtSystem[ext]; !unique {
			return ""
		}
	}
	dir := filepath.Dir(path)
	for i := 0; i < 2; i++ {
		if system, ok := romSystemAliases[strings.ToLower(filepath.Base(dir))]; ok {
			// ".md" is also Markdown: a README beside the games is a note, and
			// only a Mega Drive folder makes the extension a cartridge.
			if ext == ".md" && system.Name != "Mega Drive" {
				return ""
			}
			return system.Name
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if key, ok := filesROMExtSystem[ext]; ok {
		if system, ok := romSystemAliases[key]; ok {
			return system.Name
		}
	}
	return ""
}

// filesOpenROM starts a game with the emulator the library would pick for its
// console. It reports whether the file was a game at all: a game with no
// emulator installed is still claimed (and explained), so it is never shown as
// a hex dump instead.
func filesOpenROM(config *Config, entry FileEntry) bool {
	if entry.IsDir || entry.Name == ".." {
		return false
	}
	system := filesROMSystem(entry.Path)
	if system == "" {
		return false
	}
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		showToast("Cannot open "+entry.Name+": "+filesFriendlyError(err), ToastError())
		return true
	}
	game := libraryGame{Path: path, System: system, Title: gameDisplayTitle(path)}
	// Several emulators for this console: ask which (emupicker.go).
	chooseGameLauncher(game, func(launcher string) {
		if strings.TrimSpace(launcher) == "" {
			showToast("No emulator for "+system+" yet — add one in Emulators", ToastWarn())
			return
		}
		if err := launchLibraryGame(game, launcher); err != nil {
			showToast("Cannot start "+game.Title+": "+err.Error(), ToastError())
			return
		}
		recordPlayed(config, path)
		PlayActivateSound()
		showToast("Starting "+game.Title, ToastSuccess())
	})
	return true
}

// filesHasDedicatedViewer reports whether A on the file opens something other
// than the text editor.
func filesHasDedicatedViewer(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case isMusicPlaylistFile(path):
		return true
	case isImageExtension(ext), isAudioFile(path), isMediaFile(path), hasMediaExtension(path):
		return true
	case ext == ".pdf" || ext == ".epub" || ext == ".sh" || ext == ".html" || ext == ".js":
		return true
	case feArchiveKindOf(path) != feArchiveNone:
		// Archives open their sheet (files_extract_ui.go).
		return true
	case filesROMSystem(path) != "":
		return true
	}
	return false
}

// filesOpenDefault opens a file with no viewer of its own: text in the editor
// (read-only and paged when it is large), anything else in the hex view.
func filesOpenDefault(config *Config, entry FileEntry) {
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		showToast("Cannot open "+entry.Name+": "+filesFriendlyError(err), ToastError())
		return
	}
	filesViewerOpen(path)
}

// filesSystemOpenAvailable reports whether "Open with Windows" is offered: the
// handheld has no other app to hand a file to.
func filesSystemOpenAvailable() bool {
	return IsWindows()
}

// filesOpenWithSystem hands a file to the program Windows associates with it
// (a PDF reader, a spreadsheet, the Photos app). rundll32's protocol handler
// is what the launcher already uses for shortcuts: no console window flashes
// up, unlike cmd /c start.
func filesOpenWithSystem(path string) bool {
	if !filesSystemOpenAvailable() {
		showToast("Opening files in other apps works on Windows", ToastInfo())
		return false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	if err := cmd.Start(); err != nil {
		showToast("Windows could not open "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
		return false
	}
	// Reaped in the background so it never lingers as a zombie.
	go func() { _ = cmd.Wait() }()
	PlayActivateSound()
	showToast("Opened "+filepath.Base(path)+" with Windows", ToastSuccess())
	return true
}
