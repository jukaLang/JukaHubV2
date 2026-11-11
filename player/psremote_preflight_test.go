package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPSPreflightVerdict(t *testing.T) {
	glibcGap := "needs GLIBC_2.34; this firmware's libc.so.6 has up to GLIBC_2.33"
	loaderGlibc := "/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: /lib/libc.so.6: version `GLIBC_2.34' not found (required by /mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream)"
	loaderMissing := "/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: error while loading shared libraries: libSDL2-2.0.so.0: cannot open shared object file: No such file or directory"
	cases := []struct {
		name   string
		static string
		probe  psHelperProbeResult
		want   string // "" = no problem; otherwise a substring of the answer
	}{
		{"usage exit is fine", "", psHelperProbeResult{ran: true, code: 2, output: "usage: chiaki-stream SESSION_FILE"}, ""},
		{"the loader outranks the ELF reading", glibcGap, psHelperProbeResult{ran: true, code: 2}, ""},
		{"loader glibc refusal", "", psHelperProbeResult{ran: true, code: 127, output: loaderGlibc}, "libc.so.6: version `GLIBC_2.34' not found"},
		{"friendlier static wording wins", glibcGap, psHelperProbeResult{ran: true, code: 127, output: loaderGlibc}, glibcGap},
		{"missing library", "", psHelperProbeResult{ran: true, code: 127, output: loaderMissing}, "libSDL2-2.0.so.0: cannot open shared object file"},
		{"crash at start", "", psHelperProbeResult{ran: true, code: -1, signal: "signal: illegal instruction"}, "crashed as it started (signal: illegal instruction)"},
		{"a kill from outside cannot tell", "", psHelperProbeResult{ran: true, code: -1, signal: "signal: killed"}, ""},
		{"a kill keeps the ELF finding", glibcGap, psHelperProbeResult{ran: true, code: -1, signal: "signal: killed"}, glibcGap},
		{"another exit from its own code", "", psHelperProbeResult{ran: true, code: 3, output: "invalid or incomplete session file"}, ""},
		{"wrong processor", "", psHelperProbeResult{err: errors.New("fork/exec /x/chiaki-stream: exec format error")}, "not a program this device can run"},
		{"missing program loader", "", psHelperProbeResult{err: os.ErrNotExist, loaderMissing: true}, "its program loader is missing"},
		{"timeout cannot tell", "", psHelperProbeResult{err: context.DeadlineExceeded}, ""},
		{"timeout keeps the ELF finding", glibcGap, psHelperProbeResult{err: context.DeadlineExceeded}, glibcGap},
		{"no probe, no finding", "", psHelperProbeResult{}, ""},
	}
	for _, c := range cases {
		got := psPreflightVerdict(c.static, c.probe)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: got %q, want no problem", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// Only answers that settle the question are kept: a probe that could not tell
// is asked again.
func TestPSPreflightSettled(t *testing.T) {
	cases := []struct {
		name  string
		probe psHelperProbeResult
		want  bool
	}{
		{"usage exit", psHelperProbeResult{ran: true, code: 2}, true},
		{"loader refusal", psHelperProbeResult{ran: true, code: 127}, true},
		{"crash", psHelperProbeResult{ran: true, code: -1, signal: "signal: segmentation fault"}, true},
		{"killed from outside", psHelperProbeResult{ran: true, code: -1, signal: "signal: killed"}, false},
		{"exec format error", psHelperProbeResult{err: errors.New("fork/exec x: exec format error")}, true},
		{"missing loader", psHelperProbeResult{err: os.ErrNotExist, loaderMissing: true}, true},
		{"timeout", psHelperProbeResult{err: context.DeadlineExceeded}, false},
		{"permission", psHelperProbeResult{err: os.ErrPermission}, false},
	}
	for _, c := range cases {
		if got := psPreflightSettled(c.probe); got != c.want {
			t.Errorf("%s: settled = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPSLoaderReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"usage: chiaki-stream SESSION_FILE", ""},
		{"/bin/chiaki-stream: error while loading shared libraries: libavcodec.so.60: cannot open shared object file: No such file or directory",
			"libavcodec.so.60: cannot open shared object file"},
		{"/bin/chiaki-stream: /lib/libc.so.6: version `GLIBC_2.38' not found (required by /bin/chiaki-stream)",
			"libc.so.6: version `GLIBC_2.38' not found"},
		{"[native] starting\n/bin/chiaki-stream: /usr/lib/aarch64-linux-gnu/libstdc++.so.6: version `GLIBCXX_3.4.30' not found (required by /bin/chiaki-stream)",
			"libstdc++.so.6: version `GLIBCXX_3.4.30' not found"},
		// Newer glibc quotes the version with plain apostrophes.
		{"/bin/chiaki-stream: /lib/libc.so.6: version 'GLIBC_2.38' not found (required by /bin/chiaki-stream)",
			"libc.so.6: version 'GLIBC_2.38' not found"},
		// Under LD_BIND_NOW a function the library lacks fails before main.
		{"/bin/chiaki-stream: symbol lookup error: /bin/chiaki-stream: undefined symbol: SDL_GameControllerGetSensorData",
			"undefined symbol: SDL_GameControllerGetSensorData"},
		// Letters that change length when case-folded do not shift anything.
		{"İİİ /bin/x: error while loading shared libraries: libfoo.so.1: cannot open shared object file: No such file or directory",
			"libfoo.so.1: cannot open shared object file"},
	}
	for _, c := range cases {
		if got := psLoaderReason(c.in); got != c.want {
			t.Errorf("psLoaderReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPSHelperProbeEnvSetsTheLoaderVariables(t *testing.T) {
	base := []string{"HOME=/root", "LD_LIBRARY_PATH=/old", "PATH=/bin", "LD_LIBRARY_PATH=/older", "LD_BIND_NOW="}
	env := psHelperProbeEnv(base, "/a:/b")
	libs, binds := 0, 0
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "LD_LIBRARY_PATH="):
			libs++
			if kv != "LD_LIBRARY_PATH=/a:/b" {
				t.Errorf("library path = %q", kv)
			}
		case strings.HasPrefix(kv, "LD_BIND_NOW="):
			binds++
			if kv != "LD_BIND_NOW=1" {
				t.Errorf("bind = %q", kv)
			}
		}
	}
	if libs != 1 || binds != 1 || len(env) != 4 {
		t.Errorf("env = %v, want HOME, PATH, one library path and LD_BIND_NOW=1", env)
	}
	if got := psHelperProbeEnv(base, ""); len(got) != 3 {
		t.Errorf("an empty library path is removed, got %v", got)
	}
	if dirs := psSplitLibraryPath(" /a::/b: "); len(dirs) != 2 || dirs[0] != "/a" || dirs[1] != "/b" {
		t.Errorf("psSplitLibraryPath = %v", dirs)
	}
}

// psPreflightTestHelper is a stand-in helper file and the seams that answer
// for it; probes counts the starts.
func psPreflightTestHelper(t *testing.T, probe psHelperProbeResult) (psEngine, *int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the preflight only runs on Linux")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "chiaki-stream")
	if err := os.WriteFile(helper, []byte("not really a binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldCheck, oldProbe := psStreamHelperCheck, psStreamHelperProbe
	t.Cleanup(func() { psStreamHelperCheck, psStreamHelperProbe = oldCheck, oldProbe })
	probes := 0
	psStreamHelperCheck = func(string, []string) string { return "" }
	psStreamHelperProbe = func(path string, env []string) psHelperProbeResult {
		probes++
		if path != helper {
			t.Errorf("probed %q, want %q", path, helper)
		}
		return probe
	}
	return psEngine{Stream: helper, Root: dir}, &probes
}

// TestPSStreamHelperProblemCachesAndWords drives the check through its seams:
// one probe per helper file, and the panel's wording around the reason.
func TestPSStreamHelperProblemCachesAndWords(t *testing.T) {
	engine, probes := psPreflightTestHelper(t, psHelperProbeResult{ran: true, code: 127,
		output: "/x/chiaki-stream: /lib/libc.so.6: version `GLIBC_2.34' not found (required by /x/chiaki-stream)"})
	first := psStreamHelperProblem(engine)
	second := psStreamHelperProblem(engine)
	if !strings.HasPrefix(first, psStreamHelperLead) || !strings.Contains(first, "GLIBC_2.34") {
		t.Errorf("problem = %q", first)
	}
	if second != first || *probes != 1 {
		t.Errorf("second answer %q after %d probes, want the cached one after 1", second, *probes)
	}
	if got := psStreamHelperProblem(psEngine{}); got != "" {
		t.Errorf("no helper: %q", got)
	}
}

// A probe that could not tell (here: it timed out) is not kept, so the next
// Play asks again instead of trusting a guess for the rest of the run.
func TestPSStreamHelperProblemAsksAgainAfterATimeout(t *testing.T) {
	engine, probes := psPreflightTestHelper(t, psHelperProbeResult{err: context.DeadlineExceeded})
	if got := psStreamHelperProblem(engine); got != "" {
		t.Errorf("a timed-out probe blocked the stream: %q", got)
	}
	psStreamHelperProblem(engine)
	if *probes != 2 {
		t.Errorf("%d probes, want 2: a timeout must not be cached", *probes)
	}
}

// The panel shows the reason through firstLine (96 characters) after its own
// lead: the usual reasons fit whole.
func TestPSStreamHelperProblemFitsTheToast(t *testing.T) {
	for _, problem := range []string{
		"needs GLIBC_2.38; this firmware's libc.so.6 has up to GLIBC_2.33",
		psLoaderReason("/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: /usr/lib/aarch64-linux-gnu/libstdc++.so.6: version `GLIBCXX_3.4.30' not found (required by /mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream)"),
		psLoaderReason("/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: error while loading shared libraries: libSDL2-2.0.so.0: cannot open shared object file: No such file or directory"),
		psLoaderReason("/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: symbol lookup error: /mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream: undefined symbol: SDL_GameControllerGetSensorData"),
		"it is not a program this device can run",
		"its program loader is missing",
	} {
		msg := psStreamHelperLead + problem
		if n := utf8.RuneCountInString(msg); n > 96 || firstLine(msg) != msg {
			t.Errorf("%d characters, the toast cuts it: %q", n, msg)
		}
	}
}

// TestPSProbeHelperExecReadsTheExit runs the real probe on small scripts: one
// that answers like a helper started without arguments, one that fails the
// way the loader does, and one whose interpreter does not exist.
func TestPSProbeHelperExecReadsTheExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /bin/sh")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ok := psProbeHelperExec(write("usage", "#!/bin/sh\n"+`echo "usage: chiaki-stream SESSION_FILE" >&2; exit 2`), os.Environ())
	if !ok.ran || ok.code != psHelperUsageExit || psPreflightVerdict("", ok) != "" {
		t.Errorf("usage helper: %+v", ok)
	}
	bad := psProbeHelperExec(write("loader", "#!/bin/sh\n"+`echo "$0: error while loading shared libraries: libfoo.so.1: cannot open shared object file: No such file or directory" >&2; exit 127`), os.Environ())
	if verdict := psPreflightVerdict("", bad); verdict != "libfoo.so.1: cannot open shared object file" {
		t.Errorf("loader failure: verdict %q from %+v", verdict, bad)
	}
	gone := psProbeHelperExec(write("noloader", "#!/nonexistent/jukahub-test-loader"), os.Environ())
	if !gone.loaderMissing || psPreflightVerdict("", gone) != "its program loader is missing" {
		t.Errorf("missing loader: %+v", gone)
	}
}
