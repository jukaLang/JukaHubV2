package main

// files_zip.go — Create ZIP in Files: the marked items (or the one under the
// cursor) bundled into one .zip beside them, for sharing or a backup. It is
// the reverse of files_extract.go, and it borrows that file's rules.
//
// Y → Create ZIP asks for the name (the item's name, or the folder's for
// several) and starts a background job; one runs at a time. The job walks the
// items — folders all the way down — and writes them into a hidden
// ".jukahub-zip-*.part" file in the same folder, which is renamed to the
// chosen name only once it is complete. A cancel, a full card or any other
// failure deletes the hidden file, so the folder is left exactly as it was.
//
// What goes in, and how:
//
//   - symbolic links are never followed and never stored: a link to a folder
//     outside would otherwise put that folder (or the whole card) in the ZIP.
//     Devices, pipes and sockets are left out too;
//   - names are relative to the folder, with forward slashes, folders get their
//     own entries (an empty folder survives the round trip), and every entry
//     keeps its modification time and its executable bit;
//   - files that are already compressed (archives, disc images such as CHD and
//     CSO, pictures, music, video, APKs) are stored as they are: deflating them
//     costs the handheld's slow CPU minutes and saves nothing. Everything else
//     is deflated at the fastest level. archive/zip switches to ZIP64 by itself
//     for files and archives past 4 GB.
//
// Before the first byte is written the job compares what the items add up to
// with the free space on the card (less the extractor's margin), so a backup
// never fills the card the system saves to. The progress shows in the sheet's
// row ("Zipping · 42% · 12/80 files"); pressing the row while it runs asks
// once and then stops it. When the job ends, feZipDrain (called from the main
// loop, like feExtractDrain) relists the folder if Files is showing it, puts
// the cursor on the new ZIP and says how it went in a toast.

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// feActZip is the Files sheet's "Create ZIP" row, numbered past
// files_manager.go's own kinds (and the DOS shelf's 1000) so a new kind there
// cannot collide.
const feActZip feActionKind = 1010

const (
	// feZipTempPrefix and feZipTempSuffix name the hidden file a ZIP is
	// written to before it gets its name. A dot name, so Files does not list
	// it; one a crash left behind is swept the next time a ZIP is made there.
	feZipTempPrefix = ".jukahub-zip-"
	feZipTempSuffix = ".part"
	// feZipEntryOverhead is what one entry costs in the ZIP beyond its data
	// and its name: a local header, a data descriptor and a central record,
	// with room for the ZIP64 and timestamp extras.
	feZipEntryOverhead = int64(160)
	// feZipArmKey arms the running job's row: the second press stops it.
	feZipArmKey = "zip-cancel"
)

var (
	errFeZipTooMany = fmt.Errorf("it holds more than %d items, more than one ZIP may", feExtractMaxEntries)
	errFeZipNothing = errors.New("there was nothing in it a ZIP can hold (links and special files are left out)")
	errFeZipMixed   = errors.New("the items are not all in one folder")
)

// feZipStoredExts are the extensions whose files are stored rather than
// deflated: they are compressed already.
var feZipStoredExts = map[string]bool{
	// Archives and packages.
	".zip": true, ".7z": true, ".rar": true, ".gz": true, ".tgz": true, ".xz": true, ".txz": true,
	".bz2": true, ".tbz2": true, ".zst": true, ".lz": true, ".lz4": true, ".lzma": true, ".cab": true,
	".apk": true, ".xapk": true, ".jar": true, ".deb": true, ".rpm": true, ".squashfs": true,
	".docx": true, ".xlsx": true, ".pptx": true, ".odt": true, ".epub": true, ".cbz": true, ".cbr": true,
	// Compressed disc images.
	".chd": true, ".cso": true, ".zso": true, ".pbp": true, ".rvz": true, ".wia": true,
	// Pictures.
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".avif": true, ".heic": true,
	// Music.
	".mp3": true, ".ogg": true, ".opus": true, ".m4a": true, ".aac": true, ".flac": true, ".wma": true,
	// Video.
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true,
}

