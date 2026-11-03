package main

// filethumb.go — the picture and first-frame thumbnails the Files list draws in
// a row's leading chip.
//
// The network thumbnail pipeline (thumbstore.go) is keyed by URL: it downloads
// bytes and caches them. A file on the card is the same problem with a local
// source, so this file reuses that machinery's caches and decode budget and
// swaps in two producers: the Go image decoders for pictures, and one ffmpeg
// grab for a video's first frame (also the fallback for a picture format the Go
// decoders do not know). Generation runs on background workers because a folder
// of camera JPEGs takes a second to decode and the render loop has 16 ms; the
// render path only ever reads bytes that are already cached.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	// feThumbW and feThumbH are the chip footprint a row thumbnail fills. They
	// are the size of the type chip, so a row that gains a picture keeps its
	// name and metadata columns exactly where they were, and a folder of mixed
	// files does not stagger its text.
	feThumbW = int32(44)
	feThumbH = int32(28)

	// feThumbKeyPrefix keys both the byte caches (memory and disk) and the
	// texture cache, keeping a file thumbnail clear of the URL-keyed entries
	// that share those caches.
	feThumbKeyPrefix = "fthumb:"

	// feThumbTTL bounds the disk cache. The key already carries the file's
	// size and modification time, so an edited picture gets a new entry rather
	// than a stale one; the TTL only stops the cache growing forever on a card
	// that collects folders.
	feThumbTTL = 30 * 24 * time.Hour

	// feThumbQueueDepth is how many rows may wait for a thumbnail. A screenful
	// is about a dozen rows; the depth leaves room for the rows a fast scroll
	// throws at the queue without letting a folder of thousands of pictures
	// park work it will never draw.
	feThumbQueueDepth = 48

	// feThumbMaxPixels refuses to decode a picture whose pixels would cost more
	// RAM than the thumbnail is worth. A 48-megapixel photo is a 192 MB bitmap,
	// which is a noticeable slice of a handheld's 1 GB; such a row keeps its
	// type chip instead of a picture.
	feThumbMaxPixels = 24 << 20

	// feThumbSampleCap is how many source pixels per axis one thumbnail pixel
	// averages. The full box of a camera photo is 40 pixels across, and summing
	// all of it for 1232 destination pixels is more work than a 44-pixel wide
	// thumbnail can show; capping the sample grid keeps a folder fast without
	// visible banding.
	feThumbSampleCap = 4

	// feThumbFrameTimeout bounds one ffmpeg grab. A frame is read from the
	// first megabyte of a file, so the only way this takes long is a broken
	// container, and the worker must not sit on it.
	feThumbFrameTimeout = 20 * time.Second

	// feThumbRetryAfter is how long a file that produced no thumbnail waits
	// before its row asks again. It is long enough that a folder of unreadable
	// files costs one attempt each rather than one per frame, and short enough
	// that a video whose frame needs the ffmpeg the app installs at startup
	// gets its picture in the same session.
	feThumbRetryAfter = 2 * time.Minute
)

// errFeThumbTooLarge marks the size refusal, so the caller does not hand the
// same picture to ffmpeg and allocate the pixels there instead.
var errFeThumbTooLarge = errors.New("picture is too large for a thumbnail")

// feThumbTask is one queued thumbnail: the cache key it belongs to and the file
// to read. Both travel together because a worker that found the thumbnail on
// disk still needs the key to publish the bytes.
type feThumbTask struct {
	key  string
	path string
}

var (
	// feThumbQueue carries tasks from the render thread to the workers. It is
	// created by the workers' once; a request never sees it nil.
	feThumbQueue chan feThumbTask
	feThumbOnce  sync.Once

	// feThumbInFlight dedupes requests: the rows of a list ask again on every
	// frame until their picture arrives, and a task per frame per row would
	// bury the queue.
	feThumbInFlight sync.Map

	// feThumbFailed remembers the files that produced no thumbnail: a broken
	// container, a codec this ffmpeg build cannot read, or ffmpeg itself
	// missing. A row would otherwise ask again every frame, and each ask is a
	// cache lookup on the card for a file that will never answer.
	feThumbFailed sync.Map
)

// feThumbnailVideo reports whether a name is a video whose first frame is worth
// grabbing. The audio extensions in mediaExtensions are the reason this is not
// isMediaFile: a song has no picture to show, and running ffmpeg once per MP3
// would be the most expensive way to find that out.
func feThumbnailVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv",
		".wmv", ".mpg", ".mpeg", ".ts", ".m2ts", ".m3u8":
		return true
	}
	return false
}

