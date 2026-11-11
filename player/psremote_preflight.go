package main

// psremote_preflight.go — will the stream helper start on this firmware?
//
// Starting a stream quits JukaHub: the hand-off script waits for it to exit
// and only then runs chiaki-stream on the freed screen. A helper the loader
// refuses (linked against a newer glibc than the handheld's, built for another
// processor, needing a library the firmware does not have) used to fail only
// at that point: a black screen, one line in psremote.log, and back to Home
// with nothing said. That is a real case, not a hypothetical one — a helper
// built with the TG5050 (Smart Pro S) SDK can need a newer glibc than the
// original Smart Pro's 2.33.
//
// So the helper is checked before the hand-off, while JukaHub can still say
// what is wrong:
//
//   - elfcompat.go reads the binary's own needs (its architecture, the symbol
//     versions it wants from libc and the other system libraries) against the
//     folders the hand-off puts on LD_LIBRARY_PATH, and words a glibc gap the
//     way a person can act on ("needs GLIBC_2.34; this firmware's libc.so.6
//     has up to GLIBC_2.33");
//   - the helper is then started once with no arguments, in the hand-off's
//     environment plus LD_BIND_NOW=1, so every function it links is looked up
//     now rather than when the stream first calls it. It prints its usage and
//     exits 2 before it touches the screen or the network, so that exit proves
//     the loader accepted it; whatever the loader prints instead ("version
//     `GLIBC_2.34' not found", "cannot open shared object file", "undefined
//     symbol") is the reason the stream would have failed.
//
// The probe outranks the static reading: a binary the real loader starts is
// fine whatever the ELF tables suggested, and a check that cannot tell (not
// Linux, a probe that timed out, was killed from outside or could not be
// started for another reason) never blocks a stream on a guess. Only answers
// that settle the question are kept, per helper file (path, size, modification
// time), for the life of the process; a "cannot tell" is asked again next time.
// The panel warms the answer up off the main thread as it opens
// (psWarmStreamPreflight), so Play does not wait for the probe.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// psHelperProbeTimeout bounds the one start of the helper. Loading it and
	// its libraries from the card takes well under a second; the bound only
	// matters for a helper that hangs, which then counts as "cannot tell".
	psHelperProbeTimeout = 2 * time.Second
	// psHelperUsageExit is what both helpers return when started without
	// their file arguments (native/chiaki-stream.c, native/chiaki-regist.c).
	psHelperUsageExit = 2
	// psHelperProbeOutputMax bounds what is kept of the probe's output.
	psHelperProbeOutputMax = 4096
	// psStreamHelperLead opens the reason psRemoteStream reports. The panel
	// shows it after "Could not start Remote Play — " through firstLine, which
	// keeps 96 characters: the lead is short so the reason itself fits.
	psStreamHelperLead = "the helper won't run here: "
)

// psHelperProbeResult is what one start of a helper without arguments showed.
type psHelperProbeResult struct {
	// ran is true when the process ran to an exit status (code), including a
	// death by signal (code -1, signal names it).
	ran    bool
	code   int
	signal string
	// output is what it printed, trimmed and bounded.
	output string
	// err is a start that failed (exec format error, permission) or a probe
	// that timed out; ran is false then.
	err error
	// loaderMissing is a start refused with "no such file" for a file that is
	// there: what is missing is the program loader it names (a helper built
	// for another system).
	loaderMissing bool
}

// Test seams: the ELF reading and the probe.
var (
	psStreamHelperCheck = nativeLoadProblemWith
	psStreamHelperProbe = psProbeHelperExec
)

// psPreflight serialises the checks, so a second caller (the panel's warm-up
// and a Play press, say) waits for the first answer instead of starting the
// helper again, and remembers the settled answers per helper file.
var psPreflight struct {
	run     sync.Mutex
	mu      sync.Mutex
	answers map[string]string
}

// psStreamHelperProblem says why chiaki-stream cannot start on this device
// ("" when it can, or when that cannot be told). psRemoteStream asks it before
// JukaHub quits for the hand-off.
func psStreamHelperProblem(engine psEngine) string {
	path := strings.TrimSpace(engine.Stream)
	if path == "" {
		return ""
	}
	problem := psNativeHelperProblem(path, psStreamHelperLibraryPath(engine))
	if problem == "" {
		return ""
	}
	return psStreamHelperLead + problem
}

