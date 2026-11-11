package main

// a11y.go — Accessibility.
//
// Three things live here, all of them system-wide rather than per screen:
//
//   * Text scaling. getCachedFont is the single choke point every label in the
//     app goes through, so one multiplier there resizes scenes, overlays, the
//     on-screen keyboard and toast text together.
//   * High-contrast and colour-blind-safe palettes. They are ordinary theme
//     presets, which means the theme gallery, the config round-trip and the
//     renderer already understand them.
//   * Controller remapping. The physical button is rewritten once, on the way
//     into the event loop, so overlays and scenes cannot disagree about which
//     button means "select".
//
// Everything is stored in the config's Custom map with the same keys the panel
// writes, so a config edited by hand on the SD card behaves exactly like one
// changed from the UI.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	a11yKeyFontScale  = "a11y_font_scale"
	a11yKeyContrast   = "a11y_high_contrast"
	a11yKeyColorblind = "a11y_colorblind"
	a11yKeySwapAB     = "a11y_swap_ab"
	a11yKeySwapXY     = "a11y_swap_xy"
	a11yKeyPrevTheme  = "a11y_prev_theme"
	a11yKeyOneHanded  = "a11y_one_handed"
	a11yKeyChordBtn   = "a11y_chord_button"
	a11yKeySlowRepeat = "a11y_slow_repeat"
	a11yKeyKeymap     = "a11y_keymap"
	// Vision toggles sit beside the High Contrast palette but are not the same
	// thing: they change the shared glass and text tokens rather than swapping
	// the whole palette, so they keep working on any theme.
	a11yKeyReduceTransparency = "a11y_reduce_transparency"
	a11yKeyIncreaseContrast   = "a11y_increase_contrast"
	a11yKeyBoldText           = "a11y_bold_text"
	a11yKeyMonoAudio          = "a11y_mono_audio"
	a11yKeyAudioBalance       = "a11y_audio_balance"
	a11yKeyCaptionsAlways     = "a11y_captions_always"
	a11yKeyCaptionSize        = "a11y_caption_size"
	a11yKeyCaptionBackground  = "a11y_caption_background"
	a11yKeyLongPress          = "a11y_long_press_ms"
	a11yKeyRepeatDelay        = "a11y_repeat_delay_ms"
	a11yKeyRepeatRate         = "a11y_repeat_rate_ms"
	a11yKeyToastDuration      = "a11y_toast_duration_pct"
	a11yKeyStickyChords       = "a11y_sticky_chords"
	a11yKeyMagnifier          = "a11y_magnifier"
	a11yKeyMagnifierZoom      = "a11y_magnifier_zoom"
	a11yKeyLegibleFont        = "a11y_legible_font"
	a11yKeySwitchMode         = "a11y_switch_mode"
	a11yKeySwitchInterval     = "a11y_switch_interval_ms"

	a11yPanelTitle = "Accessibility"

	a11yMinScale = 0.85
	a11yMaxScale = 1.45
)

// a11yFontScaleSteps are the sizes the left/right buttons walk through. Steps
// are discrete on purpose: a handheld's d-pad is a coarse input, and a
// continuous slider makes every press re-rasterise every cached label.
var a11yFontScaleSteps = []float64{0.85, 1.0, 1.15, 1.3, 1.45}

// a11yColorblindModes maps the stored key to the palette it applies. The empty
// key means "don't tint anything", which restores the user's own theme.
var a11yColorblindModes = []struct {
	key    string
	label  string
	preset string
}{
	{key: "", label: "Off", preset: ""},
	{key: "deuteranopia", label: "Deuteranopia", preset: "cb-deuteranopia"},
	{key: "protanopia", label: "Protanopia", preset: "cb-protanopia"},
	{key: "tritanopia", label: "Tritanopia", preset: "cb-tritanopia"},
}

type a11ySettings struct {
	open         bool
	cursor       int
	scroll       int
	fontScale    float64
	highContrast bool
	// reduceTransparency makes glassA return solid tokens, so wallpaper and
	// scene content cannot show through tiles, rows, sheets or chips.
	reduceTransparency bool
	// increaseContrast strengthens glass alphas, widens every focus ring by
	// 2 px and lifts text to at least 7:1 on the surfaces it is drawn on.
	increaseContrast bool
	// boldText swaps the single UI face for Inter SemiBold. The size floor is
	// still applied by getCachedFont/loadRoleFont, so bold never buys legibility
	// by shrinking a caption.
	boldText bool
	// Audio access: mono playback and a left/right balance for stereo
	// decoders, plus captions that start on and their reading style.
	monoAudio         bool
	audioBalance      int
	captionsAlways    bool
	captionSize       int
	captionBackground bool
	// Timing: long-press, held-direction cadence and toast lifetime, plus a
	// sticky one-handed chord that stays armed across several directions.
	longPressMs   int
	repeatDelayMs int
	repeatRateMs  int
	toastPercent  int
	stickyChords  bool
	// legibleFont draws the UI in the best legible system face the machine has
	// (legiblefont.go), or the bundled Inter when none is installed.
	legibleFont bool
	// magnifier is the zoom lens over the focus (magnifier.go); zoom is the
	// factor it captures at, 2 or 3.
	magnifier     bool
	magnifierZoom int
	// switchMode is the one-/two-button scanning mode (switchaccess.go): off,
	// one or two; switchIntervalMs is the dwell of each highlight.
	switchMode       string
	switchIntervalMs int
	colorblind       string
	swapAB           bool
	swapXY           bool
	// One-handed mode turns the chord button plus the d-pad into the four
	// face buttons, so a player who can only reach one side of the device can
	// still navigate and confirm.
	oneHanded bool
	// chordButton is the stored key of the button that acts as the chord
	// modifier ("l1" by default); see a11yChordButtons.
	chordButton string
	// slowRepeat doubles the on-screen keyboard's held-direction delay and
	// interval, for hands that overshoot a key.
	slowRepeat bool
	// keys is the physical → logical keymap. The swap flags above are the two
	// common presets; this map is the general layer, so a panel row can remap
	// Select/Start (or swallow a button) without touching a single handler.
	keys map[uint8]uint8
}

// a11yChordButtons are the physical buttons that can act as the chord
// modifier. They are all reachable with the index finger while a thumb stays
// on the d-pad, which is the whole point of the mode. SDL2 exposes L2/R2 as
// axes, not buttons, so they are not candidates.
var a11yChordButtons = []struct {
	key    string
	label  string
	button uint8
}{
	{key: "l1", label: "L1", button: sdl.CONTROLLER_BUTTON_LEFTSHOULDER},
	{key: "r1", label: "R1", button: sdl.CONTROLLER_BUTTON_RIGHTSHOULDER},
	{key: "back", label: "Back", button: sdl.CONTROLLER_BUTTON_BACK},
	{key: "l3", label: "L3", button: sdl.CONTROLLER_BUTTON_LEFTSTICK},
}

// a11yChordRuntime is the transient state of the one-handed chord: whether the
// modifier is physically held, whether a tap has armed the next direction, and
// which physical button is currently standing in for a face button so its
// release is rewritten the same way.
type a11yChordRuntime struct {
	latched bool
	held    bool
	pressed map[uint8]uint8
}

var a11y = a11ySettings{captionSize: 1, captionBackground: true,
	longPressMs: 450, repeatDelayMs: 320, repeatRateMs: 70, toastPercent: 100,
	magnifierZoom: 2, switchMode: a11ySwitchOff, switchIntervalMs: a11ySwitchDefaultIntervalMS}
var a11yChordState a11yChordRuntime

var a11yPanelRect sdl.Rect
var a11yRowHits []struct {
	index int
	rect  sdl.Rect
}

// ── Persistence ───────────────────────────────────────────────────────────

// a11yInit loads the saved settings and applies them before the first frame, so
// a user who needs large text never sees a launch at the default size.
func a11yInit(config *Config) {
	a11y = a11ySettings{fontScale: 1.0, captionSize: 1, captionBackground: true,
		longPressMs: 450, repeatDelayMs: 320, repeatRateMs: 70, toastPercent: 100,
		magnifierZoom: 2, switchMode: a11ySwitchOff, switchIntervalMs: a11ySwitchDefaultIntervalMS,
		keys: map[uint8]uint8{}}
	a11yLoad(config)
	a11yRefreshFonts()
}

