package main

// mail_ui.go — the Mail app (docs/EMAIL_CALENDAR.md).
//
// One full-screen scene in Home's glass with three views:
//
//   - the list: account chips along the top (All inboxes and each account,
//     L1/R1 or Tab), then the newest messages — an unread dot, the sender,
//     the date, the subject and a line of the text. A opens a message; Select
//     opens the menu (Compose, Sync now, Mark all read, Email settings);
//   - the reader: the headers, the wrapped text (D-pad and the stick scroll,
//     L1/R1 page) and the attachments, which the menu saves to Downloads.
//     Y replies, X moves the message to Trash, the menu also archives it or
//     marks it unread;
//   - compose (mail_compose.go).
//
// The screen only reads snapshots: the caches belong to mail_sync.go and
// every server action runs on a goroutine whose answer comes back through
// tbxPost. Dates are written as dates ("15:04", "Yesterday", "Mon", "Jan 2"),
// never as "3 min ago": the handheld may not know the time yet, and a date
// from before its clock was set is still the date the sender wrote.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const mailSceneName = "Mail"

// mailView is which of the three views is up.
type mailView int

const (
	mailViewList mailView = iota
	mailViewRead
	mailViewCompose
)

// mailRow is one listed message with the account it belongs to.
type mailRow struct {
	acct     mailcalAccount
	validity uint32
	msg      mailCacheMsg
}

// id is the row's focus id.
func (r mailRow) id() string {
	return "m:" + r.acct.ID + ":" + strconv.FormatUint(uint64(r.msg.UID), 10)
}

// mailMenuItem is one entry of the Select menu.
type mailMenuItem struct {
	label string
	icon  string
	run   func()
	// raw draws label as written: an attachment's name is the sender's
	// text, not an interface string to translate or fill $variables into.
	raw bool
}

// mailUI is the screen's state. Main thread only.
var mailUI struct {
	visit tbxVisit
	view  mailView
	// account is the chip on: "" is All inboxes.
	account string

	listNav    tbxNav
	readNav    tbxNav
	composeNav tbxNav
	// nav is the focus list sign-in rows report to (the list's).
	nav *tbxNav

	builtGen uint64
	builtAt  time.Time
	accounts []mailcalAccount
	rows     []mailRow
	errors   []mailcalAccount // accounts with a MailError to show
	chips    []sdl.Rect
	// unread and neverSynced describe the accounts on the chip.
	unread      int
	neverSynced bool

	// The reader.
	open       mailRow
	openGen    int
	loading    bool
	entry      *mailBodyEntry
	openErr    string
	readText   string
	readWrap   tbxWrapCache
	readHTML   bool
	showImages bool
	// htmlDoc and htmlLayout are the browser engine's view of the current
	// HTML body: parsed and laid out once per width, then drawn each frame.
	htmlDoc     *webDocument
	htmlLayout  *webLayout
	htmlWidth   int32
	htmlBody    sdl.Rect
	htmlHeaderH int32

	compose mailCompose

	menuOpen  bool
	menuItems []mailMenuItem
	menuFocus int
	// menuFirst is the first item drawn: a message with many attachments
	// makes a menu taller than the screen, which then scrolls with the focus.
	menuFirst int
	menuRects []sdl.Rect
	menuCard  sdl.Rect

	// busy names a running action ("" = none), for the header.
	busy string
}

type mailScene struct{ sceneBase }

func init() {
	mailUI.nav = &mailUI.listNav
	registerScene(mailScene{sceneBase{name: mailSceneName}})
}

// mailOpenApp opens the Mail screen on its list. Main thread.
func mailOpenApp() {
	mailStart()
	msOAuthLoad(appConfig)
	if mailUI.view == mailViewRead {
		mailUI.view = mailViewList
	}
	mailUI.menuOpen = false
	mailUI.builtGen = 0
	tbxOpenScene(appConfig, mailSceneName)
	if s := mailSyncedAgo(); s < 0 || s > 2*time.Minute {
		mailSyncNow("opened")
	}
}

// mailSyncedAgo is how long ago the newest account sync finished (-1:
// never).
func mailSyncedAgo() time.Duration {
	newest := time.Time{}
	for _, a := range mailcalSettingsGet().Accounts {
		if t := mailCacheGet(a.ID).SyncedAt; t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return -1
	}
	return time.Since(newest)
}

func (mailScene) render(r *sdl.Renderer, c *Config) bool {
	if mailUI.visit.begin() {
		mailUI.menuOpen = false
		mailUI.builtGen = 0
	}
	switch mailUI.view {
	case mailViewRead:
		mailRenderReader(r, c)
	case mailViewCompose:
		mailRenderCompose(r, c)
	default:
		mailRenderList(r, c)
	}
	if mailUI.menuOpen {
		mailRenderMenu(r, c)
	}
	return true
}

func (mailScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if mailUI.menuOpen {
		return mailMenuAction(tbxKeyAction(e))
	}
	if !tbxSheetVisible(c) {
		ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
		switch sym := e.Keysym.Sym; {
		case sym == sdl.K_m || sym == sdl.K_APPLICATION || sym == sdl.K_MENU:
			mailOpenMenu(c)
			return true
		case sym == sdl.K_TAB && mailUI.view == mailViewList:
			step := 1
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				step = -1
			}
			mailCycleAccount(step)
			return true
		case (sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER) && ctrl && mailUI.view == mailViewCompose:
			mailComposeSend(c)
			return true
		case sym == sdl.K_r && mailUI.view == mailViewRead:
			mailReplyOpen(c, false)
			return true
		case sym == sdl.K_h && mailUI.view == mailViewRead && mailHTMLAvailable():
			mailSetReadHTML(!mailUI.readHTML)
			return true
		case sym == sdl.K_i && mailUI.view == mailViewRead && mailUI.readHTML:
			mailSetShowImages(!mailUI.showImages)
			return true
		}
	}
	return tbxDispatch(c, tbxKeyAction(e), mailSceneAction)
}

