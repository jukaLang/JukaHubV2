package main

// disctools.go — the two small files CD-based systems need and a plain ROM
// folder never gains by itself: a one-track .cue sheet next to a ripped .bin,
// and an .m3u playlist that turns "Game (Disc 1).chd" + "Game (Disc 2).chd"
// into one entry RetroArch can switch discs from.
//
// Both are additive. An existing .cue or .m3u is never rewritten — files are
// created with O_EXCL, so not even a race with another writer replaces one —
// and a disc an existing sheet already names is left to that sheet, so a
// hand-made playlist under another name does not gain a twin. Nothing here
// touches a cartridge folder: a stray .bin in Roms/GBA is a game file, not a
// disc image. Scanning and writing are SDL-free so they can be tested
// headless.
//
// Three layouts become one game:
//
//   - loose discs in a console's folder or in a game's own folder:
//     "Roms/PS/Game (Disc 1).chd" + "(Disc 2).chd" → "Roms/PS/Game.m3u";
//   - disc folders: "Roms/PS/Game/Disc 1/game.cue" + "Disc 2/game.cue" →
//     "Roms/PS/Game/Game.m3u" naming "Disc 1/game.cue" and "Disc 2/game.cue"
//     (a lone .bin in a disc folder first gets its one-track cue);
//   - .chd, .cue, .pbp, .iso, .img and .bin discs alike.
//
// The library's scan (romsetup.go scanROMLibrary) starts discAutoCombine:
// once per scan at most, on its own goroutine, only for the disc consoles'
// folders, and only for folders whose modification time moved since they were
// last looked at, so a scan with nothing new costs a stat per game folder.
// What it writes is logged, and an open library rescans once so the discs
// show as one game. The ROM manager's "Combine multi-disc games" and the
// tools hub's "Disc helpers" run the same code over everything at once.
//
// Sources: RetroArch's M3U documentation (one disc per line, paths relative
// to the playlist; docs.libretro.com → "M3U file tutorial"), and the cue
// sheet format every libretro CD core reads (FILE "x.bin" BINARY / TRACK 01
// MODE2/2352 / INDEX 01 00:00:00).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

// discSystemModes is the same table by library console, for the folders the
// alias table or a NextUI "Sony PlayStation (PS)" name files under one.
var discSystemModes = map[string]string{
	"PlayStation": "MODE2/2352",
	"Saturn":      "MODE1/2352",
	"Sega CD":     "MODE1/2352",
}

// discMinImageBytes is the smallest .bin that gets a cue sheet: a CD image is
// megabytes, and a 512 KiB "scph1001.bin" left in Roms/PS is a BIOS, not a
// disc.
const discMinImageBytes = 1 << 20

// discSuffixPatterns match the disc marker a multi-disc set carries at the end
// of its name: "(Disc 1)", "(Disk 2)", "[CD 3]", "(Disc 1 of 2)" and the
// " - Disc 1" spelling some rippers use. The bracketed form is tried first so
// "Game (Disc 1)" keeps its name.
var discSuffixPatterns = sync.OnceValue(func() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)[\(\[]\s*(?:disc|disk|cd)\s*([0-9]{1,2})(?:\s*(?:of|/)\s*[0-9]{1,2})?\s*[\)\]]\s*$`),
		regexp.MustCompile(`(?i)(?:^|\s)-\s*(?:disc|disk|cd)\s*([0-9]{1,2})\s*$`),
	}
})

// discTrackPattern matches per-track rips ("Game (Track 2).bin"). Those already
// belong to a multi-track cue sheet, so writing a one-track cue for them would
// be wrong.
var discTrackPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)[\(\[]\s*track\s*[0-9]{1,2}`) })

// discImageExts are the files one disc of a set can be. A .cue wins over the
// .bin it describes; .pbp is a PlayStation EBOOT (one per disc in a loose
// set).
var discImageExts = map[string]bool{".bin": true, ".chd": true, ".iso": true, ".cue": true, ".img": true, ".pbp": true}

// discEntryRank orders the files a disc folder may hold: the one a CD core
// loads first.
var discEntryRank = map[string]int{".cue": 0, ".chd": 1, ".pbp": 2, ".iso": 3, ".img": 4, ".bin": 5}

