package main

// chatvoice_scene.go — the Voice page: Chat's Voice tab, and where a Voice
// Channel invitation or the People list lands. One press joins the
// community's room on the free voice server (chatvoice_mumble.go); the call
// keeps running when this page closes. Opened from Chat, L1/R1 (Left/Right,
// Tab, 1–3) step back to Chat's conversations like the other tabs.

import (
	"net"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const chatVoiceSceneName = "VoiceChannel"

var chatVoiceScreen struct{ nav tbxNav }

type chatVoiceScene struct{ sceneBase }

func (chatVoiceScene) render(r *sdl.Renderer, c *Config) bool {
	chatVoiceRenderScene(r, c)
	return true
}
func (chatVoiceScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	// Chat's tab keys keep working on its Voice tab.
	if e != nil && e.Type == sdl.KEYDOWN && !tbxSheetVisible(c) && chatVoiceAsTab(c) {
		switch e.Keysym.Sym {
		case sdl.K_TAB:
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				chatVoiceBackToChat(c, chatTabCount-1)
			} else {
				chatVoiceBackToChat(c, chatTabCommunity)
			}
			return true
		case sdl.K_1, sdl.K_2, sdl.K_3:
			chatVoiceBackToChat(c, chatTab(e.Keysym.Sym-sdl.K_1))
			return true
		}
	}
	return tbxDispatch(c, tbxKeyAction(e), chatVoiceSceneAction)
}
func (chatVoiceScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), chatVoiceSceneAction)
}
func init() { registerScene(chatVoiceScene{sceneBase{name: chatVoiceSceneName}}) }

func chatVoiceOpenScene(config *Config) { tbxOpenScene(config, chatVoiceSceneName) }

// chatVoiceAsTab reports whether the page sits on top of Chat (B would go
// back there), so the tab keys step back into Chat's conversations.
func chatVoiceAsTab(config *Config) bool {
	if config == nil {
		config = appConfig
	}
	return config != nil && len(sceneHistory) > 0 &&
		sceneHistory[len(sceneHistory)-1] == sceneIndexExact(config, chatSceneName)
}

// chatVoiceTabOpen opens the Voice page as Chat's Voice tab. join is a click
// or tap on the tab: when no call is running it also joins the community
// room, so one click connects (a running call is only shown, never left).
// Stepping onto the tab with L1/R1 only opens the page, with Join focused —
// passing by never turns the microphone on.
func chatVoiceTabOpen(config *Config, join bool) bool {
	if transitionPhase != "none" {
		return false // a scene change is under way; the press would be lost
	}
	if chatVoiceRecording() {
		showToast("Attach or discard the voice clip before switching chats", ToastInfo())
		return false
	}
	if config == nil {
		config = appConfig
	}
	snap := chatVoiceSnapshotNow()
	if join && (snap.Invite == "" || (snap.Ended && snap.Community)) {
		chatVoiceJoinCommunity(config)
		chatVoiceScreen.nav.focus = "phone"
	} else if snap.Invite == "" {
		chatVoiceScreen.nav.focus = "room"
	}
	return tbxOpenScene(config, chatVoiceSceneName)
}

// chatVoiceBackToChat leaves the Voice tab for one of Chat's conversations.
func chatVoiceBackToChat(config *Config, tab chatTab) {
	if transitionPhase != "none" {
		return
	}
	goBackScene(config)
	chatSelectTab(tab)
}

func chatVoiceServerHost(server string) string {
	if host, _, err := net.SplitHostPort(server); err == nil {
		return host
	}
	return server
}

