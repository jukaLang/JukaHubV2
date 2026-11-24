package main

// savetimeline.go — the Save Time Machine: a timeline of each game's safe
// saves, and checkpoints with a name, each restored with one press
// (savetimeline_ui.go is the screen).
//
// Moments. The save ledger (saveledger.go) reads back every save a library
// game writes once it has settled: on the card, whole, its layout intact.
// When it confirms one, this file copies the game's save files — its battery
// save, RetroArch's slot and auto states, NextUI's and DraStic's own names,
// found by the save handoff's rules (shoFileKind) in every folder the Save
// Manager knows — into a moment:
//
//	<data>/save-manager/timeline/<game>/<seq>-<trigger>/manifest.json + files/
//
// at most once every saveTimelineMinutes while the game runs (10 by default,
// 0 turns it off), and once more when the session ends if a save landed since.
// A moment is the Save Manager's snapshot format narrowed to one game: plain
// files and a manifest with the size and SHA-256 of each, so it can be put
// back by hand too. A copy whose file changed while it was read is thrown
// away (the next confirmed save tries again), and one that holds exactly the
// newest moment's bytes is not kept. Each game keeps its newest
// saveTimelineKeep moments within saveTimelineGameMax; all games together stay
// under saveTimelineTotalMax (the game with the most bytes gives up its oldest
// moment first, and every game keeps its newest two).
//
// Checkpoints. A moment can be pinned under a name ("Before the boss"): it
// moves to save-manager/checkpoints/<game>/, where nothing prunes it. "Pin
// now" makes one of the saves as they are — after a RetroArch game closed
// normally that includes the auto state it wrote on quit, which the next
// launch loads (JukaHub turns savestate_auto_load on for its launches), so a
// checkpoint is a place in the game to come back to.
//
// Restoring. One press: the moment is proved first (every file present at
// its size and SHA-256, every target inside its save folder and not through a
// link: the Save Manager's planSaveRestore), then the game's saves as they
// are now become a "Before restore" moment, then each file lands through a
// temporary file checked against its hash and renamed into place. A battery
// save or auto state the moment did not have is removed — it is in the
// "Before restore" moment — so the next launch really starts from that
// moment; slot states made since are left alone. Nothing is restored while a
// game runs.
//
// When. A handheld boots before network time, so moments are numbered (seq)
// and carry the ledger's stamp (boot id, CLOCK_BOOTTIME, a date only when the
// clock was believable) for the words "4 min ago" or "18 Nov".
//
// Nothing here was run on a device from this desk.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	saveTimelineDirName    = "timeline"
	saveCheckpointsDirName = "checkpoints"
	saveMomentVersion      = 1
	saveMomentManifestName = "manifest.json"
	saveMomentManifestMax  = 4 << 20
)

const (
	// saveTimelineKey is the setting: minutes between two moments while a
	// game runs, 0 for none.
	saveTimelineKey        = "saveTimelineMinutes"
	saveTimelineDefaultMin = 10
	saveTimelineMaxMin     = 240
)

const (
	// saveTimelineKeep moments per game, within saveTimelineGameMax bytes,
	// and saveTimelineTotalMax for every game's together. Checkpoints are not
	// counted: the player made each one.
	saveTimelineKeep     = 12
	saveTimelineGameMax  = 192 << 20
	saveTimelineTotalMax = 1 << 30
	// saveTimelineKeepMin is what pruning never goes under for a game.
	saveTimelineKeepMin = 2
	// saveTimelineFreeFloor is the room a new moment leaves on the card.
	saveTimelineFreeFloor = 128 << 20
	// saveCheckpointMax bounds one game's checkpoints.
	saveCheckpointMax = 40
	// saveMomentNameMax is the longest checkpoint name, in characters.
	saveMomentNameMax = 40
)

const (
	// saveTimelineWalkMax and saveTimelineWalkDepth bound one look through
	// the save folders, as the ledger's do.
	saveTimelineWalkMax   = 20000
	saveTimelineWalkDepth = 4
	// While a game runs a moment is copied a megabyte at a time with a short
	// pause between: a big state read in one go can make the game stutter.
	saveTimelineCopyChunk = 1 << 20
	saveTimelineCopyPause = 4 * time.Millisecond
)

// What led to a moment, beyond the ledger's own triggers (play, sleep,
// battery, poweroff, quit) and the end of a crashed session.
const (
	saveMomentBeforeRestore = "before-restore"
	saveMomentBeforeUpload  = "before-upload"
	saveMomentManual        = "manual"
)

