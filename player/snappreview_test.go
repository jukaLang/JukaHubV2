package main

// Tests for the Games Library video-snap preview: where a clip is looked for,
// when the decoder is allowed to start, and every way it must stop.
//
// No ffmpeg runs here. snapStartDecoder is the seam: the fake records what was
// started and blocks until it is cancelled, which is exactly the contract the
// panel relies on (one decoder per focused game, none after the focus moves).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// snapFake stands in for the decode loop.
type snapFake struct {
	mu        sync.Mutex
	starts    []string
	cancelled int
}

func (f *snapFake) run(ctx context.Context, path string, config *Config) {
	f.mu.Lock()
	f.starts = append(f.starts, path)
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.cancelled++
	f.mu.Unlock()
}

func (f *snapFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *snapFake) cancels() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled
}

func (f *snapFake) started(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.starts) {
		return ""
	}
	return f.starts[i]
}

// installSnapFake swaps the decoder seam for a fake.
func installSnapFake(t *testing.T) *snapFake {
	t.Helper()
	f := &snapFake{}
	prevStart := snapStartDecoder
	snapStartDecoder = f.run
	t.Cleanup(func() { snapStartDecoder = prevStart })
	return f
}

// snapTestSetup points the preview at a temp card: a fake library open in the
// panel, a temp snap root with the given clips, and a config the tests own.
// snaps maps a ROM stem to the extension it gets.
func snapTestSetup(t *testing.T, games []libraryGame, snaps map[string]string) string {
	t.Helper()
	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	t.Cleanup(func() { appConfig = prevConfig })

	root := t.TempDir()
	prevRoot := librarySnapRoot
	librarySnapRoot = func() string { return root }
	t.Cleanup(func() { librarySnapRoot = prevRoot })

	for stem, ext := range snaps {
		dir := filepath.Join(root, "NES")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create snap dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, stem+ext), []byte("not a real clip"), 0o644); err != nil {
			t.Fatalf("write snap: %v", err)
		}
	}

	prevPanel := gameLib
	gameLib = gameLibraryPanel{
		open:   true,
		lib:    gameLibraryReport{Systems: []librarySystem{{Name: "NES", Games: games}}},
		system: 0,
		cursor: 0,
	}
	t.Cleanup(func() {
		gameLib = prevPanel
		snapPreviewReset()
	})
	snapPreviewReset()
	return root
}

// waitForSnap polls the fake until cond is true, so a goroutine start is not a
// race against the assertion.
func waitForSnap(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func snapGame(path, title string) libraryGame {
	return libraryGame{Path: path, System: "NES", Title: title}
}

// TestGameSnapPathMirrorsTheBoxArtTree pins the layout users are told to copy
// into: the console folder with underscores, then the ROM stem.
func TestGameSnapPathMirrorsTheBoxArtTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Nintendo_-_NES")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Zelda II.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	game := libraryGame{Path: "/roms/Nintendo - NES/Zelda II.nes", System: "Nintendo - NES", Title: "Zelda II"}
	got := gameSnapPath(game, root)
	want := filepath.Join(dir, "Zelda II.mp4")
	if got != want {
		t.Fatalf("gameSnapPath = %q, want %q", got, want)
	}

	// No clip for the system, an empty root or a nameless system: no preview.
	if got := gameSnapPath(libraryGame{Path: "/roms/GBA/Pokemon.gba", System: "GBA"}, root); got != "" {
		t.Fatalf("missing clip = %q, want none", got)
	}
	if got := gameSnapPath(game, ""); got != "" {
		t.Fatalf("empty root = %q, want none", got)
	}
	if got := gameSnapPath(libraryGame{Path: "/roms/X/a.nes"}, root); got != "" {
		t.Fatalf("game without a system = %q, want none", got)
	}
}

// TestGameSnapPathPrefersMP4OverWebM: the first extension in the list wins, so
// a collection that has both is deterministic.
func TestGameSnapPathPrefersMP4OverWebM(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "NES")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, ext := range []string{".webm", ".mp4"} {
		if err := os.WriteFile(filepath.Join(dir, "Game"+ext), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", ext, err)
		}
	}
	if got := gameSnapPath(snapGame("/roms/nes/Game.nes", "Game"), root); !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("chosen snap = %q, want the mp4", got)
	}
	// A directory is not a clip.
	if err := os.Remove(filepath.Join(dir, "Game.mp4")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "Game.mp4"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := gameSnapPath(snapGame("/roms/nes/Game.nes", "Game"), root); !strings.HasSuffix(got, ".webm") {
		t.Fatalf("a directory named .mp4 was treated as a clip: %q", got)
	}
}

