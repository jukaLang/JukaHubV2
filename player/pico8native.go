package main

// pico8native.go — the official PICO-8, when the user brings their own.
//
// PICO-8 carts (.p8, .p8.png) normally play in fake-08, a re-implementation
// that runs inside RetroArch or behind the firmware's Emus/PICO script. Owners
// of PICO-8 itself would rather play on the real thing: it runs every cart,
// and it has Splore, the cart browser that reads Lexaloffle's BBS. PICO-8 is
// a paid program, so JukaHub never ships or downloads it. The user copies two
// files from their own Raspberry Pi download, pico8_64 and pico8.dat, into
// Bios/P8 on the card (on a PC: pico8.exe and pico8.dat, or an installed
// PICO-8 is found where its installer put it).
//
// When both files are there:
//
//   - The library offers "PICO-8 (official)" for PICO-8 carts, first in the
//     list and badged Recommended in the Which-emulator sheet. fake-08 and the
//     firmware's launcher stay in the list and can still be picked or pinned
//     (pico8NativeLaunchers, pico8NativeIndex).
//   - A cart starts with -run. Its own folder is PICO-8's root, so a game made
//     of several carts finds the ones it loads. PICO-8's home folder is chosen
//     by JukaHub: on the card Saves/PICO-8, so config.txt and the carts' saves
//     (cdata/) sit with the other saves instead of in the program's folder. A
//     PC's PICO-8 is the user's own install: it gets only -run (or -splore)
//     and keeps its own folder (%APPDATA%\pico-8), saves and settings.
//   - Splore (Start menu, Emulators page, Home search) starts PICO-8 with
//     -splore, on the handheld with the PICO-8 Roms folder as its root, so the
//     library's carts are in its local list. When it exits, the carts
//     favourited in it are copied from its download folder into that Roms
//     folder and the library rescans. A copy is made once: the user's own or
//     edited cart of that name is never overwritten, and a copy the user
//     deleted is not brought back.
//
// Without the files nothing changes, and Splore says which two files to copy
// where instead of starting anything.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	pico8SystemName      = "PICO-8"
	pico8SploreEntryName = "Splore"
	pico8NativeEmuName   = "PICO-8 (official)"
	pico8DataFile        = "pico8.dat"
	// pico8LookupTTL is how long a look for the program is trusted. The
	// library asks on every start and the picker on every open; a file copied
	// onto the card a moment ago still counts within seconds.
	pico8LookupTTL = 10 * time.Second
	// pico8CartMaxBytes bounds a cart copied out of Splore's downloads. A
	// .p8.png is a 160×205 picture of a few dozen KB; anything this big is not
	// a cart.
	pico8CartMaxBytes = 2 << 20
	// pico8FavouritesMaxBytes bounds the favourites list that is read.
	pico8FavouritesMaxBytes = 1 << 20
	// pico8CopiedFile, in PICO-8's home, lists the downloads already copied
	// into the Roms folder, so a copy the user deleted stays deleted.
	pico8CopiedFile = "jukahub-copied.txt"
)

// pico8NativeBinaryNames are the official program's file names for this
// platform, preferred first. The Raspberry Pi download holds pico8 (32-bit),
// pico8_dyn and pico8_64; a 64-bit handheld runs pico8_64.
func pico8NativeBinaryNames() []string {
	if IsWindows() {
		return []string{"pico8.exe"}
	}
	switch runtime.GOARCH {
	case "arm64":
		return []string{"pico8_64"}
	case "arm":
		return []string{"pico8_dyn", "pico8"}
	}
	return []string{"pico8"}
}

// pico8Card is the handheld's SD card, or "" on a PC.
func pico8Card() string {
	if IsWindows() {
		return ""
	}
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		return root
	}
	if dirExists("/mnt/SDCARD") {
		return "/mnt/SDCARD"
	}
	return ""
}

