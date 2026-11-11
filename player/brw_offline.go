package main

// brw_offline.go — what the browser shows when a page does not arrive.
//
// A failed load used to be a few lines of plain text in the page area, and an
// offline one a resolver error once the 20-second timeout ran out. A failure
// is now structured (brwFailure) and drawn as a glass card in the page area:
//
//   - Offline (no connection, or IsOnline() already knows): "You're offline",
//     a big "Play JukaLand while you wait" button, Try again and Wi-Fi
//     settings. While the card is up the browser re-checks the connection
//     every few seconds and reloads the page by itself once it is back.
//   - Anything else (site not found, timed out, refused, certificate, an HTTP
//     error, not a web page) says what happened in a sentence, with Try again.
//
// JukaLand runs inside the page rectangle under the toolbar: the same world as
// the JukaLand screen. B or Esc returns to the card (and reloads, when the
// connection is back); a "Back online" pill appears over the game as soon as
// the connection returns.
//
// No socket-level error text ("dial tcp …: no such host") reaches the screen:
// brwClassifyError and brwFriendlyNetError turn every transport failure into a
// sentence, and the page text the plain fallback shows uses the same words.

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// brwFailKind classifies a load that produced no page.
type brwFailKind int

const (
	brwFailNone brwFailKind = iota
	brwFailOffline
	brwFailNotFound
	brwFailTimeout
	brwFailConnect
	brwFailTLS
	brwFailHTTP
	brwFailContent
	brwFailOther
	// brwFailWall is a search engine's wall instead of its results: Google's
	// "turn on JavaScript" page (browser_engines.go builds the card's words
	// and buttons).
	brwFailWall
)

// brwOfflineTitle and brwOfflineDetail are the offline card's words.
const (
	brwOfflineTitle  = "You're offline"
	brwOfflineDetail = "Can't connect to the internet. Connect to Wi-Fi and this page will load by itself."
)

// brwFailure is one failed load: what kind of failure, for which address, and
// the title and sentence the card shows.
type brwFailure struct {
	Kind   brwFailKind
	URL    string
	Host   string
	Title  string
	Detail string
	at     time.Time
}

// text is the page text a failure leaves behind: the "Could not load" line the
// reader has always printed, then the sentence.
func (f brwFailure) text() string {
	return "Could not load " + f.URL + "\n" + f.Detail
}

// brwFetchError carries a failure out of the fetch path.
type brwFetchError struct {
	fail brwFailure
}

func (e *brwFetchError) Error() string { return e.fail.text() }

// brwHostOf is the host of an address, or the address itself when it does not
// parse.
func brwHostOf(address string) string {
	if u, err := url.Parse(address); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return address
}

// brwNewFailure builds a failure with the card's words for its kind.
func brwNewFailure(kind brwFailKind, address, detail string) brwFailure {
	f := brwFailure{Kind: kind, URL: address, Host: brwHostOf(address)}
	switch kind {
	case brwFailOffline:
		f.Title, f.Detail = brwOfflineTitle, brwOfflineDetail
	case brwFailNotFound:
		f.Title = "Site not found"
		f.Detail = "Couldn't find " + f.Host + ". Check the address for typos, or search for it."
	case brwFailTimeout:
		f.Title = "This site is taking too long"
		f.Detail = "Timed out after " + textBrowserFetchTimeout.String() + ": " + f.Host + " didn't answer. Try again in a moment."
	case brwFailConnect:
		f.Title = "Can't reach " + f.Host
		f.Detail = detail
		if f.Detail == "" {
			f.Detail = "The site refused the connection or isn't running right now."
		}
	case brwFailTLS:
		f.Title = "Not a secure connection"
		f.Detail = f.Host + "'s security certificate isn't trusted, so the page wasn't opened."
	case brwFailHTTP:
		f.Title = "The page returned an error"
		f.Detail = detail
	case brwFailContent:
		f.Title = "Can't show this file"
		f.Detail = detail
	default:
		f.Title = "This page couldn't load"
		f.Detail = detail
		if f.Detail == "" {
			f.Detail = "Something went wrong while loading it."
		}
	}
	return f
}

