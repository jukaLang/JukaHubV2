package main

// chatview.go — drawing the Chat screen in Home's glass language.
//
//	┌──────────────────────────────────────────────────────────────┐
//	│ (#general 3) ( Juka AI ) ( Saved )          • Live  8 people  You ✎ │  tabs
//	│ (wifi-off) Offline — messages will send when you reconnect  [Wi-Fi] │  banner
//	│  ..message history, laid out bottom-up, top bubble clipped..      │
//	│                                           [ your bubble ]         │
//	│ (A) Alex 14:03                                                    │
//	│     [ their bubble ]                        ( ↓ Jump to latest )  │
//	│ (+) ( Message #general                                   ) (↑)    │  composer
//	└──────────────────────────────────────────────────────────────┘
//
// The history is laid out from the composer upward and the topmost bubble is
// drawn clipped by the list's edge, so the space under the tabs is always
// used: the old renderer stopped at the last message that fitted whole and
// left an empty band above it. Positions are in pixels (scroll is smooth), the
// expensive part of each bubble — wrapping, links, the clock — comes from the
// layout cache in chatlayout.go, and bubbles are drawn with anti-aliased
// corners from one small cached texture per radius instead of a shape texture
// per bubble size.

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// The type scale, in design pixels on the 1280-wide canvas.
const (
	chatBodyPx     = 19
	chatMetaPx     = 14
	chatTabPx      = 16
	chatComposerPx = 18
	chatTitlePx    = 21
)

// chatPx scales a design length the way the type scale is scaled, so the
// Brick's 960-wide canvas gets the same density as the Smart Pro.
func chatPx(v int32) int32 {
	if v == 0 {
		return 0
	}
	out := int32(math.Round(float64(v) * uiTextScaleFactor()))
	if out == 0 {
		if v > 0 {
			return 1
		}
		return -1
	}
	return out
}

// chatFonts is one frame's type scale.
type chatFonts struct {
	body, meta, tab, composer, title *ttf.Font
}

func chatLoadFonts(config *Config) chatFonts {
	f := chatFonts{
		body:     getFontSized(config, "small", chatBodyPx),
		meta:     getFontSized(config, "small", chatMetaPx),
		tab:      getFontSized(config, "small", chatTabPx),
		composer: getFontSized(config, "small", chatComposerPx),
		title:    getFontSized(config, "small", chatTitlePx),
	}
	if f.body == nil {
		f.body, _ = getCachedFont(config, "small")
	}
	if f.meta == nil {
		f.meta = f.body
	}
	if f.tab == nil {
		f.tab = f.body
	}
	if f.composer == nil {
		f.composer = f.body
	}
	if f.title == nil {
		f.title = f.body
	}
	return f
}

// chatLineHeight is the advance between two wrapped lines.
func chatLineHeight(font *ttf.Font) int32 {
	if font == nil {
		return 20
	}
	return max32(int32(font.Height()), int32(font.LineSkip()))
}

// ── Anti-aliased rounded shapes ──────────────────────────────────────────

// chatShapeKey names one cached corner texture: a disc, or a ring when
// thickness is set.
type chatShapeKey struct {
	radius    int32
	thickness int32
}

var (
	chatShapeOwner *sdl.Renderer
	chatShapes     = map[chatShapeKey]*sdl.Texture{}
	// Scratch rects for the per-corner copies: pointers to package variables
	// do not escape, so drawing a bubble allocates nothing.
	chatSrcRect, chatDstRect sdl.Rect
)

// chatShapeMaxRadius bounds the corner textures (128x128 at most).
const chatShapeMaxRadius = 64

// chatDiscPixels rasterises a white disc (or ring) of radius r with a soft,
// one-pixel anti-aliased rim, as RGBA bytes.
func chatDiscPixels(radius, thickness int32) []byte {
	size := int(radius) * 2
	pix := make([]byte, size*size*4)
	r := float64(radius)
	inner := r - float64(thickness)
	for y := 0; y < size; y++ {
		dy := float64(y) + 0.5 - r
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - r
			d := math.Sqrt(dx*dx + dy*dy)
			cover := uiClamp01(r - d + 0.5)
			if thickness > 0 {
				cover -= uiClamp01(inner - d + 0.5)
				if cover < 0 {
					cover = 0
				}
			}
			i := (y*size + x) * 4
			pix[i], pix[i+1], pix[i+2] = 255, 255, 255
			pix[i+3] = uint8(math.Round(cover * 255))
		}
	}
	return pix
}

// chatShapeTexture returns the cached corner texture, or nil when the renderer
// cannot make one (the caller then draws with the shared scanline shapes).
func chatShapeTexture(renderer *sdl.Renderer, radius, thickness int32) *sdl.Texture {
	if renderer == nil || radius < 2 || radius > chatShapeMaxRadius {
		return nil
	}
	if chatShapeOwner != renderer {
		// Textures belong to the renderer that made them; a cache from another
		// renderer is forgotten, never destroyed (it died with its renderer).
		chatShapes = map[chatShapeKey]*sdl.Texture{}
		chatShapeOwner = renderer
	}
	key := chatShapeKey{radius: radius, thickness: thickness}
	if tex, ok := chatShapes[key]; ok {
		return tex
	}
	size := radius * 2
	pixels := chatDiscPixels(radius, thickness)
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, size, size)
	if err != nil {
		chatShapes[key] = nil
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pixels[0]), int(size)*4); err != nil {
		tex.Destroy()
		chatShapes[key] = nil
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	chatShapes[key] = tex
	return tex
}

// clearChatShapeCache forgets the corner textures. They are a few kilobytes
// and freed with their renderer, so they are dropped rather than destroyed:
// destroying one whose renderer is already gone would corrupt SDL's heap.
func clearChatShapeCache() {
	chatShapes = map[chatShapeKey]*sdl.Texture{}
	chatShapeOwner = nil
}

// chatCopyCorners blits the four quadrants of a corner texture onto rect.
func chatCopyCorners(renderer *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect, r int32) {
	x2, y2 := rect.X+rect.W-r, rect.Y+rect.H-r
	for q := 0; q < 4; q++ {
		sx, sy, dx, dy := int32(0), int32(0), rect.X, rect.Y
		if q == 1 || q == 3 {
			sx, dx = r, x2
		}
		if q >= 2 {
			sy, dy = r, y2
		}
		chatSrcRect = sdl.Rect{X: sx, Y: sy, W: r, H: r}
		chatDstRect = sdl.Rect{X: dx, Y: dy, W: r, H: r}
		_ = renderer.Copy(tex, &chatSrcRect, &chatDstRect)
	}
}

// chatFillRound fills a rounded rect with anti-aliased corners.
func chatFillRound(renderer *sdl.Renderer, rect sdl.Rect, radius int32, c sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || c.A == 0 {
		return
	}
	radius = min32(radius, min32(rect.W, rect.H)/2)
	tex := chatShapeTexture(renderer, radius, 0)
	if tex == nil {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, c)
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = tex.SetColorMod(c.R, c.G, c.B)
	_ = tex.SetAlphaMod(c.A)
	chatCopyCorners(renderer, tex, rect, radius)
	setDrawColor(renderer, c)
	if mid := rect.W - 2*radius; mid > 0 {
		chatDstRect = sdl.Rect{X: rect.X + radius, Y: rect.Y, W: mid, H: radius}
		_ = renderer.FillRect(&chatDstRect)
		chatDstRect = sdl.Rect{X: rect.X + radius, Y: rect.Y + rect.H - radius, W: mid, H: radius}
		_ = renderer.FillRect(&chatDstRect)
	}
	if midH := rect.H - 2*radius; midH > 0 {
		chatDstRect = sdl.Rect{X: rect.X, Y: rect.Y + radius, W: rect.W, H: midH}
		_ = renderer.FillRect(&chatDstRect)
	}
}

// chatStrokeRound draws a one-pixel anti-aliased rounded outline.
func chatStrokeRound(renderer *sdl.Renderer, rect sdl.Rect, radius int32, c sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || c.A == 0 {
		return
	}
	radius = min32(radius, min32(rect.W, rect.H)/2)
	tex := chatShapeTexture(renderer, radius, 1)
	if tex == nil {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, c)
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = tex.SetColorMod(c.R, c.G, c.B)
	_ = tex.SetAlphaMod(c.A)
	chatCopyCorners(renderer, tex, rect, radius)
	setDrawColor(renderer, c)
	if mid := rect.W - 2*radius; mid > 0 {
		chatDstRect = sdl.Rect{X: rect.X + radius, Y: rect.Y, W: mid, H: 1}
		_ = renderer.FillRect(&chatDstRect)
		chatDstRect = sdl.Rect{X: rect.X + radius, Y: rect.Y + rect.H - 1, W: mid, H: 1}
		_ = renderer.FillRect(&chatDstRect)
	}
	if midH := rect.H - 2*radius; midH > 0 {
		chatDstRect = sdl.Rect{X: rect.X, Y: rect.Y + radius, W: 1, H: midH}
		_ = renderer.FillRect(&chatDstRect)
		chatDstRect = sdl.Rect{X: rect.X + rect.W - 1, Y: rect.Y + radius, W: 1, H: midH}
		_ = renderer.FillRect(&chatDstRect)
	}
}

