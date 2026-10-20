package main

import (
	"context"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func withAvailableUpdate(t *testing.T, tag string) {
	t.Helper()
	patchStateMu.Lock()
	prevTag, prevAt := patchState.AvailableUpdateTag, patchState.AvailableUpdateAt
	patchState.AvailableUpdateTag, patchState.AvailableUpdateAt = tag, ""
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		patchState.AvailableUpdateTag, patchState.AvailableUpdateAt = prevTag, prevAt
		patchStateMu.Unlock()
	})
}

// Update now installs directly: one press, no Patch scene and no second
// confirmation. The header download chip runs the same action.
func TestUpdatePromptUpdateNowInstallsDirectly(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	clearToastAfterTest(t)

	prevModal, prevPrompt := patchModal, updatePrompt
	prevRunner := updateInstallRunner
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	t.Cleanup(func() {
		patchModal, updatePrompt = prevModal, prevPrompt
		updateInstallRunner = prevRunner
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
		endPatchOp()
		clearPatchBusy()
	})
	patchModal = nil
	currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"
	updatePrompt.open, updatePrompt.focus, updatePrompt.seen = false, 0, false
	updatePrompt.nowBtn, updatePrompt.laterBtn, updatePrompt.badge = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	started := make(chan struct{}, 1)
	updateInstallRunner = func(ctx context.Context, cfg *Config) error {
		started <- struct{}{}
		return nil
	}

	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Patch"}}}
	openUpdatePrompt()
	if !updatePrompt.open {
		t.Fatal("openUpdatePrompt did not open the modal")
	}
	activateUpdatePrompt(config)

	waitForInstall(t, started)
	waitPatchIdle(t)
	if updatePrompt.open {
		t.Fatal("the prompt stayed open after Update now")
	}
	if patchModal != nil {
		t.Fatalf("Update now opened another confirmation: %+v", patchModal)
	}
	if currentSceneIndex != 0 || pendingSceneIndex == 1 {
		t.Fatalf("Update now changed scene: current=%d pending=%d", currentSceneIndex, pendingSceneIndex)
	}
}

func TestUpdatePromptNotNowLeavesPatchAlone(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	prevModal, prevPrompt := patchModal, updatePrompt
	prevScene := currentSceneIndex
	t.Cleanup(func() {
		patchModal, updatePrompt = prevModal, prevPrompt
		currentSceneIndex = prevScene
	})
	patchModal = nil
	updatePrompt.open, updatePrompt.focus = true, 1

	activateUpdatePrompt(&Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Patch"}}})
	if updatePrompt.open {
		t.Fatal("Not now left the prompt open")
	}
	if patchModal != nil {
		t.Fatalf("Not now opened a modal: %+v", patchModal)
	}
}

// The status-bar badge is the home chip on every other screen: one click or
// press installs, exactly as the D-pad path does. Opening a modal here was the
// inconsistency users saw when the offer looked different per screen.
func TestUpdateBadgeClickStartsInstall(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	clearToastAfterTest(t)
	prevPrompt := updatePrompt
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	prevRunner := updateInstallRunner
	t.Cleanup(func() {
		updatePrompt = prevPrompt
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
		updateInstallRunner = prevRunner
		clearPatchBusy()
	})
	updatePrompt.open, updatePrompt.seen = false, false
	updatePrompt.badge = sdl.Rect{X: 1000, Y: 10, W: 100, H: 30}
	sceneTitleBackRect, statusClockRect = sdl.Rect{}, sdl.Rect{}

	done := make(chan struct{}, 1)
	updateInstallRunner = func(ctx context.Context, cfg *Config) error {
		done <- struct{}{}
		return nil
	}

	if !handleChromeBarClick(1010, 20, &Config{}) {
		t.Fatal("click on the update badge was not consumed")
	}
	waitForInstall(t, done)
	waitPatchIdle(t)
	if updatePrompt.open {
		t.Fatal("clicking the badge opened a prompt instead of installing")
	}
}

func TestToolsRowsPatchAndCalculator(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	config := &Config{Scenes: []SceneConfig{
		{Name: "Home"}, {Name: "Misc"}, {Name: "Patch"}, {Name: "Calculator"},
	}}
	rows := toolsRows(config)
	var patchRow, calcRow *toolsRow
	for i := range rows {
		switch rows[i].action {
		case toolsActionPatch:
			patchRow = &rows[i]
		case toolsActionCalculator:
			calcRow = &rows[i]
		}
	}
	if patchRow == nil {
		t.Fatalf("no Patch row in %+v", rows)
	}
	if !strings.HasPrefix(patchRow.value, "Update ") {
		t.Fatalf("Patch row value = %q, want the available update", patchRow.value)
	}
	if calcRow == nil {
		t.Fatalf("no Calculator row in %+v", rows)
	}
	if calcRow.value != "Open" || calcRow.hint != "A: open" {
		t.Fatalf("Calculator row = %+v", calcRow)
	}
}

// Eleven rows (FTP, SSH, companion, party, Patch, Community, Power tools,
// Calculator, IP, utilities, Close) plus the SFTP credentials must still fit a
// 720p panel, which is what dense mode is for.
func TestToolsPanelDenseFitsWithPatchAndCalculator(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()
	if err := ftpSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	defer ftpSrv.Stop()

	config := &Config{Scenes: []SceneConfig{
		{Name: "Home"}, {Name: "Misc"}, {Name: "Patch"}, {Name: "Calculator"},
	}}
	_, panel, rects := renderToolsProbe(t, config, 0)
	if len(rects) != 11 {
		t.Fatalf("row rects = %d, want 11", len(rects))
	}
	layout := toolsPanelMetrics(config, len(rects))
	if !layout.dense {
		t.Fatal("expected dense rows for nine items on a 720p screen")
	}
	content := layout.headerH + int32(len(rects))*layout.rowH + layout.infoH + layout.footerH
	if content > probeH-40 || content > panel.H {
		t.Fatalf("dense layout needs %dpx, panel is %+v with screen %d", content, *panel, probeH)
	}
	if len(layout.info) == 0 {
		t.Fatal("dense layout dropped the SFTP credentials")
	}
}
