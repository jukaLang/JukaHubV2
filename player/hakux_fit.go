package main

// hakux_fit.go — hakuX on every TrimUI model, not only the Smart Pro.
//
// Juka, 2026-11-21: "GameCube, PS2, and XBOX should all work on all Trimui
// devices (even if performance is not great)". The hakuX package JukaHub
// installs (hakux.go, ToastedFrog2's port) is made for the original Smart Pro:
// its emulator draws a 1280x720 picture only. spruceUI builds the same port
// from the same sources (github.com/spruceUI/hakuX-spruce, release tag
// beta-main, asset hakux-aarch64.tar.gz) for every 64-bit handheld with a
// Vulkan driver: glibc 2.31, tuned for the Cortex-A53, linked against the
// device's SDL2, and drawing at the panel's own mode — the Brick's and Brick
// Pro's 1024x768 as well as the Smart Pro S's 1280x720 on its Mali GPU. Its
// archive holds hakux.aarch64 (the emulator alone), licences and the commits
// it was built from.
//
// So on every model but the Smart Pro, an install or update of the package is
// followed by this step: the newest beta-main archive is downloaded, checked
// against the SHA-256 digest GitHub states for it (an asset without one is
// refused: this binary runs as root), its hakux.aarch64 must be a 64-bit ARM
// executable, and it replaces the package's own emulator binary — found as the
// largest 64-bit ARM executable in the launcher folder whose name says hakux.
// The package's launcher, settings, swap and controls stay as they are. The
// provenance file records the replacement (ScreenBuild) and the new file's
// hash, so the next update replaces it like any file JukaHub laid down and
// runs this step again. When the step cannot run (offline, no digest, no
// binary found), the package's own binary stays and the log says why.
//
// An install made before this step existed kept the Smart Pro's binary: a
// tester's Brick Pro, whose hakuX JukaHub had installed on 2026-11-21, played
// the game's music behind a screen that never moved past the menu (relayed by
// Juka on 2026-11-22, and asked about again on 2026-11-24). Nothing fitted it
// later: the step ran only with an install or an update of the package, and
// with no newer release there was no update (X on the tile did fit it, behind
// "already the newest version"). So an Xbox game's start now fits such an
// install first (hakuxFitGate): the start waits under the install card while
// the build downloads, once, and the game starts when it is in. The emulator
// it replaces is looked for among the files the install recorded as well as
// in the launcher folder, wherever the package keeps it (hakuxFitTarget).

import (
	"archive/tar"
	"compress/gzip"
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
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// hakuxFitRepo, hakuxFitTag and hakuxFitAsset name spruceUI's build.
	hakuxFitRepo  = "spruceUI/hakuX-spruce"
	hakuxFitTag   = "beta-main"
	hakuxFitAsset = "hakux-aarch64.tar.gz"
	// hakuxFitEntry is the emulator inside that archive.
	hakuxFitEntry = "hakux.aarch64"
	// hakuxFitMaxBytes bounds the download and the unpacked binary.
	hakuxFitMaxBytes = int64(256 << 20)
	// hakuxFitSearchDepth bounds the search for the package's binary.
	hakuxFitSearchDepth = 4
)

// hakuxNeedsFit reports whether device plays with spruceUI's build: every
// model but the original Smart Pro, which the package is made for.
func hakuxNeedsFit(device DeviceModel) bool {
	return device != DeviceTrimuiSmartPro
}

// hakuxIsARM64Exec reports whether head (a file's first bytes) is a 64-bit,
// little-endian ARM executable or position-independent executable. Pure.
func hakuxIsARM64Exec(head []byte) bool {
	if len(head) < 20 || string(head[:4]) != "\x7fELF" || head[4] != 2 || head[5] != 1 {
		return false
	}
	typ := binary.LittleEndian.Uint16(head[16:18])
	machine := binary.LittleEndian.Uint16(head[18:20])
	return (typ == 2 || typ == 3) && machine == 183 // EM_AARCH64
}

// hakuxFileIsARM64Exec reads p's ELF header.
func hakuxFileIsARM64Exec(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 20)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return hakuxIsARM64Exec(head)
}

