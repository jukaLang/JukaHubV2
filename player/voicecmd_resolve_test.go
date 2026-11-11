package main

// voicecmd_resolve_test.go — which target a spoken name opens, against a
// hand-made catalog shaped like the one Home search builds.

import "testing"

func voiceTestCatalog() []voiceTarget {
	return []voiceTarget{
		{Title: "Settings", Where: "Settings", Keys: "settings preferences options configuration setup", Kind: voiceTargetScreen},
		{Title: "General settings", Where: "Settings", Keys: "general name username language", Kind: voiceTargetScreen},
		{Title: "Media", Where: "Media", Keys: "media video videos youtube watch movies search", Kind: voiceTargetScreen},
		{Title: "YouTube quality", Where: "Settings · Media", Keys: "youtube resolution 720p", Kind: voiceTargetSetting},
		{Title: "Bluetooth", Where: "Start menu", Kind: voiceTargetScreen},
		{Title: "Bluetooth", Where: "Settings · Network", Keys: "bluetooth radio", Kind: voiceTargetSetting},
		{Title: "Browser", Where: "Apps", Keys: "browser web internet www website pages", Kind: voiceTargetScreen},
		{Title: "Wi-Fi", Where: "Network", Keys: "wifi wlan wireless network internet", Kind: voiceTargetScreen},
		{Title: "Quit JukaHub", Where: "Device", Keys: "exit quit power off shutdown close", Kind: voiceTargetScreen, Quit: true},
		{Title: "RetroArch", Where: "App", Kind: voiceTargetApp},
		{Title: "Super Mario Bros 3", Where: "NES", Keys: "NES", Kind: voiceTargetGame},
		{Title: "Super Mario World", Where: "SNES", Keys: "SNES", Kind: voiceTargetGame},
		{Title: "Mario Kart 64", Where: "N64", Keys: "N64", Kind: voiceTargetGame},
		{Title: "Tetris", Where: "GB", Keys: "GB", Kind: voiceTargetGame},
		{Title: "Tetris.pdf", Where: "Books", Icon: "pdf", Kind: voiceTargetFile},
		{Title: "Offline Radio", Where: "Media", Keys: "radio", Kind: voiceTargetScreen, Blocked: "Needs internet"},
		// The Apps list repeats JukaHub's own applets, which are screens too
		// (voiceTargetsFromApp marks them Builtin).
		{Title: "JukaLand", Where: "Games", Keys: "jukaland world game", Kind: voiceTargetScreen},
		{Title: "Browser", Where: "Built into JukaHub", Icon: "juka", Kind: voiceTargetApp, Builtin: true},
		{Title: "JukaLand", Where: "Built into JukaHub", Icon: "juka", Kind: voiceTargetApp, Builtin: true},
	}
}

func TestVoiceRankOpensTheClearWinner(t *testing.T) {
	cat := voiceTestCatalog()
	for _, tc := range []struct {
		name, verb, want string
	}{
		{"settings", "open", "Settings"},
		{"youtube", "open", "Media"},
		{"bluetooth", "open", "Bluetooth"},
		{"browser", "open", "Browser"},
		{"wifi", "open", "Wi-Fi"},
		{"retroarch", "launch", "RetroArch"},
		{"retro arch", "launch", "RetroArch"},
		{"mario kart", "play", "Mario Kart 64"},
		{"super mario bros 3", "play", "Super Mario Bros 3"},
		{"jukaland", "open", "JukaLand"},
		{"jukaland", "play", "JukaLand"},
	} {
		m := voiceRankTargets(tc.name, tc.verb, cat)
		if len(m) == 0 {
			t.Errorf("%q: no match", tc.name)
			continue
		}
		if m[0].Target.Title != tc.want {
			t.Errorf("%q: best is %q (%d), want %q", tc.name, m[0].Target.Title, m[0].Score, tc.want)
			continue
		}
		if !voiceDecide(m) {
			t.Errorf("%q: %q should open by itself (scores %v)", tc.name, tc.want, voiceTestScores(m))
		}
	}
}

// "open Bluetooth" is the screen; "Bluetooth settings" is the settings row.
func TestVoiceSettingsWordPrefersTheSettingsRow(t *testing.T) {
	cat := voiceTestCatalog()
	m := voiceRankTargets("bluetooth settings", "open", cat)
	if len(m) == 0 || m[0].Target.Kind != voiceTargetSetting || m[0].Target.Title != "Bluetooth" {
		t.Fatalf("bluetooth settings: best %+v, want the Bluetooth settings row", m)
	}
	m = voiceRankTargets("bluetooth", "open", cat)
	if len(m) == 0 || m[0].Target.Kind != voiceTargetScreen {
		t.Fatalf("bluetooth: best %+v, want the Bluetooth screen", m)
	}
}

// A name two games share is a question, not a guess.
func TestVoiceAmbiguousNameListsChoices(t *testing.T) {
	m := voiceRankTargets("super mario", "play", voiceTestCatalog())
	if voiceDecide(m) {
		t.Fatalf("super mario opened %q by itself; both games match", m[0].Target.Title)
	}
	choices := voiceChoices(m, voiceMaxChoices)
	if len(choices) < 2 {
		t.Fatalf("super mario: %d choices, want both games", len(choices))
	}
}

// "play Tetris" is the game, not the PDF with the same name.
func TestVoicePlayPrefersTheGame(t *testing.T) {
	m := voiceRankTargets("tetris", "play", voiceTestCatalog())
	if len(m) == 0 || m[0].Target.Kind != voiceTargetGame {
		t.Fatalf("play tetris: best %+v, want the game", m)
	}
}

