package main

// vncserver_proto.go — the RFB protocol (RFC 6143) as JukaHub's VNC server
// speaks it: the version and VNC Authentication handshake, the client
// messages read with every length bounded, pixel formats, the tile compare
// and the Raw and ZRLE encodings. Everything here is bytes over a reader or
// a writer, so the parser and the encoders are tested without a socket
// (vncserver_test.go); the server around it is vncserver.go.
//
// Frames reach this file as RGBX: four bytes a pixel, red first, tightly
// packed (vncserver_frames.go makes every source look like that). The
// server's own pixel format is that layout read as a little-endian 32-bit
// value, so a viewer that keeps it gets the rows as they are stored.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/des"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// vncTile is the side of the squares frames are compared and sent in.
	vncTile = 64
	// vncMaxEncodings bounds SetEncodings: real viewers list a dozen or two.
	vncMaxEncodings = 512
	// vncMaxCutText is the most clipboard text taken from a viewer: longer
	// text is read past and dropped. A viewer announcing more than
	// vncMaxCutTextSkip is disconnected instead of read past.
	vncMaxCutText     = 64 << 10
	vncMaxCutTextSkip = 1 << 20
)

// RFB message types, security types and encodings.
const (
	vncMsgSetPixelFormat = 0
	vncMsgSetEncodings   = 2
	vncMsgUpdateRequest  = 3
	vncMsgKeyEvent       = 4
	vncMsgPointerEvent   = 5
	vncMsgCutText        = 6

	vncSecVNCAuth = 2

	vncEncRaw         = 0
	vncEncZRLE        = 16
	vncEncDesktopSize = -223
)

// vncServerVersion is the ProtocolVersion the server offers.
const vncServerVersion = "RFB 003.008\n"

// ── Handshake ─────────────────────────────────────────────────────────────

// vncParseClientVersion reads a viewer's ProtocolVersion ("RFB 003.008\n")
// and returns the minor version the session follows: 8 for 3.8 and later
// (Apple's 3.889 included), 7 for 3.7, and 3 for 3.3 and the 3.4–3.6 some
// viewers send, which behave as 3.3.
func vncParseClientVersion(b []byte) (int, error) {
	if len(b) != 12 || string(b[:4]) != "RFB " || b[7] != '.' || b[11] != '\n' {
		return 0, errors.New("not a VNC viewer")
	}
	major, errMajor := strconv.Atoi(string(b[4:7]))
	minor, errMinor := strconv.Atoi(string(b[8:11]))
	if errMajor != nil || errMinor != nil || major != 3 || minor < 3 {
		return 0, fmt.Errorf("unsupported RFB version %q", strings.TrimSpace(string(b)))
	}
	switch {
	case minor >= 8:
		return 8, nil
	case minor == 7:
		return 7, nil
	}
	return 3, nil
}

// vncAuthKey is the DES key VNC Authentication derives from a password: its
// first eight bytes, zero-padded, each with its bits in reverse order — the
// original VNC's quirk, which every viewer copies.
func vncAuthKey(password string) [8]byte {
	var key [8]byte
	for i := 0; i < len(key) && i < len(password); i++ {
		key[i] = bits.Reverse8(password[i])
	}
	return key
}

// vncAuthResponse is what a viewer that knows password answers to a 16-byte
// challenge: the challenge encrypted as two DES-ECB blocks. DES is what the
// protocol prescribes (RFC 6143 7.2.2), not a choice: it is why the server
// takes LAN addresses only and never stands in for SSH.
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	if len(challenge) != 16 {
		return nil, errors.New("a VNC challenge is 16 bytes")
	}
	key := vncAuthKey(password)
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:])
	return out, nil
}

// vncAuthCheck compares a viewer's response with the right one in constant
// time. An empty password never matches.
func vncAuthCheck(password string, challenge, response []byte) bool {
	if password == "" || len(response) != 16 {
		return false
	}
	want, err := vncAuthResponse(password, challenge)
	return err == nil && subtle.ConstantTimeCompare(want, response) == 1
}

// vncWriteU32 writes one big-endian 32-bit value.
func vncWriteU32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}

