package main

import (
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Screensaver: constellation particles + rotating quotes
//
// The starfield is the classic idle screen (the dock in ambient.go replaces it
// by default). It is a lock screen in Home's language: the time and date in the
// top-left, the Wi-Fi and battery chips in the top-right (saver_status.go), the
// picture chosen in Files as a full-screen backdrop when there is one, and
// whatever is still playing in a card in the middle.
// ──────────────────────────────────────────────────────────────────────

// ssChatNote is one message that arrived while the device was idle. The
// screensaver shows the newest few, so a chat answer is visible without the
// user having to wake the panel first.
type ssChatNote struct {
	sender string
	text   string
	at     time.Time
}

var ssChatFeed = struct {
	mu    sync.Mutex
	notes []ssChatNote
}{}

const (
	ssChatFeedLimit = 8
	ssChatNoteTTL   = 10 * time.Minute
	ssChatShown     = 3
)

// noteScreensaverChat records an incoming message for the idle screen. It is
// deliberately cheap and callable from the Discord goroutine.
func noteScreensaverChat(sender, text string) {
	sender = strings.TrimSpace(sender)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	ssChatFeed.mu.Lock()
	defer ssChatFeed.mu.Unlock()
	ssChatFeed.notes = append(ssChatFeed.notes, ssChatNote{sender: sender, text: text, at: time.Now()})
	if len(ssChatFeed.notes) > ssChatFeedLimit {
		ssChatFeed.notes = ssChatFeed.notes[len(ssChatFeed.notes)-ssChatFeedLimit:]
	}
}

// screensaverChatNotes returns the newest notes, oldest first, dropping ones
// that have aged out so yesterday's conversation does not reappear.
func screensaverChatNotes(now time.Time, limit int) []ssChatNote {
	if limit <= 0 {
		return nil
	}
	ssChatFeed.mu.Lock()
	defer ssChatFeed.mu.Unlock()
	var recent []ssChatNote
	for _, note := range ssChatFeed.notes {
		if now.Sub(note.at) <= ssChatNoteTTL {
			recent = append(recent, note)
		}
	}
	if len(recent) > limit {
		recent = recent[len(recent)-limit:]
	}
	return recent
}

// screensaverIdleTimeout is how long the device waits without input before the
// starfield appears. 25 seconds is the shipped default; the settings row and
// Quick Settings both cycle this value at runtime.
var screensaverIdleTimeout = 25 * time.Second

const (
	ssStarCount          = 120
	ssConstellationCount = 8
	ssQuoteInterval      = 12 * time.Second
	ssFadeInDuration     = 1500

	// ssBackdropArmWindow is how long the idle screen's clear button stays armed
	// after its first press. Every other input on this screen means "wake up",
	// so the most likely press of that button is not a request to forget the
	// photo: the first press asks, and the second one inside this window answers.
	ssBackdropArmWindow = 5 * time.Second

	// ssBackdropNoticeTTL is how long the confirmation stays up. Nothing else is
	// drawn over the idle screen — no toast layer reaches here — so this line is
	// the whole receipt for the photo having been forgotten.
	ssBackdropNoticeTTL = 3 * time.Second

	// saverNodeSpeed scales the constellation drift. It is applied per second of
	// real time, so the low-power frame rate slows the animation's cadence,
	// not its speed.
	saverNodeSpeed = 1.0
)

var screensaverQuotes = []string{
	"The best interface is no interface.",
	"Good design is as little design as possible.",
	"Simplicity is the ultimate sophistication.",
	"The details are not the details. They make the design.",
	"Design is how it works.",
	"Perfection is achieved when there is nothing left to take away.",
	"Your device. Your way.",
	"Code is poetry.",
	"Ship it.",
	"Make it work, make it right, make it fast.",
	"Talk is cheap. Show me the code.",
	"Progress, not perfection.",
}

type ssStar struct {
	x, y    float64
	size    float64
	bright  float64
	twinkle float64
	speed   float64
	r, g, b uint8
}

type ssNode struct {
	x, y    float64
	vx, vy  float64
	size    float64
	bright  float64
	phase   float64
	group   int
	r, g, b uint8
}

type screensaverState struct {
	active          bool
	fadingIn        bool
	fadeAlpha       uint8
	startTick       uint64
	lastTick        uint64
	lastQuoteChange time.Time
	quoteIndex      int
	stars           []ssStar
	nodes           []ssNode
	connections     [][2]int
	inited          bool
	// backdropArmedAt is when the clear button was pressed the first time, so
	// the second press inside ssBackdropArmWindow can act. Zero when unarmed.
	backdropArmedAt time.Time
	// backdropClearedAt is when the backdrop photo was last forgotten, which is
	// how long the screen keeps saying so.
	backdropClearedAt time.Time
}

var ss screensaverState

func initScreensaver() {
	ss.stars = make([]ssStar, ssStarCount)
	for i := range ss.stars {
		s := &ss.stars[i]
		s.x = rand.Float64() * float64(screenWidth)
		s.y = rand.Float64() * float64(screenHeight)
		s.size = 1 + rand.Float64()*2
		s.bright = 0.3 + rand.Float64()*0.7
		s.twinkle = rand.Float64() * math.Pi * 2
		s.speed = 0.5 + rand.Float64()*2
		if rand.Float64() < 0.15 {
			s.r = 200 + uint8(rand.Intn(56))
			s.g = 160 + uint8(rand.Intn(40))
			s.b = 120 + uint8(rand.Intn(40))
		} else {
			s.r = 180 + uint8(rand.Intn(76))
			s.g = 200 + uint8(rand.Intn(56))
			s.b = 240 + uint8(rand.Intn(16))
		}
	}

	ss.nodes = make([]ssNode, ssConstellationCount*5)
	for i := range ss.nodes {
		n := &ss.nodes[i]
		n.group = i % ssConstellationCount
		centerX := float64(screenWidth) * (0.15 + 0.7*float64(n.group)/float64(ssConstellationCount))
		centerY := float64(screenHeight) * (0.2 + 0.6*rand.Float64())
		n.x = centerX + (rand.Float64()-0.5)*200
		n.y = centerY + (rand.Float64()-0.5)*140
		n.vx = (rand.Float64() - 0.5) * 8
		n.vy = (rand.Float64() - 0.5) * 6
		n.size = 2 + rand.Float64()*3
		n.bright = 0.5 + rand.Float64()*0.5
		n.phase = rand.Float64() * math.Pi * 2
		n.r = 120 + uint8(rand.Intn(80))
		n.g = 160 + uint8(rand.Intn(60))
		n.b = 240 + uint8(rand.Intn(16))
	}

	ss.connections = ss.connections[:0]
	for i := range ss.nodes {
		n1 := &ss.nodes[i]
		bestDist := math.MaxFloat64
		bestIdx := -1
		for j := range ss.nodes {
			if i == j || ss.nodes[j].group != n1.group {
				continue
			}
			n2 := &ss.nodes[j]
			dx := n1.x - n2.x
			dy := n1.y - n2.y
			d := dx*dx + dy*dy
			if d < bestDist && d < 150*150 {
				bestDist = d
				bestIdx = j
			}
		}
		if bestIdx >= 0 {
			dup := false
			for _, c := range ss.connections {
				if (c[0] == i && c[1] == bestIdx) || (c[0] == bestIdx && c[1] == i) {
					dup = true
					break
				}
			}
			if !dup {
				ss.connections = append(ss.connections, [2]int{i, bestIdx})
			}
		}
	}

	ss.quoteIndex = rand.Intn(len(screensaverQuotes))
	ss.lastQuoteChange = time.Now()
	ss.inited = true
}

// screensaverNotifyInput wakes the screensaver on any input. It runs on the
// event thread, which also draws the screensaver, so the wake is immediate
// instead of waiting for a poll.
func screensaverNotifyInput() {
	noteUserInputTime(time.Now())
	ambientNotifyInput()
	if ss.active {
		deactivateScreensaver()
	}
}

// screensaverAsleep reports whether the screen is showing nothing the user can
// act on: the screensaver is up, or the dock has darkened the panel.
func screensaverAsleep() bool {
	return ss.active || ambientIsScreenOff()
}

// screensaverWakeOnPress registers one press — a key, a pad button, a click, a
// wheel step — and reports whether it was spent waking the screen. Such a
// press is consumed: the user could not see what it would have pressed, and
// the dock's B used to leave the dock (A to pause the music) on the same press
// that only meant "wake up".
func screensaverWakeOnPress() bool {
	asleep := screensaverAsleep()
	screensaverNotifyInput()
	return asleep
}

// wakeInputKind separates the code spaces wakeSwallow remembers.
type wakeInputKind uint8

const (
	wakeInputPad wakeInputKind = iota
	wakeInputKey
	wakeInputMouse
)

type wakeInputCode struct {
	kind wakeInputKind
	code int32
}

// wakeSwallowed holds the presses that were spent waking the screen, until
// their release. Main thread only.
var wakeSwallowed map[wakeInputCode]bool

// wakeSwallowPress is screensaverWakeOnPress for an input with a release. A
// press spent on waking is remembered so its release (and a held key's
// auto-repeat) is swallowed with it: several surfaces act on release, and a
// release arriving without its press acted on a screen that was dark when the
// press happened. Releases themselves never wake — the release of the very
// button that darkened the screen (X on the dock, a Sleep Screen action) used
// to switch it straight back on.
func wakeSwallowPress(kind wakeInputKind, code int32, repeat bool) bool {
	k := wakeInputCode{kind, code}
	if wakeSwallowed[k] {
		if repeat {
			return true // auto-repeat of a key still held from the wake
		}
		// A fresh press of an input whose release never came back here
		// (another event loop took it): drop the stale mark and judge this
		// press on its own, so one lost release cannot swallow a button
		// for good.
		delete(wakeSwallowed, k)
	}
	if !screensaverWakeOnPress() {
		return false
	}
	if wakeSwallowed == nil {
		wakeSwallowed = make(map[wakeInputCode]bool)
	}
	wakeSwallowed[k] = true
	return true
}

// wakeSwallowRelease reports whether a release belongs to a press that was
// spent waking the screen, and forgets that press.
func wakeSwallowRelease(kind wakeInputKind, code int32) bool {
	k := wakeInputCode{kind, code}
	if !wakeSwallowed[k] {
		return false
	}
	delete(wakeSwallowed, k)
	return true
}

// axisWakeThreshold is how far a stick or trigger must move to count as input.
// Half the navigation deadzone: a deliberate nudge counts, while a stick that
// rests slightly off centre — drift is common on these handhelds — no longer
// holds the screensaver off forever, wakes the darkened dock all night, or
// keeps the frame rate at its active cadence.
const axisWakeThreshold = stickDeadzone / 2

// axisDeflected reports whether one axis value is past axisWakeThreshold.
func axisDeflected(v int16) bool {
	return v > axisWakeThreshold || v < -axisWakeThreshold
}

// axisWakeLatched holds an axis whose movement woke the screen until it comes
// back to rest, so the push that only meant "wake up" does not also move the
// focus or page a list (a trigger fires its press at a deeper value than the
// one that woke the screen, a few events later). Sized above SDL's six axes.
// Main thread only.
var axisWakeLatched [8]bool

// axisWakeFilter registers one stick or trigger event with the idle machinery
// and reports whether the screen may act on it.
func axisWakeFilter(axis uint8, value int16) bool {
	deflected := axisDeflected(value)
	if int(axis) >= len(axisWakeLatched) {
		if deflected {
			screensaverNotifyInput()
		}
		return true
	}
	if axisWakeLatched[axis] {
		if !deflected {
			axisWakeLatched[axis] = false
		}
		return false
	}
	if !deflected {
		return true
	}
	if screensaverWakeOnPress() {
		axisWakeLatched[axis] = true
		return false
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Clearing the backdrop photo from the idle screen
//
// The idle screen is the only thing that draws the backdrop photo, so it is
// also the place to take it away: without this the only way back to the default
// starfield was to wake the panel, open Files, open the same picture and use the
// viewer's lock button. The screen wakes on any input, which is why the clear is
// a two-press action — the first press only arms it.
// ──────────────────────────────────────────────────────────────────────

// screensaverClearKey reports whether a keyboard key is the idle screen's
// backdrop-clear button. The key is spelled out in the hint the screen draws, so
// the button and the sentence naming it come from the same place.
func screensaverClearKey(sym sdl.Keycode, mod uint16) bool {
	return sym == sdl.K_y && mod&uint16(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) == 0
}

// screensaverClearButton reports whether a pad press is that same button.
func screensaverClearButton(button uint8) bool {
	return button == sdl.CONTROLLER_BUTTON_Y
}

// screensaverBackdropClearable reports whether there is a backdrop photo to
// forget. With none set the clear button is not offered, and a press of it is an
// ordinary wake.
func screensaverBackdropClearable(config *Config) bool {
	return screenlockImagePath(config) != ""
}

// screensaverBackdropArmed reports whether the first press happened and its
// window is still open.
func screensaverBackdropArmed(now time.Time) bool {
	return !ss.backdropArmedAt.IsZero() && now.Sub(ss.backdropArmedAt) <= ssBackdropArmWindow
}

// screensaverBackdropNotice reports whether the clear happened recently enough
// for the screen to still confirm it.
func screensaverBackdropNotice(now time.Time) bool {
	return !ss.backdropClearedAt.IsZero() && now.Sub(ss.backdropClearedAt) <= ssBackdropNoticeTTL
}

// screensaverBackdropHint is the line the idle screen draws above its "press any
// key" hint: what the clear button does right now, or the receipt for having
// done it. Empty when there is no photo to forget, so a default saver is exactly
// the screen it was before.
func screensaverBackdropHint(config *Config, now time.Time) string {
	switch {
	case screensaverBackdropNotice(now):
		return "Backdrop cleared · any key wakes"
	case !screensaverBackdropClearable(config):
		return ""
	case screensaverBackdropArmed(now):
		return "Y again to clear"
	default:
		return "Y clear photo"
	}
}

// screensaverBackdropPress answers one press of the clear button and reports
// whether it consumed the press. A consumed press leaves the idle screen asleep;
// an unconsumed one falls through to the normal wake, which is what a press with
// no backdrop to forget should do.
func screensaverBackdropPress(config *Config, now time.Time) bool {
	if !screensaverBackdropClearable(config) {
		ss.backdropArmedAt = time.Time{}
		return false
	}
	if !screensaverBackdropArmed(now) {
		ss.backdropArmedAt = now
		return true
	}
	ss.backdropArmedAt = time.Time{}
	if err := clearScreenlockImage(config); err != nil {
		// The photo is still there, so say why instead of pretending it is gone.
		logWarn("idle", "could not clear the backdrop: %v", err)
		return true
	}
	ss.backdropClearedAt = now
	return true
}

// saverStarfieldClockSize is the starfield clock: large enough to read from a
// desk, small enough that the clock and date stay above the media card.
func saverStarfieldClockSize() int {
	return int(saverScaled(88, 40))
}

// renderScreensaver draws one frame of the screensaver. Returns true while
// active so the caller skips normal UI rendering.
//
// Every layer is drawn at full strength and the fade-in is one veil of the dark
// base laid over the finished frame: fading each label separately made every
// fade step a new cached text texture.
func renderScreensaver(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	if !ss.inited {
		initScreensaver()
	}

	ss.active = true
	tickNow := tick

	// Fade in.
	if ss.fadingIn {
		elapsed := tickNow - ss.startTick
		if elapsed >= uint64(ssFadeInDuration) {
			ss.fadingIn = false
			ss.fadeAlpha = 255
		} else {
			ss.fadeAlpha = uint8(255 * float64(elapsed) / float64(ssFadeInDuration))
		}
	}
	now := time.Now()

	renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, 255)
	renderer.Clear()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	// A picture chosen in Files is the backdrop, scaled once to fill the
	// screen. A missing or undecodable file leaves the starfield exposed.
	drawScreenlockImage(renderer, config, 255)
	photo := saverLockShowing(config)
	if !photo {
		saverDrawStarfield(renderer, tickNow)
	}

	// Whatever is still playing owns the middle of the screen: the live video
	// frame, or the track's cover art with equaliser bars. The quote only shows
	// when nothing is running and no photo is up, so nothing stacks on it.
	media := screensaverNowPlaying()
	drawScreensaverMedia(renderer, config, media, 255)
	if now.Sub(ss.lastQuoteChange) > ssQuoteInterval {
		ss.quoteIndex = (ss.quoteIndex + 1) % len(screensaverQuotes)
		ss.lastQuoteChange = now
	}
	if media.kind == saverMediaNone && !photo {
		if font, _ := getCachedFont(config, "small"); font != nil {
			q := ellipsize(font, screensaverQuotes[ss.quoteIndex], screenWidth-2*saverScaled(64, 24))
			qw, _ := measureText(font, q)
			renderText(renderer, config, font, q, saverInkSoft, (screenWidth-qw)/2, screenHeight/2+30)
		}
	}

	// The time and date top-left, the status chips top-right: one glance says
	// what time it is, whether the device is online and whether it needs
	// charging.
	margin := saverScaled(48, 20)
	top := saverScaled(34, 14)
	saverDrawClock(renderer, config, margin, top, saverStarfieldClockSize(), now)
	saverDrawStatusRow(renderer, config, screenWidth-margin, top, now)
	m := saverChipSize()
	alarmRight := screenWidth - margin
	if next := alarmNextUpcoming(); next != nil {
		font := saverChipFont(config)
		if font != nil {
			saverDrawLabelChip(renderer, font, "alarm", "Alarm "+clockDigitsHM(config, next.Hour, next.Minute), alarmRight, top+m.h+saverScaled(10, 6))
		}
	}

	drawScreensaverChat(renderer, config, now)
	saverDrawWakeHints(renderer, config, tickNow, now)

	// The fade-in: the finished frame emerges from the dark base.
	if ss.fadeAlpha < 255 {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, 255-ss.fadeAlpha)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}
	return true
}

// saverDrawStarfield draws the twinkling stars and the drifting
// constellations. Drift is per second of real time, so the slower low-power
// frame rate does not slow the sky down.
func saverDrawStarfield(renderer *sdl.Renderer, tick uint64) {
	dt := 0.016
	if ss.lastTick > 0 && tick > ss.lastTick {
		dt = float64(tick-ss.lastTick) / 1000
		if dt > 0.25 {
			dt = 0.25
		}
	}
	ss.lastTick = tick

	// Stars.
	for i := range ss.stars {
		s := &ss.stars[i]
		twinkle := math.Sin(float64(tick)*0.001*s.speed + s.twinkle)
		alpha := uint8(s.bright * (0.5 + 0.5*twinkle) * 255)
		if alpha == 0 {
			continue
		}
		renderer.SetDrawColor(s.r, s.g, s.b, alpha)
		sx, sy := int32(s.x), int32(s.y)
		renderer.DrawPoint(sx, sy)
		if s.size > 1.5 {
			renderer.DrawPoint(sx+1, sy)
			renderer.DrawPoint(sx-1, sy)
			renderer.DrawPoint(sx, sy+1)
			renderer.DrawPoint(sx, sy-1)
		}
	}

	// Constellation connections.
	for _, c := range ss.connections {
		n1 := &ss.nodes[c[0]]
		n2 := &ss.nodes[c[1]]
		dist := math.Sqrt((n1.x-n2.x)*(n1.x-n2.x) + (n1.y-n2.y)*(n1.y-n2.y))
		if dist > 200 {
			continue
		}
		alpha := uint8(40 * (1 - dist/200))
		if alpha == 0 {
			continue
		}
		renderer.SetDrawColor(100, 140, 200, alpha)
		renderer.DrawLine(int32(n1.x), int32(n1.y), int32(n2.x), int32(n2.y))
	}

	// Constellation nodes.
	for i := range ss.nodes {
		n := &ss.nodes[i]
		n.x += n.vx * dt * saverNodeSpeed
		n.y += n.vy * dt * saverNodeSpeed
		if n.x < -20 {
			n.x = float64(screenWidth) + 20
		} else if n.x > float64(screenWidth)+20 {
			n.x = -20
		}
		if n.y < -20 {
			n.y = float64(screenHeight) + 20
		} else if n.y > float64(screenHeight)+20 {
			n.y = -20
		}
		pulse := 0.7 + 0.3*math.Sin(float64(tick)*0.002+n.phase)
		alpha := uint8(n.bright * pulse * 255)
		if alpha == 0 {
			continue
		}
		drawParticle(renderer, int32(n.x), int32(n.y), int32(n.size),
			n.r, n.g, n.b, alpha)
	}
}

// saverDrawWakeHints draws the bottom of the screen: the backdrop's clear
// button (or its receipt) as a chip, and the wake hint under it. The hint's
// pulse is stepped, so it reuses a handful of cached text textures instead of
// rendering a new one for every frame's alpha.
func saverDrawWakeHints(renderer *sdl.Renderer, config *Config, tick uint64, now time.Time) {
	hintFont, _ := getCachedFont(config, "small")
	if hintFont == nil {
		return
	}
	hint := "Press any key to wake"
	hw, hh := measureText(hintFont, hint)
	hy := screenHeight - 50
	pulse := 0.55 + 0.35*math.Sin(float64(tick)*0.003)
	step := math.Round(pulse*6) / 6
	ink := saverInkSoft
	ink.A = uint8(255 * step)
	renderText(renderer, config, hintFont, hint, ink, (screenWidth-hw)/2, hy)

	// The backdrop's clear button, on its own chip above the wake hint and
	// steady rather than pulsing: it is an action. Green while the receipt is
	// up, amber once the first press has armed it, the glass ink while it is
	// still an offer — so the armed line does not read as the same sentence.
	line := screensaverBackdropHint(config, now)
	if line == "" {
		return
	}
	g := saverGlassStyle()
	icon := "image"
	lineInk := g.ink
	switch {
	case screensaverBackdropNotice(now):
		icon = "check"
		lineInk = sdl.Color{R: 96, G: 206, B: 140, A: 255}
	case screensaverBackdropArmed(now):
		icon = "warning"
		lineInk = sdl.Color{R: 236, G: 178, B: 74, A: 255}
	}
	m := saverChipSize()
	font := saverChipFont(config)
	if font == nil {
		return
	}
	lw, lh := measureText(font, line)
	w := m.pad + m.icon + m.gap + lw + m.pad
	rect := sdl.Rect{X: (screenWidth - w) / 2, Y: hy - m.h - 6, W: w, H: m.h}
	if hh > 0 && rect.Y+rect.H > hy+hh/3 {
		rect.Y = hy + hh/3 - rect.H
	}
	saverDrawChipBody(renderer, rect, g)
	saverDrawIcon(renderer, icon, sdl.Rect{X: rect.X + m.pad, Y: rect.Y + (m.h-m.icon)/2, W: m.icon, H: m.icon}, false, g)
	drawText(renderer, font, line, rect.X+m.pad+m.icon+m.gap, rect.Y+(m.h-lh)/2, lineInk, textAlignLeft)
}

// drawScreensaverChat paints the newest incoming messages as a quiet card in
// the lower-left corner, above the wake hint.
func drawScreensaverChat(renderer *sdl.Renderer, config *Config, now time.Time) {
	notes := screensaverChatNotes(now, ssChatShown)
	if len(notes) == 0 {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	lineH := int32(font.Height()) + 6
	cardH := int32(38) + lineH*int32(len(notes))
	drawChatCard(renderer, config, notes, 255, saverScaled(48, 20), screenHeight-112-cardH)
}

// drawChatCard paints the newest incoming messages in the lower-left area of an
// idle screen. The caller places it (above the wake hint on the saver, above the
// now-playing card on the dock) and passes the fade its screen uses.
func drawChatCard(renderer *sdl.Renderer, config *Config, notes []ssChatNote, alpha uint8, x, y int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil || len(notes) == 0 || alpha < 60 {
		return
	}
	lineH := int32(font.Height()) + 6
	cardW := int32(620)
	if cardW > screenWidth-96 {
		cardW = screenWidth - 96
	}
	cardH := int32(38) + lineH*int32(len(notes))

	g := saverGlassStyle()
	a := float64(alpha) / 255.0
	scale := func(c sdl.Color) sdl.Color {
		c.A = uint8(float64(c.A) * a)
		return c
	}
	accent := glassAccent()
	fillRoundedRect(renderer, x, y, cardW, cardH, 16, scale(g.fill))
	strokeRoundedRect(renderer, x, y, cardW, cardH, 16, 1, scale(g.edge))
	fillRoundedRect(renderer, x+8, y+12, 4, cardH-24, 2, scale(accent))

	renderText(renderer, config, font, "NEW CHAT", scale(accent), x+22, y+10)
	lineY := y + 34
	for _, note := range notes {
		sender := note.sender
		if sender == "" {
			sender = "Chat"
		}
		label := sender + ": "
		sw, _ := measureText(font, label)
		renderText(renderer, config, font, label, scale(g.ink), x+22, lineY)
		textW := cardW - 40 - sw
		if textW > 0 {
			renderText(renderer, config, font, ellipsize(font, note.text, textW), scale(g.muted), x+22+sw, lineY)
		}
		lineY += lineH
	}
}

// deactivateScreensaver ends the screensaver and returns to normal UI.
func deactivateScreensaver() {
	// The low-power state ends first, so the frame drawn next — the first
	// awake one — already runs at the full clock.
	saverPowerWake()
	ss.active = false
	ss.fadingIn = false
	// Waking is not the first half of a clear: the next sleep starts with the
	// clear button at rest, whatever this session left armed.
	ss.backdropArmedAt = time.Time{}
	// The uploaded photo is several megabytes of texture; its scaled rendition
	// stays cached, so the next idle screen uploads it again without decoding.
	releaseScreenlockTexture()
}

// lastInputUnixNano tracks when the user last interacted. It is an atomic
// because the idle-monitor goroutines read it while the event loop writes it:
// a plain time.Time there would be a data race, and a torn read on 32-bit ARM.
var lastInputUnixNano atomic.Int64

func init() { lastInputUnixNano.Store(time.Now().UnixNano()) }

// noteUserInputTime records one interaction for the monitors.
func noteUserInputTime(t time.Time) {
	lastInputUnixNano.Store(t.UnixNano())
}

// userInputAge is how long ago the last interaction happened. A missing
// timestamp counts as "just now" so a monitor never enters the idle screen
// before the app has started.
func userInputAge(now time.Time) time.Duration {
	ns := lastInputUnixNano.Load()
	if ns == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, ns))
}

