package main

// webdom_module.go — ES modules for the browser's JavaScript engine.
//
// goja has no module support at all: its parser rejects import and export
// declarations, import() and import.meta. Modern sites (anything built with
// Vite, Rollup or esbuild) ship <script type="module">, so without this their
// pages stay empty. Rather than teaching the engine modules, each module is
// rewritten source to source into a classic function expression that the JS
// loader (webdom/85_scripts.js) calls with a small linker object, __jh:
//
//	(function (__jh) { return (async function () { "use strict"; PROLOGUE BODY
//	})(); })
//
// The prologue registers the module's exports as live getters (__jh.exp) and
// its re-exports (__jh.reexp, __jh.star), then binds its imports from the
// namespaces of modules the loader already ran (__jh.imp). It sits on the
// first line, and every removed statement is replaced by a ";" and spaces
// that keep its line breaks, so the line numbers in error messages still
// point at the original source. The body runs inside an async function, which
// gives a module its strict mode, its own scope and top-level await for free.
//
// The rewrite needs a real lexer, not regular expressions: "import" inside a
// string, comment, template or regex literal must stay untouched, and a regex
// such as /}/ must not unbalance the brackets that decide what is top level.
// The lexer here is small, but complete enough for minified bundles.
//
// Known limits (the price of not having a real module system): imported
// bindings are copies taken when the importing module starts, not live, so a
// counter another module increments later is not seen, and in an import cycle
// a binding read before its module ran is undefined; an anonymous default
// function is not hoisted and is named "__jh_default" instead of "default".

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// webJSImport is one module a transformed module depends on. The prologue
// refers to it by its index in the slice webJSTransformModule returns.
type webJSImport struct {
	Spec string // specifier as written (the string's value; the loader resolves it)
	Type string // "" or the import attribute type ("json", "css") from `with {type: "json"}` / `assert {…}`
}

// Limits on what the transform accepts, so that no input, however hostile,
// costs more than linear time or a bounded Go stack: the transform runs in Go,
// where the script watchdog cannot interrupt it, and a stack overflow kills
// the process instead of panicking into webJSSafeTransform's recover.
const (
	// webJSModMaxSource is the largest source. The network layer caps one
	// script far lower (3 MiB); this keeps the token slice bounded if a
	// caller ever passes more, and token offsets within an int32.
	webJSModMaxSource = 16 << 20
	// webJSModMaxNesting bounds the lexer's bracket stack, and with it the
	// recursion of pattern, which descends one level per bracket.
	webJSModMaxNesting = 10000
	// webJSModMaxClassDepth bounds classBody's recursion into classes in a
	// class heritage, which needs no brackets: class extends class extends …
	webJSModMaxClassDepth = 1000
	// webJSModMaxImports bounds the distinct imports of one module; each is a
	// module the loader fetches, so no real page comes near it.
	webJSModMaxImports = 4096
)

// webJSModKind is the kind of a lexer token: identifiers (keywords included,
// since most JavaScript keywords are only contextual), #private names,
// numbers, strings, the chunks of template literals (a whole `…` without
// substitutions; `…${, }…${ and }…` around them), regex literals and
// punctuators.
type webJSModKind uint8

const (
	webJSModIdent webJSModKind = iota + 1
	webJSModPrivate
	webJSModNumber
	webJSModString
	webJSModTemplate
	webJSModTemplateHead
	webJSModTemplateMid
	webJSModTemplateTail
	webJSModRegex
	webJSModPunct
)

// What webJSModParser.dynamic found: import(…) or import.meta.
const (
	webJSModImportCall = 1 + iota
	webJSModImportMeta
)

// webJSModToken is one token: its byte range in the source, the index of its
// partner (the other bracket of ( ) [ ] { }, the last chunk of a template
// literal with substitutions; -1 otherwise) and whether a line break comes
// before it, which automatic semicolon insertion depends on. The int32s keep
// a token at 16 bytes: a minified bundle has a token every 3 bytes or so, and
// webJSModMaxSource keeps every offset and index in range.
type webJSModToken struct {
	start int32
	end   int32
	match int32
	kind  webJSModKind
	nl    bool
}

// webJSModOpen is an open bracket on the lexer's stack. ch is '(', '[', '{'
// or '$' for a template substitution, whose tok is the literal's first chunk.
// cond marks the "(" after if, while, for (and for await) and with: a regex,
// not a division, may follow its ")". forHead marks the "(" after for, inside
// which "of" may be the keyword of a for-of loop.
type webJSModOpen struct {
	tok     int
	ch      byte
	cond    bool
	forHead bool
}

// webJSModLexer splits a script into webJSModTokens.
type webJSModLexer struct {
	src       string
	pos       int
	classic   bool // a classic script: <!-- and --> start line comments (Annex B)
	nl        bool // a line break since the last token
	condClose bool // the last ")" closed the head of if/while/for/with
	toks      []webJSModToken
	stack     []webJSModOpen
}

// webJSModPuncts are the multi-character punctuators, longest first; any
// other byte is a one-character token.
var webJSModPuncts = []string{
	">>>=", "...", "===", "!==", "**=", "<<=", ">>=", ">>>", "&&=", "||=", "??=",
	"=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--", "+=", "-=",
	"*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>", "**",
}

// webJSModLex splits src into tokens; classic selects classic-script rules
// (HTML-like comments). It fails on unterminated literals and comments and on
// unbalanced brackets: a script the lexer cannot follow cannot be rewritten
// safely.
func webJSModLex(src string, classic bool) ([]webJSModToken, error) {
	if len(src) > webJSModMaxSource { // the int32 offsets of webJSModToken
		return nil, fmt.Errorf("module too large (%d bytes)", len(src))
	}
	lx := &webJSModLexer{src: src, pos: webJSModPreamble(src), classic: classic}
	// Minified bundles, the common large input, run 2.6 to 3.4 bytes per
	// token: sized for them, the slice rarely grows (each growth is a copy).
	lx.toks = make([]webJSModToken, 0, len(src)/3+16)
	for {
		if err := lx.space(); err != nil {
			return nil, err
		}
		if lx.pos >= len(src) {
			break
		}
		if err := lx.token(); err != nil {
			return nil, err
		}
	}
	if n := len(lx.stack); n > 0 {
		open := lx.stack[n-1]
		at := len(src)
		if open.tok >= 0 && open.tok < len(lx.toks) {
			at = int(lx.toks[open.tok].start)
		}
		if open.ch == '$' {
			return nil, webJSModErrorf(src, at, "unterminated template literal")
		}
		return nil, webJSModErrorf(src, at, "unclosed %q", open.ch)
	}
	return lx.toks, nil
}

// webJSModPreamble returns the length of a leading byte order mark and #!
// line. Both only mean something at the very start of a file, and goja knows
// neither as a hashbang, so the module rewrite blanks them.
func webJSModPreamble(src string) int {
	n := 0
	if strings.HasPrefix(src, "\ufeff") {
		n = len("\ufeff")
	}
	if strings.HasPrefix(src[n:], "#!") {
		n += 2
		for n < len(src) && src[n] != '\n' && src[n] != '\r' && !webJSModLineSep(src, n) {
			n++
		}
	}
	return n
}

