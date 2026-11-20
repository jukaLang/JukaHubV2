package main

// devcheck.go — the guided Device check: nine hands-on checks of this
// handheld, each with what JukaHub measured itself, the player's verdict and
// an optional note, saved as a Markdown section for the hardware log
// (docs/HARDWARE_VALIDATION.md).
//
// What a build cannot prove on a desk is how it behaves in someone's hands:
// the buttons and sticks, the speaker and the jack, a real suspend and wake,
// the radios, a USB DAC and a game's way back to JukaHub. The check walks
// those in order. Where JukaHub can measure, it does (every button and both
// sticks seen, the output the test tone took and the volume, how long the
// kernel was suspended and how long Wi-Fi and sound took to come back, how
// many Bluetooth devices answered, the link and its signal, the USB card the
// kernel lists, the game's run and its Verified record); the rest is the
// player's word. A step this platform cannot run says so and is skipped.
//
// This file is the model: the steps, a run, the store and the report. The
// page is devcheck_ui.go; the probes and the sleep and game hooks, which all
// work off the main thread, are devcheck_probe.go.
//
// The store is <data>/verified/device-check.json with the last devCheckMaxRuns
// runs, ordered by a counter: a TrimUI often boots before its clock is set, so
// a date is written only through vfDateNow and nothing is ordered by it. An
// unfinished run is taken up again at its step the next time the page opens.
//
// Privacy: nothing here records a Wi-Fi network's name, an IP or MAC address,
// a Bluetooth device's name or address, or a time of day. Bluetooth devices
// are counted, Wi-Fi is "connected" with a signal level, and dates are dates.
// Screenshots of the page and the report are meant to be shared.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	devCheckSceneName = "DeviceCheck"
	devCheckFileName  = "device-check.json"
	devCheckSchema    = 1
	// devCheckMaxRuns is how many runs the store keeps, the oldest dropped.
	devCheckMaxRuns = 10
	// devCheckMaxBytes bounds the store's read.
	devCheckMaxBytes = 1 << 20
	// devCheckNoteMax and devCheckLineMax keep a note and an evidence line
	// to a line or two of the report; devCheckMaxLines bounds a step's lines.
	devCheckNoteMax  = 140
	devCheckLineMax  = 160
	devCheckMaxLines = 8
	// devCheckReportDir is the reports' folder at the top of the SD card
	// (osAppsUserDir): FTP, a card reader and the phone's Files card see it.
	devCheckReportDir = "JukaHub reports"
)

// The steps, by their stored ids.
const (
	dcStepButtons    = "buttons"
	dcStepSpeaker    = "speaker"
	dcStepHeadphones = "headphones"
	dcStepSleep      = "sleep"
	dcStepBluetooth  = "bluetooth"
	dcStepWiFi       = "wifi"
	dcStepUSBDAC     = "usbdac"
	dcStepGame       = "game"
	dcStepUpdate     = "update"
)

// A step's state; "" is not checked yet.
const (
	dcPass    = "pass"
	dcFail    = "fail"
	dcSkip    = "skip"
	dcNotHere = "unsupported"
)

// devCheckStepSpec is one step: what it is called, its glyph, what to do in
// a sentence or two, and the words on its Pass and Fail buttons.
type devCheckStepSpec struct {
	id    string
	title string
	icon  string
	todo  string
	pass  string
	fail  string
}

