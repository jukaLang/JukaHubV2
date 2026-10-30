package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// resolveToolURL returns the download URL for the first release asset whose
// name matches one of the wanted strings (case-insensitive).
//
// IMPORTANT: repo must be a trusted GitHub repo, chosen from a small curated list
// in this codebase. Arbitrary user-supplied repo names are not safe here because
// the GitHub API can be abused to probe repo/release metadata and because a future
// code path could be tempted to trust the returned download URL too much.
var allowedToolRepos = map[string]bool{
	"btbn/ffmpeg-builds": true,
	"yt-dlp/yt-dlp":      true,
	"jukalang/jukahubv2": true,
	// Toolbox entries (toolbox.go). Each one ships signed or unsigned release
	// archives that are downloaded without a hash from GitHub over TLS; the
	// trust boundary is this list, which is why it is short and code-reviewed.
	"jqlang/jq":          true,
	"rclone/rclone":      true,
	"burntsushi/ripgrep": true,
	"sharkdp/fd":         true,
	"jgm/pandoc":         true,
	"filosottile/age":    true,
	// Network scanners (Toolbox → naabu, httpx, dnsx). Same trust rule as the
	// rest: the releases are read from GitHub over TLS without a hash, so this
	// list is the boundary. Their runs also sit behind the one-time
	// authorized-use acknowledgement in toolboxAuthorizedUseGate.
	"projectdiscovery/naabu": true,
	"projectdiscovery/httpx": true,
	"projectdiscovery/dnsx":  true,
}

func resolveToolURL(repo, apiToken string, wants ...string) (string, error) {
	asset, err := resolveToolAsset(repo, apiToken, wants...)
	if err != nil {
		return "", err
	}
	return asset.URL, nil
}

// resolveToolAsset is resolveToolURL returning the whole asset, size included.
// The wanted names are a preference order (the GPL ffmpeg build before the LGPL
// one), so they are tried in turn rather than in the order the API lists the
// release's files.
func resolveToolAsset(repo, apiToken string, wants ...string) (releaseAsset, error) {
	if len(wants) == 0 {
		return releaseAsset{}, fmt.Errorf("resolveToolAsset called without any wanted asset names")
	}
	assets, err := latestReleaseAssets(repo, apiToken)
	if err != nil {
		return releaseAsset{}, err
	}
	if a, ok := pickReleaseAsset(assets, wants); ok {
		return a, nil
	}
	names := make([]string, 0, len(wants))
	for _, w := range wants {
		names = append(names, fmt.Sprintf("%q", w))
	}
	return releaseAsset{}, fmt.Errorf("no matching asset found in %s latest release (wanted %s)", repo, strings.Join(names, ", "))
}

// pickReleaseAsset returns the asset named by the first entry of wants that
// the release has, comparing names case-insensitively.
func pickReleaseAsset(assets []releaseAsset, wants []string) (releaseAsset, bool) {
	for _, w := range wants {
		for _, a := range assets {
			if strings.EqualFold(a.Name, w) {
				return a, true
			}
		}
	}
	return releaseAsset{}, false
}

// releaseAsset is one file attached to a GitHub release.
type releaseAsset struct {
	Name string
	URL  string
	Size int64
}

