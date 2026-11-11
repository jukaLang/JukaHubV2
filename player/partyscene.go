package main

// partyscene.go — the Watch Party screen.
//
// Two states, one screen:
//
//   - Not in a room: a hero card with Host a room / Join with a code, the rooms
//     other handhelds on this Wi-Fi are hosting (found live by partydiscovery.go
//     while the screen is up, one press to join), rooms saved earlier, and the
//     watch history. Without a network address everything that needs one is
//     dimmed and says so, and the Join button becomes a way to Wi-Fi.
//   - In a room: what the room is watching with its sync state, the shared
//     queue (A plays an item for everyone, X removes it), an "Add to the queue"
//     row (what is playing now, a copied link, a typed link or Media), the
//     invite card with a QR code and the short code, who is in the room, and
//     Leave.
//
// Network work never runs on the render thread: joins, queue edits and scans
// run on goroutines and post their results back through tbxPost. The screen
// rebuilds its strings a few times a second rather than every frame.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

// partyRowModel is one prepared list row.
type partyRowModel struct {
	id   string
	spec tbxRowSpec
}

// partyMemberLine is one line of the "In the room" card.
type partyMemberLine struct {
	name  string
	watch string
	host  bool
}

// partyModel is what the screen draws, rebuilt a few times a second.
type partyModel struct {
	builtAt time.Time
	inRoom  bool
	hosting bool
	lan     string
	header  UIPageHeader

	// Lobby.
	nearby      []partyRowModel
	emptyTitle  string
	emptyBody   string
	friends     []partyRowModel
	heroTitle   string
	heroBody    string
	joinLabel   string
	hostLabel   string
	hostBlocked bool

	// Room.
	snap         partySnapshot
	received     time.Time
	queue        []partyRowModel
	npTitle      string
	npState      string
	npSync       string
	npSyncTone   tbxTone
	npHint       string
	npProgress   float64
	npTime       string
	members      []partyMemberLine
	membersLabel string
	inviteURL    string
	unreach      bool
}

// partyScreenState is the screen's own state; the room lives in partySession.
type partyScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model partyModel
	// busy is set while a join or a host is on the network.
	busy      bool
	busyLabel string
	// qr caches the invite code's modules for the link it was made from.
	qr    [][]bool
	qrFor string
	wrap  tbxWrapCache
}

var partyScreen partyScreenState

// partyOpenScreen opens Watch Party. Cross-area contract: the Tools hub, F8 and
// the old overlay's callers all come through here.
func partyOpenScreen(config *Config) {
	tbxOpenScene(config, partySceneName)
}

type partyScene struct{ sceneBase }

func (partyScene) render(r *sdl.Renderer, c *Config) bool {
	partyRenderScreen(r, c)
	return true
}

func (partyScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), partySceneAction)
}

func (partyScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), partySceneAction)
}

func init() { registerScene(partyScene{sceneBase{name: partySceneName}}) }

// partyDiscoveryEnabled is a test seam: render tests switch the LAN scan off.
var partyDiscoveryEnabled = true

// partyScreenEnter runs once per visit: a fresh nearby list and the scan that
// fills it, tied to this visit so leaving the screen stops it.
func partyScreenEnter() {
	partyNearbyReset()
	if partyDiscoveryEnabled {
		runSceneTask(partyDiscoveryLoop)
	}
	partyScreen.nav.scroll = 0
	partyScreen.nav.focus = ""
	partyScreen.model.builtAt = time.Time{}
}

// ── Model ────────────────────────────────────────────────────────────────

// partyBuildModel prepares every string the screen draws.
func partyBuildModel(now time.Time) partyModel {
	m := partyModel{builtAt: now, lan: partyLANAddress()}
	snap, received, inRoom, _ := partyState()
	m.inRoom = inRoom
	if inRoom {
		m.hosting = partyIsHosting()
		m.snap = snap
		m.received = received
		partyBuildRoom(&m, now)
		return m
	}
	partyBuildLobby(&m)
	return m
}

