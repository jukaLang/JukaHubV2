package main

// media_hub.go — the Media screen (config scene "Tube"), drawn and driven in Go.
//
// The screen used to be the generic element renderer over a text field, four
// buttons and a one-column results list: no downloads, no history, nothing that
// worked offline, and a search that took a long time to say it could not reach
// the network. It is now a registered scene module (scene.go) laid out in
// Home's glass:
//
//   - a glass search bar — the on-screen keyboard edits the scene's own
//     search_query field, so a typed query (or a pasted link) runs through the
//     same search trigger, cache and paging as before — and a strip of tab
//     chips: For you, Trending, Shorts, Downloads, History, Favorites,
//     Library, and the Live TV and Podcasts shortcuts;
//   - For you is rows of cards (Continue watching, Trending, your downloads,
//     saved videos), every other tab a grid of thumbnail cards with the
//     duration, a Downloaded badge and a watched-progress bar; Downloads is a
//     list with sizes, dates, the storage limit and removal;
//   - Library is the films, shows and music already on the card, scanned in
//     the background and grouped into rows (media_hub_library.go, mlib.go);
//   - Y opens a details sheet: play, resume, download, favourite, Up Next,
//     Watch Party, copy link, remove the download; on Favorites, X removes
//     with an Undo bar (media_hub_favs.go);
//   - offline it says so at once, lands on Downloads and dials nothing; when
//     the connection comes back the online rows load by themselves.
//
// The scene's config elements stay as data: the input element is the
// keyboard's target, the searchresults element names the variable results
// land in, and the video element is the player every playback in the app draws
// into (sceneWithVideoElement). While a video is on screen this module draws
// that player itself.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// mediaHubSceneName is the config scene this module owns. The name stays
	// "Tube" so every config, achievement and shortcut keeps working; the
	// header calls it Media (sceneDisplayName).
	mediaHubSceneName = "Tube"
	// mediaTrendingVar holds the Trending list. It is runtime data, kept out of
	// jukauser.json (see init).
	mediaTrendingVar = "media_trending"
	// mediaResultsVar is the search results variable the scene's searchresults
	// element names; the keyboard's submit fills it.
	mediaResultsVar = "search_results"
	// mediaShortsVar is the Shorts feed the Shorts scene shares.
	mediaShortsVar = "shorts_list"
	// mediaRecentSearchKey keeps the last searches (persisted with the user
	// config like any other Custom value).
	mediaRecentSearchKey = "media_recent_searches"
	// mediaHubPageSize is one fetch: a grid row is four cards, and the native
	// search answers twenty per request, so one request fills five rows.
	mediaHubPageSize = 20
	// mediaRecentSearchMax bounds the recent-search chips.
	mediaRecentSearchMax = 8
	// mediaRowCards bounds a For you row: rows are a taste, the tabs are the lists.
	mediaRowCards = 12
	// mediaRebuildEvery refreshes progress bars and badges while on screen.
	mediaRebuildEvery = uint64(2000)
	// mediaQueuePollEvery is how often the Downloads tab re-reads the queue
	// while something is transferring.
	mediaQueuePollEvery = uint64(250)
	// mediaTrendingStale is how old the Trending list may be before entering
	// Media refreshes it.
	mediaTrendingStale = 30 * time.Minute
	// mediaSearchWait bounds the "Searching…" state when a search never
	// reports back (the search itself is bounded by mediaSearchTimeout).
	mediaSearchWait = 90 * time.Second
	// mediaHubPHQuery is the search command for the "vault" tab's default list.
	// The name is intentionally vague ("vault") in the UI and code to keep the
	// feature's purpose unclear on a quick scan of the binary or config.
	// yt-dlp's built-in extractor handles the site by domain, so the search
	// prefix here is just ytsearch like any other: the extractor is what knows
	// the destination.
	mediaPHQuery = "vault_top"
)

// mediaTab is one chip of the strip.
type mediaTab int

const (
	mediaTabForYou mediaTab = iota
	mediaTabSearch
	mediaTabTrending
	mediaTabShorts
	mediaTabDownloads
	mediaTabHistory
	mediaTabFavorites
	mediaTabLibrary
	mediaTabLiveTV
	mediaTabPodcasts
	mediaTabPH
	mediaTabCount
)

// mediaTabLabel and mediaTabIcon name a chip. Icons are canonical names drawn
// by DrawHomeIcon.
func mediaTabLabel(t mediaTab) string {
	switch t {
	case mediaTabSearch:
		return "Results"
	case mediaTabTrending:
		return "Trending"
	case mediaTabShorts:
		return "Shorts"
	case mediaTabDownloads:
		return "Downloads"
	case mediaTabHistory:
		return "History"
	case mediaTabFavorites:
		return "Favorites"
	case mediaTabLibrary:
		return "Library"
	case mediaTabLiveTV:
		return "Live TV"
	case mediaTabPodcasts:
		return "Podcasts"
	case mediaTabPH:
		return "Vault"
	}
	return "For you"
}

func mediaTabIcon(t mediaTab) string {
	switch t {
	case mediaTabSearch:
		return "search"
	case mediaTabTrending:
		return "sparkles"
	case mediaTabShorts:
		return "video"
	case mediaTabDownloads:
		return "download"
	case mediaTabHistory:
		return "clock"
	case mediaTabFavorites:
		return "heart"
	case mediaTabLibrary:
		return "grid"
	case mediaTabLiveTV:
		return "globe"
	case mediaTabPodcasts:
		return "mic"
	case mediaTabPH:
		return "filmstrip"
	}
	return "home"
}

// mediaTabShortcut reports a chip that opens another screen instead of a list.
func mediaTabShortcut(t mediaTab) bool { return t == mediaTabLiveTV || t == mediaTabPodcasts }

// mediaTabNeedsNetwork reports a tab whose list only exists online.
func mediaTabNeedsNetwork(t mediaTab) bool {
	switch t {
	case mediaTabTrending, mediaTabShorts, mediaTabLiveTV, mediaTabPodcasts, mediaTabPH:
		return true
	}
	return false
}

// mediaZone is where the D-pad focus is.
type mediaZone int

const (
	mediaZoneSearch mediaZone = iota
	mediaZoneTabs
	mediaZoneContent
)

// mediaRowKind names a For you row.
type mediaRowKind int

const (
	mediaRowContinue mediaRowKind = iota
	mediaRowRecentSearch
	mediaRowTrending
	mediaRowDownloads
	mediaRowSaved
	// The Library's rows (media_hub_library.go).
	mediaRowLibContinue
	mediaRowLibRecent
	mediaRowLibMovies
	mediaRowLibShows
	mediaRowLibMusic
)

// mediaCard is one thumbnail card, with every string it draws measured out of
// the frame loop: the strings are built when the list changes, never per frame.
type mediaCard struct {
	video    VideoInfo
	title    string
	meta     string
	detail   string
	duration string
	art      [2]string
	progress float64

	downloaded bool
	favorite   bool
	vertical   bool

	// A library entry (plays from the card, works offline).
	libID     string
	path      string
	overLimit bool
	kept      bool

	// A live queue row on the Downloads tab.
	queueID     int64
	queueStatus downloadStatus
	queuePct    float64
	queued      bool

	// A Media Library card (media_hub_library.go): what it stands for, the
	// group or file behind it, the glyph its placeholder shows, and whether
	// its picture is shown whole (an album cover) rather than cropped.
	mlibKind   mlibCardKind
	mlibKey    string
	glyph      string
	artContain bool
}

// mediaRow is one For you row.
type mediaRow struct {
	kind   mediaRowKind
	title  string
	cards  []mediaCard
	chips  []string
	focus  int
	scroll float64
}

// mediaSliceSig identifies a published result list without copying it.
type mediaSliceSig struct {
	n     int
	first *VideoInfo
}

// mediaBuildKey is everything the visible list is built from; a change of any
// part rebuilds it.
type mediaBuildKey struct {
	tab      mediaTab
	libGen   uint64
	favSig   [4]int64
	trending mediaSliceSig
	results  mediaSliceSig
	shorts   mediaSliceSig
	online   bool
	recent   int
	local    int
	queue    int64
	limit    int
	mlib     uint64
}

// mediaHitKind is what a drawn rect does when it is clicked.
type mediaHitKind int

const (
	mediaHitSearch mediaHitKind = iota
	mediaHitTab
	mediaHitCard
	mediaHitRecent
	mediaHitChip
	mediaHitSheetAction
	mediaHitSheetScrim
	mediaHitConfirm
	mediaHitStateAction
	// mediaHitRemove is the bin on a focused Favorites card, mediaHitUndo the
	// Undo bar's button (media_hub_favs.go).
	mediaHitRemove
	mediaHitUndo
)

// mediaHit is one clickable rect recorded while drawing, so a tap resolves what
// was actually painted.
type mediaHit struct {
	kind mediaHitKind
	a, b int
	rect sdl.Rect
}

// mediaSheetAction is one row of the details sheet.
type mediaSheetAction int

const (
	mediaActPlay mediaSheetAction = iota
	mediaActPlayStart
	mediaActDownload
	mediaActRemoveDownload
	mediaActFavorite
	mediaActUpNext
	mediaActParty
	mediaActCopy
	mediaActRetry
	mediaActCancelDownload
	// mediaActOpen opens a Library show, album or See all list.
	mediaActOpen
	// mediaActDownloadAudio saves a web video's sound as a track (yt-dlp).
	mediaActDownloadAudio
	// mediaActExtractAudio saves the sound of a video on the card as a track
	// (ffmpeg, mediaextract.go).
	mediaActExtractAudio
)

// mediaSheet is the details sheet for one card.
type mediaSheet struct {
	open    bool
	card    mediaCard
	actions []mediaSheetAction
	labels  []string
	focus   int
	lines   []string
	info    string
}

// mediaConfirmKind is what a confirmation does on OK.
type mediaConfirmKind int

const (
	mediaConfirmRemove mediaConfirmKind = iota
	mediaConfirmRemoveOldest
	// mediaConfirmFavDownload asks whether a removed favourite's download
	// goes too (media_hub_favs.go).
	mediaConfirmFavDownload
)

