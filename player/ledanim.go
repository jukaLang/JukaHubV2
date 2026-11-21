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
//	effect_enable        "toggle of anim to function"
//	frame_hex            one frame for all 23 LEDs, "RRGGBB RRGGBB … " (ends
//	                     in a space)
//
// The rings blinked, and kept blinking through three rounds of fixes, because
// of how a colour reaches them. A new effect colour only shows once the effect
// is re-armed, and the usual way to re-arm writes the
// effect through 0 — "disable", a dark ring — before "static". Every colour
// change was therefore a dark frame on a lit ring, and so was every repaint
// after something else touched the driver. So the writer now never re-arms a
// lit ring:
//
//   - A colour change on the re-arm path ("rearm") first dips the scale to 0
//     over ledAnimDipDownSec, re-arms while the ring is already dark, and
//     brings the scale back over ledAnimDipUpSec: a soft dip, not a blink.
//   - If frame_hex reads back what the effect engine shows (the "oracle"), the
//     driver's own frame is a live picture of the rings, and two dark-free
//     paths can be proven by reading it back a moment after a write: driving
//     the rings through frame_hex with the effects off ("frame", how LED'oh
//     animates them — no effect engine, no cycle, no re-arm ever), or writing
//     the colour and then "static" without the 0 ("direct"). A path is only
//     used once a read-back showed it take; one that did not falls back, and
//     the log says which path the rings use and why.
//   - Nothing is repainted in steady state. A check every ledAnimCheckEvery
//     compares the nodes with what they read back right after JukaHub's own
//     write; a real change by something else (TrimUI's hardwareservice on
//     charge or wake) is painted over once, softly, and after
//     ledAnimRepaintLimit of them in one take the writer yields instead of
//     fighting a firmware timer with a repaint every minute.
//
// The level (what music and voice mostly move) is the scale node, written only
// when it changes; it never re-arms anything.
//
// What the rings showed before JukaHub took them (colour, effect, scale) is
// read on the first write and put back when the sync stops, so the firmware's
// own ring colour comes back instead of a dark ring.
//
// The _lr names above are one zone. The models light different things (the
// Brick's top bar, FN keys and shoulders; the Smart Pro's logo; the Smart Pro
// S and Brick Pro's stick rings), so every zone is read from the directory
// (ledAnimZones) and gets the same colour; nothing is assumed from the model
// and a node that is not there is never written.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// ledAnimFrameNode is the driver's direct frame, ledAnimFrameLEDs the
	// number of LEDs a frame holds when the node cannot be read (the help
	// text's 23; the Brick has 14 lit, the rest are ignored).
	ledAnimFrameNode = "frame_hex"
	ledAnimFrameLEDs = 23
	// ledAnimDipDownSec and ledAnimDipUpSec are the soft dip a colour change
	// on the re-arm path goes through: the scale's way down to dark, and back
	// up after the re-arm. At 25 frames a second they are five steps down and
	// eight up; with three (0.12 s) most of the drop the eye sees falls in
	// the last step, which looks more like a blink than a dip.
	ledAnimDipDownSec = 0.2
	ledAnimDipUpSec   = 0.3
	// ledAnimCheckEvery is how often steady rings are read back, to notice
	// that something else (TrimUI's hardwareservice rewrites the effect and
	// max_scale on charge, wake and its own LED setting) took them. Reading
	// costs nothing visible; only a confirmed change repaints.
	ledAnimCheckEvery = 10 * time.Second
	// ledAnimRepaintLimit is how many such changes one take paints over.
	// After that the writer yields: something on a timer of its own would
	// otherwise get a repaint (a dip) after every one of its writes.
	ledAnimRepaintLimit = 2
	// ledAnimVerifyAfter is how long a write settles before frame_hex is read
	// back to prove a path (the driver animates at 60 fps).
	ledAnimVerifyAfter = 80 * time.Millisecond
	// ledAnimProbeMinScale is the least scale at which a frame read-back is
	// judged: dimmer, the channels are too small to tell a hue.
	ledAnimProbeMinScale = 4
	// ledAnimLogEvery rate-limits the colour-change log line per reason.
	ledAnimLogEvery = 30 * time.Second
)

// The ways a colour reaches the rings (see the file comment); the names are
// what the log says.
const (
	ledAnimPathRearm  = "rearm"
	ledAnimPathDirect = "direct"
	ledAnimPathFrame  = "frame"
)

// ledAnimProbe is the read-back a write is waiting for.
type ledAnimProbe int

const (
	ledAnimProbeNone   ledAnimProbe = iota
	ledAnimProbeOracle              // after a re-arm: does frame_hex show the rings?
	ledAnimProbeFrame               // after switching to frame_hex: do the rings keep the frame?
	ledAnimProbeDirect              // after a colour change without effect 0: did it take?
)

// ledAnimProven is what read-backs proved about the driver this run: 1 works,
// -1 does not, 0 not tried. It outlives a take (the driver does not change
// while JukaHub runs). Under ledAnimState's lock.
var ledAnimProven struct {
	oracle, frame, direct int8
}

// ledAnimHueCostly is set while the rings are driven and a colour change
// costs them a dip (only the re-arm path is proven): the modes that would
// change colour all the time (Music, Rainbow, Screen colours) change it less
// then. Any goroutine.
var ledAnimHueCostly atomic.Bool

