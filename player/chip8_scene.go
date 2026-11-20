package main

// chip8_scene.go — the CHIP-8 player: a full-screen scene that runs the
// machine (chip8_vm.go) a frame at a time and draws its screen.
//
//   - The picture is the machine's 64×32 or 128×64 screen in the game's
//     colours, scaled by the largest whole number that fits and centred, so
//     every pixel is the same size. Octo's screenRotation turns it. The
//     bands around it take the game's background, or — for a game whose
//     options set buzzColor — Octo's visual buzzer colours.
//   - Time is the monotonic clock: each drawn frame runs as many 60 Hz
//     machine frames as have passed (at most four, so a hitch never turns
//     into a fast-forward). The scene asks for 60 frames a second while it
//     runs and nothing while the menu is up.
//   - The keypad is read every frame, not from press events, so a held key
//     stays held: the pad through the same A/B swap and remap JukaLand reads
//     (jlandPadHeld), the left stick as the D-pad, and on a keyboard Octo's
//     layout (1 2 3 4 / Q W E R / A S D F / Z X C V is the hex pad) plus the
//     arrows, Enter/Space and Backspace for the pad's D-pad, A and B. The pad
//     follows Octo's convention — the D-pad is 5 7 8 9 (W A S D), A is 6 (E),
//     B is 4 (Q) — changed per game on the menu's Keys page. Holding Y (Tab)
//     shows a 4×4 keypad for games that want other keys: the D-pad picks a
//     key, A presses it.
//   - Start, Select (the Menu key on the handheld) or Esc pause the game and
//     open the menu: Resume, Reset, Speed, Mode, Keys, Sound, Quick save,
//     Quick load, Quit. A game that stops (it ran 00FD, or hit something no
//     CHIP-8 does) opens it too, with the reason. Speed and Mode apply at
//     once and are kept for the game; a quick save is the whole machine in
//     data/chip8/states.
//   - Sound: CHIP-8's buzzer and XO-CHIP's 1-bit pattern at its pitch, on a
//     queue device of the player's own that a goroutine fills the way the
//     Synth's does (synth.go). It opens on the first beep and closes a moment
//     after the game pauses, is left or stays silent, so a paused game makes
//     no sound and costs nothing.
//   - Leaving the scene any way pauses the game; its time is added to the
//     game's play time then. Quit goes back to where the game was started
//     from, the CHIP-8 shelf when that was the library.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// chip8SceneName is the player's scene (in memory, no icon: the Start
	// menu and search do not list it).
	chip8SceneName = "CHIP8"
	// chip8FrameTime is one machine frame.
	chip8FrameTime = time.Second / 60
	// chip8HintFor is how long the controls line shows after a start.
	chip8HintFor = 4 * time.Second
	// chip8CoverAfter is how long a game without a cover plays before its
	// screen becomes one.
	chip8CoverAfter = 12 * time.Second
	// chip8FlagsEvery is the least time between two keeps of the RPL flags.
	chip8FlagsEvery = 3 * time.Second
)

