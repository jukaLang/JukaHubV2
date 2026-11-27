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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
	Compat  portCompat     // whether the port's own manifest can run on this chip
	Install bool           // the PortMaster install action
	Master  bool           // the PortMaster app itself
	Info    bool           // a hint row with no status label
	Catalog mcCatalogEntry // a downloadable entry from PortMaster's live catalogue
	Action  portAction     // a row of an installed port's options (ports_manage.go)
	Store   bool           // the row that opens the Ports Store (ports_store.go)
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
			compat := portCompatForLauncher(path)
			detail := path
			if portCompatBlocked(compat.State) {
				// The manifest itself says this build cannot run here; the panel
				// refuses it rather than letting it fail inside PortMaster.
				detail = path + " · " + compat.Note
			}
			entries = append(entries, portEntry{
				Name:   label,
				Detail: detail,
				Status: emuAvailable,
				Launch: path,
				Compat: compat,
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
	portsCache.entries = portsAnnotate(buildPortEntries(launcher, portRoots(portMasterSDRoot)))
	portsCache.built = true
	return portsCache.entries
}

// portsAnnotate adds what the background checks found to the rows: a newer
// PortMaster, and ports whose catalogue build is newer than the installed one
// (once the catalogue was read this visit). Main thread, at cache build only.
func portsAnnotate(entries []portEntry) []portEntry {
	for i := range entries {
		e := &entries[i]
		switch {
		case e.Master && !e.Info && e.Launch != "":
			if note := portMasterUpdateNote(); note != "" {
				e.Detail = note
			}
		case !e.Master && !e.Install && e.Launch != "" && len(portsPanel.catalogEntries) > 0:
			meta := portMetaFor(e.Launch)
			if c, ok := portCatalogMatch(meta, e.Launch, portsPanel.catalogEntries); ok && portUpdateAvailable(meta, c) {
				e.Detail = "Update available · " + hintKeyFor("Y", "Options") + " for options · " + e.Detail
			}
		}
	}
	return entries
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
	e.Detail = "PortMaster installed · " + pluralizeCount(len(ports), "port", "ports")
	return e
}

// startPortMasterInstall downloads the official TrimUI package and unpacks it
// into Apps/PortMaster. The download runs in the background so the panel and
// the install banner stay responsive.
func startPortMasterInstall() error { return startPortMasterInstallThen(nil) }

// startPortMasterInstallThen is startPortMasterInstall that tells then, on the
// main thread, how the install ended: a port's start waits for the files
// (portMasterGate).
func startPortMasterInstallThen(then func(error)) error {
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
			logWarn("portmaster", "install failed: %v", err)
			installBarFail(firstLine(err.Error()))
			ShowToast("Could not install PortMaster — "+firstLine(err.Error()), ToastKindError)
			portMasterTell(then, err)
			return
		}
		invalidatePortsCache()
		invalidateEmulatorCache()
		installBarDone()
		logDebug("portmaster", "installed into %s", filepath.Join(portMasterSDRoot, "Apps", "PortMaster"))
		ShowToast("PortMaster installed — open Ports to add games", ToastKindSuccess)
		noteAwayToolInstalled("PortMaster")
		trophyEvent(tkPortMaster)
		// What a stock TrimUI lacks for ports (a full bash) is fetched now, so
		// the first port does not wait; Python only when PortMaster's own menu
		// is opened (portmaster_runtime.go).
		tbxPost(pmRuntimePrefetch)
		portMasterTell(then, nil)
	}()
	return nil
}

// portMasterTell hands an install's or repair's end to the port launch waiting
// for it, on the main thread.
func portMasterTell(then func(error), err error) {
	if then != nil {
		tbxPost(func() { then(err) })
	}
}

// startPortMasterRepair downloads the official TrimUI package again and writes
// its files over the installed PortMaster, which mends a damaged or
// half-updated install. Ports, the runtimes in libs/, themes and settings
// are not in the package and stay as they are.
func startPortMasterRepair() error { return startPortMasterRepairThen(nil) }