// a11yLoad reads the stored values. Numbers may arrive as float64 (JSON),
// strings (XML, or a hand-edited config) or be missing entirely; all three are
// treated as "no preference" rather than an error.
func a11yLoad(config *Config) {
	if config == nil {
		return
	}
	if s, ok := customString(config.Variables.Custom, a11yKeyFontScale); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 {
			a11y.fontScale = a11yClampScale(f)
		}
	}
	a11y.highContrast = a11yCustomBool(config, a11yKeyContrast)
	a11y.reduceTransparency = a11yCustomBool(config, a11yKeyReduceTransparency)
	a11y.increaseContrast = a11yCustomBool(config, a11yKeyIncreaseContrast)
	a11y.boldText = a11yCustomBool(config, a11yKeyBoldText)
	a11y.legibleFont = a11yCustomBool(config, a11yKeyLegibleFont)
	a11y.monoAudio = a11yCustomBool(config, a11yKeyMonoAudio)
	a11y.audioBalance = a11yInt(config, a11yKeyAudioBalance, 0, -100, 100)
	a11y.captionsAlways = a11yCustomBool(config, a11yKeyCaptionsAlways)
	a11y.captionSize = a11yInt(config, a11yKeyCaptionSize, 1, 0, 2)
	a11y.longPressMs = a11yInt(config, a11yKeyLongPress, 450, 250, 1000)
	a11y.repeatDelayMs = a11yInt(config, a11yKeyRepeatDelay, 320, 100, 800)
	a11y.repeatRateMs = a11yInt(config, a11yKeyRepeatRate, 70, 30, 200)
	a11y.toastPercent = a11yInt(config, a11yKeyToastDuration, 100, 50, 200)
	a11y.stickyChords = a11yCustomBool(config, a11yKeyStickyChords)
	a11y.magnifier = a11yCustomBool(config, a11yKeyMagnifier)
	a11y.magnifierZoom = a11yClampMagnifierZoom(a11yInt(config, a11yKeyMagnifierZoom, 2, 2, 3))
	a11y.switchMode = a11ySwitchOff
	if s, ok := customString(config.Variables.Custom, a11yKeySwitchMode); ok {
		a11y.switchMode = a11ySwitchModeValue(s)
	}
	a11y.switchIntervalMs = a11yInt(config, a11yKeySwitchInterval, a11ySwitchDefaultIntervalMS, 400, 5000)
	a11y.captionBackground = true
	if v, ok := customString(config.Variables.Custom, a11yKeyCaptionBackground); ok {
		if b, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(v))); err == nil {
			a11y.captionBackground = b
		}
	}
	a11y.swapAB = a11yCustomBool(config, a11yKeySwapAB)
	a11y.swapXY = a11yCustomBool(config, a11yKeySwapXY)
	a11y.oneHanded = a11yCustomBool(config, a11yKeyOneHanded)
	a11y.slowRepeat = a11yCustomBool(config, a11yKeySlowRepeat)
	a11y.chordButton = "l1"
	if s, ok := customString(config.Variables.Custom, a11yKeyChordBtn); ok {
		a11y.chordButton = a11yChordKey(s)
	}
	if s, ok := customString(config.Variables.Custom, a11yKeyColorblind); ok {
		if a11yColorblindPreset(s) != "" {
			a11y.colorblind = s
		}
	}
	if s, ok := customString(config.Variables.Custom, a11yKeyKeymap); ok {
		a11y.keys = a11yParseKeymap(s)
	}
	a11yApplyVision()
}

// a11yOnOff is the panel's spelling of a boolean value.
func a11yOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

func a11yCustomBool(config *Config, key string) bool {
	if config == nil {
		return false
	}
	s, ok := customString(config.Variables.Custom, key)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(strings.ToLower(s)))
	return err == nil && b
}

// a11yStore writes the settings into the config map only. It is the part a
// test can exercise without touching the card, and the part every caller
// actually depends on: a11ySave adds the disk write on top.
func a11yStore(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[a11yKeyFontScale] = a11y.fontScale
	config.Variables.Custom[a11yKeyContrast] = a11y.highContrast
	config.Variables.Custom[a11yKeyReduceTransparency] = a11y.reduceTransparency
	config.Variables.Custom[a11yKeyIncreaseContrast] = a11y.increaseContrast
	config.Variables.Custom[a11yKeyBoldText] = a11y.boldText
	config.Variables.Custom[a11yKeyLegibleFont] = a11y.legibleFont
	config.Variables.Custom[a11yKeyMonoAudio] = a11y.monoAudio
	config.Variables.Custom[a11yKeyAudioBalance] = a11y.audioBalance
	config.Variables.Custom[a11yKeyCaptionsAlways] = a11y.captionsAlways
	config.Variables.Custom[a11yKeyCaptionSize] = a11y.captionSize
	config.Variables.Custom[a11yKeyCaptionBackground] = a11y.captionBackground
	config.Variables.Custom[a11yKeyLongPress] = a11y.longPressMs
	config.Variables.Custom[a11yKeyRepeatDelay] = a11y.repeatDelayMs
	config.Variables.Custom[a11yKeyRepeatRate] = a11y.repeatRateMs
	config.Variables.Custom[a11yKeyToastDuration] = a11y.toastPercent
	config.Variables.Custom[a11yKeyStickyChords] = a11y.stickyChords
	config.Variables.Custom[a11yKeyMagnifier] = a11y.magnifier
	config.Variables.Custom[a11yKeyMagnifierZoom] = a11y.magnifierZoom
	config.Variables.Custom[a11yKeySwitchMode] = a11y.switchMode
	config.Variables.Custom[a11yKeySwitchInterval] = a11y.switchIntervalMs
	config.Variables.Custom[a11yKeyColorblind] = a11y.colorblind
	config.Variables.Custom[a11yKeySwapAB] = a11y.swapAB
	config.Variables.Custom[a11yKeySwapXY] = a11y.swapXY
	config.Variables.Custom[a11yKeyOneHanded] = a11y.oneHanded
	config.Variables.Custom[a11yKeyChordBtn] = a11y.chordButton
	config.Variables.Custom[a11yKeySlowRepeat] = a11y.slowRepeat
	config.Variables.Custom[a11yKeyKeymap] = a11yKeymapString(a11y.keys)
}

// a11ySave writes the settings back through the same user-config writer the
// Settings scenes use, so nothing else in the snapshot is lost.
func a11ySave(config *Config) {
	a11yStore(config)
	if config == nil {
		return
	}
	saveUserConfig(userConfigFromVariables(config))
}

// a11yApplyVision makes the live token state match the saved toggles. The
// focus ring is the only piece of state that cannot be computed on demand —
// every draw call reads the same width — so it is set here and in a11yAdjust,
// keeping launch, reset and both UI entry points in sync.
func a11yApplyVision() {
	glassFocusRingWidth = 3
	if a11y.increaseContrast {
		glassFocusRingWidth = 5
	}
}

// ── Text scaling ──────────────────────────────────────────────────────────

func a11yClampScale(scale float64) float64 {
	if math.IsNaN(scale) || scale <= 0 {
		return 1.0
	}
	if scale < a11yMinScale {
		scale = a11yMinScale
	}
	if scale > a11yMaxScale {
		scale = a11yMaxScale
	}
	return math.Round(scale*100) / 100
}

// a11yScaleFontSize is called by getCachedFont for every logical font, which is
// what makes the setting global. The size is part of the font cache key, so a
// new scale simply opens new faces instead of reusing stale ones.
func a11yScaleFontSize(base int) int {
	if base <= 0 {
		return base
	}
	scale := a11y.fontScale
	if scale <= 0 || math.Abs(scale-1) < 0.001 {
		return base
	}
	size := int(math.Round(float64(base) * scale))
	if size < 8 {
		size = 8
	}
	return size
}

