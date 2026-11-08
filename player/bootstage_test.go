package main

import (
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// The card's bar is only honest if it tracks the queue: the step that ran is
// the one marked done, in order, with the note it recorded, and a step that
// panics is reported as failed without stopping the rest of the boot.
func TestBootWorkSnapshotTracksRealProgress(t *testing.T) {
	resetBootWork(t)
	bootEnqueue("storage", func() { bootStepNote("41 GB free") })
	bootEnqueue("focus-graph", func() {})
	bootEnqueue("gamepad", func() { panic("no pad") })

	snap := bootWorkSnapshot()
	if snap.Title != "Starting up" {
		t.Fatalf("title = %q, want Starting up", snap.Title)
	}
	if snap.Total != 3 || snap.Done != 0 {
		t.Fatalf("empty queue reported %d/%d, want 0/3", snap.Done, snap.Total)
	}
	// Nothing has run yet, so the first step is the one about to be paid for;
	// the card shows it as running rather than as an empty queue.
	if snap.Active != 0 || snap.Current != "Checking the SD card" {
		t.Fatalf("fresh queue active row = %d (%q), want the first step", snap.Active, snap.Current)
	}
	if got := snap.Items[0].Label; got != "Checking the SD card" {
		t.Fatalf("step label = %q, want the boot card's wording", got)
	}

	bootWorkTick(0)
	snap = bootWorkSnapshot()
	if snap.Done != 1 {
		t.Fatalf("after one step: %d/%d done, want 1/3", snap.Done, snap.Total)
	}
	if snap.Items[0].State != stageDone || snap.Items[0].Detail != "41 GB free" {
		t.Fatalf("first row = %+v, want done with its probe result", snap.Items[0])
	}
	if snap.Items[1].State != stageActive {
		t.Fatalf("second row = %v, want active", snap.Items[1].State)
	}

	bootWorkDrain()
	snap = bootWorkSnapshot()
	if snap.Done != 3 || snap.Failed != 1 {
		t.Fatalf("drain: done %d failed %d, want 3 and 1", snap.Done, snap.Failed)
	}
	if snap.Items[2].State != stageFailed {
		t.Fatalf("panicking step = %v, want failed", snap.Items[2].State)
	}
	if frac := snap.fraction(); frac != 1 {
		t.Fatalf("fraction = %v after a drained queue, want 1", frac)
	}
}

// The boot card must paint the glass sheet, the track and the step rows without
// a window, which is also the cheapest guard against a layout change taking the
// first screen of every launch down.
func TestSplashCardRendersProgress(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	snap := stageSnapshot{
		Title: "Starting up", Total: 8, Done: 3, Active: 3,
		Current: "Building the home grid",
		Items: []stageItem{
			{Label: "Checking the SD card", Detail: "41 GB free", State: stageDone},
			{Label: "Reading the gamepad", State: stageDone},
			{Label: "Starting artwork prefetch", State: stageDone},
			{Label: "Building the home grid", State: stageActive},
			{Label: "Warming the first screen", State: stagePending},
		},
	}
	// Both looks: the flat card a renderer without the liquid kit gets, and
	// the liquid glass itself, built on the software renderer like a device
	// that fell back to it.
	for _, liquid := range []bool{false, true} {
		if liquid {
			card := stageCardRect()
			if !bootLiquidPrepare(renderer, card.W, card.H, stageCardRadius, nil, "") {
				t.Fatal("the liquid-glass kit could not be built on the software renderer")
			}
		}
		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		renderSplash(renderer, probeBootConfig(), snap, 600, 1200)

		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
		}
		if got := f.countNonBG(); got < 200 {
			t.Fatalf("boot card (liquid=%v) drew almost nothing (%d lit samples)", liquid, got)
		}
	}
	bootLiquidRelease()
}

