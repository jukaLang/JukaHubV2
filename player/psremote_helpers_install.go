package main

// psremote_helpers_install.go — PS Remote Play installs its own helpers.
//
// Streaming needs chiaki-stream and a PS5 pairing needs chiaki-regist, the two
// native helpers in Apps/JukaHub/bin (psremote.go). A JukaHub whose package
// had no bin/ — every release before the helpers were built in CI, a copy of
// just the binary, a release whose helper build failed — stopped at "the PS5
// pairing helper is missing (chiaki-regist)" and left the user to find files.
// Now the panel fetches them itself, once:
//
//   - Which release: the newest JukaHub release (stable before beta, up to five
//     pages of the release list) that carries JukaHub-RemotePlay-linux-arm64.zip,
//     the two helpers on their own (docker-image.yml and release.sh attach it
//     beside the app archives). A release whose helper build failed has none,
//     so an older one is used: the helpers and JukaHub talk through files both
//     sides keep compatible (player/native/README.md), so they need not come
//     from the same release.
//   - Trust, as for an app update (autoupdate.go): HTTPS to the GitHub hosts
//     on the Patch allowlist only; the archive must match the digest GitHub
//     publishes for it, and a release that carries a signed release.json must
//     verify (one whose manifest does not list the package is passed over for
//     an older release); the unpacked helpers must match the archive's own
//     SHA256SUMS.
//   - Will it run: each helper about to be installed is started once from the
//     unpacked copy, the way a stream's preflight starts it
//     (psremote_preflight.go). One the firmware's loader refuses is not
//     installed (the other still is), the panel says why, and the refusal is
//     remembered in bin/.remote-play-refused: only Retry downloads again.
//   - Install: into bin/ beside the running binary, where an update unpacks a
//     release's bin/. Only a missing helper is written, never one that is
//     there; each through a temporary name, synced, read back and renamed into
//     place, chiaki-stream last, so a half-done install never looks complete.
//
// It usually happens in the background, soon after JukaHub starts
// (psremote_helpers_bg.go), and the panel starts it as it opens
// (psHelpersAutoStart) when that has not. A PS5's PIN is asked
// for only once chiaki-regist is in (psHelpersHoldPIN: the PIN Link Device
// shows lasts five minutes, a download can take longer), and so is the PIN of
// an address typed by hand while an install runs (it may be a PS5). Play waits
// and then streams (psHelpersHoldPlay). B, or closing the panel, lets go of a
// waiting press; the activity chip's X stops the install itself (psHelpersStop).
// Nothing runs where Remote Play cannot (psRemoteSupported) or when both
// helpers are there.

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
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
	// psHelperAsset is the release asset that holds the helpers:
	// bin/chiaki-stream, bin/chiaki-regist, bin/BUILDINFO.txt and SHA256SUMS.
	psHelperAsset = "JukaHub-RemotePlay-linux-arm64.zip"
	// psHelperMaxAsset bounds the download; the package is a few megabytes.
	psHelperMaxAsset = 128 << 20
	// psHelperMaxUnpacked and psHelperMaxEntries bound what it may unpack to.
	psHelperMaxUnpacked = 256 << 20
	psHelperMaxEntries  = 32
	// psHelperPerPage and psHelperMaxPages bound the release search: every
	// push to main publishes a release, so a helper build broken for a few
	// days pushes the last good package off the first page.
	psHelperPerPage  = 20
	psHelperMaxPages = 5
	// psHelperMaxCandidates is how many releases with the package are tried
	// when a signed manifest does not list it.
	psHelperMaxCandidates = 3
	// psHelperAutoRetry is how long a failed install keeps the panel's opening
	// from starting another; Retry on its row starts one at once.
	psHelperAutoRetry = 2 * time.Minute
	// psHelperTimeout bounds one install, the download included.
	psHelperTimeout = 20 * time.Minute
	// psHelperPairWait bounds how long a PS5 pairing whose helper went missing
	// after its PIN was asked for waits for the install.
	psHelperPairWait = 3 * time.Minute
	// psHelperDownloadTries is how often one install starts the download: a
	// download cut off after it made progress is resumed at once.
	psHelperDownloadTries = 3
	// psHelperWorkDirName is the work folder in bin/: the download, kept after
	// a dropped connection so the next try resumes it, and the unpacked copy.
	psHelperWorkDirName = ".remote-play-install"
	// psHelperRefusedName remembers, beside the helpers, the release whose
	// helpers this firmware refused and why.
	psHelperRefusedName = ".remote-play-refused"
	// psActionInstallHelpers is the panel row that installs them (numbered
	// apart from the panel's other action lists, like psremote_wake.go's).
	psActionInstallHelpers psPanelAction = 300
	// The pairing's Last-error lines when the helper it needed did not come:
	// a finished install clears them.
	psPairCodeNoHelpers       = "Not paired: Remote Play did not install (see its row)"
	psPairCodeStillInstalling = "Not paired yet: Remote Play was still installing"
	// psHelperRefusedPrefix starts a refusal's reason (psHelpersFailText); the
	// panel's row and the phone's card show what follows it.
	psHelperRefusedPrefix = "it won't run on this firmware · "
)

// psHelperReleasesAPI lists JukaHub's releases, newest first, assets included.
var psHelperReleasesAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases"

// Test seams.
var (
	// psHelperInstallDirOverride replaces bin/ beside the binary.
	psHelperInstallDirOverride string
	psHelperClientFn           = newPatchClient
	psHelperSupportedFn        = psRemoteSupported
	// psHelperProbeFn says why unpacked helpers would not start here ("" when
	// they do, or when that cannot be told). An empty path is not probed.
	psHelperProbeFn = psHelperProbeStaged
)

var (
	errPSHelpersNotPublished = errors.New("no recent JukaHub release has the Remote Play helpers")
	errPSHelpersOffline      = errors.New("could not reach GitHub")
	errPSHelpersNoInternet   = errors.New("this handheld has no internet right now")
	errPSHelpersCutOff       = errors.New("the download was cut off")
	errPSHelpersRateLimited  = errors.New("GitHub is refusing requests for now")
	errPSHelpersClock        = errors.New("a certificate or manifest does not fit this handheld's date")
	errPSHelpersTrust        = errors.New("this handheld cannot check GitHub's certificate")
	errPSHelpersNoSpace      = errors.New("SD card full")
	errPSHelpersSDWrite      = errors.New("the SD card refused a write")
	errPSHelpersUnverified   = errors.New("the Remote Play download did not verify")
	errPSHelpersUnlisted     = errors.New("the release's signed manifest does not list the helper package")
	errPSHelpersWontRun      = errors.New("the Remote Play helpers won't run on this firmware")
	errPSHelpersMissing      = errors.New("the Remote Play helpers are not installed")
	// errPSHelpersStopped is an install the user stopped (psHelpersStop).
	errPSHelpersStopped = errors.New("the Remote Play download was stopped")
	// errPSPairNoHelpers marks a pairing that never reached the console: the
	// helper it needs did not install. errPSPairStillInstalling is one that
	// stopped waiting for the install.
	errPSPairNoHelpers       = errors.New("Remote Play is not installed yet")
	errPSPairStillInstalling = errors.New("Remote Play is still installing")
)

// ── Where they go ────────────────────────────────────────────────────────

// psHelperInstallDir is bin/ beside the running binary: where an update
// unpacks a release's bin/, and where the installer writes. On a handheld it
// is Apps/JukaHub/bin, the folder psBinDir names.
func psHelperInstallDir() string {
	if dir := strings.TrimSpace(psHelperInstallDirOverride); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return psBinDir()
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "bin")
}

