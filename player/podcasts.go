package main

// podcasts.go — podcast subscriptions and the library panel.
//
// The Podcasts screen used to be a URL box plus one flat list of every episode
// from four curated feeds, and every row was an audio URL. A row handed to the
// audio player was treated as a file path, so the player tried to list the
// "folder" https://host/show/ and nothing played at all.
//
// This file keeps a small subscription library in the app data directory, gives
// each feed its show title and episode metadata, and plays an episode as a
// one-show playlist in the music player — which already knows how to stream a
// URL through ffmpeg and already remembers a long-form listening position and
// speed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	podcastSubsFileName = "podcasts.json"
	// podcastMaxEpisodes bounds memory per feed. A daily show keeps years of
	// back catalogue; a thousand rows in one column is neither browsable nor
	// cheap on a 1 GB handheld.
	podcastMaxEpisodes = 120
	// podcastFetchTimeout is one feed's network budget. Feeds are fetched in
	// parallel, so this is also roughly the whole refresh's worst case.
	podcastFetchTimeout = 12 * time.Second
)

// Compiled once: a refresh parses several feeds, each with dozens of items,
// and compiling these inside the loop made the parser the slowest part of a
// refresh on the handheld.
var (
	regexpRSSItem     = regexp.MustCompile(`(?is)<item\b[^>]*>(.*?)</item>`)
	regexpItemChannel = regexp.MustCompile(`(?is)<channel\b[^>]*>(.*?)</channel>`)
)

// podcastEpisode is one playable enclososure from a feed.
type podcastEpisode struct {
	Title     string
	URL       string
	Published time.Time
	Duration  float64 // seconds, 0 when the feed did not say
}

// podcastShow is one subscription: the feed's own title and its newest
// episodes, newest first.
type podcastShow struct {
	Title    string
	Author   string
	FeedURL  string
	Episodes []podcastEpisode
	Err      string
}

// podcastStore caches the parsed feeds and the subscription list. Shows are
// keyed by feed URL so the panel keeps its cursor while a refresh runs.
var podcastStore struct {
	mu     sync.Mutex
	loaded bool
	feeds  []string
	shows  map[string]podcastShow
	// status is the panel's one-line refresh read-out. It lives here, under
	// the store's mutex, because a background refresh writes it while the
	// render thread reads it; a plain field would be a data race.
	status string
}

// podcastSubsPath is a variable so tests can point the store at a temp file.
var podcastSubsPath = defaultPodcastSubsPath

func defaultPodcastSubsPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, podcastSubsFileName)
}

// podcastDefaultFeeds are the shows a fresh install starts with. They match the
// feeds the old screen hard-coded, so an update does not empty the library.
var podcastDefaultFeeds = curatedPodcasts

// ── Feed parsing ───────────────────────────────────────────────────────────

// podcastParseDate accepts the handful of date spellings real feeds use. RSS
// says RFC 1123, but Apple's guidance and most hosts wander; an unparseable
// value leaves the date blank instead of hiding the episode.
func podcastParseDate(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// podcastParseDuration reads itunes:duration, which is either plain seconds,
// "MM:SS" or "H:MM:SS". Negative or absurd values are rejected: the panel shows
// a length, and a wrong one is worse than none.
func podcastParseDuration(raw string) float64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0
	}
	total := 0.0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0
		}
		total = total*60 + v
	}
	// A whole-feed value above a day is a parse accident, not an episode.
	if total <= 0 || total > 24*60*60 {
		return 0
	}
	return total
}

// podcastEpisodeMeta is the "date · length" line the panel shows.
func podcastEpisodeMeta(ep podcastEpisode) string {
	parts := []string{}
	if !ep.Published.IsZero() {
		parts = append(parts, ep.Published.Format("2 Jan 2006"))
	}
	if ep.Duration > 0 {
		parts = append(parts, formatMediaDuration(int(ep.Duration)))
	}
	return strings.Join(parts, " · ")
}

