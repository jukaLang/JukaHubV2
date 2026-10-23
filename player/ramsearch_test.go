package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// fakeMem is an in-memory memReader and memWriter. Each region is a byte slice
// mapped at a base address; reads outside every region are short or fail. It
// also records Close so the Memory panel's detach path is testable without a
// real process.
type fakeMem struct {
	base    uint64
	data    []byte
	regions []memRegion
	closed  bool
}

func (f *fakeMem) Regions() ([]memRegion, error) {
	return f.regions, nil
}

func (f *fakeMem) ReadAt(addr uint64, buf []byte) (int, error) {
	if addr < f.base || addr >= f.base+uint64(len(f.data)) {
		return 0, nil
	}
	n := copy(buf, f.data[addr-f.base:])
	return n, nil
}

// WriteAt stores buf in the image, refusing writes outside it.
func (f *fakeMem) WriteAt(addr uint64, buf []byte) error {
	if addr < f.base || addr+uint64(len(buf)) > f.base+uint64(len(f.data)) {
		return fmt.Errorf("write outside the image at %s", ramFormatAddr(addr))
	}
	copy(f.data[addr-f.base:], buf)
	return nil
}

// Close records the release.
func (f *fakeMem) Close() error {
	f.closed = true
	return nil
}

func newFakeMem(size int) *fakeMem {
	const base = 0x10000
	return &fakeMem{
		base:    base,
		data:    make([]byte, size),
		regions: []memRegion{{Start: base, End: base + uint64(size)}},
	}
}

func TestRamFirstScanFindsAlignedValues(t *testing.T) {
	mem := newFakeMem(64)
	binary.LittleEndian.PutUint32(mem.data[8:], 999)
	binary.LittleEndian.PutUint32(mem.data[24:], 999)
	binary.LittleEndian.PutUint32(mem.data[40:], 5)

	s, err := ramFirstScan(mem, 4, 999)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(s.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(s.Candidates))
	}
	if s.Candidates[0].Addr != mem.base+8 || s.Candidates[1].Addr != mem.base+24 {
		t.Fatalf("unexpected addresses: %+v", s.Candidates)
	}
}

func TestRamFirstScanFindsValueAtChunkEdges(t *testing.T) {
	// The last aligned 4-byte slot of the first chunk and the first slot of the
	// second must both be found.
	mem := newFakeMem(ramChunkSize * 2)
	last := ramChunkSize - 4
	first := ramChunkSize
	binary.LittleEndian.PutUint32(mem.data[last:], 0xCAFEBABE)
	binary.LittleEndian.PutUint32(mem.data[first:], 0xCAFEBABE)

	s, err := ramFirstScan(mem, 4, 0xCAFEBABE)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(s.Candidates) != 2 || s.Candidates[0].Addr != mem.base+uint64(last) || s.Candidates[1].Addr != mem.base+uint64(first) {
		t.Fatalf("want matches at chunk edges, got %+v", s.Candidates)
	}
}

func TestRamFirstScanRejectsBadWidth(t *testing.T) {
	if _, err := ramFirstScan(newFakeMem(16), 3, 1); err == nil {
		t.Fatal("width 3 should be rejected")
	}
}

func TestRamFirstScanLimitsCandidates(t *testing.T) {
	// Zero memory matches value 0 at every aligned offset, which exceeds the cap
	// for a large enough region.
	mem := newFakeMem(ramMaxCandidates + 16)
	if _, err := ramFirstScan(mem, 1, 0); err == nil {
		t.Fatal("expected the too-many-matches error")
	}
}

func TestRamNarrowKeepsOnlyStillMatching(t *testing.T) {
	mem := newFakeMem(32)
	binary.LittleEndian.PutUint32(mem.data[0:], 100)
	binary.LittleEndian.PutUint32(mem.data[8:], 100)

	s, err := ramFirstScan(mem, 4, 100)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	// The game changes one value, so only the other should survive.
	binary.LittleEndian.PutUint32(mem.data[0:], 90)

	ramNarrow(mem, s, 100)
	if len(s.Candidates) != 1 || s.Candidates[0].Addr != mem.base+8 {
		t.Fatalf("narrow kept wrong set: %+v", s.Candidates)
	}
}

func TestRamNarrowDropsUnreadableAddresses(t *testing.T) {
	mem := newFakeMem(16)
	binary.LittleEndian.PutUint32(mem.data[4:], 42)
	s, err := ramFirstScan(mem, 4, 42)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	s.Candidates = append(s.Candidates, ramCandidate{Addr: mem.base + 1000, Value: 42})

	ramNarrow(mem, s, 42)
	if len(s.Candidates) != 1 {
		t.Fatalf("unreadable address kept: %+v", s.Candidates)
	}
}

func TestParseWritableMapsKeepsOnlyReadWrite(t *testing.T) {
	maps := strings.Join([]string{
		"00400000-00401000 r-xp 00000000 08:01 1234 /usr/bin/game",
		"00600000-00601000 rw-p 00000000 08:01 1234 /usr/bin/game",
		"7f000000-7f002000 rw-p 00000000 00:00 0    [heap]",
		"7ffd0000-7ffe0000 r--p 00000000 00:00 0    [stack]",
		"malformed line",
	}, "\n")

	regions, err := parseWritableMaps(strings.NewReader(maps))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []memRegion{{Start: 0x600000, End: 0x601000}, {Start: 0x7f000000, End: 0x7f002000}}
	if len(regions) != len(want) {
		t.Fatalf("want %d regions, got %+v", len(want), regions)
	}
	for i := range want {
		if regions[i] != want[i] {
			t.Fatalf("region %d: want %+v, got %+v", i, want[i], regions[i])
		}
	}
}
