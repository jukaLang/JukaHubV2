package main

// aitools_settings.go — Juka's settings tools, through the Settings
// catalogue (setmodel.go): only rows that exist, only switches and choices,
// only values a row offers, applied by the row's own Set/Choose on the main
// thread — exactly what pressing it in Settings does. Keys, tokens, resets
// and Juka's own tool switches are never changed by Juka. Also the three
// switches in Settings → Voice & AI that govern the tools.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func aiSettingTools() []aiTool {
	return []aiTool{
		{
			Name:   "search_settings",
			Desc:   "Find settings by words (e.g. \"brightness\", \"wifi sleep\", \"retroachievements\"): section, id, current value and choices.",
			Params: aiObj(map[string]any{"query": aiStrP("Words to look for")}, "query"),
			Risk:   aiRiskRead,
			Run:    aiSearchSettings,
		},
		{
			Name:   "get_setting",
			Desc:   "One setting by id (from search_settings): value, choices, what it does, and why it is greyed out if it is.",
			Params: aiObj(map[string]any{"id": aiStrP("The setting's id")}, "id"),
			Risk:   aiRiskRead,
			Run:    aiGetSetting,
		},
		{
			Name: "set_setting",
			Desc: "Change one setting by id to one of its choices (on/off for a switch). Only switches and choices; keys, passwords and resets are refused. The user is asked first.",
			Params: aiObj(map[string]any{
				"id":    aiStrP("The setting's id"),
				"value": aiStrP("on/off for a switch, or a choice's value or name"),
			}, "id", "value"),
			Risk: aiRiskChange,
			Run:  aiSetSetting,
		},
		{
			Name: "open_screen",
			Desc: "Offer the user a button under your answer that opens a Settings section (and row) or a screen, so they can see or finish the fix themselves. section/row ids come from search_settings.",
			Params: aiObj(map[string]any{
				"section": aiStrP("A Settings section id"),
				"row":     aiStrP("A setting id inside it"),
				"screen":  aiStrP("Instead: a screen's name (e.g. Wifi, DiskSpace, Emulators, Files)"),
			}),
			Risk: aiRiskRead,
			Run:  aiOpenScreen,
		},
	}
}

// ── The catalogue ────────────────────────────────────────────────────────

// aiRowKey is the id a row is known by (its ID, else its label).
func aiRowKey(r setRow) string {
	if r.ID != "" {
		return r.ID
	}
	return r.Label
}

// aiFindRow finds a visible row by id or label. Main thread.
func aiFindRow(c *Config, id string) (setRow, setSection, bool) {
	id = strings.TrimSpace(id)
	if c == nil || id == "" {
		return setRow{}, setSection{}, false
	}
	for _, s := range setSections() {
		for _, r := range setSectionRows(c, s) {
			if !strings.EqualFold(r.ID, id) && !strings.EqualFold(r.Label, id) {
				continue
			}
			if r.Visible != nil && !r.Visible(c) {
				continue
			}
			return r, s, true
		}
	}
	return setRow{}, setSection{}, false
}

// aiRowValue is what a row shows as its value. Main thread.
func aiRowValue(c *Config, r setRow) string {
	switch r.Kind {
	case setKindToggle:
		if r.Get != nil {
			return setOnOff(r.Get(c))
		}
	case setKindChoice:
		if r.Options != nil && r.Current != nil {
			opts := r.Options(c)
			if i := r.Current(c); i >= 0 && i < len(opts) {
				return opts[i].Label
			}
		}
	}
	if r.Secret {
		return "(hidden)"
	}
	if r.Value != nil {
		return r.Value(c)
	}
	return ""
}

