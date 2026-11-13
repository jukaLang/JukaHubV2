package main

// vkvoice.go — voice typing for the on-screen keyboard.
//
// The mic chip (or R2, the Menu button or F4 while the keyboard is up:
// vkvoice_button.go) records
// through the shared microphone (voicemic.go: SDL's default capture device,
// then the kernel's capture PCMs and SDL's own list): 16 kHz mono 16-bit, at
// most vkVoiceMaxSeconds, with a live level meter in the strip. A second press (A, Enter, a tap on the bar) stops it and
// sends the clip to Groq's Whisper or the Smart Pro's bundled local recognizer.
// In Chat, provisional words appear while recording;
// the full-clip result replaces them at the caret when recording stops.
//
// The states say what is wrong instead of failing silently: no recognizer,
// no microphone (the chip dims and says so: neither SDL nor the
// kernel lists a capture device — the Smart Pro has a built-in mono mic, which
// voicemic.go reaches through ALSA when SDL's default device cannot record),
// silence (Whisper invents text for
// silence, so a quiet clip is never
// sent). Local clips use process pipes, and neither the key nor the audio is ever
// logged.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

type vkVoicePhase int

const (
	vkVoiceIdle vkVoicePhase = iota
	vkVoiceRecording
	vkVoiceSending
	vkVoiceMessage
)

// vkVoiceAction is what A does on a message (the strip offers it as a pill).
type vkVoiceAction int

const (
	vkVoiceNoAction vkVoiceAction = iota
	vkVoiceOpenKey
)

const (
	vkVoiceRate       = 16000
	vkVoiceMaxSeconds = 30
	vkVoiceMaxBytes   = vkVoiceRate * 2 * vkVoiceMaxSeconds
	// vkVoiceMinMs is the shortest clip worth sending: anything shorter was a
	// double tap on the mic, not speech.
	vkVoiceMinMs = 400
	// vkVoiceSilence is the loudest RMS level a clip may peak at and still
	// count as silence. Whisper answers silence with invented sentences.
	vkVoiceSilence   = 0.012
	vkVoiceModel     = "whisper-large-v3-turbo"
	vkVoiceMessageMs = 4500
	vkVoiceTimeout   = 45 * time.Second
	vkVoiceMeterBars = 24
	// Whisper accepts clips, not a live audio stream. Chat asks for a short
	// interim transcript as recording continues and replaces it with the
	// whole-clip transcript when recording stops.
	vkVoicePreviewFirstMs = 1800
	vkVoicePreviewGapMs   = 2200
	vkVoicePreviewTimeout = 12 * time.Second
)

// vkVoiceEndpoint is Groq's transcription endpoint; a variable so tests can
// point it at a local server.
var vkVoiceEndpoint = "https://api.groq.com/openai/v1/audio/transcriptions"

var vkVoiceHTTP = &http.Client{Timeout: vkVoiceTimeout}

var vkVoice struct {
	phase           vkVoicePhase
	mic             *micStream
	buf             []byte
	started         uint64
	level           float64
	peak            float64
	meter           [vkVoiceMeterBars]float64
	meterAt         uint64
	msg             string
	msgUntil        uint64
	action          vkVoiceAction
	cancel          context.CancelFunc
	gen             int
	secs            int
	clock           string
	previewText     string
	previewAt       uint64
	previewBusy     bool
	previewDisabled bool
	previewCancel   context.CancelFunc
}

// vkVoiceResult carries the transcription back from its goroutine; the frame
// tick applies it if it belongs to the current request.
var vkVoiceResult struct {
	mu    sync.Mutex
	ready bool
	gen   int
	text  string
	err   error
}

// Interim transcripts have their own mailbox: a slow preview must never
// replace the final whole-clip answer or revive text after cancellation.
type vkVoicePreviewReply struct {
	gen  int
	text string
	err  error
}

var vkVoicePreviewResult struct {
	mu      sync.Mutex
	replies []vkVoicePreviewReply
}

// vkMic is what is known about a microphone: 0 unknown, 1 present, 2 absent.
var (
	vkMic        atomic.Int32
	vkMicProbing atomic.Bool
)

const (
	vkMicUnknown int32 = iota
	vkMicPresent
	vkMicAbsent
)

