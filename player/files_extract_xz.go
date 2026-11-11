package main

// files_extract_xz.go — the .xz container, read for the Files extraction.
//
// github.com/ulikunitz/xz's own Reader takes each block's LZMA2 dictionary
// size from the block header and allocates it at once, and a header can ask
// for 4 GiB: on a 1 GB handheld that ends the launcher, and no recover() can
// catch it. So the container — stream header, block headers, padding, checks,
// index, footer — is read here, every block's dictionary is checked before
// anything is allocated, and only a block's LZMA2 data is handed to the
// library's lzma.Reader2, with the dictionary size this reader chose. The
// checks (CRC32, CRC64, SHA-256), the header CRCs and the index are verified,
// as `xz -t` does. Like the library, it reads LZMA2 as a block's only filter;
// an .xz made with BCJ or delta in front is refused with that reason.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"

	"github.com/ulikunitz/xz/lzma"
)

// feXzCRC64 is xz's CRC64 (ECMA-182, reflected), as the library uses it.
var feXzCRC64 = crc64.MakeTable(crc64.ECMA)

const (
	// feXzMaxBlocks bounds the blocks one stream may have (each costs a
	// record for the index check).
	feXzMaxBlocks = 1 << 20
	// feXzReadStep bounds the output one call asks a block's decoder for, so
	// a cancel is seen between steps however slowly a hostile block decodes.
	feXzReadStep = 64 << 10
)

var (
	errFeXzFormat = fmt.Errorf("%w (xz: not a well-formed stream)", errBadToolArchive)
	errFeXzShort  = fmt.Errorf("%w (xz: it ends too early)", errBadToolArchive)
	errFeXzCheck  = fmt.Errorf("%w (xz: the data does not match its check)", errBadToolArchive)
)

// feXzUnsupportedErr is an .xz this reader will not unpack for a reason worth
// a sentence of its own. It counts as a damaged archive to the readers above
// it, which pass such errors on untouched.
type feXzUnsupportedErr struct {
	what string
}

func (e feXzUnsupportedErr) Error() string { return e.what }

func (e feXzUnsupportedErr) Is(target error) bool { return target == errBadToolArchive }

// feXzCounter counts the bytes read through it: a block's compressed size is
// what lzma.Reader2 took between its header and its padding.
type feXzCounter struct {
	r io.Reader
	n int64
}

func (c *feXzCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// feXzRecord is one block as the index must list it.
type feXzRecord struct {
	unpadded, uncompressed int64
}

// feXzReader decompresses an .xz file: one stream or several, with padding
// between them.
type feXzReader struct {
	ctx     context.Context
	in      feXzCounter
	limit   int64
	streams int
	open    bool // inside a stream (its header read, its index not yet)
	done    bool
	flags   [2]byte
	check   int
	records []feXzRecord
	err     error

	// Dictionary bytes allocated so far, against what came out: a file of
	// many empty blocks that each ask for a large dictionary is refused.
	allocated int64
	produced  int64

	// The block being decoded.
	block    *lzma.Reader2
	sum      hash.Hash
	hdrLen   int64
	start    int64
	unc      int64
	wantComp int64
	wantUnc  int64
}

// newFeXzReader reads src as .xz, refusing a block whose LZMA2 dictionary is
// larger than limit. ctx stops it between blocks and between steps.
func newFeXzReader(ctx context.Context, src io.Reader, limit int64) *feXzReader {
	if ctx == nil {
		ctx = context.Background()
	}
	return &feXzReader{ctx: ctx, in: feXzCounter{r: src}, limit: limit}
}

// feXzBad is err as a damaged archive.
func feXzBad(err error) error {
	switch {
	case errors.Is(err, errBadToolArchive):
		return err
	case err == io.EOF, errors.Is(err, io.ErrUnexpectedEOF):
		return errFeXzShort
	}
	return fmt.Errorf("%w (xz: %v)", errBadToolArchive, err)
}

func (g *feXzReader) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > feXzReadStep {
		p = p[:feXzReadStep]
	}
	for {
		if err := g.ctx.Err(); err != nil {
			g.err = err
			return 0, err
		}
		if g.block != nil {
			n, err := g.block.Read(p)
			if n > 0 {
				if g.sum != nil {
					g.sum.Write(p[:n])
				}
				g.unc += int64(n)
				g.produced += int64(n)
				if g.wantUnc >= 0 && g.unc > g.wantUnc {
					g.err = errFeXzFormat
					return n, g.err
				}
			}
			switch {
			case err == io.EOF:
				g.block = nil
				if berr := g.endBlock(); berr != nil {
					g.err = berr
					return n, berr
				}
				if n > 0 {
					return n, nil
				}
				continue
			case err != nil:
				g.err = feXzBad(err)
				return n, g.err
			}
			return n, nil
		}
		if g.done {
			return 0, io.EOF
		}
		if err := g.advance(); err != nil {
			g.err = err
			return 0, err
		}
	}
}

