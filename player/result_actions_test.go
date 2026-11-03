package main

// Tests for the Audio / Video / Chat / Save buttons on a video result: the row
// → button cycle, the keys that reach it in the single-column list, the
// dispatch to the download queue (audio or video), the chat and the Favorites
// toggle, and the pill geometry on wide and narrow cards.

import (
	"errors"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestMoveResultActionCyclesThroughTheRow(t *testing.T) {
	// Right and Left both wrap, so the same key that enters the pills also
	// leaves them; the ends are whatever the drawn order says they are.
	if got := moveResultAction(resultActionNone, 1); got != resultActionOrder[0] {
		t.Errorf("Right from the row = %d, want %d", got, resultActionOrder[0])
	}
	if got := moveResultAction(resultActionNone, -1); got != resultActionOrder[len(resultActionOrder)-1] {
		t.Errorf("Left from the row = %d, want %d", got, resultActionOrder[len(resultActionOrder)-1])
	}
	if got := moveResultAction(resultActionOrder[len(resultActionOrder)-1], 1); got != resultActionNone {
		t.Errorf("Right past the last pill = %d, want the row", got)
	}
	if got := moveResultAction(resultActionOrder[0], -1); got != resultActionNone {
		t.Errorf("Left before the first pill = %d, want the row", got)
	}
	// Each step inside the strip lands on the next pill in drawn order. The
	// ends are checked above: they return to the row rather than wrapping into
	// the other end, which is what makes the same key that entered the pills
	// also leave them.
	for i := 1; i < len(resultActionOrder); i++ {
		action := resultActionOrder[i]
		if got := moveResultAction(action, -1); got != resultActionOrder[i-1] {
			t.Errorf("move(%d, -1) = %d, want %d", action, got, resultActionOrder[i-1])
		}
	}
	for i := 0; i < len(resultActionOrder)-1; i++ {
		action := resultActionOrder[i]
		if got := moveResultAction(action, 1); got != resultActionOrder[i+1] {
			t.Errorf("move(%d, +1) = %d, want %d", action, got, resultActionOrder[i+1])
		}
	}
	if got := moveResultAction(resultActionAudio, 0); got != resultActionAudio {
		t.Errorf("move with no direction = %d, want the current pill", got)
	}
}

// resultActionFixture installs a one-scene config with two results and restores
// the globals the list navigation reads.
func resultActionFixture(t *testing.T, columns int32) *Config {
	t.Helper()
	prevScene, prevIndex, prevAction := currentSceneIndex, focusedResultIndex, focusedResultAction
	prevHits := resultActionHits
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex, focusedResultAction = prevScene, prevIndex, prevAction
		resultActionHits = prevHits
		scrollY = 0
	})

	config := &Config{}
	config.Scenes = []SceneConfig{{
		Name:     "Media",
		Elements: []Element{{Type: "searchresults", Variable: "search_results", Columns: columns}},
	}}
	config.Variables.Custom = map[string]interface{}{
		"search_results": []VideoInfo{
			{ID: "a", Title: "First", WebpageURL: "https://youtu.be/aaa"},
			{ID: "b", Title: "Second", WebpageURL: "https://youtu.be/bbb"},
		},
	}
	currentSceneIndex = 0
	focusedResultIndex = 1
	focusedResultAction = resultActionNone
	return config
}

