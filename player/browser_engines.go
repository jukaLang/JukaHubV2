package main

// browser_engines.go — where the browser searches, and where Home goes.
//
// Both used to be constants: DuckDuckGo Lite answered every search (the address
// bar, the Search chip and Y, Home search's "Search the web") and its start
// page was the only home. Both are settings now — Settings → Browser — kept in
// jukauser.json as plain custom keys:
//
//   - browserSearchEngine: an id from brwEngines ("duckduckgo" when absent or
//     unknown), or "custom", with browserSearchCustom holding a results
//     address where %s marks the words (one ending in "=" takes them at the
//     end).
//   - browserHomePage: the page Home, and a Browser opened from its tile, start
//     on. Empty follows the engine: its own start page, so choosing Google makes
//     Home google.com. A saved page can be made home from Saved pages (Y) as
//     well as from Settings, and the last address typed for it is kept in
//     browserHomeTyped so stepping away from it in Settings does not lose it.
//
// Both addresses are typed on their own keyboard field (vkTargetBrowserURL),
// which applies them on submit only: a half-typed host never becomes the home
// page or the engine, and B leaves both as they were.
//
// The reader runs page scripts in a small engine of its own (webdom_runtime.go),
// but an engine is still only as good as the HTML it sends. Google sends no
// results: since 2025 its results page is a robot check (Google calls it
// SearchGuard) built to admit full desktop and phone browsers and nothing
// else. A script-less browser is redirected to "turn on JavaScript", and a
// small engine like this one fails the check just the same — passing it would
// mean disguising the reader as Chrome, which is exactly what the check exists
// to catch, so the reader does not try. Google stays on the list — it is the
// engine people ask for by name, its home page loads, and on a PC the desktop
// browser is one press away — but a search that lands on that wall draws a card
// (brwSearchWall) instead of Google's apology: the same words on Startpage,
// which serves Google's results as plain HTML, or a switch to it for good.
//
// Result links are often click-tracking hops through the engine. DuckDuckGo's
// and Google's redirect the reader on their own, but Bing's is a script that
// never runs here, so the destination is read out of every known wrapper
// before the load (brwSearchClickTarget): a result opens the page it names,
// one request sooner, without reporting the click.

