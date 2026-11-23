package main

// lightgun.go — Light-gun mode: aim with the right stick, fire with a
// shoulder button, calibrated per game, for the RetroArch cores that read a
// light gun (NES Zapper, SNES Super Scope, Mega Drive Menacer, Master System
// Light Phaser, Sega CD and PlayStation guns, arcade gun games).
//
// A libretro core reads a light gun when the port it is plugged into has a
// light-gun device type (retroarch.cfg input_libretro_device_p<N>), and
// RetroArch aims that gun with its mouse: the pointer's place in the game
// picture is where the gun points, the left button pulls the trigger and the
// right one shoots off screen (reload). A handheld has no mouse, so JukaHub
// makes one. The pieces:
//
//   - a control layout, "Light gun" (ctrllayout.go), offered for the consoles
//     that had guns. Its variants, one per core JukaHub knows, take the
//     shoulders and R3 away from the game for that start (the remap file the
//     Buttons choice writes; they aim and fire now) and set the core's own
//     options — crosshair on, gun input read as a light gun. On a core not in
//     the table below the layout changes nothing and says so.
//   - lightgunPrepare, a launch hook (gamelaunch_hooks.go) that sets, for that
//     start only and through raScopeApply (racfgscope.go), the port's device
//     type, the mouse that aims it, the trigger and reload on the mouse's
//     buttons, the gun's own Start / Select / side buttons copied from the
//     pad's bindings (a gun on port 1 replaces the pad there, and Start must
//     still start), and RetroArch's menu mouse off (a shot must not click a
//     menu row). It then makes the mouse: a uinput device created BEFORE
//     RetroArch starts, because an SDL that looks for devices once at start
//     would not find one made later (lightgun_linux.go).
//   - the aim engine (lightgun_aim.go): "Move" turns the stick's tilt into
//     the crosshair's speed; "Point" makes the stick's position a spot in the
//     aim area (the game picture by default), which needs to know where the
//     pointer is: the pointer is pushed hard into the top-left corner (SDL
//     keeps it inside the window), then moved from there.
//   - per-game settings (aim style, speed, aim area, trigger and reload
//     button, a buzz on each shot) in <data>/light-gun.json, edited and tried
//     on a practice target from the game page's More → Light gun
//     (lightgun_ui.go).
//   - a gate hook that gives a known gun game (lightgun_games.go) the layout
//     once, when it has no layout of its own; the file remembers that it did,
//     so a game switched to None stays None.
//
// During the game JukaHub draws nothing, so there are no live adjustments:
// Select and the D-pad belong to the game and to RetroArch's hotkeys on many
// firmwares (state slots), and a change nobody can see is guesswork. The R
// shoulder that does not fire (and R3, where the sticks click) puts the
// crosshair back in the middle of the aim area (and re-learns where the
// pointer is), which is the one fix a drifting pointer needs. The pad is
// never grabbed: the game keeps reading Start, Select, the D-pad and the face
// buttons. Mouse in games and apps stays out of the way while the gun is on
// (vcursorBridgeTick).
//
// On Windows there is no uinput: the PC's own mouse is the gun, and the
// layout and the launch settings still put the gun on the right port.
//
// Device ids are RETRO_DEVICE_SUBCLASS(base, id) = ((id+1) << 8) | base, with
// LIGHTGUN = 4 and MOUSE = 2. Where they come from is in docs/LIGHT_GUN.md;
// none is verified on a TrimUI yet (LIGHTGUN-HW-1 in MODERNIZATION.md).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// lightgunLayoutID is the control layout's id (stored per game).
	lightgunLayoutID = "light-gun"
	// lightgunRecordName is the settings file in JukaHub's data folder.
	lightgunRecordName    = "light-gun.json"
	lightgunSchemaVersion = 1
)

// The systems the layout is offered for: the library's own names
// (romSystemAliases, romsetup.go).
var lightgunSystems = []string{"NES", "SNES", "Mega Drive", "Master System", "Sega CD", "PlayStation", "Arcade"}

// ── The cores ───────────────────────────────────────────────────────────────

