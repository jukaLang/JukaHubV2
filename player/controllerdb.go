package main

// controllerdb.go — the controller database and the mapping a pad needs before
// SDL will treat it as a game controller (INP-9).
//
// SDL ships a small fallback table, but most of the pads people actually plug
// into a Windows PC (DualSense, DualShock 4, Switch Pro, 8BitDo) are described
// by the community database, not by SDL's built-in list. The database is
// embedded and pinned by SHA-256: a changed file cannot slip in without the
// constant and this test changing with it. Mappings a user records in the
// Controller test panel go to data/gamecontrollerdb-user.txt and are loaded
// after the embedded table, so a recorded mapping wins.
//
// The HIDAPI hints are set before SDL_Init: SDL reads them when it creates the
// joystick subsystem, and setting them afterwards has no effect. They are what
// lets SDL's own drivers see a DualSense/DS4/Switch Pro/8BitDo pad over USB or
// Bluetooth on Windows instead of the generic HID joystick path.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

//go:embed gamecontrollerdb.txt
var controllerDBText string

// controllerDBSum is the SHA-256 of gamecontrollerdb.txt at the pinned revision.
// Update both together when the database is refreshed.
const controllerDBSum = "70215ee73f266b91e863fe42bd798bd5a1cee471932748c6660d9c9b97b96c7c"

// controllerHIDAPIHintNames are the SDL hints that enable the HIDAPI drivers
// for the pads INP-9 names. 8BitDo pads identify as a Switch Pro or an Xbox
// pad depending on their mode, so those two drivers cover them.
var controllerHIDAPIHintNames = []string{
	"SDL_JOYSTICK_HIDAPI",
	"SDL_JOYSTICK_HIDAPI_PS4",
	"SDL_JOYSTICK_HIDAPI_PS5",
	"SDL_JOYSTICK_HIDAPI_SWITCH",
	"SDL_JOYSTICK_HIDAPI_XBOX",
	"SDL_JOYSTICK_HIDAPI_JOY_CONS",
	"SDL_JOYSTICK_HIDAPI_COMBINE_JOY_CONS",
}

// controllerSetHIDAPIHints turns the HIDAPI drivers on. Call before sdl.Init.
func controllerSetHIDAPIHints() {
	for _, name := range controllerHIDAPIHintNames {
		sdl.SetHint(name, "1")
	}
}

// controllerDBHash returns the hex SHA-256 of the embedded database.
func controllerDBHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// controllerDBLines returns the mapping lines in text: comments and blank
// lines dropped, whitespace trimmed. It does not validate the bindings; SDL
// does that when a line is added.
func controllerDBLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, ",") || !strings.Contains(line, "a:") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// controllerAddMapping is a test hook; the app uses SDL.
var controllerAddMapping = sdl.GameControllerAddMapping

// controllerDBLoadPinned adds the embedded table. A hash mismatch is refused
// rather than half-trusted: the log line names both hashes.
func controllerDBLoadPinned() int {
	if got := controllerDBHash(controllerDBText); got != controllerDBSum {
		logWarn("gamepad", "controller database hash %s does not match the pinned %s; not loading it", got, controllerDBSum)
		return 0
	}
	added := 0
	for _, line := range controllerDBLines(controllerDBText) {
		if controllerAddMapping(line) >= 0 {
			added++
		}
	}
	logInfo("gamepad", "controller database: %d mappings loaded", added)
	return added
}

// controllerDBUserPath is the user's recorded mappings, next to the rest of
// JukaHub's data.
func controllerDBUserPath() string {
	return filepath.Join(bkupDataDir(), "gamecontrollerdb-user.txt")
}

// controllerDBLoadUser adds mappings recorded in the Controller test panel. A
// missing file is the normal first run; a bad one is logged and skipped.
func controllerDBLoadUser() int {
	data, err := os.ReadFile(controllerDBUserPath())
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("gamepad", "could not read the user controller database: %v", err)
		}
		return 0
	}
	added := 0
	for _, line := range controllerDBLines(string(data)) {
		if controllerAddMapping(line) >= 0 {
			added++
		}
	}
	if added > 0 {
		logInfo("gamepad", "controller database: %d user mappings loaded", added)
	}
	return added
}

// controllerDBAppendUser stores one recorded mapping. The file is created 0600
// and appended to, so a recording never rewrites the mappings already there.
func controllerDBAppendUser(line string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return fmt.Errorf("empty or multi-line controller mapping")
	}
	path := controllerDBUserPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// controllerMapStep is one button the recording asks for. The token is the SDL
// mapping name (a:bN, back:bN, ...).
type controllerMapStep struct {
	Token string
	Label string
}

// controllerMapSteps is the set a pad needs for menus: face buttons, start and
// back, the D-pad and the shoulders. Sticks and triggers stay SDL's defaults;
// this flow is for a pad SDL has no mapping for at all, not a remap editor.
var controllerMapSteps = []controllerMapStep{
	{"a", "A"},
	{"b", "B"},
	{"x", "X"},
	{"y", "Y"},
	{"back", "Back"},
	{"start", "Start"},
	{"dpup", "D-pad up"},
	{"dpdown", "D-pad down"},
	{"dpleft", "D-pad left"},
	{"dpright", "D-pad right"},
	{"leftshoulder", "LB"},
	{"rightshoulder", "RB"},
}

// controllerPlatform is SDL's platform name for mapping strings.
func controllerPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Mac OS X"
	default:
		return "Linux"
	}
}

// controllerMappingLine builds the SDL mapping string from the buttons captured
// for each step. It refuses a missing step, a duplicate button (two actions on
// one physical button is never what the user meant) and a GUID that is not the
// 32 hex digits SDL printed. The order is controllerMapSteps, so the same
// recording always produces the same file line.
func controllerMappingLine(guid, name, platform string, buttons map[string]uint8) (string, bool) {
	guid = strings.ToLower(strings.TrimSpace(guid))
	if len(guid) != 32 {
		return "", false
	}
	for _, r := range guid {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", false
		}
	}
	if platform == "" {
		platform = controllerPlatform()
	}
	seen := map[uint8]string{}
	var b strings.Builder
	b.WriteString(guid)
	b.WriteByte(',')
	b.WriteString(controllerMappingName(name))
	for _, step := range controllerMapSteps {
		index, ok := buttons[step.Token]
		if !ok {
			return "", false
		}
		if _, dup := seen[index]; dup {
			return "", false
		}
		seen[index] = step.Token
		fmt.Fprintf(&b, ",%s:b%d", step.Token, index)
	}
	fmt.Fprintf(&b, ",platform:%s,", platform)
	return b.String(), true
}

// controllerMappingName makes a name safe for the comma-separated mapping line.
func controllerMappingName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, ",", " "))
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "JukaHub pad"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
