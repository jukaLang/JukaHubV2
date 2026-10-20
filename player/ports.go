package main

// ports.go — PortMaster support: install the PortMaster app, list the ports it
// installed, and launch them.
//
// PortMaster on TrimUI is an app under Apps/PortMaster, and each installed
// port leaves a launcher script in Ports/ (older layouts used Roms/Ports or
// Roms/PORTS). JukaHub lists those scripts so ports are reachable from the
// Emulators screen instead of only through the firmware's own Ports tab.

import (
	"context"
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

const (
	portMasterEntryName   = "PortMaster Ports"
	portMasterAppName     = "PortMaster"
	portMasterInstallSize = "about 24 MB"
)

var (
	// portMasterInstallURL is the official TrimUI package from the PortMaster
	// GUI releases. The "latest" path always resolves to the current release.
	portMasterInstallURL = "https://github.com/PortsMaster/PortMaster-GUI/releases/latest/download/trimui.portmaster.zip"
	// portMasterSDRoot is the SD-card root. Tests point it at a temp folder.
	portMasterSDRoot       = "/mnt/SDCARD"
	portMasterInstallState struct {
		mu      sync.Mutex
		running bool
	}
)

// portMasterLauncher returns the PortMaster launcher inside sdRoot, or "".
// The TrimUI package uses Apps/PortMaster/launch.sh; the older generic layout
// and CFW installs use a PortMaster.sh script in one of the Ports folders.
func portMasterLauncher(sdRoot string) string {
	if strings.TrimSpace(sdRoot) == "" {
		return ""
	}
	for _, rel := range []string{
		"Apps/PortMaster/launch.sh",
		"Apps/PortMaster/PortMaster/PortMaster.sh",
		"Ports/PortMaster.sh",
		"Roms/Ports/PortMaster.sh",
		"Roms/PORTS/PortMaster.sh",
	} {
		p := filepath.Join(sdRoot, filepath.FromSlash(rel))
		if regularFile(p) {
			return p
		}
	}
	return ""
}

func portMasterInstalled() bool {
	return portMasterLauncher(portMasterSDRoot) != ""
}

func portMasterInstalling() bool {
	portMasterInstallState.mu.Lock()
	defer portMasterInstallState.mu.Unlock()
	return portMasterInstallState.running
}

func setPortMasterInstalling(running bool) {
	portMasterInstallState.mu.Lock()
	portMasterInstallState.running = running
	portMasterInstallState.mu.Unlock()
}

// portRoots returns the folders a PortMaster install exposes launcher scripts
// in. Duplicates are harmless: scanning dedupes by name.
func portRoots(sdRoot string) []string {
	return []string{
		filepath.Join(sdRoot, "Ports"),
		filepath.Join(sdRoot, "Roms", "Ports"),
		filepath.Join(sdRoot, "Roms", "PORTS"),
		filepath.Join(sdRoot, "Data", "ports"),
	}
}

// portEntry is one row in the Ports panel.
type portEntry struct {
	Name    string
	Detail  string
	Status  emuStatus
	Launch  string
	Install bool // the PortMaster install action
	Master  bool // the PortMaster app itself
	Info    bool // a hint row with no status label
}

// scanPortLaunchers lists top-level .sh scripts in roots, excluding
// PortMaster's own launcher, and dedupes names case-insensitively.
func scanPortLaunchers(roots []string) []portEntry {
	seen := map[string]bool{}
	var entries []portEntry
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, item := range items {
			name := item.Name()
			if item.IsDir() || !strings.EqualFold(filepath.Ext(name), ".sh") {
				continue
			}
			if strings.HasPrefix(name, ".") || strings.EqualFold(name, "PortMaster.sh") {
				continue
			}
			label := strings.TrimSuffix(name, filepath.Ext(name))
			key := strings.ToLower(strings.TrimSpace(label))
			if key == "" || seen[key] {
				continue
			}
			path := filepath.Join(root, name)
			if !regularFile(path) {
				continue
			}
			seen[key] = true
			entries = append(entries, portEntry{
				Name:   label,
				Detail: path,
				Status: emuAvailable,
				Launch: path,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

// buildPortEntries composes the panel rows: PortMaster first (installed or
// installable), then every installed port.
func buildPortEntries(launcher string, roots []string) []portEntry {
	entries := []portEntry{}
	if launcher == "" {
		entries = append(entries, portEntry{
			Name:    portMasterAppName,
			Detail:  "Install PortMaster to browse and add ports · " + portMasterInstallSize,
			Status:  emuMissing,
			Install: true,
		})
		return entries
	}
	entries = append(entries, portEntry{
		Name:   portMasterAppName,
		Detail: "Open PortMaster to browse, install and update ports",
		Status: emuAvailable,
		Launch: launcher,
		Master: true,
	})
	ports := scanPortLaunchers(roots)
	if len(ports) == 0 {
		entries = append(entries, portEntry{
			Name:   "No ports installed",
			Detail: "Open PortMaster to install one",
			Status: emuMissing,
			Master: true,
			Info:   true,
		})
		return entries
	}
	return append(entries, ports...)
}

// portsCache memoizes installed-port detection; installing PortMaster or
// opening the panel rebuilds it.
var portsCache struct {
	mu      sync.Mutex
	entries []portEntry
	built   bool
}

func portEntries() []portEntry {
	portsCache.mu.Lock()
	defer portsCache.mu.Unlock()
	if portsCache.built {
		return portsCache.entries
	}
	launcher := portMasterLauncher(portMasterSDRoot)
	portsCache.entries = buildPortEntries(launcher, portRoots(portMasterSDRoot))
	portsCache.built = true
	return portsCache.entries
}

func invalidatePortsCache() {
	portsCache.mu.Lock()
	portsCache.built = false
	portsCache.entries = nil
	portsCache.mu.Unlock()
}

// portMasterEmuEntry is the pinned Emulators row that opens the Ports panel.
func portMasterEmuEntry() emuEntry {
	e := emuEntry{Name: portMasterEntryName, Pinned: true}
	if !portMasterInstalled() {
		e.Status = emuMissing
		e.Detail = "Open to install PortMaster · " + portMasterInstallSize
		return e
	}
	ports := scanPortLaunchers(portRoots(portMasterSDRoot))
	e.Status = emuAvailable
	e.Detail = fmt.Sprintf("PortMaster installed · %d port(s)", len(ports))
	return e
}

// startPortMasterInstall downloads the official TrimUI package and unpacks it
// into Apps/PortMaster. The download runs in the background so the panel and
// the install banner stay responsive.
func startPortMasterInstall() error {
	if IsWindows() {
		return fmt.Errorf("PortMaster runs on the handheld, not on Windows")
	}
	if portMasterLauncher(portMasterSDRoot) != "" {
		return fmt.Errorf("PortMaster is already installed")
	}
	portMasterInstallState.mu.Lock()
	if portMasterInstallState.running {
		portMasterInstallState.mu.Unlock()
		return fmt.Errorf("the PortMaster installer is already running")
	}
	portMasterInstallState.running = true
	portMasterInstallState.mu.Unlock()

	installBarBegin(portMasterAppName)
	if !IsOnline() {
		setPortMasterInstalling(false)
		installBarFail("No internet — connect WiFi, then try again")
		return fmt.Errorf("no internet connection")
	}
	go func() {
		defer setPortMasterInstalling(false)
		if err := installPortMaster(portMasterSDRoot, portMasterInstallURL); err != nil {
			log.Printf("[portmaster] install failed: %v", err)
			installBarFail(firstLine(err.Error()))
			ShowToast("Could not install PortMaster — "+firstLine(err.Error()), ToastKindError)
			return
		}
		invalidatePortsCache()
		invalidateEmulatorCache()
		installBarDone()
		log.Printf("[portmaster] installed into %s", filepath.Join(portMasterSDRoot, "Apps", "PortMaster"))
		ShowToast("PortMaster installed — open Ports to add games", ToastKindSuccess)
	}()
	return nil
}

// installPortMaster performs the download and atomic extraction. The staging
// folder lives on the SD card so the final rename does not cross filesystems.
func installPortMaster(sdRoot, downloadURL string) error {
	if strings.TrimSpace(sdRoot) == "" {
		return fmt.Errorf("SD card path is unknown")
	}
	if err := os.MkdirAll(sdRoot, 0o755); err != nil {
		return fmt.Errorf("SD card is not writable: %w", err)
	}
	stage, err := os.MkdirTemp(sdRoot, ".portmaster-install-*")
	if err != nil {
		return fmt.Errorf("cannot create staging folder: %w", err)
	}
	defer os.RemoveAll(stage)

	archive := filepath.Join(stage, "trimui.portmaster.zip")
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgress(downloadURL, archive, installBarProgress); err != nil {
		return err
	}

	installBarPhase(installPhaseExtract)
	unpacked := filepath.Join(stage, "unpack")
	if err := extractZipSafe(context.Background(), archive, unpacked, 4096, 512<<20); err != nil {
		return fmt.Errorf("unpack PortMaster: %w", err)
	}
	return installPortMasterFromStage(unpacked, sdRoot)
}

// installPortMasterFromStage moves an extracted Apps/PortMaster tree into
// place. Extraction is atomic: a failed download or unpack never leaves a
// half-written app behind. An existing install is never overwritten, so ports
// and PortMaster's own settings survive a reinstall attempt.
func installPortMasterFromStage(unpacked, sdRoot string) error {
	inner := filepath.Join(unpacked, "Apps", "PortMaster")
	if !regularFile(filepath.Join(inner, "launch.sh")) {
		// Tolerate a future package that drops the Apps/ prefix.
		inner = filepath.Join(unpacked, "PortMaster")
	}
	if !regularFile(filepath.Join(inner, "launch.sh")) {
		return fmt.Errorf("the download does not contain Apps/PortMaster/launch.sh")
	}
	appsDir := filepath.Join(sdRoot, "Apps")
	target := filepath.Join(appsDir, "PortMaster")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("Apps/PortMaster already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot check %s: %w", target, err)
	}
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return fmt.Errorf("cannot create Apps folder: %w", err)
	}
	if err := os.Rename(inner, target); err != nil {
		return fmt.Errorf("cannot move PortMaster into Apps: %w", err)
	}
	if err := os.Chmod(filepath.Join(target, "launch.sh"), 0o755); err != nil {
		log.Printf("[portmaster] warn: cannot make launch.sh executable: %v", err)
	}
	return nil
}

// ── Ports panel ──────────────────────────────────────────────────────────

type portsPanelState struct {
	open      bool
	cursor    int
	scroll    int
	returnEmu bool
}

var portsPanel portsPanelState

type portRowHit struct {
	index int
	rect  sdl.Rect
}

var portRowHits []portRowHit
var portsPanelRect sdl.Rect

func openPortsPanel(returnEmu bool) {
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{open: true, returnEmu: returnEmu}
	portsPanelRect = sdl.Rect{}
	portRowHits = nil
	invalidatePortsCache()
	PlayActivateSound()
}

func closePortsPanel() {
	if !portsPanel.open {
		return
	}
	returnEmu := portsPanel.returnEmu
	portsPanel = portsPanelState{}
	portsPanelRect = sdl.Rect{}
	portRowHits = nil
	if returnEmu {
		emuPanel.open = true
		emuPanelRect = sdl.Rect{}
		emuRowHits = nil
	}
	PlayBackSound()
}

func portsPanelMove(delta int) {
	entries := portEntries()
	if len(entries) == 0 {
		return
	}
	portsPanel.cursor += delta
	if portsPanel.cursor < 0 {
		portsPanel.cursor = len(entries) - 1
	}
	if portsPanel.cursor >= len(entries) {
		portsPanel.cursor = 0
	}
	PlayNavSound()
}

func portsPanelActivate() {
	entries := portEntries()
	if portsPanel.cursor < 0 || portsPanel.cursor >= len(entries) {
		return
	}
	e := entries[portsPanel.cursor]
	if e.Install {
		if portMasterInstalling() {
			ShowToast("The PortMaster installer is already running", ToastKindInfo)
			return
		}
		if err := startPortMasterInstall(); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
		}
		return
	}
	if e.Master {
		launcher := e.Launch
		if launcher == "" {
			// "No ports installed": open PortMaster to add one.
			launcher = portMasterLauncher(portMasterSDRoot)
		}
		if launcher == "" {
			return
		}
		portsPanel.open = false
		launchPortScript(portMasterAppName, launcher)
		return
	}
	if e.Launch == "" {
		return
	}
	portsPanel.open = false
	launchPortScript(e.Name, e.Launch)
}

// launchPortScript starts a PortMaster launcher. PortMaster's scripts assume
// bash (they source control.txt and use process substitution), so prefer bash
// when the firmware provides it and fall back to sh otherwise.
func launchPortScript(name, path string) {
	if !regularFile(path) {
		ShowToast(name+" is no longer installed", ToastKindError)
		invalidatePortsCache()
		return
	}
	shell := "sh"
	if bash, err := exec.LookPath("bash"); err == nil && bash != "" {
		shell = bash
	}
	log.Printf("[portmaster] launching %s (%s via %s)", name, path, shell)
	cmd := exec.Command(shell, path)
	cmd.Dir = filepath.Dir(path)
	if err := cmd.Start(); err != nil {
		log.Printf("[portmaster] launch failed for %s: %v", path, err)
		ShowToast("Could not launch "+name, ToastKindError)
		return
	}
	go func() { _ = cmd.Wait() }()
	PlaySuccessSound()
	ShowToast("Launching "+name, ToastKindSuccess)
}

func ctrlPortsPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		portsPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		portsPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		portsPanelActivate()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closePortsPanel()
	}
	return true
}

func handlePortsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !portsPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		portsPanelMove(-1)
	case sdl.K_DOWN:
		portsPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		portsPanelActivate()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closePortsPanel()
	}
	return true
}

func handlePortsMouse(mx, my int32) bool {
	if !portsPanel.open {
		return false
	}
	for _, hit := range portRowHits {
		if pointInRect(mx, my, hit.rect) {
			portsPanel.cursor = hit.index
			portsPanelActivate()
			return true
		}
	}
	if portsPanelRect.W > 0 && pointInRect(mx, my, portsPanelRect) {
		return true
	}
	closePortsPanel()
	return true
}

func portsSummary(entries []portEntry) string {
	installed := false
	ports := 0
	for _, e := range entries {
		if e.Master && e.Status == emuAvailable {
			installed = true
		}
		if !e.Master && !e.Install && e.Status == emuAvailable {
			ports++
		}
	}
	if !installed {
		return "PortMaster is not installed"
	}
	if ports == 0 {
		return "PortMaster installed · no ports yet"
	}
	return fmt.Sprintf("PortMaster installed · %d port(s)", ports)
}

// renderPortsPanel draws the modal Ports list, scrolling with the cursor.
func renderPortsPanel(renderer *sdl.Renderer, config *Config) {
	if !portsPanel.open || renderer == nil {
		return
	}
	entries := portEntries()
	if len(entries) == 0 {
		entries = []portEntry{{Name: "No ports found", Detail: "Nothing to list", Status: emuMissing}}
	}
	if portsPanel.cursor >= len(entries) {
		portsPanel.cursor = len(entries) - 1
	}
	if portsPanel.cursor < 0 {
		portsPanel.cursor = 0
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
	portsPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if portsPanel.cursor < portsPanel.scroll {
		portsPanel.scroll = portsPanel.cursor
	}
	if portsPanel.cursor >= portsPanel.scroll+int(visible) {
		portsPanel.scroll = portsPanel.cursor - int(visible) + 1
	}
	if portsPanel.scroll > len(entries)-int(visible) {
		portsPanel.scroll = len(entries) - int(visible)
	}
	if portsPanel.scroll < 0 {
		portsPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	renderText(renderer, config, medium, "PortMaster Ports", ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, portsSummary(entries), ColorTextTertiary(), panelX+padX, panelY+18+medH+4)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	portRowHits = make([]portRowHit, 0, visible)
	for i := int32(0); i < visible; i++ {
		idx := portsPanel.scroll + int(i)
		e := entries[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		portRowHits = append(portRowHits, portRowHit{index: idx, rect: r})

		focused := idx == portsPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, e.Name, ColorTextPrimary(), r.X+16, r.Y+8)
		if !e.Info {
			label, labelColor := emuStatusLabel(e.Status)
			if e.Install {
				label, labelColor = "Install", installHighlight()
			}
			lw, _, _ := font.SizeUTF8(label)
			renderText(renderer, config, font, label, labelColor, r.X+r.W-16-int32(lw), r.Y+8+(medH-smallH)/2)
		}
		if e.Detail != "" {
			renderText(renderer, config, font, ellipsize(font, e.Detail, r.W-32),
				ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
	}

	hint := "A: open   B: close"
	if len(entries) > 0 && entries[0].Install {
		hint = "A: install PortMaster   B: close"
	}
	if portsPanel.scroll > 0 || portsPanel.scroll+int(visible) < len(entries) {
		hint = fmt.Sprintf("D-Pad: scroll %d-%d of %d   %s",
			portsPanel.scroll+1, portsPanel.scroll+int(visible), len(entries), hint)
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
