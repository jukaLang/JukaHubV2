package main

// windows_runtime_install.go — one-click runtime setup for Windows Apps.
//
// A stock TrimUI has no Wine, and asking a handheld user to assemble Wine,
// Box64 and a display stack by hand is unrealistic. This installer fetches the
// current ARM64 Hangover release (ARM64 Wine plus its emulator DLLs), unpacks
// its Debian packages into data/windows-runtime, and writes a small launcher
// wrapper that resolveWine already understands.
//
// It also offers the WestonPack display runtime. WestonPack ships as SquashFS;
// extracting it needs unsquashfs, and mounting it needs a privileged helper.
// JukaHub therefore extracts it when unsquashfs is available and otherwise
// leaves the downloaded image for PortMaster, rather than pretending it can
// mount it.

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
)

const (
	windowsRuntimeReleaseAPI = "https://api.github.com/repos/AndreRH/hangover/releases/latest"
	windowsWestonRuntimeURL  = "https://raw.githubusercontent.com/PortsMaster/PortMaster-New/main/runtimes/weston_pkg_0.2.aarch64.squashfs"
	windowsDXVKReleaseAPI    = "https://api.github.com/repos/doitsujin/dxvk/releases/latest"
	windowsRuntimeMaxBytes   = int64(2 << 30) // 2 GiB safety limit
	windowsRuntimeMaxFiles   = 100000
)

var (
	windowsRuntimeClient  = httpClientLong
	windowsRuntimeRelease = windowsRuntimeReleaseAPI
	windowsRuntimeVerify  = verifyWineRuntime
	windowsRuntimeState   struct {
		mu      sync.Mutex
		running bool
		target  string // "wine" or "weston", while running
		status  string
		lastErr string
	}
)

type hangoverAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type hangoverRelease struct {
	TagName string          `json:"tag_name"`
	Assets  []hangoverAsset `json:"assets"`
}

// selectHangoverAsset picks the ARM64 runtime archive, not the separate DLL
// archive. Distribution names change between releases, so match on the
// architecture and archive suffix and prefer the Debian 12 build used by most
// ARM handhelds.
func selectHangoverAsset(assets []hangoverAsset) (string, string, error) {
	var fallbackName, fallbackURL string
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if !strings.Contains(lower, "arm64") || strings.Contains(lower, "dll") {
			continue
		}
		if !strings.HasSuffix(lower, ".tar") && !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
			continue
		}
		if fallbackURL == "" {
			fallbackName, fallbackURL = a.Name, a.BrowserDownloadURL
		}
		if strings.Contains(lower, "debian12") || strings.Contains(lower, "bookworm") {
			return a.Name, a.BrowserDownloadURL, nil
		}
	}
	if fallbackURL != "" {
		return fallbackName, fallbackURL, nil
	}
	return "", "", errors.New("the latest Hangover release has no ARM64 runtime archive")
}

func setWindowsRuntimeStatus(status, lastErr string, running bool) {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.status = status
	windowsRuntimeState.lastErr = lastErr
	windowsRuntimeState.running = running
	windowsRuntimeState.mu.Unlock()
}

// windowsRuntimeInstallLine is shown on the setup page so a background install
// has visible progress and a failure is not a toast the user can miss.
func windowsRuntimeInstallLine() string {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.status == "" {
		return ""
	}
	if windowsRuntimeState.running {
		return "Installer: " + windowsRuntimeState.status
	}
	if windowsRuntimeState.lastErr != "" {
		return "Installer: " + windowsRuntimeState.lastErr
	}
	return "Installer: " + windowsRuntimeState.status
}

func windowsRuntimeInstalling() bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return windowsRuntimeState.running
}

// windowsRuntimeInstallTarget names the runtime being installed so the setup
// page only marks the row the user activated as "installing".
func windowsRuntimeInstallTarget() string {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return windowsRuntimeState.target
}

