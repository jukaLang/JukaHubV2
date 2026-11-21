package main

// bootlock.go — Startup lock: a button code or a password before JukaHub
// takes any input.
//
// The owner asked for "a password for when JukaHub opens on boot". The
// handheld has no keyboard, so there are two kinds:
//
//   - a button code: 4 to 8 presses of the D-pad, A, B, X, Y, L1 and R1, read
//     as the physical buttons, before the A/B and X/Y swaps (changing a swap
//     later does not change the code). Start submits, Select deletes the last
//     press (and, with nothing typed, leaves a setup), Menu opens the power
//     choices. A held D-pad's synthetic repeats (navrepeat.go) are not
//     presses;
//   - a password typed on the on-screen keyboard (a real one on a PC), through
//     its own submit-only target (bootLockVKTarget), masked like the Wi-Fi key.
//
// Either can have a spoken phrase beside it (Voice unlock, bootlock_voice.go):
// the phrase or the code ("either"), or the code and then the phrase ("both").
// Never the phrase alone: speech recognition can let its owner down.
//
// When it locks (Settings → Privacy & data → Lock when): at power-on (the
// default: bootOSPowerOnActive), every time JukaHub opens, or every start and
// after the device slept (a JukaHub sleep flags it as it begins, powersleep.go;
// a firmware sleep is seen by the wake watcher, wake_resume.go).
//
// The lock is a lock for the screen, not encryption: jukauser.json, the games
// and the saves are plain files on the SD card. The setting says so, and the
// lock screen says how to remove a forgotten code (bootLockForgotText).
//
// Storage: never the code. startupLockSecret holds
// "jhlock1$<kind>$<iterations>$<salt>$<key>", PBKDF2-HMAC-SHA256 over
// "<kind>:<code>" with a random 16-byte salt (argon2 is not vendored), and the
// check is a constant-time compare. startupLockFails counts wrong attempts in
// a row: after bootLockFreeTries each further try waits 30 s, doubling to 5
// minutes. The count is persisted at once, and a restart re-imposes the
// current step's whole wait — counted by attempts and timed by SDL's monotonic
// ticks, never by the wall clock (the TrimUI has no RTC).
//
// While the lock is up (startupLocked) it takes every press first (main.go
// bootLockEvent), draws over everything but a ringing alarm (bootLockRender),
// and the voice orb, "Hey Juka", the game assistant, Quick Settings and the
// phone companion's acting endpoints (HTTP 423) all stand aside. The power
// choices stay one press away. The FTP and SSH servers are stopped when it
// locks, so files cannot be reached around it.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// bootLockSecretKey is the Custom key with the hashed code ("" or absent:
	// no lock). Bookkeeping, not a setting: cfgdoc never exports it.
	bootLockSecretKey = "startupLockSecret"
	// bootLockWhenKey is the Custom setting behind "Lock when".
	bootLockWhenKey = "startupLockWhen"
	// bootLockFailsKey counts wrong attempts in a row (bookkeeping).
	bootLockFailsKey = "startupLockFails"
	// bootLockPendingKey is true while a lock is up and not yet unlocked
	// (bookkeeping): the next start locks again, power-on or not.
	bootLockPendingKey = "startupLockPending"

	bootLockWhenPower = "power"
	bootLockWhenStart = "start"
	bootLockWhenWake  = "wake"

	bootLockKindCode     = "code"
	bootLockKindPassword = "password"

	bootLockHashTag = "jhlock1"
	bootLockIter    = 120000
	bootLockSaltLen = 16
	bootLockKeyLen  = 32

	bootLockCodeMin = 4
	bootLockCodeMax = 8
	bootLockPassMin = 4
	bootLockPassMax = 64

	// bootLockFreeTries wrong attempts cost nothing; the next waits.
	bootLockFreeTries = 5
	bootLockWaitBase  = 30 * time.Second
	bootLockWaitMax   = 5 * time.Minute

	// bootLockVKTarget is the on-screen keyboard's target for the password:
	// submit-only, masked, no suggestions (vkeyboard.go, vkedit.go).
	bootLockVKTarget vkTargetKind = 117
)

// ── Hashing (pure) ───────────────────────────────────────────────────────

// bootLockStored is a parsed startupLockSecret.
type bootLockStored struct {
	kind string
	iter int
	salt []byte
	key  []byte
}

var bootLockB64 = base64.RawStdEncoding

// bootLockHash makes the stored form of a new code.
func bootLockHash(kind, secret string) (string, error) {
	salt := make([]byte, bootLockSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, kind+":"+secret, salt, bootLockIter, bootLockKeyLen)
	if err != nil {
		return "", err
	}
	return bootLockHashTag + "$" + kind + "$" + strconv.Itoa(bootLockIter) + "$" +
		bootLockB64.EncodeToString(salt) + "$" + bootLockB64.EncodeToString(key), nil
}

// bootLockParse reads a stored code. A value it cannot read is no lock (a
// hand edit to anything else removes it, as the forgot hint says).
func bootLockParse(s string) (bootLockStored, bool) {
	parts := strings.Split(strings.TrimSpace(s), "$")
	if len(parts) != 5 || parts[0] != bootLockHashTag {
		return bootLockStored{}, false
	}
	st := bootLockStored{kind: parts[1]}
	if st.kind != bootLockKindCode && st.kind != bootLockKindPassword {
		return bootLockStored{}, false
	}
	iter, err := strconv.Atoi(parts[2])
	// Bounded both ways: a hand-edited huge count must not freeze the check.
	if err != nil || iter < 1000 || iter > 5000000 {
		return bootLockStored{}, false
	}
	st.iter = iter
	if st.salt, err = bootLockB64.DecodeString(parts[3]); err != nil || len(st.salt) < 8 {
		return bootLockStored{}, false
	}
	if st.key, err = bootLockB64.DecodeString(parts[4]); err != nil || len(st.key) != bootLockKeyLen {
		return bootLockStored{}, false
	}
	return st, true
}