var (
	errSaveTimelineNoSaves   = errors.New("no saves of this game were found in the save folders JukaHub knows")
	errSaveTimelineUnchanged = errors.New("the saves have not changed since the newest moment")
	errSaveTimelineBusy      = errors.New("a save changed while it was being copied")
	errSaveTimelineRunning   = errors.New("quit the game first: an emulator holds its saves open")
)

// saveMomentManifest is a moment's manifest.json: the Save Manager's snapshot
// roots plus who the moment belongs to and what led to it.
type saveMomentManifest struct {
	Version    int                `json:"version"`
	ID         string             `json:"id"`
	Seq        uint64             `json:"seq"`
	Game       string             `json:"game"`
	Title      string             `json:"title,omitempty"`
	System     string             `json:"system,omitempty"`
	Name       string             `json:"name,omitempty"`
	Trigger    string             `json:"trigger"`
	Kinds      []string           `json:"kinds,omitempty"`
	Stamp      saveLedgerStamp    `json:"stamp"`
	Created    time.Time          `json:"created"`
	TotalBytes int64              `json:"totalBytes"`
	Roots      []saveSnapshotRoot `json:"roots"`
}

// saveMoment is a manifest and the folder that holds its files.
type saveMoment struct {
	M          saveMomentManifest
	Dir        string
	Checkpoint bool
}

// saveTimelineMu keeps the time machine's writes one at a time: a capture,
// a restore, a pin, a rename or a delete.
var saveTimelineMu sync.Mutex

// saveTimelineGen counts changes, so the screen reads its lists again.
var saveTimelineGen atomic.Uint64

// saveTimelineMinutesNow is the setting, published from the main thread
// (saveTimelinePublish) for the ledger's goroutines.
var saveTimelineMinutesNow atomic.Int64

func init() {
	saveTimelineMinutesNow.Store(saveTimelineDefaultMin)
	// The setting is read where appConfig may be read: at each launch.
	gameLaunchRegisterStarted(func(libraryGame, string, *exec.Cmd) { saveTimelinePublish(appConfig) })
}

// ── Where ────────────────────────────────────────────────────────────────

// saveTimelineRoot is the folder of every game's moments, or checkpoints.
func saveTimelineRoot(checkpoint bool) string {
	if checkpoint {
		return filepath.Join(saveManagerDir(), saveCheckpointsDirName)
	}
	return filepath.Join(saveManagerDir(), saveTimelineDirName)
}

// saveTimelineGameKey is a game's folder name: its file name, FAT-safe and
// short, and a hash of its whole path, so two "Tetris.gb" in two folders keep
// two timelines. Pure.
func saveTimelineGameKey(gamePath string) string {
	slug := saveRootSlug(ssyncGameStem(gamePath))
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(gamePath))
	return fmt.Sprintf("%s-%08x", slug, h.Sum32())
}

// saveTimelineGameDir is one game's folder of moments, or of checkpoints.
func saveTimelineGameDir(gamePath string, checkpoint bool) string {
	return filepath.Join(saveTimelineRoot(checkpoint), saveTimelineGameKey(gamePath))
}

// saveMomentID names a moment's folder: its number, then what led to it.
// Pure.
func saveMomentID(seq uint64, trigger string) string {
	t := saveRootSlug(trigger)
	if t == "root" {
		t = "save"
	}
	return fmt.Sprintf("%06d-%s", seq, t)
}

// saveMomentSeqOf is the number a moment folder's name starts with.
func saveMomentSeqOf(name string) (uint64, bool) {
	name = strings.TrimPrefix(name, ".tmp-")
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(name[:i], 10, 64)
	return n, err == nil
}

// saveTimelineNextSeq is the number the game's next moment or checkpoint
// gets: one past every one it has, in both folders.
func saveTimelineNextSeq(gamePath string) uint64 {
	var top uint64
	for _, cp := range []bool{false, true} {
		entries, _ := os.ReadDir(saveTimelineGameDir(gamePath, cp))
		for _, e := range entries {
			if n, ok := saveMomentSeqOf(e.Name()); ok && n > top {
				top = n
			}
		}
	}
	return top + 1
}

// ── The game's files ─────────────────────────────────────────────────────

// saveTimelineFile is one of the game's save files as a look found it.
type saveTimelineFile struct {
	root  saveRoot
	rel   string // slash path inside the root
	path  string
	size  int64
	mtime int64
	kind  string
}