// pico8NativeDirs are the folders the program may be in, most likely first:
// the BIOS folders MinUI paks and muOS use (Bios/P8, Bios/PICO,
// bios/pico8), the stock TrimUI PICO-8 app, CrossMix's Emus folders, and on a
// PC the places the installers use.
func pico8NativeDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		key := p
		if IsWindows() {
			key = strings.ToLower(p)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		dirs = append(dirs, p)
	}
	subs := []string{"P8", "PICO8", "PICO-8", "PICO", "pico8", "pico-8"}
	if card := pico8Card(); card != "" {
		for _, bios := range []string{"Bios", "BIOS", "bios"} {
			for _, s := range subs {
				add(filepath.Join(card, bios, s))
			}
		}
		for _, rel := range []string{"Apps/pico/bin", "Apps/PICO8/bin", "Apps/PICO8", "Emus/PICO8", "Emus/PICO", "Emus/PICO8/bin", "Emus/PICO/bin"} {
			add(filepath.Join(card, filepath.FromSlash(rel)))
		}
	}
	for _, root := range biosDoctorRoots() {
		for _, s := range subs {
			add(filepath.Join(root, s))
		}
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		add(filepath.Join(dir, "PICO-8"))
	}
	if IsWindows() {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if base := os.Getenv(env); base != "" {
				add(filepath.Join(base, "PICO-8"))
			}
		}
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			add(filepath.Join(base, "Programs", "PICO-8"))
		}
		if base := os.Getenv("APPDATA"); base != "" {
			add(filepath.Join(base, "itch", "apps", "pico-8", "pico-8"))
		}
		if p, err := exec.LookPath("pico8"); err == nil {
			add(filepath.Dir(p))
		}
	}
	return dirs
}

// pico8Lookup is one look for the program.
type pico8Lookup struct {
	// bin is the program, with pico8.dat beside it; "" when not found.
	bin string
	// partialDir is the first folder holding one of the two files without
	// the other, so the setup card can name the one that is missing.
	partialDir string
	haveBin    bool
	haveDat    bool
	at         time.Time
}

var pico8LookupCache struct {
	mu sync.Mutex
	l  pico8Lookup
}

// pico8NativeScan finds the first folder of dirs holding both the program
// (one of names) and pico8.dat.
func pico8NativeScan(dirs, names []string) pico8Lookup {
	var l pico8Lookup
	for _, dir := range dirs {
		dat := regularFile(filepath.Join(dir, pico8DataFile))
		bin := ""
		for _, n := range names {
			if p := filepath.Join(dir, n); regularFile(p) {
				bin = p
				break
			}
		}
		if bin != "" && dat {
			l.bin = bin
			return l
		}
		if (bin != "" || dat) && l.partialDir == "" {
			l.partialDir, l.haveBin, l.haveDat = dir, bin != "", dat
		}
	}
	return l
}

// pico8NativeLookup is the cached look for the program. Any goroutine.
func pico8NativeLookup() pico8Lookup {
	pico8LookupCache.mu.Lock()
	l := pico8LookupCache.l
	pico8LookupCache.mu.Unlock()
	if !l.at.IsZero() && time.Since(l.at) < pico8LookupTTL {
		return l
	}
	l = pico8NativeScan(pico8NativeDirs(), pico8NativeBinaryNames())
	l.at = time.Now()
	pico8LookupCache.mu.Lock()
	pico8LookupCache.l = l
	pico8LookupCache.mu.Unlock()
	return l
}

// pico8NativeForget drops the cached look, so the next one reads the card.
func pico8NativeForget() {
	pico8LookupCache.mu.Lock()
	pico8LookupCache.l = pico8Lookup{}
	pico8LookupCache.mu.Unlock()
}

// pico8NativeBinary is the user's own PICO-8 program, or "".
func pico8NativeBinary() string { return pico8NativeLookup().bin }

// pico8NativeIsLauncher reports whether launcher is the official PICO-8: a
// program named like it with pico8.dat beside it. fake-08 and the firmware's
// scripts are not.
func pico8NativeIsLauncher(launcher string) bool {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(launcher))
	for _, n := range pico8NativeBinaryNames() {
		if base == n {
			return regularFile(filepath.Join(filepath.Dir(launcher), pico8DataFile))
		}
	}
	return false
}

