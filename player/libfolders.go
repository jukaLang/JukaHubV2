package main

// libfolders.go — Library folders: one view of where the games, BIOS files
// and saves live, whichever emulator reads them, and the means to make the
// emulators agree (libfolders_ui.go is the screen).
//
// The map. For each kind — games, BIOS files, saves, save states — the
// folders this handheld's emulators actually read: the game library's roots
// (romLibraryRoots), the BIOS folders the BIOS doctor checks (RetroArch's
// system_directory first, the firmware's BIOS folder, RetroArch's own
// system folder), and the save folders the Save Manager finds (RetroArch's,
// PPSSPP's, DraStic's, NextUI's). The first of each kind is the library's
// folder: the one RetroArch, which runs most games, was set up to use.
//
// Sharing BIOS files. RetroArch and the firmware's own emulators often read
// BIOS files from different folders, so a BIOS copied into one is "missing"
// in the other. "Share BIOS files" copies every known firmware file (the
// BIOS doctor's table) that one BIOS folder on the card has and another
// lacks, at the same place inside it: nothing is replaced, nothing deleted,
// nothing over libFoldersBIOSMax.
//
// One BIOS folder in JukaHub OS. The generated layer "jukahub-folders" (off
// until switched on in Settings → JukaHub OS) binds the library's BIOS folder
// over every other BIOS folder on the card in the OS session's private mount
// namespace, so every emulator JukaHub starts reads the one folder, and a
// BIOS added there reaches all of them. A folder is only bound over when
// every file in it is in the library's folder too (after a share, that is
// the usual case): a bind hides what is under it, and nothing may vanish.
// Saves are not joined: each emulator names its saves its own way, so the
// Save Manager, the Time Machine and Save sync treat all save folders as one
// set instead.
//
// Nothing here was run on a device from this desk.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// libFoldersBIOSMax is the largest file "Share BIOS files" copies.
	libFoldersBIOSMax = 64 << 20
	// libFoldersWalkMax and libFoldersWalkDepth bound one look through a
	// BIOS folder (RetroArch's system folder also holds core data).
	libFoldersWalkMax   = 4000
	libFoldersWalkDepth = 3
	// jukaosFoldersID is the generated layer's id.
	jukaosFoldersID = "jukahub-folders"
)

// libFolder is one folder of the map.
type libFolder struct {
	Kind  string // "games", "bios", "saves", "states"
	Name  string // who reads it: "RetroArch saves", "Firmware BIOS"…
	Path  string
	Files int
	Main  bool // the library's folder of its kind
}

// libFoldersMap is the whole map: games, BIOS, saves, states, in that order.
// It reads the card (a few directory listings); off the main thread.
func libFoldersMap() []libFolder {
	var out []libFolder
	for i, root := range romLibraryRoots() {
		if !dirExists(root) {
			continue
		}
		out = append(out, libFolder{Kind: "games", Name: "Games", Path: root, Files: libFoldersCountDirs(root), Main: i == 0 || len(out) == 0})
	}
	for i, dir := range libFoldersBIOSDirs() {
		files, _ := libFoldersWalk(dir)
		out = append(out, libFolder{Kind: "bios", Name: libFoldersBIOSName(dir), Path: dir, Files: len(files), Main: i == 0})
	}
	seen := map[string]bool{}
	for _, r := range saveManagerRoots() {
		kind := "saves"
		if strings.Contains(strings.ToLower(r.Name), "state") {
			kind = "states"
		}
		files, _ := libFoldersWalk(r.Path)
		out = append(out, libFolder{Kind: kind, Name: r.Name, Path: r.Path, Files: len(files), Main: !seen[kind]})
		seen[kind] = true
	}
	return out
}