func partyBuildLobby(m *partyModel) {
	switch {
	case partyScreen.busy:
		m.header = tbxHeader("Watch Party", "Watch together on the same Wi-Fi", "party", partyScreen.busyLabel, glassAccent())
	case m.lan == "":
		m.header = tbxHeader("Watch Party", "Watch together on the same Wi-Fi", "party", "Needs Wi-Fi", ToastWarnInk())
	default:
		m.header = tbxHeader("Watch Party", "Watch together on the same Wi-Fi", "party", "On Wi-Fi · "+m.lan, ColorTextTertiary())
	}
	m.heroTitle = "Watch together"
	m.heroBody = "Host a room or join one nearby: everyone on the same Wi-Fi sees the same video at the same moment."
	m.joinLabel = "Join with a code"
	m.hostLabel = "Host a room"
	if m.lan == "" {
		m.heroBody = "Connect to Wi-Fi to host or join a room. Rooms on your network appear below."
		m.joinLabel = "Wi-Fi settings"
		m.hostBlocked = true
	}
	if partyScreen.busy {
		m.hostBlocked = true
	}

	rooms, scans := partyNearbySnapshot()
	for _, r := range rooms {
		title := r.Room
		if title == "" {
			title = "Room " + r.Code
		}
		if r.Host != "" && !strings.EqualFold(r.Host, title) {
			title += " · " + r.Host
		}
		watching := fmt.Sprintf("%d watching", r.Members)
		if r.Members == 1 {
			watching = "1 watching"
		}
		sub := watching + " · nothing playing yet"
		if r.Title != "" {
			sub = watching + " · " + r.Title
		}
		m.nearby = append(m.nearby, partyRowModel{
			id:   "near:" + r.Addr + "|" + r.Code,
			spec: tbxRowSpec{icon: "party", title: title, subtitle: sub, pill: "Join", tone: tbxToneAccent},
		})
	}
	switch {
	case m.lan == "":
		m.emptyTitle = "Not on Wi-Fi"
		m.emptyBody = "Rooms on your network appear here once you are connected."
	case scans < 2:
		m.emptyTitle = "Looking for rooms on this Wi-Fi…"
		m.emptyBody = "Rooms hosted on other handhelds show up here."
	default:
		m.emptyTitle = "No rooms nearby yet"
		m.emptyBody = "Ask a friend to host, or join with a code."
	}
	for _, f := range partyFriends() {
		m.friends = append(m.friends, partyRowModel{
			id:   "friend:" + f.Invite,
			spec: tbxRowSpec{icon: "users", title: f.Name, subtitle: f.Invite, pill: "Rejoin", tone: tbxToneNeutral, dim: m.lan == ""},
		})
	}
}

func partyBuildRoom(m *partyModel, now time.Time) {
	snap := m.snap
	sub := "Room " + snap.Code
	if m.hosting {
		sub = "You are hosting room " + snap.Code
	} else if host := partyHostName(snap); host != "" {
		sub = "Room " + snap.Code + " · hosted by " + host
	}
	n := len(snap.Members)
	status := fmt.Sprintf("%d watching", n)
	if n == 1 {
		status = "Just you so far"
	}
	m.unreach = partyHostUnreachableFor() > 8*time.Second
	statusColor := glassAccent()
	if m.unreach {
		status = "Host not answering"
		statusColor = ToastWarnInk()
	}
	m.header = tbxHeader("Watch Party", sub, "party", status, statusColor)

	m.npProgress = -1
	if snap.Playback.URL == "" {
		m.npTitle = "Nothing playing yet"
		m.npHint = "Add a video below, then press A on it to start it for everyone."
	} else {
		m.npTitle = snap.Playback.Title
		if m.npTitle == "" {
			m.npTitle = partyTitleFromURL(snap.Playback.URL)
		}
		m.npState = "Paused"
		if snap.Playback.Playing {
			m.npState = "Playing"
		}
		m.npSync, m.npSyncTone = partySyncStatus(snap, m.received, now)
		target := partyLivePosition(snap, m.received, now)
		if partyWatchingRemote(snap) {
			st := snapshotVideoState()
			if st.duration > 0 {
				m.npProgress = st.progress
				m.npTime = partyClock(st.progress*st.duration) + " / " + partyClock(st.duration)
			}
			m.npHint = "A pauses or resumes it for everyone"
			if !snap.Playback.Playing {
				m.npHint = "A resumes it for everyone"
			}
		} else {
			m.npTime = "The room is at " + partyClock(target)
			m.npHint = "A joins the video at the room's position"
		}
	}

	for i, item := range snap.Queue {
		title := item.Title
		if title == "" {
			title = partyTitleFromURL(item.URL)
		}
		spec := tbxRowSpec{icon: "video", title: title, subtitle: "Added by " + item.AddedBy}
		if i == snap.Current {
			spec.pill, spec.tone = "Now playing", tbxToneAccent
		}
		m.queue = append(m.queue, partyRowModel{id: "q:" + strconv.Itoa(item.ID), spec: spec})
	}

	for _, member := range snap.Members {
		m.members = append(m.members, partyMemberLine{
			name:  member.Name,
			watch: partyFormatWatch(member.WatchSecs),
			host:  member.ID == snap.HostID,
		})
	}
	m.membersLabel = fmt.Sprintf("IN THE ROOM · %d", len(snap.Members))
	m.inviteURL = partyInviteURL(snap)
}