// TestSnapDecoderArgsLoopAndStaySilent is the contract of the child process:
// loop forever, no audio, pre-scaled, raw planes on stdout.
func TestSnapDecoderArgsLoopAndStaySilent(t *testing.T) {
	path := "/roms/nes/My Game.mp4"
	args := snapDecoderArgs(path, 320, 180, 10)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-stream_loop -1",
		"-an",
		"-vf scale=320:180:flags=bilinear,fps=10",
		"-f rawvideo",
		"-pix_fmt yuv420p",
		"- ",
	} {
		if !strings.Contains(joined+" ", want) {
			t.Errorf("args %q are missing %q", joined, want)
		}
	}
	// The path must be its own argument: a name with spaces cannot be split by a
	// shell that is not there.
	found := false
	for i, a := range args {
		if a == path && i > 0 && args[i-1] == "-i" {
			found = true
		}
	}
	if !found {
		t.Fatalf("path is not an -i argument: %v", args)
	}
	if strings.Contains(joined, " -ss ") {
		t.Error("a preview always starts at the beginning; a seek flag is a bug")
	}
}

// TestSnapPreviewArmsOnlyAfterTheFocusRests is the guard against a process per
// scrolled row: nothing starts until the same game has been focused for the
// arm delay, and then only once.
func TestSnapPreviewArmsOnlyAfterTheFocusRests(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, []libraryGame{snapGame("/roms/nes/A.nes", "A")}, map[string]string{"A": ".mp4"})
	base := time.Now()

	snapPreviewSync(base)
	if got := fake.count(); got != 0 {
		t.Fatalf("a decoder started on the frame the focus landed (%d starts)", got)
	}
	snapPreviewSync(base.Add(100 * time.Millisecond))
	if got := fake.count(); got != 0 {
		t.Fatalf("a decoder started before the arm delay (%d starts)", got)
	}

	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 1 })

	// Frames keep coming while the focus stays put: no second decoder.
	snapPreviewSync(base.Add(2 * time.Second))
	if got := fake.count(); got != 1 {
		t.Fatalf("staying on one game started %d decoders, want 1", got)
	}
	if got := fake.started(0); !strings.HasSuffix(got, "A.mp4") {
		t.Fatalf("started %q, want the game's snap", got)
	}
}

// TestSnapPreviewRestartsWhenTheFocusMoves: the old decoder is cancelled before
// the new one is armed, never left running beside it.
func TestSnapPreviewRestartsWhenTheFocusMoves(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, []libraryGame{
		snapGame("/roms/nes/A.nes", "A"),
		snapGame("/roms/nes/B.nes", "B"),
	}, map[string]string{"A": ".mp4", "B": ".webm"})
	base := time.Now()

	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 1 })

	gameLib.cursor = 1
	moved := base.Add(500 * time.Millisecond)
	snapPreviewSync(moved)
	waitForSnap(t, func() bool { return fake.cancels() == 1 })
	if got := fake.count(); got != 1 {
		t.Fatalf("moving the focus started a second decoder before the delay (%d)", got)
	}

	snapPreviewSync(moved.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 2 })
	if got := fake.started(1); !strings.HasSuffix(got, "B.webm") {
		t.Fatalf("second decoder = %q, want B.webm", got)
	}
}

// TestSnapPreviewRespectsTheSwitch: gameSnaps:false means no process at all,
// and it stops one that is already running.
func TestSnapPreviewRespectsTheSwitch(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, []libraryGame{snapGame("/roms/nes/A.nes", "A")}, map[string]string{"A": ".mp4"})
	base := time.Now()

	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 1 })

	appConfig.Variables.Custom["gameSnaps"] = false
	snapPreviewSync(base.Add(time.Second))
	waitForSnap(t, func() bool { return fake.cancels() == 1 })

	snapPreviewSync(base.Add(2 * time.Second))
	if got := fake.count(); got != 1 {
		t.Fatalf("the switch off still started a decoder (%d starts)", got)
	}
	if got := snapPreviewStatus(); got != "idle" {
		t.Fatalf("status with the switch off = %q, want idle", got)
	}
}