// vkMicProbeStart looks for a microphone off the render thread when a keyboard
// opens (a USB mic plugged in later is found on the next open): SDL's capture
// devices, and on Linux the kernel's capture PCMs too (micPresence) — SDL's
// list alone missed the Smart Pro's codec and hid the mic chip.
func vkMicProbeStart() {
	if !vkMicProbing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer vkMicProbing.Store(false)
		if v := micPresence(); v != vkMicUnknown {
			vkMic.Store(v)
		}
	}()
}

// vkVoiceAvailable reports whether the mic chip can be offered: a field with a
// strip (not a password, a command or a number) and no known-missing mic.
func vkVoiceAvailable() bool {
	return vkStripModeNow() != vkStripHidden && !vkSecretTarget() && vkMic.Load() != vkMicAbsent
}

// vkVoiceActive reports whether the strip shows the voice bar.
func vkVoiceActive() bool {
	if vkVoice.phase == vkVoiceMessage && sdl.GetTicks64() >= vkVoice.msgUntil {
		vkVoice.phase = vkVoiceIdle
		vkVoice.action = vkVoiceNoAction
	}
	return vkVoice.phase != vkVoiceIdle
}

// vkVoiceSay shows a message in the strip for a few seconds.
func vkVoiceSay(msg string, action vkVoiceAction) {
	vkVoice.phase = vkVoiceMessage
	vkVoice.msg = msg
	vkVoice.action = action
	vkVoice.msgUntil = sdl.GetTicks64() + vkVoiceMessageMs
	if action != vkVoiceNoAction && vkGeom.nCells > 0 {
		// Put A on the action, so "add your key" is one press away.
		vkFocusZone, vkZoneIndex = vkZoneStrip, 0
	}
}

// vkVoiceToggle is the mic chip: start listening, or stop and transcribe.
func vkVoiceToggle(config *Config) {
	switch vkVoice.phase {
	case vkVoiceRecording:
		vkVoiceStop(config)
		return
	case vkVoiceSending:
		vkVoiceCancel()
		return
	}
	switch {
	case vkStripModeNow() == vkStripHidden || vkSecretTarget():
		vkVoiceSay("Voice typing is off for this field", vkVoiceNoAction)
		return
	case vkMic.Load() == vkMicAbsent:
		vkVoiceSay("No microphone — plug one in to use voice typing", vkVoiceNoAction)
		return
	case !sttCanListenFor(config):
		vkVoiceSay("Set up speech recognition in Settings → Voice & AI", vkVoiceNoAction)
		return
	}
	// The shared microphone (voicemic.go) tries SDL's default device, then
	// the kernel's capture PCMs and SDL's own list, and hands back 16 kHz mono
	// 16-bit, which is exactly what Whisper wants.
	jukaSpeakStop() // it must not hear Juka reading a reply (tts_api.go)
	mic, err := micOpen()
	if err != nil {
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			vkVoiceSay("No microphone found", vkVoiceNoAction)
			return
		}
		vkVoiceSay("The microphone didn't open — the log says why", vkVoiceNoAction)
		return
	}
	vkMic.Store(vkMicPresent)
	if cap(vkVoice.buf) < vkVoiceMaxBytes {
		vkVoice.buf = make([]byte, 0, vkVoiceMaxBytes)
	}
	vkVoice.buf = vkVoice.buf[:0]
	vkVoice.mic = mic
	vkVoice.phase = vkVoiceRecording
	vkVoice.gen++
	vkVoicePreviewReset()
	vkVoice.started = sdl.GetTicks64()
	vkVoice.level, vkVoice.peak = 0, 0
	vkVoice.meter = [vkVoiceMeterBars]float64{}
	vkVoice.secs, vkVoice.clock = -1, ""
	vkPlay(PlayActivateSound)
}

// vkVoiceDrain moves whatever the microphone captured into the clip.
func vkVoiceDrain() {
	if vkVoice.mic == nil {
		return
	}
	room := (cap(vkVoice.buf) - len(vkVoice.buf)) &^ 1
	if room <= 0 {
		return
	}
	start := len(vkVoice.buf)
	got := vkVoice.mic.Read(vkVoice.buf[start : start+room])
	if got <= 0 {
		return
	}
	vkVoice.buf = vkVoice.buf[:start+got]
	level := vkPCMLevel(vkVoice.buf[start : start+got])
	if level > vkVoice.peak {
		vkVoice.peak = level
	}
	// Fast attack, slow release: the meter jumps with a syllable and falls
	// back gently.
	if level > vkVoice.level {
		vkVoice.level = level
	} else {
		vkVoice.level = vkVoice.level*0.82 + level*0.18
	}
}

