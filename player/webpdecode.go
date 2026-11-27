package main

// webpdecode.go — WebP pictures where SDL_image cannot decode them (backlog
// MED-37).
//
// Stock Smart Pro 1.1.0's SDL_image has no WebP decoder and there is no system
// libwebp (docs/HARDWARE_VALIDATION.md, "Image codecs"); the Windows
// SDL2_image.dll lacks the libwebp-2.dll it would load. webpDecodable used to
// hide every .webp there: from the image viewer, Files and box art. The
// firmware's FFmpeg decodes WebP, so where SDL_image cannot and an ffmpeg is on
// the card, a picture goes through one ffmpeg frame instead: its size is read
// from the header first (webpImageSize) and refused past the caller's pixel cap
// before anything is allocated, ffmpeg shrinks it to the longest edge the
// caller can use, and the pixels come back as raw RGBA — never on the render
// thread. Box art, which every library loader opens by path through SDL_image,
// gets a converted copy in a bounded cache folder instead (webpCoverPath),
// made by one background worker. With neither decoder a .webp stays hidden, as
// before.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// webpDecoder is how this installation decodes a WebP picture.
type webpDecoder int

const (
	webpDecoderNone   webpDecoder = iota // neither: a .webp stays hidden
	webpDecoderSDL                       // SDL_image, in-process
	webpDecoderFFmpeg                    // one ffmpeg frame (this file)
)

const (
	// webpToolRecheck is how long the answer "is there an ffmpeg" is kept:
	// isImageExtension asks for every .webp row a Files frame draws.
	webpToolRecheck = 30 * time.Second
	// webpHeaderBytes is enough of a file for webpImageSize.
	webpHeaderBytes = 64
	// webpFFmpegTimeout bounds one decode; a still picture takes well under a
	// second.
	webpFFmpegTimeout = 20 * time.Second
	// webpCoverMaxEdge is a converted cover's longest side: the library
	// decodes covers at most this large (libraryArtDecodeMaxEdge).
	webpCoverMaxEdge = 512
	// webpCoverCacheMax bounds the converted covers on the card (about 3,000
	// covers at that size); past it the oldest go.
	webpCoverCacheMax = 256 << 20
	// webpCoverQueueDepth is how many covers may wait for the worker; a full
	// queue drops the ask, which the next library scan repeats.
	webpCoverQueueDepth = 512
	// webpCoverRetry is how long a cover ffmpeg could not convert waits
	// before it is tried again.
	webpCoverRetry = 10 * time.Minute
)

// webpDecoderChoice is the decoder for a WebP picture: SDL_image when it really
// decodes one (webpDecodable's probe), else the card's ffmpeg, else none. Pure.
func webpDecoderChoice(sdlOK, ffmpegOK bool) webpDecoder {
	switch {
	case sdlOK:
		return webpDecoderSDL
	case ffmpegOK:
		return webpDecoderFFmpeg
	}
	return webpDecoderNone
}

// webpCurrentDecoder is webpDecoderChoice for this installation.
func webpCurrentDecoder() webpDecoder {
	if webpDecodable() {
		return webpDecoderSDL
	}
	return webpDecoderChoice(false, webpFFmpegTool() != "")
}

var webpTool struct {
	mu         sync.Mutex
	at         time.Time
	path       string
	refreshing bool // a goroutine is looking it up again
}

// webpFFmpegTool is the ffmpeg on the card (never a download: feThumbFFmpeg).
// Only the very first ask looks it up in place; after that the last answer is
// returned at once and, when older than webpToolRecheck, refreshed on a
// goroutine — isImageExtension asks per row per frame, and the lookup (a Stat
// and exec.LookPath's walk over PATH) must not land on the render thread.
func webpFFmpegTool() string {
	webpTool.mu.Lock()
	defer webpTool.mu.Unlock()
	now := time.Now()
	if webpTool.at.IsZero() {
		webpTool.path = feThumbFFmpeg()
		webpTool.at = now
		return webpTool.path
	}
	if !webpTool.refreshing && (now.Sub(webpTool.at) >= webpToolRecheck || now.Before(webpTool.at)) {
		webpTool.refreshing = true
		go func() {
			path := feThumbFFmpeg()
			webpTool.mu.Lock()
			webpTool.path, webpTool.at, webpTool.refreshing = path, time.Now(), false
			webpTool.mu.Unlock()
		}()
	}
	return webpTool.path
}

// webpFFmpegFallback reports whether path, a picture SDL_image could not
// decode, should go through ffmpeg: a .webp, with ffmpeg the decoder here.
func webpFFmpegFallback(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".webp") && webpCurrentDecoder() == webpDecoderFFmpeg
}

