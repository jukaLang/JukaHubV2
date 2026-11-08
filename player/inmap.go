package main

// inmap.go — the input mapping language: control profiles written as text.
//
// A control profile is a small text file (<data>/input-profiles/*.jkinput)
// that says what the pad's buttons do, on every screen or on some of them:
//
//	profile: Media browsing
//	description: Seek with the D-pad while something plays
//	scenes: Tube, MusicPlayer      # or * for every screen
//	map:
//	  dpad_left            -> seek:-10s
//	  dpad_right           -> seek:+10s
//	  l1                   -> key:PageUp
//	  a hold 500ms         -> action:context_menu
//	  y double             -> action:favorite
//	  l1+x                 -> profile:next
//
// The reader is forgiving: case does not matter, # and // start a comment,
// "->", "=>" and "→" are all arrows, and a line that cannot be read is
// reported with its line and column and skipped, so one typo costs that line
// and never the profile.
//
// Triggers: one button (a b x y l1 r1 l2 r2 l3 r3 start select guide dpad_up
// dpad_down dpad_left dpad_right, plus common aliases), optionally "hold"
// (with a time such as 500ms or 1s) or "double"; or a chord "l1+a" — hold the
// first buttons, press the last. Targets: button:<b>, key:<key>,
// action:<name>[:<value>], profile:next|prev|default|<name>, or none. A bare
// action or button name, and the shorthands seek:-10s, volume:+10 and
// brightness:-10, work too. The phrasing of the ideas list —
// "input: d-pad_up -> action:next_item, modifier:hold_duration_ms:500" — is
// accepted as well.
//
// Settings lines: profile: (or name:), description:, author:, scenes:,
// version: 1 and auto: off (never switch on by itself). Inside map:, a
// "scene: Tube" line scopes the lines under it ("scene: *" ends the block),
// and "scene: Tube / dpad_left -> seek:-10s" scopes a single line.
//
// inmapParse never fails; inmapValidate adds what a line-by-line read cannot
// see (duplicates, reserved chords, unknown profile and screen names). The
// engine that runs a profile is inmap_runtime.go; the named actions are
// inmap_actions.go; the registry of profiles is inmap_profiles.go.

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// inmapButton is a pad button as the language names it. L2 and R2 are analog
// triggers: SDL reports them as axes, so they have no button code and reach
// the engine through shoulders.go's latch instead.
type inmapButton uint8

const (
	inmapBtnA inmapButton = iota
	inmapBtnB
	inmapBtnX
	inmapBtnY
	inmapBtnL1
	inmapBtnR1
	inmapBtnL2
	inmapBtnR2
	inmapBtnL3
	inmapBtnR3
	inmapBtnStart
	inmapBtnSelect
	inmapBtnGuide
	inmapBtnUp
	inmapBtnDown
	inmapBtnLeft
	inmapBtnRight
	inmapButtonCount
)

// inmapButtonInfo is one row of the button table: the spelling a profile
// uses, the label the screens show, and the SDL button (none for a trigger).
type inmapButtonInfo struct {
	name    string
	label   string
	code    uint8
	trigger bool
}

// inmapButtons is indexed by inmapButton, in the constants' order.
var inmapButtons = [inmapButtonCount]inmapButtonInfo{
	{name: "a", label: "A", code: sdl.CONTROLLER_BUTTON_A},
	{name: "b", label: "B", code: sdl.CONTROLLER_BUTTON_B},
	{name: "x", label: "X", code: sdl.CONTROLLER_BUTTON_X},
	{name: "y", label: "Y", code: sdl.CONTROLLER_BUTTON_Y},
	{name: "l1", label: "L1", code: sdl.CONTROLLER_BUTTON_LEFTSHOULDER},
	{name: "r1", label: "R1", code: sdl.CONTROLLER_BUTTON_RIGHTSHOULDER},
	{name: "l2", label: "L2", trigger: true},
	{name: "r2", label: "R2", trigger: true},
	{name: "l3", label: "L3", code: sdl.CONTROLLER_BUTTON_LEFTSTICK},
	{name: "r3", label: "R3", code: sdl.CONTROLLER_BUTTON_RIGHTSTICK},
	{name: "start", label: "Start", code: sdl.CONTROLLER_BUTTON_START},
	{name: "select", label: "Select", code: sdl.CONTROLLER_BUTTON_BACK},
	{name: "guide", label: "Menu", code: sdl.CONTROLLER_BUTTON_GUIDE},
	{name: "dpad_up", label: "D-pad up", code: sdl.CONTROLLER_BUTTON_DPAD_UP},
	{name: "dpad_down", label: "D-pad down", code: sdl.CONTROLLER_BUTTON_DPAD_DOWN},
	{name: "dpad_left", label: "D-pad left", code: sdl.CONTROLLER_BUTTON_DPAD_LEFT},
	{name: "dpad_right", label: "D-pad right", code: sdl.CONTROLLER_BUTTON_DPAD_RIGHT},
}

