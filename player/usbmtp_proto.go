package main

// usbmtp_proto.go — the conversation behind "USB file transfer" (usbmtp.go).
//
// A computer talks to the handheld over the cable with MTP, the protocol
// phones use: Windows and Linux open it with nothing to install, so the SD
// card appears in File Explorer (or the file manager) next to the drives.
// MTP shares files, not a disk, which is why it is used here: the card stays
// mounted on the handheld, JukaHub keeps running from it, and nothing has to
// be unmounted the way a USB mass-storage "card reader" would need.
//
// The computer sends a command, sometimes followed by a block of data; the
// handheld may answer with data and always ends with a response (PTP/MTP over
// USB, "Still Image" class). This file is that exchange, over any transport:
// usbmtp_linux.go supplies the real one (FunctionFS endpoints), the test a
// fake. Everything here runs on one goroutine, the transport's; only the
// cancel/busy/reset flags are touched from the control endpoint's goroutine.
//
// What the computer can do: list folders, read a file (whole or a range),
// create folders, copy files in, delete, rename, move and copy on the card.
// A file being copied in is written to a hidden ".jukamtp-…" file next to it
// and renamed into place once it is complete, so a pulled cable never leaves
// half a ROM under the real name. Names from the computer are single path
// elements checked by mtpCleanName, and every path is built from the card
// root down, so nothing outside the card can be named.

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
)

// mtpTransport is the USB side: the bulk pipes and the interrupt pipe.
type mtpTransport interface {
	// read fills p with the next piece of what the computer sent. A transfer
	// longer than p arrives over several reads; a transfer ends with a short
	// read. A zero-length read is a zero-length packet.
	read(p []byte) (int, error)
	// write sends p to the computer; an empty p sends a zero-length packet.
	write(p []byte) (int, error)
	// event queues an event container on the interrupt pipe. It never blocks;
	// an event the pipe has no room for is dropped.
	event(p []byte)
	// maxPacket is the bulk packet size: a data phase whose length is a
	// multiple of it ends with a zero-length packet.
	maxPacket() int
}

// Container types.
const (
	mtpContainerCommand  = 1
	mtpContainerData     = 2
	mtpContainerResponse = 3
	mtpContainerEvent    = 4
)

const (
	mtpHeaderLen = 12
	// mtpStorageID is the card: physical storage 1, logical 1.
	mtpStorageID = 0x00010001
	// mtpAll is "every storage", "the top level" or "every object",
	// depending on the parameter.
	mtpAll = 0xFFFFFFFF
	// mtpBufSize is one USB request; a multiple of every bulk packet size.
	mtpBufSize = 64 << 10
	// mtpPartPrefix and mtpPartSuffix name a file still being copied in. Such
	// files are never listed.
	mtpPartPrefix = ".jukamtp-"
	mtpPartSuffix = ".part"
	// mtpMaxDataset caps what is read into memory for a dataset (an object's
	// description, a property value). Real ones are well under 1 KiB.
	mtpMaxDataset = 64 << 10
)

// Operations.
const (
	mtpOpGetDeviceInfo           = 0x1001
	mtpOpOpenSession             = 0x1002
	mtpOpCloseSession            = 0x1003
	mtpOpGetStorageIDs           = 0x1004
	mtpOpGetStorageInfo          = 0x1005
	mtpOpGetNumObjects           = 0x1006
	mtpOpGetObjectHandles        = 0x1007
	mtpOpGetObjectInfo           = 0x1008
	mtpOpGetObject               = 0x1009
	mtpOpDeleteObject            = 0x100B
	mtpOpSendObjectInfo          = 0x100C
	mtpOpSendObject              = 0x100D
	mtpOpGetDevicePropDesc       = 0x1014
	mtpOpGetDevicePropValue      = 0x1015
	mtpOpSetDevicePropValue      = 0x1016
	mtpOpMoveObject              = 0x1019
	mtpOpCopyObject              = 0x101A
	mtpOpGetPartialObject        = 0x101B
	mtpOpGetObjectPropsSupported = 0x9801
	mtpOpGetObjectPropDesc       = 0x9802
	mtpOpGetObjectPropValue      = 0x9803
	mtpOpSetObjectPropValue      = 0x9804
	mtpOpGetObjectPropList       = 0x9805
	mtpOpSendObjectPropList      = 0x9808
	mtpOpGetObjectReferences     = 0x9810
	mtpOpSetObjectReferences     = 0x9811
)

// Response codes.
const (
	mtpRespOK                       = 0x2001
	mtpRespGeneralError             = 0x2002
	mtpRespSessionNotOpen           = 0x2003
	mtpRespOperationNotSupported    = 0x2005
	mtpRespIncompleteTransfer       = 0x2007
	mtpRespInvalidStorageID         = 0x2008
	mtpRespInvalidObjectHandle      = 0x2009
	mtpRespDevicePropNotSupported   = 0x200A
	mtpRespStoreFull                = 0x200C
	mtpRespAccessDenied             = 0x200F
	mtpRespPartialDeletion          = 0x2012
	mtpRespStoreNotAvailable        = 0x2013
	mtpRespNoValidObjectInfo        = 0x2015
	mtpRespDeviceBusy               = 0x2019
	mtpRespInvalidParentObject      = 0x201A
	mtpRespInvalidParameter         = 0x201D
	mtpRespInvalidObjectPropCode    = 0xA801
	mtpRespInvalidObjectPropValue   = 0xA803
	mtpRespInvalidDataset           = 0xA806
	mtpRespSpecByGroupUnsupported   = 0xA807
	mtpRespSpecByDepthUnsupported   = 0xA808
	mtpRespObjectPropNotSupported   = 0xA80A
	mtpRespObjectWriteProtectedProp = 0x200D
)

// Events.
const (
	mtpEventStoreAdded   = 0x4004
	mtpEventStoreRemoved = 0x4005
)

// Object formats. Most files are "undefined" and the computer goes by the
// extension; common media types get their own code, because Windows warns
// that the device "might not be able to play" a format it does not list.
const (
	mtpFormatUndefined   = 0x3000
	mtpFormatAssociation = 0x3001 // a folder
)

// mtpFormatByExt is the format code of a file extension (lower case).
var mtpFormatByExt = map[string]uint16{
	".txt": 0x3004, ".wav": 0x3008, ".mp3": 0x3009, ".avi": 0x300A,
	".mpg": 0x300B, ".mpeg": 0x300B, ".bmp": 0x3804, ".gif": 0x3807,
	".jpg": 0x3801, ".jpeg": 0x3801, ".png": 0x380B, ".wma": 0xB901,
	".ogg": 0xB902, ".aac": 0xB903, ".flac": 0xB906, ".wmv": 0xB981,
	".mp4": 0xB982, ".m4a": 0xB982, ".m4v": 0xB982,
}