func (mailScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if mailUI.menuOpen {
		if e.Button == sdl.CONTROLLER_BUTTON_BACK {
			mailUI.menuOpen = false
			PlayBackSound()
			return true
		}
		return mailMenuAction(tbxPadAction(e))
	}
	if !tbxSheetVisible(c) {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_BACK:
			// Select is the menu here, the way it is in the Browser.
			mailOpenMenu(c)
			return true
		case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
			step := -1
			if e.Button == sdl.CONTROLLER_BUTTON_RIGHTSHOULDER {
				step = 1
			}
			// With one account there are no chips, and the shoulders page
			// the list as on every other screen.
			if mailUI.view == mailViewList && len(mailUI.accounts) > 1 {
				mailCycleAccount(step)
				return true
			}
		}
	}
	return tbxDispatch(c, tbxPadAction(e), mailSceneAction)
}

// mailSceneAction routes one action to the view.
func mailSceneAction(config *Config, act tbxAction) bool {
	switch mailUI.view {
	case mailViewRead:
		return mailReaderAction(config, act)
	case mailViewCompose:
		return mailComposeAction(config, act)
	}
	return mailListAction(config, act)
}

// mailActiveNav is the focus list of the view on screen (wheel scrolling).
func mailActiveNav() *tbxNav {
	switch mailUI.view {
	case mailViewRead:
		return &mailUI.readNav
	case mailViewCompose:
		return &mailUI.composeNav
	}
	return &mailUI.listNav
}

// mailSceneClick routes a click.
func mailSceneClick(mx, my int32, config *Config) bool {
	if mailUI.menuOpen {
		for i, r := range mailUI.menuRects {
			if pointInRect(mx, my, r) {
				mailUI.menuFocus = mailUI.menuFirst + i
				mailMenuAction(tbxActConfirm)
				return true
			}
		}
		if !pointInRect(mx, my, mailUI.menuCard) {
			mailUI.menuOpen = false
			PlayBackSound()
		}
		return true
	}
	if mailUI.view == mailViewList {
		for i, r := range mailUI.chips {
			if pointInRect(mx, my, r) {
				mailSetAccount(mailChipID(i))
				return true
			}
		}
	}
	if mailUI.view == mailViewRead && mailUI.readHTML && mailHTMLTapLink(config, mx, my) {
		return true
	}
	nav := mailActiveNav()
	it, ok := nav.hit(mx, my)
	if !ok {
		return false
	}
	nav.focus = it.id
	return mailSceneAction(config, tbxActConfirm)
}

// mailSceneHints is the footer.
func mailSceneHints() []FooterHint {
	if mailUI.menuOpen {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	}
	menu := FooterHint{Button: "Select / M", Label: "Menu"}
	switch mailUI.view {
	case mailViewRead:
		hints := []FooterHint{{Button: "D-Pad", Label: "Scroll"}}
		if mailHTMLAvailable() {
			label := "HTML"
			if mailUI.readHTML {
				label = "Text"
			}
			hints = append(hints, FooterHint{Button: "H", Label: label})
			if mailUI.readHTML && !mailUI.showImages {
				hints = append(hints, FooterHint{Button: "I", Label: "Images"})
			}
		}
		return append(hints, FooterHint{Button: "Y", Label: "Reply"}, FooterHint{Button: "X", Label: "Delete"}, menu, FooterHint{Button: "B", Label: "Back"})
	case mailViewCompose:
		return mailComposeHints()
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if len(mailUI.accounts) > 1 {
		hints = append(hints, FooterHint{Button: "L1/R1 / Tab", Label: "Inbox"})
	}
	label := "Open"
	f := mailUI.listNav.focus
	if _, ok := mailFind(f); ok {
		// X on a message moves it to Trash; the footer says so, since it
		// acts at once.
		hints = append(hints, FooterHint{Button: "X", Label: "Delete"})
	}
	switch {
	case f == "ms-qr":
		label = "Enlarge"
	case f == "ms-cancel":
		label = "Cancel"
	case strings.HasPrefix(f, "err:"), f == "settings":
		label = "Settings"
	case f == "setup-phone":
		label = "Set up"
	}
	return append(hints, FooterHint{Button: "A", Label: label}, menu, FooterHint{Button: "B", Label: "Back"})
}

// ── Model ───────────────────────────────────────────────────────────────

// mailChipID is chip i's account ("" for All inboxes).
func mailChipID(i int) string {
	if i <= 0 || i > len(mailUI.accounts) {
		return ""
	}
	return mailUI.accounts[i-1].ID
}

// mailSetAccount switches the chip.
func mailSetAccount(id string) {
	if mailUI.account == id {
		return
	}
	PlayNavSound()
	mailUI.account = id
	mailUI.listNav.focus, mailUI.listNav.scroll = "", 0
	mailUI.builtGen = 0
}

// mailCycleAccount steps the chips.
func mailCycleAccount(step int) {
	n := len(mailUI.accounts) + 1
	if n <= 1 {
		return
	}
	cur := 0
	for i := range mailUI.accounts {
		if mailUI.accounts[i].ID == mailUI.account {
			cur = i + 1
		}
	}
	mailSetAccount(mailChipID(((cur+step)%n + n) % n))
}

// mailBuild rebuilds the list when a cache changed (or every few seconds,
// for the store's error lines).
func mailBuild() {
	gen := mailGen.Load()
	if gen == mailUI.builtGen && time.Since(mailUI.builtAt) < 3*time.Second {
		return
	}
	mailUI.builtGen, mailUI.builtAt = gen, time.Now()
	s := mailcalSettingsGet()
	mailUI.accounts = mailUI.accounts[:0]
	mailUI.errors = mailUI.errors[:0]
	for _, a := range s.Accounts {
		if a.MailOff {
			continue
		}
		mailUI.accounts = append(mailUI.accounts, a)
	}
	found := mailUI.account == ""
	for _, a := range mailUI.accounts {
		found = found || a.ID == mailUI.account
	}
	if !found {
		mailUI.account = ""
	}
	mailUI.rows = mailUI.rows[:0]
	mailUI.unread, mailUI.neverSynced = 0, true
	for _, a := range mailUI.accounts {
		if mailUI.account != "" && a.ID != mailUI.account {
			continue
		}
		if a.MailError != "" {
			mailUI.errors = append(mailUI.errors, a)
		}
		c := mailCacheGet(a.ID)
		mailUI.unread += c.Unread
		if !c.SyncedAt.IsZero() {
			mailUI.neverSynced = false
		}
		for _, m := range c.Messages {
			mailUI.rows = append(mailUI.rows, mailRow{acct: a, validity: c.Validity, msg: m})
		}
	}
	if mailUI.account == "" && len(mailUI.accounts) > 1 {
		sort.SliceStable(mailUI.rows, func(i, j int) bool { return mailUI.rows[i].msg.Date.After(mailUI.rows[j].msg.Date) })
	}
}

// mailFind is the listed row with this focus id.
func mailFind(id string) (mailRow, bool) {
	for _, r := range mailUI.rows {
		if r.id() == id {
			return r, true
		}
	}
	return mailRow{}, false
}

// mailUnreadShown counts the unread messages of the accounts on the chip
// (as of the last mailBuild).
func mailUnreadShown() int {
	return mailUI.unread
}

// mailDateText is a message's date for the list: the time today,
// "Yesterday", the weekday this week, else the date, in the interface
// language and the user's date order (clockformat.go). Before the clock is
// set it is always the plain date.
func mailDateText(config *Config, now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt := t.Local()
	if now.Before(clockFloor()) {
		return formatDate(config, lt)
	}
	// The calendar days are compared in UTC, where every day is 24 hours: a
	// local midnight-to-midnight span is 23 or 25 hours across a DST change,
	// which turned "Yesterday" into a weekday twice a year.
	ny, nm, nd := now.Local().Date()
	ty, tm, td := lt.Date()
	today := time.Date(ny, nm, nd, 0, 0, 0, 0, time.UTC)
	day := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	switch diff := today.Sub(day); {
	case diff == 0:
		return clockDigits(config, lt)
	case diff == 24*time.Hour:
		return dateLocaleNow().word(-1)
	case diff > 0 && diff < 6*24*time.Hour:
		return formatWeekdayShort(lt)
	case ty == ny:
		return formatDateShort(config, lt)
	}
	return formatDate(config, lt)
}

// mailFullDate is a message's date for the reader: "Monday, 9 November 2026 ·
// 15:04" in the interface language, date order and clock.
func mailFullDate(config *Config, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt := t.Local()
	return formatDateLong(config, lt) + " · " + clockDigits(config, lt)
}

// mailText draws content exactly as written (no interface translation, no
// variable substitution).
func mailText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) {
	if text == "" || font == nil {
		return
	}
	drawTextRun(renderer, config, font, sanitizeCached(font, text), color, x, y)
}

