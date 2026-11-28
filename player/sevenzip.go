package main

// sevenzip.go — reading a .7z game's directory, and the first bytes of one of
// its files, inside JukaHub.
//
// The library has always listed .7z games and RetroArch opens them itself,
// but everything JukaHub reads from a game's file skipped them: the Game Boy
// Color check (gbcsort.go) read .gb, .gbc and .zip only, and the CRC32 the
// cover matcher and the game details look a game up by (gmetaROMCRCs) was the
// CRC of the archive, which no database lists. A zip's directory carries each
// file's CRC and Go reads zips; a 7z's directory carries the same, but Go has
// no 7z reader, and emu7z.go's 7-Zip executable is a Windows download for
// emulator installs, not something to start per game on the handheld.
//
// This is the small part of the format those callers need, in pure Go:
//
//   - The signature header (magic, version, the start header's CRC, where the
//     directory is and its CRC), then the directory ("header"): plain
//     (kHeader) or itself packed (kEncodedHeader, what 7-Zip writes by
//     default: LZMA, decoded here, then parsed).
//   - The streams (pack positions and sizes, folders with their coders and
//     unpack sizes, the sub-streams with their sizes and CRCs) and the files
//     (UTF-16LE names, empty-stream and empty-file flags), so each file gets
//     its name, size, CRC32 and place in its folder's stream.
//   - Reading the first bytes of a file: its folder's stream is decoded from
//     the start up to the file's offset plus what was asked. Copy (00), LZMA
//     (03 01 01) and LZMA2 (21) are read with the vendored
//     github.com/ulikunitz/xz/lzma package (the same one files_extract_xz.go
//     uses), alone or under the branch filters for ARM programs — ARM64 (0A),
//     ARM (03 03 05 01) and ARM-Thumb (03 03 07 01) — which 7-Zip 23 and later
//     put in front of LZMA2 by itself for every 64-bit ARM program, ELF ones
//     included: TrimUI's Smart Pro packages of ra64.trimui and its cores
//     (raget_7z.go). Anything else (the x86 BCJ/BCJ2 filters, PPMd, Deflate,
//     BZip2, AES, coders that are not one chain) is errSevenZipUnsupported,
//     and the callers then do what they did before. Listing needs no decoding
//     beyond the directory, so it works whatever the files were packed with.
//
// It is outside data, so every size is bounded before anything is allocated:
// a directory of at most sevenZipMaxHeader bytes (decoded or not), at most
// sevenZipMaxFiles files and folders, names of at most sevenZipMaxName bytes,
// a dictionary of at most sevenZipMaxDict, and at most sevenZipMaxDecode bytes
// decoded for one read; the dictionary is never larger than what one read can
// reach back into. Offsets are checked against the file's size, CRCs against
// the data, and a malformed archive is an error, never a panic.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ulikunitz/xz/lzma"
)

const (
	// sevenZipSigLen is the signature header's size; every offset in the
	// archive counts from its end.
	sevenZipSigLen = 32
	// sevenZipMaxHeader bounds the directory, packed and decoded.
	sevenZipMaxHeader = 4 << 20
	// sevenZipMaxFiles bounds the files, folders, pack streams and
	// sub-streams one directory may declare.
	sevenZipMaxFiles = 10000
	// sevenZipMaxName bounds one file name, in UTF-16 bytes.
	sevenZipMaxName = 4096
	// sevenZipMaxCoders bounds the coders of one folder (7-Zip writes at most
	// four: BCJ2 and its three LZMA streams).
	sevenZipMaxCoders = 8
	// sevenZipMaxProps bounds one coder's properties.
	sevenZipMaxProps = 1 << 10
	// sevenZipMaxDict bounds the decoder's dictionary.
	sevenZipMaxDict = 64 << 20
	// sevenZipMaxDecode bounds what one read decodes: the file's offset in its
	// solid folder plus the bytes asked for.
	sevenZipMaxDecode = 64 << 20
	// sevenZipMaxRead bounds the bytes one read returns.
	sevenZipMaxRead = 1 << 20
	// sevenZipMaxNesting bounds packed directories inside packed directories.
	sevenZipMaxNesting = 4
)

// sevenZipLimits bounds what one archive's directory may declare and what a
// read of it may need: the files (and folders, pack streams, sub-streams),
// the directory's size packed and decoded, and the decoder's dictionary. A
// game's .7z is read with sevenZipDefaultLimits; TrimUI's SD packages, a
// whole card in one archive, with larger ones (raget_7z.go).
type sevenZipLimits struct {
	Files  int
	Header uint64
	Dict   uint64
}

// sevenZipDefaultLimits are the bounds at the top of the file.
var sevenZipDefaultLimits = sevenZipLimits{Files: sevenZipMaxFiles, Header: sevenZipMaxHeader, Dict: sevenZipMaxDict}

// The directory's property ids (7zFormat.txt).
const (
	sevenZipIDEnd             = 0x00
	sevenZipIDHeader          = 0x01
	sevenZipIDArchiveProps    = 0x02
	sevenZipIDAdditional      = 0x03
	sevenZipIDMainStreams     = 0x04
	sevenZipIDFilesInfo       = 0x05
	sevenZipIDPackInfo        = 0x06
	sevenZipIDUnpackInfo      = 0x07
	sevenZipIDSubStreams      = 0x08
	sevenZipIDSize            = 0x09
	sevenZipIDCRC             = 0x0A
	sevenZipIDFolder          = 0x0B
	sevenZipIDCodersUnpackSz  = 0x0C
	sevenZipIDNumUnpackStream = 0x0D
	sevenZipIDEmptyStream     = 0x0E
	sevenZipIDEmptyFile       = 0x0F
	sevenZipIDName            = 0x11
	sevenZipIDMTime           = 0x14
	sevenZipIDEncodedHeader   = 0x17
)

// sevenZipMagic starts every 7z archive.
var sevenZipMagic = []byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C}

var (
	// errSevenZipFormat is a file that is not a 7z archive this reader can
	// make sense of (damaged, cut short, or not 7z at all).
	errSevenZipFormat = errors.New("not a readable 7z archive")
	// errSevenZipUnsupported is a valid archive packed with a method this
	// reader does not decode.
	errSevenZipUnsupported = errors.New("7z compression method not supported")
	// errSevenZipTooFar is a file that starts too deep into its solid block
	// for a bounded read.
	errSevenZipTooFar = errors.New("7z file starts too far into its block")
)

