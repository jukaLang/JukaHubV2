package main

// pads_ui.go — the Pads page's input, footer and drawing, and its sample
// browser (pads.go has the state and the actions, pads_engine.go the sound).

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Pads input actions, whatever device produced them.
const (
	padsInNone = iota
	padsInUp
	padsInDown
	padsInLeft
	padsInRight
	padsInA
	padsInB
	padsInX
	padsInY
	padsInSelect
	padsInPlay
	padsInTabPrev
	padsInTabNext
	padsInPrev // L2
	padsInNext // R2
	padsInBack
)

func padsSceneActive(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == padsSceneName
}

// padsShoulder takes the shoulders and triggers (shoulders.go routes them).
func padsShoulder(s padShoulder) {
	switch s {
	case padL1:
		padsDo(padsInTabPrev, padsHeld{}, appConfig)
	case padR1:
		padsDo(padsInTabNext, padsHeld{}, appConfig)
	case padL2:
		padsDo(padsInPrev, padsHeld{}, appConfig)
	case padR2:
		padsDo(padsInNext, padsHeld{}, appConfig)
	}
}

// padsDrumPad is the pad a button plays in finger drumming: the D-pad plays
// the upper row of the pair left to right (Left, Up, Down, Right) and Y X B A
// the row under it.
func padsDrumPad(in int) int {
	pair := padsClampInt(padsUI.drumPair, 0, padsBanks*2-1)
	bank, top := pair/2, (pair%2)*2
	row, col := -1, -1
	switch in {
	case padsInLeft:
		row, col = top, 0
	case padsInUp:
		row, col = top, 1
	case padsInDown:
		row, col = top, 2
	case padsInRight:
		row, col = top, 3
	case padsInY:
		row, col = top+1, 0
	case padsInX:
		row, col = top+1, 1
	case padsInB:
		row, col = top+1, 2
	case padsInA:
		row, col = top+1, 3
	}
	if row < 0 {
		return -1
	}
	return bank*padsPerBank + row*4 + col
}

// padsDo runs one action on the page. src is the press, for a pad that holds.
func padsDo(in int, src padsHeld, config *Config) {
	if padsUI.br.open {
		padsBrowserDo(in)
		return
	}
	if padsUI.tab == padsTabPlay && padsUI.drum {
		switch in {
		case padsInPlay:
			padsTogglePlay()
		case padsInSelect, padsInBack:
			padsUI.drum = false
		case padsInTabPrev, padsInTabNext:
			padsUI.drum = false
			padsSwitchTab(in)
		case padsInPrev, padsInNext:
			d := 1
			if in == padsInPrev {
				d = -1
			}
			padsUI.drumPair = (padsUI.drumPair + d + padsBanks*2) % (padsBanks * 2)
			padsUI.bank = padsUI.drumPair / 2
		default:
			padsPlayPad(padsDrumPad(in), src, true)
		}
		return
	}
	switch in {
	case padsInBack, padsInB:
		padsWithEngine(func(p *padsProject, e *padsEngine) { e.stop() })
		padsMaybeSave(true)
		goBackScene(config)
		return
	case padsInPlay:
		padsTogglePlay()
		return
	case padsInTabPrev, padsInTabNext:
		padsSwitchTab(in)
		return
	}
	switch padsUI.tab {
	case padsTabPlay:
		padsDoPlay(in, src)
	case padsTabBeat:
		padsDoBeat(in)
	case padsTabPad:
		switch in {
		case padsInPrev:
			padsUI.pad = (padsUI.pad + padsCount - 1) % padsCount
		case padsInNext:
			padsUI.pad = (padsUI.pad + 1) % padsCount
		case padsInSelect:
			padsAudition(padsUI.pad)
		default:
			padsDoRows(in, padsPadRows(padsUI.pad), &padsUI.padRow)
		}
		padsUI.bank = padsUI.pad / padsPerBank
		padsUI.cur = padsUI.pad % padsPerBank
	case padsTabSong:
		padsDoSong(in)
	case padsTabFX:
		if in == padsInPrev || in == padsInNext {
			padsSetPattern(padsPrevNext(in))
			return
		}
		live := padsLive()
		padsDoRows(in, padsFxRows(&live), &padsUI.fxRow)
	case padsTabMix:
		if in == padsInPrev || in == padsInNext {
			padsSetPattern(padsPrevNext(in))
			return
		}
		padsDoRows(in, padsMixRows(), &padsUI.mixRow)
	}
}

// padsPrevNext is -1 for L2 and 1 for R2.
func padsPrevNext(in int) int {
	if in == padsInPrev {
		return -1
	}
	return 1
}

func padsSwitchTab(in int) {
	d := 1
	if in == padsInTabPrev {
		d = -1
	}
	padsUI.tab = (padsUI.tab + d + padsTabCount) % padsTabCount
	padsUI.armed = ""
	padsUI.painting = false
	if padsUI.tab == padsTabBeat {
		padsUI.row = padsUI.cur
	}
}

// padsDoPlay is the Play page outside finger drumming.
func padsDoPlay(in int, src padsHeld) {
	r, c := padsUI.cur/4, padsUI.cur%4
	switch in {
	case padsInUp:
		if r == 0 {
			padsSetBank(padsUI.bank - 1)
			r = 3
		} else {
			r--
		}
	case padsInDown:
		if r == 3 {
			padsSetBank(padsUI.bank + 1)
			r = 0
		} else {
			r++
		}
	case padsInLeft:
		c = (c + 3) % 4
	case padsInRight:
		c = (c + 1) % 4
	case padsInA:
		padsPlayPad(padsUI.bank*padsPerBank+padsUI.cur, src, true)
		return
	case padsInX:
		padsUI.pad = padsUI.bank*padsPerBank + padsUI.cur
		padsUI.tab = padsTabPad
		return
	case padsInY:
		padsOpenBrowser(padsUI.bank*padsPerBank + padsUI.cur)
		return
	case padsInSelect:
		padsUI.drum = true
		padsUI.drumPair = padsUI.bank*2 + r/2
		return
	case padsInPrev:
		padsSetBank(padsUI.bank - 1)
		return
	case padsInNext:
		padsSetBank(padsUI.bank + 1)
		return
	default:
		return
	}
	padsUI.cur = r*4 + c
	padsUI.pad = padsUI.bank*padsPerBank + padsUI.cur
}