// hakuxFitTarget finds the package's emulator: the largest 64-bit ARM
// executable whose name contains "hakux" (not a library), among the files the
// install recorded in m (under the card at sdRoot: the package's Data/ folder
// as well as its Emus/ one) and in the launcher folder.
func hakuxFitTarget(sdRoot, folder string, m hakuxManifest) (string, error) {
	var best string
	var bestSize int64
	consider := func(p string, info fs.FileInfo) {
		name := strings.ToLower(info.Name())
		if !info.Mode().IsRegular() || !strings.Contains(name, "hakux") || strings.Contains(name, ".so") ||
			info.Size() <= bestSize || !hakuxFileIsARM64Exec(p) {
			return
		}
		best, bestSize = p, info.Size()
	}
	if strings.TrimSpace(sdRoot) != "" {
		for _, f := range m.Files {
			rel, err := hakuxCardRel(f.Path)
			if err != nil {
				continue
			}
			p := filepath.Join(sdRoot, hakuxOS(rel))
			if info, err := os.Lstat(p); err == nil {
				consider(p, info)
			}
		}
	}
	base := filepath.Clean(folder)
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(base, p); rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= hakuxFitSearchDepth-1 {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			consider(p, info)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", fmt.Errorf("no hakuX emulator binary found in %s or among the files its install recorded", folder)
	}
	return best, nil
}

// hakuxFitRelease reads spruceUI's beta-main release and picks its archive.
func hakuxFitRelease(ctx context.Context, client *http.Client) (emuWinRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", standaloneAPIBase, hakuxFitRepo, hakuxFitTag)
	data, err := fetchUpdateBytes(ctx, client, url, patchMaxMetadata)
	if err != nil {
		return emuWinRelease{}, err
	}
	var rel emuGitHubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return emuWinRelease{}, errors.New("unreadable hakuX-spruce release metadata")
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, hakuxFitAsset) && strings.TrimSpace(a.BrowserDownloadURL) != "" {
			return emuReleaseFrom(rel, a), nil
		}
	}
	return emuWinRelease{}, fmt.Errorf("the %s release of %s has no %s", hakuxFitTag, hakuxFitRepo, hakuxFitAsset)
}

// hakuxFitUnpack copies hakux.aarch64 out of a .tar.gz into dest, bounded.
func hakuxFitUnpack(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s holds no %s", hakuxFitAsset, hakuxFitEntry)
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != hakuxFitEntry {
			continue
		}
		if h.Size <= 0 || h.Size > hakuxFitMaxBytes {
			return fmt.Errorf("%s in %s has an unlikely size (%d bytes)", hakuxFitEntry, hakuxFitAsset, h.Size)
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if !hakuxFileIsARM64Exec(dest) {
			return fmt.Errorf("%s in %s is not a 64-bit ARM program", hakuxFitEntry, hakuxFitAsset)
		}
		return nil
	}
}

// hakuxFitApply replaces the package's emulator (folder is the launcher
// folder, on the card at p.SDRoot) with spruceUI's build and records it in m.
// progress, when not nil, gets the download's bytes.
func hakuxFitApply(ctx context.Context, client *http.Client, p hakuxPaths, folder string, m *hakuxManifest, progress func(done, total int64)) error {
	target, err := hakuxFitTarget(p.SDRoot, folder, *m)
	if err != nil {
		return err
	}
	rel, err := hakuxFitRelease(ctx, client)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rel.Digest) == "" {
		return fmt.Errorf("GitHub states no checksum for %s, so it is not installed", hakuxFitAsset)
	}
	if rel.Size > hakuxFitMaxBytes {
		return fmt.Errorf("%s is %s, more than the %s limit", hakuxFitAsset, formatInstallBytes(rel.Size), formatInstallBytes(hakuxFitMaxBytes))
	}
	// Staged beside the install's own staging folder, on the same card, so the
	// last step is a rename.
	work := filepath.Join(p.SDRoot, hakuxOS(path.Join(p.State, "fit")))
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	archive := filepath.Join(work, hakuxFitAsset)
	sum, err := emuDownload(ctx, client, rel.URL, archive, hakuxFitMaxBytes, progress)
	if err != nil {
		return err
	}
	if err := emuDigestMatches(sum, rel.Digest); err != nil {
		return err
	}
	bin := filepath.Join(work, hakuxFitEntry)
	if err := hakuxFitUnpack(archive, bin); err != nil {
		return err
	}
	_ = os.Remove(archive)
	data, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Rename(bin, target); err != nil {
		return err
	}
	_ = os.Chmod(target, mode)
	if relPath, err := filepath.Rel(p.SDRoot, target); err == nil {
		relPath = filepath.ToSlash(relPath)
		for i := range m.Files {
			if strings.EqualFold(m.Files[i].Path, relPath) {
				m.Files[i].Size = int64(len(data))
				m.Files[i].SHA256 = hex.EncodeToString(digest[:])
			}
		}
	}
	m.ScreenBuild = hakuxFitRepo + " " + hakuxFitTag + " " + rel.Digest
	logInfo("emulators", "hakuX: %s replaced with %s's %s build (%s, %s) for this model's screen and GPU",
		target, hakuxFitRepo, hakuxFitTag, rel.AssetDate, rel.Digest)
	return nil
}

