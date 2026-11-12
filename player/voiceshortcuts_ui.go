package main

// voiceshortcuts_ui.go — the Voice shortcuts page: teach the voice assistant
// your own words (voiceshortcuts.go keeps and matches them).
//
// Settings → Voice & AI → Voice shortcuts, the Voice command page's Shortcuts
// button and "open voice shortcuts" said to either voice lead here. The page
// lists the words taught so far, each with what it does; A changes one, X
// deletes it (after a question), Y tries it as if it had been said. "Teach a
// word" is three short steps:
//
//  1. the word — said into the microphone the way it will be said later
//     (Whisper writes it down, so it is spelt the way Whisper will hear it
//     next time), or typed;
//  2. what it opens — everything a voice command can open, in Home search's
//     groups with the emulators first, narrowed by a filter; or "A voice
//     command…" (said or typed — the page shows what JukaHub makes of it) or
//     "A web page…";
//  3. a check — what it will do, and a warning when the word is also a
//     built-in command or another shortcut's — then Save.
//
// Drawn in the tool screens' glass (tbxui.go) like the Voice command page. The
// microphone is the shared one (voicemic.go), open only while the page
// listens and closed the moment the page is left or covered, the voice orb
// included. Main thread only.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// vscSceneName is the page's scene, injected in memory like the Voice command
// page's (ensureVoiceScene calls vscEnsureScene).
const vscSceneName = "VoiceShortcuts"

// vscStep is where the page is.
type vscStep int

const (
	vscStepList    vscStep = iota
	vscStepWord            // 1: what the user will say
	vscStepPick            // 2: what it opens
	vscStepCommand         // 2: a voice command instead
	vscStepCheck           // 3: check and save
)

// vscGroup is one group of the pick list.
type vscGroup struct{ id, label string }

// vscGroups are the pick list's groups in tab order: the emulators first, which
// is what the tester asked for.
var vscGroups = [...]vscGroup{
	{"all", "All"}, {"emu", "Emulators"}, {"screen", "Screens"}, {"app", "Apps"},
	{"game", "Games"}, {"setting", "Settings"}, {"media", "Media"}, {"file", "Files"},
}

// vscPickCap is how many rows the pick list shows at once; the filter finds
// the rest (a card can hold thousands of games).
const vscPickCap = 60

// vscRecorder is a recording in progress, or the clip waiting for Whisper.
type vscRecorder struct {
	mic        *micStream
	buf        []byte
	on         bool // the microphone is open
	waiting    bool // the clip is with Whisper
	started    uint64
	level      float64
	peak       float64
	speaking   bool
	quietSince uint64
	// capDone ends the watcher that closes the microphone when the page is
	// left mid-listen; gen invalidates an answer for an earlier clip.
	capDone chan struct{}
	gen     int
	cancel  context.CancelFunc
}

// vscPageState is the page.
type vscPageState struct {
	visit tbxVisit
	nav   tbxNav
	step  vscStep
	// list is the saved shortcuts; descs and missing are each one's line and
	// whether what it opens is gone, worked out when the list changes rather
	// than every frame.
	list    []voiceShortcut
	descs   []string
	missing []bool
	// draft is the shortcut being taught or changed; editing is its index in
	// list (-1 for a new one). fromCheck sends Back and Next to the check step
	// when a step was opened from there.
	draft     voiceShortcut
	editing   int
	fromCheck bool
	// targets is the voice catalog, built once per visit and again when the
	// background index publishes a scan (indexGen is the scan it saw).
	targets  []voiceTarget
	indexGen uint64
	// The pick list: its group, its filter, the rows shown and how many the
	// cap left out.
	group      int
	filter     string
	picks      []voiceTarget
	more       int
	picksDirty bool
	// command is the voice command on the command step, preview what it does.
	command, preview string
	// checkWhat and checkNote are the check step's lines, worked out when the
	// step opens: the clash test ranks the whole catalog.
	checkWhat, checkNote string
	// msg is the line under the microphone (or over the list): what was
	// heard, or what went wrong.
	msg  string
	tone tbxTone
	rec  vscRecorder
	// micRect is the microphone disc as drawn, for a click.
	micRect sdl.Rect
	wrap    tbxWrapCache
}

var vscPage = vscPageState{editing: -1}

// The focus ids of the rows, made once.
var (
	vscListIDs  [voiceShortcutMax]string
	vscPickIDs  [vscPickCap]string
	vscGroupIDs [len(vscGroups)]string
)

type vscScene struct{ sceneBase }

func (vscScene) render(r *sdl.Renderer, c *Config) bool {
	vscRender(r, c)
	return true
}

func (vscScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), vscOnAction)
}

func (vscScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), vscOnAction)
}

func init() {
	registerScene(vscScene{sceneBase{name: vscSceneName}})
	for i := range vscListIDs {
		vscListIDs[i] = "s" + strconv.Itoa(i)
	}
	for i := range vscPickIDs {
		vscPickIDs[i] = "p" + strconv.Itoa(i)
	}
	for i := range vscGroupIDs {
		vscGroupIDs[i] = "g" + strconv.Itoa(i)
	}
}

// vscIDIndex is the position of id in ids, or -1.
func vscIDIndex(id string, ids []string) int {
	for i := range ids {
		if ids[i] == id {
			return i
		}
	}
	return -1
}

