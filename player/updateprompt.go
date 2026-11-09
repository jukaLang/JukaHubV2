package main

// updateprompt.go — the "a new JukaHub release is available" prompt.
//
// The startup check only advertises a release (see autoupdate.go); this file
// turns that advertisement into a visible, one-press offer:
//   - an "Update" pill in the header's system corner, immediately left of the
//     notification bell on Home and on every scene, reachable with the D-pad
//     and clickable, and
//   - the scene-header badge and the once-per-launch modal, which now install
//     directly instead of walking the user through the Patch scene.
//
// startHeaderUpdate is the single action behind all three: it downloads the
// release, verifies and extracts the archive, then hands the file swap to the
// detached helper and restarts. Progress shows in the install bar over whatever
// screen is open, so the offer never pulls the user out of what they were doing.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

var updatePrompt struct {
	open     bool
	focus    int // 0 = Update now, 1 = Not now
	seen     bool
	badge    sdl.Rect
	nowBtn   sdl.Rect
	laterBtn sdl.Rect
}

// updateAvailable reports whether the header should offer a release. A stored
// advertisement is not enough: it is persisted state, and a build that already
// caught up with it (installed without the swap helper, or a card image copied
// over the old install) must not keep showing "Update" until a network check
// happens to clear it. advertisedUpdateIsStale answers that locally.
func updateAvailable() bool {
	return availableUpdateTag() != "" && !advertisedUpdateIsStale()
}

// openUpdatePrompt shows the modal (from a badge click or the automatic offer).
func openUpdatePrompt() {
	if !updateAvailable() {
		return
	}
	updatePrompt.open = true
	updatePrompt.focus = 0
	updatePrompt.seen = true
}

func closeUpdatePrompt() {
	updatePrompt.open = false
	updatePrompt.nowBtn = sdl.Rect{}
	updatePrompt.laterBtn = sdl.Rect{}
}

// maybeOfferUpdate opens the prompt once per launch, after the app has finished
// starting and no other modal owns the screen. Calling it every frame is safe.
func maybeOfferUpdate() {
	if updatePrompt.open || updatePrompt.seen || !updateAvailable() {
		return
	}
	if virtualKeyboardActive || toolsPanel.open || imageViewerPath != "" || qs.open ||
		patchModal != nil || transitionPhase != "none" || videoFullscreen || playerPhase() != "idle" ||
		nc.open || tb.startOpen || processPanel.open || wifiPanel.open || ipPanelOpen || emuPanel.open || gpPanel.open || winAppsPanel.open || portsPanel.open || savePanel.open || curationPanel.open || chiakiPanel.open || raPanel.open || a11y.open ||
		wpOpen || shortcutsOpen || globalSearchOpen || clipSearchOpen || smartHomePanel.open ||
		memPanel.open || ctrldiag.open || fileEditor.open {
		return
	}
	openUpdatePrompt()
}

// activateUpdatePrompt runs the focused choice.
func activateUpdatePrompt(config *Config) {
	if updatePrompt.focus == 0 {
		closeUpdatePrompt()
		// One press installs: no Patch detour and no second confirmation. The
		// header download chip runs this same action.
		startHeaderUpdate()
		return
	}
	closeUpdatePrompt()
}

// updateInstallRunner performs the download, extraction and staged swap. It is
// a variable so tests can press the header chip without touching the network or
// replacing the running executable.
var updateInstallRunner = func(ctx context.Context, config *Config) error {
	return InstallAvailableJukaHubUpdate(ctx, config)
}

// startHeaderUpdate installs the advertised release in one press. The work runs
// off the event thread; the install bar reports download/install progress over
// whatever screen is open, and on success the swap helper restarts JukaHub.
// It reports whether an update was started.
func startHeaderUpdate() bool {
	if !updateAvailable() {
		ShowToast("JukaHub is up to date", ToastKindInfo)
		return false
	}
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return false
	}
	label := availableUpdateLabel()
	setPatchBusy("Updating JukaHub to " + label)
	installBarBegin("JukaHub update")
	installBarPhase(installPhaseResolving)
	ShowToast("Downloading JukaHub "+label+" — JukaHub restarts when it is ready", ToastKindInfo)

	go func() {
		defer endPatchOp()
		defer clearPatchBusy()
		// The timeout derives from the card's run token, so a cancel from the
		// card or the activity strip stops the transfer mid-flight.
		ctx, cancel := context.WithTimeout(installBarRunContext(), 20*time.Minute)
		defer cancel()
		if err := updateInstallRunner(ctx, appConfig); err != nil {
			// A stale offer (the build already carries the release, or it was
			// pulled) is not a failure: forget it and say so.
			if errors.Is(err, errAlreadyUpToDate) {
				clearAvailableUpdate()
				installBarDone()
				ShowToast("JukaHub is already up to date", ToastKindSuccess)
				return
			}
			installBarFail(firstLine(err.Error()))
			ShowToast("Update failed — "+firstLine(err.Error()), ToastKindError)
			return
		}
		installBarDone()
		if appConfig != nil {
			patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(appConfig)})
		}
	}()
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Drawing
// ──────────────────────────────────────────────────────────────────────

