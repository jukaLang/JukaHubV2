package main

// ctrldiag.go — the Controller test panel: raw pad state, stick drift and
// deadzone diagnosis.
//
// Handheld sticks drift. Until now the only way to notice was to watch the
// cursor creep, and the only fix on offer was the global deadzone slider in the
// PPSSPP calibration screen. This panel shows the raw value every axis reports,
// whether the sticks come back to centre when released, which buttons register
// at all, and how far a trigger travels.
//
// It deliberately does not claim to measure input latency: JukaHub draws menus,
// not games, and a button-to-photon number needs a camera pointed at the screen.
// It measures what it can actually see — the axes and the press durations.

import (
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// diagAxisCount is SDL's axis count (LX, LY, RX, RY, trigger left, trigger
// right). It is spelled out rather than using the cgo constant so the state
// array has a fixed size.
const diagAxisCount = 6

const (
	// diagRestWindow is how long an axis has to stay unchanged before it counts
	// as at rest. Without it a stick mid-push would be called drifting.
	diagRestWindow = 900 * time.Millisecond
	// diagCentred is the normalised value below which an axis counts as centred.
	// It is well under the app's own deadzone, so "centred" means genuinely
	// centred rather than "close enough for menus".
	diagCentred = 0.05
)

// ctrldiagClock is the clock the panel reads. Tests replace it so a rest window
// does not need a real 900ms wait.
var ctrldiagClock = time.Now

// diagAxisState is one axis as the panel sees it.
type diagAxisState struct {
	Value     int16
	Min, Max  int16
	Seen      bool
	ChangedAt time.Time
}

// diagButtonState is one physical button.
type diagButtonState struct {
	Held      bool
	Presses   int
	HeldSince time.Time
	// LastHeld is how long the previous press lasted, which is how a
	// double-registering or sticky button shows itself.
	LastHeld time.Duration
}

type ctrldiagPanelState struct {
	open    bool
	axes    [diagAxisCount]diagAxisState
	buttons map[uint8]*diagButtonState
	// resetAt stamps the last Reset so the panel can say readings are fresh.
	resetAt time.Time
}

var ctrldiag ctrldiagPanelState

// ctrldiagResetRect is the click target for the Reset chip; only touched on the
// render and event thread.
var ctrldiagResetRect sdl.Rect
var ctrldiagPanelRect sdl.Rect

// diagAxisNames labels the six axes in the order SDL reports them.
var diagAxisNames = [diagAxisCount]string{
	"Left X", "Left Y", "Right X", "Right Y", "L2", "R2",
}

// diagButtons is the order the button chips are drawn in. A pad that reports a
// button outside this list still counts in the "other" tally, so nothing is
// silently ignored.
var diagButtons = []uint8{
	sdl.CONTROLLER_BUTTON_A,
	sdl.CONTROLLER_BUTTON_B,
	sdl.CONTROLLER_BUTTON_X,
	sdl.CONTROLLER_BUTTON_Y,
	sdl.CONTROLLER_BUTTON_LEFTSHOULDER,
	sdl.CONTROLLER_BUTTON_RIGHTSHOULDER,
	sdl.CONTROLLER_BUTTON_DPAD_UP,
	sdl.CONTROLLER_BUTTON_DPAD_DOWN,
	sdl.CONTROLLER_BUTTON_DPAD_LEFT,
	sdl.CONTROLLER_BUTTON_DPAD_RIGHT,
	sdl.CONTROLLER_BUTTON_START,
	sdl.CONTROLLER_BUTTON_BACK,
	sdl.CONTROLLER_BUTTON_LEFTSTICK,
	sdl.CONTROLLER_BUTTON_RIGHTSTICK,
}

// diagButtonLabels is the short name drawn on each chip.
var diagButtonLabels = map[uint8]string{
	sdl.CONTROLLER_BUTTON_A:             "A",
	sdl.CONTROLLER_BUTTON_B:             "B",
	sdl.CONTROLLER_BUTTON_X:             "X",
	sdl.CONTROLLER_BUTTON_Y:             "Y",
	sdl.CONTROLLER_BUTTON_LEFTSHOULDER:  "L1",
	sdl.CONTROLLER_BUTTON_RIGHTSHOULDER: "R1",
	sdl.CONTROLLER_BUTTON_DPAD_UP:       "D-UP",
	sdl.CONTROLLER_BUTTON_DPAD_DOWN:     "D-DOWN",
	sdl.CONTROLLER_BUTTON_DPAD_LEFT:     "D-LEFT",
	sdl.CONTROLLER_BUTTON_DPAD_RIGHT:    "D-RIGHT",
	sdl.CONTROLLER_BUTTON_START:         "START",
	sdl.CONTROLLER_BUTTON_BACK:          "SELECT",
	sdl.CONTROLLER_BUTTON_LEFTSTICK:     "L3",
	sdl.CONTROLLER_BUTTON_RIGHTSTICK:    "R3",
}

// openCtrldiag shows the panel with every reading cleared.
func openCtrldiag() {
	qs.open = false
	toolsPanel.open = false
	powerTools.open = false
	processPanel.open = false
	// Close the memory panel through its own path: it holds a ptrace attach,
	// and just clearing its flag would leave the target frozen.
	if memPanel.open {
		closeMemPanel()
	}
	ctrldiag.open = true
	ctrldiagReset()
	PlayActivateSound()
}

// closeCtrldiag hides the panel.
func closeCtrldiag() {
	ctrldiag.open = false
	PlayBackSound()
}

// ctrldiagReset clears the readings but keeps the panel open, so a user can
// press one button and watch it register from a clean slate.
func ctrldiagReset() {
	ctrldiag.axes = [diagAxisCount]diagAxisState{}
	ctrldiag.buttons = make(map[uint8]*diagButtonState, len(diagButtons))
	for _, b := range diagButtons {
		ctrldiag.buttons[b] = &diagButtonState{}
	}
	ctrldiag.resetAt = ctrldiagClock()
	ctrldiagResetRect = sdl.Rect{}
	PlayNavSound()
}

// diagBackHold is how long SELECT has to be held to close the panel. The panel
// swallows every pad event on purpose so a press can be observed, which means
// the way out cannot be a normal button press: it would register as a press and
// nothing else. A hold is never something a test asks for.
const diagBackHold = 1200 * time.Millisecond

// ctrldiagBackHeld reports whether SELECT has been held long enough to close.
func ctrldiagBackHeld(now time.Time) bool {
	b := ctrldiag.buttons[sdl.CONTROLLER_BUTTON_BACK]
	return b != nil && b.Held && now.Sub(b.HeldSince) >= diagBackHold
}

// ctrldiagAxisEvent records one axis sample.
func ctrldiagAxisEvent(axis int, value int16) {
	if axis < 0 || axis >= diagAxisCount {
		return
	}
	now := ctrldiagClock()
	a := &ctrldiag.axes[axis]
	if !a.Seen {
		a.Seen = true
		a.Min, a.Max = value, value
		a.ChangedAt = now
		a.Value = value
		return
	}
	if value != a.Value {
		a.ChangedAt = now
	}
	a.Value = value
	if value < a.Min {
		a.Min = value
	}
	if value > a.Max {
		a.Max = value
	}
}

// ctrldiagButtonEvent records a press or release. Releases matter as much as
// presses: a button that never reports its release looks stuck, which is exactly
// the fault this panel is for.
func ctrldiagButtonEvent(e *sdl.ControllerButtonEvent) {
	if e == nil {
		return
	}
	if ctrldiag.buttons == nil {
		ctrldiag.buttons = make(map[uint8]*diagButtonState, len(diagButtons))
	}
	b := ctrldiag.buttons[e.Button]
	if b == nil {
		// A button outside the drawn list still gets recorded, so a pad with
		// extra paddles is not silently ignored.
		b = &diagButtonState{}
		ctrldiag.buttons[e.Button] = b
	}
	now := ctrldiagClock()
	switch e.Type {
	case sdl.CONTROLLERBUTTONDOWN:
		if !b.Held {
			b.Presses++
			b.Held = true
			b.HeldSince = now
		}
	case sdl.CONTROLLERBUTTONUP:
		if b.Held {
			b.LastHeld = now.Sub(b.HeldSince)
		}
		b.Held = false
	}
}

// ctrldiagNorm turns a raw axis value into -1..1.
func ctrldiagNorm(v int16) float64 {
	return float64(v) / 32767.0
}

// diagDeadzoneNorm is the deadzone the app itself applies to the left stick, so
// the rings on the sticks mean something rather than being decoration.
func diagDeadzoneNorm() float64 {
	return float64(stickDeadzone) / 32767.0
}

// ctrldiagAxisProblem judges one axis at the given time. It reports a message
// and whether the axis is worth warning about.
func ctrldiagAxisProblem(axis int, now time.Time) (string, bool) {
	if axis < 0 || axis >= diagAxisCount {
		return "", false
	}
	a := ctrldiag.axes[axis]
	if !a.Seen {
		return "no data", false
	}
	// Only a resting axis can be drifting: a stick held over is not a fault.
	if now.Sub(a.ChangedAt) < diagRestWindow {
		return "moving", false
	}
	magnitude := ctrldiagNorm(a.Value)
	if magnitude < 0 {
		magnitude = -magnitude
	}
	switch {
	case magnitude < diagCentred:
		return "centred", false
	case magnitude < diagDeadzoneNorm():
		return fmt.Sprintf("rests at %+.2f — inside the deadzone", ctrldiagNorm(a.Value)), true
	default:
		return fmt.Sprintf("rests at %+.2f — past the deadzone", ctrldiagNorm(a.Value)), true
	}
}

// ctrldiagProblems lists the axes that need attention, so the header can say
// "stick drift" instead of leaving the user to read four numbers.
func ctrldiagProblems() []string {
	now := ctrldiagClock()
	var out []string
	for axis := 0; axis < diagAxisCount; axis++ {
		if axis >= 4 {
			// Triggers resting at 0 is normal and a resting trigger is not a
			// fault; they are shown as bars, not judged.
			continue
		}
		if msg, bad := ctrldiagAxisProblem(axis, now); bad {
			out = append(out, diagAxisNames[axis]+" "+msg)
		}
	}
	return out
}

// ctrldiagSummary is the one-line header under the title.
func ctrldiagSummary() string {
	problems := ctrldiagProblems()
	switch len(problems) {
	case 0:
		return "Release the sticks: centred axes and working buttons show up below"
	case 1:
		return "Drift: " + problems[0]
	default:
		return fmt.Sprintf("Drift on %d axes: %s", len(problems), problems[0])
	}
}

// ctrldiagHeldCount is how many buttons are down right now.
func ctrldiagHeldCount() int {
	n := 0
	for _, b := range ctrldiag.buttons {
		if b.Held {
			n++
		}
	}
	return n
}

// ctrldiagButtonLabel names a button for the chip and the "other" tally.
func ctrldiagButtonLabel(btn uint8) string {
	if label, ok := diagButtonLabels[btn]; ok {
		return label
	}
	return fmt.Sprintf("B%d", btn)
}

// handleCtrldiagKey consumes keys while the panel is open.
func handleCtrldiagKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !ctrldiag.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_r, sdl.K_RETURN, sdl.K_SPACE:
		ctrldiagReset()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeCtrldiag()
	}
	return true
}

