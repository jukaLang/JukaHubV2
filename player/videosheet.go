package main

// videosheet.go — the player's settings sheet: its rows, its two pages and
// what each row does.
//
// The sheet was a fixed array of six rows. It now has a second page: "Save
// offline" opens the choices for the video on screen — download a stream as an
// MP4 or keep its sound as an M4A; extract the sound of a file on the card, or
// fetch its subtitles from the page it was downloaded from — inside the same
// sheet, so the sheet never grows over the picture. One list of rows per frame
// feeds the drawing (vcDrawSettingsSheet), the taps (vcSheetRow), the D-pad
// (moveVideoSettingsItem) and the presses (videoSettingsActivate), so the four
// always agree on how many rows there are.
//
// Each page's rows are fixed for the video on screen: whether a row can do
// anything right now (a live stream, subtitles already loaded, a file whose
// page is unknown) is said in its value and again when it is pressed, so a row
// never appears or vanishes under the D-pad and the ring cannot slide onto a
// different action between two presses.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// videoSheetRowID names one row of the settings sheet.
type videoSheetRowID int

const (
	vsRowSubtitles videoSheetRowID = iota
	vsRowFullscreen
	vsRowSpeed
	vsRowQuality
	vsRowUpNext
	vsRowSave
	vsRowClose
	vsRowSaveVideo
	vsRowSaveAudio
	vsRowExtractAudio
	vsRowGetSubtitles
	vsRowBack
)

// The sheet's pages: the settings, and the Save offline choices.
const (
	videoSheetMain = iota
	videoSheetSave
)

// videoSettingsPage is the page on screen. Opening the sheet starts on the
// settings (videoSettingsToggle).
var videoSettingsPage int

// videoSheetSubs is what "Get subtitles" can do for the file on screen.
type videoSheetSubs int

const (
	// vsSubsFetchable: the page the file came from is known and nothing is
	// loaded, so a press fetches.
	vsSubsFetchable videoSheetSubs = iota
	// vsSubsLoaded: subtitles are loaded for the file already.
	vsSubsLoaded
	// vsSubsBusy: the file is being searched for subtitles of its own, or a
	// fetch is on its way.
	vsSubsBusy
	// vsSubsNoSource: the Downloads library does not know a web page for the
	// file, so there is nowhere to fetch from.
	vsSubsNoSource
	// vsSubsNone: the page is known, but this session already learned that it
	// has no subtitles in the language.
	vsSubsNone
	// vsSubsBadLanguage: the subtitle language setting is not a code.
	vsSubsBadLanguage
)

// videoSheetContext is what the rows say: whether the video is a file on the
// card, whether it is live, and — on the Save offline page of a file — what
// "Get subtitles" can do.
type videoSheetContext struct {
	local bool
	live  bool
	subs  videoSheetSubs
}

// videoSheetRowIDsFor lists a page's rows. They depend only on whether the
// video is a file on the card: every stream offers the same two downloads and
// every file the same two actions, whatever they can do at the moment.
func videoSheetRowIDsFor(page int, c videoSheetContext) []videoSheetRowID {
	if page != videoSheetSave {
		return []videoSheetRowID{vsRowSubtitles, vsRowFullscreen, vsRowSpeed, vsRowQuality, vsRowUpNext, vsRowSave, vsRowClose}
	}
	if c.local {
		return []videoSheetRowID{vsRowExtractAudio, vsRowGetSubtitles, vsRowBack}
	}
	return []videoSheetRowID{vsRowSaveVideo, vsRowSaveAudio, vsRowBack}
}

// videoSheetContextNow reads the context of the video on screen. The
// subtitle state is only looked up for the page that shows it.
func videoSheetContextNow() videoSheetContext {
	sess := playbackCurrentSession()
	c := videoSheetContext{
		local: sess != nil && sess.local,
		live:  snapshotVideoState().duration <= 0,
	}
	if c.local && videoSettingsPage == videoSheetSave {
		c.subs = videoSheetSubsFor(sess)
	}
	return c
}