// A misheard name is offered, never opened unless it is the only close one.
func TestVoiceFuzzyNearMiss(t *testing.T) {
	m := voiceRankTargets("setings", "open", voiceTestCatalog())
	if len(m) == 0 || m[0].Target.Title != "Settings" {
		t.Fatalf("setings: best %v, want Settings", voiceTestScores(m))
	}
	if !m[0].Fuzzy {
		t.Fatalf("setings matched Settings on the ladder (%d); the test expects the fuzzy path", m[0].Score)
	}
	m = voiceRankTargets("brouser", "open", voiceTestCatalog())
	if len(m) == 0 || m[0].Target.Title != "Browser" {
		t.Fatalf("brouser: best %v, want Browser", voiceTestScores(m))
	}
}

func TestVoiceNothingMatches(t *testing.T) {
	if m := voiceRankTargets("zebra crossing", "open", voiceTestCatalog()); len(m) != 0 {
		t.Fatalf("zebra crossing matched %v", voiceTestScores(m))
	}
}

// A screen that needs internet is reported, not opened.
func TestVoiceBlockedTargetDoesNotOpen(t *testing.T) {
	m := voiceRankTargets("offline radio", "open", voiceTestCatalog())
	if len(m) == 0 || m[0].Target.Title != "Offline Radio" {
		t.Fatalf("offline radio: %v", voiceTestScores(m))
	}
	if voiceDecide(m) {
		t.Fatal("a blocked target opened by itself")
	}
}

// A built-in app that is also a screen is one target, not a tie with itself;
// a separate app that happens to share a screen's name is still a question.
func TestVoiceBuiltinAppDoesNotShadowItsScreen(t *testing.T) {
	m := voiceRankTargets("browser", "open", voiceTestCatalog())
	for _, x := range m {
		if x.Target.Builtin {
			t.Fatalf("browser: the built-in app row is still ranked: %v", voiceTestScores(m))
		}
	}
	cat := []voiceTarget{
		{Title: "Weather", Where: "Weather", Kind: voiceTargetScreen},
		{Title: "Weather", Where: "App", Kind: voiceTargetApp},
	}
	if m := voiceRankTargets("weather", "open", cat); voiceDecide(m) || len(m) != 2 {
		t.Fatalf("weather: a screen and a separate app of that name must both be offered, got %v", voiceTestScores(m))
	}
}

// A name of three letters or fewer opens only on a whole title, a whole title
// word or an exact synonym — "you" (Whisper's word for a breath) and "the" do
// not ride a prefix into YouTube or Themes.
func TestVoiceShortNameNeedsAWholeMatch(t *testing.T) {
	cat := []voiceTarget{
		{Title: "Themes", Where: "Settings", Kind: voiceTargetScreen},
		{Title: "Media", Where: "Media", Keys: "media video youtube", Kind: voiceTargetScreen},
		{Title: "Live TV", Where: "TV", Keys: "iptv channels", Kind: voiceTargetScreen},
		{Title: "YouTube", Where: "App", Kind: voiceTargetApp},
		{Title: "NES", Where: "Emulators", Kind: voiceTargetScreen},
	}
	for _, name := range []string{"you", "the", "liv"} {
		if m := voiceRankTargets(name, "open", cat); voiceDecide(m) {
			t.Errorf("%q opened %q by itself on a prefix (%v)", name, m[0].Target.Title, voiceTestScores(m))
		}
	}
	m := voiceRankTargets("you", "open", cat)
	if len(voiceChoices(m, voiceMaxChoices)) == 0 {
		t.Errorf("you: the prefix matches should still be offered, got none")
	}
	for name, want := range map[string]string{"tv": "Live TV", "nes": "NES"} {
		m := voiceRankTargets(name, "open", cat)
		if len(m) == 0 || m[0].Target.Title != want || !voiceDecide(m) {
			t.Errorf("%q: want %q to open by itself, got %v", name, want, voiceTestScores(m))
		}
	}
}

func TestVoiceSimilarity(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		min  float64
	}{
		{"settings", "settings", 1},
		{"setings", "settings", 0.85},
		{"settign", "setting", 0.85},
		{"brouser", "browser", 0.85},
	} {
		if got := voiceSimilarity(tc.a, tc.b); got < tc.min {
			t.Errorf("voiceSimilarity(%q, %q) = %.2f, want ≥ %.2f", tc.a, tc.b, got, tc.min)
		}
	}
	if got := voiceSimilarity("mario", "supermariobrosthree"); got != 0 {
		t.Errorf("a short name against a long title should not be a near miss, got %.2f", got)
	}
	if d := voiceEditDistance([]rune("ab"), []rune("ba")); d != 1 {
		t.Errorf("a transposition is one edit, got %d", d)
	}
}

func TestVoiceWhisperPromptIsBounded(t *testing.T) {
	var cat []voiceTarget
	for i := 0; i < 500; i++ {
		cat = append(cat, voiceTarget{Title: "Game number " + string(rune('A'+i%26)) + string(rune('a'+i%7)), Kind: voiceTargetGame})
	}
	p := voiceWhisperPrompt(cat)
	if n := len([]rune(p)); n > 620 {
		t.Fatalf("prompt is %d runes; Whisper reads far less", n)
	}
}

func voiceTestScores(m []voiceMatch) []string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		out = append(out, x.Target.Title+"/"+x.Target.Where)
	}
	return out
}