func chatVoiceRenderScene(renderer *sdl.Renderer, config *Config) {
	snap := chatVoiceSnapshotNow()
	community := snap.Community
	status := snap.Status
	color := ColorTextSecondary()
	switch {
	case status == "":
		status = "Not in a room"
	case snap.Ended:
		status = "Stopped" // the sentence is in the Try again row
	}
	if snap.Connected {
		color = ColorToastSuccess
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	server := chatVoiceServerConfigured(config)
	room := chatVoiceRoomConfigured(config)
	frame := tbxBeginFrame(renderer, config, tbxHeader("Voice", "Free voice room — nothing to sign up for", "mic", status, color))
	body := frame.bodyRect()
	nav := &chatVoiceScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	x, w := frame.x, frame.w
	y := int32(0)
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		r := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := r
		screen.Y += body.Y - nav.scroll
		tbxDrawRow(renderer, config, fonts, screen, spec, nav.focused(id))
		nav.add(id, r, len(nav.items), false)
		y += rowH + 10
	}
	section := func(title, icon string) {
		y += 12
		tbxDrawSection(renderer, config, title, icon, x, body.Y+y-nav.scroll, w)
		y += tbxSectionHeight
	}
	if snap.Invite == "" {
		drawRow("room", tbxRowSpec{icon: "mic", title: "Join the voice room",
			subtitle: "One room for everyone in JukaHub Chat · " + chatVoiceServerHost(server), pill: "Join", tone: tbxToneAccent})
		drawRow("start", tbxRowSpec{icon: "users", title: "Start a private Voice Channel", subtitle: "Post an invitation in community Chat", pill: "Start", tone: tbxToneNeutral})
		drawRow("join", tbxRowSpec{icon: "users", title: "Join with an invitation", subtitle: "Paste an invitation shared in Chat", pill: "Join", tone: tbxToneNeutral})
	} else {
		label, detail := "Mute microphone", "The room hears you while you speak"
		if snap.Muted {
			label, detail = "Unmute microphone", "You can listen while muted"
		}
		if snap.Ended {
			drawRow("retry", tbxRowSpec{icon: "mic", title: "Try again", subtitle: snap.Status, pill: "Retry", tone: tbxToneAccent})
		} else {
			drawRow("mute", tbxRowSpec{icon: "mic", title: label, subtitle: detail, pill: "Toggle", tone: tbxToneAccent})
		}
		if community {
			drawRow("phone", tbxRowSpec{icon: "copy", title: "Join from a phone or PC",
				subtitle: "Any Mumble app · " + server + " · " + room, pill: "Copy", tone: tbxToneNeutral})
		} else {
			drawRow("invite", tbxRowSpec{icon: "users", title: "Invite from Chat", subtitle: "Anyone in community Chat can use the invitation", pill: "Share", tone: tbxToneNeutral})
			drawRow("copy", tbxRowSpec{icon: "copy", title: "Copy invitation", subtitle: "Works in JukaHub and in Mumble apps", pill: "Copy", tone: tbxToneNeutral})
		}
		leave := "Leave Voice Channel"
		if community {
			leave = "Leave the voice room"
		}
		drawRow("leave", tbxRowSpec{icon: "close", title: leave, subtitle: "Stop speaking and listening", pill: "Leave", tone: tbxToneWarn})
	}

	section("In this room", "users")
	if len(snap.People) == 0 {
		caption := "Join the room to see who's here."
		switch {
		case snap.Ended:
			caption = "Not connected."
		case snap.Invite != "" && !snap.Connected:
			caption = "Connecting…"
		case snap.Invite != "":
			caption = "Just you so far."
		}
		renderText(renderer, config, fonts.body, caption, ColorTextMuted(), x+16, body.Y+y-nav.scroll+12)
		y += fonts.bodyH + 30
	} else {
		for _, p := range snap.People {
			name := p.Name
			if p.Slot != 0 && p.Slot == snap.OwnSlot {
				name += " · you"
			}
			if p.Muted {
				name += " · muted"
			}
			r := sdl.Rect{X: x, Y: body.Y + y - nav.scroll, W: w, H: 46}
			UICard(renderer, r, DefaultUICardStyle())
			dot := int32(10)
			if p.Talking {
				chatFillDisc(renderer, r.X+18, r.Y+(r.H-dot)/2, dot, ColorToastSuccess)
			}
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, name, w-60), ColorTextPrimary(), r.X+38, r.Y+(r.H-fonts.bodyH)/2)
			y += 52
		}
	}

	section("Voice settings", "network")
	serverDetail := server
	if server == chatVoiceDefaultServer {
		serverDetail += " · free public Mumble server"
	}
	drawRow("server", tbxRowSpec{icon: "network", title: "Voice server", subtitle: serverDetail, pill: "Edit", tone: tbxToneNeutral})
	drawRow("roomcfg", tbxRowSpec{icon: "users", title: "Voice room", subtitle: room, pill: "Edit", tone: tbxToneNeutral})
	relay := chatVoiceRelayConfigured(config)
	relayDetail := "Not set — private channels use the voice server"
	if relay != "" {
		relayDetail = relay
	}
	drawRow("relay", tbxRowSpec{icon: "network", title: "Own voice relay (optional)", subtitle: relayDetail, pill: "Edit", tone: tbxToneNeutral})
	nav.extend(y + 12)
	nav.end()
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func chatVoiceSceneAction(config *Config, act tbxAction) bool {
	switch act {
	case tbxActConfirm:
		chatVoiceSceneActivate(config, chatVoiceScreen.nav.focus)
		return true
	case tbxActPageUp, tbxActLeft:
		if chatVoiceAsTab(config) {
			chatVoiceBackToChat(config, chatTabCount-1)
			return true
		}
	case tbxActPageDown, tbxActRight:
		if chatVoiceAsTab(config) {
			chatVoiceBackToChat(config, chatTabCommunity)
			return true
		}
	case tbxActY:
		// Y mutes from anywhere on the page.
		if snap := chatVoiceSnapshotNow(); snap.Invite != "" && !snap.Ended {
			chatVoiceToggleMute()
			return true
		}
	}
	return tbxNavAction(&chatVoiceScreen.nav, act)
}

