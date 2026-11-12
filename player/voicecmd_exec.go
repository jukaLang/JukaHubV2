package main

// voicecmd_exec.go — what a voice command does.
//
// voiceExecute turns a parsed intent (voicecmd_intent.go) into a
// voiceOutcome: one sentence saying what happened or will happen, and — for
// anything that leaves the voice page — the action to run. The page shows the
// sentence, waits a moment so a misheard command can still be cancelled, then
// runs the action (voicecmd.go); a command from the phone runs at once and the
// sentence goes back to the phone as its answer (voicecmd_phone.go).
//
// Every action is an existing control: the volume and brightness paths the
// header's buttons and Quick Settings use (volumectl.go, brightnessctl.go),
// Quick Settings' own Wi-Fi and Bluetooth switches, the music player's
// transport, the screenshot and the screensaver requests, the Browser and the
// Home search sheet. Nothing here talks to the hardware directly, so a device
// that cannot do something says so in the same words its own control would.
//
// Main thread only: the actions touch UI state.

import (
	"strconv"
	"strings"
	"time"
)

// voiceSource is where a command came from.
type voiceSource int

const (
	voiceFromMic voiceSource = iota
	voiceFromTyped
	voiceFromPhone
)

// voiceFix is the one-press repair an error offers.
type voiceFix int

const (
	voiceFixNone voiceFix = iota
	voiceFixGroqKey
	voiceFixPhone
	voiceFixType
)

// voiceOutcome is what a command did, or will do.
type voiceOutcome struct {
	// Say is the answer in one sentence ("Opening Media", "Volume 60%").
	Say  string
	Tone tbxTone
	// Act is the navigation to run; nil when the command already ran (Done)
	// or only answered.
	Act func(*Config)
	// Leave runs Act after leaving the voice page: a screenshot, the
	// screensaver, Quick Settings and the search sheet are about the screen
	// behind it, not the page itself.
	Leave bool
	// Done means the command already ran (a level, a switch, the music).
	Done bool
	// Choices is the "did you mean" list; Search and Web offer the Home
	// search sheet and the Browser with these words when nothing matched.
	Choices []voiceMatch
	Search  string
	Web     string
	Fix     voiceFix
	// Cancel asks the page to stop and return to its ready state.
	Cancel bool
}

