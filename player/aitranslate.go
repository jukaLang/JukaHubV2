package main

// aitranslate.go — "Translate games": RetroArch's AI service, the feature
// Batocera calls AI game translation, for RetroArch games the
// library starts.
//
// RetroArch has carried it since 1.7.8: when the AI service is triggered (its
// hotkey, or the AI_SERVICE network command) it POSTs the current frame to
// ai_service_url and shows, plays or pauses on what comes back. JukaHub sets
// that up for one launch at a time and offers two services:
//
//   - JukaHub's own (the default). A tiny HTTP server bound to 127.0.0.1 on a
//     free port picked at launch, behind a random path, takes RetroArch's
//     request, sends the frame to the vision model JukaHub's AI already uses
//     (groqvision.go, with the user's Groq key), asks for the on-screen text
//     translated into the chosen language and hands the answer back as
//     subtitle text — or, for English, as a WAV in the handheld's own voice
//     (aitranslate_server.go). Pictures with the translation drawn in are not
//     offered here: drawing text needs SDL_ttf, which the server goroutine
//     must not touch, and the vendor tree has no pure-Go font rasteriser.
//   - Any RetroArch-compatible service the user names (a self-hosted
//     vgtranslate, ZTranslate…): RetroArch talks to it directly, and picture
//     mode is offered because such services draw the picture themselves.
//
// The launch (a registered prepare hook) writes, for that launch only
// (racfgscope.go puts the user's values back afterwards):
//
//	ai_service_enable = "true"
//	ai_service_mode   = "3" (text), "1" (speech) or "0" (picture)
//	ai_service_url    = JukaHub's server, or the user's address
//	ai_service_source_lang / ai_service_target_lang = "0" ("don't care")
//	ai_service_pause  = "true" only for picture mode
//	network_cmd_enable = "true" (the in-game menu's trigger)
//	menu_enable_widgets / video_font_enable = "true" (text and picture modes)
//
// The target language never goes through RetroArch's numbered language list,
// whose order has changed between versions: JukaHub's own server knows the
// language already, and a custom address gets "target_lang=<code>" in its
// query (what Batocera does), with RetroArch's own language left at 0.
// Text mode is RetroArch 1.17's (the AI service rework); an older RetroArch
// only logs a hint (aiTrNoteOldRetroArch).
//
// Triggers: the in-game menu's "Translate screen" row (gamemenu.go, Select +
// L2) gives the game back like Resume and, after a short settle, sends
// AI_SERVICE over RetroArch's command port (lowbattery.go); a RetroArch
// hotkey the user binds (rahotkeys) works too. JukaHub writes no input_* key.
//
// Privacy: the frame goes to the chosen service only when the player asks for
// a translation; JukaHub keeps neither the picture nor the text.
//
// Main thread for the settings and the hooks, apart from the Exited hook
// (reaper goroutine) and the menu's command goroutine; aiTr is guarded.

import (
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// aiTrKey is "Translate games" (off unless true).
	aiTrKey = "aiTranslate"
	// aiTrShowKey is how the translation is shown: aiTrShowText (default),
	// aiTrShowSpeech or aiTrShowImage.
	aiTrShowKey = "aiTranslateShow"
	// aiTrLangKey is the language to translate into: aiTrLangAuto (JukaHub's
	// own language, the default) or a code of aiTrLangs.
	aiTrLangKey = "aiTranslateLang"
	// aiTrServiceKey is aiTrServiceJukaHub (default) or aiTrServiceCustom.
	aiTrServiceKey = "aiTranslateService"
	// aiTrURLKey is the custom service's address.
	aiTrURLKey = "aiTranslateURL"

	aiTrShowText       = "text"
	aiTrShowSpeech     = "speech"
	aiTrShowImage      = "image"
	aiTrLangAuto       = "auto"
	aiTrServiceJukaHub = "jukahub"
	aiTrServiceCustom  = "custom"

	// RetroArch's ai_service_mode numbers (the order of its AI Service Output
	// list: Image, Speech, Narrator, Text, Text + Narrator, Image + Narrator).
	aiTrModeImage  = 0
	aiTrModeSpeech = 1
	aiTrModeText   = 3

	// aiTrMenuSettle lets the resumed game run a few frames, and the pad
	// watcher give the pad back, before AI_SERVICE captures the screen.
	aiTrMenuSettle = 500 * time.Millisecond
	// aiTrURLMax bounds a custom address.
	aiTrURLMax = 400
)

// gmTranslate is the in-game menu's "Translate screen" row, numbered past
// gamemenu.go's own rows (stopwatch.go and screenrec.go took 100 and 101).
const gmTranslate gmItem = 120

// aiTrLang is one language the translation can go into: its code (JukaHub's
// UI language codes), its English name for the model's instructions, and the
// code a RetroArch-compatible service reads as target_lang.
type aiTrLang struct {
	Code    string
	English string
	Service string
}

