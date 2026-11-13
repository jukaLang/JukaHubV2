package main

// ledanim.go — the TrimUI stick rings.
//
// ledsync.go only knew the kernel's LED class (/sys/class/leds). The TrimUI
// Smart Pro and Brick light their stick rings through Allwinner's own driver
// instead, /sys/class/led_anim, which the class scan never sees — so on the
// handheld the lights never moved. The driver's help text (in the directory on
// the device) describes it:
//
//	max_scale            brightness 0..60 on the tg5040 (the whole bar)
//	max_scale_lr         the same for the left/right rings, where it exists
//	effect_lr            effect of both stick rings: 0 disable, 1 linear,
//	                     2 breath, 3 sniff, 4 static, 5–7 blink (effect_names)
//	effect_rgb_hex_lr    target colour "RRGGBB " (ends in a space)
//	effect_cycles_lr     -1 repeats for ever
//
// A colour only takes when the effect is re-armed (written through 0), and a
// re-arm 25 times a second would flicker, so the writer splits a colour into
// its hue — written as effect_rgb_hex_lr only when it changes, and no more
// often than ledAnimRearmEvery — and its level, written to the scale file every
// frame it changes. A level is what music and voice mostly move.
//
// What the rings showed before JukaHub took them (colour, effect, scale) is
// read on the first write and put back when the sync stops, so the firmware's
// own ring colour comes back instead of a dark ring.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ledAnimDir is the TrimUI LED driver.
	ledAnimDir = "/sys/class/led_anim"
	// ledAnimScaleLimit is the driver's own brightness ceiling on the tg5040.
	ledAnimScaleLimit = 60
	// ledAnimStaticDefault is effect 4 ("static") when effect_names cannot be
	// read.
	ledAnimStaticDefault = 4
	// ledAnimRearmEvery bounds how often a hue change re-arms the effect.
	ledAnimRearmEvery = 300 * time.Millisecond
	// ledAnimReassertEvery is how often the writer checks that nothing else
	// (TrimUI's hardwareservice rewrites max_scale and the effect on charge,
	// wake and its own LED setting) took the rings back, and repaints them.
	// It was 2 s, and the rings were reported blinking "every 2 seconds" even
	// after the check stopped repainting what had not changed: a repaint goes
	// through effect 0, so whatever made a check see a change — a firmware
	// service on its own timer, a node that reads back oddly now and then —
	// showed as a blink on the check's own beat.
	ledAnimReassertEvery = 10 * time.Second
	// ledAnimFightEvery is the check interval once ledAnimFightLimit checks in
	// a take have found the rings changed by someone else.
	ledAnimFightEvery = 60 * time.Second
	// ledAnimFightLimit is how many repaints a take allows on the short
	// interval. The count is never reset by a quiet check: something that
	// takes the rings back every other check would otherwise keep JukaHub
	// repainting (blinking) on the short interval for ever.
	ledAnimFightLimit = 2
)

// ledAnimState is what the writer remembers about the rings. Only the LED
// loop's goroutine calls applyLED and releaseLED, but SetLEDSync can start a
// new loop while the old one blanks, so it is locked.
var ledAnimState struct {
	sync.Mutex
	taken     bool
	saved     ledAnimSaved
	static    int
	scaleFile string
	scaleMax  int
	hue       [3]uint8
	hueSet    bool
	rearmedAt time.Time
	lastScale int
	checkedAt time.Time

	// noColorBack / noEffectBack are set once a node read back something
	// other than what was just written to it (a different format, or a
	// driver that reports another value): that node is then never used to
	// decide the rings drifted, or every check would re-arm them — a blink
	// every ReassertEvery.
	// noSwitchBack and noScaleBack do the same for the enable switches and
	// the scale node.
	noColorBack, noEffectBack bool
	noSwitchBack, noScaleBack bool
	scaleChecked              bool

	// fights counts the checks in this take that found the rings changed by
	// someone else; fightLogged keeps that log line to one per take.
	fights      int
	fightLogged bool

	// switchOff lists the switches a check found off; the repaint turns
	// it back on together with the effect, never in the check itself (a write
	// to enable may restart the driver's effect: one more blink).
	switchOff []string
}

// ledAnimSaved is the rings' state before the first write, switches included
// (rings the stock settings turned off stay off after JukaHub).
type ledAnimSaved struct {
	color, effect, scale  string
	cycles, enable, effEn string
}

// ledAnimFiles are the driver nodes JukaHub writes.
var ledAnimFiles = []string{
	"effect_rgb_hex_lr", "effect_lr", "effect_cycles_lr", "max_scale",
	"max_scale_lr", "enable", "effect_enable",
}

