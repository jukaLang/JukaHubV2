package main

import (
	"bytes"
	"encoding/base64"
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
