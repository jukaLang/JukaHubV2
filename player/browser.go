package main

// browser.go — a small, controller-first web browser for the handheld.
//
// The device has no system browser, no X11 and no window manager, so a support
// link or a GitHub issue is otherwise unreachable. This browser turns the
// existing textbrowser element into a real (if deliberately simple) reading
// browser: an address bar, back/forward history, a link picker for anchors on
// the page, and quick links to the JukaHub repository and issue tracker.
//
// It is text-only on purpose. Rendering modern HTML/CSS/JS needs WebKit or
// Chromium, which the stock firmware does not carry. GitHub and most docs sites
// serve readable server-rendered HTML, which is enough for reading issues,
// release notes and documentation.

import (
	"fmt"
	htmlstd "html"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	browserSceneName       = "Browser"
	browserContentVariable = "browser_content"
	browserReposURL        = "https://github.com/jukaLang/JukaHubV2"
	// The start page is DuckDuckGo Lite so the tile opens where search happens.
	// The repo page is retired: lite.duckduckgo.com is server-rendered and
	// readable in the text browser, opening straight onto the search box.
	browserHomeURL     = "https://lite.duckduckgo.com/lite/"
	browserSearchURL   = "https://lite.duckduckgo.com/lite/?q="
	browserIssuesURL   = "https://github.com/jukaLang/JukaHubV2/issues"
	browserNewIssueURL = "https://github.com/jukaLang/JukaHubV2/issues/new"

	browserHistoryLimit = 40
	browserLinkLimit    = 80
)

type browserLink struct {
	Text string
	URL  string
}

// browserPage is one fetched page: reader text plus the anchors extracted from
// the same HTML. Keeping both in one result means the link picker can never
// show anchors from a previous page.
type browserPage struct {
	Text     string
	Links    []browserLink
	FinalURL string
	// Doc is the styled document the renderer draws instead of the plain text
	// extract (nil when the page had no readable markup).
	Doc *webDocument
	// Blocked is how many ad/tracker subresource requests the load skipped.
	Blocked int
}

var browserState = struct {
	mu      sync.Mutex
	links   map[string][]browserLink
	history []string
	index   int
}{links: map[string][]browserLink{}, index: -1}

var browserHomeLoaded bool

// ensureBrowserScene gives the browser a home in memory. The handheld config is
// deliberately pruned and older installs may not have a Browser scene at all,
// so the scene is injected exactly like the Alarm scene: the user's config file
// is never rewritten.
func ensureBrowserScene(config *Config) {
	if config == nil {
		return
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == browserSceneName {
			return
		}
	}

	// Scale the injected scene from the 1280x720 design so it fits the smaller
	// handheld panels too instead of running off the bottom of the screen.
	width, height := screenWidth, screenHeight
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 720
	}
	textBrowser := Element{
		Type:     "textbrowser",
		Text:     "web",
		Color:    "#0c0f16",
		X:        scaleX(24, width),
		Y:        scaleY(72, height),
		Width:    StringOrInt(strconv.Itoa(int(scaleX(1232, width)))),
		Height:   StringOrInt(strconv.Itoa(int(scaleY(496, height)))),
		Variable: browserContentVariable,
		Font:     "small",
	}
	buttons := []struct {
		label   string
		trigger string
	}{
		{"Back", "browser_back"},
		{"Forward", "browser_forward"},
		{"Reload", "browser_reload"},
		{"Links", "browser_links"},
		// Filter turns ad/tracker blocking off for the current site and
		// reloads it, which is the way back from a page that needs the very
		// request the filter drops.
		{"Filter", "browser_filter"},
		// Search is the controller shortcut to the web search box; without
		// it the address bar was the only way in, and that needed a mouse.
		{"Search", "browser_search"},
		{"Home", "browser_home"},
		{"Issues", "browser_issues"},
		// Sites lists the hosts the jar holds, one row each, so one site can be
		// signed out without losing every other login. It is also the controller
		// route into the picker a tap on the cookie strip opens.
		{"Sites", "browser_cookie_sites"},
		// The saved session lives on the card, so forgetting it needs a
		// controller path on this screen rather than a delete-a-file note in
		// the docs. The chip asks twice before it drops anything.
		{"Forget", "browser_forget_cookies"},
		// Save puts the page on screen on the saved list. A page sent in a
		// chat message, or a release-notes link, otherwise costs a full
		// retyping session on a D-pad to reach a second time.
		{"Save", "browser_bookmark_save"},
		// Marks opens that list. The chip carries the count, so a page saved
		// from a link is visible on the chip that opens it.
		{"Marks", "browser_bookmarks"},
		// Find searches the page already on screen. A long issue or release
		// page is otherwise a minute of D-pad paging to reach one paragraph.
		{"Find", "browser_find"},
	}
	// The chips share the content width, but not equally: each one is as wide
	// as its own word needs to be at this panel's size — measured with the font
	// the row is drawn in, then expressed in the 1232px design column the scene
	// is written in — and the column's surplus is shared out pro rata on top.
	// Equal shares sized the row by the number of actions alone, so once the row
	// grew past nine the widest label ("Forward") no longer fit its chip on any
	// panel, and a chip whose label is clipped is a control the user has to guess
	// at.
	const (
		chipRowX = int32(24)
		chipRowW = int32(1232)
		chipGap  = int32(5)
	)
	chipCount := int32(len(buttons))
	chipAvail := chipRowW - chipGap*(chipCount-1)
	chipFont, _ := getCachedFont(config, "small")
	chipWidths := make([]int32, chipCount)
	var chipDemand int32
	if chipFont == nil {
		// Nothing to measure with (a headless start): equal shares, which is what
		// the row used before it was sized by label.
		for i := range chipWidths {
			chipWidths[i] = chipAvail / chipCount
		}
	} else {
		for i, b := range buttons {
			labelW, _, err := chipFont.SizeUTF8(b.label)
			if err == nil {
				// Ceil, so the width this is expressed at never rounds the label
				// down below what the row gives it.
				need := (int32(labelW) + 2*SpaceXS) * HomeBaseWidth
				chipWidths[i] = (need + width - 1) / width
			}
			chipDemand += chipWidths[i]
		}
		switch {
		case chipDemand > 0 && chipDemand < chipAvail:
			extra := chipAvail - chipDemand
			for i := range chipWidths {
				chipWidths[i] += extra * chipWidths[i] / chipDemand
			}
		case chipDemand > chipAvail:
			// A theme or an accessibility size so large the row cannot hold its own
			// labels: shrink in proportion rather than run off the design column.
			for i := range chipWidths {
				chipWidths[i] = chipWidths[i] * chipAvail / chipDemand
			}
		}
	}
	elements := []Element{textBrowser}
	chipX := chipRowX
	for i, b := range buttons {
		elements = append(elements, Element{
			Type:    "button",
			Text:    b.label,
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			X:       scaleX(chipX, width),
			Y:       scaleY(584, height),
			Width:   StringOrInt(strconv.Itoa(int(scaleX(chipWidths[i], width)))),
			Height:  StringOrInt(strconv.Itoa(int(scaleY(44, height)))),
			Trigger: b.trigger,
			Font:    "small",
		})
		chipX += chipWidths[i] + chipGap
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        browserSceneName,
		Icon:        "@",
		Description: "Web browser with GitHub shortcuts",
		Background:  "#0B0F17",
		Elements:    elements,
	})
}

// findBrowserElement returns the first web textbrowser in the current scene,
// falling back to any scene so a trigger still works while a transition settles.
func findBrowserElement(config *Config) (Element, bool) {
	if config == nil || len(config.Scenes) == 0 {
		return Element{}, false
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, elem := range config.Scenes[currentSceneIndex].Elements {
			if elem.Type == "textbrowser" && textBrowserElementSource(elem) == textBrowserSourceWeb {
				return elem, true
			}
		}
	}
	for _, scene := range config.Scenes {
		for _, elem := range scene.Elements {
			if elem.Type == "textbrowser" && textBrowserElementSource(elem) == textBrowserSourceWeb {
				return elem, true
			}
		}
	}
	return Element{}, false
}

func findBrowserElementByVariable(config *Config, variable string) (Element, bool) {
	if config == nil || variable == "" {
		return Element{}, false
	}
	for _, scene := range config.Scenes {
		for _, elem := range scene.Elements {
			if elem.Type == "textbrowser" && elem.Variable == variable {
				return elem, true
			}
		}
	}
	return Element{}, false
}

