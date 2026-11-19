package main

// linuxpkg_install.go — what Linux packages does with a verified .deb: decide
// what has to come along (Depends, Pre-Depends, alternatives, Provides,
// version relations), unpack it into <data>/linux/root on the card, and keep a
// per-package file list so remove and upgrade delete exactly what a package
// owned. There is no dpkg: maintainer scripts never run, and the few things
// they would have done that matter here (the CA bundle, links) are done in Go.
//
// The card is usually exFAT: no symbolic links and no Unix modes beyond what
// the mount gives. Ubuntu's merged /usr is applied while unpacking (bin/, sbin/
// and lib/ land in usr/), so the big directory links never need to exist. A
// file link is made as a real symlink where the card can hold one and
// otherwise as a copy of its target once every package of the run is
// unpacked; a link to a folder is recorded and left out. Every path is checked
// to stay inside the root, and writes go through os.Root.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ── Status ───────────────────────────────────────────────────────────────

// linuxPkgInstalled is one installed package (status.json).
type linuxPkgInstalled struct {
	Version string `json:"version"`
	Arch    string `json:"arch,omitempty"`
	// Auto marks a package that came in as a dependency; autoremove may take it.
	Auto bool `json:"auto,omitempty"`
	// State is "installed", or "unpacking" while (or if a run died while) its
	// files were written: such a package is unpacked again by the next run.
	State      string `json:"state"`
	Section    string `json:"section,omitempty"`
	Desc       string `json:"desc,omitempty"`
	Depends    string `json:"depends,omitempty"`
	PreDepends string `json:"pre_depends,omitempty"`
	Provides   string `json:"provides,omitempty"`
	// Bytes is what its files take on the card.
	Bytes int64 `json:"bytes"`
}

type linuxPkgStatus struct {
	Packages map[string]linuxPkgInstalled `json:"packages"`
}

func linuxPkgLoadStatus(lay linuxPkgLayout) linuxPkgStatus {
	st := linuxPkgStatus{}
	if data, err := os.ReadFile(filepath.Join(lay.base, "status.json")); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Packages == nil {
		st.Packages = map[string]linuxPkgInstalled{}
	}
	return st
}

func linuxPkgSaveStatus(lay linuxPkgLayout, st linuxPkgStatus) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return linuxPkgWriteFile(filepath.Join(lay.base, "status.json"), data)
}

// ── Locking ──────────────────────────────────────────────────────────────

var errLinuxPkgUnsupported = errors.New("Linux packages is the TrimUI handheld's package manager (Ubuntu's arm64 programs on the SD card); this system has its own")

// linuxPkgOp is the one change running in this process (Terminal, the page
// and the Audit toolkit share it); a lock file covers `JukaHub apt` started
// from an SSH shell at the same time.
var linuxPkgOp struct {
	mu    sync.Mutex
	busy  bool
	label string
}

// linuxPkgBusyLabel names the running operation, or "".
func linuxPkgBusyLabel() string {
	linuxPkgOp.mu.Lock()
	defer linuxPkgOp.mu.Unlock()
	if !linuxPkgOp.busy {
		return ""
	}
	return linuxPkgOp.label
}

// linuxPkgBegin takes the package lock for one operation.
func linuxPkgBegin(label string) (func(), error) {
	linuxPkgOp.mu.Lock()
	if linuxPkgOp.busy {
		running := linuxPkgOp.label
		linuxPkgOp.mu.Unlock()
		return nil, fmt.Errorf("Linux packages are busy (%s); try again when that is done", running)
	}
	linuxPkgOp.busy, linuxPkgOp.label = true, label
	linuxPkgOp.mu.Unlock()
	unlock := func() {
		linuxPkgOp.mu.Lock()
		linuxPkgOp.busy, linuxPkgOp.label = false, ""
		linuxPkgOp.mu.Unlock()
	}
	lay, err := linuxPkgPrepare()
	if err != nil {
		unlock()
		return nil, err
	}
	lock := filepath.Join(lay.base, "lock")
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			f.Close()
			break
		}
		data, _ := os.ReadFile(lock)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if attempt > 0 || (pid > 0 && pid != os.Getpid() && linuxPkgProcessAlive(pid)) {
			unlock()
			return nil, fmt.Errorf("another JukaHub (process %d) is changing Linux packages; try again when it is done", pid)
		}
		// A lock left by a run that died (or by this process before a crash).
		_ = os.Remove(lock)
	}
	return func() {
		_ = os.Remove(lock)
		unlock()
	}, nil
}

// linuxPkgProcessAlive reports whether pid is a running JukaHub. Only a live
// process of this same program counts: after a power cut mid-install the lock
// names a pid that, on a handheld booting the same way every time, a firmware
// daemon may well have now, and that must not lock packages out for good.
func linuxPkgProcessAlive(pid int) bool {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return true
	}
	return filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == filepath.Base(self)
}

