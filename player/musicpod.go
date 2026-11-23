package main

// musicpod.go — the iPod layout: the music player as a click-wheel iPod. A
// stack of menus (Music, Videos, Podcasts, Audiobooks, Extras, Settings,
// Shuffle Songs, Now Playing) over any source — this device, Navidrome,
// Jellyfin, or all of them folded together (musicsrc.go) — drawn in one of
// 38 four-shade themes with six screen transitions and dithered album art
// (musicpod_theme.go, musicpod_draw.go).
//
// Controls. D-pad Up/Down scroll (the wheel); A or Right opens; B or Left
// goes back (B at the top menu leaves the player; Menu jumps to the top); X
// is the context menu (Play Next, On-The-Go, Download Offline, Info, Rename,
// Delete…); Y jumps to Now Playing, where it changes the picture (cover,
// visualizer, lyrics). Start plays and pauses; L1/R1 are previous and next;
// L2/R2 page through a list, or seek on Now Playing. On Now Playing, Up/Down
// are the volume and Left/Right previous/next, as on an iPod. A keyboard has
// the same keys plus Enter, Esc, Space and PgUp/PgDn.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type podPageKind int

const (
	podPageList podPageKind = iota
	podPageNowPlaying
	podPageVisualizer
	podPageInfo
)

// podEntry is one row of a menu.
type podEntry struct {
	label  string
	right  string
	more   bool       // opens a page (">")
	item   *musicItem // a library row
	open   func() *podPage
	act    func()
	adjust func(dir int) // a setting: Left/Right change it, A steps it
	value  func() string // a setting's value, read when drawn
	dim    bool
}

// podPage is one screen of the stack.
type podPage struct {
	title   string
	kind    podPageKind
	entries []podEntry
	cursor  int
	scroll  int
	loading bool
	errText string
	empty   string
	src     musicSrcKind
	query   *musicQuery
	items   []musicItem
	gen     int
	info    []string
	rebuild func(p *podPage) // a menu whose rows depend on state (Settings)
}

// podCtxMenu is the context menu (or a confirmation) over a page.
type podCtxMenu struct {
	title   string
	entries []podEntry
	cursor  int
}

// musicPod is the layout's state. Main thread only.
var musicPod struct {
	stack   []*podPage
	ctx     *podCtxMenu
	gen     int
	volAt   time.Time
	npMode  int // Now Playing's picture: 0 cover, 1 visualizer, 2 lyrics
	trans   podTransition
	rows    []sdl.Rect
	rowBase int
	ctxRows []sdl.Rect
	back    sdl.Rect
	npSeek  sdl.Rect
	npArt   sdl.Rect
	npPrev  sdl.Rect
	npPlay  sdl.Rect
	npNext  sdl.Rect
	entered bool
}

// podTransition is the screen change in progress.
type podTransition struct {
	from  *podPage
	start time.Time
	dir   int // +1 deeper, -1 back
	kind  int
}

const podTransitionLength = 230 * time.Millisecond

// musicPodActive reports whether the player is in the iPod layout.
func musicPodActive(config *Config) bool {
	return musicLayoutFromConfig(config) == musicLayoutPod
}

// musicPodEnter prepares the layout when the player opens: the top menu the
// first time, Now Playing when music is already loaded.
func musicPodEnter(config *Config) {
	mlibStartScan(config, false)
	music.mu.Lock()
	loaded := music.current >= 0 && music.current < len(music.tracks)
	playing := music.playing
	music.mu.Unlock()
	if len(musicPod.stack) == 0 {
		musicPod.stack = []*podPage{podRootPage(config)}
		if loaded {
			musicPod.stack = append(musicPod.stack, podNowPlayingPage())
		}
	} else if playing && podTop().kind != podPageNowPlaying {
		// A song started from elsewhere (Files, Home): show it playing.
		musicPod.stack = append(musicPod.stack, podNowPlayingPage())
	}
	musicPod.ctx = nil
	musicPod.trans.from = nil
	musicPod.entered = true
}

func podTop() *podPage {
	if len(musicPod.stack) == 0 {
		musicPod.stack = []*podPage{podRootPage(appConfig)}
	}
	return musicPod.stack[len(musicPod.stack)-1]
}

// podPush opens a page with the deeper transition.
func podPush(p *podPage) {
	if p == nil {
		return
	}
	podStartTransition(+1)
	musicPod.stack = append(musicPod.stack, p)
	if p.rebuild != nil {
		p.rebuild(p)
	}
	if p.query != nil && p.entries == nil {
		podLoad(p)
	}
}

// podPop goes back a page; at the top it leaves the player.
func podPop(config *Config) {
	musicPod.ctx = nil
	if len(musicPod.stack) <= 1 {
		musicPod.entered = false
		musicLeavePlayer(config)
		goBackScene(config)
		return
	}
	podStartTransition(-1)
	musicPod.stack = musicPod.stack[:len(musicPod.stack)-1]
	if top := podTop(); top.rebuild != nil {
		top.rebuild(top)
	}
	PlayBackSound()
}

// podPopToRoot goes back to the top menu.
func podPopToRoot() {
	if len(musicPod.stack) <= 1 {
		return
	}
	podStartTransition(-1)
	musicPod.stack = musicPod.stack[:1]
	PlayBackSound()
}

func podStartTransition(dir int) {
	if len(musicPod.stack) == 0 {
		return
	}
	musicPod.trans = podTransition{from: podTop(), start: time.Now(), dir: dir, kind: musicPodTransitionIndex(appConfig)}
}

// podShowNowPlaying opens Now Playing (or returns to it when it is open).
func podShowNowPlaying() {
	for i, p := range musicPod.stack {
		if p.kind == podPageNowPlaying {
			if i == len(musicPod.stack)-1 {
				return
			}
			podStartTransition(+1)
			musicPod.stack = append(musicPod.stack[:i], musicPod.stack[i+1:]...)
			musicPod.stack = append(musicPod.stack, p)
			return
		}
	}
	podPush(podNowPlayingPage())
}

func podNowPlayingPage() *podPage { return &podPage{title: "Now Playing", kind: podPageNowPlaying} }

// ── Loading lists ───────────────────────────────────────────────────────────

// podLoad fetches a page's list off the main thread.
func podLoad(p *podPage) {
	if p.query == nil {
		return
	}
	musicPod.gen++
	p.gen = musicPod.gen
	p.loading, p.errText = true, ""
	gen, src, q := p.gen, p.src, *p.query
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		items, err := musicFetch(ctx, src, q)
		tbxPost(func() {
			if p.gen != gen {
				return
			}
			p.loading = false
			if err != nil && len(items) == 0 {
				p.errText = firstLine(err.Error())
			}
			p.items = items
			p.entries = podEntriesFor(p, items)
			if p.cursor >= len(p.entries) {
				p.cursor = 0
			}
		})
	}()
}

// podQueryPage is a page listing a query's answer.
func podQueryPage(title string, src musicSrcKind, q musicQuery, empty string) *podPage {
	return &podPage{title: title, src: src, query: &q, empty: empty}
}