// openBrowserHome opens the Browser scene on its start page. The scene is
// injected on demand, so a config that predates the browser still works.
func openBrowserHome(config *Config) {
	if config == nil {
		return
	}
	ensureBrowserScene(config)
	idx := sceneIndexExact(config, browserSceneName)
	if idx < 0 {
		showToast("The browser is not available on this build", ToastError())
		return
	}
	browserHomeLoaded = false
	config.Variables.Custom[textBrowserURLVariable] = browserHomeURL
	changeSceneTo(config, idx)
	if currentSceneIndex == idx {
		// Already on the Browser scene: no transition will refresh the page.
		if elem, ok := findBrowserElement(config); ok {
			browserHomeLoaded = true
			loadTextBrowserURL(config, elem, browserHomeURL)
		}
	}
}

// openBrowserAtURL opens the in-app browser on a specific page. Chat links use
// it, so a URL in a message is one click away from being read instead of only
// being copied out of the bubble.
func openBrowserAtURL(config *Config, rawURL string) bool {
	if config == nil {
		return false
	}
	address, err := normalizeTextBrowserURL(rawURL)
	if err != nil {
		showToast("Cannot open that link: "+err.Error(), ToastError())
		return false
	}
	ensureBrowserScene(config)
	idx := sceneIndexExact(config, browserSceneName)
	if idx < 0 {
		showToast("The browser is not available on this build", ToastError())
		return false
	}
	// Claim the start page so the scene's own refresh cannot replace the URL
	// the user asked for with the home page.
	browserHomeLoaded = true
	config.Variables.Custom[textBrowserURLVariable] = address
	config.Variables.Custom[browserContentVariable] = "Loading " + address + " ..."
	changeSceneTo(config, idx)
	if currentSceneIndex == idx {
		if elem, ok := findBrowserElement(config); ok {
			loadTextBrowserURL(config, elem, address)
		}
	}
	return true
}

// browserOpenURL loads a URL into the current browser element.
func browserOpenURL(config *Config, rawURL string) bool {
	elem, ok := findBrowserElement(config)
	if !ok {
		showToast("Browser is not available on this screen", ToastError())
		return false
	}
	loadTextBrowserURL(config, elem, rawURL)
	return true
}

func browserBack(config *Config) {
	// Look for the page to load into before stepping the history: the step
	// commits the cursor, so doing it first meant a press with no browser on
	// screen consumed an entry and the next press skipped a page.
	elem, found := findBrowserElement(config)
	if !found {
		return
	}
	address, ok := browserHistoryStep(-1)
	if !ok {
		showToast("No previous page", ToastInfo())
		return
	}
	loadTextBrowserURLAt(config, elem, address, false)
}

func browserForward(config *Config) {
	elem, found := findBrowserElement(config)
	if !found {
		return
	}
	address, ok := browserHistoryStep(1)
	if !ok {
		showToast("No next page", ToastInfo())
		return
	}
	loadTextBrowserURLAt(config, elem, address, false)
}

// browserHome loads the browser's start page, the same way a browser's home
// button does.
func browserHome(config *Config) {
	elem, ok := findBrowserElement(config)
	if !ok {
		return
	}
	loadTextBrowserURL(config, elem, browserHomeURL)
}

// browserToggleAdFilter flips ad/tracker filtering for the site on screen and
// reloads the page, so the recovery is one press instead of a settings hunt.
// The setting is per host, not per URL: an article and its comment page break
// or work together.
func browserToggleAdFilter(config *Config) bool {
	elem, ok := findBrowserElement(config)
	if !ok {
		showToast("Browser is not available on this screen", ToastError())
		return false
	}
	address := textBrowserAddress(config)
	site := browserAdBlockSiteKey(address)
	if site == "" {
		showToast("Open a page before turning the filter off", ToastInfo())
		return false
	}
	off := browserAdBlockOff(site)
	if err := setBrowserAdBlockOff(site, !off); err != nil {
		showToast("Cannot save the filter setting: "+err.Error(), ToastError())
		return false
	}
	setBrowserAdBlockView(elem.Variable, browserAdBlockView{Site: site, Off: !off})
	if off {
		ShowToast("Ad filtering on for "+site, ToastKindInfo)
	} else {
		ShowToast("Ad filtering off for "+site+" — reloading", ToastKindInfo)
	}
	PlayActivateSound()
	browserReload(config)
	return true
}

// browserForgetConfirm arms the Forget chip for one press. Dropping a session
// on a handheld means retyping a login on a D-pad, so the chip asks first and
// forgets on the second press inside browserForgetConfirmWindow — the same
// two-press rule the Wi-Fi picker uses before it forgets a saved network.
var browserForgetConfirm = struct {
	armedAt time.Time
}{}

// browserForgetCookiesAction is the Forget chip. The page is reloaded after the
// drop so what is on screen stops being the signed-in copy: the reload fetches
// the URL again, now without the session cookie.
func browserForgetCookiesAction(config *Config) bool {
	stored := browserCookiesCount()
	if stored == 0 {
		browserForgetConfirm.armedAt = time.Time{}
		ShowToast("No cookies are stored", ToastKindInfo)
		return false
	}
	if browserForgetConfirm.armedAt.IsZero() || time.Since(browserForgetConfirm.armedAt) > browserForgetConfirmWindow {
		browserForgetConfirm.armedAt = time.Now()
		PlayNavSound()
		ShowToast("Press Forget again to drop "+browserCookieCountLabel(stored), ToastKindWarning)
		return false
	}
	browserForgetConfirm.armedAt = time.Time{}
	dropped, err := browserForgetCookies()
	if err != nil {
		ShowToast("Cannot forget cookies: "+err.Error(), ToastKindError)
		return false
	}
	PlayActivateSound()
	ShowToast("Forgot "+browserCookieCountLabel(dropped)+" — reloading", ToastKindInfo)
	browserReloadIfOpen(config)
	return true
}

// browserReloadIfOpen reloads the page that is on screen, if there is one. A
// session that was just dropped is only visibly gone once the page is fetched
// again, but with an empty address the browser would fall back to its start page
// and fetch for nothing.
func browserReloadIfOpen(config *Config) {
	if config == nil {
		return
	}
	if address, _ := config.Variables.Custom[textBrowserURLVariable].(string); strings.TrimSpace(address) != "" {
		browserReload(config)
	}
}

// browserCookieCountLabel keeps the count readable in a toast: "1 cookie", not
// "1 cookie(s)".
func browserCookieCountLabel(n int) string {
	if n == 1 {
		return "1 cookie"
	}
	return strconv.Itoa(n) + " cookies"
}

// browserForgetChipLabel is the Forget chip's text: the saved session is worth
// seeing before you press anything, so the chip carries the count. Zero keeps
// the plain word, which reads as "nothing to drop" next to the press-time
// toast, and the count is capped so the widest label still fits the chip at the
// small font.
func browserForgetChipLabel(n int) string {
	switch {
	case n <= 0:
		return "Forget"
	case n > 99:
		return "Forget 99+"
	default:
		return "Forget " + strconv.Itoa(n)
	}
}

// browserCookieSitesShown is how many hosts the strip under the page names
// before it counts the rest. Three fits the panel at the small font on the
// 800px handheld, and the "+N more" tail keeps a busy jar from turning the
// line into a wall of domains.
const browserCookieSitesShown = 3

// browserCookieSitesLine is the line under the page: which sites the saved
// cookies belong to, so the count on the Forget chip is attached to a host
// instead of being a bare number. An empty jar says so rather than leaving the
// strip blank, which also tells the user the chip's plain "Forget" has nothing
// behind it.
func browserCookieSitesLine(sites []browserCookieSite) string {
	if len(sites) == 0 {
		return "No saved cookies"
	}
	names := make([]string, 0, browserCookieSitesShown+1)
	for i, site := range sites {
		if i == browserCookieSitesShown {
			names = append(names, "+"+strconv.Itoa(len(sites)-browserCookieSitesShown)+" more")
			break
		}
		names = append(names, site.Domain)
	}
	prefix := "Saved cookies: "
	if len(sites) == 1 {
		prefix = "Saved cookie: "
	}
	return prefix + strings.Join(names, ", ")
}

// browserSyncForgetChip rewrites the Forget chip with the live count. It runs
// once per frame while the Browser is on screen and writes the element only
// when the text differs, so an unchanged count costs one string compare. The
// rewrite is what makes the count visible to every route that can change it,
// rather than only to the press that acts on it.
func browserSyncForgetChip(config *Config) {
	if config == nil {
		return
	}
	label := browserForgetChipLabel(browserCookiesCount())
	for s := range config.Scenes {
		if config.Scenes[s].Name != browserSceneName {
			continue
		}
		for i := range config.Scenes[s].Elements {
			elem := &config.Scenes[s].Elements[i]
			if elem.Trigger == "browser_forget_cookies" && elem.Text != label {
				elem.Text = label
			}
		}
		return
	}
}