// windowsWineInstallFailed reports whether the last Wine install ended with an
// error, so the setup page can label the row "failed" rather than "missing".
func windowsWineInstallFailed() bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return !windowsRuntimeState.running && windowsRuntimeState.status == "Wine install failed"
}

// windowsWestonInstallFailed is the display-runtime counterpart.
func windowsWestonInstallFailed() bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return !windowsRuntimeState.running && windowsRuntimeState.status == "WestonPack not installed"
}

// beginWindowsRuntimeInstall marks the target runtime as installing. It
// returns false when another install already owns the slot.
func beginWindowsRuntimeInstall(target, status string) bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.running {
		return false
	}
	windowsRuntimeState.running = true
	windowsRuntimeState.target = target
	windowsRuntimeState.status = status
	windowsRuntimeState.lastErr = ""
	return true
}

func endWindowsRuntimeInstall() {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.running = false
	windowsRuntimeState.target = ""
	windowsRuntimeState.mu.Unlock()
}

// setWindowsRuntimeInstallTarget moves the progress line to the component the
// provisioning chain is working on now.
func setWindowsRuntimeInstallTarget(target string) {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.target = target
	windowsRuntimeState.mu.Unlock()
}

// windowsDXVKInstallFailed reports whether the last DXVK install ended with an
// error, so the setup page can say so on the row.
func windowsDXVKInstallFailed() bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return !windowsRuntimeState.running && windowsRuntimeState.status == "DXVK install failed"
}

// startWindowsRuntimeInstall launches the Wine install in the background; the
// UI keeps rendering and the setup page shows the current step.
func startWindowsRuntimeInstall() error {
	if IsWindows() {
		return errors.New("Windows runs .exe files directly; the Wine runtime is only needed on the handheld")
	}
	if !beginWindowsRuntimeInstall("wine", "contacting the Hangover release") {
		return errors.New("a runtime install is already running")
	}

	go func() {
		releaseURL := strings.TrimSpace(os.Getenv("JUKAHUB_WINE_RUNTIME_URL"))
		if releaseURL == "" {
			releaseURL = windowsRuntimeRelease
		}
		err := installWineRuntime(releaseURL)
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
		if err != nil {
			setWindowsRuntimeStatus("Wine install failed", err.Error(), false)
			ShowToast("Wine install failed: "+err.Error(), ToastKindError)
			return
		}
		setWindowsRuntimeStatus("Wine installed", "", false)
		ShowToast("Wine installed — open an .exe to run it", ToastKindSuccess)
	}()
	return nil
}

