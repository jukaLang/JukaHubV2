package main

// sftp_root.go — SFTP request handlers confined to a root directory.
//
// The handlers are used by the SSH/SFTP service in ssh_server.go. Two rules
// hold for every request:
//
//  1. Client paths are POSIX paths rooted at "/" and are joined to the local
//     root lexically. ".." is cleaned away before the join, so a client can
//     never walk above the root.
//  2. Symlinks are resolved before any filesystem call and the result must
//     still live inside the root. A link that points outside is refused, as
//     is creating through a dangling link (its target is unknown).
//
// Errors always mention the client path, never the host path, so a remote
// listing cannot reveal where the app is installed.

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pkg/sftp"
)

var errSFTPOutsideRoot = errors.New("permission denied: path is outside the shared root")

// sftpRoot is the resolved root for one connection. root is already passed
// through filepath.EvalSymlinks by newSFTPRoot.
type sftpRoot struct {
	root     string
	readOnly bool
}

func newSFTPRoot(root string, readOnly bool) (*sftpRoot, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("no shared root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("shared root is not a directory")
	}
	return &sftpRoot{root: real, readOnly: readOnly}, nil
}

// clientPath converts a resolved local path back into the POSIX path the
// client uses. It is only called with paths already proven to be inside root.
func (r *sftpRoot) clientPath(local string) string {
	rel, err := filepath.Rel(r.root, local)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

// localPath joins a client POSIX path to the root without touching the
// filesystem. Each component is added separately, so a Windows drive letter or
// backslash smuggled into a name can never turn the result into an absolute
// host path, and no component may be "..".
func (r *sftpRoot) localPath(client string) (string, error) {
	if strings.ContainsRune(client, 0) {
		return "", errSFTPOutsideRoot
	}
	if !strings.HasPrefix(client, "/") {
		client = "/" + client
	}
	rel := strings.TrimPrefix(path.Clean(client), "/")
	local := r.root
	if rel == "" || rel == "." {
		return local, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errSFTPOutsideRoot
		}
		// On Windows ':' selects an alternate data stream and '\\' is a
		// separator, so either could be used to address a host path.
		if runtime.GOOS == "windows" && strings.ContainsAny(part, `:\`) {
			return "", errSFTPOutsideRoot
		}
		local = filepath.Join(local, part)
	}
	if !pathWithin(r.root, local) {
		return "", errSFTPOutsideRoot
	}
	return local, nil
}

// resolve maps a client path to a local path. Missing final components are
// allowed (needed for create/rename destinations): the deepest existing
// ancestor is resolved and the missing suffix is appended to it. Because the
// final existing component is resolved too, a symlink that points outside the
// root is refused before any handler opens it.
func (r *sftpRoot) resolve(client string) (string, error) {
	local, err := r.localPath(client)
	if err != nil {
		return "", err
	}
	resolved, err := resolveExisting(local)
	if err != nil {
		return "", err
	}
	if !pathWithin(r.root, resolved) {
		return "", errSFTPOutsideRoot
	}
	return resolved, nil
}

// resolveLink returns a symlink's target after proving that following it stays
// inside the root. It deliberately does not use resolve(): that follows the
// link, which would make Readlink report the target's own path.
func (r *sftpRoot) resolveLink(client string) (string, error) {
	local, err := r.localPath(client)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(local)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", os.ErrInvalid
	}
	target, err := os.Readlink(local)
	if err != nil {
		return "", err
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(local), target)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil || !pathWithin(r.root, real) {
		return "", errSFTPOutsideRoot
	}
	return target, nil
}

// resolveExisting resolves the deepest existing ancestor of p. The returned
// path may not exist yet but is free of symlinked components that do exist.
func resolveExisting(p string) (string, error) {
	cur := p
	var missing []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return real, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", os.ErrNotExist
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// pathWithin reports whether p is root or lives below it, comparing whole
// path components rather than string prefixes.
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (r *sftpRoot) denyReadOnly() error {
	if r.readOnly {
		return os.ErrPermission
	}
	return nil
}

// sftpRootHandlers implements every optional pkg/sftp handler interface so a
// single value covers get, put, open, list, stat, readlink and file commands.
type sftpRootHandlers struct {
	root *sftpRoot
}

// sftpLister is the ListAt buffer expected by pkg/sftp.
type sftpLister []os.FileInfo

func (l sftpLister) ListAt(dst []os.FileInfo, off int64) (int, error) {
	if off < 0 || off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[off:])
	if int(off)+n >= len(l) {
		return n, io.EOF
	}
	return n, nil
}

func (h *sftpRootHandlers) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	local, err := h.root.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.IsDir() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}

func (h *sftpRootHandlers) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	if err := h.root.denyReadOnly(); err != nil {
		return nil, err
	}
	local, err := h.root.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	flags := req.Pflags()
	// WriteAt supplies explicit offsets, so O_APPEND must not be set: the two
	// are incompatible and the kernel would ignore the offsets.
	openFlags := os.O_WRONLY
	if flags.Read {
		openFlags = os.O_RDWR
	}
	if flags.Creat {
		openFlags |= os.O_CREATE
	}
	if flags.Trunc {
		openFlags |= os.O_TRUNC
	}
	if flags.Excl {
		openFlags |= os.O_EXCL
	}
	f, err := os.OpenFile(local, openFlags, 0o644)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.IsDir() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}

// OpenFile supports clients that open one handle for both reading and writing.
func (h *sftpRootHandlers) OpenFile(req *sftp.Request) (sftp.WriterAtReaderAt, error) {
	if err := h.root.denyReadOnly(); err != nil {
		return nil, err
	}
	local, err := h.root.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	flags := req.Pflags()
	openFlags := os.O_RDWR
	if flags.Creat {
		openFlags |= os.O_CREATE
	}
	if flags.Trunc {
		openFlags |= os.O_TRUNC
	}
	if flags.Excl {
		openFlags |= os.O_EXCL
	}
	f, err := os.OpenFile(local, openFlags, 0o644)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.IsDir() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}

func (h *sftpRootHandlers) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	local, err := h.root.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(local)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return sftpLister{info}, nil
	}
	dir, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	list := make(sftpLister, 0, len(entries))
	for _, entry := range entries {
		if fi, err := entry.Info(); err == nil {
			list = append(list, fi)
		}
	}
	return list, nil
}

func (h *sftpRootHandlers) Lstat(req *sftp.Request) (sftp.ListerAt, error) {
	local, err := h.root.resolve(req.Filepath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(local)
	if err != nil {
		return nil, err
	}
	return sftpLister{info}, nil
}

func (h *sftpRootHandlers) Readlink(client string) (string, error) {
	target, err := h.root.resolveLink(client)
	if err != nil {
		return "", err
	}
	// An absolute target inside the root is reported as a client path so a
	// Windows session never sees the host's C:\Users\... prefix.
	if filepath.IsAbs(filepath.FromSlash(target)) {
		if resolved, err := h.root.resolve(client); err == nil {
			return h.root.clientPath(resolved), nil
		}
	}
	return filepath.ToSlash(target), nil
}

// RealPath answers the client's realpath requests. It returns the confined
// path, not the host path, and works for paths that do not exist yet.
func (h *sftpRootHandlers) RealPath(client string) (string, error) {
	local, err := h.root.resolve(client)
	if err != nil {
		return "", err
	}
	return h.root.clientPath(local), nil
}

func (h *sftpRootHandlers) Filecmd(req *sftp.Request) error {
	flags := req.AttrFlags()
	attrs := req.Attributes()

	switch req.Method {
	case "Setstat":
		local, err := h.root.resolve(req.Filepath)
		if err != nil {
			return err
		}
		if err := h.root.denyReadOnly(); err != nil {
			return err
		}
		if attrs == nil {
			return os.ErrInvalid
		}
		if flags.Permissions {
			if err := os.Chmod(local, os.FileMode(attrs.Mode)&os.ModePerm); err != nil {
				return err
			}
		}
		if flags.Size {
			if err := os.Truncate(local, int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			if err := os.Chtimes(local, attrs.AccessTime(), attrs.ModTime()); err != nil {
				return err
			}
		}
		return nil

	case "Rename", "PosixRename":
		if err := h.root.denyReadOnly(); err != nil {
			return err
		}
		from, err := h.root.resolve(req.Filepath)
		if err != nil {
			return err
		}
		to, err := h.root.resolve(req.Target)
		if err != nil {
			return err
		}
		return os.Rename(from, to)

	case "Mkdir":
		if err := h.root.denyReadOnly(); err != nil {
			return err
		}
		local, err := h.root.resolve(req.Filepath)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o755)
		if flags.Permissions && attrs != nil {
			mode = os.FileMode(attrs.Mode) & os.ModePerm
		}
		return os.Mkdir(local, mode)

	case "Rmdir":
		if err := h.root.denyReadOnly(); err != nil {
			return err
		}
		local, err := h.root.resolve(req.Filepath)
		if err != nil {
			return err
		}
		info, err := os.Lstat(local)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return os.ErrInvalid
		}
		return os.Remove(local)

	case "Remove":
		if err := h.root.denyReadOnly(); err != nil {
			return err
		}
		local, err := h.root.resolve(req.Filepath)
		if err != nil {
			return err
		}
		return os.Remove(local)

	case "Symlink", "Link":
		// Creating links would let a client manufacture paths whose meaning
		// changes later (and hard links can escape the root on some systems).
		// Existing links inside the root can still be read and followed.
		return os.ErrPermission
	}
	return sftp.ErrSSHFxOpUnsupported
}
