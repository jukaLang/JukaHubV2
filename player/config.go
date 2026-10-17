package main

// config.go defines the runtime configuration types shared by every scene and
// feature. Shapes recovered from git history (c8aa566 player/main.go), so the
// json tags and field types match jukaconfig.json and every call site.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
	ReducedMotion      bool              `json:"reducedMotion"`
	LowPower           bool              `json:"lowPower"`
	GridColumns        int               `json:"gridColumns"`
	SearchWidth        int               `json:"searchWidth"`
	DynamicBg          bool              `json:"dynamicBg"`
	ScreensaverTimeout int               `json:"screensaverTimeout"`
	SocialNotifs       bool              `json:"socialNotifs"`

	// Optional feature toggles (future-facing). These are persisted as first-class
	// config fields so the app can be configured on the device without requiring a
	// code change, and so jukaconfig.json stays a readable feature catalog.
	// They are safe to ignore when the runtime feature is not wired yet.
	NetPlayEnabled       bool `json:"netPlayEnabled"`
	VideoPlayerEnabled   bool `json:"videoPlayerEnabled"`
	ScreensaverEnabled   bool `json:"screensaverEnabled"`
	LauncherEnabled      bool `json:"launcherEnabled"`
	ExternalAppEnabled   bool `json:"externalAppEnabled"`
	PackageRepoEnabled   bool `json:"packageRepoEnabled"`
	ThemeGalleryEnabled  bool `json:"themeGalleryEnabled"`

	// TrimUI/tablet runtime hints (best-effort). These describe the target device
	// so the app can keep one config layout that still feels native on a handheld
	// like the TrimUI Smart Pro. Unknown or missing values are treated as "no
	// preference", never as an error.
	DeviceModel         string `json:"deviceModel"`
	TargetPlatform      string `json:"targetPlatform"`
	DefaultOrientation  string `json:"defaultOrientation"`
	AccentColor         string `json:"accentColor"`

	Custom             map[string]interface{}
	LoadingSpinner     bool   `json:"-"`
	SpinnerText        string `json:"-"`
}

type SceneConfig struct {
	Name        string    `json:"name"`
	Icon        string    `json:"icon"`
	Description string    `json:"description"`
	Background  string    `json:"background"`
	Layout      string    `json:"layout"`
	Elements    []Element `json:"elements"`
}

// intOrString accepts a JSON number or a numeric string, so a config written by
// the web builder ("x": "30") parses exactly like one written by the player,
// which emits numbers. Coordinates used to be plain int32: a single quoted
// coordinate made json.Decode fail, which rejected the whole config and left
// the player unable to start.
type intOrString int32

func (n *intOrString) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		str = strings.TrimSuffix(str, "px")
		str = strings.TrimSpace(str)
		if str == "" {
			*n = 0
			return nil
		}
		f, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return fmt.Errorf("intOrString: %q is not a number", str)
		}
		*n = intOrString(f)
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("intOrString: expected number or numeric string, got %q", s)
	}
	*n = intOrString(f)
	return nil
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
		"gridColumns":        true,
		"searchWidth":        true,
		"dynamicBg":          true,
		"screensaverTimeout": true,	"socialNotifs":       true,
	"reducedMotion":      true,
	"lowPower":           true,
	"custom":             true,

	// First-class feature-toggle and device-hint fields. These live in the struct
	// now so jukaconfig.json stays a readable catalog; keep them known here so the
	// Variables decoder puts them in the struct instead of burying them in Custom.
	"netPlayEnabled":       true,
	"videoPlayerEnabled":   true,
	"screensaverEnabled":   true,
	"launcherEnabled":      true,
	"externalAppEnabled":   true,
	"packageRepoEnabled":   true,
	"themeGalleryEnabled":  true,
	"deviceModel":         true,		"targetPlatform":      true,
		"defaultOrientation":  true,
		"accentColor":         true,

	// Retired settings. Older configs still carry these; they no longer control
	// anything (playback is always ffplay), so drop them instead of parking them
	// in Custom where they would be re-saved forever.
	"audioBackend": true,
	"AudioBackend": true,
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
	// The web builder regenerates settings under a capitalized "Custom" key
	// (variables.Custom.Custom["ButtonColor"] etc.); flatten those blocks so
	// Get/$-substitution resolve them like the legacy flat format.
	flattenCustomBlocks(v.Custom)
	tmp := &Config{Variables: *v}
	syncVariableOverrides(tmp)
	*v = tmp.Variables
	return nil
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
	// First-class feature-toggle / device-hint fields now live in the struct.
	// Expose them through $ substitution so the rest of the app can continue to
	// read them via Variables.Get even if the config is the canonical source.
	switch name {
	case "netPlayEnabled":        return boolString(v.NetPlayEnabled)
	case "videoPlayerEnabled":   return boolString(v.VideoPlayerEnabled)
	case "screensaverEnabled":   return boolString(v.ScreensaverEnabled)
	case "launcherEnabled":      return boolString(v.LauncherEnabled)
	case "externalAppEnabled":   return boolString(v.ExternalAppEnabled)
	case "packageRepoEnabled":   return boolString(v.PackageRepoEnabled)
	case "themeGalleryEnabled":  return boolString(v.ThemeGalleryEnabled)
	case "deviceModel":          return v.DeviceModel
	case "targetPlatform":       return v.TargetPlatform
	case "defaultOrientation":   return v.DefaultOrientation
	case "accentColor":          return v.AccentColor
	}
	return ""
}