// inmapButtonAliases are the other names people write for a button: the
// arrows on their own, the Xbox, PlayStation and Switch spellings.
var inmapButtonAliases = []struct {
	alias  string
	button inmapButton
}{
	{"up", inmapBtnUp}, {"down", inmapBtnDown}, {"left", inmapBtnLeft}, {"right", inmapBtnRight},
	{"dpad_u", inmapBtnUp}, {"dpad_d", inmapBtnDown}, {"dpad_l", inmapBtnLeft}, {"dpad_r", inmapBtnRight},
	{"back", inmapBtnSelect}, {"minus", inmapBtnSelect}, {"view", inmapBtnSelect}, {"share", inmapBtnSelect},
	{"plus", inmapBtnStart}, {"options", inmapBtnStart},
	{"menu", inmapBtnGuide}, {"home", inmapBtnGuide}, {"ps", inmapBtnGuide},
	{"lb", inmapBtnL1}, {"rb", inmapBtnR1}, {"l", inmapBtnL1}, {"r", inmapBtnR1},
	{"lt", inmapBtnL2}, {"rt", inmapBtnR2}, {"zl", inmapBtnL2}, {"zr", inmapBtnR2},
	{"ls", inmapBtnL3}, {"rs", inmapBtnR3},
	{"cross", inmapBtnA}, {"circle", inmapBtnB}, {"square", inmapBtnX}, {"triangle", inmapBtnY},
}

// inmapButtonFromSDL maps an SDL controller button to the language's button.
// Buttons the language has no name for (paddles, the touchpad) report false.
func inmapButtonFromSDL(code uint8) (inmapButton, bool) {
	for i := range inmapButtons {
		if !inmapButtons[i].trigger && inmapButtons[i].code == code {
			return inmapButton(i), true
		}
	}
	return 0, false
}

// inmapNormName folds a name for lookups: lower case, with spaces and dashes
// as underscores and every spelling of "d-pad" as "dpad", so D-Pad_Up,
// d-pad-up and dpadup all read as dpad_up.
func inmapNormName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "d_pad", "dpad")
	if strings.HasPrefix(s, "dpad") && len(s) > 4 && s[4] != '_' {
		s = "dpad_" + s[4:]
	}
	return s
}

// inmapLookupButton finds a button by its name or an alias.
func inmapLookupButton(name string) (inmapButton, bool) {
	n := inmapNormName(name)
	if n == "" {
		return 0, false
	}
	for i := range inmapButtons {
		if inmapButtons[i].name == n {
			return inmapButton(i), true
		}
	}
	for _, a := range inmapButtonAliases {
		if a.alias == n {
			return a.button, true
		}
	}
	return 0, false
}

// inmapButtonWords lists the canonical button names, for suggestions.
func inmapButtonWords() []string {
	out := make([]string, 0, len(inmapButtons))
	for i := range inmapButtons {
		out = append(out, inmapButtons[i].name)
	}
	return out
}

// inmapKeyNames are the keyboard keys a profile may send. Letters and digits
// are accepted on top of these (key:a, key:5).
var inmapKeyNames = []struct {
	name string
	key  sdl.Keycode
}{
	{"pageup", sdl.K_PAGEUP}, {"pagedown", sdl.K_PAGEDOWN}, {"home", sdl.K_HOME}, {"end", sdl.K_END},
	{"up", sdl.K_UP}, {"down", sdl.K_DOWN}, {"left", sdl.K_LEFT}, {"right", sdl.K_RIGHT},
	{"return", sdl.K_RETURN}, {"enter", sdl.K_RETURN}, {"escape", sdl.K_ESCAPE}, {"esc", sdl.K_ESCAPE},
	{"space", sdl.K_SPACE}, {"tab", sdl.K_TAB}, {"backspace", sdl.K_BACKSPACE}, {"delete", sdl.K_DELETE},
	{"insert", sdl.K_INSERT}, {"minus", sdl.K_MINUS}, {"equals", sdl.K_EQUALS}, {"plus", sdl.K_PLUS},
	{"comma", sdl.K_COMMA}, {"period", sdl.K_PERIOD}, {"slash", sdl.K_SLASH},
	{"f1", sdl.K_F1}, {"f2", sdl.K_F2}, {"f3", sdl.K_F3}, {"f4", sdl.K_F4},
	{"f5", sdl.K_F5}, {"f6", sdl.K_F6}, {"f7", sdl.K_F7}, {"f8", sdl.K_F8},
	{"f9", sdl.K_F9}, {"f10", sdl.K_F10}, {"f11", sdl.K_F11}, {"f12", sdl.K_F12},
	{"mute", sdl.K_AUDIOMUTE}, {"volumeup", sdl.K_VOLUMEUP}, {"volumedown", sdl.K_VOLUMEDOWN},
	{"play", sdl.K_AUDIOPLAY}, {"next", sdl.K_AUDIONEXT}, {"prev", sdl.K_AUDIOPREV}, {"previous", sdl.K_AUDIOPREV},
}

// inmapLookupKey finds a keyboard key by name: "PageUp", "page up" and
// "page_up" are the same key, and a single letter or digit is that key.
func inmapLookupKey(name string) (sdl.Keycode, string, bool) {
	raw := strings.TrimSpace(name)
	folded := strings.ToLower(raw)
	folded = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(folded)
	if folded == "" {
		return 0, "", false
	}
	if len(folded) == 1 {
		c := folded[0]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return sdl.Keycode(c), strings.ToUpper(folded), true
		}
	}
	for _, k := range inmapKeyNames {
		if k.name == folded {
			return k.key, raw, true
		}
	}
	return 0, "", false
}

