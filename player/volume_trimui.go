package main

// volume_trimui.go — the TrimUI Smart Pro / Brick (tg5040) audio mixer.
//
// The generic ALSA path tries Master, then PCM. The tg5040
// codec exposes neither, so on the Smart Pro and the Brick every volume write
// failed. What the codec does have, as MinUI's tg5040 port found out the
// hard way:
//
//   - "digital volume" is the control to use: setting the level with
//     "DAC Volume" distorts at low levels. It is an ATTENUATION: raw 0 is the
//     loudest and the top of its range (63) the quietest, so a percentage
//     cannot be written to it as "NN%".
//   - its quietest step is not silent. Muting also takes "DAC Volume" to 0,
//     and un-muting has to put that back.
//   - the firmware's keymon owns the hardware volume keys and keeps its level
//     (0..20) as "vol" in /mnt/UDISK/system.json. Writing it after a change
//     the user makes here is how the next key press continues from the level
//     JukaHub set instead of jumping back to the old one.
//
// The level shown is read from the codec itself: 0 while the DAC is at 0
// (silent, whoever muted it), otherwise the attenuation through the inverse
// curve. system.json is only the fallback for when amixer cannot answer: it
// can be stale (a launcher that is not the stock one leaves it behind), and
// with headphones in the firmware keeps a separate "hpvol" there, while the
// codec always carries whichever level is actually playing.
//
// A stock Smart Pro confirms the control names and system.json's "vol" key.
// Its keymon maps steps 1..20 to digital attenuation 45..0, and step 0 to
// 63; steps 1 and 4 were checked against the codec live. Listening quality
// still needs a hands-on test. Everything is probed first.
// A codec that is not the one described above — the Smart Pro
// S's A523 on its 5.15 BSP has its own — gets the first gain control it
// really lists (trimuiPickGainControl: Master, PCM, the sunxi "DAC Volume"
// held to its 0 dB point, then the output stages by name), and only a codec
// with none of them the generic Master/PCM path.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// trimuiDigitalControl and trimuiDACControl are the codec's simple mixer
	// controls, as `amixer scontrols` names them.
	trimuiDigitalControl = "digital volume"
	trimuiDACControl     = "DAC Volume"

	// trimuiDigitalMaxDefault is the attenuation range's top when the
	// control's "Limits:" line cannot be read.
	trimuiDigitalMaxDefault = 63
	// trimuiDACUnmuteDefault is the DAC level an un-mute restores when the
	// level before the mute is unknown: 160 is the control's 0 dB point (it
	// accepts values up to 255, which only clip).
	trimuiDACUnmuteDefault = 160

	// trimuiCurveScale shapes the percentage-to-attenuation curve:
	// raw = -trimuiCurveScale * log10(pct/100). It reproduces the two points
	// measured on a tg5040 (20% → 30, 50% → 13). The stock Smart Pro
	// curve below is used instead on a Smart Pro without NextUI.
	trimuiCurveScale = 43.0
	// Stock Smart Pro keymon uses floor(48 * (20 - step) / 20) for audible
	// steps. This differs sharply from the NextUI-derived logarithmic curve.
	trimuiStockAttenuationSpan = 48

	// trimuiSystemSteps is the firmware's own volume scale: "vol" runs 0..20.
	trimuiSystemSteps = 20
	// trimuiSystemSyncDelay holds the system.json write back until the level
	// has stopped moving, so a held D-pad writes the card once, not per step.
	trimuiSystemSyncDelay = 600 * time.Millisecond
	// trimuiSystemSyncRetries is how many times a write the firmware raced
	// (the file changed while it was being updated) is tried again.
	trimuiSystemSyncRetries = 2

	// trimuiProbeRetry and trimuiProbeAttempts cover a probe that found
	// neither control: the codec may still be registering when the first
	// level is asked for, so the question is asked again a few times before
	// the answer "not this codec" is kept for the session.
	trimuiProbeRetry    = 30 * time.Second
	trimuiProbeAttempts = 5
)

// trimuiSystemJSONPath is the firmware's settings file. A variable so a test
// can point it at a temporary file.
var trimuiSystemJSONPath = "/mnt/UDISK/system.json"

// trimuiMuteMarkerPath records a mute JukaHub made, with the DAC level it
// replaced, so a restart (an update, a crash, a relaunch) still knows the mute
// is its own. Without it the next run read the DAC at 0 as the firmware's
// mute, which a poll never lifts, and the hardware keys stayed silent while
// the firmware's level rose. It lives in the temp dir like the alarm's marker:
// a reboot resets the codec anyway. A variable so tests can point it at a
// temp dir.
var trimuiMuteMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-volume-mute")
}