// vscEnsureScene gives the page its scene, in memory. Idempotent.
func vscEnsureScene(config *Config) {
	if config == nil {
		return
	}
	if sceneIndexExact(config, vscSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: vscSceneName})
	}
	for i := range config.Scenes {
		if config.Scenes[i].Name == vscSceneName {
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// vscOpen opens the page (the Settings row, the Voice command page, a voice
// command).
func vscOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	vscEnsureScene(config)
	idx := sceneIndexExact(config, vscSceneName)
	if idx < 0 {
		return
	}
	if globalSearchOpen {
		homeSearchClose()
	}
	// Nothing may stay up over the page (tbxOpenScene's rule).
	powerTools.open = false
	communityPanel.open = false
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	PlayActivateSound()
	changeSceneTo(config, idx)
}

// vscOnPage reports whether the page is on screen.
func vscOnPage(config *Config) bool { return tbxCurrentScene(config) == vscSceneName }

// ── Visits and steps ─────────────────────────────────────────────────────

// vscEnter runs once per visit: the saved list, a fresh catalog, the list
// step.
func vscEnter(config *Config) {
	vscStopCapture()
	p := &vscPage
	p.step = vscStepList
	p.editing, p.fromCheck = -1, false
	p.draft = voiceShortcut{}
	p.group, p.filter = 0, ""
	p.command, p.preview = "", ""
	p.msg, p.tone = "", tbxToneNeutral
	p.nav.scroll = 0
	vkMicProbeStart()
	// The ROM and file index refreshes in the background when stale, so the
	// games and files to pick from are the card's.
	homeSearchRefreshIndex(config)
	vscRefresh(config)
	p.nav.focus = "add"
	if len(p.list) > 0 {
		p.nav.focus = vscListIDs[0]
	}
}

// vscRefresh rebuilds the catalog and re-reads the list with its lines.
func vscRefresh(config *Config) {
	p := &vscPage
	p.targets = voiceTargetsFromApp(config)
	p.list = voiceShortcutsLoad(config)
	p.descs = p.descs[:0]
	p.missing = p.missing[:0]
	p.indexGen = homeSearchIdx.gen.Load()
	for _, sc := range p.list {
		// A command's row names the command's reading only; ranking the whole
		// catalog for each one waits for the check step.
		var targets []voiceTarget
		if sc.Kind != voiceShortcutCommand {
			targets = p.targets
		}
		p.descs = append(p.descs, voiceShortcutDescribe(sc, targets))
		gone := false
		if sc.Kind != voiceShortcutCommand && sc.Kind != voiceShortcutWeb {
			_, ok := voiceShortcutResolve(sc, p.targets)
			gone = !ok
		}
		p.missing = append(p.missing, gone)
	}
	p.picksDirty = true
}

// vscGoStep moves to step with that step's first control focused.
func vscGoStep(step vscStep) {
	vscStopCapture()
	p := &vscPage
	p.step = step
	p.msg, p.tone = "", tbxToneNeutral
	p.nav.scroll = 0
	switch step {
	case vscStepList:
		p.editing, p.fromCheck = -1, false
		p.nav.focus = "add"
		if len(p.list) > 0 {
			p.nav.focus = vscListIDs[0]
		}
	case vscStepWord:
		p.nav.focus = "say"
		if p.draft.Phrase != "" {
			p.nav.focus = "next"
		}
	case vscStepPick:
		if homeSearchIdx.gen.Load() != p.indexGen {
			vscRefresh(appConfig)
		}
		p.picksDirty = true
		p.nav.focus = "p0"
	case vscStepCommand:
		p.nav.focus = "say"
		if p.command != "" {
			p.nav.focus = "use"
		}
	case vscStepCheck:
		p.nav.focus = "save"
		vscCheckLines()
	}
}

// vscCheckLines works out what the draft will do and what the check step
// warns about: a target that is gone, a word that something else answers to.
func vscCheckLines() {
	p := &vscPage
	p.checkWhat = voiceShortcutDescribe(p.draft, p.targets)
	if p.draft.Kind == voiceShortcutCommand {
		p.checkWhat = "Run “" + p.draft.Command + "” — " + vscLowerFirst(p.checkWhat)
	}
	var notes []string
	if p.draft.Kind != voiceShortcutCommand && p.draft.Kind != voiceShortcutWeb {
		if _, ok := voiceShortcutResolve(p.draft, p.targets); !ok {
			notes = append(notes, "That is not on this handheld any more — change what it opens")
		}
	}
	if clash := voiceShortcutClash(p.list, p.draft.Phrase, p.editing, p.targets); clash != "" {
		notes = append(notes, clash)
	}
	p.checkNote = strings.Join(notes, "\n")
}

// vscLowerFirst lower-cases the first letter of s ("Sets the volume" → "sets
// the volume").
func vscLowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// vscSay shows a line under the microphone (or over the list).
func vscSay(msg string, tone tbxTone) {
	vscPage.msg, vscPage.tone = msg, tone
}

// vscStartTeach is "Teach a word".
func vscStartTeach() {
	p := &vscPage
	if len(p.list) >= voiceShortcutMax {
		vscSay("50 words is the most — delete one first", tbxToneWarn)
		PlayErrorSound()
		return
	}
	p.editing, p.fromCheck = -1, false
	p.draft = voiceShortcut{}
	p.command, p.preview = "", ""
	p.filter, p.group = "", 0
	PlayActivateSound()
	vscGoStep(vscStepWord)
}

// vscEdit opens the check step on a saved shortcut.
func vscEdit(i int) {
	p := &vscPage
	if i < 0 || i >= len(p.list) {
		return
	}
	p.editing, p.fromCheck = i, false
	p.draft = p.list[i]
	p.command, p.preview = "", ""
	if p.draft.Kind == voiceShortcutCommand {
		p.command = p.draft.Command
		p.preview = voiceCommandPreview(p.command, p.targets)
	}
	PlayActivateSound()
	vscGoStep(vscStepCheck)
}

// vscBack is B on a step: one step back, the check step when a step came from
// there, the list from the check step.
func vscBack() {
	p := &vscPage
	if p.rec.on || p.rec.waiting {
		vscStopCapture()
		vscSay("", tbxToneNeutral)
		PlayBackSound()
		return
	}
	PlayBackSound()
	switch p.step {
	case vscStepWord:
		if p.fromCheck {
			vscGoStep(vscStepCheck)
			return
		}
		vscGoStep(vscStepList)
	case vscStepPick:
		if p.fromCheck {
			vscGoStep(vscStepCheck)
			return
		}
		vscGoStep(vscStepWord)
	case vscStepCommand:
		vscGoStep(vscStepPick)
	default:
		vscGoStep(vscStepList)
	}
}

// vscTidyHeard cleans what Whisper wrote for a word: its full stop, the
// quotes and the spaces around it.
func vscTidyHeard(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, " .!?,;:\"'“”‘’…")
	return strings.Join(strings.Fields(text), " ")
}

// vscSetWord takes a said or typed word for the draft.
func vscSetWord(text string) {
	text = vscTidyHeard(text)
	if text == "" {
		return
	}
	if why := voiceShortcutPhraseProblem(text); why != "" {
		vscSay(why, tbxToneWarn)
		PlayErrorSound()
		return
	}
	vscPage.draft.Phrase = text
	vscSay("", tbxToneNeutral)
	vscPage.nav.focus = "next"
	PlaySuccessSound()
}

// vscSetCommand takes a said or typed command and shows what it would do.
func vscSetCommand(text string) {
	text = voiceClip(vscTidyHeard(text))
	if text == "" {
		return
	}
	switch voiceParse(text).Kind {
	case voiceIntentNone, voiceIntentCancel:
		vscPage.command, vscPage.preview = "", ""
		vscSay("JukaHub does not read “"+text+"” as a command — try “brightness 10” or “open YouTube”", tbxToneWarn)
		PlayErrorSound()
		return
	}
	vscPage.command = text
	vscPage.preview = voiceCommandPreview(text, vscPage.targets)
	vscSay("", tbxToneNeutral)
	vscPage.nav.focus = "use"
	PlaySuccessSound()
}

