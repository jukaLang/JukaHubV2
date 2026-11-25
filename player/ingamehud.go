package main

// ingamehud.go — In-game indicators: volume, brightness, low battery and
// notifications drawn into a running game.
//
// While a game owns the screen JukaHub draws nothing (main.go parks the
// loop), so the volume keys, Menu + volume's brightness and a draining
// battery changed things the player could not see, and a toast waited for the
// game to end. The firmware draws its own bars only over its own menu.
//
// With Settings → Games & emulators → In-game indicators on, a RetroArch or
// PPSSPP game the library starts gets JukaHub's small hook library
// (bin/libjukaosd.so, player/native/ingame_osd.c, built with the Remote Play
// helpers) in LD_PRELOAD. The hook wraps the calls that put a frame on the
// screen — eglSwapBuffers, SDL_GL_SwapWindow and SDL_RenderPresent — and,
// just before the real one, draws one picture over the frame with GLES2 or
// SDL's renderer, restoring every piece of GL state it touched. A game whose
// renderer is none of those (a port with its own path, a Vulkan core) never
// calls the wrapped functions and runs unchanged; nothing else is hooked.
//
// The picture is made here: the parked main loop (10 times a second) reads
// the volume, the brightness and the battery, and when one changes it draws a
// card — the glyph, the words in the interface language and font, a level bar
// — into a small RGBA image, and writes it to a file in /tmp the hook maps
// (ingameHUDPublish). The file is a 64-byte header and the pixels: a
// sequence number written odd before the pixels and even after them, twice,
// and an FNV-1a sum of the pixels, so the hook never uploads a picture half
// written. The hook reads two integers per frame and does nothing else until
// the number changes: no system call, no allocation, no drawing while no card
// shows. A card fades in, stays for the time the header says, and fades out
// on the hook's own clock.
//
// What it shows:
//   - Volume and Brightness: the level in a bar, 1.5 s after each change.
//   - Battery: at 15, 10 and 5 % and every percent under 5, while it drains.
//   - Notifications, with Settings → General → Notifications while playing
//     set to "Over the game" (notifications_actions.go): each toast, 3 s.
//
// The in-game menu's Performance row (gmPerf, below) is the other half of the
// task: the CPU profile of the game that is running, changed while it runs and
// kept as that game's own (gameoptions.go CPUProfile).

import (
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// ingameHUDKey is the setting (off unless true).
	ingameHUDKey = "ingameHUD"
	// ingameHUDLibName is the hook library in JukaHub's bin/ folder.
	ingameHUDLibName = "libjukaosd.so"
	// ingameHUDEnv names the shared file for the hook.
	ingameHUDEnv = "JUKAHUB_OSD"
	// The file: a header, then up to ingameHUDMaxW × ingameHUDMaxH RGBA
	// pixels (straight alpha, rows top to bottom).
	ingameHUDMagic   = "JKOSD01\x00"
	ingameHUDHeader  = 64
	ingameHUDMaxW    = 640
	ingameHUDMaxH    = 160
	ingameHUDDesignH = 720 // the canvas height a card is drawn for
	// How long each kind of card stays.
	ingameHUDLevelMS   = 1500
	ingameHUDBatteryMS = 4500
	ingameHUDToastMS   = 3000
	// ingameHUDBatteryEvery is how often the battery is read under a game.
	ingameHUDBatteryEvery = 5 * time.Second
)

// ingameHUDEnabled reports the setting.
func ingameHUDEnabled(c *Config) bool {
	if c == nil || !gameAssistSupported() {
		return false
	}
	return customBool(c.Variables.Custom, ingameHUDKey, false)
}

// ingameHUDLibPath is the hook library, or "" when this JukaHub has none.
func ingameHUDLibPath() string {
	dir := psBinDir()
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, ingameHUDLibName)
	if !regularFile(p) {
		return ""
	}
	return p
}

