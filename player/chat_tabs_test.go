package main

// chat_tabs_test.go — the Chat screen's tabs, queue, wrapping, controls and
// layout.
//
// resetChatTestState is the isolation every chat test starts from: the three
// histories, the per-tab view state, the selection, the hit targets of the
// last frame and the sender loop's stand-ins are saved, cleared, and put back
// when the test ends. Nothing here reaches the network: Discord is stubbed per
// test, and Groq answers from an httptest server.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// resetChatTestState isolates the chat globals for one test.
func resetChatTestState(t *testing.T) {
	t.Helper()
	chatMutex.Lock()
	prevMessages, prevAI, prevSaved := chatMessages, chatAIMessages, chatSavedMessages
	prevTabs, prevStream, prevTitle := chatTabs, chatStream, chatChannelTitle
	chatMutex.Unlock()
	prevViews, prevView, prevItems := chatViews, chatView, chatItems
	prevActive, prevLive := chatActiveTab.Load(), chatLiveTab.Load()
	prevDirty, prevOutbox := chatDirty.Load(), chatOutboxFlag.Load()
	prevLoaded, prevVisible := chatLoaded, chatWasVisible
	prevText, prevTyping := chatInputText, chatInputActive
	prevInline, prevSkip, prevSeen := chatComposerInline, chatSkipSpace, chatKeyboardSeenAt
	prevStick := chatStickSpeed
	prevSelected, prevSelectedID, prevBar := chatSelected, chatSelectedID, chatSelectionBar
	prevReply, prevSuggest, prevAttach := chatReplyTo, chatSuggestFocus, chatAttachTab
	prevProfile, prevMembers := chatProfile, chatMembers
	prevWanted, prevNet, prevGateway := chatDiscordWanted.Load(), chatDiscordNet.Load(), chatGatewayUp.Load()
	prevBusy, prevRetry := chatAIBusy.Load(), chatRetryRequested.Load()
	prevConnect, prevPost, prevReady := chatDiscordConnectFn, chatDiscordPost, chatDiscordReady
	prevEndpoint := chatGroqEndpoint
	prevDiscordAt, prevBackoff, prevSendAt := chatDiscordRetryAt, chatDiscordBackoff, chatSendRetryAt
	chatCredMu.Lock()
	prevCreds := chatCreds
	chatCredMu.Unlock()
	chatAIRetryMu.Lock()
	prevAIAt, prevAIBackoff := chatAIRetryAt, chatAIBackoff
	chatAIRetryMu.Unlock()

	wipe := func() {
		chatMutex.Lock()
		chatMessages, chatAIMessages, chatSavedMessages = nil, nil, nil
		chatTabs, chatStream, chatChannelTitle = [chatTabCount]chatTabData{}, chatStreamState{}, ""
		chatMutex.Unlock()
		chatViews, chatView, chatItems = [chatTabCount]chatTabView{}, chatViewState{}, nil
		chatActiveTab.Store(int32(chatTabCommunity))
		chatLiveTab.Store(-1)
		chatDirty.Store(false)
		chatOutboxFlag.Store(false)
		// The history is the test's own: chatEnsureLoaded must not replace it
		// with whatever the saved user config holds.
		chatLoaded, chatWasVisible = true, false
		chatInputText, chatInputActive = "", false
		chatComposerInline, chatSkipSpace, chatKeyboardSeenAt = false, false, time.Time{}
		chatStickSpeed = 0
		chatSelected, chatSelectedID, chatSelectionBar = -1, "", chatSelection{}
		chatReplyTo, chatSuggestFocus, chatAttachTab = nil, -1, chatTabCommunity
		chatProfile, chatMembers = chatProfileCard{}, chatMembersPanel{}
		chatDiscordWanted.Store(false)
		chatSetNet(chatNetIdle)
		chatGatewayUp.Store(false)
		chatAIBusy.Store(false)
		chatRetryRequested.Store(false)
		chatDiscordRetryAt, chatDiscordBackoff, chatSendRetryAt = time.Time{}, 0, time.Time{}
		chatCredMu.Lock()
		chatCreds = chatCredSnapshot{}
		chatCredMu.Unlock()
		chatAIRetryReset()
		chatResetHits()
		clearChatLayoutCache()
		chatMemberCountCache.ok = false
		chatPlaceholderCache.text = ""
		chatComposerShown.font = nil
	}
	wipe()
	// Nothing in a test may reach Discord: connecting and posting are refused
	// unless the test installs stand-ins of its own.
	chatDiscordConnectFn = func(string, string, string) error { return errors.New("no Discord in tests") }
	chatDiscordPost = func(string, string, string) (string, error) { return "", errors.New("no Discord in tests") }
	chatDiscordReady = func() bool { return false }

	t.Cleanup(func() {
		wipe()
		chatMutex.Lock()
		chatMessages, chatAIMessages, chatSavedMessages = prevMessages, prevAI, prevSaved
		chatTabs, chatStream, chatChannelTitle = prevTabs, prevStream, prevTitle
		chatMutex.Unlock()
		chatViews, chatView, chatItems = prevViews, prevView, prevItems
		chatActiveTab.Store(prevActive)
		chatLiveTab.Store(prevLive)
		chatDirty.Store(prevDirty)
		chatOutboxFlag.Store(prevOutbox)
		chatLoaded, chatWasVisible = prevLoaded, prevVisible
		chatInputText, chatInputActive = prevText, prevTyping
		chatComposerInline, chatSkipSpace, chatKeyboardSeenAt = prevInline, prevSkip, prevSeen
		chatStickSpeed = prevStick
		chatSelected, chatSelectedID, chatSelectionBar = prevSelected, prevSelectedID, prevBar
		chatReplyTo, chatSuggestFocus, chatAttachTab = prevReply, prevSuggest, prevAttach
		chatProfile, chatMembers = prevProfile, prevMembers
		chatDiscordWanted.Store(prevWanted)
		chatDiscordNet.Store(prevNet)
		chatGatewayUp.Store(prevGateway)
		chatAIBusy.Store(prevBusy)
		chatRetryRequested.Store(prevRetry)
		chatDiscordConnectFn, chatDiscordPost, chatDiscordReady = prevConnect, prevPost, prevReady
		chatGroqEndpoint = prevEndpoint
		chatDiscordRetryAt, chatDiscordBackoff, chatSendRetryAt = prevDiscordAt, prevBackoff, prevSendAt
		chatCredMu.Lock()
		chatCreds = prevCreds
		chatCredMu.Unlock()
		chatAIRetryMu.Lock()
		chatAIRetryAt, chatAIBackoff = prevAIAt, prevAIBackoff
		chatAIRetryMu.Unlock()
	})
}

// chatSetOnlineForTest pins the connectivity monitor for one test.
func chatSetOnlineForTest(t *testing.T, online bool) {
	t.Helper()
	netMon.mu.Lock()
	prevOnline, prevChecked := netMon.online, netMon.checked
	netMon.online, netMon.checked = online, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked = prevOnline, prevChecked
		netMon.mu.Unlock()
	})
}

// chatSceneFixture is a two-scene config (Home, Chat) with the Chat scene on
// screen. The scene state goBackScene and changeSceneTo move is put back
// when the test ends.
func chatSceneFixture(t *testing.T) *Config {
	t.Helper()
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{
		{Name: "Main", Layout: "home"},
		{Name: chatSceneName, Elements: []Element{{Type: "chat", X: 24, Y: 72, Width: "1232", Height: "584"}}},
	}
	prevScene, prevPhase, prevPending := currentSceneIndex, transitionPhase, pendingSceneIndex
	prevHistory := append([]int(nil), sceneHistory...)
	prevKeyboard := virtualKeyboardActive
	t.Cleanup(func() {
		currentSceneIndex, transitionPhase, pendingSceneIndex = prevScene, prevPhase, prevPending
		sceneHistory = prevHistory
		virtualKeyboardActive = prevKeyboard
	})
	currentSceneIndex, transitionPhase, pendingSceneIndex = 1, "none", 1
	sceneHistory = nil
	virtualKeyboardActive = false
	return config
}

// chatLeftScene reports whether the last press started the way back to Home,
// and re-arms the fixture for the next press.
func chatLeftScene() bool {
	left := transitionPhase == "fade-out" && pendingSceneIndex == 0
	transitionPhase, pendingSceneIndex = "none", 1
	return left
}

// chatPad builds a pad press.
func chatPad(b uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: b}
}

// chatKey builds a key press with modifiers.
func chatKey(sym sdl.Keycode, mod uint16) *sdl.KeyboardEvent {
	return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym, Mod: mod}}
}

