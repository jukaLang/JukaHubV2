package main

// Tests for the Discord-style member profiles in the chat: identity carried by a
// message, grouping of the history into people, the name/avatar targets that
// open a card, the member list, and the actions a card offers.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/veandco/go-sdl2/sdl"
)

// resetChatProfileState isolates the chat globals for one test.
func resetChatProfileState(t *testing.T) {
	t.Helper()
	chatMutex.Lock()
	prevMessages := chatMessages
	chatMessages = nil
	chatMutex.Unlock()
	prevOffset, prevSelected, prevSelectedID := chatScrollOffset, chatSelected, chatSelectedID
	prevProfile, prevMembers := chatProfile, chatMembers
	prevBar := chatSelectionBar
	chatScrollOffset = 0
	chatSelected, chatSelectedID = -1, ""
	chatProfile = chatProfileCard{}
	chatMembers = chatMembersPanel{}
	// The action bar is part of the selection state: a test that selects a
	// message leaves it open, and an open bar takes the pad before the scene
	// underneath, so restoring the highlight alone would leak into the next
	// test's controller checks.
	chatSelectionBar = chatSelection{}
	chatMessageHits = nil
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages = prevMessages
		chatMutex.Unlock()
		chatScrollOffset = prevOffset
		chatSelected, chatSelectedID = prevSelected, prevSelectedID
		chatProfile, chatMembers = prevProfile, prevMembers
		chatSelectionBar = prevBar
		chatMessageHits = nil
	})
}

func TestChatMessageFromDiscordCarriesTheAccount(t *testing.T) {
	msg := &discordgo.Message{
		ID:        "987654321098765432",
		Content:   "hello",
		Timestamp: time.Now(),
		Author: &discordgo.User{
			ID:       "123456789012345678",
			Username: "steppa",
			Avatar:   "hash123",
			Bot:      true,
		},
	}
	entry := chatMessageFromDiscord(msg)
	if entry.ID != msg.ID || entry.Sender != "steppa" || entry.Text != "hello" {
		t.Fatalf("entry = %+v, want the message identity and text", entry)
	}
	if entry.UserID != msg.Author.ID || entry.Handle != "steppa" {
		t.Errorf("account = %q / %q, want the author id and handle", entry.UserID, entry.Handle)
	}
	if !entry.Bot {
		t.Error("a bot author should mark the entry as a bot")
	}
	want := "https://cdn.discordapp.com/avatars/123456789012345678/hash123.png?size="
	if len(entry.AvatarURL) < len(want) || entry.AvatarURL[:len(want)] != want {
		t.Errorf("avatar = %q, want the author's CDN picture", entry.AvatarURL)
	}
}

// The bridge bot relays other people's words. Its messages must not claim the
// bot's account: that would show the bot's profile for every relayed speaker.
func TestBridgeRelayKeepsNoDiscordIdentity(t *testing.T) {
	msg := &discordgo.Message{
		Content: "steppa: hi all",
		Author: &discordgo.User{
			ID:       "555555555555555555",
			Username: "TrimuiSmartProJuka",
			Avatar:   "botpic",
			Bot:      true,
		},
	}
	entry := chatMessageFromDiscord(msg)
	if entry.Sender != "steppa" {
		t.Fatalf("sender = %q, want the relayed name", entry.Sender)
	}
	if entry.UserID != "" || entry.Handle != "" || entry.AvatarURL != "" || entry.Bot {
		t.Errorf("relayed message kept the bot identity: %+v", entry)
	}
}

