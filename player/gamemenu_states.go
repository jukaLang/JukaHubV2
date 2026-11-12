package main

// gamemenu_states.go — Save state and Load state in the in-game menu
// (gamemenu.go): six slots, each with its picture and when it was saved.
//
// Only a RetroArch game the library started has them. Its launch
// (prepareGameMenuStates, savestate_resume.go) turns on, for that launch only
// (racfgscope.go), RetroArch's network command interface, the screenshot
// beside every state and a known current slot (1), and notes the game and the
// command port here. The rows show when that launch is the game on screen and
// RetroArch is running: PPSSPP, a port or a program started elsewhere has no
// such interface, so the rows stay hidden rather than promise a save that
// cannot happen.
//
// The picker reads the slots while the game is stopped under the menu: the
// state files of slots 1–6 (gmetaListStates, the same reader as the states
// sheet before a game starts) and their screenshots, decoded small on a
// goroutine under the shared decode permit and uploaded as six small
// textures that are freed the moment the picker or the overlay closes. Only
// three rows are drawn at a time (a page; Up/Down moves between pages).
//
// Acting on a slot gives the game back first, exactly as Resume does (a
// stopped RetroArch reads no commands), then asks RetroArch over UDP:
//
//	SAVE_STATE_SLOT n / LOAD_STATE_SLOT n
//
// LOAD_STATE_SLOT exists since RetroArch 1.15, SAVE_STATE_SLOT only in newer
// builds; both answer. A build that does not know the command ignores it, so
// no answer within gmStateReplyWait means "too old": the current slot is moved
// from the one JukaHub knows (1 at launch, then wherever it last moved it)
// with STATE_SLOT_PLUS / STATE_SLOT_MINUS and the plain SAVE_STATE /
// LOAD_STATE is sent. Once a game's RetroArch has shown it does not know a
// command, later presses go straight to the fallback. RetroArch shows its own
// message for the result; JukaHub is not on screen then, so the outcome is
// only logged.

import (
	"context"
	"errors"
	"image"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gmStateSlots is how many slots the picker offers (1…gmStateSlots).
	gmStateSlots    = 6
	gmStatePageSize = 3
	// gmStateSlotAtLaunch is the state_slot a library launch sets.
	gmStateSlotAtLaunch = 1
	// gmStateReplyWait is how long a slot command waits for RetroArch's answer
	// before the older commands are used instead.
	gmStateReplyWait = 2 * time.Second
	// gmStateResumeDelay lets the resumed game run a few frames (and the pad
	// watcher give the pad back) before the first command is sent.
	gmStateResumeDelay = 300 * time.Millisecond
	// gmStateStepGap spaces the fallback's commands, one per datagram.
	gmStateStepGap = 80 * time.Millisecond
	// gmStateLaunchMatch is how close the launch JukaHub noted and the game on
	// screen must have started to be the same game.
	gmStateLaunchMatch = time.Minute
)

// gmStates is the RetroArch game the library last started with the command
// interface on. Written by the launch (main thread) and by the command
// goroutine (the slot), read by the menu.
var gmStates struct {
	mu   sync.Mutex
	rom  string
	port int
	at   time.Time
	slot int // RetroArch's current slot, as far as JukaHub moved it
	// The build ignored SAVE_STATE_SLOT / LOAD_STATE_SLOT: use the fallback.
	noSaveSlot, noLoadSlot bool
}

// gmStatesForget clears the record at every library launch.
func gmStatesForget() {
	gmStates.mu.Lock()
	gmStates.rom, gmStates.port, gmStates.at = "", 0, time.Time{}
	gmStates.slot = gmStateSlotAtLaunch
	gmStates.noSaveSlot, gmStates.noLoadSlot = false, false
	gmStates.mu.Unlock()
}

// gmStatesNoteLaunch records a RetroArch launch whose config has the command
// interface on at port.
func gmStatesNoteLaunch(rom string, port int) {
	gmStates.mu.Lock()
	gmStates.rom, gmStates.port, gmStates.at = rom, port, time.Now()
	gmStates.slot = gmStateSlotAtLaunch
	gmStates.noSaveSlot, gmStates.noLoadSlot = false, false
	gmStates.mu.Unlock()
}

// gmPick is the slot picker and what the menu learned about the game when it
// opened. Main thread only.
var gmPick struct {
	// checkedFor is the overlay opening the check below was made for; ra, rom
	// and port its answer.
	checkedFor time.Time
	ra         bool
	rom        string
	port       int

	open    bool
	save    bool // Save state, else Load state
	sel     int
	confirm int // the used slot whose replacement waits for a second press, -1 none
	slots   [gmStateSlots]gmetaState
	has     [gmStateSlots]bool
	tex     [gmStateSlots]*sdl.Texture
	texW    [gmStateSlots]int32
	texH    [gmStateSlots]int32
	gen     int
}

