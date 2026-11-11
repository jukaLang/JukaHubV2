package main

// hotseat.go — local multiplayer on one device.
//
// The ideas list asked for two things. The first is noticing a second pad:
// when a controller is plugged in or paired and two or more are connected, a
// toast says local multiplayer is ready. The second is a simple menu — Local
// play (hotseat_ui.go) — where every player presses A on their own pad to
// join, gets a colour and a P1/P2 badge, and can be moved to another port.
// Launching a RetroArch game from the library then hands RetroArch the
// player → controller order. For games played by passing one device around
// there is a pass-and-play turn tracker: a full-screen "Pass to Player 2"
// card between turns, a turn counter and an optional turn timer.
//
// JukaHub normally opens one controller (gamepad_hotplug.go). The others are
// opened while Local play is on screen, so their A can be read, and stay
// open for the players who joined; ending the session closes them again.

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	hotseatMaxPlayers = 4
	// hotseatAppendName is the one-launch RetroArch config the player order
	// is written to, in the temp folder.
	hotseatAppendName = "jukahub-local-play.cfg"
	// hotseatScriptScan is how much of a launch script is read to see whether
	// it passes its arguments on.
	hotseatScriptScan = 64 << 10
)

// hotseatPad is a controller Local play opened besides the main one.
type hotseatPad struct {
	id   sdl.JoystickID
	name string
	pad  *sdl.GameController
}

// hotseatPlayer is one joined player; the slice order is the port order.
type hotseatPlayer struct {
	id   sdl.JoystickID
	name string
}

// hotseatTurns is a pass-and-play session: whose turn it is, how many turns
// were played and the optional turn timer.
type hotseatTurns struct {
	active   bool
	seats    int
	current  int
	turn     int
	limitSec int
	started  time.Time
	// passing is true while the "Pass to …" card is up, between turns.
	passing bool
	// expired says the last turn ended because its time ran out; notified
	// that the toast for it was shown on another screen.
	expired  bool
	notified bool
}

// hotseat is the session; main thread only.
var hotseat struct {
	extras  []hotseatPad
	players []hotseatPlayer
	// count is how many controllers were connected at the last change.
	count int
	// note is what the last launch did with the player order.
	note  string
	turns hotseatTurns
}

// hotseatColors are the players' colours, P1 to P4: the blue, red, green and
// amber of player lights on most pads.
var hotseatColors = [hotseatMaxPlayers]sdl.Color{
	{R: 79, G: 140, B: 255, A: 255},
	{R: 255, G: 90, B: 95, A: 255},
	{R: 61, G: 220, B: 132, A: 255},
	{R: 255, G: 176, B: 32, A: 255},
}

// hotseatColor is player i's colour (0-based).
func hotseatColor(i int) sdl.Color {
	if i < 0 {
		i = 0
	}
	return hotseatColors[i%hotseatMaxPlayers]
}

// Test seams: the controller count and the clock.
var (
	hotseatCountHook func() int
	hotseatClockHook func() time.Time
)

// hotseatAppendDir is the folder the one-launch RetroArch config is written
// to; a test points it at a temporary directory.
var hotseatAppendDir = os.TempDir

func hotseatNow() time.Time {
	if hotseatClockHook != nil {
		return hotseatClockHook()
	}
	return time.Now()
}

// ── Controllers ──────────────────────────────────────────────────────────

// hotseatControllerCount is how many game controllers are connected.
func hotseatControllerCount() int {
	if hotseatCountHook != nil {
		return hotseatCountHook()
	}
	n := 0
	for i := 0; i < sdl.NumJoysticks(); i++ {
		if sdl.IsGameController(i) {
			n++
		}
	}
	return n
}

// hotseatPrimaryID is the instance id of the pad JukaHub runs on, or -1.
func hotseatPrimaryID() sdl.JoystickID {
	if gameController == nil {
		return -1
	}
	joy := gameController.Joystick()
	if joy == nil {
		return -1
	}
	return joy.InstanceID()
}

// hotseatExtraIndex finds an opened extra pad by instance id, or -1.
func hotseatExtraIndex(id sdl.JoystickID) int {
	for i, x := range hotseat.extras {
		if x.id == id {
			return i
		}
	}
	return -1
}

// hotseatPadName names a pad for its card.
func hotseatPadName(id sdl.JoystickID) string {
	if i := hotseatExtraIndex(id); i >= 0 && strings.TrimSpace(hotseat.extras[i].name) != "" {
		return hotseat.extras[i].name
	}
	if id == hotseatPrimaryID() && gameController != nil {
		if name := strings.TrimSpace(gameController.Name()); name != "" {
			return name
		}
	}
	return "Controller"
}