// feZipStored reports a file stored as it is.
func feZipStored(name string) bool {
	return feZipStoredExts[strings.ToLower(filepath.Ext(name))]
}

// --- One job --------------------------------------------------------------------

// feZipJob is one ZIP to make.
type feZipJob struct {
	Dir   string   // the folder the items are in; the ZIP lands there too
	Items []string // the items, absolute paths already checked against the Files root
	Name  string   // the ZIP's file name: one FAT-safe component ending ".zip"
}

// feZipOutcome is how a job ended.
type feZipOutcome struct {
	Job        feZipJob
	Target     string // the finished ZIP
	Files      int    // files stored in it
	Size       int64  // its size on the card
	Links      int    // symbolic links left out
	Special    int    // devices, pipes and sockets left out
	Unreadable int    // files that vanished, changed or could not be opened
	Err        error
}

// feZipItem is one entry the walk found.
type feZipItem struct {
	path string      // where it is on the card
	name string      // its name in the ZIP: relative, forward slashes, "/" after a folder
	dir  bool        // a folder entry
	info fs.FileInfo // the walk's Lstat, compared with the opened file
}

// feZipMeter is one job's progress: written by the job's goroutine, read by the
// sheet every frame. A nil meter takes every call and records nothing (the
// test passes nil).
type feZipMeter struct {
	mu         sync.Mutex
	phase      string // "Counting", "Zipping", "Finishing"
	files      int
	filesTotal int
	done       int64
	total      int64
}

// feZipReading is a copy of a meter for the screen.
type feZipReading struct {
	Phase             string
	Files, FilesTotal int
	Done, Total       int64
}

func (m *feZipMeter) setPhase(phase string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.phase = phase
	m.mu.Unlock()
}

func (m *feZipMeter) setTotals(files int, bytes int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.filesTotal, m.total = files, bytes
	m.mu.Unlock()
}

func (m *feZipMeter) addBytes(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.done += n
	m.mu.Unlock()
}

func (m *feZipMeter) fileDone() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.files++
	m.mu.Unlock()
}