// ingameHUDSettingsRow is Settings → Games & emulators → In-game indicators.
func ingameHUDSettingsRow() setRow {
	return setRow{
		ID: ingameHUDKey, Label: "In-game indicators", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if ingameHUDLibPath() == "" {
				return "Comes with the next JukaHub package (bin/" + ingameHUDLibName + ")"
			}
			if ingameHUDEnabled(c) {
				return "Volume, brightness, low battery and notifications over RetroArch and PPSSPP games"
			}
			return "Off: the game's picture is left alone"
		},
		Visible: func(*Config) bool { return gameAssistSupported() },
		Get:     ingameHUDEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, ingameHUDKey, on)
			msg := "In-game indicators: " + setOnOff(on)
			if on && ingameHUDLibPath() == "" {
				msg += " · it needs the next JukaHub package"
			}
			setToast(msg)
		},
	}
}

// ingameHUDTarget reports whether a launch gets the hook: RetroArch, and the
// standalone PPSSPP (the managed one, a launcher named for it, a PSP game).
func ingameHUDTarget(launcher, system string) bool {
	if retroArchLauncher(launcher) || ppssppIsManagedLauncher(launcher) {
		return true
	}
	if strings.Contains(strings.ToLower(filepath.ToSlash(launcher)), "ppsspp") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(system), "psp")
}

// ingameHUD is the running game's session. The toast list is filled from
// any goroutine under mu; everything else is the main thread's.
var ingameHUD struct {
	mu     sync.Mutex
	toasts []ingameHUDNote

	file    *os.File
	path    string
	seq     uint32
	pending bool // the launch is prepared; the first tick reads the levels
	active  bool
	// The last readings, and the battery's last announced level.
	vol, bri, bat       int
	volKnown, briKnown  bool
	batAnnounced, batAt int
	batReadAt           time.Time
	// busyUntil: a card is up; a toast waits until then.
	busyUntil time.Time
	// gamePath is the running library game, for the Performance row.
	gamePath string
}

// ingameHUDPrepareLaunch is launchLibraryGame's line, last before the start
// (after the child's environment is settled): the hook and its file.
func ingameHUDPrepareLaunch(launcher, system string, cmd *exec.Cmd) {
	if cmd == nil || IsWindows() || !ingameHUDEnabled(appConfig) || !ingameHUDTarget(launcher, system) {
		return
	}
	lib := ingameHUDLibPath()
	if lib == "" {
		return
	}
	ingameHUDEnd()
	path := filepath.Join(os.TempDir(), "jukahub-osd.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		logWarn("ingamehud", "no indicators for this game: %v", err)
		return
	}
	if err := f.Truncate(ingameHUDHeader + ingameHUDMaxW*ingameHUDMaxH*4); err != nil {
		f.Close()
		logWarn("ingamehud", "no indicators for this game: %v", err)
		return
	}
	hdr := make([]byte, ingameHUDHeader)
	copy(hdr, ingameHUDMagic)
	if _, err := f.WriteAt(hdr, 0); err != nil {
		f.Close()
		return
	}
	ingameHUD.file, ingameHUD.path, ingameHUD.seq = f, path, 0
	ingameHUD.pending, ingameHUD.active = true, false
	ingameHUD.mu.Lock()
	ingameHUD.toasts = nil
	ingameHUD.mu.Unlock()
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	preload := lib
	if old, ok := childEnvLookup(env, "LD_PRELOAD"); ok && strings.TrimSpace(old) != "" && !strings.Contains(old, ingameHUDLibName) {
		preload = lib + ":" + strings.TrimSpace(old)
	}
	env = childEnvWithout(childEnvWithout(env, "LD_PRELOAD"), ingameHUDEnv)
	cmd.Env = append(env, "LD_PRELOAD="+preload, ingameHUDEnv+"="+path)
	logInfo("ingamehud", "indicators drawn into %s (%s)", filepath.Base(launcher), lib)
}

// ingameHUDEnd closes the session: the game has gone. Main thread.
func ingameHUDEnd() {
	if ingameHUD.file != nil {
		ingameHUD.file.Close()
		_ = os.Remove(ingameHUD.path)
	}
	ingameHUD.file, ingameHUD.path = nil, ""
	ingameHUD.pending, ingameHUD.active = false, false
	ingameHUD.mu.Lock()
	ingameHUD.toasts = nil
	ingameHUD.mu.Unlock()
}