// psHelperDirs is where psRemoteEngine looks for the helpers, in order: the
// install folder, then the SD card's Apps/JukaHub/bin (the same folder on a
// handheld; different only for a binary started from elsewhere).
func psHelperDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	for _, dir := range []string{psHelperInstallDir(), psBinDir()} {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		key := filepath.Clean(dir)
		if seen[key] {
			continue
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// psHelperTargetDir is where a missing helper goes: beside a chiaki-stream
// psRemoteEngine found in one of its folders (it takes chiaki-regist from the
// same one), else the install folder.
func psHelperTargetDir() string {
	engine := psRemoteEngine()
	if strings.TrimSpace(engine.Stream) != "" && engine.Root != "" {
		for _, dir := range psHelperDirs() {
			if filepath.Clean(dir) == filepath.Clean(engine.Root) {
				return engine.Root
			}
		}
	}
	return psHelperInstallDir()
}

// psHelperSeen caches which helpers psRemoteEngine finds: the panel asks every
// frame, and the answer only changes when an install ends.
var psHelperSeen struct {
	mu     sync.Mutex
	at     time.Time
	stream bool
	regist bool
}

// psHelpersPresent reports whether chiaki-stream and chiaki-regist are there,
// looked up at most every two seconds.
func psHelpersPresent() (stream, regist bool) {
	psHelperSeen.mu.Lock()
	if !psHelperSeen.at.IsZero() && time.Since(psHelperSeen.at) < 2*time.Second {
		stream, regist = psHelperSeen.stream, psHelperSeen.regist
		psHelperSeen.mu.Unlock()
		return stream, regist
	}
	psHelperSeen.mu.Unlock()
	// Looked up outside the lock: the card is slow, and the main loop asks.
	engine := psRemoteEngine()
	stream = strings.TrimSpace(engine.Stream) != ""
	regist = strings.TrimSpace(engine.Regist) != ""
	psHelperSeen.mu.Lock()
	psHelperSeen.stream, psHelperSeen.regist, psHelperSeen.at = stream, regist, time.Now()
	psHelperSeen.mu.Unlock()
	return stream, regist
}

// psHelpersForgetPresence makes the next psHelpersPresent look again.
func psHelpersForgetPresence() {
	psHelperSeen.mu.Lock()
	psHelperSeen.at = time.Time{}
	psHelperSeen.mu.Unlock()
}

// psHelpersNeeded reports whether Remote Play runs here and a helper is
// missing.
func psHelpersNeeded() bool {
	if !psHelperSupportedFn() {
		return false
	}
	stream, regist := psHelpersPresent()
	return !stream || !regist
}

// ── A refusal, remembered ────────────────────────────────────────────────

// psHelperSaveRefusal remembers that this firmware refused the helpers of tag,
// and why, so later starts of JukaHub do not download them again by
// themselves.
func psHelperSaveRefusal(dir, tag, problem string) {
	body := strings.TrimSpace(tag) + "\n" + strings.ReplaceAll(strings.TrimSpace(problem), "\n", " ") + "\n"
	if err := os.WriteFile(filepath.Join(dir, psHelperRefusedName), []byte(body), 0o644); err != nil {
		logInfo("psremote", "could not note the refused helpers: %v", err)
	}
}

// psHelperLoadRefusal reads what psHelperSaveRefusal kept.
func psHelperLoadRefusal() (tag, problem string, ok bool) {
	data, err := os.ReadFile(filepath.Join(psHelperTargetDir(), psHelperRefusedName))
	if err != nil || len(data) > 4096 {
		return "", "", false
	}
	tag, problem, _ = strings.Cut(strings.TrimSpace(string(data)), "\n")
	tag, problem = strings.TrimSpace(tag), strings.TrimSpace(problem)
	if problem == "" {
		return "", "", false
	}
	return tag, problem, true
}

// psHelperClearRefusal forgets it: the helpers installed, or Retry asked for
// another look.
func psHelperClearRefusal() {
	_ = os.Remove(filepath.Join(psHelperTargetDir(), psHelperRefusedName))
}

// psHelperRefusedWhich names the refused helpers in a problem: "stream",
// "regist" or "both".
func psHelperRefusedWhich(problem string) string {
	stream, regist := strings.Contains(problem, "chiaki-stream"), strings.Contains(problem, "chiaki-regist")
	switch {
	case stream && regist:
		return "both"
	case regist:
		return "regist"
	}
	return "stream"
}

// ── The install job ──────────────────────────────────────────────────────

type psHelperPhase int

const (
	psHelperIdle        psHelperPhase = iota
	psHelperLooking                   // asking GitHub which release has them
	psHelperDownloading               // the archive is coming in
	psHelperChecking                  // unpacking, checking, test-starting
	psHelperInstalled                 // done, in this panel session
	psHelperFailed                    // the last install failed (reason)
)

// psHelperView is what the panel shows of the install.
type psHelperView struct {
	phase psHelperPhase
	got   int64
	total int64
	tag   string
	// reason is why the last install failed, worded for the panel; refused
	// names the helpers the firmware refused ("stream", "regist", "both").
	reason  string
	refused string
	// placing is the last step: the checked helpers are being copied into
	// bin/, and a stop is no longer taken (psHelpersStop).
	placing bool
}

// psHelperRun is one install: done closes when it has ended, after err is set.
// cancel ends its context early; stopped (under psHelperJob.mu) says the user
// asked for that, so the run ends as errPSHelpersStopped; settled (under the
// same lock) says the install has returned and a stop comes too late.
type psHelperRun struct {
	done    chan struct{}
	err     error
	cancel  context.CancelFunc
	stopped bool
	settled bool
}

// psHelperJob is the one install at a time. Its fields are written by the
// worker and read by the panel, both under mu; a run's err is read after its
// done has closed.
var psHelperJob struct {
	mu      sync.Mutex
	view    psHelperView
	run     *psHelperRun
	running bool
	endedAt time.Time
	// waiters counts pairings blocked in psHelpersWait: they report the
	// result themselves, so the worker's own toast stays quiet.
	waiters int
	// next is the press waiting for the install (psHelpersHold), run on the
	// main loop when it ends; nextKind ("play" or "pin"), nextHost and
	// nextLabel say whose it is.
	next      func(error)
	nextKind  string
	nextHost  string
	nextLabel string
}

// psHelperStartMode is who asks for an install.
type psHelperStartMode int

const (
	// psStartAuto is the panel's opening: not after a recent failure, nor
	// after a refusal.
	psStartAuto psHelperStartMode = iota
	// psStartPress is a press that needs the helpers (Play, a PS5's PIN):
	// not after a refusal, which no download changes.
	psStartPress
	// psStartRetry is the install row: always, a remembered refusal
	// forgotten.
	psStartRetry
)

// psHelpersView returns the install's state for drawing.
func psHelpersView() psHelperView {
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	return psHelperJob.view
}

// psHelpersRunning reports whether an install is under way.
func psHelpersRunning() bool {
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	return psHelperJob.running
}

// psHelpersRefusal is the last install's refusal by this firmware (nil when
// it was not one, or one runs now).
func psHelpersRefusal() error {
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	if psHelperJob.running || psHelperJob.run == nil || !errors.Is(psHelperJob.run.err, errPSHelpersWontRun) {
		return nil
	}
	return psHelperJob.run.err
}

// psHelperSetView is the worker's progress report.
func psHelperSetView(v psHelperView) {
	psHelperJob.mu.Lock()
	if psHelperJob.running {
		psHelperJob.view = v
	}
	psHelperJob.mu.Unlock()
}

// psHelpersStart starts an install unless one is running, nothing is missing,
// or mode says to leave the last failure be (psHelperStartMode). It reports
// whether an install is running afterwards.
func psHelpersStart(mode psHelperStartMode) bool {
	if !psHelpersNeeded() {
		return false
	}
	if mode == psStartRetry {
		psHelperClearRefusal()
	}
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	if psHelperJob.running {
		return true
	}
	if last := psHelperJob.run; last != nil && last.err != nil {
		refused := errors.Is(last.err, errPSHelpersWontRun)
		if mode == psStartAuto && (refused || time.Since(psHelperJob.endedAt) < psHelperAutoRetry) {
			return false
		}
		if mode == psStartPress && refused {
			return false
		}
	}
	// The context is made here, under the lock, so a stop that comes before
	// the worker's first step still reaches it (psHelpersStop).
	ctx, cancel := context.WithTimeout(context.Background(), psHelperTimeout)
	run := &psHelperRun{done: make(chan struct{}), cancel: cancel}
	psHelperJob.run = run
	psHelperJob.running = true
	psHelperJob.view = psHelperView{phase: psHelperLooking}
	go psHelpersWorker(ctx, run)
	return true
}

// psHelpersStop stops the running install: the activity chip's X
// (psremote_helpers_bg.go). A download keeps what it has for the next try, and
// the install ends as errPSHelpersStopped, which no background try repeats by
// itself. It reports whether it stopped one: false when none runs, or when it
// is already copying the helpers into place or has returned, where a stop
// would only be announced and not happen.
func psHelpersStop() bool {
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	run := psHelperJob.run
	if !psHelperJob.running || run == nil || run.cancel == nil || run.settled || psHelperJob.view.placing {
		return false
	}
	run.stopped = true
	// Cancelled under the lock: the install marks its copy into bin/ under it
	// too (placing), and checks the context right after, so a stop either
	// lands before that and is seen, or is refused here.
	run.cancel()
	return true
}

// psHelpersWorker is the goroutine behind psHelpersStart.
func psHelpersWorker(ctx context.Context, run *psHelperRun) {
	defer run.cancel()
	started := time.Now()
	tag, err := psInstallHelpers(ctx, psHelperSetView)
	psHelperJob.mu.Lock()
	stopped := run.stopped
	run.settled = true
	psHelperJob.mu.Unlock()
	if stopped && err != nil && !errors.Is(err, errPSHelpersWontRun) {
		// Whatever step the stop cut short, it was the user's; a refusal by
		// the firmware is kept, since it is saved and must be shown.
		err = errPSHelpersStopped
	}
	psHelpersForgetPresence()
	if err == nil && psHelpersNeeded() {
		// Written, yet the engine lookup does not see them.
		err = fmt.Errorf("they were installed in %s, but JukaHub does not find them there", psHelperTargetDir())
	}
	if err == nil && tag != "" {
		// Answered here, off the main loop, so the first Play (a held one
		// included) does not wait for the stream helper's one test start.
		psWarmStreamPreflight()
	}
	reason := psHelpersFailText(err)
	switch {
	case errors.Is(err, errPSHelpersStopped):
		logInfo("psremote", "the Remote Play download was stopped after %s", time.Since(started).Round(time.Second))
	case err != nil:
		logWarn("psremote", "installing the Remote Play helpers failed after %s: %v", time.Since(started).Round(time.Second), err)
	case tag != "":
		logInfo("psremote", "Remote Play helpers installed from %s into %s in %s", tag, psHelperTargetDir(), time.Since(started).Round(time.Second))
	}

	psHelperJob.mu.Lock()
	psHelperJob.running = false
	psHelperJob.endedAt = time.Now()
	switch {
	case err != nil:
		refused := ""
		if errors.Is(err, errPSHelpersWontRun) {
			refused = psHelperRefusedWhich(err.Error())
		}
		psHelperJob.view = psHelperView{phase: psHelperFailed, tag: tag, reason: reason, refused: refused}
	case tag != "":
		psHelperJob.view = psHelperView{phase: psHelperInstalled, tag: tag}
	default:
		// Nothing was missing by the time it looked.
		psHelperJob.view = psHelperView{}
	}
	next := psHelperJob.next
	psHelperJob.next, psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.nextLabel = nil, "", "", ""
	waited := psHelperJob.waiters > 0
	run.err = err
	psHelperJob.mu.Unlock()

	// Queued before done closes, so whatever a waiter does next on the main
	// loop comes after it.
	tbxPost(func() {
		if err == nil {
			psPanelClearHelperCode()
		}
		switch {
		case errors.Is(err, errPSHelpersStopped):
			// The stop said so itself (psHelpersStopFromChip), and a press
			// still waiting from an earlier visit of the panel goes quietly.
		case next != nil:
			next(err)
		case waited:
			// A pairing was waiting: it goes on, or says why it cannot.
		case psHelpersQuietNow():
			// The PIN keyboard or the Account-ID card is up: its own
			// instructions stay on screen, and the row says the rest.
		case err == nil && tag != "":
			ShowToast("PS Remote Play is installed", ToastKindSuccess)
		case err != nil && chiakiPanel.open:
			ShowToast("Could not install Remote Play — "+reason, ToastKindError)
		}
	})
	close(run.done)
}

// psPanelClearHelperCode drops a pairing's Last-error line that said the
// helpers were missing, once they are in. Main loop only.
func psPanelClearHelperCode() {
	psPanelMu.Lock()
	if chiakiPanel.code == psPairCodeNoHelpers || chiakiPanel.code == psPairCodeStillInstalling {
		chiakiPanel.code = ""
	}
	psPanelMu.Unlock()
}

// psHelpersQuietNow reports whether the panel shows something a toast would
// cover: the PS Remote keyboard or the Account-ID card. Main loop only.
func psHelpersQuietNow() bool {
	if !chiakiPanel.open {
		return false
	}
	return (virtualKeyboardActive && vkTarget == vkTargetPSRemote) || chiakiPanel.page == psPageAccount
}

// psHelpersPanelFree reports whether the panel is on its console list with
// nothing over it and nothing running, where a held press may act. Main loop
// only.
func psHelpersPanelFree() bool {
	if !chiakiPanel.open || chiakiPanel.page != psPageConsoles || virtualKeyboardActive {
		return false
	}
	if tb.startOpen || nc.open || qs.open || qrZoomActive() {
		return false
	}
	psPanelMu.Lock()
	busy := chiakiPanel.busy
	psPanelMu.Unlock()
	return busy == ""
}

// psHelpersWait blocks until the running (or the last) install has ended and
// returns how it went: nil when the helpers are there.
func psHelpersWait(ctx context.Context) error {
	psHelperJob.mu.Lock()
	run, running := psHelperJob.run, psHelperJob.running
	if running {
		psHelperJob.waiters++
	}
	psHelperJob.mu.Unlock()
	if run == nil {
		psHelpersForgetPresence()
		if !psHelpersNeeded() {
			return nil
		}
		return errPSHelpersMissing
	}
	if running {
		defer func() {
			psHelperJob.mu.Lock()
			psHelperJob.waiters--
			psHelperJob.mu.Unlock()
		}()
	}
	// An install that has just ended may still be posting its result:
	// done closes after that, so waiting on it holds for a moment at most.
	select {
	case <-run.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if run.err != nil {
		return run.err
	}
	psHelpersForgetPresence()
	if psHelpersNeeded() {
		return errPSHelpersMissing
	}
	return nil
}

// psHelpersEnsure installs missing helpers, or waits for the install that is
// running, and returns how it ended (nil when nothing was missing).
func psHelpersEnsure(ctx context.Context) error {
	if !psHelpersNeeded() {
		return nil
	}
	psHelpersStart(psStartPress)
	return psHelpersWait(ctx)
}

// psHelperGlibcGap is elfcompat's "needs GLIBC_2.38; this firmware's libc.so.6
// has up to GLIBC_2.33", which psHelperCompact shortens for a panel row.
var psHelperGlibcGap = regexp.MustCompile(`needs (\S+); this firmware's \S+ has up to (\S+)`)

// psHelperCompact shortens a loader verdict to fit a row.
func psHelperCompact(problem string) string {
	return psHelperGlibcGap.ReplaceAllString(strings.TrimSpace(problem), "needs $1 · firmware has $2")
}

// psHelperNoInternetText is how offline reads: no internet, or a Wi-Fi that
// wants a sign-in first.
func psHelperNoInternetText() string {
	if PortalSignInURL() != "" {
		return "the Wi-Fi wants a sign-in first"
	}
	return "this handheld has no internet right now"
}

// psHelpersFailText words an install failure for the panel and its toasts:
// short enough for one row on the Smart Pro, the action last.
func psHelpersFailText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errPSHelpersWontRun):
		problem := strings.TrimPrefix(err.Error(), errPSHelpersWontRun.Error()+": ")
		return psHelperRefusedPrefix + psHelperCompact(problem)
	case errors.Is(err, errPSHelpersNoSpace):
		// It carries the numbers when it knows them.
		return firstLine(err.Error())
	case errors.Is(err, errPSHelpersNoInternet):
		if PortalSignInURL() != "" {
			return "sign in to the Wi-Fi first · then try again"
		}
		return "no internet right now · try again once there is"
	case errors.Is(err, errPSHelpersSDWrite):
		return "can't write to the SD card · check it on a PC, then try again"
	case errors.Is(err, errPSHelpersOffline):
		return "no connection to GitHub · check the Wi-Fi, then try again"
	case errors.Is(err, errPSHelpersCutOff):
		return "the download was cut off · try again to resume it"
	case errors.Is(err, errPSHelpersRateLimited):
		return "GitHub is limiting downloads · try again in a few minutes"
	case errors.Is(err, errPSHelpersClock):
		return "the clock is wrong · set the date and time, then try again"
	case errors.Is(err, errPSHelpersTrust):
		return "can't verify GitHub · copy JukaHub's package from a PC"
	case errors.Is(err, errPSHelpersNotPublished):
		return "no recent JukaHub release has them · try after an update"
	case errors.Is(err, errPSHelpersUnverified):
		return "the download did not verify · try again"
	case errors.Is(err, errPSHelpersStopped):
		return "the download was stopped · try again to resume it"
	case errors.Is(err, context.DeadlineExceeded):
		return "the download took too long · try again"
	}
	return firstLine(err.Error())
}

