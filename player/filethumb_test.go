package main

// filethumb_test.go — the Files list thumbnails. The pipeline has three parts
// worth pinning separately: which rows qualify, what the producers return, and
// whether the row actually paints the picture in the chip slot.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// writeFeTestPNG writes a solid-colour PNG so a thumbnail of it keeps one
// colour through the downscale.
func writeFeTestPNG(t *testing.T, path string, w, h int, c color.RGBA) {
	t.Helper()
	pic := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pic.SetRGBA(x, y, c)
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, pic); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFeThumbnailEligiblePicksPicturesAndVideos(t *testing.T) {
	cases := []struct {
		entry FileEntry
		want  bool
	}{
		{FileEntry{Name: "beach.jpg", Path: "/card/beach.jpg"}, true},
		{FileEntry{Name: "shot.png", Path: "/card/shot.png"}, true},
		{FileEntry{Name: "clip.mp4", Path: "/card/clip.mp4"}, true},
		{FileEntry{Name: "show.mkv", Path: "/card/show.mkv"}, true},
		// A directory has no picture, and the row that goes back up must keep
		// its badge so it never looks like a file.
		{FileEntry{Name: "photos", Path: "/card/photos", IsDir: true}, false},
		{FileEntry{Name: "..", Path: "/card", IsDir: true}, false},
		// Songs have no frame to grab: ffmpeg would run once per track to find
		// that out.
		{FileEntry{Name: "song.mp3", Path: "/card/song.mp3"}, false},
		{FileEntry{Name: "notes.txt", Path: "/card/notes.txt"}, false},
		{FileEntry{Name: "archive.zip", Path: "/card/archive.zip"}, false},
	}
	for _, c := range cases {
		if got := feThumbnailEligible(c.entry); got != c.want {
			t.Errorf("feThumbnailEligible(%q) = %v, want %v", c.entry.Name, got, c.want)
		}
	}
}

func TestFeThumbKeyFollowsTheFile(t *testing.T) {
	base := FileEntry{Name: "beach.jpg", Path: "/card/beach.jpg", Size: 1024, Mod: time.Unix(1700000000, 0)}
	if feThumbKey(base) != feThumbKey(base) {
		t.Fatal("the same file produced two keys; its row would rebuild the thumbnail every frame")
	}
	edited := base
	edited.Mod = base.Mod.Add(time.Minute)
	if feThumbKey(edited) == feThumbKey(base) {
		t.Fatal("a re-saved picture kept its key; the row would show the old photo")
	}
	resized := base
	resized.Size = base.Size + 1
	if feThumbKey(resized) == feThumbKey(base) {
		t.Fatal("a resized file kept its key; the row would show the old picture")
	}
	moved := base
	moved.Path = "/card/sub/beach.jpg"
	if feThumbKey(moved) == feThumbKey(base) {
		t.Fatal("two files with the same name share a key; one row would show the other's picture")
	}
}

func TestFeThumbCoverRectKeepsTheChipAspect(t *testing.T) {
	target := float64(feThumbW) / float64(feThumbH)
	for _, size := range [][2]int{{160, 100}, {100, 160}, {80, 80}, {1920, 1080}} {
		pic := image.Rect(0, 0, size[0], size[1])
		r := feThumbCoverRect(pic)
		if r.Empty() {
			t.Fatalf("%dx%d produced an empty crop %v", size[0], size[1], r)
		}
		if !r.In(pic) {
			t.Fatalf("%dx%d crop %v falls outside the picture", size[0], size[1], r)
		}
		if aspect := float64(r.Dx()) / float64(r.Dy()); aspect < target*0.95 || aspect > target*1.05 {
			t.Fatalf("%dx%d crop %v has aspect %.2f, want about %.2f", size[0], size[1], r, aspect, target)
		}
		// The crop is centred: the margins on the two cropped edges match
		// within a pixel.
		if left, right := r.Min.X-pic.Min.X, pic.Max.X-r.Max.X; left-right > 1 || right-left > 1 {
			t.Fatalf("%dx%d crop %v is not centred (margins %d and %d)", size[0], size[1], r, left, right)
		}
	}
}

