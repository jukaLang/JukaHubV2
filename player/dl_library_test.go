package main

// dl_library_test.go — the Downloads library: reconciling the index with the
// folders, refreshes that must not bring removed rows back, the storage limit
// for kept videos, keeping a watched download, and the audio/video split. No
// network and no ffmpeg: posters are stubbed and every folder is a t.TempDir.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dlLibraryTestState gives one test the library to itself, rooted in a fresh
// download folder, and waits for the library's background saves before that
// folder is removed.
func dlLibraryTestState(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prevRoot, prevGrab := downloadsRootOverride, dlPosterGrabber
	downloadsRootOverride = root
	dlPosterGrabber = func(string) []byte { return nil }
	dlLibrary.mu.Lock()
	prevEntries, prevLoaded, prevLoading := dlLibrary.entries, dlLibrary.loaded, dlLibrary.loading
	dlLibrary.entries, dlLibrary.loaded, dlLibrary.loading = nil, true, false
	dlLibrary.gen++
	dlLibrary.mu.Unlock()
	t.Cleanup(func() {
		dlWaitForLibraryIdle(t)
		dlLibrary.mu.Lock()
		dlLibrary.entries, dlLibrary.loaded, dlLibrary.loading = prevEntries, prevLoaded, prevLoading
		// The change counter only ever moves forward: views cache by it.
		dlLibrary.gen++
		dlLibrary.mu.Unlock()
		dlPosterGrabber = prevGrab
		downloadsRootOverride = prevRoot
	})
	return root
}

// dlWaitForLibraryIdle waits until no load or index write is running.
func dlWaitForLibraryIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dlLibrary.mu.Lock()
		busy := dlLibrary.saving || dlLibrary.loading
		dlLibrary.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("the library was still loading or saving after 5 s")
}

// dlWriteTestFile writes size bytes to path, creating its folder.
func dlWriteTestFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDlReconcileKeepsFilesAndDropsMissingOnes(t *testing.T) {
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	index := []dlLibraryEntry{
		{ID: "a", Path: "/card/video/A.mp4", Video: true, Title: "A", Size: 10, Added: old},
		{ID: "gone", Path: "/card/video/Gone.mp4", Video: true, Title: "Gone", Size: 5, Added: old},
		{ID: "dup", Path: "/card/video/A.mp4", Video: true, Title: "A again", Added: old},
	}
	files := []dlScannedFile{
		{path: "/card/video/A.mp4", size: 12, mod: old, video: true},
		{path: "/card/music/New Song.m4a", size: 7, mod: old.Add(time.Hour)},
	}
	kept, removed, changed := dlReconcile(index, files, nil)
	if !changed {
		t.Fatal("a missing file, a duplicate row and a new file did not count as a change")
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d rows, want 2: %+v", len(kept), kept)
	}
	// Newest first: the file the index did not know, then A.
	if kept[0].Title != "New Song" || kept[0].Video || kept[0].ID == "" || kept[0].Size != 7 {
		t.Errorf("new file row = %+v, want an audio row titled from its file name", kept[0])
	}
	if kept[1].ID != "a" || kept[1].Size != 12 {
		t.Errorf("known row = %+v, want id a with the size measured on the card", kept[1])
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d rows, want the missing file and the duplicate: %+v", len(removed), removed)
	}
}

// A folder that exists but cannot be listed this time keeps its rows: a card
// hiccup must not cost the user the metadata of every kept video.
func TestDlReconcileKeepsRowsOfAnUnreadableFolder(t *testing.T) {
	dir := filepath.Join("card", "video")
	index := []dlLibraryEntry{{ID: "a", Path: filepath.Join(dir, "A.mp4"), Title: "A", Size: 10, Kept: true}}
	kept, removed, changed := dlReconcile(index, nil, []string{dir})
	if len(kept) != 1 || !kept[0].Kept || len(removed) != 0 || changed {
		t.Fatalf("unreadable folder: kept=%+v removed=%+v changed=%v, want the row unchanged", kept, removed, changed)
	}
	kept, removed, _ = dlReconcile(index, nil, nil)
	if len(kept) != 0 || len(removed) != 1 {
		t.Fatalf("listed folder without the file: kept=%+v removed=%+v, want the row dropped", kept, removed)
	}
}

