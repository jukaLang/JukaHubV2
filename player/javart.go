package main

// javart.go — which Java JukaHub uses, and how other parts of the app ask for
// one.
//
// JukaHub can install Eclipse Temurin (javart_install.go) into <data>/java: a
// JRE that runs programs, or a JDK that also has javac, jshell, jar and the
// source-file launcher (`java Hello.java`). When it has not, a Java the
// computer already has is used: JAVA_HOME, the PATH, /usr/lib/jvm on Linux,
// and on Windows the JavaSoft registry keys and the usual Program Files
// folders (javart_windows.go / javart_other.go). JukaHub's own install wins.
//
// Finding Java must never cost the render thread anything: javaFind answers
// from a cache that a goroutine fills. The goroutine reads JukaHub's install
// marker (no process is started for it: its version was probed when it was
// installed) and starts `java -version` only for a system Java whose file is
// new or changed since the last probe (cached by path, size and time).
//
// Cross-area contract (Agent B's J2ME games use it): javaRuntime, javaFind,
// javaShellEnv, javaEnsure and javaMIDletOpener.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// javaRuntime is one usable Java installation.
type javaRuntime struct {
	Home    string // JAVA_HOME (the folder holding bin/)
	Java    string // full path of java (java.exe)
	Javaw   string // Windows: javaw.exe beside it, else ""
	Javac   string // full path of javac when this is a JDK, else ""
	Major   int    // feature version: 8, 11, 17, 21, 25 …
	Version string // full version, e.g. "25.0.1+8"
	Managed bool   // true when JukaHub installed it, false for a system Java
}

// javaMIDletOpener plays a J2ME phone game (a .jar whose manifest names a
// MIDlet). Agent B's freej2me support sets it in init(); nil means JukaHub has
// no J2ME support and Files explains that instead.
var javaMIDletOpener func(config *Config, path string) bool

const (
	// javaScanMaxAge is how long a scan is trusted. A system Java the user
	// installs meanwhile shows up after it; JukaHub's own changes call
	// javaForget and show up at once.
	javaScanMaxAge = 2 * time.Minute
	// javaMaxSystem bounds how many system Javas one scan probes.
	javaMaxSystem = 8
	// javaProbeTimeout bounds `java -version`. The first start of a JVM from
	// an SD card can take several seconds.
	javaProbeTimeout = 40 * time.Second
	// javaProbeMaxOutput bounds what a probe reads back.
	javaProbeMaxOutput = 64 << 10
)

// javaProbeResult is one `java -version`, remembered for the file it ran.
type javaProbeResult struct {
	size    int64
	mod     time.Time
	major   int
	version string
	err     string
	until   time.Time // zero: kept while the file is unchanged
}

var javaState struct {
	mu       sync.Mutex
	scanned  bool          // a scan has finished at least once
	at       time.Time     // when the last scan finished; zero = stale
	scanning bool          // a scan is running
	again    bool          // javaForget was called during the running scan
	all      []javaRuntime // usable Javas, the one javaFind returns first
	note     string        // why a system Java that was found cannot be used
	waiters  []func()      // main-thread callbacks for the end of the scan
	probes   map[string]javaProbeResult
}

// javaFind is the Java JukaHub would use now. Cheap and safe on the render
// thread: it answers from the last scan and starts a new one in the
// background when that is stale. Before the first scan ends it reports false.
func javaFind() (javaRuntime, bool) {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	javaKickScanLocked()
	if len(javaState.all) == 0 {
		return javaRuntime{}, false
	}
	return javaState.all[0], true
}

// javaFindFor is the first known Java that is at least minMajor (and a JDK
// when needJDK), JukaHub's own first. It answers from the cache.
func javaFindFor(minMajor int, needJDK bool) (javaRuntime, bool) {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	javaKickScanLocked()
	for _, rt := range javaState.all {
		if javaSatisfies(rt, minMajor, needJDK) {
			return rt, true
		}
	}
	return javaRuntime{}, false
}

// javaSatisfies reports whether rt meets a request.
func javaSatisfies(rt javaRuntime, minMajor int, needJDK bool) bool {
	return rt.Java != "" && rt.Major >= minMajor && (!needJDK || rt.Javac != "")
}

// javaKnownRuntimes is every usable Java the last scan found, the one
// javaFind returns first.
func javaKnownRuntimes() []javaRuntime {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	javaKickScanLocked()
	return append([]javaRuntime(nil), javaState.all...)
}

