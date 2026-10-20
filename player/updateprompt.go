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
		nc.open || tb.startOpen || processPanel.open || ipPanelOpen || emuPanel.open || gpPanel.open || winAppsPanel.open || portsPanel.open || chiakiPanel.open || raPanel.open || a11y.open ||
		wpOpen || socialNotifOpen || shortcutsOpen || globalSearchOpen || clipSearchOpen {
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

	smallH, medH := int32(font.Height()), int32(medium.Height())
	cardW := int32(620)
	if cardW > screenWidth-64 {
		cardW = screenWidth - 64
	}
	cardH := int32(250)
	if cardH > screenHeight-64 {
		cardH = screenHeight - 64
	}
	x := (screenWidth - cardW) / 2
	y := (screenHeight - cardH) / 2
	fillRoundedRect(renderer, x, y, cardW, cardH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, x, y, cardW, cardH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
	renderer.FillRect(&sdl.Rect{X: x + 18, Y: y + 1, W: cardW - 36, H: 2})

	renderText(renderer, config, medium, "JukaHub update available", ColorTextPrimary(), x+28, y+24)
	version := availableUpdateLabel()
	body := fmt.Sprintf("Release %s is ready to install.\nDownload and restart when you are ready — nothing is installed without your confirmation.", version)
	renderText(renderer, config, font, body, ColorTextTertiary(), x+28, y+24+medH+12)
	renderText(renderer, config, font, "You can always install it later from Tools → Patch.", ColorTextTertiary(), x+28, y+24+medH+12+smallH*2+8)

	// Buttons side by side, right-aligned.
	btnH := int32(42)
	updateW := int32(160)
	laterW := int32(120)
	laterX := x + cardW - 28 - laterW
	updateX := laterX - 12 - updateW
	btnY := y + cardH - 28 - btnH
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
