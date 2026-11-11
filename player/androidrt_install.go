package main

// androidrt_install.go — getting AndroidRT onto the device and keeping it
// current.
//
// AndroidRT (player/androidrt) is cross-built by JukaHub's CI, which puts it
// in every TrimUI archive (Apps/JukaHub/androidrt, beside the binary) and
// attaches the same build to each release as JukaHub-AndroidRT-linux-arm64.zip
// (root folder androidrt/, with a SHA256SUMS listing every file). The bundled
// copy updates with JukaHub itself. When there is none — JukaHub installed
// from an older archive, or its binary copied on its own — the APK Store
// installs the release package into <data>/androidrt through relpack.go:
// checked against the release's signed manifest (or GitHub's digest) and the
// package's own SHA256SUMS, and its loader must prove it starts here
// (`loader --version`, run on the unpacked copy before anything is replaced).
// findAndroidRuntime (androidpak.go) uses that copy when no other is there.
//
// Nothing is downloaded behind the user's back: an install starts from a press
// (the Runtime tab's Install or Update, or the one-press offer Play and Add
// make when no runtime is found), and the daily Emulator updates pass keeps
// the downloaded copy current under that pass's own setting. The bundled copy
// and a copy the user built are never touched.

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	androidRTKey   = "androidrt"
	androidRTAsset = "JukaHub-AndroidRT-linux-arm64.zip"
	androidRTTitle = "the Android runtime"
	// androidRTMaxAsset and androidRTMaxUnpacked bound the download and what
	// it unpacks to: the package is a loader, a surface probe and two text
	// files, a few megabytes.
	androidRTMaxAsset    = 64 << 20
	androidRTMaxUnpacked = 160 << 20
	// androidRTProbeTimeout bounds `loader --version` on the unpacked copy.
	androidRTProbeTimeout = 15 * time.Second
	// androidRTProbeOutputMax is how much of the probe's output is kept.
	androidRTProbeOutputMax = 4 << 10
	// androidRTCheckEvery is how long the Runtime tab trusts its last answer
	// to "is there a newer build?".
	androidRTCheckEvery = 30 * time.Minute
	// androidRTCheckTimeout bounds that question.
	androidRTCheckTimeout = 30 * time.Second
)

// androidRTSupported reports whether this machine can run AndroidRT at all:
// it is an arm64 Linux program (the handheld), so a PC reads APKs but does not
// install the runtime.
func androidRTSupported() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" }

// androidRTSpec is the release package.
func androidRTSpec() relPackSpec {
	return relPackSpec{
		Key:         androidRTKey,
		Asset:       androidRTAsset,
		Title:       androidRTTitle,
		MaxAsset:    androidRTMaxAsset,
		MaxUnpacked: androidRTMaxUnpacked,
		Root:        "androidrt",
		Probe:       androidRTProbe,
		Supported:   androidRTSupported,
	}
}

// androidRTProbeOutput keeps the first few KiB a probe prints; the rest is
// counted as written and dropped, so a runaway binary cannot fill memory.
type androidRTProbeOutput struct{ b []byte }

func (w *androidRTProbeOutput) Write(p []byte) (int, error) {
	if room := androidRTProbeOutputMax - len(w.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.b = append(w.b, p[:room]...)
	}
	return len(p), nil
}

// androidRTProbe says why the unpacked package in dir would not run here (""
// when it would). First what the file tells (androidLoaderProblem: the execute
// bit, the CPU, the glibc versions against the firmware's), then the loader
// itself: `loader --version` touches no game, file or GPU and must print its
// "AndroidRT loader …" line and exit 0.
func androidRTProbe(dir string) string {
	loader := filepath.Join(dir, "loader")
	if !regularFile(loader) {
		return "the package has no loader"
	}
	if problem := androidLoaderProblem(loader); problem != "" {
		return problem
	}
	ctx, cancel := context.WithTimeout(context.Background(), androidRTProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, loader, "--version")
	cmd.Dir = dir
	out := &androidRTProbeOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	answer := firstLine(strings.TrimSpace(string(out.b)))
	switch {
	case ctx.Err() != nil:
		return "its loader did not answer (it hung on `loader --version`)"
	case err != nil && answer != "":
		return "its loader does not start here (" + answer + ")"
	case err != nil:
		return "its loader does not start here (" + err.Error() + ")"
	case !strings.HasPrefix(answer, "AndroidRT loader"):
		return "its loader answered something unexpected to `loader --version`"
	}
	logInfo("androidrt", "probe: %s", answer)
	return ""
}

