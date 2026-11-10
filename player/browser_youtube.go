package main

// browser_youtube.go — a YouTube link followed in the browser plays in Media.
//
// The reader cannot show YouTube: a watch page is a JavaScript application,
// and what came back was a consent wall or a page of script residue. The app
// already streams YouTube properly (yt-dlp into the embedded player on the
// Media screen), so a link to one YouTube video is handed to the player
// instead of being loaded — a tap, A on a focused link, the link picker, a
// play card, a typed or pasted address, a bookmark, a redirect that ends on a
// watch page, and a link another screen opens in the browser (openBrowserAtURL)
// all play the video. Back from the player returns to the page the link was
// on, scrolled where it was, with the video in the corner window
// (videomini.go).
//
// Search results are click-tracking links (DuckDuckGo's /l/?uddg=, Google's
// /url?q=), so the destination is read out of them first. Only links to a
// single video count: channels, playlists and YouTube's own search still open
// as pages. A watch link that also names a playlist plays its video alone —
// yt-dlp would otherwise fetch the whole list into the one temp file.

import (
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// brwYouTubeLink is a link resolved for the player.
type brwYouTubeLink struct {
	ID    string  // the 11-character video id
	URL   string  // the canonical watch page handed to the player
	Start float64 // where the link asks playback to start, in seconds
}

// brwUnwrapLimit bounds nested click-tracking hops (a redirect inside a
// redirect); real links never need more than one.
const brwUnwrapLimit = 3

// brwHostIs reports whether host is domain or one of its subdomains.
func brwHostIs(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// brwIsGoogleHost matches Google's search hosts (google.com, www.google.co.uk).
func brwIsGoogleHost(host string) bool {
	return strings.HasPrefix(strings.TrimPrefix(host, "www."), "google.")
}

// brwWebLink reports whether s is an absolute http(s) URL.
func brwWebLink(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// brwUnwrapTracking returns where a search engine's click-tracking link leads
// — DuckDuckGo's /l/?uddg= (relative, protocol-relative or absolute), Google's
// /url?q= or url= — and any other link unchanged. YouTube's consent wall is
// unwrapped the same way: in the EU a redirect to a watch page ends on
// consent.youtube.com with the watch page in its continue= parameter.
func brwUnwrapTracking(raw string) string {
	cur := strings.TrimSpace(raw)
	for i := 0; i < brwUnwrapLimit; i++ {
		u, err := url.Parse(cur)
		if err != nil {
			return cur
		}
		host := strings.ToLower(u.Hostname())
		path := strings.TrimSuffix(u.Path, "/")
		next := ""
		switch {
		case (host == "" || brwHostIs(host, "duckduckgo.com")) && path == "/l":
			next = u.Query().Get("uddg")
		case brwIsGoogleHost(host) && path == "/url":
			next = u.Query().Get("q")
			if next == "" {
				next = u.Query().Get("url")
			}
		case brwHostIs(host, "consent.youtube.com") || brwHostIs(host, "consent.google.com"):
			next = u.Query().Get("continue")
		case brwHostIs(host, "bing.com") && path == "/ck/a":
			// Bing's and Yahoo's results, for the other search engines
			// (browser_engines.go decodes both).
			next = brwBingTarget(u.Query().Get("u"))
		case brwHostIs(host, "r.search.yahoo.com"):
			next = brwYahooTarget(u.EscapedPath())
		}
		next = strings.TrimSpace(next)
		if next == "" || !brwWebLink(next) {
			return cur
		}
		cur = next
	}
	return cur
}

// brwYouTubeID reports whether s is a YouTube video id: exactly 11 characters
// of the URL-safe base64 alphabet. A channel id or a custom path is longer or
// shorter, so it never passes for a video.
func brwYouTubeID(s string) bool {
	if len(s) != 11 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// brwYouTubeStart reads a start offset the way YouTube writes one: "90",
// "90s", "1m30s", "1h2m3s" (and "1m30", seconds implied). Anything else, and
// anything past a month, is no offset at all.
func brwYouTubeStart(v string) float64 {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 0
	}
	const limit = 30 * 24 * 3600
	if n, err := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64); err == nil {
		if n > 0 && n < limit {
			return n
		}
		return 0
	}
	total, num := 0, ""
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'h' || r == 'm' || r == 's':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0
			}
			switch r {
			case 'h':
				total += n * 3600
			case 'm':
				total += n * 60
			default:
				total += n
			}
			num = ""
		default:
			return 0
		}
	}
	if num != "" {
		n, err := strconv.Atoi(num)
		if err != nil {
			return 0
		}
		total += n
	}
	if total <= 0 || total >= limit {
		return 0
	}
	return float64(total)
}