// podEntriesFor turns a list's rows into menu rows.
func podEntriesFor(p *podPage, items []musicItem) []podEntry {
	out := make([]podEntry, 0, len(items)+1)
	mixed := p.query != nil && (p.query.What == "songs" || p.query.What == "search" || p.query.What == "favorites" ||
		p.query.What == "genreTracks" || p.query.What == "composerTracks" || p.query.What == "playlistTracks" || p.query.What == "artistSongs")
	tracks := 0
	for _, it := range items {
		if it.Kind == "track" {
			tracks++
		}
	}
	// An artist's albums start with All Songs.
	if p.query != nil && p.query.What == "artistAlbums" && len(items) > 0 {
		artist, src := p.query.Parent, p.src
		out = append(out, podEntry{label: "All Songs", more: true, open: func() *podPage {
			return podQueryPage(artist.Title, src, musicQuery{What: "artistSongs", Parent: artist}, "No songs")
		}})
	}
	// A list of songs starts with Shuffle, as an iPod's does.
	if tracks > 1 {
		list := items
		out = append(out, podEntry{label: "Shuffle", right: strconv.Itoa(tracks), act: func() {
			if musicPlayItems(appConfig, list, 0, true) {
				podShowNowPlaying()
			}
		}})
	}
	for i := range items {
		it := items[i]
		idx := i
		src := p.src
		e := podEntry{label: it.Title, item: &items[i]}
		if e.label == "" {
			e.label = "Untitled"
		}
		switch it.Kind {
		case "artist":
			e.more = true
			e.open = func() *podPage { return podArtistPage(src, it) }
		case "album":
			e.more = true
			if p.query != nil && p.query.What != "artistAlbums" && it.Artist != "" {
				e.right = it.Artist
			} else if it.Year > 0 {
				e.right = strconv.Itoa(it.Year)
			}
			e.open = func() *podPage {
				return podQueryPage(it.Title, src, musicQuery{What: "albumTracks", Parent: it}, "No songs on this album")
			}
		case "track":
			if mixed && it.Artist != "" {
				e.label = it.Title + " — " + it.Artist
			}
			if it.Duration > 0 {
				e.right = formatTime(it.Duration)
			}
			if musicPodHasLocalCopy(it) {
				e.right = "↓ " + e.right
			}
			list := items
			e.act = func() {
				if musicPlayItems(appConfig, list, idx, false) {
					podShowNowPlaying()
				}
			}
		case "genre", "composer":
			e.more = true
			if it.Count > 0 {
				e.right = strconv.Itoa(it.Count)
			}
			what := "genreTracks"
			if it.Kind == "composer" {
				what = "composerTracks"
			}
			e.open = func() *podPage { return podQueryPage(it.Title, src, musicQuery{What: what, Parent: it}, "No songs") }
		case "playlist":
			e.more = true
			if it.Count > 0 {
				e.right = strconv.Itoa(it.Count)
			}
			e.open = func() *podPage {
				return podQueryPage(it.Title, src, musicQuery{What: "playlistTracks", Parent: it}, "This playlist is empty")
			}
		case "movie", "episode":
			if it.Duration > 0 {
				e.right = formatTime(it.Duration)
				if it.Position > 30 && it.Duration > 0 {
					e.right = fmt.Sprintf("%d%%", int(it.Position/it.Duration*100))
				}
			}
			e.act = func() { podPlayVideo(appConfig, it) }
		case "series":
			e.more = true
			e.open = func() *podPage { return podQueryPage(it.Title, src, musicQuery{What: "seasons", Parent: it}, "No seasons") }
		case "season":
			e.more = true
			if it.Count > 0 {
				e.right = strconv.Itoa(it.Count)
			}
			e.open = func() *podPage { return podQueryPage(it.Title, src, musicQuery{What: "episodes", Parent: it}, "No episodes") }
		case "book":
			e.right = it.Artist
			e.act = func() {
				musicShelfPlayGroup(appConfig, it.ID, true, "")
				podShowNowPlaying()
			}
		}
		out = append(out, e)
	}
	return out
}

// musicPodHasLocalCopy reports whether a server's song is on the card too.
func musicPodHasLocalCopy(it musicItem) bool {
	if it.Src == musicSrcLocal || it.Src == "" {
		return false
	}
	for _, part := range it.Parts {
		if part.Src == musicSrcLocal {
			return true
		}
	}
	if dest := musicOfflinePath(it); dest != "" {
		if _, err := os.Stat(dest); err == nil {
			return true
		}
	}
	return false
}

// ── The menus ───────────────────────────────────────────────────────────────

func podMenu(title string, entries ...podEntry) *podPage {
	return &podPage{title: title, entries: entries}
}

func podOpen(label string, open func() *podPage) podEntry {
	return podEntry{label: label, more: true, open: open}
}

// podRootPage is the top menu.
func podRootPage(config *Config) *podPage {
	p := podMenu("iPod")
	p.rebuild = func(p *podPage) {
		entries := []podEntry{
			podOpen("Music", func() *podPage { return podMusicPage(config) }),
			podOpen("Videos", func() *podPage { return podVideosPage(config) }),
			podOpen("Podcasts", podPodcastsPage),
			podOpen("Audiobooks", func() *podPage {
				return podQueryPage("Audiobooks", musicSrcLocal, musicQuery{What: "books"}, "No audiobooks yet — add them to an Audiobooks folder")
			}),
			podOpen("Extras", func() *podPage { return podExtrasPage(config) }),
			podOpen("Settings", func() *podPage { return podSettingsPage(config) }),
			{label: "Shuffle Songs", act: func() { podShuffleSongs(config) }},
		}
		music.mu.Lock()
		loaded := music.current >= 0 && music.current < len(music.tracks)
		music.mu.Unlock()
		if loaded {
			entries = append(entries, podEntry{label: "Now Playing", more: true, act: podShowNowPlaying})
		}
		p.entries = entries
		if p.cursor >= len(entries) {
			p.cursor = len(entries) - 1
		}
	}
	p.rebuild(p)
	return p
}

// podMusicPage is Music: the library of the chosen source.
func podMusicPage(config *Config) *podPage {
	src := musicSourceFromConfig(config)
	q := func(title, what, empty string) podEntry {
		return podOpen(title, func() *podPage { return podQueryPage(title, src, musicQuery{What: what}, empty) })
	}
	entries := []podEntry{
		q("Playlists", "playlists", "No playlists yet — X on a song adds it to On-The-Go"),
		q("Artists", "artists", "No artists yet"),
		q("Albums", "albums", "No albums yet"),
		q("Songs", "songs", "No songs yet"),
		q("Genres", "genres", "No genres — songs without a genre tag are not listed"),
	}
	if src == musicSrcLocal || src == musicSrcAll {
		entries = append(entries, q("Composers", "composers", "No composers — songs without a composer tag are not listed"))
	}
	entries = append(entries,
		q("Favorites", "favorites", "No favorites yet — X on a song marks one"),
		q("Recently Added", "recent", "Nothing added lately"),
		podEntry{label: "Search…", act: func() { podSearch(src) }},
	)
	p := podMenu("Music", entries...)
	if src != musicSrcLocal && !musicSrcReady(src) && src != musicSrcAll {
		p.entries = append([]podEntry{{label: src.label() + " is not set up", dim: true,
			act: func() { musicServersPhoneOpen("") }}}, p.entries...)
	}
	return p
}

// podArtistPage is an artist's albums, with All Songs first.
func podArtistPage(src musicSrcKind, artist musicItem) *podPage {
	p := podQueryPage(artist.Title, src, musicQuery{What: "artistAlbums", Parent: artist}, "No albums")
	return p
}

// podSearch asks for words and lists what matches.
func podSearch(src musicSrcKind) {
	osPromptOpen("Search music", "Artist, album or song", "Search", "", 80, func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		podPush(podQueryPage("“"+text+"”", src, musicQuery{What: "search", Text: text}, "Nothing matches"))
	})
}

// podShuffleSongs plays the whole library in a random order.
func podShuffleSongs(config *Config) {
	src := musicSourceFromConfig(config)
	showToast("Shuffling "+src.label()+"…", ToastInfo())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		items, err := musicFetch(ctx, src, musicQuery{What: "random"})
		tbxPost(func() {
			if err != nil && len(items) == 0 {
				showToast(firstLine(err.Error()), ToastWarn())
				return
			}
			if musicPlayItems(config, items, 0, true) {
				podShowNowPlaying()
			}
		})
	}()
}

