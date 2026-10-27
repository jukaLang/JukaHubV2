package main

// The number on the Video snaps row: what it counts, when it lands, and that the
// row still fits its slot with the number on it. The point of the count is that
// "On" alone cannot tell a user whether the switch does anything — a card with
// no clips has a switch that changes nothing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapCountReset clears the cached census and, when scan is non-nil, swaps the
// scan seam for the length of one test. The census is package state shared by
// every test that renders Settings → General, so each of these starts and ends
// from "not counted yet" rather than from whatever ran before it.
func snapCountReset(t *testing.T, scan func(root string) (int, int)) {
	t.Helper()
	prevScan := snapClipScan
	snapCountClear()
	if scan != nil {
		snapClipScan = scan
	}
	t.Cleanup(func() {
		snapClipScan = prevScan
		snapCountClear()
	})
}

// snapCountClear drops the cached figure. The fields are cleared in place: the
// census carries its own mutex, so assigning a fresh struct would replace a
// held one.
func snapCountClear() {
	snapClips.mu.Lock()
	snapClips.known = false
	snapClips.withClip = 0
	snapClips.games = 0
	snapClips.at = time.Time{}
	snapClips.scanning = false
	snapClips.mu.Unlock()
}

// snapCountPublish is what a finished scan leaves behind.
func snapCountPublish(withClip, games int) {
	snapClips.mu.Lock()
	snapClips.withClip, snapClips.games = withClip, games
	snapClips.known = true
	snapClips.at = time.Now()
	snapClips.mu.Unlock()
}

// snapCountConfig is a config with the page the row lives on.
func snapCountConfig() *Config {
	config := &Config{Scenes: []SceneConfig{{Name: "SettingsGeneral"}}}
	config.Variables.Custom = map[string]interface{}{}
	ensureSettingsWidgets(config)
	return config
}

// snapCountRowText is the row as the page draws it.
func snapCountRowText(t *testing.T, config *Config) string {
	t.Helper()
	return substituteVars(snapSettingRow(t, config).Text, config.Variables.Custom)
}

// TestCountGamesWithClipsCountsLibraryGames: the number is the games whose clip
// path resolves, not the files under the snap root. A clip left behind for a ROM
// that is no longer on the card must not be counted, and a game with neither
// extension has nothing to play.
func TestCountGamesWithClipsCountsLibraryGames(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		filepath.Join("NES", "A.mp4"),
		filepath.Join("SNES", "C.webm"),
		// Present on the card, but its game is not in the library any more.
		filepath.Join("NES", "Gone.mp4"),
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("clip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	systems := []librarySystem{
		{Name: "NES", Games: []libraryGame{
			{Path: "/roms/nes/A.nes", System: "NES", Title: "A"},
			{Path: "/roms/nes/B.nes", System: "NES", Title: "B"},
		}},
		{Name: "SNES", Games: []libraryGame{
			{Path: "/roms/snes/C.sfc", System: "SNES", Title: "C"},
		}},
	}
	if got := countGamesWithClips(systems, root); got != 2 {
		t.Fatalf("counted %d games with a clip, want A and C", got)
	}
	if got := countGamesWithClips(nil, root); got != 0 {
		t.Fatalf("an empty library counted %d clips, want 0", got)
	}
	// An empty root is what a card without the folder looks like: nothing.
	if got := countGamesWithClips(systems, ""); got != 0 {
		t.Fatalf("an empty snap root counted %d clips, want 0", got)
	}
}

// TestSnapRowReportsTheCount walks the row through the states it can be in: no
// count yet, a count, none at all, and the switch turned off.
func TestSnapRowReportsTheCount(t *testing.T) {
	snapCountReset(t, nil)
	config := snapCountConfig()

	if got := snapCountRowText(t, config); got != "Video snaps · On" {
		t.Fatalf("before the first count the row reads %q", got)
	}

	snapCountPublish(12, 220)
	snapPreviewSyncRow(config)
	if got := snapCountRowText(t, config); got != "Video snaps · On · 12" {
		t.Fatalf("row reads %q after the count landed, want the 12 with a clip", got)
	}

	// None: the case the row exists for. It says so in words rather than as a
	// zero, which reads like a broken figure.
	snapCountPublish(0, 220)
	snapPreviewSyncRow(config)
	if got := snapCountRowText(t, config); got != "Video snaps · On · none" {
		t.Fatalf("an empty card reads %q", got)
	}

	// Off keeps the count: knowing the clips are there while the switch is off
	// is what tells a user the switch is worth turning back on.
	applySnapPreviewEnabled(config, false)
	snapPreviewSyncRow(config)
	if got := snapCountRowText(t, config); got != "Video snaps · Off · none" {
		t.Fatalf("switched off the row reads %q", got)
	}
	snapCountPublish(3, 40)
	snapPreviewSyncRow(config)
	if got := snapCountRowText(t, config); got != "Video snaps · Off · 3" {
		t.Fatalf("switched off with clips the row reads %q", got)
	}
}

