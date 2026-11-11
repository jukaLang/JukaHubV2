package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPSParseLastRun(t *testing.T) {
	run, ok := psParseLastRun([]byte("version=1\nrc=11\nresult=connect_failed\nmessage=Could not reach the PS5.\n" +
		"connected=0\nseconds=0\nframes=0\nquit_reason=Connection refused\nhost=192.168.1.40\ncodec=h264\nfuture=key\n"))
	if !ok {
		t.Fatal("a full report did not parse")
	}
	if run.RC != 11 || run.Result != psLastRunConnectFailed || run.QuitReason != "Connection refused" ||
		run.Host != "192.168.1.40" || run.Codec != "h264" || run.Connected {
		t.Errorf("parsed %+v", run)
	}
	if _, ok := psParseLastRun([]byte("version=1\nmessage=hello\n")); ok {
		t.Error("a report with neither a result nor an exit code is not one")
	}
	if run, ok := psParseLastRun([]byte("rc=139\n")); !ok || run.Result != psLastRunCrashed {
		t.Errorf("a signal's exit code alone = %+v ok=%v, want crashed", run, ok)
	}
	for rc, want := range map[int]string{3: psLastRunInvalidSession, 5: psLastRunSDLFailed, 6: psLastRunDecoderFailed,
		8: psLastRunInitFailed, 10: psLastRunVideoFailed, 11: psLastRunSessionError, 12: psLastRunPINCancelled} {
		if got := psLastRunResultForRC(rc); got != want {
			t.Errorf("exit %d reads as %q, want %q", rc, got, want)
		}
	}
	run, ok = psParseLastRun([]byte("rc=0\r\n"))
	if !ok || run.Result != psLastRunOK {
		t.Errorf("exit 0 alone = %+v ok=%v, want ok", run, ok)
	}
	if run.PINRejected || run.LogOffset != -1 {
		t.Errorf("a report without the keys = %+v, want no rejected passcode and no log offset", run)
	}
	run, ok = psParseLastRun([]byte("result=ok\nsaved_pin_rejected=1\nlog_offset=4096\ncodec=H265\n"))
	if !ok || !run.PINRejected || run.LogOffset != 4096 || run.Codec != psCodecH265 {
		t.Errorf("saved_pin_rejected / log_offset / codec = %+v", run)
	}
	if run, _ := psParseLastRun([]byte("result=ok\nsaved_pin_rejected=0\nlog_offset=-5\n")); run.PINRejected || run.LogOffset != -1 {
		t.Errorf("saved_pin_rejected=0 / a negative offset = %+v", run)
	}
}

// A refused saved passcode is reported whatever the stream's result, and it
// points at the console's options rather than at Wake.
func TestPSLastRunToastSavedPINRejected(t *testing.T) {
	for _, result := range []string{psLastRunOK, psLastRunPINCancelled, psLastRunCancelled} {
		msg, kind, show := psLastRunToast(psLastRun{Result: result, PINRejected: true}, "Den PS5")
		if !show || kind != ToastKindWarning {
			t.Errorf("%s with a refused passcode: show=%v kind=%v", result, show, kind)
		}
		for _, want := range []string{"Den PS5", "was refused", "options", "on its row"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s toast %q lacks %q", result, msg, want)
			}
		}
		if strings.Contains(msg, "(X ") {
			t.Errorf("the toast names X, which is Wake: %q", msg)
		}
	}
	if msg, _, _ := psLastRunToast(psLastRun{Result: psLastRunPINCancelled}, "Den PS5"); strings.Contains(msg, "(X ") {
		t.Errorf("the passcode toast names X, which is Wake: %q", msg)
	}
}