// pico8NameOnly reports whether path is named like the official PICO-8
// program, whether or not pico8.dat is beside it.
func pico8NameOnly(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	for _, n := range pico8NativeBinaryNames() {
		if base == n {
			return true
		}
	}
	return false
}

// pico8IsSystem reports whether a library system is PICO-8.
func pico8IsSystem(system string) bool {
	return strings.EqualFold(strings.TrimSpace(system), pico8SystemName)
}

// pico8NativeEntry is the emulator row of the official PICO-8 at bin.
func pico8NativeEntry(bin string) emuEntry {
	return emuEntry{
		Name:   pico8NativeEmuName,
		Detail: "Your own PICO-8 · " + filepath.Dir(bin),
		Status: emuAvailable,
		Launch: bin,
		Glyph:  "rom",
	}
}

// pico8NativeLaunchers is list (the launchers findGameLaunchersFor found for
// system) with the official PICO-8 first when it is here. Every other entry
// keeps its place, so fake-08 stays a choice.
func pico8NativeLaunchers(system string, list []emuEntry) []emuEntry {
	if !pico8IsSystem(system) {
		return list
	}
	bin := pico8NativeBinary()
	if bin == "" {
		return list
	}
	out := make([]emuEntry, 0, len(list)+1)
	out = append(out, pico8NativeEntry(bin))
	for _, e := range list {
		if strings.EqualFold(filepath.Clean(strings.TrimSpace(e.Launch)), filepath.Clean(bin)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// pico8NativeIndex is the index of the official PICO-8 in list, the
// launchers for system, or -1. emuBestPick recommends it: no other emulator
// plays every cart.
func pico8NativeIndex(system string, list []emuEntry) int {
	if !pico8IsSystem(system) {
		return -1
	}
	for i, e := range list {
		if pico8NativeIsLauncher(e.Launch) {
			return i
		}
	}
	return -1
}

// pico8NativeHome is PICO-8's home folder: config.txt, cdata/ (the carts'
// saves), favourites.txt and bbs/ (Splore's downloads).
func pico8NativeHome() string {
	if card := pico8Card(); card != "" {
		return filepath.Join(card, "Saves", "PICO-8")
	}
	// On a PC PICO-8 starts with no -home (pico8NativeArgs), so its home is
	// the program's own default: ~/.lexaloffle/pico-8 on Linux, the config
	// folder's pico-8 on Windows and macOS. Splore's favourites are read there.
	if runtime.GOOS == "linux" {
		if dir, err := os.UserHomeDir(); err == nil && dir != "" {
			return filepath.Join(dir, ".lexaloffle", "pico-8")
		}
	} else if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "pico-8")
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "PICO-8", "home")
	}
	return ""
}

// pico8NativeRomsDir is the library's PICO-8 folder (Roms/PICO, Roms/PICO8,
// Roms/PICO-8: romSystemAliases, or NextUI's "Pico-8 (P8)": romFolderSystem,
// the same test the library scan uses), or "" when there is none. With create,
// a card that has none gets Roms/PICO8 in the first Roms folder that exists.
func pico8NativeRomsDir(create bool) string {
	roots := romLibraryRoots()
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if sys, _, ok := romFolderSystem(e.Name(), nil); ok && sys.Name == pico8SystemName {
				return filepath.Join(root, e.Name())
			}
		}
	}
	if !create {
		return ""
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) || !dirExists(root) {
			continue
		}
		dir := filepath.Join(root, "PICO8")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logWarn("pico8", "cannot make %s: %v", dir, err)
			return ""
		}
		return dir
	}
	return ""
}

