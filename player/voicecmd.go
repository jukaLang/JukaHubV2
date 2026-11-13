package main

// voicecmd.go — Voice command: open apps, games and settings by saying so.
//
// Start → Tools → Voice command or Y in the Home search sheet opens a page
// with one big microphone (the quick way in is the voice orb: Menu, or F4 —
// voiceorb.go — which runs the same parser and executor). Press A and speak ("open YouTube", "play Mario
// Kart", "volume 50", "take a screenshot"); the page stops listening by itself
// when you stop talking, Whisper turns the clip into words, and the command
// runs a moment later — long enough for B to cancel a misheard one. A name
// that could mean several things lists them; one that matches nothing offers
// the Home search sheet with the words typed in.
//
// Three ways in, one parser (voicecmd_intent.go) and one executor
// (voicecmd_exec.go):
//
//   - the handheld's microphone (the Smart Pro has a mono one; a PC uses its
//     default input), recorded through the shared microphone (voicemic.go) at
//     16 kHz mono, recognized by Groq's Whisper when available or by the
//     bundled Smart Pro model when it is not. A short list of names on this
//     handheld helps spell "JukaLand" and game titles;
//   - the phone: Y shows a QR code for the companion's /voice page, where the
//     microphone on the phone's own keyboard dictates the command
//     (voicecmd_phone.go) — no key, and it works where the handheld's mic
//     does not;
//   - typing: X opens the keyboard, so the page doubles as a command line.
//
// Nothing is recorded until A is pressed, the microphone closes the moment the
// page is left, and neither the audio nor the words are written anywhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// voiceSceneName is the page's scene. No shipped config carries it: it is
// injected in memory (ensureVoiceScene) like the ROM manager's.
const voiceSceneName = "VoiceCommand"

const (
	// voiceMaxSeconds bounds a clip: a command is a few words.
	voiceMaxSeconds = 8
	voiceMaxBytes   = vkVoiceRate * 2 * voiceMaxSeconds
	// voiceSpeechLevel is the RMS a chunk has to reach to count as speech,
	// voiceQuietLevel the level under which it is a pause again.
	voiceSpeechLevel = 0.025
	voiceQuietLevel  = 0.015
	// voiceEndQuietMs of quiet after speech ends the clip by itself;
	// voiceNoSpeechMs without any speech gives up.
	voiceEndQuietMs = 1200
	voiceNoSpeechMs = 6000
	// voiceChimeMs of audio after the microphone opens is thrown away: the
	// start chime plays then, and the microphone hears it — it counted as
	// speech and ended the clip 1.2 s later, before the user had said a word.
	voiceChimeMs = 300
	// voiceConfirmMs is the pause between understanding a command and running
	// it, so B can still stop a misheard one.
	voiceConfirmMs = 650
	// voiceMicRadius is the microphone disc's radius (smaller while a choice
	// list needs the room).
	voiceMicRadius      = int32(60)
	voiceMicRadiusSmall = int32(34)
)

// voicePhase is what the page is doing.
type voicePhase int

const (
	voicePhaseReady voicePhase = iota
	voicePhaseListening
	voicePhaseUnderstanding
	voicePhaseConfirm
	voicePhaseChoices
	voicePhaseAnswer
)

// voicePageState is the page. Main thread only.
type voicePageState struct {
	visit tbxVisit
	nav   tbxNav
	phase voicePhase
	// heard is the command as it arrived; say is the answer line.
	heard string
	say   string
	tone  tbxTone
	fix   voiceFix
	// search and web are the offers of a command that matched nothing.
	search, web string
	choices     []voiceMatch
	// act runs when the confirm pause ends; leave runs it behind the page.
	act       func(*Config)
	leave     bool
	confirmAt uint64
	// Capture (the shared microphone, voicemic.go).
	mic        *micStream
	buf        []byte
	started    uint64
	level      float64
	peak       float64
	speaking   bool
	quietSince uint64
	// capDone is closed when the device closes; it ends the watcher that
	// closes the microphone when the page is left mid-listen.
	capDone chan struct{}
	// gen invalidates a transcription that belongs to an earlier request.
	gen    int
	cancel context.CancelFunc
	// hasKey caches whether speech recognition is available for the header.
	hasKey bool
	// micRect is the disc as drawn and clipped to the body, for a click.
	micRect sdl.Rect
	wrap    tbxWrapCache
}

var voicePage voicePageState

// voiceButton is one button of the page's row.
type voiceButton struct {
	id, label string
	primary   bool
}

// voiceChoiceIDs name the choice rows (no per-frame string building).
var voiceChoiceIDs = [voiceMaxChoices]string{"c0", "c1", "c2", "c3", "c4"}

// voiceChoiceIndex is the row a choice id names ("cancel" is not one).
func voiceChoiceIndex(id string) (int, bool) {
	for i, c := range voiceChoiceIDs {
		if id == c {
			return i, true
		}
	}
	return 0, false
}

type voiceScene struct{ sceneBase }

func (voiceScene) render(r *sdl.Renderer, c *Config) bool {
	voiceRender(r, c)
	return true
}

func (voiceScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), voiceOnAction)
}

func (voiceScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), voiceOnAction)
}

func init() { registerScene(voiceScene{sceneBase{name: voiceSceneName}}) }

// ensureVoiceScene gives the page its scene, in memory: no shipped config
// carries it and the user's config is never rewritten for it. Idempotent;
// called by the opener and after every config reload.
func ensureVoiceScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, voiceSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: voiceSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == voiceSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
	// The Voice shortcuts page travels with this one: startup and every
	// reload inject both (voiceshortcuts_ui.go).
	vscEnsureScene(config)
}

