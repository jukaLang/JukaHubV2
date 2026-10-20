package releasesign

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

const testSHA = "48ed6fb51883a9aa82c3875a0e87744354bdfd4d8cc6de80004d28d3ded0fa24"

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func signed(t *testing.T, now time.Time) (*Manifest, ed25519.PublicKey) {
	t.Helper()
	pub, priv := testKey(t)
	m := &Manifest{
		Format:    Format,
		KeyID:     "test-key",
		Tag:       "JukaHub-20261020154603",
		IssuedAt:  now.Add(-time.Hour).Format(time.RFC3339),
		ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		Targets:   []Target{{Asset: "JukaHub-Windows.zip", SHA256: testSHA, Size: 29289140}},
	}
	if err := Sign(m, priv); err != nil {
		t.Fatal(err)
	}
	return m, pub
}

func TestVerifyAcceptsSignedManifest(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	m, pub := signed(t, now)
	if err := Verify(m, pub, "test-key", now); err != nil {
		t.Fatalf("a correctly signed manifest was rejected: %v", err)
	}
}

func TestVerifyRejectsTamperedOrStaleManifests(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(m *Manifest)
		key    func(pub ed25519.PublicKey) ed25519.PublicKey
		keyID  string
		now    time.Time
	}{
		{name: "swapped archive hash", mutate: func(m *Manifest) { m.Targets[0].SHA256 = strings.Repeat("a", 64) }},
		{name: "added second archive", mutate: func(m *Manifest) {
			m.Targets = append(m.Targets, Target{Asset: "JukaHub-Trimui.zip", SHA256: testSHA, Size: 1})
		}},
		{name: "retargeted tag", mutate: func(m *Manifest) { m.Tag = "JukaHub-20200101000000" }},
		{name: "stripped signature", mutate: func(m *Manifest) { m.Signature = "" }},
		{name: "malformed signature", mutate: func(m *Manifest) { m.Signature = "not-hex" }},
		{name: "unknown format", mutate: func(m *Manifest) { m.Format = 99 }},
		{name: "no archives", mutate: func(m *Manifest) { m.Targets = nil }},
		{name: "expired", now: now.Add(48 * time.Hour)},
		{name: "issued in the future", mutate: func(m *Manifest) {
			m.IssuedAt = now.Add(2 * time.Hour).Format(time.RFC3339)
		}, keyID: "test-key", key: func(pub ed25519.PublicKey) ed25519.PublicKey { return pub }},
		{name: "unknown key id", keyID: "someone-else"},
		{name: "signature by another key", key: func(pub ed25519.PublicKey) ed25519.PublicKey {
			other, _, err := ed25519.GenerateKey(nil)
			if err != nil {
				panic(err)
			}
			return other
		}, keyID: "test-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, pub := signed(t, now)
			checkNow := now
			keyID := "test-key"
			if tc.mutate != nil {
				tc.mutate(m)
			}
			if tc.now != (time.Time{}) {
				checkNow = tc.now
			}
			if tc.keyID != "" {
				keyID = tc.keyID
			}
			if tc.key != nil {
				pub = tc.key(pub)
			}
			if err := Verify(m, pub, keyID, checkNow); err == nil {
				t.Fatal("a manifest that must not verify was accepted")
			}
		})
	}
}

// TestCanonicalBytesAreStable pins the exact signed payload. The signing tool
// and the app must hash byte-identical JSON, so a field reorder or a tag change
// here would silently break every device's verification.
func TestCanonicalBytesAreStable(t *testing.T) {
	m := &Manifest{
		Format:    Format,
		KeyID:     "k",
		Tag:       "t",
		IssuedAt:  "2026-10-20T00:00:00Z",
		Targets:   []Target{{Asset: "a.zip", SHA256: testSHA, Size: 10}},
		Signature: "ff",
	}
	payload, err := Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":1,"key_id":"k","tag":"t","issued_at":"2026-10-20T00:00:00Z","targets":[{"asset":"a.zip","sha256":"` +
		testSHA + `","size":10}],"signature":""}`
	if string(payload) != want {
		t.Fatalf("canonical payload changed:\n got %s\nwant %s", payload, want)
	}
	// The signature is never part of its own payload.
	m.Signature = "aa"
	again, err := Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Fatal("canonical payload depends on the signature field")
	}
}

func TestFindTarget(t *testing.T) {
	m := &Manifest{Targets: []Target{
		{Asset: "JukaHub-Windows.zip", SHA256: strings.ToUpper(testSHA), Size: 10},
		{Asset: "JukaHub-Trimui.zip", SHA256: "short", Size: 10},
		{Asset: "zerosize.zip", SHA256: testSHA, Size: 0},
	}}
	got, err := FindTarget(m, "jukahub-windows.ZIP")
	if err != nil {
		t.Fatalf("FindTarget: %v", err)
	}
	if got.SHA256 != testSHA {
		t.Errorf("digest = %q, want the lower-case hash %q", got.SHA256, testSHA)
	}
	if got.Size != 10 {
		t.Errorf("size = %d, want 10", got.Size)
	}

	for _, asset := range []string{"JukaHub-Trimui.zip", "zerosize.zip", "missing.zip"} {
		if _, err := FindTarget(m, asset); err == nil {
			t.Errorf("%s: a manifest entry without a usable hash or size was accepted", asset)
		}
	}
}

func TestIsSHA256Hex(t *testing.T) {
	if !IsSHA256Hex(testSHA) || !IsSHA256Hex(strings.ToUpper(testSHA)) {
		t.Error("a valid SHA-256 was rejected")
	}
	for _, bad := range []string{"", testSHA[:63], testSHA + "0", strings.Repeat("z", 64), "sha256:" + testSHA} {
		if IsSHA256Hex(bad) {
			t.Errorf("%q was accepted as a SHA-256", bad)
		}
	}
}

func TestSignSetsVerifiableSignature(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	pub, priv := testKey(t)
	m := &Manifest{Format: Format, KeyID: "test-key", Tag: "t",
		Targets: []Target{{Asset: "a.zip", SHA256: testSHA, Size: 1}}}
	if err := Sign(m, priv); err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature = %q, %v", m.Signature, err)
	}
	if err := Verify(m, pub, "test-key", now); err != nil {
		t.Fatalf("Verify after Sign: %v", err)
	}
}
