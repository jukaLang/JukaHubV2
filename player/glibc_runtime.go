package main

// A private C runtime for AArch64 programs that need newer glibc symbols than
// TrimUI's firmware provides. The firmware's /lib is never changed. The
// package is Ubuntu's arm64 libc6, pinned to the SHA-256 in its signed package
// index. Only its library tree is copied into JukaHub's data directory, and
// only a selected child process is started through its loader.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	privateGlibcPackage           = "libc6_2.39-0ubuntu8.12_arm64.deb"
	privateGlibcURL               = "https://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/" + privateGlibcPackage
	privateGlibcHTTPURL           = "http://ports.ubuntu.com/ubuntu-ports/pool/main/g/glibc/" + privateGlibcPackage
	privateGlibcSHA               = "2578bc27d4e077f5e93c283d2502bc693c516d9590a20a27840b548a4d436820"
	privateGlibcSize        int64 = 2774244
	privateGlibcMaxFiles          = 512
	privateGlibcMaxFileSize int64 = 16 << 20
	privateGlibcMaxExpanded int64 = 64 << 20
	privateGlibcTree              = "usr/lib/aarch64-linux-gnu/"
	privateGlibcMarker            = ".jukahub-glibc-source"
)

var privateGlibcRequired = []string{
	"ld-linux-aarch64.so.1", "libc.so.6", "libm.so.6", "libdl.so.2",
	"libpthread.so.0", "librt.so.1", "libresolv.so.2",
	"libnss_files.so.2", "libnss_dns.so.2",
}

func privateGlibcBase() (string, error) {
	data, err := P().DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "private-glibc"), nil
}

func privateGlibcDir() (string, error) {
	base, err := privateGlibcBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "glibc-2.39-0ubuntu8.12"), nil
}

// privateGlibcPaths reports a complete, verified install. The marker is only
// written after the pinned download, extraction and on-device loader probe.
func privateGlibcPaths() (loader, libDir string, ok bool) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return "", "", false
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

// privateGlibcExtractDeb reads the pinned .deb's zstd tar member. It copies
// only regular files under the arm64 library tree, with strict path and size
// bounds. In particular, package files can never write into the firmware.
func privateGlibcExtractDeb(deb, dest string) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || !bytes.Equal(magic, []byte("!<arch>\n")) {
		return errors.New("C runtime package is not a Debian archive")
	}
	for {
		h := make([]byte, 60)
		if _, err := io.ReadFull(f, h); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("C runtime package has no data.tar.zst")
			}
			return err
		}
		if !bytes.Equal(h[58:60], []byte("`\n")) {
			return errors.New("C runtime package has an invalid member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(h[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || size < 0 || size > 8<<20 {
			return errors.New("C runtime package member is too large")
		}
		if name == "data.tar.zst" {
			zr, err := zstd.NewReader(io.LimitReader(f, size), zstd.WithDecoderMaxMemory(64<<20))
			if err != nil {
				return err
			}
			err = privateGlibcExtractTar(tar.NewReader(zr), dest)
			zr.Close()
			return err
		}
		if _, err := io.CopyN(io.Discard, f, size+size%2); err != nil {
			return err
		}
	}
}

func privateGlibcExtractTar(tr *tar.Reader, dest string) error {
	var count int
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if !strings.HasPrefix(name, privateGlibcTree) {
			continue
		}
		rel := strings.TrimPrefix(name, privateGlibcTree)
		if rel == "" {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue // directories are made from file paths; symlinks are ignored
		}
		if strings.Contains(rel, "\\") || path.IsAbs(rel) || path.Clean(rel) != rel ||
			rel == ".." || strings.HasPrefix(rel, "../") {
			return errors.New("C runtime package contains an unsafe library path")
		}
		count++
		total += h.Size
		if count > privateGlibcMaxFiles || h.Size < 0 || h.Size > privateGlibcMaxFileSize || total > privateGlibcMaxExpanded {
			return errors.New("C runtime library tree exceeds its size limit")
		}
		outPath := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if rel == "ld-linux-aarch64.so.1" || rel == "libc.so.6" {
			mode = 0o755
		}
		out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
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
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(dest, name)) {
			return fmt.Errorf("C runtime package is missing %s", name)
		}
	}
	return nil
}

func privateGlibcProbe(ctx context.Context, dir string) error {
	loader := filepath.Join(dir, "ld-linux-aarch64.so.1")
	libc := filepath.Join(dir, "libc.so.6")
	for _, args := range [][]string{{"--version"}, {"--library-path", dir, libc}} {
		cmd := exec.CommandContext(ctx, loader, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("2.39")) {
			return fmt.Errorf("private C runtime did not start on this device: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func privateGlibcInstall(ctx context.Context) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || !IsTrimuiDevice() {
		return errors.New("private C runtime is only offered on an ARM64 TrimUI")
	}
	if privateGlibcReady() {
		return nil
	}
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
	logInfo("glibc", "installed private glibc %s from pinned Ubuntu package", privateGlibcVersion)
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
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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