// chatFillDisc fills a circle of diameter size at (x, y).
func chatFillDisc(renderer *sdl.Renderer, x, y, size int32, c sdl.Color) {
	chatFillRound(renderer, sdl.Rect{X: x, Y: y, W: size, H: size}, size/2, c)
}

// chatText draws text without the config's $variable substitution: chat text
// is what people wrote, not a template.
func chatText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) int32 {
	if font == nil || text == "" {
		return 0
	}
	w, _ := drawTextRun(renderer, config, font, sanitizeCached(font, text), color, x, y)
	return w
}

// chatTextCentered draws text centred vertically in rect at x.
func chatTextCentered(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x int32, rect sdl.Rect) int32 {
	if font == nil {
		return 0
	}
	return chatText(renderer, config, font, text, color, x, rect.Y+(rect.H-int32(font.Height()))/2)
}

// chatInsetRect grows (or, negative, shrinks) a rect on every side.
func chatInsetRect(r sdl.Rect, by int32) sdl.Rect {
	return sdl.Rect{X: r.X - by, Y: r.Y - by, W: r.W + 2*by, H: r.H + 2*by}
}

// chatClipRect is r clipped to bounds (zero size when they do not meet).
func chatClipRect(r, bounds sdl.Rect) sdl.Rect {
	return glassIntersect(r, bounds)
}

// ── Labels built once ────────────────────────────────────────────────────

var chatCountLabels = func() [100]string {
	var out [100]string
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out
}()

// chatCountLabel is the text of an unread badge.
func chatCountLabel(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 99 {
		return "99+"
	}
	return chatCountLabels[n]
}

// chatCachedText memoises one formatted string by its integer input, so a
// label that only changes with a count is not rebuilt every frame.
type chatCachedText struct {
	key  int
	text string
	ok   bool
}

func (c *chatCachedText) get(key int, build func(int) string) string {
	if !c.ok || c.key != key {
		c.key, c.text, c.ok = key, build(key), true
	}
	return c.text
}

var (
	chatOfflineCommunityText chatCachedText
	chatOfflineAIText        chatCachedText
	chatJumpText             chatCachedText
	chatPeopleText           chatCachedText
)

var chatDayCache struct {
	today  int
	labels map[int]string
}

func chatDayKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// chatDayLabelCached is chatDayLabel for the device's local day, memoised for
// the current date.
func chatDayLabelCached(t time.Time) string {
	today := chatDayKey(time.Now())
	if chatDayCache.labels == nil || chatDayCache.today != today || len(chatDayCache.labels) > 128 {
		chatDayCache.today = today
		chatDayCache.labels = make(map[int]string, 16)
	}
	local := t.Local()
	key := chatDayKey(local)
	if label, ok := chatDayCache.labels[key]; ok {
		return label
	}
	label := chatDayLabel(local)
	chatDayCache.labels[key] = label
	return label
}

// ── Per-frame state ──────────────────────────────────────────────────────

// chatBannerAction is what the banner's button does.
type chatBannerAction int

const (
	chatBannerNoAction chatBannerAction = iota
	chatBannerWiFi
	chatBannerAddKey
	chatBannerRetry
)

// chatBanner is the slim strip under the tabs: offline, setup, or a problem.
type chatBanner struct {
	show   bool
	icon   string
	text   string
	button string
	action chatBannerAction
	warn   bool
}

// chatSuggestions are the starter questions an empty Juka AI tab offers.
var chatSuggestions = [3]string{
	"What can JukaHub do?",
	"Suggest a retro game for tonight",
	"How do I set a wallpaper?",
}

const chatSuggestionCount = len(chatSuggestions)

// chatSuggestFocus is the starter question the pad is on (-1: none).
var chatSuggestFocus = -1

// chatViewState is what the last frame drew, for the input handlers.
type chatViewState struct {
	area         sdl.Rect
	list         sdl.Rect
	composer     sdl.Rect
	tabs         [chatTabCount]sdl.Rect
	bannerBtn    sdl.Rect
	bannerAction chatBannerAction
	suggestions  [chatSuggestionCount]sdl.Rect
	focusTop     int32
	focusBottom  int32
	focusValid   bool
	atBottom     bool
	lastTicks    uint64
	drawn        bool
}

var chatView chatViewState

// chatItem is one block of the history: a message (with the divider, header
// and status line that go with it) or the typing bubble.
type chatItem struct {
	idx     int // message index; chatItemTyping for the answer being written
	top     int32
	h       int32
	divider string
	newMark bool
	header  bool
	bubbleY int32 // offset of the bubble from top
	bubbleW int32
	bubbleH int32
	status  string
	lay     *chatBubbleLayout
}

const chatItemTyping = -1

var chatItems []chatItem

// chatMetrics are one frame's spacing, scaled to the canvas.
type chatMetrics struct {
	side      int32
	avatar    int32
	avatarGap int32
	padX      int32
	padY      int32
	radius    int32
	lineH     int32
	metaH     int32
	groupGap  int32
	tightGap  int32
	dividerH  int32
	headerH   int32
	statusH   int32
	cardH     int32
	cardGap   int32
	imageW    int32
	imageH    int32
	maxBubble int32
	textW     int32
	introH    int32
}

func chatMetricsFor(fonts chatFonts, list sdl.Rect) chatMetrics {
	m := chatMetrics{
		side:      chatPx(6),
		avatar:    chatPx(34),
		avatarGap: chatPx(10),
		padX:      chatPx(14),
		padY:      chatPx(9),
		radius:    chatPx(18),
		lineH:     chatLineHeight(fonts.body),
		metaH:     int32(fonts.meta.Height()),
		groupGap:  chatPx(16),
		tightGap:  chatPx(4),
		cardH:     chatPx(48),
		cardGap:   chatPx(6),
		imageW:    chatPx(280),
		imageH:    chatPx(168),
		introH:    chatPx(118),
	}
	m.dividerH = m.metaH + chatPx(20)
	m.headerH = m.metaH + chatPx(5)
	m.statusH = m.metaH + chatPx(4)
	m.maxBubble = min32(list.W*68/100, chatPx(820))
	if avail := list.W - m.side*2 - m.avatar - m.avatarGap; m.maxBubble > avail {
		m.maxBubble = avail
	}
	m.textW = max32(chatPx(60), m.maxBubble-2*m.padX)
	if m.imageW > m.textW {
		m.imageW = m.textW
	}
	return m
}

// chatResetHits clears every hit target the last frame recorded.
func chatResetHits() {
	chatNameRect = sdl.Rect{}
	chatSendBtnRect = sdl.Rect{}
	chatAttachBtnRect = sdl.Rect{}
	chatInputRect = sdl.Rect{}
	chatLinkRects = chatLinkRects[:0]
	chatTextLinkRects = chatTextLinkRects[:0]
	chatImageRects = chatImageRects[:0]
	chatFileRects = chatFileRects[:0]
	chatTrayHits = chatTrayHits[:0]
	chatTrayMore = sdl.Rect{}
	chatMessageHits = chatMessageHits[:0]
	chatHeaderMembersRect = sdl.Rect{}
	chatJumpRect = sdl.Rect{}
	chatReplyCancelRect = sdl.Rect{}
	chatReplyStrip = sdl.Rect{}
	chatView.tabs = [chatTabCount]sdl.Rect{}
	chatView.bannerBtn = sdl.Rect{}
	chatView.bannerAction = chatBannerNoAction
	chatView.suggestions = [chatSuggestionCount]sdl.Rect{}
	chatView.focusValid = false
}

// chatHover reports whether the mouse is over r. Only a desktop has a pointer
// worth following; on the handheld the emulated pointer parks on the last tap.
func chatHover(r sdl.Rect) bool {
	return IsWindows() && r.W > 0 && pointInRect(mouseX, mouseY, r)
}

// chatPadOnContent reports whether the pad's focus is in the chat rather than
// on the top bar.
func chatPadOnContent() bool {
	return chromeTier(focusEngine.ElementIndex()) == 0
}

// ── The screen ───────────────────────────────────────────────────────────

// chatSceneArea is where the chat is drawn: the chat element's rect when the
// scene config carries one, otherwise the space between the header and the
// footer.
func chatSceneArea(config *Config) sdl.Rect {
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, elem := range config.Scenes[currentSceneIndex].Elements {
			if elem.Type == "chat" {
				return sdl.Rect{X: elem.X, Y: elem.Y, W: getElementWidth(elem, 1232), H: getElementHeight(elem, 584)}
			}
		}
	}
	x := scaleX(24, screenWidth)
	y := headerBarHeight() + 16
	return sdl.Rect{X: x, Y: y, W: screenWidth - 2*x, H: screenHeight - y - footerBarHeight() - 16}
}