// formatMediaDuration renders seconds as m:ss or h:mm:ss.
func formatMediaDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// podcastParseFeed turns one feed body into a show. It is pure so the parser can
// be tested against captured feeds without a network.
func podcastParseFeed(feedURL, data string) podcastShow {
	show := podcastShow{FeedURL: strings.TrimSpace(feedURL), Episodes: []podcastEpisode{}}
	body := data
	if m := regexpItemChannel.FindStringSubmatch(data); m != nil {
		body = m[1]
	}
	show.Title = rssTag(body, "title")
	if show.Title == "" {
		show.Title = rssTag(data, "title")
	}
	if author := rssTag(body, "itunes:author"); author != "" {
		show.Author = author
	} else if author := rssTag(body, "author"); author != "" {
		show.Author = author
	}
	if show.Title == "" {
		show.Err = "feed has no title"
		return show
	}

	seen := map[string]bool{}
	for _, m := range regexpRSSItem.FindAllStringSubmatch(data, -1) {
		block := m[1]
		enc := rssEnclosureURL(block)
		if enc == "" || seen[enc] {
			continue
		}
		title := rssTag(block, "title")
		if title == "" {
			title = podcastEpisodeTitleFromURL(enc)
		}
		ep := podcastEpisode{
			Title:     title,
			URL:       enc,
			Published: podcastParseDate(rssTag(block, "pubDate")),
			Duration:  podcastParseDuration(rssTag(block, "itunes:duration")),
		}
		seen[enc] = true
		show.Episodes = append(show.Episodes, ep)
	}
	if len(show.Episodes) == 0 {
		show.Err = "no episodes in this feed"
		return show
	}
	// Newest first. Undated episodes keep their feed order, after the dated
	// ones, because a feed with no dates is usually already newest-first.
	sort.SliceStable(show.Episodes, func(i, j int) bool {
		a, b := show.Episodes[i].Published, show.Episodes[j].Published
		if a.IsZero() != b.IsZero() {
			return b.IsZero()
		}
		return a.After(b)
	})
	if len(show.Episodes) > podcastMaxEpisodes {
		show.Episodes = show.Episodes[:podcastMaxEpisodes]
	}
	return show
}

// podcastEpisodeTitleFromURL is the last-resort label for an episode with no
// title: the file's base name, decoded and de-underscored.
func podcastEpisodeTitleFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return "Episode"
	}
	name := filepath.Base(u.Path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	if strings.TrimSpace(name) == "" {
		return "Episode"
	}
	return name
}

// ── Subscriptions ──────────────────────────────────────────────────────────

// loadPodcastSubscriptions reads the feed list once. A missing file seeds the
// curated shows, so the panel is never empty on a fresh install.
func loadPodcastSubscriptions() {
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	if podcastStore.loaded {
		return
	}
	podcastStore.loaded = true
	if podcastStore.shows == nil {
		podcastStore.shows = map[string]podcastShow{}
	}
	var feeds []string
	if _, err := readJSONWithBackup(podcastSubsPath(), &feeds); err == nil {
		podcastStore.feeds = sanitizePodcastFeeds(feeds)
		return
	} else if !os.IsNotExist(err) {
		log.Printf("[PODCAST] ignoring unreadable subscriptions: %v", err)
	}
	podcastStore.feeds = sanitizePodcastFeeds(podcastDefaultFeeds)
}

// sanitizePodcastFeeds keeps feed-looking URLs, drops duplicates and caps the
// list. Triple the default seeds leaves plenty of room without letting a broken
// import add a thousand feeds the app would fetch on every refresh.
func sanitizePodcastFeeds(feeds []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(feeds))
	for _, raw := range feeds {
		feed := strings.TrimSpace(raw)
		if !isHTTPMediaURL(feed) || seen[feed] {
			continue
		}
		seen[feed] = true
		out = append(out, feed)
		if len(out) >= 3*len(podcastDefaultFeeds) {
			break
		}
	}
	return out
}

func podcastFeeds() []string {
	loadPodcastSubscriptions()
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	return append([]string(nil), podcastStore.feeds...)
}

