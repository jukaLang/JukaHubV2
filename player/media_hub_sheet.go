package main

// media_hub_sheet.go — the Media screen's details sheet and its confirmation.
//
// Y (or the "…" button on a card, or the Application key) opens a sheet for the
// focused card: the artwork, the title wrapped in full, who made it and how far
// in the user is, and every action the card has — play or resume, play from
// the start, download the video or only its sound, extract the sound of a video
// already on the card, remove the download, favourite, Up Next, the Watch Party
// queue, copy the link. Deleting a file always goes through the small
// confirmation, which says how much space it frees.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// mediaSheetActionsFor lists the actions a card offers, with their labels. It
// is a pure function of the card and the state around it so it can be tested.
// audioSaved says the page's sound is a saved track already, so neither "Save
// audio only" nor "Extract audio" is offered: either would only refuse.
func mediaSheetActionsFor(card mediaCard, online, party, favorite, queued, audioSaved bool) ([]mediaSheetAction, []string) {
	acts := make([]mediaSheetAction, 0, 8)
	labels := make([]string, 0, 8)
	add := func(a mediaSheetAction, label string) {
		acts = append(acts, a)
		labels = append(labels, label)
	}
	if card.queued {
		if card.queueStatus == downloadFailed {
			add(mediaActRetry, "Retry download")
		}
		add(mediaActCancelDownload, "Cancel download")
		return acts, labels
	}
	if mlibCardOpens(card.mlibKind) {
		// A Library show, album or See all card: open its list, or play.
		add(mediaActOpen, "Open")
		switch card.mlibKind {
		case mlibCardShow:
			add(mediaActPlay, "Play")
		case mlibCardAlbum:
			add(mediaActPlay, "Play album")
		}
		return acts, labels
	}
	local := card.path != ""
	if card.progress > 0 && card.video.Duration > 0 {
		add(mediaActPlay, "Resume from "+formatDuration(card.progress*card.video.Duration))
		add(mediaActPlayStart, "Play from the start")
	} else {
		add(mediaActPlay, "Play")
	}
	switch {
	case local:
		// A video's sound can be kept as a track without the network.
		if mediaCardIsVideoFile(card) && !audioSaved {
			add(mediaActExtractAudio, "Extract audio")
		}
		// Only a file the Downloads library lists is removed from here; any
		// other file on the card (opened from Files) is Files' to manage.
		if card.libID != "" {
			add(mediaActRemoveDownload, "Remove from this device")
		}
	case card.downloaded:
		if !audioSaved {
			add(mediaActExtractAudio, "Extract audio")
		}
		add(mediaActRemoveDownload, "Remove download")
	case online:
		add(mediaActDownload, "Download for offline")
		if !audioSaved {
			add(mediaActDownloadAudio, "Save audio only")
		}
	}
	if local {
		add(mediaActCopy, "Copy file path")
		return acts, labels
	}
	if favorite {
		add(mediaActFavorite, "Remove from Favorites")
	} else {
		add(mediaActFavorite, "Add to Favorites")
	}
	if online {
		if queued {
			add(mediaActUpNext, "Remove from Up Next")
		} else {
			add(mediaActUpNext, "Play next (Up Next)")
		}
		if party {
			add(mediaActParty, "Share to Watch Party")
		}
	}
	add(mediaActCopy, "Copy link")
	return acts, labels
}

// mediaSheetInfo is the sheet's status line under the channel.
func mediaSheetInfo(card mediaCard) string {
	parts := make([]string, 0, 4)
	if card.duration != "" {
		parts = append(parts, card.duration)
	}
	if card.progress > 0 {
		parts = append(parts, formatPercentSimple(card.progress)+" watched")
	}
	switch {
	case card.queued:
		parts = append(parts, card.detail)
	case card.path != "":
		if card.detail != "" {
			parts = append(parts, card.detail)
		}
	case card.downloaded:
		parts = append(parts, "Downloaded")
	}
	if card.overLimit {
		parts = append(parts, "over your storage limit")
	}
	return strings.Join(parts, " · ")
}

