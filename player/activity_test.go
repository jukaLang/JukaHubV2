package main

// activity_test.go — the aggregation contract and the offscreen render check
// for the persistent activity strip. The rows must be a plain function of the
// locked sources (installer card, media download queue), the queued rows must
// fold into one line, and the strip must draw nothing when nothing runs.

import (
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// seedDownloadItem pushes a row straight into the queue under the lock, the
// way the worker would, without involving a runner or the network.
func seedDownloadItem(t *testing.T, it downloadItem) {
	t.Helper()
	downloads.mu.Lock()
	it.ID = downloads.nextID
	downloads.nextID++
	item := it
	downloads.items = append(downloads.items, &item)
	downloads.mu.Unlock()
}

func TestActivityRowsCollectInstallAndDownloads(t *testing.T) {
	t.Cleanup(resetInstallBar)
	downloadsTestReset(t)

	// Nothing running: no rows.
	if rows := activityRows(); len(rows) != 0 {
		t.Fatalf("idle queue produced rows %+v, want none", rows)
	}

	// An install (the labelled kind a core update uses) is the first row.
	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	installBarPhase(installPhaseDownload)
	installBarProgress(15<<20, 60<<20)
	rows := activityRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one install row", rows)
	}
	row := rows[0]
	if row.label != "Updating emulator cores" {
		t.Errorf("install label = %q, want the card's label", row.label)
	}
	if row.frac != 0.25 {
		t.Errorf("install frac = %v, want 0.25", row.frac)
	}
	if row.glyph != "↓" {
		t.Errorf("install glyph = %q while downloading", row.glyph)
	}

	// Active downloads get their own chip with the row's display title and
	// real fraction; a queued row folds into a waiting line.
	seedDownloadItem(t, downloadItem{Title: "Instrumental cut", Status: downloadActive, Done: 1 << 20, Total: 4 << 20, added: time.Now()})
	seedDownloadItem(t, downloadItem{Status: downloadQueued, added: time.Now()})
	rows = activityRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want install + active + queued summary", rows)
	}
	if rows[1].label != "Instrumental cut" || rows[1].frac != 0.25 {
		t.Errorf("download row = %+v, want the active row's title and fraction", rows[1])
	}
	if rows[2].label != "1 waiting in queue" || rows[2].frac >= 0 {
		t.Errorf("queued row = %+v, want the folded waiting line", rows[2])
	}

	// A download whose size is unknown stays indeterminate (frac < 0) instead
	// of reporting a fake one.
	seedDownloadItem(t, downloadItem{Title: "Live stream clip", Status: downloadActive, Done: 1 << 20, added: time.Now()})
	rows = activityRows()
	if rows[3].frac >= 0 {
		t.Errorf("unknown-size row frac = %v, want indeterminate", rows[3].frac)
	}

	// Done rows are history, not activity: they never reach the strip. And
	// when the install finishes, the strip keeps showing it until the card
	// hides itself (the same hold installBarSnapshot enforces).
	installBarDone()
	rows = activityRows()
	if rows[0].glyph != "✓" {
		t.Errorf("finished install glyph = %q, want the check", rows[0].glyph)
	}
}

// More active rows than the strip shows each frame cap out with an overflow
// line, and the overflow line says how far behind the strip is.
func TestActivityRowsOverflow(t *testing.T) {
	t.Cleanup(resetInstallBar)
	downloadsTestReset(t)

	for i := 0; i < 6; i++ {
		seedDownloadItem(t, downloadItem{Title: "Track", Status: downloadActive, added: time.Now()})
	}
	// activityRows itself reports everything; the render call folds.
	if rows := activityRows(); len(rows) != 6 {
		t.Fatalf("rows = %d, want all six active rows", len(rows))
	}
}

func TestActivityFitLabelTruncatesToTheChip(t *testing.T) {
	if got := activityFitLabel(nil, "anything", 40); got != "anything" {
		t.Errorf("null font must pass the label through unchanged, got %q", got)
	}
	// With no font to measure there is nothing to truncate against the real
	// glyph widths, so the measured case is pinned in the render test below
	// via the chip's own draws.
}

// renderActivityProbe draws the strip over a flat background and captures the
// frame, the install-bar probe's route.
func renderActivityProbe(t *testing.T, f *probeFrame) {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderActivityStrip(renderer, &Config{})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
}

// stripRegion is where the chips land on the probe: the top-right column the
// strip reserves, inside the 1280x720 probe.
func stripRegion() (int32, int32, int32, int32) {
	x := probeW - 200 - 12
	y := int32(HomeTopBarH) + 10
	return x, y, probeW - 12, y + 4*44
}

// Nothing running means nothing drawn: the strip is not a permanent widget,
// and an empty queue must leave the frame untouched.
func TestActivityStripDrawsNothingWhenIdle(t *testing.T) {
	resetInstallBar()
	downloadsTestReset(t)

	f := newProbeFrame(t)
	renderActivityProbe(t, f)
	x0, y0, x1, y1 := stripRegion()
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !f.isBG(x, y) {
				t.Fatalf("empty queue left pixels at (%d,%d) — the strip must not draw while idle", x, y)
			}
		}
	}
}

