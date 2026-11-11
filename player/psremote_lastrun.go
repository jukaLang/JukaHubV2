package main

// psremote_lastrun.go — what happened to the last stream.
//
// JukaHub is not running while a stream is: it hands the screen to
// chiaki-stream and comes back when the stream ends. A stream that never got
// going (console off, wrong Wi-Fi, a login passcode nobody typed, a decoder
// that could not keep up) used to end in silence — the user was simply back on
// Home and had to read psremote.log on a PC to learn why.
//
// Now JukaHub leaves a "started" note at psremote-last.txt before the hand-off
// and the helper overwrites it with its result (key=value lines, see
// native/README.md) as it exits. On the next start the note becomes one toast
// for anything that did not end well, and the panel keeps it as a "Last
// stream" row until the next stream. A note still saying "started" means the
// helper never wrote one — it crashed, was killed, or never ran — and the exit
// line the hand-off script appends to psremote.log says which.

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Result tokens. The helper writes the first ten; psLastRunStarted is
// JukaHub's own note, and the last three are what a note still saying
// "started" (or a bare exit code) turns into.
const (
	psLastRunOK             = "ok"
	psLastRunCancelled      = "cancelled"
	psLastRunPINCancelled   = "login_pin_cancelled"
	psLastRunConnectFailed  = "connect_failed"
	psLastRunConsoleQuit    = "console_quit"
	psLastRunDecoderFailed  = "decoder_failed"
	psLastRunVideoFailed    = "video_failed"
	psLastRunSDLFailed      = "sdl_failed"
	psLastRunInvalidSession = "invalid_session"
	psLastRunInitFailed     = "init_failed"
	psLastRunStarted        = "started"
	psLastRunCrashed        = "crashed"
	psLastRunNotStarted     = "not_started"
	psLastRunSessionError   = "session_error"
)

// psLastRunLog is where the details are, as a user reads the path.
const psLastRunLog = "Apps/JukaHub/psremote.log"

// A report the helper never wrote is finished from psremote.log: at most the
// first psLastRunHeadMax bytes the stream added (the hand-off script's header)
// and its last psLastRunTailMax bytes (the exit line). Between the two a long
// stream holds little but the helper's quality line, one every five seconds.
const (
	psLastRunHeadMax = 4 << 10
	psLastRunTailMax = 32 << 10
)

// psLastRunLogPath is psremote.log; a variable so tests use their own file.
var psLastRunLogPath = psLogPath

// psLastRun is one parsed report.
type psLastRun struct {
	RC         int
	Result     string
	Message    string
	Connected  bool
	Seconds    int
	Frames     int64
	QuitReason string
	Host       string
	Codec      string
	// PINRejected is the helper's saved_pin_rejected: the console refused
	// the login passcode saved for it (it was changed on the console).
	PINRejected bool
	// LogOffset is JukaHub's own note, not the helper's: how long
	// psremote.log was when the stream started, so a report the helper never
	// wrote is read from that stream's lines only. -1 when the note has none.
	LogOffset int64
	// Remote and PS5 are set by the reader, not the helper: the host was the
	// console's internet address; the console is a PS5.
	Remote bool
	PS5    bool
}

func init() {
	// The first frames after boot: the toast needs the render loop, and the
	// task queue (startup.go) is the one place that runs once it is up.
	deferStartupTask("psremote-last-run", psRemoteShowLastRun)
}

// psLastRunNote is the last report's message for the panel's "Last stream"
// row; host is the console it was about, for the row's A.
var psLastRunNote struct {
	mu   sync.Mutex
	text string
	kind ToastKind
	host string
}

// psMarkStreamStarted leaves the "started" note the helper replaces. Written
// plainly, without a backup copy: it is a one-line marker, not a store. It
// records how long psremote.log is now: the hand-off script appends this
// stream's lines after that point, so a stream that never reported is judged
// by its own lines and not by an earlier stream's.
func psMarkStreamStarted(path, host string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	data := "version=1\nresult=" + psLastRunStarted + "\nhost=" + psSessionValue(host) + "\n"
	if info, err := os.Stat(psLastRunLogPath()); err == nil {
		data += "log_offset=" + strconv.FormatInt(info.Size(), 10) + "\n"
	} else if os.IsNotExist(err) {
		data += "log_offset=0\n"
	}
	if err := writeFileSynced(path, []byte(data), 0o600); err != nil {
		logWarn("psremote", "could not leave the stream note: %v", err)
	}
	psLastRunNoteSet("", ToastKindInfo, "")
}

