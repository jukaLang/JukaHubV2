package main

// PPSSPP's upstream defaults are keyboard oriented. The TrimUI package ships
// a controller map, but JukaHub's own build does not, and an older shared HOME
// may already contain keyboard-only controls. Prepare both without discarding
// the user's other settings.

import (
	"os"
	"path/filepath"
	"strings"
)

// ppssppTrimuiButtons is the pad half of controls.ini for the TrimUI pad as
// PPSSPP's SDL front end reports it (device 10 = the first game controller).
// Pause is 10-4: SDL's Guide button — the handheld's Menu key — arrives as
// NKCODE_BACK (4), so Menu opens PPSSPP's pause menu, whose Exit comes back to
// JukaHub (--pause-menu-exit). It was 10-109 (KEYCODE_BUTTON_SELECT), a code
// the SDL front end never sends, so nothing on the pad paused the game or left
// it (Juka, 2026-11-15: "the screen went black and I had to restart").
var ppssppTrimuiButtons = [...]struct{ key, mapping string }{
	{"Up", "10-19"}, {"Down", "10-20"}, {"Left", "10-21"}, {"Right", "10-22"},
	{"Circle", "10-190"}, {"Cross", "10-189"}, {"Square", "10-191"}, {"Triangle", "10-188"},
	{"Start", "10-197"}, {"Select", "10-196"}, {"L", "10-193"}, {"R", "10-192"},
	{"Pause", "10-4"},
	{"An.Up", "10-4003"}, {"An.Down", "10-4002"}, {"An.Left", "10-4001"}, {"An.Right", "10-4000"},
}

// ppssppTrimuiPadMapping is the SDL game-controller mapping of the TrimUI's
// own pad ("TRIMUI Player1", an Xbox 360 id), the same one main.go adds for
// JukaHub. The launcher hands it to PPSSPP in SDL_GAMECONTROLLERCONFIG, so the
// pad is a game controller to PPSSPP even when the package's controller
// database lacks it — without one, PPSSPP's SDL front end ignores the pad and
// only the keyboard mapping is left.
const ppssppTrimuiPadMapping = "030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8,dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1,rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,"

// ppssppHasMapping reports whether a controls.ini value (mappings separated
// by commas) holds mapping itself.
func ppssppHasMapping(value, mapping string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == mapping {
			return true
		}
	}
	return false
}

// ppssppNeedsPadMapping reports whether a button's value lacks its TrimUI pad
// mapping: no pad code at all, or — for Pause — the dead 10-109 an older
// JukaHub wrote without the Menu key beside it. A player who maps Pause to
// another pad button in PPSSPP keeps that.
func ppssppNeedsPadMapping(key, value, mapping string) bool {
	if key == "Pause" && ppssppHasMapping(value, "10-109") {
		return !ppssppHasMapping(value, mapping)
	}
	return !strings.Contains(value, "10-")
}

// ppssppINIValue reads one key from a named section. A false result means the
// key was absent, as distinct from an intentionally empty value.
func ppssppINIValue(lines []string, sectionName, keyName string) (string, bool) {
	section := ""
	for _, line := range lines {
		name, key, value, header := iniSectionKey(line)
		if header {
			section = name
			continue
		}
		if strings.EqualFold(section, sectionName) && strings.EqualFold(key, keyName) {
			return value, true
		}
	}
	return "", false
}

func ppssppSetSectionValue(lines []string, sectionName, keyName, value string) []string {
	out := append([]string(nil), lines...)
	section, headerAt, insertAt := "", -1, -1
	for i, line := range out {
		name, key, _, header := iniSectionKey(line)
		if header {
			section = name
			if strings.EqualFold(name, sectionName) {
				headerAt, insertAt = i, i+1
			}
			continue
		}
		if !strings.EqualFold(section, sectionName) || key == "" {
			continue
		}
		insertAt = i + 1
		if strings.EqualFold(key, keyName) {
			eq := strings.IndexByte(line, '=')
			out[i] = line[:eq] + "= " + value
			return out
		}
	}
	entry := keyName + " = " + value
	if headerAt < 0 {
		return append(out, "["+sectionName+"]", entry)
	}
	out = append(out, "")
	copy(out[insertAt+1:], out[insertAt:])
	out[insertAt] = entry
	return out
}

// ppssppTrimuiNoBackend is the graphics back end PPSSPP must never pick on the
// A133P. Vulkan needs a window the firmware's SDL2 (its mali driver) cannot
// describe, so PPSSPP ended at once with "Vulkan subsystem 0 not supported"
// every time its own crash memory sent it there (2026-11-25, JH-CA8ZMU). Listed
// in DisabledGraphicsBackends it counts as failed for good, and with OpenGL in
// the crash memory as well PPSSPP starts its default — OpenGL — rather than it.
const ppssppTrimuiNoBackend = "VULKAN"

// ppssppFailedBackendsFile is PPSSPP's crash memory for graphics: the back end
// it is starting is written there first and the file is deleted once its
// graphics came up, so a run that died on the way leaves it, and the next
// start tries another back end (Vulkan on a TrimUI, which cannot work). The
// handheld's builds keep it beside ppsspp.ini; older ones in SYSTEM/CACHE.
const ppssppFailedBackendsFile = "FailedGraphicsBackends.txt"