// discWriteMu keeps the automatic pass, the ROM manager's button and the
// tools hub's from writing the same folder at the same moment.
var discWriteMu sync.Mutex

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
		parts = append(parts, pluralizeCount(r.Cues, "cue sheet", "cue sheets"))
	}
	if r.Playlists > 0 {
		parts = append(parts, pluralizeCount(r.Playlists, "multi-disc playlist", "multi-disc playlists"))
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

// add folds another folder's counts into the report.
func (r *discHelperReport) add(cues, playlists int, errs []string) {
	r.Cues += cues
	r.Playlists += playlists
	r.Errors = append(r.Errors, errs...)
}

// cueTrackMode returns the cue mode for a Roms/<folder> name.
func cueTrackMode(folder string) (string, bool) {
	mode, ok := discSystemFolders[strings.ToLower(strings.TrimSpace(folder))]
	return mode, ok
}

// discFolderMode is cueTrackMode for any name the library files a disc
// console under: the table above, then the alias table and NextUI's tagged
// folders ("Sony PlayStation (PS)") — the latter only where NextUI runs, as
// the library scan reads them.
func discFolderMode(folder string) (string, bool) {
	if mode, ok := cueTrackMode(folder); ok {
		return mode, true
	}
	system, tagged, ok := romFolderSystem(folder, nil)
	if !ok || (tagged && !nextuiActive()) {
		return "", false
	}
	mode, ok := discSystemModes[system.Name]
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
	for _, pattern := range discSuffixPatterns() {
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

// discFolderNumber is the disc a "Disc 2" / "disk_3" folder holds, 0 when the
// name is not a disc folder (romDiscFolder's rule).
func discFolderNumber(name string) int {
	if !romDiscFolder(name) {
		return 0
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	lower = strings.TrimPrefix(strings.TrimPrefix(lower, "disc"), "disk")
	n, err := strconv.Atoi(strings.TrimLeft(lower, " _-"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// discWriteNew creates path with data, never replacing a file already there.
func discWriteNew(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// discSheetListed is every file the sheets in dir name, in lower case, split
// by what names them: any .cue, .gdi or .m3u (sheets), or an .m3u (playlists).
// Those discs and tracks already belong to a game.
func discSheetListed(dir string, names []string) (sheets, playlists map[string]bool) {
	sheets, playlists = map[string]bool{}, map[string]bool{}
	for _, name := range names {
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".cue", ".gdi", ".m3u":
			for _, ref := range romSheetReferences(filepath.Join(dir, name)) {
				key := strings.ToLower(ref)
				sheets[key] = true
				if ext == ".m3u" {
					playlists[key] = true
				}
			}
		}
	}
	return sheets, playlists
}

// writeDiscHelpers adds the missing cue sheets and playlists in one system
// folder (or one game's folder inside it). It returns what it wrote rather
// than stopping at the first failure: one unwritable file must not hide the
// eleven that worked.
func writeDiscHelpers(dir, mode string) (cues, playlists int, errs []string) {
	// Split "(Track N)" rips get their one sheet first (cuegen.go), so the
	// passes below see it like any other.
	cues, errs = cueGenTrackSetSheets(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cues, 0, append(errs, err.Error())
	}

	type discFile struct {
		name string
		size int64
	}
	var files []discFile
	names := make([]string, 0, len(entries))
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
		names = append(names, name)
		have[strings.ToLower(name)] = true
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	sheets, m3uListed := discSheetListed(dir, names)
	key := func(name string) string { return strings.ToLower(filepath.Join(dir, name)) }

	// Pass one: a lone .bin with no sheet of its own gets a one-track cue.
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f.name), ".bin") || f.size < discMinImageBytes || sheets[key(f.name)] {
			continue
		}
		if discTrackPattern().MatchString(f.name) {
			continue
		}
		base := strings.TrimSuffix(f.name, filepath.Ext(f.name))
		cue := base + ".cue"
		if have[strings.ToLower(cue)] {
			continue
		}
		// The mode the image's first sector says; none for a .bin that is
		// not a raw data track (cuegen.go).
		text, ok := cueGenSingleText(filepath.Join(dir, f.name), mode)
		if !ok {
			continue
		}
		if err := discWriteNew(filepath.Join(dir, cue), text); err != nil {
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
		if !discImageExts[strings.ToLower(filepath.Ext(f.name))] {
			continue
		}
		if discTrackPattern().MatchString(f.name) {
			continue
		}
		base, disc, ok := splitDiscNumber(strings.TrimSuffix(f.name, filepath.Ext(f.name)))
		if !ok {
			continue
		}
		setKey := strings.ToLower(base)
		sets[setKey] = append(sets[setKey], discEntry{disc: disc, name: f.name})
	}

	keys := make([]string, 0, len(sets))
	for setKey := range sets {
		keys = append(keys, setKey)
	}
	sort.Strings(keys)
nextSet:
	for _, setKey := range keys {
		list := sets[setKey]
		byDisc := make(map[int]string, len(list))
		for _, entry := range list {
			// The cue is what the emulator loads, so it wins over the .bin it
			// describes. Pass one may have just written it, which is why the
			// name is resolved against the files that exist now.
			cue := strings.TrimSuffix(entry.name, filepath.Ext(entry.name)) + ".cue"
			// A playlist already names one of the set's discs (under another
			// name than the one this would write): the set is a game already.
			if m3uListed[key(entry.name)] || m3uListed[key(cue)] {
				continue nextSet
			}
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
		if err := discWriteNew(filepath.Join(dir, playlist), body.String()); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		have[strings.ToLower(playlist)] = true
		playlists++
	}
	return cues, playlists, errs
}

// discFolderEntry picks the file a disc folder starts from: its cue sheet,
// else its .chd, .pbp, .iso or .img, else a lone big .bin, which gets its
// one-track cue first. "" when the folder holds no disc (yet).
func discFolderEntry(dir, mode string) (entry string, cues int, err error) {
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		return "", 0, rerr
	}
	best, bestRank := "", 99
	var bins []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		rank, ok := discEntryRank[ext]
		if !ok || (discTrackPattern().MatchString(name) && ext != ".cue") {
			continue
		}
		if ext == ".bin" {
			if info, ierr := e.Info(); ierr == nil && info.Size() >= discMinImageBytes {
				bins = append(bins, name)
			}
			continue
		}
		if rank < bestRank || (rank == bestRank && name < best) {
			best, bestRank = name, rank
		}
	}
	if best != "" {
		return best, 0, nil
	}
	if len(bins) != 1 || mode == "" {
		return "", 0, nil
	}
	cue := strings.TrimSuffix(bins[0], filepath.Ext(bins[0])) + ".cue"
	text, ok := cueGenSingleText(filepath.Join(dir, bins[0]), mode) // cuegen.go
	if !ok {
		return "", 0, nil
	}
	if err := discWriteNew(filepath.Join(dir, cue), text); err != nil {
		return "", 0, err
	}
	return cue, 1, nil
}

// writeDiscFolderPlaylist turns a game folder with "Disc 1", "Disc 2" … folders
// into one game: an .m3u named after the game's folder, beside the disc
// folders, naming each disc's file. pending is true when a disc folder holds
// no disc yet (a copy still running), so the caller looks again later.
func writeDiscFolderPlaylist(gameDir, mode string) (cues, playlists int, pending bool, errs []string) {
	entries, err := os.ReadDir(gameDir)
	if err != nil {
		return 0, 0, false, []string{err.Error()}
	}
	byDisc := map[int]string{}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
			continue
		}
		n := discFolderNumber(e.Name())
		if n == 0 {
			continue
		}
		if _, taken := byDisc[n]; taken {
			continue // "Disc 1" and "disc1": the first spelling is kept
		}
		// A split "(Track N)" rip in the disc folder gets its sheet (cuegen.go).
		trackCues, trackErrs := cueGenTrackSetSheets(filepath.Join(gameDir, e.Name()))
		cues += trackCues
		errs = append(errs, trackErrs...)
		entry, made, ferr := discFolderEntry(filepath.Join(gameDir, e.Name()), mode)
		cues += made
		if ferr != nil {
			errs = append(errs, ferr.Error())
			continue
		}
		if entry == "" {
			pending = true
			continue
		}
		byDisc[n] = e.Name() + "/" + entry
	}
	if len(byDisc) < 2 {
		return cues, 0, pending, errs
	}
	// A playlist beside the disc folders that already names one of them: the
	// game is one already.
	_, listed := discSheetListed(gameDir, names)
	for _, rel := range byDisc {
		if listed[strings.ToLower(filepath.Join(gameDir, filepath.FromSlash(rel)))] {
			return cues, 0, false, errs
		}
	}
	playlist := filepath.Join(gameDir, filepath.Base(gameDir)+".m3u")
	if _, err := os.Lstat(playlist); err == nil {
		return cues, 0, false, errs
	}
	discs := make([]int, 0, len(byDisc))
	for n := range byDisc {
		discs = append(discs, n)
	}
	sort.Ints(discs)
	var body strings.Builder
	for _, n := range discs {
		body.WriteString(byDisc[n])
		body.WriteByte('\n')
	}
	if err := discWriteNew(playlist, body.String()); err != nil {
		return cues, 0, pending, append(errs, err.Error())
	}
	return cues, 1, pending, errs
}

// discGameFolders lists a disc console folder's sub-folders that can hold one
// game: not hidden, not a picture folder, not a disc folder itself.
func discGameFolders(systemDir string) []string {
	entries, err := os.ReadDir(systemDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || skipScanEntryName(name) || plugGameSkipDir[strings.ToLower(name)] || romDiscFolder(name) {
			continue
		}
		out = append(out, filepath.Join(systemDir, name))
	}
	return out
}

// discSystemDirs is every disc console folder under the roots, with its mode.
func discSystemDirs(roots []string) (dirs, modes []string) {
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || skipScanEntryName(entry.Name()) {
				continue
			}
			mode, ok := discFolderMode(entry.Name())
			if !ok {
				continue
			}
			dirs = append(dirs, filepath.Join(root, entry.Name()))
			modes = append(modes, mode)
		}
	}
	return dirs, modes
}