// androidRuntimeMissingNow reports whether no usable AndroidRT exists right
// now — a fresh look at the folders (cheap; goroutines may call it).
func androidRuntimeMissingNow() bool {
	_, err := findAndroidRuntime(exec.LookPath)
	return errors.Is(err, errAndroidRuntimeMissing)
}

// ── Installing from a press ──────────────────────────────────────────────

// androidRTUI is the state the APK Store reads: the install a press started
// (its progress line) and the Runtime tab's last update check. Goroutines
// write it under mu; the store reads it on the render thread.
var androidRTUI struct {
	mu       sync.Mutex
	running  bool
	line     string
	gen      uint64 // the install bar run of the running install
	checking bool
	checked  time.Time
	newer    bool
	newerTag string
}

// androidRTInstallLine is the running install's progress line ("" when none).
func androidRTInstallLine() (string, bool) {
	androidRTUI.mu.Lock()
	defer androidRTUI.mu.Unlock()
	return androidRTUI.line, androidRTUI.running
}

// androidRTUpdateKnown is the last update check's answer: whether a newer
// build than the downloaded copy exists, and its tag.
func androidRTUpdateKnown() (bool, string) {
	androidRTUI.mu.Lock()
	defer androidRTUI.mu.Unlock()
	return androidRTUI.newer, androidRTUI.newerTag
}

// androidRTSetLine records the install's progress line and has the store
// redraw its rows when the words changed.
func androidRTSetLine(line string, running bool) {
	androidRTUI.mu.Lock()
	changed := androidRTUI.line != line || androidRTUI.running != running
	androidRTUI.line, androidRTUI.running = line, running
	androidRTUI.mu.Unlock()
	if changed {
		tbxPost(apkStoreRebuildText)
	}
}

// androidRTInstallProgress drives the install bar and the Runtime tab's line
// from relpack's progress reports (install goroutine).
func androidRTInstallProgress(p relPackProgress) {
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
	androidRTSetLine(p.line(androidRTTitle), p.Phase != relPackDone)
}

// androidRTStopInstall stops the install a press started (Stop on the
// AndroidRT sheet; the install chip's X does the same). Only that run: the bar
// may have moved on to another one meanwhile.
func androidRTStopInstall() {
	androidRTUI.mu.Lock()
	gen, running := androidRTUI.gen, androidRTUI.running
	androidRTUI.mu.Unlock()
	if running {
		installBarAbortRun(gen)
	}
}

// androidRTStartInstall installs (or updates) the downloaded AndroidRT off the
// event thread: the install bar shows it on every screen (Stop on the
// AndroidRT sheet ends it), the Runtime tab shows its progress line, and a
// toast says how it ended.
// done, when set, runs on the render thread with the outcome (nil = installed)
// — Play and Add continue through it. It runs only from a press.
func androidRTStartInstall(done func(err error)) {
	switch {
	case !androidRTSupported():
		PlayNavSound()
		ShowToast("AndroidRT runs on the handheld (arm64 Linux) — there is nothing to install on this machine", ToastKindInfo)
		return
	case relPackRunning(androidRTKey):
		ShowToast("Already installing the Android runtime", ToastKindInfo)
		return
	case !IsOnline():
		PlayErrorSound()
		ShowToast("Installing the Android runtime needs internet — connect to Wi-Fi, then try again", ToastKindWarning)
		return
	case installBarActive() || !tryBeginPatchOp():
		// A new install-bar run would cancel the one on the bar now.
		ShowToast("Another install is running — try again when it finishes", ToastKindInfo)
		return
	}
	dir := androidRuntimeDownloadDirFn()
	if dir == "" {
		endPatchOp()
		PlayErrorSound()
		ShowToast("Could not install the Android runtime: JukaHub's data folder is not available", ToastKindError)
		return
	}
	gen, runCtx := installBarBeginRun("AndroidRT")
	androidRTUI.mu.Lock()
	androidRTUI.gen = gen
	androidRTUI.mu.Unlock()
	androidRTSetLine(relPackProgress{Phase: relPackLooking}.line(androidRTTitle), true)
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(runCtx, patchDownloadTimeout)
		defer cancel()
		info, err := relPackInstall(ctx, nil, androidRTSpec(), dir, androidRTInstallProgress)
		if err == nil {
			// Every AndroidPak profile that said "not installed" can start now.
			invalidateGameProfileCache()
		}
		tbxPost(func() {
			androidRTSetLine("", false)
			androidRTForgetCheck()
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(gen)
				ShowToast("Stopped installing the Android runtime", ToastKindInfo)
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				PlayErrorSound()
				ShowToast("Could not install the Android runtime: "+msg, ToastKindError)
				logWarn("androidrt", "install failed: %v", err)
			default:
				installBarDoneRun(gen)
				PlaySuccessSound()
				ShowToast("The Android runtime is installed ("+info.Tag+")", ToastKindSuccess)
			}
			apkStoreRuntimeChanged()
			if done != nil {
				done(err)
			}
		})
	}()
}

