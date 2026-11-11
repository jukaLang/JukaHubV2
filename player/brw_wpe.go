package main

// brw_wpe.go — hand a page to WPE Browser, the full web browser for the
// handheld.
//
// The reader is deliberately small: its own script engine, no WebKit. Some
// sites only open in a full browser (Google's robot check, web apps, video
// sites), and on a TrimUI handheld there is one: WPE Browser
// (github.com/matweew/wpe-tsp), a WPE WebKit build for the Smart Pro, Brick and
// Brick Pro with GPU rendering, hardware video and a gamepad pointer. It lives
// in exactly <SD>/Apps/WPE and is started by its launch.sh, which takes an
// address as its first argument (its start page otherwise).
//
// So the reader hands over instead of failing: "Open in WPE Browser" in the
// Browser menu, and first on the "needs a full web browser" card when WPE is
// installed. WPE runs as a screen-taking child exactly like an emulator
// (launchEmulatorEntry): its own process group, the main loop parked while it
// draws, the reaper handing the screen back when it exits — and the reader is
// still on the same page, because nothing about the page was torn down.
// Before WPE starts, JukaHub lets go of what it can rebuild (the texture and
// byte caches the memory-pressure path drops, the page cache, a running script
// pass): WebKit wants most of a 1 GB handheld.
//
// When WPE is not installed, the same places offer "Get WPE Browser"
// (brw_wpe_install.go). On a PC none of this applies: the desktop's own
// browser is one press away ("Open in web browser").

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const (
	brwWpeName        = "WPE Browser"
	brwWpeDirName     = "WPE"
	brwWpeLaunchFile  = "launch.sh"
	brwWpeReleaseFile = ".jukahub-release"
	brwWpeStatusTTL   = 5 * time.Second
	brwWpeMaxURL      = 4096
)

// brwWpeUnsupportedNote is what a handheld WPE's releases do not name hears.
const brwWpeUnsupportedNote = "WPE Browser runs on the Smart Pro, Brick and Brick Pro"

// The device seams, variables so tests can stand in a temporary SD card and a
// handheld: whether this is a TrimUI at all, whether its model is one WPE's
// releases name, and the card WPE lives on (it must be /mnt/SDCARD on a
// device; psRemoteSDRootDir honours JUKAHUB_SD_ROOT for a moved install).
var (
	brwWpeHandheldFn  = func() bool { return IsTrimuiDevice() }
	brwWpeSupportedFn = func() bool { return brwWpeModelSupported(DetectDevice()) }
	brwWpeSDRootFn    = defaultBrwWpeSDRoot
)

// defaultBrwWpeSDRoot is the card of a handheld, "" anywhere else.
func defaultBrwWpeSDRoot() string {
	if !IsTrimuiDevice() {
		return ""
	}
	return psRemoteSDRootDir()
}

// brwWpeModelSupported reports whether WPE's releases name the model: the
// Smart Pro since 0.1.0, the Brick (and the Hammer, a Brick variant) and the
// Brick Pro since 0.2.0.
func brwWpeModelSupported(d DeviceModel) bool {
	switch d {
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return true
	}
	return false
}

// brwWpeDir is where WPE lives on the card, "" without a card.
func brwWpeDir(sdRoot string) string {
	if strings.TrimSpace(sdRoot) == "" {
		return ""
	}
	return filepath.Join(sdRoot, "Apps", brwWpeDirName)
}

// brwWpeStatus is what the browser knows about WPE right now.
type brwWpeStatus struct {
	Handheld   bool   // a TrimUI handheld: WPE is the way to a full browser
	Supported  bool   // a model WPE's releases run on
	Installed  bool   // <SD>/Apps/WPE/launch.sh is there
	Dir        string // <SD>/Apps/WPE ("" without a card)
	Tag        string // the release JukaHub installed, "" when unknown
	Latest     string // the newest release seen this session, "" before a check
	Installing bool   // a Get or Update is running
}

// UpdateReady reports a newer release than the one JukaHub installed. An
// install JukaHub did not make has no tag, and is never nagged about.
func (s brwWpeStatus) UpdateReady() bool {
	return s.Installed && s.Tag != "" && s.Latest != "" && s.Latest != s.Tag
}

// brwWpeOffer is what the browser offers for WPE on this device right now.
type brwWpeOffer int

const (
	brwWpeOfferNone        brwWpeOffer = iota // not a handheld: the desktop's browser does this job
	brwWpeOfferOpen                           // installed: Open in WPE Browser
	brwWpeOfferGet                            // installable: Get WPE Browser
	brwWpeOfferBusy                           // being installed right now
	brwWpeOfferUnsupported                    // a handheld WPE's releases do not name
)

