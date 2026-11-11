package main

import (
	"strings"
	"testing"
	"time"
)

// spatialTestKeepGlobals restores the state these tests change: the published
// snapshot, the media DSP, the detected output, the equaliser and the games
// row's cache. While a test runs, the games check answers "not available" at
// once on the test's own goroutine (no real probing), unless the test sets
// its own seams.
func spatialTestKeepGlobals(t *testing.T) {
	t.Helper()
	live := spatialLiveState.Load()
	run := spatialRun
	output := spatialConnectedOutput()
	eq := musicEQ
	loaded := musicPEQLoaded
	spatialGamesAvailMu.Lock()
	avail := spatialGamesAvail
	spatialGamesAvail = spatialGamesAvailState{}
	spatialGamesAvailMu.Unlock()
	availFn, availGo := spatialGamesAvailFn, spatialGamesAvailGo
	spatialGamesAvailFn = func() (bool, string) { return false, "Not in tests" }
	spatialGamesAvailGo = func(run func()) { run() }
	t.Cleanup(func() {
		spatialLiveState.Store(live)
		spatialRun = run
		rememberAudioOutput(output)
		musicEQ = eq
		musicPEQLoaded = loaded
		spatialGamesAvailMu.Lock()
		spatialGamesAvail = avail
		spatialGamesAvailMu.Unlock()
		spatialGamesAvailFn, spatialGamesAvailGo = availFn, availGo
	})
}

// spatialTestToast is the toast on screen.
func spatialTestToast() string {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.Message
}

func spatialTestConfig(custom map[string]interface{}) *Config {
	c := &Config{}
	c.Variables.Custom = custom
	return c
}

func TestSpatialSettingsDefaults(t *testing.T) {
	want := spatialSettings{Mode: "off", Strength: 0.75, Room: "medium", Games: false}
	for _, c := range []*Config{nil, spatialTestConfig(nil), spatialTestConfig(map[string]interface{}{})} {
		if got := spatialSettingsFor(c); got != want {
			t.Fatalf("defaults = %+v, want %+v", got, want)
		}
	}
}

func TestSpatialSettingsParsing(t *testing.T) {
	cases := []struct {
		custom map[string]interface{}
		want   spatialSettings
	}{
		{map[string]interface{}{"spatialMode": "Headphones"}, spatialSettings{"headphones", 0.75, "medium", false}},
		{map[string]interface{}{"spatialMode": "WIDE"}, spatialSettings{"speakers", 0.75, "medium", false}},
		{map[string]interface{}{"spatialMode": " auto "}, spatialSettings{"auto", 0.75, "medium", false}},
		{map[string]interface{}{"spatialMode": "dolby"}, spatialSettings{"off", 0.75, "medium", false}},
		{map[string]interface{}{"spatialStrength": "50"}, spatialSettings{"off", 0.5, "medium", false}},
		{map[string]interface{}{"spatialStrength": 25}, spatialSettings{"off", 0.25, "medium", false}},
		{map[string]interface{}{"spatialStrength": 100.0}, spatialSettings{"off", 1, "medium", false}},
		{map[string]interface{}{"spatialStrength": 0.5}, spatialSettings{"off", 0.5, "medium", false}},
		{map[string]interface{}{"spatialStrength": "100%"}, spatialSettings{"off", 1, "medium", false}},
		{map[string]interface{}{"spatialStrength": 150}, spatialSettings{"off", 1, "medium", false}},
		{map[string]interface{}{"spatialStrength": -5}, spatialSettings{"off", 0, "medium", false}},
		{map[string]interface{}{"spatialStrength": "loud"}, spatialSettings{"off", 0.75, "medium", false}},
		{map[string]interface{}{"spatialRoom": "LARGE"}, spatialSettings{"off", 0.75, "large", false}},
		{map[string]interface{}{"spatialRoom": "tiny"}, spatialSettings{"off", 0.75, "medium", false}},
		{map[string]interface{}{"spatialGames": "on"}, spatialSettings{"off", 0.75, "medium", true}},
		{map[string]interface{}{"spatialGames": true}, spatialSettings{"off", 0.75, "medium", true}},
		{map[string]interface{}{"spatialGames": "0"}, spatialSettings{"off", 0.75, "medium", false}},
		// A hand-edited key in other casing is still read.
		{map[string]interface{}{"SpatialMode": "speakers"}, spatialSettings{"speakers", 0.75, "medium", false}},
	}
	for _, c := range cases {
		if got := spatialSettingsFor(spatialTestConfig(c.custom)); got != c.want {
			t.Fatalf("%v: got %+v, want %+v", c.custom, got, c.want)
		}
	}
}