func (m *feZipMeter) reading() feZipReading {
	if m == nil {
		return feZipReading{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return feZipReading{Phase: m.phase, Files: m.files, FilesTotal: m.filesTotal, Done: m.done, Total: m.total}
}

// feZipPercent is a reading's progress in percent (by bytes, or by files when
// every file is empty), or -1 while it is still counting.
func feZipPercent(r feZipReading) int {
	var p int64
	switch {
	case r.Phase == "" || r.Phase == "Counting":
		return -1
	case r.Total > 0:
		p = r.Done * 100 / r.Total
	case r.FilesTotal > 0:
		p = int64(r.Files) * 100 / int64(r.FilesTotal)
	default:
		return -1
	}
	return int(max(0, min(100, p)))
}

// feZipRun makes one ZIP. Whatever happens, the hidden file is gone when it
// returns, and the folder holds either the finished ZIP or nothing new.
func feZipRun(ctx context.Context, job feZipJob, meter *feZipMeter) (out feZipOutcome) {
	out.Job = job
	defer func() {
		if r := recover(); r != nil {
			logWarn("files", "zip %s: %v", job.Name, r)
			out.Target = ""
			out.Err = fmt.Errorf("an internal error stopped it (%v)", r)
		}
	}()
	meter.setPhase("Counting")
	feZipSweep(job.Dir)
	items, total, err := feZipCollect(ctx, job, &out)
	if err != nil {
		out.Err = err
		return out
	}
	files := 0
	for _, it := range items {
		if !it.dir {
			files++
		}
	}
	meter.setTotals(files, total)
	// The ZIP is never larger than its files plus the entries' headers (what
	// is deflated only shrinks; what would grow is stored), so that plus the
	// extractor's margin must be free before anything is written.
	need := satAddBytes(total, int64(len(items))*feZipEntryOverhead)
	for _, it := range items {
		need = satAddBytes(need, int64(len(it.name)))
	}
	if budget, free := feExtractBudget(job.Dir); free >= 0 && need > budget {
		out.Err = feNoSpaceError{need: satAddBytes(need, feExtractSpaceMargin), free: free}
		return out
	}
	meter.setPhase("Zipping")
	temp, err := feZipWrite(ctx, job, items, meter, &out)
	if err != nil {
		out.Err = err
		return out
	}
	// "Finishing" is set before the last look at the cancel: feZipCancel reads
	// the phase, so a cancel either sees it and is refused, or lands before
	// this check and is honoured.
	meter.setPhase("Finishing")
	if err := ctx.Err(); err != nil {
		_ = os.Remove(temp)
		out.Err = err
		return out
	}
	target, err := feZipPlace(temp, job.Dir, job.Name)
	if err != nil {
		_ = os.Remove(temp)
		out.Err = err
		return out
	}
	out.Target = target
	if fi, err := os.Stat(target); err == nil {
		out.Size = fi.Size()
	}
	return out
}

// feZipCollect walks the job's items and answers the entries in the order they
// are written (a folder before what is in it) and what the files add up to.
// Links, special files and the hidden files of other jobs are counted in out
// and left out. A folder that cannot be read fails the job: a backup that
// silently misses a folder is worse than none.
func feZipCollect(ctx context.Context, job feZipJob, out *feZipOutcome) ([]feZipItem, int64, error) {
	var items []feZipItem
	var total int64
	add := func(path string, info fs.FileInfo) error {
		if len(items) >= feExtractMaxEntries {
			return errFeZipTooMany
		}
		rel, err := filepath.Rel(job.Dir, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return errFeZipMixed
		}
		name := filepath.ToSlash(rel)
		if info.IsDir() {
			name += "/"
		} else {
			total = satAddBytes(total, info.Size())
		}
		items = append(items, feZipItem{path: path, name: name, dir: info.IsDir(), info: info})
		return nil
	}
	// skip sorts out what never goes in; true when the entry is left out.
	skip := func(name string, mode fs.FileMode) bool {
		switch {
		case mode&fs.ModeSymlink != 0:
			out.Links++
			return true
		case !mode.IsDir() && !mode.IsRegular():
			out.Special++
			return true
		case strings.HasPrefix(name, feZipTempPrefix) && strings.HasSuffix(name, feZipTempSuffix),
			mode.IsDir() && strings.HasPrefix(name, feExtractStagePrefix):
			// Another job's unfinished output, or an extraction's staging
			// folder: neither is the user's file yet.
			return true
		}
		return false
	}
	for _, target := range job.Items {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return nil, 0, err
		}
		if skip(info.Name(), info.Mode()) {
			continue
		}
		if !info.IsDir() {
			if err := add(target, info); err != nil {
				return nil, 0, err
			}
			continue
		}
		// WalkDir never follows a link (it reports it, and skip leaves it
		// out), so nothing outside the item can be reached through one.
		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if skip(d.Name(), d.Type()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// Gone since the folder was read.
					return nil
				}
				return err
			}
			if skip(d.Name(), fi.Mode()) {
				return nil
			}
			return add(p, fi)
		})
		if err != nil {
			return nil, 0, err
		}
	}
	if len(items) == 0 {
		return nil, 0, errFeZipNothing
	}
	return items, total, nil
}

