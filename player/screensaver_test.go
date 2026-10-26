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
		// The replacement reads the custom block, not the typed field, so the
		// row must render the value rather than the placeholder itself.
		if got := substituteVars(el.Text, config.Variables.Custom); got != "Screensaver · 25s" {
			t.Errorf("row renders %q, want \"Screensaver · 25s\"", got)
		}
		return
	}
	t.Fatal("Settings → General has no screensaver timeout row")
}

// pinIdleGateForTest saves and clears every input screensaverAutoTick reads
// that is not one of its arguments: the panels behind ambientEntryBlocked, the
// dock screen, the embedded player, the install bar, the patch label and the
// music state. The auto-tick tests run mid-suite, so an earlier test that
// leaves one of these raised holds the saver off silently and the tick test
// fails for a reason it never touched — that dependency on predecessor state
// is exactly what made the pair flaky. The install bar is reset rather than
// restored: zeroed is the baseline every install test establishes for itself.
// One input cannot be pinned: retroArchRunning scans /proc on Linux; it reads
// false on the Windows suite and finds no retroarch in CI.
func pinIdleGateForTest(t *testing.T) {
	t.Helper()

	prevOnboarding := onboarding.active
	prevQS := qs.open
	prevUpdate := updatePrompt.open
	prevCompanion := companion.open
	prevShortcuts := shortcutsOpen
	prevClip := clipSearchOpen
	prevGlobalSearch := globalSearchOpen
	prevVK := virtualKeyboardActive
	prevToolsPanel := toolsPanel.open
	prevViewerPath := imageViewerPath
	prevModal := patchModal
	prevTransition := transitionPhase
	prevFullscreen := videoFullscreen
	prevNC := nc.open
	prevStartOpen := tb.startOpen
	prevProcess := processPanel.open
	prevWifi := wifiPanel.open
	prevIP := ipPanelOpen
	prevEmu := emuPanel.open
	prevGameLib := gameLib.open
	prevGP := gpPanel.open
	prevWinApps := winAppsPanel.open
	prevPorts := portsPanel.open
	prevSave := savePanel.open
	prevCuration := curationPanel.open
	prevChiaki := chiakiPanel.open
	prevRA := raPanel.open
	prevA11y := a11y.open
	prevWP := wpOpen
	prevSocial := socialNotifOpen
	prevSmart := smartHomePanel.open
	prevMem := memPanel.open
	prevCtrlDiag := ctrldiag.open
	prevPatchLabel := patchBusyLabel
	embeddedPlayer.mu.Lock()
	prevPlayerPhase := embeddedPlayer.phase
	embeddedPlayer.mu.Unlock()
	ambient.mu.Lock()
	prevAmbientActive := ambient.active
	ambient.mu.Unlock()
	music.mu.Lock()
	prevPlaying, prevPaused := music.playing, music.paused
	music.mu.Unlock()

	t.Cleanup(func() {
		onboarding.active = prevOnboarding
		qs.open = prevQS
		updatePrompt.open = prevUpdate
		companion.open = prevCompanion
		shortcutsOpen = prevShortcuts
		clipSearchOpen = prevClip
		globalSearchOpen = prevGlobalSearch
		virtualKeyboardActive = prevVK
		toolsPanel.open = prevToolsPanel
		imageViewerPath = prevViewerPath
		patchModal = prevModal
		transitionPhase = prevTransition
		videoFullscreen = prevFullscreen
		nc.open = prevNC
		tb.startOpen = prevStartOpen
		processPanel.open = prevProcess
		wifiPanel.open = prevWifi
		ipPanelOpen = prevIP
		emuPanel.open = prevEmu
		gameLib.open = prevGameLib
		gpPanel.open = prevGP
		winAppsPanel.open = prevWinApps
		portsPanel.open = prevPorts
		savePanel.open = prevSave
		curationPanel.open = prevCuration
		chiakiPanel.open = prevChiaki
		raPanel.open = prevRA
		a11y.open = prevA11y
		wpOpen = prevWP
		socialNotifOpen = prevSocial
		smartHomePanel.open = prevSmart
		memPanel.open = prevMem
		ctrldiag.open = prevCtrlDiag
		patchBusyLabel = prevPatchLabel
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPlayerPhase
		embeddedPlayer.mu.Unlock()
		ambient.mu.Lock()
		ambient.active = prevAmbientActive
		ambient.mu.Unlock()
		music.mu.Lock()
		music.playing, music.paused = prevPlaying, prevPaused
		music.mu.Unlock()
	})

	// A scene fade left half-done by an earlier test is an on-screen event and
	// holds the saver off, so the gate has to start from a quiet panel.
	onboarding.active = false
	qs.open = false
	updatePrompt.open = false
	companion.open = false
	shortcutsOpen = false
	clipSearchOpen = false
	globalSearchOpen = false
	virtualKeyboardActive = false
	toolsPanel.open = false
	imageViewerPath = ""
	patchModal = nil
	transitionPhase = "none"
	videoFullscreen = false
	nc.open = false
	tb.startOpen = false
	processPanel.open = false
	wifiPanel.open = false
	ipPanelOpen = false
	emuPanel.open = false
	gameLib.open = false
	gpPanel.open = false
	winAppsPanel.open = false
	portsPanel.open = false
	savePanel.open = false
	curationPanel.open = false
	chiakiPanel.open = false
	raPanel.open = false
	a11y.open = false
	wpOpen = false
	socialNotifOpen = false
	smartHomePanel.open = false
	memPanel.open = false
	ctrldiag.open = false
	patchBusyLabel = ""
	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "idle"
	embeddedPlayer.mu.Unlock()
	ambient.mu.Lock()
	ambient.active = false
	ambient.mu.Unlock()
	music.mu.Lock()
	music.playing, music.paused = false, false
	music.mu.Unlock()
	resetInstallBar()
}