// psHelperFailToast is the toast of a failed install that a press was waiting
// for. lead, when given, is what the press had already done ("Account-ID
// saved. "), so that news is not lost.
func psHelperFailToast(lead string, err error) string {
	if errors.Is(err, errPSHelpersNoInternet) {
		text := "Remote Play needs a one-time download, and " + psHelperNoInternetText() + " — press A again once it is back"
		return lead + text
	}
	reason := psHelpersFailText(err)
	if l := strings.TrimSuffix(strings.TrimSpace(lead), "."); l != "" {
		return l + ", but Remote Play could not install — " + reason
	}
	return "Could not install Remote Play — " + reason
}

// psPairNoHelpersText is a pairing's toast and Last-error line when the helper
// it needed did not come (errPSPairNoHelpers, errPSPairStillInstalling): no
// handshake ran, and the install's advice must not be cut. ok is false for any
// other error.
func psPairNoHelpersText(err error) (toast, code string, ok bool) {
	switch {
	case errors.Is(err, errPSPairStillInstalling):
		return "Not paired yet — Remote Play is still installing · when its row says it is installed, open Link Device for a new PIN and pair again",
			psPairCodeStillInstalling, true
	case errors.Is(err, errPSPairNoHelpers):
		return "Could not pair — " + err.Error(), psPairCodeNoHelpers, true
	}
	return "", "", false
}

