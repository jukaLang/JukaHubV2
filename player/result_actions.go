package main

// result_actions.go — the Audio / Video / Chat buttons on a video result.
//
// The Results list is a browse surface: A plays, and that used to be the only
// thing a result could do. Saving a result for offline listening meant copying
// its link into the Downloads panel, and sending one to the chat meant the same
// copy-paste. Those actions now live on the row itself, to the right of the
// title: once a row is focused, Left/Right walks the row → Audio → Video → Chat
// → row cycle, and A presses what is highlighted. Audio saves the track as m4a;
// Video saves the best picture and sound merged into an MP4.
//
// The buttons are drawn on every visible row so they are discoverable with a
// fingertip, but only the focused row answers the D-pad. Pills are recorded as
// hit rects while rendering, so a tap presses exactly the pill it landed on,
// including on a row that is not the highlighted one.
//
// The grid layout (columns > 1) keeps Left/Right for moving between cards: its
// buttons are not wired there.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	resultActionNone = iota
	resultActionAudio
	resultActionVideo
	resultActionChat
	resultActionSave
	resultActionQueue
	resultActionCount
)

// resultActionOrder is the order the pills are drawn and cycled through, left
// to right: the two downloads, then Chat, then the two list actions (Favorites
// and Up Next).
var resultActionOrder = []int{resultActionAudio, resultActionVideo, resultActionChat, resultActionSave, resultActionQueue}

// focusedResultAction is the highlighted button on the focused row, or
// resultActionNone while the row itself holds the focus.
var focusedResultAction int

// resultActionHit is one pressable pill: which action, which row, and where it
// was drawn.
type resultActionHit struct {
	action int
	index  int
	rect   sdl.Rect
}

// resultActionHits is rebuilt every frame by the list renderer.
var resultActionHits []resultActionHit

// resultEnqueueDownload and resultSendToChat are seams so the dispatch can be
// tested without a real yt-dlp process or a Discord mirror.
var (
	resultEnqueueDownload = func(url string, kind downloadKind) error {
		_, err := downloadEnqueueKind(url, kind)
		return err
	}
	resultSendToChat = func(url string) {
		sendChatMessage(chatDisplayName(), url)
	}
)

// resetResultActions puts the row focus back on the row itself.
func resetResultActions() {
	focusedResultAction = resultActionNone
}

// moveResultAction cycles row → Audio → Video → Chat → row. Both directions
// wrap, which is what makes the buttons escapable with the same d-pad key that
// entered them, rather than needing B.
func moveResultAction(current, delta int) int {
	if delta == 0 {
		return current
	}
	next := current + delta
	if next < 0 {
		next = resultActionCount - 1
	}
	if next >= resultActionCount {
		next = 0
	}
	return next
}

func resultActionLabel(action int) string {
	switch action {
	case resultActionAudio:
		return "Audio"
	case resultActionVideo:
		return "Video"
	case resultActionChat:
		return "Chat"
	case resultActionSave:
		return "Save"
	case resultActionQueue:
		return "Queue"
	}
	return ""
}

// resultActionKind is the download kind behind a pill. Chat has none.
func resultActionKind(action int) (downloadKind, bool) {
	switch action {
	case resultActionAudio:
		return downloadKindAudio, true
	case resultActionVideo:
		return downloadKindVideo, true
	}
	return downloadKindAudio, false
}

// resultActionPills lays the pills out inside a result card and returns them in
// resultActionOrder, plus the x where the title/metadata column must stop. The
// width is measured from the font so the same layout works on the 1280-wide
// panel and the 960-wide Brick, and it is reserved on every row so text does
// not reflow as the highlight moves. Audio and Video are separate because
// saving the sound is a different choice from saving the picture, and the Save
// pill is how a result lands in the Favorites library.
func resultActionPills(font *ttf.Font, cardX, cardY, cardW, cardH int32) ([]sdl.Rect, int32) {
	const (
		h     = int32(30)
		gap   = int32(8)
		inset = int32(12)
	)
	count := len(resultActionOrder)
	w := int32(76)
	if font != nil {
		widest := 0
		for _, action := range resultActionOrder {
			if tw, _, err := font.SizeUTF8(resultActionLabel(action)); err == nil && int(tw) > widest {
				widest = int(tw)
			}
		}
		// Every pill is as wide as the longest label any row can show, so a
		// saved row saying "Saved" does not resize the strip.
		for _, done := range []string{"Saved", "Queued"} {
			if tw, _, err := font.SizeUTF8(done); err == nil && int(tw) > widest {
				widest = int(tw)
			}
		}
		if measured := int32(widest + 26); measured > w {
			w = measured
		}
	}
	// The pills share the right edge, so each one is capped by the room the
	// whole strip has once the gaps and the card insets are taken out — and by
	// the title column, which keeps a width of its own. Without that second
	// limit a fifth pill squeezes the name out of a narrow row and a result
	// turns into a strip of buttons with nothing to recognise it by.
	const minTitle = int32(144) // title column plus the gap back to the pills
	if limit := (cardW - 2*inset - minTitle - gap*int32(count-1)) / int32(count); w > limit {
		w = limit
	}
	if w < 40 {
		w = 40
	}
	y := cardY + (cardH-h)/2
	rects := make([]sdl.Rect, count)
	right := cardX + cardW - inset
	for i := count - 1; i >= 0; i-- {
		rects[i] = sdl.Rect{X: right - w, Y: y, W: w, H: h}
		right -= w + gap
	}
	return rects, rects[0].X - 12
}