// ledAnimState is what the writer remembers about the rings. Only the LED
// loop's goroutine calls applyLED and releaseLED, but SetLEDSync can start a
// new loop while the old one blanks, so it is locked.
var ledAnimState struct {
	sync.Mutex
	taken  bool
	saved  ledAnimSaved
	static int
	// zones are the lit areas found in the driver (ledAnimZones); the first
	// one is the primary, the one whose read-back stands for all of them.
	zones []ledAnimZone
	// scaleFile is the primary zone's scale node; scales are every distinct
	// scale node with its own ceiling, all written with the same level.
	scaleFile string
	scaleMax  int
	scales    []ledAnimScale
	// frameOK: frame_hex is there and writable; frameSeen: it is there at
	// all (it can still prove a path read-only); frameLEDs is how many
	// entries a frame holds.
	frameOK, frameSeen bool
	frameLEDs          int
	// fill is the ledAnimFill the colour was last landed with: the share of
	// the LEDs (on the frame path) or of the zones lit, 0 for all of them.
	fill int32

	// hue is what the rings show at full value, landed by the last colour
	// write; hueSet is false until the first one of a take.
	hue    [3]uint8
	hueSet bool
	// lastScale is the primary scale node as last written, -1 unknown.
	lastScale int
	// frameAt is the previous call's time: the dip's clock.
	frameAt time.Time
	// dipping: a colour change on the re-arm path is waiting for the scale
	// to reach 0; rising: the scale is on its way back up after it. scaleF is
	// the scale during either, unrounded.
	dipping, rising bool
	scaleF          float64
	// byFrame: the rings are driven through frame_hex; frameArmed: the
	// effects are off for it (a take, or something else re-arming one,
	// clears it).
	byFrame, frameArmed bool
	// probe is a read-back due at probeAt.
	probe   ledAnimProbe
	probeAt time.Time

	// The steady check: baseline is what the watched nodes read back once
	// JukaHub's last colour write settled (nil until then), so a driver that
	// reports values in its own format never looks changed.
	// scaleBack is 1 when the scale node reads back what was written, -1
	// when it does not (then it is not watched), 0 untested this take.
	checkedAt time.Time
	baseline  []string
	scaleBack int8
	// repaints counts the changes by something else painted over this take;
	// yielded: the limit was reached and the check stopped.
	repaints int
	yielded  bool
	// land forces the next frame to write the colour again (a repaint, or a
	// direct write that did not take); why is the reason the log gives.
	land bool
	why  string

	// logAt and logSkipped rate-limit the colour-change line per reason.
	logAt      map[string]time.Time
	logSkipped map[string]int
}

// ledAnimSaved is the lights' state before the first write, switches included
// (lights the stock settings turned off stay off after JukaHub).
type ledAnimSaved struct {
	enable, effEn string
	zones         []ledAnimZoneSaved
}

// ledAnimZoneSaved is one zone's colour, effect and cycles before the take.
type ledAnimZoneSaved struct {
	color, effect, cycles string
}

// ledAnimScale is one brightness node and the ceiling JukaHub drives it to.
type ledAnimScale struct {
	file  string
	max   int
	saved string
}

// ledAnimZone is one lit area of the driver. The models differ, so the zones
// are read from the directory, never assumed from the model:
//
//	lr     both stick rings (Smart Pro, Smart Pro S, Brick Pro) or the
//	       shoulder lights (Brick); l / r are the rings one at a time
//	m      the top bar (Brick) or the logo (Smart Pro)
//	f1 f2  the Brick's FN-key lights
type ledAnimZone struct {
	name  string // the node suffix: "lr"
	scale string // its brightness node: "max_scale_lr", "max_scale_f1f2", "max_scale"
}

// ledAnimNodeKept reports whether a node is one JukaHub writes and so must be
// writable: the effect, colour and cycle nodes of any zone, the scale nodes,
// the two switches and the frame. Pure.
func ledAnimNodeKept(name string) bool {
	switch name {
	case "enable", "effect_enable", ledAnimFrameNode:
		return true
	}
	return strings.HasPrefix(name, "effect_rgb_hex_") || strings.HasPrefix(name, "effect_cycles_") ||
		strings.HasPrefix(name, "max_scale") ||
		(strings.HasPrefix(name, "effect_") && !strings.HasPrefix(name, "effect_duration_") && name != "effect_names")
}

// ledAnimUnlock makes the driver's nodes writable. The firmware leaves them
// read-only (0444), and sysfs refuses a write open on a node without any write
// bit even for root — which is why every CFW starts with
// `chmod -R 777 /sys/class/led_anim`. Without this the rings were never found
// ("the driver is there, but JukaHub can't write to it").
func ledAnimUnlock(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !ledAnimNodeKept(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o200 != 0 {
			continue
		}
		_ = os.Chmod(path, info.Mode().Perm()|0o600)
	}
}

// ledAnimZoneNames picks the zones from a directory listing: every suffix z
// with both effect_<z> and effect_rgb_hex_<z>. l and r are left out when lr
// drives both (writing all three would re-arm the rings twice). lr comes
// first, the rest in name order. Pure.
func ledAnimZoneNames(files []string) []string {
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f] = true
	}
	var zones []string
	for _, f := range files {
		z, ok := strings.CutPrefix(f, "effect_rgb_hex_")
		if !ok || z == "" || !have["effect_"+z] {
			continue
		}
		if (z == "l" || z == "r") && have["effect_rgb_hex_lr"] && have["effect_lr"] {
			continue
		}
		zones = append(zones, z)
	}
	sort.Slice(zones, func(i, j int) bool {
		if (zones[i] == "lr") != (zones[j] == "lr") {
			return zones[i] == "lr"
		}
		return zones[i] < zones[j]
	})
	return zones
}