// inmapKeyWords lists the key names, for suggestions.
func inmapKeyWords() []string {
	out := make([]string, 0, len(inmapKeyNames))
	for _, k := range inmapKeyNames {
		out = append(out, k.name)
	}
	return out
}

// inmapPressKind is how a trigger's button is pressed.
type inmapPressKind uint8

const (
	inmapPressTap inmapPressKind = iota
	inmapPressHold
	inmapPressDouble
)

// Hold times are clamped to a range a thumb can tell apart from a tap and a
// person will actually wait for; chords stop at three buttons.
const (
	inmapHoldDefaultMs = 500
	inmapHoldMinMs     = 100
	inmapHoldMaxMs     = 5000
	inmapMaxChord      = 3
)

// inmapTrigger is the left side of a mapping. In a chord every button but the
// last is held first; the mapping fires when the last one goes down.
type inmapTrigger struct {
	buttons []inmapButton
	kind    inmapPressKind
	holdMs  int
}

// inmapTargetKind is what a mapping does.
type inmapTargetKind uint8

const (
	inmapTargetNone inmapTargetKind = iota
	inmapTargetButton
	inmapTargetKey
	inmapTargetAction
	inmapTargetProfile
)

// inmapTarget is the right side of a mapping. arg is an action's value
// (seconds for seek, a percentage step for volume) when hasArg is set.
type inmapTarget struct {
	kind    inmapTargetKind
	button  inmapButton
	key     sdl.Keycode
	keyName string
	action  string
	arg     float64
	hasArg  bool
	profile string
}

// inmapBinding is one mapping line that was read.
type inmapBinding struct {
	trigger inmapTrigger
	target  inmapTarget
	// scenes limits the line to some screens; nil means the profile's own
	// scope.
	scenes []string
	line   int
	col    int
	source string
	// ok is false when validation found an error on the line: the editor
	// lists it, the engine never runs it.
	ok bool
}

// inmapProblem is one thing wrong with a profile, at a line and a column.
type inmapProblem struct {
	line    int
	col     int
	msg     string
	warning bool
}

// inmapProfile is a parsed profile.
type inmapProfile struct {
	name        string
	description string
	author      string
	// scenes is where the profile applies; empty means every screen.
	scenes   []string
	bindings []inmapBinding
	// parsed holds what the reader found; problems is that plus what
	// validation found, rebuilt on every validation.
	parsed   []inmapProblem
	problems []inmapProblem
	text     string
	// path is the file the profile was read from ("" for a built-in);
	// builtin and external say who owns it: JukaHub, or a plugin.
	path     string
	builtin  bool
	external bool
	// noAuto keeps a screen-scoped profile from switching itself on when
	// "Switch by screen" is enabled.
	noAuto bool
}

// errorCount is how many problems stop a line from running.
func (p *inmapProfile) errorCount() int {
	n := 0
	for _, pr := range p.problems {
		if !pr.warning {
			n++
		}
	}
	return n
}

// everywhere reports whether the profile applies on every screen.
func (p *inmapProfile) everywhere() bool {
	return len(p.scenes) == 0
}

// usableBindings counts the lines the engine runs.
func (p *inmapProfile) usableBindings() int {
	n := 0
	for i := range p.bindings {
		if p.bindings[i].ok {
			n++
		}
	}
	return n
}

// ── Reading ──────────────────────────────────────────────────────────────

// inmapTok is one word of a line and the byte offset it starts at.
type inmapTok struct {
	text string
	off  int
}

// inmapParser carries one read: the profile being built and the line under
// the cursor, so every problem can say where it is.
type inmapParser struct {
	p      *inmapProfile
	line   string
	lineNo int
}

func (ps *inmapParser) problem(off int, msg string, warning bool) {
	ps.p.parsed = append(ps.p.parsed, inmapProblem{
		line:    ps.lineNo,
		col:     inmapCol(ps.line, off),
		msg:     msg,
		warning: warning,
	})
}

func (ps *inmapParser) fail(off int, msg string) { ps.problem(off, msg, false) }

func (ps *inmapParser) warn(off int, msg string) { ps.problem(off, msg, true) }

// inmapCol turns a byte offset into a 1-based character column.
func inmapCol(line string, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(line) {
		off = len(line)
	}
	return utf8.RuneCountInString(line[:off]) + 1
}

// inmapStripComment cuts a # or // comment off a line.
func inmapStripComment(line string) string {
	cut := len(line)
	if i := strings.IndexByte(line, '#'); i >= 0 && i < cut {
		cut = i
	}
	if i := strings.Index(line, "//"); i >= 0 && i < cut {
		cut = i
	}
	return line[:cut]
}

// inmapArrows are the spellings of "maps to".
var inmapArrows = []string{"->", "=>", "→"}

// inmapFindArrow returns the first arrow's byte offset and length, or -1.
func inmapFindArrow(s string) (int, int) {
	best, size := -1, 0
	for _, a := range inmapArrows {
		if i := strings.Index(s, a); i >= 0 && (best < 0 || i < best) {
			best, size = i, len(a)
		}
	}
	return best, size
}

