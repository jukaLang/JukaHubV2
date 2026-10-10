package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// --- XML Config Types --------------------------------------------------------
// These mirror the JSON config structs but are optimized for XML deserialization
// with the web editor's export format (jukaconfig.xml).

type xmlJukaConfig struct {
	XMLName     xml.Name     `xml:"jukaconfig"`
	Title       string       `xml:"title"`
	Author      string       `xml:"author"`
	Description string       `xml:"description"`
	AppName     string       `xml:"appName"`
	Version     string       `xml:"version"`
	Width       string       `xml:"width"`
	Height      string       `xml:"height"`
	Background  string       `xml:"background"`
	FontPath    string       `xml:"fontPath"`
	Channel     *xmlChannel  `xml:"channelProfile"`
	Variables   xmlVariables `xml:"variables"`
	Scenes      xmlScenes    `xml:"scenes"`
}

// Channel profile (Discord chat credentials) is optional in the XML export.
type xmlChannel struct {
	ChannelID string `xml:"channelId,attr"`
	Token     string `xml:"token,attr"`
}

// xmlVariables captures every child of <variables> as an entry, however deeply
// it nests: the settings the player types (fontSizes, fonts, the colours) and
// the free-form ones the builder writes side by side.
type xmlVariables struct {
	XMLName xml.Name
	Entries []xmlCustomVar `xml:",any"`
}

// xmlCustomVar captures a variable entry. `Custom` blocks written by the web
// builder nest their entries, so children are captured too and flattened later.
type xmlCustomVar struct {
	XMLName  xml.Name
	Value    string         `xml:",chardata"`
	Children []xmlCustomVar `xml:",any"`
}

type xmlScenes struct {
	XMLName xml.Name   `xml:"scenes"`
	Scene   []xmlScene `xml:"scene"`
}

type xmlScene struct {
	XMLName     xml.Name     `xml:"scene"`
	Name        string       `xml:"name,attr"`
	Icon        string       `xml:"icon,attr"`
	Description string       `xml:"description,attr"`
	Background  string       `xml:"background,attr"`
	Layout      string       `xml:"layout,attr"`
	Elements    []xmlElement `xml:"element"`
}

type xmlElement struct {
	XMLName             xml.Name `xml:"element"`
	Type                string   `xml:"type,attr"`
	X                   string   `xml:"x,attr"`
	Y                   string   `xml:"y,attr"`
	Width               string   `xml:"width,attr"`
	Height              string   `xml:"height,attr"`
	Color               string   `xml:"color,attr"`
	BgColor             string   `xml:"bgColor,attr"`
	Font                string   `xml:"font,attr"`
	Opacity             string   `xml:"opacity,attr"`
	Trigger             string   `xml:"trigger,attr"`
	TriggerValue        string   `xml:"triggerValue,attr"`
	SceneChange         string   `xml:"sceneChange,attr"`
	ExternalAppPath     string   `xml:"externalAppPath,attr"`
	ExternalAppReturn   string   `xml:"externalAppReturn,attr"`
	VariableChange      string   `xml:"variableChange,attr"`
	VariableChangeValue string   `xml:"variableChangeValue,attr"`
	MediaVariable       string   `xml:"mediaVariable,attr"`
	Command             string   `xml:"command,attr"`
	Variable            string   `xml:"variable,attr"`
	ListVariable        string   `xml:"listVariable,attr"`
	Columns             string   `xml:"columns,attr"`
	Rows                string   `xml:"rows,attr"`
	Placeholder         string   `xml:"placeholder,attr"`
	Style               string   `xml:"style,attr"`
	Icon                string   `xml:"icon,attr"`
	Source              string   `xml:"source,attr"`
	JsonPath            string   `xml:"jsonPath,attr"`
	AutoRefresh         string   `xml:"autoRefresh,attr"`
	Image               string   `xml:"image,attr"`
	Video               string   `xml:"video,attr"`
	VideoVariable       string   `xml:"videoVariable,attr"`
	Text                string   `xml:",chardata"`
}

