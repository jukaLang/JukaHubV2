package main

// fileeditor_syntax.go — keyword colours for the text editor (fileeditor.go).
//
// The editor opens scripts, sources and configs: a launch.sh, a Python helper,
// a RetroArch .cfg, a packages.json. In one colour a stray quote or a comment
// that never closes is hard to spot on a handheld screen, and so is where a
// function starts. This file is the lexer behind the colours. It splits one
// row into spans (keyword, string, comment, number …) and hands the next row
// the state it ends in, so a block comment, a Python docstring, a shell
// here-doc or Perl POD that runs over many lines is coloured as one piece.
//
// It is a highlighter, not a parser. Each language gets the rules that decide
// where strings and comments start and stop — they are what keeps the colours
// right further down the file — plus its word lists. The ambiguous corners (a
// regex literal or a division, a Perl hash or the modulus, a shell word as a
// command or as an argument) are decided by the token before them, the way
// editors' grammars do. A wrong guess miscolours the rest of one line, never
// the rest of the file, because none of those guesses is carried to the next
// line.
//
// Nothing here touches SDL or the disk: fileeditor_syntax_draw.go owns the
// colours and the per-line state cache. Rows arrive as the editor draws them,
// tabs already turned into spaces. The large-file viewer cuts a line longer
// than 2000 bytes into several rows, which is what synScan's lineEnds is for.

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// synLangID names a highlighting language.
type synLangID uint8

// The languages. synNone is plain text in one colour. synConfig is the
// key = value family — INI, RetroArch's .cfg and .opt, TOML, .properties,
// .env — and YAML, whose comments, keys and values read the same way.
const (
	synNone synLangID = iota
	synBash
	synCpp
	synJava
	synJS
	synJSON
	synPerl
	synPython
	synConfig
	synLangCount
)

// synLangNames are the languages' names in the editor's header and menu.
var synLangNames = [synLangCount]string{"Plain text", "Shell", "C/C++", "Java", "JavaScript", "JSON", "Perl", "Python", "Config"}

// synLangName is a language's name.
func synLangName(id synLangID) string {
	if id >= synLangCount {
		id = synNone
	}
	return synLangNames[id]
}

// synLangExts maps a lower-case extension onto its language. To
// filepath.Ext a dotfile's whole name is its extension (".bashrc"), so the
// shell's start-up files are listed here too. ".ts" is TypeScript here; Files
// still plays a .ts video, because the video player claims the type first.
var synLangExts = map[string]synLangID{
	".sh": synBash, ".bash": synBash, ".zsh": synBash, ".ksh": synBash, ".ash": synBash,
	".command": synBash, ".bashrc": synBash, ".bash_profile": synBash, ".bash_login": synBash,
	".bash_logout": synBash, ".profile": synBash, ".zshrc": synBash, ".zprofile": synBash,
	".kshrc": synBash, ".mkshrc": synBash,
	".c": synCpp, ".h": synCpp, ".cc": synCpp, ".cpp": synCpp, ".cxx": synCpp, ".c++": synCpp,
	".hh": synCpp, ".hpp": synCpp, ".hxx": synCpp, ".h++": synCpp, ".inl": synCpp, ".ipp": synCpp,
	".tpp": synCpp, ".ino": synCpp, ".java": synJava,
	".js": synJS, ".mjs": synJS, ".cjs": synJS, ".jsx": synJS, ".ts": synJS, ".tsx": synJS,
	".mts": synJS, ".cts": synJS,
	".json": synJSON, ".jsonc": synJSON, ".json5": synJSON, ".geojson": synJSON,
	".webmanifest": synJSON, ".ndjson": synJSON, ".jsonl": synJSON,
	".pl": synPerl, ".pm": synPerl, ".pod": synPerl, ".t": synPerl, ".psgi": synPerl,
	".py": synPython, ".pyw": synPython, ".pyi": synPython,
	".ini": synConfig, ".cfg": synConfig, ".conf": synConfig, ".toml": synConfig,
	".properties": synConfig, ".env": synConfig, ".yaml": synConfig, ".yml": synConfig,
	".desktop": synConfig, ".service": synConfig, ".opt": synConfig, ".rmp": synConfig,
	".glslp": synConfig, ".slangp": synConfig, ".cht": synConfig, ".gitconfig": synConfig,
	".editorconfig": synConfig, ".npmrc": synConfig,
}

// synLangByName is the language a file name implies, synNone when it implies
// none. It reads no disk.
func synLangByName(name string) synLangID {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if id, ok := synLangExts[filepath.Ext(base)]; ok {
		return id
	}
	switch base {
	case "profile", "bashrc", "zshrc":
		return synBash
	}
	return synNone
}

// synLangByShebang is the language a script's first line names
// ("#!/usr/bin/env python3", "#!/bin/busybox sh"), synNone without one.
// env's options and VAR=value settings are skipped, and a version on the
// interpreter (python3.11, perl5.36) does not matter.
func synLangByShebang(firstLine string) synLangID {
	if !strings.HasPrefix(firstLine, "#!") {
		return synNone
	}
	for n, f := range strings.Fields(firstLine[2:]) {
		if k := strings.LastIndexByte(f, '/'); k >= 0 {
			f = f[k+1:]
		}
		if n > 0 && (strings.HasPrefix(f, "-") || strings.Contains(f, "=")) {
			continue
		}
		if f == "env" || f == "busybox" {
			continue
		}
		switch strings.TrimRight(f, "0123456789.") {
		case "sh", "bash", "zsh", "ksh", "mksh", "dash", "ash", "yash":
			return synBash
		case "python", "pypy":
			return synPython
		case "perl":
			return synPerl
		case "node", "nodejs", "deno", "bun", "ts-node", "qjs":
			return synJS
		}
		return synNone
	}
	return synNone
}

// synKind is the colour class of a span; the renderer maps each one onto a
// colour of the theme (fileeditor_syntax_draw.go).
type synKind uint8

const (
	// synPlain is the body colour. It is never emitted as a span.
	synPlain synKind = iota
	// synKeyword: control flow and declarations — if, def, my, function.
	synKeyword
	// synType: types and builtins — int, std, String, print, echo, push.
	synType
	// synString: strings, here-doc bodies, regex literals, #include <file>.
	synString
	// synNumber: numeric literals.
	synNumber
	// synComment: comments, Perl POD and the data after __END__.
	synComment
	// synFunc: a function where it is defined or called.
	synFunc
	// synVar: shell and Perl variables, Python's self and cls.
	synVar
	// synMeta: preprocessor lines, decorators, annotations, [section] lines.
	synMeta
	// synConst: true, false, null, None, nullptr, this.
	synConst
	// synKey: JSON object keys, config keys, Perl's `word =>`.
	synKey
	// synKindCount sizes the renderer's palette.
	synKindCount
)

// synSpan is one coloured run of a row: bytes [start, end) of its text.
type synSpan struct {
	start, end int
	kind       synKind
}