// voiceExecute runs (or prepares) one intent.
func voiceExecute(config *Config, in voiceIntent, src voiceSource) voiceOutcome {
	if config == nil {
		config = appConfig
	}
	// A word the user taught (Settings → Voice & AI → Voice shortcuts) comes
	// before the grammar: they said what it means. Cancel and help still win
	// (voiceshortcuts.go).
	if out, ok := voiceShortcutExecute(config, in, src); ok {
		return out
	}
	switch in.Kind {
	case voiceIntentNone:
		// Nothing usable was heard: silence, a breath Whisper wrote down as
		// "you", or the wake word alone.
		return voiceOutcome{Say: "Didn't catch a command — say what to open, like “open YouTube”", Tone: tbxToneWarn}
	case voiceIntentCancel:
		return voiceOutcome{Say: "Cancelled", Cancel: true}
	case voiceIntentHelp:
		return voiceOutcome{Done: true, Say: "Say “open” and a name — a screen, an app or a game — or “play” a game or " +
			"music, “volume 50”, “brighter”, “take a screenshot”, “turn off Wi-Fi”, “next song”, “what time is it”. " +
			"Teach your own words in Settings → Voice & AI → Voice shortcuts."}
	case voiceIntentTime:
		return voiceOutcome{Done: true, Say: "It is " + clockDigits(config, time.Now())}
	case voiceIntentDate:
		return voiceOutcome{Done: true, Say: "Today is " + shotClock(time.Now()).Format("Monday, 2 January 2006")}
	case voiceIntentBattery:
		return voiceOutcome{Done: true, Say: voiceBatterySentence()}
	case voiceIntentVolumeSet, voiceIntentVolumeStep, voiceIntentMute, voiceIntentUnmute:
		return voiceVolume(config, in)
	case voiceIntentBrightnessSet, voiceIntentBrightnessStep:
		return voiceBrightness(config, in)
	case voiceIntentScreenshot:
		return voiceOutcome{Say: "Taking a screenshot", Leave: true, Act: func(*Config) { requestScreenshot() }}
	case voiceIntentSleepScreen:
		return voiceOutcome{Say: "Turning the screen off", Leave: true, Act: func(*Config) { activateScreensaver() }}
	case voiceIntentQuickSettings:
		return voiceOutcome{Say: "Opening Quick Settings", Leave: true, Act: func(*Config) {
			if !qs.open {
				qsToggleOpen()
			}
		}}
	case voiceIntentWifi:
		return voiceRadioOutcome(qsActionWifi, "Wi-Fi", in.On, src)
	case voiceIntentBluetooth:
		return voiceRadioOutcome(qsActionBluetooth, "Bluetooth", in.On, src)
	case voiceIntentAirplane:
		return voiceRadioOutcome(qsActionAirplane, "Airplane mode", in.On, src)
	case voiceIntentHome:
		return voiceOutcome{Say: "Going Home", Act: voiceGoHome}
	case voiceIntentBack:
		return voiceOutcome{Say: "Going back", Act: func(c *Config) { goBackScene(c) }}
	case voiceIntentMusicPause, voiceIntentMusicResume, voiceIntentMusicNext, voiceIntentMusicPrev, voiceIntentMusicPlay:
		return voiceMusic(config, in.Kind)
	case voiceIntentWebSearch:
		q := strings.TrimSpace(in.Query)
		if !IsOnline() {
			return voiceOutcome{Say: "Searching the web needs internet", Tone: tbxToneWarn}
		}
		url := homeSearchWebURL(q)
		return voiceOutcome{Say: "Searching the web for “" + q + "”", Act: func(c *Config) { openBrowserAtURL(c, url) }}
	case voiceIntentSearch:
		q := strings.TrimSpace(in.Query)
		return voiceOutcome{Say: "Searching for “" + q + "”", Leave: true, Act: func(c *Config) { voiceOpenSearchSheet(c, q) }}
	case voiceIntentQuit:
		return voiceQuitOutcome(config)
	case voiceIntentOpen:
		return voiceOpenTarget(config, in)
	}
	return voiceOutcome{Say: "Didn't catch a command — say what to open, like “open YouTube”", Tone: tbxToneWarn}
}

// voiceQuitOutcome asks before quitting. With the Exit screen the usual
// confirmation does the asking (startMenuPower opens it); a config without
// one would quit on the spot there, so the page lists "Quit JukaHub" as the
// only choice instead and a press on it is the confirmation — on the phone
// too, where it arrives as a one-row list.
func voiceQuitOutcome(config *Config) voiceOutcome {
	if config != nil && sceneIndexExact(config, "Exit") >= 0 {
		return voiceOutcome{Say: "Quit JukaHub?", Act: startMenuPower}
	}
	quit := voiceMatch{Target: voiceTarget{Title: "Quit JukaHub", Where: "Press to quit", Icon: "power",
		Kind: voiceTargetScreen, Quit: true}, Score: 1000}
	return voiceOutcome{Say: "Quit JukaHub?", Choices: []voiceMatch{quit}}
}

