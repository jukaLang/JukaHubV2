package main

// pdfv_doc.go — reading a PDF into pages the reader can draw.
//
// The reader used to pull text out of the content streams it could find with a
// regular expression. A PDF written by anything modern keeps its objects inside
// compressed object streams, names its fonts' characters through a ToUnicode
// map, and puts its scans and photos in JPEG image objects — so most real
// documents opened as "no extractable text" at best. This file is a small,
// dependency-free PDF reader:
//
//   - every "N G obj" is indexed, object streams are unpacked, and the
//     standard security handler with an empty user password (RC4 and AES-128,
//     the "protected, but opens without a password" PDFs banks send) is
//     undone;
//   - streams are decoded (Flate with PNG predictors, ASCIIHex, ASCII85,
//     RunLength; DCT is kept as JPEG bytes for the image decoder);
//   - fonts become code → text tables (ToUnicode CMaps, Differences, WinAnsi)
//     with their widths, so text comes out as text and lands where it was set;
//   - a page's content stream is interpreted into a display list in page
//     coordinates: text runs, filled and stroked paths, images.
//
// pdfv_view.go draws the display list into textures; nothing here touches SDL.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// --- Objects ----------------------------------------------------------------

type pdfvKind uint8

const (
	pdfvNull pdfvKind = iota
	pdfvBool
	pdfvNum
	pdfvStr
	pdfvName
	pdfvArray
	pdfvDict
	pdfvRef
	pdfvOp
)

// pdfvObj is one PDF value. Names and strings keep their bytes in s; an
// operator (a bare keyword in a content stream) is kind pdfvOp.
type pdfvObj struct {
	kind pdfvKind
	num  float64
	ref  int
	b    bool
	s    []byte
	arr  []pdfvObj
	dict map[string]pdfvObj
}

func (o pdfvObj) name() string {
	if o.kind == pdfvName || o.kind == pdfvOp {
		return string(o.s)
	}
	return ""
}

// --- Lexer --------------------------------------------------------------------

type pdfvParser struct {
	data []byte
	pos  int
}

func pdfvIsWhite(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0
}

func pdfvIsDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (p *pdfvParser) skip() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if pdfvIsWhite(c) {
			p.pos++
			continue
		}
		if c == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
			continue
		}
		return
	}
}

// next reads one token-level object: a number, a name, a string, an array, a
// dictionary or an operator. References are folded by value.
func (p *pdfvParser) next(depth int) (pdfvObj, bool) {
	p.skip()
	if p.pos >= len(p.data) || depth > 64 {
		return pdfvObj{}, false
	}
	c := p.data[p.pos]
	switch {
	case c == '/':
		p.pos++
		start := p.pos
		for p.pos < len(p.data) && !pdfvIsWhite(p.data[p.pos]) && !pdfvIsDelim(p.data[p.pos]) {
			p.pos++
		}
		return pdfvObj{kind: pdfvName, s: pdfvDecodeName(p.data[start:p.pos])}, true
	case c == '(':
		return pdfvObj{kind: pdfvStr, s: p.literal()}, true
	case c == '<':
		if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
			p.pos += 2
			d := map[string]pdfvObj{}
			for {
				p.skip()
				if p.pos >= len(p.data) {
					break
				}
				if p.data[p.pos] == '>' && p.pos+1 < len(p.data) && p.data[p.pos+1] == '>' {
					p.pos += 2
					break
				}
				key, ok := p.next(depth + 1)
				if !ok {
					break
				}
				if key.kind != pdfvName {
					continue
				}
				val, ok := p.value(depth + 1)
				if !ok {
					break
				}
				d[string(key.s)] = val
			}
			return pdfvObj{kind: pdfvDict, dict: d}, true
		}
		return pdfvObj{kind: pdfvStr, s: p.hexString()}, true
	case c == '[':
		p.pos++
		var arr []pdfvObj
		for {
			p.skip()
			if p.pos >= len(p.data) {
				break
			}
			if p.data[p.pos] == ']' {
				p.pos++
				break
			}
			v, ok := p.value(depth + 1)
			if !ok {
				break
			}
			arr = append(arr, v)
		}
		return pdfvObj{kind: pdfvArray, arr: arr}, true
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		start := p.pos
		p.pos++
		for p.pos < len(p.data) {
			d := p.data[p.pos]
			if (d >= '0' && d <= '9') || d == '.' {
				p.pos++
				continue
			}
			break
		}
		v, err := strconv.ParseFloat(string(p.data[start:p.pos]), 64)
		if err != nil {
			v = 0
		}
		return pdfvObj{kind: pdfvNum, num: v}, true
	case pdfvIsDelim(c):
		p.pos++
		return pdfvObj{kind: pdfvOp, s: []byte{c}}, true
	}
	start := p.pos
	for p.pos < len(p.data) && !pdfvIsWhite(p.data[p.pos]) && !pdfvIsDelim(p.data[p.pos]) {
		p.pos++
	}
	word := p.data[start:p.pos]
	switch string(word) {
	case "true":
		return pdfvObj{kind: pdfvBool, b: true}, true
	case "false":
		return pdfvObj{kind: pdfvBool}, true
	case "null":
		return pdfvObj{}, true
	}
	return pdfvObj{kind: pdfvOp, s: word}, true
}

// value is next with "N G R" folded into a reference.
func (p *pdfvParser) value(depth int) (pdfvObj, bool) {
	v, ok := p.next(depth)
	if !ok || v.kind != pdfvNum {
		return v, ok
	}
	save := p.pos
	if g, ok := p.next(depth); ok && g.kind == pdfvNum {
		if r, ok := p.next(depth); ok && r.kind == pdfvOp && len(r.s) == 1 && r.s[0] == 'R' {
			return pdfvObj{kind: pdfvRef, ref: int(v.num)}, true
		}
	}
	p.pos = save
	return v, true
}

// literal reads a (string) with its escapes and nested parentheses.
func (p *pdfvParser) literal() []byte {
	p.pos++ // (
	var out []byte
	depth := 1
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.data):
			n := p.data[p.pos+1]
			p.pos += 2
			switch n {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				// A backslash at the end of a line continues the string.
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			case '\n':
			default:
				if n >= '0' && n <= '7' {
					v := int(n - '0')
					for k := 0; k < 2 && p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '7'; k++ {
						v = v*8 + int(p.data[p.pos]-'0')
						p.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, n)
				}
			}
			continue
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				p.pos++
				return out
			}
		}
		out = append(out, c)
		p.pos++
	}
	return out
}

