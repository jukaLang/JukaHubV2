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

// safeAbsolutePath is the final guard for a path used by the state/reset
// helpers. It accepts both absolute paths and ordinary relative paths (the ROM
// scan can return a relative Roms root), but rejects a parent component rather
// than silently resolving to somewhere else.
func safeAbsolutePath(p string) (string, error) {
	if pathHasParentComponent(p) {
		return "", fmt.Errorf("path %q contains a traversal component", p)
	}
	clean, err := cleanUserPath(p)
	if err != nil {
		return "", err
	}
	return clean, nil
}

// pathHasParentComponent reports whether any path segment is exactly "..".
// A filename such as "save..state" is legal and must not be refused.
func pathHasParentComponent(p string) bool {
	for _, part := range strings.FieldsFunc(strings.TrimSpace(p), func(r rune) bool {
		return r == '/' || r == filepath.Separator
	}) {
		if part == ".." {
			return true
		}
	}
	return false
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
	f, err := openRegularFileWithin(root, path)
	if err != nil {
		return "", err
	}
	f.Close()
	return resolved, nil
}

// openRegularFileWithin returns an already-open file anchored to root. Checking
// the pathname and then opening it separately would allow a symlink swap.
func openRegularFileWithin(root, path string) (*os.File, error) {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%q is not a regular file", resolved)
	}
	return f, nil
}

// confinedRegularFile is regularFileWithin with the symbolic-link boundary
// enforced too. A link inside root that points outside is refused, so archive
// and pack code cannot be redirected through a link that only looks contained.
func confinedRegularFile(root, path string) (string, error) {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return "", err
	}
	realRoot := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = real
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	if !isPathWithin(real, realRoot) {
		return "", fmt.Errorf("path %q links outside %q", path, root)
	}
	dir, err := os.OpenRoot(filepath.Dir(real))
	if err != nil {
		return "", err
	}
	defer dir.Close()
	info, err := dir.Stat(filepath.Base(real))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", real)
	}
	return real, nil
}

// statUserFile opens a local file path through an os.Root on its parent. The
// path is normalized and the final component is detached from directory
// traversal, while the root API keeps a replace-race from turning the final
// component into a link out of that directory. It is for paths the user picked
// anywhere on this device (chat attachments, editor files, recent media), not
// for paths that must stay under one configured tree; those use
// resolveWithinRoot or confinedRegularFile.
func statUserFile(path string) (os.FileInfo, error) {
	clean, err := cleanUserPath(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(filepath.Base(clean))
}

// openUserFile is statUserFile's open counterpart, for the same local paths.
func openUserFile(path string) (*os.File, error) {
	clean, err := cleanUserPath(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(filepath.Base(clean))
}

// readUserDir pins a user-selected directory while its entries are listed.
// A rename of the path cannot redirect the read after the root is opened.
func readUserDir(path string) ([]os.DirEntry, error) {
	clean, err := cleanUserPath(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// removeWithinRoot removes a path only when it resolves inside root. The
// removal itself goes through os.Root, so a final component replaced by a
// link between the check and the call cannot redirect it.
func removeWithinRoot(root, path string) error {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside %q", path, root)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Remove(filepath.FromSlash(rel))
}

// existingDirWithin resolves path under root and requires an existing
// directory. feCurrentPath uses it so a tampered fe_path value cannot make the
// File Explorer list (or walk) outside its configured root.
func existingDirWithin(root, path string) (string, error) {
	resolved, err := resolveWithinRoot(root, path)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	child, err := dir.OpenRoot(rel)
	if err != nil {
		return "", err
	}
	child.Close()
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
	f, err := openUserFile(clean)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
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
