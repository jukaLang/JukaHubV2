package main

// musicpeq.go — the 31-band parametric equaliser and the bit-perfect path.
//
// The bands are the ISO one-third-octave centres, so a preset written down for
// a hardware EQ (or copied from a review) maps straight onto the sliders. The
// gains become one ffmpeg filter graph, which means the equaliser costs nothing
// in Go: ffmpeg already decodes the file, so it also shapes it.
//
// Bit-perfect is the opposite switch: it throws the filter graph away (no EQ,
// no tempo change) and hands the decoded samples to the device untouched. The
// two cannot be on at once, because an equaliser by definition rewrites the
// samples.

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	musicPEQBands   = 31
	musicPEQMaxGain = 12.0 // dB either way, like the hardware EQs this mirrors
	musicPEQMinGain = -12.0
)

// musicPEQFrequencies are the ISO 1/3-octave centre frequencies, low to high.
var musicPEQFrequencies = [musicPEQBands]float64{
	20, 25, 31.5, 40, 50, 63, 80, 100, 125, 160,
	200, 250, 315, 400, 500, 630, 800, 1000, 1250, 1600,
	2000, 2500, 3150, 4000, 5000, 6300, 8000, 10000, 12500, 16000,
	20000,
}

// musicBandLabel is the short axis label: "20", "31.5", "1k", "16k".
func musicBandLabel(freq float64) string {
	if freq >= 1000 {
		v := freq / 1000
		if v == math.Trunc(v) {
			return strconv.Itoa(int(v)) + "k"
		}
		return strconv.FormatFloat(v, 'f', 1, 64) + "k"
	}
	if freq == math.Trunc(freq) {
		return strconv.Itoa(int(freq))
	}
	return strconv.FormatFloat(freq, 'f', 1, 64)
}

// musicPEQState is the live equaliser. The panel is closed on leaving the
// player, but the gains and the on/off switch are user settings.
type musicPEQState struct {
	enabled    bool
	bitPerfect bool
	gains      [musicPEQBands]float64
	preset     string
	panel      bool
	sel        int
}

var musicEQ = musicPEQState{preset: "Flat"}

// musicPEQLoaded keeps the config read once per run: every edit writes back
// immediately, so a later read would only undo an unsaved experiment.
var musicPEQLoaded bool

// musicPEQEnsureLoaded restores the saved curve the first time the player or
// the decoder needs it.
func musicPEQEnsureLoaded() {
	if musicPEQLoaded {
		return
	}
	musicPEQLoaded = true
	musicPEQLoad(appConfig)
}

// musicPEQFilterGraph renders the gains as an ffmpeg filter chain. Bands within
// 0.2 dB of flat are skipped: an untouched equaliser should cost nothing, and
// a long chain of 0 dB shelf filters is not free on a handheld CPU.
func musicPEQFilterGraph(gains [musicPEQBands]float64) string {
	parts := make([]string, 0, musicPEQBands)
	for i, g := range gains {
		if math.Abs(g) < 0.2 {
			continue
		}
		if g > musicPEQMaxGain {
			g = musicPEQMaxGain
		}
		if g < musicPEQMinGain {
			g = musicPEQMinGain
		}
		parts = append(parts, fmt.Sprintf("equalizer=f=%s:t=q:w=1:g=%.1f",
			strconv.FormatFloat(musicPEQFrequencies[i], 'f', -1, 64), g))
	}
	return strings.Join(parts, ",")
}

// musicAudioFilterChain is the whole -af argument: the equaliser first, then
// the tempo change so the EQ works on the original timing. It returns "" when
// nothing should be processed, which is exactly the bit-perfect case.
func musicAudioFilterChain(speed float64, enabled, bitPerfect bool, gains [musicPEQBands]float64) string {
	if bitPerfect {
		return ""
	}
	parts := make([]string, 0, 2)
	if enabled {
		if graph := musicPEQFilterGraph(gains); graph != "" {
			parts = append(parts, graph)
		}
	}
	if speed > 0 && math.Abs(speed-1) > 0.01 {
		parts = append(parts, fmt.Sprintf("atempo=%.2f", speed))
	}
	return strings.Join(parts, ",")
}

