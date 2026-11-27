package main

import "testing"

func TestIdleScreenHoldOffWhileAIWorks(t *testing.T) {
	if !idleScreenHoldOffFrom(false, false, false, false, false, false, true, false) {
		t.Fatal("an active AI turn did not hold the idle screen off")
	}
	if idleScreenHoldOffFrom(false, false, false, false, false, false, false, false) {
		t.Fatal("an idle device held the screen on without a reason")
	}
}

func TestIdleScreenHoldOffWhileLoading(t *testing.T) {
	if !idleScreenHoldOffFrom(false, false, false, false, false, false, false, true) {
		t.Fatal("a visible loading screen did not hold the idle screen off")
	}
}

func TestLoadingScreenVisible(t *testing.T) {
	cfg := &Config{}
	oldScene, oldLoading := currentSceneIndex, siteVideoUI.loading
	oldPhase := transitionPhase
	defer func() {
		currentSceneIndex, siteVideoUI.loading, transitionPhase = oldScene, oldLoading, oldPhase
	}()
	currentSceneIndex = -1
	transitionPhase = "none"

	if loadingScreenVisible(cfg) {
		t.Fatal("an idle device reported a loading screen")
	}

	// A site video search draws "Loading videos..." inside its own scene.
	siteVideoUI.loading = true
	if !loadingScreenVisible(cfg) {
		t.Fatal("a site video search did not hold the idle screen off")
	}
	siteVideoUI.loading = false

	// The modal loading card is drawn everywhere except the Media screen.
	cfg.Variables.LoadingSpinner = true
	if !loadingScreenVisible(cfg) {
		t.Fatal("the loading card did not hold the idle screen off")
	}
	cfg.Variables.LoadingSpinner = false

	// A scene transition covers the screen while it runs.
	transitionPhase = "out"
	if !loadingScreenVisible(cfg) {
		t.Fatal("a scene transition did not hold the idle screen off")
	}
	transitionPhase = "none"

	// A video prepare holds the screen off even while the decoder is idle.
	seq, _ := beginVideoPrepare("test")
	if !loadingScreenVisible(cfg) {
		t.Fatal("a video being prepared did not hold the idle screen off")
	}
	finishVideoPrepare(seq)
}

func TestAIHoldsScreenAwake(t *testing.T) {
	oldChat, oldTTS := chatAIBusy.Load(), ttsBusy.Load()
	oldPhase, oldAI := voiceOrb.phase, voiceOrb.ai
	defer func() {
		chatAIBusy.Store(oldChat)
		ttsBusy.Store(oldTTS)
		voiceOrb.phase, voiceOrb.ai = oldPhase, oldAI
	}()

	chatAIBusy.Store(true)
	ttsBusy.Store(false)
	voiceOrb.phase, voiceOrb.ai = orbHidden, false
	if !aiHoldsScreenAwake() {
		t.Fatal("a thinking chat AI did not hold the screen awake")
	}

	chatAIBusy.Store(false)
	ttsBusy.Store(true)
	if !aiHoldsScreenAwake() {
		t.Fatal("Juka speaking did not hold the screen awake")
	}

	ttsBusy.Store(false)
	voiceOrb.phase, voiceOrb.ai = orbThinking, true
	if !aiHoldsScreenAwake() {
		t.Fatal("the voice orb thinking did not hold the screen awake")
	}

	voiceOrb.ai = false
	if aiHoldsScreenAwake() {
		t.Fatal("a transcribing voice orb counted as AI work")
	}
}
