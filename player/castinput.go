package main

// castinput.go — a Screen Share viewer plays the handheld.
//
// The viewer page (companion_cast.go) turns a TV's or laptop's controller, a
// keyboard or a phone's touch pad into the handheld's own buttons and sends
// what is held, as a whole, over the /play socket (companion_castplay.go)
// whenever it changes and four times a second while anything is held. Here
// that state becomes presses:
//
//   - On a TrimUI handheld it is written into the built-in pad's evdev node
//     (castinput_linux.go), the way the VNC server's keys reach a game: every
//     program reading the pad — JukaHub's own screens, RetroArch, a port —
//     sees the buttons go down as if they were pressed, by position (the
//     viewer's right face button is the handheld's A), sticks and triggers
//     included. Nothing has to be found again, so a game that looked for
//     controllers when it started has the pad it found.
//   - Elsewhere (Windows), or when the pad cannot be written, the presses are
//     SDL events for JukaHub's own screens, with a held D-pad repeating the
//     way the handheld's does; games are out of reach and the page says so.
//
// Watching is not playing. A link holder asks (the page's Play button); the
// person holding the handheld answers in Start → Screen Share, where the
// request waits with the viewer's browser and address (a toast says it is
// there). A phone paired with the Phone Companion's PIN — which already types
// on the handheld — plays without asking. One viewer plays at a time; a grant
// is remembered for that browser until sharing stops or the link is replaced,
// so a dropped connection comes back without asking again. Settings →
// Screen Share → Controllers: Off refuses every request, and the startup lock
// and Kid/Kiosk mode refuse them too. X in the panel takes control back.
//
// Whatever ends a turn — the viewer stopping or leaving, the handheld taking
// it back, sharing or Controllers switched off, a new link, JukaHub closing —
// lets go of every held button first, and a viewer whose page goes quiet for
// a second while holding something is let go of too (a stalled network must
// not leave a button down in a game).

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The handheld's buttons as the page names them; bit i of a state's buttons.
const (
	castBtnUp = iota
	castBtnDown
	castBtnLeft
	castBtnRight
	castBtnA
	castBtnB
	castBtnX
	castBtnY
	castBtnL1
	castBtnR1
	castBtnL2
	castBtnR2
	castBtnSelect
	castBtnStart
	castBtnMenu
	castBtnL3
	castBtnR3
	castBtnCount
)

const (
	// castPadQuiet is how long a held state may go without a word from the
	// page before everything is let go.
	castPadQuiet = time.Second
	// castPadTick / castPadIdleTick pace the pad loop while something is held
	// (the firmware watch, a held D-pad's repeat) and while nothing is.
	castPadTick     = 8 * time.Millisecond
	castPadIdleTick = 50 * time.Millisecond
	// castCtlRemember is how long a grant is kept for a viewer that left: a
	// reload or a dropped Wi-Fi comes back without asking again.
	castCtlRemember = 2 * time.Hour
	// castCtlAskWait is how long a request waits for the handheld's answer.
	castCtlAskWait = 2 * time.Minute
	// castCtlMaxPending bounds the requests waiting at once.
	castCtlMaxPending = 4
)

// castPadState is what the playing viewer holds: buttons (bit castBtn*), the
// sticks (-1..1, down and right positive) and the triggers' travel (0..1).
type castPadState struct {
	buttons        uint32
	lx, ly, rx, ry float64
	lt, rt         float64
}

// has reports whether button b is held.
func (s castPadState) has(b int) bool { return s.buttons&(1<<uint(b)) != 0 }

// neutral reports whether nothing is held and the sticks rest.
func (s castPadState) neutral() bool {
	return s.buttons == 0 && s.lt < 0.05 && s.rt < 0.05 &&
		absFloat(s.lx) < 0.05 && absFloat(s.ly) < 0.05 && absFloat(s.rx) < 0.05 && absFloat(s.ry) < 0.05
}

// castCtlEvent is what a viewer's socket is told about its turn.
type castCtlEvent struct {
	State string // "asking", "playing", "denied", "stopped"
	Note  string
	Games bool // "playing": games hear the presses too
}

