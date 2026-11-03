package main

// playbackqueue_test.go — Up Next is what makes "after this one" possible, so
// the tests pin the list itself (order, dedupe, the cap, taking the head), the
// one-line summary the player draws, the hand-off decision at a natural end of
// stream, and the Queue pill that fills it from a result row.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// upNextFixture empties the process-wide list and restores the hand-off flag,
// so one test cannot leak a queued video into the next.
func upNextFixture(t *testing.T) {
	t.Helper()
	upNextQueue.clear()
	upNextAdvancePending = false
	t.Cleanup(func() {
		upNextQueue.clear()
		upNextAdvancePending = false
	})
}

func upNextVideo(id, title string) VideoInfo {
	return VideoInfo{ID: id, Title: title, WebpageURL: "https://youtu.be/" + id}
}

func TestUpNextAddKeepsOrderAndRefusesDuplicates(t *testing.T) {
	upNextFixture(t)

	if pos, added, err := upNextAdd(upNextVideo("a", "First")); err != nil || !added || pos != 1 {
		t.Fatalf("first add = %d/%v/%v, want position 1 added", pos, added, err)
	}
	if pos, added, err := upNextAdd(upNextVideo("b", "Second")); err != nil || !added || pos != 2 {
		t.Fatalf("second add = %d/%v/%v, want position 2 added", pos, added, err)
	}
	// The same link twice is one entry: the pill is a toggle, and a double
	// press must not turn one video into two turns in the queue.
	if pos, added, err := upNextAdd(upNextVideo("a", "First again")); err != nil || added || pos != 1 {
		t.Fatalf("duplicate add = %d/%v/%v, want its existing position and no append", pos, added, err)
	}
	if got := upNextLen(); got != 2 {
		t.Fatalf("queue length = %d, want 2", got)
	}
	items := upNextSnapshot()
	if items[0].Title != "First" || items[1].Title != "Second" {
		t.Fatalf("queue order = %q, %q", items[0].Title, items[1].Title)
	}

	// A result with no link cannot be played, so it cannot be queued either.
	if _, _, err := upNextAdd(VideoInfo{Title: "no link"}); err == nil {
		t.Error("a result without a link was queued")
	}
	if got := upNextLen(); got != 2 {
		t.Fatalf("queue length after a refused add = %d, want 2", got)
	}
}