// vkVoiceCloseDevice stops and closes the microphone.
func vkVoiceCloseDevice() {
	if vkVoice.mic != nil {
		vkVoice.mic.Close()
		vkVoice.mic = nil
	}
}

// vkVoiceStop ends the recording and sends it, unless it was too short or
// silent.
func vkVoiceStop(config *Config) {
	if vkVoice.phase != vkVoiceRecording {
		return
	}
	vkVoiceDrain()
	vkVoiceCloseDevice()
	vkVoicePreviewCancelRequest()
	ms := len(vkVoice.buf) * 1000 / (vkVoiceRate * 2)
	switch {
	case ms < vkVoiceMinMs:
		vkVoicePreviewReset()
		vkVoice.phase = vkVoiceIdle
		vkPlay(PlayBackSound)
		return
	case vkVoice.peak < vkVoiceSilence:
		vkVoicePreviewReset()
		vkVoiceSay("Didn't hear anything — is the microphone on?", vkVoiceNoAction)
		return
	}
	key := strings.TrimSpace(groqAPIKey())
	lang := ""
	if vkVoiceFollowsLayout() {
		lang = vkCurrentLayout().Lang
	}
	wav := vkEncodeWAV(vkVoice.buf, vkVoiceRate)
	vkVoice.gen++
	gen := vkVoice.gen
	ctx, cancel := context.WithTimeout(context.Background(), sttTimeout)
	vkVoice.cancel = cancel
	vkVoice.phase = vkVoiceSending
	vkPlay(PlayActivateSound)
	go func() {
		defer cancel()
		text, err := sttVoiceTypingTranscribe(ctx, key, wav, lang)
		vkVoiceResult.mu.Lock()
		vkVoiceResult.ready, vkVoiceResult.gen, vkVoiceResult.text, vkVoiceResult.err = true, gen, text, err
		vkVoiceResult.mu.Unlock()
	}()
}

// vkVoiceCancel stops listening or waiting and forgets the clip. Closing the
// keyboard calls it, so the mic never stays open behind a closed keyboard.
func vkVoiceCancel() {
	vkVoiceCloseDevice()
	vkVoicePreviewReset()
	if vkVoice.cancel != nil {
		vkVoice.cancel()
		vkVoice.cancel = nil
	}
	vkVoice.gen++
	vkVoice.phase = vkVoiceIdle
	vkVoice.action = vkVoiceNoAction
	vkVoice.buf = vkVoice.buf[:0]
}

// vkVoicePreviewReset removes only interim text. The message draft is never
// changed until the final whole-clip transcription succeeds.
func vkVoicePreviewReset() {
	vkVoicePreviewCancelRequest()
	vkVoice.previewText = ""
	vkVoice.previewAt = 0
	vkVoice.previewDisabled = false
}

func vkVoicePreviewCancelRequest() {
	if vkVoice.previewCancel != nil {
		vkVoice.previewCancel()
		vkVoice.previewCancel = nil
	}
	vkVoice.previewBusy = false
}

