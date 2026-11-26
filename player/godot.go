package main

// godot.go — Godot games on the handheld, through the TrimUI Smart Pro Godot
// port.
//
// Godot exports a game as a .pck: its scenes, scripts and assets, with no
// engine inside. On a small Linux handheld the pack plays in an engine built
// for the device (FRT, Godot 3 over SDL2, is the one the PortMaster porters
// use). For the TrimUI Smart Pro that work exists already: the TrimUI Smart
// Pro Godot port (github.com/ogregorio/trimui-tsp-godot-port, after the Godot
// launcher of dokoma's Kiwi add-on collection, github.com/dokoma/kiwi) is an
// Emus/GODOT launcher folder and a Roms/GODOT games folder, the layout MainUI
// itself reads. A game is a .pck in Roms/GODOT, optionally with a .runtime
// (which engine), a .png (its picture) and a .gptk (its pad-to-keys map) of
// the same name beside it.
//
// JukaHub does not rebuild that port. It:
//
//   - lists Roms/GODOT as the Godot shelf of the Games library: .pck files
//     only, since the side files are not games, and the .png is the game's
//     cover (romLocalCoverPath);
//   - starts a game the way MainUI would, through the port's launch.sh;
//   - shows one Godot tile on the Emulators page. X (or A) installs the port
//     from GitHub: the repository's zip, its Emus/GODOT laid over the card's
//     (emuOverlay), its own Roms/GODOT files copied in beside the player's
//     games and never over one. Once it is there A opens the Godot games and X
//     fetches the port again;
//   - reads each game's engine version from its .pck header. It goes into the
//     log at every start, and when a game ends at once the launch report says
//     whether the port carries an engine of that version (a Godot 4 game on a
//     port of Godot 3 engines ends on a black screen).
//
// The port was made for the Smart Pro. The Brick, Brick Pro and Hammer have the
// same chip and GPU and are offered it as untested; so is the Smart Pro S, whose
// chip and GPU differ (A523, Mali). Nothing is offered on a PC.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// godotSystemName is the Games library's shelf, godotEntryName the tile.
	godotSystemName = "Godot"
	godotEntryName  = "Godot"
	// godotFolderName is the port's folder under Emus and Roms.
	godotFolderName = "GODOT"
	// godotPackExt is a Godot game.
	godotPackExt = ".pck"
	// godotGlyph is the tile's icon until the port's own picture is there.
	godotGlyph = "gamepad"
	// godotMarkerName, in JukaHub's data folder, says where the port came from.
	godotMarkerName = "godot-port.json"
	// godotStageName is the install's work folder, hidden under Emus so every
	// move is a rename on the card.
	godotStageName = ".jukahub-godot"
	// godotMaxDownload bounds one repository zip (Kiwi carries more than the
	// Godot launcher).
	godotMaxDownload = int64(1 << 30)
	// godotInstallTimeout bounds one install on handheld Wi-Fi.
	godotInstallTimeout = 30 * time.Minute
	// godotScanEntries bounds the look through the port's folder for engines.
	godotScanEntries = 400
	// godotCopyMax bounds the files of the port's own Roms/GODOT copied in.
	godotCopyMax = 2000
	// godotTreeMax bounds the port's files listed, hashed or kept by one install.
	godotTreeMax = 4000
	// godotKeptName, in Emus/GODOT, holds the files an update replaced that
	// the player had changed (hidden, so no menu lists it).
	godotKeptName = ".jukahub-replaced"
	// godotMarkerMax bounds the read of godotMarkerName.
	godotMarkerMax = 1 << 20
	// godotPackHeaderSize is how much of a .pck names the engine that made
	// it: "GDPC", then the pack format and the engine's major, minor and
	// patch numbers, each a little-endian uint32.
	godotPackHeaderSize = 20
	// godotLauncherReadMax bounds the read of a launcher for what it needs.
	godotLauncherReadMax = 64 << 10
)

// godotSource is a repository the port is fetched from.
type godotSource struct {
	Owner string
	Repo  string
	// Standalone is false for a collection whose launcher may lean on the
	// rest of it (Kiwi mounts its own folder at /kiwi): its Godot folder is
	// used only when its launcher does not.
	Standalone bool
}

