package main

// moonlight_lastrun.go — what happened to the last Moonlight stream.
//
// JukaHub is not running during a stream: it hands the screen to
// moonlight-stream and comes back when the stream ends. As with PS Remote
// Play (psremote_lastrun.go), JukaHub leaves a "started" note at
// DataDir/moonlight-last.txt before the hand-off, the helper replaces it with
// its result as it exits (key=value: result, stage, port_flags, decode_ms,
// quit_app, ...), and the hand-off script appends handoff_step lines. On the
// next start the note becomes one toast when the stream did not end well —
// which ports to open when it could not connect, firewall advice when no
// video came, a lighter preset when the handheld could not decode fast
// enough — and the panel keeps it as its "Last stream" row.
//
// The note also says whether the user picked "Quit game on PC" in the stream
// menu (quit_app=1). That, or the PC's "Quit the game after streaming"
// setting, sends /cancel from a goroutine, retried for half a minute while
// the Wi-Fi comes back after the hand-off.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Result tokens: the helper writes the first eleven; the last three are
// JukaHub's own (its note, and what a note still saying "started" becomes).
const (
	mlRunOK             = "ok"
	mlRunCancelled      = "cancelled"
	mlRunConnectFailed  = "connect_failed"
	mlRunHostEnded      = "host_ended"
	mlRunConnectionLost = "connection_lost"
	mlRunNoVideo        = "no_video"
	mlRunDecoderFailed  = "decoder_failed"
	mlRunVideoFailed    = "video_failed"
	mlRunSDLFailed      = "sdl_failed"
	mlRunInvalidSession = "invalid_session"
	mlRunInitFailed     = "init_failed"
	mlRunStarted        = "started"
	mlRunCrashed        = "crashed"
	mlRunNotStarted     = "not_started"
)

const (
	// mlLastRunLogHint is where the details are, as a user reads the path.
	mlLastRunLogHint = "moonlight.log beside JukaHub"
	// mlLastRunFileMax bounds what is read of the note.
	mlLastRunFileMax = 64 << 10
	// mlCancelRetryFor and mlCancelRetryEvery: /cancel after a stream waits
	// for the Wi-Fi to come back.
	mlCancelRetryFor   = 30 * time.Second
	mlCancelRetryEvery = 3 * time.Second
	// mlPerfMinSeconds is how long a stream must have run before its decode
	// time says anything.
	mlPerfMinSeconds = 20
)

// mlLastRun is one parsed note.
type mlLastRun struct {
	RC          int
	Result      string
	Message     string
	Connected   bool
	Seconds     int
	Frames      int64
	Host        string
	Codec       string
	QuitApp     bool
	Stage       string
	ErrorCode   int
	PortFlags   string
	DecodeMS    float64
	FPSShown    float64
	LogOffset   int64
	HandoffStep string
	HandoffLog  string
}

func init() {
	// After the first frame, where a toast can show (startup.go).
	deferStartupTask("moonlight-last-run", mlShowLastRun)
}

// mlLastRunNoteState is the last note's sentence for the panel's "Last
// stream" row.
var mlLastRunNoteState struct {
	mu   sync.Mutex
	text string
	kind ToastKind
}

// mlLastRunNote returns the text for the panel's "Last stream" row ("" when
// the last stream ended well) and its kind.
func mlLastRunNote() (text string, kind ToastKind) {
	mlLastRunNoteState.mu.Lock()
	defer mlLastRunNoteState.mu.Unlock()
	return mlLastRunNoteState.text, mlLastRunNoteState.kind
}

func mlLastRunNoteSet(text string, kind ToastKind) {
	mlLastRunNoteState.mu.Lock()
	mlLastRunNoteState.text, mlLastRunNoteState.kind = text, kind
	mlLastRunNoteState.mu.Unlock()
}

