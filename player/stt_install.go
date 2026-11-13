package main

// Offline speech for the Smart Pro is not in the app archive (it is about
// 120 MB, more than the rest of JukaHub-Trimui.zip). It is its own release
// asset, JukaHub-OfflineSpeech-linux-arm64.zip, installed into <data>/
// juka-whisper through the verified release-package path (relpack.go):
//
//   - by itself once per run, after the startup tools (ffmpeg) are in and
//     before Piper's offline voice (tts_install.go waits for it), and
//   - from Settings → Voice & AI → Install offline speech, or the voice
//     orb's and voice page's "install offline speech" fix.
//
// A copy bundled by an older package (juka-whisper beside the binary) is still
// used: app updates never delete files, so those devices download nothing.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	sttPackKey   = "offline-speech"
	sttPackAsset = "JukaHub-OfflineSpeech-linux-arm64.zip"
	// sttAutoSpace is the free space the startup download wants before it
	// starts by itself: relPackInstall's own need (three times the ~120 MB
	// package plus its spare), rounded up. Below it the download waits for
	// Settings, where the failure card says how much is missing.
	sttAutoSpace = 3*(130<<20) + relPackSpare
	// sttAutoPatience is how long one run's startup download waits for the
	// card and the other startup downloads to settle before giving up.
	sttAutoPatience = time.Hour
	// sttUnsupportedMark, in the data folder, records that the recognizer
	// cannot start on this firmware, so startup stops downloading it again.
	sttUnsupportedMark = ".juka-whisper-unsupported"
)

// sttAutoTried is the one startup download per run (a reconnect after an
// offline start clears it). sttAutoHard is set when an automatic try failed
// for a reason a reconnect does not change, so waking from sleep does not show
// the same failure card again; Settings still tries. sttAutoWaiting is true
// from the moment the startup download is due until its card is up: Piper's
// voice waits on it, so the two never race for the card. sttAutoUntil
// (UnixNano) is when this run's waiting ends, however often it restarts.
var (
	sttAutoTried   atomic.Bool
	sttAutoHard    atomic.Bool
	sttAutoWaiting atomic.Bool
	sttAutoUntil   atomic.Int64
)

func sttPackSpec() relPackSpec {
	return relPackSpec{
		Key:         sttPackKey,
		Asset:       sttPackAsset,
		Title:       "offline speech",
		MaxAsset:    160 << 20,
		MaxUnpacked: 220 << 20,
		Root:        sttDirName,
		Probe:       sttPackProbe,
		Supported:   sttSupported,
	}
}

// The package checksum covers the model and binary. A short launch proves the
// binary can start on this firmware before the downloaded tree replaces one.
func sttPackProbe(dir string) string {
	bin, _, ok := sttFilesIn(dir)
	if !ok {
		return "the recognizer or its model is missing"
	}
	fastBin, _, fastOK := sttFastFilesIn(dir)
	if !fastOK {
		return "fast English recognition is missing"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "the recognizer did not start in time"
		}
		return "the recognizer cannot start on this device: " + err.Error()
	}
	cmd = exec.CommandContext(ctx, fastBin, "--help")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return "fast English recognition cannot start on this device: " + err.Error()
	}
	return ""
}

// sttInstallProgress drives the card for run gen only: once another install
// has taken the card, this download's late progress must not land on it.
func sttInstallProgress(gen uint64) func(relPackProgress) {
	return func(p relPackProgress) {
		if installBarGeneration() == gen {
			sttShowProgress(p)
		}
	}
}

func sttShowProgress(p relPackProgress) {
	switch p.Phase {
	case relPackLooking:
		installBarPhase(installPhaseResolving)
	case relPackDownloading:
		installBarPhase(installPhaseDownload)
		installBarProgress(p.Got, p.Total)
	case relPackUnpacking:
		installBarResetProgress()
		installBarPhase(installPhaseExtract)
	case relPackChecking:
		installBarPhase(installPhaseVerify)
	case relPackInstalling:
		installBarPhase(installPhaseExtract)
	}
}

// sttInstallOffline is the Settings row and the voice pages' fix: install now,
// with a toast for every reason it cannot.
func sttInstallOffline(*Config) {
	sttStartInstall(false)
}

// sttUnsupportedPath is the "won't run here" marker, "" without a data folder.
func sttUnsupportedPath() string {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return ""
	}
	return filepath.Join(data, sttUnsupportedMark)
}

