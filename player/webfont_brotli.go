package main

// webfont_brotli.go — a Brotli decoder (RFC 7932), for WOFF2 web fonts.
//
// WOFF2 packs a font's tables into one Brotli stream (webfont_woff2.go), and
// Go's standard library has no Brotli. This is a plain decoder of the whole
// format — meta-blocks stored, compressed and empty; simple and complex prefix
// codes; block types and counts for the three categories; context modes and
// context maps with move-to-front; distance codes with the last-distance ring,
// direct codes and postfix bits; and references into the static dictionary
// with its 121 word transforms (webfont_brotli_data.go). It favours being easy
// to check over speed: prefix codes are read a bit at a time (canonical codes,
// as zlib's "puff" reads Deflate's), which is fast enough for the few hundred
// kilobytes a font unpacks to. It was written beside a Python version of the
// same code that decodes, byte for byte, what the reference encoder
// (libbrotlienc) produces at every quality and window size, including streams
// that use 70 of the dictionary's transforms.
//
// Malformed input is an error, never a crash: the decoder panics with
// webBrotliErr on anything out of bounds and webBrotliDecode recovers it.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
)

// webBrotliErr is a malformed stream, raised as a panic inside the decoder.
type webBrotliErr string

var (
	webBrotliInsBase   = [24]uint32{0, 1, 2, 3, 4, 5, 6, 8, 10, 14, 18, 26, 34, 50, 66, 98, 130, 194, 322, 578, 1090, 2114, 6210, 22594}
	webBrotliInsExtra  = [24]uint8{0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 12, 14, 24}
	webBrotliCopyBase  = [24]uint32{2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 14, 18, 22, 30, 38, 54, 70, 102, 134, 198, 326, 582, 1094, 2118}
	webBrotliCopyExtra = [24]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 24}
	// The insert-and-copy alphabet's 64-code cells: the first insert and copy
	// length codes of each.
	webBrotliCellIns  = [11]uint8{0, 0, 0, 0, 8, 8, 0, 16, 8, 16, 16}
	webBrotliCellCopy = [11]uint8{0, 8, 0, 8, 0, 8, 16, 0, 16, 8, 16}
	// The order code-length code lengths come in, and the fixed variable-length
	// code they are written with (by the next four bits).
	webBrotliCLOrder     = [18]uint8{1, 2, 3, 4, 0, 5, 17, 6, 16, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	webBrotliCLPrefixLen = [16]uint8{2, 2, 2, 3, 2, 2, 2, 4, 2, 2, 2, 3, 2, 2, 2, 4}
	webBrotliCLPrefixVal = [16]uint8{0, 4, 3, 2, 0, 4, 3, 1, 0, 4, 3, 2, 0, 4, 3, 5}
	// A short distance code: which of the last distances, and the change.
	webBrotliDistIdx   = [16]int{0, 1, 2, 3, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1}
	webBrotliDistDelta = [16]int{0, 0, 0, 0, -1, 1, -2, 2, -3, 3, -1, 1, -2, 2, -3, 3}
)

// webBrotliDict is the static dictionary, unpacked and checked once; nil
// when that failed (WOFF2 fonts are then skipped).
var webBrotliDict = struct {
	once sync.Once
	data []byte
}{}

func webBrotliDictionary() []byte {
	webBrotliDict.once.Do(func() {
		gz, err := base64.StdEncoding.DecodeString(strings.TrimSpace(webBrotliDictGz))
		if err != nil {
			logWarn("browser", "Brotli dictionary unreadable: %v", err)
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			logWarn("browser", "Brotli dictionary unreadable: %v", err)
			return
		}
		data, err := io.ReadAll(io.LimitReader(zr, webBrotliDictLen+1))
		_ = zr.Close()
		sum := sha256.Sum256(data)
		if err != nil || len(data) != webBrotliDictLen || hex.EncodeToString(sum[:]) != webBrotliDictSHA256 {
			logWarn("browser", "Brotli dictionary damaged (%d bytes): WOFF2 fonts are skipped", len(data))
			return
		}
		webBrotliDict.data = data
	})
	return webBrotliDict.data
}

// webBrotliBits reads a stream's bits, least significant first.
type webBrotliBits struct {
	d   []byte
	pos int
}

func (b *webBrotliBits) read(n uint) uint32 {
	var v uint32
	for i := uint(0); i < n; i++ {
		j := b.pos >> 3
		if j >= len(b.d) {
			panic(webBrotliErr("stream ends early"))
		}
		v |= uint32(b.d[j]>>(uint(b.pos)&7)&1) << i
		b.pos++
	}
	return v
}