func TestSpatialRoomIndex(t *testing.T) {
	for room, want := range map[string]int{"small": 0, "medium": 1, "large": 2, "": 1, "Big": 2, "?": 1} {
		if got := spatialRoomIndex(room); got != want {
			t.Fatalf("spatialRoomIndex(%q) = %d, want %d", room, got, want)
		}
	}
}

func TestSpatialEffectiveMode(t *testing.T) {
	spatialTestKeepGlobals(t)
	for _, mode := range []string{"off", "auto", "headphones", "speakers"} {
		if got := spatialEffectiveMode(spatialSettings{Mode: mode, Strength: 0}); got != spatialDSPOff {
			t.Fatalf("%s at strength 0 = %d, want off", mode, got)
		}
	}
	fixed := map[string]int{"off": spatialDSPOff, "headphones": spatialDSPHeadphones, "speakers": spatialDSPSpeakers, "junk": spatialDSPOff}
	for mode, want := range fixed {
		if got := spatialEffectiveMode(spatialSettings{Mode: mode, Strength: 0.5}); got != want {
			t.Fatalf("%s = %d, want %d", mode, got, want)
		}
	}
	auto := map[string]int{
		"":                   spatialDSPSpeakers, // the built-in pair
		"Headphones":         spatialDSPHeadphones,
		"WH-1000XM4":         spatialDSPHeadphones,
		"Galaxy Buds2 Pro":   spatialDSPHeadphones,
		"JBL Flip 6 Speaker": spatialDSPSpeakers,
		"Bose SoundLink":     spatialDSPSpeakers,
		"Living room Sonos":  spatialDSPSpeakers,
	}
	for output, want := range auto {
		rememberAudioOutput(output)
		if got := spatialEffectiveMode(spatialSettings{Mode: "auto", Strength: 0.75}); got != want {
			t.Fatalf("auto with output %q = %d, want %d", output, got, want)
		}
		if got := spatialAutoMode(output); got != want {
			t.Fatalf("spatialAutoMode(%q) = %d, want %d", output, got, want)
		}
	}
}

func TestSpatialBindPublishesOnlyChanges(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialLiveState.Store(nil)
	c := spatialTestConfig(map[string]interface{}{
		"spatialMode": "headphones", "spatialStrength": 50, "spatialRoom": "small", "musicBitPerfect": true,
	})
	spatialBind(c)
	live := spatialLiveState.Load()
	want := spatialLive{mode: spatialDSPHeadphones, strength: 0.5, room: spatialDSPRoomSmall, bitPerfect: true}
	if live == nil || *live != want {
		t.Fatalf("bind published %+v, want %+v", live, want)
	}
	spatialBind(c)
	if spatialLiveState.Load() != live {
		t.Fatal("an unchanged bind replaced the snapshot")
	}
	c.Variables.Custom["musicBitPerfect"] = false
	spatialBind(c)
	if got := spatialLiveState.Load(); got == live || got.bitPerfect {
		t.Fatalf("turning Bit-perfect off was not published: %+v", got)
	}
	spatialBind(nil) // must not panic or clear the snapshot
	if spatialLiveState.Load() == nil {
		t.Fatal("a nil config cleared the snapshot")
	}
}

func spatialTestPCM(frames int) []byte {
	return spatialTestS16(spatialTestNoise(frames, 0.3))
}

func TestSpatialQueueHookOffIsUntouched(t *testing.T) {
	spatialTestKeepGlobals(t)
	pcm := spatialTestPCM(64)
	orig := string(pcm)
	for _, live := range []*spatialLive{
		nil,
		{mode: spatialDSPOff, strength: 0.75},
		{mode: spatialDSPHeadphones, strength: 0},
		{mode: spatialDSPHeadphones, strength: 0.75, bitPerfect: true},
	} {
		spatialLiveState.Store(live)
		spatialRun = spatialRunState{}
		out, scaled := spatialQueueHook(pcm, 48000, 0.5)
		if scaled || &out[0] != &pcm[0] || string(out) != orig {
			t.Fatalf("live %+v: the hook touched the buffer (scaled %v)", live, scaled)
		}
	}
}

