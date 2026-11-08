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
		// Update now raises the install card and settles it; without the
		// reset the finished card's hold outlives this test and shows up as
		// a background row in whichever test runs next.
		resetInstallBar()
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
		// The chip starts the same install the prompt does, card and all.
		resetInstallBar()
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

// The Tools hub's Updates tile calls out an available release, and the
// Calculator tile is there whenever the config has the screen. A screen the
// config lacks gets no tile at all rather than one that leads nowhere.
func TestToolsHubUpdatesAndCalculatorTiles(t *testing.T) {
	withAvailableUpdate(t, "v9.9.9")
	config := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: "Patch"}, {Name: "Calculator"}}}
	tiles := toolsHubBuildTiles(config, toolsHubNet{ip: "192.168.1.20", online: true}, toolsHubSlowValues{}, "")

	updates := toolsHubIndexOf(tiles, "updates")
	if updates < 0 {
		t.Fatalf("no Updates tile in %+v", tiles)
	}
	if !strings.HasPrefix(tiles[updates].value, "Update ") || tiles[updates].tone != toolsHubToneWarn {
		t.Fatalf("Updates tile = %+v, want the available update called out", tiles[updates])
	}
	calc := toolsHubIndexOf(tiles, "calculator")
	if calc < 0 {
		t.Fatalf("no Calculator tile in %+v", tiles)
	}
	if tiles[calc].blocked != "" {
		t.Fatalf("Calculator tile = %+v, want it usable without a network", tiles[calc])
	}

	bare := toolsHubBuildTiles(&Config{}, toolsHubNet{}, toolsHubSlowValues{}, "")
	for _, id := range []string{"updates", "calculator", "terminal", "netspeed"} {
		if toolsHubIndexOf(bare, id) >= 0 {
			t.Errorf("a config without the screen still got the %q tile", id)
		}
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