// libFoldersCountDirs counts a games root's console folders.
func libFoldersCountDirs(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

// libFoldersBIOSDirs are the BIOS folders on this card that exist, the
// library's first (the BIOS doctor's order: RetroArch's own setting leads).
func libFoldersBIOSDirs() []string {
	card := saveManagerSDRoot()
	var out []string
	for _, dir := range biosDoctorRoots() {
		if !dirExists(dir) {
			continue
		}
		if card != "" && !IsWindows() && !isPathWithin(dir, card) {
			continue // the firmware's own /usr/trimui copy is not the card's
		}
		out = append(out, filepath.Clean(dir))
	}
	return out
}

// libFoldersBIOSName says who reads a BIOS folder.
func libFoldersBIOSName(dir string) string {
	if configured, _, ok := gameDiagConfiguredSystemDir("", ""); ok && filepath.Clean(configured) == dir {
		return "RetroArch's system folder"
	}
	low := strings.ToLower(filepath.ToSlash(dir))
	switch {
	case strings.HasSuffix(low, "/retroarch/.retroarch/system"):
		return "RetroArch's own system folder"
	case strings.HasSuffix(low, "/bios"):
		if data, err := P().DataDir(); err == nil && isPathWithin(dir, data) {
			return "JukaHub's BIOS folder"
		}
		return "The firmware's BIOS folder"
	}
	return "BIOS folder"
}

// libFoldersKnownBIOS is the set of firmware file names the BIOS doctor's
// table knows, lower case: "scph1001.bin", "dc/dc_boot.bin".
func libFoldersKnownBIOS() map[string]bool {
	out := map[string]bool{}
	for _, g := range biosFilenameGroups {
		for _, names := range g.accepted {
			for _, n := range names {
				if strings.Contains(n, ".") {
					out[strings.ToLower(n)] = true
				}
			}
		}
	}
	return out
}

// libFoldersWalk lists a folder's files (slash paths, as they are), bounded;
// complete is false when the bound cut it short.
func libFoldersWalk(dir string) (files []string, complete bool) {
	complete = true
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(filepath.Base(p), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(rel, "/")+1 > libFoldersWalkDepth {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		if n > libFoldersWalkMax {
			complete = false
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			files = append(files, rel)
		}
		return nil
	})
	return files, complete
}

// libFoldersBIOSFiles lists a folder's known firmware files, keyed by their
// lower-case path.
func libFoldersBIOSFiles(dir string, known map[string]bool) map[string]string {
	files, _ := libFoldersWalk(dir)
	out := map[string]string{}
	for _, rel := range files {
		low := strings.ToLower(rel)
		if known[low] || known[strings.ToLower(filepath.Base(rel))] {
			out[low] = rel
		}
	}
	return out
}

// libFoldersShareResult is what a share did.
type libFoldersShareResult struct {
	Copied  int
	Bytes   int64
	Skipped int
	Failed  int
	First   string
}

// libFoldersShareBIOS copies every known firmware file one BIOS folder has
// and another lacks into it. Off the main thread.
func libFoldersShareBIOS() libFoldersShareResult {
	var res libFoldersShareResult
	dirs := libFoldersBIOSDirs()
	if len(dirs) < 2 {
		return res
	}
	known := libFoldersKnownBIOS()
	have := make([]map[string]string, len(dirs))
	for i, dir := range dirs {
		have[i] = libFoldersBIOSFiles(dir, known)
	}
	for i, src := range dirs {
		for low, rel := range have[i] {
			for j, dst := range dirs {
				if i == j {
					continue
				}
				if _, ok := have[j][low]; ok {
					continue
				}
				from := filepath.Join(src, filepath.FromSlash(rel))
				to := filepath.Join(dst, filepath.FromSlash(rel))
				info, err := os.Stat(from)
				if err != nil || !info.Mode().IsRegular() || info.Size() > libFoldersBIOSMax || !isPathWithin(to, dst) {
					res.Skipped++
					continue
				}
				if !parentResolvesWithin(dst, to) {
					res.Skipped++
					continue
				}
				_, n, err := copySaveFile(from, to+".jukahub-share.tmp", libFoldersBIOSMax)
				if err == nil {
					err = os.Rename(to+".jukahub-share.tmp", to)
				}
				if err != nil {
					_ = os.Remove(to + ".jukahub-share.tmp")
					res.Failed++
					if res.First == "" {
						res.First = filepath.Base(rel) + ": " + err.Error()
					}
					continue
				}
				have[j][low] = rel
				res.Copied++
				res.Bytes += n
			}
		}
	}
	if res.Copied > 0 {
		logInfo("library", "library folders: shared %d BIOS file(s), %s", res.Copied, humanBytes(res.Bytes))
	}
	return res
}

// libFoldersBIOSGaps counts, per BIOS folder, the known firmware files other
// BIOS folders have and it lacks. Off the main thread.
func libFoldersBIOSGaps() map[string]int {
	dirs := libFoldersBIOSDirs()
	known := libFoldersKnownBIOS()
	have := make([]map[string]string, len(dirs))
	all := map[string]bool{}
	for i, dir := range dirs {
		have[i] = libFoldersBIOSFiles(dir, known)
		for low := range have[i] {
			all[low] = true
		}
	}
	out := map[string]int{}
	for i, dir := range dirs {
		for low := range all {
			if _, ok := have[i][low]; !ok {
				out[dir]++
			}
		}
	}
	return out
}

// ── JukaHub OS: one BIOS folder ──────────────────────────────────────────

// jukaosFoldersLayer is the generated layer that binds the library's BIOS
// folder over the card's other BIOS folders in an OS session. A folder
// holding a file the library's folder lacks is left alone, with a note.
func jukaosFoldersLayer(h jukaosHost) jukaosLayer {
	l := jukaosLayer{
		ID: jukaosFoldersID, Builtin: true,
		Spec: jukaosLayerSpec{
			Name:        "One BIOS folder for every emulator",
			Description: "The firmware's emulators read RetroArch's BIOS folder too, so a BIOS file added there reaches every emulator",
		},
	}
	dirs := libFoldersBIOSDirs()
	if len(dirs) < 2 {
		l.Problem = "this card has one BIOS folder: there is nothing to join"
		return l
	}
	main := dirs[0]
	mainFiles, complete := libFoldersWalk(main)
	if !complete {
		l.Problem = "the library's BIOS folder is too large to check"
		return l
	}
	inMain := make(map[string]bool, len(mainFiles))
	for _, rel := range mainFiles {
		inMain[strings.ToLower(rel)] = true
	}
	for _, dir := range dirs[1:] {
		if h.AppDir != "" && isPathWithin(dir, filepath.FromSlash(h.AppDir)) {
			continue // JukaHub's own folder is never covered
		}
		files, complete := libFoldersWalk(dir)
		var unique []string
		for _, rel := range files {
			if !inMain[strings.ToLower(rel)] {
				unique = append(unique, rel)
			}
		}
		switch {
		case !complete:
			l.Notes = append(l.Notes, dir+": too large to check, left alone")
		case len(unique) > 0:
			sort.Strings(unique)
			l.Notes = append(l.Notes, dir+": holds "+pluralizeCount(len(unique), "file", "files")+" the library's BIOS folder lacks ("+
				unique[0]+"…); Share BIOS files in Library folders first")
		default:
			l.Binds = append(l.Binds, jukaosBind{Src: main, Dst: dir, Dir: true, RW: true})
		}
	}
	if len(l.Binds) == 0 && l.Problem == "" {
		l.Problem = "no BIOS folder can be joined yet: Share BIOS files in Library folders first"
	}
	return l
}