// hexString reads a <hex> string.
func (p *pdfvParser) hexString() []byte {
	p.pos++ // <
	var out []byte
	high := -1
	for p.pos < len(p.data) && p.data[p.pos] != '>' {
		v := pdfvHexVal(p.data[p.pos])
		p.pos++
		if v < 0 {
			continue
		}
		if high < 0 {
			high = v
		} else {
			out = append(out, byte(high<<4|v))
			high = -1
		}
	}
	if p.pos < len(p.data) {
		p.pos++
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return out
}

func pdfvHexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// pdfvDecodeName undoes #xx escapes in a name.
func pdfvDecodeName(raw []byte) []byte {
	if bytes.IndexByte(raw, '#') < 0 {
		return raw
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && i+2 < len(raw) {
			h, l := pdfvHexVal(raw[i+1]), pdfvHexVal(raw[i+2])
			if h >= 0 && l >= 0 {
				out = append(out, byte(h<<4|l))
				i += 2
				continue
			}
		}
		out = append(out, raw[i])
	}
	return out
}

// --- File ---------------------------------------------------------------------

// pdfvIndirect is one numbered object: its value and, for a stream, the raw
// (still encoded) bytes.
type pdfvIndirect struct {
	obj       pdfvObj
	gen       int
	stream    []byte
	hasStream bool
	inObjStm  bool
}

// pdfvFile is a parsed PDF.
type pdfvFile struct {
	objs    map[int]*pdfvIndirect
	trailer map[string]pdfvObj
	crypt   *pdfvCrypt
}

var rePDFVObjHead = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(\d+)\s+(\d+)\s+obj\b`) })

// pdfvMaxFileBytes bounds what the reader loads: the whole file stays in
// memory while it is open, and the handheld has 1 GB for everything.
const pdfvMaxFileBytes = 96 << 20

// pdfvOpenBytes indexes a PDF's objects.
func pdfvOpenBytes(data []byte) (*pdfvFile, error) {
	if len(data) < 8 {
		return nil, errors.New("the file is empty")
	}
	f := &pdfvFile{objs: map[int]*pdfvIndirect{}, trailer: map[string]pdfvObj{}}
	for _, m := range rePDFVObjHead().FindAllSubmatchIndex(data, -1) {
		num, err1 := strconv.Atoi(string(data[m[2]:m[3]]))
		gen, err2 := strconv.Atoi(string(data[m[4]:m[5]]))
		if err1 != nil || err2 != nil {
			continue
		}
		p := pdfvParser{data: data, pos: m[1]}
		obj, ok := p.value(0)
		if !ok {
			continue
		}
		ind := &pdfvIndirect{obj: obj, gen: gen}
		p.skip()
		if obj.kind == pdfvDict && bytes.HasPrefix(data[p.pos:], []byte("stream")) {
			start := p.pos + len("stream")
			if start < len(data) && data[start] == '\r' {
				start++
			}
			if start < len(data) && data[start] == '\n' {
				start++
			}
			end := -1
			if l, ok := obj.dict["Length"]; ok && l.kind == pdfvNum {
				if e := start + int(l.num); e <= len(data) && e >= start {
					rest := bytes.TrimLeft(data[e:min(len(data), e+32)], "\r\n \t")
					if bytes.HasPrefix(rest, []byte("endstream")) {
						end = e
					}
				}
			}
			if end < 0 {
				if i := bytes.Index(data[start:], []byte("endstream")); i >= 0 {
					end = start + i
					for end > start && (data[end-1] == '\n' || data[end-1] == '\r') {
						end--
					}
				} else {
					end = len(data)
				}
			}
			ind.stream = data[start:end]
			ind.hasStream = true
		}
		// A later definition (an incremental update) replaces an earlier one.
		f.objs[num] = ind
	}
	// The trailer: the last classic trailer dictionary, else the newest
	// cross-reference stream's dictionary.
	if i := bytes.LastIndex(data, []byte("trailer")); i >= 0 {
		p := pdfvParser{data: data, pos: i + len("trailer")}
		if t, ok := p.value(0); ok && t.kind == pdfvDict {
			f.trailer = t.dict
		}
	}
	if _, ok := f.trailer["Root"]; !ok {
		best := -1
		for num, ind := range f.objs {
			if ind.obj.kind == pdfvDict && ind.obj.dict["Type"].name() == "XRef" && num > best {
				best = num
				f.trailer = ind.obj.dict
			}
		}
	}
	if enc, ok := f.trailer["Encrypt"]; ok {
		crypt, err := pdfvNewCrypt(f, f.resolve(enc))
		if err != nil {
			return nil, err
		}
		f.crypt = crypt
	}
	f.expandObjectStreams()
	if len(f.objs) == 0 {
		return nil, errors.New("this is not a PDF the reader understands")
	}
	return f, nil
}

// expandObjectStreams adds the objects packed inside /Type /ObjStm streams. A
// directly written object of the same number wins: it is the newer one in an
// updated file.
func (f *pdfvFile) expandObjectStreams() {
	nums := make([]int, 0, len(f.objs))
	for num, ind := range f.objs {
		if ind.hasStream && ind.obj.dict["Type"].name() == "ObjStm" {
			nums = append(nums, num)
		}
	}
	sort.Ints(nums)
	for _, num := range nums {
		ind := f.objs[num]
		data, err := f.streamBytes(num)
		if err != nil {
			continue
		}
		n := int(f.resolve(ind.obj.dict["N"]).num)
		first := int(f.resolve(ind.obj.dict["First"]).num)
		if n <= 0 || first <= 0 || first > len(data) {
			continue
		}
		p := pdfvParser{data: data[:first]}
		for i := 0; i < n; i++ {
			on, ok1 := p.next(0)
			off, ok2 := p.next(0)
			if !ok1 || !ok2 || on.kind != pdfvNum || off.kind != pdfvNum {
				break
			}
			objNum := int(on.num)
			if old, exists := f.objs[objNum]; exists && !old.inObjStm {
				continue
			}
			q := pdfvParser{data: data, pos: first + int(off.num)}
			if obj, ok := q.value(0); ok {
				f.objs[objNum] = &pdfvIndirect{obj: obj, inObjStm: true}
			}
		}
	}
}

// resolve follows references (a few hops at most).
func (f *pdfvFile) resolve(o pdfvObj) pdfvObj {
	for i := 0; i < 8 && o.kind == pdfvRef; i++ {
		ind, ok := f.objs[o.ref]
		if !ok {
			return pdfvObj{}
		}
		o = ind.obj
	}
	return o
}

// dictOf resolves o and returns its dictionary (nil when it is not one).
func (f *pdfvFile) dictOf(o pdfvObj) map[string]pdfvObj {
	o = f.resolve(o)
	if o.kind == pdfvDict {
		return o.dict
	}
	return nil
}

func (f *pdfvFile) numOf(o pdfvObj, def float64) float64 {
	o = f.resolve(o)
	if o.kind == pdfvNum {
		return o.num
	}
	return def
}

func (f *pdfvFile) arrOf(o pdfvObj) []pdfvObj {
	o = f.resolve(o)
	if o.kind == pdfvArray {
		return o.arr
	}
	return nil
}

// --- Streams ------------------------------------------------------------------

// streamBytes decodes object num's stream through its filters. Image codecs
// (DCT, JPX, JBIG2, CCITT) are left encoded; streamFilter names the last one.
func (f *pdfvFile) streamBytes(num int) ([]byte, error) {
	ind, ok := f.objs[num]
	if !ok || !ind.hasStream {
		return nil, errors.New("not a stream")
	}
	data := ind.stream
	if f.crypt != nil && !ind.inObjStm && ind.obj.dict["Type"].name() != "XRef" {
		data = f.crypt.decrypt(data, num, ind.gen)
	}
	filters := f.arrOf(ind.obj.dict["Filter"])
	if fo := f.resolve(ind.obj.dict["Filter"]); fo.kind == pdfvName {
		filters = []pdfvObj{fo}
	}
	params := f.arrOf(ind.obj.dict["DecodeParms"])
	if po := f.resolve(ind.obj.dict["DecodeParms"]); po.kind == pdfvDict {
		params = []pdfvObj{po}
	}
	for i, filter := range filters {
		var parms map[string]pdfvObj
		if i < len(params) {
			parms = f.dictOf(params[i])
		}
		var err error
		switch filter.name() {
		case "FlateDecode", "Fl":
			data, err = pdfvInflate(data)
			if err == nil {
				data = pdfvUnpredict(data, parms, f)
			}
		case "ASCIIHexDecode", "AHx":
			data = pdfvASCIIHex(data)
		case "ASCII85Decode", "A85":
			data = pdfvASCII85(data)
		case "RunLengthDecode", "RL":
			data = pdfvRunLength(data)
		case "DCTDecode", "DCT", "JPXDecode", "JBIG2Decode", "CCITTFaxDecode", "CCF":
			return data, nil
		default:
			return nil, fmt.Errorf("unsupported filter %s", filter.name())
		}
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// streamFilter names the image codec of a stream ("" for raw pixels).
func (f *pdfvFile) streamFilter(num int) string {
	ind, ok := f.objs[num]
	if !ok {
		return ""
	}
	last := ""
	if fo := f.resolve(ind.obj.dict["Filter"]); fo.kind == pdfvName {
		last = fo.name()
	}
	for _, fo := range f.arrOf(ind.obj.dict["Filter"]) {
		last = f.resolve(fo).name()
	}
	switch last {
	case "DCTDecode", "DCT":
		return "DCT"
	case "JPXDecode":
		return "JPX"
	case "JBIG2Decode":
		return "JBIG2"
	case "CCITTFaxDecode", "CCF":
		return "CCITT"
	}
	return ""
}

func pdfvInflate(data []byte) ([]byte, error) {
	const limit = 64 << 20
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		out, rerr := io.ReadAll(io.LimitReader(zr, limit))
		zr.Close()
		// A stream cut short (a damaged file, a missing checksum) still yields
		// what was inflated before the cut.
		if len(out) > 0 || rerr == nil {
			return out, nil
		}
	}
	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	out, err := io.ReadAll(io.LimitReader(fr, limit))
	if len(out) > 0 {
		return out, nil
	}
	return nil, err
}

// pdfvUnpredict undoes a PNG predictor (/Predictor >= 10).
func pdfvUnpredict(data []byte, parms map[string]pdfvObj, f *pdfvFile) []byte {
	if parms == nil {
		return data
	}
	predictor := int(f.numOf(parms["Predictor"], 1))
	if predictor < 10 {
		return data
	}
	colors := int(f.numOf(parms["Colors"], 1))
	bpc := int(f.numOf(parms["BitsPerComponent"], 8))
	columns := int(f.numOf(parms["Columns"], 1))
	bpp := (colors*bpc + 7) / 8
	rowLen := (colors*bpc*columns + 7) / 8
	if rowLen <= 0 || bpp <= 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	prev := make([]byte, rowLen)
	for pos := 0; pos+1+rowLen <= len(data); pos += rowLen + 1 {
		kind := data[pos]
		row := append([]byte(nil), data[pos+1:pos+1+rowLen]...)
		for i := 0; i < rowLen; i++ {
			var left, up, upLeft int
			if i >= bpp {
				left = int(row[i-bpp])
				upLeft = int(prev[i-bpp])
			}
			up = int(prev[i])
			switch kind {
			case 1:
				row[i] = byte(int(row[i]) + left)
			case 2:
				row[i] = byte(int(row[i]) + up)
			case 3:
				row[i] = byte(int(row[i]) + (left+up)/2)
			case 4:
				row[i] = byte(int(row[i]) + pdfvPaeth(left, up, upLeft))
			}
		}
		out = append(out, row...)
		prev = row
	}
	return out
}

func pdfvPaeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := pdfvAbs(p-a), pdfvAbs(p-b), pdfvAbs(p-c)
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func pdfvAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func pdfvASCIIHex(data []byte) []byte {
	var out []byte
	high := -1
	for _, c := range data {
		if c == '>' {
			break
		}
		v := pdfvHexVal(c)
		if v < 0 {
			continue
		}
		if high < 0 {
			high = v
		} else {
			out = append(out, byte(high<<4|v))
			high = -1
		}
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return out
}

func pdfvASCII85(data []byte) []byte {
	var out []byte
	var group [5]byte
	n := 0
	flush := func(count int) {
		var v uint32
		for i := 0; i < 5; i++ {
			c := byte('u')
			if i < count {
				c = group[i]
			}
			v = v*85 + uint32(c-'!')
		}
		b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, b[:count-1]...)
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '~':
			i = len(data)
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
		case c >= '!' && c <= 'u':
			group[n] = c
			n++
			if n == 5 {
				flush(5)
				n = 0
			}
		}
	}
	if n > 1 {
		flush(n)
	}
	return out
}

func pdfvRunLength(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n < 128:
			end := i + n + 1
			if end > len(data) {
				end = len(data)
			}
			out = append(out, data[i:end]...)
			i = end
		case n > 128:
			if i < len(data) {
				for k := 0; k < 257-n; k++ {
					out = append(out, data[i])
				}
			}
			i++
		default:
			return out
		}
	}
	return out
}

// --- Encryption (standard handler, empty user password) ----------------------

type pdfvCrypt struct {
	key []byte
	aes bool
}

var pdfvPasswordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// errPDFVLocked is a PDF that needs a password to open.
var errPDFVLocked = errors.New("this PDF is locked with a password")

// pdfvNewCrypt computes the file key for the empty user password (PDF 1.7
// algorithm 2, revisions 2–4). AES-256 files (revision 5/6) are reported as
// locked: they need a password-based key this reader does not compute.
func pdfvNewCrypt(f *pdfvFile, enc pdfvObj) (*pdfvCrypt, error) {
	if enc.kind != pdfvDict {
		return nil, errPDFVLocked
	}
	d := enc.dict
	if d["Filter"].name() != "Standard" {
		return nil, errPDFVLocked
	}
	r := int(f.numOf(d["R"], 2))
	v := int(f.numOf(d["V"], 0))
	if r > 4 {
		return nil, errPDFVLocked
	}
	o := f.resolve(d["O"]).s
	// Some writers store P unsigned (4294967292 for -4): going through int64
	// wraps it back to the same 32 bits, a direct float conversion would not.
	pBits := int32(int64(f.numOf(d["P"], 0)))
	var id0 []byte
	if ids := f.arrOf(f.trailer["ID"]); len(ids) > 0 {
		id0 = f.resolve(ids[0]).s
	}
	length := int(f.numOf(d["Length"], 40)) / 8
	if r == 2 || length < 5 {
		length = 5
	}
	if length > 16 {
		length = 16
	}
	h := md5.New()
	h.Write(pdfvPasswordPad)
	h.Write(o)
	h.Write([]byte{byte(pBits), byte(pBits >> 8), byte(pBits >> 16), byte(pBits >> 24)})
	h.Write(id0)
	if r >= 4 {
		if em := f.resolve(d["EncryptMetadata"]); em.kind == pdfvBool && !em.b {
			h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
		}
	}
	key := h.Sum(nil)
	if r >= 3 {
		for i := 0; i < 50; i++ {
			sum := md5.Sum(key[:length])
			key = sum[:]
		}
	}
	crypt := &pdfvCrypt{key: key[:length]}
	if v >= 4 {
		if cf := f.dictOf(d["CF"]); cf != nil {
			if std := f.dictOf(cf[f.resolve(d["StmF"]).name()]); std != nil && std["CFM"].name() == "AESV2" {
				crypt.aes = true
			}
		}
	}
	return crypt, nil
}

// decrypt undoes one object's encryption.
func (c *pdfvCrypt) decrypt(data []byte, num, gen int) []byte {
	if c == nil || len(data) == 0 {
		return data
	}
	h := md5.New()
	h.Write(c.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	if c.aes {
		h.Write([]byte("sAlT"))
	}
	k := h.Sum(nil)
	n := len(c.key) + 5
	if n > 16 {
		n = 16
	}
	k = k[:n]
	if !c.aes {
		cipherRC4, err := rc4.NewCipher(k)
		if err != nil {
			return data
		}
		out := make([]byte, len(data))
		cipherRC4.XORKeyStream(out, data)
		return out
	}
	if len(data) < 32 || len(data)%16 != 0 {
		return data
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return data
	}
	out := make([]byte, len(data)-16)
	cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(out, data[16:])
	if pad := int(out[len(out)-1]); pad > 0 && pad <= 16 && pad <= len(out) {
		out = out[:len(out)-pad]
	}
	return out
}

// --- Document -------------------------------------------------------------------

// pdfvPageRef is one page of the page tree with its inherited attributes.
type pdfvPageRef struct {
	obj       map[string]pdfvObj
	resources map[string]pdfvObj
	box       [4]float64 // x0 y0 x1 y1 in points
	rotate    int
}

// pdfvPages walks the page tree in reading order.
func (f *pdfvFile) pages() []pdfvPageRef {
	var out []pdfvPageRef
	root := f.dictOf(f.trailer["Root"])
	if root == nil {
		for _, ind := range f.objs {
			if ind.obj.kind == pdfvDict && ind.obj.dict["Type"].name() == "Catalog" {
				root = ind.obj.dict
				break
			}
		}
	}
	seen := map[int]bool{}
	var walk func(node pdfvObj, res map[string]pdfvObj, box [4]float64, rot int, depth int)
	walk = func(node pdfvObj, res map[string]pdfvObj, box [4]float64, rot int, depth int) {
		if depth > 32 || len(out) > 5000 {
			return
		}
		if node.kind == pdfvRef {
			if seen[node.ref] {
				return
			}
			seen[node.ref] = true
		}
		d := f.dictOf(node)
		if d == nil {
			return
		}
		if r := f.dictOf(d["Resources"]); r != nil {
			res = r
		}
		if b := f.arrOf(d["MediaBox"]); len(b) == 4 {
			for i := range box {
				box[i] = f.numOf(b[i], box[i])
			}
		}
		if b := f.arrOf(d["CropBox"]); len(b) == 4 {
			for i := range box {
				box[i] = f.numOf(b[i], box[i])
			}
		}
		if ro := f.resolve(d["Rotate"]); ro.kind == pdfvNum {
			rot = int(ro.num)
		}
		kids := f.arrOf(d["Kids"])
		if d["Type"].name() == "Page" || (len(kids) == 0 && d["Contents"].kind != pdfvNull) {
			out = append(out, pdfvPageRef{obj: d, resources: res, box: pdfvNormBox(box), rotate: ((rot % 360) + 360) % 360})
			return
		}
		for _, kid := range kids {
			walk(kid, res, box, rot, depth+1)
		}
	}
	if root != nil {
		walk(root["Pages"], nil, [4]float64{0, 0, 612, 792}, 0, 0)
	}
	if len(out) == 0 {
		// No usable tree: every page object, in object order.
		nums := make([]int, 0, len(f.objs))
		for num, ind := range f.objs {
			if ind.obj.kind == pdfvDict && ind.obj.dict["Type"].name() == "Page" {
				nums = append(nums, num)
			}
		}
		sort.Ints(nums)
		for _, num := range nums {
			walk(pdfvObj{kind: pdfvRef, ref: num}, nil, [4]float64{0, 0, 612, 792}, 0, 0)
		}
	}
	return out
}

func pdfvNormBox(b [4]float64) [4]float64 {
	if b[0] > b[2] {
		b[0], b[2] = b[2], b[0]
	}
	if b[1] > b[3] {
		b[1], b[3] = b[3], b[1]
	}
	if b[2]-b[0] < 1 || b[3]-b[1] < 1 {
		return [4]float64{0, 0, 612, 792}
	}
	return b
}

// infoString reads a document-information string (/Title, /Author).
func (f *pdfvFile) infoString(key string) string {
	info := f.dictOf(f.trailer["Info"])
	if info == nil {
		return ""
	}
	v := f.resolve(info[key])
	if v.kind != pdfvStr {
		return ""
	}
	raw := v.s
	if f.crypt != nil {
		if ref := info[key]; ref.kind != pdfvRef {
			if num := f.infoObjectNum(); num > 0 {
				raw = f.crypt.decrypt(raw, num, f.objs[num].gen)
			}
		}
	}
	return strings.TrimSpace(pdfvTextString(raw))
}

func (f *pdfvFile) infoObjectNum() int {
	if ref := f.trailer["Info"]; ref.kind == pdfvRef {
		if _, ok := f.objs[ref.ref]; ok {
			return ref.ref
		}
	}
	return 0
}

// pdfvTextString decodes a PDF text string: UTF-16BE with a BOM, else
// PDFDocEncoding (Latin-1 for everything that matters here).
func pdfvTextString(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		u := make([]uint16, 0, len(raw)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(u))
	}
	var sb strings.Builder
	for _, b := range raw {
		if b < 0x20 && b != '\t' {
			continue
		}
		sb.WriteRune(winAnsiRune(b))
	}
	return sb.String()
}

// --- Fonts --------------------------------------------------------------------

// pdfvFont turns a font's character codes into text and widths.
type pdfvFont struct {
	twoByte   bool
	toUnicode map[uint32]string
	enc       [256]rune
	firstChar int
	widths    []float64
	cidWidths map[uint32]float64
	missing   float64
	bold      bool
	italic    bool
	mono      bool
}

// pdfvLoadFont reads one font dictionary.
func (f *pdfvFile) loadFont(d map[string]pdfvObj) *pdfvFont {
	font := &pdfvFont{missing: 500}
	if d == nil {
		for i := range font.enc {
			font.enc[i] = winAnsiRune(byte(i))
		}
		return font
	}
	base := strings.ToLower(f.resolve(d["BaseFont"]).name())
	font.bold = strings.Contains(base, "bold") || strings.Contains(base, "black") || strings.Contains(base, "heavy")
	font.italic = strings.Contains(base, "italic") || strings.Contains(base, "oblique")
	font.mono = strings.Contains(base, "courier") || strings.Contains(base, "mono")
	subtype := f.resolve(d["Subtype"]).name()
	if subtype == "Type0" {
		font.twoByte = true
		font.missing = 1000
		if desc := f.arrOf(d["DescendantFonts"]); len(desc) > 0 {
			cid := f.dictOf(desc[0])
			if cid != nil {
				font.missing = f.numOf(cid["DW"], 1000)
				font.cidWidths = f.cidWidthTable(f.arrOf(cid["W"]))
			}
		}
		if enc := f.resolve(d["Encoding"]); enc.kind == pdfvName {
			n := enc.name()
			if !strings.HasPrefix(n, "Identity") && !strings.HasSuffix(n, "UCS2-H") && !strings.HasSuffix(n, "UTF16-H") {
				// Other predefined CMaps (Shift-JIS and friends) mix one- and
				// two-byte codes; the ToUnicode map below is the best guide.
				font.twoByte = true
			}
		}
	} else {
		for i := range font.enc {
			font.enc[i] = winAnsiRune(byte(i))
		}
		enc := f.resolve(d["Encoding"])
		switch enc.kind {
		case pdfvName:
			font.applyBaseEncoding(enc.name())
		case pdfvDict:
			font.applyBaseEncoding(f.resolve(enc.dict["BaseEncoding"]).name())
			code := 0
			for _, item := range f.arrOf(enc.dict["Differences"]) {
				item = f.resolve(item)
				switch item.kind {
				case pdfvNum:
					code = int(item.num)
				case pdfvName:
					if code >= 0 && code < 256 {
						if r, ok := pdfvGlyphRune(item.name()); ok {
							font.enc[code] = r
						}
					}
					code++
				}
			}
		}
		font.firstChar = int(f.numOf(d["FirstChar"], 0))
		for _, w := range f.arrOf(d["Widths"]) {
			font.widths = append(font.widths, f.numOf(w, 0))
		}
		if fd := f.dictOf(d["FontDescriptor"]); fd != nil {
			if mw := f.numOf(fd["MissingWidth"], 0); mw > 0 {
				font.missing = mw
			}
			flags := int(f.numOf(fd["Flags"], 0))
			if flags&1 != 0 {
				font.mono = true
			}
			if flags&(1<<6) != 0 {
				font.italic = true
			}
		}
		if font.mono {
			font.missing = 600
		}
	}
	if tu := f.resolve(d["ToUnicode"]); tu.kind == pdfvRef || tu.kind == pdfvDict {
		if num := d["ToUnicode"].ref; d["ToUnicode"].kind == pdfvRef {
			if data, err := f.streamBytes(num); err == nil {
				font.toUnicode = pdfvParseCMap(data)
			}
		}
	}
	return font
}

func (font *pdfvFont) applyBaseEncoding(name string) {
	switch name {
	case "MacRomanEncoding":
		for code, r := range pdfvMacRomanHigh {
			font.enc[code] = r
		}
	case "StandardEncoding":
		font.enc['\''] = '’'
		font.enc['`'] = '‘'
	}
}

