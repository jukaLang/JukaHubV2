package main

// linuxpkg_runtime.go — how a program from Linux packages runs on the handheld.
//
// Ubuntu's programs need glibc 2.39; the firmware has 2.33 in /lib, which the
// kernel would load for them. So nothing in <data>/linux/root is started
// directly. Every program gets a small #!/bin/sh wrapper in <data>/linux/bin
// that execs Ubuntu's own loader from the root —
//
//	ld-linux-aarch64.so.1 --inhibit-cache --library-path <root lib dirs> --argv0 "$0" <program> "$@"
//
// — so only that process (and what it starts through these wrappers) uses the
// card's libraries. LD_LIBRARY_PATH is never set: firmware tools keep the
// firmware's libraries. A script (a Python or Perl program from a package)
// gets a wrapper that runs it with the wrapped interpreter. What relocated
// programs need to find their own files — PYTHONHOME, Perl's @INC, gconv
// modules, terminal descriptions, CA certificates, Nmap's data — is written
// once into bin/.jpkg-env, which every wrapper sources. All of it is rebuilt
// from the installed packages after every change.
//
// <data>/linux/bin is first on PATH for the Terminal's commands only. A
// firmware SSH shell can source <data>/linux/env.sh, or call the wrappers by
// path; bin/apt runs `JukaHub apt` for it.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	linuxPkgLoaderRel   = "usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1"
	linuxPkgWrapperMark = "# JukaHub Linux packages:"
)

// linuxPkgLoaderChecked is set once Ubuntu's loader has started on this
// kernel in this session.
var linuxPkgLoaderChecked atomic.Bool

// linuxPkgShQuote quotes s for /bin/sh.
func linuxPkgShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// linuxPkgExecDirs are the root folders whose programs get wrappers, and
// where the wrappers go ("" = bin).
var linuxPkgExecDirs = []struct{ dir, sub string }{
	{"usr/bin/", ""}, {"usr/sbin/", ""}, {"usr/games/", ""},
	// git starts its helpers (git-remote-https …) from its exec path, which
	// GIT_EXEC_PATH points at these wrappers instead.
	{"usr/lib/git-core/", "git-core"},
}

// linuxPkgInExecDir reports a path directly inside one of linuxPkgExecDirs.
func linuxPkgInExecDir(p string) bool {
	for _, d := range linuxPkgExecDirs {
		if rest, ok := strings.CutPrefix(p, d.dir); ok && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}

// linuxPkgLibDirs is the loader's search path: the multiarch and plain library
// folders, and any folder under the multiarch one that holds shared libraries
// itself (blas/, lapack/ — on Ubuntu, alternatives point into them).
func linuxPkgLibDirs(root string) []string {
	multi := filepath.Join(root, "usr", "lib", "aarch64-linux-gnu")
	dirs := []string{multi, filepath.Join(root, "usr", "lib")}
	entries, err := os.ReadDir(multi)
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if !e.IsDir() || len(dirs) >= 40 {
			continue
		}
		sub := filepath.Join(multi, e.Name())
		inner, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range inner {
			if n := f.Name(); !f.IsDir() && strings.HasPrefix(n, "lib") && strings.Contains(n, ".so") {
				dirs = append(dirs, sub)
				break
			}
		}
	}
	return dirs
}

