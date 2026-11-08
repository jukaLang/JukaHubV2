package main

// romm_plan.go — the ROM manager's scan, and the plan it proposes.
//
// The scan walks three kinds of places: the Roms trees the library reads, the
// BIOS folders, and the places games get dropped — the card's root and its
// Downloads folder on the handheld; Downloads, the Desktop and Documents on a
// PC. Every file with a game's extension is looked at, but only the ones that
// might be in the wrong place are opened: a .gba inside Roms/GBA is in place
// without a read, and a .bin or .zip inside a console folder is trusted to
// belong there.
//
// What comes out is a list of proposed changes, each with its reason:
//
//   - a game outside its console's folder (or in another console's) moves into
//     Roms/<console>, created under the name the firmware uses when the card
//     has no folder for it yet;
//   - a firmware file outside the BIOS folders moves into BIOS;
//   - a byte-for-byte duplicate (same size, CRC32 and SHA-1) of a game that
//     stays is offered for deletion — off by default, and asked about again;
//   - an empty file is offered for deletion the same way;
//   - a file that looks like a game but cannot be identified is listed and
//     left alone.
//
// Nothing here writes to the card: romm_apply.go does, and keeps a journal so
// the whole run can be put back.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// rommArea says what kind of place a scan root is.
type rommArea int

const (
	rommAreaRoms  rommArea = iota // a Roms tree the library reads
	rommAreaBIOS                  // a firmware folder
	rommAreaLoose                 // a place games get dropped
)

// rommRoot is one folder the scan walks.
type rommRoot struct {
	Path  string
	Area  rommArea
	Depth int // folder levels below Path that are walked
	Label string
	// Quiet roots (a PC's Downloads, Desktop and Documents) hold all kinds of
	// files: a .zip or an .iso there that is not a game is not worth a line.
	Quiet bool
}

// rommTargets are the folders the plan moves files into.
type rommTargets struct {
	Roms string
	BIOS string
}

const (
	// rommMaxEntries bounds the walk of one Roms tree, rommLooseMaxEntries the
	// walk of one of the other roots (a PC's Documents can hold anything).
	rommMaxEntries      = 200000
	rommLooseMaxEntries = 40000
	// rommRomsDepth is how deep a Roms tree is walked: Roms/PS/Game/disc.cue
	// and a few levels of the user's own sub-folders.
	rommRomsDepth = 6
)

// rommFile is one candidate the walk found.
type rommFile struct {
	Path     string
	Size     int64
	Area     rommArea
	RootPath string
	Label    string
	Quiet    bool
	// Folder is the library console of the Roms folder the file is in.
	Folder string
	// FolderName is that folder's name on the card, for the reason line.
	FolderName string
	// Foreign is true inside a Roms folder no library console uses (ARCADE,
	// PORTS …): files there belong to another emulator and are left alone.
	Foreign bool
	// Part is a track or a disc a sheet in the scan names.
	Part bool
	// Parts are the files a sheet names, moved together with it.
	Parts     []string
	PartsSize int64
	// Split is true when a sheet's tracks are not all beside it.
	Split bool
	Det   rommDetection
}

// rommAction is what applying an item does.
type rommAction int

const (
	rommActMove   rommAction = iota // file a game into its console's folder
	rommActBIOS                     // move a firmware file into BIOS
	rommActDelete                   // remove a duplicate or an empty file
	rommActNone                     // listed, never changed
)

// rommKind is the plan section an item is listed under.
type rommKind int

const (
	rommKindMisplaced rommKind = iota
	rommKindBIOS
	rommKindDuplicate
	rommKindEmpty
	rommKindUnknown
)

// rommItem is one proposed change.
type rommItem struct {
	Kind   rommKind
	Action rommAction
	Path   string
	// Parts are moved with Path, into the same folder (a cue sheet's tracks).
	Parts []string
	// Dest is where a move puts Path; never an existing file.
	Dest string
	// System is the console (or, for firmware, the system it boots).
	System string
	// Group is the line the item is listed under: "Roms/GBA", "BIOS" …
	Group  string
	Reason string
	Size   int64
	// Keep is the copy a duplicate is a copy of.
	Keep string
	On   bool
}