// vkVoicePreviewTick asks for a partial transcript while Chat is recording.
// Only one request runs at a time. A later request includes the whole clip so
// Whisper can revise earlier words instead of joining guesses at cut points.
func vkVoicePreviewTick() {
	if vkVoice.phase != vkVoiceRecording || vkTarget != vkTargetChat || !virtualKeyboardActive {
		return
	}
	vkVoicePreviewResult.mu.Lock()
	replies := vkVoicePreviewResult.replies
	vkVoicePreviewResult.replies = nil
	vkVoicePreviewResult.mu.Unlock()
	for _, reply := range replies {
		if reply.gen != vkVoice.gen {
			continue
		}
		vkVoice.previewBusy = false
		vkVoice.previewCancel = nil
		if reply.err == nil {
			if partial := strings.TrimSpace(reply.text); partial != "" {
				vkVoice.previewText = partial
			}
			continue
		}
		// The full-clip request still runs when recording stops. One failed
		// preview is enough; keep the partial already shown and avoid a train
		// of failing requests while the person is still speaking.
		vkVoice.previewDisabled = true
	}
	if vkVoice.previewBusy || vkVoice.previewDisabled || vkVoice.peak < vkVoiceSilence {
		return
	}
	now := sdl.GetTicks64()
	if now-vkVoice.started < vkVoicePreviewFirstMs ||
		(vkVoice.previewAt != 0 && now-vkVoice.previewAt < vkVoicePreviewGapMs) {
		return
	}
	// Ignore the start chime and a silent microphone. A quiet clip should not
	// be sent to Whisper, which can invent words for silence.
	settleBytes := vkVoiceRate * 2 * vkVoiceAutoSettleMs / 1000
	if len(vkVoice.buf) <= settleBytes || vkPCMLevel(vkVoice.buf[settleBytes:]) < vkVoiceSilence/2 {
		return
	}
	key := strings.TrimSpace(groqAPIKey())
	mode := sttModeOf(appConfig)
	localReady := sttLocalReady()
	cloudPreview, localPreview, localFallback := sttPreviewChoice(mode, key, IsOnline(), localReady)
	if !cloudPreview && !localPreview {
		return
	}
	if localPreview && vkVoice.previewAt != 0 && now-vkVoice.previewAt < 5000 {
		return
	}
	lang := ""
	if vkVoiceFollowsLayout() {
		lang = vkCurrentLayout().Lang
	}
	pcm := append([]byte(nil), vkVoice.buf...)
	gen := vkVoice.gen
	previewTimeout := vkVoicePreviewTimeout
	if localPreview || localFallback {
		previewTimeout = 35 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
	vkVoice.previewCancel = cancel
	vkVoice.previewBusy = true
	vkVoice.previewAt = now
	go func() {
		defer cancel()
		var text string
		var err error
		wav := vkEncodeWAV(pcm, vkVoiceRate)
		if localPreview {
			text, err = sttLocalPreviewTranscribe(ctx, wav, lang)
		} else if localFallback {
			cloudCtx, cloudCancel := context.WithTimeout(ctx, 8*time.Second)
			text, err = vkTranscribe(cloudCtx, key, wav, lang)
			cloudCancel()
			if err != nil && ctx.Err() == nil {
				text, err = sttLocalPreviewTranscribe(ctx, wav, lang)
			}
		} else {
			text, err = vkTranscribe(ctx, key, wav, lang)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		vkVoicePreviewResult.mu.Lock()
		vkVoicePreviewResult.replies = append(vkVoicePreviewResult.replies, vkVoicePreviewReply{gen: gen, text: text, err: err})
		vkVoicePreviewResult.mu.Unlock()
	}()
}

// vkVoiceTick runs once per frame while the keyboard is open: it records,
// stops at the time limit and applies a finished transcription. A message
// field may start listening by itself first (vkvoice_button.go).
func vkVoiceTick(config *Config) {
	vkVoiceAutoTick(config)
	switch vkVoice.phase {
	case vkVoiceRecording:
		vkVoiceDrain()
		if len(vkVoice.buf) >= cap(vkVoice.buf) {
			vkVoiceStop(config)
			return
		}
		vkVoicePreviewTick()
		now := sdl.GetTicks64()
		if now-vkVoice.meterAt >= 50 {
			vkVoice.meterAt = now
			copy(vkVoice.meter[:], vkVoice.meter[1:])
			vkVoice.meter[len(vkVoice.meter)-1] = vkVoice.level
		}
	case vkVoiceSending:
		vkVoiceResult.mu.Lock()
		ready, gen, text, err := vkVoiceResult.ready, vkVoiceResult.gen, vkVoiceResult.text, vkVoiceResult.err
		if ready {
			vkVoiceResult.ready = false
		}
		vkVoiceResult.mu.Unlock()
		if !ready || gen != vkVoice.gen {
			return
		}
		vkVoice.cancel = nil
		vkVoice.phase = vkVoiceIdle
		vkVoicePreviewReset()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			msg, action := vkVoiceErrorText(err)
			vkVoiceSay(msg, action)
			vkPlay(PlayErrorSound)
			return
		}
		vkVoiceInsert(text, config)
	}
}

// vkVoicePrepareText makes a transcript fit the field at its caret. Interim
// text and the final insert use the same spacing so the preview does not jump.
func vkVoicePrepareText(current string, caret int, text string, sentence bool) string {
	text = strings.TrimSpace(text)
	if !sentence {
		text = strings.TrimRight(text, ".。")
	}
	if text == "" {
		return ""
	}
	before := current[:vkRuneOffset(current, caret)]
	if r, _ := utf8.DecodeLastRuneInString(before); before != "" && !unicode.IsSpace(r) {
		if first, _ := utf8.DecodeRuneInString(text); !unicode.IsPunct(first) {
			text = " " + text
		}
	}
	return text
}

// vkVoicePreviewDraft returns a provisional Chat draft and its display caret.
// It never writes the real draft: cancel and transcription errors leave the
// user's original text intact.
func vkVoicePreviewDraft(current, partial string, caret int, selected bool) (string, int) {
	if selected {
		current, caret = "", 0
	}
	if caret < 0 {
		caret = 0
	}
	n := utf8.RuneCountInString(current)
	if caret > n {
		caret = n
	}
	insert := vkVoicePrepareText(current, caret, partial, true)
	insert = vkTruncateUTF8(insert, vkMaxTextLen-len(current))
	if insert == "" {
		return current, caret
	}
	off := vkRuneOffset(current, caret)
	return current[:off] + insert + current[off:], caret + utf8.RuneCountInString(insert)
}

// vkVoiceDisplayText is the on-screen Chat field while a preview is available.
// The committed draft remains in vkCurrentText until the full clip succeeds.
func vkVoiceDisplayText() (string, int, bool) {
	if vkTarget != vkTargetChat || (vkVoice.phase != vkVoiceRecording && vkVoice.phase != vkVoiceSending) || vkVoice.previewText == "" {
		return "", 0, false
	}
	current := vkCurrentText()
	text, caret := vkVoicePreviewDraft(current, vkVoice.previewText, vkCaretIndex(current), vkTextSelected)
	return text, caret, true
}

// vkVoiceInsert puts the final transcript at the caret, with a space when it
// follows a word and without Whisper's closing period in a search field.
func vkVoiceInsert(text string, config *Config) {
	current := vkCurrentText()
	caret := vkCaretIndex(current)
	if vkTextSelected {
		current, caret = "", 0
	}
	text = vkVoicePrepareText(current, caret, text, vkSentenceTarget())
	if text == "" {
		vkVoiceSay("Didn't catch that — try again", vkVoiceNoAction)
		return
	}
	vkInsertText(text, config)
	vkPlay(PlaySuccessSound)
}

// vkVoiceErrorText turns a failure into one sentence and, for a rejected key,
// the jump to fix it. Network failures read as "needs internet" rather than a
// dial error.
func vkVoiceErrorText(err error) (string, vkVoiceAction) {
	if errors.Is(err, errSTTMemory) {
		return "Close a game to free memory for offline speech", vkVoiceNoAction
	}
	if errors.Is(err, errSTTUnavailable) {
		return "Offline speech is missing — install it in Settings → Voice & AI", vkVoiceNoAction
	}
	if errors.Is(err, errSTTGroqKey) {
		return "Groq speech needs an API key — add it in Settings", vkVoiceOpenKey
	}
	if errors.Is(err, errSTTGroqOffline) {
		return "Groq speech needs Wi-Fi", vkVoiceNoAction
	}
	var gerr *groqError
	if errors.As(err, &gerr) {
		if gerr.status == http.StatusUnauthorized || gerr.status == http.StatusForbidden {
			return "Groq rejected the API key — check it in Settings", vkVoiceOpenKey
		}
		return gerr.Error(), vkVoiceNoAction
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Voice typing timed out — try a shorter clip", vkVoiceNoAction
	}
	if msg := plainNetworkError(err); msg == offlinePlainNotice {
		return "Voice typing needs internet", vkVoiceNoAction
	}
	return "Voice typing failed — try again", vkVoiceNoAction
}

// vkVoiceRunAction runs the message's action (A on the strip, a tap on the
// pill).
func vkVoiceRunAction(config *Config) {
	action := vkVoice.action
	vkVoice.phase, vkVoice.action = vkVoiceIdle, vkVoiceNoAction
	if action == vkVoiceOpenKey {
		vkVoiceOpenKeySettings(config)
	}
}

// vkVoiceOpenKeySettings closes the keyboard and opens the place where the
// Groq key is entered: Settings → General's key sheet, or Keyboard settings
// when a config has no Settings screen.
func vkVoiceOpenKeySettings(config *Config) {
	vkCloseKeyboard()
	if config == nil {
		return
	}
	// Settings → General → Groq API key with its sheet open: paste the key
	// from the phone, or type or paste it here (groqkey_sheet.go).
	if sceneIndexExact(config, "Settings") >= 0 {
		groqKeyOpenFromAnywhere(config)
		return
	}
	vkOpenSettings(config)
}

// vkVoiceHandleButton gives the voice bar first claim on a pad button: while
// listening, A or Y stops and B cancels; while sending, B cancels; a message's
// action answers A when the strip is focused.
func vkVoiceHandleButton(button uint8, config *Config) bool {
	switch vkVoice.phase {
	case vkVoiceRecording:
		switch button {
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_Y:
			vkVoiceStop(config)
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
			vkVoiceCancel()
			vkPlay(PlayBackSound)
		}
		return true
	case vkVoiceSending:
		switch button {
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			vkVoiceCancel()
			vkPlay(PlayBackSound)
			return true
		}
	case vkVoiceMessage:
		if vkVoiceActive() && vkFocusZone == vkZoneStrip && button == sdl.CONTROLLER_BUTTON_A && vkVoice.action != vkVoiceNoAction {
			vkVoiceRunAction(config)
			return true
		}
	}
	return false
}

// vkVoiceHandleKey is the same for a real keyboard: Enter or Space stops,
// Esc cancels.
func vkVoiceHandleKey(sym sdl.Keycode, config *Config) bool {
	switch vkVoice.phase {
	case vkVoiceRecording:
		switch sym {
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			vkVoiceStop(config)
		case sdl.K_ESCAPE:
			vkVoiceCancel()
		}
		return true
	case vkVoiceSending:
		if sym == sdl.K_ESCAPE {
			vkVoiceCancel()
			return true
		}
	}
	return false
}

// vkVoiceHandlePointer answers a click on the voice bar.
func vkVoiceHandlePointer(mx, my int32, config *Config) bool {
	if !vkVoiceActive() || vkGeom.strip.W <= 0 {
		return false
	}
	if !pointInRect(mx, my-vkAnimOffset(), vkGeom.strip) {
		return false
	}
	switch vkVoice.phase {
	case vkVoiceRecording:
		vkVoiceStop(config)
	case vkVoiceSending:
		vkVoiceCancel()
	case vkVoiceMessage:
		if vkVoice.action != vkVoiceNoAction {
			vkVoiceRunAction(config)
		} else {
			vkVoice.phase = vkVoiceIdle
		}
	}
	return true
}

// ── Audio and request ────────────────────────────────────────────────────

// vkPCMLevel is the RMS level (0..1) of signed 16-bit little-endian samples.
func vkPCMLevel(b []byte) float64 {
	n := len(b) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(b); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(b[i:]))) / 32768
		sum += s * s
	}
	return math.Sqrt(sum / float64(n))
}

