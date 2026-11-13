package main

// voicesettings_ai.go — Juka (Groq) may change a setting too.
//
// The grammar (voicesettings.go) covers the sentences people usually say, and
// works offline. Anything it does not read goes to Juka, which used to be told
// that nothing could be done. Now, when the question asks for a change, the
// system prompt carries a short catalogue of the switches and lists Settings
// has (id, name, values) and one rule: end the reply with [[set id=value]] per
// change. The marker is cut from the streamed text before it is shown or
// spoken (voiceSettingsHideMarks), and when the whole answer is in, on the main
// thread, every marker is checked against the same catalogue and run through
// the same row setters a voice command uses (voiceSettingRun) — the model
// never touches anything itself, and an id it made up changes nothing. What
// could not be done is added to the answer in JukaHub's own words.
//
// The offline model is far too small to follow this; it never gets the
// catalogue, and anything that looks like a marker in its answer is dropped.

import (
	"regexp"
	"strings"
)

// voiceSettingsAIMax bounds the changes one answer may make.
const voiceSettingsAIMax = 3

// voiceSettingsAIOptions bounds the values listed for one list setting; the
// rest are still accepted when asked for by name.
const voiceSettingsAIOptions = 8

// voiceSettingsAIExclude are rows Juka is not offered: switches that cut the
// connection the answer arrives on, or that the grammar already says with a
// cancellable pause (voicecmd_exec.go voiceRadioOutcome).
//
// Nor rows that share data, open the device to the network, start JukaHub at
// login, or could lock the player out of voice or the buttons: those are said
// on purpose, by voice or in Settings, not inferred by a model.
//
// The same rows only change by voice when named in full (voiceSettingSure):
// "switch on" or one word of a longer name never reaches them.
var voiceSettingsAIExclude = voiceSet("airplane", "voiceWifi", "bluetooth", "vpn", "vpnRoute", "diagnostics",
	"liveBuilder", "peerDevices", "nearbyShare", "lanplay", "hoffOffers", "hoffSettings", "cookies",
	"voiceMenuButton", "wakeWord", "swapAB", "swapXY", "layoutPreset", "selectKey", "chordButton", "switchAccess",
	"switchInterval", "autostart", "inputProfile", "inputProfileAuto", "inputProfileHotkey", "uiLanguage",
	gameAssistKey)

// voiceSettingSayInFull reports whether a row is one of those (the remap rows
// of every button included).
func voiceSettingSayInFull(id string) bool {
	return voiceSettingsAIExclude[id] || strings.HasPrefix(id, "button")
}

// voiceSettingsAIWords mark a question that asks for a change; only then is
// the catalogue sent (it costs tokens on every request).
var voiceSettingsAIWords = voiceSet("turn", "switch", "set", "change", "enable", "disable", "increase",
	"decrease", "raise", "lower", "reduce", "toggle", "activate", "deactivate", "setting", "settings",
	"bigger", "smaller", "larger", "louder", "quieter", "brighter", "darker", "dimmer", "theme", "adjust")

// voiceSettingsAIRelevant reports whether a question may ask for a setting
// change. Pure.
func voiceSettingsAIRelevant(question string) bool {
	folded := voiceNormalize(question)
	if _, ok := voiceSettingParse(folded); ok {
		return true
	}
	for _, w := range strings.Fields(folded) {
		if voiceSettingsAIWords[w] {
			return true
		}
	}
	return false
}

// voiceSettingsAIEligible reports whether Juka may change a row: a visible
// switch or list with a setter.
func voiceSettingsAIEligible(c *Config, cand voiceSettingCand) bool {
	row := cand.Row
	if row.ID == "" || voiceSettingSayInFull(row.ID) {
		return false
	}
	switch row.Kind {
	case setKindToggle:
		if row.Get == nil || row.Set == nil {
			return false
		}
	case setKindChoice:
		if row.Options == nil || row.Choose == nil {
			return false
		}
	default:
		return false
	}
	return row.Visible == nil || row.Visible(c)
}

