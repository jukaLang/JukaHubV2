package main

// chatstate.go — the Chat screen's conversations: which tabs exist, what each
// one holds, and how a message gets in (typed, received or answered).
//
// The screen used to be one list that mixed the Discord channel, the Juka AI
// conversation and the app's own error notices, with "Ask AI" as a button on
// the composer. Each source is a tab of its own now:
//
//   - Community: the configured Discord channel (chatMessages, the history the
//     rest of the app already reads and writes);
//   - Juka AI: the Groq assistant (chatAIMessages), local to this device;
//   - Saved: notes to self (chatSavedMessages), local to this device.
//
// Messages are immutable once they are in a history: an update replaces the
// slice with a copy (chatUpdateLocked), so the frame being drawn and the
// background sender can each hold a snapshot without holding chatMutex for the
// whole time they read it.

import (
	"strings"
	"sync/atomic"
	"time"
)

// chatSceneName is the config scene the chat lives in.
const chatSceneName = "Chat"

// Senders the app itself uses. They are display names too, which is why they
// are compared case-insensitively.
const (
	chatAISender     = "Juka AI"
	chatSystemSender = "System"
)

// chatTab names one conversation tab.
type chatTab int

const (
	chatTabCommunity chatTab = iota
	chatTabAI
	chatTabSaved
	chatTabCount
)

// chatTabKeys are the stable names a tab is saved and opened under.
var chatTabKeys = [chatTabCount]string{"community", "ai", "saved"}

// chatHistoryVersion marks a saved chat section that is already split into
// tabs. Version 0 is the old single mixed list.
const chatHistoryVersion = 2

// chatTabData is the part of a tab's state background goroutines touch (the
// Discord handler counts unread messages). chatMutex guards it.
type chatTabData struct {
	unread int
	seenAt int64 // unix ms of the newest message read
}

// chatAnchorMode says how a message the view must show is placed.
type chatAnchorMode int

const (
	chatAnchorNone chatAnchorMode = iota
	chatAnchorBottom
	chatAnchorTop
)

// chatTabView is a tab's on-screen state. Only the render thread and the input
// handlers (the same thread) touch it.
type chatTabView struct {
	draft string
	// scroll is how far the history is pushed down from "newest message at the
	// bottom", in pixels; target is where the smooth scroll is heading.
	scroll float64
	target float64
	// anchorID is a message the next frame must bring on screen.
	anchorID   string
	anchorMode chatAnchorMode
	// lastNewest and bottomExtra let the view hold still while a reader is
	// scrolled back and new messages (or a growing answer) arrive below.
	lastNewest  string
	bottomExtra int32
	// topReached is set when the oldest message was on screen last frame,
	// atBottom when the newest one was.
	topReached bool
	atBottom   bool
	// dividerID is the first message that arrived while the tab was unread;
	// the "New messages" divider sits above it until the tab is left.
	dividerID string
}

var (
	chatAIMessages    []ChatMessage
	chatSavedMessages []ChatMessage

	chatTabs  [chatTabCount]chatTabData
	chatViews [chatTabCount]chatTabView

	// chatActiveTab is the tab on screen. The Discord goroutine reads it, so it
	// is atomic; everything else changes on the UI thread.
	chatActiveTab atomic.Int32
	// chatLiveTab is the tab that is on screen and scrolled to its newest
	// message (-1 when none): a message arriving there is read on arrival.
	chatLiveTab atomic.Int32

	// chatDirty asks chatTick to persist the history (background writers mark
	// it; the write itself happens from the main loop).
	chatDirty atomic.Bool
	// chatOutboxFlag tells chatTick a message is waiting to be sent.
	chatOutboxFlag atomic.Bool

	chatLoaded     bool
	chatWasVisible bool
)

func init() { chatLiveTab.Store(-1) }

// chatActiveTabID returns the tab on screen.
func chatActiveTabID() chatTab {
	t := chatTab(chatActiveTab.Load())
	if t < 0 || t >= chatTabCount {
		return chatTabCommunity
	}
	return t
}

// chatTabFromKey maps a saved or requested tab name onto a tab.
func chatTabFromKey(key string) (chatTab, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for t := chatTabCommunity; t < chatTabCount; t++ {
		if chatTabKeys[t] == key {
			return t, true
		}
	}
	switch key {
	case "discord", "chat":
		return chatTabCommunity, true
	case "juka", "assistant", "groq":
		return chatTabAI, true
	case "notes", "self":
		return chatTabSaved, true
	}
	return chatTabCommunity, false
}