// godotSources are tried in order: the standalone port, then the Kiwi
// collection it was made from.
var godotSources = []godotSource{
	{Owner: "ogregorio", Repo: "trimui-tsp-godot-port", Standalone: true},
	{Owner: "dokoma", Repo: "kiwi"},
}

func (s godotSource) name() string { return s.Owner + "/" + s.Repo }

// urls are the repository's zip: its default branch through GitHub's API,
// then the main and master branches straight from codeload (the API limits
// unsigned callers to 60 requests an hour).
func (s godotSource) urls() []string {
	base := s.Owner + "/" + s.Repo
	return []string{
		"https://api.github.com/repos/" + base + "/zipball",
		"https://codeload.github.com/" + base + "/zip/refs/heads/main",
		"https://codeload.github.com/" + base + "/zip/refs/heads/master",
	}
}

// ── Where it is offered ─────────────────────────────────────────────────────

// godotSupport is what the Godot tile offers on one handheld.
type godotSupport struct {
	Offer bool
	// Untested is the caveat for a model the port was not made on.
	Untested string
}

// godotSupportFor decides per model (hakuxDevice tells a Smart Pro S from a
// Smart Pro by its CPU).
func godotSupportFor(device DeviceModel) godotSupport {
	switch device {
	case DeviceTrimuiSmartPro:
		return godotSupport{Offer: true}
	case DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return godotSupport{Offer: true, Untested: "Made for the Smart Pro and untested on this model (the same A133P chip and GPU, a 4:3 screen)."}
	case DeviceTrimuiSmartProS:
		return godotSupport{Offer: true, Untested: "Made for the original Smart Pro and untested on the Smart Pro S (an A523 chip and a Mali GPU)."}
	}
	return godotSupport{}
}

// godotOffered reports whether this device gets the Godot tile.
func godotOffered() bool {
	return !IsWindows() && runtime.GOARCH == "arm64" && godotSupportFor(hakuxDevice()).Offer
}

// ── The port on the card ────────────────────────────────────────────────────

// godotIsFolder reports a folder name the Godot tile and shelf stand for.
func godotIsFolder(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), godotFolderName)
}

// godotLauncherIn is a port folder's launch script by the rules of every Emus
// folder (emuFolderLauncher), or "".
func godotLauncherIn(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return ""
	}
	return emuFolderLauncher(folder, emuFolderConfig(folder))
}

// godotPortFolder finds the port's launcher folder under the Emus roots, the
// card first. A folder with a launcher wins over one without.
func godotPortFolder(emuRoots []string) string {
	broken := ""
	for _, root := range emuRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range entries {
			if !de.IsDir() || !godotIsFolder(de.Name()) {
				continue
			}
			folder := filepath.Join(root, de.Name())
			if godotLauncherIn(folder) != "" {
				return folder
			}
			if broken == "" {
				broken = folder
			}
		}
	}
	return broken
}

// godotTile reports the Emulators page's Godot tile. Its glyph tells it from
// a plain GODOT folder row, which a model without the tile still lists.
func godotTile(e emuEntry) bool {
	return !IsWindows() && !e.Pinned && strings.TrimSpace(e.Core) == "" && e.Glyph == godotGlyph &&
		strings.EqualFold(strings.TrimSpace(e.Name), godotEntryName)
}

// godotEmuEntry is the Godot tile for the port folder found (folder "" when
// there is none).
func godotEmuEntry(folder string, device DeviceModel) emuEntry {
	e := emuEntry{Name: godotEntryName, Glyph: godotGlyph}
	support := godotSupportFor(device)
	if folder == "" {
		e.Status = emuMissing
		e.Detail = "Godot games (.pck in Roms/" + godotFolderName + ") · X installs the TrimUI Smart Pro Godot port from GitHub"
		if support.Untested != "" {
			e.Detail += " · untested on this model"
		}
		e.Note = support.Untested
		return e
	}
	e.Folder = folder
	e.IconPath = emuFolderIcon(folder)
	launcher := godotLauncherIn(folder)
	if launcher == "" {
		e.Status = emuBroken
		e.Detail = folder + " — no launcher · X installs the Godot port again"
		return e
	}
	e.Status = emuAvailable
	e.Launch = launcher
	e.Detail = "Godot port · " + folder
	if m, ok := godotReadMarker(); ok && m.Source != "" {
		e.Detail = "Godot port from " + m.Source + " · " + folder
	}
	if engines, _ := godotPortEngines(folder); len(engines) > 0 {
		e.Detail += " · Godot " + godotVersionsText(engines) + " games"
	}
	if e.IconPath == "" {
		e.IconPath = sidecarIcon(launcher)
	}
	return e
}

