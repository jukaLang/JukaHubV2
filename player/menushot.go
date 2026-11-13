package main

// menushot.go — hold Menu for a screenshot, in JukaHub and in a game.
//
// A tap of the handheld's Menu button opens the voice orb (voiceorb.go); a
// hold of menuShotHoldMs saves the screen instead (Settings → Controls →
// "Hold Menu: screenshot", on unless turned off). With it on, push-to-talk
// (hold Menu to talk) gives way: a tap's orb listens until the speaker stops.
//
// The hold arms it (a light tick of the motor: let go now) and the release
// takes it, so a button, a trigger or a volume key joining Menu at any point
// — an emulator hotkey, the firmware's Menu + volume brightness, which may
// come late in a long hold — means no screenshot.
//
// In JukaHub the orb's Menu reader times the hold (voiceOrbMenuTick) and, on
// the release, asks the frame loop for a screenshot (requestScreenshot), so
// the picture is the frame on screen with no orb or toast in it. Where Menu
// means Back — voice switched off, or a panel the orb cannot open over — the
// Back waits for the release: a tap sends it then (menuShotSendBack), a hold
// is a screenshot.
//
// In a game JukaHub launched, the game assistant's pad watcher
// (gameassist_linux.go) reads Menu beside the game without taking the pad.
// RetroArch and most emulators open their own menu the moment Menu goes down,
// so the picture is read from the framebuffer then (gameShotGrab), before the
// game's menu has been drawn, and saved on the release of an armed hold. The
// PNG is written on a goroutine, the motor says when it is, and the "While
// you were playing" summary counts them on the way back to JukaHub.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// menuShotKey is the Custom setting behind "Hold Menu: screenshot".
	menuShotKey = "menuHoldScreenshot"
	// menuShotHoldMs is how long Menu is held for a screenshot: longer than a
	// tap, and long enough that a Menu + volume brightness chord or an
	// emulator hotkey has its second button down first.
	menuShotHoldMs = 700
	menuShotHold   = menuShotHoldMs * time.Millisecond
	// menuBackWhich marks the Back a Menu tap sends where Menu means Back. It
	// is not a pad, so nothing mistakes it for one; unlike a control
	// profile's presses it is remapped like the physical press it stands for.
	menuBackWhich sdl.JoystickID = -6
)

// menuShotEnabled reports Settings → Controls → "Hold Menu: screenshot".
func menuShotEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, menuShotKey, true)
}

// menuShotSettingsRow is the switch, on a handheld (Settings → Controls,
// under "Menu button: voice": stickcal.go).
func menuShotSettingsRow() setRow {
	return setRow{
		ID: menuShotKey, Label: "Hold Menu: screenshot", Kind: setKindToggle,
		Detail: func(*Config) string {
			if gameAssistSupported() {
				return "Hold Menu anywhere, in a game too, then let go to save the screen"
			}
			return "Hold Menu anywhere, then let go to save the screen"
		},
		Visible: func(*Config) bool { return P().IsHandheld() },
		Get:     menuShotEnabled,
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom[menuShotKey] = on
			saveUserConfigDebounced(userConfigFromVariables(c))
			setToast("Hold Menu: screenshot " + setOnOff(on))
		},
	}
}

// menuShotLegendRow is the controller card's row for it, on a handheld.
func menuShotLegendRow() (FooterHint, bool) {
	if !P().IsHandheld() || !menuShotEnabled(appConfig) {
		return FooterHint{}, false
	}
	if gameAssistSupported() {
		return FooterHint{Button: "Hold Menu", Label: "Screenshot (in a game too)"}, true
	}
	return FooterHint{Button: "Hold Menu", Label: "Screenshot"}, true
}

// menuShotOwnsGuide reports whether a Menu press is read for the hold here:
// a handheld with the switch on, outside the games JukaHub runs itself (the
// arcade, JukaLand and a browser game read their own buttons), on a pad
// whose Menu is a button of its own (voiceOrbPadHasMenu).
func menuShotOwnsGuide(config *Config, which sdl.JoystickID) bool {
	if !P().IsHandheld() || !menuShotEnabled(config) || brwGameActive() {
		return false
	}
	switch tbxCurrentScene(config) {
	case "Games", "JukaLand":
		return false
	}
	return voiceOrbPadHasMenu(which)
}

