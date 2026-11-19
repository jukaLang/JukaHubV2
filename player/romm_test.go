package main

// Tests for the ROM manager: the header sniffers on synthetic headers, the
// plan a scan builds (misplaced games, a disc game with its track, duplicates,
// BIOS, empty and unknown files), and the conflict-safe moves with their undo
// journal.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rommWriteFile writes a test file, creating its folder.
func rommWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// rommTestGBA is a 4 KiB Game Boy Advance cartridge: the logo, the fixed byte
// and a byte that tells one test game from another.
func rommTestGBA(id byte) []byte {
	rom := make([]byte, 4096)
	copy(rom[0x04:], rommGBALogoPrefix)
	copy(rom[0xA0:], "TESTGAME")
	rom[0xB2] = 0x96
	rom[0x100] = id
	for i := 0x200; i < len(rom); i++ {
		rom[i] = byte(i) ^ id
	}
	return rom
}

// rommTestMD is a 4 KiB Mega Drive cartridge.
func rommTestMD() []byte {
	rom := make([]byte, 4096)
	copy(rom[0x100:], "SEGA MEGA DRIVE ")
	return rom
}

// rommTestSNES is a SNES image with a valid internal header at at, behind a
// copier header of copier bytes.
func rommTestSNES(size, copier, at int) []byte {
	buf := make([]byte, size)
	h := copier + at
	copy(buf[h:], "SUPER TEST GAME      ")
	buf[h+0x15] = 0x20
	buf[h+0x17] = 0x09
	binary.LittleEndian.PutUint16(buf[h+0x1C:], 0x1234)
	binary.LittleEndian.PutUint16(buf[h+0x1E:], 0x1234^0xFFFF)
	return buf
}

// rommTestDisc lays sectors out the way an image stores them: 2048-byte
// sectors for an ISO, or raw 2352-byte sectors behind the sync pattern and the
// mode byte.
func rommTestDisc(raw bool, mode byte, count int, sectors map[int][]byte) []byte {
	size, offset := 2048, 0
	if raw {
		size, offset = 2352, 16
		if mode == 2 {
			offset = 24
		}
	}
	buf := make([]byte, size*count)
	if raw {
		copy(buf, rommRawSync)
		buf[15] = mode
	}
	for lba, data := range sectors {
		copy(buf[lba*size+offset:], data)
	}
	return buf
}

// rommTestPVD is an ISO 9660 primary volume descriptor naming system, with its
// root directory at rootLBA.
func rommTestPVD(system string, rootLBA uint32) []byte {
	pvd := make([]byte, 2048)
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	copy(pvd[8:40], bytes.Repeat([]byte(" "), 32))
	copy(pvd[8:], system)
	pvd[156] = 34
	binary.LittleEndian.PutUint32(pvd[156+2:], rootLBA)
	binary.LittleEndian.PutUint32(pvd[156+10:], 2048)
	return pvd
}

// rommTestRootDir is a root directory holding SYSTEM.CNF at cnfLBA.
func rommTestRootDir(cnfLBA uint32) []byte {
	dir := make([]byte, 2048)
	name := "SYSTEM.CNF;1"
	rec := 33 + len(name)
	if rec%2 == 1 {
		rec++
	}
	dir[0] = byte(rec)
	binary.LittleEndian.PutUint32(dir[2:], cnfLBA)
	dir[32] = byte(len(name))
	copy(dir[33:], name)
	return dir
}

// rommTestPlayStation is a PlayStation disc (a raw Mode 2 image when raw)
// whose SYSTEM.CNF says boot.
func rommTestPlayStation(raw bool, boot string) []byte {
	mode := byte(0)
	if raw {
		mode = 2
	}
	return rommTestDisc(raw, mode, 19, map[int][]byte{
		16: rommTestPVD("PLAYSTATION", 17),
		17: rommTestRootDir(18),
		18: []byte(boot + "\r\n"),
	})
}

// rommTestZip writes a zip of files.
func rommTestZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	rommWriteFile(t, path, buf.Bytes())
}