// installWineRuntime downloads and installs the Hangover archive. releaseURL
// may be empty to resolve the latest release from GitHub; tests pass a local
// URL directly.
func installWineRuntime(releaseURL string) error {
	if releaseURL == "" {
		releaseURL = windowsRuntimeRelease
	}
	assetURL := releaseURL
	if strings.HasSuffix(strings.ToLower(releaseURL), "/latest") || strings.Contains(releaseURL, "api.github.com") {
		setWindowsRuntimeStatus("checking the latest Hangover release", "", true)
		asset, err := latestHangoverAsset(releaseURL)
		if err != nil {
			return err
		}
		assetURL = asset
	}
	if !strings.HasPrefix(assetURL, "http://") && !strings.HasPrefix(assetURL, "https://") {
		return fmt.Errorf("runtime URL must be http(s): %s", assetURL)
	}

	base := windowsRuntimeDir()
	if err := os.MkdirAll(filepath.Join(base, "downloads"), 0o755); err != nil {
		return fmt.Errorf("create runtime folder: %w", err)
	}
	name := filepath.Base(assetURL)
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if name == "" || name == "." || name == "/" {
		name = "hangover.tar"
	}
	archive := filepath.Join(base, "downloads", name)
	setWindowsRuntimeStatus("downloading "+name, "", true)
	if err := downloadRuntimeFile(assetURL, archive); err != nil {
		return err
	}

	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	setWindowsRuntimeStatus("extracting "+name, "", true)
	if err := extractRuntimeArchive(archive, staging); err != nil {
		return err
	}
	// Hangover ships the runtime as Debian packages inside the release
	// tarball, so unwrap those before looking for wine.
	tree, err := unpackWineRelease(runtimeArchiveRoot(staging))
	if err != nil {
		return err
	}

	binary, prefix, err := locateWineBinary(tree)
	if err != nil {
		return err
	}
	payload := filepath.Join(base, "hangover")
	if filepath.Clean(tree) == filepath.Clean(payload) {
		return errors.New("the runtime archive has an unexpected layout")
	}
	if err := os.RemoveAll(payload); err != nil {
		return fmt.Errorf("replace previous runtime: %w", err)
	}
	if err := os.Rename(tree, payload); err != nil {
		// A cross-device staging folder is possible on some data layouts; fall
		// back to a copy so the install still completes.
		if copyErr := copyTree(tree, payload); copyErr != nil {
			return fmt.Errorf("install runtime: %w (copy fallback: %v)", err, copyErr)
		}
	}
	rel, err := filepath.Rel(tree, binary)
	if err != nil {
		return err
	}
	installedBinary := filepath.Join(payload, rel)
	installedPrefix := filepath.Join(payload, prefix)
	if err := writeWineWrapper(filepath.Join(base, "bin", "wine"), installedBinary, installedPrefix); err != nil {
		return err
	}
	setWindowsRuntimeStatus("checking Wine", "", true)
	if windowsRuntimeVerify != nil {
		if err := windowsRuntimeVerify(filepath.Join(base, "bin", "wine")); err != nil {
			return fmt.Errorf("Wine is installed but failed to start: %w", err)
		}
	}
	_ = os.Remove(archive)
	setWindowsRuntimeStatus("Wine installed", "", false)
	return nil
}

func latestHangoverAsset(apiURL string) (string, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	resp, err := windowsRuntimeClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("check Hangover release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("check Hangover release: HTTP %d", resp.StatusCode)
	}
	var release hangoverRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("read Hangover release: %w", err)
	}
	_, assetURL, err := selectHangoverAsset(release.Assets)
	return assetURL, err
}

func downloadRuntimeFile(url, dest string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	resp, err := windowsRuntimeClient.Do(req)
	if err != nil {
		return fmt.Errorf("download runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download runtime: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > windowsRuntimeMaxBytes {
		return fmt.Errorf("runtime archive is too large (%d bytes)", resp.ContentLength)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(resp.Body, windowsRuntimeMaxBytes+1))
	if err != nil {
		return err
	}
	if n > windowsRuntimeMaxBytes {
		return errors.New("runtime archive exceeds the size limit")
	}
	return out.Close()
}

// extractRuntimeArchive supports the uncompressed .tar releases used by
// Hangover, plus .tar.gz/.tgz and .zip for user-provided builds. Entries are
// confined to dest; symlinks are only accepted when their target stays inside
// the extraction root.
func extractRuntimeArchive(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractRuntimeZip(archive, dest)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		return extractRuntimeTar(gz, dest)
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractRuntimeTar(f, dest)
	default:
		return fmt.Errorf("unsupported runtime archive %q (use .tar, .tar.gz or .zip; set JUKAHUB_WINE_RUNTIME_URL for a custom build)", filepath.Base(archive))
	}
}

