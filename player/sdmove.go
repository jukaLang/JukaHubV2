package main

// sdmove.go — "Move to a new SD card": the rules, the plan and the record.
//
// The TrimUI has one card slot, so the new card goes into a USB card reader on
// the USB-C port (usbotg.go mounts it under /mnt/usb/<name>) or, on firmwares
// that have one, a second slot. Everything on the card JukaHub runs from is
// copied onto it file by file, every copy is read back from the new card and
// compared with the SHA-256 of the bytes read from the old one, and a record of
// every file (.jukahub-move/manifest.json on the new card) lets the next start
// — on the new card, after the swap — check that nothing is missing before the
// old card is retired.
//
// This file holds what has no screen and no worker: the categories, which
// files are left alone and why, path safety, the manifest and its journal, the
// walk of the old card, the look at a destination and the plan's checks. The
// copy and the verification are in sdmove_copy.go, the check after the swap in
// sdmove_check.go, the screen in sdmove_ui.go and the few system calls in
// sdmove_linux.go.
//
// Trust nothing that comes back from a card: a manifest is outside data the
// moment it is read again, so every path in it is checked before it is joined
// to a root, and a join can never leave that root.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	sdMoveDirName      = ".jukahub-move"
	sdMoveManifestName = "manifest.json"
	sdMoveJournalName  = "journal.jsonl"
	sdMoveCheckName    = "check.json"
	sdMoveFormat       = "jukahub-sd-move"
	sdMoveVersion      = 1
	// sdMoveMaxManifest caps what is read back: 100 000 files make about
	// 20 MB of JSON.
	sdMoveMaxManifest = 64 << 20
	// sdMoveTempSuffix ends every file the move writes under a temporary
	// name; a stopped copy leaves only such names, which the next run and the
	// walk of a card both ignore.
	sdMoveTempSuffix = ".jhmove"
	// sdMoveFAT32Max is the largest file FAT32 can hold.
	sdMoveFAT32Max = 1<<32 - 1
	// sdMoveMinBattery is the charge a move may start on without a charger.
	sdMoveMinBattery = 40
	// sdMoveLowBattery stops a running move that is not on the charger.
	sdMoveLowBattery = 10
	// sdMoveSpaceMargin is kept free on the new card beyond the plan.
	sdMoveSpaceMargin = 64 << 20
)

// The states a manifest is written in.
const (
	sdMoveStateCopying    = "copying"    // the move has not finished
	sdMoveStateVerified   = "verified"   // every copied file was read back and matched
	sdMoveStateIncomplete = "incomplete" // it finished, but some copies failed
)

// The categories a card is summed up in, in the order the plan lists them.
const (
	sdMoveCatGames   = "games"
	sdMoveCatSaves   = "saves"
	sdMoveCatBIOS    = "bios"
	sdMoveCatPorts   = "ports"
	sdMoveCatArt     = "artwork"
	sdMoveCatEmus    = "emulators"
	sdMoveCatJukaHub = "jukahub"
	sdMoveCatOther   = "other"
)

var sdMoveCatOrder = []string{sdMoveCatGames, sdMoveCatSaves, sdMoveCatBIOS, sdMoveCatPorts, sdMoveCatArt, sdMoveCatEmus, sdMoveCatJukaHub, sdMoveCatOther}

// sdMoveLeaveOutOrder is the order the plan suggests leaving categories out
// when the new card is too small: games first (they can be copied again any
// time), never the saves, the BIOS files or JukaHub and its settings.
var sdMoveLeaveOutOrder = []string{sdMoveCatGames, sdMoveCatArt, sdMoveCatOther, sdMoveCatPorts, sdMoveCatEmus}

// sdMoveCatLabel is a category's name on screen.
func sdMoveCatLabel(cat string) string {
	switch cat {
	case sdMoveCatGames:
		return "Games"
	case sdMoveCatSaves:
		return "Saves"
	case sdMoveCatBIOS:
		return "BIOS files"
	case sdMoveCatPorts:
		return "Ports"
	case sdMoveCatArt:
		return "Artwork & covers"
	case sdMoveCatEmus:
		return "Emulators & apps"
	case sdMoveCatJukaHub:
		return "JukaHub & settings"
	}
	return "Other files"
}

// sdMoveCatIcon is a category's glyph (icondefs.go).
func sdMoveCatIcon(cat string) string {
	switch cat {
	case sdMoveCatGames:
		return "roms"
	case sdMoveCatSaves:
		return "saves"
	case sdMoveCatBIOS:
		return "chip"
	case sdMoveCatPorts:
		return "packages"
	case sdMoveCatArt:
		return "image"
	case sdMoveCatEmus:
		return "emulators"
	case sdMoveCatJukaHub:
		return "jukahub"
	}
	return "files"
}

func sdMoveKnownCat(cat string) bool {
	for _, c := range sdMoveCatOrder {
		if c == cat {
			return true
		}
	}
	return false
}

// sdMoveSupported: the TrimUI handhelds, where the card is the whole system.
func sdMoveSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// ── Paths ────────────────────────────────────────────────────────────────

// sdMoveOwnPath reports whether rel is the move's own folder or inside it.
func sdMoveOwnPath(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return strings.EqualFold(first, sdMoveDirName)
}

// sdMoveCleanRel accepts a path only in its canonical form: relative, slash
// separated, no empty, "." or ".." part, no backslash, colon or NUL, and not
// inside the move's own folder. A manifest entry in any other form is refused
// rather than "cleaned" into something it did not say.
func sdMoveCleanRel(rel string) (string, error) {
	if rel == "" || len(rel) > 4096 {
		return "", errors.New("names no file")
	}
	if strings.ContainsAny(rel, "\\:\x00") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe path %q", rel)
		}
	}
	if sdMoveOwnPath(rel) {
		return "", fmt.Errorf("%q is inside the move's own folder", rel)
	}
	return rel, nil
}