// space skips white space and comments, noting line breaks for the next
// token.
func (lx *webJSModLexer) space() error {
	src := lx.src
	for lx.pos < len(src) {
		c := src[lx.pos]
		switch {
		case c == '\n' || c == '\r':
			lx.nl = true
			lx.pos++
		case c == ' ' || c == '\t' || c == '\v' || c == '\f':
			lx.pos++
		case c == '/' && lx.pos+1 < len(src) && src[lx.pos+1] == '/':
			lx.skipLine()
		case c == '/' && lx.pos+1 < len(src) && src[lx.pos+1] == '*':
			end := strings.Index(src[lx.pos+2:], "*/")
			if end < 0 {
				return lx.errorf(lx.pos, "unterminated comment")
			}
			if strings.ContainsAny(src[lx.pos+2:lx.pos+2+end], "\n\r\u2028\u2029") {
				lx.nl = true
			}
			lx.pos += end + 4
		case lx.classic && c == '<' && strings.HasPrefix(src[lx.pos:], "<!--"),
			lx.classic && c == '-' && (lx.nl || len(lx.toks) == 0) && strings.HasPrefix(src[lx.pos:], "-->"):
			lx.skipLine()
		case c < utf8.RuneSelf:
			return nil
		default:
			r, size := utf8.DecodeRuneInString(src[lx.pos:])
			switch {
			case r == '\u2028' || r == '\u2029':
				lx.nl = true
			case r != '\ufeff' && !unicode.Is(unicode.Zs, r):
				return nil // any other non-ASCII character starts an identifier
			}
			lx.pos += size
		}
	}
	return nil
}

// skipLine moves to the line break that ends a line comment.
func (lx *webJSModLexer) skipLine() {
	src := lx.src
	for lx.pos < len(src) && src[lx.pos] != '\n' && src[lx.pos] != '\r' && !webJSModLineSep(src, lx.pos) {
		lx.pos++
	}
}

// token lexes the token at lx.pos, which is not white space or a comment.
// Every path consumes at least one byte or fails.
func (lx *webJSModLexer) token() error {
	src, start := lx.src, lx.pos
	c := src[start]
	switch {
	case c == '"' || c == '\'':
		return lx.str(start)
	case c == '`':
		return lx.template(start, -1)
	case webJSModDigit(c) || c == '.' && start+1 < len(src) && webJSModDigit(src[start+1]):
		lx.number(start)
		return nil
	case webJSModIdentStart(c):
		return lx.ident(start, start, webJSModIdent)
	case c == '#' && start+1 < len(src) && webJSModIdentStart(src[start+1]):
		return lx.ident(start, start+1, webJSModPrivate)
	case c == '/' && lx.regexAllowed():
		return lx.regex(start)
	}
	return lx.punct(start)
}

// str lexes a '…' or "…" string literal.
func (lx *webJSModLexer) str(start int) error {
	src, q := lx.src, lx.src[start]
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case q:
			lx.emit(webJSModString, start, i+1)
			return nil
		case '\\':
			// The escaped byte is skipped whole; "\" CR LF is one line
			// continuation, not a continuation and then a bare line feed.
			i++
			if i+1 < len(src) && src[i] == '\r' && src[i+1] == '\n' {
				i++
			}
		case '\n', '\r':
			return lx.errorf(start, "unterminated string")
		}
	}
	return lx.errorf(start, "unterminated string")
}

// template lexes one chunk of a template literal, starting at its backquote
// (head < 0) or at the "}" that ends a substitution (head = index of the
// literal's first chunk). A chunk ending in "${" pushes a '$' entry, so the
// "}" closing the substitution resumes the literal instead of closing a block:
// that is what keeps `${ {a: 1}.a }` and nested templates balanced.
func (lx *webJSModLexer) template(start, head int) error {
	src := lx.src
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '`':
			if head < 0 {
				lx.emit(webJSModTemplate, start, i+1)
				return nil
			}
			lx.emit(webJSModTemplateTail, start, i+1)
			tail := len(lx.toks) - 1
			lx.toks[head].match = int32(tail)
			lx.toks[tail].match = int32(head)
			return nil
		case '$':
			if i+1 < len(src) && src[i+1] == '{' {
				if len(lx.stack) >= webJSModMaxNesting {
					return lx.errorf(start, "nesting too deep")
				}
				kind := webJSModTemplateMid
				if head < 0 {
					kind, head = webJSModTemplateHead, len(lx.toks)
				}
				lx.emit(kind, start, i+2)
				lx.stack = append(lx.stack, webJSModOpen{tok: head, ch: '$'})
				return nil
			}
		}
	}
	return lx.errorf(start, "unterminated template literal")
}

// number lexes a numeric literal: decimal with fraction and exponent, 0x/0o/0b,
// numeric separators and the BigInt suffix. Validity is the engine's business.
func (lx *webJSModLexer) number(start int) {
	src := lx.src
	i := start
	digits := func(hex bool) {
		for i < len(src) && (webJSModDigit(src[i]) || src[i] == '_' || hex && webJSModHexVal(src[i]) >= 0) {
			i++
		}
	}
	if src[i] == '0' && i+1 < len(src) && strings.IndexByte("xXoObB", src[i+1]) >= 0 {
		i += 2
		digits(true)
	} else {
		digits(false)
		if i < len(src) && src[i] == '.' {
			i++
			digits(false)
		}
		if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
			j := i + 1
			if j < len(src) && (src[j] == '+' || src[j] == '-') {
				j++
			}
			if j < len(src) && webJSModDigit(src[j]) {
				i = j
				digits(false)
			}
		}
	}
	if i < len(src) && src[i] == 'n' {
		i++
	}
	lx.emit(webJSModNumber, start, max(i, start+1))
}

// ident lexes an identifier (or a #private name, whose "#" is at start and
// whose name begins at from), including \u escapes and non-ASCII letters.
func (lx *webJSModLexer) ident(start, from int, kind webJSModKind) error {
	src := lx.src
	i := from
scan:
	for i < len(src) {
		c := src[i]
		switch {
		case webJSModIdentChar(c):
			i++
		case c == '\\':
			_, n := webJSModUnicodeEscape(src, i)
			if n == 0 {
				return lx.errorf(i, "bad escape in identifier")
			}
			i += n
		case c >= utf8.RuneSelf:
			r, size := utf8.DecodeRuneInString(src[i:])
			if r == '\u2028' || r == '\u2029' || r == '\ufeff' || unicode.Is(unicode.Zs, r) {
				break scan
			}
			i += size
		default:
			break scan
		}
	}
	lx.emit(kind, start, max(i, start+1))
	return nil
}

// regex lexes a regular expression literal: "/" inside a [class] does not end
// it, and neither does an escaped one.
func (lx *webJSModLexer) regex(start int) error {
	src := lx.src
	class := false
	for i := start + 1; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\':
			i++
			if i < len(src) && (src[i] == '\n' || src[i] == '\r' || webJSModLineSep(src, i)) {
				return lx.errorf(start, "unterminated regular expression")
			}
		case c == '\n' || c == '\r' || webJSModLineSep(src, i):
			return lx.errorf(start, "unterminated regular expression")
		case c == '[':
			class = true
		case c == ']':
			class = false
		case c == '/' && !class:
			i++
			for i < len(src) && webJSModIdentChar(src[i]) {
				i++ // flags
			}
			lx.emit(webJSModRegex, start, i)
			return nil
		}
	}
	return lx.errorf(start, "unterminated regular expression")
}

