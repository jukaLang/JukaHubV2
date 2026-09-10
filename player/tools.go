package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// resolveToolURL returns the download URL for the first release asset whose
// name matches one of the wanted strings (case-insensitive).
//
// IMPORTANT: repo must be a trusted GitHub repo, chosen from a small curated list
// in this codebase. Arbitrary user-supplied repo names are not safe here because
// the GitHub API can be abused to probe repo/release metadata and because a future
// code path could be tempted to trust the returned download URL too much.
var allowedToolRepos = map[string]bool{
	"btbn/ffmpeg-builds":  true,
	"yt-dlp/yt-dlp":       true,
	"jukaLang/JukaHubV2":  true,
}

func resolveToolURL(repo, apiToken string, wants ...string) (string, error) {
	if !allowedToolRepos[repo] {
		return "", fmt.Errorf("tool repo %q is not in the approved download list", repo)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	req.Header.Set("User-Agent", "JukaHub-Patch-Tool/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("github api rate limit exceeded (retry after %s)", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api %s: unexpected status %d", apiURL, resp.StatusCode)
	}

	var rel struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if len(rel.Assets) == 0 {
		return "", fmt.Errorf("no assets in %s latest release", repo)
	}
	for _, a := range rel.Assets {
		for _, w := range wants {
			if strings.EqualFold(a.Name, w) {
				return a.BrowserDownloadURL, nil
			}
		}
	}
	return "", fmt.Errorf("no matching asset found in %s latest release", repo)
}

// downloadFile downloads url to dest with a 60s timeout.
// It also enforces a modest size cap and refuses obviously truncated files.
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}
	rate := io.LimitReader(resp.Body, 512<<20+1)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	rw := &io.WriteCloserNW{w: out}
	// Copy with a hard cap, then check whether the body was truncated.
	n, err := io.Copy(rw, rate)
	if err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if n > 512<<20 {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s exceeds 512 MiB limit", url)
	}
	if n < 4096 {
		_ = os.Remove(dest)
		return fmt.Errorf("download %s is suspiciously small (%d bytes)", url, n)
	}
	return nil
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

func extractFilesFromZip(ctx context.Context, archivePath, requiredDir string, wants []string) error {
	// Keep a small, targeted extraction path for bundled tool archives, but
	// route the actual zip extraction through the project's shared safe helper so
	// there is only one zip sanitization/escape path. After extraction, verify
	// that any requested filenames are actually present.
	if ctx == nil {
		ctx = context.Background()
	}
	maxEntries := 256
	maxTotalBytes := int64(1 << 30) // 1 GiB for large ffmpeg zip candidates
	if err := extractZipSafe(ctx, archivePath, requiredDir, maxEntries, maxTotalBytes); err != nil {
		return err
	}
	if len(wants) == 0 {
		return nil
	}
	for _, w := range wants {
		if w == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(requiredDir, filepath.FromSlash(w))); err != nil {
			return fmt.Errorf("expected entry %q not found after extraction", w)
		}
	}
	return nil
}

// extractFFmpegZip was previously a separate ffmpeg-zip extraction path that
// reimplemented zip sanitization. It now routes through extractFilesFromZip via
// extractZipSafe so there is a single safe-zip code path.
func extractFFmpegZip(archivePath, requiredDir string) error {
	return extractFilesFromZip(nil, archivePath, requiredDir, []string{"ffplay.exe", "ffmpeg.exe"})
}

// extractFFmpegTarXz downloads an archive to a temp file and then extracts it
// through the shared safe-zip path. Despite the historical name, this helper does
// not implement real tar/xz extraction; it is intentionally limited to the same
// zip-based extraction the rest of the tool download path uses.
func extractFFmpegTarXz(archivePath, requiredDir string) error {
	return extractFFmpegFromRemoteArchive(archivePath, requiredDir)
}

// unzipFile extracts a user-selected zip into a sibling folder named <src>_unzipped,
// refusing any entry whose cleaned path escapes the destination.
func unzipFile(src string) error {
	ctx := context.Background()
	dest := strings.TrimSuffix(src, filepath.Ext(src)) + "_unzipped"
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return extractZipSafe(ctx, src, dest, 1024, 1<<30)
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
		return "", fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// httpGetText fetches url with timeout and returns the response body text, or "".
func httpGetText(url string, timeout time.Duration) string {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
	v, err := parsePosInt(text)
	if err != nil {
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

// copyFile copies src to dst with 0o600 permissions for the destination.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
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

// sha256Hex returns the hex-encoded SHA256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
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

// lerpInt32 linearly interpolates between a and b by t in [0,1].
func lerpInt32(a, b, t int32) int32 {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	diff := b - a
	return a + int32(float64(diff)*float64(t))
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
	return io.ReadAll(resp.Body)
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
		if !found || info.ModTime().After(best.Info().ModTime()) {
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

// resolvePath returns the cleaned absolute path for path.
func resolvePath(path string) (string, error) {
	p, err := absPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(p), nil
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
