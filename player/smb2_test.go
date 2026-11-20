package main

// smb2_test.go — the SMB client's crypto against published vectors, its
// parsers of server data against hand-built and hostile replies (they must
// fail, never panic or read past the buffer), and path rejection.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func smb2TestHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 1320 appendix A.5.
func TestSMB2MD4(t *testing.T) {
	for _, tc := range [][2]string{
		{"", "31d6cfe0d16ae931b73c59d7e0c089c0"},
		{"a", "bde52cb31de33e46245e05fbdbd6fb24"},
		{"abc", "a448017aaf21d8525fc10ae87aa6729d"},
		{"message digest", "d9130a8164549fe818874806e1c7014b"},
		{"abcdefghijklmnopqrstuvwxyz", "d79e1c308aa5bbcdeea8ed63df412da9"},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", "043f8582f241db351ce627e153e7f0e4"},
		{strings.Repeat("1234567890", 8), "e33b4ddc9c38f2199c3e7b164fcc0536"},
	} {
		if got := smb2MD4([]byte(tc[0])); hex.EncodeToString(got[:]) != tc[1] {
			t.Errorf("MD4(%q) = %x, want %s", tc[0], got, tc[1])
		}
	}
}

// MS-NLMP 4.2.1 common values. NTOWFv1 (4.2.2.1.2), NTOWFv2 (4.2.4.1.1),
// the LMv2 response (4.2.4.2.1), NTProofStr and the session base key
// (4.2.4.1.2, 4.2.4.2.2) are the published ones; the AV pairs are the
// 36 bytes of 4.2.4.3 (Domain, Server, MsvAvEOL).
func TestSMB2NTLMv2(t *testing.T) {
	if got := hex.EncodeToString(smb2NTOWFv1("Password")); got != "a4f49c406510bdcab6824ee7c30fd852" {
		t.Errorf("NTOWFv1 = %s", got)
	}
	key := smb2NTOWFv2("Password", "User", "Domain")
	if got := hex.EncodeToString(key); got != "0c868a403bfd7a93a3001ef22ef02e3f" {
		t.Errorf("NTOWFv2 = %s", got)
	}
	ti := smb2TestHex(t, "02000c0044006f006d00610069006e0001000c0053006500720076006500720000000000")
	nt, lm, base := smb2NTLMv2Response(key, smb2TestHex(t, "0123456789abcdef"), bytes.Repeat([]byte{0xaa}, 8), 0, ti)
	if got := hex.EncodeToString(lm); got != "86c35097ac9cec102554764a57cccc19aaaaaaaaaaaaaaaa" {
		t.Errorf("LMv2 = %s", got)
	}
	if got := hex.EncodeToString(nt[:16]); got != "68cd0ab851e51c96aabc927bebef6a1c" {
		t.Errorf("NTProofStr = %s", got)
	}
	if want := 16 + 32 + len(ti); len(nt) != want || !bytes.Equal(nt[44:44+len(ti)], ti) {
		t.Errorf("NTLMv2 response layout: len %d, want %d", len(nt), want)
	}
	if got := hex.EncodeToString(base); got != "8de40ccadbc14a82f15cb0ad0de95ca3" {
		t.Errorf("session base key = %s", got)
	}

	// The CHALLENGE message of MS-NLMP 4.2.4.3, then every truncation of it
	// and a target-info offset far past the end.
	msg := smb2TestHex(t, "4e544c4d53535000020000000c000c003800000033828ae20123456789abcdef"+
		"00000000000000002400240044000000060070170000000f530065007200760065007200"+
		"02000c0044006f006d00610069006e0001000c0053006500720076006500720000000000")
	ch, err := smb2ParseNtlmChallenge(msg)
	if err != nil || hex.EncodeToString(ch.challenge) != "0123456789abcdef" || !bytes.Equal(ch.targetInfo, ti) {
		t.Fatalf("challenge: %+v, %v", ch, err)
	}
	if _, ok := smb2NtlmTimestamp(ch.targetInfo); ok {
		t.Error("time stamp found where there is none")
	}
	for n := range len(msg) {
		if _, err := smb2ParseNtlmChallenge(msg[:n]); err == nil {
			t.Errorf("truncated challenge (%d bytes) accepted", n)
		}
	}
	bad := append([]byte(nil), msg...)
	binary.LittleEndian.PutUint32(bad[44:], 0xffffffff)
	if _, err := smb2ParseNtlmChallenge(bad); err == nil {
		t.Error("challenge with a wild target-info offset accepted")
	}

	// SPNEGO NegTokenResp with long-form lengths, and a rejected one.
	resp := smb2DER(0xa1, smb2DER(0x30,
		smb2DER(0xa0, []byte{0x0a, 0x01, 0x01}),
		smb2DER(0xa1, smb2OIDNTLM),
		smb2DER(0xa2, smb2DER(0x04, msg))))
	tok, rejected, err := smb2SpnegoToken(resp)
	if err != nil || rejected || !bytes.Equal(tok, msg) {
		t.Errorf("SPNEGO token: %v %v %d bytes", err, rejected, len(tok))
	}
	for n := range len(resp) {
		smb2SpnegoToken(resp[:n]) // must not panic
	}
	if _, rejected, _ := smb2SpnegoToken(smb2DER(0xa1, smb2DER(0x30, smb2DER(0xa0, []byte{0x0a, 0x01, 0x02})))); !rejected {
		t.Error("SPNEGO reject not seen")
	}
}