// The video advice names the codec the stream did not use; a PS4 only has
// H.264, so it is told to lower the quality.
func TestPSLastRunVideoAdvice(t *testing.T) {
	cases := []struct {
		run       psLastRun
		want, not string
	}{
		{psLastRun{Result: psLastRunDecoderFailed, Codec: psCodecH265, PS5: true}, "Video codec H.264", "H.265"},
		{psLastRun{Result: psLastRunDecoderFailed, Codec: psCodecH264, PS5: true}, "Video codec H.265", "codec H.264"},
		{psLastRun{Result: psLastRunVideoFailed, Codec: psCodecH264}, "lower Quality", "H.265"},
		{psLastRun{Result: psLastRunVideoFailed}, "Video codec H.264", "H.265"},
	}
	for _, c := range cases {
		msg, _, show := psLastRunToast(c.run, "Den PS5")
		if !show || !strings.Contains(msg, c.want) || strings.Contains(msg, c.not) {
			t.Errorf("%+v -> %q, want %q and not %q", c.run, msg, c.want, c.not)
		}
	}
}

// Every result the helper writes says something useful, and a stream the user
// ended says nothing at all.
func TestPSLastRunToast(t *testing.T) {
	quiet := []psLastRun{{Result: psLastRunOK}, {Result: psLastRunCancelled}, {Result: psLastRunStarted}}
	for _, run := range quiet {
		if msg, _, show := psLastRunToast(run, "Den PS5"); show {
			t.Errorf("%s raised %q", run.Result, msg)
		}
	}
	cases := []struct {
		run   psLastRun
		kind  ToastKind
		wants []string
	}{
		{psLastRun{Result: psLastRunConnectFailed, QuitReason: "Connection refused"}, ToastKindWarning, []string{"Den PS5 (Connection refused). Check", "same Wi-Fi."}},
		{psLastRun{Result: psLastRunConnectFailed, Remote: true, QuitReason: "Timeout"}, ToastKindWarning, []string{"internet address (Timeout). Check", "9295"}},
		{psLastRun{Result: psLastRunPINCancelled}, ToastKindInfo, []string{"login passcode", "options"}},
		{psLastRun{Result: psLastRunConsoleQuit, RC: 11, QuitReason: "Remote Play on another device"}, ToastKindWarning, []string{"ended the stream (Remote Play on another device)."}},
		{psLastRun{Result: psLastRunConsoleQuit}, ToastKindInfo, []string{"Den PS5 ended the stream."}},
		{psLastRun{Result: psLastRunDecoderFailed}, ToastKindError, []string{"H.264", "Quality"}},
		{psLastRun{Result: psLastRunVideoFailed}, ToastKindError, []string{"video"}},
		{psLastRun{Result: psLastRunSDLFailed}, ToastKindError, []string{psLastRunLog}},
		{psLastRun{Result: psLastRunInvalidSession}, ToastKindError, []string{"Pair the console again"}},
		{psLastRun{Result: psLastRunInitFailed}, ToastKindError, []string{psLastRunLog}},
		{psLastRun{Result: psLastRunCrashed, RC: 139}, ToastKindError, []string{"code 139"}},
		{psLastRun{Result: psLastRunCrashed, RC: -1}, ToastKindError, []string{"stopped unexpectedly."}},
		{psLastRun{Result: psLastRunNotStarted}, ToastKindError, []string{"did not start"}},
		{psLastRun{Result: psLastRunSessionError, QuitReason: "Stream connection lost"}, ToastKindWarning, []string{"ended with an error (Stream connection lost)"}},
		{psLastRun{Result: "brand_new", Message: "Something new happened."}, ToastKindWarning, []string{"Something new happened."}},
		{psLastRun{Result: "brand_new", RC: 9}, ToastKindError, []string{"code 9"}},
	}
	for _, c := range cases {
		msg, kind, show := psLastRunToast(c.run, "Den PS5")
		if !show {
			t.Errorf("%+v raised no toast", c.run)
			continue
		}
		if kind != c.kind {
			t.Errorf("%s kind = %v, want %v", c.run.Result, kind, c.kind)
		}
		for _, want := range c.wants {
			if !strings.Contains(msg, want) {
				t.Errorf("%s toast %q lacks %q", c.run.Result, msg, want)
			}
		}
	}
	if msg, _, _ := psLastRunToast(psLastRun{Result: psLastRunConsoleQuit}, ""); !strings.HasPrefix(msg, "The console ended") {
		t.Errorf("unnamed console toast = %q", msg)
	}
	if msg, _, _ := psLastRunToast(psLastRun{Result: psLastRunConnectFailed, QuitReason: "none"}, "PS5"); strings.Contains(msg, "(none)") {
		t.Errorf("an empty quit reason was quoted: %q", msg)
	}
}

