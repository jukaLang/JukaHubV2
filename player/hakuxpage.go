package main

// hakuxpage.go — hakuX on the Emulators page.
//
// hakux.go installs the package; this file is what the page shows and does with
// it: the one Xbox tile the handheld always lists, what A, X, Y and R1 do on it,
// the handheld half of the Xbox setup sheet (emuxbox.go draws it), and the
// background check that badges the tile when a newer release is out.
//
// The tile is honest about the device it is on (hakuxSupportFor): the Smart Pro
// is offered the install, the Brick Pro the same with an "untested" note, and
// every other handheld gets "Not supported" with the actual reason. On the tile:
//
//   - A opens the Xbox games in the Games Library once hakuX is installed, and
//     the setup sheet before that, so what an experimental port needs and does
//     is read before anything is downloaded;
//   - X installs or updates the package (L1 includes it in "Update all");
//   - Y opens the setup sheet: the package, the games folder, any firmware the
//     installed build's own settings name, and the in-game controls;
//   - R1 puts back what the last update replaced.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// hakuxCaveat is the one-line truth every hakuX surface carries.
	hakuxCaveat = "Experimental port: 1 GB of RAM, not every game runs, and 30 fps is a target, not a promise."
	// hakuxControls is how hakuX is driven inside a game.
	hakuxControls = "In a game: Menu opens hakuX's settings (screenshots too) · hold Start+Select about 1.2 s to exit."
)

// xboxLibraryLauncherHintFor is the Games Library's answer to an Xbox game with
// no launcher. It names the fix where the device is offered one, and the actual
// reason elsewhere, instead of the generic "no launcher for Xbox".
func xboxLibraryLauncherHintFor(device DeviceModel) string {
	if device == DeviceDevBuild {
		// Windows (DetectDevice's answer there): xemu plays Roms/XBOX, and the
		// Emulators page installs it.
		return "Install xemu: Emulators → xemu → X"
	}
	support := hakuxSupportFor(device)
	if support.Offer {
		return "Install hakuX: Emulators → Xbox → X"
	}
	reason := strings.TrimSpace(support.Reason)
	if reason == "" {
		reason = "this device has no hakuX build"
	}
	return "Xbox games need hakuX, which this device does not support — " + reason
}

// hakuxTile reports whether a tile is the handheld's Xbox tile.
func hakuxTile(e emuEntry) bool {
	return !IsWindows() && !e.Pinned && strings.TrimSpace(e.Core) == "" &&
		strings.EqualFold(strings.TrimSpace(e.Name), emuXboxEntryName)
}

// ── Games ───────────────────────────────────────────────────────────────────

// hakuxRomsDir is the card's Xbox games folder: an existing one in any
// spelling, else Roms/XBOX, the folder the package's launcher reads.
func hakuxRomsDir(romsRoot string) string {
	if entries, err := os.ReadDir(romsRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), hakuxRomsFolder) {
				return filepath.Join(romsRoot, e.Name())
			}
		}
	}
	return filepath.Join(romsRoot, hakuxRomsFolder)
}

// hakuxGameFile reports a disc image hakuX plays: an Xbox ISO (XISO).
func hakuxGameFile(name string) bool {
	if skipScanEntryName(name) {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".iso", ".xiso":
		return true
	}
	return false
}

// hakuxCountGames counts the disc images in the games folder and its direct
// subfolders, where a game kept with its extras usually sits.
func hakuxCountGames(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			if hakuxGameFile(e.Name()) {
				n++
			}
			continue
		}
		sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, s := range sub {
			if !s.IsDir() && hakuxGameFile(s.Name()) {
				n++
			}
		}
	}
	return n
}

// hakuxGamesNote is the games part of the tile's detail line.
func hakuxGamesNote(n int) string {
	switch {
	case n == 1:
		return "1 game"
	case n > 1:
		return fmt.Sprintf("%d games", n)
	}
	return "no games yet — put .iso files in Roms/" + hakuxRomsFolder
}

// ── Tile ────────────────────────────────────────────────────────────────────

