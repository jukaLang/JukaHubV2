package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func newTestSFTPRoot(t *testing.T, readOnly bool) (*sftpRoot, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	confined, err := newSFTPRoot(root, readOnly)
	if err != nil {
		t.Fatalf("newSFTPRoot: %v", err)
	}
	return confined, root
}

func TestSFTPRootResolveStaysInside(t *testing.T) {
	root, dir := newTestSFTPRoot(t, false)

	got, err := root.resolve("/")
	if err != nil || got != root.root {
		t.Fatalf("resolve / = %q, %v; want %q", got, err, root.root)
	}
	got, err = root.resolve("/../../etc/passwd")
	if err != nil {
		t.Fatalf("resolve traversal: %v", err)
	}
	if want := filepath.Join(dir, "etc", "passwd"); got != want {
		t.Fatalf("resolve /../../etc/passwd = %q, want the root-relative %q", got, want)
	}
	if !pathWithin(root.root, got) {
		t.Fatal("resolved traversal escaped the root")
	}

	// A Windows-style drive path must never resolve to a host path. On Linux
	// it is just a file name under the root; on Windows the drive colon is a
	// volume separator and must be refused.
	got, err = root.resolve(`C:\Windows\win.ini`)
	if runtime.GOOS == "windows" {
		if !errors.Is(err, errSFTPOutsideRoot) {
			t.Fatalf("drive path resolved with %v (%q), want errSFTPOutsideRoot", err, got)
		}
	} else if err != nil {
		t.Fatalf("resolve drive path: %v", err)
	} else if !pathWithin(root.root, got) {
		t.Fatalf("drive path resolved outside the root: %q", got)
	}

	if _, err := root.resolve("/bad\x00name"); err == nil {
		t.Fatal("NUL path accepted")
	}
}

func TestSFTPRootSymlinkPolicy(t *testing.T) {
	root, dir := newTestSFTPRoot(t, false)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := root.resolve("/escape"); !errors.Is(err, errSFTPOutsideRoot) {
		t.Fatalf("escape symlink resolved with %v, want errSFTPOutsideRoot", err)
	}
	handlers := &sftpRootHandlers{root: root}
	if f, err := handlers.Fileread(sftp.NewRequest("Get", "/escape")); err == nil {
		f.(io.Closer).Close()
		t.Fatal("Fileread followed a symlink outside the root")
	}
	if _, err := handlers.Filelist(sftp.NewRequest("List", "/escape")); err == nil {
		t.Fatal("Filelist followed a symlink outside the root")
	}

	inside := filepath.Join(dir, "sub", "hello.txt")
	if err := os.Symlink(inside, filepath.Join(dir, "inside-link")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	resolved, err := root.resolve("/inside-link")
	if err != nil || resolved != inside {
		t.Fatalf("inside symlink resolved to %q, %v; want %q", resolved, err, inside)
	}
	f, err := handlers.Fileread(sftp.NewRequest("Get", "/inside-link"))
	if err != nil {
		t.Fatalf("read inside symlink: %v", err)
	}
	f.(io.Closer).Close()
	target, err := handlers.Readlink("/inside-link")
	if err != nil || target != "/sub/hello.txt" {
		t.Fatalf("Readlink = %q, %v; want the client path /sub/hello.txt", target, err)
	}
}

func TestSFTPHandlersFileOperations(t *testing.T) {
	root, dir := newTestSFTPRoot(t, false)
	handlers := &sftpRootHandlers{root: root}

	if real, err := handlers.RealPath("/sub/../sub"); err != nil || real != "/sub" {
		t.Fatalf("RealPath = %q, %v; want /sub", real, err)
	}
	if real, err := handlers.RealPath("/new/file.txt"); err != nil || real != "/new/file.txt" {
		t.Fatalf("RealPath missing path = %q, %v", real, err)
	}
	if list, err := handlers.Filelist(sftp.NewRequest("List", "/")); err != nil {
		t.Fatalf("Filelist: %v", err)
	} else if n := len(list.(sftpLister)); n != 1 {
		t.Fatalf("root listing = %d entries, want 1", n)
	}

	// SSH_FXF_WRITE|CREAT|TRUNC, the flags every SFTP client sends for upload.
	put := sftp.NewRequest("Put", "/upload.txt")
	put.Flags = 0x2 | 0x8 | 0x10
	writer, err := handlers.Filewrite(put)
	if err != nil {
		t.Fatalf("Filewrite: %v", err)
	}
	if _, err := writer.WriteAt([]byte("uploaded"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if closer, ok := writer.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "upload.txt")); err != nil || string(data) != "uploaded" {
		t.Fatalf("uploaded file = %q, %v", data, err)
	}

	cmd := sftp.NewRequest("Rename", "/upload.txt")
	cmd.Target = "/sub/moved.txt"
	if err := handlers.Filecmd(cmd); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "moved.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	mkdir := sftp.NewRequest("Mkdir", "/fresh")
	if err := handlers.Filecmd(mkdir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	rmdir := sftp.NewRequest("Rmdir", "/fresh")
	if err := handlers.Filecmd(rmdir); err != nil {
		t.Fatalf("Rmdir: %v", err)
	}

	// A rename target with ".." must stay inside the root instead of walking
	// up to the host's /tmp.
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	escape := sftp.NewRequest("Rename", "/sub/moved.txt")
	escape.Target = "/../../../tmp/escaped.txt"
	if err := handlers.Filecmd(escape); err != nil {
		t.Fatalf("Rename to clamped target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp", "escaped.txt")); err != nil {
		t.Fatalf("clamped rename did not stay inside the root: %v", err)
	}
}

func TestSFTPHandlersRefuseLinkCreation(t *testing.T) {
	root, _ := newTestSFTPRoot(t, false)
	handlers := &sftpRootHandlers{root: root}
	link := sftp.NewRequest("Symlink", "/new-link")
	link.Target = "/"
	if err := handlers.Filecmd(link); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Symlink = %v, want permission denied", err)
	}
}

func TestSFTPHandlersReadOnly(t *testing.T) {
	root, dir := newTestSFTPRoot(t, true)
	handlers := &sftpRootHandlers{root: root}

	if f, err := handlers.Fileread(sftp.NewRequest("Get", "/sub/hello.txt")); err != nil {
		t.Fatalf("read-only read failed: %v", err)
	} else {
		f.(io.Closer).Close()
	}

	put := sftp.NewRequest("Put", "/nope.txt")
	put.Flags = 0x2 | 0x8
	if _, err := handlers.Filewrite(put); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only Filewrite = %v, want permission denied", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("read-only write created a file")
	}
	remove := sftp.NewRequest("Remove", "/sub/hello.txt")
	if err := handlers.Filecmd(remove); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only Remove = %v, want permission denied", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "hello.txt")); err != nil {
		t.Fatalf("read-only remove deleted the file: %v", err)
	}
}

func TestSFTPRootRequiresDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := newSFTPRoot(file, false); err == nil {
		t.Fatal("newSFTPRoot accepted a file")
	}
	if _, err := newSFTPRoot(strings.Repeat(" ", 3), false); err == nil {
		t.Fatal("newSFTPRoot accepted an empty root")
	}
}