// musicPEQCurrentChain reads the live state for the decoder.
func musicPEQCurrentChain(speed float64) string {
	musicPEQEnsureLoaded()
	return musicAudioFilterChain(speed, musicEQ.enabled, musicEQ.bitPerfect, musicEQ.gains)
}

// ──────────────────────────────────────────────────────────────────────
// Presets
// ──────────────────────────────────────────────────────────────────────

// musicPEQPresetGains builds a curve from per-band rules so each preset reads
// as an intent rather than 31 magic numbers.
func musicPEQPresetGains(name string) [musicPEQBands]float64 {
	var g [musicPEQBands]float64
	for i, f := range musicPEQFrequencies {
		switch strings.ToLower(name) {
		case "bass":
			switch {
			case f <= 80:
				g[i] = 5
			case f <= 250:
				g[i] = 2
			case f >= 8000:
				g[i] = -1
			}
		case "vocal":
			if f >= 1000 && f <= 4000 {
				g[i] = 3
			}
			if f <= 63 {
				g[i] = -4
			}
		case "rock":
			switch {
			case f <= 100:
				g[i] = 5
			case f >= 200 && f <= 500:
				g[i] = -2
			case f >= 2000 && f <= 5000:
				g[i] = 3
			}
		case "night":
			// Quiet listening: lift the quiet details, tame the peaks.
			if f <= 100 {
				g[i] = -4
			}
			if f >= 1000 && f <= 6300 {
				g[i] = 2
			}
		}
	}
	return g
}

// musicPEQPresetNames is the order Y steps through.
var musicPEQPresetNames = []string{"Flat", "Bass", "Vocal", "Rock", "Night"}

// musicPEQCyclePreset returns the next preset and its curve.
func musicPEQCyclePreset(current string) (string, [musicPEQBands]float64) {
	next := musicPEQPresetNames[0]
	for i, name := range musicPEQPresetNames {
		if strings.EqualFold(name, current) {
			next = musicPEQPresetNames[(i+1)%len(musicPEQPresetNames)]
			break
		}
	}
	return next, musicPEQPresetGains(next)
}

// musicPEQClampGain keeps one band inside the slider range.
func musicPEQClampGain(g float64) float64 {
	if g > musicPEQMaxGain {
		return musicPEQMaxGain
	}
	if g < musicPEQMinGain {
		return musicPEQMinGain
	}
	return g
}

// musicPEQMoveSelection wraps the band cursor across the whole scale.
func musicPEQMoveSelection(sel, delta int) int {
	sel += delta
	if sel < 0 {
		sel = musicPEQBands - 1
	}
	if sel >= musicPEQBands {
		sel = 0
	}
	return sel
}

// ──────────────────────────────────────────────────────────────────────
// Persistence
// ──────────────────────────────────────────────────────────────────────

const (
	musicPEQOnKey      = "musicPEQOn"
	musicPEQGainsKey   = "musicPEQGains"
	musicBitPerfectKey = "musicBitPerfect"
)

// musicPEQLoad restores the equaliser from the config. A malformed gain string
// leaves the curve flat instead of failing to start the player.
func musicPEQLoad(config *Config) {
	if config == nil {
		return
	}
	musicEQ.enabled = customBool(config.Variables.Custom, musicPEQOnKey, false)
	musicEQ.bitPerfect = customBool(config.Variables.Custom, musicBitPerfectKey, false)
	if musicEQ.bitPerfect {
		musicEQ.enabled = false
	}
	gains, ok := parseMusicPEQGains(customStringValue(config.Variables.Custom, musicPEQGainsKey))
	if ok {
		musicEQ.gains = gains
	}
	musicEQ.preset = musicPEQMatchPreset(musicEQ.gains)
	musicPEQLoaded = true
}

