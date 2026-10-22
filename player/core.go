// core.go is the application core recovered from git history (c8aa566
// player/main.go, before the "Patch" commit gutted it to a placeholder).
// It provides the rendering primitives, scene management, input handling,
// playback helpers and config loading that the feature files call.
// The type declarations live in config.go / element.go / videos_info.go, and
// func main intentionally stays out (main.go is the rewrite's entry point).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Cleanup is a list of functions to run on program exit for resource cleanup.
// This includes security-sensitive cleanup like key zeroization.
var Cleanup []func()

// --- Global state for input and navigation ---
var (
	appConfig                  *Config
	_                              = Cleanup // ensure Cleanup is used
	currentSceneIndex          int = -1
	selectedButtonIndex        int = -1
	activeSceneIndex           int = -1
	activeElementIndex         int = -1
	inputTextBuffer            string
	virtualKeyboardActive      bool
	keyboard                   [][]string
	keyboardPosX, keyboardPosY int
	keyboardRects              []sdl.Rect // for mouse hit detection
	keyboardKeys               []string   // parallel array of key labels
	keyboardRowCol             [][2]int   // parallel (row,col) for each rect
	keyboardUpper              bool       // uppercase toggle state
	loadingJokes               = []string{
		"Compiling JukaLang... or just thinking really hard?",
		"JukaLang: where semicolons are merely a suggestion.",
		"If it compiles on the first try, it's probably JukaLang.",
		"JukaHub runs on coffee and the JukaLang runtime.",
		"Loading JukaLang bytecode, byte by byte, with love.",
		"JukaLang is busy judging your variable names.",
		"Why did the Juka cross the runtime? To reach the other JukaHub.",
		"Fun fact: bugs are just unexpected JukaLang features.",
		"Hang tight — JukaLang is optimizing your fun.",
		"JukaHub: powered by JukaLang and questionable life choices.",
		"In JukaLang we trust. Everything else is a runtime error.",
		"Loading... JukaLang is secretly writing your code for you.",
		"Please wait. Even JukaLang needs a moment to feel dramatic.",
		"Loading: because instant gratification takes time.",
		"Reticulating splines... wrong game, still loading.",
		"This loading screen contains no artificial preservatives.",
		"If loading takes long, blame the semicolons. There are none.",
		"One bytecode to rule them all, one runtime to bind them.",
		"Still faster than a Windows update.",
		"0 errors, 0 warnings, 100% vibes.",
		"JukaLang never sleeps. It just reloads.",
		"The progress bar is the main character of this story.",
		"Your SD card believes in you. So do we.",
		"Warning: side effects may include actual productivity.",
		"Meanwhile, in the runtime: absolutely everything.",
		"Counting to infinity. We got as far as 'loading'.",
		"JukaLang is doing its best. Its best is quite good.",
		"Do not adjust your screen. The progress is real.",
		"Now with 47% fewer mystery crashes!",
		"This app was made with love and stack traces.",
		"Warming up the pixels. The pixels are shy.",
		"JukaHub: your games, your media, your late-night excuses.",
		"Even the loading bar needs moral support.",
		"Compiling vibes. Optimizing fun. Shipping joy.",
		"JukaLang compiles your dreams. Syntax errors first, dreams later.",
		"Somewhere a garbage collector is humming your favourite tune.",
		"JukaHub: turning SD cards into tiny arcades since day one.",
		"Loading the fun. Please keep your hands inside the runtime.",
		"Every byte here was typed by a human who believed in semicolons.",
		"JukaLang: the language your emulator dreams in.",
		"We measured twice, compiled once and blamed the linker anyway.",
		"This screen is fully recyclable. The wait is not.",
		"JukaHub: now with fewer loading screens, including this one.",
		"Warming up the CPU. It insists it is not a morning person.",
		"Your playlist and your patience are both buffering.",
		"JukaLang tip: name variables like someone will read them.",
		"Almost there. Almost is a unit of time measured in vibes.",
		"The runtime is thinking. It is not used to it, give it a second.",
		"Fun detected. Deploying the appropriate amount of loading bar.",
		"If you can read this, the semicolons have not won yet.",
		"JukaHub: retro hardware, futurist excuses.",
		"Loading assets, opinions and one very stubborn font.",
		"There is no place like 127.0.0.1, and we are nearly there.",
		"Brought to you by the Society for Fewer Mystery Crashes.",
	}

	// Home screen layout state (shared between render and mouse hit-testing)
	homeLayoutActive     bool
	homeTileElementIndex []int // slot -> scene element index

	// Focus engine (scene-level focus graph with persistence)
	focusEngine = NewFocusEngine()

	// Seek bar drag state
	isDraggingSeekBar bool
	seekBarDragRect   sdl.Rect

	// Scene-title-row back affordance (populated each frame by renderSceneTitleRow).
	sceneTitleBackRect sdl.Rect
	statusClockRect    sdl.Rect // status-bar clock hit/focus area (measured each frame)

	// Scene history for B/Esc back navigation on non-home scenes.
	sceneHistory []int

	// Bounded cache for decoded thumbnail textures (see thumbstore.go).
	thumbnailCacheMutex sync.Mutex

	// Placeholder texture for missing thumbnails
	placeholderTexture *sdl.Texture

	// Async thumbnail download queue
	thumbnailDownloadCh chan string
	thumbnailDataCache  = make(map[string][]byte)

	// Focus for search result items (grid index)
	focusedResultIndex int = -1

	// Smooth scroll offset for search results grid
	scrollY int32 = 0

	// Scroll offset for text browser panels
	textBrowserScrollY int32 = 0

	// Last update timestamp for text browser content
	textBrowserLastUpdate int64

	// Kinetic scroll state for text browser panels
	textBrowserScrollVelocity float64
	textBrowserScrollFriction = 0.92
	textBrowserScrollAccel    = 1.4
	textBrowserScrollMaxVel   = 18.0
	textBrowserScrollCooldown int

	// Toast notifications (single active toast, see ShowToast / renderToast)
	currentToast ToastState

	// Scene transition state
	transitionPhase   string = "none" // none | fade-out | fade-in
	pendingSceneIndex int
	sceneFadeAlpha    uint8 = 255
	sceneFadeStart    uint64

	// Simple animation state
	animTime float64

	// Frame counter for periodic tasks (memory checks, etc.)
	frameCount uint64

	// Mouse hover state
	mouseX, mouseY     int32   = -100, -100
	hoveredButtonIndex int     = -1
	hoverAnimProgress  float64 = 0

	// Button press feedback
	pressedButtonIndex int = -1
	pressStartTime     uint64
	pressDuration      uint64 = 150

	// Loading overlay
	loadingAlpha uint8 = 0
	loadingText  string

	// Image viewer state
	imageViewerPath    string
	imageViewerZoom    float64 = 1.0
	imageViewerPanX    int32   = 0
	imageViewerPanY    int32   = 0
	imageViewerTexture *sdl.Texture
	imageViewerW       int32
	imageViewerH       int32

	// Touch gesture state
	touchStartX      float64
	touchStartY      float64
	touchStartTime   uint64
	touchActive      bool
	touchPinchDist   float64
	touchPinchActive bool

	// JukaLand gamepad state
	jukalandMoveX       float64
	jukalandMoveY       float64
	jukalandBtnBreak    bool
	jukalandBtnPlace    bool
	jukalandBtnJump     bool
	jukalandBtnInteract bool
	jukalandBtnCraft    bool
	jukalandBtnExit     bool

	// Unit converter state
	unitCategory    string = "length"
	unitFrom        string = "m"
	unitTo          string = "ft"
	unitInputValue  string
	unitInputActive bool
	unitInputRect   sdl.Rect
	unitResult      string

	// Chat state
	chatMessages    []ChatMessage
	chatMutex       sync.Mutex
	chatInputText   string
	chatInputActive bool

	// Canvas Sandbox state
	canvasCode    string
	canvasSurface *sdl.Surface

	// Video playback state
	videoPlaybackPhase    string // idle | downloading | playing | error
	videoPlaybackPhaseAt  uint64 // monotonic ticks when the phase last changed
	videoPlaybackProgress float64
	videoPlaybackSpeed    string
	videoPlaybackETA      string
	videoPlaybackError    string
	videoPlaybackCmd      *exec.Cmd
	videoPlaybackMutex    sync.Mutex

	embeddedPlaybackPath string

	// mainWindow is kept global so runtime settings (e.g. fullscreen toggle)
	// can apply changes without threading the window through every handler.
	mainWindow *sdl.Window

	// Logical screen size (configurable for devices like TrimuiSmartPro)
	screenWidth  int32 = 1280
	screenHeight int32 = 720

	// Window scaling state for resizable Windows builds. The renderer renders
	// into a fixed 1280x720 logical canvas and SDL letterboxes to the physical
	// window. Mouse coordinates are converted from physical to logical so hit
	// testing stays correct after resizing.
	windowPhysicalW   int32   = 1280
	windowPhysicalH   int32   = 720
	windowScaleFactor float64 = 1.0
	windowOffsetX     int32   = 0
	windowOffsetY     int32   = 0
)

// updateWindowScale recalculates the scale and letterbox offsets used to map
// physical mouse coordinates into the logical 1280x720 canvas. It should be
// called whenever the SDL window is resized.
func updateWindowScale(physicalW, physicalH int32) {
	windowPhysicalW, windowPhysicalH = physicalW, physicalH
	if windowPhysicalW <= 0 || windowPhysicalH <= 0 {
		windowScaleFactor = 1.0
		windowOffsetX, windowOffsetY = 0, 0
		return
	}
	scaleW := float64(windowPhysicalW) / float64(screenWidth)
	scaleH := float64(windowPhysicalH) / float64(screenHeight)
	if scaleW < scaleH {
		windowScaleFactor = scaleW
	} else {
		windowScaleFactor = scaleH
	}
	if windowScaleFactor < 0.0001 {
		windowScaleFactor = 0.0001
	}
	logicalVisibleW := int32(float64(screenWidth) * windowScaleFactor)
	logicalVisibleH := int32(float64(screenHeight) * windowScaleFactor)
	windowOffsetX = (windowPhysicalW - logicalVisibleW) / 2
	windowOffsetY = (windowPhysicalH - logicalVisibleH) / 2
}

// physicalToLogical converts physical (window-pixel) mouse coordinates into
// the logical 1280x720 canvas, accounting for letterboxing on resize.
func physicalToLogical(px, py int32) (int32, int32) {
	sx := int32(float64(px-windowOffsetX) / windowScaleFactor)
	sy := int32(float64(py-windowOffsetY) / windowScaleFactor)
	return sx, sy
}

// Background goroutines (search, tickers, podcasts, ...) must not touch the
// shared Variables.Custom map directly — the render loop and event handlers
// read it on the main thread, and a plain map is not safe for concurrent
// access. Instead goroutines publish results here and the main loop applies
// them once per frame via drainCustom.
type customUpdate struct {
	key string
	val interface{}
}

var customCh = make(chan customUpdate, 64)

// publishCustom queues a Custom value written by a background goroutine.
func publishCustom(key string, val interface{}) {
	select {
	case customCh <- customUpdate{key: key, val: val}:
	default:
		// The render loop drains this queue every frame. If it is ever busy or
		// stalled, a background publisher (network monitor, server state, tool
		// progress) must not block on a full channel: the value is transient
		// and the next publish refreshes it.
	}
}

// drainCustom applies any pending background updates to the Custom map. Must
// be called on the main thread (inside the event/render loops) only.
func drainCustom(config *Config) {
	for {
		select {
		case u := <-customCh:
			config.Variables.Custom[u.key] = u.val
		default:
			return
		}
	}
}

// snapshotVars returns a shallow copy of Custom for use inside a goroutine so
// it never reads the live shared map.
func snapshotVars(config *Config) map[string]interface{} {
	m := make(map[string]interface{}, len(config.Variables.Custom))
	for k, v := range config.Variables.Custom {
		m[k] = v
	}
	return m
}

// --- Helper functions for dimensions ---
func getElementWidth(elem Element, defaultWidth int32) int32 {
	if elem.Width == "" {
		return defaultWidth
	}
	s := string(elem.Width)
	if val, ok := parsePosInt(s); ok {
		return val
	}
	return defaultWidth
}

func getElementHeight(elem Element, defaultHeight int32) int32 {
	if elem.Height == "" {
		return defaultHeight
	}
	s := string(elem.Height)
	if val, ok := parsePosInt(s); ok {
		return val
	}
	return defaultHeight
}

// parsePosInt safely parses the given text as a non-negative int32, avoiding
// silent truncation from very large values and non-numeric input.
func parsePosInt(text string) (int32, bool) {
	v, err := strconv.ParseInt(text, 10, 32)
	if err != nil || v < 0 {
		return 0, false
	}
	return int32(v), true
}

// parseInt32 parses a signed decimal integer only when it fits in int32.
func parseInt32(text string) (int32, bool) {
	v, err := strconv.ParseInt(text, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(v), true
}

func resolveDimension(text string, fallback int32) int32 {
	if v, ok := parseInt32(text); ok {
		return v
	}
	return fallback
}

func resolveElementDimensions(e Element, width, height int32) (int32, int32) {
	return resolveDimension(string(e.Width), width), resolveDimension(string(e.Height), height)
}

func resolveButtonDimensions(e Element, width, height int32) (int32, int32) {
	defaultWidth := width + 56
	defaultHeight := height + 36
	width = resolveDimension(string(e.Width), defaultWidth)
	height = resolveDimension(string(e.Height), defaultHeight)
	// Non-positive overrides are treated as missing: fall back to the padded
	// defaults, not to the bare padding constants.
	if width <= 0 {
		width = defaultWidth
	}
	if height <= 0 {
		height = defaultHeight
	}
	return width, height
}

// parseNonNegFloat safely parses the given text as a non-negative float64.
func parseNonNegFloat(text string) (float64, bool) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// --- Helper functions ---
func loadConfig(filename string) (*Config, error) {
	if filename == "" {
		return nil, fmt.Errorf("config filename is empty")
	}
	info, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file %s does not exist", filename)
		}
		return nil, fmt.Errorf("cannot access config file %s: %w", filename, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("config path %s is a directory, not a file", filename)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("cannot open config file %s: %w", filename, err)
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return nil, fmt.Errorf("config file %s is not valid JSON: %w", filename, err)
	}
	// Migrate older schemas forward, and refuse a newer one so an older build
	// never rewrites a file it does not understand.
	if _, err := MigrateConfig(&config); err != nil {
		return nil, fmt.Errorf("config file %s: %w", filename, err)
	}
	if config.Variables.Fonts == nil {
		config.Variables.Fonts = make(map[string]string)
	}
	if config.Variables.FontSizes == nil {
		config.Variables.FontSizes = make(map[string]int)
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}
	finalizeConfig(&config)
	return &config, nil
}

// varPattern is compiled once. It used to be compiled inside substituteVars,
// which every renderText call reaches, so a frame with 40 labels paid for 40
// regex compilations (~1.4us and 1.7KB of garbage each on the handheld).
var varPattern = regexp.MustCompile(`\$(\w+)`)

func substituteVars(text string, vars map[string]interface{}) string {
	// Fast path: almost every string on screen has no substitutions at all.
	if text == "" || !strings.Contains(text, "$") || len(vars) == 0 {
		return text
	}
	return varPattern.ReplaceAllStringFunc(text, func(m string) string {
		varName := m[1:]
		if val, ok := vars[varName]; ok {
			return fmt.Sprintf("%v", val)
		}
		return m
	})
}

var (
	ytSearchKeyPattern = regexp.MustCompile(`"ytsearch\d+:[^"]+"`)
	ffmpegFlagPattern  = regexp.MustCompile(`-f "[^"]*"`)
	spaceRunPattern    = regexp.MustCompile(`\s+`)
)

func normalizeCacheKey(raw string) string {
	s := strings.ToLower(raw)
	s = ytSearchKeyPattern.ReplaceAllString(s, `"ytsearch:QUERY"`)
	s = ffmpegFlagPattern.ReplaceAllString(s, "")
	s = spaceRunPattern.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func resolveColor(config *Config, colorName string, defaultColor sdl.Color) sdl.Color {
	if strings.HasPrefix(colorName, "$") {
		name := colorName[1:]
		// Theme colours resolve through the effective value, not Variables.Get:
		// an unset field is stored as zero RGB, which must inherit the active
		// theme rather than paint buttons black.
		if config != nil && isThemeColorVariable(name) {
			c := effectiveThemeColor(config, name)
			return sdl.Color{R: uint8(c.R), G: uint8(c.G), B: uint8(c.B), A: 255}
		}
		colorValue := config.Variables.Get(name)
		parts := strings.Split(colorValue, ",")
		if len(parts) == 3 {

			// Channels are unsigned bytes: a signed 8-bit parse rejected every
			// value above 127, so "52,152,219" silently fell back to the
			// default colour instead of the configured one.
			r, errR := strconv.ParseUint(parts[0], 10, 8)
			g, errG := strconv.ParseUint(parts[1], 10, 8)
			b, errB := strconv.ParseUint(parts[2], 10, 8)
			if errR == nil && errG == nil && errB == nil {
				return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
			}
		}
		// Custom stores colors as "#rrggbb" strings; parse those too instead
		// of silently falling back to the default.
		if c, ok := parseHexColor(colorValue); ok {
			return c
		}
		return defaultColor
	}

	if colorName != "" {
		r, g, b := hexToRGB(colorName)
		return sdl.Color{R: r, G: g, B: b, A: 255}
	}
	return defaultColor
}

func hexToRGB(hex string) (uint8, uint8, uint8) {
	if len(hex) == 7 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return 0, 0, 0
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return uint8(r), uint8(g), uint8(b)
}

// parseHexColor parses a "#rrggbb" or "rrggbb" string into an opaque color,
// reporting whether the string was valid.
func parseHexColor(s string) (sdl.Color, bool) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) != 6 {
		return sdl.Color{}, false
	}
	r, e1 := strconv.ParseUint(h[0:2], 16, 8)
	g, e2 := strconv.ParseUint(h[2:4], 16, 8)
	b, e3 := strconv.ParseUint(h[4:6], 16, 8)
	if e1 != nil || e2 != nil || e3 != nil {
		return sdl.Color{}, false
	}
	return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, true
}

// customString returns a Custom map value under the given name, accepting any
// key casing (the web editor emits capitalized keys like ButtonColor). String
// values win over non-strings when casing collides.
// customValue resolves a custom variable by exact key, then case-insensitively
// (the web builder capitalises keys: ButtonColor, GridColumns, ...). A string
// value wins over a non-string one when both spellings are present, because the
// string form is the real value.
func customValue(m map[string]interface{}, name string) (interface{}, bool) {
	if v, ok := m[name]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			if _, isStr := v.(string); isStr {
				return v, true
			}
		}
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// coerceBool reads the several shapes a variable value arrives in: a real bool
// from JSON, a "true"/"false" string, or a number (non-zero is true).
func coerceBool(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	}
	return false
}

// coerceInt accepts numbers, numeric strings and booleans.
func coerceInt(v interface{}) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func coerceString(v interface{}) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return fmt.Sprintf("%v", v)
}

// flattenCustomBlocks merges nested "Custom" blocks (the shape the web builder
// regenerates) into the top level so every setting resolves the same way.
func flattenCustomBlocks(custom map[string]interface{}) {
	for depth := 0; depth < 4; depth++ {
		nested, ok := custom["Custom"].(map[string]interface{})
		if !ok {
			break
		}
		for k, val := range nested {
			if _, exists := custom[k]; !exists {
				custom[k] = val
			}
		}
		delete(custom, "Custom")
	}
	// A <Custom/> block can also arrive as an empty string rather than a map.
	if _, ok := custom["Custom"].(string); ok {
		delete(custom, "Custom")
	}
}

// finalizeConfig applies the defaults and normalisations that must hold for
// every config source, so a config loaded from XML behaves exactly like one
// loaded from JSON.
func finalizeConfig(config *Config) {
	if config == nil {
		return
	}
	if config.SchemaVersion == 0 {
		config.SchemaVersion = ConfigSchemaVersion
	}
	if config.AppName == "" {
		config.AppName = "JukaHub"
	}
	if config.Version == "" {
		config.Version = "0.4.0"
	}
	if config.Width <= 0 {
		config.Width = 1280
	}
	if config.Height <= 0 {
		config.Height = 720
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}
	if config.Variables.Fonts == nil {
		config.Variables.Fonts = make(map[string]string)
	}
	if config.Variables.FontSizes == nil {
		config.Variables.FontSizes = make(map[string]int)
	}
	if config.Variables.BackgroundImage == "" {
		if v, ok := customValue(config.Variables.Custom, "backgroundImage"); ok {
			config.Variables.BackgroundImage = coerceString(v)
		}
	}
	for i := range config.Scenes {
		for j := range config.Scenes[i].Elements {
			elem := &config.Scenes[i].Elements[j]
			// Both trigger spellings the builder emits resolve to a scene target.
			if elem.TriggerTarget == "" && elem.SceneChange != "" {
				elem.TriggerTarget = elem.SceneChange
			}
			if strings.HasPrefix(elem.Trigger, "change_scene:") {
				elem.TriggerTarget = strings.TrimPrefix(elem.Trigger, "change_scene:")
			}
			// The web builder stores a trigger's single argument in triggerValue
			// (it has no triggerTarget field). Handlers that read TriggerTarget
			// therefore used to see an empty target for every element built in
			// the editor; filling it in here fixes the JSON and XML loaders at
			// once, and leaves configs that set both fields untouched.
			if elem.TriggerTarget == "" && elem.TriggerValue != "" {
				elem.TriggerTarget = elem.TriggerValue
			}
		}
	}
	// Make the network/FTP tools reachable from the classic Tools (Misc)
	// screen without editing configs on disk.
	ensureToolsMenuEntry(config)
	// Give the Hardware screen its interactive process list entry point.
	ensureHardwareProcessEntry(config)
	// And the Wi-Fi picker, beside it on the Hardware action row.
	ensureHardwareWiFiEntry(config)
	// IPStream library entry points (IPStream screen + Media page).
	ensureIPStreamEntries(config)
}

func customString(m map[string]interface{}, name string) (string, bool) {
	if v, ok := m[name]; ok {
		return fmt.Sprintf("%v", v), true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			if _, isStr := v.(string); isStr {
				return fmt.Sprintf("%v", v), true
			}
		}
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return fmt.Sprintf("%v", v), true
		}
	}
	return "", false
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Cache fonts
// fontCache stores fonts keyed by "path\nsize" so the same font at
// different sizes (e.g. "small"=16, "medium"=24, "big"=36) is cached
// separately.  On TSP this avoids re-opening the same .ttf file dozens
// of times per startup.
var fontCache = make(map[string]*ttf.Font)

func getCachedFont(config *Config, fontName string) (*ttf.Font, int) {
	// Resolve the path and size for this logical font name.
	fontPath := "Inter-Regular.ttf"
	size := 24
	for k, p := range config.Variables.Fonts {
		if strings.EqualFold(k, fontName) {
			fontPath = p
			break
		}
	}
	for k, v := range config.Variables.FontSizes {
		if strings.EqualFold(k, fontName) {
			size = v
			break
		}
	}
	// Accessibility text scaling applies here, at the one place every logical
	// font is resolved, so no screen can be left behind at the default size.
	size = a11yScaleFontSize(size)
	return loadCachedFont(fontPath, size), size
}

// getFontSized opens a logical font at a pixel size the caller chooses, for
// displays that need a size no config role defines (the ambient clock). It
// shares the cache, fallback chain and accessibility scaling with
// getCachedFont.
func getFontSized(config *Config, fontName string, size int) *ttf.Font {
	fontPath := "Inter-Regular.ttf"
	for k, p := range config.Variables.Fonts {
		if strings.EqualFold(k, fontName) {
			fontPath = p
			break
		}
	}
	return loadCachedFont(fontPath, a11yScaleFontSize(size))
}

// loadCachedFont opens path at size (already accessibility-scaled) and caches it
// by the pair, so the same face at different sizes is cached separately.
func loadCachedFont(fontPath string, size int) *ttf.Font {
	key := fontPath + "\n" + strconv.Itoa(size)
	if font, ok := fontCache[key]; ok {
		return font
	}

	font, err := ttf.OpenFont(resolvePath(fontPath), size)
	if err != nil {
		// Fall back to a chain of likely-present fonts so text always renders.
		for _, candidate := range []string{"Inter-Regular.ttf", "DejaVuSans.ttf", "DejaVuSans-Bold.ttf", "arial.ttf", "Roboto-Black.ttf"} {
			if candidate == "" || candidate == fontPath {
				continue
			}
			if f, e2 := ttf.OpenFont(resolvePath(candidate), size); e2 == nil {
				font, err = f, nil
				break
			}
		}
	}
	if err != nil {
		log.Printf("Error loading font %s: %v", fontPath, err)
		return nil
	}
	// Remember where this font was opened so renderText can read its cmap and
	// substitute characters it cannot draw (see fonts.go).
	registerFontPath(font, fontPath)
	fontCache[key] = font
	return font
}

// renderText draws text and returns its rendered size. Characters the font
// cannot draw (CJK, Thai, Arabic, Devanagari, Hebrew, emoji) are mapped to the
// closest supported character or collapsed into an ellipsis, so nothing ever
// renders as a .notdef box. Supported text is passed through unchanged. See
// fonts.go.
func renderText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) (int32, int32) {
	processed := substituteVars(text, config.Variables.Custom)
	if processed == "" || font == nil {
		return 0, 0
	}
	return drawTextRun(renderer, config, font, sanitizeForFont(font, processed), color, x, y)
}