// inmapFields splits s at spaces, tabs and commas. off is where s starts in
// its line, so each word keeps its real offset.
func inmapFields(s string, off int) []inmapTok {
	var out []inmapTok
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' || s[i] == '\t' || s[i] == ',' {
			if start >= 0 {
				out = append(out, inmapTok{text: s[start:i], off: off + start})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return out
}

// inmapClauses splits s at commas only, trimming each part: the target and
// the "modifier:…" clauses after it.
func inmapClauses(s string, off int) []inmapTok {
	var out []inmapTok
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != ',' {
			continue
		}
		part := s[start:i]
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, inmapTok{text: trimmed, off: off + start + strings.Index(part, trimmed)})
		}
		start = i + 1
	}
	return out
}

// inmapGlueChords joins "a", "+", "b" (and "a+", "b" or "a", "+b") back into
// the one word "a+b", so a chord may be written with spaces around its plus.
func inmapGlueChords(toks []inmapTok) []inmapTok {
	out := make([]inmapTok, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		for i+1 < len(toks) && (strings.HasSuffix(t.text, "+") || strings.HasPrefix(toks[i+1].text, "+")) {
			t.text += toks[i+1].text
			i++
		}
		out = append(out, t)
	}
	return out
}

// inmapSceneList splits "Tube, MusicPlayer" into names. "*" (also "all" or
// an empty list) means every screen and comes back as nil.
func inmapSceneList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		switch strings.ToLower(name) {
		case "":
			continue
		case "*", "all", "any", "everywhere":
			return nil
		}
		out = append(out, name)
	}
	return out
}

