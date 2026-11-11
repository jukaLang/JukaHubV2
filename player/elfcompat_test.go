package main

// elfcompat_test.go — the load check reads three things from a binary (its
// architecture, the libraries it needs, the symbol versions it needs of each)
// and one from each library (the versions it defines). The tests build those as
// bytes — minimal ELF64 little-endian files with a .dynamic, a .gnu.version_r
// and a .gnu.version_d — so they run on any platform and need no toolchain.

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// testELF describes a minimal ELF64 little-endian shared object.
type testELF struct {
	machine elf.Machine
	// needed is DT_NEEDED; needs is .gnu.version_r (library → versions), with
	// the versions in weak marked as weak requirements.
	needed []string
	needs  map[string][]string
	weak   map[string]bool
	// soname names the base version definition; defines are the versions the
	// file defines (.gnu.version_d), none when empty.
	soname  string
	defines []string
}

// buildTestELF lays the file out as header, section data, section headers.
func buildTestELF(t *testing.T, spec testELF) []byte {
	t.Helper()
	le := binary.LittleEndian
	dynstr := []byte{0}
	strOff := map[string]uint32{"": 0}
	str := func(s string) uint32 {
		if off, ok := strOff[s]; ok {
			return off
		}
		off := uint32(len(dynstr))
		dynstr = append(append(dynstr, s...), 0)
		strOff[s] = off
		return off
	}

	var dynamic []byte
	for _, lib := range spec.needed {
		entry := make([]byte, 16)
		le.PutUint64(entry[0:], uint64(elf.DT_NEEDED))
		le.PutUint64(entry[8:], uint64(str(lib)))
		dynamic = append(dynamic, entry...)
	}
	dynamic = append(dynamic, make([]byte, 16)...) // DT_NULL

	libs := make([]string, 0, len(spec.needs))
	for lib := range spec.needs {
		libs = append(libs, lib)
	}
	sort.Strings(libs)
	var verneed []byte
	for i, lib := range libs {
		versions := spec.needs[lib]
		vn := make([]byte, 16)
		le.PutUint16(vn[0:], 1)
		le.PutUint16(vn[2:], uint16(len(versions)))
		le.PutUint32(vn[4:], str(lib))
		le.PutUint32(vn[8:], 16)
		if i < len(libs)-1 {
			le.PutUint32(vn[12:], uint32(16+16*len(versions)))
		}
		verneed = append(verneed, vn...)
		for j, v := range versions {
			aux := make([]byte, 16)
			if spec.weak[v] {
				le.PutUint16(aux[4:], elfVerFlagWeak)
			}
			le.PutUint16(aux[6:], uint16(2+j))
			le.PutUint32(aux[8:], str(v))
			if j < len(versions)-1 {
				le.PutUint32(aux[12:], 16)
			}
			verneed = append(verneed, aux...)
		}
	}

	var verdef []byte
	if len(spec.defines) > 0 {
		names := append([]string{spec.soname}, spec.defines...)
		for i, name := range names {
			vd := make([]byte, 20)
			le.PutUint16(vd[0:], 1)
			if i == 0 {
				le.PutUint16(vd[2:], elfVerFlagBase)
			}
			le.PutUint16(vd[4:], uint16(i+1))
			le.PutUint16(vd[6:], 1)
			le.PutUint32(vd[12:], 20)
			if i < len(names)-1 {
				le.PutUint32(vd[16:], 28)
			}
			aux := make([]byte, 8)
			le.PutUint32(aux[0:], str(name))
			verdef = append(append(verdef, vd...), aux...)
		}
	}

	type section struct {
		name                string
		typ                 uint32
		data                []byte
		link, info, entsize uint32
	}
	sections := []section{
		{name: ".dynstr", typ: uint32(elf.SHT_STRTAB), data: dynstr},
		{name: ".dynamic", typ: uint32(elf.SHT_DYNAMIC), data: dynamic, link: 1, entsize: 16},
	}
	if len(verneed) > 0 {
		sections = append(sections, section{name: ".gnu.version_r", typ: uint32(elf.SHT_GNU_VERNEED), data: verneed, link: 1, info: uint32(len(libs))})
	}
	if len(verdef) > 0 {
		sections = append(sections, section{name: ".gnu.version_d", typ: uint32(elf.SHT_GNU_VERDEF), data: verdef, link: 1, info: uint32(len(spec.defines) + 1)})
	}
	shstrtab := []byte{0}
	nameOff := make([]uint32, len(sections)+1)
	for i, s := range sections {
		nameOff[i] = uint32(len(shstrtab))
		shstrtab = append(append(shstrtab, s.name...), 0)
	}
	nameOff[len(sections)] = uint32(len(shstrtab))
	shstrtab = append(append(shstrtab, ".shstrtab"...), 0)
	sections = append(sections, section{name: ".shstrtab", typ: uint32(elf.SHT_STRTAB), data: shstrtab})

	file := make([]byte, 64)
	offsets := make([]uint64, len(sections))
	for i, s := range sections {
		for len(file)%8 != 0 {
			file = append(file, 0)
		}
		offsets[i] = uint64(len(file))
		file = append(file, s.data...)
	}
	for len(file)%8 != 0 {
		file = append(file, 0)
	}
	shoff := uint64(len(file))
	file = append(file, make([]byte, 64)...) // the null section
	for i, s := range sections {
		sh := make([]byte, 64)
		le.PutUint32(sh[0:], nameOff[i])
		le.PutUint32(sh[4:], s.typ)
		le.PutUint64(sh[24:], offsets[i])
		le.PutUint64(sh[32:], uint64(len(s.data)))
		le.PutUint32(sh[40:], s.link)
		le.PutUint32(sh[44:], s.info)
		le.PutUint64(sh[48:], 1)
		le.PutUint64(sh[56:], uint64(s.entsize))
		file = append(file, sh...)
	}

	copy(file[0:], []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	le.PutUint16(file[16:], uint16(elf.ET_DYN))
	le.PutUint16(file[18:], uint16(spec.machine))
	le.PutUint32(file[20:], uint32(elf.EV_CURRENT))
	le.PutUint64(file[40:], shoff)
	// e_ehsize, e_phentsize, e_shentsize, then e_shnum (counting the null
	// section) and e_shstrndx (.shstrtab is the last one).
	le.PutUint16(file[52:], 64)
	le.PutUint16(file[54:], 56)
	le.PutUint16(file[58:], 64)
	le.PutUint16(file[60:], uint16(len(sections)+1))
	le.PutUint16(file[62:], uint16(len(sections)))
	return file
}

// buildTestELF32Header is a section-less 32-bit ELF header: enough for the
// architecture to be read, which is all a RetroArch binary is asked for.
func buildTestELF32Header(machine elf.Machine) []byte {
	le := binary.LittleEndian
	file := make([]byte, 52)
	copy(file[0:], []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS32), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	le.PutUint16(file[16:], uint16(elf.ET_EXEC))
	le.PutUint16(file[18:], uint16(machine))
	le.PutUint32(file[20:], uint32(elf.EV_CURRENT))
	le.PutUint16(file[40:], 52) // e_ehsize
	return file
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// testLibc is a firmware libc that defines up to GLIBC_2.33 — the TrimUI
// firmware's since 1.0.4.
func testLibc(t *testing.T, machine elf.Machine) []byte {
	return buildTestELF(t, testELF{
		machine: machine,
		soname:  "libc.so.6",
		defines: []string{"GLIBC_2.17", "GLIBC_2.18", "GLIBC_2.27", "GLIBC_2.28", "GLIBC_2.33", "GLIBC_PRIVATE"},
	})
}

func aarch64Env(dirs ...string) elfLoadEnv {
	return elfLoadEnv{class: elf.ELFCLASS64, machine: elf.EM_AARCH64, libDirs: dirs}
}

func TestELFVersionParsersReadWhatTheBuilderWrote(t *testing.T) {
	data := buildTestELF(t, testELF{
		machine: elf.EM_AARCH64,
		needed:  []string{"libc.so.6", "libstdc++.so.6"},
		needs: map[string][]string{
			"libc.so.6":      {"GLIBC_2.17", "GLIBC_2.34"},
			"libstdc++.so.6": {"GLIBCXX_3.4.30", "CXXABI_1.3.13"},
		},
		weak:    map[string]bool{"GLIBC_2.34": true},
		soname:  "libcore.so",
		defines: []string{"CORE_1.0"},
	})
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the test ELF does not parse: %v", err)
	}
	if libs, err := f.ImportedLibraries(); err != nil || strings.Join(libs, ",") != "libc.so.6,libstdc++.so.6" {
		t.Fatalf("DT_NEEDED = %v, %v", libs, err)
	}
	needs := elfVersionNeeds(f)
	if got := strings.Join(needs["libc.so.6"], ","); got != "GLIBC_2.17" {
		t.Errorf("libc needs = %q, want the weak GLIBC_2.34 left out", got)
	}
	if got := strings.Join(needs["libstdc++.so.6"], ","); got != "GLIBCXX_3.4.30,CXXABI_1.3.13" {
		t.Errorf("libstdc++ needs = %q", got)
	}
	defs := elfVersionDefs(f)
	if len(defs) != 1 || !defs["CORE_1.0"] {
		t.Errorf("defs = %v, want CORE_1.0 without the base entry", defs)
	}
	// A damaged table must end, not loop or read past the data: here the one
	// entry's auxiliary would start where the section does.
	short := make([]byte, 16)
	binary.LittleEndian.PutUint16(short[2:], 1)
	binary.LittleEndian.PutUint32(short[4:], 1)
	binary.LittleEndian.PutUint32(short[8:], 16)
	strtab := append([]byte{0}, "libc.so.6\x00"...)
	if got := parseELFVerneed(short, strtab, binary.LittleEndian, 0); len(got) != 0 {
		t.Errorf("an aux past the data was read: %v", got)
	}
	// Offsets only move forward, and a zero vn_next ends the walk even when
	// sh_info promised more entries than the data holds.
	one := make([]byte, 32)
	binary.LittleEndian.PutUint16(one[2:], 1)
	binary.LittleEndian.PutUint32(one[4:], 1)
	binary.LittleEndian.PutUint32(one[8:], 16)
	binary.LittleEndian.PutUint32(one[24:], 1)
	if got := parseELFVerneed(one, strtab, binary.LittleEndian, 5); len(got) != 1 || len(got["libc.so.6"]) != 1 {
		t.Errorf("one requirement read as %v", got)
	}
	if got := parseELFVerdef([]byte{1, 2, 3}, []byte{0}, binary.LittleEndian, 5); len(got) != 0 {
		t.Errorf("a truncated verdef produced %v", got)
	}
}

func TestELFVersionOrder(t *testing.T) {
	split := []struct {
		in     string
		family string
		nums   string
	}{
		{"GLIBC_2.34", "GLIBC", "2.34"},
		{"GLIBCXX_3.4.30", "GLIBCXX", "3.4.30"},
		{"CXXABI_TM_1", "CXXABI_TM", "1"},
		{"GLIBC_PRIVATE", "GLIBC_PRIVATE", ""},
		{"X", "X", ""},
	}
	for _, c := range split {
		family, nums := elfVersionSplit(c.in)
		parts := make([]string, 0, len(nums))
		for _, n := range nums {
			parts = append(parts, itoa(n))
		}
		if family != c.family || strings.Join(parts, ".") != c.nums {
			t.Errorf("split(%q) = %q %v, want %q %q", c.in, family, nums, c.family, c.nums)
		}
	}
	less := []struct {
		a, b string
		want bool
	}{
		{"GLIBC_2.9", "GLIBC_2.17", true},
		{"GLIBC_2.17", "GLIBC_2.17.1", true},
		{"GLIBC_2.34", "GLIBC_2.4", false},
		{"GLIBC_2.33", "GLIBC_2.33", false},
	}
	for _, c := range less {
		if got := elfVersionLess(c.a, c.b); got != c.want {
			t.Errorf("less(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	defs := map[string]bool{"GLIBC_2.4": true, "GLIBC_2.33": true, "GLIBC_2.17": true, "GLIBC_PRIVATE": true, "GLIBCXX_3.4": true}
	if got := elfHighestInFamily(defs, "GLIBC"); got != "GLIBC_2.33" {
		t.Errorf("highest GLIBC = %q, want GLIBC_2.33", got)
	}
}

func TestELFVersionGapNamesTheNewestMissingVersion(t *testing.T) {
	defs := map[string]bool{"GLIBC_2.17": true, "GLIBC_2.28": true, "GLIBC_2.33": true}
	got := elfVersionGap("/lib/libc.so.6", []string{"GLIBC_2.17", "GLIBC_2.34", "GLIBC_2.38"}, defs)
	if got != "needs GLIBC_2.38; this firmware's libc.so.6 has up to GLIBC_2.33" {
		t.Errorf("gap = %q", got)
	}
	if got := elfVersionGap("libc.so.6", []string{"GLIBC_2.17", "GLIBC_2.33"}, defs); got != "" {
		t.Errorf("satisfied needs reported %q", got)
	}
	if got := elfVersionGap("libc.so.6", []string{"GLIBC_2.99"}, nil); got != "" {
		t.Errorf("a library with no version definitions was judged: %q", got)
	}
	cxx := map[string]bool{"GLIBCXX_3.4.28": true, "CXXABI_1.3.12": true}
	if got := elfVersionGap("libstdc++.so.6", []string{"CXXABI_1.3.13", "GLIBCXX_3.4.30"}, cxx); got != "needs GLIBCXX_3.4.30; this firmware's libstdc++.so.6 has up to GLIBCXX_3.4.28" {
		t.Errorf("libstdc++ gap = %q", got)
	}
	if got := elfVersionGap("libgcc_s.so.1", []string{"GCC_12.0.0"}, map[string]bool{"GLIBC_2.17": true}); !strings.Contains(got, "does not provide") {
		t.Errorf("a family the library lacks = %q", got)
	}
}

// TestELFLoadProblemComparesWithTheFirmwareLibrary is the case the check
// exists for: a nightly that needs GLIBC_2.34 on a firmware whose libc stops
// at 2.33 is refused with a message that says so, and one within 2.33 is not.
func TestELFLoadProblemComparesWithTheFirmwareLibrary(t *testing.T) {
	lib := t.TempDir()
	writeTestFile(t, filepath.Join(lib, "libc.so.6"), testLibc(t, elf.EM_AARCH64))
	env := aarch64Env(lib)

	tooNew := buildTestELF(t, testELF{
		machine: elf.EM_AARCH64,
		needed:  []string{"libc.so.6", "libretro_helper.so"},
		needs:   map[string][]string{"libc.so.6": {"GLIBC_2.17", "GLIBC_2.34"}},
	})
	got := elfLoadProblemFrom(bytes.NewReader(tooNew), env)
	if got != "needs GLIBC_2.34; this firmware's libc.so.6 has up to GLIBC_2.33" {
		t.Fatalf("problem = %q", got)
	}
	fits := buildTestELF(t, testELF{
		machine: elf.EM_AARCH64,
		needed:  []string{"libc.so.6"},
		needs:   map[string][]string{"libc.so.6": {"GLIBC_2.17", "GLIBC_2.28"}},
	})
	if got := elfLoadProblemFrom(bytes.NewReader(fits), env); got != "" {
		t.Errorf("a build within the firmware's libc was refused: %q", got)
	}
	// A well-known system library that is nowhere is a fact about the
	// firmware; an unknown one may come with the process that loads the core.
	noStd := buildTestELF(t, testELF{machine: elf.EM_AARCH64, needed: []string{"libc.so.6", "libstdc++.so.6", "libGLESv2.so.2"}})
	got = elfLoadProblemFrom(bytes.NewReader(noStd), env)
	if !strings.Contains(got, "libstdc++.so.6") || strings.Contains(got, "libGLESv2") {
		t.Errorf("missing libraries = %q, want libstdc++ named and GLES left alone", got)
	}
}

func TestELFLoadProblemChecksTheArchitecture(t *testing.T) {
	desktop := buildTestELF(t, testELF{machine: elf.EM_X86_64})
	got := elfLoadProblemFrom(bytes.NewReader(desktop), aarch64Env())
	if !strings.Contains(got, "x86-64") || !strings.Contains(got, "64-bit ARM") {
		t.Errorf("an x86-64 build for an aarch64 device = %q", got)
	}
	for _, notELF := range [][]byte{[]byte("the build that was running"), nil, {0x7f, 'E', 'L'}} {
		if got := elfLoadProblemFrom(bytes.NewReader(notELF), aarch64Env()); got != "" {
			t.Errorf("a file that is not ELF (%q) was judged: %q", notELF, got)
		}
	}
	// archOnly compares the architecture and nothing else.
	needy := buildTestELF(t, testELF{machine: elf.EM_AARCH64, needed: []string{"libc.so.6"}})
	env := aarch64Env()
	env.archOnly = true
	if got := elfLoadProblemFrom(bytes.NewReader(needy), env); got != "" {
		t.Errorf("archOnly looked at the libraries: %q", got)
	}
}

// The loader passes over a library of another architecture; so does the check.
func TestELFFindLibrarySkipsAnotherArchitecture(t *testing.T) {
	wrong, right := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(wrong, "libc.so.6"), testLibc(t, elf.EM_X86_64))
	writeTestFile(t, filepath.Join(right, "libc.so.6"), testLibc(t, elf.EM_AARCH64))
	info, state := elfFindLibrary("libc.so.6", elf.ELFCLASS64, elf.EM_AARCH64, []string{wrong, right})
	if state != elfLibFound || !info.defs["GLIBC_2.33"] {
		t.Fatalf("state = %v, defs = %v; want the aarch64 libc from the second folder", state, info.defs)
	}
	if _, state := elfFindLibrary("libc.so.6", elf.ELFCLASS64, elf.EM_AARCH64, []string{wrong}); state != elfLibMissing {
		t.Errorf("only a foreign libc: state = %v, want missing", state)
	}
	// A file that is there but not ELF stops the search without a verdict.
	odd := t.TempDir()
	writeTestFile(t, filepath.Join(odd, "libc.so.6"), []byte("GROUP ( /lib/libc.so.6 )"))
	if _, state := elfFindLibrary("libc.so.6", elf.ELFCLASS64, elf.EM_AARCH64, []string{odd, right}); state != elfLibUnknown {
		t.Errorf("an unreadable libc: state = %v, want unknown", state)
	}
}

// The firmware's LD_LIBRARY_PATH comes before the system folders, as it does
// for the loader that starts RetroArch with it.
func TestELFLibrarySearchDirsOrder(t *testing.T) {
	got := elfLibrarySearchDirs([]string{"/bundle/lib", " "}, "/orig/lib:/lib", "/cur/lib::/orig/lib")
	want := []string{"/bundle/lib", "/orig/lib", "/lib", "/usr/lib", "/lib64", "/usr/lib64", "/usr/trimui/lib", "/cur/lib"}
	for i := range want {
		want[i] = filepath.Clean(want[i])
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("search dirs = %v, want %v", got, want)
	}
}

func TestELFHostArchitectures(t *testing.T) {
	if c, m := elfHostArch("arm64"); c != elf.ELFCLASS64 || m != elf.EM_AARCH64 {
		t.Errorf("arm64 = %v %v", c, m)
	}
	if c, m := elfHostArch("mips"); c != elf.ELFCLASSNONE || m != elf.EM_NONE {
		t.Errorf("an unknown architecture = %v %v, want no check", c, m)
	}
	if !elfHostRuns(elf.ELFCLASS64, elf.EM_AARCH64, elf.ELFCLASS32, elf.EM_ARM) {
		t.Error("an aarch64 device runs 32-bit ARM (ra32.trimui)")
	}
	if elfHostRuns(elf.ELFCLASS64, elf.EM_AARCH64, elf.ELFCLASS64, elf.EM_X86_64) {
		t.Error("an aarch64 device does not run x86-64")
	}
}

// nativeLoadProblem is the exported door: on Linux it reads the file, and
// everywhere else it says nothing.
func TestNativeLoadProblemOnThisPlatform(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.so")
	writeTestFile(t, foreign, buildTestELF(t, testELF{machine: elf.EM_PPC64}))
	text := filepath.Join(dir, "launch.sh")
	writeTestFile(t, text, []byte("#!/bin/sh\n"))
	if got := nativeLoadProblem(text); got != "" {
		t.Errorf("a script was judged: %q", got)
	}
	if got := nativeLoadProblemWith(filepath.Join(dir, "missing"), []string{dir}); got != "" {
		t.Errorf("a missing file was judged: %q", got)
	}
	got := nativeLoadProblem(foreign)
	if _, hostMachine := elfHostArch(runtime.GOARCH); runtime.GOOS != "linux" || hostMachine == elf.EM_NONE {
		if got != "" {
			t.Errorf("no check applies here, but got %q", got)
		}
		return
	}
	if !strings.Contains(got, "build, and this device needs") {
		t.Errorf("a PowerPC build on this device = %q", got)
	}
	// The answer is cached by path, size and time, and stays the same.
	if again := nativeLoadProblem(foreign); again != got {
		t.Errorf("second answer %q differs from %q", again, got)
	}
}

// A core must match the RetroArch that loads it, which on early firmware was a
// 32-bit build on the 64-bit handheld: the architecture comes from the
// RetroArch binary beside the cores folder, and then only it is compared.
func TestCoreLoadEnvFollowsTheRetroArchBinary(t *testing.T) {
	var sibling elf.Machine
	switch runtime.GOARCH {
	case "arm64":
		sibling = elf.EM_ARM
	case "amd64":
		sibling = elf.EM_386
	default:
		t.Skip("no 32-bit sibling architecture known for " + runtime.GOARCH)
	}
	root := t.TempDir()
	cores := filepath.Join(root, "RetroArch", ".retroarch", "cores")
	target := filepath.Join(cores, "mgba_libretro.so")
	writeTestFile(t, target, []byte("the build that was running"))

	hostClass, hostMachine := elfHostArch(runtime.GOARCH)
	if env := coreLoadEnv(target); env.class != hostClass || env.machine != hostMachine || env.archOnly {
		t.Fatalf("no RetroArch binary: env = %+v, want this process's architecture", env)
	}
	writeTestFile(t, filepath.Join(root, "RetroArch", "ra32.trimui"), buildTestELF32Header(sibling))
	env := coreLoadEnv(target)
	if env.class != elf.ELFCLASS32 || env.machine != sibling || !env.archOnly {
		t.Fatalf("a 32-bit RetroArch: env = %+v, want its architecture, compared alone", env)
	}
	if env.origin != cores {
		t.Errorf("origin = %q, want the cores folder", env.origin)
	}
	if runtime.GOOS == "linux" {
		native := buildTestELF(t, testELF{machine: hostMachine})
		if got := coreLoadProblemData(native, target); !strings.Contains(got, "32-bit") {
			t.Errorf("a %s core for a 32-bit RetroArch = %q", elfArchName(hostClass, hostMachine), got)
		}
	}
}

// A passing answer is kept only while the libraries it relied on are the same
// files: a bundle whose lib/ is replaced under an unchanged binary is checked
// again.
func TestNativeLoadProblemRechecksWhenALibraryChanges(t *testing.T) {
	hostClass, hostMachine := elfHostArch(runtime.GOARCH)
	if runtime.GOOS != "linux" || hostMachine == elf.EM_NONE || hostClass != elf.ELFCLASS64 {
		t.Skip("the check applies to Linux; the test files are 64-bit")
	}
	bundle := t.TempDir()
	libDir := filepath.Join(bundle, "lib")
	lib := filepath.Join(libDir, "libc.so.6")
	writeTestFile(t, lib, buildTestELF(t, testELF{machine: hostMachine, soname: "libc.so.6", defines: []string{"GLIBC_2.17", "GLIBC_2.34"}}))
	binary := filepath.Join(bundle, "emulator")
	writeTestFile(t, binary, buildTestELF(t, testELF{
		machine: hostMachine,
		needed:  []string{"libc.so.6"},
		needs:   map[string][]string{"libc.so.6": {"GLIBC_2.34"}},
	}))
	if got := nativeLoadProblemWith(binary, []string{libDir}); got != "" {
		t.Fatalf("with its own libc the bundle was refused: %q", got)
	}
	writeTestFile(t, lib, testLibc(t, hostMachine))
	if got := nativeLoadProblemWith(binary, []string{libDir}); !strings.Contains(got, "GLIBC_2.34") {
		t.Fatalf("after its libc was replaced by an older one: %q, want the missing version named", got)
	}
}