// feZipWrite writes the entries into a new hidden file in the job's folder and
// answers its path. On any error the file is removed.
func feZipWrite(ctx context.Context, job feZipJob, items []feZipItem, meter *feZipMeter, out *feZipOutcome) (_ string, err error) {
	f, err := os.CreateTemp(job.Dir, feZipTempPrefix+"*"+feZipTempSuffix)
	if err != nil {
		return "", err
	}
	temp := f.Name()
	// No Windows hidden attribute (filesSetHidden): a rename keeps it, so the
	// finished ZIP would be hidden in Files and Explorer. The dot name keeps
	// the file out of Files while it is written.
	closed, kept := false, false
	defer func() {
		// On a panic too (feZipRun recovers it), when err is still nil.
		if !kept {
			if !closed {
				_ = f.Close()
			}
			_ = os.Remove(temp)
		}
	}()
	bw := bufio.NewWriterSize(f, feExtractBufSize)
	zw := zip.NewWriter(bw)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})
	buf := make([]byte, feExtractBufSize)
	for _, it := range items {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if filesSamePath(it.path, temp) {
			continue
		}
		if err = feZipEntry(ctx, zw, it, meter, buf, out); err != nil {
			return "", err
		}
	}
	if err = zw.Close(); err != nil {
		return "", err
	}
	if err = bw.Flush(); err != nil {
		return "", err
	}
	// On the card, before the rename: a power cut after it must not leave a
	// ZIP with the right name and half its data.
	_ = f.Sync()
	closed = true
	if err = f.Close(); err != nil {
		return "", err
	}
	kept = true
	return temp, nil
}