// saveTimelineGameFiles lists the game's save files in roots: what the save
// handoff counts as the game's (not the state screenshots, nor the previous
// auto state JukaHub keeps), bounded in entries and depth.
func saveTimelineGameFiles(roots []saveRoot, g shoGame) []saveTimelineFile {
	var out []saveTimelineFile
	if g.stem == "" {
		return nil
	}
	visited := 0
	for _, root := range roots {
		rootPath := root.Path
		if rootPath == "" {
			continue
		}
		stopped := false
		_ = filepath.WalkDir(rootPath, func(p string, d os.DirEntry, err error) error {
			visited++
			if visited > saveTimelineWalkMax {
				stopped = true
				return filepath.SkipAll
			}
			if err != nil || d == nil {
				return nil
			}
			rel, relErr := filepath.Rel(rootPath, p)
			if relErr != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel != "." && strings.Count(rel, "/")+1 > saveTimelineWalkDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !isSaveFile(p) || strings.HasSuffix(strings.ToLower(rel), ".png") {
				return nil
			}
			if match, _ := shoFileKind(rel, g); !match {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, saveTimelineFile{
				root: root, rel: rel, path: p, size: info.Size(), mtime: info.ModTime().UnixNano(),
				kind: saveLedgerKindOf(path.Base(rel), g),
			})
			return nil
		})
		if stopped {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].root.Path != out[j].root.Path {
			return out[i].root.Path < out[j].root.Path
		}
		return out[i].rel < out[j].rel
	})
	return out
}