// TestSnapCountLandsOffTheRenderPath: asking for a count returns immediately and
// the figure appears on the row once the scan finishes, without the caller
// waiting for the card to be walked.
func TestSnapCountLandsOffTheRenderPath(t *testing.T) {
	snapCountReset(t, func(root string) (int, int) {
		if root != "/snaps" {
			t.Errorf("scan was handed root %q, want the config's own", root)
		}
		return 7, 90
	})
	config := snapCountConfig()
	config.Variables.Custom["gameSnapsDir"] = "/snaps"

	snapClipCountRefresh(snapPreviewRoot(config))
	// The call returns at once; the figure is not there until the scene's task
	// has run.
	waitForSceneWork()
	snapPreviewSyncRow(config)
	if got := snapCountRowText(t, config); got != "Video snaps · On · 7" {
		t.Fatalf("row reads %q after the scan, want the 7 clips it reported", got)
	}
}

// TestSnapCountRefreshReusesAFreshFigure: sitting on the page must not re-walk
// the card on every entry, so a count inside the TTL is reused.
func TestSnapCountRefreshReusesAFreshFigure(t *testing.T) {
	scans := 0
	snapCountReset(t, func(root string) (int, int) {
		scans++
		return 5, 5
	})
	snapClipCountRefresh("/snaps")
	waitForSceneWork()
	if scans != 1 {
		t.Fatalf("the first ask ran %d scans, want 1", scans)
	}
	snapClipCountRefresh("/snaps")
	waitForSceneWork()
	if scans != 1 {
		t.Fatalf("a second ask inside the TTL rescanned (%d scans)", scans)
	}
}

// TestSnapCountRefreshDoesNotStackScans: while one scan is in flight another ask
// is dropped, so a page that is drawn faster than the card can be walked does
// not queue a scan per frame.
func TestSnapCountRefreshDoesNotStackScans(t *testing.T) {
	scans := 0
	snapCountReset(t, func(root string) (int, int) {
		scans++
		return 2, 2
	})
	snapClips.mu.Lock()
	snapClips.scanning = true
	snapClips.mu.Unlock()

	snapClipCountRefresh("/snaps")
	waitForSceneWork()
	if scans != 0 {
		t.Fatalf("%d scans started while one was already in flight", scans)
	}
	if _, _, known := snapClipCount(); known {
		t.Fatal("a scan still marked in flight published a count")
	}
}

// TestSnapRowWithTheCountFitsTheSlot is the layout cost of the new value: the row
// sits in a 296px cell beside Swap A/B, so the widest thing it can say still has
// to fit that cell on every panel the app opens on. A label that overflows is
// clipped by the button, which is how a count becomes unreadable.
func TestSnapRowWithTheCountFitsTheSlot(t *testing.T) {
	config := layoutTestConfig(t)
	states := []struct {
		name  string
		with  int
		total int
		on    bool
		clear bool
	}{
		{"no count yet", 0, 0, true, true},
		{"clips", 12, 220, true, false},
		{"widest count, off", 999, 1200, false, false},
		{"no clips", 0, 220, true, false},
	}
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		withScreen(t, size.w, size.h)
		snapCountReset(t, nil)
		ensureSettingsWidgets(config)
		for _, state := range states {
			snapCountClear()
			if !state.clear {
				snapCountPublish(state.with, state.total)
			}
			applySnapPreviewEnabled(config, state.on)
			snapPreviewSyncRow(config)

			row := snapSettingRow(t, config)
			text := substituteVars(row.Text, config.Variables.Custom)
			// The draw path's own font and budget: a value the button would cut
			// with an ellipsis is a count the user cannot read.
			base, _ := getCachedFont(config, row.Font)
			if base == nil {
				t.Fatalf("no font available: the shipped Inter-Regular.ttf did not load, so this check cannot run")
			}
			font := buttonLabelFont(config, row.Font, row.Text, getElementWidth(*row, 0))
			inner := getElementWidth(*row, 0) - 2*SpaceXS
			if got := ellipsize(font, text, inner); got != text {
				t.Errorf("%dx%d, %s: %q is clipped to %q in %dpx", size.w, size.h, state.name, text, got, inner)
			}
		}
	}
}

