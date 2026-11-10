package main

// files_manager.go — the Files screen, run as a file manager.
//
// What it adds over a raw directory listing:
//   - a breadcrumb bar above the list where every path segment is a target,
//     with the folder's totals and the current sort on its right,
//   - rows that carry a type badge, the name, and right-aligned size and date,
//   - multi-select (X marks a row, "Select all" in the action sheet),
//   - one action sheet (Y) holding New folder, Rename, Copy, Cut, Paste,
//     Delete, Select all, Sort and the transfer entries. Operations that
//     destroy something arm first and act on the second press,
//   - Copy/Cut keeping paths in a clipboard, so a file moves between two
//     folders of the card without a PC.
//
// Every path is resolved against the Files root before it is touched and a new
// name goes through safeJoinFile, so this screen cannot be pointed outside the
// tree it browses — the same rule the rest of the explorer follows.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- Sorting ---------------------------------------------------------------

// feSortMode is the order the listing is kept in. Directories always come
// first, which is a file manager's fixed rule; ".." comes before even those,
// because it is the way out of the folder. The mode orders the names inside
// each group.
type feSortMode int

const (
	feSortName feSortMode = iota
	feSortSize
	feSortDate
)

var feSortModes = []feSortMode{feSortName, feSortSize, feSortDate}

func feSortLabel(mode feSortMode) string {
	switch mode {
	case feSortSize:
		return "Size"
	case feSortDate:
		return "Newest"
	}
	return "Name"
}

// feSortModeFromValue maps the stored name back to the mode. An unknown value
// (a hand-edited config) is the default order rather than an error.
func feSortModeFromValue(value string) feSortMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "size":
		return feSortSize
	case "newest", "date", "modified":
		return feSortDate
	}
	return feSortName
}

func feSortModeOf(config *Config) feSortMode {
	if config == nil {
		return feSortName
	}
	value, _ := config.Variables.Custom["fe_sort"].(string)
	return feSortModeFromValue(value)
}

// feSetSortMode remembers the choice the same way fe_path is remembered, so the
// next visit opens the folder in the order it was left in.
func feSetSortMode(config *Config, mode feSortMode) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["fe_sort"] = strings.ToLower(feSortLabel(mode))
}

func feCycleSort(config *Config) feSortMode {
	mode := feSortModeOf(config)
	for i, m := range feSortModes {
		if m == mode {
			mode = feSortModes[(i+1)%len(feSortModes)]
			break
		}
	}
	feSetSortMode(config, mode)
	feSortEntries(feEntriesOf(config), mode)
	PlayNavSound()
	return mode
}

func feEntriesOf(config *Config) []FileEntry {
	if config == nil {
		return nil
	}
	entries, _ := config.Variables.Custom["fe_entries"].([]FileEntry)
	return entries
}

// feSortEntries orders a listing in place. Directories are never sorted by size
// or date: "the biggest folder" is not a thing on a listing whose folders are
// not measured, so they stay alphabetical and only files follow the mode.
func feSortEntries(entries []FileEntry, mode feSortMode) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Name == ".." || b.Name == ".." {
			return a.Name == ".." && b.Name != ".."
		}
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		if !a.IsDir {
			switch mode {
			case feSortSize:
				if a.Size != b.Size {
					return a.Size > b.Size
				}
			case feSortDate:
				if !a.Mod.Equal(b.Mod) {
					return a.Mod.After(b.Mod)
				}
			}
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.Name < b.Name
	})
}

// --- Selection and clipboard ----------------------------------------------

// feSelectedPaths holds the marked rows by absolute path. Paths, not indices:
// the list is re-sorted and re-read on every refresh, and an index would mark
// a different file after a refresh.
var feSelectedPaths = map[string]bool{}

func feSelectionCount() int { return len(feSelectedPaths) }

func feSelectionHas(path string) bool { return feSelectedPaths[path] }

func feSelectionClear() {
	for path := range feSelectedPaths {
		delete(feSelectedPaths, path)
	}
}

func feSelectionToggle(path string) bool {
	if path == "" {
		return false
	}
	if feSelectedPaths[path] {
		delete(feSelectedPaths, path)
		return false
	}
	feSelectedPaths[path] = true
	return true
}

// feFocusEntry is the row the cursor is on, if any.
func feFocusEntry(config *Config) (FileEntry, bool) {
	entries := feEntriesOf(config)
	if focusedFileIndex < 0 || focusedFileIndex >= len(entries) {
		return FileEntry{}, false
	}
	return entries[focusedFileIndex], true
}

// feSelectionTargets is what an action applies to: the marked rows when there
// are any, otherwise the row under the cursor. That is the rule every desktop
// file manager follows, and it means the sheet's actions are useful without
// first learning that X marks rows.
//
// The ".." row is never a target: its Path is the parent folder, and the
// cursor rests on it whenever a folder is entered, so Delete with nothing
// marked used to remove the whole folder above — every ROM next to the one
// the user was looking at.
func feSelectionTargets(config *Config) []FileEntry {
	// A Locations row is a whole drive or the card: never a target for Copy,
	// Cut, Rename or Delete.
	if filesNav.atLocations {
		return nil
	}
	entries := feEntriesOf(config)
	var targets []FileEntry
	if feSelectionCount() > 0 {
		for _, entry := range entries {
			if entry.Name != ".." && feSelectedPaths[entry.Path] {
				targets = append(targets, entry)
			}
		}
		return targets
	}
	if entry, ok := feFocusEntry(config); ok && entry.Name != ".." {
		return []FileEntry{entry}
	}
	return nil
}

// feSelectablePaths lists the entries "Select all" marks: everything except the
// ".." row, which is a way out of the folder rather than a thing in it.
func feSelectablePaths(config *Config) []string {
	if filesNav.atLocations {
		return nil
	}
	var paths []string
	for _, entry := range feEntriesOf(config) {
		if entry.Name == ".." {
			continue
		}
		paths = append(paths, entry.Path)
	}
	return paths
}

func feSelectAll(config *Config) int {
	paths := feSelectablePaths(config)
	feSelectionClear()
	for _, path := range paths {
		feSelectedPaths[path] = true
	}
	return len(paths)
}

// feClipboard is the copy/cut buffer. It holds paths rather than file contents,
// so copying a folder costs nothing until it is pasted.
type feClipboardState struct {
	Paths []string
	Cut   bool
}

var feClipboard feClipboardState

func feClipboardEmpty() bool { return len(feClipboard.Paths) == 0 }

func feClipboardSet(paths []string, cut bool) {
	feClipboard = feClipboardState{Paths: append([]string(nil), paths...), Cut: cut}
}

func feClipboardClear() { feClipboard = feClipboardState{} }

func feItemCountLabel(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// --- Path safety and the operations ---------------------------------------

// feValidateName accepts one plain path component. A new name is the one input
// on this screen that is free text, so it is the one place a separator or a
// ".." could escape the folder.
func feValidateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("the name is empty")
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.New("the name contains a NUL byte")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", errors.New("a name cannot contain a path separator")
	}
	if trimmed == "." || trimmed == ".." {
		return "", errors.New("that name is reserved")
	}
	if len([]rune(trimmed)) > 128 {
		return "", errors.New("the name is too long")
	}
	return trimmed, nil
}

// feResolveEntry resolves a path from the listing to a real file or directory
// inside the Files root. A symlink is followed and the result re-checked, so an
// operation can never be aimed out of the browsable tree through a link.
func feResolveEntry(config *Config, path string) (string, error) {
	root := feRoot(config)
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) == filepath.Clean(root) {
		return "", errors.New("that is the Files root")
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return resolved, nil
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	rootReal := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		rootReal = r
	}
	if !isPathWithin(real, rootReal) {
		return "", errors.New("that link points outside the Files root")
	}
	return real, nil
}

