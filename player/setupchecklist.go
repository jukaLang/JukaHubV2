package main

// setupchecklist.go — the "Get set up" page of the first-run tour.
//
// JukaHub is usable the moment the folder lands on the SD card, but a handful of
// optional links unlock most of it: Wi-Fi, a RetroAchievements key, the media
// tools YouTube and IPTV need, a paired console for Remote Play and the phone
// companion. Each row shows its live state and opens the screen that fixes it,
// so first run ends in the app instead of in Settings.
//
// The checklist itself is a pure function of setupEnv, so every state can be
// tested without a device, a network or a console.

import "strings"

// setupRowID names one checklist line. The order of the constants is the order
// the rows are drawn in.
type setupRowID int

const (
	setupRowWiFi setupRowID = iota
	setupRowRetroAchievements
	setupRowMediaTools
	setupRowRemotePlay
	setupRowCompanion
)

// setupEnv is everything the checklist judges, gathered once per refresh. It is
// a value, which is what keeps the checklist testable.
type setupEnv struct {
	Online         bool
	Network        string
	RetroUsername  string
	RetroComplete  bool
	MissingTools   []string
	PairedConsoles int
	PSNAccount     bool
	CompanionPIN   string
	CompanionErr   string
}

// setupRow is one line of the checklist.
type setupRow struct {
	ID     setupRowID
	Title  string
	Detail string
	Done   bool
	Action func()
}

// setupActions are the panels a row can open. They are passed in rather than
// called directly so a test can watch a press without opening a real panel.
type setupActions struct {
	WiFi              func()
	RetroAchievements func()
	MediaTools        func()
	RemotePlay        func()
	Companion         func()
}

// setupActionsFor is a seam for tests: the tour opens the real panels, a test
// records which row was chosen without opening anything.
var setupActionsFor = defaultSetupActions

func defaultSetupActions() setupActions {
	return setupActions{
		WiFi:              openWiFiPanel,
		RetroAchievements: openRetroAchievementsPanel,
		MediaTools:        openToolsPanel,
		RemotePlay:        openChiakiPanel,
		Companion:         openCompanionOverlay,
	}
}

// setupChecklist builds the rows in display order. A row is "done" from live
// state only: there is no separate checklist progress to get out of sync with
// the device.
func setupChecklist(env setupEnv, actions setupActions) []setupRow {
	rows := make([]setupRow, 0, 5)

	wifi := setupRow{
		ID:     setupRowWiFi,
		Title:  "Wi-Fi",
		Detail: "Connect for updates, artwork and streaming.",
		Done:   env.Online,
		Action: actions.WiFi,
	}
	if env.Online {
		wifi.Detail = "Connected"
		if name := strings.TrimSpace(env.Network); name != "" {
			wifi.Detail = "Connected · " + name
		}
	}
	rows = append(rows, wifi)

	ra := setupRow{
		ID:     setupRowRetroAchievements,
		Title:  "RetroAchievements",
		Detail: "Link your account to sync unlocks and progress.",
		Done:   env.RetroComplete,
		Action: actions.RetroAchievements,
	}
	switch {
	case env.RetroComplete:
		ra.Detail = "Linked — unlocks and progress sync automatically"
		if name := strings.TrimSpace(env.RetroUsername); name != "" {
			ra.Detail = "Linked as " + name + " — unlocks and progress sync"
		}
	case strings.TrimSpace(env.RetroUsername) != "":
		ra.Detail = "Add the web API key from retroachievements.org."
	}
	rows = append(rows, ra)

	tools := setupRow{
		ID:     setupRowMediaTools,
		Title:  "Media tools",
		Detail: "Installed — YouTube, IPTV and video are ready.",
		Done:   len(env.MissingTools) == 0,
		Action: actions.MediaTools,
	}
	if missing := strings.Join(env.MissingTools, " and "); missing != "" {
		tools.Detail = "Install " + missing + " for YouTube, IPTV and video."
	}
	rows = append(rows, tools)

	remote := setupRow{
		ID:     setupRowRemotePlay,
		Title:  "Remote Play",
		Detail: "Pair a PS4 or PS5 with the PIN shown on the console.",
		Done:   env.PairedConsoles > 0,
		Action: actions.RemotePlay,
	}
	switch {
	case env.PairedConsoles == 1:
		remote.Detail = "1 console paired and ready to stream"
	case env.PairedConsoles > 1:
		remote.Detail = itoa(env.PairedConsoles) + " consoles paired and ready to stream"
	case !env.PSNAccount:
		remote.Detail = "Add your PSN account ID, then pair a PS4 or PS5."
	}
	rows = append(rows, remote)

	phone := setupRow{
		ID:     setupRowCompanion,
		Title:  "Phone companion",
		Detail: "Scan the Home QR to use your phone as a remote.",
		Done:   strings.TrimSpace(env.CompanionPIN) != "",
		Action: actions.Companion,
	}
	if phone.Done {
		phone.Detail = "Ready · pairing PIN " + env.CompanionPIN
	} else if err := strings.TrimSpace(env.CompanionErr); err != "" {
		phone.Detail = err
	}
	rows = append(rows, phone)

	return rows
}

// setupDoneCount is the "3 of 5 set up" summary.
func setupDoneCount(rows []setupRow) int {
	done := 0
	for _, row := range rows {
		if row.Done {
			done++
		}
	}
	return done
}

func setupSummary(rows []setupRow) string {
	if len(rows) == 0 {
		return ""
	}
	return itoa(setupDoneCount(rows)) + " of " + itoa(len(rows)) + " set up"
}

// liveSetupEnv gathers the checklist inputs from the device. It touches the disk
// (the media tools and the paired consoles), so the tour refreshes it on a timer
// instead of on every frame.
func liveSetupEnv(config *Config) setupEnv {
	env := setupEnv{
		Online:  IsOnline(),
		Network: NetworkStatus(),
	}
	creds := raEffectiveCredentials()
	env.RetroUsername = creds.Username
	env.RetroComplete = creds.complete()

	for _, tool := range []string{"yt-dlp", "ffmpeg"} {
		if getToolPath(tool, config) == "" {
			env.MissingTools = append(env.MissingTools, tool)
		}
	}

	hosts := loadPSHosts()
	for _, console := range hosts.Hosts {
		if console.paired() {
			env.PairedConsoles++
		}
	}
	env.PSNAccount = strings.TrimSpace(hosts.Settings.Account) != ""

	if companion.srv != nil && companion.srv.Running() {
		env.CompanionPIN = strings.TrimSpace(companion.pin)
	} else {
		env.CompanionErr = strings.TrimSpace(companion.err)
	}
	return env
}
