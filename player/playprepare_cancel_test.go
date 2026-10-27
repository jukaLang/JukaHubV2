package main

// playprepare_cancel_test.go — the Play press is one loading state from the
// first frame, and B backs out of all of it.
//
// The regression this pins: on a cold card the press spent its first seconds
// downloading yt-dlp with the install card up, but the modal state only began
// after the tools were resolved. The results grid stayed live under the spinner
// for the whole download, and there was no way to stop it — the loading card
// had no Cancel button either, just a line of grey text.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// TestDownloadStopsWhenCancelled covers the half of the fix the card cannot see:
// B during a cold start has to stop the transfer, not just hide it. The server
// streams slowly forever, so a download that ignores the context would hang.
func TestDownloadStopsWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		// One chunk, then hold the connection open until the test ends.
		_, _ = w.Write(make([]byte, 8192))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp.part")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- downloadFileWithProgressCtx(ctx, srv.URL, dest, nil) }()

	// Let the first chunk land so the download is really in flight.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the download never started, so the cancel path was not exercised")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled download reported success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled download returned %v, want a context.Canceled cause", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download kept running after its context was cancelled")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a cancelled download left its partial file behind")
	}
}

// TestEnsureHelperToolCtxHonoursAnAlreadyCancelledContext is the cheap half of
// the same contract: a prepare the user already cancelled must not begin a
// download at all (and must not put the install card up).
func TestEnsureHelperToolCtxHonoursAnAlreadyCancelledContext(t *testing.T) {
	installBarAbort()
	t.Cleanup(installBarAbort)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ensureHelperToolCtx(ctx, "yt-dlp", &Config{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureHelperToolCtx returned %v, want context.Canceled", err)
	}
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		t.Fatal("a cancelled install put the install card up")
	}
}

// TestInstallBarAbortHidesTheCard is what B does to the install card: it goes
// away at once instead of sitting there as a paused progress bar.
func TestInstallBarAbortHidesTheCard(t *testing.T) {
	t.Cleanup(installBarAbort)
	installBarBegin("yt-dlp")
	installBarPhase(installPhaseDownload)
	installBarProgress(4096, 1<<20)
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); !visible {
		t.Fatal("installBarBegin did not show the card, so this check proves nothing")
	}

	installBarAbort()

	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		t.Fatal("installBarAbort left the install card on screen")
	}
	// A late progress callback from the dying download must not resurrect it.
	installBarProgress(8192, 1<<20)
	if visible, _, _, _, _, _, _, _, _ := installBarSnapshot(); visible {
		t.Fatal("a progress tick after the abort brought the card back")
	}
}

// TestLoadingCancelButtonFitsTheCard pins the button's geometry on the handheld
// canvases as well as the desktop one: the B badge, its label and the pill all
// stay inside the card and below the slogan block.
func TestLoadingCancelButtonFitsTheCard(t *testing.T) {
	_ = newProbeFrame(t)
	t.Cleanup(ClearTextCache)
	for _, screen := range []struct {
		name string
		w, h int32
	}{
		{"TrimUI Smart Pro", 1280, 720},
		{"TrimUI Brick", 960, 544},
		{"small", 640, 480},
	} {
		withScreen(t, screen.w, screen.h)
		config := &Config{}
		font, _ := getCachedFont(config, "small")
		if font == nil {
			t.Fatalf("%s: no font available, so the layout cannot be measured", screen.name)
		}
		cardW, cardH := loadingCardSize()
		cardX := (screenWidth - cardW) / 2
		cardY := (screenHeight - cardH) / 2
		y := cardY + cardH - 36

		pill, labelX, labelY := loadingCancelLayout(font, cardX, cardW, y)
		if pill.H != loadingCancelPillH {
			t.Errorf("%s: pill height %d, want %d", screen.name, pill.H, loadingCancelPillH)
		}
		if pill.X < cardX || pill.X+pill.W > cardX+cardW {
			t.Errorf("%s: pill x %d..%d escapes the card %d..%d",
				screen.name, pill.X, pill.X+pill.W, cardX, cardX+cardW)
		}
		if pill.Y < cardY+160+int32(loadingSloganMaxLines)*loadingSloganLineH {
			t.Errorf("%s: pill top %d overlaps the slogan block ending at %d",
				screen.name, pill.Y, cardY+160+int32(loadingSloganMaxLines)*loadingSloganLineH)
		}
		if pill.Y+pill.H > cardY+cardH {
			t.Errorf("%s: pill bottom %d runs past the card bottom %d",
				screen.name, pill.Y+pill.H, cardY+cardH)
		}
		labelW, labelH, _ := font.SizeUTF8(loadingCancelLabel)
		if labelX+int32(labelW) > cardX+cardW {
			t.Errorf("%s: Cancel label ends at %d, past the card's %d",
				screen.name, labelX+int32(labelW), cardX+cardW)
		}
		if labelY+int32(labelH) > cardY+cardH {
			t.Errorf("%s: Cancel label bottom %d runs past the card bottom %d",
				screen.name, labelY+int32(labelH), cardY+cardH)
		}
	}
}

