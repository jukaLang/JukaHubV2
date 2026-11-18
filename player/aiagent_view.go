package main

// aiagent_view.go — what the agent adds to Chat → Juka AI:
//
//   - a strip of chips right above the composer: starter questions (typing
//     is hard on a handheld), and after an answer up to three follow-ups the
//     model suggested plus an "Open …" button when Juka pointed at a screen.
//     Up from the composer reaches the strip, Left/Right walk it, A asks /
//     opens, Up again goes on to the history;
//   - the confirmation card a change waits on, in the strip's place:
//     A Allow, B Deny, Y Allow for this conversation (not offered for a
//     destructive step). Keyboard: Enter, Esc, Y. Mouse: its buttons;
//   - R2 (keyboard: M) on the tab: speak a question (voice typing into the
//     composer; Start sends it).

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// aiStarters are the chips of a fresh conversation.
var aiStarters = []string{
	"Why won't my last game start?",
	"Free up space",
	"Make games run smoother",
	"Check my Wi-Fi",
	"What's draining battery?",
	"Clean up duplicate ROMs",
}

// aiChip is one chip: a question to ask, or a screen to open.
type aiChip struct {
	label  string
	prompt string
	open   func(c *Config)
}

// aiStripFocus is the chip the pad is on (-1: none). Main thread.
var aiStripFocus = -1

// aiStripFirst is the first chip drawn (the strip scrolls to keep the focus
// in view). Main thread.
var aiStripFirst = 0

// aiStripCache rebuilds the chips only when the conversation or the chip
// state changed.
var aiStripCache struct {
	lastID  string
	gen     uint64
	pending bool
	chips   []aiChip
}

// aiChipGen counts aiSetChips calls (aiChipState.mu guards it).
var aiChipGen uint64

// aiStripHits are the last frame's hit boxes.
var aiStripHits struct {
	chips   []sdl.Rect
	allow   sdl.Rect
	always  sdl.Rect
	deny    sdl.Rect
	visible bool
}

// aiStripChips is the chips to show now. Main thread.
func aiStripChips() []aiChip {
	chatMutex.Lock()
	list := *chatTabSliceLocked(chatTabAI)
	n := len(list)
	lastID, prevID, lastAI := "", "", false
	if n > 0 {
		lastID, lastAI = list[n-1].ID, chatIsAI(list[n-1])
		if n > 1 {
			prevID = list[n-2].ID
		}
	}
	pending := chatStream.active
	chatMutex.Unlock()

	aiChipState.mu.Lock()
	gen := aiChipGen
	c := &aiStripCache
	if c.chips != nil && c.lastID == lastID && c.gen == gen && c.pending == pending {
		aiChipState.mu.Unlock()
		return c.chips
	}
	chips := make([]aiChip, 0, 8)
	if !pending {
		if lastAI && prevID != "" && prevID == aiChipState.promptID &&
			(len(aiChipState.followups) > 0 || aiChipState.open != nil) {
			if aiChipState.open != nil {
				chips = append(chips, aiChip{label: aiChipState.openLabel, open: aiChipState.open})
			}
			for _, f := range aiChipState.followups {
				chips = append(chips, aiChip{label: f, prompt: f})
			}
		} else {
			for _, s := range aiStarters {
				chips = append(chips, aiChip{label: s, prompt: s})
			}
		}
	}
	aiChipState.mu.Unlock()
	c.lastID, c.gen, c.pending, c.chips = lastID, gen, pending, chips
	if aiStripFocus >= len(chips) {
		aiStripFocus = len(chips) - 1
	}
	return chips
}

// aiStripShown reports whether the strip (or the card) takes room on the
// Juka AI tab. Main thread.
func aiStripShown() bool {
	if chatActiveTabID() != chatTabAI {
		return false
	}
	if _, _, ok := aiConfirmPending(); ok {
		return true
	}
	return aiToolsOn() && len(aiStripChips()) > 0
}

