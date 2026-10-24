package main

// disctools.go — the two small files CD-based systems need and a plain ROM
// folder never gains by itself: a one-track .cue sheet next to a ripped .bin,
// and an .m3u playlist that turns "Game (Disc 1).chd" + "Game (Disc 2).chd"
// into one entry RetroArch can switch discs from.
//
// Both are additive. An existing .cue or .m3u is never rewritten, so a
// hand-edited sheet (audio tracks, a different sector mode) survives a run.
// Nothing here touches a cartridge folder: a stray .bin in Roms/GBA is a game
// file, not a disc image. Scanning and writing are SDL-free so they can be
// tested headless.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// discSystemFolders maps a lower-cased Roms/<folder> name to the cue track
// mode its images use. PlayStation rips are MODE2/2352; the Sega and NEC CD
// systems are MODE1/2352. A folder that is not listed is left untouched.
var discSystemFolders = map[string]string{
	"psx":                 "MODE2/2352",
	"ps1":                 "MODE2/2352",
	"ps":                  "MODE2/2352",
	"playstation":         "MODE2/2352",
	"sony - playstation":  "MODE2/2352",
	"sat":                 "MODE1/2352",
	"saturn":              "MODE1/2352",
	"sega - saturn":       "MODE1/2352",
	"segacd":              "MODE1/2352",
	"sega cd":             "MODE1/2352",
	"sega - sega cd":      "MODE1/2352",
	"pcecd":               "MODE1/2352",
	"pc engine cd":        "MODE1/2352",
	"nec - pc engine cd":  "MODE1/2352",
	"turbografx cd":       "MODE1/2352",
	"nec - turbografx cd": "MODE1/2352",
}

