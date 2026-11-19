package main

// playshot_cover.go — a game with no cover gets a picture of itself.
//
// Box art comes from the libretro thumbnails (artwork_match.go), the card's own
// Imgs / .res / .media folders and Choose cover; homebrew, hacks, translations
// and oddly named dumps often match none of them and stay a lettered tile.
// Juka, 2026-11-18: "be more lenient with artwork… if there is no artwork take
// a screenshot of the game 2 minutes in and use that". So while a library game
// plays, JukaHub counts the time it really runs (not paused under the in-game
// menu or the assistant, an alarm or the screen-off sleep) and at two minutes
// reads the screen the way Hold Menu: screenshot does — the framebuffer on a
// TrimUI handheld, the game's window on Windows — and keeps it as that game's
// cover:
//
//   - only for a game with no cover at all (downloaded, on the card, or a
//     picture from an earlier session), and once;
//   - the black bars around a game that does not fill the screen are trimmed,
//     the picture is scaled to at most 640×480 and saved as
//     <data>/game-art/.gameplay/<System>/<rom stem>.png — beside the
//     downloaded covers but not one of them, so the box-art fetch still looks
//     for a real cover, and one found later (or picked in Choose cover) takes
//     its place everywhere romCoverPath is asked;
//   - a frame that is black, white or flat (a fade, a GPU that never scans out
//     of fb0) is never kept; nor one read within 30 s of Menu going down
//     (RetroArch's menu, and most emulators', opens on it), nor one a library
//     RetroArch says it is not playing, nor 5 s after a pause or an alarm;
//     until the game has run four minutes, neither is one that did not move in
//     0.7 s (a pause screen) nor one dark nearly all over (a loading line).
//     Each is tried again after 20 s more play, up to eight minutes.
//
// Settings → Games & emulators → Covers from gameplay turns it off
// (gameplayCovers, on by default; pictures already taken stay). The library's
// Clean up covers also takes the pictures of games that are gone and of games
// that have a real cover now (artcleanup.go, playShotSuperseded).

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// playShotKey is the Custom variable behind the Settings switch.
	playShotKey = "gameplayCovers"
	// playShotDirName is the folder under game-art the pictures go in. The
	// leading dot keeps it out of the cleanup's console folders and of file
	// pickers, like .server-index beside it.
	playShotDirName = ".gameplay"
	// playShotAfter is how long a game runs before its picture is taken: past
	// the logos, the title screen and the first menus, into the game itself.
	playShotAfter = 2 * time.Minute
	// playShotRetry is how much more play a frame that was not kept waits.
	playShotRetry = 20 * time.Second
	// playShotStillOK is the play time after which a frame that does not move,
	// or is dark nearly all over, is kept anyway: a puzzle, a card game or a
	// text box can sit still for a long time, a shooter plays on black, and a
	// picture of either beats none.
	playShotStillOK = 4 * time.Minute
	// playShotGiveUp is the play time after which a session stops trying.
	playShotGiveUp = 8 * time.Minute
	// playShotMenuQuiet is how long after Menu went down no picture is read.
	playShotMenuQuiet = 30 * time.Second
	// playShotTickMax is the longest gap between two parked-loop ticks that
	// counts as play; a longer one (the overlay was up, a stall) does not.
	playShotTickMax = time.Second
	// playShotStillGap is how far apart the two reads are that tell a moving
	// game from a still one.
	playShotStillGap = 700 * time.Millisecond
	// playShotSettle is how much play follows a pause, an alarm or JukaHub's
	// overlay before the screen is read again.
	playShotSettle = 5 * time.Second
	// playShotMaxW × playShotMaxH bounds the saved picture (never scaled up):
	// about a downloaded cover's size, so its tile decodes as fast.
	playShotMaxW = 640
	playShotMaxH = 480
	// playShotMinSide is the smallest picture side worth keeping.
	playShotMinSide = 64
	// playShotDark is the channel value at or under which a pixel is black: a
	// bar beside a game that does not fill the screen.
	playShotDark = 16
)

// errPlayShotUnsupported is a device whose screen cannot be read at all: the
// session stops trying instead of retrying every 20 s.
var errPlayShotUnsupported = errors.New("this device's screen cannot be read")

// playShotVerdict is what one try came to.
type playShotVerdict int

const (
	playShotAgain playShotVerdict = iota // not kept: try again after more play
	playShotDone                         // kept, or not wanted for this game: stop
)

