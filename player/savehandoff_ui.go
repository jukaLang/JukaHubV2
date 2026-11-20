package main

// savehandoff_ui.go — the Save handoff screen, opened over a game's page from
// More → "Saves on <device>" (savehandoff.go has the rules).
//
// One screen walks the player from "which device" to "done" without leaving
// the page: with several paired devices it asks which one first; with none it
// says how to pair and A opens Settings → Network → Paired devices; with
// Nearby devices off it says so and A opens the same page, where the switch
// is. Then it compares — a device that is away says so in one plain sentence,
// with A to try again — and shows both copies side by side: when each was
// saved, how much, which files and the newest state's screenshot. The side
// that should win is focused, so the usual case is one press: A on "Get from
// Juka's PC" or "Send to Juka's PC". When both changed, the two buttons
// become "Keep this one" and the player picks; the other version is never
// thrown away (savehandoff.go). The copy keeps running when the screen is
// closed, and its toast says how it went.

import (
	"context"
	"image"
	"path"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// shoMode is what the screen shows.
type shoMode int

const (
	shoModePick shoMode = iota
	shoModeNoPeer
	shoModeOff
	shoModeChecking
	shoModeError
	shoModeResult
	shoModeWorking
)

// shoHit is a clickable part of the screen: a side's button (0 this device,
// 1 the other), a device in the list (shoHitDevice + index) or the screen's
// single button (shoHitMain).
type shoHit struct {
	rect sdl.Rect
	act  int
}

const (
	shoHitMain   = -1
	shoHitDevice = 100
)

// shoUI is the screen. Main thread only.
var shoUI struct {
	open    bool
	game    libraryGame
	mode    shoMode
	peers   []peerRecord
	cursor  int
	peer    peerRecord
	cmp     shoComparison
	errText string
	// gen changes with every check, so a late answer for an earlier one (or
	// another game) is dropped.
	gen     int
	cancel  context.CancelFunc
	working string
	done    int
	total   int
	current string
	thumbs  [2]*sdl.Texture
	pending [2]*image.NRGBA
	hits    []shoHit
	card    sdl.Rect
}

func shoUIIsOpen() bool { return shoUI.open }

// shoFreeThumbs drops the screenshots (textures are freed on the main thread).
func shoFreeThumbs() {
	for i := range shoUI.thumbs {
		if shoUI.thumbs[i] != nil {
			shoUI.thumbs[i].Destroy()
			shoUI.thumbs[i] = nil
		}
		shoUI.pending[i] = nil
	}
}

// shoUIClose shuts the screen without a sound. A copy already started keeps
// going; only a comparison is stopped.
func shoUIClose() {
	if shoUI.cancel != nil && shoUI.mode == shoModeChecking {
		shoUI.cancel()
	}
	shoUI.cancel = nil
	shoFreeThumbs()
	shoUI.open = false
	shoUI.gen++
	shoUI.hits = nil
}

// shoOpen opens the screen for a game (the More row).
func shoOpen(game libraryGame) {
	shoUIClose()
	shoUI.open = true
	shoUI.game = game
	shoUI.peers = peerPairedList()
	shoUI.cursor = 0
	switch {
	case len(shoUI.peers) == 0:
		shoUI.mode = shoModeNoPeer
	case !peerEnabledFlag.Load():
		shoUI.mode = shoModeOff
	case len(shoUI.peers) == 1:
		shoStartCheck(shoUI.peers[0])
	default:
		shoUI.mode = shoModePick
		for i, p := range shoUI.peers {
			if peerAddrFor(p.ID) != "" {
				shoUI.cursor = i
				break
			}
		}
	}
}

// shoStartCheck compares the game with peer on a goroutine.
func shoStartCheck(peer peerRecord) {
	if shoUI.cancel != nil {
		shoUI.cancel()
	}
	shoUI.gen++
	gen := shoUI.gen
	shoUI.peer = peer
	shoUI.mode = shoModeChecking
	shoUI.errText = ""
	shoFreeThumbs()
	ctx, cancel := context.WithTimeout(context.Background(), shoCheckBudget)
	shoUI.cancel = cancel
	game := shoUI.game
	g := shoGameOf(game)
	go func() {
		defer cancel()
		cmp, err := shoCheck(ctx, g, peer, true)
		tbxPost(func() {
			if err == nil {
				info, ok := shoChipFrom([]shoComparison{cmp}, time.Now())
				shoSetChip(game.Path, info, ok)
			}
			if !shoUI.open || shoUI.gen != gen {
				return
			}
			if err != nil {
				shoUI.mode = shoModeError
				shoUI.errText = peerErrorText(err, peer.Name)
				return
			}
			shoUI.cmp = cmp
			shoUI.mode = shoModeResult
			shoUI.cursor = shoDefaultSide(cmp)
		})
		if err != nil {
			return
		}
		shoLoadThumbs(ctx, gen, cmp)
	}()
}

// shoLoadThumbs decodes each side's newest state screenshot (the other
// device's is fetched first) and hands it to the render thread.
func shoLoadThumbs(ctx context.Context, gen int, cmp shoComparison) {
	for side, s := range []shoSide{cmp.Local, cmp.Remote} {
		if !s.HasThumb || ctx.Err() != nil {
			continue
		}
		var p string
		if side == 0 {
			dir, ok := ssyncRootMap(cmp.roots)[s.Thumb.Root]
			if !ok {
				continue
			}
			lp, err := ssyncLocalPath(dir, s.Thumb.Path)
			if err != nil {
				continue
			}
			p = lp
		} else {
			fp, err := shoFetchThumb(ctx, cmp.PeerID, s.Thumb)
			if err != nil {
				continue
			}
			p = fp
		}
		pic, err := gmetaLoadThumb(ctx, p)
		if err != nil {
			continue
		}
		tbxPost(func() {
			if shoUI.open && shoUI.gen == gen {
				shoUI.pending[side] = pic
			}
		})
	}
}

// shoDefaultSide is the side focused after a comparison: the one that should
// win, else the newer one, else the side that has saves.
func shoDefaultSide(cmp shoComparison) int {
	side := 0
	switch cmp.Verdict {
	case shoNewerThere:
		side = 1
	case shoBothChanged:
		if cmp.Remote.Newest > cmp.Local.Newest {
			side = 1
		}
	}
	if !shoSideEnabled(cmp, side) && shoSideEnabled(cmp, 1-side) {
		side = 1 - side
	}
	return side
}

// shoActionable reports whether a press would copy anything.
func shoActionable(cmp shoComparison) bool {
	switch cmp.Verdict {
	case shoNewerHere, shoNewerThere, shoBothChanged:
		return true
	}
	return false
}

// shoSideEnabled reports whether a side's button can be pressed: there is
// something to copy and that side has saves to keep.
func shoSideEnabled(cmp shoComparison, side int) bool {
	if !shoActionable(cmp) {
		return false
	}
	if side == 0 {
		return len(cmp.Local.Files) > 0
	}
	return len(cmp.Remote.Files) > 0
}

// shoSideLabel is a side's button.
func shoSideLabel(cmp shoComparison, side int) string {
	switch {
	case cmp.Verdict == shoBothChanged:
		return "Keep this one"
	case side == 0:
		return "Send to " + cmp.PeerName
	}
	return "Get from " + cmp.PeerName
}

// shoPress copies the focused side's saves over the other's.
func shoPress(side int) {
	cmp := shoUI.cmp
	if shoUI.mode != shoModeResult || !shoSideEnabled(cmp, side) {
		PlayBackSound()
		return
	}
	keepLocal := side == 0
	gen := shoUI.gen
	game := shoUI.game
	peer := shoUI.peer
	shoUI.mode = shoModeWorking
	shoUI.done, shoUI.total, shoUI.current = 0, 0, ""
	shoUI.working = "Getting the saves from " + peer.Name + "…"
	if keepLocal {
		shoUI.working = "Sending the saves to " + peer.Name + "…"
	}
	PlayActivateSound()
	go func() {
		res, err := shoRun(cmp, keepLocal, func(done, total int, current string) {
			name := path.Base(current)
			tbxPost(func() {
				if shoUI.open && shoUI.gen == gen {
					shoUI.done, shoUI.total = done, total
					if current != "" {
						shoUI.current = name
					}
				}
			})
		})
		tbxPost(func() {
			here := shoUI.open && shoUI.gen == gen && shoUI.game.Path == game.Path
			if err != nil {
				ShowToast(peerErrorText(err, peer.Name), ToastKindWarning)
				if here {
					shoUI.mode = shoModeError
					shoUI.errText = peerErrorText(err, peer.Name)
				}
				return
			}
			msg, kind := shoResultText(res, keepLocal, peer.Name)
			ShowToast(msg, kind)
			if here {
				// The comparison again, so the screen shows where things stand.
				shoStartCheck(peer)
			}
		})
	}()
}

// shoOpenDevices leaves the game page for Settings → Network → Paired devices.
func shoOpenDevices() {
	PlayActivateSound()
	shoUIClose()
	closeGameLibraryPanel()
	peerDevicesOpen(appConfig)
}

// ── Input ───────────────────────────────────────────────────────────────────

func shoUIKey(e *sdl.KeyboardEvent) bool {
	shoUIAction(tbxKeyAction(e))
	return true
}

func shoUIPad(e *sdl.ControllerButtonEvent) bool {
	shoUIAction(tbxPadAction(e))
	return true
}

func shoUIAction(act tbxAction) {
	if act == tbxActNone {
		return
	}
	if act == tbxActBack {
		PlayBackSound()
		shoUIClose()
		return
	}
	several := len(shoUI.peers) > 1
	switch shoUI.mode {
	case shoModePick:
		switch act {
		case tbxActUp:
			if shoUI.cursor > 0 {
				shoUI.cursor--
				PlayNavSound()
			}
		case tbxActDown:
			if shoUI.cursor < len(shoUI.peers)-1 {
				shoUI.cursor++
				PlayNavSound()
			}
		case tbxActConfirm:
			if shoUI.cursor >= 0 && shoUI.cursor < len(shoUI.peers) {
				PlayActivateSound()
				shoStartCheck(shoUI.peers[shoUI.cursor])
			}
		}
	case shoModeNoPeer, shoModeOff:
		if act == tbxActConfirm {
			shoOpenDevices()
		}
	case shoModeError:
		switch {
		case act == tbxActConfirm || act == tbxActY:
			PlayActivateSound()
			shoStartCheck(shoUI.peer)
		case act == tbxActX && several:
			PlayNavSound()
			shoUI.mode = shoModePick
		}
	case shoModeResult:
		switch act {
		case tbxActLeft, tbxActRight:
			next := 0
			if act == tbxActRight {
				next = 1
			}
			if next != shoUI.cursor && shoSideEnabled(shoUI.cmp, next) {
				shoUI.cursor = next
				PlayNavSound()
			}
		case tbxActConfirm:
			shoPress(shoUI.cursor)
		case tbxActY:
			PlayActivateSound()
			shoStartCheck(shoUI.peer)
		case tbxActX:
			if several {
				PlayNavSound()
				shoUI.mode = shoModePick
			}
		}
	}
}

func shoUIMouse(mx, my int32) bool {
	for _, hit := range shoUI.hits {
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		switch {
		case hit.act == shoHitMain:
			shoUIAction(tbxActConfirm)
		case hit.act >= shoHitDevice:
			shoUI.cursor = hit.act - shoHitDevice
			shoUIAction(tbxActConfirm)
		default:
			if shoUI.mode == shoModeResult && shoSideEnabled(shoUI.cmp, hit.act) {
				shoUI.cursor = hit.act
				shoPress(hit.act)
			}
		}
		return true
	}
	if shoUI.card.W == 0 || !pointInRect(mx, my, shoUI.card) {
		PlayBackSound()
		shoUIClose()
	}
	return true
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// shoUIRender draws the screen over the game page.
func shoUIRender(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	for i, pic := range shoUI.pending {
		if pic == nil {
			continue
		}
		if tex := imgvTexture(renderer, pic); tex != nil {
			if shoUI.thumbs[i] != nil {
				shoUI.thumbs[i].Destroy()
			}
			shoUI.thumbs[i] = tex
		}
		shoUI.pending[i] = nil
	}
	title := "Saves on another device"
	if shoUI.peer.Name != "" && shoUI.mode != shoModePick && shoUI.mode != shoModeNoPeer && shoUI.mode != shoModeOff {
		title = "Saves on " + shoUI.peer.Name
	}
	header := UIPanelHeader(title, shoUI.game.Label(), "handoff")
	headerH := UIPanelHeaderBand(config, header, 10)
	footerH := fonts.smallH + 20
	cardW := min(int32(920), screenWidth-48)
	cardH := min(int32(600), screenHeight-48)
	if shoUI.mode != shoModeResult {
		cardW = min(int32(680), screenWidth-64)
		cardH = min(headerH+footerH+260, screenHeight-48)
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	shoUI.card = card
	shoUI.hits = shoUI.hits[:0]
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	body := sdl.Rect{X: card.X + OverlayPadX, Y: card.Y + headerH, W: card.W - 2*OverlayPadX, H: card.H - headerH - footerH - 8}

	several := len(shoUI.peers) > 1
	var hints []FooterHint
	switch shoUI.mode {
	case shoModePick:
		shoDrawDevices(renderer, config, fonts, body)
		hints = []FooterHint{{"Up/Down", "Choose"}, {"A", "Compare"}, {"B", "Back"}}
	case shoModeNoPeer:
		shoDrawMessage(renderer, config, fonts, body, "No paired device yet",
			"Pair this device with your PC or handheld first: open Settings → Network → Paired devices on both, with JukaHub open and on the same Wi-Fi, and confirm the code.",
			"Open Paired devices")
		hints = []FooterHint{{"A", "Open Paired devices"}, {"B", "Back"}}
	case shoModeOff:
		shoDrawMessage(renderer, config, fonts, body, "Nearby devices is off",
			"Saves move over your Wi-Fi only while Nearby devices is on. Turn it on in Settings → Network → Paired devices, here and on the other device.",
			"Open Paired devices")
		hints = []FooterHint{{"A", "Open Paired devices"}, {"B", "Back"}}
	case shoModeChecking:
		shoDrawMessage(renderer, config, fonts, body, "Comparing with "+shoUI.peer.Name+"…",
			"Reading this game's saves on both devices. The first time can take a minute while the other device checks its save folders.", "")
		hints = []FooterHint{{"B", "Back"}}
	case shoModeError:
		shoDrawMessage(renderer, config, fonts, body, "Could not compare", shoUI.errText, "Try again")
		hints = []FooterHint{{"A", "Try again"}}
		if several {
			hints = append(hints, FooterHint{"X", "Other device"})
		}
		hints = append(hints, FooterHint{"B", "Back"})
	case shoModeWorking:
		detail := "You can close this — the copy finishes in the background."
		if shoUI.total > 0 {
			detail = strconv.Itoa(min(shoUI.done+1, shoUI.total)) + " of " + strconv.Itoa(shoUI.total)
			if shoUI.current != "" {
				detail += " · " + shoUI.current
			}
			detail += "\nYou can close this — the copy finishes in the background."
		}
		shoDrawMessage(renderer, config, fonts, body, shoUI.working, detail, "")
		hints = []FooterHint{{"B", "Close"}}
	case shoModeResult:
		shoDrawResult(renderer, config, fonts, body)
		cmp := shoUI.cmp
		if shoActionable(cmp) {
			if shoSideEnabled(cmp, 0) && shoSideEnabled(cmp, 1) {
				hints = append(hints, FooterHint{"Left/Right", "Choose"})
			}
			if shoSideEnabled(cmp, shoUI.cursor) {
				hints = append(hints, FooterHint{"A", shoSideLabel(cmp, shoUI.cursor)})
			}
		}
		hints = append(hints, FooterHint{"Y", "Check again"})
		if several {
			hints = append(hints, FooterHint{"X", "Other device"})
		}
		hints = append(hints, FooterHint{"B", "Back"})
	}
	hint := hintLine(hints...)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// shoDrawMessage draws a title, a wrapped paragraph and, when button is set,
// the screen's one button.
func shoDrawMessage(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, title, text, button string) {
	y := body.Y + 12
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, title, body.W), ColorTextPrimary(), body.X, y)
	y += fonts.bodyH + 10
	for _, para := range shoParagraphs(text) {
		for _, line := range wrapTextLines(fonts.small, para, body.W, 6) {
			renderText(renderer, config, fonts.small, line, ColorTextSecondary(), body.X, y)
			y += fonts.smallH + 4
		}
	}
	if button == "" {
		return
	}
	y += 12
	tw, _ := measureText(fonts.body, button)
	rect := sdl.Rect{X: body.X, Y: y, W: tw + 40, H: fonts.bodyH + 16}
	shoDrawButton(renderer, config, fonts, rect, button, true, true)
	shoUI.hits = append(shoUI.hits, shoHit{rect: rect, act: shoHitMain})
}

// shoParagraphs splits text at newlines (a paragraph per line).
func shoParagraphs(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}

// shoDrawButton draws a pill button: focused ones are filled with the accent,
// disabled ones dimmed.
func shoDrawButton(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, label string, focused, enabled bool) {
	ink := glassAccent()
	fill, stroke, text := WithAlpha(ink, 36), WithAlpha(ink, 120), ColorTextPrimary()
	switch {
	case !enabled:
		fill, stroke, text = WithAlpha(ink, 10), WithAlpha(ink, 40), ColorTextSecondary()
	case focused:
		fill, stroke = WithAlpha(ink, 110), WithAlpha(ink, 230)
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 2, stroke)
	label = ellipsize(fonts.body, label, rect.W-24)
	tw, th := measureText(fonts.body, label)
	renderText(renderer, config, fonts.body, label, text, rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
}

// shoDrawDevices draws the paired devices to choose from.
func shoDrawDevices(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	renderText(renderer, config, fonts.small, "Which device has the other copy?", ColorTextSecondary(), body.X, body.Y+6)
	rowH := fonts.bodyH + fonts.tinyH + 22
	y := body.Y + fonts.smallH + 16
	visible := int((body.Y + body.H - y) / rowH)
	if visible < 1 {
		visible = 1
	}
	first := 0
	if shoUI.cursor >= visible {
		first = shoUI.cursor - visible + 1
	}
	for i := first; i < len(shoUI.peers) && i < first+visible; i++ {
		p := shoUI.peers[i]
		rect := sdl.Rect{X: body.X, Y: y + int32(i-first)*rowH, W: body.W, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == shoUI.cursor, false)
		icon := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-28)/2, W: 28, H: 28}
		DrawHomeIcon(renderer, "transfer", icon, i == shoUI.cursor)
		textX := icon.X + icon.W + 14
		textW := rect.X + rect.W - 14 - textX
		status := "Not seen on this Wi-Fi lately — A looks for it"
		if peerAddrFor(p.ID) != "" {
			status = "On this Wi-Fi"
		}
		top := rect.Y + (rect.H-fonts.bodyH-fonts.tinyH-2)/2
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, p.Name, textW), ColorTextPrimary(), textX, top)
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, status, textW), ColorTextSecondary(), textX, top+fonts.bodyH+2)
		shoUI.hits = append(shoUI.hits, shoHit{rect: rect, act: shoHitDevice + i})
	}
}