// cidWidthTable reads a CID font's /W array.
func (f *pdfvFile) cidWidthTable(w []pdfvObj) map[uint32]float64 {
	out := map[uint32]float64{}
	for i := 0; i < len(w); {
		first := f.resolve(w[i])
		if first.kind != pdfvNum || i+1 >= len(w) {
			break
		}
		second := f.resolve(w[i+1])
		if second.kind == pdfvArray {
			for k, v := range second.arr {
				if len(out) > 65536 {
					return out
				}
				out[uint32(first.num)+uint32(k)] = f.numOf(v, 0)
			}
			i += 2
			continue
		}
		if i+2 >= len(w) {
			break
		}
		last := uint32(f.numOf(second, first.num))
		width := f.numOf(w[i+2], 0)
		for c := uint32(first.num); c <= last && len(out) <= 65536; c++ {
			out[c] = width
		}
		i += 3
	}
	return out
}

// decode turns a shown string into text, one entry per code, and the codes'
// widths in thousandths of the font size.
func (font *pdfvFont) decode(raw []byte, emit func(text string, width float64, space bool)) {
	step := 1
	if font.twoByte {
		step = 2
	}
	for i := 0; i+step <= len(raw); i += step {
		var code uint32
		if step == 2 {
			code = uint32(raw[i])<<8 | uint32(raw[i+1])
		} else {
			code = uint32(raw[i])
		}
		text := ""
		if s, ok := font.toUnicode[code]; ok {
			text = s
		} else if step == 1 {
			if r := font.enc[code]; r != 0 && r >= 0x20 {
				text = string(r)
			}
		}
		w := font.missing
		if step == 1 {
			if idx := int(code) - font.firstChar; idx >= 0 && idx < len(font.widths) && font.widths[idx] > 0 {
				w = font.widths[idx]
			}
		} else if cw, ok := font.cidWidths[code]; ok {
			w = cw
		}
		emit(text, w, step == 1 && code == 32)
	}
}