// rommPlan is a finished scan.
type rommPlan struct {
	Items   []rommItem
	Scanned int
	InPlace int
	// Partial is true when the scan was stopped or ran out of budget.
	Partial bool
	Targets rommTargets
	Took    time.Duration
}

// rommReport receives the scan's progress: a stage line, the files found so
// far, and done of total for the stage that can count.
type rommReport func(stage string, files, done, total int)

// rommVerdict is what classification decided about one game.
type rommVerdict int

const (
	rommInPlace rommVerdict = iota
	rommMove
	rommUnknown
	rommSilent
	rommDuplicate
)

// rommGame is one identified (or unidentifiable) game on its way to the plan.
type rommGame struct {
	f       *rommFile
	verdict rommVerdict
	sys     rommSystem
	how     string
	keep    string
}

// ── Roots ───────────────────────────────────────────────────────────────────

// rommDefaultRoots is where the scan looks on this device, and where it puts
// what it moves.
func rommDefaultRoots() ([]rommRoot, rommTargets) {
	var roots []rommRoot
	romsRoots := romLibraryRoots()
	for _, r := range romsRoots {
		roots = append(roots, rommRoot{Path: r, Area: rommAreaRoms, Depth: rommRomsDepth, Label: "Roms"})
	}
	for _, r := range biosDoctorRoots() {
		roots = append(roots, rommRoot{Path: r, Area: rommAreaBIOS, Depth: 1, Label: "BIOS"})
	}
	targets := rommTargets{Roms: rommPickRomsRoot(romsRoots)}
	if IsWindows() {
		targets.BIOS = resolvePath("BIOS")
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			roots = append(roots,
				rommRoot{Path: filepath.Join(home, "Downloads"), Area: rommAreaLoose, Depth: 2, Label: "Downloads", Quiet: true},
				rommRoot{Path: filepath.Join(home, "Desktop"), Area: rommAreaLoose, Depth: 1, Label: "the Desktop", Quiet: true},
				rommRoot{Path: filepath.Join(home, "Documents"), Area: rommAreaLoose, Depth: 2, Label: "Documents", Quiet: true},
			)
		}
	} else {
		card := filepath.Dir(deviceRomsRoot)
		targets.BIOS = filepath.Join(card, "BIOS")
		roots = append(roots,
			rommRoot{Path: card, Area: rommAreaLoose, Depth: 0, Label: "the card's root"},
			rommRoot{Path: filepath.Join(card, "Downloads"), Area: rommAreaLoose, Depth: 2, Label: "Downloads"},
		)
	}
	if dl := strings.TrimSpace(downloadsRoot()); dl != "" {
		roots = append(roots, rommRoot{Path: dl, Area: rommAreaLoose, Depth: 2, Label: "JukaHub downloads", Quiet: true})
	}
	return rommDedupRoots(roots), targets
}

// rommPickRomsRoot is the Roms folder moves go into: the first one that
// exists, so a card with games keeps them all in one tree.
func rommPickRomsRoot(roots []string) string {
	for _, r := range roots {
		if dirExists(r) {
			return r
		}
	}
	if len(roots) > 0 {
		return roots[0]
	}
	return resolvePath("Roms")
}