// drawTextRun renders one font/text pair through the shared texture cache.
func drawTextRun(renderer *sdl.Renderer, config *Config, font *ttf.Font, processed string, color sdl.Color, x, y int32) (int32, int32) {
	if processed == "" || font == nil {
		return 0, 0
	}

	// Try the text cache first (major perf win on TSP ARM64)
	cache := GetTextCache()
	if entry := cache.Get(font, processed, color); entry != nil {
		renderer.Copy(entry.texture, nil, &sdl.Rect{X: x, Y: y, W: entry.width, H: entry.height})
		return entry.width, entry.height
	}

	// Cache miss: render and cache
	surface, err := font.RenderUTF8Blended(processed, color)
	if err != nil {
		return 0, 0
	}
	defer surface.Free()
	texture, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return 0, 0
	}
	_, _, w, h, _ := texture.Query()
	renderer.Copy(texture, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
	cache.Put(font, processed, color, texture, w, h)
	return w, h
}

// renderTextWithFallback renders text with a fallback color if the original color is invalid.
func renderTextWithFallback(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, fallbackColor sdl.Color, x, y int32) {
	// Use fallback color if the original color has invalid alpha or is fully transparent.
	if color.A == 0 {
		color = fallbackColor
	}
	w, _ := renderText(renderer, config, font, text, color, x, y)
	// If rendering failed, render with fallback color
	if w == 0 && font != nil {
		renderText(renderer, config, font, text, fallbackColor, x, y)
	}
}

// --- Visual theme helpers ---------------------------------------------------

var (
	bgTexture    *sdl.Texture
	bgImagePath  string
	overlayColor = sdl.Color{R: 8, G: 10, B: 18, A: 150}

	gradientTexture    *sdl.Texture
	gradientSceneKey   string
	gradientOverlayKey sdl.Color
)

// exeDir returns the directory containing the running executable, so asset
// paths can be resolved regardless of the current working directory.
func exeDir() string {
	if ex, err := os.Executable(); err == nil {
		return filepath.Dir(ex)
	}
	return "."
}

// resolvePath resolves a possibly-relative asset path against the executable
// directory when it is not found in the current working directory.
func resolvePath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if alt := filepath.Join(exeDir(), p); alt != p {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return p
}

// ensureBackgroundTexture loads (or reloads) the background image when the
// configured path changes. The texture is cached so it is not re-read from
// disk every frame.
func ensureBackgroundTexture(renderer *sdl.Renderer, config *Config) {
	path := config.Variables.BackgroundImage
	if path == "" {
		// No custom background image was configured: rely on the theme-driven
		// gradient (rendered in renderScene's else branch). This lets the
		// selected theme preset visibly change the background instead of being
		// hidden behind a fixed image + dark overlay.
		if bgTexture != nil {
			bgTexture.Destroy()
			bgTexture = nil
		}
		bgImagePath = ""
		return
	}
	if path == bgImagePath && bgTexture != nil {
		return
	}
	if bgTexture != nil {
		bgTexture.Destroy()
		bgTexture = nil
	}
	bgImagePath = path
	if path == "" {
		return
	}
	p := resolvePath(path)
	if tex, err := img.LoadTexture(renderer, p); err == nil {
		bgTexture = tex
		log.Printf("Background image loaded: %s", p)
		return
	} else if surface, err2 := img.Load(p); err2 == nil {
		// Fallback: load as a surface and upload manually.
		if tex2, err3 := renderer.CreateTextureFromSurface(surface); err3 == nil {
			bgTexture = tex2
			surface.Free()
			log.Printf("Background image loaded (surface): %s", p)
			return
		}
		surface.Free()
		log.Printf("Background image load failed (%s): %v", path, err2)
	} else {
		log.Printf("Background image load failed (%s): %v", path, err)
	}
}

func ensureGradientTexture(renderer *sdl.Renderer, config *Config, bg sdl.Color) {
	key := fmt.Sprintf("%s-%d-%d-%d", config.Title, bg.R, bg.G, bg.B)
	if gradientTexture != nil && gradientSceneKey == key && gradientOverlayKey == overlayColor {
		return
	}
	if gradientTexture != nil {
		gradientTexture.Destroy()
		gradientTexture = nil
	}
	gradientSceneKey = key
	gradientOverlayKey = overlayColor

	surface, err := sdl.CreateRGBSurfaceWithFormat(0, screenWidth, screenHeight, 32, sdl.PIXELFORMAT_RGBA8888)
	if err != nil {
		return
	}
	defer surface.Free()

	for y := int32(0); y < screenHeight; y++ {
		t := float32(y) / float32(screenHeight)
		// Blend the theme background toward a slightly lighter shade so the
		// gradient always matches the active theme (instead of a fixed dark
		// overlay that made light themes look dark).
		end := lighten(bg, 12)
		r := uint8(float32(bg.R) + t*float32(end.R-bg.R))
		g := uint8(float32(bg.G) + t*float32(end.G-bg.G))
		b := uint8(float32(bg.B) + t*float32(end.B-bg.B))
		c := uint32(r) | uint32(g)<<8 | uint32(b)<<16 | 0xFF000000
		rect := &sdl.Rect{X: 0, Y: y, W: screenWidth, H: 1}
		surface.FillRect(rect, c)
	}

	tex, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return
	}
	gradientTexture = tex
}

func lighten(c sdl.Color, amt int) sdl.Color {
	clamp := func(v int) uint8 {
		if v > 255 {
			return 255
		}
		return uint8(v)
	}
	return sdl.Color{R: clamp(int(c.R) + amt), G: clamp(int(c.G) + amt), B: clamp(int(c.B) + amt), A: c.A}
}

func darken(c sdl.Color, amt int) sdl.Color {
	return lighten(c, -amt)
}

func lerpColor(a, b sdl.Color, t float32) sdl.Color {
	clamp := func(v int) uint8 {
		if v > 255 {
			return 255
		}
		if v < 0 {
			return 0
		}
		return uint8(v)
	}
	return sdl.Color{
		R: clamp(int(a.R) + int(float32(b.R-a.R)*t)),
		G: clamp(int(a.G) + int(float32(b.G-a.G)*t)),
		B: clamp(int(a.B) + int(float32(b.B-a.B)*t)),
		A: a.A,
	}
}

// strokeCircle draws an unfilled circle outline using an angle fan.
func strokeCircle(renderer *sdl.Renderer, cx, cy, r int32, c sdl.Color) {
	if r <= 0 {
		return
	}
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	steps := r * 8
	if steps < 32 {
		steps = 32
	}
	for i := int32(0); i < steps; i++ {
		ang := float64(i) * 2 * math.Pi / float64(steps)
		x := cx + int32(math.Round(float64(r)*math.Cos(ang)))
		y := cy + int32(math.Round(float64(r)*math.Sin(ang)))
		renderer.DrawPoint(x, y)
	}
}

// fillCircle draws a filled circle (Go-SDL2 has no native helper).
func fillCircle(renderer *sdl.Renderer, cx, cy, r int32, c sdl.Color) {
	if r <= 0 {
		return
	}
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	for dy := int32(0); dy <= r; dy++ {
		dx := int32(math.Sqrt(float64(r*r - dy*dy)))
		renderer.DrawLine(cx-dx, cy+dy, cx+dx, cy+dy)
		if dy != 0 {
			renderer.DrawLine(cx-dx, cy-dy, cx+dx, cy-dy)
		}
	}
}

// diskPieRadius returns the donut radius that fits the card: inside its padding
// and clear of the two usage lines in the bottom-left corner.
func diskPieRadius(elemW, elemH int32) int32 {
	r := int32(120)
	if r > elemW/2-20 {
		r = elemW/2 - 20
	}
	if r > elemH/2-88 {
		r = elemH/2 - 88
	}
	if r < 20 {
		r = 20
	}
	return r
}

// diskPieUsageLayout places the used/free figures in the bottom-left corner of
// the chart card. Pure geometry so a test can pin that the numbers never sit on
// top of the centred percentage.
func diskPieUsageLayout(element Element, elemH, lineH int32) (x, y int32) {
	x = element.X + 24
	y = element.Y + elemH - 2*lineH - 26
	if y < element.Y+12 {
		y = element.Y + 12
	}
	return x, y
}

// renderDiskPieChart draws a donut-style pie chart showing used vs free disk
// space. The used slice uses the provided color; the free slice is drawn in a
// darker shade. Only the percentage sits in the ring; the used/free figures are
// written in the bottom-left corner.
func renderDiskPieChart(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := getElementWidth(element, 500)
	elemH := getElementHeight(element, 400)

	diskPieMutex.Lock()
	total := diskPie.Total
	used := diskPie.Used
	pct := diskPie.Pct
	diskPieMutex.Unlock()

	cx := element.X + elemW/2
	cy := element.Y + elemH/2
	radius := diskPieRadius(elemW, elemH)
	innerR := radius / 2

	drawPanel(renderer, element.X, element.Y, elemW, elemH, WithAlpha(ColorSurfacePanel, 235), accentColor)

	label := "Disk Usage"
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font != nil {
		lw, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextSecondary(), element.X+(elemW-int32(lw))/2, element.Y+10)
	}

	if total == 0 {
		if font != nil {
			renderText(renderer, config, font, "N/A", ColorTextTertiary(), cx-20, cy-10)
		}
		return
	}

	usedFrac := float64(used) / float64(total)
	if usedFrac > 1 {
		usedFrac = 1
	}

	// outer ring
	renderer.SetDrawColor(ColorSurfaceRow.R, ColorSurfaceRow.G, ColorSurfaceRow.B, 255)
	renderCircleSector(renderer, cx, cy, radius+4, radius, 0, 2*math.Pi)

	// Free sector (background)
	ringColor := ColorSurfaceRow
	renderCircleSectorColor(renderer, cx, cy, radius, innerR, 0, 2*math.Pi, ringColor)

	// Used sector
	usedAngle := 2 * math.Pi * usedFrac
	sectorColor := accentColor
	if usedFrac > 0.9 {
		sectorColor = ColorDanger
	} else if usedFrac > 0.75 {
		sectorColor = ColorWarning
	}
	renderCircleSectorColor(renderer, cx, cy, radius, innerR, -math.Pi/2, -math.Pi/2+usedAngle, sectorColor)

	// Center disk
	centerColor := ColorSurfacePanel
	fillCircle(renderer, cx, cy, innerR, centerColor)

	// Center text: the percentage alone. The used/total lines used to be
	// stacked under it inside the ring, where the big number and the small
	// lines overprinted each other.
	usedMB := float64(used) / (1024 * 1024)
	totalMB := float64(total) / (1024 * 1024)
	freeMB := float64(total-used) / (1024 * 1024)
	centerFont, _ := getCachedFont(config, "big")
	if centerFont == nil {
		centerFont = font
	}
	if centerFont != nil {
		pctStr := fmt.Sprintf("%d%%", pct)
		pw, ph, _ := centerFont.SizeUTF8(pctStr)
		renderText(renderer, config, centerFont, pctStr, ColorTextPrimary(), cx-int32(pw)/2, cy-int32(ph)/2)
	}

	// Usage figures are anchored bottom-left, clear of the ring.
	if font != nil {
		lineH := int32(font.Height())
		usageX, usageY := diskPieUsageLayout(element, elemH, lineH)
		usage := fmt.Sprintf("Used %.1f MB    Free %.1f MB", usedMB, freeMB)
		total := fmt.Sprintf("Total %.1f MB", totalMB)
		renderText(renderer, config, font, usage, ColorTextSecondary(), usageX, usageY)
		renderText(renderer, config, font, total, ColorTextTertiary(), usageX, usageY+lineH+6)
	}
}

// renderCircleSector fills the annular sector between innerR and outerR
// spanning from angleStart to angleEnd (radians, clockwise from top).
// Uses a dense line fan so the sector reads as a filled shape at render time.
func renderCircleSector(renderer *sdl.Renderer, cx, cy, outerR, innerR int32, angleStart, angleEnd float64) {
	renderCircleSectorColor(renderer, cx, cy, outerR, innerR, angleStart, angleEnd, accentColor)
}

// renderCircleSectorColor is renderCircleSector with an explicit color.
func renderCircleSectorColor(renderer *sdl.Renderer, cx, cy, outerR, innerR int32, angleStart, angleEnd float64, c sdl.Color) {
	if outerR <= 0 {
		return
	}
	if innerR < 0 {
		innerR = 0
	}
	if innerR >= outerR {
		innerR = outerR - 1
	}
	step := 0.04
	if angleEnd < angleStart {
		step = -step
	}
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	prevOX, prevOY, prevIX, prevIY := int32(0), int32(0), int32(0), int32(0)
	first := true
	for a := angleStart; (step > 0 && a <= angleEnd+0.01) || (step < 0 && a >= angleEnd-0.01); a += step {
		ox := int32(float64(cx) + math.Cos(a)*float64(outerR))
		oy := int32(float64(cy) + math.Sin(a)*float64(outerR))
		ix := int32(float64(cx) + math.Cos(a)*float64(innerR))
		iy := int32(float64(cy) + math.Sin(a)*float64(innerR))
		if !first {
			// Outer arc segment
			renderer.DrawLine(prevOX, prevOY, ox, oy)
			// Inner arc segment
			renderer.DrawLine(prevIX, prevIY, ix, iy)
			// Two radial lines forming a thin quad
			renderer.DrawLine(prevIX, prevIY, prevOX, prevOY)
			renderer.DrawLine(ix, iy, ox, oy)
			// Cross lines to fill the quad
			renderer.DrawLine(prevIX, prevIY, ox, oy)
			renderer.DrawLine(ix, iy, prevOX, prevOY)
		}
		prevOX, prevOY, prevIX, prevIY = ox, oy, ix, iy
		first = false
	}
}

// isqrt returns the integer square root (floor) of n using Newton's method.
func isqrt(n int32) int32 {
	if n <= 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}

// roundedCornerInset returns the horizontal inset for one rounded-corner row.
// dy is the distance from the nearest horizontal edge (0 = the edge row) and r
// is the corner radius, already clamped to half the box size. The math is
// symmetric for top and bottom corners so fills and outlines never grow feet,
// tabs, or detached pixels.
func roundedCornerInset(r, dy int32) int32 {
	if r <= 0 || dy < 0 || dy >= r {
		return 0
	}
	return r - isqrt(dy*(2*r-dy))
}

// fillRoundedRect draws a filled rounded rectangle using per-scanline
// rendering. The radius is clamped to half the width/height and every pixel
// stays inside the supplied rectangle.
func fillRoundedRect(renderer *sdl.Renderer, x, y, w, h, r int32, c sdl.Color) {
	if w <= 0 || h <= 0 || renderer == nil {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if r < 1 {
		setDrawColor(renderer, c)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		return
	}
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	setDrawColor(renderer, c)
	for dy := int32(0); dy < h; dy++ {
		var inset int32
		if dy < r {
			inset = roundedCornerInset(r, dy)
		} else if dy >= h-r {
			inset = roundedCornerInset(r, h-1-dy)
		}
		x0 := x + inset
		x1 := x + w - inset
		if x1 > x0 {
			renderer.FillRect(&sdl.Rect{X: x0, Y: y + dy, W: x1 - x0, H: 1})
		}
	}
}

// renderWithClip renders content within a clip rectangle to prevent overflow.
// Automatically resets renderer state after rendering.
func renderWithClip(renderer *sdl.Renderer, x, y, w, h int32, fn func(*sdl.Renderer)) {
	// Save current clip rect and whether clipping was active.
	oldClip := renderer.GetClipRect()
	hadClip := oldClip.W > 0 && oldClip.H > 0
	// Save draw color (5 return values: r,g,b,a,err).
	or, og, ob, oa, _ := renderer.GetDrawColor()
	oldColor := sdl.Color{R: or, G: og, B: ob, A: oa}

	// Apply clip.
	renderer.SetClipRect(&sdl.Rect{X: x, Y: y, W: w, H: h})

	// Render content.
	fn(renderer)

	// Restore state.
	if hadClip {
		renderer.SetClipRect(&oldClip)
	} else {
		renderer.SetClipRect(nil)
	}
	renderer.SetDrawColor(oldColor.R, oldColor.G, oldColor.B, oldColor.A)
}

// strokeRoundedRect draws a rounded-rect outline (ring) of the given thickness.
// It uses the same symmetric per-scanline inset math as fillRoundedRect so the
// outline is complete, inside the rectangle, and never grows feet or spikes.
func strokeRoundedRect(renderer *sdl.Renderer, x, y, w, h, r, thickness int32, c sdl.Color) {
	if w <= 0 || h <= 0 || thickness < 1 || renderer == nil {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	if thickness > r {
		thickness = r
	}
	innerR := r - thickness
	if innerR < 0 {
		innerR = 0
	}
	ih := h - 2*thickness
	setDrawColor(renderer, c)
	for dy := int32(0); dy < h; dy++ {
		var oInset int32
		if dy < r {
			oInset = roundedCornerInset(r, dy)
		} else if dy >= h-r {
			oInset = roundedCornerInset(r, h-1-dy)
		}
		left := x + oInset
		right := x + w - oInset
		if right <= left {
			continue
		}
		if dy >= thickness && dy < h-thickness && ih > 0 {
			// Punch the inner hole so only the ring remains.
			id := dy - thickness
			var iInset int32
			if id < innerR {
				iInset = roundedCornerInset(innerR, id)
			} else if id >= ih-innerR {
				iInset = roundedCornerInset(innerR, ih-1-id)
			}
			innerLeft := x + thickness + iInset
			innerRight := x + w - thickness - iInset
			if innerLeft > left {
				renderer.FillRect(&sdl.Rect{X: left, Y: y + dy, W: innerLeft - left, H: 1})
			}
			if right > innerRight {
				renderer.FillRect(&sdl.Rect{X: innerRight, Y: y + dy, W: right - innerRight, H: 1})
			}
		} else {
			// Top/bottom bands of the ring are solid.
			renderer.FillRect(&sdl.Rect{X: left, Y: y + dy, W: right - left, H: 1})
		}
	}
}

// gradientRoundedRect draws a smooth vertical gradient inside a rounded rect.
// The gradient is rendered per scanline so it interpolates top→bottom without
// a visible banding seam, and the rounded corners are preserved exactly.
func gradientRoundedRect(renderer *sdl.Renderer, x, y, w, h, r int32, top, bottom sdl.Color) {
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	if r < 1 {
		renderVerticalGradient(renderer, x, y, w, h, top, bottom)
		return
	}
	for dy := int32(0); dy < h; dy++ {
		var inset int32
		if dy < r {
			inset = r - int32(math.Sqrt(float64(2*r*dy-dy*dy)))
		} else if d := dy - (h - r); d > 0 {
			inset = r - int32(math.Sqrt(float64(2*r*d-d*d)))
		}
		x0 := x + inset
		x1 := x + w - inset
		if x1 <= x0 {
			continue
		}
		c := lerpColor(top, bottom, float32(dy)/float32(h-1))
		renderer.SetDrawColor(c.R, c.G, c.B, c.A)
		renderer.FillRect(&sdl.Rect{X: x0, Y: y + dy, W: x1 - x0, H: 1})
	}
}

// renderVerticalGradient draws a plain vertical gradient (no rounding).
func renderVerticalGradient(renderer *sdl.Renderer, x, y, w, h int32, top, bottom sdl.Color) {
	if h <= 1 {
		renderer.SetDrawColor(top.R, top.G, top.B, top.A)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		return
	}
	for dy := int32(0); dy < h; dy++ {
		c := lerpColor(top, bottom, float32(dy)/float32(h-1))
		renderer.SetDrawColor(c.R, c.G, c.B, c.A)
		renderer.FillRect(&sdl.Rect{X: x, Y: y + dy, W: w, H: 1})
	}
}

// drawRectOutline draws a 2px border around the given rect.
func drawRectOutline(renderer *sdl.Renderer, x, y, w, h int32, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: w, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x, Y: y + h - 1, W: w, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x, Y: y, W: 1, H: h})
	renderer.DrawRect(&sdl.Rect{X: x + w - 1, Y: y, W: 1, H: h})
}

// drawPanel renders a translucent panel with an accent border and premium shadow.
func drawPanel(renderer *sdl.Renderer, x, y, w, h int32, fill, border sdl.Color) {
	radius := RadiusMD
	// layered premium shadow (wider spread, softer falloff)
	fillRoundedRect(renderer, x+2, y+4, w, h, radius, ShadowFill(12))
	fillRoundedRect(renderer, x+4, y+8, w, h, radius, ShadowFill(20))
	fillRoundedRect(renderer, x+6, y+12, w, h, radius, ShadowFill(28))
	// fill
	fillRoundedRect(renderer, x, y, w, h, radius, fill)
	// subtle gradient-like top sheen (two-tone highlight)
	renderer.SetDrawColor(255, 255, 255, 16)
	renderer.FillRect(&sdl.Rect{X: x + 2, Y: y + 1, W: w - 4, H: 1})
	renderer.SetDrawColor(255, 255, 255, 6)
	renderer.FillRect(&sdl.Rect{X: x + 2, Y: y + 2, W: w - 4, H: 1})
	// border with subtle glow
	renderer.SetDrawColor(border.R, border.G, border.B, border.A)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: 1, H: h - 2})
	renderer.SetDrawColor(border.R, border.G, border.B, border.A/3)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + h - 2, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + w - 2, Y: y + 1, W: 1, H: h - 2})
	// corner accent dots (subtle polish)
	dotColor := sdl.Color{R: border.R, G: border.G, B: border.B, A: uint8(border.A / 2)}
	cSize := int32(2)
	renderer.SetDrawColor(dotColor.R, dotColor.G, dotColor.B, dotColor.A)
	renderer.FillRect(&sdl.Rect{X: x + 1, Y: y + 1, W: cSize, H: cSize})
	renderer.FillRect(&sdl.Rect{X: x + w - 1 - cSize, Y: y + 1, W: cSize, H: cSize})
	renderer.FillRect(&sdl.Rect{X: x + 1, Y: y + h - 1 - cSize, W: cSize, H: cSize})
	renderer.FillRect(&sdl.Rect{X: x + w - 1 - cSize, Y: y + h - 1 - cSize, W: cSize, H: cSize})
}

// drawCard renders a raised surface card: shadow, surface fill, subtle top
// gloss, and a hairline border. Canonical content container used across screens.
func drawCard(renderer *sdl.Renderer, x, y, w, h, r int32) {
	// layered soft shadow (growing offset, fading alpha)
	fillRoundedRect(renderer, x+1, y+2, w, h, r, ShadowFill(16))
	fillRoundedRect(renderer, x+3, y+4, w, h, r, ShadowFill(24))
	fillRoundedRect(renderer, x+5, y+6, w, h, r, ShadowFill(36))
	// card body
	fillRoundedRect(renderer, x, y, w, h, r, ColorSurfaceCard)
	// 1px top highlight line (glass edge)
	renderer.SetDrawColor(255, 255, 255, 12)
	renderer.FillRect(&sdl.Rect{X: x + 2, Y: y + 1, W: w - 4, H: 1})
	// hairline border
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, ColorBorderSubtle.A)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: 1, H: h - 2})
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + h - 2, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + w - 2, Y: y + 1, W: 1, H: h - 2})
}

// drawRow renders a single list row through the shared row component, so
// every list in the shell shares one selected/hovered treatment.
func drawRow(renderer *sdl.Renderer, x, y, w, h, r int32, selected, hovered bool) {
	UIListRow(renderer, sdl.Rect{X: x, Y: y, W: w, H: h}, r, selected, hovered)
}

// drawScrollbar draws a vertical scrollbar within a track when content
// overflows. thumbFrac is visible/total (0..1); scrollFrac is the scroll
// position as a fraction (0..1).
func drawScrollbar(renderer *sdl.Renderer, x, y, w, h int32, thumbFrac, scrollFrac float64) {
	if thumbFrac >= 1.0 || h <= 0 {
		return
	}
	if thumbFrac < 0.05 {
		thumbFrac = 0.05
	}
	if scrollFrac < 0 {
		scrollFrac = 0
	}
	if scrollFrac > 1 {
		scrollFrac = 1
	}
	renderer.SetDrawColor(255, 255, 255, 22)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	thumbH := int32(float64(h) * thumbFrac)
	if thumbH < 24 {
		thumbH = 24
	}
	maxOff := h - thumbH
	thumbY := y + int32(float64(maxOff)*scrollFrac)
	fillRoundedRect(renderer, x, thumbY, w, thumbH, w/2, sdl.Color{R: 255, G: 255, B: 255, A: 75})
}

// renderButtonElement draws a sleek glass-like button with soft shadows,
// smooth hover/active transitions, and clear focus indication.
//
// Premium button features:
//   - Layered shadows with soft falloff
//   - Glass-like top sheen
//   - Smooth hover glow (expanding ring)
//   - Press feedback (subtle scale + darkening)
//   - Clear 3px focus ring on selection
func buttonHitSize(elem Element, font *ttf.Font) (int32, int32) {
	textWidth, textHeight := int32(0), int32(0)
	if font != nil {
		w, h, _ := font.SizeUTF8(elem.Text)
		textWidth, textHeight = int32(w), int32(h)
	}
	return resolveButtonDimensions(elem, textWidth, textHeight)
}

func renderButtonElement(renderer *sdl.Renderer, config *Config, elem Element, elemIndex int, selected bool, hovered bool, hoverProgress float64, pressed bool) {
	if elem.Style == "tile" {
		renderAppTile(renderer, config, elem, elemIndex, selected, hovered, hoverProgress, pressed)
		return
	}
	// Every non-tile button renders through the shared flat dark primitive so
	// the whole app shares one button look (no silver gradients, gloss, or
	// layered shadows that produced feet/tabs in the legacy renderer).
	renderDarkButtonElement(renderer, config, elem, selected, hovered, hoverProgress, pressed)
}

