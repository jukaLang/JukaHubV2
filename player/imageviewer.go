package main

// imageviewer.go — the picture viewer Files (and chat) open over the screen.
//
// It is a full-screen photo viewer now: the picture is decoded off the render
// thread (a camera JPEG takes a second on the handheld, and the old viewer
// decoded it inside a frame), shrunk once to what the GPU should hold, and from
// then on every zoom and pan is a texture copy. Zoom used to do nothing past
// "fit": the draw clamped the scale back to the screen after applying it.
//
//	pad       L1/R1 zoom · D-pad pans when zoomed, browses when not · L2/R2
//	          previous/next picture · Y Fit → Fill → 1:1 · X lock screen ·
//	          A hides the controls · B closes
//	keyboard  +/- (and the wheel) zoom · arrows · 0 fit · 1 actual size ·
//	          F mode · L lock screen · H/Enter hide · PgUp/PgDn browse · Esc
//	mouse     wheel zooms at the pointer, drag pans, the chips click
//	touch     pinch zooms, a swipe pans
//
// "Set as lock screen" hands the picture to screenlock.go (setScreenlockImage
// validates and saves it); the viewer says what happened and marks the picture
// that is the lock screen.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// imageViewerCloseBtn and imageViewerLockBtn are the screen rects of the Close
// and lock-screen chips, set every frame by renderImageViewer. The lock rect
// stays empty for a remote image, whose file lives in the chat cache rather
// than in Files.
var (
	imageViewerCloseBtn sdl.Rect
	imageViewerLockBtn  sdl.Rect
)

// imageViewerFiles is the ordered list of images in the folder the viewer was
// opened from, so Left/Right (or the triggers) can walk them without going
// back to the file list.
var (
	imageViewerFiles []string
	imageViewerIndex int = -1
)

// imageViewerRemote marks the viewer as showing a downloaded image (a chat
// preview URL) instead of a file on disk.
var imageViewerRemote bool

// imgvFitMode is how the picture is sized when it is not zoomed by hand.
type imgvFitMode int

const (
	imgvFit    imgvFitMode = iota // the whole picture on screen
	imgvFill                      // the screen filled, the edges cropped
	imgvActual                    // one picture pixel per screen pixel
	imgvCustom                    // zoomed by hand
)

const (
	// imgvZoomStep is one press of zoom in or out.
	imgvZoomStep = 1.25
	// imgvMaxPixels refuses a picture whose decode alone would take a large
	// share of the handheld's RAM (a 64-megapixel panorama is 256 MB).
	imgvMaxPixels = 64 << 20
)

// imgvState is the viewer's own state beside the shared globals in core.go
// (imageViewerPath, imageViewerZoom, imageViewerPanX/Y, imageViewerTexture and
// its size).
type imgvState struct {
	mode   imgvFitMode
	hideUI bool
	// origW/origH is the picture's own size; the texture may be smaller.
	origW, origH int32
	// fitTex is a second, screen-sized copy: a 4000-pixel photo drawn at a
	// third of its size from the full texture shimmers, because the GPU
	// samples without mipmaps.
	fitTex     *sdl.Texture
	fitW, fitH int32
	fileSize   int64
	loading    bool
	errText    string
	// drag tracks a mouse press that may become a pan.
	dragOn, dragMoved bool
	dragX, dragY      int32
	chips             []filesChip
}

var imgv imgvState

// imgvDecoded is a finished background decode.
type imgvDecoded struct {
	gen          int
	full, fit    *image.NRGBA
	origW, origH int
	size         int64
	err          error
}

// imgvLoad hands decodes from the worker to the render thread: only the result
// for the picture currently shown (gen) is kept.
var imgvLoad struct {
	mu   sync.Mutex
	gen  int
	done *imgvDecoded
}

// openImageViewerURL shows an image that was fetched over the network, such as
// a link or attachment pasted into chat. The texture comes from the chat image
// cache, so the picture is already there when its thumbnail was drawn.
func openImageViewerURL(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	imageViewerFiles = nil
	imageViewerIndex = -1
	imgvReleaseTextures()
	imgv = imgvState{}
	imageViewerRemote = true
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
	imageViewerPath = url
	imageViewerLockBtn = sdl.Rect{}
}

// webpProbe is a 36-byte 1x1 lossless WebP. SDL_image reaches WebP through
// libwebp loaded at runtime, so a build can carry the WebP code path and still
// fail to decode (the Windows SDL2_image.dll needs libwebp-2.dll beside it,
// which the release archive does not ship). Decoding this tiny image once is
// the only honest capability check; the handheld build links libwebp into
// SDL2_image, so the same probe succeeds there.
var webpProbe = []byte{
	0x52, 0x49, 0x46, 0x46, 0x1c, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
	0x56, 0x50, 0x38, 0x4c, 0x0f, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00,
	0x00, 0x07, 0x10, 0xfd, 0x8f, 0xfe, 0x07, 0x22, 0xa2, 0xff, 0x01, 0x00,
}