// brwYouTubeVideo reports whether raw — after any click-tracking wrapper —
// links to one YouTube video, and how the player should open it.
func brwYouTubeVideo(raw string) (brwYouTubeLink, bool) {
	target := brwUnwrapTracking(raw)
	if !brwWebLink(target) {
		return brwYouTubeLink{}, false
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return brwYouTubeLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	id := ""
	switch {
	case brwHostIs(host, "youtu.be"):
		id = segs[0]
	case brwHostIs(host, "youtube.com") || brwHostIs(host, "youtube-nocookie.com"):
		switch strings.ToLower(segs[0]) {
		case "watch":
			id = u.Query().Get("v")
		case "shorts", "embed", "live", "v", "e":
			if len(segs) >= 2 {
				id = segs[1]
			}
		}
	}
	if !brwYouTubeID(id) {
		return brwYouTubeLink{}, false
	}
	q := u.Query()
	start := brwYouTubeStart(q.Get("t"))
	for _, key := range []string{"start", "time_continue"} {
		if start == 0 {
			start = brwYouTubeStart(q.Get(key))
		}
	}
	if start == 0 && strings.HasPrefix(u.Fragment, "t=") {
		start = brwYouTubeStart(strings.TrimPrefix(u.Fragment, "t="))
	}
	return brwYouTubeLink{ID: id, URL: "https://www.youtube.com/watch?v=" + id, Start: start}, true
}

// brwVideoTitle turns a link's text into the video's title for the player,
// the Continue card and Recents. Search results end in " - YouTube", play
// cards start with their ▶ mark, and a URL or a bare path (what the link
// picker labels a textless anchor with) is no title.
func brwVideoTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	t = strings.TrimSpace(strings.TrimPrefix(t, "▶"))
	for _, suffix := range []string{" - YouTube", " – YouTube", " — YouTube", " | YouTube"} {
		if strings.HasSuffix(t, suffix) {
			t = strings.TrimSpace(strings.TrimSuffix(t, suffix))
			break
		}
	}
	lower := strings.ToLower(t)
	if t == "" || brwWebLink(t) || strings.HasPrefix(t, "/") ||
		strings.Contains(lower, "youtube.com/") || strings.Contains(lower, "youtu.be/") ||
		strings.EqualFold(t, "Play video") || strings.EqualFold(t, "YouTube") {
		return ""
	}
	if r := []rune(t); len(r) > 120 {
		t = strings.TrimSpace(string(r[:119])) + "…"
	}
	return t
}

// brwOpenYouTubeLink plays target in Media when it is a YouTube video link and
// reports whether it did; title is the link's text, "" when there is none.
// The callers are every way the browser follows a link, so it is the one
// place that decides.
func brwOpenYouTubeLink(config *Config, target, title string) bool {
	link, ok := brwYouTubeVideo(target)
	if !ok || config == nil {
		return false
	}
	brwPlayFromBrowser(config, link, title)
	return true
}

// brwPlayFromBrowser plays a video the reader's page led to, remembering where
// the page was so coming back from the player puts it there (brwReturn).
func brwPlayFromBrowser(config *Config, link brwYouTubeLink, title string) bool {
	brwRememberReturn(config)
	if !brwPlayYouTube(config, link, title) {
		brwReturn.armed = false
		return false
	}
	return true
}

// brwPlayYouTube hands one video to the player and reports whether it did.
// Offline it says so instead, and the screen stays where it is.
func brwPlayYouTube(config *Config, link brwYouTubeLink, title string) bool {
	if !IsOnline() {
		ShowToast("You're offline — videos need an internet connection", ToastKindWarning)
		return false
	}
	// The same guard playSmartURL keeps: a cookies path or key that could
	// alter the yt-dlp command line never reaches it.
	if err := validateYtDlpArgs(ytDlpExtraArgsSlice(config)); err != nil {
		logWarn("browser", "security: YouTube link not played, unsafe extra yt-dlp args: %v", err)
		ShowToast("This video cannot play: check the YouTube settings", ToastKindWarning)
		return false
	}
	v := VideoInfo{
		ID:         link.ID,
		Title:      brwVideoTitle(title),
		URL:        link.URL,
		WebpageURL: link.URL,
		Thumbnail:  webYouTubeThumbBase + link.ID + "/mqdefault.jpg",
		Position:   link.Start,
	}
	logInfo("browser", "YouTube link opens in Media: %s (start %.0fs)", link.ID, link.Start)
	playVideoInfo(config, v)
	if v.Title == "" {
		// A link with no usable text (a thumbnail, a bare address): look the
		// title up so Continue and Recents name the video instead of showing
		// its address (ytoembed.go).
		ytFillMissingTitle(v)
	}
	if v.Title != "" {
		// The video came from one link, not a list: naming it as its own
		// one-entry list is what puts its title on the player and the status
		// chip (videoTitleForPage), instead of the host name.
		playbackQueue = playbackQueueState{videos: []VideoInfo{v}, index: 0}
	}
	return true
}