// pdfvParseCMap reads the bfchar and bfrange entries of a ToUnicode CMap.
func pdfvParseCMap(data []byte) map[uint32]string {
	out := map[uint32]string{}
	p := pdfvParser{data: data}
	var stack []pdfvObj
	codeOf := func(o pdfvObj) (uint32, bool) {
		if o.kind != pdfvStr || len(o.s) == 0 || len(o.s) > 4 {
			return 0, false
		}
		var v uint32
		for _, b := range o.s {
			v = v<<8 | uint32(b)
		}
		return v, true
	}
	for len(out) < 1<<17 {
		o, ok := p.next(0)
		if !ok {
			break
		}
		if o.kind != pdfvOp {
			stack = append(stack, o)
			continue
		}
		switch o.name() {
		case "endbfchar":
			for i := 0; i+1 < len(stack); i += 2 {
				if code, ok := codeOf(stack[i]); ok && stack[i+1].kind == pdfvStr {
					out[code] = pdfvUTF16(stack[i+1].s)
				}
			}
		case "endbfrange":
			for i := 0; i+2 < len(stack); i += 3 {
				lo, ok1 := codeOf(stack[i])
				hi, ok2 := codeOf(stack[i+1])
				if !ok1 || !ok2 || hi < lo || hi-lo > 65535 {
					continue
				}
				dst := stack[i+2]
				switch dst.kind {
				case pdfvStr:
					base := []rune(pdfvUTF16(dst.s))
					if len(base) == 0 {
						continue
					}
					for c := lo; c <= hi; c++ {
						r := append([]rune(nil), base...)
						r[len(r)-1] += rune(c - lo)
						out[c] = string(r)
					}
				case pdfvArray:
					for k, item := range dst.arr {
						if c := lo + uint32(k); c <= hi && item.kind == pdfvStr {
							out[c] = pdfvUTF16(item.s)
						}
					}
				}
			}
		}
		stack = stack[:0]
	}
	return out
}