// musicPEQSave writes the equaliser back into the config's custom variables.
func musicPEQSave(config *Config) {
	if config == nil {
		return
	}
	musicPEQLoaded = true
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicPEQOnKey] = musicEQ.enabled
	config.Variables.Custom[musicBitPerfectKey] = musicEQ.bitPerfect
	config.Variables.Custom[musicPEQGainsKey] = formatMusicPEQGains(musicEQ.gains)
}

// formatMusicPEQGains is the config form: 31 comma-separated dB values.
func formatMusicPEQGains(gains [musicPEQBands]float64) string {
	parts := make([]string, 0, musicPEQBands)
	for _, g := range gains {
		parts = append(parts, strconv.FormatFloat(math.Round(g*10)/10, 'f', -1, 64))
	}
	return strings.Join(parts, ",")
}

// parseMusicPEQGains accepts exactly 31 numbers; anything else is rejected so a
// half-written config cannot bend a user's audio.
func parseMusicPEQGains(raw string) ([musicPEQBands]float64, bool) {
	var gains [musicPEQBands]float64
	if strings.TrimSpace(raw) == "" {
		return gains, false
	}
	fields := strings.Split(raw, ",")
	if len(fields) != musicPEQBands {
		return gains, false
	}
	for i, field := range fields {
		v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return [musicPEQBands]float64{}, false
		}
		gains[i] = musicPEQClampGain(v)
	}
	return gains, true
}

// musicPEQMatchPreset names a curve, so the panel can show "Bass" after a
// restart instead of pretending the user built it by hand.
func musicPEQMatchPreset(gains [musicPEQBands]float64) string {
	for _, name := range musicPEQPresetNames {
		if musicPEQGainsEqual(gains, musicPEQPresetGains(name)) {
			return name
		}
	}
	return "Custom"
}

func musicPEQGainsEqual(a, b [musicPEQBands]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 0.05 {
			return false
		}
	}
	return true
}