// feZipEntry writes one entry. A file is opened before its header is written
// and must still be the file the walk saw (os.SameFile): one swapped for a link
// in between, gone, or locked by another program is left out and counted.
func feZipEntry(ctx context.Context, zw *zip.Writer, it feZipItem, meter *feZipMeter, buf []byte, out *feZipOutcome) error {
	hdr := &zip.FileHeader{Name: it.name, Modified: feZipTime(it.info.ModTime())}
	hdr.SetMode(it.info.Mode())
	if it.dir {
		hdr.Method = zip.Store
		_, err := zw.CreateHeader(hdr)
		return err
	}
	src, err := os.Open(it.path)
	if err != nil {
		logWarn("files", "zip: left out %s: %v", it.path, err)
		out.Unreadable++
		meter.addBytes(it.info.Size())
		meter.fileDone()
		return nil
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(st, it.info) {
		logWarn("files", "zip: left out %s: it changed while the ZIP was made", it.path)
		out.Unreadable++
		meter.addBytes(it.info.Size())
		meter.fileDone()
		return nil
	}
	hdr.Method = zip.Deflate
	if st.Size() == 0 || feZipStored(it.name) {
		hdr.Method = zip.Store
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			meter.addBytes(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("reading %s: %w", filepath.Base(it.path), rerr)
		}
	}
	out.Files++
	meter.fileDone()
	return nil
}

// feZipTime is the time an entry records. A ZIP's MS-DOS date cannot go back
// past 1980, and a handheld that booted before network time stamps files in
// 1970: those get 1980-01-01, so the two dates a ZIP keeps agree.
func feZipTime(t time.Time) time.Time {
	if t.IsZero() || t.Year() < 1980 {
		return time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	return t
}

// feZipPlace gives the finished hidden file its name: name, or "name (2).zip"
// when that is taken, never replacing anything.
func feZipPlace(temp, dir, name string) (string, error) {
	for tries := 0; tries < 8; tries++ {
		target, err := feExtractFreeName(dir, name, false)
		if err != nil {
			return "", err
		}
		err = feRenameNoReplace(temp, target)
		if err == nil {
			return target, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		// Something took the name after it was found free: the next one.
	}
	return "", fmt.Errorf("no free name for %q could be kept", name)
}

// feZipSweep removes hidden ZIP files a crash left in dir. Only one job runs at
// a time, so a file with the prefix that is older than the extractor's stale
// age cannot be a running job's.
func feZipSweep(dir string) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, item := range items {
		name := item.Name()
		if !item.Type().IsRegular() || !strings.HasPrefix(name, feZipTempPrefix) || !strings.HasSuffix(name, feZipTempSuffix) {
			continue
		}
		info, err := item.Info()
		if err != nil || time.Since(info.ModTime()) < feExtractStaleAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			logWarn("files", "zip: could not remove the leftover %s: %v", name, err)
		}
	}
}

// --- The one job at a time ---------------------------------------------------------

// feZipState runs one ZIP at a time: two at once on one SD card would only make
// both slower, and the sheet has one row to show progress in. The goroutine
// records the outcome; feZipDrain reports it on the SDL thread.
var feZipState struct {
	mu       sync.Mutex
	running  bool
	job      feZipJob
	cancel   context.CancelFunc
	meter    *feZipMeter
	outcomes []feZipOutcome
}

// feZipStart starts job, or says why not (another ZIP is being made).
func feZipStart(job feZipJob) error {
	s := &feZipState
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("%s is being made — one ZIP at a time", s.job.Name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	meter := &feZipMeter{phase: "Counting"}
	s.running, s.job, s.cancel, s.meter = true, job, cancel, meter
	go func() {
		outcome := feZipRun(ctx, job, meter)
		cancel()
		if outcome.Err != nil && !errors.Is(outcome.Err, context.Canceled) {
			logWarn("files", "zip %s in %s: %v", job.Name, job.Dir, outcome.Err)
		}
		// The outcome and the end of "running" are one step, so the sheet
		// never shows a finished job as still zipping.
		s.mu.Lock()
		s.outcomes = append(s.outcomes, outcome)
		s.running, s.job, s.cancel, s.meter = false, feZipJob{}, nil, nil
		s.mu.Unlock()
	}()
	return nil
}

// feZipCancel stops the running job. One already giving the ZIP its name is too
// far along to stop and answers false.
func feZipCancel() bool {
	s := &feZipState
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cancel == nil || s.meter.reading().Phase == "Finishing" {
		return false
	}
	s.cancel()
	return true
}

// feZipShutdown stops a running job when JukaHub quits and gives it a moment to
// delete its hidden file (what is left is swept by the next job there).
func feZipShutdown() {
	if !feZipCancel() {
		return
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		feZipState.mu.Lock()
		running := feZipState.running
		feZipState.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// feZipStatus is the job as the sheet shows it.
type feZipStatus struct {
	Active  bool
	Job     feZipJob
	Reading feZipReading
}

// feZipSnapshot reads the job for one frame.
func feZipSnapshot() feZipStatus {
	s := &feZipState
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return feZipStatus{}
	}
	return feZipStatus{Active: true, Job: s.job, Reading: s.meter.reading()}
}

// feZipTakeOutcomes hands the finished job to the drain.
func feZipTakeOutcomes() []feZipOutcome {
	s := &feZipState
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.outcomes
	s.outcomes = nil
	return out
}

// --- The sheet's row ------------------------------------------------------------

// feZipAction is the Y menu's Create ZIP row: the running job's progress while
// one runs (a press asks to stop it), otherwise Create ZIP for the targets.
func feZipAction(targets []FileEntry) feAction {
	if s := feZipSnapshot(); s.Active {
		r := s.Reading
		detail := "counting…"
		switch pct := feZipPercent(r); {
		case r.Phase == "Finishing":
			detail = "finishing…"
		case pct >= 0:
			detail = fmt.Sprintf("%d%% · %d/%d files", pct, r.Files, r.FilesTotal)
		}
		return feAction{Kind: feActZip, Label: "Zipping", Detail: detail, Enabled: true}
	}
	if len(targets) == 0 {
		return feAction{Kind: feActZip, Label: "Create ZIP", Detail: "nothing selected", Enabled: false}
	}
	return feAction{Kind: feActZip, Label: "Create ZIP", Detail: feItemCountLabel(len(targets)), Enabled: true}
}

// feZipActivate runs the row: the second press on a running job stops it;
// otherwise the name is asked for and the job starts on Done.
func feZipActivate(config *Config) {
	if s := feZipSnapshot(); s.Active {
		name := ellipsizeRunes(s.Job.Name, 40)
		if !feSheetArmed(feZipArmKey) {
			feSheet.armed = feZipArmKey
			feSheet.armedAt = time.Now()
			PlayNavSound()
			// The name stays out of hintSentence's format: it is cached per
			// format, and a "%" in a file name would read as a verb.
			showToast("Making "+name+" — "+hintSentence("press %s again to stop", "A", "Stop"), ToastWarn())
			return
		}
		feSheet.armed = ""
		if feZipCancel() {
			PlayBackSound()
			showToast("Stopping "+name+"…", ToastInfo())
		} else {
			showToast(name+" is getting its name and can no longer be stopped", ToastInfo())
		}
		return
	}
	targets := feSelectionTargets(config)
	if len(targets) == 0 {
		showToast(hintSentence("Pick files or folders to zip, or mark several with %s", "X", "Mark"), ToastInfo())
		return
	}
	// A new file in a system folder asks twice, as New folder does.
	if !filesSystemConfirm("Create ZIP") {
		return
	}
	dir := filesNav.listedPath
	if dir == "" {
		dir = feCurrentPath(config)
	}
	paths := make([]string, 0, len(targets))
	for _, t := range targets {
		paths = append(paths, t.Path)
	}
	seed := feZipSeedName(targets, dir)
	closeFeSheetKeepKeyboard()
	osPromptOpen("Create ZIP", "Name of the ZIP file", "Create", seed, 120, func(typed string) {
		feZipSubmit(appConfig, paths, typed)
	})
}

// feZipSeedName is the name the prompt starts with: one item's name (a file's
// without its extension, so notes.txt becomes notes.zip) or, for several, the
// folder's.
func feZipSeedName(targets []FileEntry, dir string) string {
	stem := filepath.Base(dir)
	if len(targets) == 1 {
		stem = targets[0].Name
		if ext := filepath.Ext(stem); !targets[0].IsDir && ext != "" && len(ext) < len(stem) {
			stem = strings.TrimSuffix(stem, ext)
		}
	}
	stem = strings.TrimLeft(strings.TrimSpace(stem), ".")
	if stem == "" || strings.ContainsAny(stem, `/\:`) {
		stem = "Archive"
	}
	return stem + ".zip"
}

// feZipFileName turns what was typed into the ZIP's file name: one component a
// FAT card and Windows can store (the extractor's rules), not hidden, ending
// in ".zip".
func feZipFileName(typed string) string {
	name := strings.TrimSpace(typed)
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		name = name[:len(name)-len(".zip")]
	}
	name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "."))
	if name != "" {
		name = feExtractComponent(name)
	}
	if len(name) > 200 {
		// The prompt takes 120 letters, which in a script of three bytes a
		// letter pass the 255 bytes a Linux file system allows a name; cut on
		// a letter's boundary, leaving room for ".zip" and " (2)".
		cut := 0
		for i := range name {
			if i > 200 {
				break
			}
			cut = i
		}
		name = strings.TrimRight(name[:cut], " .")
	}
	if name == "" || name == "_" {
		name = "Archive"
	}
	return name + ".zip"
}