// devCheckSteps are the steps in the order they are walked.
var devCheckSteps = [...]devCheckStepSpec{
	{id: dcStepButtons, title: "Buttons and sticks", icon: "gamepad",
		todo: "Open the controller test, press every button once and push both sticks all the way round. Then come back here.",
		pass: "All worked", fail: "Something didn't"},
	{id: dcStepSpeaker, title: "Speaker", icon: "volume",
		todo: "Unplug any headphones, then play the test sound at your usual volume.",
		pass: "I heard it", fail: "Silent or broken"},
	{id: dcStepHeadphones, title: "Headphones", icon: "headphones",
		todo: "Plug headphones into the jack. Once JukaHub sees them, play the test sound.",
		pass: "I heard it", fail: "Not in the headphones"},
	{id: dcStepSleep, title: "Sleep and wake", icon: "moon",
		todo: "Stop any music, press the power key, wait ten seconds, then press it again. JukaHub measures the sleep and what comes back after it.",
		pass: "It woke up fine", fail: "Something was wrong"},
	{id: dcStepBluetooth, title: "Bluetooth", icon: "bluetooth",
		todo: "Search for nearby devices. Bluetooth is switched on for it, and off again after if it was off. Pairing a pad or a headset is up to you, on the Bluetooth page.",
		pass: "Works", fail: "Doesn't work"},
	{id: dcStepWiFi, title: "Wi-Fi", icon: "wifi",
		todo: "Connect to Wi-Fi if you haven't, then check the link and the internet.",
		pass: "Works", fail: "Doesn't work"},
	{id: dcStepUSBDAC, title: "USB DAC", icon: "plug",
		todo: "Optional: plug a USB audio adapter or DAC into the USB-C port, then play the test sound. Skip this if you have none.",
		pass: "I heard it", fail: "Not through the DAC"},
	{id: dcStepGame, title: "Game launch and return", icon: "game",
		todo: "Play the game below for a minute, then quit it the usual way. JukaHub watches it start and come back.",
		pass: "Came back cleanly", fail: "It didn't"},
	{id: dcStepUpdate, title: "Update self-check", icon: "update",
		todo: "Which JukaHub release this is, and whether its first start after the update passed its own check.",
		pass: "Looks right", fail: "Looks wrong"},
}

// devCheckStepIndex is the position of step id, or -1.
func devCheckStepIndex(id string) int {
	for i, s := range devCheckSteps {
		if s.id == id {
			return i
		}
	}
	return -1
}

