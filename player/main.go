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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- Global state for input and navigation ---
var (
	appConfig                  *Config
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

	// Scene history for B/Esc back navigation on non-home scenes.
	sceneHistory []int

	// Cache for thumbnails
	textureCache        = make(map[string]*sdl.Texture)
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
	unitCategory   string = "length"
	unitFrom       string = "m"
	unitTo         string = "ft"
	unitInputValue string
	unitResult     string

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

// --- Video info struct ---
type VideoInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Uploader    string   `json:"uploader"`
	Channel     string   `json:"channel"`
	Duration    float64  `json:"duration"`
	Thumbnail   string   `json:"thumbnail"`
	Thumbnails  []string `json:"-"`
	Description string   `json:"description"`
	ViewCount   int64    `json:"view_count"`
	UploadDate  string   `json:"upload_date"`
	WebpageURL  string   `json:"webpage_url"`
	URL         string   `json:"url"`
	// Position is the last watched offset in seconds (0 = not started). It is
	// persisted with favorites so the Continue card can resume playback.
	Position float64 `json:"position,omitempty"`
}

func (v *VideoInfo) UnmarshalJSON(data []byte) error {
	type Alias VideoInfo
	aux := &struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
		*Alias
	}{
		Alias: (*Alias)(v),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Thumbnails) > 0 {
		v.Thumbnail = aux.Thumbnails[0].URL
		for _, t := range aux.Thumbnails {
			if t.URL != "" {
				v.Thumbnails = append(v.Thumbnails, t.URL)
			}
		}
	}
	return nil
}

func (v *VideoInfo) GetURL() string {
	if v.WebpageURL != "" {
		return v.WebpageURL
	}
	if v.URL != "" {
		return v.URL
	}
	if v.ID != "" {
		return "https://www.youtube.com/watch?v=" + v.ID
	}
	return ""
}

// --- Config structs ---
// ChannelProfileConfig holds the explicit Discord connection profile. It is
// loaded automatically from jukaconfig.json so the channel id (and an optional
// bot token) are available without manual UI entry.
type ChannelProfileConfig struct {
	ChannelID string `json:"channel_id"`
	Token     string `json:"token"`
}

type Config struct {
	AppName        string               `json:"AppName"`
	Version        string               `json:"Version"`
	Width          int                  `json:"Width"`
	Height         int                  `json:"Height"`
	Background     string               `json:"Background"`
	FontPath       string               `json:"FontPath"`
	Title          string               `json:"title"`
	Author         string               `json:"author"`
	Description    string               `json:"description"`
	Variables      Variables            `json:"variables"`
	ChannelProfile ChannelProfileConfig `json:"channel_profile"`
	Scenes         []SceneConfig        `json:"scenes"`
}

// Background goroutines (search, tickers, podcasts, ...) must not touch the
// shared Variables.Custom map directly â€” the render loop and event handlers
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
	customCh <- customUpdate{key: key, val: val}
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

// RGB is a color that can be loaded from either a hex string ("#rrggbb") or
// an object {"r":..,"g":..,"b":..}.
type RGB struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

func (c *RGB) UnmarshalJSON(data []byte) error {
	// Try the object form {"r":..,"g":..,"b":..} first.
	type alias RGB
	if err := json.Unmarshal(data, (*alias)(c)); err == nil {
		return nil
	}
	// Fall back to a hex string like "#1c2230".
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	r, g, b := hexToRGB(s)
	c.R, c.G, c.B = int(r), int(g), int(b)
	return nil
}

type Variables struct {
	ButtonColor        RGB               `json:"buttonColor"`
	LabelColor         RGB               `json:"labelColor"`
	InputColor         RGB               `json:"inputColor"`
	BackgroundImage    string            `json:"backgroundImage"`
	Fonts              map[string]string `json:"fonts"`
	FontSizes          map[string]int    `json:"fontSizes"`
	Fullscreen         bool              `json:"fullscreen"`
	ScreenWidth        int               `json:"screenWidth"`
	ScreenHeight       int               `json:"screenHeight"`
	ToolsPath          string            `json:"tools_path"`
	FileExplorerRoot   string            `json:"fileExplorerRoot"`
	WeatherEnabled     bool              `json:"weatherEnabled"`
	WeatherUnit        string            `json:"weatherUnit"`
	TSPUsername        string            `json:"tspUsername"`
	PlaybackResolution string            `json:"playbackResolution"`
	AudioBackend       string            `json:"audioBackend"`
	ReducedMotion      bool              `json:"reducedMotion"`
	LowPower           bool              `json:"lowPower"`
	Custom             map[string]interface{}
	LoadingSpinner     bool   `json:"-"`
	SpinnerText        string `json:"-"`
}

type SceneConfig struct {
	Name       string    `json:"name"`
	Background string    `json:"background"`
	Layout     string    `json:"layout"`
	Elements   []Element `json:"elements"`
}

type StringOrInt string

func (s *StringOrInt) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = StringOrInt(str)
		return nil
	}
	var num int
	if err := json.Unmarshal(data, &num); err == nil {
		*s = StringOrInt(strconv.Itoa(num))
		return nil
	}
	if string(data) == "null" {
		*s = ""
		return nil
	}
	return fmt.Errorf("StringOrInt: expected string or integer, got %q", data)
}

type Element struct {
	Type          string      `json:"type"`
	Text          string      `json:"text"`
	Color         string      `json:"color"`
	X             int32       `json:"x"`
	Y             int32       `json:"y"`
	Font          string      `json:"font"`
	BgColor       string      `json:"bgColor"`
	Trigger       string      `json:"trigger"`
	TriggerTarget string      `json:"triggerTarget"`
	TriggerValue  string      `json:"triggerValue"`
	Image         string      `json:"image"`
	Width         StringOrInt `json:"width"`
	Height        StringOrInt `json:"height"`
	Video         string      `json:"video"`
	Variable      string      `json:"variable"`
	Command       string      `json:"command"`
	ListVariable  string      `json:"listVariable"`
	Columns       int32       `json:"columns"`
	Rows          int32       `json:"rows"`
	Placeholder   string      `json:"placeholder"`
	Style         string      `json:"style"` // e.g. "tile" for OS-style app icons
	Icon          string      `json:"icon"`  // monogram/short glyph for tile style
	JsonPath      string      `json:"jsonPath"`
	AutoRefresh   bool        `json:"autoRefresh"`

	// Extended trigger payloads (set by the GUI generator)
	ExternalAppPath     string `json:"externalAppPath"`
	ExternalAppReturn   string `json:"externalAppReturn"`
	VariableChange      string `json:"variableChange"`
	VariableChangeValue string `json:"variableChangeValue"`
}

// UnmarshalJSON for Variables captures known fields into the struct and any
// other keys (e.g. user-defined variables like "version", "search_query")
// into the Custom map so they are available via $ substitution at runtime.
func (v *Variables) UnmarshalJSON(data []byte) error {
	type alias Variables
	if err := json.Unmarshal(data, (*alias)(v)); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	known := map[string]bool{
		"buttonColor":        true,
		"labelColor":         true,
		"inputColor":         true,
		"backgroundImage":    true,
		"fonts":              true,
		"fontSizes":          true,
		"fullscreen":         true,
		"screenWidth":        true,
		"screenHeight":       true,
		"tools_path":         true,
		"fileExplorerRoot":   true,
		"weatherEnabled":     true,
		"weatherUnit":        true,
		"tspUsername":        true,
		"playbackResolution": true,
		"audioBackend":       true,
		"reducedMotion":      true,
		"lowPower":           true,
		"custom":             true,
	}
	v.Custom = make(map[string]interface{})
	for k, val := range raw {
		if known[k] {
			continue
		}
		var decoded interface{}
		if err := json.Unmarshal(val, &decoded); err != nil {
			continue
		}
		v.Custom[k] = decoded
	}
	// The web editor's config regeneration nests settings under a capitalized
	// "Custom" key (variables.Custom.Custom["ButtonColor"] etc.). Flatten any
	// such nested blocks into the top level so Get/substitution resolve them
	// exactly like the legacy flat format.
	for depth := 0; depth < 4; depth++ {
		nested, ok := v.Custom["Custom"].(map[string]interface{})
		if !ok {
			break
		}
		for k, val := range nested {
			if _, exists := v.Custom[k]; !exists {
				v.Custom[k] = val
			}
		}
		delete(v.Custom, "Custom")
	}
	tmp := &Config{Variables: *v}
	syncVariableOverrides(tmp)
	*v = tmp.Variables
	return nil
}

// --- Helper functions for dimensions ---
func getElementWidth(elem Element, defaultWidth int32) int32 {
	if elem.Width == "" {
		return defaultWidth
	}
	s := string(elem.Width)
	if val, err := strconv.Atoi(s); err == nil {
		return int32(val)
	}
	return defaultWidth
}

func getElementHeight(elem Element, defaultHeight int32) int32 {
	if elem.Height == "" {
		return defaultHeight
	}
	s := string(elem.Height)
	if val, err := strconv.Atoi(s); err == nil {
		return int32(val)
	}
	return defaultHeight
}

// --- Helper functions ---
func loadConfig(filename string) (*Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return nil, err
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
	return &config, nil
}

func substituteVars(text string, vars map[string]interface{}) string {
	re := regexp.MustCompile(`\$(\w+)`)
	return re.ReplaceAllStringFunc(text, func(m string) string {
		varName := m[1:]
		if val, ok := vars[varName]; ok {
			return fmt.Sprintf("%v", val)
		}
		return m
	})
}

