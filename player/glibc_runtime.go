package main

// A private C runtime for AArch64 programs that need newer glibc symbols than
// TrimUI's firmware provides (glibc 2.33). The firmware's /lib is never
// changed, and only selected child processes start through this runtime's
// loader: GreenOvercast's game command (glibc_port.go) and the bash PortMaster
// and its ports run on when the firmware's bash is BusyBox
// (portmaster_runtime.go).
//
// What is installed: Ubuntu 24.04 ("noble") arm64 libc6 — glibc 2.39 — with
// libgcc-s1, libstdc++6 and libtinfo6 beside it and Ubuntu's bash, copied into
// <data>/private-glibc/runtime-<libc6 version>-<hash of all versions>/ (the
// libraries flat, bash in bin/). <data>/private-glibc/current names the one in use.
//
// Which build: the newest Ubuntu publishes for noble in noble, noble-updates
// and noble-security, found the way apt finds it (linuxpkg_index.go). Each
// suite's Release must carry a valid signature by Ubuntu's archive key, its
// main/binary-arm64 Packages index must match the SHA-256 in that Release, and
// every .deb must match the SHA-256 in that index. A Release older than one
// already accepted is refused, so an old signed index cannot roll the card
// back. A new build replaces the old one only after its loader and its bash
// have started on this device. JukaHub looks for a newer build at most once a
// week, in the background, when the Ports panel or PortMaster opens online.
//
// When the signed index cannot be read, a first install falls back to the one
// libc6 build whose size and SHA-256 are pinned below (glibc only, no bash) —
// the build GreenOvercast was tested with on a stock TrimUI.

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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	privateGlibcVersion           = "2.39"
	privateGlibcPinned            = "2.39-0ubuntu8.12"
	privateGlibcPackage           = "libc6_" + privateGlibcPinned + "_arm64.deb"
	privateGlibcURL               = "https://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/" + privateGlibcPackage
	privateGlibcHTTPURL           = "http://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/" + privateGlibcPackage
	privateGlibcSHA               = "2578bc27d4e077f5e93c283d2502bc693c516d9590a20a27840b548a4d436820"
	privateGlibcSize        int64 = 2774244
	privateGlibcMaxFiles          = 1024
	privateGlibcMaxFileSize int64 = 16 << 20
	privateGlibcMaxExpanded int64 = 64 << 20
	privateGlibcMaxMember   int64 = 32 << 20
	privateGlibcTree              = "usr/lib/aarch64-linux-gnu/"
	privateGlibcMarker            = ".jukahub-glibc-source"
	// privateRuntimeManifestName is written last into an installed runtime.
	privateRuntimeManifestName = ".jukahub-runtime.json"
	// privateRuntimeCurrentName names the runtime folder in use.
	privateRuntimeCurrentName = "current"
	// privateRuntimeCheckedName holds when Ubuntu was last asked for a newer build.
	privateRuntimeCheckedName = "checked"
	// privateRuntimeDatesName holds each suite's newest accepted Release date.
	privateRuntimeDatesName = "releases.json"
	// privateRuntimeRefreshEvery spaces the background checks for a newer build.
	privateRuntimeRefreshEvery = 7 * 24 * time.Hour
	// privateRuntimeBashRel is Ubuntu's bash inside a runtime folder.
	privateRuntimeBashRel = "bin/bash"
)

var privateGlibcRequired = []string{
	"ld-linux-aarch64.so.1", "libc.so.6", "libm.so.6", "libdl.so.2",
	"libpthread.so.0", "librt.so.1", "libresolv.so.2",
	"libnss_files.so.2", "libnss_dns.so.2",
}

// privateRuntimePackages are the noble packages one runtime is made of.
var privateRuntimePackages = []string{"libc6", "libgcc-s1", "libstdc++6", "libtinfo6", "bash"}

// privateRuntimeRequired are the files beyond privateGlibcRequired a complete
// runtime has (the C++ runtime for ports, and bash with its terminal library).
var privateRuntimeRequired = []string{"libgcc_s.so.1", "libstdc++.so.6", "libtinfo.so.6", privateRuntimeBashRel}

// privateRuntimeLibTrees are the package paths whose contents are copied flat
// into the runtime folder (noble ships most under usr/, a few still under lib/).
var privateRuntimeLibTrees = []string{privateGlibcTree, "lib/aarch64-linux-gnu/"}

func privateGlibcBase() (string, error) {
	data, err := P().DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "private-glibc"), nil
}

// privateGlibcDir is the folder of the pinned fallback build.
func privateGlibcDir() (string, error) {
	base, err := privateGlibcBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "glibc-2.39-0ubuntu8.12"), nil
}