// TestFeScaleToChipAveragesThePicture pins the box average: a two-tone source
// has to keep both tones instead of collapsing to whichever pixel a point
// sample landed on.
func TestFeScaleToChipAveragesThePicture(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 88, 56))
	for y := 0; y < 56; y++ {
		for x := 0; x < 88; x++ {
			if x < 44 {
				src.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{A: 255})
			}
		}
	}
	chip := feScaleToChip(src, feThumbCoverRect(src.Bounds()))
	if chip.Bounds() != image.Rect(0, 0, int(feThumbW), int(feThumbH)) {
		t.Fatalf("chip is %v, want %dx%d", chip.Bounds(), feThumbW, feThumbH)
	}
	left, right := chip.RGBAAt(2, int(feThumbH/2)), chip.RGBAAt(int(feThumbW)-3, int(feThumbH/2))
	if left.R < 200 || right.R > 60 {
		t.Fatalf("chip halves are %d and %d on red; the white/black split did not survive the downscale", left.R, right.R)
	}
	if a := chip.RGBAAt(0, 0).A; a != 255 {
		t.Fatalf("chip alpha = %d, want an opaque thumbnail", a)
	}
}

func TestFeThumbDecodeRefusesHugePictures(t *testing.T) {
	if !feThumbDecodeAllowed(1920, 1080) {
		t.Fatal("a 1080p photo was refused; that is an ordinary picture on the card")
	}
	if !feThumbDecodeAllowed(6000, 4000) {
		t.Fatal("a 24-megapixel photo was refused; a camera picture that size must still get a thumbnail")
	}
	if feThumbDecodeAllowed(8000, 6000) {
		t.Fatal("a 48-megapixel photo was allowed; its decoded surface is too large for the handheld")
	}
	// Unknown dimensions are answered by the decoder, not guessed at here.
	if !feThumbDecodeAllowed(0, 0) {
		t.Fatal("unknown dimensions were refused before the decoder could answer")
	}
}

func TestFeImageThumbnailFitsTheChip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beach.png")
	writeFeTestPNG(t, path, 160, 100, color.RGBA{R: 220, G: 40, B: 190, A: 255})

	data, err := feImageThumbnail(path)
	if err != nil {
		t.Fatalf("feImageThumbnail: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the generated thumbnail does not decode: %v", err)
	}
	if format != "png" {
		t.Fatalf("thumbnail format = %q, want png", format)
	}
	if cfg.Width != int(feThumbW) || cfg.Height != int(feThumbH) {
		t.Fatalf("thumbnail is %dx%d, want %dx%d", cfg.Width, cfg.Height, feThumbW, feThumbH)
	}
	// A downscale that lost the picture (an empty surface) would be a black
	// rectangle rather than the colour that went in.
	pic, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r, g, b, _ := pic.At(int(feThumbW/2), int(feThumbH/2)).RGBA()
	if r>>8 < 180 || b>>8 < 150 || g>>8 > 90 {
		t.Fatalf("thumbnail centre is rgb(%d,%d,%d); the picture's colour did not survive the blit", r>>8, g>>8, b>>8)
	}
}

func TestFeBuildFileThumbnailCachesMemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beach.png")
	writeFeTestPNG(t, path, 200, 120, color.RGBA{R: 40, G: 200, B: 120, A: 255})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	entry := FileEntry{Name: "beach.png", Path: path, Size: info.Size(), Mod: info.ModTime()}
	key := feThumbKeyPrefix + feThumbKey(entry)
	dropFeThumbCaches(key)
	t.Cleanup(func() { dropFeThumbCaches(key) })

	feBuildFileThumbnail(feThumbTask{key: key, path: path})
	if len(thumbnailDataGet(key)) == 0 {
		t.Fatal("the worker produced no thumbnail for a plain PNG")
	}

	// A restart finds it on the card instead of decoding the folder again.
	thumbnailCacheMutex.Lock()
	thumbnailDataCache.drop(key)
	thumbnailCacheMutex.Unlock()
	feBuildFileThumbnail(feThumbTask{key: key, path: path})
	if len(thumbnailDataGet(key)) == 0 {
		t.Fatal("the disk copy did not survive a memory-cache drop, so every launch decodes the folder again")
	}
}

// dropFeThumbCaches forgets one thumbnail everywhere, so a probe starts cold
// and leaves the developer's cache as it found it.
func dropFeThumbCaches(key string) {
	thumbnailCacheMutex.Lock()
	thumbnailDataCache.drop(key)
	thumbnailCacheMutex.Unlock()
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheDelete(db, key)
	}
}