// ── Is there a newer build? ──────────────────────────────────────────────

// androidRTForgetCheck drops the last update answer (after an install).
func androidRTForgetCheck() {
	androidRTUI.mu.Lock()
	androidRTUI.checked = time.Time{}
	androidRTUI.newer, androidRTUI.newerTag = false, ""
	androidRTUI.mu.Unlock()
}

// androidRTCheckUpdate asks GitHub whether a newer build than the downloaded
// copy exists — only that copy: the bundled one updates with JukaHub, and a
// copy the user built is theirs. At most once per androidRTCheckEvery unless
// asked from a press (asked), which also says the answer in a toast; the
// Runtime tab reads it through androidRTUpdateKnown. Render thread; the
// question runs in the background.
func androidRTCheckUpdate(asked bool) {
	if !androidRTSupported() || relPackRunning(androidRTKey) {
		return
	}
	if !IsOnline() {
		if asked {
			ShowToast("Checking for a newer Android runtime needs internet", ToastKindWarning)
		}
		return
	}
	androidRTUI.mu.Lock()
	if androidRTUI.checking || (!asked && !androidRTUI.checked.IsZero() && time.Since(androidRTUI.checked) < androidRTCheckEvery) {
		androidRTUI.mu.Unlock()
		return
	}
	androidRTUI.checking = true
	androidRTUI.mu.Unlock()
	if asked {
		ShowToast("Checking for a newer Android runtime…", ToastKindInfo)
	}
	dir := androidRuntimeDownloadDirFn()
	go func() {
		newer, tag := false, ""
		var err error
		if installed, ok := relPackRead(dir); ok {
			ctx, cancel := context.WithTimeout(context.Background(), androidRTCheckTimeout)
			var c relPackCandidate
			c, err = relPackLatest(ctx, relPackClientFn(), androidRTSpec())
			cancel()
			if err == nil {
				newer, tag = relPackNewer(installed, c), c.Rel.TagName
			} else {
				logDebug("androidrt", "update check: %v", err)
			}
		}
		androidRTUI.mu.Lock()
		androidRTUI.checking = false
		androidRTUI.checked = time.Now()
		androidRTUI.newer, androidRTUI.newerTag = newer, tag
		androidRTUI.mu.Unlock()
		tbxPost(func() {
			apkStoreRebuildText()
			if !asked {
				return
			}
			switch {
			case err != nil:
				ShowToast("Could not check for a newer Android runtime: "+relPackFriendly(err), ToastKindWarning)
			case newer:
				ShowToast("The Android runtime "+tag+" is out — A on AndroidRT updates it", ToastKindInfo)
			default:
				ShowToast("The Android runtime is up to date", ToastKindInfo)
			}
		})
	}()
}

// ── The daily Emulator updates pass ──────────────────────────────────────

// The downloaded copy joins the automatic pass (emuautoupdate_registry.go):
// installed means relpack's provenance file is in <data>/androidrt, and a
// running Android game holds the pass off until it has exited.
func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       androidRTKey,
		Title:     androidRTTitle,
		Supported: androidRTSupported,
		Installed: func() bool {
			_, ok := relPackRead(androidRuntimeDownloadDirFn())
			return ok
		},
		InUse: androidPakAnyRunning,
		Check: func(ctx context.Context, client *http.Client) (bool, string, error) {
			installed, ok := relPackRead(androidRuntimeDownloadDirFn())
			if !ok {
				return false, "", nil
			}
			if client == nil {
				client = relPackClientFn()
			}
			c, err := relPackLatest(ctx, client, androidRTSpec())
			if err != nil {
				return false, "", err
			}
			return relPackNewer(installed, c), c.Rel.TagName, nil
		},
		Install: func(ctx context.Context, client *http.Client, progress func(line string)) error {
			_, err := relPackInstall(ctx, client, androidRTSpec(), androidRuntimeDownloadDirFn(), func(p relPackProgress) {
				if progress != nil {
					progress(p.line(androidRTTitle))
				}
			})
			if err == nil {
				invalidateGameProfileCache()
				androidRTForgetCheck()
			}
			return err
		},
	})
}