var webpSupport struct {
	once sync.Once
	ok   bool
}

// webpDecodable reports whether this installation can really decode WebP. It is
// answered once and cached, because the answer depends on the runtime files
// next to the executable, not on anything that changes while the app runs.
func webpDecodable() bool {
	// Before SDL is up there is no meaningful answer and caching "no" would
	// stick for the whole process; callers only ask once the UI is running.
	if sdl.WasInit(sdl.INIT_VIDEO) == 0 {
		return false
	}
	webpSupport.once.Do(func() {
		rw, err := sdl.RWFromMem(webpProbe)
		if err != nil {
			return
		}
		defer rw.Close()
		surface, err := img.LoadTypedRW(rw, false, "WEBP")
		if err != nil {
			log.Printf("WebP images unavailable in this build: %v", err)
			return
		}
		surface.Free()
		webpSupport.ok = true
	})
	return webpSupport.ok
}

// isImageExtension reports whether a lower-case extension (with the dot) is an
// image this build can decode. WebP is offered only when the loaded SDL_image
// can actually decode it, so box art never shows up as a broken picture.
func isImageExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".bmp", ".gif":
		return true
	case ".webp":
		return webpDecodable()
	}
	return false
}

// isViewableImagePath reports whether the image decoder handles the file.
func isViewableImagePath(p string) bool {
	return isImageExtension(filepath.Ext(p))
}

// openImageViewer shows path and collects the other images in the current File
// Explorer folder as a browsable list. Non-Files callers (for example a chat
// attachment) get a one-image list.
func openImageViewer(config *Config, path string) {
	path = filepath.Clean(path)
	imageViewerFiles = nil
	imageViewerIndex = -1
	if config != nil {
		if entries, ok := config.Variables.Custom["fe_entries"].([]FileEntry); ok && !filesNav.atLocations {
			for _, entry := range entries {
				if entry.IsDir || !isViewableImagePath(entry.Name) {
					continue
				}
				resolved, err := resolveExplorerEntry(config, entry.Path)
				if err != nil {
					continue
				}
				imageViewerFiles = append(imageViewerFiles, resolved)
				if filepath.Clean(resolved) == path && imageViewerIndex < 0 {
					imageViewerIndex = len(imageViewerFiles) - 1
				}
			}
		}
	}
	if imageViewerIndex < 0 {
		imageViewerFiles = []string{path}
		imageViewerIndex = 0
	}
	imgv.hideUI = false
	imageViewerShow(path)
	PlayActivateSound()
}

// imageViewerShow swaps the displayed image without closing the viewer. The
// picture is decoded in the background; the viewer shows a loading state
// until it arrives.
func imageViewerShow(path string) {
	imgvReleaseTextures()
	hide := imgv.hideUI
	imgv = imgvState{hideUI: hide, loading: true}
	imageViewerRemote = false
	imageViewerPath = path
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
	imgvLoad.mu.Lock()
	imgvLoad.gen++
	gen := imgvLoad.gen
	imgvLoad.done = nil
	imgvLoad.mu.Unlock()
	maxDim := imgvMaxTextureDim()
	fitW, fitH := int(screenWidth), int(screenHeight)
	go imgvDecodeWorker(gen, path, maxDim, fitW, fitH)
}

// imgvMaxTextureDim is the longest side the full-size texture may have: the
// handheld's GPU and RAM are the limit there; a desktop GPU takes more.
func imgvMaxTextureDim() int {
	if IsTrimuiDevice() {
		return 2048
	}
	return 4096
}

// imageViewerStep moves through the folder's images, wrapping at both ends.
func imageViewerStep(delta int) {
	if len(imageViewerFiles) == 0 || delta == 0 {
		return
	}
	if imageViewerIndex < 0 || imageViewerIndex >= len(imageViewerFiles) {
		imageViewerIndex = 0
	}
	next := (imageViewerIndex + delta%len(imageViewerFiles) + len(imageViewerFiles)) % len(imageViewerFiles)
	if next == imageViewerIndex && imageViewerFiles[next] == imageViewerPath {
		return
	}
	imageViewerIndex = next
	imageViewerShow(imageViewerFiles[imageViewerIndex])
	PlayNavSound()
}

// imgvReleaseTextures frees the viewer's own textures (never a remote one: the
// chat cache owns those).
func imgvReleaseTextures() {
	if imageViewerTexture != nil {
		imageViewerTexture.Destroy()
		imageViewerTexture = nil
	}
	if imgv.fitTex != nil {
		imgv.fitTex.Destroy()
		imgv.fitTex = nil
	}
	imageViewerW, imageViewerH = 0, 0
}

