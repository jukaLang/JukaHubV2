package main

// plugnative_tools.go — the two bundled plugins that do real work on the
// card, in Go (see plugnative.go for how bundled plugins run).
//
// Save guard: the SD card is the only copy of most people's progress and the
// part that fails first, and the save manager only snapshots when someone
// remembers to press it. Save guard backs the saves up each time JukaHub
// starts — only when a save actually changed since the newest snapshot — and
// prunes only its own beyond five (the save manager keeps ten in all).
//
// Library doctor: a card filled from a Mac or by hand collects copies of the
// same ROM under two names, zips that were cut off mid-copy, empty files and
// Finder/Explorer litter ("._" twins, .DS_Store, Thumbs.db). JukaHub's own
// library hides the litter, but the stock launcher and RetroArch's playlists
// list it, and every copy costs space. The doctor reports all of it, deletes
// the litter, and moves extra copies into Roms/.duplicates (never deletes a
// game).

import (
	"archive/zip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── Save guard ────────────────────────────────────────────────────────────

const (
	// plugSaveGuardLabel is the label Save guard's snapshots carry, so the save
	// manager shows where they came from and the prune below finds its own.
	plugSaveGuardLabel = "Save guard"
	// plugSaveGuardKeep is how many of its own snapshots Save guard keeps. The
	// save manager keeps ten in all; five leaves room for manual ones.
	plugSaveGuardKeep = 5
)

func plugNativeSaveGuard(action string, _ map[string]interface{}, _ string) (string, error) {
	switch action {
	case "backup":
		return plugSaveGuardBackup()
	case "last":
		return plugSaveGuardLast(), nil
	}
	return "", plugUnknown(action, "backup, last")
}

// plugSaveGuardBackup snapshots every save folder unless nothing changed
// since the newest snapshot.
func plugSaveGuardBackup() (string, error) {
	roots := saveManagerRoots()
	if len(roots) == 0 {
		return "No save folders yet · play a game once and Save guard has something to keep", nil
	}
	if last, ok := latestSaveSnapshot(); ok && plugSavesMatchSnapshot(roots, last) {
		return "Saves unchanged since the last backup (" + saveSnapshotTime(last) + ") · nothing to do", nil
	}
	snap, err := createSaveSnapshot(roots, plugSaveGuardLabel)
	if errors.Is(err, errSaveManagerEmpty) {
		return "No saves yet · nothing to back up", nil
	}
	if err != nil {
		return "", fmt.Errorf("backup failed: %v", err)
	}
	plugSaveGuardPrune(snap.Manifest.ID)
	return fmt.Sprintf("Saves backed up: %s, %s · Restore a backup opens them",
		pluralizeCount(saveSnapshotFileCount(snap), "file", "files"), humanBytes(snap.Manifest.TotalBytes)), nil
}

// plugSaveGuardPrune deletes Save guard's own snapshots past the newest
// plugSaveGuardKeep; snapshots made any other way are never touched.
func plugSaveGuardPrune(keepID string) {
	kept := 0
	for _, snap := range listSaveSnapshots() { // newest first
		if snap.Manifest.Label != plugSaveGuardLabel {
			continue
		}
		kept++
		if kept <= plugSaveGuardKeep || snap.Manifest.ID == keepID {
			continue
		}
		if err := deleteSaveSnapshot(snap); err != nil {
			LogScene("plugins").Warn("save guard: old snapshot not deleted", "id", snap.Manifest.ID, "err", err)
		}
	}
}

// plugSaveGuardLast describes the newest snapshot.
func plugSaveGuardLast() string {
	snaps := listSaveSnapshots()
	if len(snaps) == 0 {
		return "No backups yet · Back up saves now makes the first"
	}
	last := snaps[0]
	return fmt.Sprintf("Last backup %s (%s): %s, %s · %s kept",
		saveSnapshotTime(last), last.Manifest.Label,
		pluralizeCount(saveSnapshotFileCount(last), "file", "files"), humanBytes(last.Manifest.TotalBytes),
		pluralizeCount(len(snaps), "backup", "backups"))
}

// plugSavesMatchSnapshot reports whether the save folders hold exactly the
// files of snap, byte for byte (names, sizes, then SHA-256). It walks the same
// files createSaveSnapshot copies. The clock is not trusted for this: the
// handheld boots before network time, so modification times say nothing.
func plugSavesMatchSnapshot(roots []saveRoot, snap saveSnapshot) bool {
	want := map[string]saveSnapshotFile{}
	for _, root := range snap.Manifest.Roots {
		for _, f := range root.Files {
			want[filepath.Clean(root.Path)+"\x00"+f.Path] = f
		}
	}
	seen := 0
	same := true
	for _, root := range roots {
		if !same {
			break
		}
		if !dirExists(root.Path) {
			continue
		}
		base := filepath.Clean(root.Path)
		_ = filepath.WalkDir(root.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				same = false
				return filepath.SkipAll
			}
			if d.IsDir() || !d.Type().IsRegular() || !isSaveFile(path) {
				return nil
			}
			rel, err := filepath.Rel(root.Path, path)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			f, ok := want[base+"\x00"+filepath.ToSlash(rel)]
			info, ierr := d.Info()
			if !ok || ierr != nil || info.Size() != f.Size || plugFileSHA256(path) != f.SHA256 {
				same = false
				return filepath.SkipAll
			}
			seen++
			return nil
		})
	}
	return same && seen == len(want)
}