// mediaConfirm is the small modal that guards a deletion. cancel names the
// left button when it is not "Cancel".
type mediaConfirm struct {
	open   bool
	kind   mediaConfirmKind
	libID  string
	ids    []string
	title  string
	body   string
	ok     string
	cancel string
	focus  int // 0 = Cancel, 1 = OK
	freed  int64
	back   mediaZone
}

// mediaHub is the screen's state. It is only touched on the main thread.
var mediaHub = struct {
	tab      mediaTab
	tabs     [mediaTabCount]mediaTab
	tabN     int
	zone     mediaZone
	tabFocus int

	rows []mediaRow
	row  int

	cards   []mediaCard
	index   int
	scrollY float64

	// chipFocus is the Downloads header chip under the ring, -1 on the list.
	chipFocus int
	chipN     int

	built   mediaBuildKey
	builtAt uint64
	queueAt uint64
	queueN  int64

	query         string
	searchGen     uint64
	searchPending bool
	searchAt      time.Time
	hasSearch     bool
	localResults  []dlLibraryEntry
	localSearch   bool
	recent        []string

	online        bool
	onlineKnown   bool
	backOnlineAt  uint64
	offlineLanded bool
	deferLoad     bool

	sheet   mediaSheet
	confirm mediaConfirm

	hits []mediaHit

	playerShown  bool
	chromeHeld   bool
	freeAt       time.Time
	footer       []FooterHint
	footerKey    int
	playerElem   Element
	playerRect   sdl.Rect
	playerCanvas [2]int32
	downloadsN   int
	downloadsTag string
}{tab: mediaTabForYou, chipFocus: -1, footerKey: -1}

// mediaPlayOrigin is the scene a playback was started from, recorded where
// every Play routes through (ensureVideoScene). Media's own Back keeps a video
// it started floating over its browser; a file opened from Files returns there.
var mediaPlayOrigin struct {
	mu    sync.Mutex
	scene string
}

func init() {
	registerScene(mediaHubScene{sceneBase{name: mediaHubSceneName}})
	// Runtime lists, rebuilt every session: never written into jukauser.json.
	volatileCustomVars[mediaTrendingVar] = true
	mediaFreeBytes.Store(-1)
}

// mediaHubScene is the registry entry (scene.go).
type mediaHubScene struct{ sceneBase }

func (mediaHubScene) render(r *sdl.Renderer, c *Config) bool {
	mediaHubRender(r, c)
	return true
}

func (mediaHubScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return mediaHubHandleKey(e, c)
}

func (mediaHubScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return mediaHubHandleController(e, c)
}

// mediaHubOwnsScene reports whether a scene name is the Media screen.
func mediaHubOwnsScene(name string) bool { return name == mediaHubSceneName }

// mediaHubIsCurrent reports whether the Media screen is the scene on screen.
func mediaHubIsCurrent(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		mediaHubOwnsScene(config.Scenes[currentSceneIndex].Name)
}

// mediaNotePlayOrigin records the scene a playback is being started from. It
// is called by ensureVideoScene, before it switches to the player scene.
func mediaNotePlayOrigin(config *Config) {
	name := ""
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		name = config.Scenes[currentSceneIndex].Name
	}
	mediaPlayOrigin.mu.Lock()
	mediaPlayOrigin.scene = name
	mediaPlayOrigin.mu.Unlock()
}

// mediaForgetPlayOrigin clears the origin when playback ends, so a session
// started later without going through ensureVideoScene is not Media's.
func mediaForgetPlayOrigin() {
	mediaPlayOrigin.mu.Lock()
	mediaPlayOrigin.scene = ""
	mediaPlayOrigin.mu.Unlock()
}

// mediaHubOwnsPlayback reports whether the session on screen was started from
// the Media screen itself.
func mediaHubOwnsPlayback() bool {
	mediaPlayOrigin.mu.Lock()
	origin := mediaPlayOrigin.scene
	mediaPlayOrigin.mu.Unlock()
	return origin == mediaHubSceneName && playerPhase() != "idle"
}

// mediaHubPlayerOnScreen reports whether the Media scene is drawing the player
// right now: a session is live and it is not floating in the mini window.
func mediaHubPlayerOnScreen() bool {
	if playerPhase() == "idle" {
		return false
	}
	return !videoMiniActive() || videoFullscreen
}

// ── Entry points ─────────────────────────────────────────────────────────────