// webpFileSize reads a WebP file's size from its header; ok is false for a
// file that is not a WebP.
func webpFileSize(path string) (int, int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	head := make([]byte, webpHeaderBytes)
	n, _ := io.ReadFull(f, head)
	return webpImageSize(head[:n])
}

// webpFFmpegArgs is the command line that decodes one frame of path, scaled to
// exactly w×h, as raw straight-alpha RGBA on stdout.
func webpFFmpegArgs(path string, w, h int) []string {
	if !filepath.IsAbs(path) {
		path = "file:" + path // a relative name starting with "-" is no option
	}
	return []string{"-nostdin", "-v", "error", "-i", path, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:flags=area", w, h),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"}
}

// errWebPUnreadable is the viewer's usual words for a picture it cannot open.
var errWebPUnreadable = errors.New("this picture format cannot be opened")

// webpDecodeFFmpeg decodes the WebP at path through ffmpeg, shrunk to fit
// maxEdge×maxEdge (0: its own size). A picture over maxPixels (0: no cap) is
// refused from its header. Never on the render thread: it runs a process.
func webpDecodeFFmpeg(path string, maxEdge int, maxPixels int64) (*image.NRGBA, error) {
	w, h, ok := webpFileSize(path)
	if !ok {
		return nil, errWebPUnreadable
	}
	if maxPixels > 0 && int64(w)*int64(h) > maxPixels {
		return nil, fmt.Errorf("it is %d × %d, too large to open here", w, h)
	}
	tool := webpFFmpegTool()
	if tool == "" {
		return nil, errWebPUnreadable
	}
	ow, oh := w, h
	if maxEdge > 0 {
		ow, oh = imgvFitInside(w, h, maxEdge, maxEdge)
	}
	ctx, cancel := context.WithTimeout(context.Background(), webpFFmpegTimeout)
	defer cancel()
	cmd := P().CommandContext(ctx, tool, webpFFmpegArgs(path, ow, oh)...)
	if cmd == nil {
		return nil, errWebPUnreadable
	}
	tail := &lockedTailBuffer{max: 2 << 10}
	cmd.Stderr = tail
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errWebPUnreadable
	}
	if err := cmd.Start(); err != nil {
		logWarn("imageviewer", "ffmpeg could not start for a WebP picture: %s", ffmpegStartReason(err))
		return nil, errWebPUnreadable
	}
	pic := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	_, readErr := io.ReadFull(stdout, pic.Pix)
	if readErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		logDebug("imageviewer", "ffmpeg could not decode %s (%v): %s", filepath.Base(path), webpFirstErr(readErr, waitErr), ffmpegFailureReason(tail.String()))
		return nil, errWebPUnreadable
	}
	return pic, nil
}

// webpDecodeImage is webpDecodeFFmpeg as an image.Image: nil, not a nil
// *image.NRGBA inside an interface, when it fails.
func webpDecodeImage(path string, maxEdge int, maxPixels int64) (image.Image, error) {
	pic, err := webpDecodeFFmpeg(path, maxEdge, maxPixels)
	if err != nil {
		return nil, err
	}
	return pic, nil
}

// webpBytes reports whether data is a RIFF/WEBP file.
func webpBytes(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// webpDecodeBytes decodes WebP bytes through the card's ffmpeg, using a
// temporary file. maxEdge 0 keeps the picture's own size.
func webpDecodeBytes(data []byte, maxEdge int, maxPixels int64) (image.Image, error) {
	if !webpBytes(data) {
		return nil, errWebPUnreadable
	}
	f, err := os.CreateTemp("", "jukahub-webp-*")
	if err != nil {
		return nil, errWebPUnreadable
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(name)
		return nil, errWebPUnreadable
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return nil, errWebPUnreadable
	}
	defer os.Remove(name)
	return webpDecodeImage(name, maxEdge, maxPixels)
}

func webpFirstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// ── Box art ────────────────────────────────────────────────────────────────

// artworkWebPCover is the picture to show for a .webp cover on the card (NX
// Redux and other art packs), or "" when there is none to show:
// romLocalCoverPath's answer for one.
func artworkWebPCover(path string) string {
	if !artworkLocalImage(path, artworkIsWebP) {
		return ""
	}
	switch webpCurrentDecoder() {
	case webpDecoderSDL:
		return path
	case webpDecoderFFmpeg:
		return webpCoverPath(path)
	}
	return ""
}

// webpCoverPath is the converted copy of a .webp cover once the worker has made
// it. Until then it is the .webp itself, with the conversion queued: the ROM
// still has its own cover (so no download replaces it), it just draws the
// placeholder until a library refresh picks the copy up. A cover that cannot
// be converted (no data folder, or ffmpeg refused it within webpCoverRetry,
// e.g. an animated WebP) is "": not a cover, so the game counts as missing art
// and a download may fill in. A few stats; no decode.
func webpCoverPath(path string) string {
	dir := webpCoverCacheDir()
	info, err := os.Stat(path)
	if dir == "" || err != nil {
		return ""
	}
	key := webpCoverKey(path, info)
	if v, ok := webpCovers.failed.Load(key); ok {
		if at, _ := v.(time.Time); time.Since(at) < webpCoverRetry {
			return ""
		}
	}
	for _, ext := range []string{".jpg", ".png"} {
		p := filepath.Join(dir, key+ext)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return p
		}
	}
	webpCoverQueue(path, key)
	return path
}