func TestSpatialQueueHookProcessesAndRebuilds(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialRun = spatialRunState{}
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.75, room: spatialDSPRoomMedium})
	pcm := spatialTestPCM(100)
	out, scaled := spatialQueueHook(pcm, 48000, 0.7)
	if !scaled || len(out) != len(pcm) || &out[0] == &pcm[0] {
		t.Fatalf("active hook: scaled %v, %d bytes of %d, own buffer %v", scaled, len(out), len(pcm), &out[0] != &pcm[0])
	}
	first := spatialRun.dsp
	if first == nil || spatialRun.rate != 48000 || first.mode != spatialDSPHeadphones {
		t.Fatalf("no DSP built for 48 kHz headphones: %+v", spatialRun)
	}
	// Strength changes in place; rate, mode and room changes rebuild.
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.25, room: spatialDSPRoomMedium})
	spatialQueueHook(pcm, 48000, 1)
	if spatialRun.dsp != first || first.strength != 0.25 {
		t.Fatalf("a strength change rebuilt the DSP or was not applied (%v)", first.strength)
	}
	spatialQueueHook(pcm, 44100, 1)
	if spatialRun.dsp == first || spatialRun.rate != 44100 || spatialRun.dsp.fs != 44100 {
		t.Fatal("a rate change did not rebuild the DSP")
	}
	second := spatialRun.dsp
	spatialLiveState.Store(&spatialLive{mode: spatialDSPSpeakers, strength: 0.25, room: spatialDSPRoomMedium})
	spatialQueueHook(pcm, 44100, 1)
	if spatialRun.dsp == second || spatialRun.dsp.mode != spatialDSPSpeakers {
		t.Fatal("a mode change did not rebuild the DSP")
	}
	third := spatialRun.dsp
	// The room shapes only the headphone model: in the speakers mode a room
	// change keeps the DSP (no rebuild, no reset of the side filter) ...
	spatialLiveState.Store(&spatialLive{mode: spatialDSPSpeakers, strength: 0.25, room: spatialDSPRoomLarge})
	spatialQueueHook(pcm, 44100, 1)
	if spatialRun.dsp != third {
		t.Fatal("a room change in the speakers mode rebuilt the DSP")
	}
	// ... the headphone model is built with the room in force ...
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.25, room: spatialDSPRoomLarge})
	spatialQueueHook(pcm, 44100, 1)
	fourth := spatialRun.dsp
	if fourth == third || fourth.mode != spatialDSPHeadphones || fourth.room != spatialDSPRoomLarge {
		t.Fatal("switching to headphones did not build the large-room model")
	}
	// ... and rebuilt when the room changes there.
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.25, room: spatialDSPRoomSmall})
	spatialQueueHook(pcm, 44100, 1)
	if spatialRun.dsp == fourth || spatialRun.dsp.room != spatialDSPRoomSmall {
		t.Fatal("a room change in the headphone mode did not rebuild the DSP")
	}
}

// Bytes the DSP holds back (a read that ended mid-frame) stay in the stream
// when it is switched off: they lead the next buffer, which is cut to whole
// frames again, or every later sample would slip.
func TestSpatialQueueHookHandsBackHeldBytes(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialRun = spatialRunState{}
	spatialLiveState.Store(&spatialLive{mode: spatialDSPSpeakers, strength: 0.75, room: spatialDSPRoomMedium})
	out, scaled := spatialQueueHook([]byte{1, 2, 3, 4, 5, 6}, 48000, 1)
	if !scaled || len(out) != 4 {
		t.Fatalf("6 bytes in: %d out (scaled %v), want 4", len(out), scaled)
	}
	// The held partial frame survives a rebuild for new settings.
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.75, room: spatialDSPRoomMedium})
	if out, _ := spatialQueueHook([]byte{7}, 48000, 1); len(out) != 0 || spatialRun.dsp.carryN != 3 {
		t.Fatalf("after a rebuild: %d bytes out, %d held; want 0 and 3", len(out), spatialRun.dsp.carryN)
	}
	// Off: the held 5 6 7 lead the stream; the DSP goes, the new partial frame
	// waits.
	spatialLiveState.Store(&spatialLive{mode: spatialDSPOff})
	out, scaled = spatialQueueHook([]byte{8, 9, 10, 11}, 48000, 1)
	if scaled || string(out) != string([]byte{5, 6, 7, 8}) || spatialRun.dsp != nil || spatialRun.carryN != 3 {
		t.Fatalf("off: got %v (scaled %v, DSP kept %v, %d held), want 5 6 7 8 with 3 held",
			out, scaled, spatialRun.dsp != nil, spatialRun.carryN)
	}
	out, _ = spatialQueueHook([]byte{12, 13, 14, 15, 16, 17}, 48000, 1)
	if string(out) != string([]byte{9, 10, 11, 12, 13, 14, 15, 16}) || spatialRun.carryN != 1 {
		t.Fatalf("next buffer: %v (%d held), want 9..16 with 17 held", out, spatialRun.carryN)
	}
	// A clear (seek or stop) drops held bytes instead.
	spatialResetLocked()
	pcm := []byte{3, 4, 5, 6}
	if out, _ := spatialQueueHook(pcm, 48000, 1); len(out) != 4 || &out[0] != &pcm[0] {
		t.Fatalf("after a reset: %v, want the 4 bytes given, as given", out)
	}
}

