// Package jdelta is JukaHub's delta update format: a small, strict binary
// patch that rebuilds a new file from the copy already on the device, and a
// bundle (the .jkdelta release asset) that carries one such patch per file of
// a release.
//
// A patch is a fixed header followed by a compress/flate stream of operations:
//
//	header  "JKDPATCH" | version 1 | 3 zero bytes |
//	        old size (u64 BE) | old sha256 | new size (u64 BE) | new sha256
//	ops     COPY (1) uvarint offset, uvarint length   bytes from the old file
//	        ADD  (2) uvarint length, literal bytes
//	        END  (0)
//
// Apply refuses a base whose size or sha256 differs from the header, bounds
// every operation by the sizes the header announced, and only reports success
// when the rebuilt bytes hash to the header's new sha256. A caller that writes
// the result to a temporary file therefore never swaps in a wrong file.
//
// Diff finds the COPY ranges with content-defined chunking: a gear rolling hash
// cuts both files at boundaries that depend only on the bytes around them, so
// an insertion near the start of a file does not shift every later chunk. The
// old file's chunks go into a hash table, every chunk of the new file is looked
// up in it, and each match is grown byte by byte in both directions.
//
// It lives in its own package so the device (player/jdelta_update.go) and the
// release tooling (player/tools/make-delta) share one definition of the bytes.
package jdelta

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PatchMagic opens every single-file patch.
const PatchMagic = "JKDPATCH"

// BundleMagic opens a bundle, the .jkdelta release asset.
const BundleMagic = "JKDBUNDL"

// Version is the format version written into both headers.
const Version = 1

// BundleFormat is the schema number of a bundle's JSON index.
const BundleFormat = 1

// Extension is the file suffix of a bundle.
const Extension = ".jkdelta"

// MaxFileSize bounds one rebuilt file. JukaHub's largest file, the binary, is
// tens of megabytes; a header that claims more is corrupt, not ambitious.
const MaxFileSize = 1 << 30

// MaxFiles bounds the number of files one bundle may rebuild.
const MaxFiles = 512

// BaseInstalled marks a file patched against the copy already installed.
const BaseInstalled = "installed"

// BaseNone marks a file shipped whole: patched against an empty file.
const BaseNone = "none"

// patchHeaderSize is the fixed header of a patch, in bytes.
const patchHeaderSize = 92

// bundleHeaderSize is magic, version, three reserved bytes and the index size.
const bundleHeaderSize = 16

// maxIndexSize bounds a bundle's JSON index.
const maxIndexSize = 4 << 20

// The operation codes of a patch body.
const (
	opEnd  = 0
	opCopy = 1
	opAdd  = 2
)

// Chunking parameters. A chunk ends where the rolling hash's top chunkBits
// bits are zero, after at least chunkMin and at most chunkMax bytes: about
// 1.25 KiB on average, small enough to find the unchanged stretches between
// the edits a rebuilt Go binary scatters, large enough that the table of an
// old binary stays a few tens of thousands of entries.
const (
	chunkMin      = 256
	chunkMax      = 8 << 10
	chunkBits     = 10
	maxCandidates = 8
)

// chunkMask selects the rolling hash's top bits, which depend on the last 64
// bytes; the low bits only see the last few.
const chunkMask uint64 = (1<<chunkBits - 1) << (64 - chunkBits)

var (
	// ErrCorrupt reports a patch or bundle that is malformed or truncated.
	ErrCorrupt = errors.New("jdelta: corrupt delta")
	// ErrWrongBase reports a base file that is not the one the patch was made
	// from: another version is installed, or the file was changed.
	ErrWrongBase = errors.New("jdelta: delta does not apply to this file")
	// ErrResult reports rebuilt bytes that do not hash to the promised sha256.
	ErrResult = errors.New("jdelta: rebuilt file does not match its checksum")
)