// videoSheetSubsFor works out what "Get subtitles" can do for a local session.
func videoSheetSubsFor(sess *playbackSession) videoSheetSubs {
	if _, err := subtitleLanguage(appConfig); err != nil {
		return vsSubsBadLanguage
	}
	loaded, busy := videoSheetSubsState()
	switch {
	case loaded:
		return vsSubsLoaded
	case subtitlesSourceFetchable():
		return vsSubsFetchable
	case busy:
		return vsSubsBusy
	case sess == nil:
		return vsSubsNoSource
	}
	if e, ok := dlLibraryFindPath(sess.source); !ok || !isNetworkSource(e.Source) {
		return vsSubsNoSource
	}
	// The page is known, nothing is loaded or running, and a press may not
	// fetch: the session already asked and the page has none.
	return vsSubsNone
}

// videoSheetSubsState reads what videosubs.go knows about the video on
// screen: subtitles loaded, and the file's own search or a fetch running.
func videoSheetSubsState() (loaded, busy bool) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	return len(videoSubs.cues) > 0, videoSubs.fetching || videoSubs.preparing
}

// videoSettingsRowIDs is the rows of the page on screen.
func videoSettingsRowIDs() []videoSheetRowID {
	return videoSheetRowIDsFor(videoSettingsPage, videoSheetContextNow())
}

// videoSettingsToggle opens the sheet on its first page and first row, or
// closes it.
func videoSettingsToggle() {
	videoSettingsOpen = !videoSettingsOpen
	videoSettingsItem = 0
	videoSettingsPage = videoSheetMain
}

// videoSettingsBack steps from the Save offline page back to the settings, on
// the row that opened it; on the settings page it closes the sheet.
func videoSettingsBack() {
	if videoSettingsPage == videoSheetMain {
		videoSettingsOpen = false
		return
	}
	videoSettingsPage = videoSheetMain
	videoSettingsItem = videoSheetRowIndex(videoSheetRowIDsFor(videoSheetMain, videoSheetContext{}), vsRowSave)
}

// videoSheetRowIndex is a row's position in a list, 0 when it is not there.
func videoSheetRowIndex(rows []videoSheetRowID, id videoSheetRowID) int {
	for i, r := range rows {
		if r == id {
			return i
		}
	}
	return 0
}

// moveVideoSettingsItem steps the selection, wrapping. The selection is
// clamped first: the page may have changed under it (a session that ended and
// one of the other kind that started while the sheet stayed open).
func moveVideoSettingsItem(delta int) {
	n := len(videoSettingsRowIDs())
	if n <= 0 {
		videoSettingsItem = 0
		return
	}
	if videoSettingsItem >= n {
		videoSettingsItem = n - 1
	}
	if videoSettingsItem < 0 {
		videoSettingsItem = 0
	}
	videoSettingsItem = ((videoSettingsItem+delta)%n + n) % n
}

// vsOnOff is a switch row's value.
func vsOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// videoSheetRowLabel is one row's text: "Name: value" where it has a value
// (the sheet draws the value at the right).
func videoSheetRowLabel(id videoSheetRowID, c videoSheetContext) string {
	switch id {
	case vsRowSubtitles:
		return "Subtitles: " + vsOnOff(subtitlesEnabled())
	case vsRowFullscreen:
		return "Full screen: " + vsOnOff(videoFullscreen)
	case vsRowSpeed:
		return "Speed: " + vcSpeedLabel(snapshotVideoState().speed)
	case vsRowQuality:
		if appConfig == nil {
			return "Quality: Auto"
		}
		return "Quality: " + videoQualityRowLabel(appConfig)
	case vsRowUpNext:
		return upNextSheetRowLabel()
	case vsRowSave:
		switch {
		case c.local:
			return "Save offline: Audio"
		case c.live:
			return "Save offline: Not for live streams"
		}
		return "Save offline: Video or audio"
	case vsRowClose:
		return "Close"
	case vsRowSaveVideo:
		return "Download video: " + videoSaveRowValue(downloadKindVideo, c, "MP4")
	case vsRowSaveAudio:
		return "Audio only: " + videoSaveRowValue(downloadKindAudio, c, "M4A")
	case vsRowExtractAudio:
		if mediaExtracting.Load() {
			return "Extract audio: Working…"
		}
		return "Extract audio: M4A"
	case vsRowGetSubtitles:
		return "Get subtitles: " + videoSheetSubsValue(c.subs)
	case vsRowBack:
		return "Back"
	}
	return ""
}