// godotHintA is the A label on the Godot tile.
func godotHintA(e emuEntry) string {
	if e.Status == emuAvailable {
		return "Games"
	}
	return "Install"
}

// godotHintX is the X label on the Godot tile.
func godotHintX(e emuEntry) string {
	if e.Status == emuAvailable {
		return "Update"
	}
	return "Install"
}

// godotLauncherHint is what a Godot game with nothing to start it says.
func godotLauncherHint() string {
	if godotOffered() {
		return "Godot games need the Godot port — Emulators → Godot → X installs it"
	}
	return "Godot games play on a TrimUI handheld, through the TrimUI Smart Pro Godot port"
}

// godotRomsDir is the library's Godot folder, or "" when there is none. With
// create, a card without one gets Roms/GODOT in the first Roms folder there is.
func godotRomsDir(create bool) string {
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
			if sys, _, ok := romFolderSystem(e.Name(), nil); ok && sys.Name == godotSystemName {
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
		dir := filepath.Join(root, godotFolderName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logWarn("godot", "cannot make %s: %v", dir, err)
			return ""
		}
		return dir
	}
	return ""
}

// godotPackInFolder reports whether name, in a folder of system, is a Godot
// game. The library-change hash counts it (boxart_auto.go): .pck is no
// cartridge type elsewhere.
func godotPackInFolder(system, name string) bool {
	return system == godotSystemName && strings.EqualFold(filepath.Ext(name), godotPackExt)
}

// ── Actions ─────────────────────────────────────────────────────────────────

// godotActivate is A on the Godot tile: the games once the port is there,
// the install before that.
func godotActivate(e emuEntry) {
	if e.Status == emuAvailable {
		godotOpenGames()
		return
	}
	godotStartInstall()
}

// godotOpenGames opens the Games library on its Godot shelf. With no Godot
// games yet the library still opens, and the toast says where they go.
func godotOpenGames() {
	where := godotRomsDir(false)
	if where == "" {
		where = "Roms/" + godotFolderName
	}
	openGameLibraryOnSystem(godotSystemName, "No Godot games yet — put .pck files in "+where)
}

// godotOpen is the Home search entry: the Godot games when the port is here,
// else the Emulators page on the Godot tile, whose X installs it.
func godotOpen() {
	if godotLauncherIn(godotPortFolder(deviceEmuRoots)) != "" {
		godotOpenGames()
		return
	}
	openEmulatorPanel()
	entries := emulatorEntries()
	for i, e := range entries {
		if godotTile(e) {
			emuPanelSelect(entries, i)
			break
		}
	}
	ShowToast("Godot games need the Godot port · X installs it from GitHub", ToastKindInfo)
}

// godotStartInstall installs (or fetches again) the port off the event
// thread, reporting through the install bar and a toast.
func godotStartInstall() {
	support := godotSupportFor(hakuxDevice())
	if IsWindows() || runtime.GOARCH != "arm64" || !support.Offer {
		PlayNavSound()
		ShowToast("The Godot port runs on TrimUI handhelds only", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast("No internet connection — connect to install the Godot port", ToastKindWarning)
		return
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	emus := filepath.Join(filepath.Dir(deviceRomsRoot), "Emus")
	if godotLauncherIn(godotPortFolder([]string{emus})) != "" {
		installBarBeginLabelled(godotEntryName, "Updating the Godot port")
	} else {
		installBarBeginLabelled(godotEntryName, "Installing the Godot port")
	}
	installBarPhase(installPhaseResolving)
	// This run's context, taken before anything else can start a run.
	runCtx := installBarRunContext()
	emuSetInstalling(godotEntryName)
	if support.Untested != "" {
		ShowToast("Installing the Godot port · "+support.Untested, ToastKindInfo)
	}
	go func() {
		defer endPatchOp()
		defer emuSetInstalling("")
		ctx, cancel := context.WithTimeout(runCtx, godotInstallTimeout)
		defer cancel()
		res, err := godotInstall(ctx, emuWinClient(), emus, emuInstallBarReport)
		godotFinish(res, err)
	}()
}

// godotFinish reports one install's outcome.
func godotFinish(res godotInstallResult, err error) {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			installBarAbort()
			ShowToast("Godot port install cancelled", ToastKindInfo)
			return
		}
		msg := emuFriendlyError(err)
		installBarFail(installCardDetail(msg))
		ShowToast("Godot: "+msg, ToastKindError)
		logWarn("godot", "install failed: %v", err)
		return
	}
	installBarPhase(installPhaseDone)
	installBarDone()
	invalidateEmulatorCache()
	trophyEvent(tkEmuInstall)
	logInfo("godot", "port from %s in %s (engines: %s; %d files added to the games folder)",
		res.Source, res.Folder, godotVersionsText(res.Engines), res.Added)
	msg := "Godot is ready — put .pck games in " + res.Roms
	if res.Updated {
		msg = "Godot port updated"
	}
	if res.Added > 0 {
		msg += " · " + pluralizeCount(res.Added, "file", "files") + " added to " + filepath.Base(res.Roms)
	}
	if res.Kept > 0 {
		// One toast: the slot holds one message.
		msg += " · " + pluralizeCount(res.Kept, "file", "files") + " you had changed kept in " + res.KeptDir
	}
	ShowToast(msg, ToastKindSuccess)
	tbxPost(func() {
		if gameLib.open {
			refreshGameLibrary()
		}
	})
}