// linuxPkgPerlDirs is Ubuntu perl's @INC under the root, real folders only (the
// "5.38.2" names are links to "5.38" on Ubuntu).
func linuxPkgPerlDirs(root string) []string {
	var out []string
	add := func(p string) {
		if info, err := os.Lstat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	add(filepath.Join(root, "etc", "perl"))
	for _, pattern := range []string{
		"usr/lib/aarch64-linux-gnu/perl5/5.*",
		"usr/share/perl5",
		"usr/lib/aarch64-linux-gnu/perl-base",
		"usr/lib/aarch64-linux-gnu/perl/5.*",
		"usr/share/perl/5.*",
	} {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		sort.Strings(matches)
		for _, m := range matches {
			add(m)
		}
	}
	return out
}

// linuxPkgEnvScript is bin/.jpkg-env for what is installed now.
func linuxPkgEnvScript(lay linuxPkgLayout) string {
	root := lay.root
	exists := func(rel string) (string, bool) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_, err := os.Stat(p)
		return p, err == nil
	}
	var b strings.Builder
	b.WriteString(linuxPkgWrapperMark + " what Ubuntu's programs need to run from the card. Generated; rewritten after every change.\n")
	b.WriteString("JPKG_ROOT=" + linuxPkgShQuote(root) + "\n")
	b.WriteString("JPKG_LD=" + linuxPkgShQuote(filepath.Join(root, filepath.FromSlash(linuxPkgLoaderRel))) + "\n")
	b.WriteString("JPKG_LIBS=" + linuxPkgShQuote(strings.Join(linuxPkgLibDirs(root), ":")) + "\n")
	// A firmware preload would be loaded into a glibc 2.39 process.
	b.WriteString("unset LD_PRELOAD\n")
	b.WriteString("case \":$PATH:\" in *:" + linuxPkgShQuote(lay.bin) + ":*) ;; *) PATH=" + linuxPkgShQuote(lay.bin) + ":\"$PATH\"; export PATH ;; esac\n")
	export := func(name, value string) {
		b.WriteString("export " + name + "=" + linuxPkgShQuote(value) + "\n")
	}
	if m, _ := filepath.Glob(filepath.Join(root, "usr", "lib", "python3.*")); len(m) > 0 {
		export("PYTHONHOME", filepath.Join(root, "usr"))
	}
	if dirs := linuxPkgPerlDirs(root); len(dirs) > 0 {
		export("PERL5LIB", strings.Join(dirs, ":"))
	}
	if p, ok := exists("usr/lib/aarch64-linux-gnu/gconv"); ok {
		export("GCONV_PATH", p)
	}
	var terminfo []string
	for _, rel := range []string{"usr/share/terminfo", "usr/lib/terminfo", "etc/terminfo"} {
		if p, ok := exists(rel); ok {
			terminfo = append(terminfo, p)
		}
	}
	if len(terminfo) > 0 {
		// The empty last entry keeps the firmware's own descriptions too.
		export("TERMINFO_DIRS", strings.Join(terminfo, ":")+":")
	}
	if p, ok := exists("etc/ssl/certs/ca-certificates.crt"); ok {
		for _, name := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "REQUESTS_CA_BUNDLE"} {
			export(name, p)
		}
		export("SSL_CERT_DIR", filepath.Dir(p))
	}
	if p, ok := exists("etc/ssl/openssl.cnf"); ok {
		export("OPENSSL_CONF", p)
	}
	if p, ok := exists("usr/share/zoneinfo"); ok {
		export("TZDIR", p)
		export("PYTHONTZPATH", p)
	}
	if dirExists(filepath.Join(lay.libexec, "git-core")) {
		export("GIT_EXEC_PATH", filepath.Join(lay.libexec, "git-core"))
	}
	if p, ok := exists("usr/share/git-core/templates"); ok {
		export("GIT_TEMPLATE_DIR", p)
	}
	if p, ok := exists("usr/share/nmap"); ok {
		export("NMAPDIR", p)
	}
	for _, rel := range []string{"usr/lib/file/magic.mgc", "usr/share/misc/magic.mgc"} {
		if p, ok := exists(rel); ok {
			export("MAGIC", p)
			break
		}
	}
	// Only the C locales exist here (glibc has C.UTF-8 built in); asking for
	// another makes Perl print a page of warnings on every start.
	b.WriteString("case \"${LC_ALL:-${LC_CTYPE:-${LANG:-C}}}\" in C|C.*|POSIX) ;; *) LC_ALL=C.UTF-8; export LC_ALL ;; esac\n")
	return b.String()
}