// closeImageViewer tears down the viewer (B, Escape, the Close chip).
func closeImageViewer() {
	imgvReleaseTextures()
	imgvLoad.mu.Lock()
	imgvLoad.gen++
	imgvLoad.done = nil
	imgvLoad.mu.Unlock()
	imgv = imgvState{}
	imageViewerPath = ""
	imageViewerZoom = 1.0
	imageViewerPanX = 0
	imageViewerPanY = 0
	imageViewerFiles = nil
	imageViewerIndex = -1
	imageViewerRemote = false
	imageViewerCloseBtn = sdl.Rect{}
	imageViewerLockBtn = sdl.Rect{}
}

// toggleScreenlockFromViewer gives the picture on screen to the idle screen,
// or takes it back when it already is the backdrop. It is the one place the
// Files app writes that setting, so the keyboard shortcut, the pad button and
// the chip agree, and every outcome is said out loud.
func toggleScreenlockFromViewer(config *Config) {
	if imageViewerRemote || strings.TrimSpace(imageViewerPath) == "" {
		return
	}
	if screenlockImageIsCurrent(config, imageViewerPath) {
		if err := clearScreenlockImage(config); err != nil {
			showToast("Cannot remove the lock screen picture: "+err.Error(), ToastError())
			return
		}
		showToast("Lock screen picture removed — the default idle screen is back", ToastSuccess())
		return
	}
	if err := setScreenlockImage(config, imageViewerPath); err != nil {
		showToast("Cannot use this as the lock screen: "+err.Error(), ToastError())
		return
	}
	showToast("Lock screen set — "+filepath.Base(imageViewerPath)+" shows when the screen sleeps", ToastSuccess())
}

// --- Decoding ---------------------------------------------------------------

// imgvDecodeWorker decodes path, shrinks it to the texture limits and posts the
// result for generation gen. It runs on its own goroutine.
func imgvDecodeWorker(gen int, path string, maxDim, fitW, fitH int) {
	res := imgvDecoded{gen: gen}
	defer func() {
		if r := recover(); r != nil {
			res.err = fmt.Errorf("the picture could not be decoded")
			res.full, res.fit = nil, nil
		}
		imgvLoad.mu.Lock()
		if imgvLoad.gen == gen {
			imgvLoad.done = &res
		}
		imgvLoad.mu.Unlock()
	}()
	if info, err := os.Stat(path); err == nil {
		res.size = info.Size()
	}
	orient := imgvFileOrientation(path)
	pic, err := imgvDecodeFile(path)
	if err != nil {
		res.err = err
		return
	}
	if !imgvCurrent(gen) {
		return
	}
	b := pic.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	// origW/origH is the picture as it is seen: a phone's portrait photo is
	// stored as landscape pixels plus an EXIF turn.
	res.origW, res.origH = srcW, srcH
	fitLimitW, fitLimitH := fitW, fitH
	if orient >= 5 {
		res.origW, res.origH = srcH, srcW
		fitLimitW, fitLimitH = fitH, fitW
	}
	w, h := imgvFitInside(srcW, srcH, maxDim, maxDim)
	res.full = imgvScale(pic, w, h)
	if !imgvCurrent(gen) {
		return
	}
	// The screen-sized copy is only worth having when it is much smaller.
	if fw, fh := imgvFitInside(srcW, srcH, fitLimitW, fitLimitH); fw*3 < w*2 {
		res.fit = imgvScale(res.full, fw, fh)
	}
	if orient > 1 {
		// Turned upright after the shrink, which costs a fraction of turning
		// the camera's full picture.
		res.full = imgvOrient(res.full, orient)
		res.fit = imgvOrient(res.fit, orient)
	}
}

// imgvFileOrientation is a JPEG's EXIF orientation (1 when it has none, or for
// any other format). Only the head of the file is read: the tag sits in the
// APP1 segment right after the start marker. The lock screen reads it the same
// way (saver_backdrop.go), so a photo stands the same way in both places.
func imgvFileOrientation(path string) int {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".jpg" && ext != ".jpeg" {
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer f.Close()
	head := make([]byte, 192<<10)
	n, _ := io.ReadFull(f, head)
	return saverJPEGOrientation(head[:n])
}

// imgvOrient turns a decoded picture by its EXIF orientation, with the lock
// screen's mapping (saverOrientFor). A nil picture or orientation 1 comes back
// as it is.
func imgvOrient(pic *image.NRGBA, orient int) *image.NRGBA {
	if pic == nil || orient <= 1 || orient > 8 {
		return pic
	}
	sw, sh := pic.Rect.Dx(), pic.Rect.Dy()
	m, dw, dh := saverOrientFor(orient, sw, sh)
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for v := 0; v < dh; v++ {
		row := out.Pix[v*out.Stride:]
		for u := 0; u < dw; u++ {
			x := m.ax*u + m.bx*v + m.cx
			y := m.ay*u + m.by*v + m.cy
			if x < 0 || y < 0 || x >= sw || y >= sh {
				continue
			}
			s := pic.PixOffset(pic.Rect.Min.X+x, pic.Rect.Min.Y+y)
			copy(row[u*4:u*4+4], pic.Pix[s:s+4])
		}
	}
	return out
}

// imgvCurrent reports whether generation gen is still the picture on screen.
func imgvCurrent(gen int) bool {
	imgvLoad.mu.Lock()
	defer imgvLoad.mu.Unlock()
	return imgvLoad.gen == gen
}

// imgvDecodeFile decodes a picture with Go's decoders (JPEG, PNG, GIF), and
// anything else (BMP, WebP) through SDL_image.
func imgvDecodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if cfg, _, cfgErr := image.DecodeConfig(f); cfgErr == nil {
		if int64(cfg.Width)*int64(cfg.Height) > imgvMaxPixels {
			return nil, fmt.Errorf("it is %d × %d, too large to open here", cfg.Width, cfg.Height)
		}
		if _, err := f.Seek(0, 0); err != nil {
			return nil, err
		}
		pic, _, err := image.Decode(f)
		if err == nil {
			return pic, nil
		}
	}
	return imgvDecodeSDL(path)
}

