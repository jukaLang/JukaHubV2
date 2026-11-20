package main

// dbusmini.go — a small D-Bus client, enough for JukaHub to talk to BlueZ
// without a library (Controller Mode's Bluetooth gamepad, ctlmode_bt.go).
//
// What it does, and no more:
//
//   - connects to the system bus (DBUS_SYSTEM_BUS_ADDRESS's unix:path= or
//     unix:abstract=, else the usual socket paths), authenticates with SASL
//     EXTERNAL and says Hello;
//   - writes and reads the wire format: little-endian on the way out, either
//     byte order on the way in, the types y b n q i u x t d s o g, arrays,
//     structs, dict entries and variants, each aligned the way the
//     specification says (alignment counts from the start of the message;
//     the header is padded to 8, so the body counts from its own start);
//   - method calls whose replies are matched by serial, with a timeout, and
//     error replies turned into *dbusError;
//   - exported objects: incoming calls dispatched by path, interface and
//     member to a handler that may answer at once or later (a pairing prompt
//     answers when the user presses a button); Properties.Get/GetAll from
//     each interface's property function; ObjectManager.GetManagedObjects on
//     the roots asked for; signals out (PropertiesChanged) and AddMatch for
//     the signals wanted in.
//
// One goroutine reads; writes take a mutex. Handlers and the signal function
// run on the reading goroutine, so they must not block and must never wait
// for a reply themselves (the reply would queue behind them).
//
// It is portable Go: Go's net package dials unix sockets on every system it
// builds for (an abstract "@name" only means something on Linux), and only
// the Linux build ever starts a connection.
//
// Decoded values have fixed Go types: y byte, b bool, n int16, q uint16,
// i int32, u uint32, x int64, t uint64, d float64, s string, o
// dbusObjectPath, g dbusSignature, v dbusVariant, ay []byte, other arrays
// []any, structs []any, and dicts map[string]any (keys that are not strings
// are printed). The encoder takes those, plus any integer type for the
// integer codes, string for o and g, any slice or array for arrays and
// structs, and any map for dicts (written in sorted key order).

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Message types and flags.
const (
	dbusTypeCall   byte = 1
	dbusTypeReturn byte = 2
	dbusTypeError  byte = 3
	dbusTypeSignal byte = 4

	dbusFlagNoReply byte = 0x1
	// dbusFlagNoAutoStart keeps the bus from starting a service for a call
	// (a bluetoothd that stopped stays stopped: only btBringUp starts it).
	dbusFlagNoAutoStart byte = 0x2
)

// Header field codes.
const (
	dbusFieldPath        byte = 1
	dbusFieldInterface   byte = 2
	dbusFieldMember      byte = 3
	dbusFieldErrorName   byte = 4
	dbusFieldReplySerial byte = 5
	dbusFieldDestination byte = 6
	dbusFieldSender      byte = 7
	dbusFieldSignature   byte = 8
)

const (
	// dbusMaxMessage bounds one message (the specification allows 128 MiB;
	// nothing BlueZ sends comes near this).
	dbusMaxMessage = 32 << 20
	// dbusMaxArray bounds one array's bytes.
	dbusMaxArray = 16 << 20
	// dbusMaxDepth bounds nesting in a signature or a value.
	dbusMaxDepth = 64
	// dbusCallTimeout is a call's default wait for its reply.
	dbusCallTimeout = 10 * time.Second
	// dbusWriteTimeout bounds one write to the bus.
	dbusWriteTimeout = 5 * time.Second
	// dbusDialTimeout bounds connecting and authenticating.
	dbusDialTimeout = 5 * time.Second
)

// The bus itself and the standard interfaces.
const (
	dbusBusName     = "org.freedesktop.DBus"
	dbusBusPath     = "/org/freedesktop/DBus"
	dbusPropsIface  = "org.freedesktop.DBus.Properties"
	dbusObjMgrIface = "org.freedesktop.DBus.ObjectManager"
	dbusPeerIface   = "org.freedesktop.DBus.Peer"
)

// dbusSystemSockets are the system bus's usual sockets, tried after the
// environment's address.
var dbusSystemSockets = []string{"/run/dbus/system_bus_socket", "/var/run/dbus/system_bus_socket"}

var (
	errDbusSig    = errors.New("dbus: bad signature")
	errDbusShort  = errors.New("dbus: message cut short")
	errDbusNul    = errors.New("dbus: bad string")
	errDbusTooBig = errors.New("dbus: message too large")
	errDbusHeader = errors.New("dbus: bad message header")
	errDbusClosed = errors.New("dbus: connection closed")
	errDbusNoBus  = errors.New("the system message bus is not running")
)

// dbusObjectPath is a value of type o.
type dbusObjectPath string

// dbusSignature is a value of type g.
type dbusSignature string

// dbusVariant is a value of type v: its own signature and the value.
type dbusVariant struct {
	Sig   dbusSignature
	Value any
}

// dbusError is an error reply: the D-Bus error name and the sentence that
// came with it (BlueZ's own words, shown on the page).
type dbusError struct {
	Name string
	Text string
}

