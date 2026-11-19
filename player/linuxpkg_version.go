package main

// linuxpkg_version.go — Debian's version order and dependency syntax, as the
// Linux packages manager (linuxpkg_*.go) reads them from Ubuntu's index.
//
// Both are outside data: the comparison decides which of three suites' copies
// of a package is newest and whether an installed one is out of date, and the
// dependency fields decide what gets downloaded. They follow dpkg's own rules
// (lib/dpkg/version.c and deb-control(5)), not semver: an epoch before a colon
// outranks everything, "~" sorts before anything (even the end of the string,
// so 1.0~rc1 < 1.0), letters sort before other symbols, and digit runs compare
// as numbers.

import (
	"strconv"
	"strings"
)

// linuxPkgVersion is a Debian version split into its three parts.
type linuxPkgVersion struct {
	epoch    int
	upstream string
	revision string
}

// linuxPkgParseVersion splits [epoch:]upstream[-revision]. An epoch that is not
// a number is treated as part of the upstream version, as dpkg would refuse it
// anyway and a refusal here would hide the package instead of sorting it.
func linuxPkgParseVersion(s string) linuxPkgVersion {
	s = strings.TrimSpace(s)
	var v linuxPkgVersion
	if i := strings.IndexByte(s, ':'); i > 0 {
		if n, err := strconv.Atoi(s[:i]); err == nil && n >= 0 {
			v.epoch = n
			s = s[i+1:]
		}
	}
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		v.upstream, v.revision = s[:i], s[i+1:]
	} else {
		v.upstream = s
	}
	return v
}

// linuxPkgVersionCompare orders two Debian versions: negative when a is older,
// zero when they are the same version, positive when a is newer.
func linuxPkgVersionCompare(a, b string) int {
	va, vb := linuxPkgParseVersion(a), linuxPkgParseVersion(b)
	if va.epoch != vb.epoch {
		if va.epoch < vb.epoch {
			return -1
		}
		return 1
	}
	if c := linuxPkgVerrevcmp(va.upstream, vb.upstream); c != 0 {
		return c
	}
	return linuxPkgVerrevcmp(va.revision, vb.revision)
}

// linuxPkgOrder is dpkg's weight of one character outside a digit run: "~"
// below the end of the string, the end of the string below letters, letters
// below every other symbol.
func linuxPkgOrder(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	c := s[i]
	switch {
	case c >= '0' && c <= '9':
		return 0
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return int(c)
	case c == '~':
		return -1
	}
	return int(c) + 256
}

func linuxPkgIsDigit(s string, i int) bool {
	return i < len(s) && s[i] >= '0' && s[i] <= '9'
}

