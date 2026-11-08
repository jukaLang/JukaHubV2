package main

// xpackscene.go — the screens an experience pack adds.
//
// A pack may carry config scenes: the same JSON a jukaconfig.json scene is, so
// anyone who can lay out a page in the web builder can share one. They are
// community content, so each scene is checked before it is ever drawn:
//
//   - only display and navigation elements (labels, buttons, pictures, the
//     disk chart, favourites …) — nothing that runs a program, writes the
//     config or plays a stream on its own;
//   - only triggers from an allow-list (changing screens, searching, playing
//     what the user picks); "external_app", "terminal_run", "set_variable" and
//     their kind are refused;
//   - inputs, switches and logs may only use variables named pack_*, so a scene
//     cannot reach a setting through them, and no text may show a secret;
//   - pictures must be files inside the pack.
//
// The scenes are added to the config in memory only, the way ensureAlarmScene
// adds the app's own screens: jukaconfig.json is never written. When their
// pack stops being the one in force they are hidden from the Start menu at
// once — the scene list is index-addressed, so taking one out under a running
// app would shift every screen after it — and they are gone after a restart.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// xpackSceneTypes are the element types a pack scene may use.
var xpackSceneTypes = map[string]bool{
	"label": true, "button": true, "image": true, "textlog": true, "piechart": true,
	"favorites": true, "recent": true, "unitconverter": true, "input": true, "toggle": true,
}

// xpackSceneTriggers are the triggers a pack scene's elements may use: moving
// between screens and things the user starts on purpose. Anything that runs a
// command, launches a program, writes settings or files, signs in or quits is
// missing on purpose.
var xpackSceneTriggers = map[string]bool{
	"change_scene": true, "theme_preset": true, "yt_search": true, "youtube_search": true,
	"youtube_smart": true, "youtube_trending": true, "youtube_play": true, triggerShortsOpen: true,
	"music_player": true, "play_focused": true, "fe_list": true, "fe_up": true,
	"ip_stream": true, "custom_link": true, "iptv_load": true, "podcast_load": true,
	"podcast_panel": true, "tick": true, "netspeed_run": true, "benchmark_run": true,
	"hw_load": true, "fav_tab": true, "fav_play": true, "unit_convert": true,
	"unit_swap": true, "unit_category": true, "shorts_fetch": true, "shorts_play": true,
	"shorts_refresh": true, "open_image": true, "community_themes": true,
	"onboarding_replay": true, "jukaland_init": true,
}

