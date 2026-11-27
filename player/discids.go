package main

// discids.go — the disc ID a save is named after, read from the game itself.
//
// PSP SAVEDATA folders and PPSSPP states are named after the disc's ID
// (ULUS10041DATA, ULUS10041_0.ppst), not after the ROM's file name, so the
// save handoff cannot find them by stem. The ID lives in PSP_GAME/PARAM.SFO
// inside the ISO, in the PARAM.SFO a PBP carries, or in the same ISO inside
// PSP's CISO. GameCube and Wii memory-card exports (GCI) name the disc's
// six-character ID in the file name and in the save's header; the disc header
// carries it too.
//
// Every read here is bounded and local. A damaged or unsupported image simply
// has no ID: the handoff then falls back to the file-name rules it already
// had, never to a guess at another game's saves.

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// discIDReadMax bounds one metadata read from an image.
	discIDReadMax = 512 << 10
	// pspISOdirMax bounds a directory read: PSP_GAME and the root hold a few
	// dozen entries.
	pspISOdirMax = 256 << 10
	// csoIndexMax bounds a CISO block table.
	csoIndexMax = 16 << 20
)

// pspGameID reads a PSP game's disc ID from a PBP, ISO or CISO. It returns ""
// when the file is not one of those, has no PARAM.SFO, or cannot be read.
func pspGameID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return ""
	}
	head := make([]byte, 0x28)
	if n, _ := f.ReadAt(head, 0); n < 4 {
		return ""
	}
	if bytes.HasPrefix(head, []byte("\x00PBP")) {
		if len(head) < 0x0C {
			return ""
		}
		param := int64(binary.LittleEndian.Uint32(head[0x08:0x0C]))
		return pspParamSFOID(f, param, info.Size())
	}
	if bytes.HasPrefix(head, []byte("CISO")) && len(head) >= 0x18 {
		reader, err := newCSOReaderAt(f, info.Size())
		if err != nil {
			return ""
		}
		return pspISOID(reader)
	}
	return pspISOID(f)
}

// pspISOID finds PSP_GAME/PARAM.SFO in an ISO 9660 image and reads DISC_ID.
func pspISOID(r io.ReaderAt) string {
	layout := rommDiscLayout{sector: 2048}
	pvd, err := rommReadSector(r, layout, 16)
	if err != nil || pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return ""
	}
	root := pvd[156:190]
	rootLBA := int64(binary.LittleEndian.Uint32(root[2:6]))
	rootSize := int64(binary.LittleEndian.Uint32(root[10:14]))
	if rootLBA <= 0 || rootSize <= 0 {
		return ""
	}
	psp := pspISOFindDir(r, layout, rootLBA, rootSize, "PSP_GAME", true)
	if psp == nil {
		return ""
	}
	sfo := pspISOFindDir(r, layout, psp.lba, psp.size, "PARAM.SFO", false)
	if sfo == nil {
		return ""
	}
	size := sfo.size
	if size <= 0 || size > discIDReadMax {
		size = discIDReadMax
	}
	data := make([]byte, size)
	n, _ := r.ReadAt(data, sfo.lba*2048)
	if int64(n) != size {
		return ""
	}
	return parseParamSFOID(data)
}

// pspISOEntry is one ISO 9660 directory entry.
type pspISOEntry struct {
	name string
	lba  int64
	size int64
	dir  bool
}

// pspISOFindDir walks one directory for name. It reads at most pspISOdirMax
// bytes and never follows a directory extent outside the image.
func pspISOFindDir(r io.ReaderAt, layout rommDiscLayout, lba, size int64, name string, wantDir bool) *pspISOEntry {
	if size <= 0 {
		return nil
	}
	if size > pspISOdirMax {
		size = pspISOdirMax
	}
	want := strings.ToUpper(strings.TrimSuffix(name, ";1"))
	for read := int64(0); read < size; read += 2048 {
		sec, err := rommReadSector(r, layout, lba+read/2048)
		if err != nil {
			return nil
		}
		for pos := 0; pos < len(sec); {
			recLen := int(sec[pos])
			if recLen == 0 {
				break // padding to the end of the sector
			}
			if recLen < 34 || pos+recLen > len(sec) {
				return nil
			}
			nameLen := int(sec[pos+32])
			if 33+nameLen > recLen {
				return nil
			}
			if nameLen > 0 {
				raw := string(sec[pos+33 : pos+33+nameLen])
				entryName := strings.ToUpper(strings.TrimSuffix(raw, ";1"))
				entry := &pspISOEntry{
					name: entryName,
					lba:  int64(binary.LittleEndian.Uint32(sec[pos+2 : pos+6])),
					size: int64(binary.LittleEndian.Uint32(sec[pos+10 : pos+14])),
					dir:  sec[pos+25]&2 != 0,
				}
				if entryName == want && entry.dir == wantDir {
					return entry
				}
			}
			pos += recLen
		}
	}
	return nil
}

