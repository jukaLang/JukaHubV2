package main

// voiceshortcuts.go — the words a user teaches the voice assistant.
//
// The voice grammar (voicecmd_intent.go) knows English phrase shapes and the
// names on this handheld; it cannot know that "lights out" should mean
// "brightness 10" for this user, or that "kart" is the one Mario Kart ROM out
// of four. A tester asked for exactly that: "a menu where you can give it a
// word to listen for and then map that word to a specific thing".
//
// A voice shortcut is a phrase and a reference to what it opens:
//
//   - a screen, a settings row, an app, a game, a file or a Continue item —
//     any row of the voice catalog (voiceTargetsFromApp), the emulator pages
//     and tiles included (voiceemu.go). The reference is the kind, the title
//     and the subtitle, plus the path of a game or a file: the catalog's
//     actions are closures that cannot be saved, and a game is its file, not
//     its place in a list. When the shortcut runs, the reference is looked up
//     in the live catalog again, so it opens what is there now; when that is
//     gone, the answer says so in plain words;
//   - a voice command ("brightness 10", "turn off wifi"), run through the
//     same parser and executor as a spoken one;
//   - a web page, opened in JukaHub's Browser.
//
// Matching runs first in voiceExecute (voicecmd_exec.go), so the voice page,
// the voice orb and the phone's /voice page all know the words, and a taught
// phrase beats the built-in grammar — the user said what it means — except
// "cancel" and "help", which always work. A command matches a phrase as it
// is, after an opening verb ("open", "play", "go to"), or with an article or a
// "menu"/"app" word around it; a near-miss (a letter or two off, the way
// speech recognition slips on a long phrase) counts only when no other phrase
// is that close, and never stands in for a command the grammar understood.
// The phrases also go first in Whisper's vocabulary hint (voiceWhisperPrompt),
// so they come back spelled the way they were taught.
//
// The list is saved in jukauser.json as Custom["voiceShortcuts"], plain JSON
// (voiceShortcut's tags): at most 50, a phrase at most 40 letters. It is read
// back with voiceShortcutsParse, which drops an entry it cannot use rather
// than failing the list — the file is edited by hand as well as by the page
// (voiceshortcuts_ui.go). Main thread only: the list is read from the live
// config each time a command runs, which costs little for 50 entries.

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// voiceShortcutsKey is the Custom key the list is saved under.
const voiceShortcutsKey = "voiceShortcuts"

const (
	// voiceShortcutMax bounds the list, voiceShortcutPhraseMax a phrase (in
	// letters): a shortcut is a word or two, not a sentence.
	voiceShortcutMax       = 50
	voiceShortcutPhraseMax = 40
	voiceShortcutTextMax   = 200
	voiceShortcutPathMax   = 4096
	voiceShortcutURLMax    = 2048
	// voiceShortcutNearMiss is the similarity (voiceSimilarity, space-free) a
	// misheard phrase needs to count: one slip in nine letters.
	voiceShortcutNearMiss = 0.88
	// voiceShortcutPromptMax bounds the phrases' share of Whisper's hint.
	voiceShortcutPromptMax = 250
)

// What a shortcut opens: the catalog's kinds by name, a voice command line and
// a web page.
const (
	voiceShortcutScreen  = "screen"
	voiceShortcutSetting = "setting"
	voiceShortcutApp     = "app"
	voiceShortcutGame    = "game"
	voiceShortcutFile    = "file"
	voiceShortcutMedia   = "media"
	voiceShortcutCommand = "command"
	voiceShortcutWeb     = "web"
)

