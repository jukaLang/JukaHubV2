package main

// romaudit.go — audit the games library against No-Intro and Redump DATs.
//
// The library already knows every ROM on the card (game_library.go) and the
// BIOS doctor already proves that the files a core needs are the right ones
// (biosdoctor.go). This is the same idea for the games themselves: a DAT is a
// clrmamepro list of the canonical releases of a console, with each release's
// CRC32 and size. A file whose CRC is in the DAT is a good dump; a file whose
// name is in the DAT but whose CRC is not is a bad dump; a DAT release with no
// file is missing; two files with one CRC are duplicates; and a file whose CRC
// matches a release under another name is a rename the user may want to make.
//
// The DATs are the user's: No-Intro and Redump publish them for exactly this.
// They go in <data>/rom-audit/dat (and <data>/rom-audit itself is read too), so
// nothing is downloaded behind the user's back and the audit works offline.
// Each DAT is matched to a library console by its clrmamepro name through
// romSystemAliases; a DAT for a console with no games on the card is skipped
// rather than printing a thousand "missing" lines for games the user never had.
//
// Cartridges are hashed by CRC32 the way gmeta_dat.go does for metadata: the
// whole file and the file without an iNES or SNES copier header. A zip or .7z
// gives its single ROM's CRC from its directory. Disc sheets (.cue, .gdi,
// .m3u, .chd) are matched by name only: Redump lists the tracks inside the
// disc, which a .chd does not contain byte-for-byte. Anything over
// romAuditHashMaxBytes is matched by name too, so a card full of large disc
// images does not turn an audit into an hour of reading.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// romAuditHashMaxBytes is the largest file whose CRC32 is computed. A
	// cartridge is far below it; a disc image above it is matched by name,
	// because Redump's track CRCs would not match a .chd anyway.
	romAuditHashMaxBytes = 256 << 20
	// romAuditDatMaxFiles bounds the DATs read in one audit.
	romAuditDatMaxFiles = 64
	// romAuditListMax is how many names one problem list shows on screen.
	// The saved report is not capped.
	romAuditListMax = 12
)

// romAuditStatus is what one library file turned out to be.
type romAuditStatus uint8

const (
	romAuditVerified romAuditStatus = iota
	romAuditRename
	romAuditBad
	romAuditUnknown
	romAuditNameOnly
	romAuditDuplicate
)

func (s romAuditStatus) String() string {
	switch s {
	case romAuditVerified:
		return "verified"
	case romAuditRename:
		return "rename"
	case romAuditBad:
		return "bad dump"
	case romAuditUnknown:
		return "not in DAT"
	case romAuditNameOnly:
		return "name only"
	case romAuditDuplicate:
		return "duplicate"
	}
	return "unknown"
}

// romAuditDatRom is one <rom> block of a DAT.
type romAuditDatRom struct {
	Name string
	Size int64
	CRC  string
	MD5  string
	SHA1 string
}

// romAuditDatGame is one <game> block: a canonical release.
type romAuditDatGame struct {
	Name        string
	Description string
	ROMs        []romAuditDatRom
}

// romAuditDat is one parsed DAT file.
type romAuditDat struct {
	Path  string
	Name  string
	Games []romAuditDatGame
}

// romAuditFile is one library file and what the DAT says about it.
type romAuditFile struct {
	Path      string
	System    string
	Stem      string
	Size      int64
	CRC       string
	Checked   bool
	Status    romAuditStatus
	DatName   string
	Suggest   string
	Duplicate string
}

// romAuditSystem is one console's part of the report.
type romAuditSystem struct {
	Name       string
	Collection string
	DatPath    string
	Listed     int
	Verified   int
	Missing    int
	Bad        int
	Unknown    int
	Renamed    int
	NameOnly   int
	Duplicates int

	MissingNames   []string
	BadFiles       []string
	RenameFiles    []string
	UnknownFiles   []string
	DuplicateFiles []string
	Files          []romAuditFile
}