// RFC 4493 section 4.
func TestSMB2CMAC(t *testing.T) {
	blk, err := aes.NewCipher(smb2TestHex(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	if err != nil {
		t.Fatal(err)
	}
	m := smb2TestHex(t, "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e51"+
		"30c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710")
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "bb1d6929e95937287fa37d129b756746"},
		{16, "070a16b46b4d4144f79bdd9dd04a287c"},
		{40, "dfa66747de9ae63030ca32611497c827"},
		{64, "51f0bebf7e3b9d92fc49741779363cfe"},
	} {
		if got := smb2CMAC(blk, m[:tc.n]); hex.EncodeToString(got[:]) != tc.want {
			t.Errorf("CMAC(%d bytes) = %x, want %s", tc.n, got, tc.want)
		}
	}
}

// SMB 3.0 key derivation examples from Microsoft's Open Specifications
// blog ("SMB 2 and SMB 3 security in Windows 10", "Encryption in SMB 3.0").
func TestSMB2KDF(t *testing.T) {
	for _, tc := range []struct{ key, label, ctx, want string }{
		{"7CD451825D0450D235424E44BA6E78CC", "SMB2AESCMAC\x00", "SmbSign\x00", "0B7E9C5CAC36C0F6EA9AB275298CEDCE"},
		{"B4546771B515F766A86735532DD6C4F0", "SMB2AESCMAC\x00", "SmbSign\x00", "F773CD23C18FD1E08EE510CADA7CF852"},
		{"B4546771B515F766A86735532DD6C4F0", "SMB2AESCCM\x00", "ServerIn \x00", "261B72350558F2E9DCF613070383EDBF"},
		{"B4546771B515F766A86735532DD6C4F0", "SMB2AESCCM\x00", "ServerOut\x00", "8FE2B57EC34D2DB5B1A9727F526BBDB5"},
	} {
		got := smb2KDF(smb2TestHex(t, tc.key), []byte(tc.label), []byte(tc.ctx))
		if !strings.EqualFold(hex.EncodeToString(got), tc.want) {
			t.Errorf("KDF(%s, %q) = %x, want %s", tc.key, tc.label, got, tc.want)
		}
	}
}

// A sealed message opens again; a flipped bit, another session id or a
// cut-off message does not.
func TestSMB2Transform(t *testing.T) {
	blk, _ := aes.NewCipher(make([]byte, 16))
	g, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatal(err)
	}
	c := &smb2Crypt{enc: g, dec: g}
	msg := make([]byte, 100)
	copy(msg, "\xfeSMB")
	frame := c.seal(msg, 42)
	if plain, err := c.open(append([]byte(nil), frame...), 42); err != nil || !bytes.Equal(plain, msg) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := c.open(append([]byte(nil), frame...), 43); err == nil {
		t.Error("wrong session accepted")
	}
	flip := append([]byte(nil), frame...)
	flip[len(flip)-1] ^= 1
	if _, err := c.open(flip, 42); err == nil {
		t.Error("tampered message accepted")
	}
	for n := range len(frame) {
		if _, err := c.open(append([]byte(nil), frame[:n]...), 42); err == nil {
			t.Errorf("truncated message (%d bytes) accepted", n)
		}
	}
}

func smb2TestNegReply(dialect uint16, ctxs ...[]byte) []byte {
	msg := make([]byte, smb2HeaderLen+64)
	copy(msg, "\xfeSMB")
	p := msg[smb2HeaderLen:]
	binary.LittleEndian.PutUint16(p[0:], 65)
	binary.LittleEndian.PutUint16(p[2:], smb2SecSigningEnabled|smb2SecSigningRequired)
	binary.LittleEndian.PutUint16(p[4:], dialect)
	binary.LittleEndian.PutUint16(p[6:], uint16(len(ctxs)))
	binary.LittleEndian.PutUint32(p[24:], smb2CapLargeMTU)
	binary.LittleEndian.PutUint32(p[28:], 8<<20)
	binary.LittleEndian.PutUint32(p[32:], 8<<20)
	if len(ctxs) > 0 {
		binary.LittleEndian.PutUint32(p[60:], uint32(len(msg)))
	}
	for i, c := range ctxs {
		if i > 0 {
			msg = smb2Pad8(msg)
		}
		msg = append(msg, c...)
	}
	return msg
}

