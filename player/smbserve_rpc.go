package main

// smbserve_rpc.go — the \srvsvc pipe on the share's IPC$ (smbserve.go), so
// File Explorer, Finder and smbclient can list \\device's shares: DCE/RPC
// connection-oriented PDUs (C706) with NDR, one BIND and NetrShareEnum
// (MS-SRVS 3.1.4.8, opnum 15) at info level 1. It is the server side of
// smb2_rpc.go and shares its constants and NDR reader.
//
// The list is "JukaHub" (a disk) and "IPC$". Other pipes do not exist,
// other calls get an RPC fault, multi-fragment requests and authenticated
// binds are refused, and the pipe buffers at most smbSrvPipeMax each way.

import (
	"bytes"
	"encoding/binary"
	"strings"
)

const (
	smbSrvPipeMax       = 64 << 10
	smbSrvRPCOpRangeErr = 0x1c010002
	smbSrvRPCAssocGroup = 0x53f0
)

// smbSrvPipe is one open \srvsvc: bytes written and not yet a whole PDU,
// and replies not yet read.
type smbSrvPipe struct {
	in    []byte
	out   []byte
	bound bool
	ctxID uint16
}

// smbSrvBindFeatureUUID starts the bind-time feature negotiation syntax
// (6cb71c2c-9812-4540-…), which is acknowledged, not accepted.
var smbSrvBindFeatureUUID = []byte{0x2c, 0x1c, 0xb7, 0x6c, 0x12, 0x98, 0x40, 0x45}