// devCheckResult is one step's outcome in a run. Auto is set while JukaHub
// decided the state from its own measurement; the player's answer clears it.
type devCheckResult struct {
	State    string   `json:"state,omitempty"`
	Auto     bool     `json:"auto,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// devCheckRun is one walk through the steps on one device. ID is the store's
// counter; Step is where the walk stood, so it resumes there.
type devCheckRun struct {
	ID       int64                     `json:"id"`
	Model    string                    `json:"model"`
	Firmware string                    `json:"firmware,omitempty"`
	Release  string                    `json:"release,omitempty"`
	Date     string                    `json:"date,omitempty"`
	Step     int                       `json:"step"`
	Finished bool                      `json:"finished,omitempty"`
	Report   string                    `json:"report,omitempty"`
	Steps    map[string]devCheckResult `json:"steps,omitempty"`
}

// devCheckFile is device-check.json. Runs are oldest first.
type devCheckFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Seq           int64         `json:"seq,omitempty"`
	Runs          []devCheckRun `json:"runs"`
}

// devCheckFacts are this platform's answers the page needs, read once off the
// main thread when the page first opens.
type devCheckFacts struct {
	trimui    bool
	bluetooth bool
	usbDAC    bool
	firmware  string
	release   string
	update    devCheckUpdateInfo
}

// devCheckUpdateInfo is what is known about this release's first start after
// an update: whether a start check was recorded and whether it passed, with
// one plain line (no times of day).
type devCheckUpdateInfo struct {
	Known  bool
	Passed bool
	Detail string
}

// devCheckUpdateCheck reports the post-update start check. The self-repair
// after updates may set it from its init() to say more (a failed start that
// was rolled back); nil reads the record its start check leaves
// (devCheckReadStartFact).
var devCheckUpdateCheck func() devCheckUpdateInfo

// devCheckStartFactName is the record the update's start check
// (startcheck.go) writes into the Patch state's versions/ folder once an
// updated build started fine on this device: {"tag", "model", "date"}.
const devCheckStartFactName = "started.json"

// devCheck is the store and the run on screen. Main thread only.
var devCheck struct {
	loaded  bool
	loading bool
	file    devCheckFile
	// cur is the run on screen, an index into file.Runs; -1 before loading.
	cur   int
	facts devCheckFacts
	// saveGen orders the writes the goroutines make.
	saveGen uint64
	// reporting is up while a report is written, so a second press does not
	// race the first for the file name.
	reporting bool
}

// devCheckWrite serializes the store's writes; written is the newest
// generation on the card, so an older write that lost the race is dropped.
var devCheckWrite struct {
	mu      sync.Mutex
	written uint64
}

func init() { devCheck.cur = -1 }

// devCheckPath is <data>/verified/device-check.json.
func devCheckPath() string { return filepath.Join(vfDir(), devCheckFileName) }

// ── Loading ───────────────────────────────────────────────────────────────

// devCheckEnsureLoaded reads the store and the platform facts on a goroutine
// the first time the page opens; the page shows "Getting ready…" meanwhile.
// Main thread.
func devCheckEnsureLoaded() {
	if devCheck.loaded || devCheck.loading {
		return
	}
	devCheck.loading = true
	go func() {
		file := devCheckRead(devCheckPath())
		facts := devCheckReadFacts()
		tbxPost(func() {
			devCheck.file = file
			devCheck.facts = facts
			devCheck.loaded = true
			devCheck.loading = false
			// The newest run of this model; a card moved from another model
			// starts its own.
			model := string(DetectDevice())
			for i := len(devCheck.file.Runs) - 1; i >= 0; i-- {
				if devCheck.file.Runs[i].Model == model {
					devCheck.cur = i
					break
				}
			}
			if devCheck.cur < 0 {
				devCheckNewRun()
			}
			devCheckScreenAfterLoad()
		})
	}()
}

// devCheckRead reads and cleans the store; anything unreadable is an empty
// store (readJSONWithBackup logs it and tries the .bak first).
func devCheckRead(path string) devCheckFile {
	var f devCheckFile
	if st, err := os.Stat(path); err == nil && st.Size() > devCheckMaxBytes {
		logWarn("devcheck", "ignoring an oversized %s (%d bytes)", devCheckFileName, st.Size())
		return devCheckFile{SchemaVersion: devCheckSchema}
	}
	if _, err := readJSONWithBackup(path, &f); err != nil {
		f = devCheckFile{}
	}
	devCheckSanitize(&f)
	return f
}

// devCheckSanitize makes a read store safe to show and to write back: known
// steps and states only, texts clipped, the newest devCheckMaxRuns runs in
// counter order, and a counter past every run.
func devCheckSanitize(f *devCheckFile) {
	f.SchemaVersion = devCheckSchema
	runs := f.Runs[:0]
	for _, r := range f.Runs {
		if r.ID <= 0 {
			continue
		}
		r.Model = devCheckClip(r.Model, 64)
		r.Firmware = devCheckClip(r.Firmware, 64)
		r.Release = devCheckClip(r.Release, 64)
		r.Date = devCheckCleanDate(r.Date)
		// A file name in the reports folder, nothing else: it is written to.
		r.Report = devCheckClip(filepath.Base(strings.ReplaceAll(r.Report, "\\", "/")), 120)
		if !strings.HasSuffix(r.Report, ".md") || strings.HasPrefix(r.Report, ".") {
			r.Report = ""
		}
		if r.Step < 0 || r.Step >= len(devCheckSteps) {
			r.Step = 0
		}
		steps := make(map[string]devCheckResult, len(devCheckSteps))
		for id, res := range r.Steps {
			if devCheckStepIndex(id) < 0 {
				continue
			}
			switch res.State {
			case "", dcPass, dcFail, dcSkip, dcNotHere:
			default:
				res.State = ""
			}
			res.Evidence = devCheckCleanLines(res.Evidence)
			res.Note = devCheckClip(devCheckScrub(res.Note), devCheckNoteMax)
			steps[id] = res
		}
		r.Steps = steps
		runs = append(runs, r)
		if r.ID > f.Seq {
			f.Seq = r.ID
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	// One run per number: a hand-edited file may repeat one.
	uniq := runs[:0]
	for _, r := range runs {
		if len(uniq) == 0 || r.ID != uniq[len(uniq)-1].ID {
			uniq = append(uniq, r)
		}
	}
	runs = uniq
	if len(runs) > devCheckMaxRuns {
		runs = runs[len(runs)-devCheckMaxRuns:]
	}
	f.Runs = runs
	if f.Seq < 0 {
		f.Seq = 0
	}
}

// devCheckCleanDate keeps a stored date only in the YYYY-MM-DD shape.
func devCheckCleanDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return ""
	}
	for i, c := range s {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return ""
		}
	}
	return s
}

// devCheckAddrRE finds what looks like a MAC or an IPv4 address. Evidence
// lines quote error texts from elsewhere and notes are typed freely; the
// page and the report never carry an address (devCheckScrub).
var devCheckAddrRE = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?:[:-][0-9a-f]{2}){5}\b|\b\d{1,3}(?:\.\d{1,3}){3}\b|\b[0-9a-f]{1,4}(?::[0-9a-f]{0,4}){2,7}\b`)

