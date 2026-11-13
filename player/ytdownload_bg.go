package main

// ytdownload_bg.go — a video that has to be downloaded before it plays, sent
// to the background so the handheld is free while it downloads.
//
// When a stream will not play, ytdownload.go fetches the whole file first and
// the loading card stays up for all of it: on the handheld's Wi-Fi that can be
// minutes of staring at a progress bar with nothing else to do but B, which
// throws the download away. Juka asked for the downloads to run in the
// background "so you can do other stuff while you're waiting".
//
// While the card shows a download, Y (or a tap on its pill) sends it to the
// background: the card closes, the pad and the screens are the user's again,
// and the download keeps going with a chip in the activity strip (activity.go)
// that shows its progress and whose X stops it. When the file is whole it is
// filed in Media → Downloads (so it plays offline and survives a relaunch), a
// toast says it is ready, and the chip turns into "Ready" — a tap plays it from
// where the user pressed Play. Nothing starts playing by itself: the user is
// busy elsewhere by definition, and a video taking over the screen (and the
// audio) in the middle of a game or a chat would be the opposite of the point.
//
// How it hangs together: ytDownloadAndPlay no longer runs under the prepare's
// context itself. It runs under its own, which the prepare's cancel (B, a newer
// load) still ends — unless the download was sent here first, in which case
// only the chip's X ends it. The download's state moves from ytdlNow (the one
// the card draws) into ytdlBg by its prepare sequence, so every ytdlUpdate the
// download makes lands on the chip instead.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// ytdlBgMaxRunning bounds the downloads running in the background at once.
	// Each is a yt-dlp (and an ffmpeg joining the parts) on a 1 GB device that
	// may be streaming something else meanwhile; a third would starve the UI.
	ytdlBgMaxRunning = 2
	// ytdlBgReadyFor is how long the "Ready" chip stays up. The video stays in
	// Media → Downloads for good; only the shortcut goes.
	ytdlBgReadyFor = 15 * time.Minute
	// ytdlBgButton and ytdlBgLabel are the card's second button.
	ytdlBgButton = "Y"
	ytdlBgLabel  = "Download in background"
	// ytdlBgLabelShort is the label when the long one does not fit the card.
	ytdlBgLabelShort = "Background"
)

// ytdlBgJob is one download running (or finished) in the background.
type ytdlBgJob struct {
	seq      uint64 // the prepare it came from; ytdlUpdate finds it by this
	page     string
	title    string
	startSec float64 // where the user pressed Play; the ready video resumes here
	st       ytdlState
	cancel   context.CancelFunc
	filing   bool // whole, being moved into Downloads: too late to stop
	ready    bool
	path     string // the finished file, once ready
	temp     bool   // path is still the temp download (filing it failed)
	readyAt  time.Time
}

var ytdlBg = struct {
	mu   sync.Mutex
	jobs []*ytdlBgJob // oldest first
}{}

// ytdlBgFindLocked returns the job of prepare seq. Callers hold ytdlBg.mu.
func ytdlBgFindLocked(seq uint64) *ytdlBgJob {
	for _, j := range ytdlBg.jobs {
		if j.seq == seq {
			return j
		}
	}
	return nil
}

// ytdlBgHas reports whether the download of prepare seq was sent to the
// background.
func ytdlBgHas(seq uint64) bool {
	ytdlBg.mu.Lock()
	defer ytdlBg.mu.Unlock()
	return ytdlBgFindLocked(seq) != nil
}

// ytdlBgUpdate applies a progress change to a background job; it reports
// whether seq is one.
func ytdlBgUpdate(seq uint64, change func(*ytdlState)) bool {
	ytdlBg.mu.Lock()
	defer ytdlBg.mu.Unlock()
	j := ytdlBgFindLocked(seq)
	if j == nil || j.ready {
		return j != nil
	}
	change(&j.st)
	return true
}

// ytdlBgRunningLocked counts the jobs still downloading.
func ytdlBgRunningLocked() int {
	n := 0
	for _, j := range ytdlBg.jobs {
		if !j.ready {
			n++
		}
	}
	return n
}

// ytdlBgActive reports whether a background download is running; it keeps
// the handheld from auto-sleeping (devpower.go), which the modal card did
// before the download left it.
func ytdlBgActive() bool {
	ytdlBg.mu.Lock()
	defer ytdlBg.mu.Unlock()
	return ytdlBgRunningLocked() > 0
}

