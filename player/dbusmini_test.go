package main

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"testing"
)

// The vectors below came off a real dbus-daemon: dbusHelloHex is the Hello
// call dbusmini writes (the daemon accepted it and answered), the other two
// are calls libdbus (dbus-python) marshalled and the daemon delivered.
const (
	dbusHelloHex = "6c01000100000000010000006d00000001016f00150000002f6f72672f667265656465736b746f702f444275730000" +
		"0002017300140000006f72672e667265656465736b746f702e4442757300000000030173000500000048656c6c6f0000" +
		"0006017300140000006f72672e667265656465736b746f702e4442757300000000"
	// ReadValue({"offset": q 22, "device": o, "mtu": q 185}) from :1.1 to :1.0.
	dbusReadValueHex = "6c01000156000000020000009500000001016f001f0000002f6a756b616875622f63746c7061642f676174742f68" +
		"69642f7265706f72740006017300040000003a312e3000000000020173001d0000006f72672e626c75657a2e4761747443" +
		"686172616374657269737469633100000003017300090000005265616456616c7565000000000000000801670005617b" +
		"73767d00000000000007017300040000003a312e31000000004e00000000000000060000006f66667365740001710016" +
		"000600000064657669636500016f000000190000002f6f72672f626c75657a2f686369302f6465765f41415f42420000" +
		"00030000006d74750001710000b900"
	// Many(ybnqiuxtdsoga(yv)a(yv)a{sv}): every basic type, an empty array of
	// structs, a struct holding a variant, a dict holding a byte array.
	dbusManyHex = "6c0100017a000000030000007500000001016f00020000002f7800000000000006017300040000003a312e30000000" +
		"0002017300070000006f72672e782e490003017300040000004d616e7900000000080167001b79626e71697578746473" +
		"6f6761287976296128797629617b73767d000000000000000007017300040000003a312e310000000007000000010000" +
		"00fefffffff9ffffff0700000000000000fbffffffffffffff0100000000000080000000000000f83f03000000737472" +
		"00040000002f6f2f700005617b73767d0000000000000000000a00000000000000010173000100000078000000120000" +
		"00010000006b00026179000000020000000102"
)

func dbusTestBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad test hex: %v", err)
	}
	return b
}

// dbusManyBody is the Many call's body as the decoder returns it.
var dbusManyBody = []any{
	byte(7), true, int16(-2), uint16(65535), int32(-7), uint32(7), int64(-5), uint64(1<<63 + 1), 1.5,
	"str", dbusObjectPath("/o/p"), dbusSignature("a{sv}"),
	[]any{},
	[]any{[]any{byte(1), dbusVariant{Sig: "s", Value: "x"}}},
	map[string]any{"k": dbusVariant{Sig: "ay", Value: []byte{1, 2}}},
}

func TestDbusMarshalHello(t *testing.T) {
	m := &dbusMsg{Type: dbusTypeCall, Dest: dbusBusName, Path: dbusBusPath, Iface: dbusBusName, Member: "Hello"}
	got, err := m.marshal(1)
	if err != nil {
		t.Fatal(err)
	}
	if want := dbusTestBytes(t, dbusHelloHex); !bytes.Equal(got, want) {
		t.Fatalf("Hello:\n got %x\nwant %x", got, want)
	}
}

