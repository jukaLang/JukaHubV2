package main

// homesearch_verbs.go — commands in Home search.
//
// Typing "wifi off", "brightness 40", "screenshot", "sleep", "update" or
// "new note" into the search sheet (globalsearch.go) lists the command first,
// under "Commands", with what it would do and the value now: "Turn Wi-Fi off ·
// Wi-Fi is on now", "Set brightness to 40% · now 70%". A runs it the way the
// voice command does (voicecmd_exec.go: the header's volume and brightness,
// Quick Settings' own switches, the screenshot request, the music player), and
// the settings sentences voice understands ("dark mode on", "text size
// bigger") work as well, through their Settings rows (voicesettings.go). The
// rest of the results — screens, apps, games, files — follow as before, so a
// game called "Sleep" is still found under the command.
//
// The grammar is voice's (voicecmd_intent.go: English phrase shapes) plus a
// few words that only make sense typed: "update", "new note", "sleep" for the
// whole handheld. A word typed one edit away from a command word — a letter
// missing, extra or wrong, or two letters swapped ("wfi", "brigthness",
// "screnshot") — is read as that word (homeSearchFixTypos): a bounded
// Damerau check over a short list, no fuzzy library. Only the command reading
// is corrected; the name search below still gets what was typed.

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// homeSearchCommandsCategory is the group the command rows sit in, first.
const homeSearchCommandsCategory = "Commands"

// homeSearchCommandMaxRunes: a longer query is a search, not a command.
const homeSearchCommandMaxRunes = 60

// homeSearchCommandWords are the words commands are made of; a typed word one
// edit away from exactly one of them is read as it. Words under four letters
// are never corrected ("on" and "of" are one edit apart).
var homeSearchCommandWords = []string{
	"wifi", "bluetooth", "airplane", "volume", "brightness", "screenshot", "sleep", "update",
	"mute", "unmute", "louder", "quieter", "brighter", "dimmer", "note", "pause", "resume",
	"next", "previous", "battery", "music", "screen", "quick", "settings", "install", "close",
	"turn", "enable", "disable", "maximum", "minimum", "percent",
}

