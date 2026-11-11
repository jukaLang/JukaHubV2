package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gifWallTestPalette is black, red, green and a transparent entry.
var gifWallTestPalette = color.Palette{
	color.RGBA{0, 0, 0, 255},
	color.RGBA{255, 0, 0, 255},
	color.RGBA{0, 255, 0, 255},
	color.RGBA{0, 0, 0, 0},
}

// gifWallTestGIF encodes a 4×4, four-frame GIF that uses every disposal the
// compositor handles:
//
//	0: the whole screen red, kept (none), 50 ms
//	1: a green 2×2 in the middle, cleared to the background after, 0 → 100 ms
//	2: a green pixel at (0,0) over transparency, restored after, 30 ms
//	3: a black pixel at (3,3), kept, 10 ms → 100 ms
func gifWallTestGIF(t *testing.T) []byte {
	t.Helper()
	frame := func(r image.Rectangle, fill uint8) *image.Paletted {
		p := image.NewPaletted(r, gifWallTestPalette)
		for i := range p.Pix {
			p.Pix[i] = fill
		}
		return p
	}
	f2 := frame(image.Rect(0, 0, 2, 2), 3)
	f2.SetColorIndex(0, 0, 2)
	g := &gif.GIF{
		Image: []*image.Paletted{
			frame(image.Rect(0, 0, 4, 4), 1),
			frame(image.Rect(1, 1, 3, 3), 2),
			f2,
			frame(image.Rect(3, 3, 4, 4), 0),
		},
		Delay:    []int{5, 0, 3, 1},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
		Config:   image.Config{ColorModel: gifWallTestPalette, Width: 4, Height: 4},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// gifWallTestRender walks and renders data onto a canvasW×canvasH screen.
func gifWallTestRender(t *testing.T, data []byte, canvasW, canvasH int, lim gifWallLimits) *gifWallJob {
	t.Helper()
	scan, err := gifWallScan(bytes.NewReader(data), int64(len(data)), lim.frames)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	job := &gifWallJob{gen: gifWallGen.Load()}
	if err := gifWallRender(job, bytes.NewReader(data), scan, canvasW, canvasH, [4]byte{10, 20, 30, 255}, lim); err != nil {
		t.Fatalf("render: %v", err)
	}
	return job
}

func gifWallTestPixel(pix []byte, w, x, y int) [3]byte {
	o := (y*w + x) * 4
	return [3]byte{pix[o], pix[o+1], pix[o+2]}
}

// gifWallTestState swaps the player's state and clock for a test and puts
// them back after it.
func gifWallTestState(t *testing.T, now *uint64) {
	t.Helper()
	saved, savedTicks := gifWall, gifWallTicks
	gifWall.path, gifWall.frames, gifWall.job = "", nil, nil
	gifWall.final, gifWall.pos, gifWall.posStart, gifWall.posSet = 0, 0, 0, false
	gifWall.complete, gifWall.ends, gifWall.loop = false, nil, 0
	gifWall.failedPath, gifWall.frozenPath = "", ""
	gifWallTicks = func() uint64 { return *now }
	t.Cleanup(func() { gifWall, gifWallTicks = saved, savedTicks })
}

func TestGifWallCompositesEveryDisposal(t *testing.T) {
	lim := gifWallLimits{file: 1 << 20, budget: 1 << 20, canvas: 1 << 20, frames: gifWallMaxFrames}
	// A 12×12 screen makes the 4×4 GIF pixel art at exactly 3×: nothing is
	// cropped or scaled, so the frames are the composites themselves.
	job := gifWallTestRender(t, gifWallTestGIF(t), 12, 12, lim)
	if job.w != 4 || job.h != 4 || !job.nearest {
		t.Fatalf("plan = %dx%d nearest=%v, want 4x4 nearest", job.w, job.h, job.nearest)
	}
	if len(job.frames) != 4 {
		t.Fatalf("frames = %d, want 4", len(job.frames))
	}
	red, green, black, bg := [3]byte{255, 0, 0}, [3]byte{0, 255, 0}, [3]byte{0, 0, 0}, [3]byte{10, 20, 30}
	type px = [2]int
	want := [4]map[px][3]byte{
		{},
		{px{1, 1}: green, px{2, 1}: green, px{1, 2}: green, px{2, 2}: green},
		{px{0, 0}: green, px{1, 1}: bg, px{2, 1}: bg, px{1, 2}: bg, px{2, 2}: bg},
		{px{1, 1}: bg, px{2, 1}: bg, px{1, 2}: bg, px{2, 2}: bg, px{3, 3}: black},
	}
	for i, f := range job.frames {
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				exp, ok := want[i][px{x, y}]
				if !ok {
					exp = red
				}
				if got := gifWallTestPixel(f.pix, 4, x, y); got != exp {
					t.Errorf("frame %d pixel (%d,%d) = %v, want %v", i, x, y, got, exp)
				}
			}
		}
	}
	for i, d := range []int{50, 100, 30, 100} {
		if job.frames[i].delay != d {
			t.Errorf("frame %d delay = %d ms, want %d", i, job.frames[i].delay, d)
		}
	}
}