// hotseatOpenExtras opens every attached controller that is not open yet, so
// its A can be read on the Local play screen.
func hotseatOpenExtras() {
	primary := hotseatPrimaryID()
	for i := 0; i < sdl.NumJoysticks(); i++ {
		if !sdl.IsGameController(i) {
			continue
		}
		id := sdl.JoystickGetDeviceInstanceID(i)
		if id < 0 || id == primary || hotseatExtraIndex(id) >= 0 {
			continue
		}
		pad := sdl.GameControllerOpen(i)
		if pad == nil {
			continue
		}
		hotseat.extras = append(hotseat.extras, hotseatPad{id: id, name: pad.Name(), pad: pad})
	}
}

// hotseatCloseIdle lets go of the extra pads nobody joined with, or of every
// extra pad when keepJoined is false.
func hotseatCloseIdle(keepJoined bool) {
	kept := hotseat.extras[:0]
	for _, x := range hotseat.extras {
		if keepJoined && hotseatPlayerIndex(x.id) >= 0 {
			kept = append(kept, x)
			continue
		}
		if x.pad != nil {
			x.pad.Close()
		}
	}
	hotseat.extras = kept
}

// hotseatHasIdleExtras reports whether an extra pad is open that nobody
// joined with.
func hotseatHasIdleExtras() bool {
	for _, x := range hotseat.extras {
		if hotseatPlayerIndex(x.id) < 0 {
			return true
		}
	}
	return false
}

// hotseatDeviceChanged follows a controller being added or removed
// (gamepad_hotplug.go): a new pad is opened while Local play is on screen, a
// pad that left is let go, and the count is announced.
func hotseatDeviceChanged(e *sdl.ControllerDeviceEvent) {
	if e == nil {
		return
	}
	switch e.Type {
	case sdl.CONTROLLERDEVICEADDED:
		if hotseatLobbyOnScreen() {
			hotseatOpenExtras()
		}
	case sdl.CONTROLLERDEVICEREMOVED:
		hotseatPadGone(e.Which)
	}
	hotseatCountChanged(hotseatControllerCount())
}

// hotseatCountChanged announces a new controller count: when two or more are
// connected, and again for each pad added after that.
func hotseatCountChanged(n int) {
	prev := hotseat.count
	hotseat.count = n
	if n >= 2 && n > prev {
		ShowToast(strconv.Itoa(n)+" controllers connected — local multiplayer ready (Tools → Local play)", ToastKindInfo)
	}
}

// hotseatPadGone lets go of a pad that was unplugged and of its player.
func hotseatPadGone(id sdl.JoystickID) {
	inmapPadRemoved(id)
	if i := hotseatExtraIndex(id); i >= 0 {
		if pad := hotseat.extras[i].pad; pad != nil {
			pad.Close()
		}
		hotseat.extras = append(hotseat.extras[:i], hotseat.extras[i+1:]...)
	}
	if i := hotseatPlayerIndex(id); i >= 0 {
		hotseat.players = append(hotseat.players[:i], hotseat.players[i+1:]...)
		ShowToast("Player "+strconv.Itoa(i+1)+"'s controller disconnected", ToastKindWarning)
	}
}

// ── Players ──────────────────────────────────────────────────────────────

// hotseatPlayerIndex is a pad's port (0-based), or -1 when it has not joined.
func hotseatPlayerIndex(id sdl.JoystickID) int {
	for i, p := range hotseat.players {
		if p.id == id {
			return i
		}
	}
	return -1
}

// hotseatJoin adds the player holding pad id and reports whether it joined.
func hotseatJoin(id sdl.JoystickID, name string) bool {
	if hotseatPlayerIndex(id) >= 0 {
		return false
	}
	if len(hotseat.players) >= hotseatMaxPlayers {
		ShowToast("Four players is the most Local play seats", ToastKindInfo)
		return false
	}
	hotseat.players = append(hotseat.players, hotseatPlayer{id: id, name: name})
	return true
}

// hotseatLeave removes the player holding pad id.
func hotseatLeave(id sdl.JoystickID) bool {
	i := hotseatPlayerIndex(id)
	if i < 0 {
		return false
	}
	hotseat.players = append(hotseat.players[:i], hotseat.players[i+1:]...)
	return true
}

// hotseatMove swaps player i with its neighbour in direction dir (a port to
// the left or the right) and reports whether it moved.
func hotseatMove(i, dir int) bool {
	j := i + dir
	if i < 0 || i >= len(hotseat.players) || j < 0 || j >= len(hotseat.players) {
		return false
	}
	hotseat.players[i], hotseat.players[j] = hotseat.players[j], hotseat.players[i]
	return true
}