// sevenZipEntry is one file of the archive.
type sevenZipEntry struct {
	Name   string // the path inside the archive, with "/" separators
	Size   uint64
	CRC    uint32
	HasCRC bool
	Dir    bool
	// MTime is its time (kMTime), zero when the archive keeps none.
	MTime time.Time
	// folder is the folder holding its data (-1: an empty file or a folder);
	// offset is where it starts in that folder's decoded stream.
	folder int
	offset uint64
}

// sevenZipCoder is one coder of a folder: its method id and properties, and
// how many streams it takes in and gives out.
type sevenZipCoder struct {
	id      []byte
	props   []byte
	in, out int
}

// sevenZipFolder is one solid block: its coders and where its packed data is.
type sevenZipFolder struct {
	method []byte // the coder that reads the packed stream; nil when the coders are not one chain
	props  []byte
	// filters are the branch filters over method's output, in the order a
	// decoder applies them (7-Zip's "ARM64 LZMA2" is LZMA2, then ARM64), and
	// codeOut is method's out stream; coders are all of them, for messages.
	filters    []sevenZipCoder
	codeOut    int
	coders     []sevenZipCoder
	simple     bool // one packed stream through a chain of one-in, one-out coders
	packIndex  int  // its first pack stream
	numPacked  int
	outSizes   []uint64
	mainOut    int    // the out stream that is the folder's output
	unpackSize uint64 // the size of the stream it ends with
	crc        uint32
	hasCRC     bool
}

// sevenZipStreams is a parsed StreamsInfo.
type sevenZipStreams struct {
	packStart []uint64 // absolute offset of each pack stream
	packSize  []uint64
	folders   []sevenZipFolder
	// The sub-streams (files with data), folder by folder.
	subFolder []int
	subOffset []uint64
	subSize   []uint64
	subCRC    []uint32
	subHasCRC []bool
}

// sevenZipArchive is an archive's directory.
type sevenZipArchive struct {
	Entries []sevenZipEntry
	streams sevenZipStreams
	size    int64
}

// ── Reading the directory ───────────────────────────────────────────────────

// sevenZipBuf reads the directory's encoding. The first error sticks: every
// later read returns zero, and the caller checks err once a structure is done.
type sevenZipBuf struct {
	b   []byte
	p   int
	err error
	// maxFiles bounds the files, folders and streams the directory may
	// declare (sevenZipMaxFiles when 0).
	maxFiles int
}

// files is the most files, folders or streams the directory may declare.
func (b *sevenZipBuf) files() int {
	if b.maxFiles > 0 {
		return b.maxFiles
	}
	return sevenZipMaxFiles
}

func (b *sevenZipBuf) fail() {
	if b.err == nil {
		b.err = errSevenZipFormat
	}
}

func (b *sevenZipBuf) byte() byte {
	if b.err != nil || b.p >= len(b.b) {
		b.fail()
		return 0
	}
	v := b.b[b.p]
	b.p++
	return v
}

// bytes returns the next n bytes (a view, not a copy).
func (b *sevenZipBuf) bytes(n uint64) []byte {
	if b.err != nil || n > uint64(len(b.b)-b.p) {
		b.fail()
		return nil
	}
	v := b.b[b.p : b.p+int(n)]
	b.p += int(n)
	return v
}

// number reads 7z's variable-length integer: the first byte's leading one
// bits say how many little-endian bytes follow, its remaining bits are the
// value's top.
func (b *sevenZipBuf) number() uint64 {
	first := b.byte()
	mask := byte(0x80)
	var v uint64
	for i := 0; i < 8; i++ {
		if first&mask == 0 {
			high := uint64(first) & (uint64(mask) - 1)
			return v | high<<(8*uint(i))
		}
		v |= uint64(b.byte()) << (8 * uint(i))
		mask >>= 1
	}
	return v
}

// count reads a number that counts something and must be at most limit.
func (b *sevenZipBuf) count(limit int) int {
	n := b.number()
	if n > uint64(limit) {
		b.fail()
		return 0
	}
	return int(n)
}

func (b *sevenZipBuf) uint32() uint32 {
	v := b.bytes(4)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}

// skipData skips a property this reader does not use: its size, then that
// many bytes.
func (b *sevenZipBuf) skipData() {
	b.bytes(b.number())
}

// waitFor skips properties until id; kEnd first is a format error.
func (b *sevenZipBuf) waitFor(id uint64) {
	for b.err == nil {
		t := b.number()
		if t == id {
			return
		}
		if t == sevenZipIDEnd {
			b.fail()
			return
		}
		b.skipData()
	}
}

// bits reads n flags, the first in a byte's top bit.
func (b *sevenZipBuf) bits(n int) []bool {
	raw := b.bytes(uint64((n + 7) / 8))
	if raw == nil && n > 0 {
		return nil
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = raw[i/8]&(0x80>>(uint(i)%8)) != 0
	}
	return out
}

// digests reads n optional CRCs: an all-defined byte or a bit field, then a
// CRC32 for each one defined.
func (b *sevenZipBuf) digests(n int) ([]uint32, []bool) {
	defined := make([]bool, n)
	if b.byte() != 0 {
		for i := range defined {
			defined[i] = true
		}
	} else if got := b.bits(n); got != nil {
		defined = got
	}
	crcs := make([]uint32, n)
	for i := range crcs {
		if defined[i] {
			crcs[i] = b.uint32()
		}
	}
	return crcs, defined
}

// sevenZipOpenFile reads the directory of the archive at path.
func sevenZipOpenFile(path string) (*sevenZipArchive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errSevenZipFormat
	}
	return sevenZipOpen(f, info.Size())
}

// sevenZipOpen reads the directory of an archive of size bytes.
func sevenZipOpen(r io.ReaderAt, size int64) (*sevenZipArchive, error) {
	return sevenZipOpenWith(r, size, sevenZipDefaultLimits)
}