// inmapParseDurationMs reads 500, 500ms, 0.5s, 2sec or 1m as milliseconds. A
// sign is kept (seek:-10s); a bare number is milliseconds.
func inmapParseDurationMs(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	num, unit := s, 1.0
	for _, u := range []struct {
		suffix string
		scale  float64
	}{
		// Longest first: "mins" before "s", "ms" before "m".
		{"milliseconds", 1}, {"millisecond", 1}, {"msec", 1}, {"ms", 1},
		{"minutes", 60000}, {"minute", 60000}, {"mins", 60000}, {"min", 60000},
		{"seconds", 1000}, {"second", 1000}, {"secs", 1000}, {"sec", 1000}, {"s", 1000},
		{"m", 60000},
	} {
		if strings.HasSuffix(s, u.suffix) {
			num, unit = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.scale
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return int(math.Round(v * unit)), true
}

// inmapParsePercent reads a signed step such as +10, -5 or 20%.
func inmapParsePercent(s string) (float64, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < -100 || v > 100 {
		return 0, false
	}
	return v, true
}

// inmapIsHeaderLine reports whether a line is a setting whose value may hold
// an arrow of its own ("description: Left -> seek back").
func inmapIsHeaderLine(content string) bool {
	colon := strings.IndexByte(content, ':')
	if colon < 0 {
		return false
	}
	switch inmapNormName(content[:colon]) {
	case "profile", "name", "title", "description", "desc", "about", "author", "by", "version", "auto":
		return true
	}
	return false
}

// inmapIsMapWord reports whether a line opens the mapping section.
func inmapIsMapWord(low string) bool {
	switch strings.TrimSuffix(low, ":") {
	case "map", "mappings", "bindings", "controls", "buttons":
		return true
	}
	return false
}

// inmapParse reads profile text. It never fails: every line it cannot use is
// recorded as a problem and skipped.
func inmapParse(src string) *inmapProfile {
	p := &inmapProfile{text: src}
	ps := &inmapParser{p: p}
	inMap := false
	var block []string
	text := strings.ReplaceAll(src, "\r\n", "\n")
	// A byte-order mark from a Windows editor is not part of line 1.
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	for i, raw := range strings.Split(text, "\n") {
		ps.line, ps.lineNo = raw, i+1
		body := inmapStripComment(raw)
		content := strings.TrimSpace(body)
		if content == "" {
			continue
		}
		lead := strings.Index(body, content)
		if at, size := inmapFindArrow(body); at >= 0 && !inmapIsHeaderLine(content) {
			ps.mapping(body, at, size, block)
			continue
		}
		low := strings.ToLower(content)
		if inmapIsMapWord(low) {
			inMap = true
			continue
		}
		colon := strings.IndexByte(content, ':')
		if colon < 0 {
			ps.fail(lead, "expected \"button -> target\" or \"setting: value\"")
			continue
		}
		key := inmapNormName(content[:colon])
		rest := content[colon+1:]
		value := strings.TrimSpace(rest)
		valueOff := lead + colon + 1 + (len(rest) - len(strings.TrimLeft(rest, " \t")))
		switch key {
		case "profile", "name", "title":
			if value == "" {
				ps.fail(valueOff, "the profile name is empty")
				continue
			}
			if p.name != "" {
				ps.warn(lead, "the profile is already called \""+p.name+"\"; this line is ignored")
				continue
			}
			p.name = value
		case "description", "desc", "about":
			p.description = value
		case "author", "by":
			p.author = value
		case "scenes", "scene", "screens", "screen", "in":
			list := inmapSceneList(value)
			if value == "" {
				ps.warn(valueOff, "no screens listed; use * for every screen")
			}
			if inMap || len(p.bindings) > 0 {
				// Inside map: the lines below belong to these screens.
				block = list
				continue
			}
			p.scenes = list
		case "version":
			if value != "1" {
				ps.warn(valueOff, "unknown version \""+value+"\"; reading it as version 1")
			}
		case "auto":
			switch inmapNormName(value) {
			case "off", "no", "false", "never", "manual":
				p.noAuto = true
			case "on", "yes", "true":
				p.noAuto = false
			default:
				ps.warn(valueOff, "auto: is on or off")
			}
		default:
			ps.warn(lead, "unknown setting \""+strings.TrimSpace(content[:colon])+"\" (ignored)")
		}
	}
	p.problems = append([]inmapProblem(nil), p.parsed...)
	return p
}

// mapping reads one "trigger -> target" line. body is the line without its
// comment; at and size locate the arrow; block is the scene block it sits in.
func (ps *inmapParser) mapping(body string, at, size int, block []string) {
	left, leftOff := body[:at], 0
	right, rightOff := body[at+size:], at+size
	scenes := block
	// "scene: Tube, MusicPlayer / dpad_left -> seek:-10s" scopes one line.
	if slash := strings.LastIndexByte(left, '/'); slash >= 0 {
		head := strings.TrimSpace(left[:slash])
		if c := strings.IndexByte(head, ':'); c >= 0 {
			switch inmapNormName(head[:c]) {
			case "scene", "scenes", "screen", "screens", "in":
				scenes = inmapSceneList(head[c+1:])
				leftOff = slash + 1
				left = left[slash+1:]
			}
		}
	}
	// The ideas list wrote "input: d-pad_up"; the word is optional.
	trimmed := strings.TrimLeft(left, " \t")
	if strings.HasPrefix(strings.ToLower(trimmed), "input:") {
		skip := len(left) - len(trimmed) + len("input:")
		leftOff += skip
		left = left[skip:]
	}
	trig, ok := ps.trigger(left, leftOff)
	if !ok {
		return
	}
	clauses := inmapClauses(right, rightOff)
	if len(clauses) == 0 {
		ps.fail(rightOff, "nothing after the arrow: give a target such as button:a, key:PageUp, action:home, profile:next or none")
		return
	}
	tgt, ok := ps.target(clauses[0])
	if !ok {
		return
	}
	// "…, modifier:hold_duration_ms:500" after the target.
	for _, c := range clauses[1:] {
		if !ps.modifier(&trig, c) {
			return
		}
	}
	col := inmapCol(ps.line, leftOff+len(left)-len(strings.TrimLeft(left, " \t")))
	ps.p.bindings = append(ps.p.bindings, inmapBinding{
		trigger: trig,
		target:  tgt,
		scenes:  scenes,
		line:    ps.lineNo,
		col:     col,
		source:  strings.TrimSpace(body),
		ok:      true,
	})
}

// trigger reads the buttons and the press modifiers left of the arrow.
func (ps *inmapParser) trigger(s string, off int) (inmapTrigger, bool) {
	toks := inmapGlueChords(inmapFields(s, off))
	t := inmapTrigger{}
	if len(toks) == 0 {
		ps.fail(off, "a mapping needs a button before the arrow")
		return t, false
	}
	for _, part := range strings.Split(toks[0].text, "+") {
		if strings.TrimSpace(part) == "" {
			ps.fail(toks[0].off, "a chord is written a+b, with a button on each side of +")
			return t, false
		}
		b, ok := inmapLookupButton(part)
		if !ok {
			ps.fail(toks[0].off, inmapUnknown("button", part, inmapButtonWords()))
			return t, false
		}
		t.buttons = append(t.buttons, b)
	}
	for i := 1; i < len(toks); i++ {
		tok := toks[i]
		word := inmapNormName(tok.text)
		switch {
		case word == "hold" || word == "held" || word == "long" || word == "longpress" || word == "long_press":
			if !ps.setKind(&t, inmapPressHold, tok.off) {
				return t, false
			}
			t.holdMs = inmapHoldDefaultMs
			// "hold 500ms", or "hold 500 ms" with the unit on its own.
			if i+1 < len(toks) {
				spec := toks[i+1].text
				used := 1
				if i+2 < len(toks) && inmapIsTimeUnit(toks[i+2].text) {
					spec += toks[i+2].text
					used = 2
				}
				if ms, ok := inmapParseDurationMs(spec); ok {
					if !ps.holdRange(toks[i+1].off, ms) {
						return t, false
					}
					t.holdMs = ms
					i += used
				}
			}
		case strings.HasPrefix(word, "hold:") || strings.HasPrefix(word, "hold="):
			ms, ok := inmapParseDurationMs(word[len("hold:"):])
			if !ok {
				ps.fail(tok.off, "write the hold time as 500ms or 1s")
				return t, false
			}
			if !ps.holdRange(tok.off, ms) || !ps.setKind(&t, inmapPressHold, tok.off) {
				return t, false
			}
			t.holdMs = ms
		case word == "double" || word == "double_tap" || word == "doubletap" || word == "twice" || word == "x2" || word == "dbl":
			if !ps.setKind(&t, inmapPressDouble, tok.off) {
				return t, false
			}
		case word == "tap" || word == "press" || word == "click":
			if !ps.setKind(&t, inmapPressTap, tok.off) {
				return t, false
			}
		case strings.HasPrefix(word, "modifier:"):
			if !ps.modifier(&t, tok) {
				return t, false
			}
		default:
			if _, isButton := inmapLookupButton(tok.text); isButton {
				ps.fail(tok.off, "two buttons in a row: write "+toks[0].text+"+"+tok.text+" for a chord")
				return t, false
			}
			ps.fail(tok.off, inmapUnknown("modifier", tok.text, []string{"hold", "double", "tap"}))
			return t, false
		}
	}
	return t, true
}

// setKind sets how a trigger is pressed. A trigger is tapped, held or
// double-tapped — saying two of them on one line is a mistake.
func (ps *inmapParser) setKind(t *inmapTrigger, k inmapPressKind, off int) bool {
	if t.kind != inmapPressTap && t.kind != k {
		ps.fail(off, "a button is held or double-tapped on one line, not both")
		return false
	}
	t.kind = k
	return true
}

// inmapIsTimeUnit reports whether a word is a time unit on its own ("ms").
func inmapIsTimeUnit(word string) bool {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "ms", "msec", "millisecond", "milliseconds", "s", "sec", "secs", "second", "seconds":
		return true
	}
	return false
}

// holdRange checks a hold time against the limits.
func (ps *inmapParser) holdRange(off, ms int) bool {
	if ms < inmapHoldMinMs || ms > inmapHoldMaxMs {
		ps.fail(off, "a hold lasts between "+strconv.Itoa(inmapHoldMinMs)+"ms and "+
			strconv.Itoa(inmapHoldMaxMs/1000)+"s")
		return false
	}
	return true
}

// modifier reads a "modifier:…" clause, the ideas list's phrasing:
// modifier:hold_duration_ms:500, modifier:hold:1s, modifier:double.
func (ps *inmapParser) modifier(t *inmapTrigger, tok inmapTok) bool {
	text := strings.TrimSpace(tok.text)
	if !strings.HasPrefix(strings.ToLower(text), "modifier:") {
		ps.fail(tok.off, "only modifier:… may follow the target (for example modifier:hold_duration_ms:500)")
		return false
	}
	rest := text[len("modifier:"):]
	name, value := rest, ""
	if c := strings.IndexByte(rest, ':'); c >= 0 {
		name, value = rest[:c], rest[c+1:]
	}
	switch inmapNormName(name) {
	case "hold", "long", "long_press", "longpress":
		if !ps.setKind(t, inmapPressHold, tok.off) {
			return false
		}
		t.holdMs = inmapHoldDefaultMs
		if strings.TrimSpace(value) == "" {
			return true
		}
		ms, ok := inmapParseDurationMs(value)
		if !ok {
			ps.fail(tok.off, "write the hold time as 500ms or 1s")
			return false
		}
		if !ps.holdRange(tok.off, ms) {
			return false
		}
		t.holdMs = ms
	case "hold_duration_ms", "hold_ms", "duration_ms":
		ms, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			ps.fail(tok.off, "hold_duration_ms takes a number of milliseconds, like 500")
			return false
		}
		if !ps.holdRange(tok.off, ms) || !ps.setKind(t, inmapPressHold, tok.off) {
			return false
		}
		t.holdMs = ms
	case "double", "double_tap", "doubletap":
		if !ps.setKind(t, inmapPressDouble, tok.off) {
			return false
		}
	case "tap", "press":
		if !ps.setKind(t, inmapPressTap, tok.off) {
			return false
		}
	default:
		ps.fail(tok.off, inmapUnknown("modifier", name, []string{"hold", "hold_duration_ms", "double"}))
		return false
	}
	return true
}

