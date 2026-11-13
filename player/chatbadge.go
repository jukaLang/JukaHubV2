package main

// The Home screen's unread-chat badge: a red count on the Chat tile's corner
// and "N new messages" under its name, so a message that arrives while the
// user is elsewhere is seen without opening Chat. Opening Chat reads every tab
// (chatSceneEnter → chatMarkAllSeen), which takes the badge away.
//
// The counts are the ones the chat already keeps per tab (chatTabs[].unread):
// the Discord handler adds to the community tab as messages arrive, Juka's
// answers add to the AI tab. Saved holds notes to self and never counts. For
// the community count to move while Chat has not been opened this session,
// chatBackgroundConnect brings the gateway up shortly after start when a
// Discord token is configured.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// chatBackgroundDelay is how long after the first chat tick the gateway is
// opened in the background: past the boot animation and the first Home
// frames, which have better uses for the CPU than a TLS handshake.
const chatBackgroundDelay = 15 * time.Second

var (
	// chatTickStart is when chatTick first ran (main thread only).
	chatTickStart time.Time

	// The badge's "pop" when the count goes up (render thread only).
	chatBadgeLast   int
	chatBadgeBumpAt time.Time
)

// chatBadgeBumpFor is how long the ring around a badge that just grew fades.
const chatBadgeBumpFor = 900 * time.Millisecond

// chatBadgeAnimating keeps the frame rate up while a grown badge's ring fades.
func chatBadgeAnimating() bool {
	return homeLayoutActive && !chatBadgeBumpAt.IsZero() && time.Since(chatBadgeBumpAt) < chatBadgeBumpFor
}

// chatBackgroundConnect asks the sender loop to keep the community gateway up
// even though Chat has not been opened, once the delay has passed, a Discord
// token is configured and the device is online. It runs every frame from
// chatTick and does nothing after the first time it succeeds.
func chatBackgroundConnect(now time.Time) {
	if chatDiscordWanted.Load() {
		return
	}
	if chatTickStart.IsZero() {
		chatTickStart = now
		return
	}
	if now.Sub(chatTickStart) < chatBackgroundDelay {
		return
	}
	creds := chatCredsNow()
	if !creds.valid || creds.discordTok == "" || !IsOnline() {
		return
	}
	chatDiscordWanted.Store(true)
	chatEnsureSync()
	chatWakeSync()
}

// chatUnreadTotal is the number of unread messages across the tabs that can
// have any: the community channel and Juka AI.
func chatUnreadTotal() int {
	chatMutex.Lock()
	defer chatMutex.Unlock()
	return chatTabs[chatTabCommunity].unread + chatTabs[chatTabAI].unread
}

// chatBadgeLabel is the count drawn on the Home tile: exact up to 99, "99+"
// past it.
func chatBadgeLabel(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 99 {
		return "99+"
	}
	return fmt.Sprintf("%d", n)
}

// chatUnreadSubtitle is the tile's description while messages wait.
func chatUnreadSubtitle(n int) string {
	if n == 1 {
		return "1 new message"
	}
	if n > 99 {
		return "99+ new messages"
	}
	return fmt.Sprintf("%d new messages", n)
}

// homeItemOpensChat reports whether a Home tile opens Chat: the built-in card,
// or a Start-menu entry for the Chat scene pinned to the Widgets layout.
func homeItemOpensChat(item HomeItem) bool {
	return item.ID == HomeFocusChat || item.Action == HomeActionChat || item.Icon == "chat"
}

// homeChatTileUnread returns the unread count for a tile that opens Chat (0
// for every other tile) and the item with its subtitle swapped for "N new
// messages" while there are some.
func homeChatTileUnread(item HomeItem) (HomeItem, int) {
	if !homeItemOpensChat(item) {
		return item, 0
	}
	n := chatUnreadTotal()
	// Followed here, on every frame a Chat tile is drawn, so a count that went
	// back to zero (Chat opened) is remembered and the next message pops again.
	if n > chatBadgeLast {
		chatBadgeBumpAt = time.Now()
	}
	chatBadgeLast = n
	if n > 0 {
		item.Subtitle = chatUnreadSubtitle(n)
	}
	return item, n
}

// drawHomeChatBadge hangs the unread count inside the tile's top-right
// corner, level with the icon badge. A count that just went up wears a ring
// that grows and fades, so a live message is noticed from across the screen.
func (h *HomeLayout) drawHomeChatBadge(renderer *sdl.Renderer, fonts HomeFonts, draw sdl.Rect, n int) {
	now := time.Now()
	label := chatBadgeLabel(n)
	if h == nil || renderer == nil || label == "" {
		return
	}
	font := fonts.Meta
	if font == nil {
		font = fonts.Small
	}
	if font == nil {
		return
	}
	tw, th, err := font.SizeUTF8(label)
	if err != nil || tw <= 0 {
		return
	}
	bh := int32(th) + scaleY(6, h.Height)
	bw := int32(tw) + scaleX(14, h.Width)
	if bw < bh {
		bw = bh
	}
	pad := scaleX(homeTilePad, h.Width)
	if bw > draw.W-2*pad || bh > draw.H-2*pad {
		return
	}
	badge := sdl.Rect{X: draw.X + draw.W - pad - bw, Y: draw.Y + pad, W: bw, H: bh}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if age := now.Sub(chatBadgeBumpAt); !chatBadgeBumpAt.IsZero() && age < chatBadgeBumpFor {
		t := float64(age) / float64(chatBadgeBumpFor)
		grow := int32(t * float64(scaleY(10, h.Height)))
		alpha := uint8(200 * (1 - t))
		strokeRoundedRect(renderer, badge.X-grow, badge.Y-grow, badge.W+2*grow, badge.H+2*grow,
			(badge.H+2*grow)/2, 2, sdl.Color{R: 235, G: 72, B: 72, A: alpha})
	}
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, sdl.Color{R: 220, G: 60, B: 60, A: 245})
	drawText(renderer, font, label, badge.X+(badge.W-int32(tw))/2, badge.Y+(badge.H-int32(th))/2,
		sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignLeft)
}
