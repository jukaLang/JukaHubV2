package main

// voiceorb_test.go — the voice orb without a microphone: when speech starts
// and ends, what counts as a question for Juka, which pads have a Menu button,
// what each kind of outcome puts on the orb, and the Menu button itself.

import (
	"runtime"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// orbResetForTest puts the orb away before and after a test.
func orbResetForTest(t *testing.T) {
	t.Helper()
	voiceOrbHideNow()
	voiceOrb.menuTaken, voiceOrb.keyTaken, voiceOrb.mouseTaken = false, false, false
	t.Cleanup(func() {
		voiceOrbHideNow()
		voiceOrb.menuTaken, voiceOrb.keyTaken, voiceOrb.mouseTaken = false, false, false
	})
}

// A quiet room: the floor is learnt and nothing is speech. A word is, the pause
// after it starts the quiet clock, and the next word stops it.
func TestOrbVADFindsSpeechAndThePauseAfterIt(t *testing.T) {
	var v orbVAD
	now := uint64(0)
	for i := 0; i < 30; i++ {
		v.feed(0.002, now)
		now += 33
	}
	if v.speaking {
		t.Fatal("room noise counted as speech")
	}
	if th := v.threshold(); th != orbSpeechMin {
		t.Errorf("a quiet room's threshold = %v, want the minimum %v", th, orbSpeechMin)
	}
	v.feed(0.04, now)
	if !v.speaking {
		t.Fatal("a word at 0.04 was not heard")
	}
	now += 33
	v.feed(0.002, now)
	if v.quietSince != now {
		t.Fatalf("quietSince = %d, want %d", v.quietSince, now)
	}
	v.feed(0.04, now+33)
	if v.quietSince != 0 {
		t.Error("speaking again did not clear the pause")
	}
}

// A fan that keeps running is learnt as the floor, so it is not speech, while
// a voice over it still is.
func TestOrbVADLearnsANoisyRoom(t *testing.T) {
	var v orbVAD
	for i := 0; i < 200; i++ {
		v.feed(0.018, uint64(i*33))
	}
	if v.speaking {
		t.Fatalf("a steady fan at 0.018 counted as speech (threshold %v)", v.threshold())
	}
	if th := v.threshold(); th > 2*voiceSpeechLevel+1e-9 {
		t.Errorf("threshold %v is above its cap %v", th, 2*voiceSpeechLevel)
	}
	v.feed(0.09, 7000)
	if !v.speaking {
		t.Error("a voice over the fan was not heard")
	}
}

func TestOrbLooksLikeQuestion(t *testing.T) {
	for heard, want := range map[string]bool{
		"what is the capital of france": true,
		"how far is the moon":           true,
		"tell me a joke":                true,
		"is it going to rain":           true,
		"mario kart":                    false,
		"youtube":                       false,
		"the legend of zelda":           true, // four words: a sentence
		"":                              false,
	} {
		if got := orbLooksLikeQuestion(heard); got != want {
			t.Errorf("orbLooksLikeQuestion(%q) = %v, want %v", heard, got, want)
		}
	}
}

// A name that matched nothing goes to Juka only when it reads as a question;
// a verb, a match or a list keeps the command's own answer.
func TestVoiceOrbWantsJukaOnlyForQuestions(t *testing.T) {
	in := voiceParse("open the capital of france")
	if voiceOrbWantsJuka(in, voiceOutcome{Say: "Nothing called x", Search: "x"}) {
		t.Error(`"open …" went to Juka`)
	}
	in = voiceParse("what is the capital of france")
	if in.Kind != voiceIntentOpen || in.Verb != "" {
		t.Fatalf("the parser read a question as %+v; the Juka hand-off expects a bare name", in)
	}
	if voiceOrbWantsJuka(in, voiceOutcome{Act: func(*Config) {}}) {
		t.Error("a question that matched a screen went to Juka")
	}
	if voiceOrbWantsJuka(in, voiceOutcome{Choices: []voiceMatch{{}}}) {
		t.Error("a question with choices went to Juka")
	}
}

func TestOrbPlain(t *testing.T) {
	got := orbPlain("**Paris** is the capital.\n- It has `2M` people.\n# Note")
	if want := "Paris is the capital. It has 2M people. Note"; got != want {
		t.Errorf("orbPlain = %q, want %q", got, want)
	}
}

func TestOrbReadMs(t *testing.T) {
	if got := orbReadMs("Volume 50%", false, false); got != orbAnswerMinMs {
		t.Errorf("a short answer stays %d ms, want the minimum %d", got, orbAnswerMinMs)
	}
	if got := orbReadMs("x", true, false); got != orbOfferMinMs {
		t.Errorf("an answer with a fix stays %d ms, want %d", got, orbOfferMinMs)
	}
	long := strings.Repeat("word ", 200)
	if got := orbReadMs(long, false, false); got != orbAnswerMaxMs {
		t.Errorf("a long answer stays %d ms, want the cap %d", got, orbAnswerMaxMs)
	}
	if got := orbReadMs(long, false, true); got != orbAIAnswerMaxMs {
		t.Errorf("a long answer from Juka stays %d ms, want its cap %d", got, orbAIAnswerMaxMs)
	}
}

// The TrimUI pad maps Select (back) and Menu (guide) apart, so GUIDE is the
// Menu button; a pad that maps no Select reports it as GUIDE, which then
// stays Back.
func TestOrbMappingHasMenu(t *testing.T) {
	trimui := "030000005e0400008e02000014010000,X360 Controller,a:b0,b:b1,back:b6,dpdown:h0.4,dpleft:h0.8," +
		"dpright:h0.2,dpup:h0.1,guide:b8,leftshoulder:b4,leftstick:b9,lefttrigger:a2,leftx:a0,lefty:a1," +
		"rightshoulder:b5,rightstick:b10,righttrigger:a5,rightx:a3,righty:a4,start:b7,x:b2,y:b3,platform:Linux,"
	if !orbMappingHasMenu(trimui) {
		t.Error("the TrimUI pad's GUIDE is not taken for its Menu button")
	}
	if orbMappingHasMenu("0300,Odd pad,a:b0,b:b1,guide:b6,start:b7,platform:Linux,") {
		t.Error("a pad without a Select of its own lost GUIDE as Back")
	}
	if !orbMappingHasMenu("") {
		t.Error("no mapping at all should count as a Menu button")
	}
}

// Each kind of outcome: a command waits out the pause, a name with several
// matches lists them (four at most), an answer stays to be read with its fix,
// and "cancel" lets the orb go.
func TestVoiceOrbShowsEachOutcome(t *testing.T) {
	orbResetForTest(t)
	voiceOrb.heard = "open youtube"
	ran := false
	voiceOrbShowOutcome(voiceOutcome{Say: "Opening YouTube", Act: func(*Config) { ran = true }})
	if voiceOrb.phase != orbConfirm || voiceOrb.title != "Opening YouTube" || voiceOrb.sub != "“open youtube”" {
		t.Fatalf("a command: phase %d title %q sub %q", voiceOrb.phase, voiceOrb.title, voiceOrb.sub)
	}
	if !voiceOrbModal() {
		t.Error("the confirm pause does not own the controls (B could not stop it)")
	}
	voiceOrbRunPending(nil)
	if !ran || voiceOrb.phase != orbHidden {
		t.Fatalf("running the command: ran %v, phase %d (the orb must be gone first)", ran, voiceOrb.phase)
	}

	many := make([]voiceMatch, 6)
	for i := range many {
		many[i] = voiceMatch{Target: voiceTarget{Title: "Game " + string(rune('A'+i))}}
	}
	voiceOrbShowOutcome(voiceOutcome{Say: "Which one did you mean?", Choices: many})
	if voiceOrb.phase != orbChoices || len(voiceOrb.choices) != orbMaxChoiceRows {
		t.Fatalf("choices: phase %d, %d rows", voiceOrb.phase, len(voiceOrb.choices))
	}
	voiceOrbMove(1)
	voiceOrbMove(10)
	if voiceOrb.cursor != orbMaxChoiceRows-1 {
		t.Errorf("cursor = %d, want the last row %d", voiceOrb.cursor, orbMaxChoiceRows-1)
	}

	voiceOrbShowOutcome(voiceOutcome{Say: "Volume 50%", Done: true})
	if voiceOrb.phase != orbAnswer || voiceOrb.tone != tbxToneGood || voiceOrb.hideAt <= voiceOrb.phaseAt {
		t.Fatalf("a done answer: phase %d tone %d hideAt %d phaseAt %d", voiceOrb.phase, voiceOrb.tone,
			voiceOrb.hideAt, voiceOrb.phaseAt)
	}
	if voiceOrbModal() {
		t.Error("an answer owns the controls; it should only linger")
	}

	voiceOrbShowOutcome(voiceOutcome{Say: "Nothing called “zorp” here", Tone: tbxToneWarn, Search: "zorp"})
	if voiceOrb.offer != orbOfferSearch || voiceOrb.words != "zorp" {
		t.Errorf("no match: offer %d words %q, want the search sheet with the words", voiceOrb.offer, voiceOrb.words)
	}

	voiceOrbShowOutcome(voiceOutcome{Say: "Cancelled", Cancel: true})
	if voiceOrb.fadeAt == 0 || voiceOrbActive() {
		t.Error("cancel did not let the orb go")
	}
}

// The Menu button on a handheld: off in Settings it stays Back; on, it opens
// the orb, which without a Groq key says so with the fix one press away, takes
// the button's release too, and goes at B.
func TestVoiceOrbMenuButton(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Menu button is a handheld's")
	}
	orbResetForTest(t)
	old := appConfig
	t.Cleanup(func() { appConfig = old })
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{voiceOrbMenuKey: false}
	appConfig = config
	press := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_BACK}
	release := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: sdl.CONTROLLER_BUTTON_BACK}
	if voiceOrbController(press, sdl.CONTROLLER_BUTTON_GUIDE, config) {
		t.Fatal("with the setting off, Menu was taken from Back")
	}
	if why := modalPanelBlockReason(); why != "" || virtualKeyboardActive || !voiceOrbCanOpen(config) {
		t.Skipf("another test left the screen covered (%q, keyboard %v)", why, virtualKeyboardActive)
	}
	config.Variables.Custom[voiceOrbMenuKey] = true
	if !voiceOrbController(press, sdl.CONTROLLER_BUTTON_GUIDE, config) {
		t.Fatal("Menu did not open the orb")
	}
	if voiceOrb.phase != orbAnswer || voiceOrb.offer != orbOfferGroqKey {
		t.Fatalf("without a key: phase %d offer %d, want the Groq key fix", voiceOrb.phase, voiceOrb.offer)
	}
	if !voiceOrbController(release, sdl.CONTROLLER_BUTTON_GUIDE, config) {
		t.Error("the release of the Menu press that opened the orb reached the screen")
	}
	if voiceOrbController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X},
		sdl.CONTROLLER_BUTTON_X, config) {
		t.Error("X on a lingering answer was kept from the screen")
	}
	if voiceOrbActive() {
		t.Error("another button did not put the answer away")
	}
}

