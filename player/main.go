package main

import (
	"bytes"
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

	currentSceneIndex   int
	selectedButtonIndex int
	menuButtonRects     = make(map[int]sdl.Rect) // scene index â†’ hitbox

	// Cache for thumbnails
	textureCache = make(map[string]*sdl.Texture)

	// Focus for search result items (grid index)
	focusedResultIndex int = -1

	// Scene transition fade
	sceneFadeStart    uint64
	sceneFadeAlpha    uint8 = 255

	// Mouse hover state
	mouseX, mouseY int32 = -100, -100
	hoveredButtonIndex int = -1

	// mainWindow is kept global so runtime settings (e.g. fullscreen toggle)
	// can apply changes without threading the window through every handler.
	mainWindow *sdl.Window

	// Logical screen size (configurable for devices like TrimuiSmartPro)
	screenWidth  int32 = 1280
	screenHeight int32 = 720
)

// --- Video info struct ---
type VideoInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Uploader   string `json:"uploader"`
	Duration   float64 `json:"duration"`
	Thumbnail  string `json:"thumbnail"`
	WebpageURL string `json:"webpage_url"`
	URL        string `json:"url"`
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
type Config struct {
	Title       string        `json:"title"`
	Author      string        `json:"author"`
	Description string        `json:"description"`
	Variables   Variables     `json:"variables"`
	Scenes      []SceneConfig `json:"scenes"`
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
	ButtonColor        RGB                `json:"buttonColor"`
	LabelColor         RGB                `json:"labelColor"`
	InputColor         RGB                `json:"inputColor"`
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
	Custom             map[string]interface{}
	LoadingSpinner     bool              `json:"-"`
	SpinnerText        string            `json:"-"`
}

