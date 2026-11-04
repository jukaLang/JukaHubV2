package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const biosDoctorEntryName = "BIOS Doctor"

type biosFileFingerprint struct {
	Name   string
	Root   string
	SHA256 string
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
	Name    string
	Ready   bool
	Missing []string
	Files   []biosFileFingerprint
}

type biosDoctorReport struct {
	Roots   []string
	Systems []biosSystemStatus
	Files   []biosFileFingerprint
}

var biosFilenameGroups = []struct {
	name     string
	accepted [][]string
}{
	{name: "PS1", accepted: [][]string{{"scph5500.bin", "scph5501.bin", "scph5502.bin", "psxonpsp660.bin"}}},
	{name: "Dreamcast", accepted: [][]string{{"dc_boot.bin"}, {"dc_flash.bin"}}},
	{name: "Saturn", accepted: [][]string{{"sega_101.bin", "mpr-17933.bin", "saturn_bios.bin"}}},
}

// biosDoctorRoots lists the common JukaHub, handheld, and RetroArch BIOS
// locations. Files are never moved or modified by the doctor.
func biosDoctorRoots() []string {
	roots := []string{resolvePath("BIOS")}
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

func scanBIOSRoots(roots []string) biosDoctorReport {
	report := biosDoctorReport{Roots: append([]string(nil), roots...)}
	found := make(map[string]biosFileFingerprint)
	files := 0
	for _, root := range roots {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth >= biosScanMaxDepth {
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
			sum := biosNotHashed
			if info.Size() <= biosHashMaxBytes {
				file, err := os.Open(path)
				if err != nil {
					return nil
				}
				h := sha256.New()
				_, copyErr := io.Copy(h, io.LimitReader(file, biosHashMaxBytes+1))
				closeErr := file.Close()
				if copyErr != nil || closeErr != nil {
					return nil
				}
				sum = hex.EncodeToString(h.Sum(nil))
			}
			item := biosFileFingerprint{
				Name:   entry.Name(),
				Root:   root,
				SHA256: sum,
			}
			key := strings.ToLower(entry.Name())
			if _, exists := found[key]; !exists {
				found[key] = item
			}
			report.Files = append(report.Files, item)
			return nil
		})
	}
	sort.Slice(report.Files, func(i, j int) bool {
		if strings.EqualFold(report.Files[i].Name, report.Files[j].Name) {
			return report.Files[i].Root < report.Files[j].Root
		}
		return strings.ToLower(report.Files[i].Name) < strings.ToLower(report.Files[j].Name)
	})
	for _, group := range biosFilenameGroups {
		system := biosSystemStatus{Name: group.name, Ready: true}
		for _, requiredNames := range group.accepted {
			matched := false
			for _, name := range requiredNames {
				if item, ok := found[strings.ToLower(name)]; ok {
					matched = true
					system.Files = append(system.Files, item)
				}
			}
			if !matched {
				system.Ready = false
				system.Missing = append(system.Missing, strings.Join(requiredNames, " or "))
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
		report := scanBIOSRoots(biosDoctorRoots())
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

func biosDoctorSummary(report biosDoctorReport) string {
	parts := make([]string, 0, len(report.Systems))
	for _, system := range report.Systems {
		if system.Ready {
			parts = append(parts, system.Name+" ready")
		} else {
			parts = append(parts, system.Name+" missing "+strings.Join(system.Missing, ", "))
		}
	}
	return strings.Join(parts, ", ")
}

func renderCommunityBIOSDoctor(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32, report biosDoctorReport, scanning bool) {
	contentX := panelX + padX
	contentW := panelW - 2*padX
	lineH := int32(font.Height()) + 7
	// Under the shared page header's measured band, not a number guessed here.
	y := panelY + headerH + 16
	status := fmt.Sprintf("Checked %d location(s) · found %d file(s)", len(report.Roots), len(report.Files))
	if scanning {
		status = "Scanning the BIOS folders…"
		if len(report.Roots) > 0 {
			status += " (showing the last result)"
		}
	}
	lines := []string{status, "SHA-256 fingerprints · filenames indicate presence, not authenticity", ""}
	for _, system := range report.Systems {
		if system.Ready {
			lines = append(lines, system.Name+" · ready")
		} else {
			lines = append(lines, system.Name+" · missing "+strings.Join(system.Missing, ", "))
		}
		for _, file := range system.Files {
			lines = append(lines, "  "+file.Name+"  "+file.SHA256)
		}
		lines = append(lines, "")
	}
	if len(report.Files) > 0 {
		lines = append(lines, "Other files found:")
		known := make(map[string]bool)
		for _, system := range report.Systems {
			for _, file := range system.Files {
				known[strings.ToLower(file.Name)] = true
			}
		}
		for _, file := range report.Files {
			if !known[strings.ToLower(file.Name)] {
				lines = append(lines, "  "+file.Name+"  "+file.SHA256)
			}
		}
	} else if !scanning || len(report.Roots) > 0 {
		lines = append(lines, "No BIOS files found in the common locations.")
	}
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
