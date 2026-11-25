package main

// gamemenu_entries.go — which rows the in-game menu shows (Settings →
// Controls → "In-game menu entries").
//
// The menu (gamemenu.go) grew one row at a time — save and load, rewind,
// fast forward, the disc, cheats, the manual, Ask Juka, the game switcher,
// recording, closing background apps, sleep — and a player who never uses
// half of them scrolls past them in every game. This list lets each entry be
// switched off.
//
// Resume game stays first and Quit to JukaHub stays last, always: they are
// the way back into the game and the way out of it, and a menu without them
// could strand the player. They are not listed. A row the code does not name
// here (one added after this file) is never hidden, so a new row is not lost
// to an old choice.
//
// The choice is one Custom key, gameMenuHidden: a comma list of the rows'
// stable names ("save,rewind,ask"), never the gmItem numbers, which change
// whenever a row is added in the middle. Names this build does not know are
// kept when the list is written again, so a newer build's choice survives a
// trip through an older one. gmItems passes its rows through gmEntriesFilter
// as the last step: every other rule (save slots only for RetroArch games,
// Ask Juka only with the assistant on) still decides first.
//
// The list is the Settings list sheet (setsheet.go) used as a set of
// switches, the way Profiles' "Ask who is playing at start" row is: A flips
// the highlighted row and the sheet opens again on the same row, so several
// rows are changed in one visit.

import (
	"strconv"
	"strings"
)

const (
	// gameMenuHiddenKey is the Custom setting: the hidden rows' names.
	gameMenuHiddenKey = "gameMenuHidden"
	// gmEntriesShowAll is the sheet's last row, which shows every row again.
	gmEntriesShowAll = "*all"
	gmEntriesTitle   = "In-game menu entries"
)

// gmEntry is one row the player may hide: the item, its stored name, and
// the words the sheet shows for it.
type gmEntry struct {
	item  gmItem
	id    string
	label string
	note  string
}

// gmEntries are the optional rows in the menu's own order. Resume game and
// Quit to JukaHub are left out on purpose: they always show.
var gmEntries = []gmEntry{
	{gmMusic, "music", "Music", "Play, pause and skip JukaHub's music while it plays"},
	{gmStopwatch, "stopwatch", "Stopwatch / Timer", "A running stopwatch or countdown"},
	{gmSave, "save", "Save state", "Six slots with pictures, RetroArch games"},
	{gmLoad, "load", "Load state", "Go back to a moment you saved"},
	{gmRewind, "rewind", "Rewind", "Step back through recent gameplay"},
	{gmFast, "fast", "Fast forward", "Run the game faster until you switch it off"},
	{gmDisc, "disc", "Disc", "Change the disc of a multi-disc game"},
	{gmCheats, "cheats", "Cheats", "Switch the game's cheats on and off"},
	{gmManual, "manual", "Manual", "The game's controls and text guides"},
	{gmTranslate, "translate", "Translate screen", "With Translate games on"},
	{gmAsk, "ask", "Ask Juka", "With the game assistant on"},
	{gmSwitch, "switcher", "Game switcher", "Quit, then pick up another recent game"},
	{gmRecord, "record", "Record screen", "Record a clip of the game"},
	{gmPerf, "perf", "Performance", "The game's CPU profile, changed while it runs"},
	{gmQuick, "quick", "Quick settings", "Brightness, volume, Wi-Fi, lights, power off"},
	{gmCloseBg, "closeapps", "Close background apps", "Free memory and keep the game"},
	{gmSleep, "sleep", "Sleep", "The game carries on when the handheld wakes"},
}

// gmEntryID is an item's stored name, "" for a row that cannot be hidden.
func gmEntryID(it gmItem) string {
	for _, e := range gmEntries {
		if e.item == it {
			return e.id
		}
	}
	return ""
}

// gmEntryIndex is a name's place in gmEntries, -1 when unknown.
func gmEntryIndex(id string) int {
	for i, e := range gmEntries {
		if e.id == id {
			return i
		}
	}
	return -1
}