// startPortMasterRepairThen is startPortMasterRepair that tells then, on the
// main thread, how it ended (portMasterGate).
func startPortMasterRepairThen(then func(error)) error {
	if IsWindows() {
		return fmt.Errorf("PortMaster runs on the handheld, not on Windows")
	}
	target := filepath.Join(portMasterSDRoot, "Apps", "PortMaster")
	if !dirExists(target) {
		if portMasterLauncher(portMasterSDRoot) == "" {
			return startPortMasterInstallThen(then)
		}
		return fmt.Errorf("this PortMaster is not the TrimUI app in Apps/PortMaster; update it from its own menu")
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
		if err := repairPortMaster(portMasterSDRoot, portMasterInstallURL); err != nil {
			logWarn("portmaster", "repair failed: %v", err)
			installBarFail(firstLine(err.Error()))
			tbxPost(func() { ShowToast("Could not repair PortMaster — "+firstLine(err.Error()), ToastKindError) })
			portMasterTell(then, err)
			return
		}
		installBarDone()
		logInfo("portmaster", "repaired %s from %s", target, portMasterInstallURL)
		tbxPost(func() {
			ShowToast("PortMaster is on its latest release — its program files are fresh, your ports are untouched", ToastKindSuccess)
			invalidatePortsCache()
			invalidateEmulatorCache()
			portMasterVersionForget()
			pmRuntimeForget()
			pmRuntimePrefetch()
		})
		portMasterTell(then, nil)
	}()
	return nil
}

// portMasterDownloadPackage downloads and unpacks the TrimUI package into a
// staging folder on the card (so the final move does not cross filesystems)
// and returns its Apps/PortMaster tree and the stage to remove afterwards. It
// reports on the install card's current run and stops with it.
func portMasterDownloadPackage(sdRoot, downloadURL string) (string, string, error) {
	installBarPhase(installPhaseDownload)
	// The transfer and the unpack both run on the card's run token: the strip's
	// X on this chip stops them instead of only hiding the bar.
	return portMasterDownloadPackageCtx(installBarRunContext(), sdRoot, downloadURL, installBarProgress, installBarPhase)
}

// portMasterDownloadPackageCtx is portMasterDownloadPackage on ctx, with its
// own progress and phase reports (the automatic update's card,
// portmaster_autoupdate.go).
func portMasterDownloadPackageCtx(ctx context.Context, sdRoot, downloadURL string, progress func(done, total int64), phase func(string)) (string, string, error) {
	if strings.TrimSpace(sdRoot) == "" {
		return "", "", fmt.Errorf("SD card path is unknown")
	}
	if err := os.MkdirAll(sdRoot, 0o755); err != nil {
		return "", "", fmt.Errorf("SD card is not writable: %w", err)
	}
	stage, err := os.MkdirTemp(sdRoot, ".portmaster-install-*")
	if err != nil {
		return "", "", fmt.Errorf("cannot create staging folder: %w", err)
	}
	archive := filepath.Join(stage, "trimui.portmaster.zip")
	phase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, downloadURL, archive, progress); err != nil {
		os.RemoveAll(stage)
		return "", "", err
	}
	phase(installPhaseExtract)
	unpacked := filepath.Join(stage, "unpack")
	if err := extractZipSafe(ctx, archive, unpacked, 4096, 512<<20); err != nil {
		os.RemoveAll(stage)
		return "", "", fmt.Errorf("unpack PortMaster: %w", err)
	}
	inner, err := portMasterPackageTree(unpacked)
	if err != nil {
		os.RemoveAll(stage)
		return "", "", err
	}
	return inner, stage, nil
}

// portMasterPackageTree is the Apps/PortMaster folder of an unpacked package.
func portMasterPackageTree(unpacked string) (string, error) {
	inner := filepath.Join(unpacked, "Apps", "PortMaster")
	if !regularFile(filepath.Join(inner, "launch.sh")) {
		// Tolerate a future package that drops the Apps/ prefix.
		inner = filepath.Join(unpacked, "PortMaster")
	}
	if !regularFile(filepath.Join(inner, "launch.sh")) {
		return "", fmt.Errorf("the download does not contain Apps/PortMaster/launch.sh")
	}
	if !regularFile(filepath.Join(inner, "PortMaster", "control.txt")) {
		return "", fmt.Errorf("the download does not contain PortMaster/control.txt")
	}
	return inner, nil
}

// installPortMaster performs the download and atomic extraction.
func installPortMaster(sdRoot, downloadURL string) error {
	inner, stage, err := portMasterDownloadPackage(sdRoot, downloadURL)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	return installPortMasterTree(inner, sdRoot)
}