// bootLockVerify reports whether secret is the stored code, in constant time.
// Slow on purpose (PBKDF2): call it off the main thread.
func bootLockVerify(stored, secret string) bool {
	st, ok := bootLockParse(stored)
	if !ok {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, st.kind+":"+secret, st.salt, st.iter, len(st.key))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, st.key) == 1
}

// bootLockWaitFor is the wait before the next attempt after fails wrong ones
// in a row: none for the first bootLockFreeTries, then 30 s doubling to 5 min.
func bootLockWaitFor(fails int) time.Duration {
	if fails < bootLockFreeTries {
		return 0
	}
	d := bootLockWaitBase
	for i := bootLockFreeTries; i < fails && d < bootLockWaitMax; i++ {
		d *= 2
	}
	if d > bootLockWaitMax {
		d = bootLockWaitMax
	}
	return d
}

// ── Stored values ────────────────────────────────────────────────────────

func bootLockSecretFrom(m map[string]interface{}) string {
	s, _ := m[bootLockSecretKey].(string)
	return strings.TrimSpace(s)
}

func bootLockFailsFrom(m map[string]interface{}) int {
	n := 0
	switch v := m[bootLockFailsKey].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		n, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	if n < 0 {
		return 0
	}
	if n > 1000 {
		return 1000
	}
	return n
}

func bootLockWhenFrom(m map[string]interface{}) string {
	s, _ := m[bootLockWhenKey].(string)
	switch s {
	case bootLockWhenStart, bootLockWhenWake, bootLockWhenPower:
		return s
	}
	// Not chosen yet: a PC never starts "straight into JukaHub" at power-on,
	// so there a lock that defaulted to power-on would never lock.
	if !IsTrimuiDevice() {
		return bootLockWhenStart
	}
	return bootLockWhenPower
}

// bootLockStoreFails sets the attempt counter in a Custom map (a JSON number,
// as a reload reads it back).
// Test seam (ARCH-8): only tests call it.
func bootLockStoreFails(m map[string]interface{}, n int) {
	m[bootLockFailsKey] = float64(n)
}

// bootLockKindOf is the kind of lock in force, or "" for none.
func bootLockKindOf(c *Config) string {
	if c == nil {
		return ""
	}
	if st, ok := bootLockParse(bootLockSecretFrom(c.Variables.Custom)); ok {
		return st.kind
	}
	return ""
}

// bootLockWrite stores one value and writes jukauser.json now, not two
// seconds from now: an attempt counted must survive pulling the battery.
func bootLockWrite(c *Config, key string, value interface{}) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[key] = value
	saveUserConfig(userConfigFromVariables(c))
}

// ── State ────────────────────────────────────────────────────────────────

type bootLockMode uint8

const (
	bootLockModeOff    bootLockMode = iota // nothing up
	bootLockModeLocked                     // the lock screen
	bootLockModeSetup                      // Settings: set, change or remove it
)

type bootLockStep uint8

const (
	bootLockStepCurrent bootLockStep = iota // the code in force, first
	bootLockStepNew                         // the new code
	bootLockStepConfirm                     // the new code again
)

type bootLockPurpose uint8

const (
	bootLockForUnlock bootLockPurpose = iota
	bootLockForCurrent
	bootLockForSave
)

// bootLockResult is a check or a hash made off the main thread.
type bootLockResult struct {
	purpose bootLockPurpose
	ok      bool
	hash    string
}

// bootLock is the overlay. Main thread only; bootLockUp mirrors "locked" for
// the companion's HTTP goroutines.
var bootLock struct {
	mode   bootLockMode
	step   bootLockStep
	kind   string // what is typed now
	target string // setup: the kind being set, "" to remove the lock
	entry  []byte // button presses so far
	first  string // setup: the new code's first entry
	note   string
	warn   bool

	checking bool
	result   chan bootLockResult
	// checkFor is what the running check is for: a phrase's check leaves the
	// code free to type (bootLockCodeFree), and a code submitted meanwhile is
	// queued until it ends.
	checkFor bootLockPurpose
	queued   string

	// waitUntil is the SDL tick (monotonic ms) before which no attempt counts.
	waitUntil uint64
	// wantKeyboard opens the password keyboard on the next frame (the
	// keyboard is not set up yet when the lock arms before the boot).
	wantKeyboard bool
	// dropText drops the text of a key press that only woke the screen.
	dropText bool

	power      bool
	powerFocus int
	powerRects []sdl.Rect

	wakeSeen uint64
}

// bootLockUp is true while the lock screen is up. Any goroutine.
var bootLockUp atomic.Bool

// bootLockSlept is set as a JukaHub sleep begins (powersleep.go).
var bootLockSlept atomic.Bool

// bootLockBuffer holds the password while it is typed; the keyboard clears it
// when it closes (vkeyboard.go).
var bootLockBuffer string

// startupLocked reports whether the startup lock is up: nothing may act
// until it is unlocked. Safe from any goroutine.
func startupLocked() bool { return bootLockUp.Load() }

// bootLockOverlayUp reports whether the lock screen or its setup owns the
// screen. Main thread.
func bootLockOverlayUp() bool { return bootLock.mode != bootLockModeOff }

// bootLockNoteSleep is powersleep.go's hook as a sleep begins: with "after
// sleep" chosen, the lock goes up before the screen comes back. Any goroutine.
func bootLockNoteSleep() { bootLockSlept.Store(true) }