// ── One install ──────────────────────────────────────────────────────────

// psHelperRelease is a release as GitHub's list returns it.
type psHelperRelease struct {
	TagName     string        `json:"tag_name"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt string        `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

// psHelperCandidate is a release with the helper package.
type psHelperCandidate struct {
	rel   psHelperRelease
	asset githubAsset
}

// psHelperCandidates lists the releases with the helper package in the order
// they are tried: never a draft, stable before beta, then newest first.
func psHelperCandidates(releases []psHelperRelease) []psHelperCandidate {
	type ranked struct {
		psHelperCandidate
		at    time.Time
		dated bool
		order int
	}
	var found []ranked
	for i, r := range releases {
		if r.Draft || strings.TrimSpace(r.TagName) == "" {
			continue
		}
		for _, a := range r.Assets {
			if !strings.EqualFold(strings.TrimSpace(a.Name), psHelperAsset) {
				continue
			}
			if strings.TrimSpace(a.BrowserDownloadURL) != "" && a.Size >= 0 && a.Size <= psHelperMaxAsset {
				at, dated := releasePublishedTime(&githubRelease{TagName: r.TagName, PublishedAt: r.PublishedAt})
				found = append(found, ranked{psHelperCandidate: psHelperCandidate{rel: r, asset: a}, at: at, dated: dated, order: i})
			}
			break
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.rel.Prerelease != b.rel.Prerelease {
			return !a.rel.Prerelease
		}
		if a.dated && b.dated && !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.dated != b.dated {
			return a.dated
		}
		return a.order < b.order
	})
	out := make([]psHelperCandidate, len(found))
	for i, f := range found {
		out[i] = f.psHelperCandidate
	}
	return out
}

// psHelperPick is the release the install tries first.
func psHelperPick(releases []psHelperRelease) (psHelperRelease, githubAsset, bool) {
	cands := psHelperCandidates(releases)
	if len(cands) == 0 {
		return psHelperRelease{}, githubAsset{}, false
	}
	return cands[0].rel, cands[0].asset, true
}

// psHelperOutOfSpace reports whether err is a full card.
func psHelperOutOfSpace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no space left on device") ||
		strings.Contains(text, "not enough space on the disk") ||
		strings.Contains(text, "disk quota exceeded")
}

// psHelperCardRefused reports whether err is a card that will not take a
// write: mounted read-only, or failing.
func psHelperCardRefused(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "read-only file system") ||
		strings.Contains(text, "permission denied") ||
		strings.Contains(text, "input/output error")
}

// psHelperDiskErr maps a full or refusing card to its sentinel; nil for any
// other error.
func psHelperDiskErr(err error) error {
	switch {
	case psHelperOutOfSpace(err):
		return psHelperNoSpace(0, 0)
	case psHelperCardRefused(err):
		return fmt.Errorf("%w (%v)", errPSHelpersSDWrite, err)
	}
	return nil
}

// psHelperNoSpace is errPSHelpersNoSpace with the numbers when they are known.
func psHelperNoSpace(need, free int64) error {
	if need <= 0 {
		return fmt.Errorf("%w · free some space, then try again", errPSHelpersNoSpace)
	}
	return fmt.Errorf("%w · needs %s, has %s · free some space", errPSHelpersNoSpace, formatBytes(uint64(need)), formatBytes(uint64(max64(free, 0))))
}

// psHelperNetErr wraps a failed request: no way to GitHub reads as
// errPSHelpersOffline, a connection lost mid-way as errPSHelpersCutOff and a
// refusal for too many requests as errPSHelpersRateLimited, so the panel can
// say what to do about it.
func psHelperNetErr(what string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if disk := psHelperDiskErr(err); disk != nil {
		return disk
	}
	text := err.Error()
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	switch {
	case strings.Contains(text, "HTTP 403") || strings.Contains(text, "HTTP 429"):
		return fmt.Errorf("%w (%s: %v)", errPSHelpersRateLimited, what, err)
	case strings.Contains(text, "certificate has expired or is not yet valid"):
		// No RTC battery: after a power cycle the clock can sit years back.
		return fmt.Errorf("%w (%s: %v)", errPSHelpersClock, what, err)
	case strings.Contains(text, "certificate is valid for"):
		// A Wi-Fi sign-in page answering for GitHub.
		return fmt.Errorf("%w (%s: %v)", errPSHelpersOffline, what, err)
	case strings.Contains(text, "certificate signed by unknown authority"),
		strings.Contains(text, "failed to load system roots"):
		return fmt.Errorf("%w (%s: %v)", errPSHelpersTrust, what, err)
	case errors.Is(err, io.ErrUnexpectedEOF),
		strings.Contains(text, "unexpected EOF"),
		strings.Contains(text, "stream error"),
		strings.Contains(text, "connection reset"),
		strings.Contains(text, "partial download does not match"):
		return fmt.Errorf("%w (%s: %v)", errPSHelpersCutOff, what, err)
	case errors.As(err, &dnsErr), errors.As(err, &opErr),
		errors.As(err, &netErr) && netErr.Timeout(),
		strings.Contains(text, "no such host"),
		strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "no route to host"),
		strings.Contains(text, "connection refused"),
		strings.Contains(text, "i/o timeout"),
		strings.Contains(text, "TLS handshake timeout"):
		return fmt.Errorf("%w (%s: %v)", errPSHelpersOffline, what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// psHelperFindReleases asks GitHub for the releases to install from, in the
// order to try them: page by page until one has the helper package.
func psHelperFindReleases(ctx context.Context, client *http.Client) ([]psHelperCandidate, error) {
	sep := "?"
	if strings.Contains(psHelperReleasesAPI, "?") {
		sep = "&"
	}
	for page := 1; page <= psHelperMaxPages; page++ {
		pageURL := psHelperReleasesAPI + sep + "per_page=" + strconv.Itoa(psHelperPerPage) + "&page=" + strconv.Itoa(page)
		data, err := fetchBounded(ctx, client, pageURL, patchMaxMetadata)
		if err != nil {
			return nil, psHelperNetErr("listing JukaHub releases", err)
		}
		var releases []psHelperRelease
		if err := json.Unmarshal(data, &releases); err != nil {
			return nil, fmt.Errorf("reading JukaHub's release list: %w", err)
		}
		if cands := psHelperCandidates(releases); len(cands) > 0 {
			return cands, nil
		}
		if len(releases) < psHelperPerPage {
			// The last page.
			break
		}
	}
	return nil, errPSHelpersNotPublished
}

// psHelperExpectedSHA is the SHA-256 the archive must have: the signed
// manifest's when the release carries one, else GitHub's digest ("" when
// GitHub published none; the archive's own SHA256SUMS is still checked then).
// A manifest that verifies but does not list the package is errPSHelpersUnlisted,
// which passes the release over for an older one.
func psHelperExpectedSHA(ctx context.Context, client *http.Client, rel psHelperRelease, asset githubAsset) (string, error) {
	digest := releaseAssetDigest(asset.Digest)
	signed, _, err := signedReleaseTarget(ctx, client, &githubRelease{TagName: rel.TagName, PublishedAt: rel.PublishedAt, Assets: rel.Assets}, asset.Name)
	switch {
	case err == nil:
		if digest != "" && !strings.EqualFold(digest, signed) {
			return "", fmt.Errorf("%w: GitHub's digest for %s disagrees with the signed release manifest", errPSHelpersUnverified, asset.Name)
		}
		return strings.ToLower(signed), nil
	case errors.Is(err, errNoReleaseManifest):
		if digest == "" {
			logWarn("psremote", "%s in %s has no published sha256; relying on HTTPS and its SHA256SUMS", asset.Name, rel.TagName)
		}
		return digest, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", err
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "does not list"):
		return "", fmt.Errorf("%w (%s: %v)", errPSHelpersUnlisted, rel.TagName, err)
	case strings.Contains(text, "issued in the future"), strings.Contains(text, "manifest expired"):
		// Checked against the handheld's clock, which may sit years back.
		return "", fmt.Errorf("%w (%v)", errPSHelpersClock, err)
	}
	wrapped := psHelperNetErr("fetching the signed release manifest", err)
	for _, pass := range []error{errPSHelpersOffline, errPSHelpersCutOff, errPSHelpersRateLimited, errPSHelpersClock, errPSHelpersTrust, errPSHelpersNoSpace, errPSHelpersSDWrite} {
		if errors.Is(wrapped, pass) {
			return "", wrapped
		}
	}
	return "", fmt.Errorf("%w: %v", errPSHelpersUnverified, err)
}

// psHelperPruneWork leaves only keep (and its .part) in the work folder: a
// download of another release must not be resumed into this one.
func psHelperPruneWork(work, keep string) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep || e.Name() == keep+".part" {
			continue
		}
		_ = os.RemoveAll(filepath.Join(work, e.Name()))
	}
}