// unpackWineRelease returns a directory that contains a wine binary. A plain
// Wine tree is used as-is; the Hangover release instead ships Debian .deb
// packages, which are unpacked userland into <root>/runtime without dpkg or
// root access.
func unpackWineRelease(root string) (string, error) {
	if _, _, err := locateWineBinary(root); err == nil {
		return root, nil
	}
	var debs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".deb") {
			debs = append(debs, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(debs) == 0 {
		return "", errors.New("the runtime archive contains neither a wine binary nor .deb packages")
	}
	dest := filepath.Join(root, "runtime")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	for _, deb := range debs {
		setWindowsRuntimeStatus("unpacking "+filepath.Base(deb), "", true)
		if err := extractDebData(deb, dest); err != nil {
			return "", fmt.Errorf("unpack %s: %w", filepath.Base(deb), err)
		}
	}
	if _, _, err := locateWineBinary(dest); err != nil {
		return "", err
	}
	return dest, nil
}

// extractDebData reads a Debian package's ar container and extracts its
// data.tar member. Only the compression formats that can be decoded in pure Go
// are accepted; the bookworm/xz release uses data.tar.xz.
func extractDebData(deb, dest string) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()

	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return errors.New("not a Debian package (bad ar header)")
	}
	for {
		header := make([]byte, 60)
		if _, err := io.ReadFull(f, header); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid ar member %q", name)
		}
		if !strings.HasPrefix(name, "data.tar") {
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return err
			}
			continue
		}
		member := io.LimitReader(f, size)
		var data io.Reader = member
		switch name {
		case "data.tar":
		case "data.tar.xz":
			xr, err := xz.NewReader(member)
			if err != nil {
				return fmt.Errorf("read data.tar.xz: %w", err)
			}
			data = xr
		case "data.tar.gz":
			gr, err := gzip.NewReader(member)
			if err != nil {
				return fmt.Errorf("read data.tar.gz: %w", err)
			}
			defer gr.Close()
			data = gr
		case "data.tar.bz2":
			data = bzip2.NewReader(member)
		default:
			return fmt.Errorf("unsupported package payload %s", name)
		}
		return extractRuntimeTar(data, dest)
	}
	return errors.New("package has no data.tar")
}

// runtimeArchiveRoot drops a single wrapper directory. Releases usually ship
// as hangover/bin, hangover/lib, ...; installing the inner tree keeps the
// runtime at data/windows-runtime/hangover/bin/wine instead of a doubled
// hangover/hangover path.
func runtimeArchiveRoot(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dir
	}
	entry := entries[0]
	if entry.Type()&os.ModeSymlink != 0 {
		return dir
	}
	return filepath.Join(dir, entry.Name())
}

func extractRuntimeTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	count := 0
	var links []archiveLink
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return materializeArchiveLinks(dest, links)
		}
		if err != nil {
			return err
		}
		count++
		if count > windowsRuntimeMaxFiles {
			return errors.New("runtime archive has too many entries")
		}
		target, err := runtimeArchivePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, windowsRuntimeMaxBytes)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			resolved, err := archiveLinkTarget(dest, target, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(filepath.FromSlash(strings.TrimSpace(hdr.Linkname)), target); err != nil {
				// SD cards are usually FAT/exFAT, where symlinks are not
				// supported. Materialise the link once the archive has been
				// unpacked, when its target exists.
				links = append(links, archiveLink{target: target, source: resolved})
			}
		case tar.TypeLink:
			source, err := runtimeArchivePath(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(source, target); err != nil {
				return err
			}
		}
	}
}

func extractRuntimeZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > windowsRuntimeMaxFiles {
		return errors.New("runtime archive has too many entries")
	}
	var links []archiveLink
	for _, f := range zr.File {
		target, err := runtimeArchivePath(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			link, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if readErr != nil {
				return readErr
			}
			linkName := string(link)
			resolved, err := archiveLinkTarget(dest, target, linkName)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(filepath.FromSlash(strings.TrimSpace(linkName)), target); err != nil {
				links = append(links, archiveLink{target: target, source: resolved})
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, windowsRuntimeMaxBytes))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return materializeArchiveLinks(dest, links)
}

func runtimeArchivePath(dest, name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	if !isPathWithin(target, dest) {
		return "", fmt.Errorf("archive entry %q escapes the runtime folder", name)
	}
	return target, nil
}

