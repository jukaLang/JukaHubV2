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
	vkTextSelected             bool // pre-filled field is selected: typing replaces it
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
	homeLayoutActive bool

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
	// Downloaded thumbnail payloads, bounded by entries and by bytes (see
	// thumbnailDataBudgetBytes in cachebudget.go). A count alone let a session
	// of large images sit under the 512-entry cap.
	thumbnailDataCache = newByteOrderedStore[string, []byte](
		thumbnailMemMaxEntries,
		thumbnailDataBudgetBytes(),
		func(b []byte) int64 { return int64(len(b)) },
		nil,
	)

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
//
// Surrounding whitespace is tolerated because the XML dialect hands over the
// raw attribute value and the same fields arrive from JSON as plain strings;
// the other numeric reader in the config layer (intOrString) trims too, so a
// padded number behaves the same wherever it is written.
func parsePosInt(text string) (int32, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
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
		// Labels can be captured in screenshots or shared by the companion;
		// never resolve a protected name or an encrypted value into visible text.
		if liveBuilderSensitiveKey(varName) {
			return m
		}
		if val, ok := vars[varName]; ok {
			if encrypted, ok := val.(string); ok && strings.HasPrefix(strings.TrimSpace(encrypted), "ENC:") {
				return m
			}
			return fmt.Sprintf("%v", val)
		}
		return m
	})
}