// screensaverGateBlocked names what keeps the saver from starting, so a
// failure reports the blocking input instead of a bare "did not start". After
// pinIdleGateForTest every line here should read clear; anything it names is
// state that changed while the test was running, which is the diagnosis.
func screensaverGateBlocked(config *Config) string {
	var why []string
	if ambientModeEnabled(config) {
		why = append(why, "ambient mode is enabled")
	}
	if ambientIsActive() {
		why = append(why, "the dock screen is active")
	}
	if ambientEntryBlocked(config) {
		if retroArchRunning() {
			why = append(why, "a retroarch process is running")
		} else {
			why = append(why, "a panel opened mid-test")
		}
	}
	if _, _, _, playing, paused := ambientNowPlaying(); playing || paused {
		why = append(why, "music was left playing")
	}
	if installInProgress() {
		why = append(why, "an install is running")
	}
	if patchInstallBusy() {
		why = append(why, "a patch install is running")
	}
	if len(why) == 0 {
		return "nothing — the idle clock itself is too young"
	}
	return strings.Join(why, "; ")
}

// The idle check must start the saver once the configured timeout has passed,
// and not a moment before.
func TestScreensaverAutoTickStartsAtConfiguredTimeout(t *testing.T) {
	withScreen(t, 1280, 720)
	pinIdleGateForTest(t)
	prevActive, prevFade, prevFading, prevTimeout, prevCheck := ss.active, ss.fadeAlpha, ss.fadingIn, screensaverIdleTimeout, screensaverLastCheck
	t.Cleanup(func() {
		ss.active, ss.fadeAlpha, ss.fadingIn = prevActive, prevFade, prevFading
		screensaverIdleTimeout, screensaverLastCheck = prevTimeout, prevCheck
	})
	ss.active, ss.fadingIn, ss.fadeAlpha = false, false, 255
	screensaverIdleTimeout = 25 * time.Second
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
		t.Fatalf("screensaver did not start after the 25s timeout (blocked by %s; input age %s)",
			screensaverGateBlocked(config), userInputAge(now))
	}
}

// Playback and background downloads are use without input: the saver waits.
func TestScreensaverAutoTickHeldOffByActivity(t *testing.T) {
	withScreen(t, 1280, 720)
	pinIdleGateForTest(t)
	prevActive, prevFade, prevFading, prevTimeout, prevCheck := ss.active, ss.fadeAlpha, ss.fadingIn, screensaverIdleTimeout, screensaverLastCheck
	t.Cleanup(func() {
		ss.active, ss.fadeAlpha, ss.fadingIn = prevActive, prevFade, prevFading
		screensaverIdleTimeout, screensaverLastCheck = prevTimeout, prevCheck
	})
	ss.active, ss.fadingIn, ss.fadeAlpha = false, false, 255
	screensaverIdleTimeout = 25 * time.Second
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
		t.Fatalf("screensaver covered the panel while music was playing (blocked by %s)",
			screensaverGateBlocked(config))
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