// privateRuntimePkg is one package a runtime was built from.
type privateRuntimePkg struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// privateRuntimeManifest describes an installed runtime.
type privateRuntimeManifest struct {
	Glibc     string                       `json:"glibc"`
	Packages  map[string]privateRuntimePkg `json:"packages"`
	Installed string                       `json:"installed,omitempty"`
}

func privateRuntimeReadManifest(dir string) (privateRuntimeManifest, bool) {
	var m privateRuntimeManifest
	raw, err := os.ReadFile(filepath.Join(dir, privateRuntimeManifestName))
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	if m.Glibc != privateGlibcVersion || m.Packages["libc6"].Version == "" {
		return m, false
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(dir, name)) {
			return m, false
		}
	}
	for _, name := range privateRuntimeRequired {
		if !regularFile(filepath.Join(dir, filepath.FromSlash(name))) {
			return m, false
		}
	}
	return m, true
}

// privateRuntimeDirName is the folder name for a libc6 version: only
// characters that are safe in a file name and a shell word are kept.
func privateRuntimeDirName(version string) string {
	var b strings.Builder
	b.WriteString("runtime-")
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '+', r == '~':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// privateRuntimeName is the folder for one set of packages: the libc6
// version, and a short hash of every package's version, so an update of bash
// or libstdc++ alone gets a new folder instead of replacing the one in use.
func privateRuntimeName(entries map[string]linuxPkgEntry) string {
	h := sha256.New()
	for _, p := range privateRuntimePackages {
		fmt.Fprintf(h, "%s=%s\n", p, entries[p].Version)
	}
	return privateRuntimeDirName(entries["libc6"].Version) + "-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// privateRuntimeCurrent is the complete runtime named by <base>/current.
func privateRuntimeCurrent(base string) (string, privateRuntimeManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(base, privateRuntimeCurrentName))
	if err != nil || len(raw) > 256 {
		return "", privateRuntimeManifest{}, false
	}
	name := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(name, "runtime-") || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", privateRuntimeManifest{}, false
	}
	dir := filepath.Join(base, name)
	m, ok := privateRuntimeReadManifest(dir)
	if !ok {
		return "", privateRuntimeManifest{}, false
	}
	return dir, m, true
}

// privateGlibcPaths reports a complete, verified install: the current runtime,
// else the pinned fallback (its marker is written only after the pinned
// download, extraction and on-device loader probe).
func privateGlibcPaths() (loader, libDir string, ok bool) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return "", "", false
	}
	base, err := privateGlibcBase()
	if err != nil {
		return "", "", false
	}
	if dir, _, ok := privateRuntimeCurrent(base); ok {
		return filepath.Join(dir, "ld-linux-aarch64.so.1"), dir, true
	}
	dir, err := privateGlibcDir()
	if err != nil {
		return "", "", false
	}
	marker, err := os.ReadFile(filepath.Join(dir, privateGlibcMarker))
	if err != nil || string(marker) != privateGlibcSHA+"\n" {
		return "", "", false
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(dir, name)) {
			return "", "", false
		}
	}
	loader = filepath.Join(dir, "ld-linux-aarch64.so.1")
	return loader, dir, true
}

func privateGlibcReady() bool {
	_, _, ok := privateGlibcPaths()
	return ok
}

// privateGlibcBashWrapperDir is the folder holding the `bash` that runs
// Ubuntu's bash through the current runtime, or "" when that runtime has no
// bash (none installed, or only the pinned fallback). The wrapper is rewritten
// when the runtime changes.
func privateGlibcBashWrapperDir() string {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return ""
	}
	base, err := privateGlibcBase()
	if err != nil {
		return ""
	}
	dir, _, ok := privateRuntimeCurrent(base)
	if !ok {
		return ""
	}
	binDir := filepath.Join(base, "bin")
	wrapper := filepath.Join(binDir, "bash")
	text := privateRuntimeBashWrapper(dir)
	privateRuntimeWrapperMu.Lock()
	defer privateRuntimeWrapperMu.Unlock()
	if old, err := os.ReadFile(wrapper); err != nil || string(old) != text {
		if os.MkdirAll(binDir, 0o755) != nil {
			return ""
		}
		tmp := wrapper + ".tmp"
		if os.WriteFile(tmp, []byte(text), 0o755) != nil || os.Rename(tmp, wrapper) != nil {
			_ = os.Remove(tmp)
			return ""
		}
	}
	if !executableFile(wrapper) && os.Chmod(wrapper, 0o755) != nil {
		return ""
	}
	return binDir
}

func privateGlibcBashReady() bool { return privateGlibcBashWrapperDir() != "" }

