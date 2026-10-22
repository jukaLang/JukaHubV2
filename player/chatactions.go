package main

// chatactions.go — the message-level interactions of the chat screen.
//
// Messages are selectable (shoulders on the controller, left/right on a
// keyboard, a click with a pointer), and a selected message offers what a chat
// needs: reply with a quote, copy the text, hand it to Juka AI, open the
// sender's profile, or delete your own message. Replies carry a quote header in
// the bubble and become real Discord replies when the original came from
// Discord.

import (
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// chatMessageHit is one drawn message, in panel coordinates. The body rect
// selects the message; the avatar and name rects open the sender's profile.
type chatMessageHit struct {
	index      int
	body       sdl.Rect
	avatar     sdl.Rect
	name       sdl.Rect
	sender     string
	userID     string
	ownMessage bool
}

// chatSelection is the message context bar (the little action row that takes
// the composer's place while a message is selected).
type chatSelection struct {
	open    bool
	index   int
	focus   int
	rect    sdl.Rect
	buttons []chatProfileAction
}

var (
	chatMessageHits  []chatMessageHit
	chatSelectionBar chatSelection

	// chatSelected is the selected message index, -1 when nothing is selected.
	// chatSelectedID pins the selection to one message when the list shifts.
	chatSelected   = -1
	chatSelectedID string

	// chatReplyTo is the message the composer is currently quoting.
	chatReplyTo *ChatQuote

	// Header/composer hit targets owned by the chat screen.
	chatHeaderMembersRect sdl.Rect
	chatJumpRect          sdl.Rect
	chatReplyCancelRect   sdl.Rect
	chatReplyStrip        sdl.Rect

	// Unread tracking: chatSeenCount is the number of messages the reader had
	// reached, chatUnread is how many arrived since while scrolled back.
	chatSeenCount   int
	chatUnread      int
	chatOldestShown int
	chatNewestShown int
)

// ChatQuote is the message a reply answers. ID is only set for Discord messages,
// where it lets the reply become a real Discord reply instead of a text quote.
type ChatQuote struct {
	Author string `json:"author"`
	Text   string `json:"text"`
	ID     string `json:"id,omitempty"`
}

func (q *ChatQuote) summary(limit int) string {
	if q == nil {
		return ""
	}
	text := strings.Join(strings.Fields(q.Text), " ")
	if limit > 0 && len(text) > limit {
		text = ellipsizeRunes(text, limit)
	}
	return text
}

// ellipsizeRunes shortens text to at most max characters, keeping whole runes.
func ellipsizeRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// --- Selection -----------------------------------------------------------

// chatClampSelection keeps the selection valid across trims and reloads.
func chatClampSelection() {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	if chatSelected < 0 {
		return
	}
	if chatSelected >= len(chatMessages) || (chatSelectedID != "" && chatMessages[chatSelected].ID != chatSelectedID) {
		clearChatSelectionLocked()
	}
}

func clearChatSelectionLocked() {
	chatSelected = -1
	chatSelectedID = ""
	chatSelectionBar = chatSelection{}
}

// clearChatSelection drops the highlight and closes the action bar.
func clearChatSelection() {
	chatSelected = -1
	chatSelectedID = ""
	chatSelectionBar = chatSelection{}
}

// selectChatMessage highlights one message and opens its action bar.
func selectChatMessage(index int) {
	chatMutex.Lock()
	if index < 0 || index >= len(chatMessages) {
		chatMutex.Unlock()
		return
	}
	id := chatMessages[index].ID
	chatMutex.Unlock()
	chatSelected, chatSelectedID = index, id
	chatSelectionBar = chatSelection{open: true, index: index, focus: 0}
	chatSelectionBar.buttons = chatSelectionActions(index)
	PlayNavSound()
}

// chatSelectionActions lists the bar buttons for a message.
func chatSelectionActions(index int) []chatProfileAction {
	chatMutex.Lock()
	own := index >= 0 && index < len(chatMessages) && chatMessages[index].Own
	chatMutex.Unlock()
	actions := []chatProfileAction{
		{label: "Reply", kind: "reply"},
		{label: "Copy", kind: "copy"},
		{label: "Ask AI", kind: "ask"},
		{label: "Profile", kind: "profile"},
	}
	if own {
		actions = append(actions, chatProfileAction{label: "Delete", kind: "delete"})
	}
	return append(actions, chatProfileAction{label: "Close", kind: "close"})
}

// selectedChatMessage returns the selected message, if the selection is live.
func selectedChatMessage() (ChatMessage, int, bool) {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	if chatSelected < 0 || chatSelected >= len(chatMessages) {
		return ChatMessage{}, -1, false
	}
	return chatMessages[chatSelected], chatSelected, true
}

// moveChatSelection walks the history with the shoulders. A selection is
// created on the newest message when nothing is selected yet.
func moveChatSelection(delta int) bool {
	chatMutex.Lock()
	count := len(chatMessages)
	chatMutex.Unlock()
	if count == 0 {
		return false
	}
	next := chatSelected
	if next < 0 || next >= count {
		next = count - 1
	} else {
		next += delta
	}
	next = clampIndex(next, count)
	if next == chatSelected {
		return false
	}
	chatMutex.Lock()
	id := chatMessages[next].ID
	chatMutex.Unlock()
	chatSelected, chatSelectedID = next, id
	chatSelectionBar.open = false
	chatSelectionBar.index = next
	chatSelectionBar.focus = 0
	chatSelectionBar.buttons = chatSelectionActions(next)
	chatEnsureSelectionVisible()
	PlayNavSound()
	return true
}

// chatEnsureSelectionVisible scrolls the panel so the selected message is on
// screen. The visible range comes from the last frame, which knows how many
// messages actually fit.
func chatEnsureSelectionVisible() {
	chatMutex.Lock()
	count := len(chatMessages)
	chatMutex.Unlock()
	if chatSelected < 0 || count == 0 {
		return
	}
	if chatSelected > chatNewestShown || chatSelected < chatOldestShown || chatNewestShown == 0 {
		chatScrollOffset = clampIndex(count-1-chatSelected, count)
	}
}

// --- Reply ---------------------------------------------------------------

// setChatReply quotes the selected message in the composer.
func setChatReply(index int) bool {
	chatMutex.Lock()
	if index < 0 || index >= len(chatMessages) {
		chatMutex.Unlock()
		return false
	}
	msg := chatMessages[index]
	chatMutex.Unlock()
	chatReplyTo = &ChatQuote{Author: msg.Sender, Text: msg.Text, ID: discordQuoteID(msg)}
	clearChatSelection()
	showToast("Replying to "+msg.Sender, ToastInfo())
	return true
}

// discordQuoteID returns the message id only when it is a Discord snowflake,
// so a local message never becomes a dangling reply reference.
func discordQuoteID(msg ChatMessage) string {
	if isDiscordSnowflake(msg.ID) {
		return msg.ID
	}
	return ""
}

func clearChatReply() {
	chatReplyTo = nil
}

// takeChatReply hands the pending quote to a local send and clears it, so one
// reply quotes exactly one message.
func takeChatReply(sender string) *ChatQuote {
	if chatReplyTo == nil || !isLocalChatSender(sender) {
		return nil
	}
	quote := chatReplyTo
	chatReplyTo = nil
	return quote
}

// chatReplyStripHeight is the vertical room the reply strip needs above the
// composer (0 while nothing is quoted).
func chatReplyStripHeight(font *ttf.Font) int32 {
	if chatReplyTo == nil || font == nil {
		return 0
	}
	return int32(font.Height()) + 20
}

// drawChatReplyStrip draws the "Replying to …" strip above the composer and
// records its cancel button.
func drawChatReplyStrip(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	if chatReplyTo == nil || font == nil || rect.W <= 0 {
		return
	}
	chatReplyStrip = rect
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusSM, WithAlpha(ColorInfo, 26))
	fillRoundedRect(renderer, rect.X, rect.Y+4, 3, rect.H-8, 2, WithAlpha(ColorInfo, 200))

	pad := int32(10)
	cancelLabel := "Cancel"
	cancelW, _, _ := font.SizeUTF8(cancelLabel)
	cancelW += 18
	chatReplyCancelRect = sdl.Rect{
		X: rect.X + rect.W - int32(cancelW) - SpaceSM,
		Y: rect.Y + (rect.H-(int32(font.Height())+6))/2,
		W: int32(cancelW),
		H: int32(font.Height()) + 6,
	}
	fillRoundedRect(renderer, chatReplyCancelRect.X, chatReplyCancelRect.Y, chatReplyCancelRect.W, chatReplyCancelRect.H,
		chatReplyCancelRect.H/2, WithAlpha(ColorSurfaceAlt, 230))
	renderText(renderer, config, font, cancelLabel, ColorTextSecondary(), chatReplyCancelRect.X+9, chatReplyCancelRect.Y+3)

	head := "Replying to " + chatReplyTo.Author
	headW, _, _ := font.SizeUTF8(head)
	renderText(renderer, config, font, head, ColorInfo, rect.X+pad, rect.Y+3)

	textX := rect.X + pad + int32(headW) + SpaceSM
	textW := chatReplyCancelRect.X - textX - SpaceSM
	if textW > 40 {
		preview := chatReplyTo.summary(120)
		renderText(renderer, config, font, ellipsize(font, preview, textW), ColorTextTertiary(), textX, rect.Y+3)
	}
}