// partySyncStatus says whether this device is watching the room's video and
// how far its playhead is from the room's.
func partySyncStatus(snap partySnapshot, received, now time.Time) (string, tbxTone) {
	if partyHostUnreachableFor() > 6*time.Second {
		return "Reconnecting…", tbxToneWarn
	}
	if snap.Playback.URL == "" {
		return "", tbxToneNeutral
	}
	if !partyWatchingRemote(snap) {
		return "Not watching", tbxToneNeutral
	}
	st := snapshotVideoState()
	if st.phase == "downloading" {
		return "Loading…", tbxToneNeutral
	}
	if !snap.Playback.Playing || st.phase != "playing" {
		return "In sync", tbxToneGood
	}
	local := partyLocalPosition()
	target := partyLivePosition(snap, received, now)
	drift := local - target
	if drift < 0 {
		drift = -drift
	}
	if drift <= 2 {
		return "In sync", tbxToneGood
	}
	if local < target {
		return fmt.Sprintf("Catching up · %.0fs behind", drift), tbxToneWarn
	}
	return fmt.Sprintf("Syncing · %.0fs ahead", drift), tbxToneWarn
}

// partyClock formats seconds as m:ss or h:mm:ss.
func partyClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// partyInviteQR returns the QR modules for the invite link, rebuilt only when
// the link changes.
func partyInviteQR(link string) [][]bool {
	if link == "" {
		return nil
	}
	if link == partyScreen.qrFor && partyScreen.qr != nil {
		return partyScreen.qr
	}
	partyScreen.qrFor = link
	partyScreen.qr = nil
	code, err := qrcode.New(link, qrcode.Medium)
	if err == nil {
		partyScreen.qr = code.Bitmap()
	}
	return partyScreen.qr
}

// ── Render ───────────────────────────────────────────────────────────────

func partyRenderScreen(renderer *sdl.Renderer, config *Config) {
	if partyScreen.visit.begin() {
		partyScreenEnter()
	}
	now := time.Now()
	if now.Sub(partyScreen.model.builtAt) > 250*time.Millisecond {
		partyScreen.model = partyBuildModel(now)
	}
	m := &partyScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &partyScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	if m.inRoom {
		partyRenderRoom(renderer, config, fonts, frame, m)
	} else {
		partyRenderLobby(renderer, config, fonts, frame, m)
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func partyRenderLobby(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, m *partyModel) {
	nav := &partyScreen.nav
	top := frame.bodyTop - nav.scroll
	x, w := frame.x, frame.w
	scr := func(r sdl.Rect) sdl.Rect {
		r.Y += top
		return r
	}
	y := int32(0)
	row := 0

	// Hero card: the badge, what Watch Party does, and the two ways in.
	heroH := int32(150)
	hero := sdl.Rect{X: x, Y: y, W: w, H: heroH}
	UICard(renderer, scr(hero), DefaultUICardStyle())
	badge := int32(64)
	box := drawGlassBadge(renderer, scr(sdl.Rect{X: x + 24, Y: y + (heroH-badge)/2, W: badge, H: badge}))
	DrawHomeIcon(renderer, "party", box, false)
	btnH := int32(48)
	hostW := tbxButtonWidth(fonts.body, m.hostLabel, 168)
	joinW := tbxButtonWidth(fonts.body, m.joinLabel, 168)
	hostRect := sdl.Rect{X: x + w - 24 - hostW, Y: y + (heroH-btnH)/2, W: hostW, H: btnH}
	joinRect := sdl.Rect{X: hostRect.X - 12 - joinW, Y: hostRect.Y, W: joinW, H: btnH}
	textX := x + 24 + badge + 20
	textW := joinRect.X - 24 - textX
	if textW < 120 {
		textW = 120
	}
	lines := partyScreen.wrap.get(fonts.small, m.heroBody, textW)
	if len(lines) > 3 {
		lines = lines[:3]
	}
	blockH := fonts.titleH + 6 + int32(len(lines))*(fonts.smallH+2)
	ty := top + y + (heroH-blockH)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.heroTitle, textW), ColorTextPrimary(), textX, ty)
	ty += fonts.titleH + 6
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), textX, ty)
		ty += fonts.smallH + 2
	}
	tbxDrawButton(renderer, config, fonts.body, scr(hostRect), m.hostLabel, true, nav.focused("host"), m.hostBlocked)
	tbxDrawButton(renderer, config, fonts.body, scr(joinRect), m.joinLabel, false, nav.focused("join"), partyScreen.busy)
	// Host is listed first so a fresh visit starts on it.
	nav.add("host", hostRect, row, m.hostBlocked)
	nav.add("join", joinRect, row, partyScreen.busy)
	row++
	y += heroH + 22

	// Rooms other handhelds are hosting on this network.
	tbxDrawSection(renderer, config, "Nearby rooms", "radar", x, top+y, w)
	y += tbxSectionHeight
	rowH := tbxRowHeight(fonts)
	if len(m.nearby) == 0 {
		card := sdl.Rect{X: x, Y: y, W: w, H: 132}
		UICard(renderer, scr(card), DefaultUICardStyle())
		icon := "radar"
		if m.lan == "" {
			icon = "wifi-off"
		}
		tbxDrawEmpty(renderer, config, scr(card), icon, m.emptyTitle, m.emptyBody)
		y += card.H + 8
	}
	for _, r := range m.nearby {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		tbxDrawRow(renderer, config, fonts, scr(rect), r.spec, nav.focused(r.id))
		nav.add(r.id, rect, row, false)
		row++
		y += rowH + 8
	}

	// Rooms saved earlier, rejoined with one press.
	if len(m.friends) > 0 {
		y += 14
		tbxDrawSection(renderer, config, "Saved rooms", "users", x, top+y, w)
		y += tbxSectionHeight
		for _, f := range m.friends {
			rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
			tbxDrawRow(renderer, config, fonts, scr(rect), f.spec, nav.focused(f.id))
			nav.add(f.id, rect, row, f.spec.dim)
			row++
			y += rowH + 8
		}
	}

	y += 14
	hist := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	tbxDrawRow(renderer, config, fonts, scr(hist), tbxRowSpec{
		icon:     "trophy",
		title:    "Watch history",
		subtitle: "All-time leaderboard and every room you have watched in",
	}, nav.focused("history"))
	nav.add("history", hist, row, false)
	nav.extend(y + rowH + 8)
}