// The voice key's legend row: F4 on a keyboard, Menu on a handheld, and no
// Menu row once Settings gives Menu back to Back.
func TestVoiceOrbLegendRow(t *testing.T) {
	old := appConfig
	t.Cleanup(func() { appConfig = old })
	appConfig = &Config{}
	if row, ok := voiceOrbLegendRow(true); !ok || row.Button != "F4" {
		t.Errorf("keyboard row = %+v, %v", row, ok)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if row, ok := voiceOrbLegendRow(false); !ok || row.Button != "Menu" {
		t.Errorf("pad row = %+v, %v", row, ok)
	}
	appConfig.Variables.Custom = map[string]interface{}{voiceOrbMenuKey: false}
	if _, ok := voiceOrbLegendRow(false); ok {
		t.Error("Menu is listed as voice with the setting off")
	}
}

// Settings → Controls has the switch, on by default, after the mouse rows.
func TestVoiceOrbSettingsRow(t *testing.T) {
	rows := stickCalSettingsRows()
	found := false
	for i, r := range rows {
		if r.ID != voiceOrbMenuKey {
			continue
		}
		found = true
		if i < 3 {
			t.Errorf("the voice row is at %d, before the virtual cursor's and the mouse rows", i)
		}
		if !r.Get(&Config{}) {
			t.Error("Menu button: voice is off by default")
		}
	}
	if !found {
		t.Fatal("Settings → Controls has no Menu button row")
	}
}
