package main

// emulators.go — the Emulators panel: a cross-platform list of emulators with
// their availability.
//
// It is an overlay (like Tools) so it works on every config: the desktop
// config has no "Emulator" scene (the home card used to dead-end there), and
// on the handheld the card must list every emulator with its availability
// instead of a static launcher label. The list is built from the platform's
// real emulator locations: known install folders on Windows, the SD-card
// Emus/ and Roms/ trees plus RetroArch cores on TrimUI.

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

// emuEntry is one row of the availability list. Launch is the entry point to
// execute for available emulators; rows without one are listed for
// availability only (e.g. RetroArch cores).
type emuEntry struct {
	Name   string
	Detail string
	Status emuStatus
	Launch string
	Pinned bool // RetroAchievements sits at the top of the list
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
		e := emuEntry{Name: spec.name}
		if found != "" {
			e.Status = emuAvailable
			e.Launch = found
			e.Detail = found
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
			e := emuEntry{Name: de.Name(), Detail: folder}
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
			// Pinned order is intentional (RetroAchievements, then Game
			// Profiles); stable sort keeps it instead of alphabetising it.
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
	// was the original top row), then the Game Profiles launcher. The profile
	// row is always present so the folder is discoverable before the first
	// import.
	if retroAchievementsSupported() {
		re := retroAchievementsEmuEntry()
		re.Pinned = true
		entries = append(entries, re)
	}
	entries = append(entries, gameProfilesEmuEntry())
	if !IsWindows() {
		entries = append(entries, windowsAppsEmuEntry())
		// PortMaster is a handheld app; listing it on Windows would only offer
		// an installer that cannot produce a working runtime there.
		entries = append(entries, portMasterEmuEntry())
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

// emuRowHit maps one visible row back to its catalog index for clicks.
type emuRowHit struct {
	index int
	rect  sdl.Rect
}

// emuRowHits and emuPanelRect are filled by renderEmuPanel. Only touched on
// the render/event thread.
var emuRowHits []emuRowHit
var emuPanelRect sdl.Rect

// openEmulatorPanel shows the availability list.
func openEmulatorPanel() {
	// Never stack modals: this panel replaces Quick Settings and Tools.
	qs.open = false
	toolsPanel.open = false
	invalidateEmulatorCache()
	// The pinned Game Profiles row reports catalog totals, so rebuild those too.
	invalidateGameProfileCache()
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

func emuPanelMove(delta int) {
	entries := emulatorEntries()
	if len(entries) == 0 {
		return
	}
	emuPanel.cursor += delta
	if emuPanel.cursor < 0 {
		emuPanel.cursor = len(entries) - 1
	}
	if emuPanel.cursor >= len(entries) {
		emuPanel.cursor = 0
	}
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
		ShowToast("Cannot launch "+e.Name, ToastKindError)
		return
	}
	if !regularFile(path) {
		log.Printf("[emulators] %s vanished: %s", e.Name, path)
		ShowToast(e.Name+" is no longer installed", ToastKindError)
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
	if err := cmd.Start(); err != nil {
		log.Printf("[emulators] launch failed for %s: %v", path, err)
		ShowToast("Could not launch "+e.Name, ToastKindError)
		return
	}
	go func() { _ = cmd.Wait() }() // reap the child, keep JukaHub responsive
	PlaySuccessSound()
	ShowToast("Launching "+e.Name, ToastKindSuccess)
}

func emuPanelActivate(config *Config) {
	entries := emulatorEntries()
	if emuPanel.cursor < 0 || emuPanel.cursor >= len(entries) {
		return
	}
	e := entries[emuPanel.cursor]
	if e.Name == gameProfilesEntryName {
		emuPanel.open = false
		openGameProfilePanel(true)
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
		emuPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuPanelMove(1)
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
		emuPanelMove(-1)
	case sdl.K_DOWN:
		emuPanelMove(1)
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

// handleEmuMouse activates a clicked row or closes the panel on a click
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
		// Clicks inside the panel but off a row only consume.
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

// renderEmuPanel draws the modal availability list. Rows that do not fit are
// scrolled with the cursor.
func renderEmuPanel(renderer *sdl.Renderer, config *Config) {
	if !emuPanel.open || renderer == nil {
		return
	}
	entries := emulatorEntries()
	if len(entries) == 0 {
		entries = []emuEntry{{
			Name:   "No emulators found",
			Detail: "Nothing to list on this platform",
			Status: emuMissing,
		}}
	}
	if emuPanel.cursor >= len(entries) {
		emuPanel.cursor = len(entries) - 1
	}
	if emuPanel.cursor < 0 {
		emuPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const padX = int32(24)
	rowH := 8 + medH + 4 + smallH + 8
	headerH := 18 + medH + 4 + smallH + 14
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(entries))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	emuPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// Keep the cursor inside the visible window.
	if emuPanel.cursor < emuPanel.scroll {
		emuPanel.scroll = emuPanel.cursor
	}
	if emuPanel.cursor >= emuPanel.scroll+int(visible) {
		emuPanel.scroll = emuPanel.cursor - int(visible) + 1
	}
	if emuPanel.scroll > len(entries)-int(visible) {
		emuPanel.scroll = len(entries) - int(visible)
	}
	if emuPanel.scroll < 0 {
		emuPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	// Accent hairline at the top of the panel.
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "Emulators", ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, emuSummary(entries), ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	// The theme accent can be very dark, so reuse the install card's contrast
	// check instead of painting the selected row with an invisible tint.
	focusColor := installHighlight()

	rowW := panelW - 2*padX
	emuRowHits = make([]emuRowHit, 0, visible)
	for i := int32(0); i < visible; i++ {
		idx := emuPanel.scroll + int(i)
		e := entries[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		emuRowHits = append(emuRowHits, emuRowHit{index: idx, rect: r})

		focused := idx == emuPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, e.Name, ColorTextPrimary(), r.X+16, r.Y+8)
		label, labelColor := emuStatusLabel(e.Status)
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, labelColor, r.X+r.W-16-int32(lw), r.Y+8+(medH-smallH)/2)
		if e.Detail != "" {
			renderText(renderer, config, font, ellipsize(font, e.Detail, r.W-32),
				ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
	}

	hint := "A: launch   B: close"
	if retroAchievementsSupported() {
		hint = "A: launch   Y: achievements   B: close"
	}
	if emuPanel.scroll > 0 || emuPanel.scroll+int(visible) < len(entries) {
		if retroAchievementsSupported() {
			hint = fmt.Sprintf("D-Pad: scroll %d-%d of %d   A: launch   Y: achievements   B: close",
				emuPanel.scroll+1, emuPanel.scroll+int(visible), len(entries))
		} else {
			hint = fmt.Sprintf("D-Pad: scroll %d-%d of %d   A: launch   B: close",
				emuPanel.scroll+1, emuPanel.scroll+int(visible), len(entries))
		}
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