// a11yRefreshFonts drops every cached font and label texture so the new size is
// visible on the next frame. The faces are released by dropping the map rather
// than closing them: a texture cache elsewhere could still hold a pointer, and
// a closed face is a crash, while an unreferenced one is collected at exit.
func a11yRefreshFonts() {
	fontCache = make(map[string]*ttf.Font)
	// Fitted button labels hold faces at the previous size; they must be looked
	// up again or every button would keep drawing the old text size.
	resetButtonFitCache()
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	clearHomeTextCache()
	resetHomeFonts()
}

func a11yScaleIndex() int {
	idx := 0
	best := math.Abs(a11yFontScaleSteps[0] - a11y.fontScale)
	for i, step := range a11yFontScaleSteps {
		if d := math.Abs(step - a11y.fontScale); d < best {
			best = d
			idx = i
		}
	}
	return idx
}

func a11yInt(config *Config, key string, def, lo, hi int) int {
	if config == nil {
		return def
	}
	v, ok := config.Variables.Custom[key]
	if !ok {
		return def
	}
	n := 0
	switch t := v.(type) {
	case float64:
		n = int(math.Round(t))
	case int:
		n = t
	case string:
		p, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return def
		}
		n = p
	default:
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func a11yAudioBalanceLabel() string {
	if a11y.audioBalance == 0 {
		return "Centre"
	}
	side := "Right"
	if a11y.audioBalance < 0 {
		side = "Left"
	}
	return side + " " + strconv.Itoa(absInt(a11y.audioBalance)) + "%"
}

func a11yCaptionSizeLabel() string {
	switch a11y.captionSize {
	case 0:
		return "Small"
	case 2:
		return "Large"
	default:
		return "Medium"
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func a11yScaleLabel() string {
	return strconv.Itoa(int(math.Round(a11y.fontScale*100))) + "%"
}

// ── Palettes ──────────────────────────────────────────────────────────────

func a11yColorblindPreset(key string) string {
	for _, mode := range a11yColorblindModes {
		if mode.key == key {
			return mode.preset
		}
	}
	return ""
}

func a11yColorblindLabel(key string) string {
	for _, mode := range a11yColorblindModes {
		if mode.key == key {
			return mode.label
		}
	}
	return "Off"
}

func a11yColorblindIndex(key string) int {
	for i, mode := range a11yColorblindModes {
		if mode.key == key {
			return i
		}
	}
	return 0
}

// a11yIsA11yPreset reports whether a preset name is one this panel applies, so
// the "previous theme" slot never records our own palette as the user's choice.
func a11yIsA11yPreset(name string) bool {
	if name == "" || name == "highcontrast" {
		return true
	}
	for _, mode := range a11yColorblindModes {
		if mode.preset != "" && mode.preset == name {
			return true
		}
	}
	return false
}

// a11yRememberTheme records the palette to return to when the accessibility
// palettes are switched off again.
func a11yRememberTheme(config *Config) {
	if config == nil {
		return
	}
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if a11yIsA11yPreset(current) {
		return
	}
	config.Variables.Custom[a11yKeyPrevTheme] = current
}

// a11yPaletteName decides which theme the accessibility settings want right
// now. An empty result means "leave the current theme alone".
//
// A colour-blind palette wins over high contrast because it carries more
// information: the contrast boost is still reachable by choosing it once the
// colour-blind mode is off.
func a11yPaletteName(config *Config) string {
	if config == nil {
		return ""
	}
	if preset := a11yColorblindPreset(a11y.colorblind); preset != "" {
		return preset
	}
	if a11y.highContrast {
		return "highcontrast"
	}
	// Nothing of ours is on. Leave a theme the user picked after us alone, and
	// do nothing at all when we never replaced one: falling back to "dark" here
	// would silently wipe a palette chosen in the theme gallery.
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if !a11yIsA11yPreset(current) {
		return ""
	}
	prev, ok := customString(config.Variables.Custom, a11yKeyPrevTheme)
	if !ok || prev == "" {
		return ""
	}
	return prev
}

// a11yApplyPalette puts the selected palette on screen.
func a11yApplyPalette(config *Config) {
	name := a11yPaletteName(config)
	if name == "" {
		return
	}
	if a11yColorblindPreset(a11y.colorblind) != "" || a11y.highContrast {
		a11yRememberTheme(config)
	}
	applyThemePreset(config, name, false)
}

// ── Controller remapping ──────────────────────────────────────────────────

// a11yDisabledButton marks a physical button the keymap swallows. SDL button
// ids stay well below it, so it cannot collide with a real button.
const a11yDisabledButton = uint8(255)

// a11yButtonNames is the stable spelling used in the saved keymap. SDL ids are
// saved by name rather than number so a hand-edited config stays readable and
// cannot silently point at a different physical button.
var a11yButtonNames = []struct {
	key    string
	label  string
	button uint8
}{
	{"a", "A", sdl.CONTROLLER_BUTTON_A},
	{"b", "B", sdl.CONTROLLER_BUTTON_B},
	{"x", "X", sdl.CONTROLLER_BUTTON_X},
	{"y", "Y", sdl.CONTROLLER_BUTTON_Y},
	{"select", "Select", sdl.CONTROLLER_BUTTON_BACK},
	{"start", "Start", sdl.CONTROLLER_BUTTON_START},
	{"l1", "L1", sdl.CONTROLLER_BUTTON_LEFTSHOULDER},
	{"r1", "R1", sdl.CONTROLLER_BUTTON_RIGHTSHOULDER},
}

func a11yButtonCode(name string) (uint8, bool) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	for _, b := range a11yButtonNames {
		if b.key == wanted {
			return b.button, true
		}
	}
	return 0, false
}

func a11yButtonKey(button uint8) string {
	for _, b := range a11yButtonNames {
		if b.button == button {
			return b.key
		}
	}
	return ""
}

// a11yParseKeymap reads "select:start,start:select" (or "select:none" for a
// swallowed button). Unknown names are skipped so a typo costs that entry, not
// the whole map.
func a11yParseKeymap(raw string) map[uint8]uint8 {
	out := map[uint8]uint8{}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		physical, ok := a11yButtonCode(kv[0])
		if !ok {
			continue
		}
		target := strings.ToLower(strings.TrimSpace(kv[1]))
		if target == "none" {
			out[physical] = a11yDisabledButton
			continue
		}
		logical, ok := a11yButtonCode(target)
		if !ok || logical == physical {
			continue
		}
		out[physical] = logical
	}
	return out
}

func a11yKeymapString(keys map[uint8]uint8) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, b := range a11yButtonNames {
		target, ok := keys[b.button]
		if !ok {
			continue
		}
		name := "none"
		if target != a11yDisabledButton {
			name = a11yButtonKey(target)
			if name == "" {
				continue
			}
		}
		parts = append(parts, b.key+":"+name)
	}
	return strings.Join(parts, ",")
}

// a11yHeldNavDirection is the D-pad direction held right now as the screens
// see it: through the keymap and swaps, and through a one-handed chord press
// that is still held. Hold-to-repeat used to poll the physical D-pad alone, so
// holding Up while it stood in for A (one-handed mode) fired A once and then
// streamed Up moves, and a direction remapped to another button still
// repeated as a direction. held reports whether one physical button is down.
func a11yHeldNavDirection(held func(uint8) bool) (int, int) {
	dx, dy := 0, 0
	var seen [64]bool // runs every frame: no map allocation
	check := func(phys uint8) {
		if int(phys) >= len(seen) || seen[phys] || !held(phys) {
			return
		}
		seen[phys] = true
		logical := phys
		if target, chorded := a11yChordState.pressed[phys]; chorded && a11y.oneHanded {
			logical = target
		}
		logical, dropped := a11yResolveButton(logical)
		if dropped {
			return
		}
		switch logical {
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			dx = -1
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			dx = 1
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			dy = -1
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			dy = 1
		}
	}
	for _, phys := range []uint8{sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT} {
		check(phys)
	}
	// A button the keymap turned into a direction repeats like one.
	for phys := range a11y.keys {
		check(phys)
	}
	return dx, dy
}