// drawResultActions draws the pills for one row and records their hit rects.
// The focused row's pills are brighter, and the one selected with the d-pad is
// filled with the accent. A row already in Favorites says "Saved" on the last
// pill, which is also the press that takes it back out.
func drawResultActions(r *sdl.Renderer, config *Config, font *ttf.Font, card sdl.Rect, index int, focused, saved, queued bool) int32 {
	rects, textRight := resultActionPills(font, card.X, card.Y, card.W, card.H)
	if r == nil {
		return textRight
	}
	for i, action := range resultActionOrder {
		resultActionHits = append(resultActionHits, resultActionHit{action: action, index: index, rect: rects[i]})
	}

	drawPill := func(rect sdl.Rect, action int) {
		selected := focused && focusedResultAction == action
		switch {
		case selected:
			fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, ColorAccent)
		case focused:
			fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(ColorIconSurface, 235))
			strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, WithAlpha(ColorAccent, 170))
		default:
			fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, WithAlpha(ColorIconSurface, 150))
			strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, WithAlpha(ColorBorder, 200))
		}
		color := ColorTextSecondary()
		if selected {
			// Dark ink on the accent fill: the panel's text colours are all
			// light and would vanish on cyan.
			color = sdl.Color{R: 10, G: 16, B: 24, A: 255}
		} else if focused {
			color = ColorTextPrimary()
		}
		if font == nil {
			return
		}
		label := resultActionLabel(action)
		if action == resultActionSave && saved {
			label = "Saved"
		}
		if action == resultActionQueue && queued {
			label = "Queued"
		}
		tw, th, err := font.SizeUTF8(label)
		if err != nil {
			return
		}
		renderText(r, config, font, label, color,
			rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
	}
	for i, action := range resultActionOrder {
		drawPill(rects[i], action)
	}
	return textRight
}

// resultActionHitAt maps a click to the pill under it.
func resultActionHitAt(mx, my int32) (int, int) {
	for _, hit := range resultActionHits {
		if pointInRect(mx, my, hit.rect) {
			return hit.action, hit.index
		}
	}
	return resultActionNone, -1
}

// performResultAction runs one button for one result. It reports whether the
// press was handled, so the caller can fall through to playback.
func performResultAction(action, index int, videos []VideoInfo) bool {
	kind, isDownload := resultActionKind(action)
	if !isDownload && action != resultActionChat && action != resultActionSave && action != resultActionQueue {
		return false
	}
	if index < 0 || index >= len(videos) {
		return false
	}
	video := videos[index]
	link := strings.TrimSpace(video.GetURL())
	if link == "" {
		ShowToast("That result has no link to "+resultActionVerb(action), ToastKindWarning)
		return true
	}
	switch action {
	case resultActionAudio, resultActionVideo:
		if err := resultEnqueueDownload(link, kind); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return true
		}
		// The queue row names itself once yt-dlp resolves the title, so the
		// toast only has to confirm the press landed.
		ShowToast("Downloading "+strings.ToLower(kind.label())+" — "+ellipsizeRunes(video.Title, 30), ToastKindSuccess)
	case resultActionChat:
		resultSendToChat(link)
		ShowToast("Link sent to chat", ToastKindSuccess)
	case resultActionSave:
		if toggleFavoriteVideo(video) {
			ShowToast("Saved to Favorites", ToastKindSuccess)
		} else {
			ShowToast("Removed from Favorites", ToastKindWarning)
		}
		saveFavorites()
	case resultActionQueue:
		// Up Next is the one pill whose label already reports the state, so the
		// toast only has to confirm the press and say how long the list is.
		if queued, position, err := upNextToggle(video); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			return true
		} else if queued {
			ShowToast(fmt.Sprintf("Added to Up Next · %d waiting", position), ToastKindSuccess)
		} else {
			ShowToast("Removed from Up Next", ToastKindWarning)
		}
	}
	PlaySuccessSound()
	return true
}

func resultActionVerb(action int) string {
	switch action {
	case resultActionChat:
		return "send"
	case resultActionSave:
		return "save"
	case resultActionQueue:
		return "queue"
	}
	return "download"
}

// listResultAction handles the keys the single-column list has free for the
// row's buttons. It returns false for the keys the list still needs itself, so
// the caller's normal navigation (row movement, playback, Back) runs unchanged.
func listResultAction(action Action, config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
	if !ok || len(videos) == 0 {
		resetResultActions()
		return false
	}
	switch action {
	case ActionNavigateRight:
		if next := moveResultAction(focusedResultAction, 1); next != focusedResultAction {
			focusedResultAction = next
			PlayNavSound()
		}
		return true
	case ActionNavigateLeft:
		if next := moveResultAction(focusedResultAction, -1); next != focusedResultAction {
			focusedResultAction = next
			PlayNavSound()
		}
		return true
	case ActionNavigateUp, ActionNavigateDown:
		// The first Up/Down leaves the buttons and keeps the row; the next one
		// moves through the list as usual.
		if focusedResultAction != resultActionNone {
			resetResultActions()
			PlayNavSound()
			return true
		}
		return false
	case ActionConfirm:
		if focusedResultAction == resultActionNone {
			return false
		}
		return performResultAction(focusedResultAction, focusedResultIndex, videos)
	case ActionBack:
		if focusedResultAction != resultActionNone {
			resetResultActions()
			PlayBackSound()
			return true
		}
		return false
	}
	return false
}