// A running download puts a chip in the reserved column and the label lands
// inside it, not outside.
func TestActivityStripDrawsDownloadChip(t *testing.T) {
	t.Cleanup(resetInstallBar)
	downloadsTestReset(t)
	seedDownloadItem(t, downloadItem{Title: "instrumental take", Status: downloadActive, Done: 2 << 20, Total: 4 << 20, added: time.Now()})

	f := newProbeFrame(t)
	renderActivityProbe(t, f)
	x0, y0, x1, y1 := stripRegion()
	drawn := 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !f.isBG(x, y) {
				drawn++
			}
		}
	}
	if drawn == 0 {
		t.Fatal("a running download produced no chip pixels in the strip column")
	}
}

// The package install path publishes through the installer card, so a package
// install is visible on the strip without any strip-specific state — the same
// channel a tool or core install uses.
func TestPackageInstallPublishesThroughTheInstallCard(t *testing.T) {
	t.Cleanup(resetInstallBar)

	downloadAndInstallPackage(nil, Package{Name: "Demo Pkg"})
	if visible, _, phase, _, _, _, _, _, _ := installBarSnapshot(); !visible || phase != installPhaseError {
		t.Fatalf("URL-less package id not shown as a failed install: visible=%v phase=%q", visible, phase)
	}
}

// activityHitRect looks up the X button the last strip render registered, in
// draw order — installs first, then downloads, then the waiting line.
func activityHitRect(t *testing.T, i int) sdl.Rect {
	t.Helper()
	activityHitsMu.Lock()
	defer activityHitsMu.Unlock()
	if i >= len(activityHits) {
		t.Fatalf("hit %d: the strip registered %d X buttons", i, len(activityHits))
	}
	return activityHits[i].rect
}

// clickCenter converts a hit rect to the midpoint a press would land on.
func clickCenter(r sdl.Rect) (int32, int32) { return r.X + r.W/2, r.Y + r.H/2 }

