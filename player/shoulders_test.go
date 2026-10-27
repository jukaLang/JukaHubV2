package main

import "testing"

// A trigger reports a stream of axis values; only the squeeze counts as a
// press, and the release has to be seen before the next one.
func TestTriggerLatchesIntoOnePress(t *testing.T) {
	resetPadTriggerLatches()
	t.Cleanup(resetPadTriggerLatches)

	if triggerPressedEdge(0, 0) {
		t.Fatal("a resting trigger counted as a press")
	}
	if triggerPressedEdge(0, 8000) {
		t.Fatal("a half pull counted as a press")
	}
	if !triggerPressedEdge(0, 20000) {
		t.Fatal("a full pull did not count as a press")
	}
	// Still held: no repeated pages.
	for _, v := range []int16{20000, 30000, 32000, 25000} {
		if triggerPressedEdge(0, v) {
			t.Fatalf("a held trigger repeated at value %d", v)
		}
	}
	// Released, then squeezed again.
	if triggerPressedEdge(0, 2000) {
		t.Fatal("the release counted as a press")
	}
	if !triggerPressedEdge(0, 20000) {
		t.Fatal("the next squeeze was swallowed")
	}
	// Each trigger keeps its own latch.
	if !triggerPressedEdge(1, 20000) {
		t.Fatal("R2 was blocked by L2's latch")
	}
}

func TestClampIndex(t *testing.T) {
	cases := []struct{ index, count, want int }{
		{-5, 10, 0},
		{0, 10, 0},
		{9, 10, 9},
		{15, 10, 9},
		{3, 0, 0},
		{3, -1, 0},
	}
	for _, c := range cases {
		if got := clampIndex(c.index, c.count); got != c.want {
			t.Errorf("clampIndex(%d, %d) = %d, want %d", c.index, c.count, got, c.want)
		}
	}
}

// The playback queue is what L2/R2 step through: the Shorts feed wins, then the
// search results of the scene that launched the video.
func TestSetPlaybackQueuePicksTheLaunchingList(t *testing.T) {
	prevQueue := playbackQueue
	prevScene := currentSceneIndex
	t.Cleanup(func() { playbackQueue = prevQueue; currentSceneIndex = prevScene })
	playbackQueue = playbackQueueState{index: -1}

	results := []VideoInfo{
		{ID: "r1", WebpageURL: "https://youtu.be/r1"},
		{ID: "r2", WebpageURL: "https://youtu.be/r2"},
	}
	config := &Config{}
	config.Scenes = []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "searchresults", Variable: "search_results"},
	}}}
	config.Variables.Custom = map[string]interface{}{"search_results": results}
	currentSceneIndex = 0

	setPlaybackQueue(config, results[1])
	if len(playbackQueue.videos) != 2 || playbackQueue.index != 1 || playbackQueue.shorts {
		t.Fatalf("results queue = %+v", playbackQueue)
	}

	shorts := []VideoInfo{{ID: "s1", WebpageURL: "https://youtu.be/s1"}}
	config.Variables.Custom["shorts_list"] = shorts
	setPlaybackQueue(config, shorts[0])
	if !playbackQueue.shorts || playbackQueue.index != 0 {
		t.Fatalf("shorts queue = %+v", playbackQueue)
	}

	// A video from neither list leaves nothing to skip through.
	setPlaybackQueue(config, VideoInfo{ID: "x", WebpageURL: "https://youtu.be/x"})
	if playbackQueue.index != -1 || len(playbackQueue.videos) != 0 {
		t.Fatalf("unknown video queue = %+v", playbackQueue)
	}
}

// The notification centre pages by the rows it last drew.
func TestNotifShoulderPagesRows(t *testing.T) {
	prev := nc
	t.Cleanup(func() { nc = prev })
	nc.history = make([]notification, 9)
	nc.visibleRows = 4
	nc.cursor = 0

	notifShoulder(padR1)
	if nc.cursor != 4 {
		t.Fatalf("R1 moved to %d, want one window (4)", nc.cursor)
	}
	notifShoulder(padR2)
	if nc.cursor != 8 {
		t.Fatalf("R2 moved to %d, want the last row (8)", nc.cursor)
	}
	notifShoulder(padL1)
	if nc.cursor != 4 {
		t.Fatalf("L1 moved to %d, want 4", nc.cursor)
	}
	notifShoulder(padL2)
	if nc.cursor != 0 {
		t.Fatalf("L2 moved to %d, want the first row", nc.cursor)
	}
}

// Quick Settings pages rows, but keeps L1/R1 on the sliders as value nudges.
func TestQSShoulderPagesRows(t *testing.T) {
	prev := qs
	t.Cleanup(func() { qs = prev })
	qs.toggles = []qsToggle{{}, {}, {}}
	qs.sliders = nil
	qs.links = nil
	qs.cursor = 0

	qsShoulder(padR1)
	if qs.cursor != 3 {
		t.Fatalf("R1 moved to %d, want the last row (3)", qs.cursor)
	}
	qsShoulder(padL2)
	if qs.cursor != 0 {
		t.Fatalf("L2 moved to %d, want the first row", qs.cursor)
	}
	qsShoulder(padR2)
	if qs.cursor != 3 {
		t.Fatalf("R2 moved to %d, want the last row", qs.cursor)
	}
}

// The social notification list pages the same way.
func TestSocialNotifShoulderPages(t *testing.T) {
	prevOpen, prevHistory, prevCursor := socialNotifOpen, socialNotifHistory, socialNotifCursor
	t.Cleanup(func() {
		socialNotifOpen, socialNotifHistory, socialNotifCursor = prevOpen, prevHistory, prevCursor
	})
	socialNotifHistory = make([]socialNotif, 12)
	socialNotifCursor = 0

	socialNotifShoulder(padR1)
	if socialNotifCursor != 5 {
		t.Fatalf("R1 moved to %d, want 5", socialNotifCursor)
	}
	socialNotifShoulder(padR2)
	if socialNotifCursor != 11 {
		t.Fatalf("R2 moved to %d, want the last row", socialNotifCursor)
	}
	socialNotifShoulder(padL2)
	if socialNotifCursor != 0 {
		t.Fatalf("L2 moved to %d, want the first row", socialNotifCursor)
	}
}

// Skipping past either end of the queue answers instead of changing playback.
func TestSkipPlaybackStopsAtTheEnds(t *testing.T) {
	prevQueue := playbackQueue
	t.Cleanup(func() { playbackQueue = prevQueue })
	playbackQueue = playbackQueueState{
		videos: []VideoInfo{{ID: "a"}, {ID: "b"}},
		index:  0,
	}
	if playerPhase() != "idle" {
		// Playback state is global; a non-idle phase here means an earlier test
		// left the embedded player running, which is the leak to report rather
		// than a reason to skip the queue-boundary check.
		t.Fatalf("the embedded player is %q before this test started; an earlier test left it running", playerPhase())
	}
	if !skipPlayback(&Config{}, -1) {
		t.Fatal("skip before the first video was not handled")
	}
	playbackQueue.index = 1
	if !skipPlayback(&Config{}, 1) {
		t.Fatal("skip past the last video was not handled")
	}
	if playbackQueue.index != 1 {
		t.Fatalf("a skipped past the end moved the queue to %d", playbackQueue.index)
	}
}