// romAuditReport is one finished audit.
type romAuditReport struct {
	DatDir       string
	DatFiles     []string
	Listed       int
	Systems      []romAuditSystem
	FilesScanned int
	FilesChecked int
	SkippedDats  []string
	Errors       []string
	Started      time.Time
	Finished     time.Time
	Elapsed      time.Duration
}

// romAuditParseDAT reads one clrmamepro DAT. The tokenizer is gmeta_dat.go's,
// so the audit and the metadata lookup read the same dialect.
func romAuditParseDAT(data []byte, path string) *romAuditDat {
	toks := gmetaTokens(data)
	dat := &romAuditDat{Path: path, Name: romAuditHeaderName(toks)}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.quoted || !(strings.EqualFold(t.text, "game") || strings.EqualFold(t.text, "machine")) {
			continue
		}
		if i+1 >= len(toks) || !toks[i+1].is("(") {
			continue
		}
		game, next := romAuditParseGame(toks, i+2)
		i = next - 1
		if game.Name == "" {
			game.Name = game.Description
		}
		if game.Name != "" {
			dat.Games = append(dat.Games, game)
		}
	}
	if dat.Name == "" {
		dat.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return dat
}

// romAuditHeaderName reads the clrmamepro (or header) block's name, which is
// the console's No-Intro/Redump name ("Nintendo - Game Boy Advance").
func romAuditHeaderName(toks []gmetaToken) string {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].quoted {
			continue
		}
		if !strings.EqualFold(toks[i].text, "clrmamepro") && !strings.EqualFold(toks[i].text, "header") {
			continue
		}
		if !toks[i+1].is("(") {
			continue
		}
		for j := i + 2; j+1 < len(toks) && !toks[j].is(")"); j++ {
			if toks[j].quoted || !strings.EqualFold(toks[j].text, "name") {
				continue
			}
			if toks[j+1].is("(") || toks[j+1].is(")") {
				continue
			}
			return strings.TrimSpace(toks[j+1].text)
		}
	}
	return ""
}

// romAuditParseGame reads one game block from toks[i:] and returns the game
// and the index after its closing parenthesis.
func romAuditParseGame(toks []gmetaToken, i int) (romAuditDatGame, int) {
	g := romAuditDatGame{}
	for i < len(toks) {
		t := toks[i]
		if t.is(")") {
			return g, i + 1
		}
		if t.quoted {
			i++
			continue
		}
		key := strings.ToLower(t.text)
		if i+1 >= len(toks) {
			return g, len(toks)
		}
		v := toks[i+1]
		if v.is("(") {
			if key == "rom" {
				rom, next := romAuditParseRom(toks, i+2)
				g.ROMs = append(g.ROMs, rom)
				i = next
				continue
			}
			// disk, sample and any other nested block: skip it whole.
			i = romAuditSkipBlock(toks, i+1)
			continue
		}
		if v.is(")") {
			return g, i + 1
		}
		switch key {
		case "name":
			g.Name = strings.TrimSpace(v.text)
		case "description":
			g.Description = strings.TrimSpace(v.text)
		case "comment":
			if g.Name == "" {
				g.Name = strings.TrimSpace(v.text)
			}
		}
		i += 2
	}
	return g, i
}

// romAuditParseRom reads one rom block from toks[i:].
func romAuditParseRom(toks []gmetaToken, i int) (romAuditDatRom, int) {
	r := romAuditDatRom{}
	for i < len(toks) {
		t := toks[i]
		if t.is(")") {
			return r, i + 1
		}
		if t.quoted {
			i++
			continue
		}
		key := strings.ToLower(t.text)
		if i+1 >= len(toks) {
			return r, len(toks)
		}
		v := toks[i+1]
		if v.is("(") {
			i = romAuditSkipBlock(toks, i+1)
			continue
		}
		if v.is(")") {
			return r, i + 1
		}
		value := strings.TrimSpace(v.text)
		switch key {
		case "name":
			r.Name = value
		case "size":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				r.Size = n
			}
		case "crc":
			r.CRC = strings.ToUpper(value)
		case "md5":
			r.MD5 = strings.ToUpper(value)
		case "sha1":
			r.SHA1 = strings.ToUpper(value)
		}
		i += 2
	}
	return r, i
}