// TestSnapCountReachesTheDrawnRow is the wiring: drawing Settings → General is
// what puts the figure on the row, so a page that is already up when the scan
// finishes shows the number without being reopened.
func TestSnapCountReachesTheDrawnRow(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	snapCountReset(t, nil)
	config := widgetRenderConfig(t)
	snapCountPublish(4, 60)

	renderNamedScene(t, config, "SettingsGeneral")
	if got, _ := config.Variables.Custom[snapPreviewLabelKey].(string); got != "On · 4" {
		t.Fatalf("after drawing the page the row's value is %q, want the count", got)
	}
	if got := snapCountRowText(t, config); got != "Video snaps · On · 4" {
		t.Fatalf("row reads %q, want the 4 games with a clip", got)
	}
}

// TestSnapCountUsesTheSameClipPathAsThePreview keeps the figure honest: the count
// and the library's preview must agree on what a clip is, so a row saying "3" can
// never sit beside a preview that finds nothing to play.
func TestSnapCountUsesTheSameClipPathAsThePreview(t *testing.T) {
	root := t.TempDir()
	game := libraryGame{Path: "/roms/nes/A.nes", System: "NES", Title: "A"}
	for _, ext := range snapExtensions {
		path := filepath.Join(root, "NES", "A"+ext)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("clip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One game, two formats: the count is games, not files.
	if got := countGamesWithClips([]librarySystem{{Name: "NES", Games: []libraryGame{game}}}, root); got != 1 {
		t.Fatalf("counted %d, want 1 game", got)
	}
	if !artworkCoverPresent(gameSnapPath(game, root)) {
		t.Fatal("the counted game has no clip at the preview's own path")
	}
}

// TestSnapCountShowsInTheLibraryHeader is the request: the figure Settings
// reports is on the Games Library's own header line, where the previews
// actually play — same census, same number — and nothing is on the line until
// the first scan lands, exactly the row's rule.
func TestSnapCountShowsInTheLibraryHeader(t *testing.T) {
	snapCountReset(t, nil)
	prevPanel := gameLib
	t.Cleanup(func() { gameLib = prevPanel })
	gameLib = gameLibraryPanel{
		open: true,
		lib: gameLibraryReport{
			Systems: gameSearchFixture()[1:],
			Total:   90,
			WithArt: 60,
		},
	}
	config := snapCountConfig()

	if status := gameLibraryStatus(); strings.Contains(status, "clip") {
		t.Fatalf("before the first count the header says %q", status)
	}

	snapCountPublish(5, 90)
	snapPreviewSyncRow(config)
	status := gameLibraryStatus()
	if !strings.Contains(status, " · 5 clip(s)") {
		t.Fatalf("header = %q, want the 5 clip(s) figure", status)
	}
	if got := snapCountRowText(t, config); !strings.HasSuffix(got, "· 5") {
		t.Fatalf("row reads %q; the header and the row must report the same figure", got)
	}

	// A card with none says so in words, like the row's "none".
	snapCountPublish(0, 90)
	snapPreviewSyncRow(config)
	if status := gameLibraryStatus(); !strings.Contains(status, " · no clips") {
		t.Fatalf("empty card header = %q, want the no-clips line", status)
	}
}

// TestOpenGameLibraryPanelAsksForTheClipCount: the header's figure comes from
// somewhere, and opening the library is that moment — the ask returns at once
// and the census fills in when the scene-work scan finishes.
func TestOpenGameLibraryPanelAsksForTheClipCount(t *testing.T) {
	scans := 0
	snapCountReset(t, func(root string) (int, int) {
		scans++
		return 6, 30
	})
	prevPanel := gameLib
	prevQS, prevEmu, prevTools := qs.open, emuPanel.open, toolsPanel.open
	t.Cleanup(func() {
		gameLib = prevPanel
		qs.open, emuPanel.open, toolsPanel.open = prevQS, prevEmu, prevTools
		closeGameLibraryArt()
	})
	// The open path closes the art cache; keep it from touching textures a
	// render probe may already have handed back.
	gameLib.artCache = nil

	openGameLibraryPanel()
	waitForSceneWork()
	if scans != 1 {
		t.Fatalf("opening the library ran %d clip scans, want 1", scans)
	}
	withClip, _, known := snapClipCount()
	if !known || withClip != 6 {
		t.Fatalf("census = (%d, known=%v), want the scan's 6 once known", withClip, known)
	}
	if !gameLib.open {
		t.Fatal("the panel did not open")
	}
}
