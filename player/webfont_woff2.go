package main

// webfont_woff2.go — WOFF2 web fonts unpacked to TrueType/OpenType.
//
// Most sites now serve their @font-face files as WOFF2 only, and the reader
// skipped them, so those pages kept the reader's own faces. A WOFF2 file is a
// table directory and one Brotli stream (webfont_brotli.go) holding the font's
// tables, two or three of them transformed to pack smaller:
//
//   - glyf and loca become seven streams (contour counts, point counts, point
//     flags, coordinates as variable-length triplets, composite records,
//     bounding boxes, instructions); the outlines are rebuilt from them and
//     written back as ordinary glyf records, with loca in the font's own
//     index format;
//   - hmtx may leave out the left side bearings that equal each glyph's xMin,
//     which come back from the rebuilt outlines.
//
// The rebuilt sfnt opens in SDL_ttf (FreeType) like a downloaded TrueType or
// OpenType file. The code was written beside a Python version of itself that
// rebuilds the VM's WOFF2 files (Lato, Roboto Slab, Font Awesome) and fonts
// re-encoded with every transform into fonts whose outlines, metrics and other
// tables match fontTools' own WOFF2 reader, and FreeType draws them. Font
// collections (one WOFF2 holding several fonts) are not read.

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// webWOFF2Tags are the 63 tags a table directory names by index.
var webWOFF2Tags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post", "cvt ", "fpgm", "glyf", "loca", "prep",
	"CFF ", "VORG", "EBDT", "EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea", "vmtx", "BASE",
	"GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH", "CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt",
	"avar", "bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar", "gvar", "hsty", "just", "lcar",
	"mort", "morx", "opbd", "prop", "trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

// webWOFF2Buf reads big-endian values from d[pos:end], panicking with
// webBrotliErr past end (webFontUnWOFF2 recovers it).
type webWOFF2Buf struct {
	d        []byte
	pos, end int
}

func (b *webWOFF2Buf) need(n int) {
	if n < 0 || b.pos+n > b.end {
		panic(webBrotliErr("font data ends early"))
	}
}

func (b *webWOFF2Buf) u8() int {
	b.need(1)
	v := int(b.d[b.pos])
	b.pos++
	return v
}

func (b *webWOFF2Buf) u16() int {
	b.need(2)
	v := int(binary.BigEndian.Uint16(b.d[b.pos:]))
	b.pos += 2
	return v
}

func (b *webWOFF2Buf) i16() int {
	return int(int16(uint16(b.u16())))
}

func (b *webWOFF2Buf) u32() uint32 {
	b.need(4)
	v := binary.BigEndian.Uint32(b.d[b.pos:])
	b.pos += 4
	return v
}

func (b *webWOFF2Buf) bytes(n int) []byte {
	b.need(n)
	v := b.d[b.pos : b.pos+n]
	b.pos += n
	return v
}

// base128 is WOFF2's UIntBase128: up to five bytes of seven bits each.
func (b *webWOFF2Buf) base128() int {
	v := uint32(0)
	for i := 0; i < 5; i++ {
		c := b.u8()
		if i == 0 && c == 0x80 {
			panic(webBrotliErr("UIntBase128 with a leading zero"))
		}
		if v&0xFE000000 != 0 {
			panic(webBrotliErr("UIntBase128 overflow"))
		}
		v = v<<7 | uint32(c&0x7F)
		if c&0x80 == 0 {
			if v > 1<<30 {
				panic(webBrotliErr("table too large"))
			}
			return int(v)
		}
	}
	panic(webBrotliErr("UIntBase128 too long"))
}

// u255 is WOFF2's 255UInt16.
func (b *webWOFF2Buf) u255() int {
	c := b.u8()
	switch c {
	case 253:
		return b.u16()
	case 255:
		return b.u8() + 253
	case 254:
		return b.u8() + 506
	}
	return c
}

// webWOFF2Point is one outline point as rebuilt.
type webWOFF2Point struct {
	x, y int
	on   bool
}