// chip8Speeds are the Speed row's steps (instructions per frame).
var chip8Speeds = []int{7, 10, 15, 20, 30, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

// chip8Session is the game in the player. Main thread only.
type chip8Session struct {
	ld       chip8Loaded
	vm       *chip8VM
	opts     chip8Options
	keymap   map[string]int
	settings chip8GameSettings
	// last is the monotonic time of the last stepped frame (zero after a
	// pause, so a resume does not catch up), acc the time not yet run.
	last time.Time
	acc  time.Duration
	// startedAt is when the game started (for the hint and the cover);
	// played is the unpaused time since then.
	startedAt time.Time
	played    time.Duration
	// segStart is when this visit to the scene began, for play time;
	// firstSeg is true until the visit that began with the launch ends.
	segStart time.Time
	firstSeg bool
	// fromLibrary: Quit reopens the library on the CHIP-8 shelf.
	fromLibrary bool
	coverPath   string
	coverDone   bool
	stopShown   bool
	keys        [16]bool
	keypadOn    bool
	keypadSel   int
	// flagsPending: the game changed its RPL flags since they were last kept
	// (flagsKeptAt); chip8KeepFlags writes them.
	flagsPending bool
	flagsKeptAt  time.Time
}

var (
	chip8Cur *chip8Session
	// chip8Visit notices each arrival at the scene; chip8VisitGen tells a
	// leave from an older visit apart.
	chip8Visit    tbxVisit
	chip8VisitGen int
)

// chip8Menu is the pause menu. Main thread only.
var chip8Menu struct {
	open   bool
	page   int // 0 the menu, 1 the Keys page
	cursor int
	note   string // why the game stopped, shown at the top
}

const (
	chip8PageMain = iota
	chip8PageKeys
)

// The main page's rows.
const (
	chip8RowResume = iota
	chip8RowReset
	chip8RowSpeed
	chip8RowMode
	chip8RowKeys
	chip8RowSound
	chip8RowSave
	chip8RowLoad
	chip8RowQuit
	chip8RowCount
)

// chip8MapButtons are the pad buttons the Keys page maps (Y is the keypad's).
var chip8MapButtons = [...]string{"up", "down", "left", "right", "a", "b", "x", "l1", "r1"}

// chip8ButtonLabels name them on the page.
var chip8ButtonLabels = map[string]string{
	"up": "D-Pad Up", "down": "D-Pad Down", "left": "D-Pad Left", "right": "D-Pad Right",
	"a": "A", "b": "B", "x": "X", "l1": "L1", "r1": "R1",
}

type chip8SceneModule struct{ sceneBase }

func init() {
	registerScene(chip8SceneModule{sceneBase{name: chip8SceneName}})
}

// chip8EnsureScene gives the player and the shelf their scenes, in memory,
// like the Get Homebrew store's (homebrewEnsureScene).
func chip8EnsureScene(config *Config) {
	if config == nil {
		return
	}
	for _, name := range []string{chip8SceneName, chip8ShelfSceneName} {
		if sceneIndexExact(config, name) < 0 {
			config.Scenes = append(config.Scenes, SceneConfig{Name: name})
		}
	}
	for i := range config.Scenes {
		switch config.Scenes[i].Name {
		case chip8SceneName, chip8ShelfSceneName:
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// chip8SceneOpen starts a loaded game in the player. Main thread.
func chip8SceneOpen(config *Config, ld chip8Loaded) {
	if config == nil {
		return
	}
	chip8EnsureScene(config)
	idx := sceneIndexExact(config, chip8SceneName)
	if idx < 0 {
		ShowToast("The CHIP-8 player is unavailable in this configuration", ToastKindWarning)
		return
	}
	if chip8Cur != nil {
		chip8SessionEnd(chip8Cur)
	}
	s := &chip8Session{ld: ld, settings: ld.settings, firstSeg: true, coverPath: ld.coverPath}
	s.opts = chip8EffectiveOptions(ld.base, s.settings)
	s.keymap = chip8KeyMap(s.opts, s.settings)
	s.vm = newChip8VM(ld.rom, s.opts.Quirks, uint32(time.Now().UnixNano()))
	s.vm.xo = s.opts.Platform == chip8PlatXO
	copy(s.vm.flags[:], s.settings.Flags)
	s.startedAt = time.Now()
	s.segStart = s.startedAt
	s.fromLibrary = gameLib.open
	chip8Cur = s
	chip8Menu.open = false
	logInfo("chip8", "playing %s as %s, %d per frame (%s)", filepath.Base(ld.game.Path), s.opts.Platform.label(), s.opts.Tickrate, s.opts.Source)
	if gameLib.open {
		closeGameLibraryPanel()
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	} else {
		chip8Visit = tbxVisit{} // already here: the next frame starts a visit
	}
}

// chip8SessionEnd settles a game that is being replaced or quit: its RPL
// flags not yet kept and its play time so far. Main thread.
func chip8SessionEnd(s *chip8Session) {
	if s != nil && s.flagsPending {
		chip8KeepFlags(s)
	}
	if s == nil || s.segStart.IsZero() {
		return
	}
	first := time.Time{}
	if s.firstSeg {
		first = s.ld.playedAt
	}
	chip8CreditPlay(s.ld.game, int64(time.Since(s.segStart)/time.Second), first)
	s.segStart = time.Time{}
	s.firstSeg = false
}

// chip8SceneLeft runs when the scene is left any way: the game pauses, its
// time is credited, its picture is let go. Main thread (posted by the
// visit's watcher).
func chip8SceneLeft(gen int) {
	if gen != chip8VisitGen {
		return
	}
	chip8TexRelease()
	s := chip8Cur
	if s == nil {
		return
	}
	chip8SessionEnd(s)
	s.last = time.Time{}
	if !chip8Menu.open {
		// Paused for the return, without the menu's click: the screen the
		// player went to is already up.
		chip8Menu.open, chip8Menu.page, chip8Menu.cursor, chip8Menu.note = true, chip8PageMain, chip8RowResume, ""
		if s.vm.halted || s.vm.exited {
			chip8Menu.cursor = chip8RowReset
		}
	}
}

func (chip8SceneModule) render(r *sdl.Renderer, c *Config) bool {
	chip8Render(r, c)
	return true
}

func (chip8SceneModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	s := chip8Cur
	if s == nil {
		if e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_BACKSPACE {
			goBackScene(c)
		}
		return true
	}
	if chip8Menu.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			chip8MenuMove(-1)
		case sdl.K_DOWN:
			chip8MenuMove(1)
		case sdl.K_LEFT:
			chip8MenuAdjust(c, -1)
		case sdl.K_RIGHT:
			chip8MenuAdjust(c, 1)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			chip8MenuActivate(c)
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			chip8MenuBack()
		}
		return true
	}
	if s.keypadOn {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			chip8KeypadMove(0, -1)
		case sdl.K_DOWN:
			chip8KeypadMove(0, 1)
		case sdl.K_LEFT:
			chip8KeypadMove(-1, 0)
		case sdl.K_RIGHT:
			chip8KeypadMove(1, 0)
		}
	}
	if e.Keysym.Sym == sdl.K_ESCAPE && e.Repeat == 0 {
		chip8OpenMenu("")
	}
	// Every other key is the game's (read by chip8PollKeys); none may switch
	// scenes or go back.
	return true
}

func (chip8SceneModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	s := chip8Cur
	if s == nil {
		if e.Button == sdl.CONTROLLER_BUTTON_B || e.Button == sdl.CONTROLLER_BUTTON_BACK {
			goBackScene(c)
		}
		return true
	}
	if chip8Menu.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			chip8MenuMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			chip8MenuMove(1)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			chip8MenuAdjust(c, -1)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			chip8MenuAdjust(c, 1)
		case sdl.CONTROLLER_BUTTON_A:
			chip8MenuActivate(c)
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_BACK:
			chip8MenuBack()
		}
		return true
	}
	if s.keypadOn {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			chip8KeypadMove(0, -1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			chip8KeypadMove(0, 1)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			chip8KeypadMove(-1, 0)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			chip8KeypadMove(1, 0)
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_GUIDE:
		chip8OpenMenu("")
	}
	return true
}

// ── Running ─────────────────────────────────────────────────────────────────