func TestGifWallDroppedFramesKeepTheirTime(t *testing.T) {
	// Room for two 4×4 frames: frames 0 and 2 are kept, and each carries the
	// time of the frame dropped after it.
	lim := gifWallLimits{file: 1 << 20, budget: 2 * 4 * 4 * 4, canvas: 1 << 20, frames: gifWallMaxFrames}
	job := gifWallTestRender(t, gifWallTestGIF(t), 8, 8, lim)
	if len(job.frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(job.frames))
	}
	if job.frames[0].delay != 150 || job.frames[1].delay != 130 {
		t.Errorf("delays = %d, %d ms, want 150, 130", job.frames[0].delay, job.frames[1].delay)
	}
}

func TestGifWallScanKeepsFramesBeforeDamage(t *testing.T) {
	data := gifWallTestGIF(t)
	scan, err := gifWallScan(bytes.NewReader(data), int64(len(data)), gifWallMaxFrames)
	if err != nil || len(scan.frames) != 4 {
		t.Fatalf("scan = %v frames, %v", gifWallScanCount(scan), err)
	}
	if scan.width != 4 || scan.height != 4 {
		t.Errorf("screen = %dx%d, want 4x4", scan.width, scan.height)
	}
	// A whole file read to its last byte is not truncated, and neither is
	// one whose frame count is exactly the cap.
	if scan.truncated {
		t.Errorf("a complete file was reported truncated")
	}
	if exact, err := gifWallScan(bytes.NewReader(data), int64(len(data)), 4); err != nil || exact.truncated {
		t.Errorf("a file of exactly the frame cap: truncated=%v, %v", exact != nil && exact.truncated, err)
	}
	if f := scan.frames[1]; f.left != 1 || f.top != 1 || f.w != 2 || f.h != 2 || f.disposal != 2 || f.delayCS != 0 {
		t.Errorf("frame 1 = %+v", f)
	}
	// Cut inside the last frame: the three before it still play, and the
	// damage is not reported as a limit.
	cut := data[:scan.frames[3].off+4]
	short, err := gifWallScan(bytes.NewReader(cut), int64(len(cut))+100, gifWallMaxFrames)
	if err != nil || len(short.frames) != 3 || short.truncated {
		t.Errorf("cut scan = %v frames truncated=%v, %v; want 3, not truncated", gifWallScanCount(short), short != nil && short.truncated, err)
	}
	// The frame cap and the read limit both stop the walk, and say which.
	capped, err := gifWallScan(bytes.NewReader(data), int64(len(data)), 2)
	if err != nil || len(capped.frames) != 2 || !capped.capped || capped.cut || !capped.truncated {
		t.Errorf("capped scan = %v frames %+v, %v", gifWallScanCount(capped), capped, err)
	}
	limited, err := gifWallScan(bytes.NewReader(data), scan.frames[2].off, gifWallMaxFrames)
	if err != nil || len(limited.frames) != 2 || !limited.cut || limited.capped || !limited.truncated {
		t.Errorf("limited scan = %v frames %+v, %v; want 2, cut", gifWallScanCount(limited), limited, err)
	}
}