// ── Install ─────────────────────────────────────────────────────────────────

// godotInstallResult is what one install did.
type godotInstallResult struct {
	Source  string // the repository the port came from
	Folder  string // the card's launcher folder
	Roms    string // the card's Godot games folder
	Updated bool   // a port with a launcher was there before
	Added   int    // files of the port's own Roms/GODOT copied in
	Kept    int    // replaced files the player had changed, set aside
	KeptDir string // where they were set aside
	Engines []godotVersion
}

// errGodotLayout is a download that holds no usable Emus/GODOT: the one
// failure that makes the next source worth its download.
var errGodotLayout = errors.New("the Godot port's download is laid out in a way JukaHub does not know")

// godotTryNextSource reports a failure the next source may not have: the
// repository is gone (404, 410, 451) or its zip holds no usable launcher. A
// cut-off download or GitHub's rate limit would only cost the next, larger
// download as well.
func godotTryNextSource(err error) bool {
	if errors.Is(err, errGodotLayout) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "HTTP 410") || strings.Contains(msg, "HTTP 451")
}

// godotInstall fetches the port from the first source that has it and puts
// it on the card under emus. Off the main thread.
func godotInstall(ctx context.Context, client *http.Client, emus string, progress emuWinProgress) (godotInstallResult, error) {
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, max(total, 0))
		}
	}
	stage := filepath.Join(emus, godotStageName)
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return godotInstallResult{}, err
	}
	// The first source's failure is the one reported: the standalone port is
	// what was asked for, and a fallback's own problem would only confuse.
	var firstErr error
	for _, src := range godotSources {
		dir := filepath.Join(stage, src.Repo)
		portDir, portRoms, err := godotFetchSource(ctx, client, src, dir, report)
		if err == nil {
			return godotPlace(src, portDir, portRoms, emus, filepath.Join(stage, "backup"), report)
		}
		if ctx.Err() != nil {
			return godotInstallResult{}, ctx.Err()
		}
		logWarn("godot", "%s: %v", src.name(), err)
		// Its unpacked tree goes before the next one is unpacked beside it.
		_ = os.RemoveAll(dir)
		if firstErr == nil {
			firstErr = err
		}
		if !godotTryNextSource(err) {
			break
		}
	}
	if firstErr == nil {
		firstErr = errors.New("no Godot port could be downloaded")
	}
	return godotInstallResult{}, firstErr
}