// USB audio can become ALSA card 0 when it is present at boot. Select the
// on-board codec by its ALSA ID so volume keys never change the USB card by
// accident. The ID is stable even if its card number changes.
func trimuiCodecCardIDFrom(cards string) string {
	preferred, fallback := "", ""
	for _, line := range strings.Split(cards, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] < '0' || line[0] > '9' {
			continue
		}
		open := strings.IndexByte(line, '[')
		close := strings.IndexByte(line, ']')
		if open < 0 || close <= open {
			continue
		}
		id := strings.TrimSpace(line[open+1 : close])
		lower := strings.ToLower(id)
		label := strings.ToLower(line)
		if id == "" || strings.Contains(label, "usb") || strings.Contains(label, "hdmi") ||
			strings.Contains(label, "bluetooth") || strings.Contains(label, "dummy") || strings.Contains(label, "loopback") {
			continue
		}
		if lower == "audiocodec" {
			return id
		}
		if preferred == "" && (strings.HasPrefix(lower, "sunxi") || strings.HasPrefix(lower, "sndcodec")) {
			preferred = id
		}
		if fallback == "" {
			fallback = id
		}
	}
	if preferred != "" {
		return preferred
	}
	return fallback
}

func trimuiCodecCardID() string {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return ""
	}
	cards, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return ""
	}
	return trimuiCodecCardIDFrom(string(cards))
}

func trimuiAmixer(args ...string) (string, error) {
	if runtime.GOOS == "linux" && IsTrimuiDevice() {
		card := trimuiCodecCardID()
		if card == "" {
			return "", errors.New("built-in ALSA codec not found")
		}
		args = append([]string{"-c", card}, args...)
	}
	return systemCommandRunner("amixer", args...)
}

// The stock Smart Pro's keymon uses the linear attenuation below. NextUI
// ships a different keymon curve, so its existing logarithmic mapping remains.
func trimuiStockVolumeCurve(max int) bool {
	return runtime.GOOS == "linux" && DetectDevice() == DeviceTrimuiSmartPro &&
		!nextuiActive() && max == trimuiDigitalMaxDefault
}

func trimuiStockAttenuationFor(pct, max int) int {
	if pct <= 0 {
		return max
	}
	step := (pct*trimuiSystemSteps + 50) / 100
	if step > trimuiSystemSteps {
		step = trimuiSystemSteps
	}
	return trimuiStockAttenuationSpan * (trimuiSystemSteps - step) / trimuiSystemSteps
}

func trimuiStockPercentForAttenuation(raw, max int) int {
	if raw >= max {
		return 0
	}
	bestStep, bestDistance := 1, max+1
	for step := 1; step <= trimuiSystemSteps; step++ {
		want := trimuiStockAttenuationSpan * (trimuiSystemSteps - step) / trimuiSystemSteps
		distance := want - raw
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			bestStep, bestDistance = step, distance
		}
	}
	return bestStep * 100 / trimuiSystemSteps
}

// The generic Master/PCM fallback must use the same built-in card as the
// tg5040 controls. Otherwise a USB DAC that became card 0 could be changed.
func trimuiGenericSet(level string) error {
	if _, err := trimuiAmixer("-q", "sset", "Master", level); err == nil {
		return nil
	}
	if _, err := trimuiAmixer("-q", "sset", "PCM", level); err != nil {
		return fmt.Errorf("amixer: %w", err)
	}
	return nil
}

func trimuiGenericVolume() (int, bool) {
	for _, control := range []string{"Master", "PCM"} {
		if out, err := trimuiAmixer("get", control); err == nil {
			if pct, ok := parseFirstPercent(out); ok {
				return pct, true
			}
		}
	}
	return 0, false
}

// trimuiWriteMuteMarker records JukaHub's mute and the DAC level to restore.
func trimuiWriteMuteMarker(savedDAC int) {
	if err := os.WriteFile(trimuiMuteMarkerPath(), []byte(strconv.Itoa(savedDAC)), 0o600); err != nil {
		logDebug("volume", "could not record the mute: %v", err)
	}
}

// trimuiClearMuteMarker forgets the mute record once the DAC is back.
func trimuiClearMuteMarker() {
	if err := os.Remove(trimuiMuteMarkerPath()); err != nil && !os.IsNotExist(err) {
		logDebug("volume", "could not clear the mute record: %v", err)
	}
}

// trimuiReadMuteMarker returns the DAC level a previous run's mute replaced.
// ok is false without a record; a record that does not parse still says the
// mute was JukaHub's (level 0: the default is restored).
func trimuiReadMuteMarker() (int, bool) {
	data, err := os.ReadFile(trimuiMuteMarkerPath())
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0, true
	}
	return n, true
}

// trimuiMixerInfo is what the codec exposes, probed once.
type trimuiMixerInfo struct {
	digital    bool // "digital volume" exists
	digitalMax int  // its attenuation range's top (63 on the tg5040)
	dac        bool // "DAC Volume" exists, so a mute can be made silent
	dacMax     int  // its range's top; 0 when unknown
	// digitalName and dacName are the controls exactly as `amixer scontrols`
	// spelled them. They are matched without regard to case, but ALSA looks a
	// simple control up case-sensitively, so a codec that says "Digital
	// Volume" must be written as "Digital Volume".
	digitalName string
	dacName     string
	// gain is the volume control on a codec without "digital volume" (the
	// Smart Pro S's A523, any other sunxi BSP codec): a plain gain, higher is
	// louder, picked from what the codec lists (trimuiPickGainControl).
	// gainCeil is the raw value that counts as 100% when the top of the range
	// is a boost that only clips (the sunxi "DAC Volume": 0 dB at 160 of
	// 255); 0 means the control is written as an amixer -M percentage.
	gain     string
	gainMax  int
	gainCeil int
}