// lightgunCore is how one core reads a gun for one console.
type lightgunCore struct {
	// Cores are gameCoreFromLauncher's names.
	Cores []string
	// Systems are library system names; empty is every system the core plays.
	Systems []string
	Label   string // the core as RetroArch names it
	Gun     string // the gun it emulates, for the screens
	// Port is the RetroArch port (1-based) the gun is plugged into.
	Port int
	// Devices maps a gun kind ("" the console's usual one, "justifier" …) to
	// the device type; 0 leaves the device type alone (the core's own game
	// database plugs the gun in, or a core option reads it).
	Devices map[string]int
	// Options are the core's own options, set through the layout's variant.
	Options map[string]string
	// Sure: the device id and options are from the core's documentation or
	// source; otherwise from other front ends' configurations.
	Sure bool
}

// lightgunCores is the table. A core named nowhere here gets nothing: the
// layout says plainly that JukaHub does not know its gun.
var lightgunCores = []lightgunCore{
	{
		// Zapper = RETRO_DEVICE_SUBCLASS(MOUSE, 0); "clightgun" (the crosshair
		// light gun) makes it read RetroArch's light gun. Older FCEUmm called
		// it "lightgun"; to them this is an unknown value, and RetroArch then
		// uses the core's default, which there is that same light gun.
		Cores: []string{"fceumm"}, Systems: []string{"NES"}, Label: "FCEUmm", Gun: "Zapper", Port: 2,
		Devices: map[string]int{"": 258},
		Options: map[string]string{"fceumm_zapper_mode": "clightgun", "fceumm_show_crosshair": "enabled"},
	},
	{
		// Nestopia plugs the Zapper in itself for the games its database
		// knows; the option makes it read the light gun.
		Cores: []string{"nestopia"}, Systems: []string{"NES"}, Label: "Nestopia", Gun: "Zapper", Port: 2,
		Devices: map[string]int{"": 0},
		Options: map[string]string{"nestopia_zapper_device": "lightgun", "nestopia_show_crosshair": "enabled"},
		Sure:    true,
	},
	{
		Cores: []string{"snes9x"}, Systems: []string{"SNES"}, Label: "Snes9x", Gun: "Super Scope", Port: 2,
		Devices: map[string]int{"": 260, "justifier": 516},
		Sure:    true,
	},
	{
		Cores: []string{"snes9x2010"}, Systems: []string{"SNES"}, Label: "Snes9x 2010", Gun: "Super Scope", Port: 2,
		Devices: map[string]int{"": 260, "justifier": 516},
	},
	{
		// Phaser = SUBCLASS(LIGHTGUN, 0), Menacer = 1, Justifiers = 2.
		Cores: []string{"genesis_plus_gx"}, Systems: []string{"Mega Drive", "Sega CD"}, Label: "Genesis Plus GX", Gun: "Menacer", Port: 2,
		Devices: map[string]int{"": 516, "justifier": 772},
		Options: map[string]string{"genesis_plus_gx_gun_input": "lightgun", "genesis_plus_gx_gun_cursor": "enabled"},
		Sure:    true,
	},
	{
		Cores: []string{"genesis_plus_gx"}, Systems: []string{"Master System"}, Label: "Genesis Plus GX", Gun: "Light Phaser", Port: 1,
		Devices: map[string]int{"": 260},
		Options: map[string]string{"genesis_plus_gx_gun_input": "lightgun", "genesis_plus_gx_gun_cursor": "enabled"},
	},
	{
		// guncon 260 and konami gun 516 (a RetroArch log of the core).
		Cores: []string{"pcsx_rearmed"}, Systems: []string{"PlayStation"}, Label: "PCSX-ReARMed", Gun: "GunCon", Port: 1,
		Devices: map[string]int{"": 260, "justifier": 516},
		Options: map[string]string{"pcsx_rearmed_crosshair1": "red"},
		Sure:    true,
	},
	{
		// Beetle's gun options default to a light gun with a cross.
		Cores: []string{"mednafen_psx", "mednafen_psx_hw"}, Systems: []string{"PlayStation"}, Label: "Beetle PSX", Gun: "GunCon", Port: 1,
		Devices: map[string]int{"": 260, "justifier": 516},
	},
	{
		Cores: []string{"swanstation"}, Systems: []string{"PlayStation"}, Label: "SwanStation", Gun: "GunCon", Port: 1,
		Devices: map[string]int{"": 260},
	},
	{
		// FinalBurn Neo's "Lightgun" device is RETRO_DEVICE_LIGHTGUN itself;
		// its crosshair is hidden with a gun device unless told otherwise.
		Cores: []string{"fbneo"}, Systems: []string{"Arcade"}, Label: "FinalBurn Neo", Gun: "arcade gun", Port: 1,
		Devices: map[string]int{"": 4},
		Options: map[string]string{"fbneo-lightgun-crosshair-emulation": "always show"},
	},
	{
		// MAME 2003-Plus reads the gun through its X-Y device option; the
		// port stays a pad (its buttons are the cabinet's).
		Cores: []string{"mame2003_plus"}, Systems: []string{"Arcade"}, Label: "MAME 2003-Plus", Gun: "arcade gun", Port: 1,
		Devices: map[string]int{"": 0},
		Options: map[string]string{"mame2003-plus_xy_device": "lightgun", "mame2003-plus_crosshair_enabled": "enabled"},
	},
}

