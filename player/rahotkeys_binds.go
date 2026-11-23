package main

// rahotkeys_binds.go — player 1's pad as RetroArch reads it, for the custom
// hotkeys (rahotkeys.go): which joypad code each logical button sends.
//
// A RetroArch hotkey is a joypad code (`input_save_state_btn = "3"`), and the
// code for X differs between firmwares and joypad drivers (sdl2 and udev
// number the same buttons differently), which is why osdpreset.go never
// writes one. The codes are not guessed here either: they are copied from the
// config's own player-1 bindings, the way the light gun copies Start and
// Select (lightgun.go):
//
//   - retroarch.cfg's input_player1_<button>_btn, or its _axis when the
//     button is an axis (the Smart Pro's L2 and R2 are ABS_Z / ABS_RZ, so
//     their bind is "+2"-style) or a hat ("h0up" for the D-pad);
//   - else the handheld's own autoconfig profile (input_<button>_btn /
//     _axis there), found by raPadProfile — RetroArch uses the profile's bind
//     whenever the config leaves a button unbound.
//
// The Menu key is no RetroPad button and has no player-1 bind. Its code is
// the firmware's own hotkey or menu bind (input_menu_toggle_btn /
// input_enable_hotkey_btn, in the profile or the config) when that code is
// none of the RetroPad buttons' — the TrimUI firmwares put RetroArch's
// hotkey on Menu (gameassist_linux.go). Otherwise Menu stays unknown and a
// hotkey on it is not written.

import (
	"os"
	"path/filepath"
	"strings"
)

// raPadBind is one button's bind: a button or hat code, or an axis.
type raPadBind struct {
	Btn  string
	Axis string
}

func (b raPadBind) ok() bool { return b.Btn != "" || b.Axis != "" }

// same reports whether two binds are the same input.
func (b raPadBind) same(o raPadBind) bool {
	return (b.Btn != "" && b.Btn == o.Btn) || (b.Axis != "" && b.Axis == o.Axis)
}

// raPadButtons are the logical buttons a hotkey can use, with the RetroPad
// name RetroArch's bind keys use ("" for Menu, which has none). Menu order.
var raPadButtons = []struct{ ID, Label, Bind string }{
	{"select", "Select", "select"},
	{"start", "Start", "start"},
	{"menu", "Menu", ""},
	{"a", "A", "a"},
	{"b", "B", "b"},
	{"x", "X", "x"},
	{"y", "Y", "y"},
	{"l1", "L1", "l"},
	{"r1", "R1", "r"},
	{"l2", "L2", "l2"},
	{"r2", "R2", "r2"},
	{"l3", "L3", "l3"},
	{"r3", "R3", "r3"},
	{"up", "Up", "up"},
	{"down", "Down", "down"},
	{"left", "Left", "left"},
	{"right", "Right", "right"},
}

// raPadButtonLabel is a logical button's name on the rows: L3 / R3 are F1 /
// F2 on a Brick, whose front keys send them (stickclicks.go).
func raPadButtonLabel(id string) string {
	for _, b := range raPadButtons {
		if b.ID == id {
			return padStickClickName(b.Label)
		}
	}
	return id
}

// raPadButtonKnown reports whether id is one of raPadButtons.
func raPadButtonKnown(id string) bool {
	for _, b := range raPadButtons {
		if b.ID == id {
			return true
		}
	}
	return false
}

// raPadBindIn reads one bind from a config's text: prefix + name + _btn,
// else + _axis. Pure.
func raPadBindIn(text, prefix, name string) raPadBind {
	if v, ok := raConfigGet(text, prefix+name+"_btn"); ok && lightgunBindUsable(v) {
		return raPadBind{Btn: strings.TrimSpace(v)}
	}
	if v, ok := raConfigGet(text, prefix+name+"_axis"); ok && lightgunBindUsable(v) {
		return raPadBind{Axis: strings.TrimSpace(v)}
	}
	return raPadBind{}
}

// raPadRead is player 1's pad from a retroarch.cfg's text and the pad's
// autoconfig profile text ("" when none): each logical button JukaHub could
// find a code for. Pure.
func raPadRead(cfg, profile string) map[string]raPadBind {
	out := map[string]raPadBind{}
	for _, b := range raPadButtons {
		if b.Bind == "" {
			continue
		}
		bind := raPadBindIn(cfg, "input_player1_", b.Bind)
		if !bind.ok() && profile != "" {
			bind = raPadBindIn(profile, "input_", b.Bind)
		}
		if bind.ok() {
			out[b.ID] = bind
		}
	}
	// Menu: the firmware's own menu or hotkey code, when no RetroPad button
	// sends it; the config's before the profile's, as RetroArch reads them.
	for _, src := range []struct{ text, prefix, name string }{
		{cfg, "input_", "menu_toggle"},
		{profile, "input_", "menu_toggle"},
		{cfg, "input_", "enable_hotkey"},
		{profile, "input_", "enable_hotkey"},
	} {
		if src.text == "" {
			continue
		}
		bind := raPadBindIn(src.text, src.prefix, src.name)
		if !bind.ok() {
			continue
		}
		taken := false
		for _, other := range out {
			taken = taken || other.same(bind)
		}
		if !taken {
			out["menu"] = bind
			break
		}
	}
	return out
}

// raPadButtonFor is the logical button a bind is, "" when none of them.
// Pure.
func raPadButtonFor(pad map[string]raPadBind, bind raPadBind) string {
	if !bind.ok() {
		return ""
	}
	for _, b := range raPadButtons {
		if p, ok := pad[b.ID]; ok && p.same(bind) {
			return b.ID
		}
	}
	return ""
}

// raPadProfile finds the handheld pad's autoconfig profile for a
// retroarch.cfg: its path and text, "" when there is none. Only files named
// after the TrimUI pad are read — RetroArch ships a thousand profiles, and
// reading them all at a launch is not on — in joypad_autoconfig_dir (else
// "autoconfig" beside the config), the joypad driver's own folder first:
// sdl2 and udev number the same buttons differently. Card I/O; the launch
// path. The light gun reads its Start and Select from the same profile.
func raPadProfile(cfg, cfgPath string) (string, string) {
	dir := filepath.Join(filepath.Dir(cfgPath), "autoconfig")
	if v, ok := raConfigGet(cfg, "joypad_autoconfig_dir"); ok {
		if d := videoFilterExpandDir(v, cfgPath); d != "" {
			dir = d
		}
	}
	joypad, _ := raConfigGet(cfg, "input_joypad_driver")
	joypad = strings.ToLower(strings.TrimSpace(joypad))
	dirs := []string{dir}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if joypad != "" && strings.EqualFold(e.Name(), joypad) {
				dirs = append([]string{sub}, dirs...)
			} else {
				dirs = append(dirs, sub)
			}
		}
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".cfg") || !strings.Contains(name, "trimui") {
				continue
			}
			path := filepath.Join(d, e.Name())
			data, err := ctrlLayoutReadSmall(path)
			if err != nil {
				continue
			}
			return path, string(data)
		}
	}
	return "", ""
}