// mlMarkStreamStarted leaves the "started" note the helper replaces, with how
// long moonlight.log is now (this stream's lines come after that point).
func mlMarkStreamStarted(path, host string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	data := "version=1\nresult=" + mlRunStarted + "\nhost=" + psSessionValue(host) + "\n"
	if info, err := os.Stat(mlLogPath()); err == nil {
		data += "log_offset=" + strconv.FormatInt(info.Size(), 10) + "\n"
	} else if os.IsNotExist(err) {
		data += "log_offset=0\n"
	}
	if err := writeFileSynced(path, []byte(data), 0o600); err != nil {
		logWarn("moonlight", "could not leave the stream note: %v", err)
	}
	mlLastRunNoteSet("", ToastKindInfo)
}

// mlLastSession is JukaHub's note of the stream it started: which PC (the
// helper's report names only the address), which game, at what frame rate.
type mlLastSession struct {
	uuid    string
	addr    string
	appID   int
	appName string
	fps     int
}

func mlSaveLastSession(s mlLastSession) {
	data := "uuid=" + psSessionValue(s.uuid) + "\naddr=" + psSessionValue(s.addr) +
		"\napp_id=" + strconv.Itoa(s.appID) + "\napp_name=" + psSessionValue(s.appName) +
		"\nfps=" + strconv.Itoa(s.fps) + "\n"
	if err := writeFileSynced(mlLastSessionPath(), []byte(data), 0o600); err != nil {
		logWarn("moonlight", "could not note the stream's PC: %v", err)
	}
}

// mlTakeLastSession reads the note mlSaveLastSession left and removes it.
func mlTakeLastSession() (mlLastSession, bool) {
	path := mlLastSessionPath()
	data, err := mlReadSmall(path, 4096)
	if err != nil {
		return mlLastSession{}, false
	}
	_ = os.Remove(path)
	var s mlLastSession
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "uuid":
			s.uuid = value
		case "addr":
			s.addr = value
		case "app_id":
			s.appID, _ = strconv.Atoi(value)
		case "app_name":
			s.appName = value
		case "fps":
			s.fps, _ = strconv.Atoi(value)
		}
	}
	return s, s.uuid != ""
}

// mlReadSmall reads a file of at most max bytes (more is an error).
func mlReadSmall(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("too large")
	}
	return data, nil
}

// mlShowLastRun reads the note once, turns it into a toast when the stream
// did not end well, removes it, and sends /cancel when the user asked to
// quit the game. Main thread, at startup.
func mlShowLastRun() {
	path := mlLastRunPath()
	sess, haveSess := mlTakeLastSession()
	data, err := mlReadSmall(path, mlLastRunFileMax)
	_ = os.Remove(path)
	if err != nil {
		return
	}
	run, ok := mlParseLastRun(data)
	if !ok {
		return
	}
	logPath := mlLogPath()
	if run.HandoffLog != "" {
		// The card's log could not be written: the script used its fallback.
		logPath, run.LogOffset = run.HandoffLog, -1
	}
	if run.Result == mlRunStarted {
		run = mlLastRunFromLog(run, psLastRunLogText(logPath, run.LogOffset))
	}

	host, found := mlLastRunHost(sess, haveSess, run.Host)
	name := "the PC"
	if found {
		name = mlTruncate(host.Label(), mlStreamNameMax)
	}
	msg, kind, show := mlLastRunToast(run, name, sess.fps)
	if show {
		if run.HandoffLog != "" {
			msg = strings.ReplaceAll(msg, mlLastRunLogHint, run.HandoffLog)
		}
		if run.Result == mlRunNotStarted && run.HandoffStep != "" {
			msg = strings.TrimSuffix(msg, ".") + " — the hand-off stopped at: " + run.HandoffStep + "."
		}
		logWarn("moonlight", "last stream: result=%s rc=%d stage=%q error=%d ports=%q decode=%.1fms step=%q",
			run.Result, run.RC, run.Stage, run.ErrorCode, run.PortFlags, run.DecodeMS, run.HandoffStep)
		mlLastRunNoteSet(msg, kind)
		ShowToast(msg, kind)
	}

	if found && host.Paired && run.Result != mlRunHostEnded && (run.QuitApp || host.Settings.QuitOnExit) {
		addr := sess.addr
		if addr == "" {
			addr = run.Host
		}
		go mlCancelAfterStream(host, addr)
	}
}