// devCheckTimeRE finds a time of day ("21:42", "9:05:30 pm"), which a typed
// note may carry. The character before it is kept (${1}), so the "T21:42:07Z"
// of a timestamp counts too. It runs after devCheckAddrRE, which takes an
// IPv6 address whole (and "21:42:07" as an address: scrubbed all the same).
var devCheckTimeRE = regexp.MustCompile(`(?i)(^|[^0-9:])(?:[01]?\d|2[0-3]):[0-5]\d(?::[0-5]\d)?(?:\s*[ap]\.?m\.?|z)?\b`)

// devCheckScrub replaces anything address-like or clock-like in s.
func devCheckScrub(s string) string {
	s = devCheckAddrRE.ReplaceAllString(s, "[address]")
	return devCheckTimeRE.ReplaceAllString(s, "${1}[time]")
}

// devCheckCleanLines clips evidence lines to one line each, at most
// devCheckMaxLines of them, with addresses scrubbed.
func devCheckCleanLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l = devCheckClip(devCheckScrub(l), devCheckLineMax); l != "" {
			out = append(out, l)
		}
		if len(out) == devCheckMaxLines {
			break
		}
	}
	return out
}

// devCheckClip is s on one line, trimmed and cut to max runes.
func devCheckClip(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// devCheckReadFacts asks the platform what the steps need. Off the main
// thread: it looks for programs, reads the firmware's name and the release.
func devCheckReadFacts() devCheckFacts {
	f := devCheckFacts{
		trimui:    IsTrimuiDevice(),
		bluetooth: btDevicesAvailable(),
		usbDAC:    usbDACApplies(),
		firmware:  devCheckClip(gameLabFirmware(), 64),
		release:   devCheckClip(vfVersionNow(), 64),
	}
	f.update = devCheckReadStartFact(f.release)
	if fn := devCheckUpdateCheck; fn != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logWarn("devcheck", "the update start check could not be read: %v", r)
				}
			}()
			f.update = fn()
			f.update.Detail = devCheckClip(f.update.Detail, devCheckLineMax)
		}()
	}
	return f
}

