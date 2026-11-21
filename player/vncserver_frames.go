package main

// vncserver_frames.go — where the VNC server's pictures come from.
//
// The same two sources Screen Share reads (castcap.go), without its JPEG
// step. While JukaHub is on screen the render loop copies its finished frame
// (vncCaptureFrame, from captureCompanionScreen, through castReadFrameInto).
// While a game owns the screen the render loop is parked, so a goroutine
// reads the panel's framebuffer the way Screen Share's own reader does
// (castProbeFB, castFBPan) — raw pixels, never a JPEG decoded back. Either
// way the picture becomes an RGBX frame the size of JukaHub's canvas, so a
// viewer's framebuffer keeps one size until the canvas itself changes (TV
// mode), which DesktopSize tells the viewers that understand it.
//
// Frames are captured only while a viewer waits for one: at most vncMaxFPS a
// second in JukaHub (and never more than Screen Share's readback budget,
// castUIGap) and vncGameFPS in a game, where every read competes with the
// game for the CPU. A frame is shared by every viewer: each holds the frame
// it last sent (to compare tiles against) and the one it is sending, and a
// buffer is written again only once nobody holds it.

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vncMaxFPS caps JukaHub's own screens; vncGameFPS a game's framebuffer.
	vncMaxFPS  = 15
	vncGameFPS = 10
	// vncPoolMax bounds the frame buffers: the current frame, two per viewer
	// and the one being filled.
	vncPoolMax = 2 + 2*vncMaxClients
	// vncFBRetry is how long a framebuffer that could not be read waits
	// before the next attempt.
	vncFBRetry = 15 * time.Second
	// vncFBIdle paces the reader while nobody needs it.
	vncFBIdle = 150 * time.Millisecond
)

// vncFrame is one picture: RGBX, w*4 bytes a row.
type vncFrame struct {
	pix  []byte
	w, h int
	seq  uint64
	refs int // guarded by vncHub.mu
}

// vncHub holds the current frame and the buffers. waiting counts viewers
// blocked for a newer frame: the capture runs only while it is above zero.
var vncHub struct {
	mu      sync.Mutex
	cur     *vncFrame
	pool    []*vncFrame
	seq     uint64
	notify  chan struct{}
	waiting atomic.Int32
}

// vncLive is what the capture paths read. The main thread publishes it: the
// config and the canvas belong to it.
var vncLive struct {
	on      atomic.Bool  // the server runs
	canvasW atomic.Int32 // JukaHub's canvas: every frame's size
	canvasH atomic.Int32
	rotate  atomic.Int32 // castRotate for a game's framebuffer, -1 automatic
}

// vncHubWanted reports whether a viewer waits for a frame.
func vncHubWanted() bool { return vncLive.on.Load() && vncHub.waiting.Load() > 0 }

// vncHubTake returns a buffer for a w×h frame, held by the caller until it
// is published or dropped; nil when every buffer is in use.
func vncHubTake(w, h int) *vncFrame {
	if w <= 0 || h <= 0 {
		return nil
	}
	need := w * h * 4
	vncHub.mu.Lock()
	defer vncHub.mu.Unlock()
	for _, f := range vncHub.pool {
		if f == vncHub.cur || f.refs != 0 {
			continue
		}
		if cap(f.pix) < need {
			f.pix = make([]byte, need)
		}
		f.pix, f.w, f.h, f.refs = f.pix[:need], w, h, 1
		return f
	}
	if len(vncHub.pool) >= vncPoolMax {
		return nil
	}
	f := &vncFrame{pix: make([]byte, need), w: w, h: h, refs: 1}
	vncHub.pool = append(vncHub.pool, f)
	return f
}

// vncHubPublish makes f (from vncHubTake, filled) the current frame and
// wakes the viewers waiting for one.
func vncHubPublish(f *vncFrame) {
	vncHub.mu.Lock()
	vncHub.seq++
	f.seq = vncHub.seq
	f.refs--
	vncHub.cur = f
	if vncHub.notify != nil {
		close(vncHub.notify)
		vncHub.notify = nil
	}
	vncHub.mu.Unlock()
}