// saveTimelinePathKey compares two paths the way the card does: FAT and
// Windows ignore case.
func saveTimelinePathKey(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

// ── Copies ───────────────────────────────────────────────────────────────

// saveTimelineCopyFile copies one file and hashes it in the same pass; with
// throttle (a game is running) it pauses between megabytes.
func saveTimelineCopyFile(src, dst string, throttle bool) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	if info.Size() > saveManagerMaxFile {
		return "", 0, fmt.Errorf("file is larger than %s", humanBytes(saveManagerMaxFile))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	w := io.MultiWriter(out, h)
	buf := make([]byte, 64<<10)
	var n int64
	var copyErr error
	for {
		chunk, err := io.CopyBuffer(w, io.LimitReader(in, saveTimelineCopyChunk), buf)
		n += chunk
		if err != nil {
			copyErr = err
			break
		}
		if chunk < saveTimelineCopyChunk {
			break
		}
		if throttle {
			time.Sleep(saveTimelineCopyPause)
		}
	}
	syncErr := out.Sync()
	closeErr := out.Close()
	for _, e := range []error{copyErr, syncErr, closeErr} {
		if e != nil {
			return "", n, e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// saveMomentWriteManifest writes a manifest through a temporary file.
func saveMomentWriteManifest(dir string, m saveMomentManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(dir, saveMomentManifestName)
	tmp := final + ".tmp"
	if err := writeFileSynced(tmp, append(data, '\n'), 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// saveMomentLoad reads one moment folder.
func saveMomentLoad(dir string, checkpoint bool) (saveMoment, error) {
	f, err := os.Open(filepath.Join(dir, saveMomentManifestName))
	if err != nil {
		return saveMoment{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, saveMomentManifestMax+1))
	_ = f.Close()
	if err != nil {
		return saveMoment{}, err
	}
	return saveMomentDecode(data, dir, checkpoint)
}

// saveMomentDecode checks a manifest's bytes. Pure apart from its arguments.
func saveMomentDecode(data []byte, dir string, checkpoint bool) (saveMoment, error) {
	if len(data) > saveMomentManifestMax {
		return saveMoment{}, errors.New("moment manifest too large")
	}
	var m saveMomentManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return saveMoment{}, err
	}
	if m.Version < 1 || m.Version > saveMomentVersion || m.ID == "" || strings.TrimSpace(m.Game) == "" {
		return saveMoment{}, errors.New("not a moment manifest")
	}
	return saveMoment{M: m, Dir: dir, Checkpoint: checkpoint}, nil
}

// saveMomentSnapshot is a moment as the Save Manager's restore reads it.
func saveMomentSnapshot(m saveMoment) saveSnapshot {
	return saveSnapshot{
		Manifest: saveSnapshotManifest{Version: 1, ID: m.M.ID, Label: m.M.Trigger, Created: m.M.Created, TotalBytes: m.M.TotalBytes, Roots: m.M.Roots},
		Dir:      m.Dir,
	}
}

// saveMomentSameFiles reports whether two moments hold the same files with
// the same bytes. Pure.
func saveMomentSameFiles(a, b saveMomentManifest) bool {
	set := func(m saveMomentManifest) map[string]string {
		out := map[string]string{}
		for _, r := range m.Roots {
			for _, f := range r.Files {
				out[saveTimelinePathKey(filepath.Join(r.Path, filepath.FromSlash(f.Path)))] = strings.ToLower(f.SHA256)
			}
		}
		return out
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for k, v := range sa {
		if sb[k] != v {
			return false
		}
	}
	return true
}

// ── Capture ──────────────────────────────────────────────────────────────

// saveTimelineCapture makes a moment (or, with checkpoint, a named
// checkpoint) of the game's saves as they are. throttle paces the copy for a
// running game. A timeline moment equal to the newest one is not kept: the
// newest is returned with errSaveTimelineUnchanged.
func saveTimelineCapture(game libraryGame, roots []saveRoot, trigger, name string, checkpoint, throttle bool) (saveMoment, error) {
	saveTimelineMu.Lock()
	defer saveTimelineMu.Unlock()
	return saveTimelineCaptureLocked(game, roots, trigger, name, checkpoint, throttle, "")
}

// saveTimelineCaptureLocked is saveTimelineCapture under saveTimelineMu;
// spare is a moment folder pruning must not delete (the one a restore is
// about to read).
func saveTimelineCaptureLocked(game libraryGame, roots []saveRoot, trigger, name string, checkpoint, throttle bool, spare string) (saveMoment, error) {
	g := shoGameOf(game)
	files := saveTimelineGameFiles(roots, g)
	if len(files) == 0 {
		return saveMoment{}, errSaveTimelineNoSaves
	}
	var total int64
	for _, f := range files {
		if f.size > saveManagerMaxFile {
			return saveMoment{}, fmt.Errorf("%s is larger than %s — is it really a save?", path.Base(f.rel), humanBytes(saveManagerMaxFile))
		}
		total += f.size
	}
	if total > saveTimelineGameMax {
		return saveMoment{}, fmt.Errorf("this game's saves are %s, more than a moment keeps (%s)", humanBytes(total), humanBytes(saveTimelineGameMax))
	}
	base := saveTimelineGameDir(game.Path, checkpoint)
	if checkpoint {
		if entries, _ := os.ReadDir(base); len(entries) >= saveCheckpointMax {
			return saveMoment{}, fmt.Errorf("this game has %d checkpoints: delete one first", saveCheckpointMax)
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return saveMoment{}, err
	}
	if free, err := freeBytes(base); err == nil && free < total+saveTimelineFreeFloor {
		return saveMoment{}, fmt.Errorf("the card is nearly full (%s free)", humanBytes(free))
	}
	// A copy a power cut or a crash left behind is no moment.
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				_ = os.RemoveAll(filepath.Join(base, e.Name()))
			}
		}
	}
	seq := saveTimelineNextSeq(game.Path)
	id := saveMomentID(seq, trigger)
	staging := filepath.Join(base, ".tmp-"+id)
	fail := func(err error) (saveMoment, error) {
		_ = os.RemoveAll(staging)
		return saveMoment{}, err
	}
	m := saveMomentManifest{
		Version: saveMomentVersion, ID: id, Seq: seq, Game: game.Path, Title: game.Label(), System: game.System,
		Name: saveMomentCleanName(name), Trigger: trigger, Stamp: saveLedgerStampNow(seq), Created: time.Now().UTC(),
	}
	// slugs maps a root's path to its place in m.Roots; used counts the
	// folder names handed out, so two roots of one name get two folders.
	slugs := map[string]int{}
	used := map[string]int{}
	kinds := map[string]bool{}
	for _, f := range files {
		idx, ok := slugs[f.root.Path]
		if !ok {
			slug := saveRootSlug(f.root.Name)
			used[slug]++
			if used[slug] > 1 {
				slug = fmt.Sprintf("%s-%d", slug, used[slug])
			}
			m.Roots = append(m.Roots, saveSnapshotRoot{Name: f.root.Name, Slug: slug, Path: f.root.Path})
			idx = len(m.Roots) - 1
			slugs[f.root.Path] = idx
		}
		dst := filepath.Join(staging, "files", m.Roots[idx].Slug, filepath.FromSlash(f.rel))
		sum, n, err := saveTimelineCopyFile(f.path, dst, throttle)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", path.Base(f.rel), err))
		}
		// What was copied must be what the look found: an emulator writing
		// the file meanwhile makes the copy a mix of two saves.
		if info, err := os.Stat(f.path); err != nil || n != f.size || info.Size() != f.size || info.ModTime().UnixNano() != f.mtime {
			return fail(errSaveTimelineBusy)
		}
		r := &m.Roots[idx]
		r.Bytes += n
		r.Files = append(r.Files, saveSnapshotFile{Path: f.rel, Size: n, SHA256: sum})
		m.TotalBytes += n
		kinds[f.kind] = true
	}
	for _, k := range []string{saveLedgerKindSave, saveLedgerKindAuto, saveLedgerKindState, saveLedgerKindEmu} {
		if kinds[k] {
			m.Kinds = append(m.Kinds, k)
		}
	}
	if !checkpoint {
		if moments, _ := saveTimelineListLocked(game.Path); len(moments) > 0 && saveMomentSameFiles(moments[0].M, m) {
			_ = os.RemoveAll(staging)
			return moments[0], errSaveTimelineUnchanged
		}
	}
	if err := saveMomentWriteManifest(staging, m); err != nil {
		return fail(err)
	}
	final := filepath.Join(base, id)
	if err := os.Rename(staging, final); err != nil {
		return fail(err)
	}
	syncDirectory(base)
	if !checkpoint {
		saveTimelineSizeAdd(game.Path, m.TotalBytes)
		saveTimelinePruneLocked(game.Path, spare)
	}
	saveTimelineGen.Add(1)
	return saveMoment{M: m, Dir: final, Checkpoint: checkpoint}, nil
}

// saveMomentCleanName is a checkpoint name: one line, no control
// characters, at most saveMomentNameMax characters. Pure.
func saveMomentCleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > saveMomentNameMax {
		out = strings.TrimSpace(string(r[:saveMomentNameMax]))
	}
	return out
}

// ── Lists ────────────────────────────────────────────────────────────────

// saveTimelineList is the game's moments and checkpoints, newest first.
func saveTimelineList(gamePath string) (moments, checkpoints []saveMoment) {
	saveTimelineMu.Lock()
	defer saveTimelineMu.Unlock()
	return saveTimelineListLocked(gamePath)
}

func saveTimelineListLocked(gamePath string) (moments, checkpoints []saveMoment) {
	for _, cp := range []bool{false, true} {
		dir := saveTimelineGameDir(gamePath, cp)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			m, err := saveMomentLoad(filepath.Join(dir, e.Name()), cp)
			// The folder name hashes the path; the manifest names it.
			if err != nil || m.M.Game != gamePath {
				continue
			}
			if cp {
				checkpoints = append(checkpoints, m)
			} else {
				moments = append(moments, m)
			}
		}
	}
	newest := func(list []saveMoment) {
		sort.Slice(list, func(i, j int) bool { return list[i].M.Seq > list[j].M.Seq })
	}
	newest(moments)
	newest(checkpoints)
	return moments, checkpoints
}

// saveTimelineGame is one game the time machine holds something for.
type saveTimelineGame struct {
	Path        string
	Title       string
	System      string
	Moments     int
	Checkpoints int
	Newest      saveMoment
	order       uint64
}

// saveTimelineGames lists every game with moments or checkpoints, the one
// played last first (the ledger's order, which does not need the clock).
func saveTimelineGames() []saveTimelineGame {
	saveTimelineMu.Lock()
	byKey := map[string]*saveTimelineGame{}
	for _, cp := range []bool{false, true} {
		root := saveTimelineRoot(cp)
		games, _ := os.ReadDir(root)
		for _, gd := range games {
			if !gd.IsDir() || strings.HasPrefix(gd.Name(), ".") {
				continue
			}
			entries, _ := os.ReadDir(filepath.Join(root, gd.Name()))
			var names []string
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					names = append(names, e.Name())
				}
			}
			if len(names) == 0 {
				continue
			}
			sort.Strings(names) // zero-padded numbers: the last is the newest
			m, err := saveMomentLoad(filepath.Join(root, gd.Name(), names[len(names)-1]), cp)
			if err != nil {
				continue
			}
			g := byKey[gd.Name()]
			if g == nil {
				g = &saveTimelineGame{Path: m.M.Game, Title: m.M.Title, System: m.M.System}
				byKey[gd.Name()] = g
			}
			if cp {
				g.Checkpoints += len(names)
			} else {
				g.Moments += len(names)
			}
			if g.Newest.Dir == "" || m.M.Seq > g.Newest.M.Seq {
				g.Newest = m
			}
		}
	}
	saveTimelineMu.Unlock()
	out := make([]saveTimelineGame, 0, len(byKey))
	saveLedger.mu.Lock()
	for _, g := range byKey {
		if lg := saveLedger.file.Games[g.Path]; lg != nil {
			g.order = lg.Seq
		}
		out = append(out, *g)
	}
	saveLedger.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order > out[j].order
		}
		if !out[i].Newest.M.Created.Equal(out[j].Newest.M.Created) {
			return out[i].Newest.M.Created.After(out[j].Newest.M.Created)
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// ── Pruning ──────────────────────────────────────────────────────────────

// saveTimelineSize is what every game's moments take, read once per run and
// kept up to date by the captures and deletes.
var saveTimelineSize struct {
	mu     sync.Mutex
	loaded bool
	games  map[string]int64 // game folder name → bytes
}

// saveTimelineSizeLoadLocked reads every moment's size, once. Caller holds
// saveTimelineMu (saveTimelineSize.mu is taken here).
func saveTimelineSizeLoadLocked() {
	saveTimelineSize.mu.Lock()
	loaded := saveTimelineSize.loaded
	saveTimelineSize.mu.Unlock()
	if loaded {
		return
	}
	games := map[string]int64{}
	root := saveTimelineRoot(false)
	dirs, _ := os.ReadDir(root)
	for _, gd := range dirs {
		if !gd.IsDir() || strings.HasPrefix(gd.Name(), ".") {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(root, gd.Name()))
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if m, err := saveMomentLoad(filepath.Join(root, gd.Name(), e.Name()), false); err == nil {
				games[gd.Name()] += m.M.TotalBytes
			}
		}
	}
	saveTimelineSize.mu.Lock()
	saveTimelineSize.games, saveTimelineSize.loaded = games, true
	saveTimelineSize.mu.Unlock()
}