func TestChatMembersGroupByIdentityNotName(t *testing.T) {
	resetChatProfileState(t)
	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Alex", UserID: "111111111111111111", Text: "one", Timestamp: now.Add(-2 * time.Minute)},
		{ID: "2", Sender: "Alex", UserID: "222222222222222222", Text: "two", Timestamp: now.Add(-time.Minute)},
		{ID: "3", Sender: "Alex", Text: "local", Timestamp: now},
	}
	chatMutex.Unlock()

	profiles := chatMemberProfiles()
	if len(profiles) != 3 {
		t.Fatalf("got %d profiles, want 3 (two Discord accounts and the name-only sender)", len(profiles))
	}
	// Two people called Alex must not merge, and a message without an id must
	// stay its own entry rather than attaching to one of the accounts.
	byKey := map[string]chatMemberProfile{}
	for _, p := range profiles {
		byKey[p.Key] = p
	}
	for _, id := range []string{"id:111111111111111111", "id:222222222222222222", "name:alex"} {
		if _, ok := byKey[id]; !ok {
			t.Errorf("missing profile %q in %+v", id, byKey)
		}
	}
	if got := chatProfileForSender("Alex", "222222222222222222").Messages; got != 1 {
		t.Errorf("profile for the second account has %d messages, want 1", got)
	}
}

func TestChatProfileActionsOfferPictureAndLatest(t *testing.T) {
	p := chatMemberProfile{
		Name:      "steppa",
		Handle:    "steppa",
		UserID:    "123456789012345678",
		AvatarURL: "https://cdn.discordapp.com/avatars/1/hash.png?size=64",
		Messages:  3,
	}
	have := map[string]bool{}
	for _, action := range chatProfileActions(p) {
		have[action.kind] = true
	}
	for _, kind := range []string{"mention", "jump", "copy_id", "avatar", "close"} {
		if !have[kind] {
			t.Errorf("profile card is missing the %q action", kind)
		}
	}
	if have["rename"] {
		t.Error("only the local user's own card offers Rename")
	}
}

// A quote picked in the action bar is attached to the message that is sent, so
// the reply header survives a restart instead of being dropped at send time.
func TestReplyIsAttachedToTheSentMessage(t *testing.T) {
	withChatNameEnv(t)
	setChatDisplayName("Steppa")
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "123456789012345678", Sender: "Alex", Text: "did it work?"}}
	chatMutex.Unlock()
	if !setChatReply(0) {
		t.Fatal("setChatReply refused the message")
	}
	if chatReplyTo == nil || chatReplyTo.ID != "123456789012345678" {
		t.Fatalf("pending quote = %+v, want the Discord message id", chatReplyTo)
	}

	sendChatMessage("Steppa", "yes, all good")
	chatMutex.Lock()
	sent := chatMessages[len(chatMessages)-1]
	chatMutex.Unlock()
	if sent.Quote == nil || sent.Quote.Author != "Alex" || sent.Quote.Text != "did it work?" {
		t.Fatalf("sent message quote = %+v, want the answered message", sent.Quote)
	}
	if chatReplyTo != nil {
		t.Error("the pending quote should be cleared after one send")
	}
}

// Rendering the chat panel records one click target per message: the body for
// selecting the message, and the sender's name and picture for their profile.
func TestChatNameClickOpensProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if findSceneIndex(config, "Chat") < 0 {
		t.Fatalf("the shipped config has a Chat scene; it went missing")
	}
	renderer := probeRenderer(t)

	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Steppa", Text: "first", Timestamp: now.Add(-time.Minute)},
		{ID: "2", Sender: "Alex", UserID: "222222222222222222", Text: "second", Timestamp: now},
	}
	chatMutex.Unlock()

	renderChat(renderer, config, Element{Type: "chat", X: 0, Y: 0, Width: "1240", Height: "640", Font: "small"})
	if len(chatMessageHits) != 2 {
		t.Fatalf("recorded %d message hits, want 2", len(chatMessageHits))
	}

	var target chatMessageHit
	for _, hit := range chatMessageHits {
		if hit.sender == "Alex" {
			target = hit
		}
	}
	if target.name.W <= 0 || target.name.H <= 0 {
		t.Fatalf("no clickable name rect for Alex: %+v", target)
	}
	if target.userID != "222222222222222222" {
		t.Errorf("hit carries user id %q, want the Discord id", target.userID)
	}
	if !chatMessageClick(target.name.X+2, target.name.Y+2, config) {
		t.Fatal("clicking the name was not consumed")
	}
	if !chatProfile.open {
		t.Fatal("clicking a name did not open the profile card")
	}
	if chatProfile.profile.Name != "Alex" || chatProfile.profile.UserID != "222222222222222222" {
		t.Fatalf("card shows %+v, want Alex with the Discord id", chatProfile.profile)
	}
	closeChatProfile()

	// The avatar is the second way to the same card.
	if target.avatar.W <= 0 {
		t.Fatal("no clickable avatar for a message that starts a group")
	}
	if !chatMessageClick(target.avatar.X+2, target.avatar.Y+2, config) {
		t.Fatal("clicking the avatar was not consumed")
	}
	if !chatProfile.open || chatProfile.profile.Name != "Alex" {
		t.Fatalf("avatar click opened %+v, want Alex's card", chatProfile.profile)
	}
	closeChatProfile()

	// A click on the body selects the message instead of opening a card.
	if !chatMessageClick(target.body.X+4, target.body.Y+4, config) {
		t.Fatal("clicking a message body was not consumed")
	}
	if chatProfile.open {
		t.Error("clicking the body opened a profile card")
	}
	if _, _, ok := selectedChatMessage(); !ok {
		t.Error("clicking the body did not select the message")
	}
}