// sevenZipOpenWith is sevenZipOpen with lim's bounds.
func sevenZipOpenWith(r io.ReaderAt, size int64, lim sevenZipLimits) (*sevenZipArchive, error) {
	if size < sevenZipSigLen {
		return nil, errSevenZipFormat
	}
	sig := make([]byte, sevenZipSigLen)
	if _, err := r.ReadAt(sig, 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(sig[:6], sevenZipMagic) || sig[6] != 0 {
		return nil, errSevenZipFormat
	}
	if crc32.ChecksumIEEE(sig[12:32]) != binary.LittleEndian.Uint32(sig[8:12]) {
		return nil, errSevenZipFormat
	}
	nextOff := binary.LittleEndian.Uint64(sig[12:20])
	nextSize := binary.LittleEndian.Uint64(sig[20:28])
	nextCRC := binary.LittleEndian.Uint32(sig[28:32])
	a := &sevenZipArchive{size: size}
	if nextSize == 0 {
		return a, nil // an empty archive
	}
	room := uint64(size - sevenZipSigLen)
	if nextSize > lim.Header || nextOff > room || nextSize > room-nextOff {
		return nil, errSevenZipFormat
	}
	hdr := make([]byte, nextSize)
	if _, err := r.ReadAt(hdr, int64(sevenZipSigLen+nextOff)); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(hdr) != nextCRC {
		return nil, errSevenZipFormat
	}
	for depth := 0; ; depth++ {
		if depth > sevenZipMaxNesting {
			return nil, errSevenZipFormat
		}
		b := &sevenZipBuf{b: hdr, maxFiles: lim.Files}
		switch b.number() {
		case sevenZipIDHeader:
			if err := a.parseHeader(b); err != nil {
				return nil, err
			}
			return a, nil
		case sevenZipIDEncodedHeader:
			st := a.parseStreams(b)
			if b.err != nil {
				return nil, b.err
			}
			if len(st.folders) == 0 {
				return nil, errSevenZipFormat
			}
			// The directory is the first folder's whole output.
			f := st.folders[0]
			if f.unpackSize == 0 || f.unpackSize > lim.Header {
				return nil, errSevenZipFormat
			}
			rd, err := st.folderReaderDict(r, size, 0, f.unpackSize, lim.Dict)
			if err != nil {
				return nil, err
			}
			next := make([]byte, f.unpackSize)
			if _, err := io.ReadFull(rd, next); err != nil {
				return nil, errSevenZipFormat
			}
			if f.hasCRC && crc32.ChecksumIEEE(next) != f.crc {
				return nil, errSevenZipFormat
			}
			hdr = next
		default:
			return nil, errSevenZipFormat
		}
	}
}

// parseHeader reads a plain directory, after its kHeader id.
func (a *sevenZipArchive) parseHeader(b *sevenZipBuf) error {
	t := b.number()
	if t == sevenZipIDArchiveProps {
		for b.err == nil {
			if b.number() == sevenZipIDEnd {
				break
			}
			b.skipData()
		}
		t = b.number()
	}
	if t == sevenZipIDAdditional {
		// Never written by 7-Zip; streams this reader has no use for.
		a.parseStreams(b)
		t = b.number()
	}
	if t == sevenZipIDMainStreams {
		a.streams = a.parseStreams(b)
		t = b.number()
	}
	if t == sevenZipIDFilesInfo {
		a.parseFiles(b)
		t = b.number()
	}
	if b.err != nil {
		return b.err
	}
	if t != sevenZipIDEnd {
		return errSevenZipFormat
	}
	return nil
}

// parseStreams reads a StreamsInfo: pack info, folders, sub-streams.
func (a *sevenZipArchive) parseStreams(b *sevenZipBuf) sevenZipStreams {
	var st sevenZipStreams
	t := b.number()
	if t == sevenZipIDPackInfo {
		a.parsePackInfo(b, &st)
		t = b.number()
	}
	if t == sevenZipIDUnpackInfo {
		parseSevenZipFolders(b, &st)
		t = b.number()
	}
	if t == sevenZipIDSubStreams {
		parseSevenZipSubStreams(b, &st)
		t = b.number()
	} else {
		// Without sub-stream info every folder is one file, its CRC the
		// folder's.
		for i, f := range st.folders {
			st.addSub(i, 0, f.unpackSize, f.crc, f.hasCRC)
		}
	}
	if b.err == nil && t != sevenZipIDEnd {
		b.fail()
	}
	if b.err != nil {
		return sevenZipStreams{}
	}
	// Each folder takes its pack streams in order.
	next := 0
	for i := range st.folders {
		st.folders[i].packIndex = next
		next += st.folders[i].numPacked
	}
	if next > len(st.packSize) {
		b.fail()
		return sevenZipStreams{}
	}
	return st
}

// parsePackInfo reads where the packed streams are; offsets become absolute
// and are checked against the file.
func (a *sevenZipArchive) parsePackInfo(b *sevenZipBuf, st *sevenZipStreams) {
	pos := b.number()
	n := b.count(b.files())
	b.waitFor(sevenZipIDSize)
	if b.err != nil {
		return
	}
	limit := uint64(a.size)
	at := uint64(sevenZipSigLen)
	if pos > limit-at {
		b.fail()
		return
	}
	at += pos
	st.packStart = make([]uint64, n)
	st.packSize = make([]uint64, n)
	for i := 0; i < n; i++ {
		size := b.number()
		if size > limit-at {
			b.fail()
			return
		}
		st.packStart[i], st.packSize[i] = at, size
		at += size
	}
	for b.err == nil {
		t := b.number()
		if t == sevenZipIDEnd {
			return
		}
		if t == sevenZipIDCRC {
			b.digests(n)
			continue
		}
		b.skipData()
	}
}

// parseSevenZipFolders reads the folders, their coders and unpack sizes.
func parseSevenZipFolders(b *sevenZipBuf, st *sevenZipStreams) {
	b.waitFor(sevenZipIDFolder)
	n := b.count(b.files())
	if b.byte() != 0 {
		// The folders kept in another stream: never written by 7-Zip.
		b.err = errSevenZipUnsupported
		return
	}
	st.folders = make([]sevenZipFolder, n)
	for i := 0; i < n && b.err == nil; i++ {
		st.folders[i] = parseSevenZipFolder(b)
	}
	b.waitFor(sevenZipIDCodersUnpackSz)
	for i := 0; i < n && b.err == nil; i++ {
		f := &st.folders[i]
		for j := range f.outSizes {
			f.outSizes[j] = b.number()
		}
		if f.mainOut < len(f.outSizes) {
			f.unpackSize = f.outSizes[f.mainOut]
		}
	}
	for b.err == nil {
		t := b.number()
		if t == sevenZipIDEnd {
			return
		}
		if t == sevenZipIDCRC {
			crcs, defined := b.digests(n)
			for i := 0; i < n && b.err == nil; i++ {
				st.folders[i].crc, st.folders[i].hasCRC = crcs[i], defined[i]
			}
			continue
		}
		b.skipData()
	}
}

// parseSevenZipFolder reads one folder's coders and bind pairs, and whether
// they are one chain this reader follows (chain).
func parseSevenZipFolder(b *sevenZipBuf) sevenZipFolder {
	var f sevenZipFolder
	numCoders := b.count(sevenZipMaxCoders)
	if numCoders == 0 {
		b.fail()
		return f
	}
	totalIn, totalOut := 0, 0
	for c := 0; c < numCoders && b.err == nil; c++ {
		flags := b.byte()
		if flags&0x80 != 0 {
			b.fail() // "alternative methods": never used
			return f
		}
		id := b.bytes(uint64(flags & 0x0F))
		in, out := 1, 1
		if flags&0x10 != 0 {
			in = b.count(sevenZipMaxCoders)
			out = b.count(sevenZipMaxCoders)
		}
		var props []byte
		if flags&0x20 != 0 {
			props = b.bytes(uint64(b.count(sevenZipMaxProps)))
		}
		totalIn += in
		totalOut += out
		f.coders = append(f.coders, sevenZipCoder{id: append([]byte(nil), id...), props: append([]byte(nil), props...), in: in, out: out})
	}
	if b.err != nil || totalOut == 0 {
		b.fail()
		return f
	}
	// The folder's output is the one out stream no bind pair consumes; with
	// one packed stream, that stream goes to the one in stream no bind pair
	// feeds. feeds maps an in stream to the out stream bound to it.
	bindPairs := totalOut - 1
	boundOut := make([]bool, totalOut)
	boundIn := make([]bool, totalIn)
	feeds := make(map[int]int, bindPairs)
	for i := 0; i < bindPairs; i++ {
		in := b.number()
		out := b.number()
		if in < uint64(totalIn) && out < uint64(totalOut) {
			boundIn[in], boundOut[out] = true, true
			feeds[int(in)] = int(out)
		}
	}
	for j, isBound := range boundOut {
		if !isBound {
			f.mainOut = j
			break
		}
	}
	f.numPacked = totalIn - bindPairs
	if f.numPacked < 1 {
		b.fail()
		return f
	}
	packedIn := -1
	if f.numPacked > 1 {
		for i := 0; i < f.numPacked; i++ {
			b.number()
		}
	} else {
		for j, isBound := range boundIn {
			if !isBound {
				packedIn = j
				break
			}
		}
	}
	f.outSizes = make([]uint64, totalOut)
	f.chain(feeds, packedIn)
	return f
}

// chain works out whether f's coders are one chain from its packed stream to
// its output — every coder one stream in and one out, each feeding the next,
// the last reading packedIn (-1: several packed streams, never one chain) —
// and if so sets method and props (the coder that reads the packed stream),
// the filters over it in decoding order and codeOut, and simple.
func (f *sevenZipFolder) chain(feeds map[int]int, packedIn int) {
	for _, c := range f.coders {
		if c.in != 1 || c.out != 1 {
			return
		}
	}
	// Every coder takes one stream in and gives one out, so in stream i and
	// out stream i are both coder i's. From the output back to the packed
	// stream:
	n := len(f.coders)
	order := []int{f.mainOut}
	for cur := f.mainOut; cur != packedIn; {
		next, ok := feeds[cur]
		if !ok || len(order) >= n {
			return
		}
		order = append(order, next)
		cur = next
	}
	if len(order) != n {
		return
	}
	code := order[n-1]
	f.method = append([]byte(nil), f.coders[code].id...)
	f.props = append([]byte(nil), f.coders[code].props...)
	f.codeOut = code
	f.filters = nil
	for k := n - 2; k >= 0; k-- {
		f.filters = append(f.filters, f.coders[order[k]])
	}
	f.simple = true
}

// parseSevenZipSubStreams reads how each folder splits into files, their
// sizes and CRCs.
func parseSevenZipSubStreams(b *sevenZipBuf, st *sevenZipStreams) {
	numUnpack := make([]int, len(st.folders))
	for i := range numUnpack {
		numUnpack[i] = 1
	}
	t := b.number()
	for b.err == nil {
		if t == sevenZipIDNumUnpackStream {
			total := 0
			for i := range numUnpack {
				numUnpack[i] = b.count(b.files())
				total += numUnpack[i]
				if total > b.files() {
					b.fail()
				}
			}
			t = b.number()
			continue
		}
		if t == sevenZipIDCRC || t == sevenZipIDSize || t == sevenZipIDEnd {
			break
		}
		b.skipData()
		t = b.number()
	}
	if b.err != nil {
		return
	}
	// Sizes: every file but a folder's last is listed; the last is the rest.
	sizes := make([][]uint64, len(st.folders))
	for i, f := range st.folders {
		n := numUnpack[i]
		if n == 0 {
			continue
		}
		if t != sevenZipIDSize && n > 1 {
			b.fail()
			return
		}
		var sum uint64
		for j := 1; j < n && t == sevenZipIDSize; j++ {
			s := b.number()
			if b.err != nil || s > f.unpackSize-sum {
				b.fail()
				return
			}
			sizes[i] = append(sizes[i], s)
			sum += s
		}
		sizes[i] = append(sizes[i], f.unpackSize-sum)
	}
	if t == sevenZipIDSize {
		t = b.number()
	}
	// CRCs: listed for every file but a folder's only one whose CRC the
	// folder already gives.
	missing := 0
	for i, f := range st.folders {
		if numUnpack[i] != 1 || !f.hasCRC {
			missing += numUnpack[i]
		}
	}
	var crcs []uint32
	var defined []bool
	for b.err == nil && t != sevenZipIDEnd {
		if t == sevenZipIDCRC {
			crcs, defined = b.digests(missing)
		} else {
			b.skipData()
		}
		t = b.number()
	}
	if b.err != nil {
		return
	}
	k := 0
	for i, f := range st.folders {
		var offset uint64
		for _, size := range sizes[i] {
			crc, has := uint32(0), false
			switch {
			case numUnpack[i] == 1 && f.hasCRC:
				crc, has = f.crc, true
			case k < len(crcs) && k < len(defined):
				crc, has = crcs[k], defined[k]
				k++
			default:
				k++
			}
			st.addSub(i, offset, size, crc, has)
			offset += size
		}
	}
}

// addSub records one file's place in folder.
func (st *sevenZipStreams) addSub(folder int, offset, size uint64, crc uint32, has bool) {
	st.subFolder = append(st.subFolder, folder)
	st.subOffset = append(st.subOffset, offset)
	st.subSize = append(st.subSize, size)
	st.subCRC = append(st.subCRC, crc)
	st.subHasCRC = append(st.subHasCRC, has)
}

// parseFiles reads the files: names and which have no data.
func (a *sevenZipArchive) parseFiles(b *sevenZipBuf) {
	n := b.count(b.files())
	var emptyStream, emptyFile []bool
	var names []string
	var mtimes []time.Time
	for b.err == nil {
		t := b.number()
		if t == sevenZipIDEnd {
			break
		}
		data := b.bytes(b.number())
		if b.err != nil {
			return
		}
		switch t {
		case sevenZipIDEmptyStream:
			emptyStream = (&sevenZipBuf{b: data}).bits(n)
		case sevenZipIDEmptyFile:
			empties := 0
			for _, e := range emptyStream {
				if e {
					empties++
				}
			}
			emptyFile = (&sevenZipBuf{b: data}).bits(empties)
		case sevenZipIDName:
			names = sevenZipNames(data, n)
			if names == nil && n > 0 {
				b.fail()
				return
			}
		case sevenZipIDMTime:
			// Times that do not fit are left out, not an error: nothing
			// depends on them.
			mtimes = sevenZipTimes(data, n)
		}
	}
	if b.err != nil {
		return
	}
	st := &a.streams
	sub, empty := 0, 0
	a.Entries = make([]sevenZipEntry, 0, n)
	for i := 0; i < n; i++ {
		e := sevenZipEntry{folder: -1}
		if i < len(names) {
			e.Name = names[i]
		}
		if i < len(mtimes) {
			e.MTime = mtimes[i]
		}
		if i < len(emptyStream) && emptyStream[i] {
			// No data: a folder unless flagged an empty file.
			e.Dir = !(empty < len(emptyFile) && emptyFile[empty])
			empty++
		} else {
			if sub >= len(st.subSize) {
				b.fail()
				return
			}
			e.folder, e.offset, e.Size = st.subFolder[sub], st.subOffset[sub], st.subSize[sub]
			e.CRC, e.HasCRC = st.subCRC[sub], st.subHasCRC[sub]
			sub++
		}
		a.Entries = append(a.Entries, e)
	}
}

// sevenZipNames decodes n zero-terminated UTF-16LE names, after the
// "external" byte (which must be 0). nil when they do not fit.
func sevenZipNames(data []byte, n int) []string {
	if len(data) < 1 || data[0] != 0 {
		return nil
	}
	data = data[1:]
	out := make([]string, 0, n)
	var units []uint16
	for i := 0; i+1 < len(data) && len(out) < n; i += 2 {
		u := uint16(data[i]) | uint16(data[i+1])<<8
		if u != 0 {
			if len(units) >= sevenZipMaxName/2 {
				return nil
			}
			units = append(units, u)
			continue
		}
		name := strings.ReplaceAll(string(utf16.Decode(units)), "\\", "/")
		out = append(out, name)
		units = units[:0]
	}
	if len(out) != n {
		return nil
	}
	return out
}

// sevenZipFiletimeUnix is 1970-01-01 as a Windows FILETIME: 100-nanosecond
// steps since 1601.
const sevenZipFiletimeUnix = 116444736000000000

// sevenZipTimes decodes n optional file times (kMTime): an all-defined byte or
// a bit field, the "external" byte (which must be 0), then a FILETIME for each
// one defined. nil when they do not fit.
func sevenZipTimes(data []byte, n int) []time.Time {
	b := &sevenZipBuf{b: data}
	defined := make([]bool, n)
	if b.byte() != 0 {
		for i := range defined {
			defined[i] = true
		}
	} else if got := b.bits(n); got != nil {
		defined = got
	}
	if b.byte() != 0 {
		return nil // kept in another stream: never written by 7-Zip
	}
	out := make([]time.Time, n)
	for i := range out {
		if !defined[i] {
			continue
		}
		raw := b.bytes(8)
		if raw == nil {
			return nil
		}
		if ft := binary.LittleEndian.Uint64(raw); ft > sevenZipFiletimeUnix {
			d := ft - sevenZipFiletimeUnix
			out[i] = time.Unix(int64(d/10000000), int64(d%10000000)*100).UTC()
		}
	}
	if b.err != nil {
		return nil
	}
	return out
}

// ── Reading a file's first bytes ────────────────────────────────────────────

// folderReader decodes folder i's stream, at most limit bytes of it.
func (st *sevenZipStreams) folderReader(r io.ReaderAt, size int64, i int, limit uint64) (io.Reader, error) {
	return st.folderReaderDict(r, size, i, limit, sevenZipMaxDict)
}

// folderReaderDict is folderReader with a dictionary of at most maxDict.
func (st *sevenZipStreams) folderReaderDict(r io.ReaderAt, size int64, i int, limit, maxDict uint64) (io.Reader, error) {
	if i < 0 || i >= len(st.folders) {
		return nil, errSevenZipFormat
	}
	f := st.folders[i]
	if !f.simple {
		return nil, errSevenZipUnsupported
	}
	for _, c := range f.filters {
		if _, _, ok := sevenZipBranchFor(c); !ok {
			return nil, errSevenZipUnsupported
		}
	}
	if f.packIndex >= len(st.packSize) || f.codeOut >= len(f.outSizes) {
		return nil, errSevenZipFormat
	}
	// A branch filter keeps the size: the coder under the filters gives as
	// much as the folder does.
	if f.outSizes[f.codeOut] != f.unpackSize {
		return nil, errSevenZipFormat
	}
	start, packed := st.packStart[f.packIndex], st.packSize[f.packIndex]
	if start > uint64(size) || packed > uint64(size)-start {
		return nil, errSevenZipFormat
	}
	limit = min(limit, f.unpackSize)
	// Under a filter the coder decodes a little past the read: an instruction
	// the read's end cuts is converted whole (sevenZipBranchAhead).
	inner := limit
	if len(f.filters) > 0 {
		inner = min(f.unpackSize, limit+sevenZipBranchAhead)
	}
	src := io.NewSectionReader(r, int64(start), int64(packed))
	// The dictionary need not be the encoder's: no match reaches back further
	// than the bytes before it, so one that holds every byte the decoder ever
	// produces for this read is enough. The decoder works ahead of the reader
	// by up to the dictionary itself, but only from where the reader stands
	// and never past the end of an LZMA2 chunk (2 MiB unpacked) — or, for
	// plain LZMA, never at all once one pass filled the dictionary with more
	// than this read needs. slack is that look-ahead.
	dictFor := func(declared, slack uint64) (int, error) {
		d := min(declared, f.unpackSize, inner+slack)
		if d < lzma.MinDictCap {
			d = lzma.MinDictCap
		}
		if d > maxDict {
			return 0, errSevenZipTooFar
		}
		return int(d), nil
	}
	var dec io.Reader
	switch {
	case bytes.Equal(f.method, []byte{0x00}):
		dec = src
	case bytes.Equal(f.method, []byte{0x03, 0x01, 0x01}):
		if len(f.props) != 5 {
			return nil, errSevenZipFormat
		}
		dict, err := dictFor(uint64(binary.LittleEndian.Uint32(f.props[1:5])), 64<<10)
		if err != nil {
			return nil, err
		}
		// The classic .lzma header the package reads: the properties byte,
		// the dictionary, the unpacked size. 7z keeps the first two in the
		// coder's properties and the size in the folder.
		hdr := make([]byte, lzma.HeaderLen)
		hdr[0] = f.props[0]
		binary.LittleEndian.PutUint32(hdr[1:5], uint32(dict))
		binary.LittleEndian.PutUint64(hdr[5:13], f.unpackSize)
		rd, err := lzma.ReaderConfig{DictCap: dict}.NewReader(io.MultiReader(bytes.NewReader(hdr), src))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errSevenZipFormat, err)
		}
		dec = rd
	case bytes.Equal(f.method, []byte{0x21}):
		if len(f.props) != 1 || f.props[0] > 40 {
			return nil, errSevenZipFormat
		}
		p := f.props[0]
		declared := uint64(0xFFFFFFFF)
		if p < 40 {
			declared = uint64(2|p&1) << (p/2 + 11)
		}
		dict, err := dictFor(declared, 2<<20+64<<10)
		if err != nil {
			return nil, err
		}
		rd, err := lzma.Reader2Config{DictCap: dict}.NewReader2(src)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errSevenZipFormat, err)
		}
		dec = rd
	default:
		return nil, errSevenZipUnsupported
	}
	if len(f.filters) == 0 {
		return io.LimitReader(dec, int64(limit)), nil
	}
	out := io.LimitReader(dec, int64(inner))
	for _, c := range f.filters {
		conv, pc, _ := sevenZipBranchFor(c)
		out = newSevenZipBranchReader(out, conv, pc)
	}
	return io.LimitReader(out, int64(limit)), nil
}

