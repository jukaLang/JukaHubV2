package main

// osapps.go — JukaHub's small everyday apps, gathered in one place: Notes,
// Tasks, Voice Memos, Paint, Stopwatch, the Calculator, Alarms, the Pomodoro
// timer and Solitaire.
//
// Each app draws and drives itself in its own files (notes.go, tasks*.go,
// voicememo*.go, paint*.go, stopwatch*.go, solitaire*.go, …). What they share
// lives here:
//
//   - their scenes, injected in memory like the Calculator's
//     (toolsHubEnsureScene): no shipped config lists them and the user's config
//     file is never rewritten for them. Notes and the Pomodoro timer had a
//     scene module but no scene in the shipped configs, so nothing opened them;
//     they are injected the same way now;
//   - the Tools hub's "Everyday apps" section, the Apps screen's rows, the
//     Start menu's order and glyphs, and Home search's words for each app;
//   - osPromptOpen, one on-screen keyboard target with a submit callback for
//     every small "name it" field the apps have (a memo's name, a task, a
//     drawing's file name), so each app does not add its own eight cases to
//     vkeyboard.go. Like the other consequential fields it writes nothing
//     while typing: the callback runs on Done only, and B drops the text.

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// The new apps' scene names (also their config scene names).
const (
	paintSceneName     = "Paint"
	voiceMemoSceneName = "VoiceMemos"
	solitaireSceneName = "Solitaire"
	tasksSceneName     = "Tasks"
	stopwatchSceneName = "Stopwatch"
)

// osAppSpec is one everyday app: its scene, the monogram the Start menu needs
// to list a scene at all, its name, a one-line description, its glyph
// (icondefs.go) and the words Home search finds it by.
type osAppSpec struct {
	scene string
	mono  string
	title string
	desc  string
	glyph string
	keys  string
}

// osAppSpecs are the scene-backed everyday apps, in the order the Tools hub
// shows them. The Calculator and Alarms are injected by their own owners
// (toolsHubEnsureScenes, ensureAlarmScene) and only take part in the hub's
// section (osAppsHubTiles).
var osAppSpecs = [...]osAppSpec{
	{scene: "Notes", mono: "N", title: "Notes", desc: "Write things down, in folders", glyph: "edit",
		keys: "notes note notepad write writing text memo jot journal diary"},
	{scene: tasksSceneName, mono: "K", title: "Tasks", desc: "Checklists and reminders", glyph: "checklist",
		keys: "tasks task todo to do to-do checklist checklists list lists reminders reminder shopping errands due"},
	{scene: voiceMemoSceneName, mono: "V", title: "Voice Memos", desc: "Record, name and replay short recordings", glyph: "mic",
		keys: "voice memos memo recorder record recording recordings dictaphone audio microphone mic test"},
	{scene: paintSceneName, mono: "D", title: "Paint", desc: "Pixel art and screenshot markup", glyph: "brush",
		keys: "paint draw drawing pixel art sprite doodle sketch markup mark up annotate screenshot circle pen brush wallpaper cover"},
	{scene: stopwatchSceneName, mono: "W", title: "Stopwatch", desc: "Laps and a countdown timer", glyph: "stopwatch",
		keys: "stopwatch stop watch lap laps timer countdown count down kitchen timer speedrun time"},
	{scene: "Pomodoro", mono: "P", title: "Pomodoro", desc: "Focus timer with breaks", glyph: "clock",
		keys: "pomodoro focus timer tomato work break study productivity"},
	{scene: solitaireSceneName, mono: "S", title: "Solitaire", desc: "Klondike with undo and resume", glyph: "cards",
		keys: "solitaire klondike patience cards card game freecell spider"},
}

// osAppSpecFor is the spec of an everyday app's scene.
func osAppSpecFor(scene string) (osAppSpec, bool) {
	for _, s := range osAppSpecs {
		if s.scene == scene {
			return s, true
		}
	}
	return osAppSpec{}, false
}

// osAppsEnsureScenes appends whichever of the apps' scenes are missing.
// Appending never moves an existing scene, so it is safe on the live config;
// a config that defines one of them keeps its own. Idempotent; it runs with
// toolsHubEnsureScenes, at startup and after every config reload.
func osAppsEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	for _, s := range osAppSpecs {
		toolsHubEnsureScene(config, s.scene, s.mono, s.desc)
	}
}