// chatTestFont is the body font the chat draws with, from the shipped config.
func chatTestFont(t *testing.T) *ttf.Font {
	t.Helper()
	config := layoutTestConfig(t)
	font := getFontSized(config, "small", chatBodyPx)
	if font == nil {
		t.Fatal("the chat body font did not load")
	}
	return font
}

// ── Wrapping ─────────────────────────────────────────────────────────────

func TestChatWrapTextBreaksAtWords(t *testing.T) {
	font := chatTestFont(t)
	text := "The build I tried last night stutters on the scrolling shelves and the save state came back with the wrong slot name"
	spaceW, _ := measureText(font, " ")
	wordW, _ := measureText(font, "stutters")
	maxW := wordW*3 + spaceW*2
	lay := chatWrapText(font, text, maxW, 0)
	if len(lay.lines) < 3 {
		t.Fatalf("wrapped into %d lines at %dpx, want several", len(lay.lines), maxW)
	}
	lines := make([]string, 0, len(lay.lines))
	for i, line := range lay.lines {
		got := chatLineText(line)
		if w, _ := measureText(font, got); w > maxW+2 {
			t.Errorf("line %d %q is %dpx, past the %dpx width", i, got, w, maxW)
		}
		if line.w > lay.width {
			t.Errorf("line %d is wider (%d) than the layout's width %d", i, line.w, lay.width)
		}
		lines = append(lines, got)
	}
	if got, want := strings.Join(lines, " "), strings.Join(strings.Fields(text), " "); got != want {
		t.Fatalf("wrapped text = %q, want every word once and in order: %q", got, want)
	}
	if lay.truncated {
		t.Error("a short message was marked as cut")
	}
}