// ledAnimUnlock makes the driver's nodes writable. The firmware leaves them
// read-only (0444), and sysfs refuses a write open on a node without any write
// bit even for root — which is why every CFW starts with
// `chmod -R 777 /sys/class/led_anim`. Without this the rings were never found
// ("the driver is there, but JukaHub can't write to it").
func ledAnimUnlock(dir string) {
	for _, name := range ledAnimFiles {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o200 != 0 {
			continue
		}
		_ = os.Chmod(path, info.Mode().Perm()|0o600)
	}
}

// classifyLEDAnim reports the TrimUI rings as one device when the driver is
// there and writable.
func classifyLEDAnim(dir string) (ledDevice, bool) {
	if _, err := os.Stat(filepath.Join(dir, "effect_lr")); err != nil {
		return ledDevice{}, false
	}
	ledAnimUnlock(dir)
	if !canWriteFile(filepath.Join(dir, "effect_rgb_hex_lr")) || !canWriteFile(filepath.Join(dir, "effect_lr")) {
		return ledDevice{}, false
	}
	// The level goes through a scale file; without one the rings could only
	// change colour.
	if !canWriteFile(filepath.Join(dir, "max_scale_lr")) && !canWriteFile(filepath.Join(dir, "max_scale")) {
		return ledDevice{}, false
	}
	return ledDevice{Name: "stick rings", Dir: dir, Mode: ledModeAnim, Max: ledAnimScaleLimit}, true
}

// ledAnimSplit divides a colour into its hue at full value and a level 0..1.
// Pure.
func ledAnimSplit(r, g, b uint8) (hue [3]uint8, level float64) {
	peak := r
	if g > peak {
		peak = g
	}
	if b > peak {
		peak = b
	}
	if peak == 0 {
		return [3]uint8{}, 0
	}
	f := 255 / float64(peak)
	scale := func(v uint8) uint8 {
		out := math.Round(float64(v) * f)
		if out > 255 {
			out = 255
		}
		return uint8(out)
	}
	return [3]uint8{scale(r), scale(g), scale(b)}, float64(peak) / 255
}

// ledAnimHueTolerance is how far (per channel, at full value) a hue may move
// before the rings are re-armed. A dimmed colour's hue wobbles with rounding —
// Pulse and Charging run a 2 s brightness cycle, and the old 16-step snap
// flipped at a step edge once per cycle — and every re-arm goes through effect
// 0, which the user sees as a blink.
const ledAnimHueTolerance = 40

// ledAnimHueNear reports whether two hues are the same colour for the rings.
// Pure.
func ledAnimHueNear(a, b [3]uint8) bool {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < -ledAnimHueTolerance || d > ledAnimHueTolerance {
			return false
		}
	}
	return true
}

