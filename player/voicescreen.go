package main

// voicescreen.go — Juka sees the screen you are on.
//
// Ask the voice orb "what is this?", "read this to me" or "which one should I
// pick?" and Juka answers about what is on screen. When the orb starts
// listening it asks for one picture of JukaHub's own frame, read in the
// render pass just before the orb draws (voiceScreenCapture, the first line
// of voiceOrbRender), so neither the orb nor its bubble is in it; the virtual
// cursor, the magnifier and the software dimmer come later in the frame and
// are not in it either. The Phone Companion's pairing code and the Screen
// Share panel's link are painted over the way a shared frame's are
// (castShareRedactions), and no picture is taken while the pairing code is
// enlarged.
//
// The picture stays in memory only: it goes to Groq's vision model with the
// question (groqvision.go) when Settings → Voice & AI → "Let Juka see the
// screen" allows it — Off, When you ask about it (the default: the question
// points at the screen, voiceScreenAsksAbout) or Every question — and is
// dropped when the answer is done, when the question needs no picture, or as
// soon as the orb goes away. A failed vision request is asked again as plain
// text (chatGroqAsk), unless part of the answer had already streamed.
//
// Every question also carries one cheap line about where the user is — the
// screen, the Settings section, the highlighted row or Home tile
// (voiceScreenWhere) — in Juka's system prompt. The offline model cannot see
// pictures: it gets that line in front of a question about the screen
// instead.
//
// A sentence that is plainly a question about the screen ("what am I looking
// at", "what does this say") goes to Juka before the command runs
// (voiceScreenTakeQuestion), which would otherwise have searched for its
// words; a phrase the parser knows ("how does this work") stays a command.
//
// Main thread only, apart from voiceScreenGroqAsk, which runs on the orb's
// request goroutine with the picture handed to it.

import (
	"context"
	"image"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// voiceScreenKey is the Custom setting behind "Let Juka see the screen".
	voiceScreenKey = "jukaSeesScreen"
	// voiceScreenOff, voiceScreenAsk and voiceScreenAlways are its stops.
	voiceScreenOff    = "off"
	voiceScreenAsk    = "ask"
	voiceScreenAlways = "always"
	// voiceScreenLabelMax bounds a label in the prompt line: file names and
	// page titles may be anything.
	voiceScreenLabelMax = 60
)

// voiceScreenOptions are the setting's stops.
var voiceScreenOptions = []setOption{
	{Value: voiceScreenOff, Label: "Off"},
	{Value: voiceScreenAsk, Label: "When you ask about it"},
	{Value: voiceScreenAlways, Label: "Every question"},
}

// voiceScreen is the one picture the orb may take. pending asks the next
// frame for it; pic is the frame, held until a question takes it or the orb
// goes away.
var voiceScreen struct {
	pending bool
	pic     *image.RGBA
}

// ── The setting ──────────────────────────────────────────────────────────

// voiceScreenMode is the stored stop, voiceScreenAsk when unset or unknown.
func voiceScreenMode(c *Config) string {
	if c == nil {
		return voiceScreenAsk
	}
	switch v := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, voiceScreenKey))); v {
	case voiceScreenOff, voiceScreenAsk, voiceScreenAlways:
		return v
	}
	return voiceScreenAsk
}

// voiceScreenSettingsRows is the Voice & AI row.
func voiceScreenSettingsRows() []setRow {
	return []setRow{{
		ID: voiceScreenKey, Label: "Let Juka see the screen", Kind: setKindChoice,
		Detail:  voiceScreenDetail,
		Options: func(*Config) []setOption { return voiceScreenOptions },
		Current: func(c *Config) int { return ttsOptionIndex(voiceScreenOptions, voiceScreenMode(c)) },
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom[voiceScreenKey] = value
			saveUserConfigDebounced(userConfigFromVariables(c))
			if value == voiceScreenOff {
				voiceScreenDrop()
			}
			setToast("Let Juka see the screen: " + ttsOptionLabel(voiceScreenOptions, value))
		},
	}}
}

// voiceScreenDetail is the row's second line.
func voiceScreenDetail(c *Config) string {
	if voiceScreenMode(c) == voiceScreenOff {
		return "Juka doesn't know what's on screen when you ask the voice orb"
	}
	return "A picture of the screen goes to Groq with your question and isn't kept. Needs a Groq key — offline answers only get the screen's name"
}

// ── Taking the picture ───────────────────────────────────────────────────

// voiceScreenRequest asks the next frame for a picture of the screen; the orb
// calls it when it starts listening. Off, or over a paused game (the game
// assistant has its own picture, gameassist.go), it asks for none.
func voiceScreenRequest(c *Config) {
	voiceScreenDrop()
	if voiceScreenMode(c) == voiceScreenOff || gaOverlay.active {
		return
	}
	voiceScreen.pending = true
}