// chatRenderScene draws the whole Chat scene: Home's backdrop, the chat, and
// the shared header and footer.
func chatRenderScene(renderer *sdl.Renderer, config *Config) {
	chatSyncVisibility(config)
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	chatRenderArea(renderer, config, chatSceneArea(config))
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// chatRenderArea draws the chat into area: tabs, banner, history, composer.
func chatRenderArea(renderer *sdl.Renderer, config *Config, area sdl.Rect) {
	chatResetHits()
	if renderer == nil || config == nil || area.W <= 0 || area.H <= 0 {
		return
	}
	// One stat per session: the explorer root only changes with the config,
	// and the attach path updates it as soon as the user picks a folder.
	if chatAttachmentRoot == "." {
		chatAttachmentRoot = feRoot(config)
	}
	fonts := chatLoadFonts(config)
	if fonts.body == nil {
		return
	}
	now := sdl.GetTicks64()
	tab := chatActiveTabID()
	chatView.area = area
	chatView.drawn = true

	banner := chatCurrentBanner(tab)
	replyH := int32(0)
	if tab == chatTabCommunity && !chatSelectionBar.open {
		replyH = chatReplyStripHeight(fonts.meta)
	}
	// The files waiting to go sit right above the composer; the action bar
	// takes the composer's place, and then they wait out of sight.
	trayH := int32(0)
	if !chatSelectionBar.open && chatStagedCount(tab) > 0 {
		trayH = chatTrayHeight(fonts.meta)
	}
	gap := chatPx(10)
	tabsRect := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: chatPx(42)}
	y := tabsRect.Y + tabsRect.H + gap
	bannerRect := sdl.Rect{}
	if banner.show {
		bannerRect = sdl.Rect{X: area.X, Y: y, W: area.W, H: chatPx(34)}
		y += bannerRect.H + gap
	}
	compH := chatPx(52)
	composer := sdl.Rect{X: area.X, Y: area.Y + area.H - compH, W: area.W, H: compH}
	bottom := composer.Y - gap
	trayRect := sdl.Rect{}
	if trayH > 0 {
		trayRect = sdl.Rect{X: area.X, Y: bottom - trayH, W: area.W, H: trayH}
		bottom = trayRect.Y - chatPx(6)
	}
	replyRect := sdl.Rect{}
	if replyH > 0 {
		replyRect = sdl.Rect{X: area.X, Y: bottom - replyH, W: area.W, H: replyH}
		bottom = replyRect.Y - chatPx(6)
	}
	list := sdl.Rect{X: area.X, Y: y, W: area.W, H: max32(0, bottom-y)}
	chatView.list = list
	chatView.composer = composer

	chatDrawTabs(renderer, config, fonts, tabsRect, tab)
	if banner.show {
		chatDrawBanner(renderer, config, fonts, bannerRect, banner)
	}
	chatDrawHistory(renderer, config, fonts, list, tab, now)
	if replyH > 0 {
		drawChatReplyStrip(renderer, config, fonts.meta, replyRect)
	}
	if trayH > 0 {
		chatDrawTray(renderer, config, fonts.meta, trayRect, tab)
	}
	if chatSelectionBar.open {
		drawChatSelectionBar(renderer, config, fonts.meta, composer)
	} else {
		chatDrawComposer(renderer, config, fonts, composer, tab, now)
	}
	if !chatView.atBottom && list.H > 0 {
		chatDrawJumpPill(renderer, config, fonts.meta, list, chatTabUnread(tab))
	}
	processChatImageDownloads(renderer)
	chatView.lastTicks = now
}

// ── Tabs ─────────────────────────────────────────────────────────────────

// chatTabIcon is the glyph of a tab.
func chatTabIcon(tab chatTab) string {
	switch tab {
	case chatTabAI:
		return "sparkles"
	case chatTabSaved:
		return "star"
	}
	return "users"
}

// chatTabLabel is the title of a tab.
func chatTabLabel(tab chatTab) string {
	switch tab {
	case chatTabAI:
		return "Juka AI"
	case chatTabSaved:
		return "Saved"
	}
	return chatCommunityTitle()
}

// chatDrawTabs draws the tab strip and the status chips at its right end.
func chatDrawTabs(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect, active chatTab) {
	h := rect.H - chatPx(2)
	y := rect.Y + (rect.H-h)/2
	pad := chatPx(14)
	icon := chatPx(20)
	gap := chatPx(6)
	x := rect.X
	accent := glassAccent()
	ink := contrastText(accent)
	for t := chatTabCommunity; t < chatTabCount; t++ {
		label := chatTabLabel(t)
		lw, _ := measureText(fonts.tab, label)
		badge := ""
		if t != active {
			badge = chatCountLabel(chatTabUnread(t))
		}
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
		chatView.tabs[t] = r
		iconRect := sdl.Rect{X: r.X + pad, Y: r.Y + (h-icon)/2, W: icon, H: icon}
		labelX := iconRect.X + icon + chatPx(8)
		if t == active {
			chatFillRound(renderer, r, h/2, accent)
			DrawHomeIcon(renderer, chatTabIcon(t), iconRect, true)
			chatTextCentered(renderer, config, fonts.tab, label, ink, labelX, r)
		} else {
			if chatHover(r) {
				chatFillRound(renderer, r, h/2, glassA(14))
			}
			DrawHomeIcon(renderer, chatTabIcon(t), iconRect, false)
			chatTextCentered(renderer, config, fonts.tab, label, ColorTextSecondary(), labelX, r)
			if badgeW > 0 {
				bh := int32(fonts.meta.Height()) + chatPx(4)
				b := sdl.Rect{X: labelX + lw + chatPx(8), Y: r.Y + (h-bh)/2, W: badgeW, H: bh}
				chatFillRound(renderer, b, bh/2, accent)
				bw, _ := measureText(fonts.meta, badge)
				chatTextCentered(renderer, config, fonts.meta, badge, ink, b.X+(b.W-bw)/2, b)
			}
		}
		x += w + gap
	}
	chatDrawTabChips(renderer, config, fonts, rect, x+chatPx(12), active)
}

// chatDrawTabChips draws, right to left, the rename chip, the people chip
// (community) and the connection state, dropping whatever does not fit.
func chatDrawTabChips(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect, minX int32, tab chatTab) {
	h := chatPx(32)
	y := rect.Y + (rect.H-h)/2
	right := rect.X + rect.W
	gap := chatPx(8)
	pad := chatPx(12)

	// Rename: the name the user's messages are signed with.
	name := chatDisplayName()
	nw, _ := measureText(fonts.meta, name)
	pencil := chatPx(16)
	w := pad + nw + chatPx(6) + pencil + pad
	if right-w < minX {
		return
	}
	r := sdl.Rect{X: right - w, Y: y, W: w, H: h}
	chatNameRect = r
	chatDrawChip(renderer, r, chatHover(r))
	chatTextCentered(renderer, config, fonts.meta, name, ColorTextSecondary(), r.X+pad, r)
	DrawHomeIcon(renderer, "edit", sdl.Rect{X: r.X + pad + nw + chatPx(6), Y: r.Y + (h-pencil)/2, W: pencil, H: pencil}, false)
	right = r.X - gap

	if tab == chatTabCommunity {
		count := chatCommunityMemberCount()
		if count > 0 {
			label := chatPeopleText.get(count, func(n int) string {
				if n == 1 {
					return "1 person"
				}
				return strconv.Itoa(n) + " people"
			})
			lw, _ := measureText(fonts.meta, label)
			icon := chatPx(16)
			cw := pad + icon + chatPx(6) + lw + pad
			if right-cw >= minX {
				c := sdl.Rect{X: right - cw, Y: y, W: cw, H: h}
				chatHeaderMembersRect = c
				chatDrawChip(renderer, c, chatHover(c))
				DrawHomeIcon(renderer, "users", sdl.Rect{X: c.X + pad, Y: c.Y + (h-icon)/2, W: icon, H: icon}, false)
				chatTextCentered(renderer, config, fonts.meta, label, ColorTextSecondary(), c.X+pad+icon+chatPx(6), c)
				right = c.X - gap
			}
		}
	}

	label, dot := chatStatusLine(tab)
	if label == "" {
		return
	}
	lw, _ := measureText(fonts.meta, label)
	d := chatPx(8)
	sw := d + chatPx(6) + lw
	if right-sw < minX {
		return
	}
	sx := right - sw
	if dot.A > 0 {
		chatFillDisc(renderer, sx, y+(h-d)/2, d, dot)
	}
	chatTextCentered(renderer, config, fonts.meta, label, ColorTextTertiary(), sx+d+chatPx(6), sdl.Rect{X: sx, Y: y, W: sw, H: h})
}

// chatDrawChip is a small glass pill.
func chatDrawChip(renderer *sdl.Renderer, r sdl.Rect, hovered bool) {
	fill := glassA(12)
	if hovered {
		fill = glassA(20)
	}
	chatFillRound(renderer, r, r.H/2, fill)
	chatStrokeRound(renderer, r, r.H/2, glassA(26))
}