// vncWriteReason writes a length-prefixed reason string.
func vncWriteReason(w io.Writer, reason string) error {
	if err := vncWriteU32(w, uint32(len(reason))); err != nil {
		return err
	}
	_, err := io.WriteString(w, reason)
	return err
}

// vncHandshake runs ProtocolVersion and Security on rw: VNC Authentication
// is the only type offered (no "None"). challenge is the 16 random bytes the
// viewer must encrypt. ok reports a viewer that proved the password; a wrong
// one is told so after onFail has run (the server waits there), with the
// reason where the version carries one. err is a broken connection or
// something that is not a VNC viewer.
func vncHandshake(rw io.ReadWriter, password string, challenge []byte, onFail func()) (minor int, ok bool, err error) {
	if _, err := io.WriteString(rw, vncServerVersion); err != nil {
		return 0, false, err
	}
	var ver [12]byte
	if _, err := io.ReadFull(rw, ver[:]); err != nil {
		return 0, false, err
	}
	if minor, err = vncParseClientVersion(ver[:]); err != nil {
		return 0, false, err
	}
	if minor == 3 {
		// 3.3: the server names the type.
		if err := vncWriteU32(rw, vncSecVNCAuth); err != nil {
			return minor, false, err
		}
	} else {
		if _, err := rw.Write([]byte{1, vncSecVNCAuth}); err != nil {
			return minor, false, err
		}
		var pick [1]byte
		if _, err := io.ReadFull(rw, pick[:]); err != nil {
			return minor, false, err
		}
		if pick[0] != vncSecVNCAuth {
			if minor >= 8 {
				_ = vncWriteU32(rw, 1)
				_ = vncWriteReason(rw, "This server only accepts a VNC password")
			}
			return minor, false, fmt.Errorf("the viewer picked security type %d, which was not offered", pick[0])
		}
	}
	if len(challenge) != 16 {
		return minor, false, errors.New("a VNC challenge is 16 bytes")
	}
	if _, err := rw.Write(challenge); err != nil {
		return minor, false, err
	}
	var resp [16]byte
	if _, err := io.ReadFull(rw, resp[:]); err != nil {
		return minor, false, err
	}
	if !vncAuthCheck(password, challenge, resp[:]) {
		if onFail != nil {
			onFail()
		}
		if err := vncWriteU32(rw, 1); err != nil {
			return minor, false, err
		}
		if minor >= 8 {
			_ = vncWriteReason(rw, "Wrong password")
		}
		return minor, false, nil
	}
	return minor, true, vncWriteU32(rw, 0)
}

// vncRefuse turns a viewer away before it signs in, with a reason it shows:
// no security types and the reason from 3.7 on, type 0 and the reason for
// 3.3.
func vncRefuse(rw io.ReadWriter, reason string) error {
	if _, err := io.WriteString(rw, vncServerVersion); err != nil {
		return err
	}
	var ver [12]byte
	if _, err := io.ReadFull(rw, ver[:]); err != nil {
		return err
	}
	minor, err := vncParseClientVersion(ver[:])
	if err != nil {
		return err
	}
	if minor == 3 {
		if err := vncWriteU32(rw, 0); err != nil {
			return err
		}
	} else if _, err := rw.Write([]byte{0}); err != nil {
		return err
	}
	return vncWriteReason(rw, reason)
}

// ── Pixel formats ─────────────────────────────────────────────────────────

// vncPixelFormat is RFB's PIXEL_FORMAT.
type vncPixelFormat struct {
	bpp, depth             uint8
	bigEndian, trueColour  bool
	rMax, gMax, bMax       uint16
	rShift, gShift, bShift uint8
}

// vncNativeFormat is the server's format: RGBX bytes read as a little-endian
// 32-bit pixel.
var vncNativeFormat = vncPixelFormat{
	bpp: 32, depth: 24, trueColour: true,
	rMax: 255, gMax: 255, bMax: 255,
	rShift: 0, gShift: 8, bShift: 16,
}