// podVideosPage is Videos: films, shows and what you were watching, from
// the card and from Jellyfin.
func podVideosPage(config *Config) *podPage {
	src := musicSourceFromConfig(config)
	if src == musicSrcNavidrome {
		src = musicSrcLocal // Navidrome has no videos
	}
	q := func(title, what, empty string) podEntry {
		return podOpen(title, func() *podPage { return podQueryPage(title, src, musicQuery{What: what}, empty) })
	}
	return podMenu("Videos",
		q("Continue Watching", "continue", "Nothing in progress"),
		q("Movies", "movies", "No movies yet — add them to Videos or Movies"),
		q("TV Shows", "shows", "No shows yet — add them to TV"),
	)
}

// podPodcastsPage lists the subscriptions; a show opens its episodes.
func podPodcastsPage() *podPage {
	p := podMenu("Podcasts")
	p.rebuild = func(p *podPage) {
		shows := podcastShowsInFeedOrder()
		p.entries = p.entries[:0]
		for _, show := range shows {
			show := show
			e := podEntry{label: dlFirstNonEmpty(show.Title, podcastFeedLabel(show.FeedURL)), more: true}
			if n := len(show.Episodes); n > 0 {
				e.right = strconv.Itoa(n)
			}
			e.open = func() *podPage {
				ep := podMenu(e.label)
				for i, episode := range show.Episodes {
					i := i
					row := podEntry{label: dlFirstNonEmpty(episode.Title, podcastEpisodeTitleFromURL(episode.URL))}
					if episode.Duration > 0 {
						row.right = formatTime(episode.Duration)
					}
					row.act = func() {
						if playPodcastEpisode(appConfig, show, i) {
							podShowNowPlaying()
						}
					}
					ep.entries = append(ep.entries, row)
				}
				if len(ep.entries) == 0 {
					ep.empty = dlFirstNonEmpty(show.Err, "No episodes yet")
				}
				return ep
			}
			p.entries = append(p.entries, e)
		}
		if len(p.entries) == 0 {
			p.empty = "No podcasts yet — add a feed from the Podcasts panel"
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if podcastRefresh(ctx) > 0 {
			tbxPost(func() {
				if podTop() == p && p.rebuild != nil {
					p.rebuild(p)
				}
			})
		}
	}()
	return p
}

// podExtrasPage is Extras: visualizers, games, the stopwatch, files.
func podExtrasPage(config *Config) *podPage {
	vis := podMenu("Visualizers")
	for i, name := range musicVizNames {
		i := i
		vis.entries = append(vis.entries, podEntry{label: name, act: func() {
			musicVizSetStyle(config, i)
			podPush(&podPage{title: musicVizNames[i], kind: podPageVisualizer})
		}})
	}
	games := podMenu("Games",
		podEntry{label: "Music Quiz", act: func() { musicQuizStart(config) }},
		podEntry{label: "Sound Racer", act: func() { musicRacerStart(config) }},
		podEntry{label: "Brick Breaker", act: func() { podOpenArcade(config, gameBrickBreaker) }},
		podEntry{label: "Tetris", act: func() { podOpenArcade(config, gameTetris) }},
		podEntry{label: "Snake", act: func() { podOpenArcade(config, gameSnake) }},
		podEntry{label: "2048", act: func() { podOpenArcade(config, game2048) }},
	)
	return podMenu("Extras",
		podOpen("Visualizers", func() *podPage { return vis }),
		podOpen("Games", func() *podPage { return games }),
		podEntry{label: "Stopwatch", act: func() { osAppsOpen(config, stopwatchSceneName) }},
		podEntry{label: "Browse Files", act: func() { podBrowseFiles(config) }},
		podEntry{label: "Sound Effects", act: musicFXPanelOpen},
		podEntry{label: "Equalizer", act: func() {
			musicEQ.panel = true
			PlayActivateSound()
		}},
	)
}

// podOpenArcade starts one of the Games screen's arcade games.
func podOpenArcade(config *Config, id gameID) {
	idx := sceneIndexExact(config, "Games")
	if idx < 0 {
		showToast("The Games screen is not in this configuration", ToastWarn())
		return
	}
	changeSceneTo(config, idx)
	if idx == currentSceneIndex || (transitionPhase == "fade-out" && pendingSceneIndex == idx) {
		queueGameStart(config, id)
	}
}

// podBrowseFiles opens Files at the music folder: rename, copy, move, delete
// and file info all live there.
func podBrowseFiles(config *Config) {
	idx := sceneIndexExact(config, "FileExplorer")
	if idx < 0 {
		showToast("Files is not in this configuration", ToastWarn())
		return
	}
	dir := musicMediaRoot("Music")
	changeSceneTo(config, idx)
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			filesNavigate(config, dir, "")
		}
	}
}

// podPlayVideo plays a film or episode in the video player, where it was left.
func podPlayVideo(config *Config, it musicItem) {
	if it.Path == "" {
		return
	}
	pos := it.Position
	if it.Src == musicSrcLocal && pos <= 0 {
		pos = mlibResumeAt(it.Path)
	}
	if it.Played && pos <= 0 {
		pos = 0
	}
	scrobbleStartWatcher()
	playVideoInfo(config, VideoInfo{Title: it.Title, URL: it.Path, Position: pos, Duration: it.Duration})
}

// ── Settings ────────────────────────────────────────────────────────────────

// podSetting is a settings row: Left/Right change it.
func podSetting(label string, value func() string, adjust func(dir int)) podEntry {
	return podEntry{label: label, value: value, adjust: adjust}
}