// lightgunSameSystem compares two library system names the way the
// compatibility table does ("Genesis" is "Mega Drive").
func lightgunSameSystem(a, b string) bool {
	return strings.EqualFold(canonicalCompatSystem(a), canonicalCompatSystem(b))
}

// lightgunCoreFor finds the table row for core playing a system's game.
func lightgunCoreFor(core, system string) (lightgunCore, bool) {
	core = normalizeCoreName(core)
	for _, c := range lightgunCores {
		named := false
		for _, n := range c.Cores {
			if n == core {
				named = true
			}
		}
		if !named {
			continue
		}
		if len(c.Systems) == 0 {
			return c, true
		}
		for _, s := range c.Systems {
			if lightgunSameSystem(s, system) {
				return c, true
			}
		}
	}
	return lightgunCore{}, false
}

// lightgunDevice is the device type for a gun kind on c: the kind's own, else
// the console's usual gun.
func lightgunDevice(c lightgunCore, kind string) int {
	if d, ok := c.Devices[kind]; ok {
		return d
	}
	return c.Devices[""]
}

// lightgunKnownCores lists every core in the table once.
func lightgunKnownCores() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range lightgunCores {
		for _, n := range c.Cores {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ── The layout ──────────────────────────────────────────────────────────────

// lightgunShoulders are the pad buttons the gun takes over: they reach the
// game as nothing for that start. The R shoulder that does not fire re-centres
// the crosshair, and so does R3 where the sticks click (lightgun_aim.go).
var lightgunShoulders = map[string]string{"l": "none", "r": "none", "l2": "none", "r2": "none", "r3": "none"}

func lightgunLayout() ctrlLayout {
	// The re-centre is named on the shoulder every model has: the original
	// Smart Pro's sticks do not click, so an R3 line would name a button it
	// lacks (stickclicks.go).
	chart := ctrlLayoutLines(
		"Right stick|Aims the crosshair", "R1|Fires (or R2: More → Light gun)",
		"L1|Reloads: a shot off the screen (or L2)", "R2|Puts the crosshair back in the middle (R1 when R2 fires)",
		"A, B, X|The gun's side buttons", "Start, Select|Start and Select (coin)")
	noSticks := ctrlLayoutLines(
		"D-pad|Aims the crosshair (not the game's D-pad)", "R1|Fires (or R2)",
		"L1|Reloads: a shot off the screen (or L2)", "R2|Puts the crosshair back in the middle (R1 when R2 fires)",
		"A, B, X|The gun's side buttons", "Start, Select|Start and Select (coin)")
	l := ctrlLayout{
		ID:          lightgunLayoutID,
		Name:        "Light gun",
		Description: "Aim with the right stick, fire with R1, reload with L1 · More → Light gun tunes it",
		Systems:     lightgunSystems,
		Chart:       chart,
		Source:      "light-gun",
	}
	// Only the cores in the table: on any other the layout changes nothing,
	// rather than taking the shoulders away for a gun that is not there.
	optionsByCore := map[string]map[string]string{}
	for _, c := range lightgunCores {
		for _, n := range c.Cores {
			if len(c.Options) > 0 && len(raTuneCoreFolders[n]) > 0 {
				optionsByCore[n] = c.Options
			}
		}
	}
	for _, n := range lightgunKnownCores() {
		l.Variants = append(l.Variants, ctrlLayoutVariant{
			Cores:       []string{n},
			Buttons:     lightgunShoulders,
			CoreOptions: optionsByCore[n],
			Chart:       chart,
		})
	}
	l.Variants = append(l.Variants, ctrlLayoutVariant{
		Cores:    lightgunKnownCores(),
		NoSticks: true,
		Buttons:  map[string]string{"up": "none", "down": "none", "left": "none", "right": "none"},
		Chart:    noSticks,
	})
	return l
}

// ── Per-game settings (<data>/light-gun.json) ───────────────────────────────

// lightgunSettings is one game's calibration.
type lightgunSettings struct {
	// Aim: "move" (the tilt is the speed) or "point" (the stick's position is
	// a spot in the aim area).
	Aim string `json:"aim,omitempty"`
	// Speed: 1 to 5, for Move.
	Speed int `json:"speed,omitempty"`
	// Area: "picture" (a 4:3 picture in the middle of the screen), "full" or
	// "custom" (AreaW × AreaH percent of the screen, its middle moved by AreaX
	// / AreaY percent), for Point.
	Area  string `json:"area,omitempty"`
	AreaW int    `json:"areaW,omitempty"`
	AreaH int    `json:"areaH,omitempty"`
	AreaX int    `json:"areaX,omitempty"`
	AreaY int    `json:"areaY,omitempty"`
	// Trigger "r1" or "r2"; Reload "l1" or "l2".
	Trigger string `json:"trigger,omitempty"`
	Reload  string `json:"reload,omitempty"`
	// Recoil buzzes the motor on each shot.
	Recoil bool `json:"recoil,omitempty"`
	// Auto: the game was given the layout automatically once (or the player
	// chose for it): the gate never gives it again.
	Auto bool `json:"auto,omitempty"`
}

type lightgunRecord struct {
	SchemaVersion int `json:"schemaVersion"`
	// AutoAssign: known gun games get the layout by themselves (nil is on).
	AutoAssign *bool                       `json:"autoAssign,omitempty"`
	Games      map[string]lightgunSettings `json:"games"`
}

// The bounds of the settings.
const (
	lightgunSpeedMin, lightgunSpeedMax, lightgunSpeedDefault = 1, 5, 3
	lightgunAreaMin, lightgunAreaMax                         = 20, 100
	lightgunShiftMax                                         = 40
)

// lightgunDefaults is a game's settings before anything is chosen.
func lightgunDefaults() lightgunSettings {
	return lightgunSettings{Aim: "move", Speed: lightgunSpeedDefault, Area: "picture", AreaW: 100, AreaH: 100, Trigger: "r1", Reload: "l1"}
}

// sane keeps every value inside its choices, so a hand-edited file can make
// the gun odd but never broken. Pure.
func (s lightgunSettings) sane() lightgunSettings {
	if s.Aim != "point" {
		s.Aim = "move"
	}
	if s.Speed < lightgunSpeedMin || s.Speed > lightgunSpeedMax {
		s.Speed = lightgunSpeedDefault
	}
	switch s.Area {
	case "full", "custom":
	default:
		s.Area = "picture"
	}
	clampInt := func(v, lo, hi, zero int) int {
		switch {
		case v == 0:
			return zero
		case v < lo:
			return lo
		case v > hi:
			return hi
		}
		return v
	}
	s.AreaW = clampInt(s.AreaW, lightgunAreaMin, lightgunAreaMax, lightgunAreaMax)
	s.AreaH = clampInt(s.AreaH, lightgunAreaMin, lightgunAreaMax, lightgunAreaMax)
	s.AreaX = clampInt(s.AreaX, -lightgunShiftMax, lightgunShiftMax, 0)
	s.AreaY = clampInt(s.AreaY, -lightgunShiftMax, lightgunShiftMax, 0)
	if s.Trigger != "r2" {
		s.Trigger = "r1"
	}
	if s.Reload != "l2" {
		s.Reload = "l1"
	}
	return s
}

// lightgunStore is the file in memory, plus what the last launch of a game
// could not do (shown in the settings; not saved).
var lightgunStore struct {
	mu       sync.Mutex
	loaded   bool
	readOnly bool
	auto     *bool
	games    map[string]lightgunSettings
	notes    map[string]string
	// announce is the game given the layout automatically at its launch: its
	// end says how the gun works (a toast raised as a game starts is never
	// seen).
	announce string
}

// lightgunStorePath is the file; a variable for tests.
var lightgunStorePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, lightgunRecordName)
}