// imgvDecodeSDL decodes through SDL_image and copies the pixels into Go
// memory, so the surface can be freed at once.
func imgvDecodeSDL(path string) (image.Image, error) {
	surface, err := img.Load(path)
	if err != nil {
		return nil, errors.New("this picture format cannot be opened")
	}
	defer surface.Free()
	if int64(surface.W)*int64(surface.H) > imgvMaxPixels {
		return nil, fmt.Errorf("it is %d × %d, too large to open here", surface.W, surface.H)
	}
	conv, err := surface.ConvertFormat(sdl.PIXELFORMAT_ABGR8888, 0)
	if err != nil {
		return nil, errors.New("this picture format cannot be opened")
	}
	defer conv.Free()
	w, h := int(conv.W), int(conv.H)
	pic := image.NewNRGBA(image.Rect(0, 0, w, h))
	src := conv.Pixels()
	pitch := int(conv.Pitch)
	for y := 0; y < h; y++ {
		copy(pic.Pix[y*pic.Stride:y*pic.Stride+w*4], src[y*pitch:y*pitch+w*4])
	}
	return pic, nil
}

// imgvFitInside is the largest w×h with the picture's aspect inside maxW×maxH,
// never larger than the picture itself.
func imgvFitInside(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return 1, 1
	}
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	if scale >= 1 {
		return w, h
	}
	fw := int(math.Round(float64(w) * scale))
	fh := int(math.Round(float64(h) * scale))
	if fw < 1 {
		fw = 1
	}
	if fh < 1 {
		fh = 1
	}
	return fw, fh
}

// imgvScale box-samples src into a w×h picture with straight alpha, reading at
// most a 2×2 grid per output pixel. The common decoder types are read straight
// from their planes: going through image.At for a 12-megapixel photo is
// seconds on the handheld.
func imgvScale(src image.Image, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || w <= 0 || h <= 0 {
		return dst
	}
	var sample func(x, y int) (uint8, uint8, uint8, uint8)
	switch s := src.(type) {
	case *image.YCbCr:
		sample = func(x, y int) (uint8, uint8, uint8, uint8) {
			yi := s.YOffset(x, y)
			ci := s.COffset(x, y)
			r, g, bl := color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
			return r, g, bl, 255
		}
	case *image.NRGBA:
		sample = func(x, y int) (uint8, uint8, uint8, uint8) {
			i := s.PixOffset(x, y)
			return s.Pix[i], s.Pix[i+1], s.Pix[i+2], s.Pix[i+3]
		}
	case *image.RGBA:
		sample = func(x, y int) (uint8, uint8, uint8, uint8) {
			i := s.PixOffset(x, y)
			a := s.Pix[i+3]
			if a == 0 || a == 255 {
				return s.Pix[i], s.Pix[i+1], s.Pix[i+2], a
			}
			// Premultiplied in image.RGBA; the texture wants straight alpha.
			return uint8(int(s.Pix[i]) * 255 / int(a)), uint8(int(s.Pix[i+1]) * 255 / int(a)), uint8(int(s.Pix[i+2]) * 255 / int(a)), a
		}
	case *image.Gray:
		sample = func(x, y int) (uint8, uint8, uint8, uint8) {
			v := s.Pix[s.PixOffset(x, y)]
			return v, v, v, 255
		}
	default:
		sample = func(x, y int) (uint8, uint8, uint8, uint8) {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			return c.R, c.G, c.B, c.A
		}
	}
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*sh/h
		y1 := b.Min.Y + (y+1)*sh/h
		ys := [2]int{y0, (y0 + y1) / 2}
		ny := 1
		if y1-y0 >= 2 {
			ny = 2
		}
		row := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*sw/w
			x1 := b.Min.X + (x+1)*sw/w
			xs := [2]int{x0, (x0 + x1) / 2}
			nx := 1
			if x1-x0 >= 2 {
				nx = 2
			}
			var r, g, bl, a, n int
			for j := 0; j < ny; j++ {
				for i := 0; i < nx; i++ {
					cr, cg, cb, ca := sample(xs[i], ys[j])
					r += int(cr)
					g += int(cg)
					bl += int(cb)
					a += int(ca)
					n++
				}
			}
			o := x * 4
			row[o] = uint8(r / n)
			row[o+1] = uint8(g / n)
			row[o+2] = uint8(bl / n)
			row[o+3] = uint8(a / n)
		}
	}
	return dst
}