// linuxPkgPrepare makes the folders and the apt wrapper.
func linuxPkgPrepare() (linuxPkgLayout, error) {
	if !linuxPkgSupported() {
		return linuxPkgLayout{}, errLinuxPkgUnsupported
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return lay, err
	}
	for _, d := range []string{lay.base, lay.root, lay.bin, lay.lists, lay.index, lay.cache, lay.info} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return lay, err
		}
	}
	linuxPkgWriteAptWrapper(lay)
	return lay, nil
}

// ── Resolving ────────────────────────────────────────────────────────────

// linuxPkgSystem are dependencies that are satisfied without installing
// anything: JukaHub is the package manager here and runs no maintainer
// scripts (dpkg, debconf, ucf, install-info), nothing is started at boot or
// adds users (init and systemd, adduser, passwd), the firmware's /bin/sh and
// busybox stand in for the shell tools, and the loader runs with
// --inhibit-cache (libc-bin's ldconfig). Packages marked Essential in the
// index count as part of this set as well, except linuxPkgEssentialKept.
var linuxPkgSystem = map[string]bool{
	"dpkg": true, "debconf": true, "debconf-2.0": true, "cdebconf": true, "ucf": true, "install-info": true,
	"init-system-helpers": true, "init": true, "systemd": true, "systemd-sysv": true, "sysvinit-utils": true,
	"lsb-base": true, "adduser": true, "passwd": true, "login": true, "base-files": true, "base-passwd": true,
	"usr-is-merged": true, "usrmerge": true, "libc-bin": true,
}

// linuxPkgEssentialKept are Essential packages installed all the same: perl-base
// holds /usr/bin/perl itself, and ncurses-base the terminal descriptions
// full-screen programs (htop, nano) need over SSH.
var linuxPkgEssentialKept = map[string]bool{"perl-base": true, "ncurses-base": true}

// linuxPkgCompanions are installed beside a package because, on Ubuntu, a
// Recommends or another package's maintainer script would have made it work:
// terminal descriptions for curses programs and the CA certificates for TLS.
var linuxPkgCompanions = map[string][]string{
	"libtinfo6":      {"ncurses-base"},
	"libncursesw6":   {"ncurses-base"},
	"libssl3t64":     {"ca-certificates"},
	"libgnutls30t64": {"ca-certificates"},
}

// linuxPkgReserved are names the wrappers keep for JukaHub itself.
var linuxPkgReserved = map[string]bool{"apt": true, "apt-get": true, "jpkg": true}

// linuxPkgPlan is what one command will change.
type linuxPkgPlan struct {
	install  []linuxPkgEntry   // dependencies first
	upgrades map[string]string // name → the installed version it replaces
	manual   map[string]bool   // asked for by name
	already  []string          // asked for, already the newest version
	remove   []string
	download int64
	addBytes int64
}

func newLinuxPkgPlan() *linuxPkgPlan {
	return &linuxPkgPlan{upgrades: map[string]string{}, manual: map[string]bool{}}
}

type linuxPkgResolver struct {
	idx     *linuxPkgIndex
	st      linuxPkgStatus
	plan    *linuxPkgPlan
	planned map[string]linuxPkgEntry
	cache   map[string]*linuxPkgEntry
	notes   []string
}

func newLinuxPkgResolver(idx *linuxPkgIndex, st linuxPkgStatus) *linuxPkgResolver {
	return &linuxPkgResolver{idx: idx, st: st, plan: newLinuxPkgPlan(),
		planned: map[string]linuxPkgEntry{}, cache: map[string]*linuxPkgEntry{}}
}

func (r *linuxPkgResolver) get(name string) (linuxPkgEntry, bool) {
	if e, ok := r.cache[name]; ok {
		if e == nil {
			return linuxPkgEntry{}, false
		}
		return *e, true
	}
	e, ok := r.idx.Get(name)
	if !ok {
		r.cache[name] = nil
		return e, false
	}
	r.cache[name] = &e
	return e, true
}

func (r *linuxPkgResolver) systemProvided(name string) bool {
	if linuxPkgSystem[name] {
		return true
	}
	if linuxPkgEssentialKept[name] {
		return false
	}
	e, ok := r.get(name)
	return ok && e.Essential
}

