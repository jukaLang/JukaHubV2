package main

import (
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Social Notifications — simulated social media notifications
// Appears as animated toasts from different "social channels"
// ──────────────────────────────────────────────────────────────────────

type socialChannel struct {
	name  string
	icon  string
	color sdl.Color
}

var socialChannels = []socialChannel{
	{"Messages", "MSG", sdl.Color{R: 80, G: 200, B: 255, A: 255}},
	{"Twitter", "TWT", sdl.Color{R: 29, G: 161, B: 242, A: 255}},
	{"Instagram", "INS", sdl.Color{R: 193, G: 53, B: 132, A: 255}},
	{"YouTube", "YTB", sdl.Color{R: 255, G: 0, B: 0, A: 255}},
	{"Reddit", "RED", sdl.Color{R: 255, G: 69, B: 0, A: 255}},
	{"GitHub", "GIT", sdl.Color{R: 150, G: 150, B: 170, A: 255}},
}

type socialNotif struct {
	channel   socialChannel
	title     string
	body      string
	timestamp time.Time
}

var socialNotifHistory []socialNotif
var socialNotifOpen bool
var socialNotifCursor int

var socialMessages = []string{
	"You have 3 new messages",
	"New follower: @jukahub_fan",
	"Your video got 100 views!",
	"New comment on your post",
	"@friend mentioned you",
	"New pull request opened",
	"Your code review was approved",
	"You were assigned an issue",
	"New subscriber!",
	"Your post got 50 likes",
	"Achievement unlocked! [!]",
	"Weekly stats are ready",
	"New direct message",
	"Someone shared your post",
	"Your comment got upvoted",
	"New collaboration request",
	"Package published successfully",
	"Server health check: all green",
	"Storage 80% full — clean up recommended",
	"Background sync complete",
}

// socialNotifTimer drives periodic social notifications.
var socialNotifTimer *time.Timer

func startSocialNotifs() {
	scheduleSocialNotif()
}

func scheduleSocialNotif() {
	delay := time.Duration(45+rand.Intn(90)) * time.Second // 45-135 seconds
	socialNotifTimer = time.AfterFunc(delay, func() {
		fireSocialNotif()
		scheduleSocialNotif()
	})
}

func fireSocialNotif() {
	ch := socialChannels[rand.Intn(len(socialChannels))]
	msg := socialMessages[rand.Intn(len(socialMessages))]
	notif := socialNotif{
		channel:   ch,
		title:     ch.name,
		body:      msg,
		timestamp: time.Now(),
	}
	socialNotifHistory = append([]socialNotif{notif}, socialNotifHistory...)
	if len(socialNotifHistory) > 50 {
		socialNotifHistory = socialNotifHistory[:50]
	}
	// Push as notification
	notifPush(ch.icon+" "+ch.name+": "+msg, notifInfo)
}

// ──────────────────────────────────────────────────────────────────────
// Social notifications panel (accessible from taskbar bell or notification center)
// ──────────────────────────────────────────────────────────────────────

func renderSocialNotifPanel(renderer *sdl.Renderer, config *Config) {
	if !socialNotifOpen {
		return
	}

	// Dim
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Card
	cardW := int32(600)
	cardH := int32(500)
	cx := (screenWidth - cardW) / 2
	cy := (screenHeight - cardH) / 2
	fillRoundedRect(renderer, cx, cy, cardW, cardH, 16, ColorPanel)
	strokeRoundedRect(renderer, cx, cy, cardW, cardH, 16, 1, ColorBorder)

	// Title
	titleFont, _ := getCachedFont(config, "large")
	bodyFont, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")

	if titleFont != nil {
		title := "[Social] Notifications"
		tw, _, _ := titleFont.SizeUTF8(title)
		drawText(renderer, titleFont, title, cx+(cardW-int32(tw))/2, cy+16, getAccentColor(config), textAlignLeft)
	}

	// Channel filter tabs
	if bodyFont != nil {
		tabX := cx + 16
		for _, ch := range socialChannels {
			tw, _, _ := bodyFont.SizeUTF8(ch.icon)
			drawText(renderer, bodyFont, ch.icon, tabX, cy+56, ch.color, textAlignLeft)
			tabX += int32(tw) + 16
		}
	}

	// Notification list
	listY := cy + 88
	listH := cardH - 120
	itemH := int32(56)
	maxVisible := int(listH / itemH)

	if len(socialNotifHistory) == 0 {
		if bodyFont != nil {
			empty := "No notifications yet"
			ew, _, _ := bodyFont.SizeUTF8(empty)
			drawText(renderer, bodyFont, empty, cx+(cardW-int32(ew))/2, listY+60, ColorTextTertiary(), textAlignLeft)
		}
	}

	for i := 0; i < len(socialNotifHistory) && i < maxVisible; i++ {
		notif := socialNotifHistory[i]
		y := listY + int32(i)*itemH

		// Channel color bar
		renderer.SetDrawColor(notif.channel.color.R, notif.channel.color.G, notif.channel.color.B, 255)
		renderer.FillRect(&sdl.Rect{X: cx + 16, Y: y + 4, W: 3, H: itemH - 8})

		// Icon
		if bodyFont != nil {
			drawText(renderer, bodyFont, notif.channel.icon, cx+28, y+6, notif.channel.color, textAlignLeft)
		}

		// Title + body
		if bodyFont != nil {
			drawText(renderer, bodyFont, notif.channel.name, cx+56, y+6, ColorTextPrimary(), textAlignLeft)
		}
		if smallFont != nil {
			body := notif.body
			if len(body) > 55 {
				body = body[:52] + "..."
			}
			drawText(renderer, smallFont, body, cx+56, y+28, ColorTextSecondary(), textAlignLeft)
		}

		// Time
		if smallFont != nil {
			ts := timeSince(notif.timestamp)
			tw, _, _ := smallFont.SizeUTF8(ts)
			drawText(renderer, smallFont, ts, cx+cardW-int32(tw)-16, y+6, ColorTextTertiary(), textAlignLeft)
		}
	}

	// Footer
	if smallFont != nil {
		footer := "↑↓ navigate  •  Esc close  •  X clear all"
		fw, _, _ := smallFont.SizeUTF8(footer)
		drawText(renderer, smallFont, footer, cx+(cardW-int32(fw))/2, cy+cardH-28, ColorTextTertiary(), textAlignLeft)
	}
}

func handleSocialNotifInput(event *sdl.KeyboardEvent) bool {
	if !socialNotifOpen {
		return false
	}
	switch event.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		socialNotifOpen = false
		PlayBackSound()
	case sdl.K_UP:
		if socialNotifCursor > 0 {
			socialNotifCursor--
		}
	case sdl.K_DOWN:
		if socialNotifCursor < len(socialNotifHistory)-1 {
			socialNotifCursor++
		}
	case sdl.K_x:
		socialNotifHistory = nil
		socialNotifCursor = 0
		PlayActivateSound()
	}
	return true
}