// target reads the word right of the arrow.
func (ps *inmapParser) target(tok inmapTok) (inmapTarget, bool) {
	text := tok.text
	switch inmapNormName(text) {
	case "none", "nothing", "disable", "disabled", "off", "ignore", "noop", "_":
		return inmapTarget{kind: inmapTargetNone}, true
	}
	kind, value, valueOff := "", text, tok.off
	if c := strings.IndexByte(text, ':'); c >= 0 {
		kind, value = inmapNormName(text[:c]), strings.TrimSpace(text[c+1:])
		valueOff = tok.off + c + 1
	}
	switch kind {
	case "button", "btn", "press", "pad":
		b, ok := inmapLookupButton(value)
		if !ok {
			ps.fail(valueOff, inmapUnknown("button", value, inmapButtonWords()))
			return inmapTarget{}, false
		}
		return inmapTarget{kind: inmapTargetButton, button: b}, true
	case "key", "keyboard":
		k, name, ok := inmapLookupKey(value)
		if !ok {
			ps.fail(valueOff, inmapUnknown("key", value, inmapKeyWords()))
			return inmapTarget{}, false
		}
		return inmapTarget{kind: inmapTargetKey, key: k, keyName: name}, true
	case "action", "do", "run":
		return ps.action(value, valueOff)
	case "profile", "controls", "switch":
		if value == "" {
			ps.fail(valueOff, "profile: needs next, prev, default or a profile name")
			return inmapTarget{}, false
		}
		return inmapTarget{kind: inmapTargetProfile, profile: value}, true
	case "":
		// A bare word: an action first, then a button.
		if inmapFindAction(value) != nil {
			return ps.action(value, valueOff)
		}
		if b, ok := inmapLookupButton(value); ok {
			return inmapTarget{kind: inmapTargetButton, button: b}, true
		}
	default:
		// seek:-10s, volume:+10 — an action with its value.
		if inmapFindAction(kind) != nil {
			return ps.action(text, tok.off)
		}
	}
	ps.fail(tok.off, "unknown target \""+text+"\": use button:…, key:…, action:…, profile:… or none")
	return inmapTarget{}, false
}