// mtpFormats is every format the handheld takes, in a fixed order.
var mtpFormats = func() []uint16 {
	out := []uint16{mtpFormatUndefined, mtpFormatAssociation}
	seen := map[uint16]bool{mtpFormatUndefined: true, mtpFormatAssociation: true}
	exts := make([]string, 0, len(mtpFormatByExt))
	for ext := range mtpFormatByExt {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		if f := mtpFormatByExt[ext]; !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}()

// Object properties.
const (
	mtpPropStorageID        = 0xDC01
	mtpPropObjectFormat     = 0xDC02
	mtpPropProtection       = 0xDC03
	mtpPropObjectSize       = 0xDC04
	mtpPropFileName         = 0xDC07
	mtpPropDateCreated      = 0xDC08
	mtpPropDateModified     = 0xDC09
	mtpPropParentObject     = 0xDC0B
	mtpPropPersistentUID    = 0xDC41
	mtpPropName             = 0xDC44
	mtpDevPropSyncPartner   = 0xD401
	mtpDevPropFriendlyName  = 0xD402
	mtpDataTypeUint16       = 0x0004
	mtpDataTypeUint32       = 0x0006
	mtpDataTypeUint64       = 0x0008
	mtpDataTypeUint128      = 0x000A
	mtpDataTypeString       = 0xFFFF
	mtpAssociationGeneric   = 0x0001
	mtpStorageRemovableRAM  = 0x0004
	mtpFilesystemHierarchic = 0x0002
)

var mtpOperations = []uint16{
	mtpOpGetDeviceInfo, mtpOpOpenSession, mtpOpCloseSession, mtpOpGetStorageIDs,
	mtpOpGetStorageInfo, mtpOpGetNumObjects, mtpOpGetObjectHandles, mtpOpGetObjectInfo,
	mtpOpGetObject, mtpOpDeleteObject, mtpOpSendObjectInfo, mtpOpSendObject,
	mtpOpGetDevicePropDesc, mtpOpGetDevicePropValue, mtpOpSetDevicePropValue,
	mtpOpMoveObject, mtpOpCopyObject, mtpOpGetPartialObject,
	mtpOpGetObjectPropsSupported, mtpOpGetObjectPropDesc, mtpOpGetObjectPropValue,
	mtpOpSetObjectPropValue, mtpOpGetObjectPropList, mtpOpSendObjectPropList,
	mtpOpGetObjectReferences, mtpOpSetObjectReferences,
}

var mtpObjectProps = []uint16{
	mtpPropStorageID, mtpPropObjectFormat, mtpPropProtection, mtpPropObjectSize,
	mtpPropFileName, mtpPropDateCreated, mtpPropDateModified, mtpPropParentObject,
	mtpPropPersistentUID, mtpPropName,
}

// mtpHostSendsData lists the operations whose command is followed by a data
// phase from the computer. That data is always read to the end, even when
// the answer is an error, or the next command would be read out of it.
func mtpHostSendsData(op uint16) bool {
	switch op {
	case mtpOpSendObjectInfo, mtpOpSendObject, mtpOpSetDevicePropValue,
		mtpOpSetObjectPropValue, mtpOpSetObjectReferences, mtpOpSendObjectPropList:
		return true
	}
	return false
}

var (
	errMTPCancelled = errors.New("the computer cancelled the transfer")
	errMTPProtocol  = errors.New("the computer sent something out of turn")
	errMTPTooBig    = errors.New("dataset too large")
)

// mtpObject is one file or folder the computer has a handle for. Its path is
// its parent's path plus its name, so renaming or moving a folder needs no
// change to anything inside it.
type mtpObject struct {
	parent uint32 // 0: the card's top level
	name   string
	dir    bool

	// uid is the persistent unique ID the computer caches by: a hash of
	// where the object was first seen, kept through renames and moves.
	uid [16]byte
}

type mtpKey struct {
	parent uint32
	name   string
}

// mtpPendingSend is what SendObjectInfo or SendObjectPropList announced for
// the next SendObject.
type mtpPendingSend struct {
	handle uint32
	size   int64 // -1: not known
	mod    time.Time
}

// mtpMaxParams is how many parameters a command or response can carry.
const mtpMaxParams = 5

type mtpCmd struct {
	code uint16
	tid  uint32
	p    [mtpMaxParams]uint32
}

// mtpServerConfig is what the server needs from JukaHub.
type mtpServerConfig struct {
	root   string        // the card
	name   func() string // what the computer calls the handheld
	serial string
	locked func() bool                                     // the startup lock is up: the card is hidden
	space  func(path string) (total, free uint64, ok bool) // the card's size and free space
}

// mtpServer is one handheld's side of the conversation.
type mtpServer struct {
	t   mtpTransport
	cfg mtpServerConfig
	buf []byte

	// Set from the control endpoint's goroutine.
	cancel atomic.Bool // the computer cancelled the transaction in progress
	busy   atomic.Bool // a transaction is in progress
	reset  atomic.Bool // the computer reset the device: forget the session
	// session is the open session's ID, 0 when none. Atomic because the lock
	// watcher asks whether anyone is there to tell.
	session atomic.Uint32

	objs    map[uint32]*mtpObject
	index   map[mtpKey]uint32
	next    uint32
	pending *mtpPendingSend
	partner string
}

func newMTPServer(t mtpTransport, cfg mtpServerConfig) *mtpServer {
	if cfg.name == nil {
		cfg.name = func() string { return "JukaHub" }
	}
	if cfg.locked == nil {
		cfg.locked = func() bool { return false }
	}
	if cfg.space == nil {
		cfg.space = func(string) (uint64, uint64, bool) { return 0, 0, false }
	}
	return &mtpServer{
		t:     t,
		cfg:   cfg,
		buf:   make([]byte, mtpBufSize),
		objs:  map[uint32]*mtpObject{},
		index: map[mtpKey]uint32{},
		next:  1,
	}
}

// endSession forgets the session after a disconnect or a reset. Handles stay
// valid: a computer that reconnects lists again anyway.
func (s *mtpServer) endSession() {
	s.session.Store(0)
	s.pending = nil
	s.cancel.Store(false)
}

// sessionOpen reports whether a computer has a session open. Any goroutine.
func (s *mtpServer) sessionOpen() bool { return s.session.Load() != 0 }

// lockChanged tells the computer the card went away or came back as the
// startup lock went up or down. Any goroutine.
func (s *mtpServer) lockChanged(locked bool) {
	if !s.sessionOpen() {
		return
	}
	code := uint16(mtpEventStoreAdded)
	if locked {
		code = mtpEventStoreRemoved
	}
	s.sendEvent(code, mtpStorageID)
}

// ── The loop ─────────────────────────────────────────────────────────────

// serveOne waits for one command and carries it out. An error means the
// pipe broke (unplugged, reset) or the two sides lost step; the caller ends
// the session and carries on.
func (s *mtpServer) serveOne() error {
	n, err := s.t.read(s.buf)
	if err != nil {
		return err
	}
	if s.reset.Swap(false) {
		s.endSession()
	}
	if n < mtpHeaderLen {
		return nil // a zero-length packet left over from the last transfer
	}
	// A command is one short transfer of 12 to 32 bytes that says its own
	// length. Anything else is the tail of a cancelled data phase, whose
	// bytes could otherwise pass for a command.
	length := binary.LittleEndian.Uint32(s.buf[0:])
	if binary.LittleEndian.Uint16(s.buf[4:]) != mtpContainerCommand ||
		int64(length) != int64(n) || n > mtpHeaderLen+4*mtpMaxParams || (n-mtpHeaderLen)%4 != 0 {
		return nil
	}
	c := mtpCmd{code: binary.LittleEndian.Uint16(s.buf[6:]), tid: binary.LittleEndian.Uint32(s.buf[8:])}
	for i := 0; i < len(c.p) && mtpHeaderLen+4*i+4 <= n; i++ {
		c.p[i] = binary.LittleEndian.Uint32(s.buf[mtpHeaderLen+4*i:])
	}
	s.cancel.Store(false)
	s.busy.Store(true)
	defer s.busy.Store(false)
	code, params, err := s.dispatch(c)
	if errors.Is(err, errMTPCancelled) || s.cancel.Load() {
		// A cancelled transaction gets no response; the computer polls the
		// device status on the control pipe instead (usbmtp_linux.go).
		return nil
	}
	if err != nil {
		return err
	}
	return s.respond(c.tid, code, params)
}

// dispatch carries out one command and names the response.
func (s *mtpServer) dispatch(c mtpCmd) (uint16, []uint32, error) {
	switch c.code {
	case mtpOpGetDeviceInfo:
		return s.opDeviceInfo(c)
	case mtpOpOpenSession:
		if c.p[0] == 0 {
			return mtpRespInvalidParameter, nil, nil
		}
		// A computer that lost track (a crash, a resume) opens again
		// without closing; that is accepted rather than refused.
		s.endSession()
		s.session.Store(c.p[0])
		return mtpRespOK, nil, nil
	}
	if !s.sessionOpen() {
		if err := s.drainIf(c); err != nil {
			return 0, nil, err
		}
		return mtpRespSessionNotOpen, nil, nil
	}
	switch c.code {
	case mtpOpCloseSession:
		s.endSession()
		return mtpRespOK, nil, nil
	case mtpOpGetDevicePropDesc:
		return s.opDevicePropDesc(c)
	case mtpOpGetDevicePropValue:
		return s.opDevicePropValue(c)
	case mtpOpSetDevicePropValue:
		return s.opSetDevicePropValue(c)
	case mtpOpGetStorageIDs:
		ids := []uint32{mtpStorageID}
		if s.cfg.locked() {
			ids = nil
		}
		w := &mtpW{}
		w.u32s(ids)
		return s.reply(c, w.b)
	}
	if s.cfg.locked() {
		if err := s.drainIf(c); err != nil {
			return 0, nil, err
		}
		return mtpRespStoreNotAvailable, nil, nil
	}
	switch c.code {
	case mtpOpGetStorageInfo:
		return s.opStorageInfo(c)
	case mtpOpGetObjectHandles, mtpOpGetNumObjects:
		return s.opObjectHandles(c)
	case mtpOpGetObjectInfo:
		return s.opObjectInfo(c)
	case mtpOpGetObject:
		return s.opGetObject(c, false)
	case mtpOpGetPartialObject:
		return s.opGetObject(c, true)
	case mtpOpDeleteObject:
		return s.opDelete(c)
	case mtpOpSendObjectInfo:
		return s.opSendObjectInfo(c)
	case mtpOpSendObject:
		return s.opSendObject(c)
	case mtpOpSendObjectPropList:
		return s.opSendObjectPropList(c)
	case mtpOpMoveObject:
		return s.opMoveCopy(c, false)
	case mtpOpCopyObject:
		return s.opMoveCopy(c, true)
	case mtpOpGetObjectPropsSupported:
		w := &mtpW{}
		w.u16s(mtpObjectProps)
		return s.reply(c, w.b)
	case mtpOpGetObjectPropDesc:
		return s.opObjectPropDesc(c)
	case mtpOpGetObjectPropValue:
		return s.opObjectPropValue(c)
	case mtpOpSetObjectPropValue:
		return s.opSetObjectPropValue(c)
	case mtpOpGetObjectPropList:
		return s.opObjectPropList(c)
	case mtpOpGetObjectReferences:
		if _, _, _, code := s.lookup(c.p[0]); code != mtpRespOK {
			return code, nil, nil
		}
		w := &mtpW{}
		w.u32s(nil)
		return s.reply(c, w.b)
	case mtpOpSetObjectReferences:
		if err := s.drainIf(c); err != nil {
			return 0, nil, err
		}
		return mtpRespOK, nil, nil
	}
	return mtpRespOperationNotSupported, nil, nil
}

// reply sends payload as the data phase and answers OK.
func (s *mtpServer) reply(c mtpCmd, payload []byte) (uint16, []uint32, error) {
	if _, err := s.sendStream(c, int64(len(payload)), bytes.NewReader(payload)); err != nil {
		return 0, nil, err
	}
	return mtpRespOK, nil, nil
}

// ── Containers ───────────────────────────────────────────────────────────

func mtpPutHeader(b []byte, length uint32, typ, code uint16, tid uint32) {
	binary.LittleEndian.PutUint32(b[0:], length)
	binary.LittleEndian.PutUint16(b[4:], typ)
	binary.LittleEndian.PutUint16(b[6:], code)
	binary.LittleEndian.PutUint32(b[8:], tid)
}

func (s *mtpServer) respond(tid uint32, code uint16, params []uint32) error {
	b := make([]byte, mtpHeaderLen+4*len(params))
	mtpPutHeader(b, uint32(len(b)), mtpContainerResponse, code, tid)
	for i, p := range params {
		binary.LittleEndian.PutUint32(b[mtpHeaderLen+4*i:], p)
	}
	return s.writeAll(b)
}

func (s *mtpServer) sendEvent(code uint16, params ...uint32) {
	b := make([]byte, mtpHeaderLen+4*len(params))
	mtpPutHeader(b, uint32(len(b)), mtpContainerEvent, code, 0)
	for i, p := range params {
		binary.LittleEndian.PutUint32(b[mtpHeaderLen+4*i:], p)
	}
	s.t.event(b)
}

// ioErr names a transport error: a cancel the computer asked for wins.
func (s *mtpServer) ioErr(err error) error {
	if s.cancel.Load() {
		return errMTPCancelled
	}
	return err
}

func (s *mtpServer) writeAll(p []byte) error {
	n, err := s.t.write(p)
	if err != nil {
		return s.ioErr(err)
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

// sendStream sends size bytes from src as the data phase. Every write but
// the last fills the buffer, so the computer sees one unbroken transfer; a
// length that is a whole number of packets ends with a zero-length packet.
// A source that ends early (a file that shrank) is padded with zeros so the
// promised length still arrives; short reports it.
func (s *mtpServer) sendStream(c mtpCmd, size int64, src io.Reader) (short bool, err error) {
	total := int64(mtpHeaderLen) + size
	length := uint32(mtpAll) // 4 GiB and over: "read until it ends"
	if total < mtpAll {
		length = uint32(total)
	}
	buf := s.buf
	mtpPutHeader(buf, length, mtpContainerData, c.code, c.tid)
	fill, left := mtpHeaderLen, size
	var srcErr error
	for {
		for fill < len(buf) && left > 0 {
			want := len(buf) - fill
			if int64(want) > left {
				want = int(left)
			}
			got := 0
			if srcErr == nil {
				got, srcErr = io.ReadFull(src, buf[fill:fill+want])
			}
			if got < want {
				clear(buf[fill+got : fill+want])
				short = true
			}
			fill += want
			left -= int64(want)
		}
		if s.cancel.Load() {
			return short, errMTPCancelled
		}
		if err := s.writeAll(buf[:fill]); err != nil {
			return short, err
		}
		fill = 0
		if left == 0 {
			break
		}
	}
	if mp := int64(s.t.maxPacket()); mp > 0 && total%mp == 0 {
		if err := s.writeAll(nil); err != nil {
			return short, err
		}
	}
	return short, nil
}

// mtpIn is the data phase coming from the computer.
type mtpIn struct {
	s       *mtpServer
	pending []byte
	left    int64 // container bytes still to come; -1: unknown, ends with a short transfer
	done    bool

	// shortEnds: the length is only announced, so a short transfer also
	// ends the data phase.
	shortEnds bool
}

// recvBegin reads the start of the data phase. expect is the payload size
// announced beforehand (-1 when none): it stands in for the length field
// when that says "4 GiB or more, read until it ends".
func (s *mtpServer) recvBegin(expect int64) (*mtpIn, error) {
	n := 0
	for n == 0 {
		var err error
		if n, err = s.t.read(s.buf); err != nil {
			return nil, s.ioErr(err)
		}
	}
	if n < mtpHeaderLen || binary.LittleEndian.Uint16(s.buf[4:]) != mtpContainerData {
		return nil, errMTPProtocol
	}
	in := &mtpIn{s: s}
	length := binary.LittleEndian.Uint32(s.buf[0:])
	switch {
	case length == mtpAll && expect >= 0:
		// The announced size says where it ends; a short transfer after
		// the header ends it early (the computer sent less than it said).
		total := expect + mtpHeaderLen
		if int64(n) > total {
			n = int(total)
		}
		in.left = total - int64(n)
		in.shortEnds = true
		in.done = in.left == 0 || (n > mtpHeaderLen && n < len(s.buf))
	case length == mtpAll:
		in.left = -1
		in.done = n < len(s.buf)
	case length < mtpHeaderLen:
		return nil, errMTPProtocol
	default:
		if int64(n) > int64(length) {
			n = int(length)
		}
		in.left = int64(length) - int64(n)
		in.done = in.left == 0
	}
	in.pending = s.buf[mtpHeaderLen:n]
	return in, nil
}

// fill reads the next piece; io.EOF once the data phase is over.
func (in *mtpIn) fill() error {
	if in.done {
		return io.EOF
	}
	if in.s.cancel.Load() {
		return errMTPCancelled
	}
	m, err := in.s.t.read(in.s.buf)
	if err != nil {
		return in.s.ioErr(err)
	}
	if in.left < 0 {
		in.done = m < len(in.s.buf)
	} else {
		if m == 0 {
			in.done = true
			return io.ErrUnexpectedEOF
		}
		short := m < len(in.s.buf)
		if int64(m) > in.left {
			m = int(in.left)
		}
		in.left -= int64(m)
		in.done = in.left == 0 || (in.shortEnds && short)
	}
	in.pending = in.s.buf[:m]
	return nil
}

// copyTo writes the rest of the data phase to w. readErr is the USB side
// (the transfer is lost), writeErr the card's (the rest must be drained).
func (in *mtpIn) copyTo(w io.Writer) (n int64, readErr, writeErr error) {
	for {
		if len(in.pending) == 0 {
			if err := in.fill(); err != nil {
				if err == io.EOF {
					return n, nil, nil
				}
				return n, err, nil
			}
			continue
		}
		k, err := w.Write(in.pending)
		n += int64(k)
		in.pending = in.pending[k:]
		if err != nil {
			return n, nil, err
		}
	}
}

// drain reads the rest of the data phase and drops it.
func (in *mtpIn) drain() error {
	_, readErr, _ := in.copyTo(io.Discard)
	return readErr
}

// readAll reads a whole dataset. One over limit is drained and refused with
// errMTPTooBig.
func (in *mtpIn) readAll(limit int) ([]byte, error) {
	var out []byte
	for {
		if len(in.pending) == 0 {
			if err := in.fill(); err != nil {
				if err == io.EOF {
					return out, nil
				}
				return nil, err
			}
			continue
		}
		if len(out)+len(in.pending) > limit {
			if err := in.drain(); err != nil {
				return nil, err
			}
			return nil, errMTPTooBig
		}
		out = append(out, in.pending...)
		in.pending = nil
	}
}

// drainIf reads and drops the data phase of a command that has one.
func (s *mtpServer) drainIf(c mtpCmd) error {
	if !mtpHostSendsData(c.code) {
		return nil
	}
	expect := int64(-1)
	if c.code == mtpOpSendObject && s.pending != nil {
		expect = s.pending.size
	}
	in, err := s.recvBegin(expect)
	if err != nil {
		return err
	}
	return in.drain()
}

// recvDataset reads a command's dataset. ok false with a nil error means it
// was too large and has been drained.
func (s *mtpServer) recvDataset() (data []byte, ok bool, err error) {
	in, err := s.recvBegin(-1)
	if err != nil {
		return nil, false, err
	}
	data, err = in.readAll(mtpMaxDataset)
	if errors.Is(err, errMTPTooBig) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// ── Encoding ─────────────────────────────────────────────────────────────

// mtpW builds a dataset, little-endian.
type mtpW struct{ b []byte }

func (w *mtpW) u8(v uint8) { w.b = append(w.b, v) }
func (w *mtpW) u16(v uint16) {
	w.b = binary.LittleEndian.AppendUint16(w.b, v)
}
func (w *mtpW) u32(v uint32) {
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}
func (w *mtpW) u64(v uint64) {
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}

// str writes a PTP string: a count of UTF-16 units including the closing
// zero, then the units. The empty string is the single byte 0.
func (w *mtpW) str(s string) {
	units := utf16.Encode([]rune(s))
	if len(units) > 254 {
		units = units[:254]
		if utf16.IsSurrogate(rune(units[253])) && units[253] < 0xDC00 {
			units = units[:253] // do not end on half a pair
		}
	}
	if len(units) == 0 {
		w.u8(0)
		return
	}
	w.u8(uint8(len(units) + 1))
	for _, u := range units {
		w.u16(u)
	}
	w.u16(0)
}

func (w *mtpW) u16s(vs []uint16) {
	w.u32(uint32(len(vs)))
	for _, v := range vs {
		w.u16(v)
	}
}

func (w *mtpW) u32s(vs []uint32) {
	w.u32(uint32(len(vs)))
	for _, v := range vs {
		w.u32(v)
	}
}

// mtpReadString decodes a PTP string at b[off:]; next is the offset after it.
func mtpReadString(b []byte, off int) (s string, next int, ok bool) {
	if off < 0 || off >= len(b) {
		return "", off, false
	}
	count := int(b[off])
	off++
	if count == 0 {
		return "", off, true
	}
	if off+2*count > len(b) {
		return "", off, false
	}
	units := make([]uint16, 0, count)
	for i := 0; i < count; i++ {
		u := binary.LittleEndian.Uint16(b[off+2*i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units)), off + 2*count, true
}

// mtpDate is the MTP date-time form, local time: 20261117T213005.
func mtpDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("20060102T150405")
}

// mtpParseDate reads a date the computer sent; the zero time when it sent
// none or something else.
func mtpParseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if len(s) < 15 {
		return time.Time{}
	}
	base := s[:15]
	rest := s[15:]
	if strings.HasPrefix(rest, ".") && len(rest) >= 2 {
		rest = rest[2:] // tenths of a second
	}
	if rest == "Z" {
		if t, err := time.ParseInLocation("20060102T150405", base, time.UTC); err == nil {
			return t
		}
		return time.Time{}
	}
	if t, err := time.ParseInLocation("20060102T150405", base, time.Local); err == nil {
		return t
	}
	return time.Time{}
}

// mtpObjectInfo is the part of an ObjectInfo dataset the handheld uses.
type mtpObjectInfo struct {
	format uint16
	size   uint32
	name   string
	mod    time.Time
}

// mtpParseObjectInfo reads the dataset SendObjectInfo carries: 52 bytes of
// fixed fields, then the file name, capture date and modification date.
func mtpParseObjectInfo(b []byte) (mtpObjectInfo, bool) {
	const nameOff = 52
	if len(b) < nameOff+1 {
		return mtpObjectInfo{}, false
	}
	oi := mtpObjectInfo{
		format: binary.LittleEndian.Uint16(b[4:]),
		size:   binary.LittleEndian.Uint32(b[8:]),
	}
	name, off, ok := mtpReadString(b, nameOff)
	if !ok {
		return mtpObjectInfo{}, false
	}
	oi.name = name
	if _, off2, ok := mtpReadString(b, off); ok { // capture date
		if mod, _, ok := mtpReadString(b, off2); ok {
			oi.mod = mtpParseDate(mod)
		}
	}
	return oi, true
}

// mtpSkipValue returns where a value of type typ at b[off:] ends; false for
// a type it does not know or a value that runs past the end.
func mtpSkipValue(b []byte, off int, typ uint16) (int, bool) {
	scalar := func(t uint16) int {
		switch t {
		case 0x0001, 0x0002:
			return 1
		case 0x0003, 0x0004:
			return 2
		case 0x0005, 0x0006:
			return 4
		case 0x0007, 0x0008:
			return 8
		case 0x0009, 0x000A:
			return 16
		}
		return 0
	}
	switch {
	case typ == mtpDataTypeString:
		_, next, ok := mtpReadString(b, off)
		return next, ok
	case typ&0x4000 != 0 && scalar(typ&^0x4000) > 0: // an array
		if off < 0 || off+4 > len(b) {
			return off, false
		}
		n := int64(binary.LittleEndian.Uint32(b[off:]))
		size := int64(scalar(typ &^ 0x4000))
		if n > int64(len(b)-off-4)/size {
			return off, false
		}
		return off + 4 + int(n*size), true
	case scalar(typ) > 0:
		end := off + scalar(typ)
		return end, off >= 0 && end <= len(b)
	}
	return off, false
}

// mtpParsePropListName reads the file name and modification date out of
// the property list SendObjectPropList carries: a count, then per element
// a handle, a property code, a data type and the value.
func mtpParsePropListName(b []byte) (name string, mod time.Time, ok bool) {
	if len(b) < 4 {
		return "", time.Time{}, false
	}
	count := int64(binary.LittleEndian.Uint32(b))
	off := 4
	var fileName, plainName string
	for i := int64(0); i < count; i++ {
		if off+8 > len(b) {
			return "", time.Time{}, false
		}
		prop := binary.LittleEndian.Uint16(b[off+4:])
		typ := binary.LittleEndian.Uint16(b[off+6:])
		off += 8
		if typ == mtpDataTypeString {
			v, next, okStr := mtpReadString(b, off)
			if !okStr {
				return "", time.Time{}, false
			}
			switch prop {
			case mtpPropFileName:
				fileName = v
			case mtpPropName:
				plainName = v
			case mtpPropDateModified:
				mod = mtpParseDate(v)
			}
			off = next
			continue
		}
		next, okValue := mtpSkipValue(b, off, typ)
		if !okValue {
			return "", time.Time{}, false
		}
		off = next
	}
	if fileName == "" {
		fileName = plainName
	}
	return fileName, mod, fileName != ""
}

// mtpCleanName accepts a name from the computer only as one plain path
// element: no separators, no "." or "..", no control characters, at most 255
// bytes. Anything else is refused, never repaired.
func mtpCleanName(name string) (string, bool) {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F || r == '/' || r == '\\' {
			return "", false
		}
	}
	if !filepath.IsLocal(name) || filepath.Base(name) != name || mtpHidden(name) {
		return "", false
	}
	return name, true
}

// mtpHidden reports a card entry the computer is not shown: a copy still
// coming in.
func mtpHidden(name string) bool {
	return strings.HasPrefix(name, mtpPartPrefix) && strings.HasSuffix(name, mtpPartSuffix)
}

// mtpInside reports whether path is the root or below it.
func mtpInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}

// ── Objects ──────────────────────────────────────────────────────────────

// add returns the handle for parent/name, making one the first time.
func (s *mtpServer) add(parent uint32, name string, dir bool) uint32 {
	k := mtpKey{parent, name}
	if h, ok := s.index[k]; ok {
		if o := s.objs[h]; o != nil {
			o.dir = dir
			return h
		}
	}
	h := s.next
	s.next++
	if s.next == 0 || s.next == mtpAll {
		s.next = 1
	}
	o := &mtpObject{parent: parent, name: name, dir: dir}
	rel := name
	if parentPath, ok := s.pathOf(parent); ok {
		if r, err := filepath.Rel(s.cfg.root, filepath.Join(parentPath, name)); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	sum := sha1.Sum([]byte(rel))
	copy(o.uid[:], sum[:16])
	s.objs[h] = o
	s.index[k] = h
	return h
}

// isUnder reports whether h is ancestor or below it.
func (s *mtpServer) isUnder(h, ancestor uint32) bool {
	for depth := 0; h != 0 && depth < 512; depth++ {
		if h == ancestor {
			return true
		}
		o := s.objs[h]
		if o == nil {
			return false
		}
		h = o.parent
	}
	return false
}

// forget drops h and everything below it. The doomed handles are collected
// first: deleting while walking would cut the parent chains still being
// walked.
func (s *mtpServer) forget(h uint32) {
	var doomed []uint32
	for k := range s.objs {
		if s.isUnder(k, h) {
			doomed = append(doomed, k)
		}
	}
	for _, k := range doomed {
		if o := s.objs[k]; o != nil {
			if s.index[mtpKey{o.parent, o.name}] == k {
				delete(s.index, mtpKey{o.parent, o.name})
			}
			delete(s.objs, k)
		}
	}
}

// rename moves h to parent/name in the table.
func (s *mtpServer) rename(h, parent uint32, name string) {
	o := s.objs[h]
	if o == nil {
		return
	}
	if old, ok := s.index[mtpKey{parent, name}]; ok && old != h {
		s.forget(old)
	}
	delete(s.index, mtpKey{o.parent, o.name})
	o.parent, o.name = parent, name
	s.index[mtpKey{parent, name}] = h
}

// pathOf is h's place on the card.
func (s *mtpServer) pathOf(h uint32) (string, bool) {
	if h == 0 {
		return s.cfg.root, true
	}
	var parts []string
	for depth := 0; h != 0; depth++ {
		o := s.objs[h]
		if o == nil || depth > 512 {
			return "", false
		}
		parts = append(parts, o.name)
		h = o.parent
	}
	elems := make([]string, 0, len(parts)+1)
	elems = append(elems, s.cfg.root)
	for i := len(parts) - 1; i >= 0; i-- {
		elems = append(elems, parts[i])
	}
	p := filepath.Join(elems...)
	if !mtpInside(s.cfg.root, p) {
		return "", false
	}
	return p, true
}

// parentParam reads a "parent" parameter: 0 or 0xFFFFFFFF is the top level,
// anything else must be a folder the computer knows.
func (s *mtpServer) parentParam(p uint32) (uint32, bool) {
	if p == 0 || p == mtpAll {
		return 0, true
	}
	o, ok := s.objs[p]
	if !ok || !o.dir {
		return 0, false
	}
	return p, true
}

// lookup finds h on the card. An object that is gone is forgotten.
func (s *mtpServer) lookup(h uint32) (*mtpObject, string, os.FileInfo, uint16) {
	o, ok := s.objs[h]
	if !ok || h == 0 {
		return nil, "", nil, mtpRespInvalidObjectHandle
	}
	path, ok := s.pathOf(h)
	if !ok {
		return nil, "", nil, mtpRespInvalidObjectHandle
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		if s.pending == nil || s.pending.handle != h {
			s.forget(h)
		}
		return nil, "", nil, mtpRespInvalidObjectHandle
	}
	o.dir = info.IsDir()
	return o, path, info, mtpRespOK
}

// listDir lists a folder: its files and folders, never links or devices,
// in name order. Children the card no longer has are forgotten.
func (s *mtpServer) listDir(parent uint32) ([]uint32, bool) {
	dir, ok := s.pathOf(parent)
	if !ok {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	seen := make(map[uint32]bool, len(entries))
	out := make([]uint32, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		t := e.Type()
		if mtpHidden(name) {
			// A copy-in that never finished (the handheld lost power or
			// JukaHub stopped mid-copy). Nothing is mid-copy while a folder
			// is listed; the age is a second guard.
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 10*time.Minute {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if t&os.ModeSymlink != 0 {
			continue
		}
		if !e.IsDir() && !t.IsRegular() {
			continue
		}
		h := s.add(parent, name, e.IsDir())
		seen[h] = true
		out = append(out, h)
	}
	var gone []uint32
	for h, o := range s.objs {
		// The file SendObjectInfo announced does not exist until SendObject.
		if o.parent == parent && !seen[h] && (s.pending == nil || s.pending.handle != h) {
			if _, err := os.Lstat(filepath.Join(dir, o.name)); err != nil {
				gone = append(gone, h)
			}
		}
	}
	for _, h := range gone {
		s.forget(h)
	}
	return out, true
}

func mtpFormatOf(o *mtpObject) uint16 {
	if o.dir {
		return mtpFormatAssociation
	}
	if f, ok := mtpFormatByExt[strings.ToLower(filepath.Ext(o.name))]; ok {
		return f
	}
	return mtpFormatUndefined
}

func mtpSize32(info os.FileInfo) uint32 {
	if info.IsDir() {
		return 0
	}
	if info.Size() >= mtpAll {
		return mtpAll
	}
	return uint32(info.Size())
}

// ── Device and storage ───────────────────────────────────────────────────

func (s *mtpServer) opDeviceInfo(c mtpCmd) (uint16, []uint32, error) {
	w := &mtpW{}
	w.u16(100) // standard version 1.00
	w.u32(6)   // Microsoft's vendor extension: this is MTP, not a camera
	w.u16(100)
	w.str("microsoft.com: 1.0;")
	w.u16(0) // functional mode
	w.u16s(mtpOperations)
	w.u16s([]uint16{mtpEventStoreAdded, mtpEventStoreRemoved})
	w.u16s([]uint16{mtpDevPropSyncPartner, mtpDevPropFriendlyName})
	w.u16s(nil) // capture formats
	w.u16s(mtpFormats)
	w.str("JukaHub")
	w.str(s.cfg.name())
	w.str("1.0")
	w.str(s.cfg.serial)
	return s.reply(c, w.b)
}

func (s *mtpServer) opDevicePropDesc(c mtpCmd) (uint16, []uint32, error) {
	w := &mtpW{}
	switch c.p[0] {
	case mtpDevPropFriendlyName:
		name := s.cfg.name()
		w.u16(mtpDevPropFriendlyName)
		w.u16(mtpDataTypeString)
		w.u8(0) // read-only
		w.str(name)
		w.str(name)
		w.u8(0)
	case mtpDevPropSyncPartner:
		w.u16(mtpDevPropSyncPartner)
		w.u16(mtpDataTypeString)
		w.u8(1)
		w.str("")
		w.str(s.partner)
		w.u8(0)
	default:
		return mtpRespDevicePropNotSupported, nil, nil
	}
	return s.reply(c, w.b)
}

func (s *mtpServer) opDevicePropValue(c mtpCmd) (uint16, []uint32, error) {
	w := &mtpW{}
	switch c.p[0] {
	case mtpDevPropFriendlyName:
		w.str(s.cfg.name())
	case mtpDevPropSyncPartner:
		w.str(s.partner)
	default:
		return mtpRespDevicePropNotSupported, nil, nil
	}
	return s.reply(c, w.b)
}

func (s *mtpServer) opSetDevicePropValue(c mtpCmd) (uint16, []uint32, error) {
	data, ok, err := s.recvDataset()
	if err != nil {
		return 0, nil, err
	}
	switch c.p[0] {
	case mtpDevPropSyncPartner:
		if !ok {
			return mtpRespInvalidDataset, nil, nil
		}
		if v, _, ok := mtpReadString(data, 0); ok {
			s.partner = v
			return mtpRespOK, nil, nil
		}
		return mtpRespInvalidDataset, nil, nil
	case mtpDevPropFriendlyName:
		return mtpRespAccessDenied, nil, nil
	}
	return mtpRespDevicePropNotSupported, nil, nil
}

func (s *mtpServer) opStorageInfo(c mtpCmd) (uint16, []uint32, error) {
	if c.p[0] != mtpStorageID {
		return mtpRespInvalidStorageID, nil, nil
	}
	total, free, _ := s.cfg.space(s.cfg.root)
	w := &mtpW{}
	w.u16(mtpStorageRemovableRAM)
	w.u16(mtpFilesystemHierarchic)
	w.u16(0) // read-write
	w.u64(total)
	w.u64(free)
	w.u32(mtpAll) // free space in objects: not counted
	w.str("SD card")
	w.str("")
	return s.reply(c, w.b)
}

// ── Listing and reading ──────────────────────────────────────────────────

func (s *mtpServer) opObjectHandles(c mtpCmd) (uint16, []uint32, error) {
	if c.p[0] != mtpStorageID && c.p[0] != mtpAll {
		return mtpRespInvalidStorageID, nil, nil
	}
	// Parent 0 means "every object on the storage"; the whole card at once
	// would take minutes, so it is answered like the top level, as phones do.
	parent, ok := s.parentParam(c.p[2])
	if !ok {
		return mtpRespInvalidParentObject, nil, nil
	}
	hs, ok := s.listDir(parent)
	if !ok {
		return mtpRespInvalidParentObject, nil, nil
	}
	if f := c.p[1]; f != 0 && f != mtpAll {
		kept := hs[:0]
		for _, h := range hs {
			if uint32(mtpFormatOf(s.objs[h])) == f {
				kept = append(kept, h)
			}
		}
		hs = kept
	}
	if c.code == mtpOpGetNumObjects {
		return mtpRespOK, []uint32{uint32(len(hs))}, nil
	}
	w := &mtpW{}
	w.u32s(hs)
	return s.reply(c, w.b)
}

func (s *mtpServer) opObjectInfo(c mtpCmd) (uint16, []uint32, error) {
	o, _, info, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	date := mtpDate(info.ModTime())
	assoc := uint16(0)
	if o.dir {
		assoc = mtpAssociationGeneric
	}
	w := &mtpW{}
	w.u32(mtpStorageID)
	w.u16(mtpFormatOf(o))
	w.u16(0) // protection
	w.u32(mtpSize32(info))
	w.u16(0) // thumbnail format
	for i := 0; i < 6; i++ {
		w.u32(0) // thumbnail size and pixels, image pixels and depth
	}
	w.u32(o.parent)
	w.u16(assoc)
	w.u32(0) // association description
	w.u32(0) // sequence number
	w.str(o.name)
	w.str(date)
	w.str(date)
	w.str("")
	return s.reply(c, w.b)
}

func (s *mtpServer) opGetObject(c mtpCmd, partial bool) (uint16, []uint32, error) {
	_, path, info, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	if info.IsDir() {
		return mtpRespInvalidObjectHandle, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return mtpRespAccessDenied, nil, nil
	}
	defer f.Close()
	size := info.Size()
	if partial {
		off := int64(c.p[1])
		if off > size {
			return mtpRespInvalidParameter, nil, nil
		}
		size -= off
		if limit := int64(c.p[2]); limit < size {
			size = limit
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return mtpRespGeneralError, nil, nil
		}
	}
	short, err := s.sendStream(c, size, f)
	if err != nil {
		return 0, nil, err
	}
	if short {
		return mtpRespIncompleteTransfer, nil, nil
	}
	if partial {
		return mtpRespOK, []uint32{uint32(size)}, nil
	}
	return mtpRespOK, nil, nil
}

// ── Changing the card ────────────────────────────────────────────────────

func (s *mtpServer) opDelete(c mtpCmd) (uint16, []uint32, error) {
	if c.p[0] == mtpAll {
		return mtpRespAccessDenied, nil, nil // "delete everything" is not offered
	}
	// Already gone — a cancelled copy-in the computer tidies up, or a file
	// deleted on the handheld — is what was asked for.
	if _, known := s.objs[c.p[0]]; known && c.p[0] != 0 {
		if p, ok := s.pathOf(c.p[0]); ok {
			if _, err := os.Lstat(p); os.IsNotExist(err) {
				s.forget(c.p[0])
				return mtpRespOK, nil, nil
			}
		}
	}
	o, path, _, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	var err error
	if o.dir {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			if o.dir {
				return mtpRespPartialDeletion, nil, nil
			}
			return mtpRespAccessDenied, nil, nil
		}
	}
	s.forget(c.p[0])
	libIndexNoteChange(path) // the library drops it (libindex.go)
	return mtpRespOK, nil, nil
}

func (s *mtpServer) opSendObjectInfo(c mtpCmd) (uint16, []uint32, error) {
	data, ok, err := s.recvDataset()
	if err != nil {
		return 0, nil, err
	}
	s.pending = nil
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	oi, ok := mtpParseObjectInfo(data)
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	size := int64(oi.size)
	if oi.size == mtpAll {
		size = -1
	}
	return s.announce(c.p[0], c.p[1], oi.format == mtpFormatAssociation, oi.name, size, oi.mod)
}

// opSendObjectPropList is SendObjectInfo as a property list, with a 64-bit
// size: Windows uses it when offered, and needs it for files of 4 GiB and
// more. Parameters: storage, parent, format, size high, size low.
func (s *mtpServer) opSendObjectPropList(c mtpCmd) (uint16, []uint32, error) {
	data, ok, err := s.recvDataset()
	if err != nil {
		return 0, nil, err
	}
	s.pending = nil
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	name, mod, ok := mtpParsePropListName(data)
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	size := int64(c.p[3])<<32 | int64(c.p[4])
	return s.announce(c.p[0], c.p[1], c.p[2] == mtpFormatAssociation, name, size, mod)
}

// announce makes a folder at once, or readies the file the next SendObject
// fills. The response names the storage, the parent (0 for the top level)
// and the new handle.
func (s *mtpServer) announce(storage, parentParam uint32, isDir bool, rawName string, size int64, mod time.Time) (uint16, []uint32, error) {
	if storage != 0 && storage != mtpStorageID && storage != mtpAll {
		return mtpRespInvalidStorageID, nil, nil
	}
	parent, ok := s.parentParam(parentParam)
	if !ok {
		return mtpRespInvalidParentObject, nil, nil
	}
	name, ok := mtpCleanName(rawName)
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	dir, ok := s.pathOf(parent)
	if !ok {
		return mtpRespInvalidParentObject, nil, nil
	}
	target := filepath.Join(dir, name)
	if !mtpInside(s.cfg.root, target) {
		return mtpRespAccessDenied, nil, nil
	}
	existing, statErr := os.Lstat(target)
	if statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
		return mtpRespAccessDenied, nil, nil
	}
	if isDir {
		if statErr == nil && !existing.IsDir() {
			return mtpRespAccessDenied, nil, nil
		}
		if statErr != nil {
			if err := os.Mkdir(target, 0o755); err != nil {
				return mtpRespAccessDenied, nil, nil
			}
		}
		h := s.add(parent, name, true)
		return mtpRespOK, []uint32{mtpStorageID, parent, h}, nil
	}
	if statErr == nil && existing.IsDir() {
		return mtpRespAccessDenied, nil, nil
	}
	if size >= 0 {
		if _, free, ok := s.cfg.space(dir); ok && uint64(size) > free {
			return mtpRespStoreFull, nil, nil
		}
	}
	h := s.add(parent, name, false)
	s.pending = &mtpPendingSend{handle: h, size: size, mod: mod}
	return mtpRespOK, []uint32{mtpStorageID, parent, h}, nil
}

func (s *mtpServer) opSendObject(c mtpCmd) (uint16, []uint32, error) {
	p := s.pending
	s.pending = nil
	expect := int64(-1)
	if p != nil {
		expect = p.size
	}
	in, err := s.recvBegin(expect)
	if err != nil {
		return 0, nil, err
	}
	path, ok := "", false
	if p != nil {
		path, ok = s.pathOf(p.handle)
	}
	if !ok {
		if err := in.drain(); err != nil {
			return 0, nil, err
		}
		return mtpRespNoValidObjectInfo, nil, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), mtpPartPrefix+"*"+mtpPartSuffix)
	if err != nil {
		if err := in.drain(); err != nil {
			return 0, nil, err
		}
		return mtpErrCode(err), nil, nil
	}
	n, readErr, writeErr := in.copyTo(tmp)
	closeErr := tmp.Close()
	if readErr != nil || writeErr != nil || closeErr != nil {
		_ = os.Remove(tmp.Name())
		if readErr != nil {
			return 0, nil, readErr
		}
		if writeErr != nil {
			if err := in.drain(); err != nil {
				return 0, nil, err
			}
			return mtpErrCode(writeErr), nil, nil
		}
		return mtpErrCode(closeErr), nil, nil
	}
	if p.size >= 0 && n != p.size {
		_ = os.Remove(tmp.Name())
		return mtpRespIncompleteTransfer, nil, nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return mtpRespAccessDenied, nil, nil
	}
	_ = os.Chmod(path, 0o644)
	if !p.mod.IsZero() {
		_ = os.Chtimes(path, p.mod, p.mod)
	}
	libIndexNoteChange(path) // a game copied in shows in the library (libindex.go)
	return mtpRespOK, nil, nil
}

// mtpErrCode turns a card error into a response: a full card says so.
func mtpErrCode(err error) uint16 {
	if errors.Is(err, syscall.ENOSPC) {
		return mtpRespStoreFull
	}
	return mtpRespGeneralError
}

func (s *mtpServer) opMoveCopy(c mtpCmd, copyIt bool) (uint16, []uint32, error) {
	o, path, _, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	if c.p[1] != mtpStorageID && c.p[1] != 0 {
		return mtpRespInvalidStorageID, nil, nil
	}
	parent, ok := s.parentParam(c.p[2])
	if !ok || (o.dir && s.isUnder(parent, c.p[0])) {
		return mtpRespInvalidParentObject, nil, nil
	}
	dir, ok := s.pathOf(parent)
	if !ok {
		return mtpRespInvalidParentObject, nil, nil
	}
	dst := filepath.Join(dir, o.name)
	if dst == path {
		if copyIt {
			return mtpRespAccessDenied, nil, nil
		}
		return mtpRespOK, nil, nil
	}
	if _, err := os.Lstat(dst); err == nil {
		return mtpRespAccessDenied, nil, nil
	}
	if !copyIt {
		if err := os.Rename(path, dst); err != nil {
			return mtpRespAccessDenied, nil, nil
		}
		s.rename(c.p[0], parent, o.name)
		return mtpRespOK, nil, nil
	}
	if err := mtpCopyTree(path, dst); err != nil {
		_ = os.RemoveAll(dst)
		return mtpErrCode(err), nil, nil
	}
	h := s.add(parent, o.name, o.dir)
	return mtpRespOK, []uint32{h}, nil
}

// mtpCopyTree copies a file or a folder; links and devices are skipped.
func mtpCopyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 || mtpHidden(e.Name()) {
				continue
			}
			if err := mtpCopyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	case info.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	default:
		return nil
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return nil
}

// ── Object properties ────────────────────────────────────────────────────

func mtpPropType(p uint16) (uint16, bool) {
	switch p {
	case mtpPropStorageID, mtpPropParentObject:
		return mtpDataTypeUint32, true
	case mtpPropObjectFormat, mtpPropProtection:
		return mtpDataTypeUint16, true
	case mtpPropObjectSize:
		return mtpDataTypeUint64, true
	case mtpPropFileName, mtpPropDateCreated, mtpPropDateModified, mtpPropName:
		return mtpDataTypeString, true
	case mtpPropPersistentUID:
		return mtpDataTypeUint128, true
	}
	return 0, false
}

// writeProp writes one property's value.
func (s *mtpServer) writeProp(w *mtpW, h uint32, o *mtpObject, info os.FileInfo, p uint16) {
	switch p {
	case mtpPropStorageID:
		w.u32(mtpStorageID)
	case mtpPropObjectFormat:
		w.u16(mtpFormatOf(o))
	case mtpPropProtection:
		w.u16(0)
	case mtpPropObjectSize:
		if info.IsDir() {
			w.u64(0)
		} else {
			w.u64(uint64(info.Size()))
		}
	case mtpPropFileName, mtpPropName:
		w.str(o.name)
	case mtpPropDateCreated, mtpPropDateModified:
		w.str(mtpDate(info.ModTime()))
	case mtpPropParentObject:
		w.u32(o.parent)
	case mtpPropPersistentUID:
		w.b = append(w.b, o.uid[:]...)
	}
}

func (s *mtpServer) opObjectPropDesc(c mtpCmd) (uint16, []uint32, error) {
	p := uint16(c.p[0])
	typ, ok := mtpPropType(p)
	if !ok || c.p[0] > 0xFFFF {
		return mtpRespInvalidObjectPropCode, nil, nil
	}
	w := &mtpW{}
	w.u16(p)
	w.u16(typ)
	if p == mtpPropFileName {
		w.u8(1) // renaming is allowed
	} else {
		w.u8(0)
	}
	switch typ { // factory default
	case mtpDataTypeUint16:
		w.u16(0)
	case mtpDataTypeUint32:
		w.u32(0)
	case mtpDataTypeUint64:
		w.u64(0)
	case mtpDataTypeUint128:
		w.u64(0)
		w.u64(0)
	default:
		w.str("")
	}
	w.u32(0) // group
	w.u8(0)  // no form
	return s.reply(c, w.b)
}

func (s *mtpServer) opObjectPropValue(c mtpCmd) (uint16, []uint32, error) {
	o, _, info, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	if _, ok := mtpPropType(uint16(c.p[1])); !ok || c.p[1] > 0xFFFF {
		return mtpRespInvalidObjectPropCode, nil, nil
	}
	w := &mtpW{}
	s.writeProp(w, c.p[0], o, info, uint16(c.p[1]))
	return s.reply(c, w.b)
}

func (s *mtpServer) opSetObjectPropValue(c mtpCmd) (uint16, []uint32, error) {
	data, ok, err := s.recvDataset()
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return mtpRespInvalidDataset, nil, nil
	}
	o, path, _, code := s.lookup(c.p[0])
	if code != mtpRespOK {
		return code, nil, nil
	}
	p := c.p[1]
	if p != mtpPropFileName && p != mtpPropName {
		if _, known := mtpPropType(uint16(p)); known && p <= 0xFFFF {
			return mtpRespObjectWriteProtectedProp, nil, nil
		}
		return mtpRespObjectPropNotSupported, nil, nil
	}
	raw, _, ok := mtpReadString(data, 0)
	if !ok {
		return mtpRespInvalidObjectPropValue, nil, nil
	}
	name, ok := mtpCleanName(raw)
	if !ok {
		return mtpRespInvalidObjectPropValue, nil, nil
	}
	if name == o.name {
		return mtpRespOK, nil, nil
	}
	dst := filepath.Join(filepath.Dir(path), name)
	// Another file already has that name: refused. A change of case on the
	// card's FAT/exFAT finds the file itself there, which is let through.
	if di, err := os.Lstat(dst); err == nil {
		if si, err := os.Lstat(path); err != nil || !os.SameFile(si, di) {
			return mtpRespAccessDenied, nil, nil
		}
	}
	if err := os.Rename(path, dst); err != nil {
		return mtpRespAccessDenied, nil, nil
	}
	s.rename(c.p[0], o.parent, name)
	return mtpRespOK, nil, nil
}