// feFreeTarget names a destination in dir, adding " (2)", " (3)"… when the
// plain name is taken — the way a desktop file manager pastes a second copy
// instead of failing.
func feFreeTarget(dir, name string) (string, error) {
	candidate, err := safeJoinFile(dir, name)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(candidate); statErr != nil {
		return candidate, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i <= 99; i++ {
		next, err := safeJoinFile(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if err != nil {
			return "", err
		}
		if _, statErr := os.Lstat(next); statErr != nil {
			return next, nil
		}
	}
	return "", fmt.Errorf("%q already exists here", name)
}

// feCopyMaxDepth stops a folder that links back on itself from copying until
// the card fills up.
const feCopyMaxDepth = 32

func feCopyTree(src, dst string, visited map[string]bool, depth int) error {
	if depth > feCopyMaxDepth {
		return errors.New("folder is nested too deeply to copy")
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// A link is copied as the file it points at, so the copy is a real tree
		// rather than a new link that may dangle on the other card.
		if resolved, err := filepath.EvalSymlinks(src); err == nil {
			if visited[resolved] {
				return errors.New("folder links back on itself")
			}
			visited[resolved] = true
			src, info, err = resolved, nil, nil
			if info, err = os.Stat(src); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		children, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := feCopyTree(filepath.Join(src, child.Name()), filepath.Join(dst, child.Name()), visited, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	return copyFileStream(src, dst, perm)
}

// feCopyInto copies one entry into dir under its own name.
func feCopyInto(config *Config, src, dir string) (string, error) {
	resolved, err := feResolveEntry(config, src)
	if err != nil {
		return "", err
	}
	if isPathWithin(dir, resolved) {
		return "", errors.New("a folder cannot be copied into itself")
	}
	target, err := feFreeTarget(dir, filepath.Base(resolved))
	if err != nil {
		return "", err
	}
	if err := feCopyTree(resolved, target, map[string]bool{resolved: true}, 0); err != nil {
		return "", err
	}
	return target, nil
}

func feRenameEntry(config *Config, path, newName string) (string, error) {
	name, err := feValidateName(newName)
	if err != nil {
		return "", err
	}
	src, err := feResolveEntry(config, path)
	if err != nil {
		return "", err
	}
	dest, err := safeJoinFile(filepath.Dir(src), name)
	if err != nil {
		return "", err
	}
	if filepath.Clean(dest) == filepath.Clean(src) {
		return dest, nil
	}
	if destInfo, statErr := os.Lstat(dest); statErr == nil {
		// On a case-insensitive card (FAT, exFAT, NTFS) the "existing" file is
		// the one being renamed when only the letter case changes, and
		// "pokemon.gba" → "Pokemon.gba" was refused as already there.
		srcInfo, srcErr := os.Lstat(src)
		if srcErr != nil || !os.SameFile(srcInfo, destInfo) {
			return "", fmt.Errorf("%q already exists here", name)
		}
	}
	if err := os.Rename(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func feDeleteEntry(config *Config, path string) error {
	resolved, err := feResolveEntry(config, path)
	if err != nil {
		return err
	}
	// A link is removed as a link. feResolveEntry follows it (to prove it
	// stays inside the root), and RemoveAll on that answer emptied the whole
	// folder the link pointed at. It is handled before the parent check below,
	// which looks at the link's target: a link to the folder being browsed
	// (or above it) is still just a link in this folder.
	if lexical, err := resolveWithinRoot(feRoot(config), path); err == nil {
		if info, err := os.Lstat(lexical); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return os.Remove(lexical)
		}
	}
	// Never the folder being browsed or one above it: whatever route a parent
	// path takes to get here (the ".." row, a stale mark), a delete is for
	// things in the folder.
	if cur := feCurrentPath(config); cur != "" && isPathWithin(cur, resolved) {
		return errors.New("that folder holds the one you are in")
	}
	if err := os.RemoveAll(resolved); err != nil {
		return err
	}
	return nil
}

func feNewFolder(config *Config, name string) (string, error) {
	clean, err := feValidateName(name)
	if err != nil {
		return "", err
	}
	dir := feCurrentPath(config)
	dest, err := safeJoinFile(dir, clean)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		return "", fmt.Errorf("%q already exists here", clean)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return "", err
	}
	return dest, nil
}

// fePaste copies (or moves) the clipboard into the folder being browsed. A move
// is a copy followed by a delete of the source, so a cut across two cards is
// still safe when the second one is unplugged mid-way: the source is only
// removed after the copy succeeded.
func fePaste(config *Config) (int, error) {
	if feClipboardEmpty() {
		return 0, errors.New("nothing is copied yet")
	}
	dir := feCurrentPath(config)
	done := 0
	var lastErr error
	for _, src := range feClipboard.Paths {
		if _, err := feCopyInto(config, src, dir); err != nil {
			lastErr = err
			continue
		}
		if feClipboard.Cut {
			if err := feDeleteEntry(config, src); err != nil {
				lastErr = err
			}
		}
		done++
	}
	if done > 0 && feClipboard.Cut {
		// The clipboard is spent once its entries have moved.
		feClipboardClear()
	}
	if done == 0 && lastErr != nil {
		return 0, lastErr
	}
	return done, lastErr
}

// feFocusPath selects the row holding path, so an operation that creates or
// renames something leaves the cursor on it instead of on the old row number.
func feFocusPath(config *Config, path string) {
	for i, entry := range feEntriesOf(config) {
		if entry.Path == path {
			focusedFileIndex = i
			return
		}
	}
}

// --- Listing helpers used by the renderer ---------------------------------

// feDirTotals reports how many entries are listed (excluding "..") and how many
// bytes the files in the folder take. It is what the breadcrumb bar's right side
// shows: a file manager that never says how big a folder is makes the user open
// it to find out.
func feDirTotals(entries []FileEntry) (int, int64) {
	count := 0
	var bytes int64
	for _, entry := range entries {
		if entry.Name == ".." {
			continue
		}
		count++
		if !entry.IsDir {
			bytes += entry.Size
		}
	}
	return count, bytes
}

// feEntryTitle is the name a row reads out. The ".." entry is not a file
// called "..", so it is spelled: a listing whose way out is punctuation is a
// listing that has to be explained.
func feEntryTitle(entry FileEntry) string {
	if entry.Name == ".." {
		return "Parent folder"
	}
	return entry.Name
}

// feEntryMeta is the right-aligned half of a row: the size and the day the file
// was written, or the word that says this row is a folder. The parent row names
// the folder it goes to, because "back to roms" is the fact the user needs.
func feEntryMeta(entry FileEntry) string {
	if entry.Name == ".." {
		parent := filepath.Base(entry.Path)
		if parent == "" || parent == "." || parent == string(filepath.Separator) {
			return "one level up"
		}
		return "back to " + parent
	}
	if entry.IsDir {
		return "Folder"
	}
	if entry.Mod.IsZero() {
		return humanBytes(entry.Size)
	}
	return humanBytes(entry.Size) + " · " + entry.Mod.Format("Jan 2")
}

// feVolumeFree is the space left on the card holding the open folder. It is read
// once per listing instead of once per frame: statfs is a syscall, and the
// render loop has no business paying for one. The bar showing it is what turns
// "copy 90 MiB of video here" into a decision rather than a surprise.
var feVolumeFree struct {
	path  string
	bytes int64
	ok    bool
}

func feRefreshFreeSpace(path string) {
	feVolumeFree.path = path
	free, err := freeBytes(path)
	if err != nil {
		feVolumeFree.bytes = 0
		feVolumeFree.ok = false
		return
	}
	feVolumeFree.bytes = free
	feVolumeFree.ok = true
}

// feFreeSpaceLabel is the tail of the totals line, empty on a platform without
// statfs (or a folder that could not be read).
func feFreeSpaceLabel(path string) string {
	if !feVolumeFree.ok || feVolumeFree.path != path {
		return ""
	}
	return humanBytes(feVolumeFree.bytes) + " free"
}

// feEntryBadge is the three-letter marker in a row's leading chip: the list is
// scanned rather than read, so what a file is has to be visible before its name
// is.
func feEntryBadge(entry FileEntry) (string, sdl.Color) {
	if entry.Name == ".." {
		return "UP", ColorTextTertiary()
	}
	if entry.IsDir {
		return "DIR", sdl.Color{R: 255, G: 214, B: 120, A: 255}
	}
	ext := strings.ToLower(filepath.Ext(entry.Name))
	switch {
	case isImageExtension(ext):
		return "IMG", sdl.Color{R: 130, G: 205, B: 255, A: 255}
	case isMusicPlaylistFile(entry.Name):
		return "PL", sdl.Color{R: 255, G: 214, B: 120, A: 255}
	case isAudioFile(entry.Name):
		return "AUD", sdl.Color{R: 167, G: 235, B: 160, A: 255}
	case isMediaFile(entry.Name) || hasMediaExtension(entry.Path):
		return "VID", ColorTextAccent()
	case ext == ".zip" || ext == ".tar" || ext == ".gz" || ext == ".xz" || ext == ".7z" || ext == ".rar":
		return "ZIP", sdl.Color{R: 224, G: 176, B: 255, A: 255}
	case ext == ".pdf":
		return "PDF", sdl.Color{R: 255, G: 141, B: 141, A: 255}
	case ext == ".epub" || ext == ".mobi" || ext == ".azw3":
		return "BOOK", sdl.Color{R: 255, G: 141, B: 141, A: 255}
	case ext == ".sh" || ext == ".bash":
		return "SH", sdl.Color{R: 167, G: 235, B: 160, A: 255}
	case ext == ".html" || ext == ".htm" || ext == ".js" || ext == ".css" || ext == ".json":
		return "WEB", ColorTextAccent()
	case ext == ".apk":
		return "APK", sdl.Color{R: 167, G: 235, B: 160, A: 255}
	case ext == ".txt" || ext == ".md" || ext == ".log" || ext == ".cfg" || ext == ".ini" || ext == ".xml" || ext == ".yaml" || ext == ".yml":
		return "TXT", ColorTextSecondary()
	}
	return "FILE", ColorTextSecondary()
}

// --- Breadcrumbs -----------------------------------------------------------

// feCrumb is one clickable segment of the path above the list. Path is what a
// click navigates to; an elided marker carries an empty Path.
type feCrumb struct {
	Label string
	Path  string
}

const (
	feCrumbSep    = "›"
	feCrumbPad    = int32(9)
	feCrumbSepGap = int32(8)
)

// feRootLabel names the top of the browsable tree. The Files root is often the
// SD card mount, whose base name ("SDCARD", "sdcard") is what the user thinks
// of as "the card", so it is used rather than a generic word.
func feRootLabel(root string) string {
	clean := filepath.Clean(root)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		if volume := filepath.VolumeName(clean); volume != "" {
			return volume
		}
		// The filesystem root, reached from the Locations list.
		return "System"
	}
	return base
}

// feCrumbs builds the path from the Files root down to the folder on screen.
// The root is always the first crumb so there is one click back to the top
// whatever the depth.
func feCrumbs(config *Config) []feCrumb {
	return filesCrumbsFor(feRoot(config), feCurrentPath(config))
}

// filesCrumbCache keeps the crumbs of the listing on screen: the bar is drawn
// every frame, but its folder only changes when a folder is listed.
var filesCrumbCache struct {
	root, path string
	crumbs     []feCrumb
}

// filesCrumbsListed is feCrumbs for the frame. It builds the crumbs from the
// folder and root the last listing recorded, so drawing the bar costs no disk
// access (feCurrentPath validates fe_path against the disk on every call).
func filesCrumbsListed(config *Config) []feCrumb {
	root, path := filesNav.listedRoot, filesNav.listedPath
	if root == "" || path == "" {
		return feCrumbs(config)
	}
	if filesCrumbCache.crumbs == nil || filesCrumbCache.root != root || filesCrumbCache.path != path {
		filesCrumbCache.root, filesCrumbCache.path = root, path
		filesCrumbCache.crumbs = filesCrumbsFor(root, path)
	}
	return filesCrumbCache.crumbs
}

// filesCrumbsFor builds the crumbs from root down to current.
func filesCrumbsFor(root, current string) []feCrumb {
	crumbs := []feCrumb{{Label: feRootLabel(root), Path: root}}
	rel, err := filepath.Rel(root, current)
	if err != nil || rel == "." || rel == "" {
		return crumbs
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	path := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		path = filepath.Join(path, part)
		crumbs = append(crumbs, feCrumb{Label: part, Path: path})
	}
	return crumbs
}

// feCrumbFit drops crumbs from the middle until the line fits, marking the gap
// with an ellipsis crumb. The last crumbs are the ones that matter — they say
// which folder is open — and the first is the way back to the root, so the
// middle is what gives way.
func feCrumbFit(font *ttf.Font, crumbs []feCrumb, avail int32) []feCrumb {
	if font == nil || len(crumbs) == 0 {
		return crumbs
	}
	// Each crumb is a chip with padding, and the chips are joined by chevrons;
	// the fit has to count both or the last crumb runs under the totals.
	measure := func(list []feCrumb) int32 {
		var total int32
		for i, crumb := range list {
			if i > 0 {
				w, _, _ := font.SizeUTF8(feCrumbSep)
				total += int32(w) + feCrumbSepGap
			}
			w, _, _ := font.SizeUTF8(crumb.Label)
			total += int32(w) + 2*feCrumbPad
		}
		return total
	}
	if measure(crumbs) <= avail || len(crumbs) <= 2 {
		return crumbs
	}
	// The root and the open folder are fixed; the crumbs in the middle are
	// added back from the end until the line fills, and whatever is left out is
	// covered by one ellipsis crumb. Growing towards the tail is what keeps the
	// elbows nearest the open folder, which are the ones worth a press.
	elided := feCrumb{Label: "…"}
	working := []feCrumb{crumbs[0], elided, crumbs[len(crumbs)-1]}
	for tail := len(crumbs) - 2; tail > 1; tail-- {
		candidate := append([]feCrumb{crumbs[0], elided}, crumbs[tail:]...)
		if measure(candidate) > avail {
			break
		}
		working = candidate
	}
	if len(working) == len(crumbs) {
		// Every crumb fit behind the ellipsis, so there is no gap to mark.
		return crumbs
	}
	return working
}

// feCrumbHits and feSortChipRect are the breadcrumb bar's click targets, set
// while it draws and read by the click handler.
var feCrumbHits []struct {
	Path string
	Rect sdl.Rect
}

var feSortChipRect sdl.Rect

// feGoToPath browses a path picked from the breadcrumb bar. It goes through
// feCurrentPath's validation on the next read, so a stale crumb (a folder
// deleted from under the bar) falls back to the root instead of listing
// somewhere else.
func feGoToPath(config *Config, path string) bool {
	if config == nil || strings.TrimSpace(path) == "" {
		return false
	}
	dir, err := existingDirWithin(feRoot(config), path)
	if err != nil {
		showToast("Cannot open that folder: "+filesFriendlyError(err), ToastError())
		return false
	}
	filesNavigate(config, dir, "")
	PlayNavSound()
	return true
}

// filesLocationsCrumbRect is the "Locations" chip at the head of the breadcrumb
// bar: the click target that steps up to the Locations list from any depth.
var filesLocationsCrumbRect sdl.Rect

// handleFilesChromeMouse handles clicks on the breadcrumb bar and the sort chip,
// which are drawn above the list element and would otherwise fall through as
// clicks on empty space.
func handleFilesChromeMouse(mx, my int32, config *Config) bool {
	if !feSceneActive(config) {
		return false
	}
	if filesLocationsCrumbRect.W > 0 && pointInRect(mx, my, filesLocationsCrumbRect) {
		if !filesNav.atLocations {
			filesShowLocations(config)
		}
		return true
	}
	for _, hit := range feCrumbHits {
		if pointInRect(mx, my, hit.Rect) {
			return feGoToPath(config, hit.Path)
		}
	}
	if feSortChipRect.W > 0 && pointInRect(mx, my, feSortChipRect) {
		feCycleSort(config)
		showToast("Sorted by "+strings.ToLower(feSortLabel(feSortModeOf(config))), ToastInfo())
		return true
	}
	return false
}

func feSceneActive(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == "FileExplorer"
}

// feToggleSelectionOnFocused marks or unmarks the row under the cursor and
// steps down one row, so X can be used repeatedly to mark a run of files
// without touching the D-pad.
func feToggleSelectionOnFocused(config *Config) bool {
	entry, ok := feFocusEntry(config)
	if !ok {
		return false
	}
	// The ".." row is the way out, not a thing in the folder (it is the
	// parent), so X steps over it without marking it; a Locations row is a
	// whole drive and is never marked either.
	selected := false
	if entry.Name != ".." && !filesNav.atLocations {
		selected = feSelectionToggle(entry.Path)
	}
	if selected {
		PlayActivateSound()
	} else {
		PlayNavSound()
	}
	if focusedFileIndex < len(feEntriesOf(config))-1 {
		focusedFileIndex++
	}
	return true
}

// --- Rendering ------------------------------------------------------------

// renderFilesBreadcrumbBar draws the band above the list: every path segment as
// a target, the folder's totals, and the sort chip on the right. It replaces the
// single line of path text the screen used to show — the segments are what make
// a deep folder one press from the top instead of five B presses.
func renderFilesBreadcrumbBar(renderer *sdl.Renderer, config *Config, element Element, elemW int32, font *ttf.Font) {
	if renderer == nil || font == nil {
		return
	}
	bar := sdl.Rect{X: element.X, Y: element.Y - 52, W: elemW, H: 44}
	// A glass strip, the same surface as the list panel under it.
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, glassControlRadius, glassFill())
	strokeRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, glassControlRadius, 1, glassEdge())
	lineH := int32(font.Height())

	// Sort chip, right-aligned: the order is a per-folder preference, and the
	// chip is both its display and the way to change it. It names itself
	// ("Sort: Name") because a lone "Name" in the corner does not say what it
	// would do if pressed.
	sortText := "Sort: " + feSortLabel(feSortModeOf(config))
	sortW, _, _ := font.SizeUTF8(sortText)
	sortTextW := int32(sortW)
	chip := sdl.Rect{X: bar.X + bar.W - sortTextW - 28, Y: bar.Y + 9, W: sortTextW + 22, H: bar.H - 18}
	feSortChipRect = chip
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 7, glassChipFill())
	strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, 7, 1, glassA(26))
	renderText(renderer, config, font, sortText, ColorTextSecondary(), chip.X+11, chip.Y+(chip.H-lineH)/2)
	// The listing's cursor is remembered per folder from here, once per frame,
	// so coming back to Files (or up from a sub-folder) finds the same row.
	filesNoteFocus(config)
	filesDrawListNotice(renderer, config, element, elemW)

	// Totals between the crumbs and the chip: how much is in this folder, and
	// how much room is left on the card under it.
	totals := feFolderTotalsLabel(config)
	totalsW, _, _ := font.SizeUTF8(totals)
	totalsW32 := int32(totalsW)
	totalsInk := ColorTextTertiary()
	if feSelectionCount() > 0 || chatPickActive() {
		totalsInk = ColorTextAccent()
	}
	totalsX := chip.X - totalsW32 - 20
	if totalsX <= bar.X+140 {
		totalsX = chip.X
		totals = ""
	}
	if totals != "" {
		renderText(renderer, config, font, totals, totalsInk, totalsX, bar.Y+(bar.H-lineH)/2)
	}

	// The crumbs get what is left, with the middle giving way first. Each one
	// is its own chip with its own hit box, so a deep folder is one press from
	// the top instead of five B presses.
	chipH := bar.H - 18
	chipY := bar.Y + 9
	textY := chipY + (chipH-lineH)/2
	x := bar.X + 10
	feCrumbHits = feCrumbHits[:0]

	// "Locations" leads the bar: one press from any depth back to the list of
	// places (the card, Home, the system root, every drive). On the list itself
	// it is the crumb that reads as "here".
	drawCrumb := func(label string, current bool) int32 {
		labelW, _ := measureText(font, label)
		crumbW := labelW + 2*feCrumbPad
		ink := ColorTextSecondary()
		fill := glassA(10)
		edge := glassA(22)
		if current {
			// The folder on screen is the one that reads as "here".
			ink = ColorTextPrimary()
			fill = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 52}
			edge = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 150}
		}
		fillRoundedRect(renderer, x, chipY, crumbW, chipH, 7, fill)
		strokeRoundedRect(renderer, x, chipY, crumbW, chipH, 7, 1, edge)
		renderText(renderer, config, font, label, ink, x+feCrumbPad, textY)
		return crumbW
	}
	const locationsLabel = "Locations"
	locW := drawCrumb(locationsLabel, filesNav.atLocations)
	filesLocationsCrumbRect = sdl.Rect{X: x, Y: chipY, W: locW, H: chipH}
	x += locW
	if filesNav.atLocations {
		return
	}
	sepW, _ := measureText(font, feCrumbSep)

	// The crumbs get what is left, with the middle giving way first. Each one
	// is its own chip with its own hit box, so a deep folder is one press from
	// the top instead of five B presses.
	avail := totalsX - x - sepW - feCrumbSepGap - 24
	if avail < 160 {
		avail = 160
	}
	crumbs := feCrumbFit(font, filesCrumbsListed(config), avail)
	for i, crumb := range crumbs {
		renderText(renderer, config, font, feCrumbSep, ColorTextTertiary(), x+feCrumbSepGap/2, textY)
		x += sepW + feCrumbSepGap
		crumbW := drawCrumb(crumb.Label, i == len(crumbs)-1)
		if crumb.Path != "" {
			feCrumbHits = append(feCrumbHits, struct {
				Path string
				Rect sdl.Rect
			}{Path: crumb.Path, Rect: sdl.Rect{X: x, Y: chipY, W: crumbW, H: chipH}})
		}
		x += crumbW
	}
}

