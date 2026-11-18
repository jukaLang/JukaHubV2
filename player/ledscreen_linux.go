//go:build linux

package main

// ledscreen_linux.go — the Screen colours mode's framebuffer reader
// (ledscreen.go). It shares Screen Share's geometry probe (castProbeFB in
// castcap_fb.go) but reads only two narrow strips: nine rows of the outer 15%
// on each side, pread into a buffer allocated once, at the visible half of a
// double-buffered panel (the pan is asked through a preallocated ioctl buffer).
// The device stays open between reads; the geometry is probed again every few
// seconds, because a game may switch the panel's mode.

import (
	"encoding/binary"
	"os"
	"time"
)

const (
	// ledScreenRows is how many rows each side is sampled at.
	ledScreenRows = 9
	// ledScreenStripPct is each side strip's share of the width.
	ledScreenStripPct = 15
	// ledScreenPixels is about how many pixels of a strip row are summed.
	ledScreenPixels = 12
	// ledScreenReprobe is how often the geometry is read again.
	ledScreenReprobe = 5 * time.Second
	// ledScreenRetry is the pause after the framebuffer would not open or read.
	ledScreenRetry = 10 * time.Second
)

// ledScreenFB is the reader's state. LED loop only (under ledScreen.mu).
var ledScreenFB struct {
	f        *os.File
	geom     castFBGeom
	probedAt time.Time
	retryAt  time.Time
	vinfo    []byte
	strip    []byte
}

// ledScreenFBClose drops the device after a failure; the next read after
// ledScreenRetry opens it again.
func ledScreenFBClose(now time.Time) {
	st := &ledScreenFB
	if st.f != nil {
		_ = st.f.Close()
		st.f = nil
	}
	st.retryAt = now.Add(ledScreenRetry)
}

// ledScreenFBRelease closes the device without a retry pause: the mode is not
// in use, nothing failed. Under ledScreen.mu.
func ledScreenFBRelease() {
	st := &ledScreenFB
	if st.f != nil {
		_ = st.f.Close()
		st.f, st.retryAt = nil, time.Time{}
	}
}

// ledScreenFBOpen opens and probes the framebuffer when needed. Reports whether
// a usable one is open.
func ledScreenFBOpen(now time.Time) bool {
	st := &ledScreenFB
	if st.f == nil {
		if now.Before(st.retryAt) {
			return false
		}
		f, err := os.Open(castFBDevice)
		if err != nil {
			st.retryAt = now.Add(ledScreenRetry)
			return false
		}
		st.f, st.probedAt = f, time.Time{}
	}
	if st.probedAt.IsZero() || now.Sub(st.probedAt) >= ledScreenReprobe || now.Before(st.probedAt) {
		st.probedAt = now
		g, err := castProbeFB(st.f, int(castLive.canvasW.Load()), int(castLive.canvasH.Load()))
		if err != nil || !g.valid() {
			ledScreenFBClose(now)
			return false
		}
		st.geom = g
		need := ledScreenStripW(g) * g.BPP / 8
		if cap(st.strip) < need {
			st.strip = make([]byte, need)
		}
		st.strip = st.strip[:need]
		if st.vinfo == nil {
			st.vinfo = make([]byte, castFBVarInfoSize)
		}
	}
	return true
}

// ledScreenStripW is one side strip's width in pixels. Pure.
func ledScreenStripW(g castFBGeom) int {
	w := g.W * ledScreenStripPct / 100
	if w < 8 {
		w = 8
	}
	if w > g.W {
		w = g.W
	}
	return w
}

// ledScreenPan is where the visible picture starts, kept inside the buffer
// (castFBPan without its per-call allocation).
func ledScreenPan() (int, int) {
	st := &ledScreenFB
	g := st.geom
	x, y := g.XOff, g.YOff
	if err := castIoctl(st.f, castFBIOGetVScreenInfo, st.vinfo); err == nil {
		x = int(binary.LittleEndian.Uint32(st.vinfo[16:]))
		y = int(binary.LittleEndian.Uint32(st.vinfo[20:]))
	}
	if x < 0 || (g.VW > 0 && x+g.W > g.VW) {
		x = 0
	}
	if y < 0 || (g.VH > 0 && y+g.H > g.VH) {
		y = 0
	}
	return x, y
}

// ledScreenSampleFB averages the left and right edge strips of the picture on
// screen into left and right (0..255 a channel). Reports false where there is
// no picture to read: off the TrimUI (a desktop's fb0 is not what the user
// sees), or a framebuffer that would not open or read. LED loop.
func ledScreenSampleFB(now time.Time, left, right *[3]float64) bool {
	if !IsTrimuiDevice() || !ledScreenFBOpen(now) {
		return false
	}
	st := &ledScreenFB
	g := st.geom
	bpp := g.BPP / 8
	sw := ledScreenStripW(g)
	every := sw / ledScreenPixels
	if every < 1 {
		every = 1
	}
	px, py := ledScreenPan()
	var sums [2][3]float64
	var counts [2]float64
	for i := 0; i < ledScreenRows; i++ {
		// Rows spread from 8% to 92% of the height: the very edge is often a
		// border or a status line.
		y := py + g.H*(8+i*84/(ledScreenRows-1))/100
		if y >= py+g.H {
			y = py + g.H - 1
		}
		for side := 0; side < 2; side++ {
			x := px
			if side == 1 {
				x = px + g.W - sw
			}
			off := int64(y)*int64(g.Stride) + int64(x*bpp)
			n, err := st.f.ReadAt(st.strip, off)
			if n < len(st.strip) {
				if err != nil || n == 0 {
					ledScreenFBClose(now)
					return false
				}
			}
			for p := 0; p+bpp <= n; p += every * bpp {
				r, gr, b := ledScreenPixel(st.strip[p:p+bpp], g)
				sums[side][0] += r
				sums[side][1] += gr
				sums[side][2] += b
				counts[side]++
			}
		}
	}
	if counts[0] == 0 || counts[1] == 0 {
		return false
	}
	for c := 0; c < 3; c++ {
		left[c] = sums[0][c] / counts[0]
		right[c] = sums[1][c] / counts[1]
	}
	return true
}

// ledScreenPixel decodes one framebuffer pixel: RGB565, or a 32-bit layout
// the probe described. Pure.
func ledScreenPixel(px []byte, g castFBGeom) (float64, float64, float64) {
	if g.BPP == 16 {
		v := uint16(px[0]) | uint16(px[1])<<8
		r := uint8(v >> 11 & 0x1f)
		gr := uint8(v >> 5 & 0x3f)
		b := uint8(v & 0x1f)
		return float64(r<<3 | r>>2), float64(gr<<2 | gr>>4), float64(b<<3 | b>>2)
	}
	lay := g.Layout
	return float64(px[lay.r]), float64(px[lay.g]), float64(px[lay.b])
}
