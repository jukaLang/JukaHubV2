package main

// updateprompt.go — the "a new JukaHub release is available" prompt.
//
// The startup check only advertises a release (see autoupdate.go); this file
// turns that advertisement into a visible, one-press offer:
//   - a download icon in the home header's status cluster, immediately left of
//     the weather chip, reachable with the D-pad and clickable, and
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

func updateAvailable() bool { return availableUpdateTag() != "" }

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
		nc.open || tb.startOpen || processPanel.open || ipPanelOpen || emuPanel.open || gpPanel.open || winAppsPanel.open || portsPanel.open || savePanel.open || curationPanel.open || chiakiPanel.open || raPanel.open || a11y.open ||
		wpOpen || socialNotifOpen || shortcutsOpen || globalSearchOpen || clipSearchOpen || smartHomePanel.open {
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
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
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

// drawUpdateBadge draws the download pill ending at rightX and returns the
// horizontal space it consumed (including the gap before the bell). It records
// the hit rect used by handleChromeBarClick.
func drawUpdateBadge(renderer *sdl.Renderer, config *Config, rightX, y, h int32) int32 {
	updatePrompt.badge = sdl.Rect{}
	if renderer == nil || !updateAvailable() {
		return 0
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return 0
	}
	text := "Update"
	tw, _, _ := font.SizeUTF8(text)
	pillW := int32(tw) + 46
	pillH := h
	if pillH <= 0 {
		pillH = int32(font.Height()) + 8
	}
	x := rightX - pillW
	rect := sdl.Rect{X: x, Y: y, W: pillW, H: pillH}
	fillRoundedRect(renderer, x, y, pillW, pillH, pillH/2, WithAlpha(ColorWarning, 40))
	strokeRoundedRect(renderer, x, y, pillW, pillH, pillH/2, 1, WithAlpha(ColorWarning, 200))
	// Focus ring while the D-pad/arrows sit on the pill. Every other top-bar
	// control draws one; without it a controller user cannot tell the offer is
	// selected rather than merely present.
	if focusEngine.ElementIndex() == ChromeUpdateIndex {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawRect(&sdl.Rect{X: x - 3, Y: y - 3, W: pillW + 6, H: pillH + 6})
	}

	// Down-arrow drawn with lines: Inter may not contain U+2193 on the device.
	cx := x + 16
	cy := y + pillH/2
	renderer.SetDrawColor(ColorWarning.R, ColorWarning.G, ColorWarning.B, 235)
	renderer.DrawLine(cx, cy-7, cx, cy+5)
	renderer.DrawLine(cx-5, cy, cx, cy+5)
	renderer.DrawLine(cx+5, cy, cx, cy+5)

	renderText(renderer, config, font, text, ColorTextPrimary(), x+30, y+(pillH-int32(font.Height()))/2)
	updatePrompt.badge = rect
	return pillW + SpaceSM
}

// homeUpdateRect is the header download chip's rect for this frame, or an empty
// rect when no release is offered (or the screen is too small for the chip).
// Hit-testing and the focus entry are driven from it.
var homeUpdateRect sdl.Rect

// homeUpdateChipSize is the chip's footprint for a given text font: a small
// pill that sits inside the header band without competing with the status text
// it is next to.
func homeUpdateChipSize(font *ttf.Font) (int32, int32) {
	h := int32(24)
	if font != nil {
		if fh := int32(font.Height()) + 8; fh > h {
			h = fh
		}
	}
	// Wider than tall: room for a readable download glyph and a comfortable
	// focus ring on a small screen.
	return h + 20, h
}

// renderHomeUpdateChip draws the "a new JukaHub release is available" download
// icon in the home header. Focused, it takes the theme highlight and a ring, so
// the D-pad position is obvious on a handheld screen without a pointer.
func renderHomeUpdateChip(renderer *sdl.Renderer, fonts HomeFonts, rect sdl.Rect, focused bool) sdl.Rect {
	homeUpdateRect = sdl.Rect{}
	if renderer == nil || fonts.Small == nil || rect.W < 18 || rect.H < 18 || !updateAvailable() {
		return sdl.Rect{}
	}

	fill := WithAlpha(ColorWarning, 36)
	border := WithAlpha(ColorWarning, 200)
	// The glyph is always the primary text colour: a warning-tinted arrow on a
	// warning-tinted pill disappeared at handheld size.
	glyph := ColorTextPrimary()
	if focused {
		fill = sdl.Color{R: 22, G: 60, B: 96, A: 255}
		border = uiHighlightColor()
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, border)

	// Download glyph: a bar, an arrow head and a tray. SDL's line drawing is one
	// pixel wide, so each stroke is drawn twice to survive a 1280x720 panel.
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glyph)
	cx := rect.X + rect.W/2
	top := rect.Y + rect.H/2 - 6
	for _, dx := range []int32{0, 1} {
		renderer.DrawLine(cx+dx, top, cx+dx, top+8)
	}
	for _, d := range []int32{0, 1} {
		renderer.DrawLine(cx-5, top+3, cx, top+8+d)
		renderer.DrawLine(cx+5, top+3, cx, top+8+d)
	}
	for _, dy := range []int32{0, 1} {
		renderer.DrawLine(cx-6, top+12+dy, cx+6, top+12+dy)
	}

	homeUpdateRect = rect
	return rect
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

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 190)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	smallH := int32(font.Height())
	layout := layoutUpdatePrompt(font, medium)
	x, y, cardW, cardH := layout.card.X, layout.card.Y, layout.card.W, layout.card.H
	fillRoundedRect(renderer, x, y, cardW, cardH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, x, y, cardW, cardH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
	renderer.FillRect(&sdl.Rect{X: x + 18, Y: y + 1, W: cardW - 36, H: 2})

	renderText(renderer, config, medium, "JukaHub update available", ColorTextPrimary(), x+28, layout.titleY)
	for i, line := range layout.bodyLines {
		renderText(renderer, config, font, line, ColorTextTertiary(), x+28, layout.bodyY+int32(i)*smallH)
	}
	for i, line := range layout.hintLines {
		renderText(renderer, config, font, line, ColorTextTertiary(), x+28, layout.hintY+int32(i)*smallH)
	}
	if len(layout.notes) > 0 {
		renderText(renderer, config, font, "WHAT'S NEW", accentColor, x+28, layout.noteLabelY)
		for i, note := range layout.notes {
			// ASCII dash: the bundled handheld fonts are not guaranteed to
			// carry a bullet glyph.
			renderText(renderer, config, font, "- "+note, ColorTextSecondary(), x+28, layout.noteLabelY+smallH+6+int32(i)*smallH)
		}
	}

	// Buttons side by side, right-aligned.
	btnH := int32(42)
	updateW := int32(160)
	laterW := int32(120)
	laterX := x + cardW - 28 - laterW
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
	updatePromptPad      = int32(24)
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
	innerW := cardW - 56

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