// castCtlClient is one viewer that asked to play.
type castCtlClient struct {
	key    string // the page's own random id, for a reconnect
	name   string // what the panel shows: browser, system and address
	viaPIN bool
	token  string // the link's token ("" for the PIN)
	at     time.Time
	events chan castCtlEvent
}

// tell queues an event for the client's socket; a full queue drops the
// oldest, since only the last word matters.
func (c *castCtlClient) tell(ev castCtlEvent) {
	for {
		select {
		case c.events <- ev:
			return
		default:
		}
		select {
		case <-c.events:
		default:
		}
	}
}

var castCtl struct {
	// turn serialises the changes of player with the pad loop's start and
	// stop, so a viewer leaving never stops the loop of the one let in next.
	// It is taken before mu; the pad loop takes neither of them for long and
	// never turn.
	turn       sync.Mutex
	mu         sync.Mutex
	owner      *castCtlClient
	pending    []*castCtlClient
	remembered map[string]time.Time // client key -> when the grant lapses
	announced  *castCtlClient       // the request the last toast was for
}

// castCtlNewClient is a viewer's handle: key is the page's id (made here when
// the page sent none or a malformed one).
func castCtlNewClient(key, userAgent, addr string, viaPIN bool, token string) *castCtlClient {
	if len(key) < 8 || len(key) > 64 || strings.Trim(key, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		raw := make([]byte, 8)
		_, _ = rand.Read(raw)
		key = hex.EncodeToString(raw)
	}
	return &castCtlClient{key: key, name: castClientName(userAgent, addr), viaPIN: viaPIN, token: token,
		events: make(chan castCtlEvent, 4)}
}

// castClientName is a viewer as the panel names it: "Chrome on Android ·
// 192.168.1.23". Only plain characters are kept.
func castClientName(ua, addr string) string {
	browser := "A browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		system = "iOS"
	case strings.Contains(ua, "Tizen"), strings.Contains(ua, "Web0S"), strings.Contains(ua, "SMART-TV"), strings.Contains(ua, "SmartTV"):
		system = "a TV"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		system = "a Mac"
	case strings.Contains(ua, "CrOS"):
		system = "a Chromebook"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	name := browser
	if system != "" {
		name += " on " + system
	}
	if ip := net.ParseIP(castHost(addr)); ip != nil {
		name += " · " + ip.String()
	}
	return name
}

// ── Asking, granting, stopping ────────────────────────────────────────────

// castInputRequest is a viewer's Play. The answer comes as an event.
func castInputRequest(c *castCtlClient) {
	if c == nil {
		return
	}
	if why := castInputRefusal(); why != "" {
		c.tell(castCtlEvent{State: "denied", Note: why})
		return
	}
	castCtl.turn.Lock()
	defer castCtl.turn.Unlock()
	castCtl.mu.Lock()
	if castCtl.owner == c {
		castCtl.mu.Unlock()
		c.tell(castCtlEvent{State: "playing", Games: castPadReachesGames()})
		return
	}
	until, known := castCtl.remembered[c.key]
	if c.viaPIN || (known && time.Now().Before(until)) {
		castInputGrantLocked(c)
		castCtl.mu.Unlock()
		return
	}
	for _, p := range castCtl.pending {
		if p == c {
			castCtl.mu.Unlock()
			c.tell(castCtlEvent{State: "asking"})
			return
		}
	}
	if len(castCtl.pending) >= castCtlMaxPending {
		castCtl.mu.Unlock()
		c.tell(castCtlEvent{State: "denied", Note: "Too many viewers are asking. Try again in a moment."})
		return
	}
	c.at = time.Now()
	castCtl.pending = append(castCtl.pending, c)
	castCtl.mu.Unlock()
	c.tell(castCtlEvent{State: "asking"})
}

// castInputRefusal is why nobody may play right now, "" when they may.
func castInputRefusal() string {
	switch {
	case !castIsEnabled():
		return "Screen Share is off on the handheld."
	case !castLive.control.Load():
		return "Controllers are switched off on the handheld (Start → Screen Share)."
	case startupLocked():
		return "The handheld is locked."
	case uiModeCompanionLocked():
		return "The handheld is in " + uiModeLabel(uiModeNow()) + " mode."
	}
	return ""
}

