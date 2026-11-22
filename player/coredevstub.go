package main

// coredevstub.go — stand-ins for the device-access libraries a desktop build
// of a core links, written by JukaHub itself.
//
// Juka, 2026-11-22: "On Trimui devices (and Trimui Brick Pro) libusb-1.0.so
// ...make sure to fix it and make sure we can run Dolphin on Trimui Smart
// Pro". The libretro buildbot's Dolphin is a desktop Linux build: besides a
// newer glibc (ra_glibc.go) it links libusb and libudev — and some builds
// BlueZ's libbluetooth — for the GameCube adapter, USB passthrough and real
// Wii Remotes. No TrimUI firmware carries them, and RetroArch's dlopen of the
// core stops at the first one missing ("libusb-1.0.so.0: cannot open shared
// object file"). That morning's answer fetched Ubuntu's copies through Linux
// packages, behind tens of megabytes of package index, and only when JukaHub's
// own updater installed the core.
//
// A handheld has none of those devices for Dolphin to drive, so the libraries
// only need to load and say so. This file writes such a library: a 64-bit ARM
// shared object that exports exactly the functions (and symbol versions) the
// core imports from it, each a two-instruction function that answers "not
// supported" in that library's own terms:
//   - libusb: LIBUSB_ERROR_NOT_SUPPORTED from libusb_init and nearly every
//     other call, so Dolphin's USB code stays off — as in JukaHub's own TrimUI
//     build, whose bundled libusb finds no udev — with that error's name and
//     text from libusb_error_name and libusb_strerror (Dolphin logs them), 0
//     from libusb_has_capability and NULL from the calls that return a device
//     or a transfer;
//   - libudev: NULL or 0 from every call, udev_new first: what the cores
//     workflow's stub links into JukaHub's own build (build-cores.sh
//     udev_stub), and what hidapi and SDL meet on a firmware with no libudev,
//     so they look for devices the way they do there;
//   - libbluetooth: -1 from every call (hci_get_route: no adapter), and Dolphin
//     reports "Bluetooth not found".
// libevdev gets none: a build with Dolphin's evdev input backend needs a udev
// that works (its hotplug thread would wait on a monitor a NULL udev never
// gives), so such a build stays refused, with the library named.
//
// The library is a few kilobytes: the ELF header; two loadable segments, one
// read-only (the headers, the symbol tables and the code) and one writable
// (the dynamic section, which glibc's loader relocates in place, and the
// libusb_version libusb_get_version fills); a SysV hash table; the symbol
// versions the core's .gnu.version_r asks of the library, as .gnu.version_d
// and .gnu.version; and section headers, which tools and the load check read
// (elfcompat.go takes the version definitions from them). The code reaches its
// strings PC-relatively, so there are no relocations. The same layout, built
// for x86-64, was loaded by glibc 2.35's own loader before this was written:
// versioned and unversioned imports bound, with LD_BIND_NOW too.

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// devStubRetKind is how a stand-in function returns.
type devStubRetKind uint8

const (
	// devStubConst returns value: a number, or NULL as 0.
	devStubConst devStubRetKind = iota
	// devStubString returns a pointer to text.
	devStubString
	// devStubVersion returns a pointer to a libusb_version
	// (libusb_get_version).
	devStubVersion
)

// devStubRet is what one stand-in function returns.
type devStubRet struct {
	kind  devStubRetKind
	value int64
	text  string
}

// devStubSym is one function a stand-in exports; version is "" for an
// unversioned symbol.
type devStubSym struct {
	name    string
	version string
}

// devStubLib is what JukaHub knows of a library it can stand in for.
type devStubLib struct {
	// prefixes claim a core's unversioned imports for the library; a
	// versioned import names its library itself.
	prefixes []string
	// ret is what each of its functions returns.
	ret func(name string) devStubRet
}