// playShot follows the game on screen. Main thread only.
var playShot struct {
	launchAt time.Time     // gaTrack.launchAt of the game followed
	lastTick time.Time     // the previous parked tick
	played   time.Duration // how long the game really ran
	nextAt   time.Duration // the play time of the next try
	done     bool          // nothing more to do for this game
	waiting  bool          // a try is out on its goroutine
	gen      uint64        // the session's number (playShotGen)
	rom      scannedROM    // the game, once known
	title    string
}

// playShotGen numbers the games on screen; the main thread raises it at each
// launch, and a try's goroutine compares it to tell that its game has ended.
var playShotGen atomic.Uint64

// playShotRun is the try on its goroutine: busy while it runs (one at a time,
// whichever game it was for), then its game's number and its verdict.
var playShotRun struct {
	busy    atomic.Bool
	mu      sync.Mutex
	gen     uint64
	verdict playShotVerdict
}

// playShotEnabled reports the Settings switch. It is on unless the user turned
// it off; only an explicit off counts, in any casing (boxArtAutoEnabled's rule).
func playShotEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	v, ok := customValue(config.Variables.Custom, playShotKey)
	if !ok || v == nil {
		return true
	}
	if s, isString := v.(string); isString {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "off", "no", "0":
			return false
		}
		return true
	}
	return coerceBool(v)
}

// ── Where the picture lives ──────────────────────────────────────────────

// playShotPath is where a game's gameplay picture is written, or "".
func playShotPath(rom scannedROM, root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	return romArtworkPath(rom, filepath.Join(root, playShotDirName))
}

// playShotCoverPath is a game's gameplay picture when there is one, else "".
// It reads the folder through artworkLocalCover's cache, so a library scan of
// games without one costs a map lookup each.
func playShotCoverPath(rom scannedROM, root string) string {
	dest := playShotPath(rom, root)
	if dest == "" {
		return ""
	}
	return artworkLocalCover(strings.TrimSuffix(dest, filepath.Ext(dest)))
}

// playShotIsCover reports whether a cover path is a gameplay picture.
func playShotIsCover(path string) bool {
	return path != "" && strings.Contains(filepath.ToSlash(path), "/"+playShotDirName+"/")
}

// playShotSuperseded lists the gameplay pictures of games that have a
// downloaded cover now (<root>/<System>/<stem>.png beside
// <root>/.gameplay/<System>/<stem>.png): the cover shows instead, so the
// picture only takes room. One written in the last artCleanupFresh is left.
// Off the render thread (Clean up covers, artcleanup.go).
func playShotSuperseded(root string, now time.Time) []artCleanupFile {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	base := filepath.Join(root, playShotDirName)
	systems, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []artCleanupFile
	for _, sys := range systems {
		if !sys.IsDir() || strings.HasPrefix(sys.Name(), ".") {
			continue
		}
		covers := artworkDirNames(filepath.Join(root, sys.Name()))
		if len(covers) == 0 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(base, sys.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || strings.HasPrefix(name, ".") || !artCleanupImage(name) {
				continue
			}
			stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
			if _, ok := covers[stem+".png"]; !ok {
				continue
			}
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < artCleanupFresh {
				continue
			}
			out = append(out, artCleanupFile{
				Path:   filepath.Join(base, sys.Name(), name),
				System: sys.Name() + " gameplay",
				Size:   info.Size(),
			})
		}
	}
	return out
}

// playShotForgetDir drops a folder from the cover-folder cache, so the
// picture just written is seen at once rather than after artworkDirCacheTTL.
func playShotForgetDir(dir string) {
	artworkDirCache.Lock()
	delete(artworkDirCache.seen, dir)
	artworkDirCache.Unlock()
}

// ── Following the game ───────────────────────────────────────────────────

// playShotHeld reports the game not playing on screen right now: paused under
// JukaHub's overlay, an alarm or the screen-off sleep, or quitting. Any
// goroutine.
func playShotHeld() bool {
	return gameAssistOverlayActive() || alarmFiringActive() || powerSleepActive() || gmQuitting.Load()
}

// playShotMenuRecent reports Menu gone down within playShotMenuQuiet (the pad
// watcher notes it, gameassist_linux.go). Any goroutine.
func playShotMenuRecent() bool {
	at := gaLink.menuAt.Load()
	if at == 0 {
		return false
	}
	d := powerMonoNow() - time.Duration(at)
	return d >= 0 && d < playShotMenuQuiet
}