// castInputGrantLocked makes c the player; the one before is told it moved.
func castInputGrantLocked(c *castCtlClient) {
	if old := castCtl.owner; old != nil && old != c {
		old.tell(castCtlEvent{State: "stopped", Note: "Another viewer is playing now."})
	}
	castCtl.owner = c
	castCtl.pending = castCtlWithout(castCtl.pending, c)
	if castCtl.remembered == nil {
		castCtl.remembered = make(map[string]time.Time)
	}
	castCtl.remembered[c.key] = time.Now().Add(castCtlRemember)
	castPadStart()
	castPadSet(castPadState{})
	c.tell(castCtlEvent{State: "playing", Games: castPadReachesGames()})
	logInfo("cast", "a Screen Share viewer plays the handheld: %s", c.name)
}

func castCtlWithout(list []*castCtlClient, c *castCtlClient) []*castCtlClient {
	out := list[:0]
	for _, p := range list {
		if p != c {
			out = append(out, p)
		}
	}
	return out
}

// castInputAnswer is the handheld's answer to the oldest request (the panel,
// main thread). It reports whether there was one.
func castInputAnswer(allow bool) bool {
	castCtl.turn.Lock()
	defer castCtl.turn.Unlock()
	castCtl.mu.Lock()
	defer castCtl.mu.Unlock()
	castCtlExpireLocked(time.Now())
	if len(castCtl.pending) == 0 {
		return false
	}
	c := castCtl.pending[0]
	if !allow {
		castCtl.pending = castCtl.pending[1:]
		c.tell(castCtlEvent{State: "denied", Note: "The handheld said no."})
		return true
	}
	if why := castInputRefusal(); why != "" {
		castCtl.pending = castCtl.pending[1:]
		c.tell(castCtlEvent{State: "denied", Note: why})
		return true
	}
	castInputGrantLocked(c)
	return true
}

// castCtlExpireLocked turns away the requests nobody answered in time.
func castCtlExpireLocked(now time.Time) {
	keep := castCtl.pending[:0]
	for _, p := range castCtl.pending {
		if now.Sub(p.at) > castCtlAskWait {
			p.tell(castCtlEvent{State: "denied", Note: "Nobody answered on the handheld."})
			continue
		}
		keep = append(keep, p)
	}
	castCtl.pending = keep
}

// castInputLeave is a viewer's socket closing, or its Stop: its turn or its
// request ends, buttons let go. The grant stays remembered for a reconnect.
func castInputLeave(c *castCtlClient) {
	if c == nil {
		return
	}
	castCtl.turn.Lock()
	defer castCtl.turn.Unlock()
	castCtl.mu.Lock()
	castCtl.pending = castCtlWithout(castCtl.pending, c)
	was := castCtl.owner == c
	if was {
		castCtl.owner = nil
	}
	castCtl.mu.Unlock()
	if was {
		castPadStop()
		logInfo("cast", "the Screen Share viewer stopped playing")
	}
}

// castInputRevoke ends every turn and request for why (the handheld took it
// back, sharing or Controllers off, a new link, JukaHub closing), and forgets
// the remembered grants: playing again means asking again.
func castInputRevoke(why string) {
	castCtl.turn.Lock()
	defer castCtl.turn.Unlock()
	castCtl.mu.Lock()
	owner := castCtl.owner
	pending := castCtl.pending
	castCtl.owner, castCtl.pending, castCtl.remembered = nil, nil, nil
	castCtl.mu.Unlock()
	for _, p := range pending {
		p.tell(castCtlEvent{State: "denied", Note: why})
	}
	if owner != nil {
		owner.tell(castCtlEvent{State: "stopped", Note: why})
		logInfo("cast", "Screen Share control ended: %s", why)
	}
	castPadStop()
}

// castInputStopOwner is X in the panel: the handheld takes its pad back.
func castInputStopOwner() bool {
	castCtl.mu.Lock()
	has := castCtl.owner != nil
	castCtl.mu.Unlock()
	if has {
		castInputRevoke("The handheld took control back.")
	}
	return has
}

