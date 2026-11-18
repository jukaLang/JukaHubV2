package main

// usbdac.go — a USB DAC is the output when one is plugged in.
//
// A handheld with a USB-C port can carry a DAC dongle and play lossless files
// like a DAP. The kernel's USB audio driver (snd-usb-audio) gives the DAC an
// ALSA card of its own; nothing on the firmware moves sound to it, so JukaHub
// does, the same way it moves sound to a Bluetooth headset (audioroute.go):
// every JukaHub output goes to the one mixer device opened on
// "plughw:CARD=<id>,DEV=0", and a game started meanwhile gets the DAC as its
// AUDIODEV and ALSA "default" (audioRouteChildEnv, through outAudio.pcm).
//
// Priority: USB DAC, then Bluetooth headset, then speaker or wired jack. A DAC
// is a cable the user just plugged in for listening, and a headset left
// connected in a pocket should not take the sound back from it; the jack is
// the speaker codec itself, so it needs no route. When the DAC goes, the
// route falls to the headset (if one is connected) or the speaker, and what
// plays pauses, as a phone does when headphones are pulled.
//
// Detection is the output monitor's 2 s tick (audiooutput.go), on its own
// goroutine: one read of /proc/asound/cards (a few hundred bytes); only when
// that text changes are the card's usbid and stream0 (rates and formats) read.
// No netlink listener: the tick is the hotplug, and plugging a DAC in is
// heard within about two seconds. While a game holds the screen nothing is
// probed (the game owns the sound).
//
// A USB device with an audio-class interface (bInterfaceClass 01) but no ALSA
// card means the kernel has no USB audio driver loaded. JukaHub loads
// snd-usb-audio once per run when modules.dep lists it, by the rule
// bluetooth_attach.go already follows for the AIC8800 helper (modprobe at a
// fixed path, bounded); a kernel without the module is said plainly, once.
//
// The bit-perfect path (usbdac_bitperfect.go) opens the DAC itself at the
// track's own rate; this file only keeps what the DAC can take.

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// usbDACProcCards is the kernel's card list.
	usbDACProcCards = "/proc/asound/cards"
	// usbDACModule is the kernel's USB audio driver, as modules.dep names it
	// (usbDACModuleSys as /sys/module does).
	usbDACModule    = "snd-usb-audio"
	usbDACModuleSys = "snd_usb_audio"
	// usbDACModprobeWait bounds the one modprobe.
	usbDACModprobeWait = 10 * time.Second
	// usbDACMaxFails is how many failed opens a plugged-in DAC gets (the
	// route's quick retries and one more).
	usbDACMaxFails = outQuickRetries + 1
)

// usbDACAltset is one playback altset of a USB card's stream0: a sample
// format with the rates it plays at, either listed or a continuous range.
type usbDACAltset struct {
	format   string // ALSA's name: S16_LE, S24_3LE, S32_LE…
	channels int
	rates    []int
	rateMin  int // a continuous range ("8000 - 96000 (continuous)"); 0 when listed
	rateMax  int
}

// usbDACCard is a USB audio card the kernel lists.
type usbDACCard struct {
	index int
	id    string // ALSA's card id ("Dongle", "DAC"), what CARD= takes
	name  string // the product, for the header chip
	usbid string // vendor:product
	alts  []usbDACAltset
}

// usbDAC is what the monitor last saw. Written on the monitor's goroutine;
// the header and the music decoder read it.
var usbDAC struct {
	mu     sync.Mutex
	cards  string // /proc/asound/cards as last read, to notice a change
	usbDev string // /sys/bus/usb/devices as last listed
	card   usbDACCard
	routed bool   // JukaHub's outputs are on it
	toast  string // the card id the connect toast was shown for
	// fails counts opens of this card that failed; past usbDACMaxFails it is
	// left alone until it is plugged in again, so a DAC that never opens
	// does not keep closing a headset's stream to try.
	fails int
	// noDriver is said once per run; modprobed is the one try per run.
	noDriver  bool
	modprobed bool
}

// asoundCardLineRE reads a /proc/asound/cards head line:
// " 1 [Dongle         ]: USB-Audio - USB-C to 3.5mm Headphone Jack A".
var asoundCardLineRE = regexp.MustCompile(`^\s*(\d+)\s+\[([^\]]+)\]:\s+(\S+)\s+-\s+(.*)$`)

// asoundCard is one entry of /proc/asound/cards.
type asoundCard struct {
	index  int
	id     string
	driver string
	name   string
}

// parseAsoundCards reads /proc/asound/cards. Each card is a head line (index,
// id, driver, short name) and an indented long-name line, which is skipped.
func parseAsoundCards(text string) []asoundCard {
	var cards []asoundCard
	for _, line := range strings.Split(text, "\n") {
		m := asoundCardLineRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		cards = append(cards, asoundCard{
			index:  idx,
			id:     strings.TrimSpace(m[2]),
			driver: strings.TrimSpace(m[3]),
			name:   strings.TrimSpace(m[4]),
		})
	}
	return cards
}

