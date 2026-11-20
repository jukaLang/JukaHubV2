package main

// smb2_rpc.go — the share list for smbSession.ListShares: DCE/RPC
// (C706 connection-oriented PDUs, NDR transfer syntax) over the \srvsvc
// named pipe on IPC$, one BIND and one NetrShareEnum call (MS-SRVS 3.1.4.8,
// opnum 15) at info level 1. Disk shares are kept; printers, IPC$, special
// and "$"-hidden shares are dropped. Any refusal (IPC$ closed to guests,
// RPC turned off, a bind or call fault) comes back as errSMBNoShareList so
// the screen can ask for the share name instead.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	smb2RPCRequest  = 0
	smb2RPCResponse = 2
	smb2RPCFault    = 3
	smb2RPCBind     = 11
	smb2RPCBindAck  = 12
	smb2RPCBindNak  = 13

	smb2RPCFirstFrag = 0x01
	smb2RPCLastFrag  = 0x02

	smb2RPCMaxFrag   = 4280
	smb2RPCMaxStub   = 1 << 20
	smb2RPCMaxShares = 10000
)

// Interface ids in wire order (first three UUID fields little-endian).
var (
	// srvsvc 4b324fc8-1670-01d3-1278-5a47bf6ee188
	smb2SrvsvcUUID = []byte{0xc8, 0x4f, 0x32, 0x4b, 0x70, 0x16, 0xd3, 0x01, 0x12, 0x78, 0x5a, 0x47, 0xbf, 0x6e, 0xe1, 0x88}
	// NDR 8a885d04-1ceb-11c9-9fe8-08002b104860
	smb2NDRUUID = []byte{0x04, 0x5d, 0x88, 0x8a, 0xeb, 0x1c, 0xc9, 0x11, 0x9f, 0xe8, 0x08, 0x00, 0x2b, 0x10, 0x48, 0x60}
)

// listShares asks srvsvc for the share list.
func (c *smb2Conn) listShares(ctx context.Context) ([]string, error) {
	names, err := c.srvsvcShareEnum(ctx)
	if err != nil && !c.broken && !errors.Is(err, errSMBEncryptionRequired) {
		return nil, fmt.Errorf("%w: %w", errSMBNoShareList, err)
	}
	return names, err
}

func (c *smb2Conn) srvsvcShareEnum(ctx context.Context) ([]string, error) {
	t, err := c.tree(ctx, "IPC$")
	if err != nil {
		return nil, err
	}
	if !t.pipe {
		return nil, smb2Bad("IPC$ is not a pipe share")
	}
	// Read/write data, attributes and EA; share read+write; open existing.
	o, err := c.create(ctx, t, "srvsvc", smb2AccessPipe, 3, 0)
	if err != nil {
		return nil, err
	}
	defer c.closeFile(ctx, t, o.fid)
	if err := c.writePipe(ctx, t, o.fid, smb2RPCBindPDU()); err != nil {
		return nil, err
	}
	var pending []byte
	pdu, err := c.rpcReadPDU(ctx, t, o.fid, &pending)
	if err != nil {
		return nil, err
	}
	if err := smb2RPCCheckBindAck(pdu); err != nil {
		return nil, err
	}
	if err := c.writePipe(ctx, t, o.fid, smb2RPCRequestPDU(2, 15, smb2SrvsvcEnumStub())); err != nil {
		return nil, err
	}
	var stub []byte
	for {
		pdu, err := c.rpcReadPDU(ctx, t, o.fid, &pending)
		if err != nil {
			return nil, err
		}
		part, last, err := smb2RPCResponseStub(pdu)
		if err != nil {
			return nil, err
		}
		if len(stub)+len(part) > smb2RPCMaxStub {
			return nil, smb2Bad("share list too long")
		}
		stub = append(stub, part...)
		if last {
			break
		}
	}
	return smb2ParseShareEnum(stub)
}