// godotFetchSource downloads and unpacks one source into dir and returns its
// Emus/GODOT folder and, when it has one, its Roms/GODOT folder.
func godotFetchSource(ctx context.Context, client *http.Client, src godotSource, dir string, report emuWinProgress) (portDir, portRoms string, err error) {
	zipPath := dir + ".zip"
	defer os.Remove(zipPath)
	var dlErr error
	for _, u := range src.urls() {
		// A phase with no bytes starts the bar's counters over
		// (emuInstallBarReport).
		report(installPhaseDownload, 0, 0)
		_, dlErr = emuDownload(ctx, client, u, zipPath, godotMaxDownload, func(done, total int64) { report("", done, total) })
		if dlErr == nil {
			break
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		logDebug("godot", "%s: %v", u, dlErr)
	}
	if dlErr != nil {
		return "", "", dlErr
	}
	report(installPhaseExtract, 0, 0)
	if err := emuExtractZip(ctx, zipPath, dir, func(done, total int64) { report("", done, total) }); err != nil {
		return "", "", err
	}
	_ = os.Remove(zipPath)
	root := emuArchiveContentRoot(dir)
	portDir = godotFindPath(root, "Emus", godotFolderName)
	launcher := godotLauncherIn(portDir)
	if launcher == "" {
		// The short sentence first: the install card shows the first line.
		return "", "", fmt.Errorf("%w (%s has no Emus/%s launcher)", errGodotLayout, src.name(), godotFolderName)
	}
	if !src.Standalone && godotLauncherNeedsRest(launcher) {
		return "", "", fmt.Errorf("%w (the Godot launcher of %s needs the rest of that collection)", errGodotLayout, src.name())
	}
	return portDir, godotFindPath(root, "Roms", godotFolderName), nil
}

// godotFindPath is root/<names…> matched without regard to case, or the same
// one folder further down (a wrapper folder inside the archive's wrapper); ""
// when there is none.
func godotFindPath(root string, names ...string) string {
	if p := godotDescend(root, names); p != "" {
		return p
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && !skipScanEntryName(e.Name()) {
			if p := godotDescend(filepath.Join(root, e.Name()), names); p != "" {
				return p
			}
		}
	}
	return ""
}

// godotDescend walks names down from dir, one folder each.
func godotDescend(dir string, names []string) string {
	for _, want := range names {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		next := ""
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), want) {
				next = filepath.Join(dir, e.Name())
				break
			}
		}
		if next == "" {
			return ""
		}
		dir = next
	}
	return dir
}

// godotLauncherNeedsRest reports a launcher that names Kiwi's /kiwi mount,
// which only the whole collection sets up.
func godotLauncherNeedsRest(launcher string) bool {
	f, err := os.Open(launcher)
	if err != nil {
		return true
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, godotLauncherReadMax))
	return strings.Contains(string(head), "/kiwi")
}

// godotPlace lays the unpacked port over the card's Emus/GODOT (files it
// replaces wait in backup), sets aside the replaced files the player had
// changed, marks its programs runnable and copies the port's own Roms/GODOT
// files the card does not have yet.
func godotPlace(src godotSource, portDir, portRoms, emus, backup string, report emuWinProgress) (godotInstallResult, error) {
	res := godotInstallResult{Source: src.name()}
	target := godotPortFolder([]string{emus})
	res.Updated = godotLauncherIn(target) != ""
	if target == "" {
		target = filepath.Join(emus, godotFolderName)
	}
	report(installPhaseExtract, 0, 0)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return res, err
	}
	// The port's files, listed before they move: their hashes are how the
	// next update tells the player's own edits from the port's files.
	rels := godotTreeFiles(portDir)
	if _, err := emuOverlay(portDir, target, backup); err != nil {
		return res, err
	}
	godotMarkRunnable(target)
	if godotLauncherIn(target) == "" {
		return res, fmt.Errorf("%s has no launcher after the install", target)
	}
	res.Folder = target
	prev, _ := godotReadMarker()
	if kept, dir := godotKeepEdited(backup, target, prev.Files); kept > 0 {
		res.Kept, res.KeptDir = kept, dir
		if rel, err := filepath.Rel(filepath.Dir(emus), dir); err == nil {
			res.KeptDir = rel
		}
	}
	files := make(map[string]string, len(rels))
	for _, rel := range rels {
		if sum, err := godotFileHash(filepath.Join(target, rel)); err == nil {
			files[filepath.ToSlash(rel)] = sum
		}
	}
	res.Roms = godotRomsDir(true)
	if portRoms != "" && res.Roms != "" {
		res.Added = godotCopyMissing(portRoms, res.Roms)
	}
	if res.Roms == "" {
		res.Roms = filepath.Join(filepath.Dir(deviceRomsRoot), "Roms", godotFolderName)
	}
	res.Engines, _ = godotPortEngines(target)
	godotWriteMarker(godotMarker{Source: res.Source, Installed: time.Now().UTC(), Files: files})
	return res, nil
}