// filesDrawListNotice explains a listing that has nothing to open under its
// ".." row: a folder the app may not read, an empty folder, or one whose only
// entries are hidden. The ".." row stays above it, so the way back is on
// screen while the reason is read.
func filesDrawListNotice(renderer *sdl.Renderer, config *Config, element Element, elemW int32) {
	if filesNav.atLocations {
		return
	}
	// A listing with no rows at all (an empty filesystem root) gets the list's
	// own empty state; this covers the folder whose one row is the way back.
	entries := feEntriesOf(config)
	if len(entries) != 1 || entries[0].Name != ".." {
		return
	}
	title, body := "", ""
	switch {
	case filesNav.listErr != nil:
		title, body = filesListErrorText(filesNav.listErr)
	case filesNav.hiddenCount > 0:
		title = "Only hidden items here"
		body = fmt.Sprintf("%s hidden · Y → Show hidden files", feItemCountLabel(filesNav.hiddenCount))
	default:
		title, body = "This folder is empty", hintSentence("Press %s to go back up", "B", "Back")
	}
	top := element.Y + dynamicListListOffset(element) + dynamicListRowH + 8
	elemH := getElementHeight(element, 500)
	rect := sdl.Rect{X: element.X, Y: top, W: elemW, H: element.Y + elemH - top - 8}
	if rect.H < 80 {
		return
	}
	if filesNav.listErr != nil {
		UIErrorState(renderer, config, rect, "@warning", title, body)
		return
	}
	UIEmptyState(renderer, config, rect, "@folder", title, body)
}