// webWOFF2Triplets decodes n points: one flag byte each from flags, their
// coordinates as triplet-encoded deltas from coords.
func webWOFF2Triplets(flags, coords *webWOFF2Buf, n int) []webWOFF2Point {
	pts := make([]webWOFF2Point, 0, n)
	withSign := func(flag, v int) int {
		if flag&1 != 0 {
			return v
		}
		return -v
	}
	x, y := 0, 0
	for i := 0; i < n; i++ {
		f := flags.u8()
		on := f>>7 == 0
		f &= 0x7F
		var dx, dy int
		switch {
		case f < 10:
			dy = withSign(f, (f&14)<<7+coords.u8())
		case f < 20:
			dx = withSign(f, ((f-10)&14)<<7+coords.u8())
		case f < 84:
			b0 := f - 20
			b1 := coords.u8()
			dx = withSign(f, 1+(b0&0x30)+(b1>>4))
			dy = withSign(f>>1, 1+(b0&0x0C)<<2+(b1&0x0F))
		case f < 120:
			b0 := f - 84
			a := coords.u8()
			c := coords.u8()
			dx = withSign(f, 1+(b0/12)<<8+a)
			dy = withSign(f>>1, 1+((b0%12)>>2)<<8+c)
		case f < 124:
			a := coords.u8()
			b2 := coords.u8()
			c := coords.u8()
			dx = withSign(f, a<<4+b2>>4)
			dy = withSign(f>>1, (b2&0x0F)<<8+c)
		default:
			a := coords.u8()
			b1 := coords.u8()
			c := coords.u8()
			d := coords.u8()
			dx = withSign(f, a<<8+b1)
			dy = withSign(f>>1, c<<8+d)
		}
		x += dx
		y += dy
		if x < -32768 || x > 32767 || y < -32768 || y > 32767 {
			panic(webBrotliErr("outline point out of range"))
		}
		pts = append(pts, webWOFF2Point{x: x, y: y, on: on})
	}
	return pts
}

// webWOFF2Simple writes one simple glyph record.
func webWOFF2Simple(out []byte, ends []int, instr []byte, pts []webWOFF2Point, overlap bool, bbox [4]int) []byte {
	be := binary.BigEndian
	out = be.AppendUint16(out, uint16(len(ends)))
	for _, v := range bbox {
		out = be.AppendUint16(out, uint16(int16(v)))
	}
	for _, e := range ends {
		out = be.AppendUint16(out, uint16(e))
	}
	out = be.AppendUint16(out, uint16(len(instr)))
	out = append(out, instr...)
	flags := make([]byte, 0, len(pts))
	var xs, ys []byte
	px, py := 0, 0
	for i, p := range pts {
		f := byte(0)
		if p.on {
			f = 0x01
		}
		if i == 0 && overlap {
			f |= 0x40
		}
		dx, dy := p.x-px, p.y-py
		switch {
		case dx == 0:
			f |= 0x10
		case dx >= -255 && dx <= 255:
			f |= 0x02
			if dx > 0 {
				f |= 0x10
			}
			xs = append(xs, byte(max(dx, -dx)))
		default:
			xs = be.AppendUint16(xs, uint16(int16(dx)))
		}
		switch {
		case dy == 0:
			f |= 0x20
		case dy >= -255 && dy <= 255:
			f |= 0x04
			if dy > 0 {
				f |= 0x20
			}
			ys = append(ys, byte(max(dy, -dy)))
		default:
			ys = be.AppendUint16(ys, uint16(int16(dy)))
		}
		flags = append(flags, f)
		px, py = p.x, p.y
	}
	out = append(out, flags...)
	out = append(out, xs...)
	return append(out, ys...)
}

