package main

// chatfilesview.go — files on the Chat screen: the cards a message's files
// are drawn as, and the tray of files waiting above the composer.
//
//	│ (A) Alex 14:03                                              │
//	│     [ (zip) pokemon-hack.zip      2.4 MB · ZIP     SAVE ]   │  file card
//	│ ( (txt) crash.log 14 KB × ) ( (img) shot.png 1.2 MB × )     │  tray
//	│ (📎) ( Add a message…                                 ) (↑) │  composer
//
// The tray is part of the pad's walk through the screen: Up from the composer
// reaches it, Left/Right pick a file, A takes one off, Down goes back to the
// composer. A card is a target of its own: a click (or A in the message's
// actions) opens the file, plays it, or saves it into Downloads.

import (
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// chatTrayFocus is the waiting file the pad is on, -1 when it is elsewhere.
var chatTrayFocus = -1

// chatFileHit is one file card drawn in a bubble.
type chatFileHit struct {
	rect  sdl.Rect
	tab   chatTab
	id    string
	index int
}

var chatFileRects []chatFileHit

// chatTrayHit is one chip of the tray, with its remove button.
type chatTrayHit struct {
	rect   sdl.Rect
	remove sdl.Rect
	index  int
}

var chatTrayHits []chatTrayHit

// chatTrayMore is the "+N" after the last chip that fits, and the first file
// it stands for: clicking it moves the row on.
var (
	chatTrayMore      sdl.Rect
	chatTrayMoreIndex int
)

// chatBarWidths and chatTrayWidths are scratch for laying out a row of pills
// without allocating every frame.
var chatBarWidths, chatTrayWidths []int32

// chatPercentLabels are "0%" … "100%", and the card lines made of them, built
// once: a transfer redraws them every frame.
var chatPercentLabels, chatUploadingLabels, chatDownloadingLabels = func() ([101]string, [101]string, [101]string) {
	var pct, up, down [101]string
	for i := range pct {
		pct[i] = strconv.Itoa(i) + "%"
		up[i] = "Uploading " + pct[i]
		down[i] = "Downloading " + pct[i]
	}
	return pct, up, down
}()

// chatPercentIndex is a 0..1 fraction as a whole percentage.
func chatPercentIndex(frac float64) int {
	i := int(uiClamp01(frac)*100 + 0.5)
	if i < 0 {
		i = 0
	}
	if i > 100 {
		i = 100
	}
	return i
}

// chatPercentLabel is a 0..1 fraction as a percentage.
func chatPercentLabel(frac float64) string {
	return chatPercentLabels[chatPercentIndex(frac)]
}

// chatMoreLabels are the tray's "+1" … "+10".
var chatMoreLabels = func() [chatFilesMax + 1]string {
	var out [chatFilesMax + 1]string
	for i := range out {
		out[i] = "+" + strconv.Itoa(i)
	}
	return out
}()

// vkChatFilesTitle is the on-screen keyboard's title with files attached
// ("MESSAGE · 3 FILES", vkeyboard.go).
var vkChatFilesTitle chatCachedText

// chatUploadStatusCache keeps the line under an uploading message for the
// percentage and size it was built from.
var chatUploadStatusCache struct {
	pct   int
	total int64
	text  string
}

// chatUploadStatus is the line under a message whose files are going up:
// "Uploading 45% · 2.1 of 4.6 MB". It changes with the percentage only.
func chatUploadStatus(sent, total int64) string {
	if total <= 0 {
		return "Uploading…"
	}
	pct := chatPercentIndex(float64(sent) / float64(total))
	c := &chatUploadStatusCache
	if c.text == "" || c.pct != pct || c.total != total {
		c.pct, c.total = pct, total
		c.text = "Uploading " + chatPercentLabels[pct] + " · " + chatSizeLabel(sent) + " of " + chatSizeLabel(total)
	}
	return c.text
}

// chatRowWidth is the width of a row of pills with gap between them.
func chatRowWidth(widths []int32, gap int32) int32 {
	total := int32(0)
	for i, w := range widths {
		if i > 0 {
			total += gap
		}
		total += w
	}
	return total
}

// chatRowStart is the first pill of a row to draw so that the focused one is
// on screen: 0 while everything up to it fits, later once the focus has
// moved past the edge.
func chatRowStart(widths []int32, gap, avail int32, focus int) int {
	if focus <= 0 || focus >= len(widths) {
		return 0
	}
	for start := 0; start < focus; start++ {
		if chatRowWidth(widths[start:focus+1], gap) <= avail {
			return start
		}
	}
	return focus
}

// ── The tray ─────────────────────────────────────────────────────────────

// chatTrayMove moves the pad along the tray. It reports false when the pad is
// not on the tray, so Left/Right switch tabs as everywhere else.
func chatTrayMove(delta int) bool {
	if chatTrayFocus < 0 {
		return false
	}
	n := chatStagedCount(chatActiveTabID())
	if n == 0 {
		chatTrayFocus = -1
		return false
	}
	next := clampIndex(chatTrayFocus+delta, n)
	if next != chatTrayFocus {
		chatTrayFocus = next
		PlayNavSound()
	}
	return true
}

// chatTrayLeave takes the pad off the tray, back to the composer (B).
func chatTrayLeave() bool {
	if chatTrayFocus < 0 {
		return false
	}
	chatTrayFocus = -1
	PlayBackSound()
	return true
}

// chatTrayRemove takes waiting file idx of the tab on screen off the message.
func chatTrayRemove(idx int) bool {
	tab := chatActiveTabID()
	files := chatStagedFiles(tab)
	if idx < 0 || idx >= len(files) {
		return false
	}
	name := files[idx].Name
	if !chatUnstage(tab, idx) {
		return false
	}
	PlayBackSound()
	showToast("Removed "+name, ToastInfo())
	return true
}

// chatTrayRemoveFocused takes the file under the pad off the message; the pad
// stays on the tray while files are left in it.
func chatTrayRemoveFocused() bool {
	if chatTrayFocus < 0 {
		return false
	}
	if !chatTrayRemove(chatTrayFocus) {
		chatTrayFocus = -1
		return false
	}
	switch n := chatStagedCount(chatActiveTabID()); {
	case n == 0:
		chatTrayFocus = -1
	case chatTrayFocus >= n:
		chatTrayFocus = n - 1
	}
	return true
}

// handleChatTrayClick handles a click on the tray: × takes the file off, the
// chip itself puts the pad's focus on it.
func handleChatTrayClick(mx, my int32) bool {
	for _, hit := range chatTrayHits {
		if hit.remove.W > 0 && pointInRect(mx, my, hit.remove) {
			chatTrayRemove(hit.index)
			chatTrayFocus = -1
			return true
		}
		if pointInRect(mx, my, hit.rect) {
			// The focus moves here from wherever it was: one place at a time.
			clearChatSelection()
			chatSuggestFocus = -1
			chatTrayFocus = hit.index
			PlayNavSound()
			return true
		}
	}
	if chatTrayMore.W > 0 && pointInRect(mx, my, chatTrayMore) {
		// "+N": the row moves on to the first file out of sight.
		clearChatSelection()
		chatSuggestFocus = -1
		chatTrayFocus = chatTrayMoreIndex
		PlayNavSound()
		return true
	}
	return false
}

// chatTrayHeight is the room the tray takes above the composer.
func chatTrayHeight(font *ttf.Font) int32 {
	if font == nil {
		return 0
	}
	return int32(font.Height()) + chatPx(18)
}

// chatNameLabelKey is one cut-to-fit file name.
type chatNameLabelKey struct {
	name string
	maxW int32
	font *ttf.Font
}

// chatNameLabels caches file names cut to fit a chip or a card. Render thread
// only; dropped whole when full.
var chatNameLabels = make(map[chatNameLabelKey]string, 16)

// chatNameLabel is name as it fits maxW in font.
func chatNameLabel(font *ttf.Font, name string, maxW int32) string {
	key := chatNameLabelKey{name: name, maxW: maxW, font: font}
	if s, ok := chatNameLabels[key]; ok {
		return s
	}
	if len(chatNameLabels) >= 256 {
		chatNameLabels = make(map[chatNameLabelKey]string, 16)
	}
	s := ellipsize(font, sanitizeCached(font, name), maxW)
	chatNameLabels[key] = s
	return s
}

// chatSizeTexts caches chatSizeLabel for the chips.
var chatSizeTexts = make(map[int64]string, 16)

func chatSizeText(n int64) string {
	if s, ok := chatSizeTexts[n]; ok {
		return s
	}
	if len(chatSizeTexts) >= 256 {
		chatSizeTexts = make(map[int64]string, 16)
	}
	s := chatSizeLabel(n)
	chatSizeTexts[n] = s
	return s
}

// chatDrawTray draws the files waiting to go with the next message: one chip
// per file with its type, name and size, and a × that takes it off.
func chatDrawTray(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, tab chatTab) {
	chatTrayHits = chatTrayHits[:0]
	chatTrayMore = sdl.Rect{}
	files := chatStagedFiles(tab)
	if renderer == nil || font == nil || len(files) == 0 || rect.W <= 0 {
		return
	}
	if chatTrayFocus >= len(files) {
		chatTrayFocus = len(files) - 1
	}
	h := rect.H
	gap := chatPx(8)
	pad := chatPx(12)
	icon := chatPx(18)
	cross := chatPx(14)
	maxName := chatPx(200)
	widths := chatTrayWidths[:0]
	for _, f := range files {
		nw, _ := measureText(font, chatNameLabel(font, f.Name, maxName))
		sw, _ := measureText(font, chatSizeText(f.Size))
		widths = append(widths, pad+icon+chatPx(8)+nw+chatPx(8)+sw+chatPx(10)+cross+pad)
	}
	chatTrayWidths = widths
	focus := -1
	if chatTrayFocus >= 0 && chatPadOnContent() {
		focus = chatTrayFocus
	}
	right := rect.X + rect.W
	x := rect.X
	start := chatRowStart(widths, gap, rect.W, focus)
	if start > 0 {
		// Files before the first chip drawn, as in the action bar.
		dots, _ := measureText(font, "…")
		chatTextCentered(renderer, config, font, "…", ColorTextTertiary(), x, rect)
		x += dots + gap
		if s := chatRowStart(widths, gap, right-x, focus); s > start {
			start = s
		}
	}
	for i := start; i < len(files); i++ {
		w := widths[i]
		if x+w > right {
			// More files than room: say how many are out of sight; a click
			// on it (or Right) moves the row on.
			rest := chatMoreLabels[min(len(files)-i, chatFilesMax)]
			if rw, _ := measureText(font, rest); x+rw <= right {
				chatTextCentered(renderer, config, font, rest, ColorTextTertiary(), x, rect)
				chatTrayMore = sdl.Rect{X: x - chatPx(4), Y: rect.Y, W: rw + chatPx(8), H: rect.H}
				chatTrayMoreIndex = i
			}
			break
		}
		f := files[i]
		r := sdl.Rect{X: x, Y: rect.Y, W: w, H: h}
		focused := i == focus
		if focused {
			drawGlassFocusGlow(renderer, r)
		}
		fill := glassA(14)
		if chatHover(r) {
			fill = glassA(22)
		}
		chatFillRound(renderer, r, h/2, fill)
		if focused {
			drawGlassFocusRing(renderer, r, h/2)
		} else {
			chatStrokeRound(renderer, r, h/2, glassA(30))
		}
		ix := r.X + pad
		DrawHomeIcon(renderer, chatFileIcon(f), sdl.Rect{X: ix, Y: r.Y + (h-icon)/2, W: icon, H: icon}, false)
		tx := ix + icon + chatPx(8)
		nw := chatTextCentered(renderer, config, font, chatNameLabel(font, f.Name, maxName), ColorTextPrimary(), tx, r)
		chatTextCentered(renderer, config, font, chatSizeText(f.Size), ColorTextTertiary(), tx+nw+chatPx(8), r)
		rm := sdl.Rect{X: r.X + r.W - pad - cross, Y: r.Y + (h-cross)/2, W: cross, H: cross}
		drawHomeGlyphTinted(renderer, "close", rm, ColorTextSecondary())
		chatTrayHits = append(chatTrayHits, chatTrayHit{rect: r, remove: chatInsetRect(rm, chatPx(6)), index: i})
		x += w + gap
	}
}

// ── File cards ───────────────────────────────────────────────────────────

// chatFileCardView is what a file card says right now.
type chatFileCardView struct {
	meta     string
	action   string
	ink      sdl.Color
	progress float64 // 0..1 while the file is moving, -1 otherwise
}

// chatFileActionWord is the word at a card's right end, the action bar's word
// (chatFileOpenLabel) in capitals.
func chatFileActionWord(f ChatFile) string {
	switch chatFileKindOf(f) {
	case chatFileImage:
		return "VIEW"
	case chatFileAudio, chatFileVideo:
		return "PLAY"
	}
	return "OPEN"
}

// chatFileSavedMetas caches the meta line of a file saved from the channel.
var chatFileSavedMetas = make(map[chatFileMetaKey]string, 16)

func chatFileSavedMeta(f ChatFile) string {
	key := chatFileMetaKey{name: f.Name, size: f.Size}
	if s, ok := chatFileSavedMetas[key]; ok {
		return s
	}
	if len(chatFileSavedMetas) >= 256 {
		chatFileSavedMetas = make(map[chatFileMetaKey]string, 16)
	}
	s := chatFileMeta(f) + " · on this device"
	chatFileSavedMetas[key] = s
	return s
}

// chatFileShare is how far file idx of an upload has gone, worked out from
// the bytes of the whole request: the files go up one after another.
func chatFileShare(files []ChatFile, idx int, sent, total int64) float64 {
	if idx < 0 || idx >= len(files) || total <= 0 {
		return 0
	}
	sum := int64(0)
	for _, f := range files {
		sum += f.Size
	}
	size := files[idx].Size
	if sum <= 0 || size <= 0 {
		return 0
	}
	// The request is the files and a few hundred bytes of headers.
	done := float64(sent) / float64(total) * float64(sum)
	before := int64(0)
	for i := 0; i < idx; i++ {
		before += files[i].Size
	}
	return uiClamp01((done - float64(before)) / float64(size))
}

// chatFileCardState works out a card's second line and its action. It runs
// for every card on screen every frame, so the steady states are cached
// strings.
func chatFileCardState(msg *ChatMessage, idx int) chatFileCardView {
	f := msg.Files[idx]
	v := chatFileCardView{meta: chatFileMeta(f), ink: InfoInk(), progress: -1}
	if msg.Pending {
		if sent, total, ok := chatUploadProgress(msg.ID); ok {
			v.progress = chatFileShare(msg.Files, idx, sent, total)
			v.meta = chatUploadingLabels[chatPercentIndex(v.progress)]
			return v
		}
	}
	if got, total, ok := chatDownloadProgress(msg.ID, idx); ok {
		v.progress = 0
		if total > 0 {
			v.progress = uiClamp01(float64(got) / float64(total))
		}
		v.meta = chatDownloadingLabels[chatPercentIndex(v.progress)]
		return v
	}
	switch {
	case chatFileLocal(f) != "":
		v.action = chatFileActionWord(f)
		if !chatIsOwn(*msg) {
			v.meta = chatFileSavedMeta(f)
		}
	case f.URL != "":
		v.action = chatFileActionWord(f)
	default:
		v.meta = "Not on this device any more"
	}
	if v.action == "PLAY" {
		v.ink = glassAccent()
	}
	return v
}

// drawChatFileCard draws one file of a message: its type, name and size (or
// how far it has moved), and what a click does.
func drawChatFileCard(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, msg *ChatMessage, idx int) {
	if renderer == nil || font == nil || msg == nil || idx < 0 || idx >= len(msg.Files) {
		return
	}
	f := msg.Files[idx]
	radius := min32(chatPx(12), rect.H/2)
	fill := glassA(12)
	if chatHover(rect) {
		fill = glassA(20)
	}
	chatFillRound(renderer, rect, radius, fill)
	chatStrokeRound(renderer, rect, radius, glassA(28))

	pad := chatPx(6)
	tile := rect.H - pad*2
	tileRect := sdl.Rect{X: rect.X + pad, Y: rect.Y + pad, W: tile, H: tile}
	chatFillRound(renderer, tileRect, chatPx(8), WithAlpha(glassAccent(), 40))
	glyph := tile * 3 / 5
	DrawHomeIcon(renderer, chatFileIcon(f), sdl.Rect{X: tileRect.X + (tile-glyph)/2, Y: tileRect.Y + (tile-glyph)/2, W: glyph, H: glyph}, false)

	v := chatFileCardState(msg, idx)
	actionX := rect.X + rect.W - chatPx(12)
	if v.action != "" {
		aw, _ := measureText(font, v.action)
		actionX -= aw
		drawTextRun(renderer, config, font, v.action, v.ink, actionX, rect.Y+(rect.H-int32(font.Height()))/2)
	}
	textX := tileRect.X + tile + chatPx(10)
	if textW := actionX - textX - chatPx(10); textW > 0 {
		lineH := int32(font.Height())
		top := rect.Y + (rect.H-2*lineH)/2
		drawTextRun(renderer, config, font, chatNameLabel(font, f.Name, textW), ColorTextPrimary(), textX, top)
		drawTextRun(renderer, config, font, chatNameLabel(font, v.meta, textW), ColorTextTertiary(), textX, top+lineH)
	}
	if v.progress >= 0 {
		track := sdl.Rect{X: rect.X + radius, Y: rect.Y + rect.H - chatPx(5), W: rect.W - 2*radius, H: max32(2, chatPx(3))}
		chatFillRound(renderer, track, track.H/2, glassA(24))
		if w := int32(float64(track.W) * v.progress); w > 0 {
			chatFillRound(renderer, sdl.Rect{X: track.X, Y: track.Y, W: w, H: track.H}, track.H/2, glassAccent())
		}
	}
}

// handleChatFileClick opens the file card under the cursor.
func handleChatFileClick(mx, my int32, config *Config) bool {
	// An open action bar owns the clicks it covers, like for pictures.
	if chatSelectionBar.open || chatOverlayOpen() {
		return false
	}
	for _, hit := range chatFileRects {
		if pointInRect(mx, my, hit.rect) {
			chatFileActivate(config, hit.tab, hit.id, hit.index)
			return true
		}
	}
	return false
}
