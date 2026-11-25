package main

// mailcal_ui.go — the Email & calendar page (Tools, and Settings → Network),
// plus the small pieces Home and the menus show of it.
//
// One page in the tool-screen frame (tbxui.go):
//
//   - Accounts: Open Mail with the unread count, each mailbox with its status
//     (the error a sync left, or Ready) and its buttons — Sync now, Mail on/off,
//     Calendar on/off for an account with a CalDAV address, Remove — then
//     "Add an account from your phone" and, in a build with a Microsoft
//     client, "Sign in with Microsoft" for Outlook addresses;
//   - Calendars: each link or CalDAV calendar with its switch and Remove, and
//     "Add a calendar from your phone";
//   - Sync & reminders: how often, new-mail notifications, event reminders and
//     how early;
//   - Help: the setup guide with a QR code of docs/EMAIL_CALENDAR.md.
//
// Every change goes through mailcalSettingsUpdate on a goroutine; the page
// rebuilds its model from the store at most once a second, so a sync that
// writes an error, or the phone adding an account, shows up by itself.

import (
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const mailcalSceneName = "EmailCalendar"

// mailcalAppEntryName is the Mail app's name in the Start menu, the Apps list
// and on a Home tile ("app:do:Mail").
const mailcalAppEntryName = "Mail"

// mailcalSettingsEntryName is the page's name in the Start menu and search.
const mailcalSettingsEntryName = "Email & calendar"

// mailcalMenuBadge is the Start menu's word beside Mail: "3 new", or nothing.
func mailcalMenuBadge() string {
	if n := mailUnreadTotal(); n > 0 {
		return chatBadgeLabel(n) + " new"
	}
	return ""
}

// mailcalDocURL is the setup guide, for the help sheet's QR code.
const mailcalDocURL = "https://github.com/jukaLang/JukaHubV2/blob/main/docs/EMAIL_CALENDAR.md"

// The choices the page cycles through.
var (
	mailcalPollChoices     = [...]int{5, 15, 30, 60}
	mailcalReminderChoices = [...]int{5, 10, 15, 30, 60}
)

type mailcalModel struct {
	builtAt  time.Time
	header   UIPageHeader
	settings mailcalSettings
	ms       bool
	unread   int
}

type mailcalScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model mailcalModel
}

var mailcalScreen mailcalScreenState

// mailcalOpenSettings opens the Email & calendar page. Main thread.
func mailcalOpenSettings(config *Config) {
	if config == nil {
		config = appConfig
	}
	mailcalRefreshMS(config)
	mailcalUIInvalidate()
	tbxOpenScene(config, mailcalSceneName)
}

// mailcalUIInvalidate has the page read the store again on its next frame.
// Main thread.
func mailcalUIInvalidate() {
	mailcalScreen.model.builtAt = time.Time{}
}

type mailcalScene struct{ sceneBase }

func (mailcalScene) render(r *sdl.Renderer, c *Config) bool {
	if mailcalScreen.visit.begin() {
		mailcalScreen.nav.scroll = 0
		mailcalRefreshMS(c)
		mailcalUIInvalidate()
	}
	mailcalRenderScreen(r, c)
	return true
}

func (mailcalScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), mailcalSceneAction)
}

func (mailcalScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), mailcalSceneAction)
}

func init() { registerScene(mailcalScene{sceneBase{name: mailcalSceneName}}) }

// ── Model ────────────────────────────────────────────────────────────────

func mailcalBuildModel(now time.Time) mailcalModel {
	m := mailcalModel{builtAt: now, settings: mailcalSettingsGet(), ms: mailcalMSAvailable.Load(), unread: mailUnreadTotal()}
	status, ink := "Not set up", ColorTextMuted()
	n := len(m.settings.Accounts) + len(m.settings.Calendars)
	if n > 0 {
		status, ink = pluralizeCount(n, "source", "sources"), ToastSuccessInk()
		for _, a := range m.settings.Accounts {
			if !a.MailOff && a.MailError != "" || a.CalDAVURL != "" && !a.CalOff && a.CalError != "" {
				status, ink = "Needs a look", ToastWarnInk()
			}
		}
		for _, c := range m.settings.Calendars {
			if !c.Off && c.Error != "" {
				status, ink = "Needs a look", ToastWarnInk()
			}
		}
	}
	m.header = tbxHeader("Email & calendar", "Your mailboxes and calendars on the handheld", "mail", status, ink)
	return m
}