// ── An Xbox game's start ─────────────────────────────────────────────────────

func init() {
	gameLaunchRegisterGate(hakuxFitGate)
}

// What a start already did this session about one install's fit
// (hakuxFitState, by launcher folder).
const (
	// hakuxFitSaidOffline: a start offline said what the game waits for; the
	// next one plays with the emulator there.
	hakuxFitSaidOffline = 1
	// hakuxFitRan: a fit ran for a start. One that failed (GitHub's limit, the
	// Wi-Fi gone midway) lets the next start play with the emulator there
	// rather than wait again.
	hakuxFitRan = 2
)

// hakuxFitState and hakuxFitResume are main-thread state: the first is what a
// start did about each install's fit this session, the second the game whose
// start waited for a fit and runs again, which the gate lets through.
var (
	hakuxFitState  map[string]int
	hakuxFitResume string
)

// hakuxFitDue reports an install that still has the package's own emulator on
// a model that plays with spruceUI's build: a hakuX JukaHub installed (its
// provenance file in folder) with no screen build recorded. A small read.
func hakuxFitDue(folder string) bool {
	if IsWindows() || !IsTrimuiDevice() || strings.TrimSpace(folder) == "" {
		return false
	}
	device := hakuxDevice()
	if !hakuxNeedsFit(device) || !hakuxSupportFor(device).Offer {
		return false
	}
	m, managed := hakuxReadManifest(folder)
	return managed && strings.TrimSpace(m.ScreenBuild) == ""
}

// hakuxFitGate is the library launch's gate (gamelaunch_hooks.go) for an Xbox
// game started by the launcher of such an install (hakuxFitDue): the start
// waits while spruceUI's build goes in and runs again once it is
// (hakuxFitBegin). Offline it says what the game waits for, once; a start
// nobody pressed for, and every start after this session's fit, plays with the
// emulator there. Main thread.
func hakuxFitGate(game libraryGame, launcher string) (held bool, err error) {
	if hakuxFitResume != "" && hakuxFitResume == game.Path {
		return false, nil
	}
	launcher = strings.TrimSpace(launcher)
	if !strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) || !launcherShellScript(launcher) {
		return false, nil
	}
	folder := filepath.Dir(launcher)
	state := hakuxFitState[folder]
	if state == hakuxFitRan || !hakuxIsXboxFolder(filepath.Base(folder), folder) || !hakuxFitDue(folder) {
		return false, nil
	}
	if hakuxUnasked {
		logInfo("emulators", "hakuX: %s starts with the Smart Pro's build of the emulator: a start nobody pressed for downloads nothing", filepath.Base(game.Path))
		return false, nil
	}
	if !IsOnline() {
		if state == hakuxFitSaidOffline {
			logInfo("emulators", "hakuX: %s starts with the Smart Pro's build of the emulator (offline)", filepath.Base(game.Path))
			return false, nil
		}
		hakuxFitNote(folder, hakuxFitSaidOffline)
		return false, errors.New("hakuX here is still the Smart Pro's build, made for its 1280x720 screen · connect to the internet " +
			"and start it again to get the one for this screen (once), or start it again now to play as it is")
	}
	if why := hakuxFitBegin(game, launcher, folder); why != "" {
		return false, errors.New(why)
	}
	return true, nil
}

// hakuxFitNote records what a start did about the install at folder.
func hakuxFitNote(folder string, state int) {
	if hakuxFitState == nil {
		hakuxFitState = map[string]int{}
	}
	hakuxFitState[folder] = state
}