// repairPortMaster writes a fresh package over Apps/PortMaster.
func repairPortMaster(sdRoot, downloadURL string) error {
	inner, stage, err := portMasterDownloadPackage(sdRoot, downloadURL)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	installBarPhase(installPhaseExtract)
	target := filepath.Join(sdRoot, "Apps", "PortMaster")
	if err := portMasterOverlay(inner, target); err != nil {
		return fmt.Errorf("write PortMaster's files: %w", err)
	}
	_ = os.Chmod(filepath.Join(target, "launch.sh"), 0o755)
	portMasterEnsureDataDirs(sdRoot)
	if err := syncDisks(); err != nil {
		logWarn("portmaster", "%v", err)
	}
	return nil
}

// portMasterOverlay copies every file of src over dst, each through a
// temporary file and a rename, so an interrupted repair leaves whole files.
// PortMaster's version file goes last: an update cut short still reads as the
// old version, and the next check brings it forward again. Nothing in dst that
// src lacks is touched.
func portMasterOverlay(src, dst string) error {
	var last []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.EqualFold(filepath.ToSlash(rel), "PortMaster/version") {
			last = append(last, rel)
			return nil
		}
		return portMasterOverlayFile(p, to)
	})
	if err != nil {
		return err
	}
	for _, rel := range last {
		if err := portMasterOverlayFile(filepath.Join(src, rel), filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	return nil
}

// portMasterOverlayFile writes one file over to (a temporary file, then a
// rename).
func portMasterOverlayFile(from, to string) error {
	tmp := to + ".jukahub-new"
	if err := copyFileStream(from, tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, to); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// installPortMasterTree moves an extracted Apps/PortMaster tree into place.
// Extraction is atomic: a failed download or unpack never leaves a
// half-written app behind. An existing install is never overwritten, so ports
// and PortMaster's own settings survive a reinstall attempt.
func installPortMasterTree(inner, sdRoot string) error {
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
		logWarn("portmaster", "warn: cannot make launch.sh executable: %v", err)
	}
	for _, note := range portMasterEnsureDataDirs(sdRoot) {
		logInfo("portmaster", "%s", note)
	}
	return nil
}

// ── Ports panel ──────────────────────────────────────────────────────────

type portsPanelState struct {
	open              bool
	cursor            int
	scroll            int
	returnEmu         bool
	catalog           bool
	mayRun            bool
	query             string
	catalogEntries    []mcCatalogEntry
	catalogRows       []portEntry
	catalogLoading    bool
	catalogErr        string
	catalogGeneration uint64
	// An installed port's options (Y; ports_manage.go).
	opts      bool
	optsFor   portEntry
	optsRows  []portEntry
	optsSub   string
	optsBack  int
	confirmAt time.Time
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
	portsPanel = portsPanelState{open: true, returnEmu: returnEmu, mayRun: true}
	portsPanelRect = sdl.Rect{}
	portRowHits = nil
	invalidatePortsCache()
	// Quietly, for the "Update available" lines and a newer PortMaster: the
	// catalogue is cached for a while, so this is one download per session.
	if portsLibraryEnabled() && portMasterInstalled() && IsOnline() {
		portsPanelLoadCatalog()
		portMasterVersionCheck()
		// A newer Ubuntu build of the private runtime, at most weekly.
		privateGlibcRefreshAsync()
	}
	// No PortMaster yet: its files are installed now, online
	// (portmaster_autoupdate.go). Port zips waiting in its autoinstall folder
	// are installed by JukaHub, without opening PortMaster
	// (ports_native_install.go).
	if portsLibraryEnabled() {
		pmAutoInstallOnOpen()
		if portMasterInstalled() {
			pnAutoinstallSweep()
		}
	}
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
	entries := portsPanelRows()
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
	entries := portsPanelRows()
	if portsPanel.cursor < 0 || portsPanel.cursor >= len(entries) {
		return
	}
	e := entries[portsPanel.cursor]
	if e.Store {
		PlayActivateSound()
		portsStoreOpen(appConfig)
		return
	}
	if portsPanel.opts {
		portsPanelActivateOption(e)
		return
	}
	if portsPanel.catalog {
		if e.Catalog.Zip != "" {
			if e.Status == emuAvailable {
				ShowToast(e.Name+hintSentence2(" is already installed — %s for Installed, then %s on it to update or remove it", "X", "Installed", "Y", "Options"), ToastKindInfo)
				return
			}
			if err := portCatalogInstallStart(e.Catalog); err != nil {
				ShowToast(err.Error(), ToastKindWarning)
			}
		} else if portsPanel.catalogErr != "" {
			portsPanelLoadCatalog()
		}
		return
	}
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
	// Flag, don't guess: a 32-bit-only build is refused with the manifest's own
	// reason, which is more useful than a black screen or a missing library.
	// portIgnoreCompat is the escape hatch for a manifest that is wrong (some
	// ports ship a 64-bit wrapper the metadata does not mention).
	portsPanelLaunchInstalled(e)
}

// portMasterLaunchNeed says what a port's start must do to PortMaster's files
// before its script can load them. Both false: a folder with control.txt is on
// the card and the port can go ahead.
func portMasterLaunchNeed() (install, repair bool) {
	_, err := os.Lstat(pmAutoTarget())
	return portMasterLaunchNeedOf(portRealControlFolder(), err == nil, portMasterLauncher(portMasterSDRoot))
}

// portMasterLaunchNeedOf decides from what was found on the card: the folder
// control.txt is in (portRealControlFolder), whether the TrimUI app's folder is
// there (pmAutoTarget), and the launcher (portMasterLauncher). install: nothing
// on the card for a script to load. repair: the TrimUI app is there but its
// control.txt is not, which writing the package's files over it mends. A
// PortMaster outside Apps/PortMaster belongs to another firmware and updates
// from its own menu.
func portMasterLaunchNeedOf(control string, appExists bool, launcher string) (install, repair bool) {
	if control != "" {
		return false, false
	}
	if appExists {
		return false, true
	}
	if launcher != "" {
		return false, false
	}
	return true, false
}

// portMasterGate runs on the main thread before a port whose script loads
// PortMaster's files starts: a card with no PortMaster installs it first, and
// the TrimUI app without its control.txt is repaired, so the port runs instead
// of dying while it sources a control.txt that is not there. The launch carries
// on by itself when the download ends (errPortPreparing), and the report that
// said "open PortMaster once" for a card JukaHub could have prepared is gone.
// Main thread.
func portMasterGate(name, path string) error {
	install, repair := portMasterLaunchNeed()
	if !install && !repair {
		return nil
	}
	if !pmAutoSupported() {
		return nil // not a handheld JukaHub installs PortMaster on
	}
	if portMasterInstalling() || installBarActive() {
		ShowToast("Setting up PortMaster's files — start "+name+" again in a moment", ToastKindInfo)
		return errPortPreparing
	}
	if !IsOnline() {
		return fmt.Errorf("%s needs PortMaster's files before it can run: connect to Wi-Fi and start it again, and JukaHub installs them (%s)",
			name, portMasterInstallSize)
	}
	then := func(err error) {
		if err != nil {
			return // the install's own card and toast said why
		}
		if install, repair := portMasterLaunchNeed(); install || repair {
			ShowToast("PortMaster's files are still missing — press Y on the PortMaster row, then Repair PortMaster", ToastKindWarning)
			return
		}
		ShowToast("PortMaster's files are ready", ToastKindSuccess)
		launchPortScript(name, path)
	}
	what := "installing PortMaster's files (" + portMasterInstallSize + ")"
	var err error
	if repair {
		what = "repairing PortMaster's files"
		err = startPortMasterRepairThen(then)
	} else {
		err = startPortMasterInstallThen(then)
	}
	if err != nil {
		return fmt.Errorf("%s needs PortMaster's files: %w", name, err)
	}
	ShowToast("Before "+name+" starts: "+what, ToastKindInfo)
	return errPortPreparing
}

// launchPortScript starts a port, or PortMaster itself (name ==
// portMasterAppName), and says so. Main thread.
func launchPortScript(name, path string) {
	if privateGlibcPortNeedsRuntime(path) && !privateGlibcReady() {
		ShowToast("Downloading GreenOvercast's compatibility runtime…", ToastKindInfo)
		privateGlibcEnsureAsync(func(err error) {
			if err != nil {
				ShowToast("Could not prepare GreenOvercast — "+firstLine(err.Error()), ToastKindError)
				return
			}
			launchPortScript(name, path)
		})
		return
	}
	if err := startPortScript(name, path); err != nil {
		if !errors.Is(err, errPortPreparing) {
			ShowToast(firstLine(err.Error()), ToastKindError)
		}
		return
	}
	PlaySuccessSound()
	ShowToast("Launching "+name, ToastKindSuccess)
}

// startPortScript starts a PortMaster launcher the way the firmware's Ports
// tab would (the command and environment: ports_env.go) and makes it a game
// like any other: the screen, audio and input hand-over, the power profile,
// the in-game menu and Select + Start held (they act on every screen child),
// the recents, playtime and the game assistant's title (recordGameLaunch),
// the launch log, a diagnosis when it quits at once, and afterwards the
// helpers it left behind are ended and the lists are read again. Main thread.
func startPortScript(name, path string) error {
	if !regularFile(path) {
		invalidatePortsCache()
		return fmt.Errorf("%s is no longer installed", name)
	}
	master := name == portMasterAppName
	var game libraryGame
	if !master {
		game = portLibraryGame(path)
		if err := playtimeAllowGameLaunch(game.Title); err != nil {
			return err
		}
		// A port's script sources PortMaster's control.txt before it does
		// anything else: install PortMaster when the card has none, repair
		// the TrimUI app when its control.txt is gone, then carry on.
		if err := portMasterGate(name, path); err != nil {
			return err
		}
	}
	// On a TrimUI, the firmware's bash and python3 are checked once a session
	// and what PortMaster lacks is downloaded first (portmaster_runtime.go);
	// the launch then starts again by itself.
	if err := pmRuntimeGate(master, func() { launchPortScript(name, path) }); err != nil {
		return err
	}
	if !master {
		// A runtime the port declares and libs/ lacks is downloaded first: its
		// script would ask harbourmaster, which needs Python
		// (ports_native_install.go).
		if err := pnRuntimeGate(name, path, func() { launchPortScript(name, path) }); err != nil {
			return err
		}
	}
	plan := portBuildLaunchPlan(appConfig, path, master)
	shadow, err := privateGlibcPreparePortPlan(&plan, path)
	if err != nil {
		return err
	}
	// GreenOvercast's settings: kept private, and a saved sign-in its last
	// start could not read set aside (greenovercast_store.go). Before
	// portLogOpen, which empties that start's log.
	goPrepareSettings(&plan, name, path)
	if master && shadow == "" && plan.Mode == portLaunchDirect && len(plan.Args) == 2 && plan.Args[1] == path {
		// PortMaster's launch.sh without TRIMUI_EX runs as a patched copy.
		run, copyPath, notes := pmMasterLauncher(path)
		plan.Args[1], shadow = run, copyPath
		plan.Notes = append(plan.Notes, notes...)
	}
	plan.Notes = append(plan.Notes, pmRuntimeNotes(master)...)
	logInfo("portmaster", "launching %s: %s %s (PortMaster folder %s%s)", name, plan.Name, strings.Join(plan.Args, " "),
		setOr(plan.Control, "unknown"), map[string]string{"": "", "link": ", linked", "xdg": ", via XDG_DATA_HOME"}[plan.Fix])
	cmd := hiddenCommand(plan.Name, plan.Args...)
	cmd.Dir = plan.Dir
	// The environment is the port's alone, so JukaHub's own audio, libraries
	// and the next launch are untouched even if the port crashes.
	cmd.Env = plan.Env
	// A script's own #!/bin/bash line gets a real bash too, in the launch's
	// own mount namespace, when the firmware's /bin/bash is not one
	// (portmaster_binbash.go). Applied just before the start.
	binBash, binBashNote := pmBinBashPrepare(appConfig)
	if binBashNote != "" {
		plan.Notes = append(plan.Notes, binBashNote)
	}
	logFile, logPath := portLogOpen(name, path, plan)
	if logFile != nil {
		cmd.Stdout, cmd.Stderr = logFile, logFile
	}
	// GreenOvercast (Xbox Cloud Gaming, the console at home) decodes the
	// picture on the CPU: Xbox boost gives it the handheld (xboxboost.go).
	var boost *xboxBoostSession
	if !master {
		boost = xboxBoostBegin(xboxBoostKindForPort(path), path, name)
	}
	restoreProfile := beginLaunchPowerProfile(appConfig, path) // per-game profile, PWR-6
	// Same hand-off rule as the emulator launchers: the child leads its own
	// process group, so a wrapper script that backgrounds the game keeps the
	// screen parked until the game itself exits.
	screenChildGroupConfigure(cmd)
	binBash.apply(cmd)
	err = screenChildStart(cmd)
	if logFile != nil {
		_ = logFile.Close() // the child has its own copy
	}
	if err != nil {
		if shadow != "" {
			_ = os.Remove(shadow)
		}
		restoreProfile()
		boost.end()
		logWarn("portmaster", "launch failed for %s: %v", path, err)
		return fmt.Errorf("could not launch %s", name)
	}
	noteGameChildStarted(cmd)
	boost.started(cmd.Process.Pid)
	// GPU boost in games: ports count as heavy (gpuboost.go).
	gpuBoosted := gpuBoostBeginPort(path, name, master)
	logDone := portLogWatch(logPath)
	launchedAt := time.Now()
	gameLaunchWatchStart(cmd)
	diag := gameDiagPortStart(name, path) // a port that quits at once is diagnosed (gamediag.go)
	if diag != nil {
		// The launch as it ran, for the card's bug report (launchreport.go).
		diag.rec = launchRecordNew("PortMaster port", name, path)
		diag.rec.LogPath = logPath
		diag.rec.noteCommand(cmd)
		diag.rec.noteStarted(cmd, diag.memMB)
	}
	if !master {
		gameLaunchLogStart(game, cmd, gameLaunchMemFreeMB())
	}
	recorder := startTimelapse(appConfig, name)
	var splash *pmSplashWatch
	if master {
		// PortMaster's splash must neither cover its menu nor outlive it
		// (portmaster_splash.go).
		splash = pmSplashWatchStart(cmd)
	}
	go func() {
		waitErr := cmd.Wait()
		splashKind, endNote := splash.stop()
		// What the launcher left that only served it (PortMaster's splash, a
		// key mapper) ends now: the hand-back below waits for the whole group.
		if left := portEndOrphanHelpers(cmd); len(left) > 0 {
			note := portOrphanNote(left)
			if master && splashKind != pmSplashGaveUp && pmSplashLeft(left) {
				splashKind = pmSplashOrphaned
				pmSplashRecord(splashKind, note)
			}
			endNote = strings.TrimPrefix(endNote+"; "+note, "; ")
		}
		if master && splashKind == pmSplashNone {
			pmSplashForget() // this PortMaster needed no help: an older note no longer applies
		}
		if shadow != "" {
			_ = os.Remove(shadow)
		}
		screenChildReaped(cmd)
		portLogWatchDone(logDone)
		portLogNoteEnd(logPath, waitErr, time.Since(launchedAt), endNote)
		stopped := gameLaunchWatchEnd(cmd)
		diag.noteEnd(time.Now(), stopped)
		if !master {
			gameLaunchLogEnd(game, waitErr, time.Since(launchedAt), stopped)
		}
		restoreProfile()
		boost.end()
		gpuBoosted.end()
		recorder.Stop()
		portAfterExit(path, master, time.Since(launchedAt), splashKind)
		diag.exited(waitErr)
	}()
	if master {
		// PortMaster opened: a card that shipped with it never installs it, so
		// opening it earns the PortMaster trophy too (a best, so once is enough).
		trophyEventPeak(tkPortMaster, 1)
		return nil
	}
	// A port is a game; PortMaster's own menu is not. recordGameLaunch counts
	// the launch trophies and writes the recents entry Continue, playtime and
	// the game assistant read.
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	return nil
}

// portCompatIgnored is the escape hatch for a port whose manifest is wrong:
// `portIgnoreCompat: true` in the user config launches it anyway, and the row's
// badge is still shown so the choice is deliberate.
func portCompatIgnored(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "portIgnoreCompat"); ok {
		return coerceBool(v)
	}
	return false
}