// feFolderTotalsLabel is the bar's middle reading: what this folder holds, how
// many rows are marked, and how much room is left on the card beneath it.
func feFolderTotalsLabel(config *Config) string {
	// Picking files for the chat: the bar says so, and for which tab.
	if pick := chatPickLabel(); pick != "" {
		return pick
	}
	if filesNav.atLocations {
		n := len(filesNav.locations)
		if n == 1 {
			return "1 place"
		}
		return fmt.Sprintf("%d places", n)
	}
	count, bytes := feDirTotals(feEntriesOf(config))
	label := fmt.Sprintf("%d items · %s", count, humanBytes(bytes))
	if count == 0 {
		label = "empty folder"
	} else if count == 1 {
		label = "1 item · " + humanBytes(bytes)
	}
	if filesNav.hiddenCount > 0 {
		label += fmt.Sprintf(" · %d hidden", filesNav.hiddenCount)
	}
	if free := feFreeSpaceLabel(feDisplayPath(config)); free != "" {
		label += " · " + free
	}
	if selected := feSelectionCount(); selected > 0 {
		label = fmt.Sprintf("%d selected · %s", selected, label)
	}
	return label
}

// renderFilesRow draws one entry: a type chip, the name, then the size and the
// date on the right. A marked row is tinted and its chip turns into a tick, so a
// selection reads at a glance without a legend.
func renderFilesRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, entry FileEntry, rowX, rowY, rowW, rowH int32, focused bool) {
	if renderer == nil || font == nil {
		return
	}
	lineH := int32(font.Height())
	textY := rowY + (rowH-lineH)/2
	selected := feSelectionHas(entry.Path)

	if selected {
		// The tint is the mark that survives the row being scrolled half out of
		// view, where a tick at one end would not be noticed.
		fillRoundedRect(renderer, rowX, rowY, rowW, rowH, 8,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 44})
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.FillRect(&sdl.Rect{X: rowX + 2, Y: rowY + 7, W: 3, H: rowH - 14})
	}

	_, badgeInk := feEntryBadge(entry)
	chipW := int32(46)
	chipH := int32(20)
	chipX := rowX + 12
	chipY := rowY + (rowH-chipH)/2
	loc, isLocation := filesLocationOf(entry)
	switch {
	case isLocation:
		filesDrawIconBadge(renderer, chipX, rowY, chipW, rowH, filesLocationIcon(loc), glassAccent())
	case selected:
		// The chip is what the eye lands on first, so a marked row gets the tick
		// in its place instead of a second mark elsewhere on the row.
		fillRoundedRect(renderer, chipX, chipY, chipW, chipH, 5,
			sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 235})
		drawFeTick(renderer, chipX+chipW/2, chipY+chipH/2, sdl.Color{R: 12, G: 16, B: 26, A: 255})
	case entry.IsDir && entry.Name != "..":
		fillRoundedRect(renderer, chipX, chipY, chipW, chipH, 5, sdl.Color{R: badgeInk.R, G: badgeInk.G, B: badgeInk.B, A: 40})
		drawFeFolderIcon(renderer, chipX, chipY, badgeInk)
	default:
		if tex := feFileThumbnail(renderer, entry); tex != nil {
			// A picture and a video frame replace the type chip in the same slot:
			// the thumbnail is what makes a row recognisable before it is opened,
			// and keeping the chip's footprint leaves the name column where it was.
			drawFeThumbnail(renderer, entry, tex, chipX, rowY+(rowH-feThumbH)/2)
		} else {
			// Every other row carries its type as a glyph in a tinted glass
			// badge (the Home icon set), not three letters squeezed into a chip.
			filesDrawIconBadge(renderer, chipX, rowY, chipW, rowH, filesEntryIcon(entry), badgeInk)
		}
	}

	meta := feEntryMeta(entry)
	if isLocation {
		// A long mount path gives way before the place's name does.
		meta = ellipsize(font, filesLocationMeta(loc), rowW*45/100)
	}
	metaW := int32(0)
	if meta != "" {
		metaW, _ = measureText(font, meta)
		filesDrawPlainText(renderer, config, font, meta, ColorTextTertiary(), rowX+rowW-metaW-16, textY)
	}

	nameX := chipX + chipW + 12
	nameAvail := rowX + rowW - metaW - 28 - nameX
	if nameAvail < 80 {
		nameAvail = 80
	}
	nameInk := ColorTextPrimary()
	if entry.Name == ".." {
		nameInk = ColorTextSecondary()
	}
	// File names are the user's text: drawn as they are, never with the
	// $variable substitution renderText applies to the app's own labels.
	filesDrawPlainText(renderer, config, font, ellipsize(font, feEntryTitle(entry), nameAvail), nameInk, nameX, textY)
}

