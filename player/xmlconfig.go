package main

import (
	"encoding/xml"
	"fmt"
	"os"
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
	Variables   xmlVariables `xml:"variables"`
	Scenes      xmlScenes    `xml:"scenes"`
}

type xmlVariables struct {
	XMLName    xml.Name       `xml:"variables"`
	FontSizes  *xmlFontSizes  `xml:"fontSizes"`
	CustomVars []xmlCustomVar `xml:",any"`
}

type xmlFontSizes struct {
	Title  int `xml:"title"`
	Big    int `xml:"big"`
	Medium int `xml:"medium"`
	Small  int `xml:"small"`
}

type xmlCustomVar struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type xmlScenes struct {
	XMLName xml.Name   `xml:"scenes"`
	Scene   []xmlScene `xml:"scene"`
}

type xmlScene struct {
	XMLName  xml.Name     `xml:"scene"`
	Name     string       `xml:"name,attr"`
	Elements []xmlElement `xml:"element"`
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
	SceneChange         string   `xml:"sceneChange,attr"`
	ExternalAppPath     string   `xml:"externalAppPath,attr"`
	ExternalAppReturn   string   `xml:"externalAppReturn,attr"`
	VariableChange      string   `xml:"variableChange,attr"`
	VariableChangeValue string   `xml:"variableChangeValue,attr"`
	MediaVariable       string   `xml:"mediaVariable,attr"`
	Command             string   `xml:"command,attr"`
	Variable            string   `xml:"variable,attr"`
	Source              string   `xml:"source,attr"`
	JsonPath            string   `xml:"jsonPath,attr"`
	AutoRefresh         string   `xml:"autoRefresh,attr"`
	Image               string   `xml:"image,attr"`
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
		Title:       xc.Title,
		Author:      xc.Author,
		Description: xc.Description,
		Variables: Variables{
			Fonts:     make(map[string]string),
			FontSizes: make(map[string]int),
			Custom:    make(map[string]interface{}),
		},
	}

	// Font sizes
	if xc.Variables.FontSizes != nil {
		if xc.Variables.FontSizes.Title > 0 {
			config.Variables.FontSizes["title"] = xc.Variables.FontSizes.Title
		}
		if xc.Variables.FontSizes.Big > 0 {
			config.Variables.FontSizes["big"] = xc.Variables.FontSizes.Big
		}
		if xc.Variables.FontSizes.Medium > 0 {
			config.Variables.FontSizes["medium"] = xc.Variables.FontSizes.Medium
		}
		if xc.Variables.FontSizes.Small > 0 {
			config.Variables.FontSizes["small"] = xc.Variables.FontSizes.Small
		}
	}

	// Custom variables — store them with proper type coercion
	for _, cv := range xc.Variables.CustomVars {
		if cv.XMLName.Local == "fontSizes" {
			continue // already handled
		}
		val := strings.TrimSpace(cv.Value)
		if val == "" {
			continue
		}
		// Try to coerce to the most useful type
		config.Variables.Custom[cv.XMLName.Local] = coerceXMLValue(val)
	}

	// Extract known variable fields from Custom map
	config.Variables.BackgroundImage = xmlCustomString(config.Variables.Custom, "backgroundImage")

	if bgStr := xmlCustomString(config.Variables.Custom, "buttonColor"); bgStr != "" {
		applyColorVar(config, "buttonColor", bgStr)
	}
	if bgStr := xmlCustomString(config.Variables.Custom, "labelColor"); bgStr != "" {
		applyColorVar(config, "labelColor", bgStr)
	}
	if bgStr := xmlCustomString(config.Variables.Custom, "inputColor"); bgStr != "" {
		applyColorVar(config, "inputColor", bgStr)
	}

	// Scenes
	for _, xs := range xc.Scenes.Scene {
		scene := SceneConfig{
			Name:     xs.Name,
			Elements: make([]Element, 0, len(xs.Elements)),
		}
		for _, xe := range xs.Elements {
			elem := Element{
				Type:                xe.Type,
				Text:                strings.TrimSpace(xe.Text),
				Color:               xe.Color,
				BgColor:             xe.BgColor,
				Font:                xe.Font,
				Trigger:             xe.Trigger,
				ExternalAppPath:     xe.ExternalAppPath,
				ExternalAppReturn:   xe.ExternalAppReturn,
				VariableChange:      xe.VariableChange,
				VariableChangeValue: xe.VariableChangeValue,
				Command:             xe.Command,
				Variable:            xe.Variable,
				JsonPath:            xe.JsonPath,
				AutoRefresh:         xe.AutoRefresh == "true",
				Image:               xe.Image,
			}
			elem.X = xmlParseInt32(xe.X)
			elem.Y = xmlParseInt32(xe.Y)
			elem.Width = StringOrInt(xe.Width)
			elem.Height = StringOrInt(xe.Height)
			if xe.Trigger == "change_scene" {
				elem.TriggerTarget = xe.SceneChange
			}
			scene.Elements = append(scene.Elements, elem)
		}
		config.Scenes = append(config.Scenes, scene)
	}

	return config, nil
}

// LoadXMLConfigWithFallback tries XML first (jukaconfig.xml), then falls
// back to the standard JSON path. Returns the loaded config and its source path.
func LoadXMLConfigWithFallback(jsonPath string) (*Config, string, error) {
	// Try XML variant first
	xmlPath := strings.TrimSuffix(jsonPath, ".json") + ".xml"
	if cfg, err := LoadXMLConfig(xmlPath); err == nil {
		return cfg, xmlPath, nil
	}
	// Fall back to JSON
	cfg, err := LoadLastKnownGood(jsonPath)
	if err != nil {
		return nil, "", err
	}
	return cfg, jsonPath, nil
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

// IsXMLConfig returns true if the given filename has an .xml extension.
func IsXMLConfig(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".xml")
}
