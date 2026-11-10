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
//   - when the release publishes a SHA-256 for the file (a `.sha256` side-car
//     or a checksums list), the download must match it or nothing is
//     installed; a release without one is installed over TLS alone and the
//     tool says so;
//   - a run executes the installed binary directly with an argument vector —
//     no shell, so `;`, `|`, `&&` and `$()` are just characters.
//
// SECURITY.md, section "Toolbox and helper-tool downloads", records the rest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
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
	// Those runs sit behind the one-time authorized-use acknowledgement,
	// because a scanner pointed at a network the user does not own is the one
	// thing in this catalog that can affect somebody else.
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
			// Real scans are typed in the argument editor, e.g.
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
			// default run must say something and exit. Real use, from the
			// argument editor: -u 192.168.1.10 -title -status-code
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

// ── Releases and checksums ───────────────────────────────────────────────

// toolboxRelease is one GitHub release: its tag and its files.
type toolboxRelease struct {
	Tag    string
	Assets []releaseAsset
}

// errToolboxRateLimited is GitHub refusing anonymous API calls for a while.
var errToolboxRateLimited = errors.New("GitHub is rate-limiting update checks; try again later")

// toolboxReleaseFetch is a test seam over toolboxFetchLatestRelease.
var toolboxReleaseFetch = toolboxFetchLatestRelease

