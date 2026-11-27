package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func testParamSFO(id string) []byte {
	key := []byte("DISC_ID\x00")
	value := append([]byte(id), 0)
	out := make([]byte, 20+16+len(key)+len(value))
	binary.BigEndian.PutUint32(out[0:4], 0x46535000)
	binary.LittleEndian.PutUint32(out[8:12], 36)
	binary.LittleEndian.PutUint32(out[12:16], uint32(36+len(key)))
	binary.LittleEndian.PutUint32(out[16:20], 1)
	binary.LittleEndian.PutUint16(out[20:22], 0)
	binary.LittleEndian.PutUint16(out[22:24], 0x0204)
	binary.LittleEndian.PutUint32(out[24:28], uint32(len(value)))
	binary.LittleEndian.PutUint32(out[28:32], uint32(len(value)))
	binary.LittleEndian.PutUint32(out[32:36], 0)
	copy(out[36:], key)
	copy(out[36+len(key):], value)
	return out
}

func testPBP(id string) []byte {
	sfo := testParamSFO(id)
	head := make([]byte, 0x28)
	copy(head, []byte("\x00PBP"))
	binary.LittleEndian.PutUint32(head[4:8], 0x00010000)
	binary.LittleEndian.PutUint32(head[8:12], 0x28)
	return append(head, sfo...)
}

func isoDirRecord(name string, lba, size int64, dir bool) []byte {
	length := 33 + len(name)
	if length%2 != 0 {
		length++
	}
	out := make([]byte, length)
	out[0] = byte(length)
	binary.LittleEndian.PutUint32(out[2:6], uint32(lba))
	binary.LittleEndian.PutUint32(out[10:14], uint32(size))
	if dir {
		out[25] = 2
	}
	out[32] = byte(len(name))
	copy(out[33:], name)
	return out
}

func testPSPISO(id string) []byte {
	sfo := testParamSFO(id)
	iso := make([]byte, 21*2048)
	pvd := iso[16*2048:]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	copy(pvd[8:40], "PSP GAME")
	copy(pvd[156:190], isoDirRecord(".", 18, 2048, true))

	root := iso[18*2048:]
	pspDir := isoDirRecord("PSP_GAME", 19, 2048, true)
	copy(root, pspDir)

	gameDir := iso[19*2048:]
	copy(gameDir, isoDirRecord("PARAM.SFO", 20, int64(len(sfo)), false))

	copy(iso[20*2048:], sfo)
	return iso
}

func testCSO(iso []byte) []byte {
	const blockSize = 2048
	blocks := len(iso) / blockSize
	headerSize := 0x18
	indexSize := (blocks + 1) * 4
	start := headerSize + indexSize
	var body bytes.Buffer
	index := make([]uint32, blocks+1)
	for i := 0; i < blocks; i++ {
		block := iso[i*blockSize : (i+1)*blockSize]
		var compressed bytes.Buffer
		zw, _ := flate.NewWriter(&compressed, flate.DefaultCompression)
		_, _ = zw.Write(block)
		_ = zw.Close()
		index[i] = uint32(start + body.Len())
		if compressed.Len() < len(block) {
			body.Write(compressed.Bytes())
		} else {
			index[i] |= 0x80000000
			body.Write(block)
		}
	}
	index[blocks] = uint32(start + body.Len())
	head := make([]byte, headerSize)
	copy(head, "CISO")
	binary.LittleEndian.PutUint32(head[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint64(head[8:16], uint64(len(iso)))
	binary.LittleEndian.PutUint32(head[16:20], blockSize)
	head[20] = 1
	head[21] = 0
	out := append(head, make([]byte, indexSize)...)
	for i, v := range index {
		binary.LittleEndian.PutUint32(out[headerSize+i*4:headerSize+i*4+4], v)
	}
	return append(out, body.Bytes()...)
}

func writeTestImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPSPGameIDFromPBPISOAndCSO(t *testing.T) {
	const id = "ULUS10041"
	if got := pspGameID(writeTestImage(t, "game.pbp", testPBP(id))); got != id {
		t.Errorf("PBP ID = %q, want %q", got, id)
	}
	iso := testPSPISO(id)
	if got := pspGameID(writeTestImage(t, "game.iso", iso)); got != id {
		t.Errorf("ISO ID = %q, want %q", got, id)
	}
	if got := pspGameID(writeTestImage(t, "game.cso", testCSO(iso))); got != id {
		t.Errorf("CSO ID = %q, want %q", got, id)
	}
	if got := pspGameID(writeTestImage(t, "bad.pbp", []byte("not a PBP"))); got != "" {
		t.Errorf("bad PBP ID = %q, want empty", got)
	}
}

func TestGameCubeGameIDAndSaveNames(t *testing.T) {
	disc := make([]byte, 0x20)
	copy(disc, "G8ME01")
	binary.BigEndian.PutUint32(disc[0x1C:], 0xC2339F3D)
	path := writeTestImage(t, "game.iso", disc)
	if got := gcGameID(path); got != "G8ME01" {
		t.Fatalf("GameCube ID = %q, want G8ME01", got)
	}
	g := shoGame{stem: "game", gcID: "G8ME01"}
	if match, _ := shoFileKind("G8ME01-save.gci", g); !match {
		t.Error("G8ME01-save.gci was not matched to its game")
	}
	if match, _ := shoFileKind("01-G8ME01-save.gci", g); !match {
		t.Error("Dolphin's slot-prefixed GCI name was not matched")
	}
	if match, _ := shoFileKind("G8MF01-save.gci", g); match {
		t.Error("another game's GCI was matched")
	}
}

func TestPSPSaveNamesAndStatesMatchTheDiscID(t *testing.T) {
	g := shoGame{stem: "game", system: "PlayStation Portable", pspID: "ULUS10041"}
	if match, _ := shoFileKind("ULUS10041DATA/DATA.BIN", g); !match {
		t.Error("PSP SAVEDATA folder was not matched")
	}
	if match, _ := shoFileKind("ULUS10042DATA/DATA.BIN", g); match {
		t.Error("another PSP game's SAVEDATA was matched")
	}
	if !shoIsState("ULUS10041_0.ppst", g) {
		t.Error("PSP state was not recognised")
	}
	if match, _ := shoFileKind("ULUS10041_0.ppst", g); !match {
		t.Error("PSP state was not matched to its game")
	}
}

func TestPSPGameOfReadsTheIDFromTheISOs(t *testing.T) {
	path := writeTestImage(t, "game.iso", testPSPISO("ULUS10041"))
	g := shoGameOf(libraryGame{Path: path, System: "PlayStation Portable"})
	if g.pspID != "ULUS10041" {
		t.Fatalf("shoGameOf pspID = %q, want ULUS10041", g.pspID)
	}
}

func TestSaveRootsIncludeDolphinMemoryCards(t *testing.T) {
	sd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sd, "Dolphin", "User", "GC"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, root := range saveRootsFor(sd, "", "") {
		if root.Name == "Dolphin memory cards" {
			found = true
		}
	}
	if !found {
		t.Fatalf("saveRootsFor did not find the Dolphin memory-card root: %+v", saveRootsFor(sd, "", ""))
	}
}
