package main

// wg_awg.go — AmneziaWG: WireGuard with the obfuscation AmneziaVPN servers
// (amneziawg-go, the AmneziaWG kernel module) expect, for the VPN's own
// WireGuard (wg_noise.go, wg_device.go).
//
// An AmneziaWG configuration is a wg-quick file with more keys in its
// [Interface]. A plain WireGuard client ignores them and its handshake never
// gets an answer: the server only takes messages framed its way. What each key
// does to the wire:
//
//	H1 H2 H3 H4   the message type (the first four bytes, little-endian) of an
//	              initiation, a response, a cookie reply and a transport
//	              message instead of 1, 2, 3, 4. AmneziaWG 2.0 allows a range
//	              "min-max": each message takes a random value in it, and any
//	              value in it is accepted. The handshake's MACs cover the type
//	              as sent.
//	S1 S2 S3 S4   that many random bytes in front of an initiation, a
//	              response, a cookie reply (2.0) and every transport message
//	              (2.0). The receiver tells the handshake messages apart by
//	              their padded size, then checks the type behind the padding.
//	Jc Jmin Jmax  before every initiation, Jc datagrams of random bytes, each
//	              Jmin to Jmax long: noise the server drops, for whatever
//	              watches the start of the connection.
//	I1 … I5       (1.5) datagrams sent before those, built from tags:
//	              <b 0x…> fixed bytes, <r N> random bytes, <rc N> random
//	              letters, <rd N> random digits, <t> a 32-bit timestamp, <c> a
//	              32-bit counter — a decoy shaped like another protocol.
//	J1 J2 J3 Itime (1.5) the same kind of decoys, sent instead of the I ones
//	              between two Itime seconds apart.
//
// The server drops the decoys and the junk; only the types and the padding
// must match it, which is why a configuration without them is plain WireGuard
// and runs exactly as before (wgConfig.AWG is nil then). A decoy with a tag
// JukaHub does not know is left out (it only disguises the start) and named
// under the configuration's ignored keys. AmneziaWG 3's header protection
// (HeaderProtectionKey, ContentPaddingAddition) encrypts header fields this
// code does not know the layout of: such a file is refused with that reason
// rather than imported to never connect.

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// wgAWGMaxJunk bounds Jc (AmneziaWG's own limit).
	wgAWGMaxJunk = 128
	// wgAWGMaxJunkSize bounds Jmin and Jmax, the junk datagrams' length.
	wgAWGMaxJunkSize = 1280
	// wgAWGMaxDecoy bounds one I/J decoy datagram as its tags build it.
	wgAWGMaxDecoy = 8192
)

// wgAWGKeyNames are the AmneziaWG [Interface] keys in their usual spelling, in
// the order quickText writes them.
var wgAWGKeyNames = []string{"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
	"I1", "I2", "I3", "I4", "I5", "J1", "J2", "J3", "Itime"}

// wgAWG3Keys are AmneziaWG 3's header-protection keys, which JukaHub cannot
// carry out (see the file comment).
var wgAWG3Keys = []string{"HeaderProtectionKey", "ContentPaddingAddition"}

// errWGAWGUnknownTag marks a decoy tag JukaHub does not know.
var errWGAWGUnknownTag = errors.New("unknown tag")

// wgAWGRange is the message types one kind of message may carry.
type wgAWGRange struct {
	lo, hi uint32
}

func (r wgAWGRange) has(v uint32) bool { return v >= r.lo && v <= r.hi }

func (r wgAWGRange) String() string {
	if r.lo == r.hi {
		return strconv.FormatUint(uint64(r.lo), 10)
	}
	return strconv.FormatUint(uint64(r.lo), 10) + "-" + strconv.FormatUint(uint64(r.hi), 10)
}

// wgAWGTag is one piece of a decoy datagram.
type wgAWGTag struct {
	kind string // "b" fixed bytes, "r" random bytes, "rc" letters, "rd" digits, "t" time, "c" counter
	n    int
	data []byte
}

// wgAWG is a configuration's AmneziaWG settings.
type wgAWG struct {
	Jc, Jmin, Jmax int
	S1, S2, S3, S4 int
	// H is the type range of an initiation, a response, a cookie reply and a
	// transport message, in that order (index = WireGuard's type - 1).
	H [4]wgAWGRange
	// I and J are the decoys (nil: none); Itime is in seconds.
	I     [5][]wgAWGTag
	J     [3][]wgAWGTag
	Itime int
	// Keys holds every AmneziaWG key the file set, as written (for quickText).
	Keys map[string]string
	// Notes are decoys left out, for the configuration's ignored keys.
	Notes []string

	mu      sync.Mutex
	counter uint32
	lastI   time.Time
}