// LoadXMLConfig parses a jukaconfig.xml file and converts it to the standard
// Config struct so the rest of the player can consume it identically to JSON.
func LoadXMLConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var xc xmlJukaConfig
	if err := xml.Unmarshal(data, &xc); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}

	config := &Config{
		AppName:     "JukaHub",
		Version:     "0.4.0",
		Width:       1280,
		Height:      720,
		Background:  xc.Background,
		FontPath:    xc.FontPath,
		Title:       xc.Title,
		Author:      xc.Author,
		Description: xc.Description,
	}
	// Optional metadata: the web builder exports these so an XML-only project
	// keeps its app name, canvas size, background and font, exactly like JSON.
	if v := strings.TrimSpace(xc.AppName); v != "" {
		config.AppName = v
	}
	if v := strings.TrimSpace(xc.Version); v != "" {
		config.Version = v
	}
	if n, err := strconv.Atoi(strings.TrimSpace(xc.Width)); err == nil && n > 0 {
		config.Width = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(xc.Height)); err == nil && n > 0 {
		config.Height = n
	}
	if xc.Channel != nil {
		config.ChannelProfile.ChannelID = xc.Channel.ChannelID
		config.ChannelProfile.Token = xc.Channel.Token
	}

	// Variables. The <variables> tree is converted to its JSON equivalent and fed
	// to Variables.UnmarshalJSON, so an XML config resolves typed settings
	// (colours, font sizes, flags) and custom variables exactly like a JSON one
	// - including the nested <Custom> block the web builder writes.
	if data, err := xmlVariablesJSON(xc.Variables); err != nil {
		log.Printf("[config] reading <variables>: %v", err)
	} else if err := json.Unmarshal(data, &config.Variables); err != nil {
		log.Printf("[config] reading <variables>: %v", err)
	}
	// XML attribute round-trip can leave the first-class bool/int/color fields as
	// zero if the builder omits them; re-derive the cheap overlays from the parsed
	// variables so XML and JSON keep one visible behavior for the runtime.
	syncVariableOverrides(config)
	if config.Variables.Custom == nil {
		config.Variables.Custom = make(map[string]interface{})
	}

	// Scenes
	for _, xs := range xc.Scenes.Scene {
		scene := SceneConfig{
			Name:        xs.Name,
			Icon:        xs.Icon,
			Description: xs.Description,
			Background:  xs.Background,
			Layout:      xs.Layout,
			Elements:    make([]Element, 0, len(xs.Elements)),
		}
		for _, xe := range xs.Elements {
			elem := Element{
				Type:                xe.Type,
				Text:                xmlTextValue(xe.Text),
				Color:               xe.Color,
				BgColor:             xe.BgColor,
				Font:                xe.Font,
				Trigger:             xe.Trigger,
				TriggerValue:        xe.TriggerValue,
				SceneChange:         xe.SceneChange,
				ExternalAppPath:     xe.ExternalAppPath,
				ExternalAppReturn:   xe.ExternalAppReturn,
				VariableChange:      xe.VariableChange,
				VariableChangeValue: xe.VariableChangeValue,
				MediaVariable:       xe.MediaVariable,
				VideoVariable:       xe.VideoVariable,
				Command:             xe.Command,
				Variable:            xe.Variable,
				ListVariable:        xe.ListVariable,
				Placeholder:         xe.Placeholder,
				Style:               xe.Style,
				Icon:                xe.Icon,
				Source:              xe.Source,
				JsonPath:            xe.JsonPath,
				AutoRefresh:         xe.AutoRefresh == "true",
				Image:               xe.Image,
				Video:               xe.Video,
			}
			elem.X = xmlParseInt32(xe.X)
			elem.Y = xmlParseInt32(xe.Y)
			elem.Columns = xmlParseInt32(xe.Columns)
			elem.Rows = xmlParseInt32(xe.Rows)
			elem.Width = StringOrInt(xe.Width)
			elem.Height = StringOrInt(xe.Height)
			// Opacity stays nil when the attribute is absent, so "not set" and an
			// explicit 0 remain distinguishable.
			if v, ok := xmlParseFloat(xe.Opacity); ok {
				elem.Opacity = &v
			}
			// Pipe the XML post-load normalization through the same path the JSON
			// loader uses so element semantics stay consistent across formats.
			syncVariableOverrides(&Config{Variables: config.Variables})
			scene.Elements = append(scene.Elements, elem)
		}
		config.Scenes = append(config.Scenes, scene)
	}

	// Typed variable overrides and trigger normalisation, shared with the JSON
	// loader so either format yields the same runtime config.
	syncVariableOverrides(config)
	finalizeConfig(config)

	return config, nil
}