// portLaunchEnv is the environment a PortMaster launch gets.
//
// The handheld firmware has no PulseAudio, and a port that autodetects its SDL
// audio driver can come up silent or crackly. Pinning the driver for the child
// is the fix that needs no root, no system file and no cleanup: the parent
// environment is never modified, so nothing has to be restored if the port
// hangs or is killed. Keys:
//
//	portAudioWorkaround  bool, default on
//	portAudioDriver      default "alsa"
//	portAudioDevice      optional, passed as SDL_AUDIODEV
func portLaunchEnv(config *Config) []string {
	if config == nil {
		return portAudioEnvFor(runtime.GOOS, nil)
	}
	return portAudioEnvFor(runtime.GOOS, config.Variables.Custom)
}

// portAudioEnvFor is the pure form, so the policy is testable on any host.
func portAudioEnvFor(goos string, custom map[string]interface{}) []string {
	if goos != "linux" {
		return nil
	}
	enabled := true
	driver := "alsa"
	device := ""
	if custom != nil {
		if v, ok := customValue(custom, "portAudioWorkaround"); ok {
			enabled = coerceBool(v)
		}
		if v, ok := customString(custom, "portAudioDriver"); ok && strings.TrimSpace(v) != "" {
			driver = strings.TrimSpace(v)
		}
		if v, ok := customString(custom, "portAudioDevice"); ok {
			device = strings.TrimSpace(v)
		}
	}
	if !enabled {
		return nil
	}
	env := []string{"SDL_AUDIODRIVER=" + driver}
	if device != "" {
		env = append(env, "SDL_AUDIODEV="+device, "AUDIODEV="+device)
	}
	return env
}