// vscNext leaves the word step: to the check step when it came from there,
// else to what the word opens.
func vscNext() {
	p := &vscPage
	if strings.TrimSpace(p.draft.Phrase) == "" {
		PlayErrorSound()
		return
	}
	PlayActivateSound()
	if p.fromCheck {
		vscGoStep(vscStepCheck)
		return
	}
	vscGoStep(vscStepPick)
}

// vscPick makes the draft open a catalog row.
func vscPick(t voiceTarget) {
	p := &vscPage
	p.draft = voiceShortcutFromTarget(p.draft.Phrase, t)
	p.command, p.preview = "", ""
	PlayActivateSound()
	vscGoStep(vscStepCheck)
}

// vscUseCommand makes the draft run the command on the command step.
func vscUseCommand() {
	p := &vscPage
	if p.command == "" {
		PlayErrorSound()
		return
	}
	p.draft = voiceShortcut{Phrase: p.draft.Phrase, Kind: voiceShortcutCommand, Command: p.command}
	PlayActivateSound()
	vscGoStep(vscStepCheck)
}

// vscAskWebPage asks for the page a web shortcut opens.
func vscAskWebPage() {
	vscStopCapture()
	seed := ""
	if vscPage.draft.Kind == voiceShortcutWeb {
		seed = vscPage.draft.URL
	}
	PlayActivateSound()
	tbxOpenPrompt("Web page", "example.com/page", seed, func(text string) {
		if text == "" || !vscOnPage(appConfig) {
			return
		}
		u, ok := voiceShortcutWebURL(text)
		if !ok {
			vscSay("That is not a web address — try example.com", tbxToneWarn)
			PlayErrorSound()
			return
		}
		p := &vscPage
		p.draft = voiceShortcut{Phrase: p.draft.Phrase, Kind: voiceShortcutWeb, Title: voiceShortcutHost(u), URL: u}
		vscGoStep(vscStepCheck)
	})
}

// vscTypeText opens the keyboard for the word or the command.
func vscTypeText() {
	vscStopCapture()
	p := &vscPage
	PlayActivateSound()
	if p.step == vscStepCommand {
		tbxOpenPrompt("Voice command", "brightness 10 · turn off wifi · open YouTube", p.command, func(text string) {
			if vscOnPage(appConfig) && vscPage.step == vscStepCommand {
				vscSetCommand(text)
			}
		})
		return
	}
	tbxOpenPrompt("Your word", "lights out · retro · kart", p.draft.Phrase, func(text string) {
		if vscOnPage(appConfig) && vscPage.step == vscStepWord {
			vscSetWord(text)
		}
	})
}

// vscAskFilter opens the keyboard for the pick list's filter.
func vscAskFilter() {
	PlayActivateSound()
	tbxOpenPrompt("Filter", "a name, like mario or retroarch", vscPage.filter, func(text string) {
		if !vscOnPage(appConfig) || vscPage.step != vscStepPick {
			return
		}
		vscPage.filter = text
		vscPage.picksDirty = true
		vscPage.nav.focus = "p0"
		vscPage.nav.scroll = 0
	})
}

// vscSetGroup switches the pick list's group.
func vscSetGroup(g int) {
	if g < 0 {
		g = len(vscGroups) - 1
	}
	if g >= len(vscGroups) {
		g = 0
	}
	if g == vscPage.group {
		return
	}
	vscPage.group = g
	vscPage.picksDirty = true
	vscPage.nav.scroll = 0
	vscPage.nav.focus = vscGroupIDs[g]
	PlayNavSound()
}

// vscSave stores the draft: in place of the one being changed, or added; a
// shortcut that already listened for the same word goes.
func vscSave(config *Config) {
	p := &vscPage
	sc, ok := voiceShortcutClean(p.draft)
	if !ok {
		vscSay("Pick what the word opens first", tbxToneWarn)
		PlayErrorSound()
		return
	}
	folded := voiceShortcutFold(sc.Phrase)
	list := make([]voiceShortcut, 0, len(p.list)+1)
	placed := false
	for i, old := range p.list {
		if i == p.editing {
			list = append(list, sc)
			placed = true
			continue
		}
		if voiceShortcutFold(old.Phrase) == folded {
			continue
		}
		list = append(list, old)
	}
	if !placed {
		if len(list) >= voiceShortcutMax {
			vscSay("50 words is the most — delete one first", tbxToneWarn)
			PlayErrorSound()
			return
		}
		list = append(list, sc)
	}
	voiceShortcutsSave(config, list)
	vscRefresh(config)
	vscGoStep(vscStepList)
	for i := range p.list {
		if i < len(vscListIDs) && voiceShortcutFold(p.list[i].Phrase) == folded {
			p.nav.focus = vscListIDs[i]
			break
		}
	}
	PlaySuccessSound()
	ShowToast("Saved — say “"+sc.Phrase+"”", ToastKindSuccess)
}

// vscAskDelete asks before deleting the shortcut at i.
func vscAskDelete(config *Config, i int) {
	p := &vscPage
	if i < 0 || i >= len(p.list) {
		return
	}
	phrase := p.list[i].Phrase
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Delete this shortcut?",
		subtitle: "“" + phrase + "”",
		icon:     "mic",
		body:     "JukaHub stops listening for “" + phrase + "”. You can teach it again any time.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Delete", primary: true, run: func() { vscDelete(config, phrase) }},
		},
	})
}

// vscDelete removes the shortcut for phrase.
func vscDelete(config *Config, phrase string) {
	p := &vscPage
	folded := voiceShortcutFold(phrase)
	list := make([]voiceShortcut, 0, len(p.list))
	at := -1
	for i, sc := range p.list {
		if voiceShortcutFold(sc.Phrase) == folded {
			at = i
			continue
		}
		list = append(list, sc)
	}
	if at < 0 {
		return
	}
	voiceShortcutsSave(config, list)
	vscRefresh(config)
	switch {
	case len(p.list) == 0:
		p.nav.focus = "add"
	case at < len(p.list):
		p.nav.focus = vscListIDs[at]
	default:
		p.nav.focus = vscListIDs[len(p.list)-1]
	}
	ShowToast("Deleted “"+phrase+"”", ToastKindInfo)
}

// vscTry runs the shortcut at i as if it had been said. A list of choices
// cannot be shown here, so it is named instead (the voice page and the orb
// show it when the word is said).
func vscTry(config *Config, i int) {
	p := &vscPage
	if i < 0 || i >= len(p.list) {
		return
	}
	sc := p.list[i]
	out := voiceShortcutOutcome(config, sc, voiceFromTyped)
	switch {
	case out.Cancel:
		PlayBackSound()
	case len(out.Choices) > 0:
		PlayNavSound()
		ShowToast("“"+sc.Phrase+"”: "+out.Say+" — say it to pick one", ToastKindInfo)
	case out.Act != nil:
		PlayActivateSound()
		ShowToast(out.Say, ToastKindInfo)
		out.Act(config)
	case out.Tone == tbxToneWarn || out.Tone == tbxToneBad:
		PlayErrorSound()
		ShowToast(out.Say, ToastKindWarning)
	default:
		PlaySuccessSound()
		ShowToast(out.Say, ToastKindSuccess)
	}
}