func TestListResultActionKeys(t *testing.T) {
	config := resultActionFixture(t, 1)

	key := func(action Action) {
		t.Helper()
		handleSearchResultsAction(action, config)
	}

	// Right walks into the buttons in drawn order; one more press than there
	// are pills returns to the row.
	for i, want := range resultActionOrder {
		key(ActionNavigateRight)
		if focusedResultAction != want {
			t.Fatalf("Right %d = %d, want %d", i+1, focusedResultAction, want)
		}
	}
	key(ActionNavigateRight)
	if focusedResultAction != resultActionNone {
		t.Fatalf("Right past the last pill = %d, want the row again", focusedResultAction)
	}
	// Left enters at the far end and walks back.
	key(ActionNavigateLeft)
	if want := resultActionOrder[len(resultActionOrder)-1]; focusedResultAction != want {
		t.Fatalf("Left = %d, want %d", focusedResultAction, want)
	}
	for i := len(resultActionOrder) - 2; i >= 0; i-- {
		key(ActionNavigateLeft)
		if focusedResultAction != resultActionOrder[i] {
			t.Fatalf("Left to %d = %d, want %d", i, focusedResultAction, resultActionOrder[i])
		}
	}
	key(ActionNavigateLeft)
	if focusedResultAction != resultActionNone {
		t.Fatalf("Left past the first pill = %d, want the row again", focusedResultAction)
	}

	// Down leaves the buttons first, so the row does not move underneath the
	// hand; the row index must stay put on that press.
	focusedResultAction = resultActionChat
	key(ActionNavigateDown)
	if focusedResultAction != resultActionNone || focusedResultIndex != 1 {
		t.Fatalf("Down from Chat left action %d index %d", focusedResultAction, focusedResultIndex)
	}

	// Back closes the buttons before leaving the scene, and falls through when
	// the row itself holds the focus.
	focusedResultAction = resultActionAudio
	if !listResultAction(ActionBack, config) {
		t.Fatal("Back was not consumed while a button held the focus")
	}
	if focusedResultAction != resultActionNone {
		t.Fatalf("Back left action %d, want the row", focusedResultAction)
	}
	if listResultAction(ActionBack, config) {
		t.Error("Back with no button focused must fall through to the scene")
	}

	// A confirm on the row itself belongs to playback, not to the buttons.
	if listResultAction(ActionConfirm, config) {
		t.Error("A on the row was swallowed by the action layer")
	}
}

func TestPerformResultActionDispatches(t *testing.T) {
	var downloaded, chatted string
	var kinds []downloadKind
	prevDownload, prevChat := resultEnqueueDownload, resultSendToChat
	resultEnqueueDownload = func(url string, kind downloadKind) error {
		downloaded = url
		kinds = append(kinds, kind)
		return nil
	}
	resultSendToChat = func(url string) { chatted = url }
	t.Cleanup(func() { resultEnqueueDownload, resultSendToChat = prevDownload, prevChat })

	videos := []VideoInfo{
		{ID: "a", Title: "First", WebpageURL: "https://youtu.be/aaa"},
		{ID: "b", Title: "Second", URL: "https://youtu.be/bbb"},
	}

	if !performResultAction(resultActionAudio, 1, videos) {
		t.Fatal("audio download was not handled")
	}
	if downloaded != "https://youtu.be/bbb" {
		t.Errorf("downloaded %q, want the second video's link", downloaded)
	}

	if !performResultAction(resultActionVideo, 0, videos) {
		t.Fatal("video download was not handled")
	}
	if downloaded != "https://youtu.be/aaa" {
		t.Errorf("video downloaded %q, want the first video's link", downloaded)
	}
	if len(kinds) != 2 || kinds[0] != downloadKindAudio || kinds[1] != downloadKindVideo {
		t.Fatalf("enqueued kinds = %v, want audio then video", kinds)
	}

	if !performResultAction(resultActionChat, 0, videos) {
		t.Fatal("chat was not handled")
	}
	if chatted != "https://youtu.be/aaa" {
		t.Errorf("sent %q, want the first video's link", chatted)
	}

	// A failed queue push is reported, not swallowed, and nothing is sent.
	downloaded = ""
	kinds = nil
	resultEnqueueDownload = func(string, downloadKind) error { return errors.New("that is not a valid link") }
	if !performResultAction(resultActionVideo, 0, videos) {
		t.Fatal("a rejected download must still be consumed")
	}
	if downloaded != "" {
		t.Errorf("a rejected download reported success: %q", downloaded)
	}

	// No link: the press is consumed with a toast and a seam is never called.
	chatted = ""
	if performResultAction(resultActionChat, 5, videos) {
		t.Error("an out-of-range row acted as a button")
	}
	if !performResultAction(resultActionChat, 0, []VideoInfo{{}}) {
		t.Error("a video without a link must be consumed")
	}
	if chatted != "" {
		t.Errorf("a linkless video was sent: %q", chatted)
	}

	// The row itself is not an action.
	if performResultAction(resultActionNone, 0, videos) {
		t.Error("the row state acted as a button")
	}
}