// chatTabCap is how many messages a tab keeps.
func chatTabCap(tab chatTab) int {
	switch tab {
	case chatTabAI:
		return 200
	case chatTabSaved:
		return 500
	}
	return 400
}

// chatTabSliceLocked returns the history of a tab. chatMutex must be held.
func chatTabSliceLocked(tab chatTab) *[]ChatMessage {
	switch tab {
	case chatTabAI:
		return &chatAIMessages
	case chatTabSaved:
		return &chatSavedMessages
	}
	return &chatMessages
}

// chatTabMessages returns a snapshot of a tab's history. Messages are never
// changed in place, so the snapshot may be read after the lock is released.
func chatTabMessages(tab chatTab) []ChatMessage {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	return *chatTabSliceLocked(tab)
}

// chatActiveMessages is chatTabMessages for the tab on screen.
func chatActiveMessages() []ChatMessage {
	return chatTabMessages(chatActiveTabID())
}

// chatAppendLocked adds msg to a tab and trims the oldest messages past the
// tab's cap. chatMutex must be held.
func chatAppendLocked(tab chatTab, msg ChatMessage) {
	list := chatTabSliceLocked(tab)
	*list = append(*list, msg)
	if limit := chatTabCap(tab); len(*list) > limit {
		*list = (*list)[len(*list)-limit:]
	}
}

// chatUpdateLocked rewrites the message with id through fn, on a copy of the
// history, and reports whether it was found. chatMutex must be held.
func chatUpdateLocked(tab chatTab, id string, fn func(*ChatMessage)) bool {
	if id == "" {
		return false
	}
	list := chatTabSliceLocked(tab)
	for i := len(*list) - 1; i >= 0; i-- {
		if (*list)[i].ID != id {
			continue
		}
		next := make([]ChatMessage, len(*list))
		copy(next, *list)
		fn(&next[i])
		*list = next
		return true
	}
	return false
}

// chatUpdateMessage is chatUpdateLocked with the lock taken.
func chatUpdateMessage(tab chatTab, id string, fn func(*ChatMessage)) bool {
	chatMutex.Lock()
	ok := chatUpdateLocked(tab, id, fn)
	chatMutex.Unlock()
	if ok {
		chatDirty.Store(true)
	}
	return ok
}

// chatInsertAfterLocked inserts msg after the message afterID (and after any
// answers already given to it), or at the end when afterID is gone. An answer
// to a question queued while offline then sits under its own question, not
// under the next one. chatMutex must be held.
func chatInsertAfterLocked(tab chatTab, afterID string, msg ChatMessage) {
	list := chatTabSliceLocked(tab)
	at := len(*list)
	for i := len(*list) - 1; i >= 0; i-- {
		if (*list)[i].ID != afterID {
			continue
		}
		at = i + 1
		for at < len(*list) && strings.EqualFold((*list)[at].Sender, chatAISender) {
			at++
		}
		break
	}
	next := make([]ChatMessage, 0, len(*list)+1)
	next = append(next, (*list)[:at]...)
	next = append(next, msg)
	next = append(next, (*list)[at:]...)
	if limit := chatTabCap(tab); len(next) > limit {
		next = next[len(next)-limit:]
	}
	*list = next
}

// chatRemoveLocked drops the message with id from a tab and reports whether it
// was there. chatMutex must be held.
func chatRemoveLocked(tab chatTab, id string) bool {
	list := chatTabSliceLocked(tab)
	for i, msg := range *list {
		if msg.ID != id {
			continue
		}
		next := make([]ChatMessage, 0, len(*list)-1)
		next = append(next, (*list)[:i]...)
		next = append(next, (*list)[i+1:]...)
		*list = next
		return true
	}
	return false
}

// chatIsOwn reports whether a message was written on this device. A Discord
// message (snowflake id) never counts by name alone: every JukaHub user who
// kept the default name posts as "You".
func chatIsOwn(msg ChatMessage) bool {
	if msg.Own {
		return true
	}
	return !isDiscordSnowflake(msg.ID) && isLocalChatSender(msg.Sender)
}