// godotTreeFiles lists the regular files under dir as relative paths, at most
// godotTreeMax of them.
func godotTreeFiles(dir string) []string {
	var rels []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if len(rels) >= godotTreeMax {
			return filepath.SkipAll
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			rels = append(rels, rel)
		}
		return nil
	})
	return rels
}

// godotFileHash is a file's SHA-256, in hex.
func godotFileHash(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// godotKeepEdited moves the files an update replaced (now in backup) that the
// player had changed into a dated folder of target's godotKeptName, and
// returns how many and where. A replaced file is the port's own when its hash
// is the one recorded at the last install (known), or when the update brought
// the very same bytes back; any other one — an edited launch.sh, a file of a
// port put there by hand — is kept. The rest of backup goes with the stage.
func godotKeepEdited(backup, target string, known map[string]string) (int, string) {
	keptRoot := filepath.Join(target, godotKeptName, time.Now().Format("20060102-150405"))
	kept, seen := 0, 0
	_ = filepath.WalkDir(backup, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if seen++; seen > godotTreeMax {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(backup, p)
		if err != nil {
			return nil
		}
		old, err := godotFileHash(p)
		if err != nil {
			return nil
		}
		if want, ok := known[filepath.ToSlash(rel)]; ok && want == old {
			return nil
		}
		if now, err := godotFileHash(filepath.Join(target, rel)); err == nil && now == old {
			return nil
		}
		dst := filepath.Join(keptRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil
		}
		if err := os.Rename(p, dst); err != nil {
			if err := copyArchiveFile(p, dst, 0o644); err != nil {
				logDebug("godot", "cannot keep %s: %v", rel, err)
				return nil
			}
		}
		kept++
		return nil
	})
	if kept == 0 {
		return 0, ""
	}
	logInfo("godot", "%d replaced port files you had changed kept in %s", kept, keptRoot)
	return kept, keptRoot
}

// godotMarkRunnable sets the execute bits a script or program in dir lacks
// (a file copied across volumes is written 0644). A card that keeps no
// permissions (FAT) runs them anyway.
func godotMarkRunnable(dir string) {
	seen := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && skipScanEntryName(d.Name()) {
			return filepath.SkipDir // the kept copies (godotKeptName) stay as they were
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if seen++; seen > godotTreeMax {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Mode()&0o111 != 0 || !godotLooksRunnable(p) {
			return nil
		}
		if err := os.Chmod(p, info.Mode()|0o755); err != nil {
			logDebug("godot", "cannot mark %s executable: %v", p, err)
		}
		return nil
	})
}

// godotLooksRunnable reports a shell script or an ELF program.
func godotLooksRunnable(p string) bool {
	if strings.EqualFold(filepath.Ext(p), ".sh") {
		return true
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [4]byte
	if n, _ := io.ReadFull(f, head[:]); n < 2 {
		return false
	}
	return string(head[:]) == "\x7fELF" || string(head[:2]) == "#!"
}

// godotCopyMissing copies the files of src that dst does not have yet and
// returns how many it copied: the port's own pictures and maps go in, the
// player's files are never replaced.
func godotCopyMissing(src, dst string) int {
	added, seen := 0, 0
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != src && skipScanEntryName(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if seen++; seen > godotCopyMax {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		if !isPathWithin(target, dst) {
			return nil
		}
		if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil
		}
		if err := os.Rename(p, target); err != nil {
			if err := copyArchiveFile(p, target, 0o644); err != nil {
				logDebug("godot", "cannot copy %s: %v", rel, err)
				_ = os.Remove(target)
				return nil
			}
		}
		added++
		return nil
	})
	return added
}

// ── Provenance ──────────────────────────────────────────────────────────────

// godotMarker is JukaHub's record of the port it installed: where from, when,
// each of its files' SHA-256 (slash paths under Emus/GODOT) and the commit of
// the standalone port's repository it came from (after a fall back to Kiwi,
// the one it tried), which the Emulator updates pass records
// (emuautoupdate_handheld.go; "" for a press, which takes the default branch
// as it is).
type godotMarker struct {
	Source    string            `json:"source"`
	Installed time.Time         `json:"installed"`
	Files     map[string]string `json:"files,omitempty"`
	Commit    string            `json:"commit,omitempty"`
}

func godotMarkerPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, godotMarkerName)
}

