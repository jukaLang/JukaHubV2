package main

// usbpad_ui.go — the Controller mode screen (usbpad.go), reached from Tools →
// Controller mode and Settings → Network → Controller mode (USB).
//
// Before it starts the screen is a short form: what the kernel offers (the
// capability check runs on a goroutine on every visit), the device shape —
// PC (Xbox 360 compatible) or Generic HID gamepad — the button letters (by
// position, as an Xbox pad has them, or as printed on the handheld), and
// Start. While controller mode is on it shows one big line — connected to a
// computer, or waiting for the cable — the shape, how to leave, and a live
// tester of every button, stick and trigger as the computer gets them. The
// panel dims after a few seconds without input and wakes on the next one;
// the handheld does not go to sleep while the screen is up, and ten minutes
// without a computer end controller mode on their own.
//
// Leaving the screen any way at all stops controller mode (a scene task
// waits for the scene's end). With a computer connected the pad is grabbed
// and the screen sees no presses: Select + Start held for two seconds is the
// way back (usbpad.go), Esc on a keyboard too.

import (
	"context"
	"runtime"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// usbPadSceneName is the scene the Tools hub and Settings open.
const usbPadSceneName = "USBGamepad"

// usbPadDimAfter is how long the panel stays lit without input.
const usbPadDimAfter = 8 * time.Second

// usbPadScreen is the screen's own state, main thread only.
var usbPadScreen = struct {
	visit   tbxVisit
	nav     tbxNav
	mode    string
	printed bool
	chosen  bool // the user picked a shape: the check's default no longer applies
	lastGen uint64
}{mode: usbPadModeXbox}

// usbPadOpenScreen opens Controller mode. Cross-area contract: the Tools hub
// and Settings → Network call it.
func usbPadOpenScreen(config *Config) {
	tbxOpenScene(config, usbPadSceneName)
}

type usbPadScene struct{ sceneBase }

func (usbPadScene) render(r *sdl.Renderer, c *Config) bool {
	if usbPadScreen.visit.begin() {
		usbPadScreen.nav.scroll = 0
		usbPadProbeAsync()
		// Leaving the screen, whichever way, ends controller mode.
		runSceneTask(func(ctx context.Context) {
			<-ctx.Done()
			tbxPost(usbPadSceneLeft)
		})
	}
	usbPadRenderScreen(r, c)
	return true
}

func (usbPadScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e != nil && e.Type == sdl.KEYDOWN && e.Keysym.Sym == sdl.K_ESCAPE && usbPadOn() {
		usbPadStopFromScreen()
		return true
	}
	return tbxDispatch(c, tbxKeyAction(e), usbPadSceneAction)
}

func (usbPadScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	// A connected computer owns the pad; a press that still arrives (the grab
	// failed) is the computer's, not the screen's.
	if s := usbPadSnapshot(); s.phase == usbPadActive && s.connected {
		return true
	}
	return tbxDispatch(c, tbxPadAction(e), usbPadSceneAction)
}

func init() { registerScene(usbPadScene{sceneBase{name: usbPadSceneName}}) }

// usbPadOn reports controller mode starting, on or stopping.
func usbPadOn() bool { return usbPadSnapshot().phase != usbPadIdle }

// usbPadSceneLeft runs when the screen's scene ended.
func usbPadSceneLeft() {
	if tbxCurrentScene(appConfig) == usbPadSceneName {
		return
	}
	usbPadStopAsync()
	usbPadUndim()
}

// usbPadStopFromScreen is B (or Esc) while controller mode is on: off, and
// the form again.
func usbPadStopFromScreen() {
	PlayBackSound()
	usbPadUndim()
	if usbPadStopAsync() {
		ShowToast("USB controller off — the USB port is the handheld's again", ToastKindInfo)
	}
}

// ── Each frame ────────────────────────────────────────────────────────────

// usbPadTick keeps the handheld awake and the frame rate up while the pad
// moves, dims the panel when it does not, and ends controller mode after ten
// minutes without a computer.
func usbPadTick(snap usbPadSnap) {
	if snap.phase != usbPadActive {
		usbPadUndim()
		return
	}
	now := time.Now()
	noteUserInputTime(now)
	if snap.gen != usbPadScreen.lastGen {
		usbPadScreen.lastGen = snap.gen
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
	}
	if now.Sub(snap.lastInput) >= usbPadDimAfter {
		usbPadDim()
	} else {
		usbPadUndim()
	}
	if !snap.connected && now.Sub(snap.waitSince) >= usbPadNoHostLimit {
		usbPadUndim()
		if usbPadStopAsync() {
			ShowToast("USB controller off: no computer for 10 minutes", ToastKindInfo)
		}
	}
}

// ── Drawing ───────────────────────────────────────────────────────────────

func usbPadRenderScreen(renderer *sdl.Renderer, config *Config) {
	snap := usbPadSnapshot()
	caps := usbPadCapsNow()
	usbPadTick(snap)
	if caps.checked && !usbPadScreen.chosen && !caps.canXbox() && caps.canHID() {
		usbPadScreen.mode = usbPadModeHID
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status, statusColor := "Off", ColorTextMuted()
	switch {
	case snap.phase == usbPadActive && snap.connected:
		status, statusColor = "Connected", ToastSuccessInk()
	case snap.phase == usbPadActive:
		status = "Waiting for a computer"
	case snap.phase == usbPadStarting:
		status = "Starting…"
	case snap.phase == usbPadStopping:
		status = "Stopping…"
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader("USB controller", "The handheld as a USB controller for a computer", "gamepad", status, statusColor))
	body := frame.bodyRect()
	nav := &usbPadScreen.nav
	nav.begin(body.Y, body.H)
	switch {
	case runtime.GOOS != "linux":
		tbxDrawEmpty(renderer, config, body, "gamepad", "For the handheld",
			"USB controller turns the handheld's USB-C port into a controller for a computer. On a computer there is nothing to turn on.")
	case snap.phase != usbPadIdle:
		usbPadDrawActive(renderer, config, fonts, body, snap)
	default:
		usbPadDrawForm(renderer, config, fonts, frame, body, snap, caps)
	}
	nav.end()
	tbxEndFrame(renderer, config)
}

// usbPadCapRow is one line of the capability check.
func usbPadCapRows(caps usbPadCaps) []tbxRowSpec {
	if !caps.checked {
		return []tbxRowSpec{{icon: "plug", title: "Checking what this kernel offers…", dim: true}}
	}
	port := tbxRowSpec{icon: "plug", title: "USB device port", pill: "Ready", tone: tbxToneGood, subtitle: caps.udc + " · kernel " + caps.kernel}
	switch {
	case !caps.configfs:
		port.pill, port.tone, port.subtitle = "Missing", tbxToneBad, "This firmware's kernel has no USB gadget support"
	case caps.udc == "":
		port.pill, port.tone, port.subtitle = "Missing", tbxToneBad, "No USB device controller: the port only works as a host"
	}
	hid := tbxRowSpec{icon: "gamepad", title: "Kernel HID gadget", pill: "Available", tone: tbxToneGood, subtitle: "Generic HID uses the kernel's own HID function"}
	if !caps.hid {
		hid.pill, hid.tone, hid.subtitle = "Not in this kernel", tbxToneNeutral, "Generic HID answers the HID requests itself, over FunctionFS"
		if !caps.ffs {
			hid.subtitle = "Generic HID needs this or FunctionFS"
		}
	}
	ffs := tbxRowSpec{icon: "code", title: "FunctionFS", pill: "Available", tone: tbxToneGood, subtitle: "Carries the Xbox 360 shape (USB file transfer uses it too)"}
	if !caps.ffs {
		ffs.pill, ffs.tone, ffs.subtitle = "Not available", tbxToneBad, "The Xbox 360 shape needs it"
	}
	if caps.problem != "" && caps.configfs {
		port.subtitle = caps.problem
	}
	return []tbxRowSpec{port, hid, ffs}
}

// usbPadModeHelp is the line under the shape buttons.
func usbPadModeHelp(mode string, printed bool) string {
	letters := "A is the bottom button, as on an Xbox pad"
	if printed {
		letters = "A is the button printed A (on the right)"
	}
	if mode == usbPadModeHID {
		return "macOS, Android, Linux and DirectInput games · no rumble · " + letters
	}
	return "Windows (XInput games, Steam) and Linux · rumble · " + letters
}

func usbPadDrawForm(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame, body sdl.Rect, snap usbPadSnap, caps usbPadCaps) {
	nav := &usbPadScreen.nav
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	info := func(spec tbxRowSpec) {
		tbxDrawRow(renderer, config, fonts, sdl.Rect{X: x, Y: top + y, W: w, H: rowH}, spec, false)
		y += rowH + 8
	}
	buttons := func(ids, labels []string, selected string) {
		const btnH, gap = int32(48), int32(12)
		n := int32(len(ids))
		bw := (w - gap*(n-1)) / n
		for i, id := range ids {
			rect := sdl.Rect{X: x + int32(i)*(bw+gap), Y: y, W: bw, H: btnH}
			screen := rect
			screen.Y += top
			tbxDrawButton(renderer, config, fonts.body, screen, labels[i], id == selected, nav.focused(id), false)
			nav.add(id, rect, row, false)
		}
		row++
		y += btnH + 10
	}

	if snap.err != "" {
		info(tbxRowSpec{icon: "warning", title: "USB controller did not start", subtitle: snap.err, pill: "Not started", tone: tbxToneBad})
		y += 6
	}

	tbxDrawSection(renderer, config, "This handheld", "cpu", x, top+y, w)
	y += tbxSectionHeight
	for _, spec := range usbPadCapRows(caps) {
		info(spec)
	}
	y += 6

	tbxDrawSection(renderer, config, "Shows up on the computer as", "gamepad", x, top+y, w)
	y += tbxSectionHeight
	buttons([]string{"mode:" + usbPadModeXbox, "mode:" + usbPadModeHID}, []string{"PC (Xbox 360 compatible)", "Generic HID gamepad"}, "mode:"+usbPadScreen.mode)
	selected := "btn:position"
	if usbPadScreen.printed {
		selected = "btn:printed"
	}
	buttons([]string{"btn:position", "btn:printed"}, []string{"Letters by position", "Letters as printed"}, selected)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, usbPadModeHelp(usbPadScreen.mode, usbPadScreen.printed), w), ColorTextMuted(), x+4, top+y)
	y += fonts.smallH + 14

	start := sdl.Rect{X: x, Y: y, W: w, H: 56}
	startScreen := start
	startScreen.Y += top
	tbxDrawButton(renderer, config, fonts.title, startScreen, "Start USB controller", true, nav.focused("start"), !usbPadModeReady(caps, usbPadScreen.mode))
	nav.add("start", start, row, false)
	y += 56 + 14

	port := "While it is on, the port is the controller's: USB file transfer pauses and comes back after. Host mode (OTG) cannot be on at the same time."
	if !snap.restoreMTP && !usbMtpEnabled(config) {
		port = "While it is on, the port is the controller's. Host mode (OTG) cannot be on at the same time."
	}
	info(tbxRowSpec{icon: "plug", title: "One thing at a time on the port", subtitle: port, dim: true})

	nav.extend(y)
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
}