// brwSocketWords are the fragments that mark an error as a socket-level detail
// nobody should be shown.
var brwSocketWords = []string{"dial ", "lookup ", "tcp", "udp", "read: ", "write: ", "wsarecv", "wsasend", "connectex"}

// brwFriendlyNetError turns any network error into one sentence, for the site
// info sheet and every other place a transport error could surface.
func brwFriendlyNetError(err error) string {
	if err == nil {
		return ""
	}
	if !IsOnline() {
		return "Needs internet — connect to Wi-Fi and try again."
	}
	lower := strings.ToLower(err.Error())
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, strings.Contains(lower, "no such host"):
		return "That site's name doesn't exist."
	case errors.As(err, &dnsErr):
		return "The internet's address book (DNS) didn't answer."
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return "The site didn't answer in time."
	case strings.Contains(lower, "refused"):
		return "The site refused the connection."
	case strings.Contains(lower, "reset"), strings.Contains(lower, "broken pipe"), strings.Contains(lower, "eof"):
		return "The connection was cut off."
	case strings.Contains(lower, "unreachable"), strings.Contains(lower, "no route"):
		return "No internet connection — connect to Wi-Fi and try again."
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return "The site's certificate isn't trusted."
	case strings.Contains(lower, "tls"):
		return "The secure connection failed."
	}
	inner := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		inner = urlErr.Err
	}
	line := firstLine(inner.Error())
	lowerLine := strings.ToLower(line)
	for _, word := range brwSocketWords {
		if strings.Contains(lowerLine, word) {
			return "The connection failed."
		}
	}
	return line
}

// brwClassifyError turns a failed request into a failure. timedOut says the
// load's own deadline ran out; online is the connectivity verdict (the
// monitor's, sharpened by a quick probe on a resolver failure).
func brwClassifyError(address string, err error, timedOut, online bool) brwFailure {
	lower := ""
	if err != nil {
		lower = strings.ToLower(err.Error())
	}
	var dnsErr *net.DNSError
	switch {
	case !online:
		return brwNewFailure(brwFailOffline, address, "")
	case timedOut:
		return brwNewFailure(brwFailTimeout, address, "")
	case strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "no route to host"):
		return brwNewFailure(brwFailOffline, address, "")
	case errors.As(err, &dnsErr) && !dnsErr.IsNotFound && (dnsErr.IsTimeout || dnsErr.IsTemporary):
		return brwNewFailure(brwFailOffline, address, "")
	case errors.As(err, &dnsErr), strings.Contains(lower, "no such host"):
		return brwNewFailure(brwFailNotFound, address, "")
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return brwNewFailure(brwFailTimeout, address, "")
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return brwNewFailure(brwFailTLS, address, "")
	case strings.Contains(lower, "refused"):
		return brwNewFailure(brwFailConnect, address, "The site refused the connection or isn't running right now.")
	case strings.Contains(lower, "reset"), strings.Contains(lower, "broken pipe"):
		return brwNewFailure(brwFailConnect, address, "The connection was cut off. Try again.")
	case strings.Contains(lower, "redirect"):
		f := brwNewFailure(brwFailOther, address, brwFriendlyNetError(err))
		f.Title = "Too many redirects"
		return f
	}
	return brwNewFailure(brwFailOther, address, brwFriendlyNetError(err))
}

// brwLooksLikeResolverFailure reports whether err is a name lookup failing,
// the error an offline device gives first (often as "no such host").
func brwLooksLikeResolverFailure(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such host") || strings.Contains(lower, "lookup ")
}