// filesDrawPlainText draws text that came from the user's files (a name, a
// line of a document) exactly as it is: renderText would expand "$name" tokens
// against the config's variables, which turns a script line or a file called
// "$HOME.txt" into something else.
func filesDrawPlainText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) (int32, int32) {
	if renderer == nil || font == nil || text == "" {
		return 0, 0
	}
	return drawTextRun(renderer, config, font, sanitizeCached(font, text), color, x, y)
}

// filesDrawIconBadge paints a row's type glyph in a glass badge tinted with the
// type's colour, centred in the slot the type chip and the thumbnails use, so
// the name column lines up whatever leads the row.
func filesDrawIconBadge(renderer *sdl.Renderer, slotX, rowY, slotW, rowH int32, icon string, tint sdl.Color) {
	if renderer == nil {
		return
	}
	size := rowH - 8
	if size > 30 {
		size = 30
	}
	if size < 16 {
		size = 16
	}
	badge := sdl.Rect{X: slotX + (slotW-size)/2, Y: rowY + (rowH-size)/2, W: size, H: size}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, 8, sdl.Color{R: tint.R, G: tint.G, B: tint.B, A: 52})
	strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, 8, 1, sdl.Color{R: tint.R, G: tint.G, B: tint.B, A: 110})
	glyph := size * 2 / 3
	DrawHomeIcon(renderer, icon, sdl.Rect{X: badge.X + (size-glyph)/2, Y: badge.Y + (size-glyph)/2, W: glyph, H: glyph}, false)
}

