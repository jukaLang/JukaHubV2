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
	if calcRow.value != "Open" || calcRow.hint != "A Open" {
		t.Fatalf("Calculator row = %+v", calcRow)
	}
}

// Nineteen tiles in three sections (the ten FTP & IP tiles, party, Updates,
// power tools, toolbox, security, disc helpers, Calculator, Community, Classic
// Tools) plus the SFTP credentials line must render on a 720p panel: the tile
// grid scrolls instead of shrinking rows, and the credentials line survives
// both.
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
	if len(rects) != 19 {
		t.Fatalf("tile rects = %d, want 19 with Patch and Calculator", len(rects))
	}
	layout := toolsPanelMetrics(config, toolsRows(config))
	if panel.H > probeH-40 {
		t.Fatalf("panel is %dpx tall with screen %d", panel.H, probeH)
	}
	if len(layout.info) == 0 {
		t.Fatal("dense layout dropped the SFTP credentials")
	}
	// The cursor starts at the top, so the first tiles are the ones on screen.
	if rects[0].W == 0 {
		t.Fatal("the first tile should be visible at scroll 0")
	}
}

// TestUpdatePromptHighlights covers the "what's new" preview: the modal shows
// cleaned release-note lines, caps how many, and stays compact when nothing is
// cached (first run offline, or an older release with no stored notes).
func TestUpdatePromptHighlights(t *testing.T) {
	patchStateMu.Lock()
	prevTag, prevText := patchState.ReleaseNotesTag, patchState.ReleaseNotesText
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		patchState.ReleaseNotesTag, patchState.ReleaseNotesText = prevTag, prevText
		patchStateMu.Unlock()
	})
	setNotes := func(text string) {
		patchStateMu.Lock()
		patchState.ReleaseNotesTag, patchState.ReleaseNotesText = "v9.9.9", text
		patchStateMu.Unlock()
	}

	setNotes("")
	if got := updatePromptHighlights(3); got != nil {
		t.Fatalf("empty notes = %#v, want nil", got)
	}

	setNotes("## Fixes\n- **Patch** no longer drops the swap script\n- `Chat` keyboard wraps\n![shot](x.png)\n### Thanks\n- @someone\n- @another\n")
	got := updatePromptHighlights(3)
	// Bullets beat the "Fixes" heading, the "Thanks" handles are skipped, and
	// the image line never reaches the preview.
	want := []string{"Patch no longer drops the swap script", "Chat keyboard wraps"}
	if len(got) != len(want) {
		t.Fatalf("highlights = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("highlight %d = %q, want %q", i, got[i], want[i])
		}
	}

	if zero := updatePromptHighlights(0); zero != nil {
		t.Errorf("max 0 = %#v, want nil", zero)
	}
}