// videoSaveRowValue is a stream download row's value: what pressing it would
// save, or why it would not.
func videoSaveRowValue(kind downloadKind, c videoSheetContext, format string) string {
	if c.live {
		return "Live"
	}
	saved, queued := videoSaveState(strings.TrimSpace(currentPlaybackURL), kind)
	switch {
	case saved:
		return "In Downloads"
	case queued:
		return "Downloading"
	}
	return format
}

// videoSaveStateCache keeps the stream rows' answers for a moment. Each one is
// a pass over the Downloads library and the queue (matching YouTube ids with
// regular expressions), and the sheet is drawn every frame. Render thread only.
var videoSaveStateCache struct {
	page   string
	at     time.Time
	saved  [2]bool // [audio, video]
	queued [2]bool
}

// videoSaveStateFresh is how long the cached answers stand.
const videoSaveStateFresh = 300 * time.Millisecond

// videoSaveState reports whether the page's video (or track) is in the
// Downloads library, or waiting or downloading in the queue.
func videoSaveState(page string, kind downloadKind) (saved, queued bool) {
	i := 0
	if kind == downloadKindVideo {
		i = 1
	}
	c := &videoSaveStateCache
	if c.page != page || c.at.IsZero() || time.Since(c.at) > videoSaveStateFresh {
		c.page, c.at = page, time.Now()
		c.saved, c.queued = [2]bool{}, [2]bool{}
		if page != "" && isNetworkSource(page) {
			_, c.saved[0] = dlLibraryFindSource(page, false)
			_, c.saved[1] = dlLibraryFindSource(page, true)
			c.queued[0] = downloadsHas(page, downloadKindAudio)
			c.queued[1] = downloadsHas(page, downloadKindVideo)
		}
	}
	return c.saved[i], c.queued[i]
}

// videoSheetSubsValue is the "Get subtitles" row's value.
func videoSheetSubsValue(s videoSheetSubs) string {
	switch s {
	case vsSubsLoaded:
		return "Loaded"
	case vsSubsBusy:
		return "Looking…"
	case vsSubsNoSource:
		return "Source unknown"
	case vsSubsNone:
		return "None found"
	case vsSubsBadLanguage:
		return "Check subtitle language"
	}
	lang, err := subtitleLanguage(appConfig)
	if err != nil {
		return "Check subtitle language"
	}
	return subtitleLanguageName(lang)
}

// videoSettingsActivate runs the row at idx of the page on screen.
func videoSettingsActivate(idx int) {
	rows := videoSettingsRowIDs()
	if idx < 0 || idx >= len(rows) {
		return
	}
	switch rows[idx] {
	case vsRowSubtitles:
		toggleSubtitlesWithFeedback()
	case vsRowFullscreen:
		toggleVideoFullscreen()
		videoSettingsOpen = !videoSettingsOpen
		if videoSettingsOpen {
			revealVideoChrome()
		}
	case vsRowSpeed:
		cyclePlaybackSpeed()
	case vsRowQuality:
		cycleVideoQuality(appConfig)
	case vsRowUpNext:
		// The queue outlives the results screen it was filled from, so this is
		// the one place it can be emptied once those rows are gone.
		if n := upNextClear(); n > 0 {
			PlayBackSound()
			ShowToast(fmt.Sprintf("Up Next cleared (%d)", n), ToastKindInfo)
		} else {
			ShowToast("Up Next is empty", ToastKindInfo)
		}
	case vsRowSave:
		videoSettingsPage = videoSheetSave
		videoSettingsItem = 0
	case vsRowClose:
		videoSettingsOpen = false
	case vsRowSaveVideo:
		videoSaveCurrent(downloadKindVideo)
	case vsRowSaveAudio:
		videoSaveCurrent(downloadKindAudio)
	case vsRowExtractAudio:
		videoExtractCurrent()
	case vsRowGetSubtitles:
		videoGetSubtitlesCurrent()
	case vsRowBack:
		videoSettingsBack()
	}
}

// videoPageSaved reports whether the page on screen is in the Downloads
// library already, as a video or as a track.
func videoPageSaved(video bool) bool {
	page := strings.TrimSpace(currentPlaybackURL)
	if page == "" || !isNetworkSource(page) {
		return false
	}
	_, ok := dlLibraryFindSource(page, video)
	return ok
}