// rommDedupRoots drops a root listed twice; the first (most specific) kind
// wins.
func rommDedupRoots(roots []rommRoot) []rommRoot {
	seen := map[string]bool{}
	out := roots[:0]
	for _, r := range roots {
		clean := filepath.Clean(strings.TrimSpace(r.Path))
		if clean == "" || clean == "." {
			continue
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		r.Path = clean
		out = append(out, r)
	}
	return out
}

// rommSkipDir names the folders a walk never enters: hidden ones, the box art
// and media folders frontends keep beside the games, and system folders.
func rommSkipDir(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	switch lower {
	case "imgs", "images", "media", "snap", "snaps", "boxart", "boxarts", "thumbnails", "covers",
		"manuals", "$recycle.bin", "system volume information", "node_modules", "appdata", "__macosx":
		return true
	}
	return false
}

// rommOtherRoot reports whether dir is another scan root, which is walked as its
// own kind instead of as part of this one (Roms inside Documents).
func rommOtherRoot(dir string, self rommRoot, roots []rommRoot) bool {
	for _, r := range roots {
		if strings.EqualFold(r.Path, self.Path) {
			continue
		}
		if strings.EqualFold(filepath.Clean(dir), r.Path) {
			return true
		}
	}
	return false
}

// ── Walk ────────────────────────────────────────────────────────────────────

// rommWalk lists every file with a game's (or a firmware file's) extension in
// the roots. partial is true when the walk was stopped or hit its budget.
func rommWalk(ctx context.Context, roots []rommRoot, report rommReport) ([]*rommFile, bool) {
	var files []*rommFile
	seen := map[string]bool{}
	partial := false
	for _, root := range roots {
		if ctx.Err() != nil {
			return files, true
		}
		info, err := os.Stat(root.Path)
		if err != nil || !info.IsDir() {
			continue
		}
		stage := "Looking in " + root.Label + "…"
		report(stage, len(files), 0, 0)
		base := root.Path
		baseDepth := strings.Count(base, string(os.PathSeparator))
		budget := rommLooseMaxEntries
		if root.Area == rommAreaRoms {
			budget = rommMaxEntries
		}
		count := 0
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if ctx.Err() != nil {
				partial = true
				return fs.SkipAll
			}
			count++
			if count > budget {
				partial = true
				return fs.SkipAll
			}
			if d.IsDir() {
				if path == base {
					return nil
				}
				if rommSkipDir(d.Name()) || rommOtherRoot(path, root, roots) {
					return fs.SkipDir
				}
				if strings.Count(filepath.Clean(path), string(os.PathSeparator))-baseDepth > root.Depth {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !rommIsROMExt(filepath.Ext(d.Name())) {
				return nil
			}
			clean := filepath.Clean(path)
			key := strings.ToLower(clean)
			if seen[key] {
				return nil
			}
			seen[key] = true
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			f := &rommFile{Path: clean, Size: fi.Size(), Area: root.Area, RootPath: base, Label: root.Label, Quiet: root.Quiet}
			if root.Area == rommAreaRoms {
				rommPlaceInRoms(f, base)
			}
			files = append(files, f)
			if len(files)%250 == 0 {
				report(stage, len(files), 0, 0)
			}
			return nil
		})
	}
	return files, partial
}

// rommPlaceInRoms records which console folder of a Roms tree a file is in.
func rommPlaceInRoms(f *rommFile, root string) {
	rel, err := filepath.Rel(root, f.Path)
	if err != nil {
		return
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return // loose in the Roms folder itself
	}
	f.FolderName = parts[0]
	if sys, ok := romSystemAliases[strings.ToLower(parts[0])]; ok {
		f.Folder = sys.Name
		return
	}
	f.Foreign = true
}

// rommLinkSheets ties every cue sheet, GD-ROM sheet and playlist to the files it
// names, so a disc game moves as one unit and its tracks are not listed on
// their own.
func rommLinkSheets(files []*rommFile) {
	byPath := make(map[string]*rommFile, len(files))
	for _, f := range files {
		byPath[strings.ToLower(f.Path)] = f
	}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f.Path)) {
		case ".cue", ".gdi", ".m3u":
			rommCollectParts(f, f.Path, byPath, 0)
		}
	}
}