// renderDarkButtonElement renders a refined dark button with premium polish.
// hoverProgress (0..1, eased by the main loop) drives a small scale lift so a
// pointer hover settles instead of snapping.
func renderDarkButtonElement(renderer *sdl.Renderer, config *Config, elem Element, selected, hovered bool, hoverProgress float64, pressed bool) {
	font, _ := getCachedFont(config, elem.Font)
	width, height := buttonHitSize(elem, font)
	x, y := elem.X, elem.Y
	r := RadiusMD

	// Hover scale: grow by up to 2px around the same centre. The element's
	// rect is untouched, so hit testing and the focus graph keep their
	// settled geometry.
	if hovered && !pressed && hoverProgress > 0 {
		grow := int32(math.Round(uiClamp01(hoverProgress) * 2))
		if grow > 0 {
			x -= grow
			y -= grow
			width += grow * 2
			height += grow * 2
		}
	}

	// Eased press dip: the button travels down over the press window instead
	// of jumping by a fixed offset, so a click reads as a physical push.
	pressOffset := int32(0)
	if pressed {
		age := float64(sdl.GetTicks64() - pressStartTime)
		pressOffset = int32(math.Round(float64(PressOffset) * uiPressProgress(config, age, float64(pressDuration))))
	}
	ox := x + pressOffset
	oy := y + pressOffset

	UIButton(renderer, config, sdl.Rect{X: ox, Y: oy, W: width, H: height}, elem.Text, UIButtonStyle{
		Variant:   UIButtonCustom,
		Fill:      resolveColor(config, elem.BgColor, ColorCard),
		HoverFill: ColorCardHover,
		FocusFill: ColorCardFocus,
		Font:      font,
		Radius:    r,
	}, UIButtonState{Focused: selected, Hovered: hovered, Pressed: pressed})
}

// renderAppTile draws an OS-style launcher icon: a clean opaque card with a
// consistent line icon, monochrome accent, and a label beneath. The focused tile
// uses a bright 3px focus ring (no scale) so selection is unmistakable.
func renderAppTile(renderer *sdl.Renderer, config *Config, elem Element, elemIndex int, selected, hovered bool, hoverProgress float64, pressed bool) {
	labelFont, _ := getCachedFont(config, elem.Font)
	if labelFont == nil {
		labelFont, _ = getCachedFont(config, "medium")
	}
	width, height := buttonHitSize(elem, labelFont)
	x, y := elem.X, elem.Y
	r := TileRadius

	// Home layout override: when Main scene uses layout "home", tiles are
	// positioned by the shared grid so render + mouse agree.
	if rect, ok := homeTileRect(elemIndex); ok {
		x, y, width, height = rect.X, rect.Y, rect.W, rect.H
	}

	// Eased press dip: the button travels down over the press window instead
	// of jumping by a fixed offset, so a click reads as a physical push.
	pressOffset := int32(0)
	if pressed {
		age := float64(sdl.GetTicks64() - pressStartTime)
		pressOffset = int32(math.Round(float64(PressOffset) * uiPressProgress(config, age, float64(pressDuration))))
	}
	ox := x + pressOffset
	oy := y + pressOffset
	cw := width
	ch := height

	focusCol := focusColorForAccent(accentColor)

	// Focus ring + subtle outer glow (3px crisp outline, no scale).
	if selected {
		fillRoundedRect(renderer, ox-FocusGlow, oy-FocusGlow, cw+2*FocusGlow, ch+2*FocusGlow, r+FocusGlow, WithAlpha(focusCol, 22))
		strokeRoundedRect(renderer, ox-FocusRing, oy-FocusRing, cw+2*FocusRing, ch+2*FocusRing, r+FocusRing, FocusRing, focusCol)
	} else if hoverProgress > 0.01 {
		a := uint8(80 * hoverProgress)
		strokeRoundedRect(renderer, ox-HoverRing, oy-HoverRing, cw+2*HoverRing, ch+2*HoverRing, r+HoverRing, HoverRing, WithAlpha(focusCol, a))
	}

	// Tile body: use the configured tile color when provided, otherwise fall
	// back to the standard home card surface.
	base := resolveColor(config, elem.BgColor, HomeCardColor())
	if selected {
		base = HomeCardFocusColor()
	}
	// Clip tile body to prevent overflow
	renderWithClip(renderer, ox, oy, cw, ch, func(ren *sdl.Renderer) {
		fillRoundedRect(ren, ox, oy, cw, ch, r, base)
		// Hairline border using HomeBorderColor for consistency.
		borderCol := HomeBorderColor()
		if selected {
			borderCol = WithAlpha(focusCol, 200)
		} else if hovered {
			borderCol = WithAlpha(focusCol, 120)
		}
		strokeRoundedRect(ren, ox, oy, cw, ch, r, 1, borderCol)
	})

	// Icon and label use the configured tile text color so they read clearly
	// against both dark and colored tile backgrounds.
	iconKind := tileIconKind(elem)
	iconCol := resolveColor(config, elem.Color, ColorTextPrimary())
	if selected {
		iconCol = focusCol
	}
	iconBg := base
	iconSize := cw / 3
	if iconSize > IconSizeMax {
		iconSize = IconSizeMax
	}
	if iconSize < IconSizeMin {
		iconSize = IconSizeMin
	}
	drawTileIcon(renderer, ox+cw/2, oy+int32(float64(ch)*0.36), iconSize, iconKind, iconCol, iconBg)

	// Label beneath the icon, single line, kept off the edges.
	if labelFont != nil && elem.Text != "" {
		lw, lh, _ := labelFont.SizeUTF8(elem.Text)
		maxLW := cw - SpaceXL
		if lw > int(maxLW) {
			lw = int(maxLW)
		}
		txt := iconCol
		lx := ox + (cw-int32(lw))/2
		ly := oy + ch - int32(lh) - LabelBottomPad
		renderText(renderer, config, labelFont, elem.Text, txt, lx, ly)
	}
}

// renderLabelShadowed draws a label with a subtle drop shadow for depth.
func renderLabelShadowed(renderer *sdl.Renderer, config *Config, elem Element) {
	font, _ := getCachedFont(config, elem.Font)
	if font == nil {
		return
	}
	color := resolveColor(config, elem.Color, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	renderText(renderer, config, font, elem.Text, sdl.Color{R: 0, G: 0, B: 0, A: 140}, elem.X+2, elem.Y+2)
	renderText(renderer, config, font, elem.Text, color, elem.X, elem.Y)
}

// isMissingDLL reports whether err indicates the process failed to start
// because a required DLL could not be found. On Windows this is
// STATUS_DLL_NOT_FOUND (exit code 0xc0000135).
func isMissingDLL(err error) bool {
	if err == nil {
		return false
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code == 0xC0000135 || code == -1073741515 {
			return true
		}
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "0xc0000135") || strings.Contains(s, "c0000135") || strings.Contains(s, "status_dll_not_found")
}

// --- Tool path resolution ---
func getToolPath(tool string, config *Config) string {
	exeName := tool
	if IsWindows() {
		exeName += ".exe"
	}
	// Resolve to an absolute path so the OS reliably locates the tool's
	// sibling DLLs (e.g. ffplay.exe's av*.dll) regardless of the process CWD.
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	// Check tools_path variable (config-driven). It may name a directory that
	// holds the tools or the tool binary itself; never return a directory as
	// the executable path, or exec would fail with "is a directory".
	if tp := strings.TrimSpace(config.Variables.ToolsPath); tp != "" {
		fullPath := filepath.Join(tp, exeName)
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			return resolve(fullPath)
		}
		if info, err := os.Stat(tp); err == nil && !info.IsDir() {
			return resolve(tp)
		}
	}
	// Check the platform tools dir ("required" next to the binary) — the same
	// location EnsureHelperTool installs into.
	commonPath := filepath.Join(P().ToolsDir(), exeName)
	if _, err := os.Stat(commonPath); err == nil {
		return resolve(commonPath)
	}
	// Fallback to system PATH: resolve the actual binary so callers can stat
	// it (a bare "ffplay.exe" only stats against the working directory).
	if p, err := exec.LookPath(exeName); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return exeName
}

// helperExecutable resolves a built-in helper tool to a concrete executable.
// The name is first matched against a fixed allowlist and the path is built
// from the tools directory / a PATH lookup of that constant name, so a
// user-supplied command string can never select an arbitrary program.
func helperExecutable(name string, config *Config) (string, error) {
	switch name {
	case "yt-dlp", "ffplay", "ffmpeg", "ffprobe":
	default:
		return "", fmt.Errorf("unsupported helper tool %q", name)
	}
	exe := name
	if IsWindows() {
		exe += ".exe"
	}
	var candidates []string
	if config != nil {
		if tp, err := cleanUserPath(config.Variables.ToolsPath); err == nil {
			candidates = append(candidates, filepath.Join(tp, exe))
		}
	}
	candidates = append(candidates, filepath.Join(P().ToolsDir(), exe))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s is not installed", name)
}

// --- Quote path if it contains spaces ---
func quotePath(path string) string {
	if strings.ContainsAny(path, " ") {
		return `"` + path + `"`
	}
	return path
}

// decryptCustomString attempts to decrypt a custom config value if it is encrypted.
// If the value is missing, not a string, not encrypted, or cannot be decrypted, it
// returns "" and logs the failure. Callers that need to distinguish "not encrypted"
// from "decryption failed" should use decryptCustomStringResult instead.
func decryptCustomString(config *Config, key string) string {
	if config == nil {
		return ""
	}
	v, ok := config.Variables.Custom[key].(string)
	if !ok || v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "ENC:") {
		return v
	}
	decrypted, err := DecryptAPIToken(v, key)
	if err != nil {
		log.Printf("[SECURITY] Failed to decrypt %s: %v", key, err)
		return ""
	}
	return decrypted
}

// decryptCustomStringResult is like decryptCustomString, but now that
// DecryptAPIToken already distinguishes "not encrypted" from decryption
// failure, this helper is kept only as a thin backward-compatible wrapper.
func decryptCustomStringResult(config *Config, key string) (string, error) {
	if config == nil {
		return "", fmt.Errorf("config is nil")
	}
	v, ok := config.Variables.Custom[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: missing or non-string config value", key)
	}
	if !strings.HasPrefix(v, "ENC:") {
		return v, nil
	}
	return DecryptAPIToken(v, key)
}

// sanitizeURL removes sensitive query parameters from URLs for safe logging.
// This prevents API keys, tokens, and other secrets from appearing in logs.
// The parameter is deliberately not called "url" so the net/url package stays
// reachable inside the function.
func sanitizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Only process http/https URLs
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}

	// Parse the URL and remove sensitive query params
	if u, err := url.Parse(rawURL); err == nil {
		// Query parameters that might contain sensitive data
		sensitiveParams := map[string]bool{
			"key": true, "api_key": true, "apikey": true, "token": true,
			"access_token": true, "auth": true, "password": true, "pass": true,
			"secret": true, "signature": true, "sig": true,
		}

		q := u.Query()
		foundSensitive := false
		for param := range q {
			if sensitiveParams[strings.ToLower(param)] {
				q.Del(param)
				foundSensitive = true
			}
		}

		if foundSensitive {
			u.RawQuery = q.Encode()
			return u.String()
		}
	}

	return rawURL
}

// sanitizeLogURL is a wrapper that logs a sanitized URL or "<hidden>" if sanitization removed params.
func sanitizeLogURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	sanitized := sanitizeURL(rawURL)
	// If the URL changed, indicate parameters were removed
	if sanitized != rawURL {
		// Return just the base URL without query params for safety
		if u, err := url.Parse(rawURL); err == nil {
			return u.Scheme + "://" + u.Host + u.Path
		}
	}
	return sanitized
}

// ytDlpExtraArgs returns extra yt-dlp arguments based on config, such as
// --cookies and --extractor-args for the YouTube extractor.
func ytDlpExtraArgs(config *Config) string {
	var parts []string
	if config == nil {
		return ""
	}
	// Decrypt API key if encrypted; use the result form so a decryption failure
	// is distinguishable from "not encrypted" and gets logged specifically.
	if apiKey, err := decryptCustomStringResult(config, "google_api_key"); err == nil {
		if apiKey != "" {
			parts = append(parts, "--extractor-args", "youtube:api_key="+apiKey)
		}
	} else if strings.Contains(err.Error(), "decryption failed") || strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "not encrypted") && !strings.Contains(err.Error(), "missing") {
		// Only log when the value was encrypted but could not be decrypted.
		// "not encrypted" and "missing" are not actionable failures here.
		log.Printf("[SECURITY] yt-dlp API key unavailable: %v", err)
	}
	// Cookies file path - not encrypted (it's a file path, not a secret).
	// Normalize it and require a real regular file; helper args are resolved
	// from config, so never hand a malformed path to a subprocess.
	if v, ok := config.Variables.Custom["youtube_cookies_file"].(string); ok && v != "" {
		if cookies, err := existingRegularFile(v); err == nil {
			parts = append(parts, "--cookies", cookies)
		}
	}
	return strings.Join(parts, " ")
}

// ytSearchCmd returns a yt-dlp search command for the current config, including
// any extra args from ytDlpExtraArgs. It exists so the two YouTube search
// handlers share one command builder instead of duplicating the base yt-dlp
// invocation.
func ytSearchCmd(config *Config) string {
	base := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 20 "ytsearch20:$search_query"`
	if extra := ytDlpExtraArgs(config); extra != "" {
		return base + " " + extra
	}
	return base
}

func ytDlpExtraArgsSlice(config *Config) []string {
	var parts []string
	if config == nil {
		return nil
	}
	// Decrypt API key if encrypted; use the result form so a decryption failure
	// is distinguishable from "not encrypted" and gets logged specifically.
	if apiKey, err := decryptCustomStringResult(config, "google_api_key"); err == nil {
		if apiKey != "" {
			parts = append(parts, "--extractor-args", "youtube:api_key="+apiKey)
		}
	} else if strings.Contains(err.Error(), "decryption failed") || strings.Contains(err.Error(), "empty") || (!strings.Contains(err.Error(), "not encrypted") && !strings.Contains(err.Error(), "missing")) {
		log.Printf("[SECURITY] yt-dlp API key unavailable: %v", err)
	}
	// Cookies file path - not encrypted (it's a file path, not a secret).
	if v, ok := config.Variables.Custom["youtube_cookies_file"].(string); ok && v != "" {
		if cookies, err := existingRegularFile(v); err == nil {
			parts = append(parts, "--cookies", cookies)
		}
	}
	return parts
}

// validateYtDlpArgs rejects extra yt-dlp arguments that could break out of the
// shell command line they are interpolated into (playSmartURL passes them to
// `sh -c` / `cmd /c` unquoted). A malicious cookies_file or api_key value must
// never reach the shell: every argument has to be a plain shell-safe token.
func validateYtDlpArgs(args []string) error {
	if len(args) > 32 {
		return fmt.Errorf("too many extra yt-dlp arguments (%d)", len(args))
	}
	for i, a := range args {
		if a == "" {
			return fmt.Errorf("empty extra yt-dlp argument #%d", i)
		}
		if len(a) > 2048 {
			return fmt.Errorf("extra yt-dlp argument #%d is too long (%d bytes)", i, len(a))
		}
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("control character in extra yt-dlp argument #%d", i)
			}
		}
		if strings.ContainsAny(a, ";|&$`><(){}[]!*?~\"'%^#\n\r\t ") {
			// Never echo the argument: it can hold an API key or cookie path.
			return fmt.Errorf("shell metacharacter in extra yt-dlp argument #%d", i)
		}
	}
	return nil
}

// ytDlpExecFlags are yt-dlp options that launch another program or load extra
// configuration instead of just fetching metadata. The YouTube search command
// comes from the scene config, so editing that config must not escalate into
// arbitrary command execution.
var ytDlpExecFlags = []string{
	"--exec",
	"--exec-before-download",
	"--downloader",
	"--external-downloader",
	"--postprocessor-args",
	"--ppa",
	"--update",
	"--update-to",
	"--config-location",
}

// rejectYtDlpExecFlags refuses execution-capable options in a config-supplied
// yt-dlp command line. Both "--flag value" and "--flag=value" are caught
// because the flag name is compared before the first '='.
func rejectYtDlpExecFlags(args []string) error {
	for _, a := range args {
		flag := a
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag = flag[:i]
		}
		for _, bad := range ytDlpExecFlags {
			if flag == bad {
				return fmt.Errorf("yt-dlp option %s is not allowed in a search command", bad)
			}
		}
	}
	return nil
}

// --- Thumbnail loading with cache ---
var httpClient = &http.Client{
	Timeout: 8 * time.Second,
}

// requestDeduper prevents concurrent duplicate HTTP requests for the same URL.
// When multiple goroutines request the same URL simultaneously, only one request
// is made and all goroutines receive the same result.
var requestDeduper = struct {
	mu       sync.Mutex
	requests map[string]chan struct {
		body []byte
		err  error
	}
}{
	requests: make(map[string]chan struct {
		body []byte
		err  error
	}),
}

// DedupFetch performs an HTTP GET, deduplicating concurrent requests for the same URL.
// If another goroutine is already fetching the same URL, waits for and returns that result.
func DedupFetch(url string, timeout time.Duration) ([]byte, error) {
	requestDeduper.mu.Lock()
	if ch, exists := requestDeduper.requests[url]; exists {
		requestDeduper.mu.Unlock()
		// Wait for existing request
		result := <-ch
		return result.body, result.err
	}

	// Create channel for this request
	ch := make(chan struct {
		body []byte
		err  error
	}, 1)
	requestDeduper.requests[url] = ch
	requestDeduper.mu.Unlock()

	// Perform the request (non-blocking for other waiters)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		ch <- struct {
			body []byte
			err  error
		}{nil, err}
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		ch <- struct {
			body []byte
			err  error
		}{nil, err}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ch <- struct {
			body []byte
			err  error
		}{nil, err}
		return nil, err
	}

	ch <- struct {
		body []byte
		err  error
	}{data, nil}
	return data, nil
}

// RemoveRequest removes a URL from the deduplication map after its result is consumed.
// Call this when you're done with a deduplicated request to prevent memory leaks.
func RemoveRequest(url string) {
	requestDeduper.mu.Lock()
	delete(requestDeduper.requests, url)
	requestDeduper.mu.Unlock()
}

func loadThumbnail(renderer *sdl.Renderer, url string) *sdl.Texture {
	return loadThumbnailFromURLs(renderer, []string{url})
}

func loadThumbnailFromURLs(renderer *sdl.Renderer, urls []string) *sdl.Texture {
	// Warm the disk cache for alternate URLs in the background so scrolling
	// through a list never blocks on a cold network fetch.
	for _, rawURL := range urls[1:] {
		PrefetchThumbnail(rawURL)
	}
	for _, rawURL := range urls {
		if rawURL == "" {
			continue
		}
		url := normalizeThumbnailURL(rawURL)
		if tex := getThumbnailTextures().Get(url); tex != nil {
			return tex
		}

		// Bytes come from memory → disk → network (with retry), so a warm
		// cache never touches the network and a cold one survives restarts.
		data, err := fetchThumbnailBytes(url)
		if err != nil {
			log.Printf("[DEBUG] loadThumbnail: fetch failed for %s: %v", url, err)
			continue
		}
		if len(data) == 0 {
			log.Printf("[DEBUG] loadThumbnail: empty body for %s", url)
			continue
		}
		rwops, err := sdl.RWFromMem(data)
		if err != nil {
			log.Printf("[DEBUG] loadThumbnail: RWFromMem failed for %s: %v", url, err)
			continue
		}
		tex, err := img.LoadTextureRW(renderer, rwops, false)
		rwops.Close()
		if err != nil {
			log.Printf("[DEBUG] loadThumbnail: LoadTextureRW failed for %s: %v", url, err)
			continue
		}
		var tw, th int32
		if _, _, qw, qh, err := tex.Query(); err == nil {
			tw, th = qw, qh
		}
		getThumbnailTextures().Put(url, tex, tw, th)
		log.Printf("[DEBUG] loadThumbnail: loaded %s", url)
		return tex
	}
	return nil
}

// --- Search execution with correct JSON parsing ---
// vars is a snapshot of Custom taken on the main thread (never the live map),
// so this goroutine never reads the shared map concurrently with the renderer.

// splitShellArgs tokenizes a command string into arguments, keeping text
// inside double quotes as a single argument and stripping the quotes. This
// mirrors how a shell would split args while avoiding shell interpretation
// of special characters (quotes, <, >, [, ]).
func splitShellArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case (r == ' ' || r == '\t') && !inQuotes:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func executeYouTubeSearch(config *Config, command string, resultVar string, vars map[string]interface{}) {
	// debug removed

	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Loading videos..."
	defer func() { config.Variables.LoadingSpinner = false }()

	// Substitute variables (e.g., $search_query)
	cmdWithVars := substituteVars(command, vars)

	// If the query part is empty (e.g., "ytsearch20:"), replace with "popular"
	re := regexp.MustCompile(`"ytsearch20:\s*"`)
	cmdWithVars = re.ReplaceAllString(cmdWithVars, `"ytsearch20:popular"`)

	// Apply a playback-resolution cap if the user set one in Settings > General.
	if res, ok := vars["PlaybackResolution"].(string); ok && res != "" && res != "best" {
		cmdWithVars += fmt.Sprintf(" -f \"best[height<=%s]\"", res)
	}

	// Append extra yt-dlp args from config (cookies, api_key, etc.).
	extraArgs := ytDlpExtraArgs(config)
	if extraArgs != "" {
		cmdWithVars += " " + extraArgs
	}

	// Tokenize the command into arguments, honouring double quotes so that
	// URLs and filter expressions (which may contain characters like <, >,
	// [ ]) are passed verbatim to yt-dlp instead of being re-parsed by a shell.
	tokens := splitShellArgs(cmdWithVars)
	if len(tokens) == 0 {
		log.Printf("[ERROR] Empty command after substitution")
		publishCustom("search_error", "Empty search command")
		publishCustom(resultVar, []VideoInfo{})
		return
	}
	tool := strings.ToLower(strings.TrimSuffix(filepath.Base(tokens[0]), ".exe"))
	args := tokens[1:]
	// A search command may only invoke the bundled yt-dlp. The executable path
	// is then derived from that fixed name, never from the command text.
	if tool != "yt-dlp" {
		log.Printf("[SECURITY] executeYouTubeSearch: refusing tool %q", tool)
		publishCustom("search_error", "Unsupported search command")
		publishCustom(resultVar, []VideoInfo{})
		return
	}
	if err := rejectYtDlpExecFlags(args); err != nil {
		log.Printf("[SECURITY] executeYouTubeSearch: %v", err)
		publishCustom("search_error", "Unsupported search command")
		publishCustom(resultVar, []VideoInfo{})
		return
	}
	// Resolve before searching. If the tool is missing, start the repair in
	// the background instead of blocking the render loop on a download; the
	// startup tool-health check may already be installing it.
	fullToolPath, toolErr := helperExecutable("yt-dlp", config)
	if toolErr != nil {
		go func() {
			if err := EnsureHelperTool("yt-dlp", config); err != nil {
				log.Printf("[ERROR] yt-dlp install failed: %v", err)
				showToast(toolUnavailableMessage("yt-dlp", err), ToastError())
				return
			}
			showToast("yt-dlp installed — restart JukaHub to use it", ToastSuccess())
			refreshToolHealth()
		}()
		log.Printf("[WARN] yt-dlp is not installed yet; install started")
		publishCustom("search_error", "yt-dlp is being installed — try the search again in a moment.")
		publishCustom(resultVar, []VideoInfo{})
		showToast("Installing yt-dlp…", ToastInfo())
		return
	}

	// Check bbolt cache for YouTube search results before hitting yt-dlp.
	cacheKey := "ytsearch:" + normalizeCacheKey(cmdWithVars)
	if db, err := cacheOpen("jukahub.cache"); err == nil {
		if cached, cErr := cacheGet(db, cacheKey); cErr == nil && cached != nil {
			var videos []VideoInfo
			if json.Unmarshal(cached, &videos) == nil {
				db.Close()
				publishCustom(resultVar, videos)
				publishCustom("search_error", nil)
				focusedResultIndex = -1
				prefetchFirstResult(config, videos)
				return
			}
		}
		db.Close()
	}

	// Run the tool directly (no shell) so special characters are not
	// interpreted by cmd.exe / sh (e.g. "<" as input redirection).
	cmd := exec.Command(fullToolPath, args...)
	cmd.Env = helperToolEnv(fullToolPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Printf("[ERROR] yt-dlp execution error: %v", err)
		log.Printf("[ERROR] stderr: %s", stderr.String())
		reason := ytDlpFailureReason(stderr.String(), err, fullToolPath)
		publishCustom("search_error", reason)
		publishCustom(resultVar, []VideoInfo{})
		switch {
		case !IsOnline():
			showToast("Search failed — offline, connect WiFi", ToastWarn())
		case helperUnpackFailure(stderr.String()):
			showToast(reason, ToastError())
		default:
			showToast("Search failed — "+reason, ToastError())
		}
		return
	}

	if stdout.Len() == 0 {
		log.Printf("[WARN] yt-dlp returned empty stdout")
		publishCustom("search_error", "yt-dlp returned no data. Check your query or network.")
		publishCustom(resultVar, []VideoInfo{})
		showToast("No results found.", ToastWarn())
		return
	}

	output := stdout.String()

	var videos []VideoInfo
	var parseErr error

	// Try parsing as single JSON (--dump-single-json)
	if strings.HasPrefix(strings.TrimSpace(output), "{") {
		var playlist struct {
			Entries []VideoInfo `json:"entries"`
		}
		if err := json.Unmarshal([]byte(output), &playlist); err == nil {
			videos = playlist.Entries
		} else {
			parseErr = err
			log.Printf("[ERROR] Failed to parse single JSON: %v", err)
		}
	}

	// Only fallback to line-delimited if single JSON parsing failed
	if len(videos) == 0 && parseErr != nil {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var vi VideoInfo
			if err := json.Unmarshal([]byte(line), &vi); err != nil {
				log.Printf("[ERROR] Failed to parse line as JSON: %v\nLine: %s", err, line)
				continue
			}
			videos = append(videos, vi)
		}
	}

	seen := make(map[string]bool)
	unique := make([]VideoInfo, 0, len(videos))
	for _, v := range videos {
		key := v.ID
		if key == "" {
			key = v.Title
		}
		if !seen[key] {
			seen[key] = true
			unique = append(unique, v)
		}
	}
	videos = unique
	log.Printf("[DEBUG] Parsed %d unique videos (from %d total entries)", len(videos), len(unique))

	if len(videos) == 0 {
		log.Printf("[ERROR] No videos could be parsed from yt-dlp output")
		publishCustom("search_error", "No videos found. Try a different query.")
	}

	// Cache successful YouTube search results for 1 hour.
	if len(videos) > 0 {
		if data, err := json.Marshal(videos); err == nil {
			if db, cErr := cacheOpen("jukahub.cache"); cErr == nil {
				cacheSet(db, cacheKey, data, time.Hour)
				db.Close()
			}
		}
	}

	publishCustom(resultVar, videos)
	publishCustom("search_error", nil)
	focusedResultIndex = -1
	prefetchFirstResult(config, videos)
}

