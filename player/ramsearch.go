package main

// ramsearch.go — the value-search core behind a RAM scanner.
//
// A scan looks for every aligned little-endian integer of a given width that
// equals a value across the writable memory of one process. The first scan
// walks the whole writable map; each later "narrow" re-reads only the
// surviving addresses and keeps the ones that still match. This file is
// platform-neutral: the process-memory access sits behind memReader, and the
// Linux implementation lives in ramsearch_linux.go.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ramMaxCandidates bounds memory use. A first scan of a small value can match
// a huge number of addresses; past this the user should search for a more
// specific value instead of holding millions of entries on a handheld.
const ramMaxCandidates = 1 << 20

// ramChunkSize is the read size. It is a multiple of every supported width, so
// every aligned value lies wholly inside one chunk and none straddles a boundary.
const ramChunkSize = 64 * 1024

// memRegion is one contiguous mapped range of a target process.
type memRegion struct {
	Start, End uint64
}

// memReader gives read access to another process's memory.
type memReader interface {
	// Regions lists the writable ranges worth scanning.
	Regions() ([]memRegion, error)
	// ReadAt fills buf from addr and returns how many bytes were read.
	ReadAt(addr uint64, buf []byte) (int, error)
}

// memWriter stores bytes back into another process's memory. It is separate
// from memReader so a read-only source (the test fake, a future remote debugger
// that only reads) is still a valid memReader.
type memWriter interface {
	// WriteAt stores buf at addr.
	WriteAt(addr uint64, buf []byte) error
}

// ramAccess is one open connection to a target process: scan it, edit it, and
// release it when the session ends.
type ramAccess interface {
	memReader
	memWriter
	Close() error
}

// ramCandidate is one address whose value still matches the search.
type ramCandidate struct {
	Addr  uint64
	Value uint64
}

// ramSearch holds the surviving candidates for one value width.
type ramSearch struct {
	Width      int
	Candidates []ramCandidate
}

// ramValidWidth reports whether width is one the search supports.
func ramValidWidth(width int) bool {
	return width == 1 || width == 2 || width == 4 || width == 8
}

// ramMask keeps only the low width bytes of v.
func ramMask(width int, v uint64) uint64 {
	if width >= 8 {
		return v
	}
	return v & (uint64(1)<<(uint(width)*8) - 1)
}

// ramDecode reads a little-endian value of the given width from b.
func ramDecode(width int, b []byte) uint64 {
	switch width {
	case 1:
		return uint64(b[0])
	case 2:
		return uint64(binary.LittleEndian.Uint16(b))
	case 4:
		return uint64(binary.LittleEndian.Uint32(b))
	default:
		return binary.LittleEndian.Uint64(b)
	}
}

// ramFirstScan finds every aligned address whose width-byte value equals
// value. It returns an error if the match count exceeds ramMaxCandidates.
func ramFirstScan(mem memReader, width int, value uint64) (*ramSearch, error) {
	if !ramValidWidth(width) {
		return nil, fmt.Errorf("unsupported width %d", width)
	}
	regions, err := mem.Regions()
	if err != nil {
		return nil, err
	}
	want := ramMask(width, value)
	search := &ramSearch{Width: width}
	buf := make([]byte, ramChunkSize)

	for _, r := range regions {
		for addr := r.Start; addr < r.End; addr += ramChunkSize {
			n := uint64(ramChunkSize)
			if r.End-addr < n {
				n = r.End - addr
			}
			got, _ := mem.ReadAt(addr, buf[:n])
			for off := 0; off+width <= got; off += width {
				if ramDecode(width, buf[off:off+width]) != want {
					continue
				}
				if len(search.Candidates) >= ramMaxCandidates {
					return nil, errors.New("too many matches; search for a more specific value")
				}
				search.Candidates = append(search.Candidates, ramCandidate{
					Addr:  addr + uint64(off),
					Value: want,
				})
			}
		}
	}
	return search, nil
}

// ramNarrow re-reads each candidate and keeps the ones that now equal value.
// Addresses that can no longer be read are dropped.
func ramNarrow(mem memReader, s *ramSearch, value uint64) {
	want := ramMask(s.Width, value)
	buf := make([]byte, s.Width)
	kept := s.Candidates[:0]
	for _, c := range s.Candidates {
		got, _ := mem.ReadAt(c.Addr, buf)
		if got != s.Width || ramDecode(s.Width, buf) != want {
			continue
		}
		kept = append(kept, ramCandidate{Addr: c.Addr, Value: want})
	}
	s.Candidates = kept
}

// ramEncode stores v as a little-endian value of the given width in b.
func ramEncode(width int, v uint64, b []byte) {
	switch width {
	case 1:
		b[0] = byte(v)
	case 2:
		binary.LittleEndian.PutUint16(b, uint16(v))
	case 4:
		binary.LittleEndian.PutUint32(b, uint32(v))
	default:
		binary.LittleEndian.PutUint64(b, v)
	}
}

// ramReadValue reads one value of the given width at addr.
func ramReadValue(mem memReader, width int, addr uint64) (uint64, error) {
	if !ramValidWidth(width) {
		return 0, fmt.Errorf("unsupported width %d", width)
	}
	buf := make([]byte, width)
	got, err := mem.ReadAt(addr, buf)
	if err != nil {
		return 0, err
	}
	if got != width {
		return 0, fmt.Errorf("short read at %s", ramFormatAddr(addr))
	}
	return ramDecode(width, buf), nil
}

// ramWrite stores value at addr, truncated to the search width.
func ramWrite(mem memWriter, width int, addr, value uint64) error {
	if !ramValidWidth(width) {
		return fmt.Errorf("unsupported width %d", width)
	}
	buf := make([]byte, width)
	ramEncode(width, ramMask(width, value), buf)
	return mem.WriteAt(addr, buf)
}

// ramParseValue parses a number the way the value field accepts it: decimal,
// hexadecimal with a 0x prefix, or negative for the two's complement a game
// stores after a counter underflows.
func ramParseValue(text string) (uint64, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0, false
	}
	base := 10
	negative := false
	switch {
	case strings.HasPrefix(s, "0x"), strings.HasPrefix(s, "0X"):
		s, base = s[2:], 16
	case strings.HasPrefix(s, "-"):
		negative, s = true, s[1:]
	}
	if s == "" {
		return 0, false
	}
	raw, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		raw = uint64(-int64(raw))
	}
	return raw, true
}

// ramFormatAddr renders an address the way the panel shows it.
func ramFormatAddr(addr uint64) string {
	return fmt.Sprintf("0x%08X", addr)
}

// parseWritableMaps reads /proc/<pid>/maps content and keeps readable,
// writable mappings. Each line looks like:
//
//	7f3a1000-7f3a2000 rw-p 00000000 00:00 0      [heap]
//
// It is plain text handling, so it lives here rather than in the Linux file.
func parseWritableMaps(r io.Reader) ([]memRegion, error) {
	var regions []memRegion
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		perms := fields[1]
		if len(perms) < 2 || perms[0] != 'r' || perms[1] != 'w' {
			continue
		}
		span := strings.SplitN(fields[0], "-", 2)
		if len(span) != 2 {
			continue
		}
		start, err1 := strconv.ParseUint(span[0], 16, 64)
		end, err2 := strconv.ParseUint(span[1], 16, 64)
		if err1 != nil || err2 != nil || end <= start {
			continue
		}
		regions = append(regions, memRegion{Start: start, End: end})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return regions, nil
}