// advance reads up to the next block's data, or through an index and footer
// to the next stream or the end.
func (g *feXzReader) advance() error {
	if !g.open {
		return g.streamHeader()
	}
	var first [1]byte
	if _, err := io.ReadFull(&g.in, first[:]); err != nil {
		return feXzBad(err)
	}
	if first[0] == 0 {
		if err := g.index(); err != nil {
			return err
		}
		g.open = false
		return nil
	}
	return g.blockHeader(first[0])
}

// streamHeader reads a stream header — after the first stream, past the zero
// padding that may come before it — or finds the clean end of the file.
func (g *feXzReader) streamHeader() error {
	var head [12]byte
	n, err := io.ReadFull(&g.in, head[:4])
	if g.streams > 0 {
		for err == nil && head[0] == 0 && head[1] == 0 && head[2] == 0 && head[3] == 0 {
			n, err = io.ReadFull(&g.in, head[:4])
		}
		if err == io.EOF && n == 0 {
			g.done = true
			return nil
		}
	}
	if err != nil {
		return feXzBad(err)
	}
	if _, err := io.ReadFull(&g.in, head[4:]); err != nil {
		return feXzBad(err)
	}
	if !bytes.Equal(head[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0}) ||
		crc32.ChecksumIEEE(head[6:8]) != binary.LittleEndian.Uint32(head[8:12]) || head[6] != 0 {
		return errFeXzFormat
	}
	switch head[7] {
	case 0x00:
		g.check = 0
	case 0x01:
		g.check = 4
	case 0x04:
		g.check = 8
	case 0x0a:
		g.check = 32
	default:
		return feXzUnsupportedErr{what: "its xz file uses an integrity check JukaHub does not know"}
	}
	g.flags = [2]byte{head[6], head[7]}
	g.records = g.records[:0]
	g.streams++
	g.open = true
	return nil
}

// blockHeader reads the rest of a block header whose first byte is size, checks
// it — LZMA2 as the only filter, its dictionary within the limit — and starts
// the block's decoder with that dictionary.
func (g *feXzReader) blockHeader(size byte) error {
	if len(g.records) >= feXzMaxBlocks {
		return feXzUnsupportedErr{what: "its xz file has more blocks than JukaHub reads"}
	}
	hdr := make([]byte, (int(size)+1)*4)
	hdr[0] = size
	if _, err := io.ReadFull(&g.in, hdr[1:]); err != nil {
		return feXzBad(err)
	}
	body := hdr[:len(hdr)-4]
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(hdr[len(hdr)-4:]) {
		return errFeXzFormat
	}
	flags := body[1]
	if flags&0x3c != 0 {
		return errFeXzFormat
	}
	if flags&0x03 != 0 {
		return feXzUnsupportedErr{what: "its xz file uses a filter (BCJ or delta) JukaHub does not unpack"}
	}
	p := 2
	g.wantComp, g.wantUnc = -1, -1
	if flags&0x40 != 0 {
		v, k := feXzVarint(body[p:])
		if k == 0 || v <= 0 {
			return errFeXzFormat
		}
		g.wantComp, p = v, p+k
	}
	if flags&0x80 != 0 {
		v, k := feXzVarint(body[p:])
		if k == 0 {
			return errFeXzFormat
		}
		g.wantUnc, p = v, p+k
	}
	id, k := feXzVarint(body[p:])
	if k == 0 {
		return errFeXzFormat
	}
	p += k
	propLen, k := feXzVarint(body[p:])
	if k == 0 {
		return errFeXzFormat
	}
	p += k
	if id != 0x21 {
		return feXzUnsupportedErr{what: "its xz file is not compressed with LZMA2"}
	}
	if propLen != 1 || p >= len(body) || body[p]&0xc0 != 0 || body[p]&0x3f > 40 {
		return errFeXzFormat
	}
	d := body[p] & 0x3f
	dict := int64(0xffffffff)
	if d < 40 {
		dict = int64(2|(d&1)) << (d/2 + 11)
	}
	if dict > g.limit {
		return feXzMemoryError(dict)
	}
	for _, c := range body[p+1:] {
		if c != 0 {
			return errFeXzFormat
		}
	}
	// Each block starts with a dictionary reset, so a block that says how
	// much it unpacks to never needs a larger dictionary than that.
	if g.wantUnc >= 0 && dict > g.wantUnc {
		dict = g.wantUnc
	}
	if dict < lzma.MinDictCap {
		dict = lzma.MinDictCap
	}
	g.allocated += dict
	if g.allocated > 4*g.limit+64*g.produced {
		return feXzUnsupportedErr{what: "its xz file asks for far more memory than it unpacks to"}
	}
	g.hdrLen, g.start, g.unc = int64(len(hdr)), g.in.n, 0
	switch g.flags[1] {
	case 0x01:
		g.sum = crc32.NewIEEE()
	case 0x04:
		g.sum = crc64.New(feXzCRC64)
	case 0x0a:
		g.sum = sha256.New()
	default:
		g.sum = nil
	}
	block, err := lzma.Reader2Config{DictCap: int(dict)}.NewReader2(&g.in)
	if err != nil {
		return feXzBad(err)
	}
	g.block = block
	return nil
}

