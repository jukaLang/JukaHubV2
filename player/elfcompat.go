package main

// elfcompat.go — will this binary load on this firmware?
//
// A libretro core is a shared object RetroArch opens with dlopen, and a
// standalone emulator is an executable the firmware's loader starts. Either one
// fails the same way when it was built for a newer system than the handheld
// runs: the loader reads the binary's list of required libraries and the symbol
// versions it needs from each ("GLIBC_2.34", "GLIBCXX_3.4.30"), finds that the
// firmware's libc.so.6 only defines versions up to GLIBC_2.33, and refuses. In
// RetroArch that is a game that will not start and a log line nobody on the
// handheld can read; for an automatic update it would be every game that core
// runs, broken overnight.
//
// The libretro buildbot builds its linux/aarch64 nightlies on whatever its
// build image is this month, not against the TrimUI firmware's userspace (glibc
// 2.33 since firmware 1.0.4, 2.29 before it), so "the index has a newer build"
// is not the same as "this device can run it". This file answers the second
// question before anything is replaced, from the binary's own ELF metadata:
//
//   - the ELF class and machine must match what this process runs as (an
//     aarch64 build on the handheld, never an x86-64 or 32-bit one);
//   - every library in DT_NEEDED is looked for where the device's loader looks
//     — a bundle's own lib/ first when the caller has one, then the
//     LD_LIBRARY_PATH the firmware launched JukaHub with (launch.sh keeps it as
//     JUKAHUB_ORIG_LD_LIBRARY_PATH, and RetroArch, like every screen-taking
//     child, runs with it again — childenv.go), which the loader reads before
//     its default folders, then /lib, /usr/lib, /lib64, /usr/lib64,
//     /usr/trimui/lib, then JukaHub's own;
//   - for each library found, the versions the binary needs of it
//     (.gnu.version_r) must all be versions that library defines
//     (.gnu.version_d).
//
// A library found nowhere is reported only when it is one every system has
// (libc, libm, libstdc++, libgcc_s, libpthread, libdl, librt, libz): anything
// else may be provided by the process that loads the binary — RetroArch has
// SDL, GL and its own libraries loaded before it opens a core — so its absence
// from the folders read here proves nothing.
//
// The version tables are read here with encoding/binary rather than through
// debug/elf's symbol-version API, whose shape has changed between Go releases;
// debug/elf is used only for the header, the section table and DT_NEEDED, which
// it has always provided. The check is deliberately one-sided: whenever it
// cannot tell (not Linux, not an ELF file, a library it cannot read, a library
// without version definitions), it says nothing, so it can only ever stop an
// install it has positive evidence against — never one that would have worked.

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// elfCheckMaxSize bounds what the check opens: a core or an emulator is
	// tens of megabytes, and nothing larger is read on the device's behalf.
	elfCheckMaxSize = 256 << 20
	// elfVerFlagBase marks the version definition that names the library
	// itself rather than a symbol version.
	elfVerFlagBase = 0x1
	// elfVerFlagWeak marks a version requirement the loader only warns about.
	elfVerFlagWeak = 0x2
	// elfVerMaxEntries bounds a walk along a version chain, so a damaged file
	// whose next-offsets loop cannot hang the check.
	elfVerMaxEntries = 4096
	// elfCacheMax bounds each of the caches below.
	elfCacheMax = 512
)

// elfSystemLibDirs are the folders the firmware's loader reads system libraries
// from, in the order they are searched after a caller's own folders and the
// firmware's LD_LIBRARY_PATH.
var elfSystemLibDirs = []string{"/lib", "/usr/lib", "/lib64", "/usr/lib64", "/usr/trimui/lib"}

// nativeLoadProblem reports why the ELF at path could not be loaded on this device ("" when it can, or when
// the check does not apply: not Linux, not ELF, unreadable system libraries).
func nativeLoadProblem(path string) string {
	return nativeLoadProblemWith(path, nil)
}