// voiceOpenTarget resolves "open <name>" against the Home search index.
func voiceOpenTarget(config *Config, in voiceIntent) voiceOutcome {
	matches := voiceRankTargets(in.Target, in.Verb, voiceCatalog(config))
	if voiceDecide(matches) {
		if matches[0].Target.Quit {
			// The catalog's "Quit JukaHub" row asks like the spoken "quit".
			return voiceQuitOutcome(config)
		}
		return voiceOutcomeFor(matches[0])
	}
	choices := voiceChoices(matches, voiceMaxChoices)
	if len(choices) == 1 && choices[0].Target.Blocked != "" {
		return voiceOutcome{Say: choices[0].Target.Title + ": " + choices[0].Target.Blocked, Tone: tbxToneWarn}
	}
	if len(choices) > 0 {
		return voiceOutcome{Say: "Which one did you mean?", Choices: choices}
	}
	words := in.Target
	out := voiceOutcome{Say: "Nothing called “" + words + "” here", Tone: tbxToneWarn, Search: words}
	if IsOnline() {
		out.Web = words
	}
	return out
}

// voiceOutcomeFor is what opening one match does.
func voiceOutcomeFor(m voiceMatch) voiceOutcome {
	t := m.Target
	if t.Blocked != "" {
		return voiceOutcome{Say: t.Title + ": " + t.Blocked, Tone: tbxToneWarn}
	}
	if t.Quit {
		return voiceOutcome{Say: "Quit JukaHub?", Act: startMenuPower}
	}
	verb := "Opening "
	switch t.Kind {
	case voiceTargetGame:
		verb = "Starting "
	case voiceTargetFile, voiceTargetMedia:
		verb = "Playing "
		if t.Kind == voiceTargetFile && !voicePlayableKind(t.Icon) {
			verb = "Opening "
		}
	}
	return voiceOutcome{Say: verb + t.Title, Act: t.Action}
}

// voicePlayableKind reports whether a file kind plays rather than opens.
func voicePlayableKind(kind string) bool {
	switch kind {
	case "music", "video":
		return true
	}
	return false
}

// voiceGoHome switches to Home without putting the voice page on the scene
// history: B on Home has nowhere to go anyway.
func voiceGoHome(config *Config) {
	if config == nil {
		return
	}
	if home := homeSceneIndex(config); home >= 0 && home != currentSceneIndex {
		switchScene(config, home, false)
	}
}

// voiceOpenSearchSheet opens the Home search sheet with the words typed in,
// so the full result list (files, media, the web row) is one press away.
func voiceOpenSearchSheet(config *Config, words string) {
	homeSearchOpen(config, false)
	homeSearchSetQuery(words)
	if len(gsResults) > 0 {
		gsCursor = 0
	}
}

// voiceBatterySentence answers "how much battery".
func voiceBatterySentence() string {
	pct := getBatteryPercent()
	if pct < 0 {
		return "This device does not report a battery level"
	}
	say := "Battery " + strconv.Itoa(pct) + "%"
	if getBatteryCharging() {
		say += ", charging"
	}
	if eta := batteryETAPhrase(); eta != "" {
		say += ", " + eta
	}
	return say
}

// voiceVolume runs the volume intents through the header's volume control.
func voiceVolume(config *Config, in voiceIntent) voiceOutcome {
	if !volumeControlAvailable() {
		return voiceOutcome{Say: "JukaHub cannot change the volume on this device", Tone: tbxToneWarn}
	}
	var pct int
	switch in.Kind {
	case voiceIntentVolumeSet:
		pct = volumeSetLevel(config, in.Value)
	case voiceIntentVolumeStep:
		_, pct = volumeStepBy(config, in.Value)
	case voiceIntentMute:
		if cur, ok := volumeCurrent(); ok && cur == 0 {
			return voiceOutcome{Done: true, Say: "The sound is already off"}
		}
		// An explicit level, not the toggle: before the first mixer reading
		// the toggle decides from a guessed level and could unmute instead.
		// Setting 0 keeps the last audible level for "unmute".
		pct = volumeSetLevel(config, 0)
	case voiceIntentUnmute:
		if cur, ok := volumeCurrent(); ok && cur > 0 {
			return voiceOutcome{Done: true, Say: "The sound is on (" + strconv.Itoa(cur) + "%)"}
		}
		pct = volumeSetLevel(config, voiceUnmuteLevel())
	}
	if pct <= 0 {
		return voiceOutcome{Done: true, Say: "Sound off"}
	}
	return voiceOutcome{Done: true, Say: "Volume " + strconv.Itoa(pct) + "%"}
}