// trimuiMixer is the mixer's state. Every read and write holds mu: the volume
// worker, the alarm (alarmvol.go), the boot restore and the level poll can all
// reach the mixer, and an un-mute must not interleave with a mute.
var trimuiMixer struct {
	mu       sync.Mutex
	probedAt time.Time
	probed   bool
	attempts int // probes that got an answer from amixer
	info     trimuiMixerInfo
	// savedDAC is the DAC level before it was muted; 0 when unknown.
	savedDAC int
	// dacMuted records that the DAC is at 0 — muted here, or found that way —
	// so the next audible level restores it.
	dacMuted bool
	// mutedHere is set when JukaHub itself muted (DAC at 0, "digital volume"
	// at the top of its range). Only then does a change made by the firmware's
	// keys mean "un-mute": they know nothing about JukaHub's DAC mute, so the
	// poll puts the DAC back when they raise the level. A mute JukaHub found
	// (the firmware's own) is the firmware's to lift.
	mutedHere bool
	// gainMuted records that a 0% level switched the gain control's
	// playback switch off, so the next audible level switches it back on.
	gainMuted bool
}

// trimuiProbeLocked asks amixer which controls exist. A probe amixer did not
// answer is retried after trimuiProbeRetry instead of on every call, and so is
// one that found neither control, up to trimuiProbeAttempts answers: a codec
// still registering must not leave the session on the generic Master/PCM path,
// which the tg5040 does not have. The caller holds trimuiMixer.mu.
func trimuiProbeLocked() trimuiMixerInfo {
	if trimuiMixer.probed || (!trimuiMixer.probedAt.IsZero() && time.Since(trimuiMixer.probedAt) < trimuiProbeRetry) {
		return trimuiMixer.info
	}
	trimuiMixer.probedAt = time.Now()
	out, err := trimuiAmixer("scontrols")
	if err != nil {
		logDebug("volume", "amixer scontrols failed: %v", err)
		return trimuiMixer.info
	}
	info := trimuiMixerInfo{digitalMax: trimuiDigitalMaxDefault}
	names := parseAmixerSimpleControls(out)
	for _, name := range names {
		switch {
		case strings.EqualFold(name, trimuiDigitalControl):
			info.digital, info.digitalName = true, name
		case strings.EqualFold(name, trimuiDACControl):
			info.dac, info.dacName = true, name
		}
	}
	if !info.digital {
		// Not the tg5040 codec: the first gain control the codec really has.
		trimuiProbeGainLocked(&info, names)
	}
	trimuiMixer.attempts++
	if !info.digital && !info.dac && info.gain == "" && trimuiMixer.attempts < trimuiProbeAttempts {
		logDebug("volume", "TrimUI mixer: neither %q nor %q listed yet (attempt %d)", trimuiDigitalControl, trimuiDACControl, trimuiMixer.attempts)
		return trimuiMixer.info
	}
	if info.digital {
		if out, err := trimuiAmixer("get", info.digitalName); err == nil {
			if _, max, ok := parseAmixerLevel(out); ok && max > 0 {
				info.digitalMax = max
			}
		}
	}
	if info.dac && info.digital {
		if out, err := trimuiAmixer("get", info.dacName); err == nil {
			if raw, max, ok := parseAmixerLevel(out); ok {
				info.dacMax = max
				if raw == 0 {
					// Muted before JukaHub started (by the firmware or a
					// previous run): the first audible level puts it back.
					trimuiMixer.dacMuted = true
					// A previous run's own mute stays JukaHub's, so the
					// hardware keys still lift it (trimuiVolume) and the DAC
					// level it replaced comes back.
					if saved, ok := trimuiReadMuteMarker(); ok {
						trimuiMixer.mutedHere = true
						if saved > 0 {
							trimuiMixer.savedDAC = saved
						}
					}
				} else {
					trimuiMixer.savedDAC = raw
					trimuiClearMuteMarker()
				}
			}
		}
	}
	trimuiMixer.info = info
	trimuiMixer.probed = true
	if card := trimuiCodecCardID(); card != "" {
		logInfo("volume", "using built-in ALSA card %q", card)
	}
	logInfo("volume", "TrimUI mixer: %s", trimuiMixerDescribe(info))
	return info
}

// trimuiGainPrefs are the gain controls tried on a codec without "digital
// volume", best first: the usual ALSA master controls, the sunxi BSP codec's
// digital DAC gain, then the output stages by name. Matched without regard to
// case; only a control with a volume range (a "Limits:" line) is used, so a
// plain switch named "Speaker" is passed over.
var trimuiGainPrefs = []string{
	"Master", "PCM", "DAC Volume", "DAC", "Digital", "Playback",
	"Headphone", "Speaker", "LINEOUT Volume", "LINEOUT", "Line Out", "Lineout",
	"HPOUT Volume", "HPOUT", "SPK", "Line",
}