// browserBookmarkTitle is the label a saved page gets: the styled document's
// own <title> when the page had one, otherwise the host. A bookmark whose
// label is a bare URL is one nobody can pick out of a list.
func browserBookmarkTitle(config *Config, elem Element) string {
	if doc := browserDocumentFor(elem.Variable); doc != nil {
		if title := strings.TrimSpace(doc.Title); title != "" {
			return title
		}
	}
	return browserLinkHost(textBrowserAddress(config))
}

// browserBookmarkSaveAction is the Save chip: it puts the page on screen on
// the saved list. Saving a page that is already saved is not an error the user
// has to undo — it refreshes the title and says so, so pressing Save twice on
// something you mean to keep is harmless.
func browserBookmarkSaveAction(config *Config) bool {
	elem, ok := findBrowserElement(config)
	if !ok {
		showToast("Browser is not available on this screen", ToastError())
		return false
	}
	address := textBrowserAddress(config)
	if browserAdBlockSiteKey(address) == "" ||
		strings.TrimSpace(address) == "" ||
		strings.HasPrefix(strings.TrimSpace(address), "Enter a website") {
		showToast("Open a page before saving it", ToastInfo())
		return false
	}
	if browserBookmarkHas(address) {
		// Already saved: move it back to the top and confirm, rather than
		// silently duplicating it or refusing the press.
		browserBookmarkAdd(address, browserBookmarkTitle(config, elem))
		PlayActivateSound()
		ShowToast("Already in Saved pages — moved to the top", ToastKindInfo)
		return true
	}
	if !browserBookmarkAdd(address, browserBookmarkTitle(config, elem)) {
		showToast("Could not save this page", ToastError())
		return false
	}
	PlayActivateSound()
	ShowToast("Saved “"+browserBookmarkTitle(config, elem)+"”", ToastKindInfo)
	return true
}

// browserSyncBookmarkChips rewrites the Save and Marks chips with live state,
// the same way the Forget chip carries the cookie count. Save reads "Saved"
// when the page on screen is already on the list, so the chip answers "is this
// kept?" without a press; Marks carries the list's length, so a page saved
// from a link is visible on the chip that opens it. Both write only when the
// text differs, so an unchanged frame costs two string compares.
func browserSyncBookmarkChips(config *Config) {
	if config == nil {
		return
	}
	saveLabel := "Save"
	if _, ok := findBrowserElement(config); ok && browserBookmarkHas(textBrowserAddress(config)) {
		saveLabel = "Saved"
	}
	marksLabel := browserMarksChipLabel(browserBookmarksCount())
	for s := range config.Scenes {
		if config.Scenes[s].Name != browserSceneName {
			continue
		}
		for i := range config.Scenes[s].Elements {
			elem := &config.Scenes[s].Elements[i]
			switch elem.Trigger {
			case "browser_bookmark_save":
				if elem.Text != saveLabel {
					elem.Text = saveLabel
				}
			case "browser_bookmarks":
				if elem.Text != marksLabel {
					elem.Text = marksLabel
				}
			}
		}
		return
	}
}

// browserMarksChipLabel is the Marks chip's text: the saved-page count, so the
// list's size is visible before it is opened. Zero keeps the plain word, which
// reads as "nothing saved yet" next to the panel's own empty-state toast, and
// the count is capped so the widest label still fits its chip.
func browserMarksChipLabel(n int) string {
	switch {
	case n <= 0:
		return "Marks"
	case n > 99:
		return "Marks 99+"
	default:
		return "Marks " + strconv.Itoa(n)
	}
}

func browserReload(config *Config) {
	elem, ok := findBrowserElement(config)
	if !ok {
		return
	}
	address := textBrowserAddress(config)
	// Reload means "ask the server again": drop the cached copy so a page that
	// rendered stale or half-loaded is not served from memory. The key is the
	// normalized address, which is what the loader stored it under.
	forget := address
	if normalized, err := normalizeTextBrowserURL(address); err == nil {
		forget = normalized
	}
	browserForgetPage(forget)
	reloadTextBrowserURL(config, elem, address)
}

// browserEnsureHome loads the start page the first time the Browser scene is
// opened. Returning to an already-used browser keeps the current page.
//
// It used to skip the load when the content variable already held something,
// on the theory that a filled page was one this run had opened. It never was:
// the content and the address are saved with the rest of the config, so the
// check meant every launch came back to whatever the last run was reading — a
// news home page, in the report that prompted this — with a stale address bar
// and no request going out. The saved page is not a decision this session made.
// An explicit request still wins, because openBrowserAtURL and openBrowserHome
// claim the flag before the scene is shown.
func browserEnsureHome(config *Config) {
	if config == nil || browserHomeLoaded {
		return
	}
	elem, ok := findBrowserElement(config)
	if !ok {
		return
	}
	browserHomeLoaded = true
	config.Variables.Custom[textBrowserURLVariable] = browserHomeURL
	loadTextBrowserURLAt(config, elem, browserHomeURL, true)
}

// ── History ──────────────────────────────────────────────────────────────

func browserRecordURL(address string) {
	address = strings.TrimSpace(address)
	if address == "" {
		return
	}
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	if browserState.index >= 0 && browserState.index < len(browserState.history) &&
		browserState.history[browserState.index] == address {
		return
	}
	if browserState.index+1 < len(browserState.history) {
		browserState.history = browserState.history[:browserState.index+1]
	}
	browserState.history = append(browserState.history, address)
	if len(browserState.history) > browserHistoryLimit {
		drop := len(browserState.history) - browserHistoryLimit
		browserState.history = browserState.history[drop:]
	}
	browserState.index = len(browserState.history) - 1
}

func browserHistoryStep(delta int) (string, bool) {
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	next := browserState.index + delta
	if next < 0 || next >= len(browserState.history) {
		return "", false
	}
	browserState.index = next
	return browserState.history[next], true
}

func resetBrowserStateForTest() {
	// A load started by the test that just finished may still be on the wire;
	// join it before wiping the state it publishes into, so it cannot land in
	// the next test's browser.
	waitForBrowserFetches()
	browserState.mu.Lock()
	browserState.links = map[string][]browserLink{}
	browserState.history = nil
	browserState.index = -1
	browserState.mu.Unlock()
	browserClearPageCache()
	browserHomeLoaded = false
	browserLinkPanel = browserLinkPanelState{}
	browserCookiePanel = browserCookiePanelState{}
	browserBookmarkPanel = browserBookmarkPanelState{}
	browserBookmarkRowHits = nil
	browserBookmarkPanelRect = sdl.Rect{}
	browserCookieStripHit = sdl.Rect{}
	browserLinkCursor = browserLinkCursorState{}
	browserNav = browserModeScroll
	// Zoom is a reader preference, but a test that changes it must not size
	// the next test's layout differently.
	browserZoomReset()
	// An in-page search belongs to the page it was run against, so a new
	// browser session starts with no match list.
	browserFindReset()
	// Reading positions are per session; a fresh browser starts at the top.
	browserScrollReset()
	resetBrowserDocumentsForTest()
	resetBrowserAdBlockViewsForTest()
	resetBrowserAdBlockCount()
	// Cookies outlive a page inside one run on purpose, so a test has to say
	// when the session ends instead of inheriting the previous test's jar.
	browserResetCookies()
}

// ── Link extraction ──────────────────────────────────────────────────────

// extractBrowserLinks pulls anchors out of the raw HTML before the reader text
// strips them. Relative URLs are resolved against the final page URL, so a
// GitHub issue list can be followed without retyping addresses.
func extractBrowserLinks(source string, base *url.URL) []browserLink {
	if base == nil {
		return nil
	}
	lower := strings.ToLower(source)
	var links []browserLink
	seen := map[string]bool{}
	for pos := 0; pos < len(source) && len(links) < browserLinkLimit; {
		rel := strings.Index(lower[pos:], "<a")
		if rel < 0 {
			break
		}
		start := pos + rel
		after := start + 2
		if after < len(source) && source[after] != ' ' && source[after] != '\t' &&
			source[after] != '\r' && source[after] != '\n' && source[after] != '>' && source[after] != '/' {
			pos = after
			continue
		}
		end := findTextBrowserTagEnd(source, after)
		if end < 0 {
			break
		}
		tag := source[start+1 : end]
		href := htmlTagAttribute(tag, "href")
		closeRel := strings.Index(lower[end+1:], "</a>")
		label := ""
		if closeRel >= 0 {
			label = cleanTextBrowserHTML(stripBrowserTags(source[end+1 : end+1+closeRel]))
			pos = end + 1 + closeRel + len("</a>")
		} else {
			pos = end + 1
		}
		href = strings.TrimSpace(htmlstd.UnescapeString(href))
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		// No scheme blocklist here: url.Parse below decides, and only http and
		// https survive the allowlist. A HasPrefix list missed the spellings a
		// browser tolerates ("java\tscript:", leading whitespace, upper case in
		// the middle), which is exactly what a blocklist gets wrong.
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		parsed = base.ResolveReference(parsed)
		parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		if parsed.Hostname() == "" || parsed.User != nil {
			continue
		}
		parsed.Fragment = ""
		address := parsed.String()
		if seen[address] {
			continue
		}
		seen[address] = true
		label = strings.Join(strings.Fields(label), " ")
		if label == "" {
			label = parsed.Path
			if label == "" || label == "/" {
				label = parsed.Hostname()
			}
		}
		label = truncate(label, 100)
		links = append(links, browserLink{Text: label, URL: address})
	}
	return links
}