// synMode is what a row starts inside: code; a /* block comment */; a line
// comment the large-file viewer cut in two; a string (synState.quote closes
// it); a here-doc body (synState.quote is its terminator line); Perl POD; or
// the data after Perl's __END__, which lasts to the end of the file.
type synMode uint8

const (
	synModeCode synMode = iota
	synModeBlock
	synModeLineComment
	synModeString
	synModeHeredoc
	synModePod
	synModeData
)

// synState is what one row hands the next. The zero value is the top of a
// file. It holds scalars and strings only, so the renderer's cache can keep
// and compare states cheaply, and a string in it never points into the row it
// came from (that would keep a whole window of a large file alive).
type synState struct {
	mode synMode
	// quote closes the string the row is inside, or is the here-doc's
	// terminator line.
	quote string
	// open is the bracket a Perl q{…} construct opened with (0 for other
	// delimiters), depth how deep that bracket nests inside it, and parts how
	// many delimited parts still follow (s{…}{…} and tr have two).
	open  byte
	depth uint8
	parts uint8
	// raw: a backslash escapes nothing (shell '…', C++ raw strings).
	raw bool
	// interp: $variables inside are coloured (shell and Perl "…").
	interp bool
	// mods: modifier letters follow the last delimiter (Perl s///g, m//i).
	mods bool
	// oneLine: the string ends with its line (C, JavaScript, Python '…').
	oneLine bool
	// indent: the here-doc's terminator may be indented (<<- and <<~).
	indent bool
	// mid: the row before was cut out of this same line.
	mid bool
	// next is the terminator of a here-doc announced on this line; its body
	// starts on the next line.
	next    string
	nextInd bool
}

// synScanner is one synScan call's working state.
type synScanner struct {
	lang  synLangID
	text  string
	out   *[]synSpan
	base  int
	st    synState
	words map[string]synKind
	// lineStart: the row starts a line (it does not continue a cut one).
	lineStart bool
	// prevValue: the last token was a value — a name, a number, a string, a
	// closing bracket — so a '/' after it divides and a Perl '%' after it is
	// the modulus, where after an operator they start a regex and a hash.
	prevValue bool
	// prevWord is the last word scanned, "" once anything else followed it.
	prevWord string
	// cmdPos: the shell's next word is in command position.
	cmdPos bool
	// arith counts the shell's open (( … )): a << inside is a shift.
	arith int
}

// synScan colours one row. It starts in state st and returns the state the
// next row starts in. The spans of everything that is not plain text are
// appended to *out in order — ascending, never overlapping, on rune
// boundaries — and with out nil only the state is worked out (the pass that
// finds where each line of a file starts). lineEnds is false for a row the
// large-file viewer cut out of a longer line: a line comment or a one-line
// string then carries on into the next row, which is the same line. It never
// panics and its time is linear in the row.
func synScan(lang synLangID, st synState, text string, lineEnds bool, out *[]synSpan) synState {
	if lang == synNone || lang >= synLangCount {
		return st
	}
	s := synScanner{lang: lang, text: text, out: out, st: st, words: synWordsFor(lang)}
	if out != nil {
		s.base = len(*out)
	}
	s.lineStart = !st.mid
	s.st.mid = false
	s.cmdPos = s.lineStart
	s.run()
	return s.finish(lineEnds)
}