// plugFileSHA256 is a file's SHA-256 in hex, "" when it cannot be read.
func plugFileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ── Library doctor ────────────────────────────────────────────────────────

const (
	// plugDoctorFullHashMax: files up to this size are compared whole; bigger
	// ones (disc images) by size plus samples when only reporting, and whole
	// before anything is moved.
	plugDoctorFullHashMax = 64 << 20
	plugDoctorSample      = 1 << 20
	// plugDoctorShown is how many examples each finding lists.
	plugDoctorShown = 5
	// plugDoctorWalkCap bounds the litter walk on a huge card.
	plugDoctorWalkCap = 300000
	// plugDoctorAside is the folder extra copies are moved into, at the top of
	// the Roms folder they came from. The library skips dot folders, and the
	// stock launcher lists systems, not folders, so neither shows it.
	plugDoctorAside = ".duplicates"
)

// plugDoctorSheetExt are the files that list other files (disc sheets and
// playlists). A copy beside one is part of a multi-file game and is never
// moved: the sheet would point at nothing.
var plugDoctorSheetExt = map[string]bool{".cue": true, ".gdi": true, ".m3u": true, ".ccd": true, ".toc": true, ".mds": true}

// plugDoctorGame is one game file of the library.
type plugDoctorGame struct {
	Path   string
	Root   string // the Roms folder it is in
	Folder string // its console folder, relative to Root ("SFC")
	Size   int64
	info   os.FileInfo
}

// plugDoctorFinding is what one check found.
type plugDoctorFinding struct {
	Games   int
	Dupes   [][]plugDoctorGame // same bytes, same console folder
	Across  [][]plugDoctorGame // same bytes, different console folders
	Likely  bool               // some groups are by samples only (check)
	Broken  []plugDoctorGame   // zips that do not open
	Empty   []plugDoctorGame   // zero-byte files
	Junk    []string           // litter files and __MACOSX folders
	JunkB   int64
	Wasted  int64 // bytes the extra same-console copies take
	Skipped int   // games that could not be read
}

func plugNativeLibraryDoctor(action string, _ map[string]interface{}, _ string) (string, error) {
	roots := romLibraryRoots()
	switch action {
	case "check":
		ShowToast("Library doctor: checking your games…", ToastKindInfo)
		return plugDoctorReport(plugDoctorExamine(roots, false), roots), nil
	case "junk":
		return plugDoctorRemoveJunk(roots), nil
	case "dupes":
		ShowToast("Library doctor: comparing your games — a big card takes a minute…", ToastKindInfo)
		return plugDoctorSetAside(plugDoctorExamine(roots, true)), nil
	}
	return "", plugUnknown(action, "check, junk, dupes")
}