// stripBrowserTags removes markup nested inside an anchor label, e.g. the
// `<span>` GitHub wraps around parts of an issue title.
func stripBrowserTags(raw string) string {
	var out strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] != '<' {
			end := strings.IndexByte(raw[i:], '<')
			if end < 0 {
				end = len(raw)
			} else {
				end += i
			}
			out.WriteString(raw[i:end])
			i = end
			continue
		}
		end := findTextBrowserTagEnd(raw, i+1)
		if end < 0 {
			break
		}
		i = end + 1
	}
	return out.String()
}

// htmlTagAttribute reads one attribute from a tag body such as
// `a class="x" href='/issues/1'`. It tolerates single, double and unquoted
// values, which covers the HTML a small server-rendered site emits.
func htmlTagAttribute(tag, want string) string {
	i := 0
	for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\r' && tag[i] != '\n' {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\r' || tag[i] == '\n' || tag[i] == '/') {
			i++
		}
		start := i
		for i < len(tag) && tag[i] != '=' && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\r' && tag[i] != '\n' && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		name := strings.ToLower(tag[start:i])
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\r' || tag[i] == '\n') {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			if name == strings.ToLower(want) {
				return ""
			}
			continue
		}
		i++
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\r' || tag[i] == '\n') {
			i++
		}
		value := ""
		if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
			quote := tag[i]
			i++
			start = i
			for i < len(tag) && tag[i] != quote {
				i++
			}
			value = tag[start:i]
			if i < len(tag) {
				i++
			}
		} else {
			start = i
			for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\r' && tag[i] != '\n' && tag[i] != '>' {
				i++
			}
			value = tag[start:i]
		}
		if name == strings.ToLower(want) {
			return value
		}
	}
	return ""
}

func setBrowserLinks(variable string, links []browserLink) {
	if variable == "" {
		return
	}
	copied := append([]browserLink(nil), links...)
	browserState.mu.Lock()
	if browserState.links == nil {
		browserState.links = map[string][]browserLink{}
	}
	browserState.links[variable] = copied
	browserState.mu.Unlock()
}

func browserLinksFor(variable string) []browserLink {
	browserState.mu.Lock()
	defer browserState.mu.Unlock()
	links := browserState.links[variable]
	out := make([]browserLink, len(links))
	copy(out, links)
	return out
}

// ── Link picker overlay ──────────────────────────────────────────────────

type browserLinkPanelState struct {
	open     bool
	variable string
	cursor   int
	scroll   int
}

var browserLinkPanel browserLinkPanelState
var browserLinkRowHits []struct {
	index int
	rect  sdl.Rect
}
var browserLinkPanelRect sdl.Rect

func openBrowserLinkPanel(config *Config, elem Element) {
	if textBrowserElementSource(elem) != textBrowserSourceWeb {
		return
	}
	links := browserLinksFor(elem.Variable)
	if len(links) == 0 {
		showToast("No links on this page yet — reload or open a site", ToastInfo())
		return
	}
	browserLinkPanel = browserLinkPanelState{open: true, variable: elem.Variable}
	browserLinkRowHits = nil
	browserLinkPanelRect = sdl.Rect{}
	PlayActivateSound()
}

func closeBrowserLinkPanel() {
	if !browserLinkPanel.open {
		return
	}
	browserLinkPanel = browserLinkPanelState{}
	browserLinkRowHits = nil
	browserLinkPanelRect = sdl.Rect{}
	PlayBackSound()
}

func browserLinkPanelMove(delta int) {
	links := browserLinksFor(browserLinkPanel.variable)
	if len(links) == 0 {
		return
	}
	browserLinkPanel.cursor += delta
	if browserLinkPanel.cursor < 0 {
		browserLinkPanel.cursor = len(links) - 1
	}
	if browserLinkPanel.cursor >= len(links) {
		browserLinkPanel.cursor = 0
	}
	PlayNavSound()
}

func browserLinkPanelActivate(config *Config) {
	links := browserLinksFor(browserLinkPanel.variable)
	if browserLinkPanel.cursor < 0 || browserLinkPanel.cursor >= len(links) {
		return
	}
	link := links[browserLinkPanel.cursor]
	elem, ok := findBrowserElementByVariable(config, browserLinkPanel.variable)
	if !ok {
		showToast("This page is no longer open", ToastError())
		closeBrowserLinkPanel()
		return
	}
	closeBrowserLinkPanel()
	loadTextBrowserURL(config, elem, link.URL)
}

func ctrlBrowserLinks(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		browserLinkPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		browserLinkPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		browserLinkPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeBrowserLinkPanel()
	}
	return true
}

func handleBrowserLinksKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !browserLinkPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		browserLinkPanelMove(-1)
	case sdl.K_DOWN:
		browserLinkPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		browserLinkPanelActivate(config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		closeBrowserLinkPanel()
	}
	return true
}

func handleBrowserLinksMouse(mx, my int32, config *Config) bool {
	if !browserLinkPanel.open {
		return false
	}
	for _, hit := range browserLinkRowHits {
		if pointInRect(mx, my, hit.rect) {
			browserLinkPanel.cursor = hit.index
			browserLinkPanelActivate(config)
			return true
		}
	}
	if browserLinkPanelRect.W > 0 && pointInRect(mx, my, browserLinkPanelRect) {
		return true
	}
	closeBrowserLinkPanel()
	return true
}

func renderBrowserLinkPanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !browserLinkPanel.open {
		return
	}
	links := browserLinksFor(browserLinkPanel.variable)
	if len(links) == 0 {
		closeBrowserLinkPanel()
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	rowH := smallH*2 + 12
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height. Trailing 9
	// gives back the exact band this panel has always reserved.
	header := UIPanelHeader("Links on this page",
		fmt.Sprintf("%d link(s) · A opens · B closes", len(links)), "browser")
	header.SubtitleColor = ColorTextSecondary()
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + smallH

	panelW := int32(940)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 120 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	if int32(len(links)) < maxRows {
		maxRows = int32(len(links))
	}
	panelH := headerH + maxRows*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	browserLinkPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if browserLinkPanel.cursor < browserLinkPanel.scroll {
		browserLinkPanel.scroll = browserLinkPanel.cursor
	}
	if browserLinkPanel.cursor >= browserLinkPanel.scroll+int(maxRows) {
		browserLinkPanel.scroll = browserLinkPanel.cursor - int(maxRows) + 1
	}
	if browserLinkPanel.scroll > len(links)-int(maxRows) {
		browserLinkPanel.scroll = len(links) - int(maxRows)
	}
	if browserLinkPanel.scroll < 0 {
		browserLinkPanel.scroll = 0
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, 1, ColorBorderDefault)

	header.X = panelX + 22
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 44
	UIPageHeaderDraw(renderer, config, header)

	browserLinkRowHits = browserLinkRowHits[:0]
	y := panelY + headerH
	for i := browserLinkPanel.scroll; i < len(links) && i < browserLinkPanel.scroll+int(maxRows); i++ {
		row := sdl.Rect{X: panelX + 12, Y: y, W: panelW - 24, H: rowH - 4}
		if i == browserLinkPanel.cursor {
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 8, sdl.Color{R: 52, G: 66, B: 110, A: 230})
		}
		renderText(renderer, config, font, truncate(links[i].Text, 100), ColorTextPrimary(), row.X+12, row.Y+4)
		renderText(renderer, config, font, truncate(browserLinkHost(links[i].URL), 100), ColorTextSecondary(), row.X+12, row.Y+4+smallH)
		browserLinkRowHits = append(browserLinkRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: i, rect: row})
		y += rowH
	}

	hint := hintLine(FooterHint{"D-Pad / Mouse", "Choose"}, FooterHint{"A / Enter", "Open"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+22, panelY+panelH-footerH+8)
}

