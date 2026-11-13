package main

// setessentials.go — the short version of every Settings section.
//
// Settings grew to some two hundred rows, and the owner's complaint was fair:
// you had to go searching. Each section now opens on the few rows people
// actually change (setEssentialRows), with a "More settings (N)" row under
// them that unfolds the rest in place. Nothing is removed and no row id
// changes: search, voice commands, deep links and Home's row results still
// reach every row, and a folded row they ask for unfolds its section first
// (setHubFocusRow). The "Show all settings" switch at the end of About brings
// back the full lists for anyone who prefers them.
//
// The list is keyed by section id and row id, so a row another feature adds
// later simply lands under More; a section with no entry here (Plugins, or a
// new one) shows every row. Two small sections folded into neighbours to
// shorten the rail: Date & time is now part of General and Keyboard part of
// Controls (setSectionAliases keeps their old ids working).

import (
	"strconv"
	"strings"
)

// setMoreRowID is the synthetic "More settings" row the hub inserts. It is
// never part of the catalogue, so search and voice never see it.
const setMoreRowID = "_settingsMore"

// setShowAllKey is the Custom key of the "Show all settings" switch.
const setShowAllKey = "settingsShowAll"

// setEssentialRows are the rows each section shows before More, by row id.
// The section keeps its own order; this only says which rows are up front.
// Picked for what people change most on a handheld: the look, the sound, the
// network, the battery, the buttons.
func setEssentialRows() map[string][]string {
	m := map[string][]string{}
	m["general"] = []string{"name", "uiLanguage", groqKeyRowID, "weather", "timezone", "clock12"}
	m["display"] = []string{"themes", "glassStyle", "homeLayout", "wallpaper", "brightness", "textSize"}
	m[a11ySettingsSection] = []string{"textSize", "contrast", "boldText", "magnifier", "monoAudio", "colorblind", "oneHanded"}
	m["sound"] = []string{"volume", "uiSounds", "bgMusic", "vibration", "audio"}
	m["network"] = []string{"wifi", "bluetooth", "btdevices", "airplane", "companion"}
	m[voiceAISettingsSection] = []string{ttsModeKey, hjWordKey, voiceShortcutsKey, gameAssistKey, sttModeKey}
	m["controls"] = []string{"buttonHints", "trimuiStickCalibrate", "stickCalibrate", "inputProfile", "vk"}
	m[ledSettingsSection] = []string{"lightsMissing", "ledsync", "ledMode", "ledColor", "ledBrightness"}
	m["media"] = []string{"resolution", "subtitles", "downloads", "notifyDuringVideo"}
	m["games"] = []string{"swapAB", "swapXY", "osd", "emulators", "library", "saves"}
	m["power"] = []string{"powerMode", "batteryNow", "screensaver", "batterySaver", "lowPower"}
	m[brwSettingsSection] = []string{"searchEngine", "homePage", "blockAds"}
	m["privacy"] = []string{"cookies", "forgetCookies", "diagnostics", "clearCache"}
	m["about"] = []string{"device", "updates", "updatesCheck", "aboutPage", "bug", setShowAllKey}
	return m
}

// setEssentialSet is setEssentialRows as sets, built on first use.
var setEssentialSet map[string]map[string]bool

// setIsEssential reports whether row id is up front in section id, and
// whether the section has an essentials list at all.
func setIsEssential(sectionID, rowID string) (essential, listed bool) {
	if setEssentialSet == nil {
		lists := setEssentialRows()
		setEssentialSet = make(map[string]map[string]bool, len(lists))
		for sec, ids := range lists {
			m := make(map[string]bool, len(ids))
			for _, id := range ids {
				m[id] = true
			}
			setEssentialSet[sec] = m
		}
	}
	m, ok := setEssentialSet[sectionID]
	if !ok {
		return false, false
	}
	return m[rowID], true
}

// setMoreOpen is which sections have More unfolded, for this run of the app.
var setMoreOpen = map[string]bool{}

// setShowAll reports the "Show all settings" switch.
func setShowAll(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, setShowAllKey, false)
}

// setArrangeOut is one section's rows as the hub shows them.
type setArrangeOut struct {
	rows  []setRow
	views []setRowView
	// folded marks rows that are shown only once More is unfolded.
	folded []bool
}