// peek reads n bits without moving, zeros past the end.
func (b *webBrotliBits) peek(n uint) uint32 {
	var v uint32
	p := b.pos
	for i := uint(0); i < n; i++ {
		if j := p >> 3; j < len(b.d) {
			v |= uint32(b.d[j]>>(uint(p)&7)&1) << i
		}
		p++
	}
	return v
}

// align moves to the next byte; the bits skipped must be zero.
func (b *webBrotliBits) align() {
	for b.pos&7 != 0 {
		if b.read(1) != 0 {
			panic(webBrotliErr("non-zero padding"))
		}
	}
}

// webBrotliHuff is a canonical prefix code, read one bit at a time with the
// code's most significant bit first. single >= 0 is a code of one symbol,
// read with no bits.
type webBrotliHuff struct {
	single int
	count  [16]int
	sym    []uint16
}

func webBrotliNewHuff(lengths []uint8) *webBrotliHuff {
	h := &webBrotliHuff{single: -1}
	used, last := 0, 0
	for i, l := range lengths {
		if l != 0 {
			used++
			last = i
		}
	}
	if used == 1 {
		h.single = last
		return h
	}
	for _, l := range lengths {
		h.count[l]++
	}
	h.count[0] = 0
	var offs [16]int
	for l := 1; l < 15; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	h.sym = make([]uint16, used)
	for i, l := range lengths {
		if l != 0 {
			h.sym[offs[l]] = uint16(i)
			offs[l]++
		}
	}
	return h
}

func (h *webBrotliHuff) decode(b *webBrotliBits) int {
	if h.single >= 0 {
		return h.single
	}
	code, first, index := 0, 0, 0
	for l := 1; l < 16; l++ {
		code |= int(b.read(1))
		c := h.count[l]
		if code-c < first {
			return int(h.sym[index+code-first])
		}
		index += c
		first += c
		first <<= 1
		code <<= 1
	}
	panic(webBrotliErr("bad prefix code"))
}

// webBrotliBitLen is how many bits hold x.
func webBrotliBitLen(x int) uint {
	n := uint(0)
	for x > 0 {
		x >>= 1
		n++
	}
	return n
}

// webBrotliReadCode reads a prefix code over an alphabet of alpha symbols:
// a simple one (up to four symbols listed) or a complex one (code lengths,
// themselves prefix-coded, with runs).
func webBrotliReadCode(b *webBrotliBits, alpha int) *webBrotliHuff {
	hskip := int(b.read(2))
	if hskip == 1 {
		nsym := int(b.read(2)) + 1
		nbits := webBrotliBitLen(alpha - 1)
		syms := make([]int, nsym)
		for i := range syms {
			v := int(b.read(nbits))
			if v >= alpha {
				panic(webBrotliErr("simple code symbol out of range"))
			}
			for j := 0; j < i; j++ {
				if syms[j] == v {
					panic(webBrotliErr("simple code repeats a symbol"))
				}
			}
			syms[i] = v
		}
		if nsym == 1 {
			return &webBrotliHuff{single: syms[0]}
		}
		var byPos []uint8
		switch nsym {
		case 2:
			byPos = []uint8{1, 1}
		case 3:
			byPos = []uint8{1, 2, 2}
		default:
			if b.read(1) != 0 {
				byPos = []uint8{1, 2, 3, 3}
			} else {
				byPos = []uint8{2, 2, 2, 2}
			}
		}
		lengths := make([]uint8, alpha)
		for i, s := range syms {
			lengths[s] = byPos[i]
		}
		return webBrotliNewHuff(lengths)
	}
	var cl [18]uint8
	space, num := 32, 0
	for i := hskip; i < 18; i++ {
		idx := webBrotliCLOrder[i]
		p := b.peek(4)
		b.read(uint(webBrotliCLPrefixLen[p]))
		v := webBrotliCLPrefixVal[p]
		cl[idx] = v
		if v != 0 {
			space -= 32 >> v
			num++
			if space <= 0 {
				break
			}
		}
	}
	if !(num == 1 || space == 0) {
		panic(webBrotliErr("code length code is incomplete"))
	}
	clh := webBrotliNewHuff(cl[:])
	lengths := make([]uint8, alpha)
	sym, prev, repeat, rlen := 0, 8, 0, 0
	space = 32768
	for sym < alpha && space > 0 {
		c := clh.decode(b)
		if c < 16 {
			repeat = 0
			lengths[sym] = uint8(c)
			if c != 0 {
				prev = c
				space -= 32768 >> c
			}
			sym++
			continue
		}
		extra, next := uint(3), 0
		if c == 16 {
			extra, next = 2, prev
		}
		if rlen != next {
			repeat, rlen = 0, next
		}
		old := repeat
		if repeat > 0 {
			repeat -= 2
			repeat <<= extra
		}
		repeat += int(b.read(extra)) + 3
		delta := repeat - old
		if sym+delta > alpha {
			panic(webBrotliErr("code length run past the alphabet"))
		}
		for k := 0; k < delta; k++ {
			lengths[sym+k] = uint8(rlen)
		}
		sym += delta
		if rlen != 0 {
			space -= delta * (32768 >> rlen)
		}
	}
	if space != 0 {
		panic(webBrotliErr("prefix code is incomplete"))
	}
	return webBrotliNewHuff(lengths)
}

