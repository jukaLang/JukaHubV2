package main

// webpage_hints.go — what old HTML says with attributes, and the CSS idioms the
// reader used to draw wrongly.
//
// HTML still carries a presentation layer of its own: bgcolor, align, width,
// border, cellpadding and cellspacing on tables, <font color size face>, the
// text and link colours on <body>, a list's type. Hacker News, forums, mailing
// list archives and most pages written before CSS layout are built from them,
// and the reader ignored every one: a table's coloured header bar, its widths
// and its spacing were lost, and so was the red of a <font color> warning.
// Browsers turn them into CSS "presentational hints" — author declarations with
// zero specificity placed before every style sheet, so any rule or style=""
// for the same property still wins. webPresentationalHints builds them.
//
// The rest of the file holds three idioms of modern sheets:
//   - `rem` follows the root element's font size. `html { font-size: 62.5% }`
//     with `body { font-size: 1.6rem }` is a common reset; reading rem as 16px
//     drew such a page at 160 %.
//   - `-webkit-line-clamp` / `line-clamp` cut a card's title or summary after
//     N lines and end the last kept line with "…".
//   - `background-clip: text` gradient headings: the ramp shows through the
//     glyphs. The reader cannot clip a ramp to glyph shapes, so the text takes
//     the ramp's colour and the box paints no slab behind it.

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// webPresentationalHints returns the CSS an element's presentation attributes
// stand for, nil when there is none. The attribute list is parsed once and
// shared with the selector matcher. A cell also reads its table's cellpadding
// and border; <body link> is remembered for the links that follow it.
func (p *webParser) webPresentationalHints(el *webElement) map[string]string {
	var hints map[string]string
	set := func(name, value string) {
		if hints == nil {
			hints = make(map[string]string, 4)
		}
		hints[name] = value
	}
	attr := func(name string) (string, bool) {
		value, ok := el.attr(name)
		return strings.TrimSpace(value), ok
	}
	color := func(name, property string) {
		if v, ok := attr(name); ok {
			if c, valid := webLegacyColor(v); valid {
				set(property, c)
			}
		}
	}
	length := func(name, property string, pct bool) {
		if v, ok := attr(name); ok {
			if l, valid := webLegacyLength(v, pct); valid {
				set(property, l)
			}
		}
	}
	// align centres a block's text; on a div or a table part it also centres
	// the boxes inside it (Chrome's -webkit-center), which is how <div
	// align=center><table width=80%> puts the table in the middle.
	align := func(centerBlocks bool) {
		if v, ok := attr("align"); ok {
			if a, valid := webLegacyTextAlign(v, centerBlocks); valid {
				set("text-align", a)
			}
		}
	}
	if el.tag == "td" || el.tag == "th" {
		if table, ok := p.webOpenTable(); ok {
			if v, has := table.attr("cellpadding"); has {
				if l, valid := webLegacyLength(strings.TrimSpace(v), false); valid {
					set("padding", l)
				}
			}
			if webLegacyTableBorder(&table) > 0 {
				set("border", "1px solid "+webLegacyTableBorderColor(&table))
			}
		}
	}
	if el.attrs == nil {
		return hints
	}
	// The common tags only look for one attribute; a scan of the raw tag text
	// keeps their attribute lists unparsed when it is not there.
	mentions := func(name string) bool { return webIndexFold(el.attrs.raw, name) >= 0 }
	switch el.tag {
	case "body":
		color("bgcolor", "background-color")
		color("text", "color")
		if v, ok := attr("link"); ok {
			if c, valid := webLegacyColor(v); valid {
				p.linkColor = c
			}
		}
	case "a":
		if p.linkColor != "" && el.hasAttr("href") {
			set("color", p.linkColor)
		}
	case "font":
		color("color", "color")
		if v, ok := attr("size"); ok {
			if size, valid := webLegacyFontSize(v); valid {
				set("font-size", size)
			}
		}
		if v, ok := attr("face"); ok && v != "" {
			set("font-family", v)
		}
	case "table":
		color("bgcolor", "background-color")
		length("width", "width", true)
		if v, ok := attr("cellspacing"); ok {
			if l, valid := webLegacyLength(v, false); valid {
				set("border-spacing", l)
			}
		}
		if n := webLegacyTableBorder(el); n > 0 {
			set("border", strconv.Itoa(n)+"px solid "+webLegacyTableBorderColor(el))
		}
		if v, ok := attr("align"); ok && strings.EqualFold(v, "center") {
			set("margin-left", "auto")
			set("margin-right", "auto")
		}
	case "tr", "thead", "tbody", "tfoot":
		color("bgcolor", "background-color")
		align(true)
	case "td", "th":
		color("bgcolor", "background-color")
		align(true)
		length("width", "width", true)
		length("height", "height", false)
	case "div", "caption", "legend":
		if mentions("align") {
			align(true)
		}
	case "p", "h1", "h2", "h3", "h4", "h5", "h6":
		if mentions("align") {
			align(false)
		}
	case "img":
		if mentions("align") {
			v, _ := attr("align")
			switch strings.ToLower(v) {
			case "left":
				set("float", "left")
			case "right":
				set("float", "right")
			case "middle", "absmiddle", "center":
				set("vertical-align", "middle")
			case "top", "texttop":
				set("vertical-align", "top")
			}
		}
	case "hr":
		length("width", "width", true)
		if v, ok := attr("size"); ok {
			if l, valid := webLegacyLength(v, false); valid {
				set("border-width", l)
			}
		}
		if v, ok := attr("color"); ok {
			if c, valid := webLegacyColor(v); valid {
				set("border-color", c)
				set("background-color", c)
			}
		} else if el.hasAttr("noshade") {
			set("border-color", "#808080")
		}
	case "ol", "ul", "li":
		if mentions("type") {
			v, _ := attr("type")
			if t, valid := webLegacyListType(v); valid {
				set("list-style-type", t)
			}
		}
	case "abbr", "acronym":
		if mentions("title") && el.hasAttr("title") {
			set("text-decoration", "underline dotted")
		}
	}
	return hints
}

