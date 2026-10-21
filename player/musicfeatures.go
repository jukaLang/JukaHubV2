package main

// musicfeatures.go — the rules behind the audio player's newer controls:
// initialism search ("iwyn" finds "I Want You Now"), hold-to-skip on the
// volume keys, the auto-exit timer and the three screen-off control modes.
//
// Everything here is SDL-free apart from the key constants, and works on plain
// values, so the behaviour can be unit tested without a renderer or a sound
// device. The player owns music.mu and calls these helpers from the scene.

import (
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Initialism search
// ──────────────────────────────────────────────────────────────────────

// musicSearchNormalize keeps only letters and digits, lowercased, so a query
// can be typed without spaces or punctuation.
func musicSearchNormalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// musicInitials returns the first letter or digit of each word of title. A
// leading "The"/"A"/"An" is kept in place because dropping it would make a
// query match a different title than the one on screen. Apostrophes belong to
// the word they sit in, so "Pepper's" is one word and contributes a single "p"
// rather than an extra "s".
func musicInitials(title string) string {
	var b strings.Builder
	inWord := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inWord {
				b.WriteRune(unicode.ToLower(r))
				inWord = true
			}
			continue
		}
		if r == '\'' || r == '\u2019' || r == '\u2018' || r == '`' {
			// "Don't" and "Don’t" stay one word.
			continue
		}
		inWord = false
	}
	return b.String()
}

// musicTitleInitials is musicInitials for a track name: the file extension is
// not part of the title, so it must not become an initial.
func musicTitleInitials(name string) string {
	return musicInitials(strings.TrimSuffix(name, filepath.Ext(name)))
}

// musicSearchMatch reports whether query is a prefix of the title's initials.
// Empty queries match nothing, so the panel does not list the whole playlist.
func musicSearchMatch(title, query string) bool {
	q := musicSearchNormalize(query)
	if q == "" {
		return false
	}
	return strings.HasPrefix(musicTitleInitials(title), q)
}