// endBlock checks a block once its LZMA2 data has ended: its sizes, its
// padding and its check.
func (g *feXzReader) endBlock() error {
	comp := g.in.n - g.start
	if (g.wantComp >= 0 && comp != g.wantComp) || (g.wantUnc >= 0 && g.unc != g.wantUnc) {
		return errFeXzFormat
	}
	pad := int((4 - (g.hdrLen+comp)%4) % 4)
	var tail [3 + 32]byte
	buf := tail[:pad+g.check]
	if _, err := io.ReadFull(&g.in, buf); err != nil {
		return feXzBad(err)
	}
	for _, c := range buf[:pad] {
		if c != 0 {
			return errFeXzFormat
		}
	}
	stored := buf[pad:]
	switch sum := g.sum.(type) {
	case hash.Hash32:
		if sum.Sum32() != binary.LittleEndian.Uint32(stored) {
			return errFeXzCheck
		}
	case hash.Hash64:
		if sum.Sum64() != binary.LittleEndian.Uint64(stored) {
			return errFeXzCheck
		}
	case hash.Hash:
		if !bytes.Equal(sum.Sum(nil), stored) {
			return errFeXzCheck
		}
	}
	g.records = append(g.records, feXzRecord{unpadded: g.hdrLen + comp + int64(g.check), uncompressed: g.unc})
	return nil
}

// index reads a stream's index (its indicator already read) and footer, and
// checks both against the blocks just decoded.
func (g *feXzReader) index() error {
	sum := crc32.NewIEEE()
	sum.Write([]byte{0})
	size := int64(1)
	varint := func() (int64, error) {
		var b [1]byte
		var v int64
		for i := 0; i < 9; i++ {
			if _, err := io.ReadFull(&g.in, b[:]); err != nil {
				return 0, feXzBad(err)
			}
			sum.Write(b[:])
			size++
			v |= int64(b[0]&0x7f) << (7 * uint(i))
			if b[0]&0x80 == 0 {
				if i > 0 && b[0] == 0 {
					// Not the shortest encoding, which the format requires.
					return 0, errFeXzFormat
				}
				return v, nil
			}
		}
		return 0, errFeXzFormat
	}
	count, err := varint()
	if err != nil {
		return err
	}
	if count != int64(len(g.records)) {
		return errFeXzFormat
	}
	for _, rec := range g.records {
		unpadded, err := varint()
		if err != nil {
			return err
		}
		uncompressed, err := varint()
		if err != nil {
			return err
		}
		if unpadded != rec.unpadded || uncompressed != rec.uncompressed {
			return errFeXzFormat
		}
	}
	pad := int((4 - size%4) % 4)
	var tail [3 + 4]byte
	if _, err := io.ReadFull(&g.in, tail[:pad+4]); err != nil {
		return feXzBad(err)
	}
	for _, c := range tail[:pad] {
		if c != 0 {
			return errFeXzFormat
		}
	}
	sum.Write(tail[:pad])
	if sum.Sum32() != binary.LittleEndian.Uint32(tail[pad:pad+4]) {
		return errFeXzFormat
	}
	size += int64(pad) + 4

	var foot [12]byte
	if _, err := io.ReadFull(&g.in, foot[:]); err != nil {
		return feXzBad(err)
	}
	if foot[10] != 'Y' || foot[11] != 'Z' ||
		crc32.ChecksumIEEE(foot[4:10]) != binary.LittleEndian.Uint32(foot[0:4]) ||
		(int64(binary.LittleEndian.Uint32(foot[4:8]))+1)*4 != size ||
		foot[8] != g.flags[0] || foot[9] != g.flags[1] {
		return errFeXzFormat
	}
	return nil
}