// javaDescribe is one Java in words: "Java 25.0.1+8 (JRE) · installed by
// JukaHub".
func javaDescribe(rt javaRuntime) string {
	kind := javaKindJRE
	if rt.Javac != "" {
		kind = javaKindJDK
	}
	where := "on this computer"
	if rt.Managed {
		where = "installed by JukaHub"
	}
	version := rt.Version
	if version == "" {
		version = strconv.Itoa(rt.Major)
	}
	return "Java " + version + " (" + javaKindLabel(kind) + ") · " + where
}

// javaEnvSet reports whether an environment variable has a value.
func javaEnvSet(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

// javaScanNote is why a system Java that was found was not usable, or "".
func javaScanNote() string {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	return javaState.note
}

// javaScanFresh reports whether the cache can be believed right now: a scan
// finished recently and none is running.
func javaScanFresh() bool {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	return javaState.scanned && !javaState.scanning && !javaState.at.IsZero() && time.Since(javaState.at) <= javaScanMaxAge
}

// javaForget makes the next javaFind look again (after an install or a
// removal).
func javaForget() {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	javaState.at = time.Time{}
	if javaState.scanning {
		javaState.again = true
	}
}

// javaDropManaged is javaForget after JukaHub's Java was moved away: it also
// leaves the cache at once, so nothing starts the deleted Java before the new
// scan answers.
func javaDropManaged() {
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	var kept []javaRuntime
	for _, rt := range javaState.all {
		if !rt.Managed {
			kept = append(kept, rt)
		}
	}
	javaState.all = kept
	javaState.at = time.Time{}
	if javaState.scanning {
		javaState.again = true
	}
}

// javaWhenScanned runs fn on the main thread once a fresh scan has finished:
// at once when the cache is fresh, else after the scan it starts or joins.
func javaWhenScanned(fn func()) {
	if fn == nil {
		return
	}
	javaState.mu.Lock()
	if javaState.scanned && !javaState.scanning && !javaState.at.IsZero() && time.Since(javaState.at) <= javaScanMaxAge {
		javaState.mu.Unlock()
		fn()
		return
	}
	javaState.waiters = append(javaState.waiters, fn)
	javaKickScanLocked()
	javaState.mu.Unlock()
}

// javaKickScanLocked starts a background scan when the cache is stale and
// none is running. javaState.mu is held.
func javaKickScanLocked() {
	if javaState.scanning {
		return
	}
	if javaState.scanned && !javaState.at.IsZero() && time.Since(javaState.at) <= javaScanMaxAge {
		return
	}
	javaState.scanning = true
	go javaScanWorker()
}

// javaScanWorker scans until no javaForget arrived meanwhile, stores the
// result and hands the waiters to the main thread. The first scan of a run
// also has the leftovers of a cut-off install deleted, on the side.
func javaScanWorker() {
	javaTidyOnce.Do(func() { go javaTidyLeftovers() })
	defer func() {
		if r := recover(); r != nil {
			logWarn("java", "looking for Java failed: %v", r)
			javaState.mu.Lock()
			javaState.scanning, javaState.scanned, javaState.at = false, true, time.Now()
			waiters := javaState.waiters
			javaState.waiters = nil
			javaState.mu.Unlock()
			for _, w := range waiters {
				tbxPost(w)
			}
		}
	}()
	for {
		all, note := javaScan()
		javaState.mu.Lock()
		if javaState.again {
			javaState.again = false
			javaState.mu.Unlock()
			continue
		}
		javaState.all, javaState.note = all, note
		javaState.scanning, javaState.scanned, javaState.at = false, true, time.Now()
		waiters := javaState.waiters
		javaState.waiters = nil
		javaState.mu.Unlock()
		for _, w := range waiters {
			tbxPost(w)
		}
		return
	}
}

// javaRescanNow puts the Java an install just finished first in the cache,
// so the caller and javaFind agree at once, and has a full scan follow. It
// starts no process (a slow system Java must not hold up the end of an
// install). Never on the render thread: it reads the marker.
func javaRescanNow() {
	rt, ok := javaManagedRuntime()
	javaState.mu.Lock()
	defer javaState.mu.Unlock()
	var all []javaRuntime
	if ok {
		all = append(all, rt)
	}
	for _, other := range javaState.all {
		if !other.Managed && (!ok || javaPathKey(other.Java) != javaPathKey(rt.Java)) {
			all = append(all, other)
		}
	}
	javaState.all = all
	javaState.at = time.Time{}
	if javaState.scanning {
		javaState.again = true
	}
}

// javaScan lists the usable Javas: JukaHub's own first, then the system's in
// the order the user chose them (JAVA_HOME, PATH) and then the usual folders.
// Off the main thread: it may start `java -version`.
func javaScan() ([]javaRuntime, string) {
	var all []javaRuntime
	if rt, ok := javaManagedRuntime(); ok {
		all = append(all, rt)
	}
	note := ""
	seen := map[string]bool{}
	if len(all) > 0 {
		seen[javaPathKey(all[0].Java)] = true
	}
	probed := 0
	for _, cand := range javaSystemCandidates() {
		if probed >= javaMaxSystem {
			break
		}
		resolved := cand
		if r, err := filepath.EvalSymlinks(cand); err == nil {
			resolved = r
		}
		key := javaPathKey(resolved)
		if seen[key] || !regularFile(resolved) {
			continue
		}
		seen[key] = true
		probed++
		major, version, err := javaProbeCached(resolved)
		if err != nil {
			if note == "" {
				note = resolved + ": " + err.Error()
			}
			continue
		}
		all = append(all, javaRuntimeAt(resolved, major, version, false))
	}
	return all, note
}

// javaPathKey compares paths the way the filesystem does.
func javaPathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// javaExeName is the java launcher's file name here; javaToolName any JDK
// tool's (javac, jshell, jar).
func javaExeName() string { return javaToolName("java") }

func javaToolName(tool string) string {
	if runtime.GOOS == "windows" {
		return tool + ".exe"
	}
	return tool
}

// javaRuntimeAt describes the Java whose launcher is java (symlinks already
// resolved). A Java 8 JDK keeps its JRE in jre/, with javac one folder up.
func javaRuntimeAt(java string, major int, version string, managed bool) javaRuntime {
	bin := filepath.Dir(java)
	home := filepath.Dir(bin)
	rt := javaRuntime{Home: home, Java: java, Major: major, Version: version, Managed: managed}
	if runtime.GOOS == "windows" {
		if w := filepath.Join(bin, "javaw.exe"); regularFile(w) {
			rt.Javaw = w
		}
	}
	if c := filepath.Join(bin, javaToolName("javac")); regularFile(c) {
		rt.Javac = c
	} else if strings.EqualFold(filepath.Base(home), "jre") {
		if c := filepath.Join(filepath.Dir(home), "bin", javaToolName("javac")); regularFile(c) {
			rt.Javac = c
			rt.Home = filepath.Dir(home)
		}
	}
	return rt
}

// javaToolPath is a JDK tool of rt ("java", "javaw", "javac", "jshell",
// "jar"), or "" when rt does not have it.
func javaToolPath(rt javaRuntime, tool string) string {
	switch tool {
	case "java":
		return rt.Java
	case "javaw":
		if rt.Javaw != "" {
			return rt.Javaw
		}
		return rt.Java
	case "javac":
		return rt.Javac
	}
	if rt.Javac == "" {
		return ""
	}
	p := filepath.Join(filepath.Dir(rt.Javac), javaToolName(tool))
	if regularFile(p) {
		return p
	}
	return ""
}

// javaSystemCandidates are java launchers the computer may have, best first:
// JAVA_HOME, the PATH, then the platform's usual places.
func javaSystemCandidates() []string {
	var out []string
	// Windows users often set JAVA_HOME with quotes around it, or to its bin
	// folder.
	if home := strings.Trim(strings.TrimSpace(os.Getenv("JAVA_HOME")), `"`); home != "" {
		out = append(out, filepath.Join(home, "bin", javaExeName()))
		if strings.EqualFold(filepath.Base(filepath.Clean(home)), "bin") {
			out = append(out, filepath.Join(home, javaExeName()))
		}
	}
	// On Windows LookPath also finds a java.bat or java.cmd wrapper, whose
	// folder is not a Java's bin folder.
	if p, err := exec.LookPath("java"); err == nil && filepath.IsAbs(p) && strings.EqualFold(filepath.Base(p), javaExeName()) {
		out = append(out, p)
	}
	return append(out, javaPlatformCandidates()...)
}

// javaGlobJavas lists <pattern>/bin/java for every folder pattern matches,
// newest-looking names first (jdk-21 before jdk-17).
func javaGlobJavas(pattern string) []string {
	dirs, _ := filepath.Glob(pattern)
	sort.Slice(dirs, func(i, j int) bool { return javaNameNewer(filepath.Base(dirs[i]), filepath.Base(dirs[j])) })
	var out []string
	for _, d := range dirs {
		if p := filepath.Join(d, "bin", javaExeName()); regularFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// javaNameDigits finds the first number in a folder name ("jdk-21.0.5" → 21,
// "java-1.8.0-openjdk" → 8, "zulu17" → 17).
var javaNameDigits = regexp.MustCompile(`\d+(\.\d+)?`)

// javaNameNewer orders Java folder names by the feature version in them.
func javaNameNewer(a, b string) bool {
	na, nb := javaNameMajor(a), javaNameMajor(b)
	if na != nb {
		return na > nb
	}
	return a > b
}

func javaNameMajor(name string) int {
	m := javaNameDigits.FindString(name)
	if m == "" {
		return 0
	}
	if rest, ok := strings.CutPrefix(m, "1."); ok {
		n, _ := strconv.Atoi(rest)
		return n
	}
	whole, _, _ := strings.Cut(m, ".")
	n, _ := strconv.Atoi(whole)
	return n
}

// javaProbeCached is `java -version` for java, remembered by size and time.
func javaProbeCached(java string) (int, string, error) {
	info, err := os.Stat(java)
	if err != nil {
		return 0, "", err
	}
	javaState.mu.Lock()
	if p, ok := javaState.probes[java]; ok && p.size == info.Size() && p.mod.Equal(info.ModTime()) &&
		(p.until.IsZero() || time.Now().Before(p.until)) {
		javaState.mu.Unlock()
		if p.err != "" {
			return 0, "", errors.New(p.err)
		}
		return p.major, p.version, nil
	}
	javaState.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), javaProbeTimeout)
	defer cancel()
	major, version, perr := javaProbeVersion(ctx, java, nil)
	res := javaProbeResult{size: info.Size(), mod: info.ModTime(), major: major, version: version}
	if perr != nil {
		res.err = perr.Error()
		if ctx.Err() != nil {
			// Too slow this once (a cold disk, a virus scanner's first
			// look): asked again in a while instead of ruled out for good.
			res.until = time.Now().Add(10 * time.Minute)
		}
	}
	javaState.mu.Lock()
	if javaState.probes == nil {
		javaState.probes = map[string]javaProbeResult{}
	}
	javaState.probes[java] = res
	javaState.mu.Unlock()
	return major, version, perr
}

// javaCapBuffer keeps the first max bytes written to it.
type javaCapBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *javaCapBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// javaProbeVersion runs `java -version` and reads the version it prints (to
// stderr). env nil means JukaHub's own environment. A Java that cannot start
// here (a missing library, a glibc too old) fails with its first line.
func javaProbeVersion(ctx context.Context, java string, env []string) (int, string, error) {
	cmd := hiddenCommandContext(ctx, java, "-version")
	if env != nil {
		cmd.Env = env
	}
	cmd.Dir = filepath.Dir(java)
	out := &javaCapBuffer{max: javaProbeMaxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	text := out.buf.String()
	major, version, ok := javaParseVersion(text)
	if err != nil || !ok {
		msg := strings.TrimSpace(text)
		// JAVA_TOOL_OPTIONS and _JAVA_OPTIONS add a "Picked up …" line
		// before the real complaint.
		for strings.HasPrefix(msg, "Picked up ") {
			_, rest, _ := strings.Cut(msg, "\n")
			msg = strings.TrimSpace(rest)
		}
		if ctx.Err() != nil {
			msg = "it did not answer within " + termDurationText(javaProbeTimeout)
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "it printed no version"
		}
		return 0, "", errors.New(firstLine(msg))
	}
	return major, version, nil
}

var (
	javaVersionQuoted = regexp.MustCompile(`version "([^"]+)"`)
	javaVersionBuild  = regexp.MustCompile(`\(build ([^)\s,]+)`)
)

// javaParseVersion reads `java -version` output: `openjdk version "25.0.1"
// 2025-10-21 LTS` and its "(build 25.0.1+8-LTS)" give 25 and "25.0.1+8"; Java
// 8's "1.8.0_472" gives 8. Pure.
func javaParseVersion(out string) (int, string, bool) {
	m := javaVersionQuoted.FindStringSubmatch(out)
	if m == nil {
		return 0, "", false
	}
	quoted := m[1]
	major := javaVersionMajor(quoted)
	if major <= 0 {
		return 0, "", false
	}
	version := quoted
	if b := javaVersionBuild.FindStringSubmatch(out); b != nil {
		build := strings.TrimSuffix(b[1], "-LTS")
		if strings.HasPrefix(build, quoted) {
			version = build
		}
	}
	return major, version, true
}

// javaVersionMajor is the feature version of a version string: "25.0.1" → 25,
// "1.8.0_472" → 8, "21-ea" → 21.
func javaVersionMajor(v string) int {
	v = strings.TrimSpace(v)
	if rest, ok := strings.CutPrefix(v, "1."); ok {
		v = rest
	}
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return 0
	}
	return n
}

// javaShellEnv is env with JAVA_HOME set to the Java JukaHub uses and its bin
// folder first on PATH, for the Terminal and Java programs. env is returned
// unchanged when there is no Java; the slice passed in is never modified.
func javaShellEnv(env []string) []string {
	rt, ok := javaFind()
	if !ok {
		return env
	}
	return javaEnvFor(rt, env)
}

// javaEnvFor is javaShellEnv for a given Java (the JDK a javac run picked).
func javaEnvFor(rt javaRuntime, env []string) []string {
	if rt.Java == "" {
		return env
	}
	bin := filepath.Dir(rt.Java)
	sep := string(os.PathListSeparator)
	same := func(key, want string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(key, want)
		}
		return key == want
	}
	out := make([]string, 0, len(env)+2)
	sawPath := false
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		switch {
		case same(key, "JAVA_HOME"):
			continue
		case same(key, "PATH"):
			sawPath = true
			if val == "" {
				kv = key + "=" + bin
			} else {
				kv = key + "=" + bin + sep + val
			}
		}
		out = append(out, kv)
	}
	if !sawPath {
		def := "/usr/sbin:/usr/bin:/sbin:/bin"
		if runtime.GOOS == "windows" {
			def = os.Getenv("PATH")
		}
		out = append(out, "PATH="+bin+sep+def)
	}
	return append(out, "JAVA_HOME="+rt.Home)
}