// hakuxXboxEntry is the handheld's Xbox tile. folder is the Xbox launcher
// folder found under Emus/ ("" when there is none). A real launcher folder is
// listed as available on any device — it was put there on purpose — while an
// install is only offered where the port can run.
func hakuxXboxEntry(folder, romsRoot string, device DeviceModel) emuEntry {
	e := emuEntry{Name: emuXboxEntryName, Glyph: emuGlyphFor(emuXboxEntryName, "")}
	support := hakuxSupportFor(device)
	if folder == "" {
		if !support.Offer {
			e.Status = emuUnsupported
			e.Detail = "Xbox — no hakuX build for this device"
			e.Note = support.Reason
			return e
		}
		e.Status = emuMissing
		e.Detail = "hakuX (experimental) · A shows what it needs, X installs it from GitHub"
		if support.Untested != "" {
			e.Detail += " · untested on this model"
		}
		e.Note = hakuxCaveat
		return e
	}
	e.IconPath = emuFolderIcon(folder)
	if launcher := hakuxLauncherIn(folder); launcher != "" {
		e.Status = emuAvailable
		e.Launch = launcher
		if e.IconPath == "" {
			// Looked up once, as the folder scan does: on the handheld the
			// tiles no longer probe beside the launcher every frame.
			e.IconPath = sidecarIcon(launcher)
		}
	} else {
		e.Status = emuBroken
	}
	games := hakuxGamesNote(hakuxCountGames(hakuxRomsDir(romsRoot)))
	m, managed := hakuxReadManifest(folder)
	switch {
	case e.Status == emuBroken && support.Offer:
		e.Detail = folder + " has no launch.sh — X installs hakuX again"
	case e.Status == emuBroken:
		e.Detail = folder + " — no launcher"
	case managed:
		// Managed drives the L1 "Update all" hint, which only means something
		// where the install is offered (a card moved to a Brick keeps its
		// provenance file but cannot be updated there).
		e.Managed = support.Offer
		e.Version = m.Tag
		if support.Offer {
			e.KeptVersion = hakuxKeptTag(hakuxDevicePaths())
		}
		e.Detail = "hakuX (experimental) · " + games
	case support.Offer:
		e.Detail = folder + " · installed outside JukaHub, X updates it · " + games
	default:
		e.Detail = folder + " · " + games
	}
	if support.Untested != "" {
		e.Detail += " · untested on this model"
	}
	if !support.Offer {
		e.Note = support.Reason
	}
	return e
}

// hakuxHintA is the A label on the Xbox tile.
func hakuxHintA(e emuEntry) string {
	switch e.Status {
	case emuUnsupported:
		return "Details"
	case emuAvailable:
		return "Games"
	}
	return "Set up"
}

// hakuxHintX is the X label on the Xbox tile: Install or Update where the port
// is offered, nothing elsewhere.
func hakuxHintX(e emuEntry) string {
	if !hakuxOffered() || e.Status == emuUnsupported {
		return ""
	}
	if e.Status == emuAvailable {
		return "Update"
	}
	return "Install"
}

// ── Actions ─────────────────────────────────────────────────────────────────

// hakuxActivate is A on the Xbox tile.
func hakuxActivate(e emuEntry) {
	switch e.Status {
	case emuUnsupported:
		PlayNavSound()
		ShowToast("Xbox is not supported here — "+e.Note, ToastKindInfo)
	case emuAvailable:
		hakuxOpenGames()
	default:
		emuOpenXboxSetup()
	}
}

// hakuxOpenGames opens the Games Library on its Xbox row. With no Xbox games on
// the card the library still opens, and the toast says where they go.
func hakuxOpenGames() {
	openGameLibraryPanel()
	for i, system := range currentGameSystems() {
		if system.Name == emuXboxEntryName && len(system.Games) > 0 {
			gameLib.system = i
			gameLib.cursor, gameLib.scroll = 0, 0
			gameLib.focus = 1
			return
		}
	}
	ShowToast("No Xbox games yet — put .iso (XISO) images in "+hakuxRomsDir(deviceRomsRoot), ToastKindInfo)
}