func gifWallScanCount(s *gifScan) int {
	if s == nil {
		return -1
	}
	return len(s.frames)
}

func TestGifWallScanRejectsNonGIFs(t *testing.T) {
	for name, data := range map[string][]byte{
		"png":       []byte("\x89PNG\r\n\x1a\nxxxxxxxxxxxx"),
		"empty":     nil,
		"no frames": append([]byte("GIF89a\x04\x00\x04\x00\x00\x00\x00"), 0x3B),
	} {
		if _, err := gifWallScan(bytes.NewReader(data), 1<<20, gifWallMaxFrames); err == nil {
			t.Errorf("%s: scan succeeded, want an error", name)
		}
	}
}

func TestGifWallRefusesHugeFrames(t *testing.T) {
	// A 16×16 screen whose only frame claims 65535×65535 pixels. Once the
	// header has grown to hold the frame, image/gif would allocate 4 GB for it.
	data := []byte("GIF89a")
	data = append(data, 16, 0, 16, 0, 0x00, 0, 0)                       // screen, no global table
	data = append(data, 0x2C, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0x80) // descriptor, local table
	data = append(data, 0, 0, 0, 255, 255, 255)                         // two colours
	data = append(data, 2, 2, 0x4C, 0x01, 0)                            // LZW size, one sub-block
	data = append(data, 0x3B)
	scan, err := gifWallScan(bytes.NewReader(data), int64(len(data)), gifWallMaxFrames)
	if err != nil || len(scan.frames) != 1 {
		t.Fatalf("scan = %v frames, %v", gifWallScanCount(scan), err)
	}
	if f := scan.frames[0]; f.w != 65535 || f.h != 65535 {
		t.Fatalf("frame = %dx%d, want 65535x65535", f.w, f.h)
	}
	if _, _, err := gifWallDecodeFrame(bytes.NewReader(data), gifWallFrameHeader(scan), scan.frames[0], nil, 1<<20); err == nil {
		t.Fatal("a 65535×65535 frame was decoded; want it refused before anything is allocated")
	}
	job := &gifWallJob{gen: gifWallGen.Load()}
	lim := gifWallLimits{file: 1 << 20, budget: 1 << 20, canvas: 1 << 20, frames: gifWallMaxFrames}
	if err := gifWallRender(job, bytes.NewReader(data), scan, 64, 64, [4]byte{}, lim); err == nil {
		t.Error("the decode accepted a frame far beyond its limits")
	}
	path := filepath.Join(t.TempDir(), "huge.gif")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := wpThumbDecode(path, 100, 100, 1280, 720); len(res.pix) != 0 {
		t.Error("the preview decoded a frame it should have refused")
	}
}

func TestGifWallRenderStopsWhenCancelled(t *testing.T) {
	data := gifWallTestGIF(t)
	scan, err := gifWallScan(bytes.NewReader(data), int64(len(data)), gifWallMaxFrames)
	if err != nil {
		t.Fatal(err)
	}
	job := &gifWallJob{gen: gifWallGen.Load() + 1000}
	lim := gifWallLimits{file: 1 << 20, budget: 1 << 20, canvas: 1 << 20, frames: gifWallMaxFrames}
	if err := gifWallRender(job, bytes.NewReader(data), scan, 8, 8, [4]byte{}, lim); err != errGifWallCancelled {
		t.Errorf("err = %v, want errGifWallCancelled", err)
	}
	if len(job.frames) != 0 {
		t.Errorf("a cancelled decode handed over %d frames", len(job.frames))
	}
}