// trimuiPickGainControl orders the listed controls by trimuiGainPrefs: exact
// names first, then names that start with a preferred one ("DAC Playback
// Volume", "LINEOUT Gain"). Capture and boost controls are never picked. Pure.
func trimuiPickGainControl(names []string) []string {
	var out []string
	seen := map[string]bool{}
	usable := func(n string) bool {
		l := strings.ToLower(n)
		for _, bad := range []string{"capture", "mic", "boost", "adc", "linein", "line in", "loopback"} {
			if strings.Contains(l, bad) {
				return false
			}
		}
		return !seen[n]
	}
	for _, pass := range []func(n, pref string) bool{
		strings.EqualFold,
		func(n, pref string) bool { return strings.HasPrefix(strings.ToLower(n), strings.ToLower(pref)) },
	} {
		for _, pref := range trimuiGainPrefs {
			for _, n := range names {
				if usable(n) && pass(n, pref) {
					seen[n] = true
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// trimuiProbeGainLocked picks the gain control for a codec that is not the
// tg5040's, asking amixer for each candidate's range. The caller holds
// trimuiMixer.mu.
func trimuiProbeGainLocked(info *trimuiMixerInfo, names []string) {
	for _, name := range trimuiPickGainControl(names) {
		out, err := trimuiAmixer("get", name)
		if err != nil {
			continue
		}
		_, max, ok := parseAmixerLevel(out)
		if !ok || max <= 0 {
			continue
		}
		info.gain, info.gainMax, info.gainCeil = name, max, 0
		if strings.Contains(strings.ToLower(name), "dac") && max == 255 {
			// The sunxi DAC gain: -119.25 dB at 0, 0 dB at 160, +71.25 dB
			// at 255. Above 160 only clips.
			info.gainCeil = trimuiDACUnmuteDefault
		}
		return
	}
}

// trimuiMixerDescribe is the chosen controls in words, for the log and the
// Drivers page.
func trimuiMixerDescribe(info trimuiMixerInfo) string {
	switch {
	case info.digital && info.dac:
		return fmt.Sprintf("tg5040 codec: %q (attenuation 0..%d) + %q for a silent mute", info.digitalName, info.digitalMax, info.dacName)
	case info.digital:
		return fmt.Sprintf("tg5040 codec: %q (attenuation 0..%d), no DAC mute", info.digitalName, info.digitalMax)
	case info.gain != "" && info.gainCeil > 0:
		return fmt.Sprintf("%q (gain 0..%d, 100%% = %d, 0 dB)", info.gain, info.gainMax, info.gainCeil)
	case info.gain != "":
		return fmt.Sprintf("%q (gain 0..%d, mapped percent)", info.gain, info.gainMax)
	}
	return "no known volume control; trying Master, then PCM"
}

// trimuiMixerStatus is the Drivers page's line: what the probe chose, without
// running amixer (the probe runs with the first volume read or change).
func trimuiMixerStatus() (string, bool) {
	trimuiMixer.mu.Lock()
	defer trimuiMixer.mu.Unlock()
	if !trimuiMixer.probed {
		return "not probed yet", false
	}
	info := trimuiMixer.info
	return trimuiMixerDescribe(info), info.digital || info.gain != ""
}

// trimuiGainCurveDB and trimuiGainStepDB shape the level on a gain control
// with a known 0 dB point: alsamixer's mapped volume (60 dB from silent-ish to
// full, so 50% is about -18 dB, close to the tg5040 curve's -15) over the
// sunxi DAC's 0.75 dB steps.
const (
	trimuiGainCurveDB = 60.0
	trimuiGainStepDB  = 0.75
)

// trimuiGainRawFor maps 0..100 to a raw gain whose 100% is ceil. Pure.
func trimuiGainRawFor(pct, ceil int) int {
	if pct <= 0 || ceil <= 0 {
		return 0
	}
	if pct >= 100 {
		return ceil
	}
	raw := int(math.Round(float64(ceil) + trimuiGainCurveDB*math.Log10(float64(pct)/100)/trimuiGainStepDB))
	if raw < 1 {
		raw = 1 // audible levels never become the mute value
	}
	return raw
}

// trimuiGainPercentFor is trimuiGainRawFor's inverse, snapped to 5% steps;
// raw 0 reads as 0 (muted). Pure.
func trimuiGainPercentFor(raw, ceil int) int {
	if raw <= 0 || ceil <= 0 {
		return 0
	}
	if raw >= ceil {
		return 100
	}
	pct := 100 * math.Pow(10, float64(raw-ceil)*trimuiGainStepDB/trimuiGainCurveDB)
	snapped := int(math.Round(pct/5)) * 5
	if snapped < 5 {
		snapped = 5
	}
	return snapped
}

// trimuiGainSetLocked applies a level to the gain control. 0% also switches
// the control's playback switch off where it has one (an error from a
// control without a switch is ignored). The caller holds trimuiMixer.mu.
func trimuiGainSetLocked(info trimuiMixerInfo, pct int) error {
	var err error
	if info.gainCeil > 0 {
		err = trimuiAmixerSet(info.gain, trimuiGainRawFor(pct, info.gainCeil))
	} else {
		level := strconv.Itoa(pct) + "%"
		if _, err = trimuiAmixer("-q", "-M", "sset", info.gain, level); err != nil {
			// An amixer without -M (busybox) takes the plain percentage.
			if _, err = trimuiAmixer("-q", "sset", info.gain, level); err != nil {
				err = fmt.Errorf("amixer %s: %w", info.gain, err)
			}
		}
	}
	if err != nil {
		return err
	}
	switch {
	case pct <= 0 && !trimuiMixer.gainMuted:
		if _, e := trimuiAmixer("-q", "sset", info.gain, "mute"); e == nil {
			trimuiMixer.gainMuted = true
		}
	case pct > 0 && trimuiMixer.gainMuted:
		_, _ = trimuiAmixer("-q", "sset", info.gain, "unmute")
		trimuiMixer.gainMuted = false
	}
	return nil
}

// trimuiGainReadLocked reads the gain control's level. The caller holds
// trimuiMixer.mu.
func trimuiGainReadLocked(info trimuiMixerInfo) (int, bool) {
	if info.gainCeil > 0 {
		out, err := trimuiAmixer("get", info.gain)
		if err != nil {
			return 0, false
		}
		raw, _, ok := parseAmixerLevel(out)
		if !ok {
			return 0, false
		}
		return trimuiGainPercentFor(raw, info.gainCeil), true
	}
	out, err := trimuiAmixer("-M", "get", info.gain)
	if err != nil {
		if out, err = trimuiAmixer("get", info.gain); err != nil {
			return 0, false
		}
	}
	if strings.Contains(out, "[off]") {
		return 0, true
	}
	// Switched back on by someone else (the firmware's keys): the next 0%
	// switches it off again.
	trimuiMixer.gainMuted = false
	return parseFirstPercent(out)
}

// trimuiReadDACLocked reads the DAC's raw level; ok is false when amixer did
// not answer. The caller holds trimuiMixer.mu.
func trimuiReadDACLocked(info trimuiMixerInfo) (int, bool) {
	out, err := trimuiAmixer("get", info.dacName)
	if err != nil {
		return 0, false
	}
	raw, _, ok := parseAmixerLevel(out)
	return raw, ok
}

// trimuiRestoreDACLocked puts the DAC back where it was before the mute (its
// 0 dB point when that is unknown) and forgets the mute. The caller holds
// trimuiMixer.mu.
func trimuiRestoreDACLocked(info trimuiMixerInfo) error {
	restore := trimuiMixer.savedDAC
	if restore <= 0 {
		restore = trimuiDACUnmuteDefault
	}
	if info.dacMax > 0 && restore > info.dacMax {
		restore = info.dacMax
	}
	if err := trimuiAmixerSet(info.dacName, restore); err != nil {
		return err
	}
	trimuiMixer.dacMuted, trimuiMixer.mutedHere = false, false
	trimuiClearMuteMarker()
	return nil
}

// trimuiSetVolume applies a 0..100 level the user chose to the codec, and
// keeps the firmware's own level (system.json) in step with it. Zero is a real
// mute (the DAC goes to 0 as well); an audible level restores the DAC. A codec
// without "digital volume" gets the built-in card's Master/PCM path.
func trimuiSetVolume(pct int) error { return trimuiApplyVolume(pct, true) }

// trimuiSetVolumeTransient applies a level that is only borrowed — the alarm
// raising the speaker for a ring and giving the old level back
// (setSystemVolumeTransient). The firmware's saved level is left alone: the
// marker that undoes a ring lives in RAM on the handheld, so a 100% written to
// system.json would be what the device came back with after a crash or a
// power cut mid-ring.
func trimuiSetVolumeTransient(pct int) error { return trimuiApplyVolume(pct, false) }

// trimuiApplyVolume is the mixer write behind both: persist decides whether the
// firmware's level follows.
func trimuiApplyVolume(pct int, persist bool) error {
	trimuiMixer.mu.Lock()
	defer trimuiMixer.mu.Unlock()
	info := trimuiProbeLocked()
	if !info.digital {
		if info.gain == "" {
			return trimuiGenericSet(strconv.Itoa(pct) + "%")
		}
		if err := trimuiGainSetLocked(info, pct); err != nil {
			return err
		}
		if persist {
			trimuiSystemVolumeSyncLater(pct)
		}
		return nil
	}
	if pct <= 0 {
		if info.dac {
			if !trimuiMixer.dacMuted {
				if raw, ok := trimuiReadDACLocked(info); ok && raw > 0 {
					trimuiMixer.savedDAC = raw
				}
			}
			if err := trimuiAmixerSet(info.dacName, 0); err != nil {
				return err
			}
			trimuiMixer.dacMuted = true
		}
		if err := trimuiAmixerSet(info.digitalName, info.digitalMax); err != nil {
			return err
		}
		// The DAC and the attenuation are both where a mute made here puts
		// them, which is what the poll compares against (trimuiVolume).
		trimuiMixer.mutedHere = info.dac
		if info.dac {
			trimuiWriteMuteMarker(trimuiMixer.savedDAC)
		}
	} else {
		// The level first, then the DAC: un-muting at the old, quietest
		// attenuation and then jumping would be heard.
		raw := trimuiAttenuationFor(pct, info.digitalMax)
		if trimuiStockVolumeCurve(info.digitalMax) {
			raw = trimuiStockAttenuationFor(pct, info.digitalMax)
		}
		if err := trimuiAmixerSet(info.digitalName, raw); err != nil {
			return err
		}
		if info.dac {
			// Whoever muted the DAC — JukaHub, or the firmware's keys taking
			// the level to 0 — an audible level has to bring it back, so the
			// DAC is read rather than trusted to a flag only JukaHub's own
			// mutes set: "50%" with a silent speaker was the result.
			muted := trimuiMixer.dacMuted
			if !muted {
				if raw, ok := trimuiReadDACLocked(info); ok {
					if raw == 0 {
						muted = true
					} else {
						trimuiMixer.savedDAC = raw
					}
				}
			}
			if muted {
				if err := trimuiRestoreDACLocked(info); err != nil {
					return err
				}
			}
		}
		trimuiMixer.mutedHere = false
	}
	if persist {
		trimuiSystemVolumeSyncLater(pct)
	}
	return nil
}

// trimuiAmixerSet writes one raw value to one control: an argv call, never a
// shell line, with the control name as one argument.
func trimuiAmixerSet(control string, raw int) error {
	if _, err := trimuiAmixer("-q", "sset", control, strconv.Itoa(raw)); err != nil {
		return fmt.Errorf("amixer %s: %w", control, err)
	}
	return nil
}

// trimuiVolume reads the level from the codec: 0 while the DAC is at 0
// (silent, whoever muted it), otherwise the "digital volume" attenuation
// through the inverse curve. The firmware's "vol" in system.json is only the
// fallback for when amixer cannot answer (see the file comment for why it is
// not the first choice).
//
// It is also where JukaHub notices the firmware's keys lifting a mute it
// made: they raise the level without knowing the DAC is at 0, so the speaker
// would stay silent while the corner showed a level. The poll runs a moment
// after every hardware key (volumeNoteHardwareKey), so the DAC comes back
// with the press.
func trimuiVolume() (int, bool) {
	trimuiMixer.mu.Lock()
	defer trimuiMixer.mu.Unlock()
	info := trimuiProbeLocked()
	if !info.digital && info.gain != "" {
		if pct, ok := trimuiGainReadLocked(info); ok {
			return pct, true
		}
		return trimuiSystemVolume()
	}
	if !info.digital {
		if pct, ok := trimuiGenericVolume(); ok {
			return pct, true
		}
		return trimuiSystemVolume()
	}
	out, err := trimuiAmixer("get", info.digitalName)
	if err != nil {
		return trimuiSystemVolume()
	}
	raw, _, ok := parseAmixerLevel(out)
	if !ok {
		return trimuiSystemVolume()
	}
	if info.dac {
		if dac, ok := trimuiReadDACLocked(info); ok {
			switch {
			case dac > 0 && trimuiMixer.dacMuted:
				// Someone else (the firmware's keys) un-muted the DAC: its
				// level is the one to come back to after the next mute.
				trimuiMixer.dacMuted, trimuiMixer.mutedHere = false, false
				trimuiMixer.savedDAC = dac
				trimuiClearMuteMarker()
			case dac == 0 && trimuiMixer.mutedHere && raw < info.digitalMax:
				// JukaHub's mute, and the attenuation has moved off the top of
				// its range since: the firmware's keys raised the volume, so
				// the DAC goes back and the press is heard. system.json is not
				// a sign of a key press: the firmware rewrites the whole file
				// for other settings too (brightness), and a real key press
				// always moves the attenuation.
				if err := trimuiRestoreDACLocked(info); err != nil {
					logDebug("volume", "could not lift the mute after a hardware key: %v", err)
					return 0, true
				}
				logDebug("volume", "the firmware's keys raised the level; DAC restored")
				dac = trimuiMixer.savedDAC
				if dac <= 0 {
					dac = trimuiDACUnmuteDefault
				}
			}
			if dac == 0 {
				return 0, true
			}
		}
	}
	if trimuiStockVolumeCurve(info.digitalMax) {
		return trimuiStockPercentForAttenuation(raw, info.digitalMax), true
	}
	return trimuiPercentForAttenuation(raw, info.digitalMax), true
}

// trimuiAttenuationFor maps a 0..100 level to the codec's attenuation: 100%
// is 0 (full scale), 0% the top of the range. Pure.
func trimuiAttenuationFor(pct, max int) int {
	if max <= 0 {
		max = trimuiDigitalMaxDefault
	}
	if pct >= 100 {
		return 0
	}
	if pct <= 0 {
		return max
	}
	raw := int(math.Round(-trimuiCurveScale * math.Log10(float64(pct)/100)))
	if raw < 0 {
		raw = 0
	}
	if raw > max {
		raw = max
	}
	return raw
}

// trimuiPercentForAttenuation is trimuiAttenuationFor's inverse, snapped to
// the firmware's 5% steps so a level read back matches the one written (the
// curve is steep near the top, where one raw step is about 5%). An audible
// attenuation never reads as 0%: zero means muted. Pure.
func trimuiPercentForAttenuation(raw, max int) int {
	if max <= 0 {
		max = trimuiDigitalMaxDefault
	}
	if raw <= 0 {
		return 100
	}
	if raw > max {
		raw = max
	}
	pct := 100 * math.Pow(10, -float64(raw)/trimuiCurveScale)
	snapped := int(math.Round(pct/5)) * 5
	if snapped < 5 {
		snapped = 5
	}
	if snapped > 100 {
		snapped = 100
	}
	return snapped
}

// parseAmixerSimpleControls lists the control names in `amixer scontrols`
// output ("Simple mixer control 'digital volume',0"). Pure.
func parseAmixerSimpleControls(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "Simple mixer control '")
		if !ok {
			continue
		}
		end := strings.LastIndex(rest, "',")
		if end < 0 {
			end = strings.LastIndex(rest, "'")
		}
		if end <= 0 {
			continue
		}
		names = append(names, rest[:end])
	}
	return names
}

// parseAmixerLevel reads one control's raw value and range top from `amixer
// get` output:
//
//	Limits: Playback 0 - 63          (or "Limits: 0 - 63")
//	Mono: Playback 30 [48%] [-30.00dB]   (or "Front Left: 160 [63%]")
//
// raw is the integer just before the first "[" on the first channel line; max
// is 0 when the output has no Limits line. Pure.
func parseAmixerLevel(out string) (raw, max int, ok bool) {
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, isLimits := strings.CutPrefix(line, "Limits:"); isLimits {
			fields := strings.Fields(rest)
			for i := len(fields) - 1; i >= 0; i-- {
				if v, err := strconv.Atoi(fields[i]); err == nil {
					max = v
					break
				}
			}
			continue
		}
		if found {
			continue
		}
		bracket := strings.Index(line, "[")
		colon := strings.Index(line, ":")
		if bracket < 0 || colon < 0 || colon > bracket {
			continue
		}
		fields := strings.Fields(line[colon+1 : bracket])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			continue
		}
		raw, found = v, true
	}
	return raw, max, found
}

// ── system.json ────────────────────────────────────────────────────────────

// trimuiSystemVolume reads the firmware's level as a percentage.
func trimuiSystemVolume() (int, bool) {
	vol, ok := trimuiSystemVolumeStep()
	if !ok {
		return 0, false
	}
	return vol * 100 / trimuiSystemSteps, true
}

// trimuiRestoreVolumeAtLaunch puts the firmware's saved level (system.json
// "vol", or "hpvol" with headphones in) on the codec once at launch, where the
// codec shows another one. Started at boot by the autostart hook, JukaHub runs
// before the stock launcher would have applied the file, and the speaker came
// up at the codec's own default after every restart while the file still held
// the level. Launched from the stock menu the two agree and nothing is
// written. The file is not rewritten (it already holds the level), and a
// level the user set meanwhile wins. A codec still registering is asked again
// when the mixer probe retries (trimuiProbeRetry), a few times. Off the main
// thread: amixer runs, and it may wait.
func trimuiRestoreVolumeAtLaunch() {
	if !IsTrimuiDevice() || volumeCachedBackend() != "trimui" {
		return
	}
	for try := 0; try < 4; try++ {
		if try > 0 {
			time.Sleep(trimuiProbeRetry + time.Second)
		}
		if gen, _ := volumeEditCounters(); gen > 0 {
			return
		}
		data, err := os.ReadFile(trimuiSystemJSONPath)
		if err != nil {
			return
		}
		key := "vol"
		if headphoneJackPlugged(os.ReadFile) || headphoneJackEvdev() {
			if _, ok, _ := trimuiSystemJSONIntFrom(data, "hpvol", trimuiSystemSteps); ok {
				key = "hpvol"
			}
		}
		step, ok, inRange := trimuiSystemJSONIntFrom(data, key, trimuiSystemSteps)
		if !ok || !inRange {
			return
		}
		want := step * 100 / trimuiSystemSteps
		cur, ok := trimuiVolume()
		if !trimuiMixerAnswered() {
			// trimuiVolume fell back to the file: the codec did not answer.
			continue
		}
		if !ok || cur == want {
			return
		}
		if gen, _ := volumeEditCounters(); gen > 0 {
			return
		}
		if err := trimuiSetVolumeTransient(want); err != nil {
			logWarn("volume", "could not put back the firmware's saved level %d%%: %v", want, err)
			return
		}
		logInfo("volume", "the codec was at %d%% after the start; put back the firmware's saved %s %d (%d%%)", cur, key, step, want)
		volumeRefreshNow()
		return
	}
}

// trimuiMixerAnswered reports whether the mixer probe found a control to
// drive, i.e. amixer answered for this codec.
func trimuiMixerAnswered() bool {
	trimuiMixer.mu.Lock()
	defer trimuiMixer.mu.Unlock()
	return trimuiMixer.info.digital || trimuiMixer.info.gain != ""
}

// trimuiSystemVolumeStep reads the firmware's level in its own twentieths.
func trimuiSystemVolumeStep() (int, bool) {
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		return 0, false
	}
	return trimuiSystemVolumeFrom(data)
}