// parseUSBStreamPlayback reads the Playback section of a USB card's stream0:
// one altset per "Format:" line, with its Channels and Rates. Capture and
// anything after it are left out.
func parseUSBStreamPlayback(text string) []usbDACAltset {
	var alts []usbDACAltset
	inPlayback := false
	cur := -1
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		switch {
		case line == "Playback:":
			inPlayback = true
			continue
		case line == "Capture:":
			inPlayback = false
			continue
		}
		if !inPlayback {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "Format":
			alts = append(alts, usbDACAltset{format: strings.Fields(val + " ?")[0]})
			cur = len(alts) - 1
		case "Channels":
			if cur >= 0 {
				alts[cur].channels, _ = strconv.Atoi(val)
			}
		case "Rates":
			if cur < 0 {
				continue
			}
			if lo, hi, ok := strings.Cut(val, "-"); ok {
				// "8000 - 96000 (continuous)"
				a, errA := strconv.Atoi(strings.TrimSpace(lo))
				b, errB := strconv.Atoi(strings.Fields(strings.TrimSpace(hi) + " ?")[0])
				if errA == nil && errB == nil && a > 0 && b >= a {
					alts[cur].rateMin, alts[cur].rateMax = a, b
				}
				continue
			}
			for _, f := range strings.Split(val, ",") {
				if r, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && r > 0 {
					alts[cur].rates = append(alts[cur].rates, r)
				}
			}
		}
	}
	return alts
}

// playsRate reports whether the altset lists rate (or its range holds it).
func (a usbDACAltset) playsRate(rate int) bool {
	if a.rateMin > 0 && rate >= a.rateMin && rate <= a.rateMax {
		return true
	}
	for _, r := range a.rates {
		if r == rate {
			return true
		}
	}
	return false
}

// usbDACFirst is the first USB audio card in a card list with its stream0
// read, ok false when there is none.
func usbDACFirst(cards []asoundCard) (usbDACCard, bool) {
	for _, c := range cards {
		if !strings.EqualFold(c.driver, "USB-Audio") || c.id == "" || strings.ContainsAny(c.id, "\",\n\\ ") {
			continue
		}
		dir := "/proc/asound/card" + strconv.Itoa(c.index)
		card := usbDACCard{index: c.index, id: c.id, name: cleanAudioOutputName(c.name)}
		if raw, err := os.ReadFile(dir + "/usbid"); err == nil {
			card.usbid = strings.TrimSpace(string(raw))
		}
		if raw, err := os.ReadFile(dir + "/stream0"); err == nil && len(raw) < 64<<10 {
			card.alts = parseUSBStreamPlayback(string(raw))
		}
		if card.name == "" {
			card.name = c.id
		}
		return card, true
	}
	return usbDACCard{}, false
}

// usbDACPoll reads the card list (the monitor's 2 s tick) and reports whether
// the USB DAC picture changed, so the monitor probes the route now. Cheap:
// one small proc file, and only on a change anything else.
func usbDACPoll() bool {
	if !usbDACApplies() {
		return false
	}
	raw, err := os.ReadFile(usbDACProcCards)
	if err != nil {
		return false
	}
	text := string(raw)
	usbDAC.mu.Lock()
	same := text == usbDAC.cards
	usbDAC.cards = text
	had := usbDAC.card.id
	usbDAC.mu.Unlock()
	changed := false
	if !same {
		card, ok := usbDACFirst(parseAsoundCards(text))
		usbDAC.mu.Lock()
		if card.id != usbDAC.card.id {
			usbDAC.fails = 0
		}
		usbDAC.card = card
		usbDAC.mu.Unlock()
		if ok {
			logInfo("audio", "USB DAC: card %d %q (%s, usb %s), %d playback format(s)", card.index, card.id, card.name, card.usbid, len(card.alts))
		} else if had != "" {
			logInfo("audio", "USB DAC %q is gone", had)
		}
		changed = card.id != had
	}
	usbDAC.mu.Lock()
	present := usbDAC.card.id != ""
	usbDAC.mu.Unlock()
	if !present {
		usbDACCheckDriver()
	}
	return changed
}

// usbDACApplies reports whether this device routes to a USB DAC: the
// handhelds, like the headset route (a desktop's sound server does it there).
func usbDACApplies() bool {
	return IsTrimuiDevice() && regularFile(usbDACProcCards)
}

// usbDACCurrent is the USB DAC the kernel lists, id "" for none.
func usbDACCurrent() usbDACCard {
	usbDAC.mu.Lock()
	defer usbDAC.mu.Unlock()
	return usbDAC.card
}

// usbDACPCMFor is the route's PCM for a card: plughw, so every output's
// format reaches a DAC that takes only 24- or 32-bit samples.
func usbDACPCMFor(id string) string {
	return "plughw:CARD=" + id + ",DEV=0"
}

// usbDACRoutePCM is the PCM the route should be on, "" without a DAC (or
// with one that would not open, usbDACMaxFails times).
func usbDACRoutePCM() string {
	usbDAC.mu.Lock()
	defer usbDAC.mu.Unlock()
	if usbDAC.card.id == "" || usbDAC.fails >= usbDACMaxFails {
		return ""
	}
	return usbDACPCMFor(usbDAC.card.id)
}