// romAuditSkipBlock returns the index after the block that starts at toks[i]
// (which must be "("). A malformed block ends at the end of the token stream.
func romAuditSkipBlock(toks []gmetaToken, i int) int {
	depth := 0
	for i < len(toks) {
		switch {
		case toks[i].is("("):
			depth++
		case toks[i].is(")"):
			depth--
			if depth <= 0 {
				return i + 1
			}
		}
		i++
	}
	return i
}

// romAuditLoadDAT reads and parses one DAT file.
func romAuditLoadDAT(path string) (*romAuditDat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, gmetaDatMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > gmetaDatMaxBytes {
		return nil, fmt.Errorf("larger than %d MB", gmetaDatMaxBytes>>20)
	}
	dat := romAuditParseDAT(data, path)
	if len(dat.Games) == 0 {
		return nil, fmt.Errorf("no games in this DAT")
	}
	return dat, nil
}

// romAuditDatDir is where the audit reads DATs: <data>/rom-audit/dat.
func romAuditDatDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "rom-audit", "dat")
}

// romAuditDatFiles lists the DATs on the card. Both the dat folder and its
// parent are read, so a user who drops a DAT straight into rom-audit still
// gets an audit.
func romAuditDatFiles() []string {
	dir := romAuditDatDir()
	if dir == "" {
		return nil
	}
	roots := []string{dir, filepath.Dir(dir)}
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		rootDepth := strings.Count(root, string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth >= 2 {
					return filepath.SkipDir
				}
				return nil
			}
			if len(out) >= romAuditDatMaxFiles || !strings.EqualFold(filepath.Ext(path), ".dat") {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > gmetaDatMaxBytes {
				return nil
			}
			clean := filepath.Clean(path)
			if !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// romAuditCRCs is a library file's CRC32s. The library index's free answer is
// used first; otherwise the file is read up to romAuditHashMaxBytes.
func romAuditCRCs(path string) ([]string, bool) {
	if crcs, ok := libIndexKnownCRCs(path); ok && len(crcs) > 0 {
		return crcs, true
	}
	return romFileCRCs(path, romAuditHashMaxBytes, false)
}

// romAuditStem is the file's name without its extension, with the folder-game
// rule romGameStem applies.
func romAuditStem(path string) string {
	return strings.TrimSpace(romGameStem(path))
}

// romAuditNameOnlyExt reports a disc sheet or a compressed disc: its name can
// be checked against a DAT, but its bytes cannot be compared with the track
// CRCs a Redump DAT lists.
func romAuditNameOnlyExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue", ".gdi", ".ccd", ".m3u", ".m3u8", ".chd", ".cso", ".rvz", ".wbfs", ".xiso":
		return true
	}
	return false
}

// romAuditRenameTarget is the file name a matched release should have.
func romAuditRenameTarget(datName, path string) string {
	if ext := filepath.Ext(path); ext != "" {
		return datName + ext
	}
	return datName
}

// romAuditSystemForCollection maps a DAT's clrmamepro name to the library's
// console name through romSystemAliases.
func romAuditSystemForCollection(collection string) string {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return ""
	}
	for _, alias := range romSystemAliases {
		if strings.EqualFold(alias.Collection, collection) {
			return alias.Name
		}
	}
	return ""
}

// romAuditDatMatchesAnyCRC reports whether the DAT lists a CRC the library
// already has, used to keep a DAT whose console name is not in the aliases.
func romAuditDatMatchesAnyCRC(dat *romAuditDat, crcs map[string]bool) bool {
	for _, game := range dat.Games {
		for _, rom := range game.ROMs {
			crc := strings.ToUpper(strings.TrimSpace(rom.CRC))
			if crc != "" && crcs[crc] {
				return true
			}
		}
	}
	return false
}