func podSettingsPage(config *Config) *podPage {
	p := podMenu("Settings")
	p.rebuild = func(p *podPage) {
		p.entries = []podEntry{
			podSetting("Source", func() string { return musicSourceFromConfig(config).label() }, func(dir int) {
				musicSetSource(config, musicSrcCycle(musicSourceFromConfig(config), dir))
			}),
			podOpen("Music Servers", func() *podPage { return podServersPage(config) }),
			podSetting("Theme", func() string { return musicPodThemes[musicPodThemeIndex(config)].name }, func(dir int) {
				musicPodSetTheme(config, musicPodThemeIndex(config)+dir)
			}),
			podSetting("Transition", func() string { return musicPodTransitions[musicPodTransitionIndex(config)] }, func(dir int) {
				musicPodSetTransition(config, musicPodTransitionIndex(config)+dir)
			}),
			podSetting("Clicker", func() string { return onOff(musicPodClickerOn(config)) }, func(int) {
				musicPodSetClicker(config, !musicPodClickerOn(config))
			}),
			podSetting("Shuffle", func() string {
				music.mu.Lock()
				defer music.mu.Unlock()
				return onOff(music.shuffle)
			}, func(int) { musicToggleShuffle() }),
			podSetting("Repeat", func() string {
				music.mu.Lock()
				defer music.mu.Unlock()
				return music.repeatMode.short()
			}, func(int) { musicCycleRepeatMode() }),
			podSetting("EQ", func() string {
				if !musicEQ.enabled {
					return "Off"
				}
				return musicEQ.preset
			}, func(dir int) {
				name := musicPodNextPreset(musicEQ.preset, musicEQ.enabled, dir)
				if name == "Off" {
					musicSetPEQEnabled(false, config)
					return
				}
				musicEQ.preset, musicEQ.gains = name, musicPEQPresetGains(name)
				musicSetPEQEnabled(true, config)
			}),
			{label: "Edit EQ Curve", act: func() {
				musicEQ.panel = true
				PlayActivateSound()
			}},
			{label: "AutoEQ Headphones", value: func() string { return dlFirstNonEmpty(musicAutoEQName(), "Off") }, act: func() { musicAutoEQOpen(false) }},
			{label: "Sound Effects", value: musicFXStatus, act: musicFXPanelOpen},
			podSetting("Speed", func() string {
				music.mu.Lock()
				defer music.mu.Unlock()
				s := music.speed
				if s <= 0 {
					s = 1
				}
				return formatSpeedLabel(s)
			}, func(dir int) { musicCycleSpeed(dir) }),
			podSetting("Sleep Timer", func() string {
				if left := musicSleepRemaining(); left > 0 {
					return formatSleepRemaining(left)
				}
				return "Off"
			}, func(int) { musicCycleSleepTimer() }),
			podSetting("Visualizer", func() string { return musicVizNames[musicVizStyle(config)] }, func(dir int) {
				musicVizSetStyle(config, musicVizStyle(config)+dir)
			}),
			podSetting("Dithering", func() string { return onOff(musicVizDitherOn(config)) }, func(int) {
				musicVizSetDither(config, !musicVizDitherOn(config))
				podArtFlush()
			}),
			podOpen("Scrobbling", func() *podPage { return podScrobblePage(config) }),
			podSetting("Offline Cache", func() string {
				mb := musicCacheLimitMB.Load()
				if mb <= 0 {
					return "Off"
				}
				if mb >= 1024 {
					return fmt.Sprintf("%d GB", mb/1024)
				}
				return fmt.Sprintf("%d MB", mb)
			}, func(dir int) {
				cur, at := musicCacheLimitMB.Load(), 0
				for i, v := range musicCacheSteps {
					if v == cur {
						at = i
					}
				}
				at = (at + dir + len(musicCacheSteps)) % len(musicCacheSteps)
				musicCacheSetMB(config, musicCacheSteps[at])
			}),
			{label: "Clear Cache", act: func() {
				go func() {
					musicCacheClear()
					tbxPost(func() { showToast("Song cache cleared", ToastInfo()) })
				}()
			}},
			{label: "Downloads", value: musicDownloadStatus},
			podSetting("Background Play", func() string { return onOff(musicBackgroundEnabled(config)) }, func(int) {
				musicSetBackgroundEnabled(config, !musicBackgroundEnabled(config))
			}),
			podSetting("Language", podLanguageLabel, func(dir int) { podCycleLanguage(config, dir) }),
			podSetting("Layout", func() string { return musicLayoutFromConfig(config).label() }, func(dir int) {
				musicSetLayout(config, musicLayoutCycle(musicLayoutFromConfig(config), dir))
				musicPod.stack = nil
			}),
			{label: "About", more: true, open: podAboutPage},
		}
	}
	return p
}

// musicPodNextPreset steps the EQ row through Off and every preset.
func musicPodNextPreset(cur string, enabled bool, dir int) string {
	names := append([]string{"Off"}, musicPEQPresetNames...)
	at := 0
	if enabled {
		for i, n := range names {
			if strings.EqualFold(n, cur) {
				at = i
			}
		}
	}
	return names[(at+dir+len(names))%len(names)]
}

// podLanguageLabel and podCycleLanguage switch the interface language among
// the ones JukaHub ships (i18n.go).
func podLanguageLabel() string {
	code := uiLanguageDefault
	if appConfig != nil {
		code = dlFirstNonEmpty(customStringValue(appConfig.Variables.Custom, uiLanguageKey), uiLanguageDefault)
	}
	for _, l := range uiLanguages {
		if strings.EqualFold(l.Code, code) {
			return l.Native
		}
	}
	return "English"
}

