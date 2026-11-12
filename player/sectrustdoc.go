package main

// sectrustdoc.go — reading SECURITY.md on the device.
//
// Security & trust links every row to the section of SECURITY.md that covers
// it, and the whole document opens in a reader: headings, paragraphs wrapped to
// the screen, bullets, code blocks on a recessed panel, tables as rows, a
// contents list (Y) and paging on the shoulders. The file ships next to the
// binary in every package (package-sdcard.sh, release.sh, the release
// workflow); a development build finds it at the repository root one folder up.
// When it is missing — an old package, a hand-copied binary — a short built-in
// summary is shown with the address of the full document, as a QR code on the
// handheld.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// sectrustDocURL is the published document.
const sectrustDocURL = "https://github.com/jukaLang/JukaHubV2/blob/main/SECURITY.md"

// sectrustDocFallback is shown when SECURITY.md is not next to the app.
const sectrustDocFallback = `# JukaHub Security — summary

The full SECURITY.md was not found next to the app, so this is the short version. The complete document is at github.com/jukaLang/JukaHubV2 → SECURITY.md.

## Reporting a vulnerability

Do not open a public issue for a security problem. Use GitHub's private vulnerability reporting on the JukaHubV2 repository (Security → Report a vulnerability), with what you did, what happened and the version.

## Secrets

- API keys and tokens in jukaconfig.json are stored encrypted with AES-256-GCM.
- Set JUKAHUB_CRYPTO_KEY so they are encrypted with your own key instead of the built-in one.
- Security & trust warns when a secret is stored in plaintext.

## Downloads and updates

- Updates and packages come from a repository index signed with Ed25519; every file is checked against its SHA-256 before anything is replaced, and a rollback copy is kept.
- Helper, Toolbox and Audit toolkit downloads only come from approved project releases written into the app, and are checked against the SHA-256 the project publishes whenever it publishes one.

## Servers on your network

- FTP, SFTP, the Phone Companion and a hosted Watch Party are reachable from your Wi-Fi while they run. FTP and SFTP need the password shown on the device, new on every start, and stop on their own after 15 idle minutes or on another network; FTP is unencrypted and shares only games, saves and media; SFTP adds a host key; the Phone Companion needs the on-screen PIN.
- Stop servers you are not using, especially on public networks.

## What is not trusted

- The SD card: configs, plugins, ROMs and saves from someone else's card are untrusted input.
- Plugins and script snippets run with the app's own permissions — on the handheld, root.
- Anonymous diagnostics are off by default; when enabled, every ten launches
  file a public informational issue. Manual bug reports send what you review.
`

// sectrustMDKind is one Markdown block type the reader draws.
type sectrustMDKind int

const (
	sectrustMDPara sectrustMDKind = iota
	sectrustMDH1
	sectrustMDH2
	sectrustMDH3
	sectrustMDBullet
	sectrustMDCode
	sectrustMDTable
	sectrustMDRule
	sectrustMDQuote
)

// sectrustMDBlock is one parsed block.
type sectrustMDBlock struct {
	kind   sectrustMDKind
	text   string
	depth  int
	header bool
}

var (
	sectrustMDImage   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`) })
	sectrustMDLink    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`) })
	sectrustMDBold    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\*\*([^*]+)\*\*`) })
	sectrustMDComment = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`<!--.*?-->`) })
	sectrustMDTag     = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)</?(?:br|b|i|em|strong|code|kbd|sup|sub|details|summary|p|span|div)\b[^>]*>`)
	})
	sectrustMDBulletRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(\s*)(?:[-*+]|\d+[.)])\s+(.*)$`) })
	sectrustMDSpaces   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s{2,}`) })
)