// updatePillLabel is the word both headers put on the release offer. One label
// and one footprint everywhere is what keeps the top bar recognisable: the home
// header used to show a bare download glyph while every scene header showed a
// wider pill with text.
const (
	updatePillLabel = "Update"
	// updatePillHeightInset keeps the offer a step quieter than the labelled
	// header buttons (Back, Menu), the same inset the bell chip takes: the two
	// controls share the system corner and used to be two different heights.
	updatePillHeightInset = notifBellHeightInset
	// updatePillIconPad is where the arrow sits from the pill's left edge.
	updatePillIconPad = int32(14)
	// updatePillTextGap is where the label starts: far enough past the arrow
	// that the two never touch at handheld sizes.
	updatePillTextGap = int32(26)
	// updatePillRightPad is the breathing room after the label.
	updatePillRightPad = int32(12)
	// updatePillIconWidth is the pill's width without the label: the arrow's
	// drawn span plus a pad on either side. The 4:3 device canvases are only
	// 960px wide, and there the word "Update" costs the header more room than
	// the offer is worth next to the bell, so the pill keeps just the arrow.
	updatePillIconWidth = int32(28)
)

// updatePillHeight is the release pill's own height: one step below the
// labelled header buttons, exactly like the bell chip beside it.
func updatePillHeight() int32 {
	h := headerPillHeight() - updatePillHeightInset
	if h < notifBellMinHeight {
		h = notifBellMinHeight
	}
	return h
}

// updatePillIconOnly reports whether the offer is drawn without its label. The
// wide 1280 canvas has room for the word; the compact 960 canvas the 4:3
// devices draw into does not, and there the pill shrinks to its arrow so it
// stays a system-corner chip instead of a wide button crowding the bell.
func updatePillIconOnly() bool {
	return uiTextScaleFactor() < 0.999
}

// updatePillSize is the shared footprint of the release pill: the label plus
// room for the arrow on the wide canvas, just the arrow on the compact one, at
// the bell chip's height so the system corner reads as one row. Every caller
// gets the same numbers, on Home and in every scene.
func updatePillSize(font *ttf.Font) (int32, int32) {
	if font == nil {
		return 0, 0
	}
	if updatePillIconOnly() {
		return updatePillIconWidth, updatePillHeight()
	}
	tw, _, _ := font.SizeUTF8(updatePillLabel)
	return updatePillTextGap + int32(tw) + updatePillRightPad, updatePillHeight()
}

// drawHeaderUpdatePill draws the release offer in the header's system corner:
// the same pill, at the same button height, immediately left of the bell, on
// Home and in every scene. It records nothing — the caller owns the hit rect
// for its own screen (homeUpdateRect or updatePrompt.badge).
func drawHeaderUpdatePill(renderer *sdl.Renderer, config *Config, rightX, barH int32, focused bool) sdl.Rect {
	font := headerCaptionFont(config)
	w, h := updatePillSize(font)
	rect := sdl.Rect{X: rightX - w, Y: (barH - h) / 2, W: w, H: h}
	return drawUpdatePill(renderer, config, font, rect, focused)
}

// drawUpdatePill draws the "a release is available" pill inside rect: the
// bell's surface, a small warning-toned arrow, a secondary label on the wide
// canvas, and a focus ring while the header focus sits on it. The home header
// and the scene header both call this, so the offer cannot drift into two
// different controls again.
//
// It used to be the loudest thing in the bar — a full-height button with a
// warning-filled pill and a 200-alpha warning outline, next to a bell that is a
// quiet 32px surface chip. The offer now takes the same height and the same
// surface as the bell and lets only the arrow keep a hint of the warning
// colour, so an available release reads as one more system control instead of
// a banner shouting over the header.
func drawUpdatePill(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, focused bool) sdl.Rect {
	if renderer == nil || font == nil || rect.W < 24 || rect.H < 18 || !updateAvailable() {
		return sdl.Rect{}
	}
	radius := rect.H / 2
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, ColorIconSurface)
	// Focus ring while the D-pad/arrows sit on the pill. Every other top-bar
	// control draws one; without it a controller user cannot tell the offer is
	// selected rather than merely present.
	if focused {
		// The shared 3px ring (glassFocusRingWidth), standing just off the
		// pill like every other focused control in the bar.
		strokeRoundedRect(renderer, rect.X-3, rect.Y-3, rect.W+6, rect.H+6, (rect.H+6)/2, glassFocusRingWidth, accentColor)
	}

	// Down-arrow drawn with lines: Inter may not contain U+2193 on the device.
	// Icon-only pills centre it, since there is no label to sit beside.
	cx := rect.X + updatePillIconPad
	if updatePillIconOnly() {
		cx = rect.X + rect.W/2
	}
	cy := rect.Y + rect.H/2
	renderer.SetDrawColor(ColorWarning.R, ColorWarning.G, ColorWarning.B, 200)
	renderer.DrawLine(cx, cy-6, cx, cy+4)
	renderer.DrawLine(cx-4, cy-1, cx, cy+4)
	renderer.DrawLine(cx+4, cy-1, cx, cy+4)

	if updatePillIconOnly() {
		return rect
	}
	renderText(renderer, config, font, updatePillLabel, ColorTextSecondary(),
		rect.X+updatePillTextGap, rect.Y+(rect.H-int32(font.Height()))/2)
	return rect
}