// newWGAWG is the settings before any key: plain WireGuard's types, no
// padding, no junk.
func newWGAWG() *wgAWG {
	a := &wgAWG{Keys: map[string]string{}}
	for i := range a.H {
		a.H[i] = wgAWGRange{uint32(i + 1), uint32(i + 1)}
	}
	return a
}

// wgAWGKey is the usual spelling of an AmneziaWG key, "" for any other key.
func wgAWGKey(key string) string {
	for _, k := range wgAWGKeyNames {
		if strings.EqualFold(k, key) {
			return k
		}
	}
	return ""
}

// wgAWGSet reads one AmneziaWG key into a (lower-case key as the parser has
// it). handled is false for a key that is not AmneziaWG's.
func wgAWGSet(a *wgAWG, key, val string) (handled bool, err error) {
	for _, k := range wgAWG3Keys {
		if strings.EqualFold(k, key) && strings.TrimSpace(val) != "" {
			return true, fmt.Errorf("%s is AmneziaWG 3's header protection, which JukaHub cannot connect with yet: ask the server for an AmneziaWG 2.0 configuration (without header protection)", k)
		}
	}
	name := wgAWGKey(key)
	if name == "" {
		return false, nil
	}
	a.Keys[name] = val
	num := func(lo, hi int) (int, error) {
		n, err := strconv.Atoi(val)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%s %q is not a number from %d to %d", name, val, lo, hi)
		}
		return n, nil
	}
	switch name {
	case "Jc":
		a.Jc, err = num(0, wgAWGMaxJunk)
	case "Jmin":
		a.Jmin, err = num(0, wgAWGMaxJunkSize)
	case "Jmax":
		a.Jmax, err = num(0, wgAWGMaxJunkSize)
	case "S1":
		a.S1, err = num(0, wgAWGMaxJunkSize-wgInitiationSize)
	case "S2":
		a.S2, err = num(0, wgAWGMaxJunkSize-wgResponseSize)
	case "S3":
		a.S3, err = num(0, wgAWGMaxJunkSize-wgCookieSize)
	case "S4":
		a.S4, err = num(0, wgAWGMaxJunkSize-wgKeepaliveSize)
	case "Itime":
		a.Itime, err = num(0, 86400)
	case "H1", "H2", "H3", "H4":
		var r wgAWGRange
		r, err = wgAWGParseRange(val)
		if err != nil {
			return true, fmt.Errorf("%s %v", name, err)
		}
		a.H[name[1]-'1'] = r
	case "I1", "I2", "I3", "I4", "I5":
		var tags []wgAWGTag
		if tags, err = wgAWGDecoy(a, name, val); err != nil {
			return true, err
		}
		a.I[name[1]-'1'] = tags
	case "J1", "J2", "J3":
		var tags []wgAWGTag
		if tags, err = wgAWGDecoy(a, name, val); err != nil {
			return true, err
		}
		a.J[name[1]-'1'] = tags
	}
	return true, err
}