var (
	ytSearchKeyPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"ytsearch\d+:[^"]+"`) })
	ffmpegFlagPattern  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`-f "[^"]*"`) })
	spaceRunPattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s+`) })
)

func normalizeCacheKey(raw string) string {
	s := strings.ToLower(raw)
	s = ytSearchKeyPattern().ReplaceAllString(s, `"ytsearch:QUERY"`)
	s = ffmpegFlagPattern().ReplaceAllString(s, "")
	s = spaceRunPattern().ReplaceAllString(s, " ")
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
	normalizeMotionConfig(config)
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
	// A 0.4.0 home layout nobody edited gets the redesigned grid; the file is
	// a user file that updates keep, so this is the only way the new Home
	// reaches a device that already had one (home_migrate.go).
	migrateLegacyHomeLayout(config)
	// Give the Hardware screen its interactive process list entry point.
	ensureHardwareProcessEntry(config)
	// And the Wi-Fi picker, beside it on the Hardware action row.
	ensureHardwareWiFiEntry(config)
	// IPStream library entry points (IPStream screen + Media page).
	ensureIPStreamEntries(config)
	// Podcast library entry point (the Podcasts screen's Shows button).
	ensurePodcastEntries(config)
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
	// Resolve the path and size for this logical font name. A role may name a
	// file, but only an Inter one is honoured (homeUIFontPath): the UI is drawn
	// in the bundled Inter everywhere.
	fontPath := homeUIFontFile
	size := 24
	for k, p := range config.Variables.Fonts {
		if strings.EqualFold(k, fontName) {
			fontPath = homeUIFontPath(p)
			break
		}
	}
	for k, v := range config.Variables.FontSizes {
		if strings.EqualFold(k, fontName) {
			size = v
			break
		}
	}
	// The compact canvas squeezes the 1280-wide design into 960, so type is
	// scaled with it; then accessibility scaling applies here, at the one place
	// every logical font is resolved, so no screen is left behind. The
	// legibility floor (design.go) keeps a theme's tiny role readable.
	size = a11yScaleFontSize(uiLegibleFontSize(size))
	return loadCachedFont(fontPath, size), size
}

// getFontSized opens a logical font at a pixel size the caller chooses, for
// displays that need a size no config role defines (the ambient clock). It
// shares the cache, fallback chain and accessibility scaling with
// getCachedFont. Captions authored under the legibility floor are opened at
// the floor (uiLegibleFontSize, design.go).
func getFontSized(config *Config, fontName string, size int) *ttf.Font {
	return loadRoleFont(config, fontName, a11yScaleFontSize(uiLegibleFontSize(size)))
}

// loadRoleFont opens a logical font role at an already-scaled pixel size. It
// skips the accessibility multiplier because callers use it to step through
// sizes themselves; loadCachedFont still shares the cache and fallback chain.
func loadRoleFont(config *Config, fontName string, size int) *ttf.Font {
	fontPath := homeUIFontFile
	for k, p := range config.Variables.Fonts {
		if strings.EqualFold(k, fontName) {
			fontPath = homeUIFontPath(p)
			break
		}
	}
	return loadCachedFont(fontPath, size)
}

// fontRoleSize is the size a logical font role is authored at on this canvas,
// before accessibility scaling.
func fontRoleSize(config *Config, fontName string) int {
	size := 24
	for k, v := range config.Variables.FontSizes {
		if strings.EqualFold(k, fontName) {
			size = v
			break
		}
	}
	return uiLegibleFontSize(size)
}

// buttonLabelFont fits a button's label inside its width. A fixed-width button
// cannot grow when the text size is raised, and drawing "Play…" for "Play
// Link" defeats the setting that asked for larger text. The label therefore
// steps down a pixel at a time until it fits, but never below the size the
// same label has at 100% text scale — every shipped button fits at that size,
// so the search always ends and the user's own size is only given up where the
// layout leaves no other choice.
func buttonLabelFont(config *Config, fontName, label string, width int32) *ttf.Font {
	font, _ := getCachedFont(config, fontName)
	if font == nil || label == "" {
		return font
	}
	inner := width - 2*SpaceXS
	if inner <= 0 {
		return font
	}
	// The result is cached even when the label already fits: this runs for
	// every button on every frame, and a map lookup is cheaper on the handheld
	// than measuring the label again.
	key := buttonFitKey{base: font, label: label, width: width}
	buttonFitMutex.Lock()
	cached, ok := buttonFitCache[key]
	buttonFitMutex.Unlock()
	if ok {
		return cached
	}
	fitted := font
	if tw, _, err := font.SizeUTF8(label); err != nil || int32(tw) > inner {
		authored := fontRoleSize(config, fontName)
		floor := authored
		if floor < 9 {
			floor = 9
		}
		for size := a11yScaleFontSize(authored) - 1; size >= floor; size-- {
			candidate := loadRoleFont(config, fontName, size)
			if candidate == nil {
				break
			}
			if tw, _, err := candidate.SizeUTF8(label); err == nil && int32(tw) <= inner {
				fitted = candidate
				break
			}
		}
	}
	buttonFitMutex.Lock()
	if len(buttonFitCache) >= 512 {
		buttonFitCache = map[buttonFitKey]*ttf.Font{}
	}
	buttonFitCache[key] = fitted
	buttonFitMutex.Unlock()
	return fitted
}

// buttonFitKey identifies one fitted label: the face it starts from (which
// already encodes the role, the canvas and the accessibility size) plus the
// text and the width it had to fit into.
type buttonFitKey struct {
	base  *ttf.Font
	label string
	width int32
}

var (
	buttonFitMutex sync.Mutex
	buttonFitCache = map[buttonFitKey]*ttf.Font{}
)

// resetButtonFitCache drops the fitted fonts. Every cache entry points at a
// face the font cache owned, so a text-size change or a test reload must not
// keep handing out the old size.
func resetButtonFitCache() {
	buttonFitMutex.Lock()
	buttonFitCache = map[buttonFitKey]*ttf.Font{}
	buttonFitMutex.Unlock()
}

// loadCachedFont opens path at size (already accessibility-scaled) and caches it
// by the pair, so the same face at different sizes is cached separately.
func loadCachedFont(fontPath string, size int) *ttf.Font {
	// Inter is the only UI face: a path naming any other font is read as the
	// bundled Inter before it reaches the cache, so the cache key, the file
	// opened and the cmap registered below all agree.
	fontPath = homeUIFontPath(fontPath)
	key := fontPath + "\n" + strconv.Itoa(size)
	if font, ok := fontCache[key]; ok {
		return font
	}

	opened := fontPath
	font, err := openHomeUIFont(fontPath, size)
	if err != nil && fontPath != homeUIFontFile {
		// A missing Inter weight falls back to the bundled regular cut. There
		// is deliberately no chain past it: DejaVu, Arial and Roboto are not
		// shipped, and borrowing a system face made every device draw a
		// different UI.
		if f, e2 := openHomeUIFont(homeUIFontFile, size); e2 == nil {
			font, err, opened = f, nil, homeUIFontFile
		}
	}
	if err != nil {
		logWarn("core", "Error loading font %s: %v", fontPath, err)
		return nil
	}
	// Remember where this font was opened so renderText can read its cmap and
	// substitute characters it cannot draw (see fonts.go). That is the file
	// actually opened: registering the missing one made the coverage lookup
	// fail, which counts as "covers everything", so a fallback face drew
	// .notdef boxes instead of substitutes.
	registerFontPath(font, opened)
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
	// Cached substitution: a UI full of static labels otherwise walked every
	// rune against the font's cmap on every frame.
	return drawTextRun(renderer, config, font, sanitizeCached(font, processed), color, x, y)
}

// drawTextRun renders one font/text pair through the shared texture cache.
func drawTextRun(renderer *sdl.Renderer, config *Config, font *ttf.Font, processed string, color sdl.Color, x, y int32) (int32, int32) {
	if processed == "" || font == nil {
		return 0, 0
	}

	// Try the text cache first (major perf win on TSP ARM64)
	cache := GetTextCache()
	if entry := cache.Get(font, processed, color); entry != nil {
		// SDL rejects a zero-area destination and builds an error value for
		// it; a blank or space-only label would otherwise allocate one of
		// those on every frame it is drawn.
		if entry.width > 0 && entry.height > 0 {
			renderer.Copy(entry.texture, nil, &sdl.Rect{X: x, Y: y, W: entry.width, H: entry.height})
		}
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
	if w > 0 && h > 0 {
		renderer.Copy(texture, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
	}
	cache.Put(font, processed, color, texture, w, h)
	return w, h
}

// --- Visual theme helpers ---------------------------------------------------

var (
	bgTexture   *sdl.Texture
	bgImagePath string
	// bgImageFailed is the wallpaper path that last failed to load, so a
	// missing or corrupt image is not re-opened on every frame.
	bgImageFailed string
	overlayColor  = sdl.Color{R: 8, G: 10, B: 18, A: 150}

	gradientTexture    *sdl.Texture
	gradientSceneKey   string
	gradientOverlayKey sdl.Color
)

// bgImageFailedAt is when bgImageFailed last failed. The same path is tried
// again once bgImageRetry has passed, so a wallpaper on a card that mounts
// after JukaHub starts still appears without being picked again.
var bgImageFailedAt time.Time

// bgImageRetry is how long a wallpaper that failed waits for its next attempt.
const bgImageRetry = 15 * time.Second

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
	path := ""
	if config != nil {
		path = config.Variables.BackgroundImage
	}
	if appWallpaperIsShippedDefault(path) {
		// No wallpaper of the user's own: every screen draws the procedural
		// backdrop (drawAppBackdrop), the one Home always used. The stock
		// background.jpg is 2560x1440 — about 15 MB as a texture — and nothing
		// draws it any more, so it is not loaded at all.
		if bgTexture != nil {
			bgTexture.Destroy()
			bgTexture = nil
		}
		bgImagePath = ""
		return
	}
	if gifWallpaperPath(path) {
		// An animated wallpaper belongs to gifwall.go, which decodes it off the
		// render thread at the size it is shown. Loading it here as well kept a
		// second, stretched copy of its first frame — and where SDL_image
		// cannot read GIFs, logged a failed load every 15 seconds.
		if bgTexture != nil {
			bgTexture.Destroy()
			bgTexture = nil
		}
		bgImagePath = path
		return
	}
	if path == bgImagePath && bgTexture != nil {
		return
	}
	// A wallpaper that failed to load is not retried on every frame: that was
	// a file open, an image decode attempt and a log line per frame, forever.
	// It is tried again every bgImageRetry, or at once if another wallpaper is
	// picked and then this one again.
	if path == bgImageFailed && time.Since(bgImageFailedAt) < bgImageRetry {
		return
	}
	if bgTexture != nil {
		bgTexture.Destroy()
		bgTexture = nil
	}
	bgImagePath = path
	p := resolvePath(path)
	if tex, err := img.LoadTexture(renderer, p); err == nil {
		bgTexture = fitBackgroundTexture(renderer, tex)
		bgImageFailed = ""
		logDebug("core", "Background image loaded: %s", p)
		return
	} else if surface, err2 := img.Load(p); err2 == nil {
		// Fallback: load as a surface and upload manually.
		if tex2, err3 := renderer.CreateTextureFromSurface(surface); err3 == nil {
			bgTexture = fitBackgroundTexture(renderer, tex2)
			bgImageFailed = ""
			surface.Free()
			logDebug("core", "Background image loaded (surface): %s", p)
			return
		}
		surface.Free()
		logWarn("core", "Background image load failed (%s): %v", path, err2)
	} else {
		logWarn("core", "Background image load failed (%s): %v", path, err)
	}
	bgImageFailed = path
	bgImageFailedAt = time.Now()
}

// fitBackgroundTexture turns a loaded wallpaper into a static, opaque texture
// the size of the canvas, once, at load. A phone photo or a 4K wallpaper is
// drawn at 1280x720 (960x720 on the Brick) every frame anyway; keeping it at
// full size held tens of MB of texture for pixels the screen cannot show, and
// made every frame minify it without mipmaps. The picture is scaled by the GPU
// onto an opaque black canvas in a scratch render target, read back, and
// uploaded as an ordinary texture: no render target is kept (a Direct3D device
// reset would blank one), and a PNG with transparent areas cannot leave the
// previous frame showing through them. The original is returned unchanged when
// the renderer cannot draw into a texture.
func fitBackgroundTexture(renderer *sdl.Renderer, tex *sdl.Texture) *sdl.Texture {
	if renderer == nil || tex == nil || screenWidth <= 0 || screenHeight <= 0 {
		return tex
	}
	if !renderer.RenderTargetSupported() {
		return tex
	}
	cw, ch := screenWidth, screenHeight
	target, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, cw, ch)
	if err != nil || target == nil {
		return tex
	}
	defer target.Destroy()
	canvas, err := sdl.CreateRGBSurfaceWithFormat(0, cw, ch, 32, sdl.PIXELFORMAT_ARGB8888)
	if err != nil || canvas == nil {
		return tex
	}
	defer canvas.Free()
	prev := renderer.GetRenderTarget()
	// The wallpaper can first load mid-frame inside a caller's clip (the header
	// band repaint, or a frame being captured into a texture). SDL restores the
	// default target's clip itself, but a texture target comes back unclipped,
	// so the clip is put back by hand.
	hadClip := renderer.IsClipEnabled()
	clip := renderer.GetClipRect()
	if err := renderer.SetRenderTarget(target); err != nil {
		return tex
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	_ = renderer.SetDrawColor(0, 0, 0, 255)
	_ = renderer.Clear()
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.Copy(tex, nil, nil)
	readErr := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, canvas.Data(), int(canvas.Pitch))
	_ = renderer.SetRenderTarget(prev)
	if hadClip {
		_ = renderer.SetClipRect(&clip)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if readErr != nil {
		return tex
	}
	static, err := renderer.CreateTextureFromSurface(canvas)
	if err != nil || static == nil {
		return tex
	}
	// Opaque: copying it needs no blending, the cheapest full-screen copy the
	// GPU can do.
	_ = static.SetBlendMode(sdl.BLENDMODE_NONE)
	tex.Destroy()
	return static
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
	// The channel difference is taken in int, not uint8: b.R-a.R wrapped
	// around whenever the target was darker (0-255 is 1 in uint8), so a blend
	// towards black came back as the start colour — every home glyph's
	// "shade" and "deepShade" were plain white, and the parcel, folder and
	// gamepad lost the planes and details they were drawn with.
	return sdl.Color{
		R: clamp(int(a.R) + int(float32(int(b.R)-int(a.R))*t)),
		G: clamp(int(a.G) + int(float32(int(b.G)-int(a.G))*t)),
		B: clamp(int(a.B) + int(float32(int(b.B)-int(a.B))*t)),
		A: a.A,
	}
}

// strokeCircle draws an unfilled circle outline using an angle fan.
func strokeCircle(renderer *sdl.Renderer, cx, cy, r int32, c sdl.Color) {
	if r <= 0 {
		return
	}
	// Clock dials and spinner rings redraw every frame; cache the pixel ring.
	w := 2*r + 1
	drawShapeCached(renderer, shapeKindStrokeCircle, cx-r, cy-r, w, w, r, 0, c, func(rr *sdl.Renderer, ox, oy, _, _ int32) {
		drawCircleRing(rr, ox+r, oy+r, r, shapeOpaque(c))
	})
}

// drawCircleRing paints a 1-point ring centred at (ccx, ccy). Kept identical
// to the original strokeCircle step math so cached and fallback paths match.
func drawCircleRing(renderer *sdl.Renderer, ccx, ccy, r int32, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	steps := r * 8
	if steps < 32 {
		steps = 32
	}
	for i := int32(0); i < steps; i++ {
		ang := float64(i) * 2 * math.Pi / float64(steps)
		x := ccx + int32(math.Round(float64(r)*math.Cos(ang)))
		y := ccy + int32(math.Round(float64(r)*math.Sin(ang)))
		renderer.DrawPoint(x, y)
	}
}

// fillCircle draws a filled circle (Go-SDL2 has no native helper).
func fillCircle(renderer *sdl.Renderer, cx, cy, r int32, c sdl.Color) {
	if r <= 0 {
		return
	}
	// Filled discs redraw every frame behind badges and calendar dots; cache
	// the one-pixel disc instead of issuing a DrawLine per scanline.
	w := 2*r + 1
	drawShapeCached(renderer, shapeKindFillCircle, cx-r, cy-r, w, w, r, 0, c, func(rr *sdl.Renderer, ox, oy, _, _ int32) {
		drawCircleDisc(rr, ox+r, oy+r, r, shapeOpaque(c))
	})
}

// drawCircleDisc paints a filled disc centred at (ccx, ccy), same row math as
// the original fillCircle so cached and direct draws stay identical.
func drawCircleDisc(renderer *sdl.Renderer, ccx, ccy, r int32, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	for dy := int32(0); dy <= r; dy++ {
		dx := int32(math.Sqrt(float64(r*r - dy*dy)))
		renderer.DrawLine(ccx-dx, ccy+dy, ccx+dx, ccy+dy)
		if dy != 0 {
			renderer.DrawLine(ccx-dx, ccy-dy, ccx+dx, ccy-dy)
		}
	}
}

// diskPieLayout reserves separate space for the title, ring and three rows.
type diskPieLayout struct {
	cx, cy, radius         int32
	usageX, usageY         int32
	usageWidth, lineHeight int32
}

func layoutDiskPie(element Element, elemW, elemH, titleH, lineH int32) diskPieLayout {
	const pad = int32(16)
	const gap = int32(4)
	usageY := element.Y + elemH - pad - (3*lineH + 2*gap)
	top := element.Y + 12 + titleH + 10
	bottom := usageY - 10
	available := bottom - top
	if available < 0 {
		available = 0
	}
	radius := min32(120, min32(max32(0, (elemW-2*pad)/2), available/2))
	return diskPieLayout{
		cx: element.X + elemW/2, cy: top + available/2, radius: radius,
		usageX: element.X + pad, usageY: usageY,
		usageWidth: max32(0, elemW-2*pad), lineHeight: lineH,
	}
}

// drawDiskPieSector paints a solid clockwise arc from 12 o'clock. Grouping
// pixels into horizontal spans avoids the gaps left by a radial line fan.
func drawDiskPieSector(renderer *sdl.Renderer, cx, cy, radius, innerR int32, fraction float64, color sdl.Color) {
	if radius <= 0 || fraction <= 0 {
		return
	}
	if fraction > 1 {
		fraction = 1
	}
	// A tenth of a percent is finer than the displayed whole percentage and
	// lets the existing shape cache reuse the same arc on subsequent frames.
	steps := int32(math.Round(fraction * 1000))
	if steps <= 0 {
		return
	}
	fraction = float64(steps) / 1000
	angle := 2 * math.Pi * fraction
	ex, ey := math.Sin(angle), -math.Cos(angle)
	diameter := 2*radius + 1
	drawShapeCached(renderer, shapeKindDiskPieSector, cx-radius, cy-radius, diameter, diameter, innerR, steps, color,
		func(r *sdl.Renderer, x, y, _, _ int32) {
			r.SetDrawColor(color.R, color.G, color.B, 255)
			for dy := -radius; dy <= radius; dy++ {
				start := int32(-radius - 1)
				for dx := -radius; dx <= radius+1; dx++ {
					distance := int64(dx)*int64(dx) + int64(dy)*int64(dy)
					inRing := dx <= radius && distance <= int64(radius)*int64(radius) && distance > int64(innerR)*int64(innerR)
					endCross := float64(dx)*ey - float64(dy)*ex
					inArc := fraction >= 1 || (fraction <= 0.5 && dx >= 0 && endCross >= 0) || (fraction > 0.5 && (dx >= 0 || endCross >= 0))
					if inRing && inArc {
						if start < -radius {
							start = dx
						}
					} else if start >= -radius {
						r.DrawLine(x+radius+start, y+radius+dy, x+radius+dx-1, y+radius+dy)
						start = -radius - 1
					}
				}
			}
		})
}

func diskPieUsageLines(total, used uint64) []string {
	if used > total {
		used = total
	}
	return []string{"Used " + formatBytes(used), "Free " + formatBytes(total-used), "Total " + formatBytes(total)}
}

func diskPiePercentFont(config *Config, text string, innerR int32) *ttf.Font {
	font, size := getCachedFont(config, "big")
	// Keep the whole number inside the circular hole, including its corners.
	for font != nil {
		w, h, _ := font.SizeUTF8(text)
		if innerR > 4 && math.Hypot(float64(w)/2, float64(h)/2) <= float64(innerR-4) {
			return font
		}
		if size <= 8 {
			break
		}
		size--
		font = loadRoleFont(config, "big", size)
	}
	return nil
}

// renderDiskPieChart shows the percentage in a solid ring, with the figures
// on separate rows beneath it so long disk sizes cannot run off the card.
func renderDiskPieChart(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := getElementWidth(element, 500)
	elemH := getElementHeight(element, 400)

	diskPieMutex.Lock()
	total, used, pct := diskPie.Total, diskPie.Used, diskPie.Pct
	diskPieMutex.Unlock()

	drawPanel(renderer, element.X, element.Y, elemW, elemH, WithAlpha(ColorSurfacePanel, 235), accentColor)
	titleFont, _ := getCachedFont(config, "medium")
	font, _ := getCachedFont(config, "small")
	if titleFont == nil {
		titleFont = font
	}
	if font == nil {
		font = titleFont
	}
	titleH, lineH := int32(0), int32(0)
	if titleFont != nil {
		titleH = int32(titleFont.Height())
		title := ellipsize(titleFont, "Disk Usage", max32(0, elemW-32))
		lw, _, _ := titleFont.SizeUTF8(title)
		renderText(renderer, config, titleFont, title, ColorTextSecondary(), element.X+(elemW-int32(lw))/2, element.Y+12)
	}
	if total == 0 {
		UIEmptyState(renderer, config, sdl.Rect{X: element.X, Y: element.Y, W: elemW, H: elemH},
			"@tools", "Reading storage", "Disk figures appear once the card answers.")
		return
	}
	if used > total {
		used = total
	}
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	if font != nil {
		lineH = int32(font.Height())
	}
	layout := layoutDiskPie(element, elemW, elemH, titleH, lineH)
	innerR := layout.radius * 2 / 3
	usedFrac := float64(used) / float64(total)
	sectorColor := accentColor
	if usedFrac > 0.9 {
		sectorColor = ColorDanger
	} else if usedFrac > 0.75 {
		sectorColor = ColorWarning
	}
	// A glass track ring under the used arc, with the middle left open so the
	// panel's glass shows through it. The track used to be a full slate disc
	// with an opaque slate one stamped over its centre, which read as a grey
	// coin on the glass panel. The track is opaque on purpose: the sector's
	// uncached path paints at full alpha.
	drawDiskPieSector(renderer, layout.cx, layout.cy, layout.radius, innerR, 1, glassMix(ColorBackground, glassInk(), 0.14))
	drawDiskPieSector(renderer, layout.cx, layout.cy, layout.radius, innerR, usedFrac, sectorColor)

	pctStr := fmt.Sprintf("%d%%", pct)
	if centerFont := diskPiePercentFont(config, pctStr, innerR); centerFont != nil {
		pw, ph, _ := centerFont.SizeUTF8(pctStr)
		renderText(renderer, config, centerFont, pctStr, ColorTextPrimary(), layout.cx-int32(pw)/2, layout.cy-int32(ph)/2)
	}

	if font != nil && layout.usageWidth > 0 {
		lines := diskPieUsageLines(total, used)
		for i, line := range lines {
			color := ColorTextSecondary()
			if i == 2 {
				color = ColorTextTertiary()
			}
			renderText(renderer, config, font, ellipsize(font, line, layout.usageWidth), color,
				layout.usageX, layout.usageY+int32(i)*(layout.lineHeight+4))
		}
	}
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

// roundedRowInset is the horizontal inset of one scanline inside a rounded
// rectangle of height h: zero through the flat middle, widening towards the
// corners.
func roundedRowInset(r, h, dy int32) int32 {
	if dy < r {
		return roundedCornerInset(r, dy)
	}
	if dy >= h-r {
		return roundedCornerInset(r, h-1-dy)
	}
	return 0
}

// fillRoundedRect draws a filled rounded rectangle using per-scanline
// rendering. The radius is clamped to half the width/height and every pixel
// stays inside the supplied rectangle.
func fillRoundedRect(renderer *sdl.Renderer, x, y, w, h, r int32, c sdl.Color) {
	rc := sdl.Rect{X: x, Y: y, W: w, H: h}
	fillRoundedRectAt(renderer, rc, rc, r, c)
}

// fillRoundedRectAt fills the rounded rect at dst while taking its shape from
// base's size, so an animated rect (the home card's hover lift) reuses one
// cached texture for every step instead of rasterising a new one per pixel it
// grows. When base is dst the two are the same and nothing is scaled.
func fillRoundedRectAt(renderer *sdl.Renderer, base, dst sdl.Rect, r int32, c sdl.Color) {
	x, y, w, h := dst.X, dst.Y, dst.W, dst.H
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
	// A card/dial/ring geometry repeats every frame; round-tripping it through
	// the shape cache costs one blended Copy instead of dozens of fill passes
	// (each a separate draw call on the GLES2 renderer). See shapeCache.go.
	drawShapeCachedAt(renderer, shapeKindFillRound, base.W, base.H, dst, r, 0, c, func(rr *sdl.Renderer, ox, oy, ww, wh int32) {
		drawRoundedFillRows(rr, ox, oy, ww, wh, r, shapeOpaque(c))
	})
}

// shapeOpaque returns the shape in full alpha: the cache stores an opaque
// texture and alpha-modulates it per draw, so per-draw transparency survives.
func shapeOpaque(c sdl.Color) sdl.Color {
	return sdl.Color{R: c.R, G: c.G, B: c.B, A: 255}
}

// drawRoundedFillRows paints a filled rounded rect with its top-left at
// (ox, oy). Neighbouring scanlines with the same inset merge into one rect so
// the flat middle of a card is a handful of fills (used directly when the
// shape cannot be cached, and once per new geometry when filling the cache).
func drawRoundedFillRows(renderer *sdl.Renderer, ox, oy, w, h, r int32, c sdl.Color) {
	setDrawColor(renderer, c)
	for dy := int32(0); dy < h; {
		inset := roundedRowInset(r, h, dy)
		run := dy + 1
		for run < h && roundedRowInset(r, h, run) == inset {
			run++
		}
		x0 := ox + inset
		x1 := ox + w - inset
		if x1 > x0 {
			renderer.FillRect(&sdl.Rect{X: x0, Y: oy + dy, W: x1 - x0, H: run - dy})
		}
		dy = run
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
	rc := sdl.Rect{X: x, Y: y, W: w, H: h}
	strokeRoundedRectAt(renderer, rc, rc, r, thickness, c)
}

// strokeRoundedRectAt strokes the ring at dst, taking its shape from base's
// size: the same trick fillRoundedRectAt uses, so a focus ring that follows a
// lifting card keeps one cached texture for the whole animation.
func strokeRoundedRectAt(renderer *sdl.Renderer, base, dst sdl.Rect, r, thickness int32, c sdl.Color) {
	w, h := dst.W, dst.H
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
	// A focus ring is redrawn every frame while nothing about it moves; run it
	// through the shape cache like fills: one Copy per frame instead of
	// per-scanline fill passes (shapeCache.go).
	drawShapeCachedAt(renderer, shapeKindStrokeRound, base.W, base.H, dst, r, thickness, c, func(rr *sdl.Renderer, ox, oy, ww, wh int32) {
		drawRoundedStrokeRows(rr, ox, oy, ww, wh, r, thickness, shapeOpaque(c))
	})
}

// drawRoundedStrokeRows paints the ring of a w×h rounded rect with its top-left
// at (ox, oy). It is the one painter for both the cached texture and the
// uncached fallback.
func drawRoundedStrokeRows(renderer *sdl.Renderer, ox, oy, w, h, r, thickness int32, c sdl.Color) {
	innerR := r - thickness
	if innerR < 0 {
		innerR = 0
	}
	ih := h - 2*thickness
	// One scanline of the ring: the outer span, plus the inner hole when the
	// row cuts through the ring's sides rather than its top or bottom band.
	type ringRow struct {
		left, right           int32
		innerLeft, innerRight int32
		hole                  bool
	}
	rowAt := func(dy int32) ringRow {
		oInset := roundedRowInset(r, h, dy)
		row := ringRow{left: ox + oInset, right: ox + w - oInset}
		if dy >= thickness && dy < h-thickness && ih > 0 {
			// Punch the inner hole so only the ring remains.
			id := dy - thickness
			var iInset int32
			if id < innerR {
				iInset = roundedCornerInset(innerR, id)
			} else if id >= ih-innerR {
				iInset = roundedCornerInset(innerR, ih-1-id)
			}
			row.innerLeft = ox + thickness + iInset
			row.innerRight = ox + w - thickness - iInset
			row.hole = true
		}
		return row
	}
	setDrawColor(renderer, c)
	// Rows with the same geometry merge into one rectangle: the ring's straight
	// sides are two tall bars rather than two 1px fills per row.
	for dy := int32(0); dy < h; {
		row := rowAt(dy)
		run := dy + 1
		for run < h && rowAt(run) == row {
			run++
		}
		height := run - dy
		if row.right > row.left {
			switch {
			case !row.hole:
				// Top/bottom bands of the ring are solid.
				renderer.FillRect(&sdl.Rect{X: row.left, Y: oy + dy, W: row.right - row.left, H: height})
			default:
				if row.innerLeft > row.left {
					renderer.FillRect(&sdl.Rect{X: row.left, Y: oy + dy, W: row.innerLeft - row.left, H: height})
				}
				if row.right > row.innerRight {
					renderer.FillRect(&sdl.Rect{X: row.innerRight, Y: oy + dy, W: row.right - row.innerRight, H: height})
				}
			}
		}
		dy = run
	}
}

// drawPanel renders a content container — a list's or a text log's frame — in
// Home's glass: a translucent pane with a hairline edge and Home's 20px
// corners. The callers' fills were a translucent slate, which is what the
// glass replaces; a caller that asks for an (almost) opaque fill is covering
// something underneath (a confirmation box over a list), so it gets the opaque
// glass sheet instead. The three stacked drop shadows, the sheen lines and the
// corner dots are gone: Home's cards have none of them, and they were seven
// extra fills per panel per frame.
func drawPanel(renderer *sdl.Renderer, x, y, w, h int32, fill, border sdl.Color) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	surface := glassA(10)
	if fill.A >= 240 {
		surface = glassSheetFill()
	}
	fillRoundedRect(renderer, x, y, w, h, homeTileRadius, surface)
	strokeRoundedRect(renderer, x, y, w, h, homeTileRadius, 1, glassEdge())
}

// drawCard renders a raised surface card: Home's glass tile — the translucent
// fill and its hairline — at the caller's corner radius. Canonical content
// container used across screens.
func drawCard(renderer *sdl.Renderer, x, y, w, h, r int32) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	fillRoundedRect(renderer, x, y, w, h, r, glassFill())
	strokeRoundedRect(renderer, x, y, w, h, r, 1, glassEdge())
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
	// Glass ink, not white: on a light preset a white track and thumb vanished
	// into the page.
	track := glassA(22)
	renderer.SetDrawColor(track.R, track.G, track.B, track.A)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	thumbH := int32(float64(h) * thumbFrac)
	if thumbH < 24 {
		thumbH = 24
	}
	maxOff := h - thumbH
	thumbY := y + int32(float64(maxOff)*scrollFrac)
	fillRoundedRect(renderer, x, thumbY, w, thumbH, w/2, glassA(75))
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
	if elem.Style == menuTilesStyle {
		renderMenuTile(renderer, config, elem, selected, hovered, hoverProgress, pressed)
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
	// The hit size is measured from the requested label, so the button keeps
	// the geometry the focus graph and hit testing already know about.
	//
	// A busy control is drawn as one: the Benchmark screen's Run button is
	// disabled and relabelled for the length of a measurement, so it never
	// looks like a press would do anything. Focus, hover and the press dip are
	// dropped with it — a press that landed just before the run started must
	// not leave the button looking pressed. Its configured width still holds,
	// so the label is fitted to the label actually drawn and "Running…" is
	// stepped down rather than ellipsized.
	label, busy := benchmarkButtonFace(elem)
	if elem.Trigger == "cycle_weather_unit" {
		label = weatherUnitSettingLabel(config)
	}
	if busy {
		selected, hovered, pressed = false, false, false
	}
	font = buttonLabelFont(config, elem.Font, label, width)
	x, y := elem.X, elem.Y

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

	// Home's glass, with the author's (or the theme's) button colour kept as a
	// low-alpha wash under it, so Settings → Appearance → Button Color still
	// tints the buttons without turning them back into opaque slabs.
	tint := sdl.Color{}
	if strings.TrimSpace(elem.BgColor) != "" {
		tint = glassButtonTint(resolveColor(config, elem.BgColor, sdl.Color{}))
	}
	drawGlassButton(renderer, config, sdl.Rect{X: ox, Y: oy, W: width, H: height}, label, font, tint,
		glassState{Focused: selected, Hovered: hovered, Pressed: pressed, Disabled: busy})
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

	// Home layout override: when Main scene uses layout "home", tiles are
	// positioned by the shared grid so render + mouse agree.
	if rect, ok := homeTileRect(elemIndex); ok {
		x, y, width, height = rect.X, rect.Y, rect.W, rect.H
	}

	// One card anatomy app-wide: Home's glass tile with the icon badge in the
	// top-left corner and the name anchored bottom-left (UICategoryTileDraw).
	// The centred icon-over-label card this used to draw was the older shell's
	// tile, and the only place it still appeared was a scene outside Home.
	kind := tileIconKind(elem)
	UICategoryTileDraw(renderer, config, UICategoryTile{
		Rect:          sdl.Rect{X: x, Y: y, W: width, H: height},
		Label:         elem.Text,
		Focused:       selected,
		Hovered:       hovered,
		Pressed:       pressed,
		HoverProgress: hoverProgress,
		Glyph: func(rr *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			drawTileIcon(rr, box.X+box.W/2, box.Y+box.H/2, box.W, kind, tint, WithAlpha(tint, 40))
		},
	})
}

// renderLabelShadowed draws a label with a subtle drop shadow for depth.
func renderLabelShadowed(renderer *sdl.Renderer, config *Config, elem Element) {
	font, _ := getCachedFont(config, elem.Font)
	if font == nil {
		return
	}
	// Labels carry no box of their own, so an authored sentence longer than the
	// panel used to run straight off the screen edge (the Apps blurb did on the
	// Brick). Keep it inside the viewport instead of drawing past it.
	text := elem.Text
	if strings.TrimSpace(string(elem.Width)) == "" && screenWidth > 0 {
		if maxW := screenWidth - elem.X - 8; maxW > 0 {
			text = fitTextWidth(renderer, config, font, text, maxW)
		}
	}
	color := resolveColor(config, elem.Color, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	renderText(renderer, config, font, text, sdl.Color{R: 0, G: 0, B: 0, A: 140}, elem.X+2, elem.Y+2)
	renderText(renderer, config, font, text, color, elem.X, elem.Y)
}

// --- Tool path resolution ---
func getToolPath(tool string, config *Config) string {
	exeName := tool
	if IsWindows() {
		exeName += ".exe"
	}
	// Resolve to an absolute path so the OS reliably locates the tool's
	// sibling DLLs (e.g. a shared ffmpeg.exe's av*.dll) regardless of the process CWD.
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	// Check tools_path variable (config-driven). It may name a directory that
	// holds the tools or the tool binary itself; never return a directory as
	// the executable path, or exec would fail with "is a directory". A nil
	// config is legal here: launch helpers run in tests and before the config
	// is loaded, and they must fall through to the platform dir and PATH rather
	// than dereference it.
	if config != nil {
		if tp := strings.TrimSpace(config.Variables.ToolsPath); tp != "" {
			fullPath := filepath.Join(tp, exeName)
			if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
				return resolve(fullPath)
			}
			if info, err := os.Stat(tp); err == nil && !info.IsDir() {
				return resolve(tp)
			}
		}
	}
	// Check the platform tools dir ("required" next to the binary) — the same
	// location EnsureHelperTool installs into.
	commonPath := filepath.Join(P().ToolsDir(), exeName)
	if _, err := os.Stat(commonPath); err == nil {
		return resolve(commonPath)
	}
	// Fallback to system PATH: resolve the actual binary so callers can stat
	// it (a bare "ffmpeg.exe" only stats against the working directory).
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
	case "yt-dlp", "ffmpeg", "ffprobe":
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
		logWarn("core", "security: Failed to decrypt %s: %v", key, err)
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

// ytDlpExtraArgs returns extra yt-dlp arguments based on config: the cookies
// file, when one is set.
//
// A google_api_key used to be passed here as "youtube:api_key=…". yt-dlp has
// no such extractor argument (its youtube:innertube_key wants the site's own
// web key, not a Data API key), so the key did nothing for yt-dlp — but it
// made every search command one the native search does not model, so a user
// who added a key because they thought YouTube needed one got slower searches
// for it. Nothing in JukaHub needs a YouTube or Google API key; a key that is
// set is now only noted in the log (ytNoteIgnoredAPIKey).
func ytDlpExtraArgs(config *Config) string {
	var parts []string
	if config == nil {
		return ""
	}
	ytNoteIgnoredAPIKey(config)
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
	base := fmt.Sprintf(`yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end %d "ytsearch%d:$search_query"`, mediaSearchPageSize, mediaSearchPageSize)
	if extra := ytDlpExtraArgs(config); extra != "" {
		return base + " " + extra
	}
	return base
}

// ytDlpExtraArgsSlice is ytDlpExtraArgs as an argument list.
func ytDlpExtraArgsSlice(config *Config) []string {
	var parts []string
	if config == nil {
		return nil
	}
	ytNoteIgnoredAPIKey(config)
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

func loadThumbnail(renderer *sdl.Renderer, url string) *sdl.Texture {
	return loadThumbnailFromURLs(renderer, []string{url})
}

func loadThumbnailFromURLs(renderer *sdl.Renderer, urls []string) *sdl.Texture {
	// Warm the disk cache for alternate URLs in the background so scrolling
	// through a list never blocks on a cold network fetch. A caller may hand
	// over an empty candidate list (a card with neither metadata nor artwork
	// to derive), so guard the slice before the range.
	if len(urls) > 1 {
		for _, rawURL := range urls[1:] {
			PrefetchThumbnail(rawURL)
		}
	}
	for _, rawURL := range urls {
		if rawURL == "" {
			continue
		}
		url := normalizeThumbnailURL(rawURL)
		if tex := getThumbnailTextures().Get(url); tex != nil {
			return tex
		}

		// Render-path decode budget: a frame may start only a couple of
		// thumbnail decodes, so a cold grid cannot spend its whole frame
		// allowance decoding rows the D-pad just scrolled in. A declined URL
		// is queued through the prefetch workers and takes the next frame's
		// slots, so the list fills progressively instead of stuttering once.
		if !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
			// Out of slots — the prefetch workers pick the URL up on a later
			// frame. No log: a cold grid can defer dozens of rows per frame
			// and those lines are per-frame spam, not diagnostics.
			PrefetchThumbnail(url)
			continue
		}

		// Bytes come from memory → disk → network (with retry), so a warm
		// cache never touches the network and a cold one survives restarts.
		data, err := fetchThumbnailBytes(url)
		if err != nil || len(data) == 0 {
			// Failing URLs are not cached, so this branch can re-enter every
			// frame for a row that refuses to load; logging would spam.
			continue
		}
		rwops, err := sdl.RWFromMem(data)
		if err != nil {
			continue
		}
		tex, err := img.LoadTextureRW(renderer, rwops, false)
		rwops.Close()
		if err != nil {
			continue
		}
		var tw, th int32
		if _, _, qw, qh, err := tex.Query(); err == nil {
			tw, th = qw, qh
		}
		getThumbnailTextures().Put(url, tex, tw, th)
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

// mediaSearchTimeout bounds one yt-dlp search. Without it a wedged extractor
// (dead Wi-Fi, a stalled DNS lookup, a throttled host) left the spinner up and
// the results screen empty forever, which on the handheld reads as "search is
// broken".
const mediaSearchTimeout = 75 * time.Second

// A search is keyed by the variable it fills. Starting a newer search for the
// same variable cancels the older one, so two yt-dlp processes never compete
// for the 1 GB of RAM on a TrimUI, and the older result cannot land after the
// newer one and show videos for a query the user already replaced.
var (
	mediaSearchMu     sync.Mutex
	mediaSearchGen    = map[string]uint64{}
	mediaSearchCancel = map[string]context.CancelFunc{}
	mediaSearchBusy   int
)

// beginMediaSearch claims the result variable for a new search and returns its
// generation. The spinner is owned by the set of running searches, not by each
// one, so a cancelled run cannot clear the spinner of the run still working.
func beginMediaSearch(config *Config, resultVar string) uint64 {
	mediaSearchMu.Lock()
	defer mediaSearchMu.Unlock()
	if cancel, ok := mediaSearchCancel[resultVar]; ok && cancel != nil {
		cancel()
	}
	mediaSearchGen[resultVar]++
	mediaSearchBusy++
	if mediaSearchBusy == 1 && config != nil {
		config.Variables.LoadingSpinner = true
		config.Variables.SpinnerText = "Loading videos..."
	}
	return mediaSearchGen[resultVar]
}

// finishMediaSearch releases the search and clears the spinner once none is
// running any more.
func finishMediaSearch(config *Config) {
	// The spinner write stays inside the lock: the next search's begin and any
	// reader that observed busy==0 under the lock must see it already cleared.
	mediaSearchMu.Lock()
	defer mediaSearchMu.Unlock()
	if mediaSearchBusy > 0 {
		mediaSearchBusy--
	}
	if mediaSearchBusy == 0 && config != nil {
		config.Variables.LoadingSpinner = false
	}
}

// mediaSearchSuperseded reports whether a newer search took over the variable.
func mediaSearchSuperseded(resultVar string, gen uint64) bool {
	mediaSearchMu.Lock()
	defer mediaSearchMu.Unlock()
	return mediaSearchGen[resultVar] != gen
}

// registerMediaSearchCancel stores the cancel func for a running search.
func registerMediaSearchCancel(resultVar string, cancel context.CancelFunc) {
	mediaSearchMu.Lock()
	mediaSearchCancel[resultVar] = cancel
	mediaSearchMu.Unlock()
}

// clearMediaSearchCancel forgets a search's cancel func if it is still ours.
func clearMediaSearchCancel(resultVar string, gen uint64) {
	mediaSearchMu.Lock()
	if mediaSearchGen[resultVar] == gen {
		delete(mediaSearchCancel, resultVar)
	}
	mediaSearchMu.Unlock()
}

// sanitizeSearchVars removes the characters that would break the search
// command. The query is pasted into a double-quoted yt-dlp argument, so a
// literal quote would unbalance the tokenizer and search for the wrong text;
// typos like `song "official` are easy on the on-screen keyboard, so the quotes
// are dropped instead of failing the whole search.
func sanitizeSearchVars(vars map[string]interface{}) map[string]interface{} {
	q, ok := vars["search_query"].(string)
	if !ok || !strings.Contains(q, `"`) {
		return vars
	}
	safe := make(map[string]interface{}, len(vars))
	for k, v := range vars {
		safe[k] = v
	}
	safe["search_query"] = strings.ReplaceAll(q, `"`, " ")
	return safe
}