// generateDiscHelpers walks each Roms root and repairs every disc system in it:
// the console's folder, each game's own folder in it, and the disc folders in
// those. Roots that do not exist are skipped quietly: on a fresh card most of
// them will not.
func generateDiscHelpers(roots []string) discHelperReport {
	discWriteMu.Lock()
	defer discWriteMu.Unlock()
	var report discHelperReport
	dirs, modes := discSystemDirs(roots)
	for i, dir := range dirs {
		report.Systems++
		report.add(writeDiscHelpers(dir, modes[i]))
		for _, game := range discGameFolders(dir) {
			report.add(writeDiscHelpers(game, modes[i]))
			cues, playlists, _, errs := writeDiscFolderPlaylist(game, modes[i])
			report.add(cues, playlists, errs)
		}
	}
	return report
}

// ── Automatic pass ──────────────────────────────────────────────────────────

// discAuto is the automatic pass's state: one goroutine at a time, a second
// request while it runs makes it go round once more, and seen keeps each
// folder's modification time from the last look.
var discAuto struct {
	mu      sync.Mutex
	running bool
	again   bool
	seen    map[string]time.Time
}

// romScanStartHook is what scanROMLibrary starts with every scan: the
// automatic pass. Set in init(), not in its declaration: the pass can rescan
// the library, which reaches package variables that call scanROMLibrary, and
// Go refuses such an initialisation cycle.
var romScanStartHook func(roots []string)