func (e *dbusError) Error() string {
	if e.Text != "" {
		return e.Text
	}
	return e.Name
}

// dbusMsg is one message. Sender is filled in by the bus on the way in.
type dbusMsg struct {
	Type        byte
	Flags       byte
	Serial      uint32
	Path        dbusObjectPath
	Iface       string
	Member      string
	ErrName     string
	ReplySerial uint32
	Dest        string
	Sender      string
	Sig         dbusSignature
	Body        []any
}

// arg is the body's i-th value, or nil.
func (m *dbusMsg) arg(i int) any {
	if i >= 0 && i < len(m.Body) {
		return m.Body[i]
	}
	return nil
}

// argString is the body's i-th value as a string ("" when it is not one).
func (m *dbusMsg) argString(i int) string {
	s, _ := dbusStringOf(m.arg(i))
	return s
}

// ── Signatures ───────────────────────────────────────────────────────────

// dbusAlignOf is the alignment of the type a signature code starts.
func dbusAlignOf(c byte) int {
	switch c {
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 's', 'o', 'a', 'h':
		return 4
	case 'x', 't', 'd', '(', '{':
		return 8
	}
	return 1
}

// dbusBasic reports a basic type code (one a dict key may have).
func dbusBasic(c byte) bool {
	return strings.IndexByte("ybnqiuxtdsogh", c) >= 0
}

// dbusNextType splits the first complete type off sig.
func dbusNextType(sig string) (string, string, error) {
	return dbusNextTypeAt(sig, 0)
}

func dbusNextTypeAt(sig string, depth int) (string, string, error) {
	if sig == "" || depth > dbusMaxDepth {
		return "", "", errDbusSig
	}
	c := sig[0]
	switch {
	case dbusBasic(c) || c == 'v':
		return sig[:1], sig[1:], nil
	case c == 'a' && len(sig) > 1 && sig[1] == '{':
		if len(sig) < 5 || !dbusBasic(sig[2]) {
			return "", "", errDbusSig
		}
		val, rest, err := dbusNextTypeAt(sig[3:], depth+1)
		if err != nil || rest == "" || rest[0] != '}' {
			return "", "", errDbusSig
		}
		n := 3 + len(val) + 1
		return sig[:n], sig[n:], nil
	case c == 'a':
		elem, _, err := dbusNextTypeAt(sig[1:], depth+1)
		if err != nil {
			return "", "", err
		}
		n := 1 + len(elem)
		return sig[:n], sig[n:], nil
	case c == '(':
		n := 1
		rest := sig[1:]
		for rest != "" && rest[0] != ')' {
			field, r, err := dbusNextTypeAt(rest, depth+1)
			if err != nil {
				return "", "", err
			}
			n += len(field)
			rest = r
		}
		if rest == "" || n == 1 {
			return "", "", errDbusSig
		}
		return sig[:n+1], rest[1:], nil
	}
	return "", "", errDbusSig
}

// dbusSplitSig splits a signature into its complete types.
func dbusSplitSig(sig string) ([]string, error) {
	if len(sig) > 255 {
		return nil, errDbusSig
	}
	var out []string
	for sig != "" {
		t, rest, err := dbusNextType(sig)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		sig = rest
	}
	return out, nil
}

// ── Encoding ─────────────────────────────────────────────────────────────

// dbusEnc writes values little-endian. Offsets count from the start of b, so
// b must start where the message (or the 8-aligned body) starts.
type dbusEnc struct {
	b []byte
}

func (e *dbusEnc) pad(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *dbusEnc) u32(v uint32) {
	e.pad(4)
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
}