// webBrotliVarUint8 reads the 0-255 counts of block types and trees.
func webBrotliVarUint8(b *webBrotliBits) int {
	if b.read(1) == 0 {
		return 0
	}
	n := b.read(3)
	if n == 0 {
		return 1
	}
	return (1 << n) + int(b.read(uint(n)))
}

func webBrotliBlockLen(b *webBrotliBits, h *webBrotliHuff) int {
	c := h.decode(b)
	if c >= len(webBrotliBlockLength) {
		panic(webBrotliErr("bad block length code"))
	}
	e := webBrotliBlockLength[c]
	return int(e[0]) + int(b.read(uint(e[1])))
}

// webBrotliContextMap reads a context map of size entries over ntrees trees.
func webBrotliContextMap(b *webBrotliBits, size, ntrees int) []uint8 {
	m := make([]uint8, 0, size)
	if ntrees < 2 {
		return m[:size]
	}
	rlemax := 0
	if b.read(1) != 0 {
		rlemax = int(b.read(4)) + 1
	}
	h := webBrotliReadCode(b, ntrees+rlemax)
	for len(m) < size {
		c := h.decode(b)
		switch {
		case c == 0:
			m = append(m, 0)
		case c <= rlemax:
			reps := (1 << uint(c)) + int(b.read(uint(c)))
			if len(m)+reps > size {
				panic(webBrotliErr("context map run too long"))
			}
			for k := 0; k < reps; k++ {
				m = append(m, 0)
			}
		default:
			m = append(m, uint8(c-rlemax))
		}
	}
	if b.read(1) != 0 {
		// The inverse move-to-front transform.
		var mtf [256]uint8
		for i := range mtf {
			mtf[i] = uint8(i)
		}
		for i := range m {
			idx := int(m[i])
			v := mtf[idx]
			m[i] = v
			copy(mtf[1:idx+1], mtf[:idx])
			mtf[0] = v
		}
	}
	return m
}

// webBrotliUpper capitalises the character at w[i] the way the format does
// (ASCII letters, and a fixed bit flip for longer UTF-8 sequences) and
// returns its length.
func webBrotliUpper(w []byte, i int) int {
	c := w[i]
	if c < 0xC0 {
		if c >= 'a' && c <= 'z' {
			w[i] ^= 32
		}
		return 1
	}
	if c < 0xE0 {
		if i+1 < len(w) {
			w[i+1] ^= 32
		}
		return 2
	}
	if i+2 < len(w) {
		w[i+2] ^= 5
	}
	return 3
}

// webBrotliTransformWord applies transform tid to a dictionary word.
func webBrotliTransformWord(word []byte, tid int) []byte {
	t := webBrotliTransforms[tid]
	w := append([]byte(nil), word...)
	switch {
	case t.typ >= 1 && t.typ <= 9:
		w = w[:max(0, len(w)-int(t.typ))]
	case t.typ >= 12 && t.typ <= 20:
		w = w[min(len(w), int(t.typ)-11):]
	}
	switch t.typ {
	case 10:
		if len(w) > 0 {
			webBrotliUpper(w, 0)
		}
	case 11:
		for i := 0; i < len(w); {
			i += webBrotliUpper(w, i)
		}
	}
	out := make([]byte, 0, len(t.pre)+len(w)+len(t.suf))
	out = append(out, t.pre...)
	out = append(out, w...)
	return append(out, t.suf...)
}