// psRemoteShowLastRun reads the report once, turns it into a toast when the
// stream did not end well, and removes it.
func psRemoteShowLastRun() {
	path := psLastRunPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	run, ok := psParseLastRun(data)
	if !ok {
		return
	}
	if run.Result == psLastRunStarted {
		run = psLastRunFromLog(run, psLastRunLogText(psLastRunLogPath(), run.LogOffset))
	}
	name, host := psLastRunConsole(&run)
	msg, kind, show := psLastRunToast(run, name)
	if !show {
		return
	}
	psLastRunNoteSet(msg, kind, host)
	ShowToast(msg, kind)
}

// psParseLastRun reads a report. Unknown keys are ignored; a report with
// neither a result nor an exit code is not one.
func psParseLastRun(data []byte) (psLastRun, bool) {
	run := psLastRun{LogOffset: -1}
	haveRC := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "rc":
			if n, err := strconv.Atoi(value); err == nil {
				run.RC, haveRC = n, true
			}
		case "result":
			run.Result = strings.ToLower(value)
		case "message":
			run.Message = value
		case "connected":
			run.Connected = value == "1" || strings.EqualFold(value, "true")
		case "seconds":
			run.Seconds, _ = strconv.Atoi(value)
		case "frames":
			run.Frames, _ = strconv.ParseInt(value, 10, 64)
		case "quit_reason":
			run.QuitReason = value
		case "host":
			run.Host = value
		case "codec":
			run.Codec = strings.ToLower(value)
		case "saved_pin_rejected":
			run.PINRejected = value == "1" || strings.EqualFold(value, "true")
		case "log_offset":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				run.LogOffset = n
			}
		}
	}
	if run.Result == "" && !haveRC {
		return psLastRun{}, false
	}
	if run.Result == "" {
		run.Result = psLastRunResultForRC(run.RC)
	}
	return run, true
}

// psLastRunResultForRC reads a bare exit code the way the helper's own codes
// are documented (native/README.md): an exit by a signal (128 and up) is a
// crash, the helper's error codes name what failed.
func psLastRunResultForRC(rc int) string {
	switch {
	case rc == 0:
		return psLastRunOK
	case rc == 3:
		return psLastRunInvalidSession
	case rc == 4 || rc == 7 || rc == 8:
		return psLastRunInitFailed
	case rc == 5:
		return psLastRunSDLFailed
	case rc == 6:
		return psLastRunDecoderFailed
	case rc == 10:
		return psLastRunVideoFailed
	case rc == 11:
		return psLastRunSessionError
	case rc == 12:
		return psLastRunPINCancelled
	}
	return psLastRunCrashed
}

// psLastRunLogText is the part of the helper log that can tell what became of
// the last stream: what the hand-off script appended since offset (the log's
// length when the stream started) — its first psLastRunHeadMax bytes and its
// last psLastRunTailMax, when it is longer than both. A note without an offset
// (an older JukaHub's), or a log shorter than its offset (replaced since), gets
// the log's end instead.
func psLastRunLogText(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if offset < 0 || offset > size {
		return psReadLogRange(f, size-psLastRunTailMax, size)
	}
	if size-offset <= psLastRunHeadMax+psLastRunTailMax {
		return psReadLogRange(f, offset, size)
	}
	return psReadLogRange(f, offset, offset+psLastRunHeadMax) + "\n…\n" + psReadLogRange(f, size-psLastRunTailMax, size)
}

// psReadLogRange reads bytes [from, to) of f; a start before the file's is
// its start.
func psReadLogRange(f *os.File, from, to int64) string {
	if from < 0 {
		from = 0
	}
	if to <= from {
		return ""
	}
	buf := make([]byte, to-from)
	n, _ := f.ReadAt(buf, from)
	return string(buf[:n])
}