import (
	"bytes"
	"encoding/base64"
	"net"
	"net/url"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The settings' keys in the config's custom block (jukauser.json).
const (
	brwEngineKey       = "browserSearchEngine"
	brwEngineCustomKey = "browserSearchCustom"
	brwHomePageKey     = "browserHomePage"
	brwHomeTypedKey    = "browserHomeTyped"
)

const (
	// brwEngineDefault is the engine a fresh install searches with: DuckDuckGo
	// Lite, whose page is built for a simple browser.
	brwEngineDefault = "duckduckgo"
	// brwEngineCustomID is the engine the custom address describes.
	brwEngineCustomID = "custom"
	// brwEngineWallAlt is where a search Google walls off goes instead:
	// Startpage serves Google's own results as plain HTML.
	brwEngineWallAlt = "startpage"
	// brwSettingsSection is the Settings section with the browser's rows.
	brwSettingsSection = "browser"
)

// The two sheet rows that open the keyboard instead of choosing a value. Neither
// is an engine id or a web address, so neither can collide with a real choice.
const (
	brwSheetTypeEngine = "+type-engine"
	brwSheetTypeHome   = "+type-home"
)

// brwEngine is one search engine the browser can send words to.
type brwEngine struct {
	ID   string // what browserSearchEngine stores
	Name string // what Settings, the Search keyboard and Home search show
	// Home is the engine's start page, which Home opens while the home page
	// follows the engine.
	Home string
	// Search is the results address; %s marks where the escaped words go.
	Search string
	// Note is the line under the engine's name in Settings.
	Note string
	// Wall marks an engine whose results sit behind a check only full browsers
	// pass: a search that lands on its wall draws the card brwSearchWall builds.
	Wall bool
}

// brwEngines are the engines Settings offers, in the order it lists them.
// DuckDuckGo Lite leads: it is the default, and its page is built for a simple
// browser. Startpage follows — it serves Google's results as plain HTML — and
// the rest serve HTML too, except Google (Wall).
var brwEngines = []brwEngine{
	{ID: "duckduckgo", Name: "DuckDuckGo", Home: "https://lite.duckduckgo.com/lite/",
		Search: "https://lite.duckduckgo.com/lite/?q=%s", Note: "Private · the Lite page is made for simple browsers"},
	{ID: "startpage", Name: "Startpage", Home: "https://www.startpage.com/",
		Search: "https://www.startpage.com/sp/search?query=%s", Note: "Google's results, privately, without JavaScript"},
	{ID: "google", Name: "Google", Home: "https://www.google.com/",
		Search: "https://www.google.com/search?q=%s", Note: "Results need a full browser · each search offers Startpage", Wall: true},
	{ID: "bing", Name: "Bing", Home: "https://www.bing.com/",
		Search: "https://www.bing.com/search?q=%s", Note: "Microsoft's results"},
	{ID: "brave", Name: "Brave Search", Home: "https://search.brave.com/",
		Search: "https://search.brave.com/search?q=%s", Note: "Its own index · private"},
	{ID: "yahoo", Name: "Yahoo", Home: "https://search.yahoo.com/",
		Search: "https://search.yahoo.com/search?p=%s", Note: "Yahoo's results"},
	{ID: "mojeek", Name: "Mojeek", Home: "https://www.mojeek.com/",
		Search: "https://www.mojeek.com/search?q=%s", Note: "Its own index · no tracking"},
	{ID: "wikipedia", Name: "Wikipedia", Home: "https://en.m.wikipedia.org/",
		Search: "https://en.m.wikipedia.org/w/index.php?search=%s", Note: "Opens the article when a title matches"},
}

// brwEngineByID finds an engine of the catalog by its id.
func brwEngineByID(id string) (brwEngine, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, e := range brwEngines {
		if e.ID == id {
			return e, true
		}
	}
	return brwEngine{}, false
}

// brwDefaultEngine is DuckDuckGo Lite: its page is built for a browser without
// a full JavaScript engine, so a fresh install can search without a wall.
func brwDefaultEngine() brwEngine {
	e, _ := brwEngineByID(brwEngineDefault)
	return e
}

// brwWallAltEngine is Startpage: where a search Google walls off goes instead.
func brwWallAltEngine() brwEngine {
	e, _ := brwEngineByID(brwEngineWallAlt)
	return e
}

// brwHasDesktopBrowser reports whether this device has a web browser of its own
// to hand a page to: a PC does, a TrimUI handheld does not. It goes by the
// detected device rather than the build, because a Linux PC runs the same
// build as the handheld. Detection runs once, so the fetch goroutine may ask.
func brwHasDesktopBrowser() bool { return !DetectDevice().IsTrimui() }

// brwSetting reads one of the browser's string settings. A value of another
// type (a hand-edited jukauser.json) reads as unset rather than as "<nil>".
func brwSetting(c *Config, key string) string {
	if c == nil || c.Variables.Custom == nil {
		return ""
	}
	if v, ok := customValue(c.Variables.Custom, key); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// brwStore writes one of the browser's settings through the shared settings
// writer, under its exact key: a copy spelt in other capitals (the web builder
// capitalises keys) is dropped first, so it cannot shadow the new value.
func brwStore(c *Config, key, value string) {
	if c == nil {
		return
	}
	dropCustomMirror(c, key)
	setStoreString(c, key, strings.TrimSpace(value))
}

// brwCustomTemplate checks a typed results address and returns it in its %s
// form: an http(s) address naming a real host (one with a dot, an IP address
// or localhost), with %s where the words go — or ending in "=", which takes
// them at the end (what "…/search?q=" means to whoever typed it). A bare host
// gains https://; a scheme typed twice ("https://https://…") is refused rather
// than read as a host called "https".
func brwCustomTemplate(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", false
	}
	if !strings.Contains(t, "://") {
		t = "https://" + t
	}
	if !strings.Contains(t, "%s") {
		if !strings.HasSuffix(t, "=") {
			return "", false
		}
		t += "%s"
	}
	// "%s" is not a valid escape, so the address is checked with a word in its
	// place.
	u, err := url.Parse(strings.ReplaceAll(t, "%s", "jukahub"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", false
	}
	// "https://https://…" parses as a host called "https", which the dot rule
	// refuses along with any other bare word.
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, ".") && host != "localhost" && net.ParseIP(host) == nil {
		return "", false
	}
	return t, true
}

// brwCustomEngine is the engine the custom address describes, when it is
// usable. Its name is the site's host, which says more than "Custom" does in
// the Search keyboard and in Home search.
func brwCustomEngine(c *Config) (brwEngine, bool) {
	t, ok := brwCustomTemplate(brwSetting(c, brwEngineCustomKey))
	if !ok {
		return brwEngine{}, false
	}
	u, err := url.Parse(strings.ReplaceAll(t, "%s", "jukahub"))
	if err != nil {
		return brwEngine{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return brwEngine{
		ID:     brwEngineCustomID,
		Name:   host,
		Home:   u.Scheme + "://" + u.Host + "/",
		Search: t,
		Note:   "Your own address · " + host,
	}, true
}

// brwEngineFor is the engine a config searches with. An unknown id, or
// "custom" without a usable address, is DuckDuckGo: a typo in jukauser.json
// must not leave the browser unable to search.
func brwEngineFor(c *Config) brwEngine {
	id := strings.ToLower(brwSetting(c, brwEngineKey))
	if id == brwEngineCustomID {
		if e, ok := brwCustomEngine(c); ok {
			return e
		}
		return brwDefaultEngine()
	}
	if e, ok := brwEngineByID(id); ok {
		return e
	}
	return brwDefaultEngine()
}

// brwSearchURLWith is e's results address for query. Words that land in the
// path of a custom address are path-escaped; everywhere else they are a query
// value ("trimui smart pro" → trimui+smart+pro).
func brwSearchURLWith(e brwEngine, query string) string {
	query = strings.TrimSpace(query)
	escaped := url.QueryEscape(query)
	if at := strings.Index(e.Search, "%s"); at >= 0 {
		if q := strings.IndexByte(e.Search, '?'); q < 0 || at < q {
			escaped = url.PathEscape(query)
		}
	}
	return strings.ReplaceAll(e.Search, "%s", escaped)
}

// brwSearchURLFor is the chosen engine's results address for query.
func brwSearchURLFor(c *Config, query string) string {
	return brwSearchURLWith(brwEngineFor(c), query)
}

// brwEngineAddress is e's results for query, or its start page when there are
// no words to search for.
func brwEngineAddress(e brwEngine, query string) string {
	if strings.TrimSpace(query) == "" {
		return e.Home
	}
	return brwSearchURLWith(e, query)
}

// brwEngineNote is the line Settings shows under an engine. Google's depends on
// the device: on a PC the wall card offers the desktop's own browser first.
func brwEngineNote(e brwEngine) string {
	if e.Wall && brwHasDesktopBrowser() {
		return "Results need a full browser · each search offers your web browser"
	}
	return e.Note
}

// brwSearchQueryOf reads the words back out of a results address, so a search
// can be run again on another engine.
func brwSearchQueryOf(address string) string {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	values := u.Query()
	for _, key := range []string{"q", "query", "p", "search"} {
		if v := strings.TrimSpace(values.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

// brwSyncSearchPrompt names the engine in the Search keyboard's placeholder, so
// the empty field says where the words will go ("Search Google").
func brwSyncSearchPrompt(c *Config) {
	browserSearchPrompt = "Search " + brwEngineFor(c).Name
}

// brwSetEngine stores the search engine.
func brwSetEngine(c *Config, id string) {
	brwStore(c, brwEngineKey, strings.ToLower(strings.TrimSpace(id)))
	brwSyncSearchPrompt(c)
}

// ── Home page ────────────────────────────────────────────────────────────

// brwHomeChoice is the stored home page: an address, or "" while Home follows
// the search engine.
func brwHomeChoice(c *Config) string { return brwSetting(c, brwHomePageKey) }

// brwHomeAddress turns a stored or typed home page into an address the reader
// opens: "example.com" gains https://, while a word or another scheme is
// refused instead of becoming a search, which is not a page anyone meant as
// home.
func brwHomeAddress(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || (!strings.Contains(raw, "://") && !textBrowserLooksLikeAddress(raw)) {
		return "", false
	}
	address, err := normalizeTextBrowserURL(raw)
	if err != nil {
		return "", false
	}
	return address, true
}

// brwHomeURL is the page Home opens: the chosen one when it is an address,
// otherwise the search engine's start page.
func brwHomeURL(c *Config) string {
	if address, ok := brwHomeAddress(brwHomeChoice(c)); ok {
		return address
	}
	return brwEngineFor(c).Home
}

// brwIsHomePage reports whether address is the home page the user chose.
func brwIsHomePage(c *Config, address string) bool {
	chosen, ok := brwHomeAddress(brwHomeChoice(c))
	return ok && browserBookmarkKey(chosen) == browserBookmarkKey(address)
}

// brwIsStartPage reports whether address is a search engine's start page: the
// Browser at rest, not a page being read, so Handoff does not offer it to
// another device. A home page of the user's own (a news site, say) is reading
// and does not count.
func brwIsStartPage(c *Config, address string) bool {
	key := browserBookmarkKey(address)
	if key == "" {
		return false
	}
	if key == browserBookmarkKey(brwEngineFor(c).Home) {
		return true
	}
	for _, e := range brwEngines {
		if key == browserBookmarkKey(e.Home) {
			return true
		}
	}
	return false
}

// brwSetHome stores the home page: an address, or "" to follow the engine.
func brwSetHome(c *Config, address string) {
	brwStore(c, brwHomePageKey, address)
}

// browserBookmarkPanelMakeHome is Y in Saved pages: the highlighted page
// becomes the home page, which is the way to choose one without typing an
// address on a D-pad.
func browserBookmarkPanelMakeHome(config *Config) {
	marks := browserBookmarksList()
	i := browserBookmarkPanel.cursor
	if i < 0 || i >= len(marks) {
		return
	}
	mark := marks[i]
	if brwIsHomePage(config, mark.URL) {
		ShowToast("“"+mark.Title+"” is already your home page", ToastKindInfo)
		return
	}
	brwSetHome(config, mark.URL)
	PlayActivateSound()
	ShowToast("Home page: “"+mark.Title+"”", ToastKindInfo)
}

// ── Click-tracking links ─────────────────────────────────────────────────

// brwSearchClickTarget reports where a search engine's click-tracking link
// leads: DuckDuckGo's /l/?uddg=, Google's /url?q=, Bing's /ck/a?u=a1…, Yahoo's
// r.search.yahoo.com/…/RU=…/. The destination must be a page the reader may
// open (http or https, a host, no credentials); a wrapper around anything else
// is left as it was.
func brwSearchClickTarget(address string) (string, bool) {
	cur, changed := strings.TrimSpace(address), false
	for i := 0; i < brwUnwrapLimit; i++ {
		next := brwSearchClickNext(cur)
		if next == "" {
			break
		}
		normalized, err := normalizeTextBrowserURL(next)
		if err != nil {
			break
		}
		cur, changed = normalized, true
	}
	return cur, changed
}

// brwSearchClickNext is the destination one click-tracking link names, or "".
func brwSearchClickNext(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimSuffix(u.Path, "/")
	next := ""
	switch {
	case brwHostIs(host, "duckduckgo.com") && path == "/l":
		next = u.Query().Get("uddg")
	case brwIsGoogleHost(host) && path == "/url":
		next = u.Query().Get("q")
		if next == "" {
			next = u.Query().Get("url")
		}
	case brwHostIs(host, "bing.com") && path == "/ck/a":
		next = brwBingTarget(u.Query().Get("u"))
	case brwHostIs(host, "r.search.yahoo.com"):
		next = brwYahooTarget(u.EscapedPath())
	}
	next = strings.TrimSpace(next)
	if !brwWebLink(next) {
		return ""
	}
	return next
}

// brwBingTarget decodes Bing's u= parameter: "a1", then the destination in
// URL-safe base64 without padding.
func brwBingTarget(u string) string {
	u = strings.TrimSpace(u)
	if len(u) < 3 || !strings.HasPrefix(u, "a1") {
		return ""
	}
	payload := strings.TrimRight(u[2:], "=")
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(payload); err != nil {
			return ""
		}
	}
	return string(data)
}

// brwYahooTarget reads the destination out of a Yahoo result's escaped path,
// /…/RU=https%3a%2f%2fexample.com%2f/RK=2/RS=…: the address is escaped, so the
// first "/" after RU= ends it.
func brwYahooTarget(escapedPath string) string {
	i := strings.Index(escapedPath, "/RU=")
	if i < 0 {
		return ""
	}
	rest := escapedPath[i+len("/RU="):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	target, err := url.PathUnescape(rest)
	if err != nil {
		return ""
	}
	return target
}

// ── Google's wall ────────────────────────────────────────────────────────

// brwSearchWall reports whether a load landed on Google's wall instead of its
// results — the robot-check page (whose <noscript> says "turn on JavaScript"),
// or the "unusual traffic" check behind it — and builds the card that replaces
// it. It runs before the page's scripts on purpose: the check is built to turn
// away everything but full browsers, so letting the reader's engine run it would
// spend the whole script budget to end on the same wall.
// requested is the address that was asked for (the search the card's buttons
// run again), landed is where the redirects ended and body is that page. A
// failure carries no FinalURL, so the wall is never cached and Reload asks
// Google again.
func brwSearchWall(requested string, landed *url.URL, body []byte) (brwFailure, bool) {
	if landed == nil || !brwIsGoogleHost(strings.ToLower(landed.Hostname())) {
		return brwFailure{}, false
	}
	path := landed.Path
	captcha := strings.HasPrefix(path, "/sorry/")
	wall := strings.HasPrefix(path, "/httpservice/retry/enablejs") ||
		(path == "/search" && bytes.Contains(body, []byte("/httpservice/retry/enablejs")))
	if !captcha && !wall {
		return brwFailure{}, false
	}
	f := brwNewFailure(brwFailWall, requested, "")
	f.Host = landed.Hostname()
	// The words say what is true now that the reader runs JavaScript: the
	// obstacle is a check for full browsers, not a missing script engine. The
	// card holds three lines of detail, so each sentence stays short.
	if captcha {
		f.Title = "Google wants to check for robots"
		f.Detail = "Google paused this search for a robot check only full browsers pass. " + brwWallWayOut()
	} else {
		f.Title = "Google needs a full web browser"
		f.Detail = "Its robot check turns away small script engines like this one. " + brwWallWayOut()
	}
	return f, true
}

// brwWallWayOut is the card's second sentence: what its buttons offer here.
func brwWallWayOut() string {
	alt := brwWallAltEngine().Name
	if !brwHasDesktopBrowser() {
		return alt + " shows Google's results without the check."
	}
	return "Open the search in your web browser, or try " + alt + ": Google's results without the check."
}

// brwWallButtonSets caches the card's buttons per device kind (handheld, PC):
// the card asks for them every frame it is drawn.
var brwWallButtonSets [2][]brwCardButton

// brwWallButtons are the wall card's buttons. On the handheld the way round is
// Startpage, now or from now on; a PC opens the search in its own browser,
// which runs Google's scripts. Both can change the engine in Settings.
func brwWallButtons() []brwCardButton {
	kind := 0
	if brwHasDesktopBrowser() {
		kind = 1
	}
	if brwWallButtonSets[kind] != nil {
		return brwWallButtonSets[kind]
	}
	alt := brwWallAltEngine().Name
	buttons := []brwCardButton{
		{action: brwActWallSearch, label: "Search on " + alt, icon: "search"},
		{action: brwActWallAlways, label: "Always use " + alt, icon: "check"},
		{action: brwActWallEngines, label: "Change engine", icon: "settings"},
	}
	if kind == 1 {
		buttons = []brwCardButton{
			{action: brwActWallBrowser, label: "Open in web browser", icon: "link"},
			{action: brwActWallSearch, label: "Search on " + alt, icon: "search"},
			{action: brwActWallEngines, label: "Change engine", icon: "settings"},
		}
	}
	brwWallButtonSets[kind] = buttons
	return buttons
}

// brwWallActivate runs one of the wall card's buttons.
func brwWallActivate(config *Config, elem Element, f brwFailure, action brwCardAction) {
	alt := brwWallAltEngine()
	query := brwSearchQueryOf(f.URL)
	switch action {
	case brwActWallSearch:
		PlayActivateSound()
		loadTextBrowserURL(config, elem, brwEngineAddress(alt, query))
	case brwActWallAlways:
		PlayActivateSound()
		brwSetEngine(config, alt.ID)
		ShowToast("Searches use "+alt.Name+" now · Settings → Browser changes it", ToastKindInfo)
		loadTextBrowserURL(config, elem, brwEngineAddress(alt, query))
	case brwActWallEngines:
		PlayActivateSound()
		brwOpenSettings(config)
	case brwActWallBrowser:
		if err := openExternalURL(f.URL); err != nil {
			ShowToast("Couldn't open your web browser: "+err.Error(), ToastKindWarning)
			return
		}
		PlayActivateSound()
		ShowToast("Opened in your web browser", ToastKindInfo)
	}
}

// ── Typing an address ────────────────────────────────────────────────────

// brwPrefField is the address the keyboard's vkTargetBrowserURL field edits.
type brwPrefField int

const (
	brwPrefHome   brwPrefField = iota // the home page
	brwPrefEngine                     // a custom search engine's results address
)

// brwPref is that field: which address, and the text while it is typed. The
// text is applied on submit only (brwPrefSubmit) and cleared when the keyboard
// closes, so B leaves the stored address as it was.
var brwPref struct {
	field  brwPrefField
	buffer string
}

// brwOpenPrefKeyboard opens the on-screen keyboard on one of the two addresses.
// It holds the stored one, selected, so the first key typed replaces it — the
// browser's address bar works the same way.
func brwOpenPrefKeyboard(c *Config, field brwPrefField) {
	current := brwHomeChoice(c)
	if field == brwPrefEngine {
		current = brwSetting(c, brwEngineCustomKey)
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = vkTargetBrowserURL
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	brwPref.field, brwPref.buffer = field, current
	vkTextSelected = current != ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// brwPrefTitle is the keyboard's title for the field.
func brwPrefTitle() string {
	if brwPref.field == brwPrefEngine {
		return "SEARCH ADDRESS"
	}
	return "HOME PAGE"
}

// brwPrefPlaceholder is what the empty field shows.
func brwPrefPlaceholder() string {
	if brwPref.field == brwPrefEngine {
		return "https://searx.example/search?q=%s"
	}
	return "news.ycombinator.com"
}

// brwPrefSubmit applies what was typed and says what happened.
func brwPrefSubmit(c *Config, text string) {
	msg, warn := brwPrefApply(c, brwPref.field, text)
	kind := ToastKindInfo
	if warn {
		kind = ToastKindWarning
	}
	ShowToast(msg, kind)
}

// brwPrefApply stores a submitted address and returns the toast that reports
// it, and whether that toast is a warning. An address that is not usable
// changes nothing. An empty home page means "follow the engine"; an empty
// search address keeps the one there was.
func brwPrefApply(c *Config, field brwPrefField, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if field == brwPrefEngine {
		if text == "" {
			return "No address typed · the search engine is unchanged", false
		}
		t, ok := brwCustomTemplate(text)
		if !ok {
			return "That needs http(s), a site and %s where the words go · the search engine is unchanged", true
		}
		brwStore(c, brwEngineCustomKey, t)
		brwSetEngine(c, brwEngineCustomID)
		return "Search engine: " + brwEngineFor(c).Name, false
	}
	if text == "" {
		brwSetHome(c, "")
		return "Home page: " + brwStartPageLabel(c), false
	}
	address, ok := brwHomeAddress(text)
	if !ok {
		return "“" + truncate(text, 40) + "” isn't a web address · the home page is unchanged", true
	}
	brwStore(c, brwHomeTypedKey, address)
	brwSetHome(c, address)
	return "Home page: " + browserLinkHost(address), false
}

// ── Settings → Browser ───────────────────────────────────────────────────

// brwOpenSettings opens Settings on its Browser section: the wall card's
// Change engine and Home search's "Browser settings" land there.
func brwOpenSettings(config *Config) {
	if config == nil {
		return
	}
	section := setSectionIndex(brwSettingsSection)
	if section < 0 {
		return
	}
	if setIsHubScene(setHubSceneName(config)) {
		// Already in Settings: switch sections in place.
		setHubSelectSection(config, section, false)
		setHubEnterPane(false)
		return
	}
	if sceneIndexExact(config, "Settings") < 0 {
		setToast("That screen is not part of this setup")
		return
	}
	setHub.lastSection = section
	setHub.deepLink = true
	setOpenScene("Settings")(config)
}

// brwSettingsRows are the rows of Settings → Browser.
func brwSettingsRows() []setRow {
	return []setRow{
		{
			ID: "searchEngine", Label: "Search engine", Kind: setKindChoice,
			Detail:     brwEngineDetail,
			Options:    brwEngineOptions,
			Current:    brwEngineCurrent,
			Sheet:      brwEngineSheet,
			SheetValue: func(c *Config) string { return brwEngineFor(c).ID },
			SheetTitle: "Search engine",
			Value:      func(c *Config) string { return brwEngineFor(c).Name },
			Choose:     brwChooseEngine,
		},
		{
			ID: "homePage", Label: "Home page", Kind: setKindChoice,
			Detail:     brwHomeDetail,
			Options:    brwHomeOptions,
			Current:    brwHomeCurrent,
			Sheet:      brwHomeSheet,
			SheetValue: brwHomeSheetValue,
			SheetTitle: "Home page",
			Value:      brwHomeValue,
			Choose:     brwChooseHome,
		},
	}
}

// brwEngineOptions are the carousel's stops: every engine, then the custom
// address when there is a usable one.
func brwEngineOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(brwEngines)+1)
	for _, e := range brwEngines {
		out = append(out, setOption{Value: e.ID, Label: e.Name})
	}
	if e, ok := brwCustomEngine(c); ok {
		out = append(out, setOption{Value: e.ID, Label: "Custom · " + e.Name})
	}
	return out
}

// brwEngineCurrent is the stop of the engine in force (DuckDuckGo's while a
// custom address is unusable).
func brwEngineCurrent(c *Config) int {
	id := brwEngineFor(c).ID
	for i, o := range brwEngineOptions(c) {
		if o.Value == id {
			return i
		}
	}
	return -1
}

// brwEngineSheet is the list A opens: every engine with its note, the custom
// address when there is one, and a row that types a new one.
func brwEngineSheet(c *Config) []setOption {
	out := make([]setOption, 0, len(brwEngines)+2)
	for _, e := range brwEngines {
		out = append(out, setOption{Value: e.ID, Label: e.Name, Note: brwEngineNote(e)})
	}
	typeLabel := "Custom address…"
	if e, ok := brwCustomEngine(c); ok {
		out = append(out, setOption{Value: e.ID, Label: "Custom · " + e.Name, Note: truncate(e.Search, 72)})
		typeLabel = "Change the custom address…"
	}
	out = append(out, setOption{Value: brwSheetTypeEngine, Label: typeLabel,
		Note: "A results address with %s where the words go"})
	return out
}

// brwEngineDetail is the line under Search engine: the engine's note, or why
// the custom address in jukauser.json is not the one being used.
func brwEngineDetail(c *Config) string {
	if strings.EqualFold(brwSetting(c, brwEngineKey), brwEngineCustomID) {
		if _, ok := brwCustomEngine(c); !ok {
			return "The custom address isn't usable · searching " + brwDefaultEngine().Name
		}
	}
	return brwEngineNote(brwEngineFor(c))
}

// brwChooseEngine applies a choice from the carousel or the sheet. The sheet's
// last row types a custom address; the engine changes only when a usable one
// is submitted.
func brwChooseEngine(c *Config, value string) {
	if value == brwSheetTypeEngine {
		brwOpenPrefKeyboard(c, brwPrefEngine)
		return
	}
	brwSetEngine(c, value)
	e := brwEngineFor(c)
	msg := "Search engine: " + e.Name
	if e.Wall {
		msg += " · its results need a full web browser"
	}
	setToast(msg)
}

// brwStartPageLabel is the home page option that follows the search engine.
func brwStartPageLabel(c *Config) string {
	return brwEngineFor(c).Name + " start page"
}

// brwBookmarkTitles maps each saved page's key to its title, so naming every
// option of a list is one pass over the saved pages rather than one per row.
func brwBookmarkTitles(marks []browserBookmark) map[string]string {
	titles := make(map[string]string, len(marks))
	for _, m := range marks {
		if title := strings.TrimSpace(m.Title); title != "" {
			titles[browserBookmarkKey(m.URL)] = title
		}
	}
	return titles
}

// brwHomeOptionLabel names a home page in the carousel, the sheet and the
// toasts: the saved page's own title when it is one, otherwise its host and
// path.
func brwHomeOptionLabel(address string, titles map[string]string) string {
	if title, ok := titles[browserBookmarkKey(address)]; ok {
		return title
	}
	return browserLinkHost(address)
}

// brwHomeOptions are the carousel's stops: the engine's start page, the last
// typed address, then the saved pages, newest first. Their order must not
// depend on which one is chosen, or every step would reshuffle the row and
// Left/Right would bounce between two stops; only a chosen page that is none
// of them (the page that was open in the Browser) gets a stop of its own,
// right after the start page.
func brwHomeOptions(c *Config) []setOption {
	marks := browserBookmarksList()
	titles := brwBookmarkTitles(marks)
	var stable []string
	if typed, ok := brwHomeAddress(brwSetting(c, brwHomeTypedKey)); ok {
		stable = append(stable, typed)
	}
	for _, m := range marks {
		stable = append(stable, m.URL)
	}
	listed := make(map[string]bool, len(stable))
	for _, address := range stable {
		listed[browserBookmarkKey(address)] = true
	}
	out := make([]setOption, 0, len(stable)+2)
	out = append(out, setOption{Value: "", Label: brwStartPageLabel(c)})
	seen := map[string]bool{}
	add := func(address string) {
		key := browserBookmarkKey(address)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, setOption{Value: address, Label: brwHomeOptionLabel(address, titles)})
	}
	if chosen, ok := brwHomeAddress(brwHomeChoice(c)); ok && !listed[browserBookmarkKey(chosen)] {
		add(chosen)
	}
	for _, address := range stable {
		add(address)
	}
	return out
}

// brwHomeCurrent is the stop of the stored home page: 0 while it follows the
// engine, -1 when the stored text is not an address (the row then says so).
func brwHomeCurrent(c *Config) int {
	choice := brwHomeChoice(c)
	if choice == "" {
		return 0
	}
	address, ok := brwHomeAddress(choice)
	if !ok {
		return -1
	}
	key := browserBookmarkKey(address)
	for i, o := range brwHomeOptions(c) {
		if i > 0 && browserBookmarkKey(o.Value) == key {
			return i
		}
	}
	return -1
}

// brwHomeValue is the row's value when the stored home page is not one of the
// carousel's stops: text that is not a web address.
func brwHomeValue(c *Config) string {
	if address, ok := brwHomeAddress(brwHomeChoice(c)); ok {
		return browserLinkHost(address)
	}
	return "Not a web address"
}

// brwHomeDetail says what Home opens.
func brwHomeDetail(c *Config) string {
	choice := brwHomeChoice(c)
	if choice == "" {
		return "Follows the search engine · " + browserLinkHost(brwEngineFor(c).Home)
	}
	if address, ok := brwHomeAddress(choice); ok {
		return "Home and a new Browser open " + browserLinkHost(address)
	}
	return "“" + truncate(choice, 32) + "” isn't a web address · Home opens the start page"
}

// brwHomeSheet is the list A opens: the engine's start page, the page open in
// the Browser, the chosen and the last typed address, the saved pages, and a
// row that types an address.
func brwHomeSheet(c *Config) []setOption {
	marks := browserBookmarksList()
	titles := brwBookmarkTitles(marks)
	out := make([]setOption, 0, len(marks)+5)
	out = append(out, setOption{Value: "", Label: brwStartPageLabel(c),
		Note: "Follows the search engine · " + browserLinkHost(brwEngineFor(c).Home)})
	seen := map[string]bool{}
	add := func(address, label, note string) {
		key := browserBookmarkKey(address)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, setOption{Value: address, Label: label, Note: note})
	}
	if c != nil {
		// The page the Browser has open (or had, last time it was used): the
		// way to make the page you are reading home without typing it.
		if open, ok := c.Variables.Custom[textBrowserURLVariable].(string); ok {
			if address, ok := brwHomeAddress(open); ok && !brwIsStartPage(c, address) {
				add(address, "Page open in the Browser", browserLinkHost(address))
			}
		}
	}
	for _, stored := range []string{brwHomeChoice(c), brwSetting(c, brwHomeTypedKey)} {
		if address, ok := brwHomeAddress(stored); ok {
			add(address, brwHomeOptionLabel(address, titles), browserLinkHost(address))
		}
	}
	for _, m := range marks {
		add(m.URL, brwHomeOptionLabel(m.URL, titles), "Saved page · "+browserLinkHost(m.URL))
	}
	out = append(out, setOption{Value: brwSheetTypeHome, Label: "Type an address…",
		Note: "Any page, like news.ycombinator.com"})
	return out
}

// brwHomeSheetValue is the sheet row to tick: the stored home page, matched the
// way saved pages are (a trailing slash or a #fragment is the same page). Text
// that is not an address ticks the start page, which is what Home opens.
func brwHomeSheetValue(c *Config) string {
	address, ok := brwHomeAddress(brwHomeChoice(c))
	if !ok {
		return ""
	}
	key := browserBookmarkKey(address)
	for _, o := range brwHomeSheet(c) {
		if o.Value != "" && o.Value != brwSheetTypeHome && browserBookmarkKey(o.Value) == key {
			return o.Value
		}
	}
	return address
}

// brwChooseHome applies a choice from the carousel or the sheet. The sheet's
// last row types an address, which applies only when it is submitted.
func brwChooseHome(c *Config, value string) {
	if value == brwSheetTypeHome {
		brwOpenPrefKeyboard(c, brwPrefHome)
		return
	}
	brwSetHome(c, value)
	if value == "" {
		setToast("Home page: " + brwStartPageLabel(c))
		return
	}
	setToast("Home page: " + brwHomeOptionLabel(value, brwBookmarkTitles(browserBookmarksList())))
}