// chatIsSystem reports whether a message is the app's own notice (a divider
// such as "New conversation") rather than something a person wrote.
func chatIsSystem(msg ChatMessage) bool {
	return strings.EqualFold(msg.Sender, chatSystemSender)
}

// chatIsAI reports whether a message is the assistant's answer.
func chatIsAI(msg ChatMessage) bool {
	return strings.EqualFold(msg.Sender, chatAISender)
}

// ── Loading and saving ───────────────────────────────────────────────────

// chatEnsureLoaded reads the saved history once per session.
func chatEnsureLoaded() {
	if chatLoaded {
		return
	}
	loadChatMessages()
}

// loadChatMessages reads every tab from the saved chat section, splitting a
// history saved before the tabs existed.
func loadChatMessages() {
	var sec *ChatSection
	if userConfigCache != nil {
		sec = userConfigCache.Chat
	}
	community, ai, saved := chatSplitHistory(sec)
	pending := false
	chatMutex.Lock()
	chatMessages, chatAIMessages, chatSavedMessages = community, ai, saved
	for t := chatTabCommunity; t < chatTabCount; t++ {
		seen := int64(0)
		if sec != nil && sec.Seen != nil {
			seen = sec.Seen[chatTabKeys[t]]
		}
		chatTabs[t] = chatTabData{seenAt: seen, unread: chatCountUnreadLocked(t, seen)}
		for _, msg := range *chatTabSliceLocked(t) {
			if msg.Pending {
				pending = true
			}
		}
	}
	chatMutex.Unlock()
	if sec != nil {
		if tab, ok := chatTabFromKey(sec.Tab); ok {
			chatActiveTab.Store(int32(tab))
		}
	}
	chatLoaded = true
	if pending {
		chatOutboxFlag.Store(true)
	}
}

// chatCountUnreadLocked counts messages from others newer than seen.
func chatCountUnreadLocked(tab chatTab, seen int64) int {
	if seen <= 0 {
		return 0
	}
	n := 0
	for _, msg := range *chatTabSliceLocked(tab) {
		if !chatIsOwn(msg) && !chatIsSystem(msg) && msg.Timestamp.UnixMilli() > seen {
			n++
		}
	}
	return n
}

// chatSplitHistory returns the three histories stored in sec.
func chatSplitHistory(sec *ChatSection) (community, ai, saved []ChatMessage) {
	if sec == nil {
		return []ChatMessage{}, []ChatMessage{}, []ChatMessage{}
	}
	if sec.Version >= chatHistoryVersion {
		return chatCleanHistory(sec.Messages, chatTabCap(chatTabCommunity)),
			chatCleanHistory(sec.AI, chatTabCap(chatTabAI)),
			chatCleanHistory(sec.Saved, chatTabCap(chatTabSaved))
	}
	// One mixed list from before the tabs. The assistant's answers, and the
	// question each one answered, belong to Juka AI; the app's own "System"
	// lines were error notices ("Groq API key not configured", raw network
	// errors) and are not kept; everything else is the community channel.
	var mixedCommunity, mixedAI []ChatMessage
	for _, msg := range sec.Messages {
		switch {
		case chatIsSystem(msg):
			continue
		case chatIsAI(msg):
			if n := len(mixedCommunity); n > 0 && chatIsOwn(mixedCommunity[n-1]) {
				prompt := mixedCommunity[n-1]
				if gap := msg.Timestamp.Sub(prompt.Timestamp); gap >= 0 && gap <= 10*time.Minute {
					mixedAI = append(mixedAI, prompt)
					mixedCommunity = mixedCommunity[:n-1]
				}
			}
			mixedAI = append(mixedAI, msg)
		default:
			mixedCommunity = append(mixedCommunity, msg)
		}
	}
	return chatCleanHistory(mixedCommunity, chatTabCap(chatTabCommunity)),
		chatCleanHistory(mixedAI, chatTabCap(chatTabAI)),
		[]ChatMessage{}
}

// chatCleanHistory copies a saved history, gives every message an id (the
// selection and the reply target follow ids) and keeps the newest limit.
func chatCleanHistory(list []ChatMessage, limit int) []ChatMessage {
	if len(list) > limit {
		list = list[len(list)-limit:]
	}
	out := make([]ChatMessage, 0, len(list))
	for _, msg := range list {
		if msg.ID == "" {
			msg.ID = newChatMessageID()
		}
		out = append(out, msg)
	}
	return out
}