func init() {
	gameLaunchRegisterStarted(func(game libraryGame, _ string, _ *exec.Cmd) {
		ingameHUD.gamePath = game.Path
	})
	gameLaunchRegisterExited(func(libraryGame, string, time.Duration, error) {
		tbxPost(func() {
			ingameHUD.gamePath = ""
			ingameHUDEnd()
		})
	})
}

// ingameHUDNote is a notification waiting for the game's screen.
type ingameHUDNote struct {
	msg  string
	kind ToastKind
}

// ingameHUDToast queues a notification for the game's screen. Any goroutine.
func ingameHUDToast(msg string, kind ToastKind) {
	if msg = strings.TrimSpace(msg); msg == "" {
		return
	}
	ingameHUD.mu.Lock()
	if len(ingameHUD.toasts) < 4 {
		ingameHUD.toasts = append(ingameHUD.toasts, ingameHUDNote{msg: msg, kind: kind})
	}
	ingameHUD.mu.Unlock()
}

// ingameHUDLevels are the volume and the brightness as last read, without
// asking for a new reading: the hardware keys refresh them on every press
// (volumeNoteHardwareKey, brightnessNoteHardwareKey) and brightkeys.go sets
// the brightness itself, so nothing polls the mixer under a game.
func ingameHUDLevels() (vol int, volOK bool, bri int, briOK bool) {
	volumeLevel.mu.Lock()
	vol, volOK = volumeLevel.pct, volumeLevel.known
	volumeLevel.mu.Unlock()
	brightnessLevel.mu.Lock()
	bri, briOK = brightnessLevel.pct, brightnessLevel.known
	brightnessLevel.mu.Unlock()
	return
}

// ingameHUDTick runs in the parked main loop while a game owns the screen.
func ingameHUDTick(config *Config) {
	if ingameHUD.file == nil {
		return
	}
	now := time.Now()
	vol, volOK, bri, briOK := ingameHUDLevels()
	if ingameHUD.pending {
		// The levels as the game started: nothing to show for them.
		ingameHUD.pending, ingameHUD.active = false, true
		ingameHUD.vol, ingameHUD.volKnown = vol, volOK
		ingameHUD.bri, ingameHUD.briKnown = bri, briOK
		ingameHUD.bat, ingameHUD.batAnnounced, ingameHUD.batReadAt = getBatteryPercent(), 101, now
		return
	}
	if !ingameHUD.active {
		return
	}
	switch {
	case volOK && ingameHUD.volKnown && vol != ingameHUD.vol:
		label := "Volume " + strconv.Itoa(vol) + "%"
		icon := "volume"
		if vol == 0 {
			label, icon = "Muted", "volume-mute"
		}
		ingameHUDShow(config, icon, label, vol, ingameHUDLevelMS, now)
	case briOK && ingameHUD.briKnown && bri != ingameHUD.bri:
		ingameHUDShow(config, "sun", "Brightness "+strconv.Itoa(bri)+"%", bri, ingameHUDLevelMS, now)
	}
	if volOK {
		ingameHUD.vol, ingameHUD.volKnown = vol, true
	}
	if briOK {
		ingameHUD.bri, ingameHUD.briKnown = bri, true
	}
	if now.Sub(ingameHUD.batReadAt) >= ingameHUDBatteryEvery {
		ingameHUD.batReadAt = now
		if pct := getBatteryPercent(); pct >= 0 {
			if level, ok := ingameHUDBatteryDue(pct, getBatteryCharging(), ingameHUD.batAnnounced); ok {
				ingameHUD.batAnnounced = level
				label := "Battery " + strconv.Itoa(pct) + "%"
				if pct <= 5 {
					label += " · plug in now"
				}
				ingameHUDShow(config, "battery", label, pct, ingameHUDBatteryMS, now)
			}
			if getBatteryCharging() {
				// A charger resets the thresholds: the next drain says them again.
				ingameHUD.batAnnounced = 101
			}
			ingameHUD.bat = pct
		}
	}
	if now.Before(ingameHUD.busyUntil) {
		return
	}
	ingameHUD.mu.Lock()
	var note ingameHUDNote
	if len(ingameHUD.toasts) > 0 {
		note = ingameHUD.toasts[0]
		ingameHUD.toasts = ingameHUD.toasts[1:]
	}
	ingameHUD.mu.Unlock()
	if note.msg != "" {
		icon := "bell"
		if note.kind == ToastKindError || note.kind == ToastKindWarning {
			icon = "warning"
		}
		ingameHUDShow(config, icon, note.msg, -1, ingameHUDToastMS, now)
	}
}