// aiStripHeight is the room the strip needs. The confirmation card grows with
// its wrapped text (aiConfirmWrapped), measured with the fonts and width of
// its last draw: before the session's first card was drawn they are unknown,
// and that one frame gets the old fixed height.
func aiStripHeight() int32 {
	text, _, ok := aiConfirmPending()
	if !ok {
		return chatPx(38)
	}
	c := &aiConfirmWrap
	if c.body == nil || c.meta == nil || c.w <= 0 {
		return chatPx(96)
	}
	ask, cmd := aiConfirmWrapped(c.body, c.meta, text, c.w)
	metaH, bodyH := int32(c.meta.Height()), int32(c.body.Height())
	return chatPx(10) + metaH + chatPx(2) + int32(len(ask))*bodyH + int32(len(cmd))*metaH +
		chatPx(8) + chatPx(32) + chatPx(8)
}

// The confirmation card's two parts are each wrapped over a bounded number
// of rows, the last one ending in an ellipsis when the text is longer.
const (
	aiConfirmAskRows = 2 // the question, in the body font
	aiConfirmCmdRows = 3 // what follows it (a command, a list), in the meta font
)

// aiConfirmWrap is the card's text wrapped to its width, wrapped again only
// when the text, the width or a font changes. Main thread.
var aiConfirmWrap struct {
	text       string
	w          int32
	body, meta *ttf.Font
	ask, cmd   []string
}

// aiConfirmWrapped is the card's text as rows: its first line (the question)
// in body, the rest (a command or details, their line breaks read as spaces)
// in meta. A long command is wrapped, not cut at the card's width: what is
// allowed should be readable.
func aiConfirmWrapped(body, meta *ttf.Font, text string, w int32) (ask, cmd []string) {
	c := &aiConfirmWrap
	if c.ask != nil && text == c.text && w == c.w && body == c.body && meta == c.meta {
		return c.ask, c.cmd
	}
	c.text, c.w, c.body, c.meta = text, w, body, meta
	first, rest, hasRest := strings.Cut(text, "\n")
	c.ask = aiWrapBounded(body, uiT(first), w, aiConfirmAskRows)
	c.cmd = nil
	if hasRest {
		c.cmd = aiWrapBounded(meta, strings.ReplaceAll(rest, "\n", " "), w, aiConfirmCmdRows)
	}
	return c.ask, c.cmd
}

// aiWrapBounded wraps text at w over at most limit rows; when it needs more,
// the last row ends in an ellipsis.
func aiWrapBounded(font *ttf.Font, text string, w int32, limit int) []string {
	rows := termWrap(font, text, w)
	if len(rows) <= limit {
		return rows
	}
	out := make([]string, limit)
	copy(out, rows[:limit-1])
	out[limit-1] = ellipsize(font, rows[limit-1]+" …", w)
	return out
}