// ytdlBgRunningFor reports whether page is downloading in the background, and
// how far along it is (-1 when unknown).
func ytdlBgRunningFor(page string) (float64, bool) {
	page = strings.TrimSpace(page)
	if page == "" {
		return 0, false
	}
	// The library's key, so youtu.be and watch?v= links to one video match.
	key := dlLibraryKey(page)
	ytdlBg.mu.Lock()
	defer ytdlBg.mu.Unlock()
	for _, j := range ytdlBg.jobs {
		if !j.ready && (j.page == page || (key != "" && dlLibraryKey(j.page) == key)) {
			if j.st.stage == ytdlStageTransfer {
				return j.st.fraction, true
			}
			return -1, true
		}
	}
	return 0, false
}

// ytdlBgStillDownloadingToast is what Play says for a video that is already
// downloading in the background.
func ytdlBgStillDownloadingToast(frac float64) {
	msg := "Still downloading in the background — it will be in Media → Downloads"
	if frac >= 0 {
		pct := int(frac * 100)
		if pct > 99 {
			pct = 99
		}
		msg = fmt.Sprintf("Still downloading in the background (%d%%) — it will be in Media → Downloads", pct)
	}
	ShowToast(msg, ToastKindInfo)
}

// ytdlBgOffered reports whether the loading card offers the background button:
// a download is on the card and has not reached the player yet.
func ytdlBgOffered() bool {
	st, ok := ytdlSnapshot()
	if !ok || st.stage == ytdlStageOpening {
		return false
	}
	// Not once the download has finished trying (ytdlClaim took its cancel).
	ytdlNow.mu.Lock()
	claimed := ytdlNow.cancel == nil
	ytdlNow.mu.Unlock()
	if claimed {
		return false
	}
	// Not offered when it would be refused.
	ytdlBg.mu.Lock()
	defer ytdlBg.mu.Unlock()
	return ytdlBgRunningLocked() < ytdlBgMaxRunning
}

// ytdlSendToBackground moves the download on the loading card to the
// background (Y, or a tap on the card's pill). It reports whether it did.
// Main thread.
func ytdlSendToBackground() bool {
	st, ok := ytdlSnapshot()
	if !ok {
		return false
	}
	_, title := videoPrepareStatus()
	refused, page := "", ""
	// A quality switch titles its prepare with the URL; the recent list has
	// the video's real name.
	if t := recentVideoTitle(ytdlNowPage()); t != "" {
		title = t
	}
	moved := false
	ytdlNow.mu.Lock()
	switch {
	case !ytdlNow.on || ytdlNow.st.seq != st.seq || ytdlNow.cancel == nil:
		// The download ended (or was replaced) between the frame and the press.
	case ytdlNow.st.stage == ytdlStageOpening:
		// The file is whole and the player is opening it: nothing to wait for.
	default:
		// Lock order: ytdlNow.mu, then ytdlBg.mu (nothing takes them the
		// other way round). Moving the state while ytdlNow is held means the
		// download's next update, and its check at the end, see exactly one
		// of the two places.
		ytdlBg.mu.Lock()
		if ytdlBgRunningLocked() >= ytdlBgMaxRunning {
			refused = fmt.Sprintf("%d videos are already downloading in the background", ytdlBgMaxRunning)
		} else {
			ytdlBg.jobs = append(ytdlBg.jobs, &ytdlBgJob{
				seq:      ytdlNow.st.seq,
				page:     ytdlNow.page,
				title:    strings.TrimSpace(title),
				startSec: ytdlNow.startSec,
				st:       ytdlNow.st,
				cancel:   ytdlNow.cancel,
			})
			ytdlNow.on = false
			moved, page = true, ytdlNow.page
		}
		ytdlBg.mu.Unlock()
	}
	ytdlNow.mu.Unlock()
	if refused != "" {
		PlayErrorSound()
		ShowToast(refused, ToastKindWarning)
		return false
	}
	if !moved {
		return false
	}
	// The card goes and the pad is free. This cancels the prepare's context,
	// which the download's own context ignores now that the job is here.
	cancelVideoPrepareSeq(st.seq)
	PlayActivateSound()
	logInfo("youtube", "download of %s continues in the background", sanitizeLogURL(page))
	ShowToast("Downloading in the background — you'll get a notification when it's ready", ToastKindInfo)
	return true
}

// ytdlNowPage is the page of the download on the card.
func ytdlNowPage() string {
	ytdlNow.mu.Lock()
	defer ytdlNow.mu.Unlock()
	return ytdlNow.page
}

// ytdlBgCancel stops a background download (the chip's X).
func ytdlBgCancel(seq uint64) {
	ytdlBg.mu.Lock()
	j := ytdlBgFindLocked(seq)
	var cancel context.CancelFunc
	if j != nil && !j.ready && !j.filing {
		cancel = j.cancel
	}
	ytdlBg.mu.Unlock()
	if cancel != nil {
		PlayBackSound()
		cancel()
	}
}