// sttAutoInstallOffline is the startup download, called by the tools check
// once it has nothing of its own left to fetch (ffmpeg goes first). Any
// goroutine. It waits for another install card to finish and toasts only when
// it succeeds. Nothing is fetched when recognition is set to Groq only (read
// on the main thread: the settings map is not safe to read from here).
func sttAutoInstallOffline() {
	if !sttSupported() || sttAutoHard.Load() || !ttsAutoOnline() {
		return
	}
	if mark := sttUnsupportedPath(); mark != "" {
		if _, err := os.Stat(mark); err == nil {
			return
		}
	}
	if sttLocalReady() && !sttServeUpdateDue.Load() {
		go sttServeUpdateCheck()
		return
	}
	if !sttAutoTried.CompareAndSwap(false, true) {
		return
	}
	if dir := sttDownloadedDir(); dir != "" {
		if err := pkgCheckFreeSpace(filepath.Dir(dir), sttAutoSpace); err != nil {
			logInfo("voice", "offline speech: not downloading by itself: %v", err)
			return
		}
	}
	sttAutoUntil.Store(time.Now().Add(sttAutoPatience).UnixNano())
	sttAutoWaiting.Store(true)
	go sttAutoWait()
}

// sttServeUpdateChecked: this run has looked for a package that streams.
// sttServeUpdateDue: an installed package cannot stream (its vosk-cli predates
// --serve, stt_stream.go) and a newer release is out, so the startup download
// replaces it once — the installed one keeps working until the new one is in.
var (
	sttServeUpdateChecked atomic.Bool
	sttServeUpdateDue     atomic.Bool
)

// sttServeUpdateCheck looks, once a run, whether the installed package needs
// the streaming update and a different build is published; then the startup
// download runs as for a first install. Off the main thread.
func sttServeUpdateCheck() {
	if !sttServeUpdateChecked.CompareAndSwap(false, true) {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("voice", "offline speech update check stopped: %v", r)
		}
	}()
	if bin, _ := sttServeFiles(); bin != "" {
		sttServeProbe.state.Store(sttServeYes)
		return
	}
	sttServeProbe.state.Store(sttServeNo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	latest, err := relPackLatest(ctx, relPackClientFn(), sttPackSpec())
	if err != nil {
		// A reconnect looks again (sttAutoRetry).
		sttServeUpdateChecked.Store(false)
		logInfo("voice", "offline speech: could not look for the streaming update: %v", err)
		return
	}
	if installed, ok := relPackRead(sttDownloadedDir()); ok && !relPackNewer(installed, latest) {
		logInfo("voice", "offline speech: no newer package than %s yet; recognizing after each clip", installed.Tag)
		return
	}
	logInfo("voice", "offline speech: %s streams; downloading it once", latest.Rel.TagName)
	sttServeUpdateDue.Store(true)
	sttAutoInstallOffline()
}

// sttAutoStop ends this run's startup download without trying it; retry lets
// a reconnect try again.
func sttAutoStop(retry bool) {
	if retry {
		sttAutoTried.Store(false)
	}
	sttAutoWaiting.Store(false)
}

// sttAutoWait waits until no install card and no startup tool download has
// been busy for ten seconds (two looks five seconds apart), then starts the
// download on the main thread. It gives up for this run at sttAutoUntil.
func sttAutoWait() {
	quiet := 0
	for quiet < 2 && time.Now().UnixNano() < sttAutoUntil.Load() {
		time.Sleep(5 * time.Second)
		if installBarActive() || ttsToolsInstalling.Load() > 0 || relPackRunning(sttPackKey) {
			quiet = 0
		} else {
			quiet++
		}
	}
	if quiet < 2 {
		sttAutoStop(true)
		return
	}
	tbxPost(func() {
		switch {
		case sttAutoHard.Load():
			// A second waiter (a reconnect during the first) after a hard
			// failure: Settings tries again, startup does not.
			sttAutoStop(false)
			return
		case appConfig != nil && sttModeOf(appConfig) == sttModeGroq:
			// Groq only: nothing local is used, so nothing is fetched.
			sttAutoStop(false)
			return
		case sttStartInstall(true):
			sttAutoWaiting.Store(false)
			return
		}
		// Something got in first (a card, Wi-Fi gone): look again later.
		if ttsAutoOnline() {
			go sttAutoWait()
			return
		}
		sttAutoStop(true)
	})
}