func savePodcastSubscriptions() error {
	podcastStore.mu.Lock()
	feeds := append([]string(nil), podcastStore.feeds...)
	podcastStore.mu.Unlock()
	data, err := json.MarshalIndent(feeds, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(podcastSubsPath(), data, 0o644)
}

// addPodcastFeed subscribes to a feed. It reports whether the list changed.
func addPodcastFeed(raw string) bool {
	feed := strings.TrimSpace(raw)
	if !isHTTPMediaURL(feed) {
		return false
	}
	loadPodcastSubscriptions()
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	for _, existing := range podcastStore.feeds {
		if strings.EqualFold(existing, feed) {
			return false
		}
	}
	podcastStore.feeds = append(podcastStore.feeds, feed)
	return true
}

// removePodcastFeed unsubscribes and drops the parsed show.
func removePodcastFeed(feed string) bool {
	loadPodcastSubscriptions()
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	found := false
	for i, existing := range podcastStore.feeds {
		if strings.EqualFold(existing, feed) {
			podcastStore.feeds = append(podcastStore.feeds[:i], podcastStore.feeds[i+1:]...)
			found = true
			break
		}
	}
	if found {
		delete(podcastStore.shows, feed)
	}
	return found
}

// ── Fetching ───────────────────────────────────────────────────────────────

// podcastShowsInFeedOrder returns the cached shows in subscription order.
// Feeds that have not been fetched yet appear with a loading note, so the panel
// shows the library immediately instead of a spinner over nothing.
func podcastShowsInFeedOrder() []podcastShow {
	feeds := podcastFeeds()
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	shows := make([]podcastShow, 0, len(feeds))
	for _, feed := range feeds {
		if show, ok := podcastStore.shows[feed]; ok {
			shows = append(shows, show)
			continue
		}
		shows = append(shows, podcastShow{
			Title:   podcastFeedLabel(feed),
			FeedURL: feed,
			Err:     "waiting for the feed",
		})
	}
	return shows
}

// podcastFeedLabel is the host name a feed is shown under before its title is
// known.
func podcastFeedLabel(feed string) string {
	u, err := url.Parse(feed)
	if err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Host, "www.")
	}
	return feed
}

// podcastShowForFeed returns one cached show.
func podcastShowForFeed(feed string) (podcastShow, bool) {
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	show, ok := podcastStore.shows[feed]
	return show, ok
}

// podcastRefresh fetches every subscribed feed in parallel and returns how many
// parsed. It takes the caller's context so leaving the panel stops the work.
func podcastRefresh(ctx context.Context) int {
	feeds := podcastFeeds()
	if len(feeds) == 0 || ctx.Err() != nil {
		return 0
	}
	results := make([]*podcastShow, len(feeds))
	var wg sync.WaitGroup
	for i, feed := range feeds {
		wg.Add(1)
		go func(i int, feed string) {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			body := httpGetTextFallbackContext(ctx, feed, podcastFetchTimeout)
			if strings.TrimSpace(body) == "" {
				return
			}
			show := podcastParseFeed(feed, body)
			results[i] = &show
		}(i, feed)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return 0
	}

	loaded := 0
	podcastStore.mu.Lock()
	if podcastStore.shows == nil {
		podcastStore.shows = map[string]podcastShow{}
	}
	for i, show := range results {
		feed := feeds[i]
		if show == nil {
			// Keep a show that was parsed earlier: a flaky refresh must not
			// replace the library with four error rows.
			if previous, ok := podcastStore.shows[feed]; ok {
				podcastStore.shows[feed] = previous
			} else {
				podcastStore.shows[feed] = podcastShow{
					Title:   podcastFeedLabel(feed),
					FeedURL: feed,
					Err:     "feed could not be reached",
				}
			}
			continue
		}
		if show.Err != "" && len(show.Episodes) == 0 {
			if previous, ok := podcastStore.shows[feed]; ok && len(previous.Episodes) > 0 {
				podcastStore.shows[feed] = previous
				continue
			}
		}
		podcastStore.shows[feed] = *show
		if len(show.Episodes) > 0 {
			loaded++
		}
	}
	podcastStore.mu.Unlock()
	return loaded
}

// podcastEpisodeLookup finds an episode by its enclosure URL across every
// subscribed show. It lets the old flat list (and a link opened from elsewhere)
// play with the show's own queue and the right title.
func podcastEpisodeLookup(raw string) (podcastShow, int, bool) {
	want := strings.TrimSpace(raw)
	if want == "" {
		return podcastShow{}, 0, false
	}
	for _, show := range podcastShowsInFeedOrder() {
		for i, ep := range show.Episodes {
			if strings.EqualFold(ep.URL, want) {
				return show, i, true
			}
		}
	}
	return podcastShow{}, 0, false
}