// ── Back to the page ─────────────────────────────────────────────────────

// brwReturn remembers where the reader was when a link left for the player:
// coming back through the scene change resets the reader to the top
// (completeSceneTransition), and the page should come back where it was.
// Render/event thread only.
var brwReturn struct {
	armed   bool
	address string
	row     int32
	leftAt  uint64
}

// brwReturnSettleMS is how long the trip to the player takes at the least
// (the scene fade out and in): a restore never fires before it, so it cannot
// act on the frames the browser is still fading out.
const brwReturnSettleMS = uiSceneOutMS + uiSceneInMS + 60

// brwRememberReturn arms the restore for the page on screen, at the row it is
// scrolled to.
func brwRememberReturn(config *Config) {
	address := strings.TrimSpace(textBrowserAddress(config))
	brwReturn.armed = address != "" && textBrowserScrollY > 0
	brwReturn.address = address
	brwReturn.row = textBrowserScrollY
	brwReturn.leftAt = sdl.GetTicks64()
}

// brwRestoreReturn runs on the reader's render pass: back on the page the
// link left from, freshly reset to the top by the return, it scrolls back to
// the remembered row once — as the page fades in, not after it, so it never
// shows at the top and then jumps. Any other page disarms it.
func brwRestoreReturn(config *Config) {
	if !brwReturn.armed || transitionPhase == "fade-out" {
		return
	}
	if strings.TrimSpace(textBrowserAddress(config)) != brwReturn.address {
		brwReturn.armed = false
		return
	}
	if sdl.GetTicks64()-brwReturn.leftAt < brwReturnSettleMS {
		return
	}
	brwReturn.armed = false
	if textBrowserScrollY == 0 {
		textBrowserScrollY = brwReturn.row
		textBrowserScrollVelocity = 0
	}
}

// ── Addresses the reader was asked to load ───────────────────────────────

// brwPageOnScreen is the address of the page the reader is showing: its
// document's own URL, else the current history entry. The address variable is
// not it — the keyboard writes into it while an address is typed, and
// openBrowserAtURL sets it before the page arrives.
func brwPageOnScreen(variable string) string {
	if doc := browserDocumentFor(variable); doc != nil {
		if u := strings.TrimSpace(doc.URL); u != "" {
			return u
		}
	}
	return brwHistoryCurrent()
}

// brwHistoryCurrent is the browser history's current entry, or "".
func brwHistoryCurrent() string {
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	if i := browserState.index; i >= 0 && i < len(browserState.history) {
		return browserState.history[i]
	}
	return ""
}