// ── List ────────────────────────────────────────────────────────────────

func mailListHeader() UIPageHeader {
	sub := "All inboxes"
	for _, a := range mailUI.accounts {
		if a.ID == mailUI.account {
			sub = a.label()
		}
	}
	status, color := "", ColorTextMuted()
	switch {
	case mailUI.busy != "":
		status = mailUI.busy
	case mailMSSign.active:
		status, color = "Signing in", glassAccent()
	case mailSyncing.Load():
		status, color = "Syncing…", glassAccent()
	case mailOffline.Load():
		status, color = "Offline", ToastWarnInk()
	default:
		if n := mailUnreadShown(); n > 0 {
			status, color = strconv.Itoa(n)+" unread", glassAccent()
		}
	}
	return tbxHeader("Mail", sub, "message", status, color)
}

func mailRenderList(renderer *sdl.Renderer, config *Config) {
	mailBuild()
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, mailListHeader())
	body := frame.bodyRect()
	nav := &mailUI.listNav
	mailUI.nav = nav

	// The chips sit above the scrolling list.
	mailUI.chips = mailUI.chips[:0]
	if len(mailUI.accounts) > 1 {
		cx := frame.x
		chipH := fonts.smallH + 14
		for i := 0; i <= len(mailUI.accounts); i++ {
			label := "All inboxes"
			if i > 0 {
				label = mailUI.accounts[i-1].label()
			}
			label = ellipsize(fonts.small, label, 220)
			tw, _ := measureText(fonts.small, label)
			r := sdl.Rect{X: cx, Y: body.Y, W: tw + 28, H: chipH}
			if r.X+r.W > frame.x+frame.w {
				break
			}
			on := mailChipID(i) == mailUI.account
			fill := glassA(30)
			if on {
				fill = WithAlpha(glassAccent(), 200)
			}
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, fill)
			ink := ColorTextSecondary()
			if on {
				ink = TextInk(glassAccent())
			}
			mailText(renderer, config, fonts.small, label, ink, r.X+14, r.Y+7)
			mailUI.chips = append(mailUI.chips, r)
			cx += r.W + 8
		}
		body.Y += chipH + 12
		body.H -= chipH + 12
	}

	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	addRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}

	if mailMSSign.active {
		y = mailDrawSignInCard(renderer, config, fonts, frame, top, y)
		if len(mailMSSign.qr) > 0 {
			addRow("ms-qr", tbxRowSpec{icon: "qr", title: "Show the code full screen", pill: "Enlarge", tone: tbxToneAccent,
				subtitle: "Bigger and brighter for a phone camera"})
		}
		addRow("ms-cancel", tbxRowSpec{icon: "close", title: "Cancel sign-in", pill: "Cancel", tone: tbxToneNeutral,
			subtitle: "Nothing is stored until the phone says yes"})
		y += 10
	}

	if len(mailUI.accounts) == 0 && !mailMSSign.active {
		tbxUnclip(renderer)
		empty := sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: body.H - rowH*2 - 30}
		tbxDrawEmpty(renderer, config, empty, "message", "No mail account yet",
			"Add Gmail, Outlook, iCloud or any IMAP mailbox from your phone — no typing on the handheld.")
		y = empty.H + 10
		addRow("setup-phone", tbxRowSpec{icon: "phone", title: "Set up on your phone", pill: "Scan", tone: tbxToneAccent,
			subtitle: "A QR code opens a form on your phone; most providers need an app password"})
		addRow("settings", tbxRowSpec{icon: "settings", title: "Email & calendar settings", subtitle: "Accounts, sync interval and notifications"})
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}

	for _, a := range mailUI.errors {
		addRow("err:"+a.ID, tbxRowSpec{icon: "warning", title: a.label(), subtitle: a.MailError, pill: "Fix", tone: tbxToneWarn})
	}

	if len(mailUI.rows) == 0 && len(mailUI.accounts) == 0 {
		// The first account is still being signed in: the card above is the
		// whole page, without a "Not synced yet" under it.
		nav.end()
		tbxUnclip(renderer)
		tbxEndFrame(renderer, config)
		return
	}
	if len(mailUI.rows) == 0 {
		title, sub := "No messages", "This inbox is empty."
		switch {
		case mailSyncing.Load():
			title, sub = "Checking your mail…", "The newest messages appear here."
		case mailOffline.Load():
			title, sub = "Offline", "Connect to Wi-Fi to check your mail."
		case mailUI.neverSynced:
			title, sub = "Not synced yet", "Select → Sync now checks your mail."
		}
		nav.extend(y)
		nav.end()
		tbxUnclip(renderer)
		empty := sdl.Rect{X: body.X, Y: top + y, W: body.W, H: max(body.H-y, 160)}
		tbxDrawEmpty(renderer, config, empty, "message", title, sub)
		tbxEndFrame(renderer, config)
		return
	}

	msgH := 14 + fonts.titleH + 4 + fonts.smallH + 4 + fonts.tinyH + 14
	now := time.Now()
	showAcct := mailUI.account == "" && len(mailUI.accounts) > 1
	for _, r := range mailUI.rows {
		id := r.id()
		rect := sdl.Rect{X: x, Y: y, W: w, H: msgH}
		if scr := peerScr(rect, top); scr.Y+scr.H >= body.Y && scr.Y <= body.Y+body.H {
			mailDrawMsgRow(renderer, config, fonts, scr, r, now, showAcct, nav.focused(id))
		}
		nav.add(id, rect, row, false)
		row++
		y += msgH + 6
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// mailDrawMsgRow draws one message in the list.
func mailDrawMsgRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, r mailRow, now time.Time, showAcct, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	m := r.msg
	x := rect.X + 16
	y := rect.Y + 14
	if m.Unread {
		fillCircle(renderer, x+4, y+fonts.titleH/2, 5, glassAccent())
	}
	x += 18
	right := rect.X + rect.W - 16
	if date := mailDateText(config, now, m.Date); date != "" {
		dw, _ := measureText(fonts.small, date)
		renderText(renderer, config, fonts.small, date, ColorTextTertiary(), right-dw, y+(fonts.titleH-fonts.smallH)/2)
		right -= dw + 14
	}
	if m.Attach {
		box := sdl.Rect{X: right - 18, Y: y + (fonts.titleH-18)/2, W: 18, H: 18}
		DrawHomeIcon(renderer, "paperclip", box, focused)
		right -= 24
	}
	ink, sub := ColorTextSecondary(), ColorTextMuted()
	if m.Unread {
		ink, sub = ColorTextPrimary(), ColorTextPrimary()
	}
	mailText(renderer, config, fonts.title, ellipsize(fonts.title, m.sender(), right-x), ink, x, y)
	y += fonts.titleH + 4
	textW := rect.X + rect.W - 16 - x
	mailText(renderer, config, fonts.small, ellipsize(fonts.small, setOr(m.Subject, "(no subject)"), textW), sub, x, y)
	y += fonts.smallH + 4
	snippet := m.Snippet
	if showAcct {
		snippet = r.acct.label() + " · " + snippet
	}
	mailText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, snippet, textW), ColorTextTertiary(), x, y)
}

