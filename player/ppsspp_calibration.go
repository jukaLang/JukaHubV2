package main

// ppsspp_calibration.go — the backend of the left-stick calibration wizard.
//
// PPSSPP stores its controller mapping in PSP/SYSTEM/ppsspp.ini. On a handheld
// whose left stick reports about half of its travel, the usual cause is an
// AnalogDeadzone that is far too large: the emulator ignores everything below
// it, so a "full" push only ever reaches the middle of the axis. The wizard
// measures the real travel and writes a deadzone that sits just above the
// stick's own idle noise.
//
// The file is hand-edited by users, so nothing here rewrites more than the one
// key it is asked to: comments, ordering, other sections, line endings and
// permissions all survive, and the first write leaves a single .bak behind.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ppssppConfigRelPaths are the layouts PPSSPP installs use on an SD card. The
// order is the preference order: a standalone memstick folder first, then the
// PPSSPP app folder, then a copy unpacked into Apps.
var ppssppConfigRelPaths = []string{
	filepath.Join("PSP", "SYSTEM", "ppsspp.ini"),
	filepath.Join("ppsspp", "PSP", "SYSTEM", "ppsspp.ini"),
	filepath.Join("Apps", "PPSSPP", "PSP", "SYSTEM", "ppsspp.ini"),
}

// ppssppConfigCandidates lists every possible config path for the given roots,
// in preference order and without duplicates. FAT32 is case-insensitive, so the
// dedupe key is lowercased; the returned paths keep their original spelling.
func ppssppConfigCandidates(sdRoots []string) []string {
	seen := make(map[string]bool, len(sdRoots)*len(ppssppConfigRelPaths))
	out := make([]string, 0, len(sdRoots)*len(ppssppConfigRelPaths))
	for _, root := range sdRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, rel := range ppssppConfigRelPaths {
			path := filepath.Join(root, rel)
			key := strings.ToLower(filepath.Clean(path))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, path)
		}
	}
	return out
}

// findPPSSPPConfig returns the first candidate that exists as a file, or "".
// Directories and symlinks to directories are not configs.
func findPPSSPPConfig(sdRoots []string) string {
	for _, path := range ppssppConfigCandidates(sdRoots) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		return path
	}
	return ""
}

// readINILines reads an INI file as lines without their line endings, so the
// helpers below can compare keys without worrying about CRLF.
func readINILines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	return strings.Split(text, "\n"), nil
}

// iniSectionKey splits an INI line into its section name, key and value.
// section is "" outside a section, key is "" for a comment, blank line or
// section header.
func iniSectionKey(line string) (section, key, value string, isHeader bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return strings.TrimSpace(trimmed[1 : len(trimmed)-1]), "", "", true
	}
	if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
		return "", "", "", false
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 {
		return "", "", "", false
	}
	return "", strings.TrimSpace(trimmed[:eq]), strings.TrimSpace(trimmed[eq+1:]), false
}