// punct lexes a punctuator and keeps the bracket stack: every closing bracket
// must match the innermost open one, and the "}" of a template substitution
// hands over to template.
func (lx *webJSModLexer) punct(start int) error {
	src := lx.src
	c := src[start]
	switch c {
	case '(', '[', '{':
		if len(lx.stack) >= webJSModMaxNesting {
			return lx.errorf(start, "nesting too deep")
		}
		cond, forHead := false, false
		if c == '(' {
			n := len(lx.toks)
			w := lx.wordAt(n - 1)
			if w == "await" && lx.wordAt(n-2) == "for" {
				w = "for" // for await (x of y)
			}
			forHead = w == "for"
			cond = forHead || w == "if" || w == "while" || w == "with"
		}
		lx.stack = append(lx.stack, webJSModOpen{tok: len(lx.toks), ch: c, cond: cond, forHead: forHead})
		lx.emit(webJSModPunct, start, start+1)
		return nil
	case ')', ']', '}':
		n := len(lx.stack)
		if n == 0 {
			return lx.errorf(start, "unbalanced %q", c)
		}
		open := lx.stack[n-1]
		lx.stack = lx.stack[:n-1]
		if c == '}' && open.ch == '$' {
			return lx.template(start, open.tok)
		}
		if open.ch != webJSModOpener(c) {
			return lx.errorf(start, "unbalanced %q", c)
		}
		lx.condClose = open.cond
		lx.emit(webJSModPunct, start, start+1)
		closer := len(lx.toks) - 1
		lx.toks[open.tok].match = int32(closer)
		lx.toks[closer].match = int32(open.tok)
		return nil
	}
	size := 1
	for _, p := range webJSModPuncts {
		if strings.HasPrefix(src[start:], p) {
			size = len(p)
			break
		}
	}
	// "?." before a digit is a conditional and a number: a?.5:b
	if size == 2 && c == '?' && src[start+1] == '.' && start+2 < len(src) && webJSModDigit(src[start+2]) {
		size = 1
	}
	lx.emit(webJSModPunct, start, start+size)
	return nil
}

// regexAllowed reports whether a "/" here starts a regular expression rather
// than a division: true where an expression may start (at the start, after an
// operator, an opening bracket or a keyword such as return), false after
// something that ends one (a name, a literal, ")" or "]"). The ")" closing
// if (…), while (…), for (…), for await (…) or with (…) ends a statement
// head, so a regex may follow it; a "}" is taken to end a block.
func (lx *webJSModLexer) regexAllowed() bool {
	n := len(lx.toks)
	if n == 0 {
		return true
	}
	switch lx.toks[n-1].kind {
	case webJSModIdent:
		w := lx.prevWord()
		if w == "of" {
			return lx.ofKeyword()
		}
		return webJSModRegexAfterWord(w)
	case webJSModPunct:
		switch lx.text(n - 1) {
		case ")":
			return lx.condClose
		case "]", "++", "--":
			return false
		}
		return true
	case webJSModTemplateHead, webJSModTemplateMid:
		return true
	}
	return false
}

// prevWord returns the last token if it is a word that is not a property
// name (a.if, a?.return), else "".
func (lx *webJSModLexer) prevWord() string {
	return lx.wordAt(len(lx.toks) - 1)
}

// wordAt returns token k if it is a word that is not a property name, else "".
func (lx *webJSModLexer) wordAt(k int) string {
	if k < 0 || k >= len(lx.toks) || lx.toks[k].kind != webJSModIdent {
		return ""
	}
	if k >= 1 && lx.toks[k-1].kind == webJSModPunct {
		if p := lx.text(k - 1); p == "." || p == "?." {
			return ""
		}
	}
	return lx.text(k)
}

// ofKeyword reports whether the "of" just lexed is the keyword of a for-of
// head, which an expression (maybe a regex) follows, rather than a variable
// named "of" — minifiers such as esbuild hand out that name, and in of/2 the
// "/" divides. It is the keyword directly inside the parentheses after for,
// right after what ends a left-hand side: for (x of …), for ([a] of …). No
// semicolon is ever inserted in a for head, so a name followed by "of" there
// is nothing else.
func (lx *webJSModLexer) ofKeyword() bool {
	n := len(lx.toks)
	if len(lx.stack) == 0 || !lx.stack[len(lx.stack)-1].forHead || n < 2 {
		return false
	}
	switch lx.toks[n-2].kind {
	case webJSModIdent:
		// A property name (a.in) ends one; an operator keyword (in, typeof) does not.
		w := lx.wordAt(n - 2)
		return w == "" || !webJSModRegexAfterWord(w)
	case webJSModPunct:
		switch lx.text(n - 2) {
		case ")", "]", "}":
			return true
		}
	}
	return false
}

func (lx *webJSModLexer) text(i int) string {
	return lx.src[lx.toks[i].start:lx.toks[i].end]
}

func (lx *webJSModLexer) emit(kind webJSModKind, start, end int) {
	lx.toks = append(lx.toks, webJSModToken{start: int32(start), end: int32(end), match: -1, kind: kind, nl: lx.nl})
	lx.nl = false
	lx.pos = end
}

func (lx *webJSModLexer) errorf(at int, format string, args ...any) error {
	return webJSModErrorf(lx.src, at, format, args...)
}

// webJSModError is a transform error, whose message names the 1-based source
// line (the loader shows it in the script error). The line is counted only
// when the message is read: skipExpr gives up on, and drops, a parse error
// per broken class or function it meets, and counting from the start of a
// large module for each would make the transform quadratic.
type webJSModError struct {
	src string
	at  int
	msg string
}

func (e *webJSModError) Error() string {
	return fmt.Sprintf("module line %d: %s", webJSModLine(e.src, e.at), e.msg)
}

// webJSModErrorf returns a transform error at byte offset at of src.
func webJSModErrorf(src string, at int, format string, args ...any) error {
	return &webJSModError{src: src, at: at, msg: fmt.Sprintf(format, args...)}
}

// webJSModLine returns the 1-based line of byte offset at, counting line
// breaks the way JavaScript does: LF, CR, CR LF (once), U+2028 and U+2029.
func webJSModLine(src string, at int) int {
	at = min(max(at, 0), len(src))
	line := 1
	for i := 0; i < at; i++ {
		switch {
		case src[i] == '\n':
			line++
		case src[i] == '\r':
			if i+1 >= len(src) || src[i+1] != '\n' {
				line++ // a CR LF counts at its LF
			}
		case webJSModLineSep(src, i):
			line++
			i += 2
		}
	}
	return line
}

// webJSModRegexAfterWord reports whether a regex may follow the keyword word
// (return /x/ …); after any other word "/" divides. "of" is not here: it is
// a keyword only in a for-of head (see ofKeyword) and a common minified name.
func webJSModRegexAfterWord(word string) bool {
	switch word {
	case "return", "typeof", "instanceof", "in", "new", "delete", "void", "throw",
		"case", "do", "else", "yield", "await", "default", "extends":
		return true
	}
	return false
}

