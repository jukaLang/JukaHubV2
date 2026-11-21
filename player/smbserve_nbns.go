package main

// smbserve_nbns.go — the NetBIOS name service (RFC 1002, UDP 137) for the
// SMB share, so \\NAME\JukaHub works on Windows without knowing the IP.
// It answers name queries for the device's NetBIOS name (suffix 0x20 file
// server or 0x00 workstation) from addresses smbServePeerAllowed accepts,
// with the current LAN address. It never registers or defends the name and
// answers nothing else (no node status, no LLMNR or mDNS).

import (
	"encoding/binary"
	"net"
	"strings"
)

// smbServeNBNS answers queries on pc until it is closed.
func smbServeNBNS(pc net.PacketConn, name string) {
	defer func() {
		if r := recover(); r != nil {
			logError("smb", "NetBIOS name service stopped: %v", r)
		}
	}()
	buf := make([]byte, 576)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ua, ok := addr.(*net.UDPAddr)
		if !ok || !smbServePeerAllowed(ua.IP) {
			continue
		}
		resp := smbSrvNBNSAnswer(buf[:n], name, func() net.IP { return net.ParseIP(localIPv4()) })
		if resp != nil {
			_, _ = pc.WriteTo(resp, addr)
		}
	}
}

// smbSrvNBDecode undoes the first-level encoding of a 32-byte NetBIOS name:
// the name (space padded to 15) and its suffix byte.
func smbSrvNBDecode(enc []byte) (string, byte, bool) {
	if len(enc) != 32 {
		return "", 0, false
	}
	var raw [16]byte
	for i := range raw {
		hi, lo := enc[2*i]-'A', enc[2*i+1]-'A'
		if hi > 15 || lo > 15 {
			return "", 0, false
		}
		raw[i] = hi<<4 | lo
	}
	return strings.TrimRight(string(raw[:15]), " "), raw[15], true
}

// smbSrvNBNSAnswer is the positive answer to a name query for name, or nil
// when the packet is not one (a response, another opcode or name, a scoped
// name, a node-status query). ip is asked only once the name matched.
func smbSrvNBNSAnswer(q []byte, name string, ip func() net.IP) []byte {
	if len(q) < 50 {
		return nil
	}
	flags := binary.BigEndian.Uint16(q[2:])
	if flags&0x8000 != 0 || flags>>11&0xf != 0 || binary.BigEndian.Uint16(q[4:]) != 1 {
		return nil
	}
	if q[12] != 0x20 || q[45] != 0 || binary.BigEndian.Uint16(q[46:]) != 0x20 || binary.BigEndian.Uint16(q[48:]) != 1 {
		return nil
	}
	nm, suffix, ok := smbSrvNBDecode(q[13:45])
	if !ok || (suffix != 0x20 && suffix != 0x00) || !strings.EqualFold(nm, name) {
		return nil
	}
	ip4 := ip().To4()
	if ip4 == nil {
		return nil
	}
	b := make([]byte, 0, 62)
	b = append(b, q[0], q[1])
	b = binary.BigEndian.AppendUint16(b, 0x8400|flags&0x0100) // response, authoritative, RD echoed
	b = binary.BigEndian.AppendUint16(b, 0)                   // questions
	b = binary.BigEndian.AppendUint16(b, 1)                   // answers
	b = binary.BigEndian.AppendUint16(b, 0)
	b = binary.BigEndian.AppendUint16(b, 0)
	b = append(b, q[12:46]...) // the name as asked
	b = binary.BigEndian.AppendUint16(b, 0x20)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint32(b, 300)
	b = binary.BigEndian.AppendUint16(b, 6)
	b = binary.BigEndian.AppendUint16(b, 0) // B-node, unique name
	return append(b, ip4...)
}