// menuShotTake is a hold let go in JukaHub: the next frame is saved
// (screenshot.go, which buzzes when the file is written). A toast on screen
// now is put away first, so it is not in the picture; it stays in the
// notification centre. Main thread.
func menuShotTake() {
	logInfo("screenshot", "Menu held: saving the screen")
	currentToastMu.Lock()
	currentToast = ToastState{}
	currentToastMu.Unlock()
	requestScreenshot()
}

// voiceOrbMenuNoteAxis makes a Menu press that L2 or R2 joins a chord, as a
// button joining it does (voiceOrbController): the triggers are axes, which
// the button path never sees. Main thread, every axis event.
func voiceOrbMenuNoteAxis(e *sdl.ControllerAxisEvent) {
	if e == nil || !voiceOrb.menuDown || voiceOrb.menuHeldOpen {
		return
	}
	if (e.Axis == sdl.CONTROLLER_AXIS_TRIGGERLEFT || e.Axis == sdl.CONTROLLER_AXIS_TRIGGERRIGHT) && e.Value > 16384 {
		voiceOrb.menuCombo = true
	}
}

// menuShotSendBack is a Menu tap where Menu means Back: a Back press and
// release through SDL's queue, so the frame loop handles it as it handled
// Menu before. Main thread.
func menuShotSendBack() {
	if err := pushControllerButton(sdl.CONTROLLERBUTTONDOWN, menuBackWhich, sdl.CONTROLLER_BUTTON_BACK, sdl.PRESSED); err != nil {
		logWarn("screenshot", "could not send Menu's Back: %v", err)
		return
	}
	if err := pushControllerButton(sdl.CONTROLLERBUTTONUP, menuBackWhich, sdl.CONTROLLER_BUTTON_BACK, sdl.RELEASED); err != nil {
		logWarn("screenshot", "could not send Menu's Back release: %v", err)
	}
}

// ── What the savers tell the screen ──────────────────────────────────────

// menuShotNotes is what the saving goroutine finished, for the frame loop to
// show (menuShotDrainNotices). A game's shots are told by the "While you were
// playing" summary instead (whileaway.go), one toast for everything.
var menuShotNotes struct {
	mu      sync.Mutex
	message string
	kind    ToastKind
}

// menuShotNotice parks one toast for the next frame. Any goroutine.
func menuShotNotice(message string, kind ToastKind) {
	menuShotNotes.mu.Lock()
	menuShotNotes.message, menuShotNotes.kind = message, kind
	menuShotNotes.mu.Unlock()
}

// menuShotDrainNotices shows what is parked. Render thread, every frame.
func menuShotDrainNotices() {
	menuShotNotes.mu.Lock()
	message, kind := menuShotNotes.message, menuShotNotes.kind
	menuShotNotes.message = ""
	menuShotNotes.mu.Unlock()
	if message != "" {
		ShowToast(message, kind)
	}
}

// gameShotTell reports a game's screenshot: into the "While you were
// playing" summary while the game still has the screen, else (it ended
// while the file was written) as a toast of its own. Any goroutine.
func gameShotTell(path string, err error) {
	kind := awayKindScreenshot
	if err != nil {
		kind = awayKindScreenshotFailed
	}
	if gameScreenSuspended() {
		noteAwayEvent(kind, filepath.Base(path))
		return
	}
	if err != nil {
		menuShotNotice("Game screenshot not saved: "+err.Error(), ToastKindError)
		return
	}
	// Saved: the preview card shows the picture itself (shotpreview.go).
}

// ── A game's screenshot: the framebuffer ─────────────────────────────────

// gameShotLive is what the pad watcher's goroutine needs from the config and
// the renderer, published by the main thread (gameShotSync).
var gameShotLive struct {
	rotate  atomic.Int32 // castRotate: -1 decides from the framebuffer's shape
	canvasW atomic.Int32
	canvasH atomic.Int32
}

// gameShotSync publishes it. Main thread (the parked loop, gaPadSync).
func gameShotSync(config *Config) {
	gameShotLive.rotate.Store(int32(castRotateFor(config)))
	gameShotLive.canvasW.Store(screenWidth)
	gameShotLive.canvasH.Store(screenHeight)
}

// gameShotFrame is one framebuffer picture: the visible rows as the panel
// holds them, and how they are laid out.
type gameShotFrame struct {
	raw  []byte
	geom castFBGeom
}