// archiveLink resolves a symlink's source and rejects targets that leave the
// extraction root, whether or not the filesystem can create the link itself.
func archiveLinkTarget(dest, target, link string) (string, error) {
	link = strings.ReplaceAll(strings.TrimSpace(link), "\\", "/")
	if link == "" || strings.HasPrefix(link, "/") {
		return "", fmt.Errorf("unsafe archive symlink %q", link)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(link)))
	if !isPathWithin(resolved, dest) {
		return "", fmt.Errorf("archive symlink %q escapes the runtime folder", link)
	}
	return resolved, nil
}

type archiveLink struct {
	target string // where the link should be
	source string // what it points at, already validated as inside dest
}

// materializeArchiveLinks copies link targets for filesystems without symlink
// support. Wine archives use them for the winecfg/notepad aliases; losing them
// must not abort an otherwise valid install.
func materializeArchiveLinks(dest string, links []archiveLink) error {
	for pass := 0; pass < 4 && len(links) > 0; pass++ {
		var pending []archiveLink
		for _, link := range links {
			if !isPathWithin(link.source, dest) {
				return errors.New("archive symlink targets an unexpected path")
			}
			info, err := os.Stat(link.source)
			if err != nil || !info.Mode().IsRegular() {
				pending = append(pending, link)
				continue
			}
			if err := copyArchiveFile(link.source, link.target, info.Mode().Perm()); err != nil {
				return err
			}
		}
		links = pending
	}
	if len(links) > 0 {
		return fmt.Errorf("could not create %d archive link(s) on this filesystem", len(links))
	}
	return nil
}

func copyArchiveFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// locateWineBinary finds the wine launcher and the prefix whose lib folders
// must be on LD_LIBRARY_PATH. The returned prefix is relative to staging.
func locateWineBinary(staging string) (binary, prefix string, err error) {
	var candidates []string
	_ = filepath.WalkDir(staging, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "wine" || name == "wine64" {
			candidates = append(candidates, path)
		}
		return nil
	})
	if len(candidates) == 0 {
		return "", "", errors.New("the runtime archive does not contain a wine binary")
	}
	best := candidates[0]
	for _, c := range candidates {
		if strings.EqualFold(filepath.Base(c), "wine") {
			best = c
			break
		}
	}
	binDir := filepath.Dir(best)
	if strings.EqualFold(filepath.Base(binDir), "bin") {
		prefix = filepath.Dir(binDir)
	} else {
		prefix = binDir
	}
	relPrefix, err := filepath.Rel(staging, prefix)
	if err != nil {
		return "", "", err
	}
	return best, relPrefix, nil
}

// writeWineWrapper creates the stable data/windows-runtime/bin/wine entry point
// that resolveWine looks for, pointing at the extracted Hangover tree.
func writeWineWrapper(wrapper, binary, prefix string) error {
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\n" +
		"# Generated by JukaHub's Windows runtime installer.\n" +
		"RUNTIME_PREFIX=" + quote(prefix) + "\n" +
		"export LD_LIBRARY_PATH=\"$RUNTIME_PREFIX/lib:$RUNTIME_PREFIX/lib64:$RUNTIME_PREFIX/lib/aarch64-linux-gnu:$RUNTIME_PREFIX/usr/lib:$RUNTIME_PREFIX/usr/lib/aarch64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\"\n" +
		"export WINEDLLPATH=\"$RUNTIME_PREFIX/lib/wine:$RUNTIME_PREFIX/lib64/wine:$RUNTIME_PREFIX/usr/lib/wine${WINEDLLPATH:+:$WINEDLLPATH}\"\n" +
		"exec " + quote(binary) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		return err
	}
	return os.Chmod(wrapper, 0o755)
}

