package main

// The "Update" pill is drawn in every scene's status bar while a release is
// advertised. On Home its chip is a focus entry; on every other screen it used
// to be clickable but unreachable with the D-pad, so the offer was inconsistent
// depending on which screen the user happened to be on.

import (
	"context"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestChromeUpdatePillIsFocusable(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("UpdateBar", 2, 2))
	useTopChrome(t)
	homeLayoutActive = false
	withAvailableUpdate(t, "JukaHub-20261019120000")
	clearToastAfterTest(t)

	prevBadge := updatePrompt.badge
	t.Cleanup(func() { updatePrompt.badge = prevBadge })

	// The pill is drawn between the clock and the bell.
	updatePrompt.badge = sdl.Rect{X: 1130, Y: 10, W: 66, H: 26}

	scene := config.Scenes[0]
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if !focusEngine.SetByElementIndex(ChromeUpdateIndex) {
		t.Fatal("the Update pill is not in the focus graph")
	}
	if chromeTier(ChromeUpdateIndex) != 1 {
		t.Fatal("the Update pill is not a top-bar control")
	}

	// Walk the bar with the D-pad: clock -> Update -> bell, and back again.
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if !focusEngine.SetByElementIndex(ChromeClockIndex) {
		t.Fatal("cannot focus the clock")
	}
	if got := focusEngine.Navigate(ActionNavigateRight); got != ChromeUpdateIndex {
		t.Fatalf("Right from the clock focused %d, want the Update pill (%d)", got, ChromeUpdateIndex)
	}
	if got := focusEngine.Navigate(ActionNavigateRight); got != ChromeBellIndex {
		t.Fatalf("Right from the Update pill focused %d, want the bell (%d)", got, ChromeBellIndex)
	}
	if got := focusEngine.Navigate(ActionNavigateLeft); got != ChromeUpdateIndex {
		t.Fatalf("Left from the bell focused %d, want the Update pill (%d)", got, ChromeUpdateIndex)
	}

	// With no release advertised the pill is not drawn, so it must leave the
	// bar instead of becoming an invisible stop.
	updatePrompt.badge = sdl.Rect{}
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if focusEngine.SetByElementIndex(ChromeUpdateIndex) {
		t.Fatal("the Update pill stayed focusable with no release offered")
	}
}

func TestChromeUpdatePillActivatesTheInstall(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("UpdateBarPress", 2, 2))
	useTopChrome(t)
	homeLayoutActive = false
	withAvailableUpdate(t, "JukaHub-20261019120000")
	clearToastAfterTest(t)

	prevBadge := updatePrompt.badge
	updatePrompt.badge = sdl.Rect{X: 1130, Y: 10, W: 66, H: 26}
	prevRunner := updateInstallRunner
	t.Cleanup(func() {
		updatePrompt.badge = prevBadge
		updateInstallRunner = prevRunner
		clearPatchBusy()
		currentToast = ToastState{}
	})

	done := make(chan struct{}, 1)
	updateInstallRunner = func(ctx context.Context, cfg *Config) error {
		done <- struct{}{}
		return nil
	}

	scene := config.Scenes[0]
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	if !focusEngine.SetByElementIndex(ChromeUpdateIndex) {
		t.Fatal("cannot focus the Update pill")
	}
	if !activateChromeFocus(config) {
		t.Fatal("activating the Update pill was not handled")
	}
	waitForInstall(t, done)
	waitPatchIdle(t)
}