// saveTimelineSizeAdd counts bytes in or out of a game's moments.
func saveTimelineSizeAdd(gamePath string, delta int64) {
	saveTimelineSize.mu.Lock()
	defer saveTimelineSize.mu.Unlock()
	if !saveTimelineSize.loaded {
		return // the first full read counts it
	}
	key := saveTimelineGameKey(gamePath)
	saveTimelineSize.games[key] = max(0, saveTimelineSize.games[key]+delta)
}

// saveTimelinePruneLocked keeps the game's newest saveTimelineKeep moments
// within saveTimelineGameMax, then every game's under saveTimelineTotalMax.
// spare is never deleted.
func saveTimelinePruneLocked(gamePath, spare string) {
	moments, _ := saveTimelineListLocked(gamePath)
	var total int64
	for i, m := range moments {
		total += m.M.TotalBytes
		if i < saveTimelineKeepMin || m.Dir == spare {
			continue
		}
		if i >= saveTimelineKeep || total > saveTimelineGameMax {
			saveMomentRemoveLocked(m)
		}
	}
	saveTimelineSizeLoadLocked()
	for round := 0; round < 256; round++ {
		saveTimelineSize.mu.Lock()
		var sum, most int64
		mostKey := ""
		for k, n := range saveTimelineSize.games {
			sum += n
			if n > most {
				most, mostKey = n, k
			}
		}
		saveTimelineSize.mu.Unlock()
		if sum <= saveTimelineTotalMax || mostKey == "" {
			return
		}
		if !saveTimelineDropOldestLocked(mostKey, spare) {
			// That game is down to its newest moments: the next largest
			// gives way, or nothing can.
			saveTimelineSize.mu.Lock()
			delete(saveTimelineSize.games, mostKey)
			saveTimelineSize.mu.Unlock()
		}
	}
}