func partyRenderRoom(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, m *partyModel) {
	nav := &partyScreen.nav
	top := frame.bodyTop - nav.scroll
	scr := func(r sdl.Rect) sdl.Rect {
		r.Y += top
		return r
	}
	gap := int32(20)
	leftW := frame.w * 60 / 100
	x := frame.x
	rightX := x + leftW + gap
	rightW := frame.w - leftW - gap
	y := int32(0)
	row := 0

	// What the room is watching.
	npH := int32(156)
	np := sdl.Rect{X: x, Y: y, W: leftW, H: npH}
	partyDrawNowPlaying(renderer, config, fonts, scr(np), m, nav.focused("np"))
	nav.add("np", np, row, false)
	row++
	y += npH + 18

	// The shared queue.
	heading := "Up next"
	if len(m.queue) > 0 {
		heading = fmt.Sprintf("Up next · %d", len(m.queue))
	}
	tbxDrawSection(renderer, config, heading, "list", x, top+y, leftW)
	y += tbxSectionHeight
	rowH := tbxRowHeight(fonts)
	for _, q := range m.queue {
		rect := sdl.Rect{X: x, Y: y, W: leftW, H: rowH}
		tbxDrawRow(renderer, config, fonts, scr(rect), q.spec, nav.focused(q.id))
		nav.add(q.id, rect, row, false)
		row++
		y += rowH + 8
	}
	add := sdl.Rect{X: x, Y: y, W: leftW, H: rowH}
	tbxDrawRow(renderer, config, fonts, scr(add), tbxRowSpec{
		icon:     "plus",
		title:    "Add to the queue",
		subtitle: "What's playing, a copied link, a typed link or Media",
	}, nav.focused("add"))
	nav.add("add", add, row, false)
	row++
	y += rowH + 18

	// Leave (and, for a joiner, save the room for later).
	btnH := int32(48)
	leaveLabel := "Leave room"
	if m.hosting {
		leaveLabel = "Close room"
	}
	lw := tbxButtonWidth(fonts.body, leaveLabel, 160)
	leave := sdl.Rect{X: x, Y: y, W: lw, H: btnH}
	tbxDrawButton(renderer, config, fonts.body, scr(leave), leaveLabel, false, nav.focused("leave"), false)
	nav.add("leave", leave, row, false)
	if !m.hosting {
		sw := tbxButtonWidth(fonts.body, "Save room", 160)
		save := sdl.Rect{X: leave.X + leave.W + 12, Y: y, W: sw, H: btnH}
		tbxDrawButton(renderer, config, fonts.body, scr(save), "Save room", false, nav.focused("save"), false)
		nav.add("save", save, row, false)
	}
	nav.extend(y + btnH + 8)

	// Right column: the invite card, then who is here.
	ry := int32(0)
	qrSize := rightW - 48
	if qrSize > 176 {
		qrSize = 176
	}
	if qrSize < 96 {
		qrSize = 96
	}
	codeFont := getFontSized(config, "small", 34)
	if codeFont == nil {
		codeFont = fonts.title
	}
	codeH := int32(codeFont.Height())
	inviteH := 18 + fonts.tinyH + 12 + qrSize + 12 + codeH + 6 + fonts.smallH*2 + 22
	invite := sdl.Rect{X: rightX, Y: ry, W: rightW, H: inviteH}
	is := scr(invite)
	UIListRow(renderer, is, homeTileRadius, nav.focused("invite"), false)
	nav.add("invite", invite, 0, false)
	cy := is.Y + 18
	renderText(renderer, config, fonts.tiny, "INVITE FRIENDS", ColorTextTertiary(), is.X+20, cy)
	cy += fonts.tinyH + 12
	tile := sdl.Rect{X: is.X + (is.W-qrSize)/2, Y: cy, W: qrSize, H: qrSize}
	fillRoundedRect(renderer, tile.X, tile.Y, tile.W, tile.H, 12, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if modules := partyInviteQR(m.inviteURL); len(modules) > 0 {
		drawCompanionQR(renderer, insetRect(tile, 8), modules)
	}
	cy += qrSize + 12
	cw, _ := measureText(codeFont, m.snap.Code)
	renderText(renderer, config, codeFont, m.snap.Code, glassAccent(), is.X+(is.W-cw)/2, cy)
	cy += codeH + 6
	hint := "Listed under Nearby rooms on other handhelds"
	if !partyDiscoverable.Load() && m.hosting {
		hint = "Hidden from Nearby rooms — share the code"
	}
	partyDrawCentred(renderer, config, fonts, hint, is, cy)
	partyDrawCentred(renderer, config, fonts, "A: invite options", is, cy+fonts.smallH)
	ry += inviteH + 18

	// Who is in the room, with their watch time.
	lineH := fonts.bodyH + 10
	shown := len(m.members)
	if shown > 8 {
		shown = 8
	}
	membersH := 18 + fonts.tinyH + 10 + int32(shown)*lineH + 14
	members := sdl.Rect{X: rightX, Y: ry, W: rightW, H: membersH}
	ms := scr(members)
	UICard(renderer, ms, DefaultUICardStyle())
	my := ms.Y + 18
	renderText(renderer, config, fonts.tiny, m.membersLabel, ColorTextTertiary(), ms.X+20, my)
	my += fonts.tinyH + 10
	for i := 0; i < shown; i++ {
		mem := m.members[i]
		fillCircle(renderer, ms.X+26, my+fonts.bodyH/2, 4, ColorToastSuccess)
		ww, _ := measureText(fonts.small, mem.watch)
		renderText(renderer, config, fonts.small, mem.watch, ColorTextTertiary(), ms.X+ms.W-20-ww, my+(fonts.bodyH-fonts.smallH)/2)
		name := mem.name
		if mem.host {
			name += " · host"
		}
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, name, ms.W-80-ww), ColorTextPrimary(), ms.X+40, my)
		my += lineH
	}
	nav.extend(ry + membersH + 8)
}

