package main

// Tests for the config secret CLI (-set-secret / -check-secrets). They cover
// the round trip that matters: a value written by the tool must be readable by
// the running player, and unrelated config keys must survive the write.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretTestConfig = `{
  "AppName": "JukaHub",
  "channel_profile": { "channel_id": "975787212954275916", "token": "" },
  "variables": { "theme": "dark", "groq_api_key": "" },
  "scenes": [ { "name": "Main", "elements": [] } ]
}`

func writeSecretTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	if err := os.WriteFile(path, []byte(secretTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetConfigSecretRoundTrip(t *testing.T) {
	path := writeSecretTestConfig(t)
	const token = "MTMxMjM0NTY3ODkwMTIzNDU2.Nm9vYmFy.abcdefghijklmnopqrstuvwxyz012345678"
	if err := setConfigSecret(path, "discord_token", token); err != nil {
		t.Fatalf("setConfigSecret: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("stored config is not loadable: %v", err)
	}
	stored := cfg.ChannelProfile.Token
	if !strings.HasPrefix(stored, "ENC:") {
		t.Fatalf("token stored unencrypted: %q", stored)
	}
	got, err := DecryptAPIToken(stored, "discord_token")
	if err != nil {
		t.Fatalf("token written by the tool does not decrypt: %v", err)
	}
	if got != token {
		t.Fatalf("round trip changed the token (%d -> %d chars)", len(token), len(got))
	}
	// Unrelated keys must survive the rewrite.
	if cfg.AppName != "JukaHub" || len(cfg.Scenes) != 1 {
		t.Fatalf("config structure lost: app=%q scenes=%d", cfg.AppName, len(cfg.Scenes))
	}
	if v, _ := cfg.Variables.Custom["theme"].(string); v != "dark" {
		t.Fatalf("variable lost: %q", v)
	}
	if cfg.ChannelProfile.ChannelID != "975787212954275916" {
		t.Fatalf("channel id changed: %q", cfg.ChannelProfile.ChannelID)
	}
}

func TestSetConfigSecretKeepsChannelIDPlain(t *testing.T) {
	path := writeSecretTestConfig(t)
	if err := setConfigSecret(path, "discord_channel", "123456789"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChannelProfile.ChannelID != "123456789" {
		t.Fatalf("channel id = %q", cfg.ChannelProfile.ChannelID)
	}
}

func TestCheckConfigSecretsReportsFailures(t *testing.T) {
	path := writeSecretTestConfig(t)
	if err := setConfigSecret(path, "discord_token", "good-token"); err != nil {
		t.Fatal(err)
	}
	checks, err := checkConfigSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !checks[0].OK {
		t.Fatalf("healthy config reported as broken: %+v", checks)
	}

	// Replace the token with a value that cannot be authenticated.
	foreign := encodeWithKeyForTest(t, "nope", deriveKeyFromSecret([]byte("x"), []byte("y")), "env-deadbeef")
	if err := setRawSecretForTest(path, foreign); err != nil {
		t.Fatal(err)
	}
	checks, err = checkConfigSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].OK {
		t.Fatalf("broken value reported as healthy: %+v", checks)
	}
	if checks[0].Reason == "" {
		t.Fatal("failure has no reason")
	}
}

// Writing a token must not reformat the rest of the config: the file is a
// hand-maintained catalog of 30+ scenes.
func TestSetConfigSecretPreservesFormatting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	src := "{\n    \"AppName\":   \"JukaHub\",\n    \"channel_profile\": {\n        \"channel_id\": \"1\",\n        \"token\": \"\"\n    },\n    \"variables\": { \"theme\":   \"dark\" }\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setConfigSecret(path, "discord_token", "abc"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, keep := range []string{`"AppName":   "JukaHub"`, `"channel_id": "1"`, `"variables": { "theme":   "dark" }`} {
		if !strings.Contains(text, keep) {
			t.Fatalf("formatting changed, missing %s in:\n%s", keep, text)
		}
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("rewritten config is invalid: %v", err)
	}
	if _, err := DecryptAPIToken(cfg.ChannelProfile.Token, "discord_token"); err != nil {
		t.Fatalf("token does not decrypt: %v", err)
	}
}

func TestSetConfigSecretAddsMissingField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	src := "{\n  \"AppName\": \"JukaHub\",\n  \"channel_profile\": {\n    \"channel_id\": \"1\"\n  }\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setConfigSecret(path, "discord_token", "abc"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("config with inserted field is invalid: %v", err)
	}
	if cfg.ChannelProfile.Token == "" {
		t.Fatal("token field was not added")
	}
	if _, err := DecryptAPIToken(cfg.ChannelProfile.Token, "discord_token"); err != nil {
		t.Fatalf("token does not decrypt: %v", err)
	}
}

func TestReadSecretTrimsLineEndings(t *testing.T) {
	got, err := readSecret(strings.NewReader("token-value\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "token-value" {
		t.Fatalf("got %q", got)
	}
}

// --- helpers ----------------------------------------------------------------

func encodeWithKeyForTest(t *testing.T, plain string, key []byte, keyID string) string {
	t.Helper()
	enc, err := EncryptStringWithKey(plain, key, keyID)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func setRawSecretForTest(path, enc string) error {
	doc, perm, err := readConfigDocument(path)
	if err != nil {
		return err
	}
	profile := doc["channel_profile"].(map[string]interface{})
	profile["token"] = enc
	return writeConfigDocument(path, doc, perm)
}