// Switching off and on again within one stream starts a clean DSP: no old
// delay lines, filter states or reverb tail replayed into the new audio.
func TestSpatialQueueHookReenableStartsClean(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialRun = spatialRunState{}
	on := &spatialLive{mode: spatialDSPHeadphones, strength: 1, room: spatialDSPRoomLarge}
	spatialLiveState.Store(on)
	spatialQueueHook(spatialTestPCM(2400), 48000, 1)
	spatialLiveState.Store(&spatialLive{mode: spatialDSPOff})
	spatialQueueHook(spatialTestPCM(64), 48000, 1)
	if spatialRun.dsp != nil {
		t.Fatal("bypassing kept the old DSP")
	}
	spatialLiveState.Store(on)
	pcm := spatialTestPCM(400)
	got, scaled := spatialQueueHook(pcm, 48000, 1)
	want := newSpatialDSP(48000, spatialDSPHeadphones, 1, spatialDSPRoomLarge).processS16(pcm, 1)
	if !scaled || string(got) != string(want) {
		t.Fatal("the re-enabled DSP's output differs from a fresh one's")
	}
}

// While bypassed, odd-length reads are handed on as whole frames, unchanged,
// so a DSP switched on mid-stream starts on a frame boundary and pairs the
// right bytes into samples.
func TestSpatialBypassKeepsFrameAlignment(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialRun = spatialRunState{}
	spatialLiveState.Store(&spatialLive{mode: spatialDSPOff})
	pcm := spatialTestPCM(300)
	var passed []byte
	pos := 0
	for _, n := range []int{1, 6, 3, 7, 2} {
		out, scaled := spatialQueueHook(pcm[pos:pos+n], 48000, 1)
		if scaled || len(out)%4 != 0 {
			t.Fatalf("bypass handed on %d bytes (scaled %v): not whole frames", len(out), scaled)
		}
		passed = append(passed, out...)
		pos += n
	}
	if string(passed) != string(pcm[:16]) || spatialRun.carryN != 3 {
		t.Fatalf("bypassed %d bytes with %d held; want the first 16 unchanged and 3 held", len(passed), spatialRun.carryN)
	}
	spatialLiveState.Store(&spatialLive{mode: spatialDSPHeadphones, strength: 0.75, room: spatialDSPRoomMedium})
	got, scaled := spatialQueueHook(pcm[pos:], 48000, 1)
	want := newSpatialDSP(48000, spatialDSPHeadphones, 0.75, spatialDSPRoomMedium).processS16(pcm[16:], 1)
	if !scaled || string(got) != string(want) || spatialRun.dsp.carryN != 0 {
		t.Fatal("the DSP switched on mid-stream did not start on the held frame boundary")
	}
}