// customStringValue is customString without the case-insensitive fallbacks a
// player setting does not need.
func customStringValue(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// customBool reads a bool that may have been stored as JSON true/false or as
// the string "true"/"false".
func customBool(m map[string]interface{}, key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(customStringValue(m, key)))
	switch raw {
	case "":
		return fallback
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return fallback
}

// ──────────────────────────────────────────────────────────────────────
// Actions
// ──────────────────────────────────────────────────────────────────────

// musicPEQRestart re-opens the decoder so a new curve is heard immediately
// instead of at the next track.
func musicPEQRestart() {
	music.mu.Lock()
	cur := music.current
	playing := music.playing
	pos := music.position
	var path string
	if cur >= 0 && cur < len(music.tracks) {
		path = music.tracks[cur].Path
	}
	music.mu.Unlock()
	if path == "" || !playing {
		return
	}
	musicStartDecode(path, pos)
}

// musicSetPEQEnabled turns the equaliser on or off. Enabling it switches the
// bit-perfect path off, because the two are mutually exclusive.
func musicSetPEQEnabled(on bool, config *Config) {
	if on {
		musicEQ.bitPerfect = false
	}
	musicEQ.enabled = on
	musicPEQSave(config)
	musicPEQRestart()
}

// musicSetBitPerfect switches the unprocessed path on or off. It forces the
// listening speed back to 1: a tempo filter would defeat the whole point.
func musicSetBitPerfect(on bool, config *Config) {
	musicEQ.bitPerfect = on
	if on {
		musicEQ.enabled = false
		music.mu.Lock()
		music.speed = 1
		music.mu.Unlock()
	}
	musicPEQSave(config)
	musicPEQRestart()
}

// musicPEQAdjust changes one band and remembers the curve as Custom.
func musicPEQAdjust(band int, delta float64, config *Config) {
	if band < 0 || band >= musicPEQBands {
		return
	}
	musicEQ.gains[band] = musicPEQClampGain(musicEQ.gains[band] + delta)
	musicEQ.preset = musicPEQMatchPreset(musicEQ.gains)
	if musicEQ.enabled {
		musicPEQSave(config)
	}
}

// musicPEQApplyPreset loads a preset curve.
func musicPEQApplyPreset(name string) {
	musicEQ.preset = name
	musicEQ.gains = musicPEQPresetGains(name)
}

// musicPEQStatus is the one-line summary used by the panel and the player's
// status line.
func musicPEQStatus() string {
	if musicEQ.bitPerfect {
		return "Bit-perfect"
	}
	if !musicEQ.enabled {
		return "EQ off"
	}
	return "PEQ " + musicEQ.preset
}

// ──────────────────────────────────────────────────────────────────────
// Panel
// ──────────────────────────────────────────────────────────────────────

// musicPEQPanelRect is where the panel sits, kept here so the click handler and
// the renderer agree.
func musicPEQPanelRect() sdl.Rect {
	w := screenWidth - 96
	if w > 1100 {
		w = 1100
	}
	h := int32(336)
	return sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
}

// musicPEQBandRect returns the slider hit box for one band, so dragging or
// tapping a band selects it.
func musicPEQBandRect(panel sdl.Rect, band int) sdl.Rect {
	inner := panel.W - 64
	step := inner / musicPEQBands
	x := panel.X + 32 + int32(band)*step
	return sdl.Rect{X: x, Y: panel.Y + 96, W: step - 4, H: 168}
}

func musicPEQTogglePanel(config *Config) {
	musicEQ.panel = !musicEQ.panel
	if musicEQ.panel {
		musicCloseSearch()
	}
}

// handleMusicPEQKey drives the panel. It reports whether the key was consumed,
// so the player's own bindings stay quiet while the panel is up.
func handleMusicPEQKey(e *sdl.KeyboardEvent) bool {
	if !musicEQ.panel || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	config := appConfig
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b:
		musicEQ.panel = false
	case sdl.K_LEFT, sdl.K_a:
		musicEQ.sel = musicPEQMoveSelection(musicEQ.sel, -1)
	case sdl.K_RIGHT, sdl.K_d:
		musicEQ.sel = musicPEQMoveSelection(musicEQ.sel, 1)
	case sdl.K_UP, sdl.K_w:
		// Shaping a band is an explicit "I want the equaliser": switch it on
		// rather than changing a curve nobody can hear.
		musicEQ.enabled = true
		musicEQ.bitPerfect = false
		musicPEQAdjust(musicEQ.sel, 1, config)
		musicPEQSave(config)
		musicPEQRestart()
	case sdl.K_DOWN, sdl.K_s:
		musicEQ.enabled = true
		musicEQ.bitPerfect = false
		musicPEQAdjust(musicEQ.sel, -1, config)
		musicPEQSave(config)
		musicPEQRestart()
	case sdl.K_y:
		name, gains := musicPEQCyclePreset(musicEQ.preset)
		musicEQ.preset = name
		musicEQ.gains = gains
		if !musicEQ.enabled && !musicEQ.bitPerfect {
			musicEQ.enabled = true
		}
		musicPEQSave(config)
		musicPEQRestart()
		showToast("EQ: "+name, ToastInfo())
	case sdl.K_x:
		musicEQ.gains[musicEQ.sel] = 0
		musicEQ.preset = musicPEQMatchPreset(musicEQ.gains)
		musicPEQSave(config)
		musicPEQRestart()
	default:
		return false
	}
	PlayNavSound()
	return true
}