// podcastEpisodeTracks builds the queue for a show: one track per episode, in
// panel order, with the duration the feed advertised (ffprobe corrects it when
// playback starts).
func podcastEpisodeTracks(show podcastShow) []musicTrack {
	tracks := make([]musicTrack, 0, len(show.Episodes))
	for _, ep := range show.Episodes {
		name := strings.TrimSpace(ep.Title)
		if name == "" {
			name = podcastEpisodeTitleFromURL(ep.URL)
		}
		tracks = append(tracks, musicTrack{Path: ep.URL, Name: name, Duration: ep.Duration})
	}
	return tracks
}

// playPodcastEpisode switches to the audio player with the whole show queued at
// the chosen episode. The player already resumes a long-form position, keeps
// the speed the user picked and probes the real duration over the network.
func playPodcastEpisode(config *Config, show podcastShow, index int) bool {
	tracks := podcastEpisodeTracks(show)
	if index < 0 || index >= len(tracks) {
		return false
	}
	musicPlayPlaylist(tracks, index)
	openMusicPlayerScene(config)
	// The queue alone plays nothing: this said "Playing" over a silent player
	// until the user found the play button.
	musicPlayTrack(index)
	// The Continue timeline is the one place that answers "what was I in the
	// middle of?" across every media type, so a started episode belongs on it.
	addRecentPodcast(podcastEpisode{
		Title:    tracks[index].Name,
		URL:      tracks[index].Path,
		Duration: tracks[index].Duration,
	})
	title := show.Title
	if title == "" {
		title = "Podcast"
	}
	showToast("Playing "+title, ToastSuccess())
	return true
}

// ── Panel ──────────────────────────────────────────────────────────────────

// podcastPanelColumn says which half of the panel owns the D-pad.
const (
	podcastColumnShows = iota
	podcastColumnEpisodes
)

type podcastPanelState struct {
	open         bool
	column       int
	show         int
	episode      int
	firstShow    int
	firstEpisode int
}

func setPodcastStatus(text string) {
	podcastStore.mu.Lock()
	podcastStore.status = text
	podcastStore.mu.Unlock()
}

func podcastStatusText() string {
	podcastStore.mu.Lock()
	defer podcastStore.mu.Unlock()
	return podcastStore.status
}

var podPanel podcastPanelState

// podcastPanelShows is the show column, with one extra row at the end that asks
// for a feed. A D-pad user must be able to reach every action, and an action
// that only exists as a footer hint is unreachable on the Brick.
func podcastPanelShows() []podcastShow {
	return podcastShowsInFeedOrder()
}

func podcastPanelShowRows() int { return len(podcastPanelShows()) + 1 }

func podcastPanelEpisodeRows() int {
	shows := podcastPanelShows()
	if podPanel.show < 0 || podPanel.show >= len(shows) {
		return 0
	}
	return len(shows[podPanel.show].Episodes)
}

// podcastPanelClamp keeps both cursors inside their lists after a refresh, a
// removal, or a show that lost its episodes.
func podcastPanelClamp() {
	showRows := podcastPanelShowRows()
	if podPanel.show >= showRows {
		podPanel.show = showRows - 1
	}
	if podPanel.show < 0 {
		podPanel.show = 0
	}
	episodes := podcastPanelEpisodeRows()
	if podPanel.episode >= episodes {
		podPanel.episode = episodes - 1
	}
	if podPanel.episode < 0 {
		podPanel.episode = 0
	}
}

func openPodcastPanel() {
	qs.open = false
	toolsPanel.open = false
	processPanel.open = false
	loadPodcastSubscriptions()
	podPanel.open = true
	podPanel.column = podcastColumnShows
	setPodcastStatus("")
	podcastPanelClamp()
	PlayActivateSound()
	podcastPanelRefresh()
}

func closePodcastPanel() {
	podPanel.open = false
	PlayBackSound()
}

// podcastPanelRefresh reloads every feed as scene work, so closing the panel or
// leaving the app stops it. The status line reports the outcome instead of a
// silent stale list.
func podcastPanelRefresh() {
	setPodcastStatus("Loading feeds...")
	runSceneTask(func(ctx context.Context) {
		total := len(podcastFeeds())
		loaded := podcastRefresh(ctx)
		if ctx.Err() != nil {
			return
		}
		if loaded == 0 {
			setPodcastStatus("No feeds could be reached")
			return
		}
		setPodcastStatus(fmt.Sprintf("%d of %d shows updated", loaded, total))
	})
}