// mediaOpenDownloads opens Media on its Downloads view (cross-area contract:
// other areas call it with exactly this name — Home's search index and the
// Settings model do; the Start menu and the activity feed still open the
// transfer queue panel, openDownloadsPanel).
func mediaOpenDownloads(config *Config) {
	dlLibraryRefresh()
	mediaHubSelectTab(config, mediaTabDownloads, false)
	mediaHub.zone = mediaZoneContent
	mediaHub.chipFocus = -1
	if config == nil {
		return
	}
	idx := sceneIndexExact(config, mediaHubSceneName)
	if idx < 0 {
		// A config without the Media scene still has the queue panel.
		openDownloadsPanel()
		return
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// mediaHubEnter runs when the Media scene becomes current (core.go's scene
// transition). Loads start here, never from the frame loop; offline nothing
// dials and the screen lands on Downloads.
func mediaHubEnter(config *Config) {
	dlSettingsSync(config)
	dlLibraryRefresh()
	mediaHubLoadRecentSearches(config)
	mediaHub.online = IsOnline()
	mediaHub.onlineKnown = true
	mediaHub.sheet = mediaSheet{}
	mediaHub.confirm = mediaConfirm{}
	mediaHub.builtAt = 0
	mediaHub.footerKey = -1
	// Only a search started from here on moves the screen to Results; one that
	// ran before Media was entered is not the user's current question.
	mediaSearchMu.Lock()
	mediaHub.searchGen = mediaSearchGen[mediaResultsVar]
	mediaSearchMu.Unlock()
	if mediaHub.tab == mediaTabLibrary {
		// The Library is all on the card: it opens offline as well.
		mlibEnterTab(config)
	}
	if !mediaHub.online {
		if mediaTabNeedsNetwork(mediaHub.tab) || mediaHub.tab == mediaTabForYou || mediaHub.tab == mediaTabSearch {
			mediaHubSelectTab(config, mediaTabDownloads, false)
			mediaHub.offlineLanded = true
		}
		mediaHub.zone = mediaZoneContent
		return
	}
	mediaHub.offlineLanded = false
	if videoPrepareActive() || playerPhase() != "idle" {
		// Media hosts every player in the app, so it is often entered just to
		// show a video (a file from Files, Home's Continue card): the rows load
		// once the browser itself is on screen, not behind the picture.
		mediaHub.deferLoad = true
		return
	}
	mediaHubLoadTrending(config, false)
	if mediaHub.tab == mediaTabShorts {
		mediaHubLoadShorts(config)
	}
	if mediaHub.tab == mediaTabPH {
		mediaHubLoadFeed(config, false)
	}
	// An old yt-dlp is the usual reason YouTube stops loading videos. Opening
	// Media is when to look, in the background and at most twice a day
	// (ytdlp_update.go); the native search above needs no helper meanwhile.
	go requestYtDlpUpdate(config, ytdlpTriggerRoutine, nil)
}

// mediaHubTabShown is the tab on screen while Media is up, -1 while it is not.
// mediaHubTick writes it on the main loop; background work that must not read
// mediaHub itself (the search a yt-dlp update re-runs) reads it instead.
var mediaHubTabShown atomic.Int32

// mediaHubSearchRetryBlocked reports whether re-running the search behind the
// Results tab would take the screen from the user: Media is up on another
// tab, and a new search generation makes it switch to Results (mediaHubTick).
// On another screen — the legacy Tube scene, say — a re-run moves nothing.
func mediaHubSearchRetryBlocked() bool {
	shown := mediaHubTabShown.Load()
	return shown >= 0 && shown != int32(mediaTabSearch)
}

// mediaHubTick runs every frame from the main loop. It is a few comparisons
// unless the screen is up: connectivity changes, a finished search, and the
// settings mirror.
func mediaHubTick(config *Config) {
	if !mediaHubIsCurrent(config) {
		mediaHubTabShown.Store(-1)
		// The Library's scan is Media's: it stops when Media is left and the
		// next visit carries on from what it finished (mlib.go).
		if mlibScanning.Load() {
			mlibCancelScan()
		}
		return
	}
	mediaHubTabShown.Store(int32(mediaHub.tab))
	if transitionPhase != "none" {
		return
	}
	online := IsOnline()
	if !mediaHub.onlineKnown {
		mediaHub.online = online
		mediaHub.onlineKnown = true
	}
	if online != mediaHub.online {
		mediaHub.online = online
		mediaHub.builtAt = 0
		if online {
			// Back online: the rows that need the network load by themselves,
			// with no button to press.
			mediaHub.backOnlineAt = sdl.GetTicks64()
			mediaHubLoadTrending(config, false)
			if mediaHub.tab == mediaTabShorts {
				mediaHubLoadShorts(config)
			}
			if mediaHub.offlineLanded && mediaHub.tab == mediaTabDownloads && mediaHub.zone != mediaZoneContent {
				mediaHubSelectTab(config, mediaTabForYou, false)
			}
			mediaHub.offlineLanded = false
		}
	}
	// A search the keyboard started (vkTriggerSceneSearch) shows up as a new
	// generation of the results variable: follow it onto the Results tab.
	mediaSearchMu.Lock()
	gen := mediaSearchGen[mediaResultsVar]
	mediaSearchMu.Unlock()
	if gen != mediaHub.searchGen {
		mediaHub.searchGen = gen
		if !mediaHub.localSearch {
			mediaHub.hasSearch = true
			mediaHub.searchPending = true
			mediaHub.searchAt = time.Now()
			if q, ok := config.Variables.Custom["search_query"].(string); ok && strings.TrimSpace(q) != "" {
				mediaHub.query = strings.TrimSpace(q)
			}
			mediaHubSelectTab(config, mediaTabSearch, false)
		}
	}
	if mediaHub.searchPending {
		_, have := config.Variables.Custom[mediaResultsVar].([]VideoInfo)
		errVal, hasErr := config.Variables.Custom["search_error"]
		if have || (hasErr && errVal != nil) || time.Since(mediaHub.searchAt) > mediaSearchWait {
			mediaHub.searchPending = false
		}
	}
	if mediaHubPlayerOnScreen() {
		if !mediaHub.playerShown {
			mediaHub.playerShown = true
			mediaHub.sheet = mediaSheet{}
		}
	} else if mediaHub.playerShown {
		mediaHub.playerShown = false
		mediaHub.builtAt = 0
		// Where a library video was left feeds the Library's Continue
		// watching, beyond the player's twenty-entry recent list.
		go mlibSyncResume()
	}
	if mediaHub.deferLoad && !mediaHubPlayerOnScreen() && !videoPrepareActive() {
		mediaHub.deferLoad = false
		mediaHubLoadTrending(config, false)
		if mediaHub.tab == mediaTabShorts {
			mediaHubLoadShorts(config)
		}
	}
	if frameCount%30 == 0 {
		dlSettingsSync(config)
	}
}

// ── Tabs ─────────────────────────────────────────────────────────────────────

// mediaTabOrder is the strip after the Results chip, a fixed array so building
// the strip every frame allocates nothing.
var mediaTabOrder = [...]mediaTab{mediaTabForYou, mediaTabTrending, mediaTabShorts, mediaTabDownloads,
	mediaTabHistory, mediaTabFavorites, mediaTabLibrary, mediaTabLiveTV, mediaTabPodcasts, mediaTabPH}

// mediaHubTabs returns the chips in strip order. Results joins the strip once a
// search has run.
func mediaHubTabs() []mediaTab {
	n := 0
	if mediaHub.hasSearch {
		mediaHub.tabs[n] = mediaTabSearch
		n++
	}
	for _, t := range mediaTabOrder {
		mediaHub.tabs[n] = t
		n++
	}
	mediaHub.tabN = n
	return mediaHub.tabs[:n]
}

// mediaHubTabIndex is a tab's position in the strip, or -1.
func mediaHubTabIndex(t mediaTab) int {
	for i, cur := range mediaHubTabs() {
		if cur == t {
			return i
		}
	}
	return -1
}

// mediaHubSelectTab shows a tab. sound is false for selections the code makes
// on the user's behalf (offline landing, a search starting).
func mediaHubSelectTab(config *Config, t mediaTab, sound bool) {
	if t == mediaTabSearch && !mediaHub.hasSearch {
		t = mediaTabForYou
	}
	changed := mediaHub.tab != t
	if changed {
		mediaHub.tab = t
		mediaHub.index = 0
		mediaHub.row = 0
		mediaHub.scrollY = 0
		mediaHub.chipFocus = -1
		mediaHub.builtAt = 0
		if sound {
			PlayNavSound()
		}
	}
	if i := mediaHubTabIndex(t); i >= 0 {
		mediaHub.tabFocus = i
	}
	if t == mediaTabShorts && config != nil {
		if vids, _ := config.Variables.Custom[mediaShortsVar].([]VideoInfo); len(vids) == 0 {
			mediaHubLoadShorts(config)
		}
	}
	if t == mediaTabPH && config != nil {
		if vids, _ := config.Variables.Custom[mediaPHQuery].([]VideoInfo); len(vids) == 0 {
			mediaHubLoadFeed(config, false)
		}
	}
	if t == mediaTabDownloads {
		dlLibraryEnsureLoaded()
		mediaHubRefreshFree()
	}
	if t == mediaTabLibrary && changed {
		mlibEnterTab(config)
	}
	if changed && config != nil {
		// Build the new tab now rather than at the next frame: the press that
		// chose it decides where the focus goes from the new tab's cards.
		mediaHubEnsureBuilt(config)
	}
}

// mediaHubStepTab moves the selection one data tab left or right, skipping the
// shortcut chips (they open other screens and are only pressed on purpose).
func mediaHubStepTab(config *Config, delta int) {
	tabs := mediaHubTabs()
	cur := mediaHubTabIndex(mediaHub.tab)
	for step := 0; step < len(tabs); step++ {
		cur += delta
		if cur < 0 || cur >= len(tabs) {
			return
		}
		if !mediaTabShortcut(tabs[cur]) {
			mediaHubSelectTab(config, tabs[cur], true)
			return
		}
	}
}

// mediaHubOpenShortcut opens the screen behind a shortcut chip.
func mediaHubOpenShortcut(config *Config, t mediaTab) {
	if config == nil {
		return
	}
	if !IsOnline() {
		ShowToast(offlineReasonFor(mediaTabLabel(t)), ToastKindWarning)
		return
	}
	PlayActivateSound()
	switch t {
	case mediaTabLiveTV:
		if idx := sceneIndexExact(config, "LiveTV"); idx >= 0 {
			changeSceneTo(config, idx)
			return
		}
		ShowToast("This build has no Live TV screen", ToastKindInfo)
	case mediaTabPodcasts:
		if idx := sceneIndexExact(config, "Podcasts"); idx >= 0 {
			changeSceneTo(config, idx)
			return
		}
		openPodcastPanel()
	}
}

// ── Loading ──────────────────────────────────────────────────────────────────

// mediaHubCommand builds a yt-dlp search command for query (a literal or the
// $search_query placeholder) with Media's page size. It is the command the
// native search path answers in-process (youtubesearch.go), so the extractor
// only runs when that fails.
func mediaHubCommand(config *Config, query string) string {
	base := fmt.Sprintf(`yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end %d "ytsearch%d:%s"`,
		mediaHubPageSize, mediaHubPageSize, query)
	if extra := ytDlpExtraArgs(config); extra != "" {
		return base + " " + extra
	}
	return base
}

// encodedSiteURL is the base64-encoded URL for the Vault tab.
// Decoding it at runtime avoids the plaintext URL appearing in strings
// dumps, greps, or binary scans. The decoded value is the site yt-dlp
// will query; swap the encoded value below to retarget the tab.
//
// Current URL: RedGifs search (tags=Test)
//
// RedGifs extractor (RedGifsSearchIE) supports search/listings via
// --flat-playlist. URL format: /browse?tags=QUERY
//
// The Vault tab supports search: when on the Vault tab and the user searches
// (via the search bar or keyboard), the query is sent to RedGifs as a tags=
// parameter instead of YouTube search. The search query is stored in the
// vault_search_query config variable.
//
// RedGifs URL examples (encode these to replace encodedSiteURL):
//   - Search (test):       aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9VGVzdA==
//   - Search (Lesbian):    aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9TGVzYmlhbg==
//   - Search (Girl):       aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9R2lybA==
//   - Search (Hot):        aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9SG90
//   - Search (Cute):       aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9Q3V0ZQ==
//   - Search (Anime):      aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9QW5pbWU=
//   - Browse (no filter):  aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3Nl  (may need tags param)
//
// PornHub URL examples (also works but may show SFW/censored):
//   - Search (maiko):     aHR0cHM6Ly93d3cucG9ybmh1Yi5jb20vdmlkZW8vc2VhcmNoP3NlYXJjaD1tYWlrbyZvPW12
//
// Other sites - extractors exist but do NOT support search/listings:
//   - xvideos.com:        aHR0cHM6Ly93d3cueHZpZGVvcy5jb20= (video pages only)
//   - xnxx.com:           aHR0cHM6Ly93d3cueG54eC5jb20= (video pages only)
//   - xhamster.com:       aHR0cHM6Ly93d3cueGhhbXN0ZXIuY29t (video pages only)
//
// Other site URL examples:
//   - PornHub front page: aHR0cHM6Ly93d3cucG9ybmh1Yi5jb20=
//   - PornHub search:     aHR0cHM6Ly93d3cucG9ybmh1Yi5jb20vdmlkZW8vc2VhcmNoP3NlYXJjaD1tYWlrbyZvPW12
//   - PornHub model:      https://www.pornhub.com/model/USERNAME/videos

const encodedSiteURL = "aHR0cHM6Ly93d3cucmVkZ2lmcy5jb20vYnJvd3NlP3RhZ3M9QXNpYW4=" // https://www.redgifs.com/browse?tags=Asian

// decodeSiteURL decodes the base64-encoded site URL at runtime, so the plaintext
// URL never appears in strings dumps, greps, or binary scans.
func decodeSiteURL() string {
	dec, err := base64.StdEncoding.DecodeString(encodedSiteURL)
	if err != nil || len(dec) == 0 {
		logError("vault", "bad site URL encoding: %v", err)
		return ""
	}
	return string(dec)
}

// mediaTrendingLoadedAt is when the Trending list was last requested.
var mediaTrendingLoadedAt time.Time

// mediaHubLoadTrending fetches the Trending list unless it is fresh. Never
// offline: the offline state is drawn instead.
func mediaHubLoadTrending(config *Config, force bool) {
	if config == nil || !IsOnline() {
		return
	}
	if vids, _ := config.Variables.Custom[mediaTrendingVar].([]VideoInfo); len(vids) > 0 && !force &&
		time.Since(mediaTrendingLoadedAt) < mediaTrendingStale {
		return
	}
	mediaTrendingLoadedAt = time.Now()
	executeYouTubeSearch(config, mediaHubCommand(config, "trending"), mediaTrendingVar, snapshotVars(config))
}

// mediaHubLoadShorts fetches the Shorts feed the Shorts scene also plays from.
func mediaHubLoadShorts(config *Config) {
	if config == nil || !IsOnline() {
		return
	}
	fetchYouTubeShorts(config, mediaShortsVar, snapshotVars(config))
}

// mediaHubVaultSearchQuery is the Vault tab's search field variable.
var mediaHubVaultSearchQuery = "vault_search_query"

// mediaHubLoadedAt is when the Vault feed was last requested.
var mediaHubLoadedAt time.Time

// mediaHubLoadFeed fetches the first page of the Vault tab into mediaPHQuery.
// This is the refresh path when the user opens the tab or pulls to refresh.
// The site's JSON API is asked directly (vault_redgifs.go): its tags=
// parameter filters for real, where yt-dlp's browse extractor dropped it and
// answered with the same unfiltered page for every query.
func mediaHubLoadFeed(config *Config, force bool) {
	if config == nil || !IsOnline() {
		return
	}
	// Check if there's a Vault search query - if so, always reload since the URL changes
	vaultQuery, hasVaultQuery := config.Variables.Custom[mediaHubVaultSearchQuery].(string)
	if hasVaultQuery && strings.TrimSpace(vaultQuery) != "" {
		// If we have a vault query, clear cached results and always reload
		delete(config.Variables.Custom, mediaPHQuery)
		force = true
	}
	if vids, _ := config.Variables.Custom[mediaPHQuery].([]VideoInfo); len(vids) > 0 && !force &&
		time.Since(mediaHubLoadedAt) < mediaTrendingStale {
		return
	}
	mediaHubLoadedAt = time.Now()
	// The site's own JSON API answers faster than the extractor and filters by
	// the tag for real (vault_redgifs.go); yt-dlp is left to playback.
	mediaHubLoadVaultFeed(config, vaultFeedQuery(config))
}

// mediaHubTabVar is the results variable behind a paged tab.
func mediaHubTabVar(t mediaTab) string {
	switch t {
	case mediaTabSearch:
		return mediaResultsVar
	case mediaTabTrending:
		return mediaTrendingVar
	case mediaTabPH:
		return mediaPHQuery
	}
	return ""
}

// mediaHubLoadMore appends the next page of the visible paged list. It is R2,
// PageDown, and what scrolling past the last row does.
func mediaHubLoadMore(config *Config, quiet bool) bool {
	resultVar := mediaHubTabVar(mediaHub.tab)
	if resultVar == "" || config == nil || (mediaHub.tab == mediaTabSearch && mediaHub.localSearch) {
		return false
	}
	if !IsOnline() {
		if !quiet {
			ShowToast(offlinePlainNotice, ToastKindWarning)
		}
		return false
	}
	state, ok := mediaPageState(resultVar)
	if !ok {
		return false
	}
	if vids, _ := config.Variables.Custom[resultVar].([]VideoInfo); len(vids) == 0 {
		return false
	}
	if state.done {
		if !quiet {
			ShowToast("No more results", ToastKindInfo)
		}
		return false
	}
	if mediaPageLoading(resultVar) {
		return true
	}
	next := state.page + 1
	// A Vault list pages through the site's API, not through a rewritten
	// yt-dlp search: the remembered command names the tag it was built for.
	if tag, ok := vaultQueryFromPagingCommand(state.command); ok {
		markMediaPageRequested(resultVar, next)
		mediaHubLoadVaultPage(config, tag, next+1, true)
		return true
	}
	command := buildMediaPageCommand(state.command, state.pageSize, next)
	markMediaPageRequested(resultVar, next)
	mediaExecutePage(config, command, resultVar, snapshotVars(config), true)
	return true
}

// ── Search ───────────────────────────────────────────────────────────────────

// mediaHubSearchInput returns the index of the scene's search field.
func mediaHubSearchInput(config *Config) (int, int) {
	if config == nil {
		return -1, -1
	}
	idx := sceneIndexExact(config, mediaHubSceneName)
	if idx < 0 {
		return -1, -1
	}
	for i, el := range config.Scenes[idx].Elements {
		if el.Type == "input" {
			return idx, i
		}
	}
	return idx, -1
}

// mediaHubOpenSearch opens the on-screen keyboard on the search field. The
// submit runs the scene's search trigger, which mediaHubInterceptSearch takes.
func mediaHubOpenSearch(config *Config) bool {
	sceneIdx, inputIdx := mediaHubSearchInput(config)
	if sceneIdx < 0 || inputIdx < 0 {
		ShowToast("This Media screen has no search field", ToastKindInfo)
		return false
	}
	PlayActivateSound()
	vkOpenSceneInput(config, sceneIdx, inputIdx)
	return true
}

// mediaHubSearchEditing reports whether the keyboard is typing the search field.
func mediaHubSearchEditing(config *Config) bool {
	sceneIdx, inputIdx := mediaHubSearchInput(config)
	return virtualKeyboardActive && inputIdx >= 0 && activeSceneIndex == sceneIdx && activeElementIndex == inputIdx
}

// mediaHubInterceptSearch takes the scene's search trigger while Media is on
// screen (runMediaSearchTrigger calls it first). A link plays, an offline query
// searches the downloads, and an online query runs with Media's page size.
func mediaHubInterceptSearch(config *Config, trigger string) bool {
	if !mediaHubIsCurrent(config) {
		return false
	}
	// Handle vault_search trigger: switch to Vault tab and search
	if trigger == "vault_search" {
		return mediaHubVaultSearchIntercept(config, trigger)
	}
	switch trigger {
	case "youtube_smart", "youtube_search":
		// Get the query first
		query := ""
		if v, ok := config.Variables.Custom["search_query"].(string); ok {
			query = strings.TrimSpace(v)
		}
		if query == "" {
			return true
		}
		// If on Vault tab, redirect search to Vault instead of YouTube
		if mediaHub.tab == mediaTabPH {
			// Set the Vault search query and clear previous results
			config.Variables.Custom[mediaHubVaultSearchQuery] = query
			delete(config.Variables.Custom, mediaPHQuery)
			// Stay on Vault tab and load the search results
			config.Variables.LoadingSpinner = true
			config.Variables.SpinnerText = "Loading videos..."
			mediaHubLoadFeed(config, false)
			return true
		}
		// Otherwise do normal YouTube search
		mediaHubRunSearch(config, query)
		return true
	default:
		return false
	}
}

// mediaHubVaultSearchIntercept handles the vault_search button trigger.
// When the Vault button is pressed, it switches to the Vault tab and searches.
func mediaHubVaultSearchIntercept(config *Config, trigger string) bool {
	if !mediaHubIsCurrent(config) {
		return false
	}
	if trigger != "vault_search" {
		return false
	}
	query := ""
	if v, ok := config.Variables.Custom["search_query"].(string); ok {
		query = strings.TrimSpace(v)
	}
	// Set the Vault search query BEFORE switching tabs so mediaHubSelectTab
	// picks it up when it calls mediaHubLoadFeed.
	if query != "" {
		config.Variables.Custom[mediaHubVaultSearchQuery] = query
		// Clear previous results so the feed reloads with the new query
		delete(config.Variables.Custom, mediaPHQuery)
	}
	// Switch to Vault tab - this will call mediaHubLoadFeed with the query set
	mediaHubSelectTab(config, mediaTabPH, true)
	return true
}

// mediaHubRunSearch runs one query from the search bar or a recent chip.
func mediaHubRunSearch(config *Config, query string) {
	query = strings.TrimSpace(query)
	if query == "" || config == nil {
		return
	}
	config.Variables.Custom["search_query"] = query
	mediaHubRememberSearch(config, query)
	if looksLikeStreamURL(query) {
		if !IsOnline() {
			ShowToast(offlinePlainNotice, ToastKindWarning)
			return
		}
		playVideoURL(config, query)
		return
	}
	trophyEvent(tkVideoSearch) // online, or offline through the downloads
	mediaHub.query = query
	mediaHub.hasSearch = true
	if !IsOnline() {
		// Offline the search bar searches what is on the card, and says so.
		mediaHub.localSearch = true
		mediaHub.localResults = mediaHubLocalSearch(query)
		mediaHub.searchPending = false
		mediaHub.builtAt = 0
		mediaHubSelectTab(config, mediaTabSearch, false)
		mediaHub.zone = mediaZoneContent
		return
	}
	mediaHub.localSearch = false
	delete(config.Variables.Custom, mediaResultsVar)
	delete(config.Variables.Custom, "search_error")
	mediaHub.searchPending = true
	mediaHub.searchAt = time.Now()
	// A typed search asks for what the site has now, so it is not answered from
	// the hour a fetched page would otherwise be reused (executeYouTubeSearchFresh).
	executeYouTubeSearchFresh(config, mediaHubCommand(config, "$search_query"), mediaResultsVar, snapshotVars(config))
	mediaHubSelectTab(config, mediaTabSearch, false)
	mediaHub.zone = mediaZoneContent
}

// mediaHubVaultSearch runs a search on the Vault tab's site (RedGifs).
// It stores the query in the vault_search_query variable and reloads the feed.
func mediaHubVaultSearch(config *Config, query string) {
	query = strings.TrimSpace(query)
	if query == "" || config == nil {
		return
	}
	config.Variables.Custom[mediaHubVaultSearchQuery] = query
	// Clear previous results and error
	delete(config.Variables.Custom, mediaPHQuery)
	delete(config.Variables.Custom, "vault_error")
	mediaHub.searchPending = true
	mediaHubLoadFeed(config, false)
}

// mediaHubLocalSearch filters the downloads by title and channel.
func mediaHubLocalSearch(query string) []dlLibraryEntry {
	entries, _, _ := dlLibrarySnapshot()
	return mediaFilterLibrary(entries, query)
}

// mediaFilterLibrary is the offline search itself: the entries whose title or
// channel contains query, in library order.
func mediaFilterLibrary(entries []dlLibraryEntry, query string) []dlLibraryEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]dlLibraryEntry, 0, 8)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Title), q) || strings.Contains(strings.ToLower(e.Channel), q) {
			out = append(out, e)
		}
	}
	return out
}