// aiDrawStrip draws the card or the chips into rect. Main thread.
func aiDrawStrip(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect) {
	aiStripHits.chips = aiStripHits.chips[:0]
	aiStripHits.allow, aiStripHits.always, aiStripHits.deny = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	aiStripHits.visible = true
	if chatSelected >= 0 || chatTrayFocus >= 0 {
		aiStripFocus = -1
	}
	if text, destructive, ok := aiConfirmPending(); ok {
		aiStripFocus = -1
		aiDrawConfirm(renderer, config, fonts, rect, text, destructive)
		return
	}
	chips := aiStripChips()
	if len(chips) == 0 {
		aiStripFocus = -1
		return
	}
	h := rect.H
	gap := chatPx(8)
	pad := chatPx(16)
	if aiStripFocus >= 0 && aiStripFocus < aiStripFirst {
		aiStripFirst = aiStripFocus
	}
	if aiStripFirst >= len(chips) {
		aiStripFirst = 0
	}
	// Scroll right until the focused chip fits.
	for aiStripFocus > aiStripFirst {
		x := rect.X
		for k := aiStripFirst; k <= aiStripFocus; k++ {
			lw, _ := measureText(fonts.meta, chips[k].label)
			x += lw + 2*pad + gap
		}
		if x-gap <= rect.X+rect.W {
			break
		}
		aiStripFirst++
	}
	x := rect.X
	accent := glassAccent()
	for k := aiStripFirst; k < len(chips); k++ {
		ch := chips[k]
		lw, _ := measureText(fonts.meta, ch.label)
		w := lw + 2*pad
		if x+w > rect.X+rect.W {
			if k == aiStripFirst {
				w = rect.X + rect.W - x
			} else {
				break
			}
		}
		r := sdl.Rect{X: x, Y: rect.Y, W: w, H: h}
		focused := aiStripFocus == k && chatPadOnContent() && chatWasVisible
		if focused {
			drawGlassFocusGlow(renderer, r)
		}
		ink := ColorTextPrimary()
		if ch.open != nil {
			chatFillRound(renderer, r, h/2, accent)
			ink = contrastText(accent)
		} else {
			chatDrawChip(renderer, r, chatHover(r))
		}
		if focused {
			drawGlassFocusRing(renderer, r, h/2)
		}
		label := ch.label
		if lw > w-2*pad {
			label = ellipsize(fonts.meta, label, w-2*pad)
		}
		chatTextCentered(renderer, config, fonts.meta, label, ink, r.X+pad, r)
		aiStripHits.chips = append(aiStripHits.chips, r)
		x += w + gap
	}
}