// voiceOpen opens the page (the Start menu row, the search sheet's Y, F4).
func voiceOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	ensureVoiceScene(config)
	idx := sceneIndexExact(config, voiceSceneName)
	if idx < 0 {
		return
	}
	if globalSearchOpen {
		homeSearchClose()
	}
	// The panels that could still be up go first, as tbxOpenScene does: a
	// scene change must never leave a modal over the page, with the page
	// listening behind it.
	powerTools.open = false
	communityPanel.open = false
	qs.open = false
	vkMicProbeStart()
	if idx == currentSceneIndex {
		return
	}
	PlayActivateSound()
	changeSceneTo(config, idx)
}

// voiceToggle is the F4 key: open the page, or on it, speak (or stop).
func voiceToggle(config *Config) {
	if tbxCurrentScene(config) == voiceSceneName {
		if tbxSheetVisible(config) {
			// The phone sheet is up: F4 means "listen now", not behind it.
			tbxCloseSheet()
		}
		voiceSpeakOrStop(config)
		return
	}
	voiceOpen(config)
}

// voiceOnPage reports whether the page is on screen.
func voiceOnPage(config *Config) bool {
	return tbxCurrentScene(config) == voiceSceneName
}

// voiceEnter runs once per visit: a fresh page, focus on the microphone (or on
// the phone when there is no microphone to use).
func voiceEnter() {
	voiceStopCapture()
	voicePage.phase = voicePhaseReady
	voicePage.heard, voicePage.say, voicePage.fix = "", "", voiceFixNone
	voicePage.search, voicePage.web = "", ""
	voicePage.choices = nil
	voicePage.act, voicePage.leave = nil, false
	voicePage.nav.scroll = 0
	voicePage.nav.focus = "speak"
	if vkMic.Load() == vkMicAbsent {
		voicePage.nav.focus = "phone"
	}
	// Coming back from Settings with a key is a new visit, so this is fresh.
	voicePage.hasKey = sttCanListen()
	vkMicProbeStart()
	homeSearchRefreshIndex(appConfig)
}

// voiceReset returns the page to its ready state.
func voiceReset() {
	voiceStopCapture()
	voicePage.phase = voicePhaseReady
	voicePage.say, voicePage.tone, voicePage.fix = "", tbxToneNeutral, voiceFixNone
	voicePage.search, voicePage.web = "", ""
	voicePage.choices = nil
	voicePage.act, voicePage.leave = nil, false
	voicePage.nav.focus = "speak"
}

// voiceSay shows an answer line. The offers of an earlier "nothing called …"
// answer go with it (voiceShow sets them again when they belong to this one).
func voiceSay(msg string, tone tbxTone, fix voiceFix) {
	voicePage.phase = voicePhaseAnswer
	voicePage.say, voicePage.tone, voicePage.fix = msg, tone, fix
	voicePage.search, voicePage.web = "", ""
	voicePage.choices = nil
	voicePage.act, voicePage.leave = nil, false
	switch fix {
	case voiceFixGroqKey, voiceFixPhone, voiceFixType:
		voicePage.nav.focus = "fix"
	default:
		voicePage.nav.focus = "speak"
	}
}

// ── Listening ────────────────────────────────────────────────────────────

// voiceSpeakOrStop is A on the microphone: start listening, or stop and send.
func voiceSpeakOrStop(config *Config) {
	switch voicePage.phase {
	case voicePhaseListening:
		voiceFinishClip(config)
		return
	case voicePhaseUnderstanding:
		return
	case voicePhaseConfirm:
		voiceRunPending(config)
		return
	}
	voiceStartListening(config)
}

// voiceStartListening opens the microphone, unless something it needs is
// missing — then the page says which, with the fix one press away.
func voiceStartListening(config *Config) {
	jukaSpeakStop() // the microphone must not hear Juka (tts_api.go)
	// A device still open from an earlier listen (a phone command arrived
	// mid-listen) is closed first: overwriting voicePage.mic leaked it, and the
	// next open could then fail and mark the mic absent for voice typing too.
	if voicePage.mic != nil {
		voiceStopCapture()
	}
	voicePage.hasKey = sttCanListen()
	switch {
	case vkMic.Load() == vkMicAbsent:
		voiceSay("No microphone found — say it on your phone, or type it", tbxToneWarn, voiceFixPhone)
		PlayErrorSound()
		return
	case !sttCanListen():
		fix := voiceFixNone
		if sttModeOf(config) != sttModeOffline && strings.TrimSpace(groqAPIKey()) == "" {
			fix = voiceFixGroqKey
		}
		voiceSay("Set up speech recognition in Settings → Voice & AI", tbxToneWarn, fix)
		PlayErrorSound()
		return
	}
	// The shared microphone (voicemic.go): SDL's default device, then the
	// kernel's capture PCMs and SDL's own list, as 16 kHz mono 16-bit, which is
	// what Whisper wants.
	mic, err := micOpen()
	if err != nil {
		msg := "Couldn't open the microphone — say it on your phone, or type it"
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			msg = "No microphone found — say it on your phone, or type it"
		}
		voiceSay(msg, tbxToneWarn, voiceFixPhone)
		PlayErrorSound()
		return
	}
	vkMic.Store(vkMicPresent)
	if cap(voicePage.buf) < voiceMaxBytes {
		voicePage.buf = make([]byte, 0, voiceMaxBytes)
	}
	voicePage.buf = voicePage.buf[:0]
	voicePage.mic = mic
	voicePage.phase = voicePhaseListening
	voicePage.started = sdl.GetTicks64()
	voicePage.level, voicePage.peak = 0, 0
	voicePage.speaking, voicePage.quietSince = false, 0
	voicePage.heard, voicePage.say, voicePage.fix = "", "", voiceFixNone
	voicePage.choices = nil
	voicePage.nav.focus = "stop"
	PlayActivateSound()
	// Leaving the page closes the microphone, whichever way it is left. The
	// visit's context ends when the departure starts (switchScene), while the
	// page is still the current scene for its fade-out, so the check is the
	// capture's generation only — "not on the page any more" was still false
	// then, and the device stayed open, filling its queue, until the next
	// visit. The watcher ends with the capture (capDone).
	gen := voicePage.gen
	done := make(chan struct{})
	voicePage.capDone = done
	runSceneTask(func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		tbxPost(func() {
			if voicePage.gen == gen && voicePage.mic != nil {
				voiceStopCapture()
				voicePage.phase = voicePhaseReady
			}
		})
	})
}