func TestRommSniffCartHeaders(t *testing.T) {
	gb := make([]byte, 0x150)
	copy(gb[0x104:], rommGBLogo)
	gbc := append([]byte(nil), gb...)
	gbc[0x143] = 0xC0
	dual := append([]byte(nil), gb...)
	dual[0x143] = 0x80

	gba := rommTestGBA(1)
	gbaNoFixed := append([]byte(nil), gba...)
	gbaNoFixed[0xB2] = 0

	nds := make([]byte, 0x200)
	copy(nds[0xC0:], rommGBALogoPrefix)
	binary.LittleEndian.PutUint16(nds[0x15C:], 0xCF56)

	x32 := make([]byte, 0x200)
	copy(x32[0x100:], "SEGA 32X        ")

	sms := make([]byte, 0x8000)
	copy(sms[0x7FF0:], "TMR SEGA")
	sms[0x7FFF] = 0x4C
	gg := append([]byte(nil), sms...)
	gg[0x7FFF] = 0x7C

	snesBad := rommTestSNES(0x10000, 0, 0x7FC0)
	binary.LittleEndian.PutUint16(snesBad[0x7FC0+0x1E:], 0x1234)

	noise := make([]byte, 0x10000)
	for i := range noise {
		noise[i] = byte(i*7 + 3)
	}

	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"iNES", append([]byte("NES\x1a"), make([]byte, 60)...), "nes"},
		{"Famicom Disk System", append([]byte("FDS\x1a"), make([]byte, 60)...), "nes"},
		{"N64 big-endian", append([]byte{0x80, 0x37, 0x12, 0x40}, make([]byte, 60)...), "n64"},
		{"N64 byte-swapped", append([]byte{0x37, 0x80, 0x40, 0x12}, make([]byte, 60)...), "n64"},
		{"N64 little-endian", append([]byte{0x40, 0x12, 0x37, 0x80}, make([]byte, 60)...), "n64"},
		{"Game Boy", gb, "gb"},
		{"Game Boy Color only", gbc, "gbc"},
		{"Game Boy Color dual-mode", dual, "gbc"},
		{"Game Boy Advance", gba, "gba"},
		{"a GBA logo without the fixed byte", gbaNoFixed, ""},
		{"Nintendo DS", nds, "nds"},
		{"Mega Drive", rommTestMD(), "md"},
		{"a 32X cart is not a Mega Drive game", x32, ""},
		{"Master System", sms, "sms"},
		{"Game Gear", gg, "gg"},
		{"SNES LoROM", rommTestSNES(0x10000, 0, 0x7FC0), "snes"},
		{"SNES HiROM", rommTestSNES(0x10000, 0, 0xFFC0), "snes"},
		{"SNES behind a copier header", rommTestSNES(0x10000+512, 512, 0x7FC0), "snes"},
		{"SNES with a broken checksum", snesBad, ""},
		{"noise", noise, ""},
		{"nothing", nil, ""},
	}
	for _, c := range cases {
		if got := rommSniffCart(c.head, int64(len(c.head))); got != c.want {
			t.Errorf("%s: rommSniffCart = %q, want %q", c.name, got, c.want)
		}
	}
}

// rommSparseImage is a disc image that is zero except for one piece at an
// offset: a full Xbox 360 dump keeps its game partition hundreds of megabytes
// in, more than a test should allocate.
type rommSparseImage struct {
	size  int64
	at    int64
	piece []byte
}

