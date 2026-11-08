package main

// hoffcard.go — the glass card Home shows for a handoff ("Continue <title>?")
// and, while a pairing request waits for this user, for the code to compare.
//
// It is self-contained: home.go draws it with one call after the layout, and
// controller.go and main.go hand it the pad, the keys and the clicks while it
// is on screen. It is modal over Home only and never acts on its own: A
// continues (or confirms the code), B puts it away.

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type hoffCardKind int

const (
	hoffCardNone hoffCardKind = iota
	hoffCardOffer
	hoffCardPair
)

// hoffCard is the card's state. focus 0 is the secondary button ("Not now"),
// 1 the primary one.
var hoffCard struct {
	kind    hoffCardKind
	offer   hoffOffer
	pair    peerPairView
	sig     string
	focus   int
	checkAt time.Time
	rect    sdl.Rect
	buttons [2]sdl.Rect
	wrap    tbxWrapCache
}

// hoffCardBlocked reports whether something else owns Home right now: a
// panel, a menu, the keyboard, the tour, the idle screen or a ringing alarm.
func hoffCardBlocked() bool {
	return !homeLayoutActive || transitionPhase != "none" || virtualKeyboardActive ||
		modalPanelBlocksShoulders() || tb.startOpen || nc.open || qs.open || globalSearchOpen ||
		homeCalendarOpen || shortcutsOpen || clipSearchOpen || controlsLegend.open || wpOpen ||
		socialNotifOpen || onboarding.active || screensaverActiveNow() || alarmFiringActive()
}

// hoffCardRefresh decides what the card shows, a few times a second.
func hoffCardRefresh(now time.Time) {
	if now.Sub(hoffCard.checkAt) < 250*time.Millisecond {
		return
	}
	hoffCard.checkAt = now
	in, _ := peerPairViews()
	if in.live && in.stage == peerPairCompare && !in.localOK {
		hoffCard.kind, hoffCard.pair = hoffCardPair, in
		hoffCardSetSig("pair:" + strconv.FormatInt(in.token, 10))
		return
	}
	if o, ok := hoffPendingOffer(now); ok {
		hoffCard.kind, hoffCard.offer = hoffCardOffer, o
		hoffCardSetSig("offer:" + hoffOfferKey(o))
		return
	}
	hoffCard.kind, hoffCard.sig = hoffCardNone, ""
}

// hoffCardSetSig resets the focus to the primary button for a new card.
func hoffCardSetSig(sig string) {
	if hoffCard.sig == sig {
		return
	}
	hoffCard.sig = sig
	hoffCard.focus = 1
}

// hoffCardVisible reports whether the card is up and owns the input.
func hoffCardVisible() bool {
	return hoffCard.kind != hoffCardNone && !hoffCardBlocked()
}

// hoffCardText is what the card says, and its two buttons.
func hoffCardText(now time.Time) (title, subtitle, icon, body, primary, secondary string) {
	if hoffCard.kind == hoffCardPair {
		v := hoffCard.pair
		code := peerFormatCode(v.code)
		body = "Check that " + v.name + " shows the same code, " + code + ", and choose Codes match there too. "
		body += "Only confirm if you started pairing on " + v.name + "."
		return "Pair with " + v.name + "?", "Code " + code, "devices", body, "Codes match", "Cancel"
	}
	o := hoffCard.offer
	a := o.Activity
	title = "Continue " + a.Title + "?"
	subtitle = "From " + o.PeerName + " · " + peerAgo(now, o.When)
	if o.Sent {
		subtitle = o.PeerName + " sent this " + peerAgo(now, o.When)
	}
	icon = hoffKindIcon(a.Kind)
	detail := hoffKindLabel(a.Kind)
	if a.System != "" {
		detail += " · " + a.System
	}
	if a.Position > 1 {
		detail += " · at " + partyClock(a.Position)
	}
	switch a.Kind {
	case "game":
		body = detail + ". Its newest saves come over from " + o.PeerName + " before it starts."
	case "library":
		body = detail + ". Your library opens on it."
	case "web":
		body = detail + " · " + a.URL
	default:
		body = detail + "."
	}
	return title, subtitle, icon, body, "Continue", "Not now"
}