// wgAWGDecoy reads one I/J decoy. One with a tag JukaHub does not know is left
// out with a note: decoys only disguise the start, the server ignores them.
func wgAWGDecoy(a *wgAWG, name, val string) ([]wgAWGTag, error) {
	tags, err := wgAWGParseTags(val)
	if errors.Is(err, errWGAWGUnknownTag) {
		a.Notes = append(a.Notes, name+" ("+err.Error()+")")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return tags, nil
}

// wgAWGParseRange reads a message type: a number, or "min-max" (2.0).
func wgAWGParseRange(val string) (wgAWGRange, error) {
	lo, hi, isRange := strings.Cut(strings.TrimSpace(val), "-")
	parse := func(s string) (uint32, error) {
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
		if err != nil || n == 0 {
			return 0, fmt.Errorf("%q is not a message type (a number from 1 to 4294967295, or a range of them)", val)
		}
		return uint32(n), nil
	}
	a, err := parse(lo)
	if err != nil {
		return wgAWGRange{}, err
	}
	b := a
	if isRange {
		if b, err = parse(hi); err != nil {
			return wgAWGRange{}, err
		}
		if b < a {
			return wgAWGRange{}, fmt.Errorf("%q runs backwards", val)
		}
	}
	return wgAWGRange{a, b}, nil
}

// wgAWGParseTags reads a decoy's tags. An empty value is no decoy.
func wgAWGParseTags(val string) ([]wgAWGTag, error) {
	s := strings.TrimSpace(val)
	var tags []wgAWGTag
	size := 0
	for s != "" {
		if s[0] != '<' {
			return nil, fmt.Errorf("expected a tag such as <b 0x…> or <r 16> at %q", wgAWGShort(s))
		}
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return nil, fmt.Errorf("the tag %q has no closing >", wgAWGShort(s))
		}
		fields := strings.Fields(s[1:end])
		s = strings.TrimSpace(s[end+1:])
		if len(fields) == 0 {
			return nil, errors.New("an empty tag <>")
		}
		t := wgAWGTag{kind: strings.ToLower(fields[0])}
		switch t.kind {
		case "b":
			if len(fields) != 2 {
				return nil, errors.New("<b> needs its bytes, as in <b 0x0a0b>")
			}
			h := strings.TrimPrefix(strings.TrimPrefix(fields[1], "0x"), "0X")
			raw, err := hex.DecodeString(h)
			if err != nil || len(raw) == 0 {
				return nil, fmt.Errorf("<b %s> is not hexadecimal bytes", wgAWGShort(fields[1]))
			}
			t.data, t.n = raw, len(raw)
		case "r", "rc", "rd":
			if len(fields) != 2 {
				return nil, fmt.Errorf("<%s> needs a length, as in <%s 16>", t.kind, t.kind)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1 || n > wgAWGMaxDecoy {
				return nil, fmt.Errorf("<%s %s> needs a length from 1 to %d", t.kind, wgAWGShort(fields[1]), wgAWGMaxDecoy)
			}
			t.n = n
		case "t", "c":
			if len(fields) != 1 {
				return nil, fmt.Errorf("<%s> takes nothing after it", t.kind)
			}
			t.n = 4
		default:
			return nil, fmt.Errorf("<%s>: %w", wgAWGShort(fields[0]), errWGAWGUnknownTag)
		}
		size += t.n
		if size > wgAWGMaxDecoy {
			return nil, fmt.Errorf("the decoy is longer than %d bytes", wgAWGMaxDecoy)
		}
		tags = append(tags, t)
	}
	return tags, nil
}

// wgAWGShort cuts a value for an error message.
func wgAWGShort(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// wgAWGFinish checks the settings as a whole and returns them, or nil when
// they leave WireGuard as it is (no key, or only the defaults).
func wgAWGFinish(a *wgAWG) (*wgAWG, error) {
	if a == nil || len(a.Keys) == 0 {
		return nil, nil
	}
	if a.Jmin > a.Jmax {
		return nil, fmt.Errorf("Jmin %d is more than Jmax %d", a.Jmin, a.Jmax)
	}
	// The receiver tells the handshake messages apart by their padded sizes
	// (AmneziaWG refuses S1 + 56 = S2 for that reason).
	sizes := map[int]string{}
	for _, m := range []struct {
		name string
		size int
	}{{"initiation (148 + S1)", wgInitiationSize + a.S1}, {"response (92 + S2)", wgResponseSize + a.S2}, {"cookie reply (64 + S3)", wgCookieSize + a.S3}} {
		if other, ok := sizes[m.size]; ok {
			return nil, fmt.Errorf("the padded %s is as long as the %s; change S1, S2 or S3", m.name, other)
		}
		sizes[m.size] = m.name
	}
	for i := range a.H {
		for j := i + 1; j < len(a.H); j++ {
			if a.H[i].lo <= a.H[j].hi && a.H[j].lo <= a.H[i].hi {
				return nil, fmt.Errorf("H%d (%s) and H%d (%s) overlap; every message kind needs its own types", i+1, a.H[i], j+1, a.H[j])
			}
		}
	}
	if !a.obfuscates() {
		return nil, nil
	}
	return a, nil
}

// obfuscates reports settings that change anything on the wire.
func (a *wgAWG) obfuscates() bool {
	if a == nil {
		return false
	}
	if a.Jc > 0 || a.S1 > 0 || a.S2 > 0 || a.S3 > 0 || a.S4 > 0 {
		return true
	}
	for i, r := range a.H {
		if r.lo != uint32(i+1) || r.hi != uint32(i+1) {
			return true
		}
	}
	for _, t := range a.I {
		if len(t) > 0 {
			return true
		}
	}
	for _, t := range a.J {
		if len(t) > 0 {
			return true
		}
	}
	return false
}

// header is the type field for a message of WireGuard type kind (1-4): plain
// kind without AmneziaWG, else a value in its range.
func (a *wgAWG) header(kind uint32) uint32 {
	if a == nil || kind < 1 || kind > 4 {
		return kind
	}
	r := a.H[kind-1]
	if r.hi <= r.lo {
		return r.lo
	}
	return r.lo + uint32(rand.N(uint64(r.hi-r.lo)+1))
}

// is reports a type field that belongs to messages of WireGuard type kind.
func (a *wgAWG) is(kind, v uint32) bool {
	if a == nil || kind < 1 || kind > 4 {
		return v == kind
	}
	return a.H[kind-1].has(v)
}

// pad is the random padding in front of a message of WireGuard type kind.
func (a *wgAWG) pad(kind uint32) int {
	if a == nil {
		return 0
	}
	switch kind {
	case wgMsgInitiation:
		return a.S1
	case wgMsgResponse:
		return a.S2
	case wgMsgCookie:
		return a.S3
	case wgMsgData:
		return a.S4
	}
	return 0
}

// wgAWGFill fills b with random bytes (filler on the wire: math/rand/v2's
// ChaCha8 is plenty, and nothing secret is drawn from it).
func wgAWGFill(b []byte) {
	for len(b) >= 8 {
		binary.LittleEndian.PutUint64(b, rand.Uint64())
		b = b[8:]
	}
	if len(b) > 0 {
		var last [8]byte
		binary.LittleEndian.PutUint64(last[:], rand.Uint64())
		copy(b, last[:])
	}
}

// frame puts a handshake message of WireGuard type kind behind its padding.
func (a *wgAWG) frame(kind uint32, msg []byte) []byte {
	n := a.pad(kind)
	if n == 0 || len(msg) == 0 {
		return msg
	}
	out := make([]byte, n+len(msg))
	wgAWGFill(out[:n])
	copy(out[n:], msg)
	return out
}

// decoy builds one I/J datagram from its tags.
func (a *wgAWG) decoy(tags []wgAWGTag, counter uint32, now time.Time) []byte {
	var out []byte
	for _, t := range tags {
		switch t.kind {
		case "b":
			out = append(out, t.data...)
		case "r":
			start := len(out)
			out = append(out, make([]byte, t.n)...)
			wgAWGFill(out[start:])
		case "rc":
			const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
			for i := 0; i < t.n; i++ {
				out = append(out, letters[rand.N(len(letters))])
			}
		case "rd":
			for i := 0; i < t.n; i++ {
				out = append(out, byte('0'+rand.N(10)))
			}
		case "t":
			out = binary.BigEndian.AppendUint32(out, uint32(now.Unix()))
		case "c":
			out = binary.BigEndian.AppendUint32(out, counter)
		}
	}
	return out
}

// beforeInitiation is what goes out ahead of every handshake initiation: the
// I decoys (the J ones instead while Itime has not passed since the I ones
// went), then Jc junk datagrams of Jmin to Jmax random bytes.
func (a *wgAWG) beforeInitiation(now time.Time) [][]byte {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	decoys := a.I[:]
	if a.Itime > 0 && !a.lastI.IsZero() && now.Sub(a.lastI) < time.Duration(a.Itime)*time.Second && now.After(a.lastI) {
		decoys = a.J[:]
	} else {
		a.lastI = now
	}
	var out [][]byte
	for _, tags := range decoys {
		if len(tags) == 0 {
			continue
		}
		a.counter++
		if d := a.decoy(tags, a.counter, now); len(d) > 0 {
			out = append(out, d)
		}
	}
	a.mu.Unlock()
	for i := 0; i < a.Jc; i++ {
		n := a.Jmin
		if a.Jmax > a.Jmin {
			n += rand.N(a.Jmax - a.Jmin + 1)
		}
		if n <= 0 {
			continue
		}
		junk := make([]byte, n)
		wgAWGFill(junk)
		out = append(out, junk)
	}
	return out
}

// quickLines are the AmneziaWG keys for a wg-quick file, as the file had them.
func (a *wgAWG) quickLines() string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	for _, k := range wgAWGKeyNames {
		if v, ok := a.Keys[k]; ok {
			b.WriteString(k + " = " + v + "\n")
		}
	}
	return b.String()
}