// gameShotGrab reads the picture on the panel now into buf (grown as needed).
// The pad watcher's goroutine.
func gameShotGrab(buf []byte) (gameShotFrame, error) {
	f, err := os.Open(castFBDevice)
	if err != nil {
		return gameShotFrame{}, err
	}
	defer f.Close()
	geom, err := castProbeFB(f, int(gameShotLive.canvasW.Load()), int(gameShotLive.canvasH.Load()))
	if err != nil {
		return gameShotFrame{}, err
	}
	bpp := geom.BPP / 8
	need := geom.Stride*(geom.H-1) + geom.W*bpp
	if need <= 0 {
		return gameShotFrame{}, errors.New("the framebuffer has no pixels")
	}
	if cap(buf) < need {
		buf = make([]byte, need)
	}
	buf = buf[:need]
	x, y := castFBPan(f, geom)
	n, err := f.ReadAt(buf, int64(y)*int64(geom.Stride)+int64(x*bpp))
	if n < need {
		if n == 0 {
			if err == nil || errors.Is(err, io.EOF) {
				err = errors.New("the framebuffer read returned nothing")
			}
			return gameShotFrame{}, err
		}
		clear(buf[n:])
	}
	return gameShotFrame{raw: buf, geom: geom}, nil
}

// gameShotImage turns a framebuffer picture into an upright RGBA image.
func gameShotImage(fr gameShotFrame, deg, canvasW, canvasH int) (*image.RGBA, error) {
	g := fr.geom
	w, h := g.W, g.H
	if w <= 0 || h <= 0 || len(fr.raw) < g.Stride*(h-1)+w*(g.BPP/8) {
		return nil, errors.New("the picture has no pixels")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if g.BPP == 16 {
		castRGB565ToRGBA(img.Pix, fr.raw, w, h, g.Stride)
	} else {
		lay := g.Layout
		if lay.bpp != 4 {
			lay = castLayoutBGRA
		}
		for y := 0; y < h; y++ {
			row := fr.raw[y*g.Stride : y*g.Stride+w*4]
			out := img.Pix[y*img.Stride : y*img.Stride+w*4]
			for x := 0; x < w; x++ {
				i := x * 4
				out[i] = row[i+lay.r]
				out[i+1] = row[i+lay.g]
				out[i+2] = row[i+lay.b]
				out[i+3] = 255
			}
		}
	}
	if deg < 0 {
		deg = castAutoRotate(w, h, canvasW, canvasH)
	}
	switch deg {
	case 90, 270:
		turned := image.NewRGBA(image.Rect(0, 0, h, w))
		castRotateRGBA(turned, img, deg)
		return turned, nil
	case 180:
		turned := image.NewRGBA(image.Rect(0, 0, w, h))
		castRotateRGBA(turned, img, deg)
		return turned, nil
	}
	return img, nil
}

// gameShotSave writes a game's picture as a PNG and counts it for the toast
// back in JukaHub. Its own goroutine: it owns fr.
func gameShotSave(fr gameShotFrame, at time.Time) {
	var gen uint64 // the preview card's number (shotpreview.go)
	fail := func(err error) {
		shotPreviewFailed(gen)
		logWarn("screenshot", "game screenshot not saved: %v", err)
		playHaptic(hapticErrorPulse)
		gameShotTell("", err)
	}
	defer func() {
		if r := recover(); r != nil {
			fail(fmt.Errorf("%v", r))
		}
	}()
	img, err := gameShotImage(fr, int(gameShotLive.rotate.Load()),
		int(gameShotLive.canvasW.Load()), int(gameShotLive.canvasH.Load()))
	if err != nil {
		fail(err)
		return
	}
	// The preview waits for JukaHub to have the screen again: nothing of
	// JukaHub's is drawn while the game runs, so there is no flash either.
	maxW, maxH := shotThumbLimits(gameShotLive.canvasW.Load(), gameShotLive.canvasH.Load())
	gen = shotPreviewPark(shotThumb(img.Pix, img.Stride, img.Rect.Dx(), img.Rect.Dy(), maxW, maxH))
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		fail(err)
		return
	}
	path, err := screenshotPath("game", at)
	if err == nil {
		err = os.WriteFile(path, buf.Bytes(), 0o644)
	}
	if err != nil {
		fail(err)
		return
	}
	trophyEvent(tkScreenshot)
	playHaptic(hapticSuccessPulse)
	logInfo("screenshot", "saved the game's screen to %s", path)
	shotPreviewSaved(gen, path)
	gameShotTell(path, nil)
}