// mailDrawSignInCard draws the Microsoft code, the address and the QR code.
func mailDrawSignInCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, top, y int32) int32 {
	const qrSide = 196
	card := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: qrSide + 32}
	UICard(renderer, peerScr(card, top), DefaultUICardStyle())
	mailUI.listNav.extend(y + card.H)
	cy := top + card.Y
	textX := card.X + 20
	if mailMSSign.code == "" {
		renderText(renderer, config, fonts.body, "Asking Microsoft for a code…", ColorTextSecondary(), textX, cy+(card.H-fonts.bodyH)/2)
		return y + card.H + 10
	}
	if len(mailMSSign.qr) > 0 {
		tile := sdl.Rect{X: card.X + 16, Y: cy + 16, W: qrSide, H: qrSide}
		fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 14, sdl.Color{R: 255, G: 255, B: 255, A: 255})
		drawCompanionQR(renderer, insetRect(tile, 8), mailMSSign.qr)
		textX = tile.X + tile.W + 24
	}
	textW := card.X + card.W - 20 - textX
	big := FontForRole(config, TypeTitle)
	if big == nil {
		big = fonts.title
	}
	ty := cy + 18
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, "On your phone or computer, open", textW), ColorTextMuted(), textX, ty)
	ty += fonts.smallH + 4
	renderTextRaw(renderer, config, fonts.body, ellipsize(fonts.body, cloudShortURL(mailMSSign.url), textW), ColorTextPrimary(), textX, ty)
	ty += fonts.bodyH + 10
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, "and enter this code", textW), ColorTextMuted(), textX, ty)
	ty += fonts.smallH + 4
	renderTextRaw(renderer, config, big, mailMSSign.code, glassAccent(), textX, ty)
	ty += int32(big.Height()) + 10
	wait := "Waiting for the phone…"
	if left := time.Until(mailMSSign.expires); left > time.Minute {
		wait += " the code works for " + pluralizeCount(int(left/time.Minute), "more minute", "more minutes")
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, wait, textW), ColorTextTertiary(), textX, ty)
	return y + card.H + 10
}

func mailListAction(config *Config, act tbxAction) bool {
	nav := &mailUI.listNav
	switch act {
	case tbxActConfirm:
		mailListActivate(config, nav.focus)
		return true
	case tbxActX:
		if r, ok := mailFind(nav.focus); ok {
			mailMoveRow(r, "trash")
		}
		return true
	case tbxActY:
		return true
	case tbxActBack:
		if mailMSSign.active {
			PlayBackSound()
			mailMSCancelSignIn()
			return true
		}
		return false
	}
	return tbxNavAction(nav, act)
}