// chip8Render is the scene's frame: run the machine, then draw it and
// whatever is over it.
func chip8Render(r *sdl.Renderer, c *Config) {
	if chip8Visit.begin() {
		chip8VisitGen++
		gen := chip8VisitGen
		runSceneTask(func(ctx context.Context) {
			<-ctx.Done()
			tbxPost(func() { chip8SceneLeft(gen) })
		})
		if s := chip8Cur; s != nil && s.segStart.IsZero() {
			s.segStart = time.Now() // back to a paused game
		}
	}
	s := chip8Cur
	_ = r.SetDrawColor(0, 0, 0, 255)
	_ = r.Clear()
	if s == nil {
		chip8DrawEmpty(r, c)
		return
	}
	paused := chip8Menu.open || jlandInputBlocked()
	if paused {
		s.last = time.Time{}
		s.keys = [16]bool{}
		// No machine: a paused game is not "seen", so the sound goroutine
		// closes its device a moment later instead of waiting out the silence.
		chip8AudioSet(false, nil)
	} else {
		chip8PollKeys(c, s)
		s.vm.setKeys(s.keys)
		chip8Step(s)
		chip8AudioSet(s.vm.soundOn() && !s.settings.Mute, s.vm)
		requestFrameBurst()
		// The RPL flags are kept at most every few seconds: a game may save
		// them every frame, and each keep is a whole games.json write.
		if s.vm.flagsDirty {
			s.vm.flagsDirty = false
			s.flagsPending = true
		}
		if s.flagsPending && time.Since(s.flagsKeptAt) >= chip8FlagsEvery {
			chip8KeepFlags(s)
		}
		if (s.vm.halted || s.vm.exited) && !s.stopShown {
			s.stopShown = true
			note := s.vm.why
			if s.vm.exited {
				note = "The game ended."
			}
			logInfo("chip8", "%s stopped: %s", filepath.Base(s.ld.game.Path), note)
			chip8OpenMenu(note)
		}
		chip8MaybeCover(s)
	}
	chip8DrawGame(r, s)
	if s.keypadOn && !chip8Menu.open {
		chip8DrawKeypad(r, c, s)
	} else if !chip8Menu.open && time.Since(s.startedAt) < chip8HintFor {
		chip8DrawHint(r, c, hintLine(FooterHint{Button: "Start / Esc", Label: "Menu"}, FooterHint{Button: "Hold Y / Tab", Label: "Keypad"}))
	}
	if chip8Menu.open {
		chip8DrawMenu(r, c, s)
	}
}

// chip8KeepFlags writes the machine's RPL flags into the game's settings.
func chip8KeepFlags(s *chip8Session) {
	s.flagsPending = false
	s.flagsKeptAt = time.Now()
	flags := append([]byte(nil), s.vm.flags[:]...)
	title := s.ld.game.Label()
	chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) {
		g.Flags = flags
		g.Title = title
	})
}

// chip8Step runs the machine frames that are due.
func chip8Step(s *chip8Session) {
	now := time.Now()
	if s.last.IsZero() {
		s.last = now
		return
	}
	dt := now.Sub(s.last)
	s.last = now
	if dt <= 0 {
		return
	}
	if dt > 100*time.Millisecond {
		dt = 100 * time.Millisecond // a hitch is not a fast-forward
	}
	s.played += dt
	s.acc += dt
	for steps := 0; s.acc >= chip8FrameTime && steps < 4; steps++ {
		s.vm.runFrame(s.opts.Tickrate)
		s.acc -= chip8FrameTime
	}
	if s.acc > 4*chip8FrameTime {
		s.acc = 0
	}
}

// chip8KeyboardHex is Octo's keyboard layout: the scancode of each hex key.
var chip8KeyboardHex = [16]sdl.Scancode{
	sdl.SCANCODE_X, sdl.SCANCODE_1, sdl.SCANCODE_2, sdl.SCANCODE_3,
	sdl.SCANCODE_Q, sdl.SCANCODE_W, sdl.SCANCODE_E, sdl.SCANCODE_A,
	sdl.SCANCODE_S, sdl.SCANCODE_D, sdl.SCANCODE_Z, sdl.SCANCODE_C,
	sdl.SCANCODE_4, sdl.SCANCODE_R, sdl.SCANCODE_F, sdl.SCANCODE_V,
}

// chip8PollKeys reads the keypad as it is held this frame.
func chip8PollKeys(config *Config, s *chip8Session) {
	// held is indexed like chip8KeyNames: up down left right a b x y l1 r1.
	const (
		up = iota
		down
		left
		right
		btnA
		btnB
		btnX
		_
		btnL1
		btnR1
	)
	var keys [16]bool
	var held [len(chip8KeyNames)]bool
	ks := sdl.GetKeyboardState()
	kb := func(sc sdl.Scancode) bool { return int(sc) < len(ks) && ks[sc] != 0 }
	for k, sc := range chip8KeyboardHex {
		if kb(sc) {
			keys[k] = true
		}
	}
	held[up] = kb(sdl.SCANCODE_UP)
	held[down] = kb(sdl.SCANCODE_DOWN)
	held[left] = kb(sdl.SCANCODE_LEFT)
	held[right] = kb(sdl.SCANCODE_RIGHT)
	held[btnA] = kb(sdl.SCANCODE_RETURN) || kb(sdl.SCANCODE_SPACE)
	held[btnB] = kb(sdl.SCANCODE_BACKSPACE)
	keypad := kb(sdl.SCANCODE_TAB)
	if pad := gameController; pad != nil {
		b := jlandPadHeld(config, pad)
		held[up] = held[up] || b[sdl.CONTROLLER_BUTTON_DPAD_UP]
		held[down] = held[down] || b[sdl.CONTROLLER_BUTTON_DPAD_DOWN]
		held[left] = held[left] || b[sdl.CONTROLLER_BUTTON_DPAD_LEFT]
		held[right] = held[right] || b[sdl.CONTROLLER_BUTTON_DPAD_RIGHT]
		held[btnA] = held[btnA] || b[sdl.CONTROLLER_BUTTON_A]
		held[btnB] = held[btnB] || b[sdl.CONTROLLER_BUTTON_B]
		held[btnX] = b[sdl.CONTROLLER_BUTTON_X]
		held[btnL1] = b[sdl.CONTROLLER_BUTTON_LEFTSHOULDER]
		held[btnR1] = b[sdl.CONTROLLER_BUTTON_RIGHTSHOULDER]
		keypad = keypad || b[sdl.CONTROLLER_BUTTON_Y]
		const dead = 16000
		if ax := pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_LEFTX)); ax < -dead {
			held[left] = true
		} else if ax > dead {
			held[right] = true
		}
		if ay := pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_LEFTY)); ay < -dead {
			held[up] = true
		} else if ay > dead {
			held[down] = true
		}
	}
	s.keypadOn = keypad
	if keypad {
		// The keypad owns the D-pad and A while it is up: A presses the key
		// it points at.
		if held[btnA] {
			keys[s.keypadSel&15] = true
		}
		s.keys = keys
		return
	}
	for i, on := range held {
		if !on {
			continue
		}
		if k, ok := s.keymap[chip8KeyNames[i]]; ok && k >= 0 && k < 16 {
			keys[k] = true
		}
	}
	s.keys = keys
}