func init() { romScanStartHook = discAutoCombine }

// discAutoRootsMatch reports whether a scan is the library's own (its roots
// are romLibraryRoots()): the automatic pass writes only into the real
// library, never into a folder a test or a one-off scan passed.
func discAutoRootsMatch(roots []string) bool {
	lib := romLibraryRoots()
	if len(roots) == 0 || len(roots) != len(lib) {
		return false
	}
	for i := range roots {
		if filepath.Clean(roots[i]) != filepath.Clean(lib[i]) {
			return false
		}
	}
	return true
}

// discAutoCombine starts the automatic pass for a library scan. It never
// blocks the caller (which may be the main thread) and never runs twice at
// once.
func discAutoCombine(roots []string) {
	if !discAutoRootsMatch(roots) {
		return
	}
	discAuto.mu.Lock()
	if discAuto.running {
		discAuto.again = true
		discAuto.mu.Unlock()
		return
	}
	discAuto.running = true
	discAuto.mu.Unlock()
	roots = append([]string(nil), roots...)
	go discAutoRun(roots)
}

// discAutoRun is the pass's goroutine.
func discAutoRun(roots []string) {
	created := 0
	defer func() {
		if r := recover(); r != nil {
			logWarn("library", "combining disc games stopped: %v", r)
			discAuto.mu.Lock()
			discAuto.running, discAuto.again = false, false
			discAuto.mu.Unlock()
		}
		if created > 0 {
			tbxPost(func() { libraryRescanSoon(libraryRescanTries) })
		}
	}()
	for {
		report := discCombineChanged(roots)
		created += report.Created()
		if report.Created() > 0 || len(report.Errors) > 0 {
			logInfo("library", "disc games: %s (%d errors)", report.Summary(), len(report.Errors))
			for i, e := range report.Errors {
				if i == 5 {
					break
				}
				logWarn("library", "disc games: %s", e)
			}
		}
		// The Game Boy shelf's Color games, said once (gbcsort.go).
		gbcNoticeCheck(roots)
		// Asked again while this pass ran: once more. Otherwise the pass ends
		// under the same lock a new request checks, so none is lost.
		discAuto.mu.Lock()
		if !discAuto.again {
			discAuto.running = false
			discAuto.mu.Unlock()
			return
		}
		discAuto.again = false
		discAuto.mu.Unlock()
	}
}