// devStubLibs are the libraries JukaHub writes stand-ins for, by file name.
var devStubLibs = map[string]devStubLib{
	"libusb-1.0.so.0": {prefixes: []string{"libusb_"}, ret: devStubLibusbRet},
	"libudev.so.1":    {prefixes: []string{"udev_"}, ret: func(string) devStubRet { return devStubRet{} }},
	"libbluetooth.so.3": {
		prefixes: []string{"hci_", "sdp_", "bt_", "ba2", "str2ba", "bachk", "baswap", "batostr", "strtoba", "lmp_", "pal_"},
		ret:      func(string) devStubRet { return devStubRet{value: -1} },
	},
}

// devStubLibusbNotSupported is libusb's LIBUSB_ERROR_NOT_SUPPORTED.
const devStubLibusbNotSupported = -12

// devStubVersionInfo is the libusb_version a stand-in reports (major, minor,
// micro, nano), with devStubVersionText as its description.
var devStubVersionInfo = [4]uint16{1, 0, 27, 0}

const devStubVersionText = "JukaHub stand-in: no USB devices on this handheld"

// devStubLibusbRet is what libusb's stand-in returns (see the file comment).
func devStubLibusbRet(name string) devStubRet {
	switch name {
	case "libusb_error_name":
		return devStubRet{kind: devStubString, text: "LIBUSB_ERROR_NOT_SUPPORTED"}
	case "libusb_strerror":
		return devStubRet{kind: devStubString, text: "Operation not supported or unimplemented on this platform"}
	case "libusb_get_version":
		return devStubRet{kind: devStubVersion}
	case "libusb_try_lock_events", "libusb_wait_for_event":
		// "Not obtained" and "timed out".
		return devStubRet{value: 1}
	case "libusb_has_capability", "libusb_pollfds_handle_timeouts", "libusb_event_handling_ok",
		"libusb_event_handler_active", "libusb_get_next_timeout", "libusb_get_bus_number",
		"libusb_get_port_number", "libusb_get_device_address", "libusb_get_device_speed",
		// The calls that return a pointer: NULL.
		"libusb_get_device", "libusb_ref_device", "libusb_get_parent", "libusb_open_device_with_vid_pid",
		"libusb_alloc_transfer", "libusb_dev_mem_alloc", "libusb_get_pollfds", "libusb_hotplug_get_user_data":
		return devStubRet{}
	}
	return devStubRet{value: devStubLibusbNotSupported}
}

// devStubNeeds is what the core f asks of lib: the symbol versions its
// .gnu.version_r lists for lib (each must be defined, or the loader refuses
// the core) and the functions it imports from it — a versioned import by the
// library it names, an unversioned one by the library's prefixes.
func devStubNeeds(f *elf.File, lib string) (versions []string, syms []devStubSym) {
	info := devStubLibs[lib]
	versions = slices.Clone(elfVersionNeeds(f)[lib])
	imported, err := f.ImportedSymbols()
	if err != nil {
		return versions, nil
	}
	for _, s := range imported {
		switch {
		case s.Library == lib:
			syms = append(syms, devStubSym{name: s.Name, version: s.Version})
		case s.Library == "" && s.Version == "" && devStubClaims(s.Name, info.prefixes):
			syms = append(syms, devStubSym{name: s.Name})
		}
	}
	return versions, syms
}