// hoffRenderCard draws the card over Home. home.go calls it after the layout.
func hoffRenderCard(renderer *sdl.Renderer, config *Config) {
	hoffCard.rect = sdl.Rect{}
	if renderer == nil || config == nil {
		return
	}
	now := time.Now()
	hoffCardRefresh(now)
	if !hoffCardVisible() {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	title, subtitle, icon, body, primary, secondary := hoffCardText(now)
	cardW := int32(760)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	header := UIPanelHeader(title, subtitle, icon)
	headerH := UIPanelHeaderBand(config, header, 8)
	lines := hoffCard.wrap.get(fonts.body, body, cardW-2*OverlayPadX)
	if len(lines) > 4 {
		lines = lines[:4]
	}
	lineH := fonts.bodyH + 4
	btnH := int32(46)
	cardH := headerH + int32(len(lines))*lineH + 20 + btnH + 22
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	hoffCard.rect = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	y := card.Y + headerH
	for _, line := range lines {
		renderText(renderer, config, fonts.body, line, ColorTextSecondary(), card.X+OverlayPadX, y)
		y += lineH
	}

	// The buttons sit right-aligned along the bottom, the primary last, the
	// way the sheets put them.
	by := card.Y + card.H - btnH - 22
	right := card.X + card.W - OverlayPadX
	pw := tbxButtonWidth(fonts.body, primary, 160)
	sw := tbxButtonWidth(fonts.body, secondary, 130)
	hoffCard.buttons[1] = sdl.Rect{X: right - pw, Y: by, W: pw, H: btnH}
	hoffCard.buttons[0] = sdl.Rect{X: right - pw - 12 - sw, Y: by, W: sw, H: btnH}
	tbxDrawButton(renderer, config, fonts.body, hoffCard.buttons[0], secondary, false, hoffCard.focus == 0, false)
	tbxDrawButton(renderer, config, fonts.body, hoffCard.buttons[1], primary, true, hoffCard.focus == 1, false)
	keys := "A " + primary + "   B " + secondary
	if hoffCard.focus == 0 {
		keys = "A " + secondary + "   B " + secondary
	}
	if room := hoffCard.buttons[0].X - card.X - OverlayPadX - 16; room > 60 {
		_, kh := measureText(fonts.small, keys)
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, keys, room), ColorTextTertiary(),
			card.X+OverlayPadX, by+(btnH-kh)/2)
	}
}

// hoffCardActivate presses a button: 1 the primary (Continue, Codes match),
// 0 the secondary (Not now, Cancel).
func hoffCardActivate(config *Config, button int) {
	kind, offer, pair := hoffCard.kind, hoffCard.offer, hoffCard.pair
	hoffCard.kind, hoffCard.sig = hoffCardNone, ""
	hoffCard.checkAt = time.Time{}
	switch kind {
	case hoffCardPair:
		if button == 1 {
			PlayActivateSound()
			peerPairConfirmView(pair)
			return
		}
		PlayBackSound()
		peerPairCancelView(pair)
	case hoffCardOffer:
		if button == 1 {
			PlayActivateSound()
			hoffResume(config, offer)
			return
		}
		PlayBackSound()
		hoffDismiss(offer)
	}
}

// hoffCardController is the pad while the card is up (controller.go). Start
// still opens the Start menu, which hides the card under it.
func hoffCardController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !hoffCardVisible() {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if hoffCard.focus != 0 {
			hoffCard.focus = 0
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if hoffCard.focus != 1 {
			hoffCard.focus = 1
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		hoffCardActivate(config, hoffCard.focus)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		hoffCardActivate(config, 0)
	case sdl.CONTROLLER_BUTTON_START:
		return false
	}
	return true
}

// hoffCardKey is the keyboard while the card is up (main.go).
func hoffCardKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !hoffCardVisible() {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		if hoffCard.focus != 0 {
			hoffCard.focus = 0
			PlayNavSound()
		}
	case sdl.K_RIGHT:
		if hoffCard.focus != 1 {
			hoffCard.focus = 1
			PlayNavSound()
		}
	case sdl.K_TAB:
		hoffCard.focus = 1 - hoffCard.focus
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		hoffCardActivate(config, hoffCard.focus)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		hoffCardActivate(config, 0)
	}
	return true
}

// hoffCardClick is a click or tap while the card is up (main.go): a button
// presses; anywhere else does nothing, because the card is modal.
func hoffCardClick(mx, my int32, config *Config) bool {
	if !hoffCardVisible() || hoffCard.rect.W == 0 {
		return false
	}
	for i, r := range hoffCard.buttons {
		if r.W > 0 && pointInRect(mx, my, r) {
			hoffCard.focus = i
			hoffCardActivate(config, i)
			return true
		}
	}
	return true
}