// gameMenuHiddenNames reads the stored list in its order, lower-cased,
// without blanks or repeats.
func gameMenuHiddenNames(c *Config) []string {
	if c == nil {
		return nil
	}
	raw, ok := customString(c.Variables.Custom, gameMenuHiddenKey)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.ToLower(strings.TrimSpace(part))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// gameMenuHiddenSet is the stored list as a set.
func gameMenuHiddenSet(c *Config) map[string]bool {
	names := gameMenuHiddenNames(c)
	set := make(map[string]bool, len(names))
	for _, id := range names {
		set[id] = true
	}
	return set
}

// gameMenuHiddenCount is how many rows this build knows are hidden.
func gameMenuHiddenCount(c *Config) int {
	n := 0
	for _, id := range gameMenuHiddenNames(c) {
		if gmEntryIndex(id) >= 0 {
			n++
		}
	}
	return n
}

// gameMenuHiddenFormat writes the set back: the known rows in the menu's
// order, then the names this build does not know, as they were stored.
func gameMenuHiddenFormat(c *Config, hidden map[string]bool) string {
	var out []string
	for _, e := range gmEntries {
		if hidden[e.id] {
			out = append(out, e.id)
		}
	}
	for _, id := range gameMenuHiddenNames(c) {
		if gmEntryIndex(id) < 0 && hidden[id] {
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
}

// gmEntriesFilter is gmItems' last step: the rows the player hid go. Resume,
// Quit and any row not in gmEntries stay, and so does a hidden row that is
// the only way to stop something running now: Stop recording while a clip
// records, Normal speed while fast forward is on. items is gmItems' own new
// slice, so it is filtered in place.
func gmEntriesFilter(config *Config, items []gmItem) []gmItem {
	hidden := gameMenuHiddenSet(config)
	if len(hidden) == 0 {
		return items
	}
	out := items[:0]
	for _, it := range items {
		if id := gmEntryID(it); id != "" && hidden[id] && !gmEntryBusy(it) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// gmEntryBusy reports a hidden row that must show anyway (gmEntriesFilter).
func gmEntryBusy(it gmItem) bool {
	switch it {
	case gmRecord:
		return screenRecActive()
	case gmFast:
		_, fast := retroTransportAvailable()
		return fast
	}
	return false
}

// ── Settings ─────────────────────────────────────────────────────────────

// gameMenuEntriesSettingsRow opens the list, under the "In-game menu" switch
// (Settings → Controls: stickcal.go). Greyed while the menu itself is off.
func gameMenuEntriesSettingsRow() setRow {
	return setRow{
		ID: gameMenuHiddenKey, Label: gmEntriesTitle, Kind: setKindAction,
		Detail: func(c *Config) string {
			if !gameMenuEnabled(c) {
				return "Turn on In-game menu first"
			}
			return "Choose the rows it shows · Resume and Quit always stay"
		},
		Visible:  func(*Config) bool { return gameAssistSupported() },
		Disabled: func(c *Config) bool { return !gameMenuEnabled(c) },
		Value:    gameMenuEntriesValue,
		Open:     func(c *Config) { gameMenuEntriesSheet(c, 0, 0) },
	}
}

// gameMenuEntriesValue is the row's read-out.
func gameMenuEntriesValue(c *Config) string {
	switch n := gameMenuHiddenCount(c); n {
	case 0:
		return "All shown"
	case 1:
		return "1 hidden"
	default:
		return strconv.Itoa(n) + " hidden"
	}
}

// gameMenuEntriesSheet opens (or opens again) the list with the highlight on
// row cursor and the list scrolled as it was.
func gameMenuEntriesSheet(c *Config, cursor int, scroll int32) {
	hidden := gameMenuHiddenSet(c)
	opts := make([]setOption, 0, len(gmEntries)+1)
	for _, e := range gmEntries {
		state := "Shown"
		if hidden[e.id] {
			state = "Hidden"
		}
		opts = append(opts, setOption{Value: e.id, Label: e.label + ": " + state, Note: e.note})
	}
	if gameMenuHiddenCount(c) > 0 {
		opts = append(opts, setOption{Value: gmEntriesShowAll, Label: "Show every entry", Note: "Back to the full menu"})
	}
	setSheetOpen(&setHub.sheet, gmEntriesTitle,
		hintSentence("%s shows or hides a row · Resume game stays first, Quit to JukaHub last", "A", "Select"),
		opts, "", func(value string) { gameMenuEntriesChoose(c, value) })
	if cursor > 0 && cursor < len(opts) {
		setHub.sheet.cursor = cursor
		setHub.sheet.scroll = scroll
	}
}

// gameMenuEntriesChoose flips one row (or shows them all) and opens the list
// again on the same row.
func gameMenuEntriesChoose(c *Config, value string) {
	// The sheet closed for the pick but still holds where it was scrolled.
	scroll := setHub.sheet.scroll
	hidden := gameMenuHiddenSet(c)
	cursor := 0
	if value == gmEntriesShowAll {
		for _, e := range gmEntries {
			delete(hidden, e.id)
		}
		setToast("In-game menu: every entry shown")
		scroll = 0
	} else {
		cursor = gmEntryIndex(value)
		if cursor < 0 {
			return
		}
		hidden[value] = !hidden[value]
	}
	setStoreString(c, gameMenuHiddenKey, gameMenuHiddenFormat(c, hidden))
	setHub.dirty = true
	gameMenuEntriesSheet(c, cursor, scroll)
}