func TestChatWrapTextKeepsParagraphs(t *testing.T) {
	font := chatTestFont(t)
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"blank lines collapse to one", "one\n\n\n\ntwo", []string{"one", "", "two"}},
		{"leading blank lines go", "\n\n  \nhi", []string{"hi"}},
		{"windows line ends", "a\r\nb", []string{"a", "b"}},
		{"tabs are spaces", "a\tb", []string{"a b"}},
		{"trailing spaces go", "hi   ", []string{"hi"}},
		{"only spaces", "   \n  ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lay := chatWrapText(font, tc.text, 2000, 0)
			got := make([]string, 0, len(lay.lines))
			for _, line := range lay.lines {
				got = append(got, chatLineText(line))
			}
			if len(got) != len(tc.want) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChatWrapTextSplitsLongWordsSafely(t *testing.T) {
	font := chatTestFont(t)
	maxW, _ := measureText(font, "mmmmmmmm")
	word := strings.Repeat("abcdefghij", 12)
	lay := chatWrapText(font, word, maxW, 0)
	if len(lay.lines) < 2 {
		t.Fatalf("a %d-character word fitted %dpx on one line", len(word), maxW)
	}
	var joined strings.Builder
	for i, line := range lay.lines {
		text := chatLineText(line)
		if w, _ := measureText(font, text); w > maxW+2 {
			t.Errorf("line %d %q is %dpx, past %dpx", i, text, w, maxW)
		}
		joined.WriteString(text)
	}
	if joined.String() != word {
		t.Fatalf("the split word reads %q, want %q", joined.String(), word)
	}

	// A combining accent stays with its letter, and every line is valid UTF-8.
	accented := strings.Repeat("e"+string(rune(0x301)), 60)
	lay = chatWrapText(font, accented, maxW, 0)
	for i, line := range lay.lines {
		text := chatLineText(line)
		if !utf8.ValidString(text) {
			t.Fatalf("line %d is not valid UTF-8: %q", i, text)
		}
		if r, _ := utf8.DecodeRuneInString(text); chatJoinsPrevious(r) {
			t.Fatalf("line %d starts with a combining mark: %q", i, text)
		}
	}
}

func TestChatWrapTextMarksLinks(t *testing.T) {
	font := chatTestFont(t)
	link := "https://example.com/story"
	lay := chatWrapText(font, "read ("+link+"), then reply", 2000, 0)
	found := false
	for _, line := range lay.lines {
		for _, seg := range line.segs {
			if seg.url == "" {
				continue
			}
			if seg.url != link || seg.text != link {
				t.Fatalf("link run %q opens %q, want exactly the link", seg.text, seg.url)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("the link was not marked as a link run")
	}

	// A link longer than a line is split, and every part still opens it.
	long := "https://example.com/" + strings.Repeat("segment/", 20)
	maxW, _ := measureText(font, "https://example.com/segment")
	lay = chatWrapText(font, long, maxW, 0)
	if len(lay.lines) < 2 {
		t.Fatalf("a %d-character link fitted %dpx on one line", len(long), maxW)
	}
	var parts strings.Builder
	for _, line := range lay.lines {
		for _, seg := range line.segs {
			if seg.url != long {
				t.Fatalf("a part of the long link opens %q, want %q", seg.url, long)
			}
			parts.WriteString(seg.text)
		}
	}
	if parts.String() != long {
		t.Fatalf("the split link reads %q, want %q", parts.String(), long)
	}
}

func TestChatWrapTextCapsLongMessages(t *testing.T) {
	font := chatTestFont(t)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	lay := chatWrapText(font, b.String(), 2000, 5)
	if len(lay.lines) != 5 || !lay.truncated {
		t.Fatalf("%d lines (cut=%v), want the 5-line cap", len(lay.lines), lay.truncated)
	}
	if last := chatLineText(lay.lines[4]); !strings.HasSuffix(last, "…") {
		t.Fatalf("the last kept line %q does not end with an ellipsis", last)
	}
}

func TestChatBreakOffsetsKeepClusters(t *testing.T) {
	zwj := string(chatZWJ)
	cases := []struct {
		name string
		text string
		want []int
	}{
		{"plain", "abc", []int{1, 2, 3}},
		{"combining accent", "e" + string(rune(0x301)) + "x", []int{3, 4}},
		{"joined emoji", string(rune(0x1f469)) + zwj + string(rune(0x1f4bb)) + "x", []int{11, 12}},
		{"skin tone", string(rune(0x1f44d)) + string(rune(0x1f3fb)) + "!", []int{8, 9}},
		{"variation selector", string(rune(0x2764)) + string(rune(0xfe0f)) + "a", []int{6, 7}},
	}
	for _, tc := range cases {
		got := chatBreakOffsets(tc.text)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: break offsets = %v, want %v", tc.name, got, tc.want)
		}
		if first := chatFirstBreak(tc.text); first != tc.want[0] {
			t.Errorf("%s: first character ends at %d, want %d", tc.name, first, tc.want[0])
		}
	}
}

func TestChatURLSpansDropTrailingPunctuation(t *testing.T) {
	s := "(see https://example.com/a), then https://x.org."
	spans := chatURLSpans(s)
	want := []string{"https://example.com/a", "https://x.org"}
	if len(spans) != len(want) {
		t.Fatalf("spans = %v, want %d links", spans, len(want))
	}
	for i, sp := range spans {
		if got := s[sp[0]:sp[1]]; got != want[i] {
			t.Errorf("span %d covers %q, want %q", i, got, want[i])
		}
	}
	if chatURLSpans("no links here") != nil {
		t.Error("plain text produced link spans")
	}
	word := "(https://x.org/a),"
	pieces := chatWordPieces(word, 0, len(word), chatURLSpans(word))
	if len(pieces) != 3 || pieces[0].text != "(" || pieces[1].text != "https://x.org/a" ||
		pieces[1].url != "https://x.org/a" || pieces[2].text != ")," || pieces[2].url != "" {
		t.Fatalf("pieces = %+v, want the bracket, the link and the punctuation", pieces)
	}
}

func TestChatInitials(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Juka AI", "JA"},
		{"steppa", "S"},
		{"", "?"},
		{"  ana maria lopez", "AL"},
		{"émile zola", "ÉZ"},
		{"-- x", "X"},
		{"123 abc", "1A"},
	} {
		if got := chatInitials(tc.name); got != tc.want {
			t.Errorf("chatInitials(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestChatClockLabel(t *testing.T) {
	at := time.Date(2026, 11, 8, 15, 4, 0, 0, time.Local)
	if got := chatClockLabel(at, false); got != "15:04" {
		t.Errorf("24-hour clock = %q, want 15:04", got)
	}
	if got := chatClockLabel(at, true); got != "3:04 PM" {
		t.Errorf("12-hour clock = %q, want 3:04 PM", got)
	}
	if got := chatClockLabel(time.Time{}, false); got != "" {
		t.Errorf("a message without a time shows %q", got)
	}
}

// ── History, tabs and persistence ────────────────────────────────────────

// chatIDs lists the ids of a history, for compact comparisons.
func chatIDs(list []ChatMessage) string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// A history saved before the tabs existed is one mixed list: the assistant's
// answers and the questions they answered move to Juka AI, the app's own
// "System" error lines are dropped, and the rest stays in the community tab.
func TestChatSplitHistoryMovesTheAssistantOut(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	sec := &ChatSection{Messages: []ChatMessage{
		{ID: "1", Sender: "Alex", Text: "hi", Timestamp: t0},
		{ID: "2", Sender: "You", Own: true, Text: "what is jukahub?", Timestamp: t0.Add(time.Minute)},
		{ID: "3", Sender: "Juka AI", Text: "A launcher.", Timestamp: t0.Add(time.Minute + 5*time.Second)},
		{ID: "4", Sender: "System", Text: "Groq API key not configured", Timestamp: t0.Add(2 * time.Minute)},
		{ID: "5", Sender: "Bob", Text: "yo", Timestamp: t0.Add(3 * time.Minute)},
		{ID: "6", Sender: "You", Own: true, Text: "see you", Timestamp: t0.Add(4 * time.Minute)},
		{ID: "7", Sender: "Juka AI", Text: "A late answer.", Timestamp: t0.Add(40 * time.Minute)},
		{Sender: "Bob", Text: "no id", Timestamp: t0.Add(50 * time.Minute)},
	}}
	community, ai, saved := chatSplitHistory(sec)
	if got := chatIDs(ai); got != "2,3,7" {
		t.Errorf("Juka AI tab = %s, want the question, its answer and the late answer", got)
	}
	if len(community) != 4 || chatIDs(community[:3]) != "1,5,6" {
		t.Fatalf("community tab = %s, want 1,5,6 and the message without an id", chatIDs(community))
	}
	if community[3].ID == "" || community[3].Text != "no id" {
		t.Errorf("a message without an id was not given one: %+v", community[3])
	}
	if len(saved) != 0 {
		t.Errorf("an old history produced %d saved notes", len(saved))
	}
}

func TestChatSplitHistoryKeepsSavedTabs(t *testing.T) {
	sec := &ChatSection{
		Version:  chatHistoryVersion,
		Messages: []ChatMessage{{ID: "c", Sender: "Alex", Text: "x"}},
		AI:       []ChatMessage{{ID: "a", Sender: "Juka AI", Text: "y"}},
		Saved:    []ChatMessage{{Sender: "You", Own: true, Text: "note"}},
	}
	community, ai, saved := chatSplitHistory(sec)
	if len(community) != 1 || len(ai) != 1 || len(saved) != 1 {
		t.Fatalf("tabs = %d/%d/%d messages, want 1/1/1", len(community), len(ai), len(saved))
	}
	if saved[0].ID == "" {
		t.Error("a saved note without an id did not get one")
	}
	c, a, s := chatSplitHistory(nil)
	if c == nil || a == nil || s == nil || len(c)+len(a)+len(s) != 0 {
		t.Error("no saved section should give three empty tabs")
	}
}

func TestChatHistoryRoundTripsThroughTheUserConfig(t *testing.T) {
	resetChatTestState(t)
	prevCache := userConfigCache
	t.Cleanup(func() { userConfigCache = prevCache })
	userConfigCache = &UserConfig{
		Variables: UserVariables{Custom: map[string]interface{}{}},
		Chat:      &ChatSection{Name: "Steppa"},
	}

	now := time.Now().Truncate(time.Millisecond)
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "100000000000000001", Sender: "Alex", Text: "old", Timestamp: now.Add(-2 * time.Hour)},
		{ID: "100000000000000002", Sender: "Alex", Text: "new", Timestamp: now.Add(-time.Minute)},
		{ID: "loc1", Sender: "Steppa", Own: true, Text: "queued", Pending: true, Timestamp: now},
	}
	chatAIMessages = []ChatMessage{{ID: "q", Sender: "Steppa", Own: true, Text: "why?", Error: "Juka couldn't answer — try again", Timestamp: now}}
	chatSavedMessages = []ChatMessage{{ID: "s", Sender: "Steppa", Own: true, Text: "note", Timestamp: now}}
	chatTabs[chatTabCommunity].seenAt = now.Add(-time.Hour).UnixMilli()
	chatMutex.Unlock()
	chatActiveTab.Store(int32(chatTabSaved))

	sec := chatSnapshotSection()
	if sec.Version != chatHistoryVersion || sec.Tab != "saved" || sec.Name != "Steppa" {
		t.Fatalf("snapshot = version %d, tab %q, name %q", sec.Version, sec.Tab, sec.Name)
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ChatSection
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The next start reads it back.
	userConfigCache.Chat = &back
	chatMutex.Lock()
	chatMessages, chatAIMessages, chatSavedMessages = nil, nil, nil
	chatTabs = [chatTabCount]chatTabData{}
	chatMutex.Unlock()
	chatActiveTab.Store(int32(chatTabCommunity))
	chatOutboxFlag.Store(false)
	loadChatMessages()

	if chatActiveTabID() != chatTabSaved {
		t.Errorf("reopened on tab %d, want Saved", chatActiveTabID())
	}
	community := chatTabMessages(chatTabCommunity)
	if len(community) != 3 || !community[2].Pending {
		t.Fatalf("community = %+v, want the queued message still queued", community)
	}
	if ai := chatTabMessages(chatTabAI); len(ai) != 1 || ai[0].Error == "" {
		t.Errorf("Juka AI = %+v, want the failed question with its reason", ai)
	}
	if n := len(chatTabMessages(chatTabSaved)); n != 1 {
		t.Errorf("Saved holds %d notes, want 1", n)
	}
	if got := chatTabUnread(chatTabCommunity); got != 1 {
		t.Errorf("unread = %d, want the one message newer than the last read", got)
	}
	if !chatOutboxFlag.Load() {
		t.Error("a queued message did not wake the sender on load")
	}
}

func TestChatSubmitComposerSendsToTheTabOnScreen(t *testing.T) {
	resetChatTestState(t)
	prevCfg := appConfig
	t.Cleanup(func() { appConfig = prevCfg })
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	chatSubmitComposer("  hello  ", false)
	community := chatTabMessages(chatTabCommunity)
	if len(community) != 1 || community[0].Text != "hello" || !community[0].Own || !community[0].Pending {
		t.Fatalf("community = %+v, want one queued message of this device", community)
	}
	if !chatDiscordWanted.Load() || !chatOutboxFlag.Load() {
		t.Error("a queued community message did not wake the sender")
	}

	chatSelectTab(chatTabSaved)
	chatSubmitComposer("buy milk", false)
	if saved := chatTabMessages(chatTabSaved); len(saved) != 1 || saved[0].Pending || !saved[0].Own {
		t.Fatalf("saved = %+v, want one local note", saved)
	}

	// Juka AI without a key keeps the question in the composer.
	chatSelectTab(chatTabAI)
	chatSubmitComposer("what is new?", false)
	if n := len(chatTabMessages(chatTabAI)); n != 0 {
		t.Fatalf("a question was queued without a Groq key (%d messages)", n)
	}
	if chatInputText != "what is new?" {
		t.Fatalf("composer = %q, want the question kept", chatInputText)
	}

	// With a key it is queued for the sender.
	appConfig.Variables.Custom["groq_api_key"] = "gsk_test"
	chatInputText = ""
	chatSubmitComposer("what is new?", false)
	if ai := chatTabMessages(chatTabAI); len(ai) != 1 || !ai[0].Pending || !ai[0].Own {
		t.Fatalf("ai = %+v, want one queued question", ai)
	}

	// The keyboard's Ask AI sends from any tab to Juka AI, and shows it.
	chatSelectTab(chatTabCommunity)
	chatSubmitComposer("and music?", true)
	if chatActiveTabID() != chatTabAI || len(chatTabMessages(chatTabAI)) != 2 {
		t.Fatal("Ask AI did not send the text to the Juka AI tab")
	}
	if n := len(chatTabMessages(chatTabCommunity)); n != 1 {
		t.Errorf("Ask AI also posted to the community (%d messages)", n)
	}
}

func TestChatAppendIncomingSkipsCopies(t *testing.T) {
	resetChatTestState(t)
	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "abc123", Sender: "You", Own: true, Pending: true, Text: "hello", Timestamp: now}}
	chatMutex.Unlock()

	echo := ChatMessage{ID: "123456789012345678", Sender: "You", Text: "hello", Timestamp: now.Add(2 * time.Second)}
	if chatAppendIncoming(echo, true) {
		t.Fatal("this device's own message came back through the bot and was added twice")
	}
	list := chatTabMessages(chatTabCommunity)
	if len(list) != 1 || list[0].ID != echo.ID || list[0].Pending {
		t.Fatalf("history = %+v, want the queued copy confirmed under Discord's id", list)
	}
	if chatAppendIncoming(echo, false) {
		t.Fatal("the same Discord message was added twice")
	}

	other := ChatMessage{ID: "123456789012345679", Sender: "Alex", Text: "hey", Timestamp: now.Add(time.Minute)}
	if !chatAppendIncoming(other, false) {
		t.Fatal("a new message was dropped")
	}
	if got := chatTabUnread(chatTabCommunity); got != 1 {
		t.Fatalf("unread = %d, want 1 while the tab is not on screen", got)
	}
	// On screen at its newest message, a tab reads what arrives.
	chatLiveTab.Store(int32(chatTabCommunity))
	third := ChatMessage{ID: "123456789012345680", Sender: "Alex", Text: "still there?", Timestamp: now.Add(2 * time.Minute)}
	chatAppendIncoming(third, false)
	if got := chatTabUnread(chatTabCommunity); got != 0 {
		t.Fatalf("unread = %d, want 0 for a tab being read", got)
	}
}

func TestChatMergeCommunityOrdersAndDedupes(t *testing.T) {
	resetChatTestState(t)
	t0 := time.Now().Add(-time.Hour)
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "100000000000000002", Sender: "Alex", Text: "b", Timestamp: t0.Add(2 * time.Minute)},
		{ID: "loc1", Sender: "You", Own: true, Text: "mine", Timestamp: t0.Add(3 * time.Minute)},
	}
	chatTabs[chatTabCommunity].seenAt = t0.UnixMilli()
	chatMutex.Unlock()

	fresh := []ChatMessage{
		{ID: "100000000000000001", Sender: "Alex", Text: "a", Timestamp: t0.Add(time.Minute)},
		{ID: "100000000000000002", Sender: "Alex", Text: "b", Timestamp: t0.Add(2 * time.Minute)},
		{ID: "100000000000000003", Sender: "You", Text: "mine", Timestamp: t0.Add(3*time.Minute + time.Second)},
	}
	chatMergeCommunity(fresh, []bool{false, false, true})
	if got := chatIDs(chatTabMessages(chatTabCommunity)); got != "100000000000000001,100000000000000002,loc1" {
		t.Fatalf("history = %s, want the fetched message in time order and no copies", got)
	}
	if got := chatTabUnread(chatTabCommunity); got != 1 {
		t.Fatalf("unread = %d, want the one message newer than the last read", got)
	}
}

