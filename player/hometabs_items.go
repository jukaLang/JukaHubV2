package main

// hometabs_items.go — what the Tabs Home's own pages hold: Recent, Main and
// Entertainment (Games, Favourites and Collections are the providers',
// hometabs_games.go and hometabs_collections*.go).
//
// An item is only offered where this setup has what it opens: a Start menu
// entry (an app ref, opened the way the menu opens it, homeActivateApp) or a
// built-in Home card whose screen exists (homeTabsOpenCard, which opens it
// the way the card does). Refs follow the homeTiles grammar, so a
// favourite or a collection means the same thing everywhere.

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// homeTabsMenu is the Start menu's entries by their app ref (lower case),
// read once per page build.
type homeTabsMenu map[string]startMenuEntry

// homeTabsMenuFor reads the Start menu.
func homeTabsMenuFor(c *Config) homeTabsMenu {
	m := homeTabsMenu{}
	entries, _ := homeAppEntries(c)
	for _, e := range entries {
		m[strings.ToLower(homeAppRefFor(e))] = e
	}
	return m
}

// app is the item for the Start menu entry ref names; false when this setup
// has none. Empty title and glyph take the entry's.
func (m homeTabsMenu) app(ref, title, sub, glyph string) (homeTabItem, bool) {
	e, ok := m[strings.ToLower(ref)]
	if !ok {
		return homeTabItem{}, false
	}
	if title == "" {
		title = e.name
	}
	if glyph == "" {
		glyph = startMenuGlyph(e)
	}
	if glyph == "" {
		glyph = "apps"
	}
	open := homeAppRefFor(e)
	return homeTabItem{
		Key: open, Ref: open, Title: title, Sub: sub, Glyph: glyph, Badge: e.badge,
		Activate: func(c *Config) { homeActivateApp(c, open) },
	}, true
}

// homeTabsCard is the item for a built-in Home card; false when the screen
// it opens is not part of this setup. Empty title and line take the card's.
func homeTabsCard(c *Config, id HomeFocusID, title, sub string) (homeTabItem, bool) {
	name, note, glyph := homeCardInfo(id)
	if title == "" {
		title = name
	}
	if sub == "" {
		sub = note
	}
	switch id {
	case HomeFocusBrowser, HomeFocusTools, HomeFocusEmulator, HomeFocusContinue:
		// Opened by their own functions, on every setup.
	default:
		if c == nil || sceneIndexExact(c, homeSceneTarget(c, id)) < 0 {
			return homeTabItem{}, false
		}
	}
	ref := string(id)
	return homeTabItem{
		Key: ref, Ref: ref, Title: title, Sub: sub, Glyph: glyph,
		Activate: func(c *Config) { homeTabsOpenCard(c, id) },
	}, true
}

// homeTabsOpenCard opens what a built-in Home card opens, the way A on the
// card does (homeActivate), and notes it as used.
func homeTabsOpenCard(c *Config, id HomeFocusID) {
	if c == nil {
		return
	}
	if id != HomeFocusContinue {
		homeConsoleRecordApp(string(id))
	}
	switch id {
	case HomeFocusContinue:
		if homeContinueResume(c) {
			return
		}
		if homeContinueStartsGames() {
			openGameLibraryPanel()
			return
		}
		id = HomeFocusMedia
	case HomeFocusEmulator:
		openGameLibraryPanel()
		return
	case HomeFocusBrowser:
		openBrowserHome(c)
		return
	case HomeFocusTools:
		toolsHubOpen(c)
		return
	}
	target := homeSceneTarget(c, id)
	idx := sceneIndexExact(c, target)
	if target == "" || idx < 0 {
		title, _, _ := homeCardInfo(id)
		ShowToast(title+" is not part of this setup", ToastKindInfo)
		return
	}
	if reason, blocked := offlineSceneBlock(target); blocked {
		showToast(reason, ToastWarn())
		return
	}
	if idx != currentSceneIndex {
		changeSceneTo(c, idx)
	}
}

// homeTabsOpenScene is an item's press that opens a screen by name.
func homeTabsOpenScene(name string) func(*Config) {
	return func(c *Config) {
		if c == nil {
			return
		}
		idx := sceneIndexExact(c, name)
		if idx < 0 {
			ShowToast(sceneDisplayName(name)+" is not part of this setup", ToastKindInfo)
			return
		}
		if idx != currentSceneIndex {
			changeSceneTo(c, idx)
		}
	}
}

// ── Recent ───────────────────────────────────────────────────────────────

// homeTabsRecentGames and homeTabsRecentApps cap what Recent lists.
const (
	homeTabsRecentGames = 12
	homeTabsRecentApps  = 8
)