func godotReadMarker() (godotMarker, bool) {
	var m godotMarker
	p := godotMarkerPath()
	if p == "" {
		return m, false
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) > godotMarkerMax || json.Unmarshal(raw, &m) != nil {
		return godotMarker{}, false
	}
	return m, true
}

func godotWriteMarker(m godotMarker) {
	p := godotMarkerPath()
	if p == "" {
		return
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		logDebug("godot", "cannot write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

// ── Engine versions ─────────────────────────────────────────────────────────

// godotVersion is a Godot engine release.
type godotVersion struct {
	Major, Minor, Patch int
}

func (v godotVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// short is major.minor, the part a pack and an engine must agree on.
func (v godotVersion) short() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
}

// godotParsePackHeader reads the engine version from the first
// godotPackHeaderSize bytes of a .pck; ok=false when they are not a Godot
// pack's header or hold numbers no Godot release has.
func godotParsePackHeader(b []byte) (godotVersion, bool) {
	if len(b) < godotPackHeaderSize || string(b[:4]) != "GDPC" {
		return godotVersion{}, false
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i : i+4]) }
	format, major, minor, patch := u(4), u(8), u(12), u(16)
	if format > 9 || major < 1 || major > 9 || minor > 99 || patch > 999 {
		return godotVersion{}, false
	}
	return godotVersion{Major: int(major), Minor: int(minor), Patch: int(patch)}, true
}

// godotPackVersion is the engine version a .pck was exported with. found is
// false when the file cannot be read; ok is false when it can be but is not
// a Godot pack (a renamed file, or a pack embedded in a program).
func godotPackVersion(path string) (v godotVersion, found, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return godotVersion{}, false, false
	}
	defer f.Close()
	var head [godotPackHeaderSize]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return godotVersion{}, true, false
	}
	v, ok = godotParsePackHeader(head[:])
	return v, true, ok
}