// ppssppBackendListHas reports whether a comma list of graphics back ends
// (PPSSPP's FailedGraphicsBackends / DisabledGraphicsBackends) names one.
func ppssppBackendListHas(list, name string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(part), name) {
			return true
		}
	}
	return false
}

// ppssppPrepareTrimuiHome readies the shared settings (sys: the memstick's
// SYSTEM folder) of a tg5040 build before every game: OpenGL in full screen,
// Vulkan off, no crash memory of a failed back end, and the pad mapped.
func ppssppPrepareTrimuiHome(sys, platform string) {
	if platform != ppssppOwnPlatform || sys == "" {
		return
	}
	graphics := filepath.Join(sys, "ppsspp.ini")
	if !regularFile(graphics) {
		if err := os.WriteFile(graphics, []byte("[Graphics]\nGraphicsBackend = 0 (OPENGL)\nFullScreen = True\nDisabledGraphicsBackends = "+ppssppTrimuiNoBackend+"\n"), 0o644); err != nil {
			logWarn("ppsspp", "could not set the TrimUI graphics backend: %v", err)
		}
	} else if raw, err := os.ReadFile(graphics); err == nil {
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		backend, _ := ppssppINIValue(lines, "Graphics", "GraphicsBackend")
		fullScreen, _ := ppssppINIValue(lines, "Graphics", "FullScreen")
		failed, _ := ppssppINIValue(lines, "Graphics", "FailedGraphicsBackends")
		disabled, _ := ppssppINIValue(lines, "Graphics", "DisabledGraphicsBackends")
		fixBackend := !strings.HasPrefix(strings.TrimSpace(backend), "0 ")
		fixFull := !strings.EqualFold(fullScreen, "True")
		// The crash memory as an ini value (PPSSPP saves it there too). A
		// player's own IGNORE, which turns the fallback off, stays.
		clearFailed := failed != "" && !strings.EqualFold(failed, "IGNORE")
		addDisabled := !ppssppBackendListHas(disabled, ppssppTrimuiNoBackend)
		if fixBackend || fixFull || clearFailed || addDisabled {
			if err := writePPSSPPControls(graphics, func(lines []string) []string {
				if fixBackend {
					lines = ppssppSetSectionValue(lines, "Graphics", "GraphicsBackend", "0 (OPENGL)")
				}
				if fixFull {
					lines = ppssppSetSectionValue(lines, "Graphics", "FullScreen", "True")
				}
				if clearFailed {
					lines = ppssppSetSectionValue(lines, "Graphics", "FailedGraphicsBackends", "")
				}
				if addDisabled {
					list := ppssppTrimuiNoBackend
					if cur, _ := ppssppINIValue(lines, "Graphics", "DisabledGraphicsBackends"); cur != "" {
						list = cur + "," + list
					}
					lines = ppssppSetSectionValue(lines, "Graphics", "DisabledGraphicsBackends", list)
				}
				return lines
			}); err != nil {
				logWarn("ppsspp", "could not repair the TrimUI graphics settings: %v", err)
			} else if clearFailed {
				logInfo("ppsspp", "cleared PPSSPP's memory of a failed graphics back end (%s) in ppsspp.ini: a run that died left it", failed)
			}
		}
	}
	for _, file := range []string{filepath.Join(sys, ppssppFailedBackendsFile), filepath.Join(sys, "CACHE", ppssppFailedBackendsFile)} {
		if !regularFile(file) {
			continue
		}
		was := "empty"
		if data, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(data)) != "" {
			was = strings.TrimSpace(string(data))
			if len(was) > 80 {
				was = was[:80]
			}
		}
		if err := os.Remove(file); err != nil {
			logWarn("ppsspp", "could not clear PPSSPP's memory of a failed graphics back end: %v", err)
			continue
		}
		logInfo("ppsspp", "cleared PPSSPP's memory of a failed graphics back end (%s): a run that died left %s", was, file)
	}

	controls := filepath.Join(sys, "controls.ini")
	if !regularFile(controls) {
		lines := []string{"[ControlMapping]"}
		for _, button := range ppssppTrimuiButtons {
			lines = append(lines, button.key+" = "+button.mapping)
		}
		if err := os.WriteFile(controls, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			logWarn("ppsspp", "could not set the TrimUI button mapping: %v", err)
		}
		return
	}
	raw, err := os.ReadFile(controls)
	if err != nil {
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	// Keep every existing mapping. Fill any PSP button that has no TrimUI pad
	// code, including a keyboard-only or partly mapped older HOME.
	var missing []struct{ key, mapping string }
	for _, button := range ppssppTrimuiButtons {
		if value, _ := ppssppINIValue(lines, "ControlMapping", button.key); ppssppNeedsPadMapping(button.key, value, button.mapping) {
			missing = append(missing, button)
		}
	}
	if len(missing) == 0 {
		return
	}
	if err := writePPSSPPControls(controls, func(lines []string) []string {
		for _, button := range missing {
			value, _ := ppssppINIValue(lines, "ControlMapping", button.key)
			if value != "" {
				value += ","
			}
			lines = ppssppSetSectionValue(lines, "ControlMapping", button.key, value+button.mapping)
		}
		return lines
	}); err != nil {
		logWarn("ppsspp", "could not repair the TrimUI button mapping: %v", err)
	}
}