func (e *dbusEnc) str(s string) {
	e.u32(uint32(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

func (e *dbusEnc) sig(s string) {
	e.b = append(e.b, byte(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

func dbusTypeErr(sig string, v any) error {
	return fmt.Errorf("dbus: cannot write %T as %s", v, sig)
}

// dbusStringOf reads a string, an object path or a signature.
func dbusStringOf(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case dbusObjectPath:
		return string(x), true
	case dbusSignature:
		return string(x), true
	}
	return "", false
}

// dbusIntOf reads any Go integer as an int64 (a uint64 past MaxInt64 fails).
func dbusIntOf(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return 0, false
		}
		return int64(u), true
	}
	return 0, false
}

// value writes v as the single complete type sig.
func (e *dbusEnc) value(sig string, v any) error {
	if sig == "" {
		return errDbusSig
	}
	switch c := sig[0]; c {
	case 'y', 'n', 'q', 'i', 'u', 'x', 't':
		return e.integer(c, v)
	case 'b':
		b, ok := v.(bool)
		if !ok {
			return dbusTypeErr(sig, v)
		}
		var n uint32
		if b {
			n = 1
		}
		e.u32(n)
	case 'd':
		f, ok := v.(float64)
		if !ok {
			return dbusTypeErr(sig, v)
		}
		e.pad(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, math.Float64bits(f))
	case 's', 'o', 'g':
		s, ok := dbusStringOf(v)
		if !ok {
			return dbusTypeErr(sig, v)
		}
		if strings.IndexByte(s, 0) >= 0 {
			return errDbusNul
		}
		if c == 'g' {
			if len(s) > 255 {
				return errDbusSig
			}
			e.sig(s)
		} else {
			e.str(s)
		}
	case 'v':
		vv, ok := v.(dbusVariant)
		if !ok {
			return dbusTypeErr(sig, v)
		}
		t, rest, err := dbusNextType(string(vv.Sig))
		// The variant's signature goes out with a one-byte length.
		if err != nil || rest != "" || len(t) > 255 {
			return errDbusSig
		}
		e.sig(t)
		return e.value(t, vv.Value)
	case 'a':
		return e.array(sig[1:], v)
	case '(':
		if len(sig) < 3 || sig[len(sig)-1] != ')' {
			return errDbusSig
		}
		fields, err := dbusSplitSig(sig[1 : len(sig)-1])
		if err != nil {
			return err
		}
		rv := reflect.ValueOf(v)
		if (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || rv.Len() != len(fields) {
			return dbusTypeErr(sig, v)
		}
		e.pad(8)
		for i, f := range fields {
			if err := e.value(f, rv.Index(i).Interface()); err != nil {
				return err
			}
		}
	default:
		return errDbusSig
	}
	return nil
}

// integer writes one of y n q i u x t, range-checked.
func (e *dbusEnc) integer(c byte, v any) error {
	if u, ok := v.(uint64); ok && c == 't' {
		e.pad(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, u)
		return nil
	}
	n, ok := dbusIntOf(v)
	if !ok {
		return dbusTypeErr(string(c), v)
	}
	var lo, hi int64
	switch c {
	case 'y':
		lo, hi = 0, math.MaxUint8
	case 'n':
		lo, hi = math.MinInt16, math.MaxInt16
	case 'q':
		lo, hi = 0, math.MaxUint16
	case 'i':
		lo, hi = math.MinInt32, math.MaxInt32
	case 'u':
		lo, hi = 0, math.MaxUint32
	case 'x':
		lo, hi = math.MinInt64, math.MaxInt64
	default:
		lo, hi = 0, math.MaxInt64
	}
	if n < lo || n > hi {
		return fmt.Errorf("dbus: %d does not fit %c", n, c)
	}
	switch c {
	case 'y':
		e.b = append(e.b, byte(n))
	case 'n', 'q':
		e.pad(2)
		e.b = binary.LittleEndian.AppendUint16(e.b, uint16(n))
	case 'i', 'u':
		e.u32(uint32(n))
	default:
		e.pad(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, uint64(n))
	}
	return nil
}

// array writes an array of elem: its byte length, the padding to the
// element's alignment (there even when the array is empty), the elements.
func (e *dbusEnc) array(elem string, v any) error {
	if elem == "" {
		return errDbusSig
	}
	e.pad(4)
	at := len(e.b)
	e.b = append(e.b, 0, 0, 0, 0)
	e.pad(dbusAlignOf(elem[0]))
	start := len(e.b)
	if err := e.elems(elem, v); err != nil {
		return err
	}
	n := len(e.b) - start
	if n > dbusMaxArray {
		return errDbusTooBig
	}
	binary.LittleEndian.PutUint32(e.b[at:], uint32(n))
	return nil
}

func (e *dbusEnc) elems(elem string, v any) error {
	if v == nil {
		return nil
	}
	if b, ok := v.([]byte); ok && elem == "y" {
		e.b = append(e.b, b...)
		return nil
	}
	rv := reflect.ValueOf(v)
	if elem[0] == '{' {
		if rv.Kind() != reflect.Map || len(elem) < 4 {
			return dbusTypeErr("a"+elem, v)
		}
		ksig, vsig := elem[1:2], elem[2:len(elem)-1]
		keys := rv.MapKeys()
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = fmt.Sprint(k.Interface())
		}
		order := make([]int, len(keys))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return names[order[a]] < names[order[b]] })
		for _, i := range order {
			e.pad(8)
			if err := e.value(ksig, keys[i].Interface()); err != nil {
				return err
			}
			if err := e.value(vsig, rv.MapIndex(keys[i]).Interface()); err != nil {
				return err
			}
		}
		return nil
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return dbusTypeErr("a"+elem, v)
	}
	for i := 0; i < rv.Len(); i++ {
		if err := e.value(elem, rv.Index(i).Interface()); err != nil {
			return err
		}
	}
	return nil
}

// ── Decoding ─────────────────────────────────────────────────────────────

// dbusDec reads values from b, whose offset 0 is 8-aligned in the message.
type dbusDec struct {
	b     []byte
	pos   int
	order binary.ByteOrder
	depth int
}

func (d *dbusDec) align(n int) error {
	p := (d.pos + n - 1) / n * n
	if p > len(d.b) {
		return errDbusShort
	}
	d.pos = p
	return nil
}

func (d *dbusDec) take(n int) ([]byte, error) {
	if n < 0 || n > len(d.b)-d.pos {
		return nil, errDbusShort
	}
	b := d.b[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *dbusDec) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(b), nil
}

func (d *dbusDec) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	if n > dbusMaxArray {
		return "", errDbusTooBig
	}
	b, err := d.take(int(n) + 1)
	if err != nil {
		return "", err
	}
	if b[n] != 0 {
		return "", errDbusNul
	}
	return string(b[:n]), nil
}