// podcastPanelActivate runs the highlighted row. A show opens its episodes; an
// episode plays. The "+ Add a feed" row is the last row of the show column.
func podcastPanelActivate(config *Config) {
	shows := podcastPanelShows()
	if podPanel.column == podcastColumnShows {
		if podPanel.show >= len(shows) {
			podcastPanelAskForFeed()
			return
		}
		if len(shows[podPanel.show].Episodes) == 0 {
			podcastPanelRefresh()
			return
		}
		podPanel.column = podcastColumnEpisodes
		podPanel.episode = 0
		PlayNavSound()
		return
	}
	if podPanel.show < 0 || podPanel.show >= len(shows) {
		return
	}
	show := shows[podPanel.show]
	if podPanel.episode < 0 || podPanel.episode >= len(show.Episodes) {
		return
	}
	ep := show.Episodes[podPanel.episode]
	if !podPanelPlayFrom(config, show, podPanel.episode) {
		showToast("Could not play "+ep.Title, ToastError())
		return
	}
	podPanel.open = false
}

// podPanelPlayFrom is a variable so tests can check the chosen episode without
// starting a decoder.
var podPanelPlayFrom = playPodcastEpisode

// podcastPanelMove moves inside the active column and reports whether anything
// moved, so the caller only plays a navigation sound on real movement.
func podcastPanelMove(delta int) bool {
	podcastPanelClamp()
	before := podPanel.show
	beforeEp := podPanel.episode
	if podPanel.column == podcastColumnShows {
		podPanel.show += delta
	} else {
		podPanel.episode += delta
	}
	podcastPanelClamp()
	if podPanel.column == podcastColumnShows {
		return podPanel.show != before
	}
	return podPanel.episode != beforeEp
}

// podcastPanelSwitchColumn moves between the show list and the episode list.
func podcastPanelSwitchColumn(dir int) bool {
	if dir < 0 {
		if podPanel.column == podcastColumnEpisodes {
			podPanel.column = podcastColumnShows
			return true
		}
		return false
	}
	if podPanel.column == podcastColumnShows && podcastPanelEpisodeRows() > 0 {
		podPanel.column = podcastColumnEpisodes
		podPanel.episode = 0
		return true
	}
	return false
}

// podcastPanelRemove unsubscribes from the highlighted show. The "+ Add a feed"
// row is not removable.
func podcastPanelRemove() {
	shows := podcastPanelShows()
	if podPanel.show < 0 || podPanel.show >= len(shows) {
		return
	}
	show := shows[podPanel.show]
	if !removePodcastFeed(show.FeedURL) {
		return
	}
	if err := savePodcastSubscriptions(); err != nil {
		showToast("Could not save subscriptions: "+firstLine(err.Error()), ToastError())
		return
	}
	podcastPanelClamp()
	showToast("Unsubscribed from "+show.Title, ToastInfo())
}

// podcastPanelAskForFeed opens the keyboard for a new feed URL. The panel stays
// open behind it, so the new show appears in place after a refresh.
func podcastPanelAskForFeed() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetPodcast
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	podcastFeedInput = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// podcastSubmitFeed subscribes to the typed URL and refreshes. It runs from the
// keyboard's submit, before the field is cleared.
func podcastSubmitFeed(raw string) {
	feed := strings.TrimSpace(raw)
	if !isHTTPMediaURL(feed) {
		showToast("That is not a feed URL", ToastError())
		return
	}
	if !addPodcastFeed(feed) {
		showToast("That feed is already in the library", ToastInfo())
		return
	}
	if err := savePodcastSubscriptions(); err != nil {
		showToast("Could not save the feed: "+firstLine(err.Error()), ToastError())
		return
	}
	showToast("Subscribed to "+podcastFeedLabel(feed), ToastSuccess())
	podcastPanelClamp()
	podcastPanelRefresh()
}

// podcastPanelStepShow moves the episode column to the previous or next show, so
// L1/R1 browse a whole library without walking back to the show list.
func podcastPanelStepShow(delta int) bool {
	before := podPanel.show
	podPanel.column = podcastColumnShows
	podPanel.show += delta
	podcastPanelClamp()
	podPanel.column = podcastColumnEpisodes
	podPanel.episode = 0
	return podPanel.show != before
}