func podCycleLanguage(config *Config, dir int) {
	code := dlFirstNonEmpty(customStringValue(config.Variables.Custom, uiLanguageKey), uiLanguageDefault)
	at := 0
	for i, l := range uiLanguages {
		if strings.EqualFold(l.Code, code) {
			at = i
		}
	}
	next := uiLanguages[(at+dir+len(uiLanguages))%len(uiLanguages)]
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[uiLanguageKey] = next.Code
	uiLanguageApply(config)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// podServersPage is Music Servers: the state of each server and the ways to
// set one up.
func podServersPage(config *Config) *podPage {
	p := podMenu("Music Servers")
	p.rebuild = func(p *podPage) {
		s := musicServersGet()
		nd := "Not set up"
		if s.Navidrome.URL != "" {
			nd = s.Navidrome.User + " · " + musicHostOf(s.Navidrome.URL)
		}
		jf := "Not set up"
		if s.Jellyfin.Token != "" {
			jf = s.Jellyfin.User + " · " + dlFirstNonEmpty(s.Jellyfin.ServerName, musicHostOf(s.Jellyfin.URL))
		}
		p.entries = []podEntry{
			{label: "Navidrome", value: func() string { return nd }, act: func() { musicServersPhoneOpen("navidrome") }},
			{label: "Jellyfin", value: func() string { return jf }, act: func() { musicServersPhoneOpen("jellyfin") }},
			{label: "Jellyfin Quick Connect", act: func() { podJellyfinQuickConnect(config) }},
			{label: "Set Up From Phone", act: func() { musicServersPhoneOpen("") }},
			{label: "Navidrome Scrobbling", value: func() string { return onOff(musicServersGet().Navidrome.Scrobble) }, adjust: func(int) {
				_ = musicServersUpdate(func(s *musicServersFile) { s.Navidrome.Scrobble = !s.Navidrome.Scrobble })
			}},
			{label: "Stream Quality", value: func() string {
				if q := musicServersGet().Navidrome.Quality; q > 0 {
					return fmt.Sprintf("%d kbps", q)
				}
				return "Original"
			}, adjust: func(dir int) {
				steps := []int{0, 320, 192, 128, 96}
				cur := musicServersGet().Navidrome.Quality
				at := 0
				for i, v := range steps {
					if v == cur {
						at = i
					}
				}
				next := steps[(at+dir+len(steps))%len(steps)]
				_ = musicServersUpdate(func(s *musicServersFile) { s.Navidrome.Quality = next })
			}},
			{label: "Refresh Library", act: func() {
				musicSrcCacheClear()
				mlibStartScan(config, true)
				showToast("Asking the servers and the card again", ToastInfo())
			}},
		}
		if s.Navidrome.URL != "" {
			p.entries = append(p.entries, podEntry{label: "Forget Navidrome", act: func() {
				podConfirm("Forget Navidrome?", "Forget", func() {
					_ = musicServersUpdate(func(s *musicServersFile) { s.Navidrome = musicNavidromeConf{} })
					p.rebuild(p)
				})
			}})
		}
		if s.Jellyfin.URL != "" {
			p.entries = append(p.entries, podEntry{label: "Sign Out of Jellyfin", act: func() {
				podConfirm("Sign out of Jellyfin?", "Sign Out", func() {
					_ = musicServersUpdate(func(s *musicServersFile) { s.Jellyfin = musicJellyfinConf{} })
					p.rebuild(p)
				})
			}})
		}
	}
	return p
}

// musicHostOf is an address's host for a label.
func musicHostOf(addr string) string {
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	if i := strings.IndexAny(addr, "/?"); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// podJellyfinQuickConnect signs in without a password: the address (typed,
// or found on the home network), then a code to approve in any Jellyfin app.
func podJellyfinQuickConnect(config *Config) {
	seed := musicServersGet().Jellyfin.URL
	start := func(addr string) {
		if strings.TrimSpace(addr) == "" {
			return
		}
		showToast("Asking "+musicHostOf(addr)+" for a code…", ToastInfo())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			clean, qc, info, err := jfQuickStart(ctx, addr)
			if err != nil {
				tbxPost(func() { showToast(firstLine(err.Error()), ToastWarn()) })
				return
			}
			tbxPost(func() {
				podInfoPush("Quick Connect", []string{
					"Code: " + qc.Code,
					"",
					"In any Jellyfin app signed in as you:",
					"Settings › Quick Connect, enter the code.",
					"This handheld signs in by itself.",
				})
			})
			deadline := time.Now().Add(5 * time.Minute)
			for time.Now().Before(deadline) {
				time.Sleep(3 * time.Second)
				pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
				done, perr := jfQuickPoll(pctx, clean, qc, info)
				pcancel()
				if done {
					tbxPost(func() {
						if perr != nil {
							showToast(firstLine(perr.Error()), ToastWarn())
							return
						}
						showToast("Jellyfin: signed in to "+dlFirstNonEmpty(info.ServerName, "the server"), ToastSuccess())
						if top := podTop(); top.kind == podPageInfo {
							podPop(config)
						}
					})
					return
				}
			}
			tbxPost(func() { showToast("The Quick Connect code expired", ToastWarn()) })
		}()
	}
	if seed != "" {
		osPromptOpen("Jellyfin server address", "http://192.168.1.20:8096", "Next", seed, 200, start)
		return
	}
	showToast("Looking for Jellyfin on your network…", ToastInfo())
	go func() {
		found := jfDiscover(2 * time.Second)
		guess := ""
		if len(found) > 0 {
			guess = found[0]
		}
		tbxPost(func() {
			osPromptOpen("Jellyfin server address", "http://192.168.1.20:8096", "Next", guess, 200, start)
		})
	}()
}

// podScrobblePage is Scrobbling: Last.fm and ListenBrainz.
func podScrobblePage(config *Config) *podPage {
	p := podMenu("Scrobbling")
	p.rebuild = func(p *podPage) {
		s := musicServersGet()
		lastfm := "Not signed in"
		if s.LastFM.Session != "" {
			lastfm = s.LastFM.User
		}
		lb := "Not connected"
		if s.ListenBrainz.Token != "" {
			lb = s.ListenBrainz.User
		}
		p.entries = []podEntry{
			{label: "Last.fm", value: func() string { return lastfm }, act: lastfmSignInOnScreen},
			{label: "ListenBrainz", value: func() string { return lb }, act: func() { musicServersPhoneOpen("listenbrainz") }},
			{label: "Waiting to Send", value: func() string { return strconv.Itoa(scrobblePending()) }, act: func() {
				scrobbleKick()
				showToast("Sending listens…", ToastInfo())
			}},
		}
		if s.LastFM.Session != "" {
			p.entries = append(p.entries, podEntry{label: "Sign Out of Last.fm", act: func() {
				_ = musicServersUpdate(func(s *musicServersFile) { s.LastFM.User, s.LastFM.Session = "", "" })
				p.rebuild(p)
			}})
		}
	}
	return p
}

// podAboutPage is About.
func podAboutPage() *podPage {
	m := mlibCurrentModel()
	songs, albums := 0, 0
	if m != nil {
		albums = len(m.albums)
		for _, it := range m.items {
			if musicLocalSong(it) {
				songs++
			}
		}
	}
	used, files := musicCacheUsage()
	return &podPage{title: "About", kind: podPageInfo, info: []string{
		"JukaHub " + versionString(appConfig),
		"",
		fmt.Sprintf("Songs on this device: %d", songs),
		fmt.Sprintf("Albums: %d", albums),
		"Servers: " + musicServersSummary(),
		fmt.Sprintf("Song cache: %d songs, %.0f MB", files, float64(used)/(1<<20)),
		"Scrobbles waiting: " + strconv.Itoa(scrobblePending()),
		"",
		"AutoEQ profiles: AutoEq by Jaakko Pasanen (MIT).",
	}}
}

// podInfoPush opens an information page.
func podInfoPush(title string, lines []string) {
	podPush(&podPage{title: title, kind: podPageInfo, info: lines})
}

// ── The context menu ────────────────────────────────────────────────────────

// podConfirm asks before a destructive action.
func podConfirm(question, verb string, yes func()) {
	musicPod.ctx = &podCtxMenu{title: question, entries: []podEntry{
		{label: "Cancel", act: func() {}},
		{label: verb, act: yes},
	}}
}

// podContext opens the context menu of the focused row (or of Now Playing).
func podContext(config *Config) {
	top := podTop()
	var entries []podEntry
	title := "Options"
	if top.kind == podPageNowPlaying || top.kind == podPageVisualizer {
		entries = podNowPlayingContext(config)
		title = "Now Playing"
	} else if top.cursor >= 0 && top.cursor < len(top.entries) && top.entries[top.cursor].item != nil {
		it := *top.entries[top.cursor].item
		title = it.Title
		entries = podItemContext(config, top, it)
	}
	if len(entries) == 0 {
		return
	}
	musicPod.ctx = &podCtxMenu{title: title, entries: entries}
	PlayActivateSound()
}

// podTracksOf fetches a group's tracks (an album, an artist, a playlist).
func podTracksOf(src musicSrcKind, it musicItem, then func([]musicItem)) {
	what := ""
	switch it.Kind {
	case "track":
		then([]musicItem{it})
		return
	case "album":
		what = "albumTracks"
	case "artist":
		what = "artistSongs"
	case "playlist":
		what = "playlistTracks"
	case "genre":
		what = "genreTracks"
	case "composer":
		what = "composerTracks"
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		items, err := musicFetch(ctx, src, musicQuery{What: what, Parent: it})
		tbxPost(func() {
			if err != nil && len(items) == 0 {
				showToast(firstLine(err.Error()), ToastWarn())
				return
			}
			then(items)
		})
	}()
}

// podItemContext is the menu for a library row.
func podItemContext(config *Config, page *podPage, it musicItem) []podEntry {
	src := page.src
	if it.Src != "" && src != musicSrcAll {
		src = it.Src
	}
	var out []podEntry
	switch it.Kind {
	case "track", "album", "artist", "playlist", "genre", "composer":
		out = append(out,
			podEntry{label: "Play", act: func() {
				podTracksOf(src, it, func(items []musicItem) {
					if musicPlayItems(config, items, 0, false) {
						podShowNowPlaying()
					}
				})
			}},
			podEntry{label: "Shuffle", act: func() {
				podTracksOf(src, it, func(items []musicItem) {
					if musicPlayItems(config, items, 0, true) {
						podShowNowPlaying()
					}
				})
			}},
			podEntry{label: "Play Next", act: func() {
				podTracksOf(src, it, func(items []musicItem) {
					n := musicPodPlayNext(items)
					showToast(fmt.Sprintf("%d up next", n), ToastSuccess())
				})
			}},
			podEntry{label: "Add to On-The-Go", act: func() {
				podTracksOf(src, it, func(items []musicItem) {
					n := musicOTGAdd(items)
					showToast(fmt.Sprintf("On-The-Go: %d added", n), ToastSuccess())
				})
			}},
		)
		if it.Src == musicSrcNavidrome || it.Src == musicSrcJellyfin || musicItemHasRemotePart(it) {
			out = append(out, podEntry{label: "Download Offline", act: func() {
				podTracksOf(src, it, func(items []musicItem) {
					n := musicDownloadOffline(musicRemoteOnly(items))
					if n == 0 {
						showToast("Already on this device", ToastInfo())
						return
					}
					showToast(fmt.Sprintf("Downloading %d for offline", n), ToastInfo())
				})
			}})
		}
	case "movie", "episode":
		out = append(out, podEntry{label: "Play", act: func() { podPlayVideo(config, it) }})
		if it.Position > 0 {
			from := it
			from.Position = 0
			out = append(out, podEntry{label: "Play From Start", act: func() { podPlayVideo(config, from) }})
		}
		if it.Src == musicSrcJellyfin {
			out = append(out, podEntry{label: "Download Offline", act: func() {
				if musicDownloadOffline([]musicItem{it}) > 0 {
					showToast("Downloading "+it.Title, ToastInfo())
				} else {
					showToast("Already on this device", ToastInfo())
				}
			}})
		}
	}
	if it.Kind == "track" {
		if it.Album != "" {
			out = append(out, podEntry{label: "Go to Album", act: func() {
				album := musicItem{Kind: "album", Src: it.Src, ID: it.AlbumID, Title: it.Album, Artist: it.Artist}
				if it.Src == musicSrcLocal {
					album.ID = mlibAlbumKey(it.Artist, it.Album)
				}
				podPush(podQueryPage(it.Album, it.Src, musicQuery{What: "albumTracks", Parent: album}, "No songs"))
			}})
		}
		if it.Artist != "" {
			out = append(out, podEntry{label: "Go to Artist", act: func() {
				artist := musicItem{Kind: "artist", Src: it.Src, ID: it.ArtistID, Title: it.Artist}
				if it.Src == musicSrcLocal {
					artist.ID = strings.ToLower(it.Artist)
				}
				podPush(podArtistPage(it.Src, artist))
			}})
		}
		out = append(out, podEntry{label: "Favorite", act: func() { podToggleFavorite(it) }})
		out = append(out, podEntry{label: "Info", act: func() { podInfoPush("Info", musicItemInfo(it)) }})
		if it.Src == musicSrcLocal && it.Path != "" && !isHTTPMediaURL(it.Path) {
			out = append(out,
				podEntry{label: "Rename File…", act: func() { podRenameFile(config, page, it) }},
				podEntry{label: "Delete File", act: func() {
					podConfirm("Delete "+filepath.Base(it.Path)+"?", "Delete", func() { podDeleteFile(config, page, it) })
				}},
			)
		}
	}
	if it.Kind == "playlist" && it.Src == musicSrcLocal {
		if strings.EqualFold(it.ID, musicOTGName) {
			out = append(out, podEntry{label: "Save As Playlist…", act: func() {
				osPromptOpen("Name the playlist", "Road trip", "Save", "", 60, func(name string) {
					podSaveOTGAs(name, page)
				})
			}})
		}
		out = append(out, podEntry{label: "Delete Playlist", act: func() {
			podConfirm("Delete "+it.Title+"?", "Delete", func() {
				if musicPlaylistRemove(it.ID) {
					podLoad(page)
				}
			})
		}})
	}
	return out
}

// musicItemHasRemotePart reports whether a folded row has a server's copy.
func musicItemHasRemotePart(it musicItem) bool {
	for _, p := range it.Parts {
		if p.Src == musicSrcNavidrome || p.Src == musicSrcJellyfin {
			return true
		}
	}
	return false
}

// musicRemoteOnly keeps the server copies of folded rows.
func musicRemoteOnly(items []musicItem) []musicItem {
	out := make([]musicItem, 0, len(items))
	for _, it := range items {
		if it.Src == musicSrcNavidrome || it.Src == musicSrcJellyfin {
			out = append(out, it)
			continue
		}
		for _, p := range it.Parts {
			if p.Src == musicSrcNavidrome || p.Src == musicSrcJellyfin {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// podNowPlayingContext is the menu of Now Playing.
func podNowPlayingContext(config *Config) []podEntry {
	music.mu.Lock()
	cur := music.current
	path := ""
	if cur >= 0 && cur < len(music.tracks) {
		path = music.tracks[cur].Path
	}
	music.mu.Unlock()
	out := []podEntry{
		podSetting("Shuffle", func() string {
			music.mu.Lock()
			defer music.mu.Unlock()
			return onOff(music.shuffle)
		}, func(int) { musicToggleShuffle() }),
		podSetting("Repeat", func() string {
			music.mu.Lock()
			defer music.mu.Unlock()
			return music.repeatMode.short()
		}, func(int) { musicCycleRepeatMode() }),
		podSetting("Speed", func() string {
			music.mu.Lock()
			defer music.mu.Unlock()
			s := music.speed
			if s <= 0 {
				s = 1
			}
			return formatSpeedLabel(s)
		}, func(dir int) { musicCycleSpeed(dir) }),
		podSetting("Sleep Timer", func() string {
			if left := musicSleepRemaining(); left > 0 {
				return formatSleepRemaining(left)
			}
			return "Off"
		}, func(int) { musicCycleSleepTimer() }),
		{label: "Sound Effects", value: musicFXStatus, act: musicFXPanelOpen},
		{label: "Equalizer", act: func() {
			musicEQ.panel = true
			PlayActivateSound()
		}},
	}
	if path == "" {
		return out
	}
	meta, _ := musicMetaFor(path)
	it := musicItem{Kind: "track", Src: meta.Src, ID: meta.ID, Title: meta.Title, Artist: meta.Artist,
		Album: meta.Album, Path: path, Duration: meta.Duration, Art: meta.Art, Track: meta.Track}
	if it.Src == "" {
		it.Src = musicSrcLocal
	}
	if it.Title == "" {
		it.Title = musicTrackTitle(filepath.Base(path))
	}
	out = append(out,
		podEntry{label: "Add to On-The-Go", act: func() {
			showToast(fmt.Sprintf("On-The-Go: %d added", musicOTGAdd([]musicItem{it})), ToastSuccess())
		}},
		podEntry{label: "Favorite", act: func() { podToggleFavorite(it) }},
		podEntry{label: "Lyrics", act: func() {
			if !musicToggleLyrics() {
				showToast("No lyrics for this song", ToastInfo())
				return
			}
			musicPod.npMode = 2
		}},
		podEntry{label: "Info", act: func() { podInfoPush("Info", musicItemInfo(it)) }},
	)
	if it.Src == musicSrcNavidrome || it.Src == musicSrcJellyfin {
		out = append(out, podEntry{label: "Download Offline", act: func() {
			if musicDownloadOffline([]musicItem{it}) > 0 {
				showToast("Downloading "+it.Title, ToastInfo())
			} else {
				showToast("Already on this device", ToastInfo())
			}
		}})
	}
	return out
}

// musicPodPlayNext puts tracks right after the one playing (adding them to
// the list when they are not in it) and reports how many.
func musicPodPlayNext(items []musicItem) int {
	music.mu.Lock()
	loaded := len(music.tracks) > 0
	music.mu.Unlock()
	if !loaded {
		if musicPlayItems(appConfig, items, 0, false) {
			return len(items)
		}
		return 0
	}
	n := 0
	// Front of the queue, in reverse, so the first item plays first.
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind != "track" || it.Path == "" {
			continue
		}
		musicMetaRemember(it)
		music.mu.Lock()
		found := false
		for _, t := range music.tracks {
			if sameAudioPath(t.Path, it.Path) {
				found = true
				break
			}
		}
		if !found {
			music.tracks = append(music.tracks, musicTrack{Path: it.Path, Name: musicItemTrackName(it), Duration: it.Duration})
			if music.shuffle && len(music.order) == len(music.tracks)-1 {
				music.order = append(music.order, len(music.tracks)-1)
			}
		}
		if music.musicQueuePathLocked(it.Path, true) {
			n++
		}
		music.mu.Unlock()
	}
	return n
}

// podToggleFavorite marks or unmarks a song where it lives.
func podToggleFavorite(it musicItem) {
	switch it.Src {
	case musicSrcNavidrome, musicSrcJellyfin:
		on := !it.Starred
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var err error
			if it.Src == musicSrcNavidrome {
				err = ndStar(ctx, it.ID, on)
			} else {
				err = jfFavorite(ctx, it.ID, on)
			}
			tbxPost(func() {
				if err != nil {
					showToast(firstLine(err.Error()), ToastWarn())
					return
				}
				musicSrcCacheClear()
				if on {
					showToast("Favorite: "+it.Title, ToastSuccess())
				} else {
					showToast("Removed favorite: "+it.Title, ToastInfo())
				}
			})
		}()
	default:
		if it.Path == "" {
			return
		}
		if musicToggleFavorite(it.Path) {
			showToast("Favorite: "+it.Title, ToastSuccess())
		} else {
			showToast("Removed favorite: "+it.Title, ToastInfo())
		}
	}
}

// musicItemInfo is a song's Info page.
func musicItemInfo(it musicItem) []string {
	lines := []string{it.Title}
	add := func(label, v string) {
		if strings.TrimSpace(v) != "" {
			lines = append(lines, label+": "+v)
		}
	}
	add("Artist", it.Artist)
	add("Album", it.Album)
	add("Genre", it.Genre)
	if it.Year > 0 {
		add("Year", strconv.Itoa(it.Year))
	}
	if it.Track > 0 {
		add("Track", strconv.Itoa(it.Track))
	}
	if it.Duration > 0 {
		add("Length", formatTime(it.Duration))
	}
	add("Source", it.Src.label())
	add("Format", strings.ToUpper(it.Suffix))
	size := it.Size
	if it.Src == musicSrcLocal && it.Path != "" {
		if st, err := os.Stat(it.Path); err == nil {
			size = st.Size()
		}
		add("File", it.Path)
	}
	if size > 0 {
		add("Size", fmt.Sprintf("%.1f MB", float64(size)/(1<<20)))
		if it.Duration > 0 {
			add("Bit rate", fmt.Sprintf("%d kbps", int(float64(size)*8/it.Duration/1000)))
		}
	}
	return lines
}

// podRenameFile renames a song's file (keeping its extension) and rescans.
func podRenameFile(config *Config, page *podPage, it musicItem) {
	base := filepath.Base(it.Path)
	ext := filepath.Ext(base)
	osPromptOpen("Rename file", "New name", "Rename", strings.TrimSuffix(base, ext), 120, func(name string) {
		name = musicOfflineName(name, "")
		if name == "" || name+ext == base {
			return
		}
		dest := filepath.Join(filepath.Dir(it.Path), name+ext)
		if _, err := os.Stat(dest); err == nil {
			showToast("A file with that name is already there", ToastWarn())
			return
		}
		if err := os.Rename(it.Path, dest); err != nil {
			showToast("Could not rename: "+firstLine(err.Error()), ToastWarn())
			return
		}
		showToast("Renamed to "+name+ext, ToastSuccess())
		mlibStartScan(config, false)
		podReloadSoon(page)
	})
}

// podDeleteFile deletes a song's file and rescans.
func podDeleteFile(config *Config, page *podPage, it musicItem) {
	music.mu.Lock()
	playing := music.current >= 0 && music.current < len(music.tracks) && sameAudioPath(music.tracks[music.current].Path, it.Path)
	music.mu.Unlock()
	if playing {
		musicStop()
	}
	if err := os.Remove(it.Path); err != nil {
		showToast("Could not delete: "+firstLine(err.Error()), ToastWarn())
		return
	}
	showToast("Deleted "+filepath.Base(it.Path), ToastInfo())
	mlibStartScan(config, false)
	podReloadSoon(page)
}

// podReloadSoon reloads a page once the library scan has had a moment.
func podReloadSoon(page *podPage) {
	time.AfterFunc(1500*time.Millisecond, func() {
		tbxPost(func() {
			if page.query != nil {
				podLoad(page)
			}
		})
	})
}

// podSaveOTGAs saves On-The-Go under a name and empties it.
func podSaveOTGAs(name string, page *podPage) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, musicOTGName) {
		return
	}
	pl, ok := musicPlaylistNamed(musicOTGName)
	if !ok {
		return
	}
	pl.Name, pl.Updated = name, time.Now()
	if err := musicPlaylistPut(pl); err != nil {
		showToast(err.Error(), ToastWarn())
		return
	}
	musicPlaylistRemove(musicOTGName)
	showToast("Saved as "+name, ToastSuccess())
	if page != nil && page.query != nil {
		podLoad(page)
	}
}

// ── Input ───────────────────────────────────────────────────────────────────

// podMove moves the cursor and ticks.
func podMove(config *Config, delta int) {
	if m := musicPod.ctx; m != nil {
		n := len(m.entries)
		if n == 0 {
			return
		}
		next := m.cursor + delta
		if next < 0 {
			next = 0
		}
		if next >= n {
			next = n - 1
		}
		if next != m.cursor {
			m.cursor = next
			musicPodTick(config)
		}
		return
	}
	top := podTop()
	n := len(top.entries)
	if n == 0 {
		return
	}
	next := top.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next != top.cursor {
		top.cursor = next
		musicPodTick(config)
	}
}

// podSelect runs the focused row (A / Enter / Right).
func podSelect(config *Config, viaRight bool) {
	if m := musicPod.ctx; m != nil {
		if m.cursor >= 0 && m.cursor < len(m.entries) {
			e := m.entries[m.cursor]
			if e.adjust != nil {
				e.adjust(1)
				PlayNavSound()
				return
			}
			musicPod.ctx = nil
			if e.act != nil {
				e.act()
			}
			if e.open != nil {
				podPush(e.open())
			}
			PlayActivateSound()
		}
		return
	}
	top := podTop()
	if top.cursor < 0 || top.cursor >= len(top.entries) {
		return
	}
	e := top.entries[top.cursor]
	switch {
	case e.adjust != nil:
		e.adjust(1)
		PlayNavSound()
		if top.rebuild != nil {
			top.rebuild(top)
		}
	case e.open != nil:
		podPush(e.open())
		PlayActivateSound()
	case e.act != nil && !viaRight:
		e.act()
		PlayActivateSound()
		if t := podTop(); t == top && top.rebuild != nil {
			top.rebuild(top)
		}
	case e.act != nil && e.more:
		e.act()
	}
}

// podAdjust is Left/Right on a settings row; it reports whether the row took
// it.
func podAdjust(dir int) bool {
	var e podEntry
	if m := musicPod.ctx; m != nil {
		if m.cursor < 0 || m.cursor >= len(m.entries) {
			return false
		}
		e = m.entries[m.cursor]
	} else {
		top := podTop()
		if top.cursor < 0 || top.cursor >= len(top.entries) {
			return false
		}
		e = top.entries[top.cursor]
	}
	if e.adjust == nil {
		return false
	}
	e.adjust(dir)
	PlayNavSound()
	if top := podTop(); top.rebuild != nil && musicPod.ctx == nil {
		top.rebuild(top)
	}
	return true
}

// podBack is B / Esc / Left.
func podBack(config *Config) {
	if musicPod.ctx != nil {
		musicPod.ctx = nil
		PlayBackSound()
		return
	}
	podPop(config)
}

// podVolume changes the volume on Now Playing and shows the bar.
func podVolume(delta float64) {
	musicAdjustVolumeQuiet(delta)
	musicPod.volAt = time.Now()
}

// musicAdjustVolumeQuiet is musicAdjustVolume without the toast: the iPod
// screen shows its own volume bar.
func musicAdjustVolumeQuiet(delta float64) {
	music.mu.Lock()
	v := music.volume + delta
	music.mu.Unlock()
	musicSetVolume(v)
}

// podNowPlayingKey handles a key on Now Playing (and the visualizer page);
// it reports whether it was one of them.
func podNowPlayingKey(sym sdl.Keycode, config *Config) bool {
	top := podTop()
	if musicPod.ctx != nil || (top.kind != podPageNowPlaying && top.kind != podPageVisualizer) {
		return false
	}
	switch sym {
	case sdl.K_UP:
		podVolume(0.05)
	case sdl.K_DOWN:
		podVolume(-0.05)
	case sdl.K_LEFT:
		if top.kind == podPageVisualizer {
			musicVizStep(config, -1)
		} else {
			musicPrev()
			musicPodTick(config)
		}
	case sdl.K_RIGHT:
		if top.kind == podPageVisualizer {
			musicVizStep(config, 1)
		} else {
			musicNext()
			musicPodTick(config)
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		musicTogglePause()
	case sdl.K_y:
		if top.kind == podPageNowPlaying {
			musicPod.npMode = (musicPod.npMode + 1) % 3
			if musicPod.npMode == 2 {
				music.mu.Lock()
				has := len(music.lyrics) > 0
				music.showLyrics = has
				music.mu.Unlock()
				if !has {
					musicPod.npMode = 0
				}
			}
			PlayNavSound()
		}
	default:
		return false
	}
	return true
}

// musicPodKey is the layout's keyboard.
func musicPodKey(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if len(musicPod.stack) == 0 {
		musicPodEnter(config)
	}
	sym := e.Keysym.Sym
	if podNowPlayingKey(sym, config) {
		return
	}
	switch sym {
	case sdl.K_UP:
		podMove(config, -1)
	case sdl.K_DOWN:
		podMove(config, 1)
	case sdl.K_PAGEUP:
		podMove(config, -podPageRows())
	case sdl.K_PAGEDOWN:
		podMove(config, podPageRows())
	case sdl.K_HOME:
		podMove(config, -100000)
	case sdl.K_END:
		podMove(config, 100000)
	case sdl.K_RETURN:
		podSelect(config, false)
	case sdl.K_RIGHT:
		if !podAdjust(1) {
			podSelect(config, true)
		}
	case sdl.K_LEFT:
		if !podAdjust(-1) {
			podBack(config)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		podBack(config)
	case sdl.K_x:
		if musicPod.ctx != nil {
			musicPod.ctx = nil
			PlayBackSound()
		} else {
			podContext(config)
		}
	case sdl.K_y:
		podShowNowPlaying()
	case sdl.K_SPACE, sdl.K_p:
		musicTogglePause()
	case sdl.K_n:
		musicNext()
	case sdl.K_b:
		musicPrev()
	case sdl.K_SLASH, sdl.K_F2:
		podSearch(musicSourceFromConfig(config))
	case sdl.K_o:
		musicToggleScreenOff()
	case sdl.K_k:
		openMusicSettingsPanel(config)
	case sdl.K_g:
		musicPEQTogglePanel(config)
	case sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS, sdl.K_VOLUMEUP, sdl.K_VOLUMEDOWN:
		musicHandleVolumeKey(e)
		musicPod.volAt = time.Now()
	}
}

// musicPodPad is the layout's pad; it reports whether the press was used.
func musicPodPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if len(musicPod.stack) == 0 {
		musicPodEnter(config)
	}
	key := sdl.Keycode(0)
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		key = sdl.K_UP
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		key = sdl.K_DOWN
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		key = sdl.K_LEFT
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		key = sdl.K_RIGHT
	case sdl.CONTROLLER_BUTTON_A:
		key = sdl.K_RETURN
	case sdl.CONTROLLER_BUTTON_B:
		key = sdl.K_ESCAPE
	case sdl.CONTROLLER_BUTTON_X:
		key = sdl.K_x
	case sdl.CONTROLLER_BUTTON_Y:
		key = sdl.K_y
	case sdl.CONTROLLER_BUTTON_START:
		musicTogglePause()
		return true
	case sdl.CONTROLLER_BUTTON_BACK:
		musicToggleScreenOff()
		return true
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		musicPrev()
		musicPodTick(config)
		return true
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		musicNext()
		musicPodTick(config)
		return true
	// Lyrics and the sleep timer are on X's Now Playing menu: the stick
	// clicks that used to be their shortcuts are not on every handheld
	// (stickclicks.go).
	case sdl.CONTROLLER_BUTTON_GUIDE:
		podPopToRoot()
		return true
	default:
		return false
	}
	// Y on Now Playing changes the picture; elsewhere it jumps there.
	musicPodKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: key}}, config)
	return true
}