// pico8NativeArgs are the switches every start gets on the handheld: JukaHub's
// home folder (with screenshots and GIFs inside it), the cart folder, full
// screen and the first pad as player one. A PC's PICO-8 is the user's own
// install with its own config.txt, which PICO-8 writes back on exit: it gets
// none, so it keeps the window mode and carts folder the user set up. That is
// any computer without the handheld's card, Linux desktops included.
func pico8NativeArgs(root string) []string {
	if pico8Card() == "" {
		return nil
	}
	var args []string
	if home := pico8NativeHome(); home != "" {
		args = append(args, "-home", home, "-desktop", filepath.Join(home, "desktop"))
	}
	if root != "" {
		args = append(args, "-root_path", root)
	}
	return append(args, "-windowed", "0", "-joystick", "0")
}

// pico8NativeGameArgs is the command line of a cart started in the official
// PICO-8, ok=false when launcher is anything else (gameLaunchCommandArgs).
func pico8NativeGameArgs(launcher, romPath string) ([]string, bool) {
	if strings.TrimSpace(romPath) == "" || !pico8NativeIsLauncher(launcher) {
		return nil, false
	}
	cart := filepath.Clean(romPath)
	return append(pico8NativeArgs(filepath.Dir(cart)), "-run", cart), true
}

// pico8NativePrepareHome makes the home folder, its saves folder (so the
// first cart's save has somewhere to go) and the desktop folder -desktop
// points at (screenshots and GIFs would otherwise fail to save).
func pico8NativePrepareHome() {
	home := pico8NativeHome()
	if home == "" {
		return
	}
	for _, sub := range []string{"cdata", "desktop"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			logWarn("pico8", "cannot make PICO-8's home %s: %v", home, err)
			return
		}
	}
}

// pico8NativeMakeRunnable sets the execute bits a program copied from a PC
// may lack. A card that keeps no permissions (FAT) runs it anyway.
func pico8NativeMakeRunnable(bin string) {
	if IsWindows() {
		return
	}
	info, err := os.Stat(bin)
	if err != nil || info.Mode()&0o111 != 0 {
		return
	}
	if err := os.Chmod(bin, info.Mode()|0o755); err != nil {
		logInfo("pico8", "cannot mark %s executable: %v", bin, err)
	}
}

// pico8NativeEnv is the environment PICO-8 starts with on the handheld: the
// usual child environment, its own folder first on the library path (a
// libSDL2 or libcurl the user put beside it wins), and the handheld's pad
// mapping, which SDL's built-in list lacks (the same one PPSSPP is given).
func pico8NativeEnv(bin string) []string {
	env := screenChildEnv(os.Environ())
	dir := filepath.Dir(bin)
	libs, _ := childEnvLookup(env, "LD_LIBRARY_PATH")
	if strings.TrimSpace(libs) == "" {
		libs = dir
	} else {
		libs = dir + ":" + libs
	}
	env = append(childEnvWithout(env, "LD_LIBRARY_PATH"), "LD_LIBRARY_PATH="+libs)
	if IsTrimuiDevice() {
		if _, set := childEnvLookup(env, "SDL_GAMECONTROLLERCONFIG"); !set {
			env = append(env, "SDL_GAMECONTROLLERCONFIG="+ppssppTrimuiPadMapping)
		}
	}
	return env
}

// pico8NativePrepareLaunch readies a library start in the official PICO-8:
// its home folder, the execute bit and, on the handheld, its environment.
// Other launchers are left alone. Called by launchLibraryGame.
func pico8NativePrepareLaunch(launcher string, cmd *exec.Cmd) {
	if cmd == nil || !pico8NativeIsLauncher(launcher) {
		return
	}
	pico8NativePrepareHome()
	pico8NativeMakeRunnable(launcher)
	if !IsWindows() {
		cmd.Env = pico8NativeEnv(launcher)
	}
}

// ── Splore ───────────────────────────────────────────────────────────────

