package main

// toolbox.go — the curated tool catalog behind Tools → Toolbox.
//
// The launcher already ships three helper tools it needs itself (yt-dlp, ffmpeg,
// ffprobe). The toolbox is the other half: popular command-line tools
// the user may want on the card, installed on demand and run from the app.
//
// Trust boundary, deliberately small:
//   - a tool can only be installed if `toolboxCatalog` names it; there is no
//     free-text repo or URL anywhere in the UI (a package or a pasted link can
//     put a binary in the tools folder, but not make the toolbox fetch one);
//   - the repo must also be in `allowedToolRepos` (tools.go), so adding a tool
//     means editing and reviewing the same list as the bundled helpers;
//   - assets are matched on the stable parts of the upstream name, never on a
//     version, and the download goes through the same size-capped streamer as
//     every other download;
//   - a run executes the installed binary directly with an argument vector —
//     no shell, so `;`, `|`, `&&` and `$()` are just characters.
//
// SECURITY.md, section "Toolbox and helper downloads", records the rest: what
// this does not verify (upstream hashes), and why.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// toolboxTool is one catalog entry.
type toolboxTool struct {
	// ID is the stable key used in rows, logs and the installed file name.
	ID string
	// Name is what the UI shows.
	Name string
	// Summary is one line about what the tool is good for on the device.
	Summary string
	// Repo is the GitHub repo releases are read from. It must be listed in
	// allowedToolRepos.
	Repo string
	// Binary is the file name inside the archive, without the .exe suffix.
	Binary string
	// Licence is informational: the tools stay under their own licences.
	Licence string
	// Example is the argument list the Run row uses when the user has not
	// typed any arguments yet.
	Example []string
	// Assets maps "goos/goarch" to candidate token groups, tried in order.
	// Each group must match every token, case-insensitively, against the asset
	// name (see assetNameMatches). Version numbers are never part of a token.
	Assets map[string][][]string
	// NetworkScan marks a tool whose job is to send traffic to other hosts.
	// Those runs sit behind the one-time authorized-use acknowledgement
	// (toolboxAuthorizedUseGate), because a scanner pointed at a network the
	// user does not own is the one thing in this catalog that can affect
	// somebody else.
	NetworkScan bool
}

// toolboxPlatformKey is the Assets key for a build target.
func toolboxPlatformKey(goos, goarch string) string {
	return goos + "/" + goarch
}