// gmPickLoad carries the decoded screenshots from the goroutine to the render
// thread.
var gmPickLoad struct {
	mu   sync.Mutex
	gen  int
	pics [gmStateSlots]*image.NRGBA
}

// gmStatesAvailable reports whether the paused game is a RetroArch game the
// library started with the command interface on. It is worked out once per
// opening of the overlay: looking for RetroArch reads /proc.
func gmStatesAvailable() bool {
	if !gaOverlay.active {
		return false
	}
	if gmPick.checkedFor.Equal(gaOverlay.openedAt) && !gmPick.checkedFor.IsZero() {
		return gmPick.ra
	}
	gmPick.checkedFor = gaOverlay.openedAt
	gmPick.ra, gmPick.rom, gmPick.port = false, "", 0
	gmStates.mu.Lock()
	rom, port, at := gmStates.rom, gmStates.port, gmStates.at
	gmStates.mu.Unlock()
	if rom == "" || port <= 0 || at.IsZero() || gaTrack.launchAt.IsZero() {
		return false
	}
	// The game on screen started right after that launch, not a port or a
	// later program.
	if d := gaTrack.launchAt.Sub(at); d < -gmStateLaunchMatch || d > gmStateLaunchMatch {
		return false
	}
	if !retroArchRunning() {
		return false
	}
	gmPick.ra, gmPick.rom, gmPick.port = true, rom, port
	return true
}

// gmPickOpen shows the picker for saving or loading: the six slots are read
// now, their screenshots decoded on a goroutine.
func gmPickOpen(save bool) {
	gmPickDrop()
	gmPick.open, gmPick.save = true, save
	gmPick.sel, gmPick.confirm = 0, -1
	for _, st := range gmetaListStates(gmPick.rom, gmetaStateDirs(gmPick.rom)) {
		i := st.Slot - 1
		if i < 0 || i >= gmStateSlots || gmPick.has[i] {
			continue // newest first: the first of a slot is the one RetroArch loads
		}
		gmPick.slots[i], gmPick.has[i] = st, true
	}
	if !save {
		// Load starts on the newest save there is.
		newest := -1
		for i := range gmPick.slots {
			if gmPick.has[i] && (newest < 0 || gmPick.slots[i].Time.After(gmPick.slots[newest].Time)) {
				newest = i
			}
		}
		if newest >= 0 {
			gmPick.sel = newest
		}
	}
	var thumbs [gmStateSlots]string
	found := false
	for i := range gmPick.slots {
		if gmPick.has[i] && gmPick.slots[i].Thumb != "" {
			thumbs[i], found = gmPick.slots[i].Thumb, true
		}
	}
	if !found {
		return
	}
	gen := gmPick.gen
	gmPickLoad.mu.Lock()
	gmPickLoad.gen, gmPickLoad.pics = gen, [gmStateSlots]*image.NRGBA{}
	gmPickLoad.mu.Unlock()
	go gmPickDecode(gen, thumbs)
}

// gmPickDecode decodes the screenshots small, one at a time, and hands each
// over while the picker it was for is still open.
func gmPickDecode(gen int, thumbs [gmStateSlots]string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "reading the save pictures failed: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, path := range thumbs {
		if path == "" {
			continue
		}
		gmPickLoad.mu.Lock()
		stale := gmPickLoad.gen != gen
		gmPickLoad.mu.Unlock()
		if stale {
			return
		}
		pic, err := gmetaLoadThumb(ctx, path)
		if err != nil {
			logDebug("gamemenu", "no picture for slot %d: %v", i+1, err)
			continue
		}
		gmPickLoad.mu.Lock()
		if gmPickLoad.gen == gen {
			gmPickLoad.pics[i] = pic
		}
		gmPickLoad.mu.Unlock()
	}
}

// gmPickDrop closes the picker and frees its pictures: back to the menu, or
// the overlay closing (gaClose). Main thread.
func gmPickDrop() {
	gmPick.open = false
	gmPick.gen++
	gmPickLoad.mu.Lock()
	gmPickLoad.gen, gmPickLoad.pics = -1, [gmStateSlots]*image.NRGBA{}
	gmPickLoad.mu.Unlock()
	for i := range gmPick.tex {
		if gmPick.tex[i] != nil {
			_ = gmPick.tex[i].Destroy()
			gmPick.tex[i] = nil
		}
	}
	gmPick.slots, gmPick.has = [gmStateSlots]gmetaState{}, [gmStateSlots]bool{}
	gmPick.texW, gmPick.texH = [gmStateSlots]int32{}, [gmStateSlots]int32{}
}