// A refresh started from the live list: a row removed while it ran stays
// removed, a row added meanwhile is kept, and a row in both keeps its live
// fields with the size the scan measured.
func TestDlPublishLoadRespectsChangesDuringARefresh(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	kept := dlLibraryEntry{ID: "kept", Path: "/v/kept.mp4", Title: "Kept", Size: 1, Added: t0}
	removed := dlLibraryEntry{ID: "removed", Path: "/v/removed.mp4", Title: "Removed", Size: 1, Added: t0.Add(time.Minute)}
	startIDs := map[string]bool{"kept": true, "removed": true}

	scannedKept := kept
	scannedKept.Size = 99
	scannedNew := dlLibraryEntry{ID: "scan", Path: "/v/new.mp4", Title: "new", Size: 3, Added: t0.Add(2 * time.Minute)}
	loaded := []dlLibraryEntry{scannedKept, removed, scannedNew}

	liveKept := kept
	liveKept.ThumbFile = "/v/.thumbs/kept.jpg"
	added := dlLibraryEntry{ID: "added", Path: "/v/added.mp4", Title: "Added", Size: 4, Added: t0.Add(3 * time.Minute)}
	live := []dlLibraryEntry{added, liveKept}

	got := dlPublishLoad(loaded, live, startIDs, map[string]bool{dlPathKey(removed.Path): true})
	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if len(got) != 3 || got[0].ID != "added" || got[1].ID != "scan" || got[2].ID != "kept" {
		t.Fatalf("published %v, want [added scan kept] newest first", ids)
	}
	if got[2].ThumbFile != liveKept.ThumbFile || got[2].Size != 99 {
		t.Errorf("kept row = %+v, want the live poster and the scanned size", got[2])
	}
}

// On the first load the live list holds only rows added while it ran; such a
// row wins over the index's row for the same file.
func TestDlPublishLoadPrefersRowsAddedDuringTheFirstLoad(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	disk := dlLibraryEntry{ID: "disk", Path: "/v/a.mp4", Title: "From disk", Added: t0}
	other := dlLibraryEntry{ID: "other", Path: "/v/b.mp4", Title: "Other", Added: t0}
	live := []dlLibraryEntry{{ID: "fresh", Path: "/v/a.mp4", Title: "Fresh", Added: t0.Add(time.Hour)}}
	got := dlPublishLoad([]dlLibraryEntry{disk, other}, live, map[string]bool{}, nil)
	if len(got) != 2 || got[0].ID != "fresh" || got[1].ID != "other" {
		t.Fatalf("published %+v, want the fresh row and the other file", got)
	}
}

func TestDlOverLimitPicksTheOldestKeptVideos(t *testing.T) {
	gb := int64(1) << 30
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	entries := []dlLibraryEntry{
		{ID: "new", Kept: true, Size: 2 * gb, Added: t0.Add(3 * time.Hour)},
		// Downloaded on purpose: never counted against the limit.
		{ID: "mine", Size: 9 * gb, Added: t0},
		{ID: "mid", Kept: true, Size: 2 * gb, Added: t0.Add(2 * time.Hour)},
		{ID: "old", Kept: true, Size: 2 * gb, Added: t0.Add(time.Hour)},
	}
	over, total := dlOverLimit(entries, 4*gb)
	if total != 6*gb || len(over) != 1 || !over["old"] {
		t.Fatalf("4 GB limit: over=%v total=%d, want only the oldest kept video", over, total)
	}
	over, _ = dlOverLimit(entries, 3*gb)
	if len(over) != 2 || !over["old"] || !over["mid"] {
		t.Fatalf("3 GB limit: over=%v, want the two oldest kept videos", over)
	}
	if over, _ = dlOverLimit(entries, 0); over != nil {
		t.Fatalf("no limit: over=%v, want nothing", over)
	}
	if over, _ = dlOverLimit(entries, 8*gb); over != nil {
		t.Fatalf("under the limit: over=%v, want nothing", over)
	}
}

