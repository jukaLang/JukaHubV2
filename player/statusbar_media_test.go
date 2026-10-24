package main

// The top bar advertises what is playing: the title plus a headphone glyph in
// the system corner, drawn by the same cluster code both headers use.

import (
	"math"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// pinMusicState runs the audio player through a known state for one test. An
// empty title means nothing is playing; a title means it is playing, not
// paused.
func pinMusicState(t *testing.T, title string) {
	t.Helper()
	music.mu.Lock()
	prevTracks, prevCurrent, prevPlaying, prevPaused := music.tracks, music.current, music.playing, music.paused
	if title == "" {
		music.playing = false
	} else {
		music.tracks = []musicTrack{{Name: title}}
		music.current = 0
		music.playing = true
	}
	music.paused = false
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks, music.current, music.playing = prevTracks, prevCurrent, prevPlaying
		music.paused = prevPaused
		music.mu.Unlock()
	})
}

// pinMusicPaused holds the audio player's pause flag for one test, so a paused
// chip never leaks into another test's render.
func pinMusicPaused(t *testing.T, paused bool) {
	t.Helper()
	music.mu.Lock()
	prev := music.paused
	music.paused = paused
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.paused = prev
		music.mu.Unlock()
	})
}

// pinMusicProgress holds the audio player's position and length for one test.
func pinMusicProgress(t *testing.T, position, duration float64) {
	t.Helper()
	music.mu.Lock()
	prevPosition, prevDuration := music.position, music.duration
	music.position, music.duration = position, duration
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.position, music.duration = prevPosition, prevDuration
		music.mu.Unlock()
	})
}

// pinVideoProgress holds the embedded player's progress and length for one
// test.
func pinVideoProgress(t *testing.T, progress, duration float64) {
	t.Helper()
	embeddedPlayer.mu.Lock()
	prevProgress, prevDuration := embeddedPlayer.progress, embeddedPlayer.duration
	embeddedPlayer.progress, embeddedPlayer.duration = progress, duration
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.progress, embeddedPlayer.duration = prevProgress, prevDuration
		embeddedPlayer.mu.Unlock()
	})
}

// pinVideoState holds the embedded player in `phase` and remembers `page` as
// the session's page URL.
func pinVideoState(t *testing.T, phase, page string) {
	t.Helper()
	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = phase
	embeddedPlayer.mu.Unlock()
	prevPage := currentVideoPage()
	rememberVideoPage(page)
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.mu.Unlock()
		rememberVideoPage(prevPage)
	})
}

func TestStatusMediaPartNamesWhatIsPlaying(t *testing.T) {
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	pinAudioOutput(t, "")
	if part, ok := statusMediaPart(); ok {
		t.Fatalf("nothing is playing, but the bar advertises %q", part)
	}

	pinMusicState(t, "Low Tide Radio")
	part, ok := statusMediaPart()
	if !ok {
		t.Fatal("music is playing but the bar has no now-playing part")
	}
	if !isMediaStatusPart(part) || statusMediaTitle(part) != "Low Tide Radio" {
		t.Fatalf("music part = %q, want the track title behind the marker", part)
	}
}

// A video's real title lives in the queue it was opened from; a stream or a
// pasted link still gets a readable name rather than an empty slot.
func TestVideoTitleComesFromTheQueueThenTheURL(t *testing.T) {
	prevQueue := playbackQueue
	t.Cleanup(func() { playbackQueue = prevQueue })

	page := "https://videos.example/watch/abc"
	playbackQueue = playbackQueueState{
		videos: []VideoInfo{{URL: page, Title: "Night Drive Theory"}},
		index:  0,
	}
	if got := videoTitleForPage(page); got != "Night Drive Theory" {
		t.Fatalf("queue title = %q, want %q", got, "Night Drive Theory")
	}

	// No matching queue entry: the link itself is the only name available.
	playbackQueue = playbackQueueState{index: -1}
	if got := videoTitleForPage("https://videos.example/watch/My-Clip-1.mp4"); got != "My Clip 1" {
		t.Fatalf("url title = %q, want %q", got, "My Clip 1")
	}
	if got := videoTitleForPage("https://www.youtube.com/watch?v=abc"); got != "youtube.com" {
		t.Fatalf("host title = %q, want the host without www.", got)
	}
	if got := videoTitleForPage(""); got != "" {
		t.Fatalf("empty page produced a title %q", got)
	}
}