func browserLinkHost(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return address
	}
	host := parsed.Hostname()
	path := strings.TrimSuffix(parsed.Path, "/")
	if path != "" {
		return host + truncate(path, 70)
	}
	return host
}

// ── Page focus: links, search and the D-pad ──────────────────────────────
//
// The handheld has no pointer, and the browser used to be a one-way street:
// the D-pad scrolled the page and the only way to open an anchor was the X
// link list, while the address bar and the button row were mouse-only. The
// page now owns a link cursor (Left/Right walk the anchors in reading order,
// A opens the focused one, Y searches) and the D-pad steps out of the panel
// at either end, so every button on the scene is reachable with the pad.

const (
	// browserSearchVariable holds the query typed with the Search shortcut.
	// It is separate from the address so cancelling the keyboard leaves the
	// address bar, and the page on screen, untouched.
	browserSearchVariable = "browser_search_query"
	browserSearchPrompt   = "Search the web"

	// browserFormVariable holds the value typed into a form control on the
	// page. Its own buffer is what keeps the address bar and the web search
	// untouched while the keyboard is open for the page itself.
	browserFormVariable = "browser_form_value"
)

var (
	// browserFormEditing says the open keyboard belongs to a control on the
	// page rather than the address bar, the web search or a find field; its
	// text lands in browserFormVariable. browserFormSubmitted is set by the
	// keyboard when it is confirmed, so the field can send the form.
	browserFormEditing   bool
	browserFormSubmitted bool
)

type browserLinkCursorState struct {
	key    string // page identity, so the cursor resets on navigation
	cursor int    // index into the laid-out links, -1 when nothing is picked
}

var browserLinkCursor browserLinkCursorState

// browserPageLayout returns the laid-out page the renderer draws, or nil when
// the page has no styled document (a plain-text fallback).
func browserPageLayout(config *Config, elem Element) *webLayout {
	doc := browserDocumentFor(elem.Variable)
	if doc == nil {
		return nil
	}
	layout, _ := webDocumentRows(doc, config, elem, getElementWidth(elem, 1100)-14)
	return layout
}

// browserVisibleRows is how many page rows fit under the toolbar. It mirrors
// the renderer's measurement so the link cursor scrolls to the right place.
func browserVisibleRows(elem Element, layout *webLayout) int {
	if layout == nil || layout.RowH <= 0 {
		return 0
	}
	height := getElementHeight(elem, 480) - 28 - textBrowserURLBarHeight - 12
	rows := int(height / layout.RowH)
	if rows < 1 {
		rows = 1
	}
	return rows
}

// browserScrollMax is the last scroll row for the page: the point where the
// final line sits at the bottom of the viewport.
func browserScrollMax(elem Element, layout *webLayout) int32 {
	if layout == nil {
		return 0
	}
	extra := len(layout.Rows) - browserVisibleRows(elem, layout)
	if extra < 0 {
		extra = 0
	}
	return int32(extra)
}

// browserFocusedLink validates the stored link cursor against the page on
// screen and returns -1 when no link is picked. A new page resets it, so a
// cursor never points at a link from the previous page with the same index.
func browserFocusedLink(elem Element, layout *webLayout) int {
	if layout == nil || len(layout.Links) == 0 {
		browserLinkCursor = browserLinkCursorState{cursor: -1}
		return -1
	}
	doc := browserDocumentFor(elem.Variable)
	key := elem.Variable + "|" + strconv.Itoa(len(layout.Links))
	if doc != nil {
		key = doc.URL + "|" + key
	}
	if browserLinkCursor.key != key {
		browserLinkCursor = browserLinkCursorState{key: key, cursor: -1}
	}
	if browserLinkCursor.cursor < 0 || browserLinkCursor.cursor >= len(layout.Links) {
		return -1
	}
	return browserLinkCursor.cursor
}

// browserRevealLink scrolls the page just enough to show the focused link,
// moving it only when it would otherwise sit off screen.
func browserRevealLink(elem Element, layout *webLayout, hit webLinkHit) {
	max := browserScrollMax(elem, layout)
	if max <= 0 {
		textBrowserScrollY = 0
		return
	}
	visible := browserVisibleRows(elem, layout)
	scroll := int(textBrowserScrollY)
	if hit.Row < scroll {
		scroll = hit.Row
	} else if hit.Row >= scroll+visible {
		scroll = hit.Row - visible + 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if int32(scroll) > max {
		scroll = int(max)
	}
	textBrowserScrollY = int32(scroll)
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
}

// browserMovePageLink walks the page's links. With nothing picked, Right lands
// on the first link and Left on the last, so one press always does something.
func browserMovePageLink(config *Config, elem Element, delta int) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return false
	}
	if len(layout.Links) == 0 {
		showToast("No links on this page", ToastInfo())
		return true
	}
	cursor := browserFocusedLink(elem, layout)
	if cursor < 0 {
		if delta >= 0 {
			cursor = 0
		} else {
			cursor = len(layout.Links) - 1
		}
	} else {
		cursor = (cursor + delta + len(layout.Links)) % len(layout.Links)
	}
	browserLinkCursor.cursor = cursor
	browserRevealLink(elem, layout, layout.Links[cursor])
	PlayNavSound()
	return true
}

// browserLinkRowStep returns the index of the link on the next (dir > 0) or
// previous (dir < 0) visual row that is nearest to the current column. Rows
// wrap at the ends so the pad is never stuck on a page.
//
// Flat reading order is what Left/Right use, and it is wrong for Up/Down: on a
// two-column page a flat step leaves the cursor at the far side of the same
// row, which reads as the focus teleporting. This walks the geometry instead.
func browserLinkRowStep(links []webLinkHit, from, dir int) int {
	if len(links) == 0 {
		return -1
	}
	if from < 0 || from >= len(links) {
		if dir >= 0 {
			return 0
		}
		return len(links) - 1
	}
	cur := links[from]

	// The target row: the closest one in the requested direction, or the
	// opposite end of the page when there is none.
	var targetRow int
	found := false
	for _, l := range links {
		if dir > 0 && l.Row > cur.Row {
			if !found || l.Row < targetRow {
				targetRow, found = l.Row, true
			}
		} else if dir < 0 && l.Row < cur.Row {
			if !found || l.Row > targetRow {
				targetRow, found = l.Row, true
			}
		}
	}
	if !found {
		for _, l := range links {
			if dir > 0 {
				if !found || l.Row < targetRow {
					targetRow, found = l.Row, true
				}
			} else if !found || l.Row > targetRow {
				targetRow, found = l.Row, true
			}
		}
	}
	if !found || targetRow == cur.Row {
		// A one-row page has nothing above or below it.
		return -1
	}

	best := -1
	bestDist := int64(0)
	for i, l := range links {
		if l.Row != targetRow {
			continue
		}
		d := int64(l.X) - int64(cur.X)
		if d < 0 {
			d = -d
		}
		if best < 0 || d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// browserStepPageLinkRow is Up/Down in link mode: the nearest link on the
// adjacent visual row, revealed and announced like any other step.
func browserStepPageLinkRow(config *Config, elem Element, dir int) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return false
	}
	if len(layout.Links) == 0 {
		showToast("No links on this page", ToastInfo())
		return true
	}
	cursor := browserFocusedLink(elem, layout)
	next := browserLinkRowStep(layout.Links, cursor, dir)
	if next < 0 {
		return true
	}
	browserLinkCursor.cursor = next
	browserRevealLink(elem, layout, layout.Links[next])
	PlayNavSound()
	return true
}

// browserResolveLink turns a raw href into an absolute http(s) address. Only
// http and https survive: the same allowlist the link picker uses, so a
// javascript: or file: href in a page cannot be followed from the pad.
func browserResolveLink(baseURL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	target := base.ResolveReference(ref)
	target.Scheme = strings.ToLower(strings.TrimSpace(target.Scheme))
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", false
	}
	if target.Hostname() == "" || target.User != nil {
		return "", false
	}
	target.Fragment = ""
	return target.String(), true
}

// browserOpenFocusedPageLink follows the link under the cursor. It reports
// false when no link is picked, which leaves A on its old meaning (edit the
// address bar).
func browserOpenFocusedPageLink(config *Config, elem Element) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return false
	}
	cursor := browserFocusedLink(elem, layout)
	if cursor < 0 {
		return false
	}
	base := textBrowserAddress(config)
	if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
		base = doc.URL
	}
	target, ok := browserResolveLink(base, layout.Links[cursor].Href)
	if !ok {
		showToast("That link cannot be opened here", ToastInfo())
		return true
	}
	browserLinkCursor = browserLinkCursorState{}
	// A play card is activated, not navigated: the URL streams through
	// yt-dlp inside the embedded player, exactly like a search result.
	if layout.Links[cursor].Video {
		PlayActivateSound()
		playSmartURL(config, target)
		return true
	}
	loadTextBrowserURL(config, elem, target)
	return true
}

