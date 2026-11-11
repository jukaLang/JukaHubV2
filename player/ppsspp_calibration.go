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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ppssppConfigRelPaths are the layouts PPSSPP installs use on an SD card. The
// newest file wins (newestPPSSPPConfig); on a tie this order decides: a
// standalone memstick folder first, then the PPSSPP app folder, then a copy
// unpacked into Apps.
var ppssppConfigRelPaths = []string{
	filepath.Join("PSP", "SYSTEM", "ppsspp.ini"),
	filepath.Join("ppsspp", "PSP", "SYSTEM", "ppsspp.ini"),
	filepath.Join("Apps", "PPSSPP", "PSP", "SYSTEM", "ppsspp.ini"),
}

// ppssppConfigCandidates lists every fixed-layout config path for the given
// roots, in tie-break order and without duplicates. FAT32 is case-insensitive,
// so the dedupe key is lowercased; the returned paths keep their spelling.
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

// ppssppMemstick is the memstick (the PSP folder holding SYSTEM, SAVEDATA and
// PPSSPP_STATE) of a PPSSPP kept in a TrimUI emulator folder, with the folder
// that build runs with as HOME.
type ppssppMemstick struct {
	Home string
	Dir  string
}

// ppssppHomeMemstickRels are where the memstick sits relative to that HOME.
// CrossMix unpacks each build into Emus/PSP/PPSSPP_<version>/ and its
// launchers point HOME there, so the desktop-Linux layout (.config/ppsspp/PSP)
// is inside that folder; a build that runs portable keeps a bare PSP folder.
var ppssppHomeMemstickRels = []string{
	filepath.Join(".config", "ppsspp", "PSP"),
	"PSP",
}

// ppssppEmuFolderName reports whether an Emus/ folder belongs to the PSP:
// CrossMix's PSP and PSPMINIS, the stock firmware's PPSSPP.
func ppssppEmuFolderName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "psp") || strings.Contains(lower, "ppsspp")
}

// ppssppEmuFolders lists the PSP emulator folders on the cards in sdRoots, in
// directory order.
func ppssppEmuFolders(sdRoots []string) []string {
	var out []string
	for _, root := range sdRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		emus := filepath.Join(root, "Emus")
		entries, err := os.ReadDir(emus)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && !skipScanEntryName(entry.Name()) && ppssppEmuFolderName(entry.Name()) {
				out = append(out, filepath.Join(emus, entry.Name()))
			}
		}
	}
	return out
}

// ppssppEmuBuilds lists the PPSSPP builds unpacked inside the cards' PSP
// emulator folders (CrossMix: Emus/PSP/PPSSPP_1.17.1/), newest version first
// within each folder. A card with any of them has PPSSPP installed, even before
// the first game has made it write an ini.
func ppssppEmuBuilds(sdRoots []string) []string {
	var out []string
	for _, folder := range ppssppEmuFolders(sdRoots) {
		entries, err := os.ReadDir(folder)
		if err != nil {
			continue
		}
		var names []string
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(strings.ToLower(entry.Name()), "ppsspp") {
				names = append(names, entry.Name())
			}
		}
		sort.SliceStable(names, func(i, j int) bool { return compareCoreVersions(names[i], names[j]) > 0 })
		for _, name := range names {
			out = append(out, filepath.Join(folder, name))
		}
	}
	return out
}

// ppssppEmuHomes is every folder a PPSSPP on a TrimUI card can run with as its
// HOME: each build folder, each PSP emulator folder itself, and the card root —
// the first CrossMix images pointed HOME at the card, so their ini is
// <SD>/.config/ppsspp/PSP/SYSTEM/ppsspp.ini. Only a root with an Emus folder is
// such a card, so a desktop's search roots add nothing here.
func ppssppEmuHomes(sdRoots []string) []string {
	homes := ppssppEmuBuilds(sdRoots)
	homes = append(homes, ppssppEmuFolders(sdRoots)...)
	for _, root := range sdRoots {
		if root = strings.TrimSpace(root); root != "" && dirExists(filepath.Join(root, "Emus")) {
			homes = append(homes, root)
		}
	}
	return homes
}

// ppssppEmuMemsticks lists every memstick a PPSSPP on the cards may use, home
// by home in ppssppEmuHomes order.
func ppssppEmuMemsticks(sdRoots []string) []ppssppMemstick {
	var out []ppssppMemstick
	for _, home := range ppssppEmuHomes(sdRoots) {
		for _, rel := range ppssppHomeMemstickRels {
			out = append(out, ppssppMemstick{Home: home, Dir: filepath.Join(home, rel)})
		}
	}
	return out
}

// ppssppMemstickIni is the ini inside a memstick.
func ppssppMemstickIni(memstick string) string {
	return filepath.Join(memstick, "SYSTEM", "ppsspp.ini")
}

// findPPSSPPConfig returns the newest ini among the fixed layouts and the
// memsticks of the PPSSPPs kept in emulator folders, or "". Directories and
// symlinks to directories are not configs.
//
// While JukaHub's own PPSSPP plays PSP games (ppsspp_managed.go), its ini is
// the one that counts: that build runs with its own HOME in JukaHub's data
// folder, outside every root here, and never reads the firmware's ini.
func findPPSSPPConfig(sdRoots []string) string {
	if ini := ppssppActiveIni(); ini != "" {
		return ini
	}
	candidates := ppssppConfigCandidates(sdRoots)
	for _, memstick := range ppssppEmuMemsticks(sdRoots) {
		candidates = append(candidates, ppssppMemstickIni(memstick.Dir))
	}
	return newestPPSSPPConfig(candidates)
}

// newestPPSSPPConfig picks the ini of the PPSSPP that is actually played when
// a card holds several (CrossMix keeps two or three builds side by side, and
// another layout's file can linger from an older setup): PPSSPP writes its ini
// when it exits and CrossMix's launchers edit it just before a start, so the
// newest file is the live one — the file the stick fix has to land in. A tie
// keeps the earlier candidate: the fixed layouts, then the newer version.
func newestPPSSPPConfig(candidates []string) string {
	best := ""
	var bestTime time.Time
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = path, info.ModTime()
		}
	}
	return best
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

// hasPPSSPPInstall reports whether any of these roots holds a PPSSPP ini, which
// is what decides if the calibration entry is offered. The answer is cached for
// a few seconds because the Start menu asks on every frame it renders, and a
// scan of the card's PSP folders is not a per-frame cost worth paying.
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