// action reads "name" or "name:value" as a named action.
func (ps *inmapParser) action(spec string, off int) (inmapTarget, bool) {
	name, value := spec, ""
	if c := strings.IndexByte(spec, ':'); c >= 0 {
		name, value = spec[:c], strings.TrimSpace(spec[c+1:])
	}
	a := inmapFindAction(name)
	if a == nil {
		ps.fail(off, inmapUnknown("action", name, inmapActionWords()))
		return inmapTarget{}, false
	}
	tgt := inmapTarget{kind: inmapTargetAction, action: a.name}
	switch a.arg {
	case inmapArgNone:
		if value != "" {
			ps.fail(off, a.name+" takes no value")
			return inmapTarget{}, false
		}
	case inmapArgSeconds:
		ms, ok := inmapParseDurationMs(value)
		if !ok || ms == 0 {
			ps.fail(off, a.name+" needs a time such as -10s or +30s")
			return inmapTarget{}, false
		}
		// A bare number reads as milliseconds elsewhere; here it means
		// seconds, which is what "seek:-10" can only mean.
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			ms *= 1000
		}
		tgt.arg, tgt.hasArg = float64(ms)/1000, true
	case inmapArgPercent:
		v, ok := inmapParsePercent(value)
		if !ok || v == 0 {
			ps.fail(off, a.name+" needs a step between -100 and +100, like +10")
			return inmapTarget{}, false
		}
		tgt.arg, tgt.hasArg = v, true
	}
	return tgt, true
}

// inmapUnknown is the message for a name the reader does not know, with the
// closest known word when one is near enough to be a typo.
func inmapUnknown(what, got string, known []string) string {
	msg := "unknown " + what + " \"" + strings.TrimSpace(got) + "\""
	if s := inmapSuggest(inmapNormName(got), known); s != "" {
		msg += " (did you mean " + s + "?)"
	}
	return msg
}

// inmapSuggest returns the known word closest to got, if it is at most two
// edits away and shorter than got itself.
func inmapSuggest(got string, known []string) string {
	best, bestD := "", 3
	for _, k := range known {
		d := inmapEditDistance(got, k)
		if d < bestD && d < len(got) {
			best, bestD = k, d
		}
	}
	return best
}

// inmapEditDistance is the Levenshtein distance between two short words.
func inmapEditDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// ── Validation ───────────────────────────────────────────────────────────

// inmapKnown is what validation checks names against. A nil field skips that
// check (a profile read before the config is loaded, or a unit test).
type inmapKnown struct {
	profiles map[string]bool
	scenes   []string
}