// hakuxFitBegin puts spruceUI's build into the hakuX install at folder under
// the install card, then starts game again with launcher when nothing else
// took the screen since (hakuxFitAfter). It returns why it did not begin, ""
// when it did. Main thread.
func hakuxFitBegin(game libraryGame, launcher, folder string) string {
	title := hakuxGameTitle(game)
	if emuInstallingName() == emuXboxEntryName {
		return "hakuX is being installed — start " + title + " again once it is in"
	}
	if installBarActive() || !tryBeginPatchOp() {
		return "Another download or update is running — start " + title + " again when it is done"
	}
	hakuxFitNote(folder, hakuxFitRan)
	installBarBeginLabelled(hakuxName, "Getting hakuX for this screen")
	installBarPhase(installPhaseResolving)
	emuSetInstalling(emuXboxEntryName)
	// The caller shows "Playing …" for a start a gate held: that goes, and
	// this stays (gameReadyHushCallerToast).
	msg := "Getting the hakuX build for this screen (once) — " + title + " starts when it is in"
	ShowToast(msg, ToastKindInfo)
	gameReadyHushCallerToast(game)
	tbxPost(func() { ShowToast(msg, ToastKindInfo) })
	p := hakuxDevicePaths()
	fromLibrary := gameLib.open
	pressed := time.Now()
	logInfo("emulators", "hakuX: %s waits for spruceUI's build of the emulator: the install in %s still has the Smart Pro's", filepath.Base(game.Path), folder)
	go func() {
		ctx, cancel := context.WithTimeout(installBarRunContext(), emuWinInstallTimeout)
		err := hakuxFitInstalled(ctx, emuWinClient(), p, folder)
		cancel()
		emuSetInstalling("")
		switch {
		case err == nil:
			installBarPhase(installPhaseDone)
			installBarDone()
			invalidateEmulatorCache()
		case errors.Is(err, context.Canceled):
			installBarAbort()
		default:
			installBarFail(installCardDetail(emuFriendlyError(err)))
			logWarn("emulators", "hakuX: the build for this screen could not be installed: %v", err)
		}
		endPatchOp()
		finished := time.Now()
		tbxPost(func() { hakuxFitAfter(game, launcher, fromLibrary, pressed, finished, err) })
	}()
	return ""
}

// hakuxFitInstalled fits the install at folder (hakuxFitApply) and records
// that in its provenance file; one fitted meanwhile (X on the tile) is left as
// it is. Off the main thread, under the install card.
func hakuxFitInstalled(ctx context.Context, client *http.Client, p hakuxPaths, folder string) error {
	m, managed := hakuxReadManifest(folder)
	if !managed {
		return fmt.Errorf("%s holds no hakuX that JukaHub installed", folder)
	}
	if strings.TrimSpace(m.ScreenBuild) != "" {
		return nil
	}
	if err := hakuxFitApply(ctx, client, p, folder, &m, func(done, total int64) {
		emuInstallBarReport(installPhaseDownload, done, total)
	}); err != nil {
		return err
	}
	return hakuxWriteManifest(folder, m)
}

// hakuxFitAfter is the main loop's end of a start's fit: the game starts when
// the build went in and nothing took the screen since the press
// (hakuxPlayAfterInstall's rule); otherwise a toast says where things stand.
// After a fit that failed, the next start plays with the emulator there.
// Main thread.
func hakuxFitAfter(game libraryGame, launcher string, fromLibrary bool, pressed, finished time.Time, err error) {
	title := hakuxGameTitle(game)
	switch {
	case errors.Is(err, context.Canceled):
		ShowToast("Stopped · start "+title+" again to play with the hakuX build that is there", ToastKindInfo)
		return
	case err != nil:
		ShowToast("The hakuX build for this screen did not install ("+firstLine(emuFriendlyError(err))+") · start "+title+
			" again to play with the Smart Pro's", ToastKindWarning)
		return
	}
	still := !gameScreenSuspended() && !videoHoldsScreenAwake() && !emuPickOpen() &&
		gameLib.open == fromLibrary && time.Since(pressed) < hakuxPlayAfterWithin &&
		time.Since(finished) < hakuxPlayAfterLate
	if !still || !regularFile(game.Path) {
		ShowToast("hakuX for this screen is in · start "+title+" to play", ToastKindSuccess)
		return
	}
	logInfo("emulators", "hakuX: the build for this screen is in; starting %s", filepath.Base(game.Path))
	hakuxFitResume = game.Path
	err = launchLibraryGame(game, launcher)
	hakuxFitResume = ""
	if err != nil {
		gameLaunchErrorToast(title, err)
	}
}
