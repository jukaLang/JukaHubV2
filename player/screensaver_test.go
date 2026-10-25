package main

import (
	"strings"
	"testing"
	"time"
)

// The idle screen must stay off while the device is doing something: audio,
// video, a download, an install or an open panel. The decision is pure so every
// combination can be pinned.
func TestIdleScreenHoldOff(t *testing.T) {
	cases := []struct {
		name                                        string
		uiBlocked, music, paused, download, patched bool
		want                                        bool
	}{
		{"idle device", false, false, false, false, false, false},
		{"panel open", true, false, false, false, false, true},
		{"music playing", false, true, false, false, false, true},
		{"music paused", false, false, true, false, false, true},
		{"download running", false, false, false, true, false, true},
		{"patch install running", false, false, false, false, true, true},
	}
	for _, tc := range cases {
		got := idleScreenHoldOffFrom(tc.uiBlocked, tc.music, tc.paused, tc.download, tc.patched)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Messages that arrive while the device is idle are what the saver shows, and
// only the newest handful, oldest first, inside the freshness window.
func TestScreensaverChatFeed(t *testing.T) {
	ssChatFeed.mu.Lock()
	ssChatFeed.notes = nil
	ssChatFeed.mu.Unlock()
	t.Cleanup(func() {
		ssChatFeed.mu.Lock()
		ssChatFeed.notes = nil
		ssChatFeed.mu.Unlock()
	})

	noteScreensaverChat("Ada", "first")
	noteScreensaverChat("Bob", "second")
	noteScreensaverChat("Cy", "   ")
	now := time.Now()
	notes := screensaverChatNotes(now, ssChatShown)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want the two non-empty messages", notes)
	}
	if notes[0].sender != "Ada" || notes[1].sender != "Bob" {
		t.Errorf("notes out of order: %+v", notes)
	}

	for i := 0; i < ssChatFeedLimit+3; i++ {
		noteScreensaverChat("Bot", "spam")
	}
	notes = screensaverChatNotes(now, ssChatShown)
	if len(notes) != ssChatShown {
		t.Fatalf("shown = %d, want %d", len(notes), ssChatShown)
	}
	ssChatFeed.mu.Lock()
	size := len(ssChatFeed.notes)
	ssChatFeed.mu.Unlock()
	if size != ssChatFeedLimit {
		t.Errorf("feed kept %d notes, want the %d cap", size, ssChatFeedLimit)
	}

	// A note older than the window must not reappear later in the day.
	ssChatFeed.mu.Lock()
	for i := range ssChatFeed.notes {
		ssChatFeed.notes[i].at = now.Add(-ssChatNoteTTL - time.Minute)
	}
	ssChatFeed.mu.Unlock()
	if got := screensaverChatNotes(now, ssChatShown); len(got) != 0 {
		t.Errorf("stale notes were shown: %+v", got)
	}
}

// The timeout is the same value in the package timer, the config, the Quick
// Settings row and the saved user config.
func TestApplyScreensaverTimeout(t *testing.T) {
	prevTimeout := screensaverIdleTimeout
	// The panel may not have been initialized (qsInit runs at startup), so
	// pad the cycles slice to the layout the production code writes into.
	for len(qs.cycles) <= qsCycleScreensaverRow {
		qs.cycles = append(qs.cycles, qsCycle{})
	}
	prevCycle := qs.cycles[qsCycleScreensaverRow].Value
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = false
	t.Cleanup(func() {
		screensaverIdleTimeout = prevTimeout
		qs.cycles[qsCycleScreensaverRow].Value = prevCycle
		userConfigWritesEnabled = prevWrites
	})

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	if got := applyScreensaverTimeout(config, 25); got != 25 {
		t.Fatalf("apply returned %d", got)
	}
	if screensaverIdleTimeout != 25*time.Second {
		t.Errorf("idle timeout = %v, want 25s", screensaverIdleTimeout)
	}
	if config.Variables.ScreensaverTimeout != 25 {
		t.Errorf("config timeout = %d", config.Variables.ScreensaverTimeout)
	}
	if qs.cycles[qsCycleScreensaverRow].Value != 25 {
		t.Errorf("quick settings value = %d", qs.cycles[qsCycleScreensaverRow].Value)
	}
	if got := config.Variables.Get("screensaverTimeoutLabel"); got != "25s" {
		t.Errorf("label = %q, want 25s", got)
	}

	// A zero/negative value falls back to the default instead of disabling the
	// saver or making it fire immediately.
	applyScreensaverTimeout(config, 0)
	if screensaverIdleTimeout != 25*time.Second {
		t.Errorf("zero timeout = %v, want the 25s default", screensaverIdleTimeout)
	}
}

// Settings → General must actually offer the timeout, with a label that reads
// the configured value rather than raw seconds.
func TestSettingsOffersScreensaverTimeout(t *testing.T) {
	config := &Config{
		Scenes: []SceneConfig{{Name: "SettingsGeneral"}},
	}
	config.Variables.Custom = map[string]interface{}{}
	config.Variables.ScreensaverTimeout = 25
	ensureSettingsWidgets(config)
	scene := settingsGeneralScene(config)
	if scene == nil {
		t.Fatal("no SettingsGeneral scene")
	}
	for _, el := range scene.Elements {
		if el.Trigger != "cycle_screensaver_timeout" {
			continue
		}
		if !strings.Contains(el.Text, "$screensaverTimeoutLabel") {
			t.Errorf("row text %q does not show the current timeout", el.Text)
		}
		if got := config.Variables.Get("screensaverTimeoutLabel"); got != "25s" {
			t.Errorf("resolved label = %q, want 25s", got)
		}
		return
	}
	t.Fatal("Settings → General has no screensaver timeout row")
}

// The idle check must start the saver once the configured timeout has passed,
// and not a moment before.
func TestScreensaverAutoTickStartsAtConfiguredTimeout(t *testing.T) {
	withScreen(t, 1280, 720)
	prevActive, prevFade, prevFading, prevTimeout, prevCheck := ss.active, ss.fadeAlpha, ss.fadingIn, screensaverIdleTimeout, screensaverLastCheck
	prevTransition := transitionPhase
	t.Cleanup(func() {
		ss.active, ss.fadeAlpha, ss.fadingIn = prevActive, prevFade, prevFading
		screensaverIdleTimeout, screensaverLastCheck = prevTimeout, prevCheck
		transitionPhase = prevTransition
	})
	ss.active, ss.fadingIn, ss.fadeAlpha = false, false, 255
	screensaverIdleTimeout = 25 * time.Second
	// An earlier test may leave a scene fade half-done; a transition is an
	// on-screen event, and it holds the saver off.
	transitionPhase = "none"
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientMode": false}

	now := time.Now()
	screensaverLastCheck = time.Time{}
	noteUserInputTime(now.Add(-10 * time.Second))
	screensaverAutoTick(config, now)
	if ss.active {
		t.Fatal("screensaver started 10s into a 25s timeout")
	}

	screensaverLastCheck = time.Time{}
	noteUserInputTime(now.Add(-26 * time.Second))
	screensaverAutoTick(config, now)
	if !ss.active {
		t.Fatal("screensaver did not start after the 25s timeout")
	}
}

// Playback and background downloads are use without input: the saver waits.
func TestScreensaverAutoTickHeldOffByActivity(t *testing.T) {
	withScreen(t, 1280, 720)
	prevActive, prevFade, prevFading, prevTimeout, prevCheck := ss.active, ss.fadeAlpha, ss.fadingIn, screensaverIdleTimeout, screensaverLastCheck
	music.mu.Lock()
	prevPlaying, prevPaused := music.playing, music.paused
	music.mu.Unlock()
	prevTransition := transitionPhase
	t.Cleanup(func() {
		ss.active, ss.fadeAlpha, ss.fadingIn = prevActive, prevFade, prevFading
		screensaverIdleTimeout, screensaverLastCheck = prevTimeout, prevCheck
		transitionPhase = prevTransition
		music.mu.Lock()
		music.playing, music.paused = prevPlaying, prevPaused
		music.mu.Unlock()
	})
	ss.active, ss.fadingIn, ss.fadeAlpha = false, false, 255
	screensaverIdleTimeout = 25 * time.Second
	transitionPhase = "none"
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"ambientMode": false}

	music.mu.Lock()
	music.playing, music.paused = true, false
	music.mu.Unlock()

	now := time.Now()
	screensaverLastCheck = time.Time{}
	noteUserInputTime(now.Add(-5 * time.Minute))
	screensaverAutoTick(config, now)
	if ss.active {
		t.Fatal("screensaver covered the panel while music was playing")
	}
}