// videoSessionWillBeKept reports a stream that had to be downloaded whole
// before it could play and that the Downloads library keeps when it closes
// ("Keep watched videos", dl_library.go): downloading it again would fetch the
// same file twice.
func videoSessionWillBeKept() bool {
	sess := playbackCurrentSession()
	return sess != nil && sess.tempFile && dlKeepWatchedEnabled() && dlTempLooksComplete(sess.source)
}

// videoSaveCurrent queues the stream on screen for offline play, or its sound
// as a track for the music player. A refusal (a file, a live stream, no
// connection) is said and leaves the sheet open; a queued download — or one
// that is saved or on its way already — closes it.
func videoSaveCurrent(kind downloadKind) {
	page := strings.TrimSpace(currentPlaybackURL)
	lower := strings.ToLower(page)
	if page == "" || !(strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) {
		ShowToast("This video cannot be downloaded", ToastKindWarning)
		return
	}
	if snapshotVideoState().duration <= 0 {
		ShowToast("Live streams cannot be saved", ToastKindWarning)
		return
	}
	if _, ok := dlLibraryFindSource(page, kind == downloadKindVideo); ok {
		videoSettingsOpen = false
		ShowToast(dlAlreadySavedNotice, ToastKindInfo)
		return
	}
	if kind == downloadKindVideo && videoSessionWillBeKept() {
		videoSettingsOpen = false
		ShowToast("Already downloaded — it moves to Media → Downloads when the video closes", ToastKindInfo)
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	dlRememberMeta(page, dlWatchedMeta(page))
	if _, err := downloadEnqueueKind(page, kind); err != nil {
		if errors.Is(err, errDownloadAlreadyQueued) {
			videoSettingsOpen = false
			ShowToast(dlAlreadyQueuedNotice, ToastKindInfo)
			return
		}
		ShowToast(plainNetworkError(err), ToastKindWarning)
		return
	}
	videoSettingsOpen = false
	if kind == downloadKindVideo {
		ShowToast("Downloading the video — it will be in Media → Downloads, with subtitles when there are any", ToastKindSuccess)
		return
	}
	ShowToast("Saving the audio — it will be in Downloads and the music player", ToastKindSuccess)
}

// videoExtractCurrent saves the sound of the file on screen as a track. The
// sheet closes only when the extraction started; a refusal leaves it open
// with the reason in a toast.
func videoExtractCurrent() {
	sess := playbackCurrentSession()
	if sess == nil || !sess.local {
		ShowToast("Only a video on the card can be extracted — download it first", ToastKindWarning)
		return
	}
	title, _ := currentMediaTitle()
	if mediaExtractAudio(appConfig, sess.source, title, "") {
		videoSettingsOpen = false
	}
}

// videoGetSubtitlesCurrent is "Get subtitles": fetch the file's subtitles from
// the page it was downloaded from, or say why that is not possible now. A
// fetch closes the sheet; a refusal leaves it open.
func videoGetSubtitlesCurrent() {
	sess := playbackCurrentSession()
	if sess == nil || !sess.local {
		return
	}
	switch videoSheetSubsFor(sess) {
	case vsSubsBadLanguage:
		ShowToast("Check the subtitle language in Settings — it is not a language code", ToastKindWarning)
	case vsSubsLoaded:
		if subtitlesEnabled() {
			ShowToast("This video's subtitles are on already", ToastKindInfo)
			return
		}
		videoSettingsOpen = false
		toggleSubtitlesWithFeedback()
	case vsSubsBusy:
		ShowToast("Still looking for subtitles…", ToastKindInfo)
	case vsSubsNoSource:
		ShowToast("Only a video saved from the web can fetch subtitles — this file has no page to ask", ToastKindInfo)
	case vsSubsNone:
		lang, _ := subtitleLanguage(appConfig)
		ShowToast("No "+subtitleLanguageName(lang)+" subtitles for this video", ToastKindInfo)
	default:
		videoSettingsOpen = false
		result, msg := subtitlesFetchFromSource(appConfig)
		subtitleToggleFeedback(result, msg)
	}
}
