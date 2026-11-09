package main

// Tests for the Toolbox catalog, its download guards and the security posture
// summary. The catalog is a trust boundary, so the first test is the one that
// matters: every repo a tool can be fetched from has to be on the approved list
// in tools.go. Downloads are then matched against the published checksums.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolboxCatalogIsAllowlisted(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range toolboxCatalog() {
		if tool.ID == "" || tool.Name == "" || tool.Binary == "" || tool.Summary == "" {
			t.Errorf("incomplete catalog entry: %+v", tool)
			continue
		}
		if seen[tool.ID] {
			t.Errorf("duplicate toolbox id %q", tool.ID)
		}
		seen[tool.ID] = true
		if tool.Repo != strings.ToLower(tool.Repo) {
			t.Errorf("%s: repo %q must be lower-case (allowedToolRepos is keyed that way)", tool.ID, tool.Repo)
		}
		if !allowedToolRepos[tool.Repo] {
			t.Errorf("%s: repo %q is not in allowedToolRepos", tool.ID, tool.Repo)
		}
		if strings.ContainsAny(tool.Binary, `/\`) || strings.Contains(tool.Binary, "..") {
			t.Errorf("%s: binary %q must be a bare file name", tool.ID, tool.Binary)
		}
		if len(tool.Example) == 0 {
			t.Errorf("%s: needs an example argument line for the Run action", tool.ID)
		}
		for _, target := range []string{"linux/arm64", "windows/amd64"} {
			groups, ok := tool.Assets[target]
			if !ok || len(groups) == 0 {
				t.Errorf("%s: no asset patterns for %s", tool.ID, target)
				continue
			}
			for _, tokens := range groups {
				if len(tokens) == 0 {
					t.Errorf("%s/%s: empty token group", tool.ID, target)
				}
				for _, tok := range tokens {
					if tok != strings.ToLower(tok) || strings.TrimSpace(tok) != tok || tok == "" {
						t.Errorf("%s/%s: token %q must be lower-case and trimmed", tool.ID, target, tok)
					}
					// A version in a token would break on the next upstream
					// release, which is the whole reason matching is by token.
					if looksLikeVersionToken(tok) {
						t.Errorf("%s/%s: token %q looks like a version; match the stable parts instead", tool.ID, target, tok)
					}
				}
			}
		}
	}
	if len(seen) != len(toolboxCatalog()) {
		t.Fatalf("catalog has %d entries but %d unique ids", len(toolboxCatalog()), len(seen))
	}
}

// looksLikeVersionToken reports tokens of the form v1, 1.2 or 2024-01.
func looksLikeVersionToken(tok string) bool {
	t := strings.TrimPrefix(strings.ToLower(tok), "v")
	if t == "" {
		return false
	}
	digits := 0
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return digits > 0
}

func TestToolboxSkipsChecksumSidecars(t *testing.T) {
	assets := []releaseAsset{
		{Name: "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz.sha256", URL: "checksum"},
		{Name: "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz.sig", URL: "signature"},
		{Name: "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz", URL: "archive"},
	}
	picked, ok := pickToolAsset(assets, [][]string{{"aarch64", "linux", ".tar.gz"}})
	if !ok {
		t.Fatal("the archive should match")
	}
	if picked.URL != "archive" {
		t.Fatalf("picked %q: a checksum or signature file must never be the download", picked.URL)
	}
	for _, name := range []string{"x.sha256", "x.sig", "x.asc", "notes.txt", "release.json"} {
		if !isToolAssetSidecar(name) {
			t.Errorf("%q should be treated as a side-car", name)
		}
	}
	if isToolAssetSidecar("rclone-v1.70.1-linux-arm64.zip") {
		t.Error("an archive must not be a side-car")
	}
}

func TestToolboxInstallRejectsUnknownTool(t *testing.T) {
	if err := toolboxInstall("definitely-not-a-tool", nil); err == nil {
		t.Fatal("installing an id that is not in the catalog must fail")
	}
}

func TestSecurityPostureFlagsPlaintextSecrets(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{
		"groq_api_key": "sk-plain-text-key",
	}
	rows := securityPostureRows(cfg)
	if len(rows) < 6 {
		t.Fatalf("posture has only %d rows", len(rows))
	}
	secrets := findSecurityRow(t, rows, "Config secrets")
	if !secrets.Warn || !strings.Contains(secrets.Value, "1") {
		t.Errorf("a plaintext key must warn, got %+v", secrets)
	}

	cfg.Variables.Custom["groq_api_key"] = "ENC:default-v3-enhanced/00ff"
	secrets = findSecurityRow(t, securityPostureRows(cfg), "Config secrets")
	if secrets.Warn {
		t.Errorf("an encrypted value must not warn, got %+v", secrets)
	}

	// The rows name the mechanism, not just the state.
	for _, want := range []string{"Encryption key", "Tool downloads", "Packages & patches", "File servers", "Browser session", "Telemetry", "Threat model & details"} {
		findSecurityRow(t, securityPostureRows(cfg), want)
	}
}

func findSecurityRow(t *testing.T, rows []securityRow, label string) securityRow {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			if strings.TrimSpace(r.Value) == "" || strings.TrimSpace(r.Detail) == "" {
				t.Errorf("%s row is missing a value or detail: %+v", label, r)
			}
			return r
		}
	}
	t.Fatalf("no %q row in the posture summary", label)
	return securityRow{}
}

// TestPlaceholderSecretKeepsTheWarningAlive is the regression guard for a bug
// the posture page found: an empty string in the placeholder list matched every
// value, so SecurityCheck never reported a plaintext secret at all.
func TestPlaceholderSecretKeepsTheWarningAlive(t *testing.T) {
	for value, want := range map[string]bool{
		"":                  true,
		"   ":               true,
		"your-api-key":      true,
		"put-your-key-here": true,
		"sk-live-9f8a7b6c":  false,
		"gsk_real_token":    false,
	} {
		if got := isPlaceholderSecret(value); got != want {
			t.Errorf("isPlaceholderSecret(%q) = %v, want %v", value, got, want)
		}
	}
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"groq_api_key": "gsk_real_token"}
	warned := false
	for _, w := range (&ConfigValidator{}).SecurityCheck(cfg) {
		if strings.Contains(w, "plaintext") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("a real-looking key in the config must produce a plaintext warning")
	}
}

// The checksum file for a download is the per-file side-car when there is one,
// else the release's SHA-256 list — never a SHA-512 list.
func TestToolboxChecksumAssetPicksThePublishedList(t *testing.T) {
	archive := "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz"
	assets := []releaseAsset{
		{Name: archive, URL: "archive"},
		{Name: archive + ".sha256", URL: "side"},
		{Name: "naabu_2.6.1_checksums.txt", URL: "list"},
	}
	if sum, single, ok := toolboxChecksumAsset(assets, archive); !ok || !single || sum.URL != "side" {
		t.Fatalf("side-car = %+v single=%v ok=%v", sum, single, ok)
	}
	if sum, single, ok := toolboxChecksumAsset(assets, "naabu_2.6.1_linux_arm64.zip"); !ok || single || sum.URL != "list" {
		t.Fatalf("list = %+v single=%v ok=%v", sum, single, ok)
	}
	for _, name := range []string{"SHA256SUMS", "sha256sum.txt", "checksums.txt"} {
		if _, single, ok := toolboxChecksumAsset([]releaseAsset{{Name: "x.zip"}, {Name: name}}, "x.zip"); !ok || single {
			t.Errorf("%s was not taken as the checksum list", name)
		}
	}
	if _, _, ok := toolboxChecksumAsset([]releaseAsset{{Name: "x.zip"}, {Name: "x_checksums_sha512.txt"}}, "x.zip"); ok {
		t.Fatal("a SHA-512 list was taken for SHA-256")
	}
	if _, _, ok := toolboxChecksumAsset([]releaseAsset{{Name: "x.zip"}}, "x.zip"); ok {
		t.Fatal("a release without checksums produced one")
	}
}

func TestToolboxParseChecksum(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("B", 64)
	list := "-----BEGIN PGP SIGNED MESSAGE-----\n" +
		hashA + "  rclone-v1.70.1-linux-arm64.zip\r\n" +
		hashB + " *./rclone-v1.70.1-windows-amd64.zip\n" +
		"not-a-hash  other.zip\n"
	if got, ok := toolboxParseChecksum(list, "rclone-v1.70.1-linux-arm64.zip", false); !ok || got != hashA {
		t.Fatalf("linux sum = %q %v", got, ok)
	}
	if got, ok := toolboxParseChecksum(list, "RCLONE-v1.70.1-windows-amd64.zip", false); !ok || got != strings.ToLower(hashB) {
		t.Fatalf("binary-mode sum = %q %v, want it lower-cased", got, ok)
	}
	if _, ok := toolboxParseChecksum(list, "missing.zip", false); ok {
		t.Fatal("a file the list does not name was found")
	}
	// A side-car may hold the hash alone; a shared list may not.
	if got, ok := toolboxParseChecksum(hashA+"\n", "x.tar.gz", true); !ok || got != hashA {
		t.Fatalf("bare side-car = %q %v", got, ok)
	}
	if _, ok := toolboxParseChecksum(hashA+"\n", "x.tar.gz", false); ok {
		t.Fatal("a bare hash in a shared list was taken for this file")
	}
	if toolboxIsSHA256(strings.Repeat("g", 64)) || toolboxIsSHA256(hashA[:63]) || !toolboxIsSHA256(hashB) {
		t.Fatal("toolboxIsSHA256 misjudged a value")
	}
}

// A different published hash stops the install; a list that does not name the
// file installs unverified rather than failing.
func TestToolboxMatchChecksum(t *testing.T) {
	sum := strings.Repeat("c", 64)
	list := sum + "  tool.zip\n"
	if ok, err := toolboxMatchChecksum(strings.ToUpper(sum), list, "tool.zip", false); !ok || err != nil {
		t.Fatalf("matching sum = %v %v", ok, err)
	}
	if ok, err := toolboxMatchChecksum(strings.Repeat("d", 64), list, "tool.zip", false); ok || err == nil {
		t.Fatalf("a mismatch must be an error, got %v %v", ok, err)
	}
	if ok, err := toolboxMatchChecksum(sum, list, "other.zip", false); ok || err != nil {
		t.Fatalf("an unlisted file = %v %v, want unverified without an error", ok, err)
	}
}

// An update replaces the old binary without leaving the aside copy behind.
func TestToolboxPlaceReplaces(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := toolboxPlace(staged, target, true); err != nil {
		t.Fatalf("place: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("target = %q %v, want the new binary", data, err)
	}
	if _, err := os.Stat(target + ".old"); err == nil {
		t.Fatal("the old copy was left beside the new one")
	}
	if _, err := os.Stat(staged); err == nil {
		t.Fatal("the staged file was left behind")
	}
	first := filepath.Join(dir, "first")
	if err := os.WriteFile(first, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh-tool")
	if err := toolboxPlace(first, fresh, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if data, _ := os.ReadFile(fresh); string(data) != "v1" {
		t.Fatalf("first install = %q", data)
	}
}

// The Terminal opens with the installed path, quoted when it has spaces, and
// the typed or example arguments; nothing runs until Enter.
func TestToolboxTerminalCommand(t *testing.T) {
	tool, ok := toolboxFind("jq")
	if !ok {
		t.Fatal("jq missing")
	}
	if got := toolboxTerminalCommand(tool, "/mnt/SDCARD/Tools/jq", ""); got != "/mnt/SDCARD/Tools/jq --version" {
		t.Fatalf("default = %q", got)
	}
	if got := toolboxTerminalCommand(tool, `C:\Juka Hub\tools\jq.exe`, `.[0] "a b"`); got != `"C:\Juka Hub\tools\jq.exe" .[0] "a b"` {
		t.Fatalf("typed = %q", got)
	}
}
