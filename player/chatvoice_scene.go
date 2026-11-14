package main

// chatvoice_scene.go — the Voice Channel page reached from Chat's People list
// and from a voice invitation. The call keeps running when this page closes.

import (
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
	return tbxDispatch(c, tbxKeyAction(e), chatVoiceSceneAction)
}
func (chatVoiceScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), chatVoiceSceneAction)
}
func init() { registerScene(chatVoiceScene{sceneBase{name: chatVoiceSceneName}}) }

func chatVoiceOpenScene(config *Config) { tbxOpenScene(config, chatVoiceSceneName) }

func chatVoiceRenderScene(renderer *sdl.Renderer, config *Config) {
	snap := chatVoiceSnapshotNow()
	status := snap.Status
	color := ColorTextSecondary()
	if status == "" {
		status = "Ready"
	}
	if snap.Connected {
		color = ColorToastSuccess
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader("Voice Channel", "Talk with people from Chat", "mic", status, color))
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
	if snap.Invite == "" {
		drawRow("start", tbxRowSpec{icon: "mic", title: "Start Voice Channel", subtitle: "Post an invitation in community Chat", pill: "Start", tone: tbxToneAccent})
		drawRow("join", tbxRowSpec{icon: "users", title: "Join a Voice Channel", subtitle: "Paste an invitation shared in Chat", pill: "Join", tone: tbxToneNeutral})
	} else {
		label, detail := "Mute microphone", "Others can hear you until you mute"
		if snap.Muted {
			label, detail = "Unmute microphone", "You can listen while muted"
		}
		drawRow("mute", tbxRowSpec{icon: "mic", title: label, subtitle: detail, pill: "Toggle", tone: tbxToneAccent})
		drawRow("invite", tbxRowSpec{icon: "users", title: "Invite from Chat", subtitle: "Anyone in community Chat can use the invitation", pill: "Share", tone: tbxToneNeutral})
		drawRow("copy", tbxRowSpec{icon: "copy", title: "Copy invitation", subtitle: "Share it with another JukaHub user", pill: "Copy", tone: tbxToneNeutral})
		drawRow("leave", tbxRowSpec{icon: "close", title: "Leave Voice Channel", subtitle: "Stop speaking and listening", pill: "Leave", tone: tbxToneWarn})
	}
	relay := chatVoiceRelayConfigured(config)
	detail := "Set the Internet voice service address"
	if relay != "" {
		detail = relay
	}
	drawRow("relay", tbxRowSpec{icon: "network", title: "Voice relay", subtitle: detail, pill: "Edit", tone: tbxToneNeutral})
	y += 12
	tbxDrawSection(renderer, config, "In this channel", "users", x, body.Y+y-nav.scroll, w)
	y += tbxSectionHeight
	if len(snap.People) == 0 {
		caption := "Start or join a channel to see who's here."
		if snap.Invite != "" {
			caption = "Waiting for people to join…"
		}
		renderText(renderer, config, fonts.body, caption, ColorTextMuted(), x+16, body.Y+y-nav.scroll+12)
		y += fonts.bodyH + 30
	} else {
		for _, p := range snap.People {
			name := p.Name
			if p.Slot == snap.OwnSlot {
				name += " · you"
			}
			r := sdl.Rect{X: x, Y: body.Y + y - nav.scroll, W: w, H: 46}
			UICard(renderer, r, DefaultUICardStyle())
			renderText(renderer, config, fonts.body, ellipsize(fonts.body, name, w-40), ColorTextPrimary(), r.X+20, r.Y+(r.H-fonts.bodyH)/2)
			y += 52
		}
	}
	nav.extend(y + 12)
	nav.end()
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

func chatVoiceSceneAction(config *Config, act tbxAction) bool {
	if act == tbxActConfirm {
		chatVoiceSceneActivate(config, chatVoiceScreen.nav.focus)
		return true
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
	case "start":
		chatVoiceStartFor(config, nil)
	case "join":
		tbxOpenPrompt("Voice Channel invitation", "wss://voice.example.com/voice/…", "", func(value string) {
			invite, ok := chatVoiceInviteURL(strings.TrimSpace(value))
			if !ok {
				invite, ok = chatVoiceInviteFromMessage(value)
			}
			if !ok || !chatVoiceJoin(invite) {
				showToast("That is not a JukaHub Voice Channel invitation", ToastWarn())
			}
		})
	case "mute":
		chatVoiceToggleMute()
	case "invite":
		if invite := chatVoiceSnapshotNow().Invite; invite != "" {
			postChatMessageFiles(chatDisplayName(), chatVoiceInviteLabel(nil)+"\n"+chatVoiceInviteLine+invite, nil, true)
			showToast("Voice invitation posted in Chat", ToastInfo())
		}
	case "copy":
		if invite := chatVoiceSnapshotNow().Invite; invite != "" {
			if err := copyTextToClipboard(invite, "Voice Channel"); err != nil {
				showToast("Could not copy the invitation", ToastWarn())
			} else {
				showToast("Voice invitation copied", ToastInfo())
			}
		}
	case "leave":
		chatVoiceLeave()
		showToast("Left Voice Channel", ToastInfo())
	case "relay":
		chatVoicePromptRelay(config, nil)
	}
}

func chatVoiceSceneHints() []FooterHint {
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Back"}}
}
