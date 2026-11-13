package main

// voicecmd_intent.go — what a spoken (or typed) command asks for.
//
// The voice page (voicecmd.go) hands this file one line of text: Whisper's
// transcript of the microphone, what the phone's keyboard dictated on the
// /voice page, or what was typed. voiceParse turns it into one intent: a
// system action (volume, brightness, a screenshot, Wi-Fi, the music), a
// question (the time, the battery) or "open this" with the name that was said.
// Names are resolved against the Home search index afterwards
// (voicecmd_resolve.go); nothing here knows what is installed.
//
// Speech arrives messy, so the text is folded first: case, Whisper's trailing
// full stop and commas, curly apostrophes, contractions ("what's"), a wake word
// ("hey Juka"), politeness ("can you please … for me") and numbers spoken as
// words ("seventy five" → 75). The grammar is a short list of phrase shapes,
// English only; anything it does not recognise is treated as a name, because
// "YouTube" on its own is the most common command of all.
//
// Pure: no SDL, no config, no globals that change.

import (
	"strconv"
	"strings"
	"unicode"
)

// voiceIntentKind is what a command asks for.
type voiceIntentKind int

const (
	voiceIntentNone voiceIntentKind = iota
	voiceIntentOpen
	voiceIntentSearch
	voiceIntentWebSearch
	voiceIntentVolumeSet
	voiceIntentVolumeStep
	voiceIntentMute
	voiceIntentUnmute
	voiceIntentBrightnessSet
	voiceIntentBrightnessStep
	voiceIntentScreenshot
	voiceIntentSleepScreen
	voiceIntentQuickSettings
	voiceIntentWifi
	voiceIntentBluetooth
	voiceIntentAirplane
	voiceIntentHome
	voiceIntentBack
	voiceIntentMusicPause
	voiceIntentMusicResume
	voiceIntentMusicNext
	voiceIntentMusicPrev
	voiceIntentMusicPlay
	voiceIntentTime
	voiceIntentDate
	voiceIntentBattery
	voiceIntentQuit
	voiceIntentHelp
	voiceIntentCancel
	// voiceIntentSetting reads or changes a Settings row (voicesettings.go).
	voiceIntentSetting
)

// voiceStepDefault is how far "louder" or "brighter" moves a level, in percent.
const voiceStepDefault = 10

// voiceMaxCommandRunes bounds one command. Nothing anyone says to a launcher
// is longer; a longer text is a paste, not a command.
const voiceMaxCommandRunes = 200

// voiceIntent is one parsed command.
type voiceIntent struct {
	Kind voiceIntentKind
	// Target is the name to open, folded (lower case, single spaces).
	Target string
	// Verb is the word that introduced the target ("open", "play"; "" when
	// the name was said on its own). "play" prefers games and music.
	Verb string
	// Query is what to search for, as said (cleaned, not folded).
	Query string
	// Value is the level for a Set (0..100) or the signed step for a Step.
	Value int
	// On is the wanted state for Wi-Fi, Bluetooth and airplane mode.
	On bool
	// Heard is the folded command the intent was read from.
	Heard string
	// Setting, SetOp and SetTo are a settings sentence: the setting as said,
	// what to do with it and the value said for "set X to Y". Target and Verb
	// then hold what "open" would have made of the sentence, for when it names
	// no setting.
	Setting string
	SetOp   voiceSettingOp
	SetTo   string
}

// ── Folding ───────────────────────────────────────────────────────────────

// voiceContractions are expanded before anything else, so "what's the time"
// and "what is the time" are one phrase.
var voiceContractions = [...][2]string{
	{"what's", "what is"}, {"whats", "what is"}, {"it's", "it is"}, {"that's", "that is"},
	{"there's", "there is"}, {"i'd", "i would"}, {"i'm", "i am"}, {"let's", "let us"},
	{"don't", "do not"}, {"can't", "cannot"}, {"won't", "will not"}, {"i'll", "i will"},
	{"you're", "you are"}, {"how's", "how is"}, {"where's", "where is"},
}