// ledAnimZones lists the zones dir has that JukaHub can write, each with the
// scale node that dims it. A zone without a writable colour, effect or scale
// node is left out (and never written).
func ledAnimZones(dir string) []ledAnimZone {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}
	var out []ledAnimZone
	for _, z := range ledAnimZoneNames(files) {
		if !canWriteFile(filepath.Join(dir, "effect_rgb_hex_"+z)) || !canWriteFile(filepath.Join(dir, "effect_"+z)) {
			continue
		}
		cands := []string{"max_scale_" + z}
		if z == "f1" || z == "f2" {
			cands = append(cands, "max_scale_f1f2")
		}
		if z == "l" || z == "r" {
			cands = append(cands, "max_scale_lr")
		}
		cands = append(cands, "max_scale")
		for _, c := range cands {
			if canWriteFile(filepath.Join(dir, c)) {
				out = append(out, ledAnimZone{name: z, scale: c})
				break
			}
		}
	}
	return out
}

// ledAnimZoneList is the zones' names for a log line.
func ledAnimZoneList(zones []ledAnimZone) string {
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, z.name)
	}
	return strings.Join(names, ",")
}

// classifyLEDAnim reports the TrimUI lights as one device when the driver is
// there with at least one writable zone.
func classifyLEDAnim(dir string) (ledDevice, bool) {
	if _, err := os.Stat(dir); err != nil {
		return ledDevice{}, false
	}
	ledAnimUnlock(dir)
	zones := ledAnimZones(dir)
	if len(zones) == 0 {
		ledAnimNoZonesOnce.Do(func() {
			LogScene("led").Info("led_anim has no writable zone; lights not driven", "dir", dir)
		})
		return ledDevice{}, false
	}
	return ledDevice{Name: "led_anim " + ledAnimZoneList(zones), Dir: dir, Mode: ledModeAnim, Max: ledAnimScaleLimit}, true
}

// ledAnimNoZonesOnce keeps the "no zone" line to one per run: the Settings
// page asks again on every visit.
var ledAnimNoZonesOnce sync.Once

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
// before the rings get the new colour. A dimmed colour's hue wobbles with
// rounding — Pulse and Charging run a brightness cycle — and on the re-arm
// path every colour change is a dip.
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

// ledAnimPrimary is the zone whose read-back stands for all of them: lr when
// the driver has it. Under lock.
func ledAnimPrimary() string {
	if len(ledAnimState.zones) == 0 {
		return "lr"
	}
	return ledAnimState.zones[0].name
}

// ledAnimParseFrameToken reads one LED of a frame: "RRGGBB", "XXRRGGBB"
// (the help text calls the frame XRGB), with or without "0x" or "#". Pure.
func ledAnimParseFrameToken(tok string) (r, g, b uint8, ok bool) {
	s := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tok)), "0x"), "#")
	if len(s) == 8 {
		s = s[2:]
	}
	return parseLEDHex(s)
}

// ledAnimFrameText is one frame of n LEDs in hue, in frame_hex's format.
// Pure.
func ledAnimFrameText(hue [3]uint8, n int) string {
	if n <= 0 {
		n = ledAnimFrameLEDs
	}
	return strings.Repeat(fmt.Sprintf("%02X%02X%02X ", hue[0], hue[1], hue[2]), n)
}

// ledAnimFill is the share of the lights a layer wants lit, in thousandths:
// the battery level and the charging bar (ledbattlevel.go). 0 lights them all.
// The LED loop sets it before each write; a change lands the colour again
// (applyLEDAnim). Any goroutine.
var ledAnimFill atomic.Int32

// ledAnimLitCount is how many of n LEDs or zones a fill lights: all for 0, at
// least one otherwise. Pure.
func ledAnimLitCount(fill int32, n int) int {
	if fill <= 0 || fill >= 1000 || n <= 0 {
		return n
	}
	lit := (int(fill)*n + 999) / 1000
	return min(max(lit, 1), n)
}

// ledAnimFrameTextFill is ledAnimFrameText with only the first LEDs of the
// fill lit and the rest dark. Pure.
func ledAnimFrameTextFill(hue [3]uint8, n int, fill int32) string {
	if n <= 0 {
		n = ledAnimFrameLEDs
	}
	lit := ledAnimLitCount(fill, n)
	if lit >= n {
		return ledAnimFrameText(hue, n)
	}
	return ledAnimFrameText(hue, lit) + strings.Repeat("000000 ", n-lit)
}

// ledAnimFrameMatch judges a frame_hex read-back against a hue: readable when
// at least one LED parsed, shows when the lit LEDs are mostly that hue (the
// Brick's frame has unlit slots, and a level too low to hold a hue is not
// counted). Pure.
func ledAnimFrameMatch(raw string, hue [3]uint8) (shows, readable bool, leds int) {
	lit, near := 0, 0
	for _, tok := range strings.Fields(raw) {
		r, g, b, ok := ledAnimParseFrameToken(tok)
		if !ok {
			continue
		}
		leds++
		if max(r, g, b) < 10 {
			continue
		}
		lit++
		if h, _ := ledAnimSplit(r, g, b); ledAnimHueNear(h, hue) {
			near++
		}
	}
	return lit > 0 && near*2 >= lit, leds > 0, leds
}