// mlLastRunHost finds the PC the note is about: by the id JukaHub noted,
// else by the address the helper reports.
func mlLastRunHost(sess mlLastSession, haveSess bool, addr string) (mlHost, bool) {
	if haveSess {
		if h, ok := mlStoredHost(sess.uuid); ok {
			return h, true
		}
	}
	addr = mlBareHost(addr)
	if addr == "" {
		return mlHost{}, false
	}
	for _, h := range mlLoadHosts() {
		for _, a := range mlAddresses(h) {
			if strings.EqualFold(a, addr) {
				return h, true
			}
		}
	}
	return mlHost{}, false
}

// mlCancelAfterStream quits the game on the PC after a stream, retrying for
// mlCancelRetryFor while the network comes back, over each of its addresses
// in turn. A refusal by the PC ends it at once.
func mlCancelAfterStream(h mlHost, addr string) {
	addrs := []string{}
	seen := map[string]bool{}
	for _, a := range append([]string{mlBareHost(addr)}, mlAddresses(h)...) {
		if a != "" && !seen[strings.ToLower(a)] {
			seen[strings.ToLower(a)] = true
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return
	}
	deadline := time.Now().Add(mlCancelRetryFor)
	var err error
	for i := 0; ; i++ {
		target := addrs[i%len(addrs)]
		ctx, cancel := context.WithTimeout(context.Background(), mlCancelTimeout)
		err = mlCancelApp(ctx, h, target)
		cancel()
		if err == nil {
			logInfo("moonlight", "quit the game on %s (%s)", h.Label(), target)
			return
		}
		if mlHTTPCode(err) != 0 || errors.Is(err, errMLNotPaired) || errors.Is(err, errMLCertMismatch) || time.Now().After(deadline) {
			break
		}
		time.Sleep(mlCancelRetryEvery)
	}
	logWarn("moonlight", "could not quit the game on %s: %v", h.Label(), err)
	msg := "Could not quit the game on " + h.Label() + " — " + firstLine(err.Error())
	tbxPost(func() { ShowToast(msg, ToastKindWarning) })
}

// mlParseLastRun reads a note: the helper's keys (native/README.md), the
// hand-off script's handoff_step / handoff_log lines and JukaHub's
// log_offset. Unknown keys are ignored; a note with neither a result nor an
// exit code is not one.
func mlParseLastRun(data []byte) (mlLastRun, bool) {
	run := mlLastRun{LogOffset: -1}
	haveRC := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		bit := value == "1" || strings.EqualFold(value, "true")
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
			run.Connected = bit
		case "seconds":
			run.Seconds, _ = strconv.Atoi(value)
		case "frames":
			run.Frames, _ = strconv.ParseInt(value, 10, 64)
		case "host":
			run.Host = value
		case "codec":
			run.Codec = strings.ToLower(value)
		case "quit_app":
			run.QuitApp = bit
		case "stage":
			run.Stage = value
		case "error_code":
			run.ErrorCode, _ = strconv.Atoi(value)
		case "port_flags":
			run.PortFlags = value
		case "decode_ms":
			if f, err := strconv.ParseFloat(value, 64); err == nil && f >= 0 && f < 1e6 {
				run.DecodeMS = f
			}
		case "fps_shown":
			if f, err := strconv.ParseFloat(value, 64); err == nil && f >= 0 && f < 1e4 {
				run.FPSShown = f
			}
		case "handoff_step":
			run.HandoffStep = value
		case "handoff_log":
			// Only the script's own fallback, never an arbitrary file.
			if value == mlFallbackLogPath {
				run.HandoffLog = value
			}
		case "log_offset":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				run.LogOffset = n
			}
		}
	}
	if run.Result == "" && !haveRC {
		return mlLastRun{}, false
	}
	if run.Result == "" {
		run.Result = mlLastRunResultForRC(run.RC)
	}
	return run, true
}