func TestDlKeptLimitChipCycles(t *testing.T) {
	cur := 1
	for _, want := range []int{2, 4, 8, 16, 32, 0, 1} {
		cur = dlNextKeptLimit(cur)
		if cur != want {
			t.Fatalf("next limit = %d, want %d", cur, want)
		}
	}
	if got := dlNextKeptLimit(3); got != dlKeptLimitSteps[0] {
		t.Fatalf("an unknown limit stepped to %d, want the first step", got)
	}
	if dlKeptLimitLabel(0) != "No limit" || dlKeptLimitLabel(4) != "4 GB" {
		t.Fatalf("labels = %q, %q", dlKeptLimitLabel(0), dlKeptLimitLabel(4))
	}
}

func TestDlCustomSettingsAreForgiving(t *testing.T) {
	bools := []struct {
		in   interface{}
		def  bool
		want bool
	}{
		{true, false, true},
		{"off", true, false},
		{" Yes ", false, true},
		{float64(0), true, false},
		{nil, true, true},
		{"maybe", false, false},
	}
	for _, c := range bools {
		if got := dlCustomBool(c.in, c.def); got != c.want {
			t.Errorf("dlCustomBool(%v, %v) = %v, want %v", c.in, c.def, got, c.want)
		}
	}
	ints := []struct {
		in   interface{}
		want int
	}{
		{float64(8), 8},
		{"16", 16},
		{" x", 4},
		{int64(2), 2},
		{nil, 4},
	}
	for _, c := range ints {
		if got := dlCustomInt(c.in, 4); got != c.want {
			t.Errorf("dlCustomInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDlSettingsSyncReadsTheConfig(t *testing.T) {
	prevKeep, prevLimit := dlKeepWatchedOn.Load(), dlKeptLimitGB.Load()
	t.Cleanup(func() {
		dlKeepWatchedOn.Store(prevKeep)
		dlKeptLimitGB.Store(prevLimit)
	})
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		dlKeepWatchedKey: false,
		dlKeptLimitKey:   "8",
	}}}
	dlSettingsSync(config)
	if dlKeepWatchedEnabled() || dlKeptLimitGBValue() != 8 || dlKeptLimitBytes() != 8<<30 {
		t.Fatalf("keep=%v limit=%d GB, want off and 8 GB", dlKeepWatchedEnabled(), dlKeptLimitGBValue())
	}
	// Absent keys are the shipped defaults: keep on, 4 GB.
	dlSettingsSync(&Config{Variables: Variables{Custom: map[string]interface{}{dlKeptLimitKey: -3}}})
	if !dlKeepWatchedEnabled() || dlKeptLimitGBValue() != dlKeptLimitDefaultGB {
		t.Fatalf("defaults: keep=%v limit=%d GB", dlKeepWatchedEnabled(), dlKeptLimitGBValue())
	}
}

func TestDlLibraryKeyMatchesEveryLinkOfAVideo(t *testing.T) {
	id := "dQw4w9WgXcQ"
	for _, link := range []string{
		"https://www.youtube.com/watch?v=" + id,
		"https://youtu.be/" + id,
		"https://www.youtube.com/shorts/" + id,
		" https://m.youtube.com/watch?v=" + id + "&t=42 ",
	} {
		if got := dlLibraryKey(link); got != "yt:"+id {
			t.Errorf("dlLibraryKey(%q) = %q, want yt:%s", link, got, id)
		}
	}
	if got := dlLibraryKey(" https://example.com/clip.mp4 "); got != "https://example.com/clip.mp4" {
		t.Errorf("a non-YouTube link keyed as %q", got)
	}
	if dlLibraryKey("  ") != "" || dlSamePage("", "") {
		t.Error("an empty link must match nothing")
	}
	if !dlSamePage("https://youtu.be/"+id, "https://www.youtube.com/watch?v="+id) {
		t.Error("two links of one video are not the same page")
	}
}