// startScreensaverMonitor prepares the screensaver state. Activation itself
// runs on the event thread in screensaverAutoTick: the entry check reads modal
// flags and the player/download state, which only that thread may touch, and a
// one-second check starts the saver promptly instead of up to five seconds
// late the way the old polling goroutine did.
func startScreensaverMonitor() {
	if !ss.inited {
		initScreensaver()
	}
}

// screensaverAutoCheckInterval throttles the idle check. One second keeps the
// start close to the configured timeout without doing work every frame.
const screensaverAutoCheckInterval = time.Second

var screensaverLastCheck time.Time

// screensaverAutoTick starts the screensaver once the device has been quiet for
// the configured timeout and nothing is using the panel. It also drives the
// idle screens' low-power state, which the dock needs as much as the saver.
func screensaverAutoTick(config *Config, now time.Time) {
	saverPowerTick(now)
	if config == nil || ss.active {
		return
	}
	// The dock/ambient screen is the idle screen when it is enabled; two idle
	// overlays competing for the panel is worse than either alone.
	if ambientModeEnabled(config) || ambientIsActive() {
		return
	}
	if !screensaverLastCheck.IsZero() && now.Sub(screensaverLastCheck) < screensaverAutoCheckInterval {
		return
	}
	screensaverLastCheck = now
	if idleScreenHoldOff(config) {
		// Playback, a download or an install is use without input. Keep the
		// idle clock moving so the countdown restarts when it finishes rather
		// than dropping straight onto the saver the second it ends.
		noteUserInputTime(now)
		return
	}
	if screensaverIdleTimeout <= 0 || userInputAge(now) < screensaverIdleTimeout {
		return
	}
	activateScreensaver()
}