// --- Action bar ----------------------------------------------------------

// drawChatSelectionBar replaces the composer while a message is selected.
func drawChatSelectionBar(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	if font == nil || rect.W <= 0 {
		return
	}
	chatSelectionBar.rect = rect
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusMD, WithAlpha(ColorSurfacePanel, 245))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusMD, 1, WithAlpha(accentColor, 140))

	label := "Message"
	labelW, _, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextPrimary(), rect.X+12, rect.Y+(rect.H-int32(font.Height()))/2)

	bx := rect.X + 12 + int32(labelW) + SpaceMD
	for i := range chatSelectionBar.buttons {
		button := &chatSelectionBar.buttons[i]
		bw, _, _ := font.SizeUTF8(button.label)
		w := int32(bw) + 20
		if bx+w > rect.X+rect.W-SpaceSM {
			break
		}
		button.rect = UIButton(renderer, config, sdl.Rect{X: bx, Y: rect.Y + (rect.H-30)/2, W: w, H: 30}, button.label, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       WithAlpha(ColorSurfaceAlt, 235),
			HoverFill:  ColorCardHover,
			FocusFill:  ColorCardFocus,
			Border:     WithAlpha(ColorBorder, 150),
			LabelColor: ColorTextPrimary(),
			Font:       font,
			Pill:       true,
			NoShadow:   true,
		}, UIButtonState{Focused: i == chatSelectionBar.focus})
		bx += w + SpaceSM
	}
}

