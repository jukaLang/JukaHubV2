package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const biosDoctorEntryName = "BIOS Doctor"

type biosFileFingerprint struct {
	Name     string
	Root     string
	Path     string
	Relative string
	Size     int64
	SHA1     string
	SHA256   string
}

type biosKnownHash struct {
	Size int64
	SHA1 string
}

const (
	// biosHashMaxBytes is the largest file the doctor fingerprints. BIOS images
	// are kilobytes to a few megabytes; RetroArch's system folder also holds
	// hundreds of megabytes of core data, and hashing all of it read the whole
	// lot off the SD card before the page could draw.
	biosHashMaxBytes = 16 << 20
	// biosScanMaxDepth and biosScanMaxFiles bound the walk the same way.
	biosScanMaxDepth = 3
	biosScanMaxFiles = 600
	// biosNotHashed stands in for the fingerprint of a file over the size cap.
	biosNotHashed = "(large file, not hashed)"
)

type biosSystemStatus struct {
	Name     string
	Ready    bool
	Optional bool // no core strictly needs these files
	Checks   []biosDoctorCheck
}

type biosDoctorReport struct {
	Roots      []string
	TargetRoot string
	Systems    []biosSystemStatus
	Files      []biosFileFingerprint
	// Xbox is the Xbox checklist (emuxbox.go): xemu's on Windows, hakuX's on
	// a handheld the port is offered on, nil elsewhere.
	Xbox *emuXboxReport
}

type biosDoctorCheckKind string

const (
	biosCheckVerified   biosDoctorCheckKind = "verified"
	biosCheckMissing    biosDoctorCheckKind = "missing"
	biosCheckWrongHash  biosDoctorCheckKind = "wrong hash"
	biosCheckWrongPlace biosDoctorCheckKind = "wrong folder"
	biosCheckUnverified biosDoctorCheckKind = "unverified"
)

// biosDoctorCheck describes one set of interchangeable filenames. Found and
// Expected are full paths so the result gives a concrete next step.
type biosDoctorCheck struct {
	Kind     biosDoctorCheckKind
	Names    []string
	Found    string
	Expected string
	SHA1     string
	Note     string
}

// biosDoctorGroup is one console line of the doctor's page: each accepted
// group needs one of its names ("dc/dc_boot.bin" names a subfolder; the
// match is by file name). optional marks firmware no core strictly needs.
type biosDoctorGroup struct {
	name     string
	accepted [][]string
	optional bool
}

// biosFilenameGroups comes from the game diagnosis's firmware table
// (gamediag_bios.go), so the page and a failed launch name the same files.
// It used to be three hand-written lines (PS1, Dreamcast, Saturn) that called
// a BIOS "missing" that PCSX ReARMed and Flycast never need.
var biosFilenameGroups = biosDoctorGroupsFromTable()

// biosDoctorRoots lists the common JukaHub, handheld, and RetroArch BIOS
// locations. Files are never moved or modified by the doctor. The folder the
// device's RetroArch reads (system_directory in retroarch.cfg — CrossMix's
// /mnt/SDCARD/BIOS, or whatever the config says) comes first.
func biosDoctorRoots() []string {
	roots := []string{}
	// A "default" system_directory means each game's own folder ("." here),
	// which is no folder to scan.
	if dir, _, ok := gameDiagConfiguredSystemDir("", ""); ok && dir != "." {
		roots = append(roots, dir)
	}
	roots = append(roots, resolvePath("BIOS"))
	if !IsWindows() {
		roots = append(roots,
			filepath.Join(filepath.Dir(deviceRomsRoot), "BIOS"),
			filepath.Join(filepath.Dir(deviceRomsRoot), "RetroArch", ".retroarch", "system"),
			"/usr/trimui/roms/BIOS",
		)
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		roots = append(roots, filepath.Join(dir, "BIOS"))
	}
	seen := make(map[string]bool, len(roots))
	unique := roots[:0]
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || root == "" || seen[root] {
			continue
		}
		seen[root] = true
		unique = append(unique, root)
	}
	return unique
}

// biosDoctorRootsForScan leaves RetroArch's configured directory first. When
// RetroArch has no readable setting, the first existing common BIOS folder is
// the most useful target; the other roots remain searchable for misplaced files.
func biosDoctorRootsForScan() []string {
	roots := biosDoctorRoots()
	if dir, _, ok := gameDiagConfiguredSystemDir("", ""); ok && dir != "." {
		return roots
	}
	for i, root := range roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			if i > 0 {
				roots = append([]string{root}, append(roots[:i], roots[i+1:]...)...)
			}
			break
		}
	}
	return roots
}

func biosDoctorSamePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if IsWindows() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func biosDoctorCheckGroup(names []string, target string, found map[string][]biosFileFingerprint,
	catalog map[string]biosKnownHash) biosDoctorCheck {
	check := biosDoctorCheck{Kind: biosCheckMissing, Names: append([]string(nil), names...)}
	if len(names) == 0 {
		return check
	}
	check.Expected = filepath.Join(target, filepath.FromSlash(names[0]))
	bestScore := 6
	for _, name := range names {
		want := filepath.ToSlash(name)
		known, hasHash := catalog[strings.ToLower(want)]
		for _, file := range found[strings.ToLower(path.Base(want))] {
			rightFolder := biosDoctorSamePath(file.Path, filepath.Join(target, filepath.FromSlash(want)))
			verified := hasHash && file.Size == known.Size && strings.EqualFold(file.SHA1, known.SHA1)
			score := 5
			switch {
			case verified && rightFolder:
				score = 0
			case verified:
				score = 1
			case !hasHash && rightFolder:
				score = 2
			case hasHash && rightFolder:
				score = 3
			case !hasHash:
				score = 4
			}
			if score >= bestScore {
				continue
			}
			bestScore = score
			check.Expected = filepath.Join(target, filepath.FromSlash(want))
			check.Found = file.Path
			check.SHA1 = file.SHA1
			check.Note = ""
			switch score {
			case 0:
				check.Kind = biosCheckVerified
			case 1:
				check.Kind = biosCheckWrongPlace
			case 2, 4:
				check.Kind = biosCheckUnverified
				if file.SHA1 == "" {
					check.Note = "Could not read this file."
				} else {
					check.Note = "No known-good checksum for this filename."
				}
				if score == 4 {
					check.Note += " It is also in the wrong folder."
				}
			case 3, 5:
				check.Kind = biosCheckWrongHash
				if file.SHA1 == "" {
					check.Kind = biosCheckUnverified
					check.Note = "Could not read this file."
				} else if file.Size != known.Size {
					check.Note = fmt.Sprintf("File size is %d bytes; known-good is %d bytes.", file.Size, known.Size)
				} else {
					check.Note = "SHA-1 does not match the known-good checksum."
				}
				if score == 5 {
					check.Note += " It is also in the wrong folder."
				}
			}
		}
	}
	return check
}

func scanBIOSRoots(roots []string) biosDoctorReport {
	return scanBIOSRootsWithCatalog(roots, biosKnownHashes)
}

// scanBIOSRootsWithCatalog makes the checksum policy testable with tiny files.
// A filename alone never produces a verified result.
func scanBIOSRootsWithCatalog(roots []string, catalog map[string]biosKnownHash) biosDoctorReport {
	report := biosDoctorReport{Roots: append([]string(nil), roots...)}
	if len(roots) > 0 {
		report.TargetRoot = roots[0]
	}
	found := make(map[string][]biosFileFingerprint)
	files := 0
	for _, root := range roots {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				if strings.Count(filepath.Clean(filePath), string(os.PathSeparator))-rootDepth >= biosScanMaxDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if files >= biosScanMaxFiles {
				return filepath.SkipAll
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			files++
			rel, err := filepath.Rel(root, filePath)
			if err != nil {
				return nil
			}
			item := biosFileFingerprint{
				Name: entry.Name(), Root: root, Path: filePath,
				Relative: filepath.ToSlash(rel), Size: info.Size(), SHA256: biosNotHashed,
			}
			if info.Size() <= biosHashMaxBytes {
				if file, err := os.Open(filePath); err == nil {
					h1, h256 := sha1.New(), sha256.New()
					n, copyErr := io.Copy(io.MultiWriter(h1, h256), io.LimitReader(file, biosHashMaxBytes+1))
					closeErr := file.Close()
					if copyErr == nil && closeErr == nil && n <= biosHashMaxBytes {
						item.SHA1 = hex.EncodeToString(h1.Sum(nil))
						item.SHA256 = hex.EncodeToString(h256.Sum(nil))
					}
				}
			}
			found[strings.ToLower(item.Name)] = append(found[strings.ToLower(item.Name)], item)
			report.Files = append(report.Files, item)
			return nil
		})
	}
	sort.Slice(report.Files, func(i, j int) bool {
		if strings.EqualFold(report.Files[i].Name, report.Files[j].Name) {
			return report.Files[i].Path < report.Files[j].Path
		}
		return strings.ToLower(report.Files[i].Name) < strings.ToLower(report.Files[j].Name)
	})
	for _, group := range biosFilenameGroups {
		system := biosSystemStatus{Name: group.name, Ready: true, Optional: group.optional}
		for _, names := range group.accepted {
			check := biosDoctorCheckGroup(names, report.TargetRoot, found, catalog)
			system.Checks = append(system.Checks, check)
			if check.Kind != biosCheckVerified {
				system.Ready = false
			}
		}
		report.Systems = append(report.Systems, system)
	}
	return report
}