func TestSMB2ParseNegotiate(t *testing.T) {
	preauth := smb2AppendNegCtx(nil, 1, append([]byte{1, 0, 32, 0, 1, 0}, make([]byte, 32)...))
	gcm := smb2AppendNegCtx(nil, 2, []byte{1, 0, 2, 0})
	full := smb2TestNegReply(smb2Dialect311, preauth, gcm)
	in, err := smb2ParseNegotiate(full)
	if err != nil || in.dialect != smb2Dialect311 || in.cipher != smb2CipherAES128GCM ||
		in.maxRead != 8<<20 || in.secMode&smb2SecSigningRequired == 0 {
		t.Fatalf("3.1.1 reply: %+v, %v", in, err)
	}
	if in, err := smb2ParseNegotiate(smb2TestNegReply(smb2Dialect210)); err != nil || in.dialect != smb2Dialect210 {
		t.Errorf("2.1 reply: %+v, %v", in, err)
	}
	for n := range len(full) {
		if _, err := smb2ParseNegotiate(full[:n]); err == nil {
			t.Errorf("truncated reply (%d bytes) accepted", n)
		}
	}
	hostile := map[string][]byte{
		"no preauth context": smb2TestNegReply(smb2Dialect311, gcm),
		"SMB1 wildcard":      smb2TestNegReply(0x02ff),
		"unknown dialect":    smb2TestNegReply(0x0222),
	}
	huge := append([]byte(nil), full...)
	binary.LittleEndian.PutUint16(huge[smb2HeaderLen+64+2:], 0xffff)
	hostile["context length past the end"] = huge
	odd := append([]byte(nil), full...)
	binary.LittleEndian.PutUint32(odd[smb2HeaderLen+60:], smb2HeaderLen+64+4)
	hostile["unaligned context offset"] = odd
	far := append([]byte(nil), full...)
	binary.LittleEndian.PutUint32(far[smb2HeaderLen+60:], 0xfffffff8)
	hostile["context offset past the end"] = far
	many := append([]byte(nil), full...)
	binary.LittleEndian.PutUint16(many[smb2HeaderLen+6:], 0xffff)
	hostile["more contexts than sent"] = many
	for name, msg := range hostile {
		if _, err := smb2ParseNegotiate(msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func smb2TestDirEntry(name string, attrs uint32, size uint64, last bool) []byte {
	n := smb2UTF16(name)
	e := make([]byte, 64, 64+len(n)+8)
	binary.LittleEndian.PutUint64(e[24:], smb2FileTimeEpoch+10000000)
	binary.LittleEndian.PutUint64(e[40:], size)
	binary.LittleEndian.PutUint32(e[56:], attrs)
	binary.LittleEndian.PutUint32(e[60:], uint32(len(n)))
	e = smb2Pad8(append(e, n...))
	if !last {
		binary.LittleEndian.PutUint32(e[0:], uint32(len(e)))
	}
	return e
}

func TestSMB2ParseDirInfo(t *testing.T) {
	var buf []byte
	for _, e := range [][]byte{
		smb2TestDirEntry(".", smb2AttrDirectory, 0, false),
		smb2TestDirEntry("..", smb2AttrDirectory, 0, false),
		smb2TestDirEntry("GBA", smb2AttrDirectory, 0, false),
		smb2TestDirEntry("Mario (USA).gba", 0x20, 4096, false),
		smb2TestDirEntry("../evil", 0x20, 1, false),
		smb2TestDirEntry(`a\b`, 0x20, 1, false),
		smb2TestDirEntry("x:stream", 0x20, 1, false),
		smb2TestDirEntry("@eaDir", smb2AttrDirectory, 0, true),
	} {
		buf = append(buf, e...)
	}
	got, err := smb2ParseDirInfo(buf, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("entries: %+v, %v", got, err)
	}
	if !got[0].IsDir || got[0].Name != "GBA" || got[0].Hidden {
		t.Errorf("folder: %+v", got[0])
	}
	if got[1].Name != "Mario (USA).gba" || got[1].IsDir || got[1].Size != 4096 || !got[1].ModTime.Equal(time.Unix(1, 0)) {
		t.Errorf("file: %+v", got[1])
	}
	if !got[2].Hidden {
		t.Errorf("@eaDir not hidden: %+v", got[2])
	}
	for n := range len(buf) {
		smb2ParseDirInfo(buf[:n], nil) // must not panic
	}
	pastEnd := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(pastEnd[0:], uint32(len(buf)+8))
	longName := smb2TestDirEntry("a", 0, 0, true)
	binary.LittleEndian.PutUint32(longName[60:], 0xfffffff0)
	oddName := smb2TestDirEntry("ab", 0, 0, true)
	binary.LittleEndian.PutUint32(oddName[60:], 3)
	for name, b := range map[string][]byte{"next past the end": pastEnd, "name past the end": longName, "odd name length": oddName, "empty": nil} {
		if _, err := smb2ParseDirInfo(b, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func smb2TestNDRStr(b []byte, s string) []byte {
	b = smb2TestAlign4(b)
	u := smb2UTF16(s + "\x00")
	n := uint32(len(u) / 2)
	b = binary.LittleEndian.AppendUint32(b, n)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, n)
	return append(b, u...)
}

func smb2TestAlign4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func TestSMB2ParseShareEnum(t *testing.T) {
	shares := []struct {
		name string
		typ  uint32
	}{{"Games", 0}, {"IPC$", 0x80000003}, {"ADMIN$", 0x80000000}, {"Printer", 1}, {"Media", 0}, {"games", 0}}
	u32 := binary.LittleEndian.AppendUint32
	stub := u32(u32(u32(nil, 1), 1), 0x20000)
	stub = u32(u32(u32(stub, uint32(len(shares))), 0x20004), uint32(len(shares)))
	for i, s := range shares {
		stub = u32(u32(u32(stub, 0x20008+uint32(i)*8), s.typ), 0x2000c+uint32(i)*8)
	}
	for _, s := range shares {
		stub = smb2TestNDRStr(stub, s.name)
		stub = smb2TestNDRStr(stub, "")
	}
	stub = u32(u32(u32(smb2TestAlign4(stub), uint32(len(shares))), 0), 0)
	got, err := smb2ParseShareEnum(stub)
	if err != nil || strings.Join(got, ",") != "Games,Media" {
		t.Fatalf("shares: %q, %v", got, err)
	}
	for n := range len(stub) {
		smb2ParseShareEnum(stub[:n]) // must not panic
	}
	huge := append([]byte(nil), stub...)
	binary.LittleEndian.PutUint32(huge[12:], 0x7fffffff)
	binary.LittleEndian.PutUint32(huge[20:], 0x7fffffff)
	if _, err := smb2ParseShareEnum(huge); err == nil {
		t.Error("absurd share count accepted")
	}
	denied := u32(u32(u32(u32(nil, 1), 1), 0), 5)
	if _, err := smb2ParseShareEnum(denied); err == nil {
		t.Error("ERROR_ACCESS_DENIED accepted")
	}
}

// Paths can never climb out of the share or name a stream, wildcard or
// device; names from a hostile listing are dropped.
func TestSMB2CleanPath(t *testing.T) {
	for _, tc := range [][2]string{
		{"", ""}, {"/", ""}, {"Roms/GBA", `Roms\GBA`}, {"/Roms//GBA/", `Roms\GBA`}, {"./a/./b", `a\b`},
		{`a\b`, `a\b`}, {"Super Mario (USA).zip", "Super Mario (USA).zip"}, {"a..b/.c", `a..b\.c`},
	} {
		if got, err := smb2CleanPath(tc[0]); err != nil || got != tc[1] {
			t.Errorf("smb2CleanPath(%q) = %q, %v; want %q", tc[0], got, err, tc[1])
		}
	}
	for _, in := range []string{"..", "../x", "a/../../b", `a\..\b`, "a/...", "a/.. ", "a/\x00b", "a\nb", "a/b:stream", "a/*.gba", "a/b?", "x|y"} {
		if got, err := smb2CleanPath(in); !errors.Is(err, errSMBBadPath) {
			t.Errorf("smb2CleanPath(%q) = %q, %v; want errSMBBadPath", in, got, err)
		}
	}
	for _, sh := range []string{"", "/", "a/b", `a\b`, "a:b", "a\x00", strings.Repeat("s", 81)} {
		if _, _, err := smb2CleanSharePath(sh, ""); !errors.Is(err, errSMBBadPath) {
			t.Errorf("share %q accepted", sh)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "c:d", "a\x00"} {
		if smb2ValidName(name) {
			t.Errorf("listing name %q accepted", name)
		}
	}
	if !smb2ValidName("Zelda (USA).sfc") {
		t.Error("plain file name refused")
	}
}