// setEssentialsArrange orders a section's rows for the hub: its essentials
// (in the section's order), then the More row, then everything else, folded
// unless the section is unfolded. Expanding therefore only adds rows below
// the More row, so nothing above it moves and the focus stays on it. With
// Show all on, or for an unlisted section, the rows come back as they are;
// when nothing (or everything) would fold, the More row is kept but hidden.
func setEssentialsArrange(c *Config, sectionID string, rows []setRow, views []setRowView) setArrangeOut {
	out := setArrangeOut{rows: rows, views: views, folded: make([]bool, len(rows))}
	if setShowAll(c) || len(rows) != len(views) {
		return out
	}
	if _, listed := setIsEssential(sectionID, ""); !listed {
		return out
	}
	var front, rest []int
	shownFront, shownRest := 0, 0
	var restLabels []string
	for i, row := range rows {
		if ess, _ := setIsEssential(sectionID, row.ID); ess {
			front = append(front, i)
			if views[i].visible {
				shownFront++
			}
			continue
		}
		rest = append(rest, i)
		if views[i].visible {
			shownRest++
			if len(restLabels) < 3 {
				restLabels = append(restLabels, row.Label)
			}
		}
	}
	// When nothing would fold (no essential shown, or nothing else shown) the
	// More row is still there, hidden, and nothing folds: the shown rows keep
	// the section's order either way, and the indices stay the same when a
	// probe shows or hides a row between two refreshes, so the focus (an
	// index) never jumps to a neighbour.
	fold := shownFront > 0 && shownRest > 0
	open := setMoreOpen[sectionID]
	more := setMoreRow(sectionID, shownRest, restLabels, open)
	n := len(rows) + 1
	out = setArrangeOut{rows: make([]setRow, 0, n), views: make([]setRowView, 0, n), folded: make([]bool, 0, n)}
	for _, i := range front {
		out.rows = append(out.rows, rows[i])
		out.views = append(out.views, views[i])
		out.folded = append(out.folded, false)
	}
	moreView := setHubReadRow(c, more)
	moreView.visible = moreView.visible && fold
	out.rows = append(out.rows, more)
	out.views = append(out.views, moreView)
	out.folded = append(out.folded, false)
	for _, i := range rest {
		v := views[i]
		folded := fold && !open && v.visible
		if folded {
			v.visible = false
		}
		out.rows = append(out.rows, rows[i])
		out.views = append(out.views, v)
		out.folded = append(out.folded, folded)
	}
	return out
}

// setMoreRow is the row that folds and unfolds a section's other rows. Its
// line names the first few it hides, so what is under it is never a guess.
func setMoreRow(sectionID string, hidden int, labels []string, open bool) setRow {
	label := "More settings (" + strconv.Itoa(hidden) + ")"
	detail := strings.Join(labels, ", ")
	if hidden > len(labels) {
		detail += "…"
	}
	if open {
		label = "Fewer settings"
		detail = "Show only the essentials again"
	}
	id := sectionID
	return setRow{
		ID: setMoreRowID, Label: label, Kind: setKindAction,
		Detail: setStatic(detail),
		Open: func(c *Config) {
			setMoreOpen[id] = !setMoreOpen[id]
			setHubRefresh(c)
			setHubFocusRow(setMoreRowID, "")
		},
	}
}

// setShowAllRow is the last row of About: the old, full lists.
func setShowAllRow() setRow {
	return setRow{
		ID: setShowAllKey, Label: "Show all settings", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if setShowAll(c) {
				return "Every row in every section, no More settings"
			}
			return "Off: each section starts with its most-used rows"
		},
		Get: setShowAll,
		Set: func(c *Config, on bool) {
			setStoreBool(c, setShowAllKey, on)
			saveUserConfigDebounced(userConfigFromVariables(c))
			setToast("Show all settings: " + setOnOff(on))
			// The rows reorder; keep the focus on this switch. Voice or Home
			// search may flip it from another screen, where the hub's state
			// is not on show and the next visit re-reads it anyway.
			if setIsHubScene(setHubSceneName(c)) {
				setHubRefresh(c)
				setHubFocusRow(setShowAllKey, "")
			}
		},
	}
}

// ── Folding small sections into neighbours ───────────────────────────────

// setSectionAliases are retired section ids and the section that now holds
// their rows. setSectionIndex resolves them, so old deep links, search
// entries and saved state still land on the right page.
var setSectionAliases = map[string]string{
	"datetime": "general",
	"keyboard": "controls",
}

// setSectionAlias is the live section id for id.
func setSectionAlias(id string) string {
	if to, ok := setSectionAliases[id]; ok {
		return to
	}
	return id
}

// setArrangeSections is the catalogue as the rail shows it: Date & time's rows
// at the end of General, Keyboard's at the end of Controls, and the Show all
// switch at the end of About. A fold whose target is missing (or dynamic)
// leaves the section where it was.
func setArrangeSections(sections []setSection) []setSection {
	find := func(id string) int {
		for i := range sections {
			if sections[i].ID == id {
				return i
			}
		}
		return -1
	}
	drop := map[string]bool{}
	for from, to := range setSectionAliases {
		fi, ti := find(from), find(to)
		if fi < 0 || ti < 0 || sections[fi].Dynamic != nil || sections[ti].Dynamic != nil {
			continue
		}
		rows := make([]setRow, 0, len(sections[ti].Rows)+len(sections[fi].Rows))
		rows = append(rows, sections[ti].Rows...)
		rows = append(rows, sections[fi].Rows...)
		sections[ti].Rows = rows
		drop[from] = true
	}
	if drop["datetime"] {
		if i := find("general"); i >= 0 {
			sections[i].Subtitle = "You, language, the clock, your Groq key and the weather"
		}
	}
	if drop["keyboard"] {
		if i := find("controls"); i >= 0 {
			sections[i].Subtitle = "Buttons, sticks, profiles and the on-screen keyboard"
		}
	}
	if i := find("about"); i >= 0 && sections[i].Dynamic == nil {
		rows := make([]setRow, 0, len(sections[i].Rows)+1)
		rows = append(rows, sections[i].Rows...)
		sections[i].Rows = append(rows, setShowAllRow())
	}
	out := sections[:0]
	for _, s := range sections {
		if !drop[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// setHubFocusFirstOf focuses the first of these rows the open section shows.
func setHubFocusFirstOf(ids ...string) bool {
	for _, id := range ids {
		if setHubFocusRow(id, "") {
			return true
		}
	}
	return false
}