// pico8SploreEmuEntry is the Emulators page's Splore row. It always opens:
// without PICO-8 it shows what to copy where.
func pico8SploreEmuEntry() emuEntry {
	e := emuEntry{Name: pico8SploreEntryName, Status: emuAvailable}
	if bin := pico8NativeBinary(); bin != "" {
		e.Detail = "PICO-8's cart browser · your own PICO-8 in " + filepath.Dir(bin)
		return e
	}
	e.Status = emuBroken
	e.Detail = "Open for setup · needs your own PICO-8 (" + pico8NativeBinaryNames()[0] + " and " + pico8DataFile + ")"
	return e
}

// pico8SploreRowRefresh rewrites the Splore row in the cached Emulators
// catalog, so it stops saying "needs your own PICO-8" once the files are
// copied. Only that row: rebuilding the whole catalog walks the card.
func pico8SploreRowRefresh() {
	fresh := pico8SploreEmuEntry()
	emuCache.mu.Lock()
	defer emuCache.mu.Unlock()
	for i := range emuCache.entries {
		if emuCache.entries[i].Pinned && emuCache.entries[i].Name == pico8SploreEntryName {
			emuCache.entries[i].Detail = fresh.Detail
			emuCache.entries[i].Status = fresh.Status
		}
	}
}

// pico8SploreOpen is the Splore entry: PICO-8's cart browser when the user's
// own PICO-8 is here, otherwise the card that says which files to copy where.
// Main thread.
func pico8SploreOpen() {
	// A file copied a moment ago counts.
	pico8NativeForget()
	pico8SploreRowRefresh()
	bin := pico8NativeBinary()
	if bin == "" {
		pico8ShowSetup()
		return
	}
	pico8NativePrepareHome()
	pico8NativeMakeRunnable(bin)
	roms := pico8NativeRomsDir(true)
	args := append(pico8NativeArgs(roms), "-splore")
	cmd := hiddenCommand(bin, args...)
	cmd.Dir = filepath.Dir(bin)
	if !IsWindows() {
		cmd.Env = pico8NativeEnv(bin)
	}
	screenChildGroupConfigure(cmd)
	restoreProfile := beginGamePowerProfile(appConfig)
	if err := screenChildStart(cmd); err != nil {
		restoreProfile()
		logWarn("pico8", "Splore did not start (%s): %v", bin, err)
		showLaunchFailureDialog(pico8SploreEntryName, err, "")
		return
	}
	noteGameChildStarted(cmd)
	logInfo("pico8", "Splore started: %s %s", bin, strings.Join(args, " "))
	home := pico8NativeHome()
	go func() {
		waitErr := cmd.Wait()
		screenChildReaped(cmd)
		restoreProfile()
		if waitErr != nil {
			logInfo("pico8", "Splore ended: %v", waitErr)
		}
		copied := pico8CopyFavourites(home, roms)
		tbxPost(func() {
			if copied > 0 {
				ShowToast(pluralizeCount(copied, "Splore favourite", "Splore favourites")+" added to PICO-8 games", ToastKindSuccess)
			}
			if gameLib.open {
				refreshGameLibrary()
			}
		})
	}()
	PlaySuccessSound()
	ShowToast("Opening Splore", ToastKindSuccess)
}

// pico8SetupDir is where the setup card asks for the files: Bios/P8 on the
// card, JukaHub's BIOS/P8 on a PC.
func pico8SetupDir() string {
	if card := pico8Card(); card != "" {
		return filepath.Join(card, "Bios", "P8")
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "BIOS", "P8")
	}
	return filepath.Join("BIOS", "P8")
}