type SceneConfig struct {
	Name      string    `json:"name"`
	Background string   `json:"background"`
	Elements  []Element `json:"elements"`
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
	if len(hex) == 7 {
		hex = hex[1:]
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
	fontPath := "Roboto-Black.ttf"
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
		for _, candidate := range []string{"DejaVuSans.ttf", "DejaVuSans-Bold.ttf", "Roboto-Black.ttf", "arial.ttf"} {
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
	accentColor  = sdl.Color{R: 120, G: 130, B: 255, A: 255}
	overlayColor = sdl.Color{R: 12, G: 14, B: 20, A: 60}
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
		// Default to the bundled background so the app always has one.
		path = "background.jpg"
	}
	if path == bgImagePath && (path == "" || bgTexture != nil) {
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
	// shadow
	renderer.SetDrawColor(0, 0, 0, 90)
	renderer.FillRect(&sdl.Rect{X: x + 4, Y: y + 4, W: w, H: h})
	// fill
	renderer.SetDrawColor(fill.R, fill.G, fill.B, fill.A)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	// border
	drawRectOutline(renderer, x, y, w, h, border)
}

// renderButtonElement draws a modern rounded gradient button with a soft
// shadow, accent border and a glow when selected.
func renderButtonElement(renderer *sdl.Renderer, config *Config, elem Element, selected bool, hovered bool) {
	font, _ := getCachedFont(config, elem.Font)
	textWidth, textHeight := int32(0), int32(0)
	if font != nil {
		w, h, _ := font.SizeUTF8(elem.Text)
		textWidth, textHeight = int32(w), int32(h)
	}
	width := textWidth + 44
	height := textHeight + 22
	if string(elem.Width) != "" {
		if w, err := strconv.Atoi(string(elem.Width)); err == nil {
			width = int32(w)
		}
	}
	if string(elem.Height) != "" {
		if h, err := strconv.Atoi(string(elem.Height)); err == nil {
			height = int32(h)
		}
	}
	x, y := elem.X, elem.Y
	r := int32(12)

	// Pick a vivid base color; fall back to the theme accent when the
	// configured button color is too dark to read against the background.
	c := resolveColor(config, elem.BgColor, accentColor)
	if int(c.R)+int(c.G)+int(c.B) < 140 {
		c = accentColor
	}
	top := lighten(c, 40)
	bottom := darken(c, 22)

	// soft drop shadow
	fillRoundedRect(renderer, x+5, y+6, width, height, r, sdl.Color{R: 0, G: 0, B: 0, A: 150})

	if selected {
		fillRoundedRect(renderer, x-4, y-4, width+8, height+8, r+4,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 160})
		top = lighten(c, 60)
		bottom = lighten(c, 8)
	} else if hovered {
		fillRoundedRect(renderer, x-2, y-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 80})
		top = lighten(c, 50)
		bottom = lighten(c, 15)
	}

	// 2px border + inner gradient
	borderC := lighten(c, 85)
	if selected {
		borderC = sdl.Color{R: 255, G: 255, B: 255, A: 255}
	}
	fillRoundedRect(renderer, x, y, width, height, r, borderC)
	gradientRoundedRect(renderer, x+2, y+2, width-4, height-4, r-1, top, bottom)

	if font != nil {
		tx := x + (width-textWidth)/2
		ty := y + (height-textHeight)/2
		renderText(renderer, config, font, elem.Text, sdl.Color{R: 0, G: 0, B: 0, A: 130}, tx+1, ty+2)
		renderText(renderer, config, font, elem.Text, sdl.Color{R: 255, G: 255, B: 255, A: 255}, tx, ty)
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

// --- Tool path resolution ---
func getToolPath(tool string, config *Config) string {
	exeName := tool
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	// Check tools_path variable (config-driven)
	if config.Variables.ToolsPath != "" {
		fullPath := filepath.Join(config.Variables.ToolsPath, exeName)
		if _, err := os.Stat(fullPath); err == nil {
			log.Printf("[DEBUG] Found %s at %s", tool, fullPath)
			return fullPath
		}
		if _, err := os.Stat(config.Variables.ToolsPath); err == nil {
			log.Printf("[DEBUG] Using %s as %s path", config.Variables.ToolsPath, tool)
			return config.Variables.ToolsPath
		}
	}
	// Check ./required/ subfolder
	commonPath := filepath.Join(".", "required", exeName)
	if _, err := os.Stat(commonPath); err == nil {
		log.Printf("[DEBUG] Found %s in ./required/", tool)
		return commonPath
	}
	// Fallback to system PATH
	log.Printf("[DEBUG] Using system PATH for %s", tool)
	return exeName
}

// --- Quote path if it contains spaces ---
func quotePath(path string) string {
	if strings.ContainsAny(path, " ") {
		return `"` + path + `"`
	}
	return path
}

// --- Thumbnail loading with cache ---
func loadThumbnail(renderer *sdl.Renderer, url string) *sdl.Texture {
	if tex, ok := textureCache[url]; ok {
		return tex
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("Failed to download thumbnail: %v", err)
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil {
		return nil
	}
	texture, err := img.LoadTextureRW(renderer, rwops, true)
	if err != nil {
		log.Printf("Failed to load thumbnail texture: %v", err)
		return nil
	}
	textureCache[url] = texture
	return texture
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
	log.Printf("[DEBUG] ===== executeYouTubeSearch called =====")
	log.Printf("[DEBUG] Command template: %s", command)

	config.Variables.LoadingSpinner = true
	config.Variables.SpinnerText = "Loading videos..."
	defer func() { config.Variables.LoadingSpinner = false }()

	// Substitute variables (e.g., $search_query)
	cmdWithVars := substituteVars(command, vars)
	log.Printf("[DEBUG] After variable substitution: %s", cmdWithVars)

	// If the query part is empty (e.g., "ytsearch12:"), replace with "popular"
	re := regexp.MustCompile(`"ytsearch12:\s*"`)
	cmdWithVars = re.ReplaceAllString(cmdWithVars, `"ytsearch12:popular"`)

	// Apply a playback-resolution cap if the user set one in Settings > General.
	if res, ok := vars["PlaybackResolution"].(string); ok && res != "" && res != "best" {
		cmdWithVars += fmt.Sprintf(" -f \"best[height<=%s]\"", res)
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
	log.Printf("[DEBUG] Final command: %s %v", fullToolPath, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[DEBUG] Executing command...")
	err := cmd.Run()
	if err != nil {
		log.Printf("[ERROR] yt-dlp execution error: %v", err)
		log.Printf("[ERROR] stderr: %s", stderr.String())
		publishCustom("search_error", fmt.Sprintf("yt-dlp error: %v\n%s", err, stderr.String()))
		publishCustom(resultVar, []VideoInfo{})
		return
	}
	log.Printf("[DEBUG] Command executed successfully")
	log.Printf("[DEBUG] stdout length: %d bytes", stdout.Len())
	log.Printf("[DEBUG] stderr length: %d bytes", stderr.Len())

	if stderr.Len() > 0 {
		log.Printf("[DEBUG] stderr content: %s", stderr.String())
	}

	if stdout.Len() == 0 {
		log.Printf("[WARN] yt-dlp returned empty stdout")
		publishCustom("search_error", "yt-dlp returned no data. Check your query or network.")
		publishCustom(resultVar, []VideoInfo{})
		return
	}

	output := stdout.String()
	if len(output) > 500 {
		log.Printf("[DEBUG] stdout (first 500 chars): %s", output[:500])
	} else {
		log.Printf("[DEBUG] stdout: %s", output)
	}

	var videos []VideoInfo
	var parseErr error

	// Try parsing as single JSON (--dump-single-json)
	if strings.HasPrefix(strings.TrimSpace(output), "{") {
		var playlist struct {
			Entries []VideoInfo `json:"entries"`
		}
		if err := json.Unmarshal([]byte(output), &playlist); err == nil {
			videos = playlist.Entries
			log.Printf("[DEBUG] Parsed %d videos from single JSON", len(videos))
		} else {
			parseErr = err
			log.Printf("[ERROR] Failed to parse single JSON: %v", err)
		}
	}

	// Only fallback to lineâ€‘delimited if single JSON parsing failed
	if len(videos) == 0 && parseErr != nil {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		log.Printf("[DEBUG] Attempting to parse %d lines as JSON", len(lines))
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
		log.Printf("[DEBUG] Parsed %d videos from line-delimited JSON", len(videos))
	}

	if len(videos) == 0 {
		log.Printf("[ERROR] No videos could be parsed from yt-dlp output")
		publishCustom("search_error", "No videos found. Try a different query.")
	} else {
		log.Printf("[DEBUG] Successfully parsed %d videos", len(videos))
		for i, v := range videos {
			if i < 3 {
				log.Printf("[DEBUG] Video %d: %s by %s (duration %.0f)", i, v.Title, v.Uploader, v.Duration)
			}
		}
	}

	publishCustom(resultVar, videos)
	publishCustom("search_error", nil)
	focusedResultIndex = -1
}

// --- Fetch trending videos (2 columns x 6 rows = 12 videos) ---
func fetchTrendingVideos(config *Config, resultVar string, vars map[string]interface{}) {
	log.Printf("[DEBUG] ===== Fetching trending videos =====")
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

// --- Rendering for search results (2 columns Ã— 6 rows) ---
func renderSearchResults(renderer *sdl.Renderer, config *Config, element Element) {
	// Check for error first
	if errMsg, ok := config.Variables.Custom["search_error"]; ok && errMsg != nil {
		font, _ := getCachedFont(config, "small")
		if font != nil {
			renderText(renderer, config, font, fmt.Sprintf("Error: %v", errMsg), sdl.Color{R: 255, G: 100, B: 100, A: 255}, element.X, element.Y)
			renderText(renderer, config, font, "Check terminal for details.", sdl.Color{R: 255, G: 255, B: 255, A: 255}, element.X, element.Y+20)
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
				renderText(renderer, config, font, "No results. Try searching.", sdl.Color{R: 255, G: 255, B: 255, A: 255}, element.X, element.Y)
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
	textOffsetX := thumbWidth + 10

	// Glassmorphism panel behind the grid
	fillRoundedRect(renderer, element.X-10, element.Y-10, elemWidth+20, elemHeight+20, 16, sdl.Color{R: 20, G: 26, B: 40, A: 200})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 120)
	renderer.FillRect(&sdl.Rect{X: element.X - 10, Y: element.Y - 10, W: elemWidth + 20, H: 1})

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
		yPos := element.Y + row*(cellHeight+10)

		// Highlight focused item with glow effect
		if i == focusedResultIndex {
			fillRoundedRect(renderer, xPos-8, yPos-8, cellWidth+16, cellHeight+16, 14, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 60})
			fillRoundedRect(renderer, xPos-4, yPos-4, cellWidth+8, cellHeight+8, 12, sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 140})
		}

		// Recently-played indicator (red bar at left of cell)
		if isRecentlyPlayed(vid.GetURL()) {
			renderer.SetDrawColor(200, 30, 30, 255)
			renderer.FillRect(&sdl.Rect{X: xPos - 5, Y: yPos, W: 4, H: cellHeight})
		}

		// Thumbnail
	thumbLoaded := false
	if vid.Thumbnail != "" {
		tex := loadThumbnail(renderer, vid.Thumbnail)
		if tex != nil {
			renderer.Copy(tex, nil, &sdl.Rect{X: xPos, Y: yPos, W: thumbWidth, H: thumbHeight})
			thumbLoaded = true
		}
	}
	if !thumbLoaded {
		renderer.SetDrawColor(30, 35, 50, 255)
		fillRoundedRect(renderer, xPos, yPos, thumbWidth, thumbHeight, 8, sdl.Color{R: 30, G: 35, B: 50, A: 255})
		playFont, _ := getCachedFont(config, "big")
		if playFont != nil {
			renderText(renderer, config, playFont, "▶", sdl.Color{R: 120, G: 140, B: 180, A: 200}, xPos+40, yPos+30)
		}
	}

		// Title (shortened)
		title := vid.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		renderText(renderer, config, titleFont, title, sdl.Color{R: 255, G: 255, B: 255, A: 255}, xPos+textOffsetX, yPos)

		// Uploader
		uploader := vid.Uploader
		if len(uploader) > 35 {
			uploader = uploader[:32] + "..."
		}
		renderText(renderer, config, font, uploader, sdl.Color{R: 200, G: 200, B: 200, A: 255}, xPos+textOffsetX, yPos+25)

		// Duration
		dur := fmt.Sprintf("%d:%02d", int(vid.Duration)/60, int(vid.Duration)%60)
		renderText(renderer, config, font, dur, sdl.Color{R: 200, G: 200, B: 200, A: 255}, xPos+textOffsetX, yPos+45)
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
	recordPlayed(url)
	log.Printf("[DEBUG] Playing video: %s", url)
	go func() {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", "start", "", url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
		if err := cmd.Run(); err != nil {
			log.Printf("Playback error: %v", err)
		}
	}()
}

// --- Input handling ---
func handleInputSelection(renderer *sdl.Renderer, config *Config, sceneIdx, elemIdx int) {
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
			fillRoundedRect(renderer, boxX, boxY, boxW, 64, 10, sdl.Color{R: 18, G: 22, B: 32, A: 235})
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
					}
				}
			case *sdl.MouseButtonEvent:
				if e.Button == sdl.BUTTON_LEFT && e.Type == sdl.MOUSEBUTTONDOWN {
					mx, my := int32(e.X), int32(e.Y)
					for idx, rect := range keyboardRects {
						if mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H {
							for r := 0; r < len(keyboard); r++ {
								for c := 0; c < len(keyboard[r]); c++ {
									if idx == r*len(keyboard[r])+c {
										keyboardPosY, keyboardPosX = r, c
										break
									}
								}
							}
							handleKeyboardInput(config)
							if keyboard[keyboardPosY][keyboardPosX] == "ENTER" {
								exitInput = true
							}
							break
						}
					}
				}
			}
		}
	}
}