// chatStatusLine is the connection state shown in the tab strip.
func chatStatusLine(tab chatTab) (string, sdl.Color) {
	switch tab {
	case chatTabCommunity:
		if netReconnecting() {
			return "Reconnecting…", WithAlpha(WarningInk(), 200)
		}
		if !IsOnline() {
			return "Offline", WithAlpha(ColorTextTertiary(), 200)
		}
		switch chatNet() {
		case chatNetLive:
			return "Live", SuccessInk()
		case chatNetConnecting, chatNetIdle:
			pulse := uint8(120 + 100*math.Abs(math.Sin(float64(sdl.GetTicks64())/400)))
			return "Connecting…", WithAlpha(WarningInk(), pulse)
		}
		return "", sdl.Color{}
	case chatTabAI:
		if chatStreamSnapshot().active {
			return "Thinking…", WithAlpha(glassAccent(), 220)
		}
	case chatTabSaved:
		return "Only on this device", sdl.Color{}
	}
	return "", sdl.Color{}
}

// chatMemberCountCache remembers how many people the community history has.
var chatMemberCountCache struct {
	n      int
	newest string
	count  int
	ok     bool
}

// chatCommunityMemberCount counts the speakers of the community history,
// recounting only when the history changed.
func chatCommunityMemberCount() int {
	msgs := chatTabMessages(chatTabCommunity)
	newest := ""
	if n := len(msgs); n > 0 {
		newest = msgs[n-1].ID
	}
	c := &chatMemberCountCache
	if c.ok && c.n == len(msgs) && c.newest == newest {
		return c.count
	}
	c.n, c.newest, c.count, c.ok = len(msgs), newest, len(chatMemberProfiles()), true
	return c.count
}

// ── Banner ───────────────────────────────────────────────────────────────

// chatHasGroqKey reports whether a Groq key is configured, without decrypting
// it on every frame.
func chatHasGroqKey() bool {
	if c := chatCredsNow(); c.valid {
		return c.groqKey != ""
	}
	return chatRawGroqKey(appConfig) != ""
}

// chatCurrentBanner is the banner a tab shows, if any.
func chatCurrentBanner(tab chatTab) chatBanner {
	if b, ok := chatReconnectBanner(tab); ok {
		return b
	}
	online := IsOnline()
	switch tab {
	case chatTabCommunity:
		if !online {
			n := chatPendingCount(chatTabCommunity)
			text := chatOfflineCommunityText.get(n, func(n int) string {
				switch n {
				case 0:
					return "Offline — messages will send when you reconnect"
				case 1:
					return "Offline — 1 message will send when you reconnect"
				}
				return "Offline — " + strconv.Itoa(n) + " messages will send when you reconnect"
			})
			return chatBanner{show: true, icon: "wifi-off", text: text, button: "Wi-Fi", action: chatBannerWiFi}
		}
		switch chatNet() {
		case chatNetUnconfigured:
			return chatBanner{show: true, icon: "key", text: "Community chat isn't set up on this device"}
		case chatNetRejected:
			return chatBanner{show: true, icon: "warning", text: "Community chat is unavailable right now", button: "Retry", action: chatBannerRetry, warn: true}
		case chatNetUnreachable:
			return chatBanner{show: true, icon: "warning", text: "Can't reach Discord — trying again", button: "Retry", action: chatBannerRetry, warn: true}
		}
	case chatTabAI:
		if !chatHasGroqKey() {
			return chatBanner{show: true, icon: "key", text: "Add a Groq API key to talk to Juka", button: "Add key", action: chatBannerAddKey}
		}
		if !online {
			n := chatPendingCount(chatTabAI)
			text := chatOfflineAIText.get(n, func(n int) string {
				if n == 0 {
					return "AI needs internet — questions will send when you reconnect"
				}
				return "Offline — Juka will answer when you reconnect"
			})
			return chatBanner{show: true, icon: "wifi-off", text: text, button: "Wi-Fi", action: chatBannerWiFi}
		}
	}
	return chatBanner{}
}

// chatDrawBanner draws the slim strip under the tabs.
func chatDrawBanner(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect, b chatBanner) {
	tone := glassAccent()
	if b.warn {
		tone = WarningInk()
	}
	chatFillRound(renderer, rect, rect.H/2, WithAlpha(tone, 30))
	chatStrokeRound(renderer, rect, rect.H/2, WithAlpha(tone, 90))
	icon := chatPx(18)
	pad := chatPx(14)
	DrawHomeIcon(renderer, b.icon, sdl.Rect{X: rect.X + pad, Y: rect.Y + (rect.H-icon)/2, W: icon, H: icon}, false)
	textX := rect.X + pad + icon + chatPx(10)
	right := rect.X + rect.W - pad
	if b.button != "" {
		bw, _ := measureText(fonts.meta, b.button)
		bh := rect.H - chatPx(8)
		btnW := bw + chatPx(28)
		btn := sdl.Rect{X: rect.X + rect.W - chatPx(4) - btnW, Y: rect.Y + (rect.H-bh)/2, W: btnW, H: bh}
		hover := chatHover(btn)
		fill := glassA(18)
		if hover {
			fill = glassA(28)
		}
		chatFillRound(renderer, btn, bh/2, fill)
		chatTextCentered(renderer, config, fonts.meta, b.button, ColorTextPrimary(), btn.X+(btn.W-bw)/2, btn)
		chatView.bannerBtn = btn
		chatView.bannerAction = b.action
		right = btn.X - chatPx(10)
	}
	if avail := right - textX; avail > 0 {
		chatTextCentered(renderer, config, fonts.meta, ellipsize(fonts.meta, b.text, avail), ColorTextPrimary(), textX, rect)
	}
}

// ── History ──────────────────────────────────────────────────────────────

// chatScrollTau is the smooth scroll's time constant in milliseconds.
func chatScrollTau() float64 {
	if appConfig != nil && effectiveReducedMotion(appConfig) {
		return 35
	}
	return 70
}

// chatItemFor measures message i of msgs as a history block.
func chatItemFor(msgs []ChatMessage, i int, fonts chatFonts, m chatMetrics, view *chatTabView, tab chatTab) chatItem {
	msg := &msgs[i]
	it := chatItem{idx: i}
	if i == 0 || chatDayKey(msgs[i-1].Timestamp.Local()) != chatDayKey(msg.Timestamp.Local()) {
		it.divider = chatDayLabelCached(msg.Timestamp)
	}
	if view.dividerID != "" && msg.ID == view.dividerID {
		it.divider = "New messages"
		it.newMark = true
	}
	h := int32(0)
	if it.divider != "" {
		h += m.dividerH
	}
	if chatIsSystem(*msg) {
		it.bubbleY = h
		it.bubbleH = m.metaH + chatPx(12)
		it.h = h + it.bubbleH
		return it
	}
	it.header = it.divider != "" || !chatContinuesGroup(msgs, i)
	if it.header {
		h += m.headerH
	}
	it.lay = chatLayoutFor(fonts.body, msg, m.textW)
	it.bubbleW, it.bubbleH = chatBubbleSize(msg, it.lay, fonts, m)
	it.bubbleY = h
	h += it.bubbleH
	if chatIsOwn(*msg) {
		it.status = chatStatusFor(msg, tab)
	} else if msg.Error != "" {
		it.status = msg.Error
	}
	if it.status != "" {
		h += m.statusH
	}
	it.h = h
	return it
}

// chatBubbleSize is a bubble's size from its layout.
func chatBubbleSize(msg *ChatMessage, l *chatBubbleLayout, fonts chatFonts, m chatMetrics) (int32, int32) {
	content := int32(0)
	w := l.body.width + 2*m.padX + chatPx(2)
	if msg.Quote != nil {
		content += chatQuoteHeight(fonts.meta, msg.Quote) + chatPx(6)
		w = max32(w, chatPx(240))
	}
	content += int32(len(l.body.lines)) * m.lineH
	if l.image != "" || l.imageFile >= 0 {
		if content > 0 {
			content += chatPx(8)
		}
		content += m.imageH
		w = max32(w, m.imageW+2*m.padX)
	}
	if len(l.files) > 0 {
		if content > 0 {
			content += chatPx(8)
		}
		content += int32(len(l.files))*m.cardH + int32(len(l.files)-1)*m.cardGap
		w = max32(w, chatPx(300))
	}
	if len(l.cards) > 0 {
		if content > 0 {
			content += chatPx(8)
		}
		content += int32(len(l.cards))*m.cardH + int32(len(l.cards)-1)*m.cardGap
		w = max32(w, chatPx(300))
	}
	if content == 0 {
		content = m.lineH
	}
	w = max32(w, chatPx(56))
	if w > m.maxBubble {
		w = m.maxBubble
	}
	return w, content + 2*m.padY
}