// aiTrLangs are JukaHub's own languages: all Latin or Cyrillic, which the
// fonts RetroArch draws its subtitles with cover.
var aiTrLangs = []aiTrLang{
	{Code: "en", English: "English", Service: "en"},
	{Code: "es", English: "Spanish", Service: "es"},
	{Code: "pt-BR", English: "Brazilian Portuguese", Service: "pt"},
	{Code: "fr", English: "French", Service: "fr"},
	{Code: "de", English: "German", Service: "de"},
	{Code: "it", English: "Italian", Service: "it"},
	{Code: "ru", English: "Russian", Service: "ru"},
}

// aiTr is the launch the translation was set up for. Written by the prepare
// hook (main thread) and the Exited hook (reaper goroutine); read by the
// in-game menu.
var aiTr struct {
	mu      sync.Mutex
	gen     int
	rom     string
	at      time.Time // when the launch was prepared (monotonic)
	port    int       // RetroArch's command port for this launch
	show    string    // the mode this launch uses
	custom  bool
	lang    aiTrLang
	started bool
	srv     *aiTrServer
}

func init() {
	gameLaunchRegisterPrepare(aiTrPrepare)
	gameLaunchRegisterStarted(aiTrStarted)
	gameLaunchRegisterExited(aiTrExited)
}

// ── Settings, as stored ──────────────────────────────────────────────────

// aiTrEnabled reports "Translate games".
func aiTrEnabled(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, aiTrKey, false)
}

// aiTrCustom reports whether the user's own service is chosen.
func aiTrCustom(c *Config) bool {
	return c != nil && strings.TrimSpace(customStringValue(c.Variables.Custom, aiTrServiceKey)) == aiTrServiceCustom
}

// aiTrCustomURL is the custom service's address as stored.
func aiTrCustomURL(c *Config) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(customStringValue(c.Variables.Custom, aiTrURLKey))
}

// aiTrLangChoice is the stored language choice: aiTrLangAuto or a code.
func aiTrLangChoice(c *Config) string {
	if c == nil {
		return aiTrLangAuto
	}
	v := strings.TrimSpace(customStringValue(c.Variables.Custom, aiTrLangKey))
	if _, ok := aiTrLangFor(v); ok {
		return v
	}
	return aiTrLangAuto
}

// aiTrTargetLang is the language a translation goes into: the stored one, or
// JukaHub's own, or English.
func aiTrTargetLang(c *Config) aiTrLang {
	code := aiTrLangChoice(c)
	if code == aiTrLangAuto {
		code = uiLanguageCode()
	}
	if l, ok := aiTrLangFor(code); ok {
		return l
	}
	return aiTrLangs[0]
}

// aiTrLangFor finds a language by code.
func aiTrLangFor(code string) (aiTrLang, bool) {
	for _, l := range aiTrLangs {
		if strings.EqualFold(l.Code, code) {
			return l, true
		}
	}
	return aiTrLang{}, false
}

// aiTrShowChoice is the stored "Show as" choice.
func aiTrShowChoice(c *Config) string {
	if c == nil {
		return aiTrShowText
	}
	switch v := strings.TrimSpace(customStringValue(c.Variables.Custom, aiTrShowKey)); v {
	case aiTrShowSpeech, aiTrShowImage:
		return v
	}
	return aiTrShowText
}

// aiTrSpeechPossible reports whether "Read aloud" can work: a custom service
// makes its own sound; JukaHub's needs English (the handheld's voice is an
// English one) and a voice on this device.
func aiTrSpeechPossible(c *Config) bool {
	if aiTrCustom(c) {
		return true
	}
	if aiTrTargetLang(c).Code != "en" {
		return false
	}
	_, ok := ttsLocal()
	return ok
}

// aiTrShowMode is the mode a launch uses: the stored one where it can work,
// else text. Pictures come only from a custom service.
func aiTrShowMode(c *Config) string {
	switch show := aiTrShowChoice(c); show {
	case aiTrShowImage:
		if aiTrCustom(c) {
			return show
		}
	case aiTrShowSpeech:
		if aiTrSpeechPossible(c) {
			return show
		}
	}
	return aiTrShowText
}

// aiTrRAMode is a show mode as RetroArch's ai_service_mode number.
func aiTrRAMode(show string) int {
	switch show {
	case aiTrShowImage:
		return aiTrModeImage
	case aiTrShowSpeech:
		return aiTrModeSpeech
	}
	return aiTrModeText
}

// aiTrCheckURL validates a custom service address and returns it cleaned:
// http or https with a host, and nothing a retroarch.cfg line cannot carry
// (quotes, '#', spaces, control characters). Pure.
func aiTrCheckURL(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "Type the service's address"
	}
	if len(s) > aiTrURLMax {
		return "", "That address is too long"
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '"' || r == '#' || r == '\\' {
			return "", "The address can't contain spaces, quotes or #"
		}
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "Use an http:// or https:// address"
	}
	return s, ""
}