// saveTimelineDropOldestLocked deletes the oldest moment of one game folder
// past its newest saveTimelineKeepMin; false when there is none to delete.
func saveTimelineDropOldestLocked(key, spare string) bool {
	dir := filepath.Join(saveTimelineRoot(false), key)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i < len(names)-saveTimelineKeepMin; i++ {
		p := filepath.Join(dir, names[i])
		if p == spare {
			continue
		}
		m, err := saveMomentLoad(p, false)
		if err != nil {
			continue
		}
		saveMomentRemoveLocked(m)
		return true
	}
	return false
}

// saveMomentRemoveLocked deletes a moment or checkpoint folder.
func saveMomentRemoveLocked(m saveMoment) error {
	if !isPathWithin(m.Dir, saveTimelineRoot(m.Checkpoint)) {
		return errors.New("that moment is outside the time machine")
	}
	if err := os.RemoveAll(m.Dir); err != nil {
		return err
	}
	if !m.Checkpoint {
		saveTimelineSizeAdd(m.M.Game, -m.M.TotalBytes)
	}
	saveTimelineGen.Add(1)
	return nil
}

// ── Pin, rename, delete ──────────────────────────────────────────────────

// saveTimelinePin turns a moment into a checkpoint named name (a checkpoint
// is renamed). The folder moves; its files are not copied again.
func saveTimelinePin(m saveMoment, name string) (saveMoment, error) {
	saveTimelineMu.Lock()
	defer saveTimelineMu.Unlock()
	name = saveMomentCleanName(name)
	if m.Checkpoint {
		m.M.Name = name
		if err := saveMomentWriteManifest(m.Dir, m.M); err != nil {
			return m, err
		}
		saveTimelineGen.Add(1)
		return m, nil
	}
	cpDir := saveTimelineGameDir(m.M.Game, true)
	if entries, _ := os.ReadDir(cpDir); len(entries) >= saveCheckpointMax {
		return m, fmt.Errorf("this game has %d checkpoints: delete one first", saveCheckpointMax)
	}
	if !isPathWithin(m.Dir, saveTimelineRoot(false)) {
		return m, errors.New("that moment is outside the time machine")
	}
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		return m, err
	}
	final := filepath.Join(cpDir, filepath.Base(m.Dir))
	if _, err := os.Stat(final); err == nil {
		return m, errors.New("a checkpoint with that number is already there")
	}
	if err := os.Rename(m.Dir, final); err != nil {
		return m, err
	}
	saveTimelineSizeAdd(m.M.Game, -m.M.TotalBytes)
	out := saveMoment{M: m.M, Dir: final, Checkpoint: true}
	out.M.Name = name
	if err := saveMomentWriteManifest(final, out.M); err != nil {
		// Pinned all the same, under its old words.
		logWarn("saves", "time machine: the checkpoint's name was not written: %v", err)
	}
	saveTimelineGen.Add(1)
	return out, nil
}