func verifyWineRuntime(wrapper string) error {
	cmd := exec.Command(wrapper, "--version")
	done := make(chan error, 1)
	var output []byte
	go func() {
		out, err := cmd.CombinedOutput()
		output = out
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	case <-time.After(25 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return errors.New("wine --version timed out")
	}
}

// copyTree is the fallback when staging and the runtime folder are on different
// filesystems, where os.Rename cannot move a directory.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// ── Display runtime ──────────────────────────────────────────────────────

// installWestonRuntime downloads WestonPack. The official package is SquashFS,
// which cannot be mounted without privileges; if unsquashfs exists we extract
// it userland, otherwise the archive is kept for PortMaster and the caller gets
// a clear explanation.
func installWestonRuntime() error {
	if IsWindows() {
		return errors.New("WestonPack is only used by the handheld/Linux build")
	}
	base := windowsRuntimeDir()
	installDir := filepath.Join(base, "weston")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(base, "weston_pkg_0.2.aarch64.squashfs")
	if _, err := os.Stat(archive); err != nil {
		setWindowsRuntimeStatus("downloading WestonPack", "", true)
		if err := downloadRuntimeFile(windowsWestonRuntimeURL, archive); err != nil {
			return fmt.Errorf("WestonPack download: %w", err)
		}
	}
	unsquashfs, err := exec.LookPath("unsquashfs")
	if err != nil {
		setWindowsRuntimeStatus("WestonPack downloaded; install it with PortMaster", "", true)
		return errors.New("WestonPack downloaded to " + archive + ", but unsquashfs is not installed; use PortMaster to install the runtime, or set JUKAHUB_WESTON to an existing westonwrap.sh")
	}
	setWindowsRuntimeStatus("extracting WestonPack", "", true)
	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	cmd := exec.Command(unsquashfs, "-f", "-d", staging, archive)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract WestonPack: %w: %s", err, strings.TrimSpace(string(out)))
	}
	wrapper, err := findWestonWrapper(staging)
	if err != nil {
		return err
	}
	root := filepath.Dir(wrapper)
	if filepath.Clean(root) != filepath.Clean(installDir) {
		// Move the extracted tree into the stable location resolveWestonPack
		// checks: data/windows-runtime/weston/westonwrap.sh. A relay script
		// would break westonwrap.sh's lookups relative to its own directory.
		if err := os.RemoveAll(installDir); err != nil {
			return err
		}
		if err := os.Rename(root, installDir); err != nil {
			return err
		}
		wrapper = filepath.Join(installDir, filepath.Base(wrapper))
	} else {
		wrapper = filepath.Join(installDir, filepath.Base(wrapper))
	}
	_ = os.Chmod(wrapper, 0o755)
	setWindowsRuntimeStatus("WestonPack installed", "", false)
	return nil
}

func findWestonWrapper(root string) (string, error) {
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "westonwrap.sh") {
			found = path
			return io.EOF
		}
		return nil
	})
	if found == "" {
		return "", errors.New("WestonPack archive does not contain westonwrap.sh")
	}
	return found, nil
}

// ── DXVK ─────────────────────────────────────────────────────────────────

// selectDXVKAsset picks the release tarball that carries the D3D DLLs. Hash
// files and source archives are skipped: only the binary .tar.gz (or .zip)
// contains x64/d3d11.dll, which is what makes DXVK usable.
func selectDXVKAsset(assets []hangoverAsset) (string, string, error) {
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, "sha256") || strings.Contains(lower, ".asc") || strings.Contains(lower, "source") {
			continue
		}
		if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") && !strings.HasSuffix(lower, ".zip") {
			continue
		}
		return a.Name, a.BrowserDownloadURL, nil
	}
	return "", "", errors.New("the latest DXVK release has no .tar.gz binary archive")
}

func latestDXVKAsset(apiURL string) (string, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := windowsRuntimeClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("check DXVK release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("check DXVK release: HTTP %d", resp.StatusCode)
	}
	var release hangoverRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("read DXVK release: %w", err)
	}
	_, assetURL, err := selectDXVKAsset(release.Assets)
	return assetURL, err
}