// ingameHUDBatteryDue reports whether pct (draining) has reached a level not
// announced yet: 15, 10 and 5 %, then every percent below 5. It returns the
// level to remember. Pure.
func ingameHUDBatteryDue(pct int, charging bool, announced int) (int, bool) {
	if charging || pct < 0 || pct > 15 {
		return announced, false
	}
	level := 15
	switch {
	case pct <= 5:
		level = pct
	case pct <= 10:
		level = 10
	}
	if level >= announced {
		return announced, false
	}
	return level, true
}

// ingameHUDShow draws a card and hands it to the hook.
func ingameHUDShow(config *Config, icon, text string, level, showMS int, now time.Time) {
	img := ingameHUDCard(config, icon, uiT(text), level)
	if img == nil {
		return
	}
	if ingameHUDPublish(img, uint32(showMS)) {
		ingameHUD.busyUntil = now.Add(time.Duration(showMS) * time.Millisecond)
	}
}

// ingameHUDCard draws a card for the hook: a dark rounded plate, the glyph,
// the words and, for a level, a bar. Sizes are for a 720-pixel-high screen;
// the hook scales to the game's. Main thread (SDL_ttf).
func ingameHUDCard(config *Config, icon, text string, level int) *image.RGBA {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return nil
	}
	const h, pad, glyph, barW, barH = 60, 16, 30, 150, 8
	textImg := ingameHUDText(font, text, ingameHUDMaxW-2*pad-glyph-12-barW-16)
	tw := 0
	if textImg != nil {
		tw = textImg.Rect.Dx()
	}
	w := pad + glyph + 12 + tw + pad
	if level >= 0 {
		w += 16 + barW
	}
	w = min(max(w, 200), ingameHUDMaxW)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	ingameHUDFillRounded(img, image.Rect(0, 0, w, h), h/2, color.NRGBA{R: 14, G: 18, B: 26, A: 226})
	accent := glassAccent()
	ink := color.NRGBA{R: 245, G: 247, B: 252, A: 255}
	// The glyph, from JukaHub's own icon set.
	mask := make([]uint8, glyph*glyph)
	var cv vecCanvas
	var vp vecPath
	iconRasterize(icon, glyph, mask, &cv, &vp)
	ingameHUDBlendMask(img, mask, glyph, glyph, pad, (h-glyph)/2, ink)
	x := pad + glyph + 12
	if textImg != nil {
		ingameHUDBlendImage(img, textImg, x, (h-textImg.Rect.Dy())/2)
		x += tw
	}
	if level >= 0 {
		x += 16
		y := (h - barH) / 2
		ingameHUDFillRounded(img, image.Rect(x, y, x+barW, y+barH), barH/2, color.NRGBA{R: 255, G: 255, B: 255, A: 60})
		if fill := barW * min(max(level, 0), 100) / 100; fill > 0 {
			ingameHUDFillRounded(img, image.Rect(x, y, x+max(fill, barH), y+barH), barH/2,
				color.NRGBA{R: accent.R, G: accent.G, B: accent.B, A: 255})
		}
	}
	return img
}