// createPipe opens a pipe on IPC$; only srvsvc exists.
func (c *smbSrvConn) createPipe(r *smbSrvReq, ch *smbSrvChain, name string, access uint32) (uint32, []byte) {
	if !strings.EqualFold(strings.Trim(name, `\`), "srvsvc") {
		return smb2StatusObjectNameNotFound, nil
	}
	h := &smbSrvHandle{sess: r.sess.id, tree: r.tree.id, rel: "srvsvc", access: smbSrvMapAccess(access, false), pipe: &smbSrvPipe{}}
	c.addHandle(h, ch)
	return smb2StatusSuccess, smbSrvCreateBody(h.id, 1, smbSrvStat{attrs: smbSrvAttrNormal}, false, false, 0)
}

// put takes bytes the client wrote and answers every whole PDU in them.
func (p *smbSrvPipe) put(data []byte) {
	if len(p.in)+len(data) > smbSrvPipeMax {
		p.in = nil
		return
	}
	p.in = append(p.in, data...)
	for len(p.in) >= 16 {
		n := int(binary.LittleEndian.Uint16(p.in[8:]))
		if n < 16 {
			p.in = nil
			return
		}
		if len(p.in) < n {
			return
		}
		pdu := p.in[:n]
		p.in = p.in[n:]
		if reply := p.handle(pdu); len(reply) > 0 && len(p.out)+len(reply) <= smbSrvPipeMax {
			p.out = append(p.out, reply...)
		}
	}
}

// take returns up to n bytes of replies and whether more are waiting (one
// reply larger than n is read in parts, as a message-mode pipe does).
func (p *smbSrvPipe) take(n int) ([]byte, bool) {
	if n <= 0 || len(p.out) == 0 {
		return nil, len(p.out) > 0
	}
	k := min(n, len(p.out))
	out := append([]byte(nil), p.out[:k]...)
	p.out = p.out[k:]
	return out, len(p.out) > 0
}

// handle answers one PDU.
func (p *smbSrvPipe) handle(pdu []byte) []byte {
	if len(pdu) < 16 || pdu[0] != 5 || pdu[4]&0xf0 != 0x10 {
		return nil
	}
	callID := binary.LittleEndian.Uint32(pdu[12:])
	switch pdu[2] {
	case smb2RPCBind:
		return p.bind(pdu, callID)
	case smb2RPCRequest:
		return p.request(pdu, callID)
	}
	return nil
}

// bind accepts srvsvc v3 with NDR (version 2), acknowledges bind-time
// feature negotiation, and rejects every other presentation context.
func (p *smbSrvPipe) bind(pdu []byte, callID uint32) []byte {
	if len(pdu) < 28 || binary.LittleEndian.Uint16(pdu[10:]) != 0 {
		return nil
	}
	count := int(pdu[24])
	type result struct {
		code   uint16
		reason uint16
		syntax []byte
	}
	var results []result
	off := 28
	for range count {
		if off+4+20 > len(pdu) {
			return nil
		}
		id := binary.LittleEndian.Uint16(pdu[off:])
		nTrans := int(pdu[off+2])
		abstract := pdu[off+4 : off+24]
		off += 24
		if off+20*nTrans > len(pdu) {
			return nil
		}
		res := result{code: 2, reason: 2, syntax: make([]byte, 20)} // provider rejection
		for i := range nTrans {
			ts := pdu[off+20*i : off+20*i+20]
			switch {
			case bytes.Equal(abstract[:16], smb2SrvsvcUUID) && bytes.Equal(ts[:16], smb2NDRUUID) &&
				binary.LittleEndian.Uint32(ts[16:]) == 2 && res.code != 0:
				res = result{syntax: append([]byte(nil), ts...)}
				p.bound, p.ctxID = true, id
			case bytes.HasPrefix(ts, smbSrvBindFeatureUUID) && res.code == 2:
				res = result{code: 3, syntax: make([]byte, 20)} // negotiate_ack, no features
			}
		}
		results = append(results, res)
		off += 20 * nTrans
	}
	const port = "\\PIPE\\srvsvc\x00"
	b := smb2RPCHeader(smb2RPCBindAck, callID, 44+24*len(results)) // length set below
	b = binary.LittleEndian.AppendUint16(b, smb2RPCMaxFrag)
	b = binary.LittleEndian.AppendUint16(b, smb2RPCMaxFrag)
	b = binary.LittleEndian.AppendUint32(b, smbSrvRPCAssocGroup)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(port)))
	b = append(b, port...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	b = append(b, byte(len(results)), 0, 0, 0)
	for _, r := range results {
		b = binary.LittleEndian.AppendUint16(b, r.code)
		b = binary.LittleEndian.AppendUint16(b, r.reason)
		b = append(b, r.syntax...)
	}
	binary.LittleEndian.PutUint16(b[8:], uint16(len(b)))
	return b
}

// smbSrvRPCFault is a FAULT PDU with status.
func smbSrvRPCFault(callID uint32, ctxID uint16, status uint32) []byte {
	b := smb2RPCHeader(smb2RPCFault, callID, 32)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint16(b, ctxID)
	b = append(b, 0, 0)
	b = binary.LittleEndian.AppendUint32(b, status)
	return binary.LittleEndian.AppendUint32(b, 0)
}

// request runs one call: NetrShareEnum at level 1; anything else faults.
func (p *smbSrvPipe) request(pdu []byte, callID uint32) []byte {
	if len(pdu) < 24 || pdu[3]&(smb2RPCFirstFrag|smb2RPCLastFrag) != smb2RPCFirstFrag|smb2RPCLastFrag ||
		binary.LittleEndian.Uint16(pdu[10:]) != 0 {
		return smbSrvRPCFault(callID, 0, smbSrvRPCOpRangeErr)
	}
	ctxID := binary.LittleEndian.Uint16(pdu[20:])
	opnum := binary.LittleEndian.Uint16(pdu[22:])
	stubAt := 24
	if pdu[3]&0x80 != 0 { // PFC_OBJECT_UUID
		stubAt = 40
	}
	if !p.bound || ctxID != p.ctxID || opnum != 15 || stubAt > len(pdu) {
		return smbSrvRPCFault(callID, ctxID, smbSrvRPCOpRangeErr)
	}
	level, resume, ok := smbSrvParseShareEnumReq(pdu[stubAt:])
	if !ok || level != 1 {
		return smbSrvRPCFault(callID, ctxID, smbSrvRPCOpRangeErr)
	}
	stub := smbSrvShareEnumStub(resume)
	b := smb2RPCHeader(smb2RPCResponse, callID, 24+len(stub))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(stub)))
	b = binary.LittleEndian.AppendUint16(b, ctxID)
	b = append(b, 0, 0)
	return append(b, stub...)
}

// smbSrvParseShareEnumReq reads NetrShareEnum's arguments: the server name
// (skipped), InfoStruct's level and container, the preferred length and
// whether a resume handle was passed.
func smbSrvParseShareEnumReq(stub []byte) (level uint32, resume, ok bool) {
	r := &smb2NDR{b: stub}
	if r.u32() != 0 {
		r.str()
	}
	level = r.u32()
	r.u32() // union switch
	if r.u32() != 0 {
		r.u32() // EntriesRead
		if r.u32() != 0 {
			return 0, false, false // a client never sends entries
		}
	}
	r.u32() // PreferedMaximumLength
	resume = r.u32() != 0
	if resume {
		r.u32()
	}
	return level, resume, r.err == nil
}

// smbSrvShareEnumStub is the NDR of a level-1 NetrShareEnum reply listing
// JukaHub and IPC$.
func smbSrvShareEnumStub(resume bool) []byte {
	shares := []struct {
		name, remark string
		typ          uint32
	}{
		{smbServeShareName, "JukaHub SD card", 0},
		{"IPC$", "IPC Service", 0x80000003},
	}
	b := make([]byte, 0, 256)
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	str := func(s string) {
		u := smb2UTF16(s + "\x00")
		n := uint32(len(u) / 2)
		u32(n)
		u32(0)
		u32(n)
		b = append(b, u...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	u32(1)          // Level
	u32(1)          // union switch
	u32(0x00020000) // SHARE_INFO_1_CONTAINER pointer
	u32(uint32(len(shares)))
	u32(0x00020004) // Buffer pointer
	u32(uint32(len(shares)))
	ref := uint32(0x00020008)
	for _, s := range shares {
		u32(ref)
		u32(s.typ)
		u32(ref + 4)
		ref += 8
	}
	for _, s := range shares {
		str(s.name)
		str(s.remark)
	}
	u32(uint32(len(shares))) // TotalEntries
	if resume {
		u32(ref)
		u32(0)
	} else {
		u32(0)
	}
	u32(0) // NERR_Success
	return b
}
