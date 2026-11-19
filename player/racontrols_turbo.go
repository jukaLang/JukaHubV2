package main

// racontrols_turbo.go — the Turbo choice (racontrols.go) as RetroArch's own
// Turbo Fire, set in retroarch.cfg for one launch through raScopeApply.
//
// Why RetroArch's Turbo Fire and not the cores' own turbo options: a handful
// of cores have one (FCEUmm, Gambatte, Beetle PCE…), each with other keys and
// with "Turbo A/B" on extra RetroPad buttons whose places even the libretro
// docs do not agree on; mGBA, Snes9x and Genesis Plus GX have none. Turbo
// Fire is the same for every core, so "holding A fires A" means one thing on
// every console.
//
// RetroArch changed Turbo Fire twice, so the keys depend on the build. The
// build is not asked (the binary has no cheap version query before a game
// runs); retroarch.cfg says it, because RetroArch saves every setting it
// knows into the file, so the keys present are those of the last build that
// wrote it:
//
//   - RetroArch 1.21 and newer (the "Turbo Fire overhaul", RetroArch pull
//     request 17633; the file has input_turbo_bind / input_turbo_button /
//     input_turbo_enable). The turbo bind is a RetroPad button. libretro's
//     Turbo Fire guide gives the recipe for a button that autofires while it
//     is held: Single Button (Hold) mode with Turbo Bind and Turbo Button both
//     set to that button. So: input_turbo_enable true, input_turbo_mode 3
//     (Classic, Classic (Toggle), Single Button (Toggle), Single Button
//     (Hold)), input_turbo_bind and input_turbo_button the button's RetroPad
//     number (A 8, B 0). 1.22.1 and newer also read these keys from a remap
//     file, so a remap JukaHub writes for the same launch carries them too.
//   - RetroArch 1.20 and older. The turbo bind is a button of the physical
//     pad (input_player1_turbo_btn), and the Single Button modes only fire
//     while the button itself is up, so they cannot make a held button
//     autofire. Classic mode can: every button pressed while the turbo bind
//     is held fires repeatedly until it is let go. With the turbo bind on the
//     same physical button as A, holding A fires A. Classic is mode 0 in every
//     build (1.19 added Classic (Toggle) after it). The pad's number for A or
//     B comes from retroarch.cfg's input_player1_a_btn / _b_btn, else from the
//     handheld's own autoconfig profile (input_a_btn / input_b_btn in a
//     profile whose input_device names a TrimUI pad). When neither has it, the
//     launch is left without turbo, logged, and the game options sheet says
//     so. One quirk of Classic: another button pressed while A is held fires
//     repeatedly too, until it is let go.
//
// The speed is input_turbo_period frames per press (Fast 4, Normal 6 —
// RetroArch's default — Slow 10) with input_turbo_duty_cycle half of it, the
// split RetroArch's default (6/3) and 1.21's automatic duty both use. Builds
// before 1.21 save the duty as input_duty_cycle (the overhaul moved every
// turbo key under input_turbo_*, RetroArch pull request 17633), so the
// Classic path sets that name too.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// raTurboModeClassic and raTurboModeHold are input_turbo_mode values
	// (see the header).
	raTurboModeClassic = "0"
	raTurboModeHold    = "3"
	// raTurboAutoconfigMax bounds the autoconfig scan.
	raTurboAutoconfigMax  = 3000
	raTurboAutoconfigSize = 16 << 10
)

// raTurboPeriod is the frames per press of a speed.
func raTurboPeriod(speed string) int {
	switch speed {
	case gameTurboFast:
		return 4
	case gameTurboSlow:
		return 10
	}
	return 6
}

// raTurboNewKeys reports whether a retroarch.cfg was last saved by RetroArch
// 1.21 or newer. Pure.
func raTurboNewKeys(cfg string) bool {
	for _, key := range []string{"input_turbo_bind", "input_turbo_button", "input_turbo_enable"} {
		if _, ok := raConfigGet(cfg, key); ok {
			return true
		}
	}
	return false
}

// raTurboOldKeys reports whether a retroarch.cfg was saved by an older build.
// Pure.
func raTurboOldKeys(cfg string) bool {
	for _, key := range []string{"input_turbo_default_button", "input_duty_cycle", "input_turbo_mode", "input_turbo_period"} {
		if _, ok := raConfigGet(cfg, key); ok {
			return true
		}
	}
	return false
}