// The member list is the way to reach a name that is not on screen. Opening a
// row shows that person's card, and closing the card returns to the list.
func TestChatMemberListOpensProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	renderer := probeRenderer(t)

	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Steppa", Text: "first", Timestamp: now.Add(-time.Minute)},
		{ID: "2", Sender: "Alex", UserID: "222222222222222222", Text: "second", Timestamp: now},
	}
	chatMutex.Unlock()

	if !openChatMembers() {
		t.Fatal("openChatMembers refused a history with speakers")
	}
	renderChatMembers(renderer, config)
	if len(chatMembers.rows) != 2 {
		t.Fatalf("member list has %d rows, want 2", len(chatMembers.rows))
	}
	if chatMembers.rect.W <= 0 {
		t.Fatal("member list did not record its card rect")
	}
	// Click the actual rendered first row.
	row := chatMembers.layout.rowRect(0)
	if !chatOverlayClick(row.X+row.W/2, row.Y+row.H/2, config) {
		t.Fatal("a click on the member list was not consumed")
	}
	if !chatProfile.open {
		t.Fatal("clicking a member row did not open a profile")
	}
	if chatMembers.open {
		t.Error("the member list stayed open under the profile card")
	}
	closeChatProfile()
	if !chatMembers.open {
		t.Error("closing a card opened from the list should return to the list")
	}
}

func TestChatMembersLayoutFitsTextAndScrolls(t *testing.T) {
	resetChatProfileState(t)
	for _, tc := range []struct {
		width, height, line int32
		count               int
	}{
		{1280, 720, 28, 3},
		{960, 720, 42, 8},
		{640, 480, 38, 8},
	} {
		layout := chatMembersLayoutFor(tc.width, tc.height, tc.line, tc.count)
		if layout.rect.X < 0 || layout.rect.Y < 0 || layout.rect.X+layout.rect.W > tc.width || layout.rect.Y+layout.rect.H > tc.height {
			t.Fatalf("card outside %dx%d: %+v", tc.width, tc.height, layout.rect)
		}
		if layout.visible < 1 || layout.visible > chatMembersMax {
			t.Fatalf("unexpected visible count %d", layout.visible)
		}
		for i := 0; i < layout.visible; i++ {
			row := layout.rowRect(i)
			if row.H < 2*tc.line || row.Y < layout.rect.Y+layout.headerH || row.Y+row.H > layout.rect.Y+layout.rect.H-layout.footerH {
				t.Fatalf("text or row overlaps card chrome: layout=%+v row=%+v", layout, row)
			}
			if i > 0 && row.Y < layout.rowRect(i-1).Y+layout.rowRect(i-1).H {
				t.Fatal("member rows overlap")
			}
		}
		chatMembers.rows = make([]chatMemberProfile, tc.count)
		chatMembers.layout = layout
		chatMembers.focus = tc.count - 1
		chatMembers.scroll = 0
		chatMembersEnsureVisible()
		if chatMembers.focus < chatMembers.scroll || chatMembers.focus >= chatMembers.scroll+layout.visible {
			t.Fatalf("last focused member is not visible: focus=%d scroll=%d visible=%d", chatMembers.focus, chatMembers.scroll, layout.visible)
		}
	}
}