// hakuxStartInstall installs or updates hakuX off the event thread, reporting
// through the install bar, the tile's "Installing…" pill and a toast.
func hakuxStartInstall() {
	device := hakuxDevice()
	support := hakuxSupportFor(device)
	if IsWindows() || !support.Offer {
		PlayNavSound()
		ShowToast("Xbox is not supported here — "+support.Reason, ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to install hakuX", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	paths := hakuxDevicePaths()
	// The same question hakuxInstall asks: an Xbox launcher on the card itself.
	if folder, _, _ := hakuxInstalled([]string{filepath.Join(paths.SDRoot, "Emus")}); hakuxLauncherIn(folder) != "" {
		installBarBeginLabelled(hakuxName, "Updating hakuX (Xbox)")
	} else {
		installBarBeginLabelled(hakuxName, "Installing hakuX (Xbox)")
	}
	installBarPhase(installPhaseResolving)
	emuSetInstalling(emuXboxEntryName)
	go func() {
		defer endPatchOp()
		defer emuSetInstalling("")
		ctx, cancel := context.WithTimeout(installBarRunContext(), emuWinInstallTimeout)
		defer cancel()
		result, err := hakuxInstall(ctx, emuWinClient(), paths, device, emuInstallBarReport)
		hakuxFinish(result, err)
	}()
}

// hakuxFinish reports one install's outcome.
func hakuxFinish(result hakuxInstallResult, err error) {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			installBarAbort()
			ShowToast("hakuX install cancelled", ToastKindInfo)
			return
		}
		msg := emuFriendlyError(err)
		installBarFail(installCardDetail(msg))
		ShowToast("hakuX: "+msg, ToastKindError)
		logWarn("emulators", "hakuX install failed: %v", err)
		return
	}
	installBarPhase(installPhaseDone)
	installBarDone()
	invalidateEmulatorCache()
	emuForgetUpdate(emuXboxEntryName)
	if emuXboxSheetOpen() {
		emuXboxRescan()
	}
	// One toast: the slot holds one message, so a second would replace the
	// first at once.
	edited := ""
	if result.Edited > 0 {
		edited = fmt.Sprintf(" · %s you had changed replaced, your copies are in %s", pluralizeCount(result.Edited, "file", "files"), result.EditedDir)
	}
	if !result.Current && result.Restored == 0 {
		trophyEvent(tkEmuInstall) // installed or updated; a repair of the same version is neither
	}
	switch {
	case result.Current:
		ShowToast("hakuX is already the newest version ("+result.Tag+")", ToastKindInfo)
	case result.Restored > 0:
		PlaySuccessSound()
		ShowToast("hakuX "+result.Tag+" repaired · "+pluralizeCount(result.Restored, "missing file", "missing files")+" put back", ToastKindSuccess)
	case result.Updated:
		PlaySuccessSound()
		note := "hakuX updated to " + result.Tag
		if result.Kept {
			note += " · previous version kept (R1 rolls back)"
		}
		ShowToast(note+edited, ToastKindSuccess)
	default:
		PlaySuccessSound()
		ShowToast("hakuX "+result.Tag+" installed · put Xbox games (.iso) in Roms/"+hakuxRomsFolder+edited, ToastKindSuccess)
	}
}

// hakuxStartRollback is R1 on the Xbox tile: the files the last update
// replaced or set aside come back. On an SD card a rename can take a moment
// and a copy (two file systems) much longer, so it runs off the event thread
// like the install, inside the patch lock so it cannot meet an install half
// way.
func hakuxStartRollback() {
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	paths := hakuxDevicePaths()
	ShowToast("Rolling hakuX back…", ToastKindInfo)
	go func() {
		defer endPatchOp()
		info, err := hakuxRollbackCard(paths)
		if err != nil {
			ShowToast("hakuX: "+err.Error(), ToastKindError)
			return
		}
		invalidateEmulatorCache()
		emuForgetUpdate(emuXboxEntryName)
		if emuXboxSheetOpen() {
			emuXboxRescan()
		}
		PlaySuccessSound()
		label := strings.TrimSpace(info.Tag)
		if label == "" {
			label = "the files the update replaced"
		}
		ShowToast("hakuX rolled back to "+label+" · X installs the newest again", ToastKindSuccess)
	}()
}

// hakuxManagedOnCard reports a hakuX JukaHub installed on this device, which
// "Update all" (L1) therefore brings forward.
func hakuxManagedOnCard() bool {
	if !hakuxOffered() {
		return false
	}
	folder, _, managed := hakuxInstalled(deviceEmuRoots)
	return managed && hakuxLauncherIn(folder) != ""
}

// hakuxRunUpdate is L1's share of updating every installed emulator: the same
// install X runs, reported through the bar L1 already owns.
func hakuxRunUpdate(ctx context.Context) (hakuxInstallResult, error) {
	return hakuxInstall(ctx, emuWinClient(), hakuxDevicePaths(), hakuxDevice(), emuInstallBarReport)
}