// hotseatReset ends the session: players, turns and the extra pads go.
func hotseatReset() {
	hotseat.players = nil
	hotseat.turns = hotseatTurns{}
	hotseat.note = ""
	hotseatCloseIdle(false)
}

// ── Launching RetroArch ──────────────────────────────────────────────────

// hotseatJoypadIndex is the index RetroArch knows a controller by: the
// XInput slot on Windows (SDL's player index for an XInput pad), otherwise
// its place in the device list, which RetroArch's udev and SDL drivers walk
// in the same order. -1 when the pad is gone.
func hotseatJoypadIndex(id sdl.JoystickID) int {
	for i := 0; i < sdl.NumJoysticks(); i++ {
		if sdl.JoystickGetDeviceInstanceID(i) != id {
			continue
		}
		if IsWindows() {
			if slot := sdl.JoystickGetDevicePlayerIndex(i); slot >= 0 {
				return slot
			}
		}
		return i
	}
	return -1
}

// hotseatAppendConfig is the RetroArch config that maps player N to the
// controller at indices[N-1]; a player whose pad is gone (-1) is left out.
func hotseatAppendConfig(indices []int) string {
	var sb strings.Builder
	sb.WriteString("# Written by JukaHub Local play for one launch\n")
	for n, idx := range indices {
		if idx < 0 {
			continue
		}
		sb.WriteString("input_player" + strconv.Itoa(n+1) + "_joypad_index = \"" + strconv.Itoa(idx) + "\"\n")
	}
	return sb.String()
}

// hotseatMergeAppendConfig adds --appendconfig=path to a launch's arguments,
// joining a config the game's own options already append: RetroArch takes
// several as "a|b".
func hotseatMergeAppendConfig(extra []string, path string) []string {
	out := make([]string, 0, len(extra)+1)
	merged := false
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		switch {
		case strings.HasPrefix(arg, "--appendconfig="):
			out = append(out, arg+"|"+path)
			merged = true
		case arg == "--appendconfig" && i+1 < len(extra):
			out = append(out, arg, extra[i+1]+"|"+path)
			merged = true
			i++
		default:
			out = append(out, arg)
		}
	}
	if !merged {
		out = append(out, "--appendconfig="+path)
	}
	return out
}

// hotseatScriptForwards reports whether a launch script passes its extra
// arguments on as arguments: "$@" or a bare $* in a shell script, %* in a batch
// file. A stock launch.sh that passes only "$1" does not, and neither does one
// that passes "$*" — the stock and CrossMix scripts end in
// `ra64.trimui ... -L <core> "$*"`, which joins everything into ONE argument,
// so an appended --appendconfig became part of the ROM path and RetroArch
// could not open the game at all. "$@" wins over a quoted "$*" elsewhere in
// the script: JukaHub's own PPSSPP launcher forwards with "$@" and joins its
// arguments with "$*" only for a log line.
func hotseatScriptForwards(head string, batch bool) bool {
	if batch {
		return strings.Contains(head, "%*")
	}
	if strings.Contains(head, "$@") || strings.Contains(head, "${@}") {
		return true
	}
	if strings.Contains(head, `"$*"`) || strings.Contains(head, `"${*}"`) {
		return false
	}
	return strings.Contains(head, "$*")
}

// hotseatScriptHead reads the start of a launch script.
func hotseatScriptHead(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, hotseatScriptScan))
	if err != nil {
		return ""
	}
	return string(head)
}

// hotseatLauncherForwards reports whether a launch reaches RetroArch with its
// extra arguments: a Windows core (started with -L by the RetroArch beside
// it), RetroArch itself, or a script that passes them on.
func hotseatLauncherForwards(launcher string) bool {
	switch strings.ToLower(filepath.Ext(launcher)) {
	case ".dll":
		return true
	case ".sh", "":
		return hotseatScriptForwards(hotseatScriptHead(launcher), false)
	case ".bat", ".cmd":
		return hotseatScriptForwards(hotseatScriptHead(launcher), true)
	}
	return true
}