// devCheckReadStartFact reads the start check's record: whether release is
// the build that last started fine after an update here. Off the main thread.
func devCheckReadStartFact(release string) devCheckUpdateInfo {
	base, err := PatchStateDir()
	if err != nil || base == "" {
		return devCheckUpdateInfo{}
	}
	path := filepath.Join(base, "versions", devCheckStartFactName)
	if st, err := os.Stat(path); err != nil || st.Size() > 64<<10 {
		return devCheckUpdateInfo{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return devCheckUpdateInfo{}
	}
	var fact struct {
		Tag   string `json:"tag"`
		Model string `json:"model"`
		Date  string `json:"date"`
	}
	if json.Unmarshal(data, &fact) != nil || strings.TrimSpace(fact.Tag) == "" {
		return devCheckUpdateInfo{}
	}
	tag := devCheckClip(fact.Tag, 64)
	switch {
	case tag != strings.TrimSpace(release):
		return devCheckUpdateInfo{Detail: "The last update that passed its start check here was the " + vfVersionLabel(tag)}
	case fact.Model != "" && fact.Model != string(DetectDevice()):
		return devCheckUpdateInfo{Detail: "Its start check passed on another model"}
	}
	detail := "This release started fine after its update"
	if date := devCheckCleanDate(fact.Date); date != "" {
		detail += " (" + date + ")"
	}
	return devCheckUpdateInfo{Known: true, Passed: true, Detail: detail}
}

// ── Runs ──────────────────────────────────────────────────────────────────

// devCheckRunNow is the run on screen, or nil. Main thread.
func devCheckRunNow() *devCheckRun {
	if !devCheck.loaded || devCheck.cur < 0 || devCheck.cur >= len(devCheck.file.Runs) {
		return nil
	}
	return &devCheck.file.Runs[devCheck.cur]
}

// devCheckSupported reports whether step id can run on this platform, and
// why not.
func devCheckSupported(id string, f devCheckFacts) (bool, string) {
	switch id {
	case dcStepHeadphones:
		if !f.trimui {
			return false, "JukaHub reads the headphone jack on the handhelds only"
		}
	case dcStepSleep:
		if !f.trimui {
			return false, "The power key's sleep is the handheld's; a computer sleeps on its own"
		}
	case dcStepBluetooth:
		if !f.bluetooth {
			return false, "There are no Bluetooth tools JukaHub can use here"
		}
	case dcStepUSBDAC:
		if !f.usbDAC {
			return false, "JukaHub routes sound to a USB DAC on the handhelds only"
		}
	}
	return true, ""
}

// devCheckNewRun starts a run on this device and puts it on screen. The steps
// this platform cannot run are marked so at once, and the update step gets
// its facts. Main thread.
func devCheckNewRun() {
	f := &devCheck.file
	f.Seq++
	run := devCheckRun{
		ID:       f.Seq,
		Model:    string(DetectDevice()),
		Firmware: devCheck.facts.firmware,
		Release:  devCheck.facts.release,
		Date:     vfDateNow(),
		Steps:    make(map[string]devCheckResult, len(devCheckSteps)),
	}
	first := -1
	for i, s := range devCheckSteps {
		if ok, why := devCheckSupported(s.id, devCheck.facts); !ok {
			run.Steps[s.id] = devCheckResult{State: dcNotHere, Auto: true, Evidence: []string{why}}
			continue
		}
		if first < 0 {
			first = i
		}
	}
	run.Steps[dcStepUpdate] = devCheckUpdateResult(devCheck.facts)
	run.Step = max(first, 0)
	f.Runs = append(f.Runs, run)
	if len(f.Runs) > devCheckMaxRuns {
		f.Runs = append([]devCheckRun(nil), f.Runs[len(f.Runs)-devCheckMaxRuns:]...)
	}
	devCheck.cur = len(f.Runs) - 1
	devCheckSave()
}

// devCheckUpdateResult is the update step's evidence: the release and, when
// this build records one, the post-update start check's outcome.
func devCheckUpdateResult(f devCheckFacts) devCheckResult {
	lines := []string{"Release: " + vfVersionLabel(setOr(f.release, "unknown"))}
	switch {
	case f.update.Known && f.update.Passed:
		lines = append(lines, "Post-update start check: passed")
	case f.update.Known:
		lines = append(lines, "Post-update start check: failed")
	default:
		lines = append(lines, "No post-update start check is recorded for this release")
	}
	if f.update.Detail != "" {
		lines = append(lines, f.update.Detail)
	}
	res := devCheckResult{Auto: true, Evidence: lines}
	switch {
	case f.update.Known && f.update.Passed:
		res.State = dcPass
	case f.update.Known:
		res.State = dcFail
	default:
		res.State = dcSkip
	}
	return res
}

// devCheckResultFor is step id's result in the run on screen.
func devCheckResultFor(id string) devCheckResult {
	if run := devCheckRunNow(); run != nil {
		return run.Steps[id]
	}
	return devCheckResult{}
}

// devCheckSetEvidence stores what a probe measured for step id of run runID
// (0 is the run on screen). auto, when not "", is the state the measurement
// decides; it never replaces the player's own answer. Main thread.
func devCheckSetEvidence(runID int64, id string, lines []string, auto string) {
	run := devCheckRunNow()
	if run == nil || (runID != 0 && run.ID != runID) || devCheckStepIndex(id) < 0 {
		return
	}
	res := run.Steps[id]
	if res.State == dcNotHere {
		return
	}
	res.Evidence = devCheckCleanLines(lines)
	switch {
	case auto != "" && (res.State == "" || res.Auto):
		res.State, res.Auto = auto, true
	case auto == "" && res.Auto && id != dcStepUpdate:
		// A newer measurement that decides nothing takes back the older
		// one's verdict: the evidence on screen and the state must agree.
		res.State, res.Auto = "", false
		run.Finished = false
	}
	run.Steps[id] = res
	devCheckSave()
}

// devCheckAnswer records the player's answer for step i and moves on to the
// next step without one. It reports whether every step is answered now. Main
// thread.
func devCheckAnswer(i int, state string) bool {
	run := devCheckRunNow()
	if run == nil || i < 0 || i >= len(devCheckSteps) {
		return false
	}
	id := devCheckSteps[i].id
	res := run.Steps[id]
	if res.State != dcNotHere {
		res.State, res.Auto = state, false
		run.Steps[id] = res
	}
	next := devCheckNextOpen(run, i)
	if next < 0 {
		run.Finished = true
	} else {
		run.Step = next
	}
	devCheckSave()
	return next < 0
}

// devCheckNextOpen is the first step after i (wrapping) with no state, or -1.
func devCheckNextOpen(run *devCheckRun, i int) int {
	for k := 1; k <= len(devCheckSteps); k++ {
		j := (i + k) % len(devCheckSteps)
		if run.Steps[devCheckSteps[j].id].State == "" {
			return j
		}
	}
	return -1
}

// devCheckSetNote stores the player's note for step i. Main thread.
func devCheckSetNote(i int, note string) {
	run := devCheckRunNow()
	if run == nil || i < 0 || i >= len(devCheckSteps) {
		return
	}
	id := devCheckSteps[i].id
	res := run.Steps[id]
	res.Note = devCheckClip(devCheckScrub(note), devCheckNoteMax)
	run.Steps[id] = res
	devCheckSave()
}

// devCheckSetStep remembers the step on screen, so the run resumes there.
func devCheckSetStep(i int) {
	run := devCheckRunNow()
	if run == nil || i < 0 || i >= len(devCheckSteps) || run.Step == i {
		return
	}
	run.Step = i
	devCheckSave()
}

// devCheckCounts tallies a run's states.
func devCheckCounts(run *devCheckRun) (pass, fail, skip, notHere, open int) {
	for _, s := range devCheckSteps {
		switch run.Steps[s.id].State {
		case dcPass:
			pass++
		case dcFail:
			fail++
		case dcSkip:
			skip++
		case dcNotHere:
			notHere++
		default:
			open++
		}
	}
	return
}

// devCheckSummary is a run's tally in a line: "6 passed · 1 failed · 2 skipped".
func devCheckSummary(run *devCheckRun) string {
	pass, fail, skip, notHere, open := devCheckCounts(run)
	var parts []string
	add := func(n int, word string) {
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+word)
		}
	}
	add(pass, "passed")
	add(fail, "failed")
	add(skip, "skipped")
	add(notHere, "not on this device")
	add(open, "to do")
	return strings.Join(parts, " · ")
}

