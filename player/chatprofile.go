package main

// chatprofile.go — Discord-style member profiles for the chat screen.
//
// A name in a bubble is a person, not just a label: selecting it opens a card
// with their picture, handle, roles and history, plus the actions that make
// sense on a handheld (mention them, copy their id, open the picture, rename
// yourself). The same card is reachable from a member list, which is how a
// controller user reaches someone who has not written recently.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// chatMemberProfile is everything the chat knows about one speaker. It is
// derived from the message history (so it works offline and after a restart)
// and enriched with Discord member data when a session can answer.
type chatMemberProfile struct {
	Key       string
	Name      string
	Handle    string
	UserID    string
	AvatarURL string
	Bot       bool
	Local     bool
	AI        bool
	System    bool
	Messages  int
	FirstSeen time.Time
	LastSeen  time.Time
	LastText  string
}

// chatProfileCard is the open profile card. Focus indexes the action row.
type chatProfileCard struct {
	open     bool
	focus    int
	profile  chatMemberProfile
	fromList bool
	rect     sdl.Rect
	// avatar is the card's picture: tapping it opens the full image, the same
	// thing the Picture action does.
	avatar  sdl.Rect
	actions []chatProfileAction
}

type chatProfileAction struct {
	label string
	kind  string
	rect  sdl.Rect
}

var chatProfile chatProfileCard

// chatMembersPanel is the member list overlay.
type chatMembersPanel struct {
	open   bool
	focus  int
	scroll int
	rows   []chatMemberProfile
	rect   sdl.Rect
}

var chatMembers chatMembersPanel

const (
	chatProfileCardW = int32(470)
	chatProfileCardH = int32(330)
	chatMembersCardW = int32(470)
	// A row carries a name and a meta line, so its height follows two lines of
	// the small font plus padding; 52 px clipped the meta line on the handheld.
	chatMemberRowH = int32(64)
	chatMembersMax = 5
	// chatGroupWindow is how close together two messages from one sender must
	// be to collapse into one group (name and avatar drawn once).
	chatGroupWindow = 5 * time.Minute
)

// --- Discord member details ----------------------------------------------

// discordGuildID is the guild of the configured channel, learned when the
// gateway connects. Member lookups need it.
var discordGuildID string

type discordMemberDetails struct {
	loaded   bool
	nickname string
	joined   time.Time
	roles    []string
}

var (
	chatDetailsMu sync.Mutex
	chatDetails   = map[string]*discordMemberDetails{}
)

// resetDiscordMemberDetails drops cached member data. Called when a new
// gateway session connects, because permissions and roles may have changed.
func resetDiscordMemberDetails() {
	chatDetailsMu.Lock()
	chatDetails = map[string]*discordMemberDetails{}
	chatDetailsMu.Unlock()
}

// requestDiscordMemberDetails fetches nickname, join date and roles for a
// Discord user once, in the background. A failure is remembered so a profile
// that stays open does not hammer the API.
func requestDiscordMemberDetails(userID string) {
	if userID == "" || discordSession == nil || !isDiscordSnowflake(userID) {
		return
	}
	chatDetailsMu.Lock()
	if _, ok := chatDetails[userID]; ok {
		chatDetailsMu.Unlock()
		return
	}
	chatDetails[userID] = &discordMemberDetails{}
	guildID := discordGuildID
	session := discordSession
	chatDetailsMu.Unlock()
	if guildID == "" {
		// No guild: the profile still shows what the message carried.
		chatDetailsMu.Lock()
		chatDetails[userID] = &discordMemberDetails{loaded: true}
		chatDetailsMu.Unlock()
		return
	}
	go func() {
		details := &discordMemberDetails{loaded: true}
		if member, err := session.GuildMember(guildID, userID); err == nil && member != nil {
			details.nickname = strings.TrimSpace(member.Nick)
			details.joined = member.JoinedAt
			for _, roleID := range member.Roles {
				if name := discordCachedName(discordRoleNames, roleID); name != "" {
					details.roles = append(details.roles, name)
				}
			}
		}
		chatDetailsMu.Lock()
		chatDetails[userID] = details
		chatDetailsMu.Unlock()
	}()
}

func chatDetailsFor(userID string) discordMemberDetails {
	chatDetailsMu.Lock()
	defer chatDetailsMu.Unlock()
	if d, ok := chatDetails[userID]; ok && d != nil {
		return *d
	}
	return discordMemberDetails{}
}