func webJSModOpener(c byte) byte {
	switch c {
	case ')':
		return '('
	case ']':
		return '['
	}
	return '{'
}

func webJSModDigit(c byte) bool { return c >= '0' && c <= '9' }

func webJSModIdentChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$'
}

// webJSModIdentStart reports whether c may begin an identifier. Every
// non-ASCII byte counts: white space and line separators were consumed
// before, and in valid JavaScript nothing else non-ASCII appears outside
// literals and comments.
func webJSModIdentStart(c byte) bool {
	return c == '\\' || c >= utf8.RuneSelf || webJSModIdentChar(c) && !webJSModDigit(c)
}

func webJSModHexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// webJSModLineSep reports whether s[i:] starts with U+2028 or U+2029, which
// end lines in JavaScript like LF and CR do.
func webJSModLineSep(s string, i int) bool {
	return i >= 0 && i+2 < len(s) && s[i] == 0xE2 && s[i+1] == 0x80 && (s[i+2] == 0xA8 || s[i+2] == 0xA9)
}

// webJSModUnicodeEscape decodes the \uXXXX or \u{X…} escape at s[i]; n is its
// length, 0 when there is none.
func webJSModUnicodeEscape(s string, i int) (r rune, n int) {
	if i < 0 || i+2 >= len(s) || s[i] != '\\' || s[i+1] != 'u' {
		return 0, 0
	}
	if s[i+2] == '{' {
		j := i + 3
		for ; j < len(s) && webJSModHexVal(s[j]) >= 0; j++ {
			if r = r<<4 | rune(webJSModHexVal(s[j])); r > unicode.MaxRune {
				return 0, 0
			}
		}
		if j == i+3 || j >= len(s) || s[j] != '}' {
			return 0, 0
		}
		return r, j + 1 - i
	}
	if i+6 > len(s) {
		return 0, 0
	}
	for j := i + 2; j < i+6; j++ {
		v := webJSModHexVal(s[j])
		if v < 0 {
			return 0, 0
		}
		r = r<<4 | rune(v)
	}
	return r, 6
}

// webJSModStringValue returns the value of the string literal raw (quotes
// included): module specifiers and export names are compared by value.
func webJSModStringValue(raw string) string {
	if len(raw) < 2 {
		return ""
	}
	s := raw[1 : len(raw)-1]
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		e := s[i+1]
		i += 2
		switch e {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '0':
			b.WriteByte(0)
		case '\n':
			// "\" LF continues the line
		case '\r':
			if i < len(s) && s[i] == '\n' {
				i++
			}
		case 'x':
			if i+1 < len(s) && webJSModHexVal(s[i]) >= 0 && webJSModHexVal(s[i+1]) >= 0 {
				b.WriteRune(rune(webJSModHexVal(s[i])<<4 | webJSModHexVal(s[i+1])))
				i += 2
			} else {
				b.WriteByte('x')
			}
		case 'u':
			r, n := webJSModUnicodeEscape(s, i-2)
			if n == 0 {
				b.WriteByte('u')
				break
			}
			i += n - 2
			// A character outside the BMP written as two escapes is one
			// character, not two replacement characters.
			if utf16.IsSurrogate(r) {
				if r2, n2 := webJSModUnicodeEscape(s, i); n2 > 0 {
					if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
						r = d
						i += n2
					}
				}
			}
			b.WriteRune(r)
		default:
			if webJSModLineSep(s, i-1) {
				i += 2 // "\" U+2028 or U+2029 continues the line
				break
			}
			b.WriteByte(e) // \' \" \\ and identity escapes
		}
	}
	return b.String()
}

// webJSModIdentValue returns the name an identifier token spells, decoding
// \u escapes (a\u0062 is the name "ab").
func webJSModIdentValue(raw string) string {
	if strings.IndexByte(raw, '\\') < 0 {
		return raw
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		if r, n := webJSModUnicodeEscape(raw, i); n > 0 {
			b.WriteRune(r)
			i += n
			continue
		}
		b.WriteByte(raw[i])
		i++
	}
	return b.String()
}

// webJSModQuote returns s as a double-quoted JavaScript string literal on one
// line (a raw line break would shift the line numbers after the prologue).
func webJSModQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// webJSModEdit replaces src[start:end] by text followed by the range's line
// breaks or, when text is "", by spaces that keep the line breaks.
type webJSModEdit struct {
	start int
	end   int
	text  string
}

// webJSModApply applies non-overlapping edits to src.
func webJSModApply(src string, edits []webJSModEdit) string {
	var b strings.Builder
	b.Grow(webJSModEditedSize(src, edits))
	webJSModApplyTo(&b, src, edits)
	return b.String()
}

// webJSModEditedSize bounds the length of src after edits: a replacement
// never makes a range longer than its text.
func webJSModEditedSize(src string, edits []webJSModEdit) int {
	n := len(src)
	for _, e := range edits {
		n += len(e.text)
	}
	return n
}

// webJSModApplyTo writes src with non-overlapping edits applied to b, so
// that a multi-megabyte module is not built twice.
func webJSModApplyTo(b *strings.Builder, src string, edits []webJSModEdit) {
	// Edits come in token order except the ";" after an anonymous default
	// export, which is recorded before the body it follows is walked. The
	// stable sort keeps an insertion ahead of a range starting at the same
	// offset: the ";" that goes before a removed statement stays ahead of
	// the spaces that replace it.
	sort.SliceStable(edits, func(x, y int) bool { return edits[x].start < edits[y].start })
	last := 0
	for _, e := range edits {
		if e.start < last || e.end < e.start || e.end > len(src) {
			continue // overlapping: never produced, but never worth a panic
		}
		b.WriteString(src[last:e.start])
		b.WriteString(e.text)
		removed := src[e.start:e.end]
		for i := 0; i < len(removed); i++ {
			c := removed[i]
			switch {
			case c == '\n' || c == '\r':
				b.WriteByte(c)
			case webJSModLineSep(removed, i):
				b.WriteString(removed[i : i+3])
				i += 2
			case e.text == "":
				b.WriteByte(' ')
			}
		}
		last = e.end
	}
	b.WriteString(src[last:])
}

// webJSModParser walks the tokens of one script, collecting the edits and the
// module prologue.
type webJSModParser struct {
	src     string
	toks    []webJSModToken
	edits   []webJSModEdit
	imports []webJSImport
	index   map[webJSImport]int // position of each entry of imports
	exports []string            // `"name": () => local` getters for __jh.exp, in source order
	reexps  []string            // __jh.reexp statements, in source order
	stars   []string            // __jh.star statements, in source order
	binds   []string            // import binding statements, in source order
	steps   int                 // what skipExpr and classBody may still do (see step)
	abort   error               // a limit was hit: the transform fails, even where a parse error would be passed over
}