// plugDoctorShared reports files that are the same on purpose in many
// places: BIOS images kept beside games (neogeo.zip in every arcade folder)
// and read-me files (.md is also a Mega Drive extension).
func plugDoctorShared(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if strings.Contains(name, "bios") || strings.HasPrefix(name, "readme") {
		return true
	}
	switch name {
	case "neogeo.zip", "pgm.zip", "skns.zip", "decocass.zip", "isgsm.zip", "nmk004.zip",
		"cchip.zip", "ym2608.zip", "qsound.zip", "megaplay.zip", "naomi.zip", "hng64.zip",
		"cpzn1.zip", "cpzn2.zip", "taitofx1.zip", "coh1000c.zip", "midssio.zip", "konamigx.zip":
		return true
	}
	return false
}

// plugDoctorGames lists the library's game files the way the Games library
// sees them (scanROMLibrary), with their sizes and console folders. Links
// are left out: a link and its target are one game, not two.
func plugDoctorGames(roots []string) (games []plugDoctorGame, skipped int) {
	report := scanROMLibrary(roots)
	for _, g := range report.Games {
		info, err := os.Lstat(g.Path)
		if err != nil {
			skipped++
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		game := plugDoctorGame{Path: g.Path, Size: info.Size(), info: info}
		for _, root := range roots {
			rel, err := filepath.Rel(root, g.Path)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			game.Root = root
			game.Folder = strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
			break
		}
		if game.Root == "" {
			skipped++
			continue
		}
		games = append(games, game)
	}
	return games, skipped
}

// plugDoctorSplit groups games by key, dropping groups of one and the files
// that key could not be computed for (""). Order follows the input.
func plugDoctorSplit(games []plugDoctorGame, key func(plugDoctorGame) string) [][]plugDoctorGame {
	by := map[string][]plugDoctorGame{}
	var order []string
	for _, g := range games {
		k := key(g)
		if k == "" {
			continue
		}
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], g)
	}
	var out [][]plugDoctorGame
	for _, k := range order {
		if len(by[k]) > 1 {
			out = append(out, by[k])
		}
	}
	return out
}

// plugDoctorDistinct drops entries that are the very same file reached twice
// (a hard link, or one folder under two spellings).
func plugDoctorDistinct(group []plugDoctorGame) []plugDoctorGame {
	out := group[:0:0]
	for _, g := range group {
		dup := false
		for _, o := range out {
			if g.info != nil && o.info != nil && os.SameFile(g.info, o.info) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, g)
		}
	}
	return out
}

// plugDoctorExamine runs every check. Copies are found in stages so a card of
// same-size cartridges is not read end to end: equal size, then three 1 MB
// samples (the whole file up to 3 MB), then the whole file — for anything up
// to plugDoctorFullHashMax, or everything when full (moving needs certainty).
func plugDoctorExamine(roots []string, full bool) plugDoctorFinding {
	games, skipped := plugDoctorGames(roots)
	f := plugDoctorFinding{Games: len(games), Skipped: skipped}
	var candidates []plugDoctorGame
	for _, g := range games {
		switch {
		case g.Size == 0:
			f.Empty = append(f.Empty, g)
			continue
		case strings.EqualFold(filepath.Ext(g.Path), ".zip"):
			if r, err := zip.OpenReader(g.Path); err != nil {
				f.Broken = append(f.Broken, g)
			} else {
				r.Close()
			}
		}
		if !plugDoctorShared(g.Path) {
			candidates = append(candidates, g)
		}
	}
	sizeKey := func(g plugDoctorGame) string { return strconv.FormatInt(g.Size, 10) }
	for _, sameSize := range plugDoctorSplit(candidates, sizeKey) {
		sameSize = plugDoctorDistinct(sameSize)
		if len(sameSize) < 2 {
			continue
		}
		size := sameSize[0].Size
		sampleKey := func(g plugDoctorGame) string { return plugDoctorFingerprint(g.Path, size, false) }
		for _, group := range plugDoctorSplit(sameSize, sampleKey) {
			switch {
			case size <= 3*plugDoctorSample:
				// The samples covered the whole file.
			case full || size <= plugDoctorFullHashMax:
				wholeKey := func(g plugDoctorGame) string { return plugDoctorFingerprint(g.Path, size, true) }
				for _, exact := range plugDoctorSplit(group, wholeKey) {
					plugDoctorAddGroup(&f, exact, size)
				}
				continue
			default:
				f.Likely = true
			}
			plugDoctorAddGroup(&f, group, size)
		}
	}
	f.Junk, f.JunkB = plugDoctorFindJunk(roots)
	return f
}

