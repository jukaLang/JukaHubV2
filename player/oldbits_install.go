package main

// oldbits_install.go — making the OldBits Hub entry in Games install itself.
//
// The console added by ports_library.go is launched like any other port, but
// OldBits Hub is not in PortMaster's catalogue, so when it is missing there is
// no store row to press. This file supplies the missing piece: on the first
// launch of the OldBits entry, JukaHub downloads the hub archive and unpacks
// it into the card's ports folder, then carries on with the normal port launch
// (which already installs PortMaster's files when they are absent, ports.go).
//
// OldBits publishes the archive through mirrors rather than one stable,\
// versioned URL on oldbits.net, so the address is configurable:
//
//   - data/ports.env: OLDBITS_HUB_URL=https://…/hub.tar.gz
//   - Custom setting: oldBitsHubURL
//
// A missing address is not guessed: the player gets a precise message instead
// of a silent failure. Both .tar.gz and .zip archives are accepted.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// oldBitsHubScriptName is the launcher the install looks for.
	oldBitsHubScriptName = "OldBits Hub.sh"
	// oldBitsEnvKey names the URL in data/ports.env.
	oldBitsEnvKey = "OLDBITS_HUB_URL"
	// oldBitsCustomKey names the Custom setting.
	oldBitsCustomKey = "oldBitsHubURL"
	// oldBitsInstallTimeout bounds the download so a hung mirror cannot hold
	// the UI forever.
	oldBitsInstallTimeout = 5 * time.Minute
	// oldBitsMaxArchive caps a downloaded archive (the hub is about 9 MB).
	oldBitsMaxArchive = 128 << 20
)

// oldBitsPortScript finds an installed OldBits Hub launcher in the card's
// ports folders, or "".
func oldBitsPortScript() string {
	for _, root := range portRoots(portMasterSDRoot) {
		p := filepath.Join(root, oldBitsHubScriptName)
		if regularFile(p) {
			return p
		}
	}
	return ""
}

// oldBitsHubURL returns the configured download URL, or "".
func oldBitsHubURL(config *Config) string {
	if config != nil {
		if v, ok := customString(config.Variables.Custom, oldBitsCustomKey); ok {
			if u := strings.TrimSpace(v); u != "" {
				return u
			}
		}
	}
	if v := portEnvOverrides()[oldBitsEnvKey]; v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// oldBitsEnsureInstalled downloads and installs the hub when it is not there
// yet, and returns the launcher path. Main thread; it only downloads when a
// URL was configured, so a missing address gives a useful error rather than a
// failed launch.
func oldBitsEnsureInstalled(config *Config) (string, error) {
	if p := oldBitsPortScript(); p != "" {
		return p, nil
	}
	url := oldBitsHubURL(config)
	if url == "" {
		return "", fmt.Errorf("OldBits Hub is not installed. Set %s in %s (or %s in Custom) to its hub.tar.gz URL, then open it again",
			oldBitsEnvKey, portEnvFileName, oldBitsCustomKey)
	}
	if !IsOnline() {
		return "", fmt.Errorf("OldBits Hub is not installed: connect to Wi-Fi so JukaHub can download it")
	}
	ShowToast("Downloading OldBits Hub…", ToastKindInfo)
	dir, err := os.MkdirTemp("", "jukahub-oldbits-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	archive, err := oldBitsDownload(url, dir)
	if err != nil {
		return "", fmt.Errorf("could not download OldBits Hub: %w", err)
	}
	if err := oldBitsUnpack(archive, dir); err != nil {
		return "", fmt.Errorf("could not unpack OldBits Hub: %w", err)
	}
	root := ""
	for _, r := range portRoots(portMasterSDRoot) {
		if dirExists(r) {
			root = r
			break
		}
	}
	if root == "" {
		root = filepath.Join(portMasterSDRoot, "Roms", "PORTS")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// Copy the unpacked tree into the ports folder. Only dest paths inside
	// root are written (filepath.Rel check), so an archive cannot escape.
	src := filepath.Join(dir, "unpacked")
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == src {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("archive entry outside the ports folder: %s", rel)
		}
		dst := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, info.Mode().Perm()|0o644)
	}); err != nil {
		return "", err
	}
	invalidatePortsCache()
	portMetaForget()
	if p := oldBitsPortScript(); p != "" {
		ShowToast("OldBits Hub installed", ToastKindSuccess)
		return p, nil
	}
	return "", errors.New("the archive did not contain OldBits Hub.sh")
}

// oldBitsDownload fetches url into dir, refusing an oversized body.
func oldBitsDownload(url, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), oldBitsInstallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server answered %s", resp.Status)
	}
	name := "hub-archive"
	if lower := strings.ToLower(url); strings.Contains(lower, ".zip") {
		name += ".zip"
	} else {
		name += ".tar.gz"
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.CopyN(f, resp.Body, oldBitsMaxArchive+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n > oldBitsMaxArchive {
		return "", fmt.Errorf("the archive is larger than %d MB", oldBitsMaxArchive>>20)
	}
	return path, nil
}

// oldBitsUnpack extracts a .tar.gz or .zip archive into dir/unpacked. It uses
// the system tar/unzip when present (they already handle oldbits' own layout);
// a Go fallback covers a firmware without them.
func oldBitsUnpack(archive, dir string) error {
	out := filepath.Join(dir, "unpacked")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(archive), ".zip") {
		return oldBitsUnzip(archive, out)
	}
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
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(strings.TrimPrefix(filepath.Clean("/"+h.Name), "/"))
		if rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		dst := filepath.Join(out, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)|0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, io.LimitReader(tr, oldBitsMaxArchive))
			w.Close()
			if err != nil {
				return err
			}
		}
	}
}

func oldBitsUnzip(archive, out string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		rel := filepath.FromSlash(strings.TrimPrefix(filepath.Clean("/"+f.Name), "/"))
		if rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		dst := filepath.Join(out, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o644)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(w, io.LimitReader(r, oldBitsMaxArchive))
		w.Close()
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// oldBitsLaunchScriptPath is the launcher to pass to startPortScript.
func oldBitsLaunchScriptPath(config *Config) (string, error) {
	return oldBitsEnsureInstalled(config)
}

// oldBitsPreflight checks the parts of the hub that make a launch die in its
// first second: the program and its bundled SDL library. The launcher's own
// `./lbhub >> data/hub.log 2>&1` hides the reason from JukaHub's stdout capture,
// so a missing file must be said before the launch rather than after a card.
func oldBitsPreflight(script string) error {
	dir, _ := gameDiagPortDir(script)
	if dir == "" {
		dir = filepath.Dir(script)
	}
	var missing []string
	if !regularFile(filepath.Join(dir, "lbhub")) {
		missing = append(missing, filepath.Join(dir, "lbhub"))
	}
	if !regularFile(filepath.Join(dir, "lib", "libSDL2-2.0.so.0")) &&
		!regularFile("/usr/trimui/lib/libSDL2-2.0.so.0") &&
		!regularFile("/mnt/SDCARD/System/lib/libSDL2-2.0.so.0") {
		missing = append(missing, filepath.Join(dir, "lib", "libSDL2-2.0.so.0"))
	}
	if len(missing) > 0 {
		return fmt.Errorf("OldBits Hub is incomplete (%s is missing). Set %s in %s to reinstall it",
			strings.Join(missing, ", "), oldBitsEnvKey, portEnvFileName)
	}
	return nil
}