// webOpenTable is the innermost <table> element the parser is inside.
func (p *webParser) webOpenTable() (webElement, bool) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].el.tag == "table" {
			return p.stack[i].el, true
		}
	}
	return webElement{}, false
}

// webLegacyTableBorder is a table's border attribute in pixels: a bare
// `border` (or one that is not a number) means 1, `border="0"` means none.
func webLegacyTableBorder(table *webElement) int {
	v, ok := table.attr("border")
	if !ok {
		return 0
	}
	v = strings.TrimSpace(v)
	end := 0
	for end < len(v) && end < 4 && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return 1
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return 1
	}
	return min(n, 16)
}

// webLegacyTableBorderColor is the colour of a bordered table's edges: its
// bordercolor attribute, or the grey browsers draw.
func webLegacyTableBorderColor(table *webElement) string {
	if v, ok := table.attr("bordercolor"); ok {
		if c, valid := webLegacyColor(strings.TrimSpace(v)); valid {
			return c
		}
	}
	return "#808080"
}

// webLegacyColor reads a colour attribute the way pages write them: a CSS
// colour, or six or three hex digits without the '#' (bgcolor="ff6600").
func webLegacyColor(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" || strings.EqualFold(v, "transparent") || len(v) > 64 {
		return "", false
	}
	if _, ok := parseWebColor(v); ok {
		return v, true
	}
	hex := strings.TrimPrefix(v, "#")
	if len(hex) != 3 && len(hex) != 6 {
		return "", false
	}
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return "#" + hex, true
}

// webLegacyLength reads a width/height/cellpadding attribute: its leading
// number in pixels, or a percentage where pct allows one ("85%").
func webLegacyLength(value string, pct bool) (string, bool) {
	v := strings.TrimSpace(value)
	isPct := strings.HasSuffix(v, "%")
	end := 0
	for end < len(v) && (v[end] >= '0' && v[end] <= '9' || v[end] == '.') {
		end++
	}
	if end == 0 {
		return "", false
	}
	n, err := strconv.ParseFloat(v[:end], 64)
	if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return "", false
	}
	if isPct {
		if !pct || n <= 0 {
			return "", false
		}
		return strconv.FormatFloat(math.Min(n, 100), 'f', -1, 64) + "%", true
	}
	return strconv.FormatFloat(math.Min(n, 10000), 'f', -1, 64) + "px", true
}

// webLegacyFontSize maps <font size> (1–7, or +n/−n from the default 3) to the
// CSS size keyword HTML gives it.
func webLegacyFontSize(value string) (string, bool) {
	v := strings.TrimSpace(value)
	sign := byte(0)
	if v != "" && (v[0] == '+' || v[0] == '-') {
		sign, v = v[0], v[1:]
	}
	end := 0
	for end < len(v) && end < 3 && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return "", false
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return "", false
	}
	switch sign {
	case '+':
		n = 3 + n
	case '-':
		n = 3 - n
	}
	n = max(1, min(n, 7))
	return [...]string{"", "x-small", "small", "medium", "large", "x-large", "xx-large", "48px"}[n], true
}