// castInputApply is a state from viewer c; only the player's count.
func castInputApply(c *castCtlClient, st castPadState) {
	castCtl.mu.Lock()
	owner := castCtl.owner == c
	castCtl.mu.Unlock()
	if owner {
		castPadSet(st)
	}
}

// castInputStatus is what the panel shows: who plays ("" for nobody), the
// oldest request waiting ("" for none) and how many wait.
func castInputStatus() (owner, asking string, waiting int) {
	castCtl.mu.Lock()
	defer castCtl.mu.Unlock()
	castCtlExpireLocked(time.Now())
	if castCtl.owner != nil {
		owner = castCtl.owner.name
	}
	if len(castCtl.pending) > 0 {
		asking = castCtl.pending[0].name
	}
	return owner, asking, len(castCtl.pending)
}

// castInputTellOwner sends the player an event (a change in what its presses
// reach).
func castInputTellOwner(ev castCtlEvent) {
	castCtl.mu.Lock()
	owner := castCtl.owner
	castCtl.mu.Unlock()
	if owner != nil {
		owner.tell(ev)
	}
}

// castInputPlaying reports whether a viewer plays now (the LIVE pill, the
// capture's pace).
func castInputPlaying() bool {
	castCtl.mu.Lock()
	defer castCtl.mu.Unlock()
	return castCtl.owner != nil
}

// castInputRecoverOnce continues, once per run, a firmware pad reader a
// JukaHub that died during a viewer's turn left paused (castinput_linux.go).
var castInputRecoverOnce sync.Once

// castInputMainTick runs with castTick on the main thread: a new request is
// announced once — a toast and a buzz — and the panel's focus goes to it.
func castInputMainTick() {
	castInputRecoverOnce.Do(castInputRecover)
	castCtl.mu.Lock()
	castCtlExpireLocked(time.Now())
	var fresh *castCtlClient
	if len(castCtl.pending) > 0 && castCtl.announced != castCtl.pending[0] {
		fresh = castCtl.pending[0]
		castCtl.announced = fresh
	}
	castCtl.mu.Unlock()
	if fresh == nil {
		return
	}
	playHaptic(hapticSuccessPulse)
	if castPanel.open {
		castPanel.focus = castFocusRequest
		return
	}
	ShowToast(fresh.name+" asks to play — open Start → Screen Share to allow it", ToastKindInfo)
}

// ── The pad loop ──────────────────────────────────────────────────────────

// castPadDevice is the built-in pad as a target (castinput_linux.go).
type castPadDevice interface {
	// apply makes the pad hold st (only what changed is written).
	apply(st castPadState, now time.Time)
	// poll reads what else the pad reported and keeps st held against the
	// firmware; it reports whether it needs the fast tick.
	poll(st castPadState, now time.Time) bool
	// failed reports a pad that stopped taking writes.
	failed() bool
	// close lets go of everything and closes the pad.
	close()
}

// castFace is the SDL button each of the remote's A, B, X and Y presses on
// JukaHub's own screens, published from the settings (castInputSyncSettings)
// the way the Phone Companion's remote pre-flips them (companionButtonFor).
var castFace [4]atomic.Uint32

// castInputSyncSettings publishes the face buttons; main thread.
func castInputSyncSettings(config *Config) {
	for i, b := range []uint8{sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y} {
		castFace[i].Store(uint32(padFaceButton(config, b)) + 1)
	}
}

var castPad struct {
	mu    sync.Mutex
	in    chan castPadState
	stop  chan struct{}
	done  chan struct{}
	games atomic.Bool // the built-in pad takes the presses: games hear them too
}

// castPadReachesGames reports whether the playing viewer reaches games: true
// once the built-in pad is open, and where it may be (before it is tried).
func castPadReachesGames() bool {
	castPad.mu.Lock()
	running := castPad.stop != nil
	castPad.mu.Unlock()
	if running {
		return castPad.games.Load()
	}
	return IsTrimuiDevice()
}