func TestGifWallDelayFollowsBrowsers(t *testing.T) {
	for cs, want := range map[int]int{0: 100, 1: 100, 2: 20, 5: 50, 10: 100, 250: 2500} {
		if got := gifWallDelay(cs); got != want {
			t.Errorf("gifWallDelay(%d) = %d, want %d", cs, got, want)
		}
	}
}

func TestGifWallFrameAt(t *testing.T) {
	ends := []int64{100, 150, 400}
	for tm, want := range map[int64]int{0: 0, 99: 0, 100: 1, 149: 1, 150: 2, 399: 2, 400: 2} {
		if got := gifWallFrameAt(ends, tm); got != want {
			t.Errorf("frame at %d ms = %d, want %d", tm, got, want)
		}
	}
	if got := gifWallFrameAt([]int64{100}, 50); got != 0 {
		t.Errorf("single frame = %d, want 0", got)
	}
}

func TestGifWallPixelScale(t *testing.T) {
	for _, c := range []struct {
		name                       string
		srcW, srcH, canvasW, cH, k int
	}{
		{"Game Boy on the Smart Pro", 160, 144, 1280, 720, 8},
		{"SNES on the Brick", 256, 224, 960, 720, 4},
		{"a quarter of the screen", 320, 180, 1280, 720, 4},
		{"a video clip at a third", 480, 270, 1280, 720, 0},
		{"half the screen", 640, 360, 1280, 720, 0},
		{"rounding up would crop too much", 240, 160, 1280, 720, 0},
		{"nothing", 0, 10, 1280, 720, 0},
	} {
		if got := gifWallPixelScale(c.srcW, c.srcH, c.canvasW, c.cH); got != c.k {
			t.Errorf("%s: scale = %d, want %d", c.name, got, c.k)
		}
	}
}

func TestGifWallPlanCoversTheScreen(t *testing.T) {
	const budget = gifWallBudgetHandheld
	cases := []struct {
		name                       string
		srcW, srcH, canvasW, cH, n int
		crop                       image.Rectangle
		w, h, kept                 int
		nearest                    bool
	}{
		{"same aspect", 1920, 1080, 1280, 720, 10, image.Rect(0, 0, 1920, 1080), 1280, 720, 10, false},
		{"square on 16:9, over budget", 2000, 2000, 1280, 720, 120, image.Rect(0, 437, 2000, 1562), 640, 360, 45, false},
		{"square on the Brick", 2000, 2000, 960, 720, 120, image.Rect(0, 250, 2000, 1750), 480, 360, 60, false},
		{"panorama", 1000, 200, 1280, 720, 1, image.Rect(322, 0, 678, 200), 356, 200, 1, false},
		{"Game Boy pixel art", 160, 144, 1280, 720, 30, image.Rect(0, 27, 160, 117), 160, 90, 30, true},
		{"SNES pixel art on the Brick", 256, 224, 960, 720, 30, image.Rect(8, 22, 248, 202), 240, 180, 30, true},
		{"pixel art at a quarter", 320, 180, 1280, 720, 10, image.Rect(0, 0, 320, 180), 320, 180, 10, true},
		// A clip a third of the screen wide is smoothed whole, not cut by a
		// fifth and drawn in blocks.
		{"video clip at a third", 480, 270, 1280, 720, 10, image.Rect(0, 0, 480, 270), 480, 270, 10, false},
		{"pixel art a whole number misses", 240, 160, 1280, 720, 10, image.Rect(0, 12, 240, 147), 240, 135, 10, false},
	}
	for _, c := range cases {
		p := gifWallMakePlan(c.srcW, c.srcH, c.canvasW, c.cH, c.n, budget)
		if p.crop != c.crop || p.w != c.w || p.h != c.h || p.kept != c.kept || p.nearest != c.nearest {
			t.Errorf("%s: plan crop=%v %dx%d kept=%d nearest=%v; want crop=%v %dx%d kept=%d nearest=%v",
				c.name, p.crop, p.w, p.h, p.kept, p.nearest, c.crop, c.w, c.h, c.kept, c.nearest)
			continue
		}
		if p.nearest {
			// The whole-number scale never overshoots the screen.
			k := gifWallPixelScale(c.srcW, c.srcH, c.canvasW, c.cH)
			if p.w*k > c.canvasW || p.h*k > c.cH {
				t.Errorf("%s: %dx%d at %d× overshoots %dx%d", c.name, p.w, p.h, k, c.canvasW, c.cH)
			}
		}
		kept := 0
		for _, k := range p.keep {
			if k {
				kept++
			}
		}
		if kept != p.kept || !p.keep[0] || len(p.keep) != c.n {
			t.Errorf("%s: keep mask has %d of %d (first %v), want %d", c.name, kept, len(p.keep), p.keep[0], p.kept)
		}
		if int64(p.w)*int64(p.h)*4*int64(p.kept) > budget {
			t.Errorf("%s: %d frames of %dx%d exceed the budget", c.name, p.kept, p.w, p.h)
		}
	}
}