// psHelperFileSize is a file's size, 0 when it is not there.
func psHelperFileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

// psHelperDownload fetches the archive to path, reporting progress, and
// checks it: expect (when known) and the size GitHub states. A download cut
// off after it made progress is resumed at once, up to psHelperDownloadTries
// starts in all.
func psHelperDownload(ctx context.Context, client *http.Client, rel psHelperRelease, asset githubAsset, path, expect string, progress func(psHelperView)) error {
	total := asset.Size
	matches := func(p string) bool {
		if total > 0 && psHelperFileSize(p) != total {
			return false
		}
		if expect == "" {
			return total > 0
		}
		got, err := SHA256File(p)
		return err == nil && strings.EqualFold(got, expect)
	}
	// A complete archive from an attempt that stopped after its download, or
	// a complete .part that a power cut kept from being renamed.
	if matches(path) {
		return nil
	}
	_ = os.Remove(path)
	if part := path + ".part"; total > 0 && psHelperFileSize(part) >= total {
		if matches(part) && os.Rename(part, path) == nil {
			return nil
		}
		_ = os.Remove(part)
	}

	progress(psHelperView{phase: psHelperDownloading, got: psHelperFileSize(path + ".part"), total: total, tag: rel.TagName})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				progress(psHelperView{phase: psHelperDownloading, got: psHelperFileSize(path + ".part"), total: total, tag: rel.TagName})
			}
		}
	}()
	var err error
	for try := 1; try <= psHelperDownloadTries; try++ {
		before := psHelperFileSize(path + ".part")
		err = downloadToFile(ctx, client, asset.BrowserDownloadURL, path, expect, psHelperMaxAsset)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "sha256 mismatch") {
			err = fmt.Errorf("%w: %v", errPSHelpersUnverified, err)
			break
		}
		if psHelperOutOfSpace(err) {
			// Those bytes only keep the card full.
			_ = os.Remove(path + ".part")
			err = psHelperNoSpace(0, 0)
			break
		}
		err = psHelperNetErr("downloading "+asset.Name, err)
		lost := errors.Is(err, errPSHelpersCutOff) || errors.Is(err, errPSHelpersOffline)
		if !lost || try == psHelperDownloadTries || psHelperFileSize(path+".part") <= before {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(2 * time.Second):
			continue
		}
		break
	}
	close(stop)
	wg.Wait()
	if err != nil {
		return err
	}
	if got := psHelperFileSize(path); total > 0 && got != total {
		_ = os.Remove(path)
		return fmt.Errorf("%w: %s is %d bytes, GitHub lists %d", errPSHelpersUnverified, asset.Name, got, total)
	}
	got := psHelperFileSize(path)
	progress(psHelperView{phase: psHelperDownloading, got: got, total: got, tag: rel.TagName})
	return nil
}

// psHelperUnpackedSize is what the archive's entries add up to.
func psHelperUnpackedSize(archive string) (int64, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		total = satAddBytes(total, declaredEntryBytes(f.UncompressedSize64))
	}
	return total, nil
}

// psHelperFiles are the files of an unpacked package.
type psHelperFiles struct {
	stream, regist, buildInfo, sums string
}

// psHelperFindFiles finds the package's files in its unpacked tree: by name,
// at most two folders deep, each at most once.
func psHelperFindFiles(root string) (psHelperFiles, error) {
	var found psHelperFiles
	seen := map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		switch d.Name() {
		case "chiaki-stream":
			found.stream = p
		case "chiaki-regist":
			found.regist = p
		case "BUILDINFO.txt":
			found.buildInfo = p
		case "SHA256SUMS":
			found.sums = p
		default:
			return nil
		}
		seen[d.Name()]++
		return nil
	})
	if err != nil {
		return psHelperFiles{}, err
	}
	for _, name := range []string{"chiaki-stream", "chiaki-regist", "BUILDINFO.txt", "SHA256SUMS"} {
		if seen[name] > 1 {
			return psHelperFiles{}, fmt.Errorf("%w: the package holds %s more than once", errPSHelpersUnverified, name)
		}
	}
	if found.stream == "" || found.regist == "" {
		return psHelperFiles{}, fmt.Errorf("%w: the package has no chiaki-stream or no chiaki-regist", errPSHelpersUnverified)
	}
	return found, nil
}

// psHelperReadSums reads SHA256SUMS ("<hex>  <path>" or "<hex> *<path>" per
// line) into hashes by file name.
func psHelperReadSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		name := filepath.Base(filepath.FromSlash(strings.TrimPrefix(fields[1], "*")))
		sums[name] = strings.ToLower(fields[0])
	}
	return sums, scanner.Err()
}

// psRegistHelperLibraryPath is the LD_LIBRARY_PATH chiaki-regist runs with
// (psHelperEnv): the Brick runtime, then JukaHub's own.
func psRegistHelperLibraryPath(engine psEngine) string {
	ld := strings.TrimSpace(os.Getenv("LD_LIBRARY_PATH"))
	if rt := psEngineRuntimeDir(engine); rt != "" {
		if ld != "" {
			return rt + ":" + ld
		}
		return rt
	}
	return ld
}

// psHelperProbeStaged is psHelperProbeFn: the stream preflight's check
// (psNativeHelperProblem) for each unpacked helper given, with the library
// path it will have once installed in binDir.
func psHelperProbeStaged(stream, regist, binDir string) string {
	engine := psEngine{Stream: stream, Regist: regist, Root: binDir}
	if stream != "" {
		if problem := psNativeHelperProblem(stream, psStreamHelperLibraryPath(engine)); problem != "" {
			return "chiaki-stream: " + problem
		}
	}
	if regist != "" {
		if problem := psNativeHelperProblem(regist, psRegistHelperLibraryPath(engine)); problem != "" {
			return "chiaki-regist: " + problem
		}
	}
	return ""
}