// ── Listening ────────────────────────────────────────────────────────────

// vscListen is the microphone button: start listening, or stop and send.
func vscListen(config *Config) {
	r := &vscPage.rec
	if r.on {
		vscFinishClip(config)
		return
	}
	if r.waiting || voiceOrbActive() {
		return
	}
	switch {
	case vkMic.Load() == vkMicAbsent:
		vscSay("No microphone found — type it instead", tbxToneWarn)
		PlayErrorSound()
		return
	case strings.TrimSpace(groqAPIKey()) == "":
		vscSay("Saying it needs your Groq key (Settings → General) — or type it", tbxToneWarn)
		PlayErrorSound()
		return
	case !IsOnline():
		vscSay("Saying it needs internet — type it instead", tbxToneWarn)
		PlayErrorSound()
		return
	}
	// Juka still answering would be recorded as the word (voiceStartListening
	// does the same).
	jukaSpeakStop()
	mic, err := micOpen()
	if err != nil {
		msg := "Couldn't open the microphone — type it instead"
		if errors.Is(err, errMicNone) {
			vkMic.Store(vkMicAbsent)
			msg = "No microphone found — type it instead"
		}
		vscSay(msg, tbxToneWarn)
		PlayErrorSound()
		return
	}
	vkMic.Store(vkMicPresent)
	if cap(r.buf) < voiceMaxBytes {
		r.buf = make([]byte, 0, voiceMaxBytes)
	}
	r.buf = r.buf[:0]
	r.mic = mic
	r.on = true
	r.started = sdl.GetTicks64()
	r.level, r.peak = 0, 0
	r.speaking, r.quietSince = false, 0
	vscSay("", tbxToneNeutral)
	vscPage.nav.focus = "stop"
	PlayActivateSound()
	// Leaving the page closes the microphone, whichever way it is left (the
	// Voice command page's watcher, voiceStartListening).
	gen := r.gen
	done := make(chan struct{})
	r.capDone = done
	runSceneTask(func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		tbxPost(func() {
			if vscPage.rec.gen == gen && vscPage.rec.mic != nil {
				vscStopCapture()
			}
		})
	})
}

// vscCloseDevice closes the microphone and ends its watcher.
func vscCloseDevice() {
	r := &vscPage.rec
	if r.mic != nil {
		r.mic.Close()
		r.mic = nil
	}
	if r.capDone != nil {
		close(r.capDone)
		r.capDone = nil
	}
	r.on = false
}

// vscStopCapture closes the microphone, drops the clip and any answer still
// coming for it.
func vscStopCapture() {
	vscCloseDevice()
	r := &vscPage.rec
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.waiting = false
	r.gen++
	r.buf = r.buf[:0]
	r.level = 0
}

// vscDrainAudio moves what the microphone captured into the clip and follows
// the level (voiceDrainAudio's rules: the start chime is not speech).
func vscDrainAudio() {
	r := &vscPage.rec
	if r.mic == nil {
		return
	}
	room := (cap(r.buf) - len(r.buf)) &^ 1
	if room <= 0 {
		return
	}
	start := len(r.buf)
	got := r.mic.Read(r.buf[start : start+room])
	if got <= 0 {
		return
	}
	if sdl.GetTicks64()-r.started < voiceChimeMs {
		return
	}
	r.buf = r.buf[:start+got]
	level := vkPCMLevel(r.buf[start : start+got])
	if level > r.peak {
		r.peak = level
	}
	if level > r.level {
		r.level = level
	} else {
		r.level = r.level*0.82 + level*0.18
	}
	switch {
	case level >= voiceSpeechLevel:
		r.speaking, r.quietSince = true, 0
	case level < voiceQuietLevel && r.speaking && r.quietSince == 0:
		r.quietSince = sdl.GetTicks64()
	}
}

// vscTick runs once a frame: it records and ends the clip by itself, and stops
// listening when the page is left or covered.
func vscTick(config *Config) {
	r := &vscPage.rec
	if transitionPhase == "fade-out" {
		if r.on || r.waiting {
			vscStopCapture()
		}
		return
	}
	if !r.on {
		return
	}
	if voiceCovered() || voiceOrbActive() {
		vscStopCapture()
		return
	}
	vscDrainAudio()
	now := sdl.GetTicks64()
	switch {
	case len(r.buf) >= cap(r.buf):
		vscFinishClip(config)
	case r.speaking && r.quietSince != 0 && now-r.quietSince >= voiceEndQuietMs:
		vscFinishClip(config)
	case !r.speaking && now-r.started >= voiceNoSpeechMs:
		vscStopCapture()
		vscSay("Didn't hear anything — is the microphone on? You can type it", tbxToneWarn)
		PlayErrorSound()
	}
	if frameRateCtrl != nil {
		frameRateCtrl.NotifyInput()
	}
}

// vscFinishClip ends the recording and sends it to Whisper.
func vscFinishClip(config *Config) {
	r := &vscPage.rec
	if !r.on {
		return
	}
	vscDrainAudio()
	vscCloseDevice()
	ms := len(r.buf) * 1000 / (vkVoiceRate * 2)
	switch {
	case ms < vkVoiceMinMs:
		r.buf = r.buf[:0]
		PlayBackSound()
		return
	case r.peak < vkVoiceSilence:
		r.buf = r.buf[:0]
		vscSay("Didn't hear anything — is the microphone on? You can type it", tbxToneWarn)
		PlayErrorSound()
		return
	}
	key := strings.TrimSpace(groqAPIKey())
	if key == "" {
		vscSay("Saying it needs your Groq key (Settings → General) — or type it", tbxToneWarn)
		return
	}
	wav := vkEncodeWAV(r.buf, vkVoiceRate)
	r.buf = r.buf[:0]
	// A command gets the names on this handheld as the vocabulary hint, as a
	// spoken command does; a new word gets none — it should come back the way
	// Whisper hears it unprompted, which is how it will hear it next time.
	prompt := ""
	step := vscPage.step
	if step == vscStepCommand {
		prompt = voiceWhisperPrompt(vscPage.targets)
	}
	r.gen++
	gen := r.gen
	r.waiting = true
	vscPage.nav.focus = "cancel"
	PlayActivateSound()
	reqCtx, cancel := context.WithTimeout(sceneWorkContext(), vkVoiceTimeout)
	r.cancel = cancel
	started := runSceneTask(func(context.Context) {
		defer cancel()
		text, err := voiceTranscribe(reqCtx, key, wav, prompt)
		tbxPost(func() { vscTranscribed(gen, step, text, err) })
	})
	if !started {
		cancel()
		r.cancel = nil
		r.waiting = false
		vscSay("Could not start the request — try again", tbxToneWarn)
	}
}

