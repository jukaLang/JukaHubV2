package main

// files_extract_ui.go — the archive sheet in Files and how an extraction
// reports back.
//
// A on an archive (one that is not a game in a console folder — those start)
// turns the Files action sheet into the archive's own sheet: its name, the
// format and size, the first few things in it (a zip, or a 7z or RAR when
// 7-Zip is here to list it), and two rows, Extract and Extract here
// (files_extract.go says what each does). The sheet is the same feSheet the Y
// menu uses, with feSheet.archive set, so it is drawn, moved through and
// clicked by the code that already does that for the Y menu. Y → Extract…
// opens it for the row under the cursor (a zipped game included), and with
// several archives marked, extracts them all.
//
// The work runs on the extraction queue. Its progress shows in the sheet while
// it is open, along the bottom of the Files bar once it is closed, and in the
// activity strip on every other screen. When a job ends, feExtractDrain
// (called every frame by the main loop) closes its sheet, relists the folder if
// Files is showing it — the cursor keeps its file, or moves onto what came out
// when it was still on the archive — and says how it went in a toast.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- What the sheet knows about an archive --------------------------------------

// feArchiveTopItem is one top-level item of an archive.
type feArchiveTopItem struct {
	Name  string
	IsDir bool
	Files int
	Bytes int64
}

// feArchiveInfo is what the sheet shows about an archive before it is
// unpacked.
type feArchiveInfo struct {
	Path      string
	Dest      string
	Kind      feArchiveKind
	Packed    int64
	Listed    bool // Files, Bytes and Top are known (a zip, or a 7-Zip listing)
	Files     int
	Dirs      int
	Bytes     int64
	Top       []feArchiveTopItem
	TopCount  int
	Conflicts int    // files Extract here would replace; -1 while unknown
	SmartName string // the name Extract would give the result, "" when unknown
	SmartDir  bool   // SmartName is a folder
	Encrypted bool
	Links     int    // symbolic links a 7-Zip listing showed
	Method    string // a zip compression method only 7-Zip reads
	NeedTool  bool   // the archive needs 7-Zip (or, for RAR on Windows, tar)
	Tool      string // the program that would unpack it, "" when there is none
	Free      int64  // free bytes in Dest, -1 when unknown
	Err       string // why it cannot be read, as a clause ("it is …")
}

// feArchiveItem is one entry of an archive as the inspection counts it.
type feArchiveItem struct {
	name  string
	isDir bool
	size  int64
}

const (
	// feArchiveTopShown is how many top-level items the sheet lists.
	feArchiveTopShown = 4
	// feArchiveStatCap bounds the stats Extract here's conflict count costs;
	// past it the count stays unknown, and Extract here asks twice.
	feArchiveStatCap = 20000
)

// feArchiveInspect reads what the sheet shows: for a zip its directory (it
// sits at the end of the file, so this unpacks nothing), for a 7z or RAR the
// listing 7-Zip gives, and for a tar what the name, the first bytes and the
// size say, because a tar lists its contents only by being unpacked. It also
// answers the relative paths of the files, for the conflict count.
func feArchiveInspect(ctx context.Context, path, dest string) (feArchiveInfo, []string) {
	info := feArchiveInfo{Path: path, Dest: dest, Conflicts: -1, Free: -1}
	st, err := os.Stat(path)
	if err != nil {
		info.Err = "it cannot be read: " + filesFriendlyError(err)
		return info, nil
	}
	info.Packed = st.Size()
	info.Kind = feArchiveDetect(path)
	if free, err := feExtractFreeSpace(dest); err == nil {
		info.Free = free
	}
	var items []feArchiveItem
	switch info.Kind {
	case feArchiveZip:
		items = feArchiveZipItems(&info)
	case feArchive7z, feArchiveRar:
		info.NeedTool = true
	case feArchiveTarXz, feArchiveXz:
		if f, err := os.Open(path); err == nil {
			if dict, big := feXzDictTooBig(f, info.Packed, feXzMaxDict); big {
				info.Err = feXzMemoryError(dict).Error()
			}
			_ = f.Close()
		}
	case feArchiveNone:
		info.Err = errFeNotArchive.Error()
	}
	if info.Method != "" {
		info.NeedTool = true
	}
	if info.NeedTool && info.Err == "" {
		tool := feArchiveToolFor(info.Kind)
		info.Tool = tool.Path
		if tool.Kind == feTool7Zip && !info.Listed {
			items = feArchive7ZipItems(ctx, &info, tool.Path)
		}
	}
	rels := feArchiveTally(&info, items)
	if info.Listed {
		feArchivePredict(&info)
	}
	return info, rels
}

// feArchiveZipItems lists a zip's directory: totals come from it, and so do
// encryption and a method only 7-Zip reads.
func feArchiveZipItems(info *feArchiveInfo) []feArchiveItem {
	zr, err := feOpenZip(info.Path)
	if err != nil {
		info.Err = feArchiveOpenClause(err)
		return nil
	}
	defer zr.Close()
	info.Listed = true
	items := make([]feArchiveItem, 0, len(zr.File))
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			info.Encrypted = true
		}
		isDir := f.FileInfo().IsDir()
		if !isDir && !f.Mode().IsRegular() {
			continue
		}
		if !isDir && info.Method == "" && f.Method != zip.Store && f.Method != zip.Deflate {
			info.Method = feZipMethodName(f.Method)
		}
		items = append(items, feArchiveItem{name: feZipEntryName(f), isDir: isDir, size: declaredEntryBytes(f.UncompressedSize64)})
	}
	return items
}