// isDiscordSnowflake reports whether an id looks like a Discord snowflake.
// Local messages use random hex ids, so this keeps the two apart without a
// separate flag in the saved history.
func isDiscordSnowflake(id string) bool {
	if len(id) < 15 || len(id) > 21 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- Profiles from the history -------------------------------------------

// chatMemberKey groups messages by speaker. The Discord user id is authoritative
// when the message carries one, so two people called "Alex" never merge.
func chatMemberKey(msg ChatMessage) string {
	if msg.UserID != "" {
		return "id:" + msg.UserID
	}
	return "name:" + strings.ToLower(strings.TrimSpace(msg.Sender))
}

// chatMemberProfiles aggregates the message history into one entry per speaker,
// most recently active first.
func chatMemberProfiles() []chatMemberProfile {
	chatMutex.Lock()
	messages := append([]ChatMessage(nil), chatMessages...)
	chatMutex.Unlock()

	byKey := map[string]*chatMemberProfile{}
	order := make([]string, 0, 8)
	for _, msg := range messages {
		name := strings.TrimSpace(msg.Sender)
		if name == "" || strings.EqualFold(name, "System") {
			continue
		}
		key := chatMemberKey(msg)
		entry, ok := byKey[key]
		if !ok {
			entry = &chatMemberProfile{
				Key:    key,
				Name:   name,
				Local:  isLocalChatSender(name),
				AI:     strings.EqualFold(name, "Juka AI"),
				System: false,
			}
			byKey[key] = entry
			order = append(order, key)
		}
		entry.Messages++
		if entry.FirstSeen.IsZero() || msg.Timestamp.Before(entry.FirstSeen) {
			entry.FirstSeen = msg.Timestamp
		}
		if !msg.Timestamp.Before(entry.LastSeen) {
			entry.LastSeen = msg.Timestamp
			entry.LastText = msg.Text
			entry.Name = name
			if msg.AvatarURL != "" {
				entry.AvatarURL = msg.AvatarURL
			}
		}
		if msg.UserID != "" {
			entry.UserID = msg.UserID
		}
		if msg.Handle != "" {
			entry.Handle = msg.Handle
		}
		if msg.Bot {
			entry.Bot = true
		}
		if entry.AvatarURL == "" && msg.AvatarURL != "" {
			entry.AvatarURL = msg.AvatarURL
		}
	}

	profiles := make([]chatMemberProfile, 0, len(order))
	for _, key := range order {
		profiles = append(profiles, *byKey[key])
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		return profiles[i].LastSeen.After(profiles[j].LastSeen)
	})
	return profiles
}

// chatMembersList returns the member rows for the list overlay: the local user
// first (the person holding the device), then everyone else by recency.
func chatMembersList() []chatMemberProfile {
	profiles := chatMemberProfiles()
	sort.SliceStable(profiles, func(i, j int) bool {
		if profiles[i].Local != profiles[j].Local {
			return profiles[i].Local
		}
		return profiles[i].LastSeen.After(profiles[j].LastSeen)
	})
	return profiles
}

// chatProfileForSender builds the card contents for one name/id pair. When the
// message carried a user id the whole history of that id is used, otherwise the
// name alone decides — which is what a relayed or local message allows.
func chatProfileForSender(name, userID string) chatMemberProfile {
	key := "name:" + strings.ToLower(strings.TrimSpace(name))
	if userID != "" {
		key = "id:" + userID
	}
	for _, profile := range chatMemberProfiles() {
		if profile.Key == key {
			return profile
		}
	}
	name = strings.TrimSpace(name)
	return chatMemberProfile{
		Key:   key,
		Name:  name,
		Local: isLocalChatSender(name),
		AI:    strings.EqualFold(name, "Juka AI"),
	}
}

// chatProfileSource names where a profile comes from, in the same words the
// chat header uses.
func chatProfileSource(p chatMemberProfile) string {
	switch {
	case p.Local:
		return "This device"
	case p.AI:
		return "Built-in assistant"
	case p.UserID != "":
		return "Discord"
	default:
		return "Chat"
	}
}

// --- Overlay open/close ---------------------------------------------------

// chatOverlayOpen reports whether a chat profile or member list is up. Both are
// modal over the scene.
func chatOverlayOpen() bool {
	return chatProfile.open || chatMembers.open
}