// webJSTransformModule rewrites an ES module into
// "(function (__jh) { return (async function () { "use strict"; … })(); })"
// for the loader in webdom/85_scripts.js (contract: outputs/webdom/DESIGN.md,
// section M). imports lists the module's dependencies, one entry per distinct
// specifier and attribute type in order of first appearance.
func webJSTransformModule(src string) (code string, imports []webJSImport, err error) {
	if len(src) > webJSModMaxSource {
		return "", nil, fmt.Errorf("module too large (%d bytes)", len(src))
	}
	toks, err := webJSModLex(src, false)
	if err != nil {
		return "", nil, err
	}
	p := &webJSModParser{src: src, toks: toks, steps: 4*len(toks) + 4096}
	if n := webJSModPreamble(src); n > 0 {
		p.edits = append(p.edits, webJSModEdit{start: 0, end: n})
	}
	if err := p.module(); err != nil {
		return "", nil, err
	}
	size := 128 + webJSModEditedSize(src, p.edits)
	for _, list := range [][]string{p.exports, p.reexps, p.stars, p.binds} {
		for _, s := range list {
			size += len(s) + 2
		}
	}
	var b strings.Builder
	b.Grow(size)
	b.WriteString(`(function (__jh) { return (async function () { "use strict";`)
	if len(p.exports) > 0 {
		b.WriteString(" __jh.exp({ ")
		for k, s := range p.exports {
			if k > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s)
		}
		b.WriteString(" });")
	}
	for _, list := range [][]string{p.reexps, p.stars, p.binds} {
		for _, s := range list {
			b.WriteByte(' ')
			b.WriteString(s)
		}
	}
	b.WriteByte(' ')
	webJSModApplyTo(&b, src, p.edits)
	// On its own line: the module may end in a line comment.
	b.WriteString("\n})(); })")
	return b.String(), p.imports, nil
}

// webJSRewriteDynamicImport rewrites import(…) to __jh_import(…) and
// import.meta to ({url: document.baseURI}) in a classic script, touching
// nothing else; ok is false when there was nothing to rewrite or the script
// could not be lexed (then the engine reports its own syntax error).
func webJSRewriteDynamicImport(src string) (out string, ok bool) {
	if len(src) > webJSModMaxSource || !strings.Contains(src, "import") {
		return src, false
	}
	toks, err := webJSModLex(src, true)
	if err != nil {
		return src, false
	}
	p := &webJSModParser{src: src, toks: toks}
	for i := 0; i < len(toks); i++ {
		switch kind, last := p.dynamic(i); kind {
		case webJSModImportCall:
			p.replace(i, i+1, "__jh_import")
		case webJSModImportMeta:
			p.replace(i, last+1, "({url: document.baseURI})")
			i = last
		}
	}
	if len(p.edits) == 0 {
		return src, false
	}
	return webJSModApply(src, p.edits), true
}

// module rewrites the import and export declarations (top level only) and
// the import() calls and import.meta (anywhere) of a module.
func (p *webJSModParser) module() error {
	depth := 0
	for i := 0; i < len(p.toks); {
		next := i + 1
		switch p.toks[i].kind {
		case webJSModTemplateHead:
			depth++
		case webJSModTemplateTail:
			depth--
		case webJSModPunct:
			switch p.text(i) {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth--
			}
		case webJSModIdent:
			kind, last := p.dynamic(i)
			switch {
			case kind == webJSModImportCall:
				p.replace(i, i+1, "__jh.dyn")
			case kind == webJSModImportMeta:
				p.replace(i, last+1, "__jh.meta")
				next = last + 1
			case depth == 0 && !p.afterDot(i) && (p.text(i) == "import" || p.text(i) == "export"):
				var err error
				if p.text(i) == "import" {
					next, err = p.importDecl(i)
				} else {
					next, err = p.exportDecl(i)
				}
				if err != nil {
					return err
				}
			}
		}
		i = max(next, i+1)
	}
	return nil
}

// dynamic reports whether token i starts import(…) (webJSModImportCall) or
// import.meta (webJSModImportMeta; last is the index of "meta"). a.import(…),
// { import: 1 } and a method named import — `import(x) {` in an object
// literal or class body — are neither.
func (p *webJSModParser) dynamic(i int) (kind, last int) {
	if !p.isWord(i, "import") || p.afterDot(i) {
		return 0, i
	}
	if p.isPunct(i+1, "(") {
		if p.isPunct(p.match(i+1)+1, "{") {
			return 0, i
		}
		return webJSModImportCall, i
	}
	if p.isPunct(i+1, ".") && p.isWord(i+2, "meta") {
		return webJSModImportMeta, i + 2
	}
	return 0, i
}

// importDecl removes the import declaration at token i and records its
// bindings; it returns the index of the token after the declaration.
func (p *webJSModParser) importDecl(i int) (int, error) {
	j := i + 1
	var def, ns string
	var named []string
	if p.kind(j) != webJSModString { // not a bare `import "m"`
		more := true
		if p.kind(j) == webJSModIdent {
			def = p.text(j)
			j++
			more = p.isPunct(j, ",")
			if more {
				j++
			}
		}
		if more {
			switch {
			case p.isPunct(j, "*"):
				if !p.isWord(j+1, "as") || p.kind(j+2) != webJSModIdent {
					return 0, p.errorf(j, "expected * as name in import")
				}
				ns = p.text(j + 2)
				j += 3
			case p.isPunct(j, "{"):
				var err error
				if named, err = p.importNames(j); err != nil {
					return 0, err
				}
				j = p.match(j) + 1
			default:
				return 0, p.errorf(j, "unexpected token in import")
			}
		}
		if !p.isWord(j, "from") {
			return 0, p.errorf(j, "expected from in import")
		}
		j++
	}
	if p.kind(j) != webJSModString {
		return 0, p.errorf(j, "expected a module specifier")
	}
	typ, next, err := p.attributes(j + 1)
	if err != nil {
		return 0, err
	}
	if p.isPunct(next, ";") {
		next++
	}
	idx, err := p.addImport(j, webJSModStringValue(p.text(j)), typ)
	if err != nil {
		return 0, err
	}
	if def != "" {
		p.binds = append(p.binds, fmt.Sprintf("const %s = __jh.imp(%d).default;", def, idx))
	}
	if ns != "" {
		p.binds = append(p.binds, fmt.Sprintf("const %s = __jh.imp(%d);", ns, idx))
	}
	if len(named) > 0 {
		p.binds = append(p.binds, fmt.Sprintf("const { %s } = __jh.imp(%d);", strings.Join(named, ", "), idx))
	}
	p.removeStatement(i, next)
	return next, nil
}

// importNames parses the { a, b as c, "x-y" as z, default as d } clause
// opening at token open into destructuring entries "a: a", "b: c", …
func (p *webJSModParser) importNames(open int) ([]string, error) {
	end := p.match(open)
	var out []string
	for k := open + 1; k < end; {
		if p.isPunct(k, ",") {
			k++
			continue
		}
		value, raw, isStr, ok := p.name(k)
		if !ok {
			return nil, p.errorf(k, "bad import specifier")
		}
		key, local := raw, raw
		if isStr {
			key = webJSModQuote(value)
		}
		k++
		if p.isWord(k, "as") && k+1 < end {
			if p.kind(k+1) != webJSModIdent {
				return nil, p.errorf(k+1, "bad import binding")
			}
			local = p.text(k + 1)
			k += 2
		} else if isStr {
			return nil, p.errorf(k, "a string import name needs as")
		}
		if k < end && !p.isPunct(k, ",") {
			return nil, p.errorf(k, "expected , in import")
		}
		out = append(out, key+": "+local)
	}
	return out, nil
}