// ── Saving ────────────────────────────────────────────────────────────────

// devCheckSave writes the store on a goroutine. Main thread: the file is
// encoded here, so the goroutine never reads screen state.
func devCheckSave() {
	data, err := json.MarshalIndent(devCheck.file, "", " ")
	if err != nil {
		logWarn("devcheck", "could not encode the device check: %v", err)
		return
	}
	devCheck.saveGen++
	gen := devCheck.saveGen
	path := devCheckPath()
	go func() {
		devCheckWrite.mu.Lock()
		defer devCheckWrite.mu.Unlock()
		if gen <= devCheckWrite.written {
			return
		}
		devCheckWrite.written = gen
		if err := AtomicWrite(path, data, 0o644); err != nil {
			logWarn("devcheck", "could not save the device check: %v", err)
		}
	}()
}

// ── Report ────────────────────────────────────────────────────────────────

// devCheckStateWord is a result in the report's Result column.
func devCheckStateWord(res devCheckResult) string {
	word := "Not checked"
	switch res.State {
	case dcPass:
		word = "Pass"
	case dcFail:
		word = "Fail"
	case dcSkip:
		word = "Skipped"
	case dcNotHere:
		return "Not on this device"
	}
	if res.Auto && res.State != "" {
		word += " (measured)"
	}
	return word
}