// devStubClaims reports whether name starts with one of prefixes.
func devStubClaims(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ── The library file ─────────────────────────────────────────────────────

const (
	// devStubAlign aligns both loadable segments for any page size up to
	// 64 KiB.
	devStubAlign = 0x10000
	// devStubRWBias puts the writable segment that far above its file offset,
	// clear of every page the read-only one maps.
	devStubRWBias = 0x10000
	// devStubMaxSyms bounds one stand-in.
	devStubMaxSyms = 8192
	// devStubDynCount is the number of dynamic entries, DT_NULL included.
	devStubDynCount = 10
)

// The section headers, in file order after the null one.
const (
	devStubShHash = iota + 1
	devStubShDynsym
	devStubShDynstr
	devStubShVersym
	devStubShVerdef
	devStubShText
	devStubShRodata
	devStubShDynamic
	devStubShData
	devStubShShstrtab
	devStubShCount // with the null section
)

// devStubSectionNames are their names, by index.
var devStubSectionNames = [devStubShCount]string{"", ".hash", ".dynsym", ".dynstr", ".gnu.version", ".gnu.version_d",
	".text", ".rodata", ".dynamic", ".data", ".shstrtab"}

// devStubElfHash is the SysV ELF hash of name (DT_HASH, vd_hash).
func devStubElfHash(name string) uint32 {
	var h uint32
	for i := 0; i < len(name); i++ {
		h = h<<4 + uint32(name[i])
		if g := h & 0xf0000000; g != 0 {
			h ^= g >> 24
		}
		h &^= 0xf0000000
	}
	return h
}

// The few AArch64 instructions a stand-in uses.

const a64Ret uint32 = 0xd65f03c0 // ret

// a64MovX0 is movz/movn x0, #v for v in [-65536, 65535].
func a64MovX0(v int64) (uint32, bool) {
	switch {
	case v >= 0 && v <= 0xffff:
		return 0xd2800000 | uint32(v)<<5, true
	case v < 0 && v >= -0x10000:
		return 0x92800000 | uint32(-v-1)<<5, true
	}
	return 0, false
}

// a64Adr is adr x<rd>, target for the instruction at pc.
func a64Adr(rd uint32, pc, target uint64) (uint32, bool) {
	off := int64(target) - int64(pc)
	if off < -(1<<20) || off >= 1<<20 {
		return 0, false
	}
	u := uint32(off)
	return 0x10000000 | (u&3)<<29 | (u>>2&0x7ffff)<<5 | rd, true
}

// a64StrX is str x<rt>, [x<rn>, #imm] (imm a multiple of 8).
func a64StrX(rt, rn uint32, imm uint64) uint32 {
	return 0xf9000000 | uint32(imm/8)<<10 | rn<<5 | rt
}

// devStubBuild writes the stand-in for soname: syms exported (each returning
// what ret says), versions defined beside the base one (with any version a
// symbol names), for 64-bit ARM. See the file comment for the layout.
func devStubBuild(soname string, versions []string, syms []devStubSym, ret func(string) devStubRet) ([]byte, error) {
	if soname == "" || strings.ContainsAny(soname, "/\x00") {
		return nil, errors.New("no library name")
	}
	syms = slices.Clone(syms)
	sort.Slice(syms, func(i, j int) bool {
		if syms[i].name != syms[j].name {
			return syms[i].name < syms[j].name
		}
		return syms[i].version < syms[j].version
	})
	syms = slices.Compact(syms)
	if len(syms) > devStubMaxSyms {
		return nil, fmt.Errorf("%d functions, more than %d", len(syms), devStubMaxSyms)
	}
	var vers []string
	addVersion := func(v string) {
		if v != "" && v != soname && !slices.Contains(vers, v) {
			vers = append(vers, v)
		}
	}
	for _, v := range versions {
		addVersion(v)
	}
	for _, s := range syms {
		if s.name == "" || strings.ContainsRune(s.name, 0) || strings.ContainsRune(s.version, 0) {
			return nil, fmt.Errorf("bad function name %q", s.name)
		}
		addVersion(s.version)
	}
	for _, v := range vers {
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("bad version name %q", v)
		}
	}
	le := binary.LittleEndian

	// Strings: the library's name, the versions, the functions.
	dynstr := []byte{0}
	strAt := map[string]uint32{}
	str := func(s string) uint32 {
		if at, ok := strAt[s]; ok {
			return at
		}
		at := uint32(len(dynstr))
		dynstr = append(append(dynstr, s...), 0)
		strAt[s] = at
		return at
	}
	str(soname)
	for _, v := range vers {
		str(v)
	}
	for _, s := range syms {
		str(s.name)
	}

	// Version definitions: the base (the library itself, index 1), then one
	// per version (index 2 on); each Verdef (20 bytes) with one Verdaux (8).
	defs := append([]string{soname}, vers...)
	verdef := make([]byte, 28*len(defs))
	for i, name := range defs {
		e := verdef[28*i:]
		le.PutUint16(e[0:], 1) // vd_version
		if i == 0 {
			le.PutUint16(e[2:], 1) // VER_FLG_BASE
		}
		le.PutUint16(e[4:], uint16(i+1)) // vd_ndx
		le.PutUint16(e[6:], 1)           // vd_cnt
		le.PutUint32(e[8:], devStubElfHash(name))
		le.PutUint32(e[12:], 20) // vd_aux
		if i < len(defs)-1 {
			le.PutUint32(e[16:], 28) // vd_next
		}
		le.PutUint32(e[20:], str(name)) // vda_name
	}
	nsym := len(syms) + 1
	versym := make([]byte, 2*nsym)
	for i, s := range syms {
		ndx := uint16(1) // global, unversioned
		if s.version != "" {
			ndx = uint16(slices.Index(vers, s.version) + 2)
		}
		le.PutUint16(versym[2*(i+1):], ndx)
	}

	// The SysV hash table.
	nbucket := len(syms)/2 + 1
	hash := make([]byte, 4*(2+nbucket+nsym))
	le.PutUint32(hash[0:], uint32(nbucket))
	le.PutUint32(hash[4:], uint32(nsym))
	buckets, chains := hash[8:], hash[8+4*nbucket:]
	for i, s := range syms {
		idx := uint32(i + 1)
		b := devStubElfHash(s.name) % uint32(nbucket)
		le.PutUint32(chains[4*idx:], le.Uint32(buckets[4*b:]))
		le.PutUint32(buckets[4*b:], idx)
	}

	// One block of code per distinct return; the strings they return.
	type block struct {
		ret        devStubRet
		addr, size uint64
	}
	var blocks []block
	symBlock := make([]int, len(syms))
	var texts []string
	addText := func(s string) {
		if !slices.Contains(texts, s) {
			texts = append(texts, s)
		}
	}
	hasVersion := false
	for i, s := range syms {
		r := devStubRet{}
		if ret != nil {
			r = ret(s.name)
		}
		if _, ok := a64MovX0(r.value); r.kind == devStubConst && !ok {
			return nil, fmt.Errorf("%s returns %d, which a stand-in cannot", s.name, r.value)
		}
		at := slices.IndexFunc(blocks, func(b block) bool { return b.ret == r })
		if at < 0 {
			size := uint64(8)
			switch r.kind {
			case devStubString:
				addText(r.text)
			case devStubVersion:
				size = 24
				hasVersion = true
				addText("")
				addText(devStubVersionText)
			}
			blocks = append(blocks, block{ret: r, size: size})
			at = len(blocks) - 1
		}
		symBlock[i] = at
	}
	var rodata []byte
	textAt := map[string]uint64{}
	for _, s := range texts {
		textAt[s] = uint64(len(rodata))
		rodata = append(append(rodata, s...), 0)
	}
	shstr := []byte{0}
	shName := make([]uint32, devStubShCount)
	for i, name := range devStubSectionNames {
		if i > 0 {
			shName[i] = uint32(len(shstr))
			shstr = append(append(shstr, name...), 0)
		}
	}

	// File offsets; the read-only segment's addresses are its offsets, the
	// writable one's its offsets plus devStubRWBias.
	off := uint64(64 + 4*56)
	place := func(size, align uint64) uint64 {
		off = (off + align - 1) &^ (align - 1)
		at := off
		off += size
		return at
	}
	oHash := place(uint64(len(hash)), 8)
	oDynsym := place(uint64(nsym)*24, 8)
	oDynstr := place(uint64(len(dynstr)), 1)
	oVersym := place(uint64(len(versym)), 2)
	oVerdef := place(uint64(len(verdef)), 8)
	var textSize uint64
	for _, b := range blocks {
		textSize += b.size
	}
	oText := place(textSize, 16)
	oRodata := place(uint64(len(rodata)), 8)
	rxEnd := off
	oDynamic := place(devStubDynCount*16, 16)
	var dataSize uint64
	if hasVersion {
		dataSize = 24 // struct libusb_version: four uint16, two pointers
	}
	oData := place(dataSize, 8)
	rwEnd := off
	oShstr := place(uint64(len(shstr)), 1)
	oShdr := place(devStubShCount*64, 8)
	out := make([]byte, off)

	// The code.
	at := oText
	for i := range blocks {
		b := &blocks[i]
		b.addr = at
		var words []uint32
		switch b.ret.kind {
		case devStubConst:
			mov, _ := a64MovX0(b.ret.value)
			words = []uint32{mov, a64Ret}
		case devStubString:
			adr, ok := a64Adr(0, at, oRodata+textAt[b.ret.text])
			if !ok {
				return nil, errors.New("a string is out of reach")
			}
			words = []uint32{adr, a64Ret}
		case devStubVersion:
			// The pointers go in at the call: the file has no relocations.
			data, ok1 := a64Adr(0, at, oData+devStubRWBias)
			rc, ok2 := a64Adr(1, at+4, oRodata+textAt[""])
			desc, ok3 := a64Adr(1, at+12, oRodata+textAt[devStubVersionText])
			if !ok1 || !ok2 || !ok3 {
				return nil, errors.New("the version is out of reach")
			}
			words = []uint32{data, rc, a64StrX(1, 0, 8), desc, a64StrX(1, 0, 16), a64Ret}
		}
		for j, w := range words {
			le.PutUint32(out[at+uint64(4*j):], w)
		}
		at += b.size
	}

	// The symbol table: entry 0 is the null symbol.
	for i, s := range syms {
		e := out[oDynsym+uint64(24*(i+1)):]
		b := blocks[symBlock[i]]
		le.PutUint32(e[0:], str(s.name))
		e[4] = byte(elf.STB_GLOBAL)<<4 | byte(elf.STT_FUNC)
		le.PutUint16(e[6:], devStubShText)
		le.PutUint64(e[8:], b.addr)
		le.PutUint64(e[16:], b.size)
	}
	copy(out[oHash:], hash)
	copy(out[oDynstr:], dynstr)
	copy(out[oVersym:], versym)
	copy(out[oVerdef:], verdef)
	copy(out[oRodata:], rodata)
	if hasVersion {
		for i, v := range devStubVersionInfo {
			le.PutUint16(out[oData+uint64(2*i):], v)
		}
	}
	copy(out[oShstr:], shstr)

	// The dynamic section.
	dyn := [devStubDynCount][2]uint64{
		{uint64(elf.DT_SONAME), uint64(str(soname))},
		{uint64(elf.DT_HASH), oHash},
		{uint64(elf.DT_STRTAB), oDynstr},
		{uint64(elf.DT_SYMTAB), oDynsym},
		{uint64(elf.DT_STRSZ), uint64(len(dynstr))},
		{uint64(elf.DT_SYMENT), 24},
		{uint64(elf.DT_VERSYM), oVersym},
		{uint64(elf.DT_VERDEF), oVerdef},
		{uint64(elf.DT_VERDEFNUM), uint64(len(defs))},
		{uint64(elf.DT_NULL), 0},
	}
	for i, d := range dyn {
		le.PutUint64(out[oDynamic+uint64(16*i):], d[0])
		le.PutUint64(out[oDynamic+uint64(16*i)+8:], d[1])
	}

	// The ELF header.
	copy(out, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT), byte(elf.ELFOSABI_NONE)})
	le.PutUint16(out[16:], uint16(elf.ET_DYN))
	le.PutUint16(out[18:], uint16(elf.EM_AARCH64))
	le.PutUint32(out[20:], uint32(elf.EV_CURRENT))
	le.PutUint64(out[32:], 64) // e_phoff
	le.PutUint64(out[40:], oShdr)
	le.PutUint16(out[52:], 64) // e_ehsize
	le.PutUint16(out[54:], 56) // e_phentsize
	le.PutUint16(out[56:], 4)  // e_phnum
	le.PutUint16(out[58:], 64) // e_shentsize
	le.PutUint16(out[60:], devStubShCount)
	le.PutUint16(out[62:], devStubShShstrtab)

	// The program headers: the read-only segment from the file's start, the
	// writable one, the dynamic section, and a stack that is not executable.
	ph := func(i int, typ elf.ProgType, flags elf.ProgFlag, offset, vaddr, size, align uint64) {
		p := out[64+56*i:]
		le.PutUint32(p[0:], uint32(typ))
		le.PutUint32(p[4:], uint32(flags))
		le.PutUint64(p[8:], offset)
		le.PutUint64(p[16:], vaddr)
		le.PutUint64(p[24:], vaddr)
		le.PutUint64(p[32:], size)
		le.PutUint64(p[40:], size)
		le.PutUint64(p[48:], align)
	}
	ph(0, elf.PT_LOAD, elf.PF_R|elf.PF_X, 0, 0, rxEnd, devStubAlign)
	ph(1, elf.PT_LOAD, elf.PF_R|elf.PF_W, oDynamic, oDynamic+devStubRWBias, rwEnd-oDynamic, devStubAlign)
	ph(2, elf.PT_DYNAMIC, elf.PF_R|elf.PF_W, oDynamic, oDynamic+devStubRWBias, devStubDynCount*16, 8)
	ph(3, elf.PT_GNU_STACK, elf.PF_R|elf.PF_W, 0, 0, 0, 16)

	// The section headers.
	sh := func(i int, typ elf.SectionType, flags elf.SectionFlag, addr, offset, size uint64, link, info uint32, align, entsize uint64) {
		s := out[oShdr+uint64(64*i):]
		le.PutUint32(s[0:], shName[i])
		le.PutUint32(s[4:], uint32(typ))
		le.PutUint64(s[8:], uint64(flags))
		le.PutUint64(s[16:], addr)
		le.PutUint64(s[24:], offset)
		le.PutUint64(s[32:], size)
		le.PutUint32(s[40:], link)
		le.PutUint32(s[44:], info)
		le.PutUint64(s[48:], align)
		le.PutUint64(s[56:], entsize)
	}
	alloc := elf.SHF_ALLOC
	sh(devStubShHash, elf.SHT_HASH, alloc, oHash, oHash, uint64(len(hash)), devStubShDynsym, 0, 8, 4)
	sh(devStubShDynsym, elf.SHT_DYNSYM, alloc, oDynsym, oDynsym, uint64(nsym)*24, devStubShDynstr, 1, 8, 24)
	sh(devStubShDynstr, elf.SHT_STRTAB, alloc, oDynstr, oDynstr, uint64(len(dynstr)), 0, 0, 1, 0)
	sh(devStubShVersym, elf.SHT_GNU_VERSYM, alloc, oVersym, oVersym, uint64(len(versym)), devStubShDynsym, 0, 2, 2)
	sh(devStubShVerdef, elf.SHT_GNU_VERDEF, alloc, oVerdef, oVerdef, uint64(len(verdef)), devStubShDynstr, uint32(len(defs)), 8, 0)
	sh(devStubShText, elf.SHT_PROGBITS, alloc|elf.SHF_EXECINSTR, oText, oText, textSize, 0, 0, 16, 0)
	sh(devStubShRodata, elf.SHT_PROGBITS, alloc, oRodata, oRodata, uint64(len(rodata)), 0, 0, 8, 0)
	sh(devStubShDynamic, elf.SHT_DYNAMIC, alloc|elf.SHF_WRITE, oDynamic+devStubRWBias, oDynamic, devStubDynCount*16, devStubShDynstr, 0, 8, 16)
	sh(devStubShData, elf.SHT_PROGBITS, alloc|elf.SHF_WRITE, oData+devStubRWBias, oData, dataSize, 0, 0, 8, 0)
	sh(devStubShShstrtab, elf.SHT_STRTAB, 0, 0, oShstr, uint64(len(shstr)), 0, 0, 1, 0)
	return out, nil
}
