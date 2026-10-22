package main

// psremote_regist_test.go — PS5 pairing through the native helper: the input
// file contract, result parsing and the failure paths that must not pair.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPSNAccountID = "AAECAwQFBgc="

func ps5ResultFile(values map[string]string) map[string]string {
	return values
}

func TestPS5RegistInputContract(t *testing.T) {
	got := ps5RegistInput("192.168.1.51", "12345678", testPSNAccountID)
	want := "host=192.168.1.51\npin=12345678\naccount_id=" + testPSNAccountID + "\n"
	if got != want {
		t.Fatalf("input file =\n%s\nwant\n%s", got, want)
	}
	// The helper parses key=value lines and ignores anything else; an extra key
	// would silently change the file it deletes after reading.
	if strings.Contains(got, "target=") {
		t.Fatal("the PS5 helper picks its own target; the input must not set it")
	}
}

func TestPS5ParseResultPairsConsole(t *testing.T) {
	console, err := ps5ParseResult(ps5ResultFile(map[string]string{
		"name":        "Desk",
		"target":      "1000100",
		"rp_key_type": "2",
		"regist_key":  "3061316232633364", // ASCII "0a1b2c3d"
		"rp_key":      "000102030405060708090a0b0c0d0e0f",
		"server_mac":  "AABBCCDDEEFF",
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !console.PS5 || console.Target != psTargetPS5_1 {
		t.Fatalf("console should be a PS5: %+v", console)
	}
	if console.RegistKey != "0a1b2c3d" {
		t.Fatalf("regist key = %q, want 0a1b2c3d", console.RegistKey)
	}
	if console.RPKey != "AAECAwQFBgcICQoLDA0ODw==" {
		t.Fatalf("rp key = %q", console.RPKey)
	}
	if console.RPKeyType != 2 || console.ServerMAC != "aabbccddeeff" {
		t.Fatalf("key type/mac = %d/%q", console.RPKeyType, console.ServerMAC)
	}
	if console.Name != "Desk" || console.PairedAt.IsZero() {
		t.Fatalf("name/time not carried: %+v", console)
	}
	if !console.paired() {
		t.Fatal("parsed console should count as paired")
	}
}

func TestPS5ParseResultAcceptsRawKeyBytes(t *testing.T) {
	// A helper that prints the key bytes rather than the ASCII key: 4 bytes
	// 0x01 0x23 0x45 0x67 hex-encode to "01234567", which is still valid input.
	console, err := ps5ParseResult(map[string]string{
		"target":     "1000000",
		"regist_key": "01234567",
		"rp_key":     "0f0e0d0c0b0a09080706050403020100",
		"server_mac": "001122334455",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if console.Target != psTargetPS5 {
		t.Fatalf("target = %d, want the plain PS5 target", console.Target)
	}
	if !psRegistKeyValid(console.RegistKey) {
		t.Fatalf("regist key %q is not valid", console.RegistKey)
	}
}

func TestPS5ParseResultRejectsBadHelperOutput(t *testing.T) {
	base := map[string]string{
		"target":     "1000100",
		"regist_key": "3061316232633364",
		"rp_key":     "000102030405060708090a0b0c0d0e0f",
		"server_mac": "aabbccddeeff",
	}
	cases := map[string]map[string]string{
		"ps4 target":    {"target": "1000"},
		"empty key":     {"regist_key": ""},
		"not hex":       {"regist_key": "zz"},
		"short rp key":  {"rp_key": "0001"},
		"missing mac":   {"server_mac": ""},
		"bad key bytes": {"regist_key": "6e6f742d68657821"}, // "not-hex!"
	}
	for name, patch := range cases {
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range patch {
			values[k] = v
		}
		if _, err := ps5ParseResult(values); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestPS5RegisterNeedsAccountID(t *testing.T) {
	engine := psEngine{Regist: filepath.Join(t.TempDir(), "chiaki-regist")}
	if _, err := ps5Register(engine, "192.168.1.51", "12345678", "", time.Second); err == nil {
		t.Fatal("a PS5 pairing without a PSN Account-ID must fail")
	}
	if _, err := ps5Register(engine, "192.168.1.51", "12345678", "stub-rp-key-1", time.Second); err == nil {
		t.Fatal("the offline stub account must not be accepted for a PS5")
	}
}

func TestPS5RegisterValidatesInputFirst(t *testing.T) {
	engine := psEngine{Regist: filepath.Join(t.TempDir(), "chiaki-regist")}
	if _, err := ps5Register(engine, "", "12345678", testPSNAccountID, time.Second); err == nil {
		t.Fatal("an empty host must fail")
	}
	if _, err := ps5Register(engine, "192.168.1.51", "123", testPSNAccountID, time.Second); err == nil {
		t.Fatal("a short PIN must fail")
	}
	if _, err := ps5Register(engine, "192.168.1.51", "12345678", "not-base64", time.Second); err == nil {
		t.Fatal("a non-base64 Account-ID must fail")
	}
}

func TestPS5RegisterWithoutHelperExplainsWhy(t *testing.T) {
	engine := psEngine{Regist: filepath.Join(t.TempDir(), "chiaki-regist")}
	_, err := ps5Register(engine, "192.168.1.51", "12345678", testPSNAccountID, time.Second)
	if err == nil || !strings.Contains(err.Error(), "chiaki-regist") {
		t.Fatalf("want a missing-helper error, got %v", err)
	}
}

func TestPSHelperStageAndReason(t *testing.T) {
	output := "stage=start\nstage=network upstream=connect: connection refused\n"
	if got := psHelperStage(output); got != "network" {
		t.Fatalf("stage = %q, want network", got)
	}
	if got := psHelperReason("network", output); got != "connect: connection refused" {
		t.Fatalf("reason = %q", got)
	}
	if got := psHelperReason("protocol", "stage=protocol"); !strings.Contains(got, "PIN") {
		t.Fatalf("protocol reason should mention the PIN, got %q", got)
	}
	if got := psHelperReason("network", ""); got != "could not reach the console" {
		t.Fatalf("reason = %q", got)
	}
}