// emit records a span, keeping the order and the rune boundaries the
// renderer relies on whatever a rule above computed.
func (s *synScanner) emit(start, end int, kind synKind) {
	if s.out == nil || kind == synPlain {
		return
	}
	text := s.text
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	for start < end && !utf8.RuneStart(text[start]) {
		start++
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	out := *s.out
	if n := len(out); n > s.base && out[n-1].end > start {
		start = out[n-1].end
	}
	if start >= end {
		return
	}
	*s.out = append(out, synSpan{start: start, end: end, kind: kind})
}

// run colours the row: first whatever the row before left open, then what
// only counts at the start of a line, then token by token.
func (s *synScanner) run() {
	text := s.text
	i := s.resume()
	if i >= len(text) {
		return
	}
	if s.lang == synConfig {
		s.configRow(i, s.lineStart && i == 0)
		return
	}
	if s.lineStart && i == 0 {
		i = s.lineRules()
	}
	for i < len(text) && s.st.mode == synModeCode {
		j := s.step(i)
		if j <= i {
			j = i + 1
		}
		i = j
	}
}

// resume carries on with what the row starts inside and returns where code
// starts (len(text) when the whole row is still inside it).
func (s *synScanner) resume() int {
	text := s.text
	switch s.st.mode {
	case synModeData, synModeLineComment:
		s.emit(0, len(text), synComment)
		return len(text)
	case synModePod:
		s.emit(0, len(text), synComment)
		if s.lineStart && strings.HasPrefix(text, "=cut") {
			s.st.mode = synModeCode
		}
		return len(text)
	case synModeHeredoc:
		line := text
		if s.st.indent {
			line = strings.TrimLeft(line, " \t")
		}
		if s.lineStart && line == s.st.quote {
			s.emit(0, len(text), synKeyword)
			s.st = synState{}
			return len(text)
		}
		s.emit(0, len(text), synString)
		return len(text)
	case synModeBlock:
		if k := strings.Index(text, "*/"); k >= 0 {
			s.emit(0, k+2, synComment)
			s.st.mode = synModeCode
			return k + 2
		}
		s.emit(0, len(text), synComment)
		return len(text)
	case synModeString:
		return s.stringBody(0, 0)
	}
	return 0
}

// finish applies the end of the row. A cut row keeps everything for the rest
// of its line; the end of a line closes line comments and one-line strings and
// opens a here-doc announced on it.
func (s *synScanner) finish(lineEnds bool) synState {
	st := s.st
	if !lineEnds {
		st.mid = true
		return st
	}
	if st.mode == synModeLineComment || (st.mode == synModeString && st.oneLine) {
		st = synState{next: st.next, nextInd: st.nextInd}
	}
	if st.next != "" {
		if st.mode == synModeCode {
			return synState{mode: synModeHeredoc, quote: st.next, indent: st.nextInd}
		}
		st.next, st.nextInd = "", false
	}
	return st
}

// lineRules colours what only counts at the start of a line and returns
// where the row's tokens begin.
func (s *synScanner) lineRules() int {
	text := s.text
	// A shebang is a comment; to JavaScript it would read as a regex.
	if strings.HasPrefix(text, "#!") && s.lang != synCpp {
		return s.lineComment(0)
	}
	if s.lang == synPerl {
		if text[0] == '=' && len(text) > 1 && synIsLetter(text[1]) {
			// POD runs from =pod, =head1 … to and including =cut.
			s.emit(0, len(text), synComment)
			if !strings.HasPrefix(text, "=cut") {
				s.st.mode = synModePod
			}
			return len(text)
		}
		if t := strings.TrimSpace(text); t == "__END__" || t == "__DATA__" {
			s.emit(0, len(text), synKeyword)
			s.st.mode = synModeData
			return len(text)
		}
	}
	i := 0
	for i < len(text) && synIsSpace(text[i]) {
		i++
	}
	if i < len(text) {
		switch {
		case s.lang == synCpp && text[i] == '#':
			return s.preprocessor(i)
		case s.lang == synPython && text[i] == '@':
			end := i + 1
			for end < len(text) && (synIsWordByte(text[end]) || text[end] == '.') {
				end++
			}
			if end > i+1 {
				s.emit(i, end, synMeta)
				return end
			}
		}
	}
	return i
}

// preprocessor colours a C/C++ directive (#include, #define …) and an
// #include's <file>, and returns where the rest of the line's tokens start.
func (s *synScanner) preprocessor(i int) int {
	text := s.text
	j := i + 1
	for j < len(text) && synIsSpace(text[j]) {
		j++
	}
	k := j
	for k < len(text) && synIsWordByte(text[k]) {
		k++
	}
	s.emit(i, k, synMeta)
	switch text[j:k] {
	case "include", "include_next", "import":
		m := k
		for m < len(text) && synIsSpace(text[m]) {
			m++
		}
		if m < len(text) && text[m] == '<' {
			if e := strings.IndexByte(text[m:], '>'); e > 0 {
				s.emit(m, m+e+1, synString)
				return m + e + 1
			}
		}
	}
	return k
}

// step scans one token of code and returns where the next one starts.
func (s *synScanner) step(i int) int {
	switch s.lang {
	case synBash:
		return s.stepShell(i)
	case synPerl:
		return s.stepPerl(i)
	case synPython:
		return s.stepPython(i)
	case synJSON:
		return s.stepJSON(i)
	}
	return s.stepC(i)
}

// punct notes an operator or a bracket for the regex and hash guesses.
func (s *synScanner) punct(c byte) {
	s.prevValue = c == ')' || c == ']' || (c == '}' && s.lang == synPerl)
	s.prevWord = ""
}

// lineComment colours the rest of the row as a comment. The mode carries it
// into the next row when the viewer cut the line; the line's end closes it.
func (s *synScanner) lineComment(i int) int {
	s.emit(i, len(s.text), synComment)
	s.st.mode = synModeLineComment
	return len(s.text)
}

// blockComment colours a /* … */ comment that starts at i.
func (s *synScanner) blockComment(i int) int {
	if k := strings.Index(s.text[i+2:], "*/"); k >= 0 {
		end := i + 2 + k + 2
		s.emit(i, end, synComment)
		return end
	}
	s.emit(i, len(s.text), synComment)
	s.st.mode = synModeBlock
	return len(s.text)
}

// openStr enters the string st describes (its quote and flags), which
// starts at start with its body at from, and scans as far into it as the row
// goes.
func (s *synScanner) openStr(start, from int, st synState) int {
	st.mode = synModeString
	st.next, st.nextInd = s.st.next, s.st.nextInd
	s.st = st
	s.prevWord = ""
	return s.stringBody(from, start)
}

// stringBody scans the inside of the string the state is in, from i; seg is
// where its part not yet coloured starts. It returns where code resumes, or
// len(text) when the string runs on past the row.
func (s *synScanner) stringBody(i, seg int) int {
	text := s.text
	st := &s.st
	if st.quote == "" {
		// Not a state this file makes: end it rather than loop.
		s.st = synState{next: st.next, nextInd: st.nextInd}
		return i
	}
	for i < len(text) {
		c := text[i]
		if c == '\\' && !st.raw {
			i += 2
			continue
		}
		if st.interp && (c == '$' || (c == '@' && s.lang == synPerl)) {
			if end := s.varEnd(i, true); end > i+1 {
				s.emit(seg, i, synString)
				s.emit(i, end, synVar)
				i, seg = end, end
				continue
			}
		}
		if st.open != 0 {
			switch c {
			case st.open:
				if st.depth < 255 {
					st.depth++
				}
			case st.quote[0]:
				if st.depth == 0 {
					return s.closeQuoted(i+1, seg)
				}
				st.depth--
			}
			i++
			continue
		}
		if strings.HasPrefix(text[i:], st.quote) {
			end := i + len(st.quote)
			if st.parts > 0 {
				// s/a/b/: the middle delimiter opens the second part.
				st.parts--
				i = end
				continue
			}
			return s.closeQuoted(end, seg)
		}
		i++
	}
	s.emit(seg, len(text), synString)
	return len(text)
}

// closeQuoted ends the string at end, just past its closing delimiter. A
// Perl s{…}{…} goes on with its second part, regex modifiers join the span,
// and a JSON string followed by ':' is a key.
func (s *synScanner) closeQuoted(end, seg int) int {
	text := s.text
	st := &s.st
	if st.parts > 0 && st.open != 0 {
		j := end
		for j < len(text) && synIsSpace(text[j]) {
			j++
		}
		if j < len(text) {
			if open, quote, ok := synPerlDelimiter(text[j]); ok {
				st.parts--
				st.open, st.quote, st.depth = open, quote, 0
				return s.stringBody(j+1, seg)
			}
		}
	}
	if st.mods {
		for end < len(text) && synIsLetter(text[end]) {
			end++
		}
	}
	kind := synString
	if s.lang == synJSON && synNextIs(text, end, ':') {
		kind = synKey
	}
	s.emit(seg, end, kind)
	s.st = synState{next: st.next, nextInd: st.nextInd}
	s.prevValue, s.prevWord = true, ""
	return end
}

// --- C, C++, Java, JavaScript ---------------------------------------------

// stepC scans one token of C, C++, Java or JavaScript.
func (s *synScanner) stepC(i int) int {
	text := s.text
	c := text[i]
	next := byte(0)
	if i+1 < len(text) {
		next = text[i+1]
	}
	switch {
	case c == ' ' || c == '\t':
		return i + 1
	case c == '/' && next == '/':
		return s.lineComment(i)
	case c == '/' && next == '*':
		return s.blockComment(i)
	case c == '"' && s.lang == synJava && strings.HasPrefix(text[i:], `"""`):
		return s.openStr(i, i+3, synState{quote: `"""`})
	case c == '"':
		return s.openStr(i, i+1, synState{quote: `"`, oneLine: true})
	case c == '\'':
		return s.openStr(i, i+1, synState{quote: "'", oneLine: true})
	case c == '`' && s.lang == synJS:
		return s.openStr(i, i+1, synState{quote: "`"})
	case c == '/' && s.lang == synJS && !s.prevValue:
		if end := s.regexEnd(i); end > i {
			s.emit(i, end, synString)
			s.prevValue, s.prevWord = true, ""
			return end
		}
	case c == '@' && s.lang != synCpp && (synIsLetter(next) || next == '_'):
		// A Java annotation or a TypeScript decorator.
		end := s.wordEnd(i + 1)
		s.emit(i, end, synMeta)
		s.prevValue, s.prevWord = false, ""
		return end
	case synIsDigit(c) || (c == '.' && synIsDigit(next)):
		end := s.numberEnd(i)
		s.emit(i, end, synNumber)
		s.prevValue, s.prevWord = true, ""
		return end
	case synIsWordByte(c) || (c == '$' && s.lang == synJS):
		return s.cWord(i)
	}
	s.punct(c)
	return i + 1
}

// cWord colours a word of C, C++, Java or JavaScript.
func (s *synScanner) cWord(i int) int {
	text := s.text
	end := s.wordEnd(i)
	if end <= i {
		end = i + 1
	}
	if s.lang == synCpp && end < len(text) && text[end] == '"' {
		switch text[i:end] {
		case "R", "u8R", "uR", "UR", "LR":
			if j := s.rawString(i, end); j > end {
				return j
			}
		}
	}
	kind := s.wordKind(i, end)
	s.emit(i, end, kind)
	// After a keyword (return, typeof, in …) an expression starts, so a '/'
	// opens a regex there; after a name it divides.
	s.prevValue = kind != synKeyword
	s.prevWord = text[i:end]
	return end
}

// wordKind colours a word of a C-like language, JavaScript, Python or Perl:
// its entry in the word list, else the name a definition introduces, else a
// call. A word after a '.' is a member (obj.delete, self.print), never a
// keyword.
func (s *synScanner) wordKind(i, end int) synKind {
	text := s.text
	member := i > 0 && text[i-1] == '.' && (i < 2 || text[i-2] != '.')
	if !member {
		if k, ok := s.words[text[i:end]]; ok {
			return k
		}
		switch s.prevWord {
		case "def", "function", "sub":
			return synFunc
		case "class", "struct", "union", "enum", "interface", "namespace", "record", "extends", "implements", "new":
			return synType
		}
	}
	if end < len(text) && text[end] == '(' {
		return synFunc
	}
	return synPlain
}

// rawString colours a C++ raw string R"delim(…)delim" whose prefix starts at
// start and whose quote is at q. It returns start when the bytes after the
// quote are not a raw string's opening.
func (s *synScanner) rawString(start, q int) int {
	text := s.text
	p := q + 1
	for p < len(text) && p-q <= 17 && text[p] != '(' {
		switch text[p] {
		case ' ', ')', '\\', '"':
			return start
		}
		p++
	}
	if p >= len(text) || text[p] != '(' {
		return start
	}
	delim := text[q+1 : p]
	for from := p + 1; ; {
		k := strings.IndexByte(text[from:], ')')
		if k < 0 {
			break
		}
		k += from
		stop := k + 1 + len(delim)
		if stop < len(text) && text[k+1:stop] == delim && text[stop] == '"' {
			s.emit(start, stop+1, synString)
			s.prevValue, s.prevWord = true, ""
			return stop + 1
		}
		from = k + 1
	}
	// It runs on past the row.
	s.emit(start, len(text), synString)
	s.st = synState{mode: synModeString, quote: ")" + delim + `"`, raw: true, next: s.st.next, nextInd: s.st.nextInd}
	return len(text)
}

// regexEnd is the end of the regex literal whose '/' is at i, flags
// included, or i when the row holds no closing '/' (it was a division after
// all). A regex never runs on to the next line.
func (s *synScanner) regexEnd(i int) int {
	text := s.text
	limit := len(text)
	if limit > i+1024 {
		limit = i + 1024
	}
	inClass := false
	for j := i + 1; j < limit; j++ {
		switch text[j] {
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			if j == i+1 {
				return i
			}
			j++
			for j < len(text) && synIsLetter(text[j]) {
				j++
			}
			return j
		}
	}
	return i
}

// --- Python ---------------------------------------------------------------

// stepPython scans one token of Python.
func (s *synScanner) stepPython(i int) int {
	text := s.text
	c := text[i]
	switch {
	case c == ' ' || c == '\t':
		return i + 1
	case c == '#':
		return s.lineComment(i)
	case c == '"' || c == '\'':
		return s.pyString(i, i)
	case synIsDigit(c) || (c == '.' && i+1 < len(text) && synIsDigit(text[i+1])):
		end := s.numberEnd(i)
		s.emit(i, end, synNumber)
		s.prevWord = ""
		return end
	case synIsWordByte(c):
		end := s.wordEnd(i)
		if end < len(text) && (text[end] == '"' || text[end] == '\'') && synPyPrefix(text[i:end]) {
			return s.pyString(i, end)
		}
		w := text[i:end]
		kind := s.wordKind(i, end)
		if kind == synPlain && (w == "match" || w == "case") && s.startsLine(i) && synEndsWithColon(text) {
			// Soft keywords: only a statement's first word.
			kind = synKeyword
		}
		s.emit(i, end, kind)
		s.prevWord = w
		return end
	}
	s.punct(c)
	return i + 1
}

// pyString opens a Python string whose prefix starts at start and whose
// quote is at q: ''' and """ run over lines, ' and " end with theirs.
func (s *synScanner) pyString(start, q int) int {
	text := s.text
	switch {
	case strings.HasPrefix(text[q:], `"""`):
		return s.openStr(start, q+3, synState{quote: `"""`})
	case strings.HasPrefix(text[q:], `'''`):
		return s.openStr(start, q+3, synState{quote: `'''`})
	case text[q] == '"':
		return s.openStr(start, q+1, synState{quote: `"`, oneLine: true})
	}
	return s.openStr(start, q+1, synState{quote: "'", oneLine: true})
}

// startsLine reports whether only blanks precede i on a line's first row.
func (s *synScanner) startsLine(i int) bool {
	if !s.lineStart {
		return false
	}
	for k := 0; k < i && k < len(s.text); k++ {
		if !synIsSpace(s.text[k]) {
			return false
		}
	}
	return true
}

// synPyPrefix reports a Python string prefix: r, b, u, f, t and their pairs
// (rb, Rb, fr …).
func synPyPrefix(p string) bool {
	if p == "" || len(p) > 2 {
		return false
	}
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case 'r', 'R', 'b', 'B', 'u', 'U', 'f', 'F', 't', 'T':
		default:
			return false
		}
	}
	return true
}