// padsDoBeat is the Beat page's grid.
func padsDoBeat(in int) {
	p := padsSnapshot()
	steps := padsStepsValid(p.Patterns[p.Current].Steps)
	padsUI.step = padsClampInt(padsUI.step, 0, steps-1)
	pad := padsUI.bank*padsPerBank + padsUI.row
	moved := false
	switch in {
	case padsInUp:
		if padsUI.row == 0 {
			padsSetBank(padsUI.bank - 1)
			padsUI.row = padsPerBank - 1
		} else {
			padsUI.row--
		}
		padsUI.painting = false
	case padsInDown:
		if padsUI.row == padsPerBank-1 {
			padsSetBank(padsUI.bank + 1)
			padsUI.row = 0
		} else {
			padsUI.row++
		}
		padsUI.painting = false
	case padsInLeft:
		padsUI.step = (padsUI.step + steps - 1) % steps
		moved = true
	case padsInRight:
		padsUI.step = (padsUI.step + 1) % steps
		moved = true
	case padsInPrev:
		padsSetPattern(-1)
	case padsInNext:
		padsSetPattern(1)
	case padsInA:
		on := p.Patterns[p.Current].hitAt(pad, padsUI.step) < 0
		padsSetStep(pad, padsUI.step, on, nil)
		padsUI.painting, padsUI.paintOn = true, on
	case padsInY, padsInX:
		d := 1
		if in == padsInX {
			d = -1
		}
		padsSetStep(pad, padsUI.step, true, func(h *padsHit, had bool) {
			if had {
				h.Note = padsClampInt(h.Note+d, -padsMaxNote, padsMaxNote)
			} else {
				h.Note = padsUI.lastNote
			}
			padsUI.lastNote = h.Note
		})
	case padsInSelect:
		if p.Patterns[p.Current].hitAt(pad, padsUI.step) >= 0 {
			padsSetStep(pad, padsUI.step, true, func(h *padsHit, had bool) { h.Accent = !h.Accent })
		}
	}
	padsUI.cur = padsUI.row
	padsUI.pad = pad
	if in == padsInUp || in == padsInDown {
		padsUI.pad = padsUI.bank*padsPerBank + padsUI.row
	}
	// Painting: A held while moving along the row sets each step the way the
	// first press did.
	if moved && padsUI.painting && padsAHeld() {
		padsSetStep(pad, padsUI.step, padsUI.paintOn, nil)
	}
}

// padsSetStep turns pad's hit on step on or off; edit shapes it (had tells
// whether it existed). A hit turned on is played.
func padsSetStep(pad, step int, on bool, edit func(h *padsHit, had bool)) {
	var h padsHit
	padsEdit(func(p *padsProject, e *padsEngine) {
		pat := &p.Patterns[p.Current]
		i := pat.hitAt(pad, step)
		had := i >= 0
		h = padsHit{Pad: pad, Step: step}
		if had {
			h = pat.Hits[i]
		}
		if edit != nil {
			edit(&h, had)
		}
		pat.setHit(h, on)
	})
	if !on {
		return
	}
	padsWithEngine(func(p *padsProject, e *padsEngine) {
		d := &p.Pads[pad]
		if smp := e.samples[d.Sample]; smp != nil {
			vel := 0.8
			if h.Accent {
				vel = 1
			}
			e.trigger(pad, d, smp, h.Note, vel, int(e.baseStepSamples()), 0)
		}
	})
}

// padsDoSong drives the Song page: the settings on the left, the song's
// slots on the right (Left / Right change a slot's pattern, X removes it, Y
// repeats it, A opens its pattern).
func padsDoSong(in int) {
	set := padsSongSettingRows()
	song := padsSnapshot().Song
	n := len(set) + len(song)
	padsUI.songRow = padsClampInt(padsUI.songRow, 0, n-1)
	if padsUI.songRow < len(set) || in == padsInUp || in == padsInDown {
		switch in {
		case padsInUp:
			padsUI.songRow = (padsUI.songRow + n - 1) % n
			return
		case padsInDown:
			padsUI.songRow = (padsUI.songRow + 1) % n
			return
		case padsInPrev, padsInNext:
			padsSetPattern(padsPrevNext(in))
			return
		}
		row := padsUI.songRow
		padsDoRows(in, set, &row)
		return
	}
	k := padsUI.songRow - len(set)
	switch in {
	case padsInLeft, padsInRight:
		d := 1
		if in == padsInLeft {
			d = -1
		}
		padsEdit(func(p *padsProject, e *padsEngine) {
			if k < len(p.Song) {
				s := append([]int(nil), p.Song...)
				s[k] = (s[k] + d + padsPatterns) % padsPatterns
				p.Song = s
			}
		})
	case padsInX:
		padsEdit(func(p *padsProject, e *padsEngine) {
			if k < len(p.Song) {
				s := append([]int(nil), p.Song[:k]...)
				p.Song = append(s, p.Song[k+1:]...)
			}
		})
		if k >= len(song)-1 && padsUI.songRow > 0 {
			padsUI.songRow--
		}
	case padsInY:
		padsEdit(func(p *padsProject, e *padsEngine) {
			if k < len(p.Song) && len(p.Song) < padsSongMax {
				s := append([]int(nil), p.Song[:k+1]...)
				s = append(s, p.Song[k])
				p.Song = append(s, p.Song[k+1:]...)
			}
		})
	case padsInA:
		padsEdit(func(p *padsProject, e *padsEngine) {
			if k < len(p.Song) {
				p.Current = p.Song[k]
			}
		})
		padsUI.tab = padsTabBeat
	case padsInPrev, padsInNext:
		padsSetPattern(padsPrevNext(in))
	}
}

// ── Sample browser ───────────────────────────────────────────────────────

// padsOpenBrowser opens the browser to pick a sound for pad.
func padsOpenBrowser(pad int) {
	padsUI.br = padsBrowser{open: true, target: padsClampInt(pad, 0, padsCount-1), rows: padsSources()}
	padsBrowserDirs = nil
}

// padsBrowserOpenDir shows a folder (or "" for the sources).
func padsBrowserOpenDir(dir string, cur int) {
	b := &padsUI.br
	b.err = ""
	if dir == "" {
		b.rows = padsSources()
	} else {
		rows, err := padsListDir(dir)
		if err != nil {
			b.err = err.Error()
		}
		b.rows = rows
	}
	b.dir = dir
	b.cur = padsClampInt(cur, 0, max(0, len(b.rows)-1))
	b.top = 0
}