func mailListActivate(config *Config, id string) {
	switch {
	case id == "ms-qr":
		PlayActivateSound()
		qrZoomOpenCardDetail(mailMSSign.qr, "Sign in with Microsoft",
			"Open "+cloudShortURL(mailMSSign.url)+" and enter "+mailMSSign.code)
	case id == "ms-cancel":
		PlayBackSound()
		mailMSCancelSignIn()
	case id == "setup-phone":
		PlayActivateSound()
		mailcalSetupFromPhone()
	case id == "settings", strings.HasPrefix(id, "err:"):
		PlayActivateSound()
		mailcalOpenSettings(config)
	default:
		if r, ok := mailFind(id); ok {
			PlayActivateSound()
			mailOpenMessage(r)
		}
	}
}

// ── Reader ──────────────────────────────────────────────────────────────

// mailOpenMessage shows a message and fetches its body.
func mailOpenMessage(r mailRow) {
	mailUI.view = mailViewRead
	mailUI.open = r
	mailUI.openGen++
	gen := mailUI.openGen
	mailUI.entry, mailUI.openErr, mailUI.loading, mailUI.readText = nil, "", true, ""
	mailUI.readNav.scroll = 0
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		e, err := mailFetchBody(ctx, r.acct, r.validity, r.msg.UID)
		cancel()
		tbxPost(func() {
			if mailUI.openGen != gen {
				return
			}
			mailUI.loading = false
			if err != nil {
				mailUI.openErr = mailErrorText(err, r.acct)
				return
			}
			mailUI.entry = e
			mailUI.readText = mailReaderText()
			mailUI.open.msg.Unread = false
			mailSetReadHTML(e.body.FromHTML)
			mailUI.showImages = false
		})
	}()
}

// mailHTMLAvailable reports whether the open message has an HTML part to
// render.
func mailHTMLAvailable() bool {
	return mailUI.entry != nil && mailUI.entry.body.FromHTML && strings.TrimSpace(mailUI.entry.body.HTML) != ""
}

// mailSetReadHTML switches the reader between the browser engine's rendering
// and the flat text. Opening a message calls it with the part's kind and then
// clears showImages, so every message starts with remote images off.
func mailSetReadHTML(on bool) {
	mailUI.readHTML = on && mailHTMLAvailable()
	mailUI.htmlDoc, mailUI.htmlLayout = nil, nil
	mailUI.htmlWidth, mailUI.htmlHeaderH = 0, 0
	mailUI.readNav.scroll = 0
}

// mailSetShowImages lets remote pictures into an HTML message (or blocks them
// again); the page is parsed afresh so the tags come back or go away.
func mailSetShowImages(on bool) {
	if mailUI.showImages == on {
		return
	}
	mailUI.showImages = on
	mailUI.htmlDoc, mailUI.htmlLayout = nil, nil
	mailUI.htmlWidth = 0
}