func (s rommSparseImage) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > s.size-off {
		n = int(s.size - off)
	}
	clear(p[:n])
	if lo, hi := max(off, s.at), min(off+int64(n), s.at+int64(len(s.piece))); lo < hi {
		copy(p[lo-off:hi-off], s.piece[lo-s.at:hi-s.at])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// rommTestXDVDFS is an Xbox game partition: the volume descriptor 64 KiB in
// and a root directory at sector 40 whose entries are linked through their
// right-child offsets, as a padded tree.
func rommTestXDVDFS(names ...string) []byte {
	const rootSector = 40
	part := make([]byte, (rootSector+1)*2048)
	copy(part[0x10000:], rommXDVDFSMagic)
	binary.LittleEndian.PutUint32(part[0x10000+20:], rootSector)
	binary.LittleEndian.PutUint32(part[0x10000+24:], 2048)
	dir := part[rootSector*2048:]
	for i := range dir {
		dir[i] = 0xFF
	}
	off := 0
	for i, name := range names {
		next := (off + 14 + len(name) + 3) &^ 3
		right := 0
		if i < len(names)-1 {
			right = next / 4
		}
		binary.LittleEndian.PutUint16(dir[off:], 0)
		binary.LittleEndian.PutUint16(dir[off+2:], uint16(right))
		binary.LittleEndian.PutUint32(dir[off+4:], 41)
		binary.LittleEndian.PutUint32(dir[off+8:], 1)
		dir[off+12] = 0x20
		dir[off+13] = byte(len(name))
		copy(dir[off+14:], name)
		off = next
	}
	return part
}

// TestRommSniffXboxDiscs: an Xbox 360 image carries the same file system
// magic as an original-Xbox one, so the boot file in its root decides —
// trimmed, behind an XGD2 video partition, or behind an XGD1 one.
func TestRommSniffXboxDiscs(t *testing.T) {
	x360 := rommTestXDVDFS("$SystemUpdate", "media", "default.xex")
	xbox := rommTestXDVDFS("default.xbe", "media")
	bare := rommTestXDVDFS("media")
	empty := rommTestXDVDFS()
	looped := rommTestXDVDFS("media", "readme.txt")
	// The second entry (at byte 20, offset 5) names itself as both children.
	binary.LittleEndian.PutUint16(looped[40*2048+20:], 5)
	binary.LittleEndian.PutUint16(looped[40*2048+22:], 5)
	cases := []struct {
		name string
		at   int64
		part []byte
		want string
	}{
		{"trimmed Xbox 360 image", 0, x360, "xbox360"},
		{"trimmed Xbox image", 0, xbox, "xbox"},
		{"full XGD2 Xbox 360 dump", rommXGD2Base, x360, "xbox360"},
		{"full XGD3 Xbox 360 dump", rommXGD3Base, x360, "xbox360"},
		{"full XGD1 Xbox dump", 0x18300000, xbox, "xbox"},
		// No default.xbe in a root read whole: no original-Xbox disc boots
		// that way (a 360 second disc or content disc does).
		{"a root naming neither, trimmed", 0, bare, "xbox360"},
		{"a root naming neither, XGD2", rommXGD2Base, bare, "xbox360"},
		// A root that cannot be read whole keeps the old answer.
		{"a root with no entry, trimmed", 0, empty, "xbox"},
		{"a tree that loops", 0, looped, "xbox"},
	}
	for _, c := range cases {
		img := rommSparseImage{size: c.at + int64(len(c.part)), at: c.at, piece: c.part}
		if got := rommSniffDisc(img, img.size); got != c.want {
			t.Errorf("%s: rommSniffDisc = %q, want %q", c.name, got, c.want)
		}
	}
	if got := rommSniffDisc(rommSparseImage{size: 4 << 20}, 4<<20); got != "" {
		t.Errorf("an empty image = %q, want nothing", got)
	}
}

func TestRommPlanFilesLooseGamesDuplicatesAndBIOS(t *testing.T) {
	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	bios := filepath.Join(root, "BIOS")
	downloads := filepath.Join(root, "Downloads")
	zelda := rommTestGBA(1)

	rommWriteFile(t, filepath.Join(roms, "GBA", "Zelda.gba"), zelda)
	rommWriteFile(t, filepath.Join(roms, "SNES", "Metroid.gba"), rommTestGBA(2))
	rommTestZip(t, filepath.Join(roms, "ARCADE", "sf2.zip"), map[string][]byte{"a.rom": []byte("a"), "b.rom": []byte("b")})
	rommWriteFile(t, filepath.Join(roms, "PS", "Game.cue"), []byte("FILE \"Game.bin\" BINARY\n  TRACK 01 MODE2/2352\n"))
	rommWriteFile(t, filepath.Join(roms, "PS", "Game.bin"), rommTestPlayStation(true, "BOOT = cdrom:\\SLUS_000.01;1"))
	if err := os.MkdirAll(bios, 0o755); err != nil {
		t.Fatal(err)
	}
	rommWriteFile(t, filepath.Join(downloads, "Kirby.gba"), rommTestGBA(3))
	rommWriteFile(t, filepath.Join(downloads, "Zelda copy.gba"), zelda)
	rommWriteFile(t, filepath.Join(downloads, "Sub", "Zelda.gba"), rommTestGBA(4))
	rommWriteFile(t, filepath.Join(downloads, "scph1001.bin"), make([]byte, 512<<10))
	rommWriteFile(t, filepath.Join(downloads, "empty.gba"), nil)
	mystery := make([]byte, 2048)
	for i := range mystery {
		mystery[i] = byte(i*7 + 3)
	}
	rommWriteFile(t, filepath.Join(downloads, "mystery.bin"), mystery)
	rommWriteFile(t, filepath.Join(downloads, "Sonic.md"), rommTestMD())
	rommWriteFile(t, filepath.Join(downloads, "notes.md"), []byte("# notes\n"))
	rommWriteFile(t, filepath.Join(downloads, "Disc", "Game2.cue"),
		[]byte("FILE \"Game2 (Track 1).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"))
	rommWriteFile(t, filepath.Join(downloads, "Disc", "Game2 (Track 1).bin"), rommTestPlayStation(true, "BOOT = cdrom:\\SLUS_000.02;1"))

	roots := []rommRoot{
		{Path: roms, Area: rommAreaRoms, Depth: rommRomsDepth, Label: "Roms"},
		{Path: bios, Area: rommAreaBIOS, Depth: 1, Label: "BIOS"},
		{Path: downloads, Area: rommAreaLoose, Depth: 2, Label: "Downloads"},
	}
	plan := rommScan(context.Background(), roots, rommTargets{Roms: roms, BIOS: bios}, nil)

	items := map[string]rommItem{}
	for _, it := range plan.Items {
		key := filepath.Base(it.Path)
		if filepath.Dir(it.Path) == filepath.Join(downloads, "Sub") {
			key = "Sub/" + key
		}
		if _, dup := items[key]; dup {
			t.Fatalf("two items for %s", key)
		}
		items[key] = it
	}
	moves := []struct {
		key  string
		dest string
	}{
		{"Metroid.gba", filepath.Join(roms, "GBA", "Metroid.gba")},
		{"Kirby.gba", filepath.Join(roms, "GBA", "Kirby.gba")},
		{"Sub/Zelda.gba", filepath.Join(roms, "GBA", "Zelda (2).gba")},
		{"Sonic.md", filepath.Join(roms, "MD", "Sonic.md")},
		{"Game2.cue", filepath.Join(roms, "PS", "Game2.cue")},
	}
	for _, m := range moves {
		it, ok := items[m.key]
		if !ok {
			t.Errorf("no item for %s", m.key)
			continue
		}
		if it.Kind != rommKindMisplaced || it.Action != rommActMove || !it.On || it.Dest != m.dest {
			t.Errorf("%s = %+v, want a move into %s, switched on", m.key, it, m.dest)
		}
	}
	if it := items["Game2.cue"]; len(it.Parts) != 1 || filepath.Base(it.Parts[0]) != "Game2 (Track 1).bin" {
		t.Errorf("the disc game moves without its track: parts %v", it.Parts)
	}
	if it, ok := items["Zelda copy.gba"]; !ok || it.Kind != rommKindDuplicate || it.On ||
		it.Keep != filepath.Join(roms, "GBA", "Zelda.gba") {
		t.Errorf("the copy of Zelda = %+v, want a duplicate of the one in Roms/GBA, switched off", it)
	}
	if it, ok := items["scph1001.bin"]; !ok || it.Kind != rommKindBIOS || !it.On || it.Dest != filepath.Join(bios, "scph1001.bin") {
		t.Errorf("the PlayStation BIOS = %+v, want a move into BIOS", it)
	}
	if it, ok := items["empty.gba"]; !ok || it.Kind != rommKindEmpty || it.On {
		t.Errorf("the empty file = %+v, want an empty-file item, switched off", it)
	}
	if it, ok := items["mystery.bin"]; !ok || it.Kind != rommKindUnknown || it.Action != rommActNone {
		t.Errorf("the unknown .bin = %+v, want it listed and left alone", it)
	}
	for _, quiet := range []string{"notes.md", "Game.bin", "Game.cue", "Zelda.gba", "Game2 (Track 1).bin"} {
		if it, ok := items[quiet]; ok {
			t.Errorf("%s should not be in the plan: %+v", quiet, it)
		}
	}
	// Roms/ARCADE is a known system since LIB-2, so the set there is a third
	// game already in its place (it used to be an unknown folder).
	if plan.InPlace != 3 {
		t.Errorf("in place = %d, want 3 (Zelda in Roms/GBA, the PS disc and the arcade set)", plan.InPlace)
	}
	if changes, moved, deletes := rommPlanCounts(plan.Items); changes != 6 || moved != 6 || deletes != 0 {
		t.Errorf("counts = %d changes, %d moves, %d deletes; want 6, 6, 0", changes, moved, deletes)
	}
	if plan.Items[0].Kind != rommKindMisplaced || plan.Items[len(plan.Items)-1].Kind != rommKindUnknown {
		t.Error("the plan lists moves first and the files left alone last")
	}
}

func TestRommApplyMovesAndUndoPutsBack(t *testing.T) {
	root := t.TempDir()
	roms := filepath.Join(root, "Roms")
	downloads := filepath.Join(root, "Downloads")
	journal := filepath.Join(root, "journal.json")
	if err := os.MkdirAll(roms, 0o755); err != nil {
		t.Fatal(err)
	}
	kirby := filepath.Join(downloads, "Kirby.gba")
	cue := filepath.Join(downloads, "Disc", "Game2.cue")
	bin := filepath.Join(downloads, "Disc", "Game2 (Track 1).bin")
	dup := filepath.Join(downloads, "Zelda copy.gba")
	other := filepath.Join(downloads, "mystery.bin")
	rommWriteFile(t, kirby, rommTestGBA(3))
	rommWriteFile(t, cue, []byte("FILE \"Game2 (Track 1).bin\" BINARY\n"))
	rommWriteFile(t, bin, []byte("track"))
	rommWriteFile(t, dup, rommTestGBA(1))
	rommWriteFile(t, other, []byte("left alone"))

	items := []rommItem{
		{Kind: rommKindMisplaced, Action: rommActMove, Path: kirby, Dest: filepath.Join(roms, "GBA", "Kirby.gba"), On: true},
		{Kind: rommKindMisplaced, Action: rommActMove, Path: cue, Parts: []string{bin}, Dest: filepath.Join(roms, "PS", "Game2.cue"), On: true},
		{Kind: rommKindDuplicate, Action: rommActDelete, Path: dup, On: true},
		{Kind: rommKindUnknown, Action: rommActNone, Path: other, On: true},
		{Kind: rommKindMisplaced, Action: rommActMove, Path: other, Dest: filepath.Join(roms, "GBA", "nope.gba"), On: false},
	}
	res := rommApplyItems(context.Background(), items, journal, nil)
	if res.Moved != 2 || res.Deleted != 1 || res.Failed != 0 {
		t.Fatalf("apply = %+v, want 2 moved and 1 deleted", res)
	}
	for _, p := range []string{filepath.Join(roms, "GBA", "Kirby.gba"), filepath.Join(roms, "PS", "Game2.cue"),
		filepath.Join(roms, "PS", "Game2 (Track 1).bin"), other} {
		if !regularFile(p) {
			t.Errorf("%s is missing after the organise", p)
		}
	}
	for _, p := range []string{kirby, cue, bin, dup} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there after the organise", p)
		}
	}
	run, ok := rommLastRun(journal)
	if !ok || len(run.Moves) != 3 || len(run.Deleted) != 1 || len(run.Dirs) != 2 {
		t.Fatalf("journal run = %+v (found %v), want 3 moves, 1 deletion and 2 new folders", run, ok)
	}
	if n := rommUndoable(journal); n != 1 {
		t.Errorf("undoable runs = %d, want 1", n)
	}

	// Something takes Kirby's old place before the undo: it must not be
	// overwritten, Kirby comes back beside it.
	rommWriteFile(t, kirby, []byte("a new file"))
	undo, err := rommUndoLast(journal)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Restored != 3 || undo.Failed != 0 || undo.Lost != 1 {
		t.Fatalf("undo = %+v, want 3 restored and 1 deleted file lost", undo)
	}
	if data, _ := os.ReadFile(kirby); string(data) != "a new file" {
		t.Error("the undo overwrote the file that took Kirby's place")
	}
	if !regularFile(filepath.Join(downloads, "Kirby (2).gba")) {
		t.Error("Kirby did not come back beside the file in its place")
	}
	if !regularFile(cue) || !regularFile(bin) {
		t.Error("the disc game did not come back with its track")
	}
	for _, d := range []string{filepath.Join(roms, "GBA"), filepath.Join(roms, "PS")} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s was made by the organise and should be gone after the undo", d)
		}
	}
	if !dirExists(roms) {
		t.Error("the undo removed a folder the organise did not create")
	}
	if _, err := rommUndoLast(journal); !errors.Is(err, rommErrNothingToUndo) {
		t.Errorf("a second undo = %v, want rommErrNothingToUndo", err)
	}
}

func TestRommMovesNeverOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in", "Game.gba")
	dst := filepath.Join(dir, "out", "Game.gba")
	rommWriteFile(t, src, []byte("new"))
	rommWriteFile(t, dst, []byte("kept"))
	final, err := rommMoveFile(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "out", "Game (2).gba"); final != want {
		t.Fatalf("moved to %q, want %q", final, want)
	}
	if data, _ := os.ReadFile(dst); string(data) != "kept" {
		t.Error("the file already there was overwritten")
	}
	if data, _ := os.ReadFile(final); string(data) != "new" {
		t.Error("the moved file lost its content")
	}

	// The path a move across drives takes: a copy read back and compared, and
	// refused when the target exists.
	copySrc := filepath.Join(dir, "copy-src.bin")
	copyDst := filepath.Join(dir, "copy-dst.bin")
	rommWriteFile(t, copySrc, bytes.Repeat([]byte("rom"), 5000))
	if err := rommCopyVerified(copySrc, copyDst); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(copySrc)
	b, _ := os.ReadFile(copyDst)
	if !bytes.Equal(a, b) {
		t.Error("the verified copy differs from its source")
	}
	if err := rommCopyVerified(copySrc, copyDst); err == nil {
		t.Error("a copy over an existing file was not refused")
	}
	if regularFile(copyDst + ".jukahub-part") {
		t.Error("a refused copy left its temporary file behind")
	}

	// A name promised to another item counts as taken.
	other := filepath.Join(dir, "out", "Other.gba")
	if got := rommFreeName(other, map[string]bool{strings.ToLower(other): true}); got != filepath.Join(dir, "out", "Other (2).gba") {
		t.Errorf("rommFreeName with the name promised = %q", got)
	}
}