// partyDrawCentred draws one caption line centred in card at y.
func partyDrawCentred(renderer *sdl.Renderer, config *Config, fonts tbxFonts, text string, card sdl.Rect, y int32) {
	text = ellipsize(fonts.small, text, card.W-24)
	w, _ := measureText(fonts.small, text)
	renderText(renderer, config, fonts.small, text, ColorTextMuted(), card.X+(card.W-w)/2, y)
}

// partyDrawNowPlaying draws the room's now-playing card.
func partyDrawNowPlaying(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, m *partyModel, focused bool) {
	UIListRow(renderer, r, homeTileRadius, focused, false)
	x := r.X + 22
	w := r.W - 44
	y := r.Y + 18
	renderText(renderer, config, fonts.tiny, "NOW PLAYING", ColorTextTertiary(), x, y)
	right := r.X + r.W - 20
	if m.npSync != "" {
		p := tbxDrawPill(renderer, config, fonts.tiny, right, y+fonts.tinyH/2, m.npSync, m.npSyncTone)
		right = p.X - 8
	}
	if m.npState != "" {
		tone := tbxToneNeutral
		if m.npState == "Playing" {
			tone = tbxToneGood
		}
		tbxDrawPill(renderer, config, fonts.tiny, right, y+fonts.tinyH/2, m.npState, tone)
	}
	y += fonts.tinyH + 14
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.npTitle, w), ColorTextPrimary(), x, y)
	y += fonts.titleH + 12
	if m.npProgress >= 0 {
		UIProgressBar(renderer, sdl.Rect{X: x, Y: y, W: w, H: 6}, m.npProgress, glassA(30), glassAccent())
		y += 14
	}
	if m.npTime != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.npTime, w), ColorTextSecondary(), x, y)
		y += fonts.smallH + 4
	}
	if m.npHint != "" && y+fonts.smallH <= r.Y+r.H-8 {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.npHint, w), ColorTextMuted(), x, y)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// partySceneAction handles one action on the screen (the sheet has already had