func TestChatMemberClickUsesScrolledRowLayout(t *testing.T) {
	resetChatProfileState(t)
	chatMembers = chatMembersPanel{open: true, rows: []chatMemberProfile{
		{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"},
	}}
	chatMembers.layout = chatMembersLayoutFor(640, 480, 55, len(chatMembers.rows))
	chatMembers.rect = chatMembers.layout.rect
	chatMembers.focus = 3
	chatMembersEnsureVisible()
	index := chatMembers.focus - chatMembers.scroll
	row := chatMembers.layout.rowRect(index)
	if !chatOverlayClick(row.X+row.W/2, row.Y+row.H/2, nil) {
		t.Fatal("row click was not consumed")
	}
	if !chatProfile.open || chatProfile.profile.Name != "four" {
		t.Fatalf("scrolled click opened %+v, want fourth member", chatProfile.profile)
	}
}

// The card's own picture is a button: tapping it opens the full avatar in the
// viewer, which is how a name from Discord can be put to a face at full size.
func TestProfilePictureOpensTheImageViewer(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	renderer := probeRenderer(t)
	prevViewer := imageViewerPath
	t.Cleanup(func() { imageViewerPath = prevViewer })

	avatar := "https://cdn.discordapp.com/avatars/222/hash.png?size=64"
	chatMutex.Lock()
	chatMessages = []ChatMessage{{
		ID:        "2",
		Sender:    "Alex",
		UserID:    "222222222222222222",
		Text:      "hello",
		Timestamp: time.Now(),
		AvatarURL: avatar,
	}}
	chatMutex.Unlock()

	if !openChatProfile("Alex", "222222222222222222") {
		t.Fatal("openChatProfile refused the speaker")
	}
	renderChatProfile(renderer, config)
	if chatProfile.avatar.W <= 0 {
		t.Fatal("the card did not record its picture rect")
	}
	if !chatOverlayClick(chatProfile.avatar.X+chatProfile.avatar.W/2, chatProfile.avatar.Y+chatProfile.avatar.H/2, config) {
		t.Fatal("clicking the card picture was not consumed")
	}
	if chatProfile.open {
		t.Error("opening the picture should close the card")
	}
	want := chatAvatarURLAtSize(avatar, 256)
	if imageViewerPath != want {
		t.Fatalf("image viewer shows %q, want the sharp avatar %q", imageViewerPath, want)
	}
}

// keyEvent builds a KEYDOWN the overlay handlers accept.
func keyEvent(sym sdl.Keycode) *sdl.KeyboardEvent {
	return &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
}

// Selecting a message opens its action bar with Reply focused, so one Enter on
// the pad starts a reply without any pointer.
func TestMessageActionBarReplyFromKeyboard(t *testing.T) {
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// The bar belongs to the chat panel, so the test stands where a user does
	// when they tap a message: on the scene that draws it.
	prevScene := currentSceneIndex
	currentSceneIndex = findSceneIndex(config, "Chat")
	t.Cleanup(func() { currentSceneIndex = prevScene })
	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "1", Sender: "Alex", Text: "did it work?", Timestamp: time.Now()}}
	chatMutex.Unlock()

	selectChatMessage(0)
	if !chatSelectionBar.open {
		t.Fatal("selecting a message did not open its action bar")
	}
	if got := chatSelectionBar.buttons[0].kind; got != "reply" {
		t.Fatalf("first action = %q, want reply", got)
	}
	if !handleChatSelectionKey(keyEvent(sdl.K_RETURN), config) {
		t.Fatal("Enter was not consumed by the action bar")
	}
	if chatReplyTo == nil || chatReplyTo.Author != "Alex" {
		t.Fatalf("pending quote = %+v, want the selected message", chatReplyTo)
	}
	if chatSelectionBar.open {
		t.Error("starting a reply should close the action bar")
	}
	clearChatReply()
}