// chatSnapshotSection builds the chat section to persist from the live state.
func chatSnapshotSection() *ChatSection {
	chatMutex.Lock()
	sec := &ChatSection{
		Messages: append([]ChatMessage(nil), chatMessages...),
		AI:       append([]ChatMessage(nil), chatAIMessages...),
		Saved:    append([]ChatMessage(nil), chatSavedMessages...),
		Version:  chatHistoryVersion,
	}
	for t := chatTabCommunity; t < chatTabCount; t++ {
		if chatTabs[t].seenAt > 0 {
			if sec.Seen == nil {
				sec.Seen = make(map[string]int64, int(chatTabCount))
			}
			sec.Seen[chatTabKeys[t]] = chatTabs[t].seenAt
		}
	}
	chatMutex.Unlock()
	sec.Tab = chatTabKeys[chatActiveTabID()]
	if userConfigCache != nil && userConfigCache.Chat != nil {
		// Rebuilding the section must not drop the display name.
		sec.Name = userConfigCache.Chat.Name
	}
	return sec
}

// saveChatMessages writes the chat section now.
func saveChatMessages() {
	sec := chatSnapshotSection()
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	userConfigCache.Chat = sec
	saveUserConfig(userConfigCache)
}

// chatPersistSoon hands the chat section to the debounced user-config writer,
// so a busy channel is one card write every couple of seconds instead of one
// per message.
func chatPersistSoon() {
	sec := chatSnapshotSection()
	if userConfigCache == nil {
		userConfigCache = &UserConfig{Variables: UserVariables{Custom: make(map[string]interface{})}}
	}
	userConfigCache.Chat = sec
	saveUserConfigDebounced(userConfigCache)
}

// ── Scene lifecycle ──────────────────────────────────────────────────────

// chatSceneActive reports whether the Chat scene is the one on screen.
func chatSceneActive(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == chatSceneName
}

// chatTick runs once per frame from the main loop: it loads the history on the
// first frame, notices the Chat scene opening and closing, starts the sender
// when something is queued, and persists what background goroutines changed.
func chatTick(config *Config) {
	chatEnsureLoaded()
	chatRefreshCreds(config, time.Now(), false)
	chatSyncVisibility(config)
	if chatOutboxFlag.Swap(false) {
		chatEnsureSync()
		chatWakeSync()
	}
	if chatDirty.Swap(false) {
		chatPersistSoon()
	}
}

// chatSyncVisibility runs the enter/leave work when the Chat scene appears or
// goes away. The render calls it too, so the first frame of the scene already
// has its history even if the main loop ticks after drawing.
func chatSyncVisibility(config *Config) {
	visible := chatSceneActive(config)
	if visible == chatWasVisible {
		return
	}
	if visible {
		chatSceneEnter()
	} else {
		chatSceneLeave()
	}
}

// chatSceneEnter prepares the screen each time it opens: history loaded, the
// pad on the composer, the Discord connection asked for, and a "New messages"
// divider above whatever arrived while the user was away.
func chatSceneEnter() {
	chatWasVisible = true
	chatEnsureLoaded()
	chatComposerInline = false
	clearChatSelection()
	chatSuggestFocus = -1
	tab := chatActiveTabID()
	chatMarkDivider(tab)
	chatDiscordWanted.Store(true)
	chatEnsureSync()
	chatWakeSync()
}

// chatSceneLeave resets what belongs to one visit.
func chatSceneLeave() {
	chatWasVisible = false
	chatStopInlineTyping()
	chatStickSpeed = 0
	chatLiveTab.Store(-1)
	clearChatSelection()
	chatSuggestFocus = -1
	for t := chatTabCommunity; t < chatTabCount; t++ {
		chatViews[t].dividerID = ""
	}
	chatDirty.Store(true)
}

// chatMarkDivider remembers the first unread message of a tab, so the divider
// can mark where the new messages start.
func chatMarkDivider(tab chatTab) {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	chatViews[tab].dividerID = ""
	data := chatTabs[tab]
	if data.unread <= 0 || data.seenAt <= 0 {
		return
	}
	for _, msg := range *chatTabSliceLocked(tab) {
		if !chatIsOwn(msg) && !chatIsSystem(msg) && msg.Timestamp.UnixMilli() > data.seenAt {
			chatViews[tab].dividerID = msg.ID
			return
		}
	}
}