// homeTabsRecentItems is Recent: today's challenge whenever the Continue card
// of the other layouts would show it (game_daily.go), what Continue resumes
// (unless that is a game, which leads the games anyway; beside the challenge,
// never pushed out by it), the games played last with their covers, then the
// apps opened last. The first item — the challenge, a game or the Continue
// item — is the row view's hero.
func homeTabsRecentItems(c *Config) ([]homeTabItem, bool) {
	out := make([]homeTabItem, 0, homeTabsRecentGames+homeTabsRecentApps+2)
	resume, hasResume := homeContinueItem()
	if ch, daily := homeContinueDaily(resume, hasResume); daily {
		out = append(out, homeTabsDailyItem(ch))
	}
	// The card of the other layouts shows the challenge in place of a stale
	// item; here that item keeps its own place. A resumes the item shown, never
	// the challenge (whose pick may land between this build and the press, when
	// homeContinueResume would start it instead).
	if hasResume && resume.Type != "game" {
		item := resume
		out = append(out, homeTabsContinueItem(homeContinueStateFor(item), func(c *Config) {
			homeContinueInvalidate()
			homePlayRecent(c, item)
		}))
	}
	now := time.Now()
	for i, g := range recentGames() {
		if i >= homeTabsRecentGames {
			break
		}
		if strings.TrimSpace(g.Path) == "" {
			continue
		}
		title := strings.TrimSpace(g.Title)
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path))
		}
		sub := g.System
		// TrimUI boots before its clock is set: a time from before that, or
		// one ahead of the clock, says nothing true.
		if t := g.PlayedAt; !t.IsZero() {
			if d := now.Sub(t); d >= 0 && d < 5*365*24*time.Hour {
				if sub != "" {
					sub += " · "
				}
				sub += timeAgo(t)
			}
		}
		ref := homeTabRefGame + g.Path
		game := g
		it := homeTabItem{
			Key: ref, Ref: ref, Title: title, Sub: sub, Glyph: "emulator",
			Activate: func(*Config) { playRecentGame(game) },
		}
		if job := homeSwArtJobFor(g); job.Key != "" && job.File != "" {
			it.Art = func() *sdl.Texture { return homeTabsArt(job) }
		}
		out = append(out, it)
	}
	hero := len(out) > 0

	homeConsoleRecents.Lock()
	homeConsoleRecentLoadLocked()
	uses := append([]homeConsoleUse(nil), homeConsoleRecents.items...)
	homeConsoleRecents.Unlock()
	m := homeTabsMenuFor(c)
	seen := make(map[string]bool, homeTabsRecentApps)
	apps := 0
	for _, u := range uses {
		if apps >= homeTabsRecentApps {
			break
		}
		if u.GamePath != "" || u.Ref == "" || u.Ref == string(HomeFocusContinue) {
			continue
		}
		var it homeTabItem
		ok := false
		switch homeTileKindOf(u.Ref) {
		case homeTileCard:
			it, ok = homeTabsCard(c, HomeFocusID(u.Ref), "", "")
		case homeTileApp:
			it, ok = m.app(u.Ref, "", "", "")
		}
		if !ok || seen[strings.ToLower(it.Key)] {
			continue
		}
		seen[strings.ToLower(it.Key)] = true
		out = append(out, it)
		apps++
	}
	return out, hero
}

// homeTabsDailyItem is today's challenge on Recent: the game with its cover
// and the minutes still to play; A starts it, as Play on the Continue card
// does (homeContinueResume).
func homeTabsDailyItem(ch dailyChallenge) homeTabItem {
	game := ch.dailyGame()
	title := strings.TrimSpace(ch.Title)
	if title == "" {
		title = "Daily challenge"
	}
	it := homeTabItem{
		Key: "x:daily", Title: title, Sub: ch.dailyRecentLine(), Glyph: "emulator",
		Activate: func(*Config) {
			homeContinueInvalidate()
			playRecentGame(game)
		},
	}
	if job := homeSwArtJobFor(game); job.Key != "" && job.File != "" {
		it.Art = func() *sdl.Texture { return homeTabsArt(job) }
	}
	return it
}

// homeTabsContinueItem is what Continue resumes, as Recent lists it: the
// card's title over its chip ("Continue watching"); open resumes it.
func homeTabsContinueItem(st HomeContinueState, open func(*Config)) homeTabItem {
	title := strings.TrimSpace(st.Title)
	if title == "" {
		title = "Continue"
	}
	sub := strings.TrimSpace(st.Chip)
	if sub == "" {
		sub = "Continue"
	}
	return homeTabItem{Key: "x:continue", Title: title, Sub: sub, Glyph: "continue", Activate: open}
}

// ── Main ─────────────────────────────────────────────────────────────────