// TestResolveAndStartPlaybackStopsOnACancelledLoad covers the one load path both
// Play and the quality switch go through: a cancel has to be noticed before a
// tool is resolved (or downloaded), and it must leave no card behind and report
// that nothing played.
func TestResolveAndStartPlaybackStopsOnACancelledLoad(t *testing.T) {
	cancelVideoPrepare()
	t.Cleanup(func() { cancelVideoPrepare() })

	seq, ctx := beginVideoPrepare("A clip")
	// What B does: the card goes away and the resolution context is done.
	if !cancelVideoPrepare() {
		t.Fatal("cancel reported nothing in flight")
	}

	if played := resolveAndStartPlayback(seq, ctx, &Config{}, "https://example.com/v", 0, "test"); played {
		t.Fatal("a cancelled load reported that something played")
	}
	if videoPrepareActive() {
		t.Fatal("a cancelled load left the loading card up")
	}
}

// TestLoadingCancelButtonDrawsItsBadgeAndLabel draws the button on its own: the
// B badge sits in a filled pill and the word Cancel is painted beside it, so the
// way out of the wait is visible rather than only documented.
func TestLoadingCancelButtonDrawsItsBadgeAndLabel(t *testing.T) {
	_ = newProbeFrame(t)
	withScreen(t, 1280, 720)
	t.Cleanup(ClearTextCache)

	config := &Config{}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("no font available, so the button cannot be drawn")
	}
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(r)

	cardW, cardH := loadingCardSize()
	cardX := (screenWidth - cardW) / 2
	cardY := (screenHeight - cardH) / 2
	y := cardY + cardH - 36
	pill, labelX, labelY := loadingCancelLayout(font, cardX, cardW, y)

	r.SetDrawColor(0, 0, 0, 255)
	r.Clear()
	renderLoadingCancelButton(r, config, cardX, cardW, y, nil)

	buf := make([]byte, probeW*probeH*4)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed: %v", err)
	}
	at := func(x, yy int32) (b, g, rr uint8) {
		i := (yy*probeW + x) * 4
		return buf[i], buf[i+1], buf[i+2]
	}

	// The pill's fill: sample the middle of its left padding, away from the
	// glyph, and require the fill colour the theme defines.
	fill := ColorIconSurface
	if b, g, rr := at(pill.X+3, pill.Y+pill.H/2); b != fill.B || g != fill.G || rr != fill.R {
		t.Fatalf("the badge pill is not filled with the icon surface: got (%d,%d,%d), want (%d,%d,%d)",
			rr, g, b, fill.R, fill.G, fill.B)
	}

	// The label: count glyph-core pixels of the secondary text colour in the
	// label's box. An empty box means the word Cancel was never drawn.
	label := ColorTextSecondary()
	lw, lh, _ := font.SizeUTF8(loadingCancelLabel)
	glyphs := 0
	for yy := labelY; yy < labelY+int32(lh); yy++ {
		for x := labelX; x < labelX+int32(lw); x++ {
			if b, g, rr := at(x, yy); b == label.B && g == label.G && rr == label.R {
				glyphs++
			}
		}
	}
	if glyphs < 20 {
		t.Fatalf("only %d label pixels found, so the Cancel label was not drawn", glyphs)
	}
}
