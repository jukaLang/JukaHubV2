package main

// hometabs_start.go — which tab the Tabs Home opens on.
//
// Settings → Theme & look → Start on: "Last used tab" (what Remember last tab
// did when on) or any tab the strip shows, by name. The older switch keeps
// its meaning, so nothing changes for anyone who never touches the new row:
// homeTabsRemember on (the default) is "Last used tab"; off opens the tab
// stored here, and with none stored, Main — exactly what off did before.
// Choosing a tab turns Remember off; choosing Last used turns it on, so the
// L2 sheet's Remember switch and this row never disagree.
//
// Order at start-up: the start-up target (startup_target.go — Home, the
// game switcher, a random or the last game) runs first; this only decides
// which tab Home shows once Home is what opens.

// homeTabsStartKey is the tab Home opens on when Remember is off.
const homeTabsStartKey = "homeTabsStart"

// homeTabsStartLast is the row's stop for "Last used tab".
const homeTabsStartLast = "last"

// homeTabsStartTab is the tab Home opens on among the visible ones: the last
// used when Remember is on and it is still shown, else the chosen tab, else
// Main, else the first tab.
func homeTabsStartTab(c *Config, vis []string) string {
	start := homeTabMain
	if homeTabsRemember(c) {
		if last := homeTabsCustom(c, homeTabLastKey); homeTabIndexIn(vis, last) >= 0 {
			start = last
		}
	} else if chosen := homeTabsCustom(c, homeTabsStartKey); homeTabIndexIn(vis, chosen) >= 0 {
		start = chosen
	}
	if homeTabIndexIn(vis, start) < 0 && len(vis) > 0 {
		start = vis[0]
	}
	return start
}

// homeTabsStartValue is the row's stored stop.
func homeTabsStartValue(c *Config) string {
	if homeTabsRemember(c) {
		return homeTabsStartLast
	}
	if chosen := homeTabsCustom(c, homeTabsStartKey); chosen != "" {
		return chosen
	}
	return homeTabMain
}

// homeTabsStartOptions are Last used, then every tab the strip shows.
func homeTabsStartOptions(c *Config) []setOption {
	vis := homeTabsVisible(c)
	opts := make([]setOption, 0, len(vis)+1)
	opts = append(opts, setOption{Value: homeTabsStartLast, Label: "Last used tab", Note: "Home comes back on the tab you left it on"})
	for _, id := range vis {
		opts = append(opts, setOption{Value: id, Label: homeTabLabel(id)})
	}
	return opts
}

// homeTabsStartLabel is the row's read-out.
func homeTabsStartLabel(c *Config) string {
	v := homeTabsStartValue(c)
	if v == homeTabsStartLast {
		return "Last used tab"
	}
	return homeTabLabel(v)
}

// homeTabsStartRow is Settings → Theme & look → Start on. It stands where the
// Remember last tab switch stood; the switch stays on the L2 sheet.
func homeTabsStartRow() setRow {
	return setRow{
		ID: homeTabsStartKey, Label: "Start on", Kind: setKindChoice,
		Detail:     setStatic("The tab the Tabs Home opens on: the one you left it on, or always the same tab. Start in (a game or the game switcher at power-on) still comes first."),
		Visible:    homeTabsOnTabs,
		Options:    homeTabsStartOptions,
		Sheet:      homeTabsStartOptions,
		SheetValue: homeTabsStartValue,
		SheetTitle: "Start on",
		Current: func(c *Config) int {
			v := homeTabsStartValue(c)
			for i, opt := range homeTabsStartOptions(c) {
				if opt.Value == v {
					return i
				}
			}
			return -1
		},
		Value: homeTabsStartLabel,
		Choose: func(c *Config, value string) {
			if value == homeTabsStartLast {
				homeTabsWrite(c, homeTabsRememberKey, boolString(true))
				homeTabsNoteLast(c, homeTabs.cur)
			} else {
				homeTabsWrite(c, homeTabsStartKey, value)
				homeTabsWrite(c, homeTabsRememberKey, boolString(false))
			}
			setToast("Start on: " + homeTabsStartLabel(c))
		},
	}
}
