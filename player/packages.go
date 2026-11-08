package main

// packages.go — the JukaLang package registry: what a package is, where it
// installs, and the install itself.
//
// The store screen that lists and manages packages lives in the pkg*.go files
// (pkgcatalog.go: the model; pkgstate.go: state and background work; pkgui.go:
// drawing; pkginput.go: input). This file keeps the parts every one of them
// leans on and that other code calls: the Package type, the install folder,
// the download-and-unpack, and the removal.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/ttf"
)

// packagesURL points to the upstream JukaLang package registry.
const packagesURL = "https://raw.githubusercontent.com/jukaLang/Packages/refs/heads/main/packages.json"

// Package is a single entry in the JukaLang package registry.
type Package struct {
	Name         string   `json:"name"`
	Author       string   `json:"author"`
	Description  string   `json:"description"`
	Date         string   `json:"date"`
	Version      string   `json:"version"`
	Dependencies []string `json:"dependencies"`
	MainFilename string   `json:"mainfilename"`
	Download     string   `json:"download"`
	Device       []string `json:"device"`
}

// pkgInstallDirOverride points the package folder somewhere else; tests use a
// temporary directory so they never touch the real install.
var pkgInstallDirOverride string

// packageInstallDir returns the directory where downloaded packages are stored.
func packageInstallDir() string {
	dir := pkgInstallDirOverride
	if dir == "" {
		dir = filepath.Join(mustExecutableDir(), "packages")
	}
	os.MkdirAll(dir, 0755)
	return dir
}

// installedPackages is the set of package folders on the card, keyed by
// folder name. The store's background scan (pkgLoadLocal) replaces it; an
// install adds to it and a removal takes from it.
var (
	installedPackages   = make(map[string]bool)
	installedPackagesMu sync.Mutex
)

func init() {
	// Scan existing installed packages on startup.
	refreshInstalledPackages()
}

// refreshInstalledPackages rescans the install directory into the in-memory
// set. It is exact: a folder deleted behind the app's back leaves the set too.
func refreshInstalledPackages() {
	set := pkgScanInstalled()
	installedPackagesMu.Lock()
	installedPackages = set
	installedPackagesMu.Unlock()
}

// packageDirName is the directory a package installs into. The sanitising lives
// in one place so the install path, the installed check and the store's cards
// can never disagree about which directory a package means. Every path
// separator and every character Windows refuses become "_", and a name that
// would be "." or ".." — or hide among the store's own dot-files — gets a
// leading "_", so a registry entry can never name a folder outside the
// packages folder.
func packageDirName(name string) string {
	safeName := strings.ReplaceAll(name, " ", "_")
	safeName = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, safeName)
	if safeName == "" || strings.HasPrefix(safeName, ".") {
		safeName = "_" + safeName
	}
	return safeName
}

// downloadAndInstallPackage downloads a package zip and extracts it. It is
// the install the store's queue runs; the result is reported through the
// shared installer card (installbar.go) and a toast.
func downloadAndInstallPackage(config *Config, p Package) {
	pkgInstall(config, p)
}

// pkgInstall downloads a package and unpacks it into its folder, reporting
// through the installer card, and says whether it worked.
//
// It publishes through the shared installer card instead of the generic scene
// spinner: the card — and the activity strip that echoes it on every scene —
// names the package, shows the download's progress and its phases, and
// reports the outcome once. The download is tied to the card's run token, so
// the strip's X on this package's chip cancels the transfer.
func pkgInstall(config *Config, p Package) bool {
	// config stays unused: the installer card is global. The parameter is kept
	// so the call sites keep their shape.
	_ = config
	installBarBeginLabelled(p.Name, "Installing "+p.Name)

	if p.Download == "" {
		installBarFail("No download URL for " + p.Name)
		showToast("No download URL for "+p.Name, ToastError())
		return false
	}

	// The package's directory is what marks it installed, so it is created only
	// once the download has arrived: made up front, a failed fetch left an
	// empty folder that the list then showed as installed.
	safeName := packageDirName(p.Name)
	pkgDir := filepath.Join(packageInstallDir(), safeName)

	installBarPhase(installPhaseDownload)
	ctx := installBarRunContext()
	raw, err := pkgDownload(ctx, p.Download)
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled from the card or the strip: the user already knows.
			return false
		}
		log.Printf("packages: download %s: %v", p.Name, err)
		installBarFail("Download failed")
		showToast("Download failed — "+plainNetworkError(err), ToastError())
		return false
	}

	installBarPhase(installPhaseExtract)
	installBarResetProgress()

	_, statErr := os.Stat(pkgDir)
	fresh := os.IsNotExist(statErr)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		installBarFail("Save failed")
		showToast("Save failed: "+err.Error(), ToastError())
		return false
	}

	// Save as zip.
	zipPath := filepath.Join(pkgDir, safeName+".zip")
	if err := os.WriteFile(zipPath, raw, 0644); err != nil {
		if fresh {
			_ = os.RemoveAll(pkgDir)
		}
		installBarFail("Save failed")
		showToast("Save failed: "+err.Error(), ToastError())
		return false
	}

	// Extract it when it is a zip file.
	if strings.HasSuffix(strings.ToLower(p.Download), ".zip") || len(raw) > 4 && raw[0] == 'P' && raw[1] == 'K' {
		if err := unzipToDir(ctx, zipPath, pkgDir); err != nil {
			log.Printf("packages: unzip error: %v", err)
			// A package that did not unpack is not installed. A fresh folder
			// goes; an earlier install it was meant to update stays.
			if fresh {
				_ = os.RemoveAll(pkgDir)
			} else {
				_ = os.Remove(zipPath)
			}
			installBarFail("Could not unpack")
			showToast("Could not unpack "+p.Name+": "+err.Error(), ToastError())
			return false
		}
		os.Remove(zipPath) // Remove the zip after extraction
	}

	installedPackagesMu.Lock()
	installedPackages[safeName] = true
	installedPackagesMu.Unlock()
	pkgRecordInstalled(p)

	showToast("Installed "+p.Name, ToastSuccess())
	installBarDone()
	return true
}