var (
	mailHTMLScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	mailHTMLFrame  = regexp.MustCompile(`(?is)<(iframe|object|embed|form|video|audio|meta|base|link)\b[^>]*>.*?</(iframe|object|form|video|audio)\s*>`)
	mailHTMLVoid   = regexp.MustCompile(`(?is)<(embed|meta|base|link)\b[^>]*>`)
	mailHTMLImage  = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	mailHTMLEvent  = regexp.MustCompile(`(?is)\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
	mailHTMLJS     = regexp.MustCompile(`(?is)(href|src)\s*=\s*(["']?)\s*javascript\s*:`)
	mailHTMLCSSURL = regexp.MustCompile(`(?is)url\s*\([^)]*\)`)
)

// mailSafeHTML makes an HTML body safe to hand to the browser engine: scripts
// and active embeds are removed, event handlers and javascript: URLs are
// dropped, and remote pictures (img tags and CSS url() layers) are removed
// until the reader presses Show images. The engine never runs page scripts; it
// only parses and lays the markup out.
func mailSafeHTML(src string, showImages bool) string {
	src = mailHTMLScript.ReplaceAllString(src, "")
	src = mailHTMLFrame.ReplaceAllString(src, "")
	src = mailHTMLVoid.ReplaceAllString(src, "")
	src = mailHTMLEvent.ReplaceAllString(src, "")
	src = mailHTMLJS.ReplaceAllString(src, `${1}=${2}about:blank`)
	if !showImages {
		src = mailHTMLImage.ReplaceAllString(src, "")
		src = mailHTMLCSSURL.ReplaceAllString(src, "none")
	}
	return src
}

// mailAttachHeading heads the attachment list in the reader. The reader
// matches the whole line, so a body line that happens to start with "## " is
// still drawn as written rather than as an interface heading.
const mailAttachHeading = "## Attachments — Select saves them to Downloads"

// mailReaderText is what the reader draws below the headers.
func mailReaderText() string {
	e := mailUI.entry
	if e == nil {
		return ""
	}
	text := e.body.Text
	const show = 96 << 10
	if len(text) > show {
		cut := strings.LastIndexByte(text[:show], '\n')
		if cut < show/2 {
			// No line break late enough: cut at a rune boundary, never
			// inside a multi-byte character.
			cut = show
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		text = text[:cut] + "\n\n[The rest is too long to show on the handheld.]"
	}
	if len(e.body.Attachments) > 0 {
		var b strings.Builder
		b.WriteString(text)
		b.WriteString("\n\n" + mailAttachHeading + "\n")
		for _, a := range e.body.Attachments {
			b.WriteString("• " + mailClean(a.Name, 80) + " · " + formatSize(int64(a.Size)))
			if a.Partial {
				b.WriteString(" (incomplete)")
			}
			b.WriteByte('\n')
		}
		text = b.String()
	}
	return text
}

func mailRenderReader(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	m := mailUI.open.msg
	header := tbxHeader(setOr(m.Subject, "(no subject)"), mailUI.open.acct.label(), "message", mailUI.busy, glassAccent())
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &mailUI.readNav
	nav.begin(body.Y, body.H)
	switch {
	case mailUI.loading:
		nav.end()
		tbxDrawEmpty(renderer, config, body, "message", "Opening…", "")
		tbxEndFrame(renderer, config)
		return
	case mailUI.openErr != "":
		nav.end()
		tbxDrawEmpty(renderer, config, body, "warning", "The message did not open", mailUI.openErr)
		tbxEndFrame(renderer, config)
		return
	}
	if mailUI.readHTML && mailHTMLAvailable() {
		if mailRenderHTMLReader(renderer, config, frame, body, fonts) {
			return
		}
		// The browser engine could not lay the part out: show the flat text
		// that mailParseBody kept beside it.
		mailUI.readHTML = false
	}
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w

	// The header card.
	lines := []string{"From  " + mailFromLine(m)}
	if m.To != "" {
		lines = append(lines, "To  "+m.To)
	}
	if m.Cc != "" {
		lines = append(lines, "Cc  "+m.Cc)
	}
	if d := mailFullDate(config, m.Date); d != "" {
		lines = append(lines, d)
	}
	cardH := int32(len(lines))*(fonts.smallH+4) + 24
	UICard(renderer, sdl.Rect{X: x, Y: top, W: w, H: cardH}, DefaultUICardStyle())
	ly := top + 12
	for _, l := range lines {
		mailText(renderer, config, fonts.small, ellipsize(fonts.small, l, w-36), ColorTextSecondary(), x+18, ly)
		ly += fonts.smallH + 4
	}
	y := cardH + 18

	text := mailUI.readWrap.get(fonts.body, mailUI.readText, w-8)
	lineH := fonts.bodyH + 5
	for _, l := range text {
		sy := top + y
		if sy+lineH >= body.Y && sy <= body.Y+body.H {
			switch {
			case l == mailAttachHeading:
				renderText(renderer, config, fonts.small, strings.TrimPrefix(l, "## "), ColorTextTertiary(), x, sy+4)
			case strings.HasPrefix(l, ">"):
				mailText(renderer, config, fonts.body, l, ColorTextTertiary(), x, sy)
			default:
				mailText(renderer, config, fonts.body, l, ColorTextPrimary(), x, sy)
			}
		}
		y += lineH
	}
	nav.extend(y + 20)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// mailRenderHTMLReader draws the message's HTML part through the browser
// engine: the header card, then the laid-out page, scrolled as one. It reports
// false when the part has nothing the engine can lay out, which leaves the
// reader on its plain text.
func mailRenderHTMLReader(renderer *sdl.Renderer, config *Config, frame tbxFrame, body sdl.Rect, fonts tbxFonts) bool {
	m := mailUI.open.msg
	nav := &mailUI.readNav
	html := mailSafeHTML(mailUI.entry.body.HTML, mailUI.showImages)
	width := body.W
	if width < 80 {
		return false
	}
	if mailUI.htmlDoc == nil || mailUI.htmlWidth != width {
		mailUI.htmlDoc = parseWebPage(html, "", "")
		mailUI.htmlWidth = width
		mailUI.htmlLayout = nil
	}
	layout := mailUI.htmlLayout
	if layout == nil {
		layout, _ = webDocumentRows(mailUI.htmlDoc, config, Element{Font: "body"}, width)
		mailUI.htmlLayout = layout
	}
	if layout == nil || len(layout.Rows) == 0 {
		return false
	}

	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	lines := []string{"From  " + mailFromLine(m)}
	if m.To != "" {
		lines = append(lines, "To  "+m.To)
	}
	if m.Cc != "" {
		lines = append(lines, "Cc  "+m.Cc)
	}
	if d := mailFullDate(config, m.Date); d != "" {
		lines = append(lines, d)
	}
	cardH := int32(len(lines))*(fonts.smallH+4) + 24
	UICard(renderer, sdl.Rect{X: x, Y: top, W: w, H: cardH}, DefaultUICardStyle())
	ly := top + 12
	for _, l := range lines {
		mailText(renderer, config, fonts.small, ellipsize(fonts.small, l, w-36), ColorTextSecondary(), x+18, ly)
		ly += fonts.smallH + 4
	}

	pageH := layout.Height
	if pageH <= 0 {
		pageH = int32(len(layout.Rows)) * layout.RowH
	}
	headerH := cardH + 18
	mailUI.htmlHeaderH, mailUI.htmlBody = headerH, body
	tbxClip(renderer, body)
	drawWebPageContent(renderer, config, mailUI.htmlDoc, layout,
		sdl.Rect{X: body.X, Y: top + headerH, W: width, H: pageH}, 0, webPaintAll)
	nav.extend(headerH + pageH + 20)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
	return true
}

// mailHTMLTapLink opens the HTML link under a tap in the Browser. It reports
// whether the tap was on one, so the reader can keep the click for scrolling.
func mailHTMLTapLink(config *Config, mx, my int32) bool {
	layout := mailUI.htmlLayout
	body := mailUI.htmlBody
	if layout == nil || body.W <= 0 || layout.RowH <= 0 {
		return false
	}
	contentTop := body.Y + mailUI.htmlHeaderH - mailUI.readNav.scroll
	for _, hit := range layout.Links {
		if hit.Control != nil || hit.Video {
			continue
		}
		var r sdl.Rect
		if hit.Box.W > 0 && hit.Box.H > 0 {
			r = sdl.Rect{X: body.X + hit.Box.X, Y: contentTop + hit.Box.Y, W: hit.Box.W, H: hit.Box.H}
		} else {
			if hit.Row < 0 || hit.Row >= len(layout.Rows) {
				continue
			}
			r = sdl.Rect{X: body.X + hit.X - 4, Y: contentTop + layout.Rows[hit.Row].Y - 2, W: hit.W + 8, H: layout.RowH + 4}
		}
		if !pointInRect(mx, my, r) {
			continue
		}
		target, ok := browserResolveLink("https://mail.invalid/", hit.Href)
		if !ok {
			ShowToast("That link cannot be opened here", ToastKindInfo)
			return true
		}
		PlayActivateSound()
		openBrowserAtURL(config, target)
		return true
	}
	return false
}

// mailFromLine is "Name <address>".
func mailFromLine(m mailCacheMsg) string {
	switch {
	case m.From != "" && m.FromAddr != "":
		return m.From + " <" + m.FromAddr + ">"
	case m.FromAddr != "":
		return m.FromAddr
	}
	return m.sender()
}

func mailReaderAction(config *Config, act tbxAction) bool {
	nav := &mailUI.readNav
	step := int32(90)
	switch act {
	case tbxActUp:
		if nav.scroll <= 0 {
			// At the top an unused Up reaches the top bar, as on every list.
			return false
		}
		nav.scrollBy(-step)
	case tbxActDown:
		nav.scrollBy(step)
	case tbxActPageUp:
		nav.scrollBy(-nav.viewH * 3 / 4)
	case tbxActPageDown:
		nav.scrollBy(nav.viewH * 3 / 4)
	case tbxActHome:
		nav.scroll = 0
	case tbxActEnd:
		nav.scroll = nav.maxScroll()
	case tbxActY:
		mailReplyOpen(config, false)
	case tbxActX:
		mailMoveRow(mailUI.open, "trash")
	case tbxActConfirm:
		mailOpenMenu(config)
	case tbxActBack:
		PlayBackSound()
		mailUI.view = mailViewList
		mailUI.openGen++
	default:
		return false
	}
	return true
}

// ── Actions ─────────────────────────────────────────────────────────────

// mailRunAction runs a server action off the main thread; done gets the
// error back on the main loop.
func mailRunAction(label string, fn func(ctx context.Context) error, done func(err error)) {
	if mailUI.busy != "" {
		ShowToast("Wait a moment — "+strings.ToLower(mailUI.busy), ToastKindInfo)
		return
	}
	mailUI.busy = label
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := fn(ctx)
		cancel()
		tbxPost(func() {
			mailUI.busy = ""
			mailUI.builtGen = 0
			done(err)
		})
	}()
}

// mailMoveRow moves a message to Trash or the archive.
func mailMoveRow(r mailRow, where string) {
	PlayActivateSound()
	label, done := "Deleting…", "Moved to Trash"
	if where == "archive" {
		label, done = "Archiving…", "Archived"
	}
	if mailUI.busy != "" {
		// One action at a time: a second X while the first is on its way
		// says so instead of moving anything (mailRunAction's rule).
		ShowToast("Wait a moment — "+strings.ToLower(mailUI.busy), ToastKindInfo)
		return
	}
	// The list's focus goes to the neighbour, not back to the top, once the
	// row is gone. From the list it moves now (the cache drops the row before
	// the answer arrives, and the list would otherwise refocus its first row);
	// from the reader it moves when the reader closes.
	next := ""
	for i := range mailUI.rows {
		if mailUI.rows[i].id() != r.id() {
			continue
		}
		if i+1 < len(mailUI.rows) {
			next = mailUI.rows[i+1].id()
		} else if i > 0 {
			next = mailUI.rows[i-1].id()
		}
		break
	}
	if mailUI.view == mailViewList && next != "" && mailUI.listNav.focus == r.id() {
		mailUI.listNav.setFocus(next)
	}
	mailRunAction(label, func(ctx context.Context) error {
		return mailMoveMessage(ctx, r.acct, r.validity, r.msg.UID, where)
	}, func(err error) {
		if err != nil {
			ShowToast(mailErrorText(err, r.acct), ToastKindWarning)
			return
		}
		ShowToast(done, ToastKindSuccess)
		if mailUI.view == mailViewRead && mailUI.open.id() == r.id() {
			mailUI.view = mailViewList
			mailUI.openGen++
		}
		if next != "" && mailUI.listNav.focus == r.id() {
			mailUI.listNav.focus = next
		}
	})
}

// mailSetUnreadRow marks the open message unread and returns to the list.
func mailSetUnreadRow(r mailRow) {
	mailRunAction("Marking…", func(ctx context.Context) error {
		return mailSetUnread(ctx, r.acct, r.validity, r.msg.UID, true)
	}, func(err error) {
		if err != nil {
			ShowToast(mailErrorText(err, r.acct), ToastKindWarning)
			return
		}
		ShowToast("Marked unread", ToastKindInfo)
		mailUI.view = mailViewList
		mailUI.openGen++
	})
}

// mailMarkAllShown marks every inbox on the chip read.
func mailMarkAllShown() {
	var accts []mailcalAccount
	for _, a := range mailUI.accounts {
		if mailUI.account == "" || a.ID == mailUI.account {
			accts = append(accts, a)
		}
	}
	mailRunAction("Marking…", func(ctx context.Context) error {
		var first error
		for _, a := range accts {
			if mailCacheGet(a.ID).Unread == 0 {
				continue
			}
			if err := mailMarkAllRead(ctx, a, mailCacheGet(a.ID).Validity); err != nil && first == nil {
				first = mailPersonErr(err, a)
			}
		}
		return first
	}, func(err error) {
		if err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return
		}
		ShowToast("All marked read", ToastKindSuccess)
	})
}

// mailSaveAttachment saves attachment i of the open message to Downloads.
func mailSaveAttachment(i int) {
	e := mailUI.entry
	if e == nil || i < 0 || i >= len(e.body.Attachments) {
		return
	}
	att := e.body.Attachments[i]
	raw := e.raw
	mailRunAction("Saving…", func(context.Context) error {
		data, ok := mailExtractAttachment(raw, att.index)
		if !ok {
			return &mailError{kind: mailErrServer, msg: "the attachment could not be read"}
		}
		dir := chatSaveDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &mailError{kind: mailErrServer, msg: "the Downloads folder cannot be written"}
		}
		path := mailFreePath(dir, mailSafeFileName(att.Name))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return &mailError{kind: mailErrServer, msg: "the file could not be saved: " + firstLine(err.Error())}
		}
		return nil
	}, func(err error) {
		if err != nil {
			ShowToast(mailUpperFirst(err.Error()), ToastKindWarning)
			return
		}
		ShowToast("Saved "+mailClean(att.Name, 40)+" to Downloads", ToastKindSuccess)
	})
}

// mailSafeFileName makes an attachment's name safe on the SD card and on
// Windows.
func mailSafeFileName(name string) string {
	var b strings.Builder
	for _, r := range mailClean(name, 120) {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		return "attachment"
	}
	return out
}

// mailFreePath is dir/name, or "name (2).ext" … when that exists.
func mailFreePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		p = filepath.Join(dir, stem+" ("+strconv.Itoa(i)+")"+ext)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
	return filepath.Join(dir, stem+"-"+strconv.FormatInt(time.Now().UnixNano(), 36)+ext)
}