// webLegacyTextAlign maps an align attribute to text-align. center/middle on
// an element whose boxes centre too becomes -webkit-center.
func webLegacyTextAlign(value string, centerBlocks bool) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "left":
		return "left", true
	case "right":
		return "right", true
	case "justify":
		return "justify", true
	case "center", "middle":
		if centerBlocks {
			return "-webkit-center", true
		}
		return "center", true
	}
	return "", false
}

// webLegacyListType maps a list's type attribute. The numbering letters are
// case-sensitive ("a" counts a, b, c and "A" counts A, B, C); the bullet names
// are not.
func webLegacyListType(value string) (string, bool) {
	switch v := strings.TrimSpace(value); v {
	case "1":
		return "decimal", true
	case "a":
		return "lower-alpha", true
	case "A":
		return "upper-alpha", true
	case "i":
		return "lower-roman", true
	case "I":
		return "upper-roman", true
	}
	switch v := strings.ToLower(strings.TrimSpace(value)); v {
	case "disc", "circle", "square", "none":
		return v, true
	}
	return "", false
}

// webInheritKeyword reports `inherit` or `unset`, which on an inherited
// property (font-size, font-weight, color, text-align …) both mean the
// parent's value.
func webInheritKeyword(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "inherit", "unset":
		return true
	}
	return false
}

// --- rem --------------------------------------------------------------------

