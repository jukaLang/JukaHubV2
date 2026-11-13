package main

// voiceboost.go — getting the voice assistant's slow parts going early.
//
// On the Smart Pro a spoken question passes through three CPU-heavy steps:
// speech recognition, the offline model (when there is no Groq) and Piper's
// voice. voicePrewarm runs when the orb starts listening — while the user is
// still talking — and:
//
//   - raises the CPU to the performance governor at the policy's full ceiling
//     for a short while (voiceBoostHold), with the same helpers and crash
//     marker as a game launch (powerprofile.go). Every later step extends it;
//     it lapses voiceBoostIdle after the last, ends at once when a game
//     starts, and is never applied while a launch owns the CPU;
//   - loads the offline model when an offline answer is possible and memory
//     allows (offline_ai_server.go);
//   - starts Piper when Juka will speak with the device's voice, so the first
//     sentence — or "Opening YouTube" — is spoken without loading the voice
//     first (tts_local_other.go).
//
// Memory on a 1 GB handheld is shared by these rules (MemAvailable, MB):
//   - speech recognition comes first: voiceSTTReserveMB stays free for it
//     while the user talks;
//   - the model preloads only with offlineAIPrewarmMB free (its own need plus
//     that reserve); otherwise it loads after the words are recognised, with
//     offlineAIStartMB free;
//   - Piper preloads with voicePiperWarmMB beyond those;
//   - anything idle lets go when free memory falls below the low-water marks,
//     and all of it when a game starts (voiceReleaseForGame).

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// voiceSTTReserveMB is kept for the speech recognizer's server, which
	// may be loading while the user talks.
	voiceSTTReserveMB = 150
	// voicePiperWarmMB is what a loaded Piper voice takes.
	voicePiperWarmMB = 120
	// voiceBoostListen covers a question being spoken and recognised;
	// voiceBoostAnswer an answer being written; voiceBoostSpeak a line
	// being voiced. voiceBoostMax bounds any single hold.
	voiceBoostListen = 30 * time.Second
	voiceBoostAnswer = 45 * time.Second
	voiceBoostSpeak  = 15 * time.Second
	voiceBoostMax    = 60 * time.Second
)

// voiceBoost is the CPU boost in force. Any goroutine, under mu.
var voiceBoost struct {
	mu        sync.Mutex
	on        bool
	until     time.Time
	timer     *time.Timer
	governors []cpuGovernorSave
	ceiling   []cpuFrequencySave
	cfg       powerProfileConfig
	cfgSet    bool
	refused   bool // the firmware said no once: not tried again this run
}

// voiceCleanupHooked records that the exit hook is registered.
var voiceCleanupHooked atomic.Bool

// voiceBoostSupported: the handheld only; a PC has nothing to raise.
func voiceBoostSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// voicePrewarm gets recognition, the model and the voice ready while the user
// talks. Main thread (it reads the config). Cheap: process starts happen on
// other goroutines.
func voicePrewarm(config *Config) {
	if !voiceBoostSupported() {
		return
	}
	if config == nil {
		config = appConfig
	}
	if !sttCanListenFor(config) {
		return // the orb only says what is missing
	}
	if !voiceCleanupHooked.Swap(true) {
		Cleanup = append(Cleanup, voiceReleaseAll)
	}
	voiceBoost.mu.Lock()
	voiceBoost.cfg, voiceBoost.cfgSet = loadPowerProfileConfig(config), true
	voiceBoost.mu.Unlock()
	voiceBoostHold(voiceBoostListen)
	avail := freeMemoryMB()
	key := strings.TrimSpace(groqAPIKey())
	online := IsOnline() || netReconnecting()
	// A paused game keeps its memory, and its questions go to Groq
	// (gameassist.go): no model then.
	llm := false
	if !gaOverlay.active && runningGameChildren.Load() == 0 && orbAnswerChoice(key, online, offlineAIReady()) == "offline" {
		llm = offlineAIPrewarm(avail)
	}
	if voiceDeviceVoiceLikely(config, key, online) {
		need := uint64(voiceSTTReserveMB + voicePiperWarmMB)
		if llm {
			need += offlineAIServerNeedMB
		}
		if avail >= need {
			go ttsPiperPrewarm()
		}
	}
}

// voiceDeviceVoiceLikely reports whether Juka's next voice answer will come
// from the device's own voice: voice answers are on and Groq's voice is not
// chosen or cannot be reached.
func voiceDeviceVoiceLikely(config *Config, key string, online bool) bool {
	if quietModeOn() || !ttsModeAllows(config, ttsKindVoice) {
		return false
	}
	return ttsVoiceOf(config) == ttsVoiceDevice || key == "" || !online
}