// imgvTexture uploads a decoded picture with linear filtering: the default
// nearest-neighbour sampling made every zoom step look blocky. The hint is
// read when a texture is created, so it is set for this upload only.
func imgvTexture(renderer *sdl.Renderer, pic *image.NRGBA) *sdl.Texture {
	if renderer == nil || pic == nil || len(pic.Pix) == 0 {
		return nil
	}
	w, h := int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "1")
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, w, h)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pic.Pix[0]), pic.Stride); err != nil {
		tex.Destroy()
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// imgvTakeDecoded uploads a finished decode on the render thread.
func imgvTakeDecoded(renderer *sdl.Renderer) {
	imgvLoad.mu.Lock()
	res := imgvLoad.done
	if res != nil && res.gen == imgvLoad.gen {
		imgvLoad.done = nil
	} else {
		res = nil
	}
	imgvLoad.mu.Unlock()
	if res == nil {
		return
	}
	imgv.loading = false
	imgv.fileSize = res.size
	if res.err != nil {
		imgv.errText = res.err.Error()
		log.Printf("Image viewer: %s: %v", filepath.Base(imageViewerPath), res.err)
		return
	}
	imgvReleaseTextures()
	imageViewerTexture = imgvTexture(renderer, res.full)
	if imageViewerTexture == nil {
		imgv.errText = "the picture is too large for this screen's graphics memory"
		return
	}
	imageViewerW, imageViewerH = int32(res.full.Rect.Dx()), int32(res.full.Rect.Dy())
	if res.fit != nil {
		imgv.fitTex = imgvTexture(renderer, res.fit)
		imgv.fitW, imgv.fitH = int32(res.fit.Rect.Dx()), int32(res.fit.Rect.Dy())
	}
	imgv.origW, imgv.origH = int32(res.origW), int32(res.origH)
	imgv.mode = imgvFit
	imageViewerZoom = imgvModeScale(imgvFit)
	imageViewerPanX, imageViewerPanY = 0, 0
}

// --- Zoom and pan -------------------------------------------------------------

// imgvPictureSize is the picture's own size (what 100% means).
func imgvPictureSize() (float64, float64) {
	if imgv.origW > 0 && imgv.origH > 0 {
		return float64(imgv.origW), float64(imgv.origH)
	}
	return float64(imageViewerW), float64(imageViewerH)
}

// imgvViewport is the area the picture is fitted into.
func imgvViewport() (float64, float64) {
	return float64(screenWidth - 32), float64(screenHeight - 32)
}

// imgvModeScale is the screen pixels per picture pixel a mode stands for.
func imgvModeScale(mode imgvFitMode) float64 {
	pw, ph := imgvPictureSize()
	vw, vh := imgvViewport()
	if pw <= 0 || ph <= 0 {
		return 1
	}
	switch mode {
	case imgvFill:
		return math.Max(float64(screenWidth)/pw, float64(screenHeight)/ph)
	case imgvActual:
		return 1
	}
	return math.Min(vw/pw, vh/ph)
}

// imgvScaleLimits are the smallest and largest zoom: half of "fit" and eight
// times it (at least 400%).
func imgvScaleLimits() (float64, float64) {
	fit := imgvModeScale(imgvFit)
	return fit * 0.5, math.Max(fit*8, 4)
}

// imgvDisplaySize is the picture's size on screen at the current zoom.
func imgvDisplaySize() (float64, float64) {
	pw, ph := imgvPictureSize()
	return pw * imageViewerZoom, ph * imageViewerZoom
}

// imgvClampPan keeps the picture covering the screen when it is larger than
// it, and centred on an axis where it is smaller.
func imgvClampPan() {
	dw, dh := imgvDisplaySize()
	clamp := func(pan int32, display, view float64) int32 {
		if display <= view {
			return 0
		}
		limit := int32((display - view) / 2)
		if pan > limit {
			return limit
		}
		if pan < -limit {
			return -limit
		}
		return pan
	}
	imageViewerPanX = clamp(imageViewerPanX, dw, float64(screenWidth))
	imageViewerPanY = clamp(imageViewerPanY, dh, float64(screenHeight))
}

// imgvSetScale zooms to scale, keeping the picture point under (ax, ay) where
// it is on screen.
func imgvSetScale(scale float64, ax, ay int32) {
	lo, hi := imgvScaleLimits()
	if scale < lo {
		scale = lo
	}
	if scale > hi {
		scale = hi
	}
	old := imageViewerZoom
	if old <= 0 {
		old = scale
	}
	cx, cy := float64(screenWidth)/2, float64(screenHeight)/2
	// The anchor's offset from the picture centre scales with the zoom.
	offX := float64(ax) - (cx + float64(imageViewerPanX))
	offY := float64(ay) - (cy + float64(imageViewerPanY))
	ratio := scale / old
	imageViewerPanX = int32(float64(ax) - cx - offX*ratio)
	imageViewerPanY = int32(float64(ay) - cy - offY*ratio)
	imageViewerZoom = scale
	imgvClampPan()
}

// imgvZoomBy zooms in (steps > 0) or out around a screen point.
func imgvZoomBy(steps int, ax, ay int32) {
	if imageViewerPath == "" || steps == 0 {
		return
	}
	scale := imageViewerZoom * math.Pow(imgvZoomStep, float64(steps))
	imgv.mode = imgvCustom
	// Landing within a step of "fit" snaps back to it, so zooming out always
	// ends at the whole picture rather than a few percent off it.
	if fit := imgvModeScale(imgvFit); math.Abs(scale-fit)/fit < 0.08 {
		scale = fit
		imgv.mode = imgvFit
	}
	imgvSetScale(scale, ax, ay)
	PlayNavSound()
}

// imgvSetMode applies Fit, Fill or 1:1 and centres the picture.
func imgvSetMode(mode imgvFitMode) {
	imgv.mode = mode
	imageViewerZoom = imgvModeScale(mode)
	imageViewerPanX, imageViewerPanY = 0, 0
	imgvClampPan()
}

// imgvCycleMode steps Fit → Fill → 1:1 → Fit.
func imgvCycleMode() {
	switch imgv.mode {
	case imgvFit:
		imgvSetMode(imgvFill)
	case imgvFill:
		imgvSetMode(imgvActual)
	default:
		imgvSetMode(imgvFit)
	}
	PlayNavSound()
	showToast(imgvModeLabel(), ToastInfo())
}

func imgvModeLabel() string {
	switch imgv.mode {
	case imgvFill:
		return "Fill"
	case imgvActual:
		return "1:1"
	case imgvCustom:
		return fmt.Sprintf("%.0f%%", imageViewerZoom*100)
	}
	return "Fit"
}

// imgvZoomed reports whether the picture is larger than the screen, which is
// when the D-pad pans instead of browsing.
func imgvZoomed() bool {
	dw, dh := imgvDisplaySize()
	return dw > float64(screenWidth)+1 || dh > float64(screenHeight)+1
}

// imgvPanBy moves the picture by a fraction of the screen.
func imgvPanBy(dx, dy int32) {
	imageViewerPanX += dx
	imageViewerPanY += dy
	imgvClampPan()
}

// imgvDirection answers a D-pad direction: pan when zoomed, browse otherwise.
func imgvDirection(dx, dy int) {
	if imgvZoomed() {
		stepX := screenWidth / 6
		stepY := screenHeight / 6
		// The picture moves the way the view does: Right looks further right.
		imgvPanBy(int32(-dx)*stepX, int32(-dy)*stepY)
		return
	}
	if dx != 0 {
		imageViewerStep(dx)
	}
}

// --- Input --------------------------------------------------------------------

// imgvController answers the pad while the viewer is open.
func imgvController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return false
	}
	cx, cy := screenWidth/2, screenHeight/2
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeImageViewer()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_A:
		imgv.hideUI = !imgv.hideUI
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_X:
		// Keep the picture on the lock screen, or take it back if it already
		// is the backdrop. A remote (chat) image has no file in Files, so the
		// button stays inert for it.
		if !imageViewerRemote {
			PlayActivateSound()
			toggleScreenlockFromViewer(config)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		imgvCycleMode()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		imgvZoomBy(-1, cx, cy)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		imgvZoomBy(1, cx, cy)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		imgvDirection(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		imgvDirection(1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if imgvZoomed() {
			imgvDirection(0, -1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if imgvZoomed() {
			imgvDirection(0, 1)
		}
	}
	return true
}

// imgvShoulder answers the triggers: previous and next picture, whatever the
// zoom, so browsing never needs zooming back out first.
func imgvShoulder(s padShoulder) {
	switch s {
	case padL2:
		imageViewerStep(-1)
	case padR2:
		imageViewerStep(1)
	case padL1:
		imgvZoomBy(-1, screenWidth/2, screenHeight/2)
	case padR1:
		imgvZoomBy(1, screenWidth/2, screenHeight/2)
	}
}

// handleImageViewerInput answers the keyboard while the viewer is open.
func handleImageViewerInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	cx, cy := screenWidth/2, screenHeight/2
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		closeImageViewer()
		PlayBackSound()
	case sdl.K_RETURN, sdl.K_h, sdl.K_SPACE:
		imgv.hideUI = !imgv.hideUI
	case sdl.K_l, sdl.K_x:
		if !imageViewerRemote {
			toggleScreenlockFromViewer(config)
		}
	case sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS:
		imgvZoomBy(1, cx, cy)
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		imgvZoomBy(-1, cx, cy)
	case sdl.K_0, sdl.K_KP_0:
		imgvSetMode(imgvFit)
	case sdl.K_1, sdl.K_KP_1:
		imgvSetMode(imgvActual)
	case sdl.K_f, sdl.K_y:
		imgvCycleMode()
	case sdl.K_PAGEUP:
		imageViewerStep(-1)
	case sdl.K_PAGEDOWN:
		imageViewerStep(1)
	case sdl.K_LEFT:
		imgvDirection(-1, 0)
	case sdl.K_RIGHT:
		imgvDirection(1, 0)
	case sdl.K_UP:
		if imgvZoomed() {
			imgvDirection(0, -1)
		} else {
			imgvZoomBy(1, cx, cy)
		}
	case sdl.K_DOWN:
		if imgvZoomed() {
			imgvDirection(0, 1)
		} else {
			imgvZoomBy(-1, cx, cy)
		}
	}
}

// imgvWheel zooms around the pointer.
func imgvWheel(notches int32) bool {
	if imageViewerPath == "" {
		return false
	}
	if notches != 0 {
		imgvZoomBy(int(notches), mouseX, mouseY)
	}
	return true
}

// imgvMouseDown answers a press: a chip runs its action, anything else starts
// a drag (or, released without moving, toggles the controls).
func imgvMouseDown(mx, my int32, config *Config) {
	if !imgv.hideUI {
		switch filesChipAt(imgv.chips, mx, my) {
		case "close":
			closeImageViewer()
			PlayBackSound()
			return
		case "lock":
			toggleScreenlockFromViewer(config)
			return
		case "zoomout":
			imgvZoomBy(-1, screenWidth/2, screenHeight/2)
			return
		case "zoomin":
			imgvZoomBy(1, screenWidth/2, screenHeight/2)
			return
		case "mode":
			imgvCycleMode()
			return
		case "prev":
			imageViewerStep(-1)
			return
		case "next":
			imageViewerStep(1)
			return
		}
	}
	imgv.dragOn, imgv.dragMoved = true, false
	imgv.dragX, imgv.dragY = mx, my
}

// imgvMouseMotion pans while the left button is held.
func imgvMouseMotion(mx, my int32, leftHeld bool) {
	if !imgv.dragOn {
		return
	}
	if !leftHeld {
		imgv.dragOn = false
		return
	}
	dx, dy := mx-imgv.dragX, my-imgv.dragY
	if !imgv.dragMoved && dx*dx+dy*dy < 36 {
		return
	}
	imgv.dragMoved = true
	imgv.dragX, imgv.dragY = mx, my
	imgvPanBy(dx, dy)
}

// imgvMouseUp ends a drag; a press that never moved toggles the controls.
func imgvMouseUp() {
	if !imgv.dragOn {
		return
	}
	if !imgv.dragMoved {
		imgv.hideUI = !imgv.hideUI
	}
	imgv.dragOn, imgv.dragMoved = false, false
}

// --- Render -------------------------------------------------------------------

func renderImageViewer(renderer *sdl.Renderer, config *Config, path string) {
	if renderer == nil {
		return
	}
	texture := imageViewerTexture
	texW, texH := imageViewerW, imageViewerH
	if imageViewerRemote {
		// The picture is cached by the chat renderer; while it downloads the
		// viewer shows its loading state rather than closing.
		texture = getChatImageTexture(renderer, path)
		if texture != nil {
			if _, _, qw, qh, err := texture.Query(); err == nil && qw > 0 && qh > 0 {
				texW, texH = qw, qh
				if imgv.origW == 0 {
					imgv.origW, imgv.origH = qw, qh
					imgv.mode = imgvFit
					imageViewerZoom = imgvModeScale(imgvFit)
				}
			} else {
				texture = nil
			}
		}
	} else if texture == nil {
		imgvTakeDecoded(renderer)
		texture = imageViewerTexture
		texW, texH = imageViewerW, imageViewerH
	}

	// A dark page: a photo is judged against black, not against the glass.
	bg := glassMix(ColorBackground, sdl.Color{A: 255}, 0.7)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, bg)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	font, _ := getCachedFont(config, "small")
	switch {
	case texture != nil && texW > 0 && texH > 0:
		imgvDrawPicture(renderer, texture)
	case imgv.errText != "":
		UIErrorState(renderer, config, sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight},
			"@image", "Cannot open this picture", imgv.errText)
	default:
		UIEmptyState(renderer, config, sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight},
			"@image", "Opening the picture…", filepath.Base(path))
	}
	if imgv.hideUI || font == nil {
		imageViewerCloseBtn, imageViewerLockBtn = sdl.Rect{}, sdl.Rect{}
		imgv.chips = imgv.chips[:0]
		return
	}
	imgvDrawChrome(renderer, config)
}

