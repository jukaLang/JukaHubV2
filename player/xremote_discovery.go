package main

// xremote_discovery.go — is an Xbox on this network? The X1 pill's dot.
//
// Xbox One and Series consoles answer SmartGlass discovery: a 16-byte 0xDD00
// request sent to UDP 5050 gets a 0xDD01 answer carrying the console's name
// and id (OpenXbox's SmartGlass documentation; the same probe Greenlight's
// author publishes as XboxDiscovery.py). Discovery is one of the protocol's
// two plain-text messages, so it needs no crypto. A console that is off, or
// asleep, does not answer, so the dot is lit only while an Xbox is on and
// reachable from here: what a green light says on the PS pills too. Whether
// it can stream is still the client's to report (xremote.go).

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	// xboxDiscoveryPort is SmartGlass's UDP port.
	xboxDiscoveryPort = 5050
	// xboxDiscoveryMulticast is the group SmartGlass clients also send to.
	xboxDiscoveryMulticast = "239.255.255.250"
	// xboxScanEvery is how often Home asks while the X1 pill is drawn.
	xboxScanEvery = 10 * time.Second
	// xboxScanWait is how long one scan listens for answers.
	xboxScanWait = 1500 * time.Millisecond
	// xboxSeenFor is how long one answer keeps the dot lit: two scans in a
	// row may miss (UDP is lossy) before it goes out.
	xboxSeenFor = 25 * time.Second
	// xboxNameLimit bounds a console name read off the network.
	xboxNameLimit = 48
)

// xboxConsole is one console that answered a scan.
type xboxConsole struct {
	Host string
	Name string
	ID   string
}

// xboxDiscoveryPacket is the 0xDD00 request: the header (type, unprotected
// payload length 10, version 0), then flags 0, client type 8 (Android, the
// SmartGlass libraries' default) and protocol versions 0 to 2.
func xboxDiscoveryPacket() []byte {
	return []byte{
		0xdd, 0x00, 0x00, 0x0a, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x08,
		0x00, 0x00, 0x00, 0x02,
	}
}

// xboxParseDiscoveryResponse reads a 0xDD01 answer: the 6-byte header, flags
// (uint32) and device type (uint16), then the name and the id, each a
// big-endian uint16 length, its UTF-8 bytes and a NUL. Anything else, or an
// answer cut short before the name ends, is not a console.
func xboxParseDiscoveryResponse(payload []byte, from net.Addr) (xboxConsole, bool) {
	if len(payload) < 14 || payload[0] != 0xdd || payload[1] != 0x01 {
		return xboxConsole{}, false
	}
	field := func(off int) (string, int, bool) {
		if off < 0 || off+2 > len(payload) {
			return "", off, false
		}
		n := int(binary.BigEndian.Uint16(payload[off:]))
		off += 2
		if off+n > len(payload) {
			return "", off, false
		}
		return strings.TrimRight(string(payload[off:off+n]), "\x00"), off + n + 1, true
	}
	name, off, ok := field(12)
	if !ok {
		return xboxConsole{}, false
	}
	id, _, _ := field(off)
	host := ""
	if from != nil {
		if h, _, err := net.SplitHostPort(from.String()); err == nil {
			host = h
		} else {
			host = from.String()
		}
	}
	if host == "" {
		return xboxConsole{}, false
	}
	return xboxConsole{
		Host: host,
		Name: partyCleanLabel(name, xboxNameLimit),
		ID:   partyCleanLabel(id, xboxNameLimit),
	}, true
}

// xboxScan sends one discovery request to every broadcast address this
// machine has and the SmartGlass group, and collects answers until wait runs
// out, one per address.
func xboxScan(wait time.Duration) ([]xboxConsole, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	packet := xboxDiscoveryPacket()
	targets := append(psDiscoveryAddresses(xboxDiscoveryPort),
		net.JoinHostPort(xboxDiscoveryMulticast, strconv.Itoa(xboxDiscoveryPort)))
	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(packet, addr)
	}

	deadline := time.Now().Add(wait)
	seen := map[string]bool{}
	var out []xboxConsole
	// The answer carries the console's certificate; a buffer that cuts it
	// short is an error on Windows, not a short read.
	buf := make([]byte, 8192)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, net.ErrClosed) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		c, ok := xboxParseDiscoveryResponse(buf[:n], from)
		if !ok || seen[c.Host] {
			continue
		}
		seen[c.Host] = true
		out = append(out, c)
	}
	return out, nil
}

// x1Avail is what the X1 pill's dot draws: when an Xbox last answered. Main
// loop only: the scan runs on a goroutine and posts back.
var x1Avail struct {
	busy   bool
	at     time.Time // the last scan finished
	seenAt time.Time // an Xbox answered
}

// x1ConsoleSeen reports whether an Xbox answered on this network lately, and
// starts a scan when the last one is xboxScanEvery old. The pill asks from
// the render path, so the scan never holds a frame.
func x1ConsoleSeen() bool {
	if !x1Avail.busy && !x1Within(x1Avail.at, xboxScanEvery) {
		x1Avail.busy = true
		go func() {
			consoles, err := xboxScan(xboxScanWait)
			tbxPost(func() {
				x1Avail.busy = false
				x1Avail.at = time.Now()
				was := x1Within(x1Avail.seenAt, xboxSeenFor)
				if err != nil {
					logDebug("xremote", "Xbox scan: %v", err)
					return
				}
				if len(consoles) == 0 {
					return
				}
				if !was {
					logInfo("xremote", "Xbox %q answers at %s", consoles[0].Name, consoles[0].Host)
				}
				x1Avail.seenAt = time.Now()
			})
		}()
	}
	return x1Within(x1Avail.seenAt, xboxSeenFor)
}

// x1Within reports whether t is at most d ago on both clocks. The monotonic
// one ignores NTP's jumps (the handheld boots before its clock is set) but
// stops while the handheld sleeps; the wall clock runs on through a sleep, so
// an Xbox seen before it does not stay lit after waking. A wall-clock jump can
// only make this false early, and the next scan puts that right.
func x1Within(t time.Time, d time.Duration) bool {
	if t.IsZero() {
		return false
	}
	return time.Since(t) <= d && time.Now().Round(0).Sub(t.Round(0)) <= d
}