// voiceCloseDevice closes the microphone, if one is open, and ends its
// leave-watcher.
func voiceCloseDevice() {
	if voicePage.mic != nil {
		voicePage.mic.Close()
		voicePage.mic = nil
	}
	if voicePage.capDone != nil {
		close(voicePage.capDone)
		voicePage.capDone = nil
	}
}

// voiceDrainAudio moves what the device captured into the clip and follows
// the level: speech starts the clip, a pause after it starts the quiet clock.
func voiceDrainAudio() {
	if voicePage.mic == nil {
		return
	}
	room := (cap(voicePage.buf) - len(voicePage.buf)) &^ 1
	if room <= 0 {
		return
	}
	start := len(voicePage.buf)
	got := voicePage.mic.Read(voicePage.buf[start : start+room])
	if got <= 0 {
		return
	}
	if sdl.GetTicks64()-voicePage.started < voiceChimeMs {
		// The start chime: neither speech nor part of the clip.
		return
	}
	voicePage.buf = voicePage.buf[:start+got]
	level := vkPCMLevel(voicePage.buf[start : start+got])
	if level > voicePage.peak {
		voicePage.peak = level
	}
	// Fast attack, slow release: the halo jumps with a syllable and settles.
	if level > voicePage.level {
		voicePage.level = level
	} else {
		voicePage.level = voicePage.level*0.82 + level*0.18
	}
	switch {
	case level >= voiceSpeechLevel:
		voicePage.speaking, voicePage.quietSince = true, 0
	case level < voiceQuietLevel && voicePage.speaking && voicePage.quietSince == 0:
		voicePage.quietSince = sdl.GetTicks64()
	}
}

// voiceDrain records one frame's audio and ends the clip by itself: when it is
// full, after a pause that follows speech, or when nothing was said at all.
func voiceDrain(config *Config) {
	if voicePage.mic == nil {
		return
	}
	voiceDrainAudio()
	now := sdl.GetTicks64()
	switch {
	case len(voicePage.buf) >= cap(voicePage.buf):
		voiceFinishClip(config)
	case voicePage.speaking && voicePage.quietSince != 0 && now-voicePage.quietSince >= voiceEndQuietMs:
		voiceFinishClip(config)
	case !voicePage.speaking && now-voicePage.started >= voiceNoSpeechMs:
		voiceStopCapture()
		voiceSay("Didn't hear anything — is the microphone on? Your phone works too", tbxToneWarn, voiceFixPhone)
		PlayErrorSound()
	}
}

// voiceStopCapture closes the microphone and forgets the clip.
func voiceStopCapture() {
	voiceCloseDevice()
	if voicePage.cancel != nil {
		voicePage.cancel()
		voicePage.cancel = nil
	}
	voicePage.gen++
	voicePage.buf = voicePage.buf[:0]
	voicePage.level = 0
}

// voiceFinishClip ends the recording and sends it, unless it was too short or
// silent.
func voiceFinishClip(config *Config) {
	if voicePage.phase != voicePhaseListening {
		return
	}
	voiceDrainAudio()
	voiceCloseDevice()
	ms := len(voicePage.buf) * 1000 / (vkVoiceRate * 2)
	switch {
	case ms < vkVoiceMinMs:
		voiceReset()
		PlayBackSound()
		return
	case voicePage.peak < vkVoiceSilence:
		voicePage.buf = voicePage.buf[:0]
		voiceSay("Didn't hear anything — is the microphone on? Your phone works too", tbxToneWarn, voiceFixPhone)
		PlayErrorSound()
		return
	}
	key := strings.TrimSpace(groqAPIKey())
	wav := vkEncodeWAV(voicePage.buf, vkVoiceRate)
	voicePage.buf = voicePage.buf[:0]
	// The catalog built for Whisper's vocabulary hint resolves the answer too,
	// a moment later: building it walks every Settings row on the main thread.
	targets := voiceTargetsFromApp(config)
	voiceCatalogKeep(targets)
	prompt := voiceWhisperPrompt(targets)
	voicePage.gen++
	gen := voicePage.gen
	voicePage.phase = voicePhaseUnderstanding
	voicePage.nav.focus = "cancel"
	PlayActivateSound()
	// The request belongs to this visit (leaving the page cancels it) and to
	// this press (Cancel stops it through voicePage.cancel).
	reqCtx, cancel := context.WithTimeout(sceneWorkContext(), sttTimeout)
	voicePage.cancel = cancel
	started := runSceneTask(func(context.Context) {
		defer cancel()
		text, err := sttCommandTranscribe(reqCtx, key, wav, prompt)
		tbxPost(func() { voiceTranscribed(gen, text, err) })
	})
	if !started {
		cancel()
		voicePage.cancel = nil
		voiceSay("Could not start the request — try again", tbxToneWarn, voiceFixNone)
	}
}