func TestChatAnswerLandsUnderItsQuestion(t *testing.T) {
	resetChatTestState(t)
	chatMutex.Lock()
	chatAIMessages = []ChatMessage{
		{ID: "q1", Sender: "You", Own: true, Text: "first?"},
		{ID: "q2", Sender: "You", Own: true, Text: "second?"},
	}
	chatInsertAfterLocked(chatTabAI, "q1", ChatMessage{ID: "a1", Sender: chatAISender, Text: "one"})
	chatInsertAfterLocked(chatTabAI, "q1", ChatMessage{ID: "a1b", Sender: chatAISender, Text: "more"})
	chatInsertAfterLocked(chatTabAI, "gone", ChatMessage{ID: "a3", Sender: chatAISender, Text: "end"})
	got := chatIDs(chatAIMessages)
	chatMutex.Unlock()
	if got != "q1,a1,a1b,q2,a3" {
		t.Fatalf("history = %s, want each answer under its question", got)
	}

	// Updates copy the history, so a snapshot already handed out never changes.
	snap := chatTabMessages(chatTabAI)
	chatUpdateMessage(chatTabAI, "q1", func(m *ChatMessage) { m.Text = "edited" })
	if snap[0].Text != "first?" {
		t.Fatal("an update changed a snapshot a frame was reading")
	}
	if chatTabMessages(chatTabAI)[0].Text != "edited" {
		t.Fatal("the update was lost")
	}
}

func TestBuildGroqMessagesStartsAtTheLastNewChat(t *testing.T) {
	history := []ChatMessage{
		{Sender: "You", Own: true, Text: "old question"},
		{Sender: chatAISender, Text: "old answer"},
		{Sender: chatSystemSender, Text: "New conversation"},
		{Sender: "You", Own: true, Text: "hi"},
		{Sender: chatAISender, Text: "hello"},
		{Sender: "You", Own: true, Text: "unanswered", Error: "Juka couldn't answer — try again"},
		{Sender: "You", Own: true, Text: "how?", Pending: true},
	}
	msgs := buildGroqMessagesFrom(history, "how?")
	if msgs[0]["role"] != "system" {
		t.Fatalf("the request does not start with the system prompt: %v", msgs)
	}
	var roles, contents []string
	for _, m := range msgs[1:] {
		roles = append(roles, m["role"])
		contents = append(contents, m["content"])
	}
	if strings.Join(roles, ",") != "user,assistant,user" || strings.Join(contents, "|") != "hi|hello|how?" {
		t.Fatalf("request = %v, want the conversation since New chat and the question once", msgs)
	}
}

func TestChatNewConversationMarker(t *testing.T) {
	resetChatTestState(t)
	if chatAddMarker() {
		t.Fatal("an empty conversation got a New conversation divider")
	}
	chatMutex.Lock()
	chatAIMessages = []ChatMessage{{ID: "q", Sender: "You", Own: true, Text: "hi"}}
	chatMutex.Unlock()
	if !chatAddMarker() {
		t.Fatal("New chat did not add its divider")
	}
	if chatAddMarker() {
		t.Fatal("two dividers in a row")
	}
	if list := chatTabMessages(chatTabAI); len(list) != 2 || !chatIsSystem(list[1]) {
		t.Fatalf("history = %+v, want the question and the divider", list)
	}
}

func TestChatClearEmptiesOnlyTheTabOnScreen(t *testing.T) {
	resetChatTestState(t)
	prevCache := userConfigCache
	t.Cleanup(func() { userConfigCache = prevCache })
	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{}}}
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "c", Sender: "Alex", Text: "x"}}
	chatSavedMessages = []ChatMessage{{ID: "s", Sender: "You", Own: true, Text: "note"}}
	chatMutex.Unlock()
	chatActiveTab.Store(int32(chatTabSaved))
	chatClearTab(chatActiveTabID())
	if len(chatTabMessages(chatTabSaved)) != 0 || len(chatTabMessages(chatTabCommunity)) != 1 {
		t.Fatal("clearing Saved touched another tab or kept its notes")
	}
	if c := userConfigCache.Chat; c == nil || len(c.Saved) != 0 || len(c.Messages) != 1 {
		t.Fatalf("the cleared tab was not saved: %+v", userConfigCache.Chat)
	}
}

func TestChatDeleteAndRetryRespectTheTab(t *testing.T) {
	resetChatTestState(t)
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "123456789012345678", Sender: "Alex", Text: "theirs"},
		{ID: "loc1", Sender: "You", Own: true, Text: "mine", Error: "Not sent — Discord refused the message"},
	}
	chatMutex.Unlock()
	deleteChatMessage("123456789012345678")
	if n := len(chatTabMessages(chatTabCommunity)); n != 2 {
		t.Fatal("someone else's community message was deleted")
	}
	if !chatRetryMessage(chatTabCommunity, "loc1") {
		t.Fatal("Retry found no message")
	}
	if m := chatTabMessages(chatTabCommunity)[1]; !m.Pending || m.Error != "" {
		t.Fatalf("after Retry the message is %+v, want it queued again", m)
	}
	deleteChatMessage("loc1")
	if got := chatIDs(chatTabMessages(chatTabCommunity)); got != "123456789012345678" {
		t.Fatalf("history = %s, want this device's own message deleted", got)
	}
}

// ── Sending (chatsync.go) ────────────────────────────────────────────────

// chatFailingReader returns its data and then a broken connection.
type chatFailingReader struct{ data *strings.Reader }

func (r chatFailingReader) Read(p []byte) (int, error) {
	if r.data.Len() > 0 {
		return r.data.Read(p)
	}
	return 0, errors.New("connection reset by peer")
}

func TestChatReadGroqStream(t *testing.T) {
	event := func(payload string) string { return "data: " + payload + "\n\n" }
	cases := []struct {
		name    string
		body    io.Reader
		want    string
		wantErr bool
		deltas  int
	}{
		{"streamed answer", strings.NewReader(event(`{"choices":[{"delta":{"content":"Hel"}}]}`) +
			": keep-alive\n\n" + event(`{"choices":[{"delta":{"content":"lo"}}]}`) + "data: [DONE]\n\n"), "Hello", false, 2},
		{"whole completion", strings.NewReader(`{"choices":[{"message":{"content":" Hi there "}}]}`), "Hi there", false, 1},
		{"error event", strings.NewReader(event(`{"error":{"message":"model overloaded"}}`)), "", true, 0},
		{"error body", strings.NewReader(`{"error":{"message":"bad request"}}`), "", true, 0},
		{"cut short after words", chatFailingReader{strings.NewReader(event(`{"choices":[{"delta":{"content":"Partial"}}]}`))}, "Partial", false, 1},
		{"cut short before words", chatFailingReader{strings.NewReader("")}, "", true, 0},
		{"empty body", strings.NewReader(""), "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deltas := 0
			got, err := chatReadGroqStream(tc.body, func(string) { deltas++ })
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if got != tc.want || deltas != tc.deltas {
				t.Fatalf("answer = %q after %d updates, want %q after %d", got, deltas, tc.want, tc.deltas)
			}
		})
	}
}