// vscTranscribed applies Whisper's answer if it is still the one waited for.
func vscTranscribed(gen int, step vscStep, text string, err error) {
	r := &vscPage.rec
	if gen != r.gen || !r.waiting || vscPage.step != step || !vscOnPage(appConfig) {
		return
	}
	r.waiting = false
	r.cancel = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		msg, _ := voiceErrorText(err)
		vscSay(msg, tbxToneWarn)
		PlayErrorSound()
		return
	}
	if strings.TrimSpace(text) == "" {
		vscSay("Didn't catch that — try again", tbxToneWarn)
		return
	}
	if step == vscStepCommand {
		vscSetCommand(text)
		return
	}
	vscSetWord(text)
}

// ── Pick list ────────────────────────────────────────────────────────────

// vscInGroup reports whether t belongs in group id.
func vscInGroup(t voiceTarget, id string) bool {
	emu := voiceIsEmuTarget(t)
	switch id {
	case "all":
		return true
	case "emu":
		return emu
	case "screen":
		return t.Kind == voiceTargetScreen && !emu
	case "app":
		return t.Kind == voiceTargetApp && !emu
	case "game":
		return t.Kind == voiceTargetGame
	case "setting":
		return t.Kind == voiceTargetSetting
	case "media":
		return t.Kind == voiceTargetMedia
	case "file":
		return t.Kind == voiceTargetFile
	}
	return false
}

// vscGroupRank orders "All" without a filter: the emulators first, settings
// and files (hundreds of rows) last.
func vscGroupRank(t voiceTarget) int {
	if voiceIsEmuTarget(t) {
		return 0
	}
	switch t.Kind {
	case voiceTargetScreen:
		return 1
	case voiceTargetApp:
		return 2
	case voiceTargetGame:
		return 3
	case voiceTargetMedia:
		return 4
	case voiceTargetSetting:
		return 5
	}
	return 6
}

// vscRebuildPicks fills the rows for the group and the filter: the filter
// ranks by Home search's ladder, and without one the catalog's order stands.
func vscRebuildPicks() {
	p := &vscPage
	p.picksDirty = false
	p.picks = p.picks[:0]
	p.more = 0
	group := vscGroups[p.group].id
	q := newHomeSearchQuery(p.filter)
	// Indices into the catalog, not copies of it: "All" without a filter is
	// every row of a card of ROMs.
	type hit struct {
		idx, score, rank int
	}
	hits := make([]hit, 0, 64)
	for i := range p.targets {
		t := &p.targets[i]
		if !vscInGroup(*t, group) || t.Title == "Voice shortcuts" {
			continue
		}
		score := 0
		if !q.empty() {
			keys := t.Keys
			if t.Kind == voiceTargetScreen || t.Kind == voiceTargetSetting {
				keys = strings.TrimSpace(keys + " " + t.Where)
			}
			if score = homeSearchMatch(q, t.Title, keys); score == 0 {
				continue
			}
		}
		hits = append(hits, hit{idx: i, score: score, rank: vscGroupRank(*t)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].rank < hits[j].rank
	})
	for _, h := range hits {
		if len(p.picks) >= vscPickCap {
			p.more++
			continue
		}
		p.picks = append(p.picks, p.targets[h.idx])
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// vscOnAction is the page's input once the sheet has had its turn.
func vscOnAction(config *Config, act tbxAction) bool {
	p := &vscPage
	switch act {
	case tbxActBack:
		if p.step == vscStepList && !p.rec.on && !p.rec.waiting {
			vscStopCapture()
			return false // tbxDispatch leaves the page
		}
		vscBack()
		return true
	case tbxActConfirm:
		vscActivate(config, p.nav.focus)
		return true
	case tbxActX:
		switch p.step {
		case vscStepList:
			vscAskDelete(config, vscIDIndex(p.nav.focus, vscListIDs[:]))
		case vscStepWord, vscStepCommand:
			if !p.rec.on {
				vscTypeText()
			}
		case vscStepPick:
			vscAskFilter()
		}
		return true
	case tbxActY:
		if p.step == vscStepList {
			vscTry(config, vscIDIndex(p.nav.focus, vscListIDs[:]))
		}
		return true
	case tbxActPageUp, tbxActPageDown:
		if p.step == vscStepPick {
			if act == tbxActPageUp {
				vscSetGroup(p.group - 1)
			} else {
				vscSetGroup(p.group + 1)
			}
			return true
		}
	}
	return tbxNavAction(&p.nav, act)
}

// vscActivate presses the control with id.
func vscActivate(config *Config, id string) {
	p := &vscPage
	switch id {
	case "add":
		vscStartTeach()
		return
	case "say", "again", "stop", "mic":
		vscListen(config)
		return
	case "cancel":
		vscStopCapture()
		vscSay("", tbxToneNeutral)
		p.nav.focus = "say"
		PlayBackSound()
		return
	case "type":
		vscTypeText()
		return
	case "next":
		vscNext()
		return
	case "use":
		vscUseCommand()
		return
	case "filter":
		vscAskFilter()
		return
	case "command":
		p.command, p.preview = "", ""
		if p.draft.Kind == voiceShortcutCommand {
			p.command = p.draft.Command
			p.preview = voiceCommandPreview(p.command, p.targets)
		}
		PlayActivateSound()
		vscGoStep(vscStepCommand)
		return
	case "web":
		vscAskWebPage()
		return
	case "save":
		vscSave(config)
		return
	case "word":
		p.fromCheck = true
		PlayActivateSound()
		vscGoStep(vscStepWord)
		return
	case "what":
		p.fromCheck = true
		PlayActivateSound()
		vscGoStep(vscStepPick)
		return
	case "drop":
		PlayBackSound()
		vscGoStep(vscStepList)
		return
	}
	if i := vscIDIndex(id, vscListIDs[:]); i >= 0 {
		vscEdit(i)
		return
	}
	if i := vscIDIndex(id, vscGroupIDs[:]); i >= 0 {
		vscSetGroup(i)
		return
	}
	if i := vscIDIndex(id, vscPickIDs[:]); i >= 0 && i < len(p.picks) {
		vscPick(p.picks[i])
	}
}

// vscSceneClick routes a click or a tap on the page (main.go).
func vscSceneClick(mx, my int32, config *Config) bool {
	if !vscOnPage(config) {
		return false
	}
	if tbxSheetVisible(config) {
		tbxSheetMouse(mx, my)
		return true
	}
	if vscPage.micRect.W > 0 && pointInRect(mx, my, vscPage.micRect) {
		vscListen(config)
		return true
	}
	if it, ok := vscPage.nav.hit(mx, my); ok && !it.disabled {
		vscPage.nav.focus = it.id
		vscActivate(config, it.id)
		return true
	}
	return false
}

// vscSceneWheel scrolls the page, or its open sheet (main.go's wheel chain).
func vscSceneWheel(config *Config, y int32) bool {
	if y == 0 || !vscOnPage(config) {
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
	vscPage.nav.scrollBy(-y * 48)
	return true
}

// vscFooterHints is the footer while the page is up (extras.go).
func vscFooterHints(config *Config) []FooterHint {
	if tbxSheetVisible(config) {
		return tbxSheetHints()
	}
	p := &vscPage
	switch {
	case p.rec.on:
		return []FooterHint{{Button: "A", Label: "Done"}, {Button: "B", Label: "Cancel"}}
	case p.rec.waiting:
		return []FooterHint{{Button: "B", Label: "Cancel"}}
	}
	switch p.step {
	case vscStepList:
		if vscIDIndex(p.nav.focus, vscListIDs[:]) >= 0 {
			return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Change"},
				{Button: "X", Label: "Delete"}, {Button: "Y", Label: "Try"}, {Button: "B", Label: "Back"}}
		}
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Teach a word"}, {Button: "B", Label: "Back"}}
	case vscStepPick:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Pick"},
			{Button: "L1/R1", Label: "Group"}, {Button: "X", Label: "Filter"}, {Button: "B", Label: "Back"}}
	case vscStepCheck:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: vscFocusLabel()}, {Button: "B", Label: "Cancel"}}
	}
	return []FooterHint{{Button: "A", Label: vscFocusLabel()}, {Button: "X", Label: "Type"}, {Button: "B", Label: "Back"}}
}

