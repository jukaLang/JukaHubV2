package main

// portmaster_python.go — Python 3 for PortMaster on a TrimUI firmware that has
// none.
//
// PortMaster's menu (pugwash) and its installer (harbourmaster) are Python
// programs started as `#!/usr/bin/env python3`. Stock TrimUI firmware has no
// python3; PortMaster's own TrimUI bootstrap (TRIMUI_EX, which CrossMix
// includes) brings a Python 3.11 build, and without it PortMaster closes the
// moment it opens ("env: can't execute 'python3'").
//
// JukaHub fills that gap with a python-build-standalone CPython for
// aarch64-unknown-linux-gnu: a relocatable build whose floor is glibc 2.17, so
// it runs on the firmware's own C library (2.33) and loads the firmware's SDL2
// the way TRIMUI_EX's Python does. It goes to <data>/portmaster-runtime/python
// and is put first on PATH only for PortMaster, its ports and harbourmaster —
// and only when the firmware's python3 is missing or unusable
// (portmaster_runtime.go). Nothing outside JukaHub's folder changes.
//
// Which build: the latest release's SHA256SUMS lists every file of the release
// with its SHA-256. JukaHub takes 3.11 (the version TRIMUI_EX ships), else 3.12,
// else 3.13, the stripped install-only archive first, downloads exactly that
// file from the same release and accepts it only when its SHA-256 matches the
// line it was chosen from. Both requests stay on GitHub's download hosts
// (allowedPatchHosts) over HTTPS, the same trust PortMaster's own package is
// downloaded with. The test suite, Tk, IDLE, pip and the headers are left out.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	pmPythonSumsURL     = "https://github.com/astral-sh/python-build-standalone/releases/latest/download/SHA256SUMS"
	pmPythonReleaseBase = "https://github.com/astral-sh/python-build-standalone/releases/download/"
	pmPythonMaxSums     = int64(4 << 20)
	pmPythonMaxArchive  = int64(128 << 20)
	pmPythonMaxExpanded = int64(512 << 20)
	pmPythonMaxFiles    = 20000
	pmPythonMarker      = ".jukahub-python.json"
	pmPythonProbeOK     = "jukahub-python-ok"
	// pmPythonProbeCode is what PortMaster needs from a Python: ctypes for
	// SDL2, ssl for its downloads, zipfile and hashlib for the ports.
	pmPythonProbeCode = "import sys; assert sys.version_info >= (3, 8); import ctypes, json, ssl, zipfile, hashlib; print('" + pmPythonProbeOK + "')"
)

// pmPythonAssetRe matches an aarch64 glibc install-only archive in SHA256SUMS:
// minor, patch, release date, stripped.
var pmPythonAssetRe = regexp.MustCompile(`^cpython-3\.(\d+)\.(\d+)\+(\d{8})-aarch64-unknown-linux-gnu-install_only(_stripped)?\.tar\.gz$`)

// pmPythonMinorRank orders the Python versions JukaHub accepts, best first.
var pmPythonMinorRank = map[int]int{11: 0, 12: 1, 13: 2}

// pmPythonAsset is the file chosen from SHA256SUMS.
type pmPythonAsset struct {
	Name, SHA256, Tag string
	Minor, Patch      int
	Stripped          bool
}

// pmPythonPick chooses the archive to install from a SHA256SUMS file. Pure.
func pmPythonPick(sums []byte) (pmPythonAsset, bool) {
	var best pmPythonAsset
	found := false
	better := func(a, b pmPythonAsset) bool {
		if ra, rb := pmPythonMinorRank[a.Minor], pmPythonMinorRank[b.Minor]; ra != rb {
			return ra < rb
		}
		if a.Stripped != b.Stripped {
			return a.Stripped
		}
		return a.Patch > b.Patch
	}
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./")
		m := pmPythonAssetRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		minor, _ := strconv.Atoi(m[1])
		patch, _ := strconv.Atoi(m[2])
		if _, ok := pmPythonMinorRank[minor]; !ok {
			continue
		}
		a := pmPythonAsset{Name: name, SHA256: strings.ToLower(f[0]), Tag: m[3], Minor: minor, Patch: patch, Stripped: m[4] != ""}
		if !found || better(a, best) {
			best, found = a, true
		}
	}
	return best, found
}

func pmRuntimeBase() (string, error) {
	data, err := P().DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "portmaster-runtime"), nil
}

func pmPythonDir() string {
	base, err := pmRuntimeBase()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "python")
}

// pmPythonInstalled is what an install recorded.
type pmPythonInstalled struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Installed string `json:"installed,omitempty"`
}