// chip8KeypadOrder is the on-screen keypad's layout, the VIP's: 1 2 3 C /
// 4 5 6 D / 7 8 9 E / A 0 B F.
var chip8KeypadOrder = [16]int{0x1, 0x2, 0x3, 0xC, 0x4, 0x5, 0x6, 0xD, 0x7, 0x8, 0x9, 0xE, 0xA, 0x0, 0xB, 0xF}

// chip8KeypadMove moves the keypad's cursor (keypadSel is a hex key; the
// cursor walks the grid).
func chip8KeypadMove(dx, dy int) {
	s := chip8Cur
	if s == nil {
		return
	}
	pos := 0
	for i, k := range chip8KeypadOrder {
		if k == s.keypadSel {
			pos = i
		}
	}
	col, row := pos%4, pos/4
	col = (col + dx + 4) % 4
	row = (row + dy + 4) % 4
	s.keypadSel = chip8KeypadOrder[row*4+col]
	PlayNavSound()
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// chip8Tex is the screen's texture, 128×64 whatever the resolution.
var chip8Tex struct {
	tex      *sdl.Texture
	renderer *sdl.Renderer
	pix      []byte
	colors   [chip8ColCount]uint32
	valid    bool
}

// chip8TexRelease frees the texture (it belongs to the renderer).
func chip8TexRelease() {
	if chip8Tex.tex != nil {
		_ = chip8Tex.tex.Destroy()
	}
	chip8Tex.tex = nil
	chip8Tex.renderer = nil
	chip8Tex.valid = false
}

// chip8Upload brings the texture up to date with the machine's screen.
func chip8Upload(r *sdl.Renderer, vm *chip8VM, colors [chip8ColCount]uint32) *sdl.Texture {
	if chip8Tex.tex != nil && chip8Tex.renderer != r {
		// A new renderer: the old texture went with the old one.
		chip8Tex.tex, chip8Tex.valid = nil, false
	}
	if chip8Tex.tex == nil {
		prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "0")
		tex, err := r.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STREAMING, chip8MaxW, chip8MaxH)
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
		if err != nil || tex == nil {
			return nil
		}
		chip8Tex.tex, chip8Tex.renderer, chip8Tex.valid = tex, r, false
		if len(chip8Tex.pix) != chip8MaxW*chip8MaxH*4 {
			chip8Tex.pix = make([]byte, chip8MaxW*chip8MaxH*4)
		}
	}
	if chip8Tex.valid && !vm.dirty && chip8Tex.colors == colors {
		return chip8Tex.tex
	}
	w, h := vm.size()
	var pal [4][3]byte
	for i, ci := range [4]int{chip8ColBG, chip8ColFill, chip8ColFill2, chip8ColBlend} {
		c := colors[ci]
		pal[i] = [3]byte{byte(c >> 16), byte(c >> 8), byte(c)}
	}
	pix := chip8Tex.pix
	for y := range h {
		row := y * chip8MaxW * 4
		for x := range w {
			p := pal[vm.pixel(x, y)]
			o := row + x*4
			pix[o], pix[o+1], pix[o+2], pix[o+3] = p[0], p[1], p[2], 255
		}
	}
	rect := sdl.Rect{W: int32(w), H: int32(h)}
	if err := chip8Tex.tex.Update(&rect, unsafe.Pointer(&pix[0]), chip8MaxW*4); err != nil {
		return nil
	}
	vm.dirty = false
	chip8Tex.colors = colors
	chip8Tex.valid = true
	return chip8Tex.tex
}

// chip8Color is a 0xRRGGBB colour, scaled by f (1 is unchanged).
func chip8Color(c uint32, f float64) sdl.Color {
	ch := func(v uint32) uint8 { return uint8(math.Min(255, float64(v&0xFF)*f)) }
	return sdl.Color{R: ch(c >> 16), G: ch(c >> 8), B: ch(c), A: 255}
}

// chip8DrawGame draws the screen, whole-number scaled and centred.
func chip8DrawGame(r *sdl.Renderer, s *chip8Session) {
	band := chip8Color(s.opts.Colors[chip8ColBG], 0.6)
	if s.opts.Buzz {
		band = chip8Color(s.opts.Colors[chip8ColQuiet], 1)
		if s.vm.soundOn() && !chip8Menu.open {
			band = chip8Color(s.opts.Colors[chip8ColBuzz], 1)
		}
	}
	setDrawColor(r, band)
	_ = r.FillRect(&sdl.Rect{W: screenWidth, H: screenHeight})
	tex := chip8Upload(r, s.vm, s.opts.Colors)
	if tex == nil {
		return
	}
	w, h := s.vm.size()
	rw, rh := int32(w), int32(h)
	turned := s.opts.Rotation == 90 || s.opts.Rotation == 270
	if turned {
		rw, rh = rh, rw
	}
	scale := min(screenWidth/rw, screenHeight/rh)
	if scale < 1 {
		scale = 1
	}
	dw, dh := int32(w)*scale, int32(h)*scale
	dst := sdl.Rect{X: (screenWidth - dw) / 2, Y: (screenHeight - dh) / 2, W: dw, H: dh}
	src := sdl.Rect{W: int32(w), H: int32(h)}
	if s.opts.Rotation == 0 {
		_ = r.Copy(tex, &src, &dst)
		return
	}
	_ = r.CopyEx(tex, &src, &dst, float64(s.opts.Rotation), nil, sdl.FLIP_NONE)
}