// voiceScreenDrop forgets the picture and any request for one.
func voiceScreenDrop() {
	voiceScreen.pending = false
	voiceScreen.pic = nil
}

// voiceScreenCapture reads the frame drawn so far, the orb not yet on it,
// when a picture was asked for. voiceOrbRender calls it first thing every
// frame; without a request it costs a comparison. A picture the orb no longer
// needs (it went away) is dropped here.
func voiceScreenCapture(renderer *sdl.Renderer) {
	if voiceOrb.phase == orbHidden {
		voiceScreenDrop()
		return
	}
	if !voiceScreen.pending || renderer == nil {
		return
	}
	if voiceOrb.phase != orbListening && voiceOrb.phase != orbThinking {
		voiceScreen.pending = false
		return
	}
	// The share's encoder still owns the redaction list; the next frame.
	if castUI.busy.Load() {
		return
	}
	voiceScreen.pending = false
	// An enlarged pairing code may be what is on screen: no picture.
	if qrZoomActive() {
		return
	}
	voiceScreen.pic = voiceScreenRead(renderer)
}

// voiceScreenRead copies the viewport into a new RGBA picture, with what a
// shared frame hides painted over. nil when the screen cannot be read.
// ABGR8888 is R, G, B, A in memory on these little-endian machines, the
// layout image.RGBA keeps (castReadFrame reads the screen the same way).
func voiceScreenRead(renderer *sdl.Renderer) *image.RGBA {
	ow, oh, err := renderer.GetOutputSize()
	if err != nil || ow <= 0 || oh <= 0 {
		return nil
	}
	w, h := int(ow), int(oh)
	vp := renderer.GetViewport()
	sx, sy := renderer.GetScale()
	if sx <= 0 || sy <= 0 {
		sx, sy = 1, 1
	}
	if vp.W > 0 && vp.H > 0 {
		if pw := int(float32(vp.W) * sx); pw > 0 && pw < w {
			w = pw
		}
		if ph := int(float32(vp.H) * sy); ph > 0 && ph < h {
			h = ph
		}
	}
	stride := int(ow) * 4
	pix := make([]byte, stride*int(oh))
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ABGR8888, unsafe.Pointer(&pix[0]), stride); err != nil {
		logWarn("voice", "could not read the screen for Juka: %v", err)
		return nil
	}
	castRedact(pix, stride, w, h, castShareRedactions(sx, sy))
	return &image.RGBA{Pix: pix, Stride: stride, Rect: image.Rect(0, 0, w, h)}
}

// voiceScreenOpaque sets every pixel's alpha to opaque: a renderer's alpha
// channel means nothing on screen, and visionScale would lay a clear pixel
// on white. Any goroutine, on a picture it owns.
func voiceScreenOpaque(pic *image.RGBA) {
	w := pic.Rect.Dx()
	for y := 0; y < pic.Rect.Dy(); y++ {
		row := pic.Pix[y*pic.Stride : y*pic.Stride+w*4]
		for i := 3; i < len(row); i += 4 {
			row[i] = 255
		}
	}
}

// ── Asking with it ───────────────────────────────────────────────────────

// voiceScreenShot is what one question takes along: the picture, when it
// goes to Groq with it, and the line about where the user is.
type voiceScreenShot struct {
	pic   *image.RGBA
	where string // "The user is on …", "" with the setting off
	about bool   // the question points at the screen
}

// voiceScreenFor takes the picture for a question, or drops it when the
// question needs none. cloud says Groq will answer. Main thread.
func voiceScreenFor(c *Config, question string, cloud bool) voiceScreenShot {
	pic := voiceScreen.pic
	voiceScreenDrop()
	mode := voiceScreenMode(c)
	if mode == voiceScreenOff {
		return voiceScreenShot{}
	}
	shot := voiceScreenShot{where: voiceScreenWhere(c), about: voiceScreenAsksAbout(voiceNormalize(question))}
	if cloud && pic != nil && (mode == voiceScreenAlways || shot.about) {
		shot.pic = pic
		if voiceOrb.phase == orbThinking {
			voiceOrb.title = "Looking at the screen…"
		}
	}
	return shot
}

// prompt is what the shot adds to Juka's system prompt: "" or text that
// starts with a space.
func (s voiceScreenShot) prompt() string {
	out := ""
	if s.where != "" {
		out = " " + s.where
	}
	if s.pic != nil {
		out += voiceScreenPicturePrompt
	}
	return out
}

