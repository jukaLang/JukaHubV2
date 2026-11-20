package main

// nas_download.go — playing a game that is on a network drive.
//
// A on a network game opens its page as usual (gmeta_ui.go); the page tells
// this file (gamePageRegisterOpened) and a sheet opens over it: "Download to
// card and play", with the size and the card's free space, or — when the
// drive does not answer — that it is not reachable and why. The copy runs on
// a goroutine, one game at a time, with its progress on the sheet and on the
// game's library row; B leaves it running in the background, Cancel stops it.
//
// A game is copied whole: a .cue's tracks, an .m3u's discs and their tracks,
// or a folder game's every file. Each file is written beside its final name
// as ".<name>.part" (the library's scan skips dot names) and renamed when it
// is complete; a stopped copy continues from the end of its .part next time
// (nasFS.ReadFrom). The game's own file (the sheet) is renamed last, so a game
// in the library is always a whole one. A folder game is copied into
// ".<folder>.part" and the folder renamed at the end.
//
// What was copied is remembered in <data>/nas-downloads.json, so the game's
// page offers More → Remove downloaded copy: exactly the files written here go
// (saves, save states and anything the game made stay) and the game is back
// on its drive's shelf.

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// nasFreeMargin is the space a download leaves free on the card.
	nasFreeMargin = int64(64 << 20)
	// nasMaxRecords bounds nas-downloads.json.
	nasMaxRecords = 5000
	// nasCopyBuffer is one read's size (SFTP reads it in parallel pieces).
	nasCopyBuffer = 1 << 20
	// nasStallAfter stops a copy that has not moved a byte for this long.
	nasStallAfter = 60 * time.Second
)

// errNasStalled ends a copy whose drive stopped sending (nasDownloadStart).
var errNasStalled = errors.New("the drive stopped sending for a minute — check the Wi-Fi, then try again (the copy continues from there)")

// ── Records of what was copied ───────────────────────────────────────────────

// nasRecord is one game copied from a drive.
type nasRecord struct {
	Path       string   `json:"path"`
	Source     string   `json:"source"`
	SourceName string   `json:"source_name"`
	Base       string   `json:"base"`
	Files      []string `json:"files"`
	Dirs       []string `json:"dirs,omitempty"`
	Size       int64    `json:"size"`
	// Partial is a copy that stopped: Files and Dirs are what it made so
	// far, so the next try tells them from the player's own files. A partial
	// record is never offered for removal.
	Partial bool `json:"partial,omitempty"`
}

var nasRecords struct {
	mu     sync.Mutex
	loaded bool
	items  map[string]nasRecord
}

func nasRecordsLoadLocked() {
	if nasRecords.loaded {
		return
	}
	nasRecords.loaded = true
	nasRecords.items = map[string]nasRecord{}
	p := nasDataPath("nas-downloads.json")
	if p == "" {
		return
	}
	var f struct {
		V     int         `json:"v"`
		Items []nasRecord `json:"items"`
	}
	if _, err := readJSONWithBackup(p, &f); err != nil {
		if !os.IsNotExist(err) {
			logWarn("nas", "ignoring nas-downloads.json: %v", err)
		}
		return
	}
	for _, r := range f.Items {
		if r.Path != "" && len(nasRecords.items) < nasMaxRecords {
			nasRecords.items[r.Path] = r
		}
	}
}

