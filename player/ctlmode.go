package main

// ctlmode.go — Controller Mode: the handheld as a game controller for a PC or
// a phone, and back to JukaHub with one press of Menu.
//
// This file is the core every link shares; the links themselves are
// transports that register here (ctlmode_usb.go: a USB cable, through the
// USB controller engine in usbpad*.go; ctlmode_wifi.go: Wi-Fi to the JukaPad
// receiver on a PC; ctlmode_dsu.go: the DSU protocol PC emulators read;
// ctlmode_bt*.go: a Bluetooth gamepad), and the page that drives them is
// ctlmode_ui.go. The installed TrimUI apps that do the same job (GamePad to
// PC, the Bluetooth pad app) are offered by the page as launch choices
// (extapps.go).
//
// How a session runs:
//
//   - the page starts one transport (ctlModeStartTransport). It advertises,
//     discovers or pairs, and the page stays interactive while it does: a
//     pairing code is confirmed with the handheld's own buttons;
//   - when the transport reports a peer connected, forwarding begins on its
//     own (ctlModeBegin): from then on every physical press, release and stick
//     sample is the link's, before the quit chord, the voice orb, the
//     screensaver or any screen sees it (ctlModeEvent, hooked at the top of
//     main.go's event loop);
//   - Menu tapped alone pauses forwarding and gives the pad back to JukaHub
//     (the page, which can resume with A); Menu held, or pressed together
//     with another button, is the controller's Guide button instead. The
//     link stays up while paused and the peer sees every button released;
//   - leaving the page stops the transport.
//
// The pad state a transport receives is positional with Xbox names: A is the
// bottom face button whatever its label, and the saved layout (face buttons
// by position or by label, the D-pad as the left stick, a stick dead zone,
// the right stick's Y inverted) is applied here, so every transport sends
// the same thing.
//
// Everything here runs on the main thread except ctlModeRumble, which a
// transport's goroutine calls when the PC asks for vibration.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// ctlModeSceneName is the page's config scene, injected in memory.
	ctlModeSceneName = "ControllerMode"

	// ctlMenuHoldGuide is how long Menu is held before it counts as the Guide
	// button rather than a tap that returns to JukaHub.
	ctlMenuHoldGuide = 450 * time.Millisecond
	// ctlAwakeEvery is how often a forwarding session tells the idle timers
	// the handheld is in use: a cutscene with no input must not put it to
	// sleep under the player.
	ctlAwakeEvery = 2 * time.Second
	// ctlScreenOffAfter is how long forwarding runs before the panel goes
	// dark when the screen setting says so.
	ctlScreenOffAfter = 15 * time.Second

	// ctlScreenKey is the screen setting: "off" (default) darkens the panel
	// while forwarding, "on" keeps the live display lit.
	ctlScreenKey = "ctlModeScreen"
	// ctlLastTransportKey remembers the transport chosen last.
	ctlLastTransportKey = "ctlModeTransport"

	// ctlDirName is Controller Mode's folder in the data directory: the
	// saved layouts, and each transport's own files (paired PCs).
	ctlDirName = "controller-mode"
	// ctlLayoutsFile holds the user's layouts and the layout each peer uses.
	ctlLayoutsFile = "layouts.json"
	// ctlMaxLayouts bounds the user's own layouts.
	ctlMaxLayouts = 16
	// ctlTriggerPressed is where an analogue trigger also counts as pressed
	// (the LT/RT button bits), out of 255.
	ctlTriggerPressed = 64
)

// The controller's buttons, positional with Xbox names: ctlBtnA is the bottom
// face button, ctlBtnY the top one. LT and RT also carry an analogue value
// (ctlPadState.LT/RT); the bits say they are pressed past ctlTriggerPressed.
const (
	ctlBtnA uint32 = 1 << iota
	ctlBtnB
	ctlBtnX
	ctlBtnY
	ctlBtnLB
	ctlBtnRB
	ctlBtnLT
	ctlBtnRT
	ctlBtnBack
	ctlBtnStart
	ctlBtnGuide
	ctlBtnLS
	ctlBtnRS
	ctlBtnUp
	ctlBtnDown
	ctlBtnLeft
	ctlBtnRight
)

// ctlBtnDpad is the four D-pad bits.
const ctlBtnDpad = ctlBtnUp | ctlBtnDown | ctlBtnLeft | ctlBtnRight

// ctlPadState is one snapshot of the controller, layout applied. Sticks use
// SDL's convention: -32768..32767, +X right and +Y DOWN (a transport whose
// protocol wants +Y up negates it). Triggers are 0..255.
type ctlPadState struct {
	Buttons        uint32
	LX, LY, RX, RY int16
	LT, RT         uint8
}

// ctlLinkState is where a transport's link is.
type ctlLinkState int

const (
	ctlLinkIdle      ctlLinkState = iota // not started
	ctlLinkWaiting                       // started; waiting for a PC or phone
	ctlLinkPairing                       // a pairing prompt is up (Prompt() != nil)
	ctlLinkConnected                     // a peer receives the pad
	ctlLinkError                         // stopped by an error (Detail says why)
)