// rommCollectParts adds the files sheet (or a sheet it names) refers to.
func rommCollectParts(sheet *rommFile, path string, byPath map[string]*rommFile, depth int) {
	if depth > 2 {
		return
	}
	dir := filepath.Dir(sheet.Path)
	for _, ref := range romSheetReferences(path) {
		if strings.EqualFold(ref, sheet.Path) {
			continue
		}
		info, err := os.Stat(ref)
		if err != nil || !info.Mode().IsRegular() {
			continue // a missing track: nothing to carry
		}
		if !strings.EqualFold(filepath.Dir(ref), dir) {
			sheet.Split = true
		}
		known := false
		for _, p := range sheet.Parts {
			if strings.EqualFold(p, ref) {
				known = true
				break
			}
		}
		if !known {
			sheet.Parts = append(sheet.Parts, ref)
			sheet.PartsSize += info.Size()
		}
		if part, ok := byPath[strings.ToLower(ref)]; ok && part != sheet {
			part.Part = true
		}
		switch strings.ToLower(filepath.Ext(ref)) {
		case ".cue", ".gdi", ".m3u":
			rommCollectParts(sheet, ref, byPath, depth+1)
		}
	}
}

// ── Classification ──────────────────────────────────────────────────────────

// rommSameConsole reports whether a game of console b is at home in a folder
// of console a. Game Boy and Game Boy Color share their cores, and many cards
// keep both kinds in one folder.
func rommSameConsole(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	gb := func(s string) bool { return s == "Game Boy" || s == "Game Boy Color" }
	return gb(a) && gb(b)
}

// rommClassify decides what one file needs. It opens the file only when its
// place does not already settle the question.
func rommClassify(f *rommFile) rommGame {
	g := rommGame{f: f, verdict: rommSilent}
	ext := strings.ToLower(filepath.Ext(f.Path))
	_, unique := rommExtSystems[ext]
	detect := func() (rommSystem, bool) {
		f.Det = rommDetectFile(f.Path, f.Size)
		sys, ok := rommSystemByID(f.Det.System)
		return sys, ok && f.Det.Strong
	}
	switch f.Area {
	case rommAreaRoms:
		switch {
		case f.Folder != "":
			// The folder is trusted for containers; a cartridge with another
			// console's extension (a .gba in Roms/SNES) is checked.
			g.verdict = rommInPlace
			if !unique || rommSameConsole(f.Folder, rommSystemLabel(rommExtSystems[ext])) {
				return g
			}
			if sys, ok := detect(); ok && !rommSameConsole(f.Folder, sys.Name) {
				g.verdict, g.sys, g.how = rommMove, sys, f.Det.How
			}
			return g
		case f.Foreign:
			// Another emulator's folder: only a file only a listed console uses
			// is moved out of it.
			if !unique {
				return g
			}
			if sys, ok := detect(); ok {
				g.verdict, g.sys, g.how = rommMove, sys, f.Det.How
			}
			return g
		default:
			if sys, ok := detect(); ok {
				g.verdict, g.sys, g.how = rommMove, sys, f.Det.How
				return g
			}
			if !rommIsMarkdown(f.Path) {
				g.verdict = rommUnknown
			}
			return g
		}
	case rommAreaBIOS:
		// A .bin in a BIOS folder is firmware this table does not know, not a
		// game; only a cartridge extension is a game filed in the wrong place.
		if !unique {
			return g
		}
		if sys, ok := detect(); ok {
			g.verdict, g.sys, g.how = rommMove, sys, f.Det.How
		}
		return g
	}
	if sys, ok := detect(); ok {
		g.verdict, g.sys, g.how = rommMove, sys, f.Det.How
		return g
	}
	if !f.Quiet && !rommIsMarkdown(f.Path) {
		g.verdict = rommUnknown
	}
	return g
}

// rommIsMarkdown reports whether a file is a ".md" that turned out not to be a
// Mega Drive cartridge: a note (README.md), never listed as a game.
func rommIsMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}

