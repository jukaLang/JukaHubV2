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

// TestWestonModuleLibsCompletesAnOldZlib is the launch of JH-6G5TZF and
// JH-2S4CSH: nothing is missing — the firmware's zlib 1.2.8 is there — but
// WestonPack's libpng16 asks it for inflateValidate, which zlib 1.2.9 added,
// so Weston stopped at "undefined symbol: inflateValidate, version
// ZLIB_1.2.9". The shim answers that one call, defines every name in zlib's
// version table, and keeps the firmware's file underneath in DT_NEEDED so
// every other call is still the firmware's own code.
func TestWestonModuleLibsCompletesAnOldZlib(t *testing.T) {
	root, stubDir := t.TempDir(), filepath.Join(t.TempDir(), westonStubDirName)
	westonTestPack(t, root, map[string]testELF{
		"xwayland.so": {machine: elf.EM_AARCH64, needed: []string{"libcairo.so.2"}},
	})
	// The image's libcairo links the libpng that does the asking, two
	// libraries below the module.
	tree := filepath.Join(root, "lib_aarch64")
	writeTestFile(t, filepath.Join(tree, "libcairo.so.2"), buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, soname: "libcairo.so.2", needed: []string{"libpng16.so.16"},
	}))
	writeTestFile(t, filepath.Join(tree, "libpng16.so.16"), buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, soname: "libpng16.so.16", needed: []string{"libz.so.1"},
		needs: map[string][]string{"libz.so.1": {"ZLIB_1.2.0", "ZLIB_1.2.9"}},
		imports: []testImport{
			{name: "inflate", version: "ZLIB_1.2.0"},
			{name: "inflateValidate", version: "ZLIB_1.2.9"},
		},
	}))
	old := filepath.Join(tree, "libz.so.1")
	writeTestFile(t, old, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, soname: "libz.so.1",
		exports: []string{"inflate", "inflateInit2_", "inflateReset", "crc32"},
	}))

	// The fixture has to read the way the real libpng16 does: versioned imports
	// from libz, two libraries below the module.
	pack, err := elf.Open(filepath.Join(tree, "libpng16.so.16"))
	if err != nil {
		t.Fatalf("open the fixture's libpng16: %v", err)
	}
	imports := westonImportsByLib(pack)
	_ = pack.Close()
	if len(imports["libz.so.1"]) != 2 {
		t.Fatalf("the fixture's imports from libz.so.1 = %v, want inflate and inflateValidate", imports["libz.so.1"])
	}

	dir, unavailable, err := westonModuleLibs(root, stubDir, westonSessionModules([]string{"headless", "noop", "kiosk", "crusty_glx_gl4es"}), nil, wineLog{})
	if err != nil {
		t.Fatalf("westonModuleLibs: %v", err)
	}
	if dir != stubDir {
		t.Fatalf("folder = %q, want %q", dir, stubDir)
	}
	if len(unavailable) != 0 {
		t.Errorf("unavailable = %q, want nothing: the library is here", unavailable)
	}

	path := filepath.Join(stubDir, "libz.so.1")
	f, err := elf.Open(path)
	if err != nil {
		t.Fatalf("open the shim: %v", err)
	}
	defer f.Close()
	if soname, _ := f.DynString(elf.DT_SONAME); !slices.Contains(soname, "libz.so.1") {
		t.Errorf("soname = %v, want libz.so.1: the loader must take the shim for the firmware's library", soname)
	}
	if needed, _ := f.ImportedLibraries(); !slices.Equal(needed, []string{old}) {
		t.Errorf("DT_NEEDED = %v, want the firmware's %s underneath", needed, old)
	}
	defs := elfVersionDefs(f)
	for _, v := range []string{"ZLIB_1.2.0", "ZLIB_1.2.5", "ZLIB_1.2.9"} {
		if !defs[v] {
			t.Errorf("the shim defines %v, want %s among them", defs, v)
		}
	}
	symbols, err := f.DynamicSymbols()
	if err != nil {
		t.Fatalf("read the shim's symbols: %v", err)
	}
	have := map[string]bool{}
	for _, s := range symbols {
		have[s.Name] = true
	}
	if !have["inflateValidate"] {
		t.Errorf("the shim does not export inflateValidate")
	}
	if have["inflate"] {
		t.Errorf("the shim exports inflate: the firmware's own zlib answers that call")
	}

	// A second launch keeps the file it has.
	if again, _, err := westonModuleLibs(root, stubDir, []string{"xwayland.so"}, nil, wineLog{}); err != nil || again != stubDir {
		t.Errorf("second look = %q, %v; want the same folder", again, err)
	}
}

// TestWestonModuleLibsKeepsANewerZlib: a firmware whose zlib has
// inflateValidate (1.2.9 and later) keeps its own library, and a shim an older
// firmware needed is removed rather than shadowing it in a later session.
func TestWestonModuleLibsKeepsANewerZlib(t *testing.T) {
	root, stubDir := t.TempDir(), filepath.Join(t.TempDir(), westonStubDirName)
	westonTestPack(t, root, map[string]testELF{
		"xwayland.so": {machine: elf.EM_AARCH64, needed: []string{"libz.so.1"}},
	})
	writeTestFile(t, filepath.Join(root, "lib_aarch64", "libz.so.1"), buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, soname: "libz.so.1", defines: []string{"ZLIB_1.2.9"},
		exports: []string{"inflate", "inflateValidate"},
	}))
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(stubDir, "libz.so.1")
	writeTestFile(t, stale, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, soname: "libz.so.1", exports: []string{"inflateValidate"},
	}))

	dir, _, err := westonModuleLibs(root, stubDir, westonSessionModules([]string{"headless", "noop", "kiosk", "crusty_glx_gl4es"}), nil, wineLog{})
	if err != nil {
		t.Fatalf("westonModuleLibs: %v", err)
	}
	if dir != "" {
		t.Errorf("folder = %q, want nothing to add", dir)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("the shim of a closed gap is still in %s", stubDir)
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