// vncHubAcquire returns the current frame once it is newer than after,
// waiting up to wait (or until done closes) for one; nil when none came. The
// caller releases it.
func vncHubAcquire(after uint64, wait time.Duration, done <-chan struct{}) *vncFrame {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		vncHub.mu.Lock()
		if f := vncHub.cur; f != nil && f.seq > after {
			f.refs++
			vncHub.mu.Unlock()
			return f
		}
		if vncHub.notify == nil {
			vncHub.notify = make(chan struct{})
		}
		ch := vncHub.notify
		vncHub.mu.Unlock()
		vncHub.waiting.Add(1)
		select {
		case <-ch:
			vncHub.waiting.Add(-1)
		case <-timer.C:
			vncHub.waiting.Add(-1)
			return nil
		case <-done:
			vncHub.waiting.Add(-1)
			return nil
		}
	}
}

// vncHubRelease gives back a frame from vncHubAcquire (nil is fine).
func vncHubRelease(f *vncFrame) {
	if f == nil {
		return
	}
	vncHub.mu.Lock()
	if f.refs > 0 {
		f.refs--
	}
	vncHub.mu.Unlock()
}

// vncHubReset forgets every frame once the last viewer has gone: the memory
// goes back, and the next viewer starts from a fresh picture instead of one
// minutes old. The sequence keeps counting.
func vncHubReset() {
	vncHub.mu.Lock()
	vncHub.cur = nil
	vncHub.pool = nil
	vncHub.mu.Unlock()
}

// ── JukaHub's screens: the render loop ───────────────────────────────────

// vncUI is the render thread's capture state.
var vncUI struct {
	buf      []byte
	last     time.Time
	readCost time.Duration
}

// vncCaptureFrame copies the finished frame for the VNC viewers. Render
// thread only, at the end of a frame (captureCompanionScreen), before the
// virtual cursor and the LIVE pill are drawn. Nothing is hidden from the
// picture the way Screen Share hides the pairing code: a VNC viewer signed
// in with the password and controls the handheld anyway.
func vncCaptureFrame(renderer *sdl.Renderer) {
	if renderer == nil || !vncLive.on.Load() {
		return
	}
	cw, ch := screenWidth, screenHeight
	vncLive.canvasW.Store(cw)
	vncLive.canvasH.Store(ch)
	if vncHub.waiting.Load() == 0 || gameScreenSuspended() {
		return
	}
	now := time.Now()
	if now.Sub(vncUI.last) < castUIGap(vncMaxFPS, P().IsHandheld(), vncUI.readCost) {
		return
	}
	readAt := time.Now()
	w, h, stride, _, _, ok := castReadFrameInto(renderer, &vncUI.buf)
	if !ok {
		return
	}
	if took := time.Since(readAt); vncUI.readCost <= 0 {
		vncUI.readCost = took
	} else {
		vncUI.readCost = (vncUI.readCost*3 + took) / 4
	}
	vncUI.last = now
	f := vncHubTake(int(cw), int(ch))
	if f == nil {
		return
	}
	vncBlit(f.pix, f.w, f.h, vncUI.buf, stride, w, h, castLayoutRGBA, false, 0)
	vncHubPublish(f)
}

// ── A game: the framebuffer ───────────────────────────────────────────────