// padsBrowserDirs is the stack of folders above the one on show.
var padsBrowserDirs []string

func padsBrowserDo(in int) {
	b := &padsUI.br
	n := len(b.rows)
	switch in {
	case padsInUp:
		if n > 0 {
			b.cur = (b.cur + n - 1) % n
		}
	case padsInDown:
		if n > 0 {
			b.cur = (b.cur + 1) % n
		}
	case padsInLeft, padsInPrev:
		b.cur = max(0, b.cur-8)
	case padsInRight, padsInNext:
		b.cur = min(max(0, n-1), b.cur+8)
	case padsInPlay:
		padsTogglePlay()
	case padsInY:
		if b.cur < n && !b.rows[b.cur].dir && b.rows[b.cur].action == "" {
			padsPreview(b.rows[b.cur].path)
		}
	case padsInA:
		if b.cur >= n {
			return
		}
		row := b.rows[b.cur]
		switch {
		case row.action == "record":
			b.open = false
			ShowToast("Record in Voice Memos, then come back and pick it under Voice Memos", ToastKindInfo)
			osAppsOpen(appConfig, voiceMemoSceneName)
		case row.dir:
			padsBrowserDirs = append(padsBrowserDirs, b.dir)
			b.stack = append(b.stack, b.cur)
			padsBrowserOpenDir(row.path, 0)
		default:
			padsAssign(b.target, row.path, row.label)
			ShowToast(row.label+" is on pad "+padsPadLabel(b.target), ToastKindSuccess)
			b.open = false
			padsBrowserDirs = nil
		}
	case padsInB, padsInBack:
		if len(padsBrowserDirs) == 0 {
			b.open = false
			return
		}
		dir := padsBrowserDirs[len(padsBrowserDirs)-1]
		padsBrowserDirs = padsBrowserDirs[:len(padsBrowserDirs)-1]
		cur := 0
		if k := len(b.stack); k > 0 {
			cur = b.stack[k-1]
			b.stack = b.stack[:k-1]
		}
		padsBrowserOpenDir(dir, cur)
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func padsPadAction(e *sdl.ControllerButtonEvent) int {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return padsInUp
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return padsInDown
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return padsInLeft
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return padsInRight
	case sdl.CONTROLLER_BUTTON_A:
		return padsInA
	case sdl.CONTROLLER_BUTTON_B:
		return padsInB
	case sdl.CONTROLLER_BUTTON_X:
		return padsInX
	case sdl.CONTROLLER_BUTTON_Y:
		return padsInY
	case sdl.CONTROLLER_BUTTON_BACK:
		return padsInSelect
	case sdl.CONTROLLER_BUTTON_START:
		return padsInPlay
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return padsInTabPrev
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return padsInTabNext
	}
	return padsInNone
}

// padsKeyPads are the keys that play a bank's 4 × 4 pads on the Play page,
// row by row, the way drum-pad software lays out a keyboard.
var padsKeyPads = map[sdl.Keycode]int{
	sdl.K_1: 0, sdl.K_2: 1, sdl.K_3: 2, sdl.K_4: 3,
	sdl.K_q: 4, sdl.K_w: 5, sdl.K_e: 6, sdl.K_r: 7,
	sdl.K_a: 8, sdl.K_s: 9, sdl.K_d: 10, sdl.K_f: 11,
	sdl.K_z: 12, sdl.K_x: 13, sdl.K_c: 14, sdl.K_v: 15,
}

func padsKeyAction(e *sdl.KeyboardEvent) int {
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return padsInUp
	case sdl.K_DOWN:
		return padsInDown
	case sdl.K_LEFT:
		return padsInLeft
	case sdl.K_RIGHT:
		return padsInRight
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_a:
		return padsInA
	case sdl.K_x, sdl.K_MINUS, sdl.K_KP_MINUS:
		return padsInX
	case sdl.K_y, sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS:
		return padsInY
	case sdl.K_BACKQUOTE, sdl.K_m:
		return padsInSelect
	case sdl.K_SPACE:
		return padsInPlay
	case sdl.K_TAB:
		if shift {
			return padsInTabPrev
		}
		return padsInTabNext
	case sdl.K_LEFTBRACKET, sdl.K_PAGEUP:
		return padsInPrev
	case sdl.K_RIGHTBRACKET, sdl.K_PAGEDOWN:
		return padsInNext
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return padsInBack
	}
	return padsInNone
}

type padsScene struct{ sceneBase }

func (padsScene) render(r *sdl.Renderer, c *Config) bool { renderPads(r, c); return true }

func (padsScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	// The letter and number keys play the bank's pads on the Play page.
	if padsUI.tab == padsTabPlay && !padsUI.br.open {
		if k, ok := padsKeyPads[e.Keysym.Sym]; ok {
			if e.Repeat == 0 {
				bank := padsUI.bank
				if padsUI.drum {
					bank = padsUI.drumPair / 2
				}
				sc := int(e.Keysym.Scancode)
				padsPlayPad(bank*padsPerBank+k, padsHeld{id: 2000 + sc, btn: -1, scan: sc}, true)
			}
			return true
		}
	}
	in := padsKeyAction(e)
	if in == padsInNone {
		return true
	}
	if e.Repeat != 0 && in != padsInUp && in != padsInDown && in != padsInLeft && in != padsInRight {
		return true
	}
	if e.Repeat != 0 && padsUI.tab == padsTabPlay && padsUI.drum {
		return true
	}
	src := padsHeld{}
	if in == padsInA {
		sc := int(e.Keysym.Scancode)
		src = padsHeld{id: 2000 + sc, btn: -1, scan: sc}
	}
	padsDo(in, src, c)
	return true
}

func (padsScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	in := padsPadAction(e)
	if in == padsInNone {
		return false
	}
	// A held D-pad repeats (navrepeat.go): fine for walking, but a pad must
	// not machine-gun while finger drumming.
	if e.Which == padRepeatWhich {
		if padsUI.tab == padsTabPlay && padsUI.drum && !padsUI.br.open {
			return true
		}
		if in != padsInUp && in != padsInDown && in != padsInLeft && in != padsInRight {
			return true
		}
	}
	src := padsHeld{}
	if e.Which != padRepeatWhich {
		src = padsHeld{id: 1000 + int(e.Button), btn: int(e.Button), scan: -1}
	}
	padsDo(in, src, c)
	return true
}