// ── Menu ────────────────────────────────────────────────────────────────

// mailOpenMenu opens the Select menu for the view on screen.
func mailOpenMenu(config *Config) {
	var items []mailMenuItem
	add := func(label, icon string, run func()) {
		items = append(items, mailMenuItem{label: label, icon: icon, run: run})
	}
	switch mailUI.view {
	case mailViewRead:
		r := mailUI.open
		add("Reply", "back", func() { mailReplyOpen(config, false) })
		if r.msg.Cc != "" || strings.Contains(r.msg.To, ",") {
			add("Reply all", "users", func() { mailReplyOpen(config, true) })
		}
		add("Archive", "box", func() { mailMoveRow(r, "archive") })
		add("Delete", "trash", func() { mailMoveRow(r, "trash") })
		add("Mark unread", "message", func() { mailSetUnreadRow(r) })
		if mailHTMLAvailable() {
			if mailUI.readHTML {
				add("Show plain text", "message", func() { mailSetReadHTML(false) })
				if !mailUI.showImages {
					add("Show images", "image", func() { mailSetShowImages(true) })
				}
			} else {
				add("Show formatted message", "globe", func() { mailSetReadHTML(true) })
			}
		}
		if e := mailUI.entry; e != nil {
			for i, a := range e.body.Attachments {
				add("Save “"+mailClean(a.Name, 40)+"” to Downloads", "download", func() { mailSaveAttachment(i) })
				items[len(items)-1].raw = true
			}
		}
	case mailViewCompose:
		add("Send", "upload", func() { mailComposeSend(config) })
		add("Discard", "trash", func() { mailComposeDiscard() })
	default:
		if len(mailUI.accounts) > 0 {
			add("Compose", "edit", func() { mailComposeNew(config) })
			add("Sync now", "sync", func() {
				if !IsOnline() {
					ShowToast("Offline — connect to Wi-Fi to check your mail", ToastKindWarning)
					return
				}
				ShowToast("Checking your mail…", ToastKindInfo)
				mailSyncNow("menu")
			})
			if mailUnreadShown() > 0 {
				add("Mark all read", "check", func() { mailMarkAllShown() })
			}
		}
		if mailMSConfigured(config) {
			add("Sign in with Microsoft (Outlook)", "key", func() { mailMSSignIn(config, "") })
		}
		add("Add an account from your phone", "phone", func() { mailcalSetupFromPhone() })
		add("Email & calendar settings", "settings", func() { mailcalOpenSettings(config) })
	}
	PlayActivateSound()
	mailUI.menuItems, mailUI.menuFocus, mailUI.menuFirst, mailUI.menuOpen = items, 0, 0, true
}