// vscFocusLabel names what A presses on the word, command and check steps.
func vscFocusLabel() string {
	switch vscPage.nav.focus {
	case "type":
		return "Type"
	case "next":
		return "Next"
	case "use":
		return "Use it"
	case "save":
		return "Save"
	case "word":
		return "Change word"
	case "what":
		return "Change"
	case "drop":
		return "Cancel"
	}
	return "Say it"
}

// ── Drawing ──────────────────────────────────────────────────────────────

// vscButton is one button of a row of buttons.
type vscButton struct {
	id, label string
	primary   bool
}

// vscKindIcon is the glyph of a saved shortcut's row.
func vscKindIcon(sc voiceShortcut) string {
	switch sc.Kind {
	case voiceShortcutCommand:
		return "mic"
	case voiceShortcutWeb:
		return "browser"
	case voiceShortcutGame:
		return "gamepad"
	case voiceShortcutFile:
		return "files"
	case voiceShortcutMedia:
		return "media"
	case voiceShortcutSetting:
		return "settings"
	case voiceShortcutApp:
		return "apps"
	}
	if strings.HasPrefix(sc.Where, voiceEmuWhereTile) {
		return "emulator"
	}
	return "grid"
}

// vscHeader is the page header for the step.
func vscHeader() UIPageHeader {
	p := &vscPage
	word := strings.TrimSpace(p.draft.Phrase)
	switch p.step {
	case vscStepWord:
		return tbxHeader("Teach a word", "Step 1 of 3 · what you will say", "mic", "", sdl.Color{})
	case vscStepPick:
		return tbxHeader("Teach a word", "Step 2 of 3 · what “"+word+"” opens", "mic", "", sdl.Color{})
	case vscStepCommand:
		return tbxHeader("Teach a word", "Step 2 of 3 · the command “"+word+"” runs", "mic", "", sdl.Color{})
	case vscStepCheck:
		return tbxHeader("Teach a word", "Step 3 of 3 · check and save", "mic", "", sdl.Color{})
	}
	status := strconv.Itoa(len(p.list)) + " of " + strconv.Itoa(voiceShortcutMax)
	return tbxHeader("Voice shortcuts", "Your own words for the voice assistant", "mic", status, ColorTextMuted())
}

// vscRender draws the page.
func vscRender(renderer *sdl.Renderer, config *Config) {
	if vscPage.visit.begin() {
		vscEnter(config)
	}
	vscTick(config)
	if vscPage.step == vscStepPick && vscPage.picksDirty {
		vscRebuildPicks()
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	f := tbxBeginFrame(renderer, config, vscHeader())
	body := f.bodyRect()
	nav := &vscPage.nav
	nav.begin(body.Y, body.H)
	vscPage.micRect = sdl.Rect{}
	switch vscPage.step {
	case vscStepWord, vscStepCommand:
		vscRenderListen(renderer, config, fonts, body)
	case vscStepPick:
		vscRenderPick(renderer, config, fonts, body)
	case vscStepCheck:
		vscRenderCheck(renderer, config, fonts, body)
	default:
		vscRenderList(renderer, config, fonts, body)
	}
	tbxDrawScrollbar(renderer, body.X+body.W-6, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// vscLayoutButtons lays a row of buttons out centred on cx at y (body
// coordinates) in nav row, and returns their rects.
func vscLayoutButtons(fonts tbxFonts, btns []vscButton, cx, y int32, row int, rects []sdl.Rect) []sdl.Rect {
	rects = rects[:0]
	total := int32(0)
	for i, b := range btns {
		w := tbxButtonWidth(fonts.body, b.label, 132)
		rects = append(rects, sdl.Rect{Y: y, W: w, H: 46})
		total += w
		if i > 0 {
			total += 14
		}
	}
	x := cx - total/2
	for i := range rects {
		rects[i].X = x
		x += rects[i].W + 14
		vscPage.nav.add(btns[i].id, rects[i], row, false)
	}
	return rects
}

// vscDrawButtons draws a laid-out row of buttons.
func vscDrawButtons(renderer *sdl.Renderer, config *Config, fonts tbxFonts, btns []vscButton, rects []sdl.Rect) {
	nav := &vscPage.nav
	for i, b := range btns {
		if i >= len(rects) {
			break
		}
		r := rects[i]
		r.Y += nav.top - nav.scroll
		tbxDrawButton(renderer, config, fonts.body, r, b.label, b.primary, nav.focused(b.id), false)
	}
}

// vscDrawCentred draws one line centred on cx, cut to maxW.
func vscDrawCentred(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, cx, y, maxW int32) {
	if text == "" {
		return
	}
	line := ellipsize(font, text, maxW)
	w, _ := measureText(font, line)
	renderText(renderer, config, font, line, color, cx-w/2, y)
}

// vscMsgColor is the colour of the message line.
func vscMsgColor() sdl.Color {
	switch vscPage.tone {
	case tbxToneWarn, tbxToneBad:
		return tbxToneColor(vscPage.tone)
	}
	return ColorTextSecondary()
}

// vscRenderList draws the saved shortcuts.
func vscRenderList(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	p := &vscPage
	nav := &p.nav
	full := len(p.list) >= voiceShortcutMax
	bw := tbxButtonWidth(fonts.body, "Teach a word", 180)
	add := sdl.Rect{X: body.X, Y: 0, W: bw, H: 46}
	nav.add("add", add, 0, full)
	y := add.H + 18
	msgY := y
	if p.msg != "" {
		y += fonts.bodyH + 12
	}
	rowH := tbxRowHeight(fonts)
	listTop := y
	if len(p.list) > 0 {
		y += tbxSectionHeight
		for i := range p.list {
			if i >= len(vscListIDs) {
				break
			}
			nav.add(vscListIDs[i], sdl.Rect{X: body.X, Y: y, W: body.W - 14, H: rowH}, i+1, false)
			y += rowH + 8
		}
	} else {
		y += 220
	}
	nav.extend(y)
	nav.end()

	tbxClip(renderer, body)
	off := body.Y - nav.scroll
	r := add
	r.Y += off
	tbxDrawButton(renderer, config, fonts.body, r, "Teach a word", true, nav.focused("add"), full)
	hint := hintSentence("Say a word, and JukaHub opens what you chose — %s on a word tries it", "Y", "Try")
	if len(p.list) == 0 {
		hint = "Say a word, and JukaHub opens what you chose"
	}
	hx := add.X + add.W + 20
	if hx < body.X+body.W-80 {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, body.X+body.W-hx-16), ColorTextTertiary(),
			hx, off+(add.H-fonts.smallH)/2)
	}
	if p.msg != "" {
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, p.msg, body.W-16), vscMsgColor(), body.X, off+msgY)
	}
	if len(p.list) == 0 {
		tbxDrawEmpty(renderer, config, sdl.Rect{X: body.X, Y: off + listTop, W: body.W, H: 200}, "mic", "No words yet",
			"Teach one: “lights out” to dim the screen, “retro” for RetroArch's menu, “kart” for the one game you always play.")
	} else {
		tbxDrawSection(renderer, config, "Your words", "mic", body.X, off+listTop, body.W-14)
		for _, it := range nav.items {
			i := vscIDIndex(it.id, vscListIDs[:])
			if i < 0 || i >= len(p.list) {
				continue
			}
			sc := p.list[i]
			spec := tbxRowSpec{icon: vscKindIcon(sc), title: "“" + sc.Phrase + "”"}
			if i < len(p.descs) {
				spec.subtitle = p.descs[i]
			}
			if i < len(p.missing) && p.missing[i] {
				spec.pill, spec.tone = "Not here", tbxToneWarn
			}
			tbxDrawRow(renderer, config, fonts, nav.screenRect(it), spec, nav.focused(it.id))
		}
	}
	tbxUnclip(renderer)
}

