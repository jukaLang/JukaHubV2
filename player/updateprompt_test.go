package main

import (
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

// Update now must take the user to Patch's install confirmation, not download
// anything by itself.
func TestUpdatePromptUpdateNowOpensPatchInstall(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")

	prevModal, prevPrompt := patchModal, updatePrompt
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	t.Cleanup(func() {
		patchModal, updatePrompt = prevModal, prevPrompt
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
	})
	patchModal = nil
	currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"
	updatePrompt.open, updatePrompt.focus, updatePrompt.seen = false, 0, false
	updatePrompt.nowBtn, updatePrompt.laterBtn, updatePrompt.badge = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}

	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Patch"}}}
	openUpdatePrompt()
	if !updatePrompt.open {
		t.Fatal("openUpdatePrompt did not open the modal")
	}
	activateUpdatePrompt(config)

	if currentSceneIndex != 1 && pendingSceneIndex != 1 {
		t.Fatalf("Update now targeted current=%d pending=%d, want the Patch scene", currentSceneIndex, pendingSceneIndex)
	}
	if patchModal == nil || patchModal.ID != "confirm-install-app" {
		t.Fatalf("patch modal = %+v, want the install confirmation", patchModal)
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

func TestUpdateBadgeClickOpensPrompt(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	prevPrompt := updatePrompt
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	t.Cleanup(func() {
		updatePrompt = prevPrompt
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
	})
	updatePrompt.open, updatePrompt.seen = false, false
	updatePrompt.badge = sdl.Rect{X: 1000, Y: 10, W: 100, H: 30}
	sceneTitleBackRect, statusClockRect = sdl.Rect{}, sdl.Rect{}

	if !handleChromeBarClick(1010, 20, &Config{}) {
		t.Fatal("click on the update badge was not consumed")
	}
	if !updatePrompt.open {
		t.Fatal("click on the update badge did not open the prompt")
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