// musicSearchHits returns the matching track indexes: exact-length initialisms
// first (the most likely intent), then the rest in playlist order. Duplicate
// titles are kept because they are different files.
func musicSearchHits(tracks []musicTrack, query string, limit int) []int {
	q := musicSearchNormalize(query)
	if q == "" {
		return nil
	}
	exact := make([]int, 0, 8)
	partial := make([]int, 0, 16)
	for i, t := range tracks {
		initials := musicTitleInitials(t.Name)
		if !strings.HasPrefix(initials, q) {
			continue
		}
		if initials == q {
			exact = append(exact, i)
		} else {
			partial = append(partial, i)
		}
	}
	hits := append(exact, partial...)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// musicSearchMove clamps a selection after Up/Down, wrapping only when hits
// exist so a single result does not jump to "nothing".
func musicSearchMove(hits []int, sel, delta int) int {
	if len(hits) == 0 {
		return -1
	}
	next := sel + delta
	if next < 0 {
		next = len(hits) - 1
	}
	if next >= len(hits) {
		next = 0
	}
	return next
}

// ──────────────────────────────────────────────────────────────────────
// Hold-to-skip
// ──────────────────────────────────────────────────────────────────────

const (
	// musicHoldSkipDelay is how long a volume key must be held before it turns
	// into a track skip: long enough that a normal volume tap never skips, short
	// enough to feel deliberate.
	musicHoldSkipDelay = 600 * time.Millisecond
	// musicHoldGap is a safety net for a hold whose key-up event never arrived
	// (a lost event, a controller hot-swap): repeats arriving closer together
	// than this still belong to the same press. It is deliberately longer than
	// musicHoldSkipDelay so a slow repeat stream can still fire the skip; a real
	// key release ends the hold immediately through musicVolumeHold.release.
	musicHoldGap = 900 * time.Millisecond
)

// musicVolumeHold tracks one volume key as it repeats. delta is +1 for "next",
// -1 for "previous"; fresh reports the first event of a press, so the caller
// adjusts the volume once per press instead of once per repeat.
type musicVolumeHold struct {
	key   sdl.Keycode
	since time.Time
	last  time.Time
	fired bool
}

// release ends a hold when the key or button is let go, so the next press is
// fresh even if it arrives before musicHoldGap has elapsed.
func (h *musicVolumeHold) release(key sdl.Keycode) {
	if h.key == key {
		h.key = 0
		h.last = time.Time{}
		h.fired = false
	}
}

// press feeds one volume key event. now is passed in so the timing is testable.
func (h *musicVolumeHold) press(key sdl.Keycode, now time.Time) (delta int, fresh bool) {
	if h.key != key || h.last.IsZero() || now.Sub(h.last) > musicHoldGap {
		h.key = key
		h.since = now
		h.last = now
		h.fired = false
		return 0, true
	}
	h.last = now
	if h.fired || now.Sub(h.since) < musicHoldSkipDelay {
		return 0, false
	}
	h.fired = true
	if dir, ok := musicVolumeDirection(key); ok {
		return dir, false
	}
	return 0, false
}

// musicVolumeKey reports whether a key is one of the volume controls that can
// grow into a skip. The hardware volume keysyms count too: pads and keyboards
// that report VOLUMEUP/VOLUMEDOWN go through the same hold logic as -/+.
func musicVolumeKey(key sdl.Keycode) bool {
	_, ok := musicVolumeDirection(key)
	return ok
}

// musicVolumeDirection is +1 for a volume-up key, -1 for volume-down.
func musicVolumeDirection(key sdl.Keycode) (int, bool) {
	switch key {
	case sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_VOLUMEDOWN:
		return -1, true
	case sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS, sdl.K_VOLUMEUP:
		return 1, true
	}
	return 0, false
}

// ──────────────────────────────────────────────────────────────────────
// Auto-exit timer
// ──────────────────────────────────────────────────────────────────────

// musicExitOptions is the order the exit chip steps through, mirroring the
// sleep timer so both behave the same way.
var musicExitOptions = []time.Duration{
	0,
	15 * time.Minute,
	30 * time.Minute,
	60 * time.Minute,
	90 * time.Minute,
}

// musicTimerCycle returns the next option for a running timer: a value between
// two options advances to the next one, so a timer armed at 15 minutes goes to
// 30 rather than restarting the whole cycle.
func musicTimerCycle(remaining time.Duration, options []time.Duration) time.Duration {
	if len(options) == 0 {
		return 0
	}
	step := 0
	for i := 1; i < len(options); i++ {
		if remaining > 0 && remaining <= options[i] {
			step = i
			break
		}
	}
	return options[(step+1)%len(options)]
}

// ──────────────────────────────────────────────────────────────────────
// Screen-off modes
// ──────────────────────────────────────────────────────────────────────

// musicScreenMode decides what the controls do while the panel is dark.
type musicScreenMode int

const (
	// musicScreenAny wakes on the first input; nothing else is acted on.
	musicScreenAny musicScreenMode = iota
	// musicScreenControls keeps transport and volume working; Select wakes.
	musicScreenControls
	// musicScreenSelect ignores everything but Select, so a stray pocket press
	// cannot change the track.
	musicScreenSelect
)

func (m musicScreenMode) label() string {
	switch m {
	case musicScreenControls:
		return "Controls"
	case musicScreenSelect:
		return "Select only"
	default:
		return "Any key"
	}
}

// musicScreenModeFromConfig reads "musicScreenOffMode" from the user config.
// Unknown values fall back to the default, so a typo never locks the panel.
func musicScreenModeFromConfig(config *Config) musicScreenMode {
	if config == nil {
		return musicScreenAny
	}
	value, ok := customString(config.Variables.Custom, "musicScreenOffMode")
	if !ok {
		return musicScreenAny
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "controls", "playback":
		return musicScreenControls
	case "select", "select-only", "selectonly":
		return musicScreenSelect
	}
	return musicScreenAny
}

// musicRemoteAction is what a key or button does while the panel is dark.
type musicRemoteAction int

const (
	musicActionNone musicRemoteAction = iota
	musicActionWake
	musicActionPlayPause
	musicActionNext
	musicActionPrev
	musicActionVolume
	musicActionSeek
)

// musicScreenActionAllowed reports whether action runs instead of only waking
// the panel. Wake is always allowed: the mode governs the *other* controls.
func musicScreenActionAllowed(mode musicScreenMode, action musicRemoteAction) bool {
	switch action {
	case musicActionWake:
		return true
	case musicActionNone:
		return false
	}
	return mode == musicScreenControls
}

// musicScreenOffKey classifies a key while the panel is dark.
func musicScreenOffKey(key sdl.Keycode) musicRemoteAction {
	switch key {
	case sdl.K_o, sdl.K_ESCAPE:
		return musicActionWake
	case sdl.K_SPACE, sdl.K_p:
		return musicActionPlayPause
	case sdl.K_n:
		return musicActionNext
	case sdl.K_b:
		return musicActionPrev
	case sdl.K_LEFT, sdl.K_RIGHT:
		return musicActionSeek
	case sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS,
		sdl.K_VOLUMEUP, sdl.K_VOLUMEDOWN:
		return musicActionVolume
	}
	return musicActionNone
}

// musicScreenOffButton classifies a controller button while the panel is dark.
func musicScreenOffButton(button uint8) musicRemoteAction {
	switch button {
	case uint8(sdl.CONTROLLER_BUTTON_BACK):
		return musicActionWake
	case uint8(sdl.CONTROLLER_BUTTON_X):
		return musicActionPlayPause
	case uint8(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER):
		return musicActionNext
	case uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER):
		return musicActionPrev
	case uint8(sdl.CONTROLLER_BUTTON_DPAD_LEFT), uint8(sdl.CONTROLLER_BUTTON_DPAD_RIGHT):
		return musicActionSeek
	}
	return musicActionNone
}
