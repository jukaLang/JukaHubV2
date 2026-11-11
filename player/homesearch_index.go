package main

// homesearch_index.go — what the Home search finds, and in which order.
//
// The search sheet (globalsearch.go drives it, homesearch_render.go draws it)
// looks in five places and shows them as groups, in this order:
//
//   - Screens & settings: every screen, tool and settings page, by title and by
//     the words people use for it — "wifi" finds the Wi-Fi screen, "ftp" the
//     Network tools, "theme" the theme presets. The Start menu's own list is
//     folded in, so a screen added there is searchable without an edit here.
//   - Apps & games: the Apps launcher's entries (built-in ones carry the Juka
//     badge), recently played games and the ROM library.
//   - Files: recently opened files plus a bounded index of the media, book and
//     ROM folders, built by a background scan.
//   - Media history: the Continue timeline — videos, podcasts, games, music.
//   - Web: "Search the web for '…'" in the Browser (and "Open …" for an
//     address), dimmed with "Needs internet" while the device is offline.
//
// Matching is case-insensitive and word-aware, and punctuation is ignored, so
// "wi fi", "wifi" and "Wi-Fi" are the same query. Nothing in this file draws:
// the ranking is tested without a renderer.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The result groups, in display order.
const (
	homeSearchGroupRecent  = "Recent searches"
	homeSearchGroupScreens = "Screens & settings"
	homeSearchGroupApps    = "Apps & games"
	homeSearchGroupFiles   = "Files"
	homeSearchGroupMedia   = "Media history"
	homeSearchGroupWeb     = "Web"
)

// Per-group caps keep the sheet one screen of choices, not a dump of the index.
const (
	homeSearchMaxScreens = 6
	homeSearchMaxApps    = 6
	homeSearchMaxFiles   = 6
	homeSearchMaxMedia   = 4
	homeSearchMaxRecent  = 5
)

// homeSearchNeedsInternet is the badge a row carries when it cannot work
// offline; the row is dimmed and a press explains instead of opening.
const homeSearchNeedsInternet = "Needs internet"

// homeSearchQuery is the typed text, folded once per keystroke.
type homeSearchQuery struct {
	raw     string
	text    string   // lower case, punctuation as spaces, single-spaced
	compact string   // text without spaces: "wi fi" and "wifi" meet here
	words   []string // the words of text
}

func newHomeSearchQuery(raw string) homeSearchQuery {
	text := homeSearchFold(raw)
	return homeSearchQuery{
		raw:     strings.TrimSpace(raw),
		text:    text,
		compact: strings.ReplaceAll(text, " ", ""),
		words:   strings.Fields(text),
	}
}

func (q homeSearchQuery) empty() bool { return q.compact == "" }

