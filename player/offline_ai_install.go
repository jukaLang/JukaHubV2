package main

// The small language model is optional. It is fetched as a verified release
// package on demand, never needed for commands, speech recognition, or Groq.

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

const (
	offlineAIPackKey        = "offline-ai"
	offlineAIPackAsset      = "JukaHub-OfflineAI-linux-arm64.zip"
	offlineAIInstallTimeout = 50 * time.Minute // SD-card downloads can need resumed transfers
)

func offlineAIPackSpec() relPackSpec {
	return relPackSpec{
		Key:         offlineAIPackKey,
		Asset:       offlineAIPackAsset,
		Title:       "offline AI",
		MaxAsset:    290 << 20,
		MaxUnpacked: 300 << 20,
		Root:        offlineAIDirName,
		Probe:       offlineAIPackProbe,
		Supported:   offlineAISupported,
	}
}

func offlineAIPackProbe(dir string) string {
	bin, _, ok := offlineAIFilesIn(dir)
	if !ok {
		return "the model or its runner is missing"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "offline AI did not start in time"
		}
		return "offline AI cannot start on this device: " + err.Error()
	}
	return ""
}

func offlineAISettingsRows() []setRow {
	return []setRow{{
		ID: "offlineAI", Label: "Offline answers", Kind: setKindAction,
		Visible: func(*Config) bool { return offlineAISupported() },
		Value: func(*Config) string {
			switch {
			case offlineAIReady():
				return "Ready"
			case relPackRunning(offlineAIPackKey):
				return "Installing"
			default:
				return "Install"
			}
		},
		Detail: func(*Config) string {
			if offlineAIReady() {
				return "Small on-device model answers when Groq is unavailable · no key needed"
			}
			return "Optional ~240 MB download · needs free space on the card · slower than Groq"
		},
		Open: offlineAIInstall,
	}}
}

func offlineAIInstall(*Config) {
	switch {
	case !offlineAISupported():
		ShowToast("Offline AI is available on TrimUI Smart Pro", ToastKindInfo)
		return
	case offlineAIReady():
		ShowToast("Offline AI is ready", ToastKindInfo)
		return
	case relPackRunning(offlineAIPackKey):
		ShowToast("Offline AI is already installing", ToastKindInfo)
		return
	case !IsOnline():
		ShowToast("Connect to Wi-Fi to download offline AI once", ToastKindWarning)
		return
	case installBarActive() || !tryBeginPatchOp():
		ShowToast("Another install is running — try again when it finishes", ToastKindInfo)
		return
	}
	dir := offlineAIDownloadedDir()
	if dir == "" {
		endPatchOp()
		ShowToast("JukaHub's data folder is not available", ToastKindError)
		return
	}
	gen, runCtx := installBarBeginRun("Offline AI")
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(runCtx, offlineAIInstallTimeout)
		defer cancel()
		_, err := relPackInstall(ctx, nil, offlineAIPackSpec(), dir, sttInstallProgress)
		tbxPost(func() {
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(gen)
				ShowToast("Stopped installing offline AI", ToastKindInfo)
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				ShowToast("Could not install offline AI: "+msg, ToastKindError)
				logWarn("voice", "offline AI install failed: %v", err)
			default:
				installBarDoneRun(gen)
				ShowToast("Offline AI ready — try asking Juka a question", ToastKindInfo)
			}
		})
	}()
}
