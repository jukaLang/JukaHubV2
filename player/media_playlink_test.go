package main

// media_playlink_test.go — Play Link on the Media screen asks for a link.
//
// The regression this pins: the button only acted on a link that was already
// typed into the URL field. With nothing highlighted it showed a toast and did
// nothing, and on a handheld reaching the small field first is the whole
// problem. The button now opens that field on the on-screen keyboard, and its
// submit plays the link instead of searching for it.

import "testing"

func tubePlayLinkScene() SceneConfig {
	return SceneConfig{Name: "Tube", Elements: []Element{
		{Type: "input", Variable: "search_query", Placeholder: "Search or paste YouTube link"},
		{Type: "button", Text: "Search", Trigger: "youtube_smart"},
		{Type: "button", Text: "Play Link", Trigger: "youtube_play"},
		{Type: "searchresults", Variable: "search_results"},
	}}
}

// A highlighted result wins, a link already in the field plays, and with
// neither the button asks instead of failing the press.
func TestPlanYouTubePlayPicksResultLinkOrPrompt(t *testing.T) {
	videos := []VideoInfo{{URL: "https://youtu.be/one"}, {URL: "https://youtu.be/two"}}

	if plan := planYouTubePlay(videos, 1, ""); plan.result != 1 || plan.url != "" || plan.askURL {
		t.Fatalf("highlighted result: got %+v, want result 1", plan)
	}
	// A stale focus past the end of a refreshed grid must not swallow the press.
	if plan := planYouTubePlay(videos, 7, "https://youtu.be/typed"); plan.result != -1 || plan.url != "https://youtu.be/typed" {
		t.Fatalf("typed link: got %+v, want the typed link", plan)
	}
	if plan := planYouTubePlay(nil, -1, ""); !plan.askURL || plan.result != -1 || plan.url != "" {
		t.Fatalf("empty field: got %+v, want a prompt", plan)
	}
	// A search phrase is not a link: Play Link asks rather than playing it.
	if plan := planYouTubePlay(nil, -1, "lofi beats"); !plan.askURL {
		t.Fatalf("search phrase: got %+v, want a prompt", plan)
	}
}

// Pressing Play Link with nothing to play opens the keyboard on the scene's
// field, empty, with the play-next submit armed.
func TestPlayLinkWithoutAResultOpensTheKeyboard(t *testing.T) {
	prevScene, prevFocus := currentSceneIndex, focusedResultIndex
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex = prevScene, prevFocus
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
		inputTextBuffer = ""
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{"search_query": "lofi beats"}}}
	config.Scenes = []SceneConfig{tubePlayLinkScene()}
	currentSceneIndex = 0
	focusedResultIndex = -1

	handleTrigger(nil, config, Element{Type: "button", Text: "Play Link", Trigger: "youtube_play"})

	if !virtualKeyboardActive {
		t.Fatal("Play Link did not open the keyboard")
	}
	if !mediaPlayLinkPending {
		t.Fatal("the keyboard was opened without arming the play-link submit")
	}
	if activeSceneIndex != 0 || activeElementIndex != 0 {
		t.Fatalf("keyboard target: scene %d element %d, want 0/0", activeSceneIndex, activeElementIndex)
	}
	if inputTextBuffer != "" {
		t.Fatalf("the prompt opened holding %q, want an empty field", inputTextBuffer)
	}
}

// Submitting the prompt plays the typed link and starts no search.
func TestPlayLinkSubmitPlaysTheTypedLink(t *testing.T) {
	prevScene, prevFocus := currentSceneIndex, focusedResultIndex
	prevPlay := mediaPlayLinkPlay
	var played []string
	mediaPlayLinkPlay = func(_ *Config, url string) { played = append(played, url) }
	t.Cleanup(func() {
		mediaPlayLinkPlay = prevPlay
		currentSceneIndex, focusedResultIndex = prevScene, prevFocus
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
		inputTextBuffer = ""
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{tubePlayLinkScene()}
	currentSceneIndex = 0
	focusedResultIndex = -1

	handleTrigger(nil, config, Element{Type: "button", Text: "Play Link", Trigger: "youtube_play"})
	vkInsertText("  https://youtu.be/abc123  ", config)
	vkSubmit(config, false)

	if len(played) != 1 || played[0] != "https://youtu.be/abc123" {
		t.Fatalf("played %v, want the trimmed link once", played)
	}
	if virtualKeyboardActive {
		t.Error("the keyboard stayed up after the link was played")
	}
	if mediaPlayLinkPending {
		t.Error("the play-link submit stayed armed")
	}
	if got := config.Variables.Custom["search_results"]; got != nil {
		t.Errorf("a search was started: %v", got)
	}
}

// An empty submit closes the prompt and plays nothing.
func TestPlayLinkSubmitWithNothingTypedPlaysNothing(t *testing.T) {
	prevScene, prevFocus := currentSceneIndex, focusedResultIndex
	prevPlay := mediaPlayLinkPlay
	plays := 0
	mediaPlayLinkPlay = func(_ *Config, _ string) { plays++ }
	t.Cleanup(func() {
		mediaPlayLinkPlay = prevPlay
		currentSceneIndex, focusedResultIndex = prevScene, prevFocus
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
		inputTextBuffer = ""
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{tubePlayLinkScene()}
	currentSceneIndex = 0
	focusedResultIndex = -1

	handleTrigger(nil, config, Element{Type: "button", Text: "Play Link", Trigger: "youtube_play"})
	vkSubmit(config, false)

	if plays != 0 {
		t.Fatalf("%d playback(s) started from an empty prompt", plays)
	}
	if mediaPlayLinkPending {
		t.Error("an empty submit left the play-link submit armed")
	}
}

// Dismissing the prompt must not leave the next keyboard session armed: an
// ordinary search submitted afterwards has to search.
func TestPlayLinkDismissalDisarmsThePrompt(t *testing.T) {
	prevScene, prevFocus := currentSceneIndex, focusedResultIndex
	t.Cleanup(func() {
		currentSceneIndex, focusedResultIndex = prevScene, prevFocus
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
		inputTextBuffer = ""
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{tubePlayLinkScene()}
	currentSceneIndex = 0
	focusedResultIndex = -1

	handleTrigger(nil, config, Element{Type: "button", Text: "Play Link", Trigger: "youtube_play"})
	vkCloseKeyboard()

	if mediaPlayLinkPending {
		t.Fatal("closing the prompt kept the play-link submit armed")
	}
}

// A scene with no link field cannot prompt, so the press still explains itself.
func TestPlayLinkWithoutAFieldReportsIt(t *testing.T) {
	prevScene := currentSceneIndex
	t.Cleanup(func() {
		currentSceneIndex = prevScene
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{{Name: "Tube", Elements: []Element{
		{Type: "button", Text: "Play Link", Trigger: "youtube_play"},
	}}}
	currentSceneIndex = 0

	handleTrigger(nil, config, Element{Type: "button", Text: "Play Link", Trigger: "youtube_play"})
	if virtualKeyboardActive || mediaPlayLinkPending {
		t.Fatal("a scene without a field opened a keyboard")
	}
}