// pmPythonBinDir is the bin folder of a complete install, or "".
func pmPythonBinDir() string {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return ""
	}
	dir := pmPythonDir()
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, pmPythonMarker))
	if err != nil || len(raw) > 4<<10 {
		return ""
	}
	var m pmPythonInstalled
	if json.Unmarshal(raw, &m) != nil || m.Name == "" {
		return ""
	}
	bin := filepath.Join(dir, "bin")
	if !regularFile(filepath.Join(bin, "python3")) {
		return ""
	}
	return bin
}

func pmPythonReady() bool { return pmPythonBinDir() != "" }

// pmPythonClient stays on HTTPS GitHub download hosts and trusts the CA bundle
// JukaHub can find on firmware without a certificate store (ytTransport).
func pmPythonClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ytTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			return validatePatchURL(req.URL.String())
		},
	}
}

func pmPythonGet(ctx context.Context, raw string, max int64) ([]byte, error) {
	if err := validatePatchURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-PortMaster/1.0")
	resp, err := pmPythonClient(60 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered HTTP %d for %s", resp.StatusCode, path.Base(req.URL.Path))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("the checksum list is larger than expected")
	}
	return data, nil
}

// pmPythonDownload streams the archive to dest, accepting it only when its
// SHA-256 is want.
func pmPythonDownload(ctx context.Context, raw, dest, want string, progress func(done, total int64)) error {
	if err := validatePatchURL(raw); err != nil {
		return err
	}
	part := dest + ".part"
	defer os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-PortMaster/1.0")
	resp, err := pmPythonClient(20 * time.Minute).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d for the Python archive", resp.StatusCode)
	}
	if resp.ContentLength > pmPythonMaxArchive {
		return errors.New("the Python archive is larger than expected")
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var body io.Reader = io.LimitReader(resp.Body, pmPythonMaxArchive+1)
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	syncErr := out.Sync()
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	case n > pmPythonMaxArchive:
		return errors.New("the Python archive is larger than expected")
	case hex.EncodeToString(hash.Sum(nil)) != want:
		return errors.New("the Python archive did not match its SHA-256 in the release's SHA256SUMS")
	}
	return os.Rename(part, dest)
}

// pmPythonSkip leaves out what PortMaster never uses: headers, docs and
// terminfo, Tcl/Tk, and the parts of the standard library that are tools.
func pmPythonSkip(rel string) bool {
	for _, p := range []string{"include/", "share/", "lib/pkgconfig/", "lib/tcl", "lib/tk", "lib/itcl", "lib/thread", "lib/Tix",
		"lib/libtcl", "lib/libtk"} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 && parts[0] == "lib" && strings.HasPrefix(parts[1], "python3.") {
		switch top := parts[2]; {
		case top == "test", top == "idlelib", top == "tkinter", top == "turtledemo", top == "ensurepip", top == "lib2to3",
			strings.HasPrefix(top, "config-3."):
			return true
		case top == "site-packages" && len(parts) >= 4 && strings.HasPrefix(parts[3], "pip"):
			return true
		case top == "lib-dynload" && len(parts) >= 4 && strings.HasPrefix(parts[3], "_tkinter"):
			return true
		}
	}
	return false
}

// pmPythonCleanRel checks a member path below the archive's python/ folder.
func pmPythonCleanRel(rel string) error {
	if strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return fmt.Errorf("the Python archive has an unsafe path %q", rel)
	}
	return nil
}