func ctrlPortsPanel(e *sdl.ControllerButtonEvent) bool {
	if virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		portsPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		portsPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		portsPanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		if !portsPanel.opts {
			portsPanelToggleCatalog()
		}
	case sdl.CONTROLLER_BUTTON_Y:
		portsPanelSecondary()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if portsPanel.catalog && !portsPanel.opts {
			vkOpenPortsSearch()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		portsPanelBack()
	}
	return true
}

// portsPanelSecondary is Y: the May Run filter in the catalogue, an
// installed port's options in the list.
func portsPanelSecondary() {
	switch {
	case portsPanel.opts:
	case portsPanel.catalog:
		portsPanelToggleMayRun()
	default:
		entries := portsPanelRows()
		if portsPanel.cursor >= 0 && portsPanel.cursor < len(entries) {
			portsPanelOpenOptions(entries[portsPanel.cursor])
		}
	}
}

// portsPanelBack is B: out of a port's options, else the panel.
func portsPanelBack() {
	if portsPanel.opts {
		portsPanelCloseOptions()
		return
	}
	closePortsPanel()
}

func handlePortsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !portsPanel.open || virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		portsPanelMove(-1)
	case sdl.K_DOWN:
		portsPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		portsPanelActivate()
	case sdl.K_TAB, sdl.K_x:
		if !portsPanel.opts {
			portsPanelToggleCatalog()
		}
	case sdl.K_f, sdl.K_y, sdl.K_o:
		portsPanelSecondary()
	case sdl.K_s, sdl.K_SLASH:
		if portsPanel.catalog && !portsPanel.opts {
			vkOpenPortsSearch()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		portsPanelBack()
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
	portsPanelBack()
	return true
}

