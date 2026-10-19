package main

// scene.go — one scene per file, reached through a three-method interface.
//
// Every scene used to be listed three times: once in renderScene's if-chain
// (core.go), once in the keyboard chain (main.go) and once in
// handleSceneController's switch (controller.go), and its controller handler sat
// far away in controller.go regardless of which file owned the scene. Adding a
// scene meant editing three dispatch tables and hoping none of them was missed.
//
// Now a scene file declares its own module in an init and the three dispatchers
// ask the registry. Scenes that need no special handling (the config-driven
// element screens) simply are not registered and keep the generic path.
//
// The methods are deliberately tiny:
//
//	sceneName          the config name this module answers to
//	render             draw the scene body; false falls through to the
//	                   generic element renderer
//	handleKey          consume a key event; false falls through to generic
//	                   element navigation
//	handleController   consume a controller button; false falls through

import (
	"sort"

	"github.com/veandco/go-sdl2/sdl"
)

type sceneModule interface {
	sceneName() string
	render(renderer *sdl.Renderer, config *Config) bool
	handleKey(e *sdl.KeyboardEvent, config *Config) bool
	handleController(e *sdl.ControllerButtonEvent, config *Config) bool
}

// sceneBase supplies the name and the conservative defaults: do nothing and let
// the generic renderer/navigation handle it.
type sceneBase struct{ name string }

func (b sceneBase) sceneName() string { return b.name }
func (b sceneBase) render(*sdl.Renderer, *Config) bool {
	return false
}
func (b sceneBase) handleKey(*sdl.KeyboardEvent, *Config) bool { return false }
func (b sceneBase) handleController(*sdl.ControllerButtonEvent, *Config) bool {
	return false
}

var sceneRegistry = map[string]sceneModule{}

// registerScene adds a module. A duplicate name is a programming error, but it
// must not take the handheld down at startup: the first registration wins and
// the conflict is logged loudly.
func registerScene(m sceneModule) {
	if m == nil || m.sceneName() == "" {
		LogScene("scene").Error("ignoring scene module with no name")
		return
	}
	if _, exists := sceneRegistry[m.sceneName()]; exists {
		LogScene("scene").Error("duplicate scene registration, keeping the first",
			"scene", m.sceneName())
		return
	}
	sceneRegistry[m.sceneName()] = m
}

// lookupScene returns the module registered for a config scene name.
func lookupScene(name string) (sceneModule, bool) {
	m, ok := sceneRegistry[name]
	return m, ok
}

// registeredSceneNames is the sorted list of scenes that own their behaviour.
// Used by diagnostics and by the registry test.
func registeredSceneNames() []string {
	names := make([]string, 0, len(sceneRegistry))
	for name := range sceneRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// handleSceneController routes a controller button to the scene's module after
// the home layout has had its chance. It replaces the old per-scene switch.
func handleSceneController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	sceneName := config.Scenes[currentSceneIndex].Name

	// The tile home layout owns the D-pad long before any scene does.
	if homeLayoutActive {
		if handleHomeController(e, config) {
			return true
		}
	}

	if m, ok := lookupScene(sceneName); ok {
		return m.handleController(e, config)
	}
	return false
}