// shoDrawResult draws the verdict and the two copies side by side.
func shoDrawResult(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	cmp := shoUI.cmp
	now := time.Now()
	ink := ColorTextSecondary()
	switch cmp.Verdict {
	case shoNewerThere, shoBothChanged:
		ink = ColorWarning
	case shoSame:
		ink = ColorSuccess
	}
	verdict := shoVerdictLine(cmp, now)
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, verdict, body.W), ink, body.X, body.Y+6)
	y := body.Y + fonts.bodyH + 14
	var notes []string
	switch {
	case !cmp.Sure && shoActionable(cmp):
		notes = append(notes, "Judged by file times — the two devices have not synced these saves before.")
	case cmp.Verdict == shoNoSaves:
		notes = append(notes, "Saves are found by the ROM's file name: if it is named differently there, rename one to match.")
	}
	if len(cmp.clash) > 0 {
		notes = append(notes, "Some saves are named with different upper/lower case on the two devices and are not copied — rename one to match.")
	}
	if len(cmp.plan.Skipped) > 0 {
		notes = append(notes, "Some saves are not copied: one device has no such save folder, or cannot store the name.")
	}
	for _, note := range notes {
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, note, body.W), ColorTextSecondary(), body.X, y)
		y += fonts.tinyH + 8
	}
	gap := int32(16)
	colW := (body.W - gap) / 2
	colH := body.Y + body.H - y
	hasThumb := shoUI.thumbs[0] != nil || shoUI.thumbs[1] != nil || cmp.Local.HasThumb || cmp.Remote.HasThumb
	names := [2]string{"Here · " + cmp.local.Name, cmp.PeerName}
	for side, s := range []shoSide{cmp.Local, cmp.Remote} {
		col := sdl.Rect{X: body.X + int32(side)*(colW+gap), Y: y, W: colW, H: colH}
		enabled := shoSideEnabled(cmp, side)
		UIListRow(renderer, col, glassRowRadius, enabled && shoUI.cursor == side, false)
		x := col.X + 16
		w := col.W - 32
		cy := col.Y + 12
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, names[side], w), ColorTextPrimary(), x, cy)
		cy += fonts.bodyH + 8
		if hasThumb {
			tw := min(w, int32(gmetaThumbW))
			th := tw * gmetaThumbH / gmetaThumbW
			box := sdl.Rect{X: x, Y: cy, W: tw, H: th}
			fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 8, WithAlpha(ColorTextSecondary(), 24))
			if tex := shoUI.thumbs[side]; tex != nil {
				_, _, texW, texH, err := tex.Query()
				if err == nil && texW > 0 && texH > 0 {
					fw, fh := imgvFitInside(int(texW), int(texH), int(box.W), int(box.H))
					dst := sdl.Rect{X: box.X + (box.W-int32(fw))/2, Y: box.Y + (box.H-int32(fh))/2, W: int32(fw), H: int32(fh)}
					_ = renderer.Copy(tex, nil, &dst)
				}
			} else {
				label := "No screenshot"
				if s.HasThumb {
					label = "Loading…"
				}
				lw, lh := measureText(fonts.tiny, label)
				renderText(renderer, config, fonts.tiny, label, ColorTextSecondary(), box.X+(box.W-lw)/2, box.Y+(box.H-lh)/2)
			}
			cy += th + 10
		}
		lines := shoSideLines(s, now)
		buttonH := fonts.bodyH + 16
		for i, line := range lines {
			if cy+fonts.smallH > col.Y+col.H-buttonH-20 {
				break
			}
			font, fh, color := fonts.tiny, fonts.tinyH, ColorTextSecondary()
			if i == 0 {
				font, fh, color = fonts.small, fonts.smallH, ColorTextPrimary()
			}
			renderText(renderer, config, font, ellipsize(font, line, w), color, x, cy)
			cy += fh + 4
		}
		if shoActionable(cmp) {
			button := sdl.Rect{X: x, Y: col.Y + col.H - buttonH - 12, W: w, H: buttonH}
			shoDrawButton(renderer, config, fonts, button, shoSideLabel(cmp, side), enabled && shoUI.cursor == side, enabled)
			if enabled {
				shoUI.hits = append(shoUI.hits, shoHit{rect: button, act: side})
			}
		}
	}
}

// shoSideLines describes one copy: when it was saved, how much, which files.
func shoSideLines(s shoSide, now time.Time) []string {
	if len(s.Files) == 0 {
		return []string{"No saves for this game"}
	}
	when := "Saved — time unknown"
	if ago := shoAgo(s.Newest, now); ago != "" {
		when = "Saved " + ago
	}
	lines := []string{when, pluralizeCount(len(s.Files), "file", "files") + " · " + humanBytes(s.Bytes)}
	const shown = 4
	for i, f := range s.Files {
		if i == shown {
			lines = append(lines, "+"+strconv.Itoa(len(s.Files)-shown)+" more")
			break
		}
		lines = append(lines, path.Base(f.Path))
	}
	return lines
}