// ctlLinkStatus is what the page shows about a transport.
type ctlLinkStatus struct {
	State ctlLinkState
	// Peer is the connected PC or phone's name ("" when none).
	Peer string
	// PeerID is a stable id for that peer (a key fingerprint, a Bluetooth
	// address); the layout chosen for it is remembered under it.
	PeerID string
	// Detail is one line under the state ("Looking for the receiver on this
	// network", "Paired with DESKTOP-1", an error).
	Detail string
}

// ctlRow is one transport-specific line on the page: a PC found on the
// network, "Forget DESKTOP-1", "Get the PC receiver". A on it calls the
// transport's Do with Action; an empty Action is information only.
type ctlRow struct {
	Label  string
	Detail string
	Action string
}

// ctlPrompt is a question the transport needs answered on the handheld: a
// pairing code to compare with the PC's screen, a request to accept a phone.
// A answers YesAction, B NoAction (both go to Do).
type ctlPrompt struct {
	Title string
	// Code is drawn big ("482 913"); "" draws none.
	Code      string
	Body      string
	Yes, No   string
	YesAction string
	NoAction  string
}

// ctlTransport is one way of being a controller. Every method is called on
// the main thread and must return at once: work happens on the transport's
// own goroutines, and Status, Rows and Prompt are read every frame, so they
// answer from memory under a mutex. Implementations are pointer types: the
// session compares the running transport with ==.
type ctlTransport interface {
	// ID is stable ("wifi", "dsu", "bt"): saved settings name it.
	ID() string
	// Title is the transport's name on the page ("Wi-Fi to a PC").
	Title() string
	// Subtitle is one line saying what it is for.
	Subtitle() string
	// Available reports whether it can run on this device, and why not.
	Available() (bool, string)
	// Start begins advertising, discovery or listening.
	Start() error
	// Stop ends the link and releases everything Start took. It must leave
	// the peer seeing every button released when it can.
	Stop()
	// Send hands over the newest state. It never blocks: the transport keeps
	// the latest copy and its own goroutine sends it (and repeats it, so a
	// lost datagram heals).
	Send(st ctlPadState)
	Status() ctlLinkStatus
	// Rows are the transport's own lines on the page (nil for none).
	Rows() []ctlRow
	// Prompt is the question waiting for an answer, or nil.
	Prompt() *ctlPrompt
	// Do runs a row's or a prompt's action.
	Do(action string)
}

type ctlTransportReg struct {
	order int
	t     ctlTransport
}

// ctlTransports is filled from init() functions and read on the main thread.
var ctlTransports []ctlTransportReg

// ctlModeRegisterTransport adds a transport; order sorts the page's list. A
// second registration of an id is a programming error and is ignored.
func ctlModeRegisterTransport(order int, t ctlTransport) {
	if t == nil || t.ID() == "" {
		return
	}
	for _, r := range ctlTransports {
		if r.t.ID() == t.ID() {
			logWarn("ctlmode", "transport %q registered twice, keeping the first", t.ID())
			return
		}
	}
	ctlTransports = append(ctlTransports, ctlTransportReg{order: order, t: t})
	sort.SliceStable(ctlTransports, func(i, j int) bool { return ctlTransports[i].order < ctlTransports[j].order })
}

// ctlModeTransportList is every registered transport, in page order.
func ctlModeTransportList() []ctlTransport {
	out := make([]ctlTransport, 0, len(ctlTransports))
	for _, r := range ctlTransports {
		out = append(out, r.t)
	}
	return out
}

// ctlModeTransportByID finds a transport, or nil.
func ctlModeTransportByID(id string) ctlTransport {
	for _, r := range ctlTransports {
		if r.t.ID() == id {
			return r.t
		}
	}
	return nil
}

// ── Layouts ──────────────────────────────────────────────────────────────

// The face-button and D-pad choices of a layout.
const (
	ctlFacePosition = "position" // the bottom button sends A (what PC games expect)
	ctlFaceLabel    = "label"    // the button labelled A sends A (Nintendo-style labels)
	ctlDpadDpad     = "dpad"     // the D-pad is the D-pad
	ctlDpadStick    = "lstick"   // the D-pad moves the left stick (games that ignore the D-pad)
)

// ctlLayout is one saved way of turning the handheld's controls into the
// controller the peer sees.
type ctlLayout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Face string `json:"face"`
	Dpad string `json:"dpad"`
	// Deadzone is the share of a stick's travel, in percent (0..30), that
	// reads as centred.
	Deadzone int  `json:"deadzone"`
	InvertRY bool `json:"invertRY"`
	// MenuGuide makes Menu held (or pressed with another button) the Guide
	// button. Off, Menu only ever returns to JukaHub.
	MenuGuide bool `json:"menuGuide"`
	// Builtin layouts cannot be renamed or deleted (never saved).
	Builtin bool `json:"-"`
}