// vncParsePixelFormat decodes the 16 bytes of a PIXEL_FORMAT.
func vncParsePixelFormat(b []byte) vncPixelFormat {
	if len(b) < 13 {
		return vncPixelFormat{}
	}
	return vncPixelFormat{
		bpp: b[0], depth: b[1], bigEndian: b[2] != 0, trueColour: b[3] != 0,
		rMax:   binary.BigEndian.Uint16(b[4:]),
		gMax:   binary.BigEndian.Uint16(b[6:]),
		bMax:   binary.BigEndian.Uint16(b[8:]),
		rShift: b[10], gShift: b[11], bShift: b[12],
	}
}

// appendTo appends the 16 bytes of the format.
func (pf vncPixelFormat) appendTo(dst []byte) []byte {
	var be, tc byte
	if pf.bigEndian {
		be = 1
	}
	if pf.trueColour {
		tc = 1
	}
	dst = append(dst, pf.bpp, pf.depth, be, tc)
	dst = binary.BigEndian.AppendUint16(dst, pf.rMax)
	dst = binary.BigEndian.AppendUint16(dst, pf.gMax)
	dst = binary.BigEndian.AppendUint16(dst, pf.bMax)
	return append(dst, pf.rShift, pf.gShift, pf.bShift, 0, 0, 0)
}

// usable reports a format the server can send: true colour at 8, 16 or 32
// bits a pixel, with every channel inside the pixel. A colour-map format is
// not: the session keeps the format it had.
func (pf vncPixelFormat) usable() bool {
	if !pf.trueColour {
		return false
	}
	switch pf.bpp {
	case 8, 16, 32:
	default:
		return false
	}
	limit := uint64(1) << pf.bpp
	for _, ch := range [...]struct {
		max   uint16
		shift uint8
	}{{pf.rMax, pf.rShift}, {pf.gMax, pf.gShift}, {pf.bMax, pf.bShift}} {
		if ch.max == 0 || ch.shift >= pf.bpp || uint64(ch.max)<<ch.shift >= limit {
			return false
		}
	}
	return true
}

// vncConv turns RGBX pixels into one viewer's pixel format.
type vncConv struct {
	pf      vncPixelFormat
	r, g, b [256]uint32 // each channel's 8-bit value scaled to its max and shifted
	size    int         // bytes a pixel
	// csize and cskip are ZRLE's CPIXEL: csize bytes of the pixel as sent,
	// starting cskip bytes in (a 32-bit pixel whose colour fits in three
	// bytes sends only those three).
	csize, cskip int
	// native: the format is the server's own, so rows go out as stored.
	native bool
}

// newVncConv prepares the conversion to pf (a usable format).
func newVncConv(pf vncPixelFormat) *vncConv {
	c := &vncConv{pf: pf, size: int(pf.bpp) / 8}
	for i := 0; i < 256; i++ {
		v := uint32(i)
		c.r[i] = (v*uint32(pf.rMax) + 127) / 255 << pf.rShift
		c.g[i] = (v*uint32(pf.gMax) + 127) / 255 << pf.gShift
		c.b[i] = (v*uint32(pf.bMax) + 127) / 255 << pf.bShift
	}
	c.native = pf.bpp == 32 && !pf.bigEndian && pf.rMax == 255 && pf.gMax == 255 && pf.bMax == 255 &&
		pf.rShift == 0 && pf.gShift == 8 && pf.bShift == 16
	c.csize, c.cskip = c.size, 0
	if pf.bpp == 32 {
		// RFC 6143 7.7.6, as libvncserver and TigerVNC apply it: a colour that
		// fits in the low three bytes, or leaves the low byte empty, travels
		// as three bytes — the first three of the pixel as sent, or the last.
		mask := uint32(pf.rMax)<<pf.rShift | uint32(pf.gMax)<<pf.gShift | uint32(pf.bMax)<<pf.bShift
		ls3, ms3 := mask < 1<<24, mask&0xff == 0
		switch {
		case (ls3 && !pf.bigEndian) || (ms3 && pf.bigEndian):
			c.csize, c.cskip = 3, 0
		case (ls3 && pf.bigEndian) || (ms3 && !pf.bigEndian):
			c.csize, c.cskip = 3, 1
		}
	}
	return c
}

// value is the pixel value of the RGBX pixel at p.
func (c *vncConv) value(p []byte) uint32 {
	return c.r[p[0]] | c.g[p[1]] | c.b[p[2]]
}