func TestUpNextCapStopsWithoutLosingTheList(t *testing.T) {
	upNextFixture(t)
	for i := 0; i < upNextLimit; i++ {
		if _, _, err := upNextAdd(upNextVideo(string(rune('a'+i%26))+strings.Repeat("x", i/26), "v")); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if got := upNextLen(); got != upNextLimit {
		t.Fatalf("queue length = %d, want the cap %d", got, upNextLimit)
	}
	if _, _, err := upNextAdd(VideoInfo{Title: "overflow", WebpageURL: "https://youtu.be/overflow"}); err == nil {
		t.Fatal("a full queue accepted another video")
	}
	if got := upNextLen(); got != upNextLimit {
		t.Fatalf("a refused add changed the queue to %d, want %d", got, upNextLimit)
	}
}

func TestUpNextToggleAddsThenRemoves(t *testing.T) {
	upNextFixture(t)
	video := upNextVideo("a", "First")

	queued, pos, err := upNextToggle(video)
	if err != nil || !queued || pos != 1 {
		t.Fatalf("toggle on = %v/%d/%v, want queued at 1", queued, pos, err)
	}
	if !upNextHas(video.GetURL()) {
		t.Fatal("the queued video is not reported as present")
	}
	queued, _, err = upNextToggle(video)
	if err != nil || queued {
		t.Fatalf("toggle off = %v/%v, want removed", queued, err)
	}
	if upNextHas(video.GetURL()) || upNextLen() != 0 {
		t.Fatalf("toggle off left %d entries", upNextLen())
	}
	if _, _, err := upNextToggle(VideoInfo{Title: "no link"}); err == nil {
		t.Error("a result without a link was toggled")
	}
}

func TestUpNextTakeDrainsInOrder(t *testing.T) {
	upNextFixture(t)
	upNextAdd(upNextVideo("a", "First"))
	upNextAdd(upNextVideo("b", "Second"))
	upNextAdd(upNextVideo("c", "Third"))

	for i, want := range []string{"First", "Second", "Third"} {
		got, ok := upNextTake()
		if !ok {
			t.Fatalf("take %d reported an empty queue", i)
		}
		if got.Title != want {
			t.Fatalf("take %d = %q, want %q", i, got.Title, want)
		}
		if remaining := upNextLen(); remaining != 2-i {
			t.Fatalf("after take %d, %d entries left, want %d", i, remaining, 2-i)
		}
	}
	if _, ok := upNextTake(); ok {
		t.Error("take on an empty queue reported a video")
	}

	upNextAdd(upNextVideo("d", "Fourth"))
	upNextAdd(upNextVideo("e", "Fifth"))
	if got := upNextClear(); got != 2 {
		t.Fatalf("clear reported %d entries, want 2", got)
	}
	if upNextLen() != 0 {
		t.Fatalf("clear left %d entries", upNextLen())
	}

	// Removing by link is how the toggle takes one back out of the middle.
	upNextAdd(upNextVideo("a", "First"))
	upNextAdd(upNextVideo("b", "Second"))
	if !upNextRemove("https://youtu.be/a") {
		t.Fatal("remove did not find a queued link")
	}
	if items := upNextSnapshot(); len(items) != 1 || items[0].Title != "Second" {
		t.Fatalf("after remove = %+v, want the second video only", items)
	}
	if upNextRemove("https://youtu.be/a") {
		t.Error("removing an absent link reported success")
	}
}

func TestUpNextSummary(t *testing.T) {
	upNextFixture(t)
	if got := upNextSummary(); got != "" {
		t.Fatalf("empty queue summary = %q, want an empty line", got)
	}
	upNextAdd(upNextVideo("a", "First"))
	if got := upNextSummary(); got != "Up next: First" {
		t.Fatalf("one-item summary = %q", got)
	}
	upNextAdd(upNextVideo("b", "Second"))
	upNextAdd(upNextVideo("c", "Third"))
	if got := upNextSummary(); got != "Up next: First (+2)" {
		t.Fatalf("three-item summary = %q", got)
	}

	// A long title is shortened to the overlay budget, and a titleless result
	// still names itself instead of drawing "Up next: " and nothing.
	upNextQueue.clear()
	upNextAdd(upNextVideo("d", strings.Repeat("Long ", 20)))
	line := upNextSummary()
	if !strings.HasPrefix(line, "Up next: ") {
		t.Fatalf("long-title summary = %q", line)
	}
	title := strings.TrimPrefix(line, "Up next: ")
	if runes := []rune(title); len(runes) > upNextTitleMax {
		t.Fatalf("summary title is %d runes, want at most %d", len(runes), upNextTitleMax)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("shortened title %q lost its ellipsis", title)
	}

	upNextQueue.clear()
	upNextAdd(VideoInfo{WebpageURL: "https://youtu.be/zzz"})
	if got := upNextSummary(); got != "Up next: https://youtu.be/zzz" {
		t.Fatalf("titleless summary = %q, want the link", got)
	}
}

func TestUpNextAfterPlayback(t *testing.T) {
	upNextFixture(t)
	// Nothing queued: an end of stream is just an end.
	if upNextAfterPlayback(false) {
		t.Error("an empty queue claimed the hand-off")
	}
	upNextAdd(upNextVideo("a", "First"))
	if !upNextAfterPlayback(false) {
		t.Error("a queued video did not claim the hand-off")
	}
	// The Shorts feed advances itself, so it keeps the hand-off while armed.
	if upNextAfterPlayback(true) {
		t.Error("Up Next took the hand-off from an armed Shorts feed")
	}
	// The decision alone must not consume the queue: it is taken by the main
	// loop, once the player is idle.
	if got := upNextLen(); got != 1 {
		t.Fatalf("the hand-off decision drained the queue to %d", got)
	}
}

func TestDrainUpNextAutoplayWithNoConfigClearsTheFlag(t *testing.T) {
	upNextFixture(t)
	upNextAdd(upNextVideo("a", "First"))
	upNextAdvancePending = true

	drainUpNextAutoplay(nil)

	if upNextAdvancePending {
		t.Error("the pending hand-off survived a drain with no config")
	}
	// No config means no player: the queued video must still be waiting, not
	// silently dropped.
	if got := upNextLen(); got != 1 {
		t.Fatalf("queue length = %d, want the entry kept", got)
	}
}

// TestUpNextSheetRowClearsTheQueue pins the one place an Up Next entry can be
// dropped once the results screen it came from is gone: the player's settings
// sheet row.
func TestUpNextSheetRowClearsTheQueue(t *testing.T) {
	upNextFixture(t)
	prevOpen := videoSettingsOpen
	t.Cleanup(func() { videoSettingsOpen = prevOpen })

	if got := upNextSheetRowLabel(); got != "Up Next: empty" {
		t.Fatalf("empty row label = %q", got)
	}
	upNextAdd(upNextVideo("a", "First"))
	upNextAdd(upNextVideo("b", "Second"))
	if got := upNextSheetRowLabel(); got != "Up Next: 2 queued" {
		t.Fatalf("filled row label = %q", got)
	}

	videoSettingsActivate(4)
	if got := upNextLen(); got != 0 {
		t.Fatalf("the settings row left %d entries, want the queue empty", got)
	}
	// Pressing it again on an empty list is a no-op, not a panic, and "Close"
	// is still the last row after the queue row was inserted in front of it.
	videoSettingsActivate(4)
	videoSettingsOpen = true
	videoSettingsActivate(5)
	if videoSettingsOpen {
		t.Error("the last settings row is not Close")
	}
}

// TestQueuePillQueuesTheFocusedResult drives the real key path: D-pad into the
// strip, A on the Queue pill, and the focused result lands in Up Next. Pressing
// it again takes the result back out.
func TestQueuePillQueuesTheFocusedResult(t *testing.T) {
	upNextFixture(t)
	config := resultActionFixture(t, 1)

	focusedResultAction = resultActionNone
	for focusedResultAction != resultActionQueue {
		handleSearchResultsAction(ActionNavigateRight, config)
	}
	if focusedResultAction != resultActionQueue {
		t.Fatalf("the D-pad never reached the Queue pill (stopped at %d)", focusedResultAction)
	}
	if got := resultActionLabel(resultActionQueue); got != "Queue" {
		t.Fatalf("pill label = %q, want Queue", got)
	}

	// A runs the pill instead of the row, so nothing starts playing.
	if !performResultAction(focusedResultAction, focusedResultIndex, config.Variables.Custom["search_results"].([]VideoInfo)) {
		t.Fatal("the Queue pill was not handled")
	}
	if items := upNextSnapshot(); len(items) != 1 || items[0].Title != "Second" {
		t.Fatalf("queue = %+v, want the focused result", items)
	}
	if !upNextHas("https://youtu.be/bbb") {
		t.Fatal("the focused result's link is not reported as queued")
	}

	// The same press takes it back out; the pill stays reachable afterwards.
	if !performResultAction(resultActionQueue, focusedResultIndex, config.Variables.Custom["search_results"].([]VideoInfo)) {
		t.Fatal("the Queue pill stopped being handled")
	}
	if got := upNextLen(); got != 0 {
		t.Fatalf("a second press left %d entries, want the result removed", got)
	}
}

// TestSearchResultsListDrawsQueuePill proves the new pill is actually laid out
// on the row (not just present in the order slice): the list records a hit rect
// for every pill on every visible row, and a tap on it resolves to Queue.
func TestSearchResultsListDrawsQueuePill(t *testing.T) {
	upNextFixture(t)
	config := resultActionFixture(t, 1)
	focusedResultIndex = 0

	newProbeFrame(t)
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	ClearTextCache()
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() {
		destroyTestRenderer(renderer)
		accentColor = prevAccent
	})

	element := Element{Type: "searchresults", Variable: "search_results", X: 24, Y: 136, Columns: 1, Rows: 6}
	videos := config.Variables.Custom["search_results"].([]VideoInfo)
	scrollY = 0
	renderSearchResultsList(renderer, config, element, videos, 1232, 456)

	found := 0
	for _, hit := range resultActionHits {
		if hit.action == resultActionQueue {
			found++
		}
	}
	if want := len(videos); found != want {
		t.Fatalf("Queue hit rects = %d, want one per visible row (%d)", found, want)
	}
	var queueRect sdl.Rect
	for _, hit := range resultActionHits {
		if hit.action == resultActionQueue && hit.index == 0 {
			queueRect = hit.rect
			break
		}
	}
	if action, idx := resultActionHitAt(queueRect.X+4, queueRect.Y+queueRect.H/2); action != resultActionQueue || idx != 0 {
		t.Fatalf("tap on the Queue pill resolved to action %d index %d", action, idx)
	}
}