// satisfied reports whether d already holds: the system, a planned package, an
// installed one (unless the plan replaces it), or something providing it.
func (r *linuxPkgResolver) satisfied(d linuxPkgDep) bool {
	if r.systemProvided(d.Name) {
		return true
	}
	if e, ok := r.planned[d.Name]; ok {
		if linuxPkgSatisfies(e.Version, d.Op, d.Version) {
			return true
		}
	} else if in, ok := r.st.Packages[d.Name]; ok && in.State == "installed" && linuxPkgSatisfies(in.Version, d.Op, d.Version) {
		return true
	}
	for _, e := range r.planned {
		if linuxPkgProvidesSatisfies(e.Provides, d) {
			return true
		}
	}
	for name, in := range r.st.Packages {
		if _, replaced := r.planned[name]; !replaced && in.State == "installed" && linuxPkgProvidesSatisfies(in.Provides, d) {
			return true
		}
	}
	for _, p := range r.idx.Providers(d.Name) {
		if r.systemProvided(p) {
			if e, ok := r.get(p); ok && linuxPkgProvidesSatisfies(e.Provides, d) {
				return true
			}
		}
	}
	return false
}

// candidate is the index entry that would satisfy d: the package itself, or
// the first provider (index order puts main before universe).
func (r *linuxPkgResolver) candidate(d linuxPkgDep) (linuxPkgEntry, bool) {
	if e, ok := r.get(d.Name); ok && linuxPkgSatisfies(e.Version, d.Op, d.Version) {
		return e, true
	}
	for _, p := range r.idx.Providers(d.Name) {
		if e, ok := r.get(p); ok && linuxPkgProvidesSatisfies(e.Provides, d) {
			return e, true
		}
	}
	return linuxPkgEntry{}, false
}

// add plans e and, before it, everything it needs.
func (r *linuxPkgResolver) add(e linuxPkgEntry, manual bool) error {
	if manual {
		r.plan.manual[e.Name] = true
	}
	if _, ok := r.planned[e.Name]; ok {
		return nil
	}
	// Marked before its dependencies, so a cycle ends here.
	r.planned[e.Name] = e
	for _, field := range []string{e.PreDepends, e.Depends} {
		for _, group := range linuxPkgParseDeps(field) {
			if err := r.group(group, e.Name); err != nil {
				return err
			}
		}
	}
	for _, c := range linuxPkgCompanions[e.Name] {
		dep := linuxPkgDep{Name: c}
		if r.satisfied(dep) {
			continue
		}
		if ce, ok := r.candidate(dep); ok {
			if err := r.add(ce, false); err != nil {
				r.notes = append(r.notes, "Skipped "+c+": "+err.Error())
			}
		}
	}
	if in, ok := r.st.Packages[e.Name]; ok {
		r.plan.upgrades[e.Name] = in.Version
		r.plan.addBytes -= in.Bytes
	}
	r.plan.install = append(r.plan.install, e)
	r.plan.download += e.Size
	r.plan.addBytes += e.InstalledKB * 1024
	return nil
}

// group satisfies one dependency with alternatives: nothing to do when any
// alternative holds, otherwise the first one the index can provide.
func (r *linuxPkgResolver) group(alts []linuxPkgDep, parent string) error {
	for _, d := range alts {
		if r.satisfied(d) {
			return nil
		}
	}
	for _, d := range alts {
		if e, ok := r.candidate(d); ok {
			return r.add(e, false)
		}
	}
	names := make([]string, len(alts))
	for i, d := range alts {
		names[i] = d.String()
	}
	return fmt.Errorf("%s needs %s, which the package index cannot provide", parent, strings.Join(names, " | "))
}

// want plans a package asked for by name (or a virtual name with a single
// provider).
func (r *linuxPkgResolver) want(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	e, ok := r.get(name)
	if !ok {
		providers := r.idx.Providers(name)
		switch len(providers) {
		case 0:
			return fmt.Errorf("unable to locate package %s", name)
		case 1:
			if e, ok = r.get(providers[0]); !ok {
				return fmt.Errorf("unable to locate package %s", name)
			}
		default:
			return fmt.Errorf("%s is provided by several packages; install one of them: %s", name, strings.Join(providers, ", "))
		}
	}
	// An Essential package (bash, coreutils) is installed when it is asked
	// for by name; only the package-management and boot set is refused.
	if linuxPkgSystem[e.Name] {
		return fmt.Errorf("%s belongs to a full Ubuntu system (package management, boot or users) and is not installed here", e.Name)
	}
	if in, ok := r.st.Packages[e.Name]; ok && in.State == "installed" && linuxPkgVersionCompare(in.Version, e.Version) >= 0 {
		r.plan.manual[e.Name] = true
		r.plan.already = append(r.plan.already, e.Name+" is already the newest version ("+in.Version+").")
		return nil
	}
	return r.add(e, true)
}

// linuxPkgPlanInstall plans `install names…`; a package whose files were
// being written when a run died is unpacked again as part of it.
func linuxPkgPlanInstall(x *linuxPkgIndex, st linuxPkgStatus, names []string) (*linuxPkgPlan, []string, error) {
	r := newLinuxPkgResolver(x, st)
	for _, n := range names {
		if err := r.want(n); err != nil {
			return nil, nil, err
		}
	}
	if err := r.repairs(); err != nil {
		return nil, nil, err
	}
	return r.plan, r.notes, nil
}