// The action bar belongs to the panel that draws it. Leaving the chat scene
// with a message selected must hand the pad and the keys back to the screen
// underneath: the composer is gone, so the bar has nothing left to act on.
func TestChatSelectionBarReleasesInputOffTheChatScene(t *testing.T) {
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	chatScene := findSceneIndex(config, "Chat")
	if chatScene < 0 {
		t.Fatal("the shipped config has a Chat scene; it went missing")
	}
	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })

	chatMutex.Lock()
	chatMessages = []ChatMessage{{ID: "1", Sender: "Alex", Text: "first", Timestamp: time.Now()}}
	chatMutex.Unlock()

	currentSceneIndex = chatScene
	selectChatMessage(0)
	if !chatSelectionOwnsInput(config) {
		t.Fatal("the action bar does not own input on the panel it is drawn in")
	}

	currentSceneIndex = homeSceneIndex(config)
	if chatSelectionOwnsInput(config) {
		t.Error("a bar left open by a scene change still owns input")
	}
	if chatSelectionBar.open {
		t.Error("off the chat scene the stale action bar should be dropped")
	}
	if handleChatSelectionKey(keyEvent(sdl.K_LEFT), config) {
		t.Error("the stale bar consumed a key meant for the scene underneath")
	}
	press := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_LEFT}
	if ctrlChatSelection(press, config) {
		t.Error("the stale bar consumed a pad press meant for the scene underneath")
	}
}

// The member list and the profile card have the same owner as the action bar:
// they float inside the chat panel, so leaving that scene drops them rather
// than leaving them to eat the next press on the screen underneath.
func TestChatOverlaysReleaseInputOffTheChatScene(t *testing.T) {
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })

	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Steppa", Text: "first", Timestamp: now.Add(-time.Minute)},
		{ID: "2", Sender: "Alex", UserID: "222222222222222222", Text: "second", Timestamp: now},
	}
	chatMutex.Unlock()

	currentSceneIndex = findSceneIndex(config, "Chat")
	if !openChatMembers() {
		t.Fatal("the member list refused a history with speakers")
	}
	if !chatOverlayOwnsInput(config) {
		t.Fatal("the member list does not own input on the panel it is drawn in")
	}

	currentSceneIndex = homeSceneIndex(config)
	if chatOverlayOwnsInput(config) {
		t.Error("a member list left open by a scene change still owns input")
	}
	if chatOverlayOpen() {
		t.Error("off the chat scene the stale overlay should be dropped")
	}
	if handleChatOverlayKey(keyEvent(sdl.K_DOWN), config) {
		t.Error("the stale overlay consumed a key meant for the scene underneath")
	}
	press := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}
	if ctrlChatOverlay(press, config) {
		t.Error("the stale overlay consumed a pad press meant for the scene underneath")
	}
}

