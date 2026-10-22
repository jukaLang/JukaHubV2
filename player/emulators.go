package main

// emulators.go — the Emulators page: a cross-platform catalog of emulators
// with their availability.
//
// It is an overlay (like Tools) so it works on every config: the desktop
// config has no "Emulator" scene (the home card used to dead-end there), and
// on the handheld the card must list every emulator with its availability
// instead of a static launcher label. The catalog is built from the platform's
// real emulator locations: known install folders on Windows, the SD-card
// Emus/ and Roms/ trees plus RetroArch cores on TrimUI. emutiles.go draws it as
// a page of tiles.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// emuStatus is the availability of one emulator.
type emuStatus uint8

const (
	emuAvailable emuStatus = iota // installed with a usable launcher
	emuBroken                     // installed but unusable (no launcher)
	emuMissing                    // expected but not installed
)

// emuEntry is one tile of the availability page. Launch is the entry point to
// execute for available emulators; entries without one are listed for
// availability only (e.g. RetroArch cores).
type emuEntry struct {
	Name   string
	Detail string
	Status emuStatus
	Launch string
	Pinned bool // RetroAchievements and the tool rows come first
	// IconPath is the emulator's own picture when the install ships one: the
	// icon in a TrimUI Emus/ folder, or an image next to a Windows executable.
	IconPath string
	// Glyph is the fallback icon key (see DrawHomeIcon) for entries with no
	// picture, so a missing icon still reads as an emulator tile.
	Glyph string
}

// ── Windows catalog ──────────────────────────────────────────────────────

// winEmuSpec pairs a display name with the executable names its installs use.
type winEmuSpec struct {
	name  string
	files []string
}

var winEmuCatalog = []winEmuSpec{
	{"RetroArch", []string{"retroarch.exe"}},
	{"PPSSPP", []string{"PPSSPPWindows.exe", "ppsspp.exe"}},
	{"Dolphin", []string{"Dolphin.exe"}},
	{"PCSX2", []string{"pcsx2-qt.exe", "pcsx2.exe"}},
	{"RPCS3", []string{"rpcs3.exe"}},
	{"DuckStation", []string{"DuckStation.exe", "duckstation-qt.exe"}},
	{"Cemu", []string{"Cemu.exe"}},
	{"Vita3K", []string{"vita3k.exe"}},
	{"xemu", []string{"xemu.exe"}},
	{"melonDS", []string{"melonDS.exe"}},
	{"mGBA", []string{"mGBA.exe", "mgba.exe"}},
	{"FCEUX", []string{"fceux.exe"}},
	{"Snes9x", []string{"snes9x.exe"}},
	{"Mednafen", []string{"mednafen.exe"}},
	{"MAME", []string{"mame.exe"}},
	{"bsnes", []string{"bsnes.exe"}},
}

// winSearchDirs returns the folders searched for Windows emulator installs:
// portable copies next to JukaHub.exe first, then the standard program roots.
func winSearchDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			dirs = append(dirs, v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); v != "" {
		dirs = append(dirs, filepath.Join(v, "Programs"))
	}
	return dirs
}

// windowsEmulatorEntries detects the cataloged Windows emulators inside dirs.
// lookPath covers PATH installs; the deterministic folder probes cover
// standard installs like "C:\Program Files\PPSSPP\PPSSPPWindows.exe".
func windowsEmulatorEntries(dirs []string, lookPath func(string) (string, error)) []emuEntry {
	entries := make([]emuEntry, 0, len(winEmuCatalog))
	for _, spec := range winEmuCatalog {
		found := ""
		if lookPath != nil {
			for _, file := range spec.files {
				if p, err := lookPath(file); err == nil && p != "" {
					found = p
					break
				}
			}
		}
		if found == "" {
		search:
			for _, dir := range dirs {
				for _, file := range spec.files {
					for _, cand := range []string{
						filepath.Join(dir, file),
						filepath.Join(dir, spec.name, file),
					} {
						if regularFile(cand) {
							found = cand
							break search
						}
					}
				}
			}
		}
		e := emuEntry{Name: spec.name, Glyph: "gamepad"}
		if found != "" {
			e.Status = emuAvailable
			e.Launch = found
			e.Detail = found
			e.IconPath = sidecarIcon(found)
		} else {
			e.Status = emuMissing
			e.Detail = "Not installed"
		}
		entries = append(entries, e)
	}
	return entries
}

func regularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ── Handheld (TrimUI) catalog ────────────────────────────────────────────

// TrimUI emulator locations: the SD card first, then the internal firmware
// tree. Stock and CrossMix both use the Emus/ and Roms/ layout.
var (
	deviceEmuRoots = []string{"/mnt/SDCARD/Emus", "/usr/trimui/emus"}
	deviceRomsRoot = "/mnt/SDCARD/Roms"
	deviceRACores  = "/mnt/SDCARD/RetroArch/.retroarch/cores"
)

// deviceEmulatorEntries builds the handheld list: every emulator folder under
// the Emus roots with its availability, every installed RetroArch core, and
// every ROM system that has games but no emulator at all.
func deviceEmulatorEntries(emuRoots []string, romsRoot, coresDir string) []emuEntry {
	byName := map[string]emuEntry{}
	add := func(e emuEntry) {
		key := strings.ToLower(e.Name)
		if old, ok := byName[key]; ok {
			// Keep the first installed entry; anything replaces a broken or
			// missing one so a second root can rescue it.
			if old.Status == emuAvailable {
				return
			}
			byName[key] = e
			return
		}
		byName[key] = e
	}

	for _, root := range emuRoots {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if !de.IsDir() {
				continue
			}
			folder := filepath.Join(root, de.Name())
			e := emuEntry{Name: de.Name(), Detail: folder, Glyph: "gamepad", IconPath: emuFolderIcon(folder)}
			// launch.sh is the launcher on stock and CrossMix; a folder
			// without one still gets listed, as installed-but-unusable.
			switch {
			case regularFile(filepath.Join(folder, "launch.sh")):
				e.Status = emuAvailable
				e.Launch = filepath.Join(folder, "launch.sh")
			case regularFile(filepath.Join(folder, "launch")):
				e.Status = emuAvailable
				e.Launch = filepath.Join(folder, "launch")
			default:
				e.Status = emuBroken
				e.Detail = folder + " — no launcher"
			}
			add(e)
		}
	}

	// RetroArch cores are emulators in their own right: listing each installed
	// core shows exactly which systems are covered.
	if coresDir != "" {
		if cores, err := os.ReadDir(coresDir); err == nil {
			for _, c := range cores {
				if c.IsDir() || !strings.HasSuffix(c.Name(), ".so") {
					continue
				}
				name := strings.TrimSuffix(c.Name(), ".so")
				name = strings.TrimSuffix(name, "_libretro")
				add(emuEntry{
					Name:   name,
					Detail: "RetroArch core — " + filepath.Join(coresDir, c.Name()),
					Status: emuAvailable,
					Glyph:  "gamepad",
				})
			}
		}
	}

	// Systems with games but no emulator are the rows that matter most: the
	// user finds out here instead of when launching fails.
	if romsRoot != "" {
		if roms, err := os.ReadDir(romsRoot); err == nil {
			for _, r := range roms {
				if !r.IsDir() {
					continue
				}
				if _, ok := byName[strings.ToLower(r.Name())]; ok {
					continue
				}
				add(emuEntry{
					Name:   r.Name(),
					Detail: "Games in " + filepath.Join(romsRoot, r.Name()) + " — no emulator found",
					Status: emuMissing,
					Glyph:  "gamepad",
				})
			}
		}
	}

	entries := make([]emuEntry, 0, len(byName))
	for _, e := range byName {
		entries = append(entries, e)
	}
	sortEmulatorEntries(entries)
	return entries
}