func TestHeaderStatusPartsAdvertiseNowPlaying(t *testing.T) {
	pinStatusValues(t, 87, 3)
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")

	parts := headerStatusParts(&Config{})
	if isMediaStatusPart(parts[len(parts)-1]) {
		t.Fatalf("idle cluster ends with a now-playing part: %v", parts)
	}

	pinMusicState(t, "Low Tide Radio")
	parts = headerStatusParts(&Config{})
	last := parts[len(parts)-1]
	if !isMediaStatusPart(last) || statusMediaTitle(last) != "Low Tide Radio" {
		t.Fatalf("playing cluster = %v, want a now-playing part last", parts)
	}
}

// The title is the first part to give way when the bar is crowded: the network,
// clock and battery readings are what the corner exists for.
func TestFitStatusPartsDropsNowPlayingFirst(t *testing.T) {
	width := func(p string) int32 { return statusClusterPartWidth(nil, p) }
	media := statusMediaPartPrefix + "Night Drive Theory"
	parts := []string{statusWiFiMarker, "12:40", "87%", media}
	const gap = int32(10)

	keep := parts[:len(parts)-1]
	maxW := int32(0)
	for i, p := range keep {
		if i > 0 {
			maxW += gap
		}
		maxW += width(p)
	}
	fitted := fitStatusParts(append([]string(nil), parts...), width, gap, maxW)
	if len(fitted) != len(keep) {
		t.Fatalf("fitted cluster = %v, want exactly %v", fitted, keep)
	}
	for _, p := range fitted {
		if isMediaStatusPart(p) {
			t.Fatalf("crowded cluster kept the now-playing part: %v", fitted)
		}
	}
}

func TestStatusMediaChipDrawsHeadphonesAndTitle(t *testing.T) {
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}
	part := statusMediaPartPrefix + "Low Tide Radio"
	x, y := int32(400), int32(20)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	drawn := drawStatusMediaChip(renderer, font, part, x, y, func(text string, col sdl.Color, tx, ty int32) {
		renderText(renderer, &Config{}, font, text, col, tx, ty)
	})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	if want := statusClusterPartWidth(font, part); drawn != want {
		t.Fatalf("chip drew %dpx but measured %dpx", drawn, want)
	}

	iconH := statusIconHeight(font)
	iconW := statusMediaIconWidth(iconH)
	icon := sdl.Rect{X: x, Y: y + (int32(font.Height())-iconH)/2, W: iconW, H: iconH}
	lit := func(r sdl.Rect) int {
		return countPixelsMatching(f, r, func(c sdl.Color) bool { return c != f.bg })
	}
	band := sdl.Rect{X: icon.X, Y: icon.Y, W: icon.W, H: icon.H / 2}
	cups := sdl.Rect{X: icon.X, Y: icon.Y + icon.H/2, W: icon.W, H: icon.H - icon.H/2}
	if lit(icon) == 0 {
		t.Fatal("the chip drew no headphone glyph")
	}
	if lit(band) == 0 {
		t.Fatal("the headphone band is missing above the ear cups")
	}
	if lit(cups) == 0 {
		t.Fatal("the ear cups are missing below the band")
	}
	title := sdl.Rect{
		X: icon.X + iconW + statusChipTextGap,
		Y: icon.Y,
		W: drawn - iconW - statusChipTextGap,
		H: icon.H,
	}
	if lit(title) == 0 {
		t.Fatal("the title was not drawn beside the glyph")
	}
	// The chip must never paint past the width it reported, or the clock and
	// the bell would be covered by a long title.
	past := sdl.Rect{X: x + drawn + 2, Y: icon.Y, W: 60, H: icon.H}
	if n := lit(past); n != 0 {
		t.Fatalf("%d pixels were drawn past the measured chip width", n)
	}
}

// The chip's probe spot and label stay fixed so two playback states can be
// compared pixel for pixel.
const (
	chipProbeX    = int32(400)
	chipProbeY    = int32(20)
	chipProbePart = statusMediaPartPrefix + "Low Tide Radio"
)

// mediaChipFrame draws the now-playing chip once and captures the pixels.
func mediaChipFrame(t *testing.T) *probeFrame {
	t.Helper()
	return mediaChipFrameFor(t, chipProbePart)
}

// mediaChipFrameFor is mediaChipFrame with the part under test spelled out, so
// the idle output chip can be captured the same way.
func mediaChipFrameFor(t *testing.T, part string) *probeFrame {
	t.Helper()
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	drawStatusMediaChip(renderer, font, part, chipProbeX, chipProbeY,
		func(text string, col sdl.Color, tx, ty int32) {
			renderText(renderer, &Config{}, font, text, col, tx, ty)
		})
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	return f
}