// linuxPkgShebang reads a script's #! line: the interpreter's name (after env,
// when the line uses it) and its one optional argument.
func linuxPkgShebang(head []byte) (name, arg string, ok bool) {
	if !bytes.HasPrefix(head, []byte("#!")) {
		return "", "", false
	}
	line := string(head[2:])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	interp, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	if path.Base(interp) == "env" {
		interp, rest, _ = strings.Cut(rest, " ")
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(interp, "-") {
			return "", "", false
		}
	}
	name = path.Base(interp)
	if name == "" || name == "." || name == "/" {
		return "", "", false
	}
	return name, rest, true
}

// linuxPkgWrapperFor is one wrapper: its file and its script.
type linuxPkgWrapperFor struct {
	file string
	body string
}

// linuxPkgWriteRuntime rebuilds .jpkg-env, env.sh, every wrapper, the CA
// bundle and the apt wrapper from the installed packages.
func linuxPkgWriteRuntime(lay linuxPkgLayout, st linuxPkgStatus) error {
	linuxPkgWriteCABundle(lay)
	envFile := filepath.Join(lay.bin, ".jpkg-env")
	if err := linuxPkgWriteIfChanged(envFile, linuxPkgEnvScript(lay), 0o644); err != nil {
		return err
	}
	_ = linuxPkgWriteIfChanged(filepath.Join(lay.base, "env.sh"),
		linuxPkgWrapperMark+" source this from a firmware shell to use the installed programs.\n"+
			"PATH="+linuxPkgShQuote(lay.bin)+":\"$PATH\"; export PATH\n", 0o644)
	root, err := os.OpenRoot(lay.root)
	if err != nil {
		return err
	}
	defer root.Close()

	// Every program the installed packages own, by wrapper folder.
	type prog struct{ rel, sub, name, pkg string }
	var progs []prog
	names := make([]string, 0, len(st.Packages))
	for n := range st.Packages {
		names = append(names, n)
	}
	sort.Strings(names)
	linkTo := map[string]string{}
	for _, n := range names {
		files, links := linuxPkgReadList(lay, n)
		paths := append([]string{}, files...)
		for _, l := range links {
			paths = append(paths, l.path)
			linkTo[l.path] = l.target
		}
		for _, p := range paths {
			for _, d := range linuxPkgExecDirs {
				rest, ok := strings.CutPrefix(p, d.dir)
				if ok && rest != "" && !strings.Contains(rest, "/") {
					progs = append(progs, prog{rel: p, sub: d.sub, name: rest, pkg: n})
				}
			}
		}
	}
	wrappers := map[string]linuxPkgWrapperFor{}
	elf := map[string]bool{}
	// A program's other names are links that were not copied (see
	// linuxPkgPlaceLinks): their wrappers start the file the links lead to.
	resolve := func(rel string) string {
		for hop := 0; hop < 8; hop++ {
			if info, err := root.Lstat(filepath.FromSlash(rel)); err == nil && info.Mode().IsRegular() {
				return rel
			}
			next, ok := linkTo[rel]
			if !ok {
				return rel
			}
			rel = next
		}
		return rel
	}
	head := func(rel string) []byte {
		f, err := root.Open(filepath.FromSlash(rel))
		if err != nil {
			return nil
		}
		defer f.Close()
		if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		buf := make([]byte, 256)
		n, _ := f.Read(buf)
		return buf[:n]
	}
	dirFor := func(sub string) string {
		if sub == "" {
			return lay.bin
		}
		return filepath.Join(lay.libexec, sub)
	}
	envLine := ". " + linuxPkgShQuote(envFile) + "\n"
	// Programs first, so a script's interpreter is known when its wrapper is
	// written.
	var scripts []prog
	for _, p := range progs {
		// A dot name would replace .jpkg-env (and is skipped by the clean-up
		// below anyway).
		if (p.sub == "" && linuxPkgReserved[p.name]) || strings.HasPrefix(p.name, ".") {
			continue
		}
		p.rel = resolve(p.rel)
		h := head(p.rel)
		real := filepath.Join(lay.root, filepath.FromSlash(p.rel))
		file := filepath.Join(dirFor(p.sub), p.name)
		switch {
		case bytes.HasPrefix(h, []byte("\x7fELF")):
			wrappers[file] = linuxPkgWrapperFor{file: file, body: "#!/bin/sh\n" + linuxPkgWrapperMark + " " + p.pkg + " " + p.name + "\n" + envLine +
				"exec \"$JPKG_LD\" --inhibit-cache --library-path \"$JPKG_LIBS\" --argv0 \"$0\" " + linuxPkgShQuote(real) + " \"$@\"\n"}
			if p.sub == "" {
				elf[p.name] = true
			}
		case bytes.HasPrefix(h, []byte("#!")):
			scripts = append(scripts, p)
		case p.sub != "" && len(h) > 0:
			// git's shell libraries (git-sh-setup) are sourced from its exec
			// path, not run: a shim sources the real one.
			wrappers[file] = linuxPkgWrapperFor{file: file, body: linuxPkgWrapperMark + " " + p.pkg + " " + p.name + "\n. " + linuxPkgShQuote(real) + "\n"}
		}
	}
	for _, p := range scripts {
		name, arg, ok := linuxPkgShebang(head(p.rel))
		if !ok {
			continue
		}
		real := filepath.Join(lay.root, filepath.FromSlash(p.rel))
		file := filepath.Join(dirFor(p.sub), p.name)
		var interp string
		switch {
		case elf[name]:
			interp = linuxPkgShQuote(filepath.Join(lay.bin, name))
		case name == "bash" && fileExists("/bin/bash"):
			interp = "/bin/bash"
		case name == "sh" || name == "bash" || name == "dash":
			interp = "/bin/sh"
		default:
			interp = linuxPkgShQuote(name) // whatever PATH finds
		}
		if arg != "" {
			interp += " " + linuxPkgShQuote(arg)
		}
		wrappers[file] = linuxPkgWrapperFor{file: file, body: "#!/bin/sh\n" + linuxPkgWrapperMark + " " + p.pkg + " " + p.name + "\n" + envLine +
			"exec " + interp + " " + linuxPkgShQuote(real) + " \"$@\"\n"}
	}

	// Write what changed, then drop wrappers of programs that are gone.
	written := 0
	for _, w := range wrappers {
		if err := os.MkdirAll(filepath.Dir(w.file), 0o755); err != nil {
			return err
		}
		if err := linuxPkgWriteIfChanged(w.file, w.body, 0o755); err != nil {
			return err
		}
		written++
	}
	for _, dir := range []string{lay.bin, filepath.Join(lay.libexec, "git-core")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if _, keep := wrappers[p]; keep || e.IsDir() || strings.HasPrefix(e.Name(), ".") || linuxPkgReserved[e.Name()] {
				continue
			}
			if linuxPkgIsWrapper(p) {
				_ = os.Remove(p)
			}
		}
		if dir != lay.bin {
			_ = os.Remove(dir) // only when empty
		}
	}
	// GIT_EXEC_PATH depends on whether git-core wrappers exist.
	_ = linuxPkgWriteIfChanged(envFile, linuxPkgEnvScript(lay), 0o644)
	linuxPkgWriteAptWrapper(lay)
	logInfo("linuxpkg", "runtime rebuilt: %d wrappers for %d packages", written, len(st.Packages))
	return nil
}