// webWOFF2Glyf rebuilds glyf and loca from the transformed glyf table at
// data[off:off+n], and returns each glyph's xMin for hmtx.
func webWOFF2Glyf(data []byte, off, n int) (glyf, loca []byte, xmins []int) {
	be := binary.BigEndian
	b := &webWOFF2Buf{d: data, pos: off, end: off + n}
	b.u16() // version
	opt := b.u16()
	ng := b.u16()
	ifmt := b.u16()
	var sizes [7]int
	for i := range sizes {
		sizes[i] = int(b.u32())
	}
	st := b.pos
	var streams [7]*webWOFF2Buf
	for i, sz := range sizes {
		if sz < 0 || st+sz > off+n {
			panic(webBrotliErr("glyf stream past its table"))
		}
		streams[i] = &webWOFF2Buf{d: data, pos: st, end: st + sz}
		st += sz
	}
	ncs, nps, fls, gls, cps, bbs, ins := streams[0], streams[1], streams[2], streams[3], streams[4], streams[5], streams[6]
	var overlap []byte
	if opt&1 != 0 {
		m := (ng + 7) >> 3
		if st+m > off+n {
			panic(webBrotliErr("overlap bitmap past its table"))
		}
		overlap = data[st : st+m]
	}
	bboxMap := bbs.bytes(((ng + 31) >> 5) << 2)
	align := 2
	if ifmt != 0 {
		align = 4
	}
	offsets := make([]int, 0, ng+1)
	offsets = append(offsets, 0)
	xmins = make([]int, 0, ng)
	for g := 0; g < ng; g++ {
		nc := ncs.i16()
		hasBox := bboxMap[g>>3]&(0x80>>(g&7)) != 0
		switch {
		case nc == 0:
			if hasBox {
				panic(webBrotliErr("empty glyph with a bounding box"))
			}
			xmins = append(xmins, 0)
		case nc > 0:
			ends := make([]int, nc)
			total := 0
			for c := 0; c < nc; c++ {
				total += nps.u255()
				ends[c] = total - 1
			}
			if total > 65535 {
				panic(webBrotliErr("too many points"))
			}
			pts := webWOFF2Triplets(fls, gls, total)
			instr := ins.bytes(gls.u255())
			var box [4]int
			if hasBox {
				for k := range box {
					box[k] = bbs.i16()
				}
			} else if len(pts) > 0 {
				box = [4]int{pts[0].x, pts[0].y, pts[0].x, pts[0].y}
				for _, p := range pts[1:] {
					box[0], box[1] = min(box[0], p.x), min(box[1], p.y)
					box[2], box[3] = max(box[2], p.x), max(box[3], p.y)
				}
			}
			ov := overlap != nil && overlap[g>>3]&(0x80>>(g&7)) != 0
			glyf = webWOFF2Simple(glyf, ends, instr, pts, ov, box)
			xmins = append(xmins, box[0])
		case nc == -1:
			if !hasBox {
				panic(webBrotliErr("composite glyph without a bounding box"))
			}
			start := cps.pos
			haveInstr := false
			for {
				fl := cps.u16()
				cps.u16()
				if fl&1 != 0 {
					cps.bytes(4)
				} else {
					cps.bytes(2)
				}
				switch {
				case fl&0x08 != 0:
					cps.bytes(2)
				case fl&0x40 != 0:
					cps.bytes(4)
				case fl&0x80 != 0:
					cps.bytes(8)
				}
				if fl&0x100 != 0 {
					haveInstr = true
				}
				if fl&0x20 == 0 {
					break
				}
			}
			comp := data[start:cps.pos]
			var box [4]int
			for k := range box {
				box[k] = bbs.i16()
			}
			glyf = be.AppendUint16(glyf, 0xFFFF)
			for _, v := range box {
				glyf = be.AppendUint16(glyf, uint16(int16(v)))
			}
			glyf = append(glyf, comp...)
			if haveInstr {
				il := gls.u255()
				glyf = be.AppendUint16(glyf, uint16(il))
				glyf = append(glyf, ins.bytes(il)...)
			}
			xmins = append(xmins, box[0])
		default:
			panic(webBrotliErr("bad contour count"))
		}
		for len(glyf)%align != 0 {
			glyf = append(glyf, 0)
		}
		offsets = append(offsets, len(glyf))
	}
	if ifmt != 0 {
		for _, o := range offsets {
			loca = be.AppendUint32(loca, uint32(o))
		}
	} else {
		if offsets[len(offsets)-1] > 0x1FFFE {
			panic(webBrotliErr("glyf too large for a short loca"))
		}
		for _, o := range offsets {
			loca = be.AppendUint16(loca, uint16(o>>1))
		}
	}
	return glyf, loca, xmins
}

// webWOFF2Hmtx rebuilds hmtx from its transformed form at data[off:off+n].
func webWOFF2Hmtx(data []byte, off, n, numGlyphs, numHMetrics int, xmins []int) []byte {
	be := binary.BigEndian
	b := &webWOFF2Buf{d: data, pos: off, end: off + n}
	flags := b.u8()
	if numHMetrics < 1 || numHMetrics > numGlyphs || len(xmins) < numGlyphs {
		panic(webBrotliErr("hmtx does not fit the font"))
	}
	adv := make([]int, numHMetrics)
	for i := range adv {
		adv[i] = b.u16()
	}
	lsb := make([]int, numGlyphs)
	for i := 0; i < numHMetrics; i++ {
		if flags&1 != 0 {
			lsb[i] = xmins[i]
		} else {
			lsb[i] = b.i16()
		}
	}
	for i := numHMetrics; i < numGlyphs; i++ {
		if flags&2 != 0 {
			lsb[i] = xmins[i]
		} else {
			lsb[i] = b.i16()
		}
	}
	out := make([]byte, 0, numHMetrics*4+(numGlyphs-numHMetrics)*2)
	for i := 0; i < numGlyphs; i++ {
		if i < numHMetrics {
			out = be.AppendUint16(out, uint16(adv[i]))
		}
		out = be.AppendUint16(out, uint16(int16(lsb[i])))
	}
	return out
}

// webWOFF2Checksum is a table's sfnt checksum.
func webWOFF2Checksum(t []byte) uint32 {
	var sum uint32
	for i := 0; i < len(t); i += 4 {
		var w [4]byte
		copy(w[:], t[i:])
		sum += binary.BigEndian.Uint32(w[:])
	}
	return sum
}

