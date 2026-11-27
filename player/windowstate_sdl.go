package main

import (
	"fmt"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// windowRectKey is the one Custom key persisted in jukauser.json. It is a
// device fact: bkup.go leaves device keys out of cross-platform backups.
const windowRectKey = "windowrect"

// desktopDisplayBounds reads the monitors from SDL once, at window creation
// or on a window event — never on a frame.
func desktopDisplayBounds() []displayBounds {
	n, err := sdl.GetNumVideoDisplays()
	if err != nil || n <= 0 {
		return nil
	}
	out := make([]displayBounds, 0, n)
	for i := 0; i < n; i++ {
		r, err := sdl.GetDisplayBounds(i)
		if err != nil {
			continue
		}
		out = append(out, displayBounds{X: int(r.X), Y: int(r.Y), W: int(r.W), H: int(r.H)})
	}
	return out
}

// rememberedWindowRect reads and clamps the saved geometry. ok is false when
// there is nothing usable, in which case the caller keeps the config canvas.
func rememberedWindowRect(config *Config) (windowRect, bool) {
	if config == nil {
		return windowRect{}, false
	}
	value, _ := customValue(config.Variables.Custom, windowRectKey)
	text, _ := value.(string)
	r, ok := parseWindowRect(text)
	if !ok {
		return windowRect{}, false
	}
	return clampWindowRect(r, desktopDisplayBounds())
}

// windowBorderlessNow and windowDisplayIndex are the live values for the
// Display settings rows; neither costs anything on a frame.
func windowBorderlessNow() bool {
	return mainWindow != nil && mainWindow.GetFlags()&sdl.WINDOW_BORDERLESS != 0
}

func windowDisplayIndex() int {
	if mainWindow == nil {
		return 0
	}
	if d, err := mainWindow.GetDisplayIndex(); err == nil && d >= 0 {
		return d
	}
	return 0
}

// windowMonitorOptions are the Display row's stops, numbered the way Windows
// numbers monitors in Display settings (one-based).
func windowMonitorOptions(*Config) []setOption {
	displays := desktopDisplayBounds()
	out := make([]setOption, 0, len(displays))
	for i, b := range displays {
		out = append(out, setOption{
			Value: strconv.Itoa(i),
			Label: fmt.Sprintf("%d: %d×%d", i+1, b.W, b.H),
		})
	}
	return out
}

// setWindowBorderless applies the Display row immediately and persists it.
func setWindowBorderless(on bool) {
	if mainWindow == nil || mainWindow.GetFlags()&sdl.WINDOW_FULLSCREEN != 0 {
		return
	}
	mainWindow.SetBordered(!on)
	rememberWindowRect(appConfig, mainWindow)
}

// setWindowMonitor moves the window to the middle of the chosen monitor and
// keeps its size unless that monitor cannot hold it.
func setWindowMonitor(index int) {
	displays := desktopDisplayBounds()
	if mainWindow == nil || index < 0 || index >= len(displays) || mainWindow.GetFlags()&sdl.WINDOW_FULLSCREEN != 0 {
		return
	}
	b := displays[index]
	w, h := mainWindow.GetSize()
	if int(w) > b.W || int(w) < windowMinW {
		w = int32(b.W * 3 / 4)
	}
	if int(h) > b.H || int(h) < windowMinH {
		h = int32(b.H * 3 / 4)
	}
	mainWindow.SetPosition(int32(b.X)+(int32(b.W)-w)/2, int32(b.Y)+(int32(b.H)-h)/2)
	rememberWindowRect(appConfig, mainWindow)
}

// rememberWindowRect saves the window's current geometry, debounced by the
// config's normal save path. Called on move/resize, not per frame.
func rememberWindowRect(config *Config, window *sdl.Window) {
	if config == nil || window == nil || window.GetFlags()&sdl.WINDOW_FULLSCREEN != 0 {
		return
	}
	px, py := window.GetPosition()
	pw, ph := window.GetSize()
	display, err := window.GetDisplayIndex()
	if err != nil || display < 0 {
		display = 0
	}
	flags := window.GetFlags()
	r := windowRect{
		X: int(px), Y: int(py), W: int(pw), H: int(ph), Display: display,
		Maximized: flags&sdl.WINDOW_MAXIMIZED != 0,
		Borderless: flags&sdl.WINDOW_BORDERLESS != 0,
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[windowRectKey] = formatWindowRect(r)
	syncVariableOverrides(config)
}