// linuxPkgPlanUpgrade plans `upgrade`: every installed package with a newer
// version in the index, and whatever those now need.
func linuxPkgPlanUpgrade(x *linuxPkgIndex, st linuxPkgStatus) (*linuxPkgPlan, []string, error) {
	r := newLinuxPkgResolver(x, st)
	names := make([]string, 0, len(st.Packages))
	for n := range st.Packages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		in := st.Packages[n]
		e, ok := r.get(n)
		if !ok || linuxPkgVersionCompare(e.Version, in.Version) <= 0 {
			continue
		}
		if err := r.add(e, false); err != nil {
			return nil, nil, err
		}
	}
	if err := r.repairs(); err != nil {
		return nil, nil, err
	}
	return r.plan, r.notes, nil
}

func (r *linuxPkgResolver) repairs() error {
	for n, in := range r.st.Packages {
		if in.State == "installed" {
			continue
		}
		if e, ok := r.get(n); ok {
			if err := r.add(e, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// linuxPkgUpgradable lists installed packages with a newer version.
func linuxPkgUpgradable(x *linuxPkgIndex, st linuxPkgStatus) []string {
	var out []string
	for n, in := range st.Packages {
		if e, ok := x.Get(n); ok && linuxPkgVersionCompare(e.Version, in.Version) > 0 {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// linuxPkgDepHeld reports whether one dependency group is satisfied by the
// installed packages outside gone.
func linuxPkgDepHeld(st linuxPkgStatus, group []linuxPkgDep, gone map[string]bool) bool {
	for _, d := range group {
		if in, ok := st.Packages[d.Name]; ok && !gone[d.Name] && linuxPkgSatisfies(in.Version, d.Op, d.Version) {
			return true
		}
		for n, in := range st.Packages {
			if !gone[n] && linuxPkgProvidesSatisfies(in.Provides, d) {
				return true
			}
		}
	}
	return false
}

// linuxPkgPlanRemove is names plus every installed package that would lose
// a dependency it had (apt removes those too).
func linuxPkgPlanRemove(st linuxPkgStatus, names []string) ([]string, error) {
	gone := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if _, ok := st.Packages[n]; !ok {
			return nil, fmt.Errorf("package %s is not installed", n)
		}
		gone[n] = true
	}
	for changed := true; changed; {
		changed = false
		for n, in := range st.Packages {
			if gone[n] {
				continue
			}
			for _, group := range linuxPkgParseDeps(in.PreDepends + ", " + in.Depends) {
				// Only what the removal breaks counts: a group the installed set
				// never held (the system provides it) stays as it was.
				if linuxPkgDepHeld(st, group, nil) && !linuxPkgDepHeld(st, group, gone) {
					gone[n], changed = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(gone))
	for n := range gone {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// linuxPkgAutoremovable are packages that came in as dependencies and that no
// package installed by name needs any more.
func linuxPkgAutoremovable(st linuxPkgStatus) []string {
	keep := map[string]bool{}
	var queue []string
	for n, in := range st.Packages {
		if !in.Auto {
			keep[n] = true
			queue = append(queue, n)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		in := st.Packages[n]
		for _, group := range linuxPkgParseDeps(in.PreDepends + ", " + in.Depends) {
			for _, d := range group {
				for m, other := range st.Packages {
					if keep[m] {
						continue
					}
					if (m == d.Name && linuxPkgSatisfies(other.Version, d.Op, d.Version)) || linuxPkgProvidesSatisfies(other.Provides, d) {
						keep[m] = true
						queue = append(queue, m)
					}
				}
			}
		}
		for _, c := range linuxPkgCompanions[n] {
			if _, ok := st.Packages[c]; ok && !keep[c] {
				keep[c] = true
				queue = append(queue, c)
			}
		}
	}
	var out []string
	for n := range st.Packages {
		if !keep[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ── Unpacking ────────────────────────────────────────────────────────────

// linuxPkgCleanMember is a data.tar member name as a clean relative path
// ("" for the archive's top "./"); ok is false for anything that could leave
// the root: an absolute name, "..", a backslash or a NUL — and for a control
// character, which would break the tab-separated file lists and the wrappers'
// comment lines.
func linuxPkgCleanMember(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return "", true
	}
	if strings.ContainsAny(name, "\\") || strings.HasPrefix(name, "/") || linuxPkgHasControl(name) {
		return "", false
	}
	if path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return "", false
	}
	return name, true
}

// linuxPkgHasControl reports a byte below space (NUL, tab, newline …) or DEL.
func linuxPkgHasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// linuxPkgUsrMerge applies Ubuntu's merged /usr: /bin, /sbin and /lib are links
// into /usr there, so their files are unpacked into usr/ and no folder link is
// ever needed on the card.
func linuxPkgUsrMerge(p string) string {
	first, rest, _ := strings.Cut(p, "/")
	switch first {
	case "bin", "sbin", "lib", "lib64":
		if rest == "" {
			return "usr/" + first
		}
		return "usr/" + first + "/" + rest
	}
	return p
}

// linuxPkgLinkTarget resolves a symlink's target the way the kernel would on an
// Ubuntu system — absolute targets from the root, relative ones from the
// link's own (original) folder — and maps it into the merged /usr. ok is false
// for a target outside the root.
func linuxPkgLinkTarget(link, target string) (string, bool) {
	if target == "" || strings.ContainsAny(target, "\\") || linuxPkgHasControl(target) {
		return "", false
	}
	var p string
	if strings.HasPrefix(target, "/") {
		p = path.Clean(strings.TrimLeft(target, "/"))
	} else {
		p = path.Join(path.Dir(link), target)
	}
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return "", false
	}
	return linuxPkgUsrMerge(p), true
}

// linuxPkgSkipPrefixes are folders not worth card space on a handheld:
// documentation, manual pages, translations and service definitions for an
// init system that is not there.
var linuxPkgSkipPrefixes = []string{
	"usr/share/doc", "usr/share/man", "usr/share/info", "usr/share/lintian", "usr/share/locale",
	"usr/share/bug", "usr/share/doc-base", "usr/lib/systemd", "etc/systemd", "etc/init.d", "etc/init",
}

func linuxPkgSkipped(rel string) bool {
	for _, p := range linuxPkgSkipPrefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	// Names exFAT and FAT32 cannot hold (Perl's "IO::Socket::SSL.3pm" style).
	return strings.ContainsAny(path.Base(rel), `:*?"<>|`)
}

// linuxPkgLink is a link met while unpacking, placed after the whole run.
type linuxPkgLink struct {
	pkg    string
	path   string
	target string
}

// linuxPkgUnpackResult is what one .deb put on the card.
type linuxPkgUnpackResult struct {
	files []string
	links []linuxPkgLink
	bytes int64
}

// linuxPkgOpenData finds data.tar.* in a .deb (an ar archive) and returns a
// reader for its tar stream.
func linuxPkgOpenData(ctx context.Context, f *os.File) (io.Reader, func(), error) {
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || !bytes.Equal(magic, []byte("!<arch>\n")) {
		return nil, nil, errors.New("not a Debian package")
	}
	for {
		h := make([]byte, 60)
		if _, err := io.ReadFull(f, h); err != nil {
			return nil, nil, errors.New("the package has no data.tar")
		}
		if !bytes.Equal(h[58:60], []byte("`\n")) {
			return nil, nil, errors.New("the package has an invalid member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(h[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || size < 0 || size > linuxPkgDebMax {
			return nil, nil, errors.New("the package has an invalid member size")
		}
		if !strings.HasPrefix(name, "data.tar") {
			if _, err := io.CopyN(io.Discard, f, size+size%2); err != nil {
				return nil, nil, err
			}
			continue
		}
		body := io.LimitReader(f, size)
		switch name {
		case "data.tar":
			return body, func() {}, nil
		case "data.tar.zst":
			zr, err := zstd.NewReader(body, zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1))
			if err != nil {
				return nil, nil, err
			}
			return zr, zr.Close, nil
		case "data.tar.xz":
			return newFeXzReader(ctx, body, linuxPkgXzDict), func() {}, nil
		case "data.tar.gz":
			gz, err := gzip.NewReader(body)
			if err != nil {
				return nil, nil, err
			}
			return gz, func() { gz.Close() }, nil
		}
		return nil, nil, fmt.Errorf("the package's %s is in a format JukaHub does not unpack", name)
	}
}

// linuxPkgUnpack writes one verified .deb's files under root.
func linuxPkgUnpack(ctx context.Context, root *os.Root, deb, pkg string) (linuxPkgUnpackResult, error) {
	var res linuxPkgUnpackResult
	f, err := os.Open(deb)
	if err != nil {
		return res, err
	}
	defer f.Close()
	data, closeData, err := linuxPkgOpenData(ctx, f)
	if err != nil {
		return res, err
	}
	defer closeData()
	tr := tar.NewReader(data)
	const maxEntries = 100000
	const maxTotal = int64(4 << 30)
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, err
		}
		entries++
		if entries > maxEntries {
			return res, errors.New("the package has more files than JukaHub unpacks")
		}
		clean, ok := linuxPkgCleanMember(h.Name)
		if !ok {
			return res, fmt.Errorf("the package contains an unsafe path %q", h.Name)
		}
		if clean == "" {
			continue
		}
		rel := linuxPkgUsrMerge(clean)
		if linuxPkgSkipped(rel) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.FromSlash(rel), 0o755); err != nil {
				return res, err
			}
		case tar.TypeReg:
			if h.Size < 0 || h.Size > linuxPkgDebMax || res.bytes+h.Size > maxTotal {
				return res, errors.New("the package unpacks to more than JukaHub allows")
			}
			if err := linuxPkgWriteMember(ctx, root, rel, tr, h.Size, os.FileMode(h.Mode)); err != nil {
				return res, fmt.Errorf("%s: %w", rel, err)
			}
			res.files = append(res.files, rel)
			res.bytes += h.Size
		case tar.TypeSymlink:
			target, ok := linuxPkgLinkTarget(clean, h.Linkname)
			if !ok {
				continue // a link out of the root cannot be made here
			}
			res.links = append(res.links, linuxPkgLink{pkg: pkg, path: rel, target: target})
		case tar.TypeLink:
			cleanTarget, ok := linuxPkgCleanMember(h.Linkname)
			if !ok || cleanTarget == "" {
				return res, fmt.Errorf("the package contains an unsafe link %q", h.Linkname)
			}
			res.links = append(res.links, linuxPkgLink{pkg: pkg, path: rel, target: linuxPkgUsrMerge(cleanTarget)})
		}
	}
}

// linuxPkgWriteMember writes one file through a temporary name and a rename,
// so a program that is running from the old copy keeps its file.
func linuxPkgWriteMember(ctx context.Context, root *os.Root, rel string, r io.Reader, size int64, mode os.FileMode) error {
	p := filepath.FromSlash(rel)
	if dir := filepath.Dir(p); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if info, err := root.Lstat(p); err == nil && info.IsDir() {
		return errors.New("a folder is in the way")
	}
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	tmp := p + ".jpkg-new"
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, &archiveReader{ctx: ctx, r: io.LimitReader(r, size)})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		_ = root.Remove(tmp)
		return err
	}
	_ = root.Chmod(tmp, perm) // a FAT card keeps the mount's modes
	if err := root.Rename(tmp, p); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// linuxPkgCopyInRoot copies a regular file inside the root (a link made real).
func linuxPkgCopyInRoot(root *os.Root, from, to string) error {
	src, err := root.Open(filepath.FromSlash(from))
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	return linuxPkgWriteMember(context.Background(), root, to, src, info.Size(), info.Mode())
}

// linuxPkgCanSymlink reports whether the card holds symbolic links.
func linuxPkgCanSymlink(root *os.Root) bool {
	const probe = ".jpkg-linktest"
	_ = root.Remove(probe)
	err := root.Symlink("status.json", probe)
	_ = root.Remove(probe)
	return err == nil
}

// linuxPkgParentIsLink reports whether a folder on the way to rel (a slash
// path under the root) is a symbolic link.
func linuxPkgParentIsLink(root *os.Root, rel string) bool {
	for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if info, err := root.Lstat(filepath.FromSlash(d)); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// linuxPkgPlaceLinks makes the run's links: symlinks where the card can hold
// them, otherwise copies of their targets. A link whose target is another
// link waits for that one; a link to a folder is left out (counted in dirs).
func linuxPkgPlaceLinks(root *os.Root, links []linuxPkgLink, symlinks bool) (dirs, dangling int) {
	pending := links
	for pass := 0; pass < 8 && len(pending) > 0; pass++ {
		var next []linuxPkgLink
		for _, l := range pending {
			p := filepath.FromSlash(l.path)
			if dir := filepath.Dir(p); dir != "." {
				_ = root.MkdirAll(dir, 0o755)
			}
			if info, err := root.Lstat(p); err == nil && info.IsDir() {
				continue // a real folder already sits there
			}
			// The relative target is worked out from the link's folder as
			// written; through a folder that is itself a link it would point
			// somewhere else, possibly outside the root, so such a link is
			// copied instead.
			if symlinks && !linuxPkgParentIsLink(root, l.path) {
				rel, err := filepath.Rel(filepath.Dir(p), filepath.FromSlash(l.target))
				if err == nil {
					_ = root.Remove(p)
					if root.Symlink(rel, p) == nil {
						continue
					}
				}
			}
			if linuxPkgInExecDir(l.path) {
				// A program's other names (python3 → python3.12, git's
				// hundred builtins → git) are wrappers that start the target
				// under that name; copying the binary for each would cost
				// hundreds of megabytes.
				continue
			}
			info, err := root.Stat(filepath.FromSlash(l.target))
			switch {
			case err != nil:
				next = append(next, l)
			case info.IsDir():
				dirs++
			default:
				if err := linuxPkgCopyInRoot(root, l.target, l.path); err != nil {
					next = append(next, l)
				}
			}
		}
		if len(next) == len(pending) {
			pending = next
			break
		}
		pending = next
	}
	return dirs, len(pending)
}

// ── Lists ────────────────────────────────────────────────────────────────

// A package's list: "F\t<path>" for a file, "L\t<path>\t<target>" for a link.

func linuxPkgListPath(lay linuxPkgLayout, name string) string {
	return filepath.Join(lay.info, name+".list")
}

func linuxPkgReadList(lay linuxPkgLayout, name string) (files []string, links []linuxPkgLink) {
	f, err := os.Open(linuxPkgListPath(lay, name))
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		switch {
		case len(parts) == 2 && parts[0] == "F":
			files = append(files, parts[1])
		case len(parts) == 3 && parts[0] == "L":
			links = append(links, linuxPkgLink{pkg: name, path: parts[1], target: parts[2]})
		}
	}
	return files, links
}

func linuxPkgWriteList(lay linuxPkgLayout, name string, res linuxPkgUnpackResult) error {
	var b strings.Builder
	for _, p := range res.files {
		b.WriteString("F\t" + p + "\n")
	}
	for _, l := range res.links {
		b.WriteString("L\t" + l.path + "\t" + l.target + "\n")
	}
	return linuxPkgWriteFile(linuxPkgListPath(lay, name), []byte(b.String()))
}

// linuxPkgOwned is every path other installed packages own, lower-cased: a
// FAT card ignores case, so "E/Eterm" and "e/eterm" are one file there.
func linuxPkgOwned(lay linuxPkgLayout, st linuxPkgStatus, except map[string]bool) map[string]bool {
	owned := map[string]bool{}
	for n := range st.Packages {
		if except[n] {
			continue
		}
		files, links := linuxPkgReadList(lay, n)
		for _, p := range files {
			owned[strings.ToLower(p)] = true
		}
		for _, l := range links {
			owned[strings.ToLower(l.path)] = true
		}
	}
	return owned
}

// linuxPkgDeletePaths removes paths no other package owns, then the folders
// that left empty.
func linuxPkgDeletePaths(root *os.Root, paths []string, owned map[string]bool) {
	dirs := map[string]bool{}
	for _, p := range paths {
		if owned[strings.ToLower(p)] {
			continue
		}
		fp := filepath.FromSlash(p)
		if info, err := root.Lstat(fp); err == nil && !info.IsDir() {
			_ = root.Remove(fp)
		}
		for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return strings.Count(list[i], "/") > strings.Count(list[j], "/") })
	for _, d := range list {
		_ = root.Remove(filepath.FromSlash(d)) // only succeeds when empty
	}
}

// ── Applying ─────────────────────────────────────────────────────────────

// linuxPkgApply downloads, checks and unpacks a plan's packages, then
// rebuilds the wrappers. The caller holds the lock.
func linuxPkgApply(ctx context.Context, lay linuxPkgLayout, plan *linuxPkgPlan, o *linuxPkgIO) error {
	if len(plan.install) == 0 {
		return nil
	}
	need := plan.download + plan.addBytes + plan.addBytes/10 + 16<<20
	if free, err := filesFreeSpace(lay.base); err == nil && free < need {
		return fmt.Errorf("not enough space on the card: about %s needed, %s free", formatSize(need), formatSize(free))
	}
	debs := make([]string, len(plan.install))
	var done int64
	for i, e := range plan.install {
		o.line(fmt.Sprintf("Get:%d %s %s [%s]", i+1, e.Name, e.Version, formatSize(e.Size)))
		debs[i] = filepath.Join(lay.cache, path.Base(e.Filename))
		base := done
		progress := func(n, _ int64) {
			if o.progress != nil {
				o.progress(base+n, plan.download)
			}
		}
		if err := linuxPkgFetch(ctx, e.Filename, debs[i], 0, &linuxPkgSum{SHA256: e.SHA256, Size: e.Size}, progress); err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		done += e.Size
	}
	root, err := os.OpenRoot(lay.root)
	if err != nil {
		return err
	}
	defer root.Close()
	symlinks := linuxPkgCanSymlink(root)
	st := linuxPkgLoadStatus(lay)
	var links []linuxPkgLink
	written := map[string]bool{}
	libc := false
	for i, e := range plan.install {
		if err := ctx.Err(); err != nil {
			return err
		}
		verb := "Unpacking"
		if old, ok := plan.upgrades[e.Name]; ok {
			verb = "Upgrading " + old + " →"
		}
		o.line(fmt.Sprintf("%s %s (%s)…", verb, e.Name, e.Version))
		prev, hadPrev := st.Packages[e.Name]
		auto := true
		if hadPrev {
			auto = prev.Auto
		}
		if plan.manual[e.Name] {
			auto = false
		}
		oldFiles, oldLinks := linuxPkgReadList(lay, e.Name)
		mark := prev
		mark.State, mark.Version, mark.Auto = "unpacking", e.Version, auto
		st.Packages[e.Name] = mark
		if err := linuxPkgSaveStatus(lay, st); err != nil {
			return err
		}
		res, err := linuxPkgUnpack(ctx, root, debs[i], e.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		if err := linuxPkgWriteList(lay, e.Name, res); err != nil {
			return err
		}
		// What the old version had and the new one does not.
		if len(oldFiles)+len(oldLinks) > 0 {
			now := map[string]bool{}
			for _, p := range res.files {
				now[p] = true
			}
			for _, l := range res.links {
				now[l.path] = true
			}
			var stale []string
			for _, p := range oldFiles {
				if !now[p] {
					stale = append(stale, p)
				}
			}
			for _, l := range oldLinks {
				if !now[l.path] {
					stale = append(stale, l.path)
				}
			}
			if len(stale) > 0 {
				linuxPkgDeletePaths(root, stale, linuxPkgOwned(lay, st, map[string]bool{e.Name: true}))
			}
		}
		for _, p := range res.files {
			written[p] = true
		}
		links = append(links, res.links...)
		st.Packages[e.Name] = linuxPkgInstalled{
			Version: e.Version, Arch: e.Arch, Auto: auto, State: "installed",
			Section: e.Section, Desc: e.Desc, Depends: e.Depends, PreDepends: e.PreDepends, Provides: e.Provides,
			Bytes: res.bytes,
		}
		if err := linuxPkgSaveStatus(lay, st); err != nil {
			return err
		}
		_ = os.Remove(debs[i])
		if e.Name == "libc6" {
			libc = true
		}
	}
	if !symlinks {
		// Copies of a file that this run replaced (python3 is a link to
		// python3.12 in another package) are made again from the new file.
		inRun := map[string]bool{}
		for _, e := range plan.install {
			inRun[e.Name] = true
		}
		for n := range st.Packages {
			if inRun[n] {
				continue
			}
			_, pkgLinks := linuxPkgReadList(lay, n)
			for _, l := range pkgLinks {
				if written[l.target] {
					links = append(links, l)
				}
			}
		}
	}
	dirs, dangling := linuxPkgPlaceLinks(root, links, symlinks)
	if dirs+dangling > 0 {
		logInfo("linuxpkg", "links: %d made, %d to folders left out, %d with no target (symlinks on card: %v)",
			len(links)-dirs-dangling, dirs, dangling, symlinks)
	}
	if err := linuxPkgWriteRuntime(lay, st); err != nil {
		o.line("Warning: could not write the program wrappers: " + err.Error())
	}
	if libc || !linuxPkgLoaderChecked.Load() {
		if msg, err := linuxPkgProbeLoader(ctx, lay); err != nil {
			o.line("Warning: " + err.Error())
		} else if libc {
			o.line(msg)
		}
	}
	linuxPkgRefreshCounts(lay, st)
	return nil
}

// linuxPkgRemove deletes the named installed packages' files.
func linuxPkgRemove(lay linuxPkgLayout, names []string, o *linuxPkgIO) error {
	root, err := os.OpenRoot(lay.root)
	if err != nil {
		return err
	}
	defer root.Close()
	st := linuxPkgLoadStatus(lay)
	gone := map[string]bool{}
	for _, n := range names {
		gone[n] = true
	}
	owned := linuxPkgOwned(lay, st, gone)
	for _, n := range names {
		if _, ok := st.Packages[n]; !ok {
			continue
		}
		o.line("Removing " + n + " (" + st.Packages[n].Version + ")…")
		files, links := linuxPkgReadList(lay, n)
		paths := append([]string{}, files...)
		for _, l := range links {
			paths = append(paths, l.path)
		}
		linuxPkgDeletePaths(root, paths, owned)
		_ = os.Remove(linuxPkgListPath(lay, n))
		delete(st.Packages, n)
		if err := linuxPkgSaveStatus(lay, st); err != nil {
			return err
		}
	}
	if err := linuxPkgWriteRuntime(lay, st); err != nil {
		o.line("Warning: could not rewrite the program wrappers: " + err.Error())
	}
	linuxPkgRefreshCounts(lay, st)
	return nil
}

// linuxPkgRefreshCounts recomputes the page's "updates available" count.
func linuxPkgRefreshCounts(lay linuxPkgLayout, st linuxPkgStatus) {
	meta := linuxPkgLoadMeta(lay)
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return
	}
	defer x.Close()
	meta.Upgradable = len(linuxPkgUpgradable(x, st))
	_ = linuxPkgSaveMeta(lay, meta)
	linuxPkgSummaryForget()
}