// imgvDrawPicture copies the picture at the current zoom and pan, from the
// screen-sized copy when the picture is drawn that small.
func imgvDrawPicture(renderer *sdl.Renderer, texture *sdl.Texture) {
	if imageViewerZoom <= 0 {
		imageViewerZoom = imgvModeScale(imgvFit)
	}
	dw, dh := imgvDisplaySize()
	dst := sdl.Rect{
		X: int32(math.Round((float64(screenWidth)-dw)/2)) + imageViewerPanX,
		Y: int32(math.Round((float64(screenHeight)-dh)/2)) + imageViewerPanY,
		W: int32(math.Round(dw)),
		H: int32(math.Round(dh)),
	}
	if dst.W <= 0 || dst.H <= 0 {
		return
	}
	src := texture
	if imgv.fitTex != nil && float64(dst.W) <= float64(imgv.fitW)*1.1 {
		src = imgv.fitTex
	}
	_ = renderer.Copy(src, nil, &dst)
}

// imgvDrawChrome draws the header (name, position, size, zoom, the lock-screen
// mark), the toolbar and the footer hints over the picture.
func imgvDrawChrome(renderer *sdl.Renderer, config *Config) {
	margin := scaleX(homeSideMargin, screenWidth)
	const bandY, bandH = int32(8), int32(96)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	shade := ColorBackground
	shade.A = 190
	fillRoundedRect(renderer, margin/2, bandY, screenWidth-margin, bandH, 16, shade)
	headerY := bandY + 10

	isLock := !imageViewerRemote && screenlockImageIsCurrent(config, imageViewerPath)
	chips := imgv.chips[:0]
	if len(imageViewerFiles) > 1 {
		chips = append(chips, filesChip{ID: "prev", Label: "‹"}, filesChip{ID: "next", Label: "›"})
	}
	chips = append(chips,
		filesChip{ID: "zoomout", Label: "−"},
		filesChip{ID: "zoomin", Label: "+"},
		filesChip{ID: "mode", Label: imgvModeLabel()},
	)
	if !imageViewerRemote {
		// The chip says which state it is in: Inter has no check mark, so the
		// words carry it, and the accent wash marks the current lock picture.
		label := "Set as lock screen"
		if isLock {
			label = "On the lock screen"
		}
		chips = append(chips, filesChip{ID: "lock", Label: label, On: isLock})
	}
	chips = append(chips, filesChip{ID: "close", Label: "Close"})
	chips, toolbarX := filesDrawToolbar(renderer, config, chips, screenWidth-margin, headerY+4, 36)
	imgv.chips = chips
	imageViewerCloseBtn, imageViewerLockBtn = sdl.Rect{}, sdl.Rect{}
	for _, chip := range chips {
		switch chip.ID {
		case "close":
			imageViewerCloseBtn = chip.Rect
		case "lock":
			imageViewerLockBtn = chip.Rect
		}
	}

	title := filepath.Base(imageViewerPath)
	if imageViewerRemote {
		// A URL's basename is often meaningless, so name the source instead.
		title = "Chat image"
	}
	filesDrawHeader(renderer, config, "image", title, imgvSubtitle(isLock), margin, headerY, toolbarX-margin-16, sdl.Color{})
	filesDrawFooter(renderer, config, imgvFooterHints())
}

