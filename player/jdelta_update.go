package main

// jdelta_update.go — the device side of delta updates. The format and its
// generator live in internal/jdelta, shared with tools/make-delta; this file
// decides which delta applies to the running installation and rebuilds a
// release from it. The update cache (upch.go) and Install from file
// (upch_sideload.go) are its callers.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jukahub/JukaHub/internal/jdelta"
)

// errJdeltaNone means the release offers no delta this installation can use
// and verify. The full archive is the fallback, not a failure.
var errJdeltaNone = errors.New("no usable delta for this installation")

// jdeltaInstalledTag is the release tag the running installation was built
// from: the "from" half of a delta's name.
func jdeltaInstalledTag() string {
	if tag := strings.TrimSpace(readInstalledReleaseInfo().Tag); tag != "" {
		return tag
	}
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return strings.TrimSpace(patchState.InstalledReleaseTag)
}

// jdeltaMaterialize rebuilds the release a bundle describes into outDir from
// the installation in baseDir, then checks the result holds a JukaHub this
// device can run. outDir is removed on any failure.
func jdeltaMaterialize(ctx context.Context, bundlePath, baseDir, outDir string) error {
	b, err := jdelta.ReadBundleFile(bundlePath)
	if err != nil {
		return err
	}
	var need int64
	for _, f := range b.Files {
		need += f.NewSize
	}
	if err := upchCheckFree(filepath.Dir(outDir), need); err != nil {
		return err
	}
	_ = os.RemoveAll(outDir)
	if _, err := jdelta.ApplyBundle(ctx, bundlePath, baseDir, outDir); err != nil {
		_ = os.RemoveAll(outDir)
		return err
	}
	bin := findStagedBinary(outDir)
	if bin == "" {
		_ = os.RemoveAll(outDir)
		return errors.New("the delta does not rebuild a JukaHub for this device")
	}
	if err := upchCheckHostBinary(bin); err != nil {
		_ = os.RemoveAll(outDir)
		return err
	}
	return nil
}

// jdeltaVerifyTree checks a rebuilt tree against the bundle's index before it
// is installed: every file there, with the size and sha256 the verified
// bundle promised.
func jdeltaVerifyTree(bundlePath, tree string) error {
	b, err := jdelta.ReadBundleFile(bundlePath)
	if err != nil {
		return err
	}
	for _, f := range b.Files {
		p := filepath.Join(tree, filepath.FromSlash(f.Name))
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() != f.NewSize {
			return fmt.Errorf("%s is missing or changed", f.Name)
		}
		sum, err := SHA256File(p)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, f.NewSHA256) {
			return fmt.Errorf("%s does not match its checksum", f.Name)
		}
	}
	return nil
}

// jdeltaCheckBase reports whether a bundle applies to the installation in
// baseDir: every file it patches is there, unchanged since the release the
// delta was made from. Install from file asks before the user confirms, so a
// delta for another version is refused with a reason, not half-way through.
func jdeltaCheckBase(b *jdelta.Bundle, baseDir string) error {
	for _, f := range b.Files {
		if f.Base != jdelta.BaseInstalled {
			continue
		}
		p := filepath.Join(baseDir, filepath.FromSlash(f.Name))
		info, err := os.Stat(p)
		if err != nil || info.Size() != f.OldSize {
			return fmt.Errorf("%s is not the version this delta was made from", f.Name)
		}
		sum, err := SHA256File(p)
		if err != nil || !strings.EqualFold(sum, f.OldSHA256) {
			return fmt.Errorf("%s is not the version this delta was made from", f.Name)
		}
	}
	return nil
}