// synEndsWithColon reports a line ending in ':' (a compound statement).
func synEndsWithColon(text string) bool {
	return strings.HasSuffix(strings.TrimRight(text, " \t"), ":")
}

// --- JSON -----------------------------------------------------------------

// stepJSON scans one token of JSON (comments and single quotes allowed, as
// in jsonc and JSON5).
func (s *synScanner) stepJSON(i int) int {
	text := s.text
	c := text[i]
	next := byte(0)
	if i+1 < len(text) {
		next = text[i+1]
	}
	switch {
	case c == '/' && next == '/':
		return s.lineComment(i)
	case c == '/' && next == '*':
		return s.blockComment(i)
	case c == '"':
		return s.openStr(i, i+1, synState{quote: `"`, oneLine: true})
	case c == '\'':
		return s.openStr(i, i+1, synState{quote: "'", oneLine: true})
	case synIsDigit(c) || ((c == '-' || c == '+' || c == '.') && synIsDigit(next)):
		end := s.numberEnd(i + 1)
		s.emit(i, end, synNumber)
		return end
	case synIsWordByte(c):
		end := s.wordEnd(i)
		kind, ok := s.words[text[i:end]]
		if !ok {
			kind = synPlain
			if synNextIs(text, end, ':') {
				kind = synKey // a JSON5 key without quotes
			}
		}
		s.emit(i, end, kind)
		return end
	}
	return i + 1
}