// ── Branch filters ──────────────────────────────────────────────────────────

// sevenZipBranchAhead is how far past a read the coder under a branch filter
// decodes, so an instruction the read's end cuts is converted whole.
const sevenZipBranchAhead = 16

// sevenZipBranchBuf is the buffer a branch filter converts in.
const sevenZipBranchBuf = 64 << 10

// sevenZipBranchFor is a branch filter coder's decoding conversion and the
// stream position it starts at (its 4-byte property, 0 without one); ok is
// false for any other coder, or properties this reader does not take. These
// are the filters 7-Zip puts before LZMA/LZMA2 for ARM programs: ARM64 (0A),
// ARM (03 03 05 01) and ARM-Thumb (03 03 07 01) — the same conversions as
// xz's, checked against liblzma for ARM and ARM-Thumb.
func sevenZipBranchFor(c sevenZipCoder) (func(b []byte, pc uint32) int, uint32, bool) {
	var conv func(b []byte, pc uint32) int
	switch {
	case bytes.Equal(c.id, []byte{0x0A}):
		conv = sevenZipARM64
	case bytes.Equal(c.id, []byte{0x03, 0x03, 0x01, 0x03}):
		// x86 BCJ carries state from one instruction candidate to the next,
		// so it is a fresh closure per folder read.
		conv = sevenZipBCJ()
	case bytes.Equal(c.id, []byte{0x03, 0x03, 0x05, 0x01}):
		conv = sevenZipARM
	case bytes.Equal(c.id, []byte{0x03, 0x03, 0x07, 0x01}):
		conv = sevenZipARMT
	default:
		return nil, 0, false
	}
	switch len(c.props) {
	case 0:
		return conv, 0, true
	case 4:
		return conv, binary.LittleEndian.Uint32(c.props), true
	}
	return nil, 0, false
}