// homeUpdateRect is the header download chip's rect for this frame, or an empty
// rect when no release is offered (or the screen is too small for the chip).
// Hit-testing and the focus entry are driven from it.
var homeUpdateRect sdl.Rect

// homeUpdateChipSize is the chip's footprint for a given text font. It is the
// same pill the scene header draws, measured by the shared helper, so the offer
// keeps one size and one label across the app.
func homeUpdateChipSize(font *ttf.Font) (int32, int32) {
	return updatePillSize(font)
}

func renderUpdatePrompt(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !updatePrompt.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}

	drawOverlayScrim(renderer)

	smallH := int32(font.Height())
	layout := layoutUpdatePrompt(font, medium)
	x, y, cardW := layout.card.X, layout.card.Y, layout.card.W
	drawOverlayCard(renderer, layout.card)
	drawOverlayCardAccent(renderer, x, y, cardW)

	renderText(renderer, config, medium, "JukaHub update available", ColorTextPrimary(), x+OverlayPadX, layout.titleY)
	for i, line := range layout.bodyLines {
		renderText(renderer, config, font, line, ColorTextTertiary(), x+OverlayPadX, layout.bodyY+int32(i)*smallH)
	}
	for i, line := range layout.hintLines {
		renderText(renderer, config, font, line, ColorTextTertiary(), x+OverlayPadX, layout.hintY+int32(i)*smallH)
	}
	if len(layout.notes) > 0 {
		renderText(renderer, config, font, "WHAT'S NEW", accentColor, x+OverlayPadX, layout.noteLabelY)
		for i, note := range layout.notes {
			// ASCII dash: the bundled handheld fonts are not guaranteed to
			// carry a bullet glyph.
			renderText(renderer, config, font, "- "+note, ColorTextSecondary(), x+OverlayPadX, layout.noteLabelY+smallH+6+int32(i)*smallH)
		}
	}

	// Buttons side by side, right-aligned.
	btnH := int32(42)
	updateW := int32(160)
	laterW := int32(120)
	laterX := x + cardW - OverlayPadX - laterW
	updateX := laterX - 12 - updateW
	btnY := layout.buttonsY
	updatePrompt.laterBtn = sdl.Rect{X: laterX, Y: btnY, W: laterW, H: btnH}
	updatePrompt.nowBtn = sdl.Rect{X: updateX, Y: btnY, W: updateW, H: btnH}

	updateFill := WithAlpha(ColorInfo, 90)
	if updatePrompt.focus == 0 {
		updateFill = WithAlpha(uiHighlightColor(), 220)
	}
	updatePrompt.nowBtn = UIButton(renderer, config, updatePrompt.nowBtn, "Update now", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       updateFill,
		HoverFill:  updateFill,
		FocusFill:  updateFill,
		Border:     uiHighlightColor(),
		LabelColor: ColorTextPrimary(),
		Font:       font,
		Pill:       true,
		NoShadow:   true,
	}, UIButtonState{Focused: updatePrompt.focus == 0})
	updatePrompt.laterBtn = UIButton(renderer, config, updatePrompt.laterBtn, "Not now", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       ColorIconSurface,
		HoverFill:  ColorIconSurface,
		FocusFill:  ColorIconSurface,
		LabelColor: ColorTextSecondary(),
		Font:       font,
		Pill:       true,
		NoShadow:   true,
	}, UIButtonState{Focused: updatePrompt.focus == 1})
}

// updatePromptLayout is the measured geometry of the modal. The renderer draws
// from it and the render test checks it, so wrapped copy and the release-note
// preview cannot slip under the button row again.
type updatePromptLayout struct {
	card       sdl.Rect
	titleY     int32
	bodyY      int32
	bodyLines  []string
	hintY      int32
	hintLines  []string
	noteLabelY int32
	notes      []string
	noteBottom int32
	buttonsY   int32
}

