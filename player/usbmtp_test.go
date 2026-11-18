package main

// usbmtp_test.go — the MTP responder against what a computer sends: names
// that try to leave the card, a dataset it must read to the end, and one
// copy-in/rename/delete round trip over a fake USB pipe.

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// mtpFakeUSB plays the computer: in holds the transfers it sends, writes
// what the handheld sent back.
type mtpFakeUSB struct {
	in     [][]byte
	writes []byte
}

func (f *mtpFakeUSB) read(p []byte) (int, error) {
	if len(f.in) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.in[0])
	if n < len(f.in[0]) {
		f.in[0] = f.in[0][n:]
	} else {
		f.in = f.in[1:]
	}
	return n, nil
}

func (f *mtpFakeUSB) write(p []byte) (int, error) {
	f.writes = append(f.writes, p...)
	return len(p), nil
}

func (f *mtpFakeUSB) event([]byte)   {}
func (f *mtpFakeUSB) maxPacket() int { return 512 }

func mtpTestContainer(typ, code uint16, tid uint32, body []byte) []byte {
	b := make([]byte, mtpHeaderLen, mtpHeaderLen+len(body))
	mtpPutHeader(b, uint32(mtpHeaderLen+len(body)), typ, code, tid)
	return append(b, body...)
}

func mtpTestCommand(code uint16, tid uint32, params ...uint32) []byte {
	w := &mtpW{}
	for _, p := range params {
		w.u32(p)
	}
	return mtpTestContainer(mtpContainerCommand, code, tid, w.b)
}

func mtpTestObjectInfo(format uint16, size uint32, name string) []byte {
	w := &mtpW{}
	w.u32(mtpStorageID)
	w.u16(format)
	w.u16(0)
	w.u32(size)
	w.u16(0)
	for i := 0; i < 6; i++ {
		w.u32(0)
	}
	w.u32(0)
	w.u16(0)
	w.u32(0)
	w.u32(0)
	w.str(name)
	w.str("")
	w.str("20260101T120000")
	w.str("")
	return w.b
}

type mtpTestReply struct {
	typ, code uint16
	body      []byte
}

// mtpTestRun serves every queued command and returns the containers sent.
func mtpTestRun(t *testing.T, s *mtpServer, usb *mtpFakeUSB) []mtpTestReply {
	t.Helper()
	usb.writes = nil
	for len(usb.in) > 0 {
		if err := s.serveOne(); err != nil {
			t.Fatalf("serveOne: %v", err)
		}
	}
	var out []mtpTestReply
	b := usb.writes
	for len(b) >= mtpHeaderLen {
		n := int(binary.LittleEndian.Uint32(b))
		out = append(out, mtpTestReply{binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]), b[mtpHeaderLen:n]})
		b = b[n:]
	}
	return out
}

// mtpTestResponse is the last container, which must be a response.
func mtpTestResponse(t *testing.T, replies []mtpTestReply) (uint16, []uint32) {
	t.Helper()
	if len(replies) == 0 || replies[len(replies)-1].typ != mtpContainerResponse {
		t.Fatalf("no response in %+v", replies)
	}
	r := replies[len(replies)-1]
	var params []uint32
	for i := 0; i+4 <= len(r.body); i += 4 {
		params = append(params, binary.LittleEndian.Uint32(r.body[i:]))
	}
	return r.code, params
}

func TestMTPCleanName(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "/etc", "x\x00y", "tab\tname"} {
		if _, ok := mtpCleanName(bad); ok {
			t.Errorf("mtpCleanName(%q) accepted", bad)
		}
	}
	for _, good := range []string{"Pokémon Emerald.gba", "..hidden", "a.b.c", "日本語.zip"} {
		if got, ok := mtpCleanName(good); !ok || got != good {
			t.Errorf("mtpCleanName(%q) = %q, %v", good, got, ok)
		}
	}
	if _, _, ok := mtpReadString([]byte{200, 'a', 0}, 0); ok {
		t.Error("mtpReadString accepted a count past the end")
	}
}