// trimuiSystemStepFor turns a level into the firmware's twentieths: rounded,
// except that an audible level never becomes 0, which is the firmware's mute —
// 1–2% written as "vol": 0 read back as muted while the speaker still played.
// Pure.
func trimuiSystemStepFor(pct int) int {
	if pct <= 0 {
		return 0
	}
	vol := (pct*trimuiSystemSteps + 50) / 100
	if vol < 1 {
		vol = 1
	}
	if vol > trimuiSystemSteps {
		vol = trimuiSystemSteps
	}
	return vol
}

// trimuiSystemVolumeFrom reads the top-level "vol" (0..20) out of the
// settings document; ok=false when the document is not an object or holds no
// such number. Pure.
func trimuiSystemVolumeFrom(data []byte) (int, bool) {
	vol, ok, _ := trimuiSystemJSONIntFrom(data, "vol", trimuiSystemSteps)
	return vol, ok
}

// trimuiSystemJSONWithVolume returns the settings document with "vol" set to
// vol, changing nothing else (trimuiSystemJSONWithInt, brightness_trimui.go,
// which the brightness's "brightness" shares). It reports false (and returns
// data as it was) when the document is not an object with exactly one numeric
// "vol", or when vol is already the value. Pure.
func trimuiSystemJSONWithVolume(data []byte, vol int) ([]byte, bool) {
	return trimuiSystemJSONWithInt(data, "vol", vol, trimuiSystemSteps)
}