// psLastRunFromLog finishes a report the helper never wrote. The hand-off
// script brackets each run in psremote.log with a header and an exit line
// ("=== … exited with code N ==="): the last run's exit code says whether it
// ended normally; a header with no exit line is a run that died; no header at
// all is a hand-off that never reached the helper.
func psLastRunFromLog(run psLastRun, tail string) psLastRun {
	header := strings.LastIndex(tail, "=== JukaHub Remote Play")
	if header < 0 {
		run.Result = psLastRunNotStarted
		run.RC = -1
		return run
	}
	last := tail[header:]
	idx := strings.LastIndex(last, "exited with code ")
	if idx < 0 {
		run.Result = psLastRunCrashed
		run.RC = -1
		return run
	}
	digits := last[idx+len("exited with code "):]
	end := 0
	for end < len(digits) && (digits[end] == '-' || (digits[end] >= '0' && digits[end] <= '9')) {
		end++
	}
	rc, err := strconv.Atoi(digits[:end])
	if err != nil {
		run.Result = psLastRunCrashed
		run.RC = -1
		return run
	}
	run.RC = rc
	run.Result = psLastRunResultForRC(rc)
	return run
}

// psLastRunConsole names the console a report is about and marks a stream
// that went to its internet address. host is the console's key in the list.
func psLastRunConsole(run *psLastRun) (name, host string) {
	if run == nil || strings.TrimSpace(run.Host) == "" {
		return "the console", ""
	}
	for _, c := range loadPSHosts().Hosts {
		switch {
		case strings.EqualFold(c.Host, run.Host):
		case c.RemoteHost != "" && strings.EqualFold(c.RemoteHost, run.Host):
			run.Remote = true
		default:
			continue
		}
		run.PS5 = psConsoleIsPS5(c)
		return psStreamName(c), c.Host
	}
	return "the console", ""
}

// psLastRunToast is the one sentence a report becomes, its kind, and whether
// it is worth a toast at all (a stream the user ended is not). Pure.
func psLastRunToast(run psLastRun, name string) (string, ToastKind, bool) {
	if strings.TrimSpace(name) == "" {
		name = "the console"
	}
	// A refused saved passcode outranks how the stream ended: it is what the
	// user has to change, and the stream may even have gone on after the
	// passcode was typed on screen. The sentence names the control through the
	// hint vocabulary, with the console's name kept out of the format.
	if run.PINRejected {
		return "The login passcode saved for " + name + " was refused — " +
			hintSentence("change or remove it in its options (%s on its row in PS Remote Play).", "Start / O", "Options"),
			ToastKindWarning, true
	}
	reason := psLastRunReason(run.QuitReason)
	switch run.Result {
	case psLastRunOK, psLastRunCancelled, psLastRunStarted:
		return "", ToastKindInfo, false
	case psLastRunConnectFailed:
		if run.Remote {
			return "Remote Play could not reach " + name + " at its internet address" + reason + ". Check the " +
				"router's port forwarding (TCP 9295, UDP 9296-9297) or the VPN.", ToastKindWarning, true
		}
		return "Remote Play could not reach " + name + reason + ". Check that it is on (or in rest mode), that " +
			"Remote Play is enabled on it, and that both are on the same Wi-Fi.", ToastKindWarning, true
	case psLastRunPINCancelled:
		return capitalizeFirst(name) + " asks for its 4-digit login passcode. " +
			hintSentence("Save it in its options (%s on its row in PS Remote Play) and JukaHub enters it for you.",
				"Start / O", "Options"), ToastKindInfo, true
	case psLastRunConsoleQuit:
		// A console that ended the stream without an error (rc 0) is news,
		// not a warning.
		kind := ToastKindWarning
		if run.RC == 0 {
			kind = ToastKindInfo
		}
		if reason == "" {
			return capitalizeFirst(name) + " ended the stream.", kind, true
		}
		return capitalizeFirst(name) + " ended the stream" + reason + ".", kind, true
	case psLastRunDecoderFailed, psLastRunVideoFailed:
		return "Remote Play could not show the video from " + name + ". " + psLastRunVideoAdvice(run), ToastKindError, true
	case psLastRunSDLFailed:
		return "Remote Play could not open the screen, sound or controls. The details are in " + psLastRunLog + ".",
			ToastKindError, true
	case psLastRunInvalidSession:
		return "The saved pairing for " + name + " is incomplete. Pair the console again in PS Remote Play.",
			ToastKindError, true
	case psLastRunInitFailed:
		return "Remote Play's network engine did not start. The details are in " + psLastRunLog + ".",
			ToastKindError, true
	case psLastRunCrashed:
		if run.RC > 0 {
			return "Remote Play stopped unexpectedly (code " + strconv.Itoa(run.RC) + "). The details are in " +
				psLastRunLog + ".", ToastKindError, true
		}
		return "Remote Play stopped unexpectedly. The details are in " + psLastRunLog + ".", ToastKindError, true
	case psLastRunNotStarted:
		return "Remote Play did not start. The details are in " + psLastRunLog + ".", ToastKindError, true
	case psLastRunSessionError:
		return "The stream from " + name + " ended with an error" + reason + ". The details are in " +
			psLastRunLog + ".", ToastKindWarning, true
	}
	// A token this JukaHub does not know (a newer helper): its own sentence.
	if msg := strings.TrimSpace(run.Message); msg != "" {
		return "Remote Play: " + msg, ToastKindWarning, true
	}
	if run.RC != 0 {
		return "Remote Play stopped (code " + strconv.Itoa(run.RC) + "). The details are in " + psLastRunLog + ".",
			ToastKindError, true
	}
	return "", ToastKindInfo, false
}