// mediaHubOpenSheet opens the details sheet for a card.
func mediaHubOpenSheet(card mediaCard) {
	url := strings.TrimSpace(card.video.GetURL())
	acts, labels := mediaSheetActionsFor(card, IsOnline(), partyActive(), favoriteVideoSaved(url), upNextHas(url), mediaHubAudioSaved(card))
	mediaHub.sheet = mediaSheet{
		open:    true,
		card:    card,
		actions: acts,
		labels:  labels,
		info:    mediaSheetInfo(card),
	}
	PlayActivateSound()
}

// mediaHubRefreshSheet rebuilds the open sheet's actions after one ran, keeping
// the ring on the same row.
func mediaHubRefreshSheet() {
	if !mediaHub.sheet.open {
		return
	}
	card := mediaHub.sheet.card
	url := strings.TrimSpace(card.video.GetURL())
	acts, labels := mediaSheetActionsFor(card, IsOnline(), partyActive(), favoriteVideoSaved(url), upNextHas(url), mediaHubAudioSaved(card))
	focus := mediaHub.sheet.focus
	if focus >= len(acts) {
		focus = len(acts) - 1
	}
	if focus < 0 {
		focus = 0
	}
	mediaHub.sheet.actions = acts
	mediaHub.sheet.labels = labels
	mediaHub.sheet.focus = focus
}