// a11yResolveButton applies the general keymap, then the two swap presets, and
// reports whether the press must be swallowed entirely (a disabled button).
//
// This is the single place "physical button" becomes "logical button": every
// handler, overlay and keyboard lookup sees the result, so a new remappable
// button is a row here, not a change in scene code.
func a11yResolveButton(b uint8) (uint8, bool) {
	if target, ok := a11y.keys[b]; ok {
		if target == a11yDisabledButton {
			return b, true
		}
		b = target
	}
	if a11y.swapAB || a11y.swapXY {
		return a11ySwapButton(b), false
	}
	return b, false
}

// a11ySwapButton is the preset half of the keymap: the two classic swaps.
func a11ySwapButton(b uint8) uint8 {
	if a11y.swapAB {
		switch b {
		case sdl.CONTROLLER_BUTTON_A:
			return sdl.CONTROLLER_BUTTON_B
		case sdl.CONTROLLER_BUTTON_B:
			return sdl.CONTROLLER_BUTTON_A
		}
	}
	if a11y.swapXY {
		switch b {
		case sdl.CONTROLLER_BUTTON_X:
			return sdl.CONTROLLER_BUTTON_Y
		case sdl.CONTROLLER_BUTTON_Y:
			return sdl.CONTROLLER_BUTTON_X
		}
	}
	return b
}

// a11yRemapButton translates a physical button into the logical one the rest of
// the app expects, ignoring the disabled state (callers that must honour it use
// a11yResolveButton).
func a11yRemapButton(b uint8) uint8 {
	logical, _ := a11yResolveButton(b)
	return logical
}

// ── One-handed chord ──────────────────────────────────────────────────────

// a11yChordKey normalises a stored chord button name. An unknown value falls
// back to L1 rather than leaving the mode with no modifier at all.
func a11yChordKey(key string) string {
	wanted := strings.ToLower(strings.TrimSpace(key))
	for _, option := range a11yChordButtons {
		if option.key == wanted {
			return option.key
		}
	}
	return "l1"
}

func a11yChordLabel(key string) string {
	wanted := a11yChordKey(key)
	for _, option := range a11yChordButtons {
		if option.key == wanted {
			return option.label
		}
	}
	return "L1"
}

// a11yChordCode is the SDL button the chord modifier currently refers to.
func a11yChordCode() uint8 {
	wanted := a11yChordKey(a11y.chordButton)
	for _, option := range a11yChordButtons {
		if option.key == wanted {
			return option.button
		}
	}
	return sdl.CONTROLLER_BUTTON_LEFTSHOULDER
}

// a11yChordTarget maps a direction to the face button it stands in for while
// the chord is armed. Up is the neutral "confirm" so a single thumb can walk a
// list with Down and press Up to open the entry.
func a11yChordTarget(dir uint8) (uint8, bool) {
	switch dir {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return sdl.CONTROLLER_BUTTON_A, true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return sdl.CONTROLLER_BUTTON_B, true
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return sdl.CONTROLLER_BUTTON_X, true
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return sdl.CONTROLLER_BUTTON_Y, true
	}
	return 0, false
}

func a11yChordReset() {
	a11yChordState = a11yChordRuntime{}
}

// a11yRemapEvent rewrites the event in place before any handler sees it and
// reports whether the event must be dropped. Two things happen here, in order:
//
//   - the one-handed chord. Tapping the chord button arms the next d-pad press
//     (tap again to cancel), holding it does the same for as long as it is
//     down, and the direction is rewritten to A/B/X/Y before the swap rules.
//   - the A/B and X/Y swaps, as they always were.
//
// The physical button is rewritten once, on the way into the event loop, so
// overlays, the browser's own pad handler and the on-screen keyboard all agree
// on what "select" means.
func a11yRemapEvent(e *sdl.ControllerButtonEvent) bool {
	if e == nil {
		return false
	}
	mod := a11yChordCode()
	if e.Type == sdl.CONTROLLERBUTTONUP {
		if a11y.oneHanded && e.Button == mod {
			a11yChordState.held = false
		}
		// A press that was rewritten on the way down has to be released as the
		// button it became, or the two events disagree about what was pressed.
		if logical, ok := a11yChordState.pressed[e.Button]; ok && a11y.oneHanded {
			delete(a11yChordState.pressed, e.Button)
			e.Button = logical
		}
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}

	if a11y.oneHanded {
		if e.Button == mod {
			if a11yChordState.held {
				// A duplicate press (some pads repeat on hold): keep the latch.
				return true
			}
			a11yChordState.held = true
			a11yChordState.latched = !a11yChordState.latched
			return true
		}
		if a11yChordState.latched {
			if logical, ok := a11yChordTarget(e.Button); ok {
				a11yChordState.latched = a11y.stickyChords
				if a11yChordState.pressed == nil {
					a11yChordState.pressed = map[uint8]uint8{}
				}
				a11yChordState.pressed[e.Button] = logical
				e.Button = logical
			}
		}
	}

	if logical, dropped := a11yResolveButton(e.Button); dropped {
		return true
	} else if logical != e.Button {
		e.Button = logical
	}
	return false
}

// a11yRepeatMs scales a held-direction repeat delay. The slow setting is twice
// as deliberate, which is the difference between "I held a direction" and "the
// cursor ran away" for a player with a tremor.
func a11yRepeatMs(base int64) int64 {
	if a11y.slowRepeat {
		return base * 2
	}
	return base
}

func a11yLongPressMs() uint64  { return uint64(clampInt(a11y.longPressMs, 250, 1000)) }
func a11yRepeatDelayMs() int64 { return a11yRepeatMs(int64(clampInt(a11y.repeatDelayMs, 100, 800))) }
func a11yRepeatRateMs() int64  { return a11yRepeatMs(int64(clampInt(a11y.repeatRateMs, 30, 200))) }

func a11yToastMs(base uint64) uint64 {
	pct := clampInt(a11y.toastPercent, 50, 200)
	return base * uint64(pct) / 100
}

// ── Setters ───────────────────────────────────────────────────────────────
//
// One function per value. The Accessibility panel's rows (a11yAdjust), the
// Settings page (a11y_settings.go) and the stored config all land here, so a
// value is clamped, applied to live state and announced in one place. The
// caller saves: the panel saves once for the row it moved, a Settings row saves
// for its own change.

func a11ySetScale(scale float64) bool {
	scale = a11yClampScale(scale)
	if scale == a11y.fontScale {
		return false
	}
	a11y.fontScale = scale
	a11yRefreshFonts()
	ShowToast("Text size: "+a11yScaleLabel(), ToastKindInfo)
	return true
}

// a11yColorblindStep is the next palette in the panel's order, wrapping.
func a11yColorblindStep(dir int) string {
	n := len(a11yColorblindModes)
	idx := ((a11yColorblindIndex(a11y.colorblind)+dir)%n + n) % n
	return a11yColorblindModes[idx].key
}

func a11ySetColorblind(config *Config, key string) bool {
	if key != "" && a11yColorblindPreset(key) == "" {
		key = ""
	}
	if key == a11y.colorblind {
		return false
	}
	a11y.colorblind = key
	a11yApplyPalette(config)
	ShowToast("Colour-blind palette: "+a11yColorblindLabel(key), ToastKindInfo)
	return true
}

func a11ySetMonoAudio(on bool) bool {
	if on == a11y.monoAudio {
		return false
	}
	a11y.monoAudio = on
	ShowToast("Mono audio: "+a11yOnOff(on), ToastKindInfo)
	return true
}

func a11ySetAudioBalance(v int) bool {
	v = clampInt(v, -100, 100)
	if v == a11y.audioBalance {
		return false
	}
	a11y.audioBalance = v
	ShowToast("Audio balance: "+a11yAudioBalanceLabel(), ToastKindInfo)
	return true
}

func a11ySetCaptionsAlways(on bool) bool {
	if on == a11y.captionsAlways {
		return false
	}
	a11y.captionsAlways = on
	ShowToast("Captions always on: "+a11yOnOff(on), ToastKindInfo)
	return true
}

