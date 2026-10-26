package main

// Tests for the Toolbox catalog, its download guards and the security posture
// summary. The catalog is a trust boundary, so the first test is the one that
// matters: every repo a tool can be fetched from has to be on the approved list
// in tools.go, and the patterns have to describe real upstream assets.

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestToolboxAssetPatterns(t *testing.T) {
	tool, ok := toolboxFind("rclone")
	if !ok {
		t.Fatal("rclone missing from the catalog")
	}
	groups, err := toolboxAssetPatterns(tool, "linux", "arm64")
	if err != nil || len(groups) == 0 {
		t.Fatalf("linux/arm64 should be supported: groups=%v err=%v", groups, err)
	}
	if _, err := toolboxAssetPatterns(tool, "plan9", "mips"); err == nil {
		t.Fatal("an unsupported platform must be an error, not a download that cannot work")
	}
	if _, ok := toolboxFind("nope"); ok {
		t.Fatal("unknown id resolved")
	}
}

// TestToolboxPatternsMatchRealAssetNames pins the tokens against the shapes the
// upstreams actually publish, including the traps: a checksum side-car, an
// armv7 build that a loose token would accept, and an arm64-in-name zip.
func TestToolboxPatternsMatchRealAssetNames(t *testing.T) {
	cases := []struct {
		id     string
		goos   string
		goarch string
		want   string
		reject []string
	}{
		{"jq", "linux", "arm64", "jq-linux-arm64", []string{"jq-linux-amd64", "jq-1.8.0.tar.gz"}},
		{"jq", "windows", "amd64", "jq-windows-amd64.exe", []string{"jq-linux-arm64"}},
		{"rclone", "linux", "arm64", "rclone-v1.70.1-linux-arm64.zip", []string{"rclone-v1.70.1-linux-arm-v7.zip", "rclone-v1.70.1-osx-arm64.zip"}},
		{"rg", "linux", "arm64", "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz", []string{"ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz", "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz.sha256"}},
		{"fd", "windows", "amd64", "fd-v10.2.0-x86_64-pc-windows-msvc.zip", []string{"fd-v10.2.0-aarch64-unknown-linux-gnu.tar.gz"}},
		{"pandoc", "linux", "arm64", "pandoc-3.6.4-linux-arm64.tar.gz", []string{"pandoc-3.6.4-linux-amd64.tar.gz"}},
		{"age", "windows", "amd64", "age-v1.2.1-windows-amd64.zip", []string{"age-v1.2.1-linux-arm64.tar.gz"}},
		// The scanners publish a macOS build with "arm64" in its name and a
		// 32-bit linux_arm build beside linux_arm64, so both decoys are real.
		{"naabu", "linux", "arm64", "naabu_2.6.1_linux_arm64.zip", []string{"naabu_2.6.1_macOS_arm64.zip", "naabu_2.6.1_linux_amd64.zip"}},
		{"naabu", "windows", "amd64", "naabu_2.6.1_windows_amd64.zip", []string{"naabu_2.6.1_windows_386.zip", "naabu_2.6.1_linux_amd64.zip"}},
		{"httpx", "linux", "arm64", "httpx_1.12.0_linux_arm64.zip", []string{"httpx_1.12.0_linux_arm.zip", "httpx_1.12.0_macOS_arm64.zip"}},
		{"dnsx", "windows", "amd64", "dnsx_1.3.1_windows_amd64.zip", []string{"dnsx_1.3.1_windows_386.zip", "dnsx_1.3.1_linux_amd64.zip"}},
	}
	for _, tc := range cases {
		tool, ok := toolboxFind(tc.id)
		if !ok {
			t.Fatalf("%s missing from the catalog", tc.id)
		}
		groups, err := toolboxAssetPatterns(tool, tc.goos, tc.goarch)
		if err != nil {
			t.Errorf("%s/%s%s: %v", tc.id, tc.goos, tc.goarch, err)
			continue
		}
		// The desired asset has to win against every decoy in the release.
		assets := []releaseAsset{{Name: tc.reject[0], URL: "https://example.invalid/decoy"}}
		if len(tc.reject) > 1 {
			assets = append(assets, releaseAsset{Name: tc.reject[1], URL: "https://example.invalid/decoy2"})
		}
		assets = append(assets, releaseAsset{Name: tc.want, URL: "https://example.invalid/want"})
		picked, ok := pickToolAsset(assets, groups)
		if !ok {
			t.Errorf("%s/%s%s: no asset matched %q", tc.id, tc.goos, tc.goarch, tc.want)
			continue
		}
		if picked.URL != "https://example.invalid/want" {
			t.Errorf("%s/%s%s: picked %q, want %q", tc.id, tc.goos, tc.goarch, picked.Name, tc.want)
		}
	}
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

func TestToolboxArchived(t *testing.T) {
	for name, want := range map[string]bool{
		"rclone-v1.70.1-linux-arm64.zip":                  true,
		"ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz": true,
		"pandoc-3.6.4-linux-arm64.tar.xz":                 true,
		"jq-linux-arm64":                                  false,
		"jq-windows-amd64.exe":                            false,
	} {
		if got := toolboxArchived(name); got != want {
			t.Errorf("toolboxArchived(%q) = %v, want %v", name, got, want)
		}
	}
	if got := toolboxArchiveExt("ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz"); got != ".tar.gz" {
		t.Errorf("archive ext = %q", got)
	}
	if got := toolboxArchiveExt("jq-linux-arm64"); got != "" {
		t.Errorf("raw binary ext = %q, want empty", got)
	}
}

func TestSplitToolArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"--version", []string{"--version"}},
		{"  --max-depth  2  ", []string{"--max-depth", "2"}},
		{`--profile "My Game"`, []string{"--profile", "My Game"}},
		{`--name 'two words'`, []string{"--name", "two words"}},
		{`--path /a/b\ c`, []string{"--path", "/a/b c"}},
		{`--raw '\d+'`, []string{"--raw", `\d+`}},
		// No shell: these are literal characters, not operators.
		{"--pattern a;rm -rf /", []string{"--pattern", "a;rm", "-rf", "/"}},
		{"-e $HOME", []string{"-e", "$HOME"}},
		{`-e "*(a|b)*"`, []string{"-e", "*(a|b)*"}},
		// An unterminated quote still yields the text typed so far.
		{`--x "half`, []string{"--x", "half"}},
	}
	for _, tc := range cases {
		got := splitToolArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitToolArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitToolArgs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestToolboxCommandLine(t *testing.T) {
	tool, ok := toolboxFind("jq")
	if !ok {
		t.Fatal("jq missing")
	}
	line, args := toolboxCommandLine(tool, "")
	if line != "jq --version" || len(args) != 1 || args[0] != "--version" {
		t.Errorf("default command = %q %#v", line, args)
	}
	line, args = toolboxCommandLine(tool, `.[0].name "a b"`)
	if line != `jq .[0].name "a b"` {
		t.Errorf("typed command = %q", line)
	}
	if len(args) != 2 || args[1] != "a b" {
		t.Errorf("typed args = %#v", args)
	}
}

func TestToolboxInstallRejectsUnknownTool(t *testing.T) {
	if err := toolboxInstall("definitely-not-a-tool", nil); err == nil {
		t.Fatal("installing an id that is not in the catalog must fail")
	}
}

// TestToolboxInstalledUsesToolsDir covers the presence check and the row value
// without touching the real card: the override points at a temp folder.
func TestToolboxInstalledUsesToolsDir(t *testing.T) {
	dir := t.TempDir()
	old := toolboxToolsDirOverride
	toolboxToolsDirOverride = dir
	defer func() { toolboxToolsDirOverride = old }()

	tool, ok := toolboxFind("fd")
	if !ok {
		t.Fatal("fd missing")
	}
	if got := toolboxInstalled(tool); got != "" {
		t.Fatalf("an empty tools folder must read as missing, got %q", got)
	}
	if value, installed := toolboxRowValue(tool); installed || value != "Not installed" {
		t.Fatalf("row value = %q installed=%v", value, installed)
	}
	if _, err := toolboxRun("fd", "--version", 0); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("running a missing tool should say so, got %v", err)
	}

	// An empty placeholder (an interrupted install) is not "installed".
	target := filepath.Join(dir, toolboxExeName(tool, runtime.GOOS))
	if err := os.WriteFile(target, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := toolboxInstalled(tool); got != "" {
		t.Fatalf("a zero-byte file must read as missing, got %q", got)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := toolboxInstalled(tool); got != target {
		t.Fatalf("installed path = %q, want %q", got, target)
	}
	if value, installed := toolboxRowValue(tool); !installed || !strings.HasPrefix(value, "Installed") {
		t.Fatalf("row value = %q installed=%v", value, installed)
	}
	if n := toolboxInstalledCount(); n != 1 {
		t.Fatalf("installed count = %d, want 1", n)
	}
	if got := toolboxSummaryValue(); !strings.Contains(got, "1 of ") {
		t.Fatalf("summary = %q", got)
	}
}

// TestPowerToolboxRows drives the page the Tools panel opens: one row per
// catalog tool, Install while missing and Run once installed.
func TestPowerToolboxRows(t *testing.T) {
	dir := t.TempDir()
	old := toolboxToolsDirOverride
	toolboxToolsDirOverride = dir
	defer func() { toolboxToolsDirOverride = old }()

	withPowerToolsState(t, powerToolsState{page: powerPageToolbox})
	rows := powerToolRows(&Config{})
	if len(rows) != len(toolboxCatalog()) {
		t.Fatalf("toolbox rows = %d, want one per catalog entry (%d)", len(rows), len(toolboxCatalog()))
	}
	for _, row := range rows {
		if row.action != powerActionToolboxInstall || !row.enabled {
			t.Fatalf("missing row = %+v, want an install action", row)
		}
		if row.value != "Not installed" {
			t.Fatalf("row value = %q, want Not installed", row.value)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, toolboxExeNameOf("jq")), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	rows = powerToolRows(&Config{})
	installed := 0
	for _, row := range rows {
		if row.action == powerActionToolboxRun {
			installed++
			if !strings.Contains(row.value, "Installed") {
				t.Fatalf("installed row value = %q", row.value)
			}
			if !strings.Contains(row.hint, "X Arguments") {
				t.Fatalf("installed row hint = %q, want the argument editor", row.hint)
			}
		}
	}
	if installed != 1 {
		t.Fatalf("installed rows = %d, want 1", installed)
	}
}

// TestPowerSecurityPage renders the posture page from the live config: every
// row is informational, and a plaintext secret shows up as a warning value.
func TestPowerSecurityPage(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"groq_api_key": "gsk_real_token"}
	withPowerToolsState(t, powerToolsState{page: powerPageSecurity})
	rows := powerToolRows(cfg)
	if len(rows) != len(securityPostureRows(cfg)) {
		t.Fatalf("security rows = %d, want %d", len(rows), len(securityPostureRows(cfg)))
	}
	for _, row := range rows {
		if row.enabled || row.action != 0 {
			t.Fatalf("posture row should be informational only: %+v", row)
		}
		if row.label == "" || row.detail == "" {
			t.Fatalf("posture row is missing its label or detail: %+v", row)
		}
	}
	if rows[0].label != "Config secrets" || !strings.Contains(rows[0].value, "plaintext") || !strings.HasSuffix(rows[0].value, "·") {
		t.Fatalf("first posture row = %+v, want the plaintext warning marker", rows[0])
	}

	// The panel draws both new pages without pushing the footer off screen.
	renderPowerProbe(t, cfg, powerPageSecurity, 0)
	renderPowerProbe(t, cfg, powerPageToolbox, 0)
	if len(powerRowRects) == 0 {
		t.Fatal("the toolbox page drew no rows")
	}
}

// TestToolboxKeyboardTarget covers the argument editor: the keyboard writes to
// the toolbox buffer, and submitting saves the line for the next run.
func TestToolboxKeyboardTarget(t *testing.T) {
	dir := t.TempDir()
	old := toolboxToolsDirOverride
	toolboxToolsDirOverride = dir
	defer func() { toolboxToolsDirOverride = old }()
	t.Cleanup(func() {
		virtualKeyboardActive = false
		vkTarget = vkTargetScene
		toolArgsToolID = ""
		toolArgsBuffer = ""
	})

	vkOpenToolArgs("jq")
	if !virtualKeyboardActive || vkTarget != vkTargetToolArgs {
		t.Fatalf("keyboard target = %v active=%v", vkTarget, virtualKeyboardActive)
	}
	if toolArgsToolID != "jq" {
		t.Fatalf("tool id = %q", toolArgsToolID)
	}
	vkSetText(`--tab .`, &Config{})
	if toolArgsBuffer != `--tab .` {
		t.Fatalf("buffer = %q", toolArgsBuffer)
	}
	vkSubmit(&Config{}, false)
	if got := toolboxArgsFor("jq"); got != `--tab .` {
		t.Fatalf("saved args = %q", got)
	}
	if virtualKeyboardActive {
		t.Fatal("the keyboard must close on submit")
	}
}

func toolboxExeNameOf(id string) string {
	tool, ok := toolboxFind(id)
	if !ok {
		return id
	}
	return toolboxExeName(tool, runtime.GOOS)
}

func TestToolboxArgsAreTrimmedAndForgotten(t *testing.T) {
	toolboxSetArgs("jq", `--tab .`)
	if got := toolboxArgsFor("jq"); got != `--tab .` {
		t.Fatalf("args = %q", got)
	}
	toolboxSetArgs("jq", "   ")
	if got := toolboxArgsFor("jq"); got != "" {
		t.Fatalf("a blank line should fall back to the catalog example, got %q", got)
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

func TestSecurityPostureKeyRow(t *testing.T) {
	t.Setenv("JUKAHUB_CRYPTO_KEY", "")
	rows := securityPostureRows(&Config{})
	key := findSecurityRow(t, rows, "Encryption key")
	if !key.Warn {
		t.Error("the built-in key must be flagged")
	}
	if n := securityWarningCount(&Config{}); n == 0 {
		t.Error("the built-in key alone should count as a warning")
	}
	t.Setenv("JUKAHUB_CRYPTO_KEY", "0123456789abcdef0123456789abcdef")
	key = findSecurityRow(t, securityPostureRows(&Config{}), "Encryption key")
	if key.Warn {
		t.Error("an environment key must not be flagged")
	}
	if n := securityWarningCount(&Config{}); n != 0 {
		t.Errorf("a clean config with an environment key should have 0 warnings, got %d", n)
	}
}