// linuxPkgIsWrapper reports a file JukaHub generated (its mark in the first
// lines), so a file the user put in bin/ is never deleted.
func linuxPkgIsWrapper(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 160)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte(linuxPkgWrapperMark))
}

// linuxPkgWriteIfChanged writes a small generated file unless it already has
// that content (the card is written as little as possible).
func linuxPkgWriteIfChanged(p, content string, mode os.FileMode) error {
	if old, err := os.ReadFile(p); err == nil && string(old) == content {
		return nil
	}
	if err := linuxPkgWriteFile(p, []byte(content)); err != nil {
		return err
	}
	_ = os.Chmod(p, mode)
	return nil
}

// linuxPkgWriteAptWrapper makes bin/apt (and jpkg): `JukaHub apt …` for a
// firmware shell, with the library path JukaHub itself was started with.
func linuxPkgWriteAptWrapper(lay linuxPkgLayout) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n" + linuxPkgWrapperMark + " apt, the handheld's package manager (JukaHub apt).\n")
	if lib := os.Getenv("LD_LIBRARY_PATH"); lib != "" {
		b.WriteString("LD_LIBRARY_PATH=" + linuxPkgShQuote(lib) + " ")
	}
	b.WriteString("exec " + linuxPkgShQuote(exe) + " apt \"$@\"\n")
	for _, name := range []string{"apt", "jpkg"} {
		_ = linuxPkgWriteIfChanged(filepath.Join(lay.bin, name), b.String(), 0o755)
	}
}