// psWarmStreamPreflight asks psStreamHelperProblem ahead of a Play press. The
// panel runs it in a goroutine as it opens; a Play during the warm-up waits
// for the same answer instead of starting the helper a second time.
func psWarmStreamPreflight() {
	if !psRemoteSupported() {
		return
	}
	engine := psRemoteEngine()
	if !engine.installed() {
		return
	}
	// The probe cannot start a helper without its execute bits, and that
	// "cannot tell" would only be asked again at Play.
	psEnsureExecutable(engine.Stream)
	psStreamHelperProblem(engine)
}

// psNativeHelperProblem is the check for one helper binary started with
// ldPath as its LD_LIBRARY_PATH. The message is a clause, without a subject.
func psNativeHelperProblem(path, ldPath string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		// A missing helper is reported by the engine lookup, not here.
		return ""
	}
	key := path + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00" + ldPath

	psPreflight.run.Lock()
	defer psPreflight.run.Unlock()
	psPreflight.mu.Lock()
	answer, known := psPreflight.answers[key]
	psPreflight.mu.Unlock()
	if known {
		return answer
	}

	static := strings.TrimSpace(psStreamHelperCheck(path, psSplitLibraryPath(ldPath)))
	probe := psStreamHelperProbe(path, psHelperProbeEnv(os.Environ(), ldPath))
	answer = psPreflightVerdict(static, probe)
	if answer != "" {
		logWarn("psremote", "%s cannot start: %s (probe: ran=%t code=%d %s %v)",
			filepath.Base(path), answer, probe.ran, probe.code, firstLine(probe.output), probe.err)
	} else if static != "" {
		logInfo("psremote", "%s: the ELF check said %q, but the loader starts it", filepath.Base(path), static)
	}

	if psPreflightSettled(probe) {
		psPreflight.mu.Lock()
		if psPreflight.answers == nil || len(psPreflight.answers) >= 16 {
			psPreflight.answers = make(map[string]string, 4)
		}
		psPreflight.answers[key] = answer
		psPreflight.mu.Unlock()
	}
	return answer
}

// psPreflightSettled reports whether a probe's answer is worth keeping: the
// helper ran to a verdict of its own, or the loader refused the file outright.
// A timeout, a permission the hand-off fixes, or a kill from outside (the
// memory killer) says nothing about the file and is asked again next time.
func psPreflightSettled(probe psHelperProbeResult) bool {
	switch {
	case probe.ran:
		return !psProbeKilled(probe)
	case probe.loaderMissing:
		return true
	case probe.err != nil && strings.Contains(probe.err.Error(), "exec format error"):
		return true
	}
	return false
}

// psProbeKilled reports whether the probe died of SIGKILL, which comes from
// outside (the kernel's memory killer, a stop), never from the helper's code.
func psProbeKilled(probe psHelperProbeResult) bool {
	return probe.code < 0 && strings.HasSuffix(strings.TrimSpace(probe.signal), "killed")
}

// psPreflightVerdict combines the two answers (see the header): the probe
// decides whenever it ran, the ELF reading speaks for it otherwise, and the
// friendlier wording wins when both found the same failure.
func psPreflightVerdict(static string, probe psHelperProbeResult) string {
	switch {
	case probe.ran && probe.code == psHelperUsageExit:
		return ""
	case probe.ran && psProbeKilled(probe):
		// Killed from outside: the probe could not tell.
		return static
	case probe.ran && probe.code < 0:
		reason := "it crashed as it started"
		if probe.signal != "" {
			reason += " (" + probe.signal + ")"
		}
		if static != "" {
			return static
		}
		return reason
	case probe.ran:
		if loader := psLoaderReason(probe.output); loader != "" {
			if static != "" {
				return static
			}
			return loader
		}
		// It ran its own code and answered differently (an older helper):
		// whatever that means, the loader accepted it.
		return ""
	case probe.err != nil && strings.Contains(probe.err.Error(), "exec format error"):
		if static != "" {
			return static
		}
		return "it is not a program this device can run"
	case probe.loaderMissing:
		if static != "" {
			return static
		}
		return "its program loader is missing"
	default:
		// The probe could not tell: a timeout, a permission the hand-off fixes
		// itself (it sets the execute bits), or no probe at all.
		return static
	}
}

// The dynamic loader's complaints, as glibc's ld.so prints them. Its messages
// are fixed ASCII, so they are looked for in the line itself, never through a
// case-folded copy whose byte offsets can differ from the original's.
const (
	psLoaderSharedLibs = "error while loading shared libraries:"
	psLoaderSymbol     = "symbol lookup error:"
)