// latestReleaseAssets lists a trusted repo's latest release assets. The repo
// must be in allowedToolRepos: this is the only place a download URL comes
// from, so the list is the trust boundary rather than a convenience.
func latestReleaseAssets(repo, apiToken string) ([]releaseAsset, error) {
	if !allowedToolRepos[strings.ToLower(repo)] {
		return nil, fmt.Errorf("tool repo %q is not in the approved download list", repo)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	req.Header.Set("User-Agent", "JukaHub-Patch-Tool/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("github api rate limit exceeded (retry after %s)", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %s: unexpected status %d", apiURL, resp.StatusCode)
	}

	var rel struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if len(rel.Assets) == 0 {
		return nil, fmt.Errorf("no assets in %s latest release", repo)
	}
	out := make([]releaseAsset, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		out = append(out, releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return out, nil
}

// resolveToolURLMatching returns the first asset whose name contains every
// token of one of the wanted groups, trying the groups in order. Release asset
// names embed the version (`rclone-v1.68.1-linux-arm64.zip`), so an exact-name
// lookup would break on every upstream release; matching on the stable parts of
// the name does not. The repo must still be on the approved list.
func resolveToolURLMatching(repo, apiToken string, wanted ...[]string) (releaseAsset, error) {
	if len(wanted) == 0 {
		return releaseAsset{}, fmt.Errorf("resolveToolURLMatching called without any wanted asset patterns")
	}
	assets, err := latestReleaseAssets(repo, apiToken)
	if err != nil {
		return releaseAsset{}, err
	}
	if a, ok := pickToolAsset(assets, wanted); ok {
		return a, nil
	}
	groups := make([]string, 0, len(wanted))
	for _, tokens := range wanted {
		groups = append(groups, strings.Join(tokens, "+"))
	}
	return releaseAsset{}, fmt.Errorf("no asset in %s matched %s", repo, strings.Join(groups, ", "))
}

// pickToolAsset returns the first asset that matches one of the token groups,
// trying the groups in order. Checksum, signature and text side-cars are never
// picked: their names contain the archive name, so a token match alone would
// hand the downloader a `.sha256` file.
func pickToolAsset(assets []releaseAsset, wanted [][]string) (releaseAsset, bool) {
	for _, tokens := range wanted {
		if len(tokens) == 0 {
			continue
		}
		for _, a := range assets {
			if isToolAssetSidecar(a.Name) {
				continue
			}
			if assetNameMatches(a.Name, tokens) {
				return a, true
			}
		}
	}
	return releaseAsset{}, false
}

// isToolAssetSidecar reports whether an asset name is a checksum, signature or
// release-notes file rather than a runnable archive or binary.
func isToolAssetSidecar(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".sha256", ".sha512", ".sha1", ".md5", ".sig", ".asc", ".txt", ".json", ".pem"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// assetNameMatches reports whether every token appears in the asset name,
// case-insensitively. Tokens are the stable parts of an upstream file name
// (platform, architecture, archive extension), never a version.
func assetNameMatches(name string, tokens []string) bool {
	lower := strings.ToLower(name)
	for _, t := range tokens {
		if t == "" || !strings.Contains(lower, strings.ToLower(t)) {
			return false
		}
	}
	return true
}

// downloadFile downloads url to dest.
// It also enforces a modest size cap and refuses obviously truncated files.
func downloadFile(url, dest string) error {
	return downloadFileWithProgress(url, dest, nil)
}

// downloadFileWithProgress downloads url to dest and reports progress in
// bytes (done, total) while streaming. total is <= 0 when the server did not
// send a Content-Length. The request timeout is generous because helper-tool
// bundles (ffmpeg) are large; the size cap still applies.
func downloadFileWithProgress(url, dest string, progress func(done, total int64)) error {
	return downloadFileWithProgressCtx(context.Background(), url, dest, progress)
}

// downloadFileWithProgressCtx is downloadFileWithProgress bounded by ctx: a
// cancelled download stops at the next read instead of running to completion
// behind a screen the user has already dismissed.
func downloadFileWithProgressCtx(ctx context.Context, url, dest string, progress func(done, total int64)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}
	const maxDownload = int64(512 << 20)
	total := resp.ContentLength
	if total > maxDownload {
		return fmt.Errorf("download %s: declared size %d exceeds %d bytes", url, total, maxDownload)
	}
	rate := io.LimitReader(resp.Body, maxDownload+1)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	rw := &ioWriteCloserNW{w: out}
	// Copy with a hard cap, reporting progress as bytes stream in.
	n, err := io.Copy(rw, &progressReader{r: rate, total: total, fn: progress})
	if err != nil {
		out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: write failed: %w", url, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: close failed: %w", url, err)
	}
	if n > maxDownload {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s exceeds %d bytes", url, maxDownload)
	}
	if n < 4096 {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s is suspiciously small (%d bytes)", url, n)
	}
	if progress != nil {
		progress(n, n)
	}
	return nil
}

// progressReader forwards reads to a progress callback. The callback is
// throttled by the caller's state (installBarProgress rate-limits itself), so
// this stays a thin pass-through.
type progressReader struct {
	r     io.Reader
	total int64
	fn    func(done, total int64)
	done  int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil && n > 0 {
		p.fn(p.done, p.total)
	}
	return n, err
}

// ioWriteCloserNW is a thin wrapper so we can Close the underlying writer
// after io.Copy without changing io.Copy's error semantics.
type ioWriteCloserNW struct {
	w io.Writer
}

func (c *ioWriteCloserNW) Write(p []byte) (int, error) {
	return c.w.Write(p)
}

func (c *ioWriteCloserNW) Close() error {
	if wc, ok := c.w.(io.Closer); ok {
		return wc.Close()
	}
	return nil
}

// extractToolFiles pulls specific files out of a tool archive and places them
// at the top of destDir regardless of how deeply the archive nests them
// (BtbN ffmpeg zips ship bin/ffmpeg.exe inside a versioned folder). Only the
// requested base names are written, so archive traversal is impossible by
// construction. Returns the base names actually extracted.
//
// Everything is unpacked into a staging directory *inside destDir*, never the
// system temp dir: on TrimUI /tmp is a small tmpfs while an ffmpeg archive
// expands to several hundred megabytes. Keeping the stage on the same
// filesystem also lets each finished binary be renamed into place instead of
// being written over, which avoids ETXTBSY when a copy is already running.
func extractToolFiles(archivePath, destDir string, names []string) ([]string, error) {
	return extractToolFilesCtx(context.Background(), archivePath, destDir, names, nil)
}

// extractToolFilesCtx is extractToolFiles that stops when ctx is cancelled and
// reports how far it has read through the archive (bytes, archive size) to
// progress, which may be nil.
//
// Tar archives (plain, .tar.gz, .tar.xz) are decoded in-process. The handheld
// ffmpeg builds are .tar.xz, and the TrimUI firmware cannot unpack those with
// its own tools: its busybox has no xz, and a tar that can still writes the
// whole ~400 MB bundle to the card and, running as root, trips over chown on
// the card's exFAT. That was "Could not install ffplay" on every attempt and
// every retry. The system tar is now only a second opinion for an archive the
// built-in reader rejects.
func extractToolFilesCtx(ctx context.Context, archivePath, destDir string, names []string, progress func(done, total int64)) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create tools directory %s: %w", destDir, err)
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	stage, err := os.MkdirTemp(destDir, ".tool-stage-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir in %s: %w", destDir, err)
	}
	defer os.RemoveAll(stage)

	// Base name -> path inside the staging dir.
	var staged map[string]string
	_, isTar := toolTarCompression(archivePath)
	switch {
	case strings.HasSuffix(strings.ToLower(archivePath), ".zip"):
		staged, err = extractZipMembers(ctx, archivePath, stage, want)
	case isTar:
		staged, err = extractTarMembers(ctx, archivePath, stage, want, progress)
		if err != nil && errors.Is(err, errBadToolArchive) && ctx.Err() == nil {
			// The built-in reader could not decode the archive. A system tar may
			// know a variant it does not, so it gets one try before the archive
			// is reported damaged; its own error only goes to the log.
			log.Printf("[tools] built-in unpack of %s failed: %v; trying the system tar", filepath.Base(archivePath), err)
			alt, altErr := extractWithSystemTar(archivePath, stage, want)
			if altErr == nil {
				staged, err = alt, nil
			} else {
				log.Printf("[tools] the system tar could not unpack %s either: %v", filepath.Base(archivePath), altErr)
			}
		}
	default:
		staged, err = extractWithSystemTar(archivePath, stage, want)
	}
	if err != nil {
		return nil, err
	}

	// Move the staged binaries into place. A destination that already has
	// content is left untouched: it may be a newer copy, or the executable a
	// running player was started from (rewriting it would fail with ETXTBSY).
	found := make([]string, 0, len(names))
	for _, name := range names {
		base := strings.ToLower(name)
		src, ok := staged[base]
		if !ok {
			continue
		}
		dst, err := safeJoinFile(destDir, base)
		if err != nil {
			return found, err
		}
		found = append(found, base)
		if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
			continue
		}
		if err := os.Chmod(src, PermExec); err != nil {
			log.Printf("[tools] warn: cannot chmod staged %s: %v", base, err)
		}
		if err := os.Rename(src, dst); err != nil {
			// Different filesystem (should not happen: both live in destDir's
			// tree) or a stale directory entry. Fall back to a streamed copy.
			if err := copyFileStream(src, dst, PermExec); err != nil {
				return found, err
			}
			_ = os.Remove(src)
		}
	}
	return found, nil
}