// voiceUnmuteLevel is where "unmute" goes: the last audible level the volume
// control remembers, else the saved slider level, else the control's default.
// Never 0 — "unmute" that mutes was the bug this replaces.
func voiceUnmuteLevel() int {
	volumeLevel.mu.Lock()
	restore := volumeLevel.restore
	volumeLevel.mu.Unlock()
	if restore > 0 {
		return restore
	}
	if base := volumeBase(); base > 0 {
		return base
	}
	return volumeUnmuteDefault
}

// voiceBrightness runs the brightness intents through the header's control.
func voiceBrightness(config *Config, in voiceIntent) voiceOutcome {
	if !brightnessControlAvailable() {
		return voiceOutcome{Say: "JukaHub cannot change the brightness on this device", Tone: tbxToneWarn}
	}
	var pct int
	if in.Kind == voiceIntentBrightnessSet {
		pct = brightnessSetLevel(config, in.Value)
	} else {
		_, pct = brightnessStepBy(config, in.Value)
	}
	return voiceOutcome{Done: true, Say: "Brightness " + strconv.Itoa(pct) + "%"}
}

// voiceQSToggle is Quick Settings' switch for action and its row, or -1.
func voiceQSToggle(action qsAction) (int, *qsToggle) {
	for i := range qs.toggles {
		if qs.toggles[i].Action == action {
			return i, &qs.toggles[i]
		}
	}
	return -1, nil
}

// voiceRadioOutcome switches Wi-Fi, Bluetooth or airplane mode with Quick
// Settings' own switch, so the panel, its saved state and its error messages
// agree. Switching a radio on happens at once. Switching one off (or airplane
// mode on) waits the same cancellable moment as a navigation: a misheard
// "Wi-Fi off" can still be stopped with B, and once it runs, the microphone
// path has no internet to turn it back on with — the answer says where.
func voiceRadioOutcome(action qsAction, label string, on bool, src voiceSource) voiceOutcome {
	_, t := voiceQSToggle(action)
	if t == nil {
		return voiceOutcome{Say: "There is no " + label + " switch on this device", Tone: tbxToneWarn}
	}
	name := t.Label
	if !t.Available {
		why := strings.TrimSpace(t.Detail)
		if why == "" {
			why = "no control on this device"
		}
		return voiceOutcome{Say: name + " cannot be switched here (" + why + ")", Tone: tbxToneWarn}
	}
	// The switch's own reading can be minutes old: ask the radio.
	var live, known bool
	switch action {
	case qsActionWifi:
		live, known = wifiEnabled()
	case qsActionBluetooth:
		live, known = bluetoothEnabled()
	}
	if known {
		t.Value = live
	}
	if t.Value == on {
		return voiceOutcome{Done: true, Say: name + " is already " + strings.ToLower(onOff(on))}
	}
	cutsOff := (!on && action != qsActionAirplane) || (on && action == qsActionAirplane)
	if !cutsOff {
		return voiceRadioSwitchNow(action, on)
	}
	netGone := action == qsActionWifi || action == qsActionAirplane
	say := "Turning " + name + " " + strings.ToLower(onOff(on))
	switch {
	case netGone && src == voiceFromPhone:
		// The phone reaches the handheld over Wi-Fi, so this is the last
		// answer it gets.
		say += " — this page loses the handheld now"
	case netGone:
		say += " — voice needs internet; Quick Settings turns it back on"
	}
	return voiceOutcome{Say: say, Act: func(c *Config) {
		out := voiceRadioSwitchNow(action, on)
		switch {
		case voiceOnPage(c):
			voiceSay(out.Say, out.Tone, voiceFixNone)
			if out.Tone == tbxToneWarn {
				PlayErrorSound()
			}
		case out.Tone == tbxToneWarn:
			// Off the page (a phone command) the toast is the only report.
			ShowToast(out.Say, ToastKindWarning)
		}
	}}
}