// sceneMediaSearchButton returns the scene button that runs a search for the
// typed query. The order prefers a button that searches over one that plays:
// submitting after typing means "find this", even when the scene also offers a
// separate "Play Link" button.
func sceneMediaSearchButton(scene SceneConfig) (Element, bool) {
	for _, want := range []string{"youtube_smart", "youtube_search", "yt_search"} {
		for _, elem := range scene.Elements {
			if elem.Type == "button" && elem.Trigger == want {
				return elem, true
			}
		}
	}
	return Element{}, false
}

// runMediaSearchTrigger runs a media scene's search action from the trigger the
// scene already declares. The on-screen Search button and the on-screen
// keyboard's submit key both come through here, so typing a query and saving it
// does exactly what pressing Search does. It reports whether the trigger was a
// media search it handled.
// mediaSearchPlan is the work a media trigger asks for, worked out without
// running anything so it can be tested.
type mediaSearchPlan struct {
	command   string // yt-dlp command to run
	resultVar string // variable the results fill
	playURL   string // play this link instead of searching
	errMsg    string // shown to the user when the trigger cannot run
}

// planMediaSearchTrigger decides what a media search trigger should do. It
// reports false for triggers that are not searches ("youtube_play" and friends).
func planMediaSearchTrigger(config *Config, trigger, target, value string) (mediaSearchPlan, bool) {
	if config == nil {
		return mediaSearchPlan{}, false
	}
	query := ""
	if v, ok := config.Variables.Custom["search_query"].(string); ok {
		query = strings.TrimSpace(v)
	}
	switch trigger {
	case "yt_search":
		if strings.TrimSpace(target) == "" {
			return mediaSearchPlan{errMsg: "This scene has no search command configured."}, true
		}
		return mediaSearchPlan{command: target, resultVar: value}, true
	case "youtube_search":
		if query == "" {
			return mediaSearchPlan{errMsg: "Type a search query first."}, true
		}
		return mediaSearchPlan{command: ytSearchCmd(config), resultVar: "search_results"}, true
	case "youtube_smart":
		if query == "" {
			return mediaSearchPlan{errMsg: "Type a search query or paste a link first."}, true
		}
		if strings.Contains(query, "youtube.com") || strings.Contains(query, "youtu.be") {
			return mediaSearchPlan{playURL: query}, true
		}
		return mediaSearchPlan{command: ytSearchCmd(config), resultVar: "search_results"}, true
	}
	return mediaSearchPlan{}, false
}

func runMediaSearchTrigger(config *Config, trigger, target, value string) bool {
	// The Media screen runs its own searches (media_hub.go): offline it looks
	// through the downloads instead of dialling, and online it asks for a full
	// grid page.
	if mediaHubInterceptSearch(config, trigger) {
		return true
	}
	plan, handled := planMediaSearchTrigger(config, trigger, target, value)
	if !handled {
		return false
	}
	if plan.errMsg != "" {
		publishCustom("search_error", plan.errMsg)
		return true
	}
	if plan.playURL != "" {
		playVideoURL(config, plan.playURL)
		return true
	}
	// The search belongs to the screen that asked for it: runSceneTask, not a
	// bare goroutine, so leaving the results screen stops the run
	// (executeYouTubeSearch spawns the scene task).
	trophyEvent(tkVideoSearch)
	executeYouTubeSearch(config, plan.command, plan.resultVar, snapshotVars(config))
	return true
}

// executeYouTubeSearch runs one yt-dlp search inside the scene's task scope:
// the spawned worker joins enterSceneWork's generation, so a scene transition
// cancels it, kills the extractor process and publishes nothing into the next
// screen. It returns immediately — it never blocks the render loop.
func executeYouTubeSearch(config *Config, command string, resultVar string, vars map[string]interface{}) {
	runSceneTask(func(ctx context.Context) {
		executeYouTubeSearchContext(ctx, config, command, resultVar, vars, false)
	})
}

// executeYouTubeSearchPage runs a follow-up page fetch. In append mode the
// result is merged into the list already on screen instead of replacing it, so
// scrolling past the last card grows the grid rather than restarting it.
func executeYouTubeSearchPage(config *Config, command string, resultVar string, vars map[string]interface{}, appendMode bool) {
	runSceneTask(func(ctx context.Context) {
		executeYouTubeSearchContext(ctx, config, command, resultVar, vars, appendMode)
	})
}

// executeYouTubeSearchContext is the blocking body behind the spawned
// wrappers. parent is the scene's context (sceneWorkContext), so the scene
// scope and the newer-search cancellation both reach the child process.
func executeYouTubeSearchContext(parent context.Context, config *Config, command string, resultVar string, vars map[string]interface{}, appendMode bool) {
	// A dead scope starts nothing: no wipe of the previous screen's results,
	// no error line, no spinner on the next screen.
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		logDebug("stream", "search for %s skipped: scene scope already cancelled", resultVar)
		return
	}
	gen := beginMediaSearch(config, resultVar)
	defer finishMediaSearch(config)
	defer clearMediaSearchCancel(resultVar, gen)
	// stale is true once a newer search owns the results variable; an old run
	// must then neither publish results nor clear the error line.
	stale := func() bool { return mediaSearchSuperseded(resultVar, gen) }
	// retryAfterUpdate runs this search once more when a failure below started
	// a yt-dlp update and it installed a newer release — but only while the
	// screen that asked is still up and nothing newer took the variable. The
	// update can land minutes after the failure (a slow transfer, or a video it
	// waited out), and a re-run then would pull the user onto the Results tab
	// or start a preload beside a video they have started since; so it also
	// needs the list to still show this failure, recently, with the player
	// idle (youTubeRetryWanted). Otherwise it stays quiet: the updater's own
	// toast already says yt-dlp was updated, and every failed search waiting
	// on one update would add a toast of its own.
	scene := currentSceneWorkName()
	retryAfterUpdate := func() {
		if stale() || currentSceneWorkName() != scene {
			return
		}
		if !youTubeRetryWanted(resultVar, time.Now()) {
			return
		}
		ShowToast("yt-dlp updated — loading the videos again", ToastKindInfo)
		executeYouTubeSearchPage(config, command, resultVar, vars, appendMode)
	}

	// staleResults is the expired page this search can paint immediately, if
	// the cache still holds one. It is what makes a repeat visit to a media
	// screen instant instead of a spinner: the grid appears from the last known
	// list and the fetch below replaces it only if the page really changed.
	var staleResults []VideoInfo
	stalePainted := false

	// The error line ("search_error") belongs to the search list. Trending and
	// Shorts keep their reason in their own failure note (noteYouTubeFailure),
	// which their tabs draw. Shared by every list, the line let a Trending
	// failure end a running search's spinner with Trending's reason, and a
	// later Trending answer wipe a failed search's reason ("No results").
	ownsErrorLine := resultVar == mediaResultsVar
	setErrorLine := func(v interface{}) {
		if ownsErrorLine {
			publishCustom("search_error", v)
		}
	}

	// publish replaces the grid with a first page: the list, a cleared error
	// line, no focus ring and the first card warmed so Play is immediate.
	publish := func(videos []VideoInfo) {
		markMediaPageSettled(resultVar, false)
		publishCustom(resultVar, videos)
		setErrorLine(nil)
		focusedResultIndex = -1
		resetResultActions()
		prefetchFirstResult(config, videos)
	}

	// deliver publishes a successful fetch. A first page replaces the list and
	// resets the focus to the top; an appended page adds to the end and focuses
	// the first new card so the scroll continues where the user left off.
	deliver := func(videos []VideoInfo) {
		if stale() {
			return
		}
		// An answer, even an empty follow-up page, ends whatever failure the
		// list showed. Failures never come through here (fail below), so this
		// clear cannot wipe the note a failure has just written.
		clearYouTubeFailure(resultVar)
		// The stale page painted the same list a moment ago: publishing it again
		// would reset the focus ring and the action row for nothing.
		if stalePainted && !appendMode && sameMediaResults(videos, staleResults) {
			return
		}
		stalePainted = false
		if appendMode {
			current, _ := config.Variables.Custom[resultVar].([]VideoInfo)
			merged, added := mergeMediaResults(current, videos)
			if added == 0 {
				markMediaPageSettled(resultVar, true)
				setErrorLine(nil)
				ShowToast("No more results", ToastKindInfo)
				return
			}
			markMediaPageSettled(resultVar, false)
			publishCustom(resultVar, merged)
			setErrorLine(nil)
			focusedResultIndex = len(current)
			prefetchVideos(config, merged, focusedResultIndex)
			return
		}
		publish(videos)
	}
	// fail publishes a fetch that failed. It used to be an error line followed
	// by an empty answer through deliver, whose "an answer ends the failure"
	// clear and publish's nil error line wiped both the list's failure note
	// (noteYouTubeFailure, written just before) and the error line
	// microseconds after they were written: Trending and Shorts said "The
	// service did not answer" and a failed search read "No results". The
	// emptied list now goes out first and the error line last, so the frame
	// that drains them shows the error, and the note is left alone.
	fail := func(msg string) {
		if stale() {
			logDebug("stream", "dropping a superseded failure for %s", resultVar)
			return
		}
		if stalePainted {
			// The grid already shows the last page for this search: a refresh
			// that failed must not replace readable results with an error line
			// or an empty grid.
			logWarn("stream", "refresh for %s failed: %s", resultVar, msg)
			return
		}
		// Settled either way: a failed follow-up page must not leave the grid
		// stuck on "Loading…", and it leaves the loaded results alone.
		markMediaPageSettled(resultVar, false)
		if appendMode {
			// The loaded pages stay readable: an error line would swap the
			// whole grid for an error card (renderSearchResults) and block
			// paging, so a failed next page says why in a toast instead.
			ShowToast(msg, ToastKindWarning)
			return
		}
		publishCustom(resultVar, []VideoInfo{})
		focusedResultIndex = -1
		resetResultActions()
		setErrorLine(msg)
	}

	vars = sanitizeSearchVars(vars)

	// Substitute variables (e.g., $search_query)
	cmdWithVars := substituteVars(command, vars)

	// If the query part is empty, replace it with "popular" while preserving
	// the same initial page size as ytSearchCmd.
	searchSpec := fmt.Sprintf(`"ytsearch%d:\s*"`, mediaSearchPageSize)
	re := regexp.MustCompile(searchSpec)
	cmdWithVars = re.ReplaceAllString(cmdWithVars, fmt.Sprintf(`"ytsearch%d:popular"`, mediaSearchPageSize))

	// Apply a playback-resolution cap if the user set one in Settings > General.
	if res, ok := vars["PlaybackResolution"].(string); ok && res != "" && res != "best" {
		cmdWithVars += fmt.Sprintf(" -f \"best[height<=%s]\"", res)
	}

	// Tokenize the command into arguments, honouring double quotes so that
	// URLs and filter expressions (which may contain characters like <, >,
	// [ ]) are passed verbatim to yt-dlp instead of being re-parsed by a shell.
	//
	// This happens before the config-provided arguments are folded in: those
	// carry the user's API key, and the tool name taken from this list is
	// written to the log.
	tokens := splitShellArgs(cmdWithVars)
	if len(tokens) == 0 {
		logError("core", "Empty command after substitution")
		fail("Empty search command")
		return
	}
	tool := strings.ToLower(strings.TrimSuffix(filepath.Base(tokens[0]), ".exe"))
	args := tokens[1:]
	// A search command may only invoke the bundled yt-dlp. The executable path
	// is then derived from that fixed name, never from the command text.
	if tool != "yt-dlp" {
		// The tool name comes from the configured command text, so it goes
		// through the same redactor as the extractor's stderr: a command line
		// that embedded a credential must not put it in the log either.
		logWarn("core", "security: executeYouTubeSearch: refusing tool %q", redactDiagnosticText(tool))
		fail("Unsupported search command")
		return
	}

	// The extra yt-dlp args from config (cookies, api_key, …) join the argument
	// list, never the command text the tool name came from.
	extraArgs := ytDlpExtraArgs(config)
	if extraArgs != "" {
		args = append(args, splitShellArgs(extraArgs)...)
	}
	// The cache key and the remembered paging command describe the whole
	// invocation, so they do include those arguments.
	fullCommand := cmdWithVars
	if extraArgs != "" {
		fullCommand += " " + extraArgs
	}

	// Remember how this list was built so the triggers can request the next
	// page of the same query. A follow-up page must not overwrite it.
	if !appendMode {
		rememberMediaPaging(resultVar, fullCommand)
	}
	if err := rejectYtDlpExecFlags(args); err != nil {
		logWarn("core", "security: executeYouTubeSearch: %v", err)
		fail("Unsupported search command")
		return
	}
	// The context bounds both the native request and the extractor run: a
	// stalled endpoint or process is killed instead of leaving the spinner and
	// an empty grid up until the app restarts, a newer search for the same
	// variable cancels this one, and — because the timeout is taken from the
	// scene's context — leaving the scene kills the work too.
	ctx, cancel := context.WithTimeout(parent, mediaSearchTimeout)
	registerMediaSearchCancel(resultVar, cancel)
	defer cancel()

	// Check bbolt cache for YouTube search results before any fetch. The cache
	// is consulted before the tool is resolved as well: a page the user has
	// already seen needs neither the network nor an installed extractor.
	cacheKey := "ytsearch:" + normalizeCacheKey(fullCommand)
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		if cached, cErr := cacheGet(db, cacheKey); cErr == nil && cached != nil {
			var videos []VideoInfo
			if json.Unmarshal(cached, &videos) == nil {
				if stale() {
					return
				}
				deliver(videos)
				return
			}
		}
		// An expired page still paints at once. The refresh that follows either
		// confirms it (and publishes nothing) or replaces it.
		if !appendMode {
			if cached, ok := cacheGetStale(db, cacheKey); ok {
				if json.Unmarshal(cached, &staleResults) == nil && len(staleResults) > 0 {
					publish(staleResults)
					stalePainted = true
				}
			}
		}
	}

	// A plain search is answered in this process (youtubesearch.go). On a
	// handheld CPU the extractor binary's startup is the largest fixed cost of
	// a search, and the site's own endpoint needs none of it. Every command
	// this path does not model falls through to yt-dlp below, and so does a
	// failed attempt: the failure is logged, not shown.
	if request, ok := nativeSearchRequestFor(fullCommand); ok {
		window, nativeErr := fetchNativeSearchAttempt(ctx, request)
		if nativeErr == nil {
			if len(window) > 0 {
				cacheMediaSearchResults(cacheKey, window)
				deliver(window)
				return
			}
			// The endpoint answered and has nothing in this window. On a
			// follow-up page that is the end of the list, which the extractor
			// would discover only after its own startup; on a first page it is
			// a reason to let yt-dlp try.
			if appendMode {
				deliver(window)
				return
			}
			nativeErr = fmt.Errorf("no result in the requested window")
		}
		if ctx.Err() != nil {
			return
		}
		if !nativeSearchSkipped(nativeErr) {
			logWarn("youtube", "native search unavailable, using yt-dlp: %v", nativeErr)
		}
	}

	// Resolve before searching. If the tool is missing, start the repair in
	// the background instead of blocking the render loop on a download; the
	// startup tool-health check may already be installing it.
	fullToolPath, toolErr := helperExecutable("yt-dlp", config)
	if toolErr != nil {
		go func() {
			if err := EnsureHelperTool("yt-dlp", config); err != nil {
				logError("core", "yt-dlp install failed: %v", err)
				showToast(toolUnavailableMessage("yt-dlp", err), ToastError())
				return
			}
			showToast("yt-dlp installed — restart JukaHub to use it", ToastSuccess())
			noteAwayToolInstalled("yt-dlp")
			refreshToolHealth()
		}()
		logWarn("core", "yt-dlp is not installed yet; install started")
		noteYouTubeFailure(resultVar, ytFailUnknown, "yt-dlp is being installed — try again in a moment")
		fail("yt-dlp is being installed — try the search again in a moment.")
		showToast("Installing yt-dlp…", ToastInfo())
		return
	}

	// Run the tool directly (no shell) so special characters are not
	// interpreted by cmd.exe / sh (e.g. "<" as input redirection).
	cmd := exec.CommandContext(ctx, fullToolPath, args...)
	cmd.Env = helperToolEnv(fullToolPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.Canceled {
		// A newer search owns the results variable now; say nothing.
		logDebug("stream", "search for %s cancelled", resultVar)
		return
	}
	if ctx.Err() == context.DeadlineExceeded {
		logError("core", "yt-dlp search timed out after %v", mediaSearchTimeout)
		reason := "Search timed out — check the connection and try again"
		noteYouTubeFailure(resultVar, ytFailTimeout, reason)
		fail(reason)
		showToast(reason, ToastWarn())
		return
	}
	if err != nil {
		logError("core", "yt-dlp execution error: %v", err)
		// Redacted: the extractor receives the cookies path as its own
		// argument, so its stderr is the one log line that can carry a
		// credential verbatim.
		logError("stderr", "%s", redactDiagnosticText(stderr.String()))
		// The cause, in words that say what happens next: a 403, a format or
		// an extractor error starts a yt-dlp update and this search runs again
		// when it lands (youTubeSearchFailure, ytdlp_update.go); the raw first
		// stderr line used to be all the grid said.
		reason := youTubeSearchFailure(config, resultVar, stderr.String(), err, fullToolPath, retryAfterUpdate)
		fail(reason)
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
		logWarn("core", "yt-dlp returned empty stdout")
		noteYouTubeFailure(resultVar, ytFailUnknown, "YouTube sent nothing back — try again in a moment")
		fail("yt-dlp returned no data. Check your query or network.")
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
			logError("core", "Failed to parse single JSON: %v", err)
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
				logError("core", "Failed to parse line as JSON: %v\nLine: %s", err, line)
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
	logDebug("core", "Parsed %d unique videos (from %d total entries)", len(videos), len(unique))

	if ctx.Err() != nil {
		// The scene was left (or a newer search took the variable) while the
		// output was being parsed: publish nothing into the next screen.
		logDebug("stream", "dropping parsed search results for %s: run cancelled", resultVar)
		return
	}

	if len(videos) == 0 && !appendMode {
		// yt-dlp answered, so whatever failure the list showed is over; but an
		// empty first page is not cached (a repeat search would read "nothing"
		// from the cache for an hour) and goes out through fail, so its line
		// is not wiped by the empty answer behind it. An empty follow-up page
		// is the end of the list, which deliver says ("No more results").
		logError("core", "No videos could be parsed from yt-dlp output")
		if !stale() {
			clearYouTubeFailure(resultVar)
		}
		fail("No videos found. Try a different query.")
		return
	}

	if stale() {
		return
	}

	// Cache successful YouTube search results for 1 hour.
	cacheMediaSearchResults(cacheKey, videos)

	deliver(videos)
}

// --- Fetch trending videos (2 columns x 6 rows = 12 videos) ---
func fetchTrendingVideos(config *Config, resultVar string, vars map[string]interface{}) {
	// debug removed
	// Use the same reliable search-based approach as youtube_search,
	// with "trending" as the query. This works unauthenticated on all devices.
	cmd := fmt.Sprintf(`yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end %d "ytsearch%d:trending"`, mediaSearchPageSize, mediaSearchPageSize)
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

// --- Rendering for search results (2 columns Ã— 6 rows) ---
func renderSearchResults(renderer *sdl.Renderer, config *Config, element Element) {
	// The Audio / Video / Chat pill rects are rebuilt by the list renderer
	// every frame; clearing them here means a grid (which draws no pills), an
	// error card or an empty state cannot answer a tap with a pill a previous
	// frame recorded.
	resultActionHits = resultActionHits[:0]
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
				// DangerInk, not a hardcoded red: the palette is retinted per
				// theme, and the fixed #FF6B7A this used to draw fell under
				// AA on the light presets, on the one screen that reports
				// failures.
				renderText(renderer, config, font, line, DangerInk(),
					element.X+(elemWidth-int32(w))/2, y+int32(i)*lineH)
			}
			hint := "See Logs for details."
			w, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint, ColorTextTertiary(),
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
				// Theme inks: the fixed cyan and slate grey were the default
				// palette's, so every other preset had an off-brand spinner.
				renderSpinner(renderer, element.X+elemWidth/2-15, element.Y+elemHeight/2-40, 24, glassAccent())
				w, _, _ := font.SizeUTF8(config.Variables.SpinnerText)
				renderText(renderer, config, font, config.Variables.SpinnerText, ColorTextSecondary(),
					element.X+(elemWidth-int32(w))/2, element.Y+elemHeight/2+4)
			} else {
				// The same block the rest of the app draws (Favorites, Shorts,
				// the folder lists): a glyph, a headline and one line that says
				// what fills it. A single grey sentence floating in a
				// full-height panel read as a screen that had failed.
				UIEmptyState(renderer, config, sdl.Rect{X: element.X, Y: element.Y, W: elemWidth, H: elemHeight},
					"@media", "No results yet", "Search above, or paste a link with Play Link.")
			}
		}
		return
	}

	cols, _ := gridColsRows(element)
	elemWidth := getElementWidth(element, 1232)
	elemHeight := getElementHeight(element, 456)

	if cols <= 1 {
		renderSearchResultsList(renderer, config, element, videos, elemWidth, elemHeight)
		renderMediaNextPageButton(renderer, config, element, videos, elemWidth, elemHeight)
		return
	}
	renderSearchResultsGrid(renderer, config, element, videos, cols, elemWidth, elemHeight)
	renderMediaNextPageButton(renderer, config, element, videos, elemWidth, elemHeight)
}