// mediaChipIcon is the glyph box at the chip's probe spot.
func mediaChipIcon(t *testing.T) sdl.Rect {
	t.Helper()
	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}
	iconH := statusIconHeight(font)
	return sdl.Rect{
		X: chipProbeX,
		Y: chipProbeY + (int32(font.Height())-iconH)/2,
		W: statusMediaIconWidth(iconH),
		H: iconH,
	}
}

// Pausing must be visible in the glyph itself: headphones become two upright
// bars with a gap between them, so a glance separates paused from playing.
func TestStatusMediaChipShowsPausedState(t *testing.T) {
	pinVideoState(t, "idle", "")
	pinMusicState(t, "Low Tide Radio")
	pinMusicPaused(t, false)
	playing := mediaChipFrame(t)

	pinMusicPaused(t, true)
	paused := mediaChipFrame(t)

	icon := mediaChipIcon(t)
	differ := 0
	for y := icon.Y; y < icon.Y+icon.H; y++ {
		for x := icon.X; x < icon.X+icon.W; x++ {
			if playing.at(x, y) != paused.at(x, y) {
				differ++
			}
		}
	}
	if differ == 0 {
		t.Fatal("a paused chip is pixel-identical to a playing one")
	}

	columnLit := func(f *probeFrame, x int32) int {
		n := 0
		for y := icon.Y; y < icon.Y+icon.H; y++ {
			if f.at(x, y) != f.bg {
				n++
			}
		}
		return n
	}
	// Two upright bars with a clear gap: the lit columns must form exactly two
	// runs with at least a pixel of background between them, at every icon
	// size the bar can use.
	var runs [][2]int32
	var start int32 = -1
	for x := icon.X; x < icon.X+icon.W; x++ {
		lit := columnLit(paused, x) >= int(icon.H)/2
		if lit && start < 0 {
			start = x
		}
		if !lit && start >= 0 {
			runs = append(runs, [2]int32{start, x - 1})
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, [2]int32{start, icon.X + icon.W - 1})
	}
	if len(runs) != 2 {
		t.Fatalf("the paused glyph has %d lit columns runs %v, want two bars", len(runs), runs)
	}
	if gap := runs[1][0] - runs[0][1]; gap < 2 {
		t.Fatalf("the pause bars are only %dpx apart (runs %v)", gap, runs)
	}

	// The title is still drawn beside the state glyph.
	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}
	title := sdl.Rect{
		X: icon.X + icon.W + statusChipTextGap,
		Y: icon.Y,
		W: statusClusterPartWidth(font, chipProbePart) - icon.W - statusChipTextGap,
		H: icon.H,
	}
	if n := countPixelsMatching(paused, title, func(c sdl.Color) bool { return c != paused.bg }); n == 0 {
		t.Fatal("the paused chip lost its title")
	}
}

// A known length draws the hairline progress hint under the title; an unknown
// one (a stream, a track still being probed) draws nothing rather than a fake
// position.
func TestStatusMediaChipDrawsProgressHint(t *testing.T) {
	pinVideoState(t, "idle", "")
	pinMusicState(t, "Low Tide Radio")
	pinMusicPaused(t, false)

	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}
	width := statusClusterPartWidth(font, chipProbePart)
	lineY := chipProbeY + int32(font.Height()) + statusMediaProgressGap
	line := sdl.Rect{X: chipProbeX, Y: lineY, W: width, H: statusMediaProgressH}

	pinMusicProgress(t, 0, 0)
	unknown := mediaChipFrame(t)
	if n := countPixelsMatching(unknown, line, func(c sdl.Color) bool { return c != unknown.bg }); n != 0 {
		t.Fatalf("a session with no known length drew %d progress pixels", n)
	}

	pinMusicProgress(t, 25, 100)
	quarter := mediaChipFrame(t)
	left := quarter.at(line.X+3, line.Y+line.H/2)
	right := quarter.at(line.X+line.W-4, line.Y+line.H/2)
	if left == quarter.bg {
		t.Fatal("the played part of the progress hint was not drawn")
	}
	if right == quarter.bg {
		t.Fatal("the progress track was not drawn to the end of the chip")
	}
	if left == right {
		t.Fatalf("the played part %v looks the same as the remaining track %v", left, right)
	}
	// The hint must stay inside the measured width, like everything else.
	past := sdl.Rect{X: chipProbeX + width + 2, Y: line.Y, W: 40, H: line.H}
	if n := countPixelsMatching(quarter, past, func(c sdl.Color) bool { return c != quarter.bg }); n != 0 {
		t.Fatalf("%d progress pixels were drawn past the chip", n)
	}
}