// ── Scroll ⇄ Link mode ─────────────────────────────────────────────────
//
// One D-pad, two jobs: scrolling the page and stepping through its links.
// A cycle key switches between them so the same pad does both, the way a
// TV remote alternates between arrow keys and colored buttons. On the
// Trimui Smart Pro the pad arrives as keyboard events (browserKey below),
// so Tab is the cycle and the mode is one shared piece of state.

type browserNavMode int

const (
	browserModeScroll browserNavMode = iota
	browserModeLinks
)

var browserNav = browserModeScroll

func browserToggleNavMode() {
	if browserNav == browserModeLinks {
		browserNav = browserModeScroll
		showToast("Scroll mode — D-Pad scrolls (Tab / L3)", ToastInfo())
	} else {
		browserNav = browserModeLinks
		showToast("Link mode — D-Pad steps through links (Tab / L3)", ToastInfo())
	}
	PlayNavSound()
}

// browserEnsureLinkFocus focuses the first link when nothing is picked, so
// entering link mode and pressing A always does something. It reports
// whether a link can be followed.
func browserEnsureLinkFocus(config *Config, elem Element) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil || len(layout.Links) == 0 {
		showToast("No links on this page", ToastInfo())
		return false
	}
	if browserFocusedLink(elem, layout) < 0 {
		browserLinkCursor.cursor = 0
		browserRevealLink(elem, layout, layout.Links[0])
		PlayNavSound()
	}
	return true
}

// browserActivateFocusedLink is what A/Enter do: in link mode it always
// follows a link (focusing the first if none is set), in scroll mode it
// keeps the old behaviour of opening the address bar when nothing is on.
func browserActivateFocusedLink(renderer *sdl.Renderer, config *Config, elem Element) bool {
	if browserNav == browserModeLinks && browserEnsureLinkFocus(config, elem) {
		return browserActivatePageLink(renderer, config, elem)
	}
	return browserActivatePageLink(renderer, config, elem)
}

// browserActivatePageLink opens whatever the cursor is on: a form control does
// what its own kind says, a link navigates. It reports false when the cursor is
// on nothing, so A still falls through to the address bar.
func browserActivatePageLink(renderer *sdl.Renderer, config *Config, elem Element) bool {
	if ctrl, ok := browserFocusedPageControl(config, elem); ok {
		browserActivatePageControl(renderer, config, elem, ctrl)
		return true
	}
	return browserOpenFocusedPageLink(config, elem)
}

// browserActivatePageControl is what pressing A on a control does, and it
// depends on what the control is:
//
//   - a text field or a textarea asks for a value and sends the form
//   - a select steps to its next item in place (a handheld cannot open the
//     author's dropdown, and a stepping control needs no keyboard)
//   - a tick box toggles in place
//   - a submit button sends the form with whatever its fields hold, and when
//     they hold nothing it opens the first field instead of sending an empty
//     search
func browserActivatePageControl(renderer *sdl.Renderer, config *Config, elem Element, ctrl *webControl) {
	if config == nil || ctrl == nil {
		return
	}
	switch ctrl.Kind {
	case "select":
		if n := len(ctrl.Options); n > 1 {
			ctrl.Choice = (ctrl.Choice + 1) % n
		} else if n == 0 {
			showToast("This list has no choices", ToastInfo())
			return
		}
		browserRefreshFormState(elem)
		PlayActivateSound()
		return
	case "check", "radio":
		ctrl.Checked = !ctrl.Checked
		if ctrl.Kind == "radio" && ctrl.Checked {
			// A radio group keeps one choice, so picking this one clears the
			// rest of the group the way the page's own script would.
			for _, other := range browserFormControls(elem) {
				if other != ctrl && other.Kind == "radio" &&
					other.Form.ID == ctrl.Form.ID &&
					other.Name != "" && other.Name == ctrl.Name {
					other.Checked = false
				}
			}
		}
		browserRefreshFormState(elem)
		PlayActivateSound()
		return
	case "submit":
		if browserSubmitFormValues(config, elem, ctrl, "", true) {
			return
		}
		// Nothing filled in yet: the useful press is on the field itself.
		if field := browserFormFirstField(elem, ctrl.Form); field != nil {
			browserFillPageControl(renderer, config, elem, field)
			return
		}
		showToast("Fill in a field first", ToastInfo())
		return
	}
	browserFillPageControl(renderer, config, elem, ctrl)
}

// browserRefreshFormState re-measures the page after a control changed in
// place, so the field, list or box draws its new state on the next frame.
func browserRefreshFormState(elem Element) {
	if doc := browserDocumentFor(elem.Variable); doc != nil {
		doc.invalidateFormState()
	}
}

// browserFormControls walks the page for every form control it drew. The reader
// has no DOM, so the document's runs are the index: each control's state lives
// on the control itself, which is what makes a submit able to collect fields it
// never touched.
func browserFormControls(elem Element) []*webControl {
	doc := browserDocumentFor(elem.Variable)
	if doc == nil {
		return nil
	}
	var out []*webControl
	seen := map[*webControl]bool{}
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			ctrl := run.Style.Control
			if ctrl == nil || seen[ctrl] {
				continue
			}
			seen[ctrl] = true
			out = append(out, ctrl)
		}
	}
	return out
}

// browserFormFirstField is the field a submit button should open when the form
// is empty: the first text control in the same form, which is the one a user
// would have typed into.
func browserFormFirstField(elem Element, form webForm) *webControl {
	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind != "text" && ctrl.Kind != "textarea" {
			continue
		}
		if ctrl.Form.ID == form.ID {
			return ctrl
		}
	}
	return nil
}

// browserFocusedPageControl returns the form control under the cursor, if the
// cursor is on one rather than on a link.
func browserFocusedPageControl(config *Config, elem Element) (*webControl, bool) {
	layout := browserPageLayout(config, elem)
	if layout == nil || layout.RowH <= 0 || len(layout.Links) == 0 {
		return nil, false
	}
	cursor := browserFocusedLink(elem, layout)
	if cursor < 0 || cursor >= len(layout.Links) {
		return nil, false
	}
	if ctrl := layout.Links[cursor].Control; ctrl != nil {
		return ctrl, true
	}
	return nil, false
}