// chatStatusFor is the line under one of this device's messages.
func chatStatusFor(msg *ChatMessage, tab chatTab) string {
	if msg.Error != "" {
		return msg.Error
	}
	if !msg.Pending {
		return ""
	}
	online := IsOnline()
	switch tab {
	case chatTabAI:
		if !online {
			return "Will ask Juka when you're back online"
		}
		if s := chatStreamSnapshot(); s.active && s.promptID == msg.ID {
			return ""
		}
		return "Waiting for Juka…"
	case chatTabCommunity:
		if !online {
			return "Will send when you're back online"
		}
		if chatNet() != chatNetLive {
			return "Waiting for the connection…"
		}
		if len(msg.Files) > 0 {
			if sent, total, ok := chatUploadProgress(msg.ID); ok {
				return chatUploadStatus(sent, total)
			}
			if note := chatUploadNote(); note != "" {
				return note
			}
			return "Waiting to upload…"
		}
		// The channel keeps the order: words written after a file wait for it.
		if chatUploadBusy.Load() || chatUploadNote() != "" {
			return "Waiting for the upload above…"
		}
	}
	return "Sending…"
}

// chatQuoteHeight is the height of a reply's quote block.
func chatQuoteHeight(font *ttf.Font, q *ChatQuote) int32 {
	if font == nil || q == nil {
		return 0
	}
	if strings.TrimSpace(q.Text) == "" {
		return int32(font.Height()) + chatPx(8)
	}
	return int32(font.Height())*2 + chatPx(10)
}

// chatGapAbove is the space between message i-1 and message i.
func chatGapAbove(msgs []ChatMessage, i int, m chatMetrics) int32 {
	if i <= 0 {
		return 0
	}
	if chatContinuesGroup(msgs, i) {
		return m.tightGap
	}
	return m.groupGap
}

// chatStreamLayout caches the typing bubble's layout for the text it was built
// from.
var chatStreamLayout struct {
	text string
	maxW int32
	font *ttf.Font
	lay  *chatBubbleLayout
}