func handleKeyboardInput(config *Config) {
	if keyboardPosY >= 0 && keyboardPosY < len(keyboard) && keyboardPosX >= 0 && keyboardPosX < len(keyboard[keyboardPosY]) {
		key := keyboard[keyboardPosY][keyboardPosX]
		switch key {
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
		// For all other keys, update variable immediately
		if key != "ENTER" {
			updateInputVariable(config)
		}
	}
}

func updateInputVariable(config *Config) {
	if activeSceneIndex != -1 && activeElementIndex != -1 {
		elem := config.Scenes[activeSceneIndex].Elements[activeElementIndex]
		if elem.Variable != "" {
			config.Variables.Custom[elem.Variable] = inputTextBuffer
			log.Printf("[DEBUG] Updated variable %s = %s", elem.Variable, inputTextBuffer)
			syncVariableOverrides(config)
		}
	}
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
	bgColor := resolveColor(config, element.BgColor, sdl.Color{R: 245, G: 247, B: 250, A: 255})
	r := int32(8)
	// border (rounded)
	fillRoundedRect(renderer, element.X, element.Y, width, height, r, lighten(bgColor, 30))
	gradientRoundedRect(renderer, element.X+2, element.Y+2, width-4, height-4, r-1, bgColor, darken(bgColor, 10))

	textColor := resolveColor(config, element.Color, sdl.Color{R: 20, G: 22, B: 28, A: 255})
	font, _ := getCachedFont(config, element.Font)

	isActive := (sceneIdx == activeSceneIndex && elemIdx == activeElementIndex)
	text := inputTextBuffer
	if isActive && (uint32(sdl.GetTicks64()/500)%2 == 0) {
		text += "_"
	}
	if font != nil {
		renderText(renderer, config, font, text, textColor, element.X+12, element.Y+10)
	}

	if isActive {
		fillRoundedRect(renderer, element.X-2, element.Y-2, width+4, height+4, r+2,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255})
		gradientRoundedRect(renderer, element.X, element.Y, width, height, r, bgColor, darken(bgColor, 10))
	}
}