// homeSearchFold lower-cases s, turns punctuation into spaces and collapses
// runs of spaces, so titles and queries compare on their words.
func homeSearchFold(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '/', '\\', '.', ',', ':', ';', '(', ')', '[', ']', '{', '}',
			'&', '+', '\'', '"', '!', '?', '|', '·', '—', '–', '\t':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// homeSearchWordsMatch reports whether every query word starts a word of the
// candidate: "dark mo" matches "dark mode", "set gen" matches "General
// settings".
func homeSearchWordsMatch(words, query []string) bool {
	if len(query) == 0 {
		return false
	}
	for _, q := range query {
		found := false
		for _, w := range words {
			if strings.HasPrefix(w, q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// homeSearchInitials is the first letter of every word: "gs" for "General
// settings", so a two-letter query can reach a long title.
func homeSearchInitials(words []string) string {
	var sb strings.Builder
	for _, w := range words {
		for _, r := range w {
			sb.WriteRune(r)
			break
		}
	}
	return sb.String()
}

// homeSearchMatch scores a title (and its synonyms) against the query; 0 means
// no match. The ladder: exact title, title prefix, every query word starting a
// title word, an exact synonym, synonym word prefixes, a substring of the
// title, a substring of the synonyms, and finally the title's initials.
func homeSearchMatch(q homeSearchQuery, title, keys string) int {
	if q.empty() {
		return 0
	}
	t := homeSearchFold(title)
	if t == "" {
		return 0
	}
	tc := strings.ReplaceAll(t, " ", "")
	switch {
	case t == q.text || tc == q.compact:
		return 1000
	case strings.HasPrefix(t, q.text) || strings.HasPrefix(tc, q.compact):
		return 800
	}
	titleWords := strings.Fields(t)
	if homeSearchWordsMatch(titleWords, q.words) {
		return 600
	}
	k := homeSearchFold(keys)
	keyWords := strings.Fields(k)
	for _, w := range keyWords {
		if w == q.text || w == q.compact {
			return 480
		}
	}
	if homeSearchWordsMatch(keyWords, q.words) {
		return 380
	}
	if len(q.compact) >= 2 && strings.Contains(tc, q.compact) {
		return 260
	}
	if len(q.compact) >= 3 && strings.Contains(strings.ReplaceAll(k, " ", ""), q.compact) {
		return 160
	}
	if len(q.compact) >= 2 && len(titleWords) >= 2 && strings.HasPrefix(homeSearchInitials(titleWords), q.compact) {
		return 140
	}
	return 0
}

// ── Screens & settings ───────────────────────────────────────────────────

// homeSearchEntry is one searchable screen. scene names a config scene (the
// entry is left out when the config has no such scene); open is used instead
// when the screen is opened by a function (a Go-owned scene or a panel).
type homeSearchEntry struct {
	title string
	where string
	icon  string
	keys  string
	scene string
	open  func(*Config)
}

// homeSearchCatalog is the hand-kept part of the screen index: the titles the
// app uses and the words people search for them by. Scene-backed entries need
// the scene in the config; function-backed entries always resolve. It is built
// by a function rather than held in a package variable: the openers reach most
// of the app, and a package-level table of them would turn any future path from
// one of those screens back into search into an initialization cycle.
func homeSearchCatalog() []homeSearchEntry {
	return []homeSearchEntry{
		{title: "Wi-Fi", where: "Network", icon: "wifi", keys: "wifi wlan wireless network internet hotspot ssid connect password router", open: wifiScrOpen},
		{title: "Pair a controller", where: "Network", icon: "bluetooth", keys: "bluetooth devices pair pairing controller gamepad pad headphones earbuds headset dualsense dualshock xbox 8bitdo", open: btScreenOpen},
		{title: "Network tools", where: "Tools", icon: "network", keys: "ftp sftp ssh telnet ping dns port scan traceroute whois ip address server lan http https curl tls certificate headers", open: netToolsOpen},
		{title: "Tools", where: "Tools", icon: "tools", keys: "tools utilities misc system", open: toolsHubOpen},
		{title: "Settings", where: "Settings", icon: "settings", keys: "settings preferences options configuration setup", scene: "Settings"},
		{title: "General settings", where: "Settings", icon: "settings", keys: "general name username language sleep timeout power", scene: "SettingsGeneral"},
		{title: "Date & time", where: "Settings", icon: "clock", keys: "date time clock 12 hour 24 hour am pm time zone timezone ntp network time sync date format calendar", open: dateTimeSettingsOpen},
		{title: "Appearance", where: "Settings", icon: "palette", keys: "appearance look design colors colours accent dark light mode wallpaper background motion animations", scene: "SettingsAppearance"},
		{title: "Theme presets", where: "Settings", icon: "theme", keys: "theme themes presets skins colors colours dark mode light mode", scene: "ThemePresets"},
		{title: "Plugins", where: "Settings", icon: "plugin", keys: "plugins extensions addons add ons mods", scene: "Plugins"},
		{title: "Update and repair", where: "Settings", icon: "update", keys: "update upgrade patch repair version release reinstall", scene: "Patch"},
		{title: "About", where: "Settings", icon: "info", keys: "about version credits license build", scene: "About"},
		{title: "Keyboard settings", where: "Settings", icon: "keyboard", keys: "keyboard languages layout typing suggestions autocorrect voice dictation", open: vkOpenSettings},
		{title: "Accessibility", where: "Settings", icon: "user", keys: "accessibility text size font size large text bigger contrast reduce motion remap buttons", open: openAccessibilityPanel},
		{title: "Quick Settings", where: "Device", icon: "settings", keys: "quick settings brightness volume toggles airplane", open: func(*Config) { qsToggleOpen() }},
		{title: "Wallpapers", where: "Device", icon: "image", keys: "wallpaper wallpapers background picture photo lock screen", open: func(*Config) { wpOpen = true; initWallpaperPicker() }},
		{title: "Audio", where: "Device", icon: "music", keys: "audio sound output equalizer eq speaker headphones volume spatial surround virtual 3d hrtf", open: openMusicSettingsPanel},
		{title: "Media", where: "Media", icon: "media", keys: "media video videos youtube watch movies search", scene: "Tube"},
		{title: "Music", where: "Media", icon: "music", keys: "music songs audio mp3 flac player playlist albums", open: func(c *Config) { openAudioPlayerBrowser(c, musicDefaultDir(c)) }},
		{title: "Live TV", where: "Media", icon: "video", keys: "live tv television channels iptv", scene: "LiveTV"},
		{title: "Streams", where: "Media", icon: "link", keys: "streams stream iptv m3u playlist ip", scene: "IPStream"},
		{title: "Podcasts", where: "Media", icon: "mic", keys: "podcasts podcast episodes shows rss", scene: "Podcasts"},
		{title: "Shorts", where: "Media", icon: "video", keys: "shorts short videos reels clips", scene: "Shorts"},
		{title: "Downloads", where: "Media", icon: "download", keys: "downloads downloaded offline saved videos queue", open: mediaOpenDownloads},
		{title: "Favorites", where: "Media", icon: "favorites", keys: "favorites favourites saved bookmarks pinned", scene: "Favorites"},
		{title: "Recordings", where: "Media", icon: "camera", keys: "recordings captures clips screen recording", scene: "Recordings"},
		{title: "Files", where: "Storage", icon: "files", keys: "files file manager explorer folders sd card storage documents", scene: "FileExplorer"},
		{title: "Storage", where: "Tools", icon: "disk", keys: "storage disk space sd card free space usage full", scene: "DiskSpace"},
		{title: "Hardware", where: "Tools", icon: "hardware", keys: "hardware cpu gpu memory ram battery temperature device info specs", scene: "Hardware"},
		{title: "Speed test", where: "Tools", icon: "speed", keys: "speed test speedtest network speed bandwidth latency", scene: "NetSpeed"},
		{title: "Benchmark", where: "Tools", icon: "benchmark", keys: "benchmark performance score cpu test", scene: "Benchmark"},
		{title: "Logs", where: "Tools", icon: "logs", keys: "logs log export diagnostics debug", scene: "LogExporter"},
		{title: "Terminal", where: "Tools", icon: "terminal", keys: "terminal shell command line console bash cmd prompt", scene: "Terminal"},
		{title: "Scheduled tasks", where: "Tools", icon: "schedule", keys: "scheduled tasks cron schedule startup timer automation", open: schedOpen},
		{title: "Watch Party", where: "Tools", icon: "party", keys: "watch party together sync friends share room", open: partyOpenScreen},
		{title: "Toolbox", where: "Tools", icon: "toolbox", keys: "toolbox command line tools install binaries", open: tbxOpenScreen},
		{title: "Audit toolkit", where: "Tools", icon: "audit", keys: "audit security scan pentest nmap", open: auditOpenScreen},
		{title: "Security & trust", where: "Tools", icon: "shield", keys: "security trust privacy signatures permissions safety", open: sectrustOpenScreen},
		{title: "Report a bug", where: "Tools", icon: "bug", keys: "report bug feedback issue crash problem", open: bugrepOpenScreen},
		{title: "Weather", where: "Tools", icon: "sun", keys: "weather forecast temperature rain", scene: "Weather"},
		{title: "Alarm", where: "Clock", icon: "alarm", keys: "alarm alarms clock timer wake up", open: func(c *Config) { openAlarmScene(c) }},
		{title: "Converter", where: "Tools", icon: "calculator", keys: "unit converter convert units calculator math", scene: "UnitConverter"},
		{title: "Tickers", where: "Tools", icon: "list", keys: "tickers stocks crypto finance market prices", scene: "Tickers"},
		{title: "Chat", where: "Apps", icon: "chat", keys: "chat messages ai assistant discord groq", scene: "Chat"},
		{title: "Browser", where: "Apps", icon: "browser", keys: "browser web internet www website pages", open: openBrowserHome},
		{title: "Browser settings", where: "Settings", icon: "browser", keys: "browser search engine default engine home page homepage start page google duckduckgo bing startpage", open: brwOpenSettings},
		{title: "JukaLand", where: "Games", icon: "gamepad", keys: "jukaland game sandbox craft blocks", scene: "JukaLand"},
		{title: "Apps", where: "Apps", icon: "apps", keys: "apps applications launcher programs", scene: "Apps"},
		{title: "Packages", where: "Apps", icon: "packages", keys: "packages store install updates repository", scene: "Packages"},
		{title: "Emulators", where: "Games", icon: "emulator", keys: "emulators emulator retroarch cores consoles", open: func(*Config) { openEmulatorPanel() }},
		{title: "Games Library", where: "Games", icon: "gamepad", keys: "games game library roms play", open: func(*Config) { openGameLibraryPanel() }},
		{title: "PS Remote Play", where: "Games", icon: "gamepad", keys: "ps5 ps4 playstation remote play chiaki stream console", open: func(*Config) { openChiakiPanel() }},
		{title: "Phone companion", where: "Device", icon: "phone", keys: "phone companion qr pair remote control", open: func(*Config) { openCompanionOverlay() }},
		{title: "Screen Share", where: "Device", icon: "devices", keys: "screen share sharing cast mirror stream tv hdmi watch broadcast remote cast", open: openCastPanel},
		{title: "Trophies", where: "Games", icon: "trophy", keys: "trophies achievements playtime stats", scene: playtimeSceneName},
		{title: "Smart Home", where: "Device", icon: "home", keys: "smart home lights home assistant", open: openSmartHomePanel},
		{title: "Quit JukaHub", where: "Device", icon: "exit", keys: "exit quit power off shutdown close", open: func(*Config) { requestAppExit() }},
	}
}

// homeSearchSceneIcon picks the shared glyph for a screen the catalog does
// not describe (a Start menu row or a config scene).
func homeSearchSceneIcon(scene, title string) string {
	switch scene {
	case "Tube":
		return "media"
	case "IPStream":
		return "link"
	case "FileExplorer":
		return "files"
	case "MusicPlayer":
		return "music"
	case "LiveTV", "Shorts":
		return "video"
	case "Podcasts":
		return "mic"
	case "Recordings":
		return "camera"
	case "Misc", "Tools":
		return "tools"
	case "Apps":
		return "apps"
	case "Terminal":
		return "terminal"
	case "Cron":
		return "schedule"
	case "NetSpeed":
		return "speed"
	case "Benchmark":
		return "benchmark"
	case "DiskSpace":
		return "disk"
	case "Weather":
		return "sun"
	case "Hardware":
		return "hardware"
	case "LogExporter":
		return "logs"
	case "Favorites":
		return "favorites"
	case "UnitConverter", "Calculator":
		return "calculator"
	case "Packages":
		return "packages"
	case "Chat":
		return "chat"
	case "CanvasSandbox":
		return "code"
	case "JukaLand", "Games":
		return "gamepad"
	case "TextBrowser", "Dictionary":
		return "text"
	case "SettingsAppearance":
		return "palette"
	case "ThemePresets":
		return "theme"
	case "Plugins":
		return "plugin"
	case "Patch":
		return "update"
	case "About":
		return "info"
	case "Browser":
		return "browser"
	case "Alarm":
		return "alarm"
	case "Notes":
		return "edit"
	case "Pomodoro":
		return "clock"
	case "Dashboard":
		return "grid"
	case "PDFReader":
		return "pdf"
	}
	if strings.HasPrefix(scene, "Settings") {
		return "settings"
	}
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "screenshot"):
		return "camera"
	case strings.Contains(t, "sleep"), strings.Contains(t, "ambient"):
		return "moon"
	case strings.Contains(t, "save"):
		return "backup"
	case strings.Contains(t, "achievement"), strings.Contains(t, "trophy"):
		return "trophy"
	case strings.Contains(t, "cast"):
		return "devices"
	case strings.Contains(t, "curation"):
		return "star"
	case strings.Contains(t, "download"):
		return "download"
	case strings.Contains(t, "game"), strings.Contains(t, "port"), strings.Contains(t, "calibration"):
		return "gamepad"
	case strings.Contains(t, "wi-fi"), strings.Contains(t, "wifi"):
		return "wifi"
	case strings.Contains(t, "quit"), strings.Contains(t, "exit"):
		return "exit"
	}
	return "grid"
}

// homeSearchScreen is one resolved screen row source: a catalog entry or a
// Start menu row, with the action already bound.
type homeSearchScreen struct {
	title  string
	where  string
	icon   string
	keys   string
	scene  string
	action func(*Config)
}

// homeSearchScreens builds the screen index for this config: the catalog's
// entries whose screen exists, then every Start menu row and config scene the
// catalog does not already name. It runs once when the sheet opens, not per
// keystroke (the Start menu probes the card for a couple of its rows).
func homeSearchScreens(config *Config) []homeSearchScreen {
	catalog := homeSearchCatalog()
	out := make([]homeSearchScreen, 0, len(catalog)+160)
	seen := map[string]bool{}
	add := func(s homeSearchScreen) {
		key := homeSearchFold(s.title)
		if key == "" || seen[key] || s.action == nil {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	// Settings rows come first: a row whose label the catalog already uses
	// ("Date & time") should open that row, not merely the section above it.
	// The keys carry the id and the detail line too, so "vibration" or "button
	// hints" finds the switch whose own label does not contain the word.
	if config != nil && sceneIndexExact(config, "Settings") >= 0 {
		rowSeen := map[string]bool{}
		for _, section := range setSections() {
			for _, row := range setSectionRows(config, section) {
				label := strings.TrimSpace(row.Label)
				if label == "" || (row.Visible != nil && !row.Visible(config)) {
					continue
				}
				key := "row:" + section.ID + ":" + row.ID + ":" + label
				if rowSeen[key] {
					continue
				}
				rowSeen[key] = true
				detail := ""
				if row.Detail != nil {
					detail = row.Detail(config)
				}
				sectionID, rowID, rowLabel := section.ID, row.ID, label
				out = append(out, homeSearchScreen{
					title: label,
					where: "Settings · " + setOr(section.Title, section.ID),
					icon:  setOr(section.Glyph, "settings"),
					keys:  strings.TrimSpace(strings.Join([]string{row.ID, detail, section.Title, section.Subtitle}, " ")),
					action: func(c *Config) {
						setOpenSettingRow(c, sectionID, rowID, rowLabel)
					},
				})
			}
		}
		for _, s := range out {
			seen[homeSearchFold(s.title)] = true
		}
	}
	for _, e := range catalog {
		switch {
		case e.open != nil:
			add(homeSearchScreen{title: e.title, where: e.where, icon: e.icon, keys: e.keys, scene: e.scene, action: e.open})
		case e.scene != "" && config != nil && sceneIndexExact(config, e.scene) >= 0:
			add(homeSearchScreen{title: e.title, where: e.where, icon: e.icon, keys: e.keys, scene: e.scene, action: homeSearchSceneAction(e.scene)})
		}
		if e.scene != "" {
			// A config scene the catalog covers must not come back under its raw
			// display name from the Start menu ("Tasks", "Network", "Text").
			seen[homeSearchFold(sceneDisplayName(e.scene))] = true
		}
	}
	if config == nil {
		return out
	}
	for _, app := range getStartMenuApps(config) {
		s := homeSearchScreen{title: app.name, where: "Start menu", icon: homeSearchSceneIcon(app.scene, app.name), scene: app.scene}
		if app.action != nil {
			s.action = app.action
		} else if app.scene != "" {
			s.action = homeSearchSceneAction(app.scene)
		}
		if app.scene != "" {
			for _, sc := range config.Scenes {
				if sc.Name == app.scene {
					s.keys = sc.Name + " " + sc.Description
					break
				}
			}
		}
		add(s)
	}
	return out
}

// homeSearchSceneAction opens a config scene by name.
func homeSearchSceneAction(scene string) func(*Config) {
	return func(c *Config) {
		if c == nil {
			return
		}
		if idx := sceneIndexExact(c, scene); idx >= 0 {
			changeSceneTo(c, idx)
		}
	}
}

// homeSearchOfflineReason is why a screen cannot work right now, or "" when it
// can: the same classification the Start menu and the Home tiles dim by.
func homeSearchOfflineReason(scene, title string, online bool) string {
	if online {
		return ""
	}
	return offlineReasonFor(scene + " " + title)
}

// ── Background index (apps, ROMs, files) ─────────────────────────────────

// homeSearchFile is one indexed file or folder.
type homeSearchFile struct {
	Name    string
	Path    string
	Parent  string // the folder it sits in, shown as the row's subtitle
	IsDir   bool
	fold    string
	compact string
}

// homeSearchIndexState is what the background scans found. The scan goroutines
// write it under mu; the sheet reads a copy. gen moves whenever a scan
// publishes, so an open sheet re-runs its query and the new rows appear.
type homeSearchIndexState struct {
	mu      sync.Mutex
	apps    []FileEntry
	games   []recentGame
	files   []homeSearchFile
	builtAt time.Time
	gen     atomic.Uint64
	running atomic.Bool
}

var homeSearchIdx homeSearchIndexState

const (
	// homeSearchIndexTTL is how long a scan is trusted before the next open
	// refreshes it: long enough that reopening search costs nothing, short
	// enough that a file copied in a minute ago turns up.
	homeSearchIndexTTL = 5 * time.Minute
	// homeSearchMaxIndexedFiles bounds the file index (a card of thousands of
	// ROMs must not become thousands of rows in memory on a 1 GB device).
	homeSearchMaxIndexedFiles = 4000
	// homeSearchMaxScanDepth bounds how far below a root the walk goes.
	homeSearchMaxScanDepth = 4
	// homeSearchScanBudget bounds the walk's wall time on a slow SD card.
	homeSearchScanBudget = 3 * time.Second
)

// homeSearchSkipDir reports whether a folder is never worth indexing: hidden
// and system folders, caches and emulator internals.
func homeSearchSkipDir(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "system volume information", "windows", "program files",
		"program files (x86)", "programdata", "appdata", "cache", "caches", "tmp",
		"temp", "thumbnails", "thumbs", "imgs", "media_thumbs", "saves", "states",
		"bios", "cores", "emus", "retroarch", "lost+found":
		return true
	}
	return false
}

// homeSearchFileRoots lists the folders the file index walks: the music and
// Files roots the user already chose, the usual media folders under them and,
// on a desktop, the user's own Music/Videos/Pictures/Documents/Downloads.
// It runs on the render thread (it reads the config) before the scan starts.
func homeSearchFileRoots(config *Config) []string {
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		p = filepath.Clean(p)
		for _, r := range roots {
			if strings.EqualFold(r, p) {
				return
			}
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			roots = append(roots, p)
		}
	}
	if config != nil {
		add(musicDefaultDir(config))
		base := filesDefaultRoot(config)
		for _, sub := range []string{"Music", "Videos", "Movies", "Pictures", "Images", "Books",
			"Documents", "Downloads", "Recordings", "Roms"} {
			add(filepath.Join(base, sub))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && IsWindows() {
		for _, sub := range []string{"Music", "Videos", "Pictures", "Documents", "Downloads", "Desktop"} {
			add(filepath.Join(home, sub))
		}
	}
	return roots
}

// homeSearchScanFiles walks the roots, bounded by count, depth and time. It
// touches no globals, so it runs on a background goroutine.
func homeSearchScanFiles(roots []string, maxFiles, maxDepth int, budget time.Duration) []homeSearchFile {
	deadline := time.Now().Add(budget)
	out := make([]homeSearchFile, 0, 256)
	seen := map[string]bool{}
	for _, root := range roots {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if len(out) >= maxFiles || time.Now().After(deadline) {
				return filepath.SkipAll
			}
			if err != nil || d == nil {
				return nil
			}
			if path == root {
				return nil
			}
			name := d.Name()
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
			if d.IsDir() {
				if homeSearchSkipDir(name) || depth > maxDepth {
					return filepath.SkipDir
				}
			} else if strings.HasPrefix(name, ".") {
				return nil
			}
			clean := filepath.Clean(path)
			if seen[clean] {
				return nil
			}
			seen[clean] = true
			fold := homeSearchFold(name)
			out = append(out, homeSearchFile{
				Name:    name,
				Path:    clean,
				Parent:  filepath.Base(filepath.Dir(clean)),
				IsDir:   d.IsDir(),
				fold:    fold,
				compact: strings.ReplaceAll(fold, " ", ""),
			})
			return nil
		})
	}
	return out
}

// homeSearchRefreshIndex starts the background scans when the index is empty
// or stale. Everything that reads the config is resolved here, on the render
// thread; the goroutine only walks folders.
func homeSearchRefreshIndex(config *Config) {
	homeSearchIdx.mu.Lock()
	fresh := !homeSearchIdx.builtAt.IsZero() && time.Since(homeSearchIdx.builtAt) < homeSearchIndexTTL
	homeSearchIdx.mu.Unlock()
	if fresh || !homeSearchIdx.running.CompareAndSwap(false, true) {
		return
	}
	appRoots := append([]string{appsFolder()}, systemAppDirs()...)
	romRoots := romLibraryRoots()
	fileRoots := homeSearchFileRoots(config)
	go func() {
		defer homeSearchIdx.running.Store(false)
		apps := scanAppsFolder(appRoots[0])
		seen := map[string]bool{}
		for _, a := range apps {
			seen[strings.ToLower(a.Name)] = true
		}
		for _, dir := range appRoots[1:] {
			for _, a := range scanSystemApps(dir) {
				if len(apps) >= maxAppEntries {
					break
				}
				if key := strings.ToLower(a.Name); !seen[key] {
					seen[key] = true
					apps = append(apps, a)
				}
			}
		}
		var games []recentGame
		for _, rom := range scanROMLibrary(romRoots).Games {
			games = append(games, recentGame{Path: rom.Path, System: rom.System, Title: gameDisplayTitle(rom.Path)})
			if len(games) >= homeSearchMaxIndexedFiles {
				break
			}
		}
		files := homeSearchScanFiles(fileRoots, homeSearchMaxIndexedFiles, homeSearchMaxScanDepth, homeSearchScanBudget)
		homeSearchIdx.mu.Lock()
		homeSearchIdx.apps, homeSearchIdx.games, homeSearchIdx.files = apps, games, files
		homeSearchIdx.builtAt = time.Now()
		homeSearchIdx.mu.Unlock()
		homeSearchIdx.gen.Add(1)
	}()
}

// homeSearchIndexSnapshot copies the scanned slices' headers under the lock;
// the scans replace slices rather than appending to them, so the copies stay
// valid after the lock is released.
func homeSearchIndexSnapshot() (apps []FileEntry, games []recentGame, files []homeSearchFile) {
	homeSearchIdx.mu.Lock()
	defer homeSearchIdx.mu.Unlock()
	return homeSearchIdx.apps, homeSearchIdx.games, homeSearchIdx.files
}

// homeSearchFileKind names the glyph and the opener a file gets. It reads the
// extension only, so it is safe for any goroutine.
func homeSearchFileKind(name string, isDir bool) string {
	if isDir {
		return "folder"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".wmv", ".mpg", ".mpeg", ".ts", ".m2ts", ".m4v", ".3gp":
		return "video"
	case ".mp3", ".flac", ".ogg", ".opus", ".wav", ".m4a", ".aac", ".wma", ".aiff":
		return "music"
	case ".png", ".jpg", ".jpeg", ".bmp", ".gif", ".webp":
		return "image"
	case ".pdf", ".epub":
		return "pdf"
	case ".txt", ".md", ".log", ".json", ".xml", ".ini", ".cfg", ".csv", ".yml", ".yaml", ".sh", ".lua", ".html":
		return "text"
	}
	if romFileExtensions[strings.ToLower(filepath.Ext(name))] {
		return "rom"
	}
	return "files"
}

// ── Building the result list ─────────────────────────────────────────────

// homeSearchSources is everything a query is run against. The sheet fills it
// once per open (screens) and per keystroke (the in-memory lists), and the
// tests fill it by hand.
type homeSearchSources struct {
	screens []homeSearchScreen
	apps    []FileEntry
	games   []recentGame
	files   []homeSearchFile
	recent  []FavoriteItem
	history []string // recent queries, newest first
	online  bool
}

// homeSearchScored is a result with its rank inside its group.
type homeSearchScored struct {
	result gsResult
	score  int
}

// homeSearchTop sorts by score (then title, for a stable order) and keeps n.
func homeSearchTop(in []homeSearchScored, n int) []gsResult {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].score != in[j].score {
			return in[i].score > in[j].score
		}
		return strings.ToLower(in[i].result.Title) < strings.ToLower(in[j].result.Title)
	})
	if len(in) > n {
		in = in[:n]
	}
	out := make([]gsResult, 0, len(in))
	for _, s := range in {
		out = append(out, s.result)
	}
	return out
}