// errTrimuiSystemJSONChanged is trimuiSystemVolumeWrite giving up because the
// firmware wrote the file while it was being updated.
var errTrimuiSystemJSONChanged = errors.New("system.json changed while it was being updated")

// trimuiSystemVolumeWrite stores vol (0..20) in system.json through the
// shared rewrite (trimuiSystemJSONRewrite, brightness_trimui.go): in place, on
// the same inode keymon's inotify watch is on, written where a symlinked file
// really lives, with its mode kept, and skipped (errTrimuiSystemJSONChanged)
// when the firmware changed the file while it was being updated or left it
// half-written.
func trimuiSystemVolumeWrite(vol int) error {
	return trimuiSystemJSONRewrite(func(data []byte) ([]byte, bool) {
		return trimuiSystemJSONWithVolume(data, vol)
	})
}

// trimuiSystemSync debounces the system.json write (trimuiSystemSyncDelay).
// writeMu keeps two late timers from writing the file at the same time.
// pending is true from the moment a level is asked for until it is on the
// card; seq tells a write still in flight from a newer request; failed says
// the last write did not reach the file, so the file does not show JukaHub's
// level.
var trimuiSystemSync struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	timer   *time.Timer
	vol     int
	pending bool
	failed  bool
	seq     int
	retries int
}