// webFontUnWOFF2 rebuilds the sfnt a WOFF2 file packs, at most limit bytes.
func webFontUnWOFF2(data []byte, limit int64) (out []byte, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			logDebug("browser", "WOFF2 font not read: %v", r)
			out, ok = nil, false
		}
	}()
	sfnt, err := webWOFF2Decode(data, int(min(limit, 1<<30)))
	if err != nil {
		logDebug("browser", "WOFF2 font not read: %v", err)
		return nil, false
	}
	return sfnt, true
}

type webWOFF2Table struct {
	tag         string
	orig, size  int
	transformed bool
	off         int
}

func webWOFF2Decode(data []byte, limit int) ([]byte, error) {
	be := binary.BigEndian
	b := &webWOFF2Buf{d: data, end: len(data)}
	if string(b.bytes(4)) != "wOF2" {
		return nil, fmt.Errorf("not WOFF2")
	}
	flavor := b.u32()
	b.u32() // length
	num := b.u16()
	b.u16() // reserved
	b.u32() // totalSfntSize
	compLen := int(b.u32())
	b.bytes(4 + 4*5) // versions, metadata and private block
	if flavor == 0x74746366 {
		return nil, fmt.Errorf("font collections are not read")
	}
	if num == 0 || num > 512 {
		return nil, fmt.Errorf("%d tables", num)
	}
	tables := make([]webWOFF2Table, num)
	for i := range tables {
		fl := b.u8()
		tag := ""
		if fl&63 == 63 {
			tag = string(b.bytes(4))
		} else {
			tag = webWOFF2Tags[fl&63]
		}
		ver := fl >> 6
		orig := b.base128()
		isGlyf := tag == "glyf" || tag == "loca"
		transformed := (isGlyf && ver == 0) || (tag == "hmtx" && ver == 1)
		if !transformed && ver != 0 && !(isGlyf && ver == 3) {
			return nil, fmt.Errorf("table %q has unknown transform %d", tag, ver)
		}
		size := orig
		if transformed {
			size = b.base128()
		}
		tables[i] = webWOFF2Table{tag: tag, orig: orig, size: size, transformed: transformed}
	}
	if compLen < 0 || b.pos+compLen > len(data) {
		return nil, fmt.Errorf("compressed data past the file")
	}
	raw, err := webBrotliDecode(data[b.pos:b.pos+compLen], limit)
	if err != nil {
		return nil, err
	}
	pos := 0
	by := make(map[string]int, num)
	for i := range tables {
		tables[i].off = pos
		pos += tables[i].size
		if pos > len(raw) {
			return nil, fmt.Errorf("table %q past the data", tables[i].tag)
		}
		by[tables[i].tag] = i
	}
	result := make(map[string][]byte, num)
	for _, t := range tables {
		if !t.transformed {
			result[t.tag] = raw[t.off : t.off+t.size]
		}
	}
	var xmins []int
	if gi, ok := by["glyf"]; ok && tables[gi].transformed {
		li, ok := by["loca"]
		if !ok || !tables[li].transformed {
			return nil, fmt.Errorf("transformed glyf without loca")
		}
		var glyf, loca []byte
		glyf, loca, xmins = webWOFF2Glyf(raw, tables[gi].off, tables[gi].size)
		result["glyf"], result["loca"] = glyf, loca
	}
	if hi, ok := by["hmtx"]; ok && tables[hi].transformed {
		hhea, maxp := result["hhea"], result["maxp"]
		if xmins == nil || len(hhea) < 36 || len(maxp) < 6 {
			return nil, fmt.Errorf("transformed hmtx without glyf, hhea or maxp")
		}
		nh := int(be.Uint16(hhea[34:36]))
		ng := int(be.Uint16(maxp[4:6]))
		result["hmtx"] = webWOFF2Hmtx(raw, tables[hi].off, tables[hi].size, ng, nh, xmins)
	}
	tags := make([]string, 0, len(result))
	for tag := range result {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	n := len(tags)
	sel := 0
	for 1<<(sel+1) <= n {
		sel++
	}
	searchRange := (1 << sel) * 16
	total := 12 + 16*n
	for _, tag := range tags {
		total += (len(result[tag]) + 3) &^ 3
	}
	if total > limit {
		return nil, fmt.Errorf("font larger than %d bytes", limit)
	}
	out := make([]byte, 12+16*n, total)
	be.PutUint32(out[0:], flavor)
	be.PutUint16(out[4:], uint16(n))
	be.PutUint16(out[6:], uint16(searchRange))
	be.PutUint16(out[8:], uint16(sel))
	be.PutUint16(out[10:], uint16(n*16-searchRange))
	for i, tag := range tags {
		t := result[tag]
		rec := out[12+16*i : 12+16*i+16]
		copy(rec[0:4], tag)
		be.PutUint32(rec[4:], webWOFF2Checksum(t))
		be.PutUint32(rec[8:], uint32(len(out)))
		be.PutUint32(rec[12:], uint32(len(t)))
		out = append(out, t...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out, nil
}