func nasRecordsSaveLocked() {
	p := nasDataPath("nas-downloads.json")
	if p == "" {
		return
	}
	items := make([]nasRecord, 0, len(nasRecords.items))
	for _, r := range nasRecords.items {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	data, err := json.MarshalIndent(struct {
		V     int         `json:"v"`
		Items []nasRecord `json:"items"`
	}{1, items}, "", " ")
	if err != nil {
		return
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o600); err != nil {
		logWarn("nas", "could not save the downloaded games list: %v", err)
	}
}

// nasRecordFor is the record of a game copied to path. Any goroutine; the
// first call reads the file (nasRecordsPreload does that off the render thread).
func nasRecordFor(path string) (nasRecord, bool) {
	nasRecords.mu.Lock()
	defer nasRecords.mu.Unlock()
	nasRecordsLoadLocked()
	r, ok := nasRecords.items[path]
	return r, ok
}

// nasRecordLoaded is nasRecordFor that never reads the disk: false until the
// records are loaded. For chips drawn every frame.
func nasRecordLoaded(path string) (nasRecord, bool) {
	nasRecords.mu.Lock()
	defer nasRecords.mu.Unlock()
	if !nasRecords.loaded {
		return nasRecord{}, false
	}
	r, ok := nasRecords.items[path]
	return r, ok
}

func nasRecordsPreload() {
	go func() {
		nasRecords.mu.Lock()
		nasRecordsLoadLocked()
		nasRecords.mu.Unlock()
	}()
}

func nasRecordPut(r nasRecord) {
	nasRecords.mu.Lock()
	defer nasRecords.mu.Unlock()
	nasRecordsLoadLocked()
	if len(nasRecords.items) >= nasMaxRecords {
		if _, have := nasRecords.items[r.Path]; !have {
			return
		}
	}
	nasRecords.items[r.Path] = r
	nasRecordsSaveLocked()
}

func nasRecordDrop(path string) {
	nasRecords.mu.Lock()
	defer nasRecords.mu.Unlock()
	nasRecordsLoadLocked()
	if _, ok := nasRecords.items[path]; ok {
		delete(nasRecords.items, path)
		nasRecordsSaveLocked()
	}
}

// ── The download ─────────────────────────────────────────────────────────────

// nasDLFile is one file of a game: where it is on the drive and where it goes.
type nasDLFile struct {
	remote string
	local  string
	size   int64
}

// nasDLJob is the download running now. Main thread, except done and total.
type nasDLJob struct {
	path   string
	title  string
	ref    nasLibRef
	play   bool
	cancel context.CancelFunc
	done   atomic.Int64
	total  atomic.Int64
	phase  atomic.Value // string: what it is doing now
}

var nasDL struct {
	job *nasDLJob
}

// nasDownloadPercent is the progress of the download of path, if it runs.
// Main thread, no I/O.
func nasDownloadPercent(path string) (int, bool) {
	job := nasDL.job
	if job == nil || job.path != path {
		return 0, false
	}
	total := job.total.Load()
	if total <= 0 {
		return 0, true
	}
	pct := int(job.done.Load() * 100 / total)
	return min(max(pct, 0), 100), true
}

// nasDownloading reports whether any game is being copied (the page's line).
func nasDownloading() (title string, pct int, ok bool) {
	job := nasDL.job
	if job == nil {
		return "", 0, false
	}
	pct, _ = nasDownloadPercent(job.path)
	return job.title, pct, true
}

// nasGameOnCard reports whether a network game's copy is on the card now
// (its file, or its folder for a folder game). A stat of the card.
func nasGameOnCard(ref nasLibRef, path string) bool {
	if ref.Game.Dir {
		return dirExists(path)
	}
	return regularFile(path)
}

// nasPartPath is where a file is written until it is complete.
func nasPartPath(local string) string {
	return filepath.Join(filepath.Dir(local), "."+filepath.Base(local)+".part")
}

// nasDownloadPlan lists a game's files, the game's own file last. For a
// folder game the files go below its ".part" folder; finalDir is the folder
// it becomes. Off the render thread.
func nasDownloadPlan(ctx context.Context, fs nasFS, src nasSource, ref nasLibRef, romsRoot string) (files []nasDLFile, tempDir, finalDir string, err error) {
	console := src.Root
	if ref.Game.Folder != "." {
		console = nasJoin(src.Root, ref.Game.Folder)
	}
	if ref.Game.Dir {
		finalDir, err = nasLocalPath(romsRoot, ref.Folder, ref.Game.Rel)
		if err != nil {
			return nil, "", "", err
		}
		if _, err := os.Lstat(finalDir); err == nil {
			return nil, "", "", errors.New("a folder called " + filepath.Base(finalDir) + " is on the card already")
		}
		tempDir = filepath.Join(filepath.Dir(finalDir), "."+filepath.Base(finalDir)+".part")
		remoteDir := nasJoin(console, ref.Game.Rel)
		found, _, err := nasWalkFolder(ctx, fs, remoteDir, nil)
		if err != nil {
			return nil, "", "", err
		}
		for _, f := range found {
			if !nasSafeRel(f.rel) {
				continue
			}
			local := filepath.Join(tempDir, filepath.FromSlash(f.rel))
			if !isPathWithin(local, tempDir) {
				continue
			}
			files = append(files, nasDLFile{remote: nasJoin(remoteDir, f.rel), local: local, size: f.size})
		}
		if len(files) == 0 {
			return nil, "", "", errors.New("the game's folder on the drive is empty")
		}
		return files, tempDir, finalDir, nil
	}
	for _, rel := range append(append([]string(nil), ref.Game.Parts...), ref.Game.Rel) {
		local, err := nasLocalPath(romsRoot, ref.Folder, rel)
		if err != nil {
			return nil, "", "", fmt.Errorf("%s: %w", rel, err)
		}
		remote := nasJoin(console, rel)
		st, err := fs.Stat(ctx, remote)
		if err != nil {
			if errors.Is(err, errNasNotFound) {
				return nil, "", "", errors.New(rel + " is not on the drive any more — refresh the list on the Network drives page")
			}
			return nil, "", "", err
		}
		files = append(files, nasDLFile{remote: remote, local: local, size: st.Size})
	}
	return files, "", "", nil
}

// nasHaveBytes is how much of the plan is on the card already (whole files
// and .part files), for the free-space check and the resume line.
func nasHaveBytes(files []nasDLFile) int64 {
	var have int64
	for _, f := range files {
		if info, err := os.Stat(f.local); err == nil && info.Mode().IsRegular() && info.Size() == f.size {
			have += f.size
			continue
		}
		if info, err := os.Stat(nasPartPath(f.local)); err == nil && info.Mode().IsRegular() && info.Size() <= f.size {
			have += info.Size()
		}
	}
	return have
}

// nasCopyFile copies one file, continuing a .part when there is one. ours
// says a file already at f.local is one this feature wrote, so it may be
// replaced; any other file there that differs stops the copy (it is the
// player's). wrote is false when the file was whole on the card already.
func nasCopyFile(ctx context.Context, fs nasFS, f nasDLFile, ours bool, done *atomic.Int64) (wrote bool, err error) {
	if info, err := os.Lstat(f.local); err == nil {
		if info.Mode().IsRegular() && info.Size() == f.size {
			done.Add(f.size)
			return false, nil
		}
		if !ours || !info.Mode().IsRegular() {
			return false, errors.New("a different " + filepath.Base(f.local) + " is on the card already — move or rename it, then try again")
		}
	}
	if err := os.MkdirAll(filepath.Dir(f.local), 0o755); err != nil {
		return false, err
	}
	part := nasPartPath(f.local)
	var off int64
	if info, err := os.Stat(part); err == nil && info.Mode().IsRegular() && info.Size() <= f.size {
		off = info.Size()
	}
	if off > 0 && off == f.size {
		// Whole already: the last try stopped between its fsync and its
		// rename.
		done.Add(off)
		return true, os.Rename(part, f.local)
	}
	rc, size, err := fs.ReadFrom(ctx, f.remote, off)
	if err != nil && off > 0 && errors.Is(err, errNasPartTooBig) {
		// The file changed on the drive: from the start. Any other failure
		// (a Wi-Fi blip) keeps the .part for the next try.
		off = 0
		rc, size, err = fs.ReadFrom(ctx, f.remote, 0)
	}
	if err != nil {
		return false, err
	}
	defer rc.Close()
	if size < 0 {
		size = f.size
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if off == 0 {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := os.OpenFile(part, flag, 0o644)
	if err != nil {
		return false, err
	}
	// A stopped copy keeps what reached the card: its size is where the next
	// try continues, so it is flushed first.
	stopped := func(e error) (bool, error) {
		_ = out.Sync()
		_ = out.Close()
		return false, e
	}
	done.Add(off)
	written := off
	buf := make([]byte, nasCopyBuffer)
	for {
		if err := ctx.Err(); err != nil {
			return stopped(err)
		}
		n, rerr := rc.Read(buf)
		if n > 0 {
			w, werr := out.Write(buf[:n])
			written += int64(w)
			done.Add(int64(w))
			if werr != nil {
				return stopped(werr)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return stopped(rerr)
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	if size > 0 && written != size {
		return false, fmt.Errorf("the copy of %s stopped early (%s of %s)", filepath.Base(f.local), formatSize(written), formatSize(size))
	}
	if err := os.Rename(part, f.local); err != nil {
		return false, err
	}
	return true, nil
}

// nasDownloadRun copies the game of job. Off the render thread.
func nasDownloadRun(ctx context.Context, job *nasDLJob) (nasRecord, error) {
	src, ok := nasSourceByID(job.ref.Source)
	if !ok {
		return nasRecord{}, errors.New("that network drive was removed")
	}
	job.phase.Store("Connecting to " + nasDisplayName(src) + "…")
	fs, err := nasDial(ctx, src)
	if err != nil {
		if ctx.Err() != nil {
			return nasRecord{}, context.Cause(ctx)
		}
		nasReachSet(src.ID, false, nasErrorText(src, err))
		return nasRecord{}, errors.New(nasErrorText(src, err))
	}
	stop := context.AfterFunc(ctx, func() { fs.Close() })
	defer func() {
		stop()
		fs.Close()
	}()
	nasReachSet(src.ID, true, "")
	romsRoot := nasRomsRoot()
	job.phase.Store("Looking at the game's files…")
	files, tempDir, finalDir, err := nasDownloadPlan(ctx, fs, src, job.ref, romsRoot)
	if err != nil {
		if ctx.Err() != nil {
			return nasRecord{}, context.Cause(ctx)
		}
		return nasRecord{}, err
	}
	// The game's own file (or folder) must land where the library shows it.
	want := finalDir
	if tempDir == "" && len(files) > 0 {
		want = files[len(files)-1].local
	}
	if filepath.Clean(want) != filepath.Clean(job.path) {
		return nasRecord{}, errors.New("the card's games folder changed since the library was read — press Y in the library to rescan")
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	job.total.Store(total)
	have := nasHaveBytes(files)
	if free, err := filesFreeSpace(romsRoot); err == nil && free < total-have+nasFreeMargin {
		return nasRecord{}, fmt.Errorf("not enough space on the card: the game needs %s, %s is free", formatSize(total-have), formatSize(free))
	}
	base := filepath.Join(romsRoot, job.ref.Folder)
	// What an earlier, stopped copy of this game wrote is this feature's own;
	// anything else already on the card is the player's and is never
	// recorded (Remove downloaded copy deletes recorded files only). A folder
	// game is written into its own new .part folder: all of it is ours.
	ours := map[string]bool{}
	var newDirs []string
	if prev, ok := nasRecordFor(job.path); ok && prev.Partial && prev.Source == src.ID {
		for _, p := range prev.Files {
			ours[p] = true
		}
		newDirs = append(newDirs, prev.Dirs...)
	}
	if tempDir == "" {
		// The folders this copy is about to make (none that exist now).
		seenDir := map[string]bool{}
		for _, d := range newDirs {
			seenDir[d] = true
		}
		for _, f := range files {
			for d := filepath.Dir(f.local); d != base && isPathWithin(d, base); d = filepath.Dir(d) {
				if seenDir[d] {
					continue
				}
				seenDir[d] = true
				if _, err := os.Lstat(d); err != nil {
					newDirs = append(newDirs, d)
				}
			}
		}
	}
	job.phase.Store("")
	var made []string
	for _, f := range files {
		before := job.done.Load()
		mine := tempDir != "" || ours[f.local]
		wrote, err := nasCopyFile(ctx, fs, f, mine, &job.done)
		if err != nil {
			if ctx.Err() != nil {
				return nasRecord{}, context.Cause(ctx)
			}
			return nasRecord{}, errors.New(nasErrorText(src, err))
		}
		// A file that was there already counted itself once; keep the bar honest.
		if job.done.Load() > before+f.size {
			job.done.Store(before + f.size)
		}
		if !wrote && !mine {
			continue // the player's own copy of this file: used, never recorded
		}
		made = append(made, f.local)
		if wrote && tempDir == "" {
			// Kept after every file, so a stopped copy's files stay known.
			nasRecordPut(nasRecord{Path: job.path, Source: src.ID, SourceName: nasDisplayName(src), Base: base,
				Files: append([]string(nil), made...), Dirs: newDirs, Size: total, Partial: true})
		}
	}
	rec := nasRecord{Path: job.path, Source: src.ID, SourceName: nasDisplayName(src), Size: total}
	if tempDir != "" {
		if err := os.Rename(tempDir, finalDir); err != nil {
			return nasRecord{}, err
		}
		rec.Base = finalDir
		seenDir := map[string]bool{}
		for _, p := range made {
			rel, err := filepath.Rel(tempDir, p)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
			dst := filepath.Join(finalDir, rel)
			rec.Files = append(rec.Files, dst)
			for d := filepath.Dir(dst); d != finalDir && isPathWithin(d, finalDir); d = filepath.Dir(d) {
				if !seenDir[d] {
					seenDir[d] = true
					rec.Dirs = append(rec.Dirs, d)
				}
			}
		}
		rec.Dirs = append(rec.Dirs, finalDir)
	} else {
		rec.Base = base
		rec.Files = made
		rec.Dirs = newDirs
	}
	nasRecordPut(rec)
	logInfo("nas", "copied %s from %s (%s, %d files)", job.title, rec.SourceName, formatSize(total), len(rec.Files))
	return rec, nil
}

// nasDownloadStart starts copying a network game (main thread). play starts
// it once it is on the card, if its page is still open.
func nasDownloadStart(game libraryGame, ref nasLibRef, play bool) {
	if job := nasDL.job; job != nil {
		if job.path == game.Path {
			job.play = job.play || play
			return
		}
		ShowToast(job.title+" is still downloading — one game at a time", ToastKindInfo)
		return
	}
	ctx, cancelCause := context.WithCancelCause(context.Background())
	cancel := func() { cancelCause(nil) } // the player's Cancel: context.Canceled
	job := &nasDLJob{path: game.Path, title: game.Label(), ref: ref, play: play && !ref.Game.Dir, cancel: cancel}
	job.total.Store(ref.Game.Size)
	job.phase.Store("Starting…")
	nasDL.job = job
	stopTick := make(chan struct{})
	go func() {
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		last, lastAt := int64(-1), time.Now()
		for {
			select {
			case <-t.C:
				tbxPost(nasPageRefreshProgress)
				// WebDAV bodies and SFTP reads have no deadline of their
				// own: a copy that moved no byte for nasStallAfter is on a
				// dead connection and stops (resumable) instead of hanging.
				if phase, _ := job.phase.Load().(string); phase != "" {
					last, lastAt = -1, time.Now()
					continue
				}
				if n := job.done.Load(); n != last {
					last, lastAt = n, time.Now()
				} else if time.Since(lastAt) > nasStallAfter {
					cancelCause(errNasStalled)
				}
			case <-stopTick:
				return
			}
		}
	}()
	go func() {
		var rec nasRecord
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the download stopped unexpectedly: %v", r)
			}
			close(stopTick)
			tbxPost(func() { nasDownloadFinished(job, rec, err) })
		}()
		rec, err = nasDownloadRun(ctx, job)
	}()
}

// nasDownloadCancel stops the running download; its .part files stay.
func nasDownloadCancel() {
	if job := nasDL.job; job != nil && job.cancel != nil {
		job.cancel()
	}
}

// nasDownloadFinished ends a download (main thread).
func nasDownloadFinished(job *nasDLJob, rec nasRecord, err error) {
	if nasDL.job == job {
		nasDL.job = nil
	}
	job.cancel()
	token := nasGameToken(job.path)
	pageHere := nasPage.open && nasPage.token == token
	switch {
	case err == nil:
		refreshGameLibrary()
		if job.play && pageHere && gmetaUI.open && gmetaUI.game.Path == job.path {
			tbxCloseSheet()
			nasPage.open = false
			gmetaPlay(nil)
			return
		}
		msg := job.title + " is on the card — ready to play"
		if job.ref.Game.Dir {
			msg = job.title + " is on the card — find it on the " + job.ref.Game.Sys + " shelf"
		}
		ShowToast(msg, ToastKindSuccess)
		if pageHere {
			buttons := []tbxSheetButton{{label: "Close"}}
			if !job.ref.Game.Dir {
				buttons = append(buttons, tbxSheetButton{label: "Play", primary: true, run: func() {
					if gmetaUI.open && gmetaUI.game.Path == job.path {
						gmetaPlay(nil)
					}
				}})
			}
			tbxUpdateSheet(token, msg+".\n\nIt stays on the card until you remove it (More → Remove downloaded copy); saves stay either way.", buttons)
		}
	case errors.Is(err, context.Canceled):
		ShowToast("Download stopped — it continues from there next time", ToastKindInfo)
		if pageHere {
			tbxCloseSheet()
			nasPageSync()
		}
	default:
		text := firstLine(err.Error())
		ShowToast("Download failed: "+text, ToastKindWarning)
		if pageHere {
			game := nasPage.game
			tbxUpdateSheet(token, "The download stopped: "+text+"\n\nWhat was copied stays; trying again continues from there.", []tbxSheetButton{
				{label: "Close"},
				{label: "Try again", primary: true, keepOpen: true, run: func() { nasDownloadStart(game, job.ref, job.play); nasPageRefreshProgress() }},
			})
		}
	}
}

// ── The sheet over the game's page ───────────────────────────────────────────

// nasPage is the sheet this file has open over a game's page.
var nasPage struct {
	open  bool
	token string
	game  libraryGame
}

func nasGameToken(path string) string { return "nas-game:" + path }

func init() {
	gamePageRegisterOpened(nasPageOpened)
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "nas",
		IsOpen: func() bool { return nasPage.open },
		Key: func(e *sdl.KeyboardEvent) bool {
			if act := tbxKeyAction(e); act != tbxActNone {
				tbxSheetAction(act)
				nasPageSync()
			}
			return true
		},
		Pad: func(e *sdl.ControllerButtonEvent) bool {
			if act := tbxPadAction(e); act != tbxActNone {
				tbxSheetAction(act)
				nasPageSync()
			}
			return true
		},
		Mouse: func(mx, my int32) bool {
			tbxSheetMouse(mx, my)
			nasPageSync()
			return true
		},
		Render: func(renderer *sdl.Renderer, config *Config) {
			nasPageSync()
			if nasPage.open && tbxSheetVisible(config) {
				// The sheet's own keys in the footer, under its scrim, the
				// way the tool screens draw theirs (peerEndFrame).
				_ = renderer.SetClipRect(nil)
				peerDrawFooter(renderer, config, tbxSheetHints())
				tbxRenderSheet(renderer, config)
			}
		},
		Close: func() {
			nasPage.open = false
			if tbxSheet.open && strings.HasPrefix(tbxSheet.spec.token, "nas-") {
				tbxCloseSheet()
			}
		},
	})
	gamePageRegisterStatus(gamePageStatus{ID: "nas", Order: 15, Chip: nasPageChip})
	gamePageRegisterExtra(gamePageExtra{ID: "nas-remove", Order: 85, Icon: "network", Row: nasRemoveRow, Open: nasRemoveAsk})
}

// nasLaunchMissingError is launchLibraryGame's answer for a game that is not
// on the card because it is on a network drive (nil for any other missing
// file): where it is and how to get it, instead of "ROM is missing". The
// library's own A opens the game's page, which offers the download.
func nasLaunchMissingError(game libraryGame) error {
	ref, ok := nasLibraryRef(game.Path)
	if !ok {
		return nil
	}
	return errors.New("it is on " + ref.Name + ", not on the card yet — open it in the Games library to copy it to the card")
}

// nasPageSync notices the sheet closing (a button, B, a click outside). A
// game that is still not on the card cannot be played from its page, so the
// page closes with the sheet.
func nasPageSync() {
	if !nasPage.open {
		return
	}
	if tbxSheet.open && tbxSheet.spec.token == nasPage.token {
		return
	}
	nasPage.open = false
	game := nasPage.game
	if !gmetaUI.open || gmetaUI.game.Path != game.Path {
		return
	}
	if ref, ok := nasLibraryRef(game.Path); ok && !nasGameOnCard(ref, game.Path) {
		gmetaCloseDetails(false)
	}
}

// nasPageOpened is the game page opening: a network game that is not on the
// card gets the download sheet.
func nasPageOpened(game libraryGame) {
	nasRecordsPreload()
	ref, ok := nasLibraryRef(game.Path)
	if !ok || nasGameOnCard(ref, game.Path) {
		return
	}
	nasOpenGameSheet(game, ref)
}

// nasOpenGameSheet opens the download sheet for a network game.
func nasOpenGameSheet(game libraryGame, ref nasLibRef) {
	token := nasGameToken(game.Path)
	nasPage.open, nasPage.token, nasPage.game = true, token, game
	spec := tbxSheetSpec{token: token, title: game.Label(), subtitle: "On " + ref.Name, icon: "network"}
	if job := nasDL.job; job != nil && job.path == game.Path {
		spec.body = nasProgressBody(job)
		spec.buttons = nasProgressButtons()
		spec.focus = 1
		tbxOpenSheet(appConfig, spec)
		return
	}
	if nasLibraryOffline(ref) {
		nasOpenOfflineSheet(game, ref)
		return
	}
	spec.body = "Checking the card…"
	spec.buttons = []tbxSheetButton{{label: "Cancel"}}
	tbxOpenSheet(appConfig, spec)
	src, _ := nasSourceByID(ref.Source)
	go func() {
		romsRoot := nasRomsRoot()
		free, freeErr := filesFreeSpace(romsRoot)
		size := ref.Game.Size
		var have int64
		if !ref.Game.Dir {
			var files []nasDLFile
			for _, rel := range append(append([]string(nil), ref.Game.Parts...), ref.Game.Rel) {
				if local, err := nasLocalPath(romsRoot, ref.Folder, rel); err == nil {
					files = append(files, nasDLFile{local: local, size: 1 << 62})
				}
			}
			have = nasPartialBytes(files)
		}
		tbxPost(func() {
			if !nasPage.open || nasPage.token != token {
				return
			}
			body := "This game is on " + ref.Name + " (" + nasWhere(src) + "). It is copied to the card before it plays"
			if size > 0 {
				body += ": " + formatSize(size)
			}
			if freeErr == nil {
				body += ", " + formatSize(free) + " free on the card"
			}
			body += "."
			if have > 0 {
				body += "\n\n" + formatSize(have) + " of it was copied before; the copy continues from there."
			}
			if freeErr == nil && size > 0 && free < size-have+nasFreeMargin {
				body += "\n\nThere is not enough space on the card for it — remove a game or two first."
			}
			body += "\n\nThe copy stays on the card until you remove it (More → Remove downloaded copy). Saves stay either way."
			only := tbxSheetButton{label: "Download only", keepOpen: true, run: func() { nasDownloadStart(game, ref, false); nasPageRefreshProgress() }}
			if ref.Game.Dir {
				// A folder game starts from the file the card's scan finds in
				// it, so it is copied, then played from its shelf.
				only.label, only.primary = "Download to card", true
			}
			buttons := []tbxSheetButton{{label: "Cancel"}, only}
			if !ref.Game.Dir {
				label := "Download and play"
				if size > 0 {
					label = "Download (" + formatSize(size) + ") and play"
				}
				buttons = append(buttons, tbxSheetButton{label: label, primary: true, keepOpen: true, run: func() { nasDownloadStart(game, ref, true); nasPageRefreshProgress() }})
			}
			tbxUpdateSheet(token, body, buttons)
		})
	}()
}

// nasPartialBytes is how much of a game's files is on the card already as
// whole files or .part files (sizes unknown: any whole file counts).
func nasPartialBytes(files []nasDLFile) int64 {
	var have int64
	for _, f := range files {
		if info, err := os.Stat(f.local); err == nil && info.Mode().IsRegular() {
			have += info.Size()
			continue
		}
		if info, err := os.Stat(nasPartPath(f.local)); err == nil && info.Mode().IsRegular() {
			have += info.Size()
		}
	}
	return have
}

// nasOpenOfflineSheet says the drive does not answer, and offers to ask again.
func nasOpenOfflineSheet(game libraryGame, ref nasLibRef) {
	token := nasGameToken(game.Path)
	nasPage.open, nasPage.token, nasPage.game = true, token, game
	why := nasReachError(ref.Source)
	body := ref.Name + " is not reachable — connect to your home Wi-Fi.\n\nThe game shows here because JukaHub remembers what is on the drive; it can be copied to the card when the drive answers again."
	if why != "" && !strings.Contains(why, "not reachable") {
		body += "\n\nLast try: " + why
	}
	tbxOpenSheet(appConfig, tbxSheetSpec{
		token: token, title: game.Label(), subtitle: "Offline · " + ref.Name, icon: "network", body: body,
		buttons: []tbxSheetButton{
			{label: "OK"},
			{label: "Try again", primary: true, keepOpen: true, run: func() { nasRetryReach(game, ref) }},
		},
		focus: 1,
	})
}

// nasRetryReach asks the drive again and reopens the right sheet.
func nasRetryReach(game libraryGame, ref nasLibRef) {
	token := nasGameToken(game.Path)
	tbxUpdateSheet(token, "Asking "+ref.Name+"…", []tbxSheetButton{{label: "Cancel"}})
	src, ok := nasSourceByID(ref.Source)
	if !ok {
		return
	}
	go func() {
		err := nasReachProbe(src)
		nasReachSet(src.ID, err == nil, nasErrorText(src, err))
		tbxPost(func() {
			if !nasPage.open || nasPage.token != token {
				return
			}
			if err != nil {
				nasOpenOfflineSheet(game, ref)
				return
			}
			nasOpenGameSheet(game, ref)
		})
	}()
}

func nasProgressBody(job *nasDLJob) string {
	if phase, _ := job.phase.Load().(string); phase != "" {
		return phase
	}
	done, total := job.done.Load(), job.total.Load()
	if total <= 0 {
		return "Copying to the card: " + formatSize(done)
	}
	pct, _ := nasDownloadPercent(job.path)
	return "Copying to the card: " + formatSize(done) + " of " + formatSize(total) + " (" + strconv.Itoa(pct) + "%)\n\n" +
		"B keeps it going in the background; the game's row shows how far it is."
}

func nasProgressButtons() []tbxSheetButton {
	return []tbxSheetButton{
		{label: "Cancel download", run: nasDownloadCancel},
		{label: "Keep in background", primary: true},
	}
}

// nasPageRefreshProgress redraws the sheet's progress (main thread).
func nasPageRefreshProgress() {
	job := nasDL.job
	if job == nil || !nasPage.open || nasPage.token != nasGameToken(job.path) || !tbxSheet.open {
		return
	}
	if tbxSheet.spec.token != nasPage.token {
		return
	}
	if len(tbxSheet.spec.buttons) == 2 && tbxSheet.spec.buttons[0].label == "Cancel download" {
		tbxSheet.spec.body = nasProgressBody(job)
		return
	}
	tbxUpdateSheet(nasPage.token, nasProgressBody(job), nasProgressButtons())
}

// nasPageChip is the page's chip for a network game or a copy of one.
func nasPageChip(game libraryGame) (string, int, bool) {
	if ref, ok := nasLibraryRef(game.Path); ok {
		if _, ok := nasDownloadPercent(game.Path); ok {
			return "Downloading", gamePageToneNeutral, true
		}
		if nasLibraryOffline(ref) {
			return "Offline · " + ref.Name, gamePageToneWarn, true
		}
		return "On " + ref.Name, gamePageToneNeutral, true
	}
	if rec, ok := nasRecordLoaded(game.Path); ok && !rec.Partial {
		return "From " + rec.SourceName, gamePageToneGood, true
	}
	return "", 0, false
}

// ── Remove downloaded copy ───────────────────────────────────────────────────

func nasRemoveRow(game libraryGame) (string, string, bool) {
	rec, ok := nasRecordLoaded(game.Path)
	if !ok || rec.Partial {
		return "", "", false
	}
	return "Remove downloaded copy", "Frees " + formatSize(rec.Size) + " · saves stay · it stays on " + rec.SourceName, true
}

// nasRemoveAsk asks before removing a copied game's files.
func nasRemoveAsk(game libraryGame) {
	rec, ok := nasRecordLoaded(game.Path)
	if !ok || rec.Partial {
		return
	}
	token := "nas-remove:" + game.Path
	nasPage.open, nasPage.token, nasPage.game = true, token, game
	tbxOpenSheet(appConfig, tbxSheetSpec{
		token:    token,
		title:    "Remove downloaded copy?",
		subtitle: game.Label(),
		icon:     "network",
		body: "The files copied from " + rec.SourceName + " leave the card (" + formatSize(rec.Size) + "). Saves, save states and " +
			"anything the game wrote stay, and the game stays in the library on " + rec.SourceName + " — download it again any time.",
		buttons: []tbxSheetButton{
			{label: "Remove", run: func() { nasRemoveCopy(game, rec) }},
			{label: "Keep it", primary: true},
		},
		focus: 1,
	})
}

// nasRemoveCopy deletes exactly the recorded files (regular files inside the
// recorded console folder), then the folders the copy made, deepest first,
// each only when it is empty.
func nasRemoveCopy(game libraryGame, rec nasRecord) {
	go func() {
		removed, failed := 0, 0
		romsRoot := nasRomsRoot()
		for _, p := range rec.Files {
			if !isPathWithin(p, rec.Base) || !isPathWithin(p, romsRoot) {
				failed++
				continue
			}
			info, err := os.Lstat(p)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := os.Remove(p); err != nil {
				logWarn("nas", "remove copy: %s: %v", p, err)
				failed++
				continue
			}
			removed++
		}
		dirs := append([]string(nil), rec.Dirs...)
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			if !isPathWithin(d, romsRoot) || filepath.Clean(d) == filepath.Clean(romsRoot) {
				continue
			}
			// os.Remove refuses a folder with anything in it (a save the
			// game wrote, a file the player added).
			if info, err := os.Lstat(d); err == nil && info.IsDir() {
				_ = os.Remove(d)
			}
		}
		gone := !nasPathExists(rec.Path)
		if gone {
			nasRecordDrop(rec.Path)
			recentGamesRepath(rec.Path, "", "")
			homeConsoleRecentsRepath(rec.Path, "")
		}
		logInfo("nas", "removed the copy of %s: %d files, %d failed", game.Label(), removed, failed)
		tbxPost(func() {
			if gmetaUI.open && gmetaUI.game.Path == game.Path {
				gmetaCloseDetails(false)
			}
			refreshGameLibrary()
			gameLib.recents = recentGames()
			switch {
			case !gone:
				ShowToast("Not removed — the game's file could not be deleted", ToastKindWarning)
			case failed > 0:
				ShowToast("Removed "+game.Label()+" · "+strconv.Itoa(failed)+" files stayed", ToastKindWarning)
			default:
				ShowToast("Removed the copy of "+game.Label()+" · it is still on "+rec.SourceName, ToastKindSuccess)
			}
		})
	}()
}

func nasPathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
