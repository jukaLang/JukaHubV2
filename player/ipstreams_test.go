package main

// ipstreams_test.go — the IPStream button opens a menu that can always start
// content.
//
// The panel used to be the saved-channel list alone: on a fresh install the
// button opened a note telling the user to type a link on a screen it had not
// opened, with nothing to play. The menu now leads with the two ways to get
// content — add a link, or import the public playlist — and those rows are part
// of the same D-pad/click cursor as the channels.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// ipMenuTestState saves and restores the menu's globals for one test.
func ipMenuTestState(t *testing.T) {
	t.Helper()
	_ = mediaDurIPStore(t) // temp file: the library is written, never the real one

	prevOpen, prevCursor, prevStatus := ipPanelOpen, ipStreamsCursor, ipPanelStatus
	prevPrompt := ipPromptPending
	prevScene := currentSceneIndex
	t.Cleanup(func() {
		ipPanelOpen, ipStreamsCursor, ipPanelStatus = prevOpen, prevCursor, prevStatus
		ipPromptPending = prevPrompt
		currentSceneIndex = prevScene
		vkCloseKeyboard()
		activeSceneIndex, activeElementIndex = -1, -1
		inputTextBuffer = ""
	})
	ipPanelOpen = true
	ipStreamsCursor = 0
	ipPanelStatus = ""
	ipPromptPending = false
}

func ipMenuSetLibrary(list []IPStream) {
	ipStreamsMu.Lock()
	ipStreams = append([]IPStream(nil), list...)
	ipStreamsLoaded = true
	ipStreamsMu.Unlock()
}

// The Streams button on the Media screen is a real focus target: the D-pad
// reaches it from the last search-row button, and confirming it opens the menu.
// The injected button is appended while the config loads, so this also pins that
// the focus graph is built after the injection and that the compact Brick canvas
// keeps it on screen.
func TestStreamButtonIsDpadSelectable(t *testing.T) {
	ipMenuTestState(t)
	ipMenuSetLibrary(nil)
	ipPanelOpen = false

	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// The Brick is the tightest canvas (960x720), where a misplaced button used
	// to land off the left edge.
	applyUIProfileForDevice(config, DeviceTrimuiBrick)

	media := findSceneIndex(config, "Tube")
	if media < 0 {
		t.Fatal("the config has no Media scene")
	}
	scene := config.Scenes[media]
	ipIdx, shortsIdx := -1, -1
	for i, e := range scene.Elements {
		switch e.Trigger {
		case "ip_open":
			ipIdx = i
		case "shorts_open":
			shortsIdx = i
		}
	}
	if ipIdx < 0 {
		t.Fatal("the Media scene has no Streams button")
	}
	if shortsIdx < 0 {
		t.Fatal("the Media scene has no Shorts button to walk from")
	}
	if right := scene.Elements[ipIdx].X + getElementWidth(scene.Elements[ipIdx], 0); right > int32(config.Width) {
		t.Fatalf("the Streams button ends at %d, past the %d canvas", right, config.Width)
	}

	fe := NewFocusEngine()
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	if !fe.SetByElementIndex(shortsIdx) {
		t.Fatal("the Shorts button is not in the focus graph")
	}
	if got := fe.Navigate(ActionNavigateRight); got != ipIdx {
		t.Fatalf("Right from Shorts focused element %d, want the Streams button %d", got, ipIdx)
	}

	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	currentSceneIndex = media
	handleTrigger(nil, config, scene.Elements[ipIdx])
	if !ipPanelOpen {
		t.Fatal("confirming the Streams button did not open the menu")
	}
}