// ingameHUDText renders one line in the UI font, white, at most maxW wide.
func ingameHUDText(font *ttf.Font, text string, maxW int) *image.NRGBA {
	text = ellipsize(font, text, int32(maxW))
	if strings.TrimSpace(text) == "" {
		return nil
	}
	surf, err := font.RenderUTF8Blended(uiUnmarkTranslated(text), sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if err != nil || surf == nil {
		return nil
	}
	defer surf.Free()
	w, h := int(surf.W), int(surf.H)
	if w <= 0 || h <= 0 {
		return nil
	}
	conv, err := surf.ConvertFormat(sdl.PIXELFORMAT_ARGB8888, 0)
	if err != nil || conv == nil {
		return nil
	}
	defer conv.Free()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	pix, pitch := conv.Pixels(), int(conv.Pitch)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*pitch + x*4
			if i+3 >= len(pix) {
				break
			}
			// ARGB8888 in memory: B, G, R, A.
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = pix[i+2], pix[i+1], pix[i], pix[i+3]
		}
	}
	return out
}

// ingameHUDFillRounded fills r with a rounded rectangle of radius rad in c,
// blended over what is there.
func ingameHUDFillRounded(img *image.RGBA, r image.Rectangle, rad int, c color.NRGBA) {
	r = r.Intersect(img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cov := ingameHUDCornerCover(x-r.Min.X, y-r.Min.Y, r.Dx(), r.Dy(), rad)
			if cov <= 0 {
				continue
			}
			ingameHUDBlendPixel(img, x, y, c, cov)
		}
	}
}

// ingameHUDCornerCover is how much of pixel (x, y) of a w×h rounded rectangle
// is inside it, 0..1, with a one-pixel soft edge at the corners.
func ingameHUDCornerCover(x, y, w, h, rad int) float64 {
	rad = min(rad, min(w, h)/2)
	cx, cy := -1, -1
	switch {
	case x < rad && y < rad:
		cx, cy = rad, rad
	case x >= w-rad && y < rad:
		cx, cy = w-rad-1, rad
	case x < rad && y >= h-rad:
		cx, cy = rad, h-rad-1
	case x >= w-rad && y >= h-rad:
		cx, cy = w-rad-1, h-rad-1
	}
	if cx < 0 {
		return 1
	}
	dx, dy := float64(x-cx), float64(y-cy)
	d := dx*dx + dy*dy
	r := float64(rad)
	switch {
	case d <= (r-0.5)*(r-0.5):
		return 1
	case d >= (r+0.5)*(r+0.5):
		return 0
	}
	// Linear across the one-pixel rim, by distance.
	return min(max((r+0.5)-sqrtApprox(d), 0), 1)
}

// sqrtApprox is a few Newton steps of the square root, for the soft rim only.
func sqrtApprox(v float64) float64 {
	if v <= 0 {
		return 0
	}
	g := v / 2
	for i := 0; i < 6; i++ {
		g = (g + v/g) / 2
	}
	return g
}

// ingameHUDBlendPixel puts c over img's pixel at coverage cov (straight alpha).
func ingameHUDBlendPixel(img *image.RGBA, x, y int, c color.NRGBA, cov float64) {
	o := img.PixOffset(x, y)
	sa := float64(c.A) / 255 * cov
	da := float64(img.Pix[o+3]) / 255
	oa := sa + da*(1-sa)
	if oa <= 0 {
		return
	}
	for i, sc := range [3]uint8{c.R, c.G, c.B} {
		dc := float64(img.Pix[o+i])
		img.Pix[o+i] = uint8(min((float64(sc)*sa+dc*da*(1-sa))/oa, 255))
	}
	img.Pix[o+3] = uint8(min(oa*255+0.5, 255))
}

// ingameHUDBlendMask paints an 8-bit coverage mask in c at (x, y).
func ingameHUDBlendMask(img *image.RGBA, mask []uint8, w, h, x, y int, c color.NRGBA) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			if a := mask[j*w+i]; a > 0 && image.Pt(x+i, y+j).In(img.Rect) {
				ingameHUDBlendPixel(img, x+i, y+j, c, float64(a)/255)
			}
		}
	}
}