// aiRowChoices lists a choice row's stops as "value (Label)".
func aiRowChoices(c *Config, r setRow) string {
	switch r.Kind {
	case setKindToggle:
		return "on | off"
	case setKindChoice:
		if r.Options == nil {
			return ""
		}
		opts := r.Options(c)
		var parts []string
		for i, o := range opts {
			if i >= 24 {
				parts = append(parts, fmt.Sprintf("… %d more", len(opts)-i))
				break
			}
			if o.Value == o.Label || o.Value == "" {
				parts = append(parts, o.Label)
			} else {
				parts = append(parts, o.Value+" ("+o.Label+")")
			}
		}
		return strings.Join(parts, " | ")
	}
	return ""
}

// aiRowRefused says why Juka may not change a row ("" when it may).
func aiRowRefused(c *Config, r setRow) string {
	switch {
	case r.Kind != setKindToggle && r.Kind != setKindChoice:
		return "it isn't a switch or a choice (open it with open_screen instead)"
	case r.Secret || r.Field != "":
		return "it is typed in, not chosen"
	case r.Disabled != nil && r.Disabled(c):
		return "it is greyed out right now"
	}
	id := strings.ToLower(r.ID)
	switch id {
	case aiToolsSetting, aiChangeSetting, aiShellSetting:
		return "Juka's own permissions are only changed by the user"
	}
	text := strings.ToLower(r.ID + " " + r.Label)
	for _, w := range []string{"api key", "apikey", "token", "password", "groq", "reset", "erase", "factory", "wipe", "format"} {
		if strings.Contains(text, w) {
			return "it is about accounts, keys or erasing data"
		}
	}
	return ""
}

// aiParseOnOff reads a switch value.
func aiParseOnOff(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1", "enable", "enabled":
		return true, true
	case "off", "false", "no", "0", "disable", "disabled":
		return false, true
	}
	return false, false
}

// aiRowTarget resolves the value asked for into the row's own terms: the
// switch state, or the option. Main thread.
func aiRowTarget(c *Config, r setRow, value string) (on bool, opt setOption, err error) {
	if r.Kind == setKindToggle {
		v, ok := aiParseOnOff(value)
		if !ok || r.Set == nil || r.Get == nil {
			return false, opt, errors.New("a switch takes on or off")
		}
		return v, opt, nil
	}
	if r.Options == nil || r.Choose == nil {
		return false, opt, errors.New("that choice can't be set here")
	}
	for _, o := range r.Options(c) {
		if strings.EqualFold(o.Value, value) || strings.EqualFold(o.Label, value) {
			return false, o, nil
		}
	}
	return false, opt, fmt.Errorf("%q is not one of its choices: %s", value, aiRowChoices(c, r))
}

// aiPlanRow validates a change and returns the confirmation line ("" when
// the row already has that value). Main thread.
func aiPlanRow(c *Config, id, value string) (string, error) {
	r, _, ok := aiFindRow(c, id)
	if !ok {
		return "", fmt.Errorf("no setting %q (search_settings finds ids)", id)
	}
	if why := aiRowRefused(c, r); why != "" {
		return "", fmt.Errorf("%s can't be changed by Juka: %s", r.Label, why)
	}
	on, opt, err := aiRowTarget(c, r, value)
	if err != nil {
		return "", fmt.Errorf("%s: %v", r.Label, err)
	}
	from := aiRowValue(c, r)
	to := opt.Label
	if r.Kind == setKindToggle {
		if r.Get(c) == on {
			return "", nil
		}
		to = setOnOff(on)
	} else if strings.EqualFold(from, to) {
		return "", nil
	}
	return fmt.Sprintf("Change “%s” from %s to %s?", r.Label, from, to), nil
}

// aiApplyRow applies a change through the row itself. Main thread.
func aiApplyRow(c *Config, id, value string) (string, error) {
	r, _, ok := aiFindRow(c, id)
	if !ok {
		return "", fmt.Errorf("no setting %q", id)
	}
	if why := aiRowRefused(c, r); why != "" {
		return "", errors.New(why)
	}
	on, opt, err := aiRowTarget(c, r, value)
	if err != nil {
		return "", err
	}
	if r.Kind == setKindToggle {
		r.Set(c, on)
	} else {
		r.Choose(c, opt.Value)
	}
	logInfo("aitools", "setting %s changed at the user's OK", aiRowKey(r))
	return r.Label + " is now " + aiRowValue(c, r) + ".", nil
}

