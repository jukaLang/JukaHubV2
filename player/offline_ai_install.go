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
	runners := []string{bin}
	if server := offlineAIServerIn(dir); server != "" {
		runners = append(runners, server)
	}
	for _, runner := range runners {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, runner, "--help")
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		err := cmd.Run()
		timedOut := ctx.Err() != nil
		cancel()
		if err != nil {
			if timedOut {
				return "offline AI did not start in time"
			}
			return "offline AI cannot start on this device: " + err.Error()
		}
	}
	return ""
}

// offlineAIOutdated reports whether the installed package predates the
// server that keeps the model loaded: it still answers, more slowly, and
// updates itself (offlineAIMaybeUpgrade) or from Settings.
func offlineAIOutdated() bool {
	if !offlineAIReady() {
		return false
	}
	server, _ := offlineAIServerFiles()
	return server == ""
}

// offlineAIUpgradeTried keeps the automatic update to one try per run.
var offlineAIUpgradeTried bool

// offlineAIMaybeUpgrade updates an outdated package once per run, after an
// offline answer, when Wi-Fi is up, nothing else is installing and the newest
// release carries a different build (one that still lacks the server is not
// downloaded again on every start). Main thread.
func offlineAIMaybeUpgrade() {
	if offlineAIUpgradeTried || !offlineAIOutdated() || !IsOnline() || relPackRunning(offlineAIPackKey) || installBarActive() {
		return
	}
	offlineAIUpgradeTried = true
	dir := offlineAIDownloadedDir()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		latest, err := relPackLatest(ctx, relPackClientFn(), offlineAIPackSpec())
		if err != nil {
			logDebug("voice", "offline AI update check: %v", err)
			return
		}
		if installed, ok := relPackRead(dir); ok && !relPackNewer(installed, latest) {
			logDebug("voice", "offline AI: the newest release has the installed build")
			return
		}
		tbxPost(func() {
			if !offlineAIOutdated() || relPackRunning(offlineAIPackKey) || installBarActive() {
				return
			}
			logInfo("voice", "offline AI package has no server; updating it to %s", latest.Rel.TagName)
			ShowToast("Updating offline AI for faster answers", ToastKindInfo)
			offlineAIStartInstall()
		})
	}()
}

func offlineAISettingsRows() []setRow {
	return []setRow{{
		ID: "offlineAI", Label: "Offline answers", Kind: setKindAction,
		Visible: func(*Config) bool { return offlineAISupported() },
		Value: func(*Config) string {
			switch {
			case relPackRunning(offlineAIPackKey):
				return "Installing"
			case offlineAIOutdated():
				return "Update"
			case offlineAIReady():
				return "Ready"
			default:
				return "Install"
			}
		},
		Detail: func(*Config) string {
			if offlineAIOutdated() {
				return "A faster version is out · ~240 MB download · answers keep working until then"
			}
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
	case offlineAIReady() && !offlineAIOutdated():
		ShowToast("Offline AI is ready", ToastKindInfo)
		return
	case relPackRunning(offlineAIPackKey):
		ShowToast("Offline AI is already installing", ToastKindInfo)
		return
	case !IsOnline():
		ShowToast("Connect to Wi-Fi to download offline AI once", ToastKindWarning)
		return
	case installBarActive():
		ShowToast("Another install is running — try again when it finishes", ToastKindInfo)
		return
	}
	offlineAIStartInstall()
}

// offlineAIStartInstall downloads the package (again, for an update) behind
// the install card. Main thread.
func offlineAIStartInstall() {
	if !tryBeginPatchOp() {
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
		_, err := relPackInstall(ctx, nil, offlineAIPackSpec(), dir, sttInstallProgress(gen))
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
				// A server started from the old files lets go; the next
				// question loads the new ones.
				offlineAIServerStop("package updated")
				installBarDoneRun(gen)
				ShowToast("Offline AI ready — try asking Juka a question", ToastKindInfo)
			}
		})
	}()
}