// sevenZipARM64 undoes the ARM64 filter over b, whose first byte is at stream
// position pc: the targets of BL (26 bits) and of ADRP (21 bits, the ones
// within ±512 MiB) go back from absolute to relative. It returns the bytes it
// is done with, whole 4-byte instructions.
func sevenZipARM64(b []byte, pc uint32) int {
	i := 0
	for ; i+4 <= len(b); i += 4 {
		instr := binary.LittleEndian.Uint32(b[i:])
		at := pc + uint32(i)
		switch {
		case instr>>26 == 0x25: // BL
			rel := instr - at>>2
			v := 0x94000000 | rel&0x03FFFFFF
			binary.LittleEndian.PutUint32(b[i:], v)
		case instr&0x9F000000 == 0x90000000: // ADRP
			src := instr>>29&3 | instr>>3&0x001FFFFC
			if near := src + 0x00020000; near&0x001C0000 != 0 {
				continue
			}
			dest := src - at>>12
			v := instr & 0x9000001F
			v |= (dest & 3) << 29
			v |= (dest & 0x0003FFFC) << 3
			v |= -(dest & 0x00020000) & 0x00E00000
			binary.LittleEndian.PutUint32(b[i:], v)
		}
	}
	return i
}

// sevenZipBCJAllowed and sevenZipBCJBit are xz's x86 filter tables: whether a
// candidate is safe after the mask, and which byte of the target must still
// look like a call's high byte.
var (
	sevenZipBCJAllowed = [8]bool{true, true, true, false, true, false, false, false}
	sevenZipBCJBit     = [8]uint32{0, 1, 2, 2, 3, 3, 3, 3}
)