// --- Fetch trending videos (2 columns x 6 rows = 12 videos) ---
func fetchTrendingVideos(config *Config, resultVar string, vars map[string]interface{}) {
	// debug removed
	// Use the same reliable search-based approach as youtube_search,
	// with "trending" as the query. This works unauthenticated on all devices.
	cmd := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 12 "ytsearch12:trending"`
	executeYouTubeSearch(config, cmd, resultVar, vars)
}

// gridColsRows returns the configured search-results grid dimensions, falling
// back to the default 2x6 layout when not specified in the config.
func gridColsRows(e Element) (int32, int32) {
	cols := e.Columns
	if cols <= 0 {
		cols = 2
	}
	rows := e.Rows
	if rows <= 0 {
		rows = 6
	}
	return cols, rows
}

func formatViewCount(v int64) string {
	if v < 1000 {
		return fmt.Sprintf("%d views", v)
	}
	if v < 1_000_000 {
		return fmt.Sprintf("%.1fK views", float64(v)/1000)
	}
	if v < 1_000_000_000 {
		return fmt.Sprintf("%.1fM views", float64(v)/1_000_000)
	}
	return fmt.Sprintf("%.1fB views", float64(v)/1_000_000_000)
}

func formatUploadDate(s string) string {
	if len(s) != 8 {
		return ""
	}
	year := s[0:4]
	month := s[4:6]
	day := s[6:8]
	return fmt.Sprintf("%s-%s-%s", year, month, day)
}

// --- Rendering for search results (2 columns Ã— 6 rows) ---
func renderSearchResults(renderer *sdl.Renderer, config *Config, element Element) {
	// Error state: user-facing message, never a permanent black box.
	if errMsg, ok := config.Variables.Custom["search_error"]; ok && errMsg != nil {
		elemWidth := getElementWidth(element, 1232)
		elemHeight := getElementHeight(element, 456)
		font, _ := getCachedFont(config, "small")
		if font != nil {
			// Wrap instead of blitting one long line: error text used to run
			// off both edges of the card and hide its own beginning.
			lines := wrapTextLines(font, fmt.Sprintf("Error: %v", errMsg), elemWidth-48, 3)
			lineH := int32(font.Height()) + 4
			y := element.Y + (elemHeight-int32(len(lines))*lineH)/2 - 8
			for i, line := range lines {
				w, _, _ := font.SizeUTF8(line)
				renderText(renderer, config, font, line, sdl.Color{R: 255, G: 107, B: 122, A: 255},
					element.X+(elemWidth-int32(w))/2, y+int32(i)*lineH)
			}
			hint := "See Logs for details."
			w, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint, ColorTextMuted(),
				element.X+(elemWidth-int32(w))/2, y+int32(len(lines))*lineH+8)
		}
		return
	}

	videos, ok := config.Variables.Custom[element.Variable].([]VideoInfo)
	if !ok || len(videos) == 0 {
		// Loading or empty state, centered in the content rect.
		elemWidth := getElementWidth(element, 1232)
		elemHeight := getElementHeight(element, 456)
		font, _ := getCachedFont(config, element.Font)
		if font == nil {
			font, _ = getCachedFont(config, "small")
		}
		if font != nil {
			if config.Variables.LoadingSpinner {
				renderSpinner(renderer, element.X+elemWidth/2-15, element.Y+elemHeight/2-40, 24, sdl.Color{R: 85, G: 216, B: 255, A: 255})
				w, _, _ := font.SizeUTF8(config.Variables.SpinnerText)
				renderText(renderer, config, font, config.Variables.SpinnerText, sdl.Color{R: 156, G: 169, B: 189, A: 255},
					element.X+(elemWidth-int32(w))/2, element.Y+elemHeight/2+4)
			} else {
				w, _, _ := font.SizeUTF8("No results. Try searching.")
				renderText(renderer, config, font, "No results. Try searching.", ColorTextMuted(),
					element.X+(elemWidth-int32(w))/2, element.Y+elemHeight/2-int32(font.Height())/2)
			}
		}
		return
	}

	cols, _ := gridColsRows(element)
	elemWidth := getElementWidth(element, 1232)
	elemHeight := getElementHeight(element, 456)

	if cols <= 1 {
		renderSearchResultsList(renderer, config, element, videos, elemWidth, elemHeight)
		return
	}
	renderSearchResultsGrid(renderer, config, element, videos, cols, elemWidth, elemHeight)
}

// renderSearchResultsList renders results as one readable vertical list:
// ~100px rows with 16:9 thumbnails, one measured title line and one measured
// metadata line (both ellipsized), and the duration badge inside the
// thumbnail. The viewport is SDL-clipped so rows can never escape the content
// area, and the focused row keeps its own bright cyan ring.
func renderSearchResultsList(renderer *sdl.Renderer, config *Config, element Element, videos []VideoInfo, elemWidth, elemHeight int32) {
	rowH := int32(100)
	gap := int32(14)

	maxVisibleRows := (elemHeight + gap) / (rowH + gap)
	if maxVisibleRows < 1 {
		maxVisibleRows = 1
	}
	targetScrollY := int32(0)
	if focusedResultIndex >= 0 {
		focusedRow := int32(focusedResultIndex)
		if focusedRow >= maxVisibleRows {
			targetScrollY = (focusedRow - maxVisibleRows + 1) * (rowH + gap)
		}
	}
	if targetScrollY < 0 {
		targetScrollY = 0
	}
	totalRows := int32(len(videos))
	maxScroll := int32(0)
	if totalRows > maxVisibleRows {
		maxScroll = (totalRows - maxVisibleRows) * (rowH + gap)
	}
	if targetScrollY > maxScroll {
		targetScrollY = maxScroll
	}
	scrollY = int32(float64(scrollY) + (float64(targetScrollY-scrollY) * 0.3))
	if abs(int(scrollY-targetScrollY)) < 1 {
		scrollY = targetScrollY
	}

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}
	if font == nil {
		return
	}

	// Lookahead prefetch: warm the disk cache for the next few rows so
	// scrolling never blocks on a cold network fetch (TSP WiFi is slow).
	for i, vid := range videos {
		y := element.Y + int32(i)*(rowH+gap) - scrollY
		if y+rowH < element.Y || y > element.Y+elemHeight {
			if i < focusedResultIndex+3 {
				PrefetchThumbnail(vid.Thumbnail)
			}
			continue
		}
		PrefetchThumbnail(vid.Thumbnail)
	}

	renderWithClip(renderer, element.X, element.Y, elemWidth, elemHeight, func(r *sdl.Renderer) {
		for i, vid := range videos {
			y := element.Y + int32(i)*(rowH+gap) - scrollY
			if y+rowH < element.Y || y > element.Y+elemHeight {
				continue
			}

			cardX, cardY := element.X, y
			cardW, cardH := elemWidth, rowH
			focused := i == focusedResultIndex

			// Flat card matching the home design language: resting fill + thin
			// border, focused fill + 3px cyan ring inside the rect.
			fill := ColorCard
			if focused {
				fill = ColorCardFocus
			}
			fillRoundedRect(r, cardX, cardY, cardW, cardH, 12, fill)
			if focused {
				strokeRoundedRect(r, cardX, cardY, cardW, cardH, 12, 3, ColorAccent)
			} else {
				strokeRoundedRect(r, cardX, cardY, cardW, cardH, 12, 1, ColorBorder)
			}

			// recently-played accent bar
			if isRecentlyPlayed(vid.GetURL()) {
				fillRoundedRect(r, cardX+2, cardY+8, 3, cardH-16, 2, ColorDanger)
			}

			// 16:9 thumbnail, vertically centered on the left.
			thumbW, thumbH := int32(168), int32(95)
			thumbX, thumbY := cardX+10, cardY+(cardH-thumbH)/2
			drawThumbnail(r, config, vid, thumbX, thumbY, thumbW, thumbH)
			drawDurationBadge(r, config, font, vid.Duration, thumbX+thumbW-56, thumbY+thumbH-22, 48, 16)

			// Measured text block, vertically centered against the thumbnail.
			textX := thumbX + thumbW + 16
			textW := cardX + cardW - 14 - textX
			if textW < 24 {
				textW = 24
			}
			th := int32(titleFont.Height())
			mh := int32(font.Height())
			stackH := th + 6 + mh
			textTop := cardY + (cardH-stackH)/2

			// Preloaded cards get a lightning bolt and a yellow title: yt-dlp
			// already resolved the stream, so Play starts without a wait.
			titleX, titleW := textX, textW
			titleColor := ColorTextPrimary()
			if videoStreamPreloaded(vid.GetURL()) {
				const boltSize = 16
				titleColor = ColorWarning
				drawPreloadBolt(r, textX, textTop+(th-boltSize)/2, boltSize, ColorWarning)
				titleX = textX + boltSize + 6
				titleW = textW - boltSize - 6
				if titleW < 24 {
					titleW = 24
				}
			}
			title := ellipsize(titleFont, vid.Title, titleW)
			meta := ellipsize(font, resultMetaLine(vid), textW)
			drawText(r, titleFont, title, titleX, textTop, titleColor, textAlignLeft)
			drawText(r, font, meta, textX, textTop+th+6, ColorTextMuted(), textAlignLeft)
		}
	})

	// scroll indicator bar
	if maxScroll > 0 {
		barH := int32(40)
		barX := element.X + elemWidth - 8
		barY := element.Y + int32(float64(scrollY)/float64(maxScroll)*float64(elemHeight-barH))
		fillRoundedRect(renderer, barX, barY, 6, barH, 3, sdl.Color{R: 255, G: 255, B: 255, A: 35})
	}
}

// renderSearchResultsGrid renders the legacy multi-column grid. It shares the
// repaired primitives: measured ellipsis (never byte truncation), the duration
// badge inside the thumbnail, and an SDL-clipped viewport.
func renderSearchResultsGrid(renderer *sdl.Renderer, config *Config, element Element, videos []VideoInfo, cols, elemWidth, elemHeight int32) {
	rows := gridRowsFor(element)
	cellWidth := (elemWidth - 30) / cols
	cellHeight := (elemHeight - 30) / rows
	thumbWidth := int32(120)
	thumbHeight := int32(90)

	maxVisibleRows := (elemHeight - 30) / cellHeight
	if maxVisibleRows < 1 {
		maxVisibleRows = 1
	}
	targetScrollY := int32(0)
	if focusedResultIndex >= 0 {
		focusedRow := int32(focusedResultIndex) / cols
		if focusedRow >= maxVisibleRows {
			targetScrollY = (focusedRow - maxVisibleRows + 1) * (cellHeight + 10)
		}
	}
	if targetScrollY < 0 {
		targetScrollY = 0
	}
	maxScroll := int32(0)
	totalRows := (int32(len(videos)) + cols - 1) / cols
	if totalRows > maxVisibleRows {
		maxScroll = (totalRows - maxVisibleRows) * (cellHeight + 10)
		if maxScroll < 0 {
			maxScroll = 0
		}
	}
	if targetScrollY > maxScroll {
		targetScrollY = maxScroll
	}
	scrollY = int32(float64(scrollY) + (float64(targetScrollY-scrollY) * 0.3))
	if abs(int(scrollY-targetScrollY)) < 1 {
		scrollY = targetScrollY
	}

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}
	if font == nil {
		return
	}

	renderWithClip(renderer, element.X, element.Y, elemWidth, elemHeight, func(r *sdl.Renderer) {
		for i, vid := range videos {
			col := int32(i) % cols
			row := int32(i) / cols
			xPos := element.X + col*(cellWidth+10)
			yPos := element.Y + row*(cellHeight+10) - scrollY
			if yPos+cellHeight < element.Y || yPos > element.Y+elemHeight {
				continue
			}

			cardX := xPos + 4
			cardY := yPos + 4
			cardW := cellWidth - 8
			cardH := cellHeight - 8
			drawCard(r, cardX, cardY, cardW, cardH, 10)

			if i == focusedResultIndex {
				fillRoundedRect(r, cardX-3, cardY-3, cardW+6, cardH+6, 12, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 55})
				fillRoundedRect(r, cardX, cardY, cardW, cardH, 10, ColorSurfaceRaised)
			}

			if isRecentlyPlayed(vid.GetURL()) {
				fillRoundedRect(r, cardX+2, cardY+8, 3, cardH-16, 2, ColorDanger)
			}

			// Thumbnail with duration badge inside its bottom-right corner.
			drawThumbnail(r, config, vid, cardX+8, cardY+8, thumbWidth, thumbHeight)
			drawDurationBadge(r, config, font, vid.Duration, cardX+thumbWidth-48+8, cardY+thumbHeight-20+8, 44, 16)

			textStartX := cardX + 8 + thumbWidth + 10
			textY := cardY + 12
			textW := cardW - 8 - thumbWidth - 18
			if textW < 24 {
				textW = 24
			}

			titleX, titleW := textStartX, textW
			titleColor := ColorTextPrimary()
			if videoStreamPreloaded(vid.GetURL()) {
				const boltSize = 12
				titleColor = ColorWarning
				drawPreloadBolt(r, textStartX, textY+2, boltSize, ColorWarning)
				titleX = textStartX + boltSize + 5
				titleW = textW - boltSize - 5
				if titleW < 24 {
					titleW = 24
				}
			}
			title := ellipsize(titleFont, vid.Title, titleW)
			renderText(r, config, titleFont, title, titleColor, titleX, textY)

			meta := ellipsize(font, resultMetaLine(vid), textW)
			renderText(r, config, font, meta, ColorTextTertiary(), textStartX, textY+24)
		}
	})

	// scroll indicator bar
	if maxScroll > 0 {
		barH := int32(40)
		barX := element.X + elemWidth - 8
		barY := element.Y + int32(float64(scrollY)/float64(maxScroll)*float64(elemHeight-barH))
		fillRoundedRect(renderer, barX, barY, 6, barH, 3, sdl.Color{R: 255, G: 255, B: 255, A: 35})
	}
}

// gridRowsFor returns the configured row count for a searchresults element.
func gridRowsFor(e Element) int32 {
	if e.Rows <= 0 {
		return 6
	}
	return e.Rows
}

// resultMetaLine builds the muted metadata line for a result.
func resultMetaLine(vid VideoInfo) string {
	uploader := vid.Uploader
	if uploader == "" {
		uploader = vid.Channel
	}
	meta := uploader
	if vid.ViewCount > 0 {
		meta += " · " + formatViewCount(vid.ViewCount)
	}
	return meta
}

// drawThumbnail renders the cached thumbnail, a placeholder, or a dark tile so
// a row never shows an empty hole.
func drawThumbnail(renderer *sdl.Renderer, config *Config, vid VideoInfo, x, y, w, h int32) {
	loaded := false
	if vid.Thumbnail != "" {
		if tex := loadThumbnail(renderer, vid.Thumbnail); tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
			loaded = true
		}
	}
	if !loaded && len(vid.Thumbnails) > 0 {
		if tex := loadThumbnailFromURLs(renderer, vid.Thumbnails); tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
			loaded = true
		}
	}
	if !loaded {
		if placeholderTexture != nil {
			renderer.Copy(placeholderTexture, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
		} else {
			fillRoundedRect(renderer, x, y, w, h, 4, ColorIconSurface)
		}
	}
}

// drawDurationBadge renders the compact duration pill inside a thumbnail's
// bottom-right corner. No row is drawn for unknown durations.
func drawDurationBadge(renderer *sdl.Renderer, config *Config, font *ttf.Font, seconds float64, x, y, w, h int32) {
	if seconds <= 0 || font == nil {
		return
	}
	dur := formatDuration(seconds)
	fillRoundedRect(renderer, x, y, w, h, h/2, sdl.Color{R: 0, G: 0, B: 0, A: 150})
	dw, _, _ := font.SizeUTF8(dur)
	renderText(renderer, config, font, dur, sdl.Color{R: 250, G: 250, B: 250, A: 255},
		x+(w-int32(dw))/2, y+(h-int32(font.Height()))/2)
}

// formatDuration renders seconds as "m:ss" or "h:mm:ss".
func formatDuration(seconds float64) string {
	s := int(seconds)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// --- Generic searchresults helpers (config-driven, no hardcoded scene name) ---
func sceneSearchResultsElement(scene SceneConfig) (Element, bool) {
	for _, e := range scene.Elements {
		if e.Type == "searchresults" {
			return e, true
		}
	}
	return Element{}, false
}

func sceneHasSearchResults(scene SceneConfig) bool {
	_, ok := sceneSearchResultsElement(scene)
	return ok
}

func getSceneVideos(config *Config, scene SceneConfig) ([]VideoInfo, bool) {
	e, ok := sceneSearchResultsElement(scene)
	if !ok {
		return nil, false
	}
	videos, ok := config.Variables.Custom[e.Variable].([]VideoInfo)
	return videos, ok
}

// --- Video playback helper ---

// currentPlaybackURL is the source URL of the video currently playing in the
// embedded player; it is used to write resume positions back to the recent
// store.
var currentPlaybackURL string

// playVideoURL plays a video by URL. It is the legacy entry point; callers
// that have the full VideoInfo should use playVideoInfo so the recent store
// records a real title and resume position.
func playVideoURL(config *Config, url string) {
	if url == "" {
		log.Printf("playVideoURL: empty URL, nothing to play")
		return
	}
	playVideoInfo(config, VideoInfo{URL: url})
}

// looksLikeStreamURL reports whether s is a playable link rather than a search
// phrase. Only explicit schemes count: "cats" stays a search query.
func looksLikeStreamURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	for _, scheme := range []string{"http://", "https://", "rtsp://", "rtmp://", "udp://", "mms://", "rtp://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	return false
}

// sceneInputURL returns the link typed into a scene's text field. The Tube and
// IPStream screens drive playback from a URL box, so their buttons must read it
// instead of silently doing nothing when no result is highlighted.
func sceneInputURL(config *Config, scene SceneConfig) string {
	if config == nil {
		return ""
	}
	for i, el := range scene.Elements {
		if el.Type != "input" {
			continue
		}
		val := ""
		if i == activeElementIndex {
			// The field is being edited: its live buffer is ahead of the
			// variable, which only updates on commit.
			val = inputTextBuffer
		} else if v, ok := config.Variables.Custom[el.Variable].(string); ok {
			val = v
		}
		if looksLikeStreamURL(val) {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// sceneWithVideoElement returns the index of the scene that can show the
// embedded player, preferring the scene already on screen. The `video` element
// is what draws the player, so it has to be on screen for playback to be
// visible.
func sceneWithVideoElement(config *Config) int {
	if config == nil {
		return -1
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		for _, el := range config.Scenes[currentSceneIndex].Elements {
			if el.Type == "video" {
				return currentSceneIndex
			}
		}
	}
	for i, scene := range config.Scenes {
		for _, el := range scene.Elements {
			if el.Type == "video" {
				return i
			}
		}
	}
	return -1
}

// ensureVideoScene moves to a scene that can draw the embedded player when the
// current scene cannot. The decoder only renders into a `video` element, and
// that element exists on the player scenes alone; starting a video from Home
// (Continue card), Favorites or Shorts used to decode with nothing on screen —
// audio only, which looked like Continue doing nothing after backing out of a
// video. It reports whether a scene change was started.
func ensureVideoScene(config *Config) bool {
	idx := sceneWithVideoElement(config)
	if idx < 0 || idx == currentSceneIndex {
		return false
	}
	changeSceneTo(config, idx)
	return true
}

// playVideoInfo starts playback for a video and records it in the recently-
// played store. A nonzero v.Position (set by the Continue card) resumes the
// embedded player from that offset.
func playVideoInfo(config *Config, v VideoInfo) {
	url := v.GetURL()
	if url == "" {
		log.Printf("playVideoInfo: empty URL, nothing to play")
		return
	}
	// Video and music share one audio device: silence the playlist first.
	musicStopIfPlaying()
	ensureVideoScene(config)
	PlayActivateSound()
	addRecentVideo(v)
	currentPlaybackURL = url

	// The embedded player is the only playback backend: yt-dlp downloads the
	// stream and ffmpeg decodes it into the scene's `video` element. The old
	// ffplay pipe is gone because the bundled ffplay is linked against X11 and
	// aborts on the handheld ("libX11.so.6: cannot open shared object file"),
	// which left every video stuck on a black screen.
	go func() {
		// Missing helper tools are downloaded on demand (the install card
		// shows progress) before playback starts.
		ytDlpPath, err := helperToolPath("yt-dlp", config)
		if err != nil {
			log.Printf("[ERROR] playVideoInfo: yt-dlp unavailable: %v", err)
			showToast(toolUnavailableMessage("yt-dlp", err), ToastError())
			return
		}
		if _, err := helperToolPath("ffmpeg", config); err != nil {
			log.Printf("[ERROR] playVideoInfo: ffmpeg unavailable: %v", err)
			showToast(toolUnavailableMessage("ffmpeg", err), ToastError())
			return
		}
		// ffprobe only supplies duration/fps; probeVideoInfo falls back to safe
		// defaults, so a missing ffprobe must not block playback.
		if _, err := helperToolPath("ffprobe", config); err != nil {
			log.Printf("[DEBUG] playVideoInfo: ffprobe unavailable, using defaults: %v", err)
		}

		log.Printf("[DEBUG] playVideoInfo: url=%q yt-dlp=%q", sanitizeLogURL(url), ytDlpPath)
		// While yt-dlp resolves, the media screens show a modal spinner and
		// ignore navigation: the grid used to keep scrolling behind a player
		// that had not started yet, so a second push looked like a dead press.
		prepareSeq, prepareCtx := beginVideoPrepare(v.Title)
		// Stream straight from the CDN when possible: ffmpeg reads the media
		// URL directly, so the first frame arrives without waiting for a full
		// download. Anything ffmpeg cannot open falls back to the download path.
		started, cancelled := tryStreamPlayback(prepareCtx, config, ytDlpPath, url, v.Position)
		if cancelled {
			finishVideoPrepare(prepareSeq)
			log.Printf("[STREAM] preparation cancelled for %s", sanitizeLogURL(url))
			return
		}
		if started {
			finishVideoPrepare(prepareSeq)
			recordPlayed(config, url)
			return
		}
		// Every streaming strategy failed: this is the slow path, and it is
		// worth saying so in the log next to the reasons logged above.
		log.Printf("[STREAM] no direct stream for %s; downloading the file instead", sanitizeLogURL(url))
		// The spinner stays up for the download and is cleared by
		// playWithTempFile once the player overlay can report progress. The
		// download honours prepareCtx, so B does not leave a download running
		// invisibly behind a screen the user already left.
		playWithTempFile(prepareCtx, config, ytDlpPath, url, v.Position, prepareSeq)
		recordPlayed(config, url)
	}()
}

// videoFormatSelector returns the yt-dlp format selector used for playback.
// The ceiling comes from variables.playbackResolution (Settings → Playback
// resolution) so slow devices such as the Trimui Smart Pro can request 360p
// instead of always asking for 720p.
func videoFormatSelector(config *Config) string {
	maxHeight := 720
	if config != nil {
		if res := strings.TrimSpace(config.Variables.PlaybackResolution); res != "" && res != "best" {
			if h, err := strconv.Atoi(res); err == nil && h > 0 {
				maxHeight = h
			}
		}
	}
	return fmt.Sprintf("best[height<=%d]/best", maxHeight)
}

// playWithTempFile downloads the stream with yt-dlp and hands the file to the
// embedded player. Downloading first (instead of piping) keeps seeking and
// resume positions working and avoids holding two decoders in memory.
func playWithTempFile(parent context.Context, config *Config, ytDlpPath, url string, startSec float64, prepareSeq uint64) {
	// The caller's spinner covers the download; every way out of here has to
	// clear it, including failures, or the media screen stays blocked.
	defer finishVideoPrepare(prepareSeq)
	log.Printf("[DEBUG] playWithTempFile: downloading to temp file for %s (resume %.1fs)", url, startSec)

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "downloading"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 0
	videoPlaybackSpeed = ""
	videoPlaybackETA = ""
	videoPlaybackError = ""
	videoPlaybackMutex.Unlock()

	// Stage the download where removeTempVideo will look for it. On the
	// handheld that is the SD card (see defaultVideoTempDir), not /tmp: the
	// tmpfs is RAM-backed and a video there can get the app killed.
	tempPath := tempVideoPath()

	// Clean up any stale temp file from a previous attempt
	os.Remove(tempPath)

	var stderrBuf bytes.Buffer
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	dlArgs := []string{
		"-o", tempPath,
		"--no-check-certificate",
		"--geo-bypass",
		"--no-continue",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		"-f", videoFormatSelector(config),
		url,
	}
	dlArgs = append(dlArgs, ytDlpExtraArgsSlice(config)...)
	dlCmd := exec.CommandContext(ctx, ytDlpPath, dlArgs...)
	dlCmd.Env = helperToolEnv(ytDlpPath)
	dlCmd.Stdout = os.Stdout
	dlCmd.Stderr = &stderrBuf

	if err := dlCmd.Start(); err != nil {
		log.Printf("[ERROR] playWithTempFile: download failed to start: %v", err)
		showToast("Download failed to start.", ToastError())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackError = err.Error()
		videoPlaybackMutex.Unlock()
		return
	}

	done := make(chan error, 1)
	go func() {
		done <- dlCmd.Wait()
	}()

	downloadOK := false
	for !downloadOK {
		select {
		case err := <-done:
			if err != nil {
				if ctx.Err() != nil {
					// Cancelled by B / Esc (or the 5-minute cap): not a network
					// failure, so no error toast and no half-download left behind.
					log.Printf("[STREAM] download cancelled for %s", sanitizeLogURL(url))
					os.Remove(tempPath)
					return
				}
				log.Printf("[ERROR] playWithTempFile: download failed: %v", err)
				log.Printf("[ERROR] playWithTempFile: stderr: %s", stderrBuf.String())
				if !IsOnline() {
					showToast("Download failed — offline, connect WiFi", ToastWarn())
				} else {
					showToast("Download failed. Check network.", ToastError())
				}
				videoPlaybackMutex.Lock()
				videoPlaybackPhase = "error"
				videoPlaybackPhaseAt = sdl.GetTicks64()
				videoPlaybackError = err.Error()
				videoPlaybackMutex.Unlock()
				return
			}
			downloadOK = true
		case <-time.After(200 * time.Millisecond):
			// Parse progress from stderr
			parseDownloadProgress(stderrBuf.String())
		}
	}

	if ctx.Err() != nil {
		log.Printf("[STREAM] download cancelled after it finished for %s", sanitizeLogURL(url))
		os.Remove(tempPath)
		return
	}

	// Verify the downloaded file is valid before playing
	info, err := os.Stat(tempPath)
	if err != nil || info.Size() == 0 {
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		log.Printf("[ERROR] playWithTempFile: downloaded file is missing or empty (size=%d)", size)
		showToast("Download failed. File is empty.", ToastError())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackError = "downloaded file is empty"
		videoPlaybackMutex.Unlock()
		os.Remove(tempPath)
		return
	}
	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "playing"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 1.0
	videoPlaybackMutex.Unlock()

	// tempFile=true: this session owns the downloaded copy and removes it when
	// playback ends. User media files are never deleted.
	startEmbeddedPlayback(config, tempPath, startSec, true)
	log.Printf("[DEBUG] playWithTempFile: embedded playback started for %s (resume %.1fs)", tempPath, startSec)
}

func parseDownloadProgress(stderr string) {
	lines := strings.Split(stderr, "\n")
	for _, line := range lines {
		if strings.Contains(line, "[download]") && strings.Contains(line, "%") {
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasSuffix(part, "%") {
					val := strings.TrimSuffix(part, "%")
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						videoPlaybackMutex.Lock()
						videoPlaybackProgress = f / 100.0
						videoPlaybackMutex.Unlock()
					}
				}
				if strings.HasSuffix(part, "MiB/s") || strings.HasSuffix(part, "KiB/s") {
					videoPlaybackSpeed = part
					embeddedPlayer.mu.Lock()
					embeddedPlayer.downloadSpeed = parseSpeedToken(part)
					embeddedPlayer.mu.Unlock()
				}
				if strings.HasPrefix(part, "ETA") {
					videoPlaybackETA = part
				}
			}
		}
	}
}

// parseSpeedToken converts a yt-dlp speed token such as "1.20MiB/s" or
// "850.5KiB/s" into bytes per second.
func parseSpeedToken(token string) float64 {
	mult := 1024.0
	if strings.HasSuffix(token, "MiB/s") {
		mult = 1024.0 * 1024.0
	}
	num := strings.TrimSuffix(strings.TrimSuffix(token, "MiB/s"), "KiB/s")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return f * mult
}

// --- Input handling ---
func handleInputSelection(renderer *sdl.Renderer, config *Config, sceneIdx, elemIdx int) {
	if sceneIdx < 0 || sceneIdx >= len(config.Scenes) {
		return
	}
	if elemIdx < 0 || elemIdx >= len(config.Scenes[sceneIdx].Elements) {
		return
	}
	// Open the keyboard and return. Running a second, nested event loop here
	// duplicated every input path and could leave the app stuck in the modal
	// when playback/network callbacks changed focus; the main loop already
	// routes keyboard, mouse, controller and touch events to the keyboard.
	vkOpenSceneInput(config, sceneIdx, elemIdx)
}

// keysymToChar maps an SDL keycode to its character, honouring Shift. This is
// used so physical-keyboard typing works deterministically in the input modal
// without depending on SDL text-input events.
func keysymToChar(sym sdl.Keycode, shifted bool) (string, bool) {
	if sym >= sdl.K_a && sym <= sdl.K_z {
		c := byte('a' + (int(sym) - int(sdl.K_a)))
		if shifted {
			c = byte('A' + (int(sym) - int(sdl.K_a)))
		}
		return string(c), true
	}
	if sym >= sdl.K_0 && sym <= sdl.K_9 {
		return string(byte('0' + (int(sym) - int(sdl.K_0)))), true
	}
	switch sym {
	case sdl.K_SPACE:
		return " ", true
	case sdl.K_MINUS:
		if shifted {
			return "_", true
		}
		return "-", true
	case sdl.K_EQUALS:
		if shifted {
			return "+", true
		}
		return "=", true
	case sdl.K_SLASH:
		if shifted {
			return "?", true
		}
		return "/", true
	case sdl.K_BACKSLASH:
		return "\\", true
	case sdl.K_PERIOD:
		return ".", true
	case sdl.K_COMMA:
		return ",", true
	case sdl.K_SEMICOLON:
		if shifted {
			return ":", true
		}
		return ";", true
	case sdl.K_LEFTBRACKET:
		return "[", true
	case sdl.K_RIGHTBRACKET:
		return "]", true
	}
	return "", false
}

// runInputModal blocks until the keyboard closes. Only the text-browser address
// bar uses it: it needs the submitted value before it can load the page.
func runInputModal(renderer *sdl.Renderer, config *Config) {
	defer func() { virtualKeyboardActive = false }()
	vkEnsureLayout()
	exitInput := false
	for !exitInput {
		drainCustom(config)
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			renderScene(renderer, config, config.Scenes[currentSceneIndex])
		}
		renderKeyboard(renderer, config)
		renderer.Present()

		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			applyButtonSwap(config, event)
			switch e := event.(type) {
			case *sdl.KeyboardEvent:
				if e.Type != sdl.KEYDOWN {
					continue
				}
				switch e.Keysym.Sym {
				case sdl.K_ESCAPE:
					vkCloseKeyboard()
					exitInput = true
				case sdl.K_BACKSPACE, sdl.K_DELETE:
					vkBackspace(config)
				case sdl.K_RETURN, sdl.K_KP_ENTER:
					vkSubmit(config, false)
					exitInput = true
				case sdl.K_TAB:
					vkMoveFocus(1, 0)
				case sdl.K_UP:
					vkMoveFocus(0, -1)
				case sdl.K_DOWN:
					vkMoveFocus(0, 1)
				case sdl.K_LEFT:
					vkMoveFocus(-1, 0)
				case sdl.K_RIGHT:
					vkMoveFocus(1, 0)
				}
				// Printable characters arrive through the text-input event below.
			case *sdl.TextInputEvent:
				vkInsertText(e.GetText(), config)
			case *sdl.ControllerButtonEvent:
				if e.Type != sdl.CONTROLLERBUTTONDOWN {
					continue
				}
				switch e.Button {
				case sdl.CONTROLLER_BUTTON_DPAD_UP:
					vkMoveFocus(0, -1)
				case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
					vkMoveFocus(0, 1)
				case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
					vkMoveFocus(-1, 0)
				case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
					vkMoveFocus(1, 0)
				case sdl.CONTROLLER_BUTTON_A:
					key := vkFocusedKey()
					vkActivateFocused(config)
					if vkIsActionKey(key) {
						exitInput = true
					}
				case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
					vkCloseKeyboard()
					exitInput = true
				case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
					toggleKeyboardCase()
				case sdl.CONTROLLER_BUTTON_Y:
					if vkTarget == vkTargetChat {
						vkSubmit(config, true)
					} else {
						vkSubmit(config, false)
					}
					exitInput = true
				}
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					if vkCloseHit(mx, my) {
						vkCloseKeyboard()
						exitInput = true
						break
					}
					if row, col, ok := vkHitTest(mx, my); ok {
						keyboardPosY, keyboardPosX = row, col
						key := vkFocusedKey()
						vkActivateFocused(config)
						if vkIsActionKey(key) {
							exitInput = true
						}
						break
					}
					// Clicking the dimmed content behind the panel closes it.
					if vkOutsidePanel(mx, my) {
						vkCloseKeyboard()
						exitInput = true
					}
				}
			case *sdl.TouchFingerEvent:
				handleTouchGesture(e, config)
				if !virtualKeyboardActive {
					exitInput = true
				}
			case *sdl.MultiGestureEvent:
				handleMultiGesture(e, config)
			}
		}
	}
}

// isTerminalScene returns true if the current scene is Terminal.
func isTerminalScene() bool {
	if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) {
		return appConfig.Scenes[currentSceneIndex].Name == "Terminal"
	}
	return false
}

func updateInputVariable(config *Config) {
	if activeSceneIndex != -1 && activeElementIndex != -1 {
		if activeSceneIndex >= len(config.Scenes) {
			return
		}
		elem := config.Scenes[activeSceneIndex].Elements[activeElementIndex]
		variable := elem.Variable
		if textBrowserAddressEditing {
			variable = textBrowserURLVariable
		}
		if variable != "" {
			config.Variables.Custom[variable] = inputTextBuffer
			syncVariableOverrides(config)
		}
	}
}

// --- Touch gesture handling ---

// touchTapMaxDrift is the largest finger movement (window pixels) still
// counted as a tap rather than a swipe.
const touchTapMaxDrift = 60

// touchToWindow converts a touch coordinate to window pixels. SDL reports
// touch positions normalized to 0..1, but some handheld backends hand out
// pixels; both conventions are accepted so hit-testing lands on the control
// that was actually touched.
func touchToWindow(x, y float32) (int32, int32) {
	w, h := screenWidth, screenHeight
	if mainWindow != nil {
		if ww, wh := mainWindow.GetSize(); ww > 0 && wh > 0 {
			w, h = ww, wh
		}
	}
	px, py := float64(x), float64(y)
	if px >= 0 && px <= 1 && py >= 0 && py <= 1 {
		px *= float64(w)
		py *= float64(h)
	}
	return int32(px), int32(py)
}

// touchTapSink receives taps as synthetic left clicks. Tests replace it to
// assert the dispatch without a real SDL event queue.
var touchTapSink = pushTapClick

// pushTapClick replays a tap through the mouse pipeline. Touch used to be
// understood only as a swipe, so on the handheld a tap on the top-bar back
// pill - or on any other on-screen control - did nothing at all.
func pushTapClick(px, py int32) {
	sdl.PushEvent(&sdl.MouseMotionEvent{Type: sdl.MOUSEMOTION, X: px, Y: py})
	sdl.PushEvent(&sdl.MouseButtonEvent{Type: sdl.MOUSEBUTTONDOWN, Button: sdl.BUTTON_LEFT, State: sdl.PRESSED, Clicks: 1, X: px, Y: py})
	sdl.PushEvent(&sdl.MouseButtonEvent{Type: sdl.MOUSEBUTTONUP, Button: sdl.BUTTON_LEFT, State: sdl.RELEASED, Clicks: 1, X: px, Y: py})
}

func handleTouchGesture(e *sdl.TouchFingerEvent, config *Config) {
	if virtualKeyboardActive {
		// The keyboard owns the screen while it is open: taps press keys or
		// dismiss it, and swipes must not change the scene underneath.
		vkHandleTouchEvent(e, config)
		return
	}
	if e.Type == sdl.FINGERDOWN {
		startX, startY := touchToWindow(e.X, e.Y)
		touchStartX = float64(startX)
		touchStartY = float64(startY)
		touchStartTime = sdl.GetTicks64()
		touchActive = true
		touchPinchActive = false
	} else if e.Type == sdl.FINGERUP && touchActive {
		touchActive = false
		if touchPinchActive {
			return
		}
		px, py := touchToWindow(e.X, e.Y)
		dx := float64(px) - touchStartX
		dy := float64(py) - touchStartY
		dt := float64(sdl.GetTicks64() - touchStartTime)
		if dt >= 500 {
			// A long press is neither a tap nor a swipe.
			return
		}
		if dx*dx+dy*dy <= float64(touchTapMaxDrift*touchTapMaxDrift) {
			// A tap, not a swipe: hand it to the mouse pipeline so buttons,
			// overlays and the top-bar back pill react to a finger exactly as
			// they do to a pointer click.
			touchTapSink(px, py)
			return
		}
		if absFloat(dx) > absFloat(dy) {
			if dx > 0 {
				handleSwipe("right", config)
			} else {
				handleSwipe("left", config)
			}
		} else {
			if dy > 0 {
				handleSwipe("down", config)
			} else {
				handleSwipe("up", config)
			}
		}
	}
}

func handleMultiGesture(e *sdl.MultiGestureEvent, config *Config) {
	if e.NumFingers == 2 {
		if e.DTheta > 0.01 || e.DTheta < -0.01 {
			return
		}
		if touchPinchActive {
			if float64(e.DDist) > 0 {
				handlePinch("out", config)
			} else if float64(e.DDist) < 0 {
				handlePinch("in", config)
			}
		}
		touchPinchDist = float64(e.DDist)
		touchPinchActive = true
	} else {
		touchPinchActive = false
	}
}

func handleSwipe(direction string, config *Config) {
	if imageViewerPath != "" {
		switch direction {
		case "up", "down":
			imageViewerZoom += 0.2
			if imageViewerZoom > 5.0 {
				imageViewerZoom = 5.0
			}
		case "left":
			imageViewerPanX += 40
		case "right":
			imageViewerPanX -= 40
		}
		return
	}
	switch direction {
	case "up":
		if currentSceneIndex > 0 {
			changeSceneTo(config, currentSceneIndex-1)
		}
	case "down":
		if currentSceneIndex < len(config.Scenes)-1 {
			changeSceneTo(config, currentSceneIndex+1)
		}
	case "left":
		handleSwipeLeft(config)
	case "right":
		handleSwipeRight(config)
	}
}

func handleSwipeLeft(config *Config) {
	if currentSceneIndex > 0 {
		changeSceneTo(config, currentSceneIndex-1)
	}
}

func handleSwipeRight(config *Config) {
	if currentSceneIndex < len(config.Scenes)-1 {
		changeSceneTo(config, currentSceneIndex+1)
	}
}

func handlePinch(direction string, config *Config) {
	if imageViewerPath == "" {
		return
	}
	if direction == "out" {
		imageViewerZoom += 0.1
		if imageViewerZoom > 5.0 {
			imageViewerZoom = 5.0
		}
	} else {
		imageViewerZoom -= 0.1
		if imageViewerZoom < 0.5 {
			imageViewerZoom = 0.5
		}
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// syncVariableOverrides copies known Custom variable overrides into the typed
// Variables struct fields so that Settings inputs actually change runtime
// behavior (fullscreen, weather, resolution, paths, colors).
func syncVariableOverrides(config *Config) {
	c := config.Variables.Custom
	// Lookups are case-insensitive and accept strings, numbers or bools: the web
	// builder emits capitalised keys ("GridColumns") and type-coerced numbers, so
	// the old exact-key, string-only lookups silently dropped those settings.
	setBool := func(name string, dst *bool) {
		if v, ok := customValue(c, name); ok {
			*dst = coerceBool(v)
		}
	}
	setInt := func(name string, dst *int) {
		if v, ok := customValue(c, name); ok {
			if n, ok := coerceInt(v); ok {
				*dst = n
			}
		}
	}
	setStr := func(name string, dst *string) {
		if v, ok := customValue(c, name); ok {
			*dst = coerceString(v)
		}
	}
	setBool("fullscreen", &config.Variables.Fullscreen)
	setBool("weatherEnabled", &config.Variables.WeatherEnabled)
	setBool("reducedMotion", &config.Variables.ReducedMotion)
	setBool("lowPower", &config.Variables.LowPower)
	setBool("dynamicBg", &config.Variables.DynamicBg)
	setBool("socialNotifs", &config.Variables.SocialNotifs)
	setStr("weatherUnit", &config.Variables.WeatherUnit)
	setStr("tspUsername", &config.Variables.TSPUsername)
	setStr("playbackResolution", &config.Variables.PlaybackResolution)
	setStr("fileExplorerRoot", &config.Variables.FileExplorerRoot)
	setStr("tools_path", &config.Variables.ToolsPath)
	setInt("gridColumns", &config.Variables.GridColumns)
	setInt("searchWidth", &config.Variables.SearchWidth)
	setInt("screenWidth", &config.Variables.ScreenWidth)
	setInt("screenHeight", &config.Variables.ScreenHeight)
	setInt("screensaverTimeout", &config.Variables.ScreensaverTimeout)
	// Colors come through customString so a string value ("#0F1420") wins over a
	// struct-shaped sibling that some builder versions emit as {r,g,b} zeroes.
	for _, name := range []string{"buttonColor", "labelColor", "inputColor"} {
		if s, ok := customString(c, name); ok {
			if strings.Contains(s, ",") || strings.HasPrefix(s, "#") {
				applyColorVar(config, name, s)
			}
		}
	}
	// One snapshot helper for every save, so a settings change cannot drop the
	// Quick Settings levels on the way to disk.
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// inputVariableValue resolves the current value of a settings input from the
// canonical Variables struct fields first (so General shows the real value
// even before the user types), falling back to Variables.Custom. Known names
// map to struct fields; anything else is read from Custom.
func inputVariableValue(config *Config, variable string) string {
	v := strings.TrimSpace(variable)
	switch v {
	case "fullscreen":
		if config.Variables.Fullscreen {
			return "true"
		}
		return "false"
	case "weatherEnabled":
		if config.Variables.WeatherEnabled {
			return "true"
		}
		return "false"
	case "weatherUnit":
		return config.Variables.WeatherUnit
	case "tspUsername":
		return config.Variables.TSPUsername
	case "playbackResolution":
		return config.Variables.PlaybackResolution
	case "fileExplorerRoot":
		return config.Variables.FileExplorerRoot
	case "reducedMotion":
		if config.Variables.ReducedMotion {
			return "true"
		}
		return "false"
	case "lowPower":
		if config.Variables.LowPower {
			return "true"
		}
		return "false"
	case "gridColumns":
		return strconv.Itoa(config.Variables.GridColumns)
	case "searchWidth":
		return strconv.Itoa(config.Variables.SearchWidth)
	case "dynamicBg":
		if config.Variables.DynamicBg {
			return "true"
		}
		return "false"
	case "screensaverTimeout":
		return strconv.Itoa(config.Variables.ScreensaverTimeout)
	case "socialNotifs":
		if config.Variables.SocialNotifs {
			return "true"
		}
		return "false"
	case "buttonColor", "labelColor", "inputColor":
		return rgbHex(effectiveThemeColor(config, v))
	}
	if val, ok := config.Variables.Custom[v]; ok {
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func renderInputField(renderer *sdl.Renderer, config *Config, element Element, sceneIdx, elemIdx int) {
	width, height := resolveElementDimensions(element, 200, 40)

	bgColor := resolveColor(config, element.BgColor, sdl.Color{R: 20, G: 24, B: 34, A: 255})
	r := int32(8)
	isActive := (sceneIdx == activeSceneIndex && elemIdx == activeElementIndex)

	// subtle shadow
	fillRoundedRect(renderer, element.X+1, element.Y+2, width, height, r, ShadowFill(32))

	if isActive {
		// soft layered focus glow
		fillRoundedRect(renderer, element.X-2, element.Y-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 22})
		fillRoundedRect(renderer, element.X-1, element.Y-1, width+2, height+2, r+1,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 45})
		// crisp inner border
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 150)
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + 1, W: width - 2, H: 1})
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + 1, W: 1, H: height - 2})
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + height - 2, W: width - 2, H: 1})
		renderer.DrawRect(&sdl.Rect{X: element.X + width - 2, Y: element.Y + 1, W: 1, H: height - 2})
	} else {
		// resting hairline border so the field reads as interactive
		renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, ColorBorderSubtle.A)
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + 1, W: width - 2, H: 1})
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + 1, W: 1, H: height - 2})
		renderer.DrawRect(&sdl.Rect{X: element.X + 1, Y: element.Y + height - 2, W: width - 2, H: 1})
		renderer.DrawRect(&sdl.Rect{X: element.X + width - 2, Y: element.Y + 1, W: 1, H: height - 2})
	}

	// background
	fillRoundedRect(renderer, element.X, element.Y, width, height, r, bgColor)

	// active bottom accent bar
	if isActive {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
		renderer.FillRect(&sdl.Rect{X: element.X + 8, Y: element.Y + height - 3, W: width - 16, H: 2})
	}

	textColor := resolveColor(config, element.Color, sdl.Color{R: 220, G: 226, B: 240, A: 255})
	font, _ := getCachedFont(config, element.Font)

	// Color settings render the picked color itself. A bare "#3498db" told the
	// user nothing about what they had chosen, and the swatch follows the live
	// buffer so it previews as the value is typed.
	textX := element.X + 14
	maxTextW := int32(0)
	if col, ok, isColorField := settingsFieldColor(config, element, isActive); isColorField {
		size := height - 16
		if size > 32 {
			size = 32
		}
		if size < 14 {
			size = 14
		}
		sx := element.X + 10
		sy := element.Y + (height-size)/2
		radius := size / 4
		// A light outer ring keeps dark picks visible on the dark panel.
		strokeRoundedRect(renderer, sx-1, sy-1, size+2, size+2, radius+1, 1, sdl.Color{R: 255, G: 255, B: 255, A: 46})
		if ok {
			fillRoundedRect(renderer, sx, sy, size, size, radius, col)
			fillRoundedRect(renderer, sx, sy, size, size/2, radius, sdl.Color{R: 255, G: 255, B: 255, A: 18})
		} else {
			// Mid-typing or invalid: show an empty slot, never a wrong color.
			fillRoundedRect(renderer, sx, sy, size, size, radius, WithAlpha(ColorSurfaceRaised, 220))
			strokeRoundedRect(renderer, sx, sy, size, size, radius, 1, WithAlpha(ColorTextTertiary(), 150))
		}
		textX = sx + size + 12
		maxTextW = width - (textX - element.X) - 14
		if maxTextW < 40 {
			maxTextW = 40
		}
	}

	// Show the focused input's live buffer; every other field shows its own
	// persisted value (never the shared global buffer).
	text := ""
	if isActive {
		text = inputTextBuffer
		if uint32(sdl.GetTicks64()/500)%2 == 0 {
			text += "_"
		}
	} else if element.Variable != "" {
		text = inputVariableValue(config, element.Variable)
	} else {
		text = inputTextBuffer
	}
	if font != nil && text != "" {
		draw := text
		if maxTextW > 0 {
			draw = ellipsize(font, text, maxTextW)
		}
		renderText(renderer, config, font, draw, textColor, textX, element.Y+12)
	}

	// placeholder
	if !isActive && text == "" {
		placeholder := element.Placeholder
		if placeholder == "" {
			placeholder = "Type here..."
		}
		phColor := ColorTextTertiary()
		if maxTextW > 0 && font != nil {
			placeholder = ellipsize(font, placeholder, maxTextW)
		}
		renderText(renderer, config, font, placeholder, phColor, textX, element.Y+12)
	}
}

// --- renderScene ---
func renderScene(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	// A registered scene owns its whole body. Everything else falls through to
	// the config-driven element renderer below, which is the common case.
	if m, ok := lookupScene(scene.Name); ok && m.render(renderer, config) {
		return
	}

	ensureBackgroundTexture(renderer, config)

	if bgTexture != nil {
		renderer.Copy(bgTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		renderer.SetDrawColor(overlayColor.R, overlayColor.G, overlayColor.B, overlayColor.A)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	} else {
		bg := ColorBackground
		ensureGradientTexture(renderer, config, bg)
		if gradientTexture != nil {
			renderer.Copy(gradientTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		} else {
			renderer.SetDrawColor(bg.R, bg.G, bg.B, 255)
			renderer.Clear()
		}
	}
	refreshHomeLayout(config)

	// The home scene renders through the dedicated modern renderer so it can
	// use its own header, Continue card, focus treatment, and footer hints.
	// Generic config-driven element rendering stays for every other scene.
	if homeLayoutActive {
		// Home draws its own header (with the same status cluster and the
		// notification bell) and its own footer, so the shared bars must not
		// leave stale hit areas behind for this frame.
		sceneTitleBackRect = sdl.Rect{}
		statusClockRect = sdl.Rect{}
		tb.startRect = sdl.Rect{}
		renderHomeModern(renderer, config)
		renderWeatherPopover(renderer, config)
		renderNotifDropdown(renderer, config)
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetClipRect(nil)
		return
	}

	// Content panel behind the scene elements to frame the form/list area.
	// It ends 16px above the footer so no content can reach the footer band.
	panelMargin := int32(24)
	panelTop := HomeTopBarH + 8
	panelBottom := screenHeight - bottomBarHeight(config.Scenes[currentSceneIndex].Name) - 16
	panel := sdl.Rect{
		X: panelMargin,
		Y: panelTop,
		W: screenWidth - panelMargin*2,
		H: panelBottom - panelTop,
	}
	fillRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, RadiusLG, WithAlpha(ColorSurface, 200))
	strokeRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, RadiusLG, 1, WithAlpha(ColorBorder, 120))
	// Subtle accent line at the top of the panel.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 30)
	renderer.FillRect(&sdl.Rect{X: panel.X + 20, Y: panel.Y, W: panel.W - 40, H: 1})

	// Dynamic animated background: time-aware, weather-reactive, living backdrop.
	if config.Variables.DynamicBg {
		renderDynamicBackground(renderer)
	} else {
		renderAmbientBackground(renderer)
	}

	for i, elem := range scene.Elements {
		if homeLayoutActive && elem.Style == "tile" {
			continue
		}
		switch elem.Type {
		case "label":
			renderLabelShadowed(renderer, config, elem)
		case "button":
			renderButtonElement(renderer, config, elem, i, i == selectedButtonIndex, i == hoveredButtonIndex, hoverAnimProgress, i == pressedButtonIndex)
		case "input":
			renderInputField(renderer, config, elem, currentSceneIndex, i)
		case "searchresults":
			renderSearchResults(renderer, config, elem)
		case "dynamiclist":
			renderDynamicList(renderer, config, elem)
		case "textlog":
			renderTextLog(renderer, config, elem)
		case "piechart":
			renderDiskPieChart(renderer, config, elem)
		case "favorites":
			renderFavorites(renderer, config, elem)
		case "unitconverter":
			renderUnitConverter(renderer, config, elem)
		case "chat":
			renderChat(renderer, config, elem)
		case "shortslist":
			renderShortsGrid(renderer, config, elem)
		case "canvas":
			renderCanvasSandbox(renderer, config, elem)
		case "textbrowser":
			renderTextBrowser(renderer, config, elem)
		case "packagelist":
			renderPackageList(renderer, config, elem)
		case "recent":
			renderRecent(renderer, config, elem)
		case "image":
			renderImageElement(renderer, config, elem)
		case "video":
			renderVideoElement(renderer, config, elem)
		case "themegallery":
			renderThemeGallery(renderer, config, elem)
		case "toggle":
			renderToggleElement(renderer, config, elem, i == selectedButtonIndex)
		default:
			log.Printf("Unknown element type: %s", elem.Type)
		}
	}

	// Settings → Appearance shows the palette itself, not just its hex codes.
	if scene.Name == "SettingsAppearance" {
		renderAppearancePreview(renderer, config)
	}

	// Single global header: brand + version + active scene, the Apps pill and
	// the right-side system cluster (network, weather, clock, battery, bell).
	renderStatusBar(renderer, config)

	// The bottom of every menu is reserved for buttons now: the controller
	// hint bar, plus the app menu overlay when it is open.
	renderFooter(renderer, config)
	renderWeatherPopover(renderer, config)
	renderStartMenu(renderer, config)
	renderNotifDropdown(renderer, config)

	// Reset renderer state after rendering to avoid state leakage
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// sceneHasBackButton reports whether the scene still declares a body-level
// Back button. Back navigation is universal now (B/Escape + the header back
// pill), so no scene should declare one; the helper guards the assertion.
func sceneHasBackButton(scene SceneConfig) bool {
	for _, el := range scene.Elements {
		if el.Type == "button" && strings.EqualFold(strings.TrimSpace(el.Text), "back") {
			return true
		}
	}
	return false
}

// themeGalleryIndex is the focused card in a themegallery element.
var themeGalleryIndex int

// The gallery is a scrolling grid now that there are more presets than fit on
// one row. Card size, column count, scroll row and hit-testing all come from
// themeGalleryMetrics so rendering and clicks can never disagree.
const (
	themeGalleryGap  = int32(16)
	themeGalleryMinW = int32(230)
	themeGalleryH    = int32(168)
)

// themeGalleryScroll is the first visible row.
var themeGalleryScroll int

// themeGalleryMetrics returns the grid geometry for an element.
func themeGalleryMetrics(element Element) (cols int, cardW int32, visibleRows int) {
	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 560)
	cols = UIGridColumns(elemW, themeGalleryMinW, themeGalleryGap)
	cardW = UIGridCellWidth(elemW, cols, themeGalleryGap)
	visibleRows = UIGridVisibleRows(elemH, themeGalleryH, themeGalleryGap)
	return cols, cardW, visibleRows
}

// themeGalleryColumns is the number of cards per row (used for Up/Down).
func themeGalleryColumns(element Element) int {
	cols, _, _ := themeGalleryMetrics(element)
	return cols
}

// themeGalleryScrollFor keeps the focused row on screen and returns the first
// visible row. The scroll only moves when the focus leaves the visible band, so
// a short nav does not jerk the grid around.
func themeGalleryScrollFor(element Element, focus int) int {
	cols, _, visibleRows := themeGalleryMetrics(element)
	rows := (len(ListThemePresets()) + cols - 1) / cols
	row := 0
	if focus > 0 {
		row = focus / cols
	}
	maxFirst := rows - visibleRows
	if maxFirst < 0 {
		maxFirst = 0
	}
	if themeGalleryScroll > maxFirst {
		themeGalleryScroll = maxFirst
	}
	if themeGalleryScroll < 0 {
		themeGalleryScroll = 0
	}
	if row < themeGalleryScroll {
		themeGalleryScroll = row
	}
	if row >= themeGalleryScroll+visibleRows {
		themeGalleryScroll = row - visibleRows + 1
	}
	return themeGalleryScroll
}

// themeGalleryMove shifts the focused card by delta cards inside the grid and
// reports whether the focus moved. Up/Down pass ±columns, Left/Right ±1.
func themeGalleryMove(element Element, delta int) bool {
	n := len(ListThemePresets())
	if n == 0 {
		return false
	}
	next := themeGalleryIndex + delta
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next == themeGalleryIndex {
		return false
	}
	themeGalleryIndex = next
	themeGalleryScrollFor(element, next)
	return true
}

// themeGalleryCardAt returns the preset index under (mx, my) or -1.
func themeGalleryCardAt(element Element, mx, my int32) int {
	presets := ListThemePresets()
	if len(presets) == 0 {
		return -1
	}
	cols, cardW, _ := themeGalleryMetrics(element)
	scroll := themeGalleryScrollFor(element, themeGalleryIndex)
	relX := mx - element.X
	relY := my - element.Y
	if relX < 0 || relY < 0 {
		return -1
	}
	col := int(relX / (cardW + themeGalleryGap))
	row := int(relY / (themeGalleryH + themeGalleryGap))
	if col < 0 || col >= cols || row < 0 {
		return -1
	}
	// Reject the gutter between two cards.
	if relX-(int32(col)*(cardW+themeGalleryGap)) > cardW ||
		relY-(int32(row)*(themeGalleryH+themeGalleryGap)) > themeGalleryH {
		return -1
	}
	idx := (scroll+row)*cols + col
	if idx < 0 || idx >= len(presets) {
		return -1
	}
	return idx
}

// renderThemeGallery draws the theme presets as a scrolling grid of preview
// cards: each shows the theme name, its background/surface/accent swatches,
// and a Current badge on the active preset. The focused card uses the same 3px
// accent ring as home cards so selection is unmistakable.
func renderThemeGallery(renderer *sdl.Renderer, config *Config, element Element) {
	presets := ListThemePresets()
	if len(presets) == 0 {
		return
	}
	if themeGalleryIndex < 0 {
		themeGalleryIndex = 0
	}
	if themeGalleryIndex >= len(presets) {
		themeGalleryIndex = len(presets) - 1
	}
	current := ""
	if p, ok := config.Variables.Custom["theme_preset"].(string); ok {
		current = p
	}

	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font == nil || small == nil {
		return
	}

	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 560)
	cols, cardW, visibleRows := themeGalleryMetrics(element)
	scroll := themeGalleryScrollFor(element, themeGalleryIndex)
	rows := (len(presets) + cols - 1) / cols

	drawCard := func(i int) {
		name := presets[i]
		p := GetThemePreset(name)
		row, col := i/cols, i%cols
		x := element.X + int32(col)*(cardW+themeGalleryGap)
		y := element.Y + int32(row-scroll)*(themeGalleryH+themeGalleryGap)
		focused := i == themeGalleryIndex

		fill := ColorCard
		if focused {
			fill = ColorCardFocus
		}
		fillRoundedRect(renderer, x, y, cardW, themeGalleryH, 16, fill)
		if focused {
			strokeRoundedRect(renderer, x, y, cardW, themeGalleryH, 16, 3, ColorAccent)
		} else {
			strokeRoundedRect(renderer, x, y, cardW, themeGalleryH, 16, 1, ColorBorder)
		}

		// Theme preview panel: the preset's background with an "Aa" text
		// sample, its raised surface and an accent strip, so each card reads
		// like a real screen of that theme.
		prevX := x + 12
		prevY := y + 12
		prevW := cardW - 24
		prevH := int32(74)
		fillRoundedRect(renderer, prevX, prevY, prevW, prevH, 10, hexRGBA(p.Background))
		strokeRoundedRect(renderer, prevX, prevY, prevW, prevH, 10, 1, hexRGBA(p.BorderDefault))
		// A sample card inside the preview shows the theme's surface + text.
		fillRoundedRect(renderer, prevX+10, prevY+8, prevW-20, prevH-24, 6, hexRGBA(p.SurfaceRaised))
		drawText(renderer, small, "Aa", prevX+18, prevY+14, hexRGBA(p.TextPrimary), textAlignLeft)
		// Accent strip along the preview's bottom edge.
		fillRoundedRect(renderer, prevX, prevY+prevH-7, prevW, 7, 3, hexRGBA(p.Info))

		// Current badge on the preview corner.
		if name == current {
			badgeW := int32(74)
			badgeH := int32(20)
			badgeX := prevX + prevW - badgeW - 8
			badgeY := prevY + 6
			fillRoundedRect(renderer, badgeX, badgeY, badgeW, badgeH, badgeH/2, hexRGBA(p.Info))
			drawText(renderer, small, "Current", badgeX+badgeW/2, badgeY+(badgeH-int32(small.Height()))/2,
				contrastText(hexRGBA(p.Info)), textAlignCenter)
		}

		// Theme name below the preview.
		nameY := prevY + prevH + 8
		drawText(renderer, font, ellipsize(font, p.Name, cardW-36), x+18, nameY, ColorTextPrimary(), textAlignLeft)

		// Mini palette chips: Surface, Text, Focus border.
		chipY := nameY + int32(font.Height()) + 6
		chipH := int32(12)
		chipW := (cardW - 36 - 16) / 3
		chips := []string{p.SurfaceAlt, p.TextPrimary, p.BorderFocus}
		for j, c := range chips {
			cx := x + 18 + int32(j)*(chipW+8)
			fillRoundedRect(renderer, cx, chipY, chipW, chipH, 4, hexRGBA(c))
			strokeRoundedRect(renderer, cx, chipY, chipW, chipH, 4, 1, ColorBorder)
		}
	}

	renderWithClip(renderer, element.X, element.Y, elemW, elemH, func(ren *sdl.Renderer) {
		for i := scroll * cols; i < len(presets) && i/cols < scroll+visibleRows; i++ {
			drawCard(i)
		}
	})

	if rows > visibleRows {
		thumbFrac := float64(visibleRows) / float64(rows)
		scrollFrac := 0.0
		if rows > visibleRows {
			scrollFrac = float64(scroll) / float64(rows-visibleRows)
		}
		drawScrollbar(renderer, element.X+elemW-4, element.Y, 3, elemH, thumbFrac, scrollFrac)
	}
}

// toggleValue reports the current boolean state of a toggle element by
// parsing its variable's current value ("true"/"1"/"on" are on; anything
// else is off).
func toggleValue(config *Config, element Element) bool {
	v := strings.ToLower(strings.TrimSpace(inputVariableValue(config, element.Variable)))
	switch v {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	// Unknown/empty: use the config's default from the trigger value if set.
	d := strings.ToLower(strings.TrimSpace(element.TriggerValue))
	return d == "true" || d == "1" || d == "yes" || d == "on"
}

// abButtonsSwapped reports whether the A/B face buttons should be treated as
// flipped. Some pads and firmwares (Nintendo-style layouts, certain TrimUI
// builds) report A/B inverted relative to the SDL Xbox-style mapping the scene
// handlers assume, so the "swapABButtons" setting flips them at the event entry
// point instead of in every handler.
func abButtonsSwapped(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "swapABButtons"); ok {
		return coerceBool(v)
	}
	// Handheld firmware (TrimUI) reports the face buttons inverted relative to
	// SDL's Xbox-style mapping: physical A arrives as CONTROLLER_BUTTON_B.
	// Without the swap, A would go Back and B would select. Desktops keep the
	// SDL mapping as-is.
	return P().IsHandheld()
}

// applyButtonSwap normalizes A/B face-button events against the
// "swapABButtons" setting so every scene and overlay sees one consistent
// mapping. All controller input flows through the PollEvent loops, so
// swapping here covers the whole app.
//
// It also folds the auxiliary buttons some handheld firmwares use for the B
// face button (GUIDE / MISC1) into BACK, and logs each press while running on
// a handheld. That log is the only way to tell a wrongly mapped B button apart
// from a handler that ignored it.
func applyButtonSwap(config *Config, event sdl.Event) {
	ce, ok := event.(*sdl.ControllerButtonEvent)
	if !ok {
		return
	}
	raw := ce.Button
	handheld := P().IsHandheld()
	ce.Button = normalizeHandheldBackButton(ce.Button, handheld)
	if abButtonsSwapped(config) {
		switch ce.Button {
		case sdl.CONTROLLER_BUTTON_A:
			ce.Button = sdl.CONTROLLER_BUTTON_B
		case sdl.CONTROLLER_BUTTON_B:
			ce.Button = sdl.CONTROLLER_BUTTON_A
		}
	}
	if ce.Type == sdl.CONTROLLERBUTTONDOWN && handheld && controllerInputLogs < controllerInputLogLimit {
		controllerInputLogs++
		scene := ""
		if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			scene = config.Scenes[currentSceneIndex].Name
		}
		log.Printf("[input] pad button raw=%d mapped=%d scene=%q", raw, ce.Button, scene)
	}
}

// Keep the input trace short: it exists to identify a mis-mapped back button,
// not to log every navigation press for a whole session on the SD card.
const controllerInputLogLimit = 60

var controllerInputLogs int

// toggleVariable flips a boolean setting, persists it, and applies live side
// effects (e.g. Fullscreen on desktop builds).
func toggleVariable(config *Config, element Element) {
	on := !toggleValue(config, element)
	PlayToggleSound()
	config.Variables.Custom[element.Variable] = map[bool]string{true: "true", false: "false"}[on]
	syncVariableOverrides(config)
	if strings.EqualFold(strings.TrimSpace(element.Variable), "fullscreen") && mainWindow != nil {
		if on {
			mainWindow.SetFullscreen(1)
		} else {
			mainWindow.SetFullscreen(0)
		}
	}
	// Widget toggles (start at login, battery saver, ...) act on the system and
	// show their own, more useful message.
	if widgetToggleSideEffect(config, element.Variable, on) {
		return
	}
	label := strings.TrimSpace(element.Text)
	if label == "" {
		label = element.Variable
	}
	showToast(label+": "+map[bool]string{true: "On", false: "Off"}[on], ToastInfo())
}

// renderToggleElement draws a controller-first boolean switch: the label on
// the left and an ON/OFF pill on the right, using the same resting/focused
// card treatment as the rest of the design system. A/Enter or a click flips
// the value.
func renderToggleElement(renderer *sdl.Renderer, config *Config, element Element, focused bool) {
	width, height := resolveElementDimensions(element, 320, 48)

	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}

	fill := ColorCard
	border := ColorBorder
	borderW := int32(1)
	if focused {
		fill = ColorCardFocus
		border = ColorAccent
		borderW = 3
	}
	fillRoundedRect(renderer, element.X, element.Y, width, height, 12, fill)
	strokeRoundedRect(renderer, element.X, element.Y, width, height, 12, borderW, border)

	on := toggleValue(config, element)

	// Label, left-aligned.
	label := element.Text
	if label == "" {
		label = element.Variable
	}

	// ON/OFF pill at the right.
	pillW := int32(76)
	pillH := int32(32)
	pillX := element.X + width - pillW - 14

	// The label may never run under the pill: a long label used to be drawn
	// straight across it ("Weather Enable[d]  ON").
	labelCol := ColorTextPrimary()
	labelMaxW := pillX - (element.X + 18) - 12
	if labelMaxW < 24 {
		labelMaxW = 24
	}
	label = ellipsize(font, label, labelMaxW)
	drawText(renderer, font, label, element.X+18, element.Y+(height-int32(font.Height()))/2, labelCol, textAlignLeft)
	pillY := element.Y + (height-pillH)/2
	pillCol := ColorIconSurface
	textCol := ColorTextPrimary()
	if on {
		pillCol = ColorAccent
		textCol = ColorIconDark
	}
	fillRoundedRect(renderer, pillX, pillY, pillW, pillH, pillH/2, pillCol)
	if focused && on {
		strokeRoundedRect(renderer, pillX, pillY, pillW, pillH, pillH/2, 1, ColorIconDark)
	}
	if small != nil {
		pillText := "OFF"
		if on {
			pillText = "ON"
		}
		pw, _, _ := small.SizeUTF8(pillText)
		drawText(renderer, small, pillText, pillX+(pillW-int32(pw))/2, pillY+(pillH-int32(small.Height()))/2, textCol, textAlignLeft)
	}
}

// abs returns the absolute value of x.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// easeOutCubic applies cubic easing-out to t (0..1).
// Returns 0 when t=0, 1 when t=1, with ease-out curve in between.
// Useful for smooth animations that decelerate toward the end.
func easeOutCubic(t float64) float64 {
	return 1 - math.Pow(1-t, 3)
}

// lerp linearly interpolates from a to b by factor t (typically 0..1).
// When t=0 returns a, when t=1 returns b, linearly in between.
// t may be outside [0,1] for extrapolation.
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// lerpInt32 linearly interpolates between two int32 values.
// The result is rounded toward zero.
func lerpInt32(a, b int32, t float64) int32 {
	return int32(lerp(float64(a), float64(b), t))
}

// ToastKind classifies toast severity; it drives both the severity accent and
// how long the message stays on screen.
type ToastKind uint8

const (
	ToastKindInfo ToastKind = iota
	ToastKindSuccess
	ToastKindWarning
	ToastKindError
	ToastKindSwitch // short-lived scene-switch notification (L2/R2, Q/E)
)

// ToastState is the single active toast. Replacing the message resets the
// timer; an empty message renders nothing. lines/linesW cache the wrapped
// text so the render loop only re-measures when the message or width changes.
type ToastState struct {
	Message   string
	Kind      ToastKind
	StartedAt uint64 // monotonic SDL ticks
	Duration  uint64 // ms
	lines     []string
	linesW    int32
}

// toastRect stores the last-rendered toast bounds for dismiss-on-click.
var toastRect sdl.Rect

// toastDurationFor returns the display time for a toast kind: info/success
// ~2.5s, warnings ~3.5s, errors ~5s.
func toastDurationFor(kind ToastKind) uint64 {
	switch kind {
	case ToastKindError:
		return 5000
	case ToastKindWarning:
		return 3500
	case ToastKindSwitch:
		return 1500
	default:
		return 2500
	}
}

// toastKindColor returns the severity accent used by the toast indicator.
func toastKindColor(kind ToastKind) sdl.Color {
	switch kind {
	case ToastKindError:
		return ColorToastError
	case ToastKindWarning:
		return ColorToastWarn
	case ToastKindSuccess:
		return ColorToastSuccess
	case ToastKindSwitch:
		return ColorToastInfo
	default:
		return ColorToastInfo
	}
}

// ShowToast is the single entry point for user-facing notifications. A newer
// toast replaces the current one and resets its timer; whitespace-only
// messages clear any pending toast so nothing renders.
//
// Every visible toast is also filed in the notification center's history, so a
// download that finished or an error that flashed by can be re-read later.
// ToastKindSwitch is the exception: scene-switch labels (L2/R2, Q/E) are
// navigation feedback, not events, and would bury the log.
func ShowToast(message string, kind ToastKind) {
	showToastPrimitive(message, kind)
	msg := strings.TrimSpace(message)
	if msg == "" || kind == ToastKindSwitch {
		return
	}
	notifRecord(msg, toastKindPriority(kind))
}

// showToastPrimitive renders a toast without touching the notification
// history. ShowToast wraps it for normal callers; notifPush uses it directly
// because it has already recorded its own message.
func showToastPrimitive(message string, kind ToastKind) {
	msg := strings.TrimSpace(message)
	currentToast = ToastState{
		Message:   msg,
		Kind:      kind,
		StartedAt: sdl.GetTicks64(),
		Duration:  toastDurationFor(kind),
		lines:     nil,
		linesW:    -1,
	}
	if msg == "" {
		return
	}
	switch kind {
	case ToastKindError, ToastKindWarning:
		PlayErrorSound()
	case ToastKindSuccess, ToastKindSwitch:
		PlaySuccessSound()
	}
}

// showToast is the legacy entry point used across the codebase; it maps the
// semantic color constants to a ToastKind and delegates to ShowToast.
func showToast(message string, color sdl.Color) {
	kind := ToastKindInfo
	switch color {
	case ColorToastError:
		kind = ToastKindError
	case ColorToastWarn:
		kind = ToastKindWarning
	case ColorToastSuccess:
		kind = ToastKindSuccess
	}
	ShowToast(message, kind)
}

// trimLastRune removes the last user-visible character, not the last byte.
// Text fields must use it: slicing a multi-byte rune in half leaves invalid
// UTF-8 that renders as a replacement glyph and makes the next backspace look
// like a no-op. The on-screen keyboard has always deleted by rune; the
// physical-keyboard handlers sliced by byte and disagreed with it.
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	if size <= 0 {
		return ""
	}
	return s[:len(s)-size]
}

// wrapMeasured breaks runes into at most two lines that fit maxW, using the
// measure callback for pixel widths. A truncated second line ends with an
// ellipsis; the only allowed overflow is a single rune wider than maxW.
func wrapMeasured(runes []rune, maxW int32, measure func(string) int32) []string {
	if len(runes) == 0 {
		return nil
	}
	if maxW <= 0 {
		return []string{string(runes)}
	}
	fit := func(r []rune, suffix string) int {
		n := 0
		for n < len(r) {
			if measure(string(r[:n+1])+suffix) > maxW {
				break
			}
			n++
		}
		return n
	}
	first := fit(runes, "")
	if first == 0 {
		first = 1
	}
	if first >= len(runes) {
		return []string{string(runes)}
	}
	rest := runes[first:]
	second := fit(rest, "…")
	if second == 0 {
		second = 1
	}
	secondLine := string(rest[:second]) + "…"
	if measure(secondLine) > maxW {
		// Even one rune + ellipsis overflows (pathologically narrow box):
		// emit just the first line instead of a wasteful second line.
		if second == 1 {
			return []string{string(runes[:first])}
		}
		secondLine = string(rest[:second])
	}
	return []string{string(runes[:first]), secondLine}
}

// wrapTextLines word-wraps text into at most maxLines lines of at most maxW
// pixels, honouring explicit newlines as paragraph breaks and marking a cut
// with an ellipsis. The update modal and the Patch confirmation used to hand
// strings with \n straight to renderText, which drew them as one long line that
// ran off the card, or cut a long body after two rune-split lines.
func wrapTextLines(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	if font == nil || maxLines <= 0 {
		return nil
	}
	measure := func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			cand := word
			if cur != "" {
				cand = cur + " " + word
			}
			// A lone word wider than the box stays on its own line and is
			// ellipsized below, so wrapping always terminates.
			if cur == "" || measure(cand) <= maxW {
				cur = cand
				continue
			}
			out = append(out, cur)
			cur = word
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	truncated := len(out) > maxLines
	if truncated {
		out = out[:maxLines]
	}
	for i := range out {
		out[i] = ellipsize(font, out[i], maxW)
	}
	if truncated && len(out) > 0 {
		out[len(out)-1] = fitTextToWidth(font, out[len(out)-1]+"…", maxW)
	}
	return out
}

// wrapToastLines wraps the toast message to at most two lines within maxW.
func wrapToastLines(font *ttf.Font, message string, maxW int32) []string {
	if font == nil {
		return []string{message}
	}
	return wrapMeasured([]rune(message), maxW, func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	})
}

// renderToast draws the single active toast as a compact card in the
// bottom-right corner, 16px above the footer. It renders nothing when there
// is no message and clears expired state automatically. It is drawn after the
// scene and footer, and restores the blend mode so later opaque drawing is
// unaffected.
func renderToast(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	t := currentToast
	if strings.TrimSpace(t.Message) == "" {
		return
	}
	elapsed := sdl.GetTicks64() - t.StartedAt
	if t.Duration == 0 || elapsed > t.Duration {
		currentToast = ToastState{}
		toastRect = sdl.Rect{}
		return
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Lightweight animation: slide 16px in from the right and fade in over the
	// first 150ms; fade out over the final 180ms. No layout is animated.
	alpha := uint8(255)
	slide := int32(0)
	if elapsed < 150 {
		alpha = uint8(255 * elapsed / 150)
		slide = int32(16 * (1.0 - float64(elapsed)/150.0))
	}
	if fadeStart := t.Duration - 180; elapsed > fadeStart {
		remaining := float64(t.Duration - elapsed)
		a := uint8(255.0 * remaining / 180.0)
		if a < alpha {
			alpha = a
		}
	}

	// Wrap to at most two lines, cached per message+width.
	maxTextW := int32(420) - 16*2 - 10
	if t.lines == nil || t.linesW != maxTextW {
		t.lines = wrapToastLines(font, t.Message, maxTextW)
		t.linesW = maxTextW
		currentToast.lines = t.lines
		currentToast.linesW = t.linesW
	}
	lines := t.lines
	if len(lines) == 0 {
		lines = []string{t.Message}
	}

	lineH := int32(font.Height())
	pad := int32(16)
	textW := int32(0)
	for _, l := range lines {
		w, _, _ := font.SizeUTF8(l)
		if int32(w) > textW {
			textW = int32(w)
		}
	}
	stripW := int32(10)
	w := textW + pad*2 + stripW
	if w < 120 {
		w = 120
	}
	if w > 420 {
		w = 420
	}
	h := int32(len(lines))*lineH + pad*2
	if h < 52 {
		h = 52
	}
	if h > 96 {
		h = 96
	}

	x := screenWidth - 24 - w + slide
	y := screenHeight - HomeFooterH - 16 - h
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		y = screenHeight - bottomBarHeight(config.Scenes[currentSceneIndex].Name) - 16 - h
	}
	if x < 16 {
		x = 16
	}
	if y < 16 {
		y = 16
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Compact card: dark neutral surface, thin border, no black fill, no
	// detached shadow, no heavy outline.
	surface := ColorCard
	surface.A = alpha
	fillRoundedRect(renderer, x, y, w, h, 12, surface)
	border := ColorBorder
	border.A = alpha
	strokeRoundedRect(renderer, x, y, w, h, 12, 1, border)

	// 4px severity strip on the left edge.
	strip := toastKindColor(t.Kind)
	strip.A = alpha
	fillRoundedRect(renderer, x, y, 4, h, 2, strip)

	// Measured light text, clipped to the card bounds.
	textColor := ColorTextPrimary()
	textColor.A = alpha
	renderWithClip(renderer, x, y, w, h, func(r *sdl.Renderer) {
		textX := x + pad + stripW
		textY := y + (h-int32(len(lines))*lineH)/2
		for _, l := range lines {
			renderText(r, config, font, l, textColor, textX, textY)
			textY += lineH
		}
	})

	// Store the rendered toast rect for dismiss-on-click
	toastRect = sdl.Rect{X: x, Y: y, W: w, H: h}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
}

func renderPlaybackOverlay(renderer *sdl.Renderer, config *Config) {
	videoPlaybackMutex.Lock()
	phase := videoPlaybackPhase
	phaseAt := videoPlaybackPhaseAt
	progress := videoPlaybackProgress
	speed := videoPlaybackSpeed
	eta := videoPlaybackETA
	errMsg := videoPlaybackError
	videoPlaybackMutex.Unlock()

	if phase == "idle" {
		return
	}

	// An error must never linger on screen as a permanent black box: the
	// message is delivered through the toast system, so the overlay expires
	// itself after a short window and returns to idle.
	if phase == "error" && sdl.GetTicks64()-phaseAt > 5000 {
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "idle"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackError = ""
		videoPlaybackMutex.Unlock()
		return
	}
	// Guard against a hung download goroutine leaving the overlay forever
	// (the normal path is bounded by its own 5-minute context timeout).
	if phase == "downloading" && sdl.GetTicks64()-phaseAt > 6*60*1000 {
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "idle"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackMutex.Unlock()
		return
	}

	var title string
	switch phase {
	case "downloading":
		title = "Downloading video..."
	case "playing":
		title = "Playing..."
	case "error":
		title = "Playback error"
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	barW := int32(400)
	barH := int32(36)
	barX := (screenWidth - barW) / 2
	barY := screenHeight - 80

	// Compact status card on a dark neutral surface with a thin border —
	// never an opaque black box with a detached shadow.
	fillRoundedRect(renderer, barX, barY, barW, barH, 10, ColorCard)
	strokeRoundedRect(renderer, barX, barY, barW, barH, 10, 1, ColorBorder)

	renderText(renderer, config, font, title, sdl.Color{R: 220, G: 235, B: 255, A: 255}, barX+12, barY+8)

	if phase == "downloading" && progress > 0 {
		fillW := int32(float64(barW-24) * progress)
		if fillW > 0 {
			fillRoundedRect(renderer, barX+12, barY+28, fillW, 4, 2, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200})
		}
		status := fmt.Sprintf("%.0f%%", progress*100)
		if speed != "" {
			status += "  " + speed
		}
		if eta != "" {
			status += "  " + eta
		}
		renderText(renderer, config, font, status, sdl.Color{R: 180, G: 192, B: 210, A: 255}, barX+12, barY+38)
	}

	if phase == "error" && errMsg != "" {
		renderText(renderer, config, font, errMsg, sdl.Color{R: 248, G: 113, B: 113, A: 255}, barX+12, barY+28)
	}
}

func renderSpinner(renderer *sdl.Renderer, x, y int32, radius int32, color sdl.Color) {
	t := float64(sdl.GetTicks64()) / 1000.0
	angle := t * 4.0
	startAngle := angle
	endAngle := angle + 3.14159*1.5

	renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	for a := startAngle; a < endAngle; a += 0.15 {
		cos := float32(cosF(a))
		sin := float32(sinF(a))
		px := x + int32(cos*float32(radius))
		py := y + int32(sin*float32(radius))
		renderer.DrawPoint(px, py)
	}
}

func cosF(f float64) float64 {
	return math.Cos(f)
}
func sinF(f float64) float64 {
	return math.Sin(f)
}

// renderLoadingScreen draws a polished loading screen with the JukaHub logo,
// an animated spinner, and a progress bar. ticks is the current SDL tick
// count for animation; pass 0 for the initial static frame.
func renderLoadingScreen(renderer *sdl.Renderer, config *Config, ticks uint64) {
	renderer.SetDrawColor(15, 17, 21, 255)
	renderer.Clear()

	cx, cy := screenWidth/2, screenHeight/2

	// Animated spinner ring
	renderSpinner(renderer, cx, cy, 50, sdl.Color{R: 100, G: 180, B: 255, A: 255})

	// Inner spinner ring (smaller, slower, different color)
	if ticks > 0 {
		t := float64(ticks) / 1000.0
		innerAngle := t * -2.0
		renderer.SetDrawColor(140, 200, 255, 120)
		for a := innerAngle; a < innerAngle+math.Pi; a += 0.2 {
			px := cx + int32(float32(math.Cos(a))*30)
			py := cy + int32(float32(math.Sin(a))*30)
			renderer.DrawPoint(px, py)
		}
	}

	// Title
	if font, _ := getCachedFont(config, "big"); font != nil {
		tw, th, _ := font.SizeUTF8("JukaHub")
		renderText(renderer, config, font, "JukaHub", sdl.Color{R: 220, G: 235, B: 255, A: 255}, cx-int32(tw)/2, cy-80-int32(th)/2)
	}

	// Loading text
	if sf, _ := getCachedFont(config, "medium"); sf != nil {
		lw, lh, _ := sf.SizeUTF8("Loading...")
		renderText(renderer, config, sf, "Loading...", sdl.Color{R: 180, G: 200, B: 220, A: 255}, cx-int32(lw)/2, cy+60-int32(lh)/2)
	}

	// Animated progress bar
	if ticks > 0 {
		barW := int32(200)
		barH := int32(4)
		barX := cx - barW/2
		barY := cy + 130
		fillRoundedRect(renderer, barX, barY, barW, barH, 2, sdl.Color{R: 40, G: 50, B: 70, A: 255})
		progress := float64(ticks%3000) / 3000.0
		fillW := int32(float64(barW) * progress)
		if fillW > 0 {
			fillRoundedRect(renderer, barX, barY, fillW, barH, 2, sdl.Color{R: 100, G: 180, B: 255, A: 200})
		}
	}
}

// --- Image / Video element rendering ---
func renderImageElement(renderer *sdl.Renderer, config *Config, elem Element) {
	if elem.Image == "" && elem.Video == "" {
		return
	}
	w := getElementWidth(elem, 400)
	h := getElementHeight(elem, 300)
	fillRoundedRect(renderer, elem.X+3, elem.Y+3, w, h, 12, ShadowFill(50))
	fillRoundedRect(renderer, elem.X, elem.Y, w, h, 12, WithAlpha(ColorSurfaceCard, 230))
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 100)
	renderer.FillRect(&sdl.Rect{X: elem.X, Y: elem.Y, W: w, H: 1})

	tex := loadThumbnail(renderer, elem.Image)
	if tex == nil && elem.Video != "" {
		tex = loadThumbnail(renderer, elem.Video)
	}
	if tex != nil {
		renderer.Copy(tex, nil, &sdl.Rect{X: elem.X + 4, Y: elem.Y + 4, W: w - 8, H: h - 8})
	} else {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, "No preview", sdl.Color{R: 150, G: 150, B: 150, A: 255}, elem.X+16, elem.Y+h/2-10)
		}
	}
}

func renderVideoElement(renderer *sdl.Renderer, config *Config, elem Element) {
	if playerPhase() == "idle" {
		return
	}
	RenderVideoOverlay(renderer, config, elem)
}

// --- Trigger handling ---
func handleTrigger(renderer *sdl.Renderer, config *Config, element Element) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[trigger] recovered panic: %v", r)
		}
	}()
	if element.Trigger == "" {
		return
	}
	PlayActivateSound()
	// Normalize "change_scene:<Scene>" (used by jukaconfig.json and the web
	// editor) into the internal "change_scene" type + TriggerTarget form.
	if strings.HasPrefix(element.Trigger, "change_scene:") {
		element.TriggerTarget = strings.TrimPrefix(element.Trigger, "change_scene:")
		element.Trigger = "change_scene"
	}
	// Commit any in-progress text input so triggers (e.g. custom_link) see it.
	updateInputVariable(config)
	switch element.Trigger {
	case "change_scene":
		target := element.TriggerTarget
		for i, scene := range config.Scenes {
			if scene.Name == target {
				changeSceneTo(config, i)
				return
			}
		}
		log.Printf("change_scene: scene not found: %s", target)
	case "yt_search":
		go executeYouTubeSearch(config, element.TriggerTarget, element.TriggerValue, snapshotVars(config))
	case "youtube_search":
		q := ""
		if v, ok := config.Variables.Custom["search_query"].(string); ok {
			q = strings.TrimSpace(v)
		}
		if q == "" {
			publishCustom("search_error", "Type a search query first.")
			return
		}
		cmd := ytSearchCmd(config)
		go executeYouTubeSearch(config, cmd, "search_results", snapshotVars(config))
	case "youtube_smart":
		q := ""
		if v, ok := config.Variables.Custom["search_query"].(string); ok {
			q = strings.TrimSpace(v)
		}
		if q == "" {
			publishCustom("search_error", "Type a search query or paste a link first.")
			return
		}
		if strings.Contains(q, "youtube.com") || strings.Contains(q, "youtu.be") {
			playVideoURL(config, q)
			return
		}
		cmd := ytSearchCmd(config)
		go executeYouTubeSearch(config, cmd, "search_results", snapshotVars(config))
	case "youtube_trending":
		go fetchTrendingVideos(config, "search_results", snapshotVars(config))
	case "youtube_play":
		videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
		if ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoInfo(config, videos[focusedResultIndex])
			return
		}
		// "Play Link" is also the way to play whatever was typed into the
		// scene's URL field: with no highlighted result the button used to do
		// nothing at all, which read as a dead button.
		if url := sceneInputURL(config, config.Scenes[currentSceneIndex]); url != "" {
			log.Printf("youtube_play: playing the typed link")
			playVideoURL(config, url)
			return
		}
		log.Printf("youtube_play: no focused video and no link typed")
		ShowToast("Type a video link or pick a result first", ToastKindInfo)
	case "music_player":
		// Opens the audio player on the folder the Files scene is browsing.
		dir := feCurrentPath(config)
		if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
			config.Scenes[currentSceneIndex].Name != "FileExplorer" {
			dir = musicCurrentDir()
		}
		openAudioPlayerBrowser(config, dir)
	case "play_video_from_var":
		url, ok := config.Variables.Custom[element.TriggerTarget].(string)
		if !ok {
			if videos, ok := config.Variables.Custom[element.TriggerTarget].([]VideoInfo); ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
				playVideoInfo(config, videos[focusedResultIndex])
				return
			} else {
				log.Printf("Cannot play: %s not found", element.TriggerTarget)
				return
			}
		}
		playVideoURL(config, url)
	case "play_focused":
		videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
		if ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoInfo(config, videos[focusedResultIndex])
		} else {
			log.Printf("play_focused: no focused video to play")
		}
	case "exit":
		// Play the goodbye sequence and quit gracefully via the main loop,
		// which persists favorites and runs the deferred cleanup on the way
		// out (a hard os.Exit would skip that cleanup).
		requestAppExit()
	case "external_app":
		if element.ExternalAppPath == "" {
			log.Printf("external_app trigger has no path")
			break
		}
		// External apps are powerful: they can run arbitrary binaries, read/write
		// files, and chain onto the rest of the system. Keep their use explicit,
		// console-driven, and accountable. Do not auto-launch them from automated
		// UI flows or background goroutines.
		if err := runExplicitExternalApp(element.ExternalAppPath); err != nil {
			LogSceneOp(config.Scenes[currentSceneIndex].Name, "external_app").Error("failed", "err", err)
		}
	case "fe_list":
		feListDirectory(config)
	case "fe_up":
		feUp(config)
	case triggerAppRefresh:
		scanInstalledApps(config)
		focusedFileIndex = 0
		showToast("App list refreshed", ToastSuccess())
	case triggerAppOpenFolder:
		openAppsFolder()
	case "ip_stream":
		url := strings.TrimSpace(inputVariableValue(config, "stream_url"))
		if url == "" {
			showToast("Enter a stream URL first.", ToastError())
		} else {
			StartEmbeddedPlayback(config, url)
			currentPlaybackURL = url
			addRecentVideo(VideoInfo{URL: url, Title: url})
		}
	case "custom_link":
		url := ""
		if v, ok := config.Variables.Custom["custom_link_url"].(string); ok {
			url = strings.TrimSpace(v)
		}
		if url == "" && activeElementIndex != -1 && activeSceneIndex >= 0 && activeSceneIndex < len(config.Scenes) {
			if activeElementIndex < len(config.Scenes[activeSceneIndex].Elements) {
				if e := config.Scenes[activeSceneIndex].Elements[activeElementIndex]; e.Variable == "custom_link_url" {
					url = strings.TrimSpace(inputTextBuffer)
				}
			}
		}
		if url != "" {
			playSmartURL(config, url)
		} else {
			log.Printf("custom_link: no URL in custom_link_url")
		}
	case "iptv_load":
		loadIPTV(config)
	case "podcast_load":
		if url, ok := config.Variables.Custom["podcast_url"].(string); ok && strings.TrimSpace(url) != "" {
			loadPodcastURL(config, url)
		} else {
			loadPodcasts(config)
		}
	case "attach_image":
		// Set config variable to indicate we're in image attach mode.
		config.Variables.Custom["chat_attach_image"] = "1"
		showToast("Select an image to attach to your message", ToastInfo())
		return
	case "tick":
		fetchTickers(config)
	case "add_ticker":
		sym := ""
		if v, ok := config.Variables.Custom["custom_ticker"].(string); ok {
			sym = strings.TrimSpace(v)
		}
		if sym != "" {
			var list []string
			if existing, ok := config.Variables.Custom["custom_tickers"].([]string); ok {
				list = append(list, existing...)
			}
			// Avoid duplicates (case-insensitive).
			dup := false
			for _, e := range list {
				if strings.EqualFold(e, sym) {
					dup = true
					break
				}
			}
			if !dup {
				list = append(list, sym)
				config.Variables.Custom["custom_tickers"] = list
			}
			// Clear the input for the next entry.
			config.Variables.Custom["custom_ticker"] = ""
			fetchTickers(config)
		}
	case "clear_tickers":
		config.Variables.Custom["custom_tickers"] = []string{}
		config.Variables.Custom["custom_ticker"] = ""
		fetchTickers(config)
	case "netspeed_run":
		runNetSpeed(config)
	case "benchmark_run":
		runBenchmark(config)
	case "benchmark_random":
		runRandomBenchmark(config)
	case "terminal_run":
		if cmd, ok := config.Variables.Custom["terminal_cmd"].(string); ok {
			runTerminal(config, cmd)
		}
	case "emulator_run":
		if cmd, ok := config.Variables.Custom["emulator_cmd"].(string); ok {
			runTerminal(config, cmd)
		}
	case "cron_load":
		loadCron(config)
	case "cron_startup":
		publishCustom(cronStartupVar, loadStartupItems())
	case "hw_load":
		publishCustom("hw_text", getHardwareInfo())
	case "log_export":
		exportLogs()
	case "log_clear":
		appLog.Clear()
	case "set_variable":
		if element.VariableChange != "" {
			val := element.TriggerValue
			switch element.VariableChange {
			case "fullscreen":
				if b, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
					if mainWindow != nil {
						if b {
							mainWindow.SetFullscreen(1)
						} else {
							mainWindow.SetFullscreen(0)
						}
					}
					config.Variables.Fullscreen = b
				}
			case "buttonColor", "labelColor":
				applyColorVar(config, element.VariableChange, val)
			case "weatherUnit":
				config.Variables.WeatherUnit = strings.TrimSpace(val)
			default:
				config.Variables.Custom[element.VariableChange] = val
			}
		}
		// Persist theme/settings changes immediately
		syncVariableOverrides(config)
	case "fav_tab":
		if v, ok := config.Variables.Custom[element.TriggerValue].(string); ok {
			switch strings.ToLower(v) {
			case "videos", "0":
				favoritesCurrentTab = 0
			case "recent", "1":
				favoritesCurrentTab = 1
			case "files", "2":
				favoritesCurrentTab = 2
			case "iptv", "3":
				favoritesCurrentTab = 3
			}
		} else if element.TriggerValue != "" {
			if idx, err := strconv.Atoi(element.TriggerValue); err == nil && idx >= 0 && idx <= 3 {
				favoritesCurrentTab = idx
			}
		}
		favoritesFocusIndex = 0
	case "fav_play":
		items := getCurrentFavorites()
		if favoritesFocusIndex >= 0 && favoritesFocusIndex < len(items) {
			items[favoritesFocusIndex].Play(config)
		}
	case "fav_remove":
		items := getCurrentFavorites()
		if favoritesFocusIndex >= 0 && favoritesFocusIndex < len(items) {
			removeFavoriteAt(favoritesCurrentTab, favoritesFocusIndex)
			saveFavorites()
			items2 := getCurrentFavorites()
			if favoritesFocusIndex >= len(items2) && favoritesFocusIndex > 0 {
				favoritesFocusIndex--
			}
			showToast("Removed from favorites", ToastError())
		}
	case "canvas_run":
		canvasCode = inputTextBuffer
		if canvasSurface != nil {
			canvasSurface.Free()
			canvasSurface = nil
		}
		canvasSurface = executeCanvasCode(canvasCode)
	case "jukaland_init":
		jukalandInited = false
		initJukaLand()
	case "canvas_clear":
		canvasCode = ""
		inputTextBuffer = ""
		if canvasSurface != nil {
			canvasSurface.Free()
			canvasSurface = nil
		}
	case "unit_convert":
		if unitInputValue != "" {
			var val float64
			if _, err := fmt.Sscanf(unitInputValue, "%f", &val); err == nil {
				if res, ok := convertUnit(val, unitFrom, unitTo, unitCategory); ok {
					if unitCategory == "temperature" {
						if unitTo == "C" || unitTo == "c" {
							unitResult = fmt.Sprintf("%.2f °C", res)
						} else if unitTo == "F" || unitTo == "f" {
							unitResult = fmt.Sprintf("%.2f °F", res)
						} else {
							unitResult = fmt.Sprintf("%.2f K", res)
						}
					} else {
						unitResult = fmt.Sprintf("%.4g", res)
					}
				} else {
					unitResult = "Error"
				}
			} else {
				unitResult = "Invalid number"
			}
		}
	case "unit_swap":
		unitFrom, unitTo = unitTo, unitFrom
		unitResult = ""
	case "unit_category":
		unitCategory = element.TriggerValue
		unitFrom = ""
		unitTo = ""
		unitInputValue = ""
		unitResult = ""
	case "chat_send":
		if inputTextBuffer != "" {
			sendChatMessage(chatDisplayName(), inputTextBuffer)
			inputTextBuffer = ""
		}
	case "discord_connect":
		go func() {
			if err := discordConnect(); err != nil {
				discordStatus = "Discord: " + err.Error()
			} else {
				discordStatus = "Discord: connected"
			}
			saveUserConfig(userConfigFromVariables(config))
		}()
	case "chat_clear":
		chatMutex.Lock()
		chatMessages = []ChatMessage{}
		chatMutex.Unlock()
		saveChatMessages()
	case "groq_chat":
		if inputTextBuffer != "" {
			sendGroqChatMessage(inputTextBuffer)
			inputTextBuffer = ""
		}
	case "shorts_fetch":
		fetchYouTubeShorts(config, "shorts_list", snapshotVars(config))
	case "shorts_play":
		shortsMutex.Lock()
		videos := shortsList
		shortsMutex.Unlock()
		if currentShortIdx >= 0 && currentShortIdx < len(videos) {
			playVideoInfo(config, videos[currentShortIdx])
		}
	case "shorts_refresh":
		fetchYouTubeShorts(config, "shorts_list", snapshotVars(config))
		currentShortIdx = -1
	case "open_image":
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			entry := entries[focusedFileIndex]
			if isImageExtension(filepath.Ext(entry.Path)) {
				imagePath, err := resolveExplorerEntry(config, entry.Path)
				if err != nil {
					showToast("Cannot open image: "+err.Error(), ToastError())
					break
				}
				openImageViewer(config, imagePath)
			} else {
				showToast("Select an image file first", ToastError())
			}
		}
	case "send_to_chat":
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			entry := entries[focusedFileIndex]
			ext := strings.ToLower(filepath.Ext(entry.Path))
			if isImageExtension(ext) {
				sendChatMessage(chatDisplayName(), "[Image: "+entry.Name+"]")
			} else if ext == ".txt" || ext == ".md" || ext == ".json" || ext == ".js" || ext == ".html" {
				// The row comes from a dynamic list, not from the filesystem:
				// resolve it under the explorer root before reading.
				resolved, resolveErr := resolveExplorerEntry(config, entry.Path)
				if resolveErr != nil {
					sendChatMessage("System", "Cannot read file: "+resolveErr.Error())
					break
				}
				data, err := os.ReadFile(resolved)
				if err != nil {
					sendChatMessage("System", "Failed to read file: "+err.Error())
				} else {
					content := truncateRunesSuffix(string(data), 500, "...")
					sendChatMessage(chatDisplayName(), content)
				}
			} else {
				sendChatMessage(chatDisplayName(), "Sent file: "+entry.Name)
			}
		}
	case "canvas_open":
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			entry := entries[focusedFileIndex]
			openFileInCanvasSandbox(entry.Path)
		}
	case "onboarding_replay":
		onboardingStart()
	case "save_config":
		// Apply any color variable inputs from the current scene before saving.
		if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			for _, e := range config.Scenes[currentSceneIndex].Elements {
				if e.Type == "input" && e.Variable != "" {
					if v, ok := config.Variables.Custom[e.Variable].(string); ok && v != "" {
						applyColorVar(config, e.Variable, v)
					}
				}
			}
		}
		syncVariableOverrides(config)
		// Apply the Fullscreen setting live instead of only on restart.
		if mainWindow != nil {
			if config.Variables.Fullscreen {
				mainWindow.SetFullscreen(1)
			} else {
				mainWindow.SetFullscreen(0)
			}
		}
		// Apply theme colors live.
		SetAccentColor(resolveColor(config, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255}))
		saveConfig(config)
		showToast("Settings saved", ToastInfo())
	case "theme_preset":
		if element.TriggerTarget != "" {
			ApplyThemePreset(config, element.TriggerTarget)
		}
	case "plugin_action":
		if element.TriggerTarget != "" {
			result := ExecutePluginAction(element.TriggerTarget, element.TriggerValue, snapshotVars(config))
			showToast(result, ToastInfo())
		}
	case "plugin_refresh":
		LoadPlugins(config)
		var list []string
		for name, p := range plugins {
			status := "enabled"
			if !p.Enabled {
				status = "disabled"
			}
			list = append(list, fmt.Sprintf("- %s v%s (%s) [%s]", name, p.Version, p.Author, status))
		}
		if len(list) == 0 {
			list = []string{"No plugins found. Drop .py/.sh scripts into plugins/"}
		}
		config.Variables.Custom["plugin_list"] = strings.Join(list, "\n")
	case "packages_refresh":
		go fetchPackages(config)
	case "browser_back":
		browserBack(config)
	case "browser_forward":
		browserForward(config)
	case "browser_reload":
		browserReload(config)
	case "browser_repo":
		browserOpenURL(config, browserReposURL)
	case "browser_issues":
		browserOpenURL(config, browserIssuesURL)
	case "browser_new_issue":
		browserOpenURL(config, browserNewIssueURL)
	case "textbrowser_refresh":
		textBrowserRefresh(config, element)
		if element.AutoRefresh && textBrowserElementSource(element) != textBrowserSourceWeb {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_system":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = browseSystemInfo(element)
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_zeroconf":
		tbVar := findTextBrowserVariable(config)
		go func(v string, el Element) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[textbrowser] zeroconf goroutine recovered: %v", r)
					publishCustom(v, fmt.Sprintf("Zeroconf error: %v", r))
				}
			}()
			publishCustom(v, browseZeroconfServicesWithTimeout(4*time.Second))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(tbVar, element)
		textBrowserLastUpdate = time.Now().Unix()
	case "textbrowser_json":
		tbVar := findTextBrowserVariable(config)
		go func(v string, el Element) {
			publishCustom(v, browseJSONContent(el))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(tbVar, element)
		textBrowserLastUpdate = time.Now().Unix()
	case "textbrowser_processes":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getProcessTree()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_network":
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[textbrowser] network trigger recovered: %v", r)
					tbVar := findTextBrowserVariable(config)
					config.Variables.Custom[tbVar] = fmt.Sprintf("Network error: %v", r)
				}
			}()
			var sb strings.Builder
			ifaces, err := net.Interfaces()
			if err == nil {
				for _, iface := range ifaces {
					if iface.Flags&net.FlagLoopback != 0 {
						continue
					}
					addrs, _ := iface.Addrs()
					var ips []string
					for _, a := range addrs {
						if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
							ips = append(ips, ipnet.IP.String())
						}
					}
					sb.WriteString(fmt.Sprintf("%s: %s (up=%v)\n", iface.Name, strings.Join(ips, ", "), iface.Flags&net.FlagUp != 0))
				}
			}
			if sb.Len() == 0 {
				sb.WriteString("No active network interfaces\n")
			}
			tbVar := findTextBrowserVariable(config)
			config.Variables.Custom[tbVar] = sb.String()
			textBrowserLastUpdate = time.Now().Unix()
			if element.AutoRefresh {
				startTextBrowserAutoRefresh(config, element)
			}
		}()
	case "textbrowser_temps":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getSystemTemperature()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_services":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getRunningServices()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_files":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = scanLocalFiles("/media")
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_copy":
		tbVar := findTextBrowserVariable(config)
		if text, ok := config.Variables.Custom[tbVar].(string); ok && text != "" {
			if IsWindows() {
				exec.Command("cmd", "/c", "echo", text).Run()
			} else {
				exec.Command("pbcopy").Run()
			}
			showToast("Copied to clipboard", ToastInfo())
		}
	case "textbrowser_clear":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = ""
		textBrowserScrollY = 0
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
	case "cache_clear":
		if db, err := cacheOpen("jukahub.cache"); err == nil {
			cacheClear(db)
			db.Close()
			showToast("Cache cleared", ToastInfo())
		}
	case "tools_open":
		openToolsPanel()
	case "companion_open":
		toggleCompanionOverlay()
	case "processes_open":
		openProcessPanel()
	case "wifi_open":
		openWiFiPanel()
	case "ip_open":
		openIPStreamPanel()
	case "ip_add":
		ipPanelSaveCurrentLink(config)
	case "ip_fetch":
		ipPanelImport()
	case "back", "go_back":
		// Body-level Back affordances resolve to the same navigation as the
		// header pill, B and Escape.
		goBackScene(config)
	case "ftp_toggle":
		toggleFTPServer()
	case "ftp_start":
		if !ftpSrv.Running() {
			toggleFTPServer()
		}
	case "ftp_stop":
		if ftpSrv.Running() {
			ftpSrv.Stop()
			showToast("FTP server stopped", ToastInfo())
		}
	case "ssh_toggle":
		toggleSSHServer()
	case "ssh_start":
		if !sshSrv.Running() {
			toggleSSHServer()
		}
	case "ssh_stop":
		if sshSrv.Running() {
			sshSrv.Stop()
			showToast("SSH/SFTP server stopped", ToastInfo())
		}
	default:
		if strings.HasPrefix(element.Trigger, "http://") || strings.HasPrefix(element.Trigger, "https://") {
			go func(url string) {
				var cmd *exec.Cmd
				if IsWindows() {
					cmd = exec.Command("cmd", "/c", "start", "", url)
				} else {
					cmd = exec.Command("xdg-open", url)
				}
				if err := cmd.Run(); err != nil {
					log.Printf("open url error: %v", err)
				}
			}(element.Trigger)
		} else {
			log.Printf("Unhandled trigger: %s", element.Trigger)
		}
	}
}

func findFirstSelectableElement(scene SceneConfig) int {
	// Prefer interactive elements (buttons, inputs) over display elements.
	for i, e := range scene.Elements {
		if e.Type == "button" || e.Type == "input" || e.Type == "searchresults" || e.Type == "dynamiclist" || e.Type == "favorites" || e.Type == "recent" {
			return i
		}
	}
	// Fallback: select the first focusable element even if it's display-only
	// (textbrowser, shortslist, packagelist) so arrow navigation still works.
	for i, e := range scene.Elements {
		if e.Type == "textbrowser" || e.Type == "shortslist" || e.Type == "packagelist" {
			return i
		}
	}
	return -1
}

// changeSceneTo changes to a specific scene index with fade transition.
// pushSceneHistory records the scene being left so B/Esc can return to it.
// Consecutive duplicates are collapsed and the stack is capped.
func pushSceneHistory(idx int) {
	if idx < 0 {
		return
	}
	if len(sceneHistory) == 0 || sceneHistory[len(sceneHistory)-1] != idx {
		sceneHistory = append(sceneHistory, idx)
		if len(sceneHistory) > 16 {
			sceneHistory = sceneHistory[len(sceneHistory)-16:]
		}
	}
}

// popSceneHistory returns the most recent scene index, if any.
func popSceneHistory() (int, bool) {
	if len(sceneHistory) == 0 {
		return -1, false
	}
	last := sceneHistory[len(sceneHistory)-1]
	sceneHistory = sceneHistory[:len(sceneHistory)-1]
	return last, true
}

// normalizeHandheldBackButton folds the auxiliary buttons some handheld
// firmwares use for their B face button into BACK, so every menu closes with
// the same button regardless of the raw SDL mapping.
func normalizeHandheldBackButton(button uint8, handheld bool) uint8 {
	if !handheld {
		return button
	}
	if button == sdl.CONTROLLER_BUTTON_GUIDE {
		return sdl.CONTROLLER_BUTTON_BACK
	}
	return button
}

// homeSceneIndex returns the scene that acts as Home: the one using the home
// layout first, then a scene called Main or Home, then the first scene. Older
// configs are not guaranteed to call the entry scene "Main", and goBackScene
// used to fail silently in that case.
func homeSceneIndex(config *Config) int {
	if config == nil || len(config.Scenes) == 0 {
		return -1
	}
	for i, scene := range config.Scenes {
		if scene.Layout == "home" {
			return i
		}
	}
	for _, name := range []string{"Main", "Home"} {
		for i, scene := range config.Scenes {
			if scene.Name == name {
				return i
			}
		}
	}
	return 0
}

// goBackScene returns to the previous scene in history, falling back to Home.
// B / Escape on any non-home scene routes through here; it never terminates
// the application. Stale history entries that point at the current scene are
// skipped instead of silently consuming the press, and a busy transition keeps
// the entry so the next press still works.
func goBackScene(config *Config) {
	PlayBackSound()
	for {
		prev, ok := popSceneHistory()
		if !ok {
			break
		}
		if prev < 0 || prev >= len(config.Scenes) || prev == currentSceneIndex {
			continue
		}
		switchScene(config, prev, false)
		if pendingSceneIndex == prev {
			return
		}
		// A scene transition was already running; keep the destination for the
		// next press instead of dropping it.
		pushSceneHistory(prev)
		return
	}
	if home := homeSceneIndex(config); home >= 0 && home != currentSceneIndex {
		switchScene(config, home, false)
	}
}

// switchScene is the low-level scene transition. When record is true the scene
// being left is pushed onto the history stack (used by changeSceneTo).
func switchScene(config *Config, targetIdx int, record bool) {
	if transitionPhase != "none" {
		return
	}
	if targetIdx < 0 || targetIdx >= len(config.Scenes) || targetIdx == currentSceneIndex {
		return
	}
	if record {
		pushSceneHistory(currentSceneIndex)
	}
	// Persist focus for the current scene before leaving it.
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		focusEngine.Persist(config.Scenes[currentSceneIndex].Name)
	}
	// Remember the last focused home card so returning to Home restores it.
	if homeLayoutActive && homeLayout != nil {
		homeLayout.Focus.Remember()
	}
	pendingSceneIndex = targetIdx
	transitionPhase = "fade-out"
	sceneFadeStart = sdl.GetTicks64()
	sceneFadeAlpha = 0
	// Top-bar overlays belong to the scene that opened them; leaving must not
	// float the Start menu or the notification center over the next one.
	startMenuDismiss()
	notifDismiss()
	dismissWeatherPopover()
	// The browser link picker belongs to the page that opened it; it must not
	// float over the next scene.
	browserLinkPanel = browserLinkPanelState{}
	browserLinkRowHits = nil
	browserLinkPanelRect = sdl.Rect{}
}

func changeSceneTo(config *Config, targetIdx int) {
	switchScene(config, targetIdx, true)
}

// completeSceneTransition finishes the scene switch after fade-out and starts fade-in.
func completeSceneTransition(config *Config) {
	currentSceneIndex = pendingSceneIndex

	// Build focus graph for the new scene and restore persisted focus.
	scene := config.Scenes[currentSceneIndex]
	// Home focus is managed by the dedicated controller; restore the last
	// focused card when returning to the home scene.
	homeLayoutActive = scene.Layout == "home"
	if homeLayoutActive && homeLayout != nil {
		homeLayout.Focus.Restore()
	}
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	selectedButtonIndex = focusEngine.ElementIndex()
	if selectedButtonIndex < 0 {
		selectedButtonIndex = findFirstSelectableElement(scene)
	}

	focusedResultIndex = -1
	focusedFileIndex = 0
	scrollY = 0
	textBrowserScrollY = 0
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
	hoveredButtonIndex = -1
	pressedButtonIndex = -1

	stopAllTextBrowserAutoRefresh()
	refreshSceneTextBrowsers(config)
	if scene.Name == browserSceneName {
		browserEnsureHome(config)
	}

	// The Hardware screen shows a live snapshot: refresh it on entry so the
	// process list is not stale from the last visit (or empty on first open).
	if scene.Name == "Hardware" {
		go func() { publishCustom("hw_text", getHardwareInfo()) }()
	}

	if sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
		queryVar := ""
		for _, e := range config.Scenes[currentSceneIndex].Elements {
			if e.Type == "input" {
				queryVar = e.Variable
				break
			}
		}
		if queryVar != "" {
			if _, ok := config.Variables.Custom[queryVar]; !ok {
				config.Variables.Custom[queryVar] = ""
			}
		}
		srElem, _ := sceneSearchResultsElement(config.Scenes[currentSceneIndex])
		if _, ok := config.Variables.Custom[srElem.Variable]; !ok {
			go fetchTrendingVideos(config, srElem.Variable, snapshotVars(config))
		}
	}

	if dl, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok {
		switch dl.Variable {
		case "fe_entries":
			feListDirectory(config)
		case "iptv_entries":
			loadIPTV(config)
		case "podcast_entries":
			go loadPodcasts(config)
		case appsEntriesVar:
			scanInstalledApps(config)
		case "plugin_list":
			LoadPlugins(config)
			var list []string
			for name, p := range plugins {
				status := "enabled"
				if !p.Enabled {
					status = "disabled"
				}
				list = append(list, fmt.Sprintf("- %s v%s (%s) [%s]", name, p.Version, p.Author, status))
			}
			if len(list) == 0 {
				list = []string{"No plugins found. Drop .py/.sh scripts into plugins/"}
			}
			config.Variables.Custom["plugin_list"] = strings.Join(list, "\n")
		}
	}
	// Initialize alarm UI when entering the Alarm scene.
	if config.Scenes[currentSceneIndex].Name == "Alarm" {
		alarmInitUI()
	}
	// Initialize terminal when entering the Terminal scene.
	if config.Scenes[currentSceneIndex].Name == "Terminal" {
		termInit()
	}
	// Initialize emulator when entering the Emulator scene.
	if config.Scenes[currentSceneIndex].Name == "Emulator" {
		// Emulator scene initialized - label displays launcher info
	}
	// Initialize dashboard when entering the Dashboard scene.
	if config.Scenes[currentSceneIndex].Name == "Dashboard" {
		dashInit()
	}
	// Initialize dictionary when entering.
	if config.Scenes[currentSceneIndex].Name == "Dictionary" {
		dictInit()
	}
	// Initialize PDF reader when entering.
	if config.Scenes[currentSceneIndex].Name == "PDFReader" {
		pdfInit()
	}
	// Prepare the audio player when entering. An active playlist is kept, so
	// coming back from Files does not restart the song.
	if config.Scenes[currentSceneIndex].Name == "MusicPlayer" {
		musicSceneEnter()
	}
	// Initialize notes when entering.
	if config.Scenes[currentSceneIndex].Name == "Notes" {
		notesInit()
	}
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "textlog" && e.Variable == "weather_screen_text" {
			go func() {
				fetchWeatherOnce()
				publishCustom("weather_screen_text", weatherScreenText(config))
			}()
		}
		if e.Type == "chat" {
			loadChatMessages()
			go func() {
				if err := discordConnect(); err != nil {
					log.Printf("[discord] connect failed: %v", err)
					// "not configured" is a setup state, not something to
					// interrupt the user about every time chat opens.
					if !strings.HasPrefix(err.Error(), "discord not configured") && discordStatus != "" {
						showToast(discordStatus, ColorToastWarn)
					}
				}
			}()
		}
	}
	// Auto-focus the chat composer when entering the Chat scene. The terminal
	// draws its own prompt row (see terminal.go) and must not route typing
	// through the chat element its config still carries.
	if config.Scenes[currentSceneIndex].Name == "Chat" {
		for i, e := range config.Scenes[currentSceneIndex].Elements {
			if e.Type == "chat" {
				selectedButtonIndex = i
				chatInputActive = true
				sdl.StartTextInput()
				break
			}
		}
	}
	// Auto-fetch YouTube Shorts when entering the Shorts scene.
	if config.Scenes[currentSceneIndex].Name == "Shorts" {
		shortsMutex.Lock()
		hasShorts := len(shortsList) > 0
		shortsMutex.Unlock()
		if !hasShorts {
			go fetchYouTubeShorts(config, "shorts_list", snapshotVars(config))
		}
	}
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "favorites" {
			favoritesFocusIndex = 0
			break
		}
	}
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "packagelist" {
			if _, ok := config.Variables.Custom["packages_list"]; !ok {
				go fetchPackages(config)
			}
			break
		}
	}

	transitionPhase = "fade-in"
	sceneFadeStart = sdl.GetTicks64()
	sceneFadeAlpha = 0
}

// Modified changeScene to auto-fetch trending and initialize search_query
func changeScene(config *Config, direction int) {
	target := currentSceneIndex + direction
	if target < 0 || target >= len(config.Scenes) {
		return
	}
	changeSceneTo(config, target)
	// Brief toast naming the destination scene so a scene switch is visible
	// even without a permanent menu bar.
	if scene := config.Scenes[target].Name; scene != "" {
		ShowToast(scene, ToastKindSwitch)
	}
}

// Grid navigation functions
func moveGridSelection(config *Config, dx, dy int) {
	videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
	if !ok || len(videos) == 0 {
		return
	}
	if focusedResultIndex < 0 {
		focusedResultIndex = 0
	}
	srElem, ok := sceneSearchResultsElement(config.Scenes[currentSceneIndex])
	if !ok {
		return
	}
	cols, rows := gridColsRows(srElem)
	colsI := int(cols)
	rowsI := int(rows)
	maxItems := colsI * rowsI
	if len(videos) < maxItems {
		maxItems = len(videos)
	}
	if maxItems == 0 {
		return
	}
	row := focusedResultIndex / colsI
	col := focusedResultIndex % colsI
	row += dy
	col += dx
	// Wrap around
	if row < 0 {
		row = rowsI - 1
	} else if row >= rowsI {
		row = 0
	}
	if col < 0 {
		col = colsI - 1
	} else if col >= colsI {
		col = 0
	}
	newIdx := row*colsI + col
	if newIdx >= maxItems {
		// Clamp to last valid index
		newIdx = maxItems - 1
	}
	focusedResultIndex = newIdx
	// Resolve the highlighted card's stream in the background while the user
	// keeps moving: pressing Play then skips the yt-dlp round-trip.
	prefetchFocusedResult(config, videos)
}

// handleSearchResultsAction applies a semantic action to the current
// search-results grid. It is used by both keyboard and controller input so
// the two paths stay in sync.
func handleSearchResultsAction(action Action, config *Config) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	scene := config.Scenes[currentSceneIndex]
	if !sceneHasSearchResults(scene) {
		return
	}

	switch action {
	case ActionNavigateUp:
		// Leaving the grid upward returns to the controls row (input + the
		// Search/Play/Trending buttons) instead of wrapping to the last row.
		if srElem, ok := sceneSearchResultsElement(scene); ok {
			cols, _ := gridColsRows(srElem)
			row := -1
			if cols > 0 {
				row = focusedResultIndex / int(cols)
			}
			if focusedResultIndex <= 0 || row <= 0 {
				moveSelection(config, ActionNavigateUp)
				return
			}
		}
		moveGridSelection(config, 0, -1)
	case ActionNavigateDown:
		moveGridSelection(config, 0, 1)
	case ActionNavigateLeft:
		moveGridSelection(config, -1, 0)
	case ActionNavigateRight:
		moveGridSelection(config, 1, 0)
	case ActionConfirm:
		if focusedResultIndex < 0 {
			focusedResultIndex = 0
		}
		if videos, ok := getSceneVideos(config, scene); ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoInfo(config, videos[focusedResultIndex])
		}
	case ActionBack:
		// Leave the grid and move back to the previous element.
		moveSelection(config, ActionNavigateUp)
	}
}

// activateChromeFocus presses the focused chrome button: the back pill leaves
// the scene, the status clock toggles the same calendar popover Home shows, the
// Apps pill button toggles the app menu and the notification bell toggles the
// notification center. It reports whether a chrome button held the focus.
func activateChromeFocus(config *Config) bool {
	switch focusEngine.ElementIndex() {
	case ChromeBackIndex:
		PlayActivateSound()
		goBackScene(config)
		return true
	case ChromeClockIndex:
		PlayActivateSound()
		toggleCalendarPopover()
		return true
	case ChromeStartIndex:
		// startMenuToggle plays its own activation sound.
		if tb.startOpen {
			startMenuDismiss()
		} else {
			startMenuToggle()
		}
		return true
	case ChromeWeatherIndex:
		PlayActivateSound()
		toggleWeatherPopover()
		return true
	case ChromeBellIndex:
		// notifToggle plays its own activation sound.
		notifToggle()
		return true
	case ChromeUpdateIndex:
		// Same one-press install as the home header chip, so the offer behaves
		// identically wherever it is drawn.
		PlayActivateSound()
		startHeaderUpdate()
		return true
	}
	return false
}

// handleChromeBarClick activates a top-bar chrome button under the pointer:
// the back pill leaves the scene, the status clock toggles the calendar
// popover (exactly like Home). It reports whether a chrome button was hit.
func handleChromeBarClick(mx, my int32, config *Config) bool {
	// The update badge sits in the status cluster, left of the bell, and starts
	// the install directly — the same action as the D-pad and the home chip.
	if updatePrompt.badge.W > 0 && pointInRect(mx, my, updatePrompt.badge) {
		PlayActivateSound()
		startHeaderUpdate()
		return true
	}
	if sceneTitleBackRect.W > 0 && mx >= sceneTitleBackRect.X && mx <= sceneTitleBackRect.X+sceneTitleBackRect.W &&
		my >= sceneTitleBackRect.Y && my <= sceneTitleBackRect.Y+sceneTitleBackRect.H {
		goBackScene(config)
		return true
	}
	if statusClockRect.W > 0 && mx >= statusClockRect.X && mx <= statusClockRect.X+statusClockRect.W &&
		my >= statusClockRect.Y && my <= statusClockRect.Y+statusClockRect.H {
		toggleCalendarPopover()
		return true
	}
	if weatherStatusRect.W > 0 && pointInRect(mx, my, weatherStatusRect) {
		toggleWeatherPopover()
		return true
	}
	return false
}

// handleChromeKey processes input while a chrome button (the back pill, the
// status clock, the Apps pill or the notification bell) is focused
// on a non-home scene. Chrome consumes every key while focused so the element
// dispatch cannot steal the focus back.
func handleChromeKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || homeLayoutActive {
		return false
	}
	// The clock popover floats above every scene, so Escape closes it before
	// the focused chrome button or the scene sees the key. An open app menu or
	// notification center is modal and owns Escape.
	if homeCalendarOpen && !tb.startOpen && !nc.open &&
		(e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b) {
		homeCalendarOpen = false
		return true
	}
	switch chromeTier(focusEngine.ElementIndex()) {
	case 1, 2:
	default:
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		moveSelection(config, ActionNavigateUp)
	case sdl.K_DOWN:
		moveSelection(config, ActionNavigateDown)
	case sdl.K_LEFT:
		moveSelection(config, ActionNavigateLeft)
	case sdl.K_RIGHT:
		moveSelection(config, ActionNavigateRight)
	case sdl.K_RETURN, sdl.K_SPACE:
		activateChromeFocus(config)
	case sdl.K_ESCAPE, sdl.K_b:
		// Back over an open top-bar overlay closes it first.
		if tb.startOpen {
			startMenuDismiss()
		} else if nc.open {
			notifDismiss()
		} else {
			goBackScene(config)
		}
	}
	return true
}

// handleChromeOverlayKey routes input to an open top-bar overlay: the app menu
// and the notification center. Both are modal, so while one is open it owns the
// D-pad and Confirm/Back keys no matter where the scene focus sits (they can
// also be opened by tapping the Apps pill or the bell).
func handleChromeOverlayKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if weatherPopoverOpen {
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_b, sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
			dismissWeatherPopover()
		}
		return true
	}
	if tb.startOpen {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			startMenuNavigate(-1, config)
		case sdl.K_DOWN:
			startMenuNavigate(1, config)
		case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
			startMenuActivate(config)
		case sdl.K_ESCAPE, sdl.K_b:
			startMenuDismiss()
		}
		return true
	}
	if nc.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			notifNavigate(-1)
		case sdl.K_DOWN:
			notifNavigate(1)
		case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a, sdl.K_ESCAPE, sdl.K_b:
			notifDismiss()
		}
		return true
	}
	return false
}

// moveSelection moves focus to the neighboring selectable element along the
// semantic axis of the given action (Up/Down move rows, Left/Right move
// columns).
//
// The graph is rebuilt on every press so dynamic buttons and the measured
// top-bar chrome rects stay fresh; SetGraph keeps the focused node across
// reinstalls of the same scene, so this does not reset navigation progress
// (reinstalling used to snap focus back to the first button on every press).
func moveSelection(config *Config, action Action) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	currentScene := config.Scenes[currentSceneIndex]
	focusEngine.SetGraph(currentScene.Name, focusEngine.BuildGraph(currentScene))

	before := focusEngine.ElementIndex()
	result := focusEngine.Navigate(action)
	if result == before {
		// Dead-end: bridge between the grid's top row and the top-bar chrome
		// (back pill / status clock), which the spatial cones cannot reach.
		if alt, ok := focusEngine.StepChrome(action); ok && focusEngine.SetByElementIndex(alt) {
			result = alt
		}
	}
	// -1 means "no focus possible"; the top-bar chrome buttons use negative
	// sentinel indices and are valid results too.
	if result != -1 {
		if result != selectedButtonIndex {
			PlayNavSound()
		}
		selectedButtonIndex = result
		// First time focus enters a search-results grid, highlight the first
		// result so navigation is immediately visible (instead of an odd wrap).
		if focusedResultIndex < 0 && result >= 0 && result < len(currentScene.Elements) &&
			currentScene.Elements[result].Type == "searchresults" {
			focusedResultIndex = 0
			if videos, ok := getSceneVideos(config, currentScene); ok {
				prefetchFocusedResult(config, videos)
			}
		}
	} else {
		// Fallback to linear wrap if graph has no neighbor in that direction.
		direction := 1
		if action == ActionNavigateUp || action == ActionNavigateLeft {
			direction = -1
		}
		var interactive []int
		for i, el := range currentScene.Elements {
			if el.Type == "button" || el.Type == "input" || el.Type == "searchresults" || el.Type == "dynamiclist" || el.Type == "recent" || el.Type == "themegallery" || el.Type == "toggle" || el.Type == "textbrowser" || el.Type == "shortslist" || el.Type == "packagelist" {
				interactive = append(interactive, i)
			}
		}
		if len(interactive) == 0 {
			selectedButtonIndex = -1
			return
		}
		currentIdx := -1
		for idx, val := range interactive {
			if val == selectedButtonIndex {
				currentIdx = idx
				break
			}
		}
		if currentIdx == -1 {
			selectedButtonIndex = interactive[0]
			return
		}
		newIdx := currentIdx + direction
		if newIdx >= len(interactive) {
			newIdx = 0
		} else if newIdx < 0 {
			newIdx = len(interactive) - 1
		}
		selectedButtonIndex = interactive[newIdx]
	}
}

// moveHomeSelection moves focus within the home tile grid using the focus engine.
// It treats the "recent" element and tile grid as a unified spatial graph.
func moveHomeSelection(config *Config, dx, dy int) {
	if !homeLayoutActive || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	scene := config.Scenes[currentSceneIndex]
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))

	var action Action
	switch {
	case dy < 0:
		action = ActionNavigateUp
	case dy > 0:
		action = ActionNavigateDown
	case dx < 0:
		action = ActionNavigateLeft
	case dx > 0:
		action = ActionNavigateRight
	default:
		return
	}

	result := focusEngine.Navigate(action)
	if result >= 0 && result != selectedButtonIndex {
		PlayNavSound()
		selectedButtonIndex = result
	}
}

// handleVirtualKeyboardInput bridges physical keys to the on-screen keyboard
// while it is open: arrows/Tab move the highlight, Enter activates a key,
// Backspace deletes, Esc closes. Printable characters are delivered by SDL's
// text-input event so nothing is inserted twice.
func handleVirtualKeyboardInput(e *sdl.KeyboardEvent, config *Config) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		vkMoveFocus(0, -1)
	case sdl.K_DOWN:
		vkMoveFocus(0, 1)
	case sdl.K_LEFT:
		vkMoveFocus(-1, 0)
	case sdl.K_RIGHT:
		vkMoveFocus(1, 0)
	case sdl.K_TAB:
		vkMoveFocus(1, 0)
	case sdl.K_BACKSPACE, sdl.K_DELETE:
		vkBackspace(config)
	case sdl.K_ESCAPE:
		vkCloseKeyboard()
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		// Physical keyboards expect Enter to commit the text; the controller
		// uses A to activate the highlighted key (Xbox-style).
		vkSubmit(config, false)
	}
}