// 300 s was the old default, so an untouched user file must move to the new
// 25 s default once instead of keeping a five-minute saver on every device.
func TestLegacyScreensaverTimeoutMigrates(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	mergeUserConfig(config, &UserConfig{Variables: UserVariables{ScreensaverTimeout: 300}})
	if config.Variables.ScreensaverTimeout != 25 {
		t.Fatalf("legacy timeout = %d, want 25", config.Variables.ScreensaverTimeout)
	}
	if v, ok := customValue(config.Variables.Custom, "screensaverTimeoutMigrated"); !ok || v != true {
		t.Errorf("migration marker = %v, want true", v)
	}

	// Once migrated, a deliberate 300 s choice survives a reload.
	config2 := &Config{}
	config2.Variables.Custom = map[string]interface{}{"screensaverTimeoutMigrated": true}
	mergeUserConfig(config2, &UserConfig{Variables: UserVariables{ScreensaverTimeout: 300}})
	if config2.Variables.ScreensaverTimeout != 300 {
		t.Errorf("chosen timeout = %d, want 300", config2.Variables.ScreensaverTimeout)
	}
}

func TestScreenTimeoutStepsIncludeFastDefault(t *testing.T) {
	found := false
	for _, step := range screenTimeoutSteps {
		if step == 25 {
			found = true
		}
	}
	if !found {
		t.Fatalf("steps %v do not offer the 25s default", screenTimeoutSteps)
	}
	if got := nextScreenTimeout(15); got != 25 {
		t.Errorf("next after 15s = %d, want 25", got)
	}
}