// damerauWithinOne reports whether a and b are at most one edit apart: one
// rune inserted, deleted or replaced, or two neighbours swapped. Pure.
func damerauWithinOne(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == len(rb) {
		first := -1
		for i := range ra {
			if ra[i] == rb[i] {
				continue
			}
			if first < 0 {
				first = i
				continue
			}
			// A second difference: only a swap of neighbours is one edit.
			return first == i-1 && ra[first] == rb[i] && ra[i] == rb[first] && string(ra[i+1:]) == string(rb[i+1:])
		}
		return true
	}
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(ra)-len(rb) != 1 {
		return false
	}
	// ra is one rune longer: skip one rune of it, once.
	skipped := false
	for i, j := 0, 0; j < len(rb); {
		if ra[i] == rb[j] {
			i++
			j++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		i++
	}
	return true
}

// homeSearchFixTypos reads each word of q that is one edit away from exactly
// one command word as that word.
func homeSearchFixTypos(q string) string {
	words := strings.Fields(strings.ToLower(q))
	changed := false
	for i, w := range words {
		if utf8.RuneCountInString(w) < 4 {
			continue
		}
		match, n := "", 0
		for _, c := range homeSearchCommandWords {
			if w == c {
				n = 0
				break
			}
			if damerauWithinOne(w, c) {
				match = c
				n++
			}
		}
		if n == 1 {
			words[i] = match
			changed = true
		}
	}
	if !changed {
		return q
	}
	return strings.Join(words, " ")
}

// homeSearchCommandRows are the "Commands" rows for a query, or nil.
func homeSearchCommandRows(raw string) []gsResult {
	q := strings.TrimSpace(raw)
	if q == "" || utf8.RuneCountInString(q) > homeSearchCommandMaxRunes {
		return nil
	}
	q = homeSearchFixTypos(q)
	if rows := homeSearchTypedVerbs(voiceNormalize(q)); rows != nil {
		return rows
	}
	in := voiceParse(q)
	if row, ok := homeSearchIntentRow(in); ok {
		return []gsResult{row}
	}
	return nil
}

// homeSearchCommand is a command row.
func homeSearchCommand(title, now, icon string, run func(*Config)) gsResult {
	return gsResult{Title: title, Subtitle: now, Icon: icon, Category: homeSearchCommandsCategory, Action: run}
}

// homeSearchRunIntent runs a parsed command as the voice page would, minus
// the page: a done command says what it did in a toast, one that goes
// somewhere goes there.
func homeSearchRunIntent(in voiceIntent) func(*Config) {
	return func(c *Config) {
		out := voiceExecute(c, in, voiceFromTyped)
		if out.Act != nil {
			out.Act(c)
			return
		}
		if len(out.Choices) > 0 {
			// More than one setting matched: Search lists them by name.
			voiceOpenSearchSheet(c, in.Setting)
			return
		}
		if strings.TrimSpace(out.Say) == "" {
			return
		}
		kind := ToastKindSuccess
		if out.Tone == tbxToneWarn || out.Tone == tbxToneBad {
			kind = ToastKindWarning
		}
		ShowToast(out.Say, kind)
	}
}

// homeSearchIntentRow is the row for one parsed command, when it is one.
func homeSearchIntentRow(in voiceIntent) (gsResult, bool) {
	run := homeSearchRunIntent(in)
	pct := func(v int) string { return strconv.Itoa(v) + "%" }
	switch in.Kind {
	case voiceIntentVolumeSet, voiceIntentVolumeStep, voiceIntentMute, voiceIntentUnmute:
		if !volumeControlAvailable() {
			return gsResult{}, false
		}
		now := ""
		if cur, ok := volumeCurrent(); ok {
			now = "Now " + pct(cur)
			if cur == 0 {
				now = "Now muted"
			}
		}
		title := "Set volume to " + pct(in.Value)
		icon := "volume"
		switch in.Kind {
		case voiceIntentVolumeStep:
			title = "Volume up by " + pct(in.Value)
			if in.Value < 0 {
				title = "Volume down by " + pct(-in.Value)
			}
		case voiceIntentMute:
			title, icon = "Mute", "volume-mute"
		case voiceIntentUnmute:
			title = "Unmute"
		}
		return homeSearchCommand(title, now, icon, run), true
	case voiceIntentBrightnessSet, voiceIntentBrightnessStep:
		if !brightnessControlAvailable() {
			return gsResult{}, false
		}
		now := ""
		if cur, ok := brightnessCurrent(); ok {
			now = "Now " + pct(cur)
		}
		title := "Set brightness to " + pct(in.Value)
		if in.Kind == voiceIntentBrightnessStep {
			title = "Brightness up by " + pct(in.Value)
			if in.Value < 0 {
				title = "Brightness down by " + pct(-in.Value)
			}
		}
		return homeSearchCommand(title, now, "sun", run), true
	case voiceIntentScreenshot:
		return homeSearchCommand("Take a screenshot", "Of the screen behind this search", "camera", run), true
	case voiceIntentSleepScreen:
		// Typed "sleep" is the handheld's sleep (homeSearchTypedVerbs);
		// "screen off" and "lock" stay the screensaver.
		return homeSearchCommand("Turn the screen off", "The screensaver; any button wakes it", "moon", run), true
	case voiceIntentQuickSettings:
		return homeSearchCommand("Open Quick Settings", "Wi-Fi, Bluetooth, brightness and more", "grid", run), true
	case voiceIntentWifi, voiceIntentBluetooth, voiceIntentAirplane:
		return homeSearchRadioRow(in, run)
	case voiceIntentMusicPause, voiceIntentMusicResume, voiceIntentMusicNext, voiceIntentMusicPrev, voiceIntentMusicPlay:
		st := voiceMusicNow()
		now := "Nothing playing"
		if st.hasTrack {
			now = setOr(strings.TrimSpace(st.title), "A song")
			if st.paused || !st.playing {
				now += " · paused"
			}
		}
		title, icon := "Play music", "music"
		switch in.Kind {
		case voiceIntentMusicPause:
			title, icon = "Pause the music", "pause"
		case voiceIntentMusicResume:
			title, icon = "Resume the music", "play"
		case voiceIntentMusicNext:
			title, icon = "Next song", "skip-forward"
		case voiceIntentMusicPrev:
			title, icon = "Previous song", "skip-back"
		}
		return homeSearchCommand(title, now, icon, run), true
	case voiceIntentBattery:
		return homeSearchCommand(voiceBatterySentence(), "Battery", "battery", run), true
	case voiceIntentTime:
		return homeSearchCommand(clockDigits(appConfig, time.Now()), "The time", "clock", run), true
	case voiceIntentCloseAll:
		return homeSearchCommand("Close all apps and games", "Frees memory; JukaHub stays", "broom", run), true
	case voiceIntentSetting:
		return homeSearchSettingRow(in, run)
	}
	return gsResult{}, false
}

// homeSearchRadioRow is "Turn Wi-Fi off · Wi-Fi is on now" for the radio
// intents, read from the radio itself where it answers.
func homeSearchRadioRow(in voiceIntent, run func(*Config)) (gsResult, bool) {
	action, name, icon := qsActionWifi, "Wi-Fi", "wifi"
	switch in.Kind {
	case voiceIntentBluetooth:
		action, name, icon = qsActionBluetooth, "Bluetooth", "bluetooth"
	case voiceIntentAirplane:
		action, name, icon = qsActionAirplane, "Airplane mode", "airplane"
	}
	_, t := voiceQSToggle(action)
	if t == nil {
		return gsResult{}, false
	}
	cur, known := t.Value, true
	switch action {
	case qsActionWifi:
		if v, ok := wifiEnabled(); ok {
			cur = v
		}
	case qsActionBluetooth:
		if v, ok := bluetoothEnabled(); ok {
			cur = v
		}
	}
	now := name + " is " + strings.ToLower(onOff(cur)) + " now"
	if !t.Available {
		now, known = setOr(strings.TrimSpace(t.Detail), "No control on this device"), false
	}
	if !in.On && icon != "airplane" {
		icon += "-off"
	}
	row := homeSearchCommand("Turn "+name+" "+strings.ToLower(onOff(in.On)), now, icon, run)
	if !known {
		row.Dimmed, row.Blocked = true, name+" cannot be switched here"
	}
	return row, true
}

// homeSearchSettingRow is a settings sentence ("dark mode on") as a row with
// the setting's value now, when it names one setting clearly enough to act.
func homeSearchSettingRow(in voiceIntent, run func(*Config)) (gsResult, bool) {
	c := appConfig
	if c == nil {
		return gsResult{}, false
	}
	hits := voiceSettingRank(c, voiceSettingCatalog(), in.Setting, in.SetOp)
	best, _ := voiceSettingDecide(hits, in.SetOp)
	if best == nil || !voiceSettingSure(in, *best) {
		return gsResult{}, false
	}
	cand := best.Cand
	row := cand.Row
	if row.Visible != nil && !row.Visible(c) {
		return gsResult{}, false
	}
	now := homeSearchSettingValue(c, cand)
	title := cand.Name
	switch {
	case in.SetOp.reads():
		title = cand.Name + ": " + setOr(now, "?")
		now = "Settings · " + cand.Where
	case row.Kind == setKindToggle && (in.SetOp == voiceSetOpOn || in.SetOp == voiceSetOpUp || in.SetOp == voiceSetOpMax):
		title = "Turn " + cand.Name + " on"
	case row.Kind == setKindToggle && (in.SetOp == voiceSetOpOff || in.SetOp == voiceSetOpDown || in.SetOp == voiceSetOpMin):
		title = "Turn " + cand.Name + " off"
	case strings.TrimSpace(in.SetTo) != "":
		title = cand.Name + ": " + strings.TrimSpace(in.SetTo)
	default:
		title = "Change " + cand.Name
	}
	if now != "" && !in.SetOp.reads() {
		now = "Now " + now
	}
	return homeSearchCommand(title, now, setOr(cand.Glyph, "settings"), run), true
}

// homeSearchSettingValue is a settings row's value as the hub shows it.
func homeSearchSettingValue(c *Config, cand voiceSettingCand) string {
	row := cand.Row
	switch row.Kind {
	case setKindToggle:
		if row.Get != nil {
			return setOnOff(row.Get(c) != cand.Invert)
		}
	case setKindChoice:
		if row.Options != nil && row.Current != nil {
			opts := row.Options(c)
			if i := row.Current(c); i >= 0 && i < len(opts) {
				return opts[i].Label
			}
		}
	}
	if row.Value != nil {
		return strings.TrimSpace(row.Value(c))
	}
	return ""
}

// homeSearchTypedVerbs are the commands only search has: the whole
// handheld's sleep, an update, a new note. s is voiceNormalize's text.
func homeSearchTypedVerbs(s string) []gsResult {
	switch s {
	case "sleep", "suspend", "go to sleep", "sleep now", "put to sleep", "standby":
		if !powerSleepSupported() {
			return nil
		}
		return []gsResult{
			homeSearchCommand("Sleep", "The handheld sleeps; the power key wakes it", "moon", func(*Config) { sleepNow("search") }),
			homeSearchCommand("Turn the screen off", "The screensaver; any button wakes it", "sun-low", func(*Config) { activateScreensaver() }),
		}
	case "update", "updates", "update jukahub", "check for updates", "check for update", "install update",
		"jukahub update", "upgrade", "new version":
		return []gsResult{homeSearchUpdateRow()}
	case "new note", "note", "add note", "add a note", "create note", "create a note", "make a note", "write a note",
		"take a note", "notes new":
		if appConfig == nil || sceneIndexExact(appConfig, "Notes") < 0 {
			return nil
		}
		return []gsResult{homeSearchCommand("New note", "Opens Notes on an empty note", "edit", func(c *Config) {
			if idx := sceneIndexExact(c, "Notes"); idx >= 0 {
				if idx != currentSceneIndex {
					changeSceneTo(c, idx)
				}
				notesNewNote()
			}
		})}
	}
	return nil
}

// homeSearchUpdateRow is "Install JukaHub …" when a release is waiting, else
// "Check for updates".
func homeSearchUpdateRow() gsResult {
	if updateAvailable() {
		return homeSearchCommand("Install JukaHub "+availableUpdateLabel(), "Downloads it, then JukaHub restarts",
			"update", func(*Config) { startHeaderUpdate() })
	}
	row := homeSearchCommand("Check for updates", "You have "+versionString(appConfig), "update", func(c *Config) {
		ShowToast("Looking for a newer JukaHub…", ToastKindInfo)
		go func() {
			err := checkForUpdateOnce(c)
			tbxPost(func() {
				switch {
				case err != nil:
					ShowToast("Could not check for updates — "+plainNetworkError(err), ToastKindWarning)
				case updateAvailable():
					openUpdatePrompt()
				default:
					ShowToast("JukaHub is up to date", ToastKindSuccess)
				}
			})
		}()
	})
	if !IsOnline() {
		row.Dimmed, row.Blocked, row.Badge = true, "Checking for updates needs internet", homeSearchNeedsInternet
	}
	return row
}
