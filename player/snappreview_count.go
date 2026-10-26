package main

// snappreview_count.go — the number on the Settings row.
//
// The Video snaps switch is only useful if the card actually has clips: a card
// with none is a switch that changes nothing, and before this the row could not
// say so. The row's value therefore carries the count of the library's games
// that have a clip under the snap root, so "On · 12" versus "On · no clips" is
// the difference between a working feature and a setting nobody can use yet.
//
// The count is a library scan, which is why it is not taken while the row is
// built (that runs at startup, before the card has been read for anything else)
// and not taken on the render thread either: the settings page asks for it, the
// scan runs on the scene's own goroutine, and the render thread only reads the
// finished figure. A figure is reused for snapClipCountTTL so sitting on the
// page does not re-walk the card every frame.

import (
	"context"
	"sync"
	"time"
)

// snapClipCountTTL is how long a finished count is trusted while the Settings
// page is up. Long enough that the page is not scanning the card in the
// background, short enough that a user who drops clips on the card and comes
// back sees them.
const snapClipCountTTL = 30 * time.Second

// snapClipCensus is the last finished count. Everything is behind one mutex so
// the render thread can read it while the scan goroutine writes it.
type snapClipCensus struct {
	mu       sync.Mutex
	known    bool
	withClip int
	games    int
	at       time.Time
	scanning bool
}

var snapClips snapClipCensus

// snapClipScan is the seam the tests replace: the app's version is the same
// library scan the panel does, counting the games whose clip path resolves. The
// root is resolved by the caller, on the render thread, because it can come
// from the custom config block.
var snapClipScan = func(root string) (withClip, games int) {
	lib := scanGameLibrary()
	return countGamesWithClips(lib.Systems, root), lib.Total
}

// countGamesWithClips is the pure half: how many of these games have a clip
// under root. It uses gameSnapPath, so the number describes exactly the clips
// the preview would play — not files sitting in folders named after games that
// are no longer on the card.
func countGamesWithClips(systems []librarySystem, root string) int {
	n := 0
	for _, system := range systems {
		for _, game := range system.Games {
			if gameSnapPath(game, root) != "" {
				n++
			}
		}
	}
	return n
}

// snapClipCount reports the last finished count. known is false until the first
// scan lands, which is why the row falls back to the bare state until then.
func snapClipCount() (withClip, games int, known bool) {
	snapClips.mu.Lock()
	defer snapClips.mu.Unlock()
	return snapClips.withClip, snapClips.games, snapClips.known
}

// snapClipCountRefresh asks for a count when the last one is stale (or was never
// taken). It never blocks: the scan runs on the scene's goroutine, so a slow SD
// card cannot stall the frame that opens Settings.
func snapClipCountRefresh(root string) {
	snapClips.mu.Lock()
	if snapClips.scanning || (snapClips.known && time.Since(snapClips.at) < snapClipCountTTL) {
		snapClips.mu.Unlock()
		return
	}
	snapClips.scanning = true
	snapClips.mu.Unlock()

	askedAt := time.Now()
	started := runSceneTask(func(ctx context.Context) {
		with, games := snapClipScan(root)
		snapClips.mu.Lock()
		snapClips.withClip, snapClips.games = with, games
		snapClips.known = true
		snapClips.at = askedAt
		snapClips.scanning = false
		snapClips.mu.Unlock()
	})
	if !started {
		// Shutdown (or a test's closed manager) refused the task: clear the flag
		// so a later frame can try again instead of waiting forever.
		snapClips.mu.Lock()
		snapClips.scanning = false
		snapClips.mu.Unlock()
	}
}