func portsSummary(entries []portEntry) string {
	installed := false
	ports := 0
	unsupported := 0
	for _, e := range entries {
		if e.Master && e.Status == emuAvailable {
			installed = true
		}
		if !e.Master && !e.Install && e.Status == emuAvailable {
			ports++
			if portCompatBlocked(e.Compat.State) {
				unsupported++
			}
		}
	}
	if !installed {
		return "PortMaster is not installed"
	}
	if ports == 0 {
		return "PortMaster installed · no ports yet"
	}
	summary := "PortMaster installed · " + pluralizeCount(ports, "port", "ports")
	if unsupported > 0 {
		// Say it up front, so nobody opens a port that cannot start.
		summary += fmt.Sprintf(" · %d cannot run here (32-bit only)", unsupported)
	}
	return summary
}

// renderPortsPanel draws the modal Ports list, scrolling with the cursor.
func renderPortsPanel(renderer *sdl.Renderer, config *Config) {
	if !portsPanel.open || renderer == nil {
		return
	}
	entries := portsPanelRows()
	if len(entries) == 0 {
		entries = []portEntry{{Name: "No ports yet", Detail: "Install PortMaster to add ports", Status: emuMissing}}
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

	const padX = OverlayPadX
	rowH := 8 + medH + 4 + smallH + 8

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	title, subtitle := "PortMaster Ports", portsSummary(entries)
	if portsPanel.catalog {
		title, subtitle = "PortMaster Catalogue", portCatalogSummary()
	}
	if portsPanel.opts {
		title, subtitle = portsPanel.optsFor.Name, portsPanel.optsSub
	}
	header := UIPanelHeader(title, subtitle, "packages")
	headerH := UIPanelHeaderBand(config, header, 14)
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

	drawOverlayScrim(renderer)

	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

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

		badge := portCompatBadge(e.Compat.State)
		badgeW := int32(0)
		if badge != "" {
			bw, _, _ := font.SizeUTF8(badge)
			badgeW = int32(bw)
		}
		label, labelColor := "", ColorTextSecondary()
		statusW := int32(0)
		if !e.Info {
			label, labelColor = emuStatusLabel(e.Status)
			if e.Install {
				label, labelColor = "Install", installHighlight()
			}
			if e.Catalog.Zip != "" && e.Status != emuAvailable {
				label, labelColor = "Install", installHighlight()
			}
			if portCompatBlocked(e.Compat.State) {
				label, labelColor = portCompatBadge(e.Compat.State), ColorToastWarn
			}
			lw, _, _ := font.SizeUTF8(label)
			statusW = int32(lw)
		}

		// The name gets every pixel the status and compatibility chip leave.
		// Without this budget a long port title ran through the chip and the
		// right-aligned status, then off the row entirely.
		name := ellipsize(medium, e.Name, portsRowNameWidth(r.W, badgeW, statusW))
		nw, _, _ := medium.SizeUTF8(name)
		renderText(renderer, config, medium, name, ColorTextPrimary(), r.X+16, r.Y+8)
		if badge != "" {
			// The chip verdict sits next to the (fitted) name, before the title
			// can push it off the row: it is the first thing to know about a port.
			badgeColor := ColorTextTertiary()
			if portCompatBlocked(e.Compat.State) {
				badgeColor = ColorToastWarn
			}
			renderText(renderer, config, font, badge, badgeColor, r.X+24+int32(nw), r.Y+10+(medH-smallH)/2)
		}
		if label != "" {
			renderText(renderer, config, font, label, labelColor, r.X+r.W-16-statusW, r.Y+8+(medH-smallH)/2)
		}
		if e.Detail != "" {
			renderText(renderer, config, font, ellipsize(font, e.Detail, r.W-32),
				ColorTextTertiary(), r.X+16, r.Y+8+medH+4)
		}
	}

	hints := []FooterHint{{"A", "Open"}, {"Y", "Options"}, {"X", "Catalogue"}, {"B", "Close"}}
	if len(entries) > 0 && entries[0].Install {
		hints = []FooterHint{{"A", "Install PortMaster"}, {"X", "Catalogue"}, {"B", "Close"}}
	}
	if portsPanel.catalog {
		hints = []FooterHint{{"A", "Install"}, {"X", "Installed"}, {"Y", "May run"}, {"R1", "Search"}, {"B", "Close"}}
	}
	if portsPanel.opts {
		hints = []FooterHint{{"A", "Select"}, {"B", "Back"}}
	}
	if portsPanel.scroll > 0 || portsPanel.scroll+int(visible) < len(entries) {
		hints = append([]FooterHint{{Button: "D-Pad", Label: fmt.Sprintf("Scroll %d-%d of %d",
			portsPanel.scroll+1, portsPanel.scroll+int(visible), len(entries))}}, hints...)
	}
	if pkgHubEnabled() && tbxCurrentScene(config) == "Packages" && !portsPanel.returnEmu {
		hints = append([]FooterHint{{Button: "L2/R2", Label: "Stores"}}, hints...)
	}
	hint := ellipsize(font, hintLine(hints...), panelW-2*padX)
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}

// portsRowNameWidth is the width left for a Ports row title after the row
// padding, the right-aligned status and the compatibility chip. Keeping the
// measurement in one place is what stops a long title from painting through
// those parts of the row.
func portsRowNameWidth(rowW, badgeW, statusW int32) int32 {
	w := rowW - 32 - statusW
	if badgeW > 0 {
		w -= badgeW + 8
	}
	if w < 0 {
		return 0
	}
	return w
}

