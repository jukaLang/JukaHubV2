package main

// Tests for the on-device Windows runtime installer. They run against a local
// HTTP server and a synthetic Hangover-shaped tarball, so the real download,
// extraction and wrapper path is exercised without network access or 500 MB
// download.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeHangoverTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		mode := int64(0o644)
		if strings.HasSuffix(name, "/wine") || strings.HasSuffix(name, "wine") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func withWindowsRuntimeRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir, prevVerify := windowsRuntimeDirOverride, windowsRuntimeVerify
	windowsRuntimeDirOverride = dir
	// Do not spawn a real wine --version in unit tests.
	windowsRuntimeVerify = func(string) error { return nil }
	setWindowsRuntimeStatus("", "", false)
	t.Cleanup(func() {
		windowsRuntimeDirOverride, windowsRuntimeVerify = prevDir, prevVerify
		setWindowsRuntimeStatus("", "", false)
	})
	return dir
}

// installWineRuntime must download, extract, and leave a wrapper that
// resolveWine finds at data/windows-runtime/bin/wine.
func TestInstallWineRuntimeFromArchive(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	archive := makeHangoverTar(t, map[string]string{
		"hangover/bin/wine":            "#!/bin/sh\nexit 0\n",
		"hangover/lib/wine/libwine.so": "payload",
		"hangover/usr/bin/other":       "x",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	if err := installWineRuntime(srv.URL + "/hangover_11.16_debian12_bookworm_arm64.tar"); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(root, "bin", "wine")
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatalf("wrapper was not created: %v", err)
	}
	// Windows does not track Unix permission bits, so the executable check
	// only applies to the handheld/Linux build where the wrapper must run.
	if !IsWindows() && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("wrapper is not executable: %v", info.Mode())
	}
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("hangover", "bin", "wine"); !strings.Contains(string(script), want) {
		t.Errorf("wrapper does not run the extracted wine binary %q:\n%s", want, script)
	}
	if !strings.Contains(string(script), "LD_LIBRARY_PATH") {
		t.Errorf("wrapper does not export LD_LIBRARY_PATH:\n%s", script)
	}
	if _, err := os.Stat(filepath.Join(root, "hangover", "lib", "wine", "libwine.so")); err != nil {
		t.Errorf("runtime payload was not extracted: %v", err)
	}

	// The wrapper is the launcher's resolution point.
	t.Setenv("JUKAHUB_WINE", "")
	if got, err := resolveWine(nil); err != nil || got != wrapper {
		t.Errorf("resolveWine = %q, %v; want %q", got, err, wrapper)
	}
}

// A hostile archive must not be able to write outside the runtime folder.
func TestInstallWineRuntimeRejectsTraversal(t *testing.T) {
	root := withWindowsRuntimeRoot(t)
	archive := makeHangoverTar(t, map[string]string{
		"../../escaped.txt": "nope",
		"bin/wine":          "#!/bin/sh\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	err := installWineRuntime(srv.URL + "/hangover.tar")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error = %v, want an escape rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); statErr == nil {
		t.Fatal("traversal entry was written outside the runtime folder")
	}
}

func TestRuntimeExtractionRejectsPreexistingSymlinkEscape(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		t.Run(format, func(t *testing.T) {
			dest, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(dest, "escape")); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("creating symlinks needs extra privileges on Windows: %v", err)
				}
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "test."+format)
			if format == "tar" {
				if err := os.WriteFile(archive, makeHangoverTar(t, map[string]string{"escape/payload": "unsafe"}), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
					w, err := zw.Create("escape/payload")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write([]byte("unsafe")); err != nil {
						t.Fatal(err)
					}
				})
			}
			if err := extractRuntimeArchive(archive, dest); err == nil {
				t.Fatal("extraction through outside link succeeded")
			}
			if _, err := os.Stat(filepath.Join(outside, "payload")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("outside path exists or stat failed: %v", err)
			}
		})
	}
}

func TestRuntimeExtractionDoesNotOverwriteOutsideHardLink(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(dest, "victim")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "test.tar")
	if err := os.WriteFile(archive, makeHangoverTar(t, map[string]string{"victim": "replacement"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractRuntimeArchive(archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "original" {
		t.Fatalf("outside hard link was modified: %q, %v", got, err)
	}
}