func lightgunLoadLocked() {
	if lightgunStore.loaded {
		return
	}
	lightgunStore.loaded = true
	lightgunStore.games = map[string]lightgunSettings{}
	path := lightgunStorePath()
	if path == "" {
		return
	}
	var file lightgunRecord
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("lightgun", "ignoring %s: %v", path, err)
		}
		return
	}
	if file.SchemaVersion > lightgunSchemaVersion {
		logWarn("lightgun", "%s is from a newer JukaHub (schema %d); defaults used, the file is left as it is", path, file.SchemaVersion)
		lightgunStore.readOnly = true
		return
	}
	lightgunStore.auto = file.AutoAssign
	for rom, s := range file.Games {
		if key := gameOptionsKey(rom); key != "" {
			lightgunStore.games[key] = s.sane()
		}
	}
}

// lightgunStoreLoad reads the file if it has not been read (any thread).
func lightgunStoreLoad() {
	lightgunStore.mu.Lock()
	lightgunLoadLocked()
	lightgunStore.mu.Unlock()
}

// lightgunStoreLoaded reports the file in memory: the chip answers only then.
func lightgunStoreLoaded() bool {
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	return lightgunStore.loaded
}

// lightgunChange edits the record under the lock and writes it whole (it is
// small). It reports whether the change was saved.
func lightgunChange(change func() bool) bool {
	lightgunStore.mu.Lock()
	lightgunLoadLocked()
	if lightgunStore.readOnly {
		lightgunStore.mu.Unlock()
		ShowToast("Light gun settings were saved by a newer JukaHub · update to change them", ToastKindWarning)
		return false
	}
	if !change() {
		lightgunStore.mu.Unlock()
		return true
	}
	file := lightgunRecord{SchemaVersion: lightgunSchemaVersion, AutoAssign: lightgunStore.auto,
		Games: make(map[string]lightgunSettings, len(lightgunStore.games))}
	for k, v := range lightgunStore.games {
		file.Games[k] = v
	}
	lightgunStore.mu.Unlock()
	path := lightgunStorePath()
	if path == "" {
		return false
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("lightgun", "cannot save the light gun settings: %v", err)
		ShowToast("Cannot save the light gun settings: "+firstLine(err.Error()), ToastKindWarning)
		return false
	}
	return true
}