// ledAnimLog is the rate-limited line for every colour write: what path it
// took and why, at most once per ledAnimLogEvery for each reason, with how
// many were left out. Under lock.
func ledAnimLog(path, why, hex string) {
	st := &ledAnimState
	key := path + "/" + why
	if i := strings.IndexByte(why, ' '); i > 0 {
		key = path + "/" + why[:i]
	}
	if st.logAt == nil {
		st.logAt = make(map[string]time.Time, 8)
		st.logSkipped = make(map[string]int, 8)
	}
	now := time.Now()
	if at, ok := st.logAt[key]; ok && now.Sub(at) < ledAnimLogEvery && !now.Before(at) {
		st.logSkipped[key]++
		return
	}
	st.logAt[key] = now
	skipped := st.logSkipped[key]
	st.logSkipped[key] = 0
	LogScene("led").Info("stick rings: colour written", "path", path, "why", why, "colour", hex, "alsoSince", skipped)
}

// ledAnimTake reads what the rings show and prepares the driver. Under lock.
func ledAnimTake(dir string) {
	st := &ledAnimState
	// The modes can be reset by a suspend or a firmware service.
	ledAnimUnlock(dir)
	st.taken = true
	st.hueSet = false
	st.lastScale = -1
	st.frameAt = time.Time{}
	st.dipping, st.rising, st.scaleF = false, false, 0
	st.byFrame, st.frameArmed = false, false
	st.probe = ledAnimProbeNone
	st.checkedAt = time.Now()
	st.baseline = nil
	st.scaleBack = 0
	st.repaints, st.yielded = 0, false
	st.land, st.why = false, ""
	st.fill = 0
	st.static = ledAnimStaticIndex(ledAnimRead(dir, "effect_names"))
	// The zones are probed again on every take: a firmware update or a
	// suspend can change what is writable.
	st.zones = ledAnimZones(dir)
	st.saved = ledAnimSaved{
		enable: ledAnimRead(dir, "enable"),
		effEn:  ledAnimRead(dir, "effect_enable"),
		zones:  make([]ledAnimZoneSaved, len(st.zones)),
	}
	st.scales = st.scales[:0]
	st.scaleFile = ""
	for i, z := range st.zones {
		st.saved.zones[i] = ledAnimZoneSaved{
			color:  ledAnimRead(dir, "effect_rgb_hex_"+z.name),
			effect: ledAnimRead(dir, "effect_"+z.name),
			cycles: ledAnimRead(dir, "effect_cycles_"+z.name),
		}
		if i == 0 {
			st.scaleFile = z.scale
		}
		seen := false
		for _, s := range st.scales {
			if s.file == z.scale {
				seen = true
				break
			}
		}
		if !seen {
			saved := ledAnimRead(dir, z.scale)
			st.scales = append(st.scales, ledAnimScale{file: z.scale, max: ledAnimScaleCeiling(saved), saved: saved})
		}
		if canWriteFile(filepath.Join(dir, "effect_cycles_"+z.name)) {
			_ = writeLEDFile(filepath.Join(dir, "effect_cycles_"+z.name), "-1")
		}
	}
	st.scaleMax = ledAnimScaleLimit
	if len(st.scales) > 0 {
		st.scaleMax = st.scales[0].max
	}
	first := ledAnimZoneSaved{}
	if len(st.saved.zones) > 0 {
		first = st.saved.zones[0]
	}
	// How bright the firmware's look is now, so the first colour can dip out
	// of it instead of cutting to dark. Rings that are off already are made
	// dark at the scale before the switches go on, or turning the switches on
	// would flash the firmware's colour.
	dark := st.saved.enable == "0" || st.saved.effEn == "0" || first.effect == "" || first.effect == "0"
	if len(st.scales) > 0 {
		if n, err := strconv.Atoi(st.scales[0].saved); err == nil {
			st.lastScale = n
			dark = dark || n <= 0
		}
	}
	if dark && len(st.zones) > 0 {
		for _, s := range st.scales {
			_ = writeLEDFile(filepath.Join(dir, s.file), "0")
		}
		st.lastScale = 0
	}
	for _, sw := range []string{"effect_enable", "enable"} {
		// No zone to drive: the switches stay as the firmware left them.
		if len(st.zones) > 0 && canWriteFile(filepath.Join(dir, sw)) {
			_ = writeLEDFile(filepath.Join(dir, sw), "1")
		}
	}
	// The frame: how many LEDs it holds, read from the node itself.
	st.frameOK = canWriteFile(filepath.Join(dir, ledAnimFrameNode))
	st.frameLEDs = ledAnimFrameLEDs
	frameRaw := ledAnimRead(dir, ledAnimFrameNode)
	st.frameSeen = st.frameOK || frameRaw != ""
	if _, _, n := ledAnimFrameMatch(frameRaw, [3]uint8{}); n >= 8 {
		st.frameLEDs = n
	}
	p := &ledAnimProven
	st.byFrame = p.frame == 1 && st.frameOK
	ledAnimHueCostly.Store(!st.byFrame && p.direct != 1)
	// What the driver showed and how JukaHub will drive it, once per take:
	// if the rings still blink, this line (with the "colour written" and
	// "read-back" ones) says why.
	frame := "none"
	switch {
	case st.frameOK:
		frame = fmt.Sprintf("writable, %d LEDs read", st.frameLEDs)
	case frameRaw != "":
		frame = "read-only"
	}
	LogScene("led").Info("lights taken", "zones", ledAnimZoneList(st.zones), "effect", first.effect, "static", st.static,
		"cycles", first.cycles, "duration", ledAnimRead(dir, "effect_duration_"+ledAnimPrimary()),
		"scaleFile", st.scaleFile, "scaleMax", st.scaleMax, "scaleWas", st.lastScale, "frame", frame,
		"path", ledAnimPathFor(true), "proven", fmt.Sprintf("oracle=%d frame=%d direct=%d", p.oracle, p.frame, p.direct))
}

