package main

// pkgapps.go puts the stock-OS apps a package carries where TrimUI's own
// launcher looks for them.
//
// The Packages store unpacks every package into packages/<name>/ next to
// JukaHub (packages.go). The stock launcher only lists folders directly under
// /mnt/SDCARD/Apps, so an app installed from the store was on the card but
// never showed up there, and "Open folder" was all that was left. After a
// package installs, the app folders inside it are copied into the Apps folder;
// removing the package takes those copies away again.
//
// An app folder is a folder with a config.json and the launch file it names
// (default launch.sh), found at the package's top, one level down
// (DNSChanger/config.json) or under Apps/ (Apps/<Name>/config.json). Each copy
// carries a pkgAppMarker file naming the package that put it there, so a folder
// the user (or another installer) made is never replaced or deleted.

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// pkgStockAppsDir is the folder TrimUI's stock launcher lists apps from.
const pkgStockAppsDir = "/mnt/SDCARD/Apps"

// pkgAppMarker is the file inside a published app folder that names the
// package it came from.
const pkgAppMarker = ".jukahub-package"

// pkgFindStockApps returns the app folders inside one unpacked package, as
// paths relative to pkgDir ("." for the package folder itself).
func pkgFindStockApps(pkgDir string) []string {
	var found []string
	isApp := func(dir string) bool {
		raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			return false
		}
		var cfg struct {
			Launch string `json:"launch"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return false
		}
		launch := strings.TrimSpace(cfg.Launch)
		if launch == "" {
			launch = "launch.sh"
		}
		if launch != filepath.Base(launch) {
			return false
		}
		st, err := os.Stat(filepath.Join(dir, launch))
		return err == nil && st.Mode().IsRegular()
	}
	if isApp(pkgDir) {
		return []string{"."}
	}
	subdirs := func(dir string) []string {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, e.Name())
			}
		}
		return out
	}
	for _, a := range subdirs(pkgDir) {
		if isApp(filepath.Join(pkgDir, a)) {
			found = append(found, a)
			continue
		}
		if strings.EqualFold(a, "Apps") {
			for _, b := range subdirs(filepath.Join(pkgDir, a)) {
				if isApp(filepath.Join(pkgDir, a, b)) {
					found = append(found, filepath.Join(a, b))
				}
			}
		}
	}
	return found
}

// pkgAppDestName is the folder name an app gets in the Apps folder.
func pkgAppDestName(rel, key string) string {
	if rel == "." {
		return key
	}
	return filepath.Base(rel)
}

// pkgPublishApps copies the package's app folders into appsDir and returns
// their names. A folder already there that this package did not put there is
// left alone and skipped. Files are copied over the earlier copy without
// deleting anything, so what an app wrote beside its own files survives an
// update.
func pkgPublishApps(pkgDir, appsDir, key string) ([]string, error) {
	if st, err := os.Stat(appsDir); err != nil || !st.IsDir() {
		return nil, nil
	}
	var done []string
	var firstErr error
	for _, rel := range pkgFindStockApps(pkgDir) {
		name := pkgAppDestName(rel, key)
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
			continue
		}
		dest := filepath.Join(appsDir, name)
		if _, err := os.Lstat(dest); err == nil {
			owner, _ := os.ReadFile(filepath.Join(dest, pkgAppMarker))
			if strings.TrimSpace(string(owner)) != key {
				continue
			}
		}
		if err := pkgCopyTree(filepath.Join(pkgDir, rel), dest); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dest, pkgAppMarker), []byte(key+"\n"), 0o644); err != nil && firstErr == nil {
			firstErr = err
		}
		done = append(done, name)
	}
	return done, firstErr
}

// pkgUnpublishApps removes the app folders that package key put into appsDir.
func pkgUnpublishApps(appsDir, key string) {
	ents, err := os.ReadDir(appsDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		owner, err := os.ReadFile(filepath.Join(appsDir, e.Name(), pkgAppMarker))
		if err == nil && strings.TrimSpace(string(owner)) == key {
			_ = os.RemoveAll(filepath.Join(appsDir, e.Name()))
		}
	}
}

// pkgCopyTree copies the regular files of src into dst, keeping permissions
// (launch scripts and binaries must stay executable). Links and special files
// are skipped.
func pkgCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return pkgCopyFile(p, target)
		}
		return nil
	})
}

func pkgCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".jukahub-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, st.Mode().Perm()); err != nil && !errors.Is(err, os.ErrPermission) {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