// offer picks the one thing to offer. An installed WPE opens even while an
// update downloads: the swap at the end waits until WPE has closed.
func (s brwWpeStatus) offer() brwWpeOffer {
	switch {
	case !s.Handheld:
		return brwWpeOfferNone
	case s.Installed:
		return brwWpeOfferOpen
	case s.Installing:
		return brwWpeOfferBusy
	case s.Supported:
		return brwWpeOfferGet
	}
	return brwWpeOfferUnsupported
}

// brwWpeReadTag is the release JukaHub recorded when it installed WPE, or ""
// for an install it did not make (or a file that is not a tag).
func brwWpeReadTag(dir string) string {
	f, err := os.Open(filepath.Join(dir, brwWpeReleaseFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := f.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	tag := strings.TrimSpace(line)
	if !brwWpeValidTag(tag) {
		return ""
	}
	return tag
}

// brwWpeValidTag reports whether a release tag is a plain version name: what
// GitHub's tags look like ("v0.3.1"), nothing a path or a shell would read.
func brwWpeValidTag(tag string) bool {
	if tag == "" || len(tag) > 64 {
		return false
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '+':
		default:
			return false
		}
	}
	return true
}

// brwWpeProbe looks at the card now. A stat and a small read: callers that run
// every frame use brwWpeStatusNow instead.
func brwWpeProbe() brwWpeStatus {
	s := brwWpeStatus{Handheld: brwWpeHandheldFn(), Installing: brwWpeInstalling.Load()}
	if !s.Handheld {
		return s
	}
	s.Supported = brwWpeSupportedFn()
	s.Dir = brwWpeDir(brwWpeSDRootFn())
	if s.Dir == "" {
		return s
	}
	s.Installed = regularFile(filepath.Join(s.Dir, brwWpeLaunchFile))
	if s.Installed {
		s.Tag = brwWpeReadTag(s.Dir)
	}
	s.Latest = brwWpeLatestTag()
	return s
}

// brwWpeCache keeps the last look for brwWpeStatusTTL: the menu, the card and
// Settings ask every frame they are drawn.
var brwWpeCache struct {
	mu     sync.Mutex
	at     time.Time
	status brwWpeStatus
}

// brwWpeStatusNow is the cached status. Safe from any goroutine. A fresh look
// also starts the background release check when one is due.
func brwWpeStatusNow() brwWpeStatus {
	brwWpeCache.mu.Lock()
	now := time.Now()
	fresh := brwWpeCache.at.IsZero() || now.Sub(brwWpeCache.at) > brwWpeStatusTTL
	if fresh {
		brwWpeCache.status = brwWpeProbe()
		brwWpeCache.at = now
	}
	s := brwWpeCache.status
	brwWpeCache.mu.Unlock()
	s.Installing = brwWpeInstalling.Load()
	s.Latest = brwWpeLatestTag()
	if fresh {
		brwWpeMaybeCheckLatest(s)
	}
	return s
}

// brwWpeInvalidate makes the next brwWpeStatusNow look at the card again: an
// install finished, or WPE was closed (it may have been removed meanwhile).
func brwWpeInvalidate() {
	brwWpeCache.mu.Lock()
	brwWpeCache.at = time.Time{}
	brwWpeCache.mu.Unlock()
}

// brwWpeLatest is the newest release tag a check saw this session, and when
// the last check started.
var brwWpeLatest struct {
	mu        sync.Mutex
	tag       string
	checkedAt time.Time
	checking  bool
}

func brwWpeLatestTag() string {
	brwWpeLatest.mu.Lock()
	defer brwWpeLatest.mu.Unlock()
	return brwWpeLatest.tag
}

// brwWpeNoteLatest records a release tag an install or a check just read.
func brwWpeNoteLatest(tag string) {
	brwWpeLatest.mu.Lock()
	brwWpeLatest.tag = tag
	brwWpeLatest.checkedAt = time.Now()
	brwWpeLatest.mu.Unlock()
}

// brwWpeMaybeCheckLatest asks GitHub for WPE's newest release in the
// background, at most once every brwWpeCheckEvery, and only for an installed
// WPE on a connected handheld with nothing else downloading it: that is what
// lets the menu and Settings say "Update" without a press to find out. Safe
// mode asks nothing, like the other background update checks.
func brwWpeMaybeCheckLatest(s brwWpeStatus) {
	if !s.Handheld || !s.Installed || s.Installing || !IsOnline() || gameScreenSuspended() || !safeModeAllows(SafeFeatureUpdates) {
		return
	}
	brwWpeLatest.mu.Lock()
	if brwWpeLatest.checking || (!brwWpeLatest.checkedAt.IsZero() && time.Since(brwWpeLatest.checkedAt) < brwWpeCheckEvery) {
		brwWpeLatest.mu.Unlock()
		return
	}
	brwWpeLatest.checking = true
	brwWpeLatest.checkedAt = time.Now()
	brwWpeLatest.mu.Unlock()
	go func() {
		defer func() {
			brwWpeLatest.mu.Lock()
			brwWpeLatest.checking = false
			brwWpeLatest.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rel, err := brwWpeFetchRelease(ctx, brwWpeHTTPClient())
		if err != nil {
			logDebug("browser", "WPE Browser release check: %v", err)
			// A failed look (offline, rate-limited) is tried again in a
			// quarter of an hour, not after the whole interval.
			brwWpeLatest.mu.Lock()
			brwWpeLatest.checkedAt = time.Now().Add(15*time.Minute - brwWpeCheckEvery)
			brwWpeLatest.mu.Unlock()
			return
		}
		brwWpeNoteLatest(rel.Tag)
	}()
}

// ── Starting WPE ─────────────────────────────────────────────────────────

// brwWpeGate orders a launch against the install's swap: the launch checks
// brwWpeSwapping and records its child under it, the install sets
// brwWpeSwapping and checks for a child under it, so WPE can never be started
// from a folder that is being replaced.
var brwWpeGate sync.Mutex

// brwWpeTarget is the address handed to WPE: an http(s) URL with a host and no
// credentials, written out again by the URL parser, or "" (WPE's start page)
// for anything else. It is one argv element; launch.sh reads it as $1.
func brwWpeTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > brwWpeMaxURL {
		return ""
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	u.Scheme = scheme
	return u.String()
}

// brwWpeFreeMemory lets go of what JukaHub can rebuild before WebKit needs the
// RAM: the same texture and byte caches the critical memory-pressure path
// drops (EvictCachesForPressure, adaptive.go), the reader's page cache (Back
// refetches) and a script pass still running. The page on screen stays, so
// the reader comes back to it. The parked loop draws nothing, so nothing
// refills the caches while WPE runs. Main thread.
func brwWpeFreeMemory() {
	webJSStopRunning()
	browserClearPageCache()
	conditionalCache.clear()
	clearThumbnailDataCache()
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	ClearBgGradientCache()
	clearHomeTextCache()
	clearThumbnailCache()
	clearAppIconCache()
	clearChatImageCache()
	clearChatAvatarCache()
	// An animated wallpaper's frames are the largest set of textures JukaHub
	// holds (up to 40 MB on the handheld, gifwall.go); the parked loop shows
	// none of them, and the first backdrop drawn after WPE decodes it again.
	gifWallEvict()
	go debug.FreeOSMemory()
}

// brwWpeLaunch opens address in WPE Browser ("" or anything that is not a web
// address opens its start page) and reports whether WPE started. Main thread.
func brwWpeLaunch(config *Config, address string) bool {
	s := brwWpeProbe()
	if !s.Handheld {
		return false
	}
	if !s.Installed {
		brwWpeInvalidate()
		PlayNavSound()
		ShowToast(brwWpeName+" isn't installed · the Browser menu can get it", ToastKindInfo)
		return false
	}
	if gameScreenSuspended() {
		return false
	}
	target := brwWpeTarget(address)
	args := []string{filepath.Join(s.Dir, brwWpeLaunchFile)}
	if target != "" {
		args = append(args, target)
	}
	// sh reads the launch script; the address is an operand, never code.
	cmd := exec.Command("sh", args...)
	cmd.Dir = s.Dir
	// Own process group, the emulators' rule: launch.sh may start WebKit's
	// processes and return, and the screen stays parked until all are gone.
	// With cmd.Env left nil it also gives WPE the environment the firmware's
	// menu would have (childenv.go), not JukaHub's library path.
	screenChildGroupConfigure(cmd)

	brwWpeGate.Lock()
	if brwWpeSwapping.Load() {
		brwWpeGate.Unlock()
		ShowToast(brwWpeName+" is being updated · try again in a moment", ToastKindInfo)
		return false
	}
	brwWpeFreeMemory()
	restoreProfile := beginGamePowerProfile(config)
	if err := cmd.Start(); err != nil {
		brwWpeGate.Unlock()
		restoreProfile()
		logWarn("browser", "WPE Browser did not start: %v", err)
		ShowToast("Couldn't start "+brwWpeName+": "+firstLine(err.Error()), ToastKindError)
		return false
	}
	noteGameChildStarted(cmd)
	brwWpeGate.Unlock()
	where := "its start page"
	if target != "" {
		// The host only: a search address carries what was searched for.
		where = brwHostOf(target)
	}
	logInfo("browser", "WPE Browser started on %s; the reader parks until it closes", where)
	go func() {
		_ = cmd.Wait()
		screenChildReaped(cmd)
		restoreProfile()
		brwWpeInvalidate()
		logInfo("browser", "WPE Browser closed; back to the reader")
	}()
	PlayActivateSound()
	return true
}

// ── The "needs a full web browser" card ──────────────────────────────────

// brwWpeWallButtonsFor is the wall card's buttons with WPE in them. Installed,
// WPE comes first and "Always use Startpage" makes room, the way the desktop's
// card puts its own browser first; installable, "Get WPE Browser" takes the
// third place from "Change engine" (Settings → Browser still has every
// engine). Anything else keeps the card's own buttons. The card has room for
// three.
func brwWpeWallButtonsFor(base []brwCardButton, s brwWpeStatus) []brwCardButton {
	switch s.offer() {
	case brwWpeOfferOpen:
		out := make([]brwCardButton, 0, 3)
		out = append(out, brwCardButton{action: brwActWpeOpen, label: "Open in " + brwWpeName, icon: "browser"})
		for _, b := range base {
			if b.action != brwActWallAlways && len(out) < 3 {
				out = append(out, b)
			}
		}
		return out
	case brwWpeOfferGet:
		out := make([]brwCardButton, 0, 3)
		for _, b := range base {
			if b.action != brwActWallEngines && len(out) < 2 {
				out = append(out, b)
			}
		}
		return append(out, brwCardButton{action: brwActWpeGet, label: "Get " + brwWpeName, icon: "download"})
	}
	return base
}

// brwWpeWallCache keeps the card's list between frames (render thread only):
// the card asks for its buttons every frame it is drawn.
var brwWpeWallCache struct {
	offer brwWpeOffer
	base  []brwCardButton
	out   []brwCardButton
}

// brwWpeWallButtons is brwWpeWallButtonsFor for the device as it is now.
func brwWpeWallButtons(base []brwCardButton) []brwCardButton {
	s := brwWpeStatusNow()
	o := s.offer()
	c := &brwWpeWallCache
	if c.out != nil && c.offer == o && len(c.base) == len(base) && (len(base) == 0 || &c.base[0] == &base[0]) {
		return c.out
	}
	c.offer, c.base, c.out = o, base, brwWpeWallButtonsFor(base, s)
	return c.out
}

// brwWpeWallDetailFor rewrites the card's second sentence (wayOut, the one
// browser_engines.go ends the detail with) when the first button opens WPE.
func brwWpeWallDetailFor(detail, wayOut, alt string, open bool) string {
	if !open || wayOut == "" || !strings.HasSuffix(detail, wayOut) {
		return detail
	}
	return strings.TrimSuffix(detail, wayOut) + "Open it in " + brwWpeName + ", or try " + alt + ": Google's results without the check."
}

// brwWpeWallDetail is brwWpeWallDetailFor for the device as it is now.
func brwWpeWallDetail(detail string) string {
	open := brwWpeStatusNow().offer() == brwWpeOfferOpen
	if !open {
		return detail
	}
	return brwWpeWallDetailFor(detail, brwWallWayOut(), brwWallAltEngine().Name, open)
}

// brwWpeWallActivate runs the card's WPE buttons: the search that hit the wall
// opens in WPE, or the install starts.
func brwWpeWallActivate(config *Config, f brwFailure, action brwCardAction) {
	switch action {
	case brwActWpeOpen:
		brwWpeLaunch(config, f.URL)
	case brwActWpeGet:
		brwWpeStartInstall(false)
	}
}

// ── The Browser menu's row ───────────────────────────────────────────────

// brwWpeMenuEntry is the Browser menu's hand-over row as it stands: its words,
// glyph, read-out and tone, and whether it can act. hasPage says a page is on
// screen; on a PC the row opens it in the desktop's browser, so it needs one.
func brwWpeMenuEntry(hasPage bool) (label, icon, value string, tone int, disabled bool) {
	s := brwWpeStatusNow()
	switch s.offer() {
	case brwWpeOfferNone:
		return "Open in web browser", "link", "", brwToneNeutral, !hasPage
	case brwWpeOfferOpen:
		switch {
		case s.Installing:
			value = "Updating…"
		case s.UpdateReady():
			value, tone = "Update "+s.Latest+" in Settings", brwToneGood
		}
		return "Open in " + brwWpeName, "browser", value, tone, false
	case brwWpeOfferGet:
		return "Get " + brwWpeName, "download", "Full web browser", brwToneNeutral, false
	case brwWpeOfferBusy:
		return "Installing " + brwWpeName, "download", brwWpeProgressLabel(), brwToneNeutral, true
	}
	return brwWpeName, "browser", "Not for this model", brwToneNeutral, true
}

// brwWpeProgressLabel is the install's progress as a short read-out.
func brwWpeProgressLabel() string {
	visible, tool, phase, _, done, total, _, _, _ := installBarSnapshot()
	if !visible || tool != brwWpeName {
		return "Working…"
	}
	if phase == installPhaseDownload && total > 0 && done >= 0 && done <= total {
		return itoa(int(done*100/total)) + "%"
	}
	return phase
}

// brwWpeMenuDisabledNote is what A on the dimmed row says.
func brwWpeMenuDisabledNote() string {
	switch brwWpeStatusNow().offer() {
	case brwWpeOfferBusy:
		return brwWpeName + " is downloading · the progress bar shows how far"
	case brwWpeOfferNone:
		return "Open a page first"
	}
	return brwWpeUnsupportedNote
}

// brwWpeMenuActivate runs the row: the page on screen opens in WPE (or in the
// desktop's browser on a PC), or the install starts.
func brwWpeMenuActivate(config *Config) {
	page := brwInfoPageURL(config)
	switch brwWpeStatusNow().offer() {
	case brwWpeOfferNone:
		if page == "" {
			PlayNavSound()
			ShowToast("Open a page first", ToastKindInfo)
			return
		}
		if err := openExternalURL(page); err != nil {
			ShowToast("Couldn't open your web browser: "+err.Error(), ToastKindWarning)
			return
		}
		PlayActivateSound()
		ShowToast("Opened in your web browser", ToastKindInfo)
	case brwWpeOfferOpen:
		brwWpeLaunch(config, page)
	case brwWpeOfferGet:
		brwWpeStartInstall(false)
	default:
		PlayNavSound()
		ShowToast(brwWpeMenuDisabledNote(), ToastKindInfo)
	}
}

// ── Settings → Browser → WPE Browser ─────────────────────────────────────

// brwWpeSettingsRow is the install, update and version row, on the handheld
// only. A gets WPE when it is missing and checks for (and installs) a newer
// release when it is there.
func brwWpeSettingsRow() setRow {
	return setRow{
		ID: "wpeBrowser", Label: brwWpeName, Kind: setKindAction,
		Detail:  func(*Config) string { return brwWpeSettingsDetail(brwWpeStatusNow()) },
		Visible: func(*Config) bool { return brwWpeHandheldFn() },
		Value:   func(*Config) string { return brwWpeSettingsValue(brwWpeStatusNow()) },
		Open:    func(*Config) { brwWpeSettingsOpen() },
	}
}

// brwWpeSettingsDetail is the row's second line.
func brwWpeSettingsDetail(s brwWpeStatus) string {
	switch {
	case s.Installed:
		return "Pages open in it from the Browser menu · choose to check for an update"
	case s.Supported:
		return "A full WebKit browser for sites the reader can't show · choose to install"
	}
	return brwWpeUnsupportedNote
}

// brwWpeSettingsValue is the row's read-out.
func brwWpeSettingsValue(s brwWpeStatus) string {
	switch {
	case s.Installing:
		return "Downloading…"
	case s.UpdateReady():
		return "Update: " + s.Latest
	case s.Installed && s.Tag != "":
		return s.Tag
	case s.Installed:
		return "Installed"
	case s.Supported:
		return "Not installed"
	}
	return "Not available"
}

// brwWpeSettingsOpen is the row's press.
func brwWpeSettingsOpen() {
	s := brwWpeStatusNow()
	switch {
	case s.Installing:
		ShowToast(brwWpeName+" is downloading · the progress bar shows how far", ToastKindInfo)
	case s.Installed:
		brwWpeStartInstall(true)
	case s.Supported:
		brwWpeStartInstall(false)
	default:
		ShowToast(brwWpeUnsupportedNote, ToastKindInfo)
	}
}