// ── Tools ────────────────────────────────────────────────────────────────

func aiSearchSettings(ctx context.Context, a aiArgs) aiStep {
	words := strings.Fields(strings.ToLower(a.str("query")))
	if len(words) == 0 {
		return aiFail("give words to look for")
	}
	type hit struct {
		score int
		line  string
	}
	var hits []hit
	err := aiOnMain(ctx, func() {
		c := appConfig
		if c == nil {
			return
		}
		for _, s := range setSections() {
			for _, r := range setSectionRows(c, s) {
				if r.Visible != nil && !r.Visible(c) {
					continue
				}
				detail := ""
				if r.Detail != nil {
					detail = r.Detail(c)
				}
				hay := strings.ToLower(r.ID + " " + r.Label + " " + detail + " " + s.Title)
				score := 0
				for _, w := range words {
					if strings.Contains(hay, w) {
						score++
						if strings.Contains(strings.ToLower(r.Label), w) {
							score++
						}
					}
				}
				if score == 0 {
					continue
				}
				line := fmt.Sprintf("%s › %s [id %s, section %s] = %s", s.Title, r.Label, aiRowKey(r), s.ID, aiOr(aiRowValue(c, r), "—"))
				if ch := aiRowChoices(c, r); ch != "" && r.Kind == setKindChoice {
					line += " (choices: " + ch + ")"
				}
				if r.Kind == setKindAction {
					line += " (a button: open_screen shows it)"
				}
				hits = append(hits, hit{score, line})
			}
		}
	})
	if err != nil {
		return aiFail("%v", err)
	}
	if len(hits) == 0 {
		return aiStep{Result: "No setting matches.", Status: "Searched Settings"}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var b strings.Builder
	for i, h := range hits {
		if i >= 12 {
			break
		}
		b.WriteString(h.line + "\n")
	}
	return aiStep{Result: b.String(), Status: "Searched Settings"}
}

func aiGetSetting(ctx context.Context, a aiArgs) aiStep {
	id := a.str("id")
	var out string
	err := aiOnMain(ctx, func() {
		c := appConfig
		r, s, ok := aiFindRow(c, id)
		if !ok {
			out = "Error: no setting " + strconvQuote(id)
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s › %s [id %s]\nValue: %s\n", s.Title, r.Label, aiRowKey(r), aiOr(aiRowValue(c, r), "—"))
		if ch := aiRowChoices(c, r); ch != "" {
			fmt.Fprintf(&b, "Choices: %s\n", ch)
		}
		if r.Detail != nil {
			fmt.Fprintf(&b, "About: %s\n", r.Detail(c))
		}
		if why := aiRowRefused(c, r); why != "" {
			fmt.Fprintf(&b, "Juka can't change it: %s\n", why)
		}
		out = b.String()
	})
	if err != nil {
		return aiFail("%v", err)
	}
	return aiStep{Result: out, Status: "Checked a setting"}
}

func aiSetSetting(ctx context.Context, a aiArgs) aiStep {
	id, value := a.str("id"), a.str("value")
	var confirm string
	var perr error
	err := aiOnMain(ctx, func() { confirm, perr = aiPlanRow(appConfig, id, value) })
	if err != nil {
		return aiFail("%v", err)
	}
	if perr != nil {
		return aiFail("%v", perr)
	}
	if confirm == "" {
		return aiStep{Result: "It is already set that way."}
	}
	return aiStep{
		Confirm: confirm,
		Apply: func(ctx context.Context) string {
			var msg string
			var aerr error
			if err := aiOnMain(ctx, func() { msg, aerr = aiApplyRow(appConfig, id, value) }); err != nil {
				return "Error: " + err.Error()
			}
			if aerr != nil {
				return "Error: " + aerr.Error()
			}
			return msg
		},
	}
}

func aiOpenScreen(ctx context.Context, a aiArgs) aiStep {
	run := aiRunFrom(ctx)
	if run == nil {
		return aiFail("no conversation to add a button to")
	}
	section, row, screen := a.str("section"), a.str("row"), a.str("screen")
	var label string
	var open func(c *Config)
	var problem string
	err := aiOnMain(ctx, func() {
		c := appConfig
		switch {
		case screen != "":
			name := screen
			if strings.EqualFold(name, "wifi") || strings.EqualFold(name, "wi-fi") {
				label, open = "Open Wi-Fi", wifiScrOpen
				return
			}
			if sceneIndexExact(c, name) < 0 {
				problem = "no screen called " + name
				return
			}
			label, open = "Open "+name, setOpenScene(name)
		case section != "" || row != "":
			sid, rid := section, row
			title := ""
			if rid != "" {
				if r, s, ok := aiFindRow(c, rid); ok {
					sid, rid, title = s.ID, aiRowKey(r), s.Title+" › "+r.Label
				}
			}
			idx := setSectionIndex(sid)
			if idx < 0 {
				for i, s := range setSections() {
					if strings.EqualFold(s.Title, sid) {
						idx, sid = i, s.ID
						break
					}
				}
			}
			if idx < 0 {
				problem = "no Settings section " + strconvQuote(section)
				return
			}
			if title == "" {
				title = setSections()[idx].Title
			}
			label = "Open " + title
			open = func(c *Config) { setOpenSettingRow(c, sid, rid, "") }
		default:
			problem = "give a section, a row or a screen"
		}
	})
	if err != nil {
		return aiFail("%v", err)
	}
	if problem != "" {
		return aiFail("%s", problem)
	}
	run.setOpen(label, open)
	return aiStep{Result: "Done: the user gets a “" + label + "” button under your answer.", Status: "Added a shortcut"}
}

// ── Settings → Voice & AI ────────────────────────────────────────────────

func aiStoreToggle(c *Config, key string, on bool, label string) {
	setStoreBool(c, key, on)
	setToast(label + ": " + setOnOff(on))
}

// aiToolsSettingsRows are the three switches behind Juka's tools
// (voiceai_settings.go lists them on the Voice & AI page).
func aiToolsSettingsRows() []setRow {
	return []setRow{
		{
			ID: aiToolsSetting, Label: "Juka can use device tools", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if !aiToolsOn() {
					return "Juka only talks: it can't look at the device"
				}
				return "Juka may look at storage, logs, games, settings and the network to answer. Results go to Groq with your question; offline Juka only looks"
			},
			Get: func(*Config) bool { return aiToolsOn() },
			Set: func(c *Config, on bool) { aiStoreToggle(c, aiToolsSetting, on, "Juka can use device tools") },
		},
		{
			ID: aiChangeSetting, Label: "Let Juka change settings and files", Kind: setKindToggle,
			Detail:  setStatic("Juka asks first, every time: A allows, B says no. Off = Juka only looks"),
			Visible: func(*Config) bool { return aiToolsOn() },
			Get:     func(*Config) bool { return aiChangesOn() },
			Set: func(c *Config, on bool) {
				aiStoreToggle(c, aiChangeSetting, on, "Let Juka change settings and files")
			},
		},
		{
			ID: aiShellSetting, Label: "Let Juka run terminal commands", Kind: setKindToggle,
			Detail:  setStatic("For experts: Juka may propose a shell command; you see the exact command and allow it once"),
			Visible: func(*Config) bool { return aiToolsOn() && aiChangesOn() },
			Get:     func(*Config) bool { return aiShellOn() },
			Set: func(c *Config, on bool) {
				aiStoreToggle(c, aiShellSetting, on, "Let Juka run terminal commands")
			},
		},
	}
}