func TestGifWallScaleAverages(t *testing.T) {
	// 4×2 → 2×1: each output pixel is the mean of a 2×2 block.
	src := make([]byte, 4*2*4)
	set := func(x, y int, v byte) {
		o := (y*4 + x) * 4
		src[o], src[o+1], src[o+2], src[o+3] = v, v, v, 255
	}
	set(0, 0, 0)
	set(1, 0, 100)
	set(0, 1, 100)
	set(1, 1, 200)
	set(2, 0, 40)
	set(3, 0, 40)
	set(2, 1, 40)
	set(3, 1, 40)
	dst := make([]byte, 2*1*4)
	gifWallScale(src, 4, 2, dst, 2, 1)
	if dst[0] != 100 || dst[4] != 40 || dst[3] != 255 {
		t.Errorf("scaled = %v, want 100 and 40, opaque", dst)
	}
	same := make([]byte, len(src))
	gifWallScale(src, 4, 2, same, 4, 2)
	if !bytes.Equal(same, src) {
		t.Errorf("a same-size scale changed the pixels")
	}
}

func TestGifWallPlaysFramesAsTheyArrive(t *testing.T) {
	now := uint64(1000)
	gifWallTestState(t, &now)
	// Three frames are up; the first two have settled times, the newest may
	// still gain the time of frames dropped after it.
	gifWall.frames = []gifWallTex{{delay: 100}, {delay: 100}, {delay: 100}}
	gifWall.final = 2
	steps := []struct {
		at   uint64
		want int
	}{
		{1000, 0}, {1099, 0}, {1100, 1}, {1199, 1}, {1250, 2},
		// The newest frame's time is not settled: it stays up, however long.
		{5000, 2},
	}
	for _, s := range steps {
		now = s.at
		if got := gifWallAdvance(now); got != s.want {
			t.Fatalf("at %d ms: frame %d, want %d", s.at, got, s.want)
		}
	}
	// The next frame arrives and frame 2's time settles: frame 3 starts when
	// it arrived, not when it was due, so nothing is skipped to catch up.
	gifWall.frames = append(gifWall.frames, gifWallTex{delay: 100}, gifWallTex{delay: 100})
	gifWall.final = 4
	if got := gifWallAdvance(5000); got != 3 {
		t.Fatalf("after the wait: frame %d, want 3", got)
	}
	if got := gifWallAdvance(5099); got != 3 {
		t.Errorf("frame 3 cut short: frame %d at 5099 ms", got)
	}
	if got := gifWallAdvance(5100); got != 4 {
		t.Errorf("frame 4 late: frame %d at 5100 ms", got)
	}
}