func (d *dbusDec) sig() (string, error) {
	b, err := d.take(1)
	if err != nil {
		return "", err
	}
	n := int(b[0])
	s, err := d.take(n + 1)
	if err != nil {
		return "", err
	}
	if s[n] != 0 {
		return "", errDbusNul
	}
	return string(s[:n]), nil
}

// fixed reads an n-byte number aligned to n.
func (d *dbusDec) fixed(n int) ([]byte, error) {
	if err := d.align(n); err != nil {
		return nil, err
	}
	return d.take(n)
}

// value reads one value of the single complete type sig.
func (d *dbusDec) value(sig string) (any, error) {
	if sig == "" {
		return nil, errDbusSig
	}
	switch c := sig[0]; c {
	case 'y':
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return b[0], nil
	case 'b':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		return n != 0, nil
	case 'n', 'q':
		b, err := d.fixed(2)
		if err != nil {
			return nil, err
		}
		u := d.order.Uint16(b)
		if c == 'n' {
			return int16(u), nil
		}
		return u, nil
	case 'i', 'u':
		u, err := d.u32()
		if err != nil {
			return nil, err
		}
		if c == 'i' {
			return int32(u), nil
		}
		return u, nil
	case 'x', 't', 'd':
		b, err := d.fixed(8)
		if err != nil {
			return nil, err
		}
		u := d.order.Uint64(b)
		switch c {
		case 'x':
			return int64(u), nil
		case 't':
			return u, nil
		}
		return math.Float64frombits(u), nil
	case 's':
		s, err := d.str()
		if err != nil {
			return nil, err
		}
		return s, nil
	case 'o':
		s, err := d.str()
		if err != nil {
			return nil, err
		}
		return dbusObjectPath(s), nil
	case 'g':
		s, err := d.sig()
		if err != nil {
			return nil, err
		}
		return dbusSignature(s), nil
	case 'v':
		s, err := d.sig()
		if err != nil {
			return nil, err
		}
		t, rest, err := dbusNextType(s)
		if err != nil || rest != "" {
			return nil, errDbusSig
		}
		if d.depth++; d.depth > dbusMaxDepth {
			return nil, errDbusSig
		}
		val, err := d.value(t)
		d.depth--
		if err != nil {
			return nil, err
		}
		return dbusVariant{Sig: dbusSignature(t), Value: val}, nil
	case 'a':
		return d.array(sig[1:])
	case '(':
		if len(sig) < 3 || sig[len(sig)-1] != ')' {
			return nil, errDbusSig
		}
		fields, err := dbusSplitSig(sig[1 : len(sig)-1])
		if err != nil {
			return nil, err
		}
		if err := d.align(8); err != nil {
			return nil, err
		}
		if d.depth++; d.depth > dbusMaxDepth {
			return nil, errDbusSig
		}
		out := make([]any, 0, len(fields))
		for _, f := range fields {
			v, err := d.value(f)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		d.depth--
		return out, nil
	}
	return nil, errDbusSig
}

// array reads an array of elem.
func (d *dbusDec) array(elem string) (any, error) {
	if elem == "" {
		return nil, errDbusSig
	}
	n, err := d.u32()
	if err != nil {
		return nil, err
	}
	if n > dbusMaxArray {
		return nil, errDbusTooBig
	}
	if err := d.align(dbusAlignOf(elem[0])); err != nil {
		if n != 0 {
			return nil, err
		}
		// An empty array closing a message whose sender left out the
		// padding: nothing is lost.
		d.pos = len(d.b)
	}
	end := d.pos + int(n)
	if end > len(d.b) {
		return nil, errDbusShort
	}
	if elem == "y" {
		out := make([]byte, n)
		copy(out, d.b[d.pos:end])
		d.pos = end
		return out, nil
	}
	if d.depth++; d.depth > dbusMaxDepth {
		return nil, errDbusSig
	}
	defer func() { d.depth-- }()
	if elem[0] == '{' {
		if len(elem) < 4 {
			return nil, errDbusSig
		}
		ksig, vsig := elem[1:2], elem[2:len(elem)-1]
		out := map[string]any{}
		for d.pos < end {
			if err := d.align(8); err != nil {
				return nil, err
			}
			k, err := d.value(ksig)
			if err != nil {
				return nil, err
			}
			v, err := d.value(vsig)
			if err != nil {
				return nil, err
			}
			out[dbusKeyString(k)] = v
		}
		if d.pos != end {
			return nil, errDbusShort
		}
		return out, nil
	}
	out := []any{}
	for d.pos < end {
		v, err := d.value(elem)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if d.pos != end {
		return nil, errDbusShort
	}
	return out, nil
}

// dbusKeyString is a decoded dict key as the map's string key.
func dbusKeyString(k any) string {
	if s, ok := dbusStringOf(k); ok {
		return s
	}
	return fmt.Sprint(k)
}

// ── Messages ─────────────────────────────────────────────────────────────

// marshal writes the message with serial.
func (m *dbusMsg) marshal(serial uint32) ([]byte, error) {
	types, err := dbusSplitSig(string(m.Sig))
	if err != nil {
		return nil, err
	}
	if len(types) != len(m.Body) {
		return nil, fmt.Errorf("dbus: signature %q takes %d values, got %d", m.Sig, len(types), len(m.Body))
	}
	var body dbusEnc
	for i, t := range types {
		if err := body.value(t, m.Body[i]); err != nil {
			return nil, err
		}
	}
	fields := make([]any, 0, 7)
	add := func(code byte, sig dbusSignature, v any) {
		fields = append(fields, []any{code, dbusVariant{Sig: sig, Value: v}})
	}
	if m.Path != "" {
		add(dbusFieldPath, "o", m.Path)
	}
	if m.Iface != "" {
		add(dbusFieldInterface, "s", m.Iface)
	}
	if m.Member != "" {
		add(dbusFieldMember, "s", m.Member)
	}
	if m.ErrName != "" {
		add(dbusFieldErrorName, "s", m.ErrName)
	}
	if m.ReplySerial != 0 {
		add(dbusFieldReplySerial, "u", m.ReplySerial)
	}
	if m.Dest != "" {
		add(dbusFieldDestination, "s", m.Dest)
	}
	if m.Sig != "" {
		add(dbusFieldSignature, "g", m.Sig)
	}
	h := dbusEnc{b: make([]byte, 0, 128+len(body.b))}
	h.b = append(h.b, 'l', m.Type, m.Flags, 1)
	h.u32(uint32(len(body.b)))
	h.u32(serial)
	if err := h.value("a(yv)", fields); err != nil {
		return nil, err
	}
	h.pad(8)
	if len(h.b)+len(body.b) > dbusMaxMessage {
		return nil, errDbusTooBig
	}
	return append(h.b, body.b...), nil
}

// dbusReadMsg reads one message from r.
func dbusReadMsg(r io.Reader) (*dbusMsg, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch fixed[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, errDbusHeader
	}
	if fixed[3] != 1 {
		return nil, errDbusHeader
	}
	bodyLen := order.Uint32(fixed[4:8])
	fieldsLen := order.Uint32(fixed[12:16])
	if bodyLen > dbusMaxMessage || fieldsLen > dbusMaxMessage {
		return nil, errDbusTooBig
	}
	hdrEnd := 16 + int(fieldsLen)
	bodyAt := (hdrEnd + 7) &^ 7
	total := bodyAt + int(bodyLen)
	if total > dbusMaxMessage {
		return nil, errDbusTooBig
	}
	buf := make([]byte, total)
	copy(buf, fixed[:])
	if _, err := io.ReadFull(r, buf[16:]); err != nil {
		return nil, err
	}
	m := &dbusMsg{Type: fixed[1], Flags: fixed[2], Serial: order.Uint32(fixed[8:12])}
	hd := &dbusDec{b: buf[:hdrEnd], pos: 12, order: order}
	raw, err := hd.value("a(yv)")
	if err != nil {
		return nil, err
	}
	list, _ := raw.([]any)
	for _, f := range list {
		st, ok := f.([]any)
		if !ok || len(st) != 2 {
			continue
		}
		code, _ := st[0].(byte)
		v, _ := st[1].(dbusVariant)
		switch code {
		case dbusFieldPath:
			m.Path, _ = v.Value.(dbusObjectPath)
		case dbusFieldInterface:
			m.Iface, _ = v.Value.(string)
		case dbusFieldMember:
			m.Member, _ = v.Value.(string)
		case dbusFieldErrorName:
			m.ErrName, _ = v.Value.(string)
		case dbusFieldReplySerial:
			m.ReplySerial, _ = v.Value.(uint32)
		case dbusFieldDestination:
			m.Dest, _ = v.Value.(string)
		case dbusFieldSender:
			m.Sender, _ = v.Value.(string)
		case dbusFieldSignature:
			m.Sig, _ = v.Value.(dbusSignature)
		}
	}
	if bodyLen == 0 {
		return m, nil
	}
	types, err := dbusSplitSig(string(m.Sig))
	if err != nil {
		return nil, err
	}
	bd := &dbusDec{b: buf[bodyAt:], order: order}
	for _, t := range types {
		v, err := bd.value(t)
		if err != nil {
			return nil, err
		}
		m.Body = append(m.Body, v)
	}
	return m, nil
}

// ── Connection ───────────────────────────────────────────────────────────

// dbusIface is one interface of an exported object: its properties (read
// for Get, GetAll and GetManagedObjects; nil for none) and its methods.
type dbusIface struct {
	Name    string
	Props   func() map[string]dbusVariant
	Methods map[string]func(*dbusCall)
}

func (i *dbusIface) props() map[string]dbusVariant {
	if i.Props == nil {
		return map[string]dbusVariant{}
	}
	return i.Props()
}

// dbusCall is an incoming method call. Its handler answers with Reply or
// Fail, at once or later from any goroutine; only the first answer counts.
type dbusCall struct {
	c       *dbusConn
	Msg     *dbusMsg
	replied atomic.Bool
}

// Reply answers with sig and args.
func (k *dbusCall) Reply(sig string, args ...any) error {
	if !k.replied.CompareAndSwap(false, true) || k.Msg.Flags&dbusFlagNoReply != 0 {
		return nil
	}
	_, err := k.c.send(&dbusMsg{Type: dbusTypeReturn, Dest: k.Msg.Sender, ReplySerial: k.Msg.Serial,
		Sig: dbusSignature(sig), Body: args}, nil)
	return err
}

// Fail answers with an error name and a sentence.
func (k *dbusCall) Fail(name, text string) error {
	if !k.replied.CompareAndSwap(false, true) || k.Msg.Flags&dbusFlagNoReply != 0 {
		return nil
	}
	_, err := k.c.send(&dbusMsg{Type: dbusTypeError, ErrName: name, Dest: k.Msg.Sender,
		ReplySerial: k.Msg.Serial, Sig: "s", Body: []any{text}}, nil)
	return err
}

// dbusConn is one connection to the bus.
type dbusConn struct {
	nc   net.Conn
	rd   *bufio.Reader
	name string

	onSignal func(*dbusMsg)

	wmu sync.Mutex

	mu       sync.Mutex
	serial   uint32
	pending  map[uint32]chan *dbusMsg
	objects  map[dbusObjectPath][]*dbusIface
	managers map[dbusObjectPath]bool
	err      error

	done      chan struct{}
	closeOnce sync.Once
}

// dbusParseAddrs reads the unix: entries of a D-Bus address list: a path, or
// "@name" for an abstract socket.
func dbusParseAddrs(list string) []string {
	var out []string
	for _, one := range strings.Split(list, ";") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(one), "unix:")
		if !ok {
			continue
		}
		for _, kv := range strings.Split(rest, ",") {
			k, raw, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			val, ok := dbusUnescape(raw)
			if !ok || val == "" {
				continue
			}
			switch k {
			case "path":
				out = append(out, val)
			case "abstract":
				out = append(out, "@"+val)
			}
		}
	}
	return out
}