// romAuditBuild is the audit itself. It takes the library scan and the DAT
// paths, so a test can run it on a temporary folder without SDL or a card.
func romAuditBuild(scan romScanReport, datPaths []string) romAuditReport {
	report := romAuditReport{
		DatDir:       romAuditDatDir(),
		DatFiles:     append([]string(nil), datPaths...),
		FilesScanned: len(scan.Games),
		Started:      time.Now(),
	}
	present := map[string]bool{}
	for _, game := range scan.Games {
		present[game.System] = true
	}
	// No DATs is the first run's normal state: do not read the whole card to
	// say so.
	if len(datPaths) == 0 {
		report.Finished = time.Now()
		report.Elapsed = report.Finished.Sub(report.Started)
		return report
	}

	// Read the DATs before touching the card: only the consoles that actually
	// have a DAT need hashing, so a DAT for one console does not read a whole
	// card of games for the others.
	type loadedDat struct {
		dat    *romAuditDat
		system string
	}
	var loaded []loadedDat
	datSystems := map[string]bool{}
	unknownSystem := false
	for _, path := range datPaths {
		dat, err := romAuditLoadDAT(path)
		if err != nil {
			report.Errors = append(report.Errors, filepath.Base(path)+": "+err.Error())
			continue
		}
		system := romAuditSystemForCollection(dat.Name)
		if system == "" {
			system = dat.Name
			unknownSystem = true
		}
		if present[system] {
			datSystems[system] = true
		}
		loaded = append(loaded, loadedDat{dat: dat, system: system})
	}
	// A DAT whose console name is not a library name may still be the right
	// set under another name; every file is hashed in that case so a CRC can
	// prove it.
	hashAll := unknownSystem
	toHash := 0
	for _, game := range scan.Games {
		if hashAll || datSystems[game.System] {
			toHash++
		}
	}
	romAuditProgress.total.Store(int64(toHash))
	romAuditProgress.checked.Store(0)

	// One pass over the files that need it: hash each once, and remember its
	// CRCs for every DAT that follows.
	files := make([]romAuditFile, 0, len(scan.Games))
	crcsByPath := make(map[string][]string, len(scan.Games))
	globalCRCs := map[string]bool{}
	done := 0
	for _, game := range scan.Games {
		file := romAuditFile{
			Path:   game.Path,
			System: game.System,
			Stem:   romAuditStem(game.Path),
		}
		if !hashAll && !datSystems[game.System] {
			files = append(files, file)
			continue
		}
		if info, err := os.Stat(game.Path); err == nil {
			file.Size = info.Size()
		}
		crcs, ok := romAuditCRCs(game.Path)
		crcsByPath[game.Path] = crcs
		if ok && len(crcs) > 0 {
			file.CRC = strings.ToUpper(strings.TrimSpace(crcs[0]))
			file.Checked = true
			report.FilesChecked++
			for _, crc := range crcs {
				globalCRCs[strings.ToUpper(strings.TrimSpace(crc))] = true
			}
		}
		files = append(files, file)
		done++
		romAuditProgress.checked.Store(int64(done))
	}

	seenSystem := map[string]bool{}
	for _, item := range loaded {
		if present[item.system] {
			if seenSystem[item.system] {
				report.SkippedDats = append(report.SkippedDats, item.dat.Name+" (a second DAT for "+item.system+")")
				continue
			}
			seenSystem[item.system] = true
		} else if !romAuditDatMatchesAnyCRC(item.dat, globalCRCs) {
			report.SkippedDats = append(report.SkippedDats, item.dat.Name)
			continue
		}
		report.Listed += len(item.dat.Games)
		report.Systems = append(report.Systems, romAuditAuditDat(item.dat, item.system, files, crcsByPath, present[item.system]))
	}

	sort.SliceStable(report.Systems, func(i, j int) bool {
		ri, iKnown := consoleOrder[report.Systems[i].Name]
		rj, jKnown := consoleOrder[report.Systems[j].Name]
		switch {
		case iKnown && jKnown:
			return ri < rj
		case iKnown != jKnown:
			return iKnown
		default:
			return strings.ToLower(report.Systems[i].Name) < strings.ToLower(report.Systems[j].Name)
		}
	})
	report.Finished = time.Now()
	report.Elapsed = report.Finished.Sub(report.Started)
	return report
}