// opObjectPropList answers GetObjectPropList, which Windows uses to read a
// folder's listing in one go: one object (depth 0) or a folder's children
// (depth 1), every property or one.
func (s *mtpServer) opObjectPropList(c mtpCmd) (uint16, []uint32, error) {
	// Property 0 means "the properties of group p[3]"; groups are not
	// offered.
	if c.p[3] != 0 || c.p[2] == 0 {
		return mtpRespSpecByGroupUnsupported, nil, nil
	}
	props := mtpObjectProps
	if c.p[2] != mtpAll {
		if _, ok := mtpPropType(uint16(c.p[2])); !ok || c.p[2] > 0xFFFF {
			return mtpRespInvalidObjectPropCode, nil, nil
		}
		props = []uint16{uint16(c.p[2])}
	}
	var targets []uint32
	switch c.p[4] {
	case 0:
		if c.p[0] == mtpAll {
			return mtpRespSpecByDepthUnsupported, nil, nil
		}
		if c.p[0] != 0 {
			if _, _, _, code := s.lookup(c.p[0]); code != mtpRespOK {
				return code, nil, nil
			}
			targets = []uint32{c.p[0]}
		}
	case 1:
		parent, ok := s.parentParam(c.p[0])
		if !ok {
			return mtpRespInvalidObjectHandle, nil, nil
		}
		hs, ok := s.listDir(parent)
		if !ok {
			return mtpRespInvalidObjectHandle, nil, nil
		}
		targets = hs
	default:
		return mtpRespSpecByDepthUnsupported, nil, nil
	}
	w := &mtpW{}
	w.u32(0) // element count, filled in below
	count := uint32(0)
	for _, h := range targets {
		o, _, info, code := s.lookup(h)
		if code != mtpRespOK {
			continue
		}
		if f := c.p[1]; f != 0 && f != mtpAll && uint32(mtpFormatOf(o)) != f {
			continue
		}
		for _, p := range props {
			typ, _ := mtpPropType(p)
			w.u32(h)
			w.u16(p)
			w.u16(typ)
			s.writeProp(w, h, o, info, p)
			count++
		}
	}
	binary.LittleEndian.PutUint32(w.b[0:], count)
	return s.reply(c, w.b)
}