// lightgunSettingsFor is a game's settings (the defaults when it has none).
func lightgunSettingsFor(romPath string) lightgunSettings {
	key := gameOptionsKey(romPath)
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	lightgunLoadLocked()
	if s, ok := lightgunStore.games[key]; ok && key != "" {
		return s.sane()
	}
	return lightgunDefaults()
}

// lightgunSaveSettings stores a game's settings (its Auto mark is kept).
func lightgunSaveSettings(romPath string, s lightgunSettings) bool {
	key := gameOptionsKey(romPath)
	if key == "" {
		return false
	}
	s = s.sane()
	return lightgunChange(func() bool {
		old, had := lightgunStore.games[key]
		s.Auto = s.Auto || old.Auto
		if had && old == s {
			return false
		}
		lightgunStore.games[key] = s
		return true
	})
}

// lightgunMarkDecided records that the game's light gun was decided (given
// automatically or chosen by the player): the gate leaves it alone from now.
func lightgunMarkDecided(romPath string) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	lightgunChange(func() bool {
		s, had := lightgunStore.games[key]
		if !had {
			s = lightgunDefaults()
		}
		if s.Auto {
			return false
		}
		s.Auto = true
		lightgunStore.games[key] = s
		return true
	})
}

// lightgunDecided reports a game whose light gun was decided already.
func lightgunDecided(romPath string) bool {
	key := gameOptionsKey(romPath)
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	lightgunLoadLocked()
	return key != "" && lightgunStore.games[key].Auto
}

// lightgunAutoAssignOn reports whether known gun games get the layout by
// themselves (on unless turned off).
func lightgunAutoAssignOn() bool {
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	lightgunLoadLocked()
	return lightgunStore.auto == nil || *lightgunStore.auto
}

func lightgunSetAutoAssign(on bool) bool {
	return lightgunChange(func() bool {
		if (lightgunStore.auto == nil || *lightgunStore.auto) == on {
			return false
		}
		v := on
		lightgunStore.auto = &v
		return true
	})
}

// lightgunNote is what the last launch of a game could not set up, "" when
// all of it went in.
func lightgunNote(romPath string) string {
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	return lightgunStore.notes[gameOptionsKey(romPath)]
}