// webSubstituteRootEm rewrites every numeric rem length in value as px against
// the root element's font size. Quoted strings and url() payloads are copied
// untouched, and a number glued to an identifier ("h2rem") is not a length.
func webSubstituteRootEm(value string, rootPx float64) string {
	lower := strings.ToLower(value)
	if !strings.Contains(lower, "rem") || rootPx <= 0 {
		return value
	}
	glued := func(at int) bool {
		if at <= 0 {
			return false
		}
		b := value[at-1]
		if b == '-' || b == '+' {
			return at >= 2 && webRootEmIdentByte(value[at-2])
		}
		return webRootEmIdentByte(b)
	}
	var out strings.Builder
	out.Grow(len(value) + 8)
	for i := 0; i < len(value); {
		c := value[i]
		if c == '"' || c == '\'' {
			end := i + 1
			for end < len(value) && value[end] != c {
				if value[end] == '\\' {
					end++
				}
				end++
			}
			end = min(end+1, len(value))
			out.WriteString(value[i:end])
			i = end
			continue
		}
		if strings.HasPrefix(lower[i:], "url(") {
			end := webCSSParenEnd(value, i+3)
			if end < 0 {
				out.WriteString(value[i:])
				break
			}
			out.WriteString(value[i : end+1])
			i = end + 1
			continue
		}
		if (c >= '0' && c <= '9' || c == '.') && !glued(i) {
			start := i
			for i < len(value) && (value[i] >= '0' && value[i] <= '9' || value[i] == '.') {
				i++
			}
			if strings.HasPrefix(lower[i:], "rem") && (i+3 == len(value) || !webRootEmIdentByte(value[i+3])) {
				if n, err := strconv.ParseFloat(value[start:i], 64); err == nil {
					out.WriteString(strconv.FormatFloat(n*rootPx, 'f', -1, 64))
					out.WriteString("px")
					i += 3
					continue
				}
			}
			out.WriteString(value[start:i])
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func webRootEmIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '#' || c == '\\'
}

// --- line clamp ---------------------------------------------------------------

// webLineClampValue reads line-clamp, or -webkit-line-clamp on the legacy
// vertical box it belongs to (display: -webkit-box; -webkit-box-orient:
// vertical), which is the only place browsers honour the prefixed form. ok is
// false when the element states no usable clamp; `none` is a valid 0.
func webLineClampValue(decls map[string]string) (int, bool) {
	raw, ok := decls["line-clamp"]
	if !ok {
		if raw, ok = decls["-webkit-line-clamp"]; !ok || !webLegacyVerticalBox(decls) {
			return 0, false
		}
	}
	fields := strings.Fields(strings.ToLower(raw))
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "none", "initial", "unset":
		return 0, true
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n < 1 {
		return 0, false
	}
	return min(n, 1000), true
}

// webLegacyVerticalBox reports the line-clamp idiom's box: display -webkit-box
// (or -webkit-inline-box) with a vertical box orient. It lays its content out
// as lines, like a block, not as a row of its inline children.
func webLegacyVerticalBox(decls map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(decls["display"])) {
	case "-webkit-box", "-webkit-inline-box":
		return strings.EqualFold(strings.TrimSpace(decls["-webkit-box-orient"]), "vertical")
	}
	return false
}

// webClampEllipsis ends a clamped block's last kept line with "…": appended to
// its last word when that still fits before right, otherwise the word gives
// up letters until it does. A line that ends in an image or a form control is
// left as it is.
func webClampEllipsis(runs []webRunPos, right int32) {
	for k := len(runs) - 1; k >= 0; k-- {
		r := &runs[k]
		if r.Image != nil || r.Control != nil || r.BoxW > 0 || r.Font == nil {
			return
		}
		text := strings.TrimRight(r.Text, " ")
		if text == "" {
			continue
		}
		fits := func(s string) bool {
			w, _, err := r.Font.SizeUTF8(s)
			return err == nil && r.X+int32(w) <= right
		}
		if fits(text + "…") {
			r.Text = text + "…"
			return
		}
		for text != "" {
			_, size := utf8.DecodeLastRuneInString(text)
			text = strings.TrimRight(text[:len(text)-size], " ")
			if fits(text + "…") {
				break
			}
		}
		r.Text = text + "…"
		return
	}
}

// --- gradient text ------------------------------------------------------------

// webApplyTextClip handles the gradient-heading idiom and the text fill colour.
// With `background-clip: text` the background shows only through the glyphs,
// so the text takes the ramp's average colour (an image fill keeps the text
// colour) and no background box is painted. Otherwise a visible
// -webkit-text-fill-color is the colour the glyphs are filled with.
func webApplyTextClip(st *webStyle, decls map[string]string) {
	clip := false
	for _, key := range [...]string{"background-clip", "-webkit-background-clip"} {
		if v, ok := decls[key]; ok {
			for _, part := range strings.Split(strings.ToLower(v), ",") {
				if strings.TrimSpace(part) == "text" {
					clip = true
				}
			}
		}
	}
	if !clip {
		if v, ok := decls["-webkit-text-fill-color"]; ok {
			if c, valid := parseWebColor(v); valid && c.A > 0 {
				st.Color, st.HasColor = c, true
			}
		}
		return
	}
	if st.HasGradient && st.Gradient != nil {
		st.Color, st.HasColor = webGradientAverage(*st.Gradient), true
	} else if st.HasColor && st.Color.A == 0 {
		// A transparent fill over an image: keep the text readable in the
		// page's own colour.
		st.HasColor = false
	}
	st.HasBg, st.HasGradient, st.Gradient = false, false, nil
	st.BackgroundImage, st.BackgroundSize, st.BackgroundPosition, st.BackgroundRepeat = "", "", "", ""
	st.BackgroundLayers = [webMaxBackgroundLayers]webBackgroundLayerStyle{}
	st.BackgroundLayerCount = 0
}

// --- baseline shift -----------------------------------------------------------

// webApplyBaselineShift adds an element's own raise or drop to the shift it
// inherited (CSS px, positive is down): vertical-align super/sub moves the
// baseline by a third / a fifth of the parent's font size, and a relatively
// positioned inline box by its top or bottom (normalize.css and Bootstrap draw
// <sup> as `position: relative; top: -.5em`). A block resets it when it is
// pushed (webParser.push); inline descendants move with their parent.
func webApplyBaselineShift(st *webStyle, parentFontPx float64) {
	switch st.VerticalAlign {
	case webVerticalSuper:
		st.BaselineShift -= parentFontPx / 3
	case webVerticalSub:
		st.BaselineShift += parentFontPx / 5
	}
	// A percentage inset is of the containing block's height, which an inline
	// box does not know; it is left alone rather than guessed.
	if st.Position == webPositionRelative {
		switch {
		case st.HasTop:
			if st.InsetPct[0] == 0 {
				st.BaselineShift += st.Top
			}
		case st.HasBottom:
			if st.InsetPct[2] == 0 {
				st.BaselineShift -= st.Bottom
			}
		}
	}
	st.BaselineShift = math.Max(-64, math.Min(64, st.BaselineShift))
}