// chatMarkSeenLocked marks a tab read up to its newest message. chatMutex must
// be held.
func chatMarkSeenLocked(tab chatTab) {
	list := *chatTabSliceLocked(tab)
	if n := len(list); n > 0 {
		if ms := list[n-1].Timestamp.UnixMilli(); ms > chatTabs[tab].seenAt {
			chatTabs[tab].seenAt = ms
			chatDirty.Store(true)
		}
	}
	chatTabs[tab].unread = 0
}

// chatTabUnread returns one tab's unread count.
func chatTabUnread(tab chatTab) int {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	return chatTabs[tab].unread
}

// chatSelectTab switches the screen to tab: the draft of the tab being left is
// kept, the new tab's draft comes back, and the pad returns to the composer.
func chatSelectTab(tab chatTab) bool {
	if tab < 0 || tab >= chatTabCount {
		return false
	}
	cur := chatActiveTabID()
	if tab == cur {
		return false
	}
	chatViews[cur].draft = chatInputText
	chatInputText = chatViews[tab].draft
	clearChatSelection()
	chatSuggestFocus = -1
	chatActiveTab.Store(int32(tab))
	chatLiveTab.Store(-1)
	if chatWasVisible {
		chatMarkDivider(tab)
	}
	chatDirty.Store(true)
	return true
}

// ── Getting messages in ──────────────────────────────────────────────────

// newChatMessage builds a message from this device.
func newChatMessage(sender, text string) ChatMessage {
	return ChatMessage{
		ID:        newChatMessageID(),
		Sender:    sender,
		Text:      strings.TrimSpace(text),
		Timestamp: time.Now(),
		Own:       isLocalChatSender(sender),
	}
}

// chatOutboxPoke tells the main loop a message is queued. The sender loop is
// started from there (chatTick), not from whichever goroutine queued it.
func chatOutboxPoke() {
	chatOutboxFlag.Store(true)
	chatWakeSync()
}

// chatSubmitComposer sends a typed message to the tab on screen. askAI sends
// it to Juka AI instead (the keyboard's Ask AI key), switching to that tab so
// the answer is in view.
func chatSubmitComposer(text string, askAI bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	tab := chatActiveTabID()
	if askAI && tab != chatTabAI {
		chatSelectTab(chatTabAI)
		tab = chatTabAI
	}
	switch tab {
	case chatTabAI:
		if !chatAskAI(text) {
			// Nothing is lost while the key is missing: the text stays in the
			// composer and the setup hint says where the key goes.
			chatInputText = text
			return
		}
	case chatTabSaved:
		chatAppendSaved(text)
	default:
		sendChatMessage(chatDisplayName(), text)
	}
	// The caller plays the press sound: the keyboard's Send already did.
	chatJumpToLatest()
}

// chatAskAI queues a question for Juka. It reports false (and explains) when
// no Groq key is configured, so the caller can keep the text.
func chatAskAI(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if groqAPIKey() == "" {
		groqStatus = "Groq: no API key"
		showToast("Add your Groq API key to talk to Juka", ToastWarn())
		return false
	}
	msg := newChatMessage(chatDisplayName(), text)
	msg.Own = true
	msg.Pending = true
	chatMutex.Lock()
	chatAppendLocked(chatTabAI, msg)
	chatMutex.Unlock()
	chatDirty.Store(true)
	chatOutboxPoke()
	return true
}

// chatAppendSaved stores a note to self.
func chatAppendSaved(text string) {
	msg := newChatMessage(chatDisplayName(), text)
	msg.Own = true
	if msg.Text == "" {
		return
	}
	chatMutex.Lock()
	chatAppendLocked(chatTabSaved, msg)
	chatMutex.Unlock()
	chatDirty.Store(true)
}

// chatSaveCopy keeps a copy of any message in the Saved tab.
func chatSaveCopy(msg ChatMessage) {
	copyMsg := ChatMessage{
		ID:        newChatMessageID(),
		Sender:    chatDisplayName(),
		Text:      msg.Text,
		ImageURL:  msg.ImageURL,
		Timestamp: time.Now(),
		Own:       true,
	}
	if !chatIsOwn(msg) {
		// Who said it travels with the note; a quote without text draws as a
		// one-line "From …" header.
		copyMsg.Quote = &ChatQuote{Author: msg.Sender}
	}
	chatMutex.Lock()
	chatAppendLocked(chatTabSaved, copyMsg)
	chatMutex.Unlock()
	chatDirty.Store(true)
	showToast("Saved", ToastInfo())
}