// chip8DrawEmpty is the scene with no game (reached through the history).
func chip8DrawEmpty(r *sdl.Renderer, c *Config) {
	drawAppBackdrop(r, c, screenWidth, screenHeight)
	fonts, ok := tbxLoadFonts(c)
	if !ok {
		return
	}
	msg := "No CHIP-8 game is running. Start one from the Games library."
	tw, th := measureText(fonts.body, msg)
	renderText(r, c, fonts.body, msg, ColorTextPrimary(), (screenWidth-tw)/2, (screenHeight-th)/2)
	chip8DrawHint(r, c, hintLine(FooterHint{Button: "B", Label: "Back"}))
}

// chip8DrawHint draws one line of hints in a glass pill at the bottom.
func chip8DrawHint(r *sdl.Renderer, c *Config, line string) {
	fonts, ok := tbxLoadFonts(c)
	if !ok || line == "" {
		return
	}
	tw, th := measureText(fonts.small, line)
	pill := sdl.Rect{W: tw + 32, H: th + 14}
	pill.X = (screenWidth - pill.W) / 2
	pill.Y = screenHeight - pill.H - 18
	drawGlassSheet(r, pill, glassSheetFill())
	renderText(r, c, fonts.small, line, ColorTextPrimary(), pill.X+16, pill.Y+7)
}

// chip8DrawKeypad draws the 4×4 keypad over the game.
func chip8DrawKeypad(r *sdl.Renderer, c *Config, s *chip8Session) {
	fonts, ok := tbxLoadFonts(c)
	if !ok {
		return
	}
	cell := min(int32(72), (screenHeight-160)/4)
	gap := int32(8)
	side := cell*4 + gap*3
	card := sdl.Rect{W: side + 40, H: side + 40}
	card.X = screenWidth - card.W - 24
	card.Y = (screenHeight - card.H) / 2
	drawGlassSheet(r, card, glassSheetFill())
	for i, k := range chip8KeypadOrder {
		cr := sdl.Rect{X: card.X + 20 + int32(i%4)*(cell+gap), Y: card.Y + 20 + int32(i/4)*(cell+gap), W: cell, H: cell}
		UIListRow(r, cr, 10, k == s.keypadSel, false)
		if s.keys[k] {
			fillRoundedRect(r, cr.X, cr.Y, cr.W, cr.H, 10, WithAlpha(glassAccent(), 120))
		}
		label := strings.ToUpper(strconv.FormatInt(int64(k), 16))
		tw, th := measureText(fonts.title, label)
		renderText(r, c, fonts.title, label, ColorTextPrimary(), cr.X+(cr.W-tw)/2, cr.Y+(cr.H-th)/2)
	}
	chip8DrawHint(r, c, hintLine(FooterHint{Button: "D-Pad", Label: "Pick"}, FooterHint{Button: "A", Label: "Press"}))
}

// ── The pause menu ──────────────────────────────────────────────────────────

// chip8OpenMenu pauses the game and opens the menu, with note at its top.
func chip8OpenMenu(note string) {
	if chip8Cur == nil {
		return
	}
	if !chip8Menu.open {
		PlayActivateSound()
	}
	chip8Menu.open = true
	chip8Menu.page = chip8PageMain
	chip8Menu.cursor = chip8RowResume
	chip8Menu.note = note
	if chip8Cur.vm.halted || chip8Cur.vm.exited {
		chip8Menu.cursor = chip8RowReset
	}
}

// chip8MenuBack is B: the Keys page goes back to the menu, the menu resumes.
func chip8MenuBack() {
	if chip8Menu.page == chip8PageKeys {
		chip8Menu.page = chip8PageMain
		chip8Menu.cursor = chip8RowKeys
		PlayBackSound()
		return
	}
	chip8Resume()
}

// chip8Resume closes the menu. A stopped game stays stopped (the menu says
// so), so Resume on one only closes the menu.
func chip8Resume() {
	chip8Menu.open = false
	chip8Menu.note = ""
	PlayBackSound()
}

// chip8MenuRows is the number of rows on the open page.
func chip8MenuRows() int {
	if chip8Menu.page == chip8PageKeys {
		return len(chip8MapButtons) + 2 // the buttons, Reset keys, Back
	}
	return chip8RowCount
}

func chip8MenuMove(dir int) {
	n := chip8MenuRows()
	chip8Menu.cursor = (chip8Menu.cursor + dir + n) % n
	PlayNavSound()
}

// chip8MenuAdjust is Left/Right on a row with a value.
func chip8MenuAdjust(c *Config, dir int) {
	s := chip8Cur
	if s == nil {
		return
	}
	if chip8Menu.page == chip8PageKeys {
		if chip8Menu.cursor < len(chip8MapButtons) {
			chip8StepKey(s, chip8MapButtons[chip8Menu.cursor], dir)
		}
		return
	}
	switch chip8Menu.cursor {
	case chip8RowSpeed:
		chip8StepSpeed(s, dir)
	case chip8RowMode:
		chip8StepMode(s, dir)
	case chip8RowSound:
		chip8ToggleSound(s)
	}
}