// The state helpers behind the chip: paused is separate from playing, and the
// progress fraction is clamped and only reported when the length is known.
func TestMediaChipReportsPausedAndProgress(t *testing.T) {
	pinVideoState(t, "idle", "")
	pinMusicState(t, "Low Tide Radio")
	pinMusicProgress(t, 30, 120)

	if mediaPaused() {
		t.Error("a playing track reported as paused")
	}
	if f, ok := mediaProgress(); !ok || math.Abs(f-0.25) > 1e-9 {
		t.Fatalf("progress = %v, ok = %v; want 0.25", f, ok)
	}

	pinMusicPaused(t, true)
	if !mediaPaused() {
		t.Error("a paused track did not report paused")
	}
	if f, ok := mediaProgress(); !ok || math.Abs(f-0.25) > 1e-9 {
		t.Fatalf("a paused track lost its position: progress = %v, ok = %v", f, ok)
	}

	// Length still unknown: no hint.
	pinMusicProgress(t, 12, 0)
	if _, ok := mediaProgress(); ok {
		t.Error("a track with no duration claimed a progress fraction")
	}

	// A position past the end clamps to the end of the line.
	pinMusicProgress(t, 500, 100)
	if f, ok := mediaProgress(); !ok || f != 1 {
		t.Fatalf("overrun progress = %v, ok = %v; want 1", f, ok)
	}

	// A paused video is paused, and reports its own fraction.
	pinMusicState(t, "")
	pinVideoState(t, "paused", "https://videos.example/watch/abc")
	pinVideoProgress(t, 0.4, 90)
	if !mediaPaused() {
		t.Error("a paused video did not report paused")
	}
	if f, ok := mediaProgress(); !ok || math.Abs(f-0.4) > 1e-9 {
		t.Fatalf("video progress = %v, ok = %v; want 0.4", f, ok)
	}

	// A video whose duration is not known yet shows no hint either.
	pinVideoProgress(t, 0, 0)
	if _, ok := mediaProgress(); ok {
		t.Error("a video with no duration claimed a progress fraction")
	}
}

// With nothing playing the chip names the output instead: the connected
// headset, so the corner is never an empty gap. A live title still wins.
func TestStatusMediaPartNamesOutputWhenIdle(t *testing.T) {
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	pinAudioOutput(t, "WH-1000XM4")

	part, ok := statusMediaPart()
	if !ok || !isOutputStatusPart(part) {
		t.Fatalf("idle chip part = %q, ok = %v; want the output device", part, ok)
	}
	if got := statusChipText(part); got != "WH-1000XM4" {
		t.Fatalf("chip text = %q, want the device name", got)
	}
	if nowPlayingChipIsButton() {
		t.Error("an idle output chip claims to be a button")
	}

	// Headphones plugged in with no paired headset still name an output.
	pinAudioOutput(t, "Headphones")
	if part, ok := statusMediaPart(); !ok || statusChipText(part) != "Headphones" {
		t.Fatalf("wired chip = %q, ok = %v", part, ok)
	}

	// A live title wins over the output name.
	pinMusicState(t, "Low Tide Radio")
	part, ok = statusMediaPart()
	if !ok || !isMediaStatusPart(part) || statusChipText(part) != "Low Tide Radio" {
		t.Fatalf("playing chip part = %q, ok = %v; want the track", part, ok)
	}
	if !nowPlayingChipIsButton() {
		t.Error("a playing chip is not a button")
	}
}

