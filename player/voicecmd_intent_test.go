package main

// voicecmd_intent_test.go — the phrase table behind voice commands: what
// people say (and what Whisper or a phone keyboard writes down) and the intent
// each must become. A row that fails names the folded text too, so a new
// phrase that slips through says where it went.

import "testing"

func TestVoiceParsePhrases(t *testing.T) {
	cases := []struct {
		said string
		want voiceIntent
	}{
		{"Open YouTube.", voiceIntent{Kind: voiceIntentOpen, Target: "youtube", Verb: "open"}},
		{"open you tube", voiceIntent{Kind: voiceIntentOpen, Target: "youtube", Verb: "open"}},
		{"YouTube", voiceIntent{Kind: voiceIntentOpen, Target: "youtube", Verb: ""}},
		{"Hey Juka, open the settings app please.", voiceIntent{Kind: voiceIntentOpen, Target: "settings", Verb: "open"}},
		{"hey juca can you please open settings for me", voiceIntent{Kind: voiceIntentOpen, Target: "settings", Verb: "open"}},
		{"Launch RetroArch", voiceIntent{Kind: voiceIntentOpen, Target: "retroarch", Verb: "launch"}},
		{"open retro arch", voiceIntent{Kind: voiceIntentOpen, Target: "retroarch", Verb: "open"}},
		{"Play Mario Kart 64.", voiceIntent{Kind: voiceIntentOpen, Target: "mario kart 64", Verb: "play"}},
		{"play Kirby's Dream Land", voiceIntent{Kind: voiceIntentOpen, Target: "kirbys dream land", Verb: "play"}},
		{"start jukaland", voiceIntent{Kind: voiceIntentOpen, Target: "jukaland", Verb: "start"}},
		{"open juka land", voiceIntent{Kind: voiceIntentOpen, Target: "jukaland", Verb: "open"}},
		{"go to the browser", voiceIntent{Kind: voiceIntentOpen, Target: "browser", Verb: "go to"}},
		{"take me to my files", voiceIntent{Kind: voiceIntentOpen, Target: "files", Verb: "take me to"}},
		{"show me the trophies", voiceIntent{Kind: voiceIntentOpen, Target: "trophies", Verb: "show me"}},
		{"open the game library", voiceIntent{Kind: voiceIntentOpen, Target: "game library", Verb: "open"}},
		{"I want to play Tetris", voiceIntent{Kind: voiceIntentOpen, Target: "tetris", Verb: "play"}},
		{"let me play zelda", voiceIntent{Kind: voiceIntentOpen, Target: "zelda", Verb: "play"}},
		{"open wifi settings", voiceIntent{Kind: voiceIntentOpen, Target: "wifi settings", Verb: "open"}},
		{"connect to wifi", voiceIntent{Kind: voiceIntentOpen, Target: "wifi", Verb: "connect to"}},
		{"wifi", voiceIntent{Kind: voiceIntentOpen, Target: "wifi", Verb: ""}},
		{"Turn on Wi-Fi.", voiceIntent{Kind: voiceIntentWifi, On: true}},
		{"turn wifi off", voiceIntent{Kind: voiceIntentWifi, On: false}},
		{"disable bluetooth", voiceIntent{Kind: voiceIntentBluetooth, On: false}},
		{"switch on blue tooth", voiceIntent{Kind: voiceIntentBluetooth, On: true}},
		{"wifi on", voiceIntent{Kind: voiceIntentWifi, On: true}},
		{"Set the volume to 50%.", voiceIntent{Kind: voiceIntentVolumeSet, Value: 50}},
		{"volume 30", voiceIntent{Kind: voiceIntentVolumeSet, Value: 30}},
		{"volume to seventy five", voiceIntent{Kind: voiceIntentVolumeSet, Value: 75}},
		{"set volume to one hundred", voiceIntent{Kind: voiceIntentVolumeSet, Value: 100}},
		{"volume at 30 percent", voiceIntent{Kind: voiceIntentVolumeSet, Value: 30}},
		{"turn up the volume", voiceIntent{Kind: voiceIntentVolumeStep, Value: 10}},
		{"turn the volume down", voiceIntent{Kind: voiceIntentVolumeStep, Value: -10}},
		{"louder", voiceIntent{Kind: voiceIntentVolumeStep, Value: 10}},
		{"Quieter, please.", voiceIntent{Kind: voiceIntentVolumeStep, Value: -10}},
		{"volume up by 20", voiceIntent{Kind: voiceIntentVolumeStep, Value: 20}},
		{"turn it down", voiceIntent{Kind: voiceIntentVolumeStep, Value: -10}},
		{"max volume", voiceIntent{Kind: voiceIntentVolumeSet, Value: 100}},
		{"half volume", voiceIntent{Kind: voiceIntentVolumeSet, Value: 50}},
		{"volume down 15", voiceIntent{Kind: voiceIntentVolumeStep, Value: -15}},
		{"mute", voiceIntent{Kind: voiceIntentMute}},
		{"Mute the sound.", voiceIntent{Kind: voiceIntentMute}},
		{"unmute", voiceIntent{Kind: voiceIntentUnmute}},
		{"un mute", voiceIntent{Kind: voiceIntentUnmute}},
		{"brightness 70", voiceIntent{Kind: voiceIntentBrightnessSet, Value: 70}},
		{"make the screen brighter", voiceIntent{Kind: voiceIntentBrightnessStep, Value: 10}},
		{"dim the screen", voiceIntent{Kind: voiceIntentBrightnessStep, Value: -10}},
		{"brightness down", voiceIntent{Kind: voiceIntentBrightnessStep, Value: -10}},
		{"set brightness to maximum", voiceIntent{Kind: voiceIntentBrightnessSet, Value: 100}},
		{"Take a screenshot.", voiceIntent{Kind: voiceIntentScreenshot}},
		{"screen shot", voiceIntent{Kind: voiceIntentScreenshot}},
		{"open screenshots", voiceIntent{Kind: voiceIntentOpen, Target: "screenshots", Verb: "open"}},
		{"turn off the screen", voiceIntent{Kind: voiceIntentSleepScreen}},
		{"sleep", voiceIntent{Kind: voiceIntentSleepScreen}},
		{"quick settings", voiceIntent{Kind: voiceIntentQuickSettings}},
		{"open quick settings", voiceIntent{Kind: voiceIntentQuickSettings}},
		{"Go home.", voiceIntent{Kind: voiceIntentHome}},
		{"home screen", voiceIntent{Kind: voiceIntentHome}},
		{"go back", voiceIntent{Kind: voiceIntentBack}},
		{"close", voiceIntent{Kind: voiceIntentBack}},
		{"Pause the music.", voiceIntent{Kind: voiceIntentMusicPause}},
		{"stop", voiceIntent{Kind: voiceIntentMusicPause}},
		{"resume", voiceIntent{Kind: voiceIntentMusicResume}},
		{"play", voiceIntent{Kind: voiceIntentMusicResume}},
		{"Next song.", voiceIntent{Kind: voiceIntentMusicNext}},
		{"skip", voiceIntent{Kind: voiceIntentMusicNext}},
		{"previous song", voiceIntent{Kind: voiceIntentMusicPrev}},
		{"play some music", voiceIntent{Kind: voiceIntentMusicPlay}},
		{"What time is it?", voiceIntent{Kind: voiceIntentTime}},
		{"what's the time", voiceIntent{Kind: voiceIntentTime}},
		{"what's the date", voiceIntent{Kind: voiceIntentDate}},
		{"what day is it", voiceIntent{Kind: voiceIntentDate}},
		{"How much battery is left?", voiceIntent{Kind: voiceIntentBattery}},
		{"battery", voiceIntent{Kind: voiceIntentBattery}},
		{"search the web for cat videos", voiceIntent{Kind: voiceIntentWebSearch, Query: "cat videos"}},
		{"google weather tomorrow", voiceIntent{Kind: voiceIntentWebSearch, Query: "weather tomorrow"}},
		{"look up pancakes online", voiceIntent{Kind: voiceIntentWebSearch, Query: "pancakes"}},
		{"search for mario", voiceIntent{Kind: voiceIntentSearch, Query: "mario"}},
		{"find my notes", voiceIntent{Kind: voiceIntentSearch, Query: "notes"}},
		{"quit jukahub", voiceIntent{Kind: voiceIntentQuit}},
		{"exit", voiceIntent{Kind: voiceIntentQuit}},
		{"cancel", voiceIntent{Kind: voiceIntentCancel}},
		{"never mind", voiceIntent{Kind: voiceIntentCancel}},
		{"help", voiceIntent{Kind: voiceIntentHelp}},
		{"what can I say", voiceIntent{Kind: voiceIntentHelp}},
		{"", voiceIntent{Kind: voiceIntentNone}},
		{"open", voiceIntent{Kind: voiceIntentNone}},
		{"hey", voiceIntent{Kind: voiceIntentNone}},
		{"Open PS Remote Play.", voiceIntent{Kind: voiceIntentOpen, Target: "ps remote play", Verb: "open"}},
		{"open play station remote play", voiceIntent{Kind: voiceIntentOpen, Target: "playstation remote play", Verb: "open"}},
		{"open the date & time settings", voiceIntent{Kind: voiceIntentOpen, Target: "date time settings", Verb: "open"}},
		{"open Voice command", voiceIntent{Kind: voiceIntentOpen, Target: "voice command", Verb: "open"}},
		{"open the APK store", voiceIntent{Kind: voiceIntentOpen, Target: "apk store", Verb: "open"}},
		{"Okay Juka, launch the emulators.", voiceIntent{Kind: voiceIntentOpen, Target: "emulators", Verb: "launch"}},
		{"open juka hub settings", voiceIntent{Kind: voiceIntentOpen, Target: "jukahub settings", Verb: "open"}},
		{"Turn the brightness up by 20 percent", voiceIntent{Kind: voiceIntentBrightnessStep, Value: 20}},
		{"volume twenty", voiceIntent{Kind: voiceIntentVolumeSet, Value: 20}},
		{"turn up the volume to 80", voiceIntent{Kind: voiceIntentVolumeSet, Value: 80}},
		{"Open YouTube!!!", voiceIntent{Kind: voiceIntentOpen, Target: "youtube", Verb: "open"}},
		{"OPEN SETTINGS", voiceIntent{Kind: voiceIntentOpen, Target: "settings", Verb: "open"}},
		{"um, open YouTube", voiceIntent{Kind: voiceIntentOpen, Target: "youtube", Verb: "open"}},
		{"uh so open the browser", voiceIntent{Kind: voiceIntentOpen, Target: "browser", Verb: "open"}},
		{"open up settings", voiceIntent{Kind: voiceIntentOpen, Target: "settings", Verb: "open up"}},
		{"Juka, what time is it", voiceIntent{Kind: voiceIntentTime}},
		{"turn the volume all the way up", voiceIntent{Kind: voiceIntentVolumeSet, Value: 100}},
		{"Volume: 40%", voiceIntent{Kind: voiceIntentVolumeSet, Value: 40}},
		{"volume up a bit", voiceIntent{Kind: voiceIntentVolumeStep, Value: 10}},
		{"a little louder", voiceIntent{Kind: voiceIntentVolumeStep, Value: 10}},
		{"set volume to fifty percent", voiceIntent{Kind: voiceIntentVolumeSet, Value: 50}},
		{"turn off wifi please", voiceIntent{Kind: voiceIntentWifi, On: false}},
		{"open music", voiceIntent{Kind: voiceIntentOpen, Target: "music", Verb: "open"}},
		{"start", voiceIntent{Kind: voiceIntentNone}},
		{"next", voiceIntent{Kind: voiceIntentMusicNext}},
		{"play the next song", voiceIntent{Kind: voiceIntentMusicNext}},
		{"Show me the weather.", voiceIntent{Kind: voiceIntentOpen, Target: "weather", Verb: "show me"}},
		{"open Live TV", voiceIntent{Kind: voiceIntentOpen, Target: "live tv", Verb: "open"}},
		{"brightness", voiceIntent{Kind: voiceIntentOpen, Target: "brightness", Verb: ""}},
		{"volume", voiceIntent{Kind: voiceIntentOpen, Target: "volume", Verb: ""}},
		{"open sound settings", voiceIntent{Kind: voiceIntentOpen, Target: "sound settings", Verb: "open"}},
		{"Play Super Mario Bros. 3", voiceIntent{Kind: voiceIntentOpen, Target: "super mario bros 3", Verb: "play"}},
		{"launch PPSSPP", voiceIntent{Kind: voiceIntentOpen, Target: "ppsspp", Verb: "launch"}},
		{"open jukahub", voiceIntent{Kind: voiceIntentOpen, Target: "jukahub", Verb: "open"}},
		{"Search YouTube for lofi beats", voiceIntent{Kind: voiceIntentWebSearch, Query: "youtube lofi beats"}},
		{"where is my music", voiceIntent{Kind: voiceIntentSearch, Query: "music"}},
		// What Whisper writes for a breath or near-silence, and the wake word
		// on its own: nothing was said, so nothing opens.
		{"thanks", voiceIntent{Kind: voiceIntentNone}},
		{"you", voiceIntent{Kind: voiceIntentNone}},
		{"You.", voiceIntent{Kind: voiceIntentNone}},
		{"Thank you.", voiceIntent{Kind: voiceIntentNone}},
		{"Thanks for watching!", voiceIntent{Kind: voiceIntentNone}},
		{"Bye.", voiceIntent{Kind: voiceIntentNone}},
		{"uh", voiceIntent{Kind: voiceIntentNone}},
		{"the", voiceIntent{Kind: voiceIntentNone}},
		{"Hey Juka.", voiceIntent{Kind: voiceIntentNone}},
		{"juka", voiceIntent{Kind: voiceIntentNone}},
		{"okay jukahub", voiceIntent{Kind: voiceIntentNone}},
		// Home by any of its names, but Smart Home stays a name.
		{"open home screen", voiceIntent{Kind: voiceIntentHome}},
		{"Open the home screen.", voiceIntent{Kind: voiceIntentHome}},
		{"show me the home screen", voiceIntent{Kind: voiceIntentHome}},
		{"go to the main menu", voiceIntent{Kind: voiceIntentHome}},
		{"open smart home", voiceIntent{Kind: voiceIntentOpen, Target: "smart home", Verb: "open"}},
		// Transport and the sound, the way people say them.
		{"play next song", voiceIntent{Kind: voiceIntentMusicNext}},
		{"skip to the next song", voiceIntent{Kind: voiceIntentMusicNext}},
		{"play previous song", voiceIntent{Kind: voiceIntentMusicPrev}},
		{"volume off", voiceIntent{Kind: voiceIntentMute}},
		{"Turn off the volume.", voiceIntent{Kind: voiceIntentMute}},
		{"volume on", voiceIntent{Kind: voiceIntentUnmute}},
		{"turn the volume back on", voiceIntent{Kind: voiceIntentUnmute}},
		// Airplane mode is a switch, not a name.
		{"Turn on airplane mode.", voiceIntent{Kind: voiceIntentAirplane, On: true}},
		{"airplane mode off", voiceIntent{Kind: voiceIntentAirplane, On: false}},
		{"turn off flight mode", voiceIntent{Kind: voiceIntentAirplane, On: false}},
		{"enable airplane mode", voiceIntent{Kind: voiceIntentAirplane, On: true}},
		// Typed numbers far past 100 clamp the same on every CPU.
		{"volume 1e300", voiceIntent{Kind: voiceIntentVolumeSet, Value: 100}},
		{"volume 99999999999999999999", voiceIntent{Kind: voiceIntentVolumeSet, Value: 100}},
	}
	if len(cases) < 60 {
		t.Fatalf("the phrase table shrank to %d rows", len(cases))
	}
	for _, tc := range cases {
		got := voiceParse(tc.said)
		if got.Kind != tc.want.Kind || got.Target != tc.want.Target || got.Verb != tc.want.Verb ||
			got.Query != tc.want.Query || got.Value != tc.want.Value || got.On != tc.want.On {
			t.Errorf("%q (folded %q): got %+v, want %+v", tc.said, voiceNormalize(tc.said), got, tc.want)
		}
	}
}