// activateChatSelectionAction runs one bar button.
func activateChatSelectionAction(kind string, config *Config) {
	msg, index, ok := selectedChatMessage()
	if kind == "close" {
		clearChatSelection()
		PlayBackSound()
		return
	}
	if !ok {
		clearChatSelection()
		return
	}
	switch kind {
	case "reply":
		setChatReply(index)
	case "copy":
		sdl.SetClipboardText(msg.Text)
		showToast("Message copied", ToastInfo())
		clearChatSelection()
	case "ask":
		clearChatSelection()
		sendGroqChatMessage("About this message from " + msg.Sender + ": \"" + chatQuoteSummary(msg.Text, 600) + "\"")
	case "profile":
		clearChatSelection()
		openChatProfile(msg.Sender, msg.UserID)
	case "delete":
		deleteChatMessage(msg.ID)
	}
}

// chatQuoteSummary collapses whitespace and caps the text handed to the
// assistant, so a pasted wall of text does not blow up the prompt.
func chatQuoteSummary(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit > 0 && len(text) > limit {
		text = ellipsizeRunes(text, limit)
	}
	return text
}

// deleteChatMessage removes one of the user's own messages from the history.
func deleteChatMessage(id string) {
	chatMutex.Lock()
	kept := chatMessages[:0]
	removed := false
	for _, msg := range chatMessages {
		if !removed && msg.ID == id && msg.Own {
			removed = true
			continue
		}
		kept = append(kept, msg)
	}
	chatMessages = kept
	chatMutex.Unlock()
	if !removed {
		showToast("Only your own messages can be deleted", ToastWarn())
		clearChatSelection()
		return
	}
	clearChatSelection()
	saveChatMessages()
	showToast("Message deleted", ToastInfo())
}