func chatVoiceSceneClick(mx, my int32, config *Config) bool {
	it, ok := chatVoiceScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	chatVoiceScreen.nav.focus = it.id
	chatVoiceSceneActivate(config, it.id)
	return true
}

func chatVoiceSceneActivate(config *Config, id string) {
	switch id {
	case "room":
		chatVoiceJoinCommunity(config)
		chatVoiceScreen.nav.focus = "phone" // a second A must not mute
	case "start":
		chatVoiceStartFor(config, nil)
	case "join":
		tbxOpenPrompt("Voice Channel invitation", "mumble://… or wss://…", "", func(value string) {
			invite, ok := chatVoiceInviteURL(strings.TrimSpace(value))
			if !ok {
				invite, ok = chatVoiceInviteFromMessage(value)
			}
			if !ok {
				showToast("That is not a JukaHub Voice Channel invitation", ToastWarn())
				return
			}
			chatVoiceJoin(invite)
		})
	case "retry":
		chatVoiceRetry(config)
	case "mute":
		chatVoiceToggleMute()
	case "invite":
		if invite := chatVoiceSnapshotNow().Invite; invite != "" {
			postChatMessageFiles(chatDisplayName(), chatVoiceInviteLabel(nil)+"\n"+chatVoiceInviteLine+invite, nil, true)
			showToast("Voice invitation posted in Chat", ToastInfo())
		}
	case "phone":
		chatVoicePhoneSheet(config)
	case "copy":
		if invite := chatVoiceSnapshotNow().Invite; invite != "" {
			if err := copyTextToClipboard(invite, "Voice Channel"); err != nil {
				showToast("Could not copy the invitation", ToastWarn())
			} else {
				showToast("Voice invitation copied", ToastInfo())
			}
		}
	case "leave":
		community := chatVoiceSnapshotNow().Community
		chatVoiceLeave()
		chatVoiceScreen.nav.focus = "room"
		if community {
			showToast("Left the voice room", ToastInfo())
		} else {
			showToast("Left Voice Channel", ToastInfo())
		}
	case "server":
		tbxOpenPrompt("Voice server (empty = free default)", chatVoiceDefaultServer, chatVoiceServerConfigured(config), func(value string) {
			if err := chatVoiceSetServer(config, value); err != nil {
				showToast(err.Error(), ToastWarn())
				return
			}
			showToast("Voice server saved — used the next time you join", ToastInfo())
		})
	case "roomcfg":
		tbxOpenPrompt("Voice room (empty = default)", chatVoiceDefaultRoom, chatVoiceRoomConfigured(config), func(value string) {
			if err := chatVoiceSetRoom(config, value); err != nil {
				showToast(err.Error(), ToastWarn())
				return
			}
			showToast("Voice room saved — used the next time you join", ToastInfo())
		})
	case "relay":
		chatVoicePromptRelay(config, nil)
	}
}