// aiTrServiceURL is the address RetroArch is given for a custom service: the
// user's, with target_lang in its query unless it already has one. Pure.
func aiTrServiceURL(base string, lang aiTrLang) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	if u.Query().Has("target_lang") {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
		if strings.HasSuffix(base, "?") || strings.HasSuffix(base, "&") {
			sep = ""
		}
	}
	return base + sep + "target_lang=" + url.QueryEscape(lang.Service)
}

// aiTrURLHost is a custom address as Settings shows it: its host, so a key in
// its query is not on screen.
func aiTrURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// aiTrHaveKey reports whether a Groq key is stored, without decrypting it
// (Settings asks every frame).
func aiTrHaveKey(c *Config) bool {
	if c == nil {
		return false
	}
	for _, k := range []string{"groq_api_key", "GroqApiKey"} {
		if strings.TrimSpace(customStringValue(c.Variables.Custom, k)) != "" {
			return true
		}
	}
	return false
}

// ── The launch ───────────────────────────────────────────────────────────

// aiTrPrepare is the prepare hook: for a RetroArch launch with "Translate
// games" on, it starts JukaHub's server (when that is the service) and scopes
// the AI service keys to this launch. Like the other launch-time settings it
// never fails a launch. Main thread.
func aiTrPrepare(launcher string, game libraryGame) {
	aiTrStop("another game is starting")
	c := appConfig
	if !aiTrEnabled(c) || !retroArchLauncher(launcher) {
		return
	}
	lang := aiTrTargetLang(c)
	show := aiTrShowMode(c)
	custom := aiTrCustom(c)
	var srv *aiTrServer
	address := ""
	if custom {
		base, problem := aiTrCheckURL(aiTrCustomURL(c))
		if problem != "" {
			logWarn("aitranslate", "no usable custom service address (%s); translation is off for this game", problem)
			return
		}
		address = aiTrServiceURL(base, lang)
	} else {
		var err error
		srv, err = aiTrServerStart(aiTrServerSetup{
			key:    strings.TrimSpace(groqAPIKey()),
			lang:   lang,
			speech: show == aiTrShowSpeech,
			title:  game.Title,
			system: game.System,
		})
		if err != nil {
			logWarn("aitranslate", "cannot start the translation service: %v", err)
			return
		}
		address = srv.address()
	}
	pause := "false"
	if show == aiTrShowImage {
		// RetroArch draws a returned picture over the paused frame; the next
		// AI_SERVICE (the menu row again, or the hotkey) plays on.
		pause = "true"
	}
	values := []raScopeValue{
		{Key: "ai_service_enable", Value: "true"},
		{Key: "ai_service_mode", Value: strconv.Itoa(aiTrRAMode(show))},
		{Key: "ai_service_url", Value: address},
		{Key: "ai_service_source_lang", Value: "0"},
		{Key: "ai_service_target_lang", Value: "0"},
		{Key: "ai_service_pause", Value: pause},
		{Key: "network_cmd_enable", Value: "true"},
	}
	if show != aiTrShowSpeech {
		// Subtitles and pictures are drawn by RetroArch's graphics widgets,
		// and the service's errors come as notifications: both on for this
		// game, whatever the OSD preset (osdpreset.go) said.
		values = append(values,
			raScopeValue{Key: "menu_enable_widgets", Value: "true"},
			raScopeValue{Key: "video_font_enable", Value: "true"})
	}
	port, configured := 0, false
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		raw, readErr := os.ReadFile(path)
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("aitranslate", "cannot set up translation in %s: %v", path, err)
			continue
		}
		configured = true
		if port == 0 && readErr == nil {
			port = networkCmdPort(string(raw))
			if show == aiTrShowText {
				aiTrNoteOldRetroArch(string(raw), path)
			}
		}
	}
	if !configured {
		if srv != nil {
			srv.close()
		}
		return
	}
	aiTr.mu.Lock()
	aiTr.gen++
	gen := aiTr.gen
	aiTr.rom, aiTr.port, aiTr.show, aiTr.custom, aiTr.lang = game.Path, port, show, custom, lang
	aiTr.at, aiTr.started, aiTr.srv = time.Now(), false, srv
	aiTr.mu.Unlock()
	where := "JukaHub's service"
	if custom {
		where = aiTrURLHost(address)
	}
	logInfo("aitranslate", "translation ready for %s: %s into %s via %s (command port %d)", game.Title, show, lang.English, where, port)
	// launchLibraryGame runs on to the start in this same call: by the time
	// the main loop runs this, the game has started or its launch failed.
	tbxPost(func() { aiTrPrepareSettled(gen) })
}