// idleScreenHoldOffFrom is the pure decision at the heart of the gate: the
// screen stays awake while a modal owns the UI, while audio or video plays, and
// while a download or install is running.
func idleScreenHoldOffFrom(uiBlocked, musicPlaying, musicPaused, videoBusy, downloadActive, patchBusy bool) bool {
	return uiBlocked || musicPlaying || musicPaused || videoBusy || downloadActive || patchBusy
}

// idleScreenHoldOff reports whether something is using the device right now, so
// running it in the background is not the same as being idle.
func idleScreenHoldOff(config *Config) bool {
	_, _, _, playing, paused := ambientNowPlaying()
	return idleScreenHoldOffFrom(
		ambientEntryBlocked(config),
		playing,
		paused,
		videoHoldsScreenAwake(),
		installInProgress(),
		patchInstallBusy(),
	)
}

// videoHoldsScreenAwake reports whether the video player owns the panel: a video
// that is playing, paused or still downloading. This gate looked at music alone,
// so a video left running went dark under the idle screen partway through
// playback while the same state already held the dock screen off
// (ambientEntryBlocked counts any non-idle media player). The user asked for the
// opposite: a video that is on screen keeps playing, and the saver waits.
func videoHoldsScreenAwake() bool {
	phase := snapshotVideoState().phase
	return phase != "" && phase != "idle"
}