// dbusUnescape undoes an address value's %XX escapes.
func dbusUnescape(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		n, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(n))
		i += 2
	}
	return b.String(), true
}

// dbusSystemAddrs lists the system bus sockets to try, in order.
func dbusSystemAddrs() []string {
	out := dbusParseAddrs(os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"))
	return append(out, dbusSystemSockets...)
}

// dbusSystemBusReachable reports whether a system bus socket is there (an
// abstract one cannot be looked for, so it counts). It does not connect.
func dbusSystemBusReachable() bool {
	for _, a := range dbusSystemAddrs() {
		if strings.HasPrefix(a, "@") {
			return true
		}
		if fi, err := os.Stat(a); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return true
		}
	}
	return false
}

// dbusDialSystem connects to the system bus. onSignal (may be nil) gets every
// signal the bus routes here, on the reading goroutine.
func dbusDialSystem(onSignal func(*dbusMsg)) (*dbusConn, error) {
	var lastErr error
	tried := map[string]bool{}
	for _, addr := range dbusSystemAddrs() {
		if tried[addr] {
			continue
		}
		tried[addr] = true
		nc, err := net.DialTimeout("unix", addr, dbusDialTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		c, err := dbusHandshake(nc, onSignal)
		if err != nil {
			_ = nc.Close()
			lastErr = err
			continue
		}
		return c, nil
	}
	if lastErr == nil {
		lastErr = errDbusNoBus
	}
	return nil, lastErr
}

// dbusHandshake authenticates (SASL EXTERNAL with the process's uid), starts
// the reader and says Hello. On an error the caller closes nc.
func dbusHandshake(nc net.Conn, onSignal func(*dbusMsg)) (*dbusConn, error) {
	_ = nc.SetDeadline(time.Now().Add(dbusDialTimeout))
	rd := bufio.NewReader(nc)
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if _, err := nc.Write([]byte("\x00AUTH EXTERNAL " + uid + "\r\n")); err != nil {
		return nil, err
	}
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "OK ") {
		return nil, fmt.Errorf("the system message bus refused JukaHub (%s)", strings.TrimSpace(line))
	}
	if _, err := nc.Write([]byte("BEGIN\r\n")); err != nil {
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	c := &dbusConn{
		nc:       nc,
		rd:       rd,
		onSignal: onSignal,
		pending:  map[uint32]chan *dbusMsg{},
		objects:  map[dbusObjectPath][]*dbusIface{},
		managers: map[dbusObjectPath]bool{},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	reply, err := c.CallTimeout(dbusDialTimeout, dbusBusName, dbusBusPath, dbusBusName, "Hello", "")
	if err != nil {
		c.Close()
		return nil, err
	}
	c.name = reply.argString(0)
	return c, nil
}

// Close ends the connection; calls waiting for replies return at once.
func (c *dbusConn) Close() {
	c.fail(errDbusClosed)
}

// Done is closed when the connection has ended.
func (c *dbusConn) Done() <-chan struct{} {
	return c.done
}

// Err is why the connection ended (nil while it is up).
func (c *dbusConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *dbusConn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.nc.Close()
	})
}