// corrupt wraps ErrCorrupt with what was wrong.
func corrupt(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// gear is the rolling hash's byte table: fixed pseudo-random values
// (splitmix64 from a constant seed), so the same input always chunks the
// same way and a bundle is reproducible.
var gear = func() [256]uint64 {
	var t [256]uint64
	x := uint64(0x9E3779B97F4A7C15)
	for i := range t {
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		t[i] = z ^ (z >> 31)
	}
	return t
}()

// ── Patch header ─────────────────────────────────────────────────────────────

// Header is what a patch promises about the file it was made from and the
// file it rebuilds.
type Header struct {
	OldSize int64
	OldSHA  [32]byte
	NewSize int64
	NewSHA  [32]byte
}

// marshal encodes the fixed header.
func (h Header) marshal() []byte {
	b := make([]byte, patchHeaderSize)
	copy(b[0:8], PatchMagic)
	b[8] = Version
	binary.BigEndian.PutUint64(b[12:20], uint64(h.OldSize))
	copy(b[20:52], h.OldSHA[:])
	binary.BigEndian.PutUint64(b[52:60], uint64(h.NewSize))
	copy(b[60:92], h.NewSHA[:])
	return b
}

// ReadHeader reads and checks a patch header.
func ReadHeader(r io.Reader) (Header, error) {
	var raw [patchHeaderSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return Header{}, corrupt("short patch header")
	}
	if string(raw[0:8]) != PatchMagic {
		return Header{}, corrupt("not a JukaHub delta patch")
	}
	if raw[8] != Version {
		return Header{}, corrupt("patch version %d is not supported", raw[8])
	}
	if raw[9] != 0 || raw[10] != 0 || raw[11] != 0 {
		return Header{}, corrupt("reserved header bytes are set")
	}
	oldSize := binary.BigEndian.Uint64(raw[12:20])
	newSize := binary.BigEndian.Uint64(raw[52:60])
	if oldSize > MaxFileSize || newSize > MaxFileSize {
		return Header{}, corrupt("file sizes are out of range")
	}
	h := Header{OldSize: int64(oldSize), NewSize: int64(newSize)}
	copy(h.OldSHA[:], raw[20:52])
	copy(h.NewSHA[:], raw[60:92])
	return h, nil
}

// ── Apply ────────────────────────────────────────────────────────────────────

// Apply rebuilds a file from old (oldSize bytes long) and the patch read from
// patch, writing the result to w. The base is checked against the header
// before the first byte is written. On any error the bytes already written to
// w are garbage and the caller must discard them; ErrResult means everything
// decoded but the result did not hash to the promised sha256.
func Apply(old io.ReaderAt, oldSize int64, patch io.Reader, w io.Writer) (Header, error) {
	h, err := ReadHeader(patch)
	if err != nil {
		return h, err
	}
	if oldSize != h.OldSize {
		return h, fmt.Errorf("%w: the base is %d bytes, the delta was made from %d", ErrWrongBase, oldSize, h.OldSize)
	}
	sum, err := hashSection(old, oldSize)
	if err != nil {
		return h, fmt.Errorf("jdelta: read base: %w", err)
	}
	if sum != h.OldSHA {
		return h, fmt.Errorf("%w: the base checksum differs", ErrWrongBase)
	}

	zr := flate.NewReader(patch)
	defer zr.Close()
	br := bufio.NewReaderSize(zr, 64<<10)
	digest := sha256.New()
	out := io.MultiWriter(w, digest)
	buf := make([]byte, 64<<10)
	var written int64
	for {
		op, err := br.ReadByte()
		if err != nil {
			return h, corrupt("operations end without an END marker (%v)", err)
		}
		if op == opEnd {
			break
		}
		switch op {
		case opCopy:
			off, err := binary.ReadUvarint(br)
			if err != nil {
				return h, corrupt("truncated COPY")
			}
			n, err := binary.ReadUvarint(br)
			if err != nil {
				return h, corrupt("truncated COPY")
			}
			if n == 0 || off > uint64(oldSize) || n > uint64(oldSize)-off || n > uint64(h.NewSize-written) {
				return h, corrupt("COPY %d+%d is out of range", off, n)
			}
			if _, err := io.CopyBuffer(out, io.NewSectionReader(old, int64(off), int64(n)), buf); err != nil {
				return h, fmt.Errorf("jdelta: copy from the base: %w", err)
			}
			written += int64(n)
		case opAdd:
			n, err := binary.ReadUvarint(br)
			if err != nil {
				return h, corrupt("truncated ADD")
			}
			if n == 0 || n > uint64(h.NewSize-written) {
				return h, corrupt("ADD of %d bytes is out of range", n)
			}
			if _, err := io.CopyN(out, br, int64(n)); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return h, corrupt("truncated ADD data")
				}
				return h, fmt.Errorf("jdelta: rebuild: %w", err)
			}
			written += int64(n)
		default:
			return h, corrupt("unknown operation %d", op)
		}
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return h, corrupt("data after the END marker")
	}
	if written != h.NewSize {
		return h, corrupt("rebuilt %d bytes, the delta promised %d", written, h.NewSize)
	}
	var got [32]byte
	copy(got[:], digest.Sum(nil))
	if got != h.NewSHA {
		return h, ErrResult
	}
	return h, nil
}

