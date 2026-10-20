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
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	browserSceneName       = "Browser"
	browserContentVariable = "browser_content"
	browserHomeURL         = "https://github.com/jukaLang/JukaHubV2/issues"
	browserReposURL        = "https://github.com/jukaLang/JukaHubV2"
	browserIssuesURL       = "https://github.com/jukaLang/JukaHubV2/issues"
	browserNewIssueURL     = "https://github.com/jukaLang/JukaHubV2/issues/new"

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

	textBrowser := Element{
		Type:     "textbrowser",
		Text:     "web",
		Color:    "#0c0f16",
		X:        24,
		Y:        72,
		Width:    "1232",
		Height:   "496",
		Variable: browserContentVariable,
		Font:     "small",
	}
	buttons := []struct {
		label   string
		trigger string
	}{
		{"Back", "browser_back"},
		{"Reload", "browser_reload"},
		{"Repo", "browser_repo"},
		{"Issues", "browser_issues"},
		{"New Issue", "browser_new_issue"},
	}
	elements := []Element{textBrowser}
	for i, b := range buttons {
		elements = append(elements, Element{
			Type:    "button",
			Text:    b.label,
			Color:   "$labelColor",
			BgColor: "$buttonColor",
			X:       int32(24 + i*228),
			Y:       584,
			Width:   "220",
			Height:  "44",
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

func browserReload(config *Config) {
	elem, ok := findBrowserElement(config)
	if !ok {
		return
	}
	loadTextBrowserURLAt(config, elem, textBrowserAddress(config), false)
}

// browserEnsureHome loads the issue list the first time the Browser scene is
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
	browserHomeLoaded = false
	browserLinkPanel = browserLinkPanelState{}
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
		scheme := strings.ToLower(href)
		if strings.HasPrefix(scheme, "javascript:") || strings.HasPrefix(scheme, "mailto:") ||
			strings.HasPrefix(scheme, "tel:") || strings.HasPrefix(scheme, "data:") {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		parsed = base.ResolveReference(parsed)
		parsed.Scheme = strings.ToLower(parsed.Scheme)
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

	hint := "D-pad / mouse to choose · A or Enter opens"
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