// findDXVKTree locates the directory that holds x64/d3d11.dll inside an
// extracted release. Upstream keeps the DLLs under <version>/, but a flat
// archive (d3d11.dll at the root) is accepted too.
func findDXVKTree(root string) string {
	best := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if info, err := os.Stat(filepath.Join(path, "x64", "d3d11.dll")); err == nil && info.Size() > 0 {
			best = path
			return io.EOF
		}
		return nil
	})
	if best != "" {
		return best
	}
	if info, err := os.Stat(filepath.Join(root, "d3d11.dll")); err == nil && info.Size() > 0 {
		return root
	}
	return ""
}

// installDXVKRuntime downloads the latest DXVK and installs its DLL trees in
// data/windows-runtime/dxvk, which resolveDXVK already checks. DXVK is only
// useful with a Vulkan loader, so installing it without one is refused rather
// than leaving a directory that silently changes nothing.
func installDXVKRuntime() error {
	if windowsResolveVulkan() == "" {
		return errors.New("DXVK needs a Vulkan loader (libvulkan.so.1); install a Vulkan driver first, or set JUKAHUB_VULKAN")
	}
	assetURL, err := latestDXVKAsset(windowsDXVKRelease)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(assetURL, "http://") && !strings.HasPrefix(assetURL, "https://") {
		return fmt.Errorf("DXVK URL must be http(s): %s", assetURL)
	}
	base := windowsRuntimeDir()
	downloads := filepath.Join(base, "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return fmt.Errorf("create runtime folder: %w", err)
	}
	name := filepath.Base(assetURL)
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if name == "" || name == "." || name == "/" {
		name = "dxvk.tar.gz"
	}
	archive := filepath.Join(downloads, name)
	setWindowsRuntimeStatus("downloading "+name, "", true)
	if err := downloadRuntimeFile(assetURL, archive); err != nil {
		return err
	}
	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	setWindowsRuntimeStatus("extracting "+name, "", true)
	if err := extractRuntimeArchive(archive, staging); err != nil {
		return err
	}
	tree := findDXVKTree(runtimeArchiveRoot(staging))
	if tree == "" {
		return errors.New("the DXVK archive contains no x64/d3d11.dll")
	}
	installDir := filepath.Join(base, "dxvk")
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	for _, dir := range []string{"x64", "x32"} {
		src := filepath.Join(tree, dir)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			if err := copyTree(src, filepath.Join(installDir, dir)); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(filepath.Join(tree, "x64")); err != nil {
		// A flat archive has the DLLs at its root instead of inside x64/.
		entries, readErr := os.ReadDir(tree)
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
				continue
			}
			if err := copyArchiveFile(filepath.Join(tree, entry.Name()), filepath.Join(installDir, entry.Name()), 0o644); err != nil {
				return err
			}
		}
	}
	if !dxvkBuild(installDir) {
		return fmt.Errorf("the DXVK archive did not install a usable build in %s", installDir)
	}
	setWindowsRuntimeStatus("DXVK installed", "", false)
	return nil
}

// ── Provisioning ─────────────────────────────────────────────────────────

// windowsRuntimeStep is one runtime component a launch may need.
type windowsRuntimeStep int

const (
	windowsStepWine windowsRuntimeStep = iota
	windowsStepDisplay
	windowsStepDXVK
)

// key is the value the install state and the setup rows use for a step.
func (s windowsRuntimeStep) key() string {
	switch s {
	case windowsStepWine:
		return "wine"
	case windowsStepDisplay:
		return "weston"
	default:
		return "dxvk"
	}
}

func (s windowsRuntimeStep) label() string {
	switch s {
	case windowsStepWine:
		return "Wine"
	case windowsStepDisplay:
		return "WestonPack"
	default:
		return "DXVK"
	}
}