// psUseTempStreamLog points psremote.log at a file in dir for one test.
func psUseTempStreamLog(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "psremote.log")
	prev := psLastRunLogPath
	psLastRunLogPath = func() string { return path }
	t.Cleanup(func() { psLastRunLogPath = prev })
	return path
}

// A stream that never reported is judged by its own lines: from the offset the
// note recorded, through a long run's quality lines to its exit line — never
// by the stream before it.
func TestPSLastRunLogText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "psremote.log")
	earlier := "\n=== JukaHub Remote Play Mon ===\n[native] ok\n=== Den · PS5 exited with code 0 ===\n"
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(earlier))
	started := psLastRun{Result: psLastRunStarted, LogOffset: offset}

	// Nothing appended since: the hand-off never reached the helper, whatever
	// the stream before it did.
	if got := psLastRunFromLog(started, psLastRunLogText(path, offset)); got.Result != psLastRunNotStarted {
		t.Errorf("nothing since the offset = %+v, want not_started", got)
	}

	// A long stream (far more quality lines than the head and tail hold) that
	// crashed: the header is at the start, the exit line at the very end.
	var b strings.Builder
	b.WriteString(earlier)
	b.WriteString("\n=== JukaHub Remote Play Tue ===\n")
	for b.Len() < int(offset)+3*(psLastRunHeadMax+psLastRunTailMax) {
		b.WriteString("[native] quality: rendered=150 lost=0 fec=0 fps=30.0 suppressed=0 totals=1/0/0\n")
	}
	b.WriteString("=== Den · PS5 exited with code 139 ===\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	text := psLastRunLogText(path, offset)
	if n := len(text); n > psLastRunHeadMax+psLastRunTailMax+16 {
		t.Errorf("read %d bytes of the log, want at most the head and the tail", n)
	}
	if got := psLastRunFromLog(started, text); got.Result != psLastRunCrashed || got.RC != 139 {
		t.Errorf("a long stream that crashed = %+v, want crashed with code 139", got)
	}

	// Killed mid-stream: a header and no exit line.
	cut := strings.TrimSuffix(b.String(), "=== Den · PS5 exited with code 139 ===\n")
	if err := os.WriteFile(path, []byte(cut), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := psLastRunFromLog(started, psLastRunLogText(path, offset)); got.Result != psLastRunCrashed || got.RC != -1 {
		t.Errorf("a stream with no exit line = %+v, want crashed", got)
	}

	// An older note without an offset, and a log replaced since (shorter than
	// the offset), fall back to the log's end.
	short := earlier + "\n=== JukaHub Remote Play Tue ===\n[native] starting\n=== Den · PS5 exited with code 6 ===\n"
	if err := os.WriteFile(path, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := psLastRunFromLog(psLastRun{Result: psLastRunStarted, LogOffset: -1}, psLastRunLogText(path, -1)); got.Result != psLastRunDecoderFailed {
		t.Errorf("no offset = %+v, want the end of the log read (exit 6)", got)
	}
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := psLastRunFromLog(started, psLastRunLogText(path, offset*10)); got.Result != psLastRunOK {
		t.Errorf("a log shorter than the offset = %+v, want its end read", got)
	}
	if text := psLastRunLogText(filepath.Join(dir, "missing.log"), 0); text != "" {
		t.Errorf("a missing log read as %q", text)
	}
}

// A note still saying "started" is finished from the hand-off script's lines
// in psremote.log.
func TestPSLastRunFromLog(t *testing.T) {
	started := psLastRun{Result: psLastRunStarted}
	log := "=== JukaHub Remote Play Mon ===\n[native] ok\n=== PS5 · PS5 exited with code 0 ===\n" +
		"\n=== JukaHub Remote Play Tue ===\n[native] starting\n"
	if got := psLastRunFromLog(started, log); got.Result != psLastRunCrashed || got.RC != -1 {
		t.Errorf("a run with no exit line = %+v, want crashed", got)
	}
	if got := psLastRunFromLog(started, log+"=== PS5 · PS5 exited with code 139 ===\n"); got.Result != psLastRunCrashed || got.RC != 139 {
		t.Errorf("exit 139 = %+v", got)
	}
	if got := psLastRunFromLog(started, log+"=== PS5 · PS5 exited with code 12 ===\n"); got.Result != psLastRunPINCancelled {
		t.Errorf("an older helper's exit 12 = %+v, want the passcode advice", got)
	}
	if got := psLastRunFromLog(started, log+"=== PS5 · PS5 exited with code 0 ===\n"); got.Result != psLastRunOK {
		t.Errorf("exit 0 = %+v, want ok", got)
	}
	if got := psLastRunFromLog(started, ""); got.Result != psLastRunNotStarted {
		t.Errorf("no log = %+v, want not_started", got)
	}
}

// The boot step reads the report once, names the console, keeps the row and
// leaves nothing behind.
func TestPSRemoteShowLastRun(t *testing.T) {
	psOptsUseTempHosts(t)
	if err := savePSHosts(psHostsFile{Hosts: []psConsole{{Host: "192.168.1.40", Name: "Den", PS5: true,
		RegistKey: "0a1b2c3d", RPKey: "AAAAAAAAAAAAAAAAAAAAAA==", RemoteHost: "myhome.duckdns.org"}}}); err != nil {
		t.Fatal(err)
	}
	path := psLastRunPath()
	if err := os.WriteFile(path, []byte("version=1\nrc=11\nresult=connect_failed\nhost=myhome.duckdns.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	psRemoteShowLastRun()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the report is still there (err %v)", err)
	}
	currentToastMu.Lock()
	toast := currentToast.Message
	currentToastMu.Unlock()
	if !strings.Contains(toast, "Den") || !strings.Contains(toast, "internet address") {
		t.Errorf("toast = %q, want the console's name and the internet-address advice", toast)
	}
	rows := psLastRunRows()
	if len(rows) != 1 || rows[0].action != psActionLastRunOpen || psLastRunNoteHost() != "192.168.1.40" {
		t.Errorf("last-stream rows = %+v (host %q)", rows, psLastRunNoteHost())
	}
	// A second start has nothing to say.
	psRemoteShowLastRun()
	if rows := psLastRunRows(); len(rows) != 1 {
		t.Errorf("the row must stay until the next stream: %+v", rows)
	}
}

func TestPSMarkStreamStarted(t *testing.T) {
	dir := psOptsUseTempHosts(t)
	logPath := psUseTempStreamLog(t, dir)
	if err := os.WriteFile(logPath, []byte("=== JukaHub Remote Play Mon ===\n=== PS5 exited with code 0 ===\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	psLastRunNoteSet("old", ToastKindError, "h")
	path := psLastRunPath()
	psMarkStreamStarted(path, "192.168.1.40\nrc=0")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run, ok := psParseLastRun(data)
	if !ok || run.Result != psLastRunStarted || run.Host != "192.168.1.40 rc=0" || run.RC != 0 {
		t.Errorf("marker = %q -> %+v", data, run)
	}
	if info, err := os.Stat(logPath); err != nil || run.LogOffset != info.Size() {
		t.Errorf("marker log_offset = %d, want the log's length (stat err %v)", run.LogOffset, err)
	}
	if rows := psLastRunRows(); len(rows) != 0 {
		t.Errorf("a new stream clears the old row: %+v", rows)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 {
			t.Errorf("marker mode = %v, want 0600", info.Mode().Perm())
		}
	}
}