// The member list and the card are driveable without a mouse: Down walks the
// rows, Enter opens a profile, and Escape steps back out one layer at a time.
func TestChatOverlayKeyboardNavigation(t *testing.T) {
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// The list floats inside the chat panel, so the test stands on the scene
	// that draws it: off that scene the overlay is stale and gives input back.
	prevScene := currentSceneIndex
	currentSceneIndex = findSceneIndex(config, "Chat")
	t.Cleanup(func() { currentSceneIndex = prevScene })
	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Steppa", Text: "first", Timestamp: now.Add(-time.Minute)},
		{ID: "2", Sender: "Alex", UserID: "222222222222222222", Text: "second", Timestamp: now},
	}
	chatMutex.Unlock()

	if !openChatMembers() {
		t.Fatal("openChatMembers refused the history")
	}
	if !handleChatOverlayKey(keyEvent(sdl.K_DOWN), config) {
		t.Fatal("Down was not consumed by the member list")
	}
	if chatMembers.focus != 1 {
		t.Fatalf("focus = %d after Down, want 1", chatMembers.focus)
	}
	want := chatMembers.rows[1].Name
	if !handleChatOverlayKey(keyEvent(sdl.K_RETURN), config) {
		t.Fatal("Enter was not consumed by the member list")
	}
	if !chatProfile.open || chatProfile.profile.Name != want {
		t.Fatalf("Enter opened %+v, want %s's card", chatProfile.profile, want)
	}
	focus := chatProfile.focus
	if !handleChatOverlayKey(keyEvent(sdl.K_RIGHT), config) {
		t.Fatal("Right was not consumed by the card")
	}
	if chatProfile.focus != focus+1 {
		t.Errorf("card focus = %d after Right, want %d", chatProfile.focus, focus+1)
	}
	if !handleChatOverlayKey(keyEvent(sdl.K_ESCAPE), config) {
		t.Fatal("Escape was not consumed by the card")
	}
	if chatProfile.open {
		t.Error("Escape did not close the profile card")
	}
	if !chatMembers.open {
		t.Error("closing a card opened from the list should return to the list")
	}
	if !handleChatOverlayKey(keyEvent(sdl.K_ESCAPE), config) {
		t.Fatal("Escape was not consumed by the member list")
	}
	if chatMembers.open {
		t.Error("Escape did not close the member list")
	}
}

// "Latest" is the card action that answers "what did they say?": it closes the
// card, scrolls to their newest message and selects it.
func TestProfileLatestJumpsToTheMessage(t *testing.T) {
	resetChatProfileState(t)
	now := time.Now()
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{ID: "1", Sender: "Alex", Text: "old", Timestamp: now.Add(-time.Hour)},
		{ID: "2", Sender: "Steppa", Text: "middle", Timestamp: now.Add(-time.Minute)},
		{ID: "3", Sender: "Alex", Text: "new", Timestamp: now},
	}
	chatMutex.Unlock()

	profile := chatProfileForSender("Alex", "")
	if !jumpToMemberLatest(profile) {
		t.Fatal("jumpToMemberLatest found no message for Alex")
	}
	msg, index, ok := selectedChatMessage()
	if !ok || index != 2 || msg.Text != "new" {
		t.Fatalf("selected %+v (index %d, ok=%v), want Alex's newest message", msg, index, ok)
	}
	if chatScrollOffset != 0 {
		t.Errorf("scroll offset = %d, want 0 for the newest message", chatScrollOffset)
	}
}

// Short outgoing bubbles must not push their longer header beyond the panel.
// Long sender names yield space to the time and keep their profile hit target.
func TestChatMessageHeaderFitsPanel(t *testing.T) {
	resetChatProfileState(t)
	config := layoutTestConfig(t)
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("missing chat status font")
	}
	for _, tc := range []struct {
		name   string
		width  int32
		sender string
		own    bool
	}{
		{"short outgoing", 1240, "Juka", true},
		{"long outgoing", 500, strings.Repeat("LongName", 20), true},
		{"long incoming", 500, strings.Repeat("RemoteName", 20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatMessages = []ChatMessage{{ID: "1", Sender: tc.sender, Text: "test", Own: tc.own, Timestamp: time.Date(2026, 11, 2, 23, 21, 0, 0, time.UTC)}}
			elem := Element{Type: "chat", X: 0, Y: 0, Width: StringOrInt(fmt.Sprint(tc.width)), Height: "640", Font: "small"}
			componentFrame(t, func(r *sdl.Renderer) { renderChat(r, config, elem) })
			if len(chatMessageHits) != 1 {
				t.Fatalf("message hits = %d", len(chatMessageHits))
			}
			hit := chatMessageHits[0]
			left := int32(elem.X) + SpaceMD + SpaceSM
			right := int32(elem.X) + tc.width - SpaceMD - SpaceSM
			name, suffix, x, nameW := chatMessageHeader(font, tc.sender, "23:21", hit.body.X, hit.body.W, left, right, tc.own)
			if !strings.HasSuffix(suffix, "23:21") {
				t.Fatalf("timestamp missing: %q", suffix)
			}
			timeW, _, err := font.SizeUTF8(suffix)
			if err != nil {
				t.Fatal(err)
			}
			if x < left || x+nameW+int32(timeW) > right {
				t.Fatalf("header at %d..%d outside %d..%d", x, x+nameW+int32(timeW), left, right)
			}
			if tc.own && x+nameW+int32(timeW) != hit.body.X+hit.body.W {
				t.Fatalf("own header does not align with bubble: %+v", hit)
			}
			if hit.name.X != x || hit.name.W != nameW || name == "" {
				t.Fatalf("name hit %+v does not match %q at %d width %d", hit.name, name, x, nameW)
			}
			if !chatMessageClick(hit.name.X+2, hit.name.Y+2, config) || !chatProfile.open || chatProfile.profile.Name != tc.sender {
				t.Fatal("sender name no longer opens the correct profile")
			}
			closeChatProfile()
		})
	}
}