func TestGifWallFinishCarriesOnFromTheFrameOnScreen(t *testing.T) {
	now := uint64(1000)
	gifWallTestState(t, &now)
	job := &gifWallJob{frames: []gifWallOut{{delay: 100}, {delay: 100}, {delay: 100}, {delay: 100}}, done: true}
	gifWall.job = job
	gifWall.frames = []gifWallTex{{}, {}, {}, {}}
	gifWall.final = 4
	gifWall.pos, gifWall.posStart, gifWall.posSet = 2, 970, true
	gifWallFinish(job, 4)
	if !gifWall.complete || gifWall.loop != 400 {
		t.Fatalf("complete=%v loop=%d, want true, 400", gifWall.complete, gifWall.loop)
	}
	elapsed := int64(gifWallTicks()-gifWall.start) % gifWall.loop
	if got := gifWallFrameAt(gifWall.ends, elapsed); got != 2 || elapsed != 230 {
		t.Errorf("the loop starts at frame %d (%d ms in), want frame 2, 230 ms in", got, elapsed)
	}
}

func TestGifWallShrinkForPressureKeepsTheFirstFrame(t *testing.T) {
	now := uint64(1000)
	gifWallTestState(t, &now)
	gifWall.path = "/w/loop.gif"
	gifWall.frames = []gifWallTex{{delay: 80}, {delay: 100}, {delay: 100}}
	gifWallShrinkForPressure()
	if len(gifWall.frames) != 1 || !gifWall.complete || gifWall.loop != 80 || gifWall.frozenPath != "/w/loop.gif" {
		t.Fatalf("after pressure: %d frames complete=%v loop=%d frozen=%q",
			len(gifWall.frames), gifWall.complete, gifWall.loop, gifWall.frozenPath)
	}
	// Picking it again lets it animate again.
	gifWallRetryPick("/w/loop.gif")
	if gifWall.frozenPath != "" || gifWall.path != "" || len(gifWall.frames) != 0 {
		t.Errorf("a re-pick left frozen=%q path=%q frames=%d", gifWall.frozenPath, gifWall.path, len(gifWall.frames))
	}
}

func TestGifWallpaperPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/mnt/SDCARD/Wallpapers/rain.gif": true, `C:\Pictures\Loop.GIF`: true, " a.gif ": true,
		"a.png": false, "gif": false, "": false, "a.gif.png": false,
	} {
		if got := gifWallpaperPath(path); got != want {
			t.Errorf("gifWallpaperPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWallpaperRestorePath(t *testing.T) {
	exists := func(p string) bool { return p == "/w/rain.gif" }
	cases := []struct {
		name, current, saved string
		packOwns             bool
		want                 string
	}{
		{"nothing saved", "background.jpg", "", false, ""},
		{"saved none", "background.jpg", "none", false, ""},
		{"restored over the stock picture", "background.jpg", "/w/rain.gif", false, "/w/rain.gif"},
		{"restored over an empty value", "", "/w/rain.gif", false, "/w/rain.gif"},
		{"the config names its own", "/design/own.png", "/w/rain.gif", false, ""},
		{"an experience pack has it", "background.jpg", "/w/rain.gif", true, ""},
		{"the file is gone", "background.jpg", "/w/gone.gif", false, ""},
	}
	for _, c := range cases {
		if got := wallpaperRestorePath(c.current, c.saved, c.packOwns, exists); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWallpaperRestoreSaved(t *testing.T) {
	pick := filepath.Join(t.TempDir(), "loop.gif")
	if err := os.WriteFile(pick, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	wallpaperRestoreRemember(nil)
	defer wallpaperRestoreRemember(nil)

	cfg := &Config{}
	cfg.Variables.BackgroundImage = "background.jpg"
	cfg.Variables.Custom = map[string]interface{}{wallpaperSavedKey: pick}
	wallpaperRestoreSaved(cfg)
	if cfg.Variables.BackgroundImage != pick {
		t.Fatalf("restored %q, want %q", cfg.Variables.BackgroundImage, pick)
	}
	// A reload in place (a backup restored, the file edited) keeps the
	// pointer but names the stock picture again: the pick comes back.
	cfg.Variables.BackgroundImage = "background.jpg"
	wallpaperRestoreSaved(cfg)
	if cfg.Variables.BackgroundImage != pick {
		t.Errorf("an in-place reload lost the pick: %q", cfg.Variables.BackgroundImage)
	}
	// "None" was picked: the glass backdrop stays.
	cfg.Variables.BackgroundImage = "background.jpg"
	cfg.Variables.Custom[wallpaperSavedKey] = wallpaperNoneValue
	wallpaperRestoreSaved(cfg)
	if cfg.Variables.BackgroundImage != "background.jpg" {
		t.Errorf("a saved none restored %q", cfg.Variables.BackgroundImage)
	}
	// An experience pack has the wallpaper: the pick yields to it.
	packed := &Config{}
	packed.Variables.BackgroundImage = "background.jpg"
	packed.Variables.Custom = map[string]interface{}{wallpaperSavedKey: pick, xpackWallpaperKey: "none"}
	wallpaperRestoreSaved(packed)
	if packed.Variables.BackgroundImage != "background.jpg" {
		t.Errorf("the pick beat an experience pack's wallpaper: %q", packed.Variables.BackgroundImage)
	}
	// A pick on a card that mounts late is looked for again, but not on
	// every frame.
	late := filepath.Join(t.TempDir(), "late.gif")
	lc := &Config{}
	lc.Variables.BackgroundImage = "background.jpg"
	lc.Variables.Custom = map[string]interface{}{wallpaperSavedKey: late}
	wallpaperRestoreSaved(lc)
	if lc.Variables.BackgroundImage != "background.jpg" {
		t.Fatalf("restored a file that is not there: %q", lc.Variables.BackgroundImage)
	}
	if err := os.WriteFile(late, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	wallpaperRestoreSaved(lc)
	if lc.Variables.BackgroundImage != "background.jpg" {
		t.Errorf("looked again before the retry interval")
	}
	wallpaperRestoreState.missingAt = time.Now().Add(-2 * gifWallRetry)
	wallpaperRestoreSaved(lc)
	if lc.Variables.BackgroundImage != late {
		t.Errorf("the late card's pick never came back: %q", lc.Variables.BackgroundImage)
	}
}

func TestWpThumbFit(t *testing.T) {
	cases := [][6]int{
		{1920, 1080, 400, 300, 400, 225},
		{100, 50, 400, 300, 100, 50},
		{1080, 1920, 400, 300, 168, 300},
		{0, 10, 400, 300, 0, 0},
	}
	for _, c := range cases {
		if w, h := wpThumbFit(c[0], c[1], c[2], c[3]); w != c[4] || h != c[5] {
			t.Errorf("fit %dx%d in %dx%d = %dx%d, want %dx%d", c[0], c[1], c[2], c[3], w, h, c[4], c[5])
		}
	}
}

func TestListPickerEntriesFoldersFirst(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"b.gif", "a.png", "Zed.png", ".hidden.png", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"sub", ".git"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listPickerEntries(dir, wallpaperExts)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.name)
	}
	// Folders first, then reading order — "Zed" after "b", not before "a".
	if len(got) != 4 || !got[0].folder || got[0].name != "sub" || got[1].name != "a.png" ||
		got[2].name != "b.gif" || got[3].name != "Zed.png" {
		t.Fatalf("entries = %v, want [sub a.png b.gif Zed.png]", names)
	}
	if !got[2].gif || got[1].gif {
		t.Errorf("GIF flags = %v/%v, want only b.gif marked", got[1].gif, got[2].gif)
	}
}

func TestWallpaperUpSelectsTheFolderLeft(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"alpha", "beta", "gamma"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	savedDir, savedEntries, savedCursor := wpDir, wpEntries, wpCursor
	defer func() { wpDir, wpEntries, wpCursor = savedDir, savedEntries, savedCursor }()
	wallpaperOpenDir(filepath.Join(root, "beta"), "")
	wallpaperUp()
	if wpDir != root {
		t.Fatalf("up went to %q, want %q", wpDir, root)
	}
	if wpCursor < 0 || wpCursor >= len(wpEntries) || wpEntries[wpCursor].name != "beta" {
		t.Errorf("the cursor is on row %d, want the folder just left (beta)", wpCursor)
	}
}