// gmPickPress acts on one press while the picker is up.
func gmPickPress(b gaButton) {
	switch b {
	case gaBtnUp:
		gmPick.sel = (gmPick.sel + gmStateSlots - 1) % gmStateSlots
		gmPick.confirm = -1
		PlayNavSound()
	case gaBtnDown:
		gmPick.sel = (gmPick.sel + 1) % gmStateSlots
		gmPick.confirm = -1
		PlayNavSound()
	case gaBtnB:
		PlayBackSound()
		gmPickDrop()
	case gaBtnMenu, gaBtnMenuChord:
		PlayBackSound()
		gaClose("the player went back", true)
	case gaBtnA:
		i := gmPick.sel
		switch {
		case !gmPick.save && !gmPick.has[i]:
			PlayErrorSound()
		case gmPick.save && gmPick.has[i] && gmPick.confirm != i:
			// A used slot is replaced on the second press only.
			gmPick.confirm = i
			PlayNavSound()
		default:
			gmPickAct(i + 1)
		}
	}
}

// gmPickAct gives the game back and has RetroArch save to or load slot.
func gmPickAct(slot int) {
	save, port, rom := gmPick.save, gmPick.port, gmPick.rom
	PlayActivateSound()
	what := "load slot " + strconv.Itoa(slot)
	if save {
		what = "save to slot " + strconv.Itoa(slot)
	}
	gaClose("the player chose to "+what, true)
	go gmStateCommand(save, slot, port, rom)
}

// gmStateCommand sends the slot command to the resumed RetroArch, with the
// older commands as the fallback (see the header). Its own goroutine.
func gmStateCommand(save bool, slot, port int, rom string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "the save-state command failed: %v", r)
		}
	}()
	time.Sleep(gmStateResumeDelay)
	word, plain := "LOAD_STATE_SLOT", "LOAD_STATE"
	if save {
		word, plain = "SAVE_STATE_SLOT", "SAVE_STATE"
	}
	gmStates.mu.Lock()
	same := gmStates.rom == rom
	skip := (save && gmStates.noSaveSlot) || (!save && gmStates.noLoadSlot)
	gmStates.mu.Unlock()
	if !same {
		return // another game started meanwhile
	}
	name := gameDisplayTitle(rom)
	if !skip {
		reply, err := askRetroArchNetCmd(port, word+" "+strconv.Itoa(slot), gmStateReplyWait)
		switch {
		case err == nil && strings.HasPrefix(strings.ToUpper(reply), word):
			logInfo("gamemenu", "%s: RetroArch answered %q", name, reply)
			return
		case err == nil:
			logInfo("gamemenu", "%s: unexpected answer to %s: %q; trying the older commands", name, word, reply)
		case gmNetTimeout(err):
			logInfo("gamemenu", "%s: RetroArch did not answer %s (older than the command); using %s", name, word, plain)
		default:
			logWarn("gamemenu", "%s: RetroArch's command port did not take %s: %v", name, word, err)
			return
		}
		gmStates.mu.Lock()
		if save {
			gmStates.noSaveSlot = true
		} else {
			gmStates.noLoadSlot = true
		}
		gmStates.mu.Unlock()
	}
	gmStates.mu.Lock()
	cur := gmStates.slot
	gmStates.mu.Unlock()
	step, n := "STATE_SLOT_PLUS", slot-cur
	if n < 0 {
		step, n = "STATE_SLOT_MINUS", -n
	}
	for k := 0; k < n; k++ {
		if err := sendRetroArchNetCmd(port, step); err != nil {
			logWarn("gamemenu", "%s: could not move RetroArch to slot %d: %v", name, slot, err)
			return
		}
		time.Sleep(gmStateStepGap)
	}
	gmStates.mu.Lock()
	if gmStates.rom == rom {
		gmStates.slot = slot
	}
	gmStates.mu.Unlock()
	time.Sleep(gmStateStepGap)
	if err := sendRetroArchNetCmd(port, plain); err != nil {
		logWarn("gamemenu", "%s: %s for slot %d was not sent: %v", name, plain, slot, err)
		return
	}
	logInfo("gamemenu", "%s: %s sent for slot %d (moved from slot %d)", name, plain, slot, cur)
}