// pkgDownloadTimeout bounds one package download.
const pkgDownloadTimeout = 3 * time.Minute

// pkgDownload fetches a package with the installer card's byte progress. It
// keeps fetchURL's limits — the same 64 MiB cap and user agent — and refuses
// an error page: a 404 used to be saved and "installed" as the package.
func pkgDownload(ctx context.Context, rawURL string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, pkgDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("the server answered %s", resp.Status)
	}
	total := resp.ContentLength
	if total > fetchURLMaxBytes {
		return nil, fmt.Errorf("the package is larger than %d MiB", fetchURLMaxBytes>>20)
	}
	var buf bytes.Buffer
	if total > 0 {
		buf.Grow(int(total))
	}
	chunk := make([]byte, 32<<10)
	var done int64
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			done += int64(n)
			if done > fetchURLMaxBytes {
				return nil, fmt.Errorf("the package is larger than %d MiB", fetchURLMaxBytes>>20)
			}
			installBarProgress(done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return buf.Bytes(), nil
}

// unzipToDir extracts a zip archive to the destination directory through the
// Patch extractor, which refuses entries that escape destDir, links and
// archives that expand past the cap. It used to be a stub that only logged and
// reported success, after which the caller deleted the zip: every package
// "installed" as an empty folder.
func unzipToDir(ctx context.Context, zipPath, destDir string) error {
	return extractZipSafe(ctx, zipPath, destDir, patchMaxEntries, 1<<30)
}

// packageRepository uses the source's real host, and only labels GitHub URLs
// as repositories when they include an owner and repo. Other web downloads
// remain selectable without pretending they are GitHub repositories.
func packageRepository(raw string) (label, target string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	host := strings.ToLower(u.Hostname())
	if host == "api.github.com" && len(parts) >= 3 && parts[0] == "repos" {
		parts = parts[1:]
	}
	if (host == "github.com" || host == "www.github.com" || host == "raw.githubusercontent.com" || host == "api.github.com" || host == "codeload.github.com") && len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		owner, e1 := url.PathUnescape(parts[0])
		repo, e2 := url.PathUnescape(parts[1])
		repo = strings.TrimSuffix(repo, ".git")
		if e1 == nil && e2 == nil && owner != "" && repo != "" && !strings.ContainsAny(owner+repo, "/\\") {
			return owner + "/" + repo, "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo), true
		}
	}
	return u.Hostname() + u.EscapedPath(), u.String(), true
}

// packageInstallHook is the install a press starts. Production queues the real
// download; a test swaps it to record the call instead of touching the
// network.
var packageInstallHook func(config *Config, p Package)

// startPackageInstall queues the install for a press. Installs run one at a
// time (pkgEnqueueInstall), because the installer card shows a single run and
// a second one used to cancel the first.
func startPackageInstall(config *Config, p Package) {
	if packageInstallHook != nil {
		packageInstallHook(config, p)
		return
	}
	pkgEnqueueInstall(config, p)
}

// wrapPackageText breaks text into lines that fit within maxWidth pixels using
// the given font's measured glyph widths.
func wrapPackageText(text string, font *ttf.Font, maxWidth int32) []string {
	var lines []string
	if font == nil {
		return []string{text}
	}
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := ""
		for _, w := range words {
			test := w
			if cur != "" {
				test = cur + " " + w
			}
			wid, _, _ := font.SizeUTF8(test)
			if cur != "" && int32(wid) > maxWidth {
				lines = append(lines, cur)
				cur = w
			} else {
				cur = test
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}