// attributes parses the optional `with { type: "json" }` (or the older
// `assert { … }`, which may not follow a line break) after the module
// specifier; i is the token after the specifier. It returns the type and the
// index after the clause.
func (p *webJSModParser) attributes(i int) (typ string, next int, err error) {
	clause := p.isWord(i, "with") || p.isWord(i, "assert") && !p.toks[i].nl
	if !clause || !p.isPunct(i+1, "{") {
		return "", i, nil
	}
	end := p.match(i + 1)
	for k := i + 2; k < end; {
		if p.isPunct(k, ",") {
			k++
			continue
		}
		key, _, _, ok := p.name(k)
		if !ok || !p.isPunct(k+1, ":") || p.kind(k+2) != webJSModString {
			return "", 0, p.errorf(k, "bad import attributes")
		}
		if key == "type" {
			typ = webJSModStringValue(p.text(k + 2))
		}
		k += 3
	}
	return typ, end + 1, nil
}

// exportDecl handles the export declaration at token i and returns the index
// to continue from: past a removed statement, or just past "export" (or
// "export default") when the declaration stays in the body, whose tokens
// must still be walked for import() and import.meta.
func (p *webJSModParser) exportDecl(i int) (int, error) {
	j := i + 1
	switch {
	case p.isPunct(j, "*"):
		return p.exportStar(i)
	case p.isPunct(j, "{"):
		return p.exportList(i)
	case p.isWord(j, "default"):
		return p.exportDefault(i)
	case p.isWord(j, "var") || p.isWord(j, "let") || p.isWord(j, "const"):
		names, err := p.bindingList(j + 1)
		if err != nil {
			return 0, err
		}
		for _, name := range names {
			p.exportLocal(webJSModIdentValue(name), name)
		}
	case p.isWord(j, "function") || p.isWord(j, "class") || p.isWord(j, "async") && p.isWord(j+1, "function"):
		k := j + 1
		if p.isWord(j, "async") {
			k++
		}
		if p.isPunct(k, "*") {
			k++
		}
		if p.kind(k) != webJSModIdent {
			return 0, p.errorf(k, "an exported declaration needs a name")
		}
		p.exportLocal(webJSModIdentValue(p.text(k)), p.text(k))
	default:
		return 0, p.errorf(j, "unexpected token after export")
	}
	p.blank(i, j)
	return j, nil
}

// exportStar handles `export * from "m"` and `export * as name from "m"`.
func (p *webJSModParser) exportStar(i int) (int, error) {
	j := i + 2
	as, hasAs := "", false
	if p.isWord(j, "as") {
		var ok bool
		if as, _, _, ok = p.name(j + 1); !ok {
			return 0, p.errorf(j+1, "bad export name")
		}
		hasAs = true
		j += 2
	}
	if !p.isWord(j, "from") || p.kind(j+1) != webJSModString {
		return 0, p.errorf(j, "expected from in export *")
	}
	typ, next, err := p.attributes(j + 2)
	if err != nil {
		return 0, err
	}
	if p.isPunct(next, ";") {
		next++
	}
	idx, err := p.addImport(j+1, webJSModStringValue(p.text(j+1)), typ)
	if err != nil {
		return 0, err
	}
	if hasAs {
		p.exports = append(p.exports, fmt.Sprintf("%s: () => __jh.imp(%d)", webJSModKey(as), idx))
	} else {
		p.stars = append(p.stars, fmt.Sprintf("__jh.star(%d);", idx))
	}
	p.removeStatement(i, next)
	return next, nil
}

// exportList handles `export { a, b as c }` (local getters) and
// `export { a as b, default } from "m"` (re-exports).
func (p *webJSModParser) exportList(i int) (int, error) {
	open := i + 1
	end := p.match(open)
	from := p.isWord(end+1, "from")
	var entries []string
	for k := open + 1; k < end; {
		if p.isPunct(k, ",") {
			k++
			continue
		}
		local, raw, isStr, ok := p.name(k)
		if !ok {
			return 0, p.errorf(k, "bad export specifier")
		}
		exported := local
		k++
		if p.isWord(k, "as") && k+1 < end {
			if exported, _, _, ok = p.name(k + 1); !ok {
				return 0, p.errorf(k+1, "bad export name")
			}
			k += 2
		}
		if k < end && !p.isPunct(k, ",") {
			return 0, p.errorf(k, "expected , in export")
		}
		switch {
		case from:
			entries = append(entries, webJSModKey(exported)+": "+webJSModQuote(local))
		case isStr:
			return 0, p.errorf(k-1, "a string export name needs from")
		default:
			entries = append(entries, webJSModKey(exported)+": () => "+raw)
		}
	}
	next := end + 1
	if from {
		if p.kind(next+1) != webJSModString {
			return 0, p.errorf(next+1, "expected a module specifier")
		}
		typ, after, err := p.attributes(next + 2)
		if err != nil {
			return 0, err
		}
		idx, err := p.addImport(next+1, webJSModStringValue(p.text(next+1)), typ)
		if err != nil {
			return 0, err
		}
		if len(entries) > 0 {
			p.reexps = append(p.reexps, fmt.Sprintf("__jh.reexp(%d, { %s });", idx, strings.Join(entries, ", ")))
		}
		next = after
	} else {
		p.exports = append(p.exports, entries...)
	}
	if p.isPunct(next, ";") {
		next++
	}
	p.removeStatement(i, next)
	return next, nil
}

// exportDefault handles `export default …` at token i. A named function or
// class stays a (hoisted) declaration; an anonymous one becomes
// `const __jh_default = function () {…};` — the ";" matters, since the
// declaration form needs none and the next line may start with "(" or "[" —
// and any other expression becomes `const __jh_default = <expr>`, ending
// where the statement ended.
func (p *webJSModParser) exportDefault(i int) (int, error) {
	d := i + 1
	k := d + 1
	if k >= len(p.toks) {
		return 0, p.errorf(d, "export default needs a value")
	}
	if p.isWord(k, "async") && p.isWord(k+1, "function") && !p.toks[k+1].nl {
		k++
	}
	body := -1
	switch {
	case p.isWord(k, "function"):
		n := k + 1
		if p.isPunct(n, "*") {
			n++
		}
		if p.kind(n) == webJSModIdent {
			p.exportLocal("default", p.text(n))
			p.blank(i, d+1)
			return d + 1, nil
		}
		var err error
		if body, err = p.functionBody(n); err != nil {
			return 0, err
		}
	case p.isWord(k, "class"):
		n := k + 1
		if p.kind(n) == webJSModIdent && p.text(n) != "extends" {
			p.exportLocal("default", p.text(n))
			p.blank(i, d+1)
			return d + 1, nil
		}
		var err error
		if body, err = p.classBody(n, 0); err != nil {
			return 0, err
		}
	}
	p.replace(i, d+1, "const __jh_default =")
	if body >= 0 {
		end := int(p.toks[body].end)
		p.edits = append(p.edits, webJSModEdit{start: end, end: end, text: ";"})
	}
	p.exportLocal("default", "__jh_default")
	return d + 1, nil
}

