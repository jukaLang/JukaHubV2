package main

// browser_devices_render_test.go — the Browser on the two panels it ships on:
// the Smart Pro's 1280x720 and the Brick's 960x720 compact canvas. The scene
// is injected at runtime and never appears in either shipped config, so the
// Brick scene audit cannot see it; these renders are its place in that audit.
// Set JUKAHUB_BROWSER_DUMP=<dir> to write one PNG per canvas for a visual pass.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestBrowserSceneRendersOnBothDevices(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	useTempBrowserCookiePath(t)
	defer func() {
		setBrowserDocument(browserContentVariable, nil)
		resetBrowserCursorForTest()
	}()

	// A page like the ones people actually open: a title, a heading, body
	// text and links for the D-pad to walk.
	doc := parseWebPage("<title>Crash on boot · Issue 12 · JukaHub</title>"+
		"<body><h1>Crash on boot</h1>"+
		"<p>"+strings.Repeat("The launcher dies after the splash when the card is slow. ", 6)+"</p>"+
		"<p><a href=\"/back\">Back to issues</a> — <a href=\"/comment\">Comment</a></p></body>",
		"", "https://github.com/jukaLang/JukaHubV2/issues/12")
	setBrowserDocument(browserContentVariable, doc)

	devices := []struct {
		name  string
		model DeviceModel
		w, h  int32
	}{
		{"smartpro", DeviceTrimuiSmartPro, uiCanvasWideWidth, uiCanvasWideHeight},
		{"brick", DeviceTrimuiBrick, uiCanvasCompactW, uiCanvasCompactH},
	}
	dumpDir := os.Getenv("JUKAHUB_BROWSER_DUMP")
	if dumpDir != "" {
		if err := os.MkdirAll(dumpDir, 0o755); err != nil {
			t.Fatalf("dump dir: %v", err)
		}
	}

	for _, dev := range devices {
		config, err := loadConfig("jukaconfig.json")
		if err != nil {
			t.Fatalf("%s: loadConfig: %v", dev.name, err)
		}
		// The order the app runs in: the device profile scales the shipped
		// config first, then the panel size is known, then the browser scene
		// injects itself against that canvas.
		applyUIProfileForDevice(config, dev.model)
		withScreen(t, dev.w, dev.h)
		ensureBrowserScene(config)
		idx := sceneIndexExact(config, browserSceneName)
		if idx < 0 {
			t.Fatalf("%s: the Browser scene was not injected", dev.name)
		}
		config.Variables.Custom[textBrowserURLVariable] = "https://github.com/jukaLang/JukaHubV2/issues/12"
		config.Variables.Custom[browserContentVariable] = "Crash on boot"

		prevScene, prevConfig := currentSceneIndex, appConfig
		currentSceneIndex, appConfig = idx, config
		frame := newProbeFrame(t)
		renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			currentSceneIndex, appConfig = prevScene, prevConfig
			t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
		}

		renderer.SetDrawColor(ColorBackground.R, ColorBackground.G, ColorBackground.B, 255)
		renderer.Clear()
		renderScene(renderer, config, config.Scenes[idx])
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			destroyTestRenderer(renderer)
			currentSceneIndex, appConfig = prevScene, prevConfig
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
		}

		elem, ok := findBrowserElement(config)
		if !ok {
			destroyTestRenderer(renderer)
			currentSceneIndex, appConfig = prevScene, prevConfig
			t.Fatalf("%s: no web element in the injected scene", dev.name)
		}

		// The toolbar the page chrome draws must be laid out and boxed in:
		// every chip sits inside the panel, and no two chips overlap — the
		// narrow-panel order drops controls rather than stacking them, and
		// that deserves a number at both canvases.
		inner := sdl.Rect{X: elem.X, Y: elem.Y, W: getElementWidth(elem, 0), H: getElementHeight(elem, 0)}
		type named struct {
			name string
			r    sdl.Rect
		}
		address, mode, search, goBtn, filter, zoom := textBrowserToolbarLayout(config, elem)
		toolbar := []named{
			{"address", address}, {"search", search}, {"go", goBtn},
			{"filter", filter}, {"mode", mode}, {"zoom", zoom},
		}
		for _, chip := range toolbar {
			if chip.r.W <= 0 {
				continue // dropped for this canvas; another way in exists
			}
			if chip.r.X < inner.X || chip.r.Y < inner.Y ||
				chip.r.X+chip.r.W > inner.X+inner.W || chip.r.Y+chip.r.H > inner.Y+inner.H {
				t.Errorf("%s: toolbar %s (%d,%d %dx%d) escapes the panel (%d,%d %dx%d)",
					dev.name, chip.name, chip.r.X, chip.r.Y, chip.r.W, chip.r.H,
					inner.X, inner.Y, inner.W, inner.H)
			}
		}
		for i := range toolbar {
			for j := i + 1; j < len(toolbar); j++ {
				a, b := toolbar[i].r, toolbar[j].r
				if a.W <= 0 || b.W <= 0 {
					continue
				}
				if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
					t.Errorf("%s: toolbar %s and %s overlap", dev.name, toolbar[i].name, toolbar[j].name)
				}
			}
		}

		// And every visible chip's label fits inside its own chip. This is the
		// defect the measured layout exists for: the old fixed 84/80px chips
		// let "SEARCH" (97px) and "SCROLL" (94px) spill past their border on
		// the Smart Pro. "SCROLL" is the widest word the mode chip can say,
		// so checking it covers "LINKS" too.
		if labelFont, _ := getCachedFont(config, "small"); labelFont != nil {
			for _, c := range []struct {
				name  string
				r     sdl.Rect
				label string
			}{
				{"search", search, "SEARCH"},
				{"go", goBtn, "GO"},
				{"filter", filter, browserAdBlockLabel(browserAdBlockViewFor(elem.Variable))},
				{"mode", mode, "SCROLL"},
			} {
				if c.r.W <= 0 {
					continue
				}
				tw, _, err := labelFont.SizeUTF8(c.label)
				if err == nil && int32(tw) > c.r.W {
					t.Errorf("%s: %s label %q is %dpx in a %dpx chip", dev.name, c.name, c.label, tw, c.r.W)
				}
			}
		}

		// The touch audit (MODERNIZATION, Phase 8): each chip's tap target
		// grows to the handheld minimum without its drawn geometry moving.
		for _, c := range []struct {
			name string
			r    sdl.Rect
		}{
			{"address", address}, {"search", search}, {"go", goBtn},
			{"filter", filter}, {"mode", mode},
		} {
			hit := textBrowserTouchTarget(c.r)
			if hit.H < textBrowserTouchMin {
				t.Errorf("%s: %s tap target is %dpx tall, want >= %d", dev.name, c.name, hit.H, textBrowserTouchMin)
			}
			if hit.X != c.r.X || hit.W != c.r.W {
				t.Errorf("%s: %s tap target changed the drawn width/position: %+v vs %+v", dev.name, c.name, hit, c.r)
			}
		}
		if browserCookieStripHit.W > 0 {
			if hit := textBrowserTouchTarget(browserCookieStripHit); hit.H < textBrowserTouchMin {
				t.Errorf("%s: cookie strip tap target is %dpx tall, want >= %d", dev.name, hit.H, textBrowserTouchMin)
			}
		}

		// Both bands the chrome owns — the address row and the chip row —
		// must actually paint, not just lay out.
		for _, band := range []struct {
			name           string
			x0, y0, x1, y1 int32
		}{
			{"toolbar", address.X, address.Y, address.X + address.W, address.Y + address.H},
			{"chip row", 24, 584, dev.w - 24, 628},
		} {
			painted := 0
			for y := band.y0; y < band.y1 && y < probeH; y += 3 {
				for x := band.x0; x < band.x1 && x < probeW; x += 3 {
					i := (int(probeW)*int(y) + int(x)) * 4
					if frame.buf[i] != ColorBackground.B || frame.buf[i+1] != ColorBackground.G || frame.buf[i+2] != ColorBackground.R {
						painted++
					}
				}
			}
			if painted == 0 {
				t.Errorf("%s: the %s drew nothing on screen", dev.name, band.name)
			}
		}

		if dumpDir != "" {
			path := filepath.Join(dumpDir, "browser_"+dev.name+".png")
			if err := saveCroppedFrame(path, frame.buf, dev.w, dev.h); err != nil {
				t.Errorf("%s: write dump: %v", dev.name, err)
			} else {
				t.Log(path)
			}
		}

		destroyTestRenderer(renderer)
		currentSceneIndex, appConfig = prevScene, prevConfig
	}
}

// saveCroppedFrame writes the drawn w×h corner of the probe buffer as PNG. The
// probe buffer is always probeW wide (so a compact canvas renders into its
// top-left corner), which is why a crop, not writeFramePNG, is used here.
func saveCroppedFrame(path string, buf []byte, w, h int32) error {
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := int32(0); y < h; y++ {
		for x := int32(0); x < w; x++ {
			i := (int(probeW)*int(y) + int(x)) * 4
			img.Set(int(x), int(y), color.RGBA{R: buf[i+2], G: buf[i+1], B: buf[i], A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}