// hakuxUpdateFor is the background check's question for the handheld: is
// there a newer package than the one JukaHub installed? Nothing is asked of
// GitHub when hakuX is not installed by JukaHub on a device it is offered on.
func hakuxUpdateFor(ctx context.Context, client *http.Client) (emuWinRelease, bool, error) {
	if !hakuxOffered() {
		return emuWinRelease{}, false, nil
	}
	folder, m, managed := hakuxInstalled(deviceEmuRoots)
	if !managed || hakuxLauncherIn(folder) == "" {
		return emuWinRelease{}, false, nil
	}
	rel, err := hakuxFetchRelease(ctx, client)
	if err != nil {
		return emuWinRelease{}, false, err
	}
	return rel.Release, hakuxNewer(m, rel.Release), nil
}

// ── Setup sheet ─────────────────────────────────────────────────────────────

// hakuxFirmwareKeys maps the names an Xbox emulator's settings may give its
// firmware files to the checklist's keys: xemu.toml's [sys.files] names, and
// the spellings key = value configs use.
var hakuxFirmwareKeys = map[string]string{
	"bootrom_path":  "bootrom",
	"bootrom":       "bootrom",
	"mcpx_path":     "bootrom",
	"mcpx":          "bootrom",
	"flashrom_path": "flashrom",
	"flashrom":      "flashrom",
	"flash_path":    "flashrom",
	"flash":         "flashrom",
	"bios_path":     "flashrom",
	"bios":          "flashrom",
	"hdd_path":      "hdd",
	"hdd_image":     "hdd",
	"hdd":           "hdd",
	"eeprom_path":   "eeprom",
	"eeprom":        "eeprom",
}