// mediaHubSheetRun runs one sheet action.
func mediaHubSheetRun(config *Config, act mediaSheetAction) {
	card := mediaHub.sheet.card
	switch act {
	case mediaActPlay, mediaActPlayStart:
		mediaHub.sheet = mediaSheet{}
		list, idx := mediaHubFocusedList()
		mediaHubPlay(config, card, list, idx, act == mediaActPlayStart)
	case mediaActDownload:
		mediaHubDownload(card)
		mediaHub.sheet = mediaSheet{}
	case mediaActDownloadAudio:
		mediaHubDownloadAudio(card)
		mediaHub.sheet = mediaSheet{}
	case mediaActExtractAudio:
		// The sheet stays open on a refusal (its toast says why).
		if mediaHubExtractAudio(config, card) {
			mediaHub.sheet = mediaSheet{}
		}
	case mediaActRemoveDownload:
		mediaHubAskRemove(card)
	case mediaActFavorite:
		mediaHubToggleFavorite(card)
		mediaHubRefreshSheet()
	case mediaActUpNext:
		mediaHubToggleUpNext(card)
		mediaHubRefreshSheet()
	case mediaActParty:
		mediaHubShareToParty(card)
		mediaHub.sheet = mediaSheet{}
	case mediaActCopy:
		mediaHubCopyLink(card)
	case mediaActRetry:
		mediaHubQueueAction(card)
		mediaHub.sheet = mediaSheet{}
	case mediaActCancelDownload:
		mediaHub.sheet = mediaSheet{}
		mediaHubCancelTransfer(card)
	case mediaActOpen:
		mediaHub.sheet = mediaSheet{}
		mlibOpenGroup(config, card)
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────────

// mediaSheetRect is the sheet's card, centred over the screen.
func mediaSheetRect() sdl.Rect {
	w := scaleX(820, screenWidth)
	if limit := screenWidth - 2*scaleX(homeSideMargin, screenWidth); w > limit {
		w = limit
	}
	h := int32(440)
	if limit := screenHeight - headerBarHeight() - 40; h > limit {
		h = limit
	}
	return sdl.Rect{X: (screenWidth - w) / 2, Y: headerBarHeight() + (screenHeight-headerBarHeight()-h)/2, W: w, H: h}
}

// mediaHubDrawSheet paints the details sheet and records its hits.
func mediaHubDrawSheet(renderer *sdl.Renderer, config *Config) {
	s := &mediaHub.sheet
	if !s.open {
		return
	}
	drawOverlayScrim(renderer)
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitSheetScrim, rect: sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}})
	card := mediaSheetRect()
	drawGlassSheet(renderer, card, glassSheetFill())
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	// The card itself swallows clicks that miss its rows.
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitStateAction, a: -1, rect: card})

	pad := int32(22)
	leftW := card.W * 44 / 100
	art := sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: leftW - pad, H: (leftW - pad) * 9 / 16}
	if s.card.vertical {
		art.W = art.H * 9 / 16
		if art.H > card.H/2 {
			art.H = card.H / 2
			art.W = art.H * 9 / 16
		}
		art.X = card.X + pad + (leftW-pad-art.W)/2
	}
	mediaDrawCardArt(renderer, &s.card, art)
	small := getFontSized(config, "small", 13)
	if small != nil && s.card.duration != "" {
		mediaDrawDurationBadge(renderer, small, s.card.duration, art)
	}
	if s.card.progress > 0 {
		mediaDrawProgress(renderer, art, s.card.progress)
	}

	titleFont := getFontSized(config, "small", 20)
	metaFont := getFontSized(config, "small", 15)
	textX := card.X + pad
	textW := leftW - pad
	y := art.Y + art.H + 14
	if titleFont != nil {
		if s.lines == nil {
			s.lines = wrapTextLines(titleFont, s.card.title, textW, 3)
		}
		lineH := int32(titleFont.Height())
		for _, line := range s.lines {
			drawText(renderer, titleFont, line, textX, y, ColorTextPrimary(), textAlignLeft)
			y += lineH
		}
		y += 6
	}
	if metaFont != nil {
		if s.card.meta != "" {
			drawText(renderer, metaFont, ellipsize(metaFont, s.card.meta, textW), textX, y, ColorTextSecondary(), textAlignLeft)
			y += int32(metaFont.Height()) + 4
		}
		if s.info != "" {
			ink := ColorTextTertiary()
			if s.card.overLimit {
				ink = WarningInk()
			}
			drawText(renderer, metaFont, ellipsize(metaFont, s.info, textW), textX, y, ink, textAlignLeft)
		}
	}

	// Actions: a column of rows on the right.
	rowFont := getFontSized(config, "small", 17)
	colX := card.X + leftW + pad
	colW := card.X + card.W - pad - colX
	rowH := int32(44)
	gap := int32(6)
	n := int32(len(s.actions))
	if avail := card.H - 2*pad; n > 0 && n*(rowH+gap)-gap > avail {
		rowH = (avail+gap)/n - gap
	}
	rowY := card.Y + pad
	for i, label := range s.labels {
		r := sdl.Rect{X: colX, Y: rowY, W: colW, H: rowH}
		focused := i == s.focus
		UIListRow(renderer, r, glassRowRadius, focused, false)
		icon := mediaSheetIcon(s.actions[i])
		glyph := int32(22)
		iconRect := sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-glyph)/2, W: glyph, H: glyph}
		DrawHomeIcon(renderer, icon, iconRect, false)
		if rowFont != nil {
			ink := ColorTextPrimary()
			if s.actions[i] == mediaActRemoveDownload || s.actions[i] == mediaActCancelDownload {
				ink = DangerInk()
			}
			text := ellipsize(rowFont, label, r.W-glyph-44)
			_, th := measureText(rowFont, text)
			drawText(renderer, rowFont, text, iconRect.X+glyph+12, r.Y+(r.H-th)/2, ink, textAlignLeft)
		}
		mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitSheetAction, a: i, rect: r})
		rowY += rowH + gap
	}
}

// mediaSheetIcon is the glyph beside one action.
func mediaSheetIcon(act mediaSheetAction) string {
	switch act {
	case mediaActPlay:
		return "play"
	case mediaActPlayStart:
		return "refresh"
	case mediaActDownload:
		return "download"
	case mediaActDownloadAudio, mediaActExtractAudio:
		return "music"
	case mediaActRemoveDownload, mediaActCancelDownload:
		return "trash"
	case mediaActFavorite:
		return "heart"
	case mediaActUpNext:
		return "list"
	case mediaActParty:
		return "party"
	case mediaActCopy:
		return "copy"
	case mediaActRetry:
		return "refresh"
	case mediaActOpen:
		return "folder"
	}
	return "info"
}