const (
	updatePromptPad      = OverlayPadX
	updatePromptBtnH     = int32(42)
	updatePromptBtnGap   = int32(28)
	updatePromptMaxNotes = 3
)

// layoutUpdatePrompt measures the card from its text, top down. The height is
// derived from the wrapped lines rather than a constant, because a fixed height
// is what let the third release-note line draw behind the buttons.
func layoutUpdatePrompt(font, medium *ttf.Font) updatePromptLayout {
	smallH, medH := int32(font.Height()), int32(medium.Height())
	cardW := int32(620)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	innerW := cardW - 2*OverlayPadX

	body := fmt.Sprintf("Release %s is ready to install. Download and restart when you are ready — nothing is installed without your confirmation.", availableUpdateLabel())
	bodyLines := wrapTextLines(font, body, innerW, 3)
	hintLines := wrapTextLines(font, "You can always install it later from Tools → Patch.", innerW, 1)
	notes := updatePromptHighlights(updatePromptMaxNotes)

	measure := func(withNotes bool) updatePromptLayout {
		m := updatePromptLayout{bodyLines: bodyLines, hintLines: hintLines}
		m.titleY = updatePromptPad
		m.bodyY = m.titleY + medH + 12
		m.hintY = m.bodyY + int32(len(bodyLines))*smallH + 8
		h := m.hintY + int32(len(hintLines))*smallH
		m.noteBottom = h
		if withNotes {
			m.notes = notes
			m.noteLabelY = h + 14
			m.noteBottom = m.noteLabelY + smallH + 6 + int32(len(notes))*smallH
			h = m.noteBottom
		}
		m.buttonsY = h + updatePromptPad - 4
		h = m.buttonsY + updatePromptBtnH + updatePromptBtnGap
		m.card = sdl.Rect{W: cardW, H: h}
		return m
	}

	m := measure(len(notes) > 0)
	// A short window (a desktop resize, a scaled preview) gets the compact
	// card rather than notes overlapping the buttons.
	if maxH := screenHeight - 64; m.card.H > maxH {
		m = measure(false)
		if m.card.H > maxH {
			m.card.H = maxH
		}
	}
	// Vertical geometry above is card-relative; offset it into screen space.
	m.card.X = (screenWidth - m.card.W) / 2
	m.card.Y = (screenHeight - m.card.H) / 2
	m.titleY += m.card.Y
	m.bodyY += m.card.Y
	m.hintY += m.card.Y
	m.noteLabelY += m.card.Y
	m.noteBottom += m.card.Y
	m.buttonsY += m.card.Y
	return m
}

// updatePromptHighlights returns up to max lines of the cached release notes
// for the modal, so the offer says what is in the release and not just which
// version it is. Bullet items win over section headings ("Fixes" on its own
// tells the user nothing); an offline device with no cached notes gets nil and
// the compact layout.
func updatePromptHighlights(max int) []string {
	if max <= 0 {
		return nil
	}
	_, text := cachedReleaseNotes()
	var bullets, all []string
	for _, line := range plainReleaseNotes(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		all = append(all, line)
		item, ok := strings.CutPrefix(line, "• ")
		if !ok {
			continue
		}
		item = strings.TrimSpace(item)
		// Release bodies end with a "thanks" list of handles and links; those
		// are not changes, and three of them would fill the whole preview.
		if item == "" || strings.HasPrefix(item, "@") || strings.HasPrefix(item, "http") {
			continue
		}
		bullets = append(bullets, item)
	}
	pick := bullets
	if len(pick) == 0 {
		pick = all
	}
	if len(pick) > max {
		pick = pick[:max]
	}
	return pick
}

// ──────────────────────────────────────────────────────────────────────
// Input
// ──────────────────────────────────────────────────────────────────────

func ctrlUpdatePrompt(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		updatePrompt.focus = 1 - updatePrompt.focus
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		PlayActivateSound()
		activateUpdatePrompt(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeUpdatePrompt()
		PlayBackSound()
	}
	return true
}

func handleUpdatePromptKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !updatePrompt.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_RIGHT, sdl.K_TAB:
		updatePrompt.focus = 1 - updatePrompt.focus
	case sdl.K_RETURN, sdl.K_SPACE:
		activateUpdatePrompt(config)
	case sdl.K_ESCAPE:
		closeUpdatePrompt()
	}
	return true
}

func handleUpdatePromptMouse(mx, my int32, config *Config) bool {
	if !updatePrompt.open {
		return false
	}
	if pointInRect(mx, my, updatePrompt.nowBtn) {
		updatePrompt.focus = 0
		activateUpdatePrompt(config)
		return true
	}
	if pointInRect(mx, my, updatePrompt.laterBtn) {
		updatePrompt.focus = 1
		activateUpdatePrompt(config)
		return true
	}
	// Clicking the dimmed background dismisses it like "Not now".
	closeUpdatePrompt()
	return true
}