// plugDoctorAddGroup files one group of identical copies: per console folder
// as duplicates, and as a whole when it spans consoles.
func plugDoctorAddGroup(f *plugDoctorFinding, group []plugDoctorGame, size int64) {
	folderKey := func(g plugDoctorGame) string { return g.Root + "\x00" + strings.ToLower(g.Folder) }
	folders := map[string]bool{}
	for _, g := range group {
		folders[folderKey(g)] = true
	}
	if len(folders) > 1 {
		f.Across = append(f.Across, group)
	}
	for _, same := range plugDoctorSplit(group, folderKey) {
		f.Dupes = append(f.Dupes, same)
		f.Wasted += size * int64(len(same)-1)
	}
}

// plugDoctorFingerprint identifies a file's bytes: SHA-1 of the whole file,
// or of three 1 MB samples (start, middle, end) when whole is false. "" when
// the file cannot be read.
func plugDoctorFingerprint(path string, size int64, whole bool) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	h := sha1.New()
	if whole {
		if _, err := io.Copy(h, file); err != nil {
			return ""
		}
		return "w" + hex.EncodeToString(h.Sum(nil))
	}
	buf := make([]byte, plugDoctorSample)
	for _, at := range []int64{0, size/2 - plugDoctorSample/2, size - plugDoctorSample} {
		if at < 0 {
			at = 0
		}
		n, err := file.ReadAt(buf, at)
		if err != nil && !errors.Is(err, io.EOF) {
			return ""
		}
		h.Write(buf[:n])
	}
	return "s" + hex.EncodeToString(h.Sum(nil))
}

// plugDoctorIsJunk reports whether a file name is Finder or Explorer litter.
func plugDoctorIsJunk(name string) bool {
	if strings.HasPrefix(name, "._") {
		return true
	}
	switch strings.ToLower(name) {
	case ".ds_store", "thumbs.db", "desktop.ini":
		return true
	}
	return false
}

// plugDoctorFindJunk walks the Roms folders for litter: "._" twins,
// .DS_Store, Thumbs.db, desktop.ini, and __MACOSX folders (which hold nothing
// else). Other dot folders — including Roms/.duplicates — are not entered.
func plugDoctorFindJunk(roots []string) (paths []string, bytes int64) {
	walked := 0
	seen := map[string]bool{} // a Roms folder inside another is walked once
	for _, root := range roots {
		if !dirExists(root) {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			walked++
			if walked > plugDoctorWalkCap {
				return filepath.SkipAll
			}
			name := d.Name()
			if d.IsDir() {
				if path == root {
					return nil
				}
				if seen[path] {
					return filepath.SkipDir
				}
				seen[path] = true
				if strings.EqualFold(name, "__MACOSX") {
					paths = append(paths, path)
					bytes += plugDirSize(path)
					return filepath.SkipDir
				}
				if strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && plugDoctorIsJunk(name) && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
				if info, err := d.Info(); err == nil {
					bytes += info.Size()
				}
			}
			return nil
		})
	}
	return paths, bytes
}

// plugDirSize adds up the regular files under dir.
func plugDirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d != nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// plugDoctorName is a game as a report line shows it: console folder and file.
func plugDoctorName(g plugDoctorGame) string {
	return g.Folder + "/" + filepath.Base(g.Path)
}