// nativeLoadProblemWith is nativeLoadProblem with extra library folders searched first (a bundle's own lib/).
func nativeLoadProblemWith(path string, extraLibDirs []string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > elfCheckMaxSize {
		return ""
	}
	key := path + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00" + strings.Join(extraLibDirs, "\x00")
	elfResultCache.mu.Lock()
	libs, cached := elfResultCache.results[key]
	elfResultCache.mu.Unlock()
	if cached && elfLibStampsCurrent(libs) {
		return ""
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	env := elfHostEnv(extraLibDirs)
	env.origin = filepath.Dir(path)
	var used []elfLibStamp
	env.used = &used
	problem := elfLoadProblemFrom(f, env)
	_ = f.Close()

	elfResultCache.mu.Lock()
	if problem == "" {
		if elfResultCache.results == nil || len(elfResultCache.results) >= elfCacheMax {
			elfResultCache.results = make(map[string][]elfLibStamp, 32)
		}
		elfResultCache.results[key] = used
	} else {
		delete(elfResultCache.results, key)
	}
	elfResultCache.mu.Unlock()
	return problem
}

// elfLibStamp is a library a passing answer relied on, as it was then.
type elfLibStamp struct {
	path string
	size int64
	mod  time.Time
}

// elfLibStampsCurrent reports whether every library is still the file it was.
func elfLibStampsCurrent(libs []elfLibStamp) bool {
	for _, lib := range libs {
		st, err := os.Stat(lib.path)
		if err != nil || st.Size() != lib.size || !st.ModTime().Equal(lib.mod) {
			return false
		}
	}
	return true
}

// nativeLoadProblemData is nativeLoadProblemWith for a binary still in memory —
// a download that has passed its checksum and is not on the card yet, so a
// build that cannot load is never written anywhere.
func nativeLoadProblemData(data []byte, extraLibDirs []string) string {
	if runtime.GOOS != "linux" || len(data) > elfCheckMaxSize {
		return ""
	}
	return elfLoadProblemFrom(bytes.NewReader(data), elfHostEnv(extraLibDirs))
}

// elfResultCache remembers the answers that found no problem, by the binary's
// path, size and time and the extra folders, with the size and time of every
// library the answer relied on: a library replaced under an unchanged binary (a
// bundle's lib/ refreshed) has the binary checked again. An answer that found a
// problem is not kept — it is cheap to repeat, and a bundle that gains the
// missing library has to be able to pass.
var elfResultCache struct {
	mu      sync.Mutex
	results map[string][]elfLibStamp
}

// elfLoadEnv is what a binary has to match to load: a class and machine
// (ELFCLASSNONE / EM_NONE skip that check) and the library folders in search
// order. origin is the folder the binary is loaded from, for a $ORIGIN in its
// own run path ("" when unknown). archOnly stops after the architecture: the
// system folders hold this process's architecture, so another one's libraries
// (a 32-bit RetroArch's) are not where the check would look for them. used, when
// set, collects the libraries the answer relied on.
type elfLoadEnv struct {
	class    elf.Class
	machine  elf.Machine
	libDirs  []string
	origin   string
	archOnly bool
	used     *[]elfLibStamp
}

// elfHostEnv is the environment of the running process.
func elfHostEnv(extraLibDirs []string) elfLoadEnv {
	class, machine := elfHostArch(runtime.GOARCH)
	return elfLoadEnv{
		class:   class,
		machine: machine,
		libDirs: elfLibrarySearchDirs(extraLibDirs, os.Getenv("JUKAHUB_ORIG_LD_LIBRARY_PATH"), os.Getenv("LD_LIBRARY_PATH")),
	}
}

// elfHostArch is the ELF class and machine a Go architecture runs.
func elfHostArch(goarch string) (elf.Class, elf.Machine) {
	switch goarch {
	case "arm64":
		return elf.ELFCLASS64, elf.EM_AARCH64
	case "amd64":
		return elf.ELFCLASS64, elf.EM_X86_64
	case "arm":
		return elf.ELFCLASS32, elf.EM_ARM
	case "386":
		return elf.ELFCLASS32, elf.EM_386
	}
	return elf.ELFCLASSNONE, elf.EM_NONE
}

// elfHostRuns reports whether a device of the host architecture runs binaries
// of class and machine: its own, and the 32-bit sibling its kernel can also run
// (an aarch64 handheld ran ra32.trimui, a 32-bit RetroArch, on early firmware).
func elfHostRuns(hostClass elf.Class, hostMachine elf.Machine, class elf.Class, machine elf.Machine) bool {
	if class == hostClass && machine == hostMachine {
		return true
	}
	switch {
	case hostMachine == elf.EM_AARCH64 && machine == elf.EM_ARM && class == elf.ELFCLASS32:
		return true
	case hostMachine == elf.EM_X86_64 && machine == elf.EM_386 && class == elf.ELFCLASS32:
		return true
	}
	return false
}

// elfArchName names an architecture the way a message to the user should.
func elfArchName(class elf.Class, machine elf.Machine) string {
	switch machine {
	case elf.EM_AARCH64:
		return "64-bit ARM (aarch64)"
	case elf.EM_ARM:
		return "32-bit ARM"
	case elf.EM_X86_64:
		return "x86-64"
	case elf.EM_386:
		return "32-bit x86"
	}
	bits := ""
	switch class {
	case elf.ELFCLASS32:
		bits = "32-bit "
	case elf.ELFCLASS64:
		bits = "64-bit "
	}
	return bits + machine.String()
}

// elfLibrarySearchDirs is the folder order libraries are looked up in: the
// caller's own folders, the LD_LIBRARY_PATH the firmware launched JukaHub with,
// the firmware's system folders, then JukaHub's own. The firmware's path comes
// before the system folders because that is the loader's order for RetroArch,
// which runs with it: on CrossMix it starts with /mnt/SDCARD/System/lib, and a
// newer libstdc++ there is the one a core gets, not the older one in /usr/lib.
// Empty and repeated entries are dropped.
func elfLibrarySearchDirs(extra []string, origLD, currentLD string) []string {
	out := make([]string, 0, len(extra)+len(elfSystemLibDirs)+8)
	seen := map[string]bool{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, dir := range extra {
		add(dir)
	}
	for _, dir := range strings.Split(origLD, ":") {
		add(dir)
	}
	for _, dir := range elfSystemLibDirs {
		add(dir)
	}
	for _, dir := range strings.Split(currentLD, ":") {
		add(dir)
	}
	return out
}

// elfLoadProblemFrom is the check itself, on any reader and environment, so the
// tests can run it on a binary built in memory.
func elfLoadProblemFrom(r io.ReaderAt, env elfLoadEnv) string {
	f, err := elf.NewFile(r)
	if err != nil {
		// Not an ELF file (a Windows DLL, a script, a test fixture), or one
		// debug/elf cannot read: the check does not apply.
		return ""
	}
	if problem := elfArchProblem(f.Class, f.Machine, env.class, env.machine); problem != "" {
		return problem
	}
	if env.archOnly {
		return ""
	}
	needed, _ := f.ImportedLibraries()
	needs := elfVersionNeeds(f)
	// A binary that carries its own run path (a bundle with "$ORIGIN/lib")
	// finds its libraries there before anywhere else.
	dirs := env.libDirs
	if own := elfRunPaths(f, env.origin); len(own) > 0 {
		dirs = append(own, env.libDirs...)
	}
	var problems []string
	for _, lib := range elfNeededOrder(needed, needs) {
		info, state := elfFindLibrary(lib, f.Class, f.Machine, dirs)
		if env.used != nil && state != elfLibMissing {
			*env.used = append(*env.used, elfLibStamp{path: info.path, size: info.size, mod: info.mod})
		}
		switch state {
		case elfLibMissing:
			if elfWellKnownSystemLib(lib) {
				problems = append(problems, "needs "+lib+", which this firmware does not have")
			}
		case elfLibFound:
			if gap := elfVersionGap(lib, needs[lib], info.defs); gap != "" {
				problems = append(problems, gap)
			}
		}
	}
	return elfJoinProblems(problems)
}

// elfRunPaths reads the binary's DT_RUNPATH and DT_RPATH folders, with $ORIGIN
// resolved against origin (entries that need it are dropped when origin is "").
func elfRunPaths(f *elf.File, origin string) []string {
	var out []string
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		values, err := f.DynString(tag)
		if err != nil {
			continue
		}
		for _, value := range values {
			for _, dir := range strings.Split(value, ":") {
				if strings.Contains(dir, "ORIGIN") {
					if origin == "" {
						continue
					}
					dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", origin), "$ORIGIN", origin)
				}
				if dir = strings.TrimSpace(dir); dir != "" {
					out = append(out, dir)
				}
			}
		}
	}
	return out
}

// elfArchProblem compares a binary's class and machine with what the process
// that loads it runs as.
func elfArchProblem(class elf.Class, machine elf.Machine, wantClass elf.Class, wantMachine elf.Machine) string {
	if (wantMachine != elf.EM_NONE && machine != wantMachine) || (wantClass != elf.ELFCLASSNONE && class != wantClass) {
		name := elfArchName(class, machine)
		article := "a "
		if strings.HasPrefix(name, "x") {
			article = "an "
		}
		return "it is " + article + name + " build, and this device needs " + elfArchName(wantClass, wantMachine)
	}
	return ""
}

// elfArchOf reads only the ELF header of r. ok is false for anything that is
// not ELF.
func elfArchOf(r io.ReaderAt) (elf.Class, elf.Machine, bool) {
	f, err := elf.NewFile(r)
	if err != nil {
		return elf.ELFCLASSNONE, elf.EM_NONE, false
	}
	return f.Class, f.Machine, true
}

// elfArchOfFile is elfArchOf for a file on the card.
func elfArchOfFile(path string) (elf.Class, elf.Machine, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return elf.ELFCLASSNONE, elf.EM_NONE, false
	}
	defer fh.Close()
	return elfArchOf(fh)
}

