package main

// usbnet_dhcp.go — the DHCP server on the USB cable's network (usbnet.go).
// The computer at the other end of the cable asks for an address and gets
// one of five. The answer names no router and no DNS server, so the computer
// keeps reaching the internet the way it did before the cable went in; only
// the handheld's page is reached over the cable.
//
// Everything here is pure: the socket lives in usbnet_linux.go. A packet is
// outside data, so each length is checked before it is read.

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// DHCP message types (RFC 2132, option 53).
const (
	usbNetDHCPDiscover = 1
	usbNetDHCPOffer    = 2
	usbNetDHCPRequest  = 3
	usbNetDHCPDecline  = 4
	usbNetDHCPAck      = 5
	usbNetDHCPNak      = 6
	usbNetDHCPRelease  = 7
	usbNetDHCPInform   = 8
)

const (
	// usbNetDHCPMinLen is the fixed BOOTP part plus the magic cookie.
	usbNetDHCPMinLen = 240
	// usbNetDHCPMaxLen is one Ethernet frame's worth; nothing longer is read.
	usbNetDHCPMaxLen = 1500
	usbNetDHCPCookie = 0x63825363
	// usbNetLeaseSecs is short: the cable comes and goes, and a computer that
	// keeps it plugged in renews every five minutes.
	usbNetLeaseSecs = 600
	// usbNetOfferHold keeps an offered address for the computer it was
	// offered to until it asks for it.
	usbNetOfferHold = time.Minute
)

// The cable's network is 10.73.73.0/29: the handheld is .1 and the computer
// gets one of .2 to .6. A /29 that deep in 10.x is unlikely to be any home or
// office network, so the computer's own routes stay as they were.
var usbNetDeviceAddr = [4]byte{10, 73, 73, 1}

const (
	usbNetPrefixLen = 29
	usbNetPoolFirst = 2
	usbNetPoolLast  = 6
)

// usbNetDeviceIP is the handheld's address on the cable, as text.
func usbNetDeviceIP() string { return net.IP(usbNetDeviceAddr[:]).String() }

// usbNetMaskAddr is the /29 netmask.
func usbNetMaskAddr() [4]byte {
	var m [4]byte
	copy(m[:], net.CIDRMask(usbNetPrefixLen, 32))
	return m
}

// usbNetInPool reports whether a is one of the addresses a computer may hold.
func usbNetInPool(a [4]byte) bool {
	return a[0] == usbNetDeviceAddr[0] && a[1] == usbNetDeviceAddr[1] && a[2] == usbNetDeviceAddr[2] &&
		a[3] >= usbNetPoolFirst && a[3] <= usbNetPoolLast
}

// usbNetDHCPMsg is the part of a computer's request the server reads.
type usbNetDHCPMsg struct {
	xid       [4]byte
	flags     [2]byte
	ciaddr    [4]byte
	chaddr    [16]byte
	mac       string
	msgType   byte
	requested [4]byte
	hasReq    bool
	serverID  [4]byte
	hasServer bool
}

// usbNetDHCPOptions walks a DHCP options area and calls fn for each option.
// Pad is skipped and End stops the walk; false means an option ran past the
// end of the packet.
func usbNetDHCPOptions(b []byte, fn func(code byte, val []byte)) bool {
	for i := 0; i < len(b); {
		switch b[i] {
		case 0:
			i++
			continue
		case 255:
			return true
		}
		if i+1 >= len(b) {
			return false
		}
		n := int(b[i+1])
		if i+2+n > len(b) {
			return false
		}
		fn(b[i], b[i+2:i+2+n])
		i += 2 + n
	}
	return true
}

// usbNetDHCPParse reads one request. ok is false for anything but a
// well-formed, unrelayed BOOTREQUEST from an Ethernet address with one valid
// message type; a malformed option drops the whole packet.
func usbNetDHCPParse(b []byte) (m usbNetDHCPMsg, ok bool) {
	if len(b) < usbNetDHCPMinLen || len(b) > usbNetDHCPMaxLen {
		return m, false
	}
	// op 1 (request), htype 1 (Ethernet), hlen 6, hops 0: nothing relays on
	// a cable, so a relay address is refused too.
	if b[0] != 1 || b[1] != 1 || b[2] != 6 || b[3] != 0 {
		return m, false
	}
	if binary.BigEndian.Uint32(b[24:28]) != 0 || binary.BigEndian.Uint32(b[236:240]) != usbNetDHCPCookie {
		return m, false
	}
	copy(m.xid[:], b[4:8])
	copy(m.flags[:], b[10:12])
	copy(m.ciaddr[:], b[12:16])
	copy(m.chaddr[:], b[28:44])
	mac := net.HardwareAddr(b[28:34])
	if binary.BigEndian.Uint32(b[28:32]) == 0 && binary.BigEndian.Uint16(b[32:34]) == 0 {
		return usbNetDHCPMsg{}, false
	}
	m.mac = mac.String()
	bad := false
	walked := usbNetDHCPOptions(b[usbNetDHCPMinLen:], func(code byte, val []byte) {
		switch code {
		case 53:
			if len(val) != 1 || val[0] < usbNetDHCPDiscover || val[0] > usbNetDHCPInform {
				bad = true
				return
			}
			m.msgType = val[0]
		case 50:
			if len(val) != 4 {
				bad = true
				return
			}
			copy(m.requested[:], val)
			m.hasReq = true
		case 54:
			if len(val) != 4 {
				bad = true
				return
			}
			copy(m.serverID[:], val)
			m.hasServer = true
		}
	})
	if !walked || bad || m.msgType == 0 {
		return usbNetDHCPMsg{}, false
	}
	return m, true
}