// voiceScreenPicturePrompt is the system prompt's sentence about the picture.
// voiceScreenGroqAsk takes it out again when the question goes without one.
const voiceScreenPicturePrompt = " A screenshot of the JukaHub screen they are looking at, taken as they started speaking, comes with the question (your own voice bubble is not in it): use it when the question is about what is on screen, read any text in it they ask about, and otherwise ignore it."

// offlineQuestion is the question the offline model gets: it cannot see a
// picture, so a question about the screen starts with where the user is.
// Pure.
func (s voiceScreenShot) offlineQuestion(question string) string {
	if !s.about || s.where == "" {
		return question
	}
	return "(" + s.where + ") " + question
}

// voiceScreenGroqAsk is chatGroqAsk for the orb, with the picture when the
// shot holds one: the vision model is asked first, with messages' system
// prompt and question, and plain text is asked when it fails before any of
// its answer streamed. Blocking; the orb's request goroutine.
func voiceScreenGroqAsk(ctx context.Context, shot voiceScreenShot, key string, models []string, messages []map[string]string, onDelta func(string)) (string, error) {
	if shot.pic != nil && len(messages) >= 2 {
		voiceScreenOpaque(shot.pic)
		img, err := jukaVisionFromImage(shot.pic)
		shot.pic = nil
		if err == nil {
			streamed := false
			reply, verr := groqVisionAsk(ctx, key, messages[0]["content"], messages[len(messages)-1]["content"], []jukaVisionImage{img}, func(partial string) {
				if partial != "" {
					streamed = true
				}
				onDelta(partial)
			})
			if verr == nil || streamed || ctx.Err() != nil {
				return reply, verr
			}
			logWarn("voice", "Juka could not look at the screen (%v); asking without the picture", verr)
		} else {
			logWarn("voice", "could not prepare the screen for Juka: %v", err)
		}
		// Asked without the picture: the system prompt must not promise one,
		// or Juka answers as if it could see. A copy — the caller's slice is
		// not ours to change.
		plain := make([]map[string]string, len(messages))
		copy(plain, messages)
		plain[0] = map[string]string{"role": messages[0]["role"],
			"content": strings.Replace(messages[0]["content"], voiceScreenPicturePrompt, "", 1)}
		messages = plain
	}
	return chatGroqAsk(key, models, messages, onDelta)
}

// voiceScreenTakeQuestion hands a sentence that is plainly a question about
// the screen to Juka before the command parser reads it, and reports whether
// it did. A taught voice shortcut keeps its phrase. Main thread.
func voiceScreenTakeQuestion(c *Config, text string) bool {
	// Off is the voice orb as it was before it could see: the sentence goes
	// the command way, and Juka gets it only as an unmatched question.
	if voiceScreenMode(c) == voiceScreenOff {
		return false
	}
	in := voiceParse(text)
	if !voiceScreenIsQuestion(in.Heard) {
		return false
	}
	// A phrase the parser knows ("how does this work", "skip this song") is
	// still that command; only what would have been looked up as a name is
	// taken.
	switch in.Kind {
	case voiceIntentNone, voiceIntentOpen, voiceIntentSearch, voiceIntentWebSearch:
	default:
		return false
	}
	// A taught shortcut keeps its phrase, matched as the executor matches it
	// (voiceShortcutExecute): a near-miss counts where a name was being read.
	near := in.Kind == voiceIntentOpen || in.Kind == voiceIntentNone
	if voiceShortcutMatch(voiceShortcutsLoad(c), in.Heard, near) >= 0 {
		return false
	}
	if orbAnswerChoice(groqAPIKey(), IsOnline() || netReconnecting(), offlineAIReady()) == "" {
		return false
	}
	logDebug("voice", "orb: a question about the screen")
	voiceOrbAsk(text, false)
	return true
}

// ── Questions about the screen ───────────────────────────────────────────

// voiceScreenPointers are the words that point at something on screen.
var voiceScreenPointers = map[string]bool{
	"this": true, "that": true, "these": true, "those": true, "here": true,
	"screen": true, "highlighted": true, "selected": true, "onscreen": true,
}

// voiceScreenPhrases point at the screen as a phrase.
var voiceScreenPhrases = [...]string{
	"looking at", "which one", "which of these", "what does it say", "what does it mean",
	"read it", "can you see", "do you see", "what you see", "you are seeing", "on the left",
	"on the right", "at the top", "at the bottom", "where am i", "what should i do",
	"what do i do", "what is happening", "what is going on", "what am i seeing",
}