// raTurboValues are the keys a launch sets for turbo on button ("a" or "b"),
// whether they are the 1.21+ ones, and, when nothing can be set, why.
func raTurboValues(cfg, cfgPath, button, speed string) ([]raScopeValue, bool, string) {
	period := raTurboPeriod(speed)
	timing := []raScopeValue{
		{Key: "input_turbo_period", Value: strconv.Itoa(period)},
		{Key: "input_turbo_duty_cycle", Value: strconv.Itoa(period / 2)},
	}
	pad := raRemapButtonID(button)
	if pad < 0 {
		return nil, false, "unknown button"
	}
	physical := -1
	if !raTurboNewKeys(cfg) {
		physical = raTurboPhysicalButton(cfg, cfgPath, button)
	}
	switch {
	case raTurboNewKeys(cfg) || (!raTurboOldKeys(cfg) && physical < 0):
		// 1.21+, or a config no RetroArch has saved yet: the newer keys,
		// which an older build ignores.
		id := strconv.Itoa(pad)
		return append([]raScopeValue{
			{Key: "input_turbo_enable", Value: "true"},
			{Key: "input_turbo_mode", Value: raTurboModeHold},
			{Key: "input_turbo_bind", Value: id},
			{Key: "input_turbo_button", Value: id},
		}, timing...), true, ""
	case physical >= 0:
		// Before 1.21 the duty cycle was saved as input_duty_cycle (the
		// overhaul renamed every turbo key to input_turbo_*); both are set,
		// an unknown key is ignored.
		return append([]raScopeValue{
			{Key: "input_turbo_mode", Value: raTurboModeClassic},
			{Key: "input_player1_turbo_btn", Value: strconv.Itoa(physical)},
			{Key: "input_duty_cycle", Value: strconv.Itoa(period / 2)},
		}, timing...), false, ""
	}
	return nil, false, "this RetroArch (1.20 or older) needs the pad's " + strings.ToUpper(button) + " button set in its controls"
}

// raTurboPads caches the autoconfig lookup per config and button.
var raTurboPads struct {
	mu sync.Mutex
	m  map[string]int
}

// raTurboPhysicalButton is the handheld pad's button number for a RetroPad
// button ("a", "b"), -1 when JukaHub cannot tell.
func raTurboPhysicalButton(cfg, cfgPath, button string) int {
	if v, ok := raConfigGet(cfg, "input_player1_"+button+"_btn"); ok {
		if n, ok := raTurboButtonNumber(v); ok {
			return n
		}
	}
	key := cfgPath + "\x00" + button
	raTurboPads.mu.Lock()
	if n, ok := raTurboPads.m[key]; ok {
		raTurboPads.mu.Unlock()
		return n
	}
	raTurboPads.mu.Unlock()
	n := raTurboAutoconfigButton(cfg, cfgPath, button)
	raTurboPads.mu.Lock()
	if raTurboPads.m == nil {
		raTurboPads.m = map[string]int{}
	}
	raTurboPads.m[key] = n
	raTurboPads.mu.Unlock()
	return n
}

// raTurboButtonNumber reads a joypad button bind: a plain number ("1"); "nul",
// a hat ("h0up") or an axis is not a button. Pure.
func raTurboButtonNumber(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > 255 {
		return -1, false
	}
	return n, true
}

// raTurboAutoconfigButton looks for the handheld's own pad profile in
// RetroArch's autoconfig folders (<dir>/<driver>/<pad>.cfg) and reads the
// button from it.
func raTurboAutoconfigButton(cfg, cfgPath, button string) int {
	var roots []string
	if v, ok := raConfigGet(cfg, "joypad_autoconfig_dir"); ok {
		if dir := videoFilterExpandDir(v, cfgPath); dir != "" {
			roots = append(roots, dir)
		}
	}
	roots = append(roots,
		filepath.Join(raInstallDir(cfgPath), "autoconfig"),
		filepath.Join(filepath.Dir(cfgPath), "autoconfig"))
	seen := map[string]bool{}
	count := 0
	var walk func(dir string, depth int) int
	walk = func(dir string, depth int) int {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return -1
		}
		for _, e := range entries {
			count++
			if count > raTurboAutoconfigMax {
				return -1
			}
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if depth < 2 {
					if n := walk(full, depth+1); n >= 0 {
						return n
					}
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(e.Name()), ".cfg") {
				continue
			}
			if info, err := e.Info(); err != nil || info.Size() > raTurboAutoconfigSize {
				continue
			}
			raw, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			text := string(raw)
			device, _ := raConfigGet(text, "input_device")
			if !strings.Contains(strings.ToLower(device), "trimui") {
				continue
			}
			if v, ok := raConfigGet(text, "input_"+button+"_btn"); ok {
				if n, ok := raTurboButtonNumber(v); ok {
					return n
				}
			}
		}
		return -1
	}
	for _, root := range roots {
		key := strings.ToLower(filepath.Clean(root))
		if root == "" || seen[key] {
			continue
		}
		seen[key] = true
		if n := walk(root, 0); n >= 0 {
			return n
		}
	}
	return -1
}