// toolboxFetchLatestRelease reads a trusted repo's latest release: the tag (so
// an installed tool can say which version it is and whether a newer one
// exists) and every file, checksum lists included. The repo must be on the
// approved list and every file must come from github.com.
func toolboxFetchLatestRelease(ctx context.Context, repo string) (toolboxRelease, error) {
	if !allowedToolRepos[strings.ToLower(repo)] {
		return toolboxRelease{}, fmt.Errorf("tool repo %q is not in the approved download list", repo)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return toolboxRelease{}, err
	}
	req.Header.Set("User-Agent", "JukaHub-Toolbox/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return toolboxRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return toolboxRelease{}, errToolboxRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return toolboxRelease{}, fmt.Errorf("github api %s: unexpected status %d", repo, resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return toolboxRelease{}, err
	}
	out := toolboxRelease{Tag: strings.TrimSpace(rel.TagName)}
	for _, a := range rel.Assets {
		if !strings.HasPrefix(strings.ToLower(a.BrowserDownloadURL), "https://github.com/") {
			continue
		}
		out.Assets = append(out.Assets, releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	if len(out.Assets) == 0 {
		return toolboxRelease{}, fmt.Errorf("no assets in %s latest release", repo)
	}
	return out, nil
}

// toolboxChecksumAsset finds the file in a release that publishes the SHA-256 of
// asset: a per-file side-car (ripgrep's "<archive>.sha256") first, then a
// combined list (goreleaser's "<name>_<version>_checksums.txt", rclone's
// "SHA256SUMS", jq's "sha256sum.txt"). single reports a side-car, which may
// carry the hash alone.
func toolboxChecksumAsset(assets []releaseAsset, asset string) (sum releaseAsset, single bool, ok bool) {
	lower := strings.ToLower(asset)
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if n == lower+".sha256" || n == lower+".sha256sum" {
			return a, true, true
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		switch {
		case n == "sha256sums", n == "sha256sums.txt", n == "sha256sum.txt", n == "checksums.txt":
			return a, false, true
		case strings.Contains(n, "checksums") && strings.HasSuffix(n, ".txt") && !strings.Contains(n, "sha512"):
			return a, false, true
		}
	}
	return releaseAsset{}, false, false
}

// toolboxIsSHA256 reports whether s is 64 hex digits.
func toolboxIsSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// toolboxParseChecksum returns the SHA-256 a checksum file lists for asset. The
// usual "<hash>  <name>" and "<hash> *<name>" lines are read; a side-car
// (single) may also hold the hash alone. Signature armour and other lines are
// ignored.
func toolboxParseChecksum(text, asset string, single bool) (string, bool) {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !toolboxIsSHA256(fields[0]) {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(fields) == 1 {
			if single {
				return sum, true
			}
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		name = path.Base(strings.TrimPrefix(name, "./"))
		if strings.EqualFold(name, asset) {
			return sum, true
		}
	}
	return "", false
}

// toolboxFetchSmall downloads a small text file (a checksum list) from GitHub.
func toolboxFetchSmall(ctx context.Context, rawURL string) ([]byte, error) {
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://github.com/") {
		return nil, fmt.Errorf("refusing a checksum file from outside github.com")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-Toolbox/1.0")
	// The same certificate fallback as the downloads it verifies (tools.go).
	client := &http.Client{Timeout: 30 * time.Second, Transport: ytTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checksum file: unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// toolboxVerifyDownload checks file against the SHA-256 the release publishes
// for asset. It returns the file's own hash and whether it was checked against
// an upstream value; a mismatch is an error and nothing gets installed.
func toolboxVerifyDownload(ctx context.Context, assets []releaseAsset, asset, file string) (string, bool, error) {
	got, err := hashFile(file)
	if err != nil {
		return "", false, fmt.Errorf("hash the download: %w", err)
	}
	sumAsset, single, ok := toolboxChecksumAsset(assets, asset)
	if !ok {
		return got, false, nil
	}
	installBarPhase(installPhaseVerify)
	data, err := toolboxFetchSmall(ctx, sumAsset.URL)
	if err != nil {
		return got, false, fmt.Errorf("download the published checksum: %w", err)
	}
	verified, err := toolboxMatchChecksum(got, string(data), asset, single)
	if err == nil && !verified {
		logPatch("[TOOLS] toolbox: %s does not list %s; installed without a checksum match", sumAsset.Name, asset)
	}
	return got, verified, err
}

// toolboxMatchChecksum compares a file's SHA-256 with the value a checksum
// file lists for asset. verified is false (with no error) when the list does
// not mention the asset; a different value is an error.
func toolboxMatchChecksum(fileSum, list, asset string, single bool) (bool, error) {
	want, ok := toolboxParseChecksum(list, asset, single)
	if !ok {
		return false, nil
	}
	if !strings.EqualFold(fileSum, want) {
		return false, fmt.Errorf("the download does not match the SHA-256 the project publishes for %s", asset)
	}
	return true, nil
}

// ── Install record ───────────────────────────────────────────────────────

// toolboxRecord is what the toolbox remembers about one install: the release
// tag (for "Installed · v1.7.1" and the update check), the file it came from
// and whether its hash matched the project's published one.
type toolboxRecord struct {
	Tag      string `json:"tag,omitempty"`
	Asset    string `json:"asset,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Verified bool   `json:"verified"`
	At       string `json:"installed_at,omitempty"`
}

// toolboxManifestName sits in the tools folder next to the binaries it
// describes, so copying the folder to another card keeps it truthful.
const toolboxManifestName = "toolbox-installed.json"

var toolboxManifest struct {
	mu     sync.Mutex
	dir    string
	loaded bool
	recs   map[string]toolboxRecord
}

func toolboxManifestLoadLocked() {
	dir := toolboxToolsDir()
	if toolboxManifest.loaded && toolboxManifest.dir == dir {
		return
	}
	toolboxManifest.dir = dir
	toolboxManifest.loaded = true
	toolboxManifest.recs = map[string]toolboxRecord{}
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, toolboxManifestName))
	if err != nil {
		return
	}
	var recs map[string]toolboxRecord
	if json.Unmarshal(data, &recs) == nil && recs != nil {
		toolboxManifest.recs = recs
	}
}

func toolboxManifestSaveLocked() error {
	if toolboxManifest.dir == "" {
		return fmt.Errorf("tools directory unavailable")
	}
	data, err := json.MarshalIndent(toolboxManifest.recs, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(toolboxManifest.dir, toolboxManifestName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// toolboxRecordFor returns what was recorded when id was installed.
func toolboxRecordFor(id string) (toolboxRecord, bool) {
	toolboxManifest.mu.Lock()
	defer toolboxManifest.mu.Unlock()
	toolboxManifestLoadLocked()
	rec, ok := toolboxManifest.recs[id]
	return rec, ok
}

func toolboxSaveRecord(id string, rec toolboxRecord) {
	toolboxManifest.mu.Lock()
	defer toolboxManifest.mu.Unlock()
	toolboxManifestLoadLocked()
	toolboxManifest.recs[id] = rec
	if err := toolboxManifestSaveLocked(); err != nil {
		logPatch("[TOOLS] toolbox: cannot save the install record: %v", err)
	}
}

func toolboxForgetRecord(id string) {
	toolboxManifest.mu.Lock()
	defer toolboxManifest.mu.Unlock()
	toolboxManifestLoadLocked()
	if _, ok := toolboxManifest.recs[id]; !ok {
		return
	}
	delete(toolboxManifest.recs, id)
	if err := toolboxManifestSaveLocked(); err != nil {
		logPatch("[TOOLS] toolbox: cannot save the install record: %v", err)
	}
}

// ── Install, update, remove ──────────────────────────────────────────────

// toolboxInstall downloads and unpacks one catalog tool into the tools folder.
// It is idempotent: an installed copy is left alone so a running binary is
// never overwritten (ETXTBSY on Linux).
func toolboxInstall(id string, config *Config) error {
	return toolboxInstallTool(id, config, false)
}

// toolboxUpdate replaces an installed tool with the latest release. The new
// binary is downloaded, verified and unpacked beside the old one first, so a
// failed download leaves the working copy in place.
func toolboxUpdate(id string, config *Config) error {
	return toolboxInstallTool(id, config, true)
}

func toolboxInstallTool(id string, config *Config, replace bool) (err error) {
	t, ok := toolboxFind(id)
	if !ok {
		return fmt.Errorf("unknown tool %q", id)
	}
	existing := toolboxInstalled(t)
	if existing != "" && !replace {
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

	run, runCtx := installBarBeginRun(t.Name)
	// Every failure from here on settles the card — this run's card only: an
	// install begun meanwhile owns it now, and its progress must not turn
	// into this one's error.
	defer func() {
		if err != nil {
			installBarFailRun(run, installCardDetail(err.Error()))
		}
	}()
	installBarPhase(installPhaseResolving)
	rel, err := toolboxReleaseFetch(runCtx, t.Repo)
	if err != nil {
		return fmt.Errorf("find %s release: %w", t.Name, err)
	}
	asset, ok := pickToolAsset(rel.Assets, groups)
	if !ok {
		return fmt.Errorf("%s %s has no download for %s/%s", t.Name, rel.Tag, runtime.GOOS, runtime.GOARCH)
	}
	logPatch("[TOOLS] toolbox %s %s -> %s", t.ID, rel.Tag, asset.URL)

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
	if err := downloadFileWithProgressCtx(runCtx, asset.URL, tmpName, installBarProgress); err != nil {
		return fmt.Errorf("download %s: %w", t.Name, err)
	}
	sum, verified, err := toolboxVerifyDownload(runCtx, rel.Assets, asset.Name, tmpName)
	if err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}

	target := toolboxPath(t)
	if target == "" {
		return fmt.Errorf("tools directory unavailable (empty path)")
	}
	installBarPhase(installPhaseExtract)
	staged := tmpName
	if toolboxArchived(asset.Name) {
		stage, err := os.MkdirTemp(dir, ".toolbox-stage-*")
		if err != nil {
			return fmt.Errorf("create staging folder in %s: %w", dir, err)
		}
		defer os.RemoveAll(stage)
		base := filepath.Base(target)
		found, err := extractToolFiles(tmpName, stage, []string{base})
		if err != nil {
			return fmt.Errorf("unpack %s: %w", t.Name, err)
		}
		if len(found) == 0 {
			return fmt.Errorf("%s archive did not contain %s", t.Name, base)
		}
		staged = filepath.Join(stage, strings.ToLower(base))
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(staged, PermExec); err != nil {
			logPatch("[TOOLS] toolbox %s: chmod failed: %v", t.ID, err)
		}
	}
	if err := toolboxPlace(staged, target, existing != ""); err != nil {
		return fmt.Errorf("install %s: %w", t.Name, err)
	}
	if toolboxInstalled(t) == "" {
		return fmt.Errorf("%s reported success but %s is missing", t.Name, filepath.Base(target))
	}
	toolboxSaveRecord(t.ID, toolboxRecord{
		Tag:      rel.Tag,
		Asset:    asset.Name,
		SHA256:   sum,
		Verified: verified,
		At:       time.Now().UTC().Format(time.RFC3339),
	})
	toolboxLatestSet(t.ID, rel.Tag)
	installBarDoneRun(run)
	logPatch("[TOOLS] toolbox %s %s installed at %s (verified=%v)", t.ID, rel.Tag, target, verified)
	return nil
}

// toolboxPlace moves a staged binary to target. When an older copy is there it
// is first renamed aside — a running binary can be renamed on Linux and on
// Windows, but not overwritten — and put back if the new one cannot be placed.
func toolboxPlace(staged, target string, replace bool) error {
	old := target + ".old"
	_ = os.Remove(old)
	moved := false
	if replace {
		if err := os.Rename(target, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move the old copy aside: %w", err)
		}
		moved = true
	}
	if err := os.Rename(staged, target); err != nil {
		if err := copyFileStream(staged, target, PermExec); err != nil {
			if moved {
				_ = os.Rename(old, target)
			}
			return err
		}
	}
	if moved {
		// Best effort: a copy that is still running on Windows stays until
		// the next install clears it.
		_ = os.Remove(old)
	}
	return nil
}

// toolboxRemove deletes an installed tool and its install record.
func toolboxRemove(id string) error {
	t, ok := toolboxFind(id)
	if !ok {
		return fmt.Errorf("unknown tool %q", id)
	}
	p := toolboxInstalled(t)
	if p == "" {
		toolboxForgetRecord(id)
		return nil
	}
	if err := os.Remove(p); err != nil {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("could not remove %s — close it if it is still running", t.Name)
		}
		return fmt.Errorf("remove %s: %w", t.Name, err)
	}
	_ = os.Remove(p + ".old")
	toolboxForgetRecord(id)
	return nil
}

// ── Update check ─────────────────────────────────────────────────────────

// toolboxLatest is what the last update check found. It is kept for a few
// hours: GitHub allows sixty anonymous API calls an hour, and a visit to the
// screen must not spend nine of them every time.
var toolboxLatest struct {
	mu      sync.Mutex
	tags    map[string]string
	checked time.Time
}

const toolboxLatestTTL = 6 * time.Hour

func toolboxLatestSet(id, tag string) {
	toolboxLatest.mu.Lock()
	defer toolboxLatest.mu.Unlock()
	if toolboxLatest.tags == nil {
		toolboxLatest.tags = map[string]string{}
	}
	if tag != "" {
		toolboxLatest.tags[id] = tag
	}
}

// toolboxLatestTag is the newest release tag seen for id ("" when unknown).
func toolboxLatestTag(id string) string {
	toolboxLatest.mu.Lock()
	defer toolboxLatest.mu.Unlock()
	return toolboxLatest.tags[id]
}

// toolboxCheckUpdates asks GitHub for the latest release of every installed
// tool, at most once per TTL. It runs as a scene task: ctx ends with the visit.
func toolboxCheckUpdates(ctx context.Context) {
	toolboxLatest.mu.Lock()
	fresh := !toolboxLatest.checked.IsZero() && time.Since(toolboxLatest.checked) < toolboxLatestTTL
	toolboxLatest.mu.Unlock()
	if fresh || !IsOnline() {
		return
	}
	for _, t := range toolboxCatalog() {
		if ctx.Err() != nil {
			return
		}
		if toolboxInstalled(t) == "" {
			continue
		}
		rel, err := toolboxReleaseFetch(ctx, t.Repo)
		if err != nil {
			if errors.Is(err, errToolboxRateLimited) {
				break
			}
			continue
		}
		toolboxLatestSet(t.ID, rel.Tag)
	}
	if ctx.Err() != nil {
		return
	}
	toolboxLatest.mu.Lock()
	toolboxLatest.checked = time.Now()
	toolboxLatest.mu.Unlock()
}

// toolboxUpdateAvailable is the newer tag for an installed tool, or "" when it
// is current or either version is unknown.
func toolboxUpdateAvailable(id string) string {
	rec, ok := toolboxRecordFor(id)
	latest := toolboxLatestTag(id)
	if !ok || rec.Tag == "" || latest == "" || latest == rec.Tag {
		return ""
	}
	return latest
}

// ── Run ──────────────────────────────────────────────────────────────────

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
	// Capacity of the argument count alone: len(args)+1 is a sum that could in
	// principle wrap, and one spare element is not worth an expression nothing
	// can bound.
	parts := make([]string, 0, len(args))
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

// toolboxTerminalCommand is the line the Terminal is opened with for a tool:
// the installed path (quoted when it has spaces) and the typed or example
// arguments, ready to edit before Enter.
func toolboxTerminalCommand(t toolboxTool, path, argLine string) string {
	line, _ := toolboxCommandLine(t, argLine)
	rest := strings.TrimPrefix(line, t.Binary)
	if strings.ContainsAny(path, " \t") {
		path = `"` + path + `"`
	}
	return path + rest
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
	auditConfigureHidden(cmd)
	// A tool that leaves a child holding the output pipe must not keep the
	// run (and the sheet waiting on it) past the timeout.
	cmd.WaitDelay = powerScriptWaitDelay
	out, err := cmd.CombinedOutput()
	text := truncatePowerOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + fmt.Sprintf("\n(stopped after %s)", timeout), ctx.Err()
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return text, nil
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

// ── Authorized use ───────────────────────────────────────────────────────

// toolboxAuthorizedUseKey is the acknowledgement stored in jukauser.json the
// first time a tool that sends traffic to other hosts is started — a Toolbox
// scanner or an Audit toolkit tool.
const toolboxAuthorizedUseKey = "networkScanAuthorized"

// toolboxAuthorizedUseAccepted reports whether the user has already answered.
func toolboxAuthorizedUseAccepted(config *Config) bool {
	if config == nil {
		return false
	}
	v, ok := customValue(config.Variables.Custom, toolboxAuthorizedUseKey)
	return ok && coerceBool(v)
}

// toolboxAcceptAuthorizedUse records the answer once, so the question never
// comes back for any tool.
func toolboxAcceptAuthorizedUse(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[toolboxAuthorizedUseKey] = true
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// ── Summaries ────────────────────────────────────────────────────────────

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