// pspParamSFOID reads PARAM.SFO at offset in a PBP.
func pspParamSFOID(r io.ReaderAt, offset, size int64) string {
	if offset <= 0 || offset >= size {
		return ""
	}
	want := size - offset
	if want > discIDReadMax {
		want = discIDReadMax
	}
	data := make([]byte, want)
	n, _ := r.ReadAt(data, offset)
	if n < 20 {
		return ""
	}
	return parseParamSFOID(data[:n])
}

// parseParamSFOID reads DISC_ID (or TITLE_ID) from a PARAM.SFO. The format is
// Sony's: a 20-byte header, 16-byte entries, a key table and a data table.
func parseParamSFOID(data []byte) string {
	if len(data) < 20 || binary.BigEndian.Uint32(data[0:4]) != 0x46535000 {
		return ""
	}
	keyTable := int(binary.LittleEndian.Uint32(data[8:12]))
	dataTable := int(binary.LittleEndian.Uint32(data[12:16]))
	count := int(binary.LittleEndian.Uint32(data[16:20]))
	if count <= 0 || count > 256 || keyTable < 20 || 20+count*16 > len(data) {
		return ""
	}
	for i := 0; i < count; i++ {
		entry := data[20+i*16 : 36+i*16]
		keyOff := int(binary.LittleEndian.Uint16(entry[0:2]))
		dataLen := int(binary.LittleEndian.Uint32(entry[4:8]))
		dataOff := int(binary.LittleEndian.Uint32(entry[12:16]))
		if keyOff < 0 || keyTable+keyOff >= len(data) {
			continue
		}
		keyEnd := keyTable + keyOff
		for keyEnd < len(data) && data[keyEnd] != 0 {
			keyEnd++
		}
		key := string(data[keyTable+keyOff : keyEnd])
		if key != "DISC_ID" && key != "TITLE_ID" {
			continue
		}
		if dataLen <= 0 || dataLen > 32 || dataTable < 0 || dataTable+dataOff+dataLen > len(data) {
			continue
		}
		id := strings.TrimSpace(strings.TrimRight(string(data[dataTable+dataOff:dataTable+dataOff+dataLen]), "\x00"))
		if pspDiscIDLooksValid(id) {
			return strings.ToUpper(id)
		}
	}
	return ""
}