// webpCoverCacheDir is the converted covers' folder, "" with no data folder.
func webpCoverCacheDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "webp-covers")
}

// webpCoverKey names one converted cover: the file, its size and its time, so
// a replaced cover gets a new copy rather than the old one.
func webpCoverKey(path string, info os.FileInfo) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", filepath.Clean(path), info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:16])
}

type webpCoverJob struct {
	path, key string
}

var webpCovers struct {
	once     sync.Once
	queue    chan webpCoverJob
	inFlight sync.Map // key -> struct{}
	failed   sync.Map // key -> time.Time
	// bytes is what the folder holds, -1 until the worker has looked. The
	// worker's goroutine only.
	bytes int64
}

// webpCoverQueue asks the worker for one cover and returns at once.
func webpCoverQueue(path, key string) {
	if v, ok := webpCovers.failed.Load(key); ok {
		if at, _ := v.(time.Time); time.Since(at) < webpCoverRetry {
			return
		}
		webpCovers.failed.Delete(key)
	}
	webpCovers.once.Do(func() {
		webpCovers.queue = make(chan webpCoverJob, webpCoverQueueDepth)
		webpCovers.bytes = -1
		go webpCoverWorker()
	})
	if _, busy := webpCovers.inFlight.LoadOrStore(key, struct{}{}); busy {
		return
	}
	select {
	case webpCovers.queue <- webpCoverJob{path: path, key: key}:
	default:
		webpCovers.inFlight.Delete(key)
	}
}

func webpCoverWorker() {
	for job := range webpCovers.queue {
		webpCoverMake(job)
		webpCovers.inFlight.Delete(job.key)
	}
}

// webpCoverMake converts one cover: JPEG when it is opaque (a box-art scan),
// PNG when it has transparency, written atomically, then the folder is kept
// under webpCoverCacheMax.
func webpCoverMake(job webpCoverJob) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("artwork", "converting a WebP cover failed: %v", r)
			webpCovers.failed.Store(job.key, time.Now())
		}
	}()
	dir := webpCoverCacheDir()
	if dir == "" {
		return
	}
	// The shared decode permit: a burst of covers must not run beside the
	// thumbnails and chat pictures, which is what bounds peak RAM.
	if !imageDecodeSem.Acquire(context.Background(), 1) {
		return
	}
	pic, err := webpDecodeFFmpeg(job.path, webpCoverMaxEdge, imageDecodeMaxPixels())
	imageDecodeSem.Release(1)
	if err != nil {
		webpCovers.failed.Store(job.key, time.Now())
		return
	}
	var buf bytes.Buffer
	ext := ".jpg"
	if pic.Opaque() {
		err = jpeg.Encode(&buf, pic, &jpeg.Options{Quality: 90})
	} else {
		ext = ".png"
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, pic)
	}
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = AtomicWrite(filepath.Join(dir, job.key+ext), buf.Bytes(), 0o644)
	}
	if err != nil {
		logWarn("artwork", "could not keep a converted WebP cover: %v", err)
		webpCovers.failed.Store(job.key, time.Now())
		return
	}
	if webpCovers.bytes < 0 {
		webpCovers.bytes = webpCoverPrune(dir, webpCoverCacheMax)
	} else if webpCovers.bytes += int64(buf.Len()); webpCovers.bytes > webpCoverCacheMax {
		webpCovers.bytes = webpCoverPrune(dir, webpCoverCacheMax)
	}
}

// webpCoverPrune removes the oldest converted covers until the folder holds at
// most three quarters of limit (once it is over limit), and returns what is
// left.
func webpCoverPrune(dir string, limit int64) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type file struct {
		name string
		size int64
		mod  time.Time
	}
	files := make([]file, 0, len(entries))
	total := int64(0)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= limit {
		return total
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= limit*3/4 {
			break
		}
		if os.Remove(filepath.Join(dir, f.name)) == nil {
			total -= f.size
		}
	}
	return total
}