// its turn). Up at the top is left unhandled so the top bar can take it.
func partySceneAction(config *Config, act tbxAction) bool {
	nav := &partyScreen.nav
	switch act {
	case tbxActConfirm:
		partyActivate(config, nav.focus)
		return true
	case tbxActX:
		partyActivateX(nav.focus)
		return true
	case tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// partySceneClick handles a click on the screen's controls.
func partySceneClick(mx, my int32, config *Config) bool {
	it, ok := partyScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	partyScreen.nav.focus = it.id
	partyActivate(config, it.id)
	return true
}

// partySceneHints is the footer: the focused control's own action first.
func partySceneHints() []FooterHint {
	id := partyScreen.nav.focus
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	switch {
	case id == "host":
		hints = append(hints, FooterHint{Button: "A", Label: "Host"})
	case id == "join":
		if partyScreen.model.lan == "" {
			hints = append(hints, FooterHint{Button: "A", Label: "Wi-Fi"})
		} else {
			hints = append(hints, FooterHint{Button: "A", Label: "Join"})
		}
	case strings.HasPrefix(id, "near:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Join"})
	case strings.HasPrefix(id, "friend:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Rejoin"}, FooterHint{Button: "X", Label: "Forget"})
	case id == "np":
		if partyWatchingRemote(partyScreen.model.snap) {
			hints = append(hints, FooterHint{Button: "A", Label: "Play / Pause"})
		} else {
			hints = append(hints, FooterHint{Button: "A", Label: "Watch"})
		}
	case strings.HasPrefix(id, "q:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Play for everyone"}, FooterHint{Button: "X", Label: "Remove"})
	case id == "add":
		hints = append(hints, FooterHint{Button: "A", Label: "Add"})
	case id == "invite":
		hints = append(hints, FooterHint{Button: "A", Label: "Invite"})
	case id == "leave":
		hints = append(hints, FooterHint{Button: "A", Label: "Leave"})
	case id == "save", id == "history":
		hints = append(hints, FooterHint{Button: "A", Label: "Open"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// partyActivate runs the focused control's action.
func partyActivate(config *Config, id string) {
	switch {
	case id == "host":
		partyHostFlow()
	case id == "join":
		if partyLANAddress() == "" {
			PlayActivateSound()
			wifiScrOpen(config)
			return
		}
		if partyScreen.busy {
			return
		}
		PlayActivateSound()
		partyOpenJoinPrompt()
	case strings.HasPrefix(id, "near:"):
		key := strings.TrimPrefix(id, "near:")
		if i := strings.LastIndexByte(key, '|'); i > 0 {
			partyJoinFlow(key[:i] + " " + key[i+1:])
		}
	case strings.HasPrefix(id, "friend:"):
		partyJoinFlow(strings.TrimPrefix(id, "friend:"))
	case id == "history":
		PlayActivateSound()
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "Watch history",
			subtitle: "All-time · kept on this handheld",
			icon:     "trophy",
			body:     partyHistoryText(partyHistoryReport()),
			tall:     true,
			buttons:  []tbxSheetButton{{label: "Close"}},
		})
	case id == "np":
		partyNowPlayingPress()
	case strings.HasPrefix(id, "q:"):
		partyPlayQueued(strings.TrimPrefix(id, "q:"))
	case id == "add":
		PlayActivateSound()
		partyOpenAddSheet(config)
	case id == "invite":
		PlayActivateSound()
		partyOpenInviteSheet(config)
	case id == "leave":
		PlayActivateSound()
		partyConfirmLeave(config)
	case id == "save":
		snap := partyScreen.model.snap
		if err := partyFriendAdd(partyHostName(snap), partyInviteText()); err != nil {
			ShowToast("Could not save this room", ToastKindError)
			return
		}
		partyScreen.model.builtAt = time.Time{}
		ShowToast("Saved — rejoin it from Saved rooms", ToastKindSuccess)
	}
}

// partyActivateX is X: forget a saved room, or remove a queued video.
func partyActivateX(id string) {
	switch {
	case strings.HasPrefix(id, "friend:"):
		partyFriendRemove(strings.TrimPrefix(id, "friend:"))
		partyScreen.model.builtAt = time.Time{}
		PlayBackSound()
		ShowToast("Room forgotten", ToastKindInfo)
	case strings.HasPrefix(id, "q:"):
		qid, err := strconv.Atoi(strings.TrimPrefix(id, "q:"))
		if err != nil {
			return
		}
		PlayBackSound()
		go func() {
			err := partyRemoveItem(qid)
			tbxPost(func() {
				if err != nil {
					ShowToast("Could not remove it: "+plainNetworkError(err), ToastKindError)
					return
				}
				partyScreen.model.builtAt = time.Time{}
			})
		}()
	}
}

// ── Flows ────────────────────────────────────────────────────────────────

// partyHostFlow opens a room on this device.
func partyHostFlow() {
	if partyScreen.busy {
		return
	}
	if partyLANAddress() == "" {
		ShowToast("Connect to Wi-Fi to host a room", ToastKindWarning)
		return
	}
	if err := partyHost(partyDeviceName()); err != nil {
		ShowToast("Could not open the room: "+firstLine(err.Error()), ToastKindError)
		return
	}
	partyScreen.model.builtAt = time.Time{}
	partyScreen.nav.focus = "add"
	PlaySuccessSound()
	ShowToast("Room open — friends on this Wi-Fi can join", ToastKindSuccess)
}

// partyJoinFlow joins from an invite line or a bare room code. A bare code is
// looked up among the rooms this screen has heard, then with one more scan.
func partyJoinFlow(text string) {
	text = strings.TrimSpace(text)
	if text == "" || partyScreen.busy {
		return
	}
	if partyLANAddress() == "" {
		ShowToast("Connect to Wi-Fi to join a room", ToastKindWarning)
		return
	}
	partyScreen.busy = true
	partyScreen.busyLabel = "Joining…"
	partyScreen.model.builtAt = time.Time{}
	PlayActivateSound()
	name := partyDeviceName()
	go func() {
		invite := text
		if partyCodeValid(text) {
			code := strings.ToUpper(text)
			invite = ""
			if room, ok := partyFindNearbyCode(code); ok {
				invite = room.Invite()
			} else if rooms, _ := partyDiscoverOnce(context.Background(), partyDiscoveryWait); len(rooms) > 0 {
				for _, r := range rooms {
					if r.Code == code {
						invite = r.Invite()
						break
					}
				}
			}
			if invite == "" {
				tbxPost(func() {
					partyScreen.busy = false
					partyScreen.model.builtAt = time.Time{}
					ShowToast("No room "+code+" on this Wi-Fi — check the code, or type the host address too", ToastKindWarning)
				})
				return
			}
		}
		err := partyJoin(invite, name)
		tbxPost(func() {
			partyScreen.busy = false
			partyScreen.model.builtAt = time.Time{}
			if err != nil {
				ShowToast(partyJoinError(err), ToastKindError)
				return
			}
			partyScreen.nav.focus = "np"
			PlaySuccessSound()
			ShowToast("You are in — the room's video starts on its own", ToastKindSuccess)
		})
	}()
}

// partyJoinError turns a failed join into a sentence.
func partyJoinError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"), strings.Contains(msg, "no route"), strings.Contains(msg, "timeout"),
		strings.Contains(msg, "deadline"):
		return "That room did not answer — it may have closed, or the address is wrong"
	case strings.Contains(msg, "no such party code"):
		return "The host has a different room code now"
	case strings.Contains(msg, "full"):
		return "That room is full"
	}
	return "Could not join: " + plainNetworkError(err)
}