// webBrotliDecode decompresses a whole Brotli stream of at most limit bytes.
func webBrotliDecode(data []byte, limit int) (out []byte, err error) {
	dict := webBrotliDictionary()
	if dict == nil {
		return nil, fmt.Errorf("no Brotli dictionary")
	}
	defer func() {
		if r := recover(); r != nil {
			out = nil
			if e, ok := r.(webBrotliErr); ok {
				err = fmt.Errorf("brotli: %s", string(e))
				return
			}
			err = fmt.Errorf("brotli: %v", r)
		}
	}()
	return webBrotliRun(data, limit, dict), nil
}

func webBrotliRun(data []byte, limit int, dict []byte) []byte {
	b := &webBrotliBits{d: data}
	wbits := uint(16)
	if b.read(1) != 0 {
		if n := b.read(3); n != 0 {
			wbits = 17 + uint(n)
		} else {
			n = b.read(3)
			switch {
			case n == 1:
				panic(webBrotliErr("large-window streams are not supported"))
			case n != 0:
				wbits = 8 + uint(n)
			default:
				wbits = 17
			}
		}
	}
	maxBack := (1 << wbits) - 16
	out := make([]byte, 0, min(limit, 1<<20))
	rb := [4]int{16, 15, 11, 4} // rb[3] is the last distance
	for {
		last := b.read(1) != 0
		if last && b.read(1) != 0 {
			break
		}
		mn := b.read(2)
		if mn == 3 {
			// Metadata: skipped.
			if b.read(1) != 0 {
				panic(webBrotliErr("reserved bit set"))
			}
			nb := int(b.read(2))
			skip := 0
			for i := 0; i < nb; i++ {
				v := int(b.read(8))
				if i+1 == nb && nb > 1 && v == 0 {
					panic(webBrotliErr("metadata length not minimal"))
				}
				skip |= v << (8 * uint(i))
			}
			if nb > 0 {
				skip++
			}
			b.align()
			b.pos += 8 * skip
			if b.pos>>3 > len(data) {
				panic(webBrotliErr("metadata past the end"))
			}
			if last {
				break
			}
			continue
		}
		nib := int(mn) + 4
		mlen := 0
		for i := 0; i < nib; i++ {
			v := int(b.read(4))
			if i+1 == nib && nib > 4 && v == 0 {
				panic(webBrotliErr("length not minimal"))
			}
			mlen |= v << (4 * uint(i))
		}
		mlen++
		if len(out)+mlen > limit {
			panic(webBrotliErr("output too large"))
		}
		if !last && b.read(1) != 0 {
			// Stored bytes.
			b.align()
			st := b.pos >> 3
			if st+mlen > len(data) {
				panic(webBrotliErr("stored block past the end"))
			}
			out = append(out, data[st:st+mlen]...)
			b.pos += 8 * mlen
			continue
		}
		out, rb = webBrotliMetaBlock(b, out, mlen, maxBack, rb, dict)
		if last {
			break
		}
	}
	return out
}

