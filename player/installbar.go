package main

// installbar.go — the "Installing tools" overlay shown while helper tools
// (ffplay / ffmpeg / yt-dlp) are downloaded and installed. The download
// goroutine publishes progress through the installBar* helpers; the render
// loop draws the card once per frame via renderInstallBar.
//
// Visual language follows the toast/scene cards: rounded panel with a soft
// shadow, an accent glyph tile, a pill progress track with a glowing tip, and
// small secondary detail text (bytes done/total and speed).

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// install phases shown in the card.
const (
	installPhaseResolving = "Resolving download"
	installPhaseDownload  = "Downloading"
	installPhaseExtract   = "Installing"
	installPhaseVerify    = "Verifying"
	installPhaseDone      = "Ready"
	installPhaseError     = "Failed"
)

// installBarState is the single source of truth for the overlay. Writers are
// download goroutines; the reader is the render loop on the main thread.
type installBarState struct {
	mu      sync.Mutex
	visible bool
	tool    string
	// label replaces the default "Installing <tool>" title when the operation
	// is not an install of one named thing. A core update run, or the automatic
	// one that starts on its own, says what it is doing instead.
	label   string
	phase   string
	done    int64
	total   int64
	speed   float64 // smoothed bytes/sec
	lastAt  time.Time
	lastLen int64
	message string // error detail or status override
	endedAt time.Time
	animAt  time.Time // first show, for the slide/fade animation
	// retryable keeps a failed install on screen (with an A: retry hint) so a
	// dropped connection or an offline launch does not need an app restart.
	retryable bool
}

var installBar installBarState

// installBarBegin shows the card for a tool install.
func installBarBegin(tool string) {
	installBarBeginLabelled(tool, "")
}

// installBarBeginLabelled is installBarBegin with an explicit title. An empty
// label keeps the default wording, so every existing caller is unchanged.
func installBarBeginLabelled(tool, label string) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	now := time.Now()
	installBar.visible = true
	installBar.tool = tool
	installBar.label = label
	installBar.phase = installPhaseResolving
	installBar.done = 0
	installBar.total = 0
	installBar.speed = 0
	installBar.message = ""
	installBar.lastAt = now
	installBar.lastLen = 0
	installBar.endedAt = time.Time{}
	installBar.animAt = now
	installBar.retryable = false
}

// installBarPhase updates the status line ("Downloading", "Installing", ...).
func installBarPhase(phase string) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	if !installBar.visible {
		return
	}
	installBar.phase = phase
}

// installBarProgress reports download progress in bytes. total may be <= 0
// when the server did not send a Content-Length (indeterminate bar).
func installBarProgress(done, total int64) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	if !installBar.visible {
		return
	}
	now := time.Now()
	if dt := now.Sub(installBar.lastAt); dt >= 250*time.Millisecond {
		instant := float64(done-installBar.lastLen) / dt.Seconds()
		if installBar.speed == 0 {
			installBar.speed = instant
		} else {
			installBar.speed = 0.7*installBar.speed + 0.3*instant
		}
		installBar.lastAt = now
		installBar.lastLen = done
	}
	installBar.done = done
	installBar.total = total
}

// installBarTitle is the custom title of the running operation, or "" when the
// card should use its default wording. Read separately from the snapshot so the
// ten existing call sites of that keep their shape.
func installBarTitle() string {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	return installBar.label
}

// installBarProgressHook reports a multi-step operation's phase and detail
// through the card. It is what the core update passes (the page's and the
// automatic one) hand their runner, so a download started in the background
// still owns the visible progress bar on whatever screen is up.
func installBarProgressHook() func(phase, detail string) {
	return func(phase, detail string) {
		if phase != "" {
			installBarPhase(phase)
		}
		if detail != "" {
			installBarDetail(detail)
		}
	}
}

// installBarDetail replaces the detail line (used for errors and notes).
func installBarDetail(msg string) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	installBar.message = msg
}

// installBarDone marks the install successful; the card shows a check mark
// briefly and then hides itself.
func installBarDone() {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	installBar.phase = installPhaseDone
	installBar.message = ""
	installBar.endedAt = time.Now()
	installBar.retryable = false
}