// TestResultActionPillsLayout pins the geometry the pointer and the D-pad rely
// on: all three pills inside the card, in row order, with the text column
// stopping before them and a usable layout on the Brick's narrower panel.
func TestResultActionPillsLayout(t *testing.T) {
	newProbeFrame(t)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	font, _ := getCachedFont(&Config{}, "small")
	if font == nil {
		t.Fatalf("no font available: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	for _, cardW := range []int32{1232, 920, 600} {
		rects, textRight := resultActionPills(font, 100, 200, cardW, 100)
		if len(rects) != len(resultActionOrder) {
			t.Fatalf("card %d: %d pills, want one per action (%d)", cardW, len(rects), len(resultActionOrder))
		}
		for i, rect := range rects {
			if rect.W <= 0 {
				t.Fatalf("card %d: pill %d %+v", cardW, i, rect)
			}
			if i > 0 {
				prev := rects[i-1]
				if prev.X >= rect.X {
					t.Errorf("card %d: pills are not in %v order: %+v %+v", cardW, resultActionOrder, prev, rect)
				}
				if prev.X+prev.W > rect.X {
					t.Errorf("card %d: pills overlap: %+v %+v", cardW, prev, rect)
				}
			}
			if rect.Y < 200 || rect.Y+rect.H > 300 {
				t.Errorf("card %d: pill %d %+v is outside the 100px row", cardW, i, rect)
			}
		}
		last := rects[len(rects)-1]
		if last.X+last.W > 100+cardW {
			t.Errorf("card %d: the last pill ends at %d, past the card at %d", cardW, last.X+last.W, 100+cardW)
		}
		if textRight <= 120 || textRight > rects[0].X {
			t.Errorf("card %d: text stops at %d, want it before the pills at %d", cardW, textRight, rects[0].X)
		}
	}
}

// TestSearchResultsListDrawsActionPills renders the real list and checks the
// hit rects the pointer uses, including that they sit inside their row.
func TestSearchResultsListDrawsActionPills(t *testing.T) {
	config := resultActionFixture(t, 1)
	focusedResultIndex = 0
	focusedResultAction = resultActionAudio

	f := newProbeFrame(t)
	withScreen(t, probeW, probeH)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
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

	if want := len(videos) * len(resultActionOrder); len(resultActionHits) != want {
		t.Fatalf("hit rects = %d, want one per action on every visible row (%d)", len(resultActionHits), want)
	}
	rowH, gap := int32(100), int32(14)
	for _, hit := range resultActionHits {
		rowTop := element.Y + int32(hit.index)*(rowH+gap) - scrollY
		row := sdl.Rect{X: element.X, Y: rowTop, W: 1232, H: rowH}
		if !pointInRect(hit.rect.X+2, hit.rect.Y+2, row) {
			t.Errorf("%s pill %+v is not inside row %d %+v",
				resultActionLabel(hit.action), hit.rect, hit.index, row)
		}
		viewport := sdl.Rect{X: element.X, Y: element.Y, W: 1232, H: 456}
		if !pointInRect(hit.rect.X+2, hit.rect.Y+2, viewport) {
			t.Errorf("%s pill %+v escapes the list viewport", resultActionLabel(hit.action), hit.rect)
		}
	}
	// The selected Audio pill is actually painted with the accent, not just
	// measured: the hit rects alone would pass even if nothing was drawn.
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	audioRect := resultActionHits[0].rect
	if px := f.at(audioRect.X+6, audioRect.Y+audioRect.H/2); !nearColor(px, ColorAccent, 12) {
		t.Errorf("Audio pill interior = %+v, want the accent fill %+v", px, ColorAccent)
	}

	// The pointer resolves the pill it landed on, and Audio and Video are
	// distinct actions on the same row.
	audio := resultActionHits[0].rect
	if action, idx := resultActionHitAt(audio.X+4, audio.Y+4); action != resultActionAudio || idx != 0 {
		t.Errorf("hit at Audio = action %d index %d, want Audio/0", action, idx)
	}
	video := resultActionHits[1].rect
	if action, idx := resultActionHitAt(video.X+4, video.Y+4); action != resultActionVideo || idx != 0 {
		t.Errorf("hit at Video = action %d index %d, want Video/0", action, idx)
	}
	if action, _ := resultActionHitAt(4, 4); action != resultActionNone {
		t.Errorf("hit outside the list = action %d, want none", action)
	}
}