// webBrotliMetaBlock decodes one compressed meta-block of mlen bytes onto
// out, and returns out and the distance ring as they end.
func webBrotliMetaBlock(b *webBrotliBits, out []byte, mlen, maxBack int, rb [4]int, dict []byte) ([]byte, [4]int) {
	var nbl [3]int
	var typeH, lenH [3]*webBrotliHuff
	blen := [3]int{1 << 28, 1 << 28, 1 << 28}
	typeRB := [3][2]int{{1, 0}, {1, 0}, {1, 0}}
	var btype [3]int
	for k := 0; k < 3; k++ {
		nbl[k] = webBrotliVarUint8(b) + 1
		if nbl[k] >= 2 {
			typeH[k] = webBrotliReadCode(b, nbl[k]+2)
			lenH[k] = webBrotliReadCode(b, 26)
			blen[k] = webBrotliBlockLen(b, lenH[k])
		}
	}
	npostfix := uint(b.read(2))
	ndirect := int(b.read(4)) << npostfix
	cmodes := make([]uint8, nbl[0])
	for i := range cmodes {
		cmodes[i] = uint8(b.read(2))
	}
	ntl := webBrotliVarUint8(b) + 1
	cmapL := webBrotliContextMap(b, 64*nbl[0], ntl)
	ntd := webBrotliVarUint8(b) + 1
	cmapD := webBrotliContextMap(b, 4*nbl[2], ntd)
	litH := make([]*webBrotliHuff, ntl)
	for i := range litH {
		litH[i] = webBrotliReadCode(b, 256)
	}
	cmdH := make([]*webBrotliHuff, nbl[1])
	for i := range cmdH {
		cmdH[i] = webBrotliReadCode(b, 704)
	}
	distH := make([]*webBrotliHuff, ntd)
	for i := range distH {
		distH[i] = webBrotliReadCode(b, 16+ndirect+(48<<npostfix))
	}
	switchType := func(k int) {
		c := typeH[k].decode(b)
		var t int
		switch c {
		case 1:
			t = typeRB[k][1] + 1
		case 0:
			t = typeRB[k][0]
		default:
			t = c - 2
		}
		if t >= nbl[k] {
			t -= nbl[k]
		}
		typeRB[k][0], typeRB[k][1] = typeRB[k][1], t
		btype[k] = t
		blen[k] = webBrotliBlockLen(b, lenH[k])
	}
	remaining := mlen
	for remaining > 0 {
		if blen[1] == 0 {
			switchType(1)
		}
		blen[1]--
		cmd := cmdH[btype[1]].decode(b)
		cell := cmd >> 6
		ic := int(webBrotliCellIns[cell]) + (cmd>>3)&7
		cc := int(webBrotliCellCopy[cell]) + cmd&7
		ilen := int(webBrotliInsBase[ic]) + int(b.read(uint(webBrotliInsExtra[ic])))
		clen := int(webBrotliCopyBase[cc]) + int(b.read(uint(webBrotliCopyExtra[cc])))
		for i := 0; i < ilen; i++ {
			if blen[0] == 0 {
				switchType(0)
			}
			blen[0]--
			var p1, p2 int
			if n := len(out); n >= 1 {
				p1 = int(out[n-1])
				if n >= 2 {
					p2 = int(out[n-2])
				}
			}
			mode := int(cmodes[btype[0]])
			ctx := int(webBrotliContextLookup[mode*512+p1] | webBrotliContextLookup[mode*512+256+p2])
			out = append(out, byte(litH[cmapL[btype[0]*64+ctx]].decode(b)))
			remaining--
			if remaining == 0 {
				break
			}
		}
		if remaining <= 0 {
			break
		}
		dcode := 0
		if cell >= 2 {
			if blen[2] == 0 {
				switchType(2)
			}
			blen[2]--
			dctx := 3
			if clen <= 4 {
				dctx = clen - 2
			}
			dcode = distH[cmapD[btype[2]*4+dctx]].decode(b)
		}
		var dist int
		switch {
		case dcode < 16:
			dist = rb[3-webBrotliDistIdx[dcode]] + webBrotliDistDelta[dcode]
			if dist <= 0 {
				panic(webBrotliErr("bad distance"))
			}
		case dcode < 16+ndirect:
			dist = dcode - 15
		default:
			x := dcode - ndirect - 16
			ndb := uint(1 + x>>(npostfix+1))
			hcode := x >> npostfix
			lcode := x & (1<<npostfix - 1)
			offset := (2+hcode&1)<<ndb - 4
			dist = (offset+int(b.read(ndb)))<<npostfix + lcode + ndirect + 1
		}
		maxd := min(maxBack, len(out))
		if dist > maxd {
			// A word of the static dictionary, transformed.
			if clen < 4 || clen > 24 {
				panic(webBrotliErr("dictionary word length"))
			}
			wid := dist - maxd - 1
			nbits := uint(webBrotliDictSizeBits[clen])
			idx := wid & (1<<nbits - 1)
			tid := wid >> nbits
			if tid >= len(webBrotliTransforms) {
				panic(webBrotliErr("dictionary transform"))
			}
			off := int(webBrotliDictOffsets[clen]) + idx*clen
			word := webBrotliTransformWord(dict[off:off+clen], tid)
			if len(word) > remaining {
				panic(webBrotliErr("dictionary word past the block"))
			}
			out = append(out, word...)
			remaining -= len(word)
			continue
		}
		if dcode != 0 {
			rb = [4]int{rb[1], rb[2], rb[3], dist}
		}
		if clen > remaining {
			panic(webBrotliErr("copy past the block"))
		}
		st := len(out) - dist
		for k := 0; k < clen; k++ {
			out = append(out, out[st+k])
		}
		remaining -= clen
	}
	return out, rb
}