// padsEnsureScene gives the page its scene, in memory, like the Synth's
// (toolshub.go calls it at startup and after every reload).
func padsEnsureScene(config *Config) {
	if config == nil || sceneIndexExact(config, padsSceneName) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        padsSceneName,
		Icon:        "P",
		Description: "64-pad sample sequencer",
	})
}

// padsFooterHints names the keys of the page on show (extras.go).
func padsFooterHints() []FooterHint {
	play := FooterHint{Button: "Start / Space", Label: "Play / Stop"}
	tabs := FooterHint{Button: "L1/R1 / Tab", Label: "Page"}
	back := FooterHint{Button: "B", Label: "Back"}
	if padsUI.br.open {
		return []FooterHint{
			{Button: "A", Label: "Choose"},
			{Button: "Y", Label: "Listen"},
			{Button: "Left/Right", Label: "Page"},
			{Button: "B", Label: "Up / Close"},
		}
	}
	switch padsUI.tab {
	case padsTabPlay:
		if padsUI.drum {
			return []FooterHint{
				{Button: "D-Pad", Label: "Upper row"},
				{Button: "Y X B A", Label: "Lower row"},
				{Button: "L2/R2 / [ ]", Label: "Rows"},
				{Button: "Select / M", Label: "Stop drumming"},
				play,
			}
		}
		return []FooterHint{
			{Button: "A", Label: "Play pad"},
			{Button: "Y", Label: "Sound"},
			{Button: "X", Label: "Edit"},
			{Button: "Select / M", Label: "Finger drums"},
			{Button: "L2/R2 / [ ]", Label: "Bank"},
			play, tabs, back,
		}
	case padsTabBeat:
		return []FooterHint{
			{Button: "A", Label: "Step (hold to paint)"},
			{Button: "Y / X", Label: "Note"},
			{Button: "Select / M", Label: "Accent"},
			{Button: "L2/R2 / [ ]", Label: "Pattern"},
			play, tabs, back,
		}
	case padsTabPad:
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose / Adjust"},
			{Button: "X / Y", Label: "Big step"},
			{Button: "A", Label: "Run / Listen"},
			{Button: "L2/R2 / [ ]", Label: "Pad"},
			play, tabs, back,
		}
	case padsTabSong:
		set := len(padsSongSettingRows())
		if padsUI.songRow >= set {
			return []FooterHint{
				{Button: "Left/Right", Label: "Pattern"},
				{Button: "Y", Label: "Repeat"},
				{Button: "X", Label: "Remove"},
				{Button: "A", Label: "Edit pattern"},
				play, tabs, back,
			}
		}
	case padsTabFX:
		return []FooterHint{
			{Button: "Sticks", Label: "Play effects"},
			{Button: "Left/Right", Label: "Change"},
			{Button: "A", Label: "Hold on/off"},
			play, tabs, back,
		}
	}
	return []FooterHint{
		{Button: "D-Pad", Label: "Choose / Adjust"},
		{Button: "X / Y", Label: "Big step"},
		{Button: "A", Label: "Run"},
		{Button: "L2/R2 / [ ]", Label: "Pattern"},
		play, tabs, back,
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// renderPads draws the page.
func renderPads(renderer *sdl.Renderer, config *Config) {
	padsEnsureLoaded()
	padsTouch()
	padsPollHeld()
	if padsUI.painting && !padsAHeld() {
		padsUI.painting = false
	}
	p := padsSnapshot()
	padsUpdateStickFX(config, &p)
	live := padsLive()

	sub := "Pattern " + strconv.Itoa(p.Current+1) + " · " + strconv.Itoa(p.BPM) + " BPM · Bank " + padsBankName(padsUI.bank)
	if p.SongMode {
		sub += " · Song of " + strconv.Itoa(len(p.Song))
	}
	status, statusInk := "Stopped", sdl.Color{}
	switch {
	case live.playing && p.RecLive:
		status, statusInk = "Recording", sdl.Color{R: 255, G: 110, B: 100, A: 255}
	case live.playing:
		status, statusInk = "Playing", sdl.Color{R: 120, G: 224, B: 150, A: 255}
	case !live.open:
		padsSt.mu.Lock()
		failed := !padsSt.failAt.IsZero()
		padsSt.mu.Unlock()
		if failed {
			status, statusInk = "No sound device", ColorToastWarn
			if !padsUI.failShow {
				padsUI.failShow = true
				ShowToast("Pads could not open the speaker; it will keep trying", ToastKindWarning)
			}
		}
	}
	header := tbxHeader("Pads", sub, "pads", status, statusInk)
	f := tbxBeginFrame(renderer, config, header)
	body := f.bodyRect()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")

	tabsH := int32(40)
	padsDrawTabs(renderer, config, medium, sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: tabsH})
	stripH := int32(52)
	gap := int32(10)
	content := sdl.Rect{X: body.X, Y: body.Y + tabsH + gap, W: body.W, H: body.H - tabsH - stripH - 2*gap}
	if content.H > 40 {
		switch padsUI.tab {
		case padsTabPlay:
			padsDrawPlay(renderer, config, small, medium, content, &p, &live)
		case padsTabBeat:
			padsDrawBeat(renderer, config, small, content, &p, &live)
		case padsTabPad:
			padsDrawPadPage(renderer, config, small, content, &p, &live)
		case padsTabSong:
			padsDrawSong(renderer, config, small, content, &p, &live)
		case padsTabFX:
			rows := padsFxRows(&live)
			padsDrawRows(renderer, config, small, content, &p, rows, padsUI.fxRow, glassAccent(), padsDirCount)
		case padsTabMix:
			padsDrawRows(renderer, config, small, content, &p, padsMixRows(), padsUI.mixRow, glassAccent(), 8)
			if small != nil && padsExporting.Load() {
				renderText(renderer, config, small, "Exporting…", ColorTextAccent(), content.X+content.W/2+16, content.Y+content.H-synthFontH(small))
			}
		}
	}
	padsDrawStrip(renderer, config, small, sdl.Rect{X: body.X, Y: body.Y + body.H - stripH, W: body.W, H: stripH}, &p, &live)
	if padsUI.br.open {
		padsDrawBrowser(renderer, config, small, medium, body)
	}
	tbxEndFrame(renderer, config)
	padsMaybeSave(false)
}