// vscRenderListen draws the word step and the command step: the microphone,
// what was heard, and the buttons.
func vscRenderListen(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	p := &vscPage
	nav := &p.nav
	cx := body.X + body.W/2
	const radius = int32(48)
	micCY := radius + 14
	stateY := micCY + radius + 18
	heardY := stateY + fonts.titleH + 8
	msgY := heardY + fonts.bodyH + 8
	buttonsY := msgY + fonts.bodyH + 18

	command := p.step == vscStepCommand
	have := p.draft.Phrase != ""
	if command {
		have = p.command != ""
	}
	var btnBuf [4]vscButton
	btns := btnBuf[:0]
	switch {
	case p.rec.on:
		btns = append(btns, vscButton{"stop", "Done", true}, vscButton{"cancel", "Cancel", false})
	case p.rec.waiting:
		btns = append(btns, vscButton{"cancel", "Cancel", false})
	case have && command:
		btns = append(btns, vscButton{"use", "Use it", true}, vscButton{"again", "Say again", false}, vscButton{"type", "Type", false})
	case have:
		btns = append(btns, vscButton{"next", "Next", true}, vscButton{"again", "Say again", false}, vscButton{"type", "Type", false})
	default:
		btns = append(btns, vscButton{"say", "Say it", true}, vscButton{"type", "Type it", false})
	}
	var rectBuf [4]sdl.Rect
	rects := vscLayoutButtons(fonts, btns, cx, buttonsY, 0, rectBuf[:0])
	tipY := buttonsY + 46 + 22
	nav.extend(tipY + 3*(fonts.smallH+4))
	nav.end()

	tbxClip(renderer, body)
	off := body.Y - nav.scroll
	mcy := off + micCY
	accent := glassAccent()
	if p.rec.on {
		halo := radius + 6 + int32(p.rec.level*160)
		if halo > radius+40 {
			halo = radius + 40
		}
		fillCircle(renderer, cx, mcy, halo, WithAlpha(accent, 40))
		fillCircle(renderer, cx, mcy, radius+(halo-radius)/2, WithAlpha(accent, 70))
		fillCircle(renderer, cx, mcy, radius, WithAlpha(accent, 230))
	} else {
		fillCircle(renderer, cx, mcy, radius, glassA(34))
		if p.rec.waiting {
			strokeCircle(renderer, cx, mcy, radius+4, WithAlpha(accent, 200))
		}
	}
	strokeCircle(renderer, cx, mcy, radius, glassA(90))
	glyph := ColorTextPrimary()
	if p.rec.on {
		glyph = sdl.Color{R: 255, G: 255, B: 255, A: 255}
	}
	drawHomeGlyphTinted(renderer, "mic", sdl.Rect{X: cx - radius/2, Y: mcy - radius/2, W: radius, H: radius}, glyph)
	disc := sdl.Rect{X: cx - radius, Y: mcy - radius, W: 2 * radius, H: 2 * radius}
	if hit, ok := disc.Intersect(&body); ok {
		p.micRect = hit
	}

	var state string
	switch {
	case p.rec.on && p.rec.speaking:
		state = "Listening… " + hintSentence("stop talking or press %s", "A", "Done")
	case p.rec.on:
		state = "Listening…"
	case p.rec.waiting:
		state = "Understanding…"
	case command:
		state = hintSentence("Press %s and say a command, like “brightness 10”", "A", "Say it")
	default:
		state = hintSentence("Press %s and say the word, the way you will say it later", "A", "Say it")
	}
	vscDrawCentred(renderer, config, fonts.title, state, ColorTextPrimary(), cx, off+stateY, body.W-40)
	heard := p.draft.Phrase
	if command {
		heard = p.command
	}
	if heard != "" {
		vscDrawCentred(renderer, config, fonts.body, "“"+heard+"”", ColorTextPrimary(), cx, off+heardY, body.W-40)
	}
	msg, msgColor := p.msg, vscMsgColor()
	if msg == "" && command && p.preview != "" {
		msg, msgColor = p.preview, ColorTextSecondary()
	}
	vscDrawCentred(renderer, config, fonts.body, msg, msgColor, cx, off+msgY, body.W-40)
	vscDrawButtons(renderer, config, fonts, btns, rects)
	tip := "Short and distinct works best: one or two words that are not the name of something else."
	if command {
		tip = "Anything you can say to the voice assistant: “turn off wifi”, “volume 30”, “open YouTube”, “play music”."
	}
	lines := p.wrap.get(fonts.small, tip, body.W-120)
	ty := off + tipY
	for _, l := range lines {
		tw, _ := measureText(fonts.small, l)
		renderText(renderer, config, fonts.small, l, ColorTextTertiary(), cx-tw/2, ty)
		ty += fonts.smallH + 4
	}
	tbxUnclip(renderer)
}

