package main

// mail_compose.go — writing a message in the Mail app (docs/EMAIL_CALENDAR.md).
//
// A short form: From (the account; A switches when there are several), To,
// Cc, Subject and the message, each typed on the on-screen keyboard. The
// keyboard edits its own buffer (mailComposeVKTarget) and a field changes only
// on Done, so B on the keyboard leaves the field as it was. Start sends, as
// it does in Chat; a keyboard sends with Ctrl+Enter.
//
// A reply is addressed to the sender (Reply-To when there is one), keeps the
// thread's Message-IDs, and carries the original below the new text as
// "On <date>, <name> wrote:" with "> " lines — at most 80 lines of it — which
// a switch can leave out. The on-screen keyboard has no line break, so the
// message is one paragraph; the quote keeps its own lines.

import (
	"context"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mailComposeVKTarget aims the on-screen keyboard at a compose field.
const mailComposeVKTarget vkTargetKind = 151

// mailComposeBuffer holds the field being typed; it is applied on Done only.
var mailComposeBuffer string

// mailCompose is the message being written.
type mailCompose struct {
	gen        int
	from       string // account ID
	to         string
	cc         string
	subject    string
	body       string
	quote      string
	withQuote  bool
	inReplyTo  string
	references string
	reply      bool
	editing    string // the field the keyboard edits
	sending    bool
	dirty      bool
	back       mailView
	wrap       tbxWrapCache
	quoteWrap  tbxWrapCache
}

// mailComposeStart opens the form with c, keeping the generation counter.
func mailComposeStart(c mailCompose, focus string) {
	c.gen = mailUI.compose.gen + 1
	c.back = mailUI.view
	if c.back == mailViewCompose {
		c.back = mailViewList
	}
	mailUI.compose = c
	mailUI.view = mailViewCompose
	mailUI.composeNav.focus, mailUI.composeNav.scroll = focus, 0
	PlayActivateSound()
}

// mailComposeNew starts a new message from the account on the chip.
func mailComposeNew(config *Config) {
	from := mailUI.account
	if from == "" && len(mailUI.accounts) > 0 {
		from = mailUI.accounts[0].ID
	}
	if from == "" {
		ShowToast("Add a mail account first", ToastKindWarning)
		return
	}
	mailComposeStart(mailCompose{from: from}, "to")
}

// mailExtractAddrs finds the e-mail addresses in a display string.
func mailExtractAddrs(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '<' || r == '>' || r == '"' || r == '(' || r == ')'
	}) {
		if at := strings.LastIndexByte(f, '@'); at > 0 && strings.Contains(f[at:], ".") {
			out = append(out, f)
		}
	}
	return out
}