// voiceFold lower-cases a command, turns punctuation into spaces (keeping a
// "%" as the word "percent" and a decimal point inside a number), expands the
// contractions, joins the apostrophe of a possessive ("kirby's" → "kirbys",
// which still meets "Kirby's" in the search index on its letters) and
// collapses spaces.
func voiceFold(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("’", "'", "‘", "'", "`", "'", "´", "'", "“", " ", "”", " ").Replace(s)
	words := strings.Fields(s)
	for i, w := range words {
		w = strings.Trim(w, ".,!?;:\"()[]{}…")
		for _, c := range voiceContractions {
			if w == c[0] {
				w = c[1]
				break
			}
		}
		words[i] = w
	}
	s = strings.Join(words, " ")
	var b strings.Builder
	b.Grow(len(s) + 8)
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '%':
			b.WriteString(" percent ")
		case r == '\'':
			// A possessive or a stray apostrophe: join the letters around it.
		case r == '.' && i > 0 && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]):
			b.WriteRune(r)
		case r == '+' || r == '&':
			// "Date & time" and "N+": spoken as "and", searched as a space.
			b.WriteString(" ")
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// voiceWakeWords open a command and carry no meaning. "juka land" is a game,
// so a bare "juka" is only dropped when it is not the start of a name.
var voiceWakeWords = [...]string{"hey", "hi", "hello", "ok", "okay", "yo"}

// voiceSoundAlikes fix the names speech recognition hears wrong most often:
// JukaHub's own name first. Word for word, applied after folding.
var voiceSoundAlikes = map[string]string{
	"juca": "juka", "yuka": "juka", "jukka": "juka", "jukas": "juka", "jucca": "juka", "yucca": "juka",
	"jukahubs": "jukahub", "jucahub": "jukahub", "yukahub": "jukahub",
	"wifis": "wifi", "whyfi": "wifi", "wify": "wifi",
}

// voicePhraseAlikes are multi-word mishearings, replaced as whole phrases.
var voicePhraseAlikes = [...][2]string{
	{"why fi", "wifi"}, {"y fi", "wifi"}, {"wi fi", "wifi"}, {"wy fi", "wifi"},
	{"blue tooth", "bluetooth"}, {"you tube", "youtube"}, {"u tube", "youtube"},
	{"juka hub", "jukahub"}, {"juka land", "jukaland"}, {"retro arch", "retroarch"},
	{"play station", "playstation"}, {"screen shot", "screenshot"}, {"screen shots", "screenshots"},
	{"quick setting", "quick settings"}, {"un mute", "unmute"}, {"a hundred", "one hundred"},
	{"per cent", "percent"},
}

// voicePolitePrefixes and voicePoliteSuffixes are peeled off repeatedly, so
// "hey juka can you please open youtube for me" leaves "open youtube".
var (
	voicePolitePrefixes = [...]string{
		"please", "can you", "could you", "would you", "will you", "can i", "could i",
		"i want to", "i wanna", "i would like to", "i need to", "i would like", "let me", "let us",
		"lets", "go ahead and", "just", "now", "quickly", "kindly", "and",
		"um", "uh", "uhm", "umm", "er", "erm", "hmm", "so", "well", "alright", "all right", "okay so",
	}
	voicePoliteSuffixes = [...]string{
		"please", "for me", "now", "right now", "thanks", "thank you", "if you can", "if you could",
	}
)

// voiceNormalize is the folded command with the wake word, the politeness and
// the known mishearings dealt with: the text the grammar reads.
func voiceNormalize(raw string) string {
	s := " " + voiceFold(raw) + " "
	for _, p := range voicePhraseAlikes {
		s = strings.ReplaceAll(s, " "+p[0]+" ", " "+p[1]+" ")
	}
	words := strings.Fields(s)
	for i, w := range words {
		if fix, ok := voiceSoundAlikes[w]; ok {
			words[i] = fix
		}
	}
	// The wake word: "hey", "ok juka", "juka," — but not "jukaland" or a
	// "juka land" that the phrase list already joined.
	for len(words) > 0 {
		dropped := false
		for _, w := range voiceWakeWords {
			if words[0] == w {
				words, dropped = words[1:], true
				break
			}
		}
		if len(words) > 1 && (words[0] == "juka" || words[0] == "jukahub") {
			words, dropped = words[1:], true
		}
		if !dropped {
			break
		}
	}
	// The wake word and nothing after it ("Hey Juka." — auto-stop ends the clip
	// in the pause that follows it) is no command at all, not a name to look
	// up: as a name it listed JukaLand, JukaHub's pages and Quit.
	if len(words) == 1 && (words[0] == "juka" || words[0] == "jukahub") {
		return ""
	}
	s = strings.Join(words, " ")
	for changed := true; changed; {
		changed = false
		for _, p := range voicePolitePrefixes {
			if rest, ok := voiceCutWords(s, p); ok && rest != "" {
				s, changed = rest, true
			}
		}
		for _, p := range voicePoliteSuffixes {
			if rest, ok := voiceCutSuffixWords(s, p); ok && rest != "" {
				s, changed = rest, true
			}
		}
	}
	return s
}