// chatDrawHistory lays out and draws the message list.
func chatDrawHistory(renderer *sdl.Renderer, config *Config, fonts chatFonts, list sdl.Rect, tab chatTab, now uint64) {
	view := &chatViews[tab]
	m := chatMetricsFor(fonts, list)

	chatMutex.Lock()
	if view.atBottom && chatWasVisible && chatTabs[tab].unread > 0 {
		chatMarkSeenLocked(tab)
	}
	msgs := *chatTabSliceLocked(tab)
	stream := chatStream
	chatMutex.Unlock()

	// The right stick moves the view directly, at its speed in pixels per
	// second (chatSceneStick). A held stick sends no events, so it also keeps
	// the frame rate up.
	if chatStickSpeed != 0 && chatView.lastTicks != 0 {
		if dt := float64(now - chatView.lastTicks); dt > 0 && dt <= 250 {
			view.target += chatStickSpeed * dt / 1000
			if view.target < 0 {
				view.target = 0
			}
			view.scroll = view.target
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
	}

	// The smooth scroll: a frame-rate independent ease toward the target.
	if dt := float64(now - chatView.lastTicks); chatView.lastTicks == 0 || dt > 250 {
		view.scroll = view.target
	} else if view.scroll != view.target {
		k := 1 - math.Exp(-dt/chatScrollTau())
		view.scroll += (view.target - view.scroll) * k
		if math.Abs(view.target-view.scroll) < 0.5 {
			view.scroll = view.target
		}
	}

	// The answer Juka is writing sits under the newest message.
	var streamItem chatItem
	var streamLay *chatBubbleLayout
	streamMsg := ChatMessage{Sender: chatAISender}
	hasStream := tab == chatTabAI && stream.active
	if hasStream {
		streamMsg.Text = chatStripThink(stream.text)
		streamItem = chatItem{idx: chatItemTyping, header: true}
		if streamMsg.Text != "" {
			// The answer grows every few frames, not every frame: it is laid
			// out again only when its text changed.
			c := &chatStreamLayout
			if c.lay == nil || c.text != streamMsg.Text || c.maxW != m.textW || c.font != fonts.body {
				c.text, c.maxW, c.font = streamMsg.Text, m.textW, fonts.body
				c.lay = chatBuildLayout(fonts.body, &streamMsg, m.textW, false)
			}
			streamLay = c.lay
			streamItem.lay = streamLay
			streamItem.bubbleW, streamItem.bubbleH = chatBubbleSize(&streamMsg, streamLay, fonts, m)
		} else {
			streamItem.bubbleW = chatPx(78)
			streamItem.bubbleH = m.lineH + 2*m.padY
		}
		streamItem.bubbleY = m.headerH
		streamItem.h = m.headerH + streamItem.bubbleH
	}

	// Hold still while scrolled back: new messages (and a growing answer)
	// arrive below the view instead of pushing it.
	newest := ""
	if n := len(msgs); n > 0 {
		newest = msgs[n-1].ID
	}
	extra := streamItem.h
	if view.scroll > 0.5 || view.target > 0.5 {
		added := extra - view.bottomExtra
		if view.lastNewest != "" && newest != view.lastNewest {
			grown, found := int32(0), false
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].ID == view.lastNewest {
					found = true
					break
				}
				it := chatItemFor(msgs, i, fonts, m, view, tab)
				grown += it.h + chatGapAbove(msgs, i, m)
			}
			// When the old newest message is gone (deleted, trimmed) there is
			// no telling what moved; better a jump than a wild scroll.
			if found {
				added += grown
			}
		}
		if added != 0 {
			view.scroll += float64(added)
			view.target += float64(added)
		}
	}
	view.lastNewest = newest
	view.bottomExtra = extra

	if len(msgs) == 0 && !hasStream {
		view.scroll, view.target = 0, 0
		view.atBottom = true
		chatView.atBottom = true
		chatDrawEmpty(renderer, config, fonts, list, tab, m)
		chatSetLive(tab)
		return
	}

	// Pass 1: measure from the newest message up, far enough to cover the
	// view, the scroll target and any message the view has been asked to show.
	items := chatItems[:0]
	bottomPad := chatPx(6)
	base := list.Y + list.H - bottomPad + int32(math.Round(view.scroll))
	y := base
	if hasStream {
		streamItem.top = y - streamItem.h
		items = append(items, streamItem)
		y = streamItem.top - m.groupGap
	}
	reach := list.Y - list.H/2 - int32(math.Max(0, view.target-view.scroll))
	walkedAll := true
	for i := len(msgs) - 1; i >= 0; i-- {
		if y < reach && view.anchorID == "" {
			walkedAll = false
			break
		}
		it := chatItemFor(msgs, i, fonts, m, view, tab)
		it.top = y - it.h
		items = append(items, it)
		if msgs[i].ID == view.anchorID && view.anchorID != "" {
			chatResolveAnchor(view, it, list)
		}
		y = it.top - chatGapAbove(msgs, i, m)
	}
	chatItems = items
	if view.anchorID != "" {
		// The message is gone (deleted, trimmed): nothing to bring into view.
		view.anchorID = ""
	}

	// The top of the history: the intro block above the oldest message. When
	// it is below the list's top edge the view is scrolled past the start.
	shift := int32(0)
	introTop := int32(0)
	view.topReached = false
	if walkedAll {
		introTop = y - m.introH
		if introTop > list.Y {
			over := float64(introTop - list.Y)
			if over > view.scroll {
				over = view.scroll
			}
			if over > 0 {
				view.scroll -= over
				shift = int32(math.Round(over))
			}
			if view.target > view.scroll {
				view.target = view.scroll
			}
			view.topReached = true
		} else {
			if limit := view.scroll + float64(list.Y-introTop); view.target > limit {
				view.target = limit
			}
			view.topReached = introTop+m.introH > list.Y
		}
	}
	if view.target < 0 {
		view.target = 0
	}
	view.atBottom = view.target <= 0.5 && view.scroll <= 0.5
	chatView.atBottom = view.atBottom
	chatSetLive(tab)

	// Pass 2: draw what is on screen, clipped to the list.
	hadClip := renderer.IsClipEnabled()
	prevClip := renderer.GetClipRect()
	clip := list
	if hadClip {
		clip = chatClipRect(prevClip, list)
	}
	_ = renderer.SetClipRect(&clip)
	listBottom := list.Y + list.H
	for k := range items {
		it := &items[k]
		it.top -= shift
		if it.top > listBottom || it.top+it.h < list.Y {
			continue
		}
		if it.idx == chatItemTyping {
			chatDrawStream(renderer, config, fonts, m, list, it, streamLay, now)
			continue
		}
		chatDrawMessage(renderer, config, fonts, m, list, it, msgs, tab)
	}
	if walkedAll {
		chatDrawIntro(renderer, config, fonts, list, introTop-shift, m.introH, tab)
	}
	if hadClip {
		_ = renderer.SetClipRect(&prevClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// chatSetLive publishes which tab is read as messages arrive.
func chatSetLive(tab chatTab) {
	if chatWasVisible && chatView.atBottom {
		chatLiveTab.Store(int32(tab))
	} else {
		chatLiveTab.Store(-1)
	}
}

// chatResolveAnchor moves the scroll target so a message (with its divider
// and header) is fully in view. A message taller than the list is aligned by
// the edge the reader arrived from: its end when walking up, its start when
// walking down.
func chatResolveAnchor(view *chatTabView, it chatItem, list sdl.Rect) {
	// it.top is where the block is at the current scroll; the test is made
	// where it will be once the scroll reaches its target, so quick presses
	// during an animation still land the message fully in view.
	top := it.top + int32(math.Round(view.target-view.scroll))
	bottom := top + it.h
	listBottom := list.Y + list.H - chatPx(6)
	if it.h > list.H-chatPx(12) {
		if view.anchorMode == chatAnchorTop {
			view.target += float64(list.Y + chatPx(6) - top)
		} else {
			view.target += float64(listBottom - bottom)
		}
	} else if bottom > listBottom {
		view.target -= float64(bottom - listBottom)
	} else if top < list.Y+chatPx(6) {
		view.target += float64(list.Y + chatPx(6) - top)
	}
	if view.target < 0 {
		view.target = 0
	}
	view.anchorID = ""
	view.anchorMode = chatAnchorNone
}

// chatBubbleColors is a bubble's fill and edge.
func chatBubbleColors(own, ai, pending, failed bool) (sdl.Color, sdl.Color) {
	accent := glassAccent()
	switch {
	case own && failed:
		return WithAlpha(accent, 40), WithAlpha(WarningInk(), 210)
	case own && pending:
		return WithAlpha(accent, 34), WithAlpha(accent, 96)
	case own:
		return WithAlpha(accent, 64), WithAlpha(accent, 124)
	case ai:
		return glassA(20), WithAlpha(accent, 80)
	}
	return glassA(18), glassA(26)
}

// chatDrawDivider draws a day (or "New messages") divider in a block's top.
func chatDrawDivider(renderer *sdl.Renderer, config *Config, font *ttf.Font, list sdl.Rect, y, h int32, label string, newMark bool) {
	if font == nil {
		return
	}
	lw, lh := measureText(font, label)
	cy := y + h/2
	lineCol := glassA(26)
	textCol := ColorTextTertiary()
	if newMark {
		lineCol = WithAlpha(glassAccent(), 170)
		textCol = glassAccent()
	}
	pillW := lw + chatPx(20)
	pillX := list.X + (list.W-pillW)/2
	setDrawColor(renderer, lineCol)
	inset := chatPx(24)
	if left := pillX - chatPx(10) - (list.X + inset); left > 0 {
		chatDstRect = sdl.Rect{X: list.X + inset, Y: cy, W: left, H: 1}
		_ = renderer.FillRect(&chatDstRect)
	}
	if rightStart := pillX + pillW + chatPx(10); list.X+list.W-inset > rightStart {
		chatDstRect = sdl.Rect{X: rightStart, Y: cy, W: list.X + list.W - inset - rightStart, H: 1}
		_ = renderer.FillRect(&chatDstRect)
	}
	ph := lh + chatPx(6)
	chatFillRound(renderer, sdl.Rect{X: pillX, Y: cy - ph/2, W: pillW, H: ph}, ph/2, glassA(10))
	chatText(renderer, config, font, label, textCol, pillX+(pillW-lw)/2, cy-lh/2)
}

// chatDrawMessage draws one message block.
func chatDrawMessage(renderer *sdl.Renderer, config *Config, fonts chatFonts, m chatMetrics, list sdl.Rect, it *chatItem, msgs []ChatMessage, tab chatTab) {
	msg := &msgs[it.idx]
	y := it.top
	if it.divider != "" {
		chatDrawDivider(renderer, config, fonts.meta, list, y, m.dividerH, it.divider, it.newMark)
		y += m.dividerH
	}
	if chatIsSystem(*msg) {
		label := msg.Text
		lw, lh := measureText(fonts.meta, label)
		pw := lw + chatPx(24)
		pill := sdl.Rect{X: list.X + (list.W-pw)/2, Y: y, W: pw, H: it.bubbleH}
		chatFillRound(renderer, pill, pill.H/2, glassA(10))
		chatText(renderer, config, fonts.meta, label, ColorTextTertiary(), pill.X+(pw-lw)/2, pill.Y+(pill.H-lh)/2)
		return
	}
	own := chatIsOwn(*msg)
	ai := chatIsAI(*msg)
	l := it.lay
	bx := list.X + m.side + m.avatar + m.avatarGap
	if own {
		bx = list.X + list.W - m.side - it.bubbleW
	}
	var avatarHit, nameHit sdl.Rect
	if it.header {
		hy := y + (m.headerH-m.metaH)/2 - chatPx(1)
		if own {
			cw, _ := measureText(fonts.meta, l.clock)
			chatText(renderer, config, fonts.meta, l.clock, ColorTextTertiary(), bx+it.bubbleW-cw, hy)
		} else {
			nameCol := senderColorForName(msg.Sender)
			if ai {
				nameCol = glassAccent()
			}
			maxName := max32(chatPx(40), list.X+list.W-bx-chatPx(80))
			name := ellipsize(fonts.meta, sanitizeCached(fonts.meta, msg.Sender), maxName)
			nw, _ := measureText(fonts.meta, name)
			drawTextRun(renderer, config, fonts.meta, name, nameCol, bx+chatPx(4), hy)
			nameHit = chatClipRect(sdl.Rect{X: bx + chatPx(4), Y: hy, W: nw, H: m.metaH}, list)
			chatText(renderer, config, fonts.meta, l.clock, ColorTextTertiary(), bx+chatPx(4)+nw+chatPx(8), hy)
		}
		y += m.headerH
	}
	bubble := sdl.Rect{X: bx, Y: y, W: it.bubbleW, H: it.bubbleH}
	if !own && it.header {
		a := sdl.Rect{X: list.X + m.side, Y: bubble.Y, W: m.avatar, H: m.avatar}
		if ai {
			chatDrawAIAvatar(renderer, a)
		} else {
			renderChatAvatar(renderer, config, fonts.meta, a.X, a.Y, a.W, msg.Sender, msg.AvatarURL, senderColorForName(msg.Sender))
		}
		avatarHit = chatClipRect(a, list)
	}
	fill, edge := chatBubbleColors(own, ai, msg.Pending, msg.Error != "")
	chatFillRound(renderer, bubble, m.radius, fill)
	chatStrokeRound(renderer, bubble, m.radius, edge)
	focused := chatSelected == it.idx && chatPadOnContent()
	if focused {
		ring := chatInsetRect(bubble, glassFocusRingWidth)
		strokeRoundedRect(renderer, ring.X, ring.Y, ring.W, ring.H, m.radius+glassFocusRingWidth, glassFocusRingWidth, glassAccent())
		chatView.focusTop, chatView.focusBottom, chatView.focusValid = it.top, it.top+it.h, true
	}

	cy := bubble.Y + m.padY
	inner := bubble.W - 2*m.padX
	if msg.Quote != nil {
		qh := drawChatQuoteBlock(renderer, config, fonts.meta, bubble.X+m.padX, cy, inner, msg.Quote)
		cy += qh + chatPx(6)
	}
	textCol := ColorTextPrimary()
	linkCol := InfoInk()
	if own {
		linkCol = ColorTextPrimary()
	}
	clipTop, clipBottom := list.Y, list.Y+list.H
	ascent := int32(fonts.body.Ascent())
	for li := range l.body.lines {
		line := &l.body.lines[li]
		lineY := cy + int32(li)*m.lineH
		if lineY+m.lineH < clipTop || lineY > clipBottom {
			continue
		}
		for si := range line.segs {
			seg := &line.segs[si]
			col := textCol
			if seg.url != "" {
				col = linkCol
			}
			drawTextRun(renderer, config, fonts.body, seg.text, col, bubble.X+m.padX+seg.x, lineY)
			if seg.url == "" {
				continue
			}
			setDrawColor(renderer, WithAlpha(col, 200))
			chatDstRect = sdl.Rect{X: bubble.X + m.padX + seg.x, Y: lineY + ascent + chatPx(2), W: seg.w, H: 1}
			_ = renderer.FillRect(&chatDstRect)
			hit := chatClipRect(sdl.Rect{X: bubble.X + m.padX + seg.x, Y: lineY, W: seg.w, H: m.lineH}, list)
			if hit.W > 0 && hit.H > 0 {
				chatTextLinkRects = append(chatTextLinkRects, chatLinkHit{rect: hit, url: seg.url, play: chatLinkLabelFor(seg.url).play})
			}
		}
	}
	cy += int32(len(l.body.lines)) * m.lineH
	if l.image != "" || l.imageFile >= 0 {
		if len(l.body.lines) > 0 || msg.Quote != nil {
			cy += chatPx(8)
		}
		// A picture among the files is drawn from this device's copy when
		// there is one, else from the channel's link (chatFileImageSource).
		src := l.image
		if l.imageFile >= 0 && l.imageFile < len(msg.Files) {
			f := msg.Files[l.imageFile]
			src = chatFileImageSource(f)
			if src == f.URL && chatLinkExpired(src, time.Now()) {
				// Discord's links expire after about a day: an old picture
				// asks for a fresh one, and is drawn from it once it lands.
				chatRenewLink(tab, msg.ID, l.imageFile, f, false, nil)
			}
		}
		box := sdl.Rect{X: bubble.X + m.padX, Y: cy, W: min32(m.imageW, inner), H: m.imageH}
		chatDrawImageBox(renderer, config, fonts.meta, box, src, list)
		cy += m.imageH
	}
	if len(l.files) > 0 {
		if cy > bubble.Y+m.padY {
			cy += chatPx(8)
		}
		cardW := min32(inner, chatPx(360))
		for ci, fi := range l.files {
			r := sdl.Rect{X: bubble.X + m.padX, Y: cy + int32(ci)*(m.cardH+m.cardGap), W: cardW, H: m.cardH}
			if fi < 0 || fi >= len(msg.Files) || r.Y > clipBottom || r.Y+r.H < clipTop {
				continue
			}
			drawChatFileCard(renderer, config, fonts.meta, r, msg, fi)
			if hit := chatClipRect(r, list); hit.W > 0 && hit.H > 0 {
				chatFileRects = append(chatFileRects, chatFileHit{rect: hit, tab: tab, id: msg.ID, index: fi})
			}
		}
		cy += int32(len(l.files))*m.cardH + int32(len(l.files)-1)*m.cardGap
	}
	if len(l.cards) > 0 {
		if cy > bubble.Y+m.padY {
			cy += chatPx(8)
		}
		cardW := min32(inner, chatPx(360))
		for ci, link := range l.cards {
			r := sdl.Rect{X: bubble.X + m.padX, Y: cy + int32(ci)*(m.cardH+m.cardGap), W: cardW, H: m.cardH}
			if r.Y > clipBottom || r.Y+r.H < clipTop {
				continue
			}
			drawChatLinkCard(renderer, config, fonts.body, fonts.meta, r, link)
			if hit := chatClipRect(r, list); hit.W > 0 && hit.H > 0 {
				chatLinkRects = append(chatLinkRects, chatLinkHit{rect: hit, url: link, play: chatLinkLabelFor(link).play})
			}
		}
	}
	if it.status != "" {
		col := ColorTextTertiary()
		if msg.Error != "" {
			col = WarningInk()
		}
		sw, _ := measureText(fonts.meta, it.status)
		sx := bubble.X
		if own {
			sx = bubble.X + bubble.W - sw
		}
		chatText(renderer, config, fonts.meta, it.status, col, sx, bubble.Y+bubble.H+chatPx(3))
	}
	if body := chatClipRect(bubble, list); body.W > 0 && body.H > 0 {
		chatMessageHits = append(chatMessageHits, chatMessageHit{
			index:      it.idx,
			body:       body,
			avatar:     avatarHit,
			name:       nameHit,
			sender:     msg.Sender,
			userID:     msg.UserID,
			ownMessage: own,
		})
	}
}

// chatDrawAIAvatar is Juka's avatar: the sparkles glyph on an accent disc.
func chatDrawAIAvatar(renderer *sdl.Renderer, r sdl.Rect) {
	chatFillDisc(renderer, r.X, r.Y, r.W, glassAccent())
	g := r.W * 3 / 5
	DrawHomeIcon(renderer, "sparkles", sdl.Rect{X: r.X + (r.W-g)/2, Y: r.Y + (r.H-g)/2, W: g, H: g}, true)
}

// chatDrawImageBox draws a picture inside a fixed box: the box is reserved
// before the picture has loaded, so the history does not jump when it does.
func chatDrawImageBox(renderer *sdl.Renderer, config *Config, font *ttf.Font, box sdl.Rect, url string, list sdl.Rect) {
	if box.Y > list.Y+list.H || box.Y+box.H < list.Y {
		return
	}
	radius := chatPx(12)
	tex := getChatImageTexture(renderer, url)
	if tex == nil {
		chatFillRound(renderer, box, radius, glassA(12))
		icon := chatPx(28)
		DrawHomeIcon(renderer, "image", sdl.Rect{X: box.X + (box.W-icon)/2, Y: box.Y + (box.H-icon)/2, W: icon, H: icon}, false)
	} else {
		chatFillRound(renderer, box, radius, glassA(8))
		dw, dh := box.W, box.H
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			dh = box.W * th / tw
			if dh > box.H {
				dh = box.H
				dw = box.H * tw / th
			}
		}
		chatDstRect = sdl.Rect{X: box.X + (box.W-dw)/2, Y: box.Y + (box.H-dh)/2, W: dw, H: dh}
		_ = renderer.Copy(tex, nil, &chatDstRect)
	}
	if hit := chatClipRect(box, list); hit.W > 0 && hit.H > 0 {
		chatImageRects = append(chatImageRects, chatImageHit{rect: hit, url: url})
	}
}

// chatDrawStream draws the answer Juka is writing: three breathing dots until
// the first words arrive, then the words themselves.
func chatDrawStream(renderer *sdl.Renderer, config *Config, fonts chatFonts, m chatMetrics, list sdl.Rect, it *chatItem, lay *chatBubbleLayout, now uint64) {
	bx := list.X + m.side + m.avatar + m.avatarGap
	hy := it.top + (m.headerH-m.metaH)/2 - chatPx(1)
	chatText(renderer, config, fonts.meta, chatAISender, glassAccent(), bx+chatPx(4), hy)
	bubble := sdl.Rect{X: bx, Y: it.top + it.bubbleY, W: it.bubbleW, H: it.bubbleH}
	chatDrawAIAvatar(renderer, sdl.Rect{X: list.X + m.side, Y: bubble.Y, W: m.avatar, H: m.avatar})
	fill, edge := chatBubbleColors(false, true, false, false)
	chatFillRound(renderer, bubble, m.radius, fill)
	chatStrokeRound(renderer, bubble, m.radius, edge)
	if lay == nil {
		d := chatPx(8)
		spacing := chatPx(14)
		startX := bubble.X + (bubble.W-(2*spacing+d))/2
		cy := bubble.Y + (bubble.H-d)/2
		for k := 0; k < 3; k++ {
			phase := float64((now+uint64(k)*180)%1050) / 1050
			alpha := 0.3 + 0.7*math.Abs(math.Sin(phase*math.Pi))
			chatFillDisc(renderer, startX+int32(k)*spacing, cy, d, WithAlpha(ColorTextSecondary(), uint8(alpha*255)))
		}
		return
	}
	cy := bubble.Y + m.padY
	for li := range lay.body.lines {
		lineY := cy + int32(li)*m.lineH
		if lineY+m.lineH < list.Y || lineY > list.Y+list.H {
			continue
		}
		for _, seg := range lay.body.lines[li].segs {
			drawTextRun(renderer, config, fonts.body, seg.text, ColorTextPrimary(), bubble.X+m.padX+seg.x, lineY)
		}
	}
}

// chatDrawIntro draws the start of a conversation above its oldest message.
func chatDrawIntro(renderer *sdl.Renderer, config *Config, fonts chatFonts, list sdl.Rect, top, h int32, tab chatTab) {
	if top+h < list.Y || top > list.Y+list.H {
		return
	}
	badge := chatPx(44)
	cx := list.X + list.W/2
	b := sdl.Rect{X: cx - badge/2, Y: top + chatPx(8), W: badge, H: badge}
	DrawHomeIcon(renderer, chatTabIcon(tab), drawGlassBadge(renderer, b), false)
	title, sub := chatIntroText(tab)
	tw, _ := measureText(fonts.tab, title)
	ty := b.Y + badge + chatPx(8)
	chatText(renderer, config, fonts.tab, title, ColorTextPrimary(), cx-tw/2, ty)
	sub = ellipsize(fonts.meta, sub, list.W-chatPx(40))
	sw, _ := measureText(fonts.meta, sub)
	chatText(renderer, config, fonts.meta, sub, ColorTextTertiary(), cx-sw/2, ty+int32(fonts.tab.Height())+chatPx(2))
}

// chatIntroText is what the start of each conversation says.
func chatIntroText(tab chatTab) (string, string) {
	switch tab {
	case chatTabAI:
		return "Juka AI", "Answers come from Groq. Juka only sees this conversation."
	case chatTabSaved:
		return "Saved", "Notes, links and files for yourself. Only this device sees them."
	}
	return chatCommunityTitle(), "The start of the JukaHub community chat."
}

// chatDrawEmpty draws an empty tab: what it is for, and for Juka AI a few
// questions to start with.
func chatDrawEmpty(renderer *sdl.Renderer, config *Config, fonts chatFonts, list sdl.Rect, tab chatTab, m chatMetrics) {
	title, body := "Say hi to the community", "Messages and files from the JukaHub Discord show up here."
	switch tab {
	case chatTabAI:
		title, body = "Ask Juka anything", "Tips, help with settings — or attach a picture, a video or a log."
	case chatTabSaved:
		title, body = "Notes to self", "Save links, files and ideas. Only this device can see them."
	default:
		if chatNet() == chatNetUnconfigured {
			body = "Community chat isn't set up on this device."
		}
	}
	badge := chatPx(52)
	chipH := chatPx(40)
	chipGap := chatPx(8)
	total := badge + chatPx(14) + int32(fonts.title.Height()) + chatPx(6) + int32(fonts.meta.Height())
	showChips := tab == chatTabAI && chatHasGroqKey()
	if showChips {
		total += chatPx(20) + int32(chatSuggestionCount)*(chipH+chipGap) - chipGap
	}
	top := list.Y + (list.H-total)/2
	if top < list.Y {
		top = list.Y
	}
	cx := list.X + list.W/2
	b := sdl.Rect{X: cx - badge/2, Y: top, W: badge, H: badge}
	DrawHomeIcon(renderer, chatTabIcon(tab), drawGlassBadge(renderer, b), false)
	y := b.Y + badge + chatPx(14)
	tw, _ := measureText(fonts.title, title)
	chatText(renderer, config, fonts.title, title, ColorTextPrimary(), cx-tw/2, y)
	y += int32(fonts.title.Height()) + chatPx(6)
	body = ellipsize(fonts.meta, body, list.W-chatPx(40))
	bw, _ := measureText(fonts.meta, body)
	chatText(renderer, config, fonts.meta, body, ColorTextTertiary(), cx-bw/2, y)
	if !showChips {
		return
	}
	y += int32(fonts.meta.Height()) + chatPx(20)
	for k := 0; k < chatSuggestionCount; k++ {
		label := chatSuggestions[k]
		lw, _ := measureText(fonts.meta, label)
		w := lw + chatPx(36)
		r := sdl.Rect{X: cx - w/2, Y: y, W: w, H: chipH}
		focused := chatSuggestFocus == k && chatPadOnContent()
		if focused {
			drawGlassFocusGlow(renderer, r)
		}
		chatFillRound(renderer, r, chipH/2, glassA(14))
		if focused {
			drawGlassFocusRing(renderer, r, chipH/2)
		} else {
			chatStrokeRound(renderer, r, chipH/2, glassA(30))
		}
		chatTextCentered(renderer, config, fonts.meta, label, ColorTextPrimary(), r.X+(w-lw)/2, r)
		chatView.suggestions[k] = r
		y += chipH + chipGap
	}
}

// ── Composer ─────────────────────────────────────────────────────────────

// chatPlaceholderCache keeps the community placeholder for the channel name it
// was built from.
var chatPlaceholderCache struct {
	title string
	text  string
}

// chatPlaceholder is the composer's hint for a tab.
func chatPlaceholder(tab chatTab) string {
	if chatStagedCount(tab) > 0 {
		// Files are waiting: words are optional now.
		if tab == chatTabAI {
			return visionPlaceholder(chatStagedFiles(tab))
		}
		return "Add a message…"
	}
	switch tab {
	case chatTabAI:
		return "Ask Juka anything…"
	case chatTabSaved:
		return "Save a note or a link…"
	}
	label := chatCommunityTitle()
	if label != chatPlaceholderCache.title || chatPlaceholderCache.text == "" {
		chatPlaceholderCache.title = label
		chatPlaceholderCache.text = "Message the community…"
		if strings.HasPrefix(label, "#") {
			chatPlaceholderCache.text = "Message " + label
		}
	}
	return chatPlaceholderCache.text
}

// chatComposerShown remembers the last tail-fitted draft, so the composer is
// not re-measured every frame while nothing is typed.
var chatComposerShown struct {
	src  string
	maxW int32
	font *ttf.Font
	out  string
	w    int32
}

// chatComposerText is the end of the draft that fits maxW (newlines drawn as
// spaces), with a leading ellipsis when the start was cut.
func chatComposerText(font *ttf.Font, draft string, maxW int32) (string, int32) {
	c := &chatComposerShown
	if c.font == font && c.maxW == maxW && c.src == draft {
		return c.out, c.w
	}
	shown := strings.ReplaceAll(draft, "\n", " ")
	shown = sanitizeForFont(font, shown)
	w, _ := measureTextRaw(font, shown)
	if w > maxW {
		runes := []rune(shown)
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi) / 2
			cand := "…" + string(runes[mid:])
			cw, _ := measureTextRaw(font, cand)
			if cw <= maxW {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		shown = "…" + string(runes[lo:])
		w, _ = measureTextRaw(font, shown)
	}
	c.src, c.maxW, c.font, c.out, c.w = draft, maxW, font, shown, w
	return shown, w
}

// chatDrawComposer draws the compose bar.
func chatDrawComposer(renderer *sdl.Renderer, config *Config, fonts chatFonts, rect sdl.Rect, tab chatTab, now uint64) {
	h := rect.H
	btn := h - chatPx(6)
	gap := chatPx(10)
	accent := glassAccent()
	hasText := chatHasDraft()

	send := sdl.Rect{X: rect.X + rect.W - btn, Y: rect.Y + (h-btn)/2, W: btn, H: btn}
	chatSendBtnRect = send
	arrowCol := ColorTextTertiary()
	if hasText {
		chatFillDisc(renderer, send.X, send.Y, send.W, accent)
		arrowCol = contrastText(accent)
	} else {
		fill := glassA(14)
		if chatHover(send) {
			fill = glassA(22)
		}
		chatFillDisc(renderer, send.X, send.Y, send.W, fill)
	}
	aw, ah := measureText(fonts.title, "↑")
	chatText(renderer, config, fonts.title, "↑", arrowCol, send.X+(send.W-aw)/2, send.Y+(send.H-ah)/2)

	// The paperclip: every tab takes files (Juka AI reads text files and
	// looks at pictures and videos).
	x := rect.X
	attach := sdl.Rect{X: x, Y: rect.Y + (h-btn)/2, W: btn, H: btn}
	chatAttachBtnRect = attach
	fill := glassA(12)
	if chatHover(attach) {
		fill = glassA(22)
	}
	chatFillDisc(renderer, attach.X, attach.Y, attach.W, fill)
	icon := btn / 2
	DrawHomeIcon(renderer, "paperclip", sdl.Rect{X: attach.X + (btn-icon)/2, Y: attach.Y + (btn-icon)/2, W: icon, H: icon}, false)
	x += btn + gap

	input := sdl.Rect{X: x, Y: rect.Y, W: send.X - gap - x, H: h}
	if input.W <= 0 {
		return
	}
	chatInputRect = input
	typing := chatComposerInline || (virtualKeyboardActive && vkTarget == vkTargetChat)
	focused := !typing && chatSelected < 0 && chatSuggestFocus < 0 && chatTrayFocus < 0 && chatPadOnContent() && chatWasVisible
	if focused {
		drawGlassFocusGlow(renderer, input)
	}
	chatFillRound(renderer, input, h/2, glassA(12))
	switch {
	case typing:
		strokeRoundedRect(renderer, input.X, input.Y, input.W, input.H, h/2, 2, accent)
	case focused:
		drawGlassFocusRing(renderer, input, h/2)
	default:
		chatStrokeRound(renderer, input, h/2, glassA(30))
	}
	tx := input.X + chatPx(20)
	maxW := input.W - chatPx(40)
	ty := input.Y + (h-int32(fonts.composer.Height()))/2
	draft := chatInputText
	preview := false
	if live, _, ok := vkVoiceDisplayText(); ok {
		draft, preview = live, true
	}
	if draft == "" {
		chatText(renderer, config, fonts.composer, ellipsize(fonts.composer, chatPlaceholder(tab), maxW), ColorTextTertiary(), tx, ty)
		if typing && (now/530)%2 == 0 {
			setDrawColor(renderer, accent)
			chatDstRect = sdl.Rect{X: tx, Y: ty, W: 2, H: int32(fonts.composer.Height())}
			_ = renderer.FillRect(&chatDstRect)
		}
		return
	}
	shown, sw := chatComposerText(fonts.composer, draft, maxW-chatPx(4))
	ink := ColorTextPrimary()
	if preview {
		ink = ColorTextSecondary()
	}
	drawTextRun(renderer, config, fonts.composer, shown, ink, tx, ty)
	if typing && !preview && (now/530)%2 == 0 {
		setDrawColor(renderer, accent)
		chatDstRect = sdl.Rect{X: tx + sw + chatPx(2), Y: ty, W: 2, H: int32(fonts.composer.Height())}
		_ = renderer.FillRect(&chatDstRect)
	}
}

// chatDrawJumpPill shows the way back to the newest message (and how many
// arrived) while the reader is scrolled back.
func chatDrawJumpPill(renderer *sdl.Renderer, config *Config, font *ttf.Font, list sdl.Rect, unread int) {
	if font == nil {
		return
	}
	label := chatJumpText.get(unread, func(n int) string {
		switch {
		case n <= 0:
			return "↓  Jump to latest"
		case n == 1:
			return "↓  1 new message"
		}
		return "↓  " + strconv.Itoa(n) + " new messages"
	})
	lw, lh := measureText(font, label)
	h := lh + chatPx(16)
	w := lw + chatPx(32)
	r := sdl.Rect{X: list.X + (list.W-w)/2, Y: list.Y + list.H - h - chatPx(10), W: w, H: h}
	accent := glassAccent()
	chatFillRound(renderer, r, h/2, accent)
	chatText(renderer, config, font, label, contrastText(accent), r.X+(w-lw)/2, r.Y+(h-lh)/2)
	chatJumpRect = r
}