// copyFileStream copies src to dst with a fixed buffer, creating dst with perm.
func copyFileStream(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// errBadToolArchive marks an archive that could not be read back: truncated,
// corrupt, or in a format the decoders do not handle. Such a download is
// deleted so the next attempt fetches a fresh copy. A write failure (a full or
// read-only card) is returned untagged instead, because downloading the same
// bytes again would not help.
var errBadToolArchive = errors.New("archive is damaged")

// maxToolEntryBytes caps a single unpacked tool. BtbN's static ffmpeg is in the
// low hundreds of megabytes; nothing JukaHub installs comes near this.
const maxToolEntryBytes = int64(1 << 30)

// toolTarCompression reports whether an archive name is a tar archive the
// built-in reader handles, and the compression around it: "xz", "gz", or ""
// for a plain tar.
func toolTarCompression(name string) (string, bool) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return "xz", true
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "gz", true
	case strings.HasSuffix(lower, ".tar"):
		return "", true
	}
	return "", false
}

// archiveReader is what the unpackers read an archive through. It stops
// between reads once ctx is cancelled, and tags decode failures with
// errBadToolArchive so a damaged download can be told apart from a full card.
type archiveReader struct {
	ctx context.Context
	r   io.Reader
}

func (a *archiveReader) Read(p []byte) (int, error) {
	if err := a.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := a.r.Read(p)
	if err == nil || err == io.EOF || errors.Is(err, errBadToolArchive) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return n, err
	}
	return n, fmt.Errorf("%w (%v)", errBadToolArchive, err)
}