func a11ySetCaptionSize(n int) bool {
	n = clampInt(n, 0, 2)
	if n == a11y.captionSize {
		return false
	}
	a11y.captionSize = n
	ShowToast("Caption size: "+a11yCaptionSizeLabel(), ToastKindInfo)
	return true
}

func a11ySetCaptionBackground(on bool) bool {
	if on == a11y.captionBackground {
		return false
	}
	a11y.captionBackground = on
	ShowToast("Caption background: "+a11yOnOff(on), ToastKindInfo)
	return true
}

// a11ySetLongPress, a11ySetRepeatDelay, a11ySetRepeatRate and
// a11ySetToastPercent clamp to the range the handlers read, so a Settings stop
// and a panel step can never leave a value outside it.
func a11ySetLongPress(ms int) bool {
	ms = clampInt(ms, 250, 1000)
	if ms == a11y.longPressMs {
		return false
	}
	a11y.longPressMs = ms
	ShowToast("Long press: "+strconv.Itoa(ms)+" ms", ToastKindInfo)
	return true
}

func a11ySetRepeatDelay(ms int) bool {
	ms = clampInt(ms, 100, 800)
	if ms == a11y.repeatDelayMs {
		return false
	}
	a11y.repeatDelayMs = ms
	ShowToast("Repeat delay: "+strconv.Itoa(ms)+" ms", ToastKindInfo)
	return true
}

func a11ySetRepeatRate(ms int) bool {
	ms = clampInt(ms, 30, 200)
	if ms == a11y.repeatRateMs {
		return false
	}
	a11y.repeatRateMs = ms
	ShowToast("Repeat speed: "+strconv.Itoa(ms)+" ms", ToastKindInfo)
	return true
}

func a11ySetToastPercent(pct int) bool {
	pct = clampInt(pct, 50, 200)
	if pct == a11y.toastPercent {
		return false
	}
	a11y.toastPercent = pct
	ShowToast("Toast duration: "+strconv.Itoa(pct)+"%", ToastKindInfo)
	return true
}

func a11ySetStickyChords(on bool) bool {
	if on == a11y.stickyChords {
		return false
	}
	a11y.stickyChords = on
	ShowToast("Sticky chords: "+a11yOnOff(on), ToastKindInfo)
	return true
}

func a11ySetOneHanded(on bool) bool {
	if on == a11y.oneHanded {
		return false
	}
	a11y.oneHanded = on
	a11yChordReset()
	if on {
		ShowToast("One-handed: tap "+a11yChordLabel(a11y.chordButton)+", then a direction", ToastKindInfo)
	} else {
		ShowToast("One-handed mode off", ToastKindInfo)
	}
	return true
}

// a11yChordStep is the next chord modifier in a11yChordButtons, wrapping.
func a11yChordStep(dir int) string {
	idx := 0
	for i, option := range a11yChordButtons {
		if option.key == a11yChordKey(a11y.chordButton) {
			idx = i
		}
	}
	if dir < 0 {
		idx += len(a11yChordButtons) - 1
	} else {
		idx++
	}
	return a11yChordButtons[idx%len(a11yChordButtons)].key
}

func a11ySetChordButton(key string) bool {
	key = a11yChordKey(key)
	if key == a11yChordKey(a11y.chordButton) {
		return false
	}
	a11y.chordButton = key
	a11yChordReset()
	ShowToast("Chord button: "+a11yChordLabel(a11y.chordButton), ToastKindInfo)
	return true
}

func a11ySetSlowRepeat(on bool) bool {
	if on == a11y.slowRepeat {
		return false
	}
	a11y.slowRepeat = on
	label := "Normal"
	if on {
		label = "Slow"
	}
	ShowToast("Key repeat: "+label, ToastKindInfo)
	return true
}

// a11ySetMagnifier switches the zoom lens (magnifier.go). Turning it off drops
// the cached copy instead of leaving a hidden texture behind.
func a11ySetMagnifier(on bool) bool {
	if on == a11y.magnifier {
		return false
	}
	a11y.magnifier = on
	if on {
		ShowToast("Magnifier: On · "+strconv.Itoa(a11yClampMagnifierZoom(a11y.magnifierZoom))+"×", ToastKindInfo)
	} else {
		ShowToast("Magnifier off", ToastKindInfo)
		magnifierReset()
	}
	return true
}

func a11ySetMagnifierZoom(zoom int) bool {
	zoom = a11yClampMagnifierZoom(zoom)
	if zoom == a11yClampMagnifierZoom(a11y.magnifierZoom) {
		return false
	}
	a11y.magnifierZoom = zoom
	ShowToast("Magnifier zoom: "+strconv.Itoa(zoom)+"×", ToastKindInfo)
	return true
}

// a11ySetLegibleFont turns the legible reading face on or off. The face is
// resolved from the machine's installed fonts at the next frame, and every
// font-dependent cache is dropped so the change is visible without a restart.
func a11ySetLegibleFont(on bool) bool {
	if on == a11y.legibleFont {
		return false
	}
	a11y.legibleFont = on
	legibleFontReset()
	a11yRefreshFonts()
	if !on {
		ShowToast("Legible reading font: Off", ToastKindInfo)
		return true
	}
	if face := legibleFontFaceName(); face != "" {
		ShowToast("Legible reading font: "+face, ToastKindInfo)
	} else {
		ShowToast("Legible reading font on · no system face found, using Inter", ToastKindInfo)
	}
	return true
}

// a11ySetSwitchMode turns one-/two-button scanning on, off or into its other
// button count. The dwell clock restarts, so the newly highlighted control
// gets a full interval before it moves.
func a11ySetSwitchMode(mode string) bool {
	mode = a11ySwitchModeValue(mode)
	if mode == a11ySwitchModeValue(a11y.switchMode) {
		return false
	}
	a11y.switchMode = mode
	switchScanReset()
	switch mode {
	case a11ySwitchOne:
		ShowToast("Switch access: one button · A selects", ToastKindInfo)
	case a11ySwitchTwo:
		ShowToast("Switch access: two buttons · B next, A selects", ToastKindInfo)
	default:
		ShowToast("Switch access off", ToastKindInfo)
	}
	return true
}

func a11ySetSwitchInterval(ms int) bool {
	ms = clampInt(ms, 400, 5000)
	if ms == clampInt(a11y.switchIntervalMs, 400, 5000) {
		return false
	}
	a11y.switchIntervalMs = ms
	switchScanReset()
	ShowToast("Scan interval: "+a11ySwitchIntervalLabel(ms), ToastKindInfo)
	return true
}

// a11ySetLayoutPreset applies a whole layout, not a delta: it clears the
// per-button entries so the two ways of describing the keys cannot disagree.
func a11ySetLayoutPreset(nintendo bool) bool {
	if nintendo == (a11y.swapAB && a11y.swapXY && len(a11y.keys) == 0) {
		return false
	}
	a11y.swapAB, a11y.swapXY = nintendo, nintendo
	a11y.keys = map[uint8]uint8{}
	if nintendo {
		ShowToast("Button layout: Nintendo order · remaps cleared", ToastKindInfo)
	} else {
		ShowToast("Button layout: standard order", ToastKindInfo)
	}
	return true
}

// a11ySetButtonTarget points one physical button at another action. target ==
// button clears the entry (sends itself); a11yDisabledButton swallows the
// press. The preset that covered the button is dropped first, so an explicit
// remap is not rewritten a second time on the way out of a11yResolveButton.
func a11ySetButtonTarget(button, target uint8) bool {
	if a11y.keys == nil {
		a11y.keys = map[uint8]uint8{}
	}
	if target == button {
		delete(a11y.keys, button)
	} else {
		a11y.keys[button] = target
	}
	a11yClearSwapFor(button)
	ShowToast(a11yButtonLabel(button)+" sends "+a11yButtonTargetLabel(button), ToastKindInfo)
	return true
}

// a11ySelectNextMode is the Select row's three-state cycle.
func a11ySelectNextMode() string {
	switch a11ySelectKeyMode() {
	case a11ySelectDefault:
		return a11ySelectSwapped
	case a11ySelectSwapped:
		return a11ySelectDisabled
	}
	return a11ySelectDefault
}