// functionBody returns the index of the "}" ending the body of a function
// whose parameter list opens at token n.
func (p *webJSModParser) functionBody(n int) (int, error) {
	if !p.isPunct(n, "(") {
		return 0, p.errorf(n, "expected ( after function")
	}
	b := p.match(n) + 1
	if !p.isPunct(b, "{") {
		return 0, p.errorf(b, "expected a function body")
	}
	return p.match(b), nil
}

// classBody returns the index of the "}" ending the body of a class whose
// name, extends clause or body starts at token n; depth counts the classes
// whose heritage this one is part of. The heritage is a left-hand-side
// expression, walked as operands and what may follow one: before an operand
// only a name or literal, new, a class or function expression or a bracket;
// after one only the body's "{", "." or "?.", a call, an index or a tagged
// template. Anything else fails at once, so that a heritage running into
// garbage (no semicolons, no line breaks) cannot carry the scan to the end of
// the file and into every class after it.
func (p *webJSModParser) classBody(n, depth int) (int, error) {
	if depth >= webJSModMaxClassDepth {
		p.abort = p.errorf(n, "classes nested too deep")
		return 0, p.abort
	}
	if p.kind(n) == webJSModIdent && p.text(n) != "extends" {
		n++
	}
	if !p.isWord(n, "extends") {
		if !p.isPunct(n, "{") {
			return 0, p.errorf(n, "expected a class body")
		}
		return p.match(n), nil
	}
	operand := false // the tokens before n end an operand
	for n++; n < len(p.toks); {
		if err := p.step(n); err != nil {
			return 0, err
		}
		kind, word := p.kind(n), ""
		if kind == webJSModIdent && !p.afterDot(n) {
			word = p.text(n) // not a property name, so maybe a keyword
		}
		switch {
		case operand && p.isPunct(n, "{"):
			return p.match(n), nil
		case operand && (p.isPunct(n, ".") || p.isPunct(n, "?.")):
			n, operand = n+1, false
		case operand && (p.isPunct(n, "(") || p.isPunct(n, "[") || kind == webJSModTemplateHead):
			n = p.match(n) + 1 // a call, an index or a tagged template
		case operand && kind == webJSModTemplate:
			n++ // a tagged template
		case operand:
			return 0, p.errorf(n, "expected a class body")
		case p.isPunct(n, "(") || p.isPunct(n, "[") || p.isPunct(n, "{") || kind == webJSModTemplateHead:
			n, operand = p.match(n)+1, true
		case word == "new" && !p.isPunct(n+1, "."):
			n++ // new X(): an operand follows; new.target is one
		case word == "async" && p.isWord(n+1, "function") && !p.toks[n+1].nl:
			n++
		case word == "class":
			end, err := p.classBody(n+1, depth+1)
			if err != nil {
				return 0, err
			}
			n, operand = end+1, true
		case word == "function":
			end, err := p.functionBody(p.functionParams(n))
			if err != nil {
				return 0, err
			}
			n, operand = end+1, true
		case kind == webJSModIdent || kind == webJSModPrivate || kind == webJSModNumber ||
			kind == webJSModString || kind == webJSModTemplate || kind == webJSModRegex:
			n, operand = n+1, true
		default:
			return 0, p.errorf(n, "expected a class body")
		}
	}
	return 0, p.errorf(n, "expected a class body")
}

// functionParams returns the index of the "(" of the function expression
// whose "function" keyword is token n (skipping "*" and the name).
func (p *webJSModParser) functionParams(n int) int {
	n++
	if p.isPunct(n, "*") {
		n++
	}
	if p.kind(n) == webJSModIdent {
		n++
	}
	return n
}

// bindingList returns the names bound by the var/let/const declaration list
// starting at token i (export const {a, b: [c], ...d} = o binds a, c and d).
func (p *webJSModParser) bindingList(i int) ([]string, error) {
	var names []string
	for {
		var err error
		if names, i, err = p.pattern(i, names); err != nil {
			return nil, err
		}
		if p.isPunct(i, "=") {
			if i, err = p.skipExpr(i + 1); err != nil {
				return nil, err
			}
		}
		if !p.isPunct(i, ",") {
			return names, nil
		}
		i++
	}
}

// pattern appends the names bound by the binding identifier or destructuring
// pattern at token i and returns the index after it.
func (p *webJSModParser) pattern(i int, names []string) ([]string, int, error) {
	switch {
	case p.kind(i) == webJSModIdent:
		return append(names, p.text(i)), i + 1, nil
	case p.isPunct(i, "["):
		end := p.match(i)
		for k := i + 1; k < end; {
			if p.isPunct(k, ",") {
				k++ // a hole
				continue
			}
			if p.isPunct(k, "...") {
				k++
			}
			var err error
			if names, k, err = p.pattern(k, names); err != nil {
				return nil, 0, err
			}
			if p.isPunct(k, "=") {
				if k, err = p.skipExpr(k + 1); err != nil {
					return nil, 0, err
				}
			}
			if k < end && !p.isPunct(k, ",") {
				return nil, 0, p.errorf(k, "bad array pattern")
			}
		}
		return names, end + 1, nil
	case p.isPunct(i, "{"):
		end := p.match(i)
		for k := i + 1; k < end; {
			var err error
			switch {
			case p.isPunct(k, ","):
				k++
				continue
			case p.isPunct(k, "..."):
				names, k, err = p.pattern(k+1, names)
			case p.isPunct(k, "["): // [computed]: element
				k = p.match(k) + 1
				if !p.isPunct(k, ":") {
					return nil, 0, p.errorf(k, "bad object pattern")
				}
				names, k, err = p.pattern(k+1, names)
			case p.isPunct(k+1, ":"): // key: element
				names, k, err = p.pattern(k+2, names)
			case p.kind(k) == webJSModIdent: // shorthand
				names, k = append(names, p.text(k)), k+1
			default:
				return nil, 0, p.errorf(k, "bad object pattern")
			}
			if err != nil {
				return nil, 0, err
			}
			if p.isPunct(k, "=") {
				if k, err = p.skipExpr(k + 1); err != nil {
					return nil, 0, err
				}
			}
			if k < end && !p.isPunct(k, ",") {
				return nil, 0, p.errorf(k, "bad object pattern")
			}
		}
		return names, end + 1, nil
	}
	return nil, 0, p.errorf(i, "bad binding pattern")
}

// skipExpr skips the assignment expression starting at token i and returns
// the index after it: a "," or ";" or closing bracket at the same level, the
// first token of the next line where automatic semicolon insertion ends the
// statement, or an "export" or "import" that starts the next one. Only the
// names a declaration binds depend on this; the declaration itself stays in
// the body untouched.
func (p *webJSModParser) skipExpr(i int) (int, error) {
	start := i
	for i < len(p.toks) {
		if err := p.step(i); err != nil {
			return 0, err
		}
		t := p.toks[i]
		if t.kind == webJSModPunct {
			switch p.text(i) {
			case ",", ";", ")", "]", "}":
				return i, nil
			}
		}
		if i > start && t.nl && p.endsExpr(i-1) && !p.continuesExpr(i) {
			return i, nil
		}
		// Neither continues an expression. Stopping there also keeps every
		// export on a line without semicolons (garbage, but possible) from
		// scanning on to the end of the file.
		if p.startsStatement(i) {
			return i, nil
		}
		switch {
		case p.isWord(i, "function") && !p.afterDot(i):
			// Skipped whole: a body brace on the line after ")" does not end
			// the expression the way a "{" after a value would.
			if end, err := p.functionBody(p.functionParams(i)); err == nil {
				i = end + 1
			} else {
				i++
			}
		case p.isWord(i, "class") && !p.afterDot(i):
			end, err := p.classBody(i+1, 0)
			switch {
			case err == nil:
				i = end + 1
			case p.abort != nil:
				return 0, p.abort
			default:
				i++ // what follows is not a class; at worst a name is missed
			}
		case int(t.match) > i: // a bracket or a template literal with substitutions
			i = int(t.match) + 1
		default:
			i++
		}
	}
	return i, nil
}