func init() {
	// A level set just before quitting is still written: the exit path runs
	// the pending write instead of letting the timer die with the process.
	Cleanup = append(Cleanup, trimuiSystemVolumeFlush)
}

// trimuiSystemVolumeSyncLater schedules the firmware's "vol" to follow a level
// set here, once the level has settled.
func trimuiSystemVolumeSyncLater(pct int) {
	trimuiSystemSync.mu.Lock()
	defer trimuiSystemSync.mu.Unlock()
	trimuiSystemSync.vol = trimuiSystemStepFor(pct)
	trimuiSystemSync.pending = true
	trimuiSystemSync.seq++
	trimuiSystemSync.retries = 0
	if trimuiSystemSync.timer != nil {
		trimuiSystemSync.timer.Stop()
	}
	trimuiSystemSync.timer = time.AfterFunc(trimuiSystemSyncDelay, trimuiSystemVolumeFlush)
}

// trimuiSystemSyncState reports whether a level asked for is not on the card
// yet, and whether the last write failed.
// Test seam (ARCH-8): only tests call it.
func trimuiSystemSyncState() (pending, failed bool) {
	trimuiSystemSync.mu.Lock()
	defer trimuiSystemSync.mu.Unlock()
	return trimuiSystemSync.pending, trimuiSystemSync.failed
}

