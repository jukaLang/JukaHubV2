package main

// onboarding_start.go — the first-run tour's quick start: the page right
// after the language, where a new owner chooses what to do first instead of
// stepping through a dozen pages before anything happens.
//
//   - Play a game: the tour ends and the Games library opens (JukaHub's own
//     games are there even before any ROM is on the card);
//   - Add games: the Add games screen (addgames.go); the tour waits on this
//     page until the owner is back;
//   - Connect to Wi-Fi: the Wi-Fi page (or the computer's own settings);
//   - Pick a look: straight to the colour and Home layout pages;
//   - Set up everything else: the rest of the tour, as before.
//
// So playing is two presses from the first boot (the language, then A), and
// adding games, getting online or picking a look three (UX-10). The rows are
// the "Get set up" checklist's (setupchecklist.go), drawn and driven the same
// way; a row is ticked from live state (games found, a network up).

// The quick start's rows, after the checklist's own IDs.
const (
	onbStartPlay setupRowID = 100 + iota
	onbStartAdd
	onbStartWiFi
	onbStartLook
	onbStartMore
)

// onboardingStartRows builds the quick start's rows from the device.
func onboardingStartRows(config *Config) []setupRow {
	lib := homeTabsLibrary()
	ready := homeTabsLib.ready
	play := setupRow{ID: onbStartPlay, Title: "Play a game", Action: func() {
		onboardingFinish(config, true)
		openGameLibraryPanel()
	}}
	switch {
	case lib.Total > 0:
		play.Detail, play.Done = pluralizeCount(lib.Total, "game", "games")+" found on this handheld", true
	case !ready:
		play.Detail = "Looking for games on the card…"
	default:
		play.Detail = "No ROMs yet: JukaHub's own games are there to start with"
	}
	add := setupRow{ID: onbStartAdd, Title: "Add games", Detail: "From your phone, a computer or a USB stick",
		Done: ready && lib.Total > 0, Action: func() { addGamesOpen(config) }}
	online := IsOnline() || localIPv4() != ""
	wifi := setupRow{ID: onbStartWiFi, Title: "Connect to Wi-Fi", Detail: "Network: " + NetworkStatus(), Done: online,
		Action: func() {
			if info, err := openFirstRunSettings(config, "wifi"); err != nil {
				ShowToast(err.Error(), ToastKindInfo)
			} else {
				onboarding.info = info
			}
		}}
	look := setupRow{ID: onbStartLook, Title: "Pick a look", Detail: "A colour, then how Home is laid out",
		Action: func() { onboardingJumpTo(config, "theme") }}
	more := setupRow{ID: onbStartMore, Title: "Set up everything else", Detail: "Your name, time zone, covers, achievements and more",
		Action: func() { onboardingAdvance(config) }}
	return []setupRow{play, add, wifi, look, more}
}

// onboardingJumpTo moves the tour to the first step of kind.
func onboardingJumpTo(config *Config, kind string) {
	for i, step := range onboardingSteps {
		if step.kind != kind {
			continue
		}
		onboarding.step = i - 1
		onboardingAdvance(config)
		return
	}
}