// XMLSidecarPath is the jukaconfig.xml that sits beside jukaconfig.json.
func XMLSidecarPath(configPath string) string {
	if ext := filepath.Ext(configPath); ext != "" {
		return strings.TrimSuffix(configPath, ext) + ".xml"
	}
	return configPath + ".xml"
}

// ResolveConfigPath decides which of the two config files to load when both
// jukaconfig.json and jukaconfig.xml are present: whichever was written last
// wins. The player persists its own edits to JSON, so a fixed "XML always
// wins" rule silently discarded every saved change as soon as an XML export sat
// next to it - the design on screen was not the design that had been saved.
// When the timestamps are equal or unknown, the player's own file (JSON) wins.
func ResolveConfigPath(configPath string) string {
	xmlPath := XMLSidecarPath(configPath)
	jsonInfo, jsonErr := os.Stat(configPath)
	xmlInfo, xmlErr := os.Stat(xmlPath)
	if xmlErr != nil {
		return configPath
	}
	if jsonErr != nil {
		return xmlPath
	}
	if xmlInfo.ModTime().After(jsonInfo.ModTime()) {
		return xmlPath
	}
	return configPath
}

// LoadXMLConfigWithFallback loads the newest of jukaconfig.json and
// jukaconfig.xml, falling back to the other format if the chosen one does not
// parse. Returns the config and the path it actually came from.
func LoadXMLConfigWithFallback(jsonPath string) (*Config, string, error) {
	xmlPath := XMLSidecarPath(jsonPath)
	primary := ResolveConfigPath(jsonPath)

	if IsXMLConfig(primary) {
		cfg, err := LoadXMLConfig(primary)
		if err == nil {
			return cfg, primary, nil
		}
		// A broken XML export must not cost the user their project: fall back to
		// the JSON the player keeps and say so.
		log.Printf("[config] %s is not usable (%v); loading %s instead", primary, err, jsonPath)
	}

	cfg, err := LoadLastKnownGood(jsonPath)
	if err != nil {
		if !IsXMLConfig(primary) {
			if xcfg, xerr := LoadXMLConfig(xmlPath); xerr == nil {
				log.Printf("[config] %s is not usable (%v); loading %s instead", jsonPath, err, xmlPath)
				return xcfg, xmlPath, nil
			}
		}
		return nil, "", err
	}
	return cfg, jsonPath, nil
}

// xmlTextValue keeps element text verbatim; a body that is only whitespace is
// the pretty-printer's indentation rather than content, so it becomes empty.
func xmlTextValue(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// xmlParseFloat parses an optional numeric attribute (opacity, ...); the second
// result reports whether a value was present at all.
func xmlParseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, true
	}
	return 0, false
}

// xmlVariablesJSON renders the <variables> block as the JSON object the player's
// Variables decoder expects, so both config formats share one implementation.
func xmlVariablesJSON(v xmlVariables) ([]byte, error) {
	vars := make(map[string]interface{}, len(v.Entries))
	for _, e := range v.Entries {
		val, _ := xmlVarValue(e)
		vars[e.XMLName.Local] = val
	}
	// XML cannot tell an empty string from an empty block, and the player types
	// these keys as objects; json.Unmarshal would reject the whole variables
	// block over a single <fonts></fonts>.
	for _, key := range []string{"fonts", "Fonts", "custom", "Custom", "fontSizes", "FontSizes"} {
		if s, ok := vars[key].(string); ok && strings.TrimSpace(s) == "" {
			vars[key] = map[string]interface{}{}
		}
	}
	return json.Marshal(vars)
}

// xmlVarValue converts a captured <variables> entry into a typed value,
// recursing into nested blocks so <Custom><ButtonColor>#1E293B</ButtonColor>
// </Custom> keeps every setting the builder wrote.
func xmlVarValue(cv xmlCustomVar) (interface{}, bool) {
	if len(cv.Children) > 0 {
		m := make(map[string]interface{}, len(cv.Children))
		for _, ch := range cv.Children {
			if v, ok := xmlVarValue(ch); ok {
				m[ch.XMLName.Local] = v
			} else {
				m[ch.XMLName.Local] = ""
			}
		}
		return m, true
	}
	// Keep empty strings: the JSON path keeps them too, so a cleared setting
	// round-trips identically in both formats.
	return coerceXMLValue(strings.TrimSpace(cv.Value)), true
}