// trimuiSystemVolumeFlush writes the pending level now: the debounce timer's
// job, and the exit path's (Cleanup). A write the firmware raced is tried
// again after the delay, a couple of times, unless a newer level replaced it.
func trimuiSystemVolumeFlush() {
	trimuiSystemSync.mu.Lock()
	if trimuiSystemSync.timer != nil {
		trimuiSystemSync.timer.Stop()
		trimuiSystemSync.timer = nil
	}
	if !trimuiSystemSync.pending {
		trimuiSystemSync.mu.Unlock()
		return
	}
	v, seq := trimuiSystemSync.vol, trimuiSystemSync.seq
	trimuiSystemSync.mu.Unlock()

	trimuiSystemSync.writeMu.Lock()
	err := trimuiSystemVolumeWrite(v)
	trimuiSystemSync.writeMu.Unlock()

	trimuiSystemSync.mu.Lock()
	defer trimuiSystemSync.mu.Unlock()
	if trimuiSystemSync.seq != seq {
		return // a newer level is on its way and will be written in turn
	}
	if errors.Is(err, errTrimuiSystemJSONChanged) && trimuiSystemSync.retries < trimuiSystemSyncRetries {
		trimuiSystemSync.retries++
		trimuiSystemSync.timer = time.AfterFunc(trimuiSystemSyncDelay, trimuiSystemVolumeFlush)
		return
	}
	trimuiSystemSync.pending = false
	trimuiSystemSync.failed = err != nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logDebug("volume", "system.json not updated: %v", err)
	}
}