// playShotGame is the library game the running launch recorded (gaIdentify's
// test), when it is one whose cover is a game-art picture. Main thread.
func playShotGame() (scannedROM, string, bool) {
	r := recentGames()
	if len(r) == 0 {
		return scannedROM{}, "", false
	}
	if d := r[0].PlayedAt.Sub(gaTrack.launchAt); d < -gaIdentifyFor || d > gaIdentifyFor {
		return scannedROM{}, "", false
	}
	g := r[0]
	path, system := strings.TrimSpace(g.Path), strings.TrimSpace(g.System)
	switch {
	case path == "", system == "", strings.Contains(path, "://"),
		system == androidLibrarySystem, system == html5LibrarySystem, system == builtInLibrarySystem:
		// Android and HTML5 games keep covers of their own, built-ins carry
		// theirs, and a link (a store's game) is no file to name one after.
		return scannedROM{}, "", false
	}
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = gameDisplayTitle(g.Path)
	}
	return scannedROM{Path: g.Path, System: g.System}, title, true
}

// playShotTick runs from the parked main loop while a game owns the screen
// (main.go), after gameAssistParkedTick has followed the launch. Cheap: a few
// comparisons a tick; the screen is read on a goroutine.
func playShotTick(config *Config) {
	if gaTrack.launchAt.IsZero() {
		return
	}
	now := time.Now()
	if !gaTrack.launchAt.Equal(playShot.launchAt) {
		// A new game: start counting.
		playShot.launchAt, playShot.lastTick = gaTrack.launchAt, now
		playShot.played, playShot.nextAt = 0, playShotAfter
		playShot.done, playShot.waiting = false, false
		playShot.gen = playShotGen.Add(1)
		playShot.rom, playShot.title = scannedROM{}, ""
	}
	dt := now.Sub(playShot.lastTick)
	playShot.lastTick = now
	if playShot.waiting {
		if playShotRun.busy.Load() {
			return
		}
		playShot.waiting = false
		playShotRun.mu.Lock()
		gen, verdict := playShotRun.gen, playShotRun.verdict
		playShotRun.mu.Unlock()
		if gen == playShot.gen && verdict == playShotDone {
			playShot.done = true
			return
		}
		playShot.nextAt = playShot.played + playShotRetry
		return
	}
	if playShot.done {
		return
	}
	if dt > 0 && dt <= playShotTickMax && !playShotHeld() {
		playShot.played += dt
	} else if dt > playShotTickMax || playShotHeld() {
		// Just back from a pause, an alarm or the overlay: the screen may still
		// hold JukaHub's picture, and the game a moment to draw over it.
		playShot.nextAt = max(playShot.nextAt, playShot.played+playShotSettle)
	}
	// One try at a time: the last game's may still be finishing.
	if playShot.played < playShot.nextAt || playShotRun.busy.Load() {
		return
	}
	if playShot.played >= playShotGiveUp || !playShotEnabled(config) {
		playShot.done = true
		return
	}
	if playShot.rom.Path == "" {
		rom, title, ok := playShotGame()
		if !ok {
			playShot.done = true
			return
		}
		playShot.rom, playShot.title = rom, title
	}
	if playShotHeld() || playShotMenuRecent() {
		playShot.nextAt = playShot.played + playShotRetry
		return
	}
	root := libraryArtworkRoot()
	if strings.TrimSpace(root) == "" {
		playShot.done = true
		return
	}
	if IsTrimuiDevice() {
		// The framebuffer's turn and the canvas size, for the read.
		gameShotSync(config)
	}
	playShot.waiting = true
	playShotRun.busy.Store(true)
	// A library RetroArch is asked whether it plays (its menu, its pause).
	go playShotTry(playShotAsk{
		gen:    playShot.gen,
		rom:    playShot.rom,
		title:  playShot.title,
		root:   root,
		late:   playShot.played >= playShotStillOK,
		raPort: raLibraryPort(),
	})
}

// playShotAsk is one try's errand, from the main thread to its goroutine.
type playShotAsk struct {
	gen    uint64
	rom    scannedROM
	title  string
	root   string
	late   bool // past playShotStillOK: still and dark frames are kept too
	raPort int  // the library RetroArch's command port, 0 when it is not one
}

// playShotRAPaused reports a library RetroArch that says it is not playing
// (paused, or in a menu that pauses it). One that does not answer is taken as
// playing. Any goroutine.
func playShotRAPaused(port int) bool {
	if port <= 0 {
		return false
	}
	reply, err := askRetroArchNetCmd(port, "GET_STATUS", raCoopAskWait)
	if err != nil {
		return false
	}
	w := raStatusWord(reply)
	return w != "" && w != "PLAYING"
}

