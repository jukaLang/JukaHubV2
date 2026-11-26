package main

// greenovercast.go — GreenOvercast, the one streaming client the handheld has
// for Xbox Cloud Gaming, GeForce NOW and an Xbox console at home.
//
// GreenOvercast is a native ARM64 client for small Linux handhelds, shipped
// as a PortMaster port (greenovercast.zip). It comes in two builds that
// install as the same port, so only one can be on the card at a time:
//
//   - the cloud build — Producdevity's upstream, v0.3.0 and later: Xbox Cloud
//     Gaming and GeForce NOW. No streaming from your own console.
//   - the xHome build — doomL's fork (v0.3.0-xhome, made from upstream
//     v0.2.0): Xbox Cloud Gaming and your own Xbox (xHome). No GeForce NOW,
//     which upstream added later. Its xHome work is offered upstream as
//     pull request 6; once that lands the cloud build does everything.
//
// Cloud Gaming (cloudgaming.go) installs the cloud build and X1 (xremote.go)
// the xHome build; both play whichever is installed, and say so when the one
// on the card lacks what was asked for. JukaHub records which build it put
// there in data/greenovercast.json; a port installed by hand is recognised by
// a bounded look through its folder, and otherwise is "not known" and may try
// anything.
//
// Every build needs glibc 2.38 or newer. Stock TrimUI firmware has 2.33, so
// JukaHub installs a verified private glibc 2.39 on first launch and starts
// only GreenOvercast's binary through that loader. The firmware stays intact.
//
// The download goes into portsStagingDir() and is handed to portsInstallZip
// (store_scenes.go), the installer every port JukaHub downloads goes through.
// Nothing here claims a sign-in or a stream: GreenOvercast shows the sign-in
// code and reports the rest itself.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// goClientName is the client's name in toasts, rows and the status strip.
	goClientName = "GreenOvercast"
	// goZipName is the port zip both builds publish.
	goZipName = "greenovercast.zip"
	// goGlibcWant is the oldest glibc every GreenOvercast build loads on.
	goGlibcWant = "GLIBC_2.38"
	// goMarkerName is JukaHub's record of the build it installed, in data/.
	goMarkerName = "greenovercast.json"
	// goQueuedGrace is how long a zip queued in PortMaster's autoinstall
	// folder is left for PortMaster before it counts as passed over.
	goQueuedGrace = 10 * time.Minute
	// goScanEntries bounds the look through a port folder for its build.
	goScanEntries = 600
)

// goBuild names a GreenOvercast build.
type goBuild string

const (
	goBuildUnknown goBuild = ""
	goBuildCloud   goBuild = "cloud"
	goBuildXHome   goBuild = "xhome"
)

// goBuildURLs are tried in order for each build: the newest release, then the
// release JukaHub was written against. The asset name is the one both
// READMEs tell players to copy into PortMaster's autoinstall folder.
var goBuildURLs = map[goBuild][]string{
	goBuildCloud: {
		"https://github.com/Producdevity/GreenOvercast/releases/latest/download/" + goZipName,
		"https://github.com/Producdevity/GreenOvercast/releases/download/v0.3.0/" + goZipName,
	},
	goBuildXHome: {
		"https://github.com/doomL/GreenOvercast/releases/latest/download/" + goZipName,
		"https://github.com/doomL/GreenOvercast/releases/download/v0.3.0-xhome/" + goZipName,
	},
}

// goBuildPages are where a player fetches a build by hand.
var goBuildPages = map[goBuild]string{
	goBuildCloud: "github.com/Producdevity/GreenOvercast/releases",
	goBuildXHome: "github.com/doomL/GreenOvercast/releases",
}

// hasGFN reports whether the build may stream GeForce NOW (unknown: maybe).
func (b goBuild) hasGFN() bool { return b != goBuildXHome }

// hasXHome reports whether the build may stream your own Xbox (unknown: maybe).
func (b goBuild) hasXHome() bool { return b != goBuildCloud }

// short is the build's name in a status line.
func (b goBuild) short() string {
	switch b {
	case goBuildCloud:
		return "cloud build"
	case goBuildXHome:
		return "xHome build"
	}
	return "build not known"
}