// feThumbnailEligible reports whether a row's leading chip should be a
// thumbnail. Directories and the ".." row keep their badge: there is nothing to
// preview, and the row that goes back up must not look like a file.
func feThumbnailEligible(entry FileEntry) bool {
	if entry.IsDir || entry.Name == ".." || entry.Path == "" {
		return false
	}
	if isImageExtension(filepath.Ext(entry.Name)) {
		return true
	}
	return feThumbnailVideo(entry.Name)
}

// feThumbKey names one cached thumbnail. The path alone is not enough: a
// picture trimmed in another app keeps its name, and the row must show the new
// photo rather than the one the cache remembers.
func feThumbKey(entry FileEntry) string {
	mod := int64(0)
	if !entry.Mod.IsZero() {
		mod = entry.Mod.UnixNano()
	}
	return fmt.Sprintf("%s|%d|%d", filepath.Clean(entry.Path), entry.Size, mod)
}

// feFileThumbnail returns the row's thumbnail texture, or nil while there is
// nothing to draw. The render thread only ever reads caches here: a miss queues
// the work and draws the type chip for now, so scrolling a folder of photos
// never spends a frame decoding them.
func feFileThumbnail(renderer *sdl.Renderer, entry FileEntry) *sdl.Texture {
	if renderer == nil || !feThumbnailEligible(entry) {
		return nil
	}
	cacheKey := feThumbKeyPrefix + feThumbKey(entry)
	if feThumbRecentlyFailed(cacheKey) {
		return nil
	}
	if tex := getThumbnailTextures().Get(cacheKey); tex != nil {
		return tex
	}
	data := thumbnailDataGet(cacheKey)
	if len(data) == 0 {
		// The workers publish what they make in the byte cache, so a row whose
		// picture is still being made asks once and then waits.
		feThumbnailRequest(cacheKey, entry.Path)
		return nil
	}
	// Decoding a picture is still a decode: it spends the same per-frame budget
	// as a downloaded thumbnail, so a cold folder fills in over several frames
	// instead of stalling the one the user scrolled into.
	if !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
		return nil
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil {
		return nil
	}
	tex, err := img.LoadTextureRW(renderer, rwops, false)
	rwops.Close()
	if err != nil {
		return nil
	}
	var w, h int32
	if _, _, qw, qh, qerr := tex.Query(); qerr == nil {
		w, h = qw, qh
	}
	getThumbnailTextures().Put(cacheKey, tex, w, h)
	return tex
}

// feThumbRecentlyFailed reports whether this file's row should stop asking for
// a thumbnail yet.
func feThumbRecentlyFailed(cacheKey string) bool {
	value, ok := feThumbFailed.Load(cacheKey)
	if !ok {
		return false
	}
	at, _ := value.(time.Time)
	return time.Since(at) < feThumbRetryAfter
}

// feThumbnailRequest queues a thumbnail and returns immediately. A full queue
// drops the request: the row asks again on the next frame, which is cheaper
// than blocking the render thread behind a folder's worth of work.
func feThumbnailRequest(cacheKey, path string) {
	feThumbOnce.Do(startFeThumbWorkers)
	if _, busy := feThumbInFlight.LoadOrStore(cacheKey, struct{}{}); busy {
		return
	}
	select {
	case feThumbQueue <- feThumbTask{key: cacheKey, path: path}:
	default:
		feThumbInFlight.Delete(cacheKey)
	}
}

// startFeThumbWorkers runs the thumbnail producers. Two of them overlap the
// decode and the ffmpeg process without competing with the video path for the
// handheld's cores; the count comes from the same device-aware helper the
// download workers use.
func startFeThumbWorkers() {
	feThumbQueue = make(chan feThumbTask, feThumbQueueDepth)
	for i := 0; i < thumbnailWorkerCount(); i++ {
		go func() {
			for task := range feThumbQueue {
				feBuildFileThumbnail(task)
				feThumbInFlight.Delete(task.key)
			}
		}()
	}
}