// sectrustInline reduces inline Markdown to the text a reader shows: link and
// image text without their targets, bold and code markers dropped, simple HTML
// tags removed. Placeholders such as <keyID> are left alone.
func sectrustInline(s string) string {
	s = sectrustMDComment().ReplaceAllString(s, "")
	s = sectrustMDImage().ReplaceAllString(s, "$1")
	s = sectrustMDLink().ReplaceAllString(s, "$1")
	s = sectrustMDBold().ReplaceAllString(s, "$1")
	s = sectrustMDTag().ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "`", "")
	s = sectrustMDSpaces().ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// sectrustTableCells splits one table line into its cells.
func sectrustTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

// sectrustTableSeparator reports a |---|:---:| line.
func sectrustTableSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" || !strings.Contains(c, "-") {
			return false
		}
	}
	return true
}

// sectrustParseMarkdown turns a document into the blocks the reader lays out.
// It covers what the project's documents use: ATX headings, paragraphs, lists
// (nested by indent), fenced code, pipe tables, rules and quotes.
func sectrustParseMarkdown(src string) []sectrustMDBlock {
	var out []sectrustMDBlock
	var para []string
	flush := func() {
		if len(para) > 0 {
			if text := sectrustInline(strings.Join(para, " ")); text != "" {
				out = append(out, sectrustMDBlock{kind: sectrustMDPara, text: text})
			}
			para = nil
		}
	}
	inCode := false
	var code []string
	for _, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if inCode {
				out = append(out, sectrustMDBlock{kind: sectrustMDCode, text: strings.Join(code, "\n")})
				code = nil
				inCode = false
				// A closing fence glued to the next sentence ("```If you
				// pass…") still closes the block, and the sentence starts a
				// paragraph instead of vanishing.
				if rest := strings.TrimSpace(strings.TrimLeft(trimmed, "`~")); rest != "" {
					para = append(para, rest)
				}
			} else {
				flush()
				inCode = true
			}
			continue
		}
		if inCode {
			code = append(code, strings.ReplaceAll(line, "\t", "    "))
			continue
		}
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "#"):
			flush()
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			text := sectrustInline(strings.TrimSpace(trimmed[level:]))
			kind := sectrustMDH3
			switch level {
			case 1:
				kind = sectrustMDH1
			case 2:
				kind = sectrustMDH2
			}
			if text != "" {
				out = append(out, sectrustMDBlock{kind: kind, text: text})
			}
		case strings.HasPrefix(trimmed, "|"):
			flush()
			cells := sectrustTableCells(trimmed)
			if sectrustTableSeparator(cells) {
				if n := len(out); n > 0 && out[n-1].kind == sectrustMDTable {
					out[n-1].header = true
				}
				continue
			}
			for i := range cells {
				cells[i] = sectrustInline(cells[i])
			}
			out = append(out, sectrustMDBlock{kind: sectrustMDTable, text: strings.Join(cells, "  ·  ")})
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			flush()
			out = append(out, sectrustMDBlock{kind: sectrustMDRule})
		case strings.HasPrefix(trimmed, ">"):
			flush()
			out = append(out, sectrustMDBlock{kind: sectrustMDQuote, text: sectrustInline(strings.TrimLeft(trimmed, "> "))})
		case sectrustMDBulletRe().MatchString(line):
			flush()
			m := sectrustMDBulletRe().FindStringSubmatch(line)
			depth := len(strings.ReplaceAll(m[1], "\t", "  ")) / 2
			if depth > 3 {
				depth = 3
			}
			out = append(out, sectrustMDBlock{kind: sectrustMDBullet, text: sectrustInline(m[2]), depth: depth})
		default:
			// An indented line right under a bullet continues that bullet.
			if len(para) == 0 && strings.HasPrefix(raw, " ") {
				if n := len(out); n > 0 && out[n-1].kind == sectrustMDBullet {
					out[n-1].text += " " + sectrustInline(trimmed)
					continue
				}
			}
			para = append(para, trimmed)
		}
	}
	flush()
	if inCode && len(code) > 0 {
		out = append(out, sectrustMDBlock{kind: sectrustMDCode, text: strings.Join(code, "\n")})
	}
	return out
}