// browserFillPageControl opens the keyboard for a page control and submits what
// was typed. The field opens holding the value the page shipped, so editing a
// prefilled box keeps what is already there.
func browserFillPageControl(renderer *sdl.Renderer, config *Config, elem Element, ctrl *webControl) {
	if config == nil || ctrl == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	index := -1
	for i, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "textbrowser" && e.Variable == elem.Variable {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[browserFormVariable] = ctrl.Value
	browserFormEditing = true
	browserFormSubmitted = false
	vkOpenSceneInput(config, currentSceneIndex, index)
	if strings.TrimSpace(ctrl.Value) != "" {
		// A field that opened holding the page's value starts selected, the way
		// the address bar does: the first character typed replaces it, so
		// correcting a prefilled search box is one press, not a delete-and-hold.
		vkTextSelected = true
	}
	runInputModal(renderer, config)
	browserFormEditing = false
	activeSceneIndex = -1
	activeElementIndex = -1
	if browserFormSubmitted {
		value, _ := config.Variables.Custom[browserFormVariable].(string)
		browserSubmitPageControl(config, elem, ctrl, value)
	}
	browserFormSubmitted = false
}

// browserSubmitPageControl is the text path: the field that was just filled is
// the one being submitted.
func browserSubmitPageControl(config *Config, elem Element, ctrl *webControl, value string) {
	if strings.TrimSpace(value) == "" {
		showToast("Nothing typed", ToastInfo())
		return
	}
	browserSubmitFormValues(config, elem, ctrl, value, false)
}

// browserSubmitFormValues turns a form into a page load. The reader cannot
// POST, so a form is submitted as a GET: the action plus every field the page
// put in that form. That is the shape a search box accepts (DuckDuckGo Lite,
// site search, wiki search), which is what a handheld actually needs from a
// form, and collecting the whole form rather than one field is what makes a
// page with a text box plus a <select> filter work off one press.
//
// typed is the value the keyboard just produced for the focused control, when
// there is one; it is written back onto the control so the field keeps showing
// it. button marks a submit-button press: the button's own name and value go
// along the way a browser sends them.
func browserSubmitFormValues(config *Config, elem Element, ctrl *webControl, typed string, button bool) bool {
	if config == nil || ctrl == nil {
		return false
	}
	if typed = strings.TrimSpace(typed); typed != "" {
		ctrl.Value = typed
	}
	form := ctrl.Form
	values := url.Values{}
	// The page's own hidden inputs go first: they are the constants the site's
	// handler expects (`ie=utf-8`, a site key, a locale). A visible field with
	// the same name overwrites one below, because what the user typed is what
	// they meant.
	if doc := browserDocumentFor(elem.Variable); doc != nil {
		for _, hidden := range doc.FormHidden[form.ID] {
			if hidden.Name != "" {
				values.Add(hidden.Name, hidden.Value)
			}
		}
	}
	for _, other := range browserFormControls(elem) {
		// The form's own ID, not its action: a page with two search boxes
		// (site search and web search, say) can point both at one address, and
		// an action comparison sent the other box's fields along with this one.
		if other.Form.ID != form.ID {
			continue
		}
		name := strings.TrimSpace(other.Name)
		if name == "" {
			continue
		}
		switch other.Kind {
		case "text", "textarea":
			if v := strings.TrimSpace(other.Value); v != "" {
				values.Set(name, v)
			}
		case "select":
			values.Set(name, other.submitValue())
		case "check", "radio":
			if other.Checked {
				v := strings.TrimSpace(other.Value)
				if v == "" {
					v = "on"
				}
				values.Set(name, v)
			}
		}
	}
	if button {
		if name := strings.TrimSpace(ctrl.Name); name != "" {
			values.Add(name, strings.TrimSpace(ctrl.Value))
		}
	} else if values.Get(strings.TrimSpace(ctrl.Name)) == "" && typed != "" {
		// A field with no name of its own still has to send something: the form
		// named its first field, and that is the box the page expects.
		name := strings.TrimSpace(ctrl.Name)
		if name == "" {
			name = strings.TrimSpace(form.Field)
		}
		if name == "" {
			name = "q"
		}
		values.Set(name, typed)
	}
	if len(values) == 0 {
		return false
	}

	base := textBrowserAddress(config)
	if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
		base = doc.URL
	}
	address := base
	if action := strings.TrimSpace(form.Action); action != "" {
		if resolved, ok := browserResolveLink(base, action); ok {
			address = resolved
		} else if parsed, err := url.Parse(action); err == nil && parsed.IsAbs() {
			address = action
		}
	}
	// The action may already carry a query (site search URLs often do), so the
	// fields are appended rather than replacing it.
	sep := "?"
	if strings.Contains(address, "?") {
		sep = "&"
	}
	address += sep + values.Encode()
	PlayActivateSound()
	loadTextBrowserURL(config, elem, address)
	return true
}

// submitValue is the value a select sends: the chosen option's own value when
// the markup gave one, otherwise its label.
func (c *webControl) submitValue() string {
	if c == nil || c.Choice < 0 || c.Choice >= len(c.Options) {
		return ""
	}
	if c.Choice < len(c.OptionValues) {
		if v := strings.TrimSpace(c.OptionValues[c.Choice]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(c.Options[c.Choice])
}

// handleBrowserKey mirrors handleBrowserController for the keyboard — which
// is how the Trimui Smart Pro reports its D-pad and face buttons. Tab cycles
// between scrolling the page and stepping through its links; in link mode the
// D-pad walks the anchors and A opens the focused one. Keys outside the
// browser's model return false so older handlers keep working.

func handleBrowserKey(renderer *sdl.Renderer, e *sdl.KeyboardEvent, config *Config, elem Element) bool {
	if e == nil || e.Type != sdl.KEYDOWN || config == nil ||
		textBrowserElementSource(elem) != textBrowserSourceWeb {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_TAB:
		browserToggleNavMode()
		return true
	case sdl.K_UP:
		if browserNav == browserModeLinks {
			return browserStepPageLinkRow(config, elem, -1)
		}
		textBrowserScrollVelocity -= textBrowserScrollAccel
		if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
			textBrowserScrollVelocity = -textBrowserScrollMaxVel
		}
		textBrowserScrollCooldown = 8
		return true
	case sdl.K_DOWN:
		if browserNav == browserModeLinks {
			return browserStepPageLinkRow(config, elem, 1)
		}
		textBrowserScrollVelocity += textBrowserScrollAccel
		if textBrowserScrollVelocity > textBrowserScrollMaxVel {
			textBrowserScrollVelocity = textBrowserScrollMaxVel
		}
		textBrowserScrollCooldown = 8
		return true
	case sdl.K_LEFT:
		return browserMovePageLink(config, elem, -1)
	case sdl.K_RIGHT:
		return browserMovePageLink(config, elem, 1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		if browserActivateFocusedLink(renderer, config, elem) {
			return true
		}
		if e.Keysym.Sym != sdl.K_RETURN {
			return false
		}
		handleTextBrowserAddressInput(renderer, config, elem)
		return true
	case sdl.K_l, sdl.K_x:
		openBrowserLinkPanel(config, elem)
		return true
	case sdl.K_f:
		// Find on this page: its own keyboard, so the address bar and the
		// web search are both left alone.
		openBrowserFind(renderer, config, elem)
		return true
	case sdl.K_n:
		// N steps to the next match; with no search running the key belongs
		// to nothing, so it falls through instead of swallowing a press.
		return browserFindStep(config, elem, 1)
	case sdl.K_p:
		return browserFindStep(config, elem, -1)
	case sdl.K_MINUS, sdl.K_UNDERSCORE:
		// Zoom the reader text out. A page-scoped size, kept out of the
		// global a11y setting so the rest of the launcher is untouched.
		return browserZoomStep(config, elem, -1)
	case sdl.K_EQUALS, sdl.K_PLUS:
		// '+' is Shift+= on most layouts, so both spellings zoom in.
		return browserZoomStep(config, elem, 1)
	case sdl.K_PAGEUP:
		browserScrollByPage(config, elem, -1)
		return true
	case sdl.K_PAGEDOWN:
		browserScrollByPage(config, elem, 1)
		return true
	case sdl.K_HOME:
		textBrowserScrollY = 0
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
		return true
	case sdl.K_END:
		layout := browserPageLayout(config, elem)
		textBrowserScrollY = browserScrollMax(elem, layout)
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
		return true
	case sdl.K_ESCAPE, sdl.K_b:
		// A running find owns Escape first: leaving the search is what the
		// reader is asking for, and going back a scene is the press after.
		if browserFind.active {
			browserFindClear()
			return true
		}
		if browserNav == browserModeLinks {
			browserNav = browserModeScroll
			showToast("Scroll mode — D-Pad scrolls the page", ToastInfo())
			PlayNavSound()
			return true
		}
		goBackScene(config)
		return true
	}
	return false
}

// browserScrollButton scrolls one line and, at either end of the page, hands
// the D-pad to the scene: Up leaves to the chrome above, Down to the button
// row below. That is what makes the browser's own buttons reachable from the
// page without a mouse.
func browserScrollButton(config *Config, elem Element, dir int) bool {
	layout := browserPageLayout(config, elem)
	max := browserScrollMax(elem, layout)
	if dir < 0 {
		if textBrowserScrollY <= 0 {
			textBrowserScrollVelocity = 0
			textBrowserScrollCooldown = 0
			moveSelection(config, ActionNavigateUp)
			return true
		}
		textBrowserScrollVelocity -= textBrowserScrollAccel
		if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
			textBrowserScrollVelocity = -textBrowserScrollMaxVel
		}
		textBrowserScrollCooldown = 8
		return true
	}
	if max <= 0 || textBrowserScrollY >= max {
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
		moveSelection(config, ActionNavigateDown)
		return true
	}
	textBrowserScrollVelocity += textBrowserScrollAccel
	if textBrowserScrollVelocity > textBrowserScrollMaxVel {
		textBrowserScrollVelocity = textBrowserScrollMaxVel
	}
	textBrowserScrollCooldown = 8
	return true
}

// browserScrollByPage is the L1/R1 jump, clamped to the page.
func browserScrollByPage(config *Config, elem Element, dir int) {
	layout := browserPageLayout(config, elem)
	max := browserScrollMax(elem, layout)
	step := int32(browserVisibleRows(elem, layout) - 1)
	if step < 1 {
		step = 1
	}
	textBrowserScrollY += int32(dir) * step
	if textBrowserScrollY < 0 {
		textBrowserScrollY = 0
	}
	if textBrowserScrollY > max {
		textBrowserScrollY = max
	}
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
	PlayNavSound()
}

// openBrowserSearch is the Search shortcut: it opens the keyboard for a query
// and searches for it, without typing over the address bar. A query that is
// itself an address opens it, exactly like the address field.
func openBrowserSearch(renderer *sdl.Renderer, config *Config, elem Element) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	for i, current := range config.Scenes[currentSceneIndex].Elements {
		if current.Type != "textbrowser" || current.Variable != elem.Variable {
			continue
		}
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[browserSearchVariable] = ""
		textBrowserSearchEditing = true
		textBrowserSearchSubmitted = false
		vkOpenSceneInput(config, currentSceneIndex, i)
		runInputModal(renderer, config)
		textBrowserSearchEditing = false
		activeSceneIndex = -1
		activeElementIndex = -1
		if textBrowserSearchSubmitted {
			if query, ok := config.Variables.Custom[browserSearchVariable].(string); ok {
				if q := strings.TrimSpace(query); q != "" {
					loadTextBrowserURL(config, current, q)
				}
			}
		}
		textBrowserSearchSubmitted = false
		return
	}
}