// xpackSceneNamePattern is what a pack scene may be called: short, plain, and
// safe to show in the Start menu.
var xpackSceneNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,39}$`)

// xpackMaxSceneElements bounds a pack scene; a real page has a few dozen.
const xpackMaxSceneElements = 64

// xpackInjectedScenes maps the name of every scene a pack added to the live
// config to the pack that owns it. Main thread only; xpackEnsure resets it for
// every freshly loaded config.
var xpackInjectedScenes = map[string]string{}

// xpackValidateScene decodes and checks one pack scene. It returns the scene
// ready to add (picture paths made absolute), the pack files it uses, and why
// it was refused.
func xpackValidateScene(raw json.RawMessage, dir string) (SceneConfig, []string, error) {
	var scene SceneConfig
	if err := json.Unmarshal(raw, &scene); err != nil {
		return SceneConfig{}, nil, fmt.Errorf("is not a scene (%v)", err)
	}
	scene.Name = strings.TrimSpace(scene.Name)
	if !xpackSceneNamePattern.MatchString(scene.Name) {
		return SceneConfig{}, nil, fmt.Errorf("name %q must be 1-40 letters, digits, spaces or dashes", scene.Name)
	}
	if strings.TrimSpace(scene.Layout) != "" {
		return SceneConfig{}, nil, fmt.Errorf("%s: only plain scenes can be added (layout %q)", scene.Name, scene.Layout)
	}
	if len(scene.Elements) == 0 {
		return SceneConfig{}, nil, fmt.Errorf("%s has no elements", scene.Name)
	}
	if len(scene.Elements) > xpackMaxSceneElements {
		return SceneConfig{}, nil, fmt.Errorf("%s has more than %d elements", scene.Name, xpackMaxSceneElements)
	}
	// The Start menu lists a scene by its monogram; a scene without one would
	// be unreachable, so it gets its first letter.
	icon := []rune(strings.TrimSpace(scene.Icon))
	if len(icon) == 0 {
		icon = []rune(scene.Name)[:1]
	}
	if len(icon) > 2 {
		icon = icon[:2]
	}
	scene.Icon = string(icon)
	scene.Description = xpackClip(strings.TrimSpace(scene.Description), 80)
	var assets []string
	if bg := strings.TrimSpace(scene.Background); bg != "" {
		path, err := xpackResolveAsset(dir, bg, xpackImageExts, xpackMaxWallpaperBytes)
		if err != nil {
			return SceneConfig{}, nil, fmt.Errorf("%s: background %s %v", scene.Name, bg, err)
		}
		scene.Background = path
		assets = append(assets, bg)
	}
	for i := range scene.Elements {
		rel, err := xpackCheckElement(&scene.Elements[i], dir)
		if err != nil {
			return SceneConfig{}, nil, fmt.Errorf("%s, element %d: %v", scene.Name, i+1, err)
		}
		if rel != "" {
			assets = append(assets, rel)
		}
	}
	return scene, assets, nil
}

// xpackCheckElement checks one element of a pack scene and normalises its
// trigger the way finalizeConfig does for a loaded config. It returns the pack
// file the element shows, if any.
func xpackCheckElement(e *Element, dir string) (string, error) {
	e.Type = strings.ToLower(strings.TrimSpace(e.Type))
	if !xpackSceneTypes[e.Type] {
		return "", fmt.Errorf("element type %q is not allowed in a pack", e.Type)
	}
	if e.ExternalAppPath != "" || e.ExternalAppReturn != "" || e.VariableChange != "" ||
		e.VariableChangeValue != "" || e.Command != "" || strings.TrimSpace(e.Video) != "" ||
		e.MediaVariable != "" || e.VideoVariable != "" || e.Source != "" || e.JsonPath != "" {
		return "", errors.New("sets a field a pack may not use (programs, settings or streams)")
	}
	trigger := strings.TrimSpace(e.Trigger)
	switch {
	case trigger == "":
	case strings.HasPrefix(trigger, "change_scene:"):
		target := strings.TrimSpace(strings.TrimPrefix(trigger, "change_scene:"))
		if target == "" {
			return "", errors.New("change_scene names no screen")
		}
		e.TriggerTarget = target
	case xpackSceneTriggers[trigger]:
	default:
		return "", fmt.Errorf("trigger %q is not allowed in a pack", trigger)
	}
	e.Trigger = trigger
	if e.TriggerTarget == "" && e.SceneChange != "" {
		e.TriggerTarget = e.SceneChange
	}
	if e.TriggerTarget == "" && e.TriggerValue != "" {
		e.TriggerTarget = e.TriggerValue
	}
	switch e.Type {
	case "input", "toggle", "textlog":
		if v := strings.TrimSpace(e.Variable); !strings.HasPrefix(v, "pack_") {
			return "", fmt.Errorf("%s variable %q must start with pack_", e.Type, e.Variable)
		}
	}
	if e.ListVariable != "" && !strings.HasPrefix(e.ListVariable, "pack_") {
		return "", fmt.Errorf("list variable %q must start with pack_", e.ListVariable)
	}
	e.Text = xpackScrubSecrets(e.Text)
	e.Placeholder = xpackScrubSecrets(e.Placeholder)
	if img := strings.TrimSpace(e.Image); img != "" {
		path, err := xpackResolveAsset(dir, img, xpackImageExts, xpackMaxWallpaperBytes)
		if err != nil {
			return "", fmt.Errorf("picture %s %v", img, err)
		}
		e.Image = path
		return img, nil
	}
	return "", nil
}

// xpackScrubSecrets drops $variable references that would print a secret (an
// API key, a token) into a scene's text.
func xpackScrubSecrets(text string) string {
	if !strings.Contains(text, "$") {
		return text
	}
	return varPattern.ReplaceAllStringFunc(text, func(m string) string {
		if cfgdocIsSecretKey(m[1:]) {
			return ""
		}
		return m
	})
}

// xpackSyncScenes makes the live config carry the scenes of the pack in force:
// each added once and listed in the Start menu, while scenes of a pack that is
// no longer in force are hidden from it. A scene name the config already uses
// for one of its own screens is never taken over.
func xpackSyncScenes(config *Config) {
	if config == nil {
		return
	}
	activeID := ""
	var want []SceneConfig
	if e, ok := xpackActiveEntry(config); ok {
		activeID = e.Manifest.ID
		want = e.Scenes
	}
	wanted := map[string]bool{}
	for _, sc := range want {
		wanted[sc.Name] = true
	}
	for name, owner := range xpackInjectedScenes {
		if owner == activeID && wanted[name] {
			continue
		}
		if idx := sceneIndexExact(config, name); idx >= 0 {
			config.Scenes[idx].Icon = ""
		}
	}
	for _, sc := range want {
		idx := sceneIndexExact(config, sc.Name)
		owner, ours := xpackInjectedScenes[sc.Name]
		switch {
		case idx < 0:
			config.Scenes = append(config.Scenes, xpackScaledScene(config, sc))
		case !ours:
			LogScene("xpack").Warn("pack screen skipped: the name is in use", "pack", activeID, "scene", sc.Name)
			continue
		case owner != activeID:
			config.Scenes[idx] = xpackScaledScene(config, sc)
		default:
			config.Scenes[idx].Icon = sc.Icon
		}
		xpackInjectedScenes[sc.Name] = activeID
	}
}

// xpackScaledScene copies a pack scene for the live config, scaled from the
// 1280x720 it is authored at to the canvas in use (the Brick's is 960 wide).
// At startup the device profile has not been applied yet, so the copy stays at
// design size and the profile scales it with every other scene.
func xpackScaledScene(config *Config, sc SceneConfig) SceneConfig {
	out := sc
	out.Elements = append([]Element(nil), sc.Elements...)
	w, h := canvasDimensionsOf(config)
	if w == uiCanvasWideWidth && h == uiCanvasWideHeight {
		return out
	}
	sx := float64(w) / float64(uiCanvasWideWidth)
	sy := float64(h) / float64(uiCanvasWideHeight)
	for i := range out.Elements {
		e := &out.Elements[i]
		e.X = int32(math.Round(float64(e.X) * sx))
		e.Y = int32(math.Round(float64(e.Y) * sy))
		e.Width = xpackScaleSize(e.Width, sx)
		e.Height = xpackScaleSize(e.Height, sy)
	}
	return out
}

// xpackScaleSize scales a width or height the way scaleSceneGeometry does.
func xpackScaleSize(raw StringOrInt, factor float64) StringOrInt {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return raw
	}
	v, ok := parsePosInt(text)
	if !ok || v == 0 {
		return raw
	}
	n := int32(math.Round(float64(v) * factor))
	if n < 1 {
		n = 1
	}
	return StringOrInt(strconv.Itoa(int(n)))
}