// chatAttachTab is the tab that asked the File Explorer for a picture.
var chatAttachTab = chatTabCommunity

// chatAttachLocalImage adds a picture picked in the File Explorer to the tab
// that asked for it, as this device's own message. It stays on the device: a
// local file has no URL the channel could show.
func chatAttachLocalImage(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	tab := chatAttachTab
	if tab == chatTabAI {
		tab = chatTabSaved
	}
	msg := ChatMessage{
		ID:        newChatMessageID(),
		Sender:    chatDisplayName(),
		Timestamp: time.Now(),
		ImageURL:  path,
		Own:       true,
	}
	chatMutex.Lock()
	chatAppendLocked(tab, msg)
	chatMutex.Unlock()
	chatDirty.Store(true)
}

// chatAddMarker starts a new Juka AI conversation: a divider in the history,
// and the model only sees what comes after it.
func chatAddMarker() bool {
	chatMutex.Lock()
	list := chatAIMessages
	if n := len(list); n == 0 || chatIsSystem(list[n-1]) {
		chatMutex.Unlock()
		return false
	}
	chatAppendLocked(chatTabAI, ChatMessage{
		ID:        newChatMessageID(),
		Sender:    chatSystemSender,
		Text:      "New conversation",
		Timestamp: time.Now(),
	})
	chatMutex.Unlock()
	chatDirty.Store(true)
	return true
}

// chatRetryMessage queues a failed message again.
func chatRetryMessage(tab chatTab, id string) bool {
	ok := chatUpdateMessage(tab, id, func(m *ChatMessage) {
		m.Error = ""
		m.Pending = true
	})
	if ok {
		chatOutboxPoke()
	}
	return ok
}

// chatClearTab empties one tab's history on this device (the chat_clear
// trigger). The community channel itself is untouched: its recent messages
// come back the next time the history is fetched.
func chatClearTab(tab chatTab) {
	if tab < 0 || tab >= chatTabCount {
		return
	}
	chatMutex.Lock()
	*chatTabSliceLocked(tab) = []ChatMessage{}
	chatTabs[tab].unread = 0
	chatMutex.Unlock()
	if tab == chatActiveTabID() {
		clearChatSelection()
	}
	view := &chatViews[tab]
	view.scroll, view.target, view.anchorID, view.dividerID = 0, 0, "", ""
	saveChatMessages()
}

// chatPendingCount is the number of queued messages in a tab.
func chatPendingCount(tab chatTab) int {
	n := 0
	for _, msg := range chatTabMessages(tab) {
		if msg.Pending && msg.Error == "" {
			n++
		}
	}
	return n
}

// chatAppendIncoming adds a message that arrived from Discord. A copy that is
// already there (the same id, or the echo of a message this device just sent
// through the shared bot) is skipped; it reports whether the message was new.
func chatAppendIncoming(msg ChatMessage, echo bool) bool {
	chatMutex.Lock()
	list := chatMessages
	stop := len(list) - 80
	if stop < 0 {
		stop = 0
	}
	for i := len(list) - 1; i >= stop; i-- {
		have := list[i]
		if msg.ID != "" && have.ID == msg.ID {
			chatMutex.Unlock()
			return false
		}
		if echo && have.Own && have.Text == msg.Text {
			if gap := msg.Timestamp.Sub(have.Timestamp); gap > -5*time.Minute && gap < 5*time.Minute {
				if !isDiscordSnowflake(have.ID) && msg.ID != "" {
					chatUpdateLocked(chatTabCommunity, have.ID, func(m *ChatMessage) {
						m.ID = msg.ID
						m.Pending = false
						m.Error = ""
					})
				}
				chatMutex.Unlock()
				chatDirty.Store(true)
				return false
			}
		}
	}
	chatAppendLocked(chatTabCommunity, msg)
	if !chatIsOwn(msg) {
		if chatLiveTab.Load() == int32(chatTabCommunity) {
			chatMarkSeenLocked(chatTabCommunity)
		} else {
			chatTabs[chatTabCommunity].unread++
		}
	}
	chatMutex.Unlock()
	chatDirty.Store(true)
	return true
}
