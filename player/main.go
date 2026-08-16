package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
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

	// appConfig is the active runtime config, exposed to sub-packages (chat,
	// canvas, ...) that need access to settings such as the Discord credentials.
	appConfig *Config

	// loadingJokes are Juka / JukaLang themed messages shown rotating on the
	// loading overlay to keep things fun while assets initialize.
	loadingJokes = []string{
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

	currentSceneIndex   int
	selectedButtonIndex int
	menuButtonRects     = make(map[int]sdl.Rect) // scene index â†’ hitbox

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

	// Toast notifications
	toastMessage   string
	toastColor     sdl.Color
	toastStartTime uint64
	toastDuration  uint64  = 3000
	toastSlideY    float64 = 40

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
	chatMessages  []ChatMessage
	chatMutex     sync.Mutex
	chatInputText string

	// Canvas Sandbox state
	canvasCode    string
	canvasSurface *sdl.Surface

	// Video playback state
	videoPlaybackPhase    string // idle | downloading | playing | error
	videoPlaybackProgress float64
	videoPlaybackSpeed    string
	videoPlaybackETA      string
	videoPlaybackError    string
	videoPlaybackCmd      *exec.Cmd
	videoPlaybackMutex    sync.Mutex

	// mainWindow is kept global so runtime settings (e.g. fullscreen toggle)
	// can apply changes without threading the window through every handler.
	mainWindow *sdl.Window

	// Logical screen size (configurable for devices like TrimuiSmartPro)
	screenWidth  int32 = 1280
	screenHeight int32 = 720
)

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
	Title          string               `json:"title"`
	Author         string               `json:"author"`
	Description    string               `json:"description"`
	Version        string               `json:"version"`
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
	Custom             map[string]interface{}
	LoadingSpinner     bool   `json:"-"`
	SpinnerText        string `json:"-"`
}