// The games row never runs its check on the render thread: the first ask
// shows "Checking…" and queues one check; the answer fills the row.
func TestSpatialGamesRowAsksOffTheRenderThread(t *testing.T) {
	spatialTestKeepGlobals(t)
	clearToastAfterTest(t)
	answerOK, answerWhy := false, "Checking whether RetroArch can load audio filters…"
	calls := 0
	spatialGamesAvailFn = func() (bool, string) {
		calls++
		return answerOK, answerWhy
	}
	var queued []func()
	spatialGamesAvailGo = func(run func()) { queued = append(queued, run) }
	c := spatialTestConfig(map[string]interface{}{"spatialMode": "headphones"})
	value := func() string {
		v, _ := spatialPanelValue(msRowSpatialGames, c)
		return v
	}
	if v := value(); v != "Checking…" || calls != 0 || len(queued) != 1 {
		t.Fatalf("first ask: %q, %d checks run inline, %d queued; want Checking…, 0, 1", v, calls, len(queued))
	}
	value() // the next frame, while the check is out, queues nothing more
	if len(queued) != 1 {
		t.Fatalf("%d checks queued while one was out", len(queued))
	}
	queued[0]() // the check answers: RetroArch is still being probed
	if v := value(); v != "Checking…" || calls != 1 {
		t.Fatalf("while probing the row reads %q (%d checks)", v, calls)
	}
	// The probe has an answer once the short wait is over: available.
	answerOK, answerWhy = true, ""
	spatialGamesAvailMu.Lock()
	spatialGamesAvail.at = time.Now().Add(-time.Minute)
	spatialGamesAvailMu.Unlock()
	value()
	if len(queued) != 2 {
		t.Fatalf("a stale answer queued %d checks in all, want 2", len(queued))
	}
	queued[1]()
	if v := value(); v != "Off" {
		t.Fatalf("available and off reads %q", v)
	}
	// Switched on while the mode is Off: the toast says a mode is needed.
	c.Variables.Custom[spatialModeKey] = "off"
	if !spatialPanelAdjust(msRowSpatialGames, 1, c) || !spatialSettingsFor(c).Games {
		t.Fatal("the games row did not switch on")
	}
	if toast := spatialTestToast(); !strings.Contains(toast, "choose a Spatial audio mode") {
		t.Fatalf("toast %q does not say a mode is needed", toast)
	}
	// Unavailable later: the row says so, and the switch can still go off.
	spatialGamesAvailMu.Lock()
	spatialGamesAvail = spatialGamesAvailState{at: time.Now(), have: true, why: "The game audio plugin is not installed"}
	spatialGamesAvailMu.Unlock()
	if v := value(); v != "On · not available" {
		t.Fatalf("unavailable but on reads %q", v)
	}
	spatialPanelAdjust(msRowSpatialGames, 1, c)
	if spatialSettingsFor(c).Games {
		t.Fatal("an unavailable games row could not be switched off")
	}
	if v := value(); v != "Not available" {
		t.Fatalf("unavailable and off reads %q", v)
	}
	spatialPanelAdjust(msRowSpatialGames, 1, c)
	if spatialSettingsFor(c).Games {
		t.Fatal("an unavailable games row switched on")
	}
	if toast := spatialTestToast(); toast != "The game audio plugin is not installed" {
		t.Fatalf("refusal toast %q, want the reason", toast)
	}
}

// A panic in the check becomes an answer, so the row never stays on
// "Checking…".
func TestSpatialGamesCheckPanicIsAnAnswer(t *testing.T) {
	spatialTestKeepGlobals(t)
	spatialGamesAvailFn = func() (bool, string) { panic("probe exploded") }
	spatialPanelValue(msRowSpatialGames, spatialTestConfig(nil))
	ok, why, known := spatialGamesAvailabilityCached()
	if !known || ok || why == "" || spatialGamesChecking(ok, why) {
		t.Fatalf("after a panicking check: ok %v, why %q, known %v", ok, why, known)
	}
}

func TestSpatialNextStrength(t *testing.T) {
	cases := []struct{ cur, dir, want int }{
		{75, 1, 100}, {100, 1, 100}, {75, -1, 50}, {25, -1, 25}, {60, 1, 75}, {60, -1, 50},
		{0, 1, 25}, {0, -1, 25}, {130, -1, 100},
	}
	for _, c := range cases {
		if got := spatialNextStrength(c.cur, c.dir); got != c.want {
			t.Fatalf("spatialNextStrength(%d, %d) = %d, want %d", c.cur, c.dir, got, c.want)
		}
	}
}

