package main

// narrate_settings.go — Settings → Accessibility → Narration, and turning it
// on and off from anywhere.
//
// The rows sit at the top of the Accessibility page: Narration (on/off),
// Speaking rate, Read hints, Read messages, and on a PC the Windows voice. The
// handheld has one voice, the offline Lessac voice — the lightest and quickest
// there is (the owner's rule: no optional nicer voices on the TrimUI) — so it
// has no voice row.
//
// Turning narration on when that voice is not on the card yet starts its
// download (tts_install.go, the same pinned download as Settings → Voice &
// AI's), with a message at each quarter; offline, it says the voice needs
// Wi-Fi once and starts by itself when the handheld is online. Until the voice
// is there, a focused control answers with a soft tone (narrEarconWait). The
// same flow runs when L1 + R1 or Ctrl+Alt+Z turn narration on.

import (
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// The Custom settings.
const (
	narrKeyOn     = "narration"
	narrKeyRate   = "narrationRate"
	narrKeyHints  = "narrationHints"
	narrKeyToasts = "narrationToasts"
	narrKeyVoice  = "narrationVoice"
)

// narrRateOptions are the speaking rates, in percent of the voice's own.
var narrRateOptions = []setOption{
	{Value: "80", Label: "0.8×"},
	{Value: "100", Label: "1× (normal)"},
	{Value: "125", Label: "1.25×"},
	{Value: "150", Label: "1.5×"},
	{Value: "175", Label: "1.75×"},
	{Value: "200", Label: "2×"},
	{Value: "250", Label: "2.5×"},
}

// narrVoiceOptions are Windows' voices by kind.
var narrVoiceOptions = []setOption{
	{Value: "default", Label: "Windows default"},
	{Value: "female", Label: "Female"},
	{Value: "male", Label: "Male"},
}

// narrRateOf is the stored rate, 100 when unset or not a stop.
func narrRateOf(c *Config) int {
	if c == nil {
		return 100
	}
	v := customStringValue(c.Variables.Custom, narrKeyRate)
	if ttsOptionIndex(narrRateOptions, v) < 0 {
		return 100
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 100
	}
	return n
}

// narrVoiceOf is the stored Windows voice ("default" when unset).
func narrVoiceOf(c *Config) string {
	if c == nil {
		return "default"
	}
	v := customStringValue(c.Variables.Custom, narrKeyVoice)
	if ttsOptionIndex(narrVoiceOptions, v) < 0 {
		return "default"
	}
	return v
}

// narrBind mirrors the settings into the atomics; narrateEndFrame calls it
// once a second, so a config reload or a phone's change applies too. Main
// thread.
func narrBind(c *Config) {
	if c == nil {
		return
	}
	m := c.Variables.Custom
	on := narrStoredOn(m)
	if !on && !narrOn.Load() {
		// Off and staying off (everyone's default): nothing else is read.
		return
	}
	narrHints.Store(customBool(m, narrKeyHints, true))
	narrToasts.Store(customBool(m, narrKeyToasts, true))
	if rate := int32(narrRateOf(c)); narrRate.Swap(rate) != rate {
		narrMemClear()
	}
	gender := int32(0)
	switch narrVoiceOf(c) {
	case "female":
		gender = 1
	case "male":
		gender = 2
	}
	narrGender.Store(gender)
	if on == narrOn.Load() {
		return
	}
	narrOn.Store(on)
	if on {
		// On at start-up (or from a reload): the voice is got ready quietly.
		narrPrepare(c)
	} else {
		narrDropPending()
	}
}

// narrStoredOn is the stored Narration switch, read without allocating for
// the shapes setStoreBool and a JSON config give it (narrBind runs once a
// second for everyone).
func narrStoredOn(m map[string]interface{}) bool {
	switch v := m[narrKeyOn].(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		switch v {
		case "", "false":
			return false
		case "true":
			return true
		}
	}
	return customBool(m, narrKeyOn, false)
}

// narrToggle switches narration from the L1 + R1 hold or Ctrl+Alt+Z.
func narrToggle(c *Config) {
	narrSetEnabled(c, !narrOn.Load())
}

// narrSetEnabled turns narration on or off, saves it and confirms it with a
// tone (and, on, with the voice once it can speak). Main thread.
func narrSetEnabled(c *Config, on bool) {
	// First, so narrBind finds it already switched and leaves the rest here.
	narrOn.Store(on)
	if c != nil {
		setStoreBool(c, narrKeyOn, on)
		narrBind(c)
	}
	if !on {
		narrDropPending()
		narrPlayEarcon(narrEarconOff)
		setToast("Narration off")
		logInfo("narrate", "narration off")
		return
	}
	narrPlayEarcon(narrEarconOn)
	logInfo("narrate", "narration on")
	narrGreet()
	narrPrepare(c)
}

// narrGreet has "Narration on." said before the focused control, which is
// read afresh. Main thread.
func narrGreet() {
	narrLastNoted = narrItem{}
	narr.mu.Lock()
	narr.greet, narr.greetAt, narr.fresh = "Narration on.", time.Now(), true
	narrStartLocked()
	narr.mu.Unlock()
	narrWake()
}

// narrPrepare gets the voice ready: loaded when it is on the device,
// downloading when it is not. Main thread.
func narrPrepare(c *Config) {
	if _, ok := ttsLocal(); ok {
		go ttsPiperPrewarm()
		narrQueueCommon()
		return
	}
	narrVoiceMissing(c)
}

// narrVoiceMissing explains and fetches a missing voice. Main thread.
func narrVoiceMissing(c *Config) {
	if !narrOn.Load() {
		return
	}
	if !ttsOfflineVoiceSupported() {
		// A PC with no speech engine (a Linux desktop without espeak).
		if runtime.GOOS != "windows" {
			ShowToast("Narration needs a speech engine on this computer — install espeak-ng", ToastKindWarning)
		} else {
			ShowToast("Narration could not find Windows' speech voice", ToastKindWarning)
		}
		return
	}
	switch {
	case ttsVoiceInstalling():
		ShowToast("The narration voice is downloading — it speaks as soon as it is ready", ToastKindInfo)
	case !IsOnline():
		ShowToast("Narration needs its voice, a one-time download of about 90 MB — connect to Wi-Fi and it starts by itself", ToastKindWarning)
	case ttsStartVoiceInstall(true):
		ShowToast("Downloading the narration voice (about 90 MB)…", ToastKindInfo)
	default:
		ShowToast("The narration voice downloads when the current download is done", ToastKindInfo)
	}
	narrWatchVoice()
}

// narrVoiceWatching is set while narrVoiceWatchLoop runs.
var narrVoiceWatching atomic.Bool

// narrWatchVoice follows the voice's download: a message at each quarter, a
// retry when Wi-Fi comes back, and "Narration on" once it can speak.
func narrWatchVoice() {
	if !narrVoiceWatching.CompareAndSwap(false, true) {
		return
	}
	go narrVoiceWatchLoop()
}

func narrVoiceWatchLoop() {
	defer narrVoiceWatching.Store(false)
	quarter, tries := -1, 0
	lastTry := time.Now()
	for i := 0; i < 1800 && narrOn.Load(); i++ {
		time.Sleep(2 * time.Second)
		if _, ok := ttsLocal(); ok {
			tbxPost(func() {
				narrPlayEarcon(narrEarconOn)
				ShowToast("Narration voice ready", ToastKindSuccess)
				narrGreet()
				go ttsPiperPrewarm()
				narrQueueCommon()
			})
			return
		}
		if ttsVoiceInstalling() {
			installBar.mu.Lock()
			done, total, tool := installBar.done, installBar.total, installBar.tool
			installBar.mu.Unlock()
			if tool == "offline voice" && total > 0 {
				if q := int(done * 4 / total); q > quarter && q > 0 && q < 4 {
					quarter = q
					// The install bar and the live notification row already
					// show the percentage: a toast saying it again was the
					// duplicate indicator. Keep only the earcon.
					tbxPost(func() { narrPlayEarcon(narrEarconWait) })
				}
			}
			continue
		}
		// Not downloading and not there: once online, try again (a few times
		// a run; a refused file is not fetched over and over).
		if tries < 3 && IsOnline() && time.Since(lastTry) > 20*time.Second {
			tries++
			lastTry = time.Now()
			quarter = -1
			tbxPost(func() {
				if ttsOfflineVoiceSupported() && !ttsVoiceInstalling() {
					ttsStartVoiceInstall(true)
				}
			})
		}
	}
}

// narrQueueCommon has the worker make the pieces said on every screen ahead,
// once: the states, the hints in this input's words, the screens' names and
// the Settings sections. Main thread (setSections).
func narrQueueCommon() {
	texts := []string{"Narration on.", "on", "off", "button", "selected", "unavailable", "suggestion",
		"Settings", "Menu", "Home", "Games", "Keyboard", "Top bar",
		narrHintPhrase("Switch"), narrHintPhrase("Open"), narrHintPhrase("Edit"), narrHintPhrase("Select"),
		narrHintPhrase("Left and right change it."), narrHintPhrase("Left and right change it, or list them all with A.")}
	for _, s := range setSections() {
		texts = append(texts, s.Title)
	}
	narrAheadQueue(texts)
	narrWake()
}

// ── The rows ─────────────────────────────────────────────────────────────

// narrateSettingsRows are the Narration rows, first on the Accessibility page.
func narrateSettingsRows() []setRow {
	shown := func(*Config) bool { return narrOn.Load() }
	return []setRow{
		{
			ID: narrKeyOn, Label: "Narration", Kind: setKindToggle,
			Detail: narrDetail,
			Get:    func(c *Config) bool { return c != nil && customBool(c.Variables.Custom, narrKeyOn, false) },
			Set:    narrSetEnabled,
			Quiet:  true,
		},
		{
			ID: narrKeyRate, Label: "Speaking rate", Kind: setKindChoice, Clamp: true,
			Detail:  setStatic("How fast narration speaks. The pitch stays the same."),
			Visible: shown,
			Options: func(*Config) []setOption { return narrRateOptions },
			Current: func(c *Config) int { return ttsOptionIndex(narrRateOptions, strconv.Itoa(narrRateOf(c))) },
			Choose: func(c *Config, value string) {
				if c == nil || ttsOptionIndex(narrRateOptions, value) < 0 {
					return
				}
				setStoreString(c, narrKeyRate, value)
				narrBind(c)
			},
		},
		{
			ID: narrKeyHints, Label: "Read hints", Kind: setKindToggle,
			Detail:  setStatic("Says what A does, and a setting's description when you rest on it"),
			Visible: shown,
			Get:     func(c *Config) bool { return c != nil && customBool(c.Variables.Custom, narrKeyHints, true) },
			Set: func(c *Config, on bool) {
				setStoreBool(c, narrKeyHints, on)
				narrBind(c)
			},
		},
		{
			ID: narrKeyToasts, Label: "Read messages", Kind: setKindToggle,
			Detail:  setStatic("Reads the messages that pop up, after the focused control"),
			Visible: shown,
			Get:     func(c *Config) bool { return c != nil && customBool(c.Variables.Custom, narrKeyToasts, true) },
			Set: func(c *Config, on bool) {
				setStoreBool(c, narrKeyToasts, on)
				narrBind(c)
			},
		},
		{
			ID: narrKeyVoice, Label: "Narration voice", Kind: setKindChoice,
			Detail:  setStatic("Which of Windows' installed voices narration uses"),
			Visible: func(*Config) bool { return narrOn.Load() && runtime.GOOS == "windows" },
			Options: func(*Config) []setOption { return narrVoiceOptions },
			Current: func(c *Config) int { return ttsOptionIndex(narrVoiceOptions, narrVoiceOf(c)) },
			Choose: func(c *Config, value string) {
				if c == nil || ttsOptionIndex(narrVoiceOptions, value) < 0 {
					return
				}
				setStoreString(c, narrKeyVoice, value)
				narrBind(c)
			},
		},
	}
}

// narrDetail is the Narration row's line: what it does, how to switch it
// from anywhere, and how its voice stands.
func narrDetail(*Config) string {
	const chord = "Reads the focused control aloud. Hold L1 and R1 for 3 seconds, or press Ctrl+Alt+Z, to switch it from any screen."
	if !ttsOfflineVoiceSupported() {
		return chord
	}
	switch _, legacy, ok := ttsManagedVoiceState(); {
	case ok && !legacy:
		return chord + " Uses the offline voice on the card."
	case ok:
		return chord + " Uses the offline voice on the card (the quicker one downloads when Wi-Fi is free)."
	case ttsVoiceInstalling():
		return chord + " The offline voice is downloading."
	}
	return chord + " Turning it on downloads the offline voice once (about 90 MB)."
}