// usbDACOpenFailed counts a failed open of the DAC's route (audioRouteUpdate)
// and, at the last one, says that the sound stays where it was.
func usbDACOpenFailed() {
	usbDAC.mu.Lock()
	usbDAC.fails++
	last := usbDAC.fails == usbDACMaxFails
	name := usbDAC.card.name
	usbDAC.mu.Unlock()
	if last {
		logWarn("audio", "USB DAC %q would not open; leaving it until it is plugged in again", name)
		showToast("USB DAC "+name+" would not open — sound stays on this output", ToastWarn())
	}
}

// usbDACPCMIs reports whether pcm is a USB DAC route (as opposed to a
// Bluetooth headset's).
func usbDACPCMIs(pcm string) bool {
	return strings.HasPrefix(pcm, "plughw:CARD=") || strings.HasPrefix(pcm, "hw:CARD=")
}

// usbDACRouted records whether the route is on the DAC (audioRouteUpdate,
// the monitor's goroutine), says so on a connect, and pauses what plays when
// the DAC was pulled out.
func usbDACRouted(on bool) {
	usbDAC.mu.Lock()
	was := usbDAC.routed
	usbDAC.routed = on
	card := usbDAC.card
	toast := on && card.id != "" && usbDAC.toast != card.id
	if toast {
		usbDAC.toast = card.id
	}
	if !on && card.id == "" {
		usbDAC.toast = "" // the next plug-in says it again
	}
	usbDAC.mu.Unlock()
	if toast {
		showToast("USB DAC connected · "+card.name, ToastSuccess())
	}
	if was && !on && card.id == "" {
		tbxPost(usbDACPauseMedia)
	}
}

// usbDACPauseMedia pauses the video and the music that were playing on the
// DAC that was just pulled out, and says why. Main loop.
func usbDACPauseMedia() {
	paused := false
	if playerPhase() == "playing" {
		ToggleVideoPlayback()
		paused = true
	}
	if _, _, _, playing, isPaused := ambientNowPlaying(); playing && !isPaused {
		musicTogglePause()
		paused = true
	}
	if paused {
		ShowToast("USB DAC disconnected — playback paused", ToastKindInfo)
	} else {
		ShowToast("USB DAC disconnected", ToastKindInfo)
	}
}

// usbDACChipName is the header chip while the route is on the DAC, "" when
// it is not.
func usbDACChipName() string {
	usbDAC.mu.Lock()
	defer usbDAC.mu.Unlock()
	if !usbDAC.routed || usbDAC.card.id == "" {
		return ""
	}
	return "USB DAC · " + usbDAC.card.name
}

// ── No driver ───────────────────────────────────────────────────────────────

// usbDACCheckDriver looks for a USB device with an audio-class interface
// while no USB card exists: the USB device list is read only when it changed.
func usbDACCheckDriver() {
	const root = "/sys/bus/usb/devices"
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sig := strings.Join(names, " ")
	usbDAC.mu.Lock()
	same := sig == usbDAC.usbDev
	usbDAC.usbDev = sig
	usbDAC.mu.Unlock()
	if same {
		return
	}
	audio := false
	for _, n := range names {
		if !strings.Contains(n, ":") {
			continue // a device, not one of its interfaces
		}
		raw, err := os.ReadFile(path.Join(root, n, "bInterfaceClass"))
		if err == nil && strings.TrimSpace(string(raw)) == "01" {
			audio = true
			break
		}
	}
	if !audio {
		return
	}
	if _, err := os.Stat(filepath.Join("/sys/module", usbDACModuleSys)); err == nil {
		// The driver is there: a device it would not take (a microphone, an
		// unsupported class), nothing to say.
		return
	}
	usbDAC.mu.Lock()
	try := !usbDAC.modprobed
	usbDAC.modprobed = true
	usbDAC.mu.Unlock()
	if try && usbDACModprobe() {
		return // the card shows up on the next tick
	}
	usbDAC.mu.Lock()
	say := !usbDAC.noDriver
	usbDAC.noDriver = true
	usbDAC.mu.Unlock()
	if say {
		logWarn("audio", "a USB audio device is attached but the kernel has no USB audio driver (%s)", usbDACModule)
		showToast("USB DAC found, but this firmware's kernel has no USB audio driver (snd-usb-audio)", ToastWarn())
	}
}

// usbDACModprobe loads snd-usb-audio when the running kernel ships it
// (modules.dep lists it) and reports whether it loaded. Once per run, by the
// AIC8800 helper's rule (bluetooth_attach.go).
func usbDACModprobe() bool {
	modprobe, ok := btToolPath("modprobe")
	if !ok {
		return false
	}
	rel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	dep, err := os.ReadFile(filepath.Join("/lib/modules", strings.TrimSpace(string(rel)), "modules.dep"))
	if err != nil || !modulesDepHas(string(dep), usbDACModule) {
		return false
	}
	err = btRunBounded(context.Background(), usbDACModprobeWait, modprobe, usbDACModule)
	logInfo("audio", "USB DAC: modprobe %s: %v", usbDACModule, err)
	return err == nil
}