// put appends one pixel value in the format's size and byte order.
func (c *vncConv) put(dst []byte, v uint32) []byte {
	switch c.size {
	case 1:
		return append(dst, byte(v))
	case 2:
		if c.pf.bigEndian {
			return append(dst, byte(v>>8), byte(v))
		}
		return append(dst, byte(v), byte(v>>8))
	}
	if c.pf.bigEndian {
		return binary.BigEndian.AppendUint32(dst, v)
	}
	return binary.LittleEndian.AppendUint32(dst, v)
}

// putC appends one pixel value as a ZRLE CPIXEL.
func (c *vncConv) putC(dst []byte, v uint32) []byte {
	if c.csize == c.size {
		return c.put(dst, v)
	}
	var full [4]byte
	if c.pf.bigEndian {
		binary.BigEndian.PutUint32(full[:], v)
	} else {
		binary.LittleEndian.PutUint32(full[:], v)
	}
	return append(dst, full[c.cskip:c.cskip+3]...)
}

// ── Client messages ───────────────────────────────────────────────────────

// vncRect is a rectangle of the framebuffer.
type vncRect struct{ x, y, w, h int }

// intersect is the part of r inside o (zero size when they do not meet).
func (r vncRect) intersect(o vncRect) vncRect {
	x0, y0 := max(r.x, o.x), max(r.y, o.y)
	x1, y1 := min(r.x+r.w, o.x+o.w), min(r.y+r.h, o.y+o.h)
	if x1 <= x0 || y1 <= y0 {
		return vncRect{}
	}
	return vncRect{x: x0, y: y0, w: x1 - x0, h: y1 - y0}
}

// union is the smallest rectangle holding both (an empty one adds nothing).
func (r vncRect) union(o vncRect) vncRect {
	if r.w <= 0 || r.h <= 0 {
		return o
	}
	if o.w <= 0 || o.h <= 0 {
		return r
	}
	x0, y0 := min(r.x, o.x), min(r.y, o.y)
	x1, y1 := max(r.x+r.w, o.x+o.w), max(r.y+r.h, o.y+o.h)
	return vncRect{x: x0, y: y0, w: x1 - x0, h: y1 - y0}
}

// vncClientMsg is one message from a viewer, decoded.
type vncClientMsg struct {
	kind      byte
	pf        vncPixelFormat // SetPixelFormat
	encodings []int32        // SetEncodings
	incr      bool           // FramebufferUpdateRequest
	rect      vncRect
	down      bool   // KeyEvent
	key       uint32 // an X11 keysym
	buttons   uint8  // PointerEvent: bit 0 left … bit 3/4 wheel up/down
	x, y      int
	text      string // ClientCutText, as UTF-8
	skipped   bool   // ClientCutText longer than vncMaxCutText: read past
}