// homeSearchResults runs a query over the sources and returns the rows in
// display order (group by group). An empty query is the sheet's start page:
// recent searches, what was playing, and the screens people open most.
func homeSearchResults(raw string, src homeSearchSources) []gsResult {
	q := newHomeSearchQuery(raw)
	if q.empty() {
		return homeSearchStartPage(src)
	}
	var out []gsResult

	var screens []homeSearchScored
	for _, s := range src.screens {
		score := homeSearchMatch(q, s.title, s.keys+" "+s.where)
		if score == 0 {
			continue
		}
		r := gsResult{Title: s.title, Subtitle: s.where, Icon: s.icon, Category: homeSearchGroupScreens, Action: s.action}
		if reason := homeSearchOfflineReason(s.scene, s.title, src.online); reason != "" {
			r.Dimmed, r.Badge, r.Blocked = true, homeSearchNeedsInternet, reason
		}
		screens = append(screens, homeSearchScored{result: r, score: score})
	}
	out = append(out, homeSearchTop(screens, homeSearchMaxScreens)...)

	var apps []homeSearchScored
	seenApps := map[string]bool{}
	for _, e := range src.apps {
		name := appGridDisplayName(e.Name)
		score := homeSearchMatch(q, name, "")
		if score == 0 || seenApps[strings.ToLower(name)] {
			continue
		}
		seenApps[strings.ToLower(name)] = true
		entry := e
		r := gsResult{Title: name, Subtitle: "App", Icon: "apps", Category: homeSearchGroupApps,
			Action: func(c *Config) { launchAppEntry(c, entry) }}
		if homeAppIsBuiltin(entry) {
			r.Subtitle, r.Icon, r.Badge = "Built into JukaHub", "juka", "Juka"
		}
		apps = append(apps, homeSearchScored{result: r, score: score})
	}
	seenGames := map[string]bool{}
	for _, g := range src.games {
		key := strings.ToLower(filepath.Clean(g.Path))
		if g.Path == "" || seenGames[key] {
			continue
		}
		title := strings.TrimSpace(g.Title)
		if title == "" {
			title = gameDisplayTitle(g.Path)
		}
		score := homeSearchMatch(q, title, g.System)
		if score == 0 {
			continue
		}
		seenGames[key] = true
		game := g
		sub := "Game"
		if g.System != "" {
			sub = g.System
		}
		apps = append(apps, homeSearchScored{result: gsResult{Title: title, Subtitle: sub, Icon: "gamepad",
			Category: homeSearchGroupApps, Action: func(*Config) { playRecentGame(game) }}, score: score - 1})
	}
	out = append(out, homeSearchTop(apps, homeSearchMaxApps)...)

	var files []homeSearchScored
	seenFiles := map[string]bool{}
	addFile := func(name, path, parent string, isDir bool, score int) {
		key := strings.ToLower(path)
		if score == 0 || seenFiles[key] {
			return
		}
		seenFiles[key] = true
		p, n, dir := path, name, isDir
		sub := parent
		if sub == "" || sub == "." {
			sub = filepath.Dir(path)
		}
		files = append(files, homeSearchScored{result: gsResult{Title: name, Subtitle: sub,
			Icon: homeSearchFileKind(name, isDir), Category: homeSearchGroupFiles,
			Action: func(c *Config) { homeSearchOpenFile(c, p, n, dir) }}, score: score})
	}
	for _, it := range src.recent {
		if it.Type != "file" {
			continue
		}
		if fe, ok := it.Data.(FileEntry); ok && fe.Path != "" {
			name := fe.Name
			if name == "" {
				name = filepath.Base(fe.Path)
			}
			// Recently opened files rank above the index's matches of the same
			// strength: they are the ones the user is likely looking for.
			if score := homeSearchMatch(q, name, ""); score > 0 {
				addFile(name, fe.Path, filepath.Base(filepath.Dir(fe.Path)), fe.IsDir, score+50)
			}
		}
	}
	for _, f := range src.files {
		score := 0
		switch {
		case f.fold == q.text || f.compact == q.compact:
			score = 900
		case strings.HasPrefix(f.fold, q.text) || strings.HasPrefix(f.compact, q.compact):
			score = 700
		case homeSearchWordsMatch(strings.Fields(f.fold), q.words):
			score = 500
		case len(q.compact) >= 3 && strings.Contains(f.compact, q.compact):
			score = 300
		}
		addFile(f.Name, f.Path, f.Parent, f.IsDir, score)
	}
	out = append(out, homeSearchTop(files, homeSearchMaxFiles)...)

	var media []homeSearchScored
	for _, it := range src.recent {
		if it.Type == "file" {
			continue
		}
		title := it.Label()
		kind, icon := homeSearchRecentKind(it)
		score := homeSearchMatch(q, title, kind)
		if score == 0 {
			continue
		}
		item := it
		media = append(media, homeSearchScored{result: gsResult{Title: title, Subtitle: kind, Icon: icon,
			Category: homeSearchGroupMedia, Action: func(c *Config) { homePlayRecent(c, item) }}, score: score})
	}
	out = append(out, homeSearchTop(media, homeSearchMaxMedia)...)

	out = append(out, homeSearchWebRows(q, src.online)...)
	return out
}

