package main

// gameassist_settings.go — Settings → Voice & AI rows for the game assistant:
// Juka knowing the game being played (voiceai_settings.go), and what
// jukaSystemPrompt says about it.
//
// Both switches are off until turned on, as the tester who asked for the
// feature wanted: nothing about the games leaves the handheld while they are
// off. On, the running cost during a game is one goroutine reading the pad
// every 25 ms (the same watcher "Mouse in games and apps" uses); the picture
// of the screen, the microphone and the network are touched only when the
// player holds the chord and asks (gameassist.go).

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// gameAssistKey is the Custom setting behind "Game assistant" (off unless
	// it is true).
	gameAssistKey = "gameAssist"
	// gameAssistScreenKey is "Juka can see the game screen" (off unless true).
	gameAssistScreenKey = "gameAssistScreen"
	// gameAssistRecent is how long after a game ends Juka still knows it was
	// played: long enough to ask about it back on Home, short enough that an
	// evening's first question is not about the afternoon's game.
	gameAssistRecent = 30 * time.Minute
	// gameAssistTitleMax bounds a title or system name in the prompt: both come
	// from file names, which may be anything.
	gameAssistTitleMax = 80
)

// gameAssistLive mirrors the two switches for the goroutines that build a
// prompt (Chat's sender calls jukaSystemPrompt off the main thread, and no
// goroutine may read the config map). The main thread stores them every frame
// (gameAssistFrame) and at once when a switch changes.
var gameAssistLive struct {
	on     atomic.Bool
	screen atomic.Bool
}

// gameAssistEnabled reports Settings → Voice & AI → Game assistant.
func gameAssistEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, gameAssistKey, false)
}

// gameAssistScreenEnabled reports "Juka can see the game screen".
func gameAssistScreenEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, gameAssistScreenKey, false)
}

// gameAssistStore saves one of the two switches and mirrors it at once.
func gameAssistStore(c *Config, key string, on bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[key] = on
	saveUserConfigDebounced(userConfigFromVariables(c))
	gameAssistLive.on.Store(gameAssistEnabled(c))
	gameAssistLive.screen.Store(gameAssistScreenEnabled(c))
}

// gameAssistDetail is the switch's second line: the chord on the handheld,
// the orb's key and Chat on a PC (where no overlay can be drawn over a game's
// own window).
func gameAssistDetail(*Config) string {
	if gameAssistSupported() {
		return "Hold Select + R2 in a game to ask Juka about it"
	}
	return "The voice orb (F4) and Chat know the game you play"
}

// gameAssistSettingsRows is the game assistant's switches.
func gameAssistSettingsRows() []setRow {
	return []setRow{
		{
			ID: gameAssistKey, Label: "Game assistant", Kind: setKindToggle,
			Detail: gameAssistDetail,
			Get:    gameAssistEnabled,
			Set: func(c *Config, on bool) {
				gameAssistStore(c, gameAssistKey, on)
				setToast("Game assistant: " + setOnOff(on))
			},
		},
		{
			ID: gameAssistScreenKey, Label: "Juka can see the game screen", Kind: setKindToggle,
			Detail: setStatic("A picture of the paused game goes with your question, only when you ask"),
			// The picture is the panel's framebuffer, which only the handheld
			// has; and it means nothing with the assistant off.
			Visible: func(c *Config) bool { return gameAssistSupported() && gameAssistEnabled(c) },
			Get:     gameAssistScreenEnabled,
			Set: func(c *Config, on bool) {
				gameAssistStore(c, gameAssistScreenKey, on)
				setToast("Juka can see the game screen: " + setOnOff(on))
			},
		},
	}
}

// gameAssistPromptSuffix is what jukaSystemPrompt adds about the game being
// played: "" when the game assistant is off or no game was played recently,
// otherwise a sentence or two that starts with a space. Any goroutine: it
// reads the mirrored switch and the session record, never the config.
func gameAssistPromptSuffix() string {
	if !gameAssistLive.on.Load() {
		return ""
	}
	return gameAssistPromptFor(gaSessionSnapshot(), time.Now())
}

// gameAssistPromptFor is the suffix for one session record at now. A game
// that ended is mentioned for gameAssistRecent, counted on the monotonic
// clock (the TrimUI boots before NTP sets the wall clock); a record across a
// sleep, where the wall clock ran on while the monotonic one stood still, is
// dropped rather than called minutes old. Pure.
func gameAssistPromptFor(g gaGame, now time.Time) string {
	title := gameAssistClean(g.title)
	if title == "" {
		return ""
	}
	name := title
	if system := gameAssistClean(g.system); system != "" {
		name += " (" + system + ")"
	}
	var lead string
	if g.running {
		lead = " The player is playing " + name + " right now."
	} else {
		if g.ended.IsZero() {
			return ""
		}
		ago := now.Sub(g.ended)
		wall := now.Round(0).Sub(g.ended.Round(0))
		if ago < 0 || ago > gameAssistRecent || wall-ago > 2*time.Minute {
			return ""
		}
		lead = " The player was playing " + name + " until " + gameAssistAgo(ago) + "."
	}
	return lead + " When they say this game, the game or here, they mean it. Give concrete, spoiler-light tips."
}

// gameAssistAgo words a short time in the past: "a moment ago", "a minute
// ago", "about 12 minutes ago". Pure.
func gameAssistAgo(d time.Duration) string {
	m := int((d + 30*time.Second) / time.Minute)
	switch {
	case m < 1:
		return "a moment ago"
	case m == 1:
		return "a minute ago"
	}
	return "about " + strconv.Itoa(m) + " minutes ago"
}

// gameAssistClean makes a title or system name safe to put in a sentence: no
// control characters (a file name may hold a newline), single spaces, at most
// gameAssistTitleMax characters. Pure.
func gameAssistClean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > gameAssistTitleMax {
		s = strings.TrimSpace(string([]rune(s)[:gameAssistTitleMax]))
	}
	return s
}