// toolboxCatalog is the whole catalog: nine tools, each with official release
// binaries for the two targets JukaHub actually ships on. Six work on the card
// itself; the three at the end scan the local network and are marked
// NetworkScan.
func toolboxCatalog() []toolboxTool {
	// Windows assets publish the binary with the extension already in the name;
	// the token groups still match without it, so the .exe suffix is only used
	// when the file is placed on disk.
	return []toolboxTool{
		{
			ID:      "jq",
			Name:    "jq",
			Summary: "Filter and reshape JSON from a script or a snippet",
			Repo:    "jqlang/jq",
			Binary:  "jq",
			Licence: "MIT",
			Example: []string{"--version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"jq-linux-arm64"}},
				"linux/amd64":   {{"jq-linux-amd64"}},
				"windows/amd64": {{"jq-windows-amd64.exe"}},
			},
		},
		{
			ID:      "rclone",
			Name:    "rclone",
			Summary: "Sync saves and folders to a cloud or SSH host",
			Repo:    "rclone/rclone",
			Binary:  "rclone",
			Licence: "MIT",
			Example: []string{"version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"linux-arm64", ".zip"}},
				"linux/amd64":   {{"linux-amd64", ".zip"}},
				"windows/amd64": {{"windows-amd64", ".zip"}},
			},
		},
		{
			ID:      "rg",
			Name:    "ripgrep (rg)",
			Summary: "Search ROM folders and configs faster than a file manager",
			Repo:    "burntsushi/ripgrep",
			Binary:  "rg",
			Licence: "MIT / Unlicense",
			Example: []string{"--version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"aarch64", "linux-gnu", ".tar.gz"}},
				"linux/amd64":   {{"x86_64", "linux-musl", ".tar.gz"}},
				"windows/amd64": {{"x86_64", "windows-msvc", ".zip"}},
			},
		},
		{
			ID:      "fd",
			Name:    "fd",
			Summary: "Find files by name without remembering find syntax",
			Repo:    "sharkdp/fd",
			Binary:  "fd",
			Licence: "MIT / Apache-2.0",
			Example: []string{"--version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"aarch64", "linux-gnu", ".tar.gz"}},
				"linux/amd64":   {{"x86_64", "linux-musl", ".tar.gz"}},
				"windows/amd64": {{"x86_64", "windows-msvc", ".zip"}},
			},
		},
		{
			ID:      "pandoc",
			Name:    "pandoc",
			Summary: "Convert documents between Markdown, HTML, EPUB and PDF",
			Repo:    "jgm/pandoc",
			Binary:  "pandoc",
			Licence: "GPL-2.0",
			Example: []string{"--version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"linux-arm64", ".tar.gz"}},
				"linux/amd64":   {{"linux-amd64", ".tar.gz"}},
				"windows/amd64": {{"windows-x86_64", ".zip"}},
			},
		},
		{
			ID:      "age",
			Name:    "age",
			Summary: "Encrypt a save or a note with a passphrase or a key",
			Repo:    "filosottile/age",
			Binary:  "age",
			Licence: "BSD-3-Clause",
			Example: []string{"--version"},
			Assets: map[string][][]string{
				"linux/arm64":   {{"linux-arm64", ".tar.gz"}},
				"linux/amd64":   {{"linux-amd64", ".tar.gz"}},
				"windows/amd64": {{"windows-amd64", ".zip"}},
			},
		},
		{
			ID:      "naabu",
			Name:    "naabu",
			Summary: "Scan a host or a LAN range for open TCP ports",
			Repo:    "projectdiscovery/naabu",
			Binary:  "naabu",
			Licence: "MIT",
			// No arguments prints help; -version is a deterministic first run.
			// Real scans are typed in the X argument editor, e.g.
			// -host 192.168.1.10 -top-ports 100
			Example:     []string{"-version"},
			NetworkScan: true,
			Assets: map[string][][]string{
				"linux/arm64":   {{"linux", "arm64", ".zip"}},
				"linux/amd64":   {{"linux", "amd64", ".zip"}},
				"windows/amd64": {{"windows", "amd64", ".zip"}},
			},
		},
		{
			ID:      "httpx",
			Name:    "httpx (probe)",
			Summary: "Check which hosts answer HTTP — status, title, server",
			Repo:    "projectdiscovery/httpx",
			Binary:  "httpx",
			Licence: "MIT",
			// With no arguments httpx reads URLs from stdin and waits, so the
			// default run must say something and exit. Real use, from the X
			// editor: -u 192.168.1.10 -title -status-code
			Example:     []string{"-version"},
			NetworkScan: true,
			Assets: map[string][][]string{
				// "arm64" whole, not "arm": the same release also ships a
				// 32-bit linux_arm build.
				"linux/arm64":   {{"linux", "arm64", ".zip"}},
				"linux/amd64":   {{"linux", "amd64", ".zip"}},
				"windows/amd64": {{"windows", "amd64", ".zip"}},
			},
		},
		{
			ID:      "dnsx",
			Name:    "dnsx",
			Summary: "Resolve names and walk DNS records for a host",
			Repo:    "projectdiscovery/dnsx",
			Binary:  "dnsx",
			Licence: "MIT",
			// Like httpx, no arguments means "read from stdin". Real use:
			// -d example.com -a -resp
			Example:     []string{"-version"},
			NetworkScan: true,
			Assets: map[string][][]string{
				"linux/arm64":   {{"linux", "arm64", ".zip"}},
				"linux/amd64":   {{"linux", "amd64", ".zip"}},
				"windows/amd64": {{"windows", "amd64", ".zip"}},
			},
		},
	}
}

// toolboxRowDetail is the second line of a Toolbox row. A scanning tool says so
// before it is ever run, so the warning is on the row rather than only in a
// toast after a press.
func toolboxRowDetail(t toolboxTool) string {
	if t.NetworkScan {
		return t.Summary + " · scans other hosts"
	}
	return t.Summary
}

// toolboxFind returns the catalog entry with the given ID.
func toolboxFind(id string) (toolboxTool, bool) {
	for _, t := range toolboxCatalog() {
		if t.ID == id {
			return t, true
		}
	}
	return toolboxTool{}, false
}

// toolboxExeName is the installed file name for a build target.
func toolboxExeName(t toolboxTool, goos string) string {
	if goos == "windows" {
		return t.Binary + ".exe"
	}
	return t.Binary
}