// installBarAbort hides the card at once. It is for a user cancel, not a
// failure: there is nothing to retry and nothing to report, and leaving the
// bar up would keep a cancelled download looking like work in progress.
func installBarAbort() {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	// The fields are cleared one by one: replacing the whole struct would copy
	// the mutex this function is holding, and the deferred Unlock would then
	// unlock a different one.
	installBar.visible = false
	installBar.tool = ""
	installBar.label = ""
	installBar.phase = ""
	installBar.done = 0
	installBar.total = 0
	installBar.speed = 0
	installBar.lastAt = time.Time{}
	installBar.lastLen = 0
	installBar.message = ""
	installBar.endedAt = time.Time{}
	installBar.retryable = false
}

// installBarFail marks the install failed; the card shows the reason briefly.
func installBarFail(reason string) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	installBar.phase = installPhaseError
	installBar.message = reason
	installBar.endedAt = time.Now()
	installBar.retryable = false
}

// installCardDetail trims a failure reason to what fits the card's detail
// row, so a long wrapped error cannot spill outside the panel.
func installCardDetail(reason string) string {
	const limit = 64
	r := strings.TrimSpace(reason)
	runes := []rune(r)
	if len(runes) <= limit {
		return r
	}
	return string(runes[:limit-1]) + "…"
}

// installBarFailRetry marks the install failed and keeps the card up with an
// "A: retry" hint so the user can redownload after reconnecting.
func installBarFailRetry(tool, reason string) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	installBar.visible = true
	installBar.tool = tool
	installBar.phase = installPhaseError
	installBar.message = installCardDetail(reason)
	installBar.endedAt = time.Now()
	installBar.retryable = true
}

// installBarRetryPending reports the tool whose failed install can be retried
// right now (the card is on screen in its error state).
func installBarRetryPending() (string, bool) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	if !installBar.visible || installBar.phase != installPhaseError || !installBar.retryable || installBar.tool == "" {
		return "", false
	}
	return installBar.tool, true
}

// installBarSnapshot returns the values the renderer needs.
func installBarSnapshot() (visible bool, tool, phase, detail string, done, total int64, speed float64, age time.Duration, ending bool) {
	installBar.mu.Lock()
	defer installBar.mu.Unlock()
	if !installBar.visible {
		return false, "", "", "", 0, 0, 0, 0, false
	}
	// Auto-hide some time after the install settles. A retryable failure
	// stays long enough to read and act on.
	if !installBar.endedAt.IsZero() {
		hold := 2 * time.Second
		if installBar.phase == installPhaseError {
			hold = 4 * time.Second
			if installBar.retryable {
				hold = 20 * time.Second
			}
		}
		if time.Since(installBar.endedAt) > hold {
			installBar.visible = false
			return false, "", "", "", 0, 0, 0, 0, false
		}
	}
	age = time.Since(installBar.animAt)
	detail = installBar.message
	if detail == "" {
		if installBar.phase == installPhaseDownload && installBar.total > 0 {
			detail = fmt.Sprintf("%s of %s", formatInstallBytes(installBar.done), formatInstallBytes(installBar.total))
		}
	}
	return true, installBar.tool, installBar.phase, detail,
		installBar.done, installBar.total, installBar.speed, age, !installBar.endedAt.IsZero()
}

// formatInstallBytes renders byte counts compactly ("78.4 MB", "912 KB").
func formatInstallBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// formatInstallSpeed renders a transfer rate ("3.2 MB/s").
func formatInstallSpeed(bps float64) string {
	if bps <= 0 {
		return ""
	}
	return formatInstallBytes(int64(bps)) + "/s"
}

// colorLuma is perceived brightness (0..255), used to keep the card legible.
func colorLuma(c sdl.Color) float64 {
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}