// filesEntryIcon names the Home-icon glyph for a row's type.
func filesEntryIcon(entry FileEntry) string {
	if entry.Name == ".." {
		return "folder-up"
	}
	if entry.IsDir {
		return "folder"
	}
	ext := strings.ToLower(filepath.Ext(entry.Name))
	switch {
	case isImageExtension(ext):
		return "image"
	case isMusicPlaylistFile(entry.Name):
		return "music"
	case isAudioFile(entry.Name):
		return "music"
	case isMediaFile(entry.Name) || hasMediaExtension(entry.Path):
		return "video"
	case ext == ".pdf":
		return "pdf"
	case ext == ".epub" || ext == ".mobi" || ext == ".azw3":
		return "text"
	case ext == ".zip" || ext == ".tar" || ext == ".gz" || ext == ".xz" || ext == ".7z" || ext == ".rar":
		if filesROMSystem(entry.Path) != "" {
			return "gamepad"
		}
		return "packages"
	case ext == ".sh" || ext == ".bash" || ext == ".bat" || ext == ".cmd" || ext == ".ps1":
		return "terminal"
	case ext == ".html" || ext == ".htm" || ext == ".js" || ext == ".css" || ext == ".json" || ext == ".go" || ext == ".py" || ext == ".c" || ext == ".h" || ext == ".lua":
		return "code"
	case ext == ".apk" || ext == ".exe" || ext == ".msi":
		return "apps"
	case filesROMSystem(entry.Path) != "":
		return "gamepad"
	}
	return "text"
}

// drawFeFolderIcon paints a folder in the 46×20 type-chip slot.
func drawFeFolderIcon(renderer *sdl.Renderer, chipX, chipY int32, ink sdl.Color) {
	shade := sdl.Color{R: uint8(int(ink.R) * 3 / 4), G: uint8(int(ink.G) * 3 / 4), B: uint8(int(ink.B) * 3 / 4), A: ink.A}
	x, y := chipX+10, chipY+1
	fillRoundedRect(renderer, x, y, 10, 6, 2, shade)
	fillRoundedRect(renderer, x, y+4, 26, 15, 2, shade)
	fillRoundedRect(renderer, x, y+8, 26, 11, 2, ink)
}

// drawFeTick draws the selection check with lines: Inter has no check glyph, and
// a typed box would read as a missing character.
func drawFeTick(renderer *sdl.Renderer, cx, cy int32, ink sdl.Color) {
	renderer.SetDrawColor(ink.R, ink.G, ink.B, ink.A)
	// A short descender then a long rise, which is the shape a check reads as
	// even at this size.
	x := cx - 6
	y := cy - 1
	for i := int32(0); i < 5; i++ {
		renderer.DrawLine(x+i, y+i, x+i+1, y+i+1)
	}
	x += 4
	y += 4
	for i := int32(0); i < 6; i++ {
		renderer.DrawLine(x+i, y-i, x+i+1, y-i-1)
	}
}

// --- Action sheet ---------------------------------------------------------

type feActionKind int

const (
	feActNewFolder feActionKind = iota
	feActEdit
	feActRename
	feActCopy
	feActCut
	feActPaste
	feActDelete
	feActSelectAll
	feActSort
	feActConnect
	feActRemote
	feActHidden
	feActLocations
	feActSystemOpen
	feActSendChat
)

// feAction is one row of the sheet. Detail is the line's right-hand summary
// (what the action applies to), because "Delete" on seven marked files and
// "Delete" on one are different decisions.
type feAction struct {
	Kind      feActionKind
	Label     string
	Detail    string
	Enabled   bool
	Dangerous bool
}

func feActions(config *Config) []feAction {
	hiddenDetail := "Off"
	if filesShowHidden(config) {
		hiddenDetail = "On"
	}
	remoteDetail := "not connected"
	remoteEnabled := feRemoteConnected()
	if feRemotePanel.tgt.Host != "" {
		remoteDetail = feRemotePanel.tgt.key()
		remoteEnabled = true
	}
	if filesNav.atLocations {
		// The Locations rows are whole drives and the card: nothing here can be
		// renamed, cut or deleted, so the sheet offers what still applies.
		return []feAction{
			{Kind: feActHidden, Label: "Show hidden files", Detail: hiddenDetail, Enabled: true},
			{Kind: feActConnect, Label: "Connect to device", Detail: feRemoteAddressHint(), Enabled: true},
			{Kind: feActRemote, Label: "Remote files", Detail: remoteDetail, Enabled: remoteEnabled},
		}
	}
	targets := feSelectionTargets(config)
	listed := filesNav.listedPath
	if listed == "" {
		listed = feCurrentPath(config)
	}
	folder := filepath.Base(listed)
	newFolderDetail := "in " + folder
	if filesNav.systemFolder {
		newFolderDetail = "system folder · asks first"
	}
	renameDetail := "nothing selected"
	renameEnabled := false
	if len(targets) == 1 && targets[0].Name != ".." {
		renameDetail = targets[0].Name
		renameEnabled = true
	} else if len(targets) > 1 {
		renameDetail = "one at a time"
	}
	targetDetail := feItemCountLabel(len(targets))
	pasteDetail := "nothing copied"
	if !feClipboardEmpty() {
		pasteDetail = feItemCountLabel(len(feClipboard.Paths)) + " here"
		if feClipboard.Cut {
			pasteDetail = "move " + pasteDetail
		} else {
			pasteDetail = "copy " + pasteDetail
		}
	}
	editDetail := "pick one text file"
	editEnabled := false
	if len(targets) == 1 && targets[0].Name != ".." {
		switch {
		case targets[0].IsDir:
			editDetail = "folders cannot be edited"
		case fileEditorCanEdit(targets[0].Path) || !filesHasDedicatedViewer(targets[0].Path):
			// Anything without a viewer of its own opens in the editor anyway;
			// the sheet's row is the way to the text of a script or a page too.
			editDetail = targets[0].Name
			editEnabled = true
		default:
			editDetail = "opens in its own viewer"
		}
	}
	// "Send to chat" takes the marked files (or the one under the cursor) to
	// the chat's tray; folders are not sent.
	sendFiles := 0
	for _, t := range targets {
		if !t.IsDir {
			sendFiles++
		}
	}
	sendDetail := "pick files"
	if sendFiles > 0 {
		sendDetail = feItemCountLabel(sendFiles)
	}
	if chatPickActive() {
		sendDetail += " · for " + chatTabShortName(chatPick.tab)
	}
	actions := []feAction{
		{Kind: feActNewFolder, Label: "New folder", Detail: newFolderDetail, Enabled: true},
		{Kind: feActEdit, Label: "Edit text", Detail: editDetail, Enabled: editEnabled},
		{Kind: feActSendChat, Label: "Send to chat", Detail: sendDetail, Enabled: sendFiles > 0},
		{Kind: feActRename, Label: "Rename", Detail: renameDetail, Enabled: renameEnabled},
		{Kind: feActCopy, Label: "Copy", Detail: targetDetail, Enabled: len(targets) > 0},
		{Kind: feActCut, Label: "Cut", Detail: targetDetail, Enabled: len(targets) > 0},
		{Kind: feActPaste, Label: "Paste", Detail: pasteDetail, Enabled: !feClipboardEmpty()},
		{Kind: feActDelete, Label: "Delete", Detail: targetDetail, Enabled: len(targets) > 0, Dangerous: true},
		{Kind: feActSelectAll, Label: "Select all", Detail: feItemCountLabel(len(feSelectablePaths(config))), Enabled: true},
		{Kind: feActSort, Label: "Sort", Detail: feSortLabel(feSortModeOf(config)), Enabled: true},
		{Kind: feActHidden, Label: "Show hidden files", Detail: hiddenDetail, Enabled: true},
		{Kind: feActLocations, Label: "All locations", Detail: "card, Home, system, drives", Enabled: true},
	}
	if filesSystemOpenAvailable() {
		systemDetail := "pick one file"
		systemEnabled := false
		if len(targets) == 1 && targets[0].Name != ".." {
			systemDetail = targets[0].Name
			systemEnabled = true
		}
		actions = append(actions, feAction{Kind: feActSystemOpen, Label: "Open with Windows", Detail: systemDetail, Enabled: systemEnabled})
	}
	return append(actions,
		feAction{Kind: feActConnect, Label: "Connect to device", Detail: feRemoteAddressHint(), Enabled: true},
		feAction{Kind: feActRemote, Label: "Remote files", Detail: remoteDetail, Enabled: remoteEnabled},
	)
}