// hashSection returns the sha256 of the first size bytes of r. A source that
// is shorter simply hashes differently, which the caller reports as a wrong
// base.
func hashSection(r io.ReaderAt, size int64) ([32]byte, error) {
	var sum [32]byte
	h := sha256.New()
	if size > 0 {
		if _, err := io.Copy(h, io.NewSectionReader(r, 0, size)); err != nil {
			return sum, err
		}
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// ── Diff ─────────────────────────────────────────────────────────────────────

// Diff returns a patch that rebuilds newData from oldData. An empty oldData
// makes the patch a compressed copy of newData.
func Diff(oldData, newData []byte) ([]byte, error) {
	if len(oldData) > MaxFileSize || len(newData) > MaxFileSize {
		return nil, fmt.Errorf("jdelta: files larger than %d bytes are not supported", MaxFileSize)
	}
	h := Header{
		OldSize: int64(len(oldData)),
		OldSHA:  sha256.Sum256(oldData),
		NewSize: int64(len(newData)),
		NewSHA:  sha256.Sum256(newData),
	}
	var out bytes.Buffer
	out.Write(h.marshal())
	zw, err := flate.NewWriter(&out, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	ops := &opWriter{w: bufio.NewWriterSize(zw, 64<<10)}

	index := indexChunks(oldData)
	pos, start := 0, 0
	for _, end := range chunkEnds(newData) {
		s := start
		start = end
		if s < pos {
			// The chunk starts inside a match that already grew past it; the
			// rest of it is left to the literal run or the next match.
			continue
		}
		off, ok := findChunk(index, oldData, newData[s:end])
		if !ok {
			continue
		}
		// Grow the match backwards over the pending literal run, then forwards
		// past the chunk's end for as long as the two files agree.
		for s > pos && off > 0 && newData[s-1] == oldData[off-1] {
			s--
			off--
		}
		n := end - s
		for s+n < len(newData) && off+n < len(oldData) && newData[s+n] == oldData[off+n] {
			n++
		}
		ops.add(newData[pos:s])
		ops.copyFrom(off, n)
		pos = s + n
	}
	ops.add(newData[pos:])
	if err := ops.finish(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// chunkEnds cuts data at content-defined boundaries and returns the end offset
// of every chunk.
func chunkEnds(data []byte) []int {
	ends := make([]int, 0, len(data)/1024+1)
	start := 0
	var h uint64
	for i, b := range data {
		h = (h << 1) + gear[b]
		size := i + 1 - start
		if size < chunkMin {
			continue
		}
		if h&chunkMask == 0 || size >= chunkMax {
			ends = append(ends, i+1)
			start = i + 1
			h = 0
		}
	}
	if start < len(data) {
		ends = append(ends, len(data))
	}
	return ends
}

// chunkRef is one chunk of the old file.
type chunkRef struct {
	off int
	n   int
}

// indexChunks maps the content key of every chunk of data to where it is. A
// key repeated many times (padding, tables of zeroes) keeps its first few
// places only; any of them serves a match.
func indexChunks(data []byte) map[uint64][]chunkRef {
	ends := chunkEnds(data)
	index := make(map[uint64][]chunkRef, len(ends))
	start := 0
	for _, end := range ends {
		key := chunkKey(data[start:end])
		if refs := index[key]; len(refs) < maxCandidates {
			index[key] = append(refs, chunkRef{off: start, n: end - start})
		}
		start = end
	}
	return index
}

// chunkKey is a 64-bit FNV-1a of a chunk. A collision costs nothing but a
// comparison: findChunk checks the bytes before it trusts a key.
func chunkKey(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// findChunk returns where piece occurs in oldData as a whole chunk.
func findChunk(index map[uint64][]chunkRef, oldData, piece []byte) (int, bool) {
	for _, ref := range index[chunkKey(piece)] {
		if ref.n == len(piece) && bytes.Equal(oldData[ref.off:ref.off+ref.n], piece) {
			return ref.off, true
		}
	}
	return 0, false
}

// opWriter encodes the operation stream, merging a COPY that continues the
// previous one.
type opWriter struct {
	w       *bufio.Writer
	err     error
	tmp     [binary.MaxVarintLen64]byte
	copyOff int
	copyLen int
}

func (o *opWriter) writeByte(b byte) {
	if o.err == nil {
		o.err = o.w.WriteByte(b)
	}
}

func (o *opWriter) uvarint(v uint64) {
	if o.err != nil {
		return
	}
	n := binary.PutUvarint(o.tmp[:], v)
	_, o.err = o.w.Write(o.tmp[:n])
}

// add emits literal bytes.
func (o *opWriter) add(lit []byte) {
	if len(lit) == 0 {
		return
	}
	o.flushCopy()
	o.writeByte(opAdd)
	o.uvarint(uint64(len(lit)))
	if o.err == nil {
		_, o.err = o.w.Write(lit)
	}
}

// copyFrom emits a copy of n bytes at off in the old file.
func (o *opWriter) copyFrom(off, n int) {
	if n <= 0 {
		return
	}
	if o.copyLen > 0 && o.copyOff+o.copyLen == off {
		o.copyLen += n
		return
	}
	o.flushCopy()
	o.copyOff, o.copyLen = off, n
}

func (o *opWriter) flushCopy() {
	if o.copyLen == 0 {
		return
	}
	o.writeByte(opCopy)
	o.uvarint(uint64(o.copyOff))
	o.uvarint(uint64(o.copyLen))
	o.copyLen = 0
}

// finish writes the END marker and flushes.
func (o *opWriter) finish() error {
	o.flushCopy()
	o.writeByte(opEnd)
	if o.err == nil {
		o.err = o.w.Flush()
	}
	return o.err
}

// ── Bundle ───────────────────────────────────────────────────────────────────

// Bundle is the JSON index of a .jkdelta asset. The patches follow it.
type Bundle struct {
	Format   int          `json:"format"`
	Platform string       `json:"platform,omitempty"`
	FromTag  string       `json:"from_tag,omitempty"`
	ToTag    string       `json:"to_tag,omitempty"`
	Files    []BundleFile `json:"files"`
}

// BundleFile is one file a bundle rebuilds. Name is the path relative to the
// installation, in slash form. Offset and Length locate its patch after the
// index. The sizes and checksums repeat what the patch header promises, so a
// reader can show and check them without decoding anything.
type BundleFile struct {
	Name      string `json:"name"`
	Mode      uint32 `json:"mode,omitempty"`
	Base      string `json:"base"`
	OldSize   int64  `json:"old_size,omitempty"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSize   int64  `json:"new_size"`
	NewSHA256 string `json:"new_sha256"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
}

// BundleInput is one file handed to WriteBundle. A Delta input is patched
// against Old, the copy the device is expected to have; any other input ships
// whole.
type BundleInput struct {
	Name  string
	Mode  uint32
	Old   []byte
	New   []byte
	Delta bool
}

// WriteBundle writes a bundle with one patch per input and returns the index
// it wrote.
func WriteBundle(w io.Writer, meta Bundle, inputs []BundleInput) (Bundle, error) {
	if len(inputs) == 0 || len(inputs) > MaxFiles {
		return Bundle{}, fmt.Errorf("jdelta: a bundle carries 1 to %d files, not %d", MaxFiles, len(inputs))
	}
	index := Bundle{Format: BundleFormat, Platform: meta.Platform, FromTag: meta.FromTag, ToTag: meta.ToTag}
	var payload bytes.Buffer
	seen := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if !ValidName(in.Name) {
			return Bundle{}, fmt.Errorf("jdelta: invalid file name %q", in.Name)
		}
		key := strings.ToLower(in.Name)
		if seen[key] {
			return Bundle{}, fmt.Errorf("jdelta: %s is listed twice", in.Name)
		}
		seen[key] = true
		f := BundleFile{
			Name:      in.Name,
			Mode:      in.Mode & 0o777,
			Base:      BaseNone,
			NewSize:   int64(len(in.New)),
			NewSHA256: hexSum(in.New),
		}
		var base []byte
		if in.Delta {
			if in.Old == nil {
				return Bundle{}, fmt.Errorf("jdelta: %s has no old copy to patch against", in.Name)
			}
			base = in.Old
			f.Base = BaseInstalled
			f.OldSize = int64(len(in.Old))
			f.OldSHA256 = hexSum(in.Old)
		}
		patch, err := Diff(base, in.New)
		if err != nil {
			return Bundle{}, fmt.Errorf("jdelta: %s: %w", in.Name, err)
		}
		f.Offset = int64(payload.Len())
		f.Length = int64(len(patch))
		payload.Write(patch)
		index.Files = append(index.Files, f)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return Bundle{}, err
	}
	if len(raw) > maxIndexSize {
		return Bundle{}, errors.New("jdelta: the bundle index is too large")
	}
	var head [bundleHeaderSize]byte
	copy(head[0:8], BundleMagic)
	head[8] = Version
	binary.BigEndian.PutUint32(head[12:16], uint32(len(raw)))
	for _, part := range [][]byte{head[:], raw, payload.Bytes()} {
		if _, err := w.Write(part); err != nil {
			return Bundle{}, err
		}
	}
	return index, nil
}

// ReadBundleIndex reads and validates the index of a bundle that is size bytes
// long. It returns the index and the offset where the patches start.
func ReadBundleIndex(r io.ReaderAt, size int64) (*Bundle, int64, error) {
	if size < bundleHeaderSize {
		return nil, 0, corrupt("the bundle is too short")
	}
	var head [bundleHeaderSize]byte
	if err := readFullAt(r, head[:], 0); err != nil {
		return nil, 0, corrupt("bundle header: %v", err)
	}
	if string(head[0:8]) != BundleMagic {
		return nil, 0, corrupt("not a JukaHub delta bundle")
	}
	if head[8] != Version {
		return nil, 0, corrupt("bundle version %d is not supported", head[8])
	}
	if head[9] != 0 || head[10] != 0 || head[11] != 0 {
		return nil, 0, corrupt("reserved bundle header bytes are set")
	}
	n := int64(binary.BigEndian.Uint32(head[12:16]))
	if n <= 0 || n > maxIndexSize || bundleHeaderSize+n > size {
		return nil, 0, corrupt("bundle index size %d is out of range", n)
	}
	raw := make([]byte, n)
	if err := readFullAt(r, raw, bundleHeaderSize); err != nil {
		return nil, 0, corrupt("bundle index: %v", err)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, 0, corrupt("bundle index: %v", err)
	}
	payload := bundleHeaderSize + n
	if err := b.validate(size - payload); err != nil {
		return nil, 0, err
	}
	return &b, payload, nil
}

// validate checks every entry of an index against the payload it indexes.
func (b *Bundle) validate(payloadSize int64) error {
	if b.Format != BundleFormat {
		return corrupt("bundle format %d is not supported", b.Format)
	}
	if len(b.Files) == 0 || len(b.Files) > MaxFiles {
		return corrupt("the bundle lists %d files", len(b.Files))
	}
	seen := make(map[string]bool, len(b.Files))
	for _, f := range b.Files {
		if !ValidName(f.Name) {
			return corrupt("bad file name %q", f.Name)
		}
		key := strings.ToLower(f.Name)
		if seen[key] {
			return corrupt("%s is listed twice", f.Name)
		}
		seen[key] = true
		if f.Base != BaseInstalled && f.Base != BaseNone {
			return corrupt("%s has an unknown base %q", f.Name, f.Base)
		}
		if f.NewSize < 0 || f.NewSize > MaxFileSize || !isHexSHA(f.NewSHA256) {
			return corrupt("%s has no valid size or checksum", f.Name)
		}
		if f.Base == BaseInstalled && (f.OldSize < 0 || f.OldSize > MaxFileSize || !isHexSHA(f.OldSHA256)) {
			return corrupt("%s has no valid base checksum", f.Name)
		}
		if f.Offset < 0 || f.Length < patchHeaderSize || f.Offset > payloadSize || f.Length > payloadSize-f.Offset {
			return corrupt("%s points outside the bundle", f.Name)
		}
	}
	return nil
}

// ReadBundleFile reads the index of the bundle at path.
func ReadBundleFile(path string) (*Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, _, err := ReadBundleIndex(f, info.Size())
	return b, err
}

// ApplyBundle rebuilds every file of the bundle at bundlePath into outDir,
// patching against the same-named files under baseDir. Each file is written
// beside its destination and renamed into place only once its checksum
// matched, so a failure never leaves a file that looks finished; the caller
// owns outDir and removes it when this returns an error.
func ApplyBundle(ctx context.Context, bundlePath, baseDir, outDir string) ([]BundleFile, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, payload, err := ReadBundleIndex(f, info.Size())
	if err != nil {
		return nil, err
	}
	for _, bf := range b.Files {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if err := applyBundleFile(f, payload, bf, baseDir, outDir); err != nil {
			return nil, fmt.Errorf("%s: %w", bf.Name, err)
		}
	}
	return b.Files, nil
}

// applyBundleFile rebuilds one file of a bundle.
func applyBundleFile(bundle io.ReaderAt, payload int64, bf BundleFile, baseDir, outDir string) error {
	dest := filepath.Join(outDir, filepath.FromSlash(bf.Name))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".jdpart"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	closed, ok := false, false
	defer func() {
		if !closed {
			_ = out.Close()
		}
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	bw := bufio.NewWriterSize(out, 256<<10)
	section := io.NewSectionReader(bundle, payload+bf.Offset, bf.Length)
	var h Header
	if bf.Base == BaseInstalled {
		base, err := os.Open(filepath.Join(baseDir, filepath.FromSlash(bf.Name)))
		if err != nil {
			return fmt.Errorf("%w: the installed copy is missing", ErrWrongBase)
		}
		info, err := base.Stat()
		if err != nil {
			_ = base.Close()
			return err
		}
		h, err = Apply(base, info.Size(), section, bw)
		_ = base.Close()
		if err != nil {
			return err
		}
	} else {
		h, err = Apply(bytes.NewReader(nil), 0, section, bw)
		if err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	closed = true
	if err := out.Close(); err != nil {
		return err
	}
	// The index is what a reader shows and checks; a patch whose header says
	// something else was not built from this index.
	if h.NewSize != bf.NewSize || !strings.EqualFold(hex.EncodeToString(h.NewSHA[:]), bf.NewSHA256) {
		return corrupt("the patch and the index disagree about the result")
	}
	if bf.Base == BaseInstalled && !strings.EqualFold(hex.EncodeToString(h.OldSHA[:]), bf.OldSHA256) {
		return corrupt("the patch and the index disagree about the base")
	}
	mode := os.FileMode(bf.Mode & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	// FAT cards refuse chmod; the extracted default is usable there.
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	ok = true
	return nil
}

// readFullAt fills p from r at off.
func readFullAt(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// hexSum is the lower-case hex sha256 of b.
func hexSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isHexSHA reports whether s is a complete sha256 in hex.
func isHexSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// ValidName reports whether name is a safe relative path in slash form: no
// absolute path, no drive, no backslash, no "." or ".." element, no NUL.
func ValidName(name string) bool {
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ── Names ────────────────────────────────────────────────────────────────────

// archivePrefix is the start of every JukaHub release archive's name.
const archivePrefix = "JukaHub-"

// PlatformFromArchive returns the platform word of a release archive name,
// "Trimui" for JukaHub-Trimui.zip, or "" for any other name.
func PlatformFromArchive(asset string) string {
	name := strings.TrimSpace(asset)
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, strings.ToLower(archivePrefix)) {
		return ""
	}
	for _, ext := range []string{".zip", ".tar.gz", ".tgz"} {
		if !strings.HasSuffix(lower, ext) || len(name) < len(archivePrefix)+len(ext) {
			continue
		}
		platform := name[len(archivePrefix) : len(name)-len(ext)]
		if safeWord(platform) {
			return platform
		}
		return ""
	}
	return ""
}

// AssetName is the bundle a release publishes for installs of fromTag on a
// platform: JukaHub-<platform>-<fromStamp>.jkdelta, where fromStamp is the
// release tag without its "JukaHub-" prefix (the timestamp the release
// workflow names its tags with). It returns "" when either part is unusable.
func AssetName(platform, fromTag string) string {
	stamp := strings.TrimSpace(fromTag)
	if len(stamp) > len(archivePrefix) && strings.EqualFold(stamp[:len(archivePrefix)], archivePrefix) {
		stamp = stamp[len(archivePrefix):]
	}
	if !safeWord(platform) || !safeWord(stamp) {
		return ""
	}
	return archivePrefix + platform + "-" + stamp + Extension
}

// safeWord reports whether s may be part of an asset name.
func safeWord(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