// installHighlight picks the card's accent. It prefers the theme accent, but
// several themes use a very dark "info" colour that would make the tile and
// progress bar vanish against the panel; in that case it falls back to the
// primary text colour, and finally to a tone that contrasts the panel by
// construction. The result always stands out on ColorPanelRaised.
func installHighlight() sdl.Color {
	panel := ColorPanelRaised
	if math.Abs(colorLuma(accentColor)-colorLuma(panel)) >= 55 {
		return accentColor
	}
	if text := ColorTextPrimary(); math.Abs(colorLuma(text)-colorLuma(panel)) >= 55 {
		return text
	}
	if colorLuma(panel) > 128 {
		return sdl.Color{R: 24, G: 32, B: 48, A: 255}
	}
	return sdl.Color{R: 235, G: 241, B: 250, A: 255}
}

// installPhaseTint is the status colour for each install phase.
func installPhaseTint(phase string) sdl.Color {
	switch phase {
	case installPhaseDone:
		return ColorToastSuccess
	case installPhaseError:
		return ColorToastError
	}
	return ColorTextSecondary()
}

// renderInstallBar draws the installer banner at top-center. It renders
// nothing when no install is running and clears finished state by itself.
func renderInstallBar(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	visible, tool, phase, detail, done, total, speed, age, _ := installBarSnapshot()
	label := installBarTitle()
	if !visible {
		return
	}

	font, _ := getCachedFont(config, "small")
	titleFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if titleFont == nil {
		titleFont = font
	}

	// Ease-out slide-in from above. The card is drawn fully opaque: the text
	// cache is keyed by colour, so per-frame alpha ramps would churn it, and a
	// 140ms slide reads as a deliberate entrance on its own.
	slide := int32(0)
	if age < 240*time.Millisecond {
		t := float64(age) / float64(240*time.Millisecond)
		ease := 1 - (1-t)*(1-t)
		slide = int32(16 * (1 - ease))
	}

	const (
		cardW  = int32(560)
		padX   = int32(20)
		padY   = int32(18)
		tile   = int32(42)
		gap    = int32(14)
		barMin = int32(8)
	)

	titleH := int32(titleFont.Height())
	smallH := int32(font.Height())
	if titleH <= 0 {
		titleH = 22
	}
	if smallH <= 0 {
		smallH = 16
	}

	// Layout is measured from the fonts so it stays balanced on small screens.
	barY := padY + titleH + 16
	detailY := barY + barMin + 10
	cardH := detailY + smallH + padY

	x := (screenWidth - cardW) / 2
	barH := int32(HomeTopBarH)
	if screenHeight < 600 {
		barH = HomeTopBarHSmall
	}
	// Top-center, just below the status bar — a banner rather than a dialog, so
	// it never collides with the bottom-right toast stack or the footer hints.
	y := barH + 14 - slide

	drawLayer := func(offset int32, c sdl.Color) {
		fillRoundedRect(renderer, x+offset, y+offset*2, cardW, cardH, 16, c)
	}
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// Soft drop shadow, then the panel, then a hairline border and accent
	// underline so the banner reads as a raised surface over the scene.
	drawLayer(2, ShadowFill(70))
	panel := ColorPanelRaised
	fillRoundedRect(renderer, x, y, cardW, cardH, 16, panel)
	strokeRoundedRect(renderer, x, y, cardW, cardH, 16, 1, ColorBorderDefault)
	highlight := installHighlight()
	renderer.SetDrawColor(highlight.R, highlight.G, highlight.B, 90)
	renderer.FillRect(&sdl.Rect{X: x + 16, Y: y + cardH - 2, W: cardW - 32, H: 1})

	// Accent glyph tile: download arrow while working, check when done. The
	// glyph colour is derived from the tile so it stays readable whichever
	// accent the active theme supplies.
	tileColor := highlight
	glyph := "↓"
	switch phase {
	case installPhaseDone:
		glyph = "✓"
		tileColor = ColorToastSuccess
	case installPhaseError:
		glyph = "!"
		tileColor = ColorToastError
	}
	tx := x + padX
	ty := y + padY
	fillRoundedRect(renderer, tx, ty, tile, tile, 12, tileColor)
	if gw, gh, _ := titleFont.SizeUTF8(glyph); gw > 0 && gh > 0 {
		renderText(renderer, config, titleFont, glyph, contrastText(tileColor),
			tx+(tile-int32(gw))/2, ty+(tile-int32(gh))/2)
	}

	// Title row: "Installing ffplay" + phase/target on the right. A labelled
	// card (a core update run) names the operation instead; its outcome is the
	// glyph and the phase tint, which is the same reading an install gets.
	textX := tx + tile + gap
	title := "Installing " + tool
	if label != "" {
		title = label
	} else {
		switch phase {
		case installPhaseDone:
			title = tool + " installed"
		case installPhaseError:
			title = "Could not install " + tool
		}
	}
	renderText(renderer, config, titleFont, title, ColorTextPrimary(), textX, y+padY)

	status := phase
	if speed > 0 && phase == installPhaseDownload {
		status = formatInstallSpeed(speed)
	}
	if phase == installPhaseError && installBar.retryable {
		status = hintLine(FooterHint{"A", "Retry"})
	}
	if status != "" {
		pw, _, _ := font.SizeUTF8(status)
		renderText(renderer, config, font, status, installPhaseTint(phase),
			x+cardW-padX-int32(pw), y+padY+(titleH-smallH)/2+1)
	}

	// Progress pill with a bright travelling tip.
	barX := textX
	barW := cardW - (textX - x) - padX
	fillRoundedRect(renderer, barX, y+barY, barW, barMin, barMin/2, ColorTrack)

	frac := 0.0
	shimmer := true
	switch {
	case phase == installPhaseDone:
		frac, shimmer = 1.0, false
	case phase == installPhaseError:
		frac, shimmer = 0.0, false
	case total > 0:
		frac = float64(done) / float64(total)
		if frac > 1 {
			frac = 1
		}
		shimmer = false
	}
	fillColor := highlight
	if phase == installPhaseDone {
		fillColor = ColorToastSuccess
	}
	if !shimmer && frac > 0 {
		fillW := int32(float64(barW-4) * frac)
		if fillW < barMin {
			fillW = barMin
		}
		renderWithClip(renderer, barX, y+barY, barW, barMin, func(r *sdl.Renderer) {
			fillRoundedRect(r, barX+2, y+barY+2, fillW, barMin-4, (barMin-4)/2, fillColor)
			// Brighter cap at the moving tip.
			cap := fillColor
			cap.R = uint8(int(cap.R)/2 + 127)
			cap.G = uint8(int(cap.G)/2 + 127)
			cap.B = uint8(int(cap.B)/2 + 127)
			fillRoundedRect(r, barX+2+fillW-barMin+2, y+barY+2, barMin-2, barMin-4, (barMin-4)/2, cap)
		})
	}
	if shimmer {
		// Indeterminate: a soft shimmer travelling inside the track.
		span := barW / 3
		period := 1300 * time.Millisecond
		shift := int32(float64(barW+span) * (float64(age%period) / float64(period)))
		sc := highlight
		sc.A = 150
		renderWithClip(renderer, barX, y+barY, barW, barMin, func(r *sdl.Renderer) {
			fillRoundedRect(r, barX+2+shift-span, y+barY+2, span, barMin-4, (barMin-4)/2, sc)
		})
	}

	// Detail row: bytes (or message) on the left, percent on the right.
	detailColor := ColorTextSecondary()
	if detail == "" {
		detail = "Preparing package"
	}
	if phase == installPhaseDone {
		detail = "Ready — restart JukaHub to use it"
		detailColor = ColorTextSecondary()
	}
	if d, _, _ := font.SizeUTF8(detail); d > 0 {
		renderText(renderer, config, font, detail, detailColor, textX, y+detailY)
	}
	if phase == installPhaseDownload && total > 0 {
		pct := fmt.Sprintf("%d%%", int(frac*100+0.5))
		pw, _, _ := titleFont.SizeUTF8(pct)
		renderText(renderer, config, titleFont, pct, highlight, x+cardW-padX-int32(pw), y+detailY-4)
	}
}