// sttAutoRetry lets a reconnect after an offline start try again.
func sttAutoRetry() {
	if !sttAutoHard.Load() {
		sttAutoTried.Store(false)
	}
}

// sttSoftFailure reports whether an install error is the network's or the
// clock's (a reconnect or NTP may fix it) rather than the card's or the files'.
func sttSoftFailure(err error) bool {
	for _, soft := range []error{errRelPackOffline, errRelPackCutOff, errRelPackRateLimited, errRelPackClock, context.DeadlineExceeded} {
		if errors.Is(err, soft) {
			return true
		}
	}
	return false
}

// sttStartInstall starts the download. Main thread. It reports whether offline
// speech is installed, installing or now starting (false: Wi-Fi or another
// install kept it from starting). auto is the startup download: it stays quiet
// about everything but success and the failure card, and it does not take the
// Patch lock (as Piper's does not): holding it for a long download at boot
// would make the startup update check give up. It only writes its own folder,
// and any install that takes the card cancels it; it then waits and goes on.
func sttStartInstall(auto bool) bool {
	say := func(msg string, kind ToastKind) {
		if !auto {
			ShowToast(msg, kind)
		}
	}
	switch {
	case !sttSupported():
		say("Offline speech is available on TrimUI Smart Pro", ToastKindInfo)
		return true
	case sttLocalReady() && !sttServeUpdateDue.Load():
		say("Offline speech is ready", ToastKindInfo)
		return true
	case relPackRunning(sttPackKey):
		say("Offline speech is already installing", ToastKindInfo)
		return true
	case !IsOnline():
		say("Connect to Wi-Fi to install offline speech once", ToastKindWarning)
		return false
	case installBarActive() || auto && ttsToolsInstalling.Load() > 0 || !auto && !tryBeginPatchOp():
		say("Another install is running — try again when it finishes", ToastKindInfo)
		return false
	}
	release := func() {
		if !auto {
			endPatchOp()
		}
	}
	dir := sttDownloadedDir()
	if dir == "" {
		release()
		say("JukaHub's data folder is not available", ToastKindError)
		return true
	}
	mark := sttUnsupportedPath()
	if !auto {
		// The user asked: a firmware that refused the recognizer before is
		// checked again, and so is a failure that stopped the startup download.
		sttAutoHard.Store(false)
		if mark != "" {
			_ = os.Remove(mark)
		}
	}
	update := sttLocalReady() // the streaming update of a working package
	gen, runCtx := installBarBeginRun("Offline speech")
	go func() {
		defer release()
		ctx, cancel := context.WithTimeout(runCtx, patchDownloadTimeout)
		defer cancel()
		_, err := relPackInstall(ctx, nil, sttPackSpec(), dir, sttInstallProgress(gen))
		if err != nil && !errors.Is(err, context.Canceled) {
			logWarn("voice", "offline speech install (auto=%v) failed: %v", auto, err)
			if errors.Is(err, errRelPackWontRun) && mark != "" {
				_ = os.WriteFile(mark, []byte(err.Error()+"\n"), 0o644)
			}
			if auto && !sttSoftFailure(err) {
				sttAutoHard.Store(true)
			}
		}
		tbxPost(func() {
			switch {
			case errors.Is(err, context.Canceled):
				taken := installBarGeneration() != gen
				installBarAbortRun(gen)
				switch {
				case auto && taken:
					// Another install took the card (it cancels the run it
					// replaces): wait for it and go on, with a fresh hour.
					sttAutoUntil.Store(time.Now().Add(sttAutoPatience).UnixNano())
					sttAutoWaiting.Store(true)
					go sttAutoWait()
				case auto:
					// The user pressed X: the next try is a reconnect's or
					// the Settings row's, not the next tools check's.
				default:
					ShowToast("Stopped installing offline speech", ToastKindInfo)
				}
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				if !auto {
					ShowToast("Could not install offline speech: "+msg, ToastKindError)
				}
			default:
				installBarDoneRun(gen)
				sttServeUpdateDue.Store(false)
				// The next listen starts the new recognizer. Off the main thread:
			// the server lock may be held by a start running vosk-cli --help.
			go sttServeForget()
				if update {
					ShowToast("Offline speech updated — Juka hears you faster now", ToastKindInfo)
				} else if auto {
					ShowToast("Offline speech ready — talk to Juka without Wi-Fi", ToastKindInfo)
				} else {
					ShowToast("Offline speech is ready", ToastKindInfo)
				}
			}
		})
	}()
	return true
}