type SceneConfig struct {
	Name       string    `json:"name"`
	Background string    `json:"background"`
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
	Style         string      `json:"style"`   // e.g. "tile" for OS-style app icons
	Icon          string      `json:"icon"`    // monogram/short glyph for tile style

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
		"buttonColor":     true,
		"labelColor":      true,
		"backgroundImage": true,
		"fonts":           true,
		"fontSizes":       true,
		"fullscreen":      true,
		"screenWidth":     true,
		"screenHeight":    true,
		"tools_path":      true,
		"audioBackend":    true,
		"custom":          true,
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

func (v *Variables) Get(name string) string {
	if val, ok := v.Custom[name]; ok {
		return fmt.Sprintf("%v", val)
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

// --- Visual theme helpers ---------------------------------------------------

var (
	bgTexture    *sdl.Texture
	bgImagePath  string
	overlayColor = sdl.Color{R: 12, G: 14, B: 20, A: 60}

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

type pt struct{ x, y int32 }

func fillTriangle(renderer *sdl.Renderer, a, b, c pt) {
	renderer.DrawLine(a.x, a.y, b.x, b.y)
	renderer.DrawLine(b.x, b.y, c.x, c.y)
	renderer.DrawLine(c.x, c.y, a.x, a.y)
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
	renderer.SetDrawColor(30, 36, 50, 255)
	renderCircleSector(renderer, cx, cy, radius+4, radius, 0, 2*math.Pi)

	// Free sector (background)
	renderer.SetDrawColor(30, 36, 50, 255)
	renderCircleSector(renderer, cx, cy, radius, innerR, 0, 2*math.Pi)

	// Used sector
	usedAngle := 2 * math.Pi * usedFrac
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
	renderCircleSector(renderer, cx, cy, radius, innerR, -math.Pi/2, -math.Pi/2+usedAngle)

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
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
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

// fillRoundedRect draws a filled rounded rectangle.
func fillRoundedRect(renderer *sdl.Renderer, x, y, w, h, r int32, c sdl.Color) {
	if r < 1 {
		renderer.SetDrawColor(c.R, c.G, c.B, c.A)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		return
	}
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	renderer.SetDrawColor(c.R, c.G, c.B, c.A)
	renderer.FillRect(&sdl.Rect{X: x + r, Y: y, W: w - 2*r, H: h})
	renderer.FillRect(&sdl.Rect{X: x, Y: y + r, W: w, H: h - 2*r})
	fillCircle(renderer, x+r, y+r, r, c)
	fillCircle(renderer, x+w-r, y+r, r, c)
	fillCircle(renderer, x+r, y+h-r, r, c)
	fillCircle(renderer, x+w-r, y+h-r, r, c)
}

// gradientRoundedRect draws a 2-stop vertical gradient inside a rounded rect
// by layering a bottom-colored rounded rect with a top-colored upper portion.
func gradientRoundedRect(renderer *sdl.Renderer, x, y, w, h, r int32, top, bottom sdl.Color) {
	fillRoundedRect(renderer, x, y, w, h, r, bottom)
	half := h / 2
	if half > 2 {
		fillRoundedRect(renderer, x, y, w, half+r, r, top)
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
	// soft shadow
	fillRoundedRect(renderer, x+3, y+3, w, h, 10, ShadowFill(60))
	// fill
	fillRoundedRect(renderer, x, y, w, h, 10, fill)
	// subtle inner highlight
	fillRoundedRect(renderer, x+1, y+1, w-2, h/3, 9, GlossFill(10))
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
	fillRoundedRect(renderer, x+3, y+3, w, h, r, ShadowFill(50))
	fillRoundedRect(renderer, x, y, w, h, r, ColorSurfaceCard)
	fillRoundedRect(renderer, x+1, y+1, w-2, h/3, r-1, GlossFill(8))
	renderer.SetDrawColor(ColorBorderSubtle.R, ColorBorderSubtle.G, ColorBorderSubtle.B, ColorBorderSubtle.A)
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: w - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: x + 1, Y: y + 1, W: 1, H: h - 2})
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

func renderButtonElement(renderer *sdl.Renderer, config *Config, elem Element, selected bool, hovered bool, hoverProgress float64, pressed bool) {
	if elem.Style == "tile" {
		renderAppTile(renderer, config, elem, selected, hovered, hoverProgress, pressed)
		return
	}
	font, _ := getCachedFont(config, elem.Font)
	textWidth, textHeight := int32(0), int32(0)
	if font != nil {
		w, h, _ := font.SizeUTF8(elem.Text)
		textWidth, textHeight = int32(w), int32(h)
	}
	width, height := buttonHitSize(elem, font)
	x, y := elem.X, elem.Y
	r := int32(10)

	c := resolveColor(config, elem.BgColor, accentColor)
	if int(c.R)+int(c.G)+int(c.B) < 140 {
		c = accentColor
	}
	top := lighten(c, 40)
	bottom := darken(c, 20)

	pressOffset := int32(0)
	if pressed {
		pressOffset = 2
	}
	ox := x + pressOffset
	oy := y + pressOffset

	// soft shadow
	fillRoundedRect(renderer, ox+2, oy+2, width, height, r, ShadowFill(50))

	if selected && !pressed {
		fillRoundedRect(renderer, ox-3, oy-3, width+6, height+6, r+3,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60})
		top = lighten(c, 50)
		bottom = lighten(c, 15)
	} else if hoverProgress > 0.01 && !pressed {
		alpha := uint8(40 * hoverProgress)
		fillRoundedRect(renderer, ox-2, oy-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: alpha})
		top = lighten(c, 45)
		bottom = lighten(c, 15)
	}

	if pressed {
		top = darken(c, 15)
		bottom = darken(c, 30)
	}

	// border layer (1px) for crisp definition; accent when active
	borderCol := ColorBorderDefault
	if selected || hovered {
		borderCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200}
	}
	fillRoundedRect(renderer, ox-1, oy-1, width+2, height+2, r+1, borderCol)

	// main body with subtle white overlay
	fillRoundedRect(renderer, ox, oy, width, height, r, GlossFill(15))
	gradientRoundedRect(renderer, ox+1, oy+1, width-2, height-2, r-1, top, bottom)

	// selection ring (subtle inset accent corners)
	if selected && !pressed {
		renderer.SetDrawColor(255, 255, 255, 140)
		renderer.DrawRect(&sdl.Rect{X: ox + 2, Y: oy + 2, W: width - 4, H: 1})
		renderer.DrawRect(&sdl.Rect{X: ox + 2, Y: oy + 2, W: 1, H: height - 4})
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 160)
		renderer.DrawRect(&sdl.Rect{X: ox + 3, Y: oy + height - 3, W: width - 6, H: 1})
		renderer.DrawRect(&sdl.Rect{X: ox + width - 3, Y: oy + 3, W: 1, H: height - 6})
	}

	if font != nil {
		lum := int(c.R)*299 + int(c.G)*587 + int(c.B)*114
		var txt sdl.Color
		if lum > 60000 {
			txt = sdl.Color{R: 18, G: 22, B: 30, A: 255}
		} else {
			txt = sdl.Color{R: 245, G: 248, B: 255, A: 255}
		}
		tx := ox + (width-textWidth)/2
		ty := oy + (height-textHeight)/2
		renderText(renderer, config, font, elem.Text, txt, tx, ty)
	}
}

// renderAppTile draws an OS-style launcher icon: a rounded tile with a soft
// shadow, gradient fill, monogram badge, and a label beneath.
func renderAppTile(renderer *sdl.Renderer, config *Config, elem Element, selected, hovered bool, hoverProgress float64, pressed bool) {
	labelFont, _ := getCachedFont(config, elem.Font)
	if labelFont == nil {
		labelFont, _ = getCachedFont(config, "medium")
	}
	width, height := buttonHitSize(elem, labelFont)
	x, y := elem.X, elem.Y
	r := int32(22)

	c := resolveColor(config, elem.BgColor, accentColor)
	if int(c.R)+int(c.G)+int(c.B) < 140 {
		c = accentColor
	}
	top := lighten(c, 38)
	bottom := darken(c, 18)

	pressOffset := int32(0)
	if pressed {
		pressOffset = 3
	}
	ox := x + pressOffset
	oy := y + pressOffset

	// drop shadow
	fillRoundedRect(renderer, ox+3, oy+5, width, height, r, ShadowFill(70))

	// selection / hover glow
	if selected && !pressed {
		fillRoundedRect(renderer, ox-4, oy-4, width+8, height+8, r+4,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 75})
	} else if hoverProgress > 0.01 && !pressed {
		a := uint8(45 * hoverProgress)
		fillRoundedRect(renderer, ox-3, oy-3, width+6, height+6, r+3,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: a})
	}
	if pressed {
		top = darken(c, 12)
		bottom = darken(c, 30)
	}

	// crisp border (accent when active)
	borderCol := ColorBorderDefault
	if selected || hovered {
		borderCol = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
	}
	fillRoundedRect(renderer, ox-1, oy-1, width+2, height+2, r+1, borderCol)

	// tile body
	fillRoundedRect(renderer, ox, oy, width, height, r, GlossFill(14))
	gradientRoundedRect(renderer, ox+1, oy+1, width-2, height-2, r-1, top, bottom)

	// text color with good contrast against the tile
	lum := int(c.R)*299 + int(c.G)*587 + int(c.B)*114
	var txt sdl.Color
	if lum > 60000 {
		txt = sdl.Color{R: 16, G: 20, B: 28, A: 255}
	} else {
		txt = sdl.Color{R: 248, G: 250, B: 255, A: 255}
	}

	// monogram badge (squircle) in the upper portion of the tile
	badge := width / 3
	if badge > 96 {
		badge = 96
	}
	if badge < 44 {
		badge = 44
	}
	bx := ox + (width-badge)/2
	by := oy + int32(float32(height)*0.15)
	fillRoundedRect(renderer, bx, by, badge, badge, badge/2+6, WithAlpha(ColorShadow, 80))
	fillRoundedRect(renderer, bx+3, by+3, badge-6, badge-6, (badge-6)/2, WithAlpha(lighten(c, 72), 70))

	glyph := ""
	if elem.Icon != "" {
		glyph = string([]rune(elem.Icon)[0])
	} else if ru := []rune(elem.Text); len(ru) > 0 {
		glyph = string(ru[0])
	}
	bigFont, _ := getCachedFont(config, "big")
	if bigFont == nil {
		bigFont = labelFont
	}
	if bigFont != nil && glyph != "" {
		gw, gh, _ := bigFont.SizeUTF8(glyph)
		gx := ox + (width-int32(gw))/2
		gy := by + (badge-int32(gh))/2
		renderText(renderer, config, bigFont, glyph, txt, gx, gy)
	}

	// label beneath the badge
	if labelFont != nil && elem.Text != "" {
		lw, lh, _ := labelFont.SizeUTF8(elem.Text)
		lx := ox + (width-int32(lw))/2
		ly := oy + height - int32(lh) - 16
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
	if runtime.GOOS == "windows" {
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
	if runtime.GOOS == "windows" {
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
	// Check for error first
	if errMsg, ok := config.Variables.Custom["search_error"]; ok && errMsg != nil {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, fmt.Sprintf("Error: %v", errMsg), sdl.Color{R: 255, G: 100, B: 100, A: 255}, element.X, element.Y)
			renderText(renderer, config, font, "Check terminal for details.", ColorTextPrimary(), element.X, element.Y+20)
		}
		return
	}

	videos, ok := config.Variables.Custom[element.Variable].([]VideoInfo)
	if !ok || len(videos) == 0 {
		font, _ := getCachedFont(config, element.Font)
		if font != nil {
			if config.Variables.LoadingSpinner {
				renderSpinner(renderer, element.X+50, element.Y+50, 30, sdl.Color{R: 100, G: 180, B: 255, A: 255})
				renderText(renderer, config, font, config.Variables.SpinnerText, sdl.Color{R: 200, G: 220, B: 240, A: 255}, element.X+90, element.Y+40)
			} else {
				renderText(renderer, config, font, "No results. Try searching.", ColorTextPrimary(), element.X, element.Y)
			}
		}
		return
	}

	// Grid parameters (configurable; default 2 columns x 6 rows)
	cols, rows := gridColsRows(element)
	elemWidth := getElementWidth(element, 1080)
	elemHeight := getElementHeight(element, 500)

	cellWidth := (elemWidth - 30) / cols   // horizontal padding 30 total
	cellHeight := (elemHeight - 30) / rows // vertical padding 30 total

	thumbWidth := int32(120)
	thumbHeight := int32(90)

	// Smooth scroll offset for the grid
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

	// panel background
	fillRoundedRect(renderer, element.X-8, element.Y-8, elemWidth+16, elemHeight+16, 14, WithAlpha(ColorSurfacePanel, 230))
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 50)
	renderer.FillRect(&sdl.Rect{X: element.X - 8, Y: element.Y - 8, W: elemWidth + 16, H: 1})

	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		return
	}
	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}

	for i, vid := range videos {
		if i >= int(cols*rows) {
			break
		}
		col := int32(i) % cols
		row := int32(i) / cols
		xPos := element.X + col*(cellWidth+10)
		yPos := element.Y + row*(cellHeight+10) - scrollY

		if yPos+cellHeight < element.Y || yPos > element.Y+elemHeight {
			continue
		}

		// card
		cardX := xPos + 4
		cardY := yPos + 4
		cardW := cellWidth - 8
		cardH := cellHeight - 8
		drawCard(renderer, cardX, cardY, cardW, cardH, 10)

		// selection ring
		if i == focusedResultIndex {
			fillRoundedRect(renderer, cardX-3, cardY-3, cardW+6, cardH+6, 12, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 120})
			fillRoundedRect(renderer, cardX, cardY, cardW, cardH, 10, ColorSurfaceRaised)
			// inner top highlight
			fillRoundedRect(renderer, cardX+1, cardY+1, cardW-2, cardH/3, 8, GlossFill(6))
		}

		// recently played accent bar
		if isRecentlyPlayed(vid.GetURL()) {
			renderer.SetDrawColor(ColorDanger.R, ColorDanger.G, ColorDanger.B, ColorDanger.A)
			renderer.FillRect(&sdl.Rect{X: cardX, Y: cardY + 8, W: 3, H: cardH - 16})
		}

		// Thumbnail
		thumbLoaded := false
		if vid.Thumbnail != "" {
			tex := loadThumbnail(renderer, vid.Thumbnail)
			if tex != nil {
				renderer.Copy(tex, nil, &sdl.Rect{X: cardX + 8, Y: cardY + 8, W: thumbWidth, H: thumbHeight})
				thumbLoaded = true
			}
		}
		if !thumbLoaded && len(vid.Thumbnails) > 0 {
			tex := loadThumbnailFromURLs(renderer, vid.Thumbnails)
			if tex != nil {
				renderer.Copy(tex, nil, &sdl.Rect{X: cardX + 8, Y: cardY + 8, W: thumbWidth, H: thumbHeight})
				thumbLoaded = true
			}
		}
		if !thumbLoaded && placeholderTexture != nil {
			renderer.Copy(placeholderTexture, nil, &sdl.Rect{X: cardX + 8, Y: cardY + 8, W: thumbWidth, H: thumbHeight})
		}

		textStartX := cardX + 8 + thumbWidth + 10
		textY := cardY + 10
		textW := cardW - 8 - thumbWidth - 18

		// Title
		title := vid.Title
		if len(title) > int(textW)/7 {
			title = title[:int(textW)/7-2] + "..."
		}
		renderText(renderer, config, titleFont, title, ColorTextPrimary(), textStartX, textY)

		// Channel + views
		uploader := vid.Uploader
		if uploader == "" {
			uploader = vid.Channel
		}
		if len(uploader) > 20 {
			uploader = uploader[:17] + "..."
		}
		meta := uploader
		if vid.ViewCount > 0 {
			meta += " · " + formatViewCount(vid.ViewCount)
		}
		renderText(renderer, config, font, meta, ColorTextTertiary(), textStartX, textY+24)

		// Duration badge
		dur := fmt.Sprintf("%d:%02d", int(vid.Duration)/60, int(vid.Duration)%60)
		bw, bh := int32(48), int32(18)
		bx := cardX + cardW - bw - 10
		by := cardY + cardH - bh - 10
		fillRoundedRect(renderer, bx, by, bw, bh, 5, sdl.Color{R: 0, G: 0, B: 0, A: 120})
		renderText(renderer, config, font, dur, ColorTextPrimary(), bx+6, by+3)
	}

	// scroll indicator bar
	if maxScroll > 0 {
		barH := int32(40)
		barX := element.X + elemWidth - 8
		barY := element.Y + int32(float64(scrollY)/float64(maxScroll)*float64(elemHeight-barH))
		fillRoundedRect(renderer, barX, barY, 6, barH, 3, sdl.Color{R: 255, G: 255, B: 255, A: 35})
	}
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
func playVideoURL(config *Config, url string) {
	if url == "" {
		log.Printf("playVideoURL: empty URL, nothing to play")
		return
	}

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
		if runtime.GOOS == "windows" {
			log.Printf("[DEBUG] playVideoURL: Windows detected, using temp file mode")
			playWithTempFile(config, ffplayPath, ytDlpPath, url)
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
			log.Printf("[DEBUG] playVideoURL: pipe mode failed, trying temp file fallback")
			playWithTempFile(config, ffplayPath, ytDlpPath, url)
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
	if runtime.GOOS == "windows" {
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

func playWithTempFile(config *Config, ffplayPath, ytDlpPath, url string) {
	log.Printf("[DEBUG] playWithTempFile: downloading to temp file for %s", url)

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "downloading"
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
		videoPlaybackError = "downloaded file is empty"
		videoPlaybackMutex.Unlock()
		os.Remove(tempPath)
		return
	}

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "playing"
	videoPlaybackProgress = 1.0
	videoPlaybackMutex.Unlock()

	args := []string{
		"-loglevel", "debug",
		"-framedrop",
		"-autoexit",
		"-i", tempPath,
	}
	playCmd := exec.Command(ffplayPath, args...)
	playCmd.Env = ffplayEnv(ffplayPath)
	playCmd.Stdout = os.Stdout
	playCmd.Stderr = &stderrBuf
	videoPlaybackMutex.Lock()
	videoPlaybackCmd = playCmd
	videoPlaybackMutex.Unlock()

	log.Printf("[DEBUG] playWithTempFile: launching ffplay with args: %v", args)
	log.Printf("[DEBUG] playWithTempFile: temp file exists: %s, size: %d", tempPath, func() int64 {
		f, _ := os.Stat(tempPath)
		if f != nil {
			return f.Size()
		}
		return 0
	}())

	if err := playCmd.Start(); err != nil {
		log.Printf("[ERROR] playWithTempFile: ffplay.Start failed: %v", err)
		log.Printf("[ERROR] playWithTempFile: stderr so far: %s", stderrBuf.String())
		showToast("Playback failed. Check ffplay in required/.", ToastError())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackError = err.Error()
		videoPlaybackCmd = nil
		videoPlaybackMutex.Unlock()
		os.Remove(tempPath)
		return
	}

	log.Printf("[DEBUG] playWithTempFile: ffplay started, PID=%d", playCmd.Process.Pid)

	// Wait with a timeout to detect immediate exits
	done = make(chan error, 1)
	go func() {
		done <- playCmd.Wait()
	}()

	select {
	case err := <-done:
		log.Printf("[DEBUG] playWithTempFile: ffplay exited immediately with error: %v", err)
		log.Printf("[DEBUG] playWithTempFile: ffplay stderr: %s", stderrBuf.String())
		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "error"
		videoPlaybackError = fmt.Sprintf("ffplay exited: %v", err)
		videoPlaybackCmd = nil
		videoPlaybackMutex.Unlock()
		if isMissingDLL(err) {
			showToast("ffplay failed to start (missing DLL). Ensure a working static ffplay.exe is in the required/ folder.", ToastError())
			log.Printf("[TOOLS] ffplay failed with STATUS_DLL_NOT_FOUND — the required/ffplay.exe is missing a dependency; redownload a static ffmpeg build.")
		} else {
			showToast("Video player exited. Check logs.", ToastError())
		}
		// Auto-clear the error overlay after a few seconds so it doesn't stay
		// stuck over the UI.
		go func() {
			time.Sleep(4 * time.Second)
			videoPlaybackMutex.Lock()
			if videoPlaybackPhase == "error" {
				videoPlaybackPhase = "idle"
				videoPlaybackError = ""
			}
			videoPlaybackMutex.Unlock()
		}()
	case <-time.After(2 * time.Second):
		log.Printf("[DEBUG] playWithTempFile: ffplay is still running after 2s, assuming it's playing")
		// ffplay is running, let it continue in the background
		go func() {
			playCmd.Wait()
			videoPlaybackMutex.Lock()
			videoPlaybackPhase = "idle"
			videoPlaybackProgress = 0
			videoPlaybackCmd = nil
			videoPlaybackMutex.Unlock()
			log.Printf("[DEBUG] playWithTempFile: ffplay finished")
			os.Remove(tempPath)
		}()
		return
	}
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
	if config.Scenes[sceneIdx].Elements[elemIdx].Variable != "" {
		if val, ok := config.Variables.Custom[config.Scenes[sceneIdx].Elements[elemIdx].Variable]; ok {
			inputTextBuffer = fmt.Sprintf("%v", val)
		}
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
	if v, ok := c["buttonColor"].(string); ok {
		applyColorVar(config, "buttonColor", v)
	}
	if v, ok := c["labelColor"].(string); ok {
		applyColorVar(config, "labelColor", v)
	}
	if v, ok := c["inputColor"].(string); ok {
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
		// focus glow
		fillRoundedRect(renderer, element.X-2, element.Y-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 40})
		// inner border
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
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

	text := inputTextBuffer
	if isActive && (uint32(sdl.GetTicks64()/500)%2 == 0) {
		text += "_"
	}
	if font != nil {
		renderText(renderer, config, font, text, textColor, element.X+14, element.Y+12)
	}

	// placeholder
	if !isActive && inputTextBuffer == "" {
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

	for i, elem := range scene.Elements {
		switch elem.Type {
		case "label":
			renderLabelShadowed(renderer, config, elem)
		case "button":
			renderButtonElement(renderer, config, elem, i == selectedButtonIndex, i == hoveredButtonIndex, hoverAnimProgress, i == pressedButtonIndex)
		case "input":
			renderInputField(renderer, config, elem, currentSceneIndex, i)
		case "menu":
			renderMenu(renderer, config, elem)
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
		case "image":
			renderImageElement(renderer, config, elem)
		case "video":
			renderVideoElement(renderer, config, elem)
		default:
			log.Printf("Unknown element type: %s", elem.Type)
		}
	}

	// Always-on top status bar (clock + weather + username)
	renderStatusBar(renderer, config)
	renderBottomErrorBar(renderer, config)
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

func showToast(message string, color sdl.Color) {
	toastMessage = message
	toastColor = color
	toastStartTime = sdl.GetTicks64()
}

func renderToast(renderer *sdl.Renderer, config *Config) {
	if toastMessage == "" {
		return
	}
	elapsed := sdl.GetTicks64() - toastStartTime
	if elapsed > toastDuration {
		toastMessage = ""
		toastSlideY = 40
		return
	}
	alpha := uint8(255)
	slideOffset := 40.0
	if elapsed > uint64(float64(toastDuration)*0.7) {
		fadeStart := uint64(float64(toastDuration) * 0.7)
		fadeDuration := uint64(float64(toastDuration) * 0.3)
		remaining := float64(elapsed - fadeStart)
		fadeTotal := float64(fadeDuration)
		alpha = uint8(255 * (1.0 - remaining/fadeTotal))
		if alpha > 255 {
			alpha = 255
		}
		slideOffset = 40.0 * (remaining / fadeTotal)
	} else if elapsed < 200 {
		slideOffset = 40.0 * (1.0 - float64(elapsed)/200.0)
	}
	toastSlideY = slideOffset

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	tw, th, _ := font.SizeUTF8(toastMessage)
	x := (screenWidth - int32(tw)) / 2
	y := screenHeight - 52 - int32(toastSlideY)
	// sleek toast with glass effect
	fillRoundedRect(renderer, x-24, y-12, int32(tw)+48, int32(th)+24, 14, sdl.Color{R: 0, G: 0, B: 0, A: alpha / 3})
	fillRoundedRect(renderer, x-20, y-8, int32(tw)+40, int32(th)+20, 12, sdl.Color{R: toastColor.R, G: toastColor.G, B: toastColor.B, A: alpha})
	// subtle top highlight
	fillRoundedRect(renderer, x-20, y-8, int32(tw)+40, 1, 12, sdl.Color{R: 255, G: 255, B: 255, A: alpha / 4})
	renderText(renderer, config, font, toastMessage, sdl.Color{R: 255, G: 255, B: 255, A: alpha}, x, y)
}

func renderPlaybackOverlay(renderer *sdl.Renderer, config *Config) {
	videoPlaybackMutex.Lock()
	phase := videoPlaybackPhase
	progress := videoPlaybackProgress
	speed := videoPlaybackSpeed
	eta := videoPlaybackETA
	errMsg := videoPlaybackError
	videoPlaybackMutex.Unlock()

	if phase == "idle" {
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

	fillRoundedRect(renderer, barX-8, barY-8, barW+16, barH+16, 14, sdl.Color{R: 0, G: 0, B: 0, A: 180})
	fillRoundedRect(renderer, barX, barY, barW, barH, 10, sdl.Color{R: 14, G: 18, B: 26, A: 230})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 100)
	renderer.FillRect(&sdl.Rect{X: barX, Y: barY, W: barW, H: 1})

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

func renderMenu(renderer *sdl.Renderer, config *Config, element Element) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	// sleek frosted glass bar
	fillRoundedRect(renderer, 0, element.Y+4, screenWidth, 42, 0, sdl.Color{R: 10, G: 14, B: 22, A: 240})
	// subtle top highlight
	renderer.SetDrawColor(255, 255, 255, 15)
	renderer.FillRect(&sdl.Rect{X: 0, Y: element.Y + 4, W: screenWidth, H: 1})
	// bottom border
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 60)
	renderer.FillRect(&sdl.Rect{X: 0, Y: element.Y + 45, W: screenWidth, H: 1})

	buttonX := int32(20)
	menuButtonRects = make(map[int]sdl.Rect)

	for i, scene := range config.Scenes {
		label := scene.Name
		active := i == currentSceneIndex
		color := ColorTextTertiary()
		if active {
			color = ColorTextPrimary()
		}
		textWidth, _ := renderText(renderer, config, font, label, color, buttonX, element.Y+16)
		if active {
			// active pill background
			pillW := textWidth + 16
			pillX := buttonX - 8
			fillRoundedRect(renderer, pillX, element.Y+8, pillW, 26, 13, GlossFill(12))
			// accent underline
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: pillX + 6, Y: element.Y + 36, W: pillW - 12, H: 2})
		}
		menuButtonRects[i] = sdl.Rect{X: buttonX - 8, Y: element.Y + 8, W: textWidth + 16, H: 26}
		buttonX += textWidth + 24
	}
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
	renderImageElement(renderer, config, elem)
}

// --- Trigger handling ---
func handleTrigger(renderer *sdl.Renderer, config *Config, element Element) {
	if element.Trigger == "" {
		return
	}
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
			playVideoURL(config, videos[focusedResultIndex].GetURL())
		} else {
			log.Printf("youtube_play: no focused video to play")
		}
	case "play_video_from_var":
		url, ok := config.Variables.Custom[element.TriggerTarget].(string)
		if !ok {
			if videos, ok := config.Variables.Custom[element.TriggerTarget].([]VideoInfo); ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
				url = videos[focusedResultIndex].GetURL()
			} else {
				log.Printf("Cannot play: %s not found", element.TriggerTarget)
				return
			}
		}
		playVideoURL(config, url)
	case "play_focused":
		videos, ok := getSceneVideos(config, config.Scenes[currentSceneIndex])
		if ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
			playVideoURL(config, videos[focusedResultIndex].GetURL())
		} else {
			log.Printf("play_focused: no focused video to play")
		}
	case "exit":
		os.Exit(0)
	case "external_app":
		if element.ExternalAppPath != "" {
			go func() {
				var cmd *exec.Cmd
				if runtime.GOOS == "windows" {
					cmd = exec.Command("cmd", "/c", element.ExternalAppPath)
				} else {
					cmd = exec.Command("sh", "-c", element.ExternalAppPath)
				}
				if err := cmd.Run(); err != nil {
					log.Printf("External app error: %v", err)
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
			playVideoURL(config, videos[currentShortIdx].GetURL())
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
		saveConfig(config)
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
		packagesList := []string{
			"JukaLang/Juka - Core language runtime",
			"JukaLang/JukaHub - Handheld media hub",
			"JukaLang/Packages - Package registry",
			"JukaLang/Plugins - Plugin system",
			"JukaLang/Tools - Dev tooling",
		}
		var out []string
		for _, p := range packagesList {
			out = append(out, "- "+p)
		}
		config.Variables.Custom["packages_text"] = strings.Join(out, "\n")
	case "packages_github":
		go func(url string) {
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.Command("cmd", "/c", "start", "", url)
			} else {
				cmd = exec.Command("xdg-open", url)
			}
			if err := cmd.Run(); err != nil {
				log.Printf("open url error: %v", err)
			}
		}("https://github.com/jukaLang/Packages")
	case "textbrowser_refresh":
		textBrowserRefresh(config, element)
	case "textbrowser_system":
		config.Variables.Custom[element.Variable] = browseSystemInfo(element)
	case "textbrowser_zeroconf":
		go func(v string) {
			publishCustom(v, browseZeroconfServicesWithTimeout(4*time.Second))
		}(element.Variable)
	case "textbrowser_json":
		go func(v string, el Element) {
			publishCustom(v, browseJSONContent(el))
		}(element.Variable, element)
	case "textbrowser_processes":
		config.Variables.Custom[element.Variable] = getProcessTree()
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
	case "textbrowser_temps":
		config.Variables.Custom[element.Variable] = getSystemTemperature()
	case "textbrowser_services":
		config.Variables.Custom[element.Variable] = getRunningServices()
	case "textbrowser_files":
		config.Variables.Custom[element.Variable] = scanLocalFiles("/media")
	default:
		if strings.HasPrefix(element.Trigger, "http://") || strings.HasPrefix(element.Trigger, "https://") {
			go func(url string) {
				var cmd *exec.Cmd
				if runtime.GOOS == "windows" {
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
		if e.Type == "button" || e.Type == "input" || e.Type == "searchresults" || e.Type == "dynamiclist" || e.Type == "favorites" || e.Type == "textbrowser" {
			return i
		}
	}
	return -1
}

// changeSceneTo changes to a specific scene index with fade transition.
func changeSceneTo(config *Config, targetIdx int) {
	if transitionPhase != "none" {
		return
	}
	if targetIdx < 0 || targetIdx >= len(config.Scenes) || targetIdx == currentSceneIndex {
		return
	}
	pendingSceneIndex = targetIdx
	transitionPhase = "fade-out"
	sceneFadeStart = sdl.GetTicks64()
	sceneFadeAlpha = 0
}

// completeSceneTransition finishes the scene switch after fade-out and starts fade-in.
func completeSceneTransition(config *Config) {
	currentSceneIndex = pendingSceneIndex
	selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
	focusedResultIndex = -1
	focusedFileIndex = 0
	scrollY = 0
	hoveredButtonIndex = -1
	pressedButtonIndex = -1

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

	transitionPhase = "fade-in"
	sceneFadeStart = sdl.GetTicks64()
	sceneFadeAlpha = 0
}

// Modified changeScene to auto-fetch trending and initialize search_query
func changeScene(config *Config, direction int) {
	changeSceneTo(config, currentSceneIndex+direction)
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

func moveSelection(config *Config, direction int) {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	// This is only for buttons/inputs when no grid is focused
	currentScene := config.Scenes[currentSceneIndex]
	var interactive []int
	for i, el := range currentScene.Elements {
		if el.Type == "button" || el.Type == "input" || el.Type == "searchresults" || el.Type == "dynamiclist" {
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

// --- Main ---
func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic: %v\n%s", r, debug.Stack())
		}
	}()

	initLogging()

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_JOYSTICK | sdl.INIT_GAMECONTROLLER); err != nil {
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

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		log.Fatal("Failed to load config:", err)
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

	// Load persisted Recently Played and start the (best-effort) weather fetch.
	loadRecentlyPlayed()
	loadFavorites()
	startWeather(config)

	// Auto-download yt-dlp / ffplay into tools_path if missing (OS-aware).
	// Non-fatal: if offline, the app falls back to system PATH.
	ensureRequiredTools(config)

	// Start disk space pie chart auto-refresh (every 5 seconds).
	startDiskPieAutoRefresh()

	// Optional resolution / fullscreen configuration (great for TrimuiSmartPro)
	screenW, screenH := 1280, 720
	if config.Variables.ScreenWidth > 0 {
		screenW = config.Variables.ScreenWidth
	}
	if config.Variables.ScreenHeight > 0 {
		screenH = config.Variables.ScreenHeight
	}
	// Fullscreen is intended for the handheld device (Linux). On Windows we
	// always stay windowed so the app is usable during development.
	fullscreen := config.Variables.Fullscreen && runtime.GOOS != "windows"

	windowFlags := sdl.WINDOW_SHOWN
	if fullscreen {
		windowFlags |= sdl.WINDOW_FULLSCREEN_DESKTOP
	}
	screenWidth, screenHeight = int32(screenW), int32(screenH)
	window, err := sdl.CreateWindow(config.Title, sdl.WINDOWPOS_CENTERED, sdl.WINDOWPOS_CENTERED, screenWidth, screenHeight, uint32(windowFlags))
	mainWindow = window
	if err != nil {
		log.Fatal("Window creation error:", err)
	}
	defer window.Destroy()

	renderer, err := sdl.CreateRenderer(window, -1, sdl.RENDERER_ACCELERATED)
	if err != nil {
		log.Fatal("Renderer creation error:", err)
	}
	defer renderer.Destroy()

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
		titleW, titleH := font.SizeUTF8("JukaHub")
		renderText(renderer, config, font, "JukaHub", sdl.Color{R: 220, G: 235, B: 255, A: 255}, screenWidth/2-titleW/2, screenHeight/2-80-titleH/2)

		loadW, loadH := font.SizeUTF8("Loading...")
		renderText(renderer, config, font, "Loading...", sdl.Color{R: 180, G: 200, B: 220, A: 255}, screenWidth/2-loadW/2, screenHeight/2+60-loadH/2)
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

	running := true
	for running {
		for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
			switch e := event.(type) {
			case *sdl.QuitEvent:
				running = false
			case *sdl.KeyboardEvent:
				if e.Type == sdl.KEYDOWN {
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
							// Grid navigation
							switch e.Keysym.Sym {
							case sdl.K_UP:
								moveGridSelection(config, 0, -1)
							case sdl.K_DOWN:
								moveGridSelection(config, 0, 1)
							case sdl.K_LEFT:
								moveGridSelection(config, -1, 0)
							case sdl.K_RIGHT:
								moveGridSelection(config, 1, 0)
							case sdl.K_RETURN, sdl.K_SPACE:
								// Play focused video
								if focusedResultIndex < 0 {
									focusedResultIndex = 0
								}
								if videos, ok := getSceneVideos(config, curScene); ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
									playVideoURL(config, videos[focusedResultIndex].GetURL())
								}
							case sdl.K_ESCAPE:
								// Leave the grid and move back to the previous element
								moveSelection(config, -1)
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
						} else if curScene.Name == "JukaLand" {
							handleJukaLandInput(e)
						} else {
							// Normal button/input navigation
							switch e.Keysym.Sym {
							case sdl.K_UP, sdl.K_DOWN:
								moveSelection(config, map[sdl.Keycode]int{
									sdl.K_UP: -1, sdl.K_DOWN: 1,
								}[e.Keysym.Sym])
							case sdl.K_LEFT, sdl.K_RIGHT:
								moveSelection(config, map[sdl.Keycode]int{
									sdl.K_LEFT: -1, sdl.K_RIGHT: 1,
								}[e.Keysym.Sym])
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
							}
						}
						// Scene switching keys
						switch e.Keysym.Sym {
						case sdl.K_q:
							changeScene(config, -1)
						case sdl.K_e:
							changeScene(config, 1)
						}
					}
				}
			case *sdl.TextInputEvent:
				if activeSceneIndex != -1 && activeElementIndex != -1 {
					inputTextBuffer += string(e.Text[:])
					updateInputVariable(config)
				}
			case *sdl.MouseMotionEvent:
				mouseX, mouseY = int32(e.X), int32(e.Y)
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
					mx, my := int32(e.X), int32(e.Y)
					// Image viewer overlay: only the close button is interactive.
					if imageViewerPath != "" {
						if mx >= imageViewerCloseBtn.X && mx <= imageViewerCloseBtn.X+imageViewerCloseBtn.W &&
							my >= imageViewerCloseBtn.Y && my <= imageViewerCloseBtn.Y+imageViewerCloseBtn.H {
							closeImageViewer()
						}
						break
					}
					// Check menu
					for sceneIdx, rect := range menuButtonRects {
						if mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H {
							changeSceneTo(config, sceneIdx)
							break
						}
					}
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
							} else if elem.Type == "button" {
								font, _ := getCachedFont(config, elem.Font)
								width, height := buttonHitSize(elem, font)
								if mx >= elem.X && mx <= elem.X+width && my >= elem.Y && my <= elem.Y+height {
									pressedButtonIndex = i
									pressStartTime = sdl.GetTicks64()
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
										focusedResultIndex = idx
										if idx < len(videos) {
											playVideoURL(config, videos[idx].GetURL())
										}
										break
									}
								}
							} else if elem.Type == "dynamiclist" {
								entries, ok := config.Variables.Custom[elem.Variable].([]FileEntry)
								if ok {
									elemW := getElementWidth(elem, 600)
									elemH := getElementHeight(elem, 500)
									// Must match renderDynamicList: lineH = 40, listY = elem.Y + 30.
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
											focusedFileIndex = i2
											feEnterFocused(config)
											break
										}
									}
								}
							} else if elem.Type == "favorites" {
								handleFavoritesMouseClick(mx, my, config)
							}
						}
					}
				}
			case *sdl.ControllerButtonEvent:
				if e.Type == sdl.CONTROLLERBUTTONDOWN {
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
							// Grid navigation with controller
							if focusedResultIndex < 0 {
								focusedResultIndex = 0
							}
							switch e.Button {
							case sdl.CONTROLLER_BUTTON_DPAD_UP:
								moveGridSelection(config, 0, -1)
							case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
								moveGridSelection(config, 0, 1)
							case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
								moveGridSelection(config, -1, 0)
							case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
								moveGridSelection(config, 1, 0)
							case sdl.CONTROLLER_BUTTON_A:
								if videos, ok := getSceneVideos(config, curScene); ok && focusedResultIndex >= 0 && focusedResultIndex < len(videos) {
									playVideoURL(config, videos[focusedResultIndex].GetURL())
								}
							case sdl.CONTROLLER_BUTTON_B:
								// Leave the grid and move back to the previous element
								moveSelection(config, -1)
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
						} else if curScene.Name == "JukaLand" {
							handleJukaLandController(e)
						} else if selectedButtonIndex >= 0 && selectedButtonIndex < len(config.Scenes[currentSceneIndex].Elements) {
							elem := config.Scenes[currentSceneIndex].Elements[selectedButtonIndex]
							if elem.Type == "input" {
								handleInputSelection(renderer, config, currentSceneIndex, selectedButtonIndex)
							} else {
								handleTrigger(renderer, config, elem)
							}
						}
								}
							}
						}
						// Scene switching
						switch e.Button {
						case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
							changeScene(config, -1)
						case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
							changeScene(config, 1)
						}
					}
				}
			}
		}

		drainCustom(config)

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
		hoverAnimProgress = lerp(hoverAnimProgress, targetHover, 0.2)
		if hoverAnimProgress > targetHover-0.01 && hoverAnimProgress < targetHover+0.01 {
			hoverAnimProgress = targetHover
		}

		// Clear press feedback after duration
		if pressedButtonIndex >= 0 && sdl.GetTicks64()-pressStartTime > pressDuration {
			pressedButtonIndex = -1
		}

		renderScene(renderer, config, config.Scenes[currentSceneIndex])

		// Scene transition effects
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
		}

		// Image viewer overlay
		if imageViewerPath != "" {
			renderImageViewer(renderer, config, imageViewerPath)
		}

		// Loading overlay
		if config.Variables.LoadingSpinner || transitionPhase != "none" {
			fillRoundedRect(renderer, 0, 0, screenWidth, screenHeight, 0, sdl.Color{R: 6, G: 8, B: 14, A: 200})
			renderSpinner(renderer, screenWidth/2, screenHeight/2, 44, sdl.Color{R: 140, G: 180, B: 255, A: 200})
			font, _ := getCachedFont(config, "small")
			if font != nil {
				loadingTxt := config.Variables.SpinnerText
				if loadingTxt == "" {
					loadingTxt = "Loading..."
				}
				renderText(renderer, config, font, loadingTxt, sdl.Color{R: 200, G: 215, B: 235, A: 255}, screenWidth/2-80, screenHeight/2+55)
				// Rotating Juka / JukaLang themed joke
				if len(loadingJokes) > 0 {
					ji := int(sdl.GetTicks64() / 2800 % uint64(len(loadingJokes)))
					joke := loadingJokes[ji]
					jw, _, _ := font.SizeUTF8(joke)
					renderText(renderer, config, font, joke, sdl.Color{R: 150, G: 170, B: 200, A: 220}, screenWidth/2-int32(jw)/2, screenHeight/2+85)
				}
			}
		}

		renderToast(renderer, config)

		renderPlaybackOverlay(renderer, config)

		renderer.Present()
		animTime = float64(sdl.GetTicks64()) / 1000.0
		sdl.Delay(16)
	}

	for _, tex := range textureCache {
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