// voiceCutWords removes prefix from s when s starts with it as whole words.
func voiceCutWords(s, prefix string) (string, bool) {
	if s == prefix {
		return "", true
	}
	if strings.HasPrefix(s, prefix+" ") {
		return strings.TrimSpace(s[len(prefix)+1:]), true
	}
	return s, false
}

// voiceCutSuffixWords removes suffix from s when s ends with it as whole words.
func voiceCutSuffixWords(s, suffix string) (string, bool) {
	if s == suffix {
		return "", true
	}
	if strings.HasSuffix(s, " "+suffix) {
		return strings.TrimSpace(s[:len(s)-len(suffix)-1]), true
	}
	return s, false
}

// ── Numbers ───────────────────────────────────────────────────────────────

var voiceNumberWords = map[string]int{
	"zero": 0, "nil": 0, "none": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17,
	"eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fourty": 40,
	"fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}

// voiceFindNumber finds the first number in words: digits ("50", "7.5") or
// words up to one hundred ("seventy five", "one hundred"). It returns the
// value rounded to a whole number and whether one was found.
func voiceFindNumber(words []string) (int, bool) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "" {
			continue
		}
		if unicode.IsDigit([]rune(w)[0]) {
			digits := strings.TrimSuffix(strings.TrimSuffix(w, "percent"), "%")
			if f, err := strconv.ParseFloat(digits, 64); err == nil {
				// Bounded before the conversion: a typed "1e300" or twenty
				// nines overflows int differently on amd64 and arm64. Every
				// caller clamps to 0..100 anyway.
				if f > 1000 {
					f = 1000
				}
				return int(f + 0.5), true
			}
			continue
		}
		if w == "hundred" {
			return 100, true
		}
		v, ok := voiceNumberWords[w]
		if !ok {
			continue
		}
		if i+1 < len(words) && words[i+1] == "hundred" && v > 0 && v < 10 {
			return v * 100, true
		}
		if v >= 20 && v%10 == 0 && i+1 < len(words) {
			if u, ok := voiceNumberWords[words[i+1]]; ok && u > 0 && u < 10 {
				return v + u, true
			}
		}
		return v, true
	}
	return 0, false
}

// ── Grammar ───────────────────────────────────────────────────────────────

// voicePhrases is a set of whole commands.
type voicePhrases map[string]bool