// extractTarMembers streams a tar archive once and writes only the wanted
// regular files into stage, matched by lower-cased base name, so an ffmpeg
// bundle costs the card the binaries it was opened for rather than its whole
// tree, and no external tar, xz or busybox is involved. The stream is still
// read to its end: the xz and gzip checksums are only verified there.
func extractTarMembers(ctx context.Context, archivePath, stage string, want map[string]bool, progress func(done, total int64)) (map[string]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var total int64
	if info, err := f.Stat(); err == nil {
		total = info.Size()
	}
	// Progress counts the compressed bytes consumed, the one figure known up
	// front. The buffer above it matters for speed: the xz decoder pulls its
	// input a byte at a time, and unbuffered every byte of a 128 MB download
	// would be a read system call.
	src := bufio.NewReaderSize(&progressReader{r: f, total: total, fn: progress}, 1<<20)
	var stream io.Reader = src
	compression, _ := toolTarCompression(archivePath)
	switch compression {
	case "xz":
		xr, err := xz.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("%w (xz: %v)", errBadToolArchive, err)
		}
		stream = xr
	case "gz":
		gr, err := gzip.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("%w (gzip: %v)", errBadToolArchive, err)
		}
		defer gr.Close()
		stream = gr
	}
	tr := tar.NewReader(&archiveReader{ctx: ctx, r: stream})
	staged := make(map[string]string, len(want))
	for len(staged) < len(want) {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if errors.Is(err, errBadToolArchive) {
				return nil, err
			}
			return nil, fmt.Errorf("%w (tar: %v)", errBadToolArchive, err)
		}
		// File contents only: links (hard links included, which carry no data)
		// and devices are never materialised.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeGNUSparse {
			continue
		}
		base := strings.ToLower(path.Base(hdr.Name))
		if !want[base] {
			continue
		}
		if _, done := staged[base]; done {
			continue
		}
		dest, err := safeJoinFile(stage, base)
		if err != nil {
			return nil, err
		}
		if err := writeArchiveEntry(ctx, tr, dest, hdr.Size); err != nil {
			return nil, err
		}
		staged[base] = dest
	}
	// Drain the rest even when every wanted file came early. A single-block
	// .xz is only checked at its very end, and a corrupt download must not be
	// installed as a binary that crashes the first time a video plays.
	if _, err := io.Copy(io.Discard, &archiveReader{ctx: ctx, r: stream}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return staged, nil
}

// extractZipMembers writes the wanted entries of a zip archive into stage.
func extractZipMembers(ctx context.Context, archivePath, stage string, want map[string]bool) (map[string]string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("%w (open %s: %v)", errBadToolArchive, filepath.Base(archivePath), err)
	}
	defer zr.Close()
	staged := make(map[string]string, len(want))
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(path.Base(filepath.ToSlash(f.Name)))
		if !want[base] {
			continue
		}
		if _, done := staged[base]; done {
			continue
		}
		dest, err := safeJoinFile(stage, base)
		if err != nil {
			return nil, err
		}
		if err := extractZipEntryTo(ctx, f, dest); err != nil {
			return nil, err
		}
		staged[base] = dest
	}
	return staged, nil
}