func padsDrawTabs(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	w := min32((rect.W-(padsTabCount-1)*8)/padsTabCount, 150)
	for i := 0; i < padsTabCount; i++ {
		r := sdl.Rect{X: rect.X + int32(i)*(w+8), Y: rect.Y, W: w, H: rect.H}
		ink := ColorTextSecondary()
		if i == padsUI.tab {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(glassAccent(), 210))
			ink = ColorIconDark
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, glassFill())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassEdge())
		}
		if font != nil {
			label := padsTabNames[i]
			tw := synthTextW(font, label)
			renderText(renderer, config, font, label, ink, r.X+(r.W-tw)/2, r.Y+(r.H-synthFontH(font))/2)
		}
	}
}

// padsPadName is what a pad is called on screen.
func padsPadName(d *padsPad) string {
	switch {
	case d.Sample == "":
		return ""
	case d.Name != "":
		return d.Name
	}
	return filepath.Base(d.Sample)
}

// padsFlashLevel is how lit pad i is: the engine's hit flash or a press.
func padsFlashLevel(i int, live *padsLiveView) float64 {
	fl := float64(live.flash[i])
	if t := 1 - time.Since(padsUI.flash[i]).Seconds()/0.25; t > fl {
		fl = t
	}
	return padsClamp01(fl)
}

// padsDrumKeyNames label the buttons of finger drumming.
var padsDrumKeyNames = [2][4]string{{"Left", "Up", "Down", "Right"}, {"Y", "X", "B", "A"}}

func padsDrawPlay(renderer *sdl.Renderer, config *Config, small, medium *ttf.Font, rect sdl.Rect, p *padsProject, live *padsLiveView) {
	bank := padsUI.bank
	g := min32(rect.H, rect.W*54/100)
	gap := int32(8)
	cell := (g - 3*gap) / 4
	gx, gy := rect.X, rect.Y+(rect.H-g)/2
	color := padsBankColors[bank]
	pair := padsUI.drumPair
	for k := 0; k < padsPerBank; k++ {
		idx := bank*padsPerBank + k
		row, col := k/4, k%4
		r := sdl.Rect{X: gx + int32(col)*(cell+gap), Y: gy + int32(row)*(cell+gap), W: cell, H: cell}
		d := &p.Pads[idx]
		fl := padsFlashLevel(idx, live)
		inPair := padsUI.drum && pair/2 == bank && row/2 == pair%2
		switch {
		case d.Sample == "":
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, glassFill())
		case d.Mute:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, WithAlpha(color, 30))
		default:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, WithAlpha(color, uint8(55+190*fl)))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, 2, WithAlpha(color, 170))
		}
		if padsUI.drum && !inPair {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 14, sdl.Color{R: 0, G: 0, B: 0, A: 90})
		}
		if small != nil {
			name := padsPadName(d)
			ink := ColorTextPrimary()
			switch {
			case name == "":
				name, ink = "Empty", ColorTextTertiary()
			case d.Mute:
				name += " (muted)"
				ink = ColorTextTertiary()
			default:
				if st := padsSampleState(d.Sample); st == "loading" {
					name += " …"
				} else if st != "" {
					name, ink = "Missing: "+name, ColorToastWarn
				}
			}
			renderText(renderer, config, small, fitTextCached(small, name, r.W-14), ink, r.X+8, r.Y+8)
			renderText(renderer, config, small, padsPadLabel(idx), ColorTextTertiary(), r.X+8, r.Y+r.H-synthFontH(small)-6)
			if inPair {
				key := padsDrumKeyNames[row%2][col]
				tw := synthTextW(small, key)
				renderText(renderer, config, small, key, ColorTextAccent(), r.X+r.W-tw-8, r.Y+r.H-synthFontH(small)-6)
			}
		}
		if !padsUI.drum && k == padsUI.cur {
			drawGlassFocusRing(renderer, r, 14)
		}
	}
	// The side panel: banks, the focused pad and its sound.
	x := gx + g + 24
	w := rect.X + rect.W - x
	if w < 120 || small == nil {
		return
	}
	y := rect.Y
	cw := min32((w-3*8)/padsBanks, 110)
	for b := 0; b < padsBanks; b++ {
		r := sdl.Rect{X: x + int32(b)*(cw+8), Y: y, W: cw, H: 32}
		c := padsBankColors[b]
		ink := ColorTextSecondary()
		if b == bank {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(c, 220))
			ink = ColorIconDark
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, glassFill())
			fillCircle(renderer, r.X+14, r.Y+r.H/2, 4, c)
		}
		label := "Bank " + padsBankName(b)
		renderText(renderer, config, small, label, ink, r.X+(r.W-synthTextW(small, label))/2, r.Y+(r.H-synthFontH(small))/2)
	}
	y += 44
	idx := padsUI.bank*padsPerBank + padsUI.cur
	if padsUI.drum {
		idx = padsUI.pad
	}
	d := &p.Pads[idx]
	title := padsPadLabel(idx)
	if n := padsPadName(d); n != "" {
		title += " · " + n
	}
	if medium != nil {
		renderText(renderer, config, medium, fitTextCached(medium, title, w), ColorTextPrimary(), x, y)
		y += synthFontH(medium) + 8
	}
	wave := sdl.Rect{X: x, Y: y, W: w, H: min32(110, rect.H/4)}
	padsDrawWave(renderer, config, small, wave, d, color, false)
	y += wave.H + 10
	if d.Sample != "" {
		line := padsModeNames[padsClampInt(d.Mode, 0, padsModeCount-1)] + " · " + padsSemis(d.Pitch)
		if d.Choke > 0 {
			line += " · Choke " + strconv.Itoa(d.Choke)
		}
		if d.Reverse {
			line += " · Reverse"
		}
		renderText(renderer, config, small, fitTextCached(small, line, w), ColorTextSecondary(), x, y)
		y += synthFontH(small) + 6
	}
	var hints []string
	if padsUI.drum {
		hints = append(hints, "Finger drumming: the D-pad plays the upper row, Y X B A the lower row.",
			"L2 / R2 move to the next two rows; Select stops drumming.")
	} else {
		hints = append(hints, "A plays the pad, Y picks its sound, X edits it.",
			"Select turns on finger drumming: eight pads under your thumbs.")
	}
	if p.RecLive {
		hints = append(hints, "Hits are recorded into the pattern while it plays.")
	}
	hints = append(hints, "Sticks play effects (FX page).")
	for _, h := range hints {
		if y+synthFontH(small) > rect.Y+rect.H {
			break
		}
		renderText(renderer, config, small, fitTextCached(small, h, w), ColorTextTertiary(), x, y)
		y += synthFontH(small) + 4
	}
}