// A track saved for listening is not the video of the same page: it must not
// badge the video's card, stand in for it, or be deleted in its place.
func TestDlFindSourceKeepsAudioAndVideoApart(t *testing.T) {
	dlLibraryTestState(t)
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	dlLibrary.mu.Lock()
	dlLibrary.entries = []dlLibraryEntry{
		{ID: "track", Path: "/m/song.m4a", Source: page},
		{ID: "video", Path: "/v/clip.mp4", Video: true, Source: "https://youtu.be/dQw4w9WgXcQ"},
	}
	dlLibrary.mu.Unlock()

	if e, ok := dlLibraryFindSource(page, true); !ok || e.ID != "video" {
		t.Fatalf("video lookup = %+v, %v", e, ok)
	}
	if e, ok := dlLibraryFindSource(page, false); !ok || e.ID != "track" {
		t.Fatalf("audio lookup = %+v, %v", e, ok)
	}
	if set := dlLibrarySourceSet(); len(set) != 1 || !set["yt:dQw4w9WgXcQ"] {
		t.Fatalf("source set = %v, want the video's page only", set)
	}

	dlLibrary.mu.Lock()
	dlLibrary.entries = dlLibrary.entries[:1]
	dlLibrary.mu.Unlock()
	if _, ok := dlLibraryFindSource(page, true); ok {
		t.Fatal("the audio track stood in for the video")
	}
	if set := dlLibrarySourceSet(); len(set) != 0 {
		t.Fatalf("an audio track badged video cards: %v", set)
	}
	if e, ok := dlLibraryFindPath("/m/song.m4a"); !ok || e.ID != "track" {
		t.Fatalf("path lookup = %+v, %v", e, ok)
	}
	if _, ok := dlLibraryFindID("nope"); ok {
		t.Fatal("an unknown id was found")
	}
}

func TestDlTempLooksCompleteNeedsAWholeFile(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.mp4")
	dlWriteTestFile(t, small, 1<<10)
	whole := filepath.Join(dir, "whole.mp4")
	dlWriteTestFile(t, whole, 70<<10)
	if dlTempLooksComplete(small) {
		t.Error("a 1 KiB file passed as a whole video")
	}
	if !dlTempLooksComplete(whole) {
		t.Error("a whole file was not recognised")
	}
	dlWriteTestFile(t, whole+".part", 10)
	if dlTempLooksComplete(whole) {
		t.Error("a file with a yt-dlp partial beside it passed as whole")
	}
	if dlTempLooksComplete(filepath.Join(dir, "missing.mp4")) {
		t.Error("a missing file passed as whole")
	}
}

// A watched download moves into the library once, with the metadata the
// player knew; a second watch of the same page keeps one copy.
func TestDlKeepWatchedFilesTheVideoOnce(t *testing.T) {
	dlLibraryTestState(t)
	tempDir := t.TempDir()
	prevTemp, prevKeep, prevLimit := videoTempDir, dlKeepWatchedOn.Load(), dlKeptLimitGB.Load()
	videoTempDir = func() string { return tempDir }
	dlKeepWatchedOn.Store(true)
	dlKeptLimitGB.Store(dlKeptLimitDefaultGB)
	t.Cleanup(func() {
		videoTempDir = prevTemp
		dlKeepWatchedOn.Store(prevKeep)
		dlKeptLimitGB.Store(prevLimit)
	})

	page := "https://example.com/watch/clip-1"
	src := filepath.Join(tempDir, "jukahub-video-1.mp4")
	dlWriteTestFile(t, src, 70<<10)
	dlKeepWatched(src, page, VideoInfo{Title: "Clip One", Uploader: "Chan", Duration: 42})
	dlWaitForLibraryIdle(t)

	e, ok := dlLibraryFindSource(page, true)
	if !ok {
		t.Fatal("the watched video was not filed in the library")
	}
	if !e.Kept || !e.Video || e.Title != "Clip One" || e.Channel != "Chan" || e.Duration != 42 {
		t.Errorf("entry = %+v, want a kept video with the player's metadata", e)
	}
	if !isPathWithin(e.Path, downloadsVideoDir()) {
		t.Errorf("kept file %q is not in the video folder %q", e.Path, downloadsVideoDir())
	}
	if _, err := os.Stat(e.Path); err != nil {
		t.Errorf("kept file is missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the temp download is still there (err = %v)", err)
	}

	src2 := filepath.Join(tempDir, "jukahub-video-2.mp4")
	dlWriteTestFile(t, src2, 70<<10)
	dlKeepWatched(src2, page, VideoInfo{Title: "Clip One"})
	dlWaitForLibraryIdle(t)
	if _, err := os.Stat(src2); !os.IsNotExist(err) {
		t.Errorf("a second copy of a kept video stayed in the temp folder (err = %v)", err)
	}
	if entries, _, _ := dlLibrarySnapshot(); len(entries) != 1 {
		t.Fatalf("library holds %d rows after a second watch, want 1", len(entries))
	}
}