// --- Shell ----------------------------------------------------------------

// stepShell scans one token of a shell script.
func (s *synScanner) stepShell(i int) int {
	text := s.text
	c := text[i]
	switch c {
	case ' ', '\t':
		return i + 1
	case '\\':
		s.cmdPos, s.prevWord = false, ""
		return i + 2
	case '#':
		if i == 0 || synShellBreak(text[i-1]) {
			return s.lineComment(i)
		}
	case '\'':
		s.cmdPos = false
		return s.openStr(i, i+1, synState{quote: "'", raw: true})
	case '"':
		s.cmdPos = false
		return s.openStr(i, i+1, synState{quote: `"`, interp: true})
	case '`':
		s.cmdPos = false
		return s.openStr(i, i+1, synState{quote: "`"})
	case '$':
		if i+1 < len(text) {
			switch text[i+1] {
			case '\'':
				s.cmdPos = false
				return s.openStr(i, i+2, synState{quote: "'"})
			case '"':
				s.cmdPos = false
				return s.openStr(i, i+2, synState{quote: `"`, interp: true})
			case '(':
				// $( … ) runs a command; $(( … )) is arithmetic.
				if i+2 < len(text) && text[i+2] == '(' {
					s.arith++
					s.cmdPos, s.prevWord = false, ""
					return i + 3
				}
				s.cmdPos, s.prevWord = true, ""
				return i + 2
			}
		}
		if end := s.varEnd(i, false); end > i+1 {
			s.emit(i, end, synVar)
			s.cmdPos, s.prevWord = false, ""
			return end
		}
	case '<':
		if i+1 < len(text) && text[i+1] == '<' {
			return s.shellHeredoc(i)
		}
	case '(':
		if i+1 < len(text) && text[i+1] == '(' {
			s.arith++
			s.cmdPos, s.prevWord = false, ""
			return i + 2
		}
		s.cmdPos, s.prevWord = true, ""
		return i + 1
	case ')':
		if s.arith > 0 && i+1 < len(text) && text[i+1] == ')' {
			s.arith--
			s.cmdPos, s.prevWord = false, ""
			return i + 2
		}
		// After "pattern)" in a case, a command follows.
		s.cmdPos, s.prevWord = true, ""
		return i + 1
	case ';', '&', '|', '{', '}', '!':
		s.cmdPos, s.prevWord = true, ""
		return i + 1
	}
	switch {
	case synIsDigit(c) && (i == 0 || synShellBreak(text[i-1])):
		end := i
		for end < len(text) && synIsDigit(text[end]) {
			end++
		}
		if end == len(text) || synShellBreak(text[end]) {
			s.emit(i, end, synNumber)
			s.cmdPos, s.prevWord = false, ""
			return end
		}
	case synIsWordByte(c):
		return s.shellWord(i)
	}
	s.cmdPos, s.prevWord = false, ""
	return i + 1
}

// shellWord colours a word of a shell script: an assignment's name, a
// keyword or builtin in command position, a function being defined.
func (s *synScanner) shellWord(i int) int {
	text := s.text
	end := i
	for end < len(text) && synIsWordByte(text[end]) {
		end++
	}
	w := text[i:end]
	before := i == 0 || synShellBreak(text[i-1])
	// NAME=value, NAME+=value, NAME[i]=value: a command may follow them, so
	// the command position stays.
	if before && s.cmdPos && !synIsDigit(text[i]) && end < len(text) &&
		(text[end] == '=' || text[end] == '[' || (text[end] == '+' && end+1 < len(text) && text[end+1] == '=')) {
		s.emit(i, end, synVar)
		s.prevWord = ""
		return end
	}
	kind := synPlain
	if before && (end == len(text) || synShellBreak(text[end])) {
		k, known := s.words[w]
		switch {
		case s.prevWord == "function":
			kind = synFunc
		case known && k == synKeyword && (s.cmdPos || w == "in"):
			kind = synKeyword
		case known && k != synKeyword && s.cmdPos:
			kind = k
		case s.cmdPos && synShellDefinesFunc(text, end):
			kind = synFunc
		}
	}
	s.emit(i, end, kind)
	s.prevWord = ""
	switch {
	case kind == synKeyword && w != "in" && w != "fi" && w != "done" && w != "esac":
		// if, then, do, else, while, function …: a command (or the
		// function's name) follows.
		s.cmdPos = true
		s.prevWord = w
	case kind == synType && synShellPrefixCommand(w):
		s.cmdPos = true
	default:
		s.cmdPos = false
	}
	return end
}

// synShellPrefixCommand reports the builtins whose next word is in command
// position again: an assignment (export X=1) or a command (exec cmd).
func synShellPrefixCommand(w string) bool {
	switch w {
	case "export", "local", "declare", "readonly", "typeset", "command", "builtin", "exec":
		return true
	}
	return false
}

// shellHeredoc reads the << at i: a here-doc's operator and terminator word
// (<<EOF, <<-EOF, << 'EOF', <<"EOF", <<\EOF), whose body starts on the next
// line. <<< is a here-string and a << inside (( … )) a shift. It returns
// where scanning goes on.
func (s *synScanner) shellHeredoc(i int) int {
	text := s.text
	s.cmdPos, s.prevWord = false, ""
	j := i + 2
	if j < len(text) && text[j] == '<' {
		return j + 1
	}
	if s.arith > 0 {
		return j
	}
	indent := false
	if j < len(text) && text[j] == '-' {
		indent = true
		j++
	}
	for j < len(text) && synIsSpace(text[j]) {
		j++
	}
	if j < len(text) && text[j] == '\\' {
		j++
	}
	if j >= len(text) {
		return i + 2
	}
	var word string
	end := j
	switch q := text[j]; {
	case q == '\'' || q == '"':
		limit := len(text)
		if limit > j+257 {
			limit = j + 257
		}
		k := strings.IndexByte(text[j+1:limit], q)
		if k < 0 {
			return i + 2
		}
		word = text[j+1 : j+1+k]
		end = j + 1 + k + 1
	case synIsLetter(q) || q == '_':
		for end < len(text) && text[end] < 0x80 && synIsWordByte(text[end]) {
			end++
		}
		word = text[j:end]
	}
	if word == "" {
		return i + 2
	}
	s.emit(i, end, synKeyword)
	if s.st.next == "" {
		s.st.next, s.st.nextInd = strings.Clone(word), indent
	}
	return end
}

// synShellDefinesFunc reports "()" after a word: name() { … }.
func synShellDefinesFunc(text string, i int) bool {
	for i < len(text) && synIsSpace(text[i]) {
		i++
	}
	return strings.HasPrefix(text[i:], "()")
}

// --- Perl -----------------------------------------------------------------