// The two action rows are always there, above the saved channels, so the menu
// is never an empty list with no way out.
func TestStreamMenuAlwaysOffersContent(t *testing.T) {
	ipMenuTestState(t)

	ipMenuSetLibrary(nil)
	items := ipPanelItems()
	if len(items) != ipPanelActionRows {
		t.Fatalf("empty library menu has %d rows, want %d action rows", len(items), ipPanelActionRows)
	}
	if items[0].Kind != ipItemAdd || items[1].Kind != ipItemBrowse {
		t.Fatalf("action rows = %v/%v, want add then browse", items[0].Kind, items[1].Kind)
	}

	one := IPStream{Name: "One", URL: "http://example.com/one.m3u8"}
	ipMenuSetLibrary([]IPStream{one})
	items = ipPanelItems()
	if len(items) != ipPanelActionRows+1 {
		t.Fatalf("menu with one channel has %d rows, want %d", len(items), ipPanelActionRows+1)
	}
	if last := items[len(items)-1]; last.Kind != ipItemChannel || last.Stream.URL != one.URL {
		t.Fatalf("last row = %+v, want the saved channel", last)
	}
}

// The cursor walks every row, and a channel row maps to the saved list past the
// action rows — the arithmetic that used to make Start and A act on row N.
func TestStreamMenuCursorMapsPastTheActionRows(t *testing.T) {
	ipMenuTestState(t)
	a := IPStream{Name: "A", URL: "http://example.com/a.m3u8"}
	b := IPStream{Name: "B", URL: "http://example.com/b.m3u8"}
	ipMenuSetLibrary([]IPStream{a, b})

	if got := ipPanelChannelIndex(0); got != -1 {
		t.Fatalf("action row 0 mapped to channel %d, want none", got)
	}
	if got := ipPanelChannelIndex(ipPanelActionRows + 1); got != 1 {
		t.Fatalf("row %d mapped to channel %d, want 1", ipPanelActionRows+1, got)
	}

	var played []string
	prevPlay := ipPanelPlayLink
	ipPanelPlayLink = func(_ *Config, url string) { played = append(played, url) }
	t.Cleanup(func() { ipPanelPlayLink = prevPlay })

	ipStreamsCursor = ipPanelActionRows + 1
	ipPanelPlay(&Config{})
	if len(played) != 1 || played[0] != b.URL {
		t.Fatalf("played %v, want %s", played, b.URL)
	}
	if ipPanelOpen {
		t.Error("the menu stayed open over the player it started")
	}
}

// Removing from an action row is a no-op: the rows above the channels are not
// deletable, and Start must not delete the channel above them instead.
func TestStreamMenuRemoveIgnoresTheActionRows(t *testing.T) {
	ipMenuTestState(t)
	a := IPStream{Name: "A", URL: "http://example.com/a.m3u8"}
	b := IPStream{Name: "B", URL: "http://example.com/b.m3u8"}
	ipMenuSetLibrary([]IPStream{a, b})

	ipStreamsCursor = 0
	ipPanelRemove()
	if got := ipPanelRows(); len(got) != 2 {
		t.Fatalf("a remove on an action row left %d channels, want 2", len(got))
	}

	ipStreamsCursor = ipPanelActionRows + 1
	ipPanelRemove()
	got := ipPanelRows()
	if len(got) != 1 || got[0].URL != a.URL {
		t.Fatalf("channels after removing the second = %+v, want just %s", got, a.URL)
	}
}

// The Add row asks for a link instead of sending the user to another screen,
// and its submit saves the channel and plays it.
func TestStreamMenuAddRowAsksForALink(t *testing.T) {
	ipMenuTestState(t)
	ipMenuSetLibrary(nil)

	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	config.Scenes = []SceneConfig{{Name: "Media", Elements: []Element{
		{Type: "input", Variable: "search_query"},
	}}}
	currentSceneIndex = 0

	var played []string
	prevPlay := ipPanelPlayLink
	ipPanelPlayLink = func(_ *Config, url string) { played = append(played, url) }
	t.Cleanup(func() { ipPanelPlayLink = prevPlay })

	ipStreamsCursor = 0
	ipPanelActivate(config)

	if virtualKeyboardActive == false {
		t.Fatal("the add row did not open the keyboard")
	}
	if !ipPromptPending {
		t.Fatal("the keyboard was opened without arming the channel submit")
	}
	if ipPanelOpen {
		t.Error("the menu stayed open behind the keyboard")
	}

	vkInsertText("  https://example.com/live/channel.m3u8  ", config)
	vkSubmit(config, false)
	if ipPromptPending {
		t.Error("the channel submit stayed armed after saving")
	}
	if len(played) != 1 || played[0] != "https://example.com/live/channel.m3u8" {
		t.Fatalf("played %v, want the typed link once", played)
	}
	got := ipPanelRows()
	if len(got) != 1 || got[0].URL != played[0] {
		t.Fatalf("channels after the submit = %+v, want the typed link", got)
	}
}