// homeSearchRecentKind is the subtitle and glyph of a Continue item.
func homeSearchRecentKind(it FavoriteItem) (string, string) {
	switch it.Type {
	case "video":
		return "Video", "video"
	case "podcast":
		return "Podcast", "mic"
	case "audio":
		return "Music", "music"
	case "iptv":
		return "Channel", "media"
	case "game":
		if g, ok := it.Data.(recentGame); ok && g.System != "" {
			return g.System, "gamepad"
		}
		return "Game", "gamepad"
	}
	return "Recently played", "play"
}

// homeSearchStartPage is what the sheet lists before anything is typed: the
// last searches (they refill the field), what was playing, and a short row of
// the screens people jump to most.
func homeSearchStartPage(src homeSearchSources) []gsResult {
	var out []gsResult
	for i, h := range src.history {
		if i >= homeSearchMaxRecent {
			break
		}
		text := h
		out = append(out, gsResult{Title: h, Subtitle: "Search again", Icon: "search",
			Category: homeSearchGroupRecent, Keep: true, Fill: text})
	}
	n := 0
	for _, it := range src.recent {
		if n >= 3 {
			break
		}
		kind, icon := homeSearchRecentKind(it)
		if it.Type == "file" {
			kind, icon = "File", "files"
		}
		item := it
		out = append(out, gsResult{Title: it.Label(), Subtitle: kind, Icon: icon,
			Category: homeSearchGroupMedia, Action: func(c *Config) { homePlayRecent(c, item) }})
		n++
	}
	quick := map[string]bool{"Wi-Fi": true, "Settings": true, "Files": true, "Apps": true, "Tools": true, "Media": true}
	for _, s := range src.screens {
		if !quick[s.title] {
			continue
		}
		r := gsResult{Title: s.title, Subtitle: s.where, Icon: s.icon, Category: homeSearchGroupScreens, Action: s.action}
		if reason := homeSearchOfflineReason(s.scene, s.title, src.online); reason != "" {
			r.Dimmed, r.Badge, r.Blocked = true, homeSearchNeedsInternet, reason
		}
		out = append(out, r)
	}
	return out
}