// pspDiscIDLooksValid is four letters, five digits (ULUS10041, NPJH50332).
func pspDiscIDLooksValid(id string) bool {
	if len(id) != 9 {
		return false
	}
	for i, r := range id {
		if i < 4 {
			if r < 'A' || r > 'Z' {
				return false
			}
		} else if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// gcGameID reads the six-character disc ID of a GameCube or Wii image. The
// header is at the start of both; RVZ/WIA/GCZ are compressed and have no ID
// without their own readers, so they answer "".
func gcGameID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 0x20)
	if n, _ := f.ReadAt(head, 0); n != len(head) {
		return ""
	}
	// GameCube's magic is at 0x1C, Wii's at 0x18. Both put the ID first.
	if binary.BigEndian.Uint32(head[0x1C:0x20]) != 0xC2339F3D &&
		binary.BigEndian.Uint32(head[0x18:0x1C]) != 0x5D1C9EA3 {
		return ""
	}
	id := string(head[0:6])
	if len(id) != 6 {
		return ""
	}
	for _, r := range id {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return id
}

// ── CISO ─────────────────────────────────────────────────────────────────

// csoReaderAt reads a PSP CISO through io.ReaderAt. One block is kept: the
// ISO 9660 walk reads a directory sector after another, and a block holds
// one sector.
type csoReaderAt struct {
	f         io.ReaderAt
	total     int64
	blockSize int64
	align     uint
	index     []uint32
	cacheIdx  int64
	cache     []byte
}

// newCSOReaderAt reads a CISO header and its block table. The caller keeps f
// open for the reader's lifetime.
func newCSOReaderAt(f io.ReaderAt, size int64) (*csoReaderAt, error) {
	head := make([]byte, 0x18)
	if n, _ := f.ReadAt(head, 0); n != len(head) || string(head[0:4]) != "CISO" {
		return nil, errors.New("not a CISO")
	}
	headerSize := int64(binary.LittleEndian.Uint32(head[4:8]))
	total := int64(binary.LittleEndian.Uint64(head[8:16]))
	blockSize := int64(binary.LittleEndian.Uint32(head[16:20]))
	align := uint(head[21])
	if headerSize < 0x18 || headerSize >= size || total <= 0 || blockSize <= 0 || blockSize > 1<<20 {
		return nil, errors.New("bad CISO header")
	}
	blocks := (total + blockSize - 1) / blockSize
	if blocks <= 0 || blocks > csoIndexMax/4 {
		return nil, errors.New("CISO block table too large")
	}
	raw := make([]byte, (blocks+1)*4)
	if n, _ := f.ReadAt(raw, headerSize); int64(n) != int64(len(raw)) {
		return nil, errors.New("short CISO block table")
	}
	index := make([]uint32, blocks+1)
	for i := range index {
		index[i] = binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
	}
	return &csoReaderAt{f: f, total: total, blockSize: blockSize, align: align, index: index, cacheIdx: -1}, nil
}

// ReadAt implements io.ReaderAt.
func (c *csoReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= c.total {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > c.total-off {
		want = int(c.total - off)
	}
	done := 0
	for done < want {
		block := (off + int64(done)) / c.blockSize
		if block >= int64(len(c.index)-1) {
			break
		}
		data, err := c.blockData(block)
		if err != nil {
			return done, err
		}
		inBlock := int((off + int64(done)) % c.blockSize)
		n := copy(p[done:want], data[inBlock:])
		if n == 0 {
			break
		}
		done += n
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

// blockData returns one uncompressed block, from the cache when possible.
func (c *csoReaderAt) blockData(block int64) ([]byte, error) {
	if block == c.cacheIdx && c.cache != nil {
		return c.cache, nil
	}
	entry := c.index[block]
	next := c.index[block+1]
	start := int64(entry&0x7FFFFFFF) << c.align
	end := int64(next&0x7FFFFFFF) << c.align
	if end < start {
		return nil, errors.New("bad CISO block offset")
	}
	length := c.blockSize
	if left := c.total - block*c.blockSize; left < length {
		length = left
	}
	var data []byte
	if entry&0x80000000 != 0 {
		data = make([]byte, length)
		if n, _ := c.f.ReadAt(data, start); int64(n) != length {
			return nil, errors.New("short CISO stored block")
		}
	} else {
		if end-start > c.blockSize+1024 {
			return nil, errors.New("bad CISO compressed block")
		}
		raw := make([]byte, end-start)
		if n, _ := c.f.ReadAt(raw, start); int64(n) != int64(len(raw)) {
			return nil, errors.New("short CISO block")
		}
		zr := flate.NewReader(bytes.NewReader(raw))
		out, err := io.ReadAll(io.LimitReader(zr, c.blockSize+1))
		cerr := zr.Close()
		if err != nil || cerr != nil || int64(len(out)) != length {
			return nil, errors.New("bad CISO deflate block")
		}
		data = out
	}
	c.cacheIdx, c.cache = block, data
	return data, nil
}

// discIDForPath is the ID a handoff can use for a game, whichever console it
// belongs to. Pure apart from the reads.
func discIDForPath(path, system string) (psp, gc string) {
	switch canonicalCompatSystem(system) {
	case "PlayStation Portable":
		psp = pspGameID(path)
	case "GameCube":
		gc = gcGameID(path)
	}
	return psp, gc
}

// discIDStem is a lower-case name prefix test shared by the save matchers.
func discIDPrefix(name, id string) bool {
	if id == "" {
		return false
	}
	name = strings.ToLower(name)
	id = strings.ToLower(id)
	if name == id || strings.HasPrefix(name, id+".") || strings.HasPrefix(name, id+"-") || strings.HasPrefix(name, id+"_") {
		return true
	}
	// Dolphin's exported GCI names can put a card slot first (01-G8ME01-…).
	for _, part := range strings.Split(name, "-") {
		if part == id {
			return true
		}
	}
	return false
}

// discIDInPath reports an ID as one whole path segment's prefix, for PSP
// SAVEDATA folders (ULUS10041DATA) and state names (ULUS10041_0.ppst).
func discIDInPath(rel, id string) bool {
	if id == "" {
		return false
	}
	id = strings.ToLower(id)
	for _, seg := range strings.Split(strings.ToLower(filepath.ToSlash(rel)), "/") {
		if strings.HasPrefix(seg, id) {
			return true
		}
	}
	return false
}
