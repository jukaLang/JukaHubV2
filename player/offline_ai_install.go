package main

// The small language model is optional. It is fetched as a verified release
// package on demand, never needed for commands, speech recognition, or Groq.

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
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

// offlineAIOutdated reports whether the installed package has the older,
// larger model or lacks a warm server. It still answers while an update is
// available from Settings or after an offline answer on Wi-Fi.
func offlineAIOutdated() bool {
	if !offlineAIReady() || offlineAINewestInstalled {
		return false
	}
	server, model := offlineAIServerFiles()
	if model == "" {
		_, model = offlineAIFiles()
	}
	return server == "" || filepath.Base(model) == offlineAILegacyModelName
}

// offlineAIUpgradeTried keeps the automatic update to one try per run.
var offlineAIUpgradeTried bool

// offlineAINewestInstalled: the newest release is the installed build, even
// though it is still an older kind of package. Settings then stops offering
// an update that would download the same files again. Main thread.
var offlineAINewestInstalled bool

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
			tbxPost(func() { offlineAINewestInstalled = true })
			return
		}
		tbxPost(func() {
			if !offlineAIOutdated() || relPackRunning(offlineAIPackKey) || installBarActive() {
				return
			}
			logInfo("voice", "updating offline AI to %s", latest.Rel.TagName)
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
				return "A faster, smaller version is available · ~100 MB download"
			}
			if offlineAIReady() {
				return "Small on-device model answers when Groq is unavailable · no key needed"
			}
			return "Optional ~100 MB download · needs free space on the card · slower than Groq"
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
				offlineAIServerResetFails()
				// The release just installed is the newest: if it is still
				// the older kind, Settings must not offer it again.
				offlineAINewestInstalled = false
				offlineAINewestInstalled = offlineAIOutdated()
				installBarDoneRun(gen)
				ShowToast("Offline AI ready — try asking Juka a question", ToastKindInfo)
			}
		})
	}()
}