// bootLockArm decides, once before the boot animation, whether this start is
// locked (main.go). The boot then ends on its hold instead of drawing Home's
// arrival (bootos.go), so nothing of Home is ever on screen before the code.
func bootLockArm(config *Config) {
	bootLock.wakeSeen = wakeCount()
	if config == nil {
		return
	}
	secret := bootLockSecretFrom(config.Variables.Custom)
	st, ok := bootLockParse(secret)
	if !ok {
		if secret != "" {
			logWarn("lock", "startup lock: the stored code is unreadable; no lock this start")
		}
		return
	}
	when := bootLockWhenFrom(config.Variables.Custom)
	if when == bootLockWhenWake {
		// Firmware sleeps are seen by the wake watcher, which the network
		// monitor starts — not in safe mode (wake_resume.go).
		startWakeWatcher()
	}
	// A start that never got past the lock (a crash, an update's restart, a
	// closed window) locks again whatever "Lock when" says: a relaunch is not
	// a power-on, and must not be the way around the code.
	pending := bootLockPendingFrom(config.Variables.Custom)
	if when == bootLockWhenPower && !pending && !bootLockPowerOn(config) {
		logInfo("lock", "startup lock: not a power-on start, not locked")
		return
	}
	bootLockEngage(config, st.kind, "start")
}

// bootLockPowerOn reports a start straight from power-on whatever the boot
// animation setting: "Quick" makes bootOSDecide answer false for every start,
// which turned the default "At power-on" lock off without a word.
func bootLockPowerOn(config *Config) bool {
	if bootOSPowerOnActive() {
		return true
	}
	in := bootOSLiveInputs(config, time.Now())
	in.Setting = bootOSAnimationPower
	on, _ := bootOSDecide(in)
	return on
}

// bootLockPendingFrom reports a lock that was up and never unlocked.
func bootLockPendingFrom(m map[string]interface{}) bool {
	on, _ := m[bootLockPendingKey].(bool)
	return on
}

// bootLockEngage puts the lock screen up.
func bootLockEngage(config *Config, kind, why string) {
	if bootLock.mode == bootLockModeLocked || config == nil {
		return
	}
	if virtualKeyboardActive {
		// Whatever was being typed (a setup's code, a chat line) goes.
		vkCloseKeyboard()
	}
	// A question asked before the sleep must not be answered — and acted
	// on — behind the lock.
	if voiceOrb.phase != orbHidden {
		voiceOrbCancel()
	}
	bootLockReset()
	bootLock.mode = bootLockModeLocked
	bootLock.kind = kind
	bootLockUp.Store(true)
	bootLockImposeWait(config)
	bootLock.wantKeyboard = kind == bootLockKindPassword
	// The file servers would reach the SD card around the lock.
	if ftpSrv.Running() {
		ftpSrv.Stop()
	}
	if sshSrv.Running() {
		sshSrv.Stop()
	}
	// VNC would control, and Telnet's shell reach, what the lock guards.
	vncTelnetLockStop()
	if davSrv.Running() {
		davSrv.Stop()
	}
	logInfo("lock", "startup lock up (%s)", why)
}

// bootLockImposeWait starts the wait the persisted attempt count asks for: a
// restart does not shorten it.
func bootLockImposeWait(config *Config) {
	bootLock.waitUntil = 0
	if config == nil {
		return
	}
	if d := bootLockWaitFor(bootLockFailsFrom(config.Variables.Custom)); d > 0 {
		bootLock.waitUntil = sdl.GetTicks64() + uint64(d.Milliseconds())
	}
}

// bootLockReset clears what was typed and said.
func bootLockReset() {
	for i := range bootLock.entry {
		bootLock.entry[i] = 0
	}
	bootLock.entry = bootLock.entry[:0]
	bootLock.first = ""
	bootLock.queued = ""
	bootLock.note, bootLock.warn = "", false
	bootLock.power = false
	bootLock.wantKeyboard = false
	// The microphone, a phrase on its way and a voice setup go too
	// (bootlock_voice.go).
	bootLockVoiceReset()
}

// bootLockClose takes the overlay down.
func bootLockClose() {
	if bootLockTyping() {
		vkCloseKeyboard()
	}
	bootLockReset()
	bootLock.mode = bootLockModeOff
	bootLock.target = ""
	bootLockUp.Store(false)
	// Settings' rows read the new state on their next refresh.
	setHub.dirty = true
}

// bootLockWaitLeft is the wait still to sit out, or 0.
func bootLockWaitLeft() time.Duration {
	now := sdl.GetTicks64()
	if bootLock.waitUntil <= now {
		return 0
	}
	return time.Duration(bootLock.waitUntil-now) * time.Millisecond
}

// ── Per frame ────────────────────────────────────────────────────────────

// bootLockTick is the main thread's part, once a frame (bootLockRender).
func bootLockTick(config *Config) {
	if config == nil {
		return
	}
	bootLock.dropText = false
	slept := bootLockSlept.Swap(false)
	if n := wakeCount(); n != bootLock.wakeSeen {
		bootLock.wakeSeen = n
		// A JukaHub sleep already locked as it began; the watcher seeing the
		// same sleep a second later must not lock a screen just unlocked.
		if ev, ok := wakeLast(); ok && ev.Suspend && !powerSleepActive() && !powerSleepJustWoke(10*time.Second) {
			slept = true
		}
	}
	if slept && bootLockWhenFrom(config.Variables.Custom) == bootLockWhenWake {
		if kind := bootLockKindOf(config); kind != "" {
			bootLockEngage(config, kind, "after sleep")
		}
	}
	if bootLock.checking {
		select {
		case r := <-bootLock.result:
			bootLock.checking = false
			bootLockFinish(config, r)
		default:
		}
	}
	if !bootLock.checking && bootLock.queued != "" {
		// A code submitted while a phrase was being checked goes now.
		s := bootLock.queued
		bootLock.queued = ""
		bootLockSubmit(config, s)
	}
	if bootLock.mode == bootLockModeOff {
		return
	}
	// Recorded on the first frame, not before the boot: a relaunch of a start
	// that never got past the lock locks again (bootLockArm).
	if bootLock.mode == bootLockModeLocked && !bootLockPendingFrom(config.Variables.Custom) {
		bootLockWrite(config, bootLockPendingKey, true)
	}
	// A hold of the power key opens Home's power card (powerkey.go); over
	// the lock it is the lock's own power choices instead.
	if bootLock.mode == bootLockModeLocked && homePowerConfirm.open {
		homePowerClose()
		bootLockOpenPower()
	}
	if bootLock.mode == bootLockModeSetup && homePowerConfirm.open {
		// The Exit card took the screen from Settings: the setup gives way.
		bootLockCancel()
		return
	}
	if bootLock.wantKeyboard && !bootLock.power && bootLock.kind == bootLockKindPassword &&
		bootLockWaitLeft() == 0 && !bootLock.checking && !alarmFiringActive() {
		bootLock.wantKeyboard = false
		bootLockOpenKeyboard()
	}
	// Voice unlock: listening, its answers, its checks (bootlock_voice.go).
	bootLockVoiceTick(config)
}