// linuxPkgVerrevcmp is dpkg's verrevcmp: alternate non-digit and digit runs,
// the first by linuxPkgOrder, the second as numbers of any length.
func linuxPkgVerrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		firstDiff := 0
		for (i < len(a) && !linuxPkgIsDigit(a, i)) || (j < len(b) && !linuxPkgIsDigit(b, j)) {
			ac, bc := linuxPkgOrder(a, i), linuxPkgOrder(b, j)
			if ac != bc {
				if ac < bc {
					return -1
				}
				return 1
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for linuxPkgIsDigit(a, i) && linuxPkgIsDigit(b, j) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if linuxPkgIsDigit(a, i) {
			return 1
		}
		if linuxPkgIsDigit(b, j) {
			return -1
		}
		if firstDiff != 0 {
			if firstDiff < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// linuxPkgDep is one alternative of a dependency: a package name and, when
// the field gives one, a version relation ("<<", "<=", "=", ">=", ">>").
type linuxPkgDep struct {
	Name    string
	Op      string
	Version string
}

// String is the dependency as the index writes it.
func (d linuxPkgDep) String() string {
	if d.Op == "" {
		return d.Name
	}
	return d.Name + " (" + d.Op + " " + d.Version + ")"
}

// linuxPkgParseDeps reads a Depends/Pre-Depends field: groups separated by
// commas, alternatives inside a group by "|". Architecture qualifiers (":any")
// are dropped, an alternative restricted to other architectures ("[amd64]") is
// left out, and the obsolete "<" and ">" mean "<=" and ">=" as dpkg reads them.
// A group whose alternatives are all for other architectures disappears.
func linuxPkgParseDeps(field string) [][]linuxPkgDep {
	var groups [][]linuxPkgDep
	for _, group := range strings.Split(field, ",") {
		var alts []linuxPkgDep
		for _, alt := range strings.Split(group, "|") {
			if d, ok := linuxPkgParseDep(alt); ok {
				alts = append(alts, d)
			}
		}
		if len(alts) > 0 {
			groups = append(groups, alts)
		}
	}
	return groups
}

// linuxPkgParseDep reads one alternative; ok is false for an empty one, a
// malformed one, or one that does not apply to arm64.
func linuxPkgParseDep(s string) (linuxPkgDep, bool) {
	// Outside the version parentheses, "[arch list]" restricts the
	// alternative and "<profile>" (build profiles) only matters when building
	// from source: both are cut out here.
	var kept strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && (c == '[' || c == '<'):
			closer := byte(']')
			if c == '<' {
				closer = '>'
			}
			end := strings.IndexByte(s[i:], closer)
			if end < 0 {
				return linuxPkgDep{}, false
			}
			if c == '[' && !linuxPkgArchListMatches(s[i+1:i+end]) {
				return linuxPkgDep{}, false
			}
			i += end
			continue
		}
		kept.WriteByte(c)
	}
	s = strings.TrimSpace(kept.String())
	var d linuxPkgDep
	name := s
	if i := strings.IndexByte(s, '('); i >= 0 {
		end := strings.IndexByte(s, ')')
		if end < i {
			return linuxPkgDep{}, false
		}
		name = s[:i]
		rel := strings.TrimSpace(s[i+1 : end])
		k := 0
		for k < len(rel) && strings.IndexByte("<=>", rel[k]) >= 0 {
			k++
		}
		op, ver := rel[:k], strings.TrimSpace(rel[k:])
		switch op {
		case "<":
			op = "<="
		case ">":
			op = ">="
		case "<<", "<=", "=", ">=", ">>":
		default:
			return linuxPkgDep{}, false
		}
		if ver == "" {
			return linuxPkgDep{}, false
		}
		d.Op, d.Version = op, ver
	}
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	if !linuxPkgValidName(name) {
		return linuxPkgDep{}, false
	}
	d.Name = name
	return d, true
}

// linuxPkgArchListMatches reads "amd64 arm64" or "!i386 !amd64".
func linuxPkgArchListMatches(list string) bool {
	fields := strings.Fields(list)
	if len(fields) == 0 {
		return true
	}
	negated := strings.HasPrefix(fields[0], "!")
	for _, f := range fields {
		arch := strings.TrimPrefix(f, "!")
		if arch == "arm64" || arch == "any" || arch == "linux-any" || arch == "linux-arm64" {
			return !negated
		}
	}
	return negated
}

// linuxPkgValidName is Debian's package name rule: lower-case letters, digits
// and "+-." , starting with a letter or digit.
func linuxPkgValidName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '+' || c == '-' || c == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// linuxPkgSatisfies reports whether version have meets relation op want.
func linuxPkgSatisfies(have, op, want string) bool {
	if op == "" {
		return true
	}
	c := linuxPkgVersionCompare(have, want)
	switch op {
	case "<<":
		return c < 0
	case "<=":
		return c <= 0
	case "=":
		return c == 0
	case ">=":
		return c >= 0
	case ">>":
		return c > 0
	}
	return false
}

// linuxPkgProvidesSatisfies reports whether a Provides field offers dep: an
// unversioned provide satisfies only an unversioned dependency, a versioned
// one ("perlapi-5.38.2 (= 5.38.2)") is compared like a real package.
func linuxPkgProvidesSatisfies(provides string, dep linuxPkgDep) bool {
	if provides == "" {
		return false
	}
	for _, group := range linuxPkgParseDeps(provides) {
		for _, p := range group {
			if p.Name != dep.Name {
				continue
			}
			if dep.Op == "" {
				return true
			}
			if p.Op == "=" && linuxPkgSatisfies(p.Version, dep.Op, dep.Version) {
				return true
			}
		}
	}
	return false
}
