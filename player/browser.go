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

	"github.com/veandco/go-sdl2/sdl"
)

const (
	browserSceneName       = "Browser"
	browserContentVariable = "browser_content"
	browserReposURL        = "https://github.com/jukaLang/JukaHubV2"
	// The start page is the project repository. DuckDuckGo Lite draws its home
	// screen with JavaScript, so it rendered as a nearly blank panel; the
	// repository is server-rendered HTML and gives the browser a real page the
	// moment the tile is opened. Search is still one key away (Y) and still
	// goes through DuckDuckGo Lite.
	browserHomeURL     = browserReposURL
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
	}
	// The chips share the content width, computed from how many there are so
	// an added action never runs off the 1232px design column at 800x480.
	const (
		chipRowX = int32(24)
		chipRowW = int32(1232)
		chipGap  = int32(5)
	)
	chipW := (chipRowW - chipGap*int32(len(buttons)-1)) / int32(len(buttons))
	elements := []Element{textBrowser}
	for i, b := range buttons {
		elements = append(elements, Element{
			Type:    "button",
			Text:    b.label,
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			X:       scaleX(chipRowX+int32(i)*(chipW+chipGap), width),
			Y:       scaleY(584, height),
			Width:   StringOrInt(strconv.Itoa(int(scaleX(chipW, width)))),
			Height:  StringOrInt(strconv.Itoa(int(scaleY(44, height)))),
			Trigger: b.trigger,
			Font:    "small",
		})
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
	address, ok := browserHistoryStep(-1)
	if !ok {
		showToast("No previous page", ToastInfo())
		return
	}
	if elem, found := findBrowserElement(config); found {
		loadTextBrowserURLAt(config, elem, address, false)
	}
}

func browserForward(config *Config) {
	address, ok := browserHistoryStep(1)
	if !ok {
		showToast("No next page", ToastInfo())
		return
	}
	if elem, found := findBrowserElement(config); found {
		loadTextBrowserURLAt(config, elem, address, false)
	}
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
func browserEnsureHome(config *Config) {
	if config == nil || browserHomeLoaded {
		return
	}
	elem, ok := findBrowserElement(config)
	if !ok {
		return
	}
	content, _ := config.Variables.Custom[elem.Variable].(string)
	if strings.TrimSpace(content) != "" {
		browserHomeLoaded = true
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
	browserState.mu.Lock()
	browserState.links = map[string][]browserLink{}
	browserState.history = nil
	browserState.index = -1
	browserState.mu.Unlock()
	browserClearPageCache()
	browserHomeLoaded = false
	browserLinkPanel = browserLinkPanelState{}
	resetBrowserDocumentsForTest()
	resetBrowserAdBlockViewsForTest()
	resetBrowserAdBlockCount()
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
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	rowH := smallH*2 + 12
	headerH := 18 + medH + 4 + smallH + 12
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

	renderText(renderer, config, medium, "Links on this page", ColorTextPrimary(), panelX+22, panelY+16)
	renderText(renderer, config, font, fmt.Sprintf("%d link(s) · A opens · B closes", len(links)),
		ColorTextSecondary(), panelX+22, panelY+16+medH+2)

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
	loadTextBrowserURL(config, elem, target)
	return true
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
		if browserOpenFocusedPageLink(config, elem) {
			return true
		}
		handleTextBrowserAddressInput(renderer, config, elem)
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return browserMovePageLink(config, elem, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return browserMovePageLink(config, elem, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return browserScrollButton(config, elem, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return browserScrollButton(config, elem, 1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		browserScrollByPage(config, elem, -1)
		return true
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		browserScrollByPage(config, elem, 1)
		return true
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
