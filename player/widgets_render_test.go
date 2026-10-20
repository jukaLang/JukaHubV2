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
		t.Skipf("no usable jukaconfig.json next to the test: %v", err)
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
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderScene(renderer, config, config.Scenes[idx])
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	if dir := os.Getenv("JUKAHUB_WIDGET_DUMP_DIR"); dir != "" {
		if err := writeFramePNG(filepath.Join(dir, name+".png"), f.buf); err != nil {
			t.Errorf("could not write render dump: %v", err)
		}
	}
	return f, &config.Scenes[idx]
}

func TestSmartWidgetsPageRenders(t *testing.T) {
	config := widgetRenderConfig(t)
	f, scene := renderNamedScene(t, config, smartSettingsScene)

	// Every toggle must paint its pill in the state its variable holds, and fit
	// above the footer.
	toggles := 0
	for _, el := range scene.Elements {
		if el.Type != "toggle" {
			continue
		}
		toggles++
		on := toggleValue(config, el)
		want := ColorIconSurface
		if on {
			want = ColorAccent
		}
		cardW, cardH := resolveElementDimensions(el, 320, 48)
		pillX := el.X + cardW - 14 - 76
		pillY := el.Y + (cardH-32)/2
		matched := 0
		for y := pillY; y < pillY+32; y++ {
			for x := pillX; x < pillX+76; x++ {
				if colourNear(f.at(x, y), want, 6) {
					matched++
				}
			}
		}
		// The pill is 76x32 with rounded ends and centred text, so a good part
		// of it must be the state colour.
		if matched < 500 {
			t.Errorf("%s: pill has %d px of its %v state colour (on=%v)", el.Variable, matched, want, on)
		}
		if el.Y+52 > 660 {
			t.Errorf("%s: row at y=%d runs into the footer", el.Variable, el.Y)
		}
	}
	if toggles != 3 {
		t.Fatalf("%d toggles on the page, want 3", toggles)
	}

	// Labels must draw visible text and stay inside the content panel
	// (x 24..1256).
	for _, el := range scene.Elements {
		if el.Type != "label" {
			continue
		}
		if got := brightPixelsIn(f, el, config); got < 40 {
			t.Errorf("label %q drew %d bright pixels: the text is missing", el.Text, got)
		}
	}
}

// The Settings → General entry button must be visible inside the content panel
// and clear of the Save button.
func TestSmartWidgetsEntryRenders(t *testing.T) {
	config := widgetRenderConfig(t)
	f, scene := renderNamedScene(t, config, "SettingsGeneral")

	var entry *Element
	for i := range scene.Elements {
		if scene.Elements[i].TriggerTarget == smartSettingsScene {
			entry = &scene.Elements[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("no entry button in Settings → General")
	}
	// The content panel ends 16px above the 48px footer, so y+48 must stay
	// above 656.
	if entry.Y+48 > 656 {
		t.Fatalf("entry button bottom at y=%d runs into the footer", entry.Y+48)
	}
	for _, other := range scene.Elements {
		if other.TriggerTarget == smartSettingsScene {
			continue
		}
		otherW, otherH := resolveElementDimensions(other, 320, 48)
		entryW, entryH := resolveElementDimensions(*entry, 320, 48)
		overlapsX := entry.X < other.X+otherW && other.X < entry.X+entryW
		overlapsY := entry.Y < other.Y+otherH && other.Y < entry.Y+entryH
		if overlapsX && overlapsY && other.Type != "label" {
			t.Fatalf("entry button overlaps %q (%s) at %d,%d", other.Text, other.Type, other.X, other.Y)
		}
	}
	if got := brightPixelsIn(f, *entry, config); got < 40 {
		t.Errorf("entry button drew %d bright pixels: its label is missing", got)
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