// The channel submit refuses text that is not a link: nothing is saved and
// nothing is played.
func TestStreamMenuAddRowRejectsANonLink(t *testing.T) {
	ipMenuTestState(t)
	ipMenuSetLibrary(nil)

	var played []string
	prevPlay := ipPanelPlayLink
	ipPanelPlayLink = func(_ *Config, url string) { played = append(played, url) }
	t.Cleanup(func() { ipPanelPlayLink = prevPlay })

	submitIPStreamLink(&Config{}, "just a phrase")
	if len(played) != 0 {
		t.Fatalf("played %v from a non-link", played)
	}
	if got := ipPanelRows(); len(got) != 0 {
		t.Fatalf("saved %+v from a non-link", got)
	}
}

// The Stream button with an empty field opens the menu; that is the press the
// screen exists for, and it used to answer with a toast and no menu.
func TestStreamButtonWithoutALinkOpensTheMenu(t *testing.T) {
	ipMenuTestState(t)
	ipMenuSetLibrary(nil)
	ipPanelOpen = false

	prevScene := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevScene })
	config := &Config{Variables: Variables{Custom: map[string]interface{}{"stream_url": ""}}}
	config.Scenes = []SceneConfig{{Name: "IPStream", Elements: []Element{
		{Type: "input", Variable: "stream_url"},
		{Type: "button", Text: "Stream", Trigger: "ip_stream"},
	}}}
	currentSceneIndex = 0

	handleTrigger(nil, config, Element{Type: "button", Text: "Stream", Trigger: "ip_stream"})

	if !ipPanelOpen {
		t.Fatal("the Stream button with an empty field did not open the menu")
	}
}

// While the menu is open the D-pad is the menu's, not the scene's: Up/Down move
// the row cursor and A activates the row under it.
func TestStreamMenuOwnsTheDpad(t *testing.T) {
	ipMenuTestState(t)
	a := IPStream{Name: "A", URL: "http://example.com/a.m3u8"}
	ipMenuSetLibrary([]IPStream{a})
	ipStreamsCursor = 0

	if !handleIPPanelKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{}) {
		t.Fatal("the open menu did not consume Down")
	}
	if ipStreamsCursor != 1 {
		t.Fatalf("Down moved the cursor to %d, want 1", ipStreamsCursor)
	}
	// Walk to the last row (the saved channel) and one press past it. The
	// menu owns every key while it is open, so Down there is still consumed —
	// it just must not move past the end.
	last := ipPanelActionRows + len(ipPanelRows()) - 1
	for ipStreamsCursor < last {
		handleIPPanelKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{})
	}
	if ipStreamsCursor != last {
		t.Fatalf("the cursor stopped at %d, want the last row %d", ipStreamsCursor, last)
	}
	if !handleIPPanelKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}, &Config{}) {
		t.Fatal("the open menu did not consume Down at the last row")
	}
	if ipStreamsCursor != last {
		t.Fatalf("Down past the last row moved the cursor to %d", ipStreamsCursor)
	}

	var played []string
	prevPlay := ipPanelPlayLink
	ipPanelPlayLink = func(_ *Config, url string) { played = append(played, url) }
	t.Cleanup(func() { ipPanelPlayLink = prevPlay })

	if !handleIPPanelKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}}, &Config{}) {
		t.Fatal("the menu did not consume Enter")
	}
	if len(played) != 1 || played[0] != a.URL {
		t.Fatalf("Enter played %v, want %s", played, a.URL)
	}
}