// ctlBuiltinLayouts are always there; the first is the default.
var ctlBuiltinLayouts = []ctlLayout{
	{ID: "standard", Name: "Standard", Face: ctlFacePosition, Dpad: ctlDpadDpad, Deadzone: 8, MenuGuide: true, Builtin: true},
	{ID: "labels", Name: "Nintendo labels", Face: ctlFaceLabel, Dpad: ctlDpadDpad, Deadzone: 8, MenuGuide: true, Builtin: true},
	{ID: "retro", Name: "Retro (D-pad as stick)", Face: ctlFacePosition, Dpad: ctlDpadStick, Deadzone: 8, MenuGuide: true, Builtin: true},
}

// ctlLayoutFileBody is ctlLayoutsFile on disk.
type ctlLayoutFileBody struct {
	Layouts []ctlLayout `json:"layouts"`
	// ByPeer is the layout each peer (ctlLinkStatus.PeerID) used last.
	ByPeer map[string]string `json:"byPeer"`
	// Last is the layout chosen last, for a peer with none of its own.
	Last string `json:"last"`
}

// ctlLayouts is the loaded store (main thread only).
var ctlLayouts struct {
	loaded bool
	body   ctlLayoutFileBody
}

// ctlModeDir is Controller Mode's data folder ("" when there is none). It
// does not create it.
func ctlModeDir() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, ctlDirName)
	}
	return ""
}

func ctlLayoutsLoad() {
	if ctlLayouts.loaded {
		return
	}
	ctlLayouts.loaded = true
	ctlLayouts.body = ctlLayoutFileBody{ByPeer: map[string]string{}}
	dir := ctlModeDir()
	if dir == "" {
		return
	}
	// A damaged file falls back to the .bak AtomicWrite keeps beside it
	// (readJSONWithBackup logs either way); absent is a first run.
	var body ctlLayoutFileBody
	if _, err := readJSONWithBackup(filepath.Join(dir, ctlLayoutsFile), &body); err != nil {
		return
	}
	clean := body.Layouts[:0]
	for _, l := range body.Layouts {
		l = ctlLayoutSanitize(l)
		if l.ID == "" || ctlBuiltinLayout(l.ID) || len(clean) >= ctlMaxLayouts {
			continue
		}
		clean = append(clean, l)
	}
	body.Layouts = clean
	if body.ByPeer == nil {
		body.ByPeer = map[string]string{}
	}
	ctlLayouts.body = body
}

// ctlLayoutsSaveMu serialises the writers: AtomicWrite's temp file has one
// fixed name, so two saves at once (a built-in's first edit saves the copy
// and then the choice) could rename the other's half-written file.
// ctlLayoutsSaveGen numbers the snapshots: a writer that finds a newer one
// queued leaves the file to it, so an older snapshot never lands last.
var (
	ctlLayoutsSaveMu  sync.Mutex
	ctlLayoutsSaveGen atomic.Uint64
)

// ctlLayoutsSave writes the store off the main thread (a copy goes along).
func ctlLayoutsSave() {
	dir := ctlModeDir()
	if dir == "" {
		return
	}
	body := ctlLayouts.body
	body.Layouts = append([]ctlLayout(nil), body.Layouts...)
	peers := make(map[string]string, len(body.ByPeer))
	for k, v := range body.ByPeer {
		peers[k] = v
	}
	body.ByPeer = peers
	gen := ctlLayoutsSaveGen.Add(1)
	go func() {
		ctlLayoutsSaveMu.Lock()
		defer ctlLayoutsSaveMu.Unlock()
		if ctlLayoutsSaveGen.Load() != gen {
			return
		}
		data, err := json.MarshalIndent(body, "", "  ")
		if err != nil {
			return
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			logWarn("ctlmode", "cannot create %s: %v", dir, err)
			return
		}
		if err := AtomicWrite(filepath.Join(dir, ctlLayoutsFile), data, 0o600); err != nil {
			logWarn("ctlmode", "cannot save layouts: %v", err)
		}
	}()
}

// ctlLayoutSanitize clamps a layout read from disk or edited on the page.
func ctlLayoutSanitize(l ctlLayout) ctlLayout {
	l.ID = strings.TrimSpace(l.ID)
	l.Name = strings.TrimSpace(l.Name)
	if r := []rune(l.Name); len(r) > 32 {
		l.Name = string(r[:32])
	}
	if l.Name == "" {
		l.Name = "My layout"
	}
	if l.Face != ctlFaceLabel {
		l.Face = ctlFacePosition
	}
	if l.Dpad != ctlDpadStick {
		l.Dpad = ctlDpadDpad
	}
	if l.Deadzone < 0 {
		l.Deadzone = 0
	}
	if l.Deadzone > 30 {
		l.Deadzone = 30
	}
	l.Builtin = false
	return l
}

func ctlBuiltinLayout(id string) bool {
	for _, b := range ctlBuiltinLayouts {
		if b.ID == id {
			return true
		}
	}
	return false
}

// ctlModeLayoutList is the built-in layouts, then the user's own.
func ctlModeLayoutList() []ctlLayout {
	ctlLayoutsLoad()
	out := append([]ctlLayout(nil), ctlBuiltinLayouts...)
	return append(out, ctlLayouts.body.Layouts...)
}