// usbPadModeReady reports whether the kernel can carry mode.
func usbPadModeReady(caps usbPadCaps, mode string) bool {
	if !caps.checked {
		return false
	}
	if mode == usbPadModeHID {
		return caps.canHID()
	}
	return caps.canXbox()
}

// usbPadDrawActive is the screen while controller mode is on: the big line,
// the shape and the way out, and the tester.
func usbPadDrawActive(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect, snap usbPadSnap) {
	title := "USB controller — waiting for cable"
	sub := "Plug the handheld into a computer with a USB-C data cable"
	leave := "B stops the USB controller"
	switch {
	case snap.phase == usbPadStarting:
		title, sub = "Starting the USB controller…", "Getting the USB port ready"
	case snap.phase == usbPadStopping:
		title, sub = "Stopping the USB controller…", "Giving the USB port back"
	case snap.connected:
		title = "USB controller — connected to PC"
		sub = "Every button goes to the computer"
		leave = "Hold Select + Start for 2 seconds to leave"
		if hintsUseKeyboard() {
			leave = "Hold Select + Start for 2 seconds, or press Esc, to leave"
		}
	}
	shape := usbPadModeLabel(snap.mode)
	if snap.transport != "" {
		shape += " · over " + snap.transport
	}
	card := sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: 124}
	UIListRow(renderer, card, glassRowRadius, false, false)
	big := FontForRole(config, TypeHeading)
	if big == nil {
		big = fonts.title
	}
	_, bigH := measureText(big, title)
	ty := card.Y + 14
	titleColor := ColorTextPrimary()
	if snap.connected {
		titleColor = ToastSuccessInk()
	}
	renderText(renderer, config, big, ellipsize(big, title, card.W-36), titleColor, card.X+18, ty)
	ty += bigH + 6
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub+" · "+shape, card.W-36), ColorTextMuted(), card.X+18, ty)
	ty += fonts.smallH + 4
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, leave, card.W-36), ColorTextPrimary(), card.X+18, ty)

	area := sdl.Rect{X: body.X, Y: card.Y + card.H + 14, W: body.W, H: body.H - card.H - 14}
	if snap.note != "" {
		area.H -= fonts.smallH + 8
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, snap.note, body.W), ColorTextMuted(), body.X+4, area.Y+area.H+4)
	}
	if area.H > 120 {
		usbPadDrawTester(renderer, config, fonts.tiny, area, snap)
	}
}