// openChatProfile shows the card for a speaker.
func openChatProfile(name, userID string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	profile := chatProfileForSender(name, userID)
	chatProfile = chatProfileCard{
		open:     true,
		profile:  profile,
		fromList: chatMembers.open,
	}
	chatMembers.open = false
	chatProfile.actions = chatProfileActions(profile)
	requestDiscordMemberDetails(profile.UserID)
	PlayActivateSound()
	return true
}

func closeChatProfile() {
	if chatProfile.open && chatProfile.fromList {
		chatProfile = chatProfileCard{}
		chatMembers.open = true
		return
	}
	chatProfile = chatProfileCard{}
}

// openChatMembers shows the member list for the chat panel.
func openChatMembers() bool {
	rows := chatMembersList()
	if len(rows) == 0 {
		return false
	}
	chatMembers = chatMembersPanel{open: true, rows: rows}
	if idx := chatProfileIndexFor(rows, 0); idx >= 0 {
		chatMembers.focus = idx
	}
	PlayActivateSound()
	return true
}

func closeChatMembers() {
	chatMembers = chatMembersPanel{}
}

// chatProfileIndexFor is a small helper used when the list opens: it keeps the
// focus inside the row slice.
func chatProfileIndexFor(rows []chatMemberProfile, idx int) int {
	if len(rows) == 0 {
		return -1
	}
	return clampIndex(idx, len(rows))
}

// chatProfileActions lists the buttons for a profile, in a stable order, so the
// focus index survives a refresh of the card.
func chatProfileActions(p chatMemberProfile) []chatProfileAction {
	actions := make([]chatProfileAction, 0, 6)
	if p.Local {
		actions = append(actions, chatProfileAction{label: "Rename", kind: "rename"})
	}
	if p.UserID != "" {
		actions = append(actions, chatProfileAction{label: "Copy ID", kind: "copy_id"})
	}
	if !p.System {
		actions = append(actions, chatProfileAction{label: "Mention", kind: "mention"})
	}
	if p.Messages > 0 {
		actions = append(actions, chatProfileAction{label: "Latest", kind: "jump"})
	}
	if p.AvatarURL != "" {
		actions = append(actions, chatProfileAction{label: "Picture", kind: "avatar"})
	}
	actions = append(actions, chatProfileAction{label: "Close", kind: "close"})
	return actions
}

// chatProfileSearchText is the mention token the chat inserts: "@name " with
// the spaces of a real name collapsed, so the mention stays one token.
func chatProfileSearchText(p chatMemberProfile) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ""
	}
	return "@" + strings.ReplaceAll(name, " ", "") + " "
}

// --- Input ---------------------------------------------------------------