// hakuxLooksLikePath tells a file setting from a switch that shares its name
// ("hdd = 1"): a path has a folder or an extension.
func hakuxLooksLikePath(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && (strings.ContainsAny(v, `/\`) || strings.Contains(path.Base(v), "."))
}

// hakuxConfigFirmware reads the firmware paths one config file names: a TOML
// file through [sys.files] as xemu writes it, anything else as key = value
// lines under any section.
func hakuxConfigFirmware(name, text string) map[string]string {
	out := map[string]string{}
	take := func(key, value string) {
		k, ok := hakuxFirmwareKeys[strings.ToLower(strings.TrimSpace(key))]
		if !ok || !hakuxLooksLikePath(value) {
			return
		}
		if _, dup := out[k]; !dup {
			out[k] = strings.TrimSpace(value)
		}
	}
	if strings.EqualFold(filepath.Ext(name), ".toml") {
		// In a fixed order, so a section that names one file under two keys
		// always reports the same one.
		files := emuXemuTomlFiles(text)
		keys := make([]string, 0, len(files))
		for key := range files {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			take(key, files[key])
		}
		return out
	}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		take(line[:eq], emuTomlUnquote(strings.TrimSpace(line[eq+1:])))
	}
	return out
}

// hakuxConfigExts are the files read for firmware paths.
var hakuxConfigExts = map[string]bool{".toml": true, ".ini": true, ".cfg": true, ".conf": true}

// hakuxConfigDirs are the folders of the installed package to look for its
// settings in: the launcher folder, then the Data/ folders it installed (or,
// for a copy JukaHub did not install, the Data/ folders named after it).
func hakuxConfigDirs(folder string, m hakuxManifest, managed bool) []string {
	dirs := []string{folder}
	seen := map[string]bool{strings.ToLower(folder): true}
	add := func(dir string) {
		key := strings.ToLower(dir)
		if seen[key] || len(dirs) >= 6 {
			return
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}
	data := filepath.Join(filepath.Dir(filepath.Dir(folder)), "Data")
	if managed {
		for _, f := range m.Files {
			if parts := strings.SplitN(f.Path, "/", 3); len(parts) == 3 && parts[0] == "Data" {
				add(filepath.Join(data, parts[1]))
			}
		}
		return dirs
	}
	if entries, err := os.ReadDir(data); err == nil {
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			if e.IsDir() && (strings.Contains(lower, "xbox") || strings.Contains(lower, "hakux")) {
				add(filepath.Join(data, e.Name()))
			}
		}
	}
	return dirs
}

// hakuxFindFirmwareConfig looks, a few levels deep and a few hundred entries at
// most per folder, for the first small config file that names firmware files,
// and returns it with those paths made absolute against its folder.
func hakuxFindFirmwareConfig(dirs []string) (string, map[string]string) {
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		found := ""
		var named map[string]string
		visited := 0
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			visited++
			if visited > 400 {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if strings.Count(strings.TrimPrefix(p, dir), string(filepath.Separator)) > 3 {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !hakuxConfigExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > 64<<10 {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			files := hakuxConfigFirmware(p, string(raw))
			if len(files) == 0 {
				return nil
			}
			for key, value := range files {
				if !filepath.IsAbs(value) {
					files[key] = filepath.Join(filepath.Dir(p), filepath.FromSlash(value))
				}
			}
			found, named = p, files
			return filepath.SkipAll
		})
		if found != "" {
			return found, named
		}
	}
	return "", nil
}

// hakuxFirmwareRows are the checklist's firmware rows. hakuX 1.0 and later run
// games on their own Xbox kernel and need no BIOS, MCPX ROM or HDD image; the
// xemu-based builds before them did, and nothing JukaHub can read says which
// core a port was built from. So the rows follow what the installed build's own
// settings name: each file it names, with what was found there, or one row
// saying none is asked for.
func hakuxFirmwareRows(folder string, m hakuxManifest, managed bool, report *emuXboxReport) []emuXboxFile {
	none := emuXboxFile{Key: "firmware", Label: "Xbox BIOS, MCPX ROM, HDD image"}
	if folder == "" {
		none.Want = "Checked once hakuX is installed (hakuX 1.0 and later need none)"
		none.Note = "Checked after install"
		return []emuXboxFile{none}
	}
	cfg, named := hakuxFindFirmwareConfig(hakuxConfigDirs(folder, m, managed))
	if len(named) == 0 {
		none.Want = "hakuX's settings name none — hakuX 1.0 and later boot games without them"
		none.Note = "None asked for"
		return []emuXboxFile{none}
	}
	report.ConfigPath = cfg
	items := []emuXboxFile{
		{Key: "bootrom", Label: "MCPX boot ROM", Required: true},
		{Key: "flashrom", Label: "Flash ROM (BIOS)", Required: true},
		{Key: "hdd", Label: "Hard disk image", Required: true},
		{Key: "eeprom", Label: "EEPROM (optional)"},
	}
	var out []emuXboxFile
	for _, f := range items {
		p, ok := named[f.Key]
		if !ok {
			continue
		}
		f.Path = p
		emuXboxCheckFile(&f)
		if f.Problem == "listed but not found" {
			if !f.Required {
				// hakuX's settings name it, but an emulator creates a missing
				// EEPROM itself: nothing to supply.
				f.Path, f.Problem = "", ""
				f.Want, f.Note = "Created at "+p+" if missing", "Optional"
			} else {
				f.Problem = "missing — put it at this path"
			}
		}
		if report.Folder == "" && f.Required {
			report.Folder = filepath.Dir(p)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return []emuXboxFile{none}
	}
	return out
}

// hakuxSheetReport is the handheld's Xbox checklist: the package, the games
// folder and whatever firmware the installed build asks for, plus the lines the
// sheet prints under them (controls, what to expect, a pending update).
func hakuxSheetReport() emuXboxReport {
	support := hakuxSupportFor(hakuxDevice())
	report := emuXboxReport{Emulator: hakuxName, Supported: support.Offer}
	folder, m, managed := hakuxInstalled(deviceEmuRoots)
	launcher := hakuxLauncherIn(folder)
	report.Installed = launcher != ""
	pkg := emuXboxFile{Key: "package", Label: "hakuX (Smart Pro port)", Want: "X installs it from github.com/" + hakuxRepo, Required: true}
	switch {
	case launcher != "":
		pkg.Path = launcher
		pkg.Note = "installed outside JukaHub"
		if managed {
			pkg.Note = m.Tag
			if m.Verified != "" {
				pkg.Note += " · checksum verified"
			}
		}
	case folder != "":
		pkg.Path = folder
		pkg.Problem = "no launch.sh — X installs it again"
	case !support.Offer:
		pkg.Want = support.Reason
	}
	romsDir := hakuxRomsDir(deviceRomsRoot)
	games := emuXboxFile{Key: "games", Label: "Xbox games", Want: "Put .iso (XISO) images in " + romsDir, Required: true}
	if n := hakuxCountGames(romsDir); n > 0 {
		games.Path = romsDir
		games.Note = hakuxGamesNote(n)
	}
	report.Files = append([]emuXboxFile{pkg, games}, hakuxFirmwareRows(folder, m, managed, &report)...)
	if release, ok := emuWinUpdateKnown(emuXboxEntryName); ok && report.Installed {
		report.Notes = append(report.Notes, "Update available: "+release.Tag+" — X installs it, R1 on the tile undoes it")
	}
	report.Notes = append(report.Notes, hakuxControls, hakuxCaveat)
	if support.Untested != "" {
		report.Notes = append(report.Notes, support.Untested)
	}
	report.Configured = report.ready()
	return report
}