// --- Input ---------------------------------------------------------------

// handleChatSelectionKey drives the action bar from the keyboard. It consumes
// every key while the bar is open.
func handleChatSelectionKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !chatSelectionBar.open || e == nil || e.Type != sdl.KEYDOWN {
		return chatSelectionBar.open
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		clearChatSelection()
		PlayBackSound()
	case sdl.K_LEFT, sdl.K_a:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus-1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.K_RIGHT, sdl.K_d, sdl.K_TAB:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus+1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if chatSelectionBar.focus >= 0 && chatSelectionBar.focus < len(chatSelectionBar.buttons) {
			activateChatSelectionAction(chatSelectionBar.buttons[chatSelectionBar.focus].kind, config)
		}
	}
	return true
}

// ctrlChatSelection is the controller twin of handleChatSelectionKey.
func ctrlChatSelection(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !chatSelectionBar.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus-1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus+1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if chatSelectionBar.focus >= 0 && chatSelectionBar.focus < len(chatSelectionBar.buttons) {
			activateChatSelectionAction(chatSelectionBar.buttons[chatSelectionBar.focus].kind, config)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		clearChatSelection()
		PlayBackSound()
	}
	return true
}

// chatMessageClick handles a click on any chat-owned target: the member chip,
// the unread pill, the reply strip, the action bar, a sender's avatar/name and
// a message body. It reports whether the click was consumed, so the generic
// "click the panel to type" handler does not also fire.
func chatMessageClick(mx, my int32, config *Config) bool {
	if chatProfile.open || chatMembers.open {
		return false
	}
	if pointInRect(mx, my, chatHeaderMembersRect) {
		openChatMembers()
		return true
	}
	if pointInRect(mx, my, chatJumpRect) {
		chatScrollOffset = 0
		chatMutex.Lock()
		chatSeenCount = len(chatMessages)
		chatMutex.Unlock()
		PlayNavSound()
		return true
	}
	if chatReplyTo != nil && pointInRect(mx, my, chatReplyCancelRect) {
		clearChatReply()
		PlayBackSound()
		return true
	}
	if chatSelectionBar.open {
		for _, button := range chatSelectionBar.buttons {
			if pointInRect(mx, my, button.rect) {
				activateChatSelectionAction(button.kind, config)
				return true
			}
		}
		if pointInRect(mx, my, chatSelectionBar.rect) {
			return true
		}
		// A click elsewhere drops the bar, then continues as a normal click so
		// the next tap does what it looks like it should.
		clearChatSelection()
	}
	for _, hit := range chatMessageHits {
		if hit.avatar.W > 0 && pointInRect(mx, my, hit.avatar) {
			openChatProfile(hit.sender, hit.userID)
			return true
		}
		if hit.name.W > 0 && pointInRect(mx, my, hit.name) {
			openChatProfile(hit.sender, hit.userID)
			return true
		}
		if pointInRect(mx, my, hit.body) {
			if chatSelected == hit.index && chatSelectionBar.open {
				openChatActionsForSelected()
				return true
			}
			selectChatMessage(hit.index)
			return true
		}
	}
	return false
}

// openChatActionsForSelected opens the action bar for the selection, creating
// one on the newest message when nothing is selected.
func openChatActionsForSelected() bool {
	if msg, index, ok := selectedChatMessage(); ok {
		chatSelectionBar = chatSelection{open: true, index: index, focus: 0, buttons: chatSelectionActions(index)}
		_ = msg
		return true
	}
	chatMutex.Lock()
	count := len(chatMessages)
	chatMutex.Unlock()
	if count == 0 {
		return false
	}
	selectChatMessage(count - 1)
	return true
}