// sortEmulatorEntries groups rows by availability (available first), then
// alphabetically.
func sortEmulatorEntries(entries []emuEntry) {
	rank := map[emuStatus]int{emuAvailable: 0, emuBroken: 1, emuMissing: 2}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Pinned != entries[j].Pinned {
			return entries[i].Pinned
		}
		if entries[i].Pinned {
			// Pinned order is intentional; stable sort keeps it instead of
			// alphabetising the utility rows.
			return false
		}
		if rank[entries[i].Status] != rank[entries[j].Status] {
			return rank[entries[i].Status] < rank[entries[j].Status]
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// ── Catalog cache ────────────────────────────────────────────────────────

// emuCache memoizes the catalog. It is rebuilt whenever the panel opens, so
// installing an emulator shows up on the next visit without a restart.
var emuCache struct {
	mu      sync.Mutex
	entries []emuEntry
	built   bool
}

// emulatorEntries returns the platform's emulator catalog.
func emulatorEntries() []emuEntry {
	emuCache.mu.Lock()
	defer emuCache.mu.Unlock()
	if emuCache.built {
		return emuCache.entries
	}
	var entries []emuEntry
	if IsWindows() {
		entries = windowsEmulatorEntries(winSearchDirs(), exec.LookPath)
	} else {
		entries = deviceEmulatorEntries(deviceEmuRoots, deviceRomsRoot, deviceRACores)
	}
	// Pinned rows keep their append order: RetroAchievements stays first (it
	// was the original top row), followed by BIOS Doctor and the launchers.
	// The profile row is always present so the folder is discoverable before
	// the first import.
	// Every pinned row gets a glyph so its tile reads before any icon exists;
	// the catalog functions themselves stay unaware of presentation.
	pin := func(e emuEntry, glyph string) emuEntry {
		e.Pinned = true
		e.Glyph = glyph
		return e
	}
	if retroAchievementsSupported() {
		entries = append(entries, pin(retroAchievementsEmuEntry(), "favorites"))
	}
	entries = append(entries, pin(emuEntry{
		Name:   biosDoctorEntryName,
		Detail: "Check PS1, Dreamcast and Saturn firmware",
		Status: emuAvailable,
	}, "tools"))
	entries = append(entries, pin(gamesLibraryEmuEntry(), "gamepad"))
	entries = append(entries, pin(gameProfilesEmuEntry(), "apps"))
	entries = append(entries, pin(compatibilityEmuEntry(), "tools"))
	if !IsWindows() {
		entries = append(entries, pin(windowsAppsEmuEntry(), "apps"))
		// PortMaster is a handheld app; listing it on Windows would only offer
		// an installer that cannot produce a working runtime there.
		entries = append(entries, pin(portMasterEmuEntry(), "packages"))
	}
	sortEmulatorEntries(entries)
	emuCache.entries = entries
	emuCache.built = true
	return entries
}

func invalidateEmulatorCache() {
	emuCache.mu.Lock()
	emuCache.built = false
	emuCache.entries = nil
	emuCache.mu.Unlock()
}

// gamesLibraryEmuEntry is the pinned door to the game browser. It deliberately
// reports no counts: counting a card full of ROMs on every open would make the
// Emulators panel wait on the SD card, and the library screen scans when it
// opens anyway.
func gamesLibraryEmuEntry() emuEntry {
	return emuEntry{
		Name:   gamesLibraryEntryName,
		Detail: "Browse Roms/ by console, with box art and one-press resume",
		Status: emuAvailable,
		Pinned: true,
	}
}

// gameProfilesEmuEntry is the pinned launcher row. It reports the catalog
// totals so an empty or partially broken library is obvious before opening it.
func gameProfilesEmuEntry() emuEntry {
	total, ready, issues := gameProfileStats()
	detail := "Open the launcher · drop JSON into " + gameProfilesImportDir()
	if total > 0 {
		detail = fmt.Sprintf("%d profile(s) · %d ready", total, ready)
		if issues > 0 {
			detail += fmt.Sprintf(" · %d file issue(s)", issues)
		}
	}
	return emuEntry{
		Name:   gameProfilesEntryName,
		Detail: detail,
		Status: emuAvailable,
		Pinned: true,
	}
}

// compatibilityEmuEntry is the pinned door to the per-game intelligence
// panel. It reports catalog coverage without walking the ROM tree, so opening
// Emulators stays fast on a large SD card.
func compatibilityEmuEntry() emuEntry {
	reports, verified, systems := compatStats()
	detail := fmt.Sprintf("%d report(s) · %d system(s)", reports, systems)
	if verified > 0 {
		detail += fmt.Sprintf(" · %d verified", verified)
	}
	if reports == 0 {
		detail = "Per-game reports and boost plans · import a community pack"
	}
	return emuEntry{
		Name:   compatEntryName,
		Detail: detail,
		Status: emuAvailable,
		Pinned: true,
	}
}

// windowsAppsEmuEntry is the pinned launcher for plain .exe files. It is
// always available to open so the setup page can explain a missing Wine or
// WestonPack install; "Unavailable" means no runtime yet, not a hidden row.
func windowsAppsEmuEntry() emuEntry {
	entries := windowsAppEntries()
	runtime := currentWindowsRuntimeStatus()
	e := emuEntry{Name: windowsAppsEntryName, Pinned: true}
	if !runtime.ready() {
		e.Status = emuBroken
		e.Detail = "Open for setup · " + runtime.Problem
		return e
	}
	e.Status = emuAvailable
	e.Detail = fmt.Sprintf("%d app(s) · Wine ready · %s", len(entries), runtime.displayLabel())
	return e
}

// ── Panel ────────────────────────────────────────────────────────────────

type emuPanelState struct {
	open   bool
	cursor int
	scroll int
}

var emuPanel emuPanelState

// emuRowHit maps one visible tile back to its catalog index for clicks.
type emuRowHit struct {
	index int
	rect  sdl.Rect
}

// emuRowHits and emuPanelRect are filled by renderEmuPanel: the visible tile
// rects and the page rect. Only touched on the render/event thread.
var emuRowHits []emuRowHit
var emuPanelRect sdl.Rect

// openEmulatorPanel shows the tile page.
func openEmulatorPanel() {
	// Never stack modals: this panel replaces Quick Settings and Tools.
	qs.open = false
	toolsPanel.open = false
	invalidateEmulatorCache()
	// The pinned Game Profiles row reports catalog totals, so rebuild those too.
	invalidateGameProfileCache()
	// On Windows an executable's icon comes from the shell in the background;
	// tiles show the gamepad glyph until the picture lands in the cache.
	requestAppIconExtraction(emuProgramPaths(emulatorEntries()))
	emuPanel.open = true
	emuPanel.cursor = 0
	emuPanel.scroll = 0
	emuPanelRect = sdl.Rect{}
	emuRowHits = nil
	PlayActivateSound()
}

func closeEmulatorPanel() {
	emuPanel.open = false
	PlayBackSound()
}

// emuPanelMove walks the tile grid. Left/Right stay inside the row and
// Up/Down move a whole row, clamping at the ends so focus never jumps across
// the page the way a flat list did.
func emuPanelMove(dx, dy int) {
	entries := emulatorEntries()
	if len(entries) == 0 {
		return
	}
	next := emuGridTarget(emuPanel.cursor, len(entries), emuGridView().cols, dx, dy)
	if next == emuPanel.cursor {
		return
	}
	emuPanel.cursor = next
	PlayNavSound()
}

// launchEmulatorEntry starts a discovered emulator detached from JukaHub. The
// paths come from this file's fixed scan roots and catalog - never from
// config input - so unlike the Apps launcher there is no folder sandbox to
// pass; the entry point itself is still re-checked before it runs.
func launchEmulatorEntry(e emuEntry) {
	path := strings.TrimSpace(e.Launch)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".bat", ".cmd", ".lnk", ".url", ".sh", "":
	default:
		log.Printf("[emulators] refusing to launch %q", path)
		showLaunchFailureDialog(e.Name, fmt.Errorf("unsupported launcher file type %q", ext), "")
		return
	}
	if !regularFile(path) {
		log.Printf("[emulators] %s vanished: %s", e.Name, path)
		showLaunchFailureDialog(e.Name, fmt.Errorf("launcher file is missing: %w", os.ErrNotExist), "")
		invalidateEmulatorCache()
		return
	}
	log.Printf("[emulators] launching %s (%s)", e.Name, path)
	var cmd *exec.Cmd
	switch {
	case IsWindows() && (ext == ".lnk" || ext == ".url"):
		// Shortcuts are resolved by the shell handler, not executed directly.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case IsWindows() && (ext == ".bat" || ext == ".cmd"):
		cmd = exec.Command("cmd", "/c", path)
	case ext == ".sh" || ext == "":
		cmd = exec.Command("sh", path)
	default:
		cmd = exec.Command(path)
	}
	cmd.Dir = filepath.Dir(path)
	// Raise the CPU profile for the emulator and drop it back when the child
	// exits. beginGamePowerProfile never fails a launch: an unsupported
	// firmware only logs.
	restoreProfile := beginGamePowerProfile(appConfig)
	if err := cmd.Start(); err != nil {
		restoreProfile()
		log.Printf("[emulators] launch failed for %s: %v", path, err)
		showLaunchFailureDialog(e.Name, err, "")
		return
	}
	recorder := startTimelapse(appConfig, e.Name)
	go func() {
		_ = cmd.Wait() // reap the child, keep JukaHub responsive
		restoreProfile()
		recorder.Stop()
	}()
	PlaySuccessSound()
	ShowToast("Launching "+e.Name, ToastKindSuccess)
}

func emuPanelActivate(config *Config) {
	entries := emulatorEntries()
	if emuPanel.cursor < 0 || emuPanel.cursor >= len(entries) {
		return
	}
	e := entries[emuPanel.cursor]
	if e.Name == biosDoctorEntryName {
		openCommunityPanel(config, communityPageBIOSDoctor)
		emuPanel.open = false
		return
	}
	if e.Name == gamesLibraryEntryName {
		openGameLibraryPanel()
		return
	}
	if e.Name == gameProfilesEntryName {
		emuPanel.open = false
		openGameProfilePanel(true)
		return
	}
	if e.Name == compatEntryName {
		emuPanel.open = false
		openCompatPanel(true)
		return
	}
	if e.Name == windowsAppsEntryName {
		emuPanel.open = false
		openWindowsAppsPanel(true)
		return
	}
	if e.Name == portMasterEntryName {
		emuPanel.open = false
		openPortsPanel(true)
		return
	}
	if e.Name == retroAchievementsEntryName {
		openRetroAchievementsPanelFromEmu()
		return
	}
	switch e.Status {
	case emuAvailable:
		if e.Launch == "" {
			// Availability-only rows (RetroArch cores) explain themselves.
			ShowToast(e.Name+" is available — start it from RetroArch", ToastKindInfo)
			return
		}
		emuPanel.open = false
		launchEmulatorEntry(e)
	case emuBroken:
		ShowToast(e.Name+" is installed but has no launcher", ToastKindWarning)
	default:
		ShowToast(e.Name+": no emulator found", ToastKindWarning)
	}
}

// ctrlEmuPanel is called from the controller overlay priority chain.
func ctrlEmuPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		emuPanelMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuPanelMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		emuPanelMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		emuPanelMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		emuPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_Y:
		if retroAchievementsSupported() {
			openRetroAchievementsPanelFromEmu()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeEmulatorPanel()
	}
	return true
}

