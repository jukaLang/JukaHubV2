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

var ppssppTrimuiButtons = [...]struct{ key, mapping string }{
	{"Up", "10-19"}, {"Down", "10-20"}, {"Left", "10-21"}, {"Right", "10-22"},
	{"Circle", "10-190"}, {"Cross", "10-189"}, {"Square", "10-191"}, {"Triangle", "10-188"},
	{"Start", "10-197"}, {"Select", "10-196"}, {"L", "10-193"}, {"R", "10-192"},
	{"Pause", "10-109"},
	{"An.Up", "10-4003"}, {"An.Down", "10-4002"}, {"An.Left", "10-4001"}, {"An.Right", "10-4000"},
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

func ppssppPrepareTrimuiHome(sys, platform string) {
	if platform != ppssppOwnPlatform || sys == "" {
		return
	}
	graphics := filepath.Join(sys, "ppsspp.ini")
	if !regularFile(graphics) {
		if err := os.WriteFile(graphics, []byte("[Graphics]\nGraphicsBackend = 0 (OPENGL)\nFullScreen = True\n"), 0o644); err != nil {
			logWarn("ppsspp", "could not set the TrimUI graphics backend: %v", err)
		}
	} else if raw, err := os.ReadFile(graphics); err == nil {
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		backend, _ := ppssppINIValue(lines, "Graphics", "GraphicsBackend")
		fullScreen, _ := ppssppINIValue(lines, "Graphics", "FullScreen")
		if !strings.HasPrefix(strings.TrimSpace(backend), "0 ") || !strings.EqualFold(fullScreen, "True") {
			if err := writePPSSPPControls(graphics, func(lines []string) []string {
				if !strings.HasPrefix(strings.TrimSpace(backend), "0 ") {
					lines = ppssppSetSectionValue(lines, "Graphics", "GraphicsBackend", "0 (OPENGL)")
				}
				if !strings.EqualFold(fullScreen, "True") {
					lines = ppssppSetSectionValue(lines, "Graphics", "FullScreen", "True")
				}
				return lines
			}); err != nil {
				logWarn("ppsspp", "could not repair the TrimUI graphics settings: %v", err)
			}
		}
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
		if value, _ := ppssppINIValue(lines, "ControlMapping", button.key); !strings.Contains(value, "10-") {
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