// vscRenderPick draws the groups, the filter, the two other kinds of shortcut
// and the rows to pick from.
func vscRenderPick(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	p := &vscPage
	nav := &p.nav
	// The group chips.
	const chipH = int32(38)
	var chipBuf [len(vscGroups)]sdl.Rect
	x := body.X
	for i, g := range vscGroups {
		w := tbxButtonWidth(fonts.small, g.label, 0)
		chipBuf[i] = sdl.Rect{X: x, Y: 0, W: w, H: chipH}
		nav.add(vscGroupIDs[i], chipBuf[i], 0, false)
		x += w + 10
	}
	filterY := chipH + 14
	y := filterY
	// The filter and, in "All", the two other kinds of shortcut.
	filterLabel := "Filter…"
	if f := strings.TrimSpace(p.filter); f != "" {
		filterLabel = "Filter: " + f
	}
	var btnBuf [3]vscButton
	btns := append(btnBuf[:0], vscButton{"filter", filterLabel, false})
	if vscGroups[p.group].id == "all" {
		btns = append(btns, vscButton{"command", "A voice command…", false}, vscButton{"web", "A web page…", false})
	}
	var rowBuf [3]sdl.Rect
	bx := body.X
	for i, b := range btns {
		w := tbxButtonWidth(fonts.body, b.label, 120)
		if i == 0 && w > body.W/2 {
			w = body.W / 2
		}
		rowBuf[i] = sdl.Rect{X: bx, Y: y, W: w, H: 46}
		nav.add(b.id, rowBuf[i], 1, false)
		bx += w + 14
	}
	y += 46 + 16
	rowH := tbxRowHeight(fonts)
	for i := range p.picks {
		nav.add(vscPickIDs[i], sdl.Rect{X: body.X, Y: y, W: body.W - 14, H: rowH}, i+2, false)
		y += rowH + 8
	}
	noteY := y
	if p.more > 0 || len(p.picks) == 0 {
		y += fonts.bodyH + 16
	}
	nav.extend(y)
	nav.end()

	tbxClip(renderer, body)
	off := body.Y - nav.scroll
	for i, g := range vscGroups {
		r := chipBuf[i]
		r.Y += off
		tbxDrawButton(renderer, config, fonts.small, r, g.label, i == p.group, nav.focused(vscGroupIDs[i]), false)
	}
	for i, b := range btns {
		r := rowBuf[i]
		r.Y += off
		tbxDrawButton(renderer, config, fonts.body, r, ellipsize(fonts.body, b.label, r.W-30), false, nav.focused(b.id), false)
	}
	if p.msg != "" && bx < body.X+body.W-60 {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, p.msg, body.X+body.W-bx-8), vscMsgColor(),
			bx, off+filterY+(46-fonts.smallH)/2)
	}
	for _, it := range nav.items {
		i := vscIDIndex(it.id, vscPickIDs[:])
		if i < 0 || i >= len(p.picks) {
			continue
		}
		t := p.picks[i]
		spec := tbxRowSpec{icon: t.Icon, title: t.Title, subtitle: t.Where}
		if spec.icon == "" {
			spec.icon = "grid"
		}
		if t.Blocked != "" {
			spec.pill, spec.tone = homeSearchNeedsInternet, tbxToneWarn
		}
		tbxDrawRow(renderer, config, fonts, nav.screenRect(it), spec, nav.focused(it.id))
	}
	note := ""
	switch {
	case len(p.picks) == 0 && strings.TrimSpace(p.filter) != "":
		note = "Nothing here matches “" + strings.TrimSpace(p.filter) + "” — try another group or filter"
	case len(p.picks) == 0:
		note = "Nothing in this group on this handheld yet"
	case p.more > 0:
		note = strconv.Itoa(p.more) + " more — " + hintSentence("press %s to filter", "X", "Filter")
	}
	if note != "" {
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, note, body.W-16), ColorTextTertiary(), body.X, off+noteY+8)
	}
	tbxUnclip(renderer)
}

// vscRenderCheck draws the check step: the word, what it does, any clash, and
// Save.
func vscRenderCheck(renderer *sdl.Renderer, config *Config, fonts tbxFonts, body sdl.Rect) {
	p := &vscPage
	nav := &p.nav
	cx := body.X + body.W/2
	const gap = int32(8)
	y := int32(10)
	labelY := y
	y += fonts.smallH + gap
	wordY := y
	y += fonts.titleH + 18
	willY := y
	y += fonts.smallH + gap
	whatY := y
	y += fonts.bodyH + 18
	lines := p.wrap.get(fonts.body, p.checkNote, body.W-120)
	noteY := y
	if p.checkNote != "" {
		y += int32(len(lines))*(fonts.bodyH+4) + 14
	}
	btns := [...]vscButton{{"save", "Save", true}, {"word", "Change word", false}, {"what", "Change what it opens", false}}
	var rectBuf [3]sdl.Rect
	rects := vscLayoutButtons(fonts, btns[:], cx, y, 0, rectBuf[:0])
	y += 46 + 20
	nav.extend(y)
	nav.end()

	tbxClip(renderer, body)
	off := body.Y - nav.scroll
	vscDrawCentred(renderer, config, fonts.small, "When you say", ColorTextTertiary(), cx, off+labelY, body.W-40)
	vscDrawCentred(renderer, config, fonts.title, "“"+p.draft.Phrase+"”", ColorTextPrimary(), cx, off+wordY, body.W-40)
	vscDrawCentred(renderer, config, fonts.small, "JukaHub will", ColorTextTertiary(), cx, off+willY, body.W-40)
	vscDrawCentred(renderer, config, fonts.body, p.checkWhat, ColorTextPrimary(), cx, off+whatY, body.W-40)
	if p.checkNote != "" {
		ly := off + noteY
		for _, l := range lines {
			tw, _ := measureText(fonts.body, l)
			renderText(renderer, config, fonts.body, l, tbxToneColor(tbxToneWarn), cx-tw/2, ly)
			ly += fonts.bodyH + 4
		}
	}
	vscDrawButtons(renderer, config, fonts, btns[:], rects)
	tbxUnclip(renderer)
}