// homeSearchLooksLikeURL reports whether the query is an address rather than
// words: a scheme, or a single token with a dot and no spaces ("github.com").
func homeSearchLooksLikeURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	dot := strings.LastIndex(s, ".")
	return dot > 0 && dot < len(s)-2 && !strings.HasSuffix(s, ".")
}

// homeSearchWebURL is the Browser's search address for a query, on the engine
// chosen in Settings → Browser (browser_engines.go).
func homeSearchWebURL(raw string) string {
	return brwSearchURLFor(appConfig, raw)
}

// homeSearchWebRows is the last group: open the typed address, and search the
// web for the words. Offline both stay listed, dimmed, so the sheet says why
// nothing happens rather than hiding the way out.
func homeSearchWebRows(q homeSearchQuery, online bool) []gsResult {
	if q.empty() {
		return nil
	}
	raw := q.raw
	var out []gsResult
	mark := func(r gsResult) gsResult {
		if !online {
			r.Dimmed, r.Badge, r.Blocked = true, homeSearchNeedsInternet, offlinePlainNotice
		}
		return r
	}
	if homeSearchLooksLikeURL(raw) {
		address := raw
		out = append(out, mark(gsResult{Title: "Open " + raw, Subtitle: "Browser", Icon: "link",
			Category: homeSearchGroupWeb, Action: func(c *Config) { openBrowserAtURL(c, address) }}))
	}
	searchURL := homeSearchWebURL(raw)
	out = append(out, mark(gsResult{Title: "Search the web for “" + raw + "”", Subtitle: brwEngineFor(appConfig).Name + " in the Browser",
		Icon: "globe", Category: homeSearchGroupWeb, Action: func(c *Config) { openBrowserAtURL(c, searchURL) }}))
	return out
}

// homeSearchOpenFile opens a file result where it belongs: music in the
// player, video in the embedded player, a picture in the viewer, and anything
// else (folders, documents, games) in Files with the row selected, where its
// own open action knows the type.
func homeSearchOpenFile(config *Config, path, name string, isDir bool) {
	if config == nil || path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		showToast("That file is no longer there: "+name, ToastWarn())
		return
	}
	switch homeSearchFileKind(name, isDir) {
	case "music":
		openAudioPlayer(config, path)
		return
	case "video":
		playStream(config, path)
		return
	case "image":
		openImageViewer(config, path)
		return
	}
	dir, focus := filepath.Dir(path), name
	if isDir {
		dir, focus = path, ""
	}
	idx := sceneIndexExact(config, "FileExplorer")
	if idx < 0 {
		showToast("Files is not available on this build", ToastWarn())
		return
	}
	filesNavigate(config, dir, focus)
	changeSceneTo(config, idx)
}