// ── Loading ──────────────────────────────────────────────────────────────

// sectrustDocPaths are where SECURITY.md is looked for: next to the binary
// (every package ships it there), then one folder up (a development build in
// player/ sits under the repository root), then the working folder.
func sectrustDocPaths() []string {
	var out []string
	if dir := exeDir(); dir != "" {
		out = append(out, filepath.Join(dir, "SECURITY.md"), filepath.Join(dir, "..", "SECURITY.md"))
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		out = append(out, filepath.Join(wd, "SECURITY.md"), filepath.Join(wd, "..", "SECURITY.md"))
	}
	return out
}

// sectrustLoadDoc reads SECURITY.md, or returns the built-in summary with an
// empty source.
func sectrustLoadDoc() (text, source string) {
	for _, p := range sectrustDocPaths() {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 2<<20 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return string(data), p
	}
	return sectrustDocFallback, ""
}

// ── Reader state ─────────────────────────────────────────────────────────

// sectrustDocLine is one laid-out line.
type sectrustDocLine struct {
	kind   sectrustMDKind
	text   string
	x      int32
	y      int32
	h      int32
	first  bool
	header bool
	anchor string
}

type sectrustDocState struct {
	open     bool
	loading  bool
	source   string
	blocks   []sectrustMDBlock
	headings []string
	lines    []sectrustDocLine
	layoutW  int32
	layoutF  *ttf.Font
	height   int32
	nav      tbxNav
	toc      bool
	tocNav   tbxNav
	pending  string
	pageText string
}

var sectrustDoc sectrustDocState

// sectrustOpenDoc opens the reader, at section when it names a heading.
func sectrustOpenDoc(section string) {
	sectrustDoc.open = true
	sectrustDoc.toc = false
	sectrustDoc.pending = section
	sectrustDoc.nav.scroll = 0
	if sectrustDoc.blocks != nil || sectrustDoc.loading {
		return
	}
	sectrustDoc.loading = true
	go func() {
		text, source := sectrustLoadDoc()
		blocks := sectrustParseMarkdown(text)
		tbxPost(func() {
			sectrustDoc.loading = false
			sectrustDoc.source = source
			sectrustDoc.blocks = blocks
			sectrustDoc.lines = nil
			sectrustDoc.headings = sectrustDoc.headings[:0]
			for _, b := range blocks {
				if b.kind == sectrustMDH2 {
					sectrustDoc.headings = append(sectrustDoc.headings, b.text)
				}
			}
		})
	}()
}

// sectrustDocFonts are the reader's sizes.
type sectrustDocFonts struct {
	h1, h2, h3, body, small *ttf.Font
}

func sectrustLoadDocFonts(config *Config, fonts tbxFonts) sectrustDocFonts {
	f := sectrustDocFonts{
		h1:    FontForRole(config, TypeTitle),
		h2:    FontForRole(config, TypeHeading),
		h3:    FontForRole(config, TypeBody),
		body:  fonts.body,
		small: fonts.small,
	}
	if f.h1 == nil {
		f.h1 = fonts.title
	}
	if f.h2 == nil {
		f.h2 = fonts.title
	}
	if f.h3 == nil {
		f.h3 = fonts.body
	}
	return f
}

func (f sectrustDocFonts) forKind(k sectrustMDKind) *ttf.Font {
	switch k {
	case sectrustMDH1:
		return f.h1
	case sectrustMDH2:
		return f.h2
	case sectrustMDH3:
		return f.h3
	case sectrustMDCode, sectrustMDTable:
		return f.small
	}
	return f.body
}

