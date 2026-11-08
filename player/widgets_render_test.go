package main

// Offscreen render checks for the injected Smart Widgets page and its entry
// point. They draw the real scenes from the shipped jukaconfig.json with the
// software renderer (SDL dummy video driver, see installbar_render_test.go)
// and inspect the result, so a layout mistake — a row off the bottom of the
// panel, an empty or over-long label, an entry button buried under another
// element — fails instead of shipping.
//
// Setting JUKAHUB_WIDGET_DUMP_DIR=/some/dir writes each rendered scene to
// <dir>/<scene-name>.png.

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// widgetRenderConfig loads the shipped config with the widget scenes injected,
// or skips when the test is not running in the project directory.
func widgetRenderConfig(t *testing.T) *Config {
	t.Helper()
	config, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	if err != nil {
		t.Fatalf("no usable jukaconfig.json next to the test: %v", err)
	}
	ensureSettingsWidgets(config)
	return config
}

// renderNamedScene draws one scene of the config and returns its pixels.
func renderNamedScene(t *testing.T, config *Config, name string) (*probeFrame, *SceneConfig) {
	t.Helper()
	idx := sceneIndexExact(config, name)
	if idx < 0 {
		t.Fatalf("scene %s is missing", name)
	}
	prevScene, prevConfig := currentSceneIndex, appConfig
	currentSceneIndex, appConfig = idx, config
	t.Cleanup(func() { currentSceneIndex, appConfig = prevScene, prevConfig })

	withScreen(t, probeW, probeH)
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderScene(renderer, config, config.Scenes[idx])
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	if dir := os.Getenv("JUKAHUB_WIDGET_DUMP_DIR"); dir != "" {
		if err := writeFramePNG(filepath.Join(dir, name+".png"), f.buf); err != nil {
			t.Errorf("could not write render dump: %v", err)
		}
	}
	return f, &config.Scenes[idx]
}

// TestSmartWidgetsPageRenders: the Smart Widgets page is drawn by the Settings
// hub now (setui.go), which opens SettingsSmart on Power & battery. Its two
// device switches are rows there and its other two moved to the sections they
// belong to; every one of the four draws whole, with its switch showing the
// state its variable holds.
func TestSmartWidgetsPageRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	setSkipUnlessDefaultTextSize(t)
	config := setRenderConfig(t)
	p := setNewHubProbe(t)
	for _, w := range setTestCanvases {
		for _, c := range []struct {
			section, row, variable string
			on                     func(*Config) bool
		}{
			{"power", "batterySaver", "batterySaver", batterySaverEnabled},
			{"power", "lowBattery", "lowBatteryGuard", lowBatteryGuardEnabled},
			{"display", "timeTheme", "timeOfDayTheme", timeOfDayThemeEnabled},
			{"general", "autostart", "autoStart", autoStartEnabled},
		} {
			p.open(config, c.section, w, 720)
			i := p.assertRowWhole(c.row)
			if got, want := setHub.views[i].on, c.on(config); got != want {
				t.Errorf("%dpx %s: the switch shows %v, the setting is %v", w, c.variable, got, want)
			}
		}
	}

	// The old page itself still opens, on the section its switches lead.
	idx := sceneIndexExact(config, smartSettingsScene)
	if idx < 0 {
		t.Fatalf("no %s scene to open", smartSettingsScene)
	}
	p.config, p.scene, p.section = config, idx, "power"
	currentSceneIndex = idx
	setHub = setHubState{viewSection: -1}
	p.draw()
	if got := setSections()[setHub.section].ID; got != "power" {
		t.Fatalf("%s opened the %q section, want power", smartSettingsScene, got)
	}
}

// TestSmartWidgetsEntryRenders: the old pages' "Smart Widgets" entry button is
// not needed any more — the four switches are rows of their sections, each one
// press from the rail — but the scene it opened still answers, so an old
// config's button lands on them rather than on nothing.
func TestSmartWidgetsEntryRenders(t *testing.T) {
	config := widgetRenderConfig(t)
	general := settingsGeneralScene(config)
	if general == nil {
		t.Fatal("no Settings → General scene")
	}
	found := false
	for _, el := range general.Elements {
		if el.TriggerTarget == smartSettingsScene {
			found = true
		}
	}
	if !found {
		t.Fatal("the old page lost its Smart Widgets entry, which old configs still carry")
	}
	if !setIsHubScene(smartSettingsScene) {
		t.Fatalf("%s is not drawn by the Settings hub", smartSettingsScene)
	}
	if setSectionIndex(setLegacySection(smartSettingsScene)) < 0 {
		t.Fatalf("%s does not open a section", smartSettingsScene)
	}
}

// brightPixelsIn counts the near-white text pixels inside an element's box;
// text is drawn in the label colour, which is far brighter than any surface.
func brightPixelsIn(f *probeFrame, el Element, config *Config) int {
	font, _ := getCachedFont(config, el.Font)
	if font == nil {
		return -1
	}
	width, _, _ := font.SizeUTF8(el.Text)
	if el.Type == "button" || el.Type == "toggle" {
		w, _ := resolveElementDimensions(el, 320, 48)
		width = int(w)
	}
	bright := 0
	for y := el.Y; y < el.Y+int32(font.Height()) && y < probeH; y++ {
		for x := el.X; x < el.X+int32(width) && x < probeW; x++ {
			c := f.at(x, y)
			if int(c.R)+int(c.G)+int(c.B) > 420 {
				bright++
			}
		}
	}
	return bright
}

// colourNear reports whether two colours match within tol per channel.
func colourNear(a, b sdl.Color, tol int) bool {
	diff := func(x, y uint8) int {
		d := int(x) - int(y)
		if d < 0 {
			return -d
		}
		return d
	}
	return diff(a.R, b.R) <= tol && diff(a.G, b.G) <= tol && diff(a.B, b.B) <= tol
}

// writeFramePNG converts an ARGB8888 readback buffer (SDL's byte order on
// little-endian hosts is B,G,R,A) into a PNG.
func writeFramePNG(path string, buf []byte) error {
	img := image.NewRGBA(image.Rect(0, 0, int(probeW), int(probeH)))
	for i := 0; i < len(buf)/4; i++ {
		img.Pix[i*4+0] = buf[i*4+2]
		img.Pix[i*4+1] = buf[i*4+1]
		img.Pix[i*4+2] = buf[i*4+0]
		img.Pix[i*4+3] = 255
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}