// pdfvUTF16 decodes UTF-16BE bytes.
func pdfvUTF16(b []byte) string {
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}

// pdfvGlyphRune maps a glyph name from a /Differences array to its character.
func pdfvGlyphRune(name string) (rune, bool) {
	if r, ok := pdfvGlyphNames[name]; ok {
		return r, true
	}
	if len(name) == 1 {
		return rune(name[0]), true
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil {
			return rune(v), true
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return rune(v), true
		}
	}
	// "a.sc", "one.oldstyle": the base name is the character.
	if i := strings.IndexByte(name, '.'); i > 0 {
		return pdfvGlyphRune(name[:i])
	}
	return 0, false
}

// pdfvGlyphNames is the part of the Adobe Glyph List documents actually use.
var pdfvGlyphNames = map[string]rune{
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#', "dollar": '$', "percent": '%',
	"ampersand": '&', "quotesingle": '\'', "parenleft": '(', "parenright": ')', "asterisk": '*',
	"plus": '+', "comma": ',', "hyphen": '-', "period": '.', "slash": '/', "zero": '0', "one": '1',
	"two": '2', "three": '3', "four": '4', "five": '5', "six": '6', "seven": '7', "eight": '8',
	"nine": '9', "colon": ':', "semicolon": ';', "less": '<', "equal": '=', "greater": '>',
	"question": '?', "at": '@', "bracketleft": '[', "backslash": '\\', "bracketright": ']',
	"asciicircum": '^', "underscore": '_', "grave": '`', "braceleft": '{', "bar": '|',
	"braceright": '}', "asciitilde": '~', "quoteleft": '‘', "quoteright": '’', "quotedblleft": '“',
	"quotedblright": '”', "quotesinglbase": '‚', "quotedblbase": '„', "endash": '–', "emdash": '—',
	"bullet": '•', "ellipsis": '…', "dagger": '†', "daggerdbl": '‡', "fi": 'ﬁ', "fl": 'ﬂ',
	"ff": 'ﬀ', "ffi": 'ﬃ', "ffl": 'ﬄ', "minus": '−', "multiply": '×', "divide": '÷',
	"degree": '°', "copyright": '©', "registered": '®', "trademark": '™', "section": '§',
	"paragraph": '¶', "periodcentered": '·', "guillemotleft": '«', "guillemotright": '»',
	"guilsinglleft": '‹', "guilsinglright": '›', "Euro": '€', "sterling": '£', "yen": '¥',
	"cent": '¢', "nbspace": ' ', "uni00A0": ' ', "dotlessi": 'ı', "germandbls": 'ß', "ae": 'æ',
	"AE": 'Æ', "oe": 'œ', "OE": 'Œ', "oslash": 'ø', "Oslash": 'Ø', "aring": 'å', "Aring": 'Å',
	"ccedilla": 'ç', "Ccedilla": 'Ç', "eacute": 'é', "Eacute": 'É', "egrave": 'è', "Egrave": 'È',
	"ecircumflex": 'ê', "edieresis": 'ë', "aacute": 'á', "agrave": 'à', "acircumflex": 'â',
	"adieresis": 'ä', "Adieresis": 'Ä', "atilde": 'ã', "iacute": 'í', "igrave": 'ì', "icircumflex": 'î',
	"idieresis": 'ï', "ntilde": 'ñ', "Ntilde": 'Ñ', "oacute": 'ó', "ograve": 'ò', "ocircumflex": 'ô',
	"odieresis": 'ö', "Odieresis": 'Ö', "otilde": 'õ', "uacute": 'ú', "ugrave": 'ù', "ucircumflex": 'û',
	"udieresis": 'ü', "Udieresis": 'Ü', "yacute": 'ý', "ydieresis": 'ÿ', "scaron": 'š', "Scaron": 'Š',
	"zcaron": 'ž', "Zcaron": 'Ž', "exclamdown": '¡', "questiondown": '¿', "mu": 'µ',
	"plusminus": '±', "onehalf": '½', "onequarter": '¼', "threequarters": '¾', "arrowright": '→',
	"arrowleft": '←', "checkmark": '✓', "circlecopyrt": '©',
}