// vkEncodeWAV wraps mono 16-bit PCM in a WAV header.
func vkEncodeWAV(pcm []byte, rate int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], uint32(rate))
	binary.LittleEndian.PutUint32(out[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// vkTranscribe sends a WAV clip to Groq's Whisper and returns the text. lang
// is an ISO 639-1 hint ("" lets Whisper detect the language). The key travels
// only in the Authorization header; errors carry no request body.
func vkTranscribe(ctx context.Context, key string, wav []byte, lang string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="speech.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	fields := [][2]string{{"model", vkVoiceModel}, {"response_format", "json"}, {"temperature", "0"}}
	if lang != "" {
		fields = append(fields, [2]string{"language", lang})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vkVoiceEndpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := vkVoiceHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", groqErrorFromResponse(resp.StatusCode, raw)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("Groq returned an unreadable transcript")
	}
	return strings.TrimSpace(out.Text), nil
}

// ── Drawing ──────────────────────────────────────────────────────────────

// vkVoiceClock is the elapsed time as m:ss, rebuilt once a second.
func vkVoiceClock(now uint64) string {
	secs := int((now - vkVoice.started) / 1000)
	if secs != vkVoice.secs {
		vkVoice.secs = secs
		s := secs % 60
		pad := ""
		if s < 10 {
			pad = "0"
		}
		vkVoice.clock = strconv.Itoa(secs/60) + ":" + pad + strconv.Itoa(s) + " / 0:" + strconv.Itoa(vkVoiceMaxSeconds)
	}
	return vkVoice.clock
}

// vkDrawVoiceBar replaces the strip while voice typing is busy: a level meter
// and the clock while listening, three pulsing dots while Groq transcribes,
// and the message (with its action pill) otherwise.
func vkDrawVoiceBar(renderer *sdl.Renderer, fonts vkFontSet, pal vkPalette, bar sdl.Rect, now uint64) {
	if bar.W <= 0 || bar.H <= 0 {
		return
	}
	fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 10, pal.cellBest)
	font := fonts.small
	if font == nil {
		return
	}
	pad := int32(14)
	cy := bar.Y + bar.H/2
	_, th := measureText(font, "Ag")
	textY := cy - th/2
	// R2 started it and R2 sends it (vkvoice_button.go); A still stops too.
	hint := "R2 stop  ·  B cancel"
	if hintsUseKeyboard() {
		hint = "Enter stop  ·  Esc cancel"
	}
	switch vkVoice.phase {
	case vkVoiceRecording:
		pulse := 0.6 + 0.4*math.Sin(float64(now)/180)
		dot := int32(10)
		fillRoundedRect(renderer, bar.X+pad, cy-dot/2, dot, dot, dot/2, WithAlpha(ColorDanger, uint8(255*pulse)))
		x := bar.X + pad + dot + 10
		w, _ := measureText(font, "Listening")
		drawText(renderer, font, "Listening", x, textY, pal.ink, textAlignLeft)
		x += w + 12
		clock := vkVoiceClock(now)
		cw, _ := measureText(font, clock)
		drawText(renderer, font, clock, x, textY, pal.ink3, textAlignLeft)
		x += cw + 16
		hw, _ := measureText(font, hint)
		right := bar.X + bar.W - pad - hw
		drawText(renderer, font, hint, right, textY, pal.ink3, textAlignLeft)
		// The meter fills the room between the clock and the hint.
		meterW := right - 16 - x
		if meterW > 40 {
			n := int32(len(vkVoice.meter))
			step := meterW / n
			barW := step - 2
			if barW < 2 {
				barW = 2
			}
			maxH := bar.H - 12
			for i, v := range vkVoice.meter {
				h := int32(math.Min(1, v*6) * float64(maxH))
				if h < 3 {
					h = 3
				}
				fillRoundedRect(renderer, x+int32(i)*step, cy-h/2, barW, h, 1, pal.accent)
			}
		}
	case vkVoiceSending:
		x := bar.X + pad
		for i := 0; i < 3; i++ {
			phase := math.Sin(float64(now)/160 - float64(i)*0.9)
			a := uint8(110 + 145*(0.5+0.5*phase))
			fillRoundedRect(renderer, x+int32(i)*14, cy-4, 8, 8, 4, WithAlpha(pal.accent, a))
		}
		drawText(renderer, font, netWaitLabel("Transcribing…"), x+50, textY, pal.ink, textAlignLeft)
		cancel := "B cancel"
		if !P().IsHandheld() {
			cancel = "Esc cancel"
		}
		hw, _ := measureText(font, cancel)
		drawText(renderer, font, cancel, bar.X+bar.W-pad-hw, textY, pal.ink3, textAlignLeft)
	case vkVoiceMessage:
		x := bar.X + pad
		pillW := int32(0)
		label := "Open Settings"
		if vkVoice.action != vkVoiceNoAction {
			lw, _ := measureText(font, label)
			pillW = lw + 28
		}
		msg := ellipsize(font, vkVoice.msg, bar.W-2*pad-pillW-12)
		drawText(renderer, font, msg, x, textY, pal.ink, textAlignLeft)
		if pillW > 0 {
			pill := sdl.Rect{X: bar.X + bar.W - pad - pillW, Y: bar.Y + 5, W: pillW, H: bar.H - 10}
			fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, pal.accent)
			lw, _ := measureText(font, label)
			drawText(renderer, font, label, pill.X+(pill.W-lw)/2, textY, pal.accentInk, textAlignLeft)
			if vkFocusZone == vkZoneStrip {
				strokeRoundedRect(renderer, pill.X-3, pill.Y-3, pill.W+6, pill.H+6, (pill.H+6)/2, glassFocusRingWidth, pal.ink)
			}
		}
	}
}