// playShotNoteOnce logs why a session's read failed, once per session: a
// device that cannot read the screen would otherwise log every 20 s.
func playShotNoteOnce(gen uint64, format string, args ...any) {
	if playShotNoted.Swap(gen) != gen {
		logInfo("art", format, args...)
	}
}

// playShotNoted is the session whose read failure was logged last.
var playShotNoted atomic.Uint64

// playShotLive reports the try's game still on screen and playing. Any
// goroutine.
func playShotLive(gen uint64) bool {
	return gen == playShotGen.Load() && gameScreenSuspended() && !playShotHeld()
}

// playShotTry is one try on its own goroutine: it records its verdict and
// lets the next try go, whatever happens.
func playShotTry(ask playShotAsk) {
	verdict := playShotAgain
	defer func() {
		if r := recover(); r != nil {
			logWarn("art", "gameplay cover for %s: %v", ask.rom.Path, r)
			verdict = playShotDone
		}
		playShotRun.mu.Lock()
		playShotRun.gen, playShotRun.verdict = ask.gen, verdict
		playShotRun.mu.Unlock()
		playShotRun.busy.Store(false)
	}()
	verdict = playShotTake(ask)
}

// playShotTake reads the screen and keeps it as the game's cover when it is
// a picture worth keeping.
func playShotTake(ask playShotAsk) playShotVerdict {
	gen, rom := ask.gen, ask.rom
	// A cover since the launch (a fetch, the card's own) or a picture from an
	// earlier session — or a game whose cover the user removed
	// (artwork_nocover.go): nothing to do.
	if artworkNoCover(rom, ask.root) || artworkCoverPresent(romCoverPath(rom, ask.root)) {
		return playShotDone
	}
	if _, err := os.Stat(rom.Path); err != nil {
		return playShotDone
	}
	dest := playShotPath(rom, ask.root)
	if dest == "" {
		return playShotDone
	}
	if !playShotLive(gen) || playShotRAPaused(ask.raPort) {
		return playShotAgain
	}
	first, err := playShotGrab()
	if err != nil {
		if errors.Is(err, errPlayShotUnsupported) {
			return playShotDone
		}
		playShotNoteOnce(gen, "gameplay cover for %s: the screen could not be read: %v", rom.Path, err)
		return playShotAgain
	}
	// The game ended, or something came up over it, during the read.
	if !playShotLive(gen) {
		return playShotAgain
	}
	box := playShotTrim(first)
	if box.Dx() < playShotMinSide || box.Dy() < playShotMinSide || playShotDull(first, box, ask.late) {
		return playShotAgain
	}
	if !ask.late {
		time.Sleep(playShotStillGap)
		if !playShotLive(gen) || playShotMenuRecent() || playShotRAPaused(ask.raPort) {
			return playShotAgain
		}
		// A second read that fails keeps the first: it showed a picture.
		if second, err := playShotGrab(); err == nil && playShotLive(gen) && playShotStill(first, second, box) {
			return playShotAgain
		}
	}
	thumb := playShotScale(first, box)
	if thumb == nil {
		return playShotAgain
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, thumb); err != nil {
		logWarn("art", "gameplay cover for %s: %v", rom.Path, err)
		return playShotDone
	}
	if err := gmetaWriteCache(dest, buf.Bytes()); err != nil {
		logWarn("art", "gameplay cover for %s not saved: %v", rom.Path, err)
		return playShotDone
	}
	playShotForgetDir(filepath.Dir(dest))
	logInfo("art", "no cover for %s: kept a %d×%d picture of the game as %s",
		rom.Path, thumb.Rect.Dx(), thumb.Rect.Dy(), dest)
	// Told on the way back: "While you were playing: 1 cover taken from the
	// game".
	noteAwayEvent(awayKindCover, ask.title)
	tbxPost(func() { libraryCoverChanged(rom) })
	return playShotDone
}

// ── Looking at the picture ───────────────────────────────────────────────

// playShotLit reports a pixel brighter than a black bar.
func playShotLit(img *image.RGBA, x, y int) bool {
	i := img.PixOffset(x, y)
	return img.Pix[i] > playShotDark || img.Pix[i+1] > playShotDark || img.Pix[i+2] > playShotDark
}