// ctlModeLayoutByID finds a layout; ok is false for an unknown id.
func ctlModeLayoutByID(id string) (ctlLayout, bool) {
	for _, l := range ctlModeLayoutList() {
		if l.ID == id {
			return l, true
		}
	}
	return ctlLayout{}, false
}

// ctlModeLayoutFor is the layout a peer gets: its own last one, else the one
// chosen last, else Standard.
func ctlModeLayoutFor(peerID string) ctlLayout {
	ctlLayoutsLoad()
	if peerID != "" {
		if id, ok := ctlLayouts.body.ByPeer[peerID]; ok {
			if l, ok := ctlModeLayoutByID(id); ok {
				return l
			}
		}
	}
	if l, ok := ctlModeLayoutByID(ctlLayouts.body.Last); ok {
		return l
	}
	return ctlBuiltinLayouts[0]
}

// ctlModeUseLayout makes id the live layout and remembers it for the
// connected peer (when there is one) and as the last choice.
func ctlModeUseLayout(id string) bool {
	l, ok := ctlModeLayoutByID(id)
	if !ok {
		return false
	}
	ctlMode.layout = l
	ctlLayouts.body.Last = l.ID
	if ctlMode.peerID != "" {
		if len(ctlLayouts.body.ByPeer) >= 64 {
			ctlLayouts.body.ByPeer = map[string]string{}
		}
		ctlLayouts.body.ByPeer[ctlMode.peerID] = l.ID
	}
	ctlLayoutsSave()
	ctlModeResend()
	return true
}

// ctlModeSaveLayout stores a user layout (new when its ID is empty or
// built-in) and returns it with its ID. ok is false when the list is full.
func ctlModeSaveLayout(l ctlLayout) (ctlLayout, bool) {
	ctlLayoutsLoad()
	l = ctlLayoutSanitize(l)
	if l.ID == "" || ctlBuiltinLayout(l.ID) {
		if len(ctlLayouts.body.Layouts) >= ctlMaxLayouts {
			return l, false
		}
		l.ID = "u" + strconv.FormatInt(time.Now().UnixNano(), 36)
		for ctlLayoutIDTaken(l.ID) {
			l.ID += "x"
		}
		ctlLayouts.body.Layouts = append(ctlLayouts.body.Layouts, l)
	} else {
		found := false
		for i := range ctlLayouts.body.Layouts {
			if ctlLayouts.body.Layouts[i].ID == l.ID {
				ctlLayouts.body.Layouts[i] = l
				found = true
				break
			}
		}
		if !found {
			return l, false
		}
	}
	ctlLayoutsSave()
	if ctlMode.layout.ID == l.ID {
		ctlMode.layout = l
		ctlModeResend()
	}
	return l, true
}

func ctlLayoutIDTaken(id string) bool {
	_, ok := ctlModeLayoutByID(id)
	return ok
}

// ctlModeDeleteLayout removes a user layout; peers that used it fall back.
func ctlModeDeleteLayout(id string) bool {
	ctlLayoutsLoad()
	for i, l := range ctlLayouts.body.Layouts {
		if l.ID != id {
			continue
		}
		ctlLayouts.body.Layouts = append(ctlLayouts.body.Layouts[:i], ctlLayouts.body.Layouts[i+1:]...)
		for peer, lid := range ctlLayouts.body.ByPeer {
			if lid == id {
				delete(ctlLayouts.body.ByPeer, peer)
			}
		}
		if ctlLayouts.body.Last == id {
			ctlLayouts.body.Last = ""
		}
		if ctlMode.layout.ID == id {
			ctlMode.layout = ctlModeLayoutFor(ctlMode.peerID)
			ctlModeResend()
		}
		ctlLayoutsSave()
		return true
	}
	return false
}

// ── Session ──────────────────────────────────────────────────────────────

// ctlRawPad is one physical pad's state, before the layout.
type ctlRawPad struct {
	buttons uint32 // ctlBtn bits of the buttons held (Menu is tracked apart)
	axes    [6]int16
}

// ctlMode is the session (main thread only).
var ctlMode struct {
	// running is the started transport, nil when none.
	running ctlTransport
	// forwarding: the pad belongs to the link.
	forwarding bool
	// paused: forwarding was stopped by Menu (or the link coming back) and
	// waits for the page's Resume.
	paused bool
	// lastState is the link state seen last, for the connect edge.
	lastState ctlLinkState
	peerID    string
	peerName  string
	layout    ctlLayout
	pads      map[sdl.JoystickID]*ctlRawPad
	sent      ctlPadState
	// The Menu button: when it went down, whether another button joined it,
	// and whether it is being sent as Guide.
	menuDown  time.Time
	menuChord bool
	menuGuide bool
	// since is when forwarding began; awakeAt the last idle-timer poke.
	since   time.Time
	awakeAt time.Time
	// screenDark: this session switched the panel's light off
	// (devpower.go backlightOff) and owes it back.
	screenDark bool
	// presses counts forwarded presses, for the page's live line.
	presses int
}

func init() {
	bootOnShutdown(ctlModeShutdown)
}