const (
	// searchResultRowH and searchResultRowGap are the list layout's card
	// height and the gap under it. The renderer, the click path and the page
	// size all measure from them, so a tap can never address a different row
	// than the one under the finger.
	searchResultRowH   = int32(100)
	searchResultRowGap = int32(14)
)

// searchResultListRowRect is where the list layout draws row index at the
// current smooth-scroll offset. Rows outside the viewport are returned too —
// the clip hides them, and searchResultIndexAt hit-tests the one it needs.
func searchResultListRowRect(element Element, index int) sdl.Rect {
	return sdl.Rect{
		X: element.X,
		Y: element.Y + int32(index)*(searchResultRowH+searchResultRowGap) - scrollY,
		W: getElementWidth(element, 1232),
		H: searchResultRowH,
	}
}

// searchResultIndexAt maps a click to the result card under it, in whichever
// layout the element draws: the single-column list (searchResultListRowRect)
// or the multi-column grid (the same cell math renderSearchResultsGrid uses).
// It walks the whole loaded list — paging appends results — and returns -1
// when the click landed on no card.
func searchResultIndexAt(element Element, videos []VideoInfo, mx, my int32) int {
	// The viewport owns the tap: a row scrolled under the top edge (or half
	// under the bottom) still has geometry there that the clip hides, and a
	// press in the control band above the results must not play it.
	if mx < element.X || mx > element.X+getElementWidth(element, 1232) ||
		my < element.Y || my > element.Y+getElementHeight(element, 456) {
		return -1
	}
	cols, rows := gridColsRows(element)
	if cols <= 1 {
		for idx := range videos {
			row := searchResultListRowRect(element, idx)
			if mx >= row.X && mx <= row.X+row.W && my >= row.Y && my <= row.Y+row.H {
				return idx
			}
		}
		return -1
	}
	elemWidth := getElementWidth(element, 1232)
	elemHeight := getElementHeight(element, 456)
	cellWidth := (elemWidth - 30) / cols
	cellHeight := (elemHeight - 30) / rows
	for idx := range videos {
		col := int32(idx) % cols
		row := int32(idx) / cols
		xPos := element.X + col*(cellWidth+10)
		yPos := element.Y + row*(cellHeight+10) - scrollY
		if mx >= xPos && mx <= xPos+cellWidth && my >= yPos && my <= yPos+cellHeight {
			return idx
		}
	}
	return -1
}

// focusMediaResultAt makes mouse hover behave like D-pad selection in the media
// results. It does not start stream resolution; it only warms preview artwork.
func focusMediaResultAt(config *Config, mx, my int32) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	for elementIndex, element := range scene.Elements {
		if element.Type != "searchresults" {
			continue
		}
		videos, ok := config.Variables.Custom[element.Variable].([]VideoInfo)
		if !ok {
			continue
		}
		// The Next chip is painted above the result rows. Hovering it must
		// never move selection to the row beneath it.
		if config.Variables.Custom["search_error"] == nil && mediaNextPageHit(config, element, videos, mx, my) {
			return false
		}
		index := searchResultIndexAt(element, videos, mx, my)
		if index < 0 {
			continue
		}
		if focusedResultIndex != index {
			resetResultActions()
			focusedResultIndex = index
			prefetchCardPreviewFrames(videos[index])
		}
		selectedButtonIndex = elementIndex
		focusEngine.SetByElementIndex(elementIndex)
		return true
	}
	return false
}

// mediaPreviewFrameURLs returns YouTube's optional fixed preview samples. They
// are not animated GIFs or a full storyboard, and may be missing for a video.
func mediaPreviewFrameURLs(vid VideoInfo) []string {
	id := webYouTubeVideoID(strings.TrimSpace(vid.GetURL()))
	if id == "" {
		return nil
	}
	base := webYouTubeThumbBase + url.PathEscape(id) + "/"
	return []string{base + "1.jpg", base + "2.jpg", base + "3.jpg"}
}

const mediaPreviewFrameInterval = uint64(900)

// mediaPreviewFrameIndex changes at a relaxed pace so cycling preview samples
// doesn't compete with video playback or render work on handhelds.
func mediaPreviewFrameIndex(ticks uint64, count int) int {
	if count <= 0 {
		return -1
	}
	return int(ticks/mediaPreviewFrameInterval) % count
}