// mlLastRunResultForRC reads a bare exit code the way the helper documents
// them (contract: 0 ok, 2 usage, 3 session file, 4 moonlight-common-c init,
// 5 SDL, 6 decoder, 8 connection start, 10 video, 11 terminated).
func mlLastRunResultForRC(rc int) string {
	switch rc {
	case 0:
		return mlRunOK
	case 2, 3:
		return mlRunInvalidSession
	case 4:
		return mlRunInitFailed
	case 5:
		return mlRunSDLFailed
	case 6:
		return mlRunDecoderFailed
	case 8:
		return mlRunConnectFailed
	case 10:
		return mlRunVideoFailed
	case 11:
		return mlRunConnectionLost
	case 126, 127:
		// The shell could not run moonlight-stream at all.
		return mlRunNotStarted
	}
	return mlRunCrashed
}

// mlLastRunFromLog finishes a note the helper never wrote, from the lines
// the hand-off script wrote around this stream in moonlight.log: no banner is
// a hand-off that never reached the helper, a banner without an exit line a
// helper that died.
func mlLastRunFromLog(run mlLastRun, tail string) mlLastRun {
	header := strings.LastIndex(tail, "=== "+mlLogBanner)
	if header < 0 {
		run.Result, run.RC = mlRunNotStarted, -1
		return run
	}
	last := tail[header:]
	idx := strings.LastIndex(last, "exited with code ")
	if idx < 0 {
		run.Result, run.RC = mlRunCrashed, -1
		return run
	}
	digits := last[idx+len("exited with code "):]
	end := 0
	for end < len(digits) && (digits[end] == '-' || (digits[end] >= '0' && digits[end] <= '9')) {
		end++
	}
	rc, err := strconv.Atoi(digits[:end])
	if err != nil {
		run.Result, run.RC = mlRunCrashed, -1
		return run
	}
	run.RC = rc
	run.Result = mlLastRunResultForRC(rc)
	return run
}