func a11ySetSelectMode(mode string) bool {
	a11ySetSelectKeyMode(mode)
	switch mode {
	case a11ySelectSwapped:
		ShowToast("Select now sends Start", ToastKindInfo)
	case a11ySelectDisabled:
		ShowToast("Select disabled · Start still opens the menu", ToastKindInfo)
	default:
		ShowToast("Select restored", ToastKindInfo)
	}
	return true
}

// a11yResetDefaults is the panel's Reset row and Settings' Reset action.
func a11yResetDefaults(config *Config) {
	a11y.fontScale = 1.0
	a11y.highContrast = false
	a11y.reduceTransparency = false
	a11y.increaseContrast = false
	a11y.boldText = false
	a11y.legibleFont = false
	legibleFontReset()
	a11y.monoAudio = false
	a11y.audioBalance = 0
	a11y.captionsAlways = false
	a11y.captionSize = 1
	a11y.captionBackground = true
	a11y.longPressMs = 450
	a11y.repeatDelayMs = 320
	a11y.repeatRateMs = 70
	a11y.toastPercent = 100
	a11y.stickyChords = false
	a11y.magnifier = false
	a11y.magnifierZoom = 2
	a11y.switchMode = a11ySwitchOff
	a11y.switchIntervalMs = a11ySwitchDefaultIntervalMS
	switchScanReset()
	a11y.colorblind = ""
	a11y.swapAB = false
	a11y.swapXY = false
	a11y.oneHanded = false
	a11y.chordButton = "l1"
	a11y.slowRepeat = false
	a11y.keys = map[uint8]uint8{}
	a11yChordReset()
	a11yRefreshFonts()
	a11yApplyVision()
	a11yApplyPalette(config)
	magnifierReset()
	// The reset owns the stored values too, so a caller that only resets (a
	// test, a future shortcut) cannot leave the old ones in the config.
	a11yStore(config)
	ShowToast("Accessibility settings reset", ToastKindInfo)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ── Panel ─────────────────────────────────────────────────────────────────

type a11yRowKind int

const (
	a11yRowTextSize a11yRowKind = iota
	a11yRowContrast
	a11yRowReduceTransparency
	a11yRowIncreaseContrast
	a11yRowBoldText
	a11yRowMonoAudio
	a11yRowAudioBalance
	a11yRowCaptionsAlways
	a11yRowCaptionSize
	a11yRowCaptionBackground
	a11yRowLongPress
	a11yRowRepeatDelay
	a11yRowRepeatRate
	a11yRowToastDuration
	a11yRowStickyChords
	a11yRowColorblind
	a11yRowLayoutPreset
	// a11yRowButton edits one physical → logical entry of the keymap. The list
	// of rows is built from a11yPanelButtons, so a new remappable button is one
	// entry there rather than another kind and another case.
	a11yRowButton
	a11yRowSelectKey
	a11yRowOneHanded
	a11yRowChordButton
	a11yRowSlowRepeat
	// a11yRowPowerMode steps the named Power Mode (Battery Saver / Balanced /
	// Performance / Default). It answers for Low Power and Reduced Motion from
	// one value, so a player picks a battery trade-off without learning two
	// toggles; the Quick Settings row drives the same helper.
	a11yRowPowerMode
	// a11yRowVibration steps how hard the motor pulses (Soft / Normal /
	// Strong). It is the strength half of the Settings → General Vibration
	// switch, which stays the on/off half: this row is where "quieter at night"
	// is answered, and it is here because it is a comfort setting like the rest
	// of the panel.
	a11yRowVibration
	a11yRowReset
)

type a11yRow struct {
	label  string
	detail string
	value  string
	kind   a11yRowKind
	// button is the physical button a a11yRowButton row edits; unused by the
	// other kinds.
	button uint8
}

// a11yPanelButtons are the buttons with their own row. Select and Start keep
// the dedicated row below, which moves or disables the pair together: two rows
// free to fight over BACK/START can leave a player with no way into the menu.
var a11yPanelButtons = []uint8{
	sdl.CONTROLLER_BUTTON_A,
	sdl.CONTROLLER_BUTTON_B,
	sdl.CONTROLLER_BUTTON_X,
	sdl.CONTROLLER_BUTTON_Y,
	sdl.CONTROLLER_BUTTON_LEFTSHOULDER,
	sdl.CONTROLLER_BUTTON_RIGHTSHOULDER,
}

func a11yRows() []a11yRow {
	contrast := a11yOnOff(a11y.highContrast)
	reducedTransparency := a11yOnOff(a11y.reduceTransparency)
	increasedContrast := a11yOnOff(a11y.increaseContrast)
	boldText := a11yOnOff(a11y.boldText)
	oneHanded := "Off"
	if a11y.oneHanded {
		oneHanded = "On"
	}
	repeat := "Normal"
	if a11y.slowRepeat {
		repeat = "Slow"
	}
	rows := []a11yRow{
		{label: "Text size", detail: "Scales every menu, overlay and keyboard label.", value: a11yScaleLabel(), kind: a11yRowTextSize},
		{label: "High contrast", detail: "Stronger borders and brighter text on every screen.", value: contrast, kind: a11yRowContrast},
		{label: "Reduce transparency", detail: "Makes every glass tile, row, sheet and chip opaque, so wallpaper and background motion cannot show through them.", value: reducedTransparency, kind: a11yRowReduceTransparency},
		{label: "Increase contrast", detail: "Strengthens glass edges, widens focus outlines and keeps text at 7:1 or better on the surfaces it sits on.", value: increasedContrast, kind: a11yRowIncreaseContrast},
		{label: "Bold text", detail: "Draws every menu, list and Home label in Inter SemiBold. Captions stay at the 14 px legibility floor.", value: boldText, kind: a11yRowBoldText},
		{label: "Mono audio", detail: "Downmixes video and music to one channel for a single speaker or one-ear listening.", value: a11yOnOff(a11y.monoAudio), kind: a11yRowMonoAudio},
		{label: "Audio balance", detail: "Moves stereo video and music between the left and right channels. Interface sounds stay centred on the mono UI device.", value: a11yAudioBalanceLabel(), kind: a11yRowAudioBalance},
		{label: "Captions always on", detail: "Each video starts with its subtitles enabled; CC still turns them off for the current video.", value: a11yOnOff(a11y.captionsAlways), kind: a11yRowCaptionsAlways},
		{label: "Caption size", detail: "Small, Medium or Large cue text above the bottom of the picture.", value: a11yCaptionSizeLabel(), kind: a11yRowCaptionSize},
		{label: "Caption background", detail: "Draws a dark panel behind cues. Turn it off to keep only the text over the picture.", value: a11yOnOff(a11y.captionBackground), kind: a11yRowCaptionBackground},
		{label: "Long press", detail: "How long a letter or the globe must be held before its alternate action opens.", value: strconv.Itoa(a11y.longPressMs) + " ms", kind: a11yRowLongPress},
		{label: "Repeat delay", detail: "How long a direction waits before it starts repeating.", value: strconv.Itoa(a11y.repeatDelayMs) + " ms", kind: a11yRowRepeatDelay},
		{label: "Repeat speed", detail: "The gap between repeated direction steps. Larger is slower.", value: strconv.Itoa(a11y.repeatRateMs) + " ms", kind: a11yRowRepeatRate},
		{label: "Toast duration", detail: "How long a message stays on screen before it fades.", value: strconv.Itoa(a11y.toastPercent) + "%", kind: a11yRowToastDuration},
		{label: "Sticky chords", detail: "Leaves the one-handed chord armed after a direction, so several face buttons can be pressed in sequence.", value: a11yOnOff(a11y.stickyChords), kind: a11yRowStickyChords},
		{label: "Colour-blind palette", detail: "Replaces the theme colours with a safer set.", value: a11yColorblindLabel(a11y.colorblind), kind: a11yRowColorblind},
		{label: "Button layout", detail: "Nintendo order swaps A/B and X/Y together, for players used to the Japanese layout. Applying a preset clears the per-button remaps below.", value: a11yLayoutPresetLabel(), kind: a11yRowLayoutPreset},
	}
	for _, button := range a11yPanelButtons {
		rows = append(rows, a11yRow{
			label:  a11yButtonLabel(button),
			detail: "Which action " + a11yButtonLabel(button) + " sends. Left and right walk the list of actions, and the last step disables the button.",
			value:  a11yButtonTargetLabel(button),
			kind:   a11yRowButton,
			button: button,
		})
	}
	return append(rows,
		a11yRow{label: "Select button", detail: "Moves the Select action to Start (both stay reachable), or disables Select entirely. Start keeps opening the menu.", value: a11ySelectKeyMode(), kind: a11yRowSelectKey},
		a11yRow{label: "One-handed mode", detail: "Tap the chord button, then a direction, to press A, B, X or Y with one thumb. Tap the chord button again to cancel.", value: oneHanded, kind: a11yRowOneHanded},
		a11yRow{label: "Chord button", detail: "The button that arms the one-handed chord. Pick one your other hand's index finger can reach.", value: a11yChordLabel(a11y.chordButton), kind: a11yRowChordButton},
		a11yRow{label: "Key repeat", detail: "How fast a held direction repeats on the on-screen keyboard. Slow is for hands that overshoot a key.", value: repeat, kind: a11yRowSlowRepeat},
		a11yRow{label: "Power mode", detail: "One setting for battery vs speed: Battery Saver slows the frame and cuts motion, Performance keeps both full, Default lets the Low Power and Reduced Motion toggles decide.", value: powerModeLabel(currentPowerMode(appConfig)), kind: a11yRowPowerMode},
		a11yRow{label: "Vibration strength", detail: "How hard the motor pulses. Soft is for playing in bed, Strong is easier to feel through a case. The Vibration switch in Settings still turns feedback off entirely.", value: hapticsLevelNow().label, kind: a11yRowVibration},
		a11yRow{label: "Reset to defaults", detail: "Back to 100% text, default colours, standard buttons.", value: "", kind: a11yRowReset},
	)
}

// a11yButtonLabel names a physical button the way the panel, the toast and the
// saved keymap all spell it, so "A" means the same thing everywhere.
func a11yButtonLabel(button uint8) string {
	for _, known := range a11yButtonNames {
		if known.button == button {
			return known.label
		}
	}
	return "Button"
}

// a11yButtonTargetLabel names what a physical button currently sends, after the
// keymap and the swap preset. The row must show the effective answer: an
// explicit entry that a preset then rewrites would otherwise read as the wrong
// button.
func a11yButtonTargetLabel(button uint8) string {
	logical, dropped := a11yResolveButton(button)
	if dropped {
		return "Disabled"
	}
	return a11yButtonLabel(logical)
}

// a11yCycleButtonTarget walks the target list for one physical button. The
// order is the saved keymap's own spelling (A, B, X, Y, Select, Start, L1, R1)
// with Disabled last, then back to the button itself — which drops the entry
// instead of storing an identity mapping.
func a11yCycleButtonTarget(button uint8, dir int) uint8 {
	targets := make([]uint8, 0, len(a11yButtonNames)+1)
	for _, known := range a11yButtonNames {
		if known.button == button {
			continue
		}
		targets = append(targets, known.button)
	}
	targets = append(targets, a11yDisabledButton)

	current, _ := a11yResolveButton(button)
	idx := -1
	for i, target := range targets {
		if target == current {
			idx = i
		}
	}
	// Position 0 is "sends itself", so a button with no entry starts there.
	positions := len(targets) + 1
	pos := 0
	if idx >= 0 {
		pos = idx + 1
	}
	pos = ((pos+dir)%positions + positions) % positions
	if pos == 0 {
		return button
	}
	return targets[pos-1]
}

// a11yClearSwapFor drops the preset swap that covers a button, so an explicit
// remap is not rewritten a second time on the way out of a11yResolveButton.
func a11yClearSwapFor(button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B:
		a11y.swapAB = false
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		a11y.swapXY = false
	}
}