// psLoaderReason picks the dynamic loader's complaint out of a helper's
// output, without the program's path in front and with a library's folder
// dropped: "libSDL2-2.0.so.0: cannot open shared object file", "libc.so.6:
// version `GLIBC_2.34' not found", "undefined symbol: SDL_Foo". "" when the
// output holds no such line.
func psLoaderReason(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		var tail string
		switch {
		case strings.Contains(line, psLoaderSharedLibs):
			tail = line[strings.Index(line, psLoaderSharedLibs)+len(psLoaderSharedLibs):]
		case strings.Contains(line, psLoaderSymbol):
			tail = line[strings.Index(line, psLoaderSymbol)+len(psLoaderSymbol):]
		case strings.Contains(line, "version ") && strings.Contains(line, " not found"),
			strings.Contains(line, "cannot open shared object file"),
			strings.Contains(line, "undefined symbol"):
			// Newer glibc quotes the version 'like this', older `like this'.
			tail = line
		default:
			continue
		}
		tail = strings.TrimSpace(tail)
		// "/path/chiaki-stream: /lib/libc.so.6: version …" and "/path/chiaki-stream:
		// undefined symbol: …" name the program first; the library or the symbol
		// that follows is the news.
		for strings.HasPrefix(tail, "/") {
			i := strings.Index(tail, ": ")
			if i < 0 {
				break
			}
			next := tail[i+2:]
			if !strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "undefined symbol") {
				break
			}
			tail = strings.TrimSpace(next)
		}
		if i := strings.Index(tail, " (required by "); i >= 0 {
			tail = tail[:i]
		}
		tail = strings.TrimSuffix(tail, ": No such file or directory")
		// A library's folder says nothing a person can act on, and the toast
		// that shows this is short: "/usr/lib/libc.so.6: …" reads "libc.so.6: …".
		if strings.HasPrefix(tail, "/") {
			if i := strings.Index(tail, ": "); i > 0 {
				if slash := strings.LastIndex(tail[:i], "/"); slash >= 0 {
					tail = tail[slash+1:]
				}
			}
		}
		if tail = strings.TrimSpace(tail); tail != "" {
			return tail
		}
	}
	return ""
}

// psStreamHelperLibraryPath is the LD_LIBRARY_PATH the hand-off script ends up
// giving chiaki-stream: psHandoffEnv's, with the Brick runtime appended
// (psHandoffScript).
func psStreamHelperLibraryPath(engine psEngine) string {
	ldPath := ""
	for _, kv := range psHandoffEnv(engine.Root) {
		if strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			ldPath = strings.TrimPrefix(kv, "LD_LIBRARY_PATH=")
		}
	}
	if rt := psEngineRuntimeDir(engine); rt != "" {
		if ldPath != "" {
			ldPath = strings.TrimSuffix(ldPath, ":") + ":" + rt
		} else {
			ldPath = rt
		}
	}
	return ldPath
}

// psSplitLibraryPath turns a library path into its folders, in order.
func psSplitLibraryPath(ldPath string) []string {
	var dirs []string
	for _, dir := range strings.Split(ldPath, ":") {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// psHelperProbeEnv is base with LD_LIBRARY_PATH replaced by ldPath (removed
// when ldPath is empty) and LD_BIND_NOW=1, which makes the loader resolve every
// linked function before main instead of when it is first called: a function
// the firmware's library lacks then fails the probe, not the stream. These are
// the only variables that change whether the binary loads; the hand-off's
// others (HOME, PATH, OPENSSL_CONF) do not.
func psHelperProbeEnv(base []string, ldPath string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if name == "LD_LIBRARY_PATH" || name == "LD_BIND_NOW" {
			continue
		}
		out = append(out, kv)
	}
	if ldPath != "" {
		out = append(out, "LD_LIBRARY_PATH="+ldPath)
	}
	return append(out, "LD_BIND_NOW=1")
}

// psProbeHelperExec starts the helper with no arguments and reports how it
// ended.
func psProbeHelperExec(path string, env []string) psHelperProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), psHelperProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = env
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	if len(out) > psHelperProbeOutputMax {
		out = out[:psHelperProbeOutputMax]
	}
	res := psHelperProbeResult{output: strings.TrimSpace(string(out))}
	if ctx.Err() != nil {
		res.err = ctx.Err()
		return res
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.ran = true
	case errors.As(err, &exitErr):
		res.ran = true
		res.code = exitErr.ExitCode()
		if res.code < 0 {
			res.signal = exitErr.Error()
		}
	case errors.Is(err, fs.ErrPermission):
		// The hand-off sets the execute bits itself; nothing to report.
		res.err = err
	case errors.Is(err, fs.ErrNotExist):
		res.err = err
		// The file is there (it was just found), so what the kernel could not
		// find is the program loader it names.
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			res.loaderMissing = true
		}
	default:
		res.err = err
	}
	return res
}
