package main

// chip8_options.go — how one CHIP-8 game wants to be played: which machine
// it was written for, how fast, with which quirks and in which colours.
//
// The words are Octo's. The chip8Archive (chip8_archive.go) stores an
// "options" object per program with Octo's keys — tickrate, the seven quirk
// switches, six colours, screenRotation — and says Octo's defaults hold for
// anything missing, so an archive game is Octo's defaults with its options
// laid over them (chip8ApplyOptions). The values are not always typed alike:
// tickrate is a number in some entries and a string in others, colours are
// "#RGB", "#RRGGBB" or CSS names ("lavender"), so every field is read
// defensively and a value that does not parse keeps the default.
//
// A file from anywhere else has no options. Its machine is guessed from its
// extension (.sc8 SUPER-CHIP, .xo8 XO-CHIP), its size (more than CHIP-8's
// 3.5 KB of program space can only be XO-CHIP) and a look for the
// instructions only the extensions have (00FF switches to SUPER-CHIP's high
// resolution); then that machine's preset applies (chip8Preset): the VIP's
// quirks and a modest speed for plain CHIP-8, SUPER-CHIP 1.1's for SUPER-CHIP,
// Octo's for XO-CHIP. The pause menu's Mode row picks another preset when the
// guess is wrong.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
)

// chip8Platform is the machine a game was written for.
type chip8Platform int

const (
	chip8PlatChip8 chip8Platform = iota
	chip8PlatSChip
	chip8PlatXO
)

// label is the machine's name as the player shows it.
func (p chip8Platform) label() string {
	switch p {
	case chip8PlatSChip:
		return "SUPER-CHIP"
	case chip8PlatXO:
		return "XO-CHIP"
	}
	return "CHIP-8"
}

// ext is the file extension a game of this machine is installed under.
func (p chip8Platform) ext() string {
	switch p {
	case chip8PlatSChip:
		return ".sc8"
	case chip8PlatXO:
		return ".xo8"
	}
	return ".ch8"
}

// chip8NameFolder drops the separators people put in machine names.
var chip8NameFolder = strings.NewReplacer("-", "", "_", "", " ", "", ".", "")

// chip8PlatformFromName reads the archive's "platform" (chip8, schip,
// xochip) and the spellings people use for them.
func chip8PlatformFromName(s string) (chip8Platform, bool) {
	k := chip8NameFolder.Replace(strings.ToLower(strings.TrimSpace(s)))
	switch k {
	case "chip8", "vip", "cosmacvip", "chip":
		return chip8PlatChip8, true
	case "schip", "superchip", "schip11", "superchip11", "schip10", "schpc", "schipc":
		return chip8PlatSChip, true
	case "xochip", "xo", "octo":
		return chip8PlatXO, true
	}
	return chip8PlatChip8, false
}

// The six colours of an options object, in this order.
const (
	chip8ColBG = iota
	chip8ColFill
	chip8ColFill2
	chip8ColBlend
	chip8ColBuzz
	chip8ColQuiet
	chip8ColCount
)

// chip8ColorKeys are the options keys of the six colours.
var chip8ColorKeys = [chip8ColCount]string{"backgroundColor", "fillColor", "fillColor2", "blendColor", "buzzColor", "quietColor"}

// chip8Options is everything the player needs to run one game.
type chip8Options struct {
	Platform chip8Platform
	Tickrate int
	Quirks   chip8Quirks
	// Colors are 0xRRGGBB, indexed by chip8ColBG….
	Colors [chip8ColCount]uint32
	// Rotation is Octo's screenRotation: 0, 90, 180 or 270 degrees.
	Rotation int
	// Buzz: the options set a buzzColor, so the bands around the picture
	// show Octo's visual buzzer (quietColor, buzzColor while it sounds).
	Buzz bool
	// Keys is key information the entry carried ("up" → 5); nil when none.
	Keys map[string]int
	// Source says where the options came from, for the pause menu.
	Source string
}

// Octo's palette: the yellow-on-brown every archive game assumes.
var chip8OctoPalette = [chip8ColCount]uint32{0x996600, 0xFFCC00, 0xFF6600, 0x662200, 0xFFAA00, 0x000000}

// chip8PlainPalette is for files with no options: light pixels on near-black,
// with Octo's second-plane colours.
var chip8PlainPalette = [chip8ColCount]uint32{0x14171C, 0xE9E5D6, 0xFF6600, 0x662200, 0xFFAA00, 0x000000}

// chip8OctoDefaults are Octo's settings, which the archive says to assume for
// anything an entry leaves out.
func chip8OctoDefaults(p chip8Platform) chip8Options {
	return chip8Options{Platform: p, Tickrate: 20, Colors: chip8OctoPalette, Source: "chip8Archive"}
}