// plugDoctorReport is the "Check my games" text. Its first line is the toast.
func plugDoctorReport(f plugDoctorFinding, roots []string) string {
	if f.Games == 0 && len(f.Junk) == 0 {
		return "No games found in " + strings.Join(roots, ", ")
	}
	var head []string
	head = append(head, pluralizeCount(f.Games, "game", "games")+" checked")
	extra := 0
	for _, group := range f.Dupes {
		extra += len(group) - 1
	}
	if extra > 0 {
		head = append(head, pluralizeCount(extra, "extra copy", "extra copies")+" ("+humanBytes(f.Wasted)+")")
	}
	if n := len(f.Broken); n > 0 {
		head = append(head, pluralizeCount(n, "broken zip", "broken zips"))
	}
	if n := len(f.Empty); n > 0 {
		head = append(head, pluralizeCount(n, "empty file", "empty files"))
	}
	if n := len(f.Junk); n > 0 {
		head = append(head, pluralizeCount(n, "junk file", "junk files"))
	}
	if len(head) == 1 && len(f.Across) == 0 {
		return head[0] + " · all clear"
	}
	lines := []string{strings.Join(head, " · ")}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		lines = append(lines, title)
		for i, item := range items {
			if i == plugDoctorShown {
				lines = append(lines, fmt.Sprintf("  …and %d more", len(items)-plugDoctorShown))
				break
			}
			lines = append(lines, "  "+item)
		}
	}
	var dupes, across, broken, empty []string
	for _, group := range f.Dupes {
		names := make([]string, len(group))
		for i, g := range group {
			names[i] = filepath.Base(g.Path)
		}
		dupes = append(dupes, group[0].Folder+": "+strings.Join(names, " = "))
	}
	for _, group := range f.Across {
		names := make([]string, len(group))
		for i, g := range group {
			names[i] = plugDoctorName(g)
		}
		across = append(across, strings.Join(names, " = "))
	}
	for _, g := range f.Broken {
		broken = append(broken, plugDoctorName(g))
	}
	for _, g := range f.Empty {
		empty = append(empty, plugDoctorName(g))
	}
	list("Same game twice in one console (Set duplicates aside moves the extra copy):", dupes)
	list("Same file in two consoles (often on purpose, left alone):", across)
	list("Zips that do not open (copy them again):", broken)
	list("Empty files (0 bytes, copy them again):", empty)
	if len(f.Junk) > 0 {
		lines = append(lines, fmt.Sprintf("Mac/Windows litter: %s, %s · Remove junk files deletes it",
			pluralizeCount(len(f.Junk), "file", "files"), humanBytes(f.JunkB)))
	}
	if f.Likely {
		lines = append(lines, "Big disc images were compared by samples; Set duplicates aside compares them whole first.")
	}
	if f.Skipped > 0 {
		lines = append(lines, pluralizeCount(f.Skipped, "file", "files")+" could not be read.")
	}
	return strings.Join(lines, "\n")
}

// plugDoctorRemoveJunk deletes the litter. Only names plugDoctorIsJunk
// matches and folders named __MACOSX are removed.
func plugDoctorRemoveJunk(roots []string) string {
	paths, bytes := plugDoctorFindJunk(roots)
	if len(paths) == 0 {
		return "No junk files in the Roms folders"
	}
	removed, failed := 0, 0
	for _, path := range paths {
		var err error
		if strings.EqualFold(filepath.Base(path), "__MACOSX") {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil && !os.IsNotExist(err) {
			failed++
			continue
		}
		removed++
	}
	out := fmt.Sprintf("Removed %s of Mac/Windows litter (%s)", pluralizeCount(removed, "file", "files"), humanBytes(bytes))
	if failed > 0 {
		out += fmt.Sprintf(" · %d could not be removed (read-only card?)", failed)
	}
	return out
}

// plugDoctorSaveStems lists the save names on the card (file name without
// its extension, lower case): of two copies of a game, the one with saves is
// the one to keep.
func plugDoctorSaveStems() map[string]bool {
	stems := map[string]bool{}
	walked := 0
	for _, root := range saveManagerRoots() {
		if !dirExists(root.Path) {
			continue
		}
		_ = filepath.WalkDir(root.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			walked++
			if walked > plugDoctorWalkCap {
				return filepath.SkipAll
			}
			if !d.IsDir() {
				name := d.Name()
				stems[strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))] = true
			}
			return nil
		})
	}
	return stems
}

// plugDoctorHasSave reports whether a copy has saves under its own name.
func plugDoctorHasSave(g plugDoctorGame, saves map[string]bool) bool {
	base := filepath.Base(g.Path)
	return saves[strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))]
}

