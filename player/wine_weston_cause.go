package main

// wine_weston_cause.go — why WestonPack's Weston stopped, in the loader's own
// words.
//
// A WestonPack module that the loader cannot load stops Weston before the
// program's script runs, and the start watch (wine_watch.go) ends the session.
// Until now the card said only that the display stopped while it was starting,
// whatever the loader had said about it, and two reports (JH-6G5TZF, JH-2S4CSH)
// were filed with the cause in their log already — the library was there, but
// too old:
//
//	Failed to load module: …/libpng16.so.16: undefined symbol: inflateValidate, version ZLIB_1.2.9
//
// So the card reads the session's log (westonLoadFailure) and names the
// function, the library and the version (wineOldLibraryError); the bug report's
// findings (launchFailureIssues) carry the same three. The library that does
// not have the function is the one the failing module's own version needs name
// for that version (libpng16.so.16 asks libz.so.1 for ZLIB_1.2.9), and a
// library that defines some versions of a name but not the one asked for is too
// old for the module, not missing from the firmware.

import (
	"bytes"
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// westonLoadLine is how Weston's loader reports a module it could not load.
const westonLoadLine = "Failed to load module: "

// wineOldLibraryError is a WestonPack module the loader could not load because
// a library this firmware has is older than the module was built against:
// module (the file the loader named), the library that should answer, the
// function it needs and the symbol version it was built against.
type wineOldLibraryError struct {
	module string // libpng16.so.16
	lib    string // libz.so.1, "" when the firmware's copy could not be named
	sym    string // inflateValidate, "" for a version the loader could not find
	ver    string // ZLIB_1.2.9
}

// Error is the card's reason: what stopped, and which function, library and
// version did it.
func (e *wineOldLibraryError) Error() string {
	var cause string
	switch {
	case e.lib != "" && e.sym != "":
		cause = "this firmware's " + e.lib + " is too old: " + e.module + " needs " + e.sym +
			" (" + e.ver + "), which its copy does not have"
	case e.lib != "":
		cause = "this firmware's " + e.lib + " is too old: " + e.module +
			" needs a " + e.ver + " symbol, which its copy does not have"
	case e.sym != "":
		cause = e.module + " needs " + e.sym + " (" + e.ver + "), which the library that should answer it on this firmware does not have"
	default:
		cause = e.module + " needs a " + e.ver + " symbol, which the library that should answer it on this firmware does not define"
	}
	return "WestonPack's display stopped while it was starting because " + cause
}

// issue is the same failure as the report's finding: its short title names the
// library, the function and the version (a report's title borrows it, so it
// has to fit the title's limit), its detail says what the module needs.
func (e *wineOldLibraryError) issue() launchIssue {
	is := launchIssue{Level: 0}
	switch {
	case e.lib != "" && e.sym != "":
		is.Title = e.lib + " is too old: no " + e.sym + " (" + e.ver + ")"
		is.Detail = "WestonPack's " + e.module + " was built against " + e.ver + " and imports " + e.sym +
			"; this firmware's " + e.lib + " does not export it, so Weston could not load its modules. " +
			"A firmware update with a newer " + e.lib + " fixes it."
	case e.lib != "":
		is.Title = e.lib + " is too old: no " + e.ver + " version"
		is.Detail = "WestonPack's " + e.module + " was built against " + e.ver +
			"; this firmware's " + e.lib + " does not define that version, so Weston could not load its modules. " +
			"A firmware update with a newer " + e.lib + " fixes it."
	case e.sym != "":
		is.Title = "WestonPack needs " + e.sym + " (" + e.ver + ")"
		is.Detail = "WestonPack's " + e.module + " was built against " + e.ver + " and imports " + e.sym +
			", which the firmware's library that should answer it does not export, so Weston could not load its modules."
	default:
		is.Title = "WestonPack needs a " + e.ver + " symbol"
		is.Detail = "WestonPack's " + e.module + " was built against " + e.ver +
			", which the firmware's library that should answer it does not define, so Weston could not load its modules."
	}
	return is
}

// westonLoadFailure reads the part of the session's log at p written since
// offset and turns the first module the loader could not load into the error
// the card shows. nil when the log holds no such line (Weston died for another
// reason: a missing file, a crash, no backend).
func westonLoadFailure(p string, offset int64) *wineOldLibraryError {
	for _, line := range wineLogLinesSince(p, offset) {
		if e := westonLoadFailureLine(line); e != nil {
			return e
		}
	}
	return nil
}

// wineLogLinesSince is the log at p from offset, at most the 1 MB a launch log
// is rotated at.
func wineLogLinesSince(p string, offset int64) []string {
	if p == "" {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	var out []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 0 {
			out = append(out, string(line))
		}
	}
	return out
}

// westonLoadFailureLine turns one line of Weston's output into the failure it
// reports, for the loader's two ways of saying a library is too old: an
// undefined symbol at a version the library does not have, and a version the
// loader cannot find in it.
func westonLoadFailureLine(line string) *wineOldLibraryError {
	i := strings.Index(line, westonLoadLine)
	if i < 0 {
		return nil
	}
	rest := strings.TrimSpace(line[i+len(westonLoadLine):])
	file, detail, ok := strings.Cut(rest, ": ")
	if !ok {
		return nil
	}
	file, detail = strings.TrimSpace(file), strings.TrimSpace(detail)
	switch {
	case strings.HasPrefix(detail, "undefined symbol: "):
		sym, ver, ok := strings.Cut(strings.TrimPrefix(detail, "undefined symbol: "), ", version ")
		sym, ver = strings.TrimSpace(sym), strings.TrimSpace(ver)
		if !ok || sym == "" || ver == "" {
			return nil
		}
		return westonOldLibrary(file, sym, ver, elfSystemLibDirs)
	case strings.HasPrefix(detail, "version `"):
		ver, tail, ok := strings.Cut(strings.TrimPrefix(detail, "version `"), "'")
		ver = strings.TrimSpace(ver)
		if !ok || ver == "" {
			return nil
		}
		// "…: version `ZLIB_1.2.9' not found (required by …/libpng16.so.16)":
		// the file before it is where the version should be, the one after
		// it is what asked.
		who := file
		if j := strings.Index(tail, "required by "); j >= 0 {
			who = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(tail[j+len("required by "):]), ")"))
		}
		return westonOldLibrary(who, "", ver, elfSystemLibDirs)
	}
	return nil
}