// TestGBCCartridgeColorFlag: the Game Boy Color sort (gbcsort.go) reads the
// CGB flag at 0x143 behind the Nintendo logo, in a file and in a zip of one
// cartridge, and nothing else.
func TestGBCCartridgeColorFlag(t *testing.T) {
	cart := func(flag byte) []byte {
		rom := make([]byte, 0x8000)
		copy(rom[0x104:], rommGBLogo)
		copy(rom[0x134:], "INDIE GAME")
		rom[0x143] = flag
		return rom
	}
	noLogo := cart(0xC0)
	noLogo[0x104] = 0
	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"Game Boy", cart(0x00), false},
		{"Color-enhanced", cart(0x80), true},
		{"Color only", cart(0xC0), true},
		{"a title byte, not the flag", cart(0x84), false},
		{"no logo", noLogo, false},
		{"cut before the flag", cart(0xC0)[:0x143], false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		if got := gbcCartColor(c.head); got != c.want {
			t.Errorf("%s: gbcCartColor = %v, want %v", c.name, got, c.want)
		}
	}

	dir := t.TempDir()
	files := map[string]bool{
		"Color.gb":   true,
		"Grey.gb":    false,
		"Zipped.zip": true,
		"Two.zip":    false,
		"Color.txt":  false,
	}
	rommWriteFile(t, filepath.Join(dir, "Color.gb"), cart(0xC0))
	rommWriteFile(t, filepath.Join(dir, "Grey.gb"), cart(0x00))
	rommWriteFile(t, filepath.Join(dir, "Color.txt"), cart(0xC0))
	rommTestZip(t, filepath.Join(dir, "Zipped.zip"), map[string][]byte{"game.gbc": cart(0x80), "readme.txt": []byte("hi")})
	rommTestZip(t, filepath.Join(dir, "Two.zip"), map[string][]byte{"a.gb": cart(0xC0), "b.gb": cart(0xC0)})
	for name, want := range files {
		if got := gbcROMColor(filepath.Join(dir, name)); got != want {
			t.Errorf("gbcROMColor(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestDiscIndexRecordMatchesRetroArch: the Disc row writes RetroArch's .ldci
// record (discpage.go) the way RetroArch reads it, reads one RetroArch wrote,
// and names a disc the way the core reports it.
func TestDiscIndexRecordMatchesRetroArch(t *testing.T) {
	got := string(discIndexEncode(discIndexRecord{ImageIndex: 1, ImagePath: "/Roms/PS/Tom & Jerry (Disc 2).cue"}))
	want := "{\n  \"image_index\": 1,\n  \"image_path\": \"/Roms/PS/Tom & Jerry (Disc 2).cue\"\n}\n"
	if got != want {
		t.Errorf("discIndexEncode = %q, want %q", got, want)
	}
	rec, ok := discIndexDecode([]byte(`{ "version": "1.0", "image_index": 2, "image_path": "M:\\Games\\D (USA) (Disc 3).cue" }`))
	if !ok || rec.ImageIndex != 2 || rec.ImagePath != `M:\Games\D (USA) (Disc 3).cue` || rec.Version != "1.0" {
		t.Errorf("discIndexDecode = %+v, %v", rec, ok)
	}
	if !strings.Contains(string(discIndexEncode(rec)), `"version": "1.0"`) {
		t.Error("a record RetroArch wrote with a version lost it")
	}
	if _, ok := discIndexDecode([]byte("not json")); ok {
		t.Error("discIndexDecode took a broken record")
	}

	// The core joins the playlist's folder, as RetroArch was given it, with the line.
	sep := string(filepath.Separator)
	if got := discImagePath("/mnt/SDCARD/Emus/PS/../../Roms/PS/Game.m3u", "Game (Disc 2).cue"); got != "/mnt/SDCARD/Emus/PS/../../Roms/PS"+sep+"Game (Disc 2).cue" {
		t.Errorf("discImagePath = %q", got)
	}
	m3u := filepath.Join(t.TempDir(), "Game.m3u")
	rommWriteFile(t, m3u, []byte("\ufeff#EXTM3U\r\nGame (Disc 1).cue\r\n\r\nDisc 2/game.chd|Second disc\r\n"))
	if got := discM3UEntries(m3u); len(got) != 2 || got[0] != "Game (Disc 1).cue" || got[1] != "Disc 2/game.chd" {
		t.Errorf("discM3UEntries = %q", got)
	}
}