// voiceTranscribed applies Whisper's answer if it is still the one waited for.
func voiceTranscribed(gen int, text string, err error) {
	if gen != voicePage.gen || voicePage.phase != voicePhaseUnderstanding || !voiceOnPage(appConfig) {
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		msg, fix := voiceErrorText(err)
		voiceSay(msg, tbxToneWarn, fix)
		PlayErrorSound()
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		voiceSay("Didn't catch that — try again", tbxToneWarn, voiceFixNone)
		return
	}
	voiceCommand(appConfig, text, voiceFromMic)
}

// voiceErrorText turns a failed request into a sentence and its fix.
func voiceErrorText(err error) (string, voiceFix) {
	if errors.Is(err, errSTTMemory) {
		return "Close a game to free memory for offline speech, then try again", voiceFixNone
	}
	if errors.Is(err, errSTTUnavailable) {
		return "Offline speech is missing — install it in Settings → Voice & AI", voiceFixNone
	}
	if errors.Is(err, errSTTGroqKey) {
		return "Groq speech needs an API key — add it in Settings", voiceFixGroqKey
	}
	if errors.Is(err, errSTTGroqOffline) {
		return "Groq speech needs Wi-Fi", voiceFixNone
	}
	var gerr *groqError
	if errors.As(err, &gerr) {
		if gerr.status == http.StatusUnauthorized || gerr.status == http.StatusForbidden {
			return "Groq rejected the API key — check it in Settings", voiceFixGroqKey
		}
		return gerr.Error(), voiceFixNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Speech took too long — try again, or say it on your phone", voiceFixPhone
	}
	if msg := plainNetworkError(err); msg == offlinePlainNotice {
		return "Speech needs internet — type it, or use your phone", voiceFixType
	}
	return "Speech failed — try again", voiceFixNone
}