// voiceShortcut is one taught phrase and what it opens.
type voiceShortcut struct {
	// Phrase is what the user says, as it was taught (shown on the page and
	// sent to Whisper); it matches folded (voiceShortcutFold).
	Phrase string `json:"phrase"`
	Kind   string `json:"kind"`
	// Title and Where name a catalog row by its title and subtitle; System is
	// a game's system and Path a game's or a file's path.
	Title  string `json:"title,omitempty"`
	Where  string `json:"where,omitempty"`
	System string `json:"system,omitempty"`
	Path   string `json:"path,omitempty"`
	// Command is the voice command a command shortcut runs; URL the page a
	// web shortcut opens.
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// voiceShortcutFold is a phrase as the grammar reads a command: folded, with
// the wake word and the politeness gone.
func voiceShortcutFold(phrase string) string { return voiceNormalize(phrase) }

// voiceShortcutKindName is a catalog kind's name in the saved list.
func voiceShortcutKindName(k voiceTargetKind) string {
	switch k {
	case voiceTargetSetting:
		return voiceShortcutSetting
	case voiceTargetApp:
		return voiceShortcutApp
	case voiceTargetGame:
		return voiceShortcutGame
	case voiceTargetFile:
		return voiceShortcutFile
	case voiceTargetMedia:
		return voiceShortcutMedia
	}
	return voiceShortcutScreen
}

// voiceShortcutTargetKind is the catalog kind a saved kind names.
func voiceShortcutTargetKind(kind string) (voiceTargetKind, bool) {
	switch kind {
	case voiceShortcutScreen:
		return voiceTargetScreen, true
	case voiceShortcutSetting:
		return voiceTargetSetting, true
	case voiceShortcutApp:
		return voiceTargetApp, true
	case voiceShortcutGame:
		return voiceTargetGame, true
	case voiceShortcutFile:
		return voiceTargetFile, true
	case voiceShortcutMedia:
		return voiceTargetMedia, true
	}
	return voiceTargetScreen, false
}

// ── Validation and storage ──────────────────────────────────────────────

// voiceShortcutPhraseProblem says why a phrase cannot be taught, or "" when it
// can. Pure.
func voiceShortcutPhraseProblem(phrase string) string {
	phrase = strings.TrimSpace(phrase)
	folded := voiceShortcutFold(phrase)
	switch {
	case folded == "":
		return "That has no words to listen for — try another"
	case utf8.RuneCountInString(phrase) > voiceShortcutPhraseMax:
		return "That is too long — keep it under 40 letters"
	case voiceNoisePhrases[folded]:
		// Whisper writes these for a breath or silence: a shortcut on one
		// would fire on its own.
		return "Speech recognition writes “" + folded + "” for silence — pick another word"
	}
	switch voiceParse(phrase).Kind {
	case voiceIntentCancel:
		return "“" + folded + "” always cancels — pick another word"
	case voiceIntentHelp:
		return "“" + folded + "” always lists the commands — pick another word"
	}
	return ""
}

// voiceShortcutBound trims s, joins its runs of spaces and cuts it to max
// letters.
func voiceShortcutBound(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

// voiceShortcutWebURL checks and completes a web shortcut's address: http or
// https with a host, "https://" added to a bare "example.com".
func voiceShortcutWebURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > voiceShortcutURLMax || strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", false
	}
	return u.String(), true
}

// voiceShortcutHost is a web shortcut's site, for a sentence ("example.com").
func voiceShortcutHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "the page"
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// voiceShortcutClean tidies one shortcut and reports whether it can be used:
// the phrase must be teachable and the kind's own fields present.
func voiceShortcutClean(sc voiceShortcut) (voiceShortcut, bool) {
	sc.Phrase = voiceShortcutBound(sc.Phrase, voiceShortcutTextMax)
	sc.Kind = strings.ToLower(strings.TrimSpace(sc.Kind))
	sc.Title = voiceShortcutBound(sc.Title, voiceShortcutTextMax)
	sc.Where = voiceShortcutBound(sc.Where, voiceShortcutTextMax)
	sc.System = voiceShortcutBound(sc.System, voiceShortcutTextMax)
	sc.Path = strings.TrimSpace(sc.Path)
	sc.Command = voiceShortcutBound(sc.Command, voiceMaxCommandRunes)
	if voiceShortcutPhraseProblem(sc.Phrase) != "" {
		return voiceShortcut{}, false
	}
	switch sc.Kind {
	case voiceShortcutCommand:
		sc.Title, sc.Where, sc.System, sc.Path, sc.URL = "", "", "", "", ""
		return sc, sc.Command != ""
	case voiceShortcutWeb:
		u, ok := voiceShortcutWebURL(sc.URL)
		sc.URL = u
		sc.Where, sc.System, sc.Path, sc.Command = "", "", "", ""
		return sc, ok
	case voiceShortcutGame, voiceShortcutFile:
		sc.Command, sc.URL = "", ""
		if sc.Path == "" || len(sc.Path) > voiceShortcutPathMax {
			return voiceShortcut{}, false
		}
		if sc.Title == "" {
			sc.Title = filepath.Base(sc.Path)
		}
		return sc, true
	}
	if _, ok := voiceShortcutTargetKind(sc.Kind); !ok || sc.Title == "" {
		return voiceShortcut{}, false
	}
	sc.Path, sc.Command, sc.URL = "", "", ""
	return sc, true
}