// playShotTrim is the part of the picture inside the black bars a game that
// does not fill the screen leaves around itself (4:3 on a 16:9 panel, a
// handheld game at a whole-number scale). A row or column is a bar when under
// 1 % of it is lit. Bars come off both sides by the same amount, so a game
// whose own picture is black down one edge keeps it. Empty when the picture
// is black.
func playShotTrim(img *image.RGBA) image.Rectangle {
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return image.Rectangle{}
	}
	rowLit := func(y int) bool {
		lit, n := 0, 0
		for x := b.Min.X; x < b.Max.X; x += 4 {
			n++
			if playShotLit(img, x, y) {
				lit++
			}
		}
		return lit*100 >= n && lit > 0
	}
	colLit := func(x int) bool {
		lit, n := 0, 0
		for y := b.Min.Y; y < b.Max.Y; y += 4 {
			n++
			if playShotLit(img, x, y) {
				lit++
			}
		}
		return lit*100 >= n && lit > 0
	}
	top := 0
	for b.Min.Y+top < b.Max.Y && !rowLit(b.Min.Y+top) {
		top++
	}
	if b.Min.Y+top >= b.Max.Y {
		return image.Rectangle{}
	}
	bottom := 0
	for b.Max.Y-1-bottom > b.Min.Y+top && !rowLit(b.Max.Y-1-bottom) {
		bottom++
	}
	left := 0
	for b.Min.X+left < b.Max.X && !colLit(b.Min.X+left) {
		left++
	}
	right := 0
	for b.Max.X-1-right > b.Min.X+left && !colLit(b.Max.X-1-right) {
		right++
	}
	v, h := min(top, bottom), min(left, right)
	if 2*v >= b.Dy() || 2*h >= b.Dx() {
		return image.Rectangle{}
	}
	return image.Rect(b.Min.X+h, b.Min.Y+v, b.Max.X-h, b.Max.Y-v)
}

// playShotDull reports a picture not worth keeping inside box: nearly white,
// one flat colour or two, or — until late — dark nearly all over (a loading
// line on black, a fade, a GPU that never scans out of the framebuffer). Late
// in the session a dark picture is kept: Pac-Man and most shoot-'em-ups play
// on black. Three colours are enough: a Game Boy has four shades. 40×40
// points are looked at.
func playShotDull(img *image.RGBA, box image.Rectangle, late bool) bool {
	const n = 40
	var sum, sumSq float64
	dark, colours := 0, 0
	var seen [4096]bool
	for sy := 0; sy < n; sy++ {
		y := box.Min.Y + (2*sy+1)*box.Dy()/(2*n)
		for sx := 0; sx < n; sx++ {
			x := box.Min.X + (2*sx+1)*box.Dx()/(2*n)
			i := img.PixOffset(x, y)
			r, g, b := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
			l := float64(299*r+587*g+114*b) / 1000
			sum += l
			sumSq += l * l
			if l < 24 {
				dark++
			}
			if k := r>>4<<8 | g>>4<<4 | b>>4; !seen[k] {
				seen[k] = true
				colours++
			}
		}
	}
	const count = n * n
	mean := sum / count
	variance := sumSq/count - mean*mean
	if mean > 240 || variance < 100 || colours < 3 {
		return true
	}
	return !late && (mean < 16 || dark*10 > count*8)
}

// playShotStill reports two reads of the screen the same inside box: under
// 0.5 % of 48×48 points moved.
func playShotStill(a, b *image.RGBA, box image.Rectangle) bool {
	if a.Bounds() != b.Bounds() || a.Stride != b.Stride {
		return false
	}
	const n = 48
	moved := 0
	for sy := 0; sy < n; sy++ {
		y := box.Min.Y + (2*sy+1)*box.Dy()/(2*n)
		for sx := 0; sx < n; sx++ {
			x := box.Min.X + (2*sx+1)*box.Dx()/(2*n)
			i := a.PixOffset(x, y)
			d := absInt(int(a.Pix[i])-int(b.Pix[i])) + absInt(int(a.Pix[i+1])-int(b.Pix[i+1])) +
				absInt(int(a.Pix[i+2])-int(b.Pix[i+2]))
			if d > 24 {
				moved++
			}
		}
	}
	return moved*200 < n*n
}

// playShotScale is box of img scaled into playShotMaxW × playShotMaxH.
func playShotScale(img *image.RGBA, box image.Rectangle) *image.NRGBA {
	sub, ok := img.SubImage(box).(*image.RGBA)
	if !ok || sub.Rect.Empty() {
		return nil
	}
	return shotThumb(sub.Pix, sub.Stride, sub.Rect.Dx(), sub.Rect.Dy(), playShotMaxW, playShotMaxH)
}