// ── Settings ─────────────────────────────────────────────────────────────

// bootLockOpenSetup starts setting the lock to target (a kind, or "" for
// off) from Settings. A lock in force asks for its code first.
func bootLockOpenSetup(c *Config, target string) {
	if c == nil || bootLock.mode != bootLockModeOff {
		return
	}
	st, set := bootLockParse(bootLockSecretFrom(c.Variables.Custom))
	if !set && target == "" {
		setToast("Startup lock is already off")
		return
	}
	bootLockReset()
	bootLock.mode = bootLockModeSetup
	bootLock.target = target
	if set {
		bootLock.step, bootLock.kind = bootLockStepCurrent, st.kind
		bootLockImposeWait(c)
	} else {
		bootLock.step, bootLock.kind = bootLockStepNew, target
		bootLock.waitUntil = 0
	}
	bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
}

// bootLockCancel leaves a setup without changing anything.
func bootLockCancel() {
	if bootLock.mode != bootLockModeSetup || bootLock.checking {
		return
	}
	bootLockClose()
	PlayBackSound()
	ShowToast("Startup lock unchanged", ToastKindInfo)
}

// ── Attempts ─────────────────────────────────────────────────────────────

// bootLockSubmit is one complete entry: the code's presses or the password.
func bootLockSubmit(config *Config, secret string) {
	if bootLockCodeFree() && secret != "" {
		// A heard phrase is being checked: this code is next (bootLockTick).
		bootLock.queued = secret
		return
	}
	if bootLock.checking || config == nil || bootLock.mode == bootLockModeOff {
		return
	}
	if secret == "" {
		PlayErrorSound()
		return
	}
	verifying := bootLock.mode == bootLockModeLocked || bootLock.step == bootLockStepCurrent
	if verifying {
		if left := bootLockWaitLeft(); left > 0 {
			PlayErrorSound()
			return
		}
		// No code is that short: a stray Start is not counted as a try.
		if msg := bootLockTooShort(bootLock.kind, secret); msg != "" {
			bootLockSay(msg, true)
			PlayErrorSound()
			bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
			return
		}
		// The try is counted and saved before the check starts: power
		// pulled while it runs cannot keep a wrong one off the count. A right
		// one sets it back to 0 (bootLockFinish).
		bootLockWrite(config, bootLockFailsKey, float64(bootLockFailsFrom(config.Variables.Custom)+1))
		stored := bootLockSecretFrom(config.Variables.Custom)
		purpose := bootLockForUnlock
		if bootLock.mode == bootLockModeSetup {
			purpose = bootLockForCurrent
		}
		bootLockRun(purpose, func() bootLockResult {
			return bootLockResult{ok: bootLockVerify(stored, secret)}
		})
		return
	}
	switch bootLock.step {
	case bootLockStepNew:
		if msg := bootLockTooShort(bootLock.kind, secret); msg != "" {
			bootLockSay(msg, true)
			PlayErrorSound()
			bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
			return
		}
		bootLock.first = secret
		bootLockClearEntry()
		bootLock.step = bootLockStepConfirm
		bootLockSay("Enter it again to confirm", false)
		PlayNavSound()
	case bootLockStepConfirm:
		if subtle.ConstantTimeCompare([]byte(secret), []byte(bootLock.first)) != 1 {
			bootLock.first = ""
			bootLockClearEntry()
			bootLock.step = bootLockStepNew
			bootLockSay("The two did not match. Choose it again", true)
			PlayErrorSound()
			bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
			return
		}
		bootLock.first = ""
		kind := bootLock.kind
		bootLockRun(bootLockForSave, func() bootLockResult {
			h, err := bootLockHash(kind, secret)
			return bootLockResult{ok: err == nil, hash: h}
		})
	}
	bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword && bootLock.mode != bootLockModeOff
}

// bootLockTooShort says what is wrong with a new code's length, or "".
func bootLockTooShort(kind, secret string) string {
	if kind == bootLockKindPassword {
		switch n := utf8.RuneCountInString(secret); {
		case n < bootLockPassMin:
			return "Use at least " + strconv.Itoa(bootLockPassMin) + " characters"
		case n > bootLockPassMax:
			return "Use at most " + strconv.Itoa(bootLockPassMax) + " characters"
		}
		return ""
	}
	if len(secret) < bootLockCodeMin {
		return "Use at least " + strconv.Itoa(bootLockCodeMin) + " presses"
	}
	return ""
}

// bootLockRun does the slow part on its own goroutine; the frame picks the
// result up (bootLockTick).
func bootLockRun(purpose bootLockPurpose, work func() bootLockResult) {
	bootLock.checking, bootLock.checkFor = true, purpose
	ch := make(chan bootLockResult, 1)
	bootLock.result = ch
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- bootLockResult{purpose: purpose}
			}
		}()
		r := work()
		r.purpose = purpose
		ch <- r
	}()
}