// handleChatOverlayKey drives both overlays from the keyboard. It reports
// whether the key was consumed, and always consumes while a card is up so the
// scene underneath cannot react.
func handleChatOverlayKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !chatOverlayOpen() || e == nil {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	if chatProfile.open {
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
			closeChatProfile()
			PlayBackSound()
		case sdl.K_LEFT, sdl.K_a:
			chatProfile.focus = clampIndex(chatProfile.focus-1, len(chatProfile.actions))
			PlayNavSound()
		case sdl.K_RIGHT, sdl.K_d, sdl.K_TAB:
			chatProfile.focus = clampIndex(chatProfile.focus+1, len(chatProfile.actions))
			PlayNavSound()
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			if chatProfile.focus >= 0 && chatProfile.focus < len(chatProfile.actions) {
				activateChatProfileAction(chatProfile.actions[chatProfile.focus].kind, config)
			}
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		closeChatMembers()
		PlayBackSound()
	case sdl.K_UP, sdl.K_w:
		if chatMembers.focus > 0 {
			chatMembers.focus--
			chatMembersEnsureVisible()
			PlayNavSound()
		}
	case sdl.K_DOWN, sdl.K_s:
		if chatMembers.focus < len(chatMembers.rows)-1 {
			chatMembers.focus++
			chatMembersEnsureVisible()
			PlayNavSound()
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if chatMembers.focus >= 0 && chatMembers.focus < len(chatMembers.rows) {
			row := chatMembers.rows[chatMembers.focus]
			openChatProfile(row.Name, row.UserID)
		}
	}
	return true
}

// ctrlChatOverlay is the controller twin of handleChatOverlayKey.
func ctrlChatOverlay(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !chatOverlayOpen() || e == nil {
		return true
	}
	if chatProfile.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			chatProfile.focus = clampIndex(chatProfile.focus-1, len(chatProfile.actions))
			PlayNavSound()
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			chatProfile.focus = clampIndex(chatProfile.focus+1, len(chatProfile.actions))
			PlayNavSound()
		case sdl.CONTROLLER_BUTTON_A:
			if chatProfile.focus >= 0 && chatProfile.focus < len(chatProfile.actions) {
				activateChatProfileAction(chatProfile.actions[chatProfile.focus].kind, config)
			}
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			closeChatProfile()
			PlayBackSound()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if chatMembers.focus > 0 {
			chatMembers.focus--
			chatMembersEnsureVisible()
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if chatMembers.focus < len(chatMembers.rows)-1 {
			chatMembers.focus++
			chatMembersEnsureVisible()
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_A:
		if chatMembers.focus >= 0 && chatMembers.focus < len(chatMembers.rows) {
			row := chatMembers.rows[chatMembers.focus]
			openChatProfile(row.Name, row.UserID)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeChatMembers()
		PlayBackSound()
	}
	return true
}

func chatMembersEnsureVisible() {
	if chatMembers.focus < chatMembers.scroll {
		chatMembers.scroll = chatMembers.focus
	}
	if chatMembers.focus >= chatMembers.scroll+chatMembersMax {
		chatMembers.scroll = chatMembers.focus - chatMembersMax + 1
	}
	chatMembers.scroll = clampIndex(chatMembers.scroll, len(chatMembers.rows)-chatMembersMax+1)
}

// chatOverlayClick consumes clicks while either overlay is up: a click on an
// action or a member row activates it, anything else dismisses the card so the
// scene underneath never receives a stray tap.
func chatOverlayClick(mx, my int32, config *Config) bool {
	if !chatOverlayOpen() {
		return false
	}
	if chatProfile.open {
		if chatProfile.profile.AvatarURL != "" && pointInRect(mx, my, chatProfile.avatar) {
			activateChatProfileAction("avatar", config)
			return true
		}
		for _, action := range chatProfile.actions {
			if pointInRect(mx, my, action.rect) {
				activateChatProfileAction(action.kind, config)
				return true
			}
		}
		if !pointInRect(mx, my, chatProfileCardRect()) {
			closeChatProfile()
			PlayBackSound()
		}
		return true
	}
	rowH := chatMemberRowH
	for i, row := range chatMembers.rows[chatMembers.scroll:] {
		if i >= chatMembersMax {
			break
		}
		rowRect := sdl.Rect{
			X: chatMembers.rect.X + SpaceMD,
			Y: chatMembers.rect.Y + 52 + int32(i)*rowH,
			W: chatMembers.rect.W - SpaceMD*2,
			H: rowH - SpaceXS,
		}
		if pointInRect(mx, my, rowRect) {
			chatMembers.focus = chatMembers.scroll + i
			openChatProfile(row.Name, row.UserID)
			return true
		}
	}
	if !pointInRect(mx, my, chatMembers.rect) {
		closeChatMembers()
		PlayBackSound()
	}
	return true
}

// activateChatProfileAction runs one card button.
func activateChatProfileAction(kind string, config *Config) {
	profile := chatProfile.profile
	switch kind {
	case "close":
		closeChatProfile()
		PlayBackSound()
	case "rename":
		closeChatProfile()
		closeChatMembers()
		vkOpenName()
	case "copy_id":
		if profile.UserID != "" {
			sdl.SetClipboardText(profile.UserID)
			showToast("User ID copied", ToastInfo())
		}
	case "mention":
		if token := chatProfileSearchText(profile); token != "" {
			closeChatProfile()
			closeChatMembers()
			chatInputText += token
			vkOpenChat()
		}
	case "jump":
		jumpToMemberLatest(profile)
	case "avatar":
		if profile.AvatarURL != "" {
			url := chatAvatarURLAtSize(profile.AvatarURL, 256)
			closeChatProfile()
			closeChatMembers()
			openImageViewerURL(url)
		}
	}
}

// jumpToMemberLatest closes the card and scrolls the history to the speaker's
// newest message, selecting it so the next press acts on it. It is how a
// profile answers "what did they say?" without leaving the chat.
func jumpToMemberLatest(p chatMemberProfile) bool {
	chatMutex.Lock()
	index := -1
	for i := len(chatMessages) - 1; i >= 0; i-- {
		if chatMemberKey(chatMessages[i]) == p.Key {
			index = i
			break
		}
	}
	total := len(chatMessages)
	chatMutex.Unlock()
	if index < 0 {
		showToast("No messages from "+p.Name+" in this history", ToastWarn())
		return false
	}
	closeChatProfile()
	closeChatMembers()
	chatScrollOffset = clampIndex(total-1-index, total)
	selectChatMessage(index)
	chatEnsureSelectionVisible()
	return true
}

// chatAvatarURLAtSize asks Discord's CDN for a sharp avatar at the size the
// profile card draws. Other hosts keep whatever URL they had.
func chatAvatarURLAtSize(rawURL string, size int) string {
	idx := strings.Index(rawURL, "?size=")
	if idx < 0 {
		return rawURL
	}
	return fmt.Sprintf("%s?size=%d", rawURL[:idx], size)
}

// --- Layout and drawing ---------------------------------------------------

// chatProfileCardRect centers the profile card. It is clamped to the viewport
// so a small panel keeps the whole card on screen.
func chatProfileCardRect() sdl.Rect {
	w, h := chatProfileCardW, chatProfileCardH
	if w > screenWidth-32 {
		w = screenWidth - 32
	}
	if h > screenHeight-140 {
		h = screenHeight - 140
	}
	return sdl.Rect{
		X: (screenWidth - w) / 2,
		Y: (screenHeight - h) / 2,
		W: w,
		H: h,
	}
}

// chatMembersCardRect sizes the member list to its rows.
func chatMembersCardRect() sdl.Rect {
	w := chatMembersCardW
	if w > screenWidth-32 {
		w = screenWidth - 32
	}
	rows := len(chatMembers.rows)
	if rows > chatMembersMax {
		rows = chatMembersMax
	}
	h := 52 + int32(rows)*chatMemberRowH + 34
	if h > screenHeight-100 {
		h = screenHeight - 100
	}
	return sdl.Rect{
		X: (screenWidth - w) / 2,
		Y: (screenHeight - h) / 2,
		W: w,
		H: h,
	}
}

// drawChatScrim dims the scene behind a card so the card reads as foreground.
func drawChatScrim(renderer *sdl.Renderer) {
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 120)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
}

// renderChatProfile draws the profile card. It is called by the shared render
// loop after the scene so it floats above the chat panel.
func renderChatProfile(renderer *sdl.Renderer, config *Config) {
	if !chatProfile.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	titleFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if titleFont == nil {
		titleFont = font
	}

	r := chatProfileCardRect()
	chatProfile.rect = r
	drawChatScrim(renderer)
	UICard(renderer, r, UICardStyle{
		Fill:   sdl.Color{R: 16, G: 20, B: 30, A: 248},
		Border: ColorBorder,
		Radius: RadiusLG,
		Shadow: 110,
	})

	p := chatProfile.profile
	details := chatDetailsFor(p.UserID)
	displayName := p.Name
	if details.nickname != "" {
		displayName = details.nickname
	}

	avatarSize := int32(84)
	pad := int32(20)
	ax, ay := r.X+pad, r.Y+pad
	chatProfile.avatar = sdl.Rect{X: ax, Y: ay, W: avatarSize, H: avatarSize}
	renderChatAvatar(renderer, config, titleFont, ax, ay, avatarSize, p.Name,
		chatAvatarURLAtSize(p.AvatarURL, 128), senderColorForName(p.Name))

	// Discord puts the name on one line and the handle under it; the badge on
	// the right says how the account is known.
	textX := ax + avatarSize + SpaceMD
	textW := r.X + r.W - pad - textX
	if textW < 80 {
		textW = 80
	}
	nameCol := ColorTextPrimary()
	renderText(renderer, config, titleFont, ellipsize(titleFont, displayName, textW), nameCol, textX, ay+2)
	handle := ""
	if p.Handle != "" {
		handle = "@" + p.Handle
	} else if p.Local {
		handle = "Local chat name"
	} else if p.AI {
		handle = "Answers run on Groq"
	}
	if handle != "" {
		renderText(renderer, config, font, ellipsize(font, handle, textW), ColorTextSecondary(), textX, ay+2+int32(titleFont.Height())+2)
	}

	// Badges: BOT / YOU / AI, drawn after the handle so they never collide.
	badges := make([]string, 0, 2)
	if p.Bot {
		badges = append(badges, "BOT")
	}
	if p.Local {
		badges = append(badges, "YOU")
	}
	if p.AI {
		badges = append(badges, "AI")
	}
	badgeY := ay + 2 + int32(titleFont.Height()) + 2 + int32(font.Height()) + 4
	for _, label := range badges {
		bw, _, _ := font.SizeUTF8(label)
		bw += 14
		fillRoundedRect(renderer, textX, badgeY, int32(bw), int32(font.Height())+6, (int32(font.Height())+6)/2, WithAlpha(ColorInfo, 45))
		renderText(renderer, config, font, label, ColorInfo, textX+7, badgeY+3)
		textX += int32(bw) + 6
	}

	// Divider under the identity block.
	dividerY := r.Y + pad + avatarSize + 12
	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 90)
	renderer.FillRect(&sdl.Rect{X: r.X + pad, Y: dividerY, W: r.W - pad*2, H: 1})

	rowY := dividerY + 10
	lineH := int32(font.Height())
	metaW := r.W - pad*2

	// Where the account lives, and when it joined.
	source := chatProfileSource(p)
	if p.UserID != "" && details.joined.Year() > 1 {
		source += " · member since " + details.joined.Format("2 Jan 2006")
	}
	renderText(renderer, config, font, ellipsize(font, source, metaW), ColorTextTertiary(), r.X+pad, rowY)
	rowY += lineH + 4

	if len(details.roles) > 0 {
		label := "Roles: " + strings.Join(details.roles, ", ")
		renderText(renderer, config, font, ellipsize(font, label, metaW), ColorTextSecondary(), r.X+pad, rowY)
		rowY += lineH + 4
	}

	// What they said last, quoted the way a chat reply quotes it. It is the
	// most useful thing a profile can show before the player jumps there.
	if p.LastText != "" {
		last := "Last: " + chatQuoteSummary(p.LastText, 160)
		renderText(renderer, config, font, ellipsize(font, last, metaW), ColorTextSecondary(), r.X+pad, rowY)
		rowY += lineH + 4
	}

	stat := fmt.Sprintf("%d message", p.Messages)
	if p.Messages != 1 {
		stat += "s"
	}
	if !p.LastSeen.IsZero() {
		stat += " · last " + timeSince(p.LastSeen)
	}
	if !p.FirstSeen.IsZero() && p.LastSeen.Sub(p.FirstSeen) > 24*time.Hour {
		stat += " · first seen " + p.FirstSeen.Format("2 Jan 2006")
	}
	if p.Messages > 0 {
		renderText(renderer, config, font, ellipsize(font, stat, metaW), ColorTextSecondary(), r.X+pad, rowY)
		rowY += lineH + 4
	}
	if p.UserID != "" {
		id := "ID " + p.UserID
		renderText(renderer, config, font, ellipsize(font, id, metaW), ColorTextTertiary(), r.X+pad, rowY)
	}

	// Actions along the bottom, wrapping to a second row on a narrow card.
	buttonH := int32(30)
	buttonY := r.Y + r.H - pad - buttonH
	if len(chatProfile.actions) > 4 {
		buttonY -= buttonH + SpaceSM
	}
	bx := r.X + pad
	for i := range chatProfile.actions {
		action := &chatProfile.actions[i]
		labelW, _, _ := font.SizeUTF8(action.label)
		w := int32(labelW) + 22
		if bx+w > r.X+r.W-pad {
			bx = r.X + pad
			buttonY += buttonH + SpaceSM
		}
		action.rect = UIButton(renderer, config, sdl.Rect{X: bx, Y: buttonY, W: w, H: buttonH}, action.label, UIButtonStyle{
			Variant:    UIButtonCustom,
			Fill:       WithAlpha(ColorSurfaceAlt, 230),
			HoverFill:  ColorCardHover,
			FocusFill:  ColorCardFocus,
			Border:     WithAlpha(ColorBorder, 160),
			LabelColor: ColorTextPrimary(),
			Font:       font,
			Pill:       true,
			NoShadow:   true,
		}, UIButtonState{Focused: i == chatProfile.focus})
		bx += w + SpaceSM
	}
}

// renderChatMembers draws the member list card.
func renderChatMembers(renderer *sdl.Renderer, config *Config) {
	if !chatMembers.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	r := chatMembersCardRect()
	chatMembers.rect = r
	drawChatScrim(renderer)
	UICard(renderer, r, UICardStyle{
		Fill:   sdl.Color{R: 16, G: 20, B: 30, A: 248},
		Border: ColorBorder,
		Radius: RadiusLG,
		Shadow: 110,
	})

	pad := int32(16)
	renderText(renderer, config, font, "Members", ColorTextPrimary(), r.X+pad, r.Y+14)
	count := fmt.Sprintf("%d", len(chatMembers.rows))
	countW, _, _ := font.SizeUTF8(count)
	renderText(renderer, config, font, count, ColorTextTertiary(), r.X+r.W-pad-int32(countW), r.Y+14)

	renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 90)
	renderer.FillRect(&sdl.Rect{X: r.X + pad, Y: r.Y + 44, W: r.W - pad*2, H: 1})

	chatMembers.rows = chatMembersList()
	chatMembers.focus = clampIndex(chatMembers.focus, len(chatMembers.rows))
	rowH := chatMemberRowH
	for i := chatMembers.scroll; i < len(chatMembers.rows) && i < chatMembers.scroll+chatMembersMax; i++ {
		row := chatMembers.rows[i]
		rowRect := sdl.Rect{X: r.X + pad, Y: r.Y + 52 + int32(i-chatMembers.scroll)*rowH, W: r.W - pad*2, H: rowH - SpaceXS}
		UIListRow(renderer, rowRect, RadiusSM, i == chatMembers.focus, false)
		avatar := int32(32)
		renderChatAvatar(renderer, config, font, rowRect.X+8, rowRect.Y+(rowRect.H-avatar)/2, avatar, row.Name, row.AvatarURL, senderColorForName(row.Name))
		nameX := rowRect.X + 8 + avatar + 10
		nameW := rowRect.W - (nameX - rowRect.X) - 160
		if nameW < 60 {
			nameW = 60
		}
		renderText(renderer, config, font, ellipsize(font, row.Name, nameW), ColorTextPrimary(), nameX, rowRect.Y+5)
		meta := fmt.Sprintf("%d msg", row.Messages)
		if !row.LastSeen.IsZero() {
			meta += " · " + timeSince(row.LastSeen)
		}
		renderText(renderer, config, font, ellipsize(font, meta, rowRect.W-16), ColorTextTertiary(), nameX, rowRect.Y+5+int32(font.Height()))
		if row.Local || row.Bot || row.AI {
			tag := "BOT"
			switch {
			case row.Local:
				tag = "YOU"
			case row.AI:
				tag = "AI"
			}
			tw, _, _ := font.SizeUTF8(tag)
			chipW := int32(tw) + 12
			chipX := rowRect.X + rowRect.W - chipW - 8
			chipY := rowRect.Y + (rowRect.H-(int32(font.Height())+4))/2
			fillRoundedRect(renderer, chipX, chipY, chipW, int32(font.Height())+4, (int32(font.Height())+4)/2, WithAlpha(ColorInfo, 45))
			renderText(renderer, config, font, tag, ColorInfo, chipX+6, chipY+2)
		}
	}

	// Scroll hint only when there is more than one screen of members.
	hints := []FooterHint{{Button: "A", Label: "Open profile"}, {Button: "B", Label: "Close"}}
	if len(chatMembers.rows) > chatMembersMax {
		hints = append([]FooterHint{{Button: "Up/Down", Label: "Members"}}, hints...)
	}
	hint := hintLine(hints...)
	hintW, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(), r.X+(r.W-int32(hintW))/2, r.Y+r.H-24)
}

// drawChatMembersChip draws the header chip that opens the member list. It is
// shared by the chat header so the rect it records is the one clicks use.
func drawChatMembersChip(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y int32, count int) sdl.Rect {
	if font == nil || count <= 0 {
		return sdl.Rect{}
	}
	label := fmt.Sprintf("Members %d", count)
	w, _, _ := font.SizeUTF8(label)
	chipW := int32(w) + 18
	h := int32(font.Height()) + 8
	rect := sdl.Rect{X: x, Y: y, W: chipW, H: h}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, WithAlpha(ColorSurfaceAlt, 220))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, 1, WithAlpha(ColorBorder, 160))
	renderText(renderer, config, font, label, ColorTextSecondary(), rect.X+9, rect.Y+4)
	return rect
}