// mediaHubDrawConfirm paints the confirmation over everything else.
func mediaHubDrawConfirm(renderer *sdl.Renderer, config *Config) {
	c := &mediaHub.confirm
	if !c.open {
		return
	}
	drawOverlayScrim(renderer)
	w := scaleX(540, screenWidth)
	if limit := screenWidth - 48; w > limit {
		w = limit
	}
	titleFont := getFontSized(config, "small", 20)
	bodyFont := getFontSized(config, "small", 15)
	btnFont := getFontSized(config, "small", 17)
	if titleFont == nil || bodyFont == nil || btnFont == nil {
		return
	}
	pad := int32(24)
	lines := mediaConfirmLines(bodyFont, c.body, w-2*pad)
	bodyH := int32(len(lines)) * int32(bodyFont.Height())
	btnH := int32(44)
	h := pad + int32(titleFont.Height()) + 10 + bodyH + 22 + btnH + pad
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	// A click anywhere outside the card cancels, and the card swallows the
	// clicks that miss its buttons: nothing drawn underneath ever runs while
	// the question is up.
	mediaHub.hits = append(mediaHub.hits,
		mediaHit{kind: mediaHitConfirm, a: -1, rect: sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}},
		mediaHit{kind: mediaHitStateAction, a: -1, rect: card})
	drawGlassSheet(renderer, card, glassSheetFill())
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)

	y := card.Y + pad
	drawText(renderer, titleFont, ellipsize(titleFont, c.title, w-2*pad), card.X+pad, y, ColorTextPrimary(), textAlignLeft)
	y += int32(titleFont.Height()) + 10
	for _, line := range lines {
		drawText(renderer, bodyFont, line, card.X+pad, y, ColorTextSecondary(), textAlignLeft)
		y += int32(bodyFont.Height())
	}
	btnW := (w - 2*pad - 14) / 2
	btnY := card.Y + h - pad - btnH
	cancel := sdl.Rect{X: card.X + pad, Y: btnY, W: btnW, H: btnH}
	ok := sdl.Rect{X: cancel.X + btnW + 14, Y: btnY, W: btnW, H: btnH}
	cancelLabel := c.cancel
	if cancelLabel == "" {
		cancelLabel = "Cancel"
	}
	UIButton(renderer, nil, cancel, cancelLabel, UIButtonStyle{Variant: UIButtonSecondary, Font: btnFont},
		UIButtonState{Focused: c.focus == 0})
	UIButton(renderer, nil, ok, c.ok, UIButtonStyle{Variant: UIButtonDanger, Font: btnFont},
		UIButtonState{Focused: c.focus == 1})
	mediaHub.hits = append(mediaHub.hits,
		mediaHit{kind: mediaHitConfirm, a: 0, rect: cancel},
		mediaHit{kind: mediaHitConfirm, a: 1, rect: ok})
}

// mediaConfirmLinesCache keeps the confirmation's wrapped body between frames.
var mediaConfirmLinesCache struct {
	text  string
	width int32
	lines []string
}

// mediaConfirmLines wraps the body once per text and width, not every frame.
func mediaConfirmLines(font *ttf.Font, text string, width int32) []string {
	if mediaConfirmLinesCache.text == text && mediaConfirmLinesCache.width == width && mediaConfirmLinesCache.lines != nil {
		return mediaConfirmLinesCache.lines
	}
	mediaConfirmLinesCache.text = text
	mediaConfirmLinesCache.width = width
	mediaConfirmLinesCache.lines = wrapTextLines(font, text, width, 3)
	return mediaConfirmLinesCache.lines
}