// feBuildFileThumbnail produces one thumbnail: memory cache first, then the
// disk cache (so a restart does not decode the folder again), then the file.
func feBuildFileThumbnail(task feThumbTask) {
	if task.key == "" || task.path == "" {
		return
	}
	if thumbnailDataGet(task.key) != nil {
		return
	}
	if data := feThumbDiskGet(task.key); len(data) > 0 {
		thumbnailDataSet(task.key, data)
		return
	}
	data, err := feGenerateThumbnail(task.path)
	if err != nil || len(data) == 0 {
		// No thumbnail is a normal outcome (a codec this ffmpeg build cannot
		// read, a corrupt picture, ffmpeg not installed). The row keeps its
		// type chip, and remembering the failure stops it asking again every
		// frame; logging each one would fill the log with the folder's
		// misfits.
		feThumbFailed.Store(task.key, time.Now())
		return
	}
	thumbnailDataSet(task.key, data)
	feThumbDiskPut(task.key, data)
}

func feThumbDiskGet(key string) []byte {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return nil
	}
	data, err := cacheGet(db, feThumbKeyPrefix+key)
	if err != nil {
		return nil
	}
	return data
}

func feThumbDiskPut(key string, data []byte) {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return
	}
	_ = cacheSet(db, feThumbKeyPrefix+key, data, feThumbTTL)
}

// feGenerateThumbnail produces the thumbnail bytes for one file. Pictures are
// decoded and shrunk here; videos go through ffmpeg for their frame.
func feGenerateThumbnail(path string) ([]byte, error) {
	if isImageExtension(filepath.Ext(path)) {
		data, err := feImageThumbnail(path)
		if err == nil && len(data) > 0 {
			return data, nil
		}
		if errors.Is(err, errFeThumbTooLarge) {
			return nil, err
		}
		// A picture the Go decoders do not know (WebP, TIFF) is still a frame
		// ffmpeg can read, so it gets one attempt before the row gives up.
		if data, fErr := feFFmpegThumbnail(path, false); fErr == nil && len(data) > 0 {
			return data, nil
		}
		return nil, err
	}
	if feThumbnailVideo(filepath.Base(path)) {
		return feFFmpegThumbnail(path, true)
	}
	return nil, fmt.Errorf("no thumbnail source for %q", filepath.Base(path))
}

// feThumbDecodeAllowed reports whether a picture of this size may be decoded
// for a thumbnail. It is a plain function so the ceiling is testable without a
// 48-megapixel file on disk.
func feThumbDecodeAllowed(w, h int32) bool {
	if w <= 0 || h <= 0 {
		return true // unknown dimensions: let the decoder answer
	}
	return int64(w)*int64(h) <= feThumbMaxPixels
}