// sevenZipBCJMSByte reports the high byte of a relative call target, which is
// 0x00 or 0xFF for one that stays inside this stream.
func sevenZipBCJMSByte(b byte) bool { return b == 0x00 || b == 0xFF }

// sevenZipBCJ returns the x86 BCJ filter's inverse, the same conversion xz's
// FILTER_X86 and 7-Zip's BCJ use. It keeps its state across reads because a
// candidate is judged by what preceded it; the reader feeds the unconverted
// tail back in, so pc is the stream position of b[0] every time.
func sevenZipBCJ() func(b []byte, pc uint32) int {
	var prevMask, prevPos uint32
	started := false
	return func(b []byte, pc uint32) int {
		if len(b) < 5 {
			return 0
		}
		if !started {
			prevPos = pc - 5
			started = true
		}
		i := 0
		limit := len(b) - 5
		for i <= limit {
			if op := b[i]; op != 0xE8 && op != 0xE9 {
				i++
				continue
			}
			offset := pc + uint32(i) - prevPos
			prevPos = pc + uint32(i)
			if offset > 5 {
				prevMask = 0
			} else {
				for k := uint32(0); k < offset; k++ {
					prevMask &= 0x77
					prevMask <<= 1
				}
			}
			ms := b[i+4]
			if sevenZipBCJMSByte(ms) && sevenZipBCJAllowed[(prevMask>>1)&7] && (prevMask>>1) < 0x10 {
				src := uint32(ms)<<24 | uint32(b[i+3])<<16 | uint32(b[i+2])<<8 | uint32(b[i+1])
				var dest uint32
				for {
					dest = src - (pc + uint32(i) + 5)
					if prevMask == 0 {
						break
					}
					bit := sevenZipBCJBit[prevMask>>1]
					ms = byte(dest >> (24 - bit*8))
					if !sevenZipBCJMSByte(ms) {
						break
					}
					if bit == 0 {
						src = dest
					} else {
						src = dest ^ ((uint32(1) << (32 - bit*8)) - 1)
					}
				}
				b[i+4] = byte(^(((dest >> 24) & 1) - 1))
				b[i+3] = byte(dest >> 16)
				b[i+2] = byte(dest >> 8)
				b[i+1] = byte(dest)
				i += 5
				prevMask = 0
			} else {
				i++
				prevMask |= 1
				if sevenZipBCJMSByte(ms) {
					prevMask |= 0x10
				}
			}
		}
		return i
	}
}