// ── Panel input ────────────────────────────────────────────────────────────

func ctrlPodcastPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if podcastPanelMove(-1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if podcastPanelMove(1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if podcastPanelSwitchColumn(-1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if podcastPanelSwitchColumn(1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		podcastPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_X:
		podcastPanelAskForFeed()
	case sdl.CONTROLLER_BUTTON_Y:
		podcastPanelRefresh()
	case sdl.CONTROLLER_BUTTON_START:
		podcastPanelRemove()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if podcastPanelStepShow(-1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if podcastPanelStepShow(1) {
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if podPanel.column == podcastColumnEpisodes {
			podPanel.column = podcastColumnShows
			PlayBackSound()
			break
		}
		closePodcastPanel()
	}
	return true
}

// handlePodcastPanelKey consumes every key while the panel is up. The keyboard
// is checked before this in the key chain, so typing a feed URL is unaffected.
func handlePodcastPanelKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !podPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if podcastPanelMove(-1) {
			PlayNavSound()
		}
	case sdl.K_DOWN:
		if podcastPanelMove(1) {
			PlayNavSound()
		}
	case sdl.K_LEFT:
		if podcastPanelSwitchColumn(-1) {
			PlayNavSound()
		}
	case sdl.K_RIGHT:
		if podcastPanelSwitchColumn(1) {
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_SPACE:
		podcastPanelActivate(config)
	case sdl.K_a:
		podcastPanelAskForFeed()
	case sdl.K_r:
		podcastPanelRefresh()
	case sdl.K_DELETE, sdl.K_BACKSPACE:
		podcastPanelRemove()
	case sdl.K_PAGEUP:
		if podcastPanelStepShow(-1) {
			PlayNavSound()
		}
	case sdl.K_PAGEDOWN:
		if podcastPanelStepShow(1) {
			PlayNavSound()
		}
	case sdl.K_ESCAPE, sdl.K_b:
		if podPanel.column == podcastColumnEpisodes {
			podPanel.column = podcastColumnShows
			PlayBackSound()
			break
		}
		closePodcastPanel()
	}
	return true
}

// ── Panel rendering ────────────────────────────────────────────────────────

var (
	podcastShowRowHits []sdl.Rect
	podcastEpisodeHits []sdl.Rect
	podcastPanelRect   sdl.Rect
	podcastPanelFirst  = [2]int{}
)

// podcastNewBadge is shown for an episode published within two weeks: a quiet
// mark that the queue has something current in it.
func podcastIsNew(ep podcastEpisode) bool {
	return !ep.Published.IsZero() && time.Since(ep.Published) < 14*24*time.Hour
}

// podcastResumeFraction reports how much of an episode has been heard, from the
// same store the player writes. 0 means "not started".
func podcastResumeFraction(ep podcastEpisode) float64 {
	pos := audioResumePosition(ep.URL)
	if pos <= 0 {
		return 0
	}
	if ep.Duration > 0 {
		f := pos / ep.Duration
		if f > 1 {
			f = 1
		}
		return f
	}
	return 0
}

func renderPodcastPanel(renderer *sdl.Renderer, config *Config) {
	if !podPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	shows := podcastPanelShows()
	podcastPanelClamp()

	panelW := int32(1120)
	if panelW > screenWidth-32 {
		panelW = screenWidth - 32
	}
	panelH := int32(560)
	if panelH > screenHeight-32 {
		panelH = screenHeight - 32
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	podcastPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	sub := fmt.Sprintf("%d shows", len(shows))
	if status := podcastStatusText(); status != "" {
		sub = status
	}
	header := overlayPageHeader(sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}, "Podcasts", sub, "media")
	band := UIPanelHeaderBand(config, header, 8)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + band
	footerH := int32(font.Height()) + 20
	bodyH := panelH - band - footerH
	pad := OverlayPadX

	showW := panelW * 34 / 100
	if showW < 280 {
		showW = 280
	}
	if showW > 420 {
		showW = 420
	}
	showList := sdl.Rect{X: panelX + pad, Y: bodyY, W: showW, H: bodyH - 8}
	epList := sdl.Rect{X: showList.X + showList.W + 18, Y: bodyY, W: panelW - showW - pad*3 - 18, H: bodyH - 8}

	// A divider makes the two columns read as one browser rather than two
	// unrelated lists.
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 90)
	renderer.FillRect(&sdl.Rect{X: showList.X + showList.W + 8, Y: bodyY + 4, W: 1, H: bodyH - 16})

	// Show column: row height from the font, so a large-text theme does not
	// clip the second line.
	showRowH := int32(font.Height())*2 + 14
	showVisible := int(showList.H / showRowH)
	if showVisible < 1 {
		showVisible = 1
	}
	if podPanel.show < podPanel.firstShow {
		podPanel.firstShow = podPanel.show
	}
	if podPanel.show >= podPanel.firstShow+showVisible {
		podPanel.firstShow = podPanel.show - showVisible + 1
	}
	firstShow := podPanel.firstShow
	podcastPanelFirst[podcastColumnShows] = firstShow
	podcastShowRowHits = podcastShowRowHits[:0]
	focus := installHighlight()
	for i := 0; i < showVisible; i++ {
		idx := firstShow + i
		if idx >= len(shows)+1 {
			break
		}
		rect := sdl.Rect{X: showList.X, Y: showList.Y + int32(i)*showRowH, W: showList.W, H: showRowH - 4}
		podcastShowRowHits = append(podcastShowRowHits, rect)
		active := idx == podPanel.show
		if active {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(focus, 42))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, WithAlpha(focus, 220))
		}
		if idx >= len(shows) {
			// The add row.
			label := "+ Add a feed"
			renderText(renderer, config, font, label, ColorTextPrimary(), rect.X+14, rect.Y+showRowH/2-int32(font.Height())/2-2)
			continue
		}
		show := shows[idx]
		titleCol := ColorTextSecondary()
		if active {
			titleCol = ColorTextPrimary()
		}
		title := ellipsize(font, show.Title, rect.W-28)
		renderText(renderer, config, font, title, titleCol, rect.X+14, rect.Y+8)
		meta := fmt.Sprintf("%d episodes", len(show.Episodes))
		if show.Err != "" && len(show.Episodes) == 0 {
			meta = show.Err
		} else if show.Author != "" {
			meta = ellipsize(font, show.Author, rect.W-28) + " · " + meta
		}
		renderText(renderer, config, font, ellipsize(font, meta, rect.W-28), ColorTextTertiary(), rect.X+14, rect.Y+8+int32(font.Height())+2)
	}
	if len(shows) == 0 {
		renderText(renderer, config, font, "No subscriptions yet.", ColorTextTertiary(), showList.X+4, showList.Y+4)
	}

	// Episode column.
	podcastEpisodeHits = podcastEpisodeHits[:0]
	if podPanel.show < 0 || podPanel.show >= len(shows) {
		UIEmptyState(renderer, config, epList, "media", "No show selected", "Pick a show on the left, or add a feed.")
	} else {
		show := shows[podPanel.show]
		if len(show.Episodes) == 0 {
			msg := "Loading episodes..."
			if show.Err != "" {
				msg = show.Err
			}
			UIEmptyState(renderer, config, epList, "media", "Nothing to play yet", msg)
		} else {
			epRowH := int32(font.Height())*2 + 18
			epVisible := int(epList.H / epRowH)
			if epVisible < 1 {
				epVisible = 1
			}
			if podPanel.episode < podPanel.firstEpisode {
				podPanel.firstEpisode = podPanel.episode
			}
			if podPanel.episode >= podPanel.firstEpisode+epVisible {
				podPanel.firstEpisode = podPanel.episode - epVisible + 1
			}
			firstEp := podPanel.firstEpisode
			podcastPanelFirst[podcastColumnEpisodes] = firstEp
			for i := 0; i < epVisible; i++ {
				idx := firstEp + i
				if idx >= len(show.Episodes) {
					break
				}
				ep := show.Episodes[idx]
				rect := sdl.Rect{X: epList.X, Y: epList.Y + int32(i)*epRowH, W: epList.W, H: epRowH - 6}
				podcastEpisodeHits = append(podcastEpisodeHits, rect)
				active := podPanel.column == podcastColumnEpisodes && idx == podPanel.episode
				if active {
					fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(focus, 42))
					strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, WithAlpha(focus, 220))
				}
				titleCol := ColorTextSecondary()
				if active {
					titleCol = ColorTextPrimary()
				}
				title := ellipsize(font, ep.Title, rect.W-120)
				renderText(renderer, config, font, title, titleCol, rect.X+14, rect.Y+8)
				if podcastIsNew(ep) {
					badge := "NEW"
					bw, _, _ := font.SizeUTF8(badge)
					renderText(renderer, config, font, badge, WithAlpha(focus, 230), rect.X+rect.W-int32(bw)-14, rect.Y+8)
				} else {
					meta := podcastEpisodeMeta(ep)
					if meta != "" {
						mw, _, _ := font.SizeUTF8(meta)
						renderText(renderer, config, font, meta, ColorTextTertiary(), rect.X+rect.W-int32(mw)-14, rect.Y+8)
					}
				}
				detail := "A plays this episode"
				if fraction := podcastResumeFraction(ep); fraction > 0 {
					detail = fmt.Sprintf("Resume %d%%", int(math.Round(fraction*100)))
				}
				renderText(renderer, config, font, detail, ColorTextTertiary(), rect.X+14, rect.Y+8+int32(font.Height())+2)
				// A hairline along the row's foot for the listened part.
				if fraction := podcastResumeFraction(ep); fraction > 0 {
					barW := int32(float64(rect.W-28) * fraction)
					if barW < 2 {
						barW = 2
					}
					renderer.SetDrawColor(focus.R, focus.G, focus.B, 150)
					renderer.FillRect(&sdl.Rect{X: rect.X + 14, Y: rect.Y + rect.H - 3, W: barW, H: 2})
				}
			}
		}
	}

	footer := hintLine(
		FooterHint{"A / Enter", "Open"},
		FooterHint{"</> / Left Right", "Columns"},
		FooterHint{"X", "Add Feed"},
		FooterHint{"Y / R", "Refresh"},
		FooterHint{"Start / Del", "Unsubscribe"},
		FooterHint{"B / Esc", "Close"},
	)
	if fw, _, _ := font.SizeUTF8(footer); int32(fw) > panelW-48 {
		footer = hintLine(
			FooterHint{"A", "Open"},
			FooterHint{"X", "Add Feed"},
			FooterHint{"Y", "Refresh"},
			FooterHint{"Del", "Remove"},
			FooterHint{"B / Esc", "Close"},
		)
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(), panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+6)
}

