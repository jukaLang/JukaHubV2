package main

import "testing"

func TestIdleScreenHoldOffWhileAIWorks(t *testing.T) {
	if !idleScreenHoldOffFrom(false, false, false, false, false, false, true) {
		t.Fatal("an active AI turn did not hold the idle screen off")
	}
	if idleScreenHoldOffFrom(false, false, false, false, false, false, false) {
		t.Fatal("an idle device held the screen on without a reason")
	}
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