// godotEngineNames match an engine's file or folder name: frt_3.5.2,
// frt_3.5.2.squashfs, godot_4.2.2.squashfs, Godot_v3.5.3-stable_x11.64, and
// EmuELEC's frt_100_332_arm64.bin (FRT 1.0.0 with Godot 3.3.2).
var godotEngineNames = sync.OnceValue(func() [2]*regexp.Regexp {
	return [2]*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:frt|godot)[_\-. ]?v?([1-9])\.([0-9]{1,2})(?:\.([0-9]{1,3}))?(?:[^0-9]|$)`),
		regexp.MustCompile(`(?i)^frt_[0-9]+_([1-9])([0-9])([0-9])(?:[^0-9]|$)`),
	}
})

// godotEngineVersion is the version an engine's name carries.
func godotEngineVersion(name string) (godotVersion, bool) {
	for _, re := range godotEngineNames() {
		m := re.FindStringSubmatch(strings.TrimSpace(name))
		if m == nil {
			continue
		}
		var v godotVersion
		v.Major, _ = strconv.Atoi(m[1])
		v.Minor, _ = strconv.Atoi(m[2])
		if m[3] != "" {
			v.Patch, _ = strconv.Atoi(m[3])
		}
		return v, true
	}
	return godotVersion{}, false
}

// godotEngineLike reports a name that may be an engine (frt…, godot…).
func godotEngineLike(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "frt") || strings.HasPrefix(lower, "godot")
}

// godotSideFile reports one of the port's scripts, notes, pictures or maps,
// which is no engine whatever its name ("godot_4.2.2_notes.txt").
func godotSideFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".sh", ".txt", ".md", ".json", ".png", ".jpg", ".gptk", ".cfg", ".ini", ".log", ".runtime":
		return true
	}
	return false
}

// godotPortEngines lists the engine versions the port's folder carries, by
// file and folder name, newest first. unknown reports an engine-like name
// without a version: the list is then not the whole story.
func godotPortEngines(folder string) (engines []godotVersion, unknown bool) {
	if strings.TrimSpace(folder) == "" {
		return nil, false
	}
	seen := map[godotVersion]bool{}
	entries := 0
	root := filepath.Clean(folder)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == root {
			return nil
		}
		if entries++; entries > godotScanEntries {
			return filepath.SkipAll
		}
		name := d.Name()
		if skipScanEntryName(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && godotSideFile(name) {
			return nil
		}
		if v, ok := godotEngineVersion(name); ok {
			if !seen[v] {
				seen[v] = true
				engines = append(engines, v)
			}
		} else if godotEngineLike(name) {
			unknown = true
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator)) >= 3 {
			return filepath.SkipDir
		}
		return nil
	})
	sort.Slice(engines, func(i, j int) bool {
		a, b := engines[i], engines[j]
		if a.Major != b.Major {
			return a.Major > b.Major
		}
		if a.Minor != b.Minor {
			return a.Minor > b.Minor
		}
		return a.Patch > b.Patch
	})
	return engines, unknown
}

// godotVersionsText is "3.5.2" or "3.5.2, 3.2.3".
func godotVersionsText(list []godotVersion) string {
	parts := make([]string, 0, len(list))
	for _, v := range list {
		parts = append(parts, v.String())
	}
	return strings.Join(parts, ", ")
}

// godotEngineFits reports whether one of engines can play a pack of v: the
// same major version and a minor one no older than the pack's. A Godot 3
// engine loads packs of its own and older 3.x releases, rarely newer ones,
// and never a Godot 4 pack.
func godotEngineFits(engines []godotVersion, v godotVersion) bool {
	for _, e := range engines {
		if e.Major == v.Major && e.Minor >= v.Minor {
			return true
		}
	}
	return false
}

// godotLaunchNote writes a Godot game's engine version, and the port's, into
// the log at its start: the line a bug report needs when the game shows a
// black screen. Other games are left alone.
func godotLaunchNote(launcher, romPath string) {
	if !strings.EqualFold(filepath.Ext(romPath), godotPackExt) {
		return
	}
	v, found, ok := godotPackVersion(romPath)
	switch {
	case !found:
		return
	case !ok:
		logWarn("godot", "%s does not start like a Godot pack", filepath.Base(romPath))
		return
	}
	engines, unknown := godotPortEngines(filepath.Dir(launcher))
	logInfo("godot", "%s: made with Godot %s; port engines %q (more unnamed: %v)", filepath.Base(romPath), v, godotVersionsText(engines), unknown)
}

// godotDiagIssues is what the launch report adds for a Godot game that ended
// at once: a file that is no Godot pack, or a pack of an engine version the
// port does not carry. Nothing for other games.
func godotDiagIssues(system, romPath, launcher string) []launchIssue {
	if system != godotSystemName && !strings.EqualFold(filepath.Ext(romPath), godotPackExt) {
		return nil
	}
	base, dir := filepath.Base(romPath), filepath.Dir(romPath)
	v, found, ok := godotPackVersion(romPath)
	if !found {
		return nil
	}
	if !ok {
		return []launchIssue{{
			Level:  int(gameDiagSure),
			Title:  "This is not a Godot game pack",
			Files:  []string{base},
			Where:  dir,
			Detail: "A Godot game plays here as the .pck its PC export writes beside the program. This file does not start like one: a game built into its .exe (an embedded pack) or a renamed file does not play.",
		}}
	}
	var engines []godotVersion
	unknown := false
	if folder := filepath.Dir(launcher); godotIsFolder(filepath.Base(folder)) {
		engines, unknown = godotPortEngines(folder)
	}
	if len(engines) > 0 && !godotEngineFits(engines, v) {
		level := gameDiagLikely
		if unknown {
			level = gameDiagMaybe
		}
		return []launchIssue{{
			Level: int(level),
			Title: "Made with Godot " + v.short() + ", which the port does not have",
			Files: []string{base},
			Where: dir,
			Detail: "The Godot port here carries the Godot " + godotVersionsText(engines) + " engine. A pack plays only in an engine of its own " +
				"major version, and rarely in an older minor one. Pick the matching engine in a " + strings.TrimSuffix(base, filepath.Ext(base)) +
				".runtime file beside the game if the port has one, or look for a build of the game made with Godot " + strconv.Itoa(engines[0].Major) + ".",
		}}
	}
	if len(engines) == 0 && v.Major >= 4 {
		return []launchIssue{{
			Level:  int(gameDiagMaybe),
			Title:  "Made with Godot " + v.short(),
			Files:  []string{base},
			Where:  dir,
			Detail: "Handheld Godot ports mostly play Godot 3 games (FRT). A Godot 4 game needs a Godot 4 engine built for this handheld, which the port may not have.",
		}}
	}
	return nil
}