// pmPythonExtract unpacks the archive's python/ folder into dest. Links are
// never created (the card is FAT): a link in bin/ to a program beside it
// becomes a two-line shell script that runs it, any other link to a file a
// copy of that file; hard links are copies too.
func pmPythonExtract(ctx context.Context, archive, dest string) error {
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
	tr := tar.NewReader(gz)
	files := map[string]bool{}
	type link struct{ name, target string }
	var links []link
	count := 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if !strings.HasPrefix(name, "python/") {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(name, "python/"), "/")
		if rel == "" {
			continue
		}
		if err := pmPythonCleanRel(rel); err != nil {
			return err
		}
		if pmPythonSkip(rel) {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			count++
			total += h.Size
			if count > pmPythonMaxFiles || h.Size < 0 || total > pmPythonMaxExpanded {
				return errors.New("the Python archive expands beyond its limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, tr, h.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			files[rel] = true
		case tar.TypeSymlink:
			t := h.Linkname
			if t == "" || path.IsAbs(t) || strings.ContainsAny(t, "\\\x00") {
				continue
			}
			joined := path.Join(path.Dir(rel), t)
			if joined == ".." || strings.HasPrefix(joined, "../") {
				continue
			}
			links = append(links, link{rel, joined})
		case tar.TypeLink:
			t := strings.TrimPrefix(strings.TrimPrefix(h.Linkname, "./"), "python/")
			if pmPythonCleanRel(t) == nil {
				links = append(links, link{rel, t})
			}
		}
	}
	// Links to links resolve over a few passes; links to folders are dropped.
	for pass := 0; pass < 4 && len(links) > 0; pass++ {
		var left []link
		for _, l := range links {
			if !files[l.target] {
				left = append(left, l)
				continue
			}
			to := filepath.Join(dest, filepath.FromSlash(l.name))
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			from := filepath.Join(dest, filepath.FromSlash(l.target))
			if info, err := os.Stat(from); err == nil {
				count++
				total += info.Size()
				if count > pmPythonMaxFiles || total > pmPythonMaxExpanded {
					return errors.New("the Python archive expands beyond its limit")
				}
			}
			if path.Dir(l.name) == "bin" && path.Dir(l.target) == "bin" && pmPythonSafeName(path.Base(l.name)) &&
				pmPythonSafeName(path.Base(l.target)) {
				script := "#!/bin/sh\n# Written by JukaHub: " + path.Base(l.name) + " is " + path.Base(l.target) + ".\n" +
					"exec \"$(dirname \"$0\")/" + path.Base(l.target) + "\" \"$@\"\n"
				if err := os.WriteFile(to, []byte(script), 0o755); err != nil {
					return err
				}
			} else if err := copyFileStream(from, to, 0o755); err != nil {
				return err
			}
			files[l.name] = true
		}
		if len(left) == len(links) {
			break
		}
		links = left
	}
	if !files["bin/python3"] {
		return errors.New("the Python archive has no bin/python3")
	}
	return nil
}

// pmPythonSafeName is a file name that can go into a shell script as it is.
func pmPythonSafeName(n string) bool {
	if n == "" || n == "." || n == ".." {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '+' || r == '-') {
			return false
		}
	}
	return true
}

// pmPythonProbe runs a Python on what PortMaster imports and reports why not.
func pmPythonProbe(ctx context.Context, python string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", pmPythonProbeCode)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte(pmPythonProbeOK)) {
		msg := strings.TrimSpace(string(out))
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return errors.New(firstLine(msg))
	}
	return nil
}

// pmPythonInstall installs the Python unless it is already there. phase (may be
// nil) is told when the download ends and the unpack starts.
func pmPythonInstall(ctx context.Context, progress func(done, total int64), phase func(string)) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return errors.New("PortMaster's Python is only installed on an ARM64 handheld")
	}
	if pmPythonReady() {
		return nil
	}
	base, err := pmRuntimeBase()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	// Leftovers of an install that was cut off (a stage, a partial archive).
	for _, pattern := range []string{".python-stage-*", "cpython-*.tar.gz*"} {
		old, _ := filepath.Glob(filepath.Join(base, pattern))
		for _, o := range old {
			_ = os.RemoveAll(o)
		}
	}
	sums, err := pmPythonGet(ctx, pmPythonSumsURL, pmPythonMaxSums)
	if err != nil {
		return fmt.Errorf("reading Python's release list: %w", err)
	}
	asset, ok := pmPythonPick(sums)
	if !ok {
		return errors.New("the latest python-build-standalone release has no aarch64 Python 3.11–3.13 for Linux")
	}
	archive := filepath.Join(base, asset.Name)
	raw := pmPythonReleaseBase + asset.Tag + "/" + url.PathEscape(asset.Name)
	if err := pmPythonDownload(ctx, raw, archive, asset.SHA256, progress); err != nil {
		return err
	}
	defer os.Remove(archive)
	if phase != nil {
		phase(installPhaseExtract)
	}
	stage, err := os.MkdirTemp(base, ".python-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := pmPythonExtract(ctx, archive, stage); err != nil {
		return err
	}
	if phase != nil {
		phase(installPhaseVerify)
	}
	if err := pmPythonProbe(ctx, filepath.Join(stage, "bin", "python3"), []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + base}); err != nil {
		return fmt.Errorf("the downloaded Python did not start here: %w", err)
	}
	data, err := json.Marshal(pmPythonInstalled{Name: asset.Name, SHA256: asset.SHA256, Installed: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	// The files reach the card before the marker that makes them "ready".
	if err := syncDisks(); err != nil {
		logWarn("portmaster", "%v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, pmPythonMarker), data, 0o644); err != nil {
		return err
	}
	final := filepath.Join(base, "python")
	if _, err := os.Lstat(final); err == nil {
		// An incomplete earlier install (no marker): pmPythonReady was false.
		if err := os.RemoveAll(final); err != nil {
			return fmt.Errorf("could not replace %s: %w", final, err)
		}
	}
	if err := os.Rename(stage, final); err != nil {
		return err
	}
	logInfo("portmaster", "installed %s for PortMaster", asset.Name)
	return nil
}
