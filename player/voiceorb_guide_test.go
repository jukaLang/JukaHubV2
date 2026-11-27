package main

import (
	"strings"
	"testing"
)

// The Guide/Menu voice button is a handheld default and a Windows opt-in;
// Windows' Game Bar can take Guide first, so the switch has to say so.
func TestVoiceOrbGuideAvailabilityAndCaveat(t *testing.T) {
	if !voiceOrbGuideAvailableFor(true, false) {
		t.Fatal("a handheld must offer the voice button")
	}
	if !voiceOrbGuideAvailableFor(false, true) {
		t.Fatal("Windows must offer the voice button as an opt-in")
	}
	if voiceOrbGuideAvailableFor(false, false) {
		t.Fatal("a platform with neither Menu nor Guide must not offer the voice button")
	}
	if got := voiceOrbGuideLabel(false, true); !strings.Contains(got, "Guide") {
		t.Errorf("Windows label = %q, want Guide named", got)
	}
	if got := voiceOrbGuideLabel(true, false); !strings.Contains(got, "Menu") {
		t.Errorf("handheld label = %q, want Menu named", got)
	}
	detail := voiceOrbGuideDetail(false, true)
	for _, want := range []string{"Guide", "Game Bar"} {
		if !strings.Contains(detail, want) {
			t.Errorf("Windows detail = %q, want %q", detail, want)
		}
	}
	if !strings.Contains(voiceOrbGuideDetail(true, false), "hold it to talk") {
		t.Error("the handheld detail must keep the hold-to-talk instruction")
	}
}

// On Windows the setting is off until chosen, while a handheld starts with
// Menu as the voice button.
func TestVoiceOrbGuideDefaultsFollowThePlatform(t *testing.T) {
	if !P().IsHandheld() {
		if voiceOrbMenuEnabled(nil) {
			t.Fatal("the voice button must be off by default off a handheld")
		}
		if voiceOrbMenuEnabled(&Config{}) {
			t.Fatal("a Windows config with no stored choice must leave Guide to the system")
		}
	}
}