// chip8Preset is a machine's preset: what a file without options plays with,
// and what the pause menu's Mode row switches to.
func chip8Preset(p chip8Platform) chip8Options {
	o := chip8Options{Platform: p, Colors: chip8PlainPalette, Source: "file type"}
	switch p {
	case chip8PlatSChip:
		// SUPER-CHIP 1.1 on the HP 48.
		o.Tickrate = 30
		o.Quirks = chip8Quirks{Shift: true, LoadStore: true, Clip: true, Jump: true}
	case chip8PlatXO:
		// Octo's own behaviour; XO-CHIP games expect a fast machine.
		o.Tickrate = 200
		o.Colors = chip8OctoPalette
	default:
		// The COSMAC VIP: VF reset by the logic ops, clipped sprites and the
		// display wait.
		o.Tickrate = 15
		o.Quirks = chip8Quirks{Logic: true, Clip: true, VBlank: true}
	}
	return o
}

// chip8MaxClassicProgram is CHIP-8's and SUPER-CHIP's program space
// (0x200–0xFFF).
const chip8MaxClassicProgram = 0x1000 - chip8ProgStart

// chip8GuessPlatform guesses the machine of a file with no options from its
// lower-case extension and its bytes.
func chip8GuessPlatform(ext string, rom []byte) chip8Platform {
	switch ext {
	case ".xo8":
		return chip8PlatXO
	case ".sc8":
		return chip8PlatSChip
	}
	if len(rom) > chip8MaxClassicProgram {
		return chip8PlatXO
	}
	// Instructions start at even offsets from 0x200, so data that happens to
	// look like one is the only false match; 00FF (high resolution) and
	// XO-CHIP's audio and plane instructions are rare as data.
	hires, xo := false, 0
	for k := 0; k+1 < len(rom); k += 2 {
		op := uint16(rom[k])<<8 | uint16(rom[k+1])
		switch {
		case op == 0x00FF:
			hires = true
		case op == 0xF002, op == 0xF101, op == 0xF201, op == 0xF301:
			xo++
		}
	}
	switch {
	case xo >= 2:
		return chip8PlatXO
	case hires:
		return chip8PlatSChip
	}
	return chip8PlatChip8
}

// chip8ApplyOptions lays an Octo options object over base. Keys it does not
// know and values that do not parse are ignored.
func chip8ApplyOptions(base chip8Options, raw map[string]json.RawMessage) chip8Options {
	o := base
	if len(raw) == 0 {
		return o
	}
	if n, ok := chip8JSONInt(raw["tickrate"]); ok && n > 0 {
		o.Tickrate = min(n, chip8MaxTickrate)
	}
	for key, flag := range map[string]*bool{
		"shiftQuirks":     &o.Quirks.Shift,
		"loadStoreQuirks": &o.Quirks.LoadStore,
		"vfOrderQuirks":   &o.Quirks.VfOrder,
		"clipQuirks":      &o.Quirks.Clip,
		"jumpQuirks":      &o.Quirks.Jump,
		"logicQuirks":     &o.Quirks.Logic,
		"vBlankQuirks":    &o.Quirks.VBlank,
	} {
		if b, ok := chip8JSONBool(raw[key]); ok {
			*flag = b
		}
	}
	for i, key := range chip8ColorKeys {
		if s, ok := chip8JSONString(raw[key]); ok {
			if c, ok := chip8ParseColor(s); ok {
				o.Colors[i] = c
				if i == chip8ColBuzz {
					o.Buzz = true
				}
			}
		}
	}
	if n, ok := chip8JSONInt(raw["screenRotation"]); ok {
		switch n {
		case 0, 90, 180, 270:
			o.Rotation = n
		}
	}
	if keys := chip8ParseKeyInfo(raw["keys"]); keys != nil {
		o.Keys = keys
	}
	return o
}

// chip8JSONInt reads a number or a numeric string.
func chip8JSONInt(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return 0, false
		}
		s = strings.TrimSpace(str)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || f > 1e9 || f < -1e9 {
		return 0, false
	}
	return int(f), true
}