// streams says what the build streams.
func (b goBuild) streams() string {
	switch b {
	case goBuildCloud:
		return "Xbox Cloud Gaming and GeForce NOW"
	case goBuildXHome:
		return "Xbox Cloud Gaming and your own Xbox"
	}
	return "Xbox Cloud Gaming, and GeForce NOW or your own Xbox depending on the build"
}

// other is the build a switch installs.
func (b goBuild) other() goBuild {
	if b == goBuildCloud {
		return goBuildXHome
	}
	return goBuildCloud
}

// goOnHandheld reports whether GreenOvercast is the client here (it is an
// ARM64 Linux program).
func goOnHandheld() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" }

// ── What is on the card ──────────────────────────────────────────────────

// goMarker is data/greenovercast.json.
type goMarker struct {
	Build goBuild `json:"build"`
	URL   string  `json:"url"`
	MD5   string  `json:"md5"`
	// Size is the zip's size: a queued zip of this size in PortMaster's
	// autoinstall folder is the one JukaHub put there (the device clock
	// cannot be trusted to age it).
	Size int64 `json:"size"`
	// Installed is true when the installer reported the launcher on the card;
	// false while the zip waits for PortMaster.
	Installed bool `json:"installed"`
	// At is informative only: the handheld boots before its clock is set.
	At string `json:"at"`
}

func goMarkerPath() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, goMarkerName)
	}
	return filepath.Join("data", goMarkerName)
}

// goReadMarker reads the record; ok is false when there is none or it names
// no known build.
func goReadMarker() (goMarker, bool) {
	raw, err := os.ReadFile(goMarkerPath())
	if err != nil || len(raw) > 64<<10 {
		return goMarker{}, false
	}
	var m goMarker
	if json.Unmarshal(raw, &m) != nil || (m.Build != goBuildCloud && m.Build != goBuildXHome) {
		return goMarker{}, false
	}
	return m, true
}

func goWriteMarker(m goMarker) {
	m.At = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	path := goMarkerPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := AtomicWrite(path, raw, 0o644); err != nil {
		logWarn("greenovercast", "record the build: %v", err)
	}
}

// goState is GreenOvercast on this card.
type goState struct {
	Installed bool
	Name      string // the launcher's name
	Launch    string // the launcher script
	Dir       string // the port's folder, "" when not found
	Build     goBuild
	// FromMarker: Build is JukaHub's own record, not a guess from the folder.
	FromMarker bool
	// Queued is a zip still waiting in PortMaster's autoinstall folder, and
	// QueuedBuild the build JukaHub recorded for it.
	Queued      string
	QueuedBuild goBuild
	// PMLauncher is PortMaster's launcher, "" when it is not installed.
	PMLauncher string
}

// goPortDir finds the port's folder: the one its port.json names (when it is
// GreenOvercast's own, not a GAMEDIR such as the card root), else a
// greenovercast folder beside the launcher or in a ports folder.
func goPortDir(launch string) string {
	if meta := portMetaFor(launch); meta.Dir != "" && x1PortName(filepath.Base(meta.Dir)) && dirExists(meta.Dir) {
		return meta.Dir
	}
	roots := append([]string{filepath.Dir(launch)}, portRoots(portMasterSDRoot)...)
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.IsDir() && x1PortName(it.Name()) {
				return filepath.Join(root, it.Name())
			}
		}
	}
	return ""
}

// goGuessBuild looks through a port folder (bounded) for the files that tell
// the builds apart: GeForce NOW pieces for the cloud build, xHome ones for the
// fork. Both, or neither, is unknown.
func goGuessBuild(dir string) goBuild {
	if dir == "" {
		return goBuildUnknown
	}
	gfn, xhome, seen := false, false, 0
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > 3 || seen >= goScanEntries {
			return
		}
		items, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, it := range items {
			if seen++; seen > goScanEntries {
				return
			}
			n := strings.ToLower(it.Name())
			if strings.Contains(n, "gfn") || strings.Contains(n, "geforce") {
				gfn = true
			}
			if strings.Contains(n, "xhome") {
				xhome = true
			}
			if it.IsDir() && it.Type()&os.ModeSymlink == 0 {
				walk(filepath.Join(d, it.Name()), depth+1)
			}
		}
	}
	walk(dir, 0)
	switch {
	case gfn && !xhome:
		return goBuildCloud
	case xhome && !gfn:
		return goBuildXHome
	}
	return goBuildUnknown
}