// installInProgress reports whether a package is being fetched or unpacked, so
// the progress bar does not disappear behind an idle screen mid-download.
func installInProgress() bool {
	visible, _, phase, _, _, _, _, _, ending := installBarSnapshot()
	if !visible || ending {
		return false
	}
	switch phase {
	case installPhaseResolving, installPhaseDownload, installPhaseExtract, installPhaseVerify:
		return true
	}
	return false
}

// patchInstallBusy reports whether a saved patch is being applied.
func patchInstallBusy() bool {
	return strings.TrimSpace(CurrentPatchBusyLabel()) != ""
}

// activateScreensaver starts the fade-in. Runs on the event thread.
func activateScreensaver() {
	if !ss.inited {
		initScreensaver()
	}
	ss.active = true
	ss.fadingIn = true
	ss.fadeAlpha = 0
	ss.startTick = sdl.GetTicks64()
	ss.lastTick = 0
	// Each appearance offers the clear afresh: an old arm must not let a single
	// stray press clear the photo minutes later.
	ss.backdropArmedAt = time.Time{}
	ss.backdropClearedAt = time.Time{}
	// The status chips are re-read as the screen appears, not a full refresh
	// interval later.
	saverWifiInvalidate()
}

// applyScreensaverTimeout sets the idle timeout everywhere it is needed: the
// package timer the monitor reads, the config field, the Quick Settings row and
// the saved user config.
func applyScreensaverTimeout(config *Config, seconds int) int {
	if seconds <= 0 {
		seconds = 25
	}
	screensaverIdleTimeout = time.Duration(seconds) * time.Second
	if len(qs.cycles) > 0 {
		qs.cycles[qsCycleScreensaverRow].Value = seconds
	}
	if config != nil {
		config.Variables.ScreensaverTimeout = seconds
		config.Variables.Custom["screensaverTimeout"] = strconv.Itoa(seconds)
		// The Settings row shows $screensaverTimeoutLabel, and substitution
		// reads only the custom block: keep the derived text in step with the
		// value everywhere the timeout is changed (Settings and Quick Settings).
		config.Variables.Custom["screensaverTimeoutLabel"] = screenTimeoutLabel(seconds)
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	return seconds
}