// bootLockFinish applies a check's or a hash's result. Main thread.
func bootLockFinish(config *Config, r bootLockResult) {
	// A phrase's check or hash (bootlock_voice.go) leaves a code being typed
	// alone.
	if bootLockVoiceFinish(config, r) {
		return
	}
	bootLockClearEntry()
	switch r.purpose {
	case bootLockForUnlock, bootLockForCurrent:
		if !r.ok {
			bootLockWrong(config)
			return
		}
		// A "both" voice lock asks for the phrase next, with the "never
		// unlocked" mark still set (bootlock_voice.go).
		if r.purpose == bootLockForUnlock && bootLockVoiceSecondStep(config) {
			return
		}
		if bootLockFailsFrom(config.Variables.Custom) != 0 || bootLockPendingFrom(config.Variables.Custom) {
			// One write for both: the count and the "never unlocked" mark.
			config.Variables.Custom[bootLockPendingKey] = false
			bootLockWrite(config, bootLockFailsKey, float64(0))
		}
		bootLock.waitUntil = 0
		if r.purpose == bootLockForUnlock {
			if bootLock.mode != bootLockModeLocked {
				return
			}
			logInfo("lock", "startup lock: unlocked")
			bootLockClose()
			PlayActivateSound()
			screensaverNotifyInput()
			return
		}
		if bootLock.mode != bootLockModeSetup {
			return
		}
		if bootLockVoice.setup {
			// Voice unlock's setup: off, a mode, or the phrase twice.
			bootLockVoiceAfterCurrent(config)
			return
		}
		if bootLock.target == "" {
			// The phrase only ever goes with a code.
			bootLockVoiceForget(config)
			bootLockWrite(config, bootLockSecretKey, "")
			logInfo("lock", "startup lock: turned off")
			bootLockClose()
			PlayActivateSound()
			ShowToast("Startup lock is off", ToastKindSuccess)
			return
		}
		bootLock.step, bootLock.kind = bootLockStepNew, bootLock.target
		bootLockSay("", false)
		PlayNavSound()
		bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
	case bootLockForSave:
		if bootLock.mode != bootLockModeSetup {
			return
		}
		if !r.ok || r.hash == "" {
			bootLockClose()
			PlayErrorSound()
			ShowToast("Startup lock: the code could not be saved", ToastKindWarning)
			return
		}
		if _, had := bootLockParse(bootLockSecretFrom(config.Variables.Custom)); !had {
			// A phrase left from a lock removed by hand (the forgot hint) must
			// not come back with a new code.
			bootLockVoiceForget(config)
		}
		bootLockWrite(config, bootLockSecretKey, r.hash)
		// JukaHub OS's boot frame would show Home before the lock (bootframe.go).
		bootFrameForget()
		if bootLockPendingFrom(config.Variables.Custom) {
			// Left by a lock whose code was removed by hand (the forgot hint).
			bootLockWrite(config, bootLockPendingKey, false)
		}
		if bootLockFailsFrom(config.Variables.Custom) != 0 {
			bootLockWrite(config, bootLockFailsKey, float64(0))
		}
		logInfo("lock", "startup lock: set (%s)", bootLock.target)
		label := bootLockKindLabel(bootLock.target)
		bootLockClose()
		PlayActivateSound()
		ShowToast("Startup lock is on: "+label+" · "+bootLockWhenLabel(bootLockWhenFrom(config.Variables.Custom)), ToastKindSuccess)
	}
}

// bootLockWrong is a wrong attempt, already counted and persisted as it was
// submitted (bootLockSubmit): it starts the wait the count earns.
func bootLockWrong(config *Config) {
	fails := bootLockFailsFrom(config.Variables.Custom)
	logInfo("lock", "startup lock: wrong code (%d in a row)", fails)
	PlayErrorSound()
	what := "Wrong code"
	if bootLock.kind == bootLockKindPassword {
		what = "Wrong password"
	}
	if d := bootLockWaitFor(fails); d > 0 {
		bootLock.waitUntil = sdl.GetTicks64() + uint64(d.Milliseconds())
		bootLockSay(what, true)
		bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
		return
	}
	left := bootLockFreeTries - fails
	switch {
	case left == 1:
		what += " · 1 more try before a wait"
	case left <= 3:
		what += " · " + strconv.Itoa(left) + " more tries before a wait"
	}
	bootLockSay(what, true)
	bootLock.wantKeyboard = bootLock.kind == bootLockKindPassword
}

func bootLockSay(msg string, warn bool) {
	bootLock.note, bootLock.warn = msg, warn
}

func bootLockClearEntry() {
	for i := range bootLock.entry {
		bootLock.entry[i] = 0
	}
	bootLock.entry = bootLock.entry[:0]
}

// ── Input ────────────────────────────────────────────────────────────────