// coerceXMLValue tries to parse an XML string value into a typed Go value.
func coerceXMLValue(val string) interface{} {
	// Boolean
	if strings.EqualFold(val, "true") {
		return true
	}
	if strings.EqualFold(val, "false") {
		return false
	}
	// Integer
	if i, err := strconv.ParseInt(val, 10, 64); err == nil {
		return i
	}
	// Float
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return f
	}
	return val
}

// xmlParseInt32 parses a string to int32, returning 0 on failure.
func xmlParseInt32(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if i, err := strconv.ParseInt(s, 10, 32); err == nil {
		return int32(i)
	}
	return 0
}

// xmlCustomString extracts a string value from the Custom map.
func xmlCustomString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// setCustomBool / clearCustomBool / setCustomString are shared helpers used by
// both the JSON and XML config paths to keep the new first-class feature-toggle
// and device-hint fields consistent between Variables struct fields and the flat
// Custom map.
func setCustomBool(m map[string]interface{}, key string, val bool) {
	m[key] = val
}

func clearCustomBool(m map[string]interface{}, key string) {
	delete(m, key)
}

func setCustomString(m map[string]interface{}, key, val string) {
	m[key] = val
}

// boolString is defined in player/main.go as part of the Variables helpers.
// This declaration exists only so the XML config path compiles as part of the same
// package without importing a separate helper file.
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// IsXMLConfig returns true if the given filename has an .xml extension.
func IsXMLConfig(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".xml")
}