// sdMoveJoin is root/rel for a checked rel; it refuses anything that would
// land on root itself or outside it.
func sdMoveJoin(root, rel string) (string, error) {
	clean, err := sdMoveCleanRel(rel)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("no folder to join to")
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	if full == filepath.Clean(root) || !isPathWithin(full, root) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	return full, nil
}

// sdMoveUnder reports whether rel is base or inside it ("" is no base).
func sdMoveUnder(rel, base string) bool {
	return base != "" && (rel == base || strings.HasPrefix(rel, base+"/"))
}

// sdMoveRelTo is target relative to root as a slash path, or "" when it is
// not inside root (or is root itself).
func sdMoveRelTo(root, target string) string {
	if root == "" || target == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// sdMoveSameMtime compares two modification times the way two mounts of one
// card can report them: within 2 s (FAT keeps even seconds), or a whole
// number of 15-minute steps apart up to 14 h, because FAT stores local time
// and a kernel that learned another zone reads every stamp shifted.
func sdMoveSameMtime(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	const slack = int64(2 * time.Second)
	if d <= slack {
		return true
	}
	if d > int64(14*time.Hour)+slack {
		return false
	}
	step := int64(15 * time.Minute)
	r := d % step
	return r <= slack || step-r <= slack
}

// ── Manifest ─────────────────────────────────────────────────────────────

// sdMoveFile is one file on the new card.
type sdMoveFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Cat    string `json:"cat"`
	// Mtime is the copy's modification time as the new card stores it (unix
	// nanoseconds); SrcMtime the original's when it was read, 0 when it
	// changed while it was copied (a later run copies it again).
	Mtime    int64 `json:"mtime"`
	SrcMtime int64 `json:"srcMtime"`
	Verified bool  `json:"verified,omitempty"`
	// Live files are JukaHub's own, which JukaHub rewrites while it runs from
	// the new card; the check after the swap never raises the alarm for them.
	Live bool `json:"live,omitempty"`
}