// inmapSameChord reports whether two triggers press the same buttons, in any
// order.
func inmapSameChord(buttons []inmapButton, want ...inmapButton) bool {
	if len(buttons) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, b := range buttons {
			if b == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// inmapValidate adds the problems a line-by-line read cannot see and marks
// the lines that must not run. It can be run again after other profiles were
// added: it starts from the reader's own findings each time.
func inmapValidate(p *inmapProfile, known inmapKnown) {
	if p == nil {
		return
	}
	p.problems = append(p.problems[:0:0], p.parsed...)
	add := func(line, col int, msg string, warning bool) {
		p.problems = append(p.problems, inmapProblem{line: line, col: col, msg: msg, warning: warning})
	}
	if strings.TrimSpace(p.name) == "" {
		add(1, 1, "the profile has no name: add a line like \"profile: My controls\"", true)
	}
	if known.scenes != nil {
		for _, s := range p.scenes {
			if !inmapSceneKnown(s, known.scenes) {
				add(1, 1, "no screen called \""+s+"\" in this setup", true)
			}
		}
	}
	seen := map[string]int{}
	for i := range p.bindings {
		b := &p.bindings[i]
		b.ok = true
		t := b.trigger
		bad := func(msg string) {
			add(b.line, b.col, msg, false)
			b.ok = false
		}
		if len(t.buttons) > inmapMaxChord {
			bad("a chord takes at most " + strconv.Itoa(inmapMaxChord) + " buttons")
			continue
		}
		dup := false
		for x := 0; x < len(t.buttons) && !dup; x++ {
			for y := x + 1; y < len(t.buttons); y++ {
				if t.buttons[x] == t.buttons[y] {
					dup = true
					break
				}
			}
		}
		if dup {
			bad("a chord cannot use the same button twice")
			continue
		}
		if len(t.buttons) > 1 && t.kind != inmapPressTap {
			bad("a chord cannot also be held or double-tapped")
			continue
		}
		if inmapSameChord(t.buttons, inmapBtnSelect, inmapBtnStart) {
			bad("Select + Start quits JukaHub; pick another pair")
			continue
		}
		if inmapSameChord(t.buttons, inmapBtnSelect, inmapBtnR1) {
			add(b.line, b.col, "hold Select and press R1 switches profiles: this line only runs with the switch hotkey off", true)
		}
		scopeKey := strings.ToLower(strings.Join(b.scenes, ","))
		key := inmapTriggerKey(t) + "|" + scopeKey
		if first, ok := seen[key]; ok {
			bad(inmapTriggerLabel(t) + " is already mapped on line " + strconv.Itoa(first))
			continue
		}
		seen[key] = b.line
		if b.target.kind == inmapTargetButton && len(t.buttons) == 1 && t.kind == inmapPressTap && b.target.button == t.buttons[0] {
			add(b.line, b.col, inmapButtons[t.buttons[0]].label+" is mapped to itself", true)
		}
		if b.target.kind == inmapTargetProfile && known.profiles != nil {
			switch inmapNormName(b.target.profile) {
			case "next", "prev", "previous", "default":
			default:
				if !known.profiles[strings.ToLower(strings.TrimSpace(b.target.profile))] {
					add(b.line, b.col, "no profile called \""+b.target.profile+"\" yet", true)
				}
			}
		}
		if known.scenes != nil {
			for _, s := range b.scenes {
				if !inmapSceneKnown(s, known.scenes) {
					add(b.line, b.col, "no screen called \""+s+"\" in this setup", true)
				}
			}
		}
	}
	if len(p.bindings) == 0 && !p.builtin {
		add(1, 1, "no mappings yet: add lines like \"a hold 500ms -> action:context_menu\"", true)
	}
	if !inmapProfileKeepsBack(p) {
		add(1, 1, "B and Select are both remapped on every screen, so nothing goes back. "+
			"Select + Start still quits, and hold Select + R1 switches profiles", true)
	}
	sort.SliceStable(p.problems, func(i, j int) bool {
		if p.problems[i].line != p.problems[j].line {
			return p.problems[i].line < p.problems[j].line
		}
		return p.problems[i].col < p.problems[j].col
	})
}

// inmapSceneKnown reports whether a name is a screen of this setup, by its
// config name or by the name the header shows ("Media" for Tube).
func inmapSceneKnown(name string, scenes []string) bool {
	for _, s := range scenes {
		if strings.EqualFold(s, name) || strings.EqualFold(sceneDisplayName(s), name) {
			return true
		}
	}
	return false
}

// inmapProfileKeepsBack reports whether some way back survives the profile
// on every screen: B or Select unmapped, or a line that still goes back.
func inmapProfileKeepsBack(p *inmapProfile) bool {
	if !p.everywhere() {
		return true
	}
	remapped := map[inmapButton]bool{}
	for i := range p.bindings {
		b := &p.bindings[i]
		if !b.ok || b.scenes != nil {
			continue
		}
		t := b.trigger
		if len(t.buttons) == 1 && t.kind == inmapPressTap {
			remapped[t.buttons[0]] = true
		}
		switch {
		case b.target.kind == inmapTargetButton && (b.target.button == inmapBtnB || b.target.button == inmapBtnSelect):
			return true
		case b.target.kind == inmapTargetAction && b.target.action == "back":
			return true
		}
	}
	return !remapped[inmapBtnB] || !remapped[inmapBtnSelect]
}

// ── Labels ───────────────────────────────────────────────────────────────

// inmapTriggerKey identifies a trigger for the duplicate check: two holds of
// one button are the same trigger whatever their times.
func inmapTriggerKey(t inmapTrigger) string {
	var sb strings.Builder
	for i, b := range t.buttons {
		if i > 0 {
			sb.WriteByte('+')
		}
		sb.WriteString(inmapButtons[b].name)
	}
	switch t.kind {
	case inmapPressHold:
		sb.WriteString(" hold")
	case inmapPressDouble:
		sb.WriteString(" double")
	}
	return sb.String()
}

// inmapTriggerLabel is a trigger as the editor shows it: "Hold A · 0.5 s",
// "Double-tap Y", "L1 + A".
func inmapTriggerLabel(t inmapTrigger) string {
	parts := make([]string, 0, len(t.buttons))
	for _, b := range t.buttons {
		parts = append(parts, inmapButtons[b].label)
	}
	label := strings.Join(parts, " + ")
	switch t.kind {
	case inmapPressHold:
		return "Hold " + label + " · " + inmapSecondsLabel(float64(t.holdMs)/1000)
	case inmapPressDouble:
		return "Double-tap " + label
	}
	return label
}

// inmapSecondsLabel writes a time in seconds without trailing zeros: "0.5 s".
func inmapSecondsLabel(sec float64) string {
	return strconv.FormatFloat(sec, 'f', -1, 64) + " s"
}

// inmapTargetLabel is a target as the editor shows it.
func inmapTargetLabel(t inmapTarget) string {
	switch t.kind {
	case inmapTargetButton:
		return "Button " + inmapButtons[t.button].label
	case inmapTargetKey:
		return "Key " + t.keyName
	case inmapTargetAction:
		a := inmapFindAction(t.action)
		label := t.action
		if a != nil {
			label = a.label
		}
		if !t.hasArg {
			return label
		}
		sign := "+"
		if t.arg < 0 {
			sign = "−"
		}
		v := math.Abs(t.arg)
		if a != nil && a.arg == inmapArgSeconds {
			return label + " " + sign + inmapSecondsLabel(v)
		}
		return label + " " + sign + strconv.FormatFloat(v, 'f', -1, 64) + "%"
	case inmapTargetProfile:
		switch inmapNormName(t.profile) {
		case "next":
			return "Next profile"
		case "prev", "previous":
			return "Previous profile"
		case "default":
			return "Default profile"
		}
		return "Profile: " + t.profile
	}
	return "Nothing"
}