// startsStatement reports whether token i is "export" or an "import" other
// than import(…) and import.meta: words that only ever start a statement.
func (p *webJSModParser) startsStatement(i int) bool {
	if p.kind(i) != webJSModIdent || p.afterDot(i) {
		return false
	}
	switch p.text(i) {
	case "export":
		return true
	case "import":
		return !p.isPunct(i+1, "(") && !p.isPunct(i+1, ".")
	}
	return false
}

// step charges one step of skipExpr or classBody, which may scan tokens
// again after a parse they gave up on, against a budget linear in the number
// of tokens (real code needs a fraction of it): past it, the module is
// refused rather than keeping a goroutine the watchdog cannot stop busy.
func (p *webJSModParser) step(i int) error {
	if p.abort != nil {
		return p.abort
	}
	p.steps--
	if p.steps < 0 {
		p.abort = p.errorf(i, "module too complex")
		return p.abort
	}
	return nil
}

// endsExpr reports whether token i can end an expression, so that a line
// break after it may end the statement.
func (p *webJSModParser) endsExpr(i int) bool {
	switch p.kind(i) {
	case webJSModIdent:
		switch p.text(i) {
		case "typeof", "void", "delete", "new", "in", "instanceof", "await", "yield", "extends":
			return p.afterDot(i) // a.new is a property, which ends an expression
		}
		return true
	case webJSModNumber, webJSModString, webJSModTemplate, webJSModTemplateTail, webJSModRegex, webJSModPrivate:
		return true
	case webJSModPunct:
		switch p.text(i) {
		case ")", "]", "}", "++", "--":
			return true
		}
	}
	return false
}

// continuesExpr reports whether token i, first on its line, continues the
// expression before it (binary operators, ".", "(", "[", "?", templates), so
// that no semicolon is inserted. "++", "--" and "{" start a new statement.
func (p *webJSModParser) continuesExpr(i int) bool {
	switch p.kind(i) {
	case webJSModTemplate, webJSModTemplateHead:
		return true
	case webJSModIdent:
		return p.text(i) == "in" || p.text(i) == "instanceof"
	case webJSModPunct:
		switch p.text(i) {
		case "{", "!", "~", "++", "--", "...", "#", "@":
			return false
		}
		return true
	}
	return false
}

// name reads an IdentifierName or a string literal (module export names may
// be either) at token i.
func (p *webJSModParser) name(i int) (value, raw string, isStr, ok bool) {
	switch p.kind(i) {
	case webJSModIdent:
		raw = p.text(i)
		return webJSModIdentValue(raw), raw, false, true
	case webJSModString:
		raw = p.text(i)
		return webJSModStringValue(raw), raw, true, true
	}
	return "", "", false, false
}

// exportLocal adds a live getter exporting the local binding raw (as
// written in the source) under the name exported.
func (p *webJSModParser) exportLocal(exported, raw string) {
	p.exports = append(p.exports, webJSModKey(exported)+": () => "+raw)
}

// webJSModKey returns an export name as a key of the object literals the
// prologue hands to __jh.exp and __jh.reexp. "__proto__" is a computed key:
// written plainly it would set the literal's prototype, and the export would
// be lost.
func webJSModKey(name string) string {
	if name == "__proto__" {
		return `["__proto__"]`
	}
	return webJSModQuote(name)
}

// addImport returns the index of (spec, typ) in the module's imports, adding
// it on first appearance; at is the specifier's token, for the error past
// webJSModMaxImports. The index map keeps a module of many imports linear.
func (p *webJSModParser) addImport(at int, spec, typ string) (int, error) {
	im := webJSImport{Spec: spec, Type: typ}
	if i, ok := p.index[im]; ok {
		return i, nil
	}
	if len(p.imports) >= webJSModMaxImports {
		return 0, p.errorf(at, "more than %d imports", webJSModMaxImports)
	}
	if p.index == nil {
		p.index = make(map[webJSImport]int)
	}
	p.index[im] = len(p.imports)
	p.imports = append(p.imports, im)
	return len(p.imports) - 1, nil
}

// blank removes tokens from..to-1, with the comments between them, keeping
// their line breaks.
func (p *webJSModParser) blank(from, to int) {
	p.replace(from, to, "")
}

// removeStatement blanks the whole statement at tokens from..to-1 behind a
// ";". A line break may have been all that ended the statement before it,
// and with the statement gone the lines around it would join:
// `a = 1` + `export * from "m"` + `(f)()` must not become a call of 1.
func (p *webJSModParser) removeStatement(from, to int) {
	if from < 0 || to > len(p.toks) || from >= to {
		return
	}
	at := int(p.toks[from].start)
	p.edits = append(p.edits, webJSModEdit{start: at, end: at, text: ";"})
	p.blank(from, to)
}

// replace puts text in place of tokens from..to-1 (see webJSModEdit).
func (p *webJSModParser) replace(from, to int, text string) {
	if from < 0 || to > len(p.toks) || from >= to {
		return
	}
	p.edits = append(p.edits, webJSModEdit{start: int(p.toks[from].start), end: int(p.toks[to-1].end), text: text})
}

func (p *webJSModParser) kind(i int) webJSModKind {
	if i < 0 || i >= len(p.toks) {
		return 0
	}
	return p.toks[i].kind
}

func (p *webJSModParser) text(i int) string {
	if i < 0 || i >= len(p.toks) {
		return ""
	}
	return p.src[p.toks[i].start:p.toks[i].end]
}

func (p *webJSModParser) isPunct(i int, s string) bool {
	return p.kind(i) == webJSModPunct && p.text(i) == s
}

func (p *webJSModParser) isWord(i int, s string) bool {
	return p.kind(i) == webJSModIdent && p.text(i) == s
}

// afterDot reports whether token i is a property name after "." or "?.".
func (p *webJSModParser) afterDot(i int) bool {
	return p.isPunct(i-1, ".") || p.isPunct(i-1, "?.")
}

// match returns the partner of the bracket at token i, or i when it has none.
func (p *webJSModParser) match(i int) int {
	if i < 0 || i >= len(p.toks) || p.toks[i].match < 0 {
		return i
	}
	return int(p.toks[i].match)
}

func (p *webJSModParser) errorf(i int, format string, args ...any) error {
	at := len(p.src)
	if i >= 0 && i < len(p.toks) {
		at = int(p.toks[i].start)
	}
	return webJSModErrorf(p.src, at, format, args...)
}