// brwProbeOnline is a quick look at whether the internet answers at all, used
// when a name lookup fails: Windows reports "no such host" both for a mistyped
// name and for no connection, and only a connection tells them apart. A
// variable so tests stay off the network.
var brwProbeOnline = func() bool {
	for _, addr := range []string{"1.1.1.1:443", "8.8.8.8:443"} {
		conn, err := net.DialTimeout("tcp", addr, 1500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// ── Failure store ────────────────────────────────────────────────────────

// brwFails holds the failure per page variable. The fetch goroutine writes it
// and the render pass reads it, so it is locked.
var brwFails = struct {
	mu    sync.Mutex
	byVar map[string]brwFailure
}{byVar: map[string]brwFailure{}}

func brwSetFailure(variable string, f *brwFailure) {
	if variable == "" {
		return
	}
	brwFails.mu.Lock()
	defer brwFails.mu.Unlock()
	if f == nil {
		delete(brwFails.byVar, variable)
		return
	}
	cp := *f
	cp.at = time.Now()
	brwFails.byVar[variable] = cp
}

func brwFailureFor(variable string) (brwFailure, bool) {
	brwFails.mu.Lock()
	defer brwFails.mu.Unlock()
	f, ok := brwFails.byVar[variable]
	return f, ok
}

// brwShowsOffline reports whether a failure is drawn as the offline card. A
// timeout or a refused connection counts once the monitor agrees the device
// is offline.
func brwShowsOffline(f brwFailure) bool {
	switch f.Kind {
	case brwFailOffline:
		return true
	case brwFailTimeout, brwFailConnect, brwFailNotFound:
		return !IsOnline()
	}
	return false
}

// ── Card ─────────────────────────────────────────────────────────────────

type brwCardAction int

const (
	brwActPlay brwCardAction = iota
	brwActRetry
	brwActWiFi
	brwActSearch
	// The search-wall card's (brwWallButtons): the same words on Startpage,
	// Startpage from now on, Settings → Browser, the desktop's own browser.
	brwActWallSearch
	brwActWallAlways
	brwActWallEngines
	brwActWallBrowser
	// WPE Browser on the handheld (brw_wpe.go): the wall's search opens in
	// it, or the install starts.
	brwActWpeOpen
	brwActWpeGet
)

type brwCardButton struct {
	action brwCardAction
	label  string
	icon   string
}

var (
	brwOfflineButtons = []brwCardButton{
		{action: brwActPlay, label: "Play JukaLand while you wait", icon: "gamepad"},
		{action: brwActRetry, label: "Try again", icon: "refresh"},
		{action: brwActWiFi, label: "Wi-Fi settings", icon: "wifi"},
	}
	brwNotFoundButtons = []brwCardButton{
		{action: brwActRetry, label: "Try again", icon: "refresh"},
		{action: brwActSearch, label: "Search the web", icon: "search"},
	}
	brwErrorButtons = []brwCardButton{
		{action: brwActRetry, label: "Try again", icon: "refresh"},
	}
)

func brwButtonsFor(f brwFailure, offline bool) []brwCardButton {
	switch {
	case offline:
		return brwOfflineButtons
	case f.Kind == brwFailNotFound:
		return brwNotFoundButtons
	case f.Kind == brwFailWall:
		// On the handheld WPE Browser goes first when it is installed, or
		// is offered when it is not (brw_wpe.go).
		return brwWpeWallButtons(brwWallButtons())
	}
	return brwErrorButtons
}

// brwCard is the card's focus, its drawn buttons and the reconnect bookkeeping
// (render thread only, except the probe flag).
var brwCard struct {
	focus     int
	count     int
	rects     [3]sdl.Rect
	rows      [3]int
	failAt    time.Time
	lastRetry uint64
	retries   int
	lastProbe uint64
	wrapText  string
	wrapW     int32
	wrapMax   int
	wrapFont  *ttf.Font
	wrapLines []string
}

// brwProbing is set while a connectivity probe the card started is running,
// and brwProbeDone is when the last one finished (Unix milliseconds).
var (
	brwProbing   atomic.Bool
	brwProbeDone atomic.Int64
)

// brwRefreshNetwork asks the connectivity monitor for a fresh verdict. A
// variable so tests never let a real probe change the shared monitor.
var brwRefreshNetwork = func() { netMon.update() }

// brwRetryBypass lets one load past the offline short-circuit: Try again has
// to try, even when the monitor still says offline.
var brwRetryBypass bool

// brwKickProbe re-checks the connection in the background at most every five
// seconds while the card or the game is up, so the page can reload as soon as
// Wi-Fi is back instead of on the monitor's next 20-second tick.
func brwKickProbe(now uint64) {
	if (brwCard.lastProbe != 0 && now-brwCard.lastProbe < 5000) || brwProbing.Load() {
		return
	}
	brwCard.lastProbe = now
	brwProbing.Store(true)
	refresh := brwRefreshNetwork
	go func() {
		defer brwProbing.Store(false)
		refresh()
		brwProbeDone.Store(time.Now().UnixMilli())
	}()
}

// brwRetry reloads the failed address for real.
func brwRetry(config *Config, elem Element, address string, quiet bool) {
	brwCard.lastRetry = sdl.GetTicks64()
	brwRetryBypass = true
	if !quiet {
		PlayActivateSound()
	}
	reloadTextBrowserURL(config, elem, address)
}

// brwCardActivate runs card button i.
func brwCardActivate(config *Config, elem Element, f brwFailure, i int) {
	buttons := brwButtonsFor(f, brwShowsOffline(f))
	if i < 0 || i >= len(buttons) {
		return
	}
	switch buttons[i].action {
	case brwActPlay:
		brwGameStart(elem)
	case brwActRetry:
		brwCard.retries = 0
		brwRetry(config, elem, f.URL, false)
	case brwActWiFi:
		PlayActivateSound()
		wifiScrOpen(config)
	case brwActSearch:
		PlayActivateSound()
		loadTextBrowserURL(config, elem, textBrowserSearchURL(f.Host))
	case brwActWallSearch, brwActWallAlways, brwActWallEngines, brwActWallBrowser:
		brwWallActivate(config, elem, f, buttons[i].action)
	case brwActWpeOpen, brwActWpeGet:
		brwWpeWallActivate(config, f, buttons[i].action)
	}
}

// brwCardMove walks the card's buttons. It reports false at an edge, so the
// press falls through to the browser and the D-pad can still reach the
// toolbar above and the chip row below.
func brwCardMove(dx, dy int) bool {
	n := brwCard.count
	if n <= 1 {
		return false
	}
	cur := brwCard.focus
	if cur < 0 || cur >= n {
		cur = 0
	}
	row := brwCard.rows[cur]
	next := -1
	switch {
	case dx != 0:
		for i := cur + dx; i >= 0 && i < n; i += dx {
			if brwCard.rows[i] == row {
				next = i
				break
			}
		}
	case dy != 0:
		for i := 0; i < n; i++ {
			if brwCard.rows[i] == row+dy {
				next = i
				break
			}
		}
	}
	if next < 0 {
		return false
	}
	brwCard.focus = next
	PlayNavSound()
	return true
}

// brwCardPad is the D-pad and A on the card. Everything else (B, Y search, the
// shoulders) keeps its browser meaning.
func brwCardPad(config *Config, elem Element, e *sdl.ControllerButtonEvent) bool {
	f, ok := brwFailureFor(elem.Variable)
	if !ok {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return brwCardMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return brwCardMove(1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return brwCardMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return brwCardMove(0, 1)
	case sdl.CONTROLLER_BUTTON_A:
		brwCardActivate(config, elem, f, brwCard.focus)
		return true
	}
	return false
}

// brwCardKey is the keyboard twin of brwCardPad.
func brwCardKey(config *Config, elem Element, e *sdl.KeyboardEvent) bool {
	f, ok := brwFailureFor(elem.Variable)
	if !ok {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		return brwCardMove(-1, 0)
	case sdl.K_RIGHT:
		return brwCardMove(1, 0)
	case sdl.K_UP:
		return brwCardMove(0, -1)
	case sdl.K_DOWN:
		return brwCardMove(0, 1)
	case sdl.K_TAB:
		if brwCard.count > 1 {
			brwCard.focus = (brwCard.focus + 1) % brwCard.count
			PlayNavSound()
		}
		return true
	case sdl.K_RETURN, sdl.K_SPACE:
		brwCardActivate(config, elem, f, brwCard.focus)
		return true
	case sdl.K_r, sdl.K_F5:
		brwCard.retries = 0
		brwRetry(config, elem, f.URL, false)
		return true
	}
	return false
}

// brwPageTap takes a click on the page area while the card or the game owns
// it.
func brwPageTap(config *Config, elem Element, mx, my int32) bool {
	if brwGame.active && brwGame.variable == elem.Variable {
		if brwGame.pill.W > 0 && pointInRect(mx, my, brwGame.pill) {
			brwGameStop(config, elem, true)
			return true
		}
		// A held mouse button is read by the game every frame; a tap (which
		// never shows in that state) is handed over here.
		return jlandTap(brwGame.view, mx, my)
	}
	f, ok := brwFailureFor(elem.Variable)
	if !ok {
		return false
	}
	for i := 0; i < brwCard.count; i++ {
		if pointInRect(mx, my, brwCard.rects[i]) {
			brwCard.focus = i
			brwCardActivate(config, elem, f, i)
			return true
		}
	}
	return false
}

// brwTickCard keeps the card honest: a new failure resets the focus, the
// connection is re-checked while offline, and an offline page reloads by
// itself once the connection is back (backing off if the load fails again).
func brwTickCard(config *Config, elem Element, f brwFailure) {
	now := sdl.GetTicks64()
	if !f.at.Equal(brwCard.failAt) {
		brwCard.failAt = f.at
		brwCard.focus = 0
	}
	if !IsOnline() {
		brwKickProbe(now)
		return
	}
	if f.Kind != brwFailOffline || time.Since(f.at) < time.Second {
		return
	}
	// The monitor says online, but this page failed for want of a connection:
	// reload only once a probe that finished after the failure agrees, so a
	// stale "online" cannot spin the card through reloads.
	if brwProbeDone.Load() < f.at.UnixMilli() {
		brwKickProbe(now)
		return
	}
	wait := uint64(3000) << uint(min(brwCard.retries, 4))
	if now-brwCard.lastRetry < wait {
		return
	}
	brwCard.retries++
	brwRetry(config, elem, f.URL, true)
}

// brwWrapped wraps a sentence for the card once per text and width; wrapping
// measures every word through FreeType, which is not a per-frame cost.
func brwWrapped(font *ttf.Font, text string, width int32, maxLines int) []string {
	c := &brwCard
	if c.wrapLines == nil || text != c.wrapText || width != c.wrapW || font != c.wrapFont || maxLines != c.wrapMax {
		c.wrapText, c.wrapW, c.wrapFont, c.wrapMax = text, width, font, maxLines
		c.wrapLines = wrapTextLines(font, text, width, maxLines)
	}
	return c.wrapLines
}

// brwDrawPageState draws the card or the game into the page area when either
// owns it, and reports whether it did (the caller then skips the page).
// strip is the cookie strip's height under the page, which the game takes too.
func brwDrawPageState(r *sdl.Renderer, config *Config, elem Element, page sdl.Rect, strip int32) bool {
	if brwGame.active && brwGame.variable == elem.Variable {
		if !brwBrowserFocused(config, elem) {
			brwGameEnd(config)
		} else {
			view := page
			view.H += strip
			brwGame.view = view
			jlandRT.noPointer = sdl.Rect{}
			jlandTick(config, view, false)
			jlandDraw(r, config, view, view.Y+view.H-10)
			brwDrawOnlinePill(r, config, view)
			return true
		}
	}
	f, ok := brwFailureFor(elem.Variable)
	if !ok {
		brwCard.count = 0
		return false
	}
	brwTickCard(config, elem, f)
	brwDrawCard(r, config, page, f)
	return true
}

// brwBrowserFocused reports whether the page element still holds the focus. A
// click on a chip or the toolbar moves it, and the game must not keep reading
// a pad the chip row now owns.
func brwBrowserFocused(config *Config, elem Element) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	elems := config.Scenes[currentSceneIndex].Elements
	if selectedButtonIndex < 0 || selectedButtonIndex >= len(elems) {
		return false
	}
	cur := elems[selectedButtonIndex]
	return cur.Type == "textbrowser" && cur.Variable == elem.Variable
}

// brwDrawCard paints the failure card, centred in the page area. A short page
// area (small panels) puts every button on one row.
func brwDrawCard(r *sdl.Renderer, config *Config, page sdl.Rect, f brwFailure) {
	if r == nil || config == nil || page.W <= 0 || page.H <= 0 {
		return
	}
	offline := brwShowsOffline(f)
	buttons := brwButtonsFor(f, offline)
	title, detail, icon := f.Title, f.Detail, "warning"
	switch {
	case offline:
		title, detail, icon = brwOfflineTitle, brwOfflineDetail, "wifi-off"
	case f.Kind == brwFailNotFound:
		icon = "search"
	case f.Kind == brwFailTLS:
		icon = "unlock"
	case f.Kind == brwFailTimeout:
		icon = "clock"
	case f.Kind == brwFailWall:
		icon = "search"
		// With WPE Browser first on the card, the way-out sentence names it.
		detail = brwWpeWallDetail(detail)
	}
	titleFont := FontForRole(config, TypeTitle)
	bodyFont := FontForRole(config, TypeBody)
	smallFont := FontForRole(config, TypeLabel)
	buttonFont := FontForRole(config, TypeBody)
	if titleFont == nil || bodyFont == nil || smallFont == nil || buttonFont == nil {
		return
	}

	compact := page.H < 330 || page.W < 520
	padX, padY, badgeSize := int32(28), int32(22), int32(56)
	primaryH, rowH, gap := int32(56), int32(48), int32(12)
	maxLines := 3
	if compact {
		padX, padY, badgeSize = 18, 14, 44
		primaryH, rowH, gap = 44, 44, 8
		maxLines = 2
	}
	cardW := min(page.W-32, int32(760))
	textW := cardW - 2*padX - badgeSize - 20
	lines := brwWrapped(bodyFont, detail, textW, maxLines)
	titleH, lineH, smallH := int32(titleFont.Height()), int32(bodyFont.Height()), int32(smallFont.Height())
	textH := titleH + 6 + int32(len(lines))*lineH + 6 + smallH
	headH := max(badgeSize, textH)
	// The wall card stacks too: its first button carries a longer label
	// ("Search on Startpage") than a third of the row holds.
	stacked := (offline || f.Kind == brwFailWall) && !compact && len(buttons) == 3
	buttonsH := rowH
	if stacked {
		buttonsH = primaryH + gap + rowH
	}
	statusH := int32(0)
	if offline {
		statusH = smallH + 14
	}
	cardH := padY + headH + 18 + buttonsH + statusH + padY
	card := sdl.Rect{X: page.X + (page.W-cardW)/2, Y: page.Y + (page.H-cardH)/2, W: cardW, H: cardH}
	if card.Y < page.Y+4 {
		card.Y = page.Y + 4
	}

	homeGlowBlob(r, sdl.Rect{X: card.X - 80, Y: card.Y - 80, W: card.W + 160, H: card.H + 160}, glassAccent(), 24)
	fillRoundedRect(r, card.X, card.Y, card.W, card.H, 22, glassSheetFill())
	strokeRoundedRect(r, card.X, card.Y, card.W, card.H, 22, 1, glassSheetEdge())
	drawOverlayCardAccent(r, card.X, card.Y, card.W)

	badge := sdl.Rect{X: card.X + padX, Y: card.Y + padY + (headH-badgeSize)/2, W: badgeSize, H: badgeSize}
	DrawHomeIcon(r, icon, drawGlassBadge(r, badge), false)
	tx := badge.X + badgeSize + 20
	ty := card.Y + padY + (headH-textH)/2
	renderText(r, config, titleFont, ellipsize(titleFont, title, textW), ColorTextPrimary(), tx, ty)
	ty += titleH + 6
	for _, line := range lines {
		renderText(r, config, bodyFont, line, ColorTextSecondary(), tx, ty)
		ty += lineH
	}
	ty += 6
	renderText(r, config, smallFont, ellipsize(smallFont, f.Host, textW), ColorTextTertiary(), tx, ty)

	// Buttons: the offline card stacks Play over Try again and Wi-Fi; every
	// other card (and a short page area) puts them on one row.
	if brwCard.focus >= len(buttons) {
		brwCard.focus = 0
	}
	brwCard.count = len(buttons)
	inner := cardW - 2*padX
	y := card.Y + padY + headH + 18
	for i, b := range buttons {
		var rect sdl.Rect
		row := 0
		switch {
		case stacked && i == 0:
			rect = sdl.Rect{X: card.X + padX, Y: y, W: inner, H: primaryH}
		case stacked:
			w := (inner - gap) / 2
			rect = sdl.Rect{X: card.X + padX + int32(i-1)*(w+gap), Y: y + primaryH + gap, W: w, H: rowH}
			row = 1
		default:
			n := int32(len(buttons))
			w := (inner - (n-1)*gap) / n
			rect = sdl.Rect{X: card.X + padX + int32(i)*(w+gap), Y: y, W: w, H: rowH}
		}
		brwCard.rects[i] = rect
		brwCard.rows[i] = row
		brwDrawCardButton(r, config, buttonFont, rect, b, i == 0, i == brwCard.focus)
	}

	if offline {
		sy := card.Y + card.H - padY - smallH
		status := "Checking for a connection…"
		if IsOnline() {
			status = "Connected — loading the page…"
		}
		pulse := 0.5 + 0.5*brwPulse(sdl.GetTicks64())
		dot := WithAlpha(ColorWarning, uint8(110+140*pulse))
		if IsOnline() {
			dot = WithAlpha(ColorSuccess, 255)
		}
		fillCircle(r, card.X+padX+5, sy+smallH/2, 5, dot)
		renderText(r, config, smallFont, status, ColorTextTertiary(), card.X+padX+18, sy)
	}
}

// brwPulse is a slow -1..1 wave for the status dot.
func brwPulse(now uint64) float64 {
	phase := float64(now%1400) / 1400
	if phase < 0.5 {
		return phase*4 - 1
	}
	return 3 - phase*4
}

// brwDrawCardButton paints one card button: the first is the accent pill, the
// rest are glass; the focused one wears Home's ring and bloom.
func brwDrawCardButton(r *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, b brwCardButton, primary, focused bool) {
	radius := rect.H / 2
	if focused {
		drawGlassFocusGlow(r, rect)
	}
	ink := ColorTextPrimary()
	if primary {
		accent := glassAccent()
		fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, radius, accent)
		ink = contrastText(accent)
		if focused {
			ring := glassLifted(rect)
			strokeRoundedRect(r, ring.X, ring.Y, ring.W, ring.H, radius+glassFocusLift, glassFocusRingWidth, glassA(235))
		}
	} else {
		drawGlassSurface(r, rect, radius, glassState{Focused: focused})
	}
	iconSize := min(rect.H-20, int32(22))
	label := ellipsize(font, b.label, rect.W-iconSize-44)
	lw, lh := measureText(font, label)
	x := rect.X + (rect.W-(iconSize+10+lw))/2
	DrawHomeIcon(r, b.icon, sdl.Rect{X: x, Y: rect.Y + (rect.H-iconSize)/2, W: iconSize, H: iconSize}, primary)
	renderText(r, config, font, label, ink, x+iconSize+10, rect.Y+(rect.H-lh)/2)
}

// ── JukaLand in the page ─────────────────────────────────────────────────

// brwGame is the embedded game: which page variable it plays in, its view
// (for clicks) and the "Back online" pill.
var brwGame struct {
	active   bool
	variable string
	view     sdl.Rect
	pill     sdl.Rect
	notified bool
}

// brwGameActive reports whether JukaLand is running inside the browser.
func brwGameActive() bool { return brwGame.active }

// brwGameStart opens JukaLand in the page area.
func brwGameStart(elem Element) {
	jlandSession()
	brwGame.active = true
	brwGame.variable = elem.Variable
	brwGame.pill = sdl.Rect{}
	brwGame.notified = IsOnline()
	// A fresh tick: the press that started the game must not also dig.
	jlandRT.lastTick = 0
	jlandRT.paused = false
	PlayActivateSound()
}

// brwGameEnd stops the game without a sound (a navigation or a focus change
// took the page away) and saves the world.
func brwGameEnd(config *Config) {
	if !brwGame.active {
		return
	}
	brwGame.active = false
	brwGame.pill = sdl.Rect{}
	jlandPersist(config)
}

// brwGameStop is B / Esc in the game: back to the card, or straight to the
// page when the connection is back.
func brwGameStop(config *Config, elem Element, reload bool) {
	if !brwGame.active {
		return
	}
	brwGameEnd(config)
	PlayBackSound()
	if f, ok := brwFailureFor(elem.Variable); ok && reload && IsOnline() {
		brwCard.retries = 0
		brwRetry(config, elem, f.URL, true)
	}
}

// brwGamePad forwards a pad press to the game; B leaves it.
func brwGamePad(config *Config, elem Element, e *sdl.ControllerButtonEvent) {
	if jlandHandlePad(config, e) == jlandWantsBack {
		brwGameStop(config, elem, true)
	}
}

// brwGameKey forwards a key press to the game; Esc leaves it.
func brwGameKey(config *Config, elem Element, e *sdl.KeyboardEvent) {
	if jlandHandleKey(config, e) == jlandWantsBack {
		brwGameStop(config, elem, true)
	}
}

// brwDrawOnlinePill tells a player the connection is back, without taking the
// game away: B (or a tap on the pill) goes back to the page.
func brwDrawOnlinePill(r *sdl.Renderer, config *Config, view sdl.Rect) {
	brwGame.pill = sdl.Rect{}
	if !IsOnline() {
		brwGame.notified = false
		brwKickProbe(sdl.GetTicks64())
		return
	}
	if !brwGame.notified {
		brwGame.notified = true
		PlaySuccessSound()
	}
	font := FontForRole(config, TypeBody)
	if font == nil {
		return
	}
	key := "B"
	if jlandRT.keyboard {
		key = "Esc"
	}
	label := "Back online — reload the page?"
	lw, lh := measureText(font, label)
	kw, _ := measureText(font, key)
	badgeW := max(kw+16, lh+6)
	h := lh + 16
	w := 14 + 10 + 10 + lw + 12 + badgeW + 8
	pill := sdl.Rect{X: view.X + (view.W-w)/2, Y: view.Y + 14, W: w, H: h}
	fillRoundedRect(r, pill.X, pill.Y, pill.W, pill.H, h/2, WithAlpha(ColorBackground, 225))
	strokeRoundedRect(r, pill.X, pill.Y, pill.W, pill.H, h/2, 1, WithAlpha(ColorSuccess, 200))
	fillCircle(r, pill.X+19, pill.Y+h/2, 5, ColorSuccess)
	renderText(r, config, font, label, ColorTextPrimary(), pill.X+34, pill.Y+(h-lh)/2)
	badge := sdl.Rect{X: pill.X + 34 + lw + 12, Y: pill.Y + 5, W: badgeW, H: h - 10}
	fillRoundedRect(r, badge.X, badge.Y, badge.W, badge.H, badge.H/2, glassChipFill())
	strokeRoundedRect(r, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, glassChipEdge())
	renderText(r, config, font, key, glassChipInk(), badge.X+(badge.W-kw)/2, pill.Y+(h-lh)/2)
	brwGame.pill = pill
}

// ── Footer, departure, reset ─────────────────────────────────────────────

var (
	brwCardHints = []FooterHint{
		{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"},
		{Button: "B", Label: "Back"}, {Button: "Y / /", Label: "Search"},
	}
	brwGamePadHints = []FooterHint{
		{Button: "D-Pad", Label: "Move"}, {Button: "A", Label: "Jump"}, {Button: "Y", Label: "Mine"},
		{Button: "X", Label: "Build"}, {Button: "L1/R1", Label: "Block"}, {Button: "B", Label: "Leave game"},
		{Button: "Up/Down", Label: "Aim"},
	}
	brwGameKeyHints = []FooterHint{
		{Button: "A/D", Label: "Move"}, {Button: "Space", Label: "Jump"}, {Button: "F", Label: "Mine"},
		{Button: "E", Label: "Build"}, {Button: "1-0", Label: "Block"}, {Button: "Esc", Label: "Leave game"},
		{Button: "Up/Down", Label: "Aim"},
	}
)

// brwFooterHints replaces the Browser's footer while the offline card or the
// game owns the page area.
func brwFooterHints() ([]FooterHint, bool) {
	var hints []FooterHint
	switch {
	case brwGame.active && jlandRT.keyboard:
		hints = brwGameKeyHints
	case brwGame.active:
		hints = brwGamePadHints
	default:
		if _, ok := brwFailureFor(browserContentVariable); !ok {
			return nil, false
		}
		hints = brwCardHints
	}
	// A copy: the footer trims what does not fit.
	return append([]FooterHint(nil), hints...), true
}

// brwSceneDeparture runs when any scene is left: the site-info sheet closes,
// the embedded game stops and saves, and a JukaLand world is saved.
func brwSceneDeparture(config *Config) {
	leaving := ""
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		leaving = config.Scenes[currentSceneIndex].Name
	}
	brwInfoForget()
	brwGameEnd(config)
	jlandSceneDeparture(config, leaving)
}