func TestDbusReadLibdbusCall(t *testing.T) {
	m, err := dbusReadMsg(bytes.NewReader(dbusTestBytes(t, dbusReadValueHex)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != dbusTypeCall || m.Serial != 2 || m.Path != "/jukahub/ctlpad/gatt/hid/report" ||
		m.Iface != "org.bluez.GattCharacteristic1" || m.Member != "ReadValue" || m.Sender != ":1.1" ||
		m.Dest != ":1.0" || m.Sig != "a{sv}" {
		t.Fatalf("header: %+v", m)
	}
	want := []any{map[string]any{
		"offset": dbusVariant{Sig: "q", Value: uint16(22)},
		"device": dbusVariant{Sig: "o", Value: dbusObjectPath("/org/bluez/hci0/dev_AA_BB")},
		"mtu":    dbusVariant{Sig: "q", Value: uint16(185)},
	}}
	if !reflect.DeepEqual(m.Body, want) {
		t.Fatalf("body:\n got %#v\nwant %#v", m.Body, want)
	}
}

func TestDbusManyTypesBothWays(t *testing.T) {
	raw := dbusTestBytes(t, dbusManyHex)
	m, err := dbusReadMsg(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Member != "Many" || m.Serial != 3 {
		t.Fatalf("header: %+v", m)
	}
	if !reflect.DeepEqual(m.Body, dbusManyBody) {
		t.Fatalf("decoded:\n got %#v\nwant %#v", m.Body, dbusManyBody)
	}
	// The encoder writes the same values exactly as libdbus did.
	types, err := dbusSplitSig(string(m.Sig))
	if err != nil {
		t.Fatal(err)
	}
	var e dbusEnc
	for i, ty := range types {
		if err := e.value(ty, dbusManyBody[i]); err != nil {
			t.Fatalf("encode %s: %v", ty, err)
		}
	}
	if body := raw[len(raw)-122:]; !bytes.Equal(e.b, body) {
		t.Fatalf("encoded:\n got %x\nwant %x", e.b, body)
	}
}

func TestDbusRoundTripSignal(t *testing.T) {
	objs := map[dbusObjectPath]map[string]map[string]dbusVariant{
		"/a/b": {"org.x.I": {"P": {Sig: "as", Value: []string{"1812"}}, "Q": {Sig: "b", Value: true}}, "org.x.J": {}},
		"/c":   {},
	}
	in := &dbusMsg{Type: dbusTypeSignal, Flags: dbusFlagNoReply, Path: "/j/r", Iface: dbusPropsIface,
		Member: "PropertiesChanged", Sig: "sa{sv}asa{oa{sa{sv}}}",
		Body: []any{"org.bluez.GattCharacteristic1", map[string]dbusVariant{"Value": {Sig: "ay", Value: []byte{1, 0, 8}}},
			[]string{}, objs}}
	raw, err := in.marshal(9)
	if err != nil {
		t.Fatal(err)
	}
	out, err := dbusReadMsg(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != dbusTypeSignal || out.Flags != dbusFlagNoReply || out.Serial != 9 || out.Path != in.Path ||
		out.Iface != in.Iface || out.Member != in.Member || out.Sig != in.Sig {
		t.Fatalf("header: %+v", out)
	}
	want := []any{
		"org.bluez.GattCharacteristic1",
		map[string]any{"Value": dbusVariant{Sig: "ay", Value: []byte{1, 0, 8}}},
		[]any{},
		map[string]any{
			"/a/b": map[string]any{
				"org.x.I": map[string]any{
					"P": dbusVariant{Sig: "as", Value: []any{"1812"}},
					"Q": dbusVariant{Sig: "b", Value: true},
				},
				"org.x.J": map[string]any{},
			},
			"/c": map[string]any{},
		},
	}
	if !reflect.DeepEqual(out.Body, want) {
		t.Fatalf("body:\n got %#v\nwant %#v", out.Body, want)
	}
}

func TestDbusReadBigEndian(t *testing.T) {
	// A method return, serial 5, reply to 7, body u 0x01020304, built by hand
	// from the specification.
	raw := []byte{
		'B', 2, 0, 1, 0, 0, 0, 4, 0, 0, 0, 5, 0, 0, 0, 15,
		5, 1, 'u', 0, 0, 0, 0, 7,
		8, 1, 'g', 0, 1, 'u', 0, 0,
		1, 2, 3, 4,
	}
	m, err := dbusReadMsg(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != dbusTypeReturn || m.Serial != 5 || m.ReplySerial != 7 || m.Sig != "u" ||
		!reflect.DeepEqual(m.Body, []any{uint32(0x01020304)}) {
		t.Fatalf("got %+v", m)
	}
}

func TestDbusSignatures(t *testing.T) {
	got, err := dbusSplitSig("a{sv}as(yv)a{oa{sa{sv}}}v")
	if err != nil || !reflect.DeepEqual(got, []string{"a{sv}", "as", "(yv)", "a{oa{sa{sv}}}", "v"}) {
		t.Fatalf("split: %q %v", got, err)
	}
	for _, bad := range []string{"a", "(", "()", "(ii", "a{vs}", "a{s}", "{sv}", "a{sva}", "z"} {
		if _, err := dbusSplitSig(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	var e dbusEnc
	if err := e.value("y", 256); err == nil {
		t.Error("256 written as a byte")
	}
	if err := e.value("s", "a\x00b"); err == nil {
		t.Error("string with a NUL written")
	}
}

func TestDbusParseAddrs(t *testing.T) {
	got := dbusParseAddrs("tcp:host=x;unix:path=/run/dbus/sys%5fbus,guid=1;unix:abstract=/tmp/dbus-1")
	if !reflect.DeepEqual(got, []string{"/run/dbus/sys_bus", "@/tmp/dbus-1"}) {
		t.Fatalf("got %q", got)
	}
}