// mailcalAccountSpec is an account's row.
func mailcalAccountSpec(a mailcalAccount) tbxRowSpec {
	parts := []string{mailcalProviderName(a)}
	if a.OAuth == mailcalOAuthMicrosoft {
		parts = append(parts, "Microsoft sign-in")
	}
	spec := tbxRowSpec{icon: "mail", title: a.label(), pill: "Ready", tone: tbxToneGood}
	switch {
	case !a.hasSecret():
		spec.pill, spec.tone = "Sign in again", tbxToneWarn
	case a.MailOff && (a.CalDAVURL == "" || a.CalOff):
		spec.pill, spec.tone, spec.dim = "Off", tbxToneNeutral, true
	case !a.MailOff && a.MailError != "":
		spec.pill, spec.tone = "Error", tbxToneBad
		parts = append(parts, a.MailError)
	case a.CalDAVURL != "" && !a.CalOff && a.CalError != "":
		spec.pill, spec.tone = "Calendar error", tbxToneWarn
		parts = append(parts, a.CalError)
	}
	if a.MailOff {
		parts = append(parts, "mail off")
	}
	if a.CalDAVURL != "" && !a.CalOff && a.CalError == "" {
		parts = append(parts, "with its calendar")
	}
	spec.subtitle = strings.Join(parts, " · ")
	return spec
}

// mailcalCalendarSub is a calendar's line: its kind and host, or its error.
func mailcalCalendarSub(c mailcalCalendar) string {
	kind := "Calendar link"
	if c.Kind == mailcalCalCalDAV {
		kind = "CalDAV"
	}
	if c.Error != "" && !c.Off {
		return kind + " · " + c.Error
	}
	if host := mailcalURLHost(c.URL, mailcalKeyCalURL); host != "" {
		return kind + " · " + host
	}
	return kind
}

// mailcalPollLabel and mailcalReminderLabel are the pills of the two cycles.
func mailcalPollLabel(s mailcalSettings) string {
	return "Every " + strconv.Itoa(s.poll()) + " min"
}

func mailcalReminderLabel(s mailcalSettings) string {
	return strconv.Itoa(s.reminder()) + " min before"
}

// mailcalNextChoice is the choice after cur in choices (the first after the
// last, or for a value that is not one of them). Pure.
func mailcalNextChoice(choices []int, cur int) int {
	for i, c := range choices {
		if c == cur {
			return choices[(i+1)%len(choices)]
		}
	}
	for _, c := range choices {
		if c > cur {
			return c
		}
	}
	return choices[0]
}

// ── Drawing ──────────────────────────────────────────────────────────────

func mailcalRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(mailcalScreen.model.builtAt) > time.Second {
		mailcalScreen.model = mailcalBuildModel(now)
	}
	m := &mailcalScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &mailcalScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)

	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	drawToggle := func(id, title, sub string, on bool, width int32) {
		rect := sdl.Rect{X: x, Y: y, W: width, H: rowH}
		screen := rect
		screen.Y += top
		bugrepDrawToggleRow(renderer, config, fonts, screen, title, sub, on, nav.focused(id))
		nav.add(id, rect, row, false)
	}
	type button struct{ id, label string }
	drawButtons := func(buttons []button) {
		bx := x
		for _, b := range buttons {
			bw := tbxButtonWidth(fonts.body, b.label, 140)
			rect := sdl.Rect{X: bx, Y: y, W: bw, H: 44}
			screen := rect
			screen.Y += top
			tbxDrawButton(renderer, config, fonts.body, screen, b.label, false, nav.focused(b.id), false)
			nav.add(b.id, rect, row, false)
			bx += bw + 10
		}
		row++
		y += 44 + 16
	}
	onOff := func(on bool) string {
		if on {
			return "On"
		}
		return "Off"
	}
	s := m.settings

	// ACCOUNTS
	tbxDrawSection(renderer, config, "Accounts", "mail", x, top+y, w)
	y += tbxSectionHeight
	if len(s.Accounts) > 0 {
		open := tbxRowSpec{icon: "mail", title: "Open Mail", subtitle: "Your inbox: read, reply and write", pill: "Open", tone: tbxToneAccent}
		if m.unread > 0 {
			open.subtitle = chatUnreadSubtitle(m.unread)
			open.pill = chatBadgeLabel(m.unread) + " new"
		}
		drawRow("open", open)
	}
	for _, a := range s.Accounts {
		drawRow("acct:"+a.ID, mailcalAccountSpec(a))
		buttons := []button{{"sync:" + a.ID, "Sync now"}, {"mail:" + a.ID, "Mail: " + onOff(!a.MailOff)}}
		if a.CalDAVURL != "" {
			buttons = append(buttons, button{"cal:" + a.ID, "Calendar: " + onOff(!a.CalOff)})
		}
		buttons = append(buttons, button{"rm:" + a.ID, "Remove"})
		drawButtons(buttons)
	}
	if len(s.Accounts) < mailcalMaxAccounts {
		drawRow("add-phone", tbxRowSpec{icon: "qr", title: "Add an account from your phone", pill: "Phone", tone: tbxToneAccent,
			subtitle: "Scan a code, then type the address and app password there — Gmail, iCloud, Yahoo, Fastmail and more"})
		if m.ms {
			drawRow("ms", tbxRowSpec{icon: "user", title: "Sign in with Microsoft", pill: "Outlook", tone: tbxToneNeutral,
				subtitle: "Outlook, Hotmail and Live addresses: approve the sign-in on your phone"})
		}
	}
	y += 14

	// CALENDARS
	tbxDrawSection(renderer, config, "Calendars", "calendar", x, top+y, w)
	y += tbxSectionHeight
	for _, c := range s.Calendars {
		name := c.Name
		if name == "" {
			name = "Calendar"
		}
		rmLabel := "Remove"
		bw := tbxButtonWidth(fonts.body, rmLabel, 140)
		drawToggle("calon:"+c.ID, name, mailcalCalendarSub(c), !c.Off, w-bw-12)
		rect := sdl.Rect{X: x + w - bw, Y: y + (rowH-48)/2, W: bw, H: 48}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.body, screen, rmLabel, false, nav.focused("calrm:"+c.ID), false)
		nav.add("calrm:"+c.ID, rect, row, false)
		row++
		y += rowH + 8
	}
	if len(s.Calendars) < mailcalMaxCalendars {
		drawRow("add-cal", tbxRowSpec{icon: "calendar", title: "Add a calendar from your phone", pill: "Phone", tone: tbxToneAccent,
			subtitle: "Paste a Google, Outlook or any .ics link, or a CalDAV address — events show in the clock's calendar"})
	}
	y += 14

	// SYNC & REMINDERS
	tbxDrawSection(renderer, config, "Sync & reminders", "sync", x, top+y, w)
	y += tbxSectionHeight
	drawRow("poll", tbxRowSpec{icon: "sync", title: "Check for mail and events", pill: mailcalPollLabel(s), tone: tbxToneNeutral,
		subtitle: "In the background while JukaHub is open; Sync now any time"})
	drawToggle("notify", "New-mail notifications", "A notification when mail arrives", !s.NoMailNotify, w)
	row++
	y += rowH + 8
	drawToggle("remind", "Event reminders", "A notification before each event starts", !s.NoReminders, w)
	row++
	y += rowH + 8
	drawRow("lead", tbxRowSpec{icon: "bell", title: "Remind me", pill: mailcalReminderLabel(s), tone: tbxToneNeutral,
		subtitle: "How long before an event its reminder shows", dim: s.NoReminders})
	y += 14

	// HELP
	tbxDrawSection(renderer, config, "Help", "info", x, top+y, w)
	y += tbxSectionHeight
	drawRow("about", tbxRowSpec{icon: "info", title: "How to set it up", subtitle: "App passwords, calendar links, and what the handheld keeps"})

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func mailcalSceneAction(config *Config, act tbxAction) bool {
	nav := &mailcalScreen.nav
	switch act {
	case tbxActConfirm:
		mailcalActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func mailcalSceneClick(mx, my int32, config *Config) bool {
	it, ok := mailcalScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	mailcalScreen.nav.focus = it.id
	mailcalActivate(config, it.id)
	return true
}

func mailcalSceneHints() []FooterHint {
	label := "Select"
	f := mailcalScreen.nav.focus
	switch {
	case f == "open":
		label = "Open"
	case strings.HasPrefix(f, "acct:"), strings.HasPrefix(f, "sync:"):
		label = "Sync now"
	case strings.HasPrefix(f, "calon:"), strings.HasPrefix(f, "mail:"), strings.HasPrefix(f, "cal:"), f == "notify", f == "remind":
		label = "Toggle"
	case strings.HasPrefix(f, "rm:"), strings.HasPrefix(f, "calrm:"):
		label = "Remove"
	case f == "poll", f == "lead":
		label = "Change"
	case f == "about":
		label = "Read"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: "Back"}}
}

// mailcalSave changes the store on a goroutine and refreshes the page; then
// runs on the main loop after a successful save.
func mailcalSave(change func(*mailcalSettings), then func()) {
	go func() {
		err := mailcalSettingsUpdate(change)
		tbxPost(func() {
			mailcalUIInvalidate()
			if err != nil {
				ShowToast("Could not save the setting: "+firstLine(err.Error()), ToastKindError)
				return
			}
			if then != nil {
				then()
			}
		})
	}()
}

// mailcalRemoveAsync runs a Remove (which writes mailcal.json) on a goroutine,
// like every other change on the page, and says what went on the main loop.
// The forgetting itself is posted by remove (mailcalRemoveAccount).
func mailcalRemoveAsync(remove func() string) {
	go func() {
		label := remove()
		tbxPost(func() {
			mailcalUIInvalidate()
			if label != "" {
				ShowToast("Removed "+label, ToastKindInfo)
			}
		})
	}()
}

func mailcalActivate(config *Config, id string) {
	key, arg, _ := strings.Cut(id, ":")
	s := mailcalScreen.model.settings
	switch key {
	case "open":
		PlayActivateSound()
		mailOpenApp()
	case "acct", "sync":
		PlayActivateSound()
		mailSyncNow("manual")
		calSyncNow("manual")
		ShowToast("Syncing mail and calendars…", ToastKindInfo)
	case "mail":
		PlayToggleSound()
		mailcalSave(func(s *mailcalSettings) {
			for i := range s.Accounts {
				if s.Accounts[i].ID == arg {
					s.Accounts[i].MailOff = !s.Accounts[i].MailOff
				}
			}
		}, func() { mailSyncNow("settings") })
	case "cal":
		PlayToggleSound()
		mailcalSave(func(s *mailcalSettings) {
			for i := range s.Accounts {
				if s.Accounts[i].ID == arg {
					s.Accounts[i].CalOff = !s.Accounts[i].CalOff
				}
			}
		}, func() { calSyncNow("settings") })
	case "rm":
		a, ok := s.account(arg)
		if !ok {
			return
		}
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "Remove this account?",
			subtitle: a.label(),
			icon:     "trash",
			body:     "The handheld forgets the account, its password and the mail and events it synced. Nothing changes on the server: add it again any time.",
			buttons: []tbxSheetButton{
				{label: "Keep"},
				{label: "Remove", primary: true, run: func() {
					mailcalScreen.nav.focus = ""
					mailcalRemoveAsync(func() string { return mailcalRemoveAccount(arg) })
				}},
			},
		})
	case "add-phone":
		PlayActivateSound()
		mailcalSetupFromPhone()
	case "ms":
		PlayActivateSound()
		tbxOpenPrompt("Outlook address", "you@outlook.com", "", func(text string) {
			email, err := mailcalCleanEmail(text)
			if err != nil {
				if strings.TrimSpace(text) != "" {
					ShowToast(err.Error(), ToastKindWarning)
				}
				return
			}
			mailMSSignIn(config, email)
			mailcalUIInvalidate()
		})
	case "calon":
		PlayToggleSound()
		mailcalSave(func(s *mailcalSettings) {
			for i := range s.Calendars {
				if s.Calendars[i].ID == arg {
					s.Calendars[i].Off = !s.Calendars[i].Off
				}
			}
		}, func() { calSyncNow("settings") })
	case "calrm":
		name := "this calendar"
		for _, c := range s.Calendars {
			if c.ID == arg && c.Name != "" {
				name = c.Name
			}
		}
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "Remove this calendar?",
			subtitle: name,
			icon:     "trash",
			body:     "Its events leave the handheld's calendar. The calendar itself is not touched: add the link again any time.",
			buttons: []tbxSheetButton{
				{label: "Keep"},
				{label: "Remove", primary: true, run: func() {
					mailcalScreen.nav.focus = ""
					mailcalRemoveAsync(func() string { return mailcalRemoveCalendar(arg) })
				}},
			},
		})
	case "add-cal":
		PlayActivateSound()
		mailcalCalendarFromPhone()
	case "poll":
		PlayToggleSound()
		next := mailcalNextChoice(mailcalPollChoices[:], s.poll())
		mailcalSave(func(s *mailcalSettings) { s.PollMinutes = next }, nil)
	case "notify":
		PlayToggleSound()
		mailcalSave(func(s *mailcalSettings) { s.NoMailNotify = !s.NoMailNotify }, nil)
	case "remind":
		PlayToggleSound()
		mailcalSave(func(s *mailcalSettings) { s.NoReminders = !s.NoReminders }, nil)
	case "lead":
		if s.NoReminders {
			ShowToast("Turn Event reminders on first", ToastKindInfo)
			return
		}
		PlayToggleSound()
		next := mailcalNextChoice(mailcalReminderChoices[:], s.reminder())
		mailcalSave(func(s *mailcalSettings) { s.ReminderMinutes = next }, nil)
	case "about":
		PlayActivateSound()
		mailcalOpenHelp(config)
	}
}