// elfNeededOrder lists the libraries to check: DT_NEEDED in the binary's own
// order, then any library the version table names that DT_NEEDED did not.
func elfNeededOrder(needed []string, needs map[string][]string) []string {
	out := make([]string, 0, len(needed)+len(needs))
	seen := map[string]bool{}
	for _, lib := range needed {
		if lib != "" && !seen[lib] {
			seen[lib] = true
			out = append(out, lib)
		}
	}
	var rest []string
	for lib := range needs {
		if !seen[lib] {
			rest = append(rest, lib)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// elfWellKnownSystemLib reports a library every Linux userspace provides, whose
// absence is therefore a fact about this firmware and not about what the
// loading process brings with it.
func elfWellKnownSystemLib(name string) bool {
	base := filepath.Base(name)
	stem := base
	if i := strings.Index(base, ".so"); i >= 0 {
		stem = base[:i]
	}
	switch stem {
	case "libc", "libm", "libstdc++", "libgcc_s", "libpthread", "libdl", "librt", "libz":
		return true
	}
	return false
}

// elfLibState is the outcome of looking a library up.
type elfLibState int

const (
	elfLibMissing elfLibState = iota
	elfLibFound
	// elfLibUnknown is a file that is there but could not be read as ELF: the
	// check stands aside rather than guess.
	elfLibUnknown
)

// elfLibInfo is what the check needs of one library file.
type elfLibInfo struct {
	path    string
	exists  bool
	parsed  bool
	class   elf.Class
	machine elf.Machine
	// defs is the set of versions the library defines; nil when it defines none
	// (then nothing is compared against it).
	defs map[string]bool
	size int64
	mod  time.Time
}

// elfLibCache holds the device's libraries once per process: they do not change
// while JukaHub runs, and every core check reads the same libc. An entry is
// re-read only when the file's size or time changed.
var elfLibCache struct {
	mu   sync.Mutex
	libs map[string]elfLibInfo
}

// elfFindLibrary looks a DT_NEEDED name up in dirs the way the loader does: the
// first file of the binary's own class and machine wins, and one of another
// architecture (a 32-bit libc in a mixed tree) is passed over.
func elfFindLibrary(name string, class elf.Class, machine elf.Machine, dirs []string) (elfLibInfo, elfLibState) {
	candidates := make([]string, 0, len(dirs))
	if strings.Contains(name, "/") {
		candidates = append(candidates, name)
	} else {
		for _, dir := range dirs {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	for _, path := range candidates {
		info := elfLibraryInfo(path)
		if !info.exists {
			continue
		}
		if !info.parsed {
			return info, elfLibUnknown
		}
		if info.class != class || info.machine != machine {
			continue
		}
		return info, elfLibFound
	}
	return elfLibInfo{}, elfLibMissing
}

// elfLibraryInfo reads (or recalls) one library file.
func elfLibraryInfo(path string) elfLibInfo {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return elfLibInfo{}
	}
	elfLibCache.mu.Lock()
	if hit, ok := elfLibCache.libs[path]; ok && hit.size == st.Size() && hit.mod.Equal(st.ModTime()) {
		elfLibCache.mu.Unlock()
		return hit
	}
	elfLibCache.mu.Unlock()

	info := elfLibInfo{path: path, exists: true, size: st.Size(), mod: st.ModTime()}
	if st.Size() <= elfCheckMaxSize {
		if fh, err := os.Open(path); err == nil {
			if f, err := elf.NewFile(fh); err == nil {
				info.parsed = true
				info.class, info.machine = f.Class, f.Machine
				info.defs = elfVersionDefs(f)
			}
			_ = fh.Close()
		}
	}
	elfLibCache.mu.Lock()
	if elfLibCache.libs == nil || len(elfLibCache.libs) >= elfCacheMax {
		elfLibCache.libs = make(map[string]elfLibInfo, 32)
	}
	elfLibCache.libs[path] = info
	elfLibCache.mu.Unlock()
	return info
}

// elfSectionOfType is the first section of a type, or nil.
func elfSectionOfType(f *elf.File, typ elf.SectionType) *elf.Section {
	for _, s := range f.Sections {
		if s.Type == typ {
			return s
		}
	}
	return nil
}

// elfLinkedStrings is the string table a version section names in sh_link.
func elfLinkedStrings(f *elf.File, s *elf.Section) []byte {
	link := int(s.Link)
	if link <= 0 || link >= len(f.Sections) {
		return nil
	}
	data, err := f.Sections[link].Data()
	if err != nil {
		return nil
	}
	return data
}

// elfVersionNeeds reads .gnu.version_r: for each library, the symbol versions
// the binary requires of it.
func elfVersionNeeds(f *elf.File) map[string][]string {
	s := elfSectionOfType(f, elf.SHT_GNU_VERNEED)
	if s == nil {
		return nil
	}
	data, err := s.Data()
	if err != nil {
		return nil
	}
	strtab := elfLinkedStrings(f, s)
	if strtab == nil {
		return nil
	}
	return parseELFVerneed(data, strtab, f.ByteOrder, int(s.Info))
}

// elfVersionDefs reads .gnu.version_d: the versions a library defines.
func elfVersionDefs(f *elf.File) map[string]bool {
	s := elfSectionOfType(f, elf.SHT_GNU_VERDEF)
	if s == nil {
		return nil
	}
	data, err := s.Data()
	if err != nil {
		return nil
	}
	strtab := elfLinkedStrings(f, s)
	if strtab == nil {
		return nil
	}
	defs := parseELFVerdef(data, strtab, f.ByteOrder, int(s.Info))
	if len(defs) == 0 {
		return nil
	}
	return defs
}

// parseELFVerneed walks Elf_Verneed entries (vn_version u16, vn_cnt u16,
// vn_file u32, vn_aux u32, vn_next u32) and their Elf_Vernaux entries
// (vna_hash u32, vna_flags u16, vna_other u16, vna_name u32, vna_next u32) —
// the same 16-byte layouts in 32- and 64-bit files. count is sh_info, the
// number of entries; 0 means "follow vn_next until it ends". Weak requirements
// are left out: the loader only warns about those.
func parseELFVerneed(data, strtab []byte, order binary.ByteOrder, count int) map[string][]string {
	out := map[string][]string{}
	size := uint64(len(data))
	var off uint64
	for i := 0; i < elfVerMaxEntries; i++ {
		if off+16 > size {
			break
		}
		cnt := int(order.Uint16(data[off+2:]))
		file, fileOK := elfString(strtab, order.Uint32(data[off+4:]))
		aux := uint64(order.Uint32(data[off+8:]))
		next := uint64(order.Uint32(data[off+12:]))
		if fileOK && file != "" && aux != 0 {
			a := off + aux
			for j := 0; j < cnt && j < elfVerMaxEntries; j++ {
				if a+16 > size {
					break
				}
				flags := order.Uint16(data[a+4:])
				if name, ok := elfString(strtab, order.Uint32(data[a+8:])); ok && name != "" && flags&elfVerFlagWeak == 0 {
					out[file] = append(out[file], name)
				}
				an := uint64(order.Uint32(data[a+12:]))
				if an == 0 {
					break
				}
				a += an
			}
		}
		if next == 0 || (count > 0 && i+1 >= count) {
			break
		}
		off += next
	}
	return out
}

// parseELFVerdef walks Elf_Verdef entries (vd_version u16, vd_flags u16,
// vd_ndx u16, vd_cnt u16, vd_hash u32, vd_aux u32, vd_next u32 — 20 bytes)
// and reads each one's first Elf_Verdaux (vda_name u32, vda_next u32), which
// names the version; later auxiliaries name its parents. The base entry names
// the library itself and is not a version.
func parseELFVerdef(data, strtab []byte, order binary.ByteOrder, count int) map[string]bool {
	out := map[string]bool{}
	size := uint64(len(data))
	var off uint64
	for i := 0; i < elfVerMaxEntries; i++ {
		if off+20 > size {
			break
		}
		flags := order.Uint16(data[off+2:])
		cnt := order.Uint16(data[off+6:])
		aux := uint64(order.Uint32(data[off+12:]))
		next := uint64(order.Uint32(data[off+16:]))
		if flags&elfVerFlagBase == 0 && cnt > 0 && aux != 0 {
			if a := off + aux; a+8 <= size {
				if name, ok := elfString(strtab, order.Uint32(data[a:])); ok && name != "" {
					out[name] = true
				}
			}
		}
		if next == 0 || (count > 0 && i+1 >= count) {
			break
		}
		off += next
	}
	return out
}

// elfString reads the NUL-terminated string at off in a string table.
func elfString(strtab []byte, off uint32) (string, bool) {
	if uint64(off) >= uint64(len(strtab)) {
		return "", false
	}
	rest := strtab[off:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", false
	}
	return string(rest[:end]), true
}

// elfVersionSplit splits "GLIBC_2.34" into the family "GLIBC" and the numbers
// [2 34]. A name with no numeric part ("GLIBC_PRIVATE") is all family.
func elfVersionSplit(v string) (string, []int) {
	for i := len(v) - 2; i >= 0; i-- {
		if v[i] == '_' && v[i+1] >= '0' && v[i+1] <= '9' {
			var nums []int
			for _, part := range strings.Split(v[i+1:], ".") {
				n := 0
				digits := 0
				for digits < len(part) && part[digits] >= '0' && part[digits] <= '9' {
					n = n*10 + int(part[digits]-'0')
					digits++
				}
				if digits == 0 {
					break
				}
				nums = append(nums, n)
			}
			return v[:i], nums
		}
	}
	return v, nil
}

// elfVersionLess orders versions by family, then numerically ("GLIBC_2.9" <
// "GLIBC_2.17" < "GLIBC_2.17.1").
func elfVersionLess(a, b string) bool {
	fa, na := elfVersionSplit(a)
	fb, nb := elfVersionSplit(b)
	if fa != fb {
		return fa < fb
	}
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return a < b
}

// elfHighestInFamily is the newest numbered version of a family in defs.
func elfHighestInFamily(defs map[string]bool, family string) string {
	best := ""
	for v := range defs {
		f, nums := elfVersionSplit(v)
		if f != family || len(nums) == 0 {
			continue
		}
		if best == "" || elfVersionLess(best, v) {
			best = v
		}
	}
	return best
}

// elfVersionGap compares what a binary needs of one library with what that
// library defines and words the newest thing missing:
// "needs GLIBC_2.38; this firmware's libc.so.6 has up to GLIBC_2.33".
func elfVersionGap(lib string, needed []string, defs map[string]bool) string {
	if len(needed) == 0 || len(defs) == 0 {
		return ""
	}
	var missing []string
	for _, v := range needed {
		if !defs[v] {
			missing = append(missing, v)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	// The family named in the message is the one that sorts last (GLIBCXX
	// before CXXABI for libstdc++), then its newest missing version.
	sort.Slice(missing, func(i, j int) bool {
		fi, _ := elfVersionSplit(missing[i])
		fj, _ := elfVersionSplit(missing[j])
		if fi != fj {
			return fi > fj
		}
		return elfVersionLess(missing[j], missing[i])
	})
	top := missing[0]
	family, _ := elfVersionSplit(top)
	name := filepath.Base(lib)
	if have := elfHighestInFamily(defs, family); have != "" {
		return fmt.Sprintf("needs %s; this firmware's %s has up to %s", top, name, have)
	}
	return fmt.Sprintf("needs %s, which this firmware's %s does not provide", top, name)
}

// elfJoinProblems makes one line of the problems found: the first two, and a
// count of the rest.
func elfJoinProblems(problems []string) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range problems {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > 2 {
		return strings.Join(out[:2], "; ") + fmt.Sprintf(" (+%d more)", len(out)-2)
	}
	return strings.Join(out, "; ")
}