// vncReadClientMessage reads one message. started runs as soon as its type
// byte has arrived, so the caller can give the rest a deadline while a quiet
// viewer may wait between messages as long as it likes. Every count and
// length the viewer sends is bounded before anything is allocated for it.
func vncReadClientMessage(r *bufio.Reader, started func()) (vncClientMsg, error) {
	t, err := r.ReadByte()
	if err != nil {
		return vncClientMsg{}, err
	}
	if started != nil {
		started()
	}
	m := vncClientMsg{kind: t}
	var b [20]byte
	switch t {
	case vncMsgSetPixelFormat:
		if _, err := io.ReadFull(r, b[:19]); err != nil {
			return m, err
		}
		m.pf = vncParsePixelFormat(b[3:19])
	case vncMsgSetEncodings:
		if _, err := io.ReadFull(r, b[:3]); err != nil {
			return m, err
		}
		n := int(binary.BigEndian.Uint16(b[1:3]))
		if n > vncMaxEncodings {
			return m, fmt.Errorf("SetEncodings lists %d encodings", n)
		}
		m.encodings = make([]int32, n)
		for i := range m.encodings {
			if _, err := io.ReadFull(r, b[:4]); err != nil {
				return m, err
			}
			m.encodings[i] = int32(binary.BigEndian.Uint32(b[:4]))
		}
	case vncMsgUpdateRequest:
		if _, err := io.ReadFull(r, b[:9]); err != nil {
			return m, err
		}
		m.incr = b[0] != 0
		m.rect = vncRect{
			x: int(binary.BigEndian.Uint16(b[1:])), y: int(binary.BigEndian.Uint16(b[3:])),
			w: int(binary.BigEndian.Uint16(b[5:])), h: int(binary.BigEndian.Uint16(b[7:])),
		}
	case vncMsgKeyEvent:
		if _, err := io.ReadFull(r, b[:7]); err != nil {
			return m, err
		}
		m.down = b[0] != 0
		m.key = binary.BigEndian.Uint32(b[3:7])
	case vncMsgPointerEvent:
		if _, err := io.ReadFull(r, b[:5]); err != nil {
			return m, err
		}
		m.buttons = b[0]
		m.x, m.y = int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
	case vncMsgCutText:
		if _, err := io.ReadFull(r, b[:7]); err != nil {
			return m, err
		}
		n := int64(int32(binary.BigEndian.Uint32(b[3:7])))
		switch {
		case n < 0:
			// A negative length is the extended clipboard, which this server
			// never offers.
			return m, errors.New("an extended-clipboard message the server did not offer")
		case n > vncMaxCutTextSkip:
			return m, fmt.Errorf("clipboard text of %d bytes", n)
		case n > vncMaxCutText:
			if _, err := io.CopyN(io.Discard, r, n); err != nil {
				return m, err
			}
			m.skipped = true
			return m, nil
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return m, err
		}
		m.text = vncCutTextString(buf)
	default:
		// The length of an unknown message cannot be known: the stream is lost.
		return m, fmt.Errorf("unknown message type %d", t)
	}
	return m, nil
}

// vncCutTextString reads clipboard text. RFB says Latin-1; several viewers
// send UTF-8 anyway, so text that is valid UTF-8 is taken as it is.
func vncCutTextString(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

// ── Tiles ─────────────────────────────────────────────────────────────────

// vncTilesAcross is how many tiles cover n pixels.
func vncTilesAcross(n int) int { return (n + vncTile - 1) / vncTile }

// vncMarkChanged sets dirty for every tile of a w×h RGBX frame (tightly
// packed) where cur differs from prev. Tiles already dirty are not compared.
func vncMarkChanged(dirty []bool, prev, cur []byte, w, h int) {
	cols, stride := vncTilesAcross(w), w*4
	if len(prev) < stride*h || len(cur) < stride*h || len(dirty) < cols*vncTilesAcross(h) {
		for i := range dirty {
			dirty[i] = true
		}
		return
	}
	for ty := 0; ty*vncTile < h; ty++ {
		y0, y1 := ty*vncTile, min((ty+1)*vncTile, h)
		for tx := 0; tx < cols; tx++ {
			i := ty*cols + tx
			if dirty[i] {
				continue
			}
			x0, x1 := tx*vncTile*4, min((tx+1)*vncTile, w)*4
			for y := y0; y < y1; y++ {
				o := y * stride
				if !bytes.Equal(prev[o+x0:o+x1], cur[o+x0:o+x1]) {
					dirty[i] = true
					break
				}
			}
		}
	}
}

// vncDirtyRuns appends the dirty tiles inside r (already clipped to the
// w×h framebuffer) as rectangles — neighbouring dirty tiles in a tile row
// make one — and clears the tiles it sent whole. A tile r only cuts through
// stays dirty, so the part outside r goes out with a later request.
func vncDirtyRuns(dirty []bool, w, h int, r vncRect, out []vncRect) []vncRect {
	if r.w <= 0 || r.h <= 0 {
		return out
	}
	cols, rows := vncTilesAcross(w), vncTilesAcross(h)
	fb := vncRect{w: w, h: h}
	tx0, tx1 := r.x/vncTile, min((r.x+r.w-1)/vncTile, cols-1)
	ty0, ty1 := r.y/vncTile, min((r.y+r.h-1)/vncTile, rows-1)
	for ty := ty0; ty <= ty1; ty++ {
		start := -1
		for tx := tx0; tx <= tx1+1; tx++ {
			on := tx <= tx1 && dirty[ty*cols+tx]
			if on && start < 0 {
				start = tx
			}
			if on || start < 0 {
				continue
			}
			run := vncRect{x: start * vncTile, y: ty * vncTile, w: (tx - start) * vncTile, h: vncTile}
			if part := run.intersect(fb).intersect(r); part.w > 0 && part.h > 0 {
				out = append(out, part)
			}
			for i := start; i < tx; i++ {
				tile := vncRect{x: i * vncTile, y: ty * vncTile, w: vncTile, h: vncTile}.intersect(fb)
				if tile.intersect(r) == tile {
					dirty[ty*cols+i] = false
				}
			}
			start = -1
		}
	}
	return out
}

// ── Encodings ─────────────────────────────────────────────────────────────

// vncAppendRectHeader appends a rectangle's header.
func vncAppendRectHeader(dst []byte, r vncRect, enc int32) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.x))
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.y))
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.w))
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.h))
	return binary.BigEndian.AppendUint32(dst, uint32(enc))
}