// brwOpenAddress plays a YouTube address the reader was asked to load — typed
// or pasted, a bookmark, a link from another screen — and reports whether it
// was one. The address bar goes back to the page on screen: the video's URL
// left in it would make Reload replay the video and Save keep it as a page.
func brwOpenAddress(config *Config, variable, address string) bool {
	link, ok := brwYouTubeVideo(address)
	if !ok || config == nil {
		return false
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	page := brwPageOnScreen(variable)
	config.Variables.Custom[textBrowserURLVariable] = page
	played := brwPlayFromBrowser(config, link, "")
	if page == "" {
		// Nothing was on screen — another screen opened the browser on this
		// link — so the reader says where it went instead of a "Loading …"
		// that never ends.
		note := brwLandingNote
		if !played {
			note = brwLandingStaleNote
		}
		config.Variables.Custom[variable] = note
	}
	return true
}

// ── Redirects that land on a video ───────────────────────────────────────

// brwLandingMaxAge is how long a landing waits for the browser to be on
// screen and idle. One older than that is from a visit the reader has left:
// it must not start a video minutes later, when they come back.
const brwLandingMaxAge = 10000

// brwLandingNote replaces the reader's "Loading …" when a load ended on a
// video and there is no page to put back; brwLandingStaleNote when the video
// was not started (the landing waited too long, or the handheld is offline).
const (
	brwLandingNote      = "That link opened a video in Media."
	brwLandingStaleNote = "That link leads to a video. Open it again to play it in Media."
)

// brwLanding carries "this load ended on a YouTube video" from the fetch
// goroutine to the render pass, which plays it and puts back the page the
// link was followed from. The link handlers catch direct links before any
// request; this is for a shortener or a tracking page that redirects there.
var brwLanding struct {
	mu       sync.Mutex
	pending  bool
	at       uint64 // when the load landed (sdl ticks)
	variable string
	from     string // the address that was loaded
	back     string // the page on screen before it
	recorded bool   // the load added from to the history
	request  uint64 // the load's request number (textBrowserRequestSequence)
	link     brwYouTubeLink
}

// brwQueueLanding reports whether a finished load landed on a YouTube video,
// queueing the hand-off when it did. The caller publishes nothing then.
// request is the load's number in textBrowserRequestSequence: the render pass
// plays the landing only while no newer load has started.
func brwQueueLanding(variable, from, back string, recorded bool, request uint64, finalURL string) bool {
	if finalURL == "" || finalURL == from {
		return false
	}
	link, ok := brwYouTubeVideo(finalURL)
	if !ok {
		return false
	}
	brwLanding.mu.Lock()
	brwLanding.pending = true
	brwLanding.at = sdl.GetTicks64()
	brwLanding.variable, brwLanding.from, brwLanding.back = variable, from, back
	brwLanding.recorded, brwLanding.request, brwLanding.link = recorded, request, link
	brwLanding.mu.Unlock()
	return true
}

// brwTakeLanding plays a queued landing on the render pass, once the browser is
// settled on screen with no keyboard up. The hop leaves the history, the page
// the link was on comes back (from the page cache, so in the same frame) —
// or, with no page to put back, the reader says where the link went — and the
// video starts in Media. A landing for a load the reader has already navigated
// away from, or one that waited too long, plays nothing.
func brwTakeLanding(config *Config, element Element) {
	if config == nil || virtualKeyboardActive || transitionPhase != "none" {
		return
	}
	brwLanding.mu.Lock()
	if !brwLanding.pending {
		brwLanding.mu.Unlock()
		return
	}
	brwLanding.pending = false
	at, variable, from, back := brwLanding.at, brwLanding.variable, brwLanding.from, brwLanding.back
	recorded, request, link := brwLanding.recorded, brwLanding.request, brwLanding.link
	brwLanding.mu.Unlock()
	if element.Variable != "" && variable != element.Variable {
		return
	}
	// Still the reader's latest load? The address bar cannot tell: the
	// keyboard writes into it while an address is being typed.
	textBrowserRequestMutex.Lock()
	current := textBrowserRequestSequence[variable] == request
	textBrowserRequestMutex.Unlock()
	if !current {
		return
	}
	if recorded {
		brwDropHop(from)
	}
	// The page to put back: the one on screen when the link was followed, or
	// for a load the history made, the entry the hop was recorded after.
	restore := back
	if recorded {
		if cur := brwHistoryCurrent(); cur != "" {
			restore = cur
		}
	}
	restored := false
	if normal, err := normalizeTextBrowserURL(restore); err == nil && restore != "" && normal != from {
		loadTextBrowserURLAt(config, element, normal, false)
		restored = true
		restore = normal
	} else {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[textBrowserURLVariable] = restore
	}
	played := false
	if sdl.GetTicks64()-at <= brwLandingMaxAge {
		played = brwPlayFromBrowser(config, link, "")
	}
	if !restored {
		// No page to put back: the reader says where the link went.
		note := brwLandingNote
		if !played {
			note = brwLandingStaleNote
		}
		config.Variables.Custom[variable] = note
	}
	if restored && !played {
		// The page came back, but the video did not start (the landing
		// waited too long, or the handheld is offline): say so.
		ShowToast(brwLandingStaleNote, ToastKindInfo)
	}
	// The page just put back is still at the top: the row to come back to is
	// the one the browser saved when the link was followed.
	if played && restored && brwReturn.address == restore && !brwReturn.armed {
		if row := browserScrollMemoryGet(restore); row > 0 {
			brwReturn.armed, brwReturn.row = true, row
		}
	}
}

// brwDropHop removes the current history entry when it is from, a load that
// only ever led to a video: Back from the restored page must not step into it.
func brwDropHop(from string) {
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	i := browserState.index
	if i < 0 || i >= len(browserState.history) || browserState.history[i] != from {
		return
	}
	browserState.history = append(browserState.history[:i], browserState.history[i+1:]...)
	browserState.index = i - 1
}
