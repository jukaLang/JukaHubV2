package main

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
)

// The move record comes back from a card: every path in it is outside data.
func TestSDMoveManifestPathSafety(t *testing.T) {
	hash := strings.Repeat("a", 64)
	good := `{"format":"jukahub-sd-move","version":1,"state":"verified","files":[{"path":"Roms/GBA/a.gba","size":3,"sha256":"` + hash + `","cat":"games","mtime":1,"srcMtime":1}]}`
	m, err := sdMoveParseManifest([]byte(good))
	if err != nil || len(m.Files) != 1 || m.TotalBytes != 3 {
		t.Fatalf("good manifest: %v %+v", err, m)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../b", "a//b", "./a", `a\b`, "C:/x", ".jukahub-move/manifest.json", ".JUKAHUB-MOVE/x", ""} {
		doc := strings.Replace(good, "Roms/GBA/a.gba", strings.ReplaceAll(bad, `\`, `\\`), 1)
		if _, err := sdMoveParseManifest([]byte(doc)); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
	for _, doc := range []string{
		strings.Replace(good, `"version":1`, `"version":2`, 1),
		strings.Replace(good, hash, "xyz", 1),
		strings.Replace(good, `"state":"verified"`, `"state":"done"`, 1),
		strings.Replace(good, `"format":"jukahub-sd-move"`, `"format":"other"`, 1),
		strings.Replace(good, `]}`, `,{"path":"Roms/GBA/a.gba","size":3,"sha256":"`+hash+`"}]}`, 1),
	} {
		if _, err := sdMoveParseManifest([]byte(doc)); err == nil {
			t.Errorf("manifest accepted: %s", doc)
		}
	}

	root := t.TempDir()
	if p, err := sdMoveJoin(root, "Saves/x.srm"); err != nil || p != filepath.Join(root, "Saves", "x.srm") {
		t.Fatalf("join: %q %v", p, err)
	}
	if _, err := sdMoveJoin(root, "../outside"); err == nil {
		t.Fatal("join left the root")
	}

	// A problem whose name the record refuses still counts once read back.
	p := sdMoveRecordable(sdMoveProblem{Path: `a\b`, Why: sdMoveWhyBadName})
	if p.Path != "" || len(sdMoveCleanProblems([]sdMoveProblem{p})) != 1 {
		t.Fatalf("recordable: %+v", p)
	}
}

// A journal cut by a power loss keeps the lines before the cut; a "v" line
// only counts for the copy it names.
func TestSDMoveJournalReplay(t *testing.T) {
	h1, h2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	m := sdMoveManifest{Format: sdMoveFormat, Version: sdMoveVersion, State: sdMoveStateCopying}
	j := `{"k":"c","f":{"path":"a","size":1,"sha256":"` + h1 + `","cat":"other"}}
{"k":"c","f":{"path":"../evil","size":1,"sha256":"` + h1 + `"}}
{"k":"v","f":{"path":"a","size":1,"sha256":"` + h2 + `"}}
{"k":"v","f":{"path":"a","size":1,"sha256":"` + h1 + `"}}
{"k":"c","f":{"path":"b","si`
	if n := sdMoveApplyJournal(&m, []byte(j)); n != 2 {
		t.Fatalf("applied %d lines, want 2", n)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "a" || !m.Files[0].Verified {
		t.Fatalf("replayed files: %+v", m.Files)
	}
}

// Card check (cardcheck.go) reads a partition's boot sector, /proc/mounts,
// /proc/swaps and the kernel log: outside data, decoded by hand.
func TestCardCheckParsers(t *testing.T) {
	boot := func(bps, rsvd, rootEnt, fat16Size int, tot32, fat32Size uint32, spc, state byte, stateAt int) []byte {
		sec := make([]byte, 512)
		binary.LittleEndian.PutUint16(sec[11:], uint16(bps))
		sec[13] = spc
		binary.LittleEndian.PutUint16(sec[14:], uint16(rsvd))
		sec[16] = 2
		binary.LittleEndian.PutUint16(sec[17:], uint16(rootEnt))
		binary.LittleEndian.PutUint16(sec[22:], uint16(fat16Size))
		binary.LittleEndian.PutUint32(sec[32:], tot32)
		binary.LittleEndian.PutUint32(sec[36:], fat32Size)
		sec[stateAt] = state
		sec[510], sec[511] = 0x55, 0xAA
		return sec
	}
	// FAT32: 130 818 clusters; the dirty bit sits at byte 65.
	b, err := cardCheckDecodeBoot(boot(512, 32, 0, 0, 1<<20, 1000, 8, 0x01, 65))
	if err != nil || b.Kind != "FAT32" || !b.StateDirty || b.FAT1Offset != 32*512+4 {
		t.Fatalf("FAT32: %+v %v", b, err)
	}
	// FAT16: 49 891 clusters; byte 65 is not its dirty byte, 37 is.
	b, err = cardCheckDecodeBoot(boot(512, 1, 512, 200, 200000, 0, 4, 0x01, 65))
	if err != nil || b.Kind != "FAT16" || b.StateDirty || b.FAT1Offset != 512+2 {
		t.Fatalf("FAT16: %+v %v", b, err)
	}
	// exFAT: VolumeFlags at byte 106, VolumeDirty bit 1, MediaFailure bit 2.
	ex := make([]byte, 512)
	copy(ex[3:], "EXFAT   ")
	binary.LittleEndian.PutUint16(ex[106:], 0x0004)
	ex[510], ex[511] = 0x55, 0xAA
	if b, err := cardCheckDecodeBoot(ex); err != nil || b.Kind != "exFAT" || b.VolumeDirty || !b.MediaFailure {
		t.Fatalf("exFAT: %+v %v", b, err)
	}
	for _, bad := range [][]byte{make([]byte, 512), boot(500, 32, 0, 0, 1<<20, 1000, 8, 0, 65), boot(512, 32, 0, 0, 1<<20, 1000, 3, 0, 65), make([]byte, 100)} {
		if b, err := cardCheckDecodeBoot(bad); err == nil {
			t.Errorf("decoded a sector that is not FAT or exFAT: %+v", b)
		}
	}
	// FAT[1]: the clean-shutdown and hard-error bits.
	le32 := func(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
	le16 := func(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
	for _, tc := range []struct {
		kind         string
		entry        []byte
		clean, noErr bool
	}{
		{"FAT32", le32(0x0FFFFFFF), true, true},
		{"FAT32", le32(0xF7FFFFFF), false, true},
		{"FAT32", le32(0xFBFFFFFF), true, false},
		{"FAT16", le16(0xFFFF), true, true},
		{"FAT16", le16(0x7FFF), false, true},
		{"FAT16", le16(0xBFFF), true, false},
	} {
		clean, noErr, ok := cardCheckFAT1Flags(tc.kind, tc.entry)
		if !ok || clean != tc.clean || noErr != tc.noErr {
			t.Errorf("FAT[1] %s %x: clean=%v noErr=%v ok=%v", tc.kind, tc.entry, clean, noErr, ok)
		}
	}
	if _, _, ok := cardCheckFAT1Flags("FAT12", le32(0)); ok {
		t.Error("FAT12 keeps no flags")
	}

	// The longest mount point above a path, with /proc/mounts' escapes.
	mounts := cardCheckParseMounts("/dev/root / ext4 ro 0 0\n/dev/mmcblk0p1 /mnt/SDCARD vfat rw,relatime 0 0\n/dev/sda1 /mnt/SDCARD/Roms\\040x exfat rw 0 0\n")
	for path, want := range map[string]string{
		"/mnt/SDCARD/Roms/GBA": "/mnt/SDCARD",
		"/mnt/SDCARD/Roms x/a": "/mnt/SDCARD/Roms x",
		"/mnt/SDCARDX/Roms":    "/",
	} {
		if m, ok := cardCheckMountFor(mounts, path); !ok || m.Target != want {
			t.Errorf("mount for %s = %q, want %q", path, m.Target, want)
		}
	}
	for dev, ok := range map[string]bool{"/dev/mmcblk0p1": true, "/dev/block/sda1": true, "/dev/../etc/x": false, "mmcblk0p1": false, "/dev/": false} {
		if cardCheckSafeDev(dev) != ok {
			t.Errorf("cardCheckSafeDev(%q) != %v", dev, ok)
		}
	}
	if got := cardCheckParseSwaps("Filename\tType\tSize\tUsed\tPriority\n/mnt/SDCARD/swapfile file 524284 0 -2\n/dev/zram0 partition 1 0 5\n"); len(got) != 1 || got[0] != "/mnt/SDCARD/swapfile" {
		t.Errorf("swaps: %q", got)
	}
	k := cardCheckKernelLines("<4>[    5.1] FAT-fs (mmcblk0p1): Volume was not properly unmounted. Some data may be corrupt. Please run fsck.\n"+
		"<3>[   99.0] blk_update_request: I/O error, dev mmcblk0, sector 123\n<6>[    1.0] mmcblk1: p1\n", "mmcblk0p1", "mmcblk0")
	if !k.unclean || !k.ioErrors || k.fsErrors || len(k.lines) != 2 {
		t.Errorf("kernel lines: %+v", k)
	}

	// The fake card test's pattern: each block carries its offset, and two
	// offsets or two seeds never give the same block.
	a, c, d := make([]byte, 2*cardCheckBlock), make([]byte, cardCheckBlock), make([]byte, cardCheckBlock)
	cardCheckPattern(42, 0, a)
	cardCheckPattern(42, cardCheckBlock, c)
	cardCheckPattern(43, cardCheckBlock, d)
	if !bytes.Equal(a[cardCheckBlock:], c) || bytes.Equal(a[:cardCheckBlock], c) || bytes.Equal(c, d) ||
		binary.LittleEndian.Uint64(c) != cardCheckBlock {
		t.Error("the fake card test's pattern does not tell blocks apart")
	}
}