func lightgunSetNote(romPath, note string) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	lightgunStore.mu.Lock()
	defer lightgunStore.mu.Unlock()
	if lightgunStore.notes == nil {
		lightgunStore.notes = map[string]string{}
	}
	if note == "" {
		delete(lightgunStore.notes, key)
		return
	}
	lightgunStore.notes[key] = note
}

// ── retroarch.cfg for one launch ────────────────────────────────────────────

// lightgunBindNames are the pad bindings the gun copies from player 1: the
// gun's own button → the pad button it is bound like.
var lightgunBindNames = [][2]string{
	{"gun_aux_a", "a"}, {"gun_aux_b", "b"}, {"gun_aux_c", "x"},
	{"gun_start", "start"}, {"gun_select", "select"},
}

// lightgunDpadNames are the gun's D-pad bindings, copied only on a device
// with sticks: on a Brick the D-pad aims.
var lightgunDpadNames = [][2]string{
	{"gun_dpad_up", "up"}, {"gun_dpad_down", "down"}, {"gun_dpad_left", "left"}, {"gun_dpad_right", "right"},
}

// lightgunBindUsable is a binding value worth copying ("nul" binds nothing).
// A value with a quote or a line break would break the line it is written
// to (`key = "value"`), so it is not one.
func lightgunBindUsable(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && !strings.EqualFold(v, "nul") && !strings.ContainsAny(v, "\"\r\n\x00")
}

// lightgunPadBinds reads player 1's button bindings from a retroarch.cfg
// (input_player1_<button>_btn) and the joypad it reads. Pure.
func lightgunPadBinds(cfg string) map[string]string {
	out := map[string]string{}
	for _, names := range [][][2]string{lightgunBindNames, lightgunDpadNames} {
		for _, n := range names {
			if v, ok := raConfigGet(cfg, "input_player1_"+n[1]+"_btn"); ok && lightgunBindUsable(v) {
				out[n[1]] = strings.TrimSpace(v)
			}
		}
	}
	if v, ok := raConfigGet(cfg, "input_player1_joypad_index"); ok && strings.TrimSpace(v) != "" {
		out["joypad_index"] = strings.TrimSpace(v)
	}
	return out
}

// lightgunAutoconfigBinds fills the bindings retroarch.cfg leaves to the
// pad's autoconfig profile (input_<button>_btn there), found by raPadProfile
// (rahotkeys_binds.go, shared with the custom hotkeys). Card I/O; the launch
// path.
func lightgunAutoconfigBinds(cfg, cfgPath string, have map[string]string) {
	path, text := raPadProfile(cfg, cfgPath)
	if path == "" {
		return
	}
	for _, names := range [][][2]string{lightgunBindNames, lightgunDpadNames} {
		for _, n := range names {
			if _, ok := have[n[1]]; ok {
				continue
			}
			if v, ok := raConfigGet(text, "input_"+n[1]+"_btn"); ok && lightgunBindUsable(v) {
				have[n[1]] = strings.TrimSpace(v)
			}
		}
	}
	logInfo("lightgun", "pad bindings the config leaves out read from %s", path)
}

// lightgunCfgPlan is what one launch's gun needs from retroarch.cfg.
type lightgunCfgPlan struct {
	Port   int
	Device int
	Sticks bool
	// Players is how many Local play seats there are: with a second player,
	// port 2's pad is theirs, and the gun's buttons read it.
	Players int
}