// pdfvMacRomanHigh is MacRoman's upper half where it differs from WinAnsi.
var pdfvMacRomanHigh = map[int]rune{
	0x80: 'Ä', 0x81: 'Å', 0x82: 'Ç', 0x83: 'É', 0x84: 'Ñ', 0x85: 'Ö', 0x86: 'Ü', 0x87: 'á',
	0x88: 'à', 0x89: 'â', 0x8A: 'ä', 0x8B: 'ã', 0x8C: 'å', 0x8D: 'ç', 0x8E: 'é', 0x8F: 'è',
	0x90: 'ê', 0x91: 'ë', 0x92: 'í', 0x93: 'ì', 0x94: 'î', 0x95: 'ï', 0x96: 'ñ', 0x97: 'ó',
	0x98: 'ò', 0x99: 'ô', 0x9A: 'ö', 0x9B: 'õ', 0x9C: 'ú', 0x9D: 'ù', 0x9E: 'û', 0x9F: 'ü',
	0xA0: '†', 0xA5: '•', 0xA7: 'ß', 0xA8: '®', 0xA9: '©', 0xAA: '™', 0xD0: '–', 0xD1: '—',
	0xD2: '“', 0xD3: '”', 0xD4: '‘', 0xD5: '’', 0xC9: '…', 0xCA: ' ',
}

// --- Display list --------------------------------------------------------------

type pdfvItemKind uint8

const (
	pdfvItemText pdfvItemKind = iota
	pdfvItemFill
	pdfvItemStroke
	pdfvItemImage
)

// pdfvPt is a point in page space (points, y up from the box's bottom edge).
type pdfvPt struct{ x, y float64 }

// pdfvItem is one thing to draw. Text sits on its baseline at (x, y) with an
// advance w; a fill or stroke carries its subpaths; an image covers x..x+w,
// y..y+h (y is the bottom edge).
type pdfvItem struct {
	kind   pdfvItemKind
	x, y   float64
	w, h   float64
	size   float64
	text   string
	bold   bool
	italic bool
	color  [3]uint8
	width  float64
	paths  [][]pdfvPt
	image  int
}

// pdfvImageRef is an image the page draws, by its object number.
type pdfvImageRef struct {
	num    int
	inline bool
}

// pdfvPageContent is a page ready to draw.
type pdfvPageContent struct {
	box    [4]float64
	rotate int
	items  []pdfvItem
	images []pdfvImageRef
	// text is the page's reading-order text, for the text view and search.
	text string
}

// pdfvMatrix is a PDF transformation [a b c d e f].
type pdfvMatrix [6]float64

var pdfvIdentity = pdfvMatrix{1, 0, 0, 1, 0, 0}