// rpcReadPDU returns the next whole PDU from the pipe, reading more as
// needed; pending keeps bytes past it.
func (c *smb2Conn) rpcReadPDU(ctx context.Context, t *smb2Tree, fid [16]byte, pending *[]byte) ([]byte, error) {
	for reads := 0; ; reads++ {
		if b := *pending; len(b) >= 16 {
			fl := int(binary.LittleEndian.Uint16(b[8:]))
			if fl < 16 {
				return nil, smb2Bad("RPC fragment length")
			}
			if len(b) >= fl {
				*pending = b[fl:]
				return b[:fl], nil
			}
		}
		if reads > 1024 || len(*pending) > smb2RPCMaxStub {
			return nil, smb2Bad("RPC reply too long")
		}
		data, err := c.read(ctx, t, fid, 0, min(c.readSize(), 65536))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, smb2Bad("empty RPC read")
		}
		*pending = append(*pending, data...)
	}
}

// smb2RPCHeader starts a single-fragment PDU of fragLen bytes.
func smb2RPCHeader(ptype byte, callID uint32, fragLen int) []byte {
	h := make([]byte, 16, fragLen)
	h[0] = 5 // RPC version 5.0
	h[2] = ptype
	h[3] = smb2RPCFirstFrag | smb2RPCLastFrag
	h[4] = 0x10 // little-endian integers, ASCII, IEEE floats
	binary.LittleEndian.PutUint16(h[8:], uint16(fragLen))
	binary.LittleEndian.PutUint32(h[12:], callID)
	return h
}

// smb2RPCBindPDU binds srvsvc v3.0 with NDR v2 as context 0.
func smb2RPCBindPDU() []byte {
	b := smb2RPCHeader(smb2RPCBind, 1, 72)
	// max_xmit_frag, max_recv_frag, assoc_group_id
	b = binary.LittleEndian.AppendUint16(b, smb2RPCMaxFrag)
	b = binary.LittleEndian.AppendUint16(b, smb2RPCMaxFrag)
	b = binary.LittleEndian.AppendUint32(b, 0)
	// one presentation context: id 0, one transfer syntax
	b = append(b, 1, 0, 0, 0)
	b = append(b, 0, 0, 1, 0)
	b = append(b, smb2SrvsvcUUID...)
	b = append(b, 3, 0, 0, 0)
	b = append(b, smb2NDRUUID...)
	return append(b, 2, 0, 0, 0)
}

// smb2RPCRequestPDU is a single-fragment REQUEST on context 0.
func smb2RPCRequestPDU(callID uint32, opnum uint16, stub []byte) []byte {
	b := smb2RPCHeader(smb2RPCRequest, callID, 24+len(stub))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(stub)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, opnum)
	return append(b, stub...)
}

// smb2SrvsvcEnumStub is the NDR of NetrShareEnum(ServerName = "",
// InfoStruct = {Level 1, empty container}, PreferedMaximumLength = all,
// ResumeHandle = 0).
func smb2SrvsvcEnumStub() []byte {
	b := make([]byte, 0, 64)
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	u32(0x00020000) // ServerName: unique pointer
	u32(1)          // max count
	u32(0)          // offset
	u32(1)          // actual count: just the NUL
	b = append(b, 0, 0, 0, 0)
	u32(1)          // Level
	u32(1)          // union switch
	u32(0x00020004) // SHARE_INFO_1_CONTAINER pointer
	u32(0)          // EntriesRead
	u32(0)          // Buffer: NULL
	u32(0xffffffff) // PreferedMaximumLength
	u32(0x00020008) // ResumeHandle pointer
	u32(0)          // *ResumeHandle
	return b
}

func smb2RPCCheck(pdu []byte) error {
	if len(pdu) < 16 || pdu[0] != 5 {
		return smb2Bad("RPC reply")
	}
	if pdu[4]&0xf0 != 0x10 {
		return fmt.Errorf("%w: big-endian RPC", errSMBUnsupported)
	}
	return nil
}

// smb2RPCCheckBindAck accepts a BIND_ACK whose first result is acceptance.
func smb2RPCCheckBindAck(pdu []byte) error {
	if err := smb2RPCCheck(pdu); err != nil {
		return err
	}
	switch pdu[2] {
	case smb2RPCBindAck:
	case smb2RPCBindNak:
		return errors.New("srvsvc bind refused")
	default:
		return smb2Bad("RPC bind reply")
	}
	if len(pdu) < 26 {
		return smb2Bad("RPC bind reply")
	}
	off := (26 + int(binary.LittleEndian.Uint16(pdu[24:])) + 3) &^ 3
	if off+8 > len(pdu) || pdu[off] == 0 {
		return smb2Bad("RPC bind results")
	}
	if r := binary.LittleEndian.Uint16(pdu[off+4:]); r != 0 {
		return fmt.Errorf("srvsvc bind not accepted (%d)", r)
	}
	return nil
}