// castPadStart starts the loop (once).
func castPadStart() {
	castPad.mu.Lock()
	defer castPad.mu.Unlock()
	if castPad.stop != nil {
		return
	}
	castPad.in = make(chan castPadState, 1)
	castPad.stop = make(chan struct{})
	castPad.done = make(chan struct{})
	castPad.games.Store(IsTrimuiDevice())
	go castPadLoop(castPad.in, castPad.stop, castPad.done)
}

// castPadSet hands the loop the newest state; an older one not yet taken is
// replaced.
func castPadSet(st castPadState) {
	castPad.mu.Lock()
	in := castPad.in
	castPad.mu.Unlock()
	if in == nil {
		return
	}
	for {
		select {
		case in <- st:
			return
		default:
		}
		select {
		case <-in:
		default:
		}
	}
}

// castPadStop ends the loop, which lets go of everything first, and waits a
// moment for it.
func castPadStop() {
	castPad.mu.Lock()
	stop, done := castPad.stop, castPad.done
	castPad.stop, castPad.done, castPad.in = nil, nil, nil
	castPad.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		logWarn("cast", "the remote pad did not stop in time")
	}
}

// castPadLoop owns the target for one turn: the built-in pad when it opens,
// else SDL events for JukaHub's own screens.
func castPadLoop(in <-chan castPadState, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	var dev castPadDevice
	if IsTrimuiDevice() {
		d, err := castPadDeviceOpen()
		if err != nil {
			logWarn("cast", "a viewer's presses cannot reach games: %v", err)
			// The player was told games are in reach (castPadReachesGames).
			castInputTellOwner(castCtlEvent{State: "playing", Games: false})
		} else {
			dev = d
		}
	}
	castPad.games.Store(dev != nil)
	var ui castSDLPad
	var want castPadState
	// blocked: a game is on screen and Games too is off, so viewers see a
	// pause card — and their presses do not reach the game either.
	blocked := false
	heard := time.Now()
	timer := time.NewTimer(castPadIdleTick)
	defer timer.Stop()
	defer func() {
		if r := recover(); r != nil {
			logWarn("cast", "the remote pad stopped: %v", r)
		}
		if dev != nil {
			dev.close()
		}
		ui.apply(castPadState{}, time.Now())
	}()
	for {
		fresh := false
		select {
		case <-stop:
			return
		case st := <-in:
			want, heard, fresh = st, time.Now(), true
		case <-timer.C:
		}
		now := time.Now()
		if !want.neutral() && now.Sub(heard) > castPadQuiet {
			// The page went quiet while holding: its network stalled, or the
			// tab was closed without a word. Nothing stays down.
			want, fresh = castPadState{}, true
		}
		if b := gameScreenSuspended() && castGamesSupported() && !castLive.games.Load(); b != blocked {
			blocked, fresh = b, true
		}
		eff := want
		if blocked {
			eff = castPadState{}
		}
		var fast bool
		if dev != nil {
			if fresh {
				dev.apply(eff, now)
			}
			fast = dev.poll(eff, now)
			if dev.failed() {
				// The pad went away (or stopped taking writes): JukaHub's own
				// screens still hear the viewer, through SDL.
				dev.close()
				dev = nil
				castPad.games.Store(false)
				castInputTellOwner(castCtlEvent{State: "playing", Games: false})
			}
		} else {
			fast = ui.apply(eff, now)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if fast || !want.neutral() {
			timer.Reset(castPadTick)
		} else {
			timer.Reset(castPadIdleTick)
		}
	}
}

// ── JukaHub's own screens, through SDL ────────────────────────────────────

// castSDLPad presses SDL controller buttons for the remote on JukaHub's own
// screens: held is what it pushed down, repeatAt when a held D-pad direction
// next repeats. While a game owns the screen it lets go of everything, and
// presses nothing until JukaHub is back.
type castSDLPad struct {
	held     uint32
	axes     [6]int16
	repeatAt time.Time
	repeat   int // the D-pad button repeating, plus one; 0 for none
}

// castSDLButton is the SDL button for a remote button, ok false for the
// triggers (axes) and anything unknown.
func castSDLButton(b int) (uint8, bool) {
	switch b {
	case castBtnUp:
		return sdl.CONTROLLER_BUTTON_DPAD_UP, true
	case castBtnDown:
		return sdl.CONTROLLER_BUTTON_DPAD_DOWN, true
	case castBtnLeft:
		return sdl.CONTROLLER_BUTTON_DPAD_LEFT, true
	case castBtnRight:
		return sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true
	case castBtnA, castBtnB, castBtnX, castBtnY:
		if v := castFace[b-castBtnA].Load(); v > 0 {
			return uint8(v - 1), true
		}
		return []uint8{sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y}[b-castBtnA], true
	case castBtnL1:
		return sdl.CONTROLLER_BUTTON_LEFTSHOULDER, true
	case castBtnR1:
		return sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, true
	case castBtnSelect:
		return sdl.CONTROLLER_BUTTON_BACK, true
	case castBtnStart:
		return sdl.CONTROLLER_BUTTON_START, true
	case castBtnMenu:
		return sdl.CONTROLLER_BUTTON_GUIDE, true
	case castBtnL3:
		return sdl.CONTROLLER_BUTTON_LEFTSTICK, true
	case castBtnR3:
		return sdl.CONTROLLER_BUTTON_RIGHTSTICK, true
	}
	return 0, false
}

// apply pushes what changed and repeats a held D-pad direction; it reports
// whether it needs the fast tick (a direction is held).
func (p *castSDLPad) apply(st castPadState, now time.Time) bool {
	if gameScreenSuspended() {
		st = castPadState{}
	}
	for b := 0; b < castBtnCount; b++ {
		btn, ok := castSDLButton(b)
		if !ok {
			continue
		}
		down, was := st.has(b), p.held&(1<<uint(b)) != 0
		if down == was {
			continue
		}
		if down {
			p.held |= 1 << uint(b)
			_ = pushControllerButton(sdl.CONTROLLERBUTTONDOWN, 0, btn, sdl.PRESSED)
		} else {
			p.held &^= 1 << uint(b)
			_ = pushControllerButton(sdl.CONTROLLERBUTTONUP, 0, btn, sdl.RELEASED)
		}
	}
	trig := func(down bool, t float64) int16 {
		if down {
			t = 1
		}
		return int16(max(0, min(1, t)) * 32767)
	}
	stick := func(v float64) int16 { return int16(max(-1, min(1, v)) * 32767) }
	vals := [6]int16{stick(st.lx), stick(st.ly), stick(st.rx), stick(st.ry),
		trig(st.has(castBtnL2), st.lt), trig(st.has(castBtnR2), st.rt)}
	axes := [6]uint8{sdl.CONTROLLER_AXIS_LEFTX, sdl.CONTROLLER_AXIS_LEFTY, sdl.CONTROLLER_AXIS_RIGHTX,
		sdl.CONTROLLER_AXIS_RIGHTY, sdl.CONTROLLER_AXIS_TRIGGERLEFT, sdl.CONTROLLER_AXIS_TRIGGERRIGHT}
	for i, v := range vals {
		if v != p.axes[i] {
			p.axes[i] = v
			_ = pushControllerAxis(0, axes[i], v)
		}
	}
	// A held direction repeats like the handheld's (navrepeat.go), which
	// reads the real pad and so never sees this one.
	dir := -1
	for _, b := range []int{castBtnUp, castBtnDown, castBtnLeft, castBtnRight} {
		if st.has(b) {
			dir = b
			break
		}
	}
	switch {
	case dir < 0:
		p.repeat = 0
	case dir+1 != p.repeat:
		p.repeat, p.repeatAt = dir+1, now.Add(navRepeatDelayMs*time.Millisecond)
	case !now.Before(p.repeatAt):
		p.repeatAt = now.Add(navRepeatIntervalMs * time.Millisecond)
		if btn, ok := castSDLButton(dir); ok {
			_ = pushControllerButton(sdl.CONTROLLERBUTTONDOWN, 0, btn, sdl.PRESSED)
		}
	}
	return dir >= 0
}