// voiceRadioSwitchNow flips Quick Settings' switch for action to on and says
// how it went. The row is found again: the Act runs a moment after the
// outcome was made.
func voiceRadioSwitchNow(action qsAction, on bool) voiceOutcome {
	i, t := voiceQSToggle(action)
	if t == nil {
		return voiceOutcome{Say: "That switch is gone from Quick Settings", Tone: tbxToneWarn}
	}
	name := t.Label
	if t.Value == on {
		return voiceOutcome{Done: true, Say: name + " is already " + strings.ToLower(onOff(on))}
	}
	qsToggleRow(i)
	if t.Value != on {
		// qsToggleRow showed what the radio said.
		return voiceOutcome{Say: name + " did not switch " + strings.ToLower(onOff(on)), Tone: tbxToneWarn}
	}
	say := name + " " + strings.ToLower(onOff(on))
	switch {
	case action == qsActionWifi && !on:
		say += " — turn it back on in Quick Settings"
	case action == qsActionAirplane && on:
		say += " — turn it off in Quick Settings"
	}
	return voiceOutcome{Done: true, Say: say, Tone: tbxToneGood}
}

// voiceMusicState is the player's transport as the music intents need it.
type voiceMusicState struct {
	playing, paused, hasTrack bool
	title                     string
}

func voiceMusicNow() voiceMusicState {
	music.mu.Lock()
	defer music.mu.Unlock()
	st := voiceMusicState{playing: music.playing, paused: music.paused}
	if music.current >= 0 && music.current < len(music.tracks) {
		st.hasTrack = true
		st.title = music.tracks[music.current].Name
		if st.title == "" {
			st.title = gameDisplayTitle(music.tracks[music.current].Path)
		}
	}
	return st
}

// voiceMusic runs the transport intents on the music player.
func voiceMusic(config *Config, kind voiceIntentKind) voiceOutcome {
	st := voiceMusicNow()
	switch kind {
	case voiceIntentMusicPause:
		switch {
		case st.playing && !st.paused:
			musicTogglePause()
			return voiceOutcome{Done: true, Say: "Music paused"}
		case st.playing:
			return voiceOutcome{Done: true, Say: "The music is already paused"}
		}
		return voiceOutcome{Say: "Nothing is playing", Tone: tbxToneWarn}
	case voiceIntentMusicResume, voiceIntentMusicPlay:
		switch {
		case st.playing && st.paused, !st.playing && st.hasTrack:
			musicTogglePause()
			return voiceOutcome{Done: true, Say: voiceNowPlaying(st.title)}
		case st.playing:
			return voiceOutcome{Done: true, Say: voiceNowPlaying(st.title)}
		}
		if kind == voiceIntentMusicResume {
			return voiceOutcome{Say: "Nothing to resume — say “play music”", Tone: tbxToneWarn}
		}
		return voiceOutcome{Say: "Opening Music", Act: func(c *Config) { openAudioPlayerBrowser(c, musicDefaultDir(c)) }}
	case voiceIntentMusicNext, voiceIntentMusicPrev:
		if !st.hasTrack {
			return voiceOutcome{Say: "Nothing is playing", Tone: tbxToneWarn}
		}
		if kind == voiceIntentMusicNext {
			musicNext()
			return voiceOutcome{Done: true, Say: "Next song"}
		}
		musicPrev()
		return voiceOutcome{Done: true, Say: "Previous song"}
	}
	return voiceOutcome{Say: "Nothing is playing", Tone: tbxToneWarn}
}

// voiceNowPlaying is "Playing <title>", or just "Playing".
func voiceNowPlaying(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "Playing"
	}
	return "Playing " + title
}