// padsDrawWave draws a pad's sound in rect: the trimmed region, or with
// whole set the full sound with the trim shown.
func padsDrawWave(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, d *padsPad, color sdl.Color, whole bool) {
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassFill())
	if d.Sample == "" {
		if font != nil {
			msg := "No sound yet"
			renderText(renderer, config, font, msg, ColorTextTertiary(), rect.X+(rect.W-synthTextW(font, msg))/2, rect.Y+(rect.H-synthFontH(font))/2)
		}
		return
	}
	cols := int(rect.W-16) / 2
	start, end := d.Start, d.End
	if whole {
		start, end = 0, 1
	}
	w := padsWave(d.Sample, start, end, cols)
	if w == nil {
		padsRequest(d.Sample, nil)
		if font != nil {
			msg := "Loading…"
			if st := padsSampleState(d.Sample); st != "" && st != "loading" {
				msg = "Could not load: " + st
			}
			msg = fitTextCached(font, msg, rect.W-16)
			renderText(renderer, config, font, msg, ColorTextTertiary(), rect.X+(rect.W-synthTextW(font, msg))/2, rect.Y+(rect.H-synthFontH(font))/2)
		}
		return
	}
	mid := rect.Y + rect.H/2
	half := float32(rect.H/2 - 6)
	for c, v := range w {
		if d.Reverse && !whole {
			v = w[len(w)-1-c]
		}
		x := rect.X + 8 + int32(c)*2
		h := int32(v * half)
		if h < 1 {
			h = 1
		}
		a := uint8(220)
		if whole {
			f := float64(c) / float64(len(w))
			if f < d.Start || f > d.End {
				a = 60
			}
		}
		_ = renderer.SetDrawColor(color.R, color.G, color.B, a)
		_ = renderer.FillRect(&sdl.Rect{X: x, Y: mid - h, W: 1, H: 2 * h})
	}
	if whole {
		for _, f := range []float64{d.Start, d.End} {
			x := rect.X + 8 + int32(f*float64(rect.W-16))
			_ = renderer.SetDrawColor(255, 255, 255, 200)
			_ = renderer.FillRect(&sdl.Rect{X: x, Y: rect.Y + 4, W: 2, H: rect.H - 8})
		}
	}
}

func padsDrawBeat(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *padsProject, live *padsLiveView) {
	pat := &p.Patterns[p.Current]
	steps := padsStepsValid(pat.Steps)
	padsUI.step = padsClampInt(padsUI.step, 0, steps-1)
	page := padsUI.step / 16
	bank := padsUI.bank
	color := padsBankColors[bank]
	infoH := int32(26)
	rowH := max32((rect.H-infoH)/padsPerBank, 14)
	nameW := min32(170, rect.W/5)
	gx := rect.X + nameW + 8
	groupGap := int32(6)
	pitch := (rect.W - nameW - 8 - 3*groupGap) / 16
	showHead := live.playing && live.firedPat == p.Current && live.step/16 == page && live.step >= 0
	// Every hit of the page, by pad.
	cells := map[int]padsHit{}
	for _, h := range pat.Hits {
		if h.Pad/padsPerBank == bank && h.Step/16 == page {
			cells[(h.Pad%padsPerBank)*16+h.Step%16] = h
		}
	}
	top := rect.Y + infoH
	if showHead {
		col := synthStepRect(gx, top-2, pitch, groupGap, rowH*padsPerBank+2, live.step%16)
		fillRoundedRect(renderer, col.X-2, col.Y, col.W+4, col.H, 6, sdl.Color{R: 255, G: 255, B: 255, A: 26})
	}
	for row := 0; row < padsPerBank; row++ {
		idx := bank*padsPerBank + row
		d := &p.Pads[idx]
		y := top + int32(row)*rowH
		ch := rowH - 3
		nr := sdl.Rect{X: rect.X, Y: y, W: nameW, H: ch}
		fillRoundedRect(renderer, nr.X, nr.Y, nr.W, nr.H, 6, glassFill())
		if fl := padsFlashLevel(idx, live); fl > 0.05 && d.Sample != "" {
			fillRoundedRect(renderer, nr.X, nr.Y, nr.W, nr.H, 6, WithAlpha(color, uint8(70*fl)))
		}
		if font != nil {
			label := padsPadLabel(idx)
			if n := padsPadName(d); n != "" {
				label += " " + n
			}
			ink := ColorTextPrimary()
			if d.Sample == "" || d.Mute {
				ink = ColorTextTertiary()
			}
			renderText(renderer, config, font, fitTextCached(font, label, nr.W-12), ink, nr.X+6, nr.Y+(ch-synthFontH(font))/2)
		}
		for s := 0; s < 16; s++ {
			c := synthStepRect(gx, y, pitch, groupGap, ch, s)
			abs := page*16 + s
			if abs >= steps {
				break
			}
			h, on := cells[row*16+s]
			if !on {
				a := uint8(14)
				if s%4 == 0 {
					a = 26
				}
				fillRoundedRect(renderer, c.X, c.Y, c.W, c.H, 5, sdl.Color{R: 255, G: 255, B: 255, A: a})
			} else {
				a := uint8(180)
				if h.Accent || (showHead && live.step == abs) {
					a = 255
				}
				if d.Mute {
					a = 70
				}
				fillRoundedRect(renderer, c.X, c.Y, c.W, c.H, 5, WithAlpha(color, a))
				if h.Accent {
					fillCircle(renderer, c.X+c.W-6, c.Y+6, 3, sdl.Color{R: 255, G: 255, B: 255, A: 230})
				}
				if h.Note != 0 && font != nil {
					txt := strconv.Itoa(h.Note)
					if h.Note > 0 {
						txt = "+" + txt
					}
					if tw := synthTextW(font, txt); tw <= c.W-2 && synthFontH(font) <= c.H {
						renderText(renderer, config, font, txt, ColorIconDark, c.X+(c.W-tw)/2, c.Y+(c.H-synthFontH(font))/2)
					}
				}
			}
			if row == padsUI.row && abs == padsUI.step {
				drawGlassFocusRing(renderer, c, 5)
			}
		}
	}
	if font == nil {
		return
	}
	info := fmt.Sprintf("Pattern %d · Bank %s · Steps %d-%d of %d", p.Current+1, padsBankName(bank), page*16+1, min(steps, page*16+16), steps)
	cur := bank*padsPerBank + padsUI.row
	info += " · " + padsPadLabel(cur) + " step " + strconv.Itoa(padsUI.step+1)
	if i := pat.hitAt(cur, padsUI.step); i >= 0 {
		h := pat.Hits[i]
		info += " · " + padsSemis(h.Note)
		if h.Accent {
			info += " · Accent"
		}
	}
	renderText(renderer, config, font, fitTextCached(font, info, rect.W), ColorTextSecondary(), rect.X, rect.Y)
}