// appendRaw appends rectangle r of pix (RGBX, stride bytes a row) as Raw
// pixel data in the viewer's format.
func (c *vncConv) appendRaw(dst, pix []byte, stride int, r vncRect) []byte {
	for y := r.y; y < r.y+r.h; y++ {
		row := pix[y*stride+r.x*4 : y*stride+(r.x+r.w)*4]
		if c.native {
			dst = append(dst, row...)
			continue
		}
		for i := 0; i+3 < len(row); i += 4 {
			dst = c.put(dst, c.r[row[i]]|c.g[row[i+1]]|c.b[row[i+2]])
		}
	}
	return dst
}

// vncTileSolid reports whether the w×h tile at x, y is one colour, and that
// colour's RGBX pixel.
func vncTileSolid(pix []byte, stride, x, y, w, h int) ([]byte, bool) {
	o := y*stride + x*4
	r, g, b := pix[o], pix[o+1], pix[o+2]
	for yy := y; yy < y+h; yy++ {
		row := pix[yy*stride+x*4 : yy*stride+(x+w)*4]
		for i := 0; i+3 < len(row); i += 4 {
			if row[i] != r || row[i+1] != g || row[i+2] != b {
				return nil, false
			}
		}
	}
	return pix[o : o+4], true
}

// vncZRLE is one connection's ZRLE state: the zlib stream every rectangle
// continues (one stream a connection, RFC 6143 7.7.6) and scratch space.
type vncZRLE struct {
	zw   *zlib.Writer
	zout bytes.Buffer
	raw  []byte
}

// appendRect appends rectangle r of pix as a ZRLE body: the compressed
// length, then the data. Tiles run 64×64 from the rectangle's corner; a tile
// of one colour goes as "solid", any other as raw CPIXELs, and zlib does the
// rest.
func (z *vncZRLE) appendRect(dst []byte, c *vncConv, pix []byte, stride int, r vncRect) ([]byte, error) {
	raw := z.raw[:0]
	for ty := r.y; ty < r.y+r.h; ty += vncTile {
		th := min(vncTile, r.y+r.h-ty)
		for tx := r.x; tx < r.x+r.w; tx += vncTile {
			tw := min(vncTile, r.x+r.w-tx)
			if px, solid := vncTileSolid(pix, stride, tx, ty, tw, th); solid {
				raw = append(raw, 1)
				raw = c.putC(raw, c.value(px))
				continue
			}
			raw = append(raw, 0)
			for y := ty; y < ty+th; y++ {
				row := pix[y*stride+tx*4 : y*stride+(tx+tw)*4]
				for i := 0; i+3 < len(row); i += 4 {
					raw = c.putC(raw, c.r[row[i]]|c.g[row[i+1]]|c.b[row[i+2]])
				}
			}
		}
	}
	z.raw = raw
	if z.zw == nil {
		zw, err := zlib.NewWriterLevel(&z.zout, zlib.BestSpeed)
		if err != nil {
			return dst, err
		}
		z.zw = zw
	}
	z.zout.Reset()
	if _, err := z.zw.Write(raw); err != nil {
		return dst, err
	}
	if err := z.zw.Flush(); err != nil {
		return dst, err
	}
	dst = binary.BigEndian.AppendUint32(dst, uint32(z.zout.Len()))
	return append(dst, z.zout.Bytes()...), nil
}