// syncVariableOverrides normalizes Variables after they are loaded from either
// JSON or XML so the runtime sees one consistent view. It is intentionally pure
// input-to-output: it does not persist, and it is safe to call on any config
// that has already been parsed.
func syncVariableOverrides(config *Config) {

	if config == nil {
		return
	}
	v := &config.Variables
	if v.Custom == nil {
		return
	}
	// Mirror the canonical struct fields back into the flat Custom map so the rest
	// of the app can keep using $ substitution for settings that were originally
	// stored under both keys. This keeps legacy configs (buttonColor: "#94a3b8")
	// and regenerated configs (Custom.ButtonColor: "#94a3b8") behaving the same.
	if s, ok := v.Custom["buttonColor"]; ok {
		// Prefer a string value when both a string and a struct mirror are present.
		if str, isStr := s.(string); isStr && str != "" {
			// Only overwrite when the struct field is effectively empty.
			if v.ButtonColor.R == 0 && v.ButtonColor.G == 0 && v.ButtonColor.B == 0 {
				if c, ok := parseHexColor(str); ok {
					v.ButtonColor = c
				}
			}
		}
	}
	if v.ButtonColor.R != 0 || v.ButtonColor.G != 0 || v.ButtonColor.B != 0 {
		v.Custom["buttonColor"] = fmt.Sprintf("#%02X%02X%02X", v.ButtonColor.R, v.ButtonColor.G, v.ButtonColor.B)
	}
	if s, ok := v.Custom["labelColor"]; ok {
		if str, isStr := s.(string); isStr && str != "" {
			if v.LabelColor.R == 0 && v.LabelColor.G == 0 && v.LabelColor.B == 0 {
				if c, ok := parseHexColor(str); ok {
					v.LabelColor = c
				}
			}
		}
	}
	if v.LabelColor.R != 0 || v.LabelColor.G != 0 || v.LabelColor.B != 0 {
		v.Custom["labelColor"] = fmt.Sprintf("#%02X%02X%02X", v.LabelColor.R, v.LabelColor.G, v.LabelColor.B)
	}
	if s, ok := v.Custom["inputColor"]; ok {
		if str, isStr := s.(string); isStr && str != "" {
			if v.InputColor.R == 0 && v.InputColor.G == 0 && v.InputColor.B == 0 {
				if c, ok := parseHexColor(str); ok {
					v.InputColor = c
				}
			}
		}
	}
	if v.InputColor.R != 0 || v.InputColor.G != 0 || v.InputColor.B != 0 {
		v.Custom["inputColor"] = fmt.Sprintf("#%02X%02X%02X", v.InputColor.R, v.InputColor.G, v.InputColor.B)
	}
	// Forward the designer-facing size map out of the struct so code that looks up
	// font sizes by role (title/big/medium/small) still finds them.
	if len(v.FontSizes) > 0 {
		for k, sz := range v.FontSizes {
			v.Custom[fmt.Sprintf("fontSize_%s", k)] = sz
		}
	}
	// Push the current theme preset back into the custom map so scene code that
	// reads $theme_preset / $theme_background etc. sees the active values.
	if preset, ok := v.Custom["theme_preset"]; ok {
		if name, isStr := preset.(string); isStr && name != "" {
			p := GetThemePreset(name)
			v.Custom["theme_background"] = p.Background
			v.Custom["theme_button"] = p.ButtonColor
			v.Custom["theme_label"] = p.LabelColor
			v.Custom["theme_input"] = p.InputColor
			v.Custom["theme_surface"] = p.Surface
			v.Custom["theme_surface_alt"] = p.SurfaceAlt
			v.Custom["theme_surface_raised"] = p.SurfaceRaised
			v.Custom["theme_text_primary"] = p.TextPrimary
			v.Custom["theme_text_secondary"] = p.TextSecondary
			v.Custom["theme_text_tertiary"] = p.TextTertiary
			v.Custom["theme_border_subtle"] = p.BorderSubtle
			v.Custom["theme_border_default"] = p.BorderDefault
			v.Custom["theme_border_focus"] = p.BorderFocus
			v.Custom["theme_success"] = p.Success
			v.Custom["theme_warning"] = p.Warning
			v.Custom["theme_danger"] = p.Danger
			v.Custom["theme_info"] = p.Info
			v.Custom["theme_overlay"] = p.Overlay
		}
	}

	// Reconcile the new first-class feature-toggle / device-hint fields with the
	// existing Custom map so the app can read them either as struct fields or via
	// $ substitution and neither view gets out of sync.
	if v.NetPlayEnabled {
		setCustomBool(v.Custom, "netPlayEnabled", true)
	} else {
		clearCustomBool(v.Custom, "netPlayEnabled")
	}
	if v.VideoPlayerEnabled {
		setCustomBool(v.Custom, "videoPlayerEnabled", true)
	} else {
		clearCustomBool(v.Custom, "videoPlayerEnabled")
	}
	if v.ScreensaverEnabled {
		setCustomBool(v.Custom, "screensaverEnabled", true)
	} else {
		clearCustomBool(v.Custom, "screensaverEnabled")
	}
	if v.LauncherEnabled {
		setCustomBool(v.Custom, "launcherEnabled", true)
	} else {
		clearCustomBool(v.Custom, "launcherEnabled")
	}
	if v.ExternalAppEnabled {
		setCustomBool(v.Custom, "externalAppEnabled", true)
	} else {
		clearCustomBool(v.Custom, "externalAppEnabled")
	}
	if v.PackageRepoEnabled {
		setCustomBool(v.Custom, "packageRepoEnabled", true)
	} else {
		clearCustomBool(v.Custom, "packageRepoEnabled")
	}
	if v.ThemeGalleryEnabled {
		setCustomBool(v.Custom, "themeGalleryEnabled", true)
	} else {
		clearCustomBool(v.Custom, "themeGalleryEnabled")
	}
	if v.DeviceModel != "" {
		setCustomString(v.Custom, "deviceModel", v.DeviceModel)
	}
	if v.TargetPlatform != "" {
		setCustomString(v.Custom, "targetPlatform", v.TargetPlatform)
	}
	if v.DefaultOrientation != "" {
		setCustomString(v.Custom, "defaultOrientation", v.DefaultOrientation)
	}
	if v.AccentColor != "" {
		setCustomString(v.Custom, "accentColor", v.AccentColor)
	}
}

// setCustomBool / clearCustomBool / setCustomString are shared helpers used by
// both the JSON and XML config paths to keep the new first-class feature-toggle
// and device-hint fields consistent between Variables struct fields and the flat
// Custom map.
func setCustomBool(m map[string]interface{}, key string, val bool) {
	m[key] = val
}

func clearCustomBool(m map[string]interface{}, key string) {
	delete(m, key)
}

func setCustomString(m map[string]interface{}, key, val string) {
	m[key] = val
}

// boolString is defined in player/main.go; this declaration keeps the XML config
// path compilable as part of the same package without a separate helper file.
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