// lightgunCfgValues are the retroarch.cfg keys a gun launch sets, from the
// config's own text (player 1's bindings) and binds (those, plus any the
// autoconfig profile gave). Pure.
func lightgunCfgValues(cfg string, plan lightgunCfgPlan, binds map[string]string) []raScopeValue {
	if plan.Port < 1 || plan.Port > 8 {
		return nil
	}
	p := "input_player" + strconv.Itoa(plan.Port) + "_"
	values := []raScopeValue{
		{Key: p + "mouse_index", Value: "0"},
		{Key: p + "gun_trigger_mbtn", Value: "1"},
		{Key: p + "gun_offscreen_shot_mbtn", Value: "2"},
		// A shot must not click RetroArch's menu while it is open.
		{Key: "menu_mouse_enable", Value: "false"},
	}
	if plan.Device > 0 {
		values = append(values, raScopeValue{Key: "input_libretro_device_p" + strconv.Itoa(plan.Port), Value: strconv.Itoa(plan.Device)})
	}
	names := lightgunBindNames
	if plan.Sticks {
		names = append(append([][2]string(nil), lightgunBindNames...), lightgunDpadNames...)
	}
	for _, n := range names {
		if v, ok := binds[n[1]]; ok && lightgunBindUsable(v) {
			values = append(values, raScopeValue{Key: p + n[0] + "_btn", Value: v})
		}
	}
	if plan.Port > 1 {
		// A gun on port 2 reads its buttons from port 2's joypad, which is no
		// pad on a handheld: point it at player 1's, unless a second player's
		// pad is there.
		if plan.Players < 2 {
			idx := binds["joypad_index"]
			if idx == "" {
				idx = "0"
			}
			values = append(values, raScopeValue{Key: p + "joypad_index", Value: idx})
		}
		// Port 2 is read only when RetroArch has two users.
		users := 0
		if v, ok := raConfigGet(cfg, "input_max_users"); ok {
			users, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		if users < plan.Port {
			values = append(values, raScopeValue{Key: "input_max_users", Value: strconv.Itoa(plan.Port)})
		}
	}
	return values
}

// ── The launch ──────────────────────────────────────────────────────────────

// lightgunPanelSize is the display in pixels, which is the window RetroArch
// opens. Main thread.
func lightgunPanelSize() (int, int) {
	if mode, err := sdl.GetCurrentDisplayMode(0); err == nil && mode.W > 0 && mode.H > 0 {
		return int(mode.W), int(mode.H)
	}
	if mainWindow != nil {
		if w, h := mainWindow.GetSize(); w > 0 && h > 0 {
			return int(w), int(h)
		}
	}
	return 1280, 720
}

// lightgunGameKind is the gun a known game wants ("" its console's usual).
func lightgunGameKind(game libraryGame) string {
	if t, ok := lightgunKnownGame(game); ok {
		return t.Kind
	}
	return ""
}

// lightgunPrepare is the launch hook: for a game played with the Light gun
// layout in a core JukaHub knows, the launch's retroarch.cfg keys and the
// mouse that aims. It never blocks or fails a launch. Main thread.
func lightgunPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("lightgun", "preparing the light gun failed: %v", r)
		}
	}()
	// A gun left from a launch that never started is gone first.
	lightgunSessionStop("", "a new launch")
	if l, ok := ctrlLayoutForGame(game); !ok || l.ID != lightgunLayoutID {
		lightgunSetNote(game.Path, "")
		return
	}
	r, ok := ctrlLayoutLaunchFor(launcher, game)
	if !ok || r.Layout.ID != lightgunLayoutID {
		lightgunSetNote(game.Path, "")
		return
	}
	spec, known := lightgunCoreFor(r.Core, game.System)
	if !known {
		logInfo("lightgun", "%s: JukaHub does not know how %s reads a light gun; the pad plays as it is", game.Path, r.Core)
		lightgunSetNote(game.Path, "JukaHub does not know this core's light gun ("+r.Core+")")
		return
	}
	set := lightgunSettingsFor(game.Path)
	sticks := stickHardware()
	plan := lightgunCfgPlan{Port: spec.Port, Device: lightgunDevice(spec, lightgunGameKind(game)), Sticks: sticks, Players: len(hotseat.players)}
	var notes []string
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logInfo("lightgun", "no retroarch.cfg found for %s; the gun's port is not set", launcher)
		notes = append(notes, "No RetroArch settings file found: the gun's port was not set")
	}
	for _, cfgPath := range targets {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		cfg := string(raw)
		binds := lightgunPadBinds(cfg)
		if _, ok := binds["start"]; !ok {
			lightgunAutoconfigBinds(cfg, cfgPath, binds)
		}
		if _, ok := binds["start"]; !ok {
			logInfo("lightgun", "%s binds no Start for player 1; the gun's Start is left as it is", cfgPath)
		}
		driver, _ := raConfigGet(cfg, "input_driver")
		driver = strings.ToLower(strings.TrimSpace(driver))
		logInfo("lightgun", "%s: %s on port %d (device %d), input driver %q, %s", cfgPath, spec.Gun, spec.Port, plan.Device, driver, set.Aim)
		if driver == "linuxraw" {
			notes = append(notes, "RetroArch's input driver (linuxraw) reads no mouse: set it to sdl2 or udev")
		}
		if _, err := raScopeApply(cfgPath, lightgunCfgValues(cfg, plan, binds)); err != nil {
			logWarn("lightgun", "cannot set the gun in %s: %v", cfgPath, err)
			notes = append(notes, "The gun's RetroArch settings could not be written")
		}
	}
	if lightgunSessionSupported() {
		w, h := lightgunPanelSize()
		// The goroutine buzzes the motor: find it here, on the main thread.
		hapticsAvailable()
		err := lightgunSessionStart(lightgunSessionParams{
			Path:   game.Path,
			Recoil: set.Recoil,
			Aim:    lightgunAimParams{Set: set, CalR: stickCalFor(appConfig, stickRight), Sticks: sticks, W: w, H: h},
		})
		if err != nil {
			logWarn("lightgun", "no light gun for %s: %v", game.Path, err)
			notes = append(notes, "The gun could not be made: "+firstLine(err.Error()))
		}
	}
	// Two configs of one RetroArch say the same thing once.
	var said []string
	for _, n := range notes {
		dup := false
		for _, s := range said {
			dup = dup || s == n
		}
		if !dup {
			said = append(said, n)
		}
	}
	lightgunSetNote(game.Path, strings.Join(said, " · "))
}