// biosScan runs the doctor's walk off the event thread: even bounded, it reads
// up to biosScanMaxFiles files off the SD card, and the page used to freeze
// the UI for as long as that took — on open and again on every Rescan.
var biosScan struct {
	sync.Mutex
	running bool
	report  *biosDoctorReport
}

// biosScanStart begins a scan unless one is already running.
func biosScanStart() {
	biosScan.Lock()
	if biosScan.running {
		biosScan.Unlock()
		return
	}
	biosScan.running = true
	biosScan.Unlock()
	go func() {
		report := scanBIOSRoots(biosDoctorRootsForScan())
		if IsWindows() || hakuxOffered() {
			xbox := emuXboxScan()
			report.Xbox = &xbox
		}
		biosScan.Lock()
		biosScan.report = &report
		biosScan.running = false
		biosScan.Unlock()
	}()
}

// biosScanSnapshot is the latest finished report (nil before the first one)
// and whether a scan is in flight.
func biosScanSnapshot() (*biosDoctorReport, bool) {
	biosScan.Lock()
	defer biosScan.Unlock()
	return biosScan.report, biosScan.running
}

// biosDoctorXboxLines is the Xbox block of the doctor's page: the emulator's
// checklist (xemu on Windows, hakuX on the Smart Pro) with what was found
// where, and where the missing files go. Nothing on a handheld with no Xbox
// report.
func biosDoctorXboxLines(xbox *emuXboxReport) []string {
	if xbox == nil {
		return nil
	}
	name := "Xbox"
	if xbox.Emulator != "" {
		name += " (" + xbox.Emulator + ")"
	}
	status := name + " · ready"
	missing := xbox.missing()
	switch {
	case xbox.Emulator == hakuxName && !xbox.Installed:
		status = name + " · not installed — the Xbox tile in Emulators installs it (experimental)"
	case !xbox.Installed:
		// xemu on Windows: the firmware can be put in place before xemu is.
		status = name + " · xemu is not installed (Emulators → xemu → X)"
		if len(missing) > 0 {
			status += " · missing " + strings.Join(missing, ", ")
		}
	case len(missing) > 0:
		status = name + " · missing " + strings.Join(missing, ", ")
	case !xbox.Configured:
		status = name + " · files found · starting xemu applies them, or Y Setup on its tile"
	}
	lines := []string{status}
	for _, f := range xbox.Files {
		switch {
		case f.ok():
			line := "  " + f.Label + "  " + f.Path
			if f.Note != "" {
				line += "  (" + f.Note + ")"
			}
			lines = append(lines, line)
		case f.Problem != "":
			lines = append(lines, "  "+f.Label+"  "+f.Path+"  — "+f.Problem)
		case f.Required:
			lines = append(lines, "  "+f.Label+"  missing · "+f.Want)
		case f.Note != "":
			// An informational row: firmware this build does not ask for.
			lines = append(lines, "  "+f.Label+"  "+f.Note+" · "+f.Want)
		}
	}
	if xbox.Folder != "" {
		lines = append(lines, "  Put the Xbox files in "+xbox.Folder)
	}
	for _, line := range xbox.Notes {
		lines = append(lines, "  "+line)
	}
	return append(lines, "")
}