// voiceSettingsAIPrompt is what the system prompt adds for a question that may
// ask for a change: the rule and the catalogue. "" for any other question.
// Main thread (it reads the rows' lists).
func voiceSettingsAIPrompt(c *Config, question string) string {
	if c == nil || !voiceSettingsAIRelevant(question) {
		return ""
	}
	var lines []string
	listed := map[string]bool{}
	for _, cand := range voiceSettingCatalog() {
		if listed[cand.Row.ID] || !voiceSettingsAIEligible(c, cand) {
			continue
		}
		listed[cand.Row.ID] = true
		values := "on/off"
		if cand.Row.Kind == setKindChoice {
			opts := cand.Row.Options(c)
			if len(opts) == 0 {
				continue
			}
			names := make([]string, 0, voiceSettingsAIOptions+1)
			for i, o := range opts {
				if i >= voiceSettingsAIOptions {
					names = append(names, "…")
					break
				}
				label := strings.TrimSpace(o.Label)
				if cut := strings.Index(label, " · "); cut > 0 {
					label = label[:cut]
				}
				names = append(names, setOr(label, o.Value))
			}
			values = strings.Join(names, "|")
		}
		lines = append(lines, cand.Row.ID+": "+cand.Name+" = "+values)
	}
	if len(lines) == 0 {
		return ""
	}
	return " You can change JukaHub's settings. When the user asks for a change, end your reply with one line " +
		"per change, at most 3: [[set ID=VALUE]]. VALUE is on, off or toggle for a switch; for a list, one of its " +
		"values, or up, down, max or min. Use only an ID from the list below and never write the brackets for " +
		"anything else. Say in a few words what you are changing; never say you cannot change settings. If no " +
		"listed setting fits, say so and name the Settings page to look in. Settings (ID: name = values): " +
		strings.Join(lines, "; ") + "."
}

// voiceSettingsMarkRe is one change: [[set ID=VALUE]].
var voiceSettingsMarkRe = regexp.MustCompile(`(?i)\[\[\s*set\s*:?\s*([A-Za-z0-9_.\-]+)\s*=\s*([^\]\n]{1,60})\]\]`)

// voiceSettingsHideMarks removes the markers from text that is shown or
// spoken — a marker still arriving too, from its "[[" on. Pure.
func voiceSettingsHideMarks(text string) string {
	for {
		i := strings.Index(text, "[[")
		if i < 0 {
			break
		}
		j := strings.Index(text[i:], "]]")
		if j < 0 {
			text = text[:i]
			break
		}
		text = text[:i] + text[i+j+2:]
	}
	// The first bracket of a marker that has not finished arriving.
	return strings.TrimSuffix(text, "[")
}

// voiceSettingsAIOp reads a marker's VALUE.
func voiceSettingsAIOp(value string) (voiceSettingOp, string) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "toggle", "switch":
		return voiceSetOpToggle, ""
	case "up", "next", "higher", "more":
		return voiceSetOpUp, ""
	case "down", "previous", "lower", "less":
		return voiceSetOpDown, ""
	case "max", "maximum", "highest":
		return voiceSetOpMax, ""
	case "min", "minimum", "lowest":
		return voiceSetOpMin, ""
	}
	if voiceSettingOnWords[v] {
		return voiceSetOpOn, ""
	}
	if voiceSettingOffWords[v] {
		return voiceSetOpOff, ""
	}
	return voiceSetOpValue, strings.TrimSpace(value)
}