// Whisper ends a sentence with a full stop and capitalises it; a phone keyboard
// may add a comma after the wake word. None of that may change the command.
func TestVoiceNormalizeIgnoresDictationNoise(t *testing.T) {
	for _, said := range []string{"Open settings.", "open settings", "OPEN SETTINGS!", "Hey Juka, open settings.",
		"okay juka open settings please", "um, open settings", "Can you open settings for me?"} {
		if got := voiceNormalize(said); got != "open settings" {
			t.Errorf("voiceNormalize(%q) = %q, want %q", said, got, "open settings")
		}
	}
}

// "JukaLand" is a game, not the wake word: "juka land" must survive as a name.
func TestVoiceWakeWordKeepsJukaLand(t *testing.T) {
	if got := voiceParse("juka land"); got.Kind != voiceIntentOpen || got.Target != "jukaland" {
		t.Fatalf("juka land = %+v, want open jukaland", got)
	}
	if got := voiceParse("juka, open jukaland"); got.Kind != voiceIntentOpen || got.Target != "jukaland" {
		t.Fatalf("juka, open jukaland = %+v, want open jukaland", got)
	}
}

func TestVoiceFindNumber(t *testing.T) {
	for _, tc := range []struct {
		words []string
		want  int
		ok    bool
	}{
		{[]string{"volume", "50"}, 50, true},
		{[]string{"seventy", "five"}, 75, true},
		{[]string{"one", "hundred"}, 100, true},
		{[]string{"hundred"}, 100, true},
		{[]string{"to", "thirty"}, 30, true},
		{[]string{"7.5"}, 8, true},
		{[]string{"twenty"}, 20, true},
		{[]string{"volume", "up"}, 0, false},
	} {
		got, ok := voiceFindNumber(tc.words)
		if got != tc.want || ok != tc.ok {
			t.Errorf("voiceFindNumber(%v) = %d, %v; want %d, %v", tc.words, got, ok, tc.want, tc.ok)
		}
	}
}

// Levels never leave 0..100, however they are said.
func TestVoiceLevelsAreClamped(t *testing.T) {
	if got := voiceParse("volume 250"); got.Kind != voiceIntentVolumeSet || got.Value != 100 {
		t.Fatalf("volume 250 = %+v, want a set to 100", got)
	}
	if got := voiceParse("brightness up by 300"); got.Kind != voiceIntentBrightnessStep || got.Value != 100 {
		t.Fatalf("brightness up by 300 = %+v, want a step of 100", got)
	}
}