// renderMusicPEQPanel draws the equaliser over whatever is behind it. The bars
// are the gains, the line across the middle is 0 dB.
func renderMusicPEQPanel(renderer *sdl.Renderer, config *Config) {
	if !musicEQ.panel || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	panel := musicPEQPanelRect()

	// Dim the player so the panel reads as modal without hiding context.
	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	drawCard(renderer, panel.X, panel.Y, panel.W, panel.H, 18)

	title := "31-band PEQ   ·   " + musicPEQStatus()
	renderText(renderer, config, font, title, ColorTextPrimary(), panel.X+24, panel.Y+18)
	hint := "LEFT/RIGHT band   UP/DOWN gain   Y preset   X flatten band   G close"
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(),
		panel.X+(panel.W-int32(hw))/2, panel.Y+46)

	// Selected band readout, so the number is exact even when the bar is short.
	if musicEQ.sel >= 0 && musicEQ.sel < musicPEQBands {
		readout := fmt.Sprintf("%s Hz   %+.1f dB", musicBandLabel(musicPEQFrequencies[musicEQ.sel]),
			musicEQ.gains[musicEQ.sel])
		rw, _, _ := font.SizeUTF8(readout)
		renderText(renderer, config, font, readout, ColorTextSecondary(),
			panel.X+panel.W-int32(rw)-24, panel.Y+18)
	}

	midY := panel.Y + 96 + 84
	renderer.SetDrawColor(255, 255, 255, 26)
	renderer.FillRect(&sdl.Rect{X: panel.X + 24, Y: midY, W: panel.W - 48, H: 1})

	for band := 0; band < musicPEQBands; band++ {
		rect := musicPEQBandRect(panel, band)
		half := rect.H / 2
		gain := musicEQ.gains[band]
		h := int32(math.Abs(gain) / musicPEQMaxGain * float64(half))
		y := midY
		if gain >= 0 {
			y = midY - h
		}
		col := WithAlpha(accentColor, 200)
		if band == musicEQ.sel {
			col = accentColor
		}
		if !musicEQ.enabled || musicEQ.bitPerfect {
			col = WithAlpha(ColorTextTertiary(), 120)
		}
		if h > 0 {
			fillRoundedRect(renderer, rect.X+4, y, rect.W-8, h, 3, col)
		}
		// A 1 dB tick per band, so an untouched band still shows its place.
		if gain == 0 {
			renderer.SetDrawColor(col.R, col.G, col.B, 120)
			renderer.FillRect(&sdl.Rect{X: rect.X + 4, Y: midY - 1, W: rect.W - 8, H: 2})
		}
		if band == musicEQ.sel {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 6, 1, accentColor)
		}
		// Label every fifth band, plus the selection, so 31 labels do not
		// collide on a small screen.
		if band%5 == 0 || band == musicEQ.sel {
			label := musicBandLabel(musicPEQFrequencies[band])
			lw, _, _ := font.SizeUTF8(label)
			renderText(renderer, config, font, label, ColorTextTertiary(),
				rect.X+(rect.W-int32(lw))/2, panel.Y+96+rect.H+8)
		}
	}

	footer := "EQ off — press G to open, Y to pick a preset, or Up/Down to start shaping"
	if musicEQ.enabled {
		footer = "Saved to Settings · playback restarts when the curve changes"
	}
	if musicEQ.bitPerfect {
		footer = "Bit-perfect on: EQ and tempo are bypassed until it is turned off"
	}
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panel.X+(panel.W-int32(fw))/2, panel.Y+panel.H-34)
}

// HandleMusicPEQClick selects a band from a tap on its slider.
func HandleMusicPEQClick(mx, my int32) bool {
	if !musicEQ.panel {
		return false
	}
	panel := musicPEQPanelRect()
	for band := 0; band < musicPEQBands; band++ {
		if pointInRect(mx, my, musicPEQBandRect(panel, band)) {
			musicEQ.sel = band
			return true
		}
	}
	// The panel is modal: a tap outside it closes it instead of reaching the
	// transport underneath.
	if !pointInRect(mx, my, panel) {
		musicEQ.panel = false
	}
	return true
}