// goBuildIn names the build behind an installed launcher: JukaHub's record
// when it belongs to this install, else a look through dir. When both md5s
// are known they decide; otherwise only a record of a finished install
// counts — a zip that is merely queued (a switch PortMaster has not done yet)
// says nothing about what is on the card. It touches the disk.
func goBuildIn(launch, dir string) (goBuild, bool) {
	if marker, ok := goReadMarker(); ok {
		meta := portMetaFor(launch)
		switch {
		case meta.MD5 != "" && marker.MD5 != "":
			if strings.EqualFold(meta.MD5, marker.MD5) {
				return marker.Build, true
			}
		case marker.Installed:
			return marker.Build, true
		}
	}
	return goGuessBuild(dir), false
}

// goScanState reads the card. It touches the disk: scene tasks only.
func goScanState() goState {
	var st goState
	st.PMLauncher = portMasterLauncher(portMasterSDRoot)
	if e, ok := x1FindPort(portRoots(portMasterSDRoot)); ok {
		st.Installed, st.Name, st.Launch = true, e.Name, e.Launch
		st.Dir = goPortDir(e.Launch)
		st.Build, st.FromMarker = goBuildIn(e.Launch, st.Dir)
	}
	marker, haveMarker := goReadMarker()
	if q := goQueuedZip(st.PMLauncher); q != "" {
		st.Queued = q
		if haveMarker && !marker.Installed {
			st.QueuedBuild = marker.Build
		}
	}
	return st
}

// goQueuedZip is a good GreenOvercast zip waiting in PortMaster's autoinstall
// folder, else "". The zip JukaHub queued (the record's size, not yet
// installed) counts however long it waits — its file time is the download's,
// and the clock may be wrong. Any other zip counts only within
// goQueuedGrace, after which PortMaster has passed over it.
func goQueuedZip(launcher string) string {
	inbox := mcAutoinstallDir(launcher)
	if inbox == "" {
		return ""
	}
	q := filepath.Join(inbox, goZipName)
	info, err := os.Stat(q)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	m, ok := goReadMarker()
	ours := ok && !m.Installed && m.Size > 0 && m.Size == info.Size()
	if !ours {
		if age := time.Since(info.ModTime()); age < 0 || age > goQueuedGrace {
			return ""
		}
	}
	if goCheckPortZip(q) != nil {
		return ""
	}
	return q
}

// goCheckPortZip accepts a readable zip shaped like a PortMaster port that
// carries GreenOvercast: a launcher script or port.json, an entry named for
// it, and no entry that would land outside the folder it is unpacked in.
func goCheckPortZip(p string) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("the download is not a zip: %w", err)
	}
	defer zr.Close()
	named, port := false, false
	for _, f := range zr.File {
		n := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasPrefix(n, "/") || n == ".." || strings.HasPrefix(n, "../") ||
			strings.Contains(n, "/../") || strings.HasSuffix(n, "/..") || (len(n) > 1 && n[1] == ':') {
			return errors.New("the download has an unsafe path in it")
		}
		name := strings.TrimSuffix(f.Name, "/")
		base := filepath.Base(name)
		if x1PortName(base) || x1PortName(strings.SplitN(name, "/", 2)[0]) {
			named = true
		}
		if strings.EqualFold(filepath.Ext(base), ".sh") || strings.EqualFold(base, "port.json") {
			port = true
		}
	}
	if !named || !port {
		return errors.New("the download is not the " + goClientName + " port")
	}
	return nil
}

// ── glibc ────────────────────────────────────────────────────────────────

var goGlibc struct {
	once sync.Once
	have string
	old  bool
}

// goGlibcOld reports positive evidence that this firmware's glibc is older
// than GreenOvercast needs, with the version it has. Read once: it walks the
// library folders.
func goGlibcOld() (string, bool) {
	goGlibc.once.Do(func() {
		if goOnHandheld() {
			goGlibc.have, goGlibc.old = hostGlibcBelow(goGlibcWant)
		}
	})
	return strings.TrimPrefix(goGlibc.have, "GLIBC_"), goGlibc.old
}