// chip8MenuActivate is A on a row.
func chip8MenuActivate(c *Config) {
	s := chip8Cur
	if s == nil {
		return
	}
	if chip8Menu.page == chip8PageKeys {
		switch n := len(chip8MapButtons); {
		case chip8Menu.cursor < n:
			chip8StepKey(s, chip8MapButtons[chip8Menu.cursor], 1)
		case chip8Menu.cursor == n:
			s.settings.Keys = nil
			s.keymap = chip8KeyMap(s.opts, s.settings)
			chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) { g.Keys = nil })
			PlayActivateSound()
			ShowToast("Keys are back to the default", ToastKindInfo)
		default:
			chip8MenuBack()
		}
		return
	}
	switch chip8Menu.cursor {
	case chip8RowResume:
		chip8Resume()
	case chip8RowReset:
		s.vm.reset(s.ld.rom, uint32(time.Now().UnixNano()))
		s.stopShown = false
		s.last = time.Time{}
		chip8Menu.open = false
		chip8Menu.note = ""
		PlayActivateSound()
	case chip8RowSpeed:
		chip8StepSpeed(s, 1)
	case chip8RowMode:
		chip8StepMode(s, 1)
	case chip8RowKeys:
		chip8Menu.page = chip8PageKeys
		chip8Menu.cursor = 0
		PlayActivateSound()
	case chip8RowSound:
		chip8ToggleSound(s)
	case chip8RowSave:
		chip8QuickSave(s)
	case chip8RowLoad:
		chip8QuickLoad(s)
	case chip8RowQuit:
		chip8Quit(c)
	}
}

// chip8Quit leaves the player for where the game was started from.
func chip8Quit(c *Config) {
	s := chip8Cur
	chip8Menu.open = false
	chip8Menu.note = ""
	if s != nil {
		chip8SessionEnd(s)
	}
	chip8Cur = nil
	chip8AudioSet(false, nil)
	chip8TexRelease()
	goBackScene(c)
	if s != nil && s.fromLibrary {
		openGameLibraryOnSystem(chip8System, "")
	}
}

// chip8StepSpeed moves the Speed one step and keeps it for the game.
func chip8StepSpeed(s *chip8Session, dir int) {
	cur := s.opts.Tickrate
	next := cur
	if dir > 0 {
		for _, v := range chip8Speeds {
			if v > cur {
				next = v
				break
			}
		}
	} else {
		for i := len(chip8Speeds) - 1; i >= 0; i-- {
			if chip8Speeds[i] < cur {
				next = chip8Speeds[i]
				break
			}
		}
	}
	if next == cur {
		return
	}
	own := chip8EffectiveOptions(s.ld.base, chip8GameSettings{Mode: s.settings.Mode}).Tickrate
	keep := next
	if next == own {
		keep = 0 // back to the game's own
	}
	s.settings.Tickrate = keep
	s.opts.Tickrate = next
	chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) {
		g.Tickrate = keep
		g.Title = s.ld.game.Label()
	})
	PlayNavSound()
}

// chip8Modes are the Mode row's choices: the game's own, then the presets.
var chip8Modes = []string{"", "chip8", "schip", "xochip"}

// chip8StepMode picks the next Mode; it applies at once.
func chip8StepMode(s *chip8Session, dir int) {
	i := 0
	for k, m := range chip8Modes {
		if m == s.settings.Mode {
			i = k
		}
	}
	i = (i + dir + len(chip8Modes)) % len(chip8Modes)
	s.settings.Mode = chip8Modes[i]
	s.settings.Tickrate = 0 // a new machine plays at its own speed
	s.opts = chip8EffectiveOptions(s.ld.base, s.settings)
	s.vm.quirks = s.opts.Quirks
	s.vm.xo = s.opts.Platform == chip8PlatXO
	chip8Tex.valid = false
	mode, title := s.settings.Mode, s.ld.game.Label()
	chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) {
		g.Mode = mode
		g.Tickrate = 0
		g.Title = title
	})
	PlayNavSound()
}

// chip8ModeLabel is the Mode row's value.
func chip8ModeLabel(s *chip8Session) string {
	if p, ok := chip8PlatformFromName(s.settings.Mode); ok && s.settings.Mode != "" {
		return p.label()
	}
	if s.ld.base.Source == "file type" {
		return "Automatic (" + s.ld.base.Platform.label() + ")"
	}
	return "Game's own (" + s.ld.base.Platform.label() + ")"
}

func chip8ToggleSound(s *chip8Session) {
	s.settings.Mute = !s.settings.Mute
	mute := s.settings.Mute
	chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) { g.Mute = mute })
	PlayNavSound()
}

// chip8StepKey moves one pad button's hex key through none, 0…F.
func chip8StepKey(s *chip8Session, button string, dir int) {
	cur := s.keymap[button]
	next := cur + dir
	if next > 15 {
		next = -1
	}
	if next < -1 {
		next = 15
	}
	s.keymap[button] = next
	if s.settings.Keys == nil {
		s.settings.Keys = map[string]int{}
	}
	s.settings.Keys[button] = next
	chip8SettingsUpdate(s.ld.hash, func(g *chip8GameSettings) {
		if g.Keys == nil {
			g.Keys = map[string]int{}
		}
		g.Keys[button] = next
	})
	PlayNavSound()
}

// chip8KeyLabel is a hex key as the Keys page shows it.
func chip8KeyLabel(k int) string {
	if k < 0 || k > 15 {
		return "—"
	}
	return strings.ToUpper(strconv.FormatInt(int64(k), 16))
}

// chip8MenuRow is one row of the menu as drawn: its label and its value.
type chip8MenuRow struct{ label, value string }

// chip8MenuRowBuf is reused by every frame the menu is drawn.
var chip8MenuRowBuf []chip8MenuRow

