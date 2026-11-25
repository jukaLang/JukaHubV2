package main

// emu_azahar.go — Nintendo 3DS games on a Windows PC, played by Azahar.
//
// Azahar is the maintained 3DS emulator: the Citra forks (PabloMK7's Citra,
// Lime3DS) merged into it in 2024–2025, and Citra itself stopped in 2024. It
// ships for Windows on GitHub (azahar-emu/azahar) as an installer
// (azahar-windows-msvc-<version>-installer.exe, also msys2 and mxe builds) and
// as a portable .zip; both hold azahar.exe. The installer puts it in
// C:\Program Files\Azahar (all users) or %LOCALAPPDATA%\Programs\Azahar (one
// user, "Azahar (User)" in the Uninstall keys). JukaHub installs the portable
// zip like the other Windows emulators (emuAzaharWinSpec), or finds the
// player's own copy, and starts games in it:
//
//   - Installing, updating and rolling back are emuwininstall.go's, from the
//     rule below: the newest stable release's MSVC zip, laid into
//     Emulators\Azahar with a manifest, so a newer release shows as an update
//     and R1 puts the one before back, as for Xenia Canary.
//   - Finding it is the Windows emulator catalog's job (emulators.go): this
//     file adds an "Azahar" row whose launcher is azahar.exe, so the catalog's
//     usual probes cover JukaHub's own Emulators\Azahar folder, Program Files,
//     %LOCALAPPDATA%\Programs, PATH, a portable copy in a folder named Azahar
//     on the Desktop, in Downloads, Documents, Games or Emulators, and the
//     folder any Uninstall key named "Azahar…" records (emuwinregistry).
//   - The library: Roms/3DS (also N3DS, "Nintendo 3DS" and the No-Intro
//     name) is the "Nintendo 3DS" shelf — the name emusystems.go's citra core
//     row and the emu-3ds icon already use — and its games find Azahar
//     through the launcher aliases. The shelf lists what Azahar opens
//     straight from a file: .3ds/.cci, .cxi/.app, .3dsx, .elf/.axf and their
//     compressed z-forms. A .cia is not a game file but an installer (Azahar's
//     File → Install CIA puts it in the emulated console's storage), so it is
//     not listed.
//   - On a handheld the shelf says "Not supported" with the reason
//     (emuDesktopSystems): no TrimUI chip emulates a 3DS at playable speed.
//   - Its command line is "azahar.exe [options] <file>"; -f starts the game
//     full screen (emuAzaharGameArgs, used by emuStandaloneGameArgs). The
//     short flag on purpose: builds 2126.x read the long --fullscreen as a
//     run of short options and quit (azahar-emu/azahar#2548), while -f is the
//     one Azahar's own team points launchers at.
//
// Lime3DS and Citra are not catalog rows of their own: both are
// discontinued, Azahar reads their user folders, and two more "Not
// installed" tiles on every PC would only point at dead projects.
//
// The tables are package variables other files own (romsetup.go,
// game_library.go, emulators.go, emuwininstall.go); their rows are added from
// init(), before anything reads them, and never over a row another file
// already gives the same key.

import (
	"path/filepath"
	"strings"
)

const (
	// emu3DSSystem is the Games library's name for the console.
	emu3DSSystem = "Nintendo 3DS"
	// emuAzaharName is Azahar's catalog row and install folder name.
	emuAzaharName = "Azahar"
	// emuAzaharSite is where Azahar's Windows builds are published.
	emuAzaharSite = "https://github.com/azahar-emu/azahar/releases"
)

// emuAzaharWinSpec is Azahar's Windows install rule. The portable build's name
// changed at 2125.0: azahar-windows-msvc-<version>.zip since then,
// azahar-<version>-windows-msvc.zip up to 2124.3, both one folder holding
// azahar.exe. A release also carries the installers (.exe), the msys2 and mxe
// builds, the libretro core (azahar-libretro-windows-x86_64-<version>.zip) and
// the multiplayer room server, none of them the program a game starts in.
// There is no official Windows on ARM build: Windows 11 on ARM runs the x64
// one. Tags are plain release numbers ("2124", "2125.0.1", "2126.2"), which
// emuWinUpdateAvailable orders as versions.
var emuAzaharWinSpec = emuWinSpec{
	Name:     emuAzaharName,
	Repo:     "azahar-emu/azahar",
	Match:    [][]string{{"azahar", "windows", "msvc", ".zip"}},
	Exclude:  []string{"installer", "msys2", "mxe", "room", "libretro"},
	MaxBytes: 256 << 20,
	Site:     emuAzaharSite,
}

// emu3DSFolders are the Roms folder names the 3DS shelf is found under (the
// same names emuDesktopSystems lists for it).
var emu3DSFolders = []string{"3ds", "n3ds", "nintendo 3ds", "nintendo - nintendo 3ds"}

// emu3DSExtensions are the files Azahar opens directly.
var emu3DSExtensions = map[string]bool{
	".3ds": true, ".cci": true, ".zcci": true,
	".cxi": true, ".app": true, ".zcxi": true,
	".3dsx": true, ".z3dsx": true,
	".elf": true, ".axf": true,
}

func init() {
	for _, folder := range emu3DSFolders {
		if _, taken := romSystemAliases[folder]; !taken {
			romSystemAliases[folder] = romSystem{Name: emu3DSSystem, Collection: "Nintendo - Nintendo 3DS"}
		}
	}
	if _, taken := systemLauncherAliases[emu3DSSystem]; !taken {
		systemLauncherAliases[emu3DSSystem] = []string{"3ds", "azahar", "lime3ds", "citra"}
	}
	romShelfRegister(emu3DSSystem, func(_, _, ext string) bool { return emu3DSExtensions[ext] })
	if !emuAzaharCataloged() {
		winEmuCatalog = append(winEmuCatalog, winEmuSpec{emuAzaharName, []string{"azahar.exe"}})
	}
	if _, ok := emuWinSpecFor(emuAzaharName); !ok {
		emuWinSpecs = append(emuWinSpecs, emuAzaharWinSpec)
	}
}

// emuAzaharCataloged reports an Azahar row already in the Windows catalog.
func emuAzaharCataloged() bool {
	for _, spec := range winEmuCatalog {
		if strings.EqualFold(spec.name, emuAzaharName) {
			return true
		}
	}
	return false
}

// emuIsAzaharLauncher reports Azahar's own program.
func emuIsAzaharLauncher(launcher string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(launcher)))
	return base == "azahar.exe" || base == "azahar"
}

// emuAzaharGameArgs is Azahar's command line for a game: full screen, the
// file last.
func emuAzaharGameArgs(romPath string) []string {
	return []string{"-f", romPath}
}