// windowsRuntimePlan lists what a Wine launch still needs, in install order:
// the Wine runtime first, then the display bridge, then DXVK (only when a
// Vulkan loader makes it usable). An empty plan means the runtime is ready.
func windowsRuntimePlan(status windowsRuntimeStatus) []windowsRuntimeStep {
	var plan []windowsRuntimeStep
	if strings.TrimSpace(status.Wine) == "" {
		plan = append(plan, windowsStepWine)
	}
	if strings.TrimSpace(status.Display) == "" {
		plan = append(plan, windowsStepDisplay)
	}
	if status.Vulkan && strings.TrimSpace(status.DXVK) == "" {
		plan = append(plan, windowsStepDXVK)
	}
	return plan
}

// windowsPlanLabel names the missing pieces for the setup-page notice.
func windowsPlanLabel(plan []windowsRuntimeStep) string {
	names := make([]string, 0, len(plan))
	for _, step := range plan {
		names = append(names, step.label())
	}
	return strings.Join(names, " and ")
}

// The install steps and the status lookup are variables so the provisioning
// chain can be tested without a network or a 300 MB download.
var (
	windowsRuntimeStatusFor   = currentWindowsRuntimeStatus
	windowsInstallWineFn      = func() error { return installWineRuntime("") }
	windowsInstallDisplayFn   = installWestonRuntime
	windowsInstallDXVKFn      = installDXVKRuntime
	windowsLaunchAfterInstall = launchWindowsApp
	windowsResolveVulkan      = resolveVulkan
	windowsDXVKRelease        = windowsDXVKReleaseAPI
)

// startWindowsProvisioning installs every missing piece in the background and
// launches pending when the last one succeeds. It returns an error when an
// install is already running or there is nothing to do for pending.
func startWindowsProvisioning(pending *windowsAppEntry) error {
	status := windowsRuntimeStatusFor()
	plan := windowsRuntimePlan(status)
	if len(plan) == 0 {
		if pending == nil {
			return errors.New("the Windows runtime is already installed")
		}
		return windowsLaunchAfterInstall(*pending)
	}
	if !beginWindowsRuntimeInstall(plan[0].key(), "checking the Windows runtime") {
		return errors.New("a runtime install is already running")
	}
	if pending != nil {
		copy := *pending
		pending = &copy
	}
	go windowsProvisionLoop(pending)
	return nil
}

// windowsProvisionLoop re-checks what is missing before every step, so an
// install that also provides a later piece (or a component someone added by
// hand meanwhile) is not installed twice. A step that reports success but
// leaves its component missing stops the chain instead of looping forever.
func windowsProvisionLoop(pending *windowsAppEntry) {
	defer func() {
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
	}()
	attempted := map[windowsRuntimeStep]bool{}
	for {
		status := windowsRuntimeStatusFor()
		plan := windowsRuntimePlan(status)
		if len(plan) == 0 {
			break
		}
		step := plan[0]
		if attempted[step] {
			setWindowsRuntimeStatus(step.label()+" not installed", "still missing after install", false)
			ShowToast(step.label()+" is still missing after installing it", ToastKindError)
			return
		}
		attempted[step] = true
		setWindowsRuntimeInstallTarget(step.key())
		setWindowsRuntimeStatus("installing "+step.label(), "", true)
		var err error
		switch step {
		case windowsStepWine:
			err = windowsInstallWineFn()
		case windowsStepDisplay:
			err = windowsInstallDisplayFn()
		case windowsStepDXVK:
			err = windowsInstallDXVKFn()
		}
		if err != nil {
			setWindowsRuntimeStatus(step.label()+" install failed", err.Error(), false)
			ShowToast(step.label()+" install failed: "+err.Error(), ToastKindError)
			return
		}
	}
	setWindowsRuntimeStatus("Windows runtime ready", "", false)
	invalidateWindowsAppsCache()
	if pending == nil {
		ShowToast("Windows runtime ready", ToastKindSuccess)
		return
	}
	if err := windowsLaunchAfterInstall(*pending); err != nil {
		showLaunchFailureDialog(pending.Title, err, "")
		return
	}
	ShowToast("Launching "+pending.Title, ToastKindSuccess)
}