// mailcalOpenHelp is the "How to set it up" sheet.
func mailcalOpenHelp(config *Config) {
	body := "## Mail\n" +
		"• Gmail, Yahoo, iCloud, AOL, Fastmail, Zoho and Yandex want an app password, made in your account's security settings — the phone card says where when you type your address.\n" +
		"• Outlook, Hotmail and Live sign in with Microsoft: choose Sign in with Microsoft and approve it on your phone.\n" +
		"• Any other provider works with its IMAP and SMTP servers, under Advanced on the phone.\n\n" +
		"## Calendars\n" +
		"• iCloud, Yahoo, Fastmail, Zoho and Yandex bring their calendar with the account.\n" +
		"• Google: on a computer, Google Calendar → Settings → your calendar → Secret address in iCal format. Add that link.\n" +
		"• Outlook: Settings → Calendar → Shared calendars → Publish a calendar → ICS link.\n" +
		"• Events show in the clock's calendar and on the Calendar widget; the handheld does not change them.\n\n" +
		"## What the handheld keeps\n" +
		"Passwords, sign-ins and calendar links are stored encrypted on this device and never shown again, to the phone either. Remove an account here and it is forgotten."
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Email & calendar", subtitle: "Setting it up", icon: "mail", body: body,
		qr: tbxQRModules(mailcalDocURL), qrLabel: "EMAIL_CALENDAR.md",
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// ── Home, the menus and the calendar widget ─────────────────────────────

// mailcalHubTile is the Tools hub's tile: the unread count, a source that
// needs a look, or what the page is for.
func mailcalHubTile() toolsHubTile {
	t := toolsHubTile{id: "mailcal", label: "Email & calendar", icon: "mail",
		value: "Your mail and calendars — set up from your phone"}
	s := mailcalSettingsGet()
	if len(s.Accounts)+len(s.Calendars) == 0 {
		return t
	}
	t.value, t.tone = pluralizeCount(len(s.Accounts), "account", "accounts")+" · "+pluralizeCount(len(s.Calendars), "calendar", "calendars"), toolsHubToneGood
	if n := mailUnreadTotal(); n > 0 {
		t.value = chatUnreadSubtitle(n)
	}
	return t
}

// mailcalOpenCalendar shows the clock's calendar, where synced events are
// listed (calendarpopover.go). Main thread.
func mailcalOpenCalendar() {
	if !homeCalendarOpen {
		toggleCalendarPopover()
	}
}

// homeItemOpensMail reports whether a Home tile is the Mail app pinned from
// the Start menu.
func homeItemOpensMail(item HomeItem) bool {
	return strings.EqualFold(string(item.ID), homeTileActionPrefix+mailcalAppEntryName)
}

// homeMailTileUnread is homeChatTileUnread for the Mail tile: the unread
// count, and "N new messages" under its name while there are some.
func homeMailTileUnread(item HomeItem) (HomeItem, int) {
	if !homeItemOpensMail(item) {
		return item, 0
	}
	n := mailUnreadTotal()
	if n > 0 {
		item.Subtitle = chatUnreadSubtitle(n)
	}
	return item, n
}

// mailcalNextEvent is the Calendar widget's line about the next event, asked
// of the calendar sync at most every 30 s. Render thread only.
var mailcalNextEvent struct {
	at   time.Time
	text string
}

// mailcalNextEventLine is "14:30 · Team call", "Tomorrow 9:00 · Dentist" or
// "Fri · Holiday" for the next event within a week, "" when there is none (and
// always in screenshot mode, which never shows the user's calendar).
func mailcalNextEventLine(now time.Time) string {
	if shotModeOn() {
		return ""
	}
	c := &mailcalNextEvent
	if !c.at.IsZero() && now.Sub(c.at) < 30*time.Second && !now.Before(c.at) {
		return c.text
	}
	c.at, c.text = now, ""
	ev, ok := calNextEvent(now)
	if !ok || ev.Start.IsZero() || ev.Start.After(now.Add(7*24*time.Hour)) {
		return ""
	}
	// The title comes from someone else's calendar: no line breaks or control
	// characters may reach the widget.
	title := calSafeText(ev.Title, 60)
	if title == "" {
		title = "Event"
	}
	c.text = mailcalEventWhen(appConfig, ev, now) + " · " + title
	return c.text
}

// mailcalEventWhen says when an event starts, relative to now: "Now", "14:30",
// "Tomorrow 9:00", "Fri 9:00"; all-day events by day alone. The day words,
// weekdays and the clock are the interface language's and the user's
// (clockformat.go).
func mailcalEventWhen(config *Config, ev calEvent, now time.Time) string {
	start := ev.Start.In(now.Location())
	if !ev.AllDay && !start.After(now) {
		return "Now"
	}
	clock := clockDigits(config, start)
	l := dateLocaleNow()
	ny, nm, nd := now.Date()
	today := time.Date(ny, nm, nd, 0, 0, 0, 0, now.Location())
	sy, sm, sd := start.Date()
	day := time.Date(sy, sm, sd, 0, 0, 0, 0, now.Location())
	switch days := int(day.Sub(today).Hours()/24 + 0.5); {
	case days <= 0 && ev.AllDay:
		return l.word(0)
	case days <= 0:
		return clock
	case days == 1 && ev.AllDay:
		return l.word(1)
	case days == 1:
		return l.word(1) + " " + clock
	case ev.AllDay:
		return formatWeekdayShort(start)
	default:
		return formatWeekdayShort(start) + " " + clock
	}
}
