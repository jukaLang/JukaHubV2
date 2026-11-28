package main

import (
	"bytes"
	"encoding/base64"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// sevenZipFixture is a 519-byte archive built byte by byte with Python's lzma
// module: a directory "Docs", "readme.txt" (9 bytes) and "Tetris DX.gbc" (a
// 1 KiB Game Boy Color header) solid in one LZMA2 block, an empty
// "notes.txt", and the directory itself packed with LZMA (kEncodedHeader), the
// way 7-Zip writes it.
const sevenZipFixture = `N3q8ryccAATaMvEvxQEAAAAAAAAiAAAAAAAAAPejpVjgBAgBRl0ANBlJ7o3pBhLseUonF0KCHWT9
4qXbTstswFFNTFVeSLuB5+2UmAZ7nT/58riHzB4jmPRARXTQhWsxOxwLVa1IRQFpzWO/FrsiB++k
l8mb06Hl0g9CRSe2aUG0+QOM/ezwm723Y1lM8IwG7/gy2n70VZDAUd+eKpsbeGaSzGgszJ4ttpyf
IL8mPOJzbKLtsLXKgirIrKzaJhmtaWbhpHJ4j8c7M4U/2q9NPhSKv46woG5iXP/fbJa+nB3IR8KW
6R8cfusVcplEnlX1vFzLTQrCWLhJ4VuhQak7+xML3oMmBaj6s/bDmiKrzgCa6ZbI+UVG3VZFw0N+
srnnOKdpg55aV+yM/Wpx/l92fC90oxzEP7cBw/lbtaCINKOwx26pkO7sit2hKcJZHRK1OfYGkF9U
ZX6PeNfl/h+dLHS3GFSbZ1bcmuk42wAAAACBMweuD9U5jEyXJNP+s3AWsWSRo0JUL8bjmEttoiZI
pE9N0CVE+KwJWSZ0RZhjjGP54yObH2mrXCWId7C9x4Hu/AghPUpFZKR2PYi/pMQZYsdN+bLg7nwi
6xHvC3Zxtgpw7tzOnyCPfeskJ+xSFhmXdf0OBgAXBoFOAQl3AAcLAQABIwMBAQVdAAABAAyAiAoB
OGEMbAAA`

func TestSevenZipReader(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(string(bytes.ReplaceAll([]byte(sevenZipFixture), []byte("\n"), nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	a, err := sevenZipOpen(r, int64(len(data)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := []sevenZipEntry{
		{Name: "Docs", Dir: true},
		{Name: "readme.txt", Size: 9, CRC: 0x05E8B4A8, HasCRC: true},
		{Name: "Tetris DX.gbc", Size: 0x400, CRC: 0x46A8F69F, HasCRC: true},
		{Name: "notes.txt"},
	}
	if len(a.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(a.Entries), len(want))
	}
	for i, w := range want {
		e := a.Entries[i]
		if e.Name != w.Name || e.Dir != w.Dir || e.Size != w.Size || e.CRC != w.CRC || e.HasCRC != w.HasCRC {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
	}
	if got := a.sevenZipGame(); got != 2 {
		t.Fatalf("game entry = %d, want 2", got)
	}
	// The cartridge starts 9 bytes into the block: decoded past the read-me.
	head, err := a.readHead(r, 2, gbcHeaderEnd)
	if err != nil || len(head) != gbcHeaderEnd || !gbcCartColor(head) {
		t.Fatalf("cartridge header: %d bytes, err %v", len(head), err)
	}
	if text, err := a.readHead(r, 1, 100); err != nil || string(text) != "hello 7z\n" {
		t.Fatalf("readme = %q, %v", text, err)
	}

	path := filepath.Join(t.TempDir(), "Tetris DX.7z")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !gbcROMColor(path) {
		t.Error("gbcROMColor(.7z) = false")
	}
	if crcs := gmetaROMCRCs(path); len(crcs) != 1 || crcs[0] != "46A8F69F" {
		t.Errorf("gmetaROMCRCs(.7z) = %v", crcs)
	}
	if det := rommDetect7z(path); !det.HasCRC || det.CRC != 0x46A8F69F {
		t.Errorf("rommDetect7z = %+v", det)
	}

	// Cut short or damaged anywhere: an error or other bytes, never a panic.
	for n := 0; n < len(data); n++ {
		_, _ = sevenZipOpen(bytes.NewReader(data[:n]), int64(n))
	}
	for i := range data {
		bad := append([]byte(nil), data...)
		bad[i] ^= 0x5A
		br := bytes.NewReader(bad)
		if got, err := sevenZipOpen(br, int64(len(bad))); err == nil {
			for j := range got.Entries {
				_, _ = got.readHead(br, j, gbcHeaderEnd)
			}
		}
	}
}

// sevenZipBranchFixture is an 812-byte archive built byte by byte by
// tmp/raget-7z-fixture/mk7z_bra.py, its coders stored the way 7-Zip stores
// them (LZMA2 first, the filter second, one bind pair): retroarch.cfg in plain
// LZMA2; Tools/flash.exe under the x86 BCJ filter and ra32.trimui under the
// ARM filter, both packed by liblzma and read back by libarchive; ra64.trimui
// (3,002 bytes, not a whole number of instructions) and
// …/cores/a_libretro.so after it under the ARM64 filter, which 7-Zip 23 and
// later put on every 64-bit ARM program — the way TrimUI's Smart Pro packages
// carry RetroArch.
const sevenZipBranchFixture = `N3q8ryccAATV/CUEpAEAAAAAAABoAQAAAAAAAJT4uXrgAC8ALl0AOxpIsFdMufT9vUTSyZk8btxq
FLUBCEemEGPNcmaaNhLYVPQY46l+2y3+i96FAADgArsAE10ASDn8AA+HOTAXDrfZoC47e0lAAADg
BAEAH10AIAB8DsD9QeFSz1DRqwXnWgxEr3M3y8IjisI+zC9RAADgEZYBJF0AP5FFhGlDL3A2eCq6
HGxaBcXcm7JOLJn2OYY3AKRe/Z6SBNFaa9Tw6mgMXz7/UyEq/QqC4HlLpOjjHczWP2LyCZ73omwZ
56icUTZIKlsj/SYp9qGL1baTHwMJ6zd2cDDIHwFWL7Ag+iz39mNUaQpHVqB8tPW/TOzpxvBEvbPB
pzV26O2XHSJqV/D+6OcQQnB+gWG6LN9Wjxr8TjD0MzcDItrPhSgn7bPbmbbHDKrIJtSJGCL3L08m
iHi4YlY6i5/OJZ9ZJRIpZC3nR7z/LTE40SiOFH/hh4LdR+BCFd3Ja91x/G9EJKORndrzpon5Sy0b
BcvSbgULf77QUQq+x3UMUB84yJ0Zc6xXrBTLYfhoDi0cehiEw3gD5E9ojVekkaXVwYv7AAABBAYA
BAk2GyeBLAAHCwQAASEhARACISEBEAQDAwEDAQACISEBEAQDAwUBAQACISEBEAEKAQAMMIK8gryE
AoQCkZeRlwAIDQEBAQIJi7oKAXvDv9T4EVJMuujYvrwomfJ6n8QFAAAFBRGA+wBSAGUAdAByAG8A
QQByAGMAaAAvAHIAZQB0AHIAbwBhAHIAYwBoAC4AYwBmAGcAAABUAG8AbwBsAHMALwBmAGwAYQBz
AGgALgBlAHgAZQAAAFIAZQB0AHIAbwBBAHIAYwBoAC8AcgBhADMAMgAuAHQAcgBpAG0AdQBpAAAA
UgBlAHQAcgBvAEEAcgBjAGgALwByAGEANgA0AC4AdAByAGkAbQB1AGkAAABSAGUAdAByAG8AQQBy
AGMAaAAvAC4AcgBlAHQAcgBvAGEAcgBjAGgALwBjAG8AcgBlAHMALwBhAF8AbABpAGIAcgBlAHQA
cgBvAC4AcwBvAAAAAAA=`

// TestSevenZipBranchFilters pins the branch filters: every file of the x86
// BCJ, ARM and ARM64 blocks comes back whole and matches its CRC (a_libretro.so
// starts 3,002 bytes into its block, inside an instruction), a read that stops
// inside an instruction converts it whole first, and the methods read as
// 7-Zip lists them.
func TestSevenZipBranchFilters(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(string(bytes.ReplaceAll([]byte(sevenZipBranchFixture), []byte("\n"), nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	a, err := sevenZipOpen(r, int64(len(data)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := map[string]struct {
		size    uint64
		crc     uint32
		methods string
	}{
		"RetroArch/retroarch.cfg":                  {48, 0xd4bfc37b, "LZMA2"},
		"Tools/flash.exe":                          {700, 0x4c5211f8, "BCJ + LZMA2"},
		"RetroArch/ra32.trimui":                    {1026, 0xbed8e8ba, "ARM + LZMA2"},
		"RetroArch/ra64.trimui":                    {3002, 0xf29928bc, "ARM64 + LZMA2"},
		"RetroArch/.retroarch/cores/a_libretro.so": {1501, 0x05c49f7a, "ARM64 + LZMA2"},
	}
	if len(a.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(a.Entries), len(want))
	}
	for i, e := range a.Entries {
		w, ok := want[e.Name]
		if !ok || e.Size != w.size || e.CRC != w.crc || e.folder < 0 {
			t.Fatalf("entry %d = %+v", i, e)
		}
		if got := sevenZipFolderMethods(a.streams.folders[e.folder]); got != w.methods {
			t.Errorf("%s: methods %q, want %q", e.Name, got, w.methods)
		}
		body, err := a.readHead(r, i, int(e.Size))
		if err != nil || uint64(len(body)) != e.Size || crc32.ChecksumIEEE(body) != e.CRC {
			t.Errorf("%s came back as %d bytes, err %v", e.Name, len(body), err)
		}
		if e.Name == "RetroArch/ra64.trimui" {
			// Byte 2050 is in the middle of a BL whose low half the filter
			// changed: cut before the conversion, the read would end wrong.
			head, err := a.readHead(r, i, 2050)
			if err != nil || len(head) != 2050 || crc32.ChecksumIEEE(head) != 0x5baf9770 {
				t.Errorf("ra64.trimui's first 2050 bytes came back as %d bytes, err %v", len(head), err)
			}
		}
	}
}