// psHelperPlace copies src to dir/name through a temporary file, checks the
// copy against want (src's SHA-256) and renames it into place.
func psHelperPlace(src, dir, name, want string, mode os.FileMode) error {
	tmp := filepath.Join(dir, "."+name+".new")
	_ = os.Remove(tmp)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if want != "" {
		if got, err := SHA256File(tmp); err != nil || !strings.EqualFold(got, want) {
			_ = os.Remove(tmp)
			return fmt.Errorf("%w: %s was not written to the SD card intact", errPSHelpersUnverified, name)
		}
	}
	if runtime.GOOS != "windows" {
		// A FAT card ignores modes; its mount decides what may run.
		_ = os.Chmod(tmp, mode)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// psInstallHelpers installs the missing helpers and returns the release they
// came from ("" when nothing was missing).
func psInstallHelpers(ctx context.Context, progress func(psHelperView)) (string, error) {
	if progress == nil {
		progress = func(psHelperView) {}
	}
	if !psHelperSupportedFn() {
		return "", errors.New("Remote Play streams on the TrimUI handhelds only")
	}
	dir := psHelperTargetDir()
	if dir == "" {
		return "", errors.New("JukaHub does not know its own folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if disk := psHelperDiskErr(err); disk != nil {
			return "", disk
		}
		return "", fmt.Errorf("could not create %s: %w", dir, err)
	}
	// What is missing; a helper that is there is never replaced.
	psHelpersForgetPresence()
	stream, regist := psHelpersPresent()
	needStream := !stream && !regularFile(filepath.Join(dir, "chiaki-stream"))
	needRegist := !regist && !regularFile(filepath.Join(dir, "chiaki-regist"))
	if !needStream && !needRegist {
		return "", nil
	}
	client := psHelperClientFn()

	progress(psHelperView{phase: psHelperLooking})
	cands, err := psHelperFindReleases(ctx, client)
	if err != nil {
		return "", err
	}
	var rel psHelperRelease
	var asset githubAsset
	expect := ""
	for i, c := range cands {
		if i >= psHelperMaxCandidates {
			break
		}
		sum, err := psHelperExpectedSHA(ctx, client, c.rel, c.asset)
		if errors.Is(err, errPSHelpersUnlisted) {
			logWarn("psremote", "%v; trying an older release", err)
			continue
		}
		if err != nil {
			return c.rel.TagName, err
		}
		rel, asset, expect = c.rel, c.asset, sum
		break
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("%w: the signed manifests of the newest releases do not list %s", errPSHelpersUnverified, psHelperAsset)
	}

	// Room for the archive first; what it unpacks to is checked once known.
	if free, err := freeBytes(dir); err == nil && asset.Size > 0 && free < asset.Size+16<<20 {
		return rel.TagName, psHelperNoSpace(asset.Size+16<<20, free)
	}

	work := filepath.Join(dir, psHelperWorkDirName)
	if err := os.MkdirAll(work, 0o755); err != nil {
		if disk := psHelperDiskErr(err); disk != nil {
			return rel.TagName, disk
		}
		return rel.TagName, fmt.Errorf("could not create %s: %w", work, err)
	}
	archive := filepath.Join(work, sanitizeFileName(rel.TagName)+"-"+psHelperAsset)
	psHelperPruneWork(work, filepath.Base(archive))
	if err := psHelperDownload(ctx, client, rel, asset, archive, expect, progress); err != nil {
		if errors.Is(err, errPSHelpersUnverified) {
			// Those bytes must not be resumed into the next try.
			_ = os.RemoveAll(work)
		}
		return rel.TagName, err
	}

	progress(psHelperView{phase: psHelperChecking, tag: rel.TagName})
	unpackedSize, err := psHelperUnpackedSize(archive)
	if err != nil {
		_ = os.RemoveAll(work)
		return rel.TagName, fmt.Errorf("%w: reading %s: %v", errPSHelpersUnverified, asset.Name, err)
	}
	// The unpacked copy and the copies placed beside the binary.
	if free, err := freeBytes(dir); err == nil && free < 2*unpackedSize+8<<20 {
		return rel.TagName, psHelperNoSpace(2*unpackedSize+8<<20, free)
	}
	unpacked := filepath.Join(work, "unpacked")
	_ = os.RemoveAll(unpacked)
	defer os.RemoveAll(unpacked)
	if err := extractZipSafe(ctx, archive, unpacked, psHelperMaxEntries, psHelperMaxUnpacked); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Stopped, or out of time: the archive is not at fault and stays
			// for the next try.
			return rel.TagName, ctxErr
		}
		if disk := psHelperDiskErr(err); disk != nil {
			// The archive is good: it stays for the next try.
			return rel.TagName, disk
		}
		_ = os.RemoveAll(work)
		return rel.TagName, fmt.Errorf("%w: unpacking %s: %v", errPSHelpersUnverified, asset.Name, err)
	}
	files, err := psHelperFindFiles(unpacked)
	if err != nil {
		_ = os.RemoveAll(work)
		return rel.TagName, err
	}
	hashes := map[string]string{}
	for name, path := range map[string]string{"chiaki-stream": files.stream, "chiaki-regist": files.regist, "BUILDINFO.txt": files.buildInfo} {
		if path == "" {
			continue
		}
		sum, err := SHA256File(path)
		if err != nil {
			return rel.TagName, err
		}
		hashes[name] = sum
	}
	if files.sums != "" {
		sums, err := psHelperReadSums(files.sums)
		if err != nil {
			return rel.TagName, fmt.Errorf("%w: reading SHA256SUMS: %v", errPSHelpersUnverified, err)
		}
		for name, sum := range hashes {
			listed, ok := sums[name]
			if !ok && name == "BUILDINFO.txt" {
				continue
			}
			if !ok || !strings.EqualFold(listed, sum) {
				_ = os.RemoveAll(work)
				return rel.TagName, fmt.Errorf("%w: %s does not match the package's SHA256SUMS", errPSHelpersUnverified, name)
			}
		}
	}

	// Each helper about to be installed is started once, on its own: one
	// this firmware refuses does not keep the other from being installed,
	// and a working helper of the user's own is never judged at all.
	var refusals []string
	installStream, installRegist := needStream, needRegist
	if needStream {
		psEnsureExecutable(files.stream)
		if problem := strings.TrimSpace(psHelperProbeFn(files.stream, "", dir)); problem != "" {
			refusals = append(refusals, problem)
			installStream = false
		}
	}
	if needRegist {
		psEnsureExecutable(files.regist)
		if problem := strings.TrimSpace(psHelperProbeFn("", files.regist, dir)); problem != "" {
			refusals = append(refusals, problem)
			installRegist = false
		}
	}

	// From here the helpers are copied into bin/, and a stop is no longer
	// taken (psHelpersStop reads placing under the same lock). One that came
	// during the checks wins now, before anything is written; the archive
	// stays for the next try.
	progress(psHelperView{phase: psHelperChecking, tag: rel.TagName, placing: true})
	if err := ctx.Err(); err != nil {
		return rel.TagName, err
	}
	place := func(src, name, sum string, mode os.FileMode) error {
		err := psHelperPlace(src, dir, name, sum, mode)
		if disk := psHelperDiskErr(err); disk != nil {
			return disk
		}
		if err != nil {
			return fmt.Errorf("installing %s: %w", name, err)
		}
		return nil
	}
	// chiaki-stream last: psRemoteEngine calls Remote Play installed by it.
	if installRegist {
		if err := place(files.regist, "chiaki-regist", hashes["chiaki-regist"], 0o755); err != nil {
			return rel.TagName, err
		}
	}
	if installStream {
		if files.buildInfo != "" && installRegist {
			// It describes the pair; a lone helper keeps the note it came with.
			if err := place(files.buildInfo, "BUILDINFO.txt", hashes["BUILDINFO.txt"], 0o644); err != nil {
				logInfo("psremote", "BUILDINFO.txt not installed: %v", err)
			}
		}
		if err := place(files.stream, "chiaki-stream", hashes["chiaki-stream"], 0o755); err != nil {
			return rel.TagName, err
		}
	}
	// Trying the same archive again cannot change a refusal.
	_ = os.RemoveAll(work)
	if len(refusals) > 0 {
		problem := strings.Join(refusals, "; ")
		psHelperSaveRefusal(dir, rel.TagName, problem)
		return rel.TagName, fmt.Errorf("%w: %s", errPSHelpersWontRun, problem)
	}
	psHelperClearRefusal()
	return rel.TagName, nil
}

// ── Presses that wait for the install ────────────────────────────────────

// psHoldResult is what psHelpersHold did with a press.
type psHoldResult int

const (
	psHoldNotNeeded   psHoldResult = iota // nothing missing after all: go on
	psHoldBlocked                         // no install can come now (the error says why)
	psHoldQueued                          // waiting for the install
	psHoldQueuedAgain                     // the same press was already waiting
)