// toolboxAssetPatterns returns the candidate token groups for a build target.
// An unsupported target is an error the row can show instead of a download that
// can never succeed.
func toolboxAssetPatterns(t toolboxTool, goos, goarch string) ([][]string, error) {
	groups, ok := t.Assets[toolboxPlatformKey(goos, goarch)]
	if !ok || len(groups) == 0 {
		return nil, fmt.Errorf("%s has no release for %s/%s", t.Name, goos, goarch)
	}
	return groups, nil
}

// toolboxArchived reports whether an asset name is a zip or tarball. A raw
// binary (jq publishes one) is placed as-is instead of being unpacked.
func toolboxArchived(assetName string) bool {
	lower := strings.ToLower(assetName)
	for _, ext := range []string{".zip", ".tar.gz", ".tar.xz", ".tgz"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// toolboxArchiveExt is the extension the staged download must keep: extraction
// picks zip vs tar from the file name.
func toolboxArchiveExt(assetName string) string {
	lower := strings.ToLower(assetName)
	for _, ext := range []string{".tar.gz", ".tar.xz", ".tgz", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			return ext
		}
	}
	return ""
}

// toolboxToolsDir is where catalog tools are installed. The override exists so
// tests can point the toolbox at a temp folder instead of the developer's card;
// nothing in the app sets it.
var toolboxToolsDirOverride string

func toolboxToolsDir() string {
	if dir := strings.TrimSpace(toolboxToolsDirOverride); dir != "" {
		return dir
	}
	return P().ToolsDir()
}

// toolboxPath is where a tool lives (or would live) on this machine.
func toolboxPath(t toolboxTool) string {
	dir := toolboxToolsDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, toolboxExeName(t, runtime.GOOS))
}

// toolboxInstalled reports the installed path for a tool, or "" when missing.
// Only presence and size are inspected: the toolbox never runs a binary to
// decide whether it is installed.
func toolboxInstalled(t toolboxTool) string {
	p := toolboxPath(t)
	if p == "" {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return p
}

// toolboxInstall downloads and unpacks one catalog tool into the tools folder.
// It is idempotent: an installed copy is left alone so a running binary is
// never overwritten (ETXTBSY on Linux).
func toolboxInstall(id string, config *Config) error {
	t, ok := toolboxFind(id)
	if !ok {
		return fmt.Errorf("unknown tool %q", id)
	}
	if toolboxInstalled(t) != "" {
		return nil
	}
	dir := toolboxToolsDir()
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("tools directory unavailable (empty path)")
	}
	if err := os.MkdirAll(dir, PermDir); err != nil {
		return fmt.Errorf("cannot create tools directory %s: %w", dir, err)
	}
	groups, err := toolboxAssetPatterns(t, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !IsOnline() {
		return errToolsOffline
	}

	installBarBegin(t.Name)
	installBarPhase(installPhaseResolving)
	asset, err := resolveToolURLMatching(t.Repo, "", groups...)
	if err != nil {
		return fmt.Errorf("find %s release: %w", t.Name, err)
	}
	logPatch("[TOOLS] toolbox %s -> %s", t.ID, asset.URL)

	// Stage inside the tools directory: on the handheld /tmp is a small tmpfs
	// on another filesystem, and every archive here expands to megabytes.
	tmp, err := os.CreateTemp(dir, ".toolbox-*"+toolboxArchiveExt(asset.Name))
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	installBarPhase(installPhaseDownload)
	// The transfer runs on the card's run token: the strip's X on this chip
	// stops the download instead of only hiding the bar.
	if err := downloadFileWithProgressCtx(installBarRunContext(), asset.URL, tmpName, installBarProgress); err != nil {
		return fmt.Errorf("download %s: %w", t.Name, err)
	}

	target := toolboxPath(t)
	if target == "" {
		return fmt.Errorf("tools directory unavailable (empty path)")
	}
	installBarPhase(installPhaseExtract)
	if toolboxArchived(asset.Name) {
		found, err := extractToolFiles(tmpName, dir, []string{filepath.Base(target)})
		if err != nil {
			return fmt.Errorf("unpack %s: %w", t.Name, err)
		}
		if len(found) == 0 {
			return fmt.Errorf("%s archive did not contain %s", t.Name, filepath.Base(target))
		}
	} else {
		if err := os.Rename(tmpName, target); err != nil {
			if err := copyFileStream(tmpName, target, PermExec); err != nil {
				return fmt.Errorf("install %s: %w", t.Name, err)
			}
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, PermExec); err != nil {
			logPatch("[TOOLS] toolbox %s: chmod failed: %v", t.ID, err)
		}
	}
	if toolboxInstalled(t) == "" {
		return fmt.Errorf("%s reported success but %s is missing", t.Name, filepath.Base(target))
	}
	installBarDone()
	logPatch("[TOOLS] toolbox %s installed at %s", t.ID, target)
	return nil
}

// splitToolArgs splits a typed argument line into an argv without a shell:
// whitespace separates, single and double quotes group, and a backslash escapes
// the next character outside single quotes. Nothing is expanded, so `$HOME`,
// `*` and `;` reach the tool as ordinary text.
func splitToolArgs(line string) []string {
	var out []string
	var cur strings.Builder
	started := false
	quote := rune(0)
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			started = true
		case r == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

// toolboxCommandLine renders the argv the Run action will execute, for the
// output title and for the Copy row. It is quoted for display only; execution
// never goes through a shell.
func toolboxCommandLine(t toolboxTool, argLine string) (string, []string) {
	args := splitToolArgs(argLine)
	if len(args) == 0 {
		args = append(args, t.Example...)
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, t.Binary)
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " "), args
}

// toolboxRun executes an installed tool with the typed arguments and returns
// its combined output. The binary is run directly (no shell) with the helper
// environment, and the caller's timeout bounds the run.
func toolboxRun(id, argLine string, timeout time.Duration) (string, error) {
	t, ok := toolboxFind(id)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", id)
	}
	path := toolboxInstalled(t)
	if path == "" {
		return "", fmt.Errorf("%s is not installed yet", t.Name)
	}
	if timeout <= 0 {
		timeout = powerTimeoutScript
	}
	_, args := toolboxCommandLine(t, argLine)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = helperToolEnv(path)
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + fmt.Sprintf("\n(stopped after %s)", timeout), ctx.Err()
	}
	if err != nil && strings.TrimSpace(text) == "" {
		return "", err
	}
	return text, err
}