// chip8DrawMenu draws the open page of the menu over the paused game.
func chip8DrawMenu(r *sdl.Renderer, c *Config, s *chip8Session) {
	fonts, ok := tbxLoadFonts(c)
	if !ok {
		return
	}
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = r.SetDrawColor(0, 0, 0, 150)
	_ = r.FillRect(&sdl.Rect{W: screenWidth, H: screenHeight})
	rows := chip8MenuRowBuf[:0]
	title := s.ld.game.Label()
	sub := s.opts.Platform.label() + " · " + strconv.Itoa(s.opts.Tickrate) + " per frame · " + s.opts.Source
	if chip8Menu.page == chip8PageKeys {
		title, sub = "Keys", "Which hex key each button presses in "+s.ld.game.Label()
		for _, b := range chip8MapButtons {
			rows = append(rows, chip8MenuRow{chip8ButtonLabels[b], chip8KeyLabel(s.keymap[b])})
		}
		rows = append(rows, chip8MenuRow{"Reset keys", ""}, chip8MenuRow{"Back", ""})
	} else {
		sound := "On"
		if s.settings.Mute {
			sound = "Off"
		}
		rows = append(rows,
			chip8MenuRow{"Resume", ""}, chip8MenuRow{"Reset", ""},
			chip8MenuRow{"Speed", strconv.Itoa(s.opts.Tickrate) + " per frame"},
			chip8MenuRow{"Mode", chip8ModeLabel(s)},
			chip8MenuRow{"Keys", ""}, chip8MenuRow{"Sound", sound},
			chip8MenuRow{"Quick save", ""}, chip8MenuRow{"Quick load", ""}, chip8MenuRow{"Quit", ""},
		)
	}
	chip8MenuRowBuf = rows
	rowH := fonts.bodyH + 18
	cardW := min(int32(620), screenWidth-48)
	headH := fonts.titleH + fonts.smallH + 30
	noteH := int32(0)
	note := chip8Menu.note
	if note != "" && chip8Menu.page == chip8PageMain {
		noteH = fonts.smallH + 14
	}
	hintH := fonts.smallH + 22
	cardH := headH + noteH + int32(len(rows))*rowH + hintH + 12
	if maxH := screenHeight - 24; cardH > maxH {
		// A short screen: the rows get tighter rather than running off it.
		rowH = max(fonts.bodyH+6, (maxH-headH-noteH-hintH-12)/int32(len(rows)))
		cardH = headH + noteH + int32(len(rows))*rowH + hintH + 12
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	drawGlassSheet(r, card, glassSheetFill())
	x := card.X + 22
	y := card.Y + 16
	renderText(r, c, fonts.title, ellipsize(fonts.title, title, cardW-44), ColorTextPrimary(), x, y)
	y += fonts.titleH + 4
	renderText(r, c, fonts.small, ellipsize(fonts.small, sub, cardW-44), ColorTextMuted(), x, y)
	y = card.Y + headH
	if noteH > 0 {
		renderText(r, c, fonts.small, ellipsize(fonts.small, note, cardW-44), ColorToastWarn, x, y)
		y += noteH
	}
	for i, rw := range rows {
		rr := sdl.Rect{X: card.X + 12, Y: y, W: cardW - 24, H: rowH - 4}
		UIListRow(r, rr, 10, i == chip8Menu.cursor, false)
		ty := rr.Y + (rr.H-fonts.bodyH)/2
		renderText(r, c, fonts.body, rw.label, ColorTextPrimary(), rr.X+14, ty)
		if rw.value != "" {
			val := rw.value
			if i == chip8Menu.cursor {
				val = "‹ " + val + " ›"
			}
			vw, _ := measureText(fonts.body, val)
			renderText(r, c, fonts.body, val, ColorTextMuted(), rr.X+rr.W-14-vw, ty)
		}
		y += rowH
	}
	hints := hintLine(FooterHint{Button: "D-Pad", Label: "Move"}, FooterHint{Button: "Left/Right", Label: "Change"},
		FooterHint{Button: "A", Label: "Choose"}, FooterHint{Button: "B", Label: "Back"})
	renderText(r, c, fonts.small, ellipsize(fonts.small, hints, cardW-44), ColorTextTertiary(), x, card.Y+cardH-hintH+4)
}

// ── Quick save ──────────────────────────────────────────────────────────────

func chip8StatePath(hash string) string {
	dir := filepath.Join(chip8DataDir(), "states")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, hash+".json")
}

// chip8QuickSave writes the whole machine; the write is on a goroutine.
func chip8QuickSave(s *chip8Session) {
	st := s.vm.snapshot()
	hash, title := s.ld.hash, s.ld.game.Label()
	PlayActivateSound()
	go func() {
		data, err := json.Marshal(st)
		if err == nil {
			err = AtomicWrite(chip8StatePath(hash), data, 0o644)
		}
		tbxPost(func() {
			if err != nil {
				logWarn("chip8", "quick save of %s: %v", title, err)
				ShowToast("Could not save "+title+": "+firstLine(err.Error()), ToastKindError)
				return
			}
			ShowToast("Saved "+title, ToastKindSuccess)
		})
	}()
}

// chip8QuickLoad reads the quick save on a goroutine and puts it back.
func chip8QuickLoad(s *chip8Session) {
	hash, title := s.ld.hash, s.ld.game.Label()
	PlayActivateSound()
	go func() {
		var st chip8State
		_, err := readJSONWithBackup(chip8StatePath(hash), &st)
		tbxPost(func() {
			cur := chip8Cur
			if cur == nil || cur.ld.hash != hash {
				return
			}
			if errors.Is(err, os.ErrNotExist) {
				ShowToast("No quick save for "+title+" yet", ToastKindInfo)
				return
			}
			if err == nil {
				err = cur.vm.restore(st)
			}
			if err != nil {
				ShowToast("Could not load the save: "+firstLine(err.Error()), ToastKindError)
				return
			}
			cur.stopShown = cur.vm.exited
			cur.last = time.Time{}
			chip8Menu.open = false
			chip8Menu.note = ""
			ShowToast("Loaded "+title, ToastKindSuccess)
		})
	}()
}