// feZipSubmit starts the job for the items the row was pressed on, once the
// name is typed. The paths come from the listing, so each is checked against
// the Files root again.
func feZipSubmit(config *Config, paths []string, typed string) {
	if strings.TrimSpace(typed) == "" {
		showToast("No name typed — nothing was zipped", ToastInfo())
		return
	}
	root := "."
	if config != nil {
		root = feRoot(config)
	}
	items := make([]string, 0, len(paths))
	dir := ""
	for _, p := range paths {
		resolved, err := resolveWithinRoot(root, p)
		if err != nil {
			showToast("Cannot zip "+filepath.Base(p)+": "+filesFriendlyError(err), ToastError())
			return
		}
		parent := filepath.Dir(resolved)
		if dir == "" {
			dir = parent
		} else if !filesSamePath(dir, parent) {
			showToast("Cannot zip: "+errFeZipMixed.Error(), ToastError())
			return
		}
		items = append(items, resolved)
	}
	if len(items) == 0 {
		return
	}
	job := feZipJob{Dir: dir, Items: items, Name: feZipFileName(typed)}
	if err := feZipStart(job); err != nil {
		showToast(err.Error(), ToastInfo())
		return
	}
	PlayActivateSound()
	showToast("Zipping "+feItemCountLabel(len(items))+" into "+ellipsizeRunes(job.Name, 40)+"…", ToastInfo())
}