// goGlibcNote explains how an older firmware is handled in a details sheet.
func goGlibcNote() string {
	if have, old := goGlibcOld(); old {
		if !IsTrimuiDevice() {
			return "GreenOvercast needs glibc 2.38 or newer, while this firmware has " + have +
				". A newer firmware or a compatible runtime is needed."
		}
		if privateGlibcReady() {
			return "GreenOvercast needs a newer C runtime than this firmware's " + have +
				". JukaHub's private runtime is ready and is used for GreenOvercast without changing the firmware."
		}
		return "GreenOvercast needs a newer C runtime than this firmware's " + have +
			". On first launch, JukaHub downloads and checks a private runtime for GreenOvercast. Keep Wi-Fi connected."
	}
	return "GreenOvercast needs glibc 2.38 or newer; JukaHub did not detect an older version here."
}

// goDecodeNote is the video-decoding caveat for TrimUI handhelds.
func goDecodeNote() string {
	if !IsTrimuiDevice() {
		return ""
	}
	if DetectDevice() == DeviceTrimuiSmartProS {
		// Report JH-AT4EEE (2026-11-25): "Cedar decoder initialization
		// failed", then "Selected video decoder: ffmpeg-software".
		return "On a Smart Pro S (A523), GreenOvercast's Cedar decoder did not start in the first test, so it decodes the picture on the CPU: " +
			"pick 720p and 30 fps in GreenOvercast's settings."
	}
	return "On a tested A133 TrimUI, GreenOvercast selected its cedar-h616 decoder at startup; a live stream has not been checked yet. " +
		"If the picture stutters, pick 720p and 30 fps in GreenOvercast's settings."
}

// ── Install ──────────────────────────────────────────────────────────────

var goInstallState struct {
	mu      sync.Mutex
	running bool
	build   goBuild
	phase   string // installPhaseDownload, installPhaseExtract
}

// goInstallBusy reports a GreenOvercast install in flight.
func goInstallBusy() (goBuild, string, bool) {
	goInstallState.mu.Lock()
	defer goInstallState.mu.Unlock()
	return goInstallState.build, goInstallState.phase, goInstallState.running
}

func goSetInstallPhase(phase string) {
	goInstallState.mu.Lock()
	goInstallState.phase = phase
	goInstallState.mu.Unlock()
}

func goEndInstall() {
	goInstallState.mu.Lock()
	goInstallState.running, goInstallState.build, goInstallState.phase = false, goBuildUnknown, ""
	goInstallState.mu.Unlock()
}