// readLoop is the one reader: replies to their callers, signals to onSignal,
// calls to the exported objects.
func (c *dbusConn) readLoop() {
	for {
		m, err := dbusReadMsg(c.rd)
		if err != nil {
			if c.Err() == nil {
				logDebug("dbus", "connection ended: %v", err)
			}
			c.fail(err)
			return
		}
		switch m.Type {
		case dbusTypeReturn, dbusTypeError:
			c.mu.Lock()
			ch := c.pending[m.ReplySerial]
			delete(c.pending, m.ReplySerial)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case dbusTypeSignal:
			if c.onSignal != nil {
				c.safely(m, c.onSignal)
			}
		case dbusTypeCall:
			c.safely(m, c.dispatch)
		}
	}
}

// safely runs a handler; a panic in one is logged and the reader goes on.
func (c *dbusConn) safely(m *dbusMsg, fn func(*dbusMsg)) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("dbus", "handler for %s.%s panicked: %v", m.Iface, m.Member, r)
		}
	}()
	fn(m)
}

// send writes m with a new serial; reply (cap 1, may be nil) gets the answer.
func (c *dbusConn) send(m *dbusMsg, reply chan *dbusMsg) (uint32, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, err
	}
	c.serial++
	if c.serial == 0 {
		c.serial = 1
	}
	serial := c.serial
	if reply != nil {
		c.pending[serial] = reply
	}
	c.mu.Unlock()
	data, err := m.marshal(serial)
	if err == nil {
		err = c.write(data)
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, serial)
		c.mu.Unlock()
		return 0, err
	}
	return serial, nil
}