// renderSearchResultsList renders results as one readable vertical list:
// ~100px rows with 16:9 thumbnails, one measured title line and one measured
// metadata line (both ellipsized), and the duration badge inside the
// thumbnail. The viewport is SDL-clipped so rows can never escape the content
// area, and the focused row keeps its own bright cyan ring.
func renderSearchResultsList(renderer *sdl.Renderer, config *Config, element Element, videos []VideoInfo, elemWidth, elemHeight int32) {
	// The pills are recorded by this frame only: a list drawn twice (a resize,
	// or two renders in one test) must not leave the first frame's rects to
	// shadow the second's, and a direct call to this layout must be as
	// self-contained as renderSearchResults' other branches.
	resultActionHits = resultActionHits[:0]
	rowH, gap := searchResultRowH, searchResultRowGap

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
		// The same rect the click path tests, so prefetch, draw and tap
		// can never disagree about where a row is.
		y := searchResultListRowRect(element, i).Y
		if y+rowH < element.Y || y > element.Y+elemHeight {
			if i < focusedResultIndex+3 {
				prefetchCardThumbnail(vid)
			}
			continue
		}
		prefetchCardThumbnail(vid)
	}

	// The Audio / Video / Chat pills are recorded while drawing so a tap
	// addresses the pill it landed on; the list is the only layout that owns
	// Left/Right. Last frame's rects were cleared in renderSearchResults.
	renderWithClip(renderer, element.X, element.Y, elemWidth, elemHeight, func(r *sdl.Renderer) {
		for i, vid := range videos {
			y := searchResultListRowRect(element, i).Y
			if y+rowH < element.Y || y > element.Y+elemHeight {
				continue
			}

			cardX, cardY := element.X, y
			cardW, cardH := elemWidth, rowH
			focused := i == focusedResultIndex

			// Home's glass card: the translucent fill and hairline at rest, the
			// brighter fill and Home's 3px accent ring when focused — the ring on
			// the card's own edge, because the list clips at its viewport.
			drawGlassSurface(r, sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, glassControlRadius,
				glassState{Focused: focused, NoLift: true})

			// recently-played accent bar
			if isRecentlyPlayed(vid.GetURL()) {
				fillRoundedRect(r, cardX+2, cardY+8, 3, cardH-16, 2, ColorDanger)
			}

			// 16:9 thumbnail, vertically centered on the left.
			thumbW, thumbH := int32(168), int32(95)
			thumbX, thumbY := cardX+10, cardY+(cardH-thumbH)/2
			drawResultThumbnail(r, config, vid, i, thumbX, thumbY, thumbW, thumbH)
			drawDurationBadge(r, config, font, vid.Duration, thumbX+thumbW-56, thumbY+thumbH-22, 48, 16)

			// Audio / Video / Chat / Save / Queue pills own the right edge of the
			// row; the title column stops where they begin, on every row so text
			// never jumps as the highlight moves.
			textLimit := drawResultActions(r, config, font, sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, i, focused, favoriteVideoSaved(vid.GetURL()), upNextHas(vid.GetURL()))

			// Measured text block, vertically centered against the thumbnail.
			textX := thumbX + thumbW + 16
			textW := textLimit - textX
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
				titleColor = WarningInk()
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
			// Home's card: glass, with Home's bloom, lift and ring when focused.
			card := sdl.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}
			if i == focusedResultIndex {
				drawGlassFocusGlow(r, card)
			}
			drawGlassSurface(r, card, glassControlRadius, glassState{Focused: i == focusedResultIndex})

			if isRecentlyPlayed(vid.GetURL()) {
				fillRoundedRect(r, cardX+2, cardY+8, 3, cardH-16, 2, ColorDanger)
			}

			// Thumbnail with duration badge inside its bottom-right corner.
			drawResultThumbnail(r, config, vid, i, cardX+8, cardY+8, thumbWidth, thumbHeight)
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
				titleColor = WarningInk()
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

// realThumbnailCandidates lists a media card's artwork URLs in preference
// order: the metadata's own fields first, then — when a YouTube entry ships no
// artwork at all (yt-dlp's flat-playlist Shorts rows and some extractors omit
// them on search results) — the real thumbnail derived from the video URL
// alone. That is the same yt-dlp-free derivation the reader's play cards use
// (webpage_video.go), so a Shorts or search-result card paints its actual
// frame through the shared disk-backed cache and decode budget instead of a
// generic tile.
func realThumbnailCandidates(vid VideoInfo) []string {
	urls := make([]string, 0, 2+len(vid.Thumbnails))
	if vid.Thumbnail != "" {
		urls = append(urls, vid.Thumbnail)
	}
	urls = append(urls, vid.Thumbnails...)
	if u := webYouTubeThumbURL(strings.TrimSpace(vid.GetURL())); u != "" {
		urls = append(urls, u)
	}
	return urls
}

// prefetchCardThumbnail warms the artwork URL the card will paint first.
func prefetchCardThumbnail(vid VideoInfo) {
	if urls := realThumbnailCandidates(vid); len(urls) > 0 {
		PrefetchThumbnail(urls[0])
	}
}

// prefetchCardPreviewFrames warms YouTube's optional fixed preview samples for
// the selected card. Prefetch is non-blocking and shared with the normal image
// cache; absent samples simply leave the poster thumbnail in place.
func prefetchCardPreviewFrames(vid VideoInfo) {
	for _, frameURL := range mediaPreviewFrameURLs(vid) {
		PrefetchThumbnail(frameURL)
	}
}

// cachedThumbnailTexture may decode an already-prefetched in-memory payload,
// but never reads disk or touches the network. This keeps the animated preview
// from adding a blocking fetch to the render loop on a handheld.
func cachedThumbnailTexture(renderer *sdl.Renderer, rawURL string) *sdl.Texture {
	url := normalizeThumbnailURL(rawURL)
	if tex := getThumbnailTextures().Get(url); tex != nil {
		return tex
	}
	data := thumbnailDataGet(url)
	if len(data) == 0 || !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
		return nil
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil {
		return nil
	}
	tex, err := img.LoadTextureRW(renderer, rwops, false)
	_ = rwops.Close()
	if err != nil {
		return nil
	}
	var w, h int32
	if _, _, tw, th, err := tex.Query(); err == nil {
		w, h = tw, th
	}
	getThumbnailTextures().Put(url, tex, w, h)
	return tex
}

// drawResultThumbnail keeps the normal poster for idle rows and cycles through
// any cached YouTube preview samples on the selected row. Missing samples are
// skipped, and the poster is always the fallback.
func drawResultThumbnail(renderer *sdl.Renderer, config *Config, vid VideoInfo, index int, x, y, w, h int32) {
	if index == focusedResultIndex {
		urls := mediaPreviewFrameURLs(vid)
		if len(urls) > 0 {
			start := mediaPreviewFrameIndex(sdl.GetTicks64(), len(urls))
			for offset := 0; offset < len(urls); offset++ {
				frameURL := urls[(start+offset)%len(urls)]
				if tex := cachedThumbnailTexture(renderer, frameURL); tex != nil {
					renderer.Copy(tex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
					return
				}
			}
		}
	}
	drawThumbnail(renderer, config, vid, x, y, w, h)
}

// drawThumbnail renders the cached thumbnail, a placeholder, or a dark tile so
// a row never shows an empty hole. The artwork pass walks the card's candidate
// URLs in order — metadata first, the URL-derived YouTube artwork last — so a
// card misses out on real art only when both the extractor and the CDN come up
// empty.
// It reports whether real artwork was painted, so a caller that wants a glyph
// on an empty frame (the Favorites library rows) can draw one.
func drawThumbnail(renderer *sdl.Renderer, config *Config, vid VideoInfo, x, y, w, h int32) bool {
	loaded := false
	if tex := loadThumbnailFromURLs(renderer, realThumbnailCandidates(vid)); tex != nil {
		renderer.Copy(tex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
		loaded = true
	}
	if !loaded {
		if placeholderTexture != nil {
			renderer.Copy(placeholderTexture, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
		} else {
			fillRoundedRect(renderer, x, y, w, h, 4, ColorIconSurface)
		}
	}
	return loaded
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
		logDebug("core", "playVideoURL: empty URL, nothing to play")
		return
	}
	v := VideoInfo{URL: url}
	playVideoInfo(config, v)
	// A pasted YouTube link has no title: look it up so Continue and Recents
	// do not show the address (ytoembed.go).
	ytFillMissingTitle(v)
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
	// Every Play routes through here, so this is where the screen a video was
	// started from is recorded (media_hub.go): Back from a file opened in Files
	// returns to Files, Back from Media's own player stays in Media.
	mediaNotePlayOrigin(config)
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
		logDebug("core", "playVideoInfo: empty URL, nothing to play")
		return
	}
	// Starting any video leaves the Shorts feed unless the caller re-arms it
	// (startShortAt does), so a normal video does not auto-advance a feed.
	shortsAutoplay = false
	// Remember the list behind this video (results or the Shorts feed) so the
	// shoulder triggers can skip forward and back while it plays.
	setPlaybackQueue(config, v)
	// Video and music share one audio device: silence the playlist first.
	musicStopIfPlaying()
	ensureVideoScene(config)
	PlayActivateSound()
	addRecentVideo(v)
	currentPlaybackURL = url
	// A quality pin belongs to one video; a different URL forgets it.
	syncVideoQualitySource(url)

	// The loading state begins here, on the press, and not after the helper
	// tools are resolved. On a cold card that window is spent downloading
	// yt-dlp with the install card up, and it used to leave the results grid
	// live underneath it: the d-pad scrolled behind a spinner the user could
	// not dismiss. The card is modal from this line on.
	prepareSeq, prepareCtx := beginVideoPrepare(v.Title)

	// The embedded player is the only playback backend: yt-dlp downloads the
	// stream and ffmpeg decodes it into the scene's `video` element. The old
	// ffplay pipe is gone because the bundled ffplay is linked against X11 and
	// aborts on the handheld ("libX11.so.6: cannot open shared object file"),
	// which left every video stuck on a black screen.
	go func() {
		// A cancelled load played nothing, so it must not count as a play in the
		// recently-played list or the playtime figures.
		outcome := resolveAndStartPlayback(prepareSeq, prepareCtx, config, url, v.Position, "playVideoInfo")
		if outcome.Played() {
			recordPlayed(config, url)
		}
		// Only a load that really reached the stream says anything about the
		// link: a missing tool or a cancel is not the channel's fault, and a
		// saved IPTV channel would otherwise be painted red for it.
		if outcome.Tried() && prepareCtx.Err() == nil {
			ipStreamRecordResult(url, outcome.Played())
		}
	}()
}

// videoFormatSelectorFor is videoFormatSelector aware of one video: the
// per-video ceiling (Quality row in the player settings) wins over the global
// playbackResolution when it is pinned to this source.
func videoFormatSelectorFor(config *Config, source string) string {
	return fmt.Sprintf("best[height<=%d]/best", effectivePlaybackMaxHeight(config, source))
}

// playWithTempFile downloads the stream with yt-dlp and hands the file to the
// embedded player. Downloading first (instead of piping) keeps seeking and
// resume positions working and avoids holding two decoders in memory.
// It reports whether playback started; every failure path (spawn, download,
// empty file, cancel) returns false, so callers never mistake "the process
// exited" for "something played".
func playWithTempFile(parent context.Context, config *Config, ytDlpPath, url string, startSec float64, prepareSeq uint64) bool {
	// The caller's spinner covers the download; every way out of here has to
	// clear it, including failures, or the media screen stays blocked.
	defer finishVideoPrepare(prepareSeq)
	logDebug("core", "playWithTempFile: downloading to temp file for %s (resume %.1fs)", sanitizeLogURL(url), startSec)

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

	// yt-dlp prints its [download] progress on stdout, so both streams feed
	// the parser; the buffer is locked because the loop below reads it while
	// the helper is still writing.
	stderrBuf := &lockedTailBuffer{max: 64 << 10}
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
		// The clients that still carry a muxed format (streaming.go explains
		// the list and why android_vr and plain web left it).
		"--extractor-args", ytDlpDownloadClients,
		"-f", videoFormatSelectorFor(config, url),
		url,
	}
	dlArgs = append(dlArgs, ytDlpExtraArgsSlice(config)...)
	dlCmd := exec.CommandContext(ctx, ytDlpPath, dlArgs...)
	dlCmd.Env = helperToolEnv(ytDlpPath)
	dlCmd.Stdout = stderrBuf
	dlCmd.Stderr = stderrBuf

	if err := dlCmd.Start(); err != nil {
		logError("core", "playWithTempFile: download failed to start: %v", err)
		showToast("Download failed to start.", ToastError())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackError = err.Error()
		videoPlaybackMutex.Unlock()
		return false
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
					// Cancelled by B / Esc: not a network failure, so no error
					// toast and no half-download left behind.
					logDebug("stream", "download cancelled for %s", sanitizeLogURL(url))
					removeTempVideoArtifacts(tempPath)
					if parent.Err() == nil && ctx.Err() == context.DeadlineExceeded {
						// The five-minute cap, not the user: a long video on a
						// slow link used to end here with nothing on screen.
						showToast("This video took too long to download — try a lower quality in Settings", ToastWarn())
					}
					return false
				}
				// A failed fetch leaves its partial pieces too; each attempt has
				// a new name, so nothing would ever reuse or remove them.
				removeTempVideoArtifacts(tempPath)
				logError("core", "playWithTempFile: download failed: %v", err)
				// Same redaction as the search path: these arguments include the
				// cookies path.
				logError("core", "playWithTempFile: stderr: %s", redactDiagnosticText(stderrBuf.String()))
				// The cause in words (ytfailure.go), not "Check network." for a
				// 403, a missing format or an outdated helper — which also
				// starts the yt-dlp update that fixes those.
				showToast(youTubePlaybackFailureMessage(config, url, stderrBuf.String()), ToastError())
				videoPlaybackMutex.Lock()
				videoPlaybackPhase = "error"
				videoPlaybackPhaseAt = sdl.GetTicks64()
				videoPlaybackError = err.Error()
				videoPlaybackMutex.Unlock()
				return false
			}
			downloadOK = true
		case <-time.After(200 * time.Millisecond):
			// Parse progress from stderr
			parseDownloadProgress(stderrBuf.String())
		}
	}

	if ctx.Err() != nil {
		logDebug("stream", "download cancelled after it finished for %s", sanitizeLogURL(url))
		removeTempVideoArtifacts(tempPath)
		return false
	}

	// Verify the downloaded file is valid before playing
	info, err := os.Stat(tempPath)
	if err != nil || info.Size() == 0 {
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		logError("core", "playWithTempFile: downloaded file is missing or empty (size=%d)", size)
		showToast("Download failed. File is empty.", ToastError())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackError = "downloaded file is empty"
		videoPlaybackMutex.Unlock()
		removeTempVideoArtifacts(tempPath)
		return false
	}
	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "playing"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 1.0
	videoPlaybackMutex.Unlock()

	// tempFile=true: this session owns the downloaded copy and removes it when
	// playback ends. User media files are never deleted.
	startEmbeddedPlayback(config, tempPath, startSec, true)
	logDebug("core", "playWithTempFile: embedded playback started for %s (resume %.1fs)", tempPath, startSec)
	return true
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

// runInputModal blocks until the keyboard closes. Only the text-browser address
// bar uses it: it needs the submitted value before it can load the page.
func runInputModal(renderer *sdl.Renderer, config *Config) {
	defer func() { virtualKeyboardActive = false }()
	vkEnsureLayout()
	exitInput := false
	for !exitInput {
		// An exit request from outside the loop (a stop signal, the power
		// key's shutdown path, the companion's Exit) used to wait for the
		// keyboard to be closed by hand — long enough for the firmware to give
		// up and cut power. The main loop owns the exit, so hand back to it.
		if exitRequested.Load() {
			vkCloseKeyboard()
			return
		}
		drainCustom(config)
		vkTickNavRepeat()
		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			renderScene(renderer, config, config.Scenes[currentSceneIndex])
		}
		renderKeyboard(renderer, config)
		// The software dimmer covers this loop's frames too (brightnessctl.go).
		brightnessSoftRender(renderer)
		renderer.Present()

		quit := false
		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			applyButtonSwap(config, event)
			switch event.(type) {
			case *sdl.KeyboardEvent, *sdl.TextInputEvent, *sdl.ControllerButtonEvent,
				*sdl.MouseButtonEvent, *sdl.TouchFingerEvent:
				// Typing is input: without this the idle clock kept running
				// while the keyboard was up, and closing it after a long entry
				// dropped straight into the screensaver.
				screensaverNotifyInput()
				if frameRateCtrl != nil {
					frameRateCtrl.NotifyInput()
				}
			}
			switch e := event.(type) {
			case *sdl.QuitEvent:
				// Closing the window while typing used to be swallowed here.
				quit = true
			case *sdl.KeyboardEvent:
				// The browser key that opened this keyboard (/, Y, F, Space,
				// A) may still be held: its repeats neither type nor act.
				if browserTextOpenerRepeat(e) {
					continue
				}
				if e.Type != sdl.KEYDOWN {
					continue
				}
				// Caret keys, Delete, Ctrl+A/C/X/V/Z and an open accent popup
				// (vkedit.go), the same as in the main loop.
				if vkHandlePhysicalKey(e, config) {
					exitInput = !virtualKeyboardActive
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
					exitInput = !virtualKeyboardActive
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
				// The keyboard runs its own event loop, so the system quit chord
				// has to be checked here as well as in the main loop.
				if exitChordPressed(e) {
					vkCloseKeyboard()
					requestAppExit()
					return
				}
				// The modal owns the pad while the keyboard is up, so the
				// accessibility chord and swaps have to be applied here too.
				// Otherwise one-handed mode worked everywhere except the place a
				// user types, which is exactly where it is needed most.
				if a11yRemapEvent(e) {
					continue
				}
				if e.Type != sdl.CONTROLLERBUTTONDOWN {
					continue
				}
				// The same handler as the main loop: the modal input owns the pad
				// while the keyboard is up, and any button that closes it leaves
				// the modal loop.
				if vkHandleControllerButton(e.Button, config) && !virtualKeyboardActive {
					exitInput = true
				}
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
					// The keyboard routes the click (tools, strip, field, keys,
					// or the dimmed content behind it, which closes it).
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					if vkHandlePointerDown(mx, my, vkHoldMouse, config) {
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
		if quit {
			// Re-queued after the drain (inside it, PollEvent would hand the
			// same event straight back) for the main loop to act on.
			vkCloseKeyboard()
			requeueQuit()
			return
		}
		// Cap the modal at the main loop's frame rate. Without a delay this loop
		// spun flat out for as long as the address bar was open.
		sdl.Delay(16)
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
		if textBrowserSearchEditing {
			variable = browserSearchVariable
		} else if textBrowserFindEditing {
			variable = browserFindVariable
		} else if textBrowserAddressEditing {
			variable = textBrowserURLVariable
		} else if browserFormEditing {
			// A field on the page. Without this the typed text went to the
			// element's own variable — the browser's page content — so filling a
			// search box replaced the page behind the keyboard.
			variable = browserFormVariable
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
	_ = pushSyntheticMouseMotion(px, py)
	_ = pushSyntheticMouseButton(sdl.BUTTON_LEFT, sdl.PRESSED, px, py)
	_ = pushSyntheticMouseButton(sdl.BUTTON_LEFT, sdl.RELEASED, px, py)
}

// disableSyntheticTouchMouse turns off SDL's built-in touch-to-mouse mapping
// ("SDL_TOUCH_MOUSE_EVENTS"). SDL posts its own mouse press/release at
// finger-down for every touch, on top of the app's own tap pipeline
// (handleTouchGesture replays FINGERUP taps as clicks), so one tap dispatched
// the page's action twice: opening a reader page's video card started two
// yt-dlp resolves for one URL, and a virtual-keyboard tap inserted a
// character twice. Real mice never send finger events, which is why the
// doubling only shows up on touch devices. The app's replay keeps working,
// and this is safe to call more than once.
func disableSyntheticTouchMouse() {
	if !sdl.SetHint("SDL_TOUCH_MOUSE_EVENTS", "0") {
		logDebug("core", "disableSyntheticTouchMouse: SDL_TOUCH_MOUSE_EVENTS hint was not set")
	}
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
	// JukaLand (its screen, or played on the browser's offline page) owns the
	// touch screen: a drag across the world is not "change the scene".
	if jlandKeysCaptured(config) {
		return
	}
	if imageViewerPath != "" {
		// A swipe pans a zoomed picture and browses an unzoomed one
		// (imageviewer.go); the picture follows the finger.
		switch direction {
		case "up":
			imgvPanBy(0, -screenHeight/5)
		case "down":
			imgvPanBy(0, screenHeight/5)
		case "left":
			if imgvZoomed() {
				imgvPanBy(-screenWidth/5, 0)
			} else {
				imageViewerStep(1)
			}
		case "right":
			if imgvZoomed() {
				imgvPanBy(screenWidth/5, 0)
			} else {
				imageViewerStep(-1)
			}
		}
		return
	}
	// The Media screen uses swipes for its own tabs and lists (media_hub_input.go)
	// instead of letting them change the whole scene.
	if mediaHubSwipe(direction, config) {
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
	// Fingers spreading zoom in around the screen centre (imageviewer.go).
	if direction == "out" {
		imgvZoomBy(1, screenWidth/2, screenHeight/2)
	} else {
		imgvZoomBy(-1, screenWidth/2, screenHeight/2)
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
	applyVariableOverrides(config)
	// One snapshot helper for every save, so a settings change cannot drop the
	// Quick Settings levels on the way to disk.
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// dropCustomMirror removes the Custom keys that shadow a typed setting,
// matched case-insensitively the way applyVariableOverrides matches them. A
// setter calls it before syncVariableOverrides: otherwise a stale copy — the
// shipped config's flattened {"Fullscreen": false}, or the lowPower and
// reducedMotion values the battery saver writes — was read back over the value
// the user had just chosen, and the toggle snapped back while its switch still
// showed the new state.
func dropCustomMirror(config *Config, name string) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	for k := range config.Variables.Custom {
		if strings.EqualFold(k, name) {
			delete(config.Variables.Custom, k)
		}
	}
}

// applyVariableOverrides copies same-name Custom values onto the typed fields
// without saving anything. Loading a config uses it: Variables.UnmarshalJSON
// used to call the saving form, so every load scheduled a jukauser.json write
// built from jukaconfig.json — at boot, before jukauser.json was even read,
// with the Quick Settings levels zeroed — and a crash or power cut inside the
// debounce left the user's colours, weather unit, name and levels reset.
func applyVariableOverrides(config *Config) {
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
	setStr("powerMode", &config.Variables.PowerMode)
	setBool("dynamicBg", &config.Variables.DynamicBg)
	setBool("clock12", &config.Variables.Clock12)
	setStr("weatherUnit", &config.Variables.WeatherUnit)
	setStr("tspUsername", &config.Variables.TSPUsername)
	setStr("playbackResolution", &config.Variables.PlaybackResolution)
	setStr("subtitleLanguage", &config.Variables.SubtitleLanguage)
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
	case "subtitleLanguage":
		return config.Variables.SubtitleLanguage
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
	case "powerMode":
		return currentPowerMode(config)
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
	case "clock12":
		if config.Variables.Clock12 {
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
	isActive := (sceneIdx == activeSceneIndex && elemIdx == activeElementIndex)
	// A field the D-pad rests on (but is not typing into) wore no mark at all:
	// only an active keyboard drew the ring, so stepping onto a colour field in
	// Settings → Appearance looked like the focus had vanished. It now takes
	// Home's 3px ring on its own edge, the way a focused list row does.
	isFocused := !isActive && sceneIdx == currentSceneIndex && elemIdx == selectedButtonIndex &&
		chromeTier(focusEngine.ElementIndex()) == 0

	// Home's glass, a step recessed: the field is a darker pane than a button
	// (the author's or the theme's input colour kept as a wash under it), with
	// a hairline at rest and Home's accent ring and bloom while it is typing.
	field := sdl.Rect{X: element.X, Y: element.Y, W: width, H: height}
	r := glassControlRadius
	if height < 2*r {
		r = height / 2
	}
	if isActive {
		drawGlassFocusGlow(renderer, field)
	}
	if strings.TrimSpace(element.BgColor) != "" {
		if wash := resolveColor(config, element.BgColor, sdl.Color{}); wash.A > 0 {
			fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, r, WithAlpha(wash, 150))
		}
	}
	fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, r, glassA(10))
	switch {
	case isActive:
		strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, r, 2, glassAccent())
	case isFocused:
		drawGlassFocusRing(renderer, field, r)
	default:
		strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, r, 1, glassA(26))
	}

	textColor := resolveColor(config, element.Color, ColorTextPrimary())
	font, _ := getCachedFont(config, element.Font)

	// Color settings render the picked color itself. A bare "#3498db" told the
	// user nothing about what they had chosen, and the swatch follows the live
	// buffer so it previews as the value is typed.
	textX := element.X + 14
	// The value and the placeholder sit on the field's vertical centre: the
	// old fixed 12px offset only centred the 24px default font in a 48px field.
	textY := element.Y + 12
	if font != nil {
		textY = element.Y + (height-int32(font.Height()))/2
	}
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
		// Two hairlines, dark outside and light inside, so the chip's edge reads
		// on a light theme as well as a dark one whatever colour it holds: the
		// single light ring vanished on the light presets.
		strokeRoundedRect(renderer, sx-2, sy-2, size+4, size+4, radius+2, 1, sdl.Color{R: 0, G: 0, B: 0, A: 110})
		strokeRoundedRect(renderer, sx-1, sy-1, size+2, size+2, radius+1, 1, sdl.Color{R: 255, G: 255, B: 255, A: 120})
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
		renderText(renderer, config, font, draw, textColor, textX, textY)
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
		renderText(renderer, config, font, placeholder, phColor, textX, textY)
	}
}

// --- renderScene ---
func renderScene(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	// A registered scene owns its whole body. Everything else falls through to
	// the config-driven element renderer below, which is the common case.
	if m, ok := lookupScene(scene.Name); ok && m.render(renderer, config) {
		// Most of these scenes draw the shared status bar themselves, so the
		// menus its Apps pill, weather chip and bell open must float over them
		// too. They were never drawn here: Start opened an invisible menu that
		// still took every press until B closed it.
		renderer.SetClipRect(nil)
		renderTopBarOverlays(renderer, config)
		return
	}

	refreshHomeLayout(config)

	// The home scene renders through the dedicated modern renderer so it can
	// use its own header, Continue card, focus treatment, and footer hints.
	// Generic config-driven element rendering stays for every other scene.
	//
	// It runs before the background below on purpose: the layout fills the
	// screen with the theme colour itself, so painting the backdrop image or
	// gradient here first was a full-screen blit and a blended full-screen fill
	// that the next call covered — a second copy of that dead work, on top of
	// the one renderHomeModern used to do.
	if homeLayoutActive {
		// Home draws its own header (with the same status cluster and the
		// notification bell) and its own footer, so the shared bars must not
		// leave stale hit areas behind for this frame.
		sceneTitleBackRect = sdl.Rect{}
		statusClockRect = sdl.Rect{}
		tb.startRect = sdl.Rect{}
		renderHomeModern(renderer, config)
		// With no Apps pill measured the Start menu anchors under the top bar.
		renderTopBarOverlays(renderer, config)
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetClipRect(nil)
		return
	}

	ensureBackgroundTexture(renderer, config)

	// Every scene stands on Home's backdrop (ui_glass.go): the navy base, the
	// two colour blooms and the ambient layer, or the user's own wallpaper
	// under them, or the dynamic background when that setting is on. The
	// opaque slate panel that used to frame the body is gone: Home has no frame
	// around its cards, and the panel was what made a scene read as a dialog
	// box rather than a page of the same app. It was also a 1232x590 rounded
	// texture blended over the whole body on every frame, on top of a
	// full-screen copy of the 2560x1440 stock wallpaper and a full-screen dim.
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

	// The classic Tools menu draws its own page header and section headings; its
	// buttons were placed as tiles when the scene was entered (menutiles.go).
	if menuTilesScene(scene) {
		renderMenuTilesDecor(renderer, config, scene)
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
			logWarn("core", "Unknown element type: %s", elem.Type)
		}
	}

	// Settings → Appearance shows the palette itself, not just its hex codes.
	if scene.Name == "SettingsAppearance" {
		renderAppearancePreview(renderer, config)
	}

	// Settings → General's Video snaps row reports how many of the card's games
	// have a clip. The scan runs on the scene's own goroutine; this only writes
	// the finished count into the row.
	if scene.Name == "SettingsGeneral" {
		snapPreviewSyncRow(config)
	}

	if brwFullscreenScene(scene) {
		// The full-screen Browser is the one config scene without the shared
		// bars: its page is the whole screen, and Select opens the menu that
		// holds what the bars and its toolbar did (brw_fullscreen.go). The
		// bar's hit areas go with it, or the screen before's would still click.
		brwClearChromeHits()
	} else {
		// Single global header: brand + version + active scene, the Apps pill and
		// the right-side system cluster (network, weather, clock, battery, bell).
		renderStatusBar(renderer, config)

		// The bottom of every menu is reserved for buttons now: the controller
		// hint bar, plus the app menu overlay when it is open.
		renderFooter(renderer, config)
	}
	renderTopBarOverlays(renderer, config)

	// Reset renderer state after rendering to avoid state leakage
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)
}