// imgvSubtitle is the header's status line.
func imgvSubtitle(isLock bool) string {
	var parts []string
	if len(imageViewerFiles) > 1 && imageViewerIndex >= 0 {
		parts = append(parts, fmt.Sprintf("%d of %d", imageViewerIndex+1, len(imageViewerFiles)))
	}
	if imgv.origW > 0 {
		parts = append(parts, fmt.Sprintf("%d × %d", imgv.origW, imgv.origH))
	}
	if imgv.fileSize > 0 {
		parts = append(parts, humanBytes(imgv.fileSize))
	}
	if imgv.origW > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%%", imageViewerZoom*100))
	}
	if isLock {
		parts = append(parts, "lock screen picture")
	}
	if imgv.loading && imgv.origW == 0 {
		parts = append(parts, "loading…")
	}
	return strings.Join(parts, " · ")
}

// imgvFooterHints are the viewer's keys.
func imgvFooterHints() []FooterHint {
	direction := "Browse"
	if imgvZoomed() {
		direction = "Pan"
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: direction},
		{Button: "L1/R1", Label: "Zoom"},
		{Button: "Y", Label: "Fit / Fill / 1:1"},
	}
	if !imageViewerRemote {
		hints = append(hints, FooterHint{Button: "X", Label: "Lock screen"})
	}
	if len(imageViewerFiles) > 1 {
		hints = append(hints, FooterHint{Button: "L2/R2", Label: "Prev / Next"})
	}
	return append(hints,
		FooterHint{Button: "A", Label: "Hide"},
		FooterHint{Button: "B", Label: "Close"},
	)
}