// usbPadDrawTester draws a controller with every input as the computer gets
// it: lit buttons, the sticks' dots, the triggers' bars.
func usbPadDrawTester(renderer *sdl.Renderer, config *Config, font *ttf.Font, area sdl.Rect, snap usbPadSnap) {
	st := snap.state
	on, off, rim := glassAccent(), glassA(30), glassA(70)
	pressed := func(bit uint32) bool { return st.buttons&bit != 0 }
	color := func(lit bool) sdl.Color {
		if lit {
			return on
		}
		return off
	}
	label := func(text string, cx, cy int32) {
		w, h := measureText(font, text)
		renderText(renderer, config, font, text, ColorTextPrimary(), cx-w/2, cy-h/2)
	}
	pill := func(text string, cx, cy, w, h int32, lit bool) {
		fillRoundedRect(renderer, cx-w/2, cy-h/2, w, h, h/2, color(lit))
		strokeRoundedRect(renderer, cx-w/2, cy-h/2, w, h, h/2, 1, rim)
		label(text, cx, cy)
	}
	r := area.H / 5
	if r > 54 {
		r = 54
	}
	if r < 22 {
		r = 22
	}
	ax, aw := area.X, area.W
	topY := area.Y + area.H/8
	upperY := area.Y + area.H*2/5
	lowerY := area.Y + area.H*3/4

	// Shoulders and triggers.
	trigger := func(text string, cx int32, v uint8, lit bool) {
		bw, bh := r*2, int32(16)
		x := cx - bw/2
		fillRoundedRect(renderer, x, topY-bh-6, bw, bh, bh/2, off)
		if v > 0 {
			fillRoundedRect(renderer, x, topY-bh-6, bw*int32(v)/255, bh, bh/2, on)
		}
		label(text, cx, topY-bh/2-6)
		pill(map[bool]string{true: "LB", false: "RB"}[text == "LT"], cx, topY+14, bw, 24, lit)
	}
	trigger("LT", ax+aw/5, st.lt, pressed(usbPadLB))
	trigger("RT", ax+aw*4/5, st.rt, pressed(usbPadRB))

	// Sticks: the ring, the dot where the stick is, lit when clicked.
	stick := func(cx, cy int32, sx, sy int16, click bool) {
		fillCircle(renderer, cx, cy, r, off)
		strokeCircle(renderer, cx, cy, r, rim)
		dx := int32(float64(sx) / 32767 * float64(r) * 0.7)
		dy := int32(-float64(sy) / 32767 * float64(r) * 0.7)
		fillCircle(renderer, cx+dx, cy+dy, r*2/5, color(click))
	}
	stick(ax+aw/5, upperY, st.lx, st.ly, pressed(usbPadLS))
	stick(ax+aw*3/5, lowerY, st.rx, st.ry, pressed(usbPadRS))

	// D-pad.
	d := r * 3 / 5
	dpx, dpy := ax+aw*2/5, lowerY
	fillRoundedRect(renderer, dpx-d/2, dpy-d*3/2, d, d, 6, color(pressed(usbPadUp)))
	fillRoundedRect(renderer, dpx-d/2, dpy+d/2, d, d, 6, color(pressed(usbPadDown)))
	fillRoundedRect(renderer, dpx-d*3/2, dpy-d/2, d, d, 6, color(pressed(usbPadLeft)))
	fillRoundedRect(renderer, dpx+d/2, dpy-d/2, d, d, 6, color(pressed(usbPadRight)))

	// Face buttons where they are on the handheld, named as the computer
	// gets them (the letters swap with "as printed").
	fx, fy := ax+aw*4/5, upperY
	br := r * 2 / 5
	gap := r * 4 / 5
	face := func(bit uint32, text string, cx, cy int32) {
		fillCircle(renderer, cx, cy, br, color(pressed(bit)))
		strokeCircle(renderer, cx, cy, br, rim)
		label(text, cx, cy)
	}
	bottom, right, left, top := usbPadA, usbPadB, usbPadX, usbPadY
	bl, rl, ll, tl := "A", "B", "X", "Y"
	if snap.printed {
		bottom, right, left, top = usbPadB, usbPadA, usbPadY, usbPadX
		bl, rl, ll, tl = "B", "A", "Y", "X"
	}
	face(bottom, bl, fx, fy+gap)
	face(right, rl, fx+gap, fy)
	face(left, ll, fx-gap, fy)
	face(top, tl, fx, fy-gap)

	// Select, Menu (the Xbox Guide button) and Start in the middle.
	mid := ax + aw/2
	pill("Select", mid-r*3/2, upperY, r*2, 26, pressed(usbPadBack))
	pill("Menu", mid, upperY-r*2/3, r*2, 26, pressed(usbPadGuide))
	pill("Start", mid+r*3/2, upperY, r*2, 26, pressed(usbPadStart))
	if snap.chord {
		label("Keep holding to leave…", mid, upperY+r*2/3)
	}
	if snap.rumble > 0 {
		pill("Rumble", mid, lowerY+r, r*3, 26, true)
	}
}

