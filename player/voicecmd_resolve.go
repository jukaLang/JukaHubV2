package main

// voicecmd_resolve.go — which screen, app or game "open <name>" means.
//
// Voice commands resolve names against exactly what Home search finds
// (homesearch_index.go): every screen and settings row with the words people
// use for them, the Apps launcher, recently played games and the ROM library,
// the indexed media and book folders and the Continue timeline. The ranking is
// homeSearchMatch's ladder (exact title, title prefix, every word, a synonym,
// …) plus three adjustments that matter when the words are spoken rather than
// typed:
//
//   - A settings row loses to a screen of the same name unless the command
//     says "settings": "open Bluetooth" is the Bluetooth page, not the
//     Bluetooth switch on the Network settings page.
//   - "play" favours games, music and the Continue timeline.
//   - A misheard name still gets somewhere: when nothing matches outright, a
//     title within a small edit distance is offered ("setings" → Settings),
//     but never opened without a confirming press unless it is the only close
//     one.
//
// A match opens at once only when it is clearly the one meant: strong (a title
// or a synonym, not a substring) and well ahead of the next. Otherwise the
// voice page lists the best few and the user picks; when nothing is close, it
// offers the Home search sheet with the words already typed.
//
// The ranking (voiceRankTargets, voiceDecide) is pure and tested without a
// renderer; voiceTargetsFromApp gathers the live sources on the main thread.

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// voiceTargetKind is where a target comes from.
type voiceTargetKind int

const (
	voiceTargetScreen voiceTargetKind = iota
	voiceTargetSetting
	voiceTargetApp
	voiceTargetGame
	voiceTargetFile
	voiceTargetMedia
)

// voiceTarget is one thing a name can open.
type voiceTarget struct {
	Title string
	Where string // the subtitle in a choice list: "Settings · Display", "SNES"
	Icon  string
	Keys  string // synonyms (screens and settings rows)
	Kind  voiceTargetKind
	// Blocked is why the target cannot open right now ("Needs internet").
	Blocked string
	// Quit marks the "Quit JukaHub" row, which voice routes through the
	// confirmation instead of quitting on the spot.
	Quit bool
	// Builtin marks an app JukaHub carries inside itself (JukaLand, the
	// Browser, Trophies, PS Remote Play): the Apps list repeats those screens
	// under the same names.
	Builtin bool
	Action  func(*Config)
	// Path is a game's or a file's path: what a voice shortcut keeps to find
	// it again (voiceshortcuts.go).
	Path string
}

// voiceMatch is a ranked target.
type voiceMatch struct {
	Target voiceTarget
	Score  int
	// Fuzzy matches came from the edit distance, not the search ladder; Sim
	// is their similarity (0..1).
	Fuzzy bool
	Sim   float64
}

// Ranking thresholds. The tiers are homeSearchMatch's: 1000 exact title, 800
// title prefix, 600 every word, 480 an exact synonym, 380 synonym words, 260
// title substring, 160 synonym substring, 140 initials.
const (
	voiceSettingPenalty = 500 // a settings row against a screen of the same name
	voiceSettingBoost   = 150 // … unless the command said "settings"
	voicePlayBoost      = 60  // "play" prefers games, music and the timeline
	voiceFilePenalty    = 40  // indexed files are many and noisy
	voiceMediaPenalty   = 20
	voiceConfidentMin   = 380 // weaker than a synonym word never opens by itself
	voiceConfidentLead  = 100 // and the best must be this far ahead
	voiceChoiceMin      = 140
	voiceMaxChoices     = 5
	voiceFuzzyMin       = 0.72
	voiceFuzzySure      = 0.88
)

// voiceSettingsWords mark a command that asks for a settings page.
var voiceSettingsWords = voiceSet("settings", "setting", "options", "option", "preferences")