// mailQuoteText is the original message as a reply quotes it.
func mailQuoteText(date time.Time, who, text string) string {
	head := who + " wrote:"
	if !date.IsZero() {
		head = "On " + date.Local().Format("Mon, Jan 2, 2006 at 15:04") + ", " + who + " wrote:"
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte('\n')
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	size := 0
	for i, l := range lines {
		if i >= 80 || size > 8000 {
			b.WriteString("> […]\n")
			break
		}
		if strings.HasPrefix(l, "[This message is larger") {
			continue
		}
		if l == "" {
			b.WriteString(">\n")
		} else {
			b.WriteString("> " + l + "\n")
		}
		size += len(l)
	}
	return strings.TrimRight(b.String(), "\n")
}

// mailReplyOpen answers the open message (all: everyone it went to).
func mailReplyOpen(config *Config, all bool) {
	r := mailUI.open
	m := r.msg
	to := setOr(m.ReplyTo, m.FromAddr)
	if to == "" {
		ShowToast("This message has no address to answer", ToastKindWarning)
		return
	}
	c := mailCompose{from: r.acct.ID, to: to, reply: true, withQuote: true}
	if all {
		seen := map[string]bool{strings.ToLower(to): true, strings.ToLower(strings.TrimSpace(r.acct.Email)): true}
		var cc []string
		for _, addr := range mailExtractAddrs(m.To + ", " + m.Cc) {
			if key := strings.ToLower(addr); !seen[key] {
				seen[key] = true
				cc = append(cc, addr)
			}
		}
		c.cc = strings.Join(cc, ", ")
	}
	subject := strings.TrimSpace(m.Subject)
	if low := strings.ToLower(subject); !strings.HasPrefix(low, "re:") {
		subject = "Re: " + subject
	}
	c.subject = strings.TrimSpace(subject)
	c.inReplyTo = m.MessageID
	c.references = strings.TrimSpace(m.References + " " + m.MessageID)
	text := m.Snippet
	if mailUI.entry != nil && !mailUI.loading {
		text = mailUI.entry.body.Text
	}
	c.quote = mailQuoteText(m.Date, m.sender(), text)
	mailComposeStart(c, "body")
}

// ── Fields and the keyboard ─────────────────────────────────────────────

// mailComposeField is a field's text.
func mailComposeField(field string) string {
	c := &mailUI.compose
	switch field {
	case "to":
		return c.to
	case "cc":
		return c.cc
	case "subject":
		return c.subject
	case "body":
		return c.body
	}
	return ""
}

// mailComposeEdit opens the keyboard on a field.
func mailComposeEdit(field string) {
	chatInputActive = false
	unitInputActive = false
	vkTarget = mailComposeVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	mailUI.compose.editing = field
	mailComposeBuffer = mailComposeField(field)
	vkTextSelected = false
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// mailComposeSubmit is the keyboard's Done: the field takes the text and the
// focus moves on to the next empty field.
func mailComposeSubmit(text string) {
	c := &mailUI.compose
	field := c.editing
	c.editing = ""
	if field == "" || mailUI.view != mailViewCompose {
		return
	}
	if field != "body" {
		text = strings.Join(strings.Fields(text), " ")
	}
	switch field {
	case "to":
		c.to = text
	case "cc":
		c.cc = text
	case "subject":
		c.subject = text
	case "body":
		c.body = text
	}
	c.dirty = true
	next := map[string]string{"to": "subject", "cc": "subject", "subject": "body", "body": "send"}[field]
	if next != "" && (next == "send" || mailComposeField(next) == "") {
		mailUI.composeNav.setFocus(next)
	}
}

// mailComposeKeyboardTitle names the field on the keyboard.
func mailComposeKeyboardTitle() string {
	switch mailUI.compose.editing {
	case "to":
		return "TO"
	case "cc":
		return "CC"
	case "subject":
		return "SUBJECT"
	}
	return "MESSAGE"
}

// mailComposeKeyboardHint is the empty field's placeholder.
func mailComposeKeyboardHint() string {
	switch mailUI.compose.editing {
	case "to", "cc":
		return "name@example.com, another@example.com"
	case "subject":
		return "What it is about"
	}
	return "Write your message"
}

// mailComposeProse reports a field of sentences (auto-capitalised).
func mailComposeProse() bool {
	f := mailUI.compose.editing
	return f == "subject" || f == "body"
}

// mailStartSends lets Start send the message being written. It runs before
// the Start menu opens (handleControllerEvent); anywhere else in Mail it
// declines and Start opens the menu as on every screen.
func mailStartSends(config *Config) bool {
	if tbxCurrentScene(config) != mailSceneName || mailUI.view != mailViewCompose || virtualKeyboardActive ||
		mailUI.menuOpen || tbxSheetVisible(config) {
		return false
	}
	mailComposeSend(config)
	return true
}

// ── Send and discard ────────────────────────────────────────────────────

// mailComposeSend sends the message on a goroutine; the form stays until it
// went, so a failure loses nothing.
func mailComposeSend(config *Config) {
	c := &mailUI.compose
	if c.sending {
		return
	}
	a, ok := mailcalSettingsGet().account(c.from)
	switch {
	case !ok:
		ShowToast("Choose an account to send from", ToastKindWarning)
		return
	case strings.TrimSpace(c.to) == "":
		// Cc alone is refused too: a message addressed only in copy is
		// nearly always a slip of the D-pad onto the wrong row.
		ShowToast("Add who the message is for in To", ToastKindWarning)
		mailUI.composeNav.setFocus("to")
		return
	case strings.TrimSpace(c.body) == "" && strings.TrimSpace(c.subject) == "":
		ShowToast("Write something first", ToastKindWarning)
		mailUI.composeNav.setFocus("body")
		return
	case !IsOnline():
		ShowToast("Offline — connect to Wi-Fi, then send again", ToastKindWarning)
		return
	}
	out := mailOutgoing{To: c.to, Cc: c.cc, Subject: c.subject, Body: c.body, InReplyTo: c.inReplyTo, References: c.references}
	if c.withQuote && c.quote != "" {
		out.Body = strings.TrimRight(c.body, "\n") + "\n\n" + c.quote
	}
	c.sending = true
	gen := c.gen
	PlayActivateSound()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := mailSend(ctx, a, out)
		cancel()
		tbxPost(func() {
			cur := &mailUI.compose
			if cur.gen == gen {
				cur.sending = false
			}
			if err != nil {
				PlayErrorSound()
				ShowToast("Not sent: "+mailErrorText(err, a), ToastKindWarning)
				return
			}
			PlaySuccessSound()
			ShowToast("Sent", ToastKindSuccess)
			if cur.gen == gen && mailUI.view == mailViewCompose {
				back := cur.back
				mailUI.compose = mailCompose{gen: gen}
				mailUI.view = back
			}
		})
	}()
}

// mailComposeDiscard leaves the form, asking first when something was typed.
func mailComposeDiscard() {
	leave := func() {
		back := mailUI.compose.back
		mailUI.compose = mailCompose{gen: mailUI.compose.gen}
		mailUI.view = back
	}
	if mailUI.compose.sending {
		// The form stays until the server answered, so a failure still has
		// the text to send again; discarding now would lose it on an error.
		PlayErrorSound()
		ShowToast("Sending — a moment", ToastKindInfo)
		return
	}
	if !mailUI.compose.dirty {
		PlayBackSound()
		leave()
		return
	}
	tbxOpenSheet(appConfig, tbxSheetSpec{
		title: "Discard this message?",
		icon:  "trash",
		body:  "What you typed is not kept anywhere once it is discarded.",
		buttons: []tbxSheetButton{
			{label: "Keep writing"},
			{label: "Discard", primary: true, run: leave},
		},
	})
}

// ── Drawing and input ───────────────────────────────────────────────────

func mailRenderCompose(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	c := &mailUI.compose
	title := "New message"
	if c.reply {
		title = "Reply"
	}
	a, _ := mailcalSettingsGet().account(c.from)
	status := ""
	if c.sending {
		status = "Sending…"
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader(title, a.label(), "edit", status, glassAccent()))
	body := frame.bodyRect()
	nav := &mailUI.composeNav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	field := func(id, icon, label, value, empty string) {
		spec := tbxRowSpec{icon: icon, title: label, subtitle: value}
		if value == "" {
			spec.subtitle, spec.dim = empty, true
		}
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, peerScr(rect, top), spec, nav.focused(id))
		nav.add(id, rect, row, false)
		row++
		y += rowH + 8
	}
	from := tbxRowSpec{icon: "user", title: "From", subtitle: a.label()}
	if len(mailUI.accounts) > 1 {
		from.pill, from.tone = "Change", tbxToneNeutral
	}
	rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	tbxDrawRow(renderer, config, fonts, peerScr(rect, top), from, nav.focused("from"))
	nav.add("from", rect, row, false)
	row++
	y += rowH + 8
	field("to", "users", "To", c.to, "Who it goes to")
	field("cc", "users", "Cc", c.cc, "Optional")
	field("subject", "text", "Subject", c.subject, "What it is about")

	// The message: a card with the wrapped text.
	lines := c.wrap.get(fonts.body, setOr(c.body, "Write your message"), w-40)
	if len(lines) > 10 {
		lines = append(lines[:9:9], "…")
	}
	lineH := fonts.bodyH + 4
	cardH := max(int32(len(lines))*lineH+fonts.smallH+34, rowH*2)
	card := sdl.Rect{X: x, Y: y, W: w, H: cardH}
	scr := peerScr(card, top)
	UIListRow(renderer, scr, glassRowRadius, nav.focused("body"), false)
	renderText(renderer, config, fonts.small, "Message", ColorTextTertiary(), scr.X+20, scr.Y+12)
	ly := scr.Y + 16 + fonts.smallH
	ink := ColorTextPrimary()
	if c.body == "" {
		ink = ColorTextTertiary()
	}
	for _, l := range lines {
		mailText(renderer, config, fonts.body, l, ink, scr.X+20, ly)
		ly += lineH
	}
	nav.add("body", card, row, false)
	row++
	y += cardH + 8

	if c.quote != "" {
		r := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		bugrepDrawToggleRow(renderer, config, fonts, peerScr(r, top), "Include the original message", "Quoted below what you write", c.withQuote, nav.focused("quote"))
		nav.add("quote", r, row, false)
		row++
		y += rowH + 8
		if c.withQuote {
			q := c.quoteWrap.get(fonts.small, c.quote, w-24)
			if len(q) > 8 {
				q = append(q[:7:7], "> …")
			}
			for _, l := range q {
				mailText(renderer, config, fonts.small, l, ColorTextTertiary(), x+12, top+y)
				y += fonts.smallH + 3
			}
			y += 12
		}
	}

	// Send and Discard.
	bx := x
	for _, b := range []struct {
		id, label string
		primary   bool
	}{{"send", "Send", true}, {"discard", "Discard", false}} {
		label := b.label
		if b.id == "send" && c.sending {
			label = "Sending…"
		}
		bw := tbxButtonWidth(fonts.body, label, 160)
		br := sdl.Rect{X: bx, Y: y, W: bw, H: 48}
		tbxDrawButton(renderer, config, fonts.body, peerScr(br, top), label, b.primary, nav.focused(b.id), c.sending)
		nav.add(b.id, br, row, false)
		bx += bw + 12
	}
	y += 48 + 20
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func mailComposeAction(config *Config, act tbxAction) bool {
	nav := &mailUI.composeNav
	c := &mailUI.compose
	switch act {
	case tbxActConfirm:
		switch f := nav.focus; f {
		case "from":
			if len(mailUI.accounts) > 1 {
				PlayToggleSound()
				i := 0
				for k, a := range mailUI.accounts {
					if a.ID == c.from {
						i = k + 1
					}
				}
				c.from = mailUI.accounts[i%len(mailUI.accounts)].ID
			}
		case "to", "cc", "subject", "body":
			if c.sending {
				return true
			}
			PlayActivateSound()
			mailComposeEdit(f)
		case "quote":
			PlayToggleSound()
			c.withQuote = !c.withQuote
		case "send":
			mailComposeSend(config)
		case "discard":
			mailComposeDiscard()
		}
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		if c.sending {
			// B leaves Mail while the message is on its way; the form waits
			// in Mail and closes itself once it went (or keeps the text when
			// it did not). goBackScene plays the sound.
			return false
		}
		mailComposeDiscard()
		return true
	}
	return tbxNavAction(nav, act)
}

// mailComposeHints is the compose footer.
func mailComposeHints() []FooterHint {
	label := "Edit"
	switch mailUI.composeNav.focus {
	case "from":
		label = "Change"
	case "quote":
		label = "Toggle"
	case "send":
		label = "Send"
	case "discard":
		label = "Discard"
	}
	if mailUI.compose.sending {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label},
		{Button: "Start / Ctrl+Enter", Label: "Send"}, {Button: "Select / M", Label: "Menu"}, {Button: "B", Label: "Discard"}}
}