// osAppsDisplayName is the name an app's scene shows under (the header, the
// Start menu, search), or "" for a scene that is not one of them.
func osAppsDisplayName(scene string) string {
	if s, ok := osAppSpecFor(scene); ok && s.title != s.scene {
		return s.title
	}
	return ""
}

// osAppsOpen opens an everyday app by its scene name, injecting the scene
// first in case a reload swapped the config underneath.
func osAppsOpen(config *Config, scene string) {
	if config == nil {
		return
	}
	osAppsEnsureScenes(config)
	toolsHubOpenScene(config, scene)
}

// osAppsUserDir is a folder of the user's own files that an app keeps
// (recordings, drawings): name at the top of the SD card on the handheld, so
// the Phone Companion's Files card, FTP and a card reader all see it, and
// Documents/JukaHub/name on a PC. It does not create the folder.
func osAppsUserDir(name string) string {
	if IsTrimuiDevice() {
		root := strings.TrimSpace(defaultTrimuiSDRoot())
		if root == "" {
			root = "/mnt/SDCARD"
		}
		return filepath.Join(root, name)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if data, derr := P().DataDir(); derr == nil && data != "" {
			return filepath.Join(data, name)
		}
		return name
	}
	if dirExists(filepath.Join(home, "Documents")) {
		return filepath.Join(home, "Documents", "JukaHub", name)
	}
	return filepath.Join(home, "JukaHub", name)
}

// osAppsDataPath is where an app keeps its own state file (a saved game, the
// task lists): JukaHub's data folder, or next to the app when it has none.
func osAppsDataPath(file string) string {
	if data, err := P().DataDir(); err == nil && data != "" {
		return filepath.Join(data, file)
	}
	return file
}

// ── Tools hub ─────────────────────────────────────────────────────────────

// osAppsTilePrefix starts the id of an "Everyday apps" tile; the rest is the
// scene it opens.
const osAppsTilePrefix = "osapp:"

// osAppsHubTiles is the Tools hub's "Everyday apps" section: the apps above,
// with the Calculator and Alarms among them. A tile whose scene the config
// cannot have is left out (has reports it).
func osAppsHubTiles(has func(string) bool) []toolsHubTile {
	tiles := make([]toolsHubTile, 0, len(osAppSpecs)+2)
	add := func(scene, label, glyph, value string) {
		if !has(scene) {
			return
		}
		t := toolsHubTile{id: osAppsTilePrefix + scene, label: label, icon: glyph, value: value}
		// The live line, from memory only (each app caches it): a recording
		// under way, a running clock, what is due, a drawing to continue.
		live, on, tone := "", false, toolsHubToneGood
		switch scene {
		case tasksSceneName:
			live, on = tasksHubValue()
			tone = toolsHubToneWarn // something is due: worth a look
		case voiceMemoSceneName:
			live, on = voiceMemoHubValue()
		case paintSceneName:
			live, _ = paintHubValue() // a drawing to continue is not "on"
		case stopwatchSceneName:
			live, on = stopwatchHubValue()
		}
		if live != "" {
			t.value = live
		}
		if on {
			t.tone = tone
		}
		tiles = append(tiles, t)
	}
	for _, s := range osAppSpecs {
		if s.scene == solitaireSceneName {
			// A game: the Games screen and the Apps list carry it.
			continue
		}
		add(s.scene, s.title, s.glyph, s.desc)
		if s.scene == stopwatchSceneName {
			// The other clocks sit beside it.
			add("Calculator", "Calculator", "calculator", "Quick math with history")
			add("Alarm", "Alarms", "alarm", "Wake-up alarms and their sounds")
		}
	}
	if len(tiles) > 0 {
		tiles[0].section, tiles[0].sectionIcon = "Everyday apps", "apps"
	}
	return tiles
}