// chatVoicePhoneSheet shows the room's address as a QR code: a handheld has
// no use for its own clipboard, a phone's camera does.
func chatVoicePhoneSheet(config *Config) {
	snap := chatVoiceSnapshotNow()
	if snap.Invite == "" {
		return
	}
	server, room, ok := chatVoiceMumbleInvite(snap.Invite)
	if !ok {
		return
	}
	host, port, _ := net.SplitHostPort(server)
	invite := snap.Invite
	body := "Install Mumla (Android) or Mumble (iPhone, Windows, Mac, Linux), then scan the code " +
		"or open this address:\n\n" + invite + "\n\nOr add the server by hand: " + host + ", port " + port +
		", any name, then open " + strings.ReplaceAll(room, "/", " → ") + "."
	tbxOpenSheet(config, tbxSheetSpec{title: "Join from a phone or PC", icon: "mic", body: body,
		qr: tbxQRModules(invite), qrLabel: "Scan with a phone",
		buttons: []tbxSheetButton{
			{label: "Close", primary: true, run: func() {}},
			{label: "Copy address", run: func() {
				if err := copyTextToClipboard(invite, "Voice"); err != nil {
					showToast("Could not copy the address", ToastWarn())
				} else {
					showToast("Room address copied", ToastInfo())
				}
			}},
		}})
}

func chatVoiceSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Back"}}
	if snap := chatVoiceSnapshotNow(); snap.Invite != "" && !snap.Ended {
		hints = append(hints, FooterHint{Button: "Y", Label: "Mute"})
	}
	if chatVoiceAsTab(nil) {
		hints = append(hints, FooterHint{Button: "L1/R1", Label: "Tabs"})
	}
	return hints
}

// chatDrawVoiceTab draws the Voice tab after Chat's conversation tabs and
// returns its width. A green outline and the number of people show a live
// call. Its hit box is chatHeaderVoiceRect (chatinput.go).
func chatDrawVoiceTab(renderer *sdl.Renderer, config *Config, fonts chatFonts, x, y, h int32) int32 {
	pad, icon := chatPx(14), chatPx(20)
	label := "Voice"
	snap := chatVoiceSnapshotNow()
	badge := ""
	if snap.Connected && len(snap.People) > 0 {
		badge = strconv.Itoa(len(snap.People))
	}
	lw, _ := measureText(fonts.tab, label)
	badgeW := int32(0)
	if badge != "" {
		bw, _ := measureText(fonts.meta, badge)
		badgeW = max32(bw+chatPx(12), int32(fonts.meta.Height())+chatPx(4))
	}
	w := pad + icon + chatPx(8) + lw + pad
	if badgeW > 0 {
		w += badgeW + chatPx(8)
	}
	r := sdl.Rect{X: x, Y: y, W: w, H: h}
	chatHeaderVoiceRect = r
	iconRect := sdl.Rect{X: r.X + pad, Y: r.Y + (h-icon)/2, W: icon, H: icon}
	labelX := iconRect.X + icon + chatPx(8)
	if chatHover(r) {
		chatFillRound(renderer, r, h/2, glassA(14))
	}
	if snap.Connected {
		chatStrokeRound(renderer, r, h/2, WithAlpha(SuccessInk(), 170))
	}
	DrawHomeIcon(renderer, "mic", iconRect, false)
	chatTextCentered(renderer, config, fonts.tab, label, ColorTextSecondary(), labelX, r)
	if badgeW > 0 {
		bh := int32(fonts.meta.Height()) + chatPx(4)
		b := sdl.Rect{X: labelX + lw + chatPx(8), Y: r.Y + (h-bh)/2, W: badgeW, H: bh}
		ink := SuccessInk()
		chatFillRound(renderer, b, bh/2, ink)
		bw, _ := measureText(fonts.meta, badge)
		chatTextCentered(renderer, config, fonts.meta, badge, contrastText(ink), b.X+(b.W-bw)/2, b)
	}
	return w
}