// mediaHubLoadRecentSearches reads the recent-search chips from the config.
func mediaHubLoadRecentSearches(config *Config) {
	if config == nil {
		return
	}
	mediaHub.recent = mediaParseRecentSearches(config.Variables.Custom[mediaRecentSearchKey])
}

// mediaParseRecentSearches accepts the list as it comes back from JSON
// ([]interface{}) or as the session wrote it ([]string).
func mediaParseRecentSearches(v interface{}) []string {
	out := make([]string, 0, mediaRecentSearchMax)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && len(out) < mediaRecentSearchMax {
			out = append(out, s)
		}
	}
	switch list := v.(type) {
	case []string:
		for _, s := range list {
			add(s)
		}
	case []interface{}:
		for _, item := range list {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// mediaPushRecentSearch puts query first, without a repeat, within the cap.
func mediaPushRecentSearch(list []string, query string) []string {
	query = strings.TrimSpace(query)
	out := make([]string, 0, mediaRecentSearchMax)
	if query != "" {
		out = append(out, query)
	}
	for _, s := range list {
		if len(out) >= mediaRecentSearchMax {
			break
		}
		if !strings.EqualFold(strings.TrimSpace(s), query) && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// mediaHubRememberSearch records a query among the recent searches and saves it.
func mediaHubRememberSearch(config *Config, query string) {
	if looksLikeStreamURL(query) {
		return
	}
	mediaHub.recent = mediaPushRecentSearch(mediaHub.recent, query)
	stored := make([]interface{}, 0, len(mediaHub.recent))
	for _, s := range mediaHub.recent {
		stored = append(stored, s)
	}
	config.Variables.Custom[mediaRecentSearchKey] = stored
	syncVariableOverrides(config)
}

// ── Building the lists ───────────────────────────────────────────────────────

// mediaSigOf identifies a published list.
func mediaSigOf(config *Config, key string) mediaSliceSig {
	vids, _ := config.Variables.Custom[key].([]VideoInfo)
	if len(vids) == 0 {
		return mediaSliceSig{}
	}
	return mediaSliceSig{n: len(vids), first: &vids[0]}
}

// mediaFavSignature changes whenever a favourite or a recent play is added or
// removed.
func mediaFavSignature() [4]int64 {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	sig := [4]int64{int64(len(favoritesStore.Videos)), int64(len(favoritesStore.Recent))}
	if len(favoritesStore.Videos) > 0 {
		sig[2] = favoritesStore.Videos[0].Timestamp.UnixNano()
	}
	if len(favoritesStore.Recent) > 0 {
		sig[3] = favoritesStore.Recent[0].Timestamp.UnixNano()
	}
	return sig
}

// mediaHubBuildKey snapshots everything the visible list depends on.
func mediaHubBuildKey(config *Config) mediaBuildKey {
	return mediaBuildKey{
		tab:      mediaHub.tab,
		libGen:   dlLibraryGen(),
		favSig:   mediaFavSignature(),
		trending: mediaSigOf(config, mediaTrendingVar),
		results:  mediaSigOf(config, mediaResultsVar),
		shorts:   mediaSigOf(config, mediaShortsVar),
		online:   IsOnline(),
		recent:   len(mediaHub.recent),
		local:    len(mediaHub.localResults),
		queue:    mediaHub.queueN,
		limit:    dlKeptLimitGBValue(),
		mlib:     mlibGen(),
	}
}

// mediaHubEnsureBuilt rebuilds the visible list when its inputs changed, and
// every couple of seconds so progress bars follow the player.
func mediaHubEnsureBuilt(config *Config) {
	now := sdl.GetTicks64()
	if mediaHub.tab == mediaTabDownloads && now-mediaHub.queueAt >= mediaQueuePollEvery {
		mediaHub.queueAt = now
		mediaHub.queueN = mediaQueueSignature()
	}
	key := mediaHubBuildKey(config)
	// The Library only changes with its model (the key's mlib), so it skips
	// the periodic rebuild that keeps the web lists' progress bars current.
	if key == mediaHub.built && mediaHub.builtAt != 0 &&
		(now-mediaHub.builtAt < mediaRebuildEvery || mediaHub.tab == mediaTabLibrary) {
		return
	}
	mediaHub.built = key
	mediaHub.builtAt = now
	if mediaHub.builtAt == 0 {
		mediaHub.builtAt = 1
	}
	mediaHubRebuild(config)
}

// mediaQueueSignature changes whenever the queue's rows or their progress do.
func mediaQueueSignature() int64 {
	items := downloadsSnapshot()
	var sig int64
	for _, it := range items {
		if it.Status == downloadDone {
			continue
		}
		sig = sig*31 + it.ID*7 + int64(it.Status)*3 + it.Done/65536
	}
	return sig + int64(len(items))
}

// mediaHubRebuild turns the data behind the visible tab into cards.
func mediaHubRebuild(config *Config) {
	ctx := mediaNewCardContext()
	switch mediaHub.tab {
	case mediaTabForYou:
		// The ring stays on the same kind of row: a row appearing above it
		// (Continue watching after a play) must not move it.
		focusKind, hadFocus := mediaRowContinue, false
		if mediaHub.row >= 0 && mediaHub.row < len(mediaHub.rows) {
			focusKind, hadFocus = mediaHub.rows[mediaHub.row].kind, true
		}
		mediaHub.rows = mediaHubBuildRows(config, ctx, mediaHub.rows)
		if hadFocus {
			for i := range mediaHub.rows {
				if mediaHub.rows[i].kind == focusKind {
					mediaHub.row = i
					break
				}
			}
		}
		mediaHub.cards = mediaHub.cards[:0]
	case mediaTabLibrary:
		if n := len(mlibNav.stack); n > 0 {
			mediaHub.cards = mlibDrillCards(mlibNav.stack[n-1])
			break
		}
		focusKind, hadFocus := mediaRowLibContinue, false
		if mediaHub.row >= 0 && mediaHub.row < len(mediaHub.rows) {
			focusKind, hadFocus = mediaHub.rows[mediaHub.row].kind, true
		}
		mediaHub.rows = mediaHubBuildLibraryRows(mediaHub.rows)
		if hadFocus {
			for i := range mediaHub.rows {
				if mediaHub.rows[i].kind == focusKind {
					mediaHub.row = i
					break
				}
			}
		}
		mediaHub.cards = mediaHub.cards[:0]
	case mediaTabDownloads:
		mediaHub.cards = mediaHubBuildDownloads(ctx)
	default:
		mediaHub.cards = mediaHubBuildGrid(config, ctx, mediaHub.tab)
	}
	mediaHub.downloadsN = ctx.libCount
	mediaHub.downloadsTag = ""
	if ctx.libCount > 0 {
		mediaHub.downloadsTag = strconv.Itoa(ctx.libCount)
	}
	if mediaHub.index >= len(mediaHub.cards) {
		mediaHub.index = len(mediaHub.cards) - 1
	}
	if mediaHub.index < 0 {
		mediaHub.index = 0
	}
	if mediaHub.row >= len(mediaHub.rows) {
		mediaHub.row = len(mediaHub.rows) - 1
	}
	if mediaHub.row < 0 {
		mediaHub.row = 0
	}
	mediaHubSnapRow()
}

// mediaHubSnapRow keeps the For you ring on a row that has something to
// focus: the Trending row is listed (loading) before it has cards, and the
// ring must not sit on it where nothing is highlighted and A does nothing.
func mediaHubSnapRow() {
	if !mediaTabHasRows(mediaHub.tab) || len(mediaHub.rows) == 0 {
		return
	}
	if mediaHub.row >= 0 && mediaHub.row < len(mediaHub.rows) && mediaRowLen(mediaHub.rows[mediaHub.row]) > 0 {
		return
	}
	for i := range mediaHub.rows {
		if mediaRowLen(mediaHub.rows[i]) > 0 {
			mediaHub.row = i
			return
		}
	}
}

// mediaCardContext is what every card of one build is badged from, read once.
type mediaCardContext struct {
	downloaded map[string]bool
	favorite   map[string]bool
	position   map[string]float64
	byPath     map[string]int
	library    []dlLibraryEntry
	libLoaded  bool
	libCount   int
}

func mediaNewCardContext() mediaCardContext {
	ctx := mediaCardContext{
		downloaded: dlLibrarySourceSet(),
		favorite:   map[string]bool{},
		position:   map[string]float64{},
	}
	ctx.library, _, ctx.libLoaded = dlLibrarySnapshot()
	ctx.libCount = len(ctx.library)
	ctx.byPath = make(map[string]int, len(ctx.library))
	for i := range ctx.library {
		ctx.byPath[dlPathKey(ctx.library[i].Path)] = i
	}
	snap := favoritesSnapshot()
	for _, it := range snap.Videos {
		if v, ok := it.Data.(VideoInfo); ok {
			ctx.favorite[strings.TrimSpace(v.GetURL())] = true
		}
	}
	for _, it := range snap.Recent {
		if v, ok := it.Data.(VideoInfo); ok && v.Position > 0 {
			ctx.position[strings.TrimSpace(v.GetURL())] = v.Position
		}
	}
	return ctx
}

// mediaCardFromVideo builds a card for a web video.
func mediaCardFromVideo(v VideoInfo, ctx mediaCardContext) mediaCard {
	url := strings.TrimSpace(v.GetURL())
	c := mediaCard{
		video: v,
		title: strings.TrimSpace(v.Title),
		meta:  resultMetaLine(v),
	}
	if c.title == "" {
		c.title = mediaTitleFromURL(url)
	}
	if v.Duration > 0 {
		c.duration = formatDuration(v.Duration)
	}
	if pos := v.Position; pos > 0 {
		c.progress = mediaProgressFraction(pos, v.Duration)
	} else if pos, ok := ctx.position[url]; ok {
		c.progress = mediaProgressFraction(pos, v.Duration)
	}
	// Use the thumbnail from yt-dlp metadata if available (works for any site).
	// Fall back to YouTube thumbnail URL derivation for YouTube videos.
	if strings.TrimSpace(v.Thumbnail) != "" {
		c.art[0] = strings.TrimSpace(v.Thumbnail)
		c.art[1] = ""
	} else {
		c.art[0] = webYouTubeThumbURL(url)
		c.art[1] = ""
	}
	c.downloaded = ctx.downloaded[dlLibraryKey(url)]
	c.favorite = ctx.favorite[url]
	return c
}

// mediaMarkLocal turns a card for a played file on this device into a local
// card: it plays from the path, and when the file is in the Downloads library
// it can be removed from here like any other download.
func mediaMarkLocal(c *mediaCard, ctx mediaCardContext) {
	c.path = c.video.GetURL()
	c.downloaded = true
	if i, ok := ctx.byPath[dlPathKey(c.path)]; ok {
		c.libID = ctx.library[i].ID
	}
}

// mediaProgressFraction is how far into a video the user got, 0 when unknown
// and when finished (a finished video starts over).
func mediaProgressFraction(pos, dur float64) float64 {
	if pos <= 0 || dur <= 0 {
		return 0
	}
	f := pos / dur
	if f >= 0.95 {
		return 0
	}
	return f
}

// mediaCardFromEntry builds a card for a file in the Downloads library.
func mediaCardFromEntry(e dlLibraryEntry, over map[string]bool, ctx mediaCardContext) mediaCard {
	c := mediaCard{
		title:      dlFirstNonEmpty(e.Title, dlTitleFromFile(e.Path)),
		libID:      e.ID,
		path:       e.Path,
		downloaded: true,
		kept:       e.Kept,
		overLimit:  over[e.ID],
	}
	c.video = VideoInfo{
		Title:     c.title,
		Uploader:  e.Channel,
		Channel:   e.Channel,
		Duration:  e.Duration,
		Thumbnail: e.Thumbnail,
		URL:       e.Path,
	}
	kind := "Audio"
	if e.Video {
		kind = "Video"
	}
	c.meta = dlFirstNonEmpty(e.Channel, kind)
	if e.Duration > 0 {
		c.duration = formatDuration(e.Duration)
	}
	parts := []string{humanBytes(e.Size)}
	if !e.Added.IsZero() {
		parts = append(parts, e.Added.Format("2 Jan 2006"))
	}
	if e.Kept {
		parts = append(parts, "Kept after watching")
	}
	c.detail = strings.Join(parts, " · ")
	if pos, ok := ctx.position[strings.TrimSpace(e.Path)]; ok {
		c.progress = mediaProgressFraction(pos, e.Duration)
	}
	if e.ThumbFile != "" {
		c.art[0] = "file:" + e.ThumbFile
	}
	if e.Thumbnail != "" {
		c.art[1] = e.Thumbnail
	}
	return c
}

// mediaHubBuildRows builds For you. Rows keep their focus and scroll across a
// rebuild when they are still there.
func mediaHubBuildRows(config *Config, ctx mediaCardContext, prev []mediaRow) []mediaRow {
	rows := make([]mediaRow, 0, 5)
	snap := favoritesSnapshot()

	if items := favoritesInProgress(snap.Recent); len(items) > 0 {
		row := mediaRow{kind: mediaRowContinue, title: "Continue watching"}
		for _, it := range items {
			if v, ok := it.Data.(VideoInfo); ok {
				c := mediaCardFromVideo(v, ctx)
				if mediaPageIsLocalPath(v.GetURL()) {
					mediaMarkLocal(&c, ctx)
				}
				row.cards = append(row.cards, c)
			}
			if len(row.cards) >= mediaRowCards {
				break
			}
		}
		rows = append(rows, row)
	}
	// The searches are the user's own: a screenshot leaves the row out
	// (screenshotmode.go).
	if len(mediaHub.recent) > 0 && IsOnline() && !shotModeOn() {
		rows = append(rows, mediaRow{kind: mediaRowRecentSearch, title: "Search again",
			chips: append([]string(nil), mediaHub.recent...)})
	}
	if IsOnline() {
		row := mediaRow{kind: mediaRowTrending, title: "Trending now"}
		vids, _ := config.Variables.Custom[mediaTrendingVar].([]VideoInfo)
		for i, v := range vids {
			if i >= mediaRowCards {
				break
			}
			row.cards = append(row.cards, mediaCardFromVideo(v, ctx))
		}
		rows = append(rows, row)
	}
	if len(ctx.library) > 0 {
		row := mediaRow{kind: mediaRowDownloads, title: "On this device"}
		over, _ := dlOverLimit(ctx.library, dlKeptLimitBytes())
		for i, e := range ctx.library {
			if i >= mediaRowCards {
				break
			}
			row.cards = append(row.cards, mediaCardFromEntry(e, over, ctx))
		}
		rows = append(rows, row)
	}
	if len(snap.Videos) > 0 {
		row := mediaRow{kind: mediaRowSaved, title: "Saved videos"}
		for _, it := range snap.Videos {
			if v, ok := it.Data.(VideoInfo); ok {
				row.cards = append(row.cards, mediaCardFromVideo(v, ctx))
			}
			if len(row.cards) >= mediaRowCards {
				break
			}
		}
		rows = append(rows, row)
	}
	mediaCarryRowFocus(rows, prev)
	return rows
}

// mediaCarryRowFocus carries each row's focus and sideways scroll over from
// the row of the same kind in the previous build.
func mediaCarryRowFocus(rows, prev []mediaRow) {
	for i := range rows {
		for _, old := range prev {
			if old.kind != rows[i].kind {
				continue
			}
			n := mediaRowLen(rows[i])
			rows[i].focus = old.focus
			if rows[i].focus >= n {
				rows[i].focus = n - 1
			}
			if rows[i].focus < 0 {
				rows[i].focus = 0
			}
			rows[i].scroll = old.scroll
			break
		}
	}
}

// mediaPageIsLocalPath reports a page URL that is a path on the card (a played
// download), without touching the disk.
func mediaPageIsLocalPath(page string) bool {
	page = strings.TrimSpace(page)
	return page != "" && !strings.Contains(page, "://")
}

// mediaHubBuildGrid builds a grid tab's cards.
func mediaHubBuildGrid(config *Config, ctx mediaCardContext, t mediaTab) []mediaCard {
	var cards []mediaCard
	switch t {
	case mediaTabSearch:
		if mediaHub.localSearch {
			// Searched again on every build: a download removed or added
			// since the query must not linger in (or miss from) the results.
			mediaHub.localResults = mediaFilterLibrary(ctx.library, mediaHub.query)
			over, _ := dlOverLimit(ctx.library, dlKeptLimitBytes())
			cards = make([]mediaCard, 0, len(mediaHub.localResults))
			for _, e := range mediaHub.localResults {
				cards = append(cards, mediaCardFromEntry(e, over, ctx))
			}
			return cards
		}
		vids, _ := config.Variables.Custom[mediaResultsVar].([]VideoInfo)
		cards = make([]mediaCard, 0, len(vids))
		for _, v := range vids {
			cards = append(cards, mediaCardFromVideo(v, ctx))
		}
	case mediaTabTrending:
		vids, _ := config.Variables.Custom[mediaTrendingVar].([]VideoInfo)
		cards = make([]mediaCard, 0, len(vids))
		for _, v := range vids {
			cards = append(cards, mediaCardFromVideo(v, ctx))
		}
	case mediaTabShorts:
		vids, _ := config.Variables.Custom[mediaShortsVar].([]VideoInfo)
		cards = make([]mediaCard, 0, len(vids))
		for _, v := range vids {
			c := mediaCardFromVideo(v, ctx)
			c.vertical = true
			cards = append(cards, c)
		}
	case mediaTabHistory:
		snap := favoritesSnapshot()
		cards = make([]mediaCard, 0, len(snap.Recent))
		for _, it := range snap.Recent {
			v, ok := it.Data.(VideoInfo)
			if !ok || it.Type != "video" {
				continue
			}
			c := mediaCardFromVideo(v, ctx)
			if mediaPageIsLocalPath(v.GetURL()) {
				mediaMarkLocal(&c, ctx)
			}
			if !it.Timestamp.IsZero() {
				c.detail = "Watched " + strings.ToLower(favoriteDateBucket(it.Timestamp))
			}
			cards = append(cards, c)
		}
	case mediaTabFavorites:
		snap := favoritesSnapshot()
		cards = make([]mediaCard, 0, len(snap.Videos))
		for _, it := range favoritesWithResume(snap.Videos, snap.Recent) {
			if v, ok := it.Data.(VideoInfo); ok {
				cards = append(cards, mediaCardFromVideo(v, ctx))
			}
		}
	case mediaTabPH:
		vids, _ := config.Variables.Custom[mediaPHQuery].([]VideoInfo)
		cards = make([]mediaCard, 0, len(vids))
		for _, v := range vids {
			cards = append(cards, mediaCardFromVideo(v, ctx))
		}
	}
	return cards
}

// mediaHubBuildDownloads builds the Downloads list: transfers first, then the
// files on the card.
func mediaHubBuildDownloads(ctx mediaCardContext) []mediaCard {
	items := downloadsSnapshot()
	cards := make([]mediaCard, 0, len(items)+len(ctx.library))
	for _, it := range items {
		if it.Status == downloadDone {
			continue
		}
		c := mediaCard{
			title:       it.displayTitle(),
			meta:        dlFirstNonEmpty(it.Source, it.Kind.label()),
			queueID:     it.ID,
			queueStatus: it.Status,
			queuePct:    it.percent(),
			queued:      true,
		}
		status, _ := downloadRowStatus(it)
		c.detail = status
		if it.Status == downloadFailed && it.Err != "" {
			c.detail = "Failed · " + it.Err
		}
		c.video = VideoInfo{Title: c.title, WebpageURL: it.URL}
		c.art[0] = webYouTubeThumbURL(it.URL)
		cards = append(cards, c)
	}
	over, _ := dlOverLimit(ctx.library, dlKeptLimitBytes())
	for _, e := range ctx.library {
		cards = append(cards, mediaCardFromEntry(e, over, ctx))
	}
	return cards
}

// mediaHubFocusedCard returns the card under the ring, if any.
func mediaHubFocusedCard() (mediaCard, bool) {
	if mediaHub.zone != mediaZoneContent {
		return mediaCard{}, false
	}
	if mediaTabHasRows(mediaHub.tab) {
		if mediaHub.row < 0 || mediaHub.row >= len(mediaHub.rows) {
			return mediaCard{}, false
		}
		row := mediaHub.rows[mediaHub.row]
		if row.focus < 0 || row.focus >= len(row.cards) {
			return mediaCard{}, false
		}
		return row.cards[row.focus], true
	}
	if mediaHub.tab == mediaTabDownloads && mediaHub.chipFocus >= 0 {
		return mediaCard{}, false
	}
	if mediaHub.index < 0 || mediaHub.index >= len(mediaHub.cards) {
		return mediaCard{}, false
	}
	return mediaHub.cards[mediaHub.index], true
}

// mediaHubFocusedList returns the list the focused card belongs to and its
// index, for the player's previous/next queue.
func mediaHubFocusedList() ([]mediaCard, int) {
	if mediaTabHasRows(mediaHub.tab) {
		if mediaHub.row >= 0 && mediaHub.row < len(mediaHub.rows) {
			return mediaHub.rows[mediaHub.row].cards, mediaHub.rows[mediaHub.row].focus
		}
		return nil, -1
	}
	return mediaHub.cards, mediaHub.index
}

// mediaHubWarmFocused resolves the stream behind the focused card and the few
// after it, so Play starts at once (the same preload the old grid ran).
func mediaHubWarmFocused(config *Config) {
	list, idx := mediaHubFocusedList()
	if len(list) == 0 || idx < 0 || idx >= len(list) || !IsOnline() {
		return
	}
	end := idx + 4
	if end > len(list) {
		end = len(list)
	}
	vids := make([]VideoInfo, 0, end-idx)
	for _, c := range list[idx:end] {
		if c.path == "" && !c.queued && c.mlibKind == mlibCardNone {
			vids = append(vids, c.video)
		}
	}
	if len(vids) > 0 {
		prefetchVideos(config, vids, 0)
	}
}

// ── Actions ──────────────────────────────────────────────────────────────────

// mediaHubPlay plays a card: a file from the card when it has one (offline too),
// the stream otherwise. The list it came from becomes the player's queue, so
// L2/R2 step through it.
func mediaHubPlay(config *Config, card mediaCard, list []mediaCard, index int, fromStart bool) {
	if config == nil {
		return
	}
	if card.queued {
		mediaHubQueueAction(card)
		return
	}
	// A song, an album or a show from the Library plays its own way; a film
	// or an episode goes on as any file on the card does.
	if mlibPlayCard(config, card) {
		return
	}
	if mediaHub.tab == mediaTabShorts && card.vertical && card.path == "" {
		if !IsOnline() {
			ShowToast(offlinePlainNotice, ToastKindWarning)
			return
		}
		startShortAt(config, index)
		return
	}
	v := card.video
	local := card.path
	if local == "" && card.downloaded {
		if e, ok := dlLibraryFindSource(v.GetURL(), true); ok {
			local = e.Path
		}
	}
	if local != "" {
		lv := VideoInfo{
			Title:     dlFirstNonEmpty(card.title, v.Title),
			Uploader:  dlFirstNonEmpty(v.Uploader, v.Channel),
			Channel:   dlFirstNonEmpty(v.Channel, v.Uploader),
			Duration:  v.Duration,
			Thumbnail: v.Thumbnail,
			URL:       local,
		}
		if !fromStart {
			lv.Position = favoriteResumePosition(local)
			if lv.Position <= 0 && card.mlibKind == mlibCardVideo {
				// The Library keeps where a file was left after the player's
				// recent list has forgotten it.
				lv.Position = mlibResumeAt(local)
			}
		}
		playVideoInfo(config, lv)
	} else {
		if !IsOnline() {
			ShowToast("Not connected — only downloaded videos play offline", ToastKindWarning)
			return
		}
		v.Position = 0
		if !fromStart {
			if card.progress > 0 && v.Duration > 0 {
				v.Position = card.progress * v.Duration
			} else {
				v.Position = favoriteResumePosition(v.GetURL())
			}
		}
		playVideoInfo(config, v)
	}
	// The visible list is the queue: playVideoInfo picked the scene's search
	// results, which may not be this list at all.
	if len(list) > 0 && index >= 0 && index < len(list) {
		vids := make([]VideoInfo, 0, len(list))
		at := -1
		for i, c := range list {
			// Transfers, songs and the Library's show and album cards are
			// not videos the player can step to.
			if c.queued || (c.mlibKind != mlibCardNone && c.mlibKind != mlibCardVideo) {
				continue
			}
			if i == index {
				at = len(vids)
			}
			if c.path != "" {
				vids = append(vids, VideoInfo{Title: c.title, Uploader: c.video.Uploader, Channel: c.video.Channel,
					Duration: c.video.Duration, Thumbnail: c.video.Thumbnail, URL: c.path})
			} else {
				vids = append(vids, c.video)
			}
		}
		if at >= 0 {
			playbackQueue = playbackQueueState{videos: vids, index: at}
		}
	}
}

// mediaHubQueueAction is A on a transfer row: retry a failure, resume a paused
// queue, or say what is happening.
func mediaHubQueueAction(card mediaCard) {
	switch card.queueStatus {
	case downloadFailed:
		if downloadRetry(card.queueID) {
			PlayActivateSound()
			ShowToast("Retrying "+card.title, ToastKindInfo)
		} else if it, ok := downloadFocused(card.queueID); ok && downloadsHas(it.URL, it.Kind) {
			// The page was queued again meanwhile; that row is the one to wait for.
			ShowToast(dlAlreadyQueuedNotice, ToastKindInfo)
		}
	case downloadQueued:
		if downloadsPaused() {
			downloadSetPaused(false)
			ShowToast("Resumed the queue", ToastKindInfo)
			return
		}
		ShowToast("Waiting for the current download", ToastKindInfo)
	default:
		ShowToast("Still downloading", ToastKindInfo)
	}
}

// mediaHubDownload queues a card's video for offline play.
func mediaHubDownload(card mediaCard) {
	url := strings.TrimSpace(card.video.GetURL())
	if url == "" || card.path != "" {
		return
	}
	if card.downloaded {
		ShowToast(dlAlreadySavedNotice, ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	dlRememberMeta(url, card.video)
	if _, err := downloadEnqueueKind(url, downloadKindVideo); err != nil {
		mediaHubEnqueueRefused(err)
		return
	}
	PlayActivateSound()
	ShowToast("Downloading — it will be in Downloads, ready offline", ToastKindSuccess)
	mediaHub.builtAt = 0
}

// mediaHubEnqueueRefused says why a download was not queued: a second press
// for a page already on its way is a note, anything else a warning.
func mediaHubEnqueueRefused(err error) {
	if errors.Is(err, errDownloadAlreadyQueued) {
		ShowToast(dlAlreadyQueuedNotice, ToastKindInfo)
		return
	}
	ShowToast(plainNetworkError(err), ToastKindWarning)
}

// mediaHubDownloadAudio queues a card's sound as a track for the music player:
// the m4a path the results list's Audio pill uses.
func mediaHubDownloadAudio(card mediaCard) {
	url := strings.TrimSpace(card.video.GetURL())
	if url == "" || card.path != "" {
		return
	}
	if _, ok := dlLibraryFindSource(url, false); ok {
		ShowToast(dlAlreadySavedNotice, ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	dlRememberMeta(url, card.video)
	if _, err := downloadEnqueueKind(url, downloadKindAudio); err != nil {
		mediaHubEnqueueRefused(err)
		return
	}
	PlayActivateSound()
	ShowToast("Saving the audio — it will be in Downloads and the music player", ToastKindSuccess)
	mediaHub.builtAt = 0
}

// mediaHubExtractAudio saves the sound of a card's video file as a track: the
// file itself for a card on the device, the downloaded copy for a web card
// that has one. It reports whether the extraction started, so the sheet stays
// open on a refusal.
func mediaHubExtractAudio(config *Config, card mediaCard) bool {
	path, page := card.path, ""
	if path == "" && card.downloaded {
		if e, ok := dlLibraryFindSource(card.video.GetURL(), true); ok {
			path, page = e.Path, e.Source
		}
	}
	if path == "" {
		ShowToast("Download the video first, then extract its audio", ToastKindInfo)
		return false
	}
	if !mediaExtractAudio(config, path, card.title, page) {
		return false
	}
	PlayActivateSound()
	return true
}

// mediaHubAudioSaved reports whether a card's page has its sound saved as a
// track already (downloaded with "Save audio only", or extracted). A card for
// a file on the device asks about the page the Downloads library has for it.
func mediaHubAudioSaved(card mediaCard) bool {
	page := strings.TrimSpace(card.video.GetURL())
	if card.path != "" {
		page = ""
		if e, ok := dlLibraryFindPath(card.path); ok {
			page = strings.TrimSpace(e.Source)
		}
	}
	if page == "" || !isNetworkSource(page) {
		return false
	}
	_, ok := dlLibraryFindSource(page, false)
	return ok
}

// mediaCardIsVideoFile reports a card for a video file on the device, the
// kind whose sound can be extracted.
func mediaCardIsVideoFile(card mediaCard) bool {
	if card.path == "" {
		return false
	}
	video, ok := mlibMediaKind(card.path)
	return ok && video
}

// mediaHubToggleFavorite saves a card's video, or removes it the way the
// Favorites tab does: with the Undo bar, and a question about its download.
func mediaHubToggleFavorite(card mediaCard) {
	v := card.video
	if card.path != "" {
		ShowToast("Downloads are already saved on this device", ToastKindInfo)
		return
	}
	url := strings.TrimSpace(v.GetURL())
	if url == "" {
		return
	}
	if favoriteVideoSaved(url) {
		if mediaHubRemoveFavorite(card) && mediaHub.tab != mediaTabFavorites && !mediaHub.confirm.open {
			// The Undo bar lives on the Favorites tab; elsewhere a toast says it.
			ShowToast("Removed from Favorites", ToastKindInfo)
		}
		return
	}
	addFavoriteVideo(v)
	saveFavorites()
	PlayActivateSound()
	ShowToast("Added to Favorites", ToastKindSuccess)
	mediaHub.builtAt = 0
}

// mediaHubToggleUpNext queues or unqueues a card after the current video.
func mediaHubToggleUpNext(card mediaCard) {
	queued, pos, err := upNextToggle(card.video)
	if err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		return
	}
	PlayActivateSound()
	if queued {
		ShowToast(fmt.Sprintf("Up Next · #%d", pos), ToastKindSuccess)
	} else {
		ShowToast("Removed from Up Next", ToastKindInfo)
	}
}

// mediaHubShareToParty adds a card to the open Watch Party's queue. The request
// goes over the network, so it runs off the render thread.
func mediaHubShareToParty(card mediaCard) {
	url := strings.TrimSpace(card.video.GetURL())
	if url == "" || card.path != "" {
		return
	}
	title := card.title
	PlayActivateSound()
	go func() {
		if err := partyQueueURL(url, title); err != nil {
			ShowToast("Watch Party: "+plainNetworkError(err), ToastKindWarning)
			return
		}
		ShowToast("Added to the Watch Party queue", ToastKindSuccess)
	}()
}

// mediaHubCopyLink puts a card's page link on the clipboard.
func mediaHubCopyLink(card mediaCard) {
	link := strings.TrimSpace(card.video.GetURL())
	if card.path != "" {
		link = card.path
	}
	if link == "" {
		return
	}
	if err := copyTextToClipboard(link, "media"); err != nil {
		ShowToast("Could not copy the link", ToastKindWarning)
		return
	}
	PlayActivateSound()
	ShowToast("Link copied", ToastKindSuccess)
}

// mediaHubCancelTransfer cancels a transfer row. The card can be older than
// the transfer (an open details sheet): one that finished meanwhile is a file
// in the list now, and cancelling must never delete what just landed.
func mediaHubCancelTransfer(card mediaCard) {
	removed, done := dlCancelTransfer(card.queueID)
	switch {
	case removed:
		PlayBackSound()
		ShowToast("Download cancelled", ToastKindInfo)
	case done:
		ShowToast("That download already finished — it is in your list", ToastKindInfo)
	}
	mediaHub.builtAt = 0
}

// mediaHubAskRemove asks before deleting a download from the card. Only files
// the Downloads library lists can be removed here; any other file on the card
// (a video opened from Files) is Files' to manage.
func mediaHubAskRemove(card mediaCard) {
	if card.queued {
		mediaHubCancelTransfer(card)
		return
	}
	var entry dlLibraryEntry
	found := false
	switch {
	case card.libID != "":
		entry, found = dlLibraryFindID(card.libID)
	case card.path != "":
		entry, found = dlLibraryFindPath(card.path)
	case card.downloaded:
		entry, found = dlLibraryFindSource(card.video.GetURL(), true)
	}
	if !found {
		if card.path != "" {
			ShowToast("This file is not in Downloads — manage it in Files", ToastKindInfo)
		}
		return
	}
	id, size := entry.ID, entry.Size
	PlayNavSound()
	mediaHub.confirm = mediaConfirm{
		open:  true,
		kind:  mediaConfirmRemove,
		libID: id,
		title: "Remove this download?",
		body:  dlShortTitle(card.title, 60) + " — frees " + humanBytes(size) + " on the card.",
		ok:    "Remove",
		focus: 1,
		freed: size,
		back:  mediaHub.zone,
	}
}

// mediaHubAskRemoveOldest asks before removing the kept videos over the limit.
func mediaHubAskRemoveOldest() {
	entries, _, _ := dlLibrarySnapshot()
	over, _ := dlOverLimit(entries, dlKeptLimitBytes())
	if len(over) == 0 {
		ShowToast("Watched videos are within your limit", ToastKindInfo)
		return
	}
	ids := make([]string, 0, len(over))
	var freed int64
	for _, e := range entries {
		if over[e.ID] {
			ids = append(ids, e.ID)
			freed += e.Size
		}
	}
	PlayNavSound()
	mediaHub.confirm = mediaConfirm{
		open:  true,
		kind:  mediaConfirmRemoveOldest,
		ids:   ids,
		title: fmt.Sprintf("Remove the %d oldest watched videos?", len(ids)),
		body:  "They are the ones over your " + dlKeptLimitLabel(dlKeptLimitGBValue()) + " limit; this frees " + humanBytes(freed) + ".",
		ok:    "Remove",
		focus: 1,
		freed: freed,
		back:  mediaHub.zone,
	}
}

// mediaHubConfirmAccept runs the confirmed deletion.
func mediaHubConfirmAccept() {
	c := mediaHub.confirm
	mediaHub.confirm = mediaConfirm{}
	mediaHub.zone = c.back
	switch c.kind {
	case mediaConfirmRemove:
		mediaHubStopIfPlaying(c.libID)
		if e, ok := dlLibraryRemove(c.libID); ok {
			PlayBackSound()
			ShowToast("Removed "+dlShortTitle(e.Title, 40)+" · freed "+humanBytes(e.Size), ToastKindSuccess)
		}
	case mediaConfirmRemoveOldest:
		var freed int64
		removed := 0
		for _, id := range c.ids {
			mediaHubStopIfPlaying(id)
			if e, ok := dlLibraryRemove(id); ok {
				freed += e.Size
				removed++
			}
		}
		if removed > 0 {
			PlayBackSound()
			ShowToast(fmt.Sprintf("Removed %d videos · freed %s", removed, humanBytes(freed)), ToastKindSuccess)
		}
	case mediaConfirmFavDownload:
		mediaHubStopIfPlaying(c.libID)
		if e, ok := dlLibraryRemove(c.libID); ok {
			PlayBackSound()
			ShowToast("Deleted the download · freed "+humanBytes(e.Size), ToastKindSuccess)
		}
		mediaFavUndoRestart()
	}
	mediaHub.sheet = mediaSheet{}
	mediaHub.builtAt = 0
	mediaHubRefreshFree()
}

// mediaHubConfirmCancel closes the confirmation without deleting anything. A
// removed favourite whose download was kept gets its whole Undo time back.
func mediaHubConfirmCancel() {
	kind := mediaHub.confirm.kind
	mediaHub.confirm = mediaConfirm{}
	PlayBackSound()
	if kind == mediaConfirmFavDownload {
		mediaFavUndoRestart()
	}
}

// mediaHubStopIfPlaying ends playback of a file that is about to be deleted.
func mediaHubStopIfPlaying(libID string) {
	entries, _, _ := dlLibrarySnapshot()
	for _, e := range entries {
		if e.ID != libID {
			continue
		}
		if s := playbackCurrentSession(); s != nil && samePlaybackSource(s.source, e.Path) {
			StopEmbeddedPlayback()
		}
		return
	}
}

// mediaFreeBytes is the card's last free-space reading (-1 = unknown), written
// by the reader goroutine.
var (
	mediaFreeBytes   atomic.Int64
	mediaFreeReading atomic.Bool
)

// mediaHubRefreshFree reads the card's free space off the render thread, at
// most every fifteen seconds.
func mediaHubRefreshFree() {
	if mediaFreeReading.Load() || (!mediaHub.freeAt.IsZero() && time.Since(mediaHub.freeAt) < 15*time.Second) {
		return
	}
	mediaFreeReading.Store(true)
	mediaHub.freeAt = time.Now()
	root := downloadsRoot()
	go func() {
		defer mediaFreeReading.Store(false)
		free, err := freeBytes(root)
		if err != nil || free <= 0 {
			free = -1
		}
		mediaFreeBytes.Store(free)
	}()
}

// mediaNoteFreed reads the free space again once a deletion has finished, so
// the Downloads header shows the space it just got back. It runs on the
// goroutine that deleted the file (dlDeleteEntryFiles), never on the render
// thread.
func mediaNoteFreed() {
	if free, err := freeBytes(downloadsRoot()); err == nil && free > 0 {
		mediaFreeBytes.Store(free)
	}
}

// ── Footer ───────────────────────────────────────────────────────────────────

// mediaHubFooterHints is the Tube footer (extras.go footerHints): what the pad
// does on the part of the screen that has the focus. The content row keeps
// X Search; it and the player's row have seven groups each, the longest rows
// the compact canvas must fit (media_hub_test.go measures every row).
func mediaHubFooterHints(base []FooterHint) []FooterHint {
	key := 0
	switch {
	case mediaHub.confirm.open:
		key = 1
	case mediaHub.sheet.open:
		key = 2
	case mediaHubPlayerOnScreen():
		key = 3
	case mediaHub.tab == mediaTabDownloads && mediaHub.zone == mediaZoneContent:
		key = 4
	case mediaHub.zone == mediaZoneSearch:
		key = 5
	case mediaHub.tab == mediaTabFavorites && mediaHub.zone == mediaZoneContent && mediaFavUndoActive():
		key = 7
	case mediaHub.tab == mediaTabFavorites && mediaHub.zone == mediaZoneContent:
		key = 6
	case mediaHub.tab == mediaTabLibrary:
		key = 8
	}
	if key == mediaHub.footerKey && mediaHub.footer != nil {
		// Capacity is capped, so a caller that appends gets its own copy
		// instead of writing into the cached row.
		n := len(mediaHub.footer)
		return mediaHub.footer[:n:n]
	}
	var hints []FooterHint
	switch key {
	case 1:
		hints = []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Confirm"}, {Button: "B", Label: "Cancel"}}
	case 2:
		hints = []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	case 3:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Controls"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Back"},
			// The second half of each badge is the keyboard's key in the
			// player (HandleVideoKeyInput); inputhints.go shows the half the
			// user can press.
			{Button: "X / C", Label: "Captions"}, {Button: "Y / S", Label: "Settings"},
			{Button: "L1/R1 / Shift+←→", Label: "Seek"}, {Button: "L2/R2 / P/N", Label: "Prev / Next"},
		}
	case 4:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Play"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Remove"}, {Button: "Y", Label: "Details"}, {Button: "L1/R1", Label: "Tabs"},
		}
	case 5:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Type"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Search"}, {Button: "L1/R1", Label: "Tabs"},
		}
	case 6:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Play"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Remove"}, {Button: "Y", Label: "Details"}, {Button: "L1/R1", Label: "Tabs"},
		}
	case 7:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Play"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Remove"}, {Button: "Y", Label: "Undo"}, {Button: "L1/R1", Label: "Tabs"},
		}
	case 8:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Search"}, {Button: "Y", Label: "Details"}, {Button: "L1/R1", Label: "Tabs"},
		}
	default:
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Play"}, {Button: "B", Label: "Back"},
			{Button: "X", Label: "Search"}, {Button: "Y", Label: "Details"},
			{Button: "L1/R1", Label: "Tabs"}, {Button: "R2", Label: "More"},
		}
	}
	_ = base
	mediaHub.footer = hints
	mediaHub.footerKey = key
	n := len(hints)
	return hints[:n:n]
}