// gmNetTimeout reports whether err is a read that timed out.
func gmNetTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gmPickTakePictures uploads the screenshots the goroutine finished.
func gmPickTakePictures(renderer *sdl.Renderer) {
	gmPickLoad.mu.Lock()
	defer gmPickLoad.mu.Unlock()
	if gmPickLoad.gen != gmPick.gen {
		return
	}
	for i, pic := range gmPickLoad.pics {
		if pic == nil {
			continue
		}
		gmPickLoad.pics[i] = nil
		if gmPick.tex[i] != nil {
			continue
		}
		if tex := imgvTexture(renderer, pic); tex != nil {
			gmPick.tex[i] = tex
			gmPick.texW[i], gmPick.texH[i] = int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
		}
	}
}

// gmPickSub is a slot's smaller line.
func gmPickSub(i int, now time.Time) string {
	if !gmPick.has[i] {
		if gmPick.save {
			return "Empty — save here"
		}
		return "Empty"
	}
	when := gmetaWhen(gmPick.slots[i].Time, now)
	if gmPick.save && gmPick.confirm == i {
		return orbKeyName("A") + " again to replace the save from " + when
	}
	return "Saved " + when
}

// gmPickDraw is the picker card: which action, the game, three slots per page with
// their pictures, and the buttons that work it.
func gmPickDraw(renderer *sdl.Renderer, fonts tbxFonts) {
	gmPickTakePictures(renderer)
	pad := orbPad
	rowGap := int32(6)
	kicker := "LOAD STATE"
	action := "load"
	if gmPick.save {
		kicker, action = "SAVE STATE", "save"
	}
	first := gmPick.sel / gmStatePageSize * gmStatePageSize
	kicker += " · " + strconv.Itoa(first+1) + "–" + strconv.Itoa(first+gmStatePageSize) + " OF " + strconv.Itoa(gmStateSlots)
	title := gaOverlay.title
	if title == "" {
		title = "Your game"
	}
	hint := "D-pad move · " + orbKeyName("A") + " " + action + " · " + orbKeyName("B") + " back"

	w := screenWidth * 50 / 100
	if minW := scaleX(480, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	inner := w - 2*pad
	thumbH := 2*fonts.bodyH + fonts.smallH
	thumbW := thumbH * 16 / 9
	rowH := thumbH + pad
	h := pad + fonts.tinyH + 2 + fonts.bodyH + pad + gmStatePageSize*(rowH+rowGap) + pad/2 + fonts.smallH + pad

	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	x, y := card.X+pad, card.Y+pad
	drawText(renderer, fonts.tiny, kicker, x, y, glassAccent(), textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, ellipsize(fonts.body, title, inner), x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH + pad

	accent := glassAccent()
	now := time.Now()
	for i := first; i < first+gmStatePageSize; i++ {
		row := sdl.Rect{X: card.X + pad/2, Y: y, W: card.W - pad, H: rowH}
		if i == gmPick.sel {
			f := accent
			f.A = 56
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, f)
			strokeRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, 2, accent)
		}
		box := sdl.Rect{X: row.X + pad/2, Y: row.Y + pad/2, W: thumbW, H: thumbH}
		well := sdl.Color{R: 0, G: 0, B: 0, A: 90}
		fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 6, well)
		if tex := gmPick.tex[i]; tex != nil {
			dst := gaFit(gmPick.texW[i], gmPick.texH[i], box.W, box.H)
			dst.X += box.X
			dst.Y += box.Y
			_ = renderer.Copy(tex, nil, &dst)
		} else if !gmPick.has[i] {
			drawText(renderer, fonts.small, "—", box.X+box.W/2, box.Y+(box.H-fonts.smallH)/2, ColorTextTertiary(), textAlignCenter)
		}
		tx := box.X + box.W + pad
		tw := row.X + row.W - pad - tx
		labelColor, subColor := ColorTextPrimary(), ColorTextSecondary()
		if !gmPick.save && !gmPick.has[i] {
			labelColor, subColor = ColorTextTertiary(), ColorTextTertiary()
		}
		ty := row.Y + (row.H-fonts.bodyH-2-fonts.smallH)/2
		drawText(renderer, fonts.body, "Slot "+strconv.Itoa(i+1), tx, ty, labelColor, textAlignLeft)
		drawText(renderer, fonts.small, ellipsize(fonts.small, gmPickSub(i, now), tw), tx, ty+fonts.bodyH+2, subColor, textAlignLeft)
		y += rowH + rowGap
	}
	y += pad / 2
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
}