// ppssppINIValue reads a key from the [ControlMapping] section. It is scoped to
// that section on purpose: the same key name exists in more than one PPSSPP
// section ([Graphics] has its own ideas about many words), and writing the
// wrong one would silently do nothing.
func ppssppINIValue(lines []string, key string) (string, bool) {
	section := ""
	for _, line := range lines {
		name, k, v, isHeader := iniSectionKey(line)
		if isHeader {
			section = name
			continue
		}
		if section != "" && strings.EqualFold(section, "ControlMapping") && k != "" && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// setPPSSPPINIValue replaces key in [ControlMapping], or inserts it there.
//
// The rules that keep a hand-edited file intact:
//   - only the value of the matching key changes, its comment stays on the line
//     if the user put it there as an inline comment (format is rebuilt as
//     "Key = value", because PPSSPP writes it that way);
//   - a key in another section is never matched or moved;
//   - when the key is missing it is appended after the section's last entry, so
//     the section keeps its order;
//   - when the section is missing it is appended at the end of the file.
func setPPSSPPINIValue(lines []string, key, value string) []string {
	out := append([]string(nil), lines...)
	section := ""
	headerIdx := -1
	insertAt := -1
	for i, line := range out {
		name, k, _, isHeader := iniSectionKey(line)
		if isHeader {
			section = name
			if strings.EqualFold(name, "ControlMapping") {
				headerIdx = i
			}
			continue
		}
		if section == "" || !strings.EqualFold(section, "ControlMapping") {
			continue
		}
		if k != "" {
			insertAt = i + 1
			if strings.EqualFold(k, key) {
				// Rebuild only the value, keeping the key's own spelling.
				raw := out[i]
				eq := strings.IndexByte(raw, '=')
				out[i] = raw[:eq] + "= " + value
				return out
			}
		}
	}

	entry := key + " = " + value
	switch {
	case headerIdx >= 0:
		if insertAt < 0 {
			insertAt = headerIdx + 1
		}
		out = append(out[:insertAt], append([]string{entry}, out[insertAt:]...)...)
	case len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "":
		out = append(out[:len(out)-1], "[ControlMapping]", entry, "")
	default:
		out = append(out, "[ControlMapping]", entry)
	}
	return out
}

// deadzoneFromSamples computes the PPSSPP AnalogDeadzone from what the wizard
// saw. samples are the peak per-axis magnitudes while the stick was rolled
// around; idlePeak is the largest magnitude seen with the stick released.
//
// The result is just above the idle noise, but never so high that a real push
// is swallowed: it stays under 60% of the smallest genuine deflection. Bad
// input (NaN, negatives, nothing usable) falls back to the 0.02 floor.
func deadzoneFromSamples(samples []float64, idlePeak float64) float64 {
	const (
		floor = 0.02
		cap   = 0.25
		noise = 0.03
	)
	if math.IsNaN(idlePeak) || idlePeak < 0 {
		idlePeak = 0
	}
	minSample := math.Inf(1)
	valid := 0
	for _, s := range samples {
		if math.IsNaN(s) || math.IsInf(s, 0) || s < 0 {
			continue
		}
		valid++
		if s < minSample {
			minSample = s
		}
	}
	if valid == 0 {
		return floor
	}

	dz := idlePeak + noise
	if upper := 0.6 * minSample; dz > upper {
		dz = upper
	}
	if dz < floor {
		dz = floor
	}
	if dz > cap {
		dz = cap
	}
	// Round down so the written value never exceeds what was justified.
	return math.Floor(dz*1000) / 1000
}

// writePPSSPPDeadzone stores the calibrated value in [ControlMapping].
//
// The original is copied to path+".bak" once — a second calibration must not
// clobber the factory file the user may want back — and the rewrite keeps the
// file's line endings and permissions.
// hasPPSSPPInstall reports whether any of these roots holds a PPSSPP ini, which
// is what decides if the calibration entry is offered. The answer is cached for
// a few seconds because the Start menu asks on every frame it renders, and a
// stat of six paths on an SD card is not a per-frame cost worth paying.
func hasPPSSPPInstall(roots []string) bool { return cachedPPSSPPConfig(roots) != "" }

var ppssppInstallCache struct {
	mu   sync.Mutex
	key  string
	path string
	at   time.Time
}

// resetPPSSPPInstallCache forgets the cached discovery, which the wizard and
// tests call after the ini appears or moves.
func resetPPSSPPInstallCache() {
	ppssppInstallCache.mu.Lock()
	ppssppInstallCache.key = ""
	ppssppInstallCache.path = ""
	ppssppInstallCache.at = time.Time{}
	ppssppInstallCache.mu.Unlock()
}

func cachedPPSSPPConfig(roots []string) string {
	key := strings.Join(roots, "\x00")
	ppssppInstallCache.mu.Lock()
	defer ppssppInstallCache.mu.Unlock()
	if ppssppInstallCache.key == key && !ppssppInstallCache.at.IsZero() &&
		time.Since(ppssppInstallCache.at) < 30*time.Second {
		return ppssppInstallCache.path
	}
	ppssppInstallCache.key = key
	ppssppInstallCache.path = findPPSSPPConfig(roots)
	ppssppInstallCache.at = time.Now()
	return ppssppInstallCache.path
}

func writePPSSPPDeadzone(path string, deadzone float64) error {
	return writePPSSPPControls(path, func(lines []string) []string {
		return setPPSSPPINIValue(lines, "AnalogDeadzone", fmt.Sprintf("%.3f", deadzone))
	})
}

// writePPSSPPStickFix is what the calibration wizard calls: it lowers the
// deadzone and turns off PPSSPP's AnalogLimiter, which clamps the stick to 60%
// of its range and is the reason a full push reads as half input. Both keys live
// in [ControlMapping]; everything else in the file is left alone and the first
// write keeps a .bak next to it.
func writePPSSPPStickFix(path string, deadzone, limiter float64) error {
	return writePPSSPPControls(path, func(lines []string) []string {
		lines = setPPSSPPINIValue(lines, "AnalogDeadzone", fmt.Sprintf("%.3f", deadzone))
		return setPPSSPPINIValue(lines, "AnalogLimiter", fmt.Sprintf("%.3f", limiter))
	})
}

// writePPSSPPControls rewrites the ini through edit, preserving the original
// line endings, mode, trailing newline and a one-time backup.
func writePPSSPPControls(path string, edit func([]string) []string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	eol := "\n"
	if strings.Contains(string(raw), "\r\n") {
		eol = "\r\n"
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	trailingNewline := strings.HasSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}

	lines = edit(lines)

	body := strings.Join(lines, eol)
	if trailingNewline {
		body += eol
	}

	resetPPSSPPInstallCache()
	if _, statErr := os.Stat(path + ".bak"); os.IsNotExist(statErr) {
		if err := os.WriteFile(path+".bak", raw, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(body), info.Mode().Perm())
}

// ppssppDeadzoneSummary is the one-line status the panel shows for a config.
func ppssppDeadzoneSummary(path string) string {
	if strings.TrimSpace(path) == "" {
		return "not installed"
	}
	lines, err := readINILines(path)
	if err != nil {
		return "not installed"
	}
	entries := 0
	section := ""
	for _, line := range lines {
		name, k, _, isHeader := iniSectionKey(line)
		if isHeader {
			section = name
			continue
		}
		if strings.EqualFold(section, "ControlMapping") && k != "" {
			entries++
		}
	}
	value, ok := ppssppINIValue(lines, "AnalogDeadzone")
	if !ok || strings.TrimSpace(value) == "" {
		return "deadzone not set · " + strconv.Itoa(entries) + " entries"
	}
	formatted := strings.TrimSpace(value)
	if f, err := strconv.ParseFloat(formatted, 64); err == nil {
		formatted = fmt.Sprintf("%.3f", f)
	}
	return "deadzone " + formatted + " · " + strconv.Itoa(entries) + " entries"
}