// voiceSettingsAIApply runs the changes an answer asks for and returns the
// answer to show — markers gone, with what could not be done added — and
// whether anything changed. Main thread.
func voiceSettingsAIApply(c *Config, reply string) (string, bool) {
	text := chatStripThink(reply)
	marks := voiceSettingsMarkRe.FindAllStringSubmatch(text, voiceSettingsAIMax)
	if len(marks) == 0 {
		return voiceSettingsHideMarks(reply), false
	}
	clean := strings.TrimSpace(voiceSettingsHideMarks(text))
	byID := map[string]voiceSettingCand{}
	for _, cand := range voiceSettingCatalog() {
		id := strings.ToLower(cand.Row.ID)
		if _, dup := byID[id]; !dup && voiceSettingsAIEligible(c, cand) {
			byID[id] = cand
		}
	}
	var notes []string
	changed := false
	for _, m := range marks {
		cand, ok := byID[strings.ToLower(strings.TrimSpace(m[1]))]
		if !ok {
			logInfo("voice", "Juka asked to change a setting that is not offered: %q", m[1])
			notes = append(notes, "That setting could not be changed by voice")
			continue
		}
		op, value := voiceSettingsAIOp(m[2])
		out := voiceSettingRun(c, cand, op, value)
		logInfo("voice", "Juka changed %s (%s): %s", cand.Row.ID, strings.TrimSpace(m[2]), out.Say)
		switch {
		case out.Act != nil:
			// A switch or list never navigates; anything else is not run.
			notes = append(notes, cand.Name+" was not changed")
		case out.Tone == tbxToneWarn || out.Tone == tbxToneBad:
			notes = append(notes, out.Say)
		default:
			changed = true
			if clean == "" {
				notes = append(notes, out.Say)
			}
		}
	}
	for _, n := range notes {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !strings.HasSuffix(n, ".") && !strings.HasSuffix(n, "!") && !strings.HasSuffix(n, "?") {
			n += "."
		}
		clean = strings.TrimSpace(clean + " " + n)
	}
	if changed {
		trophyEvent(tkVoiceCmd)
	}
	return clean, changed
}

// voiceOrbSettingsReply is the orb's answer with Juka's setting changes run —
// only for a cloud answer still waited for; anything else just loses its
// markers.
func voiceOrbSettingsReply(gen int, reply string, err error, local bool) (string, bool) {
	if err != nil || reply == "" {
		return reply, false
	}
	if local || gen != voiceOrb.gen || voiceOrb.phase != orbThinking || !voiceOrb.ai || !voiceOrbActive() {
		return voiceSettingsHideMarks(reply), false
	}
	return voiceSettingsAIApply(appConfig, reply)
}

// voiceSettingsGameCommand runs what was said over a paused game when it is
// plainly a command for the handheld — the volume, the brightness, a setting
// named outright — rather than a question about the game. Anything that would
// leave the game (opening a page) or needs a choice stays a question.
func voiceSettingsGameCommand(c *Config, question string) (voiceOutcome, bool) {
	in := voiceParse(question)
	switch in.Kind {
	case voiceIntentVolumeSet, voiceIntentVolumeStep, voiceIntentMute, voiceIntentUnmute,
		voiceIntentBrightnessSet, voiceIntentBrightnessStep:
		return voiceExecute(c, in, voiceFromMic), true
	case voiceIntentSetting:
		// "Turn off the music" over a game is the game's music.
		if voiceSettingVolumeNames[in.Setting] {
			return voiceOutcome{}, false
		}
		hits := voiceSettingRank(c, voiceSettingCatalog(), in.Setting, in.SetOp)
		best, _ := voiceSettingDecide(hits, in.SetOp)
		if best == nil || best.Score < voiceSetScoreExact {
			return voiceOutcome{}, false
		}
		// The assistant switched off under its own overlay would drop the
		// pad watcher that is holding the pad for it (gameassist_linux.go).
		if best.Cand.Row.ID == gameAssistKey && !in.SetOp.reads() {
			return voiceOutcome{Say: "Turn the game assistant off in Settings → Voice & AI", Tone: tbxToneWarn}, true
		}
		out := voiceSettingRun(c, best.Cand, in.SetOp, in.SetTo)
		if out.Act != nil || len(out.Choices) > 0 {
			return voiceOutcome{}, false
		}
		return out, true
	}
	return voiceOutcome{}, false
}