// sectrustLayoutDoc lays every block out for a column width.
func sectrustLayoutDoc(f sectrustDocFonts, width int32) {
	lines := sectrustDoc.lines[:0]
	y := int32(0)
	add := func(kind sectrustMDKind, text string, x, h int32, first, header bool, anchor string) {
		lines = append(lines, sectrustDocLine{kind: kind, text: text, x: x, y: y, h: h, first: first, header: header, anchor: anchor})
		y += h
	}
	wrap := func(font *ttf.Font, text string, w int32) []string {
		if w < 80 {
			w = 80
		}
		return tbxWrapByMeasure(text, w, func(s string) int32 {
			sw, _, err := font.SizeUTF8(s)
			if err != nil {
				return 0
			}
			return int32(sw)
		})
	}
	for _, b := range sectrustDoc.blocks {
		font := f.forKind(b.kind)
		lh := int32(font.Height()) + 4
		switch b.kind {
		case sectrustMDH1:
			for i, line := range wrap(font, b.text, width) {
				add(b.kind, line, 0, lh, i == 0, false, b.text)
			}
			y += 12
		case sectrustMDH2:
			y += 22
			for i, line := range wrap(font, b.text, width-16) {
				add(b.kind, line, 16, lh, i == 0, false, b.text)
			}
			y += 8
		case sectrustMDH3:
			y += 14
			for i, line := range wrap(font, b.text, width) {
				add(b.kind, line, 0, lh, i == 0, false, b.text)
			}
			y += 4
		case sectrustMDBullet:
			indent := int32(22 + b.depth*22)
			for i, line := range wrap(font, b.text, width-indent) {
				add(b.kind, line, indent, lh, i == 0, false, "")
			}
			y += 4
		case sectrustMDCode:
			y += 6
			start := len(lines)
			for i, line := range wrap(font, b.text, width-36) {
				add(b.kind, line, 18, lh, i == 0, false, "")
			}
			if len(lines) > start {
				// The panel gets 8px of air above the first line and below
				// the last: the first line grows and draws its text 8px
				// lower, so every line after it moves down with it.
				lines[start].h += 8
				for i := start + 1; i < len(lines); i++ {
					lines[i].y += 8
				}
				lines[len(lines)-1].h += 8
				y += 16
			}
			y += 8
		case sectrustMDTable:
			for i, line := range wrap(font, b.text, width-28) {
				add(b.kind, line, 14, lh+4, i == 0, b.header, "")
			}
		case sectrustMDRule:
			y += 10
			add(b.kind, "", 0, 2, true, false, "")
			y += 10
		case sectrustMDQuote:
			for i, line := range wrap(font, b.text, width-24) {
				add(b.kind, line, 20, lh, i == 0, false, "")
			}
			y += 8
		default:
			for i, line := range wrap(font, b.text, width) {
				add(b.kind, line, 0, lh, i == 0, false, "")
			}
			y += 12
		}
	}
	sectrustDoc.lines = lines
	sectrustDoc.height = y
	sectrustDoc.layoutW = width
	sectrustDoc.layoutF = f.body
}

// ── Render ───────────────────────────────────────────────────────────────