// mailMenuAction applies one action to the open menu.
func mailMenuAction(act tbxAction) bool {
	n := len(mailUI.menuItems)
	switch act {
	case tbxActUp:
		if mailUI.menuFocus > 0 {
			mailUI.menuFocus--
			PlayNavSound()
		}
	case tbxActDown:
		if mailUI.menuFocus < n-1 {
			mailUI.menuFocus++
			PlayNavSound()
		}
	case tbxActConfirm:
		if mailUI.menuFocus >= 0 && mailUI.menuFocus < n {
			item := mailUI.menuItems[mailUI.menuFocus]
			mailUI.menuOpen = false
			if item.run != nil {
				item.run()
			}
		}
	case tbxActBack:
		mailUI.menuOpen = false
		PlayBackSound()
	}
	return true
}

// mailRenderMenu draws the menu as a card over the view.
func mailRenderMenu(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	n := int32(len(mailUI.menuItems))
	rowH := fonts.bodyH + 22
	cardW := min(int32(560), screenWidth-64)
	cardH := n*(rowH+6) + 34
	if cardH > screenHeight-48 {
		cardH = screenHeight - 48
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	mailUI.menuCard = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	mailUI.menuRects = mailUI.menuRects[:0]
	fits := int((cardH - 34 + 6) / (rowH + 6))
	if fits < 1 {
		fits = 1
	}
	if mailUI.menuFocus < mailUI.menuFirst {
		mailUI.menuFirst = mailUI.menuFocus
	}
	if mailUI.menuFocus >= mailUI.menuFirst+fits {
		mailUI.menuFirst = mailUI.menuFocus - fits + 1
	}
	if mailUI.menuFirst > len(mailUI.menuItems)-fits {
		mailUI.menuFirst = max(len(mailUI.menuItems)-fits, 0)
	}
	y := card.Y + 14
	for i := mailUI.menuFirst; i < len(mailUI.menuItems); i++ {
		it := mailUI.menuItems[i]
		r := sdl.Rect{X: card.X + 14, Y: y, W: card.W - 28, H: rowH}
		if r.Y+r.H > card.Y+card.H-10 {
			break
		}
		focused := i == mailUI.menuFocus
		UIListRow(renderer, r, glassRowRadius, focused, false)
		icon := sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-24)/2, W: 24, H: 24}
		DrawHomeIcon(renderer, it.icon, icon, focused)
		if it.raw {
			mailText(renderer, config, fonts.body, ellipsize(fonts.body, it.label, r.W-70), ColorTextPrimary(), r.X+52, r.Y+(r.H-fonts.bodyH)/2)
		} else {
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, it.label, r.W-70), ColorTextPrimary(), r.X+52, r.Y+(r.H-fonts.bodyH)/2)
		}
		mailUI.menuRects = append(mailUI.menuRects, r)
		y += rowH + 6
	}
	renderer.SetClipRect(nil)
}