// ctlModeActive reports whether the pad is being forwarded.
func ctlModeActive() bool { return ctlMode.forwarding }

// ctlModeRunning is the started transport, or nil.
func ctlModeRunning() ctlTransport { return ctlMode.running }

// ctlModePaused reports a connected link whose forwarding Menu paused.
func ctlModePaused() bool { return ctlMode.paused && ctlMode.running != nil }

// ctlModeLiveState is the state last sent (the page's live display).
func ctlModeLiveState() ctlPadState { return ctlMode.sent }

// ctlModeLayoutNow is the layout in use.
func ctlModeLayoutNow() ctlLayout {
	if ctlMode.layout.ID == "" {
		ctlMode.layout = ctlModeLayoutFor(ctlMode.peerID)
	}
	return ctlMode.layout
}

// ctlModePresses is the number of presses forwarded this session.
func ctlModePresses() int { return ctlMode.presses }

// ctlModeStartTransport starts a transport by id, stopping any other first.
func ctlModeStartTransport(id string) error {
	t := ctlModeTransportByID(id)
	if t == nil {
		return errCtlUnknown
	}
	if ctlMode.running == t {
		return nil
	}
	ctlModeStopTransport()
	if ok, why := t.Available(); !ok {
		if strings.TrimSpace(why) == "" {
			// The page shows this as a toast: never an empty one.
			why = t.Title() + " cannot run on this device"
		}
		return ctlError(why)
	}
	if err := t.Start(); err != nil {
		return err
	}
	ctlMode.running = t
	ctlMode.lastState = ctlLinkIdle
	ctlMode.paused = false
	ctlMode.presses = 0
	ctlMode.layout = ctlModeLayoutFor("")
	ctlStoreSetting(ctlLastTransportKey, id)
	logInfo("ctlmode", "transport %s started", id)
	return nil
}

// ctlModeStopTransport ends forwarding and stops the running transport.
func ctlModeStopTransport() {
	ctlModeEnd("")
	ctlMode.paused = false
	if ctlMode.running != nil {
		logInfo("ctlmode", "transport %s stopped", ctlMode.running.ID())
		ctlMode.running.Stop()
		ctlMode.running = nil
	}
	ctlMode.peerID, ctlMode.peerName = "", ""
	ctlMode.lastState = ctlLinkIdle
}

// ctlModeBegin gives the pad to the link. Held buttons are forgotten, so a
// button still down from the page's own press is not sent as held.
func ctlModeBegin() {
	if ctlMode.running == nil || ctlMode.forwarding {
		return
	}
	ctlMode.forwarding = true
	ctlMode.paused = false
	ctlMode.pads = map[sdl.JoystickID]*ctlRawPad{}
	ctlMode.menuDown = time.Time{}
	ctlMode.menuChord, ctlMode.menuGuide = false, false
	now := time.Now()
	ctlMode.since, ctlMode.awakeAt = now, now
	// The releases of whatever JukaHub saw held go to the link from here on
	// (ctlModeEvent), so its own trackers would wait for them forever: a Menu
	// already down would open the voice orb by its hold timer mid-game, a
	// pushed stick would keep moving the virtual cursor, its held click
	// would stay down. An orb already open goes as it does for a game: no
	// button could answer it now.
	voiceOrbAway()
	vcursorResetAxes()
	ctlModeResend()
	PlayActivateSound()
}

// ctlModeEnd takes the pad back: the peer sees every button released and the
// panel lights again. why non-empty shows a toast ("menu" is silent: the
// page itself says Paused).
func ctlModeEnd(why string) {
	if !ctlMode.forwarding {
		return
	}
	ctlMode.forwarding = false
	ctlMode.pads = nil
	ctlMode.menuDown = time.Time{}
	ctlMode.menuChord, ctlMode.menuGuide = false, false
	ctlMode.sent = ctlPadState{}
	if ctlMode.running != nil {
		ctlMode.running.Send(ctlPadState{})
	}
	ctlModeScreenLight()
	// JukaHub's own stick readings were not fed while the link had the pad:
	// read them again so the virtual cursor and stick navigation start from
	// where the sticks are now.
	vcursorResetAxes()
	switch why {
	case "", "menu":
	case "lost":
		ShowToast("The controller link dropped — the pad is JukaHub's again", ToastKindWarning)
	default:
		ShowToast(why, ToastKindInfo)
	}
}

// ctlModePause is Menu's tap: forwarding stops, the link stays up.
func ctlModePause() {
	ctlModeEnd("menu")
	if ctlMode.running != nil {
		ctlMode.paused = true
	}
	PlayBackSound()
}

// ctlModeShutdown restores the backlight and says goodbye to the peer.
func ctlModeShutdown() {
	ctlModeScreenLight()
	if ctlMode.running != nil {
		ctlMode.running.Send(ctlPadState{})
		ctlMode.running.Stop()
		ctlMode.running = nil
	}
	ctlMode.forwarding = false
}