// Chat hitboxes survive a scene switch but must not steal the Hardware buttons.
func TestChatComposerHitboxesStayOnChatScene(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	previousScene := currentSceneIndex
	oldName, oldInput := chatNameRect, chatInputRect
	oldSend, oldAI, oldAttach := chatSendBtnRect, chatAskAIBtnRect, chatAttachBtnRect
	t.Cleanup(func() {
		currentSceneIndex = previousScene
		chatNameRect, chatInputRect = oldName, oldInput
		chatSendBtnRect, chatAskAIBtnRect, chatAttachBtnRect = oldSend, oldAI, oldAttach
	})
	config := &Config{Width: 1280, Height: 720, Variables: Variables{Custom: map[string]interface{}{}}, Scenes: []SceneConfig{
		{Name: "Chat", Elements: []Element{{Type: "chat"}}},
		{Name: "Hardware", Elements: []Element{{Type: "button", Text: "Refresh", X: 60, Y: 611, Width: "140", Height: "45"}}},
	}}
	ensureHardwareProcessEntry(config)
	ensureHardwareWiFiEntry(config)
	testedButtons := 0
	for _, button := range config.Scenes[1].Elements {
		if button.Trigger != "processes_open" && button.Trigger != "wifi_open" {
			continue
		}
		testedButtons++
		w, h := buttonHitSize(button, nil)
		mx, my := button.X+w/2, button.Y+h/2
		for _, target := range []*sdl.Rect{&chatNameRect, &chatInputRect, &chatSendBtnRect, &chatAskAIBtnRect, &chatAttachBtnRect} {
			chatNameRect, chatInputRect, chatSendBtnRect, chatAskAIBtnRect, chatAttachBtnRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
			*target = sdl.Rect{X: mx - 5, Y: my - 5, W: 10, H: 10}
			currentSceneIndex = 1
			virtualKeyboardActive = false
			if handleChatComposerMouse(mx, my, config) || virtualKeyboardActive {
				t.Fatalf("stale chat target stole %s click", button.Text)
			}
		}
	}

	if testedButtons != 2 {
		t.Fatalf("tested %d Hardware buttons, want Processes and Wi-Fi", testedButtons)
	}
	currentSceneIndex = 0
	chatNameRect, chatSendBtnRect, chatAskAIBtnRect, chatAttachBtnRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	chatInputRect = sdl.Rect{X: 20, Y: 30, W: 100, H: 40}
	virtualKeyboardActive = false
	if !handleChatComposerMouse(30, 40, config) || !virtualKeyboardActive || vkTarget != vkTargetChat {
		t.Fatal("the chat composer did not open its keyboard on the chat scene")
	}
	vkCloseKeyboard()
	chatInputRect = sdl.Rect{}
	chatNameRect = sdl.Rect{X: 20, Y: 30, W: 100, H: 40}
	if !handleChatComposerMouse(30, 40, config) || !virtualKeyboardActive || vkTarget != vkTargetName {
		t.Fatal("the chat name did not open its keyboard on the chat scene")
	}
	vkCloseKeyboard()
}
