package main

// hometabs_tools.go — the Tabs Home's Tools tab and its grid selection ring
// switch.
//
// Tools is an optional tab, off until it is switched on in the tab list (L2 on
// Home → Y on Tools). It lists the apps and tools of this setup as the Start
// menu has them — its Screens (Files, Settings, Music, YouTube, the Terminal,
// Notes…), its Tools group (Downloads, Network tools, Screen Share…) and the
// switched-on plugins' buttons — each opened the way the menu opens it
// (homeActivateApp). Nothing here is a second catalogue: a screen the
// config does not have, or a tool this device cannot run, is not in the menu
// and so not on the tab. Home itself and the game screens (the Games tab's)
// are left out.
//
// Grid selection ring (homeTabsGridRing, on unless switched off): the ring
// and glow around the selected tile of the Tabs Home's tile views (grid,
// rows, carousel, compact, backdrop). Off, the selected tile stands out by
// its lift and — with Dim unselected art — by being the one cover in full
// colour. It is a row of the L2 sheet's look rows (homelook.go) and of
// Settings → Display & theme.

import "strings"

// homeTabTools is the Tools tab's ID (homeTabOrder, hometabs_model.go).
const homeTabTools = "tools"

// homeTabsGridRingKey is the Custom switch behind Grid selection ring.
const homeTabsGridRingKey = "homeTabsGridRing"

// homeTabHiddenByDefault reports a tab that stays off the strip until it is
// switched on: one a stored homeTabsShown does not name is hidden, not added
// at the end (parseHomeTabsShown).
func homeTabHiddenByDefault(id string) bool { return id == homeTabTools }

// homeTabsToolsGroups are the Start menu groups the tab lists, in order.
var homeTabsToolsGroups = [...]string{"Screens", "Tools", "Plugins"}

// homeTabsToolsSkip are the Screens rows that are not tools: Home itself and
// the games' own screens (the Start menu lists those under Games already,
// which keeps them off Screens; named here for a config that does not).
var homeTabsToolsSkip = map[string]bool{
	"Main": true, "Games": true, "JukaLand": true,
}

// homeTabsToolsItems is the Tools tab: the Start menu's screens, tools and
// plugin buttons, in the menu's order.
func homeTabsToolsItems(c *Config) []homeTabItem {
	entries, groups := homeAppEntries(c)
	out := make([]homeTabItem, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, want := range homeTabsToolsGroups {
		for i, e := range entries {
			if i >= len(groups) || groups[i] != want || e.name == "" || (e.scene == "" && e.action == nil) {
				continue
			}
			if e.scene != "" && homeTabsToolsSkip[e.scene] {
				continue
			}
			ref := homeAppRefFor(e)
			key := strings.ToLower(ref)
			if seen[key] {
				continue
			}
			seen[key] = true
			glyph := startMenuGlyph(e)
			if glyph == "" {
				glyph = "apps"
			}
			out = append(out, homeTabItem{
				Key: ref, Ref: ref, Title: e.name, Sub: homeAppGroupNote(want), Glyph: glyph, Badge: e.badge,
				Activate: func(c *Config) { homeActivateApp(c, ref) },
			})
		}
	}
	return out
}

// homeTabsGridRingOn reports whether the selected tile wears its ring.
func homeTabsGridRingOn(c *Config) bool { return homeLookBool(c, homeTabsGridRingKey, true) }

// homeTabsTileRing reports whether a tile drawn focused gets the ring and
// glow (hometabs_draw.go tabsPaintTile).
func homeTabsTileRing(focused bool) bool { return focused && homeTabsGridRingOn(appConfig) }

// homeTabsGridRingRow is Display & theme → Grid selection ring, and the L2
// sheet's row (homeLookRows).
func homeTabsGridRingRow() setRow {
	return setRow{
		ID: homeTabsGridRingKey, Label: "Grid selection ring", Kind: setKindToggle,
		Detail:  setStatic("The ring around the selected tile on the Tabs Home; off, the selected cover stands out by its size and colour"),
		Visible: homeTabsOnTabs,
		Get:     homeTabsGridRingOn,
		Set: func(c *Config, on bool) {
			homeTabsWrite(c, homeTabsGridRingKey, boolString(on))
			setToast("Grid selection ring: " + setOnOff(on))
		},
	}
}