// romAuditFileMatchesDAT reports whether a file has any CRC or title the DAT
// lists. It is used only when the DAT's console name is not a library name.
func romAuditFileMatchesDAT(file romAuditFile, crcs []string, byCRC, byNorm map[string]int) bool {
	for _, crc := range crcs {
		if _, ok := byCRC[strings.ToUpper(strings.TrimSpace(crc))]; ok {
			return true
		}
	}
	if norm := artworkNormalizeTitle(file.Stem); norm != "" {
		_, ok := byNorm[norm]
		return ok
	}
	return false
}

// romAuditAuditDat matches one DAT against the library files of its console.
func romAuditAuditDat(dat *romAuditDat, system string, files []romAuditFile,
	crcsByPath map[string][]string, systemPresent bool) romAuditSystem {
	sys := romAuditSystem{
		Name:       system,
		Collection: dat.Name,
		DatPath:    dat.Path,
		Listed:     len(dat.Games),
	}
	byCRC := map[string]int{}
	byNorm := map[string]int{}
	for i, game := range dat.Games {
		for _, rom := range game.ROMs {
			crc := strings.ToUpper(strings.TrimSpace(rom.CRC))
			if crc == "" {
				continue
			}
			if old, ok := byCRC[crc]; !ok || gmetaRegionRank(game.Name) < gmetaRegionRank(dat.Games[old].Name) {
				byCRC[crc] = i
			}
		}
		if norm := artworkNormalizeTitle(game.Name); norm != "" {
			if old, ok := byNorm[norm]; !ok || gmetaRegionRank(game.Name) < gmetaRegionRank(dat.Games[old].Name) {
				byNorm[norm] = i
			}
		}
	}

	// The console's own files when the DAT names a console on the card. When
	// the name is not one the library knows, only the files whose CRC or title
	// actually matches the DAT are considered: a DAT kept for a renamed console
	// must not turn every other console's games into "not in DAT".
	indexes := make([]int, 0, len(files))
	for i := range files {
		if systemPresent {
			if files[i].System == system {
				indexes = append(indexes, i)
			}
			continue
		}
		if romAuditFileMatchesDAT(files[i], crcsByPath[files[i].Path], byCRC, byNorm) {
			indexes = append(indexes, i)
		}
	}
	matched := make([]bool, len(dat.Games))
	for _, fi := range indexes {
		file := &files[fi]
		game := -1
		for _, crc := range crcsByPath[file.Path] {
			if g, ok := byCRC[strings.ToUpper(strings.TrimSpace(crc))]; ok {
				game = g
				break
			}
		}
		if game >= 0 {
			matched[game] = true
			file.DatName = dat.Games[game].Name
			if !strings.EqualFold(file.Stem, file.DatName) {
				file.Status = romAuditRename
				file.Suggest = romAuditRenameTarget(file.DatName, file.Path)
			} else {
				file.Status = romAuditVerified
			}
			continue
		}
		if norm := artworkNormalizeTitle(file.Stem); norm != "" {
			if g, ok := byNorm[norm]; ok {
				matched[g] = true
				file.DatName = dat.Games[g].Name
				if !file.Checked || romAuditNameOnlyExt(file.Path) {
					file.Status = romAuditNameOnly
				} else {
					file.Status = romAuditBad
				}
				continue
			}
		}
		file.Status = romAuditUnknown
	}

	// Duplicates: the first file with a CRC keeps its own verdict; every later
	// copy is reported against it. The DAT release is the key when there is
	// one, because the same dump may have a header on one copy and not the
	// other, so their whole-file CRCs differ.
	firstByCRC := map[string]int{}
	for _, fi := range indexes {
		file := &files[fi]
		key := file.CRC
		if file.DatName != "" && (file.Status == romAuditVerified || file.Status == romAuditRename) {
			key = "dat:" + file.DatName
		}
		if key == "" {
			continue
		}
		if first, ok := firstByCRC[key]; ok {
			file.Status = romAuditDuplicate
			file.Duplicate = files[first].Path
			sys.Duplicates++
		} else {
			firstByCRC[key] = fi
		}
	}

	for _, fi := range indexes {
		file := files[fi]
		sys.Files = append(sys.Files, file)
		switch file.Status {
		case romAuditVerified:
			sys.Verified++
		case romAuditRename:
			sys.Renamed++
			sys.RenameFiles = append(sys.RenameFiles, file.Path+" -> "+file.Suggest)
		case romAuditBad:
			sys.Bad++
			sys.BadFiles = append(sys.BadFiles, file.Path+" (DAT: "+file.DatName+")")
		case romAuditUnknown:
			sys.Unknown++
			sys.UnknownFiles = append(sys.UnknownFiles, file.Path)
		case romAuditNameOnly:
			sys.NameOnly++
		case romAuditDuplicate:
			sys.DuplicateFiles = append(sys.DuplicateFiles, file.Path+" (also "+file.Duplicate+")")
		}
	}
	for i, game := range dat.Games {
		if !matched[i] {
			sys.Missing++
			sys.MissingNames = append(sys.MissingNames, game.Name)
		}
	}
	return sys
}