// TestFilesRowShowsThePictureInTheChipSlot is the visible half: the chip slot
// of an image row must hold the picture, and a row with nothing to preview must
// keep its type badge instead.
func TestFilesRowShowsThePictureInTheChipSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatal("no font for the row; the probe would prove nothing")
	}

	dir := t.TempDir()
	photo := filepath.Join(dir, "beach.png")
	writeFeTestPNG(t, photo, 160, 100, color.RGBA{R: 220, G: 40, B: 190, A: 255})
	info, err := os.Stat(photo)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	entry := FileEntry{Name: "beach.png", Path: photo, Size: info.Size(), Mod: info.ModTime()}
	key := feThumbKeyPrefix + feThumbKey(entry)
	dropFeThumbCaches(key)

	// Seed exactly what a worker would have cached, so the probe tests the row
	// rather than the queue's timing.
	data, err := feImageThumbnail(photo)
	if err != nil {
		t.Fatalf("feImageThumbnail: %v", err)
	}
	thumbnailDataSet(key, data)
	getThumbnailTextures().Clear()
	t.Cleanup(func() {
		getThumbnailTextures().Clear()
		dropFeThumbCaches(key)
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	bg := sdl.Color{R: 9, G: 14, B: 25, A: 255}
	buf := make([]byte, probeW*probeH*4)
	renderer.SetDrawColor(bg.R, bg.G, bg.B, 255)
	renderer.Clear()

	const rowH = int32(36)
	// A frame number this process has not drawn yet opens a fresh decode
	// budget, so the seed is decoded rather than deferred to a prefetch.
	frameCount += 1
	renderFilesRow(renderer, config, font, entry, 0, 0, 300, rowH, false)
	textRow := FileEntry{Name: "notes.txt", Path: filepath.Join(dir, "notes.txt")}
	renderFilesRow(renderer, config, font, textRow, 0, rowH+4, 300, rowH, false)

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels: %v", err)
	}
	at := func(x, y int32) sdl.Color {
		i := (y*probeW + x) * 4
		return sdl.Color{B: buf[i], G: buf[i+1], R: buf[i+2], A: buf[i+3]}
	}
	// The chip slot: 12 px in from the row's left edge, 44 wide, 28 tall.
	painted := 0
	for y := int32(4); y < rowH-4; y++ {
		for x := int32(12); x < 12+44; x++ {
			c := at(x, y)
			if c.R > 150 && c.B > 120 && c.G < 100 {
				painted++
			}
		}
	}
	if painted < 300 {
		t.Fatalf("the image row painted %d picture pixels in its chip slot; the thumbnail is missing or the wrong picture", painted)
	}

	badgeRow := 0
	for y := rowH + 4; y < 2*rowH+4; y++ {
		for x := int32(12); x < 12+44; x++ {
			c := at(x, y)
			if c.R > 150 && c.B > 120 && c.G < 100 {
				badgeRow++
			}
		}
	}
	if badgeRow != 0 {
		t.Fatalf("the text row painted %d picture pixels; a file with no preview must keep its type badge", badgeRow)
	}
}

// TestFeVideoThumbnailGrabsTheFirstFrame drives the ffmpeg producer on a clip
// this test makes, and skips on a machine without the tool: the row is meant to
// fall back to its type chip there, which is a different behaviour.
func TestFeVideoThumbnailGrabsTheFirstFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("ffmpeg round trip skipped in -short mode")
	}
	ffmpeg := feThumbFFmpeg()
	if ffmpeg == "" {
		t.Skip("ffmpeg is not installed here; a row then keeps its type chip")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	if err := runQuietCommand(ffmpeg, []string{
		"-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=128x72:rate=10",
		"-t", "2", "-pix_fmt", "yuv420p",
		"-y", clip,
	}, time.Minute); err != nil {
		t.Skipf("this ffmpeg cannot build a test clip: %v", err)
	}

	data, err := feFFmpegThumbnail(clip, true)
	if err != nil {
		t.Fatalf("feFFmpegThumbnail: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the grabbed frame does not decode: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("frame format = %q, want jpeg", format)
	}
	if cfg.Width != int(feThumbW) || cfg.Height != int(feThumbH) {
		t.Fatalf("frame is %dx%d, want %dx%d", cfg.Width, cfg.Height, feThumbW, feThumbH)
	}
}