// biosDoctorLines keeps the diagnosis separate from drawing, so the precise
// file and destination paths can also be checked without an SDL window.
func biosDoctorLines(report biosDoctorReport, scanning bool) []string {
	status := "Checked " + pluralizeCount(len(report.Roots), "location", "locations") +
		" · found " + pluralizeCount(len(report.Files), "file", "files")
	if scanning {
		status = "Scanning the BIOS folders…"
		if len(report.Roots) > 0 {
			status += " (showing the last result)"
		}
	}
	lines := []string{status, "Known-good SHA-1 checksums: libretro System.dat v1.19.0"}
	if report.TargetRoot != "" {
		lines = append(lines, "BIOS folder checked for RetroArch: "+report.TargetRoot)
	}
	lines = append(lines, "")
	for _, system := range report.Systems {
		allMissing := true
		for _, check := range system.Checks {
			if check.Kind != biosCheckMissing {
				allMissing = false
				break
			}
		}
		switch {
		case system.Ready:
			lines = append(lines, system.Name+" · verified")
		case system.Optional && allMissing:
			lines = append(lines, system.Name+" · optional, not found (most games run without)")
		default:
			lines = append(lines, system.Name+" · needs attention")
		}
		for _, check := range system.Checks {
			switch check.Kind {
			case biosCheckVerified:
				lines = append(lines, "  Verified: "+filepath.Base(check.Expected))
			case biosCheckMissing:
				lines = append(lines, "  Missing: "+strings.Join(check.Names, " or "))
				lines = append(lines, "    Put a known-good copy at "+check.Expected)
			case biosCheckWrongHash:
				lines = append(lines, "  Wrong hash: "+check.Found)
				if check.Note != "" {
					lines = append(lines, "    "+check.Note)
				}
				lines = append(lines, "    Replace with a known-good copy at "+check.Expected)
			case biosCheckWrongPlace:
				lines = append(lines, "  Wrong folder: "+check.Found)
				lines = append(lines, "    Put this verified file at "+check.Expected)
			case biosCheckUnverified:
				lines = append(lines, "  Cannot verify: "+check.Found)
				if check.Note != "" {
					lines = append(lines, "    "+check.Note)
				}
				if !biosDoctorSamePath(check.Found, check.Expected) {
					lines = append(lines, "    Expected at "+check.Expected)
				}
			}
		}
		lines = append(lines, "")
	}
	lines = append(lines, biosDoctorXboxLines(report.Xbox)...)
	if len(report.Files) > 0 {
		lines = append(lines, "Other files found:")
		known := make(map[string]bool)
		for _, group := range biosFilenameGroups {
			for _, names := range group.accepted {
				for _, name := range names {
					known[strings.ToLower(path.Base(name))] = true
				}
			}
		}
		for _, file := range report.Files {
			if !known[strings.ToLower(file.Name)] {
				lines = append(lines, "  "+file.Path+"  SHA-256 "+file.SHA256)
			}
		}
	} else if !scanning || len(report.Roots) > 0 {
		lines = append(lines, "No BIOS files found in the common locations.")
	}
	return lines
}

// biosDoctorWrapLines splits long paths over rows without hiding their ends.
// A width based on the font's wide M glyph keeps the rendered rows within the
// panel while preserving every character of a destination path.
func biosDoctorWrapLines(lines []string, maxChars int) []string {
	if maxChars < 12 {
		maxChars = 12
	}
	var out []string
	for _, original := range lines {
		line := original
		if line == "" {
			out = append(out, "")
			continue
		}
		first := true
		for line != "" {
			prefix := ""
			if !first {
				prefix = "    "
			}
			limit := maxChars - len(prefix)
			runes := []rune(line)
			if len(runes) <= limit {
				out = append(out, prefix+line)
				break
			}
			cut := limit
			for i := limit - 1; i > 4; i-- {
				if runes[i] == ' ' || runes[i] == '/' || runes[i] == '\\' {
					cut = i + 1
					break
				}
			}
			out = append(out, strings.TrimRight(prefix+string(runes[:cut]), " "))
			line = strings.TrimLeft(string(runes[cut:]), " ")
			first = false
		}
	}
	return out
}

func renderCommunityBIOSDoctor(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32, report biosDoctorReport, scanning bool) {
	contentX := panelX + padX
	contentW := panelW - 2*padX
	lineH := int32(font.Height()) + 7
	// Under the shared page header's measured band, not a number guessed here.
	y := panelY + headerH + 16
	charW, _, _ := font.SizeUTF8("M")
	if charW < 1 {
		charW = 1
	}
	maxChars := int(contentW / int32(charW))
	lines := biosDoctorWrapLines(biosDoctorLines(report, scanning), maxChars)
	available := int((panelY + panelH - 44 - y) / lineH)
	if available < 1 {
		available = 1
	}
	maxScroll := len(lines) - available
	if maxScroll < 0 {
		maxScroll = 0
	}
	if communityPanel.scroll > maxScroll {
		communityPanel.scroll = maxScroll
	}
	if communityPanel.scroll < 0 {
		communityPanel.scroll = 0
	}
	for i := 0; i < available; i++ {
		idx := communityPanel.scroll + i
		if idx >= len(lines) {
			break
		}
		line := lines[idx]
		if line != "" {
			renderText(renderer, config, font, ellipsize(font, line, contentW), ColorTextSecondary(), contentX, y)
		}
		y += lineH
	}
	footer := hintLine(FooterHint{"Up/Down", "Scroll"}, FooterHint{"Y", "Rescan"}, FooterHint{"B", "Back"}) +
		"   Files stay untouched"
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