// feImageThumbnail decodes a picture and shrinks it to the chip footprint. The
// shrunken copy is what gets cached, so the folder is decoded once rather than
// on every frame that draws the row.
func feImageThumbnail(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// The header is read before the pixels: a 48-megapixel picture is refused
	// while it is still four integers, not after it has been allocated.
	if cfg, _, cfgErr := image.DecodeConfig(file); cfgErr == nil {
		if !feThumbDecodeAllowed(int32(cfg.Width), int32(cfg.Height)) {
			return nil, fmt.Errorf("%s is %dx%d: %w", filepath.Base(path), cfg.Width, cfg.Height, errFeThumbTooLarge)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	} else if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	pic, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	chip := feScaleToChip(pic, feThumbCoverRect(pic.Bounds()))
	var buf bytes.Buffer
	if err := png.Encode(&buf, chip); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// feThumbCoverRect is the source rectangle that fills the chip without
// squashing the picture: the longer side is cropped, which is what a gallery
// thumbnail does.
func feThumbCoverRect(bounds image.Rectangle) image.Rectangle {
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return bounds
	}
	target := float64(feThumbW) / float64(feThumbH)
	aspect := float64(w) / float64(h)
	if aspect > target {
		cw := int(float64(h) * target)
		if cw < 1 {
			cw = 1
		}
		x0 := bounds.Min.X + (w-cw)/2
		return image.Rect(x0, bounds.Min.Y, x0+cw, bounds.Max.Y)
	}
	ch := int(float64(w) / target)
	if ch < 1 {
		ch = 1
	}
	y0 := bounds.Min.Y + (h-ch)/2
	return image.Rect(bounds.Min.X, y0, bounds.Max.X, y0+ch)
}

// feScaleToChip averages crop down to the chip size. A box average rather than
// a point sample: a single sample of a photo lands on one noisy pixel, and a
// 44-pixel thumbnail of a face is exactly where that shows.
func feScaleToChip(src image.Image, crop image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, int(feThumbW), int(feThumbH)))
	if crop.Empty() {
		return dst
	}
	scaleX := float64(crop.Dx()) / float64(feThumbW)
	scaleY := float64(crop.Dy()) / float64(feThumbH)
	for y := 0; y < int(feThumbH); y++ {
		y0 := crop.Min.Y + int(float64(y)*scaleY)
		y1 := crop.Min.Y + int(float64(y+1)*scaleY)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < int(feThumbW); x++ {
			x0 := crop.Min.X + int(float64(x)*scaleX)
			x1 := crop.Min.X + int(float64(x+1)*scaleX)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb uint64
			var n uint64
			stepX := (x1 - x0) / feThumbSampleCap
			if stepX < 1 {
				stepX = 1
			}
			stepY := (y1 - y0) / feThumbSampleCap
			if stepY < 1 {
				stepY = 1
			}
			for sy := y0; sy < y1; sy += stepY {
				for sx := x0; sx < x1; sx += stepX {
					r, g, b, _ := src.At(sx, sy).RGBA()
					sr += uint64(r >> 8)
					sg += uint64(g >> 8)
					sb += uint64(b >> 8)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(sr / n)
			dst.Pix[i+1] = uint8(sg / n)
			dst.Pix[i+2] = uint8(sb / n)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// feFFmpegThumbnail grabs a frame with ffmpeg: a video's poster frame, or a
// picture in a format the Go decoders do not read. A frame a second in is more
// representative of a video than the black fade a fresh recording starts with,
// so the seek is tried first and a clip shorter than the seek falls back to its
// opening frame; a still picture is read at once.
func feFFmpegThumbnail(path string, video bool) ([]byte, error) {
	ffmpeg := feThumbFFmpeg()
	if ffmpeg == "" {
		return nil, fmt.Errorf("ffmpeg is not installed")
	}
	seeks := []string{"0"}
	if video {
		seeks = []string{"1", "0"}
	}
	var lastErr error
	for _, seek := range seeks {
		data, err := feFFmpegFrame(ffmpeg, path, seek)
		if err == nil && len(data) > 0 {
			return data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ffmpeg produced no frame for %q", filepath.Base(path))
	}
	return nil, lastErr
}

func feFFmpegFrame(ffmpeg, path, seek string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "jukahub-frame-*.jpg")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d",
		feThumbW, feThumbH, feThumbW, feThumbH)
	if err := runQuietCommand(ffmpeg, []string{
		"-loglevel", "error",
		"-ss", seek,
		"-i", path,
		"-frames:v", "1",
		"-vf", scale,
		"-y", name,
	}, feThumbFrameTimeout); err != nil {
		return nil, err
	}
	return os.ReadFile(name)
}

// feThumbFFmpeg resolves the bundled ffmpeg without downloading it: a
// thumbnail is not worth a network install in the middle of scrolling, and the
// row simply keeps its type chip until the tool is there.
func feThumbFFmpeg() string {
	path := strings.TrimSpace(getToolPath("ffmpeg", nil))
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return path
}

// drawFeThumbnail paints a row's picture in the chip slot. The backdrop keeps a
// transparent picture from showing the row fill through it, and the frame gives
// every picture the same edge so a light photo does not bleed into the row.
func drawFeThumbnail(renderer *sdl.Renderer, entry FileEntry, tex *sdl.Texture, x, y int32) {
	if renderer == nil || tex == nil {
		return
	}
	dst := sdl.Rect{X: x, Y: y, W: feThumbW, H: feThumbH}
	renderer.SetDrawColor(12, 16, 26, 255)
	renderer.FillRect(&dst)
	renderer.Copy(tex, nil, &dst)
	if renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 200) == nil {
		renderer.DrawRect(&dst)
	}
	if feThumbnailVideo(entry.Name) {
		// The picture of a video is a still, so it carries the play mark the
		// media rows use; without it a frame is indistinguishable from a photo.
		feDrawPlayMark(renderer, dst.X+feThumbW/2, dst.Y+feThumbH/2)
	}
}

// feDrawPlayMark draws the small play badge a video thumbnail carries: a dark
// disc so the triangle reads over any picture, then the triangle itself.
func feDrawPlayMark(renderer *sdl.Renderer, cx, cy int32) {
	// Kept small: the frame is what the row is showing, so the mark sits on it
	// rather than over it.
	fillCircle(renderer, cx, cy, 6, sdl.Color{R: 0, G: 0, B: 0, A: 140})
	fillTriangleFilled(renderer,
		pt{x: cx - 2, y: cy - 4},
		pt{x: cx - 2, y: cy + 4},
		pt{x: cx + 4, y: cy},
		ColorTextPrimary())
}