// ledAnimScaleCeiling is how bright JukaHub drives a scale node: the
// firmware's own setting (the user chose it in the stock settings) up to the
// driver's limit; unset or unreadable gets the limit. A light the user dimmed
// almost to nothing would make the sync look broken, so never below 20. Pure.
func ledAnimScaleCeiling(saved string) int {
	ceiling := ledAnimScaleLimit
	if n, err := strconv.Atoi(saved); err == nil && n > 0 && n < ledAnimScaleLimit {
		ceiling = n
	}
	if ceiling < 20 {
		ceiling = 20
	}
	return ceiling
}

// ledAnimPathFor is how the next colour change reaches the rings: the frame
// once proven (or while it is being proven), direct once proven, and the
// re-arm otherwise. plain is an ordinary colour change on rings JukaHub
// already painted static: only such a change may try direct for the first
// time, because from any other effect (the firmware's, or the frame's 0) a
// static write takes anyway and would prove nothing. Under lock.
func ledAnimPathFor(plain bool) string {
	st := &ledAnimState
	p := &ledAnimProven
	switch {
	case st.byFrame:
		return ledAnimPathFrame
	case p.direct == 1:
		return ledAnimPathDirect
	case plain && st.hueSet && p.oracle == 1 && p.direct == 0 && st.probe != ledAnimProbeDirect &&
		(!st.frameOK || p.frame == -1):
		// One unproven direct write at a time: while its read-back waits (a
		// level too dim to judge), more of them could leave the rings on an
		// old colour for as long as the level stays down.
		return ledAnimPathDirect
	}
	return ledAnimPathRearm
}

// ledAnimWriteFrame shows hue on every LED through frame_hex. The first frame
// of a take (or after something else armed an effect) turns the effects off
// first, as LED'oh does before it animates the LEDs itself: every zone's
// effect to 0 and effect_enable to 0; each frame is then followed by
// effect_enable 1, LED'oh's order. Under lock.
func ledAnimWriteFrame(dir string, hue [3]uint8) error {
	st := &ledAnimState
	if !st.frameArmed {
		for _, z := range st.zones {
			_ = writeLEDFile(filepath.Join(dir, "effect_"+z.name), "0")
		}
		_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "0")
		st.frameArmed = true
	}
	if err := writeLEDFile(filepath.Join(dir, ledAnimFrameNode), ledAnimFrameTextFill(hue, st.frameLEDs, st.fill)); err != nil {
		return err
	}
	_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "1")
	return nil
}

// ledAnimLand writes hue to the rings through path. On the re-arm path the
// caller has already dipped the scale to 0, so the effect's 0 is never seen.
// Under lock.
func ledAnimLand(dir string, hue [3]uint8, path, why string) error {
	st := &ledAnimState
	hex := fmt.Sprintf("%02X%02X%02X", hue[0], hue[1], hue[2])
	// A layer that lights only part of them (ledbattlevel.go): the first LEDs
	// of the frame, or the first zones, the primary one always.
	st.fill = ledAnimFill.Load()
	if strings.HasPrefix(why, "external") {
		// Something else may have switched the lights off: on again, here
		// rather than in the check (a switch write can restart the effect).
		for _, sw := range []string{"enable", "effect_enable"} {
			if canWriteFile(filepath.Join(dir, sw)) {
				_ = writeLEDFile(filepath.Join(dir, sw), "1")
			}
		}
	}
	if path == ledAnimPathFrame {
		if err := ledAnimWriteFrame(dir, hue); err != nil {
			// The effects may be off already, and a frame that does not get
			// through leaves nothing lit: back on the effect engine for this
			// take (an unproven frame for good), and the colour lands there
			// on the next frame.
			p := &ledAnimProven
			if p.frame == 0 {
				p.frame = -1
			}
			st.byFrame, st.frameArmed = false, false
			st.probe = ledAnimProbeNone
			_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "1")
			st.land, st.why = true, "frame write failed"
			ledAnimHueCostly.Store(p.direct != 1)
			return err
		}
	} else {
		lit := ledAnimLitCount(st.fill, len(st.zones))
		for i, z := range st.zones {
			col := hex + " "
			if i >= lit {
				col = "000000 "
			}
			// The primary zone's failure is the device's; another zone that
			// stops answering keeps the rest lit.
			if err := writeLEDFile(filepath.Join(dir, "effect_rgb_hex_"+z.name), col); err != nil && i == 0 {
				return err
			}
			if path == ledAnimPathRearm {
				_ = writeLEDFile(filepath.Join(dir, "effect_"+z.name), "0")
			}
			if err := writeLEDFile(filepath.Join(dir, "effect_"+z.name), strconv.Itoa(st.static)); err != nil && i == 0 {
				return err
			}
		}
	}
	st.hue, st.hueSet = hue, true
	st.land, st.why = false, ""
	// The nodes are read again (not judged) at the next check: what they
	// report now is JukaHub's own write.
	st.baseline = nil
	st.checkedAt = time.Now()
	p := &ledAnimProven
	// A read-back always judges the latest write: one left over from an
	// earlier write would compare frame_hex with this colour and could prove
	// a path that never ran.
	st.probe = ledAnimProbeNone
	switch {
	case path == ledAnimPathRearm && p.oracle == 0 && st.frameSeen:
		// A colour the effect engine drew: does frame_hex show it?
		st.probe = ledAnimProbeOracle
	case path == ledAnimPathFrame && p.frame == 0:
		// A colour change while the frame is still being proven.
		st.probe = ledAnimProbeFrame
	case path == ledAnimPathDirect && p.direct == 0:
		st.probe = ledAnimProbeDirect
	}
	if st.probe != ledAnimProbeNone {
		st.probeAt = time.Now().Add(ledAnimVerifyAfter)
	}
	ledAnimLog(path, why, hex)
	return nil
}