// javaEnsure runs then with a Java that is at least minMajor (and a JDK when
// needJDK). Main thread. With one at hand, then runs at once. Otherwise the
// Tools → Java screen opens with an offer that says why (the why text, e.g.
// "J2ME games run on Java"); Install downloads it with the install card up,
// and then runs on the main thread when it is ready — unless the install was
// cancelled or failed, or a game took the screen meanwhile. While the cache
// is stale or a system Java is still being probed, it waits for the scan.
func javaEnsure(config *Config, why string, minMajor int, needJDK bool, then func(rt javaRuntime)) {
	if then == nil {
		return
	}
	if minMajor < 8 {
		minMajor = 8
	}
	javaWhenScanned(func() { javaEnsureNow(config, why, minMajor, needJDK, then) })
}

// javaEnsureNow is javaEnsure once the cache is fresh. Main thread.
func javaEnsureNow(config *Config, why string, minMajor int, needJDK bool, then func(rt javaRuntime)) {
	if rt, ok := javaFindFor(minMajor, needJDK); ok {
		javaRunWhenReady(rt, then)
		return
	}
	if minMajor > javaNewestMajor() {
		ShowToast("This needs Java "+strconv.Itoa(minMajor)+" — JukaHub installs Java up to "+strconv.Itoa(javaNewestMajor()), ToastKindWarning)
		return
	}
	major := javaDefaultMajor
	if minMajor > major {
		major = javaNewestMajor()
	}
	kind := javaPrefKind()
	if needJDK {
		kind = javaKindJDK
	}
	// The install that is running already brings what is asked for: wait.
	if javaInstallJoin(minMajor, needJDK, then) {
		ShowToast("Java is being installed — it starts when that is done", ToastKindInfo)
		return
	}
	javaOfferInstall(config, why, major, kind, minMajor, needJDK, then)
}

// javaRunWhenReady is the common end of an install: then runs on the main
// thread unless a game has the screen.
func javaRunWhenReady(rt javaRuntime, then func(rt javaRuntime)) {
	if then == nil {
		return
	}
	if gameScreenSuspended() {
		ShowToast("Java is ready — start it again when you like", ToastKindInfo)
		return
	}
	then(rt)
}