// TestSnapPreviewStopsWithThePanel: closing the library ends the decoder, the
// same way it frees the box-art textures.
func TestSnapPreviewStopsWithThePanel(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, []libraryGame{snapGame("/roms/nes/A.nes", "A")}, map[string]string{"A": ".mp4"})
	base := time.Now()

	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	waitForSnap(t, func() bool { return fake.count() == 1 })

	gameLib.open = false
	snapPreviewSync(base.Add(time.Second))
	waitForSnap(t, func() bool { return fake.cancels() == 1 })
}

// TestSnapPreviewBrokenClipIsNotRetried: a clip ffmpeg cannot decode is
// remembered for the session. Restarting it on every frame is a process storm
// on a device with one core to spare.
func TestSnapPreviewBrokenClipIsNotRetried(t *testing.T) {
	prevStart := snapStartDecoder
	var mu sync.Mutex
	starts, finished := 0, 0
	snapStartDecoder = func(ctx context.Context, path string, config *Config) {
		mu.Lock()
		starts++
		mu.Unlock()
		// What snapPreviewRun does when the pipe yields no frame.
		snapPreviewMarkBroken(path)
		mu.Lock()
		finished++
		mu.Unlock()
	}
	t.Cleanup(func() { snapStartDecoder = prevStart })

	snapTestSetup(t, []libraryGame{snapGame("/roms/nes/A.nes", "A")}, map[string]string{"A": ".mp4"})
	base := time.Now()
	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))

	// Wait for the decoder to finish, not just to start. A broken clip is only
	// guarded by the memo once snapPreviewMarkBroken has run, so asserting
	// before that point would pass even when the retry does happen — that timing
	// window is exactly what made this test flaky. The remaining frames here
	// also exercise the other order: the decoder is armed (starting) while they
	// run, so it must not be restarted from that state either.
	waitForSnap(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return finished == 1
	})

	clip := snapPreviewSnapPath(t)
	for i := 1; i <= 3; i++ {
		snapPreviewSync(base.Add(time.Duration(i) * time.Second))
		// Arming a decoder writes the state synchronously, so this reports a
		// retry the moment it is queued. Counting goroutines instead is what
		// made the test flaky in both directions: the arm could land after the
		// final assertion (a silent pass) or its goroutine could run first (a
		// spurious failure).
		if got := snapPreviewStatus(); got != "waiting for a snap" {
			t.Fatalf("frame %d left the preview %q after a broken clip", i, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("a broken clip was retried %d times", starts-1)
	}

	// And the reason it is not retried is the session memo, keyed by the clip the
	// decoder was handed. Written under one key and read under another, the memo
	// never matched and the retry above was the result.
	snapState.mu.Lock()
	marked := snapState.broken[clip]
	snapState.mu.Unlock()
	if !marked {
		t.Fatalf("the broken clip %q was not remembered this session", clip)
	}
}

// snapPreviewSnapPath is the clip the panel resolved for the focused game.
func snapPreviewSnapPath(t *testing.T) string {
	t.Helper()
	snapState.mu.Lock()
	defer snapState.mu.Unlock()
	if snapState.snap == "" {
		t.Fatal("the panel resolved no clip for the focused game")
	}
	return snapState.snap
}

// TestSnapPreviewNeedsAFullyResolvedFocus: an empty library (or a search with no
// hits) never arms anything.
func TestSnapPreviewNeedsAFullyResolvedFocus(t *testing.T) {
	fake := installSnapFake(t)
	snapTestSetup(t, nil, nil)
	base := time.Now()

	snapPreviewSync(base)
	snapPreviewSync(base.Add(snapPreviewArmDelay + 50*time.Millisecond))
	if got := fake.count(); got != 0 {
		t.Fatalf("an empty library started %d decoders", got)
	}
	if got := snapPreviewStatus(); got != "idle" {
		t.Fatalf("status with no game = %q", got)
	}
}

// TestSnapPreviewDirOverride: gameSnapsDir wins over the app data folder, so a
// large collection can live on the card's root.
func TestSnapPreviewDirOverride(t *testing.T) {
	snapTestSetup(t, nil, nil)
	custom := t.TempDir()
	if got := snapPreviewRoot(appConfig); got != librarySnapRoot() {
		t.Fatalf("default root = %q, want the app data folder", got)
	}
	appConfig.Variables.Custom["gameSnapsDir"] = "  " + custom + "  "
	if got := snapPreviewRoot(appConfig); got != custom {
		t.Fatalf("override root = %q, want %q", got, custom)
	}
}