// handleBrowserController gives the browser its own D-pad model. It returns
// false for buttons it does not own, so the scene's generic handling still
// runs (Y already means something else outside the browser).
func handleBrowserController(renderer *sdl.Renderer, e *sdl.ControllerButtonEvent, config *Config, elem Element) bool {
	if e == nil || config == nil || textBrowserElementSource(elem) != textBrowserSourceWeb {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
		return true
	case sdl.CONTROLLER_BUTTON_X:
		openBrowserLinkPanel(config, elem)
		return true
	case sdl.CONTROLLER_BUTTON_Y:
		openBrowserSearch(renderer, config, elem)
		return true
	case sdl.CONTROLLER_BUTTON_A:
		if browserActivateFocusedLink(renderer, config, elem) {
			return true
		}
		handleTextBrowserAddressInput(renderer, config, elem)
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return browserMovePageLink(config, elem, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return browserMovePageLink(config, elem, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if browserNav == browserModeLinks {
			return browserStepPageLinkRow(config, elem, -1)
		}
		return browserScrollButton(config, elem, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if browserNav == browserModeLinks {
			return browserStepPageLinkRow(config, elem, 1)
		}
		return browserScrollButton(config, elem, 1)
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		// Firmware that reports a real gamepad has no Tab key; the stick
		// click is the same cycle, and the toast names both keys.
		browserToggleNavMode()
		return true
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		browserScrollByPage(config, elem, -1)
		return true
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		browserScrollByPage(config, elem, 1)
		return true
	}
	return false
}

// ── Clicking links on the page ───────────────────────────────────────

// browserPageContentBox mirrors renderTextBrowser's geometry for the styled
// page: the toolbar, the focus outline and this hit-test share the same
// numbers, so a tap lands on exactly the row the renderer drew.
func browserPageContentBox(elem Element) (left, top, width, height int32) {
	width = getElementWidth(elem, 1100) - 14
	headerH := int32(28)
	height = getElementHeight(elem, 480) - headerH - textBrowserURLBarHeight - 12
	left = elem.X + 7
	top = elem.Y + 8 + headerH + textBrowserURLBarHeight + 4
	return left, top, width, height
}

// browserPageLinkRects maps the laid-out links into screen coordinates as the
// renderer draws them at the current scroll. Off-screen links map too — the
// caller decides visibility — so tests can assert where a row landed.
func browserPageLinkRects(config *Config, elem Element, layout *webLayout) []sdl.Rect {
	if layout == nil || layout.RowH <= 0 {
		return nil
	}
	left, top, _, _ := browserPageContentBox(elem)
	scroll := int(textBrowserScrollY)
	if scroll > int(browserScrollMax(elem, layout)) {
		scroll = int(browserScrollMax(elem, layout))
	}
	rects := make([]sdl.Rect, len(layout.Links))
	for i, hit := range layout.Links {
		y := top + int32(hit.Row-scroll)*layout.RowH - 2
		rects[i] = sdl.Rect{
			X: left + hit.X - 4,
			Y: y,
			W: hit.W + 8,
			H: layout.RowH + 4,
		}
	}
	return rects
}

// browserTapPageVideo leaves a tap on one of the page's play cards. It
// fails when the tap is not on one, so the caller can keep the click for
// links and the address bar. A video tap never navigates: the URL goes to
// playSmartURL, which streams media files and hands the rest to yt-dlp.
func browserTapPageVideo(config *Config, elem Element, mx, my int32) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil || len(layout.Videos) == 0 || layout.RowH <= 0 {
		return false
	}
	left, top, width, height := browserPageContentBox(elem)
	if mx < left || mx >= left+width || my < top || my >= top+height {
		return false
	}
	scroll := int(textBrowserScrollY)
	if scroll > int(browserScrollMax(elem, layout)) {
		scroll = int(browserScrollMax(elem, layout))
	}
	scrollPx := int32(scroll) * layout.RowH
	doc := browserDocumentFor(elem.Variable)
	for _, v := range layout.Videos {
		x := left + v.Rect.X
		y := top + v.Rect.Y - scrollPx
		if y >= top+height || y+v.Rect.H <= top {
			continue // off screen
		}
		if !pointInRect(mx, my, sdl.Rect{X: x, Y: y, W: v.Rect.W, H: v.Rect.H}) {
			continue
		}
		base := textBrowserAddress(config)
		if doc != nil && strings.TrimSpace(doc.URL) != "" {
			base = doc.URL
		}
		target, ok := browserResolveLink(base, v.Src)
		if !ok {
			showToast("That video cannot be played here", ToastInfo())
			return true
		}
		PlayActivateSound()
		playSmartURL(config, target)
		return true
	}
	return false
}

// browserTapPageLink leaves a tap inside the rendered page on one of its
// links: it fails when the tap is not on one, so the caller can keep the
// click for whatever else wants it.
func browserTapPageLink(renderer *sdl.Renderer, config *Config, elem Element, mx, my int32) bool {
	layout := browserPageLayout(config, elem)
	if layout == nil || len(layout.Links) == 0 {
		return false
	}
	// Play cards first: their boxes are drawn where the video sits and a
	// tap cannot land on both.
	if browserTapPageVideo(config, elem, mx, my) {
		return true
	}
	left, top, width, height := browserPageContentBox(elem)
	if mx < left || mx >= left+width || my < top || my >= top+height {
		return false
	}
	for i, rect := range browserPageLinkRects(config, elem, layout) {
		rel := int32(layout.Links[i].Row - int(textBrowserScrollY))
		if rel < 0 || rel*layout.RowH >= height {
			continue // off screen; the outline cannot be there either
		}
		if rect.W > 0 && pointInRect(mx, my, rect) {
			// A form control is a tap target like a link, and a tap does what its
			// kind says: a field opens the keyboard, a select steps, a box ticks.
			if ctrl := layout.Links[i].Control; ctrl != nil {
				PlayActivateSound()
				browserActivatePageControl(renderer, config, elem, ctrl)
				return true
			}
			base := textBrowserAddress(config)
			if doc := browserDocumentFor(elem.Variable); doc != nil && strings.TrimSpace(doc.URL) != "" {
				base = doc.URL
			}
			target, ok := browserResolveLink(base, layout.Links[i].Href)
			if !ok {
				showToast("That link cannot be opened here", ToastInfo())
				return true
			}
			PlayActivateSound()
			browserLinkCursor = browserLinkCursorState{}
			loadTextBrowserURL(config, elem, target)
			return true
		}
	}
	return false
}

// browserDrawPageLinkFocus outlines the link the D-pad is on. Rows are document
// rows and the page is drawn scrolled, so both are shifted the same way.
func browserDrawPageLinkFocus(renderer *sdl.Renderer, elem Element, layout *webLayout, scroll int, contentTop, contentH int32) {
	if renderer == nil || layout == nil || layout.RowH <= 0 || contentH <= 0 {
		return
	}
	cursor := browserFocusedLink(elem, layout)
	if cursor < 0 {
		return
	}
	hit := layout.Links[cursor]
	rel := hit.Row - scroll
	if rel < 0 || int32(rel)*layout.RowH >= contentH {
		return
	}
	left := elem.X + 7
	right := left + getElementWidth(elem, 1100) - 14
	x := left + hit.X - 4
	w := hit.W + 8
	if x < left {
		w -= left - x
		x = left
	}
	if x+w > right {
		w = right - x
	}
	if w < 8 {
		return
	}
	y := contentTop + int32(rel)*layout.RowH - 2
	fillRoundedRect(renderer, x, y, w, layout.RowH, 4, WithAlpha(accentColor, 48))
	strokeRoundedRect(renderer, x, y, w, layout.RowH, 4, 2, WithAlpha(accentColor, 235))
}