// The splash fills its snapshot every frame; once the buffer has grown it must
// not allocate, or the card would feed the collector 30 times a second.
func TestBootWorkSnapshotIntoDoesNotAllocate(t *testing.T) {
	resetBootWork(t)
	for _, name := range []string{"storage", "gamepad", "keyboard", "focus-graph"} {
		bootEnqueue(name, func() {})
	}
	var snap stageSnapshot
	bootWorkSnapshotInto(&snap)
	if allocs := testing.AllocsPerRun(50, func() { bootWorkSnapshotInto(&snap) }); allocs != 0 {
		t.Fatalf("bootWorkSnapshotInto allocated %.1f times per frame", allocs)
	}
	if snap.Total != 4 || snap.Active != 0 {
		t.Fatalf("snapshot = %+v, want 4 rows headed by the first", snap)
	}
}

// The checks run beside the queue: draining the render-thread steps does not
// wait for them, a running check keeps its row active with its latest note,
// a failing one is reported, and the boot only counts as settled once every
// check has answered.
func TestBootWorkAsyncChecksRunBesideTheQueue(t *testing.T) {
	resetBootWork(t)
	release := make(chan struct{})
	bootEnqueueAsync("storage", func(note func(string)) error {
		note("probing")
		<-release
		note("41 GB free")
		return nil
	})
	bootEnqueueAsync("tools", func(note func(string)) error {
		note("ffmpeg missing")
		return errors.New("no network")
	})
	bootEnqueue("focus-graph", func() {})

	bootWorkDrain()
	if bootWorkPending() {
		t.Fatal("the queue still has steps after a drain")
	}
	// Both checks run on their own goroutines: wait for the failure and for the
	// storage check's first note, whichever lands last.
	waitForBoot(t, func() bool {
		s := bootWorkSnapshot()
		return s.Items[1].State == stageFailed && s.Items[0].Detail == "probing"
	})
	if bootWorkSettled() {
		t.Fatal("the boot settled while a check was still running")
	}
	if snap := bootWorkSnapshot(); snap.Items[0].State != stageActive || snap.Items[0].Detail != "probing" {
		t.Fatalf("running check row = %+v, want active with its note", snap.Items[0])
	}

	close(release)
	waitForBoot(t, bootWorkSettled)
	snap := bootWorkSnapshot()
	if snap.Items[0].State != stageDone || snap.Items[0].Detail != "41 GB free" {
		t.Fatalf("finished check row = %+v", snap.Items[0])
	}
	if snap.Done != 3 || snap.Failed != 1 {
		t.Fatalf("done %d failed %d, want 3 and 1", snap.Done, snap.Failed)
	}
}

// waitForBoot polls cond for up to two seconds.
func waitForBoot(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The storage check calls a desktop's folder a disk and the handheld's a card,
// and formats sizes the way the Tools screen does.
func TestBootFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512 << 20, "512 MB"},
		{3 << 30, "3.0 GB"},
		{(3 << 30) + (1 << 29), "3.5 GB"},
		{41 << 30, "41 GB"},
	} {
		if got := bootFormatBytes(tc.in); got != tc.want {
			t.Errorf("bootFormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	want := "card"
	if IsWindows() {
		want = "disk"
	}
	if got := bootStorageNoun(); got != want {
		t.Fatalf("storage noun = %q, want %q", got, want)
	}
}

// A track drives the same snapshot shape the boot queue builds, so the teardown
// card and the boot card cannot drift apart.
func TestStageTrackReportsActiveAndDone(t *testing.T) {
	track := newStageTrack("Powering down", []string{"stop games", "flush state"})
	track.beginAt(0)
	track.finishAt(0, "1 stopped", nil)
	track.beginAt(1)

	snap := track.snapshot()
	if snap.Done != 1 || snap.Active != 1 || snap.Current != "flush state" {
		t.Fatalf("snapshot = %+v, want one done and the second running", snap)
	}
	if snap.Items[0].Detail != "1 stopped" {
		t.Fatalf("detail = %q, want the step's result", snap.Items[0].Detail)
	}
	track.finishAt(1, "", nil)
	if snap := track.snapshot(); snap.Done != 2 || snap.Active != -1 {
		t.Fatalf("finished track = %+v", snap)
	}
}