// ytdlBgDismiss forgets a ready job (its chip's X, or its time ran out). A
// file that never made it into Downloads is the temp download, which nothing
// else would delete before the next boot.
func ytdlBgDismiss(seq uint64) {
	ytdlBg.mu.Lock()
	j := ytdlBgFindLocked(seq)
	path := ""
	if j != nil && j.ready && j.temp {
		path = j.path
	}
	ytdlBg.mu.Unlock()
	ytdlBgRemove(seq)
	if path != "" {
		removeTempVideoArtifacts(path)
	}
}

// ytdlBgShutdown stops every background download on the way out and gives
// them a moment to kill their helpers and delete their partial files; the
// next boot's sweep (sweepTempVideos) takes whatever is left.
func ytdlBgShutdown() {
	ytdlBg.mu.Lock()
	var cancels []context.CancelFunc
	for _, j := range ytdlBg.jobs {
		if !j.ready && j.cancel != nil {
			cancels = append(cancels, j.cancel)
		}
	}
	ytdlBg.mu.Unlock()
	if len(cancels) == 0 {
		return
	}
	for _, cancel := range cancels {
		cancel()
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		ytdlBg.mu.Lock()
		running := ytdlBgRunningLocked()
		ytdlBg.mu.Unlock()
		if running == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ytdlBgRemove forgets a job.
func ytdlBgRemove(seq uint64) {
	ytdlBg.mu.Lock()
	for i, j := range ytdlBg.jobs {
		if j.seq == seq {
			ytdlBg.jobs = append(ytdlBg.jobs[:i], ytdlBg.jobs[i+1:]...)
			break
		}
	}
	ytdlBg.mu.Unlock()
}

// ytdlBgSettle ends a background download. path is the finished temp file, or
// "" when it failed (failMsg says why) or was stopped (cancelled). It runs on
// the download's goroutine.
func ytdlBgSettle(seq uint64, path, failMsg string, cancelled bool) {
	ytdlBg.mu.Lock()
	j := ytdlBgFindLocked(seq)
	var title, page string
	if j != nil {
		title, page = j.title, j.page
	}
	ytdlBg.mu.Unlock()
	if j == nil {
		if path != "" {
			removeTempVideoArtifacts(path)
		}
		return
	}
	name := dlShortTitle(dlFirstNonEmpty(title, "The video"), 48)
	if cancelled || path == "" {
		ytdlBgRemove(seq)
		if path != "" {
			removeTempVideoArtifacts(path)
		}
		if !cancelled {
			if failMsg == "" {
				failMsg = "Couldn't download this video"
			}
			ShowToast(name+": "+failMsg, ToastKindError)
		}
		return
	}

	ytdlBg.mu.Lock()
	if j := ytdlBgFindLocked(seq); j != nil {
		j.filing = true
	}
	ytdlBg.mu.Unlock()
	meta := dlWatchedMeta(page)
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = title
	}
	final, err := ytdlBgFile(path, page, meta)
	temp := false
	if err != nil {
		// It is downloaded and plays from where it is; it just is not in the
		// library, and the next boot's sweep removes it.
		logWarn("youtube", "background download of %s not filed in Downloads: %v", sanitizeLogURL(page), err)
		final, temp = path, true
	}
	ytdlBg.mu.Lock()
	if j := ytdlBgFindLocked(seq); j != nil {
		j.ready, j.filing, j.path, j.temp, j.readyAt = true, false, final, temp, time.Now()
		j.st.stage = ytdlStageOpening
		j.st.fraction = 1
	}
	ytdlBg.mu.Unlock()
	PlaySuccessSound()
	if temp {
		ShowToast("Ready to watch: "+name+" — play it from its chip at the top right", ToastKindSuccess)
	} else {
		ShowToast("Ready to watch: "+name+" — it's in Media → Downloads", ToastKindSuccess)
	}
}

// ytdlBgFile files a finished background download in the Downloads library,
// the way a finished queue download is filed (dlLibraryNoteSaved), and returns
// where the file is now. A video the library already holds keeps that copy.
func ytdlBgFile(tempPath, pageURL string, meta VideoInfo) (string, error) {
	dlLibraryLoadNow()
	if e, ok := dlLibraryFindSource(pageURL, true); ok {
		if _, err := os.Stat(e.Path); err == nil {
			removeTempVideoArtifacts(tempPath)
			return e.Path, nil
		}
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(downloadsVideoDir(), 0o755); err != nil {
		return "", err
	}
	title := strings.TrimSpace(meta.Title)
	name := safeDownloadName(title)
	if name == "" {
		name = "Video " + time.Now().Format("2006-01-02 15.04")
	}
	ext := strings.ToLower(filepath.Ext(tempPath))
	if ext == "" {
		ext = ".mp4"
	}
	dest := uniqueDownloadPath(downloadsVideoDir(), name, ext)
	if err := dlMoveFile(tempPath, dest, info.Size()); err != nil {
		if errors.Is(err, errDlCardFull) {
			ShowToast("Not saved to Downloads: the card is almost full", ToastKindWarning)
		}
		return "", err
	}
	entry := dlLibraryEntry{
		ID:        dlNewID(),
		Path:      dest,
		Video:     true,
		Title:     dlFirstNonEmpty(title, name),
		Channel:   dlFirstNonEmpty(meta.Uploader, meta.Channel),
		Duration:  meta.Duration,
		Thumbnail: dlThumbURLFor(meta, pageURL),
		Source:    strings.TrimSpace(pageURL),
		Size:      info.Size(),
		Added:     time.Now(),
	}
	dlLibraryAdd(entry)
	dlKeepCachedSubtitles(pageURL, dest)
	go dlFetchPoster(entry)
	return dest, nil
}

// ytdlBgPlay plays a ready background download (the chip's body). Main thread.
func ytdlBgPlay(seq uint64) {
	ytdlBg.mu.Lock()
	j := ytdlBgFindLocked(seq)
	var job ytdlBgJob
	if j != nil {
		job = *j
	}
	ytdlBg.mu.Unlock()
	if j == nil || !job.ready {
		return
	}
	if _, err := os.Stat(job.path); err != nil {
		ytdlBgRemove(seq)
		ShowToast("That video is no longer on the card", ToastKindWarning)
		return
	}
	ytdlBgRemove(seq)
	if appConfig == nil {
		if job.temp {
			removeTempVideoArtifacts(job.path)
		}
		return
	}
	if job.temp {
		// Not in Downloads: the session owns the temp file and removes it (or
		// keeps it, dl_library.go) when playback ends, as a download that
		// played at once would have.
		musicStopIfPlaying()
		ensureVideoScene(appConfig)
		PlayActivateSound()
		setPlaybackPageURL(job.page)
		startEmbeddedPlayback(appConfig, job.path, job.startSec, true)
		return
	}
	meta := dlWatchedMeta(job.page)
	playVideoInfo(appConfig, VideoInfo{
		Title:     dlFirstNonEmpty(job.title, meta.Title, dlTitleFromFile(job.path)),
		Uploader:  dlFirstNonEmpty(meta.Uploader, meta.Channel),
		Channel:   dlFirstNonEmpty(meta.Channel, meta.Uploader),
		Duration:  meta.Duration,
		Thumbnail: meta.Thumbnail,
		URL:       job.path,
		Position:  job.startSec,
	})
}

// ytdlBgActivityItems are the strip's chips for the background downloads:
// progress with an X while one runs, "Ready" with a tap to play once it is
// done. Ready chips go after ytdlBgReadyFor.
func ytdlBgActivityItems() []activityItem {
	now := time.Now()
	var expired []string
	ytdlBg.mu.Lock()
	kept := ytdlBg.jobs[:0]
	for _, j := range ytdlBg.jobs {
		if j.ready && now.Sub(j.readyAt) > ytdlBgReadyFor {
			if j.temp {
				expired = append(expired, j.path)
			}
			continue
		}
		kept = append(kept, j)
	}
	for i := len(kept); i < len(ytdlBg.jobs); i++ {
		ytdlBg.jobs[i] = nil
	}
	ytdlBg.jobs = kept
	jobs := make([]ytdlBgJob, 0, len(kept))
	for _, j := range kept {
		jobs = append(jobs, *j)
	}
	ytdlBg.mu.Unlock()
	if len(expired) > 0 {
		go func() {
			for _, p := range expired {
				removeTempVideoArtifacts(p)
			}
		}()
	}

	items := make([]activityItem, 0, len(jobs))
	for _, j := range jobs {
		seq := j.seq
		title := dlFirstNonEmpty(j.title, "Video")
		if j.ready {
			items = append(items, activityItem{
				glyph:  "▶",
				label:  "Ready: " + title,
				frac:   1,
				tint:   ColorToastSuccess,
				cancel: func() { ytdlBgDismiss(seq) },
				open:   func() { ytdlBgPlay(seq) },
			})
			continue
		}
		frac := -1.0
		if j.st.stage == ytdlStageTransfer && j.st.fraction >= 0 {
			frac = j.st.fraction
			if frac > 1 {
				frac = 1
			}
		}
		st := j.st
		var cancel func()
		if !j.filing {
			cancel = func() { ytdlBgCancel(seq) }
		}
		items = append(items, activityItem{
			glyph:  "↓",
			label:  title,
			frac:   frac,
			tint:   installHighlight(),
			cancel: cancel,
			// The body says where the download is, in the card's words.
			open: func() { ShowToast(title+" — "+ytdlDetailLine(st), ToastKindInfo) },
		})
	}
	return items
}