// discCombineChanged is generateDiscHelpers for the folders that changed since
// the last pass: a folder whose modification time is the one seen last time
// is skipped without being read.
func discCombineChanged(roots []string) discHelperReport {
	discWriteMu.Lock()
	defer discWriteMu.Unlock()
	discAuto.mu.Lock()
	if discAuto.seen == nil {
		discAuto.seen = map[string]time.Time{}
	}
	discAuto.mu.Unlock()
	changed := func(dir string) (time.Time, bool) {
		info, err := os.Stat(dir)
		if err != nil {
			return time.Time{}, false
		}
		discAuto.mu.Lock()
		last, ok := discAuto.seen[dir]
		discAuto.mu.Unlock()
		return info.ModTime(), !ok || !last.Equal(info.ModTime())
	}
	// remember keeps the time the folder had before it was read, so a file
	// copied in while the pass read it is looked at next time. A folder the
	// pass wrote into is forgotten instead: its own files moved the time, and
	// the next pass reads it once more, finds nothing to add and remembers it.
	remember := func(dir string, before time.Time, wrote bool) {
		discAuto.mu.Lock()
		if wrote {
			delete(discAuto.seen, dir)
		} else {
			discAuto.seen[dir] = before
		}
		discAuto.mu.Unlock()
	}
	onNextUI := nextuiActive()
	var report discHelperReport
	dirs, modes := discSystemDirs(roots)
	for i, dir := range dirs {
		// NextUI's own "Name (TAG)" folders are only written on request (the
		// ROM manager, Disc helpers): its menu would list the new playlist
		// beside the discs, and JukaHub leaves NextUI's Roms as they are.
		if onNextUI {
			if _, tagged, ok := romFolderSystem(filepath.Base(dir), nil); ok && tagged {
				continue
			}
		}
		report.Systems++
		if before, ok := changed(dir); ok {
			cues, playlists, errs := writeDiscHelpers(dir, modes[i])
			report.add(cues, playlists, errs)
			remember(dir, before, cues+playlists > 0)
		}
		for _, game := range discGameFolders(dir) {
			before, ok := changed(game)
			if !ok {
				continue
			}
			cues, playlists, errs := writeDiscHelpers(game, modes[i])
			report.add(cues, playlists, errs)
			folderCues, folderPlaylists, pending, folderErrs := writeDiscFolderPlaylist(game, modes[i])
			report.add(folderCues, folderPlaylists, folderErrs)
			if !pending {
				remember(game, before, cues+playlists+folderCues+folderPlaylists > 0)
			}
		}
	}
	return report
}

// ── Running it now ──────────────────────────────────────────────────────────

// discCombineBusy is set while "Combine multi-disc games" runs.
var discCombineBusy atomic.Bool

// errDiscCombineBusy is the answer while a combine is already running.
var errDiscCombineBusy = errors.New("already combining")

// discCombineNow runs the whole pass in the background (the ROM manager's
// row): a toast says what it did, and an open library rescans. done, when
// set, runs on the main thread afterwards.
func discCombineNow(done func(discHelperReport)) error {
	if !discCombineBusy.CompareAndSwap(false, true) {
		return errDiscCombineBusy
	}
	ShowToast("Looking for multi-disc games…", ToastKindInfo)
	roots := append([]string(nil), romLibraryRoots()...)
	go func() {
		var report discHelperReport
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "combining disc games stopped: %v", r)
				report.Errors = append(report.Errors, "the run stopped unexpectedly")
			}
			discCombineBusy.Store(false)
			tbxPost(func() {
				kind := ToastKindInfo
				switch {
				case report.Created() == 0 && len(report.Errors) > 0:
					kind = ToastKindError
				case len(report.Errors) > 0:
					kind = ToastKindWarning
				case report.Created() > 0:
					kind = ToastKindSuccess
				}
				ShowToast(report.Summary(), kind)
				if report.Created() > 0 {
					libraryRescanSoon(libraryRescanTries)
				}
				if done != nil {
					done(report)
				}
			})
		}()
		report = generateDiscHelpers(roots)
		logInfo("library", "combine multi-disc games: %s (%d errors)", report.Summary(), len(report.Errors))
	}()
	return nil
}

// libraryRescanTries is how long the callers let libraryRescanSoon wait for a
// running scan: 30 s, as a big card's first scan on a handheld takes seconds.
const libraryRescanTries = 60

// libraryRescanSoon rescans an open library once the scan running now (if
// any) is done, so files written meanwhile show. Main thread; tries bounds
// how long it waits for a running scan (half a second each).
func libraryRescanSoon(tries int) {
	if !gameLib.open {
		return
	}
	if gameLib.scanning {
		if tries <= 0 {
			return
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			tbxPost(func() { libraryRescanSoon(tries - 1) })
		}()
		return
	}
	refreshGameLibrary()
}