// ── Scan state ──────────────────────────────────────────────────────────────

var romAuditScan struct {
	sync.Mutex
	running bool
	report  *romAuditReport
}

var romAuditProgress struct {
	checked atomic.Int64
	total   atomic.Int64
}

// romAuditStart begins an audit in the background. The library scan and the
// hashing both read the card, so neither may run on the event thread.
func romAuditStart() {
	romAuditScan.Lock()
	if romAuditScan.running {
		romAuditScan.Unlock()
		return
	}
	romAuditScan.running = true
	romAuditScan.Unlock()
	go func() {
		scan := scanROMLibrary(romLibraryRoots())
		romAuditProgress.total.Store(int64(len(scan.Games)))
		romAuditProgress.checked.Store(0)
		report := romAuditBuild(scan, romAuditDatFiles())
		romAuditScan.Lock()
		romAuditScan.report = &report
		romAuditScan.running = false
		romAuditScan.Unlock()
	}()
}

// romAuditSnapshot is the latest finished report (nil before the first one),
// whether a scan is in flight, and its progress.
func romAuditSnapshot() (*romAuditReport, bool, int, int) {
	romAuditScan.Lock()
	report, running := romAuditScan.report, romAuditScan.running
	romAuditScan.Unlock()
	return report, running, int(romAuditProgress.checked.Load()), int(romAuditProgress.total.Load())
}

// ── Report ──────────────────────────────────────────────────────────────────

// romAuditList appends one problem list, capped on screen and complete in the
// saved report.
func romAuditList(lines []string, label string, items []string, full bool) []string {
	if len(items) == 0 {
		return lines
	}
	lines = append(lines, "  "+label+":")
	show := items
	if !full && len(show) > romAuditListMax {
		show = show[:romAuditListMax]
	}
	for _, item := range show {
		lines = append(lines, "    "+item)
	}
	if !full && len(items) > len(show) {
		lines = append(lines, "    ... and "+itoa(len(items)-len(show))+" more (X saves the full report)")
	}
	return lines
}