// saveTimelineDelete deletes a moment or checkpoint.
func saveTimelineDelete(m saveMoment) error {
	saveTimelineMu.Lock()
	defer saveTimelineMu.Unlock()
	return saveMomentRemoveLocked(m)
}

// ── Restore ──────────────────────────────────────────────────────────────

// saveTimelineGameRunning reports a game holding the screen, whichever it
// is: its emulator would write its own saves over a restore on quit.
func saveTimelineGameRunning() bool {
	return runningGameChildren.Load() > 0
}

// saveTimelineRestore puts the game's saves back as they were in m. It
// returns the "Before restore" moment it kept first (a zero moment when the
// saves as they were already were the newest moment, or there were none).
func saveTimelineRestore(game libraryGame, m saveMoment, roots []saveRoot) (saveMoment, error) {
	if saveTimelineGameRunning() {
		return saveMoment{}, errSaveTimelineRunning
	}
	saveTimelineMu.Lock()
	defer saveTimelineMu.Unlock()
	if m.M.Game != game.Path {
		return saveMoment{}, errors.New("nothing was changed — that moment belongs to another game")
	}
	plan, err := planSaveRestore(saveMomentSnapshot(m), roots)
	if err != nil {
		return saveMoment{}, fmt.Errorf("nothing was changed — %w", err)
	}
	inMoment := make(map[string]bool, len(plan))
	for _, p := range plan {
		inMoment[saveTimelinePathKey(p.Dst)] = true
	}
	var extras []string
	for _, f := range saveTimelineGameFiles(roots, shoGameOf(game)) {
		if (f.kind == saveLedgerKindSave || f.kind == saveLedgerKindAuto) && !inMoment[saveTimelinePathKey(f.path)] {
			extras = append(extras, f.path)
		}
	}
	before, err := saveTimelineCaptureLocked(game, roots, saveMomentBeforeRestore, "", false, false, m.Dir)
	switch {
	case err == nil:
	case errors.Is(err, errSaveTimelineUnchanged):
		// The saves as they are now are the newest moment already.
		before = saveMoment{}
	case errors.Is(err, errSaveTimelineNoSaves):
		before, extras = saveMoment{}, nil
	default:
		return saveMoment{}, fmt.Errorf("nothing was changed — the saves as they are could not be kept first: %w", err)
	}
	for _, p := range plan {
		if err := saveTimelinePlace(p); err != nil {
			return before, fmt.Errorf("%s: %w", p.Rel, err)
		}
	}
	for _, x := range extras {
		if err := os.Remove(x); err != nil && !os.IsNotExist(err) {
			logWarn("saves", "time machine: could not set %s aside: %v", filepath.Base(x), err)
		}
	}
	saveTimelineGen.Add(1)
	saveTimelineForgetCapture(game.Path)
	logInfo("saves", "time machine: %s restored to %s (%d files)", saveLedgerTitle(game.Title, game.Path), m.M.ID, len(plan))
	return before, nil
}