// a11yLayoutPresetLabel names the combined state of the whole button layer. A
// preset and a per-button remap are different ways to say the same thing, so
// any explicit entry makes the answer Custom rather than a preset that would
// stop describing the keys.
func a11yLayoutPresetLabel() string {
	if len(a11y.keys) > 0 {
		return "Custom"
	}
	switch {
	case a11y.swapAB && a11y.swapXY:
		return "Nintendo"
	case !a11y.swapAB && !a11y.swapXY:
		return "Standard"
	default:
		return "Custom"
	}
}

// The three states of the Select row. They are stored in the general keymap, so
// the row is a view of the layer rather than a second mechanism.
const (
	a11ySelectDefault  = "Default"
	a11ySelectSwapped  = "Swapped"
	a11ySelectDisabled = "Disabled"
)

func a11ySelectKeyMode() string {
	switch a11y.keys[sdl.CONTROLLER_BUTTON_BACK] {
	case sdl.CONTROLLER_BUTTON_START:
		return a11ySelectSwapped
	case a11yDisabledButton:
		return a11ySelectDisabled
	}
	return a11ySelectDefault
}

// a11ySetSelectKeyMode writes the pair so swapping keeps both buttons
// reachable: Select sends Start and Start sends Select.
func a11ySetSelectKeyMode(mode string) {
	if a11y.keys == nil {
		a11y.keys = map[uint8]uint8{}
	}
	back, start := uint8(sdl.CONTROLLER_BUTTON_BACK), uint8(sdl.CONTROLLER_BUTTON_START)
	switch mode {
	case a11ySelectSwapped:
		a11y.keys[back] = start
		a11y.keys[start] = back
	case a11ySelectDisabled:
		delete(a11y.keys, start)
		a11y.keys[back] = a11yDisabledButton
	default:
		delete(a11y.keys, back)
		delete(a11y.keys, start)
	}
}

// a11ySyncFromTheme makes the panel agree with the palette that is actually on
// screen. Choosing a theme in the gallery replaces our palette without touching
// these flags, and a panel that still said "Deuteranopia" would be lying.
func a11ySyncFromTheme(config *Config) {
	if config == nil {
		return
	}
	current, _ := customString(config.Variables.Custom, "theme_preset")
	if current == "highcontrast" {
		a11y.highContrast = true
		a11y.colorblind = ""
		return
	}
	a11y.highContrast = false
	a11y.colorblind = ""
	for _, mode := range a11yColorblindModes {
		if mode.preset != "" && mode.preset == current {
			a11y.colorblind = mode.key
			return
		}
	}
}

func openAccessibilityPanel(config *Config) {
	qs.open = false
	toolsPanel.open = false
	smartHomePanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	winAppsPanel = windowsAppsPanelState{}
	portsPanel = portsPanelState{}
	chiakiPanel = chiakiPanelState{}
	raPanel.open = false
	a11ySyncFromTheme(config)
	a11y.open = true
	a11y.cursor = 0
	a11y.scroll = 0
	a11yPanelRect = sdl.Rect{}
	a11yRowHits = nil
	PlayActivateSound()
}

func closeAccessibilityPanel() {
	if !a11y.open {
		return
	}
	a11y.open = false
	a11yPanelRect = sdl.Rect{}
	a11yRowHits = nil
	PlayBackSound()
}

func a11yPanelMove(delta int) {
	rows := a11yRows()
	if len(rows) == 0 {
		return
	}
	a11y.cursor += delta
	if a11y.cursor < 0 {
		a11y.cursor = len(rows) - 1
	}
	if a11y.cursor >= len(rows) {
		a11y.cursor = 0
	}
	PlayNavSound()
}