// pico8ShowSetup is the card Splore shows when PICO-8 is not here: the two
// files, the folder they go in, and where they come from. The folder is made
// so it is there to copy into. Nothing is started or downloaded.
func pico8ShowSetup() {
	l := pico8NativeLookup()
	bin := pico8NativeBinaryNames()[0]
	where := pico8SetupDir()
	if filepath.IsAbs(where) {
		if err := os.MkdirAll(where, 0o755); err != nil {
			logInfo("pico8", "cannot make %s: %v", where, err)
		}
	}
	from := "Both come from your own PICO-8 download for Raspberry Pi (pico-8_…_raspi.zip on lexaloffle.com): " +
		bin + " is the 64-bit program this handheld runs, " + pico8DataFile + " its data."
	if IsWindows() {
		from = "Both are in the folder of your own PICO-8 for Windows. A PICO-8 installed in Program Files is found by itself."
	}
	issue := launchIssue{
		Title:  "Copy your own PICO-8 here",
		Files:  []string{bin, pico8DataFile},
		Where:  where,
		Detail: from,
	}
	if l.partialDir != "" {
		have, missing := pico8DataFile, bin
		if l.haveBin {
			have, missing = bin, pico8DataFile
		}
		issue = launchIssue{
			Title:  "Half of PICO-8 is here",
			Files:  []string{missing},
			Where:  l.partialDir,
			Detail: have + " is in this folder, but " + missing + " is not. PICO-8 needs both side by side. " + from,
		}
	}
	summary := "PICO-8 is a paid program, so JukaHub never downloads it. With your own copy in place, Splore opens and PICO-8 carts play on the real PICO-8 instead of fake-08."
	showLaunchDiagnosis("Splore needs PICO-8", summary, []launchIssue{issue}, nil, "")
}

// ── Splore favourites → Roms ─────────────────────────────────────────────

// pico8Favourite is one line of PICO-8's favourites.txt: the downloaded cart
// (relative to PICO-8's home, "bbs/carts/dank_tomb-0.p8.png") when the line
// names one, else the cart's id ("dank_tomb-0", or an old cart's number) and,
// after a numeric id, the first named slug; and the title Splore keeps.
type pico8Favourite struct {
	rel   string
	slugs []string
	title string
}

// pico8FavouritesMaxEntries bounds how many lines of favourites.txt are
// used, and pico8FavouritesMaxCopies how many carts one pass copies: the file
// is outside data, and a huge one must not keep the card busy for minutes.
const (
	pico8FavouritesMaxEntries = 500
	pico8FavouritesMaxCopies  = 200
)

// pico8ParseFavourites reads favourites.txt. Its lines are fields between
// "|", padded with spaces to line up; the layout is not documented, so the
// cart is the field that ends in .p8.png or .p8 wherever it is, else the first
// field that can be a cart name. The title is the first field after that one
// with a letter in it that is not itself a slug: the title comes before the
// author on Splore's lines, so the last field would often be the author.
func pico8ParseFavourites(text string) []pico8Favourite {
	var out []pico8Favourite
	for _, line := range strings.Split(text, "\n") {
		if len(out) >= pico8FavouritesMaxEntries {
			break
		}
		if !strings.Contains(line, "|") {
			continue
		}
		var fav pico8Favourite
		idAt := -1
		for i, f := range strings.Split(line, "|") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			lower := strings.ToLower(f)
			switch {
			case idAt < 0 && (strings.HasSuffix(lower, ".p8.png") || strings.HasSuffix(lower, ".p8")):
				fav.rel, idAt = f, i
			case idAt < 0 && pico8SafeSlug(f):
				fav.slugs, idAt = append(fav.slugs, f), i
			case idAt < 0:
				// Before the cart's id: not the title.
			case fav.rel == "" && len(fav.slugs) == 1 && pico8Numeric(fav.slugs[0]) && pico8SlugLike(f):
				// An old cart's number, then its named slug: try both.
				fav.slugs = append(fav.slugs, f)
			case fav.title == "" && !pico8SlugLike(f) && strings.IndexFunc(f, unicode.IsLetter) >= 0:
				fav.title = f
			}
		}
		if fav.rel != "" || len(fav.slugs) > 0 {
			out = append(out, fav)
		}
	}
	return out
}