// plugDoctorKeepFirst orders a group of copies so the one to keep is first:
// the one with saves, then the shallowest, then the shortest path.
func plugDoctorKeepFirst(group []plugDoctorGame, saves map[string]bool) {
	hasSave := func(g plugDoctorGame) bool { return plugDoctorHasSave(g, saves) }
	depth := func(g plugDoctorGame) int { return strings.Count(filepath.ToSlash(g.Path), "/") }
	sort.SliceStable(group, func(i, j int) bool {
		a, b := group[i], group[j]
		if sa, sb := hasSave(a), hasSave(b); sa != sb {
			return sa
		}
		if da, db := depth(a), depth(b); da != db {
			return da < db
		}
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}
		return a.Path < b.Path
	})
}

// plugDoctorMovable reports whether a copy can be moved on its own: it is not
// a sheet or playlist and no sheet or playlist sits beside it.
func plugDoctorMovable(path string) bool {
	if plugDoctorSheetExt[strings.ToLower(filepath.Ext(path))] {
		return false
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && plugDoctorSheetExt[strings.ToLower(filepath.Ext(e.Name()))] {
			return false
		}
	}
	return true
}

// plugDoctorSetAside moves every extra same-console copy into
// <Roms>/.duplicates/<same path>, keeping the copy with saves.
func plugDoctorSetAside(f plugDoctorFinding) string {
	if len(f.Dupes) == 0 {
		return "No duplicate games · nothing to move"
	}
	saves := plugDoctorSaveStems()
	moved, kept, failed := 0, 0, 0
	var bytes int64
	var lines []string
	for _, group := range f.Dupes {
		plugDoctorKeepFirst(group, saves)
		if len(group) > 1 && plugDoctorHasSave(group[1], saves) {
			// Two copies with saves of their own: moving one would strand
			// its saves, so the group stays for the player to choose.
			kept += len(group) - 1
			continue
		}
		for _, g := range group[1:] {
			if !plugDoctorMovable(g.Path) {
				kept++
				continue
			}
			rel, err := filepath.Rel(g.Root, g.Path)
			if err != nil || strings.HasPrefix(rel, "..") {
				failed++
				continue
			}
			dest := filepath.Join(g.Root, plugDoctorAside, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				failed++
				continue
			}
			ext := filepath.Ext(dest)
			for i := 2; i < 100; i++ {
				if _, err := os.Lstat(dest); os.IsNotExist(err) {
					break
				}
				dest = filepath.Join(g.Root, plugDoctorAside, strings.TrimSuffix(rel, ext)+fmt.Sprintf(" (%d)", i)+ext)
			}
			if _, err := os.Lstat(dest); err == nil {
				failed++
				continue
			}
			if err := os.Rename(g.Path, dest); err != nil {
				failed++
				continue
			}
			moved++
			bytes += g.Size
			if len(lines) < plugDoctorShown {
				lines = append(lines, "  "+plugDoctorName(g)+" (kept "+filepath.Base(group[0].Path)+")")
			}
		}
	}
	head := "Moved " + pluralizeCount(moved, "extra copy", "extra copies") + " (" + humanBytes(bytes) + ") to Roms/" + plugDoctorAside
	if moved == 0 {
		head = "Nothing moved"
	}
	out := []string{head}
	out = append(out, lines...)
	if moved > len(lines) {
		out = append(out, fmt.Sprintf("  …and %d more", moved-len(lines)))
	}
	if moved > 0 {
		out = append(out, "To undo, move them back from Roms/"+plugDoctorAside+"; delete that folder once you are sure.")
		// An open Games library still lists the moved copies until it rescans.
		tbxPost(func() {
			if gameLib.open {
				refreshGameLibrary()
			}
		})
	}
	if kept > 0 {
		out = append(out, pluralizeCount(kept, "copy", "copies")+" stayed: part of a disc set (beside a .cue or .m3u), or both copies have saves; tidy those by hand.")
	}
	if failed > 0 {
		out = append(out, pluralizeCount(failed, "copy", "copies")+" could not be moved (read-only card?).")
	}
	return strings.Join(out, "\n")
}