// ledAnimProbeDue reads frame_hex back once a write has settled and records
// what it proved. Under lock.
func ledAnimProbeDue(dir string, now time.Time) {
	st := &ledAnimState
	if st.probe == ledAnimProbeNone || now.Before(st.probeAt) || st.dipping || st.rising || st.land {
		return
	}
	if st.lastScale < ledAnimProbeMinScale {
		// Too dim to tell a hue; the next brighter frame judges it.
		return
	}
	raw := ledAnimRead(dir, ledAnimFrameNode)
	shows, readable, leds := ledAnimFrameMatch(raw, st.hue)
	sample := raw
	if len(sample) > 64 {
		sample = sample[:64]
	}
	hex := fmt.Sprintf("%02X%02X%02X", st.hue[0], st.hue[1], st.hue[2])
	p := &ledAnimProven
	lg := LogScene("led")
	probe := st.probe
	st.probe = ledAnimProbeNone
	switch probe {
	case ledAnimProbeOracle:
		if !shows {
			p.oracle = -1
			lg.Info("stick rings: frame_hex does not show the rings; colour changes dip through dark",
				"readable", readable, "leds", leds, "wanted", hex, "read", sample)
			return
		}
		p.oracle = 1
		lg.Info("stick rings: frame_hex shows the rings; trying colour changes without effect 0", "leds", leds, "frameWritable", st.frameOK)
		if st.frameOK && p.frame == 0 {
			st.byFrame = true
			if err := ledAnimWriteFrame(dir, st.hue); err != nil {
				p.frame = -1
				st.byFrame, st.frameArmed = false, false
				lg.Info("stick rings: frame_hex write failed; staying on effects", "err", err)
				_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "1")
				st.land, st.why = true, "frame write failed"
				return
			}
			st.baseline = nil
			st.probe, st.probeAt = ledAnimProbeFrame, now.Add(ledAnimVerifyAfter)
		}
	case ledAnimProbeFrame:
		if shows {
			p.frame = 1
			ledAnimHueCostly.Store(false)
			lg.Info("stick rings: driven through frame_hex (no effect, no re-arm)", "leds", leds)
			return
		}
		p.frame = -1
		lg.Info("stick rings: the frame did not hold; back to effects", "wanted", hex, "read", sample)
		// Back on the effect engine: the rings show the wrong thing or
		// nothing now, so this one re-arm is not a blink of a lit colour.
		st.byFrame, st.frameArmed = false, false
		_ = writeLEDFile(filepath.Join(dir, "effect_enable"), "1")
		st.land, st.why = true, "frame did not hold"
	case ledAnimProbeDirect:
		if shows {
			p.direct = 1
			ledAnimHueCostly.Store(false)
			lg.Info("stick rings: a colour takes without effect 0 (direct)")
			return
		}
		p.direct = -1
		lg.Info("stick rings: a colour needs effect 0 to take; colour changes dip through dark", "wanted", hex, "read", sample)
		// The rings still show the colour before: land it the safe way.
		st.land, st.why = true, "direct did not take"
	}
}

// ledAnimWatched is what the steady check reads, in a fixed order. Under
// lock.
func ledAnimWatched(dir string) []string {
	st := &ledAnimState
	z := ledAnimPrimary()
	out := []string{
		ledAnimRead(dir, "enable"),
		ledAnimRead(dir, "effect_"+z),
	}
	if !st.byFrame {
		// effect_enable is written with every frame; the effect's colour is
		// not the rings' while the frame drives them.
		out = append(out, ledAnimRead(dir, "effect_enable"), ledAnimRead(dir, "effect_rgb_hex_"+z))
	}
	return out
}

// ledAnimWatchedNames names ledAnimWatched's entries for the log. Under lock.
func ledAnimWatchedNames() []string {
	z := ledAnimPrimary()
	names := []string{"enable", "effect_" + z}
	if !ledAnimState.byFrame {
		names = append(names, "effect_enable", "effect_rgb_hex_"+z)
	}
	return names
}

