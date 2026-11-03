package main

// ledpanel.go — the LED style panel.
//
// The LED sync already knew how to write a colour to /sys/class/leds, and Quick
// Settings had one cycler for the policy. That left colour, effect and
// brightness reachable only by hand-editing jukauser.json. This panel is the
// missing face for those keys: it edits the same custom-map values the loop
// reads, writes them through the debounced user-config writer, and keeps the
// animation running while it is open so every change is visible on the device.

import (
	"fmt"
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Panel rows, in draw order.
const (
	ledRowMode = iota
	ledRowEffect
	ledRowColor
	ledRowBrightness
	ledRowSync
	ledRowCount
)

// ledEffectOrder is the cycle behind the Effect row. Everything but Solid is a
// brightness curve the loop already computes; Rainbow is a colour rotation.
var ledEffectOrder = []struct {
	key   string
	label string
}{
	{"solid", "Solid"},
	{"pulse", "Pulse"},
	{"rainbow", "Rainbow"},
}

const (
	ledBrightnessMin     = 10 // the panel never offers a black LED
	ledBrightnessMax     = 100
	ledBrightnessDefault = 100
)

type ledPanelState struct {
	open bool
	row  int

	rect sdl.Rect
	hits []sdl.Rect

	// forcedSync is true when opening the panel started the animation for a
	// live preview; closing then stops it again instead of leaving the user's
	// saved "off" quietly overridden.
	forcedSync bool
}

var ledPanel ledPanelState

// ledEffectKey normalises a stored effect name.
func ledEffectKey(raw string) string {
	wanted := strings.ToLower(strings.TrimSpace(raw))
	for _, e := range ledEffectOrder {
		if e.key == wanted {
			return e.key
		}
	}
	return "solid"
}

func ledEffectLabel(key string) string {
	wanted := ledEffectKey(key)
	for _, e := range ledEffectOrder {
		if e.key == wanted {
			return e.label
		}
	}
	return "Solid"
}

func ledEffectIndex(key string) int {
	wanted := ledEffectKey(key)
	for i, e := range ledEffectOrder {
		if e.key == wanted {
			return i
		}
	}
	return 0
}

// ledEffectFromConfig reads the stored effect. The default is the historical
// one: a solid custom colour.
func ledEffectFromConfig(config *Config) string {
	if config == nil {
		return "solid"
	}
	if raw, ok := customString(config.Variables.Custom, ledKeyEffect); ok {
		return ledEffectKey(raw)
	}
	return "solid"
}

// ledBrightnessFromConfig reads the stored brightness as a percentage, with a
// missing or malformed value meaning full brightness.
func ledBrightnessFromConfig(config *Config) int {
	if config == nil {
		return ledBrightnessDefault
	}
	raw, ok := customString(config.Variables.Custom, ledKeyBrightness)
	if !ok {
		return ledBrightnessDefault
	}
	var pct int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &pct); err != nil {
		return ledBrightnessDefault
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ledApplyBrightness scales one colour by a percentage. Brightness is applied
// here, to the colour, rather than to the sysfs brightness file: brightness-mode
// devices derive their level from the peak channel, so scaling the colour dims
// every mode the same way. A zero percentage is an off LED, not a rounding
// accident.
func ledApplyBrightness(r, g, b uint8, pct int) (uint8, uint8, uint8) {
	if pct <= 0 {
		return 0, 0, 0
	}
	if pct > 100 {
		pct = 100
	}
	scale := func(v uint8) uint8 {
		out := int(math.Round(float64(v) * float64(pct) / 100))
		if out > 255 {
			out = 255
		}
		return uint8(out)
	}
	return scale(r), scale(g), scale(b)
}

// ledColorKey is the custom-map hex for one hue. Full-saturation, full-value
// HSV keeps the swatch and the stored value in agreement.
func ledColorKey(hue float64) string {
	r, g, b := hsvToRGB(hue, 1, 1)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// ledHueFromConfig is the hue of the stored custom colour. An unset or
// unparseable colour starts on the app's accent hue instead of red, so the
// panel opens on something that already belongs to the UI.
func ledHueFromConfig(config *Config) float64 {
	if config != nil {
		if hex, ok := customString(config.Variables.Custom, ledKeyColor); ok {
			if r, g, b, ok := parseLEDHex(hex); ok {
				return rgbToHue(r, g, b)
			}
		}
	}
	r, g, b := accentColor.R, accentColor.G, accentColor.B
	return rgbToHue(r, g, b)
}

// rgbToHue converts 8-bit RGB to a hue in [0,1). Grey has no hue; it returns the
// accent's hue so the colour row never jumps to red on its own. Pure.
func rgbToHue(r, g, b uint8) float64 {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	delta := max - min
	if delta == 0 {
		accentR, accentG, accentB := float64(accentColor.R)/255, float64(accentColor.G)/255, float64(accentColor.B)/255
		if accentR == accentG && accentG == accentB {
			return 0.58
		}
		return rgbToHue(accentColor.R, accentColor.G, accentColor.B)
	}
	var h float64
	switch max {
	case rf:
		h = math.Mod((gf-bf)/delta, 6)
	case gf:
		h = (bf-rf)/delta + 2
	default:
		h = (rf-gf)/delta + 4
	}
	h /= 6
	if h < 0 {
		h += 1
	}
	return h
}

// ── Panel lifecycle ────────────────────────────────────────────────────────

func openLEDPanel() {
	if !ledSyncAvailable() {
		ShowToast("No addressable LED on this device", ToastKindInfo)
		return
	}
	qs.open = false
	toolsPanel.open = false
	processPanel.open = false
	ledPanel.open = true
	ledPanel.row = 0
	ledPanel.hits = ledPanel.hits[:0]
	// Live preview: the panel's values mean nothing until the loop writes them.
	if !LEDSyncRunning() {
		if err := SetLEDSync(true); err == nil {
			ledPanel.forcedSync = true
		}
	}
	PlayActivateSound()
}

func closeLEDPanel() {
	ledPanel.open = false
	if ledPanel.forcedSync {
		_ = SetLEDSync(false)
		ledPanel.forcedSync = false
	}
	PlayBackSound()
}

// ledPanelEnsureKeys makes sure the custom map can hold the panel's values; a
// config loaded without a Custom map would panic on the first write.
func ledPanelEnsureKeys(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
}

// ledPanelPersist copies only the LED keys into the user config and saves. The
// whole config is not written back: the panel does not own the rest of it.
func ledPanelPersist(config *Config) {
	if config == nil {
		return
	}
	user := loadUserConfig()
	if user == nil {
		return
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = map[string]interface{}{}
	}
	for _, key := range []string{ledKeyPolicy, ledKeyColor, ledKeyEffect, ledKeyBrightness} {
		if v, ok := config.Variables.Custom[key]; ok {
			user.Variables.Custom[key] = v
		}
	}
	user.Variables.LedSync = config.Variables.LEDSync
	saveUserConfigDebounced(user)
}

// ledPanelSelectPolicy writes a policy into the config, so the running loop
// switches on its next frame.
func ledPanelSelectPolicy(config *Config, key string) {
	if config == nil {
		return
	}
	ledPanelEnsureKeys(config)
	config.Variables.Custom[ledKeyPolicy] = ledPolicyKey(key)
}

// ledPanelUseCustom switches to Custom when a colour or effect is edited, so the
// change is actually the one the lights show.
func ledPanelUseCustom(config *Config) {
	if config == nil {
		return
	}
	if ledPolicyFromConfig(config) != ledPolicyCustom {
		ledPanelSelectPolicy(config, ledPolicyCustom)
	}
}

// ledPanelAdjust moves one row. dir is +1 or -1.
func ledPanelAdjust(config *Config, dir int) {
	if dir == 0 {
		dir = 1
	}
	ledPanelEnsureKeys(config)
	switch ledPanel.row {
	case ledRowMode:
		if config == nil {
			return
		}
		next := nextLEDPolicy(ledPolicyFromConfig(config), dir)
		ledPanelSelectPolicy(config, next)
		ShowToast("LED Mode: "+ledPolicyLabel(next), ToastKindInfo)
	case ledRowEffect:
		effect := ledEffectFromConfig(config)
		idx := ledEffectIndex(effect)
		idx = (idx + dir + len(ledEffectOrder)) % len(ledEffectOrder)
		if config != nil {
			config.Variables.Custom[ledKeyEffect] = ledEffectOrder[idx].key
		}
		ledPanelUseCustom(config)
		ShowToast("LED Effect: "+ledEffectOrder[idx].label, ToastKindInfo)
	case ledRowColor:
		hue := ledHueFromConfig(config) + float64(dir)*15.0/360.0
		if config != nil {
			config.Variables.Custom[ledKeyColor] = ledColorKey(hue)
		}
		ledPanelUseCustom(config)
	case ledRowBrightness:
		pct := ledBrightnessFromConfig(config) + dir*5
		if pct < ledBrightnessMin {
			pct = ledBrightnessMin
		}
		if pct > ledBrightnessMax {
			pct = ledBrightnessMax
		}
		if config != nil {
			config.Variables.Custom[ledKeyBrightness] = pct
		}
	case ledRowSync:
		ledPanelToggleSync(config)
		return
	}
	ledPanelPersist(config)
	PlayNavSound()
}

// ledPanelToggleSync flips the animation itself and remembers it as the user's
// choice, not a preview.
func ledPanelToggleSync(config *Config) {
	if config == nil {
		return
	}
	want := !config.Variables.LEDSync
	if err := SetLEDSync(want); err != nil {
		ShowToast("LED sync: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	config.Variables.LEDSync = want
	ledPanel.forcedSync = false
	ledPanelPersist(config)
	PlayToggleSound()
}

// ledPanelActivate is A/Enter: the next value for a cycler, a preset colour, a
// 50/100 brightness toggle, and the sync switch.
func ledPanelActivate(config *Config) {
	if ledPanel.row == ledRowColor {
		ledPanelEnsureKeys(config)
		hue := ledHueFromConfig(config) + 45.0/360.0
		if config != nil {
			config.Variables.Custom[ledKeyColor] = ledColorKey(hue)
		}
		ledPanelUseCustom(config)
		ledPanelPersist(config)
		PlayNavSound()
		return
	}
	if ledPanel.row == ledRowBrightness {
		pct := ledBrightnessDefault
		if ledBrightnessFromConfig(config) >= 75 {
			pct = 50
		}
		ledPanelEnsureKeys(config)
		if config != nil {
			config.Variables.Custom[ledKeyBrightness] = pct
		}
		ledPanelPersist(config)
		PlayNavSound()
		return
	}
	ledPanelAdjust(config, 1)
}

func ledPanelMove(delta int) {
	ledPanel.row += delta
	if ledPanel.row < 0 {
		ledPanel.row = ledRowCount - 1
	}
	if ledPanel.row >= ledRowCount {
		ledPanel.row = 0
	}
	PlayNavSound()
}

// ── Input ──────────────────────────────────────────────────────────────────

func handleLEDPanelKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !ledPanel.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		ledPanelMove(-1)
	case sdl.K_DOWN:
		ledPanelMove(1)
	case sdl.K_LEFT:
		ledPanelAdjust(config, -1)
	case sdl.K_RIGHT:
		ledPanelAdjust(config, 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		ledPanelActivate(config)
	case sdl.K_ESCAPE, sdl.K_b:
		closeLEDPanel()
	}
	return true
}

func ctrlLEDPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		ledPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		ledPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		ledPanelAdjust(config, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		ledPanelAdjust(config, 1)
	case sdl.CONTROLLER_BUTTON_A:
		ledPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeLEDPanel()
	default:
		return false
	}
	return true
}

// handleLEDPanelMouse selects a clicked row and closes on a click outside. It
// always returns true while the panel is open so the scene underneath never
// receives the click.
func handleLEDPanelMouse(mx, my int32, config *Config) bool {
	if !ledPanel.open {
		return false
	}
	for i, rect := range ledPanel.hits {
		if rect.W > 0 && pointInRect(mx, my, rect) {
			ledPanel.row = i
			ledPanelActivate(config)
			return true
		}
	}
	if ledPanel.rect.W > 0 && !pointInRect(mx, my, ledPanel.rect) {
		closeLEDPanel()
	}
	return true
}

// ── Rendering ──────────────────────────────────────────────────────────────

func renderLEDPanel(renderer *sdl.Renderer, config *Config) {
	if !ledPanel.open || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	panelW := int32(560)
	if panelW > screenWidth-32 {
		panelW = screenWidth - 32
	}
	panelH := int32(390)
	if panelH > screenHeight-32 {
		panelH = screenHeight - 32
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	ledPanel.rect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})

	sub := "LED style"
	if devs := discoverLEDs(ledRootDir); len(devs) > 0 {
		sub = fmt.Sprintf("%d LED%s", len(devs), pluralS(len(devs)))
	}
	if ledPanel.forcedSync {
		// The loop is running only for this panel; say so, because the Sync row
		// still shows the user's saved setting.
		sub += " · live preview"
	}
	header := overlayPageHeader(sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}, "LED", sub, "settings")
	band := UIPanelHeaderBand(config, header, 8)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + band
	footerH := int32(font.Height()) + 20
	pad := OverlayPadX
	rowH := int32(44)
	gap := int32(6)
	rowW := panelW - pad*2
	ledPanel.hits = append(ledPanel.hits[:0], make([]sdl.Rect, ledRowCount)...)

	focus := installHighlight()
	for row := 0; row < ledRowCount; row++ {
		y := bodyY + int32(row)*(rowH+gap)
		rect := sdl.Rect{X: panelX + pad, Y: y, W: rowW, H: rowH}
		ledPanel.hits[row] = rect
		active := row == ledPanel.row
		if active {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(focus, 42))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, WithAlpha(focus, 220))
		}
		labelCol := ColorTextSecondary()
		if active {
			labelCol = ColorTextPrimary()
		}
		ledDrawPanelRow(renderer, config, font, row, rect, labelCol, focus)
	}

	footer := hintLine(
		FooterHint{"A", "Change"},
		FooterHint{"Left/Right", "Adjust"},
		FooterHint{"B / Esc", "Close"},
	)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-footerH+6)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ledDrawPanelRow renders one row: its label, its value and, for the colour and