// ctlModeGameAway runs in main.go's parked loop while a game, an emulator or
// an app has the screen. The pad is theirs then and the page is not drawn
// (ctlModeTick does not run), so a link left up behind a launch (the Start
// menu, a phone, a launch hook) stops here: the peer sees every button
// released and the panel's light comes back.
func ctlModeGameAway() {
	if ctlMode.running == nil && !ctlMode.forwarding && !ctlMode.screenDark {
		return
	}
	logInfo("ctlmode", "a game took the screen: the controller link stops")
	ctlModeStopTransport()
	ctlModeScreenLight()
}

// ctlModeTick runs every frame while the page is up: the connect and
// disconnect edges, the Menu hold, the idle timers and the dark panel.
func ctlModeTick(now time.Time) {
	t := ctlMode.running
	if t == nil {
		return
	}
	st := t.Status()
	if st.State == ctlLinkConnected && ctlMode.lastState != ctlLinkConnected {
		ctlMode.peerID, ctlMode.peerName = st.PeerID, st.Peer
		ctlMode.layout = ctlModeLayoutFor(st.PeerID)
		ctlMode.paused = false
		ctlModeBegin()
		name := st.Peer
		if name == "" {
			name = "the receiver"
		}
		ShowToast("Connected to "+name+" — tap Menu to return to JukaHub", ToastKindSuccess)
	}
	if st.State != ctlLinkConnected && ctlMode.lastState == ctlLinkConnected {
		ctlMode.paused = false
		if ctlMode.forwarding {
			ctlModeEnd("lost")
		}
		ctlMode.peerID, ctlMode.peerName = "", ""
	}
	ctlMode.lastState = st.State
	if !ctlMode.forwarding {
		return
	}
	// An alarm, the power button's sleep or a game taking the screen ends
	// forwarding: each of them needs the pad.
	if alarmFiringActive() || powerSleepActive() {
		ctlModeEnd("")
		ctlMode.paused = true
		return
	}
	// Keep the event loop at its input cadence: a controller polled at the
	// idle frame rate would lag by a frame of 66 ms.
	if frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
	if now.Sub(ctlMode.awakeAt) >= ctlAwakeEvery {
		ctlMode.awakeAt = now
		noteUserInputTime(now)
	}
	if !ctlMode.menuDown.IsZero() && !ctlMode.menuGuide && ctlModeLayoutNow().MenuGuide &&
		now.Sub(ctlMode.menuDown) >= ctlMenuHoldGuide {
		ctlMode.menuGuide = true
		ctlModeResend()
	}
	// The panel goes dark through devpower.go's switch, the one sleep and the
	// dark dock use: it takes the newer TrimUIs' sysfs path, leaves a marker
	// the next start relights a crashed run's panel from, comes back on in
	// the panic cleanup, and keeps brightness polls off the borrowed level.
	// A light already off for someone else (TV mode) is not ours to turn on
	// later, so it is left alone; a failed switch tries again in 15 s.
	if !ctlMode.screenDark && ctlScreenSetting() == "off" && now.Sub(ctlMode.since) >= ctlScreenOffAfter &&
		backlightSupported() && !backlightIsOff.Load() {
		backlightOff(func() bool { return ctlMode.forwarding })
		if backlightIsOff.Load() {
			ctlMode.screenDark = true
		} else {
			ctlMode.since = now
		}
	}
}

// ctlModeScreenLight gives the panel its backlight back. During a sleep the
// sleep owns the light (it is dark anyway, and its wake relights it):
// switching it on here could light the panel of a device going to sleep.
func ctlModeScreenLight() {
	if !ctlMode.screenDark {
		return
	}
	ctlMode.screenDark = false
	if !powerSleepActive() {
		backlightOn()
	}
}

// ctlModeScreenDark reports whether the panel is dark for the session.
func ctlModeScreenDark() bool { return ctlMode.screenDark }

// ctlScreenSetting is "off" (darken while forwarding) or "on".
func ctlScreenSetting() string {
	if appConfig != nil {
		if v, ok := customValue(appConfig.Variables.Custom, ctlScreenKey); ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "on" {
				return "on"
			}
		}
	}
	return "off"
}

// ctlModeSetScreen saves the screen setting ("on" or "off").
func ctlModeSetScreen(v string) {
	if v != "on" {
		v = "off"
	}
	ctlStoreSetting(ctlScreenKey, v)
	if v == "on" {
		ctlModeScreenLight()
	}
}