// ingameHUDBlendImage puts src over img at (x, y).
func ingameHUDBlendImage(img *image.RGBA, src *image.NRGBA, x, y int) {
	b := src.Rect
	for j := b.Min.Y; j < b.Max.Y; j++ {
		for i := b.Min.X; i < b.Max.X; i++ {
			o := src.PixOffset(i, j)
			if a := src.Pix[o+3]; a > 0 && image.Pt(x+i, y+j).In(img.Rect) {
				c := color.NRGBA{R: src.Pix[o], G: src.Pix[o+1], B: src.Pix[o+2], A: 255}
				ingameHUDBlendPixel(img, x+i, y+j, c, float64(a)/255)
			}
		}
	}
}

// ingameHUDSum is the FNV-1a sum the hook checks the pixels against.
func ingameHUDSum(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// ingameHUDPublish writes a card to the hook's file: the header with an odd
// sequence first, the pixels, then the header with the next even number in
// both places and the pixels' sum.
func ingameHUDPublish(img *image.RGBA, showMS uint32) bool {
	f := ingameHUD.file
	if f == nil || img == nil {
		return false
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 || w > ingameHUDMaxW || h > ingameHUDMaxH || len(img.Pix) < w*h*4 {
		return false
	}
	pix := img.Pix[:w*h*4]
	hdr := make([]byte, ingameHUDHeader)
	copy(hdr, ingameHUDMagic)
	writing := ingameHUD.seq + 1
	binary.LittleEndian.PutUint32(hdr[8:], writing)
	if _, err := f.WriteAt(hdr, 0); err != nil {
		return false
	}
	if _, err := f.WriteAt(pix, ingameHUDHeader); err != nil {
		return false
	}
	done := writing + 1
	binary.LittleEndian.PutUint32(hdr[8:], done)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(w))
	binary.LittleEndian.PutUint32(hdr[16:], uint32(h))
	binary.LittleEndian.PutUint32(hdr[20:], showMS)
	binary.LittleEndian.PutUint32(hdr[24:], 0) // anchor: top centre
	binary.LittleEndian.PutUint32(hdr[28:], ingameHUDDesignH)
	binary.LittleEndian.PutUint32(hdr[32:], ingameHUDSum(pix))
	binary.LittleEndian.PutUint32(hdr[36:], done)
	if _, err := f.WriteAt(hdr, 0); err != nil {
		return false
	}
	ingameHUD.seq = done
	return true
}

// ── The in-game menu's Performance row ────────────────────────────────────

// gmPerf is the row (gamemenu.go numbers its own rows; 100–130 are taken).
const gmPerf gmItem = 140

// gmPerfAvailable reports a running library game on a CPU whose profile can
// be switched, with power profiles on (their end-of-game restore undoes a
// change made here).
func gmPerfAvailable(config *Config) bool {
	return ingameHUD.gamePath != "" && loadPowerProfileConfig(config).Enabled &&
		len(cpuGovernorPaths(cpufreqRoot)) > 0
}

// gmPerfLabel is the row's words.
func gmPerfLabel() (string, string) {
	cur := gameOptionsFor(ingameHUD.gamePath).CPUProfile
	return "Performance: " + gameCPUProfileLabel(cur), "Left / Right change it · kept for this game"
}

// gmPerfStep moves the game's CPU profile one stop and applies it at once;
// the launch's own restore puts the hub's governor and clock back when the
// game ends.
func gmPerfStep(config *Config, delta int) {
	path := ingameHUD.gamePath
	if path == "" {
		return
	}
	opts := updateGameOptions(path, func(o gameOptions) gameOptions {
		o.CPUProfile = cycleGameCPUProfile(o.CPUProfile, delta)
		return o
	})
	cfg := gameLaunchPowerConfig(loadPowerProfileConfig(config), opts)
	profile := powerProfilePerformance
	if cfg.Balanced {
		profile = powerProfileBalanced
	}
	if _, err := applyPowerProfile(profile, cfg); err != nil {
		logWarn("gamemenu", "performance profile: %v", err)
		PlayErrorSound()
		return
	}
	if _, _, err := applyFrequencyCeiling(cfg); err != nil {
		logWarn("gamemenu", "performance profile ceiling: %v", err)
	}
	logInfo("gamemenu", "performance profile for %s: %s", filepath.Base(path), gameCPUProfileLabel(opts.CPUProfile))
	PlayNavSound()
}