// aiTrNoteOldRetroArch logs when a config RetroArch has saved lacks the keys
// its 1.17 AI service rework added: that RetroArch has no text mode, and the
// translation will not show. Text only; the launch goes on.
func aiTrNoteOldRetroArch(cfg, path string) {
	if _, ok := retroArchConfigValue(cfg, "video_driver"); !ok {
		return // never saved by RetroArch: nothing to tell from
	}
	for _, key := range []string{"ai_service_poll_delay", "ai_service_text_position", "ai_service_text_padding"} {
		if _, ok := retroArchConfigValue(cfg, key); ok {
			return
		}
	}
	logWarn("aitranslate", "%s looks like a RetroArch older than 1.17, which has no text mode: the translation may not show", path)
}

// aiTrPrepareSettled ends a setup whose game never started (the launch failed
// after the settings). Main thread.
func aiTrPrepareSettled(gen int) {
	aiTr.mu.Lock()
	failed := aiTr.gen == gen && aiTr.rom != "" && !aiTr.started
	aiTr.mu.Unlock()
	if failed {
		aiTrStop("the game did not start")
	}
}

// aiTrStarted is the start hook.
func aiTrStarted(game libraryGame, _ string, _ *exec.Cmd) {
	aiTr.mu.Lock()
	if aiTr.rom != "" && aiTr.rom == game.Path {
		aiTr.started = true
	}
	aiTr.mu.Unlock()
}

// aiTrExited is the exit hook (reaper goroutine): the server goes with the
// game. The reaper may get here seconds after the game ended (a time-lapse
// being put together), when the same game may already have been started
// again: only a setup made before this run started is this run's.
func aiTrExited(game libraryGame, _ string, ranFor time.Duration, _ error) {
	startedAt := time.Now().Add(-ranFor)
	aiTr.mu.Lock()
	mine := aiTr.rom != "" && aiTr.rom == game.Path && aiTr.at.Before(startedAt)
	aiTr.mu.Unlock()
	if mine {
		aiTrStop("the game ended")
	}
}

// aiTrStop forgets the launch and closes its server. Any goroutine.
func aiTrStop(why string) {
	aiTr.mu.Lock()
	srv := aiTr.srv
	had := aiTr.rom != ""
	aiTr.gen++
	aiTr.rom, aiTr.port, aiTr.show, aiTr.custom, aiTr.lang = "", 0, "", false, aiTrLang{}
	aiTr.at, aiTr.started, aiTr.srv = time.Time{}, false, nil
	aiTr.mu.Unlock()
	if srv != nil {
		srv.close()
	}
	if had {
		logInfo("aitranslate", "translation off: %s", why)
	}
}

// ── The in-game menu ─────────────────────────────────────────────────────

// aiTranslateMenuShown reports whether the in-game menu offers "Translate
// screen": the paused game is the RetroArch launch translation was set up
// for, with its command port known (gmStatesAvailable has already matched the
// game on screen to the library's launch). Main thread.
func aiTranslateMenuShown() bool {
	if !gmStatesAvailable() {
		return false
	}
	aiTr.mu.Lock()
	defer aiTr.mu.Unlock()
	return aiTr.rom != "" && aiTr.port > 0 && aiTr.rom == gmPick.rom
}

// aiTranslateMenuLabel is the row's words.
func aiTranslateMenuLabel() (string, string) {
	aiTr.mu.Lock()
	show, lang := aiTr.show, aiTr.lang.English
	aiTr.mu.Unlock()
	switch show {
	case aiTrShowImage:
		return "Translate screen", "The game waits under the translation; choose this again to play on"
	case aiTrShowSpeech:
		return "Translate screen", "Reads the screen's text aloud in " + lang
	}
	return "Translate screen", "Shows the screen's text in " + lang + " over the game"
}

// aiTranslateMenuPress gives the game back, as Resume does — a stopped
// RetroArch reads no commands and has no fresh frame — and asks it for a
// translation once it has run a moment.
func aiTranslateMenuPress() {
	aiTr.mu.Lock()
	gen, port := aiTr.gen, aiTr.port
	aiTr.mu.Unlock()
	if port <= 0 {
		return
	}
	PlayActivateSound()
	gaClose("the player asked for a translation", true)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("aitranslate", "asking RetroArch for a translation failed: %v", r)
			}
		}()
		time.Sleep(aiTrMenuSettle)
		aiTr.mu.Lock()
		same := aiTr.gen == gen && aiTr.port == port
		aiTr.mu.Unlock()
		if !same {
			return
		}
		if err := sendRetroArchNetCmd(port, "AI_SERVICE"); err != nil {
			logWarn("aitranslate", "AI_SERVICE command failed: %v", err)
			return
		}
		logInfo("aitranslate", "translation asked of RetroArch (port %d)", port)
	}()
}