// usbNetDHCPReply builds an answer: msgType offer, ack or nak, yiaddr the
// address handed out (zero for a NAK or an INFORM), lease 0 for no lease
// times (INFORM). Only the server ID, the subnet mask and the lease times go
// in — never a router (3) or DNS servers (6).
func usbNetDHCPReply(req usbNetDHCPMsg, msgType byte, yiaddr [4]byte, lease uint32) []byte {
	b := make([]byte, usbNetDHCPMinLen, 300)
	b[0], b[1], b[2] = 2, 1, 6
	copy(b[4:8], req.xid[:])
	copy(b[10:12], req.flags[:])
	if req.msgType == usbNetDHCPInform {
		copy(b[12:16], req.ciaddr[:])
	}
	if msgType != usbNetDHCPNak {
		copy(b[16:20], yiaddr[:])
	}
	copy(b[28:44], req.chaddr[:])
	binary.BigEndian.PutUint32(b[236:240], usbNetDHCPCookie)
	opt := func(code byte, val ...byte) {
		b = append(b, code, byte(len(val)))
		b = append(b, val...)
	}
	opt(53, msgType)
	opt(54, usbNetDeviceAddr[:]...)
	if msgType != usbNetDHCPNak {
		mask := usbNetMaskAddr()
		opt(1, mask[:]...)
		if lease > 0 {
			opt(51, binary.BigEndian.AppendUint32(nil, lease)...)
			opt(58, binary.BigEndian.AppendUint32(nil, lease/2)...)
			opt(59, binary.BigEndian.AppendUint32(nil, lease/8*7)...)
		}
	}
	b = append(b, 255)
	for len(b) < 300 {
		b = append(b, 0)
	}
	return b
}

// usbNetLease is an address held by one hardware address until a time.
type usbNetLease struct {
	addr  [4]byte
	until time.Time
}

// usbNetDHCPPool hands out the five addresses, one per hardware address. An
// address a computer declined (something else uses it) is parked under
// "!<address>" for a lease time. The map never holds more than ten entries:
// a new hardware address is only remembered when an address is free for it.
type usbNetDHCPPool struct {
	mu     sync.Mutex
	leases map[string]usbNetLease
}

func newUSBNetDHCPPool() *usbNetDHCPPool {
	return &usbNetDHCPPool{leases: map[string]usbNetLease{}}
}

// handle answers one request at time now. reply is nil when there is
// nothing to say; to is where a reply goes when it must not be broadcast
// (an INFORM, from a computer that has its address), else zero.
func (p *usbNetDHCPPool) handle(req usbNetDHCPMsg, now time.Time) (reply []byte, to [4]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, l := range p.leases {
		if !now.Before(l.until) {
			delete(p.leases, k)
		}
	}
	leaseFor := now.Add(usbNetLeaseSecs * time.Second)
	switch req.msgType {
	case usbNetDHCPDiscover:
		a, ok := p.pickLocked(req.mac, req.requested, req.hasReq)
		if !ok {
			return nil, to
		}
		held := p.leases[req.mac]
		until := now.Add(usbNetOfferHold)
		if held.addr == a && held.until.After(until) {
			until = held.until
		}
		p.leases[req.mac] = usbNetLease{addr: a, until: until}
		return usbNetDHCPReply(req, usbNetDHCPOffer, a, usbNetLeaseSecs), to
	case usbNetDHCPRequest:
		if req.hasServer && req.serverID != usbNetDeviceAddr {
			// The computer took another server's offer.
			delete(p.leases, req.mac)
			return nil, to
		}
		want, have := req.requested, req.hasReq
		if !have && req.ciaddr != ([4]byte{}) {
			want, have = req.ciaddr, true
		}
		if !have || !usbNetInPool(want) || p.heldByOtherLocked(want, req.mac) {
			return usbNetDHCPReply(req, usbNetDHCPNak, [4]byte{}, 0), to
		}
		p.leases[req.mac] = usbNetLease{addr: want, until: leaseFor}
		return usbNetDHCPReply(req, usbNetDHCPAck, want, usbNetLeaseSecs), to
	case usbNetDHCPRelease:
		if l, ok := p.leases[req.mac]; ok && l.addr == req.ciaddr {
			delete(p.leases, req.mac)
		}
	case usbNetDHCPDecline:
		if req.hasReq && usbNetInPool(req.requested) {
			if l, ok := p.leases[req.mac]; ok && l.addr == req.requested {
				delete(p.leases, req.mac)
			}
			p.leases["!"+net.IP(req.requested[:]).String()] = usbNetLease{addr: req.requested, until: leaseFor}
		}
	case usbNetDHCPInform:
		// A computer with an address of its own on this network asks for
		// the rest: the mask, no lease.
		if usbNetInPool(req.ciaddr) {
			return usbNetDHCPReply(req, usbNetDHCPAck, [4]byte{}, 0), req.ciaddr
		}
	}
	return nil, to
}

// pickLocked is the address for mac: the one it holds, the one it asks for
// when that is free, else the first free one.
func (p *usbNetDHCPPool) pickLocked(mac string, want [4]byte, hasWant bool) ([4]byte, bool) {
	if l, ok := p.leases[mac]; ok {
		return l.addr, true
	}
	if hasWant && usbNetInPool(want) && !p.heldByOtherLocked(want, mac) {
		return want, true
	}
	for n := usbNetPoolFirst; n <= usbNetPoolLast; n++ {
		a := usbNetDeviceAddr
		a[3] = byte(n)
		if !p.heldByOtherLocked(a, mac) {
			return a, true
		}
	}
	return [4]byte{}, false
}

// heldByOtherLocked reports whether anyone but mac holds a.
func (p *usbNetDHCPPool) heldByOtherLocked(a [4]byte, mac string) bool {
	for k, l := range p.leases {
		if k != mac && l.addr == a {
			return true
		}
	}
	return false
}