func (c *dbusConn) write(data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(dbusWriteTimeout))
	if _, err := c.nc.Write(data); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

// CallTimeout calls a method and waits up to timeout for its reply. An error
// reply comes back as *dbusError (with the reply message too).
func (c *dbusConn) CallTimeout(timeout time.Duration, dest string, path dbusObjectPath, iface, member, sig string, args ...any) (*dbusMsg, error) {
	ch := make(chan *dbusMsg, 1)
	m := &dbusMsg{Type: dbusTypeCall, Flags: dbusCallFlags(dest), Dest: dest, Path: path, Iface: iface, Member: member,
		Sig: dbusSignature(sig), Body: args}
	serial, err := c.send(m, ch)
	if err != nil {
		return nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.Type == dbusTypeError {
			return r, &dbusError{Name: r.ErrName, Text: r.argString(0)}
		}
		return r, nil
	case <-t.C:
		c.mu.Lock()
		delete(c.pending, serial)
		c.mu.Unlock()
		return nil, &dbusError{Name: "org.freedesktop.DBus.Error.NoReply", Text: "No answer to " + member + " in time"}
	case <-c.done:
		if err := c.Err(); err != nil {
			return nil, err
		}
		return nil, errDbusClosed
	}
}

// dbusCallFlags are a call's header flags: no auto-start for a service; the
// bus's own calls (Hello, AddMatch) carry none.
func dbusCallFlags(dest string) byte {
	if dest == "" || dest == dbusBusName {
		return 0
	}
	return dbusFlagNoAutoStart
}

// Call is CallTimeout with dbusCallTimeout.
func (c *dbusConn) Call(dest string, path dbusObjectPath, iface, member, sig string, args ...any) (*dbusMsg, error) {
	return c.CallTimeout(dbusCallTimeout, dest, path, iface, member, sig, args...)
}

// CallNoReply sends a call and asks for no answer.
func (c *dbusConn) CallNoReply(dest string, path dbusObjectPath, iface, member, sig string, args ...any) error {
	_, err := c.send(&dbusMsg{Type: dbusTypeCall, Flags: dbusFlagNoReply | dbusCallFlags(dest), Dest: dest, Path: path,
		Iface: iface, Member: member, Sig: dbusSignature(sig), Body: args}, nil)
	return err
}

// Emit sends a signal to whoever listens.
func (c *dbusConn) Emit(path dbusObjectPath, iface, member, sig string, args ...any) error {
	_, err := c.send(&dbusMsg{Type: dbusTypeSignal, Flags: dbusFlagNoReply, Path: path, Iface: iface,
		Member: member, Sig: dbusSignature(sig), Body: args}, nil)
	return err
}

// EmitPropsChanged sends org.freedesktop.DBus.Properties.PropertiesChanged.
func (c *dbusConn) EmitPropsChanged(path dbusObjectPath, iface string, changed map[string]dbusVariant) error {
	return c.Emit(path, dbusPropsIface, "PropertiesChanged", "sa{sv}as", iface, changed, []string{})
}

// AddMatch asks the bus for the signals rule matches.
func (c *dbusConn) AddMatch(rule string) error {
	_, err := c.Call(dbusBusName, dbusBusPath, dbusBusName, "AddMatch", "s", rule)
	return err
}