// voiceRankTargets scores every target against the spoken name and returns
// the matches, best first. verb is the intent's verb ("play" changes the
// weights). The order of equal scores is the catalog's, which puts screens
// before apps before games, as the Home search sheet does.
func voiceRankTargets(name, verb string, targets []voiceTarget) []voiceMatch {
	name = strings.TrimSpace(name)
	if name == "" || len(targets) == 0 {
		return nil
	}
	words := strings.Fields(name)
	wantsSettings := false
	kept := words[:0:0]
	for _, w := range words {
		if voiceSettingsWords[w] {
			wantsSettings = true
			continue
		}
		kept = append(kept, w)
	}
	query := name
	if wantsSettings && len(kept) > 0 {
		// "bluetooth settings" looks for Bluetooth among the settings rows; a
		// bare "settings" stays itself.
		query = strings.Join(kept, " ")
	}
	q := newHomeSearchQuery(query)
	// A name of three letters or fewer — often a word speech recognition made
	// out of a breath ("you") — opens by itself only on an exact title, a whole
	// title word or an exact synonym. A prefix ("you" → YouTube, "the" →
	// Themes) is offered in the list at most.
	short := utf8.RuneCountInString(q.compact) < 4
	screens := voiceScreenTitles(targets)
	out := make([]voiceMatch, 0, 16)
	for _, t := range targets {
		if voiceShadowed(t, screens) {
			continue
		}
		keys := t.Keys
		if t.Kind == voiceTargetScreen || t.Kind == voiceTargetSetting {
			keys = strings.TrimSpace(t.Keys + " " + t.Where)
		}
		raw := homeSearchMatch(q, t.Title, keys)
		if raw == 0 {
			continue
		}
		score := raw
		switch t.Kind {
		case voiceTargetSetting:
			if wantsSettings {
				score += voiceSettingBoost
			} else {
				score -= voiceSettingPenalty
			}
		case voiceTargetGame:
			score--
		case voiceTargetFile:
			score -= voiceFilePenalty
		case voiceTargetMedia:
			score -= voiceMediaPenalty
		}
		if verb == "play" {
			switch t.Kind {
			case voiceTargetGame, voiceTargetFile, voiceTargetMedia:
				score += voicePlayBoost
			}
		}
		if score <= 0 {
			continue
		}
		if short && score >= voiceConfidentMin && !voiceShortMatchIsWhole(raw, q, t.Title) {
			score = voiceConfidentMin - 1
		}
		out = append(out, voiceMatch{Target: t, Score: score})
	}
	if voiceBestScore(out) < 260 {
		// Nothing, or only weak matches: a near-miss of a title may be what
		// was meant.
		out = append(out, voiceFuzzyTargets(q.compact, targets, screens)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return voiceDedupeMatches(out)
}

// voiceShadowed reports whether t is a built-in app that is also one of the
// screens (JukaLand, the Browser, Trophies, PS Remote Play): the two copies
// tied, and "open browser" asked which of two identical rows was meant.
func voiceShadowed(t voiceTarget, screens map[string]bool) bool {
	return t.Builtin && t.Kind == voiceTargetApp && screens[homeSearchFold(t.Title)]
}

// voiceScreenTitles is the set of the screens' folded titles.
func voiceScreenTitles(targets []voiceTarget) map[string]bool {
	m := make(map[string]bool, 64)
	for _, t := range targets {
		if t.Kind == voiceTargetScreen {
			m[homeSearchFold(t.Title)] = true
		}
	}
	return m
}

// voiceShortMatchIsWhole reports whether homeSearchMatch's tier raw met the
// name whole: the exact title (1000), an exact synonym (480), or every word
// equal to a title word (600 also takes word prefixes, which a short name
// must not ride on).
func voiceShortMatchIsWhole(raw int, q homeSearchQuery, title string) bool {
	switch raw {
	case 1000, 480:
		return true
	case 600:
		words := strings.Fields(homeSearchFold(title))
		for _, qw := range q.words {
			if !voiceHasWord(words, qw) {
				return false
			}
		}
		return len(q.words) > 0
	}
	return false
}

// voiceBestScore is the highest score in m.
func voiceBestScore(m []voiceMatch) int {
	best := 0
	for _, x := range m {
		if x.Score > best {
			best = x.Score
		}
	}
	return best
}

// voiceFuzzyTargets compares the folded, space-free name with every title's
// and keeps the close ones. A fuzzy score sits below every ladder tier (at
// most 130), so a real match always ranks first.
func voiceFuzzyTargets(compact string, targets []voiceTarget, screens map[string]bool) []voiceMatch {
	if utf8.RuneCountInString(compact) < 4 {
		return nil
	}
	var out []voiceMatch
	for _, t := range targets {
		if voiceShadowed(t, screens) {
			continue
		}
		title := strings.ReplaceAll(homeSearchFold(t.Title), " ", "")
		if title == "" {
			continue
		}
		sim := voiceSimilarity(compact, title)
		if sim < voiceFuzzyMin {
			continue
		}
		out = append(out, voiceMatch{Target: t, Score: int(130 * sim), Fuzzy: true, Sim: sim})
	}
	return out
}

// voiceDedupeMatches drops the second of two matches with the same title and
// subtitle: a screen can be both in the catalog and in the Start menu.
func voiceDedupeMatches(in []voiceMatch) []voiceMatch {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, m := range in {
		key := strings.ToLower(m.Target.Title) + "\x00" + strings.ToLower(m.Target.Where)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out
}

// voiceDecide says whether the best match opens by itself. It does when it is
// a strong match well ahead of the next one, or the only close near-miss.
func voiceDecide(m []voiceMatch) bool {
	if len(m) == 0 {
		return false
	}
	best := m[0]
	if best.Target.Blocked != "" {
		// It still opens nothing: the page says why instead.
		return false
	}
	if best.Fuzzy {
		if best.Sim < voiceFuzzySure {
			return false
		}
		return len(m) == 1 || m[1].Sim < voiceFuzzyMin || (!m[1].Fuzzy && m[1].Score < best.Score)
	}
	if best.Score < voiceConfidentMin {
		return false
	}
	if len(m) == 1 {
		return true
	}
	return best.Score-m[1].Score >= voiceConfidentLead
}

// voiceChoices keeps the matches worth offering as a list, at most n.
func voiceChoices(m []voiceMatch, n int) []voiceMatch {
	out := make([]voiceMatch, 0, n)
	for _, x := range m {
		if len(out) >= n {
			break
		}
		if !x.Fuzzy && x.Score < voiceChoiceMin {
			continue
		}
		out = append(out, x)
	}
	return out
}

// voiceSimilarity is 1 - (edit distance / longer length) over runes, with a
// transposition counted as one edit ("seting" and "settign" are both one
// away from what was meant).
func voiceSimilarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	longer := len(ra)
	if len(rb) > longer {
		longer = len(rb)
	}
	// A far longer title is not a misspelling of a short name ("mario" is a
	// word of "Super Mario Bros", which the ladder already handles).
	if len(ra)*2 < len(rb) || len(rb)*2 < len(ra) {
		return 0
	}
	d := voiceEditDistance(ra, rb)
	return 1 - float64(d)/float64(longer)
}

// voiceEditDistance is the optimal-string-alignment distance: insertions,
// deletions, substitutions and adjacent transpositions.
func voiceEditDistance(a, b []rune) int {
	n, m := len(a), len(b)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}
	prev2 := make([]int, m+1)
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := prev[j] + 1
			if x := cur[j-1] + 1; x < v {
				v = x
			}
			if x := prev[j-1] + cost; x < v {
				v = x
			}
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if x := prev2[j-2] + 1; x < v {
					v = x
				}
			}
			cur[j] = v
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[m]
}