// voiceScreenQuestionStarts open a question or a request to read, explain or
// describe — never a command that does something to "this".
var voiceScreenQuestionStarts = map[string]bool{
	"what": true, "who": true, "whose": true, "why": true, "how": true, "when": true,
	"where": true, "which": true, "is": true, "are": true, "was": true, "does": true,
	"do": true, "should": true, "tell": true, "explain": true, "describe": true,
	"translate": true, "read": true, "summarize": true, "summarise": true, "define": true,
	"help": true,
}

// voiceScreenAsksAbout reports whether a folded question points at what is
// on screen (voiceNormalize's text). Pure.
func voiceScreenAsksAbout(folded string) bool {
	if folded == "" {
		return false
	}
	for _, w := range strings.Fields(folded) {
		if voiceScreenPointers[w] {
			return true
		}
	}
	padded := " " + folded + " "
	for _, p := range voiceScreenPhrases {
		if strings.Contains(padded, " "+p+" ") {
			return true
		}
	}
	return false
}

// voiceScreenIsQuestion reports whether a folded sentence is plainly a
// question about the screen — it opens like a question or a request to read
// or explain, it points at the screen, and it is no screenshot command —
// so it goes to Juka rather than being searched for. Pure.
func voiceScreenIsQuestion(folded string) bool {
	words := strings.Fields(folded)
	if len(words) < 2 || !voiceScreenQuestionStarts[words[0]] {
		return false
	}
	if strings.Contains(folded, "screenshot") {
		return false
	}
	return voiceScreenAsksAbout(folded)
}

// ── Where the user is ────────────────────────────────────────────────────

// voiceScreenWhere is the line about the screen on show, from what is
// already known: the scene's name, the Settings section and row, the Home
// tile, or the focused control. Main thread; it never blocks.
func voiceScreenWhere(c *Config) string {
	if c == nil || currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return ""
	}
	name := c.Scenes[currentSceneIndex].Name
	screen := sceneDisplayName(name)
	section, focused := "", ""
	switch {
	case homeLayoutActive && homeLayout != nil:
		screen = "Home"
		id := homeLayout.Focus.Current()
		focused = voiceScreenHomeTile(id)
		if it, ok := homeLayout.Items[id]; ok && it.Title != "" {
			focused = it.Title
		}
	case setIsHubScene(name) && setHub.scene == name:
		screen = "Settings"
		if setHub.onSearch {
			// The rail's Search entry (setsearch.go): setHub.section
			// still names the last section, which is not on show.
			section = "Search"
		} else if secs := setSections(); setHub.section >= 0 && setHub.section < len(secs) {
			section = secs[setHub.section].Title
			if !setHub.inPane {
				focused = section
			}
		}
		if setHub.inPane && setHub.row >= 0 && setHub.row < len(setHub.rows) {
			focused = setHub.rows[setHub.row].Label
		}
	case focusEngine != nil && focusEngine.SceneName() == name:
		if n, ok := focusEngine.Current(); ok && n.Label != n.Type {
			focused = n.Label
		}
	}
	return voiceScreenWhereLine(screen, section, focused)
}

// voiceScreenHomeTile names a Carousel or Widgets tile from its ref (a widget,
// an app, a screen); "" for the add tile. Pure.
func voiceScreenHomeTile(id HomeFocusID) string {
	ref := string(id)
	if ref == homeTileAddRef {
		return ""
	}
	if name := homeTileWidgetName(ref); name != "" {
		if spec, ok := homeWidgetSpecFor(name); ok {
			return spec.Title + " widget"
		}
	}
	return homeAppTitleFromRef(ref)
}

// voiceScreenWhereLine words the line: "The user is on the Settings screen
// (Display & theme); highlighted: Glass." Empty parts are left out; "" when
// the screen has no name. Pure.
func voiceScreenWhereLine(screen, section, focused string) string {
	screen = voiceScreenClean(screen)
	if screen == "" {
		return ""
	}
	section, focused = voiceScreenClean(section), voiceScreenClean(focused)
	line := "The user is on JukaHub's " + screen + " screen"
	if section != "" && !strings.EqualFold(section, screen) {
		line += " (" + section + ")"
	}
	if focused != "" && !strings.EqualFold(focused, screen) && !strings.EqualFold(focused, section) {
		line += "; highlighted: " + focused
	}
	return line + "."
}

// voiceScreenClean is a label fit for the prompt: one line, no quotes that
// would read as an instruction's edge, at most voiceScreenLabelMax runes.
// Pure.
func voiceScreenClean(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '`' || r < ' ' || r == utf8.RuneError:
			return ' '
		}
		return r
	}, s)), " ")
	if utf8.RuneCountInString(s) > voiceScreenLabelMax {
		s = strings.TrimSpace(string([]rune(s)[:voiceScreenLabelMax])) + "…"
	}
	return s
}