// goInstall gets build onto the handheld: PortMaster first when it is
// missing, then the download, then the shared installer. It speaks for
// itself in toasts. screenUp is asked on the main loop when the installer
// reports a zip left for PortMaster: PortMaster's own app opens to finish it
// only when nothing else waits in the install queue, that screen is still up
// and no game holds the screen — otherwise a toast says it is queued. done,
// when set, runs on the main loop with the installer's result. UI thread.
func goInstall(build goBuild, screenUp func() bool, done func(portsInstallResult)) {
	if !goOnHandheld() {
		ShowToast(goClientName+" runs on the handheld", ToastKindInfo)
		return
	}
	if !uiModeGate(uiFeatSetup) { // Kiosk: the X1 tile and Cloud Gaming install nothing (uimode.go)
		return
	}
	urls := goBuildURLs[build]
	if len(urls) == 0 {
		return
	}
	if _, _, busy := goInstallBusy(); busy {
		ShowToast(goClientName+" is already downloading — the status strip shows how far", ToastKindInfo)
		return
	}
	if portMasterInstalling() {
		ShowToast("PortMaster is installing — press again when it is done", ToastKindInfo)
		return
	}
	launcher := portMasterLauncher(portMasterSDRoot)
	if launcher == "" {
		switch {
		case installBarActive():
			ShowToast("Another download is running — try again when it finishes", ToastKindInfo)
		default:
			if err := startPortMasterInstall(); err != nil {
				PlayErrorSound()
				ShowToast(goClientName+" comes through PortMaster — "+firstLine(err.Error()), ToastKindError)
				return
			}
			PlayActivateSound()
			ShowToast("Installing PortMaster first — press again when it is done", ToastKindInfo)
		}
		return
	}
	// A zip of this build that JukaHub queued is PortMaster's to finish. Any
	// other queued zip (the other build, or one PortMaster passed over) is
	// dropped only once a good replacement has downloaded (below), so going
	// offline never costs the player the zip that is there.
	if q := goQueuedZip(launcher); q != "" {
		if m, ok := goReadMarker(); ok && !m.Installed && m.Build == build {
			if screenUp == nil || screenUp() {
				PlayActivateSound()
				mcOpenPortMaster(launcher, "PortMaster is installing "+goClientName+" — close it when it is done to come back")
			} else {
				ShowToast(goClientName+" waits in PortMaster — open PortMaster to finish installing it", ToastKindInfo)
			}
			return
		}
	}
	if !IsOnline() {
		PlayErrorSound()
		ShowToast("No internet — connect Wi-Fi, then try again", ToastKindError)
		return
	}
	if installBarActive() {
		ShowToast("Another download is running — try again when it finishes", ToastKindInfo)
		return
	}
	goInstallState.mu.Lock()
	if goInstallState.running {
		goInstallState.mu.Unlock()
		return
	}
	goInstallState.running, goInstallState.build, goInstallState.phase = true, build, installPhaseDownload
	goInstallState.mu.Unlock()

	PlayActivateSound()
	ShowToast("Downloading "+goClientName+" ("+build.short()+")", ToastKindInfo)
	gen, card := installBarBeginRun(goClientName)
	ctx, cancel := mcRunContext(gen, card)
	go func() {
		defer cancel()
		// Its own sub-folder: the store queue manages staging/<zip> files and
		// must never take this download for one of its own.
		path, url, err := goDownloadZip(ctx, gen, filepath.Join(portsStagingDir(), "greenovercast"), urls)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			logDebug("greenovercast", "download cancelled")
			installBarAbortRun(gen)
			goEndInstall()
			return
		default:
			logWarn("greenovercast", "download %s: %v", build, err)
			installBarFailRun(gen, firstLine(err.Error()))
			goEndInstall()
			tbxPost(func() {
				ShowToast("Could not download "+goClientName+" — get "+goZipName+" from "+goBuildPages[build], ToastKindError)
			})
			return
		}
		sum, _ := mcFileMD5(path)
		var size int64
		if info, err := os.Stat(path); err == nil {
			size = info.Size()
		}
		installBarDoneRun(gen)
		goSetInstallPhase(installPhaseExtract)
		// The replacement is here: a zip still queued in PortMaster's
		// autoinstall folder (the other build, or one PortMaster passed over)
		// goes now, so PortMaster cannot install it over the new one later.
		if inbox := mcAutoinstallDir(launcher); inbox != "" {
			if q := filepath.Join(inbox, goZipName); regularFile(q) {
				logDebug("greenovercast", "dropping queued %s", q)
				_ = os.Remove(q)
			}
		}
		tbxPost(func() {
			// portsInstallZip is main-loop only and returns at once
			// (store_scenes.go). PortMaster is opened here, in Done, not by
			// the queue: by then the screen that asked may be long gone.
			portsInstallZip(portsInstallRequest{
				Name: goClientName,
				Zip:  path,
				Done: func(res portsInstallResult) { goInstallDone(build, url, sum, size, path, screenUp, done, res) },
			})
		})
	}()
}