// aiDrawConfirm draws the card a change waits on.
func aiDrawConfirm(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect, text string, destructive bool) {
	tone := glassAccent()
	title := "Juka asks to change something"
	if destructive {
		tone = WarningInk()
		title = "Juka asks — this can't be undone"
	}
	chatFillRound(renderer, rect, chatPx(18), WithAlpha(tone, 34))
	chatStrokeRound(renderer, rect, chatPx(18), WithAlpha(tone, 120))
	pad := chatPx(16)
	y := rect.Y + chatPx(10)
	chatText(renderer, config, fonts.meta, title, ColorTextSecondary(), rect.X+pad, y)
	metaH, bodyH := int32(fonts.meta.Height()), int32(fonts.body.Height())
	y += metaH + chatPx(2)
	// The question, then the command (or details) under it, each wrapped over
	// a few rows; aiStripHeight made the card tall enough. Rows that would
	// reach the buttons are left out (only the session's first card's first
	// frame, before its height knew the fonts).
	bh := chatPx(32)
	by := rect.Y + rect.H - bh - chatPx(8)
	ask, cmd := aiConfirmWrapped(fonts.body, fonts.meta, text, rect.W-2*pad)
	for _, row := range ask {
		if y+bodyH > by {
			break
		}
		chatText(renderer, config, fonts.body, row, ColorTextPrimary(), rect.X+pad, y)
		y += bodyH
	}
	for _, row := range cmd {
		if y+metaH > by {
			break
		}
		chatText(renderer, config, fonts.meta, row, ColorTextSecondary(), rect.X+pad, y)
		y += metaH
	}
	// The buttons, right-aligned on the bottom row.
	right := rect.X + rect.W - pad
	type btn struct {
		key, label string
		hit        *sdl.Rect
		primary    bool
	}
	btns := [3]btn{
		{"B", "Deny", &aiStripHits.deny, false},
		{"Y", "Allow for this chat", &aiStripHits.always, false},
		{"A", "Allow", &aiStripHits.allow, true},
	}
	for _, b := range btns {
		if b.key == "Y" && destructive {
			continue
		}
		label := hintKeyFor(b.key, b.label) + "  " + b.label
		lw, _ := measureText(fonts.meta, label)
		w := lw + chatPx(28)
		r := sdl.Rect{X: right - w, Y: by, W: w, H: bh}
		if b.primary {
			chatFillRound(renderer, r, bh/2, tone)
			chatTextCentered(renderer, config, fonts.meta, label, contrastText(tone), r.X+chatPx(14), r)
		} else {
			chatDrawChip(renderer, r, chatHover(r))
			chatTextCentered(renderer, config, fonts.meta, label, ColorTextPrimary(), r.X+chatPx(14), r)
		}
		*b.hit = r
		right = r.X - chatPx(8)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// aiChipActivate runs a chip. Main thread.
func aiChipActivate(config *Config, ch aiChip) {
	aiStripFocus = -1
	PlayActivateSound()
	if ch.open != nil {
		ch.open(config)
		return
	}
	chatSubmitComposer(ch.prompt, false)
}

// aiConfirmButton answers the card from a pad or key press.
func aiConfirmButton(d aiDecision) bool {
	if chatActiveTabID() != chatTabAI {
		return false
	}
	if _, _, ok := aiConfirmPending(); !ok {
		return false
	}
	if aiConfirmAnswer(d) {
		if d == aiDeny {
			PlayBackSound()
		} else {
			PlayActivateSound()
		}
	}
	return true
}

// aiStripController takes the pad first on the Juka AI tab: the card's
// buttons while it is up, Left/Right and A on the strip. Main thread.
func aiStripController(e *sdl.ControllerButtonEvent) bool {
	if chatActiveTabID() != chatTabAI {
		return false
	}
	if _, _, ok := aiConfirmPending(); ok {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_A:
			return aiConfirmButton(aiAllow)
		case sdl.CONTROLLER_BUTTON_Y:
			return aiConfirmButton(aiAllowConversation)
		case sdl.CONTROLLER_BUTTON_B:
			return aiConfirmButton(aiDeny)
		}
		return false
	}
	if aiStripFocus < 0 {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		aiStripMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		aiStripMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		return aiStripActivate(appConfig)
	case sdl.CONTROLLER_BUTTON_B:
		aiStripFocus = -1
		PlayBackSound()
	default:
		return false
	}
	return true
}

// aiStripKey is aiStripController for a keyboard.
func aiStripKey(sym sdl.Keycode) bool {
	if chatActiveTabID() != chatTabAI {
		return false
	}
	if _, _, ok := aiConfirmPending(); ok {
		switch sym {
		case sdl.K_RETURN, sdl.K_KP_ENTER:
			return aiConfirmButton(aiAllow)
		case sdl.K_y:
			return aiConfirmButton(aiAllowConversation)
		case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
			return aiConfirmButton(aiDeny)
		}
		return false
	}
	if sym == sdl.K_m {
		return aiStripSpeak(appConfig)
	}
	if aiStripFocus < 0 {
		return false
	}
	switch sym {
	case sdl.K_LEFT:
		aiStripMove(-1)
	case sdl.K_RIGHT:
		aiStripMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		return aiStripActivate(appConfig)
	case sdl.K_ESCAPE:
		aiStripFocus = -1
		PlayBackSound()
	default:
		return false
	}
	return true
}

func aiStripMove(d int) {
	n := len(aiStripChips())
	next := aiStripFocus + d
	if next < 0 || next >= n {
		return
	}
	aiStripFocus = next
	PlayNavSound()
}

func aiStripActivate(config *Config) bool {
	chips := aiStripChips()
	if aiStripFocus < 0 || aiStripFocus >= len(chips) {
		return false
	}
	aiChipActivate(config, chips[aiStripFocus])
	return true
}

// aiStripUp is Up on the Juka AI tab: from the composer row onto the strip
// (true), or off the strip toward the history (false: chatFocusUp goes on).
func aiStripUp() bool {
	if aiStripFocus >= 0 {
		aiStripFocus = -1
		return false
	}
	if !aiStripShown() || chatSelected >= 0 || chatTrayFocus >= 0 || chatSuggestFocus >= 0 {
		return false
	}
	if _, _, ok := aiConfirmPending(); ok {
		return false
	}
	aiStripFocus = 0
	aiStripFirst = 0
	chatAttachFocus, chatVoiceFocus = false, false
	PlayNavSound()
	return true
}

// aiStripDown is Down on the strip: back to the composer.
func aiStripDown() bool {
	if aiStripFocus < 0 {
		return false
	}
	aiStripFocus = -1
	PlayNavSound()
	return true
}

// aiStripClick takes a click on the card or a chip.
func aiStripClick(mx, my int32, config *Config) bool {
	if !aiStripHits.visible || chatActiveTabID() != chatTabAI {
		return false
	}
	if _, _, ok := aiConfirmPending(); ok {
		switch {
		case aiStripHits.allow.W > 0 && pointInRect(mx, my, aiStripHits.allow):
			return aiConfirmButton(aiAllow)
		case aiStripHits.always.W > 0 && pointInRect(mx, my, aiStripHits.always):
			return aiConfirmButton(aiAllowConversation)
		case aiStripHits.deny.W > 0 && pointInRect(mx, my, aiStripHits.deny):
			return aiConfirmButton(aiDeny)
		}
		return false
	}
	chips := aiStripChips()
	for i, r := range aiStripHits.chips {
		k := aiStripFirst + i
		if r.W > 0 && pointInRect(mx, my, r) && k < len(chips) {
			aiChipActivate(config, chips[k])
			return true
		}
	}
	return false
}

// aiStripSpeak is R2 (keyboard M) on the Juka AI tab: the keyboard opens on
// the composer already listening, so a question is one press plus Start.
func aiStripSpeak(config *Config) bool {
	if config == nil || chatActiveTabID() != chatTabAI || chatVoiceRecording() || virtualKeyboardActive {
		return false
	}
	chatOpenKeyboard()
	if virtualKeyboardActive && !vkVoiceActive() {
		vkVoiceToggle(config)
	}
	return true
}

// aiFooterHints are the footer's words while the card or the strip has the
// pad (ok false: the chat's own hints).
func aiFooterHints() ([]FooterHint, bool) {
	if chatActiveTabID() != chatTabAI {
		return nil, false
	}
	if _, destructive, ok := aiConfirmPending(); ok {
		hints := []FooterHint{{Button: "A", Label: "Allow"}, {Button: "B", Label: "Deny"}}
		if !destructive {
			hints = append(hints, FooterHint{Button: "Y", Label: "Allow for this chat"})
		}
		return hints, true
	}
	if aiStripFocus >= 0 {
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Ask"},
			{Button: "B", Label: "Done"},
			{Button: "R2 / M", Label: "Speak"},
		}, true
	}
	return nil, false
}

// ── The voice orb ────────────────────────────────────────────────────────

// aiVoiceTake sends a spoken question about the device ("why won't my game
// start", "what's filling my card") through the agent with read-only tools,
// and answers on the orb the way voiceOrbAsk does. Main thread.
func aiVoiceTake(text string) bool {
	if !aiVoiceWants(text) {
		return false
	}
	voiceOrbCancelRequest()
	gen := voiceOrb.gen
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	voiceOrb.cancel = cancel
	voiceOrb.ai = true
	voiceOrbSetPhase(orbThinking)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = "Checking the device…", orbQuote(text), tbxToneNeutral, orbOfferNone
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logWarn("aiagent", "voice check stopped: %v", r)
				tbxPost(func() { voiceOrbAnswered(gen, text, "", errors.New("Juka's check stopped"), false, false) })
			}
		}()
		reply, local, err := aiAgentVoiceAnswer(ctx, text)
		tbxPost(func() { voiceOrbAnswered(gen, text, reply, err, local, false) })
	}()
	return true
}