// vncFBLoop reads the panel's framebuffer while a game owns the screen and a
// viewer waits. It runs as long as the server (stop ends it). The device is
// opened per game and closed when JukaHub is back, so a new game's mode is
// probed again; a framebuffer that cannot be read is retried after
// vncFBRetry, and viewers keep the last picture meanwhile.
func vncFBLoop(stop <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("vnc", "the game picture reader stopped after a fault: %v", r)
		}
	}()
	var f *os.File
	var geom castFBGeom
	var raw []byte
	var retryAt time.Time
	closeFB := func() {
		if f != nil {
			_ = f.Close()
			f = nil
		}
	}
	defer closeFB()
	timer := time.NewTimer(vncFBIdle)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		inGame := gameScreenSuspended()
		if !inGame {
			closeFB()
			retryAt = time.Time{}
		}
		if !inGame || !castGamesSupported() || !vncHubWanted() {
			timer.Reset(vncFBIdle)
			continue
		}
		now := time.Now()
		cw, ch := int(vncLive.canvasW.Load()), int(vncLive.canvasH.Load())
		if f == nil {
			if now.Before(retryAt) {
				timer.Reset(vncFBIdle)
				continue
			}
			fh, err := os.Open(castFBDevice)
			if err == nil {
				if geom, err = castProbeFB(fh, cw, ch); err != nil {
					_ = fh.Close()
				}
			}
			if err != nil {
				logWarn("vnc", "cannot read the game's picture: %v", err)
				retryAt = now.Add(vncFBRetry)
				timer.Reset(vncFBIdle)
				continue
			}
			f = fh
			raw = make([]byte, geom.Stride*(geom.H-1)+geom.W*geom.BPP/8)
			logInfo("vnc", "showing the game from %s: %dx%d at %d bits", castFBDevice, geom.W, geom.H, geom.BPP)
		}
		start := time.Now()
		if err := vncReadFB(f, geom, raw); err != nil {
			logWarn("vnc", "the game's picture stopped: %v", err)
			closeFB()
			retryAt = now.Add(vncFBRetry)
			timer.Reset(vncFBIdle)
			continue
		}
		deg := int(vncLive.rotate.Load())
		if deg < 0 {
			deg = castAutoRotate(geom.W, geom.H, cw, ch)
		}
		if fr := vncHubTake(cw, ch); fr != nil {
			vncBlit(fr.pix, fr.w, fr.h, raw, geom.Stride, geom.W, geom.H, geom.Layout, geom.BPP == 16, deg)
			vncHubPublish(fr)
		}
		timer.Reset(castNativeWait(time.Second/vncGameFPS, time.Since(start)))
	}
}

// vncReadFB reads the visible picture of the framebuffer into raw, following
// the double-buffer pan.
func vncReadFB(f *os.File, g castFBGeom, raw []byte) error {
	x, y := castFBPan(f, g)
	n, err := f.ReadAt(raw, int64(y)*int64(g.Stride)+int64(x*g.BPP/8))
	if n < len(raw) {
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			return errors.New("the framebuffer read returned nothing")
		}
		clear(raw[n:])
	}
	return nil
}

// vncBlit copies a w×h picture (stride bytes a row, laid out as lay, or
// RGB565 when rgb565) into dst, a dw×dh RGBX frame: turned clockwise by deg,
// and scaled by nearest neighbour when the sizes differ (a desktop window
// larger than the canvas, a framebuffer of another size).
func vncBlit(dst []byte, dw, dh int, src []byte, stride, w, h int, lay castPixLayout, rgb565 bool, deg int) {
	if dw <= 0 || dh <= 0 || w <= 0 || h <= 0 || len(dst) < dw*dh*4 {
		return
	}
	bpp := lay.bpp
	if rgb565 {
		bpp = 2
	}
	if bpp <= 0 || len(src) < stride*(h-1)+w*bpp {
		return
	}
	if deg == 0 && w == dw && h == dh && bpp == 4 {
		for y := 0; y < h; y++ {
			row, out := src[y*stride:y*stride+w*4], dst[y*dw*4:(y+1)*dw*4]
			if lay == castLayoutRGBA {
				copy(out, row)
				continue
			}
			for o := 0; o+3 < len(row); o += 4 {
				out[o], out[o+1], out[o+2], out[o+3] = row[o+lay.r], row[o+lay.g], row[o+lay.b], 255
			}
		}
		return
	}
	rw, rh := w, h
	if deg == 90 || deg == 270 {
		rw, rh = h, w
	}
	for dy := 0; dy < dh; dy++ {
		ry := dy * rh / dh
		out := dst[dy*dw*4 : (dy+1)*dw*4]
		for dx := 0; dx < dw; dx++ {
			rx := dx * rw / dw
			sx, sy := rx, ry
			switch deg {
			case 90:
				sx, sy = ry, h-1-rx
			case 180:
				sx, sy = w-1-rx, h-1-ry
			case 270:
				sx, sy = w-1-ry, rx
			}
			o, p := sy*stride+sx*bpp, out[dx*4:dx*4+4]
			if rgb565 {
				v := uint16(src[o]) | uint16(src[o+1])<<8
				r, g, b := uint8(v>>11&0x1f), uint8(v>>5&0x3f), uint8(v&0x1f)
				p[0], p[1], p[2] = r<<3|r>>2, g<<2|g>>4, b<<3|b>>2
			} else {
				p[0], p[1], p[2] = src[o+lay.r], src[o+lay.g], src[o+lay.b]
			}
			p[3] = 255
		}
	}
}