// brightness rows, the control itself.
func ledDrawPanelRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, row int, rect sdl.Rect, labelCol, focus sdl.Color) {
	labelY := rect.Y + 12
	switch row {
	case ledRowMode:
		mode := ledPolicyFromConfig(config)
		ledRowLabel(renderer, config, font, "Mode", labelCol, rect)
		ledRowValue(renderer, config, font, ledPolicyLabel(mode), rect)
	case ledRowEffect:
		effect := ledEffectFromConfig(config)
		ledRowLabel(renderer, config, font, "Effect", labelCol, rect)
		ledRowValue(renderer, config, font, ledEffectLabel(effect), rect)
		if ledPolicyFromConfig(config) != ledPolicyCustom {
			note := "Custom only"
			nw, _, _ := font.SizeUTF8(note)
			renderText(renderer, config, font, note, ColorTextTertiary(), rect.X+rect.W-int32(nw)-14, labelY+int32(font.Height())+2)
		}
	case ledRowColor:
		ledRowLabel(renderer, config, font, "Colour", labelCol, rect)
		hex := ledColorKey(ledHueFromConfig(config))
		r, g, b, _ := parseLEDHex(hex)
		swatchR := int32(9)
		swatchX := rect.X + rect.W - 14 - swatchR
		swatchY := rect.Y + rect.H/2
		fillCircle(renderer, swatchX, swatchY, swatchR, sdl.Color{R: r, G: g, B: b, A: 255})
		strokeCircle(renderer, swatchX, swatchY, swatchR, ColorBorderDefault)
		txt := strings.ToUpper(hex)
		tw, _, _ := font.SizeUTF8(txt)
		renderText(renderer, config, font, txt, ColorTextSecondary(),
			swatchX-swatchR-10-int32(tw), labelY)
		// Hue strip: the whole wheel, with a marker on the current hue.
		track := sdl.Rect{X: rect.X + 100, Y: rect.Y + rect.H - 14, W: rect.W - 100 - 120, H: 4}
		ledDrawHueStrip(renderer, track)
		markerX := track.X + int32(float64(track.W)*ledHueFromConfig(config))
		fillCircle(renderer, markerX, track.Y+track.H/2, 5, sdl.Color{R: 245, G: 248, B: 255, A: 245})
	case ledRowBrightness:
		pct := ledBrightnessFromConfig(config)
		ledRowLabel(renderer, config, font, "Brightness", labelCol, rect)
		ledRowValue(renderer, config, font, fmt.Sprintf("%d%%", pct), rect)
		track := sdl.Rect{X: rect.X + 120, Y: rect.Y + rect.H - 14, W: rect.W - 120 - 30, H: 4}
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 2, sdl.Color{R: 30, G: 36, B: 50, A: 200})
		frac := float64(pct) / 100.0
		fillW := int32(float64(track.W) * frac)
		if fillW < 1 {
			fillW = 1
		}
		fillRoundedRect(renderer, track.X, track.Y, fillW, track.H, 2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220})
		fillCircle(renderer, track.X+fillW, track.Y+track.H/2, 5, sdl.Color{R: 242, G: 246, B: 255, A: 240})
	case ledRowSync:
		ledRowLabel(renderer, config, font, "LED Sync", labelCol, rect)
		on := config != nil && config.Variables.LEDSync
		text := "Off"
		if on {
			text = "On"
		}
		// The switch itself, drawn like Quick Settings' so the two agree.
		swW, swH := int32(44), int32(24)
		swX := rect.X + rect.W - swW - 14
		swY := rect.Y + (rect.H-swH)/2
		fill := sdl.Color{R: 60, G: 65, B: 80, A: 200}
		if on {
			fill = sdl.Color{R: 80, G: 160, B: 255, A: 255}
		}
		fillRoundedRect(renderer, swX, swY, swW, swH, swH/2, fill)
		knob := int32(8)
		knobX := swX + knob + 4
		if on {
			knobX = swX + swW - knob - 4
		}
		fillCircle(renderer, knobX, swY+swH/2, knob, sdl.Color{R: 255, G: 255, B: 255, A: 240})
		lw, _, _ := font.SizeUTF8(text)
		renderText(renderer, config, font, text, ColorTextTertiary(),
			swX-8-int32(lw), labelY)
	}
}

func ledRowLabel(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, col sdl.Color, rect sdl.Rect) {
	renderText(renderer, config, font, label, col, rect.X+14, rect.Y+12)
}

func ledRowValue(renderer *sdl.Renderer, config *Config, font *ttf.Font, value string, rect sdl.Rect) {
	vw, _, _ := font.SizeUTF8(value)
	renderText(renderer, config, font, value, ColorTextTertiary(), rect.X+rect.W-int32(vw)-16, rect.Y+12)
}

// ledDrawHueStrip paints a cached strip of the colour wheel. The strip is drawn
// as vertical bands: 72 bands across a few hundred pixels reads as a smooth
// gradient on the handheld's panel and costs one rectangle per band.
func ledDrawHueStrip(renderer *sdl.Renderer, rect sdl.Rect) {
	const bands = 72
	bandW := rect.W / bands
	if bandW < 1 {
		bandW = 1
	}
	for i := 0; i < bands; i++ {
		r, g, b := hsvToRGB(float64(i)/float64(bands), 1, 1)
		renderer.SetDrawColor(r, g, b, 255)
		renderer.FillRect(&sdl.Rect{
			X: rect.X + int32(i)*bandW,
			Y: rect.Y,
			W: bandW,
			H: rect.H,
		})
	}
}