// Export puts an object on the bus (replacing one at the same path).
func (c *dbusConn) Export(path dbusObjectPath, ifaces ...*dbusIface) {
	c.mu.Lock()
	c.objects[path] = ifaces
	c.mu.Unlock()
}

// ExportManager answers ObjectManager.GetManagedObjects at root with every
// exported object below it.
func (c *dbusConn) ExportManager(root dbusObjectPath) {
	c.mu.Lock()
	c.managers[root] = true
	c.mu.Unlock()
}

// dispatch answers an incoming call (on the reading goroutine).
func (c *dbusConn) dispatch(m *dbusMsg) {
	call := &dbusCall{c: c, Msg: m}
	switch m.Iface {
	case dbusPropsIface:
		c.serveProps(call)
		return
	case dbusObjMgrIface:
		c.mu.Lock()
		root := c.managers[m.Path]
		c.mu.Unlock()
		if root && m.Member == "GetManagedObjects" {
			_ = call.Reply("a{oa{sa{sv}}}", c.managedObjects(m.Path))
			return
		}
	case dbusPeerIface:
		if m.Member == "Ping" {
			_ = call.Reply("")
			return
		}
	}
	c.mu.Lock()
	ifaces, known := c.objects[m.Path]
	c.mu.Unlock()
	if !known {
		_ = call.Fail("org.freedesktop.DBus.Error.UnknownObject", "No object at "+string(m.Path))
		return
	}
	for _, ifc := range ifaces {
		if m.Iface != "" && ifc.Name != m.Iface {
			continue
		}
		if fn := ifc.Methods[m.Member]; fn != nil {
			fn(call)
			return
		}
	}
	_ = call.Fail("org.freedesktop.DBus.Error.UnknownMethod", "No method "+m.Member+" in "+m.Iface)
}

// serveProps answers org.freedesktop.DBus.Properties.
func (c *dbusConn) serveProps(call *dbusCall) {
	m := call.Msg
	c.mu.Lock()
	ifaces, known := c.objects[m.Path]
	c.mu.Unlock()
	if !known {
		_ = call.Fail("org.freedesktop.DBus.Error.UnknownObject", "No object at "+string(m.Path))
		return
	}
	var ifc *dbusIface
	for _, x := range ifaces {
		if x.Name == m.argString(0) {
			ifc = x
			break
		}
	}
	switch m.Member {
	case "GetAll", "Get":
		if ifc == nil {
			_ = call.Fail("org.freedesktop.DBus.Error.UnknownInterface", "No interface "+m.argString(0))
			return
		}
		props := ifc.props()
		if m.Member == "GetAll" {
			_ = call.Reply("a{sv}", props)
			return
		}
		v, ok := props[m.argString(1)]
		if !ok {
			_ = call.Fail("org.freedesktop.DBus.Error.UnknownProperty", "No property "+m.argString(1))
			return
		}
		_ = call.Reply("v", v)
	case "Set":
		_ = call.Fail("org.freedesktop.DBus.Error.PropertyReadOnly", "These properties are read-only")
	default:
		_ = call.Fail("org.freedesktop.DBus.Error.UnknownMethod", "No method "+m.Member)
	}
}

// managedObjects is every exported object below root with its interfaces'
// properties.
func (c *dbusConn) managedObjects(root dbusObjectPath) map[dbusObjectPath]map[string]map[string]dbusVariant {
	prefix := string(root) + "/"
	if root == "/" {
		prefix = "/"
	}
	type entry struct {
		path   dbusObjectPath
		ifaces []*dbusIface
	}
	var list []entry
	c.mu.Lock()
	for p, ifs := range c.objects {
		if p != root && strings.HasPrefix(string(p), prefix) {
			list = append(list, entry{path: p, ifaces: ifs})
		}
	}
	c.mu.Unlock()
	out := make(map[dbusObjectPath]map[string]map[string]dbusVariant, len(list))
	for _, e := range list {
		im := make(map[string]map[string]dbusVariant, len(e.ifaces))
		for _, ifc := range e.ifaces {
			im[ifc.Name] = ifc.props()
		}
		out[e.path] = im
	}
	return out
}

// ── Reading BlueZ's answers ──────────────────────────────────────────────

// dbusDict is a decoded dict, or nil when v is not one.
func dbusDict(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// dbusPropString reads a string property out of a decoded a{sv}.
func dbusPropString(props map[string]any, key string) string {
	if v, ok := props[key].(dbusVariant); ok {
		s, _ := dbusStringOf(v.Value)
		return s
	}
	return ""
}

// dbusPropBool reads a boolean property; ok is false when it is not there.
func dbusPropBool(props map[string]any, key string) (val, ok bool) {
	if v, found := props[key].(dbusVariant); found {
		val, ok = v.Value.(bool)
	}
	return val, ok
}

// dbusStrings reads a decoded as (or ao).
func dbusStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := dbusStringOf(x); ok {
			out = append(out, s)
		}
	}
	return out
}