// pico8Numeric reports whether s is all digits (an old BBS cart's number).
func pico8Numeric(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// pico8SlugLike reports whether s reads as a BBS cart name ("dank_tomb-0")
// rather than a title: safe characters only, with a "_" or "-" in it.
func pico8SlugLike(s string) bool {
	return pico8SafeSlug(s) && strings.ContainsAny(s, "_-")
}

// pico8SafeSlug reports whether s can be a BBS cart name: letters, digits,
// "_" and "-" only.
func pico8SafeSlug(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// pico8FavouriteSource is the downloaded cart a favourite names, inside home,
// with the key it is remembered by; ok=false when it is not a cart on disk.
func pico8FavouriteSource(home string, fav pico8Favourite) (src, key string, ok bool) {
	var rels []string
	if fav.rel != "" {
		rel := path.Clean(filepath.ToSlash(fav.rel))
		rels = append(rels, rel, "bbs/"+rel)
	} else {
		// A line with only the cart's id: Splore keeps its download as
		// bbs/carts/<id>.p8.png. The id's own field first; at most one
		// named slug after an old cart's number (pico8ParseFavourites).
		for _, w := range fav.slugs {
			if pico8SafeSlug(w) {
				rels = append(rels, "bbs/carts/"+w+".p8.png")
			}
		}
	}
	for _, rel := range rels {
		if rel == "" || path.IsAbs(rel) || strings.Contains(rel, ":") || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		p := filepath.Join(home, filepath.FromSlash(rel))
		if !isPathWithin(p, home) {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > pico8CartMaxBytes {
			continue
		}
		return p, strings.ToLower(rel), true
	}
	return "", "", false
}

// pico8CartExt is ".p8.png" or ".p8", the way name ends.
func pico8CartExt(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".p8.png") {
		return name[len(name)-len(".p8.png"):]
	}
	return filepath.Ext(name)
}

// pico8CleanTitle makes a Splore title a file name: no characters FAT or
// Windows refuse, single spaces, at most 64 letters; "" when nothing with a
// letter in it is left.
func pico8CleanTitle(title string) string {
	var b strings.Builder
	space := false
	letter := false
	n := 0
	for _, r := range strings.TrimSpace(title) {
		if n >= 64 {
			break
		}
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
			n++
		}
		if unicode.IsLetter(r) {
			letter = true
		}
		b.WriteRune(r)
		n++
	}
	out := strings.Trim(b.String(), ". ")
	if !letter || out == "" || pico8ReservedName(out) {
		return ""
	}
	return out
}

// pico8ReservedName reports a name Windows will not create a file under
// whatever the extension: CON, PRN, AUX, NUL, COM1-9 and LPT1-9. A card
// edited on a PC must not hold a cart that Explorer cannot copy or delete.
func pico8ReservedName(name string) bool {
	base := strings.ToUpper(strings.TrimSpace(name))
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// pico8CartFileName is the name a favourite gets in the Roms folder: Splore's
// title when it kept a real one, else the cart's name without its BBS version
// ("dank_tomb-0" is "dank_tomb").
func pico8CartFileName(fav pico8Favourite, src string) string {
	base := filepath.Base(src)
	ext := pico8CartExt(base)
	slug := strings.TrimSuffix(base, ext)
	name := slug
	if i := strings.LastIndexByte(slug, '-'); i > 0 && i < len(slug)-1 && strings.Trim(slug[i+1:], "0123456789") == "" {
		name = slug[:i]
	}
	if fav.title != "" {
		if t := pico8CleanTitle(fav.title); t != "" && !strings.EqualFold(t, slug) && !strings.EqualFold(t, name) {
			name = t
		}
	}
	return name + strings.ToLower(ext)
}

// pico8CartSlugFileName is the fallback name when the title's name is taken
// by a different cart (two carts called "Snake"): the title with the cart's
// BBS name after it, both already safe as a file name. A cart named after its
// slug without the version ("dank_tomb") falls back to the full slug.
func pico8CartSlugFileName(fav pico8Favourite, src string) string {
	named := pico8CartFileName(fav, src)
	ext := pico8CartExt(named)
	slug := strings.TrimSuffix(filepath.Base(src), pico8CartExt(filepath.Base(src)))
	stem := strings.TrimSuffix(named, ext)
	switch {
	case !pico8SafeSlug(slug), strings.EqualFold(stem, slug):
		return named
	case strings.HasPrefix(strings.ToLower(slug), strings.ToLower(stem)):
		return slug + ext
	}
	return stem + " (" + slug + ")" + ext
}

// pico8LoadCopied reads the downloads already copied (pico8CopiedFile).
func pico8LoadCopied(home string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(home, pico8CopiedFile))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

// pico8SaveCopied writes the list back, whole, through a temporary file.
func pico8SaveCopied(home string, done map[string]bool) {
	keys := make([]string, 0, len(done))
	for k := range done {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	target := filepath.Join(home, pico8CopiedFile)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(keys, "\n")+"\n"), 0o644); err != nil {
		logWarn("pico8", "cannot write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		logWarn("pico8", "cannot replace %s: %v", target, err)
		_ = os.Remove(tmp)
	}
}

// pico8CopyCart copies one cart, through a temporary file, so a card pulled
// mid-copy leaves no half cart in the library.
func pico8CopyCart(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, pico8CartMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > pico8CartMaxBytes {
		return fmt.Errorf("%s is too big for a cart", filepath.Base(src))
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		// A full card can leave a short .part behind.
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// pico8CopyFavourites copies the carts favourited in Splore from PICO-8's
// downloads (home/bbs) into roms, once each, and returns how many it copied.
// The same cart already in roms is left alone; a different cart under that
// title gets its BBS name added instead. A download copied before is never
// copied again, so a cart the user deleted from the library stays deleted.
// Off the main thread.
func pico8CopyFavourites(home, roms string) int {
	if home == "" || roms == "" {
		return 0
	}
	list := filepath.Join(home, "favourites.txt")
	info, err := os.Stat(list)
	if err != nil || !info.Mode().IsRegular() || info.Size() > pico8FavouritesMaxBytes {
		return 0
	}
	data, err := os.ReadFile(list)
	if err != nil {
		return 0
	}
	done := pico8LoadCopied(home)
	copied, changed := 0, false
	for _, fav := range pico8ParseFavourites(string(data)) {
		if copied >= pico8FavouritesMaxCopies {
			break
		}
		src, key, ok := pico8FavouriteSource(home, fav)
		if !ok || done[key] {
			continue
		}
		dst := filepath.Join(roms, pico8CartFileName(fav, src))
		if pico8SameCart(src, dst) {
			// Already in the library (copied by hand, or before this list).
			done[key], changed = true, true
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			// Another cart has this title: name this one after its BBS name
			// too, rather than skip it for good.
			dst = filepath.Join(roms, pico8CartSlugFileName(fav, src))
			if _, err := os.Stat(dst); err == nil {
				done[key], changed = true, true
				continue
			}
		}
		if err := pico8CopyCart(src, dst); err != nil {
			logWarn("pico8", "cannot copy Splore favourite %s to %s: %v", src, dst, err)
			continue
		}
		done[key], changed = true, true
		copied++
		logInfo("pico8", "Splore favourite %s copied to %s", filepath.Base(src), dst)
	}
	if changed {
		pico8SaveCopied(home, done)
	}
	return copied
}

// pico8SameCart reports whether dst exists with exactly src's size, the cheap
// sign it is the same download; a cart of another size under that name is a
// different game with the same title.
func pico8SameCart(src, dst string) bool {
	a, err := os.Stat(src)
	if err != nil {
		return false
	}
	b, err := os.Stat(dst)
	return err == nil && b.Mode().IsRegular() && a.Size() == b.Size()
}

// pico8CartInFolder reports whether name, in a folder of system, is a PICO-8
// cart picture (.p8.png). Elsewhere a .png is a picture, and the library
// leaves it to the image viewer (romFileExtensions).
func pico8CartInFolder(system, name string) bool {
	return system == pico8SystemName && strings.HasSuffix(strings.ToLower(name), ".p8.png")
}