func padsDrawPadPage(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *padsProject, live *padsLiveView) {
	i := padsClampInt(padsUI.pad, 0, padsCount-1)
	d := &p.Pads[i]
	color := padsBankColors[i/padsPerBank]
	waveH := min32(84, rect.H/5)
	labelW := int32(0)
	if font != nil {
		label := padsPadLabel(i)
		labelW = synthTextW(font, label) + 24
		chip := sdl.Rect{X: rect.X, Y: rect.Y, W: labelW, H: waveH}
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 10, WithAlpha(color, uint8(90+150*padsFlashLevel(i, live))))
		renderText(renderer, config, font, label, ColorIconDark, chip.X+12, chip.Y+(chip.H-synthFontH(font))/2)
		labelW += 10
	}
	padsDrawWave(renderer, config, font, sdl.Rect{X: rect.X + labelW, Y: rect.Y, W: rect.W - labelW, H: waveH}, d, color, true)
	rows := padsPadRows(i)
	body := sdl.Rect{X: rect.X, Y: rect.Y + waveH + 10, W: rect.W, H: rect.H - waveH - 10}
	padsDrawRows(renderer, config, font, body, p, rows, padsUI.padRow, color, (len(rows)+1)/2)
}

// padsDrawRows draws rows in two columns, the first split of them on the
// left: each a label, a slider (or the choice) and its value, plus a live
// meter when the row has one.
func padsDrawRows(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *padsProject, rows []padsRow, cur int, color sdl.Color, split int) {
	n := len(rows)
	split = padsClampInt(split, 1, max(1, n))
	if n == 0 {
		return
	}
	perCol := max(split, n-split)
	rowH := min32(rect.H/int32(perCol), 44)
	if rowH < 20 {
		rowH = 20
	}
	colGap := int32(20)
	colW := (rect.W - colGap) / 2
	cur = padsClampInt(cur, 0, n-1)
	for i, r := range rows {
		col, row := 0, i
		if i >= split {
			col, row = 1, i-split
		}
		box := sdl.Rect{X: rect.X + int32(col)*(colW+colGap), Y: rect.Y + int32(row)*rowH, W: colW, H: rowH - 4}
		padsDrawRow(renderer, config, font, box, p, r, i == cur, color)
	}
}

// padsDrawRow draws one row in box.
func padsDrawRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, box sdl.Rect, p *padsProject, r padsRow, focused bool, color sdl.Color) {
	drawGlassSurface(renderer, box, 8, glassState{Focused: focused, NoLift: true})
	if r.live != nil {
		if v := padsClamp01(r.live()); v > 0.01 {
			fillRoundedRect(renderer, box.X+4, box.Y+box.H-5, int32(float64(box.W-8)*v), 3, 1, WithAlpha(color, 230))
		}
	}
	if font == nil {
		return
	}
	ty := box.Y + (box.H-synthFontH(font))/2
	labelW := box.W * 2 / 5
	ink := ColorTextPrimary()
	if r.kind == padsRowAction && r.text == nil {
		ink = ColorTextAccent()
	}
	renderText(renderer, config, font, fitTextCached(font, r.label, labelW-14), ink, box.X+12, ty)
	val := ""
	if r.text != nil {
		val = r.text(p)
	}
	maxVal := box.W - labelW - 12
	if r.kind == padsRowSlider {
		maxVal = min32(maxVal, 110)
	}
	val = fitTextCached(font, val, maxVal)
	vw := synthTextW(font, val)
	renderText(renderer, config, font, val, ColorTextSecondary(), box.X+box.W-12-vw, ty)
	if r.kind == padsRowSlider && r.get != nil {
		tx := box.X + labelW
		tw := box.W - labelW - 12 - max32(vw, 56) - 12
		if tw > 30 {
			th := int32(6)
			tyy := box.Y + box.H/2 - th/2
			fillRoundedRect(renderer, tx, tyy, tw, th, th/2, sdl.Color{R: 255, G: 255, B: 255, A: 30})
			fw := int32(float64(tw) * padsClamp01(r.get(p)))
			if fw > 0 {
				fillRoundedRect(renderer, tx, tyy, fw, th, th/2, WithAlpha(color, 230))
			}
			fillCircle(renderer, tx+fw, tyy+th/2, 6, WithAlpha(color, 255))
		}
	}
}