func TestMTPCopyInRenameDelete(t *testing.T) {
	root := t.TempDir()
	usb := &mtpFakeUSB{}
	s := newMTPServer(usb, mtpServerConfig{root: root})

	usb.in = [][]byte{mtpTestCommand(mtpOpOpenSession, 1, 1)}
	if code, _ := mtpTestResponse(t, mtpTestRun(t, s, usb)); code != mtpRespOK {
		t.Fatalf("OpenSession = %#x", code)
	}

	// A folder at the top level, then a file in it.
	usb.in = [][]byte{
		mtpTestCommand(mtpOpSendObjectInfo, 2, mtpStorageID, mtpAll),
		mtpTestContainer(mtpContainerData, mtpOpSendObjectInfo, 2, mtpTestObjectInfo(mtpFormatAssociation, 0, "Roms")),
	}
	code, params := mtpTestResponse(t, mtpTestRun(t, s, usb))
	if code != mtpRespOK || len(params) != 3 {
		t.Fatalf("SendObjectInfo(folder) = %#x %v", code, params)
	}
	folder := params[2]
	usb.in = [][]byte{
		mtpTestCommand(mtpOpSendObjectInfo, 3, mtpStorageID, folder),
		mtpTestContainer(mtpContainerData, mtpOpSendObjectInfo, 3, mtpTestObjectInfo(mtpFormatUndefined, 5, "game.gba")),
		mtpTestCommand(mtpOpSendObject, 4),
		mtpTestContainer(mtpContainerData, mtpOpSendObject, 4, []byte("hello")),
	}
	replies := mtpTestRun(t, s, usb)
	if code, _ := mtpTestResponse(t, replies); code != mtpRespOK {
		t.Fatalf("SendObject = %#x", code)
	}
	_, params = mtpTestResponse(t, replies[:1])
	file := params[2]
	if b, err := os.ReadFile(filepath.Join(root, "Roms", "game.gba")); err != nil || string(b) != "hello" {
		t.Fatalf("copied file = %q, %v", b, err)
	}

	// A name that climbs out of the card is refused and nothing is made.
	usb.in = [][]byte{
		mtpTestCommand(mtpOpSendObjectInfo, 5, mtpStorageID, folder),
		mtpTestContainer(mtpContainerData, mtpOpSendObjectInfo, 5, mtpTestObjectInfo(mtpFormatUndefined, 1, "../../evil")),
	}
	if code, _ := mtpTestResponse(t, mtpTestRun(t, s, usb)); code != mtpRespInvalidDataset {
		t.Fatalf("SendObjectInfo(../../evil) = %#x", code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil")); err == nil {
		t.Fatal("a file was written outside the card")
	}

	// Reading it back, renaming, deleting the folder.
	usb.in = [][]byte{mtpTestCommand(mtpOpGetObject, 6, file)}
	replies = mtpTestRun(t, s, usb)
	if len(replies) != 2 || string(replies[0].body) != "hello" {
		t.Fatalf("GetObject = %+v", replies)
	}
	nameValue := &mtpW{}
	nameValue.str("renamed.gba")
	usb.in = [][]byte{
		mtpTestCommand(mtpOpSetObjectPropValue, 7, file, mtpPropFileName),
		mtpTestContainer(mtpContainerData, mtpOpSetObjectPropValue, 7, nameValue.b),
	}
	if code, _ := mtpTestResponse(t, mtpTestRun(t, s, usb)); code != mtpRespOK {
		t.Fatalf("rename = %#x", code)
	}
	if _, err := os.Stat(filepath.Join(root, "Roms", "renamed.gba")); err != nil {
		t.Fatalf("renamed file: %v", err)
	}
	// Windows announces files with SendObjectPropList (64-bit size in the
	// parameters) and may send the data with an unknown length.
	list := &mtpW{}
	list.u32(2)
	list.u32(0)
	list.u16(mtpPropObjectFormat)
	list.u16(mtpDataTypeUint16)
	list.u16(mtpFormatUndefined)
	list.u32(0)
	list.u16(mtpPropFileName)
	list.u16(mtpDataTypeString)
	list.str("save.srm")
	body := mtpTestContainer(mtpContainerData, mtpOpSendObject, 10, []byte("abc"))
	binary.LittleEndian.PutUint32(body, mtpAll)
	usb.in = [][]byte{
		mtpTestCommand(mtpOpSendObjectPropList, 9, mtpStorageID, folder, mtpFormatUndefined, 0, 3),
		mtpTestContainer(mtpContainerData, mtpOpSendObjectPropList, 9, list.b),
		mtpTestCommand(mtpOpSendObject, 10),
		body,
	}
	replies = mtpTestRun(t, s, usb)
	if code, _ := mtpTestResponse(t, replies); code != mtpRespOK || len(replies) != 2 {
		t.Fatalf("SendObjectPropList + SendObject = %+v", replies)
	}
	if b, err := os.ReadFile(filepath.Join(root, "Roms", "save.srm")); err != nil || string(b) != "abc" {
		t.Fatalf("save.srm = %q, %v", b, err)
	}

	usb.in = [][]byte{mtpTestCommand(mtpOpDeleteObject, 8, folder)}
	if code, _ := mtpTestResponse(t, mtpTestRun(t, s, usb)); code != mtpRespOK {
		t.Fatalf("DeleteObject = %#x", code)
	}
	if _, err := os.Stat(filepath.Join(root, "Roms")); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
}

// While the startup lock is up the card is hidden, and the data a refused
// command carries is still read, so the next command is understood.
func TestMTPLockedDrainsData(t *testing.T) {
	usb := &mtpFakeUSB{}
	s := newMTPServer(usb, mtpServerConfig{root: t.TempDir(), locked: func() bool { return true }})
	usb.in = [][]byte{
		mtpTestCommand(mtpOpOpenSession, 1, 1),
		mtpTestCommand(mtpOpSendObjectInfo, 2, mtpStorageID, mtpAll),
		mtpTestContainer(mtpContainerData, mtpOpSendObjectInfo, 2, mtpTestObjectInfo(mtpFormatUndefined, 1, "x")),
		mtpTestCommand(mtpOpGetStorageIDs, 3),
	}
	replies := mtpTestRun(t, s, usb)
	if len(replies) != 4 || replies[1].code != mtpRespStoreNotAvailable {
		t.Fatalf("replies = %+v", replies)
	}
	if ids := replies[2].body; len(ids) != 4 || binary.LittleEndian.Uint32(ids) != 0 {
		t.Fatalf("storage IDs while locked = %v", ids)
	}
}