func sectrustRenderDoc(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	sub := "How JukaHub protects your data, and how to report a problem"
	if sectrustDoc.source == "" && !sectrustDoc.loading {
		sub = "Summary · SECURITY.md was not found next to the app"
	}
	header := tbxHeader("SECURITY.md", sub, "shield", sectrustDoc.pageText, ColorTextTertiary())
	if sectrustDoc.toc {
		header = tbxHeader("Contents", "SECURITY.md", "list", "", sdl.Color{})
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	if sectrustDoc.loading {
		tbxDrawEmpty(renderer, config, body, "shield", "Opening SECURITY.md…", "")
		tbxEndFrame(renderer, config)
		return
	}
	if sectrustDoc.toc {
		sectrustRenderToc(renderer, config, fonts, frame)
		tbxEndFrame(renderer, config)
		return
	}
	df := sectrustLoadDocFonts(config, fonts)
	colW := frame.w
	if colW > 940 {
		colW = 940
	}
	colX := frame.x + (frame.w-colW)/2
	if sectrustDoc.lines == nil || sectrustDoc.layoutW != colW || sectrustDoc.layoutF != df.body {
		sectrustLayoutDoc(df, colW)
	}
	nav := &sectrustDoc.nav
	nav.begin(body.Y, body.H)
	nav.extend(sectrustDoc.height + 24)
	if sectrustDoc.pending != "" {
		for _, l := range sectrustDoc.lines {
			if l.anchor != "" && strings.EqualFold(l.anchor, sectrustDoc.pending) {
				nav.scroll = l.y - 8
				break
			}
		}
		sectrustDoc.pending = ""
	}
	nav.end()
	pages := (nav.contentH + body.H - 1) / body.H
	if pages < 1 {
		pages = 1
	}
	page := (nav.scroll+body.H/2)/body.H + 1
	if page > pages {
		page = pages
	}
	if text := sectrustItoa(page) + " / " + sectrustItoa(pages); text != sectrustDoc.pageText {
		sectrustDoc.pageText = text
	}

	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	for _, l := range sectrustDoc.lines {
		y := top + l.y
		if y+l.h < body.Y || y > body.Y+body.H {
			continue
		}
		sectrustDrawDocLine(renderer, config, df, l, colX, y, colW)
	}
	tbxUnclip(renderer)
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// sectrustItoa formats a small count without fmt.
func sectrustItoa(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sectrustDrawDocLine draws one line in its block's style.
func sectrustDrawDocLine(renderer *sdl.Renderer, config *Config, f sectrustDocFonts, l sectrustDocLine, colX, y, colW int32) {
	font := f.forKind(l.kind)
	x := colX + l.x
	switch l.kind {
	case sectrustMDH1:
		renderText(renderer, config, font, l.text, ColorTextPrimary(), x, y)
	case sectrustMDH2:
		if l.first {
			fillRoundedRect(renderer, colX, y+4, 4, l.h-8, 2, glassAccent())
		}
		renderText(renderer, config, font, l.text, ColorTextPrimary(), x, y)
	case sectrustMDH3:
		renderText(renderer, config, font, l.text, glassAccent(), x, y)
	case sectrustMDBullet:
		if l.first {
			fillCircle(renderer, x-12, y+int32(font.Height())/2+1, 3, glassAccent())
		}
		renderText(renderer, config, font, l.text, ColorTextSecondary(), x, y)
	case sectrustMDCode:
		setDrawColor(renderer, glassA(14))
		renderer.FillRect(&sdl.Rect{X: colX, Y: y, W: colW, H: l.h})
		ty := y
		if l.first {
			ty += 8
		}
		renderText(renderer, config, font, l.text, ColorTextPrimary(), x, ty)
	case sectrustMDTable:
		if l.header && l.first {
			fillRoundedRect(renderer, colX, y, colW, l.h, 6, glassA(16))
		} else if l.first {
			setDrawColor(renderer, glassHairline())
			renderer.FillRect(&sdl.Rect{X: colX, Y: y, W: colW, H: 1})
		}
		color := ColorTextSecondary()
		if l.header {
			color = ColorTextPrimary()
		}
		renderText(renderer, config, font, l.text, color, x, y+2)
	case sectrustMDRule:
		setDrawColor(renderer, glassHairline())
		renderer.FillRect(&sdl.Rect{X: colX, Y: y, W: colW, H: 1})
	case sectrustMDQuote:
		fillRoundedRect(renderer, colX+4, y, 3, l.h, 1, WithAlpha(glassAccent(), 160))
		renderText(renderer, config, font, l.text, ColorTextMuted(), x, y)
	default:
		renderText(renderer, config, font, l.text, ColorTextSecondary(), x, y)
	}
}

// sectrustRenderToc draws the contents list: one row per section.
func sectrustRenderToc(renderer *sdl.Renderer, config *Config, fonts tbxFonts, frame tbxFrame) {
	body := frame.bodyRect()
	nav := &sectrustDoc.tocNav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	rowH := fonts.titleH + 28
	y := int32(0)
	for i, h := range sectrustDoc.headings {
		id := "toc:" + h
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		screen := rect
		screen.Y += top
		UIListRow(renderer, screen, glassRowRadius, nav.focused(id), false)
		num := sectrustItoa(int32(i + 1))
		renderText(renderer, config, fonts.small, num, ColorTextTertiary(), screen.X+18, screen.Y+(rowH-fonts.smallH)/2)
		renderText(renderer, config, fonts.title, ellipsize(fonts.title, h, screen.W-80), ColorTextPrimary(), screen.X+56, screen.Y+(rowH-fonts.titleH)/2)
		nav.add(id, rect, i, false)
		y += rowH + 6
	}
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
}

// ── Input ────────────────────────────────────────────────────────────────

func sectrustDocAction(config *Config, act tbxAction) bool {
	if sectrustDoc.toc {
		nav := &sectrustDoc.tocNav
		switch act {
		case tbxActConfirm:
			if h := strings.TrimPrefix(nav.focus, "toc:"); h != "" {
				PlayActivateSound()
				sectrustDoc.toc = false
				sectrustDoc.pending = h
			}
			return true
		case tbxActBack, tbxActY:
			sectrustDoc.toc = false
			PlayBackSound()
			return true
		case tbxActX:
			return true
		}
		return tbxNavAction(nav, act)
	}
	nav := &sectrustDoc.nav
	step := int32(66)
	switch act {
	case tbxActUp:
		if nav.scroll <= 0 {
			return false
		}
		nav.scrollBy(-step)
	case tbxActDown:
		nav.scrollBy(step)
	case tbxActPageUp, tbxActLeft:
		nav.scrollBy(-(nav.viewH - 48))
	case tbxActPageDown, tbxActRight:
		nav.scrollBy(nav.viewH - 48)
	case tbxActHome:
		nav.scroll = 0
	case tbxActEnd:
		nav.scrollBy(1 << 20)
	case tbxActY:
		if len(sectrustDoc.headings) > 0 {
			PlayActivateSound()
			sectrustDoc.toc = true
			sectrustDoc.tocNav.scroll = 0
		}
	case tbxActX:
		sectrustOpenOnline(config)
	case tbxActBack:
		sectrustDoc.open = false
		PlayBackSound()
	}
	return true
}

func sectrustDocClick(mx, my int32, config *Config) bool {
	if !sectrustDoc.toc {
		return true
	}
	it, ok := sectrustDoc.tocNav.hit(mx, my)
	if !ok {
		return true
	}
	PlayActivateSound()
	sectrustDoc.toc = false
	sectrustDoc.pending = strings.TrimPrefix(it.id, "toc:")
	return true
}

func sectrustDocHints() []FooterHint {
	if sectrustDoc.toc {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Go to"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{
		{Button: "Up/Down", Label: "Scroll"},
		{Button: "L1/R1", Label: "Page"},
		{Button: "Y", Label: "Contents"},
		{Button: "X", Label: "Online"},
		{Button: "B", Label: "Back"},
	}
}

// sectrustOpenOnline opens the published document: in the browser on a
// desktop, as a QR code on the handheld.
func sectrustOpenOnline(config *Config) {
	if !P().IsHandheld() {
		if err := openExternalURL(sectrustDocURL); err == nil {
			ShowToast("Opened SECURITY.md in the browser", ToastKindInfo)
			return
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "SECURITY.md online",
		subtitle: "The latest version, on GitHub",
		icon:     "globe",
		body:     "Scan the code with a phone to read the latest version of the document:\n\n" + sectrustDocURL,
		qr:       tbxQRModules(sectrustDocURL),
		qrLabel:  "Tap it or press Y to enlarge",
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}