func padsDrawSong(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *padsProject, live *padsLiveView) {
	set := padsSongSettingRows()
	colGap := int32(20)
	colW := (rect.W - colGap) / 2
	rowH := min32(rect.H/int32(len(set)), 44)
	cur := padsUI.songRow
	for i, r := range set {
		box := sdl.Rect{X: rect.X, Y: rect.Y + int32(i)*rowH, W: colW, H: rowH - 4}
		padsDrawRow(renderer, config, font, box, p, r, i == cur, glassAccent())
	}
	x := rect.X + colW + colGap
	if font == nil {
		return
	}
	if len(p.Song) == 0 {
		y := rect.Y + 8
		for _, line := range []string{
			"The song is empty.",
			"\"Add pattern to the song\" puts the pattern on show",
			"at the end; \"Start plays\" chooses the song or the loop.",
		} {
			renderText(renderer, config, font, fitTextCached(font, line, colW), ColorTextTertiary(), x, y)
			y += synthFontH(font) + 4
		}
		return
	}
	slotH := int32(36)
	visible := max(1, int(rect.H/slotH))
	k := cur - len(set)
	if k >= 0 {
		if k < padsUI.songTop {
			padsUI.songTop = k
		}
		if k >= padsUI.songTop+visible {
			padsUI.songTop = k - visible + 1
		}
	}
	padsUI.songTop = padsClampInt(padsUI.songTop, 0, max(0, len(p.Song)-visible))
	for j := 0; j < visible && padsUI.songTop+j < len(p.Song); j++ {
		s := padsUI.songTop + j
		box := sdl.Rect{X: x, Y: rect.Y + int32(j)*slotH, W: colW, H: slotH - 4}
		focused := s == k
		drawGlassSurface(renderer, box, 8, glassState{Focused: focused, NoLift: true})
		pat := padsClampInt(p.Song[s], 0, padsPatterns-1)
		if live.playing && p.SongMode && live.songPos == s {
			fillRoundedRect(renderer, box.X, box.Y, 6, box.H, 3, glassAccent())
		}
		label := fmt.Sprintf("%d.  Pattern %d", s+1, pat+1)
		renderText(renderer, config, font, label, ColorTextPrimary(), box.X+16, box.Y+(box.H-synthFontH(font))/2)
		info := fmt.Sprintf("%d steps", padsStepsValid(p.Patterns[pat].Steps))
		if p.Patterns[pat].empty() {
			info = "empty"
		}
		renderText(renderer, config, font, info, ColorTextTertiary(), box.X+box.W-12-synthTextW(font, info), box.Y+(box.H-synthFontH(font))/2)
	}
}

// padsDrawStrip is the bottom band: the eight patterns and the output scope.
func padsDrawStrip(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *padsProject, live *padsLiveView) {
	box := rect.H - 8
	x := rect.X
	for i := 0; i < padsPatterns; i++ {
		r := sdl.Rect{X: x, Y: rect.Y + 4, W: box, H: box}
		playing := live.playing && live.firedPat == i
		switch {
		case playing:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(glassAccent(), 200))
		case !p.Patterns[i].empty():
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, sdl.Color{R: 255, G: 255, B: 255, A: 40})
		default:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, glassFill())
		}
		if i == p.Current {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, 3, glassAccent())
		}
		if font != nil {
			n := strconv.Itoa(i + 1)
			ink := ColorTextSecondary()
			if playing {
				ink = ColorIconDark
			}
			renderText(renderer, config, font, n, ink, r.X+(r.W-synthTextW(font, n))/2, r.Y+(r.H-synthFontH(font))/2)
		}
		x += box + 8
	}
	sc := sdl.Rect{X: x + 16, Y: rect.Y + 4, W: rect.X + rect.W - x - 16, H: box}
	if sc.W < 60 {
		return
	}
	fillRoundedRect(renderer, sc.X, sc.Y, sc.W, sc.H, 10, glassFill())
	mid := sc.Y + sc.H/2
	pts := make([]sdl.Point, 0, 128)
	for i := int32(0); i < 128; i++ {
		v := live.scope[int(i)*padsScopeLen/128]
		pts = append(pts, sdl.Point{X: sc.X + 8 + i*(sc.W-16)/127, Y: mid - int32(float32(sc.H/2-4)*v)})
	}
	c := padsBankColors[padsUI.bank]
	_ = renderer.SetDrawColor(c.R, c.G, c.B, 230)
	_ = renderer.DrawLines(pts)
}

// padsDrawBrowser draws the sample browser over the page.
func padsDrawBrowser(renderer *sdl.Renderer, config *Config, small, medium *ttf.Font, body sdl.Rect) {
	b := &padsUI.br
	fillRoundedRect(renderer, body.X, body.Y, body.W, body.H, 0, sdl.Color{R: 0, G: 0, B: 0, A: 150})
	panel := sdl.Rect{X: body.X + body.W/10, Y: body.Y + 10, W: body.W * 8 / 10, H: body.H - 20}
	drawGlassSurface(renderer, panel, 16, glassState{NoLift: true})
	if small == nil || medium == nil {
		return
	}
	title := "Sound for pad " + padsPadLabel(b.target)
	renderText(renderer, config, medium, title, ColorTextPrimary(), panel.X+20, panel.Y+14)
	where := "Where sounds come from"
	if b.dir == padsKitPrefix {
		where = "Built-in kit"
	} else if b.dir != "" {
		where = b.dir
	}
	y := panel.Y + 14 + synthFontH(medium) + 4
	renderText(renderer, config, small, fitTextCached(small, where, panel.W-40), ColorTextTertiary(), panel.X+20, y)
	y += synthFontH(small) + 10
	if b.err != "" {
		renderText(renderer, config, small, fitTextCached(small, "Could not open: "+b.err, panel.W-40), ColorToastWarn, panel.X+20, y)
		y += synthFontH(small) + 6
	}
	if len(b.rows) == 0 {
		renderText(renderer, config, small, "No sound files here (WAV, MP3, OGG, FLAC, M4A, OPUS)", ColorTextTertiary(), panel.X+20, y)
		return
	}
	rowH := int32(40)
	visible := max(1, int((panel.Y+panel.H-12-y)/rowH))
	if b.cur < b.top {
		b.top = b.cur
	}
	if b.cur >= b.top+visible {
		b.top = b.cur - visible + 1
	}
	b.top = padsClampInt(b.top, 0, max(0, len(b.rows)-visible))
	for j := 0; j < visible && b.top+j < len(b.rows); j++ {
		i := b.top + j
		row := b.rows[i]
		box := sdl.Rect{X: panel.X + 14, Y: y + int32(j)*rowH, W: panel.W - 28, H: rowH - 4}
		if i == b.cur {
			drawGlassSurface(renderer, box, 8, glassState{Focused: true, NoLift: true})
		}
		label := row.label
		if row.dir {
			label += " /"
		}
		ink := ColorTextPrimary()
		if row.action != "" {
			ink = ColorTextAccent()
		}
		dw := min32(box.W/2, synthTextW(small, row.detail))
		renderText(renderer, config, small, fitTextCached(small, label, box.W-dw-36), ink, box.X+12, box.Y+(box.H-synthFontH(small))/2)
		det := fitTextCached(small, row.detail, dw)
		renderText(renderer, config, small, det, ColorTextTertiary(), box.X+box.W-12-synthTextW(small, det), box.Y+(box.H-synthFontH(small))/2)
	}
}