func voiceSet(list ...string) voicePhrases {
	m := make(voicePhrases, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

var (
	// voiceNoisePhrases are what speech recognition writes for a breath, a
	// click or a near-silent clip — Whisper's classic answers to noise — and
	// the fillers that stand alone. Nothing was said: they open nothing.
	voiceNoisePhrases = voiceSet("you", "thank you", "thanks", "thank you very much", "thank you so much",
		"thanks for watching", "thank you for watching", "thanks for listening", "thank you for listening",
		"please subscribe", "bye", "bye bye", "goodbye", "the end", "uh", "um", "uhm", "umm", "hmm", "hm",
		"er", "erm", "ah", "oh", "so", "and", "the", "a", "i", "it", "yes", "yeah")
	voiceCancelPhrases = voiceSet("cancel", "never mind", "nevermind", "stop listening", "forget it",
		"nothing", "no", "no thanks", "not now", "please")
	voiceHelpPhrases = voiceSet("help", "what can i say", "what can you do", "what can i do", "commands",
		"show commands", "how does this work", "what do i say", "list commands")
	voiceTimePhrases = voiceSet("time", "what time is it", "what is the time", "tell me the time",
		"what time", "the time", "current time", "what is the time now", "what time is it now",
		"what is the current time", "do you have the time", "clock")
	voiceDatePhrases = voiceSet("date", "what is the date", "what day is it", "what is today",
		"todays date", "what date is it", "what is the date today", "what is todays date", "the date",
		"what day is today", "today")
	voiceScreenOffPhrases = voiceSet("sleep", "go to sleep", "sleep screen", "screen off", "turn off the screen",
		"turn the screen off", "turn screen off", "lock", "lock the screen", "lock screen", "screensaver",
		"start the screensaver", "screen saver", "turn off screen", "switch off the screen", "sleep mode")
	voiceQSPhrases = voiceSet("quick settings", "control center", "control centre", "quick panel",
		"open quick settings", "show quick settings", "open control center", "open control centre")
	voiceHomePhrases = voiceSet("home", "go home", "go to home", "go to the home screen", "home screen",
		"main menu", "main screen", "go to main menu", "go to the main menu", "show home", "open home",
		"take me home", "back to home", "go back home", "back home", "the home screen", "go to home screen")
	voiceBackPhrases = voiceSet("back", "go back", "previous", "previous screen", "go to the previous screen",
		"return", "close", "close this", "exit this", "leave", "close it", "go back one")
	voicePausePhrases = voiceSet("pause", "pause music", "pause the music", "pause it", "pause song",
		"pause the song", "stop", "stop music", "stop the music", "stop playing", "stop the song", "pause playback",
		"stop the playback")
	voiceResumePhrases = voiceSet("resume", "resume music", "resume the music", "continue", "continue playing",
		"unpause", "play", "keep playing", "resume playing", "resume playback", "continue the music")
	voiceNextPhrases = voiceSet("next", "next song", "next track", "skip", "skip song", "skip this song",
		"skip track", "skip this", "play next", "play the next song", "next one", "skip it", "skip the song",
		"play next song", "play next track", "play the next track", "skip to the next song", "skip to next song",
		"skip to the next track", "go to the next song", "go to next song")
	voicePrevPhrases = voiceSet("previous song", "previous track", "last song", "go back a song",
		"play the previous song", "previous one", "back a song", "play the last song", "last track",
		"play previous song", "play previous track", "play the previous track", "go to the previous song",
		"skip back", "go back to the previous song")
	voiceMusicPhrases = voiceSet("play music", "play some music", "music please", "start music", "play my music",
		"play songs", "play a song", "start the music", "play something", "put on some music", "put on music")
	voiceMutePhrases = voiceSet("mute", "mute it", "mute the sound", "mute sound", "mute audio", "mute volume",
		"mute the volume", "silence", "be quiet", "shh", "shush", "sound off", "turn off the sound",
		"turn the sound off", "turn sound off", "mute the audio", "volume off", "turn off the volume",
		"turn the volume off", "turn volume off", "volume mute", "switch off the sound", "switch the sound off")
	voiceUnmutePhrases = voiceSet("unmute", "unmute it", "sound on", "turn on the sound", "turn the sound on",
		"turn sound on", "unmute the sound", "unmute audio", "unmute the volume", "unmute volume", "volume on",
		"turn on the volume", "turn the volume on", "turn volume on", "volume back on", "sound back on",
		"switch on the sound", "switch the sound on", "turn the volume back on", "turn the sound back on")
	voiceQuitPhrases = voiceSet("quit", "exit", "quit jukahub", "exit jukahub", "close jukahub", "quit the app",
		"exit the app", "close the app", "shut down", "shutdown", "power off", "turn off", "shut down jukahub",
		"turn off jukahub", "quit juka", "exit juka", "close juka")
	voiceBatteryPhrases = voiceSet("battery", "battery level", "how much battery", "how much battery is left",
		"how much battery do i have", "how much battery left", "what is my battery", "battery status",
		"how is my battery", "check battery", "check the battery", "battery percentage", "battery life",
		"show battery", "what is the battery", "what is the battery level", "battery left", "battery percent")
)

// voiceOpenVerbs introduce a name. Longest first, so "go to" wins over "go".
var voiceOpenVerbs = [...]string{
	"navigate to", "take me to", "bring up", "switch to", "show me", "let me play", "i want to play",
	"connect to", "go to", "goto", "open up", "open", "launch", "start", "run", "show", "load", "play",
}

// voiceTargetFillers are dropped from either end of a name: "open the
// settings app" → "settings".
var (
	voiceTargetLeading  = [...]string{"the", "my", "a", "an", "up", "me"}
	voiceTargetTrailing = [...]string{"app", "application", "screen", "page", "game", "please", "menu", "panel"}
)

// voiceUpWords and voiceDownWords decide a step's direction.
var (
	voiceUpWords   = voiceSet("up", "raise", "increase", "louder", "higher", "more", "brighter", "brighten", "turn up", "boost")
	voiceDownWords = voiceSet("down", "lower", "decrease", "quieter", "softer", "less", "dimmer", "darker", "dim", "reduce")
	voiceMaxWords  = voiceSet("max", "maximum", "full", "all the way up", "highest", "loudest", "brightest", "100 percent")
	voiceMinWords  = voiceSet("min", "minimum", "lowest", "all the way down", "zero", "darkest")
)

// voiceParse reads one command.
func voiceParse(raw string) voiceIntent {
	s := voiceNormalize(raw)
	in := voiceIntent{Heard: s}
	if s == "" {
		return in
	}
	words := strings.Fields(s)
	set := func(k voiceIntentKind) voiceIntent { in.Kind = k; return in }
	switch {
	case voiceNoisePhrases[s]:
		return in
	case voiceCancelPhrases[s]:
		return set(voiceIntentCancel)
	case voiceHelpPhrases[s]:
		return set(voiceIntentHelp)
	case voiceTimePhrases[s]:
		return set(voiceIntentTime)
	case voiceDatePhrases[s]:
		return set(voiceIntentDate)
	case voiceBatteryPhrases[s]:
		return set(voiceIntentBattery)
	case voiceQuitPhrases[s]:
		return set(voiceIntentQuit)
	case voiceScreenOffPhrases[s]:
		return set(voiceIntentSleepScreen)
	case voiceQSPhrases[s]:
		return set(voiceIntentQuickSettings)
	case voiceHomePhrases[s]:
		return set(voiceIntentHome)
	case voiceBackPhrases[s]:
		return set(voiceIntentBack)
	case voiceMutePhrases[s]:
		return set(voiceIntentMute)
	case voiceUnmutePhrases[s]:
		return set(voiceIntentUnmute)
	case voiceMusicPhrases[s]:
		return set(voiceIntentMusicPlay)
	case voicePausePhrases[s]:
		return set(voiceIntentMusicPause)
	case voiceResumePhrases[s]:
		return set(voiceIntentMusicResume)
	case voiceNextPhrases[s]:
		return set(voiceIntentMusicNext)
	case voicePrevPhrases[s]:
		return set(voiceIntentMusicPrev)
	}
	// A sentence about a setting (voicesettings.go): "turn off hold menu
	// screenshot" is that switch, not a capture.
	ask, isSetting := voiceSettingParse(s)
	if voiceHasWord(words, "screenshot") || strings.Contains(s, "capture the screen") ||
		strings.Contains(s, "screen capture") || strings.Contains(s, "print screen") {
		// "open screenshots" is the folder, not a new capture.
		if !(voiceHasWord(words, "screenshots") && voiceStartsWithVerb(s)) && !(isSetting && ask.op != voiceSetOpValue) {
			return set(voiceIntentScreenshot)
		}
	}
	// A question about a setting ("is Wi-Fi on?") comes before the radios,
	// which would read its "on" as a command.
	if isSetting && ask.op.reads() {
		return voiceSettingIntentFor(in, s, ask)
	}
	if k, ok := voiceRadioIntent(words, s); ok {
		in.Kind = k.kind
		in.On = k.on
		return in
	}
	if lvl, ok := voiceLevelIntent(words, s); ok {
		in.Kind, in.Value = lvl.kind, lvl.value
		return in
	}
	if q, ok := voiceWebSearchQuery(s); ok {
		in.Kind, in.Query = voiceIntentWebSearch, q
		return in
	}
	if q, ok := voiceLocalSearchQuery(s); ok {
		in.Kind, in.Query = voiceIntentSearch, q
		return in
	}
	if isSetting {
		if kind, step, ok := voiceSettingAsCommand(ask); ok {
			// "turn the music down" is the volume, "music off" the player.
			in.Kind, in.Value = kind, step
			return in
		}
		return voiceSettingIntentFor(in, s, ask)
	}
	verb, rest := voiceSplitVerb(s)
	target := voiceTrimTarget(rest)
	if target == "" {
		// "open" on its own, or "open the": nothing to open.
		return in
	}
	if voiceHomeTargets[target] && verb != "play" {
		// "open the home screen", "show me the main menu": Home itself, not a
		// name to look up (that listed Smart Home and Edit Home instead).
		return set(voiceIntentHome)
	}
	in.Kind, in.Verb, in.Target = voiceIntentOpen, verb, target
	return in
}

// voiceSettingIntentFor is a settings intent, with what "open" would have made
// of the same sentence kept for when it names no setting.
func voiceSettingIntentFor(in voiceIntent, s string, ask voiceSettingAsk) voiceIntent {
	in.Kind = voiceIntentSetting
	in.Setting, in.SetOp, in.SetTo = ask.name, ask.op, ask.value
	in.Verb, in.Target = voiceSplitVerb(s)
	in.Target = voiceTrimTarget(in.Target)
	return in
}

// voiceHomeTargets are the names Home goes by once the verb and the "screen" /
// "menu" words are gone.
var voiceHomeTargets = voiceSet("home", "main")

// voiceHasWord reports whether w is one of words.
func voiceHasWord(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

// voiceStartsWithVerb reports whether s opens with one of the open verbs.
func voiceStartsWithVerb(s string) bool {
	verb, _ := voiceSplitVerb(s)
	return verb != ""
}

// voiceSplitVerb splits "open the settings app" into "open" and "the settings
// app". A command without a verb returns "" and the whole text.
func voiceSplitVerb(s string) (string, string) {
	for _, v := range voiceOpenVerbs {
		if rest, ok := voiceCutWords(s, v); ok {
			return v, rest
		}
	}
	return "", s
}

// voiceTrimTarget drops the articles and the "app"/"screen" words around a
// name, as long as something is left.
func voiceTrimTarget(s string) string {
	for changed := true; changed; {
		changed = false
		for _, w := range voiceTargetLeading {
			if rest, ok := voiceCutWords(s, w); ok && rest != "" {
				s, changed = rest, true
			}
		}
		for _, w := range voiceTargetTrailing {
			if rest, ok := voiceCutSuffixWords(s, w); ok && rest != "" {
				s, changed = rest, true
			}
		}
	}
	return strings.TrimSpace(s)
}

// voiceRadio is a Wi-Fi, Bluetooth or airplane-mode on/off request.
type voiceRadio struct {
	kind voiceIntentKind
	on   bool
}

// voiceRadioIntent reads "turn on wifi", "wifi off", "disable bluetooth",
// "turn on airplane mode". A radio named without on/off is a screen to open,
// not an intent here.
func voiceRadioIntent(words []string, s string) (voiceRadio, bool) {
	kind := voiceIntentNone
	switch {
	case voiceHasWord(words, "airplane") || voiceHasWord(words, "aeroplane") || voiceHasPhrase(s, "flight mode"):
		// Before Wi-Fi: "airplane mode off" is about the mode, whatever else
		// the sentence names.
		kind = voiceIntentAirplane
	case voiceHasWord(words, "wifi") || voiceHasWord(words, "wlan") || voiceHasWord(words, "wireless"):
		kind = voiceIntentWifi
	case voiceHasWord(words, "bluetooth"):
		kind = voiceIntentBluetooth
	default:
		return voiceRadio{}, false
	}
	// "switch on" and "turn off" land on their "on" / "off"; "connect to
	// wifi" is the Wi-Fi screen (a network to pick), not the radio.
	on, off := false, false
	for _, w := range words {
		switch w {
		case "enable", "activate", "on", "start":
			on = true
		case "disable", "deactivate", "disconnect", "off", "stop":
			off = true
		}
	}
	if on == off {
		return voiceRadio{}, false
	}
	return voiceRadio{kind: kind, on: on}, true
}

// voiceLevel is a volume or brightness request.
type voiceLevel struct {
	kind  voiceIntentKind
	value int
}

// voiceLevelIntent reads the volume and brightness phrases: a level ("volume
// 50", "set the brightness to seventy percent", "max volume", "half
// brightness") or a step ("louder", "turn the volume down by 20", "dimmer").
func voiceLevelIntent(words []string, s string) (voiceLevel, bool) {
	volume := voiceHasWord(words, "volume") || voiceHasWord(words, "sound") || voiceHasWord(words, "louder") ||
		voiceHasWord(words, "quieter") || voiceHasWord(words, "softer") || s == "turn it up" || s == "turn it down"
	bright := voiceHasWord(words, "brightness") || voiceHasWord(words, "brighter") || voiceHasWord(words, "dimmer") ||
		voiceHasWord(words, "darker") || voiceHasWord(words, "brighten") ||
		(voiceHasWord(words, "screen") && (voiceHasWord(words, "dim") || voiceHasWord(words, "bright")))
	if !volume && !bright {
		return voiceLevel{}, false
	}
	// A volume phrase that names a screen ("open volume settings") is a name;
	// one about the menus' music or clicks is that setting (voicesettings.go).
	if voiceHasWord(words, "settings") || voiceHasWord(words, "setting") || voiceHasWord(words, "background") ||
		voiceHasWord(words, "bgm") || voiceHasWord(words, "interface") || voiceHasWord(words, "click") ||
		voiceHasWord(words, "clicks") || voiceHasWord(words, "vibration") || voiceHasWord(words, "lights") {
		return voiceLevel{}, false
	}
	setKind, stepKind := voiceIntentVolumeSet, voiceIntentVolumeStep
	if bright && !volume {
		setKind, stepKind = voiceIntentBrightnessSet, voiceIntentBrightnessStep
	}
	up, down := false, false
	for i, w := range words {
		if voiceUpWords[w] {
			up = true
		}
		if voiceDownWords[w] {
			down = true
		}
		if i+1 < len(words) {
			pair := w + " " + words[i+1]
			if voiceUpWords[pair] {
				up = true
			}
		}
	}
	for phrase := range voiceMaxWords {
		if voiceHasPhrase(s, phrase) {
			return voiceLevel{kind: setKind, value: 100}, true
		}
	}
	for phrase := range voiceMinWords {
		if voiceHasPhrase(s, phrase) {
			return voiceLevel{kind: setKind, value: 0}, true
		}
	}
	if voiceHasWord(words, "half") {
		return voiceLevel{kind: setKind, value: 50}, true
	}
	n, hasNumber := voiceFindNumber(words)
	if hasNumber {
		if n > 100 {
			n = 100
		}
		if n < 0 {
			n = 0
		}
	}
	switch {
	case hasNumber && (voiceHasWord(words, "by") || ((up || down) && !voiceHasWord(words, "to") && !voiceHasWord(words, "set") && !voiceHasWord(words, "at"))):
		// "volume up by 20", "turn it down 10": a step of that size.
		if down && !up {
			return voiceLevel{kind: stepKind, value: -n}, true
		}
		return voiceLevel{kind: stepKind, value: n}, true
	case hasNumber:
		return voiceLevel{kind: setKind, value: n}, true
	case up && !down:
		return voiceLevel{kind: stepKind, value: voiceStepDefault}, true
	case down && !up:
		return voiceLevel{kind: stepKind, value: -voiceStepDefault}, true
	}
	// "volume" on its own says nothing about the level.
	return voiceLevel{}, false
}

// voiceHasPhrase reports whether phrase appears in s as whole words.
func voiceHasPhrase(s, phrase string) bool {
	return strings.Contains(" "+s+" ", " "+phrase+" ")
}

// voiceWebSearchQuery reads "search the web for cats", "google cats", "look
// up cats online". The words searched for keep their spelling as folded.
func voiceWebSearchQuery(s string) (string, bool) {
	for _, p := range [...]string{
		"search the web for", "search the internet for", "search online for", "search google for",
		"search the web", "search the internet", "search online", "search google", "google search for",
		"google search", "google", "look up online", "web search for", "web search", "search youtube for",
	} {
		if rest, ok := voiceCutWords(s, p); ok && rest != "" {
			if p == "search youtube for" {
				return "youtube " + rest, true
			}
			return rest, true
		}
	}
	for _, suffix := range [...]string{"online", "on the web", "on the internet", "on google"} {
		for _, p := range [...]string{"search for", "search", "look up", "lookup", "find"} {
			if rest, ok := voiceCutWords(s, p); ok {
				if q, ok := voiceCutSuffixWords(rest, suffix); ok && q != "" {
					return q, true
				}
			}
		}
	}
	return "", false
}

// voiceLocalSearchQuery reads "search for mario", "find my notes": a search
// of everything Home search knows, shown as its sheet.
func voiceLocalSearchQuery(s string) (string, bool) {
	for _, p := range [...]string{"search for", "look for", "search", "find", "look up", "lookup", "where is"} {
		if rest, ok := voiceCutWords(s, p); ok && rest != "" {
			return voiceTrimTarget(rest), true
		}
	}
	return "", false
}
