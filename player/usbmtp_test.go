package main

// usbmtp_test.go — the MTP responder against what a computer sends: names
// that try to leave the card, a dataset it must read to the end, and one
// copy-in/rename/delete round trip over a fake USB pipe. Also the DHCP
// server of "Browser over USB" (usbnet_dhcp.go) against malformed requests.

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// usbNetTestRequest is a DHCP request of type typ from hardware address
// 02:00:00:00:00:<mac>, with the extra options given.
func usbNetTestRequest(typ, mac byte, opts ...byte) []byte {
	b := make([]byte, usbNetDHCPMinLen)
	b[0], b[1], b[2] = 1, 1, 6
	b[4] = 0x42
	b[28], b[33] = 0x02, mac
	binary.BigEndian.PutUint32(b[236:], usbNetDHCPCookie)
	b = append(b, 53, 1, typ)
	b = append(b, opts...)
	return append(b, 255)
}

// usbNetTestOptions reads a reply's options; a router or DNS server in it
// fails the test, since the computer must keep its own internet route.
func usbNetTestOptions(t *testing.T, reply []byte) map[byte][]byte {
	t.Helper()
	if len(reply) < usbNetDHCPMinLen || reply[0] != 2 || binary.BigEndian.Uint32(reply[236:240]) != usbNetDHCPCookie {
		t.Fatalf("not a DHCP reply: % x", reply)
	}
	opts := map[byte][]byte{}
	if !usbNetDHCPOptions(reply[usbNetDHCPMinLen:], func(code byte, val []byte) { opts[code] = val }) {
		t.Fatal("reply options run past the end")
	}
	if _, ok := opts[3]; ok {
		t.Error("reply names a router")
	}
	if _, ok := opts[6]; ok {
		t.Error("reply names DNS servers")
	}
	return opts
}

// The DHCP server on the USB cable's network reads what the computer sends:
// a malformed request is dropped whole, and answers carry an address and a
// mask but never a router or DNS server.
func TestUSBNetDHCP(t *testing.T) {
	edit := func(b []byte, i int, v byte) []byte {
		b[i] = v
		return b
	}
	noType := append(usbNetTestRequest(usbNetDHCPDiscover, 1)[:usbNetDHCPMinLen], 255)
	for name, b := range map[string][]byte{
		"short":            usbNetTestRequest(usbNetDHCPDiscover, 1)[:200],
		"reply":            edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 0, 2),
		"long hw address":  edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 2, 16),
		"relayed":          edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 24, 10),
		"cookie":           edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 236, 0),
		"no hw address":    edit(edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 28, 0), 33, 0),
		"option overrun":   usbNetTestRequest(usbNetDHCPDiscover, 1, 50, 200, 10),
		"type length":      edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 241, 2),
		"unknown type":     edit(usbNetTestRequest(usbNetDHCPDiscover, 1), 242, 9),
		"requested length": usbNetTestRequest(usbNetDHCPRequest, 1, 50, 3, 10, 73, 73),
		"no type":          noType,
		"oversized":        append(usbNetTestRequest(usbNetDHCPDiscover, 1), make([]byte, usbNetDHCPMaxLen)...),
	} {
		if _, ok := usbNetDHCPParse(b); ok {
			t.Errorf("%s: accepted", name)
		}
	}

	pool := newUSBNetDHCPPool()
	now := time.Unix(1800000000, 0)
	handle := func(b []byte) ([]byte, [4]byte) {
		t.Helper()
		req, ok := usbNetDHCPParse(b)
		if !ok {
			t.Fatalf("request refused: % x", b)
		}
		return pool.handle(req, now)
	}
	offer, _ := handle(usbNetTestRequest(usbNetDHCPDiscover, 1))
	opts := usbNetTestOptions(t, offer)
	var got [4]byte
	copy(got[:], offer[16:20])
	if len(opts[53]) != 1 || opts[53][0] != usbNetDHCPOffer || !usbNetInPool(got) {
		t.Fatalf("DISCOVER answered %v with %v", opts[53], got)
	}
	if mask := opts[1]; len(mask) != 4 || mask[3] != 248 {
		t.Errorf("mask = %v", mask)
	}
	ask := append([]byte{50, 4}, got[:]...)
	ask = append(ask, 54, 4)
	ask = append(ask, usbNetDeviceAddr[:]...)
	ack, _ := handle(usbNetTestRequest(usbNetDHCPRequest, 1, ask...))
	if o := usbNetTestOptions(t, ack); len(o[53]) != 1 || o[53][0] != usbNetDHCPAck || len(o[51]) != 4 {
		t.Fatalf("REQUEST answered %v", o)
	}
	// Another computer asking for the same address is refused; one that took
	// another server's offer gets no answer.
	nak, _ := handle(usbNetTestRequest(usbNetDHCPRequest, 2, ask...))
	if o := usbNetTestOptions(t, nak); len(o[53]) != 1 || o[53][0] != usbNetDHCPNak {
		t.Errorf("REQUEST for a held address answered %v", o)
	}
	if r, _ := handle(usbNetTestRequest(usbNetDHCPRequest, 3, 50, 4, got[0], got[1], got[2], got[3], 54, 4, 192, 168, 1, 1)); r != nil {
		t.Error("answered a REQUEST meant for another server")
	}
	// INFORM: straight to the computer's own address, with no lease.
	inform := usbNetTestRequest(usbNetDHCPInform, 1)
	copy(inform[12:16], got[:])
	reply, to := handle(inform)
	if o := usbNetTestOptions(t, reply); to != got || len(o[51]) != 0 || binary.BigEndian.Uint32(reply[16:20]) != 0 {
		t.Errorf("INFORM answered %v to %v", o, to)
	}
}