// renderTopBarOverlays draws the menus the top bar opens, in their stacking
// order: the weather card, the Start menu, the notification list. Each is a
// no-op while closed. Every scene path in renderScene ends with this, Home and
// the self-drawing registered scenes included.
func renderTopBarOverlays(renderer *sdl.Renderer, config *Config) {
	renderWeatherPopover(renderer, config)
	renderVolumePopover(renderer, config)
	renderBrightnessPopover(renderer, config)
	renderStartMenu(renderer, config)
	renderNotifDropdown(renderer, config)
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

// themeGalleryTab is which tab of the gallery is open. Switching tabs resets
// the focus and the scroll: a colour preset and a total conversion are not
// positions in one list, so carrying an index across would land on a card the
// user never looked at.
var themeGalleryTab int

// themeGalleryTabH is the strip above the cards: one pill per tab.
const themeGalleryTabH = int32(40)

// themeGalleryCurrentTab clamps the open tab and returns it.
func themeGalleryCurrentTab() themeTab {
	if themeGalleryTab < 0 || themeGalleryTab >= len(themeGalleryTabs) {
		themeGalleryTab = 0
	}
	return themeGalleryTabs[themeGalleryTab]
}

// themeGalleryPresets is the list the grid draws: the presets of the open tab,
// in the canonical order.
func themeGalleryPresets() []string {
	return ListThemePresetsFor(themeGalleryCurrentTab().Kind)
}

// themeGallerySwitchTab moves to another tab and resets the grid to its first
// card. It reports whether anything changed.
func themeGallerySwitchTab(delta int) bool {
	if len(themeGalleryTabs) == 0 {
		return false
	}
	next := (themeGalleryTab + delta) % len(themeGalleryTabs)
	if next < 0 {
		next += len(themeGalleryTabs)
	}
	if next == themeGalleryTab {
		return false
	}
	themeGalleryTab = next
	themeGalleryIndex = 0
	themeGalleryScroll = 0
	return true
}

// themeGalleryTabRect is the pill of tab i in screen space.
func themeGalleryTabRect(element Element, i int) sdl.Rect {
	x := element.X
	for j := 0; j < i && j < len(themeGalleryTabs); j++ {
		x += themeGalleryTabWidth(j) + 10
	}
	return sdl.Rect{X: x, Y: element.Y, W: themeGalleryTabWidth(i), H: themeGalleryTabH - 8}
}

// themeGalleryTabWidth measures a tab by its label: a pill that does not fit
// its word is worse than a wider strip.
func themeGalleryTabWidth(i int) int32 {
	if i < 0 || i >= len(themeGalleryTabs) {
		return 0
	}
	// Hit-testing asks for a tab's width without a config in hand, so the
	// global one is used when it exists and the label's own length stands in
	// when nothing has been loaded yet.
	var font *ttf.Font
	if appConfig != nil {
		font = themeGalleryTabFont(appConfig)
	}
	if font == nil {
		return int32(24 + 9*len(themeGalleryTabs[i].Label))
	}
	w, _, _ := font.SizeUTF8(themeGalleryTabs[i].Label)
	return int32(w) + 32
}

// themeGalleryTabAt returns the tab under (mx, my), or -1.
func themeGalleryTabAt(element Element, mx, my int32) int {
	for i := range themeGalleryTabs {
		if pointInRect(mx, my, themeGalleryTabRect(element, i)) {
			return i
		}
	}
	return -1
}

// themeGalleryMetrics returns the grid geometry for an element.
func themeGalleryMetrics(element Element) (cols int, cardW int32, visibleRows int) {
	elemW := getElementWidth(element, 1160)
	elemH := getElementHeight(element, 560) - themeGalleryGridOffset()
	cols = UIGridColumns(elemW, themeGalleryMinW, themeGalleryGap)
	cardW = UIGridCellWidth(elemW, cols, themeGalleryGap)
	visibleRows = UIGridVisibleRows(elemH, themeGalleryH, themeGalleryGap)
	return cols, cardW, visibleRows
}

// themeGalleryGridOffset is how far the cards start below the element's top
// edge: the tab strip, plus a gap.
func themeGalleryGridOffset() int32 {
	return themeGalleryTabH + 8
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
	rows := (len(themeGalleryPresets()) + cols - 1) / cols
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
	n := len(themeGalleryPresets())
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
	presets := themeGalleryPresets()
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
	renderThemeGalleryTabs(renderer, config, element)
	presets := themeGalleryPresets()
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
		y := element.Y + themeGalleryGridOffset() + int32(row-scroll)*(themeGalleryH+themeGalleryGap)
		focused := i == themeGalleryIndex

		// Home's glass card, ringed in the accent when focused. No lift: the
		// gallery scrolls inside its element and a grown card would be cut.
		drawGlassSurface(renderer, sdl.Rect{X: x, Y: y, W: cardW, H: themeGalleryH}, 16,
			glassState{Focused: focused, NoLift: true})

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
		if p.Kind == themeKindConversion {
			// A conversion is judged by its arrangement, so its preview is a
			// sketch of one: a big card and small ones for the dashboard, equal
			// tiles for the grid.
			drawThemeArrangementSketch(renderer, p.Arrangement, prevX+10, prevY+8, prevW-20, prevH-24,
				hexRGBA(p.TextSecondary), hexRGBA(p.Info))
		} else {
			drawText(renderer, small, "Aa", prevX+18, prevY+14, hexRGBA(p.TextPrimary), textAlignLeft)
		}
		// Accent strip along the preview's bottom edge.
		fillRoundedRect(renderer, prevX, prevY+prevH-7, prevW, 7, 3, hexRGBA(p.Info))

		// Current badge on the preview corner, sized from its word (setwidgets.go):
		// a fixed 74x20 box under the theme's "small" font — 24px when a config
		// does not set it — was smaller than the label it carried.
		if name == current {
			badgeFont := getFontSized(config, "small", 14)
			if badgeFont == nil {
				badgeFont = small
			}
			badgeW, _ := setPillSize(badgeFont, "Current")
			badgeFill := hexRGBA(p.Info)
			badgeFill.A = 255
			setPillDraw(renderer, badgeFont, "Current", prevX+prevW-badgeW-8, prevY+6, badgeFill, contrastText(badgeFill))
		}

		// Theme name below the preview.
		nameY := prevY + prevH + 8
		drawText(renderer, font, ellipsize(font, p.Name, cardW-36), x+18, nameY, ColorTextPrimary(), textAlignLeft)

		// Mini palette chips: Surface, Text, Focus border. A conversion shows
		// these too — it repaints as well as rearranges — and its preview above
		// is the sketch of the arrangement. The summary line is the toast's job
		// to read out: at this card width it was always ellipsised.
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
		drawScrollbar(renderer, element.X+elemW-4, element.Y+themeGalleryGridOffset(), 3,
			elemH-themeGalleryGridOffset(), thumbFrac, scrollFrac)
	}
}

// renderThemeGalleryTabs draws the gallery's tabs: a pill per kind, the open
// one filled with the accent. Colour and "Total conversion" are different
// promises, so they are different tabs rather than one long list.
func renderThemeGalleryTabs(renderer *sdl.Renderer, config *Config, element Element) {
	font := themeGalleryTabFont(config)
	if font == nil {
		return
	}
	active := themeGalleryCurrentTab()
	for i, tab := range themeGalleryTabs {
		rect := themeGalleryTabRect(element, i)
		open := tab.Kind == active.Kind
		fill := glassChipFill()
		ink := ColorTextSecondary()
		if open {
			fill = glassAccent()
			ink = contrastText(fill)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, fill)
		if !open {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, glassChipEdge())
		}
		tw, th, _ := font.SizeUTF8(tab.Label)
		drawText(renderer, font, tab.Label, rect.X+(rect.W-int32(tw))/2,
			rect.Y+(rect.H-int32(th))/2, ink, textAlignLeft)
	}
	// The community library is the one thing on this screen that is not a
	// preset, so its key is named on the band rather than left to the controls
	// legend. It is dropped on a narrow element instead of crowding the tabs.
	last := themeGalleryTabRect(element, len(themeGalleryTabs)-1)
	hint := "C/Y · community themes"
	hintW, hintH, _ := font.SizeUTF8(hint)
	hintX := element.X + getElementWidth(element, 1160) - int32(hintW) - 4
	if hintX > last.X+last.W+16 {
		drawText(renderer, font, hint, hintX, last.Y+(last.H-int32(hintH))/2,
			ColorTextSecondary(), textAlignLeft)
	}
}

// drawThemeArrangementSketch draws a miniature of an arrangement inside a
// theme preview: the dashboard's wide lead card and small ones, the hero grid's
// tiles, the carousel's row of cards or the widgets' mixed sizes.
func drawThemeArrangementSketch(renderer *sdl.Renderer, arrangement string, x, y, w, h int32, ink, accent sdl.Color) {
	if w <= 0 || h <= 0 {
		return
	}
	tile := func(r sdl.Rect, colour sdl.Color) {
		if r.W <= 0 || r.H <= 0 {
			return
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 3, colour)
	}
	soft := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 90}
	switch normalizeHomeArrangement(arrangement) {
	case homeArrangementGrid:
		// The hero tile, then the ten cards that fill the rest of the mosaic.
		gap := int32(4)
		cw := (w - 3*gap) / 4
		ch := (h - 2*gap) / 3
		tile(sdl.Rect{X: x, Y: y, W: cw, H: 2*ch + gap}, accent)
		for _, cellPos := range [][2]int32{
			{1, 0}, {2, 0}, {3, 0},
			{1, 1}, {2, 1}, {3, 1},
			{0, 2}, {1, 2}, {2, 2}, {3, 2},
		} {
			tile(sdl.Rect{
				X: x + cellPos[0]*(cw+gap),
				Y: y + cellPos[1]*(ch+gap),
				W: cw, H: ch,
			}, soft)
		}
	case homeArrangementCarousel:
		// The focused card in the middle, its smaller, dimmed neighbours half
		// off the edges, and the position dots under them.
		gap := int32(4)
		dot := max(h/12, 2)
		cw, ch := w/2, h-dot-gap
		sw, sh := cw*2/3, ch*2/3
		sy := y + (ch-sh)/2
		dim := sdl.Color{R: ink.R, G: ink.G, B: ink.B, A: 50}
		tile(sdl.Rect{X: x, Y: sy, W: sw / 2, H: sh}, dim)
		tile(sdl.Rect{X: x + w - sw/2, Y: sy, W: sw / 2, H: sh}, dim)
		tile(sdl.Rect{X: x + (w-cw)/2, Y: y, W: cw, H: ch}, accent)
		pillW := dot * 3
		dx := x + (w-8*dot-pillW)/2
		for i := 0; i < 5; i++ {
			dw, colour := dot, soft
			if i == 2 {
				dw, colour = pillW, accent
			}
			tile(sdl.Rect{X: dx, Y: y + h - dot, W: dw, H: dot}, colour)
			dx += dw + dot
		}
	case homeArrangementWidgets:
		// The default tiles on six columns: Continue 2×2, the Media strip 4×1
		// beside it, then cards and widgets one cell each.
		gap := int32(4)
		cw := (w - 5*gap) / 6
		ch := (h - 2*gap) / 3
		tile(sdl.Rect{X: x, Y: y, W: 2*cw + gap, H: 2*ch + gap}, accent)
		tile(sdl.Rect{X: x + 2*(cw+gap), Y: y, W: 4*cw + 3*gap, H: ch}, soft)
		for _, cellPos := range [][2]int32{
			{2, 1}, {3, 1}, {4, 1}, {5, 1},
			{0, 2}, {1, 2}, {2, 2}, {3, 2}, {4, 2}, {5, 2},
		} {
			tile(sdl.Rect{
				X: x + cellPos[0]*(cw+gap),
				Y: y + cellPos[1]*(ch+gap),
				W: cw, H: ch,
			}, soft)
		}
	default:
		gap := int32(4)
		leadW := (w - gap) * 3 / 5
		sideW := w - gap - leadW
		smallH := (h - 2*gap) / 3
		tile(sdl.Rect{X: x, Y: y, W: leadW, H: h}, accent)
		for row := int32(0); row < 3; row++ {
			tile(sdl.Rect{X: x + leadW + gap, Y: y + row*(smallH+gap), W: sideW, H: smallH}, soft)
		}
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

// xyButtonsSwapped reports whether the X/Y face buttons should be treated as
// flipped. It is the other half of the quirk abButtonsSwapped handles: the
// TrimUI handhelds print a Nintendo layout on their buttons (A right, B bottom,
// X top, Y left), while the firmware's pad ("TRIMUI Player1", an X360 mapping)
// reports positions the Xbox way. Only the A/B half used to be corrected, so the
// top button ran every "Y" action and the left one every "X" action — the
// opposite of the letter printed on it. "swapXYButtons" is a setting of its own,
// so a pad that mirrors only one pair can be told apart; with no value stored it
// follows the platform, exactly like the A/B swap.
func xyButtonsSwapped(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "swapXYButtons"); ok {
		return coerceBool(v)
	}
	return P().IsHandheld()
}

// padFaceButton maps a face button between what the pad reports and the
// logical button every handler is written against: A<->B while
// abButtonsSwapped, X<->Y while xyButtonsSwapped, anything else unchanged.
//
// Each swap is its own inverse, so the same call also turns a logical button
// back into the raw one. That is what the code that queues a press for the main
// loop to swap again (the phone remote, control profiles) and the code that
// polls the pad for a held button (pad.Button) need. Every one of those sites
// goes through this function: they used to copy the A/B switch by hand, which
// is how the X/Y half could be missing from all of them at once.
func padFaceButton(config *Config, button uint8) uint8 {
	switch button {
	case sdl.CONTROLLER_BUTTON_A:
		if abButtonsSwapped(config) {
			return sdl.CONTROLLER_BUTTON_B
		}
	case sdl.CONTROLLER_BUTTON_B:
		if abButtonsSwapped(config) {
			return sdl.CONTROLLER_BUTTON_A
		}
	case sdl.CONTROLLER_BUTTON_X:
		if xyButtonsSwapped(config) {
			return sdl.CONTROLLER_BUTTON_Y
		}
	case sdl.CONTROLLER_BUTTON_Y:
		if xyButtonsSwapped(config) {
			return sdl.CONTROLLER_BUTTON_X
		}
	}
	return button
}