// ── Input ─────────────────────────────────────────────────────────────────

func usbPadSceneAction(config *Config, act tbxAction) bool {
	if usbPadOn() {
		if act == tbxActBack {
			usbPadStopFromScreen()
		}
		return true
	}
	nav := &usbPadScreen.nav
	switch act {
	case tbxActConfirm:
		usbPadActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

// usbPadSceneClick is a click or tap on the form.
func usbPadSceneClick(mx, my int32, config *Config) bool {
	if usbPadOn() {
		return true
	}
	it, ok := usbPadScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	usbPadScreen.nav.focus = it.id
	usbPadActivate(config, it.id)
	return true
}

// usbPadSceneHints is the footer: the form's keys, or the way out.
func usbPadSceneHints() []FooterHint {
	if runtime.GOOS != "linux" {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	snap := usbPadSnapshot()
	leave := FooterHint{Button: "Select + Start", Label: "Hold 2 s to leave"}
	if hintsUseKeyboard() {
		leave = FooterHint{Button: "Esc", Label: "Leave USB controller"}
	}
	switch {
	case snap.phase == usbPadActive && snap.connected:
		return []FooterHint{leave}
	case snap.phase != usbPadIdle:
		return []FooterHint{{Button: "B", Label: "Stop"}}
	}
	label := "Choose"
	if usbPadScreen.nav.focus == "start" {
		label = "Start"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: "Back"}}
}

func usbPadActivate(config *Config, id string) {
	caps := usbPadCapsNow()
	switch id {
	case "mode:" + usbPadModeXbox, "mode:" + usbPadModeHID:
		mode := id[len("mode:"):]
		if caps.checked && !usbPadModeReady(caps, mode) {
			PlayErrorSound()
			ShowToast(usbPadModeLabel(mode)+" is not possible with this firmware's kernel", ToastKindWarning)
			return
		}
		PlayActivateSound()
		usbPadScreen.mode, usbPadScreen.chosen = mode, true
	case "btn:position", "btn:printed":
		PlayActivateSound()
		usbPadScreen.printed = id == "btn:printed"
	case "start":
		switch {
		case !caps.checked:
			ShowToast("Still checking what this kernel offers — a moment", ToastKindInfo)
		case !usbPadModeReady(caps, usbPadScreen.mode):
			PlayErrorSound()
			ShowToast(usbPadModeLabel(usbPadScreen.mode)+" is not possible with this firmware's kernel", ToastKindWarning)
		default:
			PlayActivateSound()
			usbPadTurnOn(config, usbPadScreen.mode, usbPadScreen.printed)
		}
	}
}