// discSuffixPatterns match the disc marker a multi-disc set carries at the end
// of its name: "(Disc 1)", "(Disk 2)", "[CD 3]", "(Disc 1 of 2)" and the
// " - Disc 1" spelling some rippers use. The bracketed form is tried first so
// "Game (Disc 1)" keeps its name.
var discSuffixPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[\(\[]\s*(?:disc|disk|cd)\s*([0-9]{1,2})(?:\s*(?:of|/)\s*[0-9]{1,2})?\s*[\)\]]\s*$`),
	regexp.MustCompile(`(?i)(?:^|\s)-\s*(?:disc|disk|cd)\s*([0-9]{1,2})\s*$`),
}

// discTrackPattern matches per-track rips ("Game (Track 2).bin"). Those already
// belong to a multi-track cue sheet, so writing a one-track cue for them would
// be wrong.
var discTrackPattern = regexp.MustCompile(`(?i)[\(\[]\s*track\s*[0-9]{1,2}`)

// discHelperReport is what one run changed, for the toast and the log.
type discHelperReport struct {
	Systems   int
	Cues      int
	Playlists int
	Errors    []string
}

// Created is the number of files the run added.
func (r discHelperReport) Created() int { return r.Cues + r.Playlists }

// Summary is the one-line result shown after the action.
func (r discHelperReport) Summary() string {
	if r.Systems == 0 {
		return "No disc folders found in Roms/"
	}
	parts := make([]string, 0, 2)
	if r.Cues > 0 {
		parts = append(parts, itoa(r.Cues)+" cue sheet(s)")
	}
	if r.Playlists > 0 {
		parts = append(parts, itoa(r.Playlists)+" multi-disc playlist(s)")
	}
	if len(parts) == 0 {
		if len(r.Errors) > 0 {
			return "Could not write: " + r.Errors[0]
		}
		return "Every disc game already has its CUE and M3U"
	}
	line := "Added " + strings.Join(parts, " and ")
	if len(r.Errors) > 0 {
		line += " — " + itoa(len(r.Errors)) + " failed"
	}
	return line
}

// cueTrackMode returns the cue mode for a Roms/<folder> name.
func cueTrackMode(folder string) (string, bool) {
	mode, ok := discSystemFolders[strings.ToLower(strings.TrimSpace(folder))]
	return mode, ok
}

// cueSheetText is a single-track cue sheet for binary. The trailing newline is
// part of the format every cue reader expects.
func cueSheetText(binary, mode string) string {
	return fmt.Sprintf("FILE \"%s\" BINARY\n  TRACK 01 %s\n    INDEX 01 00:00:00\n", filepath.Base(binary), mode)
}

// splitDiscNumber separates "Final Fantasy VII (Disc 2)" into its set name and
// disc number. ok is false when the name carries no disc marker.
func splitDiscNumber(name string) (base string, disc int, ok bool) {
	for _, pattern := range discSuffixPatterns {
		loc := pattern.FindStringSubmatchIndex(name)
		if loc == nil {
			continue
		}
		n, err := strconv.Atoi(name[loc[2]:loc[3]])
		if err != nil || n <= 0 {
			continue
		}
		base = strings.TrimSpace(name[:loc[0]])
		base = strings.TrimSpace(strings.TrimRight(base, "-"))
		if base == "" {
			return "", 0, false
		}
		return base, n, true
	}
	return "", 0, false
}

// writeDiscHelpers adds the missing cue sheets and playlists in one system
// folder. It returns what it wrote rather than stopping at the first failure:
// one unwritable file must not hide the eleven that worked.
func writeDiscHelpers(dir, mode string) (cues, playlists int, errs []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, []string{err.Error()}
	}

	type discFile struct {
		name string
		size int64
	}
	var files []discFile
	have := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		files = append(files, discFile{name: name, size: info.Size()})
		have[strings.ToLower(name)] = true
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	// Pass one: a lone .bin with no sheet of its own gets a one-track cue.
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f.name), ".bin") || f.size == 0 {
			continue
		}
		if discTrackPattern.MatchString(f.name) {
			continue
		}
		base := strings.TrimSuffix(f.name, filepath.Ext(f.name))
		cue := base + ".cue"
		if have[strings.ToLower(cue)] {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, cue), []byte(cueSheetText(f.name, mode)), 0o644); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		have[strings.ToLower(cue)] = true
		cues++
	}

	// Pass two: two or more discs of one set get a playlist. The cue written
	// above is preferred as the entry, because that is what the emulator loads.
	type discEntry struct {
		disc int
		name string
	}
	sets := make(map[string][]discEntry)
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.name))
		switch ext {
		case ".bin", ".chd", ".iso", ".cue", ".img":
		default:
			continue
		}
		if discTrackPattern.MatchString(f.name) {
			continue
		}
		base, disc, ok := splitDiscNumber(strings.TrimSuffix(f.name, filepath.Ext(f.name)))
		if !ok {
			continue
		}
		key := strings.ToLower(base)
		sets[key] = append(sets[key], discEntry{disc: disc, name: f.name})
	}

	keys := make([]string, 0, len(sets))
	for key := range sets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		list := sets[key]
		byDisc := make(map[int]string, len(list))
		for _, entry := range list {
			// The cue is what the emulator loads, so it wins over the .bin it
			// describes. Pass one may have just written it, which is why the
			// name is resolved against the files that exist now.
			cue := strings.TrimSuffix(entry.name, filepath.Ext(entry.name)) + ".cue"
			if have[strings.ToLower(cue)] {
				byDisc[entry.disc] = cue
				continue
			}
			if _, seen := byDisc[entry.disc]; !seen {
				byDisc[entry.disc] = entry.name
			}
		}
		if len(byDisc) < 2 {
			continue
		}
		discs := make([]int, 0, len(byDisc))
		for disc := range byDisc {
			discs = append(discs, disc)
		}
		sort.Ints(discs)
		stem := strings.TrimSuffix(list[0].name, filepath.Ext(list[0].name))
		base, _, _ := splitDiscNumber(stem)
		playlist := base + ".m3u"
		if have[strings.ToLower(playlist)] {
			continue
		}
		var body strings.Builder
		for _, disc := range discs {
			body.WriteString(byDisc[disc])
			body.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, playlist), []byte(body.String()), 0o644); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		have[strings.ToLower(playlist)] = true
		playlists++
	}
	return cues, playlists, errs
}

// generateDiscHelpers walks each Roms root and repairs every disc system in it.
// Roots that do not exist are skipped quietly: on a fresh card most of them
// will not.
func generateDiscHelpers(roots []string) discHelperReport {
	var report discHelperReport
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			mode, ok := cueTrackMode(entry.Name())
			if !ok {
				continue
			}
			report.Systems++
			cues, playlists, errs := writeDiscHelpers(filepath.Join(root, entry.Name()), mode)
			report.Cues += cues
			report.Playlists += playlists
			report.Errors = append(report.Errors, errs...)
		}
	}
	return report
}