// sevenZipARM undoes the ARM filter: BL's 24-bit target, in 4-byte steps.
func sevenZipARM(b []byte, pc uint32) int {
	i := 0
	for ; i+4 <= len(b); i += 4 {
		if b[i+3] != 0xEB {
			continue
		}
		src := uint32(b[i+2])<<16 | uint32(b[i+1])<<8 | uint32(b[i])
		at := pc + uint32(i) + 8
		dest := src<<2 - at
		dest >>= 2
		b[i+2] = byte(dest >> 16)
		b[i+1] = byte(dest >> 8)
		b[i] = byte(dest)
	}
	return i
}

// sevenZipARMT undoes the ARM-Thumb filter: a BL pair's 22-bit target, in
// 2-byte steps.
func sevenZipARMT(b []byte, pc uint32) int {
	i := 0
	for ; i+4 <= len(b); i += 2 {
		if b[i+1]&0xF8 != 0xF0 || b[i+3]&0xF8 != 0xF8 {
			continue
		}
		hi := uint32(b[i+1]) & 7
		lo := uint32(b[i+3]) & 7
		src := hi<<19 | uint32(b[i])<<11 | lo<<8 | uint32(b[i+2])
		at := pc + uint32(i) + 4
		dest := src<<1 - at
		dest >>= 1
		b[i+1] = 0xF0 | byte(dest>>19&7)
		b[i] = byte(dest >> 11)
		b[i+3] = 0xF8 | byte(dest>>8&7)
		b[i+2] = byte(dest)
		i += 2
	}
	return i
}

// sevenZipBranchReader undoes a branch filter over src. An instruction a read
// cuts waits for the rest; the stream's last bytes, too few for one, come
// out as they are.
type sevenZipBranchReader struct {
	src  io.Reader
	conv func(b []byte, pc uint32) int
	pc   uint32 // the stream position of buf[0], with the filter's start
	buf  []byte
	// buf[start:ready] is converted and not yet returned; buf[:end] came
	// from src; err is src's error once it gave one.
	start, ready, end int
	err               error
}

func newSevenZipBranchReader(src io.Reader, conv func(b []byte, pc uint32) int, pc uint32) *sevenZipBranchReader {
	return &sevenZipBranchReader{src: src, conv: conv, pc: pc, buf: make([]byte, sevenZipBranchBuf)}
}

func (r *sevenZipBranchReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.start == r.ready {
		if r.err != nil {
			return 0, r.err
		}
		// The bytes not converted yet go to the front, then more come.
		n := copy(r.buf, r.buf[r.ready:r.end])
		r.start, r.ready, r.end = 0, 0, n
		for tries := 0; r.err == nil; tries++ {
			m, err := r.src.Read(r.buf[r.end:])
			r.end += m
			switch {
			case err != nil:
				r.err = err
			case m == 0 && tries >= 100:
				r.err = io.ErrNoProgress
			}
			if m > 0 {
				break
			}
		}
		done := r.conv(r.buf[:r.end], r.pc)
		r.pc += uint32(done)
		r.ready = done
		if r.err != nil {
			r.ready = r.end
		}
	}
	n := copy(p, r.buf[r.start:r.ready])
	r.start += n
	return n, nil
}

// sevenZipMethodNames names 7z methods for messages.
var sevenZipMethodNames = map[string]string{
	"\x00": "Copy", "\x03\x01\x01": "LZMA", "\x21": "LZMA2", "\x03": "Delta",
	"\x03\x03\x01\x03": "BCJ", "\x03\x03\x01\x1b": "BCJ2", "\x03\x03\x02\x05": "PPC",
	"\x03\x03\x04\x01": "IA64", "\x03\x03\x05\x01": "ARM", "\x03\x03\x07\x01": "ARMT",
	"\x03\x03\x08\x05": "SPARC", "\x0a": "ARM64", "\x0b": "RISCV", "\x03\x04\x01": "PPMd",
	"\x04\x01\x08": "Deflate", "\x04\x01\x09": "Deflate64", "\x04\x02\x02": "BZip2",
	"\x06\xf1\x07\x01": "AES",
}