// feArmWindow is how long an armed destructive press stays armed. It matches
// the other two-press confirmations in the shell (the Wi-Fi picker's forgotten
// network, the cookie picker's site).
const feArmWindow = 5 * time.Second

type feSheetState struct {
	open    bool
	cursor  int
	scroll  int
	armed   string
	armedAt time.Time
}

var feSheet feSheetState

var feSheetRowHits []struct {
	index int
	rect  sdl.Rect
}

var feSheetRect sdl.Rect

func openFeSheet(config *Config) {
	feSheet = feSheetState{open: true}
	feSheetRowHits = nil
	feSheetRect = sdl.Rect{}
	PlayActivateSound()
}

// feSheetForget clears the sheet and the row rects it was clicked through,
// without a sound. Closing it by hand and leaving the Files scene both go
// through here, so a row from the folder that was left cannot answer a press.
func feSheetForget() {
	feSheet = feSheetState{}
	feSheetRowHits = nil
	feSheetRect = sdl.Rect{}
}

func closeFeSheet() {
	if !feSheet.open {
		return
	}
	feSheetForget()
	PlayBackSound()
}

func feSheetArmed(kind string) bool {
	return feSheet.armed == kind && !feSheet.armedAt.IsZero() && time.Since(feSheet.armedAt) <= feArmWindow
}

func feSheetMove(delta int) {
	actions := feActions(appConfig)
	if len(actions) == 0 {
		return
	}
	feSheet.cursor += delta
	if feSheet.cursor < 0 {
		feSheet.cursor = len(actions) - 1
	}
	if feSheet.cursor >= len(actions) {
		feSheet.cursor = 0
	}
	// Moving away from a row disarms it: the second press has to be on the row
	// the user is looking at.
	feSheet.armed = ""
	PlayNavSound()
}

// feSheetActivate runs the highlighted row. Delete arms on the first press and
// acts on the second; everything else acts straight away.
func feSheetActivate(config *Config) {
	actions := feActions(config)
	if feSheet.cursor < 0 || feSheet.cursor >= len(actions) {
		return
	}
	action := actions[feSheet.cursor]
	if !action.Enabled {
		showToast(action.Label+" is not available here", ToastInfo())
		return
	}
	switch action.Kind {
	case feActNewFolder:
		// In a system folder every change asks twice (files_locations.go).
		if !filesSystemConfirm(action.Label) {
			return
		}
		filesKeyboard = filesEditNewFolder
		filesInputBuffer = ""
		closeFeSheetKeepKeyboard()
		vkOpenFilesInput(filesEditNewFolder, "")
	case feActEdit:
		targets := feSelectionTargets(config)
		if len(targets) != 1 {
			showToast("Edit one file at a time", ToastInfo())
			return
		}
		if !fileEditorOpen(targets[0].Path) {
			return
		}
		closeFeSheetKeepKeyboard()
	case feActRename:
		targets := feSelectionTargets(config)
		if len(targets) != 1 {
			showToast("Rename works on one item at a time", ToastInfo())
			return
		}
		if !filesSystemConfirm(action.Label) {
			return
		}
		filesRenamePath = targets[0].Path
		closeFeSheetKeepKeyboard()
		vkOpenFilesInput(filesEditRename, targets[0].Name)
	case feActCopy:
		targets := feSelectionTargets(config)
		paths := make([]string, 0, len(targets))
		for _, t := range targets {
			paths = append(paths, t.Path)
		}
		feClipboardSet(paths, false)
		PlayActivateSound()
		showToast("Copied "+feItemCountLabel(len(paths))+" — Paste into a folder", ToastSuccess())
	case feActCut:
		// A cut removes the originals when it is pasted, so taking files out of
		// a system folder is confirmed here, where the folder is on screen.
		if !filesSystemConfirm(action.Label) {
			return
		}
		targets := feSelectionTargets(config)
		paths := make([]string, 0, len(targets))
		for _, t := range targets {
			paths = append(paths, t.Path)
		}
		feClipboardSet(paths, true)
		PlayActivateSound()
		showToast("Cut "+feItemCountLabel(len(paths))+" — Paste where it belongs", ToastSuccess())
	case feActPaste:
		if !filesSystemConfirm(action.Label) {
			return
		}
		done, err := fePaste(config)
		switch {
		case err != nil && done == 0:
			showToast("Cannot paste: "+err.Error(), ToastError())
		case err != nil:
			feListDirectory(config)
			showToast(fmt.Sprintf("Pasted %d, one failed: %v", done, err), ToastWarn())
		default:
			feListDirectory(config)
			showToast("Pasted "+feItemCountLabel(done), ToastSuccess())
		}
	case feActDelete:
		if !feSheetArmed("delete") {
			feSheet.armed = "delete"
			feSheet.armedAt = time.Now()
			PlayNavSound()
			prompt := "Press again to delete "
			if filesNav.systemFolder {
				prompt = "System folder — press again to delete "
			}
			showToast(prompt+feItemCountLabel(len(feSelectionTargets(config))), ToastWarn())
			return
		}
		feSheet.armed = ""
		targets := feSelectionTargets(config)
		failed := 0
		var lastErr error
		for _, target := range targets {
			if err := feDeleteEntry(config, target.Path); err != nil {
				failed++
				lastErr = err
			}
		}
		feSelectionClear()
		feListDirectory(config)
		switch {
		case failed == 0:
			showToast("Deleted "+feItemCountLabel(len(targets)), ToastSuccess())
		case failed == len(targets):
			showToast("Cannot delete: "+lastErr.Error(), ToastError())
		default:
			showToast(fmt.Sprintf("Deleted %d, %d failed", len(targets)-failed, failed), ToastWarn())
		}
		closeFeSheet()
	case feActSelectAll:
		marked := feSelectAll(config)
		if marked == 0 {
			showToast("Nothing in this folder to select", ToastInfo())
			return
		}
		PlayActivateSound()
		showToast("Selected "+feItemCountLabel(marked)+" — "+hintSentence("%s for actions", "Y", "Actions"), ToastInfo())
		closeFeSheet()
	case feActSort:
		mode := feCycleSort(config)
		showToast("Sorted by "+strings.ToLower(feSortLabel(mode)), ToastInfo())
		closeFeSheet()
	case feActHidden:
		filesToggleHidden(config)
		closeFeSheet()
	case feActLocations:
		feSheetForget()
		filesShowLocations(config)
	case feActSystemOpen:
		targets := feSelectionTargets(config)
		if len(targets) != 1 {
			showToast("Open one file at a time", ToastInfo())
			return
		}
		closeFeSheet()
		filesOpenWithSystem(targets[0].Path)
	case feActSendChat:
		// The files wait in the chat's tray, and the chat comes up to send
		// them (chatfiles.go).
		targets := feSelectionTargets(config)
		feSheetForget()
		chatSendFromFiles(config, targets)
	case feActConnect:
		closeFeSheetKeepKeyboard()
		openFeRemoteAddressEntry(config)
	case feActRemote:
		closeFeSheet()
		openFeRemotePanel(config)
	}
}