// privateRuntimeWrapperMu serialises writing the bash wrapper (the main
// thread and the runtime goroutines all ask for it).
var privateRuntimeWrapperMu sync.Mutex

// privateRuntimeBashWrapper starts Ubuntu's bash from dir through dir's own
// loader. Only that bash process uses the private libraries: everything it
// starts runs with the firmware's loader and LD_LIBRARY_PATH, which are left
// alone. --argv0 keeps $0 and $BASH on the wrapper, so a script that starts
// "$BASH" again comes back here.
func privateRuntimeBashWrapper(dir string) string {
	return "#!/bin/sh\n" +
		"# Written by JukaHub: Ubuntu's bash on JukaHub's private C runtime (glibc " + privateGlibcVersion + "),\n" +
		"# for PortMaster on firmware whose bash is BusyBox. The firmware is not changed.\n" +
		"exec " + shellQuote(filepath.Join(dir, "ld-linux-aarch64.so.1")) + " --inhibit-cache --library-path " +
		shellQuote(dir) + " --argv0 \"$0\" " + shellQuote(filepath.Join(dir, filepath.FromSlash(privateRuntimeBashRel))) + " \"$@\"\n"
}

// ── Pinned fallback download ─────────────────────────────────────────────

// privateGlibcDownload accepts exactly the pinned Ubuntu package and bounds
// the download before it can fill a card. A failed or mismatched partial file
// never becomes an installed runtime.
func privateGlibcDownload(ctx context.Context, dest string) error {
	if info, err := os.Stat(dest); err == nil && info.Size() == privateGlibcSize {
		if sum, err := privateGlibcFileSHA(dest); err == nil && sum == privateGlibcSHA {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	err := privateGlibcFetch(ctx, dest, privateGlibcURL)
	if err == nil {
		return nil
	}
	var networkErr *url.Error
	if !errors.As(err, &networkErr) || ctx.Err() != nil {
		return err
	}
	// Stock TrimUI's CA bundle cannot validate ports.ubuntu.com. Plain HTTP
	// is usable here because the complete package is pinned by size and SHA-256
	// before a single byte can become an installed runtime.
	logWarn("glibc", "HTTPS download failed; trying pinned package over HTTP: %v", err)
	if fallbackErr := privateGlibcFetch(ctx, dest, privateGlibcHTTPURL); fallbackErr != nil {
		return fmt.Errorf("C runtime download failed over HTTPS (%v) and HTTP (%w)", err, fallbackErr)
	}
	return nil
}

func privateGlibcFetch(ctx context.Context, dest, source string) error {
	part := dest + ".part"
	defer os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	wantScheme := req.URL.Scheme
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: ytTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 || req.URL.Scheme != wantScheme || req.URL.Hostname() != "ports.ubuntu.com" {
				return errors.New("C runtime download redirected away from Ubuntu")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > privateGlibcSize {
		return fmt.Errorf("C runtime download returned HTTP %d or an unexpected size", resp.StatusCode)
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(resp.Body, privateGlibcSize+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != privateGlibcSize || hex.EncodeToString(hash.Sum(nil)) != privateGlibcSHA {
		return errors.New("C runtime package did not match Ubuntu's pinned SHA-256")
	}
	return os.Rename(part, dest)
}

func privateGlibcFileSHA(file string) (string, error) {
	f, err := os.Open(file)
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

// ── Debian packages ──────────────────────────────────────────────────────

// privateGlibcDebData finds a .deb's data.tar member and returns a reader of
// the uncompressed tar (zstd, xz or gzip) and a function that releases it.
func privateGlibcDebData(f *os.File) (io.Reader, func(), error) {
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || !bytes.Equal(magic, []byte("!<arch>\n")) {
		return nil, nil, errors.New("C runtime package is not a Debian archive")
	}
	for {
		h := make([]byte, 60)
		if _, err := io.ReadFull(f, h); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, errors.New("C runtime package has no data.tar")
			}
			return nil, nil, err
		}
		if !bytes.Equal(h[58:60], []byte("`\n")) {
			return nil, nil, errors.New("C runtime package has an invalid member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(h[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || size < 0 || size > privateGlibcMaxMember {
			return nil, nil, errors.New("C runtime package member is too large")
		}
		body := io.LimitReader(f, size)
		switch name {
		case "data.tar.zst":
			zr, err := zstd.NewReader(body, zstd.WithDecoderMaxMemory(64<<20))
			if err != nil {
				return nil, nil, err
			}
			return zr, zr.Close, nil
		case "data.tar.xz":
			return newFeXzReader(context.Background(), body, linuxPkgXzDict), func() {}, nil
		case "data.tar.gz":
			gz, err := gzip.NewReader(body)
			if err != nil {
				return nil, nil, err
			}
			return gz, func() { gz.Close() }, nil
		}
		if _, err := io.CopyN(io.Discard, f, size+size%2); err != nil {
			return nil, nil, err
		}
	}
}

// privateGlibcExtractDeb reads the pinned libc6 package's library tree (the
// fallback install). It copies only regular files under the arm64 library
// tree, with strict path and size bounds. In particular, package files can
// never write into the firmware.
func privateGlibcExtractDeb(deb, dest string) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()
	r, done, err := privateGlibcDebData(f)
	if err != nil {
		return err
	}
	defer done()
	return privateGlibcExtractTar(tar.NewReader(r), dest)
}

// privateGlibcCleanRel checks a path inside a package tree.
func privateGlibcCleanRel(rel string) error {
	if strings.Contains(rel, "\\") || path.IsAbs(rel) || path.Clean(rel) != rel ||
		rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || strings.HasSuffix(rel, "/..") {
		return errors.New("C runtime package contains an unsafe library path")
	}
	return nil
}

func privateGlibcExtractTar(tr *tar.Reader, dest string) error {
	x := privateRuntimeExtractor{dest: dest, libOnly: true}
	if err := x.add(tr); err != nil {
		return err
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(dest, name)) {
			return fmt.Errorf("C runtime package is missing %s", name)
		}
	}
	return nil
}

// privateRuntimeExtractor copies the members a runtime needs from one or more
// packages' data.tar into dest: the arm64 library trees flat, and bash into
// bin/. Symbolic links are never created; a link to a file beside it (the
// sonames libstdc++.so.6 and libtinfo.so.6) becomes a copy in finish.
type privateRuntimeExtractor struct {
	dest    string
	libOnly bool // the pinned fallback: usr/lib/aarch64-linux-gnu/ only
	count   int
	total   int64
	files   map[string]bool
	links   map[string]string
}

// member maps a package path to its place in the runtime, or "".
func (x *privateRuntimeExtractor) member(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if !x.libOnly && (name == "bin/bash" || name == "usr/bin/bash") {
		return privateRuntimeBashRel, nil
	}
	trees := privateRuntimeLibTrees
	if x.libOnly {
		trees = trees[:1]
	}
	for _, tree := range trees {
		if !strings.HasPrefix(name, tree) {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(name, tree), "/")
		if rel == "" {
			return "", nil
		}
		if err := privateGlibcCleanRel(rel); err != nil {
			return "", err
		}
		if rel == "bin" || strings.HasPrefix(rel, "bin/") {
			return "", nil // never shadows the bash slot
		}
		return rel, nil
	}
	return "", nil
}

func (x *privateRuntimeExtractor) add(tr *tar.Reader) error {
	if x.files == nil {
		x.files, x.links = map[string]bool{}, map[string]string{}
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := x.member(h.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink:
			// Only a plain name in the same folder is kept, as a copy later.
			if t := h.Linkname; t != "" && !strings.ContainsAny(t, `/\`) && t != "." && t != ".." {
				x.links[rel] = path.Join(path.Dir(rel), t)
			}
			continue
		default:
			continue // directories are made from file paths
		}
		x.count++
		x.total += h.Size
		if x.count > privateGlibcMaxFiles || h.Size < 0 || h.Size > privateGlibcMaxFileSize || x.total > privateGlibcMaxExpanded {
			return errors.New("C runtime library tree exceeds its size limit")
		}
		outPath := filepath.Join(x.dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if rel == "ld-linux-aarch64.so.1" || rel == "libc.so.6" || rel == privateRuntimeBashRel {
			mode = 0o755
		}
		// A later package may ship a file an earlier one did; the later wins.
		out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
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
		if err := os.Chmod(outPath, mode); err != nil {
			return err
		}
		x.files[rel] = true
		delete(x.links, rel)
	}
}

// finish turns same-folder links into copies (a link to a link is followed
// over a few passes) and checks the runtime is complete.
func (x *privateRuntimeExtractor) finish() error {
	for pass := 0; pass < 4 && len(x.links) > 0; pass++ {
		progress := false
		for link, target := range x.links {
			if !x.files[target] {
				continue
			}
			if err := copyFileStream(filepath.Join(x.dest, filepath.FromSlash(target)),
				filepath.Join(x.dest, filepath.FromSlash(link)), 0o644); err != nil {
				return err
			}
			x.files[link] = true
			delete(x.links, link)
			progress = true
		}
		if !progress {
			break
		}
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(x.dest, name)) {
			return fmt.Errorf("C runtime package is missing %s", name)
		}
	}
	for _, name := range privateRuntimeRequired {
		if !regularFile(filepath.Join(x.dest, filepath.FromSlash(name))) {
			return fmt.Errorf("C runtime packages are missing %s", name)
		}
	}
	return os.Chmod(filepath.Join(x.dest, filepath.FromSlash(privateRuntimeBashRel)), 0o755)
}

func privateGlibcProbe(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	loader := filepath.Join(dir, "ld-linux-aarch64.so.1")
	libc := filepath.Join(dir, "libc.so.6")
	for _, args := range [][]string{{"--version"}, {"--library-path", dir, libc}} {
		cmd := exec.CommandContext(ctx, loader, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte(privateGlibcVersion)) {
			return fmt.Errorf("private C runtime did not start on this device: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// privateRuntimeProbeBash runs the runtime's bash on a bash array — the
// feature BusyBox's sh lacks and PortMaster's scripts use.
func privateRuntimeProbeBash(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "ld-linux-aarch64.so.1"), "--inhibit-cache", "--library-path", dir,
		filepath.Join(dir, filepath.FromSlash(privateRuntimeBashRel)), "-c", `a=(jukahub bash); echo "${a[1]}-${#a[@]}"`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("bash-2")) {
		return fmt.Errorf("Ubuntu's bash did not start on this device: %s", firstLine(strings.TrimSpace(string(out)+" "+fmt.Sprint(err))))
	}
	return nil
}

// ── The newest build, through Ubuntu's signed index ──────────────────────

// privateRuntimeInstallMu serialises every install and update of the runtime.
var privateRuntimeInstallMu sync.Mutex

func privateRuntimeLoadDates(base string) map[string]time.Time {
	out := map[string]time.Time{}
	raw, err := os.ReadFile(filepath.Join(base, privateRuntimeDatesName))
	if err != nil || len(raw) > 16<<10 {
		return out
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for suite, v := range m {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out[suite] = t
		}
	}
	return out
}

func privateRuntimeSaveDates(base string, dates map[string]time.Time) error {
	m := map[string]string{}
	for suite, t := range dates {
		m[suite] = t.UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return linuxPkgWriteFile(filepath.Join(base, privateRuntimeDatesName), data)
}

// privateRuntimeResolve reads the three suites' signed Releases and their main
// Packages indexes and returns the newest entry of every runtime package.
func privateRuntimeResolve(ctx context.Context, base string) (map[string]linuxPkgEntry, error) {
	lists := filepath.Join(base, "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, p := range privateRuntimePackages {
		want[p] = true
	}
	seen := privateRuntimeLoadDates(base)
	dates := map[string]time.Time{}
	best := map[string]linuxPkgEntry{}
	for _, suite := range linuxPkgSuites {
		relFile := filepath.Join(lists, suite+"_Release")
		sigFile := relFile + ".gpg"
		if err := linuxPkgFetch(ctx, "dists/"+suite+"/Release", relFile, linuxPkgReleaseMax, nil, nil); err != nil {
			return nil, fmt.Errorf("%s: %w", suite, err)
		}
		if err := linuxPkgFetch(ctx, "dists/"+suite+"/Release.gpg", sigFile, linuxPkgSigMax, nil, nil); err != nil {
			return nil, fmt.Errorf("%s: %w", suite, err)
		}
		data, err := os.ReadFile(relFile)
		if err != nil {
			return nil, err
		}
		sig, err := os.ReadFile(sigFile)
		if err != nil {
			return nil, err
		}
		// Parsed first only for its date (the clock floor of the check);
		// nothing in it is used until the signature has verified.
		rel, perr := linuxPkgParseRelease(data)
		if err := linuxPkgVerifyRelease(data, sig, rel.Date); err != nil {
			return nil, fmt.Errorf("%s: %w", suite, err)
		}
		if perr != nil {
			return nil, fmt.Errorf("%s: %w", suite, perr)
		}
		if rel.Suite != suite {
			return nil, fmt.Errorf("%s: the signed Release is for %q", suite, rel.Suite)
		}
		if prev, ok := seen[suite]; ok && !rel.Date.IsZero() && rel.Date.Before(prev) {
			return nil, fmt.Errorf("%s: Ubuntu's archive sent an older signed index (%s) than one already accepted (%s); refusing it",
				suite, rel.Date.Format("2006-01-02"), prev.Format("2006-01-02"))
		}
		if !rel.Date.IsZero() {
			dates[suite] = rel.Date
		} else if prev, ok := seen[suite]; ok {
			dates[suite] = prev
		}
		found := false
		for _, ext := range []string{".xz", ".gz"} {
			name := "main/binary-" + linuxPkgArch + "/Packages" + ext
			sum, ok := rel.SHA256[name]
			if !ok {
				continue
			}
			local := filepath.Join(lists, suite+"_main_Packages"+ext)
			remote := "dists/" + suite + "/" + name
			if rel.ByHash {
				remote = "dists/" + suite + "/main/binary-" + linuxPkgArch + "/by-hash/SHA256/" + sum.SHA256
			}
			if err := linuxPkgFetch(ctx, remote, local, 0, &sum, nil); err != nil {
				return nil, fmt.Errorf("%s main: %w", suite, err)
			}
			if err := privateRuntimeScanPackages(ctx, local, ext == ".xz", want, best); err != nil {
				return nil, fmt.Errorf("%s main: %w", suite, err)
			}
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("%s: the signed Release lists no main package index for %s", suite, linuxPkgArch)
		}
	}
	for _, p := range privateRuntimePackages {
		if _, ok := best[p]; !ok {
			return nil, fmt.Errorf("Ubuntu's noble index has no %s for %s", p, linuxPkgArch)
		}
	}
	if err := privateRuntimeSaveDates(base, dates); err != nil {
		logWarn("glibc", "could not record the accepted Release dates: %v", err)
	}
	return best, nil
}

// privateRuntimeScanPackages streams one verified Packages file and keeps the
// newest arm64 entry of each wanted package in best.
func privateRuntimeScanPackages(ctx context.Context, file string, xz bool, want map[string]bool, best map[string]linuxPkgEntry) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader
	if xz {
		r = newFeXzReader(ctx, f, linuxPkgXzDict)
	} else {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	_, err = linuxPkgReadPackages(r, func(e linuxPkgEntry) error {
		if !want[e.Name] || e.Arch != linuxPkgArch {
			return nil
		}
		if cur, ok := best[e.Name]; !ok || linuxPkgVersionCompare(e.Version, cur.Version) > 0 {
			best[e.Name] = e
		}
		return ctx.Err()
	})
	return err
}

// privateRuntimeSame reports whether m was built from exactly these entries.
func privateRuntimeSame(m privateRuntimeManifest, entries map[string]linuxPkgEntry) bool {
	for _, p := range privateRuntimePackages {
		e, ok := entries[p]
		have, had := m.Packages[p]
		if !ok || !had || have.Version != e.Version || have.SHA256 != e.SHA256 {
			return false
		}
	}
	return true
}

// privateRuntimeSetCurrent points <base>/current at name.
func privateRuntimeSetCurrent(base, name string) error {
	return linuxPkgWriteFile(filepath.Join(base, privateRuntimeCurrentName), []byte(name+"\n"))
}

// privateRuntimeInstallLatest installs the newest signed build unless the
// current runtime already is that build. progress (may be nil) sees each
// download. The caller does not hold privateRuntimeInstallMu.
func privateRuntimeInstallLatest(ctx context.Context, progress func(done, total int64)) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || !IsTrimuiDevice() {
		return errors.New("private C runtime is only offered on an ARM64 TrimUI")
	}
	privateRuntimeInstallMu.Lock()
	defer privateRuntimeInstallMu.Unlock()
	base, err := privateGlibcBase()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	entries, err := privateRuntimeResolve(ctx, base)
	if err != nil {
		return err
	}
	libc := entries["libc6"]
	if !strings.HasPrefix(libc.Version, privateGlibcVersion+"-") {
		return fmt.Errorf("Ubuntu's libc6 is %s, not glibc %s; keeping the current runtime", libc.Version, privateGlibcVersion)
	}
	// The floor for a first install, before any Release date was recorded: a
	// replayed old (validly signed) index cannot offer less than the build
	// pinned below.
	if linuxPkgVersionCompare(libc.Version, privateGlibcPinned) < 0 {
		return fmt.Errorf("the index offers libc6 %s, older than %s; refusing it", libc.Version, privateGlibcPinned)
	}
	currentName := ""
	if dir, m, ok := privateRuntimeCurrent(base); ok {
		if privateRuntimeSame(m, entries) {
			return nil
		}
		if linuxPkgVersionCompare(libc.Version, m.Packages["libc6"].Version) < 0 {
			return fmt.Errorf("the index offers libc6 %s, older than the installed %s; keeping it", libc.Version, m.Packages["libc6"].Version)
		}
		currentName = filepath.Base(dir)
	}
	name := privateRuntimeName(entries)
	if name == currentName {
		return fmt.Errorf("the runtime folder %s is in use; keeping it", name)
	}
	target := filepath.Join(base, name)
	if m, ok := privateRuntimeReadManifest(target); ok && privateRuntimeSame(m, entries) {
		return privateRuntimeSetCurrent(base, name)
	}
	debs := filepath.Join(base, "debs")
	paths := map[string]string{}
	for _, p := range privateRuntimePackages {
		e := entries[p]
		dest := filepath.Join(debs, strings.TrimPrefix(privateRuntimeDirName(p+"_"+e.Version), "runtime-")+".deb")
		if err := linuxPkgFetch(ctx, e.Filename, dest, 0, &linuxPkgSum{SHA256: e.SHA256, Size: e.Size}, progress); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		paths[p] = dest
	}
	stage, err := os.MkdirTemp(base, ".runtime-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	x := privateRuntimeExtractor{dest: stage}
	for _, p := range privateRuntimePackages {
		if err := privateRuntimeExtractInto(paths[p], &x); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := x.finish(); err != nil {
		return err
	}
	if err := privateGlibcProbe(ctx, stage); err != nil {
		return err
	}
	if err := privateRuntimeProbeBash(ctx, stage); err != nil {
		return err
	}
	m := privateRuntimeManifest{Glibc: privateGlibcVersion, Packages: map[string]privateRuntimePkg{},
		Installed: time.Now().UTC().Format(time.RFC3339)}
	for _, p := range privateRuntimePackages {
		m.Packages[p] = privateRuntimePkg{Version: entries[p].Version, SHA256: entries[p].SHA256}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// The files reach the card before the manifest that makes them "ready".
	if err := syncDisks(); err != nil {
		logWarn("glibc", "%v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, privateRuntimeManifestName), data, 0o644); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		// An earlier attempt at this version that never became current.
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("could not replace %s: %w", name, err)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		return err
	}
	if err := syncDisks(); err != nil {
		logWarn("glibc", "%v", err)
	}
	if err := privateRuntimeSetCurrent(base, name); err != nil {
		return err
	}
	_ = os.RemoveAll(debs)
	logInfo("glibc", "installed private runtime %s (libc6 %s, libstdc++6 %s, bash %s) from Ubuntu's signed index",
		name, libc.Version, entries["libstdc++6"].Version, entries["bash"].Version)
	return nil
}

func privateRuntimeExtractInto(deb string, x *privateRuntimeExtractor) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()
	r, done, err := privateGlibcDebData(f)
	if err != nil {
		return err
	}
	defer done()
	return x.add(tar.NewReader(r))
}

// privateRuntimeSweep removes runtime folders that are no longer current, the
// pinned fallback once a signed runtime replaced it, and leftovers of
// interrupted installs. It runs before a refresh, when no port started from
// an old folder can still be running (the Ports panel is on screen).
func privateRuntimeSweep(base string) {
	dir, _, ok := privateRuntimeCurrent(base)
	if !ok {
		return
	}
	current := filepath.Base(dir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case n == current:
		case strings.HasPrefix(n, "runtime-") && e.IsDir(),
			strings.HasPrefix(n, ".runtime-stage-"), strings.HasPrefix(n, ".glibc-stage-"),
			n == "glibc-2.39-0ubuntu8.12", n == privateGlibcPackage, n == "debs":
			if err := os.RemoveAll(filepath.Join(base, n)); err != nil {
				logDebug("glibc", "could not remove old %s: %v", n, err)
			}
		}
	}
}

// ── Install entry points ─────────────────────────────────────────────────

// privateGlibcInstallPinned installs the pinned fallback libc6.
func privateGlibcInstallPinned(ctx context.Context) error {
	base, err := privateGlibcBase()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	target, err := privateGlibcDir()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		// A previous interrupted install or a damaged card must not leave the
		// player stuck forever. This is JukaHub's fixed, versioned data folder.
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("could not repair the private C runtime: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	packagePath := filepath.Join(base, privateGlibcPackage)
	if err := privateGlibcDownload(ctx, packagePath); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(base, ".glibc-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := privateGlibcExtractDeb(packagePath, stage); err != nil {
		return err
	}
	if err := privateGlibcProbe(ctx, stage); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, privateGlibcMarker), []byte(privateGlibcSHA+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		return err
	}
	if !privateGlibcReady() {
		return errors.New("private C runtime installation did not pass its final check")
	}
	logInfo("glibc", "installed pinned private glibc %s (fallback)", privateGlibcVersion)
	return nil
}

// privateGlibcInstall makes sure some verified runtime is installed: the
// newest signed build, else the pinned fallback.
func privateGlibcInstall(ctx context.Context) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || !IsTrimuiDevice() {
		return errors.New("private C runtime is only offered on an ARM64 TrimUI")
	}
	if privateGlibcReady() {
		return nil
	}
	err := privateRuntimeInstallLatest(ctx, nil)
	if err == nil {
		privateRuntimeMarkChecked()
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	logWarn("glibc", "newest runtime from Ubuntu's signed index failed (%v); installing the pinned libc6", err)
	privateRuntimeInstallMu.Lock()
	defer privateRuntimeInstallMu.Unlock()
	if privateGlibcReady() {
		return nil
	}
	if pinErr := privateGlibcInstallPinned(ctx); pinErr != nil {
		return fmt.Errorf("%v; pinned fallback: %w", err, pinErr)
	}
	return nil
}

// privateGlibcEnsureBash makes sure the current runtime has Ubuntu's bash
// (only the signed path provides one).
func privateGlibcEnsureBash(ctx context.Context, progress func(done, total int64)) error {
	if privateGlibcBashReady() {
		return nil
	}
	if err := privateRuntimeInstallLatest(ctx, progress); err != nil {
		return err
	}
	privateRuntimeMarkChecked()
	if !privateGlibcBashReady() {
		return errors.New("the private runtime has no usable bash")
	}
	return nil
}

var privateGlibcWork struct {
	mu       sync.Mutex
	running  bool
	callback func(error)
}

// privateGlibcEnsureAsync joins concurrent requests to one install. If a
// player chooses a different streaming service during the download, the last
// choice is the one launched. The callback runs on the UI thread.
func privateGlibcEnsureAsync(after func(error)) {
	if privateGlibcReady() {
		if after != nil {
			tbxPost(func() { after(nil) })
		}
		return
	}
	privateGlibcWork.mu.Lock()
	privateGlibcWork.callback = after
	if privateGlibcWork.running {
		privateGlibcWork.mu.Unlock()
		return
	}
	privateGlibcWork.running = true
	privateGlibcWork.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := privateGlibcInstall(ctx)
		privateGlibcWork.mu.Lock()
		callback := privateGlibcWork.callback
		privateGlibcWork.callback = nil
		privateGlibcWork.running = false
		privateGlibcWork.mu.Unlock()
		if callback != nil {
			tbxPost(func() { callback(err) })
		}
	}()
}

// ── Weekly refresh ───────────────────────────────────────────────────────

var privateRuntimeRefresh struct {
	mu      sync.Mutex
	running bool
	tried   bool // once a session
}

func privateRuntimeMarkChecked() {
	if base, err := privateGlibcBase(); err == nil {
		_ = linuxPkgWriteFile(filepath.Join(base, privateRuntimeCheckedName), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"))
	}
}

// privateRuntimeRefreshDue is true a week after the last check, when there was
// none, and when the clock is behind it (TrimUI starts before NTP).
func privateRuntimeRefreshDue(base string) bool {
	raw, err := os.ReadFile(filepath.Join(base, privateRuntimeCheckedName))
	if err != nil {
		return true
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	if err != nil {
		return true
	}
	now := time.Now()
	return now.Before(t.Add(-24*time.Hour)) || now.Sub(t) > privateRuntimeRefreshEvery
}

// privateGlibcRefreshAsync looks for a newer Ubuntu build in the background —
// once a session, at most once a week, only when a runtime is installed and
// the handheld is online — and swaps it in when it has started here.
func privateGlibcRefreshAsync() {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || !IsTrimuiDevice() || !IsOnline() || !privateGlibcReady() {
		return
	}
	privateRuntimeRefresh.mu.Lock()
	if privateRuntimeRefresh.running || privateRuntimeRefresh.tried {
		privateRuntimeRefresh.mu.Unlock()
		return
	}
	privateRuntimeRefresh.running, privateRuntimeRefresh.tried = true, true
	privateRuntimeRefresh.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("glibc", "runtime refresh failed: %v", r)
			}
			privateRuntimeRefresh.mu.Lock()
			privateRuntimeRefresh.running = false
			privateRuntimeRefresh.mu.Unlock()
		}()
		base, err := privateGlibcBase()
		if err != nil {
			return
		}
		func() {
			privateRuntimeInstallMu.Lock()
			defer privateRuntimeInstallMu.Unlock()
			privateRuntimeSweep(base)
		}()
		if !privateRuntimeRefreshDue(base) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := privateRuntimeInstallLatest(ctx, nil); err != nil {
			logWarn("glibc", "looking for a newer private runtime: %v", err)
			return
		}
		// The folder this replaced stays until the next session's sweep: a
		// port started meanwhile may still be using it.
		privateRuntimeMarkChecked()
	}()
}
