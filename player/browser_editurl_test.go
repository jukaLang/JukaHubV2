package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// browserEditURLFixture opens the browser scene and returns the element that
// owns the address bar, its scene index and a test renderer.
func browserEditURLFixture(t *testing.T) (*Config, *sdl.Renderer, Element, int, int) {
	t.Helper()
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	ensureBrowserScene(config)
	elem, ok := findBrowserElement(config)
	if !ok {
		t.Fatal("no browser element")
	}
	renderer := probeRenderer(t)
	currentSceneIndex = findSceneIndex(config, browserSceneName)
	if currentSceneIndex < 0 {
		t.Fatal("no browser scene")
	}
	index := -1
	for i, e := range config.Scenes[currentSceneIndex].Elements {
		if e.Type == "textbrowser" && e.Variable == elem.Variable {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("browser element is not in the scene")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	resetBrowserEditURLState()
	return config, renderer, elem, currentSceneIndex, index
}

func resetBrowserEditURLState() {
	textBrowserAddressEditing = false
	textBrowserAddressSubmitted = false
	vkTextSelected = false
	virtualKeyboardActive = false
	activeSceneIndex = -1
	activeElementIndex = -1
}

// Typing a new address in the bar must replace the current one. It used to be
// appended to it ("https://old/https://new"), which loaded a 404.
func TestBrowserEditURLReplacesTheAddress(t *testing.T) {
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()
	config.Variables.Custom[textBrowserURLVariable] = "https://old.example/"

	sdl.PushEvent(&sdl.TextInputEvent{Type: sdl.TEXTINPUT, Text: [32]byte{
		'h', 't', 't', 'p', 's', ':', '/', '/', 'n', 'e', 'w', '.', 'e', 'x', 'a', 'm', 'p', 'l', 'e', '/',
	}})
	sdl.PushEvent(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}})
	handleTextBrowserAddressInput(renderer, config, elem)

	if got := textBrowserAddress(config); got != "https://new.example/" {
		t.Fatalf("address = %q, want the typed value", got)
	}
}

// The bar opens with the current address selected, so a fresh address can be
// typed straight away, and cancelling leaves the page alone.
func TestBrowserEditURLOpensSelectedAndCancelKeepsPage(t *testing.T) {
	config, renderer, elem, sceneIdx, elemIdx := browserEditURLFixture(t)
	defer resetBrowserEditURLState()
	const page = "https://kept.example/page"
	config.Variables.Custom[textBrowserURLVariable] = page

	textBrowserAddressEditing = true
	vkOpenSceneInput(config, sceneIdx, elemIdx)
	if !vkTextSelected {
		t.Error("the address was not selected when the field opened")
	}
	if got := vkCurrentText(); got != page {
		t.Errorf("field opened with %q, want %q", got, page)
	}
	textBrowserAddressEditing = false
	vkCloseKeyboard()

	sdl.PushEvent(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}})
	handleTextBrowserAddressInput(renderer, config, elem)

	if got := config.Variables.Custom[textBrowserURLVariable]; got != page {
		t.Fatalf("address after cancel = %q, want %q", got, page)
	}
}