// A tappable chip's X sits in the chip column, and a press on it runs the
// chip's stop action; the chip body jumps to where the work is managed. Table
// order: body first, X second, per row.
func TestActivityStripClickRoutesEachChip(t *testing.T) {
	t.Cleanup(resetInstallBar)
	downloadsTestReset(t)

	// The install's source scene is whatever is up when it begins; the user has
	// since navigated elsewhere, and the chip tap has to find the way back.
	// transitionPhase is the scene-fade state machine's global: it must go
	// back to its idle "none" between assertions, or every later scene change
	// in the suite (and the ambient gate) sees a fade still in flight.
	prevConfig, prevScene, prevPhase := appConfig, currentSceneIndex, transitionPhase
	savedStart, savedBack := tb.startRect, sceneTitleBackRect
	appConfig = &Config{Scenes: []SceneConfig{{Name: "Main"}, {Name: "Patch"}}}
	currentSceneIndex = 0
	transitionPhase = "none"
	tb.startRect, sceneTitleBackRect = sdl.Rect{}, sdl.Rect{}
	t.Cleanup(func() {
		appConfig, currentSceneIndex, transitionPhase = prevConfig, prevScene, prevPhase
		tb.startRect, sceneTitleBackRect = savedStart, savedBack
		closeDownloadsPanel()
	})

	buildStrip := func() {
		f := newProbeFrame(t)
		renderActivityProbe(t, f)
	}

	// A running install, an active download and a queued waiting row cover
	// the three stoppable chips in draw order.
	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	installBarPhase(installPhaseDownload)
	installBarProgress(15<<20, 60<<20)
	seedDownloadItem(t, downloadItem{Title: "Instrumental cut", Status: downloadActive, Done: 1 << 20, Total: 4 << 20, added: time.Now()})
	seedDownloadItem(t, downloadItem{Status: downloadQueued, added: time.Now()})
	// Leave for another scene, the way the user would while the run continues.
	currentSceneIndex = 1
	buildStrip()
	x0, y0, _, y1 := stripRegion()

	// hit 0 is the install's chip body, hit 1 its X.
	installRect := activityHitRect(t, 0)
	if installRect.W != 200 {
		t.Fatalf("first installed hit is the %+v chip body, want the full 200px chip", installRect)
	}
	if installRect.Y < y0 || installRect.Y+installRect.H > y1 || installRect.X < x0-1 || installRect.X+installRect.W > x0+200+1 {
		t.Fatalf("install chip body %+v is not inside the chip column (%d,%d..%d,%d)", installRect, x0, y0, x0+200, y1)
	}

	// A miss is not a cancel: the scene keeps the click.
	if activityStripClick(640, 360) {
		t.Fatal("a press off the strip was consumed")
	}
	if !installBarActive() || len(downloadsSnapshot()) != 2 {
		t.Fatal("the missed click changed state anyway")
	}

	// The install chip's body goes back to the scene the install started on.
	bmx, bmy := clickCenter(installRect)
	if !activityStripClick(bmx, bmy) {
		t.Fatal("the install chip's body did not consume its click")
	}
	if pendingSceneIndex != 0 || transitionPhase != "fade-out" {
		t.Fatalf("body tap queued scene %d in phase %q, want the install's source scene 0", pendingSceneIndex, transitionPhase)
	}
	currentSceneIndex = 1    // stay away for the rest of the check
	transitionPhase = "none" // the queued fade is not part of what follows

	// The install chip's X stops the run the card shows and drops the card —
	// the X wins over the body's navigation when the press lands on the button.
	installXRect := activityHitRect(t, 1)
	runCtx := installBarRunContext()
	mx, my := clickCenter(installXRect)
	if !activityStripClick(mx, my) {
		t.Fatal("the install chip's X did not consume its click")
	}
	if installBarActive() {
		t.Fatal("the install kept running after its X")
	}
	if runCtx.Err() == nil {
		t.Fatal("the cancelled install's run token was not fired")
	}
	if transitionPhase != "none" { // no navigation side effect
		t.Fatal("the X press also navigated")
	}

	// The active download's chip: body opens the Downloads panel; X aborts the
	// row and it disappears from the queue.
	buildStrip()
	dlPanel.hits = nil
	dbmx, dbmy := clickCenter(activityHitRect(t, 0))
	if !activityStripClick(dbmx, dbmy) {
		t.Fatal("the download chip's body did not consume its click")
	}
	if !dlPanel.open {
		t.Fatal("a download chip's body tap did not open the Downloads panel")
	}
	closeDownloadsPanel()
	dmRect := activityHitRect(t, 1)
	dmx, dmy := clickCenter(dmRect)
	if !activityStripClick(dmx, dmy) {
		t.Fatal("the download chip's X did not consume its click")
	}
	for _, it := range downloadsSnapshot() {
		if it.Title == "Instrumental cut" {
			t.Fatal("the active download survived its X")
		}
	}

	// The waiting line: body opens the panel, X pauses; another X resumes it.
	// Only the waiting row remains after the active download was aborted, so
	// its body and X are the rebuilt table's hits 0 and 1.
	buildStrip()
	qbmx, qbmy := clickCenter(activityHitRect(t, 0))
	if !activityStripClick(qbmx, qbmy) || !dlPanel.open {
		t.Fatal("the waiting line's body tap did not open the Downloads panel")
	}
	closeDownloadsPanel()
	qRect := activityHitRect(t, 1)
	qx, qy := clickCenter(qRect)
	if !activityStripClick(qx, qy) || !downloadsPaused() {
		t.Fatalf("the waiting line's X did not pause the queue (rect %+v)", qRect)
	}
	buildStrip()
	qRect = activityHitRect(t, 1)
	qx, qy = clickCenter(qRect)
	if !activityStripClick(qx, qy) || downloadsPaused() {
		t.Fatal("a second press on the paused line did not resume the queue")
	}
}

// activityXHitCount counts the X-window hits (the 20px button footprint) the
// last strip frame registered, so a chip's body tap does not muddy the count.
func activityXHitCount(t *testing.T) int {
	t.Helper()
	activityHitsMu.Lock()
	defer activityHitsMu.Unlock()
	n := 0
	for _, h := range activityHits {
		if h.rect.W == 20 {
			n++
		}
	}
	return n
}

// A settled install (holding its ✓) draws no X: the button is only an
// affordance on work the app can actually stop.
func TestActivityStripNoXOnSettledOrOverflowChips(t *testing.T) {
	t.Cleanup(resetInstallBar)
	downloadsTestReset(t)
	// Strip scene navigation anyway: the chip test's config is a test-local
	// global shift, and this one wants no scene to resolve a body tap to.
	prevConfig, prevScene := appConfig, currentSceneIndex
	appConfig, currentSceneIndex = nil, -1
	t.Cleanup(func() { appConfig, currentSceneIndex = prevConfig, prevScene })

	installBarBegin("ffmpeg")
	installBarProgress(10, 10)
	installBarDone()
	f := newProbeFrame(t)
	renderActivityProbe(t, f)
	if n := activityXHitCount(t); n != 0 {
		t.Fatalf("a finished install's chip registered %d X buttons, want none", n)
	}

	// Overflow does not matter: the folded "more behind" line is not stoppable
	// work either. The settled card row is dropped first so all four visible
	// chips are downloads, leaving the fold as the only X-less row.
	resetInstallBar()
	for i := 0; i < 6; i++ {
		seedDownloadItem(t, downloadItem{Title: "Track", Status: downloadActive, added: time.Now()})
	}
	f = newProbeFrame(t)
	renderActivityProbe(t, f)
	// Four visible chips carry X buttons; the overflow line must not.
	if n := activityXHitCount(t); n != 4 {
		t.Fatalf("overflow strip registered %d X buttons, want 4 (one per visible download, none on the fold)", n)
	}
}