// a11yAdjust moves the focused row's value. dir < 0 is left / previous,
// dir > 0 is right / next.
func a11yAdjust(dir int, config *Config) {
	rows := a11yRows()
	if a11y.cursor < 0 || a11y.cursor >= len(rows) {
		return
	}
	changed := false
	switch rows[a11y.cursor].kind {
	case a11yRowTextSize:
		idx := clampInt(a11yScaleIndex()+dir, 0, len(a11yFontScaleSteps)-1)
		changed = a11ySetScale(a11yFontScaleSteps[idx])
	case a11yRowContrast:
		a11y.highContrast = !a11y.highContrast
		a11yApplyPalette(config)
		changed = true
	case a11yRowReduceTransparency:
		a11y.reduceTransparency = !a11y.reduceTransparency
		ShowToast("Reduce transparency: "+a11yOnOff(a11y.reduceTransparency), ToastKindInfo)
		changed = true
	case a11yRowIncreaseContrast:
		a11y.increaseContrast = !a11y.increaseContrast
		a11yApplyVision()
		ShowToast("Increase contrast: "+a11yOnOff(a11y.increaseContrast), ToastKindInfo)
		changed = true
	case a11yRowBoldText:
		a11y.boldText = !a11y.boldText
		a11yRefreshFonts()
		ShowToast("Bold text: "+a11yOnOff(a11y.boldText), ToastKindInfo)
		changed = true
	case a11yRowMonoAudio:
		changed = a11ySetMonoAudio(!a11y.monoAudio)
	case a11yRowAudioBalance:
		changed = a11ySetAudioBalance(a11y.audioBalance + dir*10)
	case a11yRowCaptionsAlways:
		changed = a11ySetCaptionsAlways(!a11y.captionsAlways)
	case a11yRowCaptionSize:
		changed = a11ySetCaptionSize((a11y.captionSize + dir + 3) % 3)
	case a11yRowCaptionBackground:
		changed = a11ySetCaptionBackground(!a11y.captionBackground)
	case a11yRowLongPress:
		changed = a11ySetLongPress(a11y.longPressMs + dir*50)
	case a11yRowRepeatDelay:
		changed = a11ySetRepeatDelay(a11y.repeatDelayMs + dir*20)
	case a11yRowRepeatRate:
		changed = a11ySetRepeatRate(a11y.repeatRateMs + dir*10)
	case a11yRowToastDuration:
		changed = a11ySetToastPercent(a11y.toastPercent + dir*25)
	case a11yRowStickyChords:
		changed = a11ySetStickyChords(!a11y.stickyChords)
	case a11yRowColorblind:
		changed = a11ySetColorblind(config, a11yColorblindStep(dir))
	case a11yRowButton:
		button := rows[a11y.cursor].button
		changed = a11ySetButtonTarget(button, a11yCycleButtonTarget(button, dir))
	case a11yRowLayoutPreset:
		nintendo := a11y.swapAB && a11y.swapXY && len(a11y.keys) == 0
		changed = a11ySetLayoutPreset(!nintendo)
	case a11yRowSelectKey:
		changed = a11ySetSelectMode(a11ySelectNextMode())
	case a11yRowOneHanded:
		changed = a11ySetOneHanded(!a11y.oneHanded)
	case a11yRowChordButton:
		changed = a11ySetChordButton(a11yChordStep(dir))
	case a11yRowSlowRepeat:
		changed = a11ySetSlowRepeat(!a11y.slowRepeat)
	case a11yRowPowerMode:
		// Stepping the named mode reuses the Quick Settings helper, so there is
		// still exactly one place where a mode becomes live state.
		cur := currentPowerMode(config)
		if config == nil {
			config = appConfig
		}
		label := cyclePowerModeTo(config, nextPowerMode(cur, dir))
		ShowToast("Power mode: "+label, ToastKindInfo)
		changed = true
	case a11yRowVibration:
		level := hapticsCycleLevel(config, dir)
		ShowToast("Vibration: "+level.label, ToastKindInfo)
		changed = true
	case a11yRowReset:
		a11yResetDefaults(config)
		changed = true
	}
	if changed {
		PlayNavSound()
		a11ySave(config)
	}
}

func ctrlAccessibilityPanel(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		a11yPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		a11yPanelMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		a11yAdjust(-1, config)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_A:
		a11yAdjust(1, config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeAccessibilityPanel()
	}
	return true
}

func handleAccessibilityKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !a11y.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		a11yPanelMove(-1)
	case sdl.K_DOWN:
		a11yPanelMove(1)
	case sdl.K_LEFT:
		a11yAdjust(-1, config)
	case sdl.K_RIGHT, sdl.K_RETURN, sdl.K_SPACE:
		a11yAdjust(1, config)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		closeAccessibilityPanel()
	}
	return true
}

func handleAccessibilityMouse(mx, my int32, config *Config) bool {
	if !a11y.open {
		return false
	}
	for _, hit := range a11yRowHits {
		if pointInRect(mx, my, hit.rect) {
			a11y.cursor = hit.index
			a11yAdjust(1, config)
			return true
		}
	}
	if a11yPanelRect.W > 0 && pointInRect(mx, my, a11yPanelRect) {
		return true
	}
	closeAccessibilityPanel()
	return true
}

// renderAccessibilityPanel draws the card. It follows the Tools / Ports / Remote
// Play layout so it does not look bolted on.
func renderAccessibilityPanel(renderer *sdl.Renderer, config *Config) {
	if !a11y.open || renderer == nil {
		return
	}
	rows := a11yRows()
	if a11y.cursor >= len(rows) {
		a11y.cursor = len(rows) - 1
	}
	if a11y.cursor < 0 {
		a11y.cursor = 0
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
	medH := int32(medium.Height())

	const padX = OverlayPadX
	// Rows are one line each (label left, value right) and the focused row's
	// explanation is drawn once underneath the list. Two-line rows wasted a
	// third of a 720p screen and left the last settings unreachable at 130%
	// text; this way every row fits at every scale.
	rowH := 10 + medH + 10

	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar that fills it measure the same height.
	header := UIPanelHeader(a11yPanelTitle,
		"Applies to every screen · saved automatically", "settings")
	headerH := UIPanelHeaderBand(config, header, 14)
	detailH := 10 + smallH + 10
	footerH := 20 + smallH

	panelW := int32(820)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 40 - headerH - detailH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	visible := int32(len(rows))
	if visible > maxRows {
		visible = maxRows
	}
	panelH := headerH + visible*rowH + detailH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	a11yPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if a11y.cursor < a11y.scroll {
		a11y.scroll = a11y.cursor
	}
	if a11y.cursor >= a11y.scroll+int(visible) {
		a11y.scroll = a11y.cursor - int(visible) + 1
	}
	if a11y.scroll > len(rows)-int(visible) {
		a11y.scroll = len(rows) - int(visible)
	}
	if a11y.scroll < 0 {
		a11y.scroll = 0
	}

	drawOverlayScrim(renderer)

	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	UIPageHeaderDraw(renderer, config, header)

	focusColor := installHighlight()
	rowW := panelW - 2*padX
	a11yRowHits = a11yRowHits[:0]
	for i := int32(0); i < visible; i++ {
		idx := a11y.scroll + int(i)
		row := rows[idx]
		y := panelY + headerH + i*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 4}
		a11yRowHits = append(a11yRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: idx, rect: r})

		focused := idx == a11y.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}

		renderText(renderer, config, medium, row.label, ColorTextPrimary(), r.X+16, r.Y+10)
		if row.value != "" {
			color := ColorTextSecondary()
			if focused {
				color = focusColor
			}
			value := row.value
			if focused {
				value = "< " + value + " >"
			}
			vw, _, _ := font.SizeUTF8(value)
			renderText(renderer, config, font, value, color, r.X+r.W-16-int32(vw), r.Y+10+(medH-smallH)/2)
		}
	}

	// One explanation for the focused row, under the list.
	detail := rows[a11y.cursor].detail
	if detail == "" {
		detail = hintSentence("Change the value with Left/Right, or press %s.", "A", "Next")
	}
	detailY := panelY + headerH + visible*rowH + 10
	renderText(renderer, config, font, ellipsize(font, detail, rowW), ColorTextTertiary(), panelX+padX, detailY)

	hint := hintLine(FooterHint{"Left/Right", "Change"}, FooterHint{"A", "Next"}, FooterHint{"B", "Close"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+padX, panelY+panelH-footerH+8)
}