// hotseatLaunchArgs adds the player order to a game's extra arguments when
// two or more players joined and the game runs in RetroArch: a one-launch
// config with input_playerN_joypad_index lines, passed with --appendconfig.
// A launcher that does not hand its arguments on to RetroArch (a stock
// launch.sh that passes only the ROM) is left alone, and Local play says so.
func hotseatLaunchArgs(launcher string, extra []string) []string {
	if len(hotseat.players) < 2 {
		return extra
	}
	if !retroArchLauncher(launcher) {
		hotseat.note = "Player order only reaches RetroArch games; this one sets up its own controllers"
		return extra
	}
	if !hotseatLauncherForwards(launcher) {
		hotseat.note = "This system's launcher does not pass options on to RetroArch, so RetroArch's own controller order is used"
		return extra
	}
	indices := make([]int, 0, len(hotseat.players))
	for _, p := range hotseat.players {
		indices = append(indices, hotseatJoypadIndex(p.id))
	}
	path := filepath.Join(hotseatAppendDir(), hotseatAppendName)
	if err := os.WriteFile(path, []byte(hotseatAppendConfig(indices)), 0o644); err != nil {
		hotseat.note = "Could not hand the player order to RetroArch: " + firstLine(err.Error())
		return extra
	}
	hotseat.note = "The last game started with players 1–" + strconv.Itoa(len(hotseat.players)) + " in this order"
	return hotseatMergeAppendConfig(extra, path)
}

// ── Pass and play ────────────────────────────────────────────────────────

// start begins a session for seats players with a turn limit in seconds (0
// for none). It opens on the card that hands the device to Player 1.
func (t *hotseatTurns) start(seats, limitSec int) {
	if seats < 2 {
		seats = 2
	}
	if seats > hotseatMaxPlayers {
		seats = hotseatMaxPlayers
	}
	*t = hotseatTurns{active: true, seats: seats, turn: 1, limitSec: limitSec, passing: true}
}

// begin starts the current player's turn (A on the card).
func (t *hotseatTurns) begin(now time.Time) {
	if !t.active {
		return
	}
	t.passing, t.expired, t.notified = false, false, false
	t.started = now
}

// pass ends the current turn and puts the card up for the next player.
func (t *hotseatTurns) pass(expired bool) {
	if !t.active || t.passing {
		return
	}
	t.current = (t.current + 1) % t.seats
	t.turn++
	t.passing, t.expired = true, expired
	t.started = time.Time{}
}

// remaining is how many whole seconds the turn has left, or -1 when there is
// no timer running.
func (t *hotseatTurns) remaining(now time.Time) int {
	if !t.active || t.limitSec <= 0 || t.passing || t.started.IsZero() {
		return -1
	}
	left := t.limitSec - int(now.Sub(t.started)/time.Second)
	if left < 0 {
		left = 0
	}
	return left
}

// tick ends a turn whose time is up and reports whether it just did.
func (t *hotseatTurns) tick(now time.Time) bool {
	if t.remaining(now) != 0 {
		return false
	}
	t.pass(true)
	return true
}

// round is the round the session is in: everyone has one turn per round.
func (t *hotseatTurns) round() int {
	if t.seats <= 0 || t.turn <= 0 {
		return 1
	}
	return (t.turn-1)/t.seats + 1
}

// hotseatSeatNames are the seats' names; the cards draw them every frame.
var hotseatSeatNames = [hotseatMaxPlayers]string{"Player 1", "Player 2", "Player 3", "Player 4"}

// hotseatSeatName names seat i (0-based) the way the cards do.
func hotseatSeatName(i int) string {
	if i >= 0 && i < hotseatMaxPlayers {
		return hotseatSeatNames[i]
	}
	return "Player " + strconv.Itoa(i+1)
}

// hotseatTick runs the turn timer from any screen (inmapTick calls it). A
// turn that runs out while another screen is up says so in a toast; the card
// waits on the Local play screen.
func hotseatTick(config *Config) {
	onScreen := inmapCurrentScene(config) == hotseatScene
	// The pads nobody joined with were opened for the lobby only.
	if !onScreen && hotseatHasIdleExtras() {
		hotseatCloseIdle(true)
	}
	t := &hotseat.turns
	if !t.active || !t.tick(hotseatNow()) {
		return
	}
	if !onScreen && !t.notified {
		t.notified = true
		ShowToast("Time's up — pass to "+hotseatSeatName(t.current), ToastKindWarning)
		return
	}
	PlayNavSound()
}

// hotseatPassTurn ends the turn of a running pass-and-play session (the
// pass_turn action and A on the turn view). It reports false when no turn is
// running.
func hotseatPassTurn(config *Config) bool {
	t := &hotseat.turns
	if !t.active || t.passing {
		return false
	}
	t.pass(false)
	PlayActivateSound()
	if inmapCurrentScene(config) != hotseatScene {
		ShowToast("Pass to "+hotseatSeatName(t.current), ToastKindInfo)
	}
	return true
}