func normalizeCacheKey(raw string) string {
	s := strings.ToLower(raw)
	s = regexp.MustCompile(`"ytsearch\d+:[^"]+"`).ReplaceAllString(s, `"ytsearch:QUERY"`)
	s = regexp.MustCompile(`-f "[^"]*"`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func resolveColor(config *Config, colorName string, defaultColor sdl.Color) sdl.Color {
	if strings.HasPrefix(colorName, "$") {
		colorValue := config.Variables.Get(colorName[1:])
		parts := strings.Split(colorValue, ",")
		if len(parts) == 3 {
			r, _ := strconv.Atoi(parts[0])
			g, _ := strconv.Atoi(parts[1])
			b, _ := strconv.Atoi(parts[2])
			return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
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
	h := strings.TrimSpace(s)
	if strings.HasPrefix(h, "#") {
		h = h[1:]
	}
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

func (v *Variables) Get(name string) string {
	if val, ok := v.Custom[name]; ok {
		return fmt.Sprintf("%v", val)
	}
	// The web editor emits capitalized keys (ButtonColor); accept any casing
	// so a config regeneration can never leave colors resolving to black.
	// When keys collide across casing (InputColor string vs inputColor struct
	// mirror), prefer the string form, which is the real value.
	for k, val := range v.Custom {
		if strings.EqualFold(k, name) {
			if _, isStr := val.(string); isStr {
				return fmt.Sprintf("%v", val)
			}
		}
	}
	for k, val := range v.Custom {
		if strings.EqualFold(k, name) {
			return fmt.Sprintf("%v", val)
		}
	}
	switch name {
	case "buttonColor":
		return fmt.Sprintf("%d,%d,%d", v.ButtonColor.R, v.ButtonColor.G, v.ButtonColor.B)
	case "labelColor":
		return fmt.Sprintf("%d,%d,%d", v.LabelColor.R, v.LabelColor.G, v.LabelColor.B)
	case "backgroundImage":
		return v.BackgroundImage
	}
	if path, ok := v.Fonts[name]; ok {
		return path
	}
	if size, ok := v.FontSizes[name]; ok {
		return strconv.Itoa(size)
	}
	return ""
}

// Cache fonts
var fontCache = make(map[string]*ttf.Font)

func getCachedFont(config *Config, fontName string) (*ttf.Font, int) {
	key := fontName
	if font, ok := fontCache[key]; ok {
		size := 24
		for k, v := range config.Variables.FontSizes {
			if strings.EqualFold(k, fontName) {
				size = v
				break
			}
		}
		return font, size
	}
	fontPath := "Inter-Regular.ttf"
	size := 24
	for key, path := range config.Variables.Fonts {
		if strings.EqualFold(key, fontName) {
			fontPath = path
			break
		}
	}
	for key, val := range config.Variables.FontSizes {
		if strings.EqualFold(key, fontName) {
			size = val
			break
		}
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
		return nil, 0
	}
	fontCache[key] = font
	return font, size
}

func renderText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) (int32, int32) {
	processed := substituteVars(text, config.Variables.Custom)
	if processed == "" || font == nil {
		return 0, 0
	}
	surface, err := font.RenderUTF8Blended(processed, color)
	if err != nil {
		return 0, 0
	}
	defer surface.Free()
	texture, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return 0, 0
	}
	defer texture.Destroy()
	_, _, w, h, _ := texture.Query()
	renderer.Copy(texture, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
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

// renderDiskPieChart draws a donut-style pie chart showing used vs free disk
// space. The used slice uses the provided color; the free slice is drawn in a
// darker shade. Used/total text is rendered in the center.
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
	radius := int32(120)
	if radius > elemW/2-20 {
		radius = elemW/2 - 20
	}
	if radius > elemH/2-40 {
		radius = elemH/2 - 40
	}
	if radius < 20 {
		radius = 20
	}
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

	// Center text
	usedMB := float64(used) / (1024 * 1024)
	totalMB := float64(total) / (1024 * 1024)
	centerFont, _ := getCachedFont(config, "big")
	if centerFont == nil {
		centerFont = font
	}
	if centerFont != nil {
		pctStr := fmt.Sprintf("%d%%", pct)
		usedStr := fmt.Sprintf("%.1f MB", usedMB)
		totalStr := fmt.Sprintf("/ %.1f MB", totalMB)
		pw, _, _ := centerFont.SizeUTF8(pctStr)
		uw, _, _ := centerFont.SizeUTF8(usedStr)
		tw, _, _ := centerFont.SizeUTF8(totalStr)
		renderText(renderer, config, centerFont, pctStr, ColorTextPrimary(), cx-int32(pw)/2, cy-int32(32))
		renderText(renderer, config, font, usedStr, ColorTextSecondary(), cx-int32(uw)/2, cy-6)
		renderText(renderer, config, font, totalStr, ColorTextTertiary(), cx-int32(tw)/2, cy+16)
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

// drawPanel renders a translucent panel with an accent border and soft shadow.
func drawPanel(renderer *sdl.Renderer, x, y, w, h int32, fill, border sdl.Color) {
	// layered soft shadow (growing offset, fading alpha)
	fillRoundedRect(renderer, x+1, y+2, w, h, 10, ShadowFill(18))
	fillRoundedRect(renderer, x+3, y+4, w, h, 10, ShadowFill(26))
	fillRoundedRect(renderer, x+5, y+6, w, h, 10, ShadowFill(38))
	// fill
	fillRoundedRect(renderer, x, y, w, h, 10, fill)
	// subtle 1px top highlight line
	renderer.SetDrawColor(255, 255, 255, 12)
	renderer.FillRect(&sdl.Rect{X: x + 2, Y: y + 1, W: w - 4, H: 1})
	// border
	renderer.SetDrawColor(border.R, border.G, border.B, border.A)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: 1, H: h - 2})
	renderer.SetDrawColor(border.R, border.G, border.B, border.A/2)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + h - 2, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + w - 2, Y: y + 1, W: 1, H: h - 2})
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

// drawRow renders a single list row with a consistent surface and optional
// selection (accent tint + left accent bar + inner highlight) or hover state.
func drawRow(renderer *sdl.Renderer, x, y, w, h, r int32, selected, hovered bool) {
	fillRoundedRect(renderer, x+1, y+1, w, h, r, ShadowFill(40))
	fillRoundedRect(renderer, x, y, w, h, r, ColorSurfaceRow)
	if selected {
		fillRoundedRect(renderer, x, y, w, h, r, WithAlpha(accentColor, 60))
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: 4, H: h})
		fillRoundedRect(renderer, x+1, y+1, w-2, h/2, r-1, GlossFill(10))
	} else if hovered {
		fillRoundedRect(renderer, x, y, w, h, r, GlossFill(6))
	}
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
func buttonHitSize(elem Element, font *ttf.Font) (int32, int32) {
	textWidth, textHeight := int32(0), int32(0)
	if font != nil {
		w, h, _ := font.SizeUTF8(elem.Text)
		textWidth, textHeight = int32(w), int32(h)
	}
	width := textWidth + 56
	height := textHeight + 36
	if string(elem.Width) != "" {
		if w, _ := strconv.Atoi(string(elem.Width)); w > 0 {
			width = int32(w)
		}
	}
	if string(elem.Height) != "" {
		if h, _ := strconv.Atoi(string(elem.Height)); h > 0 {
			height = int32(h)
		}
	}
	return width, height
}

func renderButtonElement(renderer *sdl.Renderer, config *Config, elem Element, elemIndex int, selected bool, hovered bool, hoverProgress float64, pressed bool) {
	if elem.Style == "tile" {
		renderAppTile(renderer, config, elem, elemIndex, selected, hovered, hoverProgress, pressed)
		return
	}
	// Every non-tile button renders through the shared flat dark primitive so
	// the whole app shares one button look (no silver gradients, gloss, or
	// layered shadows that produced feet/tabs in the legacy renderer).
	renderDarkButtonElement(renderer, config, elem, selected, hovered, pressed)
}

// renderDarkButtonElement renders a flat, compact dark button (used by the
// Tube toolbar): no gradient, gloss, or drop shadow — just a dark neutral
// surface, a thin border, and a strong cyan focus ring so it matches the home
// design language instead of reading as a bright desktop widget.
func renderDarkButtonElement(renderer *sdl.Renderer, config *Config, elem Element, selected, hovered, pressed bool) {
	font, _ := getCachedFont(config, elem.Font)
	width, height := buttonHitSize(elem, font)
	x, y := elem.X, elem.Y
	r := int32(10)

	pressOffset := int32(0)
	if pressed {
		pressOffset = 2
	}
	ox := x + pressOffset
	oy := y + pressOffset

	fill := resolveColor(config, elem.BgColor, ColorCard)
	border := ColorBorder
	if selected || hovered {
		fill = ColorCardFocus
		border = ColorAccent
	}
	if pressed {
		fill = darken(fill, 18)
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, ox, oy, width, height, r, fill)
	thickness := int32(1)
	if selected {
		thickness = 3
	}
	strokeRoundedRect(renderer, ox, oy, width, height, r, thickness, border)

	if font != nil {
		txt := ColorTextPrimary()
		lum := int(fill.R)*299 + int(fill.G)*587 + int(fill.B)*114
		if lum > 60000 {
			txt = sdl.Color{R: 18, G: 22, B: 30, A: 255}
		}
		w, h, _ := font.SizeUTF8(elem.Text)
		tx := ox + (width-int32(w))/2
		ty := oy + (height-int32(h))/2
		renderText(renderer, config, font, elem.Text, txt, tx, ty)
	}
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

	pressOffset := int32(0)
	if pressed {
		pressOffset = PressOffset
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

// ffplayEnv returns an environment for ffplay that includes the directory
// containing ffplay.exe on Windows. This ensures dependent DLLs are found
// (fixes 0xc0000135 / STATUS_DLL_NOT_FOUND on some systems).
func ffplayEnv(ffplayPath string) []string {
	env := os.Environ()
	if IsWindows() {
		dir := filepath.Dir(ffplayPath)
		if dir != "" && dir != "." {
			path := os.Getenv("PATH")
			env = append(env, "PATH="+dir+string(os.PathListSeparator)+path)
		}
	}
	env = append(env,
		"SDL_AUDIODRIVER=directsound",
	)
	// NOTE: Do NOT force SDL_VIDEODRIVER=directx here. On some systems the
	// DirectX SDL backend fails to initialize for the ffplay child process
	// ("Could not initialize SDL - directx not available"), while the default
	// auto-detected backend (which IPTV playback uses successfully) works.
	// Letting SDL pick its own video driver is the reliable choice.
	return env
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
	// Check tools_path variable (config-driven)
	if config.Variables.ToolsPath != "" {
		fullPath := filepath.Join(config.Variables.ToolsPath, exeName)
		if _, err := os.Stat(fullPath); err == nil {
			return resolve(fullPath)
		}
		if _, err := os.Stat(config.Variables.ToolsPath); err == nil {
			return resolve(config.Variables.ToolsPath)
		}
	}
	// Check ./required/ subfolder
	commonPath := filepath.Join(".", "required", exeName)
	if _, err := os.Stat(commonPath); err == nil {
		return resolve(commonPath)
	}
	// Fallback to system PATH
	return exeName
}

// --- Quote path if it contains spaces ---
func quotePath(path string) string {
	if strings.ContainsAny(path, " ") {
		return `"` + path + `"`
	}
	return path
}

// ytDlpExtraArgs returns extra yt-dlp arguments based on config, such as
// --cookies and --extractor-args for the YouTube extractor.
func ytDlpExtraArgs(config *Config) string {
	var parts []string
	if config == nil {
		return ""
	}
	if v, ok := config.Variables.Custom["google_api_key"].(string); ok && v != "" {
		parts = append(parts, "--extractor-args", "youtube:api_key="+v)
	}
	if v, ok := config.Variables.Custom["youtube_cookies_file"].(string); ok && v != "" {
		if _, err := os.Stat(v); err == nil {
			parts = append(parts, "--cookies", v)
		}
	}
	return strings.Join(parts, " ")
}

// ytDlpExtraArgsSlice returns extra yt-dlp arguments as a string slice based on
// config, suitable for appending to exec.Command args.
func ytDlpExtraArgsSlice(config *Config) []string {
	var parts []string
	if config == nil {
		return nil
	}
	if v, ok := config.Variables.Custom["google_api_key"].(string); ok && v != "" {
		parts = append(parts, "--extractor-args", "youtube:api_key="+v)
	}
	if v, ok := config.Variables.Custom["youtube_cookies_file"].(string); ok && v != "" {
		if _, err := os.Stat(v); err == nil {
			parts = append(parts, "--cookies", v)
		}
	}
	return parts
}

// --- Thumbnail loading with cache ---
var httpClient = &http.Client{
	Timeout: 8 * time.Second,
}

func loadThumbnail(renderer *sdl.Renderer, url string) *sdl.Texture {
	return loadThumbnailFromURLs(renderer, []string{url})
}

func loadThumbnailFromURLs(renderer *sdl.Renderer, urls []string) *sdl.Texture {
	for _, rawURL := range urls {
		if rawURL == "" {
			continue
		}
		// Strip query parameters — YouTube serves WebP with sqp params,
		// but the bare .jpg URL works without libwebpdemux-2.dll.
		url := rawURL
		if i := strings.Index(url, "?"); i >= 0 {
			url = url[:i]
		}
		// If the URL doesn't already point at a directly-loadable image
		// extension, rewrite common patterns to .jpg for SDL2_image
		// compatibility (avoids needing libwebpdemux-2.dll for WebP).
		lower := strings.ToLower(url)
		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".bmp") {
			url = strings.TrimSuffix(url, ".webp") + ".jpg"
		}
		if tex, ok := textureCache[url]; ok {
			return tex
		}

		resp, err := httpClient.Get(url)
		if err != nil {
			log.Printf("[DEBUG] loadThumbnail: HTTP GET failed for %s: %v", url, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Printf("[DEBUG] loadThumbnail: HTTP %d for %s", resp.StatusCode, url)
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("[DEBUG] loadThumbnail: read body failed for %s: %v", url, err)
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
		textureCache[url] = tex
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
	tool := tokens[0]
	args := tokens[1:]
	fullToolPath := getToolPath(tool, config)

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
				return
			}
		}
		db.Close()
	}

	// Run the tool directly (no shell) so special characters are not
	// interpreted by cmd.exe / sh (e.g. "<" as input redirection).
	cmd := exec.Command(fullToolPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Printf("[ERROR] yt-dlp execution error: %v", err)
		log.Printf("[ERROR] stderr: %s", stderr.String())
		publishCustom("search_error", fmt.Sprintf("yt-dlp error: %v\n%s", err, stderr.String()))
		publishCustom(resultVar, []VideoInfo{})
		showToast("Search failed. Check network.", ToastError())
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
			msg := fmt.Sprintf("Error: %v", errMsg)
			w, h, _ := font.SizeUTF8(msg)
			renderText(renderer, config, font, msg, sdl.Color{R: 255, G: 107, B: 122, A: 255},
				element.X+(elemWidth-int32(w))/2, element.Y+(elemHeight-int32(h))/2-14)
			w2, _, _ := font.SizeUTF8("Check terminal for details.")
			renderText(renderer, config, font, "Check terminal for details.", ColorTextMuted(),
				element.X+(elemWidth-int32(w2))/2, element.Y+(elemHeight-int32(h))/2+10)
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
			title := ellipsize(titleFont, vid.Title, textW)
			meta := ellipsize(font, resultMetaLine(vid), textW)

			th := int32(titleFont.Height())
			mh := int32(font.Height())
			stackH := th + 6 + mh
			textTop := cardY + (cardH-stackH)/2
			drawText(r, titleFont, title, textX, textTop, ColorTextPrimary(), textAlignLeft)
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

			title := ellipsize(titleFont, vid.Title, textW)
			renderText(r, config, titleFont, title, ColorTextPrimary(), textStartX, textY)

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

// playVideoInfo starts playback for a video and records it in the recently-
// played store. A nonzero v.Position (set by the Continue card) resumes the
// embedded player from that offset.
func playVideoInfo(config *Config, v VideoInfo) {
	url := v.GetURL()
	if url == "" {
		log.Printf("playVideoInfo: empty URL, nothing to play")
		return
	}
	PlayActivateSound()
	addRecentVideo(v)
	currentPlaybackURL = url

	backend := strings.TrimSpace(config.Variables.AudioBackend)
	if backend == "" {
		backend = "ffplay"
	}
	if strings.EqualFold(backend, "mpv") {
		playWithMPV(config, url)
		return
	}

	go func() {
		ffplayPath := getToolPath("ffplay", config)
		ytDlpPath := getToolPath("yt-dlp", config)

		if _, err := os.Stat(ffplayPath); os.IsNotExist(err) {
			log.Printf("[ERROR] playVideoURL: ffplay not found at %s", ffplayPath)
			showToast("ffplay not found. Check tools path.", ToastError())
			return
		}
		if _, err := os.Stat(ytDlpPath); os.IsNotExist(err) {
			log.Printf("[ERROR] playVideoURL: yt-dlp not found at %s", ytDlpPath)
			showToast("yt-dlp not found. Check tools path.", ToastError())
			return
		}

		log.Printf("[DEBUG] playVideoURL: url=%q ffplay=%q yt-dlp=%q", url, ffplayPath, ytDlpPath)

		// On Windows, skip pipe mode entirely — it deadlocks due to SDL/pipe
		// buffering. Use temp-file mode with ffplay (works on Trimui Smart Pro).
		if IsWindows() {
			log.Printf("[DEBUG] playVideoInfo: Windows detected, using temp file mode")
			playWithTempFile(config, ffplayPath, ytDlpPath, url, v.Position)
			recordPlayed(config, url)
			return
		}

		// Pipe mode for Linux/other platforms
		pipeSuccess := false
		ytArgs := []string{
			"-o", "-",
			"--no-check-certificate",
			"--geo-bypass",
			"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
			"-f", "best[height<=720]/best",
			url,
		}
		ytArgs = append(ytArgs, ytDlpExtraArgsSlice(config)...)
		ytCmd := exec.Command(ytDlpPath, ytArgs...)
		pipeArgs := []string{
			"-i", "pipe:0",
			"-loglevel", "warning",
			"-framedrop",
			"-autoexit",
		}
		if runtime.GOOS == "windows" {
			pipeArgs = append([]string{"-noborder", "-x", "1280", "-y", "720"}, pipeArgs...)
		} else {
			pipeArgs = append([]string{"-fs"}, pipeArgs...)
		}
		ffCmd := exec.Command(ffplayPath, pipeArgs...)
		ffCmd.Env = ffplayEnv(ffplayPath)

		pipe, err := ytCmd.StdoutPipe()
		if err == nil {
			ffCmd.Stdin = pipe
			ytCmd.Stderr = os.Stderr
			ffCmd.Stderr = os.Stderr
			// Start ffplay FIRST so it's ready to read from the pipe before
			// yt-dlp starts producing data. On Windows the pipe buffer is small,
			// so starting yt-dlp first causes it to block immediately.
			if err := ffCmd.Start(); err == nil {
				// Brief pause to let ffplay open its stdin before yt-dlp writes.
				time.Sleep(300 * time.Millisecond)
				if err := ytCmd.Start(); err == nil {
					ytCmd.Wait()
					ffCmd.Wait()
					pipeSuccess = true
				} else {
					log.Printf("[DEBUG] playVideoURL: yt-dlp.Start failed in pipe mode: %v", err)
					ffCmd.Process.Kill()
					ffCmd.Wait()
				}
			} else {
				log.Printf("[DEBUG] playVideoURL: ffplay.Start failed in pipe mode: %v", err)
			}
		} else {
			log.Printf("[DEBUG] playVideoURL: StdoutPipe failed: %v", err)
		}

		if !pipeSuccess {
			log.Printf("[DEBUG] playVideoInfo: pipe mode failed, trying temp file fallback")
			playWithTempFile(config, ffplayPath, ytDlpPath, url, v.Position)
			recordPlayed(config, url)
		} else {
			recordPlayed(config, url)
		}
	}()
}

func playWithDirectURL(config *Config, ffplayPath, ytDlpPath, url string) {
	log.Printf("[DEBUG] playWithDirectURL: trying --get-url for %s", url)
	getArgs := []string{
		"--get-url",
		"--no-check-certificate",
		"--geo-bypass",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		"-f", "best[height<=720]/best",
		url,
	}
	getArgs = append(getArgs, ytDlpExtraArgsSlice(config)...)
	getCmd := exec.Command(ytDlpPath, getArgs...)
	getCmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	getCmd.Stderr = os.Stderr
	directURLBytes, err := getCmd.Output()
	directURL := strings.TrimSpace(string(directURLBytes))
	if err != nil || directURL == "" || !strings.HasPrefix(directURL, "http") {
		log.Printf("[DEBUG] playWithDirectURL: get-url failed or empty (err=%v), using original URL", err)
		directURL = url
	}
	log.Printf("[DEBUG] playWithDirectURL: directURL=%q", directURL)

	args := []string{
		"-loglevel", "error",
		"-i", directURL,
		"-framedrop",
		"-autoexit",
		"-user_agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	}
	if IsWindows() {
		args = append([]string{"-noborder", "-x", "1280", "-y", "720"}, args...)
	} else {
		args = append([]string{"-fs"}, args...)
	}
	ffCmd2 := exec.Command(ffplayPath, args...)
	ffCmd2.Env = ffplayEnv(ffplayPath)

	var stderrBuf bytes.Buffer
	ffCmd2.Stderr = &stderrBuf
	if err := ffCmd2.Start(); err != nil {
		log.Printf("[ERROR] playWithDirectURL: ffplay.Start failed: %v", err)
		showToast("Playback failed. Check ffplay in required/.", ToastError())
		return
	}
	log.Printf("[DEBUG] playWithDirectURL: ffplay started, waiting...")
	ffCmd2.Wait()
	log.Printf("[DEBUG] playWithDirectURL: ffplay finished")
	if stderrBuf.Len() > 0 {
		log.Printf("[ERROR] playWithDirectURL: ffplay stderr: %s", stderrBuf.String())
	}
}

func playWithTempFile(config *Config, ffplayPath, ytDlpPath, url string, startSec float64) {
	log.Printf("[DEBUG] playWithTempFile: downloading to temp file for %s (resume %.1fs)", url, startSec)

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "downloading"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 0
	videoPlaybackSpeed = ""
	videoPlaybackETA = ""
	videoPlaybackError = ""
	videoPlaybackMutex.Unlock()

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("jukahub-video-%d.mp4", time.Now().UnixNano()))

	// Clean up any stale temp file from a previous attempt
	os.Remove(tempPath)

	var stderrBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dlArgs := []string{
		"-o", tempPath,
		"--no-check-certificate",
		"--geo-bypass",
		"--no-continue",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		"-f", "best[height<=720]/best",
		url,
	}
	dlArgs = append(dlArgs, ytDlpExtraArgsSlice(config)...)
	dlCmd := exec.CommandContext(ctx, ytDlpPath, dlArgs...)
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
				log.Printf("[ERROR] playWithTempFile: download failed: %v", err)
				log.Printf("[ERROR] playWithTempFile: stderr: %s", stderrBuf.String())
				showToast("Download failed. Check network.", ToastError())
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

	StartEmbeddedPlaybackAt(config, tempPath, startSec)
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
				}
				if strings.HasPrefix(part, "ETA") {
					videoPlaybackETA = part
				}
			}
		}
	}
}

// --- Input handling ---
func handleInputSelection(renderer *sdl.Renderer, config *Config, sceneIdx, elemIdx int) {
	if sceneIdx < 0 || sceneIdx >= len(config.Scenes) {
		return
	}
	if elemIdx < 0 || elemIdx >= len(config.Scenes[sceneIdx].Elements) {
		return
	}
	activeSceneIndex = sceneIdx
	activeElementIndex = elemIdx
	virtualKeyboardActive = true
	sdl.StartTextInput()
	inputTextBuffer = ""
	if variable := config.Scenes[sceneIdx].Elements[elemIdx].Variable; variable != "" {
		inputTextBuffer = inputVariableValue(config, variable)
	}
	handleInputElement(renderer, config)
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

func handleInputElement(renderer *sdl.Renderer, config *Config) {
	defer func() { virtualKeyboardActive = false }()
	exitInput := false
	for !exitInput {
		renderer.SetDrawColor(249, 249, 249, 255)
		renderer.Clear()
		drainCustom(config)
		renderScene(renderer, config, config.Scenes[currentSceneIndex])
		renderKeyboard(renderer, config)

		// Prominent display of the text being typed so it is clearly visible
		// (the underlying input field can be small / easy to miss).
		bigFont, _ := getCachedFont(config, "big")
		if bigFont != nil {
			boxW := int32(900)
			boxX := (screenWidth - boxW) / 2
			boxY := int32(36)
			fillRoundedRect(renderer, boxX, boxY, boxW, 64, 10, WithAlpha(ColorSurfaceCard, 235))
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
			renderer.FillRect(&sdl.Rect{X: boxX, Y: boxY, W: boxW, H: 3})
			display := inputTextBuffer
			if len(display) == 0 {
				display = "Type here..."
			} else if uint32(sdl.GetTicks64()/500)%2 == 0 {
				display += "_"
			}
			renderText(renderer, config, bigFont, display, sdl.Color{R: 235, G: 238, B: 245, A: 255}, boxX+18, boxY+16)
		}

		renderer.Present()

		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			switch e := event.(type) {
			case *sdl.KeyboardEvent:
				if e.Type == sdl.KEYDOWN {
					switch e.Keysym.Sym {
					case sdl.K_ESCAPE:
						exitInput = true
					case sdl.K_BACKSPACE:
						if len(inputTextBuffer) > 0 {
							inputTextBuffer = inputTextBuffer[:len(inputTextBuffer)-1]
							updateInputVariable(config)
						}
					case sdl.K_RETURN:
						// Submit the typed text and close the input (do not append
						// the currently-highlighted virtual key).
						updateInputVariable(config)
						exitInput = true
					default:
						// Physical keyboard typing (desktop) â€” convert the key
						// to a character and accumulate into the buffer.
						if ch, ok := keysymToChar(e.Keysym.Sym, e.Keysym.Mod&sdl.KMOD_SHIFT != 0); ok {
							inputTextBuffer += ch
							updateInputVariable(config)
						}
					}
				}
			case *sdl.ControllerButtonEvent:
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
					switch e.Button {
					case sdl.CONTROLLER_BUTTON_DPAD_UP:
						if keyboardPosY > 0 {
							keyboardPosY--
						}
					case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
						if keyboardPosY < len(keyboard)-1 {
							keyboardPosY++
						}
					case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
						if keyboardPosX > 0 {
							keyboardPosX--
						}
					case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
						if keyboardPosX < len(keyboard[keyboardPosY])-1 {
							keyboardPosX++
						}
					case sdl.CONTROLLER_BUTTON_A:
						handleKeyboardInput(config)
						exitInput = true
					case sdl.CONTROLLER_BUTTON_B:
						exitInput = true
					case sdl.CONTROLLER_BUTTON_BACK:
						toggleKeyboardCase()
					}
				}
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
					mx, my := int32(e.X), int32(e.Y)
					for idx, rect := range keyboardRects {
						if mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H {
							if idx < len(keyboardRowCol) {
								keyboardPosY, keyboardPosX = keyboardRowCol[idx][0], keyboardRowCol[idx][1]
							}
							handleKeyboardInput(config)
							if keyboardPosY >= 0 && keyboardPosY < len(keyboard) &&
								keyboardPosX >= 0 && keyboardPosX < len(keyboard[keyboardPosY]) &&
								keyboard[keyboardPosY][keyboardPosX] == "ENTER" {
								exitInput = true
							}
							break
						}
					}
				}
			case *sdl.TouchFingerEvent:
				handleTouchGesture(e, config)
			case *sdl.MultiGestureEvent:
				handleMultiGesture(e, config)
			}
		}
	}
}