// feArchive7ZipItems lists a 7z or a RAR with 7-Zip. A listing 7-Zip cannot
// give leaves the archive unlisted rather than blocked: unpacking may still
// work, and says why when it does not.
func feArchive7ZipItems(ctx context.Context, info *feArchiveInfo, tool string) []feArchiveItem {
	entries, err := feList7Zip(ctx, tool, info.Path)
	switch {
	case errors.Is(err, errFeArchiveEncrypted):
		info.Encrypted = true
		return nil
	case errors.Is(err, errFeTooMany):
		info.Err = err.Error()
		return nil
	case errors.Is(err, context.Canceled):
		return nil
	case err != nil:
		// Extraction refuses what 7-Zip cannot list; the sheet says so now.
		logWarn("files", "inspect %s: %v", info.Path, err)
		info.Err = "7-Zip could not list what is in it — it may be damaged"
		return nil
	}
	info.Listed = true
	items := make([]feArchiveItem, 0, len(entries))
	for _, e := range entries {
		if e.Encrypted {
			info.Encrypted = true
		}
		if e.Link {
			info.Links++
			continue
		}
		items = append(items, feArchiveItem{name: e.Path, isDir: e.Dir, size: e.Size})
	}
	return items
}

// feArchiveOpenClause says why a zip's directory could not be read.
func feArchiveOpenClause(err error) string {
	if errors.Is(err, errFeTooMany) {
		return err.Error()
	}
	if errors.Is(err, errBadToolArchive) {
		return "it is damaged, or not a zip JukaHub can read"
	}
	return "it cannot be read: " + filesFriendlyError(err)
}

// feArchiveTally fills info's totals and top-level items from the entries, the
// way extraction will name them, and returns the files' relative paths. Names
// are grouped without regard to case: on the card "Game/a" and "game/b" end up
// in one folder.
func feArchiveTally(info *feArchiveInfo, items []feArchiveItem) []string {
	index := map[string]int{}
	var tops []feArchiveTopItem
	var rels []string
	for _, it := range items {
		rel, reason := feExtractRel(it.name)
		if reason != "" || rel == "" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		key := strings.ToLower(parts[0])
		i, ok := index[key]
		if !ok {
			i = len(tops)
			index[key] = i
			tops = append(tops, feArchiveTopItem{Name: parts[0]})
		}
		if it.isDir || len(parts) > 1 {
			tops[i].IsDir = true
		}
		if it.isDir {
			info.Dirs++
			continue
		}
		info.Files++
		info.Bytes = satAddBytes(info.Bytes, it.size)
		tops[i].Files++
		tops[i].Bytes = satAddBytes(tops[i].Bytes, it.size)
		rels = append(rels, rel)
	}
	info.TopCount = len(tops)
	if len(tops) > feArchiveTopShown {
		tops = tops[:feArchiveTopShown]
	}
	info.Top = tops
	return rels
}

// feArchivePredict works out the name Extract will give the result, for the
// sheet's row: the lone top-level item's, or a new folder named after the
// archive, numbered when the name is taken.
func feArchivePredict(info *feArchiveInfo) {
	if info.TopCount == 0 {
		return
	}
	name, isDir := feArchiveStem(info.Path), true
	if info.TopCount == 1 && len(info.Top) == 1 {
		name, isDir = info.Top[0].Name, info.Top[0].IsDir
	}
	target, err := feExtractFreeName(info.Dest, name, isDir)
	if err != nil {
		return
	}
	info.SmartName, info.SmartDir = filepath.Base(target), isDir
}

// feArchiveCountConflicts counts the files Extract here would replace. The
// archive itself is never replaced, so it does not count. It gives up (-1)
// when wanted turns false: a newer inspection replaced this one.
func feArchiveCountConflicts(dest, archive string, rels []string, wanted func() bool) int {
	conflicts := 0
	own := filepath.Base(archive)
	for i, rel := range rels {
		if i%256 == 0 && !wanted() {
			return -1
		}
		if strings.EqualFold(rel, own) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dest, rel)); err == nil && feMergeReplaceable(fi) {
			conflicts++
		}
	}
	return conflicts
}

// feArchiveInfoCache holds the inspection of the archive the sheet is open
// on. It is filled off the SDL thread — the listing first, the conflict count
// (a stat per file on a slow card) after it — and read by the sheet every
// frame. Until the listing is ready it holds what the name and the size say.
var feArchiveInfoCache struct {
	mu     sync.Mutex
	path   string
	gen    int
	ready  bool
	info   feArchiveInfo
	cancel context.CancelFunc // stops the inspection that is running
}

// feArchiveInspectAsync starts reading path for the sheet.
func feArchiveInspectAsync(path, dest string, size int64) {
	c := &feArchiveInfoCache
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.gen++
	gen := c.gen
	c.path, c.ready, c.cancel = path, false, cancel
	c.info = feArchiveInfo{Path: path, Dest: dest, Kind: feArchiveKindOf(path), Packed: size, Conflicts: -1, Free: -1}
	c.mu.Unlock()
	wanted := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.gen == gen
	}
	publish := func(info feArchiveInfo) {
		c.mu.Lock()
		if c.gen == gen {
			c.info, c.ready = info, true
		}
		c.mu.Unlock()
	}
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logWarn("files", "inspect %s: %v", path, r)
				publish(feArchiveInfo{Path: path, Dest: dest, Conflicts: -1, Free: -1, Err: "it could not be read"})
			}
		}()
		info, rels := feArchiveInspect(ctx, path, dest)
		if ctx.Err() != nil {
			return
		}
		if info.Listed && len(rels) == 0 {
			info.Conflicts = 0
		}
		publish(info)
		if len(rels) == 0 || len(rels) > feArchiveStatCap || feExtractPlace(path) > 0 {
			return
		}
		if n := feArchiveCountConflicts(dest, path, rels, wanted); n >= 0 {
			info.Conflicts = n
			publish(info)
		}
	}()
}

// feArchiveInfoPeek is what is known about path: the inspection once ready
// (true), else what its name and size said (false).
func feArchiveInfoPeek(path string) (feArchiveInfo, bool) {
	c := &feArchiveInfoCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if path == "" || c.path != path {
		return feArchiveInfo{Path: path, Kind: feArchiveKindOf(path), Conflicts: -1, Free: -1}, false
	}
	return c.info, c.ready
}