// voiceTranscribe sends a clip to Groq's Whisper, the way voice typing does
// (vkTranscribe), with English as the language — the commands are English —
// and prompt as the vocabulary hint.
func voiceTranscribe(ctx context.Context, key string, wav []byte, prompt string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="command.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	fields := [][2]string{{"model", vkVoiceModel}, {"response_format", "json"}, {"temperature", "0"}, {"language", "en"}}
	if prompt != "" {
		fields = append(fields, [2]string{"prompt", prompt})
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

// ── Running a command ────────────────────────────────────────────────────

// voiceCatalogTTL is how long the catalog built for a clip's vocabulary hint
// may stand in for a fresh one when its answer comes back (Whisper answers in
// a second or two).
const voiceCatalogTTL = 15000 // ms

// voiceCatalogCache is that catalog, used once. Main thread only.
var voiceCatalogCache struct {
	targets []voiceTarget
	at      uint64
}

// voiceCatalogKeep keeps the catalog a clip's hint was built from.
func voiceCatalogKeep(targets []voiceTarget) {
	voiceCatalogCache.targets, voiceCatalogCache.at = targets, sdl.GetTicks64()
}

// voiceCatalog is what a name is resolved against: the kept catalog when the
// microphone path just built one, else a fresh one.
func voiceCatalog(config *Config) []voiceTarget {
	kept, at := voiceCatalogCache.targets, voiceCatalogCache.at
	voiceCatalogCache.targets = nil
	if kept != nil && sdl.GetTicks64()-at < voiceCatalogTTL {
		return kept
	}
	return voiceTargetsFromApp(config)
}

// voiceClip bounds a command's length (a paste is not a command).
func voiceClip(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > voiceMaxCommandRunes {
		text = string([]rune(text)[:voiceMaxCommandRunes])
	}
	return text
}

// voiceCommand runs one command heard, typed or sent from the phone while the
// page is on screen, and shows what it did.
func voiceCommand(config *Config, text string, src voiceSource) {
	text = voiceClip(text)
	if text == "" {
		voiceReset()
		return
	}
	in := voiceParse(text)
	out := voiceExecute(config, in, src)
	voicePage.heard = text
	logDebug("voice", "command understood as intent %d", int(in.Kind))
	voiceShow(config, out, src)
	ttsSpeakPageOutcome(in.Kind, out, src) // the time, the date… out loud (tts_api.go)
}

// voiceShow puts an outcome on the page: the answer, the choices, or the
// pause before its action runs.
func voiceShow(config *Config, out voiceOutcome, src voiceSource) {
	voicePage.search, voicePage.web = out.Search, out.Web
	switch {
	case out.Cancel:
		voiceReset()
		PlayBackSound()
	case len(out.Choices) > 0:
		voicePage.phase = voicePhaseChoices
		voicePage.say, voicePage.tone, voicePage.fix = out.Say, out.Tone, voiceFixNone
		voicePage.choices = out.Choices
		voicePage.act, voicePage.leave = nil, false
		voicePage.nav.focus = "c0"
		voicePage.nav.scroll = 0
		PlayNavSound()
	case out.Act != nil:
		voicePage.phase = voicePhaseConfirm
		voicePage.say, voicePage.tone, voicePage.fix = out.Say, tbxToneAccent, voiceFixNone
		voicePage.choices = nil
		voicePage.act, voicePage.leave = out.Act, out.Leave
		voicePage.confirmAt = sdl.GetTicks64() + voiceConfirmMs
		voicePage.nav.focus = "now"
		if src == voiceFromPhone {
			// The phone already shows the answer; there is nobody at the
			// handheld to cancel.
			voiceRunPending(config)
			return
		}
		PlaySuccessSound()
	default:
		tone := out.Tone
		if out.Done && tone == tbxToneNeutral {
			tone = tbxToneGood
		}
		fix := out.Fix
		voiceSay(out.Say, tone, fix)
		voicePage.search, voicePage.web = out.Search, out.Web
		if out.Search != "" {
			voicePage.nav.focus = "search"
		}
		if out.Done {
			trophyEvent(tkVoiceCmd)
			PlaySuccessSound()
		} else if tone == tbxToneWarn || tone == tbxToneBad {
			PlayErrorSound()
		}
	}
}

// voiceRunPending runs the confirmed action. A navigation must not leave the
// voice page on the scene history (B on the new screen goes back to where the
// user was before they spoke), and an action about the screen behind the page
// (a screenshot, the screensaver) runs once the page is gone.
func voiceRunPending(config *Config) {
	act, leave := voicePage.act, voicePage.leave
	voicePage.act, voicePage.leave = nil, false
	voicePage.phase = voicePhaseReady
	voicePage.say = ""
	if act == nil {
		return
	}
	trophyEvent(tkVoiceCmd)
	if config == nil {
		config = appConfig
	}
	if !voiceOnPage(config) {
		act(config)
		return
	}
	if leave {
		goBackScene(config)
		voiceAfterTransition(act)
		return
	}
	voiceIdx := currentSceneIndex
	act(config)
	voiceDropFromHistory(voiceIdx)
}

// voiceDropFromHistory takes the voice page off the top of the scene history
// when the action just navigated away from it.
func voiceDropFromHistory(voiceIdx int) {
	if transitionPhase == "none" || pendingSceneIndex == voiceIdx {
		return
	}
	if n := len(sceneHistory); n > 0 && sceneHistory[n-1] == voiceIdx {
		sceneHistory = sceneHistory[:n-1]
	}
}

// voiceAfterTransition runs act on the main loop once the running scene
// transition is through, checking once a frame for at most about two seconds.
func voiceAfterTransition(act func(*Config)) {
	tries := 0
	var step func()
	step = func() {
		if transitionPhase != "none" && tries < 120 {
			tries++
			tbxPost(step)
			return
		}
		act(appConfig)
	}
	tbxPost(step)
}

// voiceRunChoice opens one of the listed matches.
func voiceRunChoice(config *Config, i int) {
	if i < 0 || i >= len(voicePage.choices) {
		return
	}
	out := voiceOutcomeFor(voicePage.choices[i])
	voicePage.choices = nil
	if out.Act == nil {
		voiceSay(out.Say, out.Tone, voiceFixNone)
		PlayErrorSound()
		return
	}
	// A pick is already the confirmation: no pause.
	voicePage.act, voicePage.leave = out.Act, out.Leave
	PlayActivateSound()
	voiceRunPending(config)
}

// voiceTypePrompt opens the keyboard for a typed command.
func voiceTypePrompt(config *Config) {
	voiceStopCapture()
	if voicePage.phase == voicePhaseListening || voicePage.phase == voicePhaseUnderstanding || voicePage.phase == voicePhaseConfirm {
		voiceReset()
	}
	PlayActivateSound()
	tbxOpenPrompt("Type a command", "open YouTube · volume 50 · what time is it", "", func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		voiceCommand(appConfig, text, voiceFromTyped)
	})
}

// voiceRunFix performs the error's repair.
func voiceRunFix(config *Config) {
	switch voicePage.fix {
	case voiceFixGroqKey:
		voiceReset()
		vkVoiceOpenKeySettings(config)
	case voiceFixPhone:
		voiceOpenPhoneSheet(config)
	case voiceFixType:
		voiceTypePrompt(config)
	}
}

// voiceFixLabel names the fix button.
func voiceFixLabel(fix voiceFix) string {
	switch fix {
	case voiceFixGroqKey:
		return "Add Groq key"
	case voiceFixPhone:
		return "Use my phone"
	case voiceFixType:
		return "Type it"
	}
	return ""
}

// ── Phone ────────────────────────────────────────────────────────────────

// voicePhoneURL is the companion's /voice page with this session's PIN, or ""
// while the companion has no address. In screenshot mode companionURL has
// already put in the stand-in address and PIN.
func voicePhoneURL() string {
	u := companion.url
	if i := strings.Index(u, "/?p="); i >= 0 {
		return u[:i] + "/voice?p=" + u[i+len("/?p="):]
	}
	return ""
}

// voiceOpenPhoneSheet shows the QR code for the phone page.
func voiceOpenPhoneSheet(config *Config) {
	companionEnsureStarted()
	companionStart()
	url := voicePhoneURL()
	spec := tbxSheetSpec{
		title:    "Say it on your phone",
		subtitle: "Your phone's keyboard microphone sends the command",
		icon:     "phone",
	}
	if url == "" {
		why := strings.TrimSpace(companion.err)
		if why == "" {
			why = "The phone companion is not running"
		}
		spec.body = why + ".\n\nConnect the handheld to Wi-Fi, then open this again."
		spec.buttons = []tbxSheetButton{{label: "Close", primary: true}}
		tbxOpenSheet(config, spec)
		PlayErrorSound()
		return
	}
	spec.qr = tbxQRModules(url)
	spec.qrLabel = "Scan with the phone's camera"
	spec.body = "• Scan the code with a phone on the same Wi-Fi.\n" +
		"• Tap the text field, then the microphone on the phone's keyboard, and say what to open — " +
		"“open YouTube”, “play Mario Kart”, “volume 50”.\n" +
		"• The command runs here and the phone shows what happened.\n\n" +
		"No Groq key is needed: the phone turns the speech into words."
	spec.buttons = []tbxSheetButton{{label: "Done", primary: true}}
	tbxOpenSheet(config, spec)
	PlayActivateSound()
}

// ── Input ────────────────────────────────────────────────────────────────

// voiceOnAction is the page's input once the sheet has had its turn.
func voiceOnAction(config *Config, act tbxAction) bool {
	switch act {
	case tbxActBack:
		switch voicePage.phase {
		case voicePhaseListening, voicePhaseUnderstanding, voicePhaseConfirm, voicePhaseChoices:
			voiceReset()
			PlayBackSound()
			return true
		}
		voiceStopCapture()
		return false // tbxDispatch leaves the page
	case tbxActX:
		if voicePage.phase != voicePhaseListening {
			voiceTypePrompt(config)
		}
		return true
	case tbxActY:
		// The sheet covers the page: nothing may keep listening or count
		// down behind it.
		switch voicePage.phase {
		case voicePhaseListening, voicePhaseUnderstanding, voicePhaseConfirm:
			voiceReset()
		}
		voiceOpenPhoneSheet(config)
		return true
	case tbxActConfirm:
		voiceActivate(config, voicePage.nav.focus)
		return true
	}
	return tbxNavAction(&voicePage.nav, act)
}

// voiceActivate presses the control with id.
func voiceActivate(config *Config, id string) {
	switch {
	case id == "speak" || id == "again" || id == "stop" || id == "mic":
		voiceSpeakOrStop(config)
	case id == "type":
		voiceTypePrompt(config)
	case id == "phone":
		voiceOpenPhoneSheet(config)
	case id == "shortcuts":
		// The words the user taught (voiceshortcuts_ui.go).
		voiceStopCapture()
		vscOpen(config)
	case id == "fix":
		voiceRunFix(config)
	case id == "cancel":
		voiceReset()
		PlayBackSound()
	case id == "now":
		voiceRunPending(config)
	case id == "search":
		words := voicePage.search
		voiceReset()
		PlayActivateSound()
		goBackScene(config)
		voiceAfterTransition(func(c *Config) { voiceOpenSearchSheet(c, words) })
	case id == "web":
		words := voicePage.web
		voiceReset()
		url := homeSearchWebURL(words)
		voicePage.act = func(c *Config) { openBrowserAtURL(c, url) }
		voiceRunPending(config)
	default:
		if i, ok := voiceChoiceIndex(id); ok {
			voiceRunChoice(config, i)
		}
	}
}

// voiceSceneClick routes a click or a tap on the page (main.go).
func voiceSceneClick(mx, my int32, config *Config) bool {
	if !voiceOnPage(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	if voicePage.micRect.W > 0 && pointInRect(mx, my, voicePage.micRect) {
		voiceSpeakOrStop(config)
		return true
	}
	if it, ok := voicePage.nav.hit(mx, my); ok && !it.disabled {
		voicePage.nav.focus = it.id
		voiceActivate(config, it.id)
		return true
	}
	return false
}

// voiceSceneWheel scrolls the page (a long choice list), or its open sheet
// (main.go's wheel chain).
func voiceSceneWheel(config *Config, y int32) bool {
	if y == 0 || !voiceOnPage(config) {
		return false
	}
	if tbxSheetVisible(config) {
		if y > 0 {
			tbxSheetAction(tbxActUp)
		} else {
			tbxSheetAction(tbxActDown)
		}
		return true
	}
	voicePage.nav.scrollBy(-y * 48)
	return true
}

// voiceFooterHints is the footer while the page is up (extras.go).
func voiceFooterHints(config *Config) []FooterHint {
	if tbxSheetVisible(config) {
		return tbxSheetHints()
	}
	switch voicePage.phase {
	case voicePhaseListening:
		return []FooterHint{{Button: "A", Label: "Done"}, {Button: "B", Label: "Cancel"}}
	case voicePhaseUnderstanding:
		return []FooterHint{{Button: "B", Label: "Cancel"}}
	case voicePhaseConfirm:
		return []FooterHint{{Button: "A", Label: "Now"}, {Button: "B", Label: "Cancel"}}
	case voicePhaseChoices:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Cancel"}}
	}
	return []FooterHint{{Button: "A", Label: voiceFocusLabel()}, {Button: "X", Label: "Type"}, {Button: "Y", Label: "Phone"}, {Button: "B", Label: "Back"}}
}

// voiceFocusLabel names what A presses: the focused button.
func voiceFocusLabel() string {
	switch voicePage.nav.focus {
	case "type":
		return "Type"
	case "phone":
		return "Phone"
	case "shortcuts":
		return "Shortcuts"
	case "fix":
		if label := voiceFixLabel(voicePage.fix); label != "" {
			return label
		}
	case "search":
		return "Search"
	case "web":
		return "Search web"
	}
	return "Speak"
}

// ── Frame tick ───────────────────────────────────────────────────────────

// voiceTick runs once a frame while the page is up: it records, ends the
// confirm pause, and keeps the frame rate up while something moves. The page
// is still drawn while it fades out and under an overlay; neither is a moment
// to keep listening or to fire a pending command behind the screen the user
// is looking at.
func voiceTick(config *Config) {
	if transitionPhase == "fade-out" {
		// The page is being left (the Start menu, the back pill, a search
		// result, a phone command that navigated): the microphone closes now
		// and a counted-down command is dropped. An action this page started
		// itself was taken off it before the departure began.
		if voicePage.mic != nil || voicePage.phase == voicePhaseListening || voicePage.phase == voicePhaseConfirm {
			voiceReset()
		}
		return
	}
	switch voicePage.phase {
	case voicePhaseListening:
		if voiceCovered() {
			voiceReset()
			break
		}
		voiceDrain(config)
	case voicePhaseConfirm:
		if voiceCovered() {
			// Something else took the screen during the pause: the user moved
			// on, so the command does not run behind it, now or later.
			voiceReset()
			break
		}
		if sdl.GetTicks64() >= voicePage.confirmAt {
			voiceRunPending(config)
		}
	}
	switch voicePage.phase {
	case voicePhaseListening, voicePhaseUnderstanding, voicePhaseConfirm:
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
	}
}

// voiceCovered reports whether something covers the page: the Start menu,
// Quick Settings, the search sheet, a key sheet, a modal panel or the
// on-screen keyboard (modalPanelBlockReason lists the panels).
func voiceCovered() bool {
	return tb.startOpen || qs.open || globalSearchOpen || shortcutsOpen || controlsLegend.open || wpOpen ||
		modalPanelBlockReason() != ""
}

// ── Drawing ──────────────────────────────────────────────────────────────

// voiceHeaderStatus is the header's read-out: whether the microphone can be
// used.
func voiceHeaderStatus() (string, sdl.Color) {
	switch vkMic.Load() {
	case vkMicPresent:
		if !voicePage.hasKey {
			return "Speech recognition unavailable", tbxToneColor(tbxToneWarn)
		}
		return "Microphone ready", tbxToneColor(tbxToneGood)
	case vkMicAbsent:
		return "No microphone · phone works", tbxToneColor(tbxToneWarn)
	}
	return "", sdl.Color{}
}

// voiceStateLine is the line under the microphone.
func voiceStateLine() string {
	switch voicePage.phase {
	case voicePhaseListening:
		secs := int((sdl.GetTicks64() - voicePage.started) / 1000)
		if voicePage.speaking {
			return "Listening… " + strconv.Itoa(secs) + " s — " + hintSentence("stop talking or press %s", "A", "Done")
		}
		return "Listening… say what to open"
	case voicePhaseUnderstanding:
		return "Understanding…"
	case voicePhaseChoices, voicePhaseConfirm, voicePhaseAnswer:
		if voicePage.heard != "" {
			return "“" + voicePage.heard + "”"
		}
	}
	return hintSentence("Press %s and say what to open", "A", "Speak")
}

// voiceRender draws the page.
func voiceRender(renderer *sdl.Renderer, config *Config) {
	if voicePage.visit.begin() {
		voiceEnter()
	}
	voiceTick(config)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status, statusColor := voiceHeaderStatus()
	header := tbxHeader("Voice command", "Say what to open — screens, apps, games, settings, music", "mic", status, statusColor)
	f := tbxBeginFrame(renderer, config, header)
	body := f.bodyRect()
	nav := &voicePage.nav
	nav.begin(body.Y, body.H)

	cx := body.X + body.W/2
	radius := voiceMicRadius
	if voicePage.phase == voicePhaseChoices {
		radius = voiceMicRadiusSmall
	}
	// Layout in body coordinates (y = 0 is the top of the viewport).
	micCY := radius + 18
	stateY := micCY + radius + 20
	sayY := stateY + fonts.titleH + 10
	buttonsY := sayY + fonts.bodyH + 22
	const buttonH = int32(46)

	// The buttons for this phase, left to right (a fixed array: nothing is
	// allocated per frame).
	var btnBuf [6]voiceButton
	btns := btnBuf[:0]
	switch voicePage.phase {
	case voicePhaseListening:
		btns = append(btns, voiceButton{"stop", "Done", true}, voiceButton{"cancel", "Cancel", false})
	case voicePhaseUnderstanding:
		btns = append(btns, voiceButton{"cancel", "Cancel", false})
	case voicePhaseConfirm:
		btns = append(btns, voiceButton{"now", "Go now", true}, voiceButton{"cancel", "Cancel", false})
	case voicePhaseChoices:
		btns = append(btns, voiceButton{"again", "Speak again", false}, voiceButton{"type", "Type", false},
			voiceButton{"cancel", "Cancel", false})
	default:
		if label := voiceFixLabel(voicePage.fix); label != "" {
			btns = append(btns, voiceButton{"fix", label, true})
		}
		if voicePage.search != "" {
			btns = append(btns, voiceButton{"search", "Search for it", voicePage.fix == voiceFixNone})
			if voicePage.web != "" {
				btns = append(btns, voiceButton{"web", "Search the web", false})
			}
		}
		btns = append(btns, voiceButton{"speak", "Speak", voicePage.fix == voiceFixNone && voicePage.search == ""})
		// The fix button already is "Type it" or "Use my phone": no twin.
		if voicePage.fix != voiceFixType {
			btns = append(btns, voiceButton{"type", "Type", false})
		}
		if voicePage.fix != voiceFixPhone {
			btns = append(btns, voiceButton{"phone", "Use my phone", false})
		}
		// Only beside Speak, Type and Phone: the row (and widthBuf) holds six.
		if voicePage.fix == voiceFixNone && voicePage.search == "" {
			btns = append(btns, voiceButton{"shortcuts", "Shortcuts", false})
		}
	}
	totalW := int32(0)
	var widthBuf [6]int32
	widths := widthBuf[:len(btns)]
	for i, b := range btns {
		widths[i] = tbxButtonWidth(fonts.body, b.label, 132)
		totalW += widths[i]
	}
	if len(btns) > 1 {
		totalW += int32(len(btns)-1) * 14
	}
	bx := cx - totalW/2
	for i, b := range btns {
		nav.add(b.id, sdl.Rect{X: bx, Y: buttonsY, W: widths[i], H: buttonH}, 0, false)
		bx += widths[i] + 14
	}
	y := buttonsY + buttonH + 24
	rowH := tbxRowHeight(fonts)
	if voicePage.phase == voicePhaseChoices {
		for i := range voicePage.choices {
			if i >= len(voiceChoiceIDs) {
				break
			}
			nav.add(voiceChoiceIDs[i], sdl.Rect{X: body.X, Y: y, W: body.W, H: rowH}, i+1, false)
			y += rowH + 10
		}
	}
	nav.extend(y)
	nav.end()

	tbxClip(renderer, body)
	off := body.Y - nav.scroll
	// The microphone: a glass disc that glows while it listens, its halo
	// following the voice.
	mcy := off + micCY
	accent := glassAccent()
	if voicePage.phase == voicePhaseListening {
		halo := radius + 6 + int32(voicePage.level*160)
		if halo > radius+46 {
			halo = radius + 46
		}
		fillCircle(renderer, cx, mcy, halo, WithAlpha(accent, 40))
		fillCircle(renderer, cx, mcy, radius+(halo-radius)/2, WithAlpha(accent, 70))
		fillCircle(renderer, cx, mcy, radius, WithAlpha(accent, 230))
	} else {
		fillCircle(renderer, cx, mcy, radius, glassA(34))
		if voicePage.phase == voicePhaseUnderstanding || voicePage.phase == voicePhaseConfirm {
			strokeCircle(renderer, cx, mcy, radius+4, WithAlpha(accent, 200))
		}
	}
	strokeCircle(renderer, cx, mcy, radius, glassA(90))
	icon := radius
	glyphColor := ColorTextPrimary()
	if voicePage.phase == voicePhaseListening {
		glyphColor = sdl.Color{R: 255, G: 255, B: 255, A: 255}
	}
	drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: cx - icon/2, Y: mcy - icon/2, W: icon, H: icon}, glyphColor)
	// Only the visible part of the disc takes a click: scrolled under the
	// header, a tap there is the header's.
	disc := sdl.Rect{X: cx - radius, Y: mcy - radius, W: 2 * radius, H: 2 * radius}
	if hit, ok := disc.Intersect(&body); ok {
		voicePage.micRect = hit
	} else {
		voicePage.micRect = sdl.Rect{}
	}

	// The state line, then the answer.
	line := ellipsize(fonts.title, voiceStateLine(), body.W-40)
	lw, _ := measureText(fonts.title, line)
	renderText(renderer, config, fonts.title, line, ColorTextPrimary(), cx-lw/2, off+stateY)
	if voicePage.say != "" {
		col := ColorTextSecondary()
		switch voicePage.tone {
		case tbxToneWarn, tbxToneBad:
			col = tbxToneColor(voicePage.tone)
		case tbxToneGood, tbxToneAccent:
			col = ColorTextPrimary()
		}
		say := ellipsize(fonts.body, voicePage.say, body.W-40)
		sw, _ := measureText(fonts.body, say)
		renderText(renderer, config, fonts.body, say, col, cx-sw/2, off+sayY)
	}
	// The confirm pause as a shrinking bar under the answer.
	if voicePage.phase == voicePhaseConfirm {
		left := int64(voicePage.confirmAt) - int64(sdl.GetTicks64())
		if left < 0 {
			left = 0
		}
		barW := int32(240)
		fill := int32(int64(barW) * left / voiceConfirmMs)
		bar := sdl.Rect{X: cx - barW/2, Y: off + sayY + fonts.bodyH + 6, W: barW, H: 4}
		fillRoundedRect(renderer, bar.X, bar.Y, bar.W, bar.H, 2, glassA(30))
		if fill > 0 {
			fillRoundedRect(renderer, bar.X, bar.Y, fill, bar.H, 2, accent)
		}
	}
	for _, it := range nav.items {
		r := nav.screenRect(it)
		i, isChoice := voiceChoiceIndex(it.id)
		switch {
		case isChoice:
			if voicePage.phase != voicePhaseChoices || i >= len(voicePage.choices) {
				continue
			}
			m := voicePage.choices[i]
			spec := tbxRowSpec{icon: m.Target.Icon, title: m.Target.Title, subtitle: m.Target.Where}
			if spec.icon == "" {
				spec.icon = "search"
			}
			if m.Target.Blocked != "" {
				spec.pill, spec.tone, spec.dim = homeSearchNeedsInternet, tbxToneWarn, true
			}
			tbxDrawRow(renderer, config, fonts, r, spec, nav.focused(it.id))
		default:
			for _, b := range btns {
				if b.id == it.id {
					tbxDrawButton(renderer, config, fonts.body, r, b.label, b.primary, nav.focused(it.id), false)
					break
				}
			}
		}
	}
	// What to say, while there is room for it.
	if voicePage.phase == voicePhaseReady || (voicePage.phase == voicePhaseAnswer && voicePage.tone != tbxToneWarn) {
		tip := "Try “open YouTube”, “play Mario Kart”, “volume 50”, “brighter”, “take a screenshot”, “turn off Wi-Fi”, “next song” or “what time is it”."
		lines := voicePage.wrap.get(fonts.small, tip, body.W-120)
		ty := off + buttonsY + buttonH + 26
		for _, l := range lines {
			tw, _ := measureText(fonts.small, l)
			renderText(renderer, config, fonts.small, l, ColorTextTertiary(), cx-tw/2, ty)
			ty += fonts.smallH + 4
		}
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, body.X+body.W-6, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}