// chip8MaybeCover saves the screen as the game's cover once the game has
// played a while without one (only when it shows something).
func chip8MaybeCover(s *chip8Session) {
	if s.coverDone || s.coverPath == "" || s.played < chip8CoverAfter {
		return
	}
	s.coverDone = true
	if s.vm.blank() {
		return
	}
	// The PNG is made on the goroutine from a copy of the machine (its screen
	// as it is now), so the encode costs the game no frame.
	snap := new(chip8VM)
	*snap = *s.vm
	colors, path := s.opts.Colors, s.coverPath
	go func() {
		if regularFile(path) {
			return
		}
		data := chip8FramePNG(snap, colors)
		if len(data) == 0 {
			return
		}
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := chip8WriteFile(path, data); err != nil {
			logWarn("chip8", "saving a cover: %v", err)
		}
	}()
}

// ── Sound ───────────────────────────────────────────────────────────────────

// chip8Audio is what the sound goroutine plays, set by the scene each frame.
var chip8Audio struct {
	mu      sync.Mutex
	on      bool
	custom  bool
	pattern [16]byte
	pitch   byte
	running bool
}

// chip8AudioEpoch is the monotonic origin of the two times below (a clock
// change must not stop or keep the sound).
var (
	chip8AudioEpoch = time.Now()
	// chip8AudioSeen is when the scene last ran an unpaused frame, and
	// chip8AudioHeard when it last asked for sound (nanoseconds since the
	// epoch).
	chip8AudioSeen  atomic.Int64
	chip8AudioHeard atomic.Int64
)

const (
	chip8AudioIdle    = 300 * time.Millisecond // unseen this long: stop
	chip8AudioSilence = 5 * time.Second        // silent this long: close the device
)

// chip8AudioSet tells the sound goroutine what to play, and starts it on
// the first beep. vm nil (or on false) is silence.
func chip8AudioSet(on bool, vm *chip8VM) {
	now := int64(time.Since(chip8AudioEpoch))
	chip8Audio.mu.Lock()
	chip8Audio.on = on && vm != nil
	if chip8Audio.on {
		chip8Audio.custom = vm.patternSet
		chip8Audio.pattern = vm.pattern
		chip8Audio.pitch = vm.pitch
	}
	start := chip8Audio.on && !chip8Audio.running
	if start {
		chip8Audio.running = true
	}
	chip8Audio.mu.Unlock()
	if vm != nil && !chip8Menu.open {
		chip8AudioSeen.Store(now)
	}
	if on {
		chip8AudioHeard.Store(now)
	}
	if start {
		go chip8AudioLoop()
	}
}

// chip8AudioLoop owns the player's sound device while a game beeps.
func chip8AudioLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("chip8", "sound stopped: %v", r)
		}
		chip8Audio.mu.Lock()
		chip8Audio.running = false
		chip8Audio.mu.Unlock()
	}()
	desired := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16LSB, Channels: 1, Samples: 512}
	var obtained sdl.AudioSpec
	dev, err := outOpen(&desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logWarn("chip8", "no sound device: %v", err)
		time.Sleep(2 * time.Second) // not again on every frame
		return
	}
	defer outClose(dev)
	rate := float64(obtained.Freq)
	if rate <= 0 {
		rate = 48000
	}
	const block = 512
	buf := make([]byte, block*2)
	ahead := uint32(3 * block * 2)
	var env, lp, tonePhase, patPos float64
	started := false
	for {
		now := time.Since(chip8AudioEpoch)
		if now-time.Duration(chip8AudioSeen.Load()) > chip8AudioIdle ||
			now-time.Duration(chip8AudioHeard.Load()) > chip8AudioSilence {
			break
		}
		for outQueued(dev) < ahead {
			chip8Audio.mu.Lock()
			on, custom, pattern, pitch := chip8Audio.on, chip8Audio.custom, chip8Audio.pattern, chip8Audio.pitch
			chip8Audio.mu.Unlock()
			// XO-CHIP plays the pattern at 4000·2^((pitch−64)/48) bits a second.
			bitStep := 4000 * math.Pow(2, (float64(pitch)-64)/48) / rate
			toneStep := 440.0 / rate
			target := 0.0
			if on {
				target = 1
			}
			if !on && env == 0 && math.Abs(lp) < 1e-4 {
				// Quiet and the off-ramp done: nothing is queued (the device
				// plays silence by itself), so a pause or a gap between
				// beeps leaves no backlog behind the next beep.
				lp = 0
				break
			}
			for i := range block {
				// A 2 ms ramp on and off, so a beep never clicks.
				if env < target {
					env = math.Min(target, env+1/(0.002*rate))
				} else if env > target {
					env = math.Max(target, env-1/(0.002*rate))
				}
				var x float64
				if custom {
					bit := int(patPos) & 127
					if pattern[bit>>3]&(0x80>>(bit&7)) != 0 {
						x = 1
					} else {
						x = -1
					}
					patPos += bitStep
					if patPos >= 128 {
						patPos -= 128
					}
				} else {
					if tonePhase < 0.5 {
						x = 1
					} else {
						x = -1
					}
					tonePhase += toneStep
					if tonePhase >= 1 {
						tonePhase--
					}
				}
				// A gentle low-pass takes the edge off the square wave.
				lp += (x*env - lp) * 0.3
				v := int16(lp * 0.22 * 32767)
				buf[2*i] = byte(v)
				buf[2*i+1] = byte(uint16(v) >> 8)
			}
			if err := outQueue(dev, buf); err != nil {
				return
			}
		}
		if !started {
			outPause(dev, false)
			started = true
		}
		time.Sleep(4 * time.Millisecond)
	}
	outClear(dev)
}