// Removing a row deletes its file only inside the app's own download folder.
func TestDlRemoveDeletesOnlyInsideTheDownloadFolder(t *testing.T) {
	root := dlLibraryTestState(t)
	inside := filepath.Join(root, "video", "a.mp4")
	outside := filepath.Join(t.TempDir(), "b.mp4")
	dlWriteTestFile(t, inside, 3)
	dlWriteTestFile(t, outside, 3)
	dlLibrary.mu.Lock()
	dlLibrary.entries = []dlLibraryEntry{
		{ID: "in", Path: inside, Video: true, Size: 3},
		{ID: "out", Path: outside, Video: true, Size: 3},
	}
	dlLibrary.mu.Unlock()

	if e, ok := dlLibraryRemove("in"); !ok || e.ID != "in" {
		t.Fatalf("remove = %+v, %v", e, ok)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(inside); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the removed download is still on the card")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, ok := dlLibraryRemove("in"); ok {
		t.Fatal("a row was removed twice")
	}
	if _, ok := dlLibraryRemove("out"); !ok {
		t.Fatal("the outside row was not dropped from the list")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the download folder was deleted: %v", err)
	}
}

// A refresh reconciles the live list: a file deleted in Files leaves, and the
// rows that stay keep their ids.
func TestDlRefreshDropsAFileDeletedInFiles(t *testing.T) {
	root := dlLibraryTestState(t)
	a := filepath.Join(root, "video", "A.mp4")
	b := filepath.Join(root, "video", "B.mp4")
	dlWriteTestFile(t, a, 10)
	dlWriteTestFile(t, b, 10)

	dlLibrary.mu.Lock()
	dlLibrary.loaded, dlLibrary.loading = false, true
	dlLibrary.mu.Unlock()
	dlLibraryLoad()
	first, _, loaded := dlLibrarySnapshot()
	if !loaded || len(first) != 2 {
		t.Fatalf("first load listed %d rows (loaded=%v), want both files", len(first), loaded)
	}
	idOfA := ""
	for _, e := range first {
		if dlPathKey(e.Path) == dlPathKey(a) {
			idOfA = e.ID
		}
	}

	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	dlLibrary.mu.Lock()
	dlLibrary.loading = true
	dlLibrary.mu.Unlock()
	dlLibraryLoad()
	again, _, _ := dlLibrarySnapshot()
	if len(again) != 1 || again[0].ID != idOfA {
		t.Fatalf("refresh listed %+v, want only A with its old id %q", again, idOfA)
	}
}

// Cancelling from a details sheet that is older than the transfer must leave
// a download that finished meanwhile — and its file — alone.
func TestDlCancelTransferLeavesAFinishedDownload(t *testing.T) {
	downloadsTestReset(t)
	saved := filepath.Join(downloadsVideoDir(), "done.mp4")
	dlWriteTestFile(t, saved, 3)
	downloads.mu.Lock()
	downloads.items = []*downloadItem{
		{ID: 1, Status: downloadDone, Path: saved, Kind: downloadKindVideo},
		{ID: 2, Status: downloadQueued, Kind: downloadKindVideo},
	}
	downloads.mu.Unlock()

	if removed, done := dlCancelTransfer(1); removed || !done {
		t.Fatalf("finished row: removed=%v done=%v, want it reported done and left alone", removed, done)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("cancel deleted a finished download: %v", err)
	}
	if removed, done := dlCancelTransfer(2); !removed || done {
		t.Fatalf("queued row: removed=%v done=%v, want it cancelled", removed, done)
	}
	if removed, done := dlCancelTransfer(3); removed || done {
		t.Fatal("an unknown row was cancelled")
	}
	downloads.mu.Lock()
	n := len(downloads.items)
	downloads.mu.Unlock()
	if n != 1 {
		t.Fatalf("queue holds %d rows, want only the finished one", n)
	}
}