// toolboxArgLines remembers the arguments typed for each tool for the rest of
// the session. It is not persisted: an argument line can hold a path or a
// passphrase, and a per-run default is friendlier than a stale one from a
// previous boot. An empty line means "use the catalog example".
var (
	toolboxArgsMu   sync.Mutex
	toolboxArgLines = map[string]string{}
)

func toolboxArgsFor(id string) string {
	toolboxArgsMu.Lock()
	defer toolboxArgsMu.Unlock()
	return toolboxArgLines[id]
}

func toolboxSetArgs(id, line string) {
	toolboxArgsMu.Lock()
	defer toolboxArgsMu.Unlock()
	if strings.TrimSpace(line) == "" {
		delete(toolboxArgLines, id)
		return
	}
	toolboxArgLines[id] = line
}

// toolboxRowValue is the one-line state shown on a Toolbox row.
func toolboxRowValue(t toolboxTool) (string, bool) {
	path := toolboxInstalled(t)
	if path == "" {
		return "Not installed", false
	}
	if info, err := os.Stat(path); err == nil {
		return "Installed · " + powerHumanSize(info.Size()), true
	}
	return "Installed", true
}

// toolboxInstalledCount is how many catalog tools are on the card.
func toolboxInstalledCount() int {
	n := 0
	for _, t := range toolboxCatalog() {
		if toolboxInstalled(t) != "" {
			n++
		}
	}
	return n
}

// toolboxSummaryValue is the Tools row value: "3 of 6 installed".
func toolboxSummaryValue() string {
	n := toolboxInstalledCount()
	if n == 0 {
		return fmt.Sprintf("None installed · %d available", len(toolboxCatalog()))
	}
	return fmt.Sprintf("%d of %d installed", n, len(toolboxCatalog()))
}

// securityPostureValue is the Tools row value for the posture page, plus the
// flag that turns the row's dot amber.
func securityPostureValue(config *Config) (string, bool) {
	n := securityWarningCount(config)
	switch n {
	case 0:
		return "No warnings", false
	case 1:
		return "1 thing to review", true
	}
	return fmt.Sprintf("%d things to review", n), true
}

// ── Security posture ───────────────────────────────────────────────────────
//
// The Tools → Security page answers "how is this device doing?" with the facts
// the app already knows. It is a summary, not a scanner: every row names the
// mechanism behind it, and SECURITY.md is the long form.

