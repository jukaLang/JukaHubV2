package main

// Tests for the Discord-style member profiles in the chat: identity carried by a
// message, grouping of the history into people, the name/avatar targets that
// open a card, the member list, and the actions a card offers.

import (
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
	chatScrollOffset = 0
	chatSelected, chatSelectedID = -1, ""
	chatProfile = chatProfileCard{}
	chatMembers = chatMembersPanel{}
	chatMessageHits = nil
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages = prevMessages
		chatMutex.Unlock()
		chatScrollOffset = prevOffset
		chatSelected, chatSelectedID = prevSelected, prevSelectedID
		chatProfile, chatMembers = prevProfile, prevMembers
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
		t.Skip("Chat scene missing")
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
	// Rows are drawn from the list's own top padding; click the first row.
	if !chatOverlayClick(chatMembers.rect.X+chatMembers.rect.W/2, chatMembers.rect.Y+60, config) {
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

// The member list and the card are driveable without a mouse: Down walks the
// rows, Enter opens a profile, and Escape steps back out one layer at a time.
func TestChatOverlayKeyboardNavigation(t *testing.T) {
	resetChatProfileState(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
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