// chip8JSONBool reads true/false, "true"/"false" or 0/1.
func chip8JSONBool(raw json.RawMessage) (bool, bool) {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	switch s {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	return false, false
}

// chip8JSONString reads a string value.
func chip8JSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// chip8KeyNames are the pad buttons key information can name.
var chip8KeyNames = [...]string{"up", "down", "left", "right", "a", "b", "x", "y", "l1", "r1"}

// chip8ParseKeyInfo reads {"up": 5, "a": "6", …}: a pad button to a hex key.
// No archive field carries this today (chip8Archive issue #38 asks for one);
// it is read when an entry or an options file has it.
func chip8ParseKeyInfo(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	out := map[string]int{}
	for name, val := range obj {
		name = strings.ToLower(strings.TrimSpace(name))
		known := false
		for _, k := range chip8KeyNames {
			if k == name {
				known = true
			}
		}
		if !known {
			continue
		}
		if n, ok := chip8JSONInt(val); ok && n >= 0 && n < 16 {
			out[name] = n
			continue
		}
		if s, ok := chip8JSONString(val); ok && len(s) == 1 {
			if n, err := strconv.ParseUint(s, 16, 8); err == nil {
				out[name] = int(n)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// chip8ParseColor reads "#RGB", "#RRGGBB" (a trailing alpha is ignored) or
// a CSS colour name.
func chip8ParseColor(s string) (uint32, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	hex := strings.TrimPrefix(s, "#")
	if len(hex) == 3 || len(hex) == 4 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) == 8 {
		hex = hex[:6]
	}
	if len(hex) == 6 {
		if n, err := strconv.ParseUint(hex, 16, 32); err == nil {
			return uint32(n), true
		}
	}
	chip8CSSOnce.Do(chip8LoadCSSColors)
	c, ok := chip8CSSColors[strings.ReplaceAll(s, " ", "")]
	return c, ok
}

var (
	chip8CSSOnce   sync.Once
	chip8CSSColors map[string]uint32
)

// chip8CSSColorList is CSS's named colours, as "name rrggbb" pairs.
const chip8CSSColorList = `aliceblue f0f8ff antiquewhite faebd7 aqua 00ffff aquamarine 7fffd4 azure f0ffff
beige f5f5dc bisque ffe4c4 black 000000 blanchedalmond ffebcd blue 0000ff blueviolet 8a2be2 brown a52a2a
burlywood deb887 cadetblue 5f9ea0 chartreuse 7fff00 chocolate d2691e coral ff7f50 cornflowerblue 6495ed
cornsilk fff8dc crimson dc143c cyan 00ffff darkblue 00008b darkcyan 008b8b darkgoldenrod b8860b
darkgray a9a9a9 darkgrey a9a9a9 darkgreen 006400 darkkhaki bdb76b darkmagenta 8b008b darkolivegreen 556b2f
darkorange ff8c00 darkorchid 9932cc darkred 8b0000 darksalmon e9967a darkseagreen 8fbc8f
darkslateblue 483d8b darkslategray 2f4f4f darkslategrey 2f4f4f darkturquoise 00ced1 darkviolet 9400d3
deeppink ff1493 deepskyblue 00bfff dimgray 696969 dimgrey 696969 dodgerblue 1e90ff firebrick b22222
floralwhite fffaf0 forestgreen 228b22 fuchsia ff00ff gainsboro dcdcdc ghostwhite f8f8ff gold ffd700
goldenrod daa520 gray 808080 grey 808080 green 008000 greenyellow adff2f honeydew f0fff0 hotpink ff69b4
indianred cd5c5c indigo 4b0082 ivory fffff0 khaki f0e68c lavender e6e6fa lavenderblush fff0f5
lawngreen 7cfc00 lemonchiffon fffacd lightblue add8e6 lightcoral f08080 lightcyan e0ffff
lightgoldenrodyellow fafad2 lightgray d3d3d3 lightgrey d3d3d3 lightgreen 90ee90 lightpink ffb6c1
lightsalmon ffa07a lightseagreen 20b2aa lightskyblue 87cefa lightslategray 778899 lightslategrey 778899
lightsteelblue b0c4de lightyellow ffffe0 lime 00ff00 limegreen 32cd32 linen faf0e6 magenta ff00ff
maroon 800000 mediumaquamarine 66cdaa mediumblue 0000cd mediumorchid ba55d3 mediumpurple 9370db
mediumseagreen 3cb371 mediumslateblue 7b68ee mediumspringgreen 00fa9a mediumturquoise 48d1cc
mediumvioletred c71585 midnightblue 191970 mintcream f5fffa mistyrose ffe4e1 moccasin ffe4b5
navajowhite ffdead navy 000080 oldlace fdf5e6 olive 808000 olivedrab 6b8e23 orange ffa500 orangered ff4500
orchid da70d6 palegoldenrod eee8aa palegreen 98fb98 paleturquoise afeeee palevioletred db7093
papayawhip ffefd5 peachpuff ffdab9 peru cd853f pink ffc0cb plum dda0dd powderblue b0e0e6 purple 800080
rebeccapurple 663399 red ff0000 rosybrown bc8f8f royalblue 4169e1 saddlebrown 8b4513 salmon fa8072
sandybrown f4a460 seagreen 2e8b57 seashell fff5ee sienna a0522d silver c0c0c0 skyblue 87ceeb
slateblue 6a5acd slategray 708090 slategrey 708090 snow fffafa springgreen 00ff7f steelblue 4682b4
tan d2b48c teal 008080 thistle d8bfd8 tomato ff6347 turquoise 40e0d0 violet ee82ee wheat f5deb3
white ffffff whitesmoke f5f5f5 yellow ffff00 yellowgreen 9acd32`

func chip8LoadCSSColors() {
	f := strings.Fields(chip8CSSColorList)
	chip8CSSColors = make(map[string]uint32, len(f)/2)
	for i := 0; i+1 < len(f); i += 2 {
		if n, err := strconv.ParseUint(f[i+1], 16, 32); err == nil {
			chip8CSSColors[f[i]] = uint32(n)
		}
	}
}