// extractZipEntryTo writes a single zip entry to dest. The zip reader checks
// the entry's CRC when it reaches the end, so a corrupt download surfaces as
// errBadToolArchive rather than as a broken binary.
func extractZipEntryTo(ctx context.Context, f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w (%s: %v)", errBadToolArchive, f.Name, err)
	}
	defer rc.Close()
	return writeArchiveEntry(ctx, rc, dest, int64(f.UncompressedSize64))
}

// writeArchiveEntry copies one archive member to dest and never leaves a
// partial file behind. size is the length the archive declares (0 if unknown);
// a member that ends early is a damaged archive.
func writeArchiveEntry(ctx context.Context, src io.Reader, dest string, size int64) error {
	if size > maxToolEntryBytes {
		return fmt.Errorf("%w (%s declares %d bytes)", errBadToolArchive, filepath.Base(dest), size)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, PermExec)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, &archiveReader{ctx: ctx, r: io.LimitReader(src, maxToolEntryBytes+1)})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		switch {
		case n > maxToolEntryBytes:
			err = fmt.Errorf("%w (%s is larger than %d bytes)", errBadToolArchive, filepath.Base(dest), maxToolEntryBytes)
		case n == 0:
			err = fmt.Errorf("%w (%s is empty)", errBadToolArchive, filepath.Base(dest))
		case size > 0 && n != size:
			err = fmt.Errorf("%w (%s ended after %d of %d bytes)", errBadToolArchive, filepath.Base(dest), n, size)
		}
	}
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}