// ledAnimCheck is the steady read-back: nothing is written unless a node
// changed since JukaHub's own write, and then the colour is painted once more
// (softly, like any colour change; only the level when only the scale moved)
// — at most ledAnimRepaintLimit times a take. Under lock.
func ledAnimCheck(dir string, now time.Time, level float64) {
	st := &ledAnimState
	if st.yielded || !st.hueSet || level <= 0 || st.dipping || st.rising || st.land || st.probe != ledAnimProbeNone {
		return
	}
	if st.baseline == nil {
		// What JukaHub's own write reads back as, taken as soon as it has
		// settled: a firmware service that writes the rings a moment later
		// (at boot, on wake) is then a change, not the baseline.
		if now.Sub(st.checkedAt) >= ledAnimVerifyAfter || now.Before(st.checkedAt) {
			st.baseline, st.checkedAt = ledAnimWatched(dir), now
		}
		return
	}
	if now.Sub(st.checkedAt) < ledAnimCheckEvery && !now.Before(st.checkedAt) {
		return
	}
	st.checkedAt = now
	cur := ledAnimWatched(dir)
	what := ""
	names := ledAnimWatchedNames()
	for i := range cur {
		if i < len(st.baseline) && i < len(names) && cur[i] != st.baseline[i] {
			what = names[i] + "=" + cur[i]
			break
		}
	}
	readScale, scaleErr := strconv.Atoi(ledAnimRead(dir, st.scaleFile))
	scaleOnly := false
	if what == "" && st.scaleBack == 1 && st.lastScale >= 0 && scaleErr == nil && readScale != st.lastScale {
		what = st.scaleFile + "=" + strconv.Itoa(readScale)
		scaleOnly = true
	}
	// The frame is judged only where the probes judge it: dimmer, a
	// frame_hex that reports the scaled LEDs has no lit entry left, which
	// is not a change.
	if what == "" && st.byFrame && ledAnimProven.oracle == 1 && st.lastScale >= ledAnimProbeMinScale {
		if shows, readable, _ := ledAnimFrameMatch(ledAnimRead(dir, ledAnimFrameNode), st.hue); readable && !shows {
			what = ledAnimFrameNode
		}
	}
	if what == "" {
		return
	}
	st.repaints++
	if st.repaints > ledAnimRepaintLimit {
		st.yielded = true
		LogScene("led").Warn("stick rings: something else keeps changing them; JukaHub stops repainting until the lights restart",
			"node", what, "repaints", st.repaints-1)
		return
	}
	if scaleOnly {
		// Only the brightness moved; the colour is still JukaHub's. The
		// level is written again below (lastScale is what the node holds
		// now), with no colour write and so no dip.
		LogScene("led").Info("stick rings: brightness changed by something else; setting it once more", "node", what,
			"repaint", st.repaints)
		st.lastScale = readScale
		return
	}
	LogScene("led").Info("stick rings: changed by something else; painting them once more", "node", what,
		"path", ledAnimPathFor(false), "repaint", st.repaints)
	st.land, st.why = true, "external "+what
	// The repaint starts from the brightness the rings show now (the dip
	// comes down from it) and writes the level again in the end.
	st.lastScale = -1
	if scaleErr == nil {
		st.lastScale = readScale
	}
	if st.byFrame {
		// Something armed an effect over the frame: off again first.
		st.frameArmed = false
	}
}