// psHelpersHold leaves a press waiting for the install: next runs on the main
// loop when it ends, and kind/host/label say whose press it is. A newer press
// takes the place of an older one, and replaced then says what the older one
// will no longer do. It starts the install when none runs; when none can come
// now (a refusal by this firmware, no internet, a failure just now) it says
// so through the error and leaves the toast to the caller.
func psHelpersHold(kind, host, label string, next func(error)) (res psHoldResult, v psHelperView, replaced string, err error) {
	if refusal := psHelpersRefusal(); refusal != nil {
		return psHoldBlocked, psHelperView{}, "", refusal
	}
	if !psHelpersRunning() && !IsOnline() {
		return psHoldBlocked, psHelperView{}, "", errPSHelpersNoInternet
	}
	if !psHelpersStart(psStartPress) {
		if refusal := psHelpersRefusal(); refusal != nil {
			return psHoldBlocked, psHelperView{}, "", refusal
		}
		return psHoldNotNeeded, psHelperView{}, "", nil
	}
	psHelperJob.mu.Lock()
	if !psHelperJob.running {
		// It ended in between.
		var last error
		if psHelperJob.run != nil {
			last = psHelperJob.run.err
		}
		psHelperJob.mu.Unlock()
		if last == nil {
			return psHoldNotNeeded, psHelperView{}, "", nil
		}
		return psHoldBlocked, psHelperView{}, "", last
	}
	again := psHelperJob.next != nil && psHelperJob.nextKind == kind && psHelperJob.nextHost == host
	if psHelperJob.next != nil && !again {
		switch {
		case psHelperJob.nextKind == "play" && psHelperJob.nextLabel != "":
			replaced = psHelperJob.nextLabel + " won't start by itself now — "
		case psHelperJob.nextKind == "pin":
			replaced = "The PIN keyboard won't open by itself now — "
		}
	}
	psHelperJob.next, psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.nextLabel = next, kind, host, label
	v = psHelperJob.view
	psHelperJob.mu.Unlock()
	if again {
		return psHoldQueuedAgain, v, "", nil
	}
	return psHoldQueued, v, replaced, nil
}

// psHelpersPressHeld reports whether a press waits for the install (the
// footer offers B to stop it).
func psHelpersPressHeld() bool {
	psHelperJob.mu.Lock()
	defer psHelperJob.mu.Unlock()
	return psHelperJob.next != nil
}

// psHelpersDropHeld forgets a waiting press without a word (a new visit of
// the panel).
func psHelpersDropHeld() {
	psHelperJob.mu.Lock()
	psHelperJob.next, psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.nextLabel = nil, "", "", ""
	psHelperJob.mu.Unlock()
}

// psHelpersCancelHeld lets go of a waiting press and says so: B on the console
// list, or the panel closing (which plays its own sound, so sound is false
// there). The install itself goes on. It reports whether there was one.
func psHelpersCancelHeld(sound bool) bool {
	psHelperJob.mu.Lock()
	kind, label, held := psHelperJob.nextKind, psHelperJob.nextLabel, psHelperJob.next != nil
	psHelperJob.next, psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.nextLabel = nil, "", "", ""
	psHelperJob.mu.Unlock()
	if !held {
		return false
	}
	if sound {
		PlayBackSound()
	}
	if kind == "play" && label != "" {
		ShowToast("Stopped — "+label+" won't start by itself; Remote Play keeps installing", ToastKindInfo)
	} else {
		ShowToast("Stopped — the PIN keyboard won't open by itself; Remote Play keeps installing", ToastKindInfo)
	}
	return true
}

// psHelperProgressNote is " (42%)" while the download runs, else "".
func psHelperProgressNote(v psHelperView) string {
	if v.phase == psHelperDownloading {
		return " (" + psHelperPercent(v) + ")"
	}
	return ""
}

// psHelperBlockedKind is the toast kind of a press no install can serve now.
func psHelperBlockedKind(err error) ToastKind {
	if errors.Is(err, errPSHelpersNoInternet) {
		return ToastKindWarning
	}
	return ToastKindError
}

// psHelpersHoldPlay keeps a Play press while chiaki-stream is missing: the
// install starts (or goes on), and the press is made again on the main loop
// once it is done, so the first stream needs nothing but A. It reports whether
// it took the press; false means the helper is there, or Remote Play cannot
// run here at all (psRemoteStream says so).
func psHelpersHoldPlay(c psConsole) bool {
	if !psHelperSupportedFn() {
		return false
	}
	if stream, _ := psHelpersPresent(); stream {
		return false
	}
	host, label := c.Host, c.label()
	res, v, replaced, err := psHelpersHold("play", host, label, func(err error) { psHelpersReplayPlay(host, label, err) })
	switch res {
	case psHoldNotNeeded:
		return false
	case psHoldBlocked:
		ShowToast(psHelperFailToast("", err), psHelperBlockedKind(err))
		return true
	}
	PlayActivateSound()
	lead := "Installing Remote Play first"
	if res == psHoldQueuedAgain {
		lead = "Still installing Remote Play"
	}
	ShowToast(replaced+lead+psHelperProgressNote(v)+" — "+label+" streams by itself when it is done", ToastKindInfo)
	return true
}

// psHelpersReplayPlay is the held Play press, on the main loop once the
// install has ended: the stream starts while the panel is free on its console
// list, and a toast says what happened otherwise.
func psHelpersReplayPlay(host, label string, err error) {
	if err != nil {
		ShowToast(psHelperFailToast("", err), ToastKindError)
		return
	}
	if !chiakiPanel.open {
		ShowToast("PS Remote Play is installed — open it and press A on "+label+" to stream", ToastKindSuccess)
		return
	}
	if !psHelpersPanelFree() {
		if !psHelpersQuietNow() {
			ShowToast("PS Remote Play is installed — press A on "+label+" to stream", ToastKindSuccess)
		}
		return
	}
	console, ok := psPanelConsole(host)
	if !ok || !console.paired() {
		ShowToast("PS Remote Play is installed", ToastKindSuccess)
		return
	}
	psPanelPlayOrWake(console)
}

// psHelpersHoldPIN keeps a PIN prompt until chiaki-regist is installed: the
// PIN Link Device shows lasts five minutes, and a pairing that waited for a
// download could hand the console an expired one. mustHave is a known PS5,
// which cannot pair without it; an address no scan has identified (it may be
// a PS4) waits only for an install that can come now, and is asked for its PIN
// at once otherwise. prefix leads the toast ("Account-ID saved. "). It reports
// whether it took the prompt; false means ask for the PIN now.
func psHelpersHoldPIN(host, prefix string, mustHave bool) bool {
	if !psHelperSupportedFn() {
		return false
	}
	if _, regist := psHelpersPresent(); regist {
		return false
	}
	res, v, replaced, err := psHelpersHold("pin", host, "", func(err error) { psHelpersResumePIN(host, mustHave, err) })
	switch res {
	case psHoldNotNeeded:
		return false
	case psHoldBlocked:
		if !mustHave {
			// It may be a PS4, which pairs without the helper.
			return false
		}
		ShowToast(psHelperFailToast(prefix, err), psHelperBlockedKind(err))
		return true
	}
	PlayActivateSound()
	lead := "Installing Remote Play first"
	if res == psHoldQueuedAgain {
		lead = "Still installing Remote Play"
	}
	ShowToast(replaced+prefix+lead+psHelperProgressNote(v)+" — the PIN keyboard opens when it is done; open Link Device on the PS5 then", ToastKindInfo)
	return true
}

// psHelpersResumePIN is the held PIN prompt, on the main loop once the install
// has ended: it opens while the panel is free on its console list for that
// console. An address that may be a PS4 is asked even when the install failed.
func psHelpersResumePIN(host string, mustHave bool, err error) {
	if err != nil && mustHave {
		ShowToast(psHelperFailToast("", err), ToastKindError)
		return
	}
	if !chiakiPanel.open {
		if err == nil {
			ShowToast("PS Remote Play is installed — open it and press A on the PS5 to pair it", ToastKindSuccess)
		}
		return
	}
	if !psHelpersPanelFree() || chiakiPanel.pairHost != host {
		switch {
		case psHelpersQuietNow():
		case err != nil:
			ShowToast(psHelperFailToast("", err)+" · a PS4 pairs without it", ToastKindWarning)
		default:
			ShowToast("PS Remote Play is installed — press A on the PS5 to pair it", ToastKindSuccess)
		}
		return
	}
	if err != nil {
		// Straight to the keyboard: the prompt's own path would hold it for
		// another install, and that one would fail the same way.
		ShowToast("Remote Play did not install, so only a PS4 pairs at this address. "+psPanelLinkDeviceHint(4), ToastKindInfo)
		psPanelOpenKeyboard(psEditPIN)
		return
	}
	psPanelPromptPIN("PS Remote Play is installed. ")
}

// psHelpersForPS5Pairing makes sure chiaki-regist is there before a known
// PS5's handshake: its PIN is asked for only once the helper is in
// (psHelpersHoldPIN), so this waits only for one that went missing since, at
// most psHelperPairWait. Runs on the pairing goroutine; the error wraps
// errPSPairNoHelpers, or is errPSPairStillInstalling.
func psHelpersForPS5Pairing() error {
	if !psHelperSupportedFn() {
		// ps5Register says what is missing.
		return nil
	}
	if _, regist := psHelpersPresent(); regist {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), psHelperPairWait)
	defer cancel()
	err := psHelpersEnsure(ctx)
	if err == nil {
		return nil
	}
	// The wait may have run out just as the install ended.
	psHelpersForgetPresence()
	if _, regist := psHelpersPresent(); regist {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) && psHelpersRunning() {
		return errPSPairStillInstalling
	}
	return fmt.Errorf("%w: %s", errPSPairNoHelpers, psHelpersFailText(err))
}

