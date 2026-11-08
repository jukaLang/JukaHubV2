package main

// chatactions.go — the message-level interactions of the chat screen.
//
// The pad walks the history message by message (chatinput.go); A on a message
// turns the composer into its action bar, and a click on a bubble does the
// same. A message offers what a chat needs on a handheld: enlarge its picture,
// open or play its link, reply with a quote, copy, ask Juka about it, keep it
// in Saved, open the sender's profile, retry a message that did not go out,
// and delete. Replies carry a quote header in the bubble and become real
// Discord replies when the original came from Discord.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// chatMessageHit is one drawn message, in screen coordinates. The body rect
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

// chatSelection is the message action bar (the row of actions that takes the
// composer's place while it is open).
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

	// chatSelected is the focused message of the tab on screen, -1 for the
	// composer. chatSelectedID pins it to one message when the list shifts.
	chatSelected   = -1
	chatSelectedID string

	// chatReplyTo is the community message the composer is quoting.
	chatReplyTo *ChatQuote

	// Hit targets owned by the chat screen.
	chatHeaderMembersRect sdl.Rect
	chatJumpRect          sdl.Rect
	chatReplyCancelRect   sdl.Rect
	chatReplyStrip        sdl.Rect
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

// chatClampSelection keeps the selection on its message across trims, merges
// and reloads: the index follows the id, and a message that is gone drops the
// selection.
func chatClampSelection() {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	if chatSelected < 0 {
		return
	}
	list := *chatTabSliceLocked(chatActiveTabID())
	if chatSelected < len(list) && (chatSelectedID == "" || list[chatSelected].ID == chatSelectedID) {
		return
	}
	if chatSelectedID != "" {
		for i := range list {
			if list[i].ID == chatSelectedID {
				chatSelected = i
				chatSelectionBar.index = i
				return
			}
		}
	}
	clearChatSelectionLocked()
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

// selectChatMessage highlights one message of the tab on screen and opens its
// action bar.
func selectChatMessage(index int) {
	msgs := chatActiveMessages()
	if index < 0 || index >= len(msgs) {
		return
	}
	chatStopInlineTyping()
	chatSuggestFocus = -1
	chatSelected, chatSelectedID = index, msgs[index].ID
	chatSelectionBar = chatSelection{open: true, index: index, focus: 0}
	chatSelectionBar.buttons = chatSelectionActions(index)
	PlayNavSound()
}

// chatFirstLink is the first link of a message, and whether it plays in the
// media player rather than opening in the browser.
func chatFirstLink(msg ChatMessage) (string, bool) {
	links := chatMessageLinks(msg.Text)
	if len(links) == 0 {
		return "", false
	}
	return links[0], chatLinkOpensInPlayer(links[0])
}

// chatSelectionActions lists the action bar buttons for a message of the tab
// on screen.
func chatSelectionActions(index int) []chatProfileAction {
	msgs := chatActiveMessages()
	if index < 0 || index >= len(msgs) {
		return []chatProfileAction{{label: "Close", kind: "close"}}
	}
	msg := msgs[index]
	tab := chatActiveTabID()
	own := chatIsOwn(msg)
	actions := make([]chatProfileAction, 0, 8)
	// A picture is the one part of a message that cannot be read at bubble
	// size, and a link is the one that does nothing until it is opened, so
	// those lead; every other entry is about the message as a whole.
	if chatMessageImageURL(msg) != "" {
		actions = append(actions, chatProfileAction{label: "Enlarge", kind: "image"})
	}
	if link, plays := chatFirstLink(msg); link != "" {
		if plays {
			actions = append(actions, chatProfileAction{label: "Play", kind: "play"})
		} else {
			actions = append(actions, chatProfileAction{label: "Open link", kind: "open"})
		}
	}
	if own && msg.Error != "" {
		actions = append(actions, chatProfileAction{label: "Retry", kind: "retry"})
	}
	if tab == chatTabCommunity {
		actions = append(actions, chatProfileAction{label: "Reply", kind: "reply"})
	}
	if strings.TrimSpace(msg.Text) != "" {
		actions = append(actions, chatProfileAction{label: "Copy", kind: "copy"})
	}
	if tab != chatTabAI && strings.TrimSpace(msg.Text) != "" {
		actions = append(actions, chatProfileAction{label: "Ask Juka", kind: "ask"})
	}
	if tab != chatTabSaved {
		actions = append(actions, chatProfileAction{label: "Save", kind: "save"})
	}
	if tab == chatTabCommunity && !own {
		actions = append(actions, chatProfileAction{label: "Profile", kind: "profile"})
	}
	// The community channel is shared: only this device's own messages can be
	// removed (from this device). The other tabs are this device's alone.
	if own || tab != chatTabCommunity {
		actions = append(actions, chatProfileAction{label: "Delete", kind: "delete"})
	}
	return append(actions, chatProfileAction{label: "Close", kind: "close"})
}

// selectedChatMessage returns the selected message, if the selection is live.
func selectedChatMessage() (ChatMessage, int, bool) {
	msgs := chatActiveMessages()
	if chatSelected < 0 || chatSelected >= len(msgs) {
		return ChatMessage{}, -1, false
	}
	return msgs[chatSelected], chatSelected, true
}

// moveChatSelection walks the selection by delta messages (a selection starts
// on the newest message), without opening the action bar.
func moveChatSelection(delta int) bool {
	msgs := chatActiveMessages()
	if len(msgs) == 0 {
		return false
	}
	next := chatSelected
	if next < 0 || next >= len(msgs) {
		next = len(msgs) - 1
	} else {
		next += delta
	}
	next = clampIndex(next, len(msgs))
	if next == chatSelected {
		return false
	}
	mode := chatAnchorTop
	if delta < 0 {
		mode = chatAnchorBottom
	}
	chatFocusMessage(msgs, next, mode)
	return true
}

// chatEnsureSelectionVisible asks the next frame to bring the selected message
// on screen.
func chatEnsureSelectionVisible() {
	msgs := chatActiveMessages()
	if chatSelected < 0 || chatSelected >= len(msgs) {
		return
	}
	chatScrollToIndex(chatSelected)
}

// --- Reply ---------------------------------------------------------------

// setChatReply quotes a community message in the composer.
func setChatReply(index int) bool {
	msgs := chatTabMessages(chatTabCommunity)
	if index < 0 || index >= len(msgs) {
		return false
	}
	msg := msgs[index]
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
	return int32(font.Height()) + chatPx(18)
}

// drawChatReplyStrip draws the "Replying to …" strip above the composer and
// records its cancel button.
func drawChatReplyStrip(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	if chatReplyTo == nil || font == nil || rect.W <= 0 {
		return
	}
	chatReplyStrip = rect
	accent := glassAccent()
	chatFillRound(renderer, rect, rect.H/2, WithAlpha(accent, 26))
	chatFillRound(renderer, sdl.Rect{X: rect.X + chatPx(12), Y: rect.Y + chatPx(6), W: chatPx(3), H: rect.H - chatPx(12)}, chatPx(2), accent)

	cancelLabel := "Cancel"
	cancelW, _ := measureText(font, cancelLabel)
	cancelW += chatPx(24)
	ch := rect.H - chatPx(8)
	chatReplyCancelRect = sdl.Rect{X: rect.X + rect.W - cancelW - chatPx(4), Y: rect.Y + (rect.H-ch)/2, W: cancelW, H: ch}
	fill := glassA(16)
	if chatHover(chatReplyCancelRect) {
		fill = glassA(26)
	}
	chatFillRound(renderer, chatReplyCancelRect, ch/2, fill)
	chatTextCentered(renderer, config, font, cancelLabel, ColorTextSecondary(), chatReplyCancelRect.X+chatPx(12), chatReplyCancelRect)

	pad := chatPx(24)
	head := "Replying to " + chatReplyTo.Author
	headW := chatTextCentered(renderer, config, font, head, InfoInk(), rect.X+pad, rect)
	textX := rect.X + pad + headW + chatPx(10)
	if textW := chatReplyCancelRect.X - textX - chatPx(10); textW > chatPx(40) {
		chatTextCentered(renderer, config, font, ellipsize(font, sanitizeCached(font, chatReplyTo.summary(120)), textW), ColorTextTertiary(), textX, rect)
	}
}

// --- Action bar ----------------------------------------------------------

// drawChatSelectionBar replaces the composer while a message's actions are
// open: glass pills, the focused one with Home's ring.
func drawChatSelectionBar(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	if font == nil || rect.W <= 0 {
		return
	}
	chatSelectionBar.rect = rect
	chatFillRound(renderer, rect, rect.H/2, glassA(14))
	chatStrokeRound(renderer, rect, rect.H/2, WithAlpha(glassAccent(), 120))

	h := rect.H - chatPx(14)
	gap := chatPx(8)
	pad := chatPx(18)
	x := rect.X + chatPx(10)
	right := rect.X + rect.W - chatPx(10)
	for i := range chatSelectionBar.buttons {
		button := &chatSelectionBar.buttons[i]
		lw, _ := measureText(font, button.label)
		w := lw + 2*pad
		button.rect = sdl.Rect{}
		if x+w > right {
			break
		}
		r := sdl.Rect{X: x, Y: rect.Y + (rect.H-h)/2, W: w, H: h}
		button.rect = r
		focused := i == chatSelectionBar.focus
		if focused {
			drawGlassFocusGlow(renderer, r)
		}
		fill := glassA(16)
		if chatHover(r) {
			fill = glassA(26)
		}
		chatFillRound(renderer, r, h/2, fill)
		if focused {
			drawGlassFocusRing(renderer, r, h/2)
		}
		col := ColorTextPrimary()
		if button.kind == "delete" {
			col = DangerInk()
		}
		chatTextCentered(renderer, config, font, button.label, col, r.X+pad, r)
		x += w + gap
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
	tab := chatActiveTabID()
	switch kind {
	case "image":
		// The viewer owns the screen once it opens, so the bar is dropped
		// first: a bar left behind would reappear over the page when the
		// picture closed, still pointing at an old selection.
		url := chatMessageImageURL(msg)
		clearChatSelection()
		if url == "" {
			showToast("This message has no picture to enlarge", ColorToastWarn)
			return
		}
		openImageViewerURL(url)
	case "open", "play":
		link, plays := chatFirstLink(msg)
		clearChatSelection()
		if link == "" {
			return
		}
		PlayActivateSound()
		if plays {
			playSmartURL(config, link)
		} else {
			openBrowserAtURL(config, link)
		}
	case "retry":
		clearChatSelection()
		if chatRetryMessage(tab, msg.ID) {
			PlayActivateSound()
		}
	case "reply":
		setChatReply(index)
	case "copy":
		if err := copyTextToClipboard(msg.Text, "Chat"); err != nil {
			showToast("Could not copy the message", ColorToastWarn)
		} else {
			showToast("Message copied", ToastInfo())
		}
		clearChatSelection()
	case "ask":
		clearChatSelection()
		question := "About this message from " + msg.Sender + ": \"" + chatQuoteSummary(msg.Text, 600) + "\""
		if chatHasGroqKey() {
			chatSelectTab(chatTabAI)
		}
		if chatAskAI(question) {
			chatJumpToLatest()
		}
	case "save":
		clearChatSelection()
		chatSaveCopy(msg)
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

// deleteChatMessage removes a message from the tab on screen: in the shared
// community channel only this device's own messages, anywhere else any.
func deleteChatMessage(id string) {
	tab := chatActiveTabID()
	chatMutex.Lock()
	allowed := true
	if tab == chatTabCommunity {
		allowed = false
		for _, msg := range chatMessages {
			if msg.ID == id {
				allowed = chatIsOwn(msg)
				break
			}
		}
	}
	removed := allowed && chatRemoveLocked(tab, id)
	chatMutex.Unlock()
	clearChatSelection()
	if !removed {
		showToast("Only your own messages can be deleted", ToastWarn())
		return
	}
	chatDirty.Store(true)
	PlayActivateSound()
	showToast("Message deleted", ToastInfo())
}

// --- Input ---------------------------------------------------------------

// chatSelectionOwnsInput reports whether the message action bar should take
// the pad and the keys. The bar is drawn inside the chat, so it only has a
// meaning while that scene is on screen; a bar left open by a scene change is
// stale and drops itself here instead of swallowing the next press on the
// screen underneath (it used to eat X on Home's Now Playing chip, which is how
// the floating video is closed).
func chatSelectionOwnsInput(config *Config) bool {
	if !chatSelectionBar.open {
		return false
	}
	if !currentSceneHasChat(config) {
		clearChatSelection()
		return false
	}
	return true
}

// handleChatSelectionKey drives the action bar from the keyboard. It consumes
// every key while the bar is open.
func handleChatSelectionKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return chatSelectionOwnsInput(config)
	}
	if !chatSelectionOwnsInput(config) {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		chatSelectionBar.open = false
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
	if !chatSelectionOwnsInput(config) {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus-1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		chatSelectionBar.focus = clampIndex(chatSelectionBar.focus+1, len(chatSelectionBar.buttons))
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if chatSelectionBar.focus >= 0 && chatSelectionBar.focus < len(chatSelectionBar.buttons) {
			activateChatSelectionAction(chatSelectionBar.buttons[chatSelectionBar.focus].kind, config)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		// The bar closes and the message keeps the focus, so the next B
		// leaves the chat as usual.
		chatSelectionBar.open = false
		PlayBackSound()
	}
	return true
}

// chatMessageClick handles a click on any chat-owned target: the people chip,
// the jump pill, the reply strip, the action bar, a sender's avatar or name
// and a message body. It reports whether the click was consumed.
func chatMessageClick(mx, my int32, config *Config) bool {
	if chatProfile.open || chatMembers.open {
		return false
	}
	if pointInRect(mx, my, chatHeaderMembersRect) {
		openChatMembers()
		return true
	}
	if pointInRect(mx, my, chatJumpRect) {
		clearChatSelection()
		chatJumpToLatest()
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
			if button.rect.W > 0 && pointInRect(mx, my, button.rect) {
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
	if _, index, ok := selectedChatMessage(); ok {
		chatSelectionBar = chatSelection{open: true, index: index, focus: 0, buttons: chatSelectionActions(index)}
		return true
	}
	msgs := chatActiveMessages()
	if len(msgs) == 0 {
		return false
	}
	selectChatMessage(len(msgs) - 1)
	return true
}

// --- Grouping helpers ----------------------------------------------------

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
	if chatIsSystem(prev) || !strings.EqualFold(prev.Sender, cur.Sender) || prev.UserID != cur.UserID {
		return false
	}
	if cur.Quote != nil || !chatSameDay(prev.Timestamp.Local(), cur.Timestamp.Local()) {
		return false
	}
	gap := cur.Timestamp.Sub(prev.Timestamp)
	return gap >= 0 && gap <= chatGroupWindow
}

// drawChatQuoteBlock draws the quote header inside a reply bubble and returns
// its height (chatQuoteHeight). A quote without text — a note kept in Saved
// from someone else — is a one-line "From …" header.
func drawChatQuoteBlock(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32, quote *ChatQuote) int32 {
	if quote == nil || font == nil || w <= 0 {
		return 0
	}
	h := chatQuoteHeight(font, quote)
	lineH := int32(font.Height())
	chatFillRound(renderer, sdl.Rect{X: x, Y: y, W: w, H: h}, chatPx(10), glassA(12))
	chatFillRound(renderer, sdl.Rect{X: x + chatPx(4), Y: y + chatPx(4), W: chatPx(3), H: h - chatPx(8)}, chatPx(2), WithAlpha(glassAccent(), 200))
	textX := x + chatPx(14)
	textW := w - chatPx(20)
	if textW <= 0 {
		return h
	}
	if strings.TrimSpace(quote.Text) == "" {
		chatText(renderer, config, font, ellipsize(font, sanitizeCached(font, "From "+quote.Author), textW), InfoInk(), textX, y+(h-lineH)/2)
		return h
	}
	top := y + (h-2*lineH)/2
	chatText(renderer, config, font, ellipsize(font, sanitizeCached(font, quote.Author), textW), InfoInk(), textX, top)
	chatText(renderer, config, font, ellipsize(font, sanitizeCached(font, quote.summary(90)), textW), ColorTextTertiary(), textX, top+lineH)
	return h
}