// --- Reporting back -------------------------------------------------------------

// feZipDrain reports a finished job on the SDL thread. The main loop calls it
// every frame, wherever the user is, so a ZIP finished behind another screen
// still says so.
func feZipDrain(config *Config) {
	for _, o := range feZipTakeOutcomes() {
		feZipReport(config, o)
	}
}

// feZipReport relists the folder when Files shows it and raises the toast. The
// cursor goes onto the new ZIP unless a sheet is open over the list (its rows
// act on the row under the cursor, so it keeps its file then), and an armed
// row is dropped: a second press must never land on a row the relist moved.
func feZipReport(config *Config, o feZipOutcome) {
	if feSheet.armed == feZipArmKey {
		feSheet.armed = ""
	}
	if o.Err == nil && config != nil && feSceneActive(config) && !filesNav.atLocations && filesSamePath(filesNav.listedPath, o.Job.Dir) {
		focused, hasFocus := feFocusEntry(config)
		feListDirectory(config)
		switch {
		case feSheet.open:
			feSheet.armed = ""
			if hasFocus {
				feFocusPath(config, focused.Path)
			}
		case o.Target != "":
			feFocusPath(config, o.Target)
		}
	}
	msg, color := feZipOutcomeMessage(o)
	showToast(msg, color)
}

// feZipOutcomeMessage is a finished job's toast.
func feZipOutcomeMessage(o feZipOutcome) (string, sdl.Color) {
	name := ellipsizeRunes(o.Job.Name, 40)
	switch {
	case errors.Is(o.Err, context.Canceled):
		return "Stopped — " + name + " was not created", ToastInfo()
	case o.Err != nil:
		return "Cannot create " + name + ": " + feZipFriendlyError(o.Err), ToastError()
	}
	color := ToastSuccess()
	msg := "Created " + ellipsizeRunes(filepath.Base(o.Target), 40) + " · " + feFilesWord(o.Files) + " · " + humanBytes(o.Size)
	var left []string
	add := func(n int, one, many string) {
		if n > 0 {
			left = append(left, feCountWord(n, one, many))
		}
	}
	add(o.Links, "link", "links")
	add(o.Special, "special file", "special files")
	add(o.Unreadable, "unreadable file", "unreadable files")
	if len(left) > 0 {
		msg += " · " + strings.Join(left, ", ") + " left out"
		if o.Unreadable > 0 {
			color = ToastWarn()
		}
	}
	return msg, color
}

// feZipFriendlyError is why a job failed, as the clause after "Cannot create
// name: ", on one line (the log has the rest).
func feZipFriendlyError(err error) string {
	var space feNoSpaceError
	var msg string
	switch {
	case errors.As(err, &space):
		msg = space.Error()
	case errors.Is(err, errFeZipTooMany), errors.Is(err, errFeZipNothing), errors.Is(err, errFeZipMixed):
		msg = err.Error()
	case isDiskFullError(err):
		msg = "the card is full"
	case feFileTooLarge(err):
		msg = "it grew past 4 GB, more than this card's FAT32 format can hold in one file"
	case errors.Is(err, fs.ErrPermission):
		msg = "this folder cannot be written to"
		// The walk's refusals (lstat, open, readdirent) are a folder in the
		// items that cannot be read, not the ZIP's own folder.
		var pe *fs.PathError
		if errors.As(err, &pe) && pe.Op != "createtemp" && pe.Op != "write" && pe.Op != "close" {
			msg = filepath.Base(pe.Path) + " cannot be read"
		}
	default:
		msg = filesFriendlyError(err)
	}
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	return ellipsizeRunes(msg, 120)
}