// mul is m × n (m applied first).
func (m pdfvMatrix) mul(n pdfvMatrix) pdfvMatrix {
	return pdfvMatrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func (m pdfvMatrix) apply(x, y float64) (float64, float64) {
	return x*m[0] + y*m[2] + m[4], x*m[1] + y*m[3] + m[5]
}

// scale is how much the matrix stretches the vertical unit vector.
func (m pdfvMatrix) scale() float64 {
	return math.Hypot(m[2], m[3])
}

type pdfvGState struct {
	ctm       pdfvMatrix
	fill      [3]uint8
	stroke    [3]uint8
	lineWidth float64
	font      *pdfvFont
	fontSize  float64
	charSpace float64
	wordSpace float64
	hScale    float64
	leading   float64
	rise      float64
	render    int
}

// pdfvInterp interprets content streams into a page's display list.
type pdfvInterp struct {
	file   *pdfvFile
	fonts  map[string]*pdfvFont
	page   *pdfvPageContent
	gs     pdfvGState
	stack  []pdfvGState
	tm     pdfvMatrix
	tlm    pdfvMatrix
	path   [][]pdfvPt
	cur    []pdfvPt
	run    *pdfvItem
	depth  int
	budget int
}

// pdfvMaxItems bounds a page's display list: a map with a million path
// segments would take minutes to draw on the handheld.
const pdfvMaxItems = 60000

// pdfvLoadPage interprets page i.
func (f *pdfvFile) loadPage(ref pdfvPageRef) *pdfvPageContent {
	page := &pdfvPageContent{box: ref.box, rotate: ref.rotate}
	in := &pdfvInterp{file: f, fonts: map[string]*pdfvFont{}, page: page, budget: pdfvMaxItems}
	in.gs = pdfvGState{ctm: pdfvIdentity, lineWidth: 1, hScale: 1}
	var content []byte
	contents := f.resolve(ref.obj["Contents"])
	switch contents.kind {
	case pdfvArray:
		for _, part := range contents.arr {
			if part.kind == pdfvRef {
				if data, err := f.streamBytes(part.ref); err == nil {
					content = append(content, data...)
					content = append(content, '\n')
				}
			}
		}
	default:
		if ref.obj["Contents"].kind == pdfvRef {
			if data, err := f.streamBytes(ref.obj["Contents"].ref); err == nil {
				content = data
			}
		}
	}
	in.run = nil
	in.exec(content, ref.resources)
	in.flushRun()
	page.text = pdfvPageText(page.items)
	return page
}

// exec runs one content stream with its resources.
func (in *pdfvInterp) exec(content []byte, res map[string]pdfvObj) {
	if in.depth > 6 || len(content) == 0 {
		return
	}
	in.depth++
	defer func() { in.depth-- }()
	p := pdfvParser{data: content}
	var ops []pdfvObj
	num := func(i int) float64 {
		if i < len(ops) && ops[i].kind == pdfvNum {
			return ops[i].num
		}
		return 0
	}
	for in.budget > 0 {
		o, ok := p.next(0)
		if !ok {
			break
		}
		if o.kind != pdfvOp {
			if len(ops) < 64 {
				ops = append(ops, o)
			}
			continue
		}
		op := o.name()
		switch op {
		case "q":
			if len(in.stack) < 64 {
				in.stack = append(in.stack, in.gs)
			}
		case "Q":
			if n := len(in.stack); n > 0 {
				in.gs = in.stack[n-1]
				in.stack = in.stack[:n-1]
			}
		case "cm":
			if len(ops) >= 6 {
				m := pdfvMatrix{num(0), num(1), num(2), num(3), num(4), num(5)}
				in.gs.ctm = m.mul(in.gs.ctm)
			}
		case "w":
			in.gs.lineWidth = num(0)
		case "g":
			in.gs.fill = pdfvGray(num(0))
		case "G":
			in.gs.stroke = pdfvGray(num(0))
		case "rg":
			in.gs.fill = pdfvRGB(num(0), num(1), num(2))
		case "RG":
			in.gs.stroke = pdfvRGB(num(0), num(1), num(2))
		case "k":
			in.gs.fill = pdfvCMYK(num(0), num(1), num(2), num(3))
		case "K":
			in.gs.stroke = pdfvCMYK(num(0), num(1), num(2), num(3))
		case "sc", "scn", "SC", "SCN":
			var c [3]uint8
			nums := 0
			for _, v := range ops {
				if v.kind == pdfvNum {
					nums++
				}
			}
			switch {
			case nums >= 4:
				c = pdfvCMYK(num(0), num(1), num(2), num(3))
			case nums == 3:
				c = pdfvRGB(num(0), num(1), num(2))
			case nums >= 1:
				c = pdfvGray(num(0))
			default:
				// A pattern: drawn as mid grey rather than not at all.
				c = [3]uint8{150, 150, 150}
			}
			if op == "sc" || op == "scn" {
				in.gs.fill = c
			} else {
				in.gs.stroke = c
			}
		case "cs":
			// A new colour space resets the colour to black (or its default).
			in.gs.fill = [3]uint8{}
		case "CS":
			in.gs.stroke = [3]uint8{}
		case "m":
			in.closeSub(false)
			in.cur = []pdfvPt{in.pt(num(0), num(1))}
		case "l":
			in.cur = append(in.cur, in.pt(num(0), num(1)))
		case "c":
			in.curve(in.pt(num(0), num(1)), in.pt(num(2), num(3)), in.pt(num(4), num(5)))
		case "v":
			if n := len(in.cur); n > 0 {
				in.curve(in.cur[n-1], in.pt(num(0), num(1)), in.pt(num(2), num(3)))
			}
		case "y":
			p3 := in.pt(num(2), num(3))
			in.curve(in.pt(num(0), num(1)), p3, p3)
		case "h":
			in.closeSub(true)
		case "re":
			in.closeSub(false)
			x, y, w, h := num(0), num(1), num(2), num(3)
			in.path = append(in.path, []pdfvPt{in.pt(x, y), in.pt(x+w, y), in.pt(x+w, y+h), in.pt(x, y+h), in.pt(x, y)})
		case "f", "F", "f*":
			in.paint(true, false)
		case "B", "B*", "b", "b*":
			if op == "b" || op == "b*" {
				in.closeSub(true)
			}
			in.paint(true, true)
		case "S":
			in.paint(false, true)
		case "s":
			in.closeSub(true)
			in.paint(false, true)
		case "n":
			in.closeSub(false)
			in.path = nil
		case "BT":
			in.tm, in.tlm = pdfvIdentity, pdfvIdentity
		case "ET":
			in.flushRun()
		case "Tf":
			if len(ops) >= 2 && ops[0].kind == pdfvName {
				in.gs.font = in.font(ops[0].name(), res)
				in.gs.fontSize = ops[1].num
			}
		case "Tc":
			in.gs.charSpace = num(0)
		case "Tw":
			in.gs.wordSpace = num(0)
		case "Tz":
			in.gs.hScale = num(0) / 100
		case "TL":
			in.gs.leading = num(0)
		case "Ts":
			in.gs.rise = num(0)
		case "Tr":
			in.gs.render = int(num(0))
		case "Td":
			in.tlm = pdfvMatrix{1, 0, 0, 1, num(0), num(1)}.mul(in.tlm)
			in.tm = in.tlm
		case "TD":
			in.gs.leading = -num(1)
			in.tlm = pdfvMatrix{1, 0, 0, 1, num(0), num(1)}.mul(in.tlm)
			in.tm = in.tlm
		case "Tm":
			if len(ops) >= 6 {
				in.tlm = pdfvMatrix{num(0), num(1), num(2), num(3), num(4), num(5)}
				in.tm = in.tlm
			}
		case "T*":
			in.nextLine()
		case "Tj":
			if len(ops) > 0 && ops[len(ops)-1].kind == pdfvStr {
				in.show(ops[len(ops)-1].s)
			}
		case "'":
			in.nextLine()
			if len(ops) > 0 && ops[len(ops)-1].kind == pdfvStr {
				in.show(ops[len(ops)-1].s)
			}
		case "\"":
			if len(ops) >= 3 {
				in.gs.wordSpace = num(0)
				in.gs.charSpace = num(1)
			}
			in.nextLine()
			if len(ops) > 0 && ops[len(ops)-1].kind == pdfvStr {
				in.show(ops[len(ops)-1].s)
			}
		case "TJ":
			if len(ops) > 0 && ops[len(ops)-1].kind == pdfvArray {
				for _, part := range ops[len(ops)-1].arr {
					switch part.kind {
					case pdfvStr:
						in.show(part.s)
					case pdfvNum:
						tx := -part.num / 1000 * in.gs.fontSize * in.gs.hScale
						in.tm = pdfvMatrix{1, 0, 0, 1, tx, 0}.mul(in.tm)
					}
				}
			}
		case "Do":
			if len(ops) > 0 && ops[0].kind == pdfvName {
				in.xobject(ops[0].name(), res)
			}
		case "BI":
			// An inline image: skipped (its data runs to EI).
			if i := bytes.Index(content[p.pos:], []byte("EI")); i >= 0 {
				p.pos += i + 2
			}
		}
		ops = ops[:0]
	}
}

func pdfvGray(v float64) [3]uint8 {
	g := pdfvByte(v)
	return [3]uint8{g, g, g}
}

func pdfvRGB(r, g, b float64) [3]uint8 {
	return [3]uint8{pdfvByte(r), pdfvByte(g), pdfvByte(b)}
}

func pdfvCMYK(c, m, y, k float64) [3]uint8 {
	return [3]uint8{pdfvByte((1 - c) * (1 - k)), pdfvByte((1 - m) * (1 - k)), pdfvByte((1 - y) * (1 - k))}
}

func pdfvByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// pt maps user space to page space through the CTM.
func (in *pdfvInterp) pt(x, y float64) pdfvPt {
	px, py := in.gs.ctm.apply(x, y)
	return pdfvPt{px, py}
}

func (in *pdfvInterp) curve(c1, c2, p3 pdfvPt) {
	n := len(in.cur)
	if n == 0 {
		in.cur = append(in.cur, p3)
		return
	}
	p0 := in.cur[n-1]
	for i := 1; i <= 8; i++ {
		t := float64(i) / 8
		u := 1 - t
		x := u*u*u*p0.x + 3*u*u*t*c1.x + 3*u*t*t*c2.x + t*t*t*p3.x
		y := u*u*u*p0.y + 3*u*u*t*c1.y + 3*u*t*t*c2.y + t*t*t*p3.y
		in.cur = append(in.cur, pdfvPt{x, y})
	}
}

func (in *pdfvInterp) closeSub(close bool) {
	if len(in.cur) == 0 {
		return
	}
	if close && len(in.cur) > 1 {
		in.cur = append(in.cur, in.cur[0])
	}
	if len(in.cur) > 1 {
		in.path = append(in.path, in.cur)
	}
	in.cur = nil
}

func (in *pdfvInterp) paint(fill, stroke bool) {
	in.closeSub(false)
	if len(in.path) == 0 {
		return
	}
	if fill {
		in.add(pdfvItem{kind: pdfvItemFill, color: in.gs.fill, paths: in.path})
	}
	if stroke {
		w := in.gs.lineWidth * in.gs.ctm.scale()
		in.add(pdfvItem{kind: pdfvItemStroke, color: in.gs.stroke, paths: in.path, width: w})
	}
	in.path = nil
}

func (in *pdfvInterp) add(item pdfvItem) {
	if in.budget <= 0 {
		return
	}
	in.budget--
	in.page.items = append(in.page.items, item)
}

func (in *pdfvInterp) nextLine() {
	in.tlm = pdfvMatrix{1, 0, 0, 1, 0, -in.gs.leading}.mul(in.tlm)
	in.tm = in.tlm
}

// font finds a resource font by name, loading it once per page.
func (in *pdfvInterp) font(name string, res map[string]pdfvObj) *pdfvFont {
	if font, ok := in.fonts[name]; ok {
		return font
	}
	var d map[string]pdfvObj
	if fonts := in.file.dictOf(res["Font"]); fonts != nil {
		d = in.file.dictOf(fonts[name])
	}
	font := in.file.loadFont(d)
	in.fonts[name] = font
	return font
}

// show draws one string with the current text state, joining it to the run in
// progress when it continues the same line.
func (in *pdfvInterp) show(raw []byte) {
	font := in.gs.font
	if font == nil {
		font = in.file.loadFont(nil)
		in.gs.font = font
	}
	size := in.gs.fontSize
	th := in.gs.hScale
	if th == 0 {
		th = 1
	}
	font.decode(raw, func(text string, width float64, space bool) {
		trm := pdfvMatrix{size * th, 0, 0, size, 0, in.gs.rise}.mul(in.tm).mul(in.gs.ctm)
		x, y := trm[4], trm[5]
		tx := (width/1000*size + in.gs.charSpace) * th
		if space {
			tx += in.gs.wordSpace * th
		}
		next := pdfvMatrix{1, 0, 0, 1, tx, 0}.mul(in.tm)
		if in.gs.render != 3 && in.gs.render != 7 {
			end := next.mul(in.gs.ctm)
			in.appendGlyph(text, x, y, end[4], trm.scale(), font)
		}
		in.tm = next
	})
}

// appendGlyph adds one character to the current run or starts a new run.
func (in *pdfvInterp) appendGlyph(text string, x, y, endX, size float64, font *pdfvFont) {
	if size < 0.5 {
		return
	}
	if r := in.run; r != nil {
		sameLine := math.Abs(r.y-y) < size*0.3 && math.Abs(r.size-size) < size*0.2 && r.bold == font.bold
		gap := x - (r.x + r.w)
		if sameLine && gap > -size*0.6 && gap < size*1.2 {
			if gap > size*0.22 && !strings.HasSuffix(r.text, " ") && text != " " {
				r.text += " "
			}
			r.text += text
			if endX > r.x+r.w {
				r.w = endX - r.x
			}
			return
		}
		in.flushRun()
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	in.run = &pdfvItem{kind: pdfvItemText, x: x, y: y, w: endX - x, size: size, text: text,
		bold: font.bold, italic: font.italic, color: in.gs.fill}
}

func (in *pdfvInterp) flushRun() {
	if in.run == nil {
		return
	}
	run := *in.run
	in.run = nil
	run.text = strings.TrimRight(run.text, " ")
	if run.text == "" || !utf8.ValidString(run.text) {
		return
	}
	in.add(run)
}

// xobject draws a named XObject: an image as an image item, a form by running
// its content with its own resources and matrix.
func (in *pdfvInterp) xobject(name string, res map[string]pdfvObj) {
	f := in.file
	xobjs := f.dictOf(res["XObject"])
	if xobjs == nil {
		return
	}
	ref := xobjs[name]
	if ref.kind != pdfvRef {
		return
	}
	ind, ok := f.objs[ref.ref]
	if !ok || ind.obj.kind != pdfvDict {
		return
	}
	d := ind.obj.dict
	switch f.resolve(d["Subtype"]).name() {
	case "Image":
		in.flushRun()
		corners := [4]pdfvPt{in.pt(0, 0), in.pt(1, 0), in.pt(0, 1), in.pt(1, 1)}
		x0, y0, x1, y1 := corners[0].x, corners[0].y, corners[0].x, corners[0].y
		for _, c := range corners[1:] {
			x0, y0 = math.Min(x0, c.x), math.Min(y0, c.y)
			x1, y1 = math.Max(x1, c.x), math.Max(y1, c.y)
		}
		idx := -1
		for i, img := range in.page.images {
			if img.num == ref.ref {
				idx = i
			}
		}
		if idx < 0 {
			in.page.images = append(in.page.images, pdfvImageRef{num: ref.ref})
			idx = len(in.page.images) - 1
		}
		in.add(pdfvItem{kind: pdfvItemImage, x: x0, y: y0, w: x1 - x0, h: y1 - y0, image: idx})
	case "Form":
		data, err := f.streamBytes(ref.ref)
		if err != nil {
			return
		}
		saved := in.gs
		if m := f.arrOf(d["Matrix"]); len(m) == 6 {
			var mm pdfvMatrix
			for i := range mm {
				mm[i] = f.numOf(m[i], 0)
			}
			in.gs.ctm = mm.mul(in.gs.ctm)
		}
		formRes := f.dictOf(d["Resources"])
		if formRes == nil {
			formRes = res
		}
		savedTM, savedTLM := in.tm, in.tlm
		in.exec(data, formRes)
		in.flushRun()
		in.gs = saved
		in.tm, in.tlm = savedTM, savedTLM
	}
}

// pdfvPageText is a page's text in reading order: runs sorted top to bottom,
// then left to right, with lines joined by newlines and columns kept apart.
func pdfvPageText(items []pdfvItem) string {
	var runs []pdfvItem
	for _, it := range items {
		if it.kind == pdfvItemText {
			runs = append(runs, it)
		}
	}
	if len(runs) == 0 {
		return ""
	}
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		if math.Abs(a.y-b.y) > math.Min(a.size, b.size)*0.5 {
			return a.y > b.y
		}
		return a.x < b.x
	})
	var sb strings.Builder
	lastY := runs[0].y
	lastSize := runs[0].size
	lastEnd := runs[0].x
	for i, r := range runs {
		if i > 0 {
			switch {
			case math.Abs(r.y-lastY) > math.Min(r.size, lastSize)*0.5:
				if lastY-r.y > lastSize*2.2 {
					// A gap of more than a line is a paragraph break.
					sb.WriteString("\n\n")
				} else {
					sb.WriteByte('\n')
				}
			case r.x-lastEnd > r.size*0.2:
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(r.text)
		lastY, lastSize, lastEnd = r.y, r.size, r.x+r.w
	}
	return strings.TrimSpace(sb.String())
}

// pdfvExtract reads every page's text: what the reflowing text view shows and
// what the old extractor's callers expect.
func pdfvExtract(data []byte) ([]string, string, string, error) {
	f, err := pdfvOpenBytes(data)
	if err != nil {
		return nil, "", "", err
	}
	refs := f.pages()
	texts := make([]string, 0, len(refs))
	for _, ref := range refs {
		texts = append(texts, f.loadPage(ref).text)
	}
	return texts, f.infoString("Title"), f.infoString("Author"), nil
}