// westonOldLibrary names the library that should have answered: the failing
// module's own version needs say which library carries that version, and the
// library's version definitions say whether it has it. file is the module the
// loader named (an absolute path, or a bare name the firmware's folders are
// searched for); dirs are the firmware's library folders, after the module's
// own.
func westonOldLibrary(file, sym, ver string, dirs []string) *wineOldLibraryError {
	if ver == "" {
		return nil
	}
	e := &wineOldLibraryError{module: filepath.Base(file), sym: sym, ver: ver}
	path := file
	if !strings.Contains(path, "/") {
		// A bare name the loader named: find the file where the session would.
		if info, state := elfFindLibrary(file, elf.ELFCLASS64, elf.EM_AARCH64, dirs); state == elfLibFound {
			path = info.path
		}
	}
	f, err := elf.Open(path)
	if err != nil {
		return e
	}
	needs := elfVersionNeeds(f)
	class, machine := f.Class, f.Machine
	needed, _ := f.ImportedLibraries()
	_ = f.Close()
	// The folders the session's loader reads, the module's own first.
	libDirs := append([]string{filepath.Dir(path)}, dirs...)
	// The version's own library: what the module's version needs name for it.
	for _, lib := range westonSortedNames(needs) {
		if !hasVersion(needs[lib], ver) {
			continue
		}
		info, state := elfFindLibrary(lib, class, machine, libDirs)
		if state != elfLibFound {
			continue
		}
		if info.defs[ver] {
			continue // a newer copy elsewhere provides it; not this failure
		}
		e.lib = filepath.Base(lib)
		return e
	}
	// No version need names it: a library that defines some versions of the
	// same name but not this one is too old for the module.
	prefix := ver
	if i := strings.LastIndex(prefix, "_"); i > 0 {
		prefix = prefix[:i+1]
	}
	for _, lib := range needed {
		info, state := elfFindLibrary(lib, class, machine, libDirs)
		if state != elfLibFound || info.defs == nil || info.defs[ver] {
			continue
		}
		for defined := range info.defs {
			if strings.HasPrefix(defined, prefix) {
				e.lib = filepath.Base(lib)
				return e
			}
		}
	}
	return e
}

// hasVersion reports whether versions holds v.
func hasVersion(versions []string, v string) bool {
	for _, has := range versions {
		if has == v {
			return true
		}
	}
	return false
}