// --- renderScene ---
func renderScene(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	ensureBackgroundTexture(renderer, config)

	if bgTexture != nil {
		renderer.Copy(bgTexture, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		renderer.SetDrawColor(overlayColor.R, overlayColor.G, overlayColor.B, overlayColor.A)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	} else {
		// No image: use the scene's background color (or a tasteful default).
		bg := resolveColor(config, scene.Background, sdl.Color{R: 14, G: 16, B: 22, A: 255})
		renderer.SetDrawColor(bg.R, bg.G, bg.B, 255)
		renderer.Clear()
		// subtle vertical gradient using the overlay tint
		for i := int32(0); i < screenHeight; i += 2 {
			t := float32(i) / float32(screenHeight)
			r := uint8(float32(bg.R) + t*float32(overlayColor.R-bg.R))
			g := uint8(float32(bg.G) + t*float32(overlayColor.G-bg.G))
			b := uint8(float32(bg.B) + t*float32(overlayColor.B-bg.B))
			renderer.SetDrawColor(r, g, b, 255)
			renderer.FillRect(&sdl.Rect{X: 0, Y: i, W: screenWidth, H: 2})
		}
	}

	for i, elem := range scene.Elements {
		switch elem.Type {
		case "label":
			renderLabelShadowed(renderer, config, elem)
		case "button":
			renderButtonElement(renderer, config, elem, i == selectedButtonIndex, i == hoveredButtonIndex)
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
		default:
			log.Printf("Unknown element type: %s", elem.Type)
		}
	}

	// Always-on top status bar (clock + weather + username)
	renderStatusBar(renderer, config)
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
	// bar background with a gradient top highlight
	renderer.SetDrawColor(10, 12, 18, 225)
	renderer.FillRect(&sdl.Rect{X: 0, Y: element.Y, W: screenWidth, H: 50})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
	renderer.FillRect(&sdl.Rect{X: 0, Y: element.Y + 48, W: screenWidth, H: 2})

	buttonX := int32(30)
	menuButtonRects = make(map[int]sdl.Rect)

	for i, scene := range config.Scenes {
		label := scene.Name
		active := i == currentSceneIndex
		color := sdl.Color{R: 150, G: 156, B: 170, A: 255}
		if active {
			color = sdl.Color{R: 255, G: 255, B: 255, A: 255}
		}
		textWidth, _ := renderText(renderer, config, font, label, color, buttonX, element.Y+15)
		if active {
			// underline the active scene
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
			renderer.FillRect(&sdl.Rect{X: buttonX - 2, Y: element.Y + 40, W: textWidth + 4, H: 3})
		}
		menuButtonRects[i] = sdl.Rect{X: buttonX - 10, Y: element.Y + 5, W: textWidth + 20, H: 40}
		buttonX += textWidth + 30
	}
}

func renderKeyboard(renderer *sdl.Renderer, config *Config) {
	if !virtualKeyboardActive {
		return
	}
	renderer.SetDrawColor(0, 0, 0, 200)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	keyWidth := int32(60)
	keyHeight := int32(60)
	padding := int32(10)
	startX := (screenWidth - (10*keyWidth + 9*padding)) / 2
	startY := int32(200)

	keyboardRects = nil
	keyboardKeys = nil

	for y, row := range keyboard {
		rowStartX := startX
		if y == 1 {
			rowStartX += keyWidth / 2
		}
		if y == 2 {
			rowStartX += keyWidth
		}
		if y == 3 {
			rowStartX += keyWidth * 3
		}
		for x, key := range row {
			bgColor := sdl.Color{R: 255, G: 255, B: 255}
			if x == keyboardPosX && y == keyboardPosY {
				bgColor = sdl.Color{R: 0, G: 255, B: 0}
			}
			renderer.SetDrawColor(bgColor.R, bgColor.G, bgColor.B, 255)
			rect := &sdl.Rect{
				X: rowStartX + int32(x)*(keyWidth+padding),
				Y: startY + int32(y)*(keyHeight+padding),
				W: keyWidth,
				H: keyHeight,
			}
			renderer.FillRect(rect)
			keyboardRects = append(keyboardRects, *rect)
			keyboardKeys = append(keyboardKeys, key)

			font, _ := getCachedFont(config, "medium")
			if font != nil {
				renderText(renderer, config, font, key, sdl.Color{R: 0, G: 0, B: 0}, rect.X+10, rect.Y+15)
			}
		}
	}
}

func initKeyboard() {
	keyboard = [][]string{
		{"Q", "W", "E", "R", "T", "Y", "U", "I", "O", "P"},
		{"A", "S", "D", "F", "G", "H", "J", "K", "L"},
		{"Z", "X", "C", "V", "B", "N", "M"},
		{"SPACE", "BACK", "ENTER"},
	}
	keyboardPosX, keyboardPosY = 0, 0
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
				currentSceneIndex = i
				selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
				focusedResultIndex = -1
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
		cmd := `yt-dlp --flat-playlist --dump-single-json --default-search ytsearch --no-playlist --no-check-certificate --geo-bypass --skip-download --quiet --ignore-errors --playlist-start 1 --playlist-end 12 "ytsearch12:$search_query"`
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
		if url == "" && activeElementIndex != -1 {
			if e := config.Scenes[activeSceneIndex].Elements[activeElementIndex]; e.Variable == "custom_link_url" {
				url = strings.TrimSpace(inputTextBuffer)
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
			val := element.VariableChangeValue
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
					log.Printf("[DEBUG] Set fullscreen = %v", b)
				}
			case "buttonColor", "labelColor":
				applyColorVar(config, element.VariableChange, val)
			case "weatherUnit":
				config.Variables.WeatherUnit = strings.TrimSpace(val)
				log.Printf("[DEBUG] Set weatherUnit = %s", config.Variables.WeatherUnit)
			default:
				config.Variables.Custom[element.VariableChange] = val
				log.Printf("[DEBUG] Set variable %s = %s", element.VariableChange, val)
			}
			// Persist theme/settings changes immediately
			saveConfig(config)
		}
	case "save_config":
		syncVariableOverrides(config)
		saveConfig(config)
	default:
		log.Printf("Unhandled trigger: %s", element.Trigger)
	}
}