// ── The live catalog ─────────────────────────────────────────────────────

// voiceOwnTitles are left out of the catalog: the voice page opening itself.
var voiceOwnTitles = voiceSet("voice command", "voice commands")

// voiceTargetsFromApp gathers what Home search would search, on the main
// thread: the screens (with the settings rows), the apps, the recent and
// indexed games, recently opened and indexed files, and the Continue
// timeline. online decides which screens are marked as needing internet.
func voiceTargetsFromApp(config *Config) []voiceTarget {
	if config == nil {
		config = appConfig
	}
	online := IsOnline()
	indexedApps, indexedGames, indexedFiles := homeSearchIndexSnapshot()
	out := make([]voiceTarget, 0, 256)
	for _, s := range homeSearchScreens(config) {
		if voiceOwnTitles[strings.ToLower(s.title)] {
			continue
		}
		kind := voiceTargetScreen
		if strings.HasPrefix(s.where, "Settings ·") {
			kind = voiceTargetSetting
		}
		t := voiceTarget{Title: s.title, Where: s.where, Icon: s.icon, Keys: s.keys, Kind: kind, Action: s.action}
		if reason := homeSearchOfflineReason(s.scene, s.title, online); reason != "" {
			t.Blocked = reason
		}
		if strings.EqualFold(s.title, "Quit JukaHub") {
			t.Quit = true
		}
		out = append(out, t)
	}
	var apps []FileEntry
	if config != nil {
		if entries, ok := config.Variables.Custom[appsEntriesVar].([]FileEntry); ok && len(entries) > 0 {
			apps = entries
		}
	}
	if len(apps) == 0 {
		apps = append(append([]FileEntry(nil), indexedApps...), builtinAppEntries(config)...)
	}
	seenApps := map[string]bool{}
	for _, e := range apps {
		name := appGridDisplayName(e.Name)
		key := strings.ToLower(name)
		if name == "" || seenApps[key] {
			continue
		}
		seenApps[key] = true
		entry := e
		t := voiceTarget{Title: name, Where: "App", Icon: "apps", Kind: voiceTargetApp,
			Action: func(c *Config) { launchAppEntry(c, entry) }}
		if homeAppIsBuiltin(entry) {
			t.Where, t.Icon, t.Builtin = "Built into JukaHub", "juka", true
		}
		out = append(out, t)
	}
	// The emulators — the pages around them and every tile of the Emulators
	// page (voiceemu.go) — and the Voice shortcuts page (voiceshortcuts.go).
	out = append(out, voiceEmulatorTargets(out)...)
	out = append(out, voiceShortcutScreenTarget())
	// A fresh slice: recentGames' answer must not have the index appended
	// into its spare capacity.
	recentList := recentGames()
	games := make([]recentGame, 0, len(recentList)+len(indexedGames))
	games = append(append(games, recentList...), indexedGames...)
	seenGames := map[string]bool{}
	for _, g := range games {
		key := strings.ToLower(filepath.Clean(g.Path))
		if g.Path == "" || seenGames[key] {
			continue
		}
		seenGames[key] = true
		title := strings.TrimSpace(g.Title)
		if title == "" {
			title = gameDisplayTitle(g.Path)
		}
		game := g
		where := "Game"
		if g.System != "" {
			where = g.System
		}
		out = append(out, voiceTarget{Title: title, Where: where, Icon: "gamepad", Keys: g.System, Kind: voiceTargetGame,
			Path: g.Path, Action: func(*Config) { playRecentGame(game) }})
	}
	recent := getRecentItems()
	seenFiles := map[string]bool{}
	addFile := func(name, path, parent string, isDir bool) {
		key := strings.ToLower(path)
		if path == "" || seenFiles[key] {
			return
		}
		seenFiles[key] = true
		p, n, dir := path, name, isDir
		if parent == "" || parent == "." {
			parent = filepath.Base(filepath.Dir(path))
		}
		out = append(out, voiceTarget{Title: name, Where: parent, Icon: homeSearchFileKind(name, isDir), Kind: voiceTargetFile,
			Path: p, Action: func(c *Config) { homeSearchOpenFile(c, p, n, dir) }})
	}
	for _, it := range recent {
		if it.Type != "file" {
			continue
		}
		if fe, ok := it.Data.(FileEntry); ok && fe.Path != "" {
			name := fe.Name
			if name == "" {
				name = filepath.Base(fe.Path)
			}
			addFile(name, fe.Path, filepath.Base(filepath.Dir(fe.Path)), fe.IsDir)
		}
	}
	for _, f := range indexedFiles {
		addFile(f.Name, f.Path, f.Parent, f.IsDir)
	}
	for i := range recent {
		it := recent[i]
		if it.Type == "file" {
			continue
		}
		title := it.Label()
		if strings.TrimSpace(title) == "" {
			continue
		}
		kind, icon := homeSearchRecentKind(it)
		item := it
		out = append(out, voiceTarget{Title: title, Where: kind, Icon: icon, Keys: kind, Kind: voiceTargetMedia,
			Action: func(c *Config) { homePlayRecent(c, item) }})
	}
	return out
}

// voiceWhisperPrompt is the vocabulary hint sent with a clip: the names on
// this handheld that speech recognition would otherwise spell wrong
// ("JukaLand", "RetroArch", a game's title). Whisper reads at most a couple
// of hundred words of prompt; the list is cut well before that.
func voiceWhisperPrompt(targets []voiceTarget) string {
	const maxRunes = 600
	var b strings.Builder
	// The user's own words first, so they come back spelled the way they were
	// taught (voiceshortcuts.go).
	b.WriteString(voiceShortcutPromptWords())
	b.WriteString("JukaHub, JukaLand, RetroArch, YouTube, Wi-Fi, Bluetooth, screenshot, volume, brightness")
	seen := map[string]bool{}
	for _, t := range targets {
		if t.Kind == voiceTargetSetting || t.Kind == voiceTargetFile {
			continue
		}
		title := strings.TrimSpace(t.Title)
		key := strings.ToLower(title)
		if title == "" || seen[key] || utf8.RuneCountInString(title) > 40 {
			continue
		}
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(title)+2 > maxRunes {
			break
		}
		seen[key] = true
		b.WriteString(", ")
		b.WriteString(title)
	}
	b.WriteString(".")
	return b.String()
}