// voiceBoostHold raises the CPU, or keeps it raised, for d more. Any
// goroutine. Nothing happens before the orb was first opened, while a launch
// owns the CPU, with the launch profile switched off in Settings, or after
// the firmware refused once.
func voiceBoostHold(d time.Duration) {
	if !voiceBoostSupported() {
		return
	}
	if d > voiceBoostMax {
		d = voiceBoostMax
	}
	voiceBoost.mu.Lock()
	defer voiceBoost.mu.Unlock()
	until := time.Now().Add(d)
	if voiceBoost.on {
		if until.After(voiceBoost.until) {
			voiceBoost.until = until
			voiceBoost.timer.Reset(d)
		}
		return
	}
	// Only once the orb has run this session (voicePrewarm read the setting):
	// a notification read aloud alone does not raise the clock.
	cfg := voiceBoost.cfg
	if !voiceBoost.cfgSet || voiceBoost.refused || !cfg.Enabled || voiceBoostLaunchActive() {
		return
	}
	paths := cpuGovernorPaths(cpufreqRoot)
	if len(paths) == 0 {
		return
	}
	// An idle screen's low clock goes first (saver_power.go): recorded as
	// the value to restore, it would outlive the boost, and the saver's own
	// compare-and-restore would then skip it.
	saverPowerWake()
	cfg.Balanced = false
	governors := readGovernorSaves(paths)
	ceiling, _, ceilingErr := applyFrequencyCeiling(cfg)
	if _, err := applyPowerProfile(powerProfilePerformance, cfg); err != nil {
		restoreFrequencyCeiling(ceiling)
		_ = restoreGovernors(governors)
		voiceBoost.refused = true
		logDebug("voice", "CPU boost unavailable: %v", err)
		return
	}
	if ceilingErr != nil {
		logDebug("voice", "CPU boost without a higher ceiling: %v", ceilingErr)
	}
	// A crash while boosted is undone by the next start, as a game's is.
	bootCPUMarkerWrite(governors, ceiling)
	voiceBoost.on = true
	voiceBoost.until = until
	voiceBoost.governors, voiceBoost.ceiling = governors, ceiling
	if voiceBoost.timer == nil {
		voiceBoost.timer = time.AfterFunc(d, voiceBoostLapse)
	} else {
		voiceBoost.timer.Reset(d)
	}
	logDebug("voice", "CPU boost on")
}

// voiceBoostLaunchActive reports whether a game or app launch holds the CPU
// profile (it decides the clock then).
func voiceBoostLaunchActive() bool {
	if runningGameChildren.Load() > 0 {
		return true
	}
	launchPowerProfile.mu.Lock()
	depth := launchPowerProfile.depth
	launchPowerProfile.mu.Unlock()
	return depth > 0
}

// voiceBoostLapse is the timer: the boost ends unless it was extended.
func voiceBoostLapse() {
	voiceBoost.mu.Lock()
	defer voiceBoost.mu.Unlock()
	if voiceBoost.on && time.Now().Before(voiceBoost.until) {
		voiceBoost.timer.Reset(time.Until(voiceBoost.until))
		return
	}
	// Under the same lock as the check, so a hold that lands in between is
	// not undone.
	voiceBoostEndLocked()
}

// voiceBoostEnd puts the governor and ceiling back now. Any goroutine; a
// launch calls it before reading the values it will restore later.
func voiceBoostEnd() {
	voiceBoost.mu.Lock()
	defer voiceBoost.mu.Unlock()
	voiceBoostEndLocked()
}

// voiceBoostEndLocked is voiceBoostEnd with mu held.
func voiceBoostEndLocked() {
	if !voiceBoost.on {
		return
	}
	voiceBoost.on = false
	if voiceBoost.timer != nil {
		voiceBoost.timer.Stop()
	}
	restoreFrequencyCeiling(voiceBoost.ceiling)
	if err := restoreGovernors(voiceBoost.governors); err != nil {
		logWarn("voice", "CPU boost: could not restore the governor (%v)", err)
	}
	voiceBoost.governors, voiceBoost.ceiling = nil, nil
	bootCPUMarkerClear()
	logDebug("voice", "CPU boost off")
}

// voiceBoostActive reports whether the boost is in force.
func voiceBoostActive() bool {
	voiceBoost.mu.Lock()
	defer voiceBoost.mu.Unlock()
	return voiceBoost.on
}

// voiceReleaseForGame gives a starting game everything the voice assistant
// held: the model, Piper and the CPU boost. gamesuspend.go calls it.
func voiceReleaseForGame() {
	offlineAIServerStop("a game started")
	ttsPiperWarmStop()
	voiceBoostEnd()
}

// voiceReleaseAll is the exit hook.
func voiceReleaseAll() {
	offlineAIServerStop("JukaHub is closing")
	ttsPiperWarmStop()
	voiceBoostEnd()
}