// stepPerl scans one token of Perl.
func (s *synScanner) stepPerl(i int) int {
	text := s.text
	c := text[i]
	switch {
	case c == ' ' || c == '\t':
		return i + 1
	case c == '#':
		return s.lineComment(i)
	case c == '\'':
		return s.openStr(i, i+1, synState{quote: "'"})
	case c == '"':
		return s.openStr(i, i+1, synState{quote: `"`, interp: true})
	case c == '`':
		return s.openStr(i, i+1, synState{quote: "`", interp: true})
	case c == '$' || c == '@' || (c == '%' && !s.prevValue):
		if end := s.varEnd(i, false); end > i+1 {
			s.emit(i, end, synVar)
			s.prevValue, s.prevWord = true, ""
			return end
		}
	case c == '<' && i+1 < len(text) && text[i+1] == '<':
		if end := s.perlHeredoc(i); end > i {
			return end
		}
	case c == '/' && !s.prevValue:
		if end := s.regexEnd(i); end > i {
			s.emit(i, end, synString)
			s.prevValue, s.prevWord = true, ""
			return end
		}
	case synIsDigit(c):
		end := s.numberEnd(i)
		s.emit(i, end, synNumber)
		s.prevValue, s.prevWord = true, ""
		return end
	case synIsWordByte(c):
		return s.perlWord(i)
	}
	s.punct(c)
	return i + 1
}

// perlWord colours a word of Perl: a `word =>` key, a quote-like operator
// and what it quotes, a keyword, a builtin, a sub's name or a call.
func (s *synScanner) perlWord(i int) int {
	text := s.text
	end := s.wordEnd(i)
	if end <= i {
		end = i + 1
	}
	if synNextIsFatComma(text, end) {
		s.emit(i, end, synKey)
		s.prevValue, s.prevWord = true, ""
		return end
	}
	if j := s.perlQuoteLike(i, end); j > end {
		return j
	}
	kind := s.wordKind(i, end)
	s.emit(i, end, kind)
	// Keywords and builtins (my, return, split, grep …) take an expression,
	// so a '/' or a '%' after them starts a regex or a hash.
	s.prevValue = kind != synKeyword && kind != synType
	s.prevWord = text[i:end]
	return end
}

// perlQuoteLike starts q qq qw qx m qr s tr y when the word at [i, end) is
// one of them used as an operator, returning where scanning goes on — end
// when it is not one: a method (->s), a hash key ({s}), a sub's name, a file
// test (-s $file).
func (s *synScanner) perlQuoteLike(i, end int) int {
	text := s.text
	var parts uint8
	mods, interp := false, true
	switch text[i:end] {
	case "q", "qw":
		interp = false
	case "qq", "qx":
	case "m", "qr":
		mods = true
	case "s":
		parts, mods = 1, true
	case "tr", "y":
		parts, mods, interp = 1, true, false
	default:
		return end
	}
	if i > 0 {
		switch text[i-1] {
		case '>', '-', '$', '@', '%', '&', '*':
			return end
		}
	}
	if s.prevWord == "sub" {
		return end
	}
	j := end
	for j < len(text) && synIsSpace(text[j]) {
		j++
	}
	if j >= len(text) || (text[j] == '#' && j > end) {
		return end
	}
	if text[j] == '}' && i > 0 && text[i-1] == '{' {
		return end
	}
	open, quote, ok := synPerlDelimiter(text[j])
	if !ok {
		return end
	}
	s.st = synState{mode: synModeString, quote: quote, open: open, parts: parts, mods: mods, interp: interp,
		next: s.st.next, nextInd: s.st.nextInd}
	return s.stringBody(j+1, i)
}

// synPerlDelimiter reads the byte a quote-like operator is followed by: a
// bracket nests and closes with its partner, other punctuation closes with
// itself. Bytes that far more often mean something else — '=', a comma, a
// sigil, a closing bracket — are not taken.
func synPerlDelimiter(d byte) (byte, string, bool) {
	switch d {
	case '{':
		return '{', "}", true
	case '(':
		return '(', ")", true
	case '[':
		return '[', "]", true
	case '<':
		return '<', ">", true
	case '}', ')', ']', '>', ',', ';', '=', '-', '.', ':', '?', '$', '@', '%':
		return 0, "", false
	}
	if d < '!' || d > '~' || synIsWordByte(d) {
		return 0, "", false
	}
	return 0, synByteString(d), true
}

// perlHeredoc reads a Perl here-doc's start at i: <<"EOF", <<'EOF', <<EOF
// (the name right after <<, so 1 << 2 stays a shift) and <<~EOF, whose
// terminator may be indented. It returns i when there is none.
func (s *synScanner) perlHeredoc(i int) int {
	text := s.text
	j := i + 2
	indent := false
	if j < len(text) && text[j] == '~' {
		indent = true
		j++
	}
	if j >= len(text) {
		return i
	}
	var word string
	end := j
	switch q := text[j]; {
	case q == '"' || q == '\'' || q == '`':
		limit := len(text)
		if limit > j+257 {
			limit = j + 257
		}
		k := strings.IndexByte(text[j+1:limit], q)
		if k <= 0 {
			return i
		}
		word = text[j+1 : j+1+k]
		end = j + 1 + k + 1
	case synIsLetter(q) || q == '_':
		for end < len(text) && text[end] < 0x80 && synIsWordByte(text[end]) {
			end++
		}
		word = text[j:end]
	default:
		return i
	}
	s.emit(i, end, synKeyword)
	if s.st.next == "" {
		s.st.next, s.st.nextInd = strings.Clone(word), indent
	}
	s.prevValue, s.prevWord = true, ""
	return end
}

// --- Shared pieces --------------------------------------------------------

// varEnd is the end of the shell or Perl variable at text[i] — $name, ${…},
// $1, $@, @list, %hash, $#list — or i when text[i] starts none. inString
// keeps Perl's punctuation variables ($. $, $/) out of strings, where "$."
// is far more often a price than the input line number.
func (s *synScanner) varEnd(i int, inString bool) int {
	text := s.text
	if i+1 >= len(text) {
		return i
	}
	sigil, c, j := text[i], text[i+1], i+1
	switch {
	case c == '{':
		limit := len(text)
		if limit > j+256 {
			limit = j + 256
		}
		if k := strings.IndexByte(text[j:limit], '}'); k > 0 {
			return j + k + 1
		}
		return i
	case synIsLetter(c) || c == '_' || c >= 0x80:
		return s.wordEnd(j)
	case synIsDigit(c):
		if s.lang == synBash {
			return j + 1
		}
		for j < len(text) && synIsDigit(text[j]) {
			j++
		}
		return j
	case s.lang == synBash:
		if sigil == '$' && strings.IndexByte("@*#?$!-", c) >= 0 {
			return j + 1
		}
		return i
	case c == ':' && j+2 < len(text) && text[j+1] == ':' && (synIsLetter(text[j+2]) || text[j+2] == '_'):
		return s.wordEnd(j + 2)
	case c == '$':
		if j+1 < len(text) && (synIsLetter(text[j+1]) || text[j+1] == '_') {
			return s.wordEnd(j + 1)
		}
		if sigil == '$' && !inString {
			return j + 1 // $$, the process ID
		}
		return i
	case sigil != '$':
		return i
	case c == '#':
		if j+1 < len(text) && (synIsLetter(text[j+1]) || text[j+1] == '_') {
			return s.wordEnd(j + 1)
		}
		if j+1 < len(text) && (text[j+1] == '{' || text[j+1] == '$') {
			return j + 2
		}
		return j + 1
	case c == '^' && j+1 < len(text) && synIsLetter(text[j+1]):
		return j + 2
	case !inString && strings.IndexByte("&`'\"+!@/\\,;.<>()[]?|~=%-", c) >= 0:
		return j + 1
	}
	return i
}