// goInstallDone settles an install the shared installer reported on. The
// record is written off the main loop (it syncs the card), then the rest
// runs back on the main loop in order. Main loop.
func goInstallDone(build goBuild, url, sum string, size int64, path string, screenUp func() bool, done func(portsInstallResult), res portsInstallResult) {
	finish := func() {
		goEndInstall()
		x1InvalidateScan()
		switch {
		case res.Installed:
			PlaySuccessSound()
			ShowToast(goClientName+" is installed ("+build.short()+")", ToastKindSuccess)
		case res.Queued:
			n, _ := pstoreQueueCounts()
			launcher := portMasterLauncher(portMasterSDRoot)
			if n == 0 && launcher != "" && (screenUp == nil || screenUp()) && !gameScreenSuspended() {
				mcOpenPortMaster(launcher, "PortMaster is installing "+goClientName+" — close it when it is done to come back")
			} else {
				ShowToast(goClientName+" is downloaded — PortMaster installs it the next time it opens", ToastKindSuccess)
			}
		default:
			msg := "the installer stopped"
			if res.Err != nil {
				msg = firstLine(res.Err.Error())
			}
			PlayErrorSound()
			ShowToast("Could not install "+goClientName+" — "+msg, ToastKindError)
		}
		if done != nil {
			done(res)
		}
	}
	go func() {
		switch {
		case res.Installed:
			goWriteMarker(goMarker{Build: build, URL: url, MD5: sum, Size: size, Installed: true})
		case res.Queued:
			goWriteMarker(goMarker{Build: build, URL: url, MD5: sum, Size: size})
		default:
			if regularFile(path) {
				_ = os.Remove(path)
			}
		}
		tbxPost(finish)
	}()
}

// goDownloadZip fetches the port zip into dir under a hidden name, checks it
// is the GreenOvercast port and only then gives it its real name. It returns
// the path and the address that answered.
func goDownloadZip(ctx context.Context, gen uint64, dir string, urls []string) (string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	final := filepath.Join(dir, goZipName)
	part := filepath.Join(dir, "."+goZipName+".part")
	_ = os.Remove(final)
	progress := func(done, total int64) { mcCardUpdate(gen, installPhaseDownload, done, total) }
	var lastErr error
	for _, u := range urls {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		mcCardUpdate(gen, installPhaseDownload, 0, 0)
		if err := downloadFileWithProgressCtx(ctx, u, part, progress); err != nil {
			_ = os.Remove(part)
			lastErr = err
			logDebug("greenovercast", "download %s: %v", u, err)
			continue
		}
		mcCardUpdate(gen, installPhaseVerify, 0, 0)
		if err := goCheckPortZip(part); err != nil {
			_ = os.Remove(part)
			lastErr = err
			continue
		}
		if err := mcSyncFile(part); err != nil {
			_ = os.Remove(part)
			return "", "", err
		}
		if err := os.Rename(part, final); err != nil {
			_ = os.Remove(part)
			return "", "", err
		}
		return final, u, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no download address")
	}
	return "", "", lastErr
}

// ── Launch ───────────────────────────────────────────────────────────────

// goLaunch starts the installed client. On older TrimUI firmware it obtains
// the private C runtime first, then retries through the tested loader path.
func goLaunch(name, launch, service, warn string) {
	if gameScreenSuspended() || launch == "" {
		return
	}
	if privateGlibcPortNeedsRuntime(launch) && !privateGlibcReady() {
		ShowToast("Downloading GreenOvercast's compatibility runtime…", ToastKindInfo)
		privateGlibcEnsureAsync(func(err error) {
			if err != nil {
				ShowToast("Could not prepare GreenOvercast — "+firstLine(err.Error()), ToastKindError)
				return
			}
			goLaunch(name, launch, service, warn)
		})
		return
	}
	name = setOr(name, goClientName)
	var caveats []string
	if warn != "" {
		caveats = append(caveats, warn)
	}
	if have, old := goGlibcOld(); old && !IsTrimuiDevice() {
		caveats = append(caveats, "this firmware has glibc "+have+"; GreenOvercast needs 2.38")
	}
	logDebug("greenovercast", "launch %s (%s) for %s", name, launch, service)
	if err := startPortScript(name, launch); err != nil {
		if !errors.Is(err, errPortPreparing) {
			ShowToast(firstLine(err.Error()), ToastKindError)
		}
		return
	}
	msg := "Starting " + goClientName + " — choose " + service + "; sign in with the code it shows, on your phone"
	if len(caveats) > 0 {
		ShowToast(msg+" ("+strings.Join(caveats, "; ")+")", ToastKindWarning)
		return
	}
	PlaySuccessSound()
	ShowToast(msg, ToastKindSuccess)
}