// feArchiveInfoForget drops the inspection of path: an extraction changed the
// folder it counted conflicts in.
func feArchiveInfoForget(path string) {
	c := &feArchiveInfoCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != "" && filesSamePath(c.path, path) {
		if c.cancel != nil {
			c.cancel()
		}
		c.gen++
		c.path, c.ready, c.info, c.cancel = "", false, feArchiveInfo{}, nil
	}
}

// feArchiveInspectStop stops an inspection still running for a sheet that
// closed (a 7-Zip listing of a big RAR can take a while). What it read stays.
func feArchiveInspectStop() {
	c := &feArchiveInfoCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil && !c.ready {
		c.cancel()
		c.gen++
		c.path, c.cancel = "", nil
	}
}

// feArchiveReinspect reads the sheet's archive again (another job just wrote
// into its folder).
func feArchiveReinspect(path string) {
	info, _ := feArchiveInfoPeek(path)
	feArchiveInspectAsync(path, filepath.Dir(path), info.Packed)
}

// --- Opening the sheet ------------------------------------------------------------

// feArchiveOpensSheet reports whether A on entry opens the archive sheet: an
// archive, unless it is a game in a console folder (those start instead). It
// reads no disk; the footer asks it every frame.
func feArchiveOpensSheet(entry FileEntry) bool {
	return !entry.IsDir && entry.Name != ".." && feArchiveKindOf(entry.Name) != feArchiveNone &&
		filesROMSystem(entry.Path) == ""
}