// psLastRunVideoAdvice is what to change after a stream whose video could not
// be shown: the other codec than the one the stream used, or — for a PS4,
// which only sends H.264 — a lower quality. A report without a codec (an
// older helper) gets the general advice.
func psLastRunVideoAdvice(run psLastRun) string {
	switch {
	case run.Codec == psCodecH265:
		return "Try Video codec H.264 or a lower Quality in PS Remote Play."
	case run.Codec == psCodecH264 && run.PS5:
		return "Try a lower Quality, or Video codec H.265, in PS Remote Play."
	case run.Codec == psCodecH264:
		return "Try a lower Quality in PS Remote Play."
	}
	return "Try Video codec H.264 or a lower Quality in PS Remote Play."
}

// psLastRunReason turns libchiaki's quit reason into a clause (" (…)"), or
// nothing when it has none.
func psLastRunReason(reason string) string {
	reason = strings.TrimSpace(psSessionValue(reason))
	if reason == "" || strings.EqualFold(reason, "none") || strings.EqualFold(reason, "unknown") {
		return ""
	}
	runes := []rune(reason)
	if len(runes) > 80 {
		reason = string(runes[:79]) + "…"
	}
	return " (" + reason + ")"
}

// capitalizeFirst upper-cases a sentence's first letter ("the console" at the
// start of a sentence).
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func psLastRunNoteSet(text string, kind ToastKind, host string) {
	psLastRunNote.mu.Lock()
	psLastRunNote.text, psLastRunNote.kind, psLastRunNote.host = text, kind, host
	psLastRunNote.mu.Unlock()
}

// psLastRunRows is the console list's "Last stream" row, while there is a
// report worth showing. A on it opens that console's options when the report
// is about something set there (the passcode, the internet address).
func psLastRunRows() []chiakiRow {
	psLastRunNote.mu.Lock()
	text, kind, host := psLastRunNote.text, psLastRunNote.kind, psLastRunNote.host
	psLastRunNote.mu.Unlock()
	if text == "" {
		return nil
	}
	row := chiakiRow{label: "Last stream", detail: text, status: "Info", action: psActionNone}
	if kind == ToastKindError {
		row.status = "Error"
	}
	if host != "" {
		row.action = psActionLastRunOpen
	}
	return []chiakiRow{row}
}

// psLastRunNoteHost is the console the "Last stream" row is about.
func psLastRunNoteHost() string {
	psLastRunNote.mu.Lock()
	defer psLastRunNote.mu.Unlock()
	return psLastRunNote.host
}