// partyQueueFlow adds a link to the shared queue.
func partyQueueFlow(link, title string) {
	link = strings.TrimSpace(link)
	if !looksLikeStreamURL(link) {
		ShowToast("That is not a video link — use an http(s) or stream address", ToastKindWarning)
		return
	}
	go func() {
		err := partyQueueURL(link, title)
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not add it: "+plainNetworkError(err), ToastKindError)
				return
			}
			partyScreen.model.builtAt = time.Time{}
			PlaySuccessSound()
			ShowToast("Added to the queue", ToastKindSuccess)
		})
	}()
}

// partyPlayQueued starts a queued video for everyone.
func partyPlayQueued(idText string) {
	qid, err := strconv.Atoi(idText)
	if err != nil {
		return
	}
	index := -1
	for i, item := range partyScreen.model.snap.Queue {
		if item.ID == qid {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	PlayActivateSound()
	partyResumeRemote()
	go func() {
		err := partySelectItem(index)
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not start it: "+plainNetworkError(err), ToastKindError)
				return
			}
			partyScreen.model.builtAt = time.Time{}
			ShowToast("Starting for everyone", ToastKindSuccess)
		})
	}()
}

// partyNowPlayingPress is A on the now-playing card: join the room's video if
// this device is not showing it, or pause/resume it for everyone.
func partyNowPlayingPress() {
	snap := partyScreen.model.snap
	if snap.Playback.URL == "" {
		ShowToast("Nothing is playing yet — add a video to the queue", ToastKindInfo)
		return
	}
	PlayActivateSound()
	if partyWatchingRemote(snap) {
		// The player publishes its own play/pause to the room.
		ToggleVideoPlayback()
		return
	}
	partyResumeRemote()
	ShowToast("Joining the video at the room's position", ToastKindInfo)
}