// handleEmuKey processes keyboard input while the panel is open and consumes
// every key so the scene underneath never reacts behind the modal.
func handleEmuKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !emuPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		emuPanelMove(0, -1)
	case sdl.K_DOWN:
		emuPanelMove(0, 1)
	case sdl.K_LEFT:
		emuPanelMove(-1, 0)
	case sdl.K_RIGHT:
		emuPanelMove(1, 0)
	case sdl.K_RETURN, sdl.K_SPACE:
		emuPanelActivate(config)
	case sdl.K_y:
		if retroAchievementsSupported() {
			openRetroAchievementsPanelFromEmu()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeEmulatorPanel()
	}
	return true
}

// handleEmuMouse activates a clicked tile or closes the page on a click
// outside it. Returns true when the click was consumed.
func handleEmuMouse(mx, my int32, config *Config) bool {
	if !emuPanel.open {
		return false
	}
	for _, hit := range emuRowHits {
		if pointInRect(mx, my, hit.rect) {
			emuPanel.cursor = hit.index
			emuPanelActivate(config)
			return true
		}
	}
	if emuPanelRect.W > 0 && pointInRect(mx, my, emuPanelRect) {
		// Clicks inside the page but off a tile only consume.
		return true
	}
	closeEmulatorPanel()
	return true
}

func emuStatusLabel(s emuStatus) (string, sdl.Color) {
	switch s {
	case emuAvailable:
		return "Available", ColorToastSuccess
	case emuBroken:
		return "Unavailable", ColorToastError
	default:
		return "Not installed", ColorTextTertiary()
	}
}

func emuSummary(entries []emuEntry) string {
	var ok, broken, missing int
	for _, e := range entries {
		switch e.Status {
		case emuAvailable:
			ok++
		case emuBroken:
			broken++
		default:
			missing++
		}
	}
	parts := []string{fmt.Sprintf("%d available", ok)}
	if broken > 0 {
		parts = append(parts, fmt.Sprintf("%d unavailable", broken))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d not installed", missing))
	}
	return strings.Join(parts, " · ")
}