func TestChatStripThink(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"<think>plan</think>Answer", "Answer"},
		{"Hi <think>still planning", "Hi"},
		{"<think>a</think>x<think>b</think>y", "xy"},
		{"  plain  ", "plain"},
	} {
		if got := chatStripThink(tc.in); got != tc.want {
			t.Errorf("chatStripThink(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChatIsNetworkError(t *testing.T) {
	chatSetOnlineForTest(t, true)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"none", nil, false},
		{"deadline", fmt.Errorf("request failed: %w", context.DeadlineExceeded), true},
		{"socket", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{"refused", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"cut short", errors.New("Groq stream interrupted: unexpected EOF"), true},
		{"bad key", &groqError{status: http.StatusUnauthorized, msg: "Groq rejected the API key."}, false},
		{"refusal", errors.New("HTTP 403 Forbidden, Missing Access"), false},
	}
	for _, tc := range cases {
		if got := chatIsNetworkError(tc.err); got != tc.want {
			t.Errorf("%s: chatIsNetworkError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// chatGroqTestServer answers like Groq's chat completions endpoint: a stream
// for a good request, 401 for another key, 404 for a retired model.
func chatGroqTestServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case r.Header.Get("Authorization") != "Bearer gsk_test":
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"Invalid API Key"}}`)
		case req.Model == "retired":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"The model retired does not exist"}}`)
		case !req.Stream:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"expected a streamed request"}}`)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"lo"}}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	chatGroqEndpoint = srv.URL
}

func TestChatGroqAskStreamsAndFallsBack(t *testing.T) {
	resetChatTestState(t)
	chatSetOnlineForTest(t, true)
	chatGroqTestServer(t)
	msgs := buildGroqMessagesFrom(nil, "hi")
	var seen []string
	reply, err := chatGroqAsk("gsk_test", []string{"retired", "openai/gpt-oss-20b"}, msgs, func(s string) { seen = append(seen, s) })
	if err != nil || reply != "Hello" {
		t.Fatalf("reply = %q, %v; want the streamed answer from the second model", reply, err)
	}
	if strings.Join(seen, "|") != "Hel|Hello" {
		t.Errorf("the typing bubble saw %q, want it to grow word by word", seen)
	}
	_, err = chatGroqAsk("wrong", []string{"openai/gpt-oss-20b"}, msgs, nil)
	if err == nil {
		t.Fatal("a rejected key answered")
	}
	if got := chatFriendlyAIError(err); got != "Groq rejected the API key — check it in Settings" {
		t.Errorf("a rejected key reads %q", got)
	}
}

func TestChatRunAIFilesTheAnswerUnderTheQuestion(t *testing.T) {
	resetChatTestState(t)
	chatSetOnlineForTest(t, true)
	chatGroqTestServer(t)
	now := time.Now()
	prompt := ChatMessage{ID: "q1", Sender: "You", Own: true, Pending: true, Text: "hi", Timestamp: now}
	later := ChatMessage{ID: "q2", Sender: "You", Own: true, Pending: true, Text: "and?", Timestamp: now}
	chatMutex.Lock()
	chatAIMessages = []ChatMessage{prompt, later}
	chatMutex.Unlock()

	chatAIBusy.Store(true)
	chatRunAI("gsk_test", []string{"openai/gpt-oss-20b"}, prompt)
	if chatAIBusy.Load() {
		t.Error("the request left the assistant marked busy")
	}
	list := chatTabMessages(chatTabAI)
	if len(list) != 3 || list[1].Sender != chatAISender || list[1].Text != "Hello" || list[2].ID != "q2" {
		t.Fatalf("history = %+v, want the answer between the two questions", list)
	}
	if list[0].Pending || list[0].Error != "" {
		t.Error("the answered question is still queued")
	}
	if !list[2].Pending {
		t.Error("the next question lost its place in the queue")
	}
	if got := chatTabUnread(chatTabAI); got != 1 {
		t.Errorf("unread = %d, want the answer counted while the tab is not on screen", got)
	}
	if chatStreamSnapshot().active {
		t.Error("the typing bubble stayed up after the answer")
	}

	// A refusal is one sentence under the question, not a raw error.
	chatRunAI("wrong", []string{"openai/gpt-oss-20b"}, later)
	if q := chatTabMessages(chatTabAI)[2]; q.Pending || q.Error != "Groq rejected the API key — check it in Settings" {
		t.Fatalf("refused question = %+v", q)
	}
}

func TestChatSyncAIWaitsForAKeyAndTheNetwork(t *testing.T) {
	resetChatTestState(t)
	chatMutex.Lock()
	chatAIMessages = []ChatMessage{{ID: "q", Sender: "You", Own: true, Pending: true, Text: "hi"}}
	chatMutex.Unlock()

	// Before the settings are read nothing is decided.
	chatSyncAI(time.Now())
	if q := chatTabMessages(chatTabAI)[0]; !q.Pending || q.Error != "" {
		t.Fatalf("question = %+v before the settings were read", q)
	}

	// Offline the question waits, quietly.
	chatSetOnlineForTest(t, false)
	chatCredMu.Lock()
	chatCreds = chatCredSnapshot{valid: true, groqKey: "gsk_test", groqModel: "openai/gpt-oss-20b"}
	chatCredMu.Unlock()
	chatSyncAI(time.Now())
	if q := chatTabMessages(chatTabAI)[0]; !q.Pending || q.Error != "" || chatAIBusy.Load() {
		t.Fatalf("offline, the question became %+v (busy=%v)", q, chatAIBusy.Load())
	}

	// Without a key it says where the key goes instead of waiting forever.
	chatCredMu.Lock()
	chatCreds.groqKey = ""
	chatCredMu.Unlock()
	chatSyncAI(time.Now())
	if q := chatTabMessages(chatTabAI)[0]; q.Pending || q.Error != "Add a Groq API key in Settings to ask Juka" {
		t.Fatalf("without a key the question is %+v", q)
	}
}

func TestChatSyncDiscordDeliversTheQueue(t *testing.T) {
	resetChatTestState(t)
	chatSetOnlineForTest(t, true)
	chatCredMu.Lock()
	chatCreds = chatCredSnapshot{valid: true, discordTok: "tok", channel: "975787212954275916", tokenType: "bot"}
	chatCredMu.Unlock()
	connected := false
	chatDiscordReady = func() bool { return connected }
	chatDiscordConnectFn = func(tok, ch, tokenType string) error {
		if tok != "tok" || ch != "975787212954275916" || tokenType != "bot" {
			return fmt.Errorf("connect with %q on %q as %q", tok, ch, tokenType)
		}
		connected = true
		return nil
	}
	var posted []string
	replyTo := ""
	chatDiscordPost = func(sender, text, replyToID string) (string, error) {
		posted = append(posted, sender+": "+text)
		replyTo = replyToID
		return "123456789012345678", nil
	}
	quote := &ChatQuote{Author: "Alex", Text: "hi", ID: "123456789012345600"}
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "loc1", Sender: "Steppa", Own: true, Pending: true, Text: "hello", Quote: quote}}
	chatMutex.Unlock()

	chatSyncDiscord(time.Now())
	if chatNet() != chatNetLive {
		t.Fatalf("state = %d after connecting, want live", chatNet())
	}
	if len(posted) != 1 || posted[0] != "Steppa: hello" || replyTo != "123456789012345600" {
		t.Fatalf("posted %q replying to %q, want the queued reply", posted, replyTo)
	}
	if m := chatTabMessages(chatTabCommunity)[0]; m.Pending || m.ID != "123456789012345678" {
		t.Fatalf("sent message = %+v, want it delivered under Discord's id", m)
	}

	// With nothing queued and nobody in Chat the loop leaves Discord alone.
	posted = nil
	chatSyncDiscord(time.Now())
	if len(posted) != 0 {
		t.Fatalf("posted %q with an empty queue", posted)
	}
}

func TestChatSyncDiscordExplainsWhatWentWrong(t *testing.T) {
	cases := []struct {
		name      string
		postErr   error
		connErr   error
		online    bool
		wantNet   chatNetState
		pending   bool
		errorText string
	}{
		{"offline waits", nil, nil, false, chatNetOffline, true, ""},
		{"network failure retries", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), nil, true, chatNetLive, true, ""},
		{"refusal is shown", errors.New("HTTP 403 Forbidden, Missing Access"), nil, true, chatNetLive, false, "Not sent — Discord refused the message"},
		{"not set up", nil, errors.New("discord not configured: Discord: token is missing"), true, chatNetUnconfigured, false, "Not sent — community chat isn't set up on this device"},
		{"token rejected", nil, errors.New("token rejected by Discord (revoked, or not a valid bot token)"), true, chatNetRejected, false, "Not sent — community chat is unavailable right now"},
		{"unreachable retries", nil, errors.New("cannot reach Discord: i/o timeout"), true, chatNetUnreachable, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetChatTestState(t)
			chatSetOnlineForTest(t, tc.online)
			chatCredMu.Lock()
			chatCreds = chatCredSnapshot{valid: true, discordTok: "tok", channel: "1", tokenType: "bot"}
			chatCredMu.Unlock()
			connected := false
			chatDiscordReady = func() bool { return connected }
			chatDiscordConnectFn = func(string, string, string) error {
				if tc.connErr != nil {
					return tc.connErr
				}
				connected = true
				return nil
			}
			chatDiscordPost = func(string, string, string) (string, error) {
				if tc.postErr != nil {
					return "", tc.postErr
				}
				return "123456789012345678", nil
			}
			chatMutex.Lock()
			chatMessages = []ChatMessage{{ID: "loc1", Sender: "You", Own: true, Pending: true, Text: "hello"}}
			chatMutex.Unlock()

			chatSyncDiscord(time.Now())
			if got := chatNet(); got != tc.wantNet {
				t.Fatalf("state = %d, want %d", got, tc.wantNet)
			}
			if m := chatTabMessages(chatTabCommunity)[0]; m.Pending != tc.pending || m.Error != tc.errorText {
				t.Fatalf("message = %+v, want pending=%v error=%q", m, tc.pending, tc.errorText)
			}
		})
	}
}

func TestChatRetryButtonForgetsTheBackoff(t *testing.T) {
	resetChatTestState(t)
	chatSetOnlineForTest(t, true)
	chatCredMu.Lock()
	chatCreds = chatCredSnapshot{valid: true, discordTok: "tok", channel: "1", tokenType: "bot"}
	chatCredMu.Unlock()
	attempts := 0
	chatDiscordConnectFn = func(string, string, string) error {
		attempts++
		return errors.New("cannot reach Discord: i/o timeout")
	}
	chatDiscordWanted.Store(true)
	now := time.Now()
	chatSyncDiscord(now)
	chatSyncDiscord(now.Add(time.Second))
	if attempts != 1 {
		t.Fatalf("connected %d times inside the backoff, want 1", attempts)
	}
	chatDiscordRetryNow()
	chatSyncDiscord(now.Add(2 * time.Second))
	if attempts != 2 {
		t.Fatalf("Retry did not try again at once (%d attempts)", attempts)
	}
}

// ── What the screen says ─────────────────────────────────────────────────

func TestChatBannerIsOneSentence(t *testing.T) {
	resetChatTestState(t)
	cases := []struct {
		name   string
		tab    chatTab
		online bool
		net    chatNetState
		key    string
		queued int
		text   string
		action chatBannerAction
	}{
		{"community offline", chatTabCommunity, false, chatNetOffline, "", 0, "Offline — messages will send when you reconnect", chatBannerWiFi},
		{"community offline with a queue", chatTabCommunity, false, chatNetOffline, "", 2, "Offline — 2 messages will send when you reconnect", chatBannerWiFi},
		{"community live", chatTabCommunity, true, chatNetLive, "", 0, "", chatBannerNoAction},
		{"community not set up", chatTabCommunity, true, chatNetUnconfigured, "", 0, "Community chat isn't set up on this device", chatBannerNoAction},
		{"community unreachable", chatTabCommunity, true, chatNetUnreachable, "", 0, "Can't reach Discord — trying again", chatBannerRetry},
		{"assistant without a key", chatTabAI, true, chatNetLive, "", 0, "Add a Groq API key to talk to Juka", chatBannerAddKey},
		{"assistant offline", chatTabAI, false, chatNetLive, "gsk", 0, "AI needs internet — questions will send when you reconnect", chatBannerWiFi},
		{"assistant offline with a queue", chatTabAI, false, chatNetLive, "gsk", 1, "Offline — Juka will answer when you reconnect", chatBannerWiFi},
		{"assistant ready", chatTabAI, true, chatNetLive, "gsk", 0, "", chatBannerNoAction},
		{"saved", chatTabSaved, false, chatNetOffline, "", 0, "", chatBannerNoAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatSetOnlineForTest(t, tc.online)
			chatSetNet(tc.net)
			chatCredMu.Lock()
			chatCreds = chatCredSnapshot{valid: true, groqKey: tc.key}
			chatCredMu.Unlock()
			chatMutex.Lock()
			list := chatTabSliceLocked(tc.tab)
			*list = nil
			for i := 0; i < tc.queued; i++ {
				*list = append(*list, ChatMessage{ID: fmt.Sprintf("p%d", i), Sender: "You", Own: true, Pending: true, Text: "x"})
			}
			chatMutex.Unlock()
			b := chatCurrentBanner(tc.tab)
			if b.show != (tc.text != "") || b.text != tc.text || b.action != tc.action {
				t.Fatalf("banner = %+v, want %q (action %d)", b, tc.text, tc.action)
			}
		})
	}
}

func TestChatStatusUnderOwnMessages(t *testing.T) {
	resetChatTestState(t)
	queued := &ChatMessage{ID: "q", Sender: "You", Own: true, Pending: true, Text: "x"}
	failed := &ChatMessage{ID: "f", Sender: "You", Own: true, Error: "Not sent — Discord refused the message"}
	sent := &ChatMessage{ID: "s", Sender: "You", Own: true, Text: "x"}
	cases := []struct {
		name   string
		msg    *ChatMessage
		tab    chatTab
		online bool
		net    chatNetState
		want   string
	}{
		{"community offline", queued, chatTabCommunity, false, chatNetOffline, "Will send when you're back online"},
		{"community connecting", queued, chatTabCommunity, true, chatNetConnecting, "Waiting for the connection…"},
		{"community sending", queued, chatTabCommunity, true, chatNetLive, "Sending…"},
		{"assistant offline", queued, chatTabAI, false, chatNetIdle, "Will ask Juka when you're back online"},
		{"assistant waiting", queued, chatTabAI, true, chatNetIdle, "Waiting for Juka…"},
		{"refused", failed, chatTabCommunity, true, chatNetLive, "Not sent — Discord refused the message"},
		{"delivered", sent, chatTabCommunity, true, chatNetLive, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatSetOnlineForTest(t, tc.online)
			chatSetNet(tc.net)
			if got := chatStatusFor(tc.msg, tc.tab); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChatFooterHintsFollowTheState(t *testing.T) {
	resetChatTestState(t)
	prevCfg := appConfig
	t.Cleanup(func() { appConfig = prevCfg })
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	labels := func() map[string]string {
		out := map[string]string{}
		for _, h := range chatFooterHints() {
			out[h.Button] = h.Label
		}
		return out
	}
	if got := labels(); got["A"] != "Type" || got["Y"] != "People" || got["X"] != "Rename" || got["B"] != "Back" {
		t.Fatalf("community hints = %v", got)
	}
	chatInputText = "draft"
	if got := labels(); got["Start"] != "Send" {
		t.Fatalf("with a draft the hints = %v, want Start Send", got)
	}
	chatInputText = ""
	chatActiveTab.Store(int32(chatTabAI))
	if got := labels(); got["Y"] != "Add key" {
		t.Fatalf("Juka AI without a key: hints = %v, want Y Add key", got)
	}
	appConfig.Variables.Custom["groq_api_key"] = "gsk_test"
	if got := labels(); got["Y"] != "New chat" {
		t.Fatalf("Juka AI with a key: hints = %v, want Y New chat", got)
	}
	chatComposerInline = true
	if got := labels(); got["Enter"] != "Send" || got["Esc"] != "Done" {
		t.Fatalf("typing hints = %v", got)
	}
	chatComposerInline = false
	chatSelectionBar = chatSelection{open: true}
	if got := labels(); got["A"] != "Select" || got["B"] != "Close" {
		t.Fatalf("action bar hints = %v", got)
	}
}

// ── Controls (chatinput.go) ──────────────────────────────────────────────

// B and Esc leave Chat on every input. On Windows they used to be eaten: the
// composer was armed on entry, Escape only cleared the draft and "b" was
// typed into the message.
func TestChatBackLeavesOnEveryInput(t *testing.T) {
	resetKeyboardState(t)
	resetChatTestState(t)
	config := chatSceneFixture(t)
	for _, key := range []sdl.Keycode{sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE} {
		if !chatSceneKey(keyEvent(key), config) || !chatLeftScene() {
			t.Errorf("key %d did not leave Chat", key)
		}
	}
	for _, button := range []uint8{sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK} {
		if !chatSceneController(chatPad(button), config) || !chatLeftScene() {
			t.Errorf("pad button %d did not leave Chat", button)
		}
	}
	// The on-screen keyboard closes itself first: while it is up, the
	// scene's handlers stay out of the way.
	virtualKeyboardActive = true
	if chatSceneKey(keyEvent(sdl.K_ESCAPE), config) || chatSceneController(chatPad(sdl.CONTROLLER_BUTTON_B), config) {
		t.Error("the chat took Back from the open keyboard")
	}
	virtualKeyboardActive = false
}

func TestChatTypingInlineKeepsLettersAndEscStopsIt(t *testing.T) {
	resetKeyboardState(t)
	resetChatTestState(t)
	config := chatSceneFixture(t)

	// Enter starts typing: straight into the composer on a desktop, on the
	// on-screen keyboard on the handheld (where the pad's A can arrive as Enter).
	if !chatSceneKey(keyEvent(sdl.K_RETURN), config) {
		t.Fatal("Enter was not taken by the chat")
	}
	if P().IsHandheld() {
		if !virtualKeyboardActive || vkTarget != vkTargetChat {
			t.Fatal("Enter on the handheld did not open the on-screen keyboard")
		}
		vkCloseKeyboard()
		chatStartInlineTyping()
	} else if !chatComposerInline {
		t.Fatal("Enter did not start typing into the composer")
	}
	// While typing, "b" and "q" are text.
	for _, key := range []sdl.Keycode{sdl.K_b, sdl.K_q, sdl.K_SPACE} {
		if !chatSceneKey(keyEvent(key), config) {
			t.Fatalf("key %d was not consumed while typing", key)
		}
		if chatLeftScene() {
			t.Fatalf("key %d left Chat while typing", key)
		}
	}
	if !chatComposerTakesText(config) {
		t.Fatal("typed text does not reach the composer")
	}
	chatComposerInsert("b")
	chatComposerInsert("q")
	if chatInputText != "bq" {
		t.Fatalf("draft = %q, want %q", chatInputText, "bq")
	}
	// Ctrl+V is the paste shortcut, not text.
	if chatSceneKey(chatKey(sdl.K_v, sdl.KMOD_LCTRL), config) {
		t.Error("Ctrl+V was swallowed by the composer")
	}
	// Shift+Enter is a new line, Esc stops typing, a second Esc leaves.
	chatSceneKey(chatKey(sdl.K_RETURN, sdl.KMOD_LSHIFT), config)
	if chatInputText != "bq\n" {
		t.Fatalf("Shift+Enter left %q", chatInputText)
	}
	if !chatSceneKey(keyEvent(sdl.K_ESCAPE), config) || chatComposerInline || chatLeftScene() {
		t.Fatal("Esc while typing must stop typing and stay in Chat")
	}
	if chatInputText != "bq\n" {
		t.Error("stopping typing threw the draft away")
	}
	if !chatSceneKey(keyEvent(sdl.K_ESCAPE), config) || !chatLeftScene() {
		t.Fatal("Esc after typing did not leave Chat")
	}
}

func TestChatInlineKeysAndDeleteBack(t *testing.T) {
	for _, tc := range []struct {
		sym  sdl.Keycode
		ctrl bool
		want bool
	}{
		{sdl.K_b, false, true},
		{sdl.K_q, false, true},
		{sdl.K_F5, false, false},
		{sdl.K_v, true, false},
	} {
		if got := chatInlineKey(tc.sym, tc.ctrl, false, false); got != tc.want {
			t.Errorf("key %d (ctrl=%v) consumed = %v, want %v", tc.sym, tc.ctrl, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		word bool
		want string
	}{
		{"hello", false, "hell"},
		{"", false, ""},
		{"hello world", true, "hello "},
		{"hello  ", true, ""},
		{"one\ntwo", true, "one\n"},
		{"héllo", false, "héll"},
	} {
		if got := chatDeleteBack(tc.in, tc.word); got != tc.want {
			t.Errorf("chatDeleteBack(%q, %v) = %q, want %q", tc.in, tc.word, got, tc.want)
		}
	}
}

func TestChatStartSendsTheDraft(t *testing.T) {
	resetKeyboardState(t)
	resetChatTestState(t)
	config := chatSceneFixture(t)
	if chatStartSendsDraft(config) {
		t.Fatal("Start with nothing typed should open the Start menu, not send")
	}
	chatInputText = "  ready when you are  "
	if !chatStartSendsDraft(config) {
		t.Fatal("Start did not send the draft")
	}
	if chatInputText != "" {
		t.Errorf("the composer kept %q after sending", chatInputText)
	}
	if list := chatTabMessages(chatTabCommunity); len(list) != 1 || list[0].Text != "ready when you are" {
		t.Fatalf("history = %+v, want the sent draft", list)
	}
	// While the keyboard is up, Start is the keyboard's.
	chatInputText = "more"
	virtualKeyboardActive = true
	if chatStartSendsDraft(config) {
		t.Error("Start sent past the open keyboard")
	}
	virtualKeyboardActive = false
}

func TestChatTabsKeepTheirDrafts(t *testing.T) {
	resetKeyboardState(t)
	resetChatTestState(t)
	config := chatSceneFixture(t)

	chatInputText = "for the channel"
	chatSceneController(chatPad(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER), config)
	if chatActiveTabID() != chatTabAI || chatInputText != "" {
		t.Fatalf("R1 went to tab %d with draft %q, want Juka AI and an empty composer", chatActiveTabID(), chatInputText)
	}
	chatInputText = "for juka"
	chatSceneKey(keyEvent(sdl.K_RIGHT), config)
	if chatActiveTabID() != chatTabSaved {
		t.Fatalf("Right went to tab %d, want Saved", chatActiveTabID())
	}
	chatSceneShoulder(padR1, config)
	if chatActiveTabID() != chatTabCommunity || chatInputText != "for the channel" {
		t.Fatalf("R1 from the last tab went to %d with %q, want the community draft back", chatActiveTabID(), chatInputText)
	}
	chatSceneKey(keyEvent(sdl.K_2), config)
	if chatActiveTabID() != chatTabAI || chatInputText != "for juka" {
		t.Fatalf("2 opened tab %d with %q, want Juka AI and its draft", chatActiveTabID(), chatInputText)
	}
	chatSceneController(chatPad(sdl.CONTROLLER_BUTTON_LEFTSHOULDER), config)
	if chatActiveTabID() != chatTabCommunity {
		t.Fatalf("L1 went to tab %d, want the community", chatActiveTabID())
	}
}

func TestChatPadWalksTheHistory(t *testing.T) {
	resetChatTestState(t)
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Alex", Text: "one"},
		{ID: "2", Sender: chatSystemSender, Text: "New conversation"},
		{ID: "3", Sender: "Sam", Text: "three"},
	}
	chatMutex.Unlock()
	if !chatFocusUp() || chatSelected != 2 {
		t.Fatalf("Up from the composer selected %d, want the newest message", chatSelected)
	}
	if !chatFocusUp() || chatSelected != 0 {
		t.Fatalf("Up selected %d, want the message above the divider", chatSelected)
	}
	if got := chatViews[chatTabCommunity].anchorID; got != "1" {
		t.Errorf("the view was asked to show %q, want the selected message", got)
	}
	chatViews[chatTabCommunity].topReached = true
	if chatFocusUp() {
		t.Error("Up at the top of the history should hand the focus to the top bar")
	}
	chatFocusDown()
	chatFocusDown()
	if chatSelected != -1 {
		t.Fatalf("Down past the newest message selected %d, want the composer", chatSelected)
	}
	if chatSelectionBar.open {
		t.Error("walking the history opened an action bar")
	}
}

func TestChatEmptyAssistantOffersStarters(t *testing.T) {
	resetChatTestState(t)
	prevCfg := appConfig
	t.Cleanup(func() { appConfig = prevCfg })
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{"groq_api_key": "gsk_test"}}}
	chatActiveTab.Store(int32(chatTabAI))
	if !chatFocusUp() || chatSuggestFocus != chatSuggestionCount-1 {
		t.Fatalf("Up on an empty Juka AI focused starter %d", chatSuggestFocus)
	}
	chatActivate()
	list := chatTabMessages(chatTabAI)
	if len(list) != 1 || list[0].Text != chatSuggestions[chatSuggestionCount-1] || !list[0].Pending {
		t.Fatalf("A on a starter queued %+v", list)
	}
}

func TestChatWheelScrollsTheHistory(t *testing.T) {
	resetChatTestState(t)
	config := chatSceneFixture(t)
	if !chatSceneWheel(config, 2) || chatViews[chatTabCommunity].target != float64(2*chatPx(64)) {
		t.Fatalf("two notches up moved the history to %v", chatViews[chatTabCommunity].target)
	}
	chatSceneWheel(config, -10)
	if got := chatViews[chatTabCommunity].target; got != 0 {
		t.Fatalf("scrolling down past the newest message left the target at %v", got)
	}
	currentSceneIndex = 0
	if chatSceneWheel(config, 1) {
		t.Error("the chat took the wheel on another scene")
	}
}

func TestChatRightStickScrollsFreely(t *testing.T) {
	resetChatTestState(t)
	config := chatSceneFixture(t)
	if !chatSceneStick(config, -32767) || chatStickSpeed <= 0 {
		t.Fatalf("stick up gave speed %v, want it to scroll towards older messages", chatStickSpeed)
	}
	full := chatStickSpeed
	if !chatSceneStick(config, 22000) || chatStickSpeed >= 0 || -chatStickSpeed >= full {
		t.Fatalf("a half push down gave speed %v, want a slower scroll towards the newest", chatStickSpeed)
	}
	if !chatSceneStick(config, 3000) || chatStickSpeed != 0 {
		t.Fatalf("a resting stick still scrolls at %v", chatStickSpeed)
	}
	currentSceneIndex = 0
	if chatSceneStick(config, -32767) || chatStickSpeed != 0 {
		t.Error("the right stick scrolled the chat from another scene")
	}
}

func TestChatOpenTabShowsTheTab(t *testing.T) {
	resetChatTestState(t)
	config := chatSceneFixture(t)
	currentSceneIndex = 0
	chatOpenTab(config, "juka")
	if chatActiveTabID() != chatTabAI {
		t.Fatalf("chatOpenTab(juka) opened tab %d", chatActiveTabID())
	}
	if transitionPhase != "fade-out" || pendingSceneIndex != 1 {
		t.Fatalf("chatOpenTab did not go to Chat (phase %q, pending %d)", transitionPhase, pendingSceneIndex)
	}
}

func TestChatSceneEnterAndLeave(t *testing.T) {
	resetChatTestState(t)
	config := chatSceneFixture(t)
	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "100000000000000001", Sender: "Alex", Text: "read", Timestamp: now.Add(-time.Hour)},
		{ID: "100000000000000002", Sender: "Alex", Text: "new", Timestamp: now},
	}
	chatTabs[chatTabCommunity] = chatTabData{unread: 1, seenAt: now.Add(-time.Minute).UnixMilli()}
	chatMutex.Unlock()
	chatComposerInline = true

	chatSyncVisibility(config)
	if !chatWasVisible || !chatDiscordWanted.Load() {
		t.Fatal("opening Chat did not ask for the Discord connection")
	}
	if chatComposerInline {
		t.Error("typing carried over into a fresh visit")
	}
	if got := chatViews[chatTabCommunity].dividerID; got != "100000000000000002" {
		t.Errorf("the New messages divider sits above %q, want the unread message", got)
	}

	currentSceneIndex = 0
	chatSyncVisibility(config)
	if chatWasVisible || chatViews[chatTabCommunity].dividerID != "" || chatLiveTab.Load() != -1 {
		t.Fatal("leaving Chat kept the visit's state")
	}
}

// ── Layout (chatview.go) ─────────────────────────────────────────────────

// chatRenderTest is a renderer, the shipped config and the chat element the
// render probes draw into.
func chatRenderTest(t *testing.T) (*sdl.Renderer, *Config, Element) {
	t.Helper()
	config := layoutTestConfig(t)
	renderer := probeRenderer(t)
	return renderer, config, Element{Type: "chat", X: 0, Y: 0, Width: "1240", Height: "640", Font: "small"}
}

// The history is laid out from the composer up and the topmost bubble is
// clipped by the list's edge: the old renderer stopped at the last message
// that fitted whole and left an empty band under the tabs.
func TestChatHistoryLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatTestState(t)
	renderer, config, element := chatRenderTest(t)
	now := time.Now()
	msgs := make([]ChatMessage, 0, 40)
	for i := 0; i < 40; i++ {
		sender := "Alex"
		if i%2 == 1 {
			sender = "Sam"
		}
		msgs = append(msgs, ChatMessage{
			ID:        fmt.Sprintf("m%02d", i),
			Sender:    sender,
			Text:      strings.Repeat("A long line of chat text that wraps more than once. ", 3+i%3),
			Timestamp: now.Add(time.Duration(i-40) * time.Second),
		})
	}
	chatMutex.Lock()
	chatMessages = msgs
	chatMutex.Unlock()

	renderChat(renderer, config, element)
	list := chatView.list
	if len(chatMessageHits) == 0 || list.H <= 0 {
		t.Fatalf("nothing was drawn (list %+v)", list)
	}
	m := chatMetricsFor(chatLoadFonts(config), list)
	top, bottom := list.Y+list.H, list.Y
	for _, hit := range chatMessageHits {
		top = min32(top, hit.body.Y)
		bottom = max32(bottom, hit.body.Y+hit.body.H)
	}
	if band := top - list.Y; band > m.dividerH+m.headerH+m.groupGap {
		t.Fatalf("the history leaves a %dpx empty band under the tabs (first bubble at %d, list from %d)", band, top, list.Y)
	}
	if gap := list.Y + list.H - bottom; gap < 0 || gap > chatPx(6)+1 {
		t.Fatalf("the newest message ends %dpx above the composer, want it at the bottom", gap)
	}
	if !chatView.atBottom || chatJumpRect.W != 0 {
		t.Fatal("at the newest message the jump pill is showing")
	}

	// Scrolled back, the way to the newest message appears and takes the reader
	// there. The smooth scroll is snapped: the test reads, it does not watch.
	chatScrollBy(600)
	chatView.lastTicks = 0
	renderChat(renderer, config, element)
	if chatView.atBottom || chatJumpRect.W <= 0 {
		t.Fatal("scrolled back, the jump pill is missing")
	}
	pill := chatJumpRect
	if !chatMessageClick(pill.X+pill.W/2, pill.Y+pill.H/2, config) {
		t.Fatal("the jump pill did not take the click")
	}
	if got := chatViews[chatTabCommunity].target; got != 0 {
		t.Fatalf("the jump pill scrolled to %v, want the newest message", got)
	}
}

// Offline, the community tab says so in one slim line with a Wi-Fi button,
// and the history keeps the rest of the screen.
func TestChatOfflineBannerIsSlim(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatTestState(t)
	renderer, config, element := chatRenderTest(t)
	chatSetOnlineForTest(t, false)
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "q", Sender: "You", Own: true, Pending: true, Text: "see you there", Timestamp: time.Now()}}
	chatMutex.Unlock()

	renderChat(renderer, config, element)
	if chatView.bannerAction != chatBannerWiFi || chatView.bannerBtn.W <= 0 {
		t.Fatal("offline, the community tab has no Wi-Fi button in its banner")
	}
	if used := chatView.list.Y - chatView.area.Y; used > chatPx(42+10+34+10) {
		t.Fatalf("the tabs and the banner take %dpx above the history", used)
	}
	if len(chatMessageHits) != 1 {
		t.Fatalf("drew %d messages, want the queued one", len(chatMessageHits))
	}
}

// An empty Juka AI tab offers a few questions to start with, and a click on
// one asks it.
func TestChatStarterQuestionsAreClickable(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatTestState(t)
	renderer, config, element := chatRenderTest(t)
	prevCfg, prevScene := appConfig, currentSceneIndex
	t.Cleanup(func() { appConfig, currentSceneIndex = prevCfg, prevScene })
	config.Variables.Custom["groq_api_key"] = "gsk_test"
	appConfig = config
	currentSceneIndex = findSceneIndex(config, chatSceneName)

	chatActiveTab.Store(int32(chatTabAI))
	renderChat(renderer, config, element)
	chip := chatView.suggestions[0]
	if chip.W <= 0 {
		t.Fatal("an empty Juka AI tab drew no starter questions")
	}
	if !chatSceneClick(chip.X+chip.W/2, chip.Y+chip.H/2, config) {
		t.Fatal("the starter question did not take the click")
	}
	if list := chatTabMessages(chatTabAI); len(list) != 1 || list[0].Text != chatSuggestions[0] {
		t.Fatalf("the starter question queued %+v", list)
	}
}