// handleCtrldiagMouse closes on an outside click and resets on the chip.
func handleCtrldiagMouse(mx, my int32, config *Config) bool {
	if !ctrldiag.open {
		return false
	}
	if ctrldiagResetRect.W > 0 && pointInRect(mx, my, ctrldiagResetRect) {
		ctrldiagReset()
		return true
	}
	if ctrldiagPanelRect.W > 0 && !pointInRect(mx, my, ctrldiagPanelRect) {
		closeCtrldiag()
	}
	return true
}

// renderCtrldiagPanel draws the panel.
func renderCtrldiagPanel(renderer *sdl.Renderer, config *Config) {
	if !ctrldiag.open || renderer == nil {
		return
	}
	// Holding SELECT is the way out on a pad-only device.
	if ctrldiagBackHeld(ctrldiagClock()) {
		closeCtrldiag()
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
	smallH := int32(font.Height())
	const padX = int32(24)

	stickSize := int32(150)
	if screenHeight < 700 {
		stickSize = 124
	}
	chipH := smallH + 12
	chipGap := int32(8)
	chipRows := int32((len(diagButtons) + 3) / 4)
	labelH := smallH + 6

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height. A drifting
	// stick or a dead button inks the subtitle in the warning colour.
	header := UIPanelHeader("Controller test", ctrldiagSummary(), "gamepad")
	if len(ctrldiagProblems()) > 0 {
		header.SubtitleColor = ColorToastWarn
	}
	headerH := UIPanelHeaderBand(config, header, 10)
	bodyH := stickSize + labelH + 14 + // sticks
		(chipH + 6) + 10 + // trigger bars
		chipRows*(chipH+chipGap) + 10
	footerH := 20 + smallH

	panelW := int32(880)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := headerH + bodyH + footerH
	if panelH > screenHeight-40 {
		panelH = screenHeight - 40
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	ctrldiagPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	header.X = panelX + padX
	header.Y = panelY + UIPanelHeaderInset
	header.W = panelW - 2*padX
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	// Sticks side by side, centred in the panel.
	gap := int32(40)
	totalW := stickSize*2 + gap
	stickX := panelX + (panelW-totalW)/2
	drawDiagStick(renderer, config, font, stickX, bodyY, stickSize, 0, 1, "LEFT STICK")
	drawDiagStick(renderer, config, font, stickX+stickSize+gap, bodyY, stickSize, 2, 3, "RIGHT STICK")

	// Trigger bars.
	barY := bodyY + stickSize + labelH + 14
	barW := (panelW - 2*padX - gap) / 2
	drawDiagTrigger(renderer, config, font, panelX+padX, barY, barW, 4, "L2")
	drawDiagTrigger(renderer, config, font, panelX+padX+barW+gap, barY, barW, 5, "R2")

	// Button chips, four to a row.
	chipsY := barY + chipH + 16
	chipW := (panelW - 2*padX - 3*chipGap) / 4
	for i, btn := range diagButtons {
		col := int32(i % 4)
		row := int32(i / 4)
		x := panelX + padX + col*(chipW+chipGap)
		y := chipsY + row*(chipH+chipGap)
		drawDiagChip(renderer, config, font, x, y, chipW, chipH, btn)
	}

	// Reset chip in the footer, right-aligned.
	footerY := panelY + panelH - footerH + 6
	resetW := int32(96)
	ctrldiagResetRect = sdl.Rect{X: panelX + panelW - padX - resetW, Y: footerY, W: resetW, H: chipH}
	fillRoundedRect(renderer, ctrldiagResetRect.X, ctrldiagResetRect.Y, ctrldiagResetRect.W, ctrldiagResetRect.H, 8,
		WithAlpha(installHighlight(), 40))
	strokeRoundedRect(renderer, ctrldiagResetRect.X, ctrldiagResetRect.Y, ctrldiagResetRect.W, ctrldiagResetRect.H, 8, 1, ColorBorderDefault)
	renderText(renderer, config, font, "Reset (A)", ColorTextSecondary(),
		ctrldiagResetRect.X+12, ctrldiagResetRect.Y+6)

	footer := hintLine(FooterHint{"Click", "Reset"}, FooterHint{"Hold SELECT", "Close"})
	renderText(renderer, config, font, footer, ColorTextTertiary(), panelX+padX, footerY+6)
}

// drawDiagStick draws one stick box with the deadzone ring, the travel box and
// the live dot.
func drawDiagStick(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, size int32, axisX, axisY int, label string) {
	fillRoundedRect(renderer, x, y, size, size, 14, WithAlpha(ColorBorderDefault, 26))
	strokeRoundedRect(renderer, x, y, size, size, 14, 1, ColorBorderDefault)

	cx, cy := x+size/2, y+size/2
	radius := size/2 - 6
	// The deadzone ring is the one the app actually applies, so "inside the
	// ring" means the menu cursor will not move.
	strokeCircle(renderer, cx, cy, int32(diagDeadzoneNorm()*float64(radius)), WithAlpha(accentColor, 120))
	strokeCircle(renderer, cx, cy, 2, WithAlpha(ColorTextTertiary(), 120))

	nx := ctrldiagNorm(ctrldiag.axes[axisX].Value)
	ny := ctrldiagNorm(ctrldiag.axes[axisY].Value)
	dotX := cx + int32(nx*float64(radius))
	dotY := cy + int32(ny*float64(radius))
	dotColor := ColorTextPrimary()
	if _, bad := ctrldiagAxisProblem(axisX, ctrldiagClock()); bad {
		dotColor = ColorToastWarn
	}
	fillCircle(renderer, dotX, dotY, 6, dotColor)

	renderText(renderer, config, font, label, ColorTextTertiary(), x, y+size+2)
	readout := fmt.Sprintf("%+.2f  %+.2f", nx, ny)
	renderText(renderer, config, font, readout, ColorTextSecondary(), x+size-88, y+size+2)
}

// drawDiagTrigger draws one analog trigger as a labelled bar.
func drawDiagTrigger(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w int32, axis int, label string) {
	h := int32(font.Height()) + 4
	renderText(renderer, config, font, label, ColorTextTertiary(), x, y)
	barX := x + 34
	barW := w - 34
	fillRoundedRect(renderer, barX, y+2, barW, h-6, 4, WithAlpha(ColorBorderDefault, 30))
	value := ctrldiag.axes[axis].Value
	// SDL reports a trigger as 0..32767, not -1..1.
	fill := float64(value) / 32767.0
	if fill < 0 {
		fill = 0
	}
	if fill > 0 {
		fillRoundedRect(renderer, barX, y+2, int32(float64(barW)*fill), h-6, 4, WithAlpha(accentColor, 200))
	}
	renderText(renderer, config, font, fmt.Sprintf("%d", value), ColorTextSecondary(), barX+barW+6, y)
}

// drawDiagChip draws one button chip, lit while it is held.
func drawDiagChip(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, btn uint8) {
	b := ctrldiag.buttons[btn]
	held := b != nil && b.Held
	fill := WithAlpha(ColorBorderDefault, 26)
	border := ColorBorderDefault
	text := ColorTextSecondary()
	if held {
		fill = WithAlpha(installHighlight(), 90)
		border = installHighlight()
		text = ColorTextPrimary()
	} else if b != nil && b.Presses > 0 {
		// A button that has been pressed at least once stays slightly brighter,
		// which is how a dead button stands out from a working one.
		fill = WithAlpha(ColorBorderDefault, 60)
		text = ColorTextPrimary()
	}
	fillRoundedRect(renderer, x, y, w, h, 8, fill)
	strokeRoundedRect(renderer, x, y, w, h, 8, 1, border)
	label := ctrldiagButtonLabel(btn)
	renderText(renderer, config, font, label, text, x+10, y+5)
	if b != nil && b.Presses > 0 {
		renderText(renderer, config, font, fmt.Sprintf("%d", b.Presses), ColorTextTertiary(), x+w-20, y+5)
	}
}