func TestSpatialRowsCycleAndStore(t *testing.T) {
	spatialTestKeepGlobals(t)
	c := spatialTestConfig(map[string]interface{}{})
	for _, want := range []string{"auto", "headphones", "speakers", "off", "auto"} {
		spatialCycleMode(c, 1)
		if got := spatialSettingsFor(c).Mode; got != want {
			t.Fatalf("mode cycled to %q, want %q", got, want)
		}
	}
	spatialCycleMode(c, -1)
	if got := spatialSettingsFor(c).Mode; got != "off" {
		t.Fatalf("back from auto went to %q, want off", got)
	}
	if pct := spatialStepStrength(c, 1); pct != 100 || spatialSettingsFor(c).Strength != 1 {
		t.Fatalf("strength up from 75 = %d", pct)
	}
	if room := spatialCycleRoom(c, 1); room != "large" || spatialSettingsFor(c).Room != "large" {
		t.Fatalf("room right from medium = %q", room)
	}
	if room := spatialCycleRoom(c, 1); room != "small" {
		t.Fatalf("room right from large = %q, want small", room)
	}
}

func TestSpatialModeSwitchesBitPerfectOff(t *testing.T) {
	spatialTestKeepGlobals(t)
	// The equaliser counts as loaded, so nothing is read from appConfig.
	musicPEQLoaded = true
	musicEQ.bitPerfect = true
	musicEQ.enabled = false
	c := spatialTestConfig(map[string]interface{}{"musicBitPerfect": true})
	msg := spatialSetMode(c, "headphones")
	if customBool(c.Variables.Custom, musicBitPerfectKey, true) || musicEQ.bitPerfect {
		t.Fatal("choosing a spatial mode left Bit-perfect on")
	}
	if msg == "" || spatialSettingsFor(c).Mode != "headphones" {
		t.Fatalf("mode not stored (%q)", msg)
	}
	// Off never touches Bit-perfect.
	c.Variables.Custom["musicBitPerfect"] = true
	musicEQ.bitPerfect = true
	spatialSetMode(c, "off")
	if !customBool(c.Variables.Custom, musicBitPerfectKey, false) {
		t.Fatal("turning spatial audio off switched Bit-perfect off")
	}
}

func TestSpatialPanelRowsAndValues(t *testing.T) {
	spatialTestKeepGlobals(t)
	rows := musicSettingsRows()
	peq := -1
	for i, r := range rows {
		if r.kind == msRowPEQ {
			peq = i
		}
	}
	want := []musicSettingsRowKind{msRowSpatialMode, msRowSpatialStrength, msRowSpatialRoom, msRowSpatialGames}
	if peq < 0 || peq+len(want) >= len(rows) {
		t.Fatalf("PEQ row at %d of %d rows", peq, len(rows))
	}
	for i, k := range want {
		if rows[peq+1+i].kind != k || rows[peq+1+i].hint == "" {
			t.Fatalf("row %d after PEQ is %v, want %v with a hint", i+1, rows[peq+1+i].kind, k)
		}
	}
	c := spatialTestConfig(map[string]interface{}{"spatialMode": "speakers", "spatialStrength": 50, "spatialRoom": "small"})
	checks := map[musicSettingsRowKind]string{
		msRowSpatialMode: "Speakers", msRowSpatialStrength: "50%", msRowSpatialRoom: "Small",
	}
	for kind, want := range checks {
		if got, ok := spatialPanelValue(kind, c); !ok || got != want {
			t.Fatalf("value of %v = %q (%v), want %q", kind, got, ok, want)
		}
	}
	if _, ok := spatialPanelValue(msRowLayout, c); ok {
		t.Fatal("spatialPanelValue claimed the layout row")
	}
	if spatialPanelAdjust(msRowBackground, 1, c) {
		t.Fatal("spatialPanelAdjust claimed the background row")
	}
	c.Variables.Custom["musicBitPerfect"] = true
	if got := spatialModeValue(c); got != "Speakers · bypassed" {
		t.Fatalf("with Bit-perfect on the mode reads %q", got)
	}
}

func TestSpatialSettingsRowOptions(t *testing.T) {
	row := spatialSettingsRow()
	if row.Kind != setKindChoice || row.Options == nil || row.Current == nil || row.Choose == nil || row.Detail == nil {
		t.Fatalf("incomplete choice row: %+v", row)
	}
	opts := row.Options(nil)
	if len(opts) != 4 || opts[0].Value != "off" || opts[1].Value != "auto" || opts[2].Value != "headphones" || opts[3].Value != "speakers" {
		t.Fatalf("options = %+v", opts)
	}
	c := spatialTestConfig(map[string]interface{}{"spatialMode": "headphones"})
	if got := row.Current(c); got != 2 {
		t.Fatalf("Current = %d, want 2 (headphones)", got)
	}
	if row.Detail(c) == "" || row.Detail(nil) == "" {
		t.Fatal("the row needs a detail line")
	}
}
