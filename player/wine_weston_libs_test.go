package main

// wine_weston_libs_test.go — the check that gives WestonPack's Weston modules
// the one library its image leaves to the firmware. The modules are built as
// minimal ELF64 files (buildTestELF, elfcompat_test.go), so the tests need no
// aarch64 toolchain and no WestonPack image.

import (
	"debug/elf"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// westonTestPack writes a WestonPack-shaped folder: the modules named under
// lib_aarch64/libweston-14 (or lib_aarch64/weston for a shell), each made of
// spec. root is the tree's folder.
func westonTestPack(t *testing.T, root string, modules map[string]testELF) {
	t.Helper()
	for name, spec := range modules {
		dir := filepath.Join(root, "lib_aarch64", "libweston-14")
		if name == "kiosk-shell.so" {
			dir = filepath.Join(root, "lib_aarch64", "weston")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), buildTestELF(t, spec), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// westonTestLib writes a library of the tree's own into lib_aarch64.
func westonTestLib(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, "lib_aarch64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	spec := testELF{machine: elf.EM_AARCH64, soname: name}
	if err := os.WriteFile(filepath.Join(dir, name), buildTestELF(t, spec), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestWestonSessionModules pins the modules a mode loads: every mode starts
// Weston with Xwayland (westonwrap's --xwayland), so xwayland.so is in all of
// them — the module JH-MSD7AY died on.
func TestWestonSessionModules(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		want  []string
	}{
		{"windows (GDI)", []string{"drm", "gl", "kiosk", "system"},
			[]string{"drm-backend.so", "gl-renderer.so", "kiosk-shell.so", "xwayland.so"}},
		{"OpenGL", []string{"headless", "noop", "kiosk", "crusty_glx_gl4es"},
			[]string{"headless-backend.so", "kiosk-shell.so", "xwayland.so"}},
		{"pixman", []string{"drm", "pixman", "desktop", "system"},
			[]string{"drm-backend.so", "desktop-shell.so", "xwayland.so"}},
		{"an unknown backend is left to Weston", []string{"bogus", "noop", "kiosk", "system"},
			[]string{"kiosk-shell.so", "xwayland.so"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := westonSessionModules(c.words); !slices.Equal(got, c.want) {
				t.Errorf("westonSessionModules(%q) = %q, want %q", c.words, got, c.want)
			}
		})
	}
}

// TestWestonModuleLibsWritesJpegStandin is the launch of JH-MSD7AY: the mode's
// modules link libjpeg.so.8, the tree has only libjpeg.so.62 (and everything
// else), so a stand-in is written for the library that defines LIBJPEG_8.0 and
// the folder comes back for the session's library path.
func TestWestonModuleLibsWritesJpegStandin(t *testing.T) {
	root, stubDir := t.TempDir(), filepath.Join(t.TempDir(), westonStubDirName)
	westonTestPack(t, root, map[string]testELF{
		"xwayland.so": {machine: elf.EM_AARCH64,
			// The dynamic linker is the one already running Weston, and
			// libc.so.6 is the firmware's: only libjpeg.so.8 is JukaHub's to
			// answer here.
			needed: []string{"libcairo.so.2", "libjpeg.so.8", "libc.so.6", "ld-linux-aarch64.so.1"},
			needs:  map[string][]string{"libjpeg.so.8": {"LIBJPEG_8.0"}}},
	})
	westonTestLib(t, root, "libcairo.so.2")
	westonTestLib(t, root, "libjpeg.so.62") // the image's: a different ABI

	dir, unavailable, err := westonModuleLibs(root, stubDir, westonSessionModules([]string{"drm", "gl", "kiosk", "system"}), nil, wineLog{})
	if err != nil {
		t.Fatalf("westonModuleLibs: %v", err)
	}
	if dir != stubDir {
		t.Fatalf("folder = %q, want %q", dir, stubDir)
	}
	if slices.Contains(unavailable, "libjpeg.so.8") {
		t.Errorf("libjpeg.so.8 reported unavailable although a stand-in was written")
	}
	if !slices.Contains(unavailable, "libc.so.6") {
		t.Errorf("unavailable = %q, want the firmware library JukaHub has no stand-in for", unavailable)
	}
	if slices.Contains(unavailable, "ld-linux-aarch64.so.1") {
		t.Errorf("unavailable = %q, want the dynamic linker left to the loader", unavailable)
	}

	f, err := elf.Open(filepath.Join(stubDir, "libjpeg.so.8"))
	if err != nil {
		t.Fatalf("open the stand-in: %v", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 || f.Type != elf.ET_DYN {
		t.Errorf("stand-in is class %v, machine %v, type %v; want a 64-bit ARM shared object", f.Class, f.Machine, f.Type)
	}
	if defs := elfVersionDefs(f); !defs["LIBJPEG_8.0"] {
		t.Errorf("stand-in defines %v, want LIBJPEG_8.0", defs)
	}

	// A second launch keeps the file it has.
	if again, _, err := westonModuleLibs(root, stubDir, []string{"xwayland.so"}, nil, wineLog{}); err != nil || again != stubDir {
		t.Errorf("second look = %q, %v; want the same folder", again, err)
	}
}

// TestWestonModuleLibsLeavesARealCopyAlone: a firmware (or an image) that has
// libjpeg.so.8 keeps it. Nothing is written, so nothing is put on the path.
func TestWestonModuleLibsLeavesARealCopyAlone(t *testing.T) {
	root, stubDir := t.TempDir(), filepath.Join(t.TempDir(), westonStubDirName)
	westonTestPack(t, root, map[string]testELF{
		"headless-backend.so": {machine: elf.EM_AARCH64,
			needed: []string{"libjpeg.so.8"},
			needs:  map[string][]string{"libjpeg.so.8": {"LIBJPEG_8.0"}}},
	})
	westonTestLib(t, root, "libjpeg.so.8")

	dir, unavailable, err := westonModuleLibs(root, stubDir, westonSessionModules([]string{"headless", "noop", "kiosk", "crusty_glx_gl4es"}), nil, wineLog{})
	if err != nil {
		t.Fatalf("westonModuleLibs: %v", err)
	}
	if dir != "" || len(unavailable) != 0 {
		t.Errorf("folder = %q, unavailable = %q; want nothing to do", dir, unavailable)
	}
	if _, err := os.Stat(filepath.Join(stubDir, "libjpeg.so.8")); err == nil {
		t.Errorf("a stand-in was written although the tree carries the real library")
	}
}

// TestWestonStubCovers: a stand-in is rewritten only when it no longer answers
// what the modules import.
func TestWestonStubCovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libjpeg.so.8")
	syms := []devStubSym{
		{name: "jpeg_read_header", version: "LIBJPEG_8.0"},
		{name: "jpeg_std_error", version: "LIBJPEG_8.0"},
	}
	data, err := devStubBuild("libjpeg.so.8", []string{"LIBJPEG_8.0"}, syms, westonStubJpegRet)
	if err != nil {
		t.Fatalf("devStubBuild: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write the stand-in: %v", err)
	}
	if !westonStubCovers(path, []string{"LIBJPEG_8.0"}, syms) {
		t.Errorf("the stand-in does not cover what it was built for")
	}
	wider := append(slices.Clone(syms), devStubSym{name: "jpeg_start_decompress", version: "LIBJPEG_8.0"})
	if westonStubCovers(path, []string{"LIBJPEG_8.0"}, wider) {
		t.Errorf("the stand-in covers a function it does not export")
	}
	if westonStubCovers(path, []string{"LIBJPEG_8.0", "LIBJPEG_9.0"}, syms) {
		t.Errorf("the stand-in covers a version it does not define")
	}
	if westonStubCovers(filepath.Join(t.TempDir(), "absent.so"), nil, nil) {
		t.Errorf("a missing library counts as covering")
	}
}
