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
}

// ledAnimSaved is the rings' state before the first write, switches included
// (rings the stock settings turned off stay off after JukaHub).
type ledAnimSaved struct {
	color, effect, scale  string
	cycles, enable, effEn string
}

// classifyLEDAnim reports the TrimUI rings as one device when the driver is
// there and writable.
func classifyLEDAnim(dir string) (ledDevice, bool) {
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
		// Snap to 16 steps so a hue that only wobbles by rounding never re-arms.
		q := int(out+8) / 16 * 16
		if q > 255 {
			q = 255
		}
		return uint8(q)
	}
	return [3]uint8{scale(r), scale(g), scale(b)}, float64(peak) / 255
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

// ledAnimTake reads what the rings show and prepares the driver. Under lock.
func ledAnimTake(dir string) {
	st := &ledAnimState
	st.taken = true
	st.hueSet = false
	st.lastScale = -1
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
	if level > 0 && (!st.hueSet || hue != st.hue) && (!st.hueSet || now.Sub(st.rearmedAt) >= ledAnimRearmEvery) {
		col := fmt.Sprintf("%02X%02X%02X ", hue[0], hue[1], hue[2])
		if err := writeLEDFile(filepath.Join(dev.Dir, "effect_rgb_hex_lr"), col); err != nil {
			return err
		}
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), "0")
		if err := writeLEDFile(filepath.Join(dev.Dir, "effect_lr"), strconv.Itoa(st.static)); err != nil {
			return err
		}
		st.hue, st.hueSet, st.rearmedAt = hue, true, now
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
