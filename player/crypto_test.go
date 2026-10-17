package main

// Tests for config secret decryption. The regression that mattered: the default
// salt string was normalised to 32 bytes in one build and not in another, so
// values written with the same key ID could not be opened by the other build.

import (
	"os"
	"strings"
	"testing"
)

// legacyDefaultKey reproduces the derivation used before the default salt was
// normalised: the salt literal is fed to the KDF untrimmed.
func legacyDefaultKey() []byte {
	return deriveKeyFromSecret([]byte(defaultSecretText), []byte(defaultSaltText))
}

func requireBuiltInKey(t *testing.T) {
	t.Helper()
	if os.Getenv("JUKAHUB_CRYPTO_KEY") != "" || os.Getenv("JUKAHUB_CRYPTO_SALT") != "" {
		t.Skip("custom crypto key material in environment")
	}
}

func TestDecryptsValueFromCurrentKey(t *testing.T) {
	enc, err := EncryptString("current-format-token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptString(enc)
	if err != nil {
		t.Fatalf("current-format value failed to decrypt: %v", err)
	}
	if got != "current-format-token" {
		t.Fatalf("got %q", got)
	}
}

// A value written with the pre-normalisation salt must still decrypt: this is
// exactly what shipped configs contain, and failing here breaks Discord chat.
func TestDecryptsValueFromLegacyDefaultSalt(t *testing.T) {
	requireBuiltInKey(t)
	plain := "MTMxMjM0NTY3ODkwMTIzNDU2.Nm9vYmFy.abcdefghijklmnopqrstuvwxyz012345678"
	enc, err := EncryptStringWithKey(plain, legacyDefaultKey(), "default-v3-enhanced")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptString(enc)
	if err != nil {
		t.Fatalf("legacy-salt value failed to decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("legacy round trip returned %d chars, want %d", len(got), len(plain))
	}
}

func TestDecryptAPITokenNamesTheField(t *testing.T) {
	requireBuiltInKey(t)
	enc, err := EncryptStringWithKey("bot-secret", legacyDefaultKey(), "default-v3-enhanced")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptAPIToken(enc, "discord_token")
	if err != nil {
		t.Fatalf("DecryptAPIToken: %v", err)
	}
	if got != "bot-secret" {
		t.Fatalf("got %q", got)
	}
}

func TestDecryptRejectsWrongKeyWithHint(t *testing.T) {
	other := deriveKeyFromSecret([]byte("some-other-secret"), []byte("some-other-salt-value"))
	enc, err := EncryptStringWithKey("not-for-this-build", other, "env-other1234")
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecryptString(enc)
	if err == nil {
		t.Fatal("value encrypted with a foreign key decrypted")
	}
	if !strings.Contains(err.Error(), "env-other1234") {
		t.Fatalf("error does not name the key that encrypted the value: %v", err)
	}
	if !strings.Contains(err.Error(), currentKeyInfo.ID) {
		t.Fatalf("error does not name the running key: %v", err)
	}
}

// Tampered data must fail rather than return garbage; the error must not claim
// a key mismatch when the key ID matches.
func TestDecryptRejectsCorruptedData(t *testing.T) {
	enc, err := EncryptString("payload")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(enc, "ENC:")
	keyID, hexBody, _ := strings.Cut(body, "/")
	flipped := []byte(hexBody)
	if flipped[len(flipped)/2] == 'a' {
		flipped[len(flipped)/2] = 'b'
	} else {
		flipped[len(flipped)/2] = 'a'
	}
	_, err = DecryptString("ENC:" + keyID + "/" + string(flipped))
	if err == nil {
		t.Fatal("corrupted ciphertext decrypted")
	}
	if strings.Contains(err.Error(), "but this build uses") {
		t.Fatalf("corruption reported as a key mismatch: %v", err)
	}
}

func TestDecryptAPITokenErrorKinds(t *testing.T) {
	if _, err := DecryptAPIToken("", "k"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty value: got %v", err)
	}
	if _, err := DecryptAPIToken("plain-token", "k"); err == nil || !strings.Contains(err.Error(), "not encrypted") {
		t.Fatalf("plaintext value: got %v", err)
	}
}

// The config that ships with the app must be readable by the app. This is the
// exact failure that left Discord chat stuck on "token decryption failed".
func TestShippedConfigSecretsDecrypt(t *testing.T) {
	requireBuiltInKey(t)
	cfg, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Skipf("no config in the test working directory: %v", err)
	}
	tok := strings.TrimSpace(cfg.ChannelProfile.Token)
	if tok == "" {
		t.Skip("no discord token in the shipped config")
	}
	plain, err := DecryptAPIToken(tok, "discord_token")
	if err != nil {
		t.Fatalf("shipped discord token does not decrypt: %v", err)
	}
	if len(plain) < 50 || !strings.Contains(plain, ".") {
		t.Fatalf("decrypted value does not look like a Discord token (%d chars)", len(plain))
	}

	// discordCreds is the path the chat uses: it must hand back a token and no
	// error status.
	prevCfg, prevStatus := appConfig, discordStatus
	t.Cleanup(func() { appConfig, discordStatus = prevCfg, prevStatus })
	appConfig = cfg
	got, ch := discordCreds()
	if got == "" {
		t.Fatalf("discordCreds returned no token: %s", discordStatus)
	}
	if ch == "" {
		t.Fatal("discordCreds returned no channel")
	}
}

func TestDecryptWithAuthRoundTrip(t *testing.T) {
	enc, err := EncryptWithAuth("authenticated-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptWithAuth(enc)
	if err != nil {
		t.Fatalf("DecryptWithAuth: %v", err)
	}
	if got != "authenticated-secret" {
		t.Fatalf("got %q", got)
	}
	if _, err := DecryptWithAuth(enc[:len(enc)-2] + "00"); err == nil {
		t.Fatal("tampered authenticated value decrypted")
	}
}
