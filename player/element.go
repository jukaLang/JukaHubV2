package main

// element.go defines the scene element type. Shape recovered from git history
// (c8aa566 player/main.go); Width/Height are StringOrInt because the web
// builder emits quoted numbers.

import (
	"encoding/json"
)

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
	Source        string      `json:"source"`
	AutoRefresh   bool        `json:"autoRefresh"`

	// Extended trigger payloads (set by the GUI generator)
	ExternalAppPath     string `json:"externalAppPath"`
	ExternalAppReturn   string `json:"externalAppReturn"`
	VariableChange      string `json:"variableChange"`
	VariableChangeValue string `json:"variableChangeValue"`

	// SceneChange is the target of the builder's "change_scene" dropdown trigger.
	SceneChange string `json:"sceneChange"`
	// Opacity is the web builder's per-element alpha (0..1, 1 = fully opaque). Nil
	// means "not set in the config", which is different from an explicit 0.
	Opacity *float64 `json:"opacity"`
	// MediaVariable / VideoVariable name the variable holding the media URL for
	// the builder's play_video / play_image triggers ("video"): both used to be
	// dropped on load, so those elements could never resolve their source.
	MediaVariable string `json:"mediaVariable"`
	VideoVariable string `json:"videoVariable"`
}

// UnmarshalJSON keeps the default decoding for every field but tolerates quoted
// numbers for the pixel/grid fields, which the web builder emits as strings.
func (e *Element) UnmarshalJSON(data []byte) error {
	type elementAlias Element
	aux := struct {
		*elementAlias
		X       intOrString `json:"x"`
		Y       intOrString `json:"y"`
		Columns intOrString `json:"columns"`
		Rows    intOrString `json:"rows"`
	}{elementAlias: (*elementAlias)(e)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	e.X = int32(aux.X)
	e.Y = int32(aux.Y)
	e.Columns = int32(aux.Columns)
	e.Rows = int32(aux.Rows)
	return nil
}

// Alpha is the element's opacity clamped to 0..1; an unset value is opaque.
func (e Element) Alpha() float64 {
	if e.Opacity == nil {
		return 1
	}
	v := *e.Opacity
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