// saveTimelinePlace writes one proved file: a temporary file beside the
// target, checked against the hash, renamed into place.
func saveTimelinePlace(p saveRestorePlan) error {
	dir := filepath.Dir(p.Dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(p.Dst)+".jukahub-restore.tmp")
	sum, _, err := saveTimelineCopyFile(p.Src, tmp, false)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !strings.EqualFold(sum, p.SHA256) {
		_ = os.Remove(tmp)
		return errors.New("did not land intact — is the card full?")
	}
	if err := os.Rename(tmp, p.Dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(dir)
	return nil
}

// ── While a game runs ────────────────────────────────────────────────────

// saveTimelineRun is what this run knows about each game's captures.
var saveTimelineRun struct {
	mu sync.Mutex
	// last is when the game's last moment was taken (or found unchanged),
	// on Go's monotonic clock; pending marks a confirmed save since.
	last    map[string]time.Time
	pending map[string]bool
	busy    map[string]bool
}

// saveTimelinePublish reads the setting (main thread) for the goroutines.
func saveTimelinePublish(c *Config) {
	saveTimelineMinutesNow.Store(int64(saveTimelineMinutes(c)))
}

// saveTimelineMinutes is the setting: saveTimelineKey, default
// saveTimelineDefaultMin, 0 for no moments. Main thread.
func saveTimelineMinutes(c *Config) int {
	if c == nil {
		return saveTimelineDefaultMin
	}
	v, ok := customValue(c.Variables.Custom, saveTimelineKey)
	if !ok {
		return saveTimelineDefaultMin
	}
	n, ok := coerceInt(v)
	if !ok || n < 0 {
		return saveTimelineDefaultMin
	}
	return min(n, saveTimelineMaxMin)
}

// saveTimelineNoteSave is the ledger's word that one of the game's saves
// read back whole (saveledger.go record, under the session's scan lock): a
// moment is taken when the game has none from this run yet or the last is
// saveTimelineMinutes old. Never blocks.
func saveTimelineNoteSave(game libraryGame, roots []saveRoot) {
	every := time.Duration(saveTimelineMinutesNow.Load()) * time.Minute
	if every <= 0 || len(roots) == 0 {
		return
	}
	saveTimelineRun.mu.Lock()
	if saveTimelineRun.pending == nil {
		saveTimelineRun.pending = map[string]bool{}
		saveTimelineRun.last = map[string]time.Time{}
		saveTimelineRun.busy = map[string]bool{}
	}
	saveTimelineRun.pending[game.Path] = true
	last, seen := saveTimelineRun.last[game.Path]
	due := (!seen || time.Since(last) >= every) && !saveTimelineRun.busy[game.Path]
	if due {
		saveTimelineRun.busy[game.Path] = true
	}
	saveTimelineRun.mu.Unlock()
	if due {
		go saveTimelineTake(game, append([]saveRoot(nil), roots...), saveLedgerTrigPlay, runningGameChildren.Load() > 0)
	}
}

// saveTimelineSessionEnded is the ledger's word that the game's session
// ended (how: quit, crash, battery, poweroff): one last moment when a save
// landed since the last one. Off the main thread. It waits for the copy
// after a quit or a crash; before a power off it does not hold the device up
// (a copy the power cuts short leaves only a staging folder, cleared later).
func saveTimelineSessionEnded(game libraryGame, roots []saveRoot, how string) {
	if saveTimelineMinutesNow.Load() <= 0 || len(roots) == 0 {
		return
	}
	saveTimelineRun.mu.Lock()
	pending := saveTimelineRun.pending[game.Path] && !saveTimelineRun.busy[game.Path]
	if pending {
		saveTimelineRun.busy[game.Path] = true
	}
	saveTimelineRun.mu.Unlock()
	if !pending {
		return
	}
	roots = append([]saveRoot(nil), roots...)
	switch how {
	case saveLedgerTrigBattery, saveLedgerTrigPowerOff:
		go saveTimelineTake(game, roots, how, false)
	default:
		saveTimelineTake(game, roots, how, false)
	}
}

// saveTimelineTake takes one moment and notes it. Off the main thread.
func saveTimelineTake(game libraryGame, roots []saveRoot, trigger string, throttle bool) {
	_, err := saveTimelineCapture(game, roots, trigger, "", false, throttle)
	saveTimelineRun.mu.Lock()
	delete(saveTimelineRun.busy, game.Path)
	if err == nil || errors.Is(err, errSaveTimelineUnchanged) {
		saveTimelineRun.last[game.Path] = time.Now()
		saveTimelineRun.pending[game.Path] = false
	}
	saveTimelineRun.mu.Unlock()
	switch {
	case err == nil:
		logDebug("saves", "time machine: a moment of %s (%s)", saveLedgerTitle(game.Title, game.Path), trigger)
	case errors.Is(err, errSaveTimelineUnchanged), errors.Is(err, errSaveTimelineBusy), errors.Is(err, errSaveTimelineNoSaves):
		// Nothing new, or being written: the next confirmed save tries again.
	default:
		logWarn("saves", "time machine: no moment of %s: %v", saveLedgerTitle(game.Title, game.Path), err)
	}
}

// saveTimelineForgetCapture starts the game's interval over after a restore:
// the next confirmed save is a moment at once.
func saveTimelineForgetCapture(gamePath string) {
	saveTimelineRun.mu.Lock()
	if saveTimelineRun.last != nil {
		delete(saveTimelineRun.last, gamePath)
		saveTimelineRun.pending[gamePath] = false
	}
	saveTimelineRun.mu.Unlock()
}