func handleKeyboardInput(config *Config) {
	if keyboardPosY >= 0 && keyboardPosY < len(keyboard) && keyboardPosX >= 0 && keyboardPosX < len(keyboard[keyboardPosY]) {
		key := keyboard[keyboardPosY][keyboardPosX]
		switch key {
		case "⇧":
			toggleKeyboardCase()
		case "SPACE":
			inputTextBuffer += " "
		case "BACK":
			if len(inputTextBuffer) > 0 {
				inputTextBuffer = inputTextBuffer[:len(inputTextBuffer)-1]
			}
		case "ENTER":
			// Save the final input before clearing
			updateInputVariable(config)
			virtualKeyboardActive = false
			activeSceneIndex = -1
			activeElementIndex = -1
			// If the active scene is a search scene, trigger its search button
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
				sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
				for _, elem := range config.Scenes[currentSceneIndex].Elements {
					if elem.Type == "button" && elem.Trigger == "yt_search" {
						go executeYouTubeSearch(config, elem.TriggerTarget, elem.TriggerValue, snapshotVars(config))
						break
					}
				}
			}
		default:
			inputTextBuffer += key
		}
		if key != "ENTER" && key != "⇧" {
			updateInputVariable(config)
		}
	}
}

func toggleKeyboardCase() {
	keyboardUpper = !keyboardUpper
	buildKeyboard()
}

func updateInputVariable(config *Config) {
	if activeSceneIndex != -1 && activeElementIndex != -1 {
		if activeSceneIndex >= len(config.Scenes) {
			return
		}
		elem := config.Scenes[activeSceneIndex].Elements[activeElementIndex]
		if elem.Variable != "" {
			config.Variables.Custom[elem.Variable] = inputTextBuffer
			syncVariableOverrides(config)
		}
	}
}

// --- Touch gesture handling ---