// ledAnimStaticIndex finds "static" in the driver's effect_names, whatever its
// layout: "4: static" or "static 4" (on one line with the others or not), or
// bare names in index order. Never 0, which is "disable"; unsure means 4. Pure.
func ledAnimStaticIndex(names string) int {
	toks := strings.FieldsFunc(strings.ToLower(names), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	numbered := false
	for _, t := range toks {
		if _, err := strconv.Atoi(t); err == nil {
			numbered = true
			break
		}
	}
	valid := func(t string) (int, bool) {
		n, err := strconv.Atoi(t)
		return n, err == nil && n > 0 && n < 16
	}
	pos := 0
	for i, t := range toks {
		if t != "static" {
			if _, err := strconv.Atoi(t); err != nil {
				pos++
			}
			continue
		}
		if numbered {
			// "4: static" lists the number first, "static 4" after the name;
			// the list's first token says which.
			_, errFirst := strconv.Atoi(toks[0])
			numFirst := errFirst == nil
			if numFirst && i > 0 {
				if n, ok := valid(toks[i-1]); ok {
					return n
				}
			}
			if !numFirst && i+1 < len(toks) {
				if n, ok := valid(toks[i+1]); ok {
					return n
				}
			}
			return ledAnimStaticDefault
		}
		if pos > 0 && pos < 16 {
			return pos
		}
		return ledAnimStaticDefault
	}
	return ledAnimStaticDefault
}

func ledAnimRead(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ledAnimDrifted reports whether the driver no longer shows JukaHub's look:
// the master switch went off, the effect is not static, or the colour is not
// hue. A node that cannot be read or parsed counts as unchanged, so an odd
// read-back format never makes the rings re-arm (and flicker) every check.
// Under lock.
func ledAnimDrifted(dir string, hue [3]uint8, static int) (drifted bool, what string) {
	st := &ledAnimState
	st.switchOff = st.switchOff[:0]
	if !st.noSwitchBack {
		for _, sw := range []string{"enable", "effect_enable"} {
			if ledAnimRead(dir, sw) != "0" {
				continue
			}
			// Turned back on by the repaint (applyLEDAnim), which also finds
			// out whether the write sticks.
			st.switchOff = append(st.switchOff, sw)
			drifted, what = true, sw+"=0"
		}
	}
	if !st.noEffectBack {
		raw := ledAnimRead(dir, "effect_lr")
		if n, err := strconv.Atoi(raw); err == nil && n != static {
			drifted, what = true, "effect_lr="+raw
		}
	}
	if !st.noColorBack {
		raw := ledAnimRead(dir, "effect_rgb_hex_lr")
		if r, g, b, ok := ledAnimParseHex(raw); ok && !ledAnimHueNear([3]uint8{r, g, b}, hue) {
			drifted, what = true, "effect_rgb_hex_lr="+raw
		}
	}
	return drifted, what
}

// ledAnimVerify reads the colour and effect back right after a re-arm. A node
// that does not report what was just written is unusable as a drift signal
// (see noColorBack). Under lock.
func ledAnimVerify(dir string, hue [3]uint8, static int) {
	st := &ledAnimState
	if !st.noEffectBack {
		raw := ledAnimRead(dir, "effect_lr")
		if n, err := strconv.Atoi(raw); err == nil && n != static {
			st.noEffectBack = true
			LogScene("led").Info("stick rings: effect_lr reads back differently, not watching it", "wrote", static, "read", raw)
		}
	}
	if !st.noColorBack {
		raw := ledAnimRead(dir, "effect_rgb_hex_lr")
		if r, g, b, ok := ledAnimParseHex(raw); ok && !ledAnimHueNear([3]uint8{r, g, b}, hue) {
			st.noColorBack = true
			LogScene("led").Info("stick rings: colour reads back differently, not watching it", "wrote", fmt.Sprintf("%02X%02X%02X", hue[0], hue[1], hue[2]), "read", raw)
		}
	}
}

// ledAnimScaleDrifted reports whether the scale node no longer holds what
// JukaHub wrote. Unreadable counts as unchanged: a blind rewrite every check
// could restart the driver's effect, which shows as a blink. Under lock.
func ledAnimScaleDrifted(dir string) bool {
	st := &ledAnimState
	if st.lastScale < 0 || st.noScaleBack {
		return false
	}
	n, err := strconv.Atoi(ledAnimRead(dir, st.scaleFile))
	return err == nil && n != st.lastScale
}

// ledAnimParseHex reads the first colour of a read-back like "FF0000 " or
// "0xff0000". Pure.
func ledAnimParseHex(raw string) (r, g, b uint8, ok bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, 0, 0, false
	}
	s := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(fields[0]), "0x"), "#")
	return parseLEDHex(s)
}

// ledAnimTake reads what the rings show and prepares the driver. Under lock.
func ledAnimTake(dir string) {
	st := &ledAnimState
	// The modes can be reset by a suspend or a firmware service.
	ledAnimUnlock(dir)
	st.taken = true
	st.hueSet = false
	st.lastScale = -1
	st.checkedAt = time.Now()
	st.fights, st.fightLogged = 0, false
	st.switchOff = st.switchOff[:0]
	st.static = ledAnimStaticIndex(ledAnimRead(dir, "effect_names"))
	st.scaleFile = "max_scale"
	if canWriteFile(filepath.Join(dir, "max_scale_lr")) {
		st.scaleFile = "max_scale_lr"
	}
	st.saved = ledAnimSaved{
		color:  ledAnimRead(dir, "effect_rgb_hex_lr"),
		effect: ledAnimRead(dir, "effect_lr"),
		scale:  ledAnimRead(dir, st.scaleFile),
		cycles: ledAnimRead(dir, "effect_cycles_lr"),
		enable: ledAnimRead(dir, "enable"),
		effEn:  ledAnimRead(dir, "effect_enable"),
	}
	// The firmware's own brightness is the ceiling (the user set it in the
	// stock settings); an unset or unreadable one gets the driver's limit.
	st.scaleMax = ledAnimScaleLimit
	if n, err := strconv.Atoi(st.saved.scale); err == nil && n > 0 && n < ledAnimScaleLimit {
		st.scaleMax = n
	}
	if st.scaleMax < 20 {
		// A ring the user dimmed almost to nothing would make the sync look
		// broken; it still never goes past the driver's limit.
		st.scaleMax = 20
	}
	_ = writeLEDFile(filepath.Join(dir, "effect_cycles_lr"), "-1")
	_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "1")
	_ = writeLEDFile(filepath.Join(dir, "enable"), "1")
	// What the driver showed, once per take: if the rings still blink, this
	// line (with the "reads back" and "keeps changing" ones) says why.
	LogScene("led").Info("stick rings taken", "effect", st.saved.effect, "static", st.static,
		"cycles", st.saved.cycles, "duration", ledAnimRead(dir, "effect_duration_lr"),
		"scale", st.saved.scale, "scaleFile", st.scaleFile)
}

