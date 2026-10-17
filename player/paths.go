package main

// paths.go centralizes validation for filesystem paths that come from user
// input: config values, File Explorer rows, archive entries, plugin names and
// app-launcher rows. The guards reject rather than repair, so a malformed path
// can never silently resolve somewhere unexpected.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cleanUserPath trims and normalizes a user-supplied path and rejects empty
// paths and NUL bytes (which the OS layer truncates in surprising ways).
func cleanUserPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(p, '\x00') {
		return "", errors.New("path contains a NUL byte")
	}
	return filepath.Clean(p), nil
}

// isPathWithin reports whether path is base itself or a descendant of base.
// Both sides are made absolute first so relative working-directory tricks
// cannot pass the check.
func isPathWithin(path, base string) bool {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveWithinRoot resolves candidate against root (unless it is already
// absolute) and refuses any result that escapes root.
func resolveWithinRoot(root, candidate string) (string, error) {
	cleanRoot, err := cleanUserPath(root)
	if err != nil {
		return "", fmt.Errorf("invalid root: %w", err)
	}
	cleanCandidate, err := cleanUserPath(candidate)
	if err != nil {
		return "", err
	}
	resolved := cleanCandidate
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cleanRoot, resolved)
	}
	resolved = filepath.Clean(resolved)
	if !isPathWithin(resolved, cleanRoot) {
		return "", fmt.Errorf("path %q escapes %q", candidate, root)
	}
	return resolved, nil
}

// regularFileWithin resolves path under root and requires an existing regular
// file. It is the guard for features that open a file selected in a scene
// whose root is known (File Explorer, PDF reader, image viewer).
func regularFileWithin(root, path string) (string, error) {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", resolved)
	}
	return resolved, nil
}

// existingDirWithin resolves path under root and requires an existing
// directory. feCurrentPath uses it so a tampered fe_path value cannot make the
// File Explorer list (or walk) outside its configured root.
func existingDirWithin(root, path string) (string, error) {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return resolved, nil
}

// existingRegularFile cleans a user-supplied path and requires it to name an
// existing regular file. Used where the file may legitimately live anywhere
// (a cookies file the user picked), so containment is not appropriate.
func existingRegularFile(path string) (string, error) {
	clean, err := cleanUserPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", clean)
	}
	return clean, nil
}

// safeArchiveBaseName reduces an archive entry name to a single safe file name.
// Only the base name is ever used, so directory traversal has no effect even
// for hostile archives; the extra checks reject names that cannot be a file.
func safeArchiveBaseName(name string) (string, error) {
	if strings.ContainsRune(name, '\x00') {
		return "", errors.New("archive entry contains a NUL byte")
	}
	base := filepath.Base(filepath.FromSlash(strings.TrimSpace(name)))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return "", fmt.Errorf("invalid archive entry name %q", name)
	}
	if strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("archive entry name %q is not a plain file name", name)
	}
	return base, nil
}

// safeJoinFile joins a validated plain file name to dir and double-checks the
// result stays inside dir.
func safeJoinFile(dir, name string) (string, error) {
	base, err := safeArchiveBaseName(name)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, base)
	if !isPathWithin(dest, dir) {
		return "", fmt.Errorf("path %q escapes %q", dest, dir)
	}
	return dest, nil
}