// mlLastRunToast is the sentence a note becomes, its kind, and whether it is
// worth a toast at all. fps is the stream's frame rate (0: 60). Pure.
func mlLastRunToast(run mlLastRun, name string, fps int) (string, ToastKind, bool) {
	if strings.TrimSpace(name) == "" {
		name = "the PC"
	}
	switch run.Result {
	case mlRunOK, mlRunCancelled, mlRunStarted:
		if advice := mlPerfAdvice(run, name, fps); advice != "" {
			return advice, ToastKindInfo, true
		}
		return "", ToastKindInfo, false
	case mlRunHostEnded:
		return "The game on " + name + " was closed, so the stream ended.", ToastKindInfo, true
	case mlRunConnectFailed:
		// A 4xx/5xx in the RTSP stage is the host saying no (the game did not
		// start, or it refused this client), not a blocked port: the helper
		// words its card the same way.
		if strings.Contains(strings.ToLower(run.Stage), "rtsp") && run.ErrorCode >= 400 && run.ErrorCode <= 599 {
			return name + " refused the stream (RTSP " + strconv.Itoa(run.ErrorCode) + "). The game may have failed to " +
				"start: Vibepollo / Sunshine's log on the PC says why.", ToastKindWarning, true
		}
		stage := ""
		if s := mlTruncate(run.Stage, 40); s != "" {
			stage = " (" + s + ")"
		}
		return "Moonlight could not connect to " + name + stage + ". " + mlPortsAdvice(run.PortFlags), ToastKindWarning, true
	case mlRunNoVideo:
		return "Moonlight reached " + name + ", but no video arrived. Let UDP 47998-48000 through the PC's firewall " +
			"(and forward them on the router when streaming over the internet).", ToastKindWarning, true
	case mlRunConnectionLost:
		code := ""
		if run.ErrorCode != 0 {
			code = " (error " + strconv.Itoa(run.ErrorCode) + ")"
		}
		return "The stream from " + name + " was lost" + code + ". The Wi-Fi may have dropped; a lighter preset " +
			"(Light or Steady 30) holds on better.", ToastKindWarning, true
	case mlRunDecoderFailed, mlRunVideoFailed:
		advice := "Try the Light or Steady 30 preset in its settings."
		if run.Codec == mlCodecHEVC || run.Codec == "hevc" || run.Codec == mlCodecAutoSetting {
			advice = "Try Video codec H.264, or the Light preset, in its settings."
		}
		return "Moonlight could not show the video from " + name + ". " + advice, ToastKindError, true
	case mlRunSDLFailed:
		return "Moonlight could not open the screen, sound or controls. The details are in " + mlLastRunLogHint + ".",
			ToastKindError, true
	case mlRunInvalidSession:
		return "Moonlight's stream setup was incomplete — start it again. The details are in " + mlLastRunLogHint + ".",
			ToastKindError, true
	case mlRunInitFailed:
		return "Moonlight's streaming engine did not start. The details are in " + mlLastRunLogHint + ".",
			ToastKindError, true
	case mlRunCrashed:
		if run.RC > 0 {
			return "Moonlight stopped unexpectedly (code " + strconv.Itoa(run.RC) + "). The details are in " +
				mlLastRunLogHint + ".", ToastKindError, true
		}
		return "Moonlight stopped unexpectedly. The details are in " + mlLastRunLogHint + ".", ToastKindError, true
	case mlRunNotStarted:
		return "Moonlight did not start. The details are in " + mlLastRunLogHint + ".", ToastKindError, true
	}
	// A token this JukaHub does not know (a newer helper): its own sentence.
	if msg := mlTruncate(run.Message, 160); msg != "" {
		return "Moonlight: " + msg, ToastKindWarning, true
	}
	if run.RC != 0 {
		return "Moonlight stopped (code " + strconv.Itoa(run.RC) + "). The details are in " + mlLastRunLogHint + ".",
			ToastKindError, true
	}
	return "", ToastKindInfo, false
}

// mlPortsAdvice names the ports to open: the ones the helper found blocked
// (port_flags), else the GameStream set.
func mlPortsAdvice(portFlags string) string {
	if p := mlTruncate(portFlags, 80); p != "" {
		return "Check that the PC's firewall lets through " + p + "."
	}
	return "Check that the PC is on and that its firewall lets through TCP 47984, 47989 and 48010 and UDP 47998-48000."
}

// mlPerfAdvice is a lighter preset's suggestion when the handheld decoded
// more slowly than the frame rate needs (more than ~85% of a frame's time on
// average over a stream long enough to tell); "" otherwise.
func mlPerfAdvice(run mlLastRun, name string, fps int) string {
	if !run.Connected || run.DecodeMS <= 0 || run.Seconds < mlPerfMinSeconds {
		return ""
	}
	if fps <= 0 {
		fps = 60
	}
	budget := 1000.0 / float64(fps) * 0.85
	if run.DecodeMS <= budget {
		return ""
	}
	ms := strconv.FormatFloat(run.DecodeMS, 'f', 1, 64)
	if fps <= 30 {
		return "The stream from " + name + " was heavy for this handheld (" + ms + " ms to decode a frame). " +
			"Try the Light preset in its settings."
	}
	return "The stream from " + name + " was heavy for this handheld (" + ms + " ms to decode a frame). " +
		"Try the Light or Steady 30 preset in its settings."
}
