package main

// playbackqueue.go — the Up Next list.
//
// The player already remembers the list a video came from, so the shoulder
// buttons can step through search results (playbackQueue in videoplayer.go).
// Up Next is the explicit version of that: results the user pointed at and said
// "after this one" with the Queue pill on their row. It outlives the results
// screen — gathering a few things while browsing and then letting them play is
// the whole point — and its head starts by itself when a video reaches its
// natural end.
//
// It is deliberately session-only. A queue is a plan for tonight, not part of
// the library: persisting it would mean deciding what an expired link means on
// the next boot, and Favorites already covers "keep this for later". Entries are
// deduplicated by link and capped, so a held-down pill cannot grow it without
// bound.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	// upNextLimit is how many results Up Next holds. At a few minutes each,
	// fifty is already a long evening.
	upNextLimit = 50
	// upNextTitleMax keeps the overlay line readable on the 640-wide panel.
	upNextTitleMax = 34
)

// upNextState is the Up Next list. The render thread draws the summary while
// the event thread adds and removes entries, so the slice is behind a mutex
// instead of being read optimistically.
type upNextState struct {
	mu    sync.Mutex
	items []VideoInfo
}

// upNextQueue is the process-wide list.
var upNextQueue upNextState

var (
	errUpNextNoLink = errors.New("that result has no link")
	errUpNextFull   = fmt.Errorf("Up Next is full (%d videos)", upNextLimit)
)

// add appends v unless its link is already queued. It reports the 1-based
// position either way, which is what the toast and the "Queued" pill need.
func (q *upNextState) add(v VideoInfo) (position int, added bool, err error) {
	url := strings.TrimSpace(v.GetURL())
	if url == "" {
		return 0, false, errUpNextNoLink
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if strings.TrimSpace(q.items[i].GetURL()) == url {
			return i + 1, false, nil
		}
	}
	if len(q.items) >= upNextLimit {
		return 0, false, errUpNextFull
	}
	q.items = append(q.items, v)
	return len(q.items), true, nil
}

func (q *upNextState) remove(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if strings.TrimSpace(q.items[i].GetURL()) == url {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

func (q *upNextState) has(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if strings.TrimSpace(q.items[i].GetURL()) == url {
			return true
		}
	}
	return false
}

func (q *upNextState) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// snapshot is the list as it was at the call: the panel and the tests never
// hold the lock.
func (q *upNextState) snapshot() []VideoInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]VideoInfo, len(q.items))
	copy(out, q.items)
	return out
}

// take removes and returns the head of the list — the video that plays when the
// current one ends.
func (q *upNextState) take() (VideoInfo, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return VideoInfo{}, false
	}
	next := q.items[0]
	// Copy the tail so the popped entry cannot be resurrected by a snapshot
	// that was taken before this call.
	tail := make([]VideoInfo, len(q.items)-1)
	copy(tail, q.items[1:])
	q.items = tail
	return next, true
}

// clear drops the list and reports how many entries it held, for the toast.
func (q *upNextState) clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items)
	q.items = nil
	return n
}

func upNextAdd(v VideoInfo) (int, bool, error) { return upNextQueue.add(v) }
func upNextRemove(url string) bool             { return upNextQueue.remove(url) }
func upNextHas(url string) bool                { return upNextQueue.has(url) }
func upNextLen() int                           { return upNextQueue.len() }
func upNextSnapshot() []VideoInfo              { return upNextQueue.snapshot() }
func upNextTake() (VideoInfo, bool)            { return upNextQueue.take() }
func upNextClear() int                         { return upNextQueue.clear() }

// upNextToggle is what the Queue pill does: a result already waiting is taken
// back out, so the same press cannot queue the same video twice.
func upNextToggle(v VideoInfo) (queued bool, position int, err error) {
	url := strings.TrimSpace(v.GetURL())
	if url == "" {
		return false, 0, errUpNextNoLink
	}
	if upNextRemove(url) {
		return false, 0, nil
	}
	position, _, err = upNextAdd(v)
	if err != nil {
		return false, 0, err
	}
	return true, position, nil
}

// upNextTitle is the name a queued result is shown under. A result without a
// title still has to say something, and "queued video" is better than a blank
// chip.
func upNextTitle(v VideoInfo, max int) string {
	title := strings.TrimSpace(v.Title)
	if title == "" {
		title = strings.TrimSpace(v.WebpageURL)
	}
	if title == "" {
		return "queued video"
	}
	return ellipsizeRunes(title, max)
}

// upNextSummary is the single line the player overlay draws above its controls
// — what starts when this video ends, and how much follows it — or "" when
// nothing is waiting.
func upNextSummary() string {
	items := upNextSnapshot()
	switch len(items) {
	case 0:
		return ""
	case 1:
		return "Up next: " + upNextTitle(items[0], upNextTitleMax)
	}
	return fmt.Sprintf("Up next: %s (+%d)", upNextTitle(items[0], upNextTitleMax), len(items)-1)
}

// upNextSheetRowLabel is the player settings sheet's Up Next row: how many
// videos are waiting, on the row whose press empties them. The queue outlives
// the results screen it was filled from, so this is the one place it can be
// cleared once those rows are gone.
func upNextSheetRowLabel() string {
	if n := upNextLen(); n > 0 {
		return fmt.Sprintf("Up Next: %d queued", n)
	}
	return "Up Next: empty"
}

// upNextAfterPlayback is the decision a natural end of stream makes. The Shorts
// feed keeps the hand-off while it is armed, because it advances itself;
// otherwise Up Next takes it when anything is waiting. Stopping a video by hand
// never reaches this, so a queue cannot hijack an intentional stop.
func upNextAfterPlayback(shortsArmed bool) bool {
	if shortsArmed {
		return false
	}
	return upNextLen() > 0
}

// upNextAdvancePending is raised on a natural end of stream and consumed by the
// main loop: the player is still being released at that point, so the next
// video starts on the event thread once it is idle again.
var upNextAdvancePending bool

// drainUpNextAutoplay runs on the main loop after a finished video has released
// the player, and starts whatever was next in line.
func drainUpNextAutoplay(config *Config) {
	if !upNextAdvancePending {
		return
	}
	upNextAdvancePending = false
	if config == nil {
		return
	}
	next, ok := upNextTake()
	if !ok {
		return
	}
	PlayActivateSound()
	playVideoInfo(config, next)
}