// applyLEDAnim writes one colour to the rings.
func applyLEDAnim(dev ledDevice, r, g, b uint8) error {
	st := &ledAnimState
	st.Lock()
	defer st.Unlock()
	if !st.taken {
		ledAnimTake(dev.Dir)
	}
	if len(st.zones) == 0 || len(st.scales) == 0 {
		// Nothing writable any more: one error the loop logs once, no writes.
		return fmt.Errorf("led_anim: no writable zone")
	}
	now := time.Now()
	dt := now.Sub(st.frameAt).Seconds()
	if st.frameAt.IsZero() || dt <= 0 || dt > 0.25 {
		dt = ledFrameInterval.Seconds()
	}
	st.frameAt = now
	hue, level := ledAnimSplit(r, g, b)
	target := 0
	if level > 0 {
		target = int(math.Round(level * float64(st.scaleMax)))
		if target < 1 {
			target = 1
		}
	}
	ledAnimProbeDue(dev.Dir, now)
	ledAnimCheck(dev.Dir, now, level)

	if level > 0 && st.hueSet && !st.land && ledAnimFill.Load() != st.fill {
		// A layer lights more or fewer of them (ledbattlevel.go): the colour
		// lands again with the new share.
		st.land, st.why = true, "fill"
	}
	if level <= 0 {
		// Dark wanted: no colour is written now (the next lit frame lands its
		// own, already dark), and no dip is left half done.
		st.dipping, st.rising = false, false
	} else if st.land || !st.hueSet || !ledAnimHueNear(hue, st.hue) {
		why := st.why
		switch {
		case why != "":
		case !st.hueSet:
			why = "take"
		default:
			why = "colour"
		}
		path := ledAnimPathFor(why == "colour")
		if path == ledAnimPathRearm && st.lastScale != 0 {
			// Lit: down to dark first; the re-arm happens on the frame after
			// the scale reached 0, when its "disable" frame cannot be seen.
			if !st.dipping {
				st.dipping, st.rising = true, false
				st.scaleF = float64(st.lastScale)
				if st.lastScale < 0 {
					st.scaleF = float64(st.scaleMax)
				}
			}
		} else {
			if err := ledAnimLand(dev.Dir, hue, path, why); err != nil {
				return err
			}
			if path == ledAnimPathRearm {
				// Re-armed in the dark: the level comes back up softly.
				st.dipping, st.rising, st.scaleF = false, true, 0
			}
		}
	} else if st.dipping {
		// The colour came back before the rings went dark: no re-arm, just up
		// again.
		st.dipping, st.rising = false, true
	}

	scale := target
	switch {
	case st.dipping:
		st.scaleF -= float64(st.scaleMax) * dt / ledAnimDipDownSec
		if st.scaleF < 0 {
			st.scaleF = 0
		}
		if float64(target) < st.scaleF {
			st.scaleF = float64(target)
		}
		scale = int(math.Round(st.scaleF))
	case st.rising:
		st.scaleF += float64(st.scaleMax) * dt / ledAnimDipUpSec
		if st.scaleF >= float64(target) {
			st.rising = false
		} else {
			scale = int(math.Round(st.scaleF))
		}
	}
	if scale == st.lastScale {
		return nil
	}
	ratio := float64(scale) / float64(st.scaleMax)
	for i, s := range st.scales {
		v := scale
		if i > 0 {
			// Each scale node has its own ceiling (max_scale_f1f2 is not
			// max_scale_lr): the same level, in its own range.
			v = int(math.Round(ratio * float64(s.max)))
			if scale > 0 && v < 1 {
				v = 1
			}
		}
		if err := writeLEDFile(filepath.Join(dev.Dir, s.file), strconv.Itoa(v)); err != nil && i == 0 {
			return err
		}
	}
	st.lastScale = scale
	if st.scaleBack == 0 {
		// Once a take: a driver that clamps or rescales the value would
		// otherwise look changed at every check.
		st.scaleBack = -1
		raw := ledAnimRead(dev.Dir, st.scaleFile)
		if n, err := strconv.Atoi(raw); err == nil && n == scale {
			st.scaleBack = 1
		} else {
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
	ledAnimHueCostly.Store(false)
	saved := st.saved
	if st.byFrame && st.frameArmed {
		// The frame stays on the LEDs until something draws over it: a zone
		// the firmware had off must not keep JukaHub's colour.
		_ = writeLEDFile(filepath.Join(dev.Dir, ledAnimFrameNode), ledAnimFrameText([3]uint8{}, st.frameLEDs))
	}
	st.byFrame, st.frameArmed = false, false
	for i, z := range st.zones {
		if i >= len(saved.zones) {
			break
		}
		zs := saved.zones[i]
		col := "000000 "
		if zs.color != "" {
			col = zs.color + " "
		}
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_rgb_hex_"+z.name), col)
		// The cycles go back before the re-arm, which may read them when the
		// effect starts: the firmware's effect runs as it did, not with
		// JukaHub's -1.
		if zs.cycles != "" {
			_ = writeLEDFile(filepath.Join(dev.Dir, "effect_cycles_"+z.name), zs.cycles)
		}
		_ = writeLEDFile(filepath.Join(dev.Dir, "effect_"+z.name), "0")
		if zs.effect != "" && zs.effect != "0" {
			_ = writeLEDFile(filepath.Join(dev.Dir, "effect_"+z.name), zs.effect)
		}
	}
	for _, s := range st.scales {
		if s.saved != "" {
			_ = writeLEDFile(filepath.Join(dev.Dir, s.file), s.saved)
		}
	}
	for _, sw := range []struct{ file, value string }{
		{"effect_enable", saved.effEn},
		{"enable", saved.enable},
	} {
		if sw.value != "" {
			_ = writeLEDFile(filepath.Join(dev.Dir, sw.file), sw.value)
		}
	}
}

func init() { registerDriverStatus(driverRowLEDs, ledAnimDriverStatus) }

// ledAnimDriverStatus is the LEDs line of Settings → Drivers & hardware: the
// zones the driver has and which of them JukaHub can write. Read-only: it
// lists and opens nodes for writing, it never chmods or writes them.
func ledAnimDriverStatus() driverStatus {
	entries, err := os.ReadDir(ledAnimDir)
	if err != nil {
		if deviceHardwareCurrent().HasLEDs {
			return driverStatus{State: driverMissing, Driver: "led_anim", Detail: "no " + ledAnimDir}
		}
		return driverStatus{State: driverNotUsed, Detail: "no RGB lights on this model"}
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}
	names := ledAnimZoneNames(files)
	st := driverStatus{Driver: "led_anim", Node: ledAnimDir}
	if len(names) == 0 {
		st.State, st.Detail = driverProblem, "no colour zones in the driver"
		return st
	}
	writable := 0
	for _, z := range names {
		if canWriteFile(filepath.Join(ledAnimDir, "effect_rgb_hex_"+z)) {
			writable++
		}
	}
	st.Detail = fmt.Sprintf("zones %s", strings.Join(names, ","))
	switch {
	case writable == 0:
		// The firmware ships the nodes 0444; the sync unlocks them on start.
		st.State = driverUnknown
		st.Detail += " · locked until the lights start"
	case LEDSyncRunning():
		st.State = driverOK
		st.Detail += " · driven by JukaHub"
	default:
		st.State = driverOK
	}
	return st
}