// ── The panel ────────────────────────────────────────────────────────────

// psHelpersAutoStart is the panel's opening: missing helpers start installing
// at once while the handheld is online, so they are usually in place by the
// time a console is chosen. A finished install's row belongs to the panel
// session it finished in, and so does a held press: one left from a visit that
// another screen ended (they close the panel without closeChiakiPanel) never
// acts in this one. A refusal remembered from an earlier start is shown, not
// downloaded again.
func psHelpersAutoStart() {
	psHelpersDropHeld()
	psHelpersForgetPresence()
	if !psHelpersNeeded() {
		psHelperJob.mu.Lock()
		if !psHelperJob.running && psHelperJob.view.phase == psHelperInstalled {
			psHelperJob.view = psHelperView{}
		}
		psHelperJob.mu.Unlock()
		return
	}
	psHelpersAdoptSavedRefusal()
	if !IsOnline() {
		return
	}
	psHelpersStart(psStartAuto)
}

// psHelpersAdoptSavedRefusal makes a refusal remembered from an earlier start
// (bin/.remote-play-refused) the last install's outcome when nothing has run
// in this one: the panel then says why, and neither its opening nor the
// background download (psremote_helpers_bg.go) fetches those helpers again.
// It reports whether a refusal was saved.
func psHelpersAdoptSavedRefusal() bool {
	tag, problem, ok := psHelperLoadRefusal()
	if !ok {
		return false
	}
	psHelperJob.mu.Lock()
	if psHelperJob.run == nil && !psHelperJob.running {
		err := fmt.Errorf("%w: %s", errPSHelpersWontRun, problem)
		run := &psHelperRun{done: make(chan struct{}), err: err}
		close(run.done)
		psHelperJob.run = run
		psHelperJob.view = psHelperView{phase: psHelperFailed, tag: tag, reason: psHelpersFailText(err), refused: psHelperRefusedWhich(problem)}
	}
	psHelperJob.mu.Unlock()
	return true
}

// psPanelInstallHelpers is A on the install row: Install, or Retry.
func psPanelInstallHelpers() {
	if !psHelperSupportedFn() {
		ShowToast("Remote Play streams on the TrimUI handhelds", ToastKindInfo)
		return
	}
	psHelpersForgetPresence()
	if !psHelpersNeeded() {
		ShowToast("PS Remote Play is installed", ToastKindInfo)
		return
	}
	if !psHelpersRunning() && !IsOnline() {
		ShowToast(psHelperFailToast("", errPSHelpersNoInternet), ToastKindWarning)
		return
	}
	PlayActivateSound()
	psHelpersStart(psStartRetry)
	// A Retry also wakes the background download (psremote_helpers_bg.go):
	// a network failure of this try is then tried again by itself.
	psBgKick()
}

// psHelperPercent is the install's progress for a status column.
func psHelperPercent(v psHelperView) string {
	if v.phase != psHelperDownloading {
		return "…"
	}
	if v.total <= 0 {
		return formatBytes(uint64(max64(v.got, 0)))
	}
	pct := int(v.got * 100 / v.total)
	if pct > 99 {
		// 100% is the install's end, not the download's.
		pct = 99
	}
	if pct < 0 {
		pct = 0
	}
	return itoa(pct) + "%"
}

// psHelperRows is the console list's line about the helpers: the install's
// progress, why it failed, or the offer to install while one is missing, and
// once an install has finished in this panel session a line that says so
// (the rows below it do not move under the cursor).
func psHelperRows() []chiakiRow {
	v := psHelpersView()
	if !psHelpersNeeded() {
		if v.phase == psHelperInstalled && psHelperSupportedFn() {
			return []chiakiRow{{label: "Remote Play is installed", detail: "The streaming and PS5 pairing helpers are ready", status: "Done"}}
		}
		return nil
	}
	// The download and the install are named apart: "Downloading" while bytes
	// come in (with how many), "Installing" while they are checked and put in
	// place.
	switch v.phase {
	case psHelperLooking:
		return []chiakiRow{{label: "Downloading Remote Play…", detail: "Finding it on GitHub · a one-time download of a few MB", status: "…"}}
	case psHelperDownloading:
		detail := "Streaming and pairing helpers"
		if v.total > 0 {
			detail = formatBytes(uint64(max64(v.got, 0))) + " of " + formatBytes(uint64(v.total))
		}
		detail += " · keeps going if you close this"
		return []chiakiRow{{label: "Downloading Remote Play…", detail: detail, status: psHelperPercent(v)}}
	case psHelperChecking:
		detail := "Checking the download and that it runs on this handheld"
		if v.placing {
			detail = "Copying the helpers into place"
		}
		return []chiakiRow{{label: "Installing Remote Play…", detail: detail, status: "…"}}
	case psHelperFailed:
		label, detail := "Remote Play is not installed", v.reason
		if v.refused != "" {
			label = "Remote Play won't run on this firmware"
			if v.refused == "regist" {
				label = "PS5 pairing won't run on this firmware"
			}
			detail = strings.TrimPrefix(v.reason, psHelperRefusedPrefix)
		}
		return []chiakiRow{{label: label, detail: detail, status: "Retry", action: psActionInstallHelpers}}
	}
	detail := "One-time download of the streaming and PS5 pairing helpers"
	if !IsOnline() {
		detail = "No internet right now · a one-time download of a few MB"
		if PortalSignInURL() != "" {
			detail = "Sign in to the Wi-Fi first · a one-time download of a few MB"
		}
	}
	return []chiakiRow{{label: "Install Remote Play", detail: detail, status: "Install", action: psActionInstallHelpers}}
}

// psHelperSubtitle is the panel's subtitle while a helper is missing ("" when
// both are there).
func psHelperSubtitle() string {
	if !psHelpersNeeded() {
		return ""
	}
	v := psHelpersView()
	switch v.phase {
	case psHelperLooking:
		return "Downloading Remote Play · one moment"
	case psHelperDownloading:
		return "Downloading Remote Play · " + psHelperPercent(v)
	case psHelperChecking:
		return "Installing Remote Play · one moment"
	case psHelperFailed:
		if v.refused == "regist" {
			return "PS5 pairing won't run on this firmware · see the row below"
		}
		return "Remote Play is not installed yet · see the row below"
	}
	return "Remote Play needs a one-time download"
}

// psHelperConsoleNote is a console row's line while the helpers decide what A
// does on it: a press waiting for the install, or a paired console whose
// stream helper is missing. ok is false when the row keeps its own line.
func psHelperConsoleNote(c psConsole) (string, bool) {
	if !psHelperSupportedFn() {
		return "", false
	}
	psHelperJob.mu.Lock()
	kind, host, running := psHelperJob.nextKind, psHelperJob.nextHost, psHelperJob.running
	psHelperJob.mu.Unlock()
	if host == c.Host {
		switch kind {
		case "play":
			return hintSentence("Streams by itself when Remote Play is installed · %s stops", "B", "Stop"), true
		case "pin":
			return hintSentence("PIN keyboard opens when Remote Play is installed · %s stops", "B", "Stop"), true
		}
	}
	if !c.paired() {
		return "", false
	}
	if stream, _ := psHelpersPresent(); stream {
		return "", false
	}
	switch {
	case running:
		return hintSentence("Streams once Remote Play is installed · %s waits for it", "A", "Play"), true
	case psHelpersRefusal() != nil:
		return "Remote Play won't run on this firmware · see its row", true
	case !IsOnline():
		return "Needs a one-time download first · no internet right now", true
	}
	return hintSentence("Needs Remote Play installed first · %s installs it", "A", "Play"), true
}

// psHelperPairingWait is the pairing row's line while a pairing waits for
// chiaki-regist ("" when it does not).
func psHelperPairingWait() string {
	psHelperJob.mu.Lock()
	running, waiting, v := psHelperJob.running, psHelperJob.waiters > 0, psHelperJob.view
	psHelperJob.mu.Unlock()
	if !running || !waiting {
		return ""
	}
	if v.phase == psHelperChecking {
		return "Installing Remote Play, then pairing"
	}
	return "Downloading Remote Play" + psHelperProgressNote(v) + ", then pairing"
}