func findFirstSelectableElement(scene SceneConfig) int {
	for i, e := range scene.Elements {
		if e.Type == "button" || e.Type == "input" || e.Type == "searchresults" || e.Type == "dynamiclist" {
			return i
		}
	}
	return -1
}

// Modified changeScene to auto-fetch trending and initialize search_query
func changeScene(config *Config, direction int) {
	currentSceneIndex += direction
	if currentSceneIndex < 0 {
		currentSceneIndex = len(config.Scenes) - 1
	} else if currentSceneIndex >= len(config.Scenes) {
		currentSceneIndex = 0
	}
	selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
	focusedResultIndex = -1
	sceneFadeStart = sdl.GetTicks64()
	sceneFadeAlpha = 0

	if sceneHasSearchResults(config.Scenes[currentSceneIndex]) {
		// Initialize the scene's search input variable (if any) before fetching
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
				log.Printf("[DEBUG] Initialized %s to empty string", queryVar)
			}
		}
		// Fetch trending videos once (stored into the scene's searchresults variable)
		srElem, _ := sceneSearchResultsElement(config.Scenes[currentSceneIndex])
		if _, ok := config.Variables.Custom[srElem.Variable]; !ok {
			log.Printf("[DEBUG] Entering scene with searchresults, fetching trending videos")
			go fetchTrendingVideos(config, srElem.Variable, snapshotVars(config))
		}
	}

	if dl, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok {
		focusedFileIndex = 0
		switch dl.Variable {
		case "fe_entries":
			feListDirectory(config)
		case "iptv_entries":
			loadIPTV(config)
		case "podcast_entries":
			go loadPodcasts(config)
		}
	}

	switch config.Scenes[currentSceneIndex].Name {
	case "Tickers":
		go fetchTickers(config)
	case "Cron":
		loadCron(config)
		go func() { publishCustom(cronStartupVar, loadStartupItems()) }()
	case "Weather":
		config.Variables.Custom["weather_screen_text"] = weatherScreenText(config)
	case "Hardware":
		go func() { publishCustom("hw_text", getHardwareInfo()) }()
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

func moveSelection(config *Config, direction int) {
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

	// Defaults for optional settings
	if config.Variables.WeatherUnit == "" {
		config.Variables.WeatherUnit = "C"
	}

	// Load persisted Recently Played and start the (best-effort) weather fetch.
	loadRecentlyPlayed()
	startWeather(config)

	// Auto-download yt-dlp / ffplay into tools_path if missing (OS-aware).
	// Non-fatal: if offline, the app falls back to system PATH.
	ensureRequiredTools(config)

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
		renderText(renderer, config, font, "JukaHub", sdl.Color{R: 220, G: 235, B: 255, A: 255}, screenWidth/2-80, screenHeight/2-80)
		renderText(renderer, config, font, "Loading...", sdl.Color{R: 180, G: 200, B: 220, A: 255}, screenWidth/2-70, screenHeight/2+60)
	}
	renderer.Present()
	sdl.Delay(500)
	config.Variables.LoadingSpinner = false

	sdl.GameControllerAddMapping("030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,")

	if sdl.NumJoysticks() > 0 {
		if c := sdl.GameControllerOpen(0); c != nil {
			defer c.Close()
		}
	}

	initKeyboard()

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
									} else {
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
					// Check menu
					for sceneIdx, rect := range menuButtonRects {
						if mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H {
							currentSceneIndex = sceneIdx
							selectedButtonIndex = findFirstSelectableElement(config.Scenes[currentSceneIndex])
							focusedResultIndex = -1
							break
						}
					}
					// Check other elements (input, button, searchresults)
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
							textWidth, textHeight := int32(0), int32(0)
							if font != nil {
								w, h, _ := font.SizeUTF8(elem.Text)
								textWidth, textHeight = int32(w), int32(h)
							}
							width := textWidth + 20
							height := textHeight + 10
							if string(elem.Width) != "" {
								w, _ := strconv.Atoi(string(elem.Width))
								width = int32(w)
							}
							if string(elem.Height) != "" {
								h, _ := strconv.Atoi(string(elem.Height))
								height = int32(h)
							}
							if mx >= elem.X && mx <= elem.X+width && my >= elem.Y && my <= elem.Y+height {
								handleTrigger(renderer, config, elem)
							}
						} else if elem.Type == "searchresults" {
							videos, ok := config.Variables.Custom[elem.Variable].([]VideoInfo)
							if !ok {
								continue
							}
							// Grid hit detection
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
								yPos := elem.Y + row*(cellHeight+10)
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
								lineH := int32(34)
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
					textWidth, textHeight := int32(0), int32(0)
					if font != nil {
						w, h, _ := font.SizeUTF8(elem.Text)
						textWidth, textHeight = int32(w), int32(h)
					}
					width := textWidth + 20
					height := textHeight + 10
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
					if mouseX >= elem.X && mouseX <= elem.X+width && mouseY >= elem.Y && mouseY <= elem.Y+height {
						hoveredButtonIndex = i
						break
					}
				}
			}
		}

		renderScene(renderer, config, config.Scenes[currentSceneIndex])

		// Scene fade-in effect
		if sceneFadeAlpha < 255 {
			elapsed := sdl.GetTicks64() - sceneFadeStart
			if elapsed > 300 {
				sceneFadeAlpha = 255
			} else {
				sceneFadeAlpha = uint8((elapsed * 255) / 300)
			}
			renderer.SetDrawColor(15, 17, 21, 255-sceneFadeAlpha)
			renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		}

		if config.Variables.LoadingSpinner {
			renderSpinner(renderer, screenWidth/2, screenHeight/2, 40, sdl.Color{R: 100, G: 180, B: 255, A: 200})
			font, _ := getCachedFont(config, "small")
			if font != nil {
				renderText(renderer, config, font, config.Variables.SpinnerText, sdl.Color{R: 220, G: 235, B: 255, A: 255}, screenWidth/2-60, screenHeight/2+50)
			}
		}

		renderer.Present()
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