// handlePodcastPanelMouse activates a clicked row, or closes on an outside
// click. Clicking a show selects it and opens its episodes; clicking an episode
// plays it, which is what the same click does on every other list in the app.
func handlePodcastPanelMouse(mx, my int32, config *Config) bool {
	if !podPanel.open {
		return false
	}
	for i, rect := range podcastShowRowHits {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			podPanel.show = podcastPanelFirst[podcastColumnShows] + i
			podPanel.column = podcastColumnShows
			podcastPanelActivate(config)
			return true
		}
	}
	for i, rect := range podcastEpisodeHits {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			podPanel.episode = podcastPanelFirst[podcastColumnEpisodes] + i
			podPanel.column = podcastColumnEpisodes
			podcastPanelActivate(config)
			return true
		}
	}
	if podcastPanelRect.W > 0 && !pointInRect(mx, my, podcastPanelRect) {
		closePodcastPanel()
	}
	return true
}

// ensurePodcastEntries gives the Podcasts scene a library button. The screen's
// own URL box still works (Load plays the feed's newest episode list), but the
// button is the way to the shows, subscriptions and resume positions.
func ensurePodcastEntries(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		scene := &config.Scenes[i]
		if scene.Name != "Podcasts" || sceneHasTriggerElement(*scene, "podcast_panel") {
			continue
		}
		x := int32(24)
		for _, e := range scene.Elements {
			if e.Type == "button" && e.Y == 72 {
				if right := e.X + getElementWidth(e, 140) + 8; right > x {
					x = right
				}
			}
		}
		canvasW := int32(config.Width)
		if canvasW <= 0 {
			canvasW = int32(config.Variables.ScreenWidth)
		}
		if canvasW <= 0 {
			canvasW = 1280
		}
		if x+150 > canvasW-16 {
			continue
		}
		scene.Elements = append(scene.Elements, Element{
			Type: "button", Text: "Shows", Color: "$labelColor", BgColor: "$buttonColor",
			X: x, Y: 72, Width: "150", Height: "48", Trigger: "podcast_panel",
		})
	}
}