// extractWithSystemTar is the external route: the system tar unpacks the whole
// archive into a folder inside stage and the wanted names are picked out of it.
// It is what every tar archive used to go through.
func extractWithSystemTar(archivePath, stage string, want map[string]bool) (map[string]string, error) {
	dir, err := os.MkdirTemp(stage, "tar-")
	if err != nil {
		return nil, err
	}
	if err := runTarExtract(archivePath, dir); err != nil {
		return nil, err
	}
	staged := make(map[string]string, len(want))
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		base := strings.ToLower(info.Name())
		if !want[base] {
			return nil
		}
		if _, done := staged[base]; !done {
			staged[base] = p
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staged, nil
}

// runTarExtract extracts an archive into dstDir using the system tar, with a
// decompress-then-untar fallback for busybox/tar builds without xz support.
func runTarExtract(archivePath, dstDir string) error {
	base := filepath.Base(archivePath)
	tarPath, tarErr := exec.LookPath("tar")
	busyboxPath, _ := exec.LookPath("busybox")

	// Forward slashes keep GNU tar from reading "C:\..." as a remote
	// host:path spec (bsdtar accepts them too).
	dstArg := filepath.ToSlash(dstDir)

	untar := func(archive string) error {
		// Run tar from the archive's own directory and pass only its base name:
		// GNU tar (Git Bash / MinGW) reads "C:/..." as a remote host:path
		// spec and fails with "Cannot connect to C:".
		cmdDir := filepath.Dir(archive)
		name := filepath.Base(archive)
		var lastErr error
		if tarPath != "" {
			// -xJf unpacks .tar.xz directly; -xf is the bsdtar fallback that
			// auto-detects compression.
			for _, args := range [][]string{{"-xJf", name, "-C", dstArg}, {"-xf", name, "-C", dstArg}} {
				cmd := exec.Command(tarPath, args...)
				cmd.Dir = cmdDir
				out, err := cmd.CombinedOutput()
				if err == nil {
					return nil
				}
				lastErr = fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
				log.Printf("[tools] tar %v failed: %v", args, lastErr)
			}
		}
		if busyboxPath != "" {
			for _, args := range [][]string{{"tar", "-xJf", name, "-C", dstArg}, {"tar", "-xf", name, "-C", dstArg}} {
				cmd := exec.Command(busyboxPath, args...)
				cmd.Dir = cmdDir
				out, err := cmd.CombinedOutput()
				if err == nil {
					return nil
				}
				lastErr = fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
				log.Printf("[tools] busybox %v failed: %v", args, lastErr)
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no tar available: %w", tarErr)
		}
		return lastErr
	}

	if err := untar(archivePath); err == nil {
		return nil
	}

	// Last resort for tar builds without xz: decompress with an external xz
	// (or busybox's xz applet) and untar the plain archive.
	if strings.HasSuffix(strings.ToLower(archivePath), ".xz") {
		plainTar := filepath.Join(dstDir, "archive.tar")
		defer os.Remove(plainTar)
		if err := decompressXZ(archivePath, plainTar); err != nil {
			return fmt.Errorf("could not unpack %s: %w", base, err)
		}
		if err := untar(plainTar); err == nil {
			return nil
		}
	}
	return fmt.Errorf("could not unpack %s: xz support missing from tar/xz (copy ffmpeg and ffprobe into the required folder manually)", base)
}

// decompressXZ writes the decompressed contents of an .xz file to dst using
// the first available xz implementation.
func decompressXZ(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	srcArg := filepath.ToSlash(src)
	var candidates [][]string
	if p, err := exec.LookPath("xz"); err == nil {
		candidates = append(candidates, []string{p, "-dc", srcArg})
	}
	if p, err := exec.LookPath("busybox"); err == nil {
		candidates = append(candidates, []string{p, "xz", "-dc", srcArg})
	}
	var lastErr error
	for _, argv := range candidates {
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := out.Truncate(0); err != nil {
			return err
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = out
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("[tools] %v failed: %v", argv, err)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no xz decompressor available")
	}
	return lastErr
}

// hashFile returns the SHA256 of path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureDirPath is a no-op helper that normalizes paths across platforms.
func ensureDirPath(dir string) string {
	dir = filepath.Clean(dir)
	if dir == "" {
		dir = "."
	}
	return dir
}

// safeInt converts a string to int32 without silent truncation. If the value
// is too large or not a valid integer, it returns the fallback value.
func safeInt(text string, fallback int32) int32 {
	v, ok := parsePosInt(text)
	if !ok {
		return fallback
	}
	return v
}

// parsePositiveSize parses a WxH string (e.g. "1280x720") into (width, height).
// Non-numeric or missing parts fall back to 1280 and 720, and values are capped
// to sane maximums to avoid runaway allocations.
func parsePositiveSize(raw string) (int32, int32) {
	parts := strings.SplitN(strings.TrimSpace(raw), "x", 2)
	width := int32(1280)
	height := int32(720)
	if len(parts) == 2 {
		w, ok := parsePosInt(strings.TrimSpace(parts[0]))
		if ok && w > 0 {
			width = w
			if width > 7680 {
				width = 7680
			}
		}
		h, ok := parsePosInt(strings.TrimSpace(parts[1]))
		if ok && h > 0 {
			height = h
			if height > 4320 {
				height = 4320
			}
		}
	}
	return width, height
}

// firstExisting returns the first candidate path that exists on disk.
func firstExisting(candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// retryDo retries fn up to maxAttempts times on transient errors.
func retryDo(maxAttempts int, fn func() error) error {
	var err error
	for i := 0; i < maxAttempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", maxAttempts, err)
}

// containsIgnoreCase reports whether substr appears in s, ignoring case.
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// clampInt32 clamps v into [min, max].
func clampInt32(v, min, max int32) int32 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// clampFloat64 clamps v into [min, max].
func clampFloat64(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// roundToInt rounds f to the nearest int32.
func roundToInt(f float64) int32 {
	return int32(math.Round(f))
}

// floorToInt floors f to the nearest int32.
func floorToInt(f float64) int32 {
	return int32(math.Floor(f))
}

// ceilToInt ceilings f to the nearest int32.
func ceilToInt(f float64) int32 {
	return int32(math.Ceil(f))
}

// modI returns a % b with a non-negative result when b > 0.
func modI(a, b int32) int32 {
	if b <= 0 {
		return a
	}
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// clampAngle normalizes an angle into [0, 360).
func clampAngle(deg float64) float64 {
	deg = math.Mod(deg, 360)
	if deg < 0 {
		deg += 360
	}
	return deg
}

// lerpFloat64 linearly interpolates between a and b by t in [0,1].
func lerpFloat64(a, b, t float64) float64 {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return a + (b-a)*t
}

// scaleDown scales a value down by factor while keeping it >= 1.
func scaleDown(v, factor int32) int32 {
	if factor <= 1 {
		return v
	}
	n := int64(v) * int64(100) / int64(factor)
	if n < 1 {
		return 1
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n) / 100
}

// roundedScale scales v by ratio and rounds to nearest int32, with bounds.
func roundedScale(v int32, ratio float64) int32 {
	f := float64(v) * ratio
	if f < 1 {
		return 0
	}
	if f > float64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int32(math.Round(f))
}

// splitLines splits text into non-empty trimmed lines.
func splitLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// uniqueStrings returns a deduplicated copy of strs, preserving order.
func uniqueStrings(strs []string) []string {
	seen := make(map[string]struct{}, len(strs))
	var out []string
	for _, s := range strs {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// joinNonEmpty joins the string slice, skipping empty entries.
func joinNonEmpty(strs []string, sep string) string {
	var b strings.Builder
	for i, s := range strs {
		if s == "" {
			continue
		}
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s)
	}
	return b.String()
}

// safeLower returns strings.ToLower(s) with a guard against huge input.
func safeLower(s string) string {
	if len(s) > 1024*1024 {
		s = s[:1024*1024]
	}
	return strings.ToLower(s)
}

// safeTrimSpace returns strings.TrimSpace(s) with a guard against huge input.
func safeTrimSpace(s string) string {
	if len(s) > 10*1024*1024 {
		s = s[:10*1024*1024]
	}
	return strings.TrimSpace(s)
}

// httpsGetBytes fetches url with the given timeout and returns the body bytes.
func httpsGetBytes(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchURLMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > fetchURLMaxBytes {
		return nil, fmt.Errorf("response is larger than %d MiB", fetchURLMaxBytes>>20)
	}
	return data, nil
}

// readFileTrimmed reads path and returns its trimmed content, or "".
func readFileTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ensureParentDir ensures the parent directory of path exists.
func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

// writeFile writes data to path with 0o600 permissions.
func writeFile(path string, data []byte) error {
	if err := ensureParentDir(path); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// readJSONFile reads and unmarshals a JSON file.
func readJSONFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSONFile marshals v to JSON and writes it to path with 0o600.
func writeJSONFile(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "\t")
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ensureDir ensures dir exists.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// removePath removes path (file or empty dir).
func removePath(path string) error {
	return os.Remove(path)
}

// removeAll removes path and everything under it.
func removeAll(path string) error {
	return os.RemoveAll(path)
}

// copyRecursive copies srcDir into dstDir.
func copyRecursive(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return ensureDir(dest)
		}
		return copyFile(path, dest)
	})
}

// listFiles returns the names of all files (not dirs) in dir.
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// listDirs returns the names of all directories in dir.
func listDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// latestFile returns the most recently modified file in dir matching filter.
func latestFile(dir, filter string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best os.DirEntry
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), strings.ToLower(filter)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !found {
			best = e
			found = true
			continue
		}
		if bestInfo, berr := best.Info(); berr == nil && info.ModTime().After(bestInfo.ModTime()) {
			best = e
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("no file matching %q in %s", filter, dir)
	}
	return filepath.Join(dir, best.Name()), nil
}

// newestModifyTime returns the latest ModTime among entries in dir.
func newestModifyTime(dir string) (time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, err
	}
	var best time.Time
	found := false
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(best) {
			best = info.ModTime()
			found = true
		}
	}
	if !found {
		return time.Time{}, fmt.Errorf("no entries in %s", dir)
	}
	return best, nil
}

// relativeTo returns a path relative to base, or the cleaned path on failure.
func relativeTo(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.Clean(path)
	}
	return rel
}

// nearestExisting walks up from path to the root looking for name.
func nearestExisting(path, name string) string {
	for {
		candidate := filepath.Join(path, name)
		if fileExists(candidate) {
			return candidate
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return ""
}

// isAbsolute reports whether path is absolute for the current OS.
func isAbsolute(path string) bool {
	return filepath.IsAbs(path)
}

// absPath returns the absolute form of path.
func absPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(path)
}

// walkFiles walks root and calls fn for each file (not dir).
func walkFiles(root string, fn func(path string) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return fn(path)
	})
}

// fileSize returns the size of path in bytes, or -1 on error.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

// readFirstLine reads the first line of path, or "".
func readFirstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for i, b := range data {
		if b == '\n' {
			return strings.TrimSpace(string(data[:i]))
		}
	}
	return strings.TrimSpace(string(data))
}