// smb2RPCResponseStub returns the stub data of a RESPONSE fragment.
func smb2RPCResponseStub(pdu []byte) (stub []byte, last bool, err error) {
	if err := smb2RPCCheck(pdu); err != nil {
		return nil, false, err
	}
	switch pdu[2] {
	case smb2RPCResponse:
	case smb2RPCFault:
		code := uint32(0)
		if len(pdu) >= 28 {
			code = binary.LittleEndian.Uint32(pdu[24:])
		}
		return nil, false, fmt.Errorf("srvsvc fault 0x%08x", code)
	default:
		return nil, false, smb2Bad("RPC reply type")
	}
	if len(pdu) < 24 || binary.LittleEndian.Uint16(pdu[10:]) != 0 {
		return nil, false, smb2Bad("RPC response")
	}
	return pdu[24:], pdu[3]&smb2RPCLastFrag != 0, nil
}

// smb2NDR reads little-endian NDR; the first error sticks.
type smb2NDR struct {
	b   []byte
	off int
	err error
}

func (r *smb2NDR) u32() uint32 {
	if r.err != nil {
		return 0
	}
	r.off = (r.off + 3) &^ 3
	if r.off+4 > len(r.b) {
		r.err = smb2Bad("NDR data")
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

// str reads a conformant varying UTF-16 string.
func (r *smb2NDR) str() string {
	maxCount, _, actual := r.u32(), r.u32(), r.u32()
	if r.err != nil {
		return ""
	}
	if actual > maxCount || actual > 4096 || int(actual)*2 > len(r.b)-r.off {
		r.err = smb2Bad("NDR string")
		return ""
	}
	s := smb2FromUTF16(r.b[r.off : r.off+int(actual)*2])
	r.off += int(actual) * 2
	return strings.TrimRight(s, "\x00")
}

// smb2ParseShareEnum reads the NetrShareEnum reply stub (level 1) and keeps
// plain disk shares.
func smb2ParseShareEnum(stub []byte) ([]string, error) {
	if len(stub) < 4 {
		return nil, smb2Bad("NetrShareEnum reply")
	}
	if ret := binary.LittleEndian.Uint32(stub[len(stub)-4:]); ret != 0 && ret != 234 {
		return nil, fmt.Errorf("NetrShareEnum failed (Windows error %d)", ret)
	}
	r := &smb2NDR{b: stub}
	level, _ := r.u32(), r.u32()
	if r.err == nil && level != 1 {
		return nil, smb2Bad("NetrShareEnum level")
	}
	ctr := r.u32()
	if r.err != nil {
		return nil, r.err
	}
	if ctr == 0 {
		return nil, nil
	}
	count, bufPtr := r.u32(), r.u32()
	if r.err != nil {
		return nil, r.err
	}
	if count == 0 || bufPtr == 0 {
		return nil, nil
	}
	maxCount := r.u32()
	if r.err != nil {
		return nil, r.err
	}
	if count > maxCount || count > smb2RPCMaxShares || int(count)*12 > len(stub)-r.off {
		return nil, smb2Bad("NetrShareEnum count")
	}
	type ent struct{ name, typ, remark uint32 }
	ents := make([]ent, count)
	for i := range ents {
		ents[i] = ent{r.u32(), r.u32(), r.u32()}
	}
	var names []string
	seen := map[string]bool{}
	for _, e := range ents {
		name := ""
		if e.name != 0 {
			name = r.str()
		}
		if e.remark != 0 {
			r.str()
		}
		if r.err != nil {
			return nil, r.err
		}
		// STYPE_DISKTREE with no STYPE_SPECIAL bit; "$" shares are hidden.
		if e.typ&0xff != 0 || e.typ&0x80000000 != 0 || strings.HasSuffix(name, "$") {
			continue
		}
		if _, _, err := smb2CleanSharePath(name, ""); err != nil || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	return names, nil
}