// lightgunExited removes the gun with the game, and says how it works after
// the first game that got it by itself. Reaper goroutine.
func lightgunExited(game libraryGame) {
	lightgunSessionStop(game.Path, "the game ended")
	lightgunStore.mu.Lock()
	announce := lightgunStore.announce != "" && lightgunStore.announce == game.Path
	if announce {
		lightgunStore.announce = ""
	}
	lightgunStore.mu.Unlock()
	if announce {
		aim := "the right stick aims"
		if !stickHardware() {
			aim = "the D-pad aims"
		}
		ShowToast(game.Label()+" played with the light gun: "+aim+", R1 fires, L1 reloads · More → Light gun to tune it or turn it off", ToastKindInfo)
	}
}

// lightgunGate gives a known gun game the Light gun layout once, when it has
// no layout of its own and its core is one JukaHub knows. It never holds the
// launch. Main thread.
func lightgunGate(game libraryGame, launcher string) (bool, error) {
	// The title first: it is answered from memory, while retroArchLauncher
	// reads the launch script, and every launch passes through here.
	title, ok := lightgunAutoGame(game)
	if !ok || strings.TrimSpace(launcher) == "" || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return false, nil
	}
	if !lightgunAutoAssignOn() || ctrlLayoutGameChoice(game.Path) != "" || lightgunDecided(game.Path) {
		return false, nil
	}
	if l, ok := ctrlLayoutForGame(game); ok && l.ID == lightgunLayoutID {
		return false, nil
	}
	core := normalizeCoreName(gameCoreFromLauncher(launcher, game.System))
	if _, ok := lightgunCoreFor(core, game.System); !ok {
		logInfo("lightgun", "%s is a light-gun game, but JukaHub does not know %s's gun; left as it is", game.Label(), core)
		return false, nil
	}
	if !ctrlLayoutSetGame(game.Path, lightgunLayoutID) {
		return false, nil
	}
	lightgunMarkDecided(game.Path)
	lightgunStore.mu.Lock()
	lightgunStore.announce = game.Path
	lightgunStore.mu.Unlock()
	logInfo("lightgun", "%s (%s) is a light-gun game: given the Light gun layout once (More → Light gun turns it off)", game.Label(), title.Names[0])
	return false, nil
}

func init() {
	l := lightgunLayout()
	ctrlLayoutRegister(l)
	gameLaunchRegisterGate(lightgunGate)
	gameLaunchRegisterPrepare(lightgunPrepare)
	// The gun goes live once the game's process is there.
	gameLaunchRegisterStarted(func(game libraryGame, _ string, _ *exec.Cmd) {
		lightgunSessionLive(game.Path)
	})
	gameLaunchRegisterExited(func(game libraryGame, _ string, _ time.Duration, _ error) {
		lightgunExited(game)
	})
	// Quitting removes the mouse even with a game still running.
	Cleanup = append(Cleanup, func() { lightgunSessionStop("", "JukaHub is quitting") })
	deferStartupTask("light-gun", func() { go lightgunStoreLoad() })
}