// applyLEDAnim writes one colour to the rings.
func applyLEDAnim(dev ledDevice, r, g, b uint8) error {
	st := &ledAnimState
	st.Lock()
	defer st.Unlock()
	if !st.taken {
		ledAnimTake(dev.Dir)
	}
	hue, level := ledAnimSplit(r, g, b)
	scale := 0
	if level > 0 {
		scale = int(math.Round(level * float64(st.scaleMax)))
		if scale < 1 {
			scale = 1
		}
	}
	now := time.Now()
	drifted := false
	every := ledAnimReassertEvery
	if st.fights >= ledAnimFightLimit {
		// Something keeps taking the rings back on its own timer: winning
		// every time would be a blink on every check, so check rarely instead.
		every = ledAnimFightEvery
	}
	if now.Sub(st.checkedAt) >= every && st.hueSet && level > 0 {
		// A steady colour never changes level or hue, so without this a
		// hardwareservice write would stick until the user changed something.
		// Only what reads back changed is repainted: nothing is rewritten
		// blindly.
		st.checkedAt = now
		var what string
		drifted, what = ledAnimDrifted(dev.Dir, st.hue, st.static)
		if ledAnimScaleDrifted(dev.Dir) {
			st.lastScale = -1
			if what == "" {
				what = st.scaleFile
			}
		}
		if what != "" {
			st.fights++
			if st.fights >= ledAnimFightLimit && !st.fightLogged {
				st.fightLogged = true
				LogScene("led").Warn("stick rings: something else keeps changing them", "node", what)
			}
		}
	}
	changed := !st.hueSet || !ledAnimHueNear(hue, st.hue)
	if level > 0 && (drifted || (changed && (!st.hueSet || now.Sub(st.rearmedAt) >= ledAnimRearmEvery))) {
		for _, sw := range st.switchOff {
			_ = writeLEDFile(filepath.Join(dev.Dir, sw), "1")
			if ledAnimRead(dev.Dir, sw) == "0" {
				// A write that does not stick (or a node that always reads
				// 0) would repaint the rings at every check.
				st.noSwitchBack = true
			}
		}
		st.switchOff = st.switchOff[:0]
		col := fmt.Sprintf("%02X%02X%02X ", hue[0], hue[1], hue[2])
		if err := writeLEDFile(filepath.Join(dev.Dir, "effect_rgb_hex_lr"), col); err != nil {
			return err
		}
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), "0")
		if err := writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), strconv.Itoa(st.static)); err != nil {
			return err
		}
		st.hue, st.hueSet, st.rearmedAt = hue, true, now
		ledAnimVerify(dev.Dir, hue, st.static)
		// A driver may reset the scale with the effect: write it again.
		st.lastScale = -1
	}
	if scale == st.lastScale {
		return nil
	}
	if err := writeLEDFile(filepath.Join(dev.Dir, st.scaleFile), strconv.Itoa(scale)); err != nil {
		return err
	}
	st.lastScale = scale
	if !st.scaleChecked && !st.noScaleBack {
		// Once: a driver that clamps or rescales the value would otherwise
		// look changed at every check and get rewritten every 2 s.
		st.scaleChecked = true
		raw := ledAnimRead(dev.Dir, st.scaleFile)
		if n, err := strconv.Atoi(raw); err == nil && n != scale {
			st.noScaleBack = true
			LogScene("led").Info("stick rings: scale reads back differently, not watching it", "wrote", scale, "read", raw)
		}
	}
	return nil
}

// releaseLEDAnim puts back what the rings showed before the first write. A
// colour that could not be read is left dark.
func releaseLEDAnim(dev ledDevice) {
	st := &ledAnimState
	st.Lock()
	defer st.Unlock()
	if !st.taken {
		return
	}
	st.taken = false
	saved := st.saved
	if saved.color != "" {
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_rgb_hex_lr"), saved.color+" ")
	} else {
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_rgb_hex_lr"), "000000 ")
	}
	_ = writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), "0")
	if saved.effect != "" && saved.effect != "0" {
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), saved.effect)
	}
	if saved.scale != "" {
		_ = writeLEDFile(filepath.Join(dev.Dir, st.scaleFile), saved.scale)
	}
	for _, sw := range []struct{ file, value string }{
		{"effect_cycles_lr", saved.cycles},
		{"effect_enable", saved.effEn},
		{"enable", saved.enable},
	} {
		if sw.value != "" {
			_ = writeLEDFile(filepath.Join(dev.Dir, sw.file), sw.value)
		}
	}
}