// bootLockEvent is main.go's hook, right after the sleep filter: while the
// lock (or its setup) is up every input event is its own, except while an
// alarm rings — the alarm's card answers it. Everything that is not input
// (quit, window, a pad plugged in, audio devices) passes.
func bootLockEvent(config *Config, event sdl.Event) bool {
	if bootLock.mode == bootLockModeOff || event == nil {
		return false
	}
	if alarmFiringActive() {
		// Only what the alarm's card answers passes (main.go hands presses,
		// keys, text and clicks to it first); a stick, a wheel or a touch
		// stays the lock's.
		switch event.(type) {
		case *sdl.ControllerButtonEvent, *sdl.KeyboardEvent, *sdl.TextInputEvent, *sdl.MouseButtonEvent:
			return false
		}
	}
	switch e := event.(type) {
	case *sdl.ControllerButtonEvent:
		if e.Type != sdl.CONTROLLERBUTTONDOWN {
			wakeSwallowRelease(wakeInputPad, int32(e.Button))
			return true
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		inputHintsNotePad()
		if wakeSwallowPress(wakeInputPad, int32(e.Button), false) {
			return true
		}
		bootLockPad(config, e)
	case *sdl.KeyboardEvent:
		if e.Type != sdl.KEYDOWN {
			wakeSwallowRelease(wakeInputKey, int32(e.Keysym.Scancode))
			return true
		}
		if frameRateCtrl != nil {
			frameRateCtrl.NotifyInput()
		}
		inputHintsNoteKey(e)
		if wakeSwallowPress(wakeInputKey, int32(e.Keysym.Scancode), e.Repeat != 0) {
			bootLock.dropText = true
			return true
		}
		bootLockKey(config, e)
	case *sdl.TextInputEvent:
		if bootLock.dropText {
			bootLock.dropText = false
			return true
		}
		if bootLockTyping() {
			vkInsertText(e.GetText(), config)
		}
	case *sdl.MouseButtonEvent:
		if e.Type == sdl.MOUSEBUTTONDOWN && e.Button == sdl.BUTTON_LEFT {
			screensaverNotifyInput()
			if bootLockTyping() {
				kmx, kmy := physicalToLogical(e.X, e.Y)
				vkHandlePointerDown(kmx, kmy, vkHoldMouse, config)
			}
		}
	case *sdl.ControllerAxisEvent:
		// R2 is "say the phrase" (bootlock_voice.go); nothing behind sees it.
		bootLockAxis(config, e)
	case *sdl.TextEditingEvent, *sdl.JoyButtonEvent, *sdl.JoyAxisEvent,
		*sdl.JoyHatEvent, *sdl.JoyBallEvent, *sdl.MouseMotionEvent, *sdl.MouseWheelEvent,
		*sdl.TouchFingerEvent, *sdl.MultiGestureEvent, *sdl.DollarGestureEvent, *sdl.DropEvent:
		// Input nothing behind the lock may see.
	default:
		return false
	}
	return true
}

// bootLockTyping reports whether the password keyboard is up.
func bootLockTyping() bool {
	return bootLock.mode != bootLockModeOff && virtualKeyboardActive && vkTarget == bootLockVKTarget
}

// bootLockPadChar is a code button's letter in the hashed string.
func bootLockPadChar(b uint8) (byte, bool) {
	switch sdl.GameControllerButton(b) {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return 'U', true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return 'D', true
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return 'L', true
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return 'R', true
	case sdl.CONTROLLER_BUTTON_A:
		return 'A', true
	case sdl.CONTROLLER_BUTTON_B:
		return 'B', true
	case sdl.CONTROLLER_BUTTON_X:
		return 'X', true
	case sdl.CONTROLLER_BUTTON_Y:
		return 'Y', true
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return 'l', true
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return 'r', true
	}
	return 0, false
}

// bootLockPad is one pad press.
func bootLockPad(config *Config, e *sdl.ControllerButtonEvent) {
	b := sdl.GameControllerButton(e.Button)
	synthetic := e.Which == padRepeatWhich || e.Which == inmapSynthWhich || e.Which == menuBackWhich
	if b == sdl.CONTROLLER_BUTTON_BACK && (e.Which == menuBackWhich || (!synthetic && padEventRaw == sdl.CONTROLLER_BUTTON_GUIDE)) {
		// Menu is still Menu here: on a handheld applyButtonSwap folds the
		// physical GUIDE into BACK (padEventRaw keeps it), and a Menu tap can
		// come as Back (menushot.go).
		b = sdl.CONTROLLER_BUTTON_GUIDE
	}
	// A code is the buttons pressed, not their meaning: read before the A/B
	// and X/Y swaps, so changing those later does not change the code.
	raw := uint8(b)
	if !synthetic {
		raw = padEventRaw
	}
	if bootLock.power {
		switch b {
		case sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			bootLockPowerMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			bootLockPowerMove(1)
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
			bootLockPowerPress(bootLock.powerFocus)
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_GUIDE:
			bootLock.power = false
			PlayBackSound()
		}
		return
	}
	if bootLock.checking && !bootLockCodeFree() {
		return
	}
	if bootLockVoiceStepActive() {
		// The phrase is wanted, not the code (bootlock_voice.go).
		if e.Which != padRepeatWhich {
			bootLockVoicePad(config, b)
		}
		return
	}
	if bootLock.kind == bootLockKindPassword {
		if bootLockTyping() {
			vkHandleControllerButton(e.Button, config)
			return
		}
		switch b {
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
			bootLockOpenKeyboard()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			bootLockCancel()
		case sdl.CONTROLLER_BUTTON_GUIDE:
			bootLockMenu()
		}
		return
	}
	// A held D-pad's repeats are queued as presses (navrepeat.go); a code is
	// pressed, not held.
	if e.Which == padRepeatWhich {
		return
	}
	switch b {
	case sdl.CONTROLLER_BUTTON_START:
		bootLockSubmit(config, string(bootLock.entry))
	case sdl.CONTROLLER_BUTTON_BACK:
		bootLockBackspace()
	case sdl.CONTROLLER_BUTTON_GUIDE:
		bootLockMenu()
	default:
		if c, ok := bootLockPadChar(raw); ok {
			bootLockPress(c)
		}
	}
}

// bootLockKey is one key press: arrows and A/B/X/Y/L/R type a code, Enter
// submits, Backspace deletes, Esc is Menu (the power choices) on the lock
// screen and Cancel in a setup. The password keyboard takes the keys while it
// is up.
func bootLockKey(config *Config, e *sdl.KeyboardEvent) {
	sym := e.Keysym.Sym
	if bootLock.power {
		switch sym {
		case sdl.K_UP, sdl.K_LEFT:
			bootLockPowerMove(-1)
		case sdl.K_DOWN, sdl.K_RIGHT, sdl.K_TAB:
			bootLockPowerMove(1)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			if e.Repeat == 0 {
				bootLockPowerPress(bootLock.powerFocus)
			}
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			bootLock.power = false
			PlayBackSound()
		}
		return
	}
	if bootLock.checking && !bootLockCodeFree() {
		return
	}
	if bootLockTyping() {
		// A held Enter must not submit again and again (main.go's repeat
		// filter runs after this hook); Backspace and the arrows repeat.
		if e.Repeat != 0 && (sym == sdl.K_RETURN || sym == sdl.K_KP_ENTER || sym == sdl.K_ESCAPE) {
			return
		}
		handleVirtualKeyboardInput(e, config)
		return
	}
	if e.Repeat != 0 {
		return
	}
	if bootLockVoiceStepActive() {
		bootLockVoiceKeyPress(config, sym)
		return
	}
	if sym == sdl.K_v && bootLockVoiceEitherUp(config) {
		// V is R2's keyboard twin: say the phrase (no code key uses it).
		bootLockVoicePress(config)
		return
	}
	if bootLock.kind == bootLockKindPassword {
		switch sym {
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			bootLockOpenKeyboard()
			// Space's own text, which SDL sends next, is not the password's
			// first character.
			bootLock.dropText = true
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			bootLockMenu()
		}
		return
	}
	var c byte
	switch sym {
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		bootLockSubmit(config, string(bootLock.entry))
		return
	case sdl.K_BACKSPACE, sdl.K_DELETE:
		bootLockBackspace()
		return
	case sdl.K_ESCAPE:
		bootLockMenu()
		return
	case sdl.K_UP:
		c = 'U'
	case sdl.K_DOWN:
		c = 'D'
	case sdl.K_LEFT:
		c = 'L'
	case sdl.K_RIGHT:
		c = 'R'
	case sdl.K_a:
		c = 'A'
	case sdl.K_b:
		c = 'B'
	case sdl.K_x:
		c = 'X'
	case sdl.K_y:
		c = 'Y'
	case sdl.K_l:
		c = 'l'
	case sdl.K_r:
		c = 'r'
	default:
		return
	}
	bootLockPress(c)
}

// bootLockMenu is Menu (Esc): the power choices on the lock screen, Cancel in
// a setup.
func bootLockMenu() {
	if bootLock.mode == bootLockModeSetup {
		bootLockCancel()
		return
	}
	bootLockOpenPower()
}

// bootLockPress adds one press to the code.
func bootLockPress(c byte) {
	if bootLock.mode == bootLockModeLocked || bootLock.step == bootLockStepCurrent {
		if bootLockWaitLeft() > 0 {
			PlayErrorSound()
			return
		}
	}
	if len(bootLock.entry) >= bootLockCodeMax {
		PlayErrorSound()
		return
	}
	bootLock.entry = append(bootLock.entry, c)
	if bootLock.warn {
		bootLockSay("", false)
	}
	PlayNavSound()
}

// bootLockBackspace is Select: the last press goes; with none, a setup ends.
func bootLockBackspace() {
	if n := len(bootLock.entry); n > 0 {
		bootLock.entry[n-1] = 0
		bootLock.entry = bootLock.entry[:n-1]
		PlayBackSound()
		return
	}
	bootLockCancel()
}

// bootLockOpenKeyboard opens the on-screen keyboard for the password. It
// starts empty and never shows what was typed.
func bootLockOpenKeyboard() {
	if bootLock.mode == bootLockModeOff || (bootLock.checking && !bootLockCodeFree()) || bootLockVoiceStepActive() {
		return
	}
	if (bootLock.mode == bootLockModeLocked || bootLock.step == bootLockStepCurrent) && bootLockWaitLeft() > 0 {
		PlayErrorSound()
		return
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = bootLockVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	bootLockBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// bootLockPasswordSubmit is the keyboard's submit (vkeyboard.go), after it
// closed.
func bootLockPasswordSubmit(config *Config, text string) {
	if bootLock.mode == bootLockModeOff || bootLock.kind != bootLockKindPassword {
		return
	}
	if text == "" {
		bootLock.wantKeyboard = true
		return
	}
	bootLockSubmit(config, text)
}

// The keyboard's words for this target (vkeyboard.go).

func bootLockKeyboardAction() string {
	switch {
	case bootLock.mode == bootLockModeLocked:
		return "Unlock"
	case bootLock.step == bootLockStepConfirm:
		return "Save"
	}
	return "Next"
}

func bootLockKeyboardTitle() string {
	if bootLock.mode == bootLockModeSetup && bootLock.step != bootLockStepCurrent {
		return "NEW PASSWORD"
	}
	return "PASSWORD"
}

func bootLockKeyboardPlaceholder() string {
	switch {
	case bootLock.mode == bootLockModeLocked:
		return "Your startup password"
	case bootLock.step == bootLockStepCurrent:
		return "The current password"
	case bootLock.step == bootLockStepConfirm:
		return "The same password again"
	}
	return "At least " + strconv.Itoa(bootLockPassMin) + " characters"
}

// ── Power choices ────────────────────────────────────────────────────────

type bootLockPowerItem uint8

const (
	bootLockPowerSleep bootLockPowerItem = iota
	bootLockPowerRestart
	bootLockPowerOff
	bootLockPowerExit
	bootLockPowerCancel
)

// bootLockPowerItems are the choices this device has. The handheld does not
// offer Exit: leaving to the stock launcher would walk around the lock.
func bootLockPowerItems() []bootLockPowerItem {
	if devPowerSupported() {
		return []bootLockPowerItem{bootLockPowerSleep, bootLockPowerRestart, bootLockPowerOff, bootLockPowerCancel}
	}
	return []bootLockPowerItem{bootLockPowerExit, bootLockPowerCancel}
}

func bootLockPowerLabel(it bootLockPowerItem) string {
	switch it {
	case bootLockPowerSleep:
		return "Sleep"
	case bootLockPowerRestart:
		return "Restart"
	case bootLockPowerOff:
		return "Power off"
	case bootLockPowerExit:
		return "Exit JukaHub"
	}
	return "Cancel"
}

func bootLockOpenPower() {
	if bootLock.mode != bootLockModeLocked {
		return
	}
	if bootLockTyping() {
		vkCloseKeyboard()
	}
	bootLock.power = true
	bootLock.powerFocus = 0
	PlayActivateSound()
}

func bootLockPowerMove(d int) {
	n := len(bootLockPowerItems())
	next := bootLock.powerFocus + d
	if next < 0 || next >= n {
		return
	}
	bootLock.powerFocus = next
	PlayNavSound()
}

func bootLockPowerPress(i int) {
	items := bootLockPowerItems()
	if i < 0 || i >= len(items) {
		return
	}
	bootLock.power = false
	switch items[i] {
	case bootLockPowerSleep:
		PlayActivateSound()
		devSleepNow("lock screen")
	case bootLockPowerRestart, bootLockPowerOff:
		PlayActivateSound()
		if snapshotVideoState().phase != "idle" {
			StopEmbeddedPlayback()
		}
		if items[i] == bootLockPowerRestart {
			devPowerRequest(devPowerReboot)
		} else {
			devPowerRequest(devPowerPoweroff)
		}
	case bootLockPowerExit:
		PlayActivateSound()
		requestAppExit()
	default:
		PlayBackSound()
		if bootLock.kind == bootLockKindPassword {
			bootLock.wantKeyboard = true
		}
	}
}

// ── Settings rows (Privacy & data) ───────────────────────────────────────

func bootLockKindLabel(kind string) string {
	switch kind {
	case bootLockKindCode:
		return "Button code"
	case bootLockKindPassword:
		return "Password"
	}
	return "Off"
}

func bootLockWhenLabel(when string) string {
	switch when {
	case bootLockWhenStart:
		return "Every time JukaHub opens"
	case bootLockWhenWake:
		return "Every start and after sleep"
	}
	return "At power-on"
}

func bootLockKindOptions(*Config) []setOption {
	return []setOption{
		{Value: "off", Label: bootLockKindLabel("")},
		{Value: bootLockKindCode, Label: bootLockKindLabel(bootLockKindCode)},
		{Value: bootLockKindPassword, Label: bootLockKindLabel(bootLockKindPassword)},
	}
}

func bootLockKindValue(c *Config) string {
	if k := bootLockKindOf(c); k != "" {
		return k
	}
	return "off"
}

// bootLockTypeRow is "Startup lock": picking a type sets a code for it (the
// current one first, when a lock is in force); Off removes it.
func bootLockTypeRow() setRow {
	return setRow{
		ID: "startupLock", Label: "Startup lock", Kind: setKindChoice,
		Detail:  setStatic("Locks the screen, it does not encrypt: the SD card can still be read in a computer"),
		Options: bootLockKindOptions,
		Current: func(c *Config) int {
			switch bootLockKindOf(c) {
			case bootLockKindCode:
				return 1
			case bootLockKindPassword:
				return 2
			}
			return 0
		},
		Value:      func(c *Config) string { return bootLockKindLabel(bootLockKindOf(c)) },
		Sheet:      bootLockKindOptions,
		SheetValue: bootLockKindValue,
		SheetTitle: "Startup lock",
		Choose: func(c *Config, value string) {
			if value == "off" {
				value = ""
			}
			bootLockOpenSetup(c, value)
		},
	}
}

// bootLockWhenRow is "Lock when", shown while a lock is set.
func bootLockWhenRow() setRow {
	whens := []string{bootLockWhenPower, bootLockWhenStart, bootLockWhenWake}
	return setRow{
		ID: "startupLockWhen", Label: "Lock when", Kind: setKindChoice,
		Visible: func(c *Config) bool { return bootLockKindOf(c) != "" },
		Detail: func(c *Config) string {
			switch bootLockWhenFrom(c.Variables.Custom) {
			case bootLockWhenStart:
				return "Every start of JukaHub asks for the code"
			case bootLockWhenWake:
				return "Every start, and waking from sleep, asks for the code"
			}
			return "Only when the handheld starts straight into JukaHub"
		},
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(whens))
			for _, w := range whens {
				out = append(out, setOption{Value: w, Label: bootLockWhenLabel(w)})
			}
			return out
		},
		Current: func(c *Config) int {
			now := bootLockWhenFrom(c.Variables.Custom)
			for i, w := range whens {
				if w == now {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if c.Variables.Custom == nil {
				c.Variables.Custom = map[string]interface{}{}
			}
			c.Variables.Custom[bootLockWhenKey] = value
			saveUserConfigDebounced(userConfigFromVariables(c))
			if value == bootLockWhenWake {
				startWakeWatcher() // firmware sleeps too (bootLockArm)
			}
			setToast("Startup lock: " + bootLockWhenLabel(value))
		},
	}
}

// bootLockChangeRow is "Change the code", shown while a lock is set.
func bootLockChangeRow() setRow {
	return setRow{
		ID: "startupLockChange", Label: "Change the code", Kind: setKindAction,
		Visible: func(c *Config) bool { return bootLockKindOf(c) != "" },
		Detail: func(c *Config) string {
			return "The current " + strings.ToLower(bootLockKindLabel(bootLockKindOf(c))) + " first, then the new one twice"
		},
		Open: func(c *Config) { bootLockOpenSetup(c, bootLockKindOf(c)) },
	}
}