// linuxPkgWriteCABundle does what ca-certificates' maintainer script does:
// one bundle of Mozilla's roots that OpenSSL, GnuTLS, curl, Python and Perl
// read through SSL_CERT_FILE.
func linuxPkgWriteCABundle(lay linuxPkgLayout) {
	src := filepath.Join(lay.root, "usr", "share", "ca-certificates", "mozilla")
	dst := filepath.Join(lay.root, "etc", "ssl", "certs", "ca-certificates.crt")
	entries, err := os.ReadDir(src)
	if err != nil {
		_ = os.Remove(dst)
		return
	}
	var b bytes.Buffer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".crt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return
	}
	_ = linuxPkgWriteIfChanged(dst, b.String(), 0o644)
}

// linuxPkgProbeLoader starts Ubuntu's loader once: the proof that glibc 2.39
// runs on this kernel from this card.
func linuxPkgProbeLoader(ctx context.Context, lay linuxPkgLayout) (string, error) {
	loader := filepath.Join(lay.root, filepath.FromSlash(linuxPkgLoaderRel))
	if !regularFile(loader) {
		return "", nil
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, loader, "--version")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	first := strings.TrimSpace(firstLine(string(out)))
	if err != nil {
		logWarn("linuxpkg", "Ubuntu's loader did not start: %v: %s", err, first)
		return "", fmt.Errorf("Ubuntu's C library did not start on this device (%v); the installed programs cannot run", err)
	}
	linuxPkgLoaderChecked.Store(true)
	logInfo("linuxpkg", "loader runs: %s", first)
	return "C library: " + first, nil
}

// linuxPkgShellEnv puts <data>/linux/bin first on PATH in env, for the
// Terminal's commands. Elsewhere env is returned as it is.
func linuxPkgShellEnv(env []string) []string {
	if !linuxPkgSupported() {
		return env
	}
	lay, err := linuxPkgDirs()
	if err != nil || !dirExists(lay.bin) {
		return env
	}
	for i, kv := range env {
		if rest, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + lay.bin + ":" + rest
			return env
		}
	}
	return append(env, "PATH="+lay.bin+":/usr/sbin:/usr/bin:/sbin:/bin")
}

// linuxPkgWrapperPath is the wrapper of an installed program, or "".
func linuxPkgWrapperPath(name string) string {
	if !linuxPkgSupported() || name == "" || strings.ContainsAny(name, `/\`) {
		return ""
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return ""
	}
	p := filepath.Join(lay.bin, name)
	if regularFile(p) && linuxPkgIsWrapper(p) {
		return p
	}
	return ""
}