// wordEnd is the end of the word starting at i: letters, digits, '_' and any
// non-ASCII byte; '$' in JavaScript; Perl's Package::name.
func (s *synScanner) wordEnd(i int) int {
	text := s.text
	for i < len(text) {
		c := text[i]
		switch {
		case synIsWordByte(c), c == '$' && s.lang == synJS:
			i++
		case c == ':' && s.lang == synPerl && i+2 < len(text) && text[i+1] == ':' && synIsWordByte(text[i+2]):
			i += 2
		default:
			return i
		}
	}
	return i
}

// numberEnd is the end of the number starting at i: digits, a base prefix, a
// fraction, an exponent with its sign, digit separators and suffixes (0x1F,
// 1_000, 1'000, 2.5e-3f, 10n). A ".." after it is a range, not a fraction.
func (s *synScanner) numberEnd(i int) int {
	text := s.text
	hex := i+1 < len(text) && text[i] == '0' && (text[i+1]|0x20) == 'x'
	for i < len(text) {
		c := text[i]
		switch {
		case c < 0x80 && synIsWordByte(c):
			i++
		case c == '.' && (i+1 >= len(text) || text[i+1] != '.'):
			i++
		case c == '\'' && s.lang == synCpp && i+1 < len(text) && text[i+1] < 0x80 && synIsWordByte(text[i+1]):
			i++
		case (c == '+' || c == '-') && i > 0 && ((!hex && (text[i-1]|0x20) == 'e') || (hex && (text[i-1]|0x20) == 'p')):
			i++
		default:
			return i
		}
	}
	return i
}

// --- Config files ---------------------------------------------------------

// configRow colours one line of a config file: a comment line, a [section]
// header, or key = value (key: value in YAML and .properties). atStart is
// false for the rest of a line after a cut or a string that ran into it,
// which can only be part of a value.
func (s *synScanner) configRow(i int, atStart bool) {
	text := s.text
	if !atStart {
		s.configValue(i)
		return
	}
	for i < len(text) && synIsSpace(text[i]) {
		i++
	}
	if i >= len(text) {
		return
	}
	switch text[i] {
	case '#', ';':
		s.lineComment(i)
		return
	case '[':
		end := -1
		if strings.HasPrefix(text[i:], "[[") {
			if k := strings.Index(text[i+2:], "]]"); k >= 0 {
				end = i + k + 4
			}
		} else if k := strings.IndexByte(text[i:], ']'); k > 0 {
			end = i + k + 1
		}
		if end > 0 {
			s.emit(i, end, synMeta)
			s.configValue(end)
			return
		}
	case '-':
		if strings.TrimSpace(text[i:]) == "---" {
			s.emit(i, i+3, synMeta)
			return
		}
		if i+1 < len(text) && text[i+1] == ' ' {
			// A YAML list item: "- name: value" has a key after the dash.
			i += 2
			for i < len(text) && synIsSpace(text[i]) {
				i++
			}
		}
	}
	if strings.HasPrefix(text[i:], "export ") {
		s.emit(i, i+6, synKeyword)
		i += 7
	}
	// The key runs to the first '=' or ':' outside quotes.
	j := i
	for j < len(text) && text[j] != '=' && text[j] != ':' {
		if q := text[j]; q == '"' || q == '\'' {
			k := strings.IndexByte(text[j+1:], q)
			if k < 0 {
				j = len(text)
				break
			}
			j += k + 2
			continue
		}
		if text[j] == '#' && j > i && synIsSpace(text[j-1]) {
			j = len(text)
			break
		}
		j++
	}
	isKey := j < len(text) && j > i
	if isKey && text[j] == ':' && j+1 < len(text) && (text[j+1] == '/' || text[j+1] == '\\') {
		isKey = false // a URL or a Windows path, not "key: value"
	}
	if !isKey {
		s.configValue(i)
		return
	}
	k := j
	for k > i && synIsSpace(text[k-1]) {
		k--
	}
	s.emit(i, k, synKey)
	s.configValue(j + 1)
}

// configValue colours a value from i on: quoted strings, numbers, on/off
// words and a trailing comment (# or ; after a blank).
func (s *synScanner) configValue(i int) {
	text := s.text
	for i < len(text) && s.st.mode == synModeCode {
		c := text[i]
		switch {
		case (c == '#' || c == ';') && (i == 0 || synIsSpace(text[i-1])):
			s.lineComment(i)
			return
		case i > 0 && synIsWordByte(text[i-1]):
			// Inside a word: an apostrophe here is not a quote.
		case c == '"':
			i = s.openStr(i, i+1, synState{quote: `"`, oneLine: true})
			continue
		case c == '\'':
			i = s.openStr(i, i+1, synState{quote: "'", oneLine: true, raw: true})
			continue
		case synIsDigit(c) || ((c == '-' || c == '+') && i+1 < len(text) && synIsDigit(text[i+1])):
			end := s.numberEnd(i + 1)
			s.emit(i, end, synNumber)
			i = end
			continue
		case synIsLetter(c):
			end := i
			for end < len(text) && synIsWordByte(text[end]) {
				end++
			}
			if synConfigConst(text[i:end]) && (end == len(text) || !synConfigWordTail(text[end])) {
				s.emit(i, end, synConst)
			}
			i = end
			continue
		}
		i++
	}
}

// synConfigConst reports a config value word that is a switch or nothing.
func synConfigConst(w string) bool {
	for _, c := range [...]string{"true", "false", "yes", "no", "on", "off", "null", "none", "nil"} {
		if strings.EqualFold(w, c) {
			return true
		}
	}
	return false
}

// synConfigWordTail reports a byte that makes a word longer than it looks
// ("on-screen", "no.png", "off/"), so the word is not a switch.
func synConfigWordTail(c byte) bool {
	return c == '-' || c == '.' || c == '/'
}

// --- Bytes ----------------------------------------------------------------

func synIsSpace(c byte) bool {
	return c == ' ' || c == '\t'
}

func synIsDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func synIsLetter(c byte) bool {
	c |= 0x20
	return c >= 'a' && c <= 'z'
}

// synIsWordByte reports a byte of a name: ASCII letters, digits and '_', and
// every byte of a non-ASCII character (names may be Unicode).
func synIsWordByte(c byte) bool {
	return c == '_' || c >= 0x80 || synIsDigit(c) || synIsLetter(c)
}

// synShellBreak reports a byte that ends a shell word.
func synShellBreak(c byte) bool {
	switch c {
	case ' ', '\t', ';', '&', '|', '(', ')', '<', '>', '`', '{', '}':
		return true
	}
	return false
}