// partyOpenAddSheet offers the ways to add a video.
func partyOpenAddSheet(config *Config) {
	buttons := make([]tbxSheetButton, 0, 4)
	playing := playbackPageURL()
	if looksLikeStreamURL(playing) && snapshotVideoState().phase != "idle" {
		buttons = append(buttons, tbxSheetButton{label: "What's playing", primary: true, run: func() {
			partyQueueFlow(playing, "")
		}})
	}
	buttons = append(buttons,
		tbxSheetButton{label: "Paste link", run: func() {
			text, err := pasteTextFromClipboard()
			text = strings.TrimSpace(text)
			if err != nil || !looksLikeStreamURL(text) {
				ShowToast("The clipboard has no video link — copy one first", ToastKindWarning)
				return
			}
			partyQueueFlow(text, "")
		}},
		tbxSheetButton{label: "Type link", run: partyOpenLinkPrompt},
		tbxSheetButton{label: "Browse Media", run: func() {
			idx := sceneIndexExact(config, "Tube")
			if idx < 0 {
				ShowToast("This build has no Media screen", ToastKindInfo)
				return
			}
			changeSceneTo(config, idx)
			ShowToast("Play something — it is shared with the room", ToastKindInfo)
		}},
	)
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Add to the queue",
		subtitle: "Everyone in the room can play it",
		icon:     "plus",
		body: "Add the video that is playing now, a link you copied, or type one.\n\n" +
			"Anything you start from Media while you are in the room is shared with everyone too.",
		buttons: buttons,
	})
}

// partyOpenInviteSheet shows the large invite: QR code, room code and address.
func partyOpenInviteSheet(config *Config) {
	snap := partyScreen.model.snap
	m := &partyScreen.model
	body := "## Room code\n" + snap.Code + "\n\n" +
		"On another handheld on this Wi-Fi, open Tools → Watch Party: this room is listed under Nearby rooms. " +
		"Or choose Join with a code and enter " + snap.Code + ".\n\n" +
		"A phone that scans the code gets the same instructions."
	if snap.HostAddr != "" {
		body += "\n\n## Host address\n" + snap.HostAddr + " " + snap.Code
	}
	buttons := []tbxSheetButton{{label: "Copy invite", run: func() {
		invite := partyInviteText()
		if invite == "" {
			ShowToast("No invite yet", ToastKindInfo)
			return
		}
		if err := copyTextToClipboard(invite, "Watch Party"); err != nil {
			ShowToast("Could not copy the invite", ToastKindError)
			return
		}
		ShowToast("Copied "+invite, ToastKindSuccess)
	}}}
	if m.hosting {
		label := "Hide from nearby"
		if !partyDiscoverable.Load() {
			label = "List nearby"
		}
		buttons = append(buttons, tbxSheetButton{label: label, run: func() {
			on := !partyDiscoverable.Load()
			partyDiscoverable.Store(on)
			if on {
				ShowToast("Listed under Nearby rooms again", ToastKindInfo)
			} else {
				ShowToast("Hidden from Nearby rooms — the code still works", ToastKindInfo)
			}
		}})
	}
	buttons = append(buttons, tbxSheetButton{label: "Done", primary: true})
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Invite friends",
		subtitle: "Room " + snap.Code,
		icon:     "qr",
		body:     body,
		qr:       partyInviteQR(m.inviteURL),
		qrLabel:  "Tap it or press Y to enlarge",
		buttons:  buttons,
		focus:    len(buttons) - 1,
	})
}

// partyConfirmLeave asks before a host closes the room for everyone.
func partyConfirmLeave(config *Config) {
	m := &partyScreen.model
	body := "You leave the room; it stays open for the others, and you can rejoin it from Nearby rooms."
	label := "Leave room"
	if m.hosting {
		label = "Close room"
		others := len(m.snap.Members) - 1
		switch {
		case others <= 0:
			body = "Nobody else is in the room. Closing it stops the room on this handheld."
		case others == 1:
			body = "Closing the room ends the party for the 1 other person watching."
		default:
			body = fmt.Sprintf("Closing the room ends the party for the %d other people watching.", others)
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    label + "?",
		subtitle: "Room " + m.snap.Code,
		icon:     "party",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Stay"},
			{label: label, primary: true, run: func() {
				partyLeaveSession()
				partyNearbyReset()
				partyScreen.model.builtAt = time.Time{}
				partyScreen.nav.focus = ""
				ShowToast("You left the room", ToastKindInfo)
			}},
		},
	})
}