// --- Drawing helpers used by renderChat ----------------------------------

// chatDayLabel names the day of a message the way a chat reader expects.
func chatDayLabel(t time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case day.Year() == today.Year():
		return day.Format("Mon 2 Jan")
	}
	return day.Format("2 Jan 2006")
}

func chatSameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// chatContinuesGroup reports whether a message is drawn as a continuation of
// the one before it: same speaker, same day, within a few minutes and not a
// reply (a reply always shows who is being answered).
func chatContinuesGroup(messages []ChatMessage, i int) bool {
	if i <= 0 || i >= len(messages) {
		return false
	}
	prev, cur := messages[i-1], messages[i]
	if !strings.EqualFold(prev.Sender, cur.Sender) || prev.UserID != cur.UserID {
		return false
	}
	if cur.Quote != nil || !chatSameDay(prev.Timestamp, cur.Timestamp) {
		return false
	}
	gap := cur.Timestamp.Sub(prev.Timestamp)
	return gap >= 0 && gap <= chatGroupWindow
}

// drawChatDayDivider draws the "Today" / date separator. unread switches it to
// the accent colour and the "NEW MESSAGES" label.
func drawChatDayDivider(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, w, y int32, label string, unread bool) {
	if font == nil || w <= 0 {
		return
	}
	col := WithAlpha(ColorBorder, 150)
	labelCol := ColorTextTertiary()
	if unread {
		col = WithAlpha(accentColor, 170)
		labelCol = accentColor
	}
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	renderer.FillRect(&sdl.Rect{X: x, Y: y + int32(font.Height())/2, W: w, H: 1})
	lw, _, _ := font.SizeUTF8(label)
	labelX := x + (w-int32(lw))/2
	fillRoundedRect(renderer, labelX-8, y, int32(lw)+16, int32(font.Height()), int32(font.Height())/2, WithAlpha(ColorSurfacePanel, 250))
	renderText(renderer, config, font, label, labelCol, labelX, y)
}

// drawChatJumpPill shows how many messages arrived while the reader is scrolled
// back, and jumps to them when clicked.
func drawChatJumpPill(renderer *sdl.Renderer, config *Config, font *ttf.Font, anchorX, anchorY, panelW int32) {
	if chatUnread <= 0 || font == nil {
		return
	}
	label := fmt.Sprintf("%d new message", chatUnread)
	if chatUnread != 1 {
		label += "s"
	}
	label += " · Jump to latest"
	w, _, _ := font.SizeUTF8(label)
	pillW := int32(w) + 24
	pillH := int32(font.Height()) + 10
	x := anchorX + (panelW-pillW)/2
	if x < anchorX {
		x = anchorX
	}
	chatJumpRect = sdl.Rect{X: x, Y: anchorY - pillH - SpaceXS, W: pillW, H: pillH}
	fillRoundedRect(renderer, chatJumpRect.X, chatJumpRect.Y, chatJumpRect.W, chatJumpRect.H, pillH/2, WithAlpha(accentColor, 220))
	renderText(renderer, config, font, label, ColorTextInverse(), chatJumpRect.X+12, chatJumpRect.Y+5)
}

// drawChatQuoteBlock draws the quote header inside a reply bubble.
func drawChatQuoteBlock(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32, quote *ChatQuote) int32 {
	if quote == nil || font == nil {
		return 0
	}
	lineH := int32(font.Height())
	h := lineH*2 + 6
	fillRoundedRect(renderer, x, y, w, h, RadiusSM, WithAlpha(ColorSurfaceAlt, 200))
	fillRoundedRect(renderer, x, y+3, 3, h-6, 2, WithAlpha(ColorInfo, 190))
	author := "↳ " + quote.Author
	renderText(renderer, config, font, ellipsize(font, author, w-16), ColorInfo, x+10, y+2)
	renderText(renderer, config, font, ellipsize(font, quote.summary(90), w-16), ColorTextTertiary(), x+10, y+2+lineH)
	return h
}