// sdMoveProblem is a file that is not (or not correctly) on the new card, and
// why — or one that was left alone on purpose.
type sdMoveProblem struct {
	Path string `json:"path"`
	Why  string `json:"why"`
	Cat  string `json:"cat,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// sdMoveManifest is .jukahub-move/manifest.json on the new card. WrittenText
// is the handheld's clock as text, for a person to read: the TrimUI boots
// before it learns the time, so nothing ever counts or compares with it.
type sdMoveManifest struct {
	Format      string          `json:"format"`
	Version     int             `json:"version"`
	State       string          `json:"state"`
	WrittenText string          `json:"writtenText,omitempty"`
	AppVersion  string          `json:"appVersion,omitempty"`
	Device      string          `json:"device,omitempty"`
	DeviceName  string          `json:"deviceName,omitempty"`
	SourceRoot  string          `json:"sourceRoot,omitempty"`
	SourceFS    string          `json:"sourceFS,omitempty"`
	SourceCID   string          `json:"sourceCID,omitempty"`
	DestFS      string          `json:"destFS,omitempty"`
	AppRel      string          `json:"appRel,omitempty"`
	DataRel     string          `json:"dataRel,omitempty"`
	LeftOut     []string        `json:"leftOut,omitempty"`
	Files       []sdMoveFile    `json:"files"`
	NotCopied   []sdMoveProblem `json:"notCopied,omitempty"`
	Unreadable  []sdMoveProblem `json:"unreadable,omitempty"`
	Failed      []sdMoveProblem `json:"failed,omitempty"`
	Skipped     []sdMoveProblem `json:"skipped,omitempty"`
	TotalBytes  int64           `json:"totalBytes"`
}

// sdMoveValidHash accepts a lower-case hex SHA-256.
func sdMoveValidHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// sdMoveCleanProblems keeps the entries of a problem list whose path is safe;
// the lists are only shown, so a bad entry is dropped, not fatal.
func sdMoveCleanProblems(in []sdMoveProblem) []sdMoveProblem {
	out := in[:0]
	for _, p := range in {
		if _, err := sdMoveCleanRel(p.Path); err != nil && p.Path != "" {
			continue
		}
		if len(p.Why) > 300 {
			p.Why = p.Why[:300]
		}
		out = append(out, p)
	}
	return out
}

// sdMoveRecordable keeps a problem countable once the record is read again: a
// path the record would refuse (a name with \ or :, from an ext4 card) moves
// into the reason, so sdMoveCleanProblems keeps the entry instead of dropping
// it — the check after the swap counts these lists.
func sdMoveRecordable(p sdMoveProblem) sdMoveProblem {
	if p.Path != "" {
		if _, err := sdMoveCleanRel(p.Path); err != nil {
			p.Why += ": " + p.Path
			p.Path = ""
		}
	}
	return p
}

// sdMoveParseManifest reads a manifest from a card. Every file entry must be
// safe and well formed or the whole record is refused: a check that silently
// skipped a doctored entry would report a card complete that is not.
func sdMoveParseManifest(data []byte) (sdMoveManifest, error) {
	if len(data) > sdMoveMaxManifest {
		return sdMoveManifest{}, errors.New("the move record is too large")
	}
	var m sdMoveManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return sdMoveManifest{}, fmt.Errorf("the move record is damaged: %v", err)
	}
	if m.Format != sdMoveFormat {
		return sdMoveManifest{}, errors.New("this is not a JukaHub move record")
	}
	if m.Version < 1 || m.Version > sdMoveVersion {
		return sdMoveManifest{}, fmt.Errorf("the move record is version %d; this JukaHub reads up to %d", m.Version, sdMoveVersion)
	}
	switch m.State {
	case sdMoveStateCopying, sdMoveStateVerified, sdMoveStateIncomplete:
	default:
		return sdMoveManifest{}, fmt.Errorf("the move record has an unknown state %q", m.State)
	}
	seen := make(map[string]bool, len(m.Files))
	var total int64
	for i := range m.Files {
		f := &m.Files[i]
		if _, err := sdMoveCleanRel(f.Path); err != nil {
			return sdMoveManifest{}, fmt.Errorf("file %d: %v", i+1, err)
		}
		if seen[f.Path] {
			return sdMoveManifest{}, fmt.Errorf("file %d: %q is listed twice", i+1, f.Path)
		}
		seen[f.Path] = true
		if f.Size < 0 || !sdMoveValidHash(f.SHA256) {
			return sdMoveManifest{}, fmt.Errorf("file %d: %q has no valid size and checksum", i+1, f.Path)
		}
		if !sdMoveKnownCat(f.Cat) {
			f.Cat = sdMoveCatOther
		}
		total += f.Size
	}
	m.TotalBytes = total
	var cats []string
	for _, c := range m.LeftOut {
		if sdMoveKnownCat(c) {
			cats = append(cats, c)
		}
	}
	m.LeftOut = cats
	m.NotCopied = sdMoveCleanProblems(m.NotCopied)
	m.Unreadable = sdMoveCleanProblems(m.Unreadable)
	m.Failed = sdMoveCleanProblems(m.Failed)
	m.Skipped = sdMoveCleanProblems(m.Skipped)
	return m, nil
}

// sdMoveJournalLine is one line of the journal a running move appends to:
// "c" a file copied (with the hash of the bytes read), "v" that copy read
// back from the new card and matched.
type sdMoveJournalLine struct {
	K string     `json:"k"`
	F sdMoveFile `json:"f"`
}

// sdMoveApplyJournal folds journal lines into m and reports how many were
// used. A line that does not parse (the last one, cut by a power loss) or
// names an unsafe path is ignored: the file it describes is simply copied
// again.
func sdMoveApplyJournal(m *sdMoveManifest, data []byte) int {
	index := make(map[string]int, len(m.Files))
	for i, f := range m.Files {
		index[f.Path] = i
	}
	used := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var l sdMoveJournalLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		f := l.F
		if _, err := sdMoveCleanRel(f.Path); err != nil || f.Size < 0 || !sdMoveValidHash(f.SHA256) {
			continue
		}
		if !sdMoveKnownCat(f.Cat) {
			f.Cat = sdMoveCatOther
		}
		i, ok := index[f.Path]
		switch l.K {
		case "c":
			f.Verified = false
			if ok {
				m.Files[i] = f
			} else {
				index[f.Path] = len(m.Files)
				m.Files = append(m.Files, f)
			}
		case "v":
			if !ok || m.Files[i].SHA256 != f.SHA256 || m.Files[i].Size != f.Size {
				continue
			}
			m.Files[i].Verified = true
		default:
			continue
		}
		used++
	}
	return used
}

// sdMoveReadState loads what an earlier run left on a card: the manifest with
// the journal folded in. ok is false when the card holds no move.
func sdMoveReadState(root string) (m sdMoveManifest, ok bool, err error) {
	dir := filepath.Join(root, sdMoveDirName)
	data, rerr := os.ReadFile(filepath.Join(dir, sdMoveManifestName))
	switch {
	case rerr == nil:
		m, err = sdMoveParseManifest(data)
		if err != nil {
			return sdMoveManifest{}, false, err
		}
		ok = true
	case errors.Is(rerr, fs.ErrNotExist):
		m = sdMoveManifest{Format: sdMoveFormat, Version: sdMoveVersion, State: sdMoveStateCopying}
	default:
		return sdMoveManifest{}, false, rerr
	}
	if j, jerr := os.ReadFile(filepath.Join(dir, sdMoveJournalName)); jerr == nil && len(j) <= sdMoveMaxManifest {
		if sdMoveApplyJournal(&m, j) > 0 {
			ok = true
		}
	}
	return m, ok, nil
}

// sdMoveWriteAtomic writes data through a temporary name in the same folder,
// fsynced before the rename. No chmod: FAT refuses most mode changes.
func sdMoveWriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".jhmove-*"+sdMoveTempSuffix)
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	return nil
}

// sdMoveWriteManifest writes m into root's move folder, files sorted by path.
func sdMoveWriteManifest(root string, m *sdMoveManifest) error {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	var total int64
	for _, f := range m.Files {
		total += f.Size
	}
	m.TotalBytes = total
	m.Format, m.Version = sdMoveFormat, sdMoveVersion
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, sdMoveDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return sdMoveWriteAtomic(filepath.Join(dir, sdMoveManifestName), data)
}

// sdMoveClockText is "now" for a person to read, labelled as the handheld's
// own clock.
func sdMoveClockText() string {
	return time.Now().Format("2006-01-02 15:04") + " (handheld clock)"
}

// ── What is left alone ───────────────────────────────────────────────────

// sdMoveJunkDirs are folders at the top of a card that a computer or the
// filesystem keeps for itself; the new card gets its own.
var sdMoveJunkDirs = map[string]bool{
	"system volume information": true, "$recycle.bin": true, ".spotlight-v100": true,
	".fseventsd": true, ".trashes": true, "lost+found": true, "lost.dir": true,
}

// sdMoveIsLog matches JukaHub's logs: errors.txt (and its rotations) and
// *.log / *.log.N. A new card starts new logs.
func sdMoveIsLog(name string) bool {
	lower := strings.ToLower(name)
	if lower == "errors.txt" || strings.HasPrefix(lower, "errors.txt.") || strings.HasSuffix(lower, ".log") {
		return true
	}
	if i := strings.LastIndex(lower, ".log."); i > 0 {
		rest := lower[i+len(".log."):]
		if rest == "" {
			return false
		}
		for _, c := range rest {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// The skip reasons that leave something of the user's behind: the run lists
// these in the record's notCopied too, so the old card is never called a
// complete backup while it holds them.
const (
	sdMoveWhyLink    = "a link — links are never followed"
	sdMoveWhyBadName = "a name a memory card cannot store (\\ or :)"
)

// sdMoveSkipLeavesData reports a skip reason from the list above.
func sdMoveSkipLeavesData(why string) bool {
	return why == sdMoveWhyLink || why == sdMoveWhyBadName
}

// sdMoveSkipReason says why a walked entry is left alone, or "" to copy it.
func sdMoveSkipReason(rel string, isDir bool, appRel string, swaps map[string]bool) string {
	name := path.Base(rel)
	lower := strings.ToLower(name)
	switch {
	case isDir && !strings.Contains(rel, "/") && sdMoveJunkDirs[lower]:
		return "a folder the computer or the filesystem keeps for itself"
	case !isDir && swaps[rel]:
		return "an active swap file"
	case !isDir && lower == memSwapFileName:
		return "JukaHub's swap file — it makes a new one on the new card"
	case !isDir && (strings.HasSuffix(lower, sdMoveTempSuffix) || strings.HasSuffix(lower, ".jhpart")):
		return "an unfinished copy"
	case !isDir && sdMoveUnder(rel, appRel) && sdMoveIsLog(name):
		return "JukaHub's log — it starts a new one"
	}
	if _, err := sdMoveCleanRel(rel); err != nil {
		return sdMoveWhyBadName
	}
	return ""
}

// sdMoveActiveSwaps lists the swap files in use (/proc/swaps), as paths.
func sdMoveActiveSwaps() []string {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || f[1] != "file" {
			continue
		}
		out = append(out, unescapeMountPath(f[0]))
	}
	return out
}

// sdMoveCategoryOf sorts one path into a category. JukaHub's own folders
// first, then the save folders the save manager knows, then by the folder at
// the top of the card. Artwork is matched by folder name anywhere above the
// file, so Roms/GBA/Imgs is artwork, not games.
func sdMoveCategoryOf(rel, appRel, dataRel string, saveRels []string) string {
	lower := strings.ToLower(rel)
	if sdMoveUnder(lower, strings.ToLower(appRel)) || sdMoveUnder(lower, strings.ToLower(dataRel)) {
		return sdMoveCatJukaHub
	}
	for _, s := range saveRels {
		if sdMoveUnder(lower, strings.ToLower(s)) {
			return sdMoveCatSaves
		}
	}
	parts := strings.Split(lower, "/")
	first := parts[0]
	second := ""
	if len(parts) > 2 {
		second = parts[1]
	}
	switch {
	case first == "saves" || first == "save":
		return sdMoveCatSaves
	case first == "bios":
		return sdMoveCatBIOS
	case first == "ports", (first == "roms" || first == "data") && second == "ports":
		return sdMoveCatPorts
	}
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "imgs", "images", ".media", "boxart", "box art", "covers", "artwork", "thumbnails":
			return sdMoveCatArt
		}
	}
	switch first {
	case "roms":
		return sdMoveCatGames
	case "emus", "emu", "apps", "app", "retroarch", "portmaster", ".system", "system", "trimui", ".tmp_update",
		".userdata", "tools", "ppsspp", "psp", "data", "cores", "bin", "lib":
		return sdMoveCatEmus
	}
	return sdMoveCatOther
}

// sdMoveCaseSensitive reports whether a filesystem keeps "a" and "A" apart.
func sdMoveCaseSensitive(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "vfat", "fat", "msdos", "exfat", "ntfs", "ntfs3", "fuseblk", "":
		return false
	}
	return true
}

// sdMoveIsFAT32 reports a FAT destination (the 4 GiB file limit).
func sdMoveIsFAT32(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "vfat", "fat", "msdos", "fat32":
		return true
	}
	return false
}

// ── The old card ─────────────────────────────────────────────────────────

// sdMoveItem is one file on the old card.
type sdMoveItem struct {
	Rel   string
	Size  int64
	Mtime int64 // unix nanoseconds
	Cat   string
	Live  bool
}

// sdMoveCatTotal sums a category.
type sdMoveCatTotal struct {
	Files int
	Bytes int64
}

// sdMoveScan is the walk of the old card.
type sdMoveScan struct {
	Root       string
	FS         string
	Items      []sdMoveItem
	Dirs       int
	Cats       map[string]sdMoveCatTotal
	Bytes      int64
	Skipped    []sdMoveProblem
	Unreadable []sdMoveProblem
	AppRel     string
	DataRel    string
	Disk       string
	CID        string
	Parts      int
	// System: the handheld starts from this card itself (JukaHub OS image,
	// Knulli-style cards), so a file copy cannot make a card that boots.
	System bool
	Total  int64
	Free   int64
}

// sdMoveSourceRoot is the card JukaHub runs from, resolved.
func sdMoveSourceRoot() string {
	root := bkupCardRoot()
	if root == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		return r
	}
	return root
}

// sdMoveScanSource walks the old card. progress, when set, is called every
// few hundred files with the count so far. Folders that cannot be listed are
// recorded, not fatal: a failing card is the reason many people move.
func sdMoveScanSource(ctx context.Context, root string, progress func(files int)) (*sdMoveScan, error) {
	if root == "" {
		return nil, errors.New("JukaHub could not find the SD card it runs from")
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("the SD card could not be read: %v", err)
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("the SD card is not a folder")
	}
	s := &sdMoveScan{Root: root, Cats: map[string]sdMoveCatTotal{}}
	s.FS = fsTypeFor(readMounts(), root)
	s.AppRel = sdMoveRelTo(root, bkupAbs(bkupAppDir()))
	s.DataRel = sdMoveRelTo(root, bkupDataDir())
	if t, f, _, err := sdMoveStatfs(root); err == nil {
		s.Total, s.Free = t, f
	}
	s.Disk = sdMoveDiskOf(root)
	if s.Disk != "" {
		s.CID = sdMoveCardCID(s.Disk)
		s.Parts = sdMovePartitionCount(s.Disk)
		// A boot area alone is not enough: a card once written with a system
		// image and later reformatted keeps a stale boot0 in front of its one
		// partition. A system card also has its system partition.
		rootDisk := sdMoveDiskOf("/")
		s.System = (rootDisk != "" && rootDisk == s.Disk) || (s.Parts > 1 && sdMoveHasBootArea(s.Disk))
	}
	var saveRels []string
	for _, sr := range saveManagerRoots() {
		if rel := sdMoveRelTo(root, sr.Path); rel != "" {
			saveRels = append(saveRels, rel)
		}
	}
	swaps := map[string]bool{}
	for _, p := range sdMoveActiveSwaps() {
		if rel := sdMoveRelTo(root, p); rel != "" {
			swaps[rel] = true
		}
	}
	rootDev, haveDev := sdMoveFileDev(rootInfo)
	count := 0
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == root {
			return err
		}
		relOS, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel := filepath.ToSlash(relOS)
		if sdMoveOwnPath(rel) {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil {
			s.Unreadable = append(s.Unreadable, sdMoveProblem{Path: rel, Why: "could not be read: " + firstLine(err.Error())})
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		isDir := d.IsDir()
		if why := sdMoveSkipReason(rel, isDir, s.AppRel, swaps); why != "" {
			s.Skipped = append(s.Skipped, sdMoveProblem{Path: rel, Why: why})
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if isDir {
			if info, ierr := d.Info(); ierr == nil && haveDev {
				if dev, ok := sdMoveFileDev(info); ok && dev != rootDev {
					s.Skipped = append(s.Skipped, sdMoveProblem{Path: rel, Why: "another drive mounted inside the card"})
					return filepath.SkipDir
				}
			}
			s.Dirs++
			return nil
		}
		if !d.Type().IsRegular() {
			why := "not a regular file"
			if d.Type()&fs.ModeSymlink != 0 {
				why = sdMoveWhyLink
			}
			s.Skipped = append(s.Skipped, sdMoveProblem{Path: rel, Why: why})
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			s.Unreadable = append(s.Unreadable, sdMoveProblem{Path: rel, Why: "could not be read: " + firstLine(ierr.Error())})
			return nil
		}
		it := sdMoveItem{Rel: rel, Size: info.Size(), Mtime: info.ModTime().UnixNano()}
		it.Cat = sdMoveCategoryOf(rel, s.AppRel, s.DataRel, saveRels)
		it.Live = sdMoveUnder(rel, s.AppRel) || sdMoveUnder(rel, s.DataRel)
		s.Items = append(s.Items, it)
		t := s.Cats[it.Cat]
		t.Files++
		t.Bytes += it.Size
		s.Cats[it.Cat] = t
		s.Bytes += it.Size
		count++
		if progress != nil && count%250 == 0 {
			progress(count)
		}
		return nil
	})
	if walkErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("the SD card could not be read: %v", walkErr)
	}
	return s, nil
}

// ── The new card ─────────────────────────────────────────────────────────

// sdMoveDest is a place the move can go to: a USB volume (mounted or not) or
// another mounted card.
type sdMoveDest struct {
	ID      string
	Title   string
	Sub     string
	Path    string // mount point, "" while not mounted
	Disk    string // block disk, for the boot-area probe and Eject
	VolName string // USB volume (sda1), for Mount
	Label   string // volume label (USB), for the system-card check
	FS      string
	Size    int64
	USB     bool
}

// sdMoveExtraMountCands are the second-slot mount points other firmwares use
// (rafind.go's list) and the usual desktop-style media folders.
var sdMoveExtraMountCands = []string{"/mnt/SDCARD2", "/mnt/sdcard2", "/mnt/mmc", "/mnt/sdcard", "/mnt/SDCARD"}

// sdMoveExtraDests lists mounted volumes that are not the old card and not a
// USB volume (usbotg.go lists those): a second card slot, mostly.
func sdMoveExtraDests(srcRoot string) []sdMoveDest {
	if runtime.GOOS != "linux" {
		return nil
	}
	srcDev, srcOK := sdMoveDevOf(srcRoot)
	srcDisk := sdMoveDiskOf(srcRoot)
	cands := append([]string(nil), sdMoveExtraMountCands...)
	for _, pattern := range []string{"/media/*", "/run/media/*/*"} {
		if m, err := filepath.Glob(pattern); err == nil {
			cands = append(cands, m...)
		}
	}
	mounts := readMounts()
	seen := map[uint64]bool{}
	var out []sdMoveDest
	for _, c := range cands {
		resolved, err := filepath.EvalSymlinks(c)
		if err != nil || sdMoveUnder(filepath.ToSlash(resolved), usbOtgMountRoot) {
			continue
		}
		dev, ok := sdMoveDevOf(resolved)
		if !ok || (srcOK && dev == srcDev) || seen[dev] {
			continue
		}
		// Only a mount point: a plain folder is on some other filesystem
		// (the system's), which is not a card.
		parentDev, pok := sdMoveDevOf(filepath.Dir(resolved))
		if !pok || parentDev == dev {
			continue
		}
		// Another partition of the old card is the old card.
		disk := sdMoveDiskOf(resolved)
		if srcDisk != "" && disk == srcDisk {
			continue
		}
		seen[dev] = true
		total, _, _, _ := sdMoveStatfs(resolved)
		fstype := fsTypeFor(mounts, resolved)
		title := "Second card slot"
		if !strings.Contains(strings.ToLower(c), "sdcard2") && !strings.HasSuffix(c, "/mmc") {
			title = filepath.Base(resolved)
		}
		bits := []string{resolved}
		if total > 0 {
			bits = append(bits, humanBytes(total))
		}
		if fsName := usbFSName(fstype); fsName != "" {
			bits = append(bits, fsName)
		}
		out = append(out, sdMoveDest{
			ID: "mnt:" + resolved, Title: title, Sub: strings.Join(bits, " · "), Path: resolved,
			Disk: disk, FS: fstype, Size: total,
		})
	}
	return out
}

// sdMoveUSBDests turns the USB & OTG snapshot's volumes into destinations.
func sdMoveUSBDests(snap usbOtgSnapshot) []sdMoveDest {
	out := make([]sdMoveDest, 0, len(snap.volumes))
	for _, v := range snap.volumes {
		out = append(out, sdMoveDest{
			ID: "usb:" + v.Name, Title: usbOtgVolumeTitle(v), Sub: usbOtgVolumeSubtitle(v),
			Path: v.MountedAt, Disk: v.Disk, VolName: v.Name, Label: v.Label, FS: v.FSType, Size: v.Size, USB: true,
		})
	}
	return out
}

// sdMoveDestInfo is what a look at the chosen destination found.
type sdMoveDestInfo struct {
	Root     string
	FS       string
	Total    int64
	Free     int64
	Block    int64
	ReadOnly bool
	Writable bool
	WriteErr string
	SameCard bool
	Disk     string
	System   bool
	// Label is the chosen volume's label (USB volumes only); the caller
	// copies it from sdMoveDest after the probe.
	Label   string
	Used    int64
	Prev    *sdMoveManifest
	PrevErr string
	Err     string
}

// sdMoveProbeDest looks at a destination: its size, free space, cluster size
// and filesystem, whether it is the old card under another name, whether a
// small file can be written, synced and removed there, and what an earlier
// move left on it.
func sdMoveProbeDest(root, srcRoot string) sdMoveDestInfo {
	d := sdMoveDestInfo{Root: root}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
		d.Root = r
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		d.Err = "the new card is not mounted any more"
		return d
	}
	if srcRoot != "" {
		if isPathWithin(root, srcRoot) || isPathWithin(srcRoot, root) {
			d.SameCard = true
		}
		sdev, sok := sdMoveDevOf(srcRoot)
		if ddev, dok := sdMoveFileDev(info); sok && dok && sdev == ddev {
			d.SameCard = true
		}
	}
	d.FS = fsTypeFor(readMounts(), root)
	total, free, block, err := sdMoveStatfs(root)
	if err != nil {
		d.Err = "the new card's size could not be read: " + firstLine(err.Error())
		return d
	}
	d.Total, d.Free, d.Block = total, free, block
	d.Used = total - free
	d.ReadOnly = sdMoveReadOnly(root)
	d.Disk = sdMoveDiskOf(root)
	if d.Disk != "" {
		// Another partition of the old card has its own device number but
		// is the old card all the same: never written to.
		if srcRoot != "" && d.Disk == sdMoveDiskOf(srcRoot) {
			d.SameCard = true
		}
		d.System = sdMovePartitionCount(d.Disk) > 1 && sdMoveHasBootArea(d.Disk)
	}
	if !d.SameCard {
		d.Writable, d.WriteErr = sdMoveWriteProbe(root)
		if m, ok, perr := sdMoveReadState(root); perr != nil {
			d.PrevErr = firstLine(perr.Error())
		} else if ok {
			d.Prev = &m
		}
	}
	return d
}

// sdMoveWriteProbe writes 64 KiB at the top of root, syncs, reads it back and
// removes it.
func sdMoveWriteProbe(root string) (bool, string) {
	f, err := os.CreateTemp(root, ".jhmove-probe-*"+sdMoveTempSuffix)
	if err != nil {
		return false, firstLine(err.Error())
	}
	name := f.Name()
	defer os.Remove(name)
	block := bytes.Repeat([]byte("JukaHub move probe\n"), 64<<10/19+1)[:64<<10]
	if _, err := f.Write(block); err != nil {
		_ = f.Close()
		return false, firstLine(err.Error())
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false, firstLine(err.Error())
	}
	if err := f.Close(); err != nil {
		return false, firstLine(err.Error())
	}
	back, err := os.ReadFile(name)
	if err != nil {
		return false, firstLine(err.Error())
	}
	if !bytes.Equal(back, block) {
		return false, "what was written did not read back the same"
	}
	return true, ""
}

// ── The plan ─────────────────────────────────────────────────────────────

// sdMoveCheck is one line of the plan's checklist. Hard checks that fail keep
// Start disabled; Body is the explanation its sheet shows.
type sdMoveCheck struct {
	ID    string
	Title string
	Sub   string
	OK    bool
	Hard  bool
	Note  bool // informational: neither good nor bad news
	Body  string
}

// sdMovePlan is the plan for one destination with the user's choices.
type sdMovePlan struct {
	Checks   []sdMoveCheck
	Files    int
	Bytes    int64
	Need     int64
	Fits     bool
	Big      []sdMoveItem
	Clashes  []sdMoveProblem
	Suggest  []string
	Reuse    int
	CatNeed  map[string]int64
	CanStart bool
}

// sdMovePrevUsable reports whether an earlier move's record on the new card
// came from this card, so its entries may be trusted for a resume.
func sdMovePrevUsable(s *sdMoveScan, prev *sdMoveManifest) bool {
	if s == nil || prev == nil {
		return false
	}
	return prev.SourceCID == "" || s.CID == "" || prev.SourceCID == s.CID
}

// sdMoveOSDataLabel is the label of JukaHub OS's data partition (os/smartpro
// and os/plastron both make it): the only place a system card's data may go.
const sdMoveOSDataLabel = "JUKAHUB"

// sdMoveSystemDestOK reports a destination a system card may be copied to: a
// USB volume labelled JUKAHUB on a card with its own boot area and system
// partitions — not that card's boot or system partition, not a plain card,
// not another system's card.
func sdMoveSystemDestOK(d sdMoveDestInfo) bool {
	return d.System && strings.EqualFold(strings.TrimSpace(d.Label), sdMoveOSDataLabel)
}

// sdMoveEvaluate builds the plan. Pure: everything it needs is passed in.
func sdMoveEvaluate(s *sdMoveScan, d sdMoveDestInfo, leftOut map[string]bool, acceptBig bool, battPct int, charging, gameRunning bool) sdMovePlan {
	p := sdMovePlan{CatNeed: map[string]int64{}}
	if s == nil {
		return p
	}
	block := d.Block
	if block <= 0 {
		block = 4096
	}
	round := func(n int64) int64 {
		if n <= 0 {
			return 0
		}
		return (n + block - 1) / block * block
	}
	prev := map[string]sdMoveFile{}
	if sdMovePrevUsable(s, d.Prev) {
		for _, f := range d.Prev.Files {
			prev[f.Path] = f
		}
	}
	fat := sdMoveIsFAT32(d.FS)
	clashCheck := s.FS != "" && sdMoveCaseSensitive(s.FS) && !sdMoveCaseSensitive(d.FS)
	lowerSeen := map[string]string{}
	for _, it := range s.Items {
		if leftOut[it.Cat] {
			continue
		}
		if fat && it.Size > sdMoveFAT32Max {
			p.Big = append(p.Big, it)
			continue
		}
		if clashCheck {
			key := strings.ToLower(it.Rel)
			if other, ok := lowerSeen[key]; ok {
				p.Clashes = append(p.Clashes, sdMoveProblem{Path: it.Rel, Why: "has the same name as " + other + " on a card that ignores upper and lower case", Cat: it.Cat, Size: it.Size})
				continue
			}
			lowerSeen[key] = it.Rel
		}
		p.Files++
		p.Bytes += it.Size
		if f, ok := prev[it.Rel]; ok && f.Size == it.Size && f.SrcMtime == it.Mtime && f.SrcMtime != 0 {
			p.Reuse++
			continue
		}
		r := round(it.Size)
		p.Need += r
		p.CatNeed[it.Cat] += r
	}
	p.Need += int64(s.Dirs)*block + sdMoveSpaceMargin + int64(len(s.Items))*512
	p.Fits = d.Free > 0 && p.Need <= d.Free
	if !p.Fits && d.Free > 0 {
		deficit, saved := p.Need-d.Free, int64(0)
		for _, cat := range sdMoveLeaveOutOrder {
			if leftOut[cat] || p.CatNeed[cat] == 0 {
				continue
			}
			p.Suggest = append(p.Suggest, cat)
			saved += p.CatNeed[cat]
			if saved >= deficit {
				break
			}
		}
		if saved < deficit {
			p.Suggest = nil
		}
	}

	add := func(c sdMoveCheck) { p.Checks = append(p.Checks, c) }
	add(sdMoveCheck{ID: "card", Title: "A different card", OK: !d.SameCard, Hard: true,
		Sub:  map[bool]string{true: "The new card is not the one JukaHub runs from", false: "This is the card JukaHub runs from"}[!d.SameCard],
		Body: "The new card has to be a different card from the one in the handheld's slot. Put the new card into a USB card reader on the USB-C port (or into a second slot, if your handheld has one) and choose it here."})
	if s.System {
		ok := sdMoveSystemDestOK(d)
		c := sdMoveCheck{ID: "system", Title: "A card that starts the handheld", OK: ok, Hard: true}
		if ok {
			c.Sub = "Both cards have JukaHub OS: only the " + sdMoveOSDataLabel + " partition is copied"
			c.Body = "Your card starts the handheld by itself (JukaHub OS), and so does the new one. The move copies your " + sdMoveOSDataLabel + " data partition — JukaHub, your games, saves and settings — into the new card's " + sdMoveOSDataLabel + " partition and leaves the new card's own system as it is."
		} else {
			c.Sub = "Choose the " + sdMoveOSDataLabel + " partition of a card written with JukaHub OS"
			c.Body = "Your card starts the handheld by itself: besides the folder JukaHub runs from it holds a boot area and a system partition, which a file copy cannot recreate. JukaHub only copies such a card into the " + sdMoveOSDataLabel + " data partition of a new card that already has JukaHub OS — never into another partition or a plain card.\n\n## What to do\n• On a computer, write the same JukaHub OS image to the new card.\n• If the new card shows no " + sdMoveOSDataLabel + " partition in the reader, start the handheld from the new card once (the first start makes it), power off and put your old card back.\n• Put the new card into a USB card reader on the USB-C port and choose its " + sdMoveOSDataLabel + " partition here: JukaHub then copies your data — JukaHub, games, saves and settings — onto it."
		}
		add(c)
	}
	writeC := sdMoveCheck{ID: "write", Title: "Can be written", OK: d.Writable, Hard: true}
	switch {
	case d.Writable:
		writeC.Sub = "A test file was written, read back and removed"
		writeC.Body = "JukaHub wrote a small file to the new card, read it back unchanged and removed it again."
	case d.ReadOnly:
		writeC.Sub = "The new card is read-only"
		writeC.Body = "The system mounted the new card read-only. Full-size SD cards and adapters have a small lock switch on the side: slide it up (away from \"LOCK\"), then unplug the reader and plug it in again."
	default:
		writeC.Sub = "Writing a test file failed"
		writeC.Body = "JukaHub could not write a test file to the new card: " + d.WriteErr + "\n\nCheck the lock switch on the card's adapter, then unplug the reader and plug it in again. If it still fails, format the new card as exFAT on a computer."
	}
	add(writeC)
	spaceC := sdMoveCheck{ID: "space", Title: "Enough room", OK: p.Fits, Hard: true}
	spaceC.Sub = "Needs " + humanBytes(p.Need) + " of " + humanBytes(d.Free) + " free"
	if p.Fits {
		spaceC.Body = fmt.Sprintf("The new card has %s free of %s. The copy needs about %s: the files, rounded up to the card's %s blocks, plus room for folders and the move's record.", humanBytes(d.Free), humanBytes(d.Total), humanBytes(p.Need), humanBytes(block))
	} else {
		spaceC.Body = fmt.Sprintf("The copy needs about %s but the new card has %s free.", humanBytes(p.Need), humanBytes(d.Free))
		if len(p.Suggest) > 0 {
			var names []string
			for _, c := range p.Suggest {
				names = append(names, sdMoveCatLabel(c))
			}
			spaceC.Body += "\n\nLeaving out " + strings.Join(names, ", ") + " makes it fit. Left-out files stay on the old card; keep it to copy them later."
		} else {
			spaceC.Body += "\n\nEven without games, artwork and other files it does not fit. Use a bigger card."
		}
	}
	add(spaceC)
	if len(p.Big) > 0 {
		var bigBytes int64
		for _, it := range p.Big {
			bigBytes += it.Size
		}
		c := sdMoveCheck{ID: "big", Title: "Files too big for FAT32", OK: acceptBig, Hard: true,
			Sub: fmt.Sprintf("%s of 4 GB or more (%s)", pluralizeCount(len(p.Big), "file", "files"), humanBytes(bigBytes))}
		var b strings.Builder
		b.WriteString("The new card is formatted as FAT32, which cannot hold a file of 4 GB or more. These would not fit:\n\n")
		for i, it := range p.Big {
			if i == 30 {
				fmt.Fprintf(&b, "• … and %d more\n", len(p.Big)-30)
				break
			}
			b.WriteString("• " + it.Rel + " (" + humanBytes(it.Size) + ")\n")
		}
		b.WriteString("\n## What to do\n• Best: format the new card as exFAT on a computer (Windows: Format → exFAT; macOS: Disk Utility → ExFAT), then start again. The handheld reads exFAT cards.\n• Or switch on \"Copy without them\": the move leaves them on the old card and lists them.")
		c.Body = b.String()
		if acceptBig {
			c.Sub += " — left on the old card"
		}
		add(c)
	}
	battOK := charging || battPct < 0 || battPct >= sdMoveMinBattery
	battC := sdMoveCheck{ID: "battery", Title: "Battery", OK: battOK, Hard: true}
	switch {
	case charging:
		battC.Sub = "Charging"
	case battPct < 0:
		battC.Sub = "The battery level is not reported"
	default:
		battC.Sub = fmt.Sprintf("%d%% — plug in the charger to be safe", battPct)
		if battPct < sdMoveMinBattery {
			battC.Sub = fmt.Sprintf("%d%% — plug in the charger to start", battPct)
		}
	}
	battC.Body = fmt.Sprintf("Copying a whole card can take an hour or more. Plug in the charger, or start with at least %d%%. The move also stops by itself if the battery drops below %d%% without a charger; it continues where it stopped when you start it again.", sdMoveMinBattery, sdMoveLowBattery)
	add(battC)
	gameC := sdMoveCheck{ID: "game", Title: "No game running", OK: !gameRunning, Hard: true,
		Sub:  map[bool]string{true: "Nothing else is writing to the card", false: "Close the game first: it writes saves while it runs"}[!gameRunning],
		Body: "A running game writes saves and states to the card while the move reads it. Close it first. If you start a game while the move runs, the move waits until you quit it, and copies the saves it wrote at the end."}
	add(gameC)
	if n := len(s.Unreadable); n > 0 {
		var b strings.Builder
		b.WriteString("These could not be read from the old card. That is often the first sign of a failing card — copy everything else now, before more goes.\n\n")
		for i, u := range s.Unreadable {
			if i == 30 {
				fmt.Fprintf(&b, "• … and %d more\n", n-30)
				break
			}
			b.WriteString("• " + u.Path + " — " + u.Why + "\n")
		}
		add(sdMoveCheck{ID: "unreadable", Title: "Could not be read", Sub: pluralizeCount(n, "file or folder", "files or folders") + " on the old card", OK: false, Body: b.String()})
	}
	if n := len(p.Clashes); n > 0 {
		var b strings.Builder
		b.WriteString("The new card does not tell upper and lower case apart, so these cannot sit beside a file of the same name. The first one is copied; these stay on the old card:\n\n")
		for i, c := range p.Clashes {
			if i == 30 {
				fmt.Fprintf(&b, "• … and %d more\n", n-30)
				break
			}
			b.WriteString("• " + c.Path + "\n")
		}
		add(sdMoveCheck{ID: "clash", Title: "Names that clash", Sub: pluralizeCount(n, "file", "files") + " stay on the old card", OK: false, Body: b.String()})
	}
	if p.Reuse > 0 {
		add(sdMoveCheck{ID: "resume", Title: "Continues an earlier move", OK: true, Note: true,
			Sub:  pluralizeCount(p.Reuse, "file is", "files are") + " already on the new card",
			Body: "An earlier move to this card stopped before it finished. Files it already copied, and that have not changed since, are not copied again; the move goes on from there."})
	} else if d.Used > 256<<20 && d.Prev == nil {
		add(sdMoveCheck{ID: "existing", Title: "The new card is not empty", OK: true, Note: true,
			Sub:  humanBytes(d.Used) + " already used",
			Body: "The new card already holds files. They are kept: nothing on the new card is deleted. A file with the same name as one on the old card is replaced by the old card's."})
	}
	if s.Parts > 1 && !s.System {
		add(sdMoveCheck{ID: "parts", Title: "More than one partition", OK: true, Note: true,
			Sub:  fmt.Sprintf("The old card has %d; the one JukaHub runs from is copied", s.Parts),
			Body: "The old card has more than one partition. The move copies the one JukaHub runs from; the others are not touched and not copied."})
	}
	p.CanStart = p.Files > 0
	for _, c := range p.Checks {
		if c.Hard && !c.OK {
			p.CanStart = false
		}
	}
	return p
}