// osAppsHubActivate opens the app behind an "Everyday apps" tile. It reports
// whether id was one of them.
func osAppsHubActivate(config *Config, id string) bool {
	scene, ok := strings.CutPrefix(id, osAppsTilePrefix)
	if !ok {
		return false
	}
	switch scene {
	case "Alarm":
		openAlarmScene(config)
	case "Calculator":
		toolsHubOpenScene(config, "Calculator")
	default:
		osAppsOpen(config, scene)
	}
	return true
}

// ── Start menu and search ─────────────────────────────────────────────────

// osAppsSceneGlyph is an app scene's Start menu glyph, or "".
func osAppsSceneGlyph(scene string) string {
	if s, ok := osAppSpecFor(scene); ok {
		return s.glyph
	}
	return ""
}

// osAppsSearchEntries are the apps in Home search's catalog, with the words
// people look for them by.
func osAppsSearchEntries() []homeSearchEntry {
	out := make([]homeSearchEntry, 0, len(osAppSpecs))
	for _, s := range osAppSpecs {
		where := "Apps"
		if s.scene == solitaireSceneName {
			where = "Games"
		}
		out = append(out, homeSearchEntry{title: s.title, where: where, icon: s.glyph, keys: s.keys, scene: s.scene})
	}
	return out
}

// ── The shell's hooks ─────────────────────────────────────────────────────

// osAppsLive is the app scene that was entered last and not yet left
// ("" when the screen is not one of them). Main thread only.
var osAppsLive string

// osAppsIsApp reports whether scene is one of the apps with their own
// lifecycle (Notes and Pomodoro keep theirs in core.go).
func osAppsIsApp(scene string) bool {
	switch scene {
	case paintSceneName, voiceMemoSceneName, solitaireSceneName, tasksSceneName, stopwatchSceneName:
		return true
	}
	return false
}

// osAppsSceneEnter runs on every scene transition (toolsScenesEnter, from
// core.go): the app being left saves and lets go of what it holds (a
// recording, the canvas's textures), the app arriving loads what it shows.
// Re-entering the same app only enters it again.
func osAppsSceneEnter(config *Config, scene string) {
	if osAppsLive != "" && osAppsLive != scene {
		osAppsLeave(osAppsLive)
	}
	osAppsLive = ""
	if !osAppsIsApp(scene) {
		return
	}
	osAppsLive = scene
	switch scene {
	case paintSceneName:
		paintEnter(config)
	case voiceMemoSceneName:
		voiceMemoEnter(config)
	case solitaireSceneName:
		solitaireEnter(config)
	case tasksSceneName:
		tasksEnter(config)
	case stopwatchSceneName:
		stopwatchEnter(config)
	}
}

// osAppsLeave tells an app its screen was left.
func osAppsLeave(scene string) {
	switch scene {
	case paintSceneName:
		paintLeave()
	case voiceMemoSceneName:
		voiceMemoLeave()
	case solitaireSceneName:
		solitaireLeave()
	case tasksSceneName:
		tasksLeave()
	case stopwatchSceneName:
		stopwatchLeave()
	}
}

// osAppsFlushAll writes every app's state on the way out (shutdown.go's
// "Saving your place" row). Voice Memos is flushed earlier, with the other
// playback, while the audio device is still open (shutdownStopPlayback).
func osAppsFlushAll() {
	paintFlush()
	solitaireFlush()
	tasksFlush()
	stopwatchFlush()
}

// osAppsFooterHints is the footer of an app's screen, which follows its
// tool, sheet or recording.
func osAppsFooterHints(scene string) ([]FooterHint, bool) {
	switch scene {
	case paintSceneName:
		return paintFooterHints(), true
	case voiceMemoSceneName:
		return voiceMemoFooterHints(), true
	case solitaireSceneName:
		return solitaireFooterHints(), true
	case tasksSceneName:
		return tasksFooterHints(), true
	case stopwatchSceneName:
		return stopwatchFooterHints(), true
	}
	return nil, false
}

// osAppsCurrent names the current scene when it is one of the apps, or "".
func osAppsCurrent(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	if name := config.Scenes[currentSceneIndex].Name; osAppsIsApp(name) {
		return name
	}
	return ""
}