// devCheckMDCell makes text safe for one Markdown table cell.
func devCheckMDCell(s string) string {
	s = strings.ReplaceAll(devCheckClip(s, 4*devCheckLineMax), "|", "\\|")
	if s == "" {
		return "—"
	}
	return s
}

// devCheckModelName is the run's model in words: "TrimUI Smart Pro".
func devCheckModelName(model string) string {
	d := DeviceModel(model)
	if d.IsTrimui() {
		name := deviceDisplayName(d)
		if i := strings.Index(name, " ("); i > 0 {
			name = name[:i]
		}
		if name != "" && name != "Unknown" {
			return name
		}
	}
	return devCheckUpperFirst(vfModelShort(model))
}

// devCheckFirmwareLabel is the run's system: "TrimUI firmware", "NextUI
// firmware" on a handheld, the operating system elsewhere; "" unknown.
func devCheckFirmwareLabel(run *devCheckRun) string {
	if run.Firmware == "" || !DeviceModel(run.Model).IsTrimui() {
		return run.Firmware
	}
	return run.Firmware + " firmware"
}

// devCheckUpperFirst capitalises the first letter ("this PC" → "This PC").
func devCheckUpperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// devCheckReport is run as a section of docs/HARDWARE_VALIDATION.md: the
// heading, two sentences, the table and the notes. Pure.
func devCheckReport(run devCheckRun) string {
	var b strings.Builder
	date := setOr(run.Date, "Undated")
	fmt.Fprintf(&b, "## %s — %s guided device check\n\n", date, devCheckUpperFirst(vfModelShort(run.Model)))
	pass, fail, skip, notHere, open := devCheckCounts(&run)
	on := devCheckModelName(run.Model)
	if fw := devCheckFirmwareLabel(&run); fw != "" {
		on += " (" + fw + ")"
	}
	fmt.Fprintf(&b, "JukaHub's guided device check ran on a %s with the %s. ", on, vfVersionLabel(setOr(run.Release, "unknown release")))
	tally := []string{fmt.Sprintf("%d passed", pass), fmt.Sprintf("%d failed", fail)}
	if skip > 0 {
		tally = append(tally, fmt.Sprintf("%d skipped", skip))
	}
	if notHere > 0 {
		tally = append(tally, fmt.Sprintf("%d cannot run on this model", notHere))
	}
	if open > 0 {
		tally = append(tally, fmt.Sprintf("%d not checked", open))
	}
	last := tally[len(tally)-1]
	head := strings.Join(tally[:len(tally)-1], ", ")
	fmt.Fprintf(&b, "Of the %d checks, %s and %s. ", len(devCheckSteps), head, last)
	b.WriteString("Evidence is what JukaHub measured itself; \"measured\" marks a result it decided from that alone, " +
		"the others are the tester's verdict. Quitting JukaHub back to one MainUI cannot be seen from inside JukaHub " +
		"and is not part of this check.\n\n")
	b.WriteString("| Check | Result | Evidence |\n|---|---|---|\n")
	for _, s := range devCheckSteps {
		res := run.Steps[s.id]
		fmt.Fprintf(&b, "| %s | %s | %s |\n", s.title, devCheckStateWord(res), devCheckMDCell(strings.Join(res.Evidence, "; ")))
	}
	var notes []string
	for _, s := range devCheckSteps {
		if note := devCheckClip(run.Steps[s.id].Note, devCheckNoteMax); note != "" {
			notes = append(notes, "- "+s.title+": "+note)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\nNotes from the run:\n\n")
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// devCheckReportName is the report's file name: device-check-smart-pro-
// 2026-11-20.md, or with the run's number when there is no date.
func devCheckReportName(run devCheckRun) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, vfModelShort(run.Model))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "device"
	}
	when := run.Date
	if when == "" {
		when = "run-" + strconv.FormatInt(run.ID, 10)
	}
	return "device-check-" + slug + "-" + when + ".md"
}