// sevenZipFolderMethods names a folder's coders for a message, an unknown one
// by its id: a chain from the filter that makes the output to the coder that
// reads the packed stream ("ARM64 + LZMA2", as 7-Zip lists it; 7-Zip stores
// LZMA2 first), any other folder's in the order stored.
func sevenZipFolderMethods(f sevenZipFolder) string {
	coders := f.coders
	if f.simple {
		coders = make([]sevenZipCoder, 0, len(f.filters)+1)
		for k := len(f.filters) - 1; k >= 0; k-- {
			coders = append(coders, f.filters[k])
		}
		coders = append(coders, sevenZipCoder{id: f.method})
	}
	if len(coders) == 0 {
		return "no coder"
	}
	names := make([]string, 0, len(coders))
	for _, c := range coders {
		name, ok := sevenZipMethodNames[string(c.id)]
		if !ok {
			name = fmt.Sprintf("method %x", c.id)
		}
		names = append(names, name)
	}
	return strings.Join(names, " + ")
}

// readHead returns the first n bytes of entry i (fewer when the file is
// shorter), decoding its folder from the start.
func (a *sevenZipArchive) readHead(r io.ReaderAt, i, n int) ([]byte, error) {
	if i < 0 || i >= len(a.Entries) || n < 0 {
		return nil, errSevenZipFormat
	}
	e := a.Entries[i]
	want := min(uint64(n), e.Size, sevenZipMaxRead)
	if e.folder < 0 || want == 0 {
		return []byte{}, nil
	}
	if e.offset > sevenZipMaxDecode || want > sevenZipMaxDecode-e.offset {
		return nil, errSevenZipTooFar
	}
	rd, err := a.streams.folderReader(r, a.size, e.folder, e.offset+want)
	if err != nil {
		return nil, err
	}
	if e.offset > 0 {
		if _, err := io.CopyN(io.Discard, rd, int64(e.offset)); err != nil {
			return nil, errSevenZipFormat
		}
	}
	out := make([]byte, want)
	got, err := io.ReadFull(rd, out)
	if err != nil && got == 0 {
		return nil, errSevenZipFormat
	}
	return out[:got], nil
}

// ── What the library asks ───────────────────────────────────────────────────

// sevenZipGame is the one game in an archive: the single file that is not a
// directory or a read-me, scan or picture (rommZipIgnored), as a zip's is
// found (rommDetectZip). -1 when there is none or several.
func (a *sevenZipArchive) sevenZipGame() int {
	game := -1
	for i, e := range a.Entries {
		if e.Dir {
			continue
		}
		if rommZipIgnored[strings.ToLower(filepath.Ext(e.Name))] {
			continue
		}
		if game >= 0 {
			return -1
		}
		game = i
	}
	return game
}

// sevenZipROMCRC is the CRC32 of the one game in the .7z at path, from its
// directory: nothing of the game itself is decoded.
func sevenZipROMCRC(path string) (uint32, bool) {
	a, err := sevenZipOpenFile(path)
	if err != nil {
		return 0, false
	}
	i := a.sevenZipGame()
	if i < 0 || !a.Entries[i].HasCRC {
		return 0, false
	}
	return a.Entries[i].CRC, true
}

// rommDetect7z is rommDetectZip for a .7z (the ROM manager): the single game
// in its directory, its console by extension, its CRC32, and — when the game
// is the first file of its block, so the read stays short — its header.
func rommDetect7z(path string) rommDetection {
	f, err := os.Open(path)
	if err != nil {
		return rommDetection{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return rommDetection{}
	}
	a, err := sevenZipOpen(f, info.Size())
	if err != nil {
		return rommDetection{}
	}
	names := make([]string, 0, len(a.Entries))
	for _, e := range a.Entries {
		if !e.Dir {
			names = append(names, strings.ToLower(filepath.Base(e.Name)))
		}
	}
	if rommNeoGeoSet(names) {
		return rommDetection{System: "neogeo", Strong: true, How: "a Neo Geo ROM set"}
	}
	i := a.sevenZipGame()
	if i < 0 {
		return rommDetection{}
	}
	e := a.Entries[i]
	det := rommDetection{CRC: e.CRC, HasCRC: e.HasCRC}
	ext := strings.ToLower(filepath.Ext(e.Name))
	if id, ok := rommExtSystems[ext]; ok {
		det.System, det.Strong, det.How = id, true, "a "+ext+" file in the 7z"
	}
	if ext == ".zip" || ext == ".7z" || ext == ".chd" || e.offset != 0 {
		return det
	}
	limit := rommHeadSize
	if rommDiscExts[ext] {
		limit = 192 << 10
	}
	head, err := a.readHead(f, i, limit)
	if err != nil {
		return det
	}
	if id := rommSniffCart(head, int64(e.Size)); id != "" {
		det.System, det.Strong, det.How = id, true, rommSystemLabel(id)+" header in the 7z"
		return det
	}
	if rommDiscExts[ext] {
		if id := rommSniffDisc(bytes.NewReader(head), int64(len(head))); id != "" {
			det.System, det.Strong, det.How = id, true, rommSystemLabel(id)+" disc in the 7z"
		}
	}
	return det
}

// sevenZipCartHead is the first n bytes of the single file in the .7z at path
// whose extension is one of exts, when no other game file is beside it and it
// is at most maxSize bytes. nil otherwise, or when its packing is one this
// reader does not decode.
func sevenZipCartHead(path string, exts []string, maxEntries int, maxSize uint64, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	a, err := sevenZipOpen(f, info.Size())
	if err != nil || len(a.Entries) > maxEntries {
		return nil
	}
	i := a.sevenZipGame()
	if i < 0 || a.Entries[i].Size > maxSize {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(a.Entries[i].Name))
	match := false
	for _, want := range exts {
		if ext == want {
			match = true
		}
	}
	if !match {
		return nil
	}
	head, err := a.readHead(f, i, n)
	if err != nil {
		if !errors.Is(err, errSevenZipUnsupported) {
			logDebug("library", "%s: %v", filepath.Base(path), err)
		}
		return nil
	}
	return head
}