// romAuditLines is the report as text: the screen shows the capped version,
// the saved file the whole thing.
func romAuditLines(report romAuditReport, scanning bool, checked, total int, full bool) []string {
	var lines []string
	if scanning {
		if total > 0 {
			lines = append(lines, "Scanning the library... "+itoa(checked)+" of "+itoa(total)+" files checked")
		} else {
			lines = append(lines, "Scanning the library...")
		}
	}
	if len(report.DatFiles) == 0 {
		lines = append(lines, "No DAT files found.")
		lines = append(lines, "Put No-Intro or Redump .dat files in:")
		if dir := romAuditDatDir(); dir != "" {
			lines = append(lines, "  "+dir)
		}
		lines = append(lines, "Then press Y to scan again.")
		return lines
	}
	if report.Finished.IsZero() {
		lines = append(lines, "The audit has not run yet.")
		return lines
	}

	head := "Audited " + pluralizeCount(report.FilesScanned, "file", "files") +
		" against " + pluralizeCount(len(report.DatFiles), "DAT", "DATs") +
		" (" + pluralizeCount(report.Listed, "release", "releases") + ")"
	lines = append(lines, head)
	byName := report.FilesScanned - report.FilesChecked
	detail := "  " + pluralizeCount(report.FilesChecked, "file", "files") + " checked by CRC32"
	if byName > 0 {
		detail += " - " + pluralizeCount(byName, "file", "files") + " had no CRC32 (large files and disc sheets are matched by name)"
	}
	lines = append(lines, detail)
	if report.Elapsed > 0 {
		lines = append(lines, "  Took "+report.Elapsed.Round(time.Second).String())
	}
	if len(report.SkippedDats) > 0 {
		lines = append(lines, "  Skipped "+pluralizeCount(len(report.SkippedDats), "DAT", "DATs")+" for consoles with no games on the card")
	}
	for _, err := range report.Errors {
		lines = append(lines, "  Could not read "+err)
	}
	lines = append(lines, "")

	if len(report.Systems) == 0 {
		lines = append(lines, "No DAT matched a console in the library.")
		lines = append(lines, "Check that the DAT is a No-Intro or Redump set for a console you have.")
		return lines
	}
	for _, sys := range report.Systems {
		summary := sys.Name + " - " + itoa(sys.Verified) + " verified"
		if sys.Missing > 0 {
			summary += " - " + itoa(sys.Missing) + " missing"
		}
		if sys.Bad > 0 {
			summary += " - " + itoa(sys.Bad) + " bad"
		}
		if sys.Renamed > 0 {
			summary += " - " + itoa(sys.Renamed) + " rename"
		}
		if sys.Duplicates > 0 {
			summary += " - " + itoa(sys.Duplicates) + " duplicate"
		}
		if sys.Unknown > 0 {
			summary += " - " + itoa(sys.Unknown) + " not in DAT"
		}
		if sys.NameOnly > 0 {
			summary += " - " + itoa(sys.NameOnly) + " name only"
		}
		lines = append(lines, summary)
		lines = romAuditList(lines, "Missing", sys.MissingNames, full)
		lines = romAuditList(lines, "Bad dumps", sys.BadFiles, full)
		lines = romAuditList(lines, "Rename suggestions", sys.RenameFiles, full)
		lines = romAuditList(lines, "Duplicates", sys.DuplicateFiles, full)
		lines = romAuditList(lines, "Not in the DAT", sys.UnknownFiles, full)
		if sys.Missing == 0 && sys.Bad == 0 && sys.Renamed == 0 && sys.Duplicates == 0 && sys.Unknown == 0 {
			lines = append(lines, "  Every game matches.")
		}
		lines = append(lines, "")
	}
	if full {
		if dir := romAuditDatDir(); dir != "" {
			lines = append(lines, "DAT folder: "+dir)
		}
	}
	return lines
}

// romAuditExport writes the full report next to the DATs and returns its path.
func romAuditExport() {
	report, _, _, _ := romAuditSnapshot()
	if report == nil {
		ShowToast("Run the audit first (Y)", ToastKindInfo)
		return
	}
	path, err := romAuditWriteReport(*report)
	if err != nil {
		ShowToast("Could not save the audit report: "+err.Error(), ToastKindError)
		return
	}
	ShowToast("Audit report saved to "+path, ToastKindSuccess)
}

// romAuditWriteReport saves the uncapped report in <data>/rom-audit.
func romAuditWriteReport(report romAuditReport) (string, error) {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("no data folder")
	}
	dir = filepath.Join(dir, "rom-audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "rom-audit-"+time.Now().Format("20060102-1504")+".txt")
	text := strings.Join(romAuditLines(report, false, 0, 0, true), "\n") + "\n"
	if err := AtomicWrite(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