// applyButtonSwap normalizes face-button events against the "swapABButtons"
// and "swapXYButtons" settings (padFaceButton) so every scene and overlay sees
// one consistent mapping. All controller input flows through the PollEvent
// loops, so swapping here covers the whole app.
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
	// The voice orb's Menu is the physical GUIDE, which the fold below turns
	// into BACK (voiceorb.go reads it for this event).
	padEventRaw = raw
	handheld := P().IsHandheld()
	ce.Button = normalizeHandheldBackButton(ce.Button, handheld)
	ce.Button = padFaceButton(config, ce.Button)
	if ce.Type == sdl.CONTROLLERBUTTONDOWN && handheld && controllerInputLogs < controllerInputLogLimit {
		controllerInputLogs++
		scene := ""
		if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			scene = config.Scenes[currentSceneIndex].Name
		}
		logDebug("input", "pad button raw=%d mapped=%d scene=%q", raw, ce.Button, scene)
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
	if strings.EqualFold(strings.TrimSpace(element.Variable), "fullscreen") {
		applyFullscreen(config, on)
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

// handleToggleAction lets a toggle participate in scene navigation without
// treating Up/Down as changes to its value.
func handleToggleAction(config *Config, element Element, action Action) {
	switch action {
	case ActionNavigateUp, ActionNavigateDown:
		moveSelection(config, action)
	case ActionConfirm, ActionNavigateLeft, ActionNavigateRight:
		toggleVariable(config, element)
	}
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
	if font == nil {
		return
	}

	// A glass row with Home's focus treatment; the state pill on the right is
	// Home's chip language — the solid accent with dark type when on, a quiet
	// glass chip when off.
	row := sdl.Rect{X: element.X, Y: element.Y, W: width, H: height}
	if focused {
		drawGlassFocusGlow(renderer, row)
	}
	drawGlassSurface(renderer, row, glassControlRadius, glassState{Focused: focused})

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
	// The off pill is Home's resting chip surface (the Exit chip and the bell).
	pillCol, textCol := ColorIconSurface, ColorTextSecondary()
	if on {
		pillCol = glassAccent()
		textCol = contrastText(pillCol)
	}
	fillRoundedRect(renderer, pillX, pillY, pillW, pillH, pillH/2, pillCol)
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

// lerp linearly interpolates from a to b by factor t (typically 0..1).
// When t=0 returns a, when t=1 returns b, linearly in between.
// t may be outside [0,1] for extrapolation.
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
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
// timer; an empty message renders nothing. lines/linesW/linesFont cache the
// wrapped text so the render loop only re-measures when the message, the width
// or the font (a text-size change) changes; cut records that the wrap ended in
// "…", so the whole message is only in the notification centre.
type ToastState struct {
	Message   string
	Kind      ToastKind
	StartedAt uint64 // monotonic SDL ticks
	Duration  uint64 // ms
	lines     []string
	linesW    int32
	linesFont *ttf.Font
	cut       bool
}

// toastRect stores the last-rendered toast bounds for dismiss-on-click.
var toastRect sdl.Rect

// toastCutMsg is the message of the toast in toastRect when the card had to
// cut it short, "" otherwise: a click on it opens the notification centre on
// that message instead of only dismissing it. Holding the message rather than
// a flag means a toast raised between the frame and the click is not mistaken
// for the one that was cut.
var toastCutMsg string

// toastDurationFor returns the display time for a toast kind: info/success
// ~2.5s, warnings ~3.5s, errors ~5s; toastDurationForMessage (notiftext.go)
// stretches the slot for a long message. Screenshot mode shows none: a zero
// duration is cleared before it is drawn (renderToast, screenshotmode.go).
func toastDurationFor(kind ToastKind) uint64 {
	if shotModeOn() {
		return 0
	}
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
	// Confirmations the user just saw are filed read; warnings and errors
	// raise the bell's count (notifRecordToast, notifications.go).
	notifRecordToast(msg, kind)
}

// showToastPrimitive renders a toast without touching the notification
// history. ShowToast wraps it for normal callers; notifPush uses it directly
// because it has already recorded its own message.
func showToastPrimitive(message string, kind ToastKind) {
	msg := strings.TrimSpace(message)
	currentToastMu.Lock()
	currentToast = ToastState{
		Message:   msg,
		Kind:      kind,
		StartedAt: sdl.GetTicks64(),
		Duration:  toastDurationForMessage(kind, msg),
		lines:     nil,
		linesW:    -1,
	}
	currentToastMu.Unlock()
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

// currentToastMu guards currentToast. Toasts are raised from goroutines (a
// download finishing, a network answer, the volume-key skip ticker) while the
// render loop reads the toast every frame, and an unguarded struct write can
// be read half done: the text pointer of one toast with the length of another.
var currentToastMu sync.Mutex

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

// wrapToastLines word-wraps the toast message to at most toastMaxLines lines
// within maxW, and reports whether it had to end in "…". It used wrapMeasured,
// which cuts by pixel count into two lines ("…every fil" / "e on the…").
func wrapToastLines(font *ttf.Font, message string, maxW int32) ([]string, bool) {
	if font == nil {
		return []string{message}, false
	}
	return wrapWordsMeasured(message, maxW, toastMaxLines, func(s string) int32 {
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
	// The hit area is this frame's: every return below that draws nothing
	// leaves it empty, so a click where a toast used to be does nothing (a
	// toast cleared with an empty message used to leave its rect behind).
	toastRect = sdl.Rect{}
	toastCutMsg = ""
	currentToastMu.Lock()
	t := currentToast
	currentToastMu.Unlock()
	if strings.TrimSpace(t.Message) == "" {
		return
	}
	elapsed := sdl.GetTicks64() - t.StartedAt
	if t.Duration == 0 || elapsed > t.Duration {
		currentToastMu.Lock()
		// Only the toast this frame read is cleared; one raised since stays.
		if currentToast.StartedAt == t.StartedAt && currentToast.Message == t.Message {
			currentToast = ToastState{}
		}
		currentToastMu.Unlock()
		return
	}
	// While the open notification centre shows this message on its card, the
	// toast would only sit over the centre's lower rows: it waits out its timer
	// unseen (notifCentreShowsRow; the centre was drawn earlier this frame). A
	// toast the centre does not keep (the Live Builder's pairing code), one
	// whose row is scrolled off the card, and every toast while the centre is
	// covered (the full-screen player, a chat sheet) still show.
	if notifCentreShowsRow(t.Message) {
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

	// Word-wrapped to toastMaxLines lines in a card up to toastMaxWidth wide
	// (notiftext.go), cached per message, width and font. It used to be cut by
	// pixel count into two lines of a 420×96 card, so a long message ended
	// mid-word in "…" and a large text size clipped even the second line.
	maxW := toastMaxWidth(screenWidth)
	maxTextW := toastTextWidth(maxW)
	if t.lines == nil || t.linesW != maxTextW || t.linesFont != font {
		t.lines, t.cut = wrapToastLines(font, t.Message, maxTextW)
		t.linesW = maxTextW
		t.linesFont = font
		currentToastMu.Lock()
		if currentToast.StartedAt == t.StartedAt && currentToast.Message == t.Message {
			currentToast.lines = t.lines
			currentToast.linesW = t.linesW
			currentToast.linesFont = t.linesFont
			currentToast.cut = t.cut
		}
		currentToastMu.Unlock()
	}
	lines := t.lines
	if len(lines) == 0 {
		lines = []string{t.Message}
	}

	lineH := int32(font.Height())
	pad := toastPad
	textW := int32(0)
	for _, l := range lines {
		lw, _ := measureText(font, l)
		textW = max(textW, lw)
	}
	stripW := toastStripW
	w, h := toastCardSize(textW, len(lines), lineH, maxW)

	x := screenWidth - toastMarginRight - w + slide
	y := screenHeight - HomeFooterH - 16 - h
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		y = screenHeight - bottomBarHeight(config.Scenes[currentSceneIndex].Name) - 16 - h
	}
	if x < toastMarginMin {
		x = toastMarginMin
	}
	if y < toastMarginMin {
		y = toastMarginMin
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Compact card: the shared overlay sheet (Home's glass, opaque) with its
	// hairline, faded as one with the text.
	surface := glassSheetFill()
	surface.A = alpha
	fillRoundedRect(renderer, x, y, w, h, 12, surface)
	border := glassSheetEdge()
	border.A = uint8(int(border.A) * int(alpha) / 255)
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

	// Store the rendered toast rect for dismiss-on-click; a cut toast's click
	// opens its row in the centre (scene-switch labels are not filed there).
	toastRect = sdl.Rect{X: x, Y: y, W: w, H: h}
	if t.cut && t.Kind != ToastKindSwitch {
		toastCutMsg = t.Message
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
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
			logError("trigger", "recovered panic: %v", r)
		}
	}()
	if element.Trigger == "" {
		return
	}
	// A control drawn busy is not pressable, at the source: no activate sound,
	// no press dip, no dispatch. The Benchmark screen's Run button is the one
	// scene button busy for a long time — a whole 100 MB measurement — and the
	// run guard in runBenchmark is the backstop, not the only answer.
	if _, busy := benchmarkButtonFace(element); busy {
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
		// Every entry point (home card, menu tile, in-scene button) ends here,
		// so this is the one place an offline screen is refused. The tile is
		// already dimmed; the press has to say why it does nothing.
		if reason, blocked := offlineSceneBlock(target); blocked {
			showToast(reason, ToastWarn())
			return
		}
		for i, scene := range config.Scenes {
			if scene.Name == target {
				changeSceneTo(config, i)
				return
			}
		}
		logWarn("core", "change_scene: scene not found: %s", target)
	case "yt_search", "youtube_search", "youtube_smart":
		runMediaSearchTrigger(config, element.Trigger, element.TriggerTarget, element.TriggerValue)
	case "youtube_trending":
		fetchTrendingVideos(config, "search_results", snapshotVars(config))
	case triggerShortsOpen:
		// The Media screen's Shorts button: open the feed, start it and keep it
		// running. Same path as the Y button and Ctrl+Shift+S.
		openShortsFeed(config)
	case "youtube_play":
		scene := config.Scenes[currentSceneIndex]
		videos, _ := getSceneVideos(config, scene)
		plan := planYouTubePlay(videos, focusedResultIndex, sceneInputURL(config, scene))
		switch {
		case plan.result >= 0:
			playVideoInfo(config, videos[plan.result])
		case plan.url != "":
			logDebug("core", "youtube_play: playing the typed link")
			playVideoURL(config, plan.url)
		case plan.askURL:
			// No result and no link yet: ask for one instead of failing the
			// press. Submitting the keyboard plays what was typed.
			logDebug("core", "youtube_play: asking for a link")
			if !openMediaPlayLink(config) {
				ShowToast("This scene has no link field", ToastKindInfo)
			}
		}
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
				logWarn("core", "Cannot play: %s not found", element.TriggerTarget)
				return
			}
		}
		playVideoURL(config, url)
	case "play_focused":
		videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
		if ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoInfo(config, videos[focusedResultIndex])
		} else {
			logDebug("core", "play_focused: no focused video to play")
		}
	case "exit":
		// Quit gracefully via the main loop, which persists favorites, runs the
		// glass teardown and the deferred cleanup on the way out (a hard
		// os.Exit would skip that cleanup).
		requestAppExit()
	case "external_app":
		if element.ExternalAppPath == "" {
			logDebug("core", "external_app trigger has no path")
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
		// An empty field is not a failed press: the button is the screen's way
		// in, so it opens the channel menu, which can add a link or browse public
		// channels. Typing a link still plays it straight away.
		switch {
		case url == "":
			openIPStreamPanel()
		case !looksLikeStreamURL(url):
			showToast("That does not look like a stream URL.", ToastError())
		default:
			// The page is set first: the session reads it as it starts, and
			// a video still playing would otherwise lend it its own page.
			currentPlaybackURL = url
			StartEmbeddedPlayback(config, url)
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
			logDebug("core", "custom_link: no URL in custom_link_url")
		}
	case "iptv_load":
		reloadIPTV(config)
	case triggerRecordingsScan:
		count := loadRecordings(config)
		kind := ToastSuccess()
		if count == 0 {
			kind = ToastInfo()
		}
		showToast(recordingsScanMessage(config, count), kind)
	case "podcast_load":
		if url, ok := config.Variables.Custom["podcast_url"].(string); ok && strings.TrimSpace(url) != "" {
			loadPodcastURL(config, url)
		} else {
			loadPodcasts(config)
		}
	case "podcast_panel":
		openPodcastPanel()
	case "attach_image":
		// A layout's attach button. The name is older than file support:
		// Files opens (or, already open, starts picking) for the chat tab on
		// screen, and any file can be picked (chatfiles.go).
		chatStartAttach(config)
		return
	case "tick":
		fetchTickers(config)
	case "add_ticker":
		sym := ""
		if v, ok := config.Variables.Custom["custom_ticker"].(string); ok {
			sym = strings.TrimSpace(v)
		}
		if sym != "" {
			list := customTickerList(config)
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
		publishCustom(cronStartupVar, loadStartupItems())
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
					applyFullscreen(config, b)
				}
			case "buttonColor", "labelColor":
				applyColorVar(config, element.VariableChange, val)
			case "weatherUnit":
				config.Variables.WeatherUnit = strings.TrimSpace(val)
			case "powerMode":
				config.Variables.PowerMode = normalizePowerMode(val)
				applyPowerMode(config)
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
		// The same removal as X on a row (favorites.go): an Undo bar, and the
		// offer to delete the downloaded file when the video has one.
		favoritesRemoveFocused(config)
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
					trophyEvent(tkUnitConvert)
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
		// The chat's sender loop owns the connection (chatsync.go): this hands
		// it fresh credentials and asks it to connect now, past any backoff.
		saveUserConfig(userConfigFromVariables(config))
		chatRefreshCreds(config, time.Now(), true)
		chatDiscordRetryNow()
	case "chat_clear":
		// Clears the chat tab on screen, as the button beside it promises.
		chatClearTab(chatActiveTabID())
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
		// The focused file itself goes to the chat: it waits in the community
		// tab's tray (resolved under the Files root, like every row action) and
		// the chat opens with it ready to send. It used to post "Sent file:
		// name", or the first 500 characters of a text file, and no file.
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			chatSendFromFiles(config, []FileEntry{entries[focusedFileIndex]})
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
		applyFullscreen(config, config.Variables.Fullscreen)
		// Apply theme colors live. The accent stays the theme's: the button
		// colour tints the glass buttons themselves (renderDarkButtonElement).
		SetAccentColor(themeAccentColor())
		saveConfig(config)
		showToast("Settings saved", ToastInfo())
	case "cycle_weather_unit":
		cycleWeatherUnit(config)
		showToast("Weather unit: "+weatherUnitSettingLabel(config), ToastInfo())
	case "cycle_screensaver_timeout":
		// Settings → General and Quick Settings share this step list.
		next := applyScreensaverTimeout(config, nextScreenTimeout(config.Variables.ScreensaverTimeout))
		showToast("Screensaver after "+screenTimeoutLabel(next), ToastInfo())
	case "cycle_cookie_retention":
		// Choosing a window also drops what is already older than it, so the
		// count on the Browser's Forget chip never promises something the
		// setting is not keeping.
		days := nextCookieRetentionDays(cookieRetentionDays(config))
		pruned := applyCookieRetention(config, days)
		msg := "Saved cookies are kept until you forget them"
		if days > 0 {
			msg = "Saved cookies drop after " + cookieRetentionLabel(days)
		}
		if pruned > 0 {
			msg += " · " + strconv.Itoa(pruned) + " removed"
		}
		showToast(msg, ToastInfo())
	case "community_themes":
		// Browse the community theme repository from a config button.
		openThemeCommunityPanel(config)
	case "cycle_osd_preset":
		// The emulator's own OSD: display switches only, never a hotkey bind.
		showToast("In-game OSD: "+cycleOSDPreset(config), ToastInfo())
	case "forget_saved_cookies":
		// Beside the retention row: the age policy says how long a session is
		// kept, and this drops the one on the card now. Same two-press rule as
		// the Browser's chip, because it discards the same thing.
		forgetSavedCookiesFromSettings(config)
	case "toggle_game_snaps":
		// The Settings row is the one place the library preview can be switched
		// off without hand-editing jukauser.json. A preview already on screen
		// stops on the next frame: snapPreviewSync asks the same setting.
		state := "off"
		if toggleSnapPreview(config) {
			state = "on"
		}
		showToast("Video snaps "+state, ToastInfo())
	case "theme_preset":
		if element.TriggerTarget != "" {
			ApplyThemePreset(config, element.TriggerTarget)
		}
	case "plugin_action":
		if element.TriggerTarget != "" {
			// Off the event thread, like the Plugins page (plugcatalog.go): a
			// slow script no longer freezes the screen.
			plugRunTrigger(element.TriggerTarget, element.TriggerValue, snapshotVars(config))
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
		// Older configs still bind a Refresh button to this; the store
		// refreshes itself on arrival and on X now (pkgstate.go).
		pkgRequestRefresh(config)
	case "browser_back":
		browserBack(config)
	case "browser_forward":
		browserForward(config)
	case "browser_reload":
		browserReload(config)
	case "browser_home":
		browserHome(config)
	case "browser_links":
		if elem, ok := findBrowserElement(config); ok {
			openBrowserLinkPanel(config, elem)
		}
	case "browser_filter":
		// Per-site ad/tracker exception: off for the page that broke, on again
		// for the next visit to it.
		browserToggleAdFilter(config)
	case "browser_cookie_sites":
		// The picker: one row per host the jar holds, so a single site can be
		// signed out without dropping every other login. The strip under the
		// page opens the same panel when tapped.
		openBrowserCookiePanel(config)
	case "browser_forget_cookies":
		// The saved session lives on the card now, so the chip that drops it
		// is a trigger like any other; it asks twice before it forgets.
		browserForgetCookiesAction(config)
	case "browser_bookmark_save":
		// Save puts the page on screen on the saved list, so a link sent in a
		// chat message can be reached again without retyping it.
		browserBookmarkSaveAction(config)
	case "browser_bookmarks":
		// The saved-page picker: one row per page, A opens it and X removes it.
		openBrowserBookmarkPanel(config)
	case "browser_find":
		// Find-on-this-page: its own keyboard, so the address bar and the
		// web search are both left alone.
		if elem, ok := findBrowserElement(config); ok {
			openBrowserFind(renderer, config, elem)
		}
	case "browser_site_info":
		// The page's connection, certificate and security headers (the
		// padlock, `i` and R3 open the same sheet).
		brwInfoOpenFor(config)
	case "browser_search":
		// The Search chip and the Y button both open the query keyboard.
		if elem, ok := findBrowserElement(config); ok {
			openBrowserSearch(renderer, config, elem)
		}
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
		trophyEvent(tkTextPage)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_zeroconf":
		tbVar := findTextBrowserVariable(config)
		go func(v string, el Element) {
			defer func() {
				if r := recover(); r != nil {
					logDebug("textbrowser", "zeroconf goroutine recovered: %v", r)
					publishCustom(v, fmt.Sprintf("Zeroconf error: %v", r))
				}
			}()
			publishCustom(v, browseZeroconfServicesWithTimeout(4*time.Second))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(tbVar, element)
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
	case "textbrowser_json":
		tbVar := findTextBrowserVariable(config)
		go func(v string, el Element) {
			publishCustom(v, browseJSONContent(el))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(tbVar, element)
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
	case "textbrowser_processes":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getProcessTree()
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_network":
		func() {
			defer func() {
				if r := recover(); r != nil {
					logDebug("textbrowser", "network trigger recovered: %v", r)
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
			trophyEvent(tkTextPage)
			if element.AutoRefresh {
				startTextBrowserAutoRefresh(config, element)
			}
		}()
	case "textbrowser_temps":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getSystemTemperature()
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_services":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = getRunningServices()
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_files":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = scanLocalFiles("/media")
		textBrowserLastUpdate = time.Now().Unix()
		trophyEvent(tkTextPage)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_copy":
		tbVar := findTextBrowserVariable(config)
		if text, ok := config.Variables.Custom[tbVar].(string); ok && text != "" {
			// The shared clipboard path. This ran `cmd /c echo <page text>`
			// (cmd then acted on any & or | in the page) or a bare pbcopy
			// that is not on Linux, and said "Copied" either way.
			copyWithToast(text, "Browser", "Copied to clipboard")
		}
	case "textbrowser_clear":
		tbVar := findTextBrowserVariable(config)
		config.Variables.Custom[tbVar] = ""
		textBrowserScrollY = 0
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
	case "cache_clear":
		if db, err := openSharedCache("jukahub.cache"); err == nil {
			cacheClear(db)
			showToast("Cache cleared", ToastInfo())
		}
	case "tools_open":
		toolsHubOpen(config)
	case "companion_open":
		toggleCompanionOverlay()
	case "processes_open":
		openProcessPanel()
	case "wifi_open":
		wifiScrOpen(config)
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
		if link, ok := externalURLAllowed(element.Trigger); ok &&
			(strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://")) {
			// No shell: the URL reaches the desktop handler as one argument, so
			// "&", "|" and "^" in a query string cannot become cmd.exe syntax.
			go func(url string) {
				var cmd *exec.Cmd
				if IsWindows() {
					cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
				} else {
					cmd = exec.Command("xdg-open", url)
				}
				if err := cmd.Run(); err != nil {
					logWarn("core", "open url error: %v", err)
				}
			}(link)
		} else {
			logWarn("core", "Unhandled trigger: %s", element.Trigger)
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
	// Keep the fallback in sync with the focus graph, including scenes whose
	// only controls are toggles, a theme gallery or chat.
	for i, e := range scene.Elements {
		if isFocusableElement(e) {
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
	// Work started by this scene stops here: a speed test or a feed fetch must
	// not keep the Wi-Fi and the CPU busy behind the next screen.
	cancelSceneWork()
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
	dismissVolumePopover()
	dismissBrightnessPopover()
	// Home's "Exit JukaHub?" card (home_power.go) must not wait behind a scene
	// change and greet the next return to Home with Exit focused.
	homePowerClose()
	// The browser link picker belongs to the page that opened it; it must not
	// float over the next scene.
	browserLinkPanel = browserLinkPanelState{}
	browserLinkRowHits = nil
	browserLinkPanelRect = sdl.Rect{}
	// The same is true of the other scene-owned menus: the saved-cookie picker,
	// the saved-page picker and the Files action sheet all name rows on the
	// screen being left. They are drawn and route input on the strength of their
	// open flag alone, so left up they float over the next scene and eat the
	// press meant for it — the cookie picker's A would drop a site from a page
	// nobody can see. Dismissed silently: the transition is already the feedback,
	// and a back chirp for a scene the user did not press B on would be a lie.
	browserCookiePageForget()
	browserBookmarkPickerForget()
	// The browser's site-info sheet closes too, JukaLand played on its offline
	// page stops, and a JukaLand world is saved however its screen was left.
	brwSceneDeparture(config)
	// So does the Browser's Select menu, and its page's status pills.
	brwMenuForget()
	feSheetForget()
	// The editor's card names one file on the screen being left, and its keys
	// would otherwise keep answering on the next scene.
	fileEditorForget()
	// The package row menu and the Audio / Video / Chat selection used to be
	// cleared when the next scene arrived. That is a frame too late: input is
	// not gated while the fade runs and the scene index still points at the
	// screen being left, so a press during the fade still reached them — a
	// doubled or held A would Reinstall the package the user just walked away
	// from, and a tap could press a pill on a row that is no longer drawn.
	// Cleared here so departure is the one moment they go, exactly like the
	// pickers above.
	packageActionsForget()
	resetResultActions()
	resultActionHits = nil
}

func changeSceneTo(config *Config, targetIdx int) {
	switchScene(config, targetIdx, true)
}

// completeSceneTransition finishes the scene switch after fade-out and starts fade-in.
func completeSceneTransition(config *Config) {
	currentSceneIndex = pendingSceneIndex

	// A Tools menu is laid out as a launcher page before the focus graph is
	// built, so navigation, hover and click hit-testing all use the tiles that
	// are actually drawn. The authored geometry comes back on save.
	menuTilesApply(config, currentSceneIndex)

	// Build focus graph for the new scene and restore persisted focus.
	scene := config.Scenes[currentSceneIndex]
	enterSceneWork(scene.Name)
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
	resetResultActions()
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

	// The Hardware screen is a live monitor now (systool_hw.go): its sampler
	// starts below with the other tool screens, off the render thread. The
	// old synchronous getHardwareInfo here enumerated every process on the
	// event thread at each entry.
	// The disk pie's poller skips hidden screens, so the chart takes a fresh
	// reading on the way in instead of opening with a reading from whenever the
	// scene was last up.
	if sceneHasPieChart(config) {
		refreshDiskPie()
	}
	resetNetSpeedOnSceneEntry(config, scene)
	// The Terminal, Scheduled tasks and the system tool screens start their
	// own work as they open, inside this visit's task scope (systool.go).
	sysToolSceneEnter(config, scene.Name)
	// Settings → General's Video snaps row reports how many of the card's games
	// have a clip. The library scan is started here, on the scene's goroutine,
	// so the row can show a count without the page ever walking the card.
	if scene.Name == "SettingsGeneral" {
		snapClipCountRefresh(snapPreviewRoot(config))
		// The cookie row carries the day the saved session began, which only
		// changes between visits (a sign-in in the Browser, a Forget). Re-seed it
		// on the way in — a jar lock and a read of its stamps, no card I/O — so
		// the date is on the first frame rather than after the next relaunch.
		refreshCookieRetentionRow(config)
	}

	// The Media screen loads its own rows on the way in, and dials nothing at
	// all while offline (media_hub.go); the generic results fetch below would
	// start a Trending search into the hidden results element.
	mediaOwned := mediaHubOwnsScene(scene.Name)
	if mediaOwned {
		mediaHubEnter(config)
	}
	if !mediaOwned && sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
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
			fetchTrendingVideos(config, srElem.Variable, snapshotVars(config))
		}
	}

	if dl, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok {
		switch dl.Variable {
		case "fe_entries":
			// Lists the folder and puts the cursor back on the row it was left
			// on (the reset above is for every other list).
			filesSceneEntered(config)
			feFocusList(config)
		case "iptv_entries":
			loadIPTV(config)
		case "podcast_entries":
			loadPodcasts(config)
		case recordingsEntriesVar:
			loadRecordings(config)
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
	// Initialize dashboard when entering the Dashboard scene.
	if config.Scenes[currentSceneIndex].Name == "Dashboard" {
		dashInit()
	}
	// Initialize dictionary when entering.
	if config.Scenes[currentSceneIndex].Name == "Dictionary" {
		dictInit()
	}
	// Prepare the reader when entering. An open document is kept: Files
	// opens the file, then navigates here.
	if config.Scenes[currentSceneIndex].Name == "PDFReader" {
		pdfSceneEnter()
	} else {
		// Any other scene: the reader's page tiles (megabytes at a high
		// zoom) are released instead of waiting for the next document.
		pdfvSceneLeft()
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
	// The Tools hub, Network tools and Wi-Fi pages load what they show as
	// they open (values, the probes that need no input, the Wi-Fi scan).
	toolsScenesEnter(config, config.Scenes[currentSceneIndex].Name)
	// The Benchmark screen measures as it opens.
	benchmarkAutoStartOnOpen(config, config.Scenes[currentSceneIndex])
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "textlog" && e.Variable == "weather_screen_text" {
			trophyEvent(tkWeather) // the Weather screen opened
			runSceneTask(func(ctx context.Context) {
				fetchWeatherOnceContext(ctx)
				if ctx.Err() != nil {
					return
				}
				publishCustom("weather_screen_text", weatherScreenText(config))
			})
		}
	}
	// Chat loads its history and connects from its own lifecycle (chatTick and
	// chatSceneEnter in chatstate.go). Entering it no longer switches the
	// composer on — that typed "b" into the draft instead of going back — and
	// the Terminal, whose config also carries a "chat" element, no longer
	// opens a Discord connection or a raw-error toast on the way in.
	// Auto-fetch YouTube Shorts when entering the Shorts scene.
	if config.Scenes[currentSceneIndex].Name == "Shorts" {
		shortsMutex.Lock()
		hasShorts := len(shortsList) > 0
		shortsMutex.Unlock()
		if !hasShorts {
			fetchYouTubeShorts(config, "shorts_list", snapshotVars(config))
		}
	}
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "favorites" {
			favoritesFocusIndex = 0
			// Removing a favourite offers to delete its download too, so the
			// Downloads library is read (in the background) on the way in.
			dlLibraryEnsureLoaded()
			break
		}
	}
	// The package store shows its cached list at once and refreshes itself in
	// the background (pkgstate.go); the sheet and press of the last visit
	// were dropped on departure (packageActionsForget).
	for _, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "packagelist" {
			pkgOnSceneEnter(config)
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
		// The toast names the screen, not its internal scene ID: switching to
		// Settings → General showed a toast reading "SettingsGeneral".
		if label := sceneDisplayName(scene); label != "" {
			scene = label
		}
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
	cols, _ := gridColsRows(srElem)
	colsI := int(cols)
	if colsI < 1 {
		colsI = 1
	}
	// Navigate the whole loaded list, not just the configured page: paging
	// appends results, and a grid capped at 2x6 could never reach them.
	rowsI := (len(videos) + colsI - 1) / colsI
	if rowsI < 1 {
		rowsI = 1
	}
	row := focusedResultIndex / colsI
	col := focusedResultIndex % colsI
	row += dy
	col += dx
	// Rows clamp (Down on the last card asks for the next page), columns wrap
	// so Left/Right still cycle within a row.
	if row < 0 {
		row = 0
	} else if row >= rowsI {
		row = rowsI - 1
	}
	if col < 0 {
		col = colsI - 1
	} else if col >= colsI {
		col = 0
	}
	newIdx := row*colsI + col
	if newIdx >= len(videos) {
		// Clamp to last valid index
		newIdx = len(videos) - 1
	}
	if newIdx != focusedResultIndex {
		// The Audio / Video / Chat selection belongs to the row that owned it.
		resetResultActions()
	}
	focusedResultIndex = newIdx
	// Warm the selected card's optional hover-preview frames and resolve its
	// stream in the background while the user keeps moving.
	prefetchCardPreviewFrames(videos[newIdx])
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

	// A single-column list has Left/Right free: they belong to the focused
	// row's Audio / Video / Chat buttons. The grid keeps them for card
	// navigation.
	if srElem, ok := sceneSearchResultsElement(scene); ok {
		if cols, _ := gridColsRows(srElem); cols <= 1 && listResultAction(action, config) {
			return
		}
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
		// Pressing Down on the last loaded card asks for the next page: that is
		// the "scroll past the end" gesture the Next chip advertises.
		if videos, ok := getSceneVideos(config, scene); ok && len(videos) > 0 &&
			focusedResultIndex >= len(videos)-1 {
			loadMoreMediaResults(config)
		}
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
	case ChromeTrophyIndex:
		// The cup beside the bell opens the trophy room (header_trophy.go).
		PlayActivateSound()
		headerTrophyActivate(config)
		return true
	case ChromeUpdateIndex:
		// Same one-press install as the home header chip, so the offer behaves
		// identically wherever it is drawn.
		PlayActivateSound()
		startHeaderUpdate()
		return true
	case ChromeMediaIndex:
		// The chip names what is playing; pressing it opens that player.
		PlayActivateSound()
		openNowPlaying(config)
		return true
	case ChromeWiFiIndex:
		// The signal meter is the Wi-Fi button: it opens the same Wi-Fi page
		// a tap on the meter does.
		PlayActivateSound()
		wifiScrOpen(config)
		return true
	case ChromeVolumeIndex:
		// The speaker opens the volume popover (volumepopover.go), the same
		// card a tap on it opens.
		PlayActivateSound()
		toggleVolumePopover()
		return true
	case ChromeBrightnessIndex:
		// The sun opens the brightness card (brightnesspopover.go).
		PlayActivateSound()
		toggleBrightnessPopover()
		return true
	}
	return false
}

// handleChromeBarClick activates a top-bar chrome button under the pointer:
// the back pill leaves the scene, the status clock toggles the calendar
// popover (exactly like Home). It reports whether a chrome button was hit.
func handleChromeBarClick(mx, my int32, config *Config) bool {
	// The update badge sits in the system corner, left of the cup and the bell, and starts
	// the install directly — the same action as the D-pad and the home chip.
	// Every bar control is hit-tested through the shared touch target: the
	// pills are 28–36 px tall by design, and a fingertip that lands a few pixels
	// above one used to hit nothing. The drawn geometry does not move.
	if updatePrompt.badge.W > 0 && hitTouchTarget(updatePrompt.badge, mx, my) {
		PlayActivateSound()
		startHeaderUpdate()
		return true
	}
	// The trophy room's cup, between the offer and the bell, opens Trophies.
	if headerTrophyHit(mx, my) {
		PlayActivateSound()
		headerTrophyActivate(config)
		return true
	}
	if sceneTitleBackRect.W > 0 && hitTouchTarget(sceneTitleBackRect, mx, my) {
		goBackScene(config)
		return true
	}
	if statusClockRect.W > 0 && hitTouchTarget(statusClockRect, mx, my) {
		toggleCalendarPopover()
		return true
	}
	if weatherStatusRect.W > 0 && hitTouchTarget(weatherStatusRect, mx, my) {
		toggleWeatherPopover()
		return true
	}
	// The speaker opens the volume popover. Its rect is already finger-sized
	// and split from the meter's at the gap's middle (growStatusButtonRects),
	// so it is tested as drawn, and before the meter, whose grown target may
	// reach a few pixels over it.
	if statusVolumeRect.W > 0 && pointInRect(mx, my, statusVolumeRect) {
		PlayActivateSound()
		toggleVolumePopover()
		return true
	}
	// The sun left of it opens the brightness card; its target is split from
	// the speaker's the same way, so it is tested as drawn too.
	if statusBrightnessRect.W > 0 && pointInRect(mx, my, statusBrightnessRect) {
		PlayActivateSound()
		toggleBrightnessPopover()
		return true
	}
	// The Wi-Fi meter opens the network picker (scan, connect, enter a
	// password) instead of a popover: the picker already owns that flow.
	if statusWiFiRect.W > 0 && hitTouchTarget(statusWiFiRect, mx, my) {
		PlayActivateSound()
		wifiScrOpen(config)
		return true
	}
	// The now-playing chip is a button too: a tap returns to the player. While
	// it names an idle output device it is a reading, so the tap falls through
	// instead of opening nothing.
	if statusMediaRect.W > 0 && nowPlayingChipIsButton() && hitTouchTarget(statusMediaRect, mx, my) {
		PlayActivateSound()
		openNowPlaying(config)
		return true
	}
	return false
}

// screenPadPress routes one controller press for the screen on display. The
// order is the accessibility contract, and it is one function so the main loop
// and the audits cannot disagree about it:
//
//  1. The top bar, when it holds the focus. A bar button that draws its focus
//     ring has to answer to A: the screen underneath binds the same buttons
//     (the music player's A plays a track, the terminal's arrows scroll), and
//     running the scene first left the ring on a button the pad could not
//     press.
//  2. The home layout, the Patch list and the screen's own module.
//  3. The top bar again, for an Up the screen did not take. A screen with no
//     controls of its own (Playtime, MusicPlayer, Terminal) has no grid to step
//     up from, so Up on an unfocused screen is how the header is reached on a
//     handheld with no touchscreen.
//
// It reports whether the press was used.
func screenPadPress(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if handleChromeController(e, config) {
		return true
	}
	if homeLayoutActive && handleHomeController(e, config) {
		return true
	}
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == "Patch" && handlePatchController(e, config) {
		return true
	}
	if handleSceneController(e, config) {
		return true
	}
	if e.Button == sdl.CONTROLLER_BUTTON_DPAD_UP && focusEngine.ElementIndex() < 0 {
		return focusChromeBar()
	}
	return false
}

// focusChromeBar moves the pad onto the top bar's leftmost button and reports
// whether the bar had one to focus. It is the entry point screens with nothing
// focusable use to reach the header's buttons.
func focusChromeBar() bool {
	if !focusEngine.FocusChromeBar() {
		return false
	}
	PlayNavSound()
	focusEngine.SyncSelected()
	return true
}

// handleChromeController owns pad input while a top-bar button is focused.
// It must run before the scene element dispatch, since chrome uses negative
// indices rather than indices into the scene's element slice.
func handleChromeController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || homeLayoutActive || chromeTier(focusEngine.ElementIndex()) == 0 {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		moveSelection(config, ActionNavigateUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		moveSelection(config, ActionNavigateDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		moveSelection(config, ActionNavigateLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		moveSelection(config, ActionNavigateRight)
	case sdl.CONTROLLER_BUTTON_A:
		activateChromeFocus(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) && config.Scenes[currentSceneIndex].Name == "FileExplorer" {
			feBack(config)
		} else {
			goBackScene(config)
		}
	default:
		return false
	}
	return true
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
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		activateChromeFocus(config)
	case sdl.K_ESCAPE, sdl.K_b:
		// Back over an open top-bar overlay closes it first.
		if tb.startOpen {
			startMenuDismiss()
		} else if nc.open {
			notifDismiss()
		} else if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) && config.Scenes[currentSceneIndex].Name == "FileExplorer" {
			feBack(config)
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
	// The volume card owns every key while it is up (volumepopover.go), and
	// so does the brightness card (brightnesspopover.go).
	if volumePopoverKey(e, config) {
		return true
	}
	if brightnessPopoverKey(e, config) {
		return true
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
			if tb.focus == 0 {
				startMenuMoveGroup(-1, config)
			} else {
				startMenuNavigate(-1, config)
			}
		case sdl.K_DOWN:
			if tb.focus == 0 {
				startMenuMoveGroup(1, config)
			} else {
				startMenuNavigate(1, config)
			}
		case sdl.K_LEFT:
			startMenuFocusRail()
		case sdl.K_RIGHT, sdl.K_TAB:
			startMenuFocusItems(config)
		case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
			startMenuActivate(config)
		case sdl.K_x:
			// The menu's power key, as on the pad (startMenuPower).
			startMenuPower(config)
		case sdl.K_ESCAPE, sdl.K_b:
			startMenuDismiss()
		case sdl.K_F10:
			// F10 is the keyboard's Start (startMenuKeyOpen): it closes the
			// menu it opened, like the pad's Start does. A held F10's repeats
			// do not, or holding it would open and shut the menu.
			if e.Repeat == 0 {
				startMenuDismiss()
			}
		case sdl.K_PAGEUP, sdl.K_PAGEDOWN, sdl.K_HOME, sdl.K_END:
			// The keyboard twins of L1/R1/L2/R2 (inputhints.go).
			startMenuShoulder(keyShoulder(e.Keysym.Sym), config)
		}
		return true
	}
	if nc.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			notifNavigate(-1)
		case sdl.K_DOWN:
			notifNavigate(1)
		case sdl.K_PAGEUP, sdl.K_PAGEDOWN, sdl.K_HOME, sdl.K_END:
			notifShoulder(keyShoulder(e.Keysym.Sym))
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
		} else if action == ActionNavigateDown && chromeTier(before) == 1 {
			// Nothing below the bar to step back into: the screen draws no
			// controls of its own, so drop the focus and hand the pad back to
			// it instead of stranding the ring on a bar button.
			focusEngine.ClearFocus()
			selectedButtonIndex = -1
			return
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
				if len(videos) > 0 {
					prefetchCardPreviewFrames(videos[0])
				}
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
// while it is open: Left/Right/Home/End move the caret and Ctrl+A/X/Z edit
// (vkHandlePhysicalKey), Up/Down/Tab move the highlight, Enter submits,
// Backspace deletes, Esc closes. Printable characters are delivered by SDL's
// text-input event so nothing is inserted twice.
func handleVirtualKeyboardInput(e *sdl.KeyboardEvent, config *Config) {
	if vkHandlePhysicalKey(e, config) {
		return
	}
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
