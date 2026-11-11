package main

// voicecmd_exec_test.go — what a command does where that is decided without a
// device: quitting always asks, and "unmute" never goes to zero.

import "testing"

// "quit" asks before quitting: through the Exit screen when the config has
// one, else as a one-row list whose press is the confirmation.
func TestVoiceQuitAlwaysAsks(t *testing.T) {
	with := &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Exit"}}}
	out := voiceQuitOutcome(with)
	if out.Act == nil || len(out.Choices) != 0 {
		t.Fatalf("with an Exit screen: %+v, want the Exit screen's confirmation", out)
	}
	for _, cfg := range []*Config{nil, {Scenes: []SceneConfig{{Name: "Main"}}}} {
		out := voiceQuitOutcome(cfg)
		if out.Act != nil {
			t.Fatal("without an Exit screen, quit must not run after the pause")
		}
		if len(out.Choices) != 1 || !out.Choices[0].Target.Quit {
			t.Fatalf("without an Exit screen: %+v, want a one-row Quit list", out)
		}
		// The press on that row is the confirmation: it quits.
		if pick := voiceOutcomeFor(out.Choices[0]); pick.Act == nil {
			t.Fatal("picking Quit JukaHub from the list does nothing")
		}
	}
	if out := voiceExecute(&Config{}, voiceIntent{Kind: voiceIntentQuit}, voiceFromTyped); out.Act != nil {
		t.Fatal(`"quit" on a config without an Exit screen ran without asking`)
	}
}

// "unmute" goes to the last audible level, else the slider's saved level, else
// the default — never to zero.
func TestVoiceUnmuteLevelIsAudible(t *testing.T) {
	volumeLevel.mu.Lock()
	oldPct, oldKnown, oldRestore := volumeLevel.pct, volumeLevel.known, volumeLevel.restore
	volumeLevel.mu.Unlock()
	oldSliders := qs.sliders
	t.Cleanup(func() {
		volumeLevel.mu.Lock()
		volumeLevel.pct, volumeLevel.known, volumeLevel.restore = oldPct, oldKnown, oldRestore
		volumeLevel.mu.Unlock()
		qs.sliders = oldSliders
	})

	set := func(pct int, known bool, restore int) {
		volumeLevel.mu.Lock()
		volumeLevel.pct, volumeLevel.known, volumeLevel.restore = pct, known, restore
		volumeLevel.mu.Unlock()
	}
	qs.sliders = nil
	set(0, false, 35)
	if got := voiceUnmuteLevel(); got != 35 {
		t.Fatalf("with a remembered level: %d, want 35", got)
	}
	set(0, false, 0)
	if got := voiceUnmuteLevel(); got != volumeUnmuteDefault {
		t.Fatalf("with nothing known: %d, want the default %d", got, volumeUnmuteDefault)
	}
	qs.sliders = []qsSlider{{Action: "volume", Value: 0.7, Available: true}}
	if got := voiceUnmuteLevel(); got != 70 {
		t.Fatalf("with the slider's saved level: %d, want 70", got)
	}
	// A saved level of zero (muted last time) is not a level to unmute to.
	qs.sliders = []qsSlider{{Action: "volume", Value: 0, Available: true}}
	if got := voiceUnmuteLevel(); got <= 0 {
		t.Fatalf("unmute level %d", got)
	}
}