// synNextIs reports whether the first non-blank byte from i on is c.
func synNextIs(text string, i int, c byte) bool {
	for i < len(text) && synIsSpace(text[i]) {
		i++
	}
	return i < len(text) && text[i] == c
}

// synNextIsFatComma reports Perl's => after optional blanks.
func synNextIsFatComma(text string, i int) bool {
	for i < len(text) && synIsSpace(text[i]) {
		i++
	}
	return strings.HasPrefix(text[i:], "=>")
}

// synPunct holds the printable ASCII bytes in order, so a one-byte
// delimiter can be kept in synState as a slice of a constant rather than of
// the row it came from.
const synPunct = "!\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"

// synByteString is the one-byte string of a printable ASCII byte.
func synByteString(c byte) string {
	if c < '!' || c > '~' {
		return ""
	}
	return synPunct[c-'!' : c-'!'+1]
}

// --- Word lists -----------------------------------------------------------

// synWordTable builds a language's word list from space-separated words.
// A word in two lists keeps the later kind: keywords win.
func synWordTable(keywords, types, consts, vars string) map[string]synKind {
	m := make(map[string]synKind, 192)
	for _, w := range strings.Fields(vars) {
		m[w] = synVar
	}
	for _, w := range strings.Fields(types) {
		m[w] = synType
	}
	for _, w := range strings.Fields(consts) {
		m[w] = synConst
	}
	for _, w := range strings.Fields(keywords) {
		m[w] = synKeyword
	}
	return m
}

// synWordsFor is a language's word list (nil for config files, whose words
// are their keys).
func synWordsFor(lang synLangID) map[string]synKind {
	switch lang {
	case synBash:
		return synShellWords
	case synCpp:
		return synCppWords
	case synJava:
		return synJavaWords
	case synJS:
		return synJSWords
	case synJSON:
		return synJSONWords
	case synPerl:
		return synPerlWords
	case synPython:
		return synPythonWords
	}
	return nil
}

var synShellWords = synWordTable(
	`if then else elif fi case esac for select while until do done in function time coproc`,
	`alias bg bind break builtin caller cd command compgen complete continue declare dirs disown
	echo enable eval exec exit export false fc fg getopts hash help history jobs kill let local
	logout mapfile popd printf pushd pwd read readarray readonly return set shift shopt source
	suspend test times trap true type typeset ulimit umask unalias unset wait sleep`,
	``, ``)

var synCppWords = synWordTable(
	`alignas alignof and and_eq asm break case catch class co_await co_return co_yield compl
	concept const consteval constexpr constinit const_cast continue decltype default delete do
	dynamic_cast else enum explicit export extern final for friend goto if inline mutable
	namespace new noexcept not not_eq operator or or_eq override private protected public
	register reinterpret_cast requires return sizeof static static_assert static_cast struct
	switch template thread_local throw try typedef typeid typename union using virtual volatile
	while xor xor_eq`,
	`auto bool char char8_t char16_t char32_t double float int long short signed unsigned void
	wchar_t size_t ssize_t ptrdiff_t intptr_t uintptr_t intmax_t uintmax_t int8_t int16_t
	int32_t int64_t uint8_t uint16_t uint32_t uint64_t off_t time_t FILE va_list std string
	string_view vector unordered_map unordered_set unique_ptr shared_ptr weak_ptr optional
	deque`,
	`true false nullptr NULL this`, ``)

var synJavaWords = synWordTable(
	`abstract assert break case catch class const continue default do else enum extends final
	finally for goto if implements import instanceof interface native new package permits
	private protected public record return sealed static strictfp super switch synchronized
	throw throws transient try var volatile while yield`,
	`boolean byte char double float int long short void String Integer Long Double Float Short
	Byte Character Boolean Number Object Class Void List ArrayList LinkedList Map HashMap
	TreeMap LinkedHashMap Set HashSet TreeSet LinkedHashSet Iterator Iterable Collection
	Collections Arrays Optional Stream Collectors System Math Thread Runnable Exception
	RuntimeException IllegalArgumentException IllegalStateException NullPointerException
	IOException Throwable Error StringBuilder Override Deprecated SuppressWarnings
	FunctionalInterface`,
	`true false null this`, ``)

var synJSWords = synWordTable(
	`abstract as async await break case catch class const continue debugger declare default
	delete do else enum export extends finally for from function get if implements import in
	infer instanceof interface is keyof let namespace new of private protected public readonly
	return satisfies set static super switch throw try type typeof var void while with yield`,
	`Array ArrayBuffer Object String Number Boolean Promise Map Set WeakMap WeakSet JSON Math
	Date RegExp Error TypeError RangeError SyntaxError Symbol BigInt Reflect Proxy Intl
	Uint8Array Int32Array Float32Array Float64Array console window document globalThis
	navigator require module exports process Buffer any unknown never string number boolean
	bigint symbol object`,
	`true false null undefined NaN Infinity this`, ``)

var synJSONWords = synWordTable(``, ``, `true false null NaN Infinity`, ``)

var synPerlWords = synWordTable(
	`my our local state sub package use no require if elsif else unless while until for foreach
	do last next redo goto return and or not xor eq ne lt gt le ge cmp x BEGIN END INIT CHECK
	UNITCHECK DESTROY AUTOLOAD __PACKAGE__ __FILE__ __LINE__ __SUB__ undef defined wantarray`,
	`print printf say open close binmode die warn eval chomp chop chdir chmod chown push pop
	shift unshift splice split join keys values each exists delete scalar ref bless map grep
	sort reverse length substr index rindex sprintf lc uc lcfirst ucfirst abs int sqrt rand
	srand time localtime gmtime sleep system exec exit kill fork wait waitpid opendir readdir
	closedir mkdir rmdir unlink rename stat lstat read sysread syswrite seek tell eof pack
	unpack select caller glob link symlink readlink utime umask fileno flock truncate getc
	readline hex oct ord chr crypt quotemeta pos`,
	``, ``)

var synPythonWords = synWordTable(
	`and as assert async await break class continue def del elif else except finally for from
	global if import in is lambda nonlocal not or pass raise return try while with yield`,
	`abs aiter all anext any ascii bin bool breakpoint bytearray bytes callable chr classmethod
	compile complex delattr dict dir divmod enumerate eval exec filter float format frozenset
	getattr globals hasattr hash help hex id input int isinstance issubclass iter len list
	locals map max memoryview min next object oct open ord pow print property range repr
	reversed round set setattr slice sorted staticmethod str sum super tuple type vars zip
	__import__ BaseException Exception ArithmeticError AssertionError AttributeError EOFError
	FileExistsError FileNotFoundError ImportError IndexError KeyError KeyboardInterrupt
	LookupError MemoryError ModuleNotFoundError NameError NotImplementedError OSError
	OverflowError PermissionError RecursionError RuntimeError StopIteration SyntaxError
	SystemExit TimeoutError TypeError UnicodeDecodeError UnicodeEncodeError ValueError
	ZeroDivisionError IOError Warning DeprecationWarning UserWarning`,
	`True False None NotImplemented Ellipsis __debug__`,
	`self cls`)