// rommWhere says where a file is, for its reason line.
func rommWhere(f *rommFile) string {
	switch f.Area {
	case rommAreaRoms:
		if f.FolderName != "" {
			return "In Roms/" + f.FolderName
		}
		return "Loose in Roms"
	case rommAreaBIOS:
		return "In BIOS"
	}
	return "In " + f.Label
}

// ── Duplicates ──────────────────────────────────────────────────────────────

// rommQuickHash fingerprints the start, the middle and the end of a file:
// two different games of the same size almost never agree there, so the full
// read is only spent on real candidates.
func rommQuickHash(path string, size int64, buf []byte) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	for _, off := range []int64{0, size / 2, size - int64(len(buf))} {
		if off < 0 {
			off = 0
		}
		n, err := f.ReadAt(buf, off)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
	}
	return h.Sum32(), nil
}

// rommFullHash is a file's CRC32 and SHA-1, read once.
func rommFullHash(ctx context.Context, path string, buf []byte) (uint32, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	crc := crc32.NewIEEE()
	sum := sha1.New()
	w := io.MultiWriter(crc, sum)
	for {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return 0, "", werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	return crc.Sum32(), hex.EncodeToString(sum.Sum(nil)), nil
}

// rommDupKey groups files whose contents are the same.
type rommDupKey struct {
	size  int64
	crc   uint32
	sha   string
	quick uint32
}

// rommFindDuplicates returns the sets of byte-identical files among games.
// Only files that share their size with another are read, and of those only
// the ones whose quick fingerprints agree are read in full.
func rommFindDuplicates(ctx context.Context, games []*rommFile, report rommReport) [][]*rommFile {
	bySize := map[int64][]*rommFile{}
	for _, f := range games {
		if f.Size >= 1024 {
			bySize[f.Size] = append(bySize[f.Size], f)
		}
	}
	sizes := make([]int64, 0, len(bySize))
	queued := 0
	for size, group := range bySize {
		if len(group) > 1 {
			sizes = append(sizes, size)
			queued += len(group)
		}
	}
	if queued == 0 {
		return nil
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
	stage := "Comparing files of the same size…"
	buf := make([]byte, 64<<10)
	done := 0
	var sets [][]*rommFile
	for _, size := range sizes {
		quick := map[rommDupKey][]*rommFile{}
		for _, f := range bySize[size] {
			if ctx.Err() != nil {
				return sets
			}
			h, err := rommQuickHash(f.Path, size, buf)
			done++
			report(stage, len(games), done, queued)
			if err != nil {
				continue
			}
			k := rommDupKey{size: size, quick: h}
			quick[k] = append(quick[k], f)
		}
		for _, group := range quick {
			if len(group) < 2 {
				continue
			}
			full := map[rommDupKey][]*rommFile{}
			for _, f := range group {
				crc, sha, err := rommFullHash(ctx, f.Path, buf)
				if err != nil {
					if ctx.Err() != nil {
						return sets
					}
					continue
				}
				k := rommDupKey{size: size, crc: crc, sha: sha}
				full[k] = append(full[k], f)
			}
			for _, same := range full {
				if len(same) > 1 {
					sort.Slice(same, func(i, j int) bool { return same[i].Path < same[j].Path })
					sets = append(sets, same)
				}
			}
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i][0].Path < sets[j][0].Path })
	return sets
}

// rommSameContent reports whether two files hold the same bytes.
func rommSameContent(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA != nil || errB != nil || ia.Size() != ib.Size() {
		return false
	}
	buf := make([]byte, 64<<10)
	ctx := context.Background()
	ca, sa, errA := rommFullHash(ctx, a, buf)
	cb, sb, errB := rommFullHash(ctx, b, buf)
	return errA == nil && errB == nil && ca == cb && sa == sb
}

// ── Plan ────────────────────────────────────────────────────────────────────

// rommScan walks the roots, identifies what it finds and builds the plan.
func rommScan(ctx context.Context, roots []rommRoot, targets rommTargets, report rommReport) rommPlan {
	start := time.Now()
	if report == nil {
		report = func(string, int, int, int) {}
	}
	files, partial := rommWalk(ctx, roots, report)
	plan := rommBuildPlan(ctx, files, targets, report)
	plan.Partial = plan.Partial || partial || ctx.Err() != nil
	plan.Took = time.Since(start)
	return plan
}

// rommBuildPlan classifies the files and turns the verdicts into items.
func rommBuildPlan(ctx context.Context, files []*rommFile, targets rommTargets, report rommReport) rommPlan {
	if report == nil {
		report = func(string, int, int, int) {}
	}
	plan := rommPlan{Scanned: len(files), Targets: targets}
	rommLinkSheets(files)
	stage := "Checking what each file is…"
	var games []rommGame
	var biosFiles []*rommFile
	for i, f := range files {
		if ctx.Err() != nil {
			plan.Partial = true
			break
		}
		if i%50 == 0 {
			report(stage, len(files), i, len(files))
		}
		if f.Part {
			continue
		}
		if f.Size == 0 {
			// An empty .md is an empty note, not a failed Mega Drive download.
			if rommIsMarkdown(f.Path) {
				continue
			}
			plan.Items = append(plan.Items, rommItem{
				Kind: rommKindEmpty, Action: rommActDelete, Path: f.Path,
				Group: "Empty files", Reason: rommWhere(f) + " · 0 bytes, nothing to play",
			})
			continue
		}
		if _, ok := rommBIOSFor(f.Path, f.Size); ok {
			switch {
			case f.Area == rommAreaBIOS:
				plan.InPlace++
			case f.Area == rommAreaRoms && strings.EqualFold(filepath.Ext(f.Path), ".zip"):
				// An arcade BIOS set (neogeo.zip) beside the games is where the
				// MAME-family cores look for it, so it stays.
				plan.InPlace++
			default:
				biosFiles = append(biosFiles, f)
			}
			continue
		}
		g := rommClassify(f)
		switch g.verdict {
		case rommSilent:
			continue
		case rommInPlace:
			plan.InPlace++
		}
		games = append(games, g)
	}

	// Duplicates: whole files only. A sheet's tracks are parts, not games, so a
	// disc kept twice as cue and bins is not compared track by track.
	if ctx.Err() == nil {
		var candidates []*rommFile
		index := map[*rommFile]int{}
		for i, g := range games {
			if g.verdict == rommUnknown || len(g.f.Parts) > 0 {
				continue
			}
			index[g.f] = i
			candidates = append(candidates, g.f)
		}
		for _, set := range rommFindDuplicates(ctx, candidates, report) {
			keep := rommPickKeeper(set, games, index)
			for _, f := range set {
				if f == keep {
					continue
				}
				i := index[f]
				if games[i].verdict == rommInPlace {
					plan.InPlace--
				}
				games[i].verdict = rommDuplicate
				games[i].keep = keep.Path
			}
		}
	}

	existing := rommListDirs(targets.Roms)
	taken := map[string]bool{}
	for _, g := range games {
		switch g.verdict {
		case rommDuplicate:
			plan.Items = append(plan.Items, rommItem{
				Kind: rommKindDuplicate, Action: rommActDelete, Path: g.f.Path, Size: g.f.Size,
				Group: "Duplicates", Keep: g.keep,
				Reason: rommWhere(g.f) + " · the same bytes as " + rommShortPath(g.keep, targets),
			})
		case rommUnknown:
			reason := rommWhere(g.f) + " · not recognised, so left where it is"
			plan.Items = append(plan.Items, rommItem{
				Kind: rommKindUnknown, Action: rommActNone, Path: g.f.Path, Size: g.f.Size,
				Group: "Not recognised", Reason: reason,
			})
		case rommMove:
			plan.Items = append(plan.Items, rommPlanMove(g, targets, existing, taken))
		}
	}
	for _, f := range biosFiles {
		plan.Items = append(plan.Items, rommPlanBIOS(f, targets, taken))
	}
	rommSortItems(plan.Items)
	return plan
}

// rommPickKeeper chooses the copy of a duplicate set that stays: one already in
// its console's folder, then any in a Roms tree, then the shortest path.
func rommPickKeeper(set []*rommFile, games []rommGame, index map[*rommFile]int) *rommFile {
	score := func(f *rommFile) int {
		s := 0
		if f.Area == rommAreaRoms {
			s += 2
		}
		if games[index[f]].verdict == rommInPlace {
			s += 4
		}
		return s
	}
	best := set[0]
	for _, f := range set[1:] {
		sf, sb := score(f), score(best)
		switch {
		case sf != sb:
			if sf > sb {
				best = f
			}
		case len(f.Path) != len(best.Path):
			if len(f.Path) < len(best.Path) {
				best = f
			}
		case f.Path < best.Path:
			best = f
		}
	}
	return best
}

// rommListDirs lists the folder names directly under root.
func rommListDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// rommTargetFolder is where a console's games go: a folder the card already
// uses for it (the firmware's own name first), else the firmware's name.
func rommTargetFolder(root string, sys rommSystem, existing []string) string {
	best := ""
	for _, name := range existing {
		alias, ok := romSystemAliases[strings.ToLower(name)]
		if !ok || alias.Name != sys.Name {
			continue
		}
		if strings.EqualFold(name, sys.Folder) {
			return filepath.Join(root, name)
		}
		if best == "" {
			best = name
		}
	}
	if best != "" {
		return filepath.Join(root, best)
	}
	return filepath.Join(root, sys.Folder)
}

// rommShortPath shows a path relative to the folder it is best known by.
func rommShortPath(path string, targets rommTargets) string {
	for _, base := range []string{targets.Roms, targets.BIOS} {
		if base == "" {
			continue
		}
		if rel, err := filepath.Rel(filepath.Dir(base), path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

// rommFreeName is path, or the first "name (2).ext", "name (3).ext" … that is
// neither on the card nor already promised to another item. A move never
// overwrites anything.
func rommFreeName(path string, taken map[string]bool) string {
	free := func(p string) bool {
		if taken != nil && taken[strings.ToLower(p)] {
			return false
		}
		_, err := os.Lstat(p)
		return os.IsNotExist(err)
	}
	if free(path) {
		return path
	}
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	for i := 2; i < 1000; i++ {
		candidate := filepath.Join(dir, stem+" ("+itoa(i)+")"+ext)
		if free(candidate) {
			return candidate
		}
	}
	return filepath.Join(dir, stem+" ("+time.Now().Format("20060102-150405")+")"+ext)
}

// rommPlanMove turns a misplaced game into a move, or explains why it cannot
// move as it is.
func rommPlanMove(g rommGame, targets rommTargets, existing []string, taken map[string]bool) rommItem {
	f := g.f
	destDir := rommTargetFolder(targets.Roms, g.sys, existing)
	item := rommItem{
		Kind: rommKindMisplaced, Action: rommActMove, Path: f.Path, Size: f.Size + f.PartsSize,
		System: g.sys.Name, Group: rommShortPath(destDir, targets), On: true,
		Reason: rommWhere(f) + " · " + g.how,
	}
	base := filepath.Base(f.Path)
	if len(f.Parts) > 0 {
		// A disc game moves as a unit under its own names: a renamed track no
		// longer matches its cue sheet.
		if f.Split {
			return rommItem{Kind: rommKindUnknown, Action: rommActNone, Path: f.Path, Size: item.Size,
				Group: "Not recognised", Reason: rommWhere(f) + " · its tracks are in other folders, so move it by hand"}
		}
		for _, p := range append([]string{f.Path}, f.Parts...) {
			dest := filepath.Join(destDir, filepath.Base(p))
			if rommFreeName(dest, taken) != dest {
				return rommItem{Kind: rommKindUnknown, Action: rommActNone, Path: f.Path, Size: item.Size,
					Group: "Not recognised", Reason: rommWhere(f) + " · " + item.Group + " already has a " + filepath.Base(p)}
			}
		}
		for _, p := range f.Parts {
			taken[strings.ToLower(filepath.Join(destDir, filepath.Base(p)))] = true
		}
		item.Parts = append([]string(nil), f.Parts...)
		item.Dest = filepath.Join(destDir, base)
		taken[strings.ToLower(item.Dest)] = true
		return item
	}
	dest := filepath.Join(destDir, base)
	if _, err := os.Lstat(dest); err == nil && rommSameContent(f.Path, dest) {
		return rommItem{Kind: rommKindDuplicate, Action: rommActDelete, Path: f.Path, Size: f.Size,
			Group: "Duplicates", Keep: dest,
			Reason: rommWhere(f) + " · already in " + item.Group}
	}
	item.Dest = rommFreeName(dest, taken)
	if item.Dest != dest {
		item.Reason += " · renamed: " + item.Group + " has another " + base
	}
	taken[strings.ToLower(item.Dest)] = true
	return item
}

// rommPlanBIOS turns a stray firmware file into a move into BIOS.
func rommPlanBIOS(f *rommFile, targets rommTargets, taken map[string]bool) rommItem {
	bios, _ := rommBIOSFor(f.Path, f.Size)
	name := filepath.Base(f.Path)
	dest := filepath.Join(targets.BIOS, bios.Sub, name)
	group := rommShortPath(filepath.Dir(dest), targets)
	label := bios.System + " BIOS"
	if _, err := os.Lstat(dest); err == nil {
		if rommSameContent(f.Path, dest) {
			return rommItem{Kind: rommKindDuplicate, Action: rommActDelete, Path: f.Path, Size: f.Size,
				Group: "Duplicates", Keep: dest, System: bios.System,
				Reason: rommWhere(f) + " · " + label + ", already in " + group}
		}
		return rommItem{Kind: rommKindUnknown, Action: rommActNone, Path: f.Path, Size: f.Size,
			Group: "Not recognised", System: bios.System,
			Reason: rommWhere(f) + " · " + group + " has a different " + name + ", so this one stays"}
	}
	if taken[strings.ToLower(dest)] {
		return rommItem{Kind: rommKindUnknown, Action: rommActNone, Path: f.Path, Size: f.Size,
			Group: "Not recognised", System: bios.System,
			Reason: rommWhere(f) + " · another " + name + " is already moving to " + group}
	}
	taken[strings.ToLower(dest)] = true
	return rommItem{Kind: rommKindBIOS, Action: rommActBIOS, Path: f.Path, Dest: dest, Size: f.Size,
		System: bios.System, Group: group, On: true, Reason: rommWhere(f) + " · " + label}
}

// rommSystemRank orders the move sections the way the plan lists consoles.
func rommSystemRank(name string) int {
	for i, s := range rommSystems {
		if s.Name == name {
			return i
		}
	}
	return len(rommSystems)
}

// rommSortItems orders the plan: moves by console, then firmware, duplicates,
// empty files and the files left alone; by path inside a section.
func rommSortItems(items []rommItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Kind == rommKindMisplaced {
			if ra, rb := rommSystemRank(a.System), rommSystemRank(b.System); ra != rb {
				return ra < rb
			}
			if a.Group != b.Group {
				return a.Group < b.Group
			}
		}
		return strings.ToLower(a.Path) < strings.ToLower(b.Path)
	})
}

// rommPlanCounts are the totals the summary card shows.
func rommPlanCounts(items []rommItem) (changes, moves, deletes int) {
	for _, it := range items {
		if !it.On {
			continue
		}
		switch it.Action {
		case rommActMove, rommActBIOS:
			moves++
			changes++
		case rommActDelete:
			deletes++
			changes++
		}
	}
	return changes, moves, deletes
}
