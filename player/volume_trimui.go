package main

// volume_trimui.go — the TrimUI Smart Pro / Brick (tg5040) audio mixer.
//
// The generic ALSA path (runAmixerSet) tries Master, then PCM. The tg5040
// codec exposes neither, so on the Smart Pro and the Brick every volume write
// failed. What the codec does have, as MinUI's and NextUI's tg5040 ports
// found out the hard way:
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
// The control names, the curve and the system.json layout come from those
// ports and their bug reports, not from a device on this desk: everything is
// probed first and falls back to the generic path when the codec is not the
// one described above.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
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
	// NextUI measured on a tg5040 (20% → 30, 50% → 13), so a level set here
	// sounds like the same level set by the stock keys.
	trimuiCurveScale = 43.0

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
	out, err := systemCommandRunner("amixer", "scontrols")
	if err != nil {
		logDebug("volume", "amixer scontrols failed: %v", err)
		return trimuiMixer.info
	}
	info := trimuiMixerInfo{digitalMax: trimuiDigitalMaxDefault}
	for _, name := range parseAmixerSimpleControls(out) {
		switch {
		case strings.EqualFold(name, trimuiDigitalControl):
			info.digital, info.digitalName = true, name
		case strings.EqualFold(name, trimuiDACControl):
			info.dac, info.dacName = true, name
		}
	}
	trimuiMixer.attempts++
	if !info.digital && !info.dac && trimuiMixer.attempts < trimuiProbeAttempts {
		logDebug("volume", "TrimUI mixer: neither %q nor %q listed yet (attempt %d)", trimuiDigitalControl, trimuiDACControl, trimuiMixer.attempts)
		return trimuiMixer.info
	}
	if info.digital {
		if out, err := systemCommandRunner("amixer", "get", info.digitalName); err == nil {
			if _, max, ok := parseAmixerLevel(out); ok && max > 0 {
				info.digitalMax = max
			}
		}
	}
	if info.dac {
		if out, err := systemCommandRunner("amixer", "get", info.dacName); err == nil {
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
	logInfo("volume", "TrimUI mixer: digital volume=%v (0..%d), DAC Volume=%v", info.digital, info.digitalMax, info.dac)
	return info
}

// trimuiReadDACLocked reads the DAC's raw level; ok is false when amixer did
// not answer. The caller holds trimuiMixer.mu.
func trimuiReadDACLocked(info trimuiMixerInfo) (int, bool) {
	out, err := systemCommandRunner("amixer", "get", info.dacName)
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
// without "digital volume" gets the generic Master/PCM path.
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
		return runAmixerSet(strconv.Itoa(pct) + "%")
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
		if err := trimuiAmixerSet(info.digitalName, trimuiAttenuationFor(pct, info.digitalMax)); err != nil {
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
	if _, err := systemCommandRunner("amixer", "-q", "sset", control, strconv.Itoa(raw)); err != nil {
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
	if !info.digital {
		if pct, ok := amixerGenericVolume(); ok {
			return pct, true
		}
		return trimuiSystemVolume()
	}
	out, err := systemCommandRunner("amixer", "get", info.digitalName)
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
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, false
	}
	v, ok := doc["vol"].(float64)
	if !ok || math.IsNaN(v) {
		return 0, false
	}
	vol := int(math.Round(v))
	if vol < 0 {
		vol = 0
	}
	if vol > trimuiSystemSteps {
		vol = trimuiSystemSteps
	}
	return vol, true
}

// trimuiVolKey finds the "vol" member and its number in the settings text.
var trimuiVolKey = regexp.MustCompile(`("vol"\s*:\s*)(-?[0-9]+(?:\.[0-9]+)?)`)

// trimuiSystemJSONWithVolume returns the settings document with "vol" set to
// vol, changing nothing else: not the key order, not the spacing, not another
// number's spelling. Re-encoding the whole document would have rewritten the
// firmware's file in Go's style. It reports false (and returns data as it was)
// when the document is not an object with exactly one numeric "vol", or when
// vol is already the value. Pure.
func trimuiSystemJSONWithVolume(data []byte, vol int) ([]byte, bool) {
	current, ok := trimuiSystemVolumeFrom(data)
	if !ok {
		return data, false
	}
	matches := trimuiVolKey.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return data, false
	}
	m := matches[0]
	if current == vol && string(data[m[4]:m[5]]) == strconv.Itoa(vol) {
		return data, false
	}
	out := make([]byte, 0, len(data)+2)
	out = append(out, data[:m[4]]...)
	out = append(out, strconv.Itoa(vol)...)
	out = append(out, data[m[5]:]...)
	return out, true
}

// errTrimuiSystemJSONChanged is trimuiSystemVolumeWrite giving up because the
// firmware wrote the file while it was being updated.
var errTrimuiSystemJSONChanged = errors.New("system.json changed while it was being updated")

// trimuiSystemVolumeWrite stores vol (0..20) in system.json: a temp file in
// the same directory, flushed, then renamed over the original, so a power cut
// leaves the old file or the new one and never half of each. No .bak is left
// in the firmware's partition.
//
// A symlinked settings file is updated where it really lives (renaming over
// the link would replace it with a plain file and leave the real one stale),
// the temp file gets the original's exact mode (OpenFile's mode passes through
// the umask), and the rename is skipped when the file changed since it was
// read: the firmware writes it too (its keys, its other settings), and
// renaming then would throw that change away.
func trimuiSystemVolumeWrite(vol int) error {
	path := trimuiSystemJSONPath
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed := trimuiSystemJSONWithVolume(data, vol)
	if !changed {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".jukahub-tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	abandon := func(cause error) error {
		f.Close()
		os.Remove(tmp)
		return cause
	}
	if _, err := f.Write(out); err != nil {
		return abandon(err)
	}
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		return abandon(err)
	}
	if err := f.Sync(); err != nil {
		return abandon(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if now, err := os.Stat(path); err != nil || now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime()) {
		os.Remove(tmp)
		return errTrimuiSystemJSONChanged
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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