// devCheckWriteReport writes text into dir: under the run's earlier report
// name when it has one, else under name or, when that is another run's,
// name-2, name-3… It returns the file name used. Off the main thread.
func devCheckWriteReport(dir, name, earlier, text string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	use := earlier
	if use == "" {
		stem := strings.TrimSuffix(name, ".md")
		use = name
		for n := 2; n < 100; n++ {
			if _, err := os.Stat(filepath.Join(dir, use)); os.IsNotExist(err) {
				break
			}
			use = stem + "-" + strconv.Itoa(n) + ".md"
		}
	}
	path := filepath.Join(dir, use)
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return use, nil
}

// devCheckSaveReport writes the run on screen as a report into the SD card's
// "JukaHub reports" folder and toasts its name. Main thread; the write is a
// goroutine's.
func devCheckSaveReport() {
	run := devCheckRunNow()
	if run == nil || devCheck.reporting {
		return
	}
	devCheck.reporting = true
	snap := *run
	text := devCheckReport(snap)
	dir := osAppsUserDir(devCheckReportDir)
	name := devCheckReportName(snap)
	earlier := snap.Report
	id := snap.ID
	ShowToast("Saving the report…", ToastKindInfo)
	go func() {
		used, err := devCheckWriteReport(dir, name, earlier, text)
		tbxPost(func() {
			devCheck.reporting = false
			if err != nil {
				logWarn("devcheck", "could not save the report: %v", err)
				ShowToast("Could not save the report: "+firstLine(err.Error()), ToastKindError)
				return
			}
			logInfo("devcheck", "report saved as %s", used)
			ShowToast("Report saved: "+devCheckReportDir+"/"+used, ToastKindSuccess)
			for i := range devCheck.file.Runs {
				if devCheck.file.Runs[i].ID == id && devCheck.file.Runs[i].Report != used {
					devCheck.file.Runs[i].Report = used
					devCheckSave()
				}
			}
		})
	}()
}