// closeFeSheetKeepKeyboard dismisses the sheet for an action that opens the
// on-screen keyboard: the sheet would otherwise sit over the list behind the
// keyboard, and its armed state has nothing left to confirm.
func closeFeSheetKeepKeyboard() {
	feSheetForget()
}

func handleFeSheetKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !feSheet.open || virtualKeyboardActive {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		feSheetMove(-1)
	case sdl.K_DOWN:
		feSheetMove(1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		feSheetActivate(config)
	case sdl.K_ESCAPE, sdl.K_b:
		closeFeSheet()
	default:
		return true
	}
	return true
}

func ctrlFeSheet(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !feSheet.open {
		return false
	}
	if virtualKeyboardActive {
		// The keyboard's own pad model owns the buttons while a field is open.
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		feSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		feSheetMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		feSheetActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeFeSheet()
	}
	return true
}

func handleFeSheetMouse(mx, my int32, config *Config) bool {
	if !feSheet.open || virtualKeyboardActive {
		return false
	}
	for _, hit := range feSheetRowHits {
		if pointInRect(mx, my, hit.rect) {
			feSheet.cursor = hit.index
			feSheetActivate(config)
			return true
		}
	}
	if feSheetRect.W > 0 && pointInRect(mx, my, feSheetRect) {
		return true
	}
	closeFeSheet()
	return true
}

// feFilesKey handles the Files screen's own two keys. Everything the D-pad does
// still goes through the scene's action path, so the focus graph is unchanged.
func feFilesKey(key sdl.Keycode, config *Config) bool {
	if !feSceneActive(config) {
		return false
	}
	switch key {
	case sdl.K_y:
		openFeSheet(config)
		return true
	case sdl.K_x:
		return feToggleSelectionOnFocused(config)
	case sdl.K_h, sdl.K_PERIOD:
		// Ctrl+H / "." are how desktop file managers show dotfiles; plain H
		// does it here, the handheld has the sheet's row.
		filesToggleHidden(config)
		return true
	case sdl.K_HOME:
		return filesPageList(config, -1<<20)
	case sdl.K_END:
		return filesPageList(config, 1<<20)
	}
	return false
}

// handleFilesController takes the Files screen's Y and X before the generic
// scene dispatch reaches them, so the two buttons behind the footer hints
// cannot be claimed by a focused element. L1/R1 page the list: the list's own
// pad branch never reached the shoulder handler, so they did nothing here.
func handleFilesController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || virtualKeyboardActive || !feSceneActive(config) {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_Y:
		openFeSheet(config)
		return true
	case sdl.CONTROLLER_BUTTON_X:
		return feToggleSelectionOnFocused(config)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return filesPageList(config, -filesListPageRows(config))
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return filesPageList(config, filesListPageRows(config))
	}
	return false
}

// filesListPageRows is one screenful of the Files list.
func filesListPageRows(config *Config) int {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return 10
	}
	if el, ok := sceneDynamicListElement(config.Scenes[currentSceneIndex]); ok {
		return dynamicListMaxVisible(el)
	}
	return 10
}

// filesPageList moves the cursor by delta rows, clamped to the list.
func filesPageList(config *Config, delta int) bool {
	entries := feEntriesOf(config)
	if len(entries) == 0 {
		return false
	}
	next := clampIndex(focusedFileIndex+delta, len(entries))
	if next != focusedFileIndex {
		focusedFileIndex = next
		PlayNavSound()
	}
	return true
}

// renderFeSheet draws the action sheet.
func renderFeSheet(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !feSheet.open {
		return
	}
	actions := feActions(config)
	if len(actions) == 0 {
		return
	}
	if feSheet.cursor >= len(actions) {
		feSheet.cursor = len(actions) - 1
	}
	if feSheet.cursor < 0 {
		feSheet.cursor = 0
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	rowH := smallH + 14
	selected := feSelectionCount()
	subtitle := fmt.Sprintf("%d item(s) in this folder", len(feSelectablePaths(config)))
	if selected > 0 {
		subtitle = fmt.Sprintf("%d selected · %d in this folder", selected, len(feSelectablePaths(config)))
	}
	subtitleInk := ColorTextSecondary()
	if armed := feSheet.armed == "delete" && feSheetArmed("delete"); armed {
		subtitle = "Press A again to delete " + feItemCountLabel(len(feSelectionTargets(config)))
		subtitleInk = WarningInk()
	} else if strings.HasPrefix(feSheet.armed, "sys:") && feSheetArmed(feSheet.armed) {
		subtitle = "System folder — press A again to " + strings.ToLower(strings.TrimPrefix(feSheet.armed, "sys:"))
		subtitleInk = WarningInk()
	} else if filesNav.atLocations {
		subtitle = fmt.Sprintf("%d places", len(filesNav.locations))
	} else if filesNav.systemFolder {
		subtitle = "System folder · changes ask twice · " + subtitle
	}
	header := UIPanelHeader("Files", subtitle, "files")
	header.SubtitleColor = subtitleInk
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + smallH

	panelW := int32(720)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	rows := int32(len(actions))
	maxRows := (screenHeight - 120 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	if rows > maxRows {
		rows = maxRows
	}
	panelH := headerH + rows*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	feSheetRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if feSheet.cursor < feSheet.scroll {
		feSheet.scroll = feSheet.cursor
	}
	if feSheet.cursor >= feSheet.scroll+int(maxRows) {
		feSheet.scroll = feSheet.cursor - int(maxRows) + 1
	}
	if feSheet.scroll > len(actions)-int(maxRows) {
		feSheet.scroll = len(actions) - int(maxRows)
	}
	if feSheet.scroll < 0 {
		feSheet.scroll = 0
	}

	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	feSheetRowHits = feSheetRowHits[:0]
	y := panelY + headerH
	for i := feSheet.scroll; i < len(actions) && i < feSheet.scroll+int(maxRows); i++ {
		action := actions[i]
		rect := sdl.Rect{X: panelX + 12, Y: y, W: panelW - 24, H: rowH - 4}
		if i == feSheet.cursor {
			UIListRow(renderer, rect, 10, true, false)
		}
		ink := ColorTextPrimary()
		detailInk := ColorTextSecondary()
		switch {
		case !action.Enabled:
			ink = ColorTextTertiary()
			detailInk = ColorTextTertiary()
		case action.Dangerous:
			ink = ToastErrorInk()
		}
		if action.Dangerous && feSheet.armed == "delete" && feSheetArmed("delete") {
			ink = WarningInk()
		}
		detail := action.Detail
		detailW := int32(0)
		if detail != "" {
			if w, _, err := font.SizeUTF8(detail); err == nil {
				detailW = int32(w)
			}
		}
		budget := rect.W - detailW - 40
		if budget < 140 {
			budget = 140
		}
		renderText(renderer, config, font, ellipsize(font, action.Label, budget), ink, rect.X+12, rect.Y+7)
		if detail != "" {
			renderText(renderer, config, font, detail, detailInk, rect.X+rect.W-12-detailW, rect.Y+7)
		}
		feSheetRowHits = append(feSheetRowHits, struct {
			index int
			rect  sdl.Rect
		}{index: i, rect: rect})
		y += rowH
	}

	hint := hintLine(FooterHint{"D-Pad / Mouse", "Choose"}, FooterHint{"A / Enter", "Run"},
		FooterHint{"B / Esc", "Close"})
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+22, panelY+panelH-footerH+8)
}