func handleTouchGesture(e *sdl.TouchFingerEvent, config *Config) {
	if e.Type == sdl.FINGERDOWN {
		touchStartX = float64(e.X)
		touchStartY = float64(e.Y)
		touchStartTime = sdl.GetTicks64()
		touchActive = true
		touchPinchActive = false
	} else if e.Type == sdl.FINGERUP && touchActive {
		dx := float64(e.X) - touchStartX
		dy := float64(e.Y) - touchStartY
		dt := float64(sdl.GetTicks64() - touchStartTime)
		if dt > 0 && dt < 500 {
			dist := dx*dx + dy*dy
			if dist > 3600 {
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
		touchActive = false
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
	if v, ok := c["fullscreen"].(string); ok {
		config.Variables.Fullscreen = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := c["weatherEnabled"].(string); ok {
		config.Variables.WeatherEnabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := c["weatherUnit"].(string); ok {
		config.Variables.WeatherUnit = strings.TrimSpace(v)
	}
	if v, ok := c["tspUsername"].(string); ok {
		config.Variables.TSPUsername = strings.TrimSpace(v)
	}
	if v, ok := c["playbackResolution"].(string); ok {
		config.Variables.PlaybackResolution = strings.TrimSpace(v)
	}
	if v, ok := c["audioBackend"].(string); ok {
		config.Variables.AudioBackend = strings.TrimSpace(v)
	}
	if v, ok := c["fileExplorerRoot"].(string); ok {
		config.Variables.FileExplorerRoot = strings.TrimSpace(v)
	}
	if v, ok := customString(c, "buttonColor"); ok {
		applyColorVar(config, "buttonColor", v)
	}
	if v, ok := customString(c, "labelColor"); ok {
		applyColorVar(config, "labelColor", v)
	}
	if v, ok := customString(c, "inputColor"); ok {
		applyColorVar(config, "inputColor", v)
	}
	saveUserConfigDebounced(&UserConfig{Variables: UserVariables{
		ButtonColor:        config.Variables.ButtonColor,
		LabelColor:         config.Variables.LabelColor,
		InputColor:         config.Variables.InputColor,
		Fullscreen:         config.Variables.Fullscreen,
		FileExplorerRoot:   config.Variables.FileExplorerRoot,
		WeatherEnabled:     config.Variables.WeatherEnabled,
		WeatherUnit:        config.Variables.WeatherUnit,
		TSPUsername:        config.Variables.TSPUsername,
		PlaybackResolution: config.Variables.PlaybackResolution,
		AudioBackend:       config.Variables.AudioBackend,
		Custom:             config.Variables.Custom,
	}})
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
	case "audioBackend":
		return config.Variables.AudioBackend
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
	}
	if val, ok := config.Variables.Custom[v]; ok {
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func renderInputField(renderer *sdl.Renderer, config *Config, element Element, sceneIdx, elemIdx int) {
	width := int32(200)
	if string(element.Width) != "" {
		w, _ := strconv.Atoi(string(element.Width))
		width = int32(w)
	}
	height := int32(40)
	if string(element.Height) != "" {
		h, _ := strconv.Atoi(string(element.Height))
		height = int32(h)
	}
	bgColor := resolveColor(config, element.BgColor, sdl.Color{R: 20, G: 24, B: 34, A: 255})
	r := int32(8)
	isActive := (sceneIdx == activeSceneIndex && elemIdx == activeElementIndex)

	// subtle shadow
	fillRoundedRect(renderer, element.X+1, element.Y+1, width, height, r, ShadowFill(40))

	if isActive {
		// soft layered focus glow
		fillRoundedRect(renderer, element.X-3, element.Y-3, width+6, height+6, r+3,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 18})
		fillRoundedRect(renderer, element.X-2, element.Y-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 40})
		// crisp inner border
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
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
		renderText(renderer, config, font, text, textColor, element.X+14, element.Y+12)
	}

	// placeholder
	if !isActive && text == "" {
		placeholder := element.Placeholder
		if placeholder == "" {
			placeholder = "Type here..."
		}
		phColor := ColorTextTertiary()
		renderText(renderer, config, font, placeholder, phColor, element.X+14, element.Y+12)
	}
}

// --- renderScene ---
func renderScene(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	if scene.Name == "JukaLand" {
		updateJukaLand()
		renderJukaLand(renderer, config)
		renderStatusBar(renderer, config)
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
		renderHomeModern(renderer, config)
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetClipRect(nil)
		return
	}

	// Content panel behind the scene elements to frame the form/list area.
	// It ends 16px above the footer so no content can reach the footer band.
	panelMargin := int32(24)
	panelTop := HomeTopBarH + 8
	panelBottom := screenHeight - HomeFooterH - 16
	panel := sdl.Rect{
		X: panelMargin,
		Y: panelTop,
		W: screenWidth - panelMargin*2,
		H: panelBottom - panelTop,
	}
	fillRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, RadiusLG, WithAlpha(ColorSurface, 210))
	strokeRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, RadiusLG, 1, WithAlpha(ColorBorder, 120))

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

	// Single global header: brand + version + active scene + status.
	renderStatusBar(renderer, config)

	// Shared footer with contextual controller hints.
	renderFooter(renderer, config)

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

// themeGalleryCardAt returns the preset index under (mx, my) or -1. The card
// width is derived from the element width so any preset count fits.
func themeGalleryCardAt(element Element, mx, my int32) int {
	presets := ListThemePresets()
	if len(presets) == 0 {
		return -1
	}
	gap := int32(20)
	cardW := (getElementWidth(element, 1160) - gap*int32(len(presets)-1)) / int32(len(presets))
	cardH := int32(190)
	for i := range presets {
		x := element.X + int32(i)*(cardW+gap)
		if mx >= x && mx < x+cardW && my >= element.Y && my < element.Y+cardH {
			return i
		}
	}
	return -1
}

// renderThemeGallery draws the theme presets as preview cards: each shows the
// theme name, its background/surface/accent swatches, and a Current badge on
// the active preset. The focused card uses the same 3px cyan ring as home
// cards so selection is unmistakable.
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

	gap := int32(20)
	cardW := (getElementWidth(element, 1160) - gap*int32(len(presets)-1)) / int32(len(presets))
	cardH := int32(190)

	for i, name := range presets {
		p := GetThemePreset(name)
		x := element.X + int32(i)*(cardW+gap)
		y := element.Y
		focused := i == themeGalleryIndex

		fill := ColorCard
		if focused {
			fill = ColorCardFocus
		}
		fillRoundedRect(renderer, x, y, cardW, cardH, 16, fill)
		if focused {
			strokeRoundedRect(renderer, x, y, cardW, cardH, 16, 3, ColorAccent)
		} else {
			strokeRoundedRect(renderer, x, y, cardW, cardH, 16, 1, ColorBorder)
		}

		// Theme preview panel: the preset's background with an "Aa" text
		// sample and an accent strip, so each card reads like a real screen.
		prevX := x + 12
		prevY := y + 12
		prevW := cardW - 24
		prevH := int32(84)
		fillRoundedRect(renderer, prevX, prevY, prevW, prevH, 10, hexRGBA(p.Background))
		strokeRoundedRect(renderer, prevX, prevY, prevW, prevH, 10, 1, hexRGBA(p.BorderDefault))
		drawText(renderer, font, "Aa", prevX+14, prevY+8, hexRGBA(p.TextPrimary), textAlignLeft)
		// Accent strip along the preview's bottom edge.
		fillRoundedRect(renderer, prevX, prevY+prevH-7, prevW, 7, 3, hexRGBA(p.Info))

		// Current badge on the preview corner.
		if name == current {
			badgeW := int32(86)
			badgeH := int32(24)
			badgeX := prevX + prevW - badgeW - 10
			badgeY := prevY + 8
			fillRoundedRect(renderer, badgeX, badgeY, badgeW, badgeH, badgeH/2, hexRGBA(p.Info))
			drawText(renderer, small, "Current", badgeX+badgeW/2, badgeY+(badgeH-int32(small.Height()))/2,
				contrastText(hexRGBA(p.Info)), textAlignCenter)
		}

		// Theme name below the preview.
		nameY := prevY + prevH + 12
		drawText(renderer, font, p.Name, x+18, nameY, ColorTextPrimary(), textAlignLeft)

		// Mini palette chips: Surface, Text, Focus border.
		chipY := nameY + int32(font.Height()) + 10
		chipH := int32(16)
		chipW := (cardW - 36 - 2*8) / 3
		chips := []string{p.SurfaceAlt, p.TextPrimary, p.BorderFocus}
		for i, c := range chips {
			cx := x + 18 + int32(i)*(chipW+8)
			fillRoundedRect(renderer, cx, chipY, chipW, chipH, 4, hexRGBA(c))
			strokeRoundedRect(renderer, cx, chipY, chipW, chipH, 4, 1, ColorBorder)
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
	width := int32(320)
	if string(element.Width) != "" {
		if w, err := strconv.Atoi(string(element.Width)); err == nil {
			width = int32(w)
		}
	}
	height := int32(48)
	if string(element.Height) != "" {
		if h, err := strconv.Atoi(string(element.Height)); err == nil {
			height = int32(h)
		}
	}

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
	labelCol := ColorTextPrimary()
	drawText(renderer, font, label, element.X+18, element.Y+(height-int32(font.Height()))/2, labelCol, textAlignLeft)

	// ON/OFF pill at the right.
	pillW := int32(76)
	pillH := int32(32)
	pillX := element.X + width - pillW - 14
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

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// easeOutCubic applies a smooth cubic easing to t (0..1).
func easeOutCubic(t float64) float64 {
	return 1 - math.Pow(1-t, 3)
}

// lerp linearly interpolates between a and b by t (0..1).
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// lerpInt32 returns an int32 lerp.
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
func ShowToast(message string, kind ToastKind) {
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

func renderKeyboard(renderer *sdl.Renderer, config *Config) {
	if !virtualKeyboardActive {
		return
	}

	// frosted backdrop covering the whole screen
	fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, WithAlpha(ColorBackground, 235))

	margin := int32(40)
	availW := screenWidth - 2*margin
	gap := int32(10)
	keyHeight := int32(58)
	maxCols := 10
	keyWidth := (availW - int32(maxCols-1)*gap) / int32(maxCols)
	if keyWidth > 92 {
		keyWidth = 92
	}
	if keyWidth < 40 {
		keyWidth = 40
	}
	panelPad := int32(20)
	rows := len(keyboard)
	totalH := int32(rows)*keyHeight + int32(rows-1)*gap
	panelX := margin - panelPad
	panelY := int32(170) - panelPad
	panelW := availW + 2*panelPad
	panelH := totalH + 2*panelPad + 96
	drawCard(renderer, panelX, panelY, panelW, panelH, 16)

	// live input display bar
	barX := panelX + panelPad
	barY := panelY + panelPad
	barW := panelW - 2*panelPad
	barH := int32(48)
	fillRoundedRect(renderer, barX, barY, barW, barH, 10, ColorSurfaceRaised)
	renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, ColorBorderDefault.A)
	renderer.DrawRect(&sdl.Rect{X: barX + 1, Y: barY + 1, W: barW - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: barX + 1, Y: barY + 1, W: 1, H: barH - 2})
	kfont, _ := getCachedFont(config, "medium")
	if kfont == nil {
		kfont, _ = getCachedFont(config, "small")
	}
	if kfont != nil {
		cursor := ""
		if uint32(sdl.GetTicks64()/500)%2 == 0 {
			cursor = "_"
		}
		display := inputTextBuffer + cursor
		col := ColorTextPrimary()
		if inputTextBuffer == "" {
			display = "Type to search" + cursor
			col = ColorTextTertiary()
		}
		renderText(renderer, config, kfont, display, col, barX+16, barY+(barH-14)/2)
	}

	// key grid
	keyboardRects = nil
	keyboardKeys = nil
	keyboardRowCol = nil
	gridTop := barY + barH + 24
	for y, row := range keyboard {
		widths := make([]int32, len(row))
		var rowWidth int32
		for x, key := range row {
			w := keyWidth
			if y == 3 {
				switch key {
				case "SPACE":
					w = keyWidth*4 + gap*3
				case "BACK", "ENTER":
					w = keyWidth*2 + gap
				}
			}
			widths[x] = w
			rowWidth += w
		}
		rowWidth += int32(len(row)-1) * gap
		rx := (screenWidth - rowWidth) / 2
		ky := gridTop + int32(y)*(keyHeight+gap)
		for x, key := range row {
			kx := rx
			w := widths[x]
			isSelected := (x == keyboardPosX && y == keyboardPosY)
			special := key == "SPACE" || key == "BACK" || key == "ENTER"
			r := int32(10)

			miamiCyan := sdl.Color{R: 0, G: 220, B: 255, A: 255}
			miamiPink := sdl.Color{R: 255, G: 0, B: 200, A: 255}
			miamiPurple := sdl.Color{R: 100, G: 0, B: 255, A: 255}
			miamiDarkPurple := sdl.Color{R: 60, G: 0, B: 180, A: 255}

			fillRoundedRect(renderer, kx+2, ky+3, w, keyHeight, r, sdl.Color{R: 40, G: 0, B: 100, A: 180})
			if isSelected || (key == "⇧" && keyboardUpper) {
				fillRoundedRect(renderer, kx-3, ky-3, w+6, keyHeight+6, r+3, WithAlpha(miamiPink, 170))
				fillRoundedRect(renderer, kx, ky, w, keyHeight, r, miamiPink)
				fillRoundedRect(renderer, kx+1, ky+1, w-2, keyHeight/2, r-1, GlossFill(50))
			} else if special {
				fillRoundedRect(renderer, kx, ky, w, keyHeight, r, miamiPurple)
				fillRoundedRect(renderer, kx+1, ky+1, w-2, keyHeight/2, r-1, GlossFill(10))
				renderer.SetDrawColor(miamiDarkPurple.R, miamiDarkPurple.G, miamiDarkPurple.B, 255)
				renderer.DrawRect(&sdl.Rect{X: kx + 1, Y: ky + 1, W: w - 2, H: 1})
				renderer.DrawRect(&sdl.Rect{X: kx + 1, Y: ky + 1, W: 1, H: keyHeight - 2})
			} else {
				fillRoundedRect(renderer, kx, ky, w, keyHeight, r, miamiCyan)
				fillRoundedRect(renderer, kx+1, ky+1, w-2, keyHeight/2, r-1, GlossFill(8))
				renderer.SetDrawColor(miamiPurple.R, miamiPurple.G, miamiPurple.B, 255)
				renderer.DrawRect(&sdl.Rect{X: kx + 1, Y: ky + 1, W: w - 2, H: 1})
				renderer.DrawRect(&sdl.Rect{X: kx + 1, Y: ky + 1, W: 1, H: keyHeight - 2})
			}

			keyboardRects = append(keyboardRects, sdl.Rect{X: kx, Y: ky, W: w, H: keyHeight})
			keyboardKeys = append(keyboardKeys, key)
			keyboardRowCol = append(keyboardRowCol, [2]int{y, x})

			label := key
			if key == "BACK" {
				label = "DEL"
			} else if key == "ENTER" {
				label = "OK"
			}
			if kfont != nil {
				kw, kh, _ := kfont.SizeUTF8(label)
				tx := kx + (w-int32(kw))/2
				ty := ky + (keyHeight-int32(kh))/2
				switch {
				case isSelected || (key == "⇧" && keyboardUpper):
					renderText(renderer, config, kfont, label, ColorTextInverse(), tx, ty)
				case special:
					renderText(renderer, config, kfont, label, ColorTextInverse(), tx, ty)
				default:
					renderText(renderer, config, kfont, label, sdl.Color{R: 0, G: 20, B: 40, A: 255}, tx, ty)
				}
			}
			rx += w + gap
		}
	}

	hintFont, _ := getCachedFont(config, "small")
	if hintFont != nil {
		renderText(renderer, config, hintFont, "Arrow keys to move · Enter to type · Esc to close", ColorTextTertiary(), margin, gridTop+totalH+14)
	}
}

func buildKeyboard() {
	lower := [][]string{
		{"q", "w", "e", "r", "t", "y", "u", "i", "o", "p"},
		{"a", "s", "d", "f", "g", "h", "j", "k", "l"},
		{"z", "x", "c", "v", "b", "n", "m"},
	}
	upper := [][]string{
		{"Q", "W", "E", "R", "T", "Y", "U", "I", "O", "P"},
		{"A", "S", "D", "F", "G", "H", "J", "K", "L"},
		{"Z", "X", "C", "V", "B", "N", "M"},
	}
	letters := lower
	if keyboardUpper {
		letters = upper
	}
	keyboard = make([][]string, len(letters))
	for i, row := range letters {
		keyboard[i] = make([]string, len(row))
		copy(keyboard[i], row)
	}
	keyboard = append(keyboard, []string{"⇧", "SPACE", "BACK", "ENTER"})
	keyboardPosX, keyboardPosY = 0, 0
}

func initKeyboard() {
	keyboardUpper = false
	buildKeyboard()
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
	if embeddedPlayer.phase == "idle" {
		return
	}
	RenderVideoOverlay(renderer, config, elem)
}

// --- Trigger handling ---
func handleTrigger(renderer *sdl.Renderer, config *Config, element Element) {
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
		cmd := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 20 "ytsearch20:$search_query"` + " " + ytDlpExtraArgs(config)
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
		cmd := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 20 "ytsearch20:$search_query"` + " " + ytDlpExtraArgs(config)
		go executeYouTubeSearch(config, cmd, "search_results", snapshotVars(config))
	case "youtube_trending":
		go fetchTrendingVideos(config, "search_results", snapshotVars(config))
	case "youtube_play":
		videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
		if ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoInfo(config, videos[focusedResultIndex])
		} else {
			log.Printf("youtube_play: no focused video to play")
		}
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
		// Persist favorites (incl. resume positions) before a hard exit.
		saveFavorites()
		os.Exit(0)
	case "external_app":
		if element.ExternalAppPath != "" {
			go func() {
				ctx := context.Background()
				var cmd *exec.Cmd
				if IsWindows() {
					cmd = exec.CommandContext(ctx, "cmd", "/c", element.ExternalAppPath)
				} else {
					cmd = exec.CommandContext(ctx, "sh", "-c", element.ExternalAppPath)
				}
				if err := cmd.Run(); err != nil {
					LogSceneOp(config.Scenes[currentSceneIndex].Name, "external_app").Error("failed", "err", err)
				}
			}()
		} else {
			log.Printf("external_app trigger has no path")
		}
	case "fe_list":
		feListDirectory(config)
	case "fe_up":
		feUp(config)
	case "ip_stream":
		if url, ok := config.Variables.Custom["stream_url"].(string); ok && strings.TrimSpace(url) != "" {
			playStream(config, strings.TrimSpace(url))
		} else {
			log.Printf("ip_stream: no URL in stream_url")
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
			sendChatMessage("User", inputTextBuffer)
			inputTextBuffer = ""
		}
	case "discord_connect":
		go func() {
			if err := discordConnect(); err != nil {
				discordStatus = "Discord: " + err.Error()
			} else {
				discordStatus = "Discord: connected"
			}
			user := &UserConfig{
				Variables: UserVariables{
					ButtonColor:        config.Variables.ButtonColor,
					LabelColor:         config.Variables.LabelColor,
					InputColor:         config.Variables.InputColor,
					Fullscreen:         config.Variables.Fullscreen,
					FileExplorerRoot:   config.Variables.FileExplorerRoot,
					WeatherEnabled:     config.Variables.WeatherEnabled,
					WeatherUnit:        config.Variables.WeatherUnit,
					TSPUsername:        config.Variables.TSPUsername,
					PlaybackResolution: config.Variables.PlaybackResolution,
					AudioBackend:       config.Variables.AudioBackend,
					Custom:             config.Variables.Custom,
				},
			}
			saveUserConfig(user)
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
			ext := strings.ToLower(filepath.Ext(entry.Path))
			if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".bmp" {
				imageViewerPath = entry.Path
				if imageViewerTexture != nil {
					imageViewerTexture.Destroy()
					imageViewerTexture = nil
				}
				imageViewerZoom = 1.0
				imageViewerPanX = 0
				imageViewerPanY = 0
			} else {
				showToast("Select an image file first", ToastError())
			}
		}
	case "send_to_chat":
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			entry := entries[focusedFileIndex]
			ext := strings.ToLower(filepath.Ext(entry.Path))
			if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".bmp" {
				sendChatMessage("User", "[Image: "+entry.Name+"]")
			} else if ext == ".txt" || ext == ".md" || ext == ".json" || ext == ".js" || ext == ".html" {
				data, err := os.ReadFile(entry.Path)
				if err != nil {
					sendChatMessage("System", "Failed to read file: "+err.Error())
				} else {
					content := string(data)
					if len(content) > 500 {
						content = content[:500] + "..."
					}
					sendChatMessage("User", content)
				}
			} else {
				sendChatMessage("User", "Sent file: "+entry.Name)
			}
		}
	case "canvas_open":
		entries, ok := getSceneFileEntries(config, config.Scenes[currentSceneIndex])
		if ok && focusedFileIndex >= 0 && focusedFileIndex < len(entries) {
			entry := entries[focusedFileIndex]
			openFileInCanvasSandbox(entry.Path)
		}
	case "save_config":
		syncVariableOverrides(config)
		// Apply the Fullscreen setting live instead of only on restart.
		if mainWindow != nil {
			if config.Variables.Fullscreen {
				mainWindow.SetFullscreen(1)
			} else {
				mainWindow.SetFullscreen(0)
			}
		}
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
	case "textbrowser_refresh":
		textBrowserRefresh(config, element)
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_system":
		config.Variables.Custom[element.Variable] = browseSystemInfo(element)
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_zeroconf":
		go func(v string, el Element) {
			publishCustom(v, browseZeroconfServicesWithTimeout(4*time.Second))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(element.Variable, element)
		textBrowserLastUpdate = time.Now().Unix()
	case "textbrowser_json":
		go func(v string, el Element) {
			publishCustom(v, browseJSONContent(el))
			if el.AutoRefresh {
				startTextBrowserAutoRefresh(config, el)
			}
		}(element.Variable, element)
		textBrowserLastUpdate = time.Now().Unix()
	case "textbrowser_processes":
		config.Variables.Custom[element.Variable] = getProcessTree()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_network":
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
		config.Variables.Custom[element.Variable] = sb.String()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_temps":
		config.Variables.Custom[element.Variable] = getSystemTemperature()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_services":
		config.Variables.Custom[element.Variable] = getRunningServices()
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_files":
		config.Variables.Custom[element.Variable] = scanLocalFiles("/media")
		textBrowserLastUpdate = time.Now().Unix()
		if element.AutoRefresh {
			startTextBrowserAutoRefresh(config, element)
		}
	case "textbrowser_copy":
		if text, ok := config.Variables.Custom[element.Variable].(string); ok && text != "" {
			if IsWindows() {
				exec.Command("cmd", "/c", "echo", text).Run()
			} else {
				exec.Command("pbcopy").Run()
			}
			showToast("Copied to clipboard", ToastInfo())
		}
	case "textbrowser_clear":
		config.Variables.Custom[element.Variable] = ""
		textBrowserScrollY = 0
		textBrowserScrollVelocity = 0
		textBrowserScrollCooldown = 0
	case "cache_clear":
		if db, err := cacheOpen("jukahub.cache"); err == nil {
			cacheClear(db)
			db.Close()
			showToast("Cache cleared", ToastInfo())
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
	for i, e := range scene.Elements {
		if e.Type == "button" || e.Type == "input" || e.Type == "searchresults" || e.Type == "dynamiclist" || e.Type == "favorites" || e.Type == "textbrowser" || e.Type == "packagelist" || e.Type == "recent" {
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

// goBackScene returns to the previous scene in history, falling back to Home
// ("Main"). B / Escape on any non-home scene routes through here; it never
// terminates the application.
func goBackScene(config *Config) {
	PlayBackSound()
	if prev, ok := popSceneHistory(); ok {
		switchScene(config, prev, false)
		return
	}
	if idx := findSceneIndex(config, "Main"); idx >= 0 {
		switchScene(config, idx, false)
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
				if err := discordConnect(); err != nil && err.Error() != "discord not configured" {
					log.Printf("[discord] connect failed: %v", err)
				}
			}()
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
				moveSelection(config, -1)
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
		moveSelection(config, -1)
	}
}

func moveSelection(config *Config, direction int) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	currentScene := config.Scenes[currentSceneIndex]
	focusEngine.SetGraph(currentScene.Name, focusEngine.BuildGraph(currentScene))

	var action Action
	if direction < 0 {
		action = ActionNavigateUp
	} else {
		action = ActionNavigateDown
	}

	result := focusEngine.Navigate(action)
	if result >= 0 {
		if result != selectedButtonIndex {
			PlayNavSound()
		}
		selectedButtonIndex = result
		// First time focus enters a search-results grid, highlight the first
		// result so navigation is immediately visible (instead of an odd wrap).
		if focusedResultIndex < 0 && result < len(currentScene.Elements) &&
			currentScene.Elements[result].Type == "searchresults" {
			focusedResultIndex = 0
		}
	} else {
		// Fallback to linear wrap if graph has no neighbor in that direction.
		var interactive []int
		for i, el := range currentScene.Elements {
			if el.Type == "button" || el.Type == "input" || el.Type == "searchresults" || el.Type == "dynamiclist" || el.Type == "recent" || el.Type == "themegallery" || el.Type == "toggle" {
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

// --- Main ---
func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic: %v\n%s", r, debug.Stack())
		}
	}()

	initLogging()

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_AUDIO | sdl.INIT_JOYSTICK | sdl.INIT_GAMECONTROLLER); err != nil {
		log.Fatal("SDL init error:", err)
	}
	defer sdl.Quit()

	if err := ttf.Init(); err != nil {
		log.Fatal("TTF init error:", err)
	}
	defer ttf.Quit()

	if err := img.Init(img.INIT_PNG | img.INIT_JPG); err != nil {
		log.Fatal("IMG init error:", err)
	}
	// Enable text input so physical keyboard typing works in input fields
	// (both the on-screen keyboard modal and the main loop).
	sdl.StartTextInput()
	defer img.Quit()

	config, err := LoadLastKnownGood("jukaconfig.json")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Validate and apply defaults.
	if err := NewConfigValidator().Validate(config); err != nil {
		LogScene("init").Warn("config validation failed, applying defaults", "err", err)
	}

	// Merge mutable user settings from jukauser.json over design defaults.
	mergeUserConfig(config, loadUserConfig())

	// Expose the active config for sub-packages (Discord chat, canvas, ...).
	appConfig = config

	SetAccentColor(resolveColor(config, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255}))

	// Reapply the persisted theme preset (if any) so the chosen theme is
	// visible immediately on launch, not just after re-selecting it.
	if presetName, ok := config.Variables.Custom["theme_preset"].(string); ok && presetName != "" {
		ApplyThemeColors(GetThemePreset(presetName))
	}

	// Defaults for optional settings
	if config.Variables.WeatherUnit == "" {
		config.Variables.WeatherUnit = "C"
	}
	// Default reduced-motion and low-power on handheld targets
	// (e.g. Trimui Smart Pro) for a safer out-of-box experience. Windows
	// development builds keep full animation.
	if !IsWindows() && !config.Variables.ReducedMotion && !config.Variables.LowPower {
		config.Variables.ReducedMotion = true
		config.Variables.LowPower = true
	}

	// Load persisted Recently Played and start the (best-effort) weather fetch.
	loadRecentlyPlayed()
	loadFavorites()
	startWeather(config)

	// Auto-download yt-dlp / ffplay into tools_path if missing (OS-aware).
	// Non-fatal: if offline, the app falls back to system PATH.
	ensureRequiredTools(config)

	// Start disk space pie chart auto-refresh (every 5 seconds).
	startDiskPieAutoRefresh()

	// Initialize scene index early so logging and window setup can safely
	// reference the first scene.
	currentSceneIndex = 0

	// Optional resolution / fullscreen configuration (great for TrimuiSmartPro)
	screenW, screenH := 1280, 720
	if config.Variables.ScreenWidth > 0 {
		screenW = config.Variables.ScreenWidth
	}
	if config.Variables.ScreenHeight > 0 {
		screenH = config.Variables.ScreenHeight
	}
	// Fullscreen is intended for the handheld device. On Windows we
	// always stay windowed so the app is usable during development.
	fullscreen := config.Variables.Fullscreen && P().FullscreenDefault()

	windowFlags := sdl.WINDOW_SHOWN
	if fullscreen {
		windowFlags |= sdl.WINDOW_FULLSCREEN_DESKTOP
	}
	// Keep the window resizable on Windows so the logical 1280x720 canvas can
	// be letterboxed to arbitrary window sizes for development/testing.
	if P().ResizableDefault() {
		windowFlags |= sdl.WINDOW_RESIZABLE
	}
	LogSceneOp(config.Scenes[currentSceneIndex].Name, "init").Info("creating window",
		"platform", P().Name(),
		"width", screenW,
		"height", screenH,
		"fullscreen", fullscreen,
		"resizable", P().ResizableDefault(),
	)
	screenWidth, screenHeight = int32(screenW), int32(screenH)
	window, err := sdl.CreateWindow(config.Title, sdl.WINDOWPOS_CENTERED, sdl.WINDOWPOS_CENTERED, screenWidth, screenHeight, uint32(windowFlags))
	mainWindow = window
	if err != nil {
		log.Fatal("Window creation error:", err)
	}
	defer window.Destroy()

	renderer, err := sdl.CreateRenderer(window, -1, sdl.RENDERER_ACCELERATED)
	if err != nil {
		// Fall back to a software renderer when the accelerated backend is
		// unavailable (e.g. dummy/offscreen drivers used in CI/headless).
		renderer, err = sdl.CreateRenderer(window, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			log.Fatal("Renderer creation error:", err)
		}
	}
	defer renderer.Destroy()

	// Render into a fixed logical 1280x720 coordinate system and let SDL
	// letterbox to the physical window size. This preserves the Trimui layout
	// on Windows when the window is resized.
	renderer.SetLogicalSize(screenWidth, screenHeight)

	// Enable alpha blending for draw operations (FillRect, etc.) so
	// semi-transparent overlays/panels composite correctly over the
	// background image instead of painting opaque.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// Show loading screen while initializing
	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Loading..."
	renderer.SetDrawColor(15, 17, 21, 255)
	renderer.Clear()
	renderSpinner(renderer, screenWidth/2, screenHeight/2, 50, sdl.Color{R: 100, G: 180, B: 255, A: 255})
	font, _ := getCachedFont(config, "big")
	if font != nil {
		titleW, titleH, _ := font.SizeUTF8("JukaHub")
		tw32, th32 := int32(titleW), int32(titleH)
		renderText(renderer, config, font, "JukaHub", sdl.Color{R: 220, G: 235, B: 255, A: 255}, screenWidth/2-tw32/2, screenHeight/2-80-th32/2)

		loadW, loadH, _ := font.SizeUTF8("Loading...")
		lw32, lh32 := int32(loadW), int32(loadH)
		renderText(renderer, config, font, "Loading...", sdl.Color{R: 180, G: 200, B: 220, A: 255}, screenWidth/2-lw32/2, screenHeight/2+60-lh32/2)
	}
	renderer.Present()
	sdl.Delay(500)
	config.Variables.LoadingSpinner = false

	// Create placeholder texture for missing thumbnails
	placeholderSurface, _ := sdl.CreateRGBSurfaceWithFormat(0, 120, 90, 32, sdl.PIXELFORMAT_RGBA8888)
	if placeholderSurface != nil {
		placeholderSurface.FillRect(&sdl.Rect{X: 0, Y: 0, W: 120, H: 90}, sdl.MapRGBA(placeholderSurface.Format, 30, 35, 50, 255))
		placeholderTexture, _ = renderer.CreateTextureFromSurface(placeholderSurface)
		placeholderSurface.Free()
	}

	sdl.GameControllerAddMapping("030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,")

	if sdl.NumJoysticks() > 0 {
		if c := sdl.GameControllerOpen(0); c != nil {
			defer c.Close()
		}
	}

	initKeyboard()

	thumbnailDownloadCh = make(chan string, 256)
	go func() {
		for url := range thumbnailDownloadCh {
			if url == "" {
				continue
			}
			thumbnailCacheMutex.Lock()
			if _, ok := textureCache[url]; ok {
				thumbnailCacheMutex.Unlock()
				continue
			}
			if _, ok := thumbnailDataCache[url]; ok {
				thumbnailCacheMutex.Unlock()
				continue
			}
			thumbnailCacheMutex.Unlock()

			resp, err := httpClient.Get(url)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}
			thumbnailCacheMutex.Lock()
			thumbnailDataCache[url] = data
			thumbnailCacheMutex.Unlock()
		}
	}()

	currentSceneIndex = 0
	selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
	focusEngine.SetGraph(config.Scenes[currentSceneIndex].Name, focusEngine.BuildGraph(config.Scenes[currentSceneIndex]))
	focusEngine.SyncSelected()

	running := true
	for running {
		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			switch e := event.(type) {
			case *sdl.QuitEvent:
				running = false
			case *sdl.WindowEvent:
				if e.Event == sdl.WINDOWEVENT_SIZE_CHANGED {
					w, h := mainWindow.GetSize()
					updateWindowScale(w, h)
				}
			case *sdl.KeyboardEvent:
				if e.Type == sdl.KEYDOWN {
					focusEngine.SyncSelected()
					if imageViewerPath != "" {
						handleImageViewerInput(e)
						continue
					}
					if virtualKeyboardActive {
						handleVirtualKeyboardInput(e, config)
					} else if activeSceneIndex != -1 {
						switch e.Keysym.Sym {
						case sdl.K_BACKSPACE:
							if len(inputTextBuffer) > 0 {
								inputTextBuffer = inputTextBuffer[:len(inputTextBuffer)-1]
							}
							updateInputVariable(config)
						case sdl.K_RETURN:
							activeSceneIndex = -1
							activeElementIndex = -1
						}
					} else {
						// Home scene uses the dedicated focus graph + activation.
						if homeLayoutActive && handleHomeKey(e, config) {
							continue
						}
						// Video control shortcuts (always active when video is playing)
						HandleVideoKeyInput(e)
						// Handle navigation
						curScene := config.Scenes[currentSceneIndex]
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							selectedButtonIndex = findFirstSelectableElement(curScene)
						}
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							continue
						}
						curElem := curScene.Elements[selectedButtonIndex]
						if curElem.Type == "searchresults" {
							// Grid navigation via the semantic action handler.
							switch e.Keysym.Sym {
							case sdl.K_UP:
								handleSearchResultsAction(ActionNavigateUp, config)
							case sdl.K_DOWN:
								handleSearchResultsAction(ActionNavigateDown, config)
							case sdl.K_LEFT:
								handleSearchResultsAction(ActionNavigateLeft, config)
							case sdl.K_RIGHT:
								handleSearchResultsAction(ActionNavigateRight, config)
							case sdl.K_RETURN, sdl.K_SPACE:
								handleSearchResultsAction(ActionConfirm, config)
							case sdl.K_ESCAPE:
								handleSearchResultsAction(ActionBack, config)
							}
						} else if curElem.Type == "dynamiclist" {
							entries, _ := getSceneFileEntries(config, curScene)
							n := 0
							if entries != nil {
								n = len(entries)
							}
							switch e.Keysym.Sym {
							case sdl.K_UP:
								if focusedFileIndex > 0 {
									focusedFileIndex--
								}
							case sdl.K_DOWN:
								if focusedFileIndex < n-1 {
									focusedFileIndex++
								}
							case sdl.K_RETURN, sdl.K_SPACE:
								feEnterFocused(config)
							case sdl.K_ESCAPE:
								// File Explorer: B/Escape climbs the directory tree and
								// only leaves the scene at the root.
								if curScene.Name == "FileExplorer" {
									feBack(config)
								} else {
									moveSelection(config, -1)
								}
							}
						} else if curElem.Type == "themegallery" {
							presets := ListThemePresets()
							switch e.Keysym.Sym {
							case sdl.K_LEFT:
								if themeGalleryIndex > 0 {
									themeGalleryIndex--
								}
							case sdl.K_RIGHT:
								if themeGalleryIndex < len(presets)-1 {
									themeGalleryIndex++
								}
							case sdl.K_RETURN, sdl.K_SPACE:
								if themeGalleryIndex >= 0 && themeGalleryIndex < len(presets) {
									ApplyThemePreset(config, presets[themeGalleryIndex])
								}
							case sdl.K_ESCAPE:
								moveSelection(config, -1)
							}
						} else if curElem.Type == "toggle" {
							switch e.Keysym.Sym {
							case sdl.K_RETURN, sdl.K_SPACE, sdl.K_LEFT, sdl.K_RIGHT:
								toggleVariable(config, curElem)
							case sdl.K_ESCAPE:
								moveSelection(config, -1)
							}
						} else if curElem.Type == "favorites" {
							handleFavoritesInput(e, config)
						} else if curElem.Type == "unitconverter" {
							handleUnitInput(e, config)
						} else if curElem.Type == "chat" {
							handleChatInput(e, config)
						} else if curElem.Type == "shortslist" {
							handleShortsInput(e, config)
						} else if curElem.Type == "canvas" {
							handleCanvasInput(e, config)
						} else if curElem.Type == "textbrowser" {
							switch e.Keysym.Sym {
							case sdl.K_UP:
								textBrowserScrollVelocity -= textBrowserScrollAccel
								if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
									textBrowserScrollVelocity = -textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.K_DOWN:
								textBrowserScrollVelocity += textBrowserScrollAccel
								if textBrowserScrollVelocity > textBrowserScrollMaxVel {
									textBrowserScrollVelocity = textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.K_PAGEUP:
								textBrowserScrollY -= 10
								if textBrowserScrollY < 0 {
									textBrowserScrollY = 0
								}
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.K_PAGEDOWN:
								textBrowserScrollY += 10
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.K_HOME:
								textBrowserScrollY = 0
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.K_END:
								textBrowserScrollY = 1 << 30
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							}
						} else if curElem.Type == "packagelist" {
							handlePackageInput(e, config)
						} else if curElem.Type == "recent" {
							handleRecentInput(e, config)
						} else if curScene.Name == "JukaLand" {
							handleJukaLandInput(e)
						} else {
							// Normal button/input navigation
							switch e.Keysym.Sym {
							case sdl.K_UP, sdl.K_DOWN:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, 0, map[sdl.Keycode]int{
										sdl.K_UP: -1, sdl.K_DOWN: 1,
									}[e.Keysym.Sym])
								} else {
									moveSelection(config, map[sdl.Keycode]int{
										sdl.K_UP: -1, sdl.K_DOWN: 1,
									}[e.Keysym.Sym])
								}
							case sdl.K_LEFT, sdl.K_RIGHT:
								if homeLayoutActive && curElem.Type == "button" && curElem.Style == "tile" {
									moveHomeSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym], 0)
								} else {
									moveSelection(config, map[sdl.Keycode]int{
										sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
									}[e.Keysym.Sym])
								}
							case sdl.K_RETURN, sdl.K_SPACE:
								if selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
									elem := config.Scenes[currentSceneIndex].Elements[selectedButtonIndex]
									if elem.Type == "input" {
										handleInputSelection(renderer, config, currentSceneIndex, selectedButtonIndex)
									} else if elem.Type == "button" {
										pressedButtonIndex = selectedButtonIndex
										pressStartTime = sdl.GetTicks64()
										handleTrigger(renderer, config, elem)
									}
								}
							case sdl.K_ESCAPE:
								// Generic back on non-home scenes: returns to the
								// previous scene (or Home). Never exits the app.
								if !homeLayoutActive {
									goBackScene(config)
								}
							}
						}
						focusEngine.SyncSelected()
						// Scene switching keys
						switch e.Keysym.Sym {
						case sdl.K_q:
							changeScene(config, -1)
						case sdl.K_e:
							changeScene(config, 1)
						}
						// Global search chord (Ctrl+K) jumps to the Tube/search scene.
						if e.Keysym.Sym == sdl.K_k && (e.Keysym.Mod&sdl.KMOD_CTRL) != 0 {
							if idx := findSceneIndex(config, "Tube"); idx >= 0 {
								changeSceneTo(config, idx)
							}
						}
						// Fullscreen toggle (F11 / Alt+Enter).
						if e.Keysym.Sym == sdl.K_F11 || (e.Keysym.Sym == sdl.K_RETURN && (e.Keysym.Mod&sdl.KMOD_ALT) != 0) {
							config.Variables.Fullscreen = !config.Variables.Fullscreen
							if mainWindow != nil {
								if config.Variables.Fullscreen {
									mainWindow.SetFullscreen(sdl.WINDOW_FULLSCREEN_DESKTOP)
								} else {
									mainWindow.SetFullscreen(0)
								}
							}
						}
					}
					focusEngine.SyncSelected()
				}
			case *sdl.TextInputEvent:
				if activeSceneIndex != -1 && activeElementIndex != -1 {
					curElem := config.Scenes[activeSceneIndex].Elements[activeElementIndex]
					if curElem.Type == "chat" {
						chatInputText += string(e.Text[:])
					} else {
						inputTextBuffer += string(e.Text[:])
						updateInputVariable(config)
					}
				} else if currentSceneIndex >= 0 && selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
					// Chat is not a modal input: route typed text straight to the
					// composer whenever the chat element is the focused element.
					if config.Scenes[currentSceneIndex].Elements[selectedButtonIndex].Type == "chat" {
						chatInputText += string(e.Text[:])
					}
				}
			case *sdl.MouseMotionEvent:
				mouseX, mouseY = physicalToLogical(int32(e.X), int32(e.Y))
				// Hovering a home card focuses it so mouse users get the same
				// highlight as D-pad navigation. A deliberate click remembers
				// the card for restore-on-return (see handleHomeMouseClick).
				if homeLayoutActive && homeLayout != nil {
					homeLayout.FocusAt(mouseX, mouseY)
				}
				if isDraggingSeekBar {
					fraction := float64(mouseX-seekBarDragRect.X) / float64(seekBarDragRect.W)
					if fraction < 0 {
						fraction = 0
					}
					if fraction > 1 {
						fraction = 1
					}
					SeekVideo(fraction)
				}
			case *sdl.MouseWheelEvent:
				if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
					for _, elem := range config.Scenes[currentSceneIndex].Elements {
						if elem.Type == "textbrowser" {
							elemW := getElementWidth(elem, 1100)
							elemH := getElementHeight(elem, 480)
							if mouseX >= elem.X && mouseX <= elem.X+elemW && mouseY >= elem.Y && mouseY <= elem.Y+elemH {
								textBrowserScrollVelocity -= float64(e.Y) * 2.5
								if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
									textBrowserScrollVelocity = -textBrowserScrollMaxVel
								} else if textBrowserScrollVelocity > textBrowserScrollMaxVel {
									textBrowserScrollVelocity = textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 10
								break
							}
						}
						if elem.Type == "packagelist" {
							elemW := getElementWidth(elem, 1160)
							elemH := getElementHeight(elem, 500)
							if mouseX >= elem.X && mouseX <= elem.X+elemW && mouseY >= elem.Y && mouseY <= elem.Y+elemH {
								packagesMutex.Lock()
								n := len(packagesList)
								packagesMutex.Unlock()
								step := 3
								if e.Y < 0 {
									packagesFocusIndex -= step
								} else {
									packagesFocusIndex += step
								}
								if packagesFocusIndex < 0 {
									packagesFocusIndex = 0
								}
								if packagesFocusIndex >= n {
									packagesFocusIndex = n - 1
								}
								break
							}
						}
					}
				}
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT {
					mx, my := physicalToLogical(int32(e.X), int32(e.Y))
					if e.Type == sdl.MOUSEBUTTONDOWN {
						// Check if clicking on seek bar for dragging
						if pointInRect(mx, my, videoControlRects.progress) {
							isDraggingSeekBar = true
							seekBarDragRect = videoControlRects.progress
							fraction := float64(mx-videoControlRects.progress.X) / float64(videoControlRects.progress.W)
							SeekVideo(fraction)
						}
						// Image viewer overlay: only the close button is interactive.
						if imageViewerPath != "" {
							if mx >= imageViewerCloseBtn.X && mx <= imageViewerCloseBtn.X+imageViewerCloseBtn.W &&
								my >= imageViewerCloseBtn.Y && my <= imageViewerCloseBtn.Y+imageViewerCloseBtn.H {
								closeImageViewer()
							}
							break
						}
						// Home cards: select + activate on click.
						if homeLayoutActive && handleHomeMouseClick(mx, my, config) {
							break
						}
						// Scene-title-row back affordance (mouse-only back for scenes
						// without an explicit Back button; controller/keyboard use B/Esc).
						if sceneTitleBackRect.W > 0 && mx >= sceneTitleBackRect.X && mx <= sceneTitleBackRect.X+sceneTitleBackRect.W &&
							my >= sceneTitleBackRect.Y && my <= sceneTitleBackRect.Y+sceneTitleBackRect.H {
							goBackScene(config)
							break
						}
						// Chat composer: "Ask Juka AI" pill (controller/keyboard use Y).
						if chatAskAIBtnRect.W > 0 && mx >= chatAskAIBtnRect.X && mx <= chatAskAIBtnRect.X+chatAskAIBtnRect.W &&
							my >= chatAskAIBtnRect.Y && my <= chatAskAIBtnRect.Y+chatAskAIBtnRect.H {
							if strings.TrimSpace(chatInputText) != "" {
								sendGroqChatMessage(chatInputText)
								chatInputText = ""
							}
							break
						}
						// Check video controls
						HandleVideoControlClick(mx, my)
						// Check recent panel (Continue cards)
						handleRecentMouseClick(mx, my, config)
						// Check other elements (input, button, searchresults)
						if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
							for i, elem := range config.Scenes[currentSceneIndex].Elements {
								if elem.Type == "input" {
									width := int32(200)
									if string(elem.Width) != "" {
										w, _ := strconv.Atoi(string(elem.Width))
										width = int32(w)
									}
									height := int32(40)
									if string(elem.Height) != "" {
										h, _ := strconv.Atoi(string(elem.Height))
										height = int32(h)
									}
									if mx >= elem.X && mx <= elem.X+width && my >= elem.Y && my <= elem.Y+height {
										handleInputSelection(renderer, config, currentSceneIndex, i)
									}
								} else if elem.Type == "toggle" {
									tw := int32(320)
									if string(elem.Width) != "" {
										if w, err := strconv.Atoi(string(elem.Width)); err == nil {
											tw = int32(w)
										}
									}
									th := int32(48)
									if string(elem.Height) != "" {
										if h, err := strconv.Atoi(string(elem.Height)); err == nil {
											th = int32(h)
										}
									}
									if mx >= elem.X && mx <= elem.X+tw && my >= elem.Y && my <= elem.Y+th {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										toggleVariable(config, elem)
									}
								} else if elem.Type == "chat" {
									// Clicking the chat panel focuses the composer and
									// starts text input so the user can type a message.
									cw := getElementWidth(elem, 1160)
									chh := getElementHeight(elem, 480)
									if mx >= elem.X && mx <= elem.X+cw && my >= elem.Y && my <= elem.Y+chh {
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										if !chatInputActive {
											sdl.StartTextInput()
											chatInputActive = true
										}
									}
								} else if elem.Type == "themegallery" {
									if idx := themeGalleryCardAt(elem, mx, my); idx >= 0 {
										themeGalleryIndex = idx
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										presets := ListThemePresets()
										if idx < len(presets) {
											ApplyThemePreset(config, presets[idx])
										}
									}
								} else if elem.Type == "button" {
									font, _ := getCachedFont(config, elem.Font)
									width, height := buttonHitSize(elem, font)
									bx := elem.X
									by := elem.Y
									bw := width
									bh := height
									if homeLayoutActive && elem.Style == "tile" {
										if rect, ok := homeTileRect(i); ok {
											bx, by, bw, bh = rect.X, rect.Y, rect.W, rect.H
										}
									}
									if mx >= bx && mx <= bx+bw && my >= by && my <= by+bh {
										pressedButtonIndex = i
										pressStartTime = sdl.GetTicks64()
										selectedButtonIndex = i
										focusEngine.SetByElementIndex(i)
										handleTrigger(renderer, config, elem)
									}
								} else if elem.Type == "searchresults" {
									videos, ok := config.Variables.Custom[elem.Variable].([]VideoInfo)
									if !ok {
										continue
									}
									// Grid hit detection (account for smooth scroll offset)
									cols, rows := gridColsRows(elem)
									elemWidth := getElementWidth(elem, 1080)
									elemHeight := getElementHeight(elem, 500)
									cellWidth := (elemWidth - 30) / cols
									cellHeight := (elemHeight - 30) / rows
									for idx := range videos {
										if idx >= int(cols*rows) {
											break
										}
										col := int32(idx) % cols
										row := int32(idx) / cols
										xPos := elem.X + col*(cellWidth+10)
										yPos := elem.Y + row*(cellHeight+10) - scrollY
										if mx >= xPos && mx <= xPos+cellWidth && my >= yPos && my <= yPos+cellHeight {
											selectedButtonIndex = i
											focusEngine.SetByElementIndex(i)
											focusedResultIndex = idx
											if idx < len(videos) {
												playVideoInfo(config, videos[idx])
											}
											break
										}
									}
								} else if elem.Type == "dynamiclist" {
									entries, ok := config.Variables.Custom[elem.Variable].([]FileEntry)
									if ok {
										elemW := getElementWidth(elem, 600)
										elemH := getElementHeight(elem, 500)
										lineH := int32(40)
										maxVisible := int((elemH - 30) / lineH)
										if maxVisible < 1 {
											maxVisible = 1
										}
										start := 0
										if focusedFileIndex >= maxVisible {
											start = focusedFileIndex - maxVisible + 1
										}
										end := start + maxVisible
										if end > len(entries) {
											end = len(entries)
										}
										listY := elem.Y + 30
										for i2 := start; i2 < end; i2++ {
											y := listY + int32(i2-start)*lineH
											if mx >= elem.X && mx <= elem.X+elemW && my >= y-2 && my <= y+lineH-2 {
												selectedButtonIndex = i
												focusEngine.SetByElementIndex(i)
												focusedFileIndex = i2
												feEnterFocused(config)
												break
											}
										}
									}
								} else if elem.Type == "favorites" {
									handleFavoritesMouseClick(mx, my, config)
								} else if elem.Type == "packagelist" {
									handlePackageMouseClick(mx, my, config)
								}
							}
						}
					} else if e.Type == sdl.MOUSEBUTTONUP {
						isDraggingSeekBar = false
					}
				}
				focusEngine.SyncSelected()
			case *sdl.ControllerButtonEvent:
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
					focusEngine.SyncSelected()
					if virtualKeyboardActive {
						switch e.Button {
						case sdl.CONTROLLER_BUTTON_DPAD_UP:
							if keyboardPosY > 0 {
								keyboardPosY--
							}
						case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
							if keyboardPosY < len(keyboard)-1 {
								keyboardPosY++
							}
						case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
							if keyboardPosX > 0 {
								keyboardPosX--
							}
						case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
							if keyboardPosX < len(keyboard[keyboardPosY])-1 {
								keyboardPosX++
							}
						case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B:
							handleKeyboardInput(config)
							if e.Button == sdl.CONTROLLER_BUTTON_B {
								virtualKeyboardActive = false
								activeSceneIndex = -1
							}
						}
					} else if activeSceneIndex != -1 {
						// Input field active - handled by virtualKeyboardActive
					} else {
						// Home scene uses the dedicated focus graph + activation.
						if homeLayoutActive && handleHomeController(e, config) {
							continue
						}
						// Video control shortcuts (always active when video is playing)
						HandleVideoControllerInput(e)
						// Handle navigation
						curScene := config.Scenes[currentSceneIndex]
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							selectedButtonIndex = findFirstSelectableElement(curScene)
						}
						if selectedButtonIndex < 0 || selectedButtonIndex >= len(curScene.Elements) {
							continue
						}
						curElem := curScene.Elements[selectedButtonIndex]
						if curElem.Type == "searchresults" {
							// Grid navigation with controller via the semantic action handler.
							if focusedResultIndex < 0 {
								focusedResultIndex = 0
							}
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								handleSearchResultsAction(ActionNavigateUp, config)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								handleSearchResultsAction(ActionNavigateDown, config)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								handleSearchResultsAction(ActionNavigateLeft, config)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								handleSearchResultsAction(ActionNavigateRight, config)
							case sdl.CONTROLLER_BUTTON_A:
								handleSearchResultsAction(ActionConfirm, config)
							case sdl.CONTROLLER_BUTTON_B:
								handleSearchResultsAction(ActionBack, config)
							}
						} else if curElem.Type == "dynamiclist" {
							entries, _ := getSceneFileEntries(config, curScene)
							n := 0
							if entries != nil {
								n = len(entries)
							}
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if focusedFileIndex > 0 {
									focusedFileIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if focusedFileIndex < n-1 {
									focusedFileIndex++
								}
							case sdl.CONTROLLER_BUTTON_A:
								feEnterFocused(config)
							case sdl.CONTROLLER_BUTTON_B:
								// File Explorer: B climbs the directory tree; at the root
								// it returns to the previous scene.
								if curScene.Name == "FileExplorer" {
									feBack(config)
								} else {
									moveSelection(config, -1)
								}
							}
						} else if curElem.Type == "themegallery" {
							presets := ListThemePresets()
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								if themeGalleryIndex > 0 {
									themeGalleryIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								if themeGalleryIndex < len(presets)-1 {
									themeGalleryIndex++
								}
							case sdl.CONTROLLER_BUTTON_A:
								if themeGalleryIndex >= 0 && themeGalleryIndex < len(presets) {
									ApplyThemePreset(config, presets[themeGalleryIndex])
								}
							case sdl.CONTROLLER_BUTTON_B:
								moveSelection(config, -1)
							}
						} else if curElem.Type == "toggle" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								toggleVariable(config, curElem)
							case sdl.CONTROLLER_BUTTON_B:
								moveSelection(config, -1)
							}
						} else if curElem.Type == "favorites" {
							items := getCurrentFavorites()
							n := len(items)
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								if favoritesFocusIndex > 0 {
									favoritesFocusIndex--
								}
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if favoritesFocusIndex < n-1 {
									favoritesFocusIndex++
								}
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								if favoritesCurrentTab > 0 {
									favoritesCurrentTab--
									favoritesFocusIndex = 0
								}
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								if favoritesCurrentTab < 3 {
									favoritesCurrentTab++
									favoritesFocusIndex = 0
								}
							case sdl.CONTROLLER_BUTTON_A:
								if favoritesFocusIndex >= 0 && favoritesFocusIndex < n {
									items[favoritesFocusIndex].Play(config)
								}
							case sdl.CONTROLLER_BUTTON_B:
								for _, scene := range config.Scenes {
									if scene.Name == "Main" {
										changeSceneTo(config, findSceneIndex(config, "Main"))
										break
									}
								}
							}
						} else if curElem.Type == "textbrowser" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								textBrowserScrollVelocity -= textBrowserScrollAccel
								if textBrowserScrollVelocity < -textBrowserScrollMaxVel {
									textBrowserScrollVelocity = -textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								textBrowserScrollVelocity += textBrowserScrollAccel
								if textBrowserScrollVelocity > textBrowserScrollMaxVel {
									textBrowserScrollVelocity = textBrowserScrollMaxVel
								}
								textBrowserScrollCooldown = 8
							case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
								textBrowserScrollY -= 10
								if textBrowserScrollY < 0 {
									textBrowserScrollY = 0
								}
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
								textBrowserScrollY += 10
								textBrowserScrollVelocity = 0
								textBrowserScrollCooldown = 0
							}
						} else if curElem.Type == "chat" {
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								chatScrollOffset++
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								if chatScrollOffset > 0 {
									chatScrollOffset--
								}
							case sdl.CONTROLLER_BUTTON_A:
								if strings.TrimSpace(chatInputText) != "" {
									sendChatMessage("You", chatInputText)
									chatInputText = ""
								}
							case sdl.CONTROLLER_BUTTON_Y:
								if strings.TrimSpace(chatInputText) != "" {
									sendGroqChatMessage(chatInputText)
									chatInputText = ""
								}
							}
						} else {
							// Normal button/input navigation
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								moveSelection(config, -1)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								moveSelection(config, 1)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								moveSelection(config, -1)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								moveSelection(config, 1)
							case sdl.CONTROLLER_BUTTON_A:
								if selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
									elem := config.Scenes[currentSceneIndex].Elements[selectedButtonIndex]
									if elem.Type == "input" {
										handleInputSelection(renderer, config, currentSceneIndex, selectedButtonIndex)
									} else {
										handleTrigger(renderer, config, elem)
									}
								}
							case sdl.CONTROLLER_BUTTON_B:
								// Generic back on non-home scenes: returns to the
								// previous scene (or Home). Never exits the app.
								if !homeLayoutActive {
									goBackScene(config)
								}
							case sdl.CONTROLLER_BUTTON_X:
								// Search shortcut: jump to the Tube/search scene.
								if idx := findSceneIndex(config, "Tube"); idx >= 0 {
									changeSceneTo(config, idx)
								}
							case sdl.CONTROLLER_BUTTON_BACK:
								// Menu-style shortcut to the Settings scene.
								if idx := findSceneIndex(config, "Settings"); idx >= 0 {
									changeSceneTo(config, idx)
								}
							}
							// Scene switching (L1/R1 or L2/R2)
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
								changeScene(config, -1)
							case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
								changeScene(config, 1)
							}
						}
						focusEngine.SyncSelected()
					}
				}
			}
		}

		drainCustom(config)

		// Apply kinetic scroll for text browser panels
		if textBrowserScrollCooldown > 0 {
			textBrowserScrollCooldown--
		} else if textBrowserScrollVelocity != 0 {
			textBrowserScrollVelocity *= textBrowserScrollFriction
			if textBrowserScrollVelocity > -0.05 && textBrowserScrollVelocity < 0.05 {
				textBrowserScrollVelocity = 0
			}
		}
		if textBrowserScrollVelocity != 0 {
			textBrowserScrollY += int32(textBrowserScrollVelocity)
			// Clamp scroll position based on current scene textbrowser elements
			if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
				for _, elem := range config.Scenes[currentSceneIndex].Elements {
					if elem.Type == "textbrowser" {
						elemH := getElementHeight(elem, 480)
						headerH := int32(28)
						contentH := elemH - headerH - 8
						lineH := int32(22)
						maxLines := int((contentH) / lineH)
						totalLines := 0
						if v, ok := config.Variables.Custom[elem.Variable].(string); ok {
							totalLines = len(strings.Split(v, "\n"))
						}
						scrollMax := int32(0)
						if totalLines > maxLines {
							scrollMax = int32(totalLines) - int32(maxLines)
						}
						if textBrowserScrollY < 0 {
							textBrowserScrollY = 0
						}
						if textBrowserScrollY > scrollMax {
							textBrowserScrollY = scrollMax
						}
						break
					}
				}
			}
		}

		// Update hover state for buttons
		hoveredButtonIndex = -1
		if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
			for i, elem := range config.Scenes[currentSceneIndex].Elements {
				if elem.Type == "button" {
					font, _ := getCachedFont(config, elem.Font)
					width, height := buttonHitSize(elem, font)
					if mouseX >= elem.X && mouseX <= elem.X+width && mouseY >= elem.Y && mouseY <= elem.Y+height {
						hoveredButtonIndex = i
						break
					}
				}
			}
		}

		// Animate hover progress
		targetHover := float64(0)
		if hoveredButtonIndex >= 0 {
			targetHover = 1.0
		}
		if config.Variables.ReducedMotion {
			hoverAnimProgress = targetHover
		} else {
			hoverAnimProgress = lerp(hoverAnimProgress, targetHover, 0.2)
			if hoverAnimProgress > targetHover-0.01 && hoverAnimProgress < targetHover+0.01 {
				hoverAnimProgress = targetHover
			}
		}

		// Clear press feedback after duration (instantly in reduced-motion mode).
		if pressedButtonIndex >= 0 {
			if config.Variables.ReducedMotion || sdl.GetTicks64()-pressStartTime > pressDuration {
				pressedButtonIndex = -1
			}
		}

		renderScene(renderer, config, config.Scenes[currentSceneIndex])

		// Scene transition effects
		if config.Variables.ReducedMotion && (transitionPhase == "fade-out" || transitionPhase == "fade-in") {
			if transitionPhase == "fade-out" {
				completeSceneTransition(config)
			} else {
				transitionPhase = "none"
			}
		}
		if transitionPhase == "fade-out" {
			elapsed := float64(sdl.GetTicks64() - sceneFadeStart)
			duration := float64(250)
			if elapsed >= duration {
				sceneFadeAlpha = 255
				completeSceneTransition(config)
			} else {
				t := easeOutCubic(elapsed / duration)
				sceneFadeAlpha = uint8(t * 255)
			}
			renderer.SetDrawColor(15, 17, 21, sceneFadeAlpha)
			renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
			// accent sweep along the bottom edge
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, sceneFadeAlpha/2)
			renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 4, W: screenWidth, H: 4})
		} else if transitionPhase == "fade-in" {
			elapsed := float64(sdl.GetTicks64() - sceneFadeStart)
			duration := float64(300)
			if elapsed >= duration {
				sceneFadeAlpha = 255
				transitionPhase = "none"
			} else {
				t := easeOutCubic(elapsed / duration)
				sceneFadeAlpha = uint8(t * 255)
			}
			renderer.SetDrawColor(15, 17, 21, 255-sceneFadeAlpha)
			renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
			// accent sweep along the bottom edge
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, (255-sceneFadeAlpha)/2)
			renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 4, W: screenWidth, H: 4})
		}

		// Image viewer overlay
		if imageViewerPath != "" {
			renderImageViewer(renderer, config, imageViewerPath)
		}

		// Loading overlay
		if config.Variables.LoadingSpinner || transitionPhase != "none" {
			// frosted backdrop
			fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, sdl.Color{R: 6, G: 8, B: 14, A: 200})
			// centered glass card
			cardW := int32(340)
			cardH := int32(220)
			cardX := (screenWidth - cardW) / 2
			cardY := (screenHeight - cardH) / 2
			fillRoundedRect(renderer, cardX+4, cardY+6, cardW, cardH, 20, ShadowFill(80))
			fillRoundedRect(renderer, cardX, cardY, cardW, cardH, 20, sdl.Color{R: 18, G: 24, B: 36, A: 235})
			renderer.SetDrawColor(255, 255, 255, 14)
			renderer.FillRect(&sdl.Rect{X: cardX + 12, Y: cardY + 1, W: cardW - 24, H: 1})
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 80)
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: cardW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + 1, W: 1, H: cardH - 2})
			renderer.DrawRect(&sdl.Rect{X: cardX + 1, Y: cardY + cardH - 2, W: cardW - 2, H: 1})
			renderer.DrawRect(&sdl.Rect{X: cardX + cardW - 2, Y: cardY + 1, W: 1, H: cardH - 2})
			renderSpinner(renderer, cardX+cardW/2, cardY+60, 40, sdl.Color{R: 140, G: 180, B: 255, A: 220})
			font, _ := getCachedFont(config, "medium")
			if font == nil {
				font, _ = getCachedFont(config, "small")
			}
			if font != nil {
				loadingTxt := config.Variables.SpinnerText
				if loadingTxt == "" {
					loadingTxt = "Loading..."
				}
				lw, _, _ := font.SizeUTF8(loadingTxt)
				renderText(renderer, config, font, loadingTxt, sdl.Color{R: 220, G: 230, B: 245, A: 255}, cardX+(cardW-int32(lw))/2, cardY+120)
				// Rotating Juka / JukaLang themed joke
				if len(loadingJokes) > 0 {
					ji := int(sdl.GetTicks64() / 2800 % uint64(len(loadingJokes)))
					joke := loadingJokes[ji]
					smallFont, _ := getCachedFont(config, "small")
					if smallFont == nil {
						smallFont = font
					}
					jw, _, _ := smallFont.SizeUTF8(joke)
					renderText(renderer, config, smallFont, joke, sdl.Color{R: 150, G: 170, B: 200, A: 200}, cardX+(cardW-int32(jw))/2, cardY+155)
				}
			}
		}

		// Keep SDL text input on only while the chat element is the focused
		// element so typing is routed to the chat composer. Chat is focused
		// via the normal selection graph (selectedButtonIndex), not the input
		// modal's activeElementIndex.
		chatFocused := false
		if currentSceneIndex >= 0 && selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
			chatFocused = config.Scenes[currentSceneIndex].Elements[selectedButtonIndex].Type == "chat"
		}
		if chatFocused {
			if !chatInputActive {
				sdl.StartTextInput()
				chatInputActive = true
			}
		} else {
			if chatInputActive {
				sdl.StopTextInput()
				chatInputActive = false
			}
		}

		renderToast(renderer, config)

		renderer.Present()
		animTime = float64(sdl.GetTicks64()) / 1000.0
		// In low-power mode throttle the render loop when no transition or
		// press feedback is active. Interaction still wakes the loop via events.
		if config.Variables.LowPower && transitionPhase == "none" && pressedButtonIndex < 0 && hoverAnimProgress == 0 {
			sdl.Delay(33)
		} else {
			sdl.Delay(16)
		}
	}

	// Persist favorites and resume positions on every graceful quit path
	// (window close, exit button, scene-exit trigger).
	saveFavorites()
	for _, tex := range textureCache {
		tex.Destroy()
	}
	for _, tex := range homeTextCache {
		tex.Destroy()
	}
	for _, font := range fontCache {
		font.Close()
	}
}

func handleVirtualKeyboardInput(e *sdl.KeyboardEvent, config *Config) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		if keyboardPosY > 0 {
			keyboardPosY--
		}
	case sdl.K_DOWN:
		if keyboardPosY < len(keyboard)-1 {
			keyboardPosY++
		}
	case sdl.K_LEFT:
		if keyboardPosX > 0 {
			keyboardPosX--
		}
	case sdl.K_RIGHT:
		if keyboardPosX < len(keyboard[keyboardPosY])-1 {
			keyboardPosX++
		}
	case sdl.K_RETURN:
		handleKeyboardInput(config)
	}
}