// osAppsClick routes a left click or tap (main.go calls it after the top bar
// and the overlays had theirs). On Paint's canvas the press starts a stroke;
// anywhere else on Paint it presses the control under it.
func osAppsClick(mx, my int32, config *Config) bool {
	switch osAppsCurrent(config) {
	case paintSceneName:
		return paintPointer(0, mx, my) || paintClick(mx, my, config)
	case voiceMemoSceneName:
		return voiceMemoClick(mx, my, config)
	case solitaireSceneName:
		return solitaireClick(mx, my, config)
	case tasksSceneName:
		return tasksClick(mx, my, config)
	case stopwatchSceneName:
		return stopwatchClick(mx, my, config)
	}
	return false
}

// osAppsWheel routes the mouse wheel: Paint zooms, the lists scroll.
func osAppsWheel(config *Config, y int32) bool {
	switch osAppsCurrent(config) {
	case paintSceneName:
		return paintWheel(y)
	case voiceMemoSceneName:
		return voiceMemoWheel(y)
	case solitaireSceneName:
		return solitaireWheel(y)
	case tasksSceneName:
		return tasksWheel(y)
	case stopwatchSceneName:
		return stopwatchWheel(y)
	}
	return false
}

// osAppsPointerMotion carries a held press across Paint's canvas, while
// nothing floats over Paint.
func osAppsPointerMotion(config *Config, mx, my int32) {
	if paintOwnsPad(config) {
		paintPointer(1, mx, my)
	}
}

// osAppsPointerUp ends a stroke or a dragged shape on Paint's canvas. It is
// sent whatever opened meanwhile, so a stroke never stays held.
func osAppsPointerUp(config *Config, mx, my int32) {
	if paintIsCurrent(config) {
		paintPointer(2, mx, my)
	}
}

// ── Keyboard prompt ───────────────────────────────────────────────────────

// osPromptVKTarget aims the on-screen keyboard at osPrompt.
const osPromptVKTarget vkTargetKind = 2611

// osPrompt is the open prompt: what the keyboard shows and what Done calls.
// Main thread only.
var osPrompt struct {
	title       string
	placeholder string
	action      string
	buffer      string
	max         int
	submit      func(string)
}

// osPromptOpen opens the on-screen keyboard (or takes a physical keyboard's
// typing) for one line of text. title is the keyboard's heading, placeholder
// the grey example in an empty field, action the Done key's word ("Save",
// "Add", "Rename"; "" is "Done"), seed the starting text and maxRunes the
// longest text accepted (0 is 200). submit gets the trimmed text on Done —
// possibly empty, which the caller decides about — and nothing on B.
func osPromptOpen(title, placeholder, action, seed string, maxRunes int, submit func(string)) {
	if maxRunes <= 0 {
		maxRunes = 200
	}
	osPrompt.title = strings.ToUpper(strings.TrimSpace(title))
	osPrompt.placeholder = placeholder
	osPrompt.action = action
	osPrompt.max = maxRunes
	osPrompt.submit = submit
	osPrompt.buffer = ""
	osPromptSetText(seed)
	chatInputActive = false
	unitInputActive = false
	vkTarget = osPromptVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// osPromptSetText stores typed text, cut to the prompt's length and to one
// line (a paste can carry newlines).
func osPromptSetText(text string) {
	text = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(text)
	if osPrompt.max > 0 && utf8.RuneCountInString(text) > osPrompt.max {
		r := []rune(text)
		text = string(r[:osPrompt.max])
	}
	osPrompt.buffer = text
}

// osPromptActionLabel is the Done key's word.
func osPromptActionLabel() string {
	if osPrompt.action != "" {
		return osPrompt.action
	}
	return "Done"
}

// osPromptTake hands over the prompt's callback, once: Done takes it before
// the keyboard closes (closing drops it, the way B does).
func osPromptTake() func(string) {
	fn := osPrompt.submit
	osPrompt.submit = nil
	return fn
}

// osPromptRun calls a taken callback with the trimmed text. It runs after the
// keyboard closed, so the callback may open the keyboard again (a second
// field) without the close undoing it.
func osPromptRun(fn func(string), text string) {
	if fn != nil {
		fn(strings.TrimSpace(text))
	}
}

// osPromptActive reports whether the keyboard is up on an app's prompt.
func osPromptActive() bool {
	return virtualKeyboardActive && vkTarget == osPromptVKTarget
}
