package main

// The app package includes offline speech on Smart Pro. This installer repairs
// a missing copy without asking people to reinstall the whole app. It uses the
// same verified release-package path as the other optional device tools.

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

const (
	sttPackKey   = "offline-speech"
	sttPackAsset = "JukaHub-OfflineSpeech-linux-arm64.zip"
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

func sttInstallProgress(p relPackProgress) {
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

func sttInstallOffline(*Config) {
	switch {
	case !sttSupported():
		ShowToast("Offline speech is available on TrimUI Smart Pro", ToastKindInfo)
		return
	case sttLocalReady():
		ShowToast("Offline speech is ready", ToastKindInfo)
		return
	case relPackRunning(sttPackKey):
		ShowToast("Offline speech is already installing", ToastKindInfo)
		return
	case !IsOnline():
		ShowToast("Connect to Wi-Fi to install offline speech once", ToastKindWarning)
		return
	case installBarActive() || !tryBeginPatchOp():
		ShowToast("Another install is running — try again when it finishes", ToastKindInfo)
		return
	}
	dir := sttDownloadedDir()
	if dir == "" {
		endPatchOp()
		ShowToast("JukaHub's data folder is not available", ToastKindError)
		return
	}
	gen, runCtx := installBarBeginRun("Offline speech")
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(runCtx, patchDownloadTimeout)
		defer cancel()
		_, err := relPackInstall(ctx, nil, sttPackSpec(), dir, sttInstallProgress)
		tbxPost(func() {
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(gen)
				ShowToast("Stopped installing offline speech", ToastKindInfo)
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				ShowToast("Could not install offline speech: "+msg, ToastKindError)
				logWarn("voice", "offline speech install failed: %v", err)
			default:
				installBarDoneRun(gen)
				ShowToast("Offline speech is ready", ToastKindInfo)
			}
		})
	}()
}