// musicPodShoulder takes L2/R2: a page of rows in a list, a 10 s seek on Now
// Playing.
func musicPodShoulder(s padShoulder, config *Config) bool {
	if !musicPodActive(config) || musicPanelsOpen() {
		return false
	}
	dir := -1
	if s == padR2 {
		dir = 1
	}
	top := podTop()
	if top.kind == podPageNowPlaying || top.kind == podPageVisualizer {
		musicSeekBy(float64(10 * dir))
		return true
	}
	podMove(config, dir*podPageRows())
	return true
}

// musicPodWheel scrolls a list a row a notch.
func musicPodWheel(delta int32) bool {
	if !musicSceneActive() || !musicPodActive(appConfig) || musicPanelsOpen() {
		return false
	}
	switch {
	case delta > 0:
		podMove(appConfig, -1)
	case delta < 0:
		podMove(appConfig, 1)
	}
	return true
}

// musicPodClick handles a tap: a row runs, the header's left end goes back,
// Now Playing's bar seeks and its buttons play.
func musicPodClick(mx, my int32, config *Config) bool {
	if m := musicPod.ctx; m != nil {
		for i, r := range musicPod.ctxRows {
			if pointInRect(mx, my, r) && i < len(m.entries) {
				m.cursor = i
				podSelect(config, false)
				return true
			}
		}
		musicPod.ctx = nil
		return true
	}
	if pointInRect(mx, my, musicPod.back) {
		podBack(config)
		return true
	}
	top := podTop()
	if top.kind == podPageNowPlaying {
		switch {
		case pointInRect(mx, my, musicPod.npSeek) && musicPod.npSeek.W > 0:
			music.mu.Lock()
			dur := music.duration
			music.mu.Unlock()
			if dur > 0 {
				musicSeek(dur * float64(mx-musicPod.npSeek.X) / float64(musicPod.npSeek.W))
			}
		case pointInRect(mx, my, musicPod.npPrev):
			musicPrev()
		case pointInRect(mx, my, musicPod.npPlay):
			musicTogglePause()
		case pointInRect(mx, my, musicPod.npNext):
			musicNext()
		case pointInRect(mx, my, musicPod.npArt):
			podNowPlayingKey(sdl.K_y, config)
		}
		return true
	}
	for i, r := range musicPod.rows {
		if pointInRect(mx, my, r) {
			top.cursor = musicPod.rowBase + i
			podSelect(config, false)
			return true
		}
	}
	return true
}

// podPageRows is how many rows a page shows (a page for L2/R2).
func podPageRows() int {
	if n := len(musicPod.rows); n > 1 {
		return n - 1
	}
	return 8
}

// musicPlaylistRemove deletes a saved playlist by name.
func musicPlaylistRemove(name string) bool {
	musicPlaylistState.mu.Lock()
	defer musicPlaylistState.mu.Unlock()
	musicPlaylistsLoadLocked()
	for i, pl := range musicPlaylistState.items {
		if strings.EqualFold(pl.Name, name) {
			musicPlaylistState.items = append(musicPlaylistState.items[:i], musicPlaylistState.items[i+1:]...)
			musicPlaylistsSaveLocked()
			return true
		}
	}
	return false
}