// ctlLastTransport is the transport chosen last ("" none).
func ctlLastTransport() string {
	if appConfig != nil {
		if v, ok := customValue(appConfig.Variables.Custom, ctlLastTransportKey); ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// ctlStoreSetting writes a bookkeeping key straight into Custom and saves the
// user config (setStoreString would re-apply same-name mirrors).
func ctlStoreSetting(key, value string) {
	if appConfig == nil || appConfig.Variables.Custom == nil {
		return
	}
	appConfig.Variables.Custom[key] = value
	saveUserConfigDebounced(userConfigFromVariables(appConfig))
}

// ctlModeEvent is main.go's hook, run for every event before anything else
// sees it. While forwarding, physical pad events and keys are the link's and
// it returns true; otherwise it returns false and leaves the event alone.
func ctlModeEvent(event sdl.Event) bool {
	if !ctlMode.forwarding {
		return false
	}
	switch e := event.(type) {
	case *sdl.ControllerButtonEvent:
		// A ringing alarm gets the pad (ctlModeTick ends forwarding on its
		// next frame; the press goes to the alarm now).
		if alarmFiringActive() {
			return false
		}
		// Synthetic presses (key repeat, the Menu tap's Back, remapping) have
		// negative ids: only physical buttons are forwarded.
		if e.Which < 0 {
			return true
		}
		ctlModeButton(e.Which, padEventRaw, e.Type == sdl.CONTROLLERBUTTONDOWN)
		return true
	case *sdl.ControllerAxisEvent:
		if alarmFiringActive() {
			return false
		}
		if e.Which >= 0 && int(e.Axis) < len(ctlRawPad{}.axes) {
			p := ctlModePad(e.Which)
			p.axes[e.Axis] = e.Value
			ctlModeResend()
		}
		return true
	case *sdl.KeyboardEvent:
		// A keyboard's Escape is Menu's tap; other keys are swallowed so a
		// screen behind the page does not move.
		if e.Type == sdl.KEYDOWN && e.Repeat == 0 && e.Keysym.Sym == sdl.K_ESCAPE {
			ctlModePause()
		}
		return true
	case *sdl.TextInputEvent:
		return true
	case *sdl.ControllerDeviceEvent:
		// A pad unplugged mid-game sends no releases: what it held is let go
		// here, or the PC would keep it held. The event goes on to
		// handleControllerDevice as always.
		// Menu is tracked apart from the pads, so a Menu down (perhaps
		// on that pad, already sending Guide) is forgotten with it.
		if e.Type == sdl.CONTROLLERDEVICEREMOVED {
			delete(ctlMode.pads, e.Which)
			ctlMode.menuDown = time.Time{}
			ctlMode.menuChord, ctlMode.menuGuide = false, false
			ctlModeResend()
		}
		return false
	}
	return false
}

func ctlModePad(which sdl.JoystickID) *ctlRawPad {
	if ctlMode.pads == nil {
		ctlMode.pads = map[sdl.JoystickID]*ctlRawPad{}
	}
	p := ctlMode.pads[which]
	if p == nil {
		p = &ctlRawPad{}
		ctlMode.pads[which] = p
	}
	return p
}

// ctlModeButton applies one physical button (SDL's button before JukaHub's
// own swaps and folds).
func ctlModeButton(which sdl.JoystickID, raw uint8, down bool) {
	now := time.Now()
	if raw == sdl.CONTROLLER_BUTTON_GUIDE {
		if down {
			ctlMode.menuDown = now
			ctlMode.menuChord, ctlMode.menuGuide = false, false
			return
		}
		if ctlMode.menuDown.IsZero() {
			return
		}
		wasGuide, chord := ctlMode.menuGuide, ctlMode.menuChord
		ctlMode.menuDown = time.Time{}
		ctlMode.menuChord, ctlMode.menuGuide = false, false
		if wasGuide {
			ctlModeResend()
			return
		}
		if !chord {
			ctlModePause()
		}
		return
	}
	bit := ctlRawButtonBit(raw)
	if bit == 0 {
		return
	}
	p := ctlModePad(which)
	if down {
		p.buttons |= bit
		ctlMode.presses++
		// Menu with another button is Guide + that button (Steam's Guide
		// chords), never the tap that returns.
		if !ctlMode.menuDown.IsZero() {
			ctlMode.menuChord = true
			if ctlModeLayoutNow().MenuGuide {
				ctlMode.menuGuide = true
			}
		}
	} else {
		p.buttons &^= bit
	}
	ctlModeResend()
}

// ctlRawButtonBit maps an SDL button to its controller bit (0 for buttons
// a controller does not have: paddles, the touchpad, MISC1).
func ctlRawButtonBit(b uint8) uint32 {
	switch b {
	case sdl.CONTROLLER_BUTTON_A:
		return ctlBtnA
	case sdl.CONTROLLER_BUTTON_B:
		return ctlBtnB
	case sdl.CONTROLLER_BUTTON_X:
		return ctlBtnX
	case sdl.CONTROLLER_BUTTON_Y:
		return ctlBtnY
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return ctlBtnLB
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return ctlBtnRB
	case sdl.CONTROLLER_BUTTON_BACK:
		return ctlBtnBack
	case sdl.CONTROLLER_BUTTON_START:
		return ctlBtnStart
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		return ctlBtnLS
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return ctlBtnRS
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return ctlBtnUp
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return ctlBtnDown
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return ctlBtnLeft
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return ctlBtnRight
	}
	return 0
}

// ctlModeResend merges the pads, applies the layout and hands the result to
// the transport when it changed.
func ctlModeResend() {
	if !ctlMode.forwarding || ctlMode.running == nil {
		return
	}
	var merged ctlRawPad
	for _, p := range ctlMode.pads {
		merged.buttons |= p.buttons
		for i, v := range p.axes {
			if ctlAbs16(v) > ctlAbs16(merged.axes[i]) {
				merged.axes[i] = v
			}
		}
	}
	st := ctlApplyLayout(ctlModeLayoutNow(), merged, ctlMode.menuGuide)
	if st == ctlMode.sent {
		return
	}
	ctlMode.sent = st
	ctlMode.running.Send(st)
}

func ctlAbs16(v int16) int32 {
	if v < 0 {
		return -int32(v)
	}
	return int32(v)
}

// ctlApplyLayout turns the merged physical state into the controller's.
func ctlApplyLayout(l ctlLayout, raw ctlRawPad, guide bool) ctlPadState {
	b := raw.buttons
	if l.Face == ctlFaceLabel {
		b = ctlSwapBits(b, ctlBtnA, ctlBtnB)
		b = ctlSwapBits(b, ctlBtnX, ctlBtnY)
	}
	if guide {
		b |= ctlBtnGuide
	}
	st := ctlPadState{}
	st.LX, st.LY = ctlDeadzone(raw.axes[0], raw.axes[1], l.Deadzone)
	st.RX, st.RY = ctlDeadzone(raw.axes[2], raw.axes[3], l.Deadzone)
	if l.InvertRY {
		st.RY = ctlNeg16(st.RY)
	}
	st.LT = ctlTriggerByte(raw.axes[4])
	st.RT = ctlTriggerByte(raw.axes[5])
	if st.LT >= ctlTriggerPressed {
		b |= ctlBtnLT
	}
	if st.RT >= ctlTriggerPressed {
		b |= ctlBtnRT
	}
	if l.Dpad == ctlDpadStick && b&ctlBtnDpad != 0 {
		// The D-pad wins over a resting stick only: a stick pushed on purpose
		// keeps its own value.
		if st.LX == 0 && st.LY == 0 {
			if b&ctlBtnLeft != 0 {
				st.LX = -32767
			} else if b&ctlBtnRight != 0 {
				st.LX = 32767
			}
			if b&ctlBtnUp != 0 {
				st.LY = -32767
			} else if b&ctlBtnDown != 0 {
				st.LY = 32767
			}
		}
		b &^= ctlBtnDpad
	}
	st.Buttons = b
	return st
}

func ctlSwapBits(b, x, y uint32) uint32 {
	hasX, hasY := b&x != 0, b&y != 0
	b &^= x | y
	if hasX {
		b |= y
	}
	if hasY {
		b |= x
	}
	return b
}

func ctlNeg16(v int16) int16 {
	if v == -32768 {
		return 32767
	}
	return -v
}

// ctlTriggerByte maps SDL's 0..32767 trigger axis onto 0..255.
func ctlTriggerByte(v int16) uint8 {
	if v <= 0 {
		return 0
	}
	return uint8(int32(v) * 255 / 32767)
}

// ctlDeadzone applies a radial dead zone of pct percent and rescales the rest
// of the travel so the edge still reads full.
func ctlDeadzone(x, y int16, pct int) (int16, int16) {
	if pct <= 0 {
		return x, y
	}
	fx, fy := float64(x)/32767, float64(y)/32767
	mag := fx*fx + fy*fy
	dz := float64(pct) / 100
	if mag <= dz*dz {
		return 0, 0
	}
	m := math.Sqrt(mag)
	scale := (m - dz) / (1 - dz) / m
	return ctlClamp16(fx * scale * 32767), ctlClamp16(fy * scale * 32767)
}

func ctlClamp16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32767 {
		return -32767
	}
	return int16(v)
}

// ctlModeRumble vibrates the handheld for a peer that asked (a game's
// rumble). Any goroutine may call it; strength is 0..1.
func ctlModeRumble(strength float32, ms uint32) {
	if strength <= 0 || ms == 0 {
		return
	}
	if strength > 1 {
		strength = 1
	}
	if ms > 1000 {
		ms = 1000
	}
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	if !hapticTried {
		hapticTried = true
		hapticDev = openPadHaptics()
	}
	if hapticDev == nil {
		return
	}
	if err := hapticDev.play(strength, ms); err != nil {
		logDebug("ctlmode", "rumble failed: %v", err)
	}
}

// ctlError is an error carrying a sentence for the page.
type ctlError string

func (e ctlError) Error() string { return string(e) }

const errCtlUnknown = ctlError("That controller link is not part of this build")

// ctlModeEnsureScene injects the page's scene (toolsHubEnsureScenes calls
// it at startup and after every reload).
func ctlModeEnsureScene(config *Config) {
	if config == nil {
		return
	}
	toolsHubEnsureScene(config, ctlModeSceneName, "G", "Use this handheld as a controller for a PC or phone")
}

// ctlModeOpen opens the page. It is not offered while a game has the screen
// and the pad (ctlmode_ui.go ctlModeGameRunning).
func ctlModeOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if ctlModeGameRunning() {
		ShowToast(ctlUIGameRunningMsg, ToastKindWarning)
		return
	}
	ctlModeEnsureScene(config)
	toolsHubOpenScene(config, ctlModeSceneName)
}