// The idle output chip is a reading: it is drawn and measured like the player
// chip, but it never joins the chrome focus order and a tap on it does nothing.
func TestOutputChipIsNotAButton(t *testing.T) {
	pinMusicState(t, "")
	pinVideoState(t, "idle", "")
	pinAudioOutput(t, "WH-1000XM4")
	prevRect := statusMediaRect
	statusMediaRect = sdl.Rect{X: 980, Y: 8, W: 140, H: 32}
	t.Cleanup(func() { statusMediaRect = prevRect })

	config := audioTestConfig()
	focusEngine.Clear()
	focusEngine.SetGraph("Main", focusEngine.BuildGraph(SceneConfig{Name: "Main"}))
	if focusEngine.SetByElementIndex(ChromeMediaIndex) {
		t.Fatal("the idle output chip joined the chrome focus graph")
	}
	isolateSceneSwitch(t)
	if handleChromeBarClick(statusMediaRect.X+4, statusMediaRect.Y+4, config) {
		t.Fatal("a tap on the idle output chip was consumed")
	}

	// A track makes the same spot a button again.
	pinMusicState(t, "Low Tide Radio")
	focusEngine.Clear()
	focusEngine.SetGraph("Main", focusEngine.BuildGraph(SceneConfig{Name: "Main"}))
	if !focusEngine.SetByElementIndex(ChromeMediaIndex) {
		t.Fatal("the playing chip did not rejoin the focus graph")
	}
	if !handleChromeBarClick(statusMediaRect.X+4, statusMediaRect.Y+4, config) {
		t.Fatal("a tap on the playing chip was not consumed")
	}
}

// The output chip carries the headphone glyph and the device name, and ignores
// playback state: no pause bars and no progress hairline.
// The idle output chip is informational too, so it is the first part to go when
// the cluster runs out of room.
func TestFitStatusPartsDropsOutputChipFirst(t *testing.T) {
	width := func(p string) int32 { return statusClusterPartWidth(nil, p) }
	output := statusOutputPartPrefix + "WH-1000XM4"
	parts := []string{statusWiFiMarker, "12:40", "87%", output}
	const gap = int32(10)

	keep := parts[:len(parts)-1]
	maxW := int32(0)
	for i, p := range keep {
		if i > 0 {
			maxW += gap
		}
		maxW += width(p)
	}
	fitted := fitStatusParts(append([]string(nil), parts...), width, gap, maxW)
	for _, p := range fitted {
		if isOutputStatusPart(p) {
			t.Fatalf("a crowded cluster kept the output chip: %v", fitted)
		}
	}
	if len(fitted) != len(keep) {
		t.Fatalf("fitted cluster = %v, want %v", fitted, keep)
	}
}

func TestStatusOutputChipDrawsDeviceName(t *testing.T) {
	pinVideoState(t, "idle", "")
	pinMusicState(t, "Low Tide Radio")
	pinMusicPaused(t, false)
	pinMusicProgress(t, 50, 100)
	pinAudioOutput(t, "WH-1000XM4")
	part := statusOutputPartPrefix + "WH-1000XM4"

	playing := mediaChipFrameFor(t, part)
	pinMusicPaused(t, true)
	paused := mediaChipFrameFor(t, part)
	for i := range playing.buf {
		if playing.buf[i] != paused.buf[i] {
			t.Fatalf("an output chip changed with playback state at byte %d", i)
		}
	}

	font := loadHomeFonts().Small
	if font == nil {
		t.Skip("no status font on this machine")
	}
	icon := mediaChipIcon(t)
	title := sdl.Rect{
		X: icon.X + icon.W + statusChipTextGap,
		Y: icon.Y,
		W: statusClusterPartWidth(font, part) - icon.W - statusChipTextGap,
		H: icon.H,
	}
	lit := func(r sdl.Rect) int {
		return countPixelsMatching(playing, r, func(c sdl.Color) bool { return c != playing.bg })
	}
	if lit(title) == 0 {
		t.Fatal("the output chip did not draw the device name")
	}
	line := sdl.Rect{
		X: chipProbeX,
		Y: chipProbeY + int32(font.Height()) + statusMediaProgressGap,
		W: statusClusterPartWidth(font, part),
		H: statusMediaProgressH,
	}
	if n := lit(line); n != 0 {
		t.Fatalf("the output chip drew %d progress pixels", n)
	}

	// A long device name is ellipsized to the same width the cluster measured,
	// so the clock and bell cannot be covered by a verbose headset.
	long := statusOutputPartPrefix + "Sony WH-1000XM4 Wireless Noise Cancelling Headphones"
	longFrame := mediaChipFrameFor(t, long)
	past := sdl.Rect{
		X: chipProbeX + statusClusterPartWidth(font, long) + 2,
		Y: icon.Y,
		W: 80,
		H: icon.H,
	}
	if n := countPixelsMatching(longFrame, past, func(c sdl.Color) bool { return c != longFrame.bg }); n != 0 {
		t.Fatalf("%d pixels of a long device name were drawn past the chip", n)
	}
}