// securityRow is one posture line: a label, the state, and why it matters.
type securityRow struct {
	Label  string
	Value  string
	Detail string
	// Warn marks a state the user can improve (a plaintext secret, the built-in
	// key, a server that is listening).
	Warn bool
}

// securityPostureRows builds the posture summary from the live config and the
// process state. Pure reads: no network, no file writes.
func securityPostureRows(config *Config) []securityRow {
	rows := make([]securityRow, 0, 8)

	// 1. Secrets in the config. SecurityCheck also reports the storage key and
	// the Discord token type; only the plaintext findings belong on this row, or
	// "1 stored in plaintext" would appear for a config with no secrets at all.
	warnings := (&ConfigValidator{}).SecurityCheck(config)
	plaintext := 0
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "plaintext") {
			plaintext++
		}
	}
	secrets := securityRow{
		Label:  "Config secrets",
		Value:  "No plaintext secret",
		Detail: "API keys and tokens are encrypted with AES-256-GCM",
	}
	if n := plaintext; n == 1 {
		secrets.Value = "1 stored in plaintext"
		secrets.Detail = "Set it again with -set-secret, or run the secret CLI"
		secrets.Warn = true
	} else if n > 1 {
		secrets.Value = fmt.Sprintf("%d stored in plaintext", n)
		secrets.Detail = "Each one is readable by anything that reads the config"
		secrets.Warn = true
	}
	rows = append(rows, secrets)

	// 2. Which key encrypts them.
	key := securityRow{
		Label:  "Encryption key",
		Value:  "Built-in default",
		Detail: "Anyone with the binary can derive it; set JUKAHUB_CRYPTO_KEY",
		Warn:   true,
	}
	if os.Getenv("JUKAHUB_CRYPTO_KEY") != "" {
		key.Value = "JUKAHUB_CRYPTO_KEY"
		key.Detail = "Derived with HMAC-SHA256, never written to the config"
		key.Warn = false
	}
	rows = append(rows, key)

	// 3. Where downloads may come from.
	rows = append(rows, securityRow{
		Label:  "Tool downloads",
		Value:  fmt.Sprintf("%d approved repos", len(allowedToolRepos)),
		Detail: "Curated GitHub repos only — no free-text URL, no user-supplied repo",
	})

	// 4. Packages and patches: they install signed, code-reviewed files.
	packages := "No package index"
	if repo, err := LoadPackageRepo(); err == nil {
		packages = fmt.Sprintf("%d package(s) available", len(repo.Packages))
	}
	rows = append(rows, securityRow{
		Label:  "Packages & patches",
		Value:  packages,
		Detail: "Digest-checked, signature-checked, rollback copy kept",
	})

	// 5. Listening servers are capabilities, not vulnerabilities — say so.
	servers := "None listening"
	warn := false
	if ftpSrv.Running() || sshSrv.Running() {
		names := make([]string, 0, 2)
		if ftpSrv.Running() {
			names = append(names, fmt.Sprintf("FTP :%d", ftpSrv.Port()))
		}
		if sshSrv.Running() {
			names = append(names, fmt.Sprintf("SFTP :%d", sshSrv.Port()))
		}
		servers = strings.Join(names, " · ")
		warn = true
	}
	rows = append(rows, securityRow{
		Label:  "File servers",
		Value:  servers,
		Detail: "Reachable by any device on this network while running",
		Warn:   warn,
	})

	// 6. The browser's own data.
	browser := securityRow{
		Label:  "Browser session",
		Value:  "No saved session",
		Detail: "The cookie jar lives in the app's data folder",
	}
	if path := browserCookiesPath(); path != "" {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			browser.Value = powerHumanSize(info.Size()) + " on the card"
			browser.Detail = "Browser → Forget empties it and deletes the file"
		}
	}
	rows = append(rows, browser)

	// 7. Telemetry is off unless asked for.
	rows = append(rows, securityRow{
		Label:  "Telemetry",
		Value:  telemetryStatusValue(),
		Detail: "Off by default; a single boot/crash ping when enabled",
	})

	// 8. Where the long form is.
	rows = append(rows, securityRow{
		Label:  "Threat model & details",
		Value:  "SECURITY.md",
		Detail: "What is protected, what is not, and how to report a problem",
	})
	return rows
}

// securityWarningCount is the number of posture rows the user can improve; it
// drives the amber dot on the Tools row.
func securityWarningCount(config *Config) int {
	n := 0
	for _, row := range securityPostureRows(config) {
		if row.Warn {
			n++
		}
	}
	return n
}
