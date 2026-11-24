package main

// homefirst.go — where the default Home leads: Resume, Games and Media.
//
// The Tabs Home opens on Recent. On a new handheld Recent had nothing on it
// (no game played, no app opened), so the first thing a new owner saw was an
// empty page and the focus parked on the tab strip. Recent now always leads
// with the three places a handheld is for:
//
//   - Resume — what Continue would resume, a game or a video, as the large
//     first tile (the existing hero; nothing to resume leaves it out);
//   - Games — the library, with how many games it holds; with none yet it is
//     "Add games" and opens addgames.go's screen instead;
//   - Media — music, videos and the rest of Entertainment.
//
// While the library is empty, Recent also offers Add games, and while there
// is no network, Connect to Wi-Fi: on a fresh handheld playing, adding games
// and getting online are each one press from Home. Played games and opened
// apps follow, as before.

import "strings"

// homeFirstGamesItem is the Games tile: the library, or Add games while the
// library has none.
func homeFirstGamesItem(c *Config) homeTabItem {
	lib := homeTabsLibrary()
	it := homeTabItem{Key: "x:games", Title: "Games", Glyph: "games"}
	switch {
	case lib.Total > 0:
		consoles := 0
		for _, s := range lib.Systems {
			if len(s.Games) > 0 {
				consoles++
			}
		}
		it.Sub = pluralizeCount(lib.Total, "game", "games")
		if consoles > 1 {
			it.Sub += " · " + pluralizeCount(consoles, "console", "consoles")
		}
		it.Activate = func(*Config) { openGameLibraryPanel() }
	case !homeTabsLib.ready:
		it.Sub = "Looking for games…"
		it.Activate = func(*Config) { openGameLibraryPanel() }
	default:
		it.Sub = "None yet: add your first games"
		it.Activate = func(c *Config) { addGamesOpen(c) }
	}
	return it
}

// homeFirstMediaItem is the Media tile: the Media screen where this setup
// has one, else the Entertainment tab.
func homeFirstMediaItem(c *Config) homeTabItem {
	if it, ok := homeTabsCard(c, HomeFocusMedia, "Media", "Music, videos and more"); ok {
		// The card's own key: Media opened last is then not listed twice.
		it.Glyph = "media"
		return it
	}
	return homeTabItem{
		Key: "x:media", Title: "Media", Sub: "Music, videos and more", Glyph: "media",
		Activate: func(c *Config) { homeTabsGo(c, homeTabEntertainment, 1) },
	}
}

// homeFirstStepItems are the new handheld's tiles: Add games while the
// library is empty, Connect to Wi-Fi while there is no network.
func homeFirstStepItems(c *Config) []homeTabItem {
	var out []homeTabItem
	if lib := homeTabsLibrary(); homeTabsLib.ready && lib.Total == 0 {
		out = append(out, homeTabItem{
			Key: "x:addgames", Title: "Add games", Sub: "From your phone, a computer or a USB stick", Glyph: "add",
			Activate: func(c *Config) { addGamesOpen(c) },
		})
	}
	if localIPv4() == "" && !IsOnline() {
		out = append(out, homeTabItem{
			Key: "x:wifi", Title: "Connect to Wi-Fi", Sub: "For updates, covers and your phone", Glyph: "wifi",
			Activate: func(c *Config) { wifiScrOpen(c) },
		})
	}
	return out
}

// homeFirstLead puts Games and Media right after Recent's first tile (the
// resume hero) — or first, when there is nothing to resume — and the new
// handheld's tiles after them. It reports whether the first tile should be
// drawn large.
func homeFirstLead(c *Config, items []homeTabItem, hero bool) ([]homeTabItem, bool) {
	lead := []homeTabItem{homeFirstGamesItem(c), homeFirstMediaItem(c)}
	lead = append(lead, homeFirstStepItems(c)...)
	// A tile already on the page (an app opened last that is the same
	// screen) is not listed twice.
	seen := map[string]bool{}
	for _, it := range lead {
		seen[strings.ToLower(it.Key)] = true
	}
	kept := items[:0:0]
	for _, it := range items {
		if !seen[strings.ToLower(it.Key)] {
			kept = append(kept, it)
		}
	}
	if !hero || len(kept) == 0 {
		return append(lead, kept...), true
	}
	out := make([]homeTabItem, 0, len(kept)+len(lead))
	out = append(out, kept[0])
	out = append(out, lead...)
	return append(out, kept[1:]...), true
}