// voiceShortcutsTidy cleans a list: unusable entries go, a phrase that folds
// like an earlier one goes (the first keeps it), and the list stops at
// voiceShortcutMax.
func voiceShortcutsTidy(in []voiceShortcut) []voiceShortcut {
	out := make([]voiceShortcut, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, sc := range in {
		if len(out) >= voiceShortcutMax {
			break
		}
		sc, ok := voiceShortcutClean(sc)
		if !ok {
			continue
		}
		key := voiceShortcutFold(sc.Phrase)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sc)
	}
	return out
}

// voiceShortcutsParse reads the saved list from a Custom value: the plain JSON
// a reload gives ([]interface{} of objects), the typed list, or a JSON string
// someone wrote by hand. An entry that does not decode is skipped, not the
// whole list.
func voiceShortcutsParse(v interface{}) []voiceShortcut {
	var data []byte
	switch t := v.(type) {
	case nil:
		return nil
	case []voiceShortcut:
		return voiceShortcutsTidy(t)
	case string:
		data = []byte(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		data = b
	}
	var items []json.RawMessage
	if json.Unmarshal(data, &items) != nil {
		return nil
	}
	if len(items) > 4*voiceShortcutMax {
		items = items[:4*voiceShortcutMax]
	}
	list := make([]voiceShortcut, 0, len(items))
	for _, item := range items {
		var sc voiceShortcut
		if json.Unmarshal(item, &sc) != nil {
			continue
		}
		list = append(list, sc)
	}
	return voiceShortcutsTidy(list)
}

// voiceShortcutsValue is the list as plain JSON values, the shape it has after
// a reload, so the Config page compares like with like.
func voiceShortcutsValue(list []voiceShortcut) []interface{} {
	out := make([]interface{}, 0, len(list))
	for _, sc := range list {
		m := map[string]interface{}{"phrase": sc.Phrase, "kind": sc.Kind}
		for _, kv := range [...][2]string{
			{"title", sc.Title}, {"where", sc.Where}, {"system", sc.System},
			{"path", sc.Path}, {"command", sc.Command}, {"url", sc.URL},
		} {
			if kv[1] != "" {
				m[kv[0]] = kv[1]
			}
		}
		out = append(out, m)
	}
	return out
}

// voiceShortcutsLoad is the saved list. Main thread (it reads the live config).
func voiceShortcutsLoad(config *Config) []voiceShortcut {
	if config == nil {
		config = appConfig
	}
	if config == nil || config.Variables.Custom == nil {
		return nil
	}
	return voiceShortcutsParse(config.Variables.Custom[voiceShortcutsKey])
}

// voiceShortcutsSave stores the list and schedules the jukauser.json write.
// The key is written directly, not through the settings store: it is a list,
// and the store's sync would re-apply every same-named mirror.
func voiceShortcutsSave(config *Config, list []voiceShortcut) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	list = voiceShortcutsTidy(list)
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	if len(list) == 0 {
		delete(config.Variables.Custom, voiceShortcutsKey)
	} else {
		config.Variables.Custom[voiceShortcutsKey] = voiceShortcutsValue(list)
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// voiceShortcutCountLabel is the Settings row's read-out.
func voiceShortcutCountLabel(config *Config) string {
	switch n := len(voiceShortcutsLoad(config)); n {
	case 0:
		return "None yet"
	case 1:
		return "1 word"
	default:
		return strconv.Itoa(n) + " words"
	}
}

// ── Matching ─────────────────────────────────────────────────────────────

// voiceShortcutTrimLead drops the articles in front of a name ("the", "my"),
// as long as something is left.
func voiceShortcutTrimLead(s string) string {
	for changed := true; changed; {
		changed = false
		for _, w := range voiceTargetLeading {
			if rest, ok := voiceCutWords(s, w); ok && rest != "" {
				s, changed = rest, true
			}
		}
	}
	return s
}

// voiceShortcutForms are the ways a folded command can carry a phrase, the
// most literal first: as it is, without the articles, without the
// "menu"/"app" words too, and the same three after an opening verb. Pure.
func voiceShortcutForms(heard string) []string {
	forms := make([]string, 0, 6)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, f := range forms {
			if f == s {
				return
			}
		}
		forms = append(forms, s)
	}
	add(heard)
	add(voiceShortcutTrimLead(heard))
	add(voiceTrimTarget(heard))
	if verb, rest := voiceSplitVerb(heard); verb != "" {
		add(rest)
		add(voiceShortcutTrimLead(rest))
		add(voiceTrimTarget(rest))
	}
	return forms
}

// voiceShortcutMatch is the index of the shortcut a folded command means, or
// -1. The most literal form decides first; among shortcuts the list's order
// does (the list never holds two phrases that fold alike). With nearMiss set,
// a phrase of nine letters or more that every form misses by a slip still
// counts — when it is the only one that close. Pure.
func voiceShortcutMatch(list []voiceShortcut, heard string, nearMiss bool) int {
	heard = strings.TrimSpace(heard)
	if heard == "" || len(list) == 0 {
		return -1
	}
	forms := voiceShortcutForms(heard)
	folded := make([]string, len(list))
	for i := range list {
		folded[i] = voiceShortcutFold(list[i].Phrase)
	}
	for _, f := range forms {
		for i := range folded {
			if folded[i] != "" && folded[i] == f {
				return i
			}
		}
	}
	if !nearMiss {
		return -1
	}
	best, bestSim, closeCount := -1, 0.0, 0
	for i := range folded {
		p := strings.ReplaceAll(folded[i], " ", "")
		if utf8.RuneCountInString(p) < 4 {
			continue
		}
		sim := 0.0
		for _, f := range forms {
			if s := voiceSimilarity(strings.ReplaceAll(f, " ", ""), p); s > sim {
				sim = s
			}
		}
		if sim >= voiceShortcutNearMiss {
			closeCount++
			if sim > bestSim {
				best, bestSim = i, sim
			}
		}
	}
	if closeCount == 1 {
		return best
	}
	return -1
}

// voiceShortcutRan is the folded command the last voiceExecute answered with a
// shortcut ("" when it did not): the voice orb asks voiceShortcutHandled before
// it hands a long sentence to Juka. voiceShortcutDepth is set while a command
// shortcut's own command runs, so a command that says another taught phrase
// (or its own) cannot loop. Main thread only.
var (
	voiceShortcutRan   string
	voiceShortcutDepth int
	// voiceShortcutInner is the intent a command shortcut's own command was
	// read as (voiceIntentNone for any other shortcut).
	voiceShortcutInner voiceIntentKind
)

// voiceShortcutHandled reports whether the command just executed was a taught
// phrase.
func voiceShortcutHandled(heard string) bool {
	return heard != "" && heard == voiceShortcutRan
}

// voiceShortcutSpokenKind is the intent whose answer the last command showed:
// a command shortcut's own command ("what time is it" taught as "clock") when
// the last command was one, else kind. The spoken answers (tts_api.go) choose
// by it: the time is read out, a volume change is not.
func voiceShortcutSpokenKind(kind voiceIntentKind) voiceIntentKind {
	if voiceShortcutRan != "" && voiceShortcutInner != voiceIntentNone {
		return voiceShortcutInner
	}
	return kind
}

// voiceShortcutExecute is voiceExecute's first step: the outcome of the
// shortcut the command names, and true; or false when it names none.
func voiceShortcutExecute(config *Config, in voiceIntent, src voiceSource) (voiceOutcome, bool) {
	if voiceShortcutDepth > 0 {
		return voiceOutcome{}, false
	}
	voiceShortcutRan, voiceShortcutInner = "", voiceIntentNone
	switch in.Kind {
	case voiceIntentCancel, voiceIntentHelp:
		// Always what they say, whatever was taught.
		return voiceOutcome{}, false
	}
	heard := strings.TrimSpace(in.Heard)
	if heard == "" || voiceNoisePhrases[heard] {
		return voiceOutcome{}, false
	}
	list := voiceShortcutsLoad(config)
	if len(list) == 0 {
		return voiceOutcome{}, false
	}
	// A near-miss may stand in for a name or for words the grammar did not
	// read; never for a command it understood ("volume 50" stays the volume).
	near := in.Kind == voiceIntentOpen || in.Kind == voiceIntentNone
	i := voiceShortcutMatch(list, heard, near)
	if i < 0 {
		return voiceOutcome{}, false
	}
	logDebug("voice", "voice shortcut %d (%s) matched", i, list[i].Kind)
	out := voiceShortcutOutcome(config, list[i], src)
	voiceShortcutRan = heard
	return out, true
}

// voiceShortcutRunCommand runs a command shortcut's command, with the
// shortcuts themselves out of the way.
func voiceShortcutRunCommand(config *Config, command string, src voiceSource) voiceOutcome {
	voiceShortcutDepth++
	defer func() { voiceShortcutDepth-- }()
	in := voiceParse(command)
	voiceShortcutInner = in.Kind
	return voiceExecute(config, in, src)
}

// voiceShortcutOutcome is what running sc does, in the catalog's own words:
// "Opening Settings", "Starting Mario Kart", the command's own answer.
func voiceShortcutOutcome(config *Config, sc voiceShortcut, src voiceSource) voiceOutcome {
	switch sc.Kind {
	case voiceShortcutCommand:
		return voiceShortcutRunCommand(config, sc.Command, src)
	case voiceShortcutWeb:
		if !IsOnline() {
			return voiceOutcome{Say: "Opening " + voiceShortcutHost(sc.URL) + " needs internet", Tone: tbxToneWarn}
		}
		address := sc.URL
		return voiceOutcome{Say: "Opening " + voiceShortcutHost(address), Act: func(c *Config) { openBrowserAtURL(c, address) }}
	}
	t, ok := voiceShortcutResolve(sc, voiceCatalog(config))
	if !ok {
		return voiceOutcome{Say: voiceShortcutGoneSentence(sc), Tone: tbxToneWarn}
	}
	if t.Quit {
		return voiceQuitOutcome(config)
	}
	return voiceOutcomeFor(voiceMatch{Target: t, Score: 1000})
}

// voiceShortcutGoneSentence says that what a shortcut opened is not here any
// more.
func voiceShortcutGoneSentence(sc voiceShortcut) string {
	what := sc.Title
	if what == "" {
		what = filepath.Base(sc.Path)
	}
	return "“" + sc.Phrase + "” was for " + what + ", which is not here any more"
}

// voiceShortcutSwappable reports whether two kinds may stand for each other
// when a shortcut is looked up: an emulator tile is a program (an app) once
// it is installed and a place on the Emulators page before (voiceemu.go).
func voiceShortcutSwappable(a, b voiceTargetKind) bool {
	isPlace := func(k voiceTargetKind) bool { return k == voiceTargetScreen || k == voiceTargetApp }
	return isPlace(a) && isPlace(b)
}

// voiceShortcutResolve finds what sc opens in the catalog: the same kind and
// title, the same subtitle preferred (two settings rows can share a label); a
// game or a file by its path, or — when the catalog does not list it (an
// index still being built, a file opened long ago) — the file itself while it
// is there.
func voiceShortcutResolve(sc voiceShortcut, targets []voiceTarget) (voiceTarget, bool) {
	kind, ok := voiceShortcutTargetKind(sc.Kind)
	if !ok {
		return voiceTarget{}, false
	}
	if kind == voiceTargetGame || kind == voiceTargetFile {
		return voiceShortcutResolvePath(sc, kind, targets)
	}
	title, where := homeSearchFold(sc.Title), homeSearchFold(sc.Where)
	if title == "" {
		return voiceTarget{}, false
	}
	best, bestRank := -1, 0
	for i := range targets {
		t := &targets[i]
		// The kind first: it is free, and folding every title of a card of
		// ROMs and files for each shortcut is not.
		rank := 0
		switch {
		case t.Kind == kind:
			rank = 2
		case voiceShortcutSwappable(t.Kind, kind):
			rank = 1
		default:
			continue
		}
		if homeSearchFold(t.Title) != title {
			continue
		}
		if where != "" && homeSearchFold(t.Where) == where {
			rank += 2
		}
		if rank > bestRank {
			best, bestRank = i, rank
		}
	}
	if best < 0 {
		return voiceTarget{}, false
	}
	return targets[best], true
}

// voiceShortcutResolvePath finds a game or a file shortcut's target.
func voiceShortcutResolvePath(sc voiceShortcut, kind voiceTargetKind, targets []voiceTarget) (voiceTarget, bool) {
	path := strings.TrimSpace(sc.Path)
	if path == "" {
		return voiceTarget{}, false
	}
	clean := filepath.Clean(path)
	for i := range targets {
		t := &targets[i]
		if t.Kind == kind && t.Path != "" && strings.EqualFold(filepath.Clean(t.Path), clean) {
			return *t, true
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return voiceTarget{}, false
	}
	title := sc.Title
	if title == "" {
		title = filepath.Base(path)
	}
	if kind == voiceTargetGame {
		if !info.Mode().IsRegular() {
			return voiceTarget{}, false
		}
		game := recentGame{Path: path, System: sc.System, Title: title}
		return voiceTarget{Title: title, Where: sc.Where, Icon: "gamepad", Kind: kind, Path: path,
			Action: func(*Config) { playRecentGame(game) }}, true
	}
	name, dir := filepath.Base(path), info.IsDir()
	return voiceTarget{Title: title, Where: sc.Where, Icon: homeSearchFileKind(name, dir), Kind: kind, Path: path,
		Action: func(c *Config) { homeSearchOpenFile(c, path, name, dir) }}, true
}

// voiceShortcutFromTarget is the saved reference to a catalog row.
func voiceShortcutFromTarget(phrase string, t voiceTarget) voiceShortcut {
	sc := voiceShortcut{Phrase: phrase, Kind: voiceShortcutKindName(t.Kind), Title: t.Title, Where: t.Where}
	switch t.Kind {
	case voiceTargetGame:
		// The catalog keeps a game's system in its keys.
		sc.Path, sc.System = t.Path, t.Keys
	case voiceTargetFile:
		sc.Path = t.Path
	}
	return sc
}

// ── Words for the page ───────────────────────────────────────────────────

// voiceShortcutDescribe says what a shortcut does, for its row and the check
// step. targets resolves a command's name ("open mario" → which Mario); nil
// describes it without looking.
func voiceShortcutDescribe(sc voiceShortcut, targets []voiceTarget) string {
	switch sc.Kind {
	case voiceShortcutCommand:
		return voiceCommandPreview(sc.Command, targets)
	case voiceShortcutWeb:
		return "Opens " + voiceShortcutHost(sc.URL) + " in the Browser"
	case voiceShortcutGame:
		s := "Starts " + sc.Title
		if sys := strings.TrimSpace(sc.System); sys != "" {
			s += " · " + sys
		}
		return s
	case voiceShortcutFile:
		return "Opens " + sc.Title + " · " + filepath.Base(filepath.Dir(sc.Path))
	}
	s := "Opens " + sc.Title
	if sc.Where != "" {
		s += " · " + sc.Where
	}
	return s
}

// voiceCommandPreview says what a command would do, without doing it: the
// grammar's reading of it and, for a name, what the catalog would open.
func voiceCommandPreview(command string, targets []voiceTarget) string {
	in := voiceParse(command)
	pct := func(n int) string { return strconv.Itoa(n) + "%" }
	onOffWord := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	switch in.Kind {
	case voiceIntentOpen:
		if targets == nil {
			return "Opens “" + in.Target + "”"
		}
		matches := voiceRankTargets(in.Target, in.Verb, targets)
		if voiceDecide(matches) {
			t := matches[0].Target
			if t.Where != "" {
				return "Opens " + t.Title + " · " + t.Where
			}
			return "Opens " + t.Title
		}
		if choices := voiceChoices(matches, 3); len(choices) > 0 {
			names := make([]string, 0, len(choices))
			for _, c := range choices {
				names = append(names, c.Target.Title)
			}
			return "Asks which one: " + strings.Join(names, ", ")
		}
		return "Finds nothing called “" + in.Target + "” yet"
	case voiceIntentSearch:
		return "Searches for “" + in.Query + "”"
	case voiceIntentWebSearch:
		return "Searches the web for “" + in.Query + "”"
	case voiceIntentVolumeSet:
		return "Sets the volume to " + pct(in.Value)
	case voiceIntentVolumeStep:
		if in.Value < 0 {
			return "Turns the volume down by " + strconv.Itoa(-in.Value)
		}
		return "Turns the volume up by " + strconv.Itoa(in.Value)
	case voiceIntentMute:
		return "Mutes the sound"
	case voiceIntentUnmute:
		return "Turns the sound back on"
	case voiceIntentBrightnessSet:
		return "Sets the brightness to " + pct(in.Value)
	case voiceIntentBrightnessStep:
		if in.Value < 0 {
			return "Dims the screen by " + strconv.Itoa(-in.Value)
		}
		return "Brightens the screen by " + strconv.Itoa(in.Value)
	case voiceIntentScreenshot:
		return "Takes a screenshot"
	case voiceIntentSleepScreen:
		return "Turns the screen off"
	case voiceIntentQuickSettings:
		return "Opens Quick Settings"
	case voiceIntentWifi:
		return "Turns Wi-Fi " + onOffWord(in.On)
	case voiceIntentBluetooth:
		return "Turns Bluetooth " + onOffWord(in.On)
	case voiceIntentAirplane:
		return "Turns airplane mode " + onOffWord(in.On)
	case voiceIntentHome:
		return "Goes to Home"
	case voiceIntentBack:
		return "Goes back"
	case voiceIntentMusicPause:
		return "Pauses the music"
	case voiceIntentMusicResume:
		return "Resumes the music"
	case voiceIntentMusicNext:
		return "Skips to the next song"
	case voiceIntentMusicPrev:
		return "Goes back a song"
	case voiceIntentMusicPlay:
		return "Plays music"
	case voiceIntentTime:
		return "Says the time"
	case voiceIntentDate:
		return "Says the date"
	case voiceIntentBattery:
		return "Says the battery level"
	case voiceIntentQuit:
		return "Asks to quit JukaHub"
	case voiceIntentHelp:
		return "Lists what you can say"
	case voiceIntentCancel:
		return "Cancels"
	}
	return "Nothing JukaHub understands as a command"
}

// voiceShortcutClash says what else answers to a phrase — another shortcut, a
// built-in command or a name the catalog opens — or "" when nothing does.
// skip is the index of the shortcut being changed (-1 for a new one).
func voiceShortcutClash(list []voiceShortcut, phrase string, skip int, targets []voiceTarget) string {
	folded := voiceShortcutFold(phrase)
	if folded == "" {
		return ""
	}
	for i := range list {
		if i != skip && voiceShortcutFold(list[i].Phrase) == folded {
			return "Another shortcut already listens for “" + folded + "” — saving replaces it"
		}
	}
	in := voiceParse(phrase)
	switch in.Kind {
	case voiceIntentNone:
		return ""
	case voiceIntentOpen:
		if targets == nil {
			return ""
		}
		matches := voiceRankTargets(in.Target, in.Verb, targets)
		if voiceDecide(matches) {
			return "“" + folded + "” already opens " + matches[0].Target.Title + " — your shortcut takes over"
		}
		return ""
	}
	what := voiceCommandPreview(phrase, nil)
	if what != "" {
		what = strings.ToLower(what[:1]) + what[1:]
	}
	return "“" + folded + "” is also a built-in command (it " + what + ") — your shortcut takes over"
}

// voiceShortcutPromptWords is the start of Whisper's vocabulary hint
// (voiceWhisperPrompt): the taught phrases as they were taught, each followed
// by ", ", bounded so the names after them still fit.
func voiceShortcutPromptWords() string {
	list := voiceShortcutsLoad(appConfig)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sc := range list {
		p := strings.TrimSpace(sc.Phrase)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(p)+2 > voiceShortcutPromptMax {
			break
		}
		b.WriteString(p)
		b.WriteString(", ")
	}
	return b.String()
}

// voiceShortcutScreenTarget is the Voice shortcuts page in the voice catalog.
// Home search reaches the page through its Settings row; a voice command only
// focuses a row, so "open voice shortcuts" gets the page itself here.
func voiceShortcutScreenTarget() voiceTarget {
	return voiceTarget{Title: "Voice shortcuts", Where: "Voice & AI", Icon: "mic",
		Keys: "voice shortcuts teach a word custom voice commands words phrases", Kind: voiceTargetScreen,
		Action: vscOpen}
}