// homeTabsMainItems is Main: the apps the hub is for. Settings, both kinds
// of achievements (JukaHub's Trophies; RetroAchievements where a card can
// hold its login), Chat, Mail, the Browser, the Store (a folder), Tools, Files,
// and the radios — Bluetooth where it can pair, Wi-Fi where the picker can
// do something — plus the phone's pairing code while the companion has one:
// on this Home the top bar has no room for the code the other layouts show.
func homeTabsMainItems(c *Config) []homeTabItem {
	m := homeTabsMenuFor(c)
	out := make([]homeTabItem, 0, 14)
	add := func(it homeTabItem, ok bool) {
		if ok {
			out = append(out, it)
		}
	}
	add(homeTabsCard(c, HomeFocusSettings, "", ""))
	add(m.app(homeTileScenePrefix+playtimeSceneName, "Trophies", "JukaHub achievements", "trophy"))
	add(m.app(homeTileActionPrefix+"RetroAchievements", "", "RetroArch achievements", "trophy"))
	add(homeTabsCard(c, HomeFocusChat, "", ""))
	add(m.app(homeTileActionPrefix+mailcalAppEntryName, "", "Your inbox", "mail"))
	add(homeTabsCard(c, HomeFocusBrowser, "", ""))
	out = append(out, homeTabItem{
		Key: "x:store", Title: "Store", Sub: "Apps and downloads", Glyph: "packages",
		Folder: true, FolderView: "store", Children: homeTabsStoreItems,
	})
	add(homeTabsCard(c, HomeFocusTools, "", ""))
	add(homeTabsCard(c, HomeFocusFiles, "", ""))
	add(m.app(homeTileActionPrefix+"Bluetooth", "", "Headphones and controllers", "bluetooth"))
	add(m.app(homeTileActionPrefix+"Wi-Fi Networks", "Wi-Fi", "Networks and hotspots", "wifi"))
	if len(companion.qr) > 0 {
		out = append(out, homeTabItem{
			Key: "x:phone", Title: "Phone", Sub: "Pair the phone companion", Glyph: "qr",
			Activate: func(*Config) { homeShowPairingQR() },
		})
	}
	return out
}

// homeTabsStoreItems is the Store folder: everything that installs or
// downloads something.
func homeTabsStoreItems(c *Config) []homeTabItem {
	m := homeTabsMenuFor(c)
	out := make([]homeTabItem, 0, 8)
	add := func(it homeTabItem, ok bool) {
		if ok {
			out = append(out, it)
		}
	}
	add(m.app(homeTileActionPrefix+apkStoreEntryName, "Android apps", "APK Store", ""))
	add(homeTabsCard(c, HomeFocusPackages, "", "Apps, games and emulators"))
	add(m.app(homeTileActionPrefix+portMasterEntryName, "PortMaster", "Ports for the handheld", ""))
	add(m.app(homeTileActionPrefix+homebrewEntryName, "", "New games from itch.io", ""))
	add(m.app(homeTileActionPrefix+windowsAppsEntryName, "", "Programs and installers", ""))
	add(m.app(homeTileActionPrefix+"Downloads", "", "Queue and saved files", ""))
	if c != nil && sceneIndexExact(c, "ThemePresets") >= 0 {
		out = append(out, homeTabItem{
			Key: "x:themes", Title: "Themes", Sub: "Colours, consoles and experience packs", Glyph: "palette",
			Activate: homeTabsOpenScene("ThemePresets"),
		})
	}
	add(m.app(homeTileActionPrefix+"Wallpapers", "", "Pictures under the glass", "image"))
	return out
}

// ── Entertainment ────────────────────────────────────────────────────────

// homeTabsEntertainmentItems is Entertainment: YouTube, its Shorts, Live TV,
// what was downloaded to watch or listen to offline, the music player,
// podcasts and the game clips the emulators recorded. "Downloaded" opens the
// Downloads panel on its Saved tab — the one list that holds both the videos
// and the tracks saved for offline, each playable with A.
func homeTabsEntertainmentItems(c *Config) []homeTabItem {
	m := homeTabsMenuFor(c)
	out := make([]homeTabItem, 0, 8)
	add := func(it homeTabItem, ok bool) {
		if ok {
			out = append(out, it)
		}
	}
	add(m.app(homeTileScenePrefix+"Tube", "YouTube", "Videos and channels", "youtube"))
	add(m.app(homeTileScenePrefix+"Shorts", "", "Short videos", ""))
	add(m.app(homeTileScenePrefix+"LiveTV", "Live TV", "Channels to watch now", "tv"))
	if it, ok := m.app(homeTileActionPrefix+"Downloads", "Downloaded", "Saved videos and music", "download"); ok {
		it.Key = "x:downloaded"
		it.Activate = func(*Config) {
			openDownloadsPanel()
			downloadPanelTab(downloadTabSaved)
		}
		out = append(out, it)
	}
	add(m.app(homeTileScenePrefix+"MusicPlayer", "Music", "Your songs and playlists", "music"))
	add(m.app(homeTileScenePrefix+"Podcasts", "", "Shows and episodes", ""))
	add(m.app(homeTileScenePrefix+"Recordings", "Game clips", "Recorded gameplay", "camera"))
	return out
}