// openFeArchiveSheet opens the archive sheet for path. A path from the listing
// is checked against the Files root first; trusted is for the archive of a
// job already in the queue, which may sit in a folder the user has left.
func openFeArchiveSheet(config *Config, path string, trusted bool) bool {
	resolved := path
	if !trusted {
		r, err := resolveExplorerEntry(config, path)
		if err != nil {
			showToast("Cannot open "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
			return false
		}
		resolved = r
	}
	var size int64
	if fi, err := os.Stat(resolved); err == nil {
		size = fi.Size()
	}
	feSheet = feSheetState{open: true, archive: resolved}
	feSheetRowHits = nil
	feSheetRect = sdl.Rect{}
	feArchiveInspectAsync(resolved, filepath.Dir(resolved), size)
	PlayActivateSound()
	return true
}

// feArchiveSheetBusy reports an archive sheet whose archive is in the queue:
// B then hides it, and the extraction carries on.
func feArchiveSheetBusy() bool {
	return feSheet.open && feSheet.archive != "" && feExtractPlace(feSheet.archive) > 0
}

// --- The rows -------------------------------------------------------------------

// feArchiveTargets are the archives among an action's targets.
func feArchiveTargets(targets []FileEntry) []FileEntry {
	var out []FileEntry
	for _, t := range targets {
		if !t.IsDir && t.Name != ".." && feArchiveKindOf(t.Name) != feArchiveNone {
			out = append(out, t)
		}
	}
	return out
}

// feArchiveMainAction is the Y menu's archive row: Extract… for the archive
// under the cursor (or every marked one), the running extraction when nothing
// else is an archive, and a disabled row otherwise. It is always one row, so
// the rows below it never move under the cursor.
func feArchiveMainAction(targets []FileEntry) feAction {
	archives := feArchiveTargets(targets)
	switch {
	case len(archives) == 1:
		return feAction{Kind: feActArchive, Label: "Extract…", Detail: archives[0].Name, Enabled: true}
	case len(archives) > 1:
		return feAction{Kind: feActArchive, Label: "Extract", Detail: feCountWord(len(archives), "archive", "archives"), Enabled: true}
	}
	if s := feExtractSnapshot(); s.Active {
		detail := ellipsizeRunes(filepath.Base(s.Job.Archive), 22)
		if pct := feExtractPercent(s.Reading); pct >= 0 {
			detail += fmt.Sprintf(" · %d%%", pct)
		}
		return feAction{Kind: feActArchive, Label: "Extracting", Detail: detail, Enabled: true}
	}
	return feAction{Kind: feActArchive, Label: "Extract…", Detail: "pick an archive", Enabled: false}
}

// feArchiveConflictsNow is Extract here's conflict count for this moment: a
// job unpacking (or waiting to unpack) into the same folder makes a count
// taken before it ran unknown.
func feArchiveConflictsNow(info feArchiveInfo) int {
	if info.Conflicts == 0 && feExtractDestBusy(info.Dest) && feExtractPlace(info.Path) == 0 {
		return -1
	}
	return info.Conflicts
}

// feArchiveActions are the archive sheet's two rows. While the archive is in
// the queue the first row reports on it (pressing it again only says so) and
// the second cancels it; the cursor is put back on the first row when a job
// starts, so a doubled press cannot call the extraction off.
func feArchiveActions(config *Config) []feAction {
	path := feSheet.archive
	extract := feAction{Kind: feActExtract, Label: "Extract", Detail: "reading…", Enabled: true}
	here := feAction{Kind: feActExtractHere, Label: "Extract here", Detail: "reading…", Enabled: true}
	if info, ready := feArchiveInfoPeek(path); ready {
		extract.Detail = feArchiveSmartDetail(info)
		here.Detail = feArchiveMergeDetail(feArchiveConflictsNow(info))
		switch {
		case feArchiveBlocker(info) != "":
			// The reason is the sheet's warning line, and a press toasts it.
			extract.Detail, here.Detail = "not possible", "not possible"
			extract.Enabled, here.Enabled = false, false
		case info.Listed && info.TopCount == 0:
			extract.Detail, here.Detail = "nothing to extract", "nothing to extract"
			extract.Enabled, here.Enabled = false, false
		}
	}
	switch place := feExtractPlace(path); {
	case place == 1:
		s := feExtractSnapshot()
		detail := "working…"
		if entry := s.Reading.Entry; entry != "" {
			detail = filepath.Base(entry)
		}
		extract = feAction{Kind: feActExtract, Label: "Extracting", Detail: detail, Enabled: true}
		here = feAction{Kind: feActExtractCancel, Label: "Cancel extraction", Detail: "the folder stays as it was", Enabled: true}
		if s.Reading.Phase == "Finishing" {
			here = feAction{Kind: feActExtractCancel, Label: "Finishing", Detail: "moving it into place", Enabled: false}
		}
	case place > 1:
		extract = feAction{Kind: feActExtract, Label: "Waiting its turn", Detail: fmt.Sprintf("%d ahead", place-1), Enabled: true}
		here = feAction{Kind: feActExtractCancel, Label: "Cancel", Detail: "take it out of the queue", Enabled: true}
	}
	return []feAction{extract, here}
}

// feArchiveDisabledReason is what a press on a disabled archive row says, ""
// for the generic answer.
func feArchiveDisabledReason(action feAction) string {
	if feSheet.archive == "" {
		if action.Kind == feActArchive {
			return hintSentence("Pick an archive to extract, or mark several with %s", "X", "Mark")
		}
		return ""
	}
	name := filepath.Base(feSheet.archive)
	if info, ready := feArchiveInfoPeek(feSheet.archive); ready {
		if why := feArchiveBlocker(info); why != "" {
			return "Cannot extract " + name + ": " + why
		}
		if info.Listed && info.TopCount == 0 {
			return name + " holds nothing to extract"
		}
	}
	if action.Kind == feActExtractCancel {
		return name + " is being moved into place and can no longer be stopped"
	}
	return ""
}

// feArchiveSmartDetail says where Extract puts the result.
func feArchiveSmartDetail(info feArchiveInfo) string {
	if info.SmartName == "" {
		return "to a folder or file of its own"
	}
	switch {
	case info.SmartDir && info.TopCount > 1:
		return "→ new folder " + info.SmartName + "/"
	case info.SmartDir:
		return "→ " + info.SmartName + "/"
	}
	return "→ " + info.SmartName
}

// feArchiveMergeDetail says what Extract here does to the folder, for a
// conflict count (-1: unknown).
func feArchiveMergeDetail(conflicts int) string {
	switch {
	case conflicts > 0:
		return "replaces " + feFilesWord(conflicts)
	case conflicts == 0:
		return "into this folder"
	}
	return "into this folder · may replace files"
}

// feFilesWord is "1 file" or "N files".
func feFilesWord(n int) string {
	return feCountWord(n, "file", "files")
}

// feCountWord is "1 thing" or "N things".
func feCountWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// feArchiveBlocker is why an archive cannot be extracted here, as a clause,
// or "" when it can.
func feArchiveBlocker(info feArchiveInfo) string {
	switch {
	case info.Err != "":
		return info.Err
	case info.Encrypted:
		return "it is password-protected (JukaHub cannot ask for passwords yet)"
	case info.NeedTool && info.Tool == "" && !(info.Kind == feArchive7z && IsWindows()):
		return feArchiveToolMissing(info)
	case info.Links > 0:
		return errFeHasLinks.Error()
	case info.Listed && info.Free >= 0 && satAddBytes(info.Bytes, feExtractSpaceMargin) > info.Free:
		return feNoSpaceError{need: satAddBytes(info.Bytes, feExtractSpaceMargin), free: info.Free}.Error()
	}
	return ""
}

// feArchiveToolMissing explains an archive only 7-Zip reads on a device that
// has none.
func feArchiveToolMissing(info feArchiveInfo) string {
	what := "it needs 7-Zip"
	if info.Method != "" {
		what = "its " + info.Method + " compression needs 7-Zip"
	}
	if IsWindows() {
		return what + " (7-zip.org)"
	}
	return what + " (7zz), which is not on this device"
}

// feArchiveSummary is the sheet's subtitle: the format and what it holds.
func feArchiveSummary(info feArchiveInfo) string {
	parts := []string{feArchiveLabel(info.Kind)}
	switch {
	case info.Listed:
		parts = append(parts, feFilesWord(info.Files))
		if info.Bytes > 0 {
			parts = append(parts, humanBytes(info.Bytes)+" unpacked")
		}
	case info.Packed > 0:
		parts = append(parts, humanBytes(info.Packed))
	}
	return strings.Join(parts, " · ")
}

// feArchiveSheetHeader is the archive sheet's header.
func feArchiveSheetHeader() UIPageHeader {
	path := feSheet.archive
	info, _ := feArchiveInfoPeek(path)
	subtitle, ink := feArchiveSummary(info), ColorTextSecondary()
	if feSheet.armed != "" && feSheetArmed(feSheet.armed) && feArchiveArmNote != "" {
		subtitle, ink = feArchiveArmNote, WarningInk()
	}
	h := UIPanelHeader(filepath.Base(path), subtitle, "packages")
	h.SubtitleColor = ink
	return h
}

// --- Running the rows ----------------------------------------------------------

// feArchiveSheetActivate runs one of the archive rows (from the Y menu or the
// archive sheet).
func feArchiveSheetActivate(config *Config, action feAction) {
	switch action.Kind {
	case feActArchive:
		archives := feArchiveTargets(feSelectionTargets(config))
		switch {
		case len(archives) == 1:
			openFeArchiveSheet(config, archives[0].Path, false)
		case len(archives) > 1:
			feArchiveExtractAll(config, archives)
		default:
			if s := feExtractSnapshot(); s.Active {
				openFeArchiveSheet(config, s.Job.Archive, true)
			}
		}
	case feActExtract:
		feArchiveStart(config, feExtractSmart, action.Label)
	case feActExtractHere:
		feArchiveStart(config, feExtractMerge, action.Label)
	case feActExtractCancel:
		if feExtractCancel(feSheet.archive) {
			PlayBackSound()
			showToast("Stopping "+filepath.Base(feSheet.archive)+"…", ToastInfo())
		} else {
			showToast(filepath.Base(feSheet.archive)+" is being moved into place and can no longer be stopped", ToastInfo())
		}
	}
}

// feArchiveArmNote is what the armed row's second press will do, for the
// sheet's header while the arm lasts (set on the SDL thread only).
var feArchiveArmNote string

// feArchiveArmText says what the second press of an armed row does.
func feArchiveArmText(mode feExtractMode, system bool, conflicts int) string {
	lead := "Press %s again to extract"
	if mode == feExtractMerge {
		lead += " here"
	}
	if system {
		lead = "System folder — p" + lead[1:]
	}
	switch {
	case mode != feExtractMerge:
		return hintSentence(lead, "A", "Extract")
	case conflicts > 0:
		return hintSentence(lead+" — "+feFilesWord(conflicts)+" with the same name will be replaced", "A", "Extract")
	case conflicts < 0:
		return hintSentence(lead+" — files with the same name are replaced", "A", "Extract")
	}
	return hintSentence(lead, "A", "Extract")
}

// feArchiveSystemDest reports a destination outside the user's own folders,
// where a change asks twice (files_locations.go).
func feArchiveSystemDest(config *Config, dest string) bool {
	areas := filesNav.userAreas
	if areas == nil {
		areas = filesComputeUserAreas(config)
	}
	return filesIsSystemPath(dest, areas)
}

// feArchiveStart queues the sheet's archive. Extract here asks twice when it
// would replace files (or cannot tell), and that one confirmation also covers
// a system folder; anything else in a system folder asks twice on its own. The
// sheet then stays up on the progress, and B hides it without stopping
// anything.
func feArchiveStart(config *Config, mode feExtractMode, label string) {
	path := feSheet.archive
	name := filepath.Base(path)
	switch place := feExtractPlace(path); {
	case place == 1:
		showToast(name+" is being extracted — the row below cancels it", ToastInfo())
		return
	case place > 1:
		showToast(name+" is waiting its turn — the row below cancels it", ToastInfo())
		return
	}
	info, ready := feArchiveInfoPeek(path)
	if !ready {
		showToast("Still reading "+name+"…", ToastInfo())
		return
	}
	if why := feArchiveBlocker(info); why != "" {
		showToast("Cannot extract "+name+": "+why, ToastWarn())
		return
	}
	if info.Listed && info.TopCount == 0 {
		showToast(name+" holds nothing to extract", ToastInfo())
		return
	}
	// Whether this row asks twice is decided once per row and kept in one arm,
	// so a conflict count that lands between the two presses cannot turn the
	// second press into a first one again.
	system := feArchiveSystemDest(config, info.Dest)
	conflicts := feArchiveConflictsNow(info)
	if (mode == feExtractMerge && conflicts != 0) || system {
		key := "sys:" + label
		if mode == feExtractMerge {
			key = "extract-here"
		}
		if !feSheetArmed(key) {
			feSheet.armed = key
			feSheet.armedAt = time.Now()
			PlayNavSound()
			feArchiveArmNote = feArchiveArmText(mode, system, conflicts)
			showToast(feArchiveArmNote, ToastWarn())
			return
		}
		feSheet.armed = ""
	}
	place, err := feExtractEnqueue(feExtractJob{Archive: path, Dest: info.Dest, Mode: mode, Kind: info.Kind})
	if err != nil {
		showToast(err.Error(), ToastInfo())
		return
	}
	PlayActivateSound()
	// The sheet stays up on the progress. Its second row is Cancel now, so the
	// cursor goes back to the first, and nothing stays armed.
	feSheet.cursor, feSheet.armed = 0, ""
	if place > 1 {
		showToast(name+" waits for "+feCountWord(place-1, "extraction", "extractions")+" ahead of it", ToastInfo())
	}
}

// feArchiveExtractAll queues every marked archive for Extract, each beside
// itself, and unmarks them; other marked rows stay marked.
func feArchiveExtractAll(config *Config, archives []FileEntry) {
	if !filesSystemConfirm("Extract") {
		return
	}
	queued, failed := 0, 0
	var lastErr error
	for _, a := range archives {
		path, err := resolveExplorerEntry(config, a.Path)
		if err != nil {
			err = errors.New(a.Name + ": " + filesFriendlyError(err))
		} else {
			_, err = feExtractEnqueue(feExtractJob{Archive: path, Dest: filepath.Dir(path), Mode: feExtractSmart})
		}
		if err != nil {
			failed++
			lastErr = err
			continue
		}
		queued++
		delete(feSelectedPaths, a.Path)
	}
	feSheetForget()
	switch {
	case queued == 0 && lastErr != nil:
		showToast("Cannot extract: "+lastErr.Error(), ToastError())
	case failed > 0:
		showToast(fmt.Sprintf("Extracting %s — %d could not be queued", feCountWord(queued, "archive", "archives"), failed), ToastWarn())
	default:
		PlayActivateSound()
		showToast("Extracting "+feCountWord(queued, "archive", "archives")+", one after another", ToastInfo())
	}
}

// --- Drawing ----------------------------------------------------------------------

// feArchiveLine is one line of the archive sheet's preview.
type feArchiveLine struct {
	text     string
	meta     string
	ink      sdl.Color
	bar      bool
	progress float64 // 0..1, or negative while unknown (a moving segment)
	wrap     bool    // the text may take two lines
}

// feArchiveLineGap is the space under each preview line.
const feArchiveLineGap = int32(6)

// feArchivePreview is what the sheet shows between its header and its rows:
// the extraction's progress while it runs, a warning when it cannot run, and
// the first few things in the archive.
func feArchivePreview() []feArchiveLine {
	path := feSheet.archive
	var lines []feArchiveLine
	switch place := feExtractPlace(path); {
	case place == 1:
		s := feExtractSnapshot()
		progress := -1.0
		if pct := feExtractPercent(s.Reading); pct >= 0 {
			progress = float64(pct) / 100
		}
		lines = append(lines, feArchiveLine{text: feExtractPhaseWord(s.Reading.Phase), meta: feExtractProgressText(s),
			ink: ColorTextAccent(), bar: true, progress: progress})
	case place > 1:
		lines = append(lines, feArchiveLine{text: "Waiting its turn", meta: fmt.Sprintf("%d ahead", place-1), ink: ColorTextSecondary()})
	}
	info, ready := feArchiveInfoPeek(path)
	if !ready {
		return append(lines, feArchiveLine{text: "Reading the archive…", ink: ColorTextTertiary()})
	}
	if why := feArchiveBlocker(info); why != "" {
		lines = append(lines, feArchiveLine{text: feSentenceCase(why), ink: WarningInk(), wrap: true})
	} else if info.NeedTool && info.Tool == "" {
		lines = append(lines, feArchiveLine{text: "7-Zip's 7zr.exe (600 KB) is downloaded once to unpack it", ink: ColorTextTertiary(), wrap: true})
	}
	switch {
	case info.Listed:
		for _, top := range info.Top {
			name, meta := top.Name, humanBytes(top.Bytes)
			if top.IsDir {
				name += "/"
				meta = feFilesWord(top.Files) + " · " + humanBytes(top.Bytes)
			}
			lines = append(lines, feArchiveLine{text: name, meta: meta, ink: ColorTextPrimary()})
		}
		if more := info.TopCount - len(info.Top); more > 0 {
			lines = append(lines, feArchiveLine{text: fmt.Sprintf("+ %d more", more), ink: ColorTextTertiary()})
		}
		if info.TopCount == 0 && info.Err == "" {
			lines = append(lines, feArchiveLine{text: "It holds nothing to extract", ink: ColorTextTertiary()})
		}
	case info.Err == "":
		lines = append(lines, feArchiveLine{text: feArchiveUnlistedText(info), ink: ColorTextTertiary()})
	}
	return lines
}

// feArchiveUnlistedText says why an archive's contents are not listed.
func feArchiveUnlistedText(info feArchiveInfo) string {
	switch info.Kind {
	case feArchiveGz, feArchiveXz, feArchiveBz2:
		return "Unpacks to " + feArchiveStem(info.Path) + ", or to the tar inside it"
	case feArchive7z, feArchiveRar:
		if info.Tool != "" {
			return "Unpacked by " + filepath.Base(info.Tool)
		}
		return "Unpacked by 7-Zip"
	}
	return "A tar shows what it holds only as it unpacks"
}

// feSentenceCase capitalises the first letter of a clause.
func feSentenceCase(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// feExtractPhaseWord is the progress line's lead word.
func feExtractPhaseWord(phase string) string {
	switch phase {
	case "", "Unpacking":
		return "Extracting"
	case "Checking":
		return "Checking the archive"
	}
	return phase
}

// feArchiveWrapCache keeps the one wrapped warning line the sheet draws: the
// wrap measures every word through FreeType, and the sheet is drawn every
// frame. Only the SDL thread uses it.
var feArchiveWrapCache struct {
	font  *ttf.Font
	text  string
	w     int32
	lines []string
}

// feArchiveWrap is text in at most two lines of w pixels.
func feArchiveWrap(font *ttf.Font, text string, w int32) []string {
	c := &feArchiveWrapCache
	if c.lines != nil && c.font == font && c.text == text && c.w == w {
		return c.lines
	}
	lines := wrapTextLines(font, text, w, 2)
	if len(lines) == 0 {
		lines = []string{ellipsize(font, text, w)}
	}
	c.font, c.text, c.w, c.lines = font, text, w, lines
	return lines
}

// feArchiveLineRows is how many text rows a preview line takes.
func feArchiveLineRows(font *ttf.Font, line feArchiveLine, w int32) int {
	if !line.wrap || line.meta != "" {
		return 1
	}
	return len(feArchiveWrap(font, line.text, w))
}

// feArchivePreviewHeight is the room the preview lines take at width w.
func feArchivePreviewHeight(font *ttf.Font, lines []feArchiveLine, w int32) int32 {
	if font == nil || len(lines) == 0 {
		return 0
	}
	lineH := int32(font.Height())
	h := int32(4)
	for _, line := range lines {
		h += int32(feArchiveLineRows(font, line, w))*lineH + feArchiveLineGap
		if line.bar {
			h += 12
		}
	}
	return h + 6
}

// feArchivePreviewMaxH is the tallest the preview can get — a progress line,
// a two-line warning, four items and "+ N more" — bounded by maxH.
func feArchivePreviewMaxH(font *ttf.Font, maxH int32) int32 {
	if font == nil {
		return 0
	}
	lineH := int32(font.Height())
	h := 4 + (lineH + feArchiveLineGap + 12) + (2*lineH + feArchiveLineGap) +
		int32(feArchiveTopShown+1)*(lineH+feArchiveLineGap) + 6
	if h > maxH {
		return maxH
	}
	return h
}

// feArchivePreviewFit drops lines from the end (the listing, never the
// progress or the warning, which come first) until the preview fits maxH.
func feArchivePreviewFit(font *ttf.Font, lines []feArchiveLine, w, maxH int32) []feArchiveLine {
	for len(lines) > 1 && feArchivePreviewHeight(font, lines, w) > maxH {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// drawFeArchivePreview draws the preview lines from y down, w wide. Names come
// from the archive, so they are drawn as they are (no $variable expansion).
func drawFeArchivePreview(renderer *sdl.Renderer, config *Config, font *ttf.Font, lines []feArchiveLine, x, y, w int32) {
	if renderer == nil || font == nil {
		return
	}
	lineH := int32(font.Height())
	y += 4
	for _, line := range lines {
		if feArchiveLineRows(font, line, w) > 1 {
			for _, row := range feArchiveWrap(font, line.text, w) {
				filesDrawPlainText(renderer, config, font, row, line.ink, x, y)
				y += lineH
			}
			y += feArchiveLineGap
			continue
		}
		metaW := int32(0)
		if line.meta != "" {
			meta := ellipsize(font, line.meta, w/2)
			metaW, _ = measureText(font, meta)
			filesDrawPlainText(renderer, config, font, meta, ColorTextTertiary(), x+w-metaW, y)
		}
		filesDrawPlainText(renderer, config, font, ellipsize(font, line.text, w-metaW-20), line.ink, x, y)
		y += lineH + feArchiveLineGap
		if line.bar {
			track := sdl.Rect{X: x, Y: y - 2, W: w, H: 6}
			if line.progress < 0 {
				feDrawIndeterminate(renderer, config, track)
			} else {
				UIProgressBar(renderer, track, line.progress, glassA(28), glassAccent())
			}
			y += 12
		}
	}
}

// feDrawIndeterminate fills a track for work of unknown length: a segment that
// moves along it, or, under reduced motion, the whole track at half strength
// (a still segment would read as a part done).
func feDrawIndeterminate(renderer *sdl.Renderer, config *Config, track sdl.Rect) {
	if renderer == nil || track.W <= 0 || track.H <= 0 {
		return
	}
	radius := track.H / 2
	if effectiveReducedMotion(config) {
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, radius, WithAlpha(glassAccent(), 110))
		return
	}
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, radius, glassA(18))
	seg := track.W / 5
	if seg < 1 {
		seg = 1
	}
	pos := int32(sdl.GetTicks64()/3%uint64(track.W+seg)) - seg
	x0, x1 := track.X+pos, track.X+pos+seg
	if x0 < track.X {
		x0 = track.X
	}
	if x1 > track.X+track.W {
		x1 = track.X + track.W
	}
	if x1 > x0 {
		fillRoundedRect(renderer, x0, track.Y, x1-x0, track.H, radius, glassAccent())
	}
}

// feExtractProgressText is the running job's progress in words:
// "45% · 312.0 MiB of 700.0 MiB", or the bytes written so far for a tar.
func feExtractProgressText(s feExtractStatus) string {
	r := s.Reading
	var parts []string
	if pct := feExtractPercent(r); pct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%%", pct))
	}
	if !r.Percent {
		switch {
		case r.Output && r.Total > 0:
			parts = append(parts, humanBytes(r.Written)+" of "+humanBytes(r.Total))
		case r.Written > 0:
			parts = append(parts, humanBytes(r.Written)+" written")
		}
	}
	if len(parts) == 0 {
		if r.Phase == "" || r.Phase == "Preparing" {
			return "starting…"
		}
		return "working…"
	}
	return strings.Join(parts, " · ")
}

// feExtractActive reports a job unpacking now.
func feExtractActive() bool {
	return feExtractSnapshot().Active
}

// feExtractBarLabel is the Files bar's reading while an extraction runs: the
// archive, how far it has come and how many wait behind it.
func feExtractBarLabel() string {
	s := feExtractSnapshot()
	if !s.Active {
		return ""
	}
	label := "Extracting " + ellipsizeRunes(filepath.Base(s.Job.Archive), 16)
	if pct := feExtractPercent(s.Reading); pct >= 0 {
		label += fmt.Sprintf(" · %d%%", pct)
	}
	if s.Waiting > 0 {
		label += fmt.Sprintf(" · %d waiting", s.Waiting)
	}
	return label
}

// feExtractDrawBarProgress lines the bottom of the Files bar with the running
// extraction's progress (a moving segment while it is unknown).
func feExtractDrawBarProgress(renderer *sdl.Renderer, config *Config, bar sdl.Rect) {
	if renderer == nil || bar.W <= 40 {
		return
	}
	s := feExtractSnapshot()
	if !s.Active {
		return
	}
	track := sdl.Rect{X: bar.X + 14, Y: bar.Y + bar.H - 5, W: bar.W - 28, H: 3}
	if pct := feExtractPercent(s.Reading); pct >= 0 {
		UIProgressBar(renderer, track, float64(pct)/100, glassA(18), glassAccent())
		return
	}
	feDrawIndeterminate(renderer, config, track)
}

// feExtractActivityItem is the activity strip's chip for a running
// extraction, on every screen but Files, whose bar shows it already. Its X
// cancels, and a tap opens Files on the archive.
func feExtractActivityItem() (activityItem, bool) {
	s := feExtractSnapshot()
	if !s.Active || feSceneActive(appConfig) {
		return activityItem{}, false
	}
	archive := s.Job.Archive
	frac := -1.0
	if pct := feExtractPercent(s.Reading); pct >= 0 {
		frac = float64(pct) / 100
	}
	cancel := func() {
		if !feExtractCancel(archive) {
			showToast(filepath.Base(archive)+" is being moved into place and can no longer be stopped", ToastInfo())
		}
	}
	return activityItem{
		glyph:  "↓",
		label:  "Extracting " + filepath.Base(archive),
		frac:   frac,
		tint:   installHighlight(),
		cancel: cancel,
		open:   func() { chatShowInFiles(appConfig, archive) },
	}, true
}

// --- Reporting back -------------------------------------------------------------

// feExtractAutoClosedAt is when the drain last closed a sheet by itself: an A
// pressed just then was meant for the sheet, not for the list it uncovered.
var feExtractAutoClosedAt time.Time

// feExtractJustClosed reports that moment (features.go ignores A then).
func feExtractJustClosed() bool {
	return !feExtractAutoClosedAt.IsZero() && time.Since(feExtractAutoClosedAt) < 500*time.Millisecond
}

// feExtractDrain reports finished extractions on the SDL thread. The main loop
// calls it every frame, wherever the user is, so a job finished behind another
// screen still says so.
func feExtractDrain(config *Config) {
	for _, outcome := range feExtractTakeOutcomes() {
		feExtractReport(config, outcome)
	}
}

// feExtractReport closes the job's sheet, relists the folder it landed in when
// Files shows it, and raises the toast. The cursor keeps its file across the
// relist (a new folder sorts in above the files), moves onto the result when
// it was on the archive and no sheet is open, and anything a sheet armed is
// dropped: a second press must never land on a row the relist moved.
func feExtractReport(config *Config, o feExtractOutcome) {
	if feSheet.open && feSheet.archive != "" && filesSamePath(feSheet.archive, o.Job.Archive) {
		feSheetForget()
		feExtractAutoClosedAt = time.Now()
	}
	feArchiveInfoForget(o.Job.Archive)
	if feSheet.open && feSheet.archive != "" && filesSamePath(filepath.Dir(feSheet.archive), o.Job.Dest) {
		// Another archive's sheet over the same folder: its conflict count and
		// the name Extract would pick were read before this job wrote anything.
		feSheet.armed = ""
		feArchiveReinspect(feSheet.archive)
	}
	if config != nil && feSceneActive(config) && !filesNav.atLocations && filesSamePath(filesNav.listedPath, o.Job.Dest) {
		focused, hasFocus := feFocusEntry(config)
		onArchive := hasFocus && filesSamePath(focused.Path, o.Job.Archive)
		feListDirectory(config)
		switch {
		case feSheet.open:
			feSheet.armed = ""
			if hasFocus {
				feFocusPath(config, focused.Path)
			}
		case onArchive && o.Err == nil && o.Target != "" && o.Job.Mode == feExtractSmart:
			feFocusPath(config, o.Target)
		case hasFocus:
			feFocusPath(config, focused.Path)
		}
	}
	msg, color := feExtractOutcomeMessage(o)
	showToast(msg, color)
}

// feExtractOutcomeMessage is a finished job's toast.
func feExtractOutcomeMessage(o feExtractOutcome) (string, sdl.Color) {
	name := ellipsizeRunes(filepath.Base(o.Job.Archive), 40)
	switch {
	case o.NeverRan:
		return "Took " + name + " out of the queue", ToastInfo()
	case errors.Is(o.Err, context.Canceled):
		return "Stopped extracting " + name + " — the folder is as it was", ToastInfo()
	case o.Err != nil:
		return "Cannot extract " + name + ": " + feExtractFriendlyError(o.Err, o.Stats), ToastError()
	}
	color := ToastSuccess()
	var msg string
	if o.Job.Mode == feExtractMerge {
		msg = "Extracted " + name + " here · " + feFilesWord(o.Stats.Files)
		if o.Stats.Replaced > 0 {
			msg += fmt.Sprintf(" · %d replaced", o.Stats.Replaced)
		}
		if o.Stats.Kept > 0 {
			msg += fmt.Sprintf(" · %d kept (a folder or link has the name)", o.Stats.Kept)
			color = ToastWarn()
		}
		if o.Stats.NotMoved > 0 {
			msg += fmt.Sprintf(" · %d could not be moved in (open or read-only)", o.Stats.NotMoved)
			color = ToastWarn()
		}
	} else {
		target := ellipsizeRunes(filepath.Base(o.Target), 40)
		if o.IsDir {
			target += "/"
		}
		msg = "Extracted " + name + " → " + target
		if o.IsDir {
			msg += " · " + feFilesWord(o.Stats.Files)
		}
	}
	if skipped := feExtractSkipText(o.Stats.Skips); skipped != "" {
		msg += " · " + skipped
		color = ToastWarn()
	}
	if o.Stats.Warning != "" {
		msg += " · 7-Zip: " + ellipsizeRunes(o.Stats.Warning, 60)
		color = ToastWarn()
	}
	return msg, color
}

// feExtractSkipText says what was left out, or "" when nothing worth saying
// was (macOS metadata is dropped quietly).
func feExtractSkipText(s feExtractSkips) string {
	var parts []string
	add := func(n int, one, many string) {
		if n > 0 {
			parts = append(parts, feCountWord(n, one, many))
		}
	}
	add(s.Links, "link", "links")
	add(s.Unsafe, "unsafe name", "unsafe names")
	add(s.Special, "special file", "special files")
	add(s.Duplicate, "duplicate", "duplicates")
	add(s.Failed, "name the card cannot hold", "names the card cannot hold")
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " skipped"
}

// feExtractFriendlyError is why a job failed, as the clause after "Cannot
// extract name: ".
func feExtractFriendlyError(err error, st feExtractStats) string {
	var space feNoSpaceError
	var xzMemory feXzMemoryErr
	var xzUnsupported feXzUnsupportedErr
	switch {
	case errors.As(err, &space):
		return space.Error()
	case errors.As(err, &xzMemory):
		return xzMemory.Error()
	case errors.As(err, &xzUnsupported):
		return xzUnsupported.Error()
	case errors.Is(err, errFeArchiveEncrypted):
		return "it is password-protected (JukaHub cannot ask for passwords yet)"
	case errors.Is(err, errFeArchiveNeedsTool):
		if IsWindows() {
			return err.Error() + " — install it from 7-zip.org"
		}
		return err.Error() + " — 7zz is not on this device"
	case errors.Is(err, errFeArchiveEmpty):
		if skipped := feExtractSkipText(st.Skips); skipped != "" {
			return "nothing in it could be written (" + skipped + ")"
		}
		if st.Skips.Junk > 0 {
			return "it only holds macOS metadata"
		}
		return err.Error()
	case errors.Is(err, errFeNotArchive), errors.Is(err, errFeCardFull), errors.Is(err, errFeHasLinks),
		errors.Is(err, errFeTooMany):
		return err.Error()
	case isDiskFullError(err):
		return "the card is full"
	case feFileTooLarge(err):
		return "a file in it is over 4 GB, more than this card's FAT32 format can hold"
	case errors.Is(err, errBadToolArchive):
		return "the archive is damaged or incomplete"
	case errors.Is(err, errFeToolFailed):
		// 7-Zip's (or tar's) own line says more than any summary would.
		return ellipsizeRunes(err.Error(), 120)
	case errors.Is(err, fs.ErrPermission):
		return "this folder cannot be written to"
	}
	return filesFriendlyError(err)
}
