package main

// webdom_parse.go — the HTML parser behind the browser's JavaScript DOM.
//
// The DOM prelude (player/webdom/*.js, run in goja) builds its document from
// the JSON tree this file produces, and calls back into it for innerHTML,
// insertAdjacentHTML, document.write and DOMParser. Tokenizing in Go keeps the
// hot loop out of the interpreter: the handheld parses a page natively once
// and hands it over as one string that JSON.parse turns into nested arrays.
//
// The tree builder is a simplified HTML5 one. It knows the implied
// html/head/body, p/li/dd/dt closing, headings, tables with implied
// tbody/tr/colgroup and foster parenting, SVG/MathML foreign content and the
// end-tag scoping rules, but it has no adoption agency and does not reopen
// formatting elements, so badly misnested markup gets a sensible tree that is
// not always the one a desktop browser would build. Bad input can never make
// it panic or spin: every loop advances, the stack depth, attribute count and
// node count are capped, and a token is handed back to the builder only a
// bounded number of times.
//
// The JSON tree (the contract with webdom.js) is an array of top-level nodes:
// text is a string, an element ["tag", ATTRS, CHILDREN] or ["tag", ATTRS,
// CHILDREN, NS] (NS 1 SVG, 2 MathML; omitted for HTML), ATTRS 0 or a flat
// ["name", "value", …] array in source order, CHILDREN 0 or an array, a
// comment [8, "data"] and a doctype [10, "name", "publicId", "systemId"].

import (
	"html"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// webDOMParseMaxDepth bounds the stack of open elements. Deeper start
	// tags are still inserted but not pushed, so the tree stays shallow
	// enough for the recursive JSON encoder and the tree walks in webdom.js.
	webDOMParseMaxDepth = 512
	// webDOMParseMaxAttrs caps the attributes kept per element: a tag with
	// thousands of them is an attack on the duplicate check, not markup.
	webDOMParseMaxAttrs = 256
	// webDOMParseAttrSetAt is where the duplicate-attribute check switches
	// from scanning the kept names to a set of them: the scan is cheaper for
	// the few attributes real tags have, the set keeps a tag that repeats one
	// name a million times (repeats do not count toward the cap) linear.
	webDOMParseAttrSetAt = 32
	// webDOMParseMaxReprocess bounds how often one token goes round the tree
	// builder after an implied start or end tag (a <td> straight in a table
	// goes round three times: tbody, tr, td).
	webDOMParseMaxReprocess = 16
	// webDOMParseWorkPerByte and webDOMParseWorkBase set the work budget.
	// End tags and some start tags search the stack of open elements (up to
	// webDOMParseMaxDepth entries) without creating a node, so the node
	// budget never stops them: 500 open spans and a million stray end tags
	// would be hundreds of steps per byte, seconds of an uninterruptible call
	// on the handheld. Every such step counts, and past WorkPerByte steps per
	// input byte plus WorkBase the parse stops as truncated (the caller falls
	// back). Real pages, even nested 40 deep, take a few steps per byte.
	webDOMParseWorkPerByte = 16
	webDOMParseWorkBase    = 1 << 20
	webDOMParseHex         = "0123456789abcdef"
)

// Node kinds of webDOMParseNode.
const (
	webDOMParseElementNode uint8 = iota
	webDOMParseTextNode
	webDOMParseCommentNode
	webDOMParseDoctypeNode
)

// Token kinds of webDOMParseToken.
const (
	webDOMParseEOF uint8 = iota
	webDOMParseTextToken
	webDOMParseStartTag
	webDOMParseEndTag
	webDOMParseCommentToken
	webDOMParseDoctypeToken
)

// Document-level insertion modes (webDOMParseBuilder.mode). From <body> on
// everything runs in webDOMParseInBody, where the open elements pick the
// table modes below.
const (
	webDOMParseInitial uint8 = iota
	webDOMParseBeforeHTML
	webDOMParseBeforeHead
	webDOMParseInHead
	webDOMParseAfterHead
	webDOMParseInBody
)

// Body modes: the spec's table-related insertion modes, derived from the
// nearest table, table section, row, cell, caption, colgroup or template on
// the stack (webDOMParseBuilder.modes) instead of being switched by hand.
// webDOMParseModeNone marks an element that leaves the mode of its parent.
const (
	webDOMParseModeBody uint8 = iota
	webDOMParseModeTable
	webDOMParseModeTableBody
	webDOMParseModeRow
	webDOMParseModeCell
	webDOMParseModeCaption
	webDOMParseModeColgroup
	webDOMParseModeTemplate
	webDOMParseModeNone
)

// Element scopes for webDOMParseBuilder.inScope.
const (
	webDOMParseScopeDefault uint8 = iota
	webDOMParseScopeButton
	webDOMParseScopeList
	webDOMParseScopeTable
)

// Raw text kinds for webDOMParseTokenizer.rawText.
const (
	webDOMParseRawText uint8 = iota
	webDOMParseRCDATA
	webDOMParseScriptData
)

// webDOMParseNode is one node of the tree under construction. Text grows in
// place when adjacent text merges, so a page that splits one text run into
// thousands of tokens (stray end tags, foster parenting) stays linear.
type webDOMParseNode struct {
	parent   *webDOMParseNode
	children []*webDOMParseNode
	attrs    []string // element: flat name/value pairs; doctype: public and system id
	data     []byte   // text or comment data
	name     string   // element local name or doctype name
	kind     uint8
	ns       uint8 // 0 HTML, 1 SVG, 2 MathML
	htmlIP   bool  // MathML annotation-xml declaring HTML content (webDOMParseHTMLIntegration)
}

// webDOMParseToken is one tokenizer result. Text is already entity-decoded;
// a doctype keeps its public and system id in attrs.
type webDOMParseToken struct {
	attrs       []string
	name        string
	data        string
	kind        uint8
	selfClosing bool
}

// webDOMParseTokenizer walks the (CR- and NUL-normalized) source with an
// index. Raw text, RCDATA and plaintext are read on request of the tree
// builder, the only one that knows which element was just opened.
type webDOMParseTokenizer struct {
	src     string
	pos     int
	cdataOK bool // the current node is SVG/MathML: <![CDATA[…]]> is text
}

// webDOMParseBuilder is the tree builder: a stack of open elements plus the
// few pointers and flags the simplified insertion modes need.
type webDOMParseBuilder struct {
	tz        webDOMParseTokenizer
	doc       *webDOMParseNode   // the document, or the fragment context; never emitted itself
	stack     []*webDOMParseNode // open elements: stack[0] is html or the fragment context
	modes     []uint8            // modes[i]: the body mode stack[0..i] puts the parser in
	html      *webDOMParseNode
	head      *webDOMParseNode
	body      *webDOMParseNode
	form      *webDOMParseNode // the form element pointer: a nested <form> is ignored
	nodes     int
	maxNodes  int
	work      int   // stack-walk steps so far (and attribute merges): see webDOMParseWorkPerByte
	maxWork   int   // the work budget: past it the parse stops as truncated
	reprocess int   // rounds the current token has made (webDOMParseMaxReprocess)
	mode      uint8 // document-level insertion mode
	foster    bool  // foster parenting is on (anything-else tokens in table modes)
	skipLF    bool  // drop a leading LF from the next text token (<pre>, <listing>)
	afterBody bool  // after </body>: comments go to html
	afterHTML bool  // after </html>: comments go to the document
	stopped   bool  // the node or work limit was hit: stop with what was built
}

var (
	// webDOMParseSVGTagNames maps the lower-cased SVG element names the
	// tokenizer produces back to their camelCase spelling.
	webDOMParseSVGTagNames = sync.OnceValue(func() map[string]string {
		return webDOMParseCaseTable("altGlyph altGlyphDef altGlyphItem animateColor animateMotion animateTransform " +
			"clipPath feBlend feColorMatrix feComponentTransfer feComposite feConvolveMatrix feDiffuseLighting " +
			"feDisplacementMap feDistantLight feDropShadow feFlood feFuncA feFuncB feFuncG feFuncR feGaussianBlur " +
			"feImage feMerge feMergeNode feMorphology feOffset fePointLight feSpecularLighting feSpotLight feTile " +
			"feTurbulence foreignObject glyphRef linearGradient radialGradient textPath")
	})
	// webDOMParseSVGAttrNames does the same for SVG attributes (viewbox →
	// viewBox): scripts and the renderer look them up in camelCase.
	webDOMParseSVGAttrNames = sync.OnceValue(func() map[string]string {
		return webDOMParseCaseTable("attributeName attributeType baseFrequency baseProfile calcMode clipPathUnits " +
			"diffuseConstant edgeMode filterUnits glyphRef gradientTransform gradientUnits kernelMatrix " +
			"kernelUnitLength keyPoints keySplines keyTimes lengthAdjust limitingConeAngle markerHeight markerUnits " +
			"markerWidth maskContentUnits maskUnits numOctaves pathLength patternContentUnits patternTransform " +
			"patternUnits pointsAtX pointsAtY pointsAtZ preserveAlpha preserveAspectRatio primitiveUnits refX refY " +
			"repeatCount repeatDur requiredExtensions requiredFeatures specularConstant specularExponent " +
			"spreadMethod startOffset stdDeviation stitchTiles surfaceScale systemLanguage tableValues targetX " +
			"targetY textLength viewBox viewTarget xChannelSelector yChannelSelector zoomAndPan")
	})
)

// webDOMParseDocument parses a whole HTML document into the JSON tree webdom.js
// builds its DOM from: [doctype?] [comments…] ["html", A, [["head", …],
// ["body", …]]] [comments…], with html, head and body created when the source
// lacks them. maxNodes <= 0 means no limit; when the next node would exceed
// it, or the markup costs more than the work budget (webDOMParseWorkPerByte),
// parsing stops and the tree built so far comes back with truncated set —
// still with its html, head and body, for which three nodes of the budget are
// held back (a budget below 4 can therefore come back with up to 4 nodes).
func webDOMParseDocument(src string, maxNodes int) (tree string, nodes int, truncated bool) {
	// A byte order mark the fetch left in place would otherwise be text that
	// opens <body> before the doctype and the head elements are seen.
	src = strings.TrimPrefix(webDOMParseNormalize(src), "\uFEFF")
	b := &webDOMParseBuilder{tz: webDOMParseTokenizer{src: src}, doc: &webDOMParseNode{},
		maxWork: webDOMParseWorkPerByte*len(src) + webDOMParseWorkBase}
	if maxNodes > 0 {
		// Callers that ignore truncated (DOMParser) must still get a document
		// whose body exists, so the skeleton is never what the budget cuts.
		b.maxNodes = max(maxNodes-3, 1)
	}
	b.run()
	truncated = b.stopped
	b.stopped, b.maxNodes = false, 0
	b.finishDocument()
	return webDOMParseEncode(b.doc.children, len(src)), b.nodes, truncated
}

// webDOMParseFragment parses markup the way innerHTML, insertAdjacentHTML and
// document.write do, in the context of an element: context is its local name
// (lower-case for HTML), ns 0 HTML, 1 SVG, 2 MathML. The context element is
// the root of the stack but not part of the result: the tree is the list of
// its new children.
func webDOMParseFragment(src, context string, ns int, maxNodes int) (tree string, nodes int, truncated bool) {
	src = webDOMParseNormalize(src)
	root := &webDOMParseNode{name: webDOMParseLower(context)}
	switch ns {
	case 1:
		root.ns = 1
		root.name = webDOMParseSVGName(root.name)
	case 2:
		root.ns = 2
	}
	b := &webDOMParseBuilder{tz: webDOMParseTokenizer{src: src}, doc: root, maxNodes: maxNodes, mode: webDOMParseInBody,
		maxWork: webDOMParseWorkPerByte*len(src) + webDOMParseWorkBase}
	if root.ns == 0 {
		switch root.name {
		case "script", "style", "xmp", "iframe", "noembed", "noframes", "noscript", "plaintext":
			// The tokenizer starts in raw text with no start tag an end tag
			// could match, so even "</script>" is just more text.
			b.appendText(root, src)
			return webDOMParseEncode(root.children, len(src)), b.nodes, b.stopped
		case "textarea", "title":
			b.appendText(root, webDOMParseText(src))
			return webDOMParseEncode(root.children, len(src)), b.nodes, b.stopped
		case "html":
			// documentElement.innerHTML gets a head and a body like a
			// document does, also when the budget stops it (two nodes are
			// held back for them, as in webDOMParseDocument).
			b.html = root
			b.mode = webDOMParseBeforeHead
			if maxNodes > 0 {
				b.maxNodes = max(maxNodes-2, 1)
			}
		case "form":
			// The context is where the form element pointer starts, so a
			// <form> inside it is ignored as it is in a page (nested forms
			// do not exist); a </form> clears the pointer.
			b.form = root
		}
	}
	b.push(root)
	b.run()
	truncated = b.stopped
	if b.html == root {
		b.stopped, b.maxNodes = false, 0
		b.finishDocument()
	}
	return webDOMParseEncode(root.children, len(src)), b.nodes, truncated
}

// webDOMParseNormalize is the input stream preprocessing: CRLF and CR become
// LF, NUL becomes U+FFFD.
func webDOMParseNormalize(s string) string {
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "\uFFFD")
	}
	return s
}

// ---------------------------------------------------------------- tokenizer

// next returns the next token of the data state: a run of text, a tag, a
// comment or a doctype.
func (t *webDOMParseTokenizer) next() webDOMParseToken {
	s := t.src
	for t.pos < len(s) {
		start, i := t.pos, t.pos
		for i < len(s) {
			k := strings.IndexByte(s[i:], '<')
			if k < 0 {
				i = len(s)
				break
			}
			i += k
			if i+1 < len(s) && webDOMParseOpensMarkup(s[i+1]) {
				break
			}
			i++ // a '<' that opens nothing ("a < b", "<3") is text
		}
		if i > start {
			t.pos = i
			return webDOMParseToken{kind: webDOMParseTextToken, data: webDOMParseText(s[start:i])}
		}
		if tok, ok := t.markup(); ok {
			return tok
		}
	}
	return webDOMParseToken{kind: webDOMParseEOF}
}

// markup reads what starts at t.pos: a '<' followed by a letter, '/', '!' or
// '?'. ok is false when nothing is emitted ("</>", or a tag cut off by the
// end of the input); t.pos has advanced either way.
func (t *webDOMParseTokenizer) markup() (tok webDOMParseToken, ok bool) {
	s, i := t.src, t.pos
	switch c := s[i+1]; c {
	case '!':
		rest := s[i+2:]
		switch {
		case strings.HasPrefix(rest, "--"):
			return t.comment(i + 4), true
		case len(rest) >= 7 && webDOMParseEqualFold(rest[:7], "doctype"):
			return t.doctype(i + 9), true
		case t.cdataOK && strings.HasPrefix(rest, "[CDATA["):
			// CDATA is only a section in SVG and MathML; in HTML it is a
			// bogus comment.
			start := i + 9
			end := strings.Index(s[start:], "]]>")
			if end < 0 {
				t.pos = len(s)
				return webDOMParseToken{kind: webDOMParseTextToken, data: s[start:]}, true
			}
			t.pos = start + end + 3
			return webDOMParseToken{kind: webDOMParseTextToken, data: s[start : start+end]}, true
		}
		return t.bogus(i + 2), true
	case '?':
		return t.bogus(i + 1), true
	case '/':
		if i+2 >= len(s) {
			t.pos = len(s)
			return webDOMParseToken{kind: webDOMParseTextToken, data: "</"}, true
		}
		switch d := s[i+2]; {
		case webDOMParseIsLetter(d):
			return t.tag(i+2, webDOMParseEndTag)
		case d == '>':
			t.pos = i + 3
			return tok, false
		}
		return t.bogus(i + 2), true
	}
	return t.tag(i+1, webDOMParseStartTag)
}

// tag reads a start or end tag whose name starts at i. Attributes on end tags
// are parsed and dropped; a tag cut off by the end of the input is dropped
// whole, as the spec's EOF-in-tag rule says.
func (t *webDOMParseTokenizer) tag(i int, kind uint8) (webDOMParseToken, bool) {
	s := t.src
	start := i
	for i < len(s) && !webDOMParseIsSpace(s[i]) && s[i] != '/' && s[i] != '>' {
		i++
	}
	tok := webDOMParseToken{kind: kind, name: webDOMParseLower(s[start:i])}
	var seen map[string]struct{} // the kept attribute names, from webDOMParseAttrSetAt on
	for {
		// Whitespace, and a '/' that does not end the tag, separate
		// attributes.
		for i < len(s) && (webDOMParseIsSpace(s[i]) || s[i] == '/' && (i+1 >= len(s) || s[i+1] != '>')) {
			i++
		}
		if i >= len(s) {
			t.pos = len(s)
			return webDOMParseToken{}, false
		}
		if s[i] == '>' {
			t.pos = i + 1
			return tok, true
		}
		if s[i] == '/' { // "/>"
			tok.selfClosing = true
			t.pos = i + 2
			return tok, true
		}
		// The name runs to whitespace, '/', '>' or '='; its first character
		// may itself be '=' (`<a =x>` has an attribute named "=x").
		nameStart := i
		i++
		for i < len(s) && !webDOMParseIsSpace(s[i]) && s[i] != '/' && s[i] != '>' && s[i] != '=' {
			i++
		}
		name := s[nameStart:i]
		for i < len(s) && webDOMParseIsSpace(s[i]) {
			i++
		}
		value := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && webDOMParseIsSpace(s[i]) {
				i++
			}
			if i >= len(s) {
				continue // dropped at the top of the loop
			}
			switch q := s[i]; q {
			case '"', '\'':
				k := strings.IndexByte(s[i+1:], q)
				if k < 0 {
					t.pos = len(s)
					return webDOMParseToken{}, false
				}
				value = s[i+1 : i+1+k]
				i += k + 2
			case '>':
				// `a=>`: an empty value; the '>' ends the tag.
			default:
				k := i
				for k < len(s) && !webDOMParseIsSpace(s[k]) && s[k] != '>' {
					k++
				}
				value = s[i:k]
				i = k
			}
		}
		if kind == webDOMParseStartTag {
			seen = tok.addAttr(name, value, seen)
		}
	}
}

// addAttr keeps the first of duplicate attributes and at most
// webDOMParseMaxAttrs of them; only kept values are entity-decoded. The
// duplicate check scans the kept names until there are webDOMParseAttrSetAt
// of them, then looks names up in seen, the set of them that addAttr creates
// at that point and returns: repeats do not count toward the cap, so
// `<p a0 … a254` followed by " a254" 800 000 times would otherwise compare
// 255 names per repeat.
func (tok *webDOMParseToken) addAttr(name, value string, seen map[string]struct{}) map[string]struct{} {
	if len(tok.attrs) >= 2*webDOMParseMaxAttrs {
		return seen
	}
	name = webDOMParseLower(name)
	if seen != nil {
		if _, dup := seen[name]; dup {
			return seen
		}
		seen[name] = struct{}{}
	} else if webDOMParseAttrIndex(tok.attrs, name) >= 0 {
		return seen
	}
	tok.attrs = append(tok.attrs, name, webDOMParseAttrValue(value))
	if seen == nil && len(tok.attrs) >= 2*webDOMParseAttrSetAt {
		seen = make(map[string]struct{}, 2*webDOMParseAttrSetAt)
		for i := 0; i+1 < len(tok.attrs); i += 2 {
			seen[tok.attrs[i]] = struct{}{}
		}
	}
	return seen
}

// comment reads a comment whose data starts at i (just after "<!--").
func (t *webDOMParseTokenizer) comment(i int) webDOMParseToken {
	s := t.src
	tok := webDOMParseToken{kind: webDOMParseCommentToken}
	switch {
	case strings.HasPrefix(s[i:], ">"): // "<!-->"
		t.pos = i + 1
		return tok
	case strings.HasPrefix(s[i:], "->"): // "<!--->"
		t.pos = i + 2
		return tok
	}
	for j := i; ; {
		k := strings.Index(s[j:], "--")
		if k < 0 {
			// Unterminated: the comment runs to the end of the input, minus
			// a "-" or "--" that had started its end.
			data := s[i:]
			if strings.HasSuffix(data, "--") {
				data = data[:len(data)-2]
			} else {
				data = strings.TrimSuffix(data, "-")
			}
			t.pos = len(s)
			tok.data = data
			return tok
		}
		k += j
		if strings.HasPrefix(s[k+2:], ">") {
			t.pos = k + 3
			tok.data = s[i:k]
			return tok
		}
		if strings.HasPrefix(s[k+2:], "!>") {
			t.pos = k + 4
			tok.data = s[i:k]
			return tok
		}
		j = k + 1
	}
}

// bogus reads a bogus comment ("<?xml …>", "<!x>", "</ x>") whose data starts
// at i and runs to the next '>'.
func (t *webDOMParseTokenizer) bogus(i int) webDOMParseToken {
	s := t.src
	tok := webDOMParseToken{kind: webDOMParseCommentToken, data: s[i:]}
	t.pos = len(s)
	if k := strings.IndexByte(s[i:], '>'); k >= 0 {
		tok.data = s[i : i+k]
		t.pos = i + k + 1
	}
	return tok
}

// doctype reads `<!doctype name public "…" "…">` from i (just after the
// keyword) to the next '>', which ends it even inside a quoted id.
func (t *webDOMParseTokenizer) doctype(i int) webDOMParseToken {
	s := t.src
	body := s[i:]
	t.pos = len(s)
	if k := strings.IndexByte(body, '>'); k >= 0 {
		body = body[:k]
		t.pos = i + k + 1
	}
	j := webDOMParseLeadingSpace(body)
	n := j
	for n < len(body) && !webDOMParseIsSpace(body[n]) {
		n++
	}
	name := webDOMParseLower(body[j:n])
	rest := body[n:]
	rest = rest[webDOMParseLeadingSpace(rest):]
	var public, system string
	switch {
	case len(rest) >= 6 && webDOMParseEqualFold(rest[:6], "public"):
		public, rest = webDOMParseQuoted(rest[6:])
		system, _ = webDOMParseQuoted(rest)
	case len(rest) >= 6 && webDOMParseEqualFold(rest[:6], "system"):
		system, _ = webDOMParseQuoted(rest[6:])
	}
	return webDOMParseToken{kind: webDOMParseDoctypeToken, name: name, attrs: []string{public, system}}
}

// webDOMParseQuoted reads one quoted doctype id after optional whitespace and
// returns it with the rest of s.
func webDOMParseQuoted(s string) (string, string) {
	s = s[webDOMParseLeadingSpace(s):]
	if s == "" || s[0] != '"' && s[0] != '\'' {
		return "", s
	}
	k := strings.IndexByte(s[1:], s[0])
	if k < 0 {
		return s[1:], ""
	}
	return s[1 : 1+k], s[2+k:]
}

// rawText reads the content of a raw text (script, style …) or RCDATA (title,
// textarea) element up to its end tag, which it consumes too. A missing end
// tag makes the rest of the input the content.
func (t *webDOMParseTokenizer) rawText(name string, kind uint8) string {
	s := t.src
	start := t.pos
	var end int
	if kind == webDOMParseScriptData {
		end = webDOMParseScriptEnd(s, start)
	} else {
		end = webDOMParseRawEnd(s, start, name)
	}
	t.pos = end
	if end < len(s) {
		t.tag(end+2, webDOMParseEndTag)
	}
	text := s[start:end]
	if kind == webDOMParseRCDATA {
		text = webDOMParseText(text)
	}
	return text
}

// rest returns the unread input (the content of <plaintext>).
func (t *webDOMParseTokenizer) rest() string {
	s := t.src[t.pos:]
	t.pos = len(t.src)
	return s
}

// webDOMParseRawEnd finds the "</name" that ends raw text starting at i.
func webDOMParseRawEnd(s string, i int, name string) int {
	for i < len(s) {
		k := strings.Index(s[i:], "</")
		if k < 0 {
			break
		}
		i += k
		if webDOMParseEndTagAt(s, i, name) {
			return i
		}
		i += 2
	}
	return len(s)
}

// webDOMParseScriptEnd finds the </script> that ends script data starting at
// i. Old pages wrap scripts in <!-- … --> and document.write
// "<script>…</script>" from inside them; the tokenizer's escape states keep
// such a nested </script> from ending the element early, which would spill
// the rest of the code into the page as text.
func webDOMParseScriptEnd(s string, i int) int {
	escaped, double := false, false
	for i < len(s) {
		var k int
		if escaped {
			k = strings.IndexAny(s[i:], "<-")
		} else {
			k = strings.IndexByte(s[i:], '<')
		}
		if k < 0 {
			break
		}
		i += k
		switch {
		case s[i] == '<' && webDOMParseEndTagAt(s, i, "script"):
			if !double {
				return i
			}
			double = false
			i += len("</script")
		case s[i] == '<' && !escaped && strings.HasPrefix(s[i:], "<!--"):
			escaped = true
			i += 2 // the "--" may already be the start of "-->"
		case s[i] == '<' && escaped && !double && webDOMParseStartTagAt(s, i, "script"):
			double = true
			i += len("<script")
		case s[i] == '-' && escaped && strings.HasPrefix(s[i:], "-->"):
			escaped, double = false, false
			i += len("-->")
		default:
			i++
		}
	}
	return len(s)
}

// webDOMParseEndTagAt reports whether s[i:] is "</name" in any case followed
// by whitespace, '/' or '>': the only end tag that closes raw text.
func webDOMParseEndTagAt(s string, i int, name string) bool {
	e := i + 2 + len(name)
	return e < len(s) && strings.HasPrefix(s[i:], "</") && webDOMParseEqualFold(s[i+2:e], name) && webDOMParseTagEnd(s[e])
}

// webDOMParseStartTagAt is webDOMParseEndTagAt for "<name".
func webDOMParseStartTagAt(s string, i int, name string) bool {
	e := i + 1 + len(name)
	return e < len(s) && s[i] == '<' && webDOMParseEqualFold(s[i+1:e], name) && webDOMParseTagEnd(s[e])
}

// webDOMParseText decodes the character references in text and RCDATA. Named
// references go through html.UnescapeString, numeric ones through
// webDOMParseCharRef: UnescapeString differs from browsers there ("&#x;"
// becomes U+FFFD instead of staying literal, "&#9x" stays literal instead of
// a tab and "x", "&#4294967361;" overflows to "A" instead of U+FFFD). A named
// reference never contains '#', so cutting the text at each "&#" never
// splits one.
func webDOMParseText(s string) string {
	i := strings.Index(s, "&#")
	if i < 0 {
		return html.UnescapeString(s)
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i >= 0 {
		sb.WriteString(html.UnescapeString(s[:i]))
		s = s[i:]
		dec, n := webDOMParseCharRef(s)
		if n == 0 {
			dec, n = "&#", 2 // no digits: the "&#" is text
		}
		sb.WriteString(dec)
		s = s[n:]
		i = strings.Index(s, "&#")
	}
	sb.WriteString(html.UnescapeString(s))
	return sb.String()
}

// webDOMParseAttrValue decodes the character references in an attribute
// value. Unlike text, a named reference without ';' is decoded only for the
// old ones pages really write bare (&amp &lt &gt &quot &nbsp &copy &reg, and
// the upper-case &AMP &LT &GT &QUOT &COPY &REG the entity table lists too),
// and only when no '=' or alphanumeric follows, so URLs like "?a=1&copy=2" or
// "&not=" survive intact.
func webDOMParseAttrValue(s string) string {
	i := strings.IndexByte(s, '&')
	if i < 0 {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i >= 0 {
		sb.WriteString(s[:i])
		s = s[i:]
		dec, n := webDOMParseCharRef(s)
		if n == 0 {
			dec, n = "&", 1
		}
		sb.WriteString(dec)
		s = s[n:]
		i = strings.IndexByte(s, '&')
	}
	sb.WriteString(s)
	return sb.String()
}

// webDOMParseCharRef decodes the reference at the start of s (s[0] == '&') in
// an attribute value and returns it with the bytes it used; 0 = not one.
func webDOMParseCharRef(s string) (string, int) {
	if len(s) < 3 {
		return "", 0
	}
	if s[1] == '#' {
		j, base := 2, 10
		if s[2] == 'x' || s[2] == 'X' {
			j, base = 3, 16
		}
		start, v := j, 0
		for ; j < len(s); j++ {
			d := webDOMParseDigit(s[j], base)
			if d < 0 {
				break
			}
			if v <= 0x10FFFF { // stop growing: the value is invalid anyway
				v = v*base + d
			}
		}
		if j == start {
			return "", 0
		}
		if j < len(s) && s[j] == ';' {
			j++
		}
		// html.UnescapeString maps 0, surrogates and values past U+10FFFF
		// to U+FFFD and 0x80–0x9F through Windows-1252, as browsers do.
		return html.UnescapeString("&#" + strconv.Itoa(min(v, 0x110000)) + ";"), j
	}
	j := 1
	for j < len(s) && (webDOMParseIsLetter(s[j]) || '0' <= s[j] && s[j] <= '9') {
		j++
	}
	if j == 1 {
		return "", 0
	}
	if j < len(s) && s[j] == ';' {
		ref := s[:j+1]
		// A whole-name match decodes to one or two code points; anything
		// longer is UnescapeString's legacy prefix match ("&notit;" →
		// "¬it;"), which attribute values do not get.
		if dec := html.UnescapeString(ref); dec != ref && utf8.RuneCountInString(dec) <= 2 {
			return dec, j + 1
		}
		return "", 0
	}
	if j < len(s) && s[j] == '=' {
		return "", 0
	}
	switch s[1:j] {
	case "amp", "AMP":
		return "&", j
	case "lt", "LT":
		return "<", j
	case "gt", "GT":
		return ">", j
	case "quot", "QUOT":
		return "\"", j
	case "nbsp":
		return "\u00a0", j
	case "copy", "COPY":
		return "\u00a9", j
	case "reg", "REG":
		return "\u00ae", j
	}
	return "", 0
}

// webDOMParseDigit is the value of c as a digit in base 10 or 16, or -1.
func webDOMParseDigit(c byte, base int) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case base == 16 && 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case base == 16 && 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// ------------------------------------------------------------- tree builder

// run feeds tokens to the tree builder until the input or the node budget
// runs out.
func (b *webDOMParseBuilder) run() {
	for !b.stopped {
		b.tz.cdataOK = len(b.stack) > 0 && b.stack[len(b.stack)-1].ns != 0
		tok := b.tz.next()
		if tok.kind == webDOMParseEOF {
			return
		}
		if b.skipLF {
			// A newline right after <pre> or <listing> is source formatting.
			b.skipLF = false
			if tok.kind == webDOMParseTextToken {
				tok.data = strings.TrimPrefix(tok.data, "\n")
				if tok.data == "" {
					continue
				}
			}
		}
		b.reprocess = 0
		b.process(&tok)
	}
}

// process hands a token to the document-level insertion mode. Handlers that
// imply or close an element call it again with the same token; reprocess
// bounds that chain, so no combination of tags can make it loop. Once the
// stack walks so far exceed the work budget the parse stops here: one token
// can only add a bounded amount (a few walks of the capped stack per round).
func (b *webDOMParseBuilder) process(tok *webDOMParseToken) {
	if b.work > b.maxWork {
		b.stopped = true
	}
	if b.stopped || b.reprocess >= webDOMParseMaxReprocess {
		return
	}
	b.reprocess++
	switch b.mode {
	case webDOMParseInitial, webDOMParseBeforeHTML:
		b.beforeHTML(tok)
	case webDOMParseBeforeHead:
		b.beforeHead(tok)
	case webDOMParseInHead:
		if b.current() != b.head {
			// A <template> is open in head: its content parses like body
			// content.
			b.inBody(tok)
			return
		}
		b.inHead(tok)
	case webDOMParseAfterHead:
		if b.current() != b.html {
			b.inBody(tok)
			return
		}
		b.afterHead(tok)
	default:
		b.inBody(tok)
	}
}

// beforeHTML handles the start of a document: the doctype, comments that
// belong to the document itself, and the html element (implied if missing).
func (b *webDOMParseBuilder) beforeHTML(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseDoctypeToken:
		if b.mode == webDOMParseInitial {
			if n := b.newNode(webDOMParseDoctypeNode); n != nil {
				n.name, n.attrs = tok.name, tok.attrs
				webDOMParseInsert(b.doc, n, nil)
			}
			b.mode = webDOMParseBeforeHTML
		}
		return
	case webDOMParseCommentToken:
		b.appendComment(b.doc, tok.data)
		return
	case webDOMParseTextToken:
		tok.data = tok.data[webDOMParseLeadingSpace(tok.data):]
		if tok.data == "" {
			return
		}
	case webDOMParseStartTag:
		if tok.name == "html" {
			b.openHTML(tok.attrs)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "head", "body", "html", "br":
		default:
			return
		}
	}
	if b.openHTML(nil) {
		b.process(tok)
	}
}

// openHTML creates the html element and moves on to before head.
func (b *webDOMParseBuilder) openHTML(attrs []string) bool {
	n := b.newNode(webDOMParseElementNode)
	if n == nil {
		return false
	}
	n.name, n.attrs = "html", attrs
	webDOMParseInsert(b.doc, n, nil)
	b.html = n
	b.push(n)
	b.mode = webDOMParseBeforeHead
	return true
}

// beforeHead skips whitespace until <head>, or anything else, opens head.
func (b *webDOMParseBuilder) beforeHead(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		tok.data = tok.data[webDOMParseLeadingSpace(tok.data):]
		if tok.data == "" {
			return
		}
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
		return
	case webDOMParseDoctypeToken:
		return
	case webDOMParseStartTag:
		switch tok.name {
		case "html":
			b.mergeAttrs(b.html, tok.attrs)
			return
		case "head":
			b.openHead(tok.attrs)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "head", "body", "html", "br":
		default:
			return
		}
	}
	if b.openHead(nil) {
		b.process(tok)
	}
}

// openHead inserts and pushes head.
func (b *webDOMParseBuilder) openHead(attrs []string) bool {
	n := b.insertElement("head", 0, attrs)
	if n == nil {
		return false
	}
	b.head = n
	b.push(n)
	b.mode = webDOMParseInHead
	return true
}

// inHead keeps whitespace, comments and the head-type elements in head;
// anything else closes it.
func (b *webDOMParseBuilder) inHead(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		ws := webDOMParseLeadingSpace(tok.data)
		b.insertText(tok.data[:ws])
		tok.data = tok.data[ws:]
		if tok.data == "" {
			return
		}
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
		return
	case webDOMParseDoctypeToken:
		return
	case webDOMParseStartTag:
		switch tok.name {
		case "html":
			b.mergeAttrs(b.html, tok.attrs)
			return
		case "head":
			return
		}
		if b.headElement(tok) {
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "head":
			b.pop()
			b.mode = webDOMParseAfterHead
			return
		case "body", "html", "br":
		default:
			return
		}
	}
	b.pop()
	b.mode = webDOMParseAfterHead
	b.process(tok)
}

// afterHead sits between </head> and <body>: whitespace and comments go to
// html, late head-type elements still go into head, anything else opens body.
func (b *webDOMParseBuilder) afterHead(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		ws := webDOMParseLeadingSpace(tok.data)
		b.insertText(tok.data[:ws])
		tok.data = tok.data[ws:]
		if tok.data == "" {
			return
		}
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
		return
	case webDOMParseDoctypeToken:
		return
	case webDOMParseStartTag:
		switch tok.name {
		case "html":
			b.mergeAttrs(b.html, tok.attrs)
			return
		case "body":
			b.openBody(tok.attrs)
			return
		case "head", "frame":
			return
		case "frameset":
			// Framesets are not supported: <frameset> opens an implied body
			// (and is dropped there), so the <noframes> content that follows
			// is what the reader shows.
		case "base", "basefont", "bgsound", "link", "meta", "noframes", "script", "style", "template", "title":
			if b.head == nil {
				break
			}
			// head goes back on the stack just for this element; a
			// template stays open above html, so its content still parses
			// before body.
			b.push(b.head)
			b.headElement(tok)
			b.removeNode(b.head)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "body", "html", "br":
		default:
			return
		}
	}
	if b.openBody(nil) {
		b.process(tok)
	}
}

// openBody inserts and pushes body.
func (b *webDOMParseBuilder) openBody(attrs []string) bool {
	n := b.insertElement("body", 0, attrs)
	if n == nil {
		return false
	}
	b.body = n
	b.push(n)
	b.mode = webDOMParseInBody
	return true
}

// finishDocument creates whatever of html, head and body the input never
// implied (an empty document, or one that ended in head).
func (b *webDOMParseBuilder) finishDocument() {
	if b.html == nil {
		n := b.newNode(webDOMParseElementNode)
		if n == nil {
			return
		}
		n.name = "html"
		webDOMParseInsert(b.doc, n, nil)
		b.html = n
	}
	for _, p := range []**webDOMParseNode{&b.head, &b.body} {
		if *p != nil {
			continue
		}
		n := b.newNode(webDOMParseElementNode)
		if n == nil {
			return
		}
		n.name = "head"
		if p == &b.body {
			n.name = "body"
		}
		webDOMParseInsert(b.html, n, nil)
		*p = n
	}
}

// inBody is the entry for everything from <body> on, and for template
// content: foreign content first, then the after-body comment rules, then
// the body or table mode the open elements put the parser in.
func (b *webDOMParseBuilder) inBody(tok *webDOMParseToken) {
	if b.foreignToken(tok) {
		b.foreignContent(tok)
		return
	}
	if b.afterBody {
		switch {
		case tok.kind == webDOMParseCommentToken:
			parent := b.html
			if b.afterHTML || parent == nil {
				parent = b.doc
			}
			b.appendComment(parent, tok.data)
			return
		case tok.kind == webDOMParseDoctypeToken:
			return
		case tok.kind == webDOMParseEndTag && tok.name == "html":
			b.afterHTML = true
			return
		case tok.kind == webDOMParseTextToken && webDOMParseLeadingSpace(tok.data) == len(tok.data),
			tok.kind == webDOMParseStartTag && tok.name == "html":
			// Whitespace after </body> still lands in body, as in browsers.
		default:
			b.afterBody, b.afterHTML = false, false
		}
	}
	b.htmlContent(tok)
}

// htmlContent dispatches on the body mode of the open elements.
func (b *webDOMParseBuilder) htmlContent(tok *webDOMParseToken) {
	mode := webDOMParseModeBody
	if len(b.modes) > 0 {
		mode = b.modes[len(b.modes)-1]
	}
	switch mode {
	case webDOMParseModeTable:
		b.inTable(tok)
	case webDOMParseModeTableBody:
		b.inTableBody(tok)
	case webDOMParseModeRow:
		b.inRow(tok)
	case webDOMParseModeCell:
		b.inCell(tok)
	case webDOMParseModeCaption:
		b.inCaption(tok)
	case webDOMParseModeColgroup:
		b.inColgroup(tok)
	case webDOMParseModeTemplate:
		b.inTemplate(tok)
	default:
		b.bodyRules(tok)
	}
}

// bodyRules is the "in body" insertion mode.
func (b *webDOMParseBuilder) bodyRules(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		b.insertText(tok.data)
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
	case webDOMParseStartTag:
		b.bodyStart(tok)
	case webDOMParseEndTag:
		b.bodyEnd(tok)
	}
}

// bodyStart handles a start tag in body.
func (b *webDOMParseBuilder) bodyStart(tok *webDOMParseToken) {
	switch name := tok.name; name {
	case "html":
		b.mergeAttrs(b.html, tok.attrs)
	case "body":
		b.mergeAttrs(b.body, tok.attrs)
	case "head", "frameset", "frame", "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
		// Document structure again, or table parts outside a table: dropped.
	case "address", "article", "aside", "blockquote", "center", "details", "dialog", "dir", "div", "dl",
		"fieldset", "figcaption", "figure", "footer", "header", "hgroup", "main", "menu", "nav", "ol", "p",
		"search", "section", "summary", "table", "ul":
		b.closeP()
		b.insertPush(tok)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		b.closeP()
		if webDOMParseIsHeading(b.current()) {
			b.pop()
		}
		b.insertPush(tok)
	case "pre", "listing":
		b.closeP()
		if b.insertPush(tok) != nil {
			b.skipLF = true
		}
	case "form":
		tmpl := b.templateOpen()
		if b.form != nil && !tmpl {
			return
		}
		b.closeP()
		if n := b.insertPush(tok); n != nil && !tmpl {
			b.form = n
		}
	case "li", "dd", "dt":
		b.closeListItem(name)
		b.closeP()
		b.insertPush(tok)
	case "plaintext":
		b.closeP()
		if n := b.insertPush(tok); n != nil {
			b.appendText(n, b.tz.rest())
		}
	case "button":
		if i := b.inScope("button", webDOMParseScopeDefault); i > 0 {
			b.popTo(i)
		}
		b.insertPush(tok)
	case "a", "nobr":
		// No adoption agency: a second <a> simply ends the open one, unless
		// a table, cell or button lies between them.
		if i := b.inScope(name, webDOMParseScopeButton); i > 0 {
			b.popTo(i)
		}
		b.insertPush(tok)
	case "area", "br", "embed", "img", "keygen", "wbr", "input", "param", "source", "track":
		b.insertElement(name, 0, tok.attrs)
	case "hr":
		b.closeP()
		b.insertElement(name, 0, tok.attrs)
	case "image":
		b.insertElement("img", 0, tok.attrs)
	case "textarea":
		b.insertRaw(tok, webDOMParseRCDATA)
	case "xmp":
		b.closeP()
		b.insertRaw(tok, webDOMParseRawText)
	case "iframe", "noembed":
		b.insertRaw(tok, webDOMParseRawText)
	case "select":
		// A <select> inside a select closes it and is otherwise dropped.
		if i := b.inScope("select", webDOMParseScopeDefault); i > 0 {
			b.popTo(i)
			return
		}
		b.insertPush(tok)
	case "option":
		if webDOMParseIsHTML(b.current(), "option") {
			b.pop()
		}
		b.insertPush(tok)
	case "optgroup":
		if webDOMParseIsHTML(b.current(), "option") {
			b.pop()
		}
		if webDOMParseIsHTML(b.current(), "optgroup") {
			b.pop()
		}
		b.insertPush(tok)
	case "rb", "rp", "rt", "rtc":
		// An open annotation ends at the next one only inside a <ruby>:
		// without one in scope, browsers nest them like any element.
		cur := b.current()
		closes := cur.ns == 0 && (cur.name == "rb" || cur.name == "rp" || cur.name == "rt" || cur.name == "rtc" && (name == "rb" || name == "rtc"))
		if closes && b.inScope("ruby", webDOMParseScopeDefault) > 0 {
			b.pop()
		}
		b.insertPush(tok)
	case "math":
		b.insertForeign(tok, 2)
	case "svg":
		b.insertForeign(tok, 1)
	default:
		if !b.headElement(tok) {
			b.insertPush(tok)
		}
	}
}

// bodyEnd handles an end tag in body.
func (b *webDOMParseBuilder) bodyEnd(tok *webDOMParseToken) {
	switch name := tok.name; name {
	case "body", "html":
		// Not popped: later content still goes into body, but comments now
		// go after it.
		if b.body != nil && b.inScopeNode(b.body) {
			b.afterBody = true
			b.afterHTML = name == "html"
		}
	case "head":
	case "p":
		if i := b.inScope("p", webDOMParseScopeButton); i > 0 {
			b.popTo(i)
		}
	case "li":
		if i := b.inScope("li", webDOMParseScopeList); i > 0 {
			b.popTo(i)
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		// Any open heading closes, whatever its level.
		for i := len(b.stack) - 1; i > 0; i-- {
			b.work++
			n := b.stack[i]
			if webDOMParseIsHeading(n) {
				b.popTo(i)
				return
			}
			if webDOMParseScopeBoundary(n, webDOMParseScopeDefault) {
				return
			}
		}
	case "form":
		b.closeForm()
	case "br":
		b.insertElement("br", 0, nil)
	case "template":
		for i := len(b.stack) - 1; i > 0; i-- {
			b.work++
			if webDOMParseIsHTML(b.stack[i], "template") {
				b.popTo(i)
				return
			}
		}
	case "address", "applet", "article", "aside", "blockquote", "button", "center", "dd", "details", "dialog",
		"dir", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "header", "hgroup", "listing",
		"main", "marquee", "menu", "nav", "object", "ol", "optgroup", "option", "pre", "search", "section",
		"select", "summary", "ul", "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike",
		"strong", "tt", "u":
		// Blocks close the nearest open element of their name in scope,
		// with everything opened inside it. Formatting elements end the
		// same way: without the adoption agency that is the closest cheap
		// match for what browsers build.
		if i := b.inScope(name, webDOMParseScopeDefault); i > 0 {
			b.popTo(i)
		}
	default:
		// Any other end tag closes the nearest open element of that name,
		// unless a special element (div, li, table …) comes first: a stray
		// </span> must not close the block it sits in.
		for i := len(b.stack) - 1; i > 0; i-- {
			b.work++
			n := b.stack[i]
			if n.ns == 0 && n.name == name {
				b.popTo(i)
				return
			}
			if webDOMParseIsSpecial(n) {
				return
			}
		}
	}
}

// closeP implies </p> before a block start tag.
func (b *webDOMParseBuilder) closeP() {
	if i := b.inScope("p", webDOMParseScopeButton); i > 0 {
		b.popTo(i)
	}
}

// closeListItem implies </li> before <li> (and </dd>, </dt> before <dd>,
// <dt>) unless a special element other than address, div and p is in the
// way — the list a nested item belongs to.
func (b *webDOMParseBuilder) closeListItem(name string) {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		n := b.stack[i]
		if n.ns == 0 && (n.name == name || name != "li" && (n.name == "dd" || n.name == "dt")) {
			b.popTo(i)
			return
		}
		if webDOMParseIsSpecial(n) && !(n.ns == 0 && (n.name == "address" || n.name == "div" || n.name == "p")) {
			return
		}
	}
}

// closeForm handles </form>: outside templates only the form element itself
// leaves the stack, so elements opened inside it (a form wrapped around half
// a table is common) stay open.
func (b *webDOMParseBuilder) closeForm() {
	if b.templateOpen() {
		if i := b.inScope("form", webDOMParseScopeDefault); i > 0 {
			b.popTo(i)
		}
		return
	}
	form := b.form
	b.form = nil
	if form == nil {
		return
	}
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		n := b.stack[i]
		if n == form {
			b.removeAt(i)
			return
		}
		if webDOMParseScopeBoundary(n, webDOMParseScopeDefault) {
			return
		}
	}
}

// inTable is the "in table" mode: table parts are created (with implied
// tbody and colgroup), anything else is foster-parented.
func (b *webDOMParseBuilder) inTable(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		if webDOMParseTableStructure(b.current()) && webDOMParseLeadingSpace(tok.data) == len(tok.data) {
			b.insertText(tok.data)
			return
		}
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
		return
	case webDOMParseDoctypeToken:
		return
	case webDOMParseStartTag:
		switch tok.name {
		case "caption", "colgroup", "tbody", "tfoot", "thead":
			b.clearTo(webDOMParseModeTable)
			b.insertPush(tok)
			return
		case "col":
			b.clearTo(webDOMParseModeTable)
			if b.insertImplied("colgroup") {
				b.process(tok)
			}
			return
		case "td", "th", "tr":
			b.clearTo(webDOMParseModeTable)
			if b.insertImplied("tbody") {
				b.process(tok)
			}
			return
		case "table":
			// A table straight inside a table ends the first one.
			if i := b.inScope("table", webDOMParseScopeTable); i > 0 {
				b.popTo(i)
				b.process(tok)
			}
			return
		case "style", "script", "template":
			b.headElement(tok)
			return
		case "input":
			if webDOMParseEqualFold(webDOMParseAttr(tok.attrs, "type"), "hidden") {
				b.insertElement("input", 0, tok.attrs)
				return
			}
		case "form":
			if b.form == nil && !b.templateOpen() {
				b.form = b.insertElement("form", 0, tok.attrs)
			}
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "table":
			if i := b.inScope("table", webDOMParseScopeTable); i > 0 {
				b.popTo(i)
			}
			return
		case "body", "caption", "col", "colgroup", "html", "tbody", "td", "tfoot", "th", "thead", "tr":
			return
		case "template":
			b.bodyEnd(tok)
			return
		}
	}
	// Anything else parses as body content, and while a table part is the
	// current node it is moved out in front of the table (foster parenting).
	b.foster = true
	b.bodyRules(tok)
	b.foster = false
}

// inTableBody is "in table body" (an open tbody, thead or tfoot).
func (b *webDOMParseBuilder) inTableBody(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseStartTag:
		switch tok.name {
		case "tr":
			b.clearTo(webDOMParseModeTableBody)
			b.insertPush(tok)
			return
		case "td", "th":
			b.clearTo(webDOMParseModeTableBody)
			if b.insertImplied("tr") {
				b.process(tok)
			}
			return
		case "caption", "col", "colgroup", "tbody", "tfoot", "thead":
			b.closeTableBody(tok)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "tbody", "tfoot", "thead":
			if b.inScope(tok.name, webDOMParseScopeTable) > 0 {
				b.clearTo(webDOMParseModeTableBody)
				b.pop()
			}
			return
		case "table":
			b.closeTableBody(tok)
			return
		case "body", "caption", "col", "colgroup", "html", "td", "th", "tr":
			return
		}
	}
	b.inTable(tok)
}

// closeTableBody ends the open table section and hands tok back.
func (b *webDOMParseBuilder) closeTableBody(tok *webDOMParseToken) {
	if b.inScope("tbody", webDOMParseScopeTable) < 0 && b.inScope("thead", webDOMParseScopeTable) < 0 && b.inScope("tfoot", webDOMParseScopeTable) < 0 {
		return
	}
	b.clearTo(webDOMParseModeTableBody)
	b.pop()
	b.process(tok)
}

// inRow is "in row" (an open tr).
func (b *webDOMParseBuilder) inRow(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseStartTag:
		switch tok.name {
		case "td", "th":
			b.clearTo(webDOMParseModeRow)
			b.insertPush(tok)
			return
		case "caption", "col", "colgroup", "tbody", "tfoot", "thead", "tr":
			b.closeRow(tok)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "tr":
			if b.inScope("tr", webDOMParseScopeTable) > 0 {
				b.clearTo(webDOMParseModeRow)
				b.pop()
			}
			return
		case "table":
			b.closeRow(tok)
			return
		case "tbody", "tfoot", "thead":
			if b.inScope(tok.name, webDOMParseScopeTable) > 0 {
				b.closeRow(tok)
			}
			return
		case "body", "caption", "col", "colgroup", "html", "td", "th":
			return
		}
	}
	b.inTable(tok)
}

// closeRow ends the open row and hands tok back.
func (b *webDOMParseBuilder) closeRow(tok *webDOMParseToken) {
	if b.inScope("tr", webDOMParseScopeTable) < 0 {
		return
	}
	b.clearTo(webDOMParseModeRow)
	b.pop()
	b.process(tok)
}

// inCell is "in cell": body content, until a table part ends the cell.
func (b *webDOMParseBuilder) inCell(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseStartTag:
		switch tok.name {
		case "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
			if b.closeCell() {
				b.process(tok)
			}
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "td", "th":
			if i := b.inScope(tok.name, webDOMParseScopeTable); i > 0 {
				b.popTo(i)
			}
			return
		case "body", "caption", "col", "colgroup", "html":
			return
		case "table", "tbody", "tfoot", "thead", "tr":
			if b.inScope(tok.name, webDOMParseScopeTable) > 0 && b.closeCell() {
				b.process(tok)
			}
			return
		}
	}
	b.bodyRules(tok)
}

// closeCell pops up to and including the nearest open td or th in table
// scope; false when there is none.
func (b *webDOMParseBuilder) closeCell() bool {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		n := b.stack[i]
		if n.ns != 0 {
			continue
		}
		switch n.name {
		case "td", "th":
			b.popTo(i)
			return true
		case "html", "table", "template":
			return false
		}
	}
	return false
}

// inCaption is "in caption": body content, until a table part ends it.
func (b *webDOMParseBuilder) inCaption(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseStartTag:
		switch tok.name {
		case "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
			if i := b.inScope("caption", webDOMParseScopeTable); i > 0 {
				b.popTo(i)
				b.process(tok)
			}
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "caption":
			if i := b.inScope("caption", webDOMParseScopeTable); i > 0 {
				b.popTo(i)
			}
			return
		case "table":
			if i := b.inScope("caption", webDOMParseScopeTable); i > 0 {
				b.popTo(i)
				b.process(tok)
			}
			return
		case "body", "col", "colgroup", "html", "tbody", "td", "tfoot", "th", "thead", "tr":
			return
		}
	}
	b.bodyRules(tok)
}

// inColgroup is "in column group": only col (and whitespace) belongs here;
// anything else closes the colgroup. As a fragment context nothing closes it,
// so anything else is dropped.
func (b *webDOMParseBuilder) inColgroup(tok *webDOMParseToken) {
	cur := b.current()
	if !webDOMParseIsHTML(cur, "colgroup") {
		b.bodyRules(tok)
		return
	}
	switch tok.kind {
	case webDOMParseTextToken:
		ws := webDOMParseLeadingSpace(tok.data)
		b.insertText(tok.data[:ws])
		tok.data = tok.data[ws:]
		if tok.data == "" {
			return
		}
	case webDOMParseCommentToken:
		b.appendComment(cur, tok.data)
		return
	case webDOMParseDoctypeToken:
		return
	case webDOMParseStartTag:
		switch tok.name {
		case "col":
			b.insertElement("col", 0, tok.attrs)
			return
		case "template":
			b.insertPush(tok)
			return
		}
	case webDOMParseEndTag:
		switch tok.name {
		case "colgroup":
			b.pop()
			return
		case "col":
			return
		case "template":
			b.bodyEnd(tok)
			return
		}
	}
	if len(b.stack) == 1 {
		return
	}
	b.pop()
	b.process(tok)
}

// inTemplate puts table parts straight into a template (its content may be
// a bare <tr>); everything else is body content.
func (b *webDOMParseBuilder) inTemplate(tok *webDOMParseToken) {
	if tok.kind == webDOMParseStartTag && webDOMParseIsHTML(b.current(), "template") {
		switch tok.name {
		case "caption", "colgroup", "tbody", "tfoot", "thead", "tr", "td", "th":
			b.insertPush(tok)
			return
		case "col":
			b.insertElement("col", 0, tok.attrs)
			return
		}
	}
	b.bodyRules(tok)
}

// foreignToken reports whether tok goes to the foreign-content rules: the
// current node is SVG or MathML and not an integration point for this kind
// of token.
func (b *webDOMParseBuilder) foreignToken(tok *webDOMParseToken) bool {
	if len(b.stack) == 0 {
		return false
	}
	cur := b.stack[len(b.stack)-1]
	if cur.ns == 0 {
		return false
	}
	switch tok.kind {
	case webDOMParseTextToken:
		return !webDOMParseTextIntegration(cur) && !webDOMParseHTMLIntegration(cur)
	case webDOMParseStartTag:
		if webDOMParseTextIntegration(cur) && tok.name != "mglyph" && tok.name != "malignmark" {
			return false
		}
		if cur.ns == 2 && cur.name == "annotation-xml" && tok.name == "svg" {
			return false
		}
		return !webDOMParseHTMLIntegration(cur)
	}
	return true
}

// foreignContent is "in foreign content": SVG and MathML elements nest by
// their own rules (self-closing tags, CDATA) until an HTML-only tag breaks
// back out.
func (b *webDOMParseBuilder) foreignContent(tok *webDOMParseToken) {
	switch tok.kind {
	case webDOMParseTextToken:
		b.insertText(tok.data)
	case webDOMParseCommentToken:
		b.appendComment(b.current(), tok.data)
	case webDOMParseStartTag:
		if webDOMParseBreakout(tok) {
			b.popForeign()
			b.htmlContent(tok)
			return
		}
		b.insertForeign(tok, b.current().ns)
	case webDOMParseEndTag:
		if tok.name == "br" || tok.name == "p" {
			b.popForeign()
			b.htmlContent(tok)
			return
		}
		// The nearest foreign element of that name (in any case) closes;
		// reaching an HTML element first hands the tag to the HTML rules.
		for i := len(b.stack) - 1; i > 0; i-- {
			b.work++
			n := b.stack[i]
			if n.ns == 0 {
				b.htmlContent(tok)
				return
			}
			if webDOMParseEqualFold(n.name, tok.name) {
				b.popTo(i)
				return
			}
		}
	}
}

// popForeign pops foreign elements until an HTML element or an integration
// point is current.
func (b *webDOMParseBuilder) popForeign() {
	for len(b.stack) > 1 {
		b.work++
		cur := b.stack[len(b.stack)-1]
		if cur.ns == 0 || webDOMParseTextIntegration(cur) || webDOMParseHTMLIntegration(cur) {
			return
		}
		b.pop()
	}
}

// insertForeign inserts an SVG (ns 1) or MathML (ns 2) element with the
// spec's case adjustments; a self-closing one is not pushed.
func (b *webDOMParseBuilder) insertForeign(tok *webDOMParseToken, ns uint8) {
	name := tok.name
	if ns == 1 {
		name = webDOMParseSVGName(name)
	}
	for i := 0; i+1 < len(tok.attrs); i += 2 {
		switch {
		case ns == 1:
			if v, ok := webDOMParseSVGAttrNames()[tok.attrs[i]]; ok {
				tok.attrs[i] = v
			}
		case tok.attrs[i] == "definitionurl":
			tok.attrs[i] = "definitionURL"
		}
	}
	n := b.insertElement(name, ns, tok.attrs)
	if n == nil {
		return
	}
	if ns == 2 && name == "annotation-xml" {
		// Worked out once: the integration check runs for every token while
		// the element is current, and scanning up to 256 attributes per
		// token (creating no node) would be a slow path for hostile pages.
		enc := webDOMParseAttr(n.attrs, "encoding")
		n.htmlIP = webDOMParseEqualFold(enc, "text/html") || webDOMParseEqualFold(enc, "application/xhtml+xml")
	}
	if !tok.selfClosing {
		b.push(n)
	}
}

// headElement inserts the head-type elements (wherever they appear: in body
// they stay where they are) and reports false for any other tag.
func (b *webDOMParseBuilder) headElement(tok *webDOMParseToken) bool {
	switch tok.name {
	case "base", "basefont", "bgsound", "link", "meta":
		b.insertElement(tok.name, 0, tok.attrs)
	case "title":
		b.insertRaw(tok, webDOMParseRCDATA)
	case "noscript", "noframes", "style":
		// noscript is raw text because scripting is on.
		b.insertRaw(tok, webDOMParseRawText)
	case "script":
		b.insertRaw(tok, webDOMParseScriptData)
	case "template":
		b.insertPush(tok)
	default:
		return false
	}
	return true
}

// insertRaw inserts a raw text or RCDATA element (script, style, title,
// textarea …) together with its content, read straight from the tokenizer up
// to the matching end tag. Such elements are never pushed.
func (b *webDOMParseBuilder) insertRaw(tok *webDOMParseToken, kind uint8) {
	n := b.insertElement(tok.name, 0, tok.attrs)
	text := b.tz.rawText(tok.name, kind)
	if n == nil {
		return
	}
	if tok.name == "textarea" {
		text = strings.TrimPrefix(text, "\n")
	}
	b.appendText(n, text)
}

// insertPush inserts an HTML element for tok and pushes it.
func (b *webDOMParseBuilder) insertPush(tok *webDOMParseToken) *webDOMParseNode {
	n := b.insertElement(tok.name, 0, tok.attrs)
	if n != nil {
		b.push(n)
	}
	return n
}

// insertImplied inserts and pushes an element the markup left out (tbody,
// tr, colgroup). At the depth cap it does nothing, so the caller drops the
// token instead of implying the same element again.
func (b *webDOMParseBuilder) insertImplied(name string) bool {
	if len(b.stack) >= webDOMParseMaxDepth {
		return false
	}
	n := b.insertElement(name, 0, nil)
	if n == nil {
		return false
	}
	b.push(n)
	return true
}

// insertElement creates an element and inserts it at the insertion location.
func (b *webDOMParseBuilder) insertElement(name string, ns uint8, attrs []string) *webDOMParseNode {
	n := b.newNode(webDOMParseElementNode)
	if n == nil {
		return nil
	}
	n.name, n.ns, n.attrs = name, ns, attrs
	parent, before := b.location()
	webDOMParseInsert(parent, n, before)
	return n
}

// insertText adds text at the insertion location, merged into a text node
// right before it.
func (b *webDOMParseBuilder) insertText(s string) {
	if s == "" {
		return
	}
	parent, before := b.location()
	i := len(parent.children)
	if before != nil {
		if i = webDOMParseChildIndex(parent, before); i < 0 {
			i, before = len(parent.children), nil
		}
	}
	if i > 0 && parent.children[i-1].kind == webDOMParseTextNode {
		parent.children[i-1].data = append(parent.children[i-1].data, s...)
		return
	}
	if n := b.newNode(webDOMParseTextNode); n != nil {
		n.data = []byte(s)
		webDOMParseInsert(parent, n, before)
	}
}

// appendText adds text as the last child of parent (raw text content).
func (b *webDOMParseBuilder) appendText(parent *webDOMParseNode, s string) {
	if s == "" {
		return
	}
	if k := len(parent.children); k > 0 && parent.children[k-1].kind == webDOMParseTextNode {
		parent.children[k-1].data = append(parent.children[k-1].data, s...)
		return
	}
	if n := b.newNode(webDOMParseTextNode); n != nil {
		n.data = []byte(s)
		webDOMParseInsert(parent, n, nil)
	}
}

// appendComment adds a comment as the last child of parent.
func (b *webDOMParseBuilder) appendComment(parent *webDOMParseNode, data string) {
	if n := b.newNode(webDOMParseCommentNode); n != nil {
		n.data = []byte(data)
		webDOMParseInsert(parent, n, nil)
	}
}

// newNode counts a node against maxNodes; nil (and stopped) once the budget
// is spent.
func (b *webDOMParseBuilder) newNode(kind uint8) *webDOMParseNode {
	if b.maxNodes > 0 && b.nodes >= b.maxNodes {
		b.stopped = true
		return nil
	}
	b.nodes++
	return &webDOMParseNode{kind: kind}
}

// location is the appropriate place for inserting a node: the end of the
// current node, or — while foster parenting and a table part is current —
// right before the table in its parent (into a template that is nearer).
func (b *webDOMParseBuilder) location() (parent, before *webDOMParseNode) {
	cur := b.current()
	if !b.foster || len(b.stack) == 0 || !webDOMParseTableStructure(cur) {
		return cur, nil
	}
	for i := len(b.stack) - 1; i >= 0; i-- {
		b.work++
		n := b.stack[i]
		switch {
		case n.ns != 0:
		case n.name == "template":
			return n, nil
		case n.name == "table":
			if i == 0 {
				return n, nil // the fragment context is the table
			}
			if n.parent != nil {
				return n.parent, n
			}
			return b.stack[i-1], nil
		}
	}
	return b.stack[0], nil
}

// webDOMParseInsert links n into parent before before (nil = at the end).
func webDOMParseInsert(parent, n, before *webDOMParseNode) {
	n.parent = parent
	i := -1
	if before != nil {
		i = webDOMParseChildIndex(parent, before)
	}
	if i < 0 {
		parent.children = append(parent.children, n)
		return
	}
	parent.children = append(parent.children, nil)
	copy(parent.children[i+1:], parent.children[i:])
	parent.children[i] = n
}

// webDOMParseChildIndex finds child among parent's children, searching from
// the end where foster parenting inserts.
func webDOMParseChildIndex(parent, child *webDOMParseNode) int {
	for i := len(parent.children) - 1; i >= 0; i-- {
		if parent.children[i] == child {
			return i
		}
	}
	return -1
}

// current is the current node (the document before html exists).
func (b *webDOMParseBuilder) current() *webDOMParseNode {
	if len(b.stack) == 0 {
		return b.doc
	}
	return b.stack[len(b.stack)-1]
}

// push opens n. Past webDOMParseMaxDepth it is left closed: its content then
// goes to the deepest open element instead (the depth cap flattens).
func (b *webDOMParseBuilder) push(n *webDOMParseNode) {
	if len(b.stack) >= webDOMParseMaxDepth {
		return
	}
	b.stack = append(b.stack, n)
	b.modes = append(b.modes, b.modeAt(len(b.stack)-1))
}

// modeAt computes modes[i] from stack[i] and modes[i-1].
func (b *webDOMParseBuilder) modeAt(i int) uint8 {
	m := webDOMParseModeOf(b.stack[i])
	if i == 0 {
		// A cell as the fragment context is plain body content (the spec's
		// "td or th and last is false").
		if m == webDOMParseModeNone || m == webDOMParseModeCell {
			return webDOMParseModeBody
		}
		return m
	}
	if m == webDOMParseModeNone {
		return b.modes[i-1]
	}
	return m
}

// pop closes the current node; the stack bottom (html or the fragment
// context) never closes.
func (b *webDOMParseBuilder) pop() {
	if len(b.stack) > 1 {
		b.stack = b.stack[:len(b.stack)-1]
		b.modes = b.modes[:len(b.modes)-1]
	}
}

// popTo closes stack[i] and everything opened after it.
func (b *webDOMParseBuilder) popTo(i int) {
	i = max(i, 1)
	if i < len(b.stack) {
		b.stack = b.stack[:i]
		b.modes = b.modes[:i]
	}
}

// removeAt takes stack[i] out of the middle of the stack (</form>, the head
// element after an after-head insertion).
func (b *webDOMParseBuilder) removeAt(i int) {
	if i < 1 || i >= len(b.stack) {
		return
	}
	b.stack = append(b.stack[:i], b.stack[i+1:]...)
	b.modes = b.modes[:i]
	for j := i; j < len(b.stack); j++ {
		b.work++
		b.modes = append(b.modes, b.modeAt(j))
	}
}

// removeNode takes n off the stack wherever it is.
func (b *webDOMParseBuilder) removeNode(n *webDOMParseNode) {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		if b.stack[i] == n {
			b.removeAt(i)
			return
		}
	}
}

// clearTo pops back to a table context (ctx webDOMParseModeTable), table
// body context (TableBody) or row context (Row); a template or the stack
// bottom stops it too.
func (b *webDOMParseBuilder) clearTo(ctx uint8) {
	for len(b.stack) > 1 {
		b.work++
		n := b.stack[len(b.stack)-1]
		if n.ns == 0 {
			switch n.name {
			case "template":
				return
			case "table":
				if ctx == webDOMParseModeTable {
					return
				}
			case "tbody", "tfoot", "thead":
				if ctx == webDOMParseModeTableBody {
					return
				}
			case "tr":
				if ctx == webDOMParseModeRow {
					return
				}
			}
		}
		b.pop()
	}
}

// inScope returns the stack index of the nearest open HTML element named
// name, or -1 when a boundary of the scope (or the stack bottom: html or the
// fragment context) comes first.
func (b *webDOMParseBuilder) inScope(name string, scope uint8) int {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		n := b.stack[i]
		if n.ns == 0 && n.name == name {
			return i
		}
		if webDOMParseScopeBoundary(n, scope) {
			return -1
		}
	}
	return -1
}

// inScopeNode is inScope for one particular element.
func (b *webDOMParseBuilder) inScopeNode(target *webDOMParseNode) bool {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		n := b.stack[i]
		if n == target {
			return true
		}
		if webDOMParseScopeBoundary(n, webDOMParseScopeDefault) {
			return false
		}
	}
	return false
}

// templateOpen reports whether a template element is open.
func (b *webDOMParseBuilder) templateOpen() bool {
	for i := len(b.stack) - 1; i > 0; i-- {
		b.work++
		if webDOMParseIsHTML(b.stack[i], "template") {
			return true
		}
	}
	return false
}

// webDOMParseScopeBoundary reports whether n ends a scope search.
func webDOMParseScopeBoundary(n *webDOMParseNode, scope uint8) bool {
	if n.ns != 0 {
		// The SVG and MathML integration points (all special) bound every
		// scope but the table one.
		return scope != webDOMParseScopeTable && webDOMParseIsSpecial(n)
	}
	switch n.name {
	case "html", "table", "template":
		return true
	case "applet", "caption", "td", "th", "marquee", "object":
		return scope != webDOMParseScopeTable
	case "button":
		return scope == webDOMParseScopeButton
	case "ol", "ul":
		return scope == webDOMParseScopeList
	}
	return false
}

// webDOMParseModeOf is the body mode an open element sets, or
// webDOMParseModeNone.
func webDOMParseModeOf(n *webDOMParseNode) uint8 {
	if n.ns != 0 {
		return webDOMParseModeNone
	}
	switch n.name {
	case "table":
		return webDOMParseModeTable
	case "tbody", "tfoot", "thead":
		return webDOMParseModeTableBody
	case "tr":
		return webDOMParseModeRow
	case "td", "th":
		return webDOMParseModeCell
	case "caption":
		return webDOMParseModeCaption
	case "colgroup":
		return webDOMParseModeColgroup
	case "template":
		return webDOMParseModeTemplate
	case "html", "body":
		return webDOMParseModeBody
	}
	return webDOMParseModeNone
}

// webDOMParseIsSpecial reports the spec's "special" elements, which stop the
// generic end-tag search and the implied </li>.
func webDOMParseIsSpecial(n *webDOMParseNode) bool {
	switch n.ns {
	case 1:
		return n.name == "foreignObject" || n.name == "desc" || n.name == "title"
	case 2:
		switch n.name {
		case "mi", "mo", "mn", "ms", "mtext", "annotation-xml":
			return true
		}
		return false
	}
	switch n.name {
	case "address", "applet", "area", "article", "aside", "base", "basefont", "bgsound", "blockquote", "body",
		"br", "button", "caption", "center", "col", "colgroup", "dd", "details", "dir", "div", "dl", "dt",
		"embed", "fieldset", "figcaption", "figure", "footer", "form", "frame", "frameset", "h1", "h2", "h3",
		"h4", "h5", "h6", "head", "header", "hgroup", "hr", "html", "iframe", "img", "input", "keygen", "li",
		"link", "listing", "main", "marquee", "menu", "meta", "nav", "noembed", "noframes", "noscript",
		"object", "ol", "p", "param", "plaintext", "pre", "script", "search", "section", "select", "source",
		"style", "summary", "table", "tbody", "td", "template", "textarea", "tfoot", "th", "thead", "title",
		"tr", "track", "ul", "wbr", "xmp":
		return true
	}
	return false
}

// webDOMParseTextIntegration reports a MathML text integration point (mi,
// mo, mn, ms, mtext), whose text and most start tags are HTML again.
func webDOMParseTextIntegration(n *webDOMParseNode) bool {
	if n.ns != 2 {
		return false
	}
	switch n.name {
	case "mi", "mo", "mn", "ms", "mtext":
		return true
	}
	return false
}

// webDOMParseHTMLIntegration reports an HTML integration point: SVG
// foreignObject, desc and title, and MathML annotation-xml that declares
// HTML content (decided from its encoding attribute by insertForeign).
func webDOMParseHTMLIntegration(n *webDOMParseNode) bool {
	switch n.ns {
	case 1:
		return n.name == "foreignObject" || n.name == "desc" || n.name == "title"
	case 2:
		return n.htmlIP
	}
	return false
}

// webDOMParseBreakout reports the HTML-only start tags that end foreign
// content (font only with color, face or size).
func webDOMParseBreakout(tok *webDOMParseToken) bool {
	switch tok.name {
	case "b", "big", "blockquote", "body", "br", "center", "code", "dd", "div", "dl", "dt", "em", "embed",
		"h1", "h2", "h3", "h4", "h5", "h6", "head", "hr", "i", "img", "li", "listing", "menu", "meta", "nobr",
		"ol", "p", "pre", "ruby", "s", "small", "span", "strike", "strong", "sub", "sup", "table", "tt", "u",
		"ul", "var":
		return true
	case "font":
		for i := 0; i+1 < len(tok.attrs); i += 2 {
			switch tok.attrs[i] {
			case "color", "face", "size":
				return true
			}
		}
	}
	return false
}

// webDOMParseTableStructure reports the table parts text cannot go into
// (table, its sections and rows): content there is foster-parented.
func webDOMParseTableStructure(n *webDOMParseNode) bool {
	if n.ns != 0 {
		return false
	}
	switch n.name {
	case "table", "tbody", "tfoot", "thead", "tr":
		return true
	}
	return false
}

// webDOMParseIsHTML reports whether n is the HTML element name.
func webDOMParseIsHTML(n *webDOMParseNode, name string) bool {
	return n != nil && n.kind == webDOMParseElementNode && n.ns == 0 && n.name == name
}

// webDOMParseIsHeading reports an HTML h1–h6.
func webDOMParseIsHeading(n *webDOMParseNode) bool {
	return n.ns == 0 && len(n.name) == 2 && n.name[0] == 'h' && '1' <= n.name[1] && n.name[1] <= '6'
}

// webDOMParseAttrIndex is the index of name in a flat attribute list, or -1.
func webDOMParseAttrIndex(attrs []string, name string) int {
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == name {
			return i
		}
	}
	return -1
}

// webDOMParseAttr is the value of attribute name ("" when missing).
func webDOMParseAttr(attrs []string, name string) string {
	if i := webDOMParseAttrIndex(attrs, name); i >= 0 {
		return attrs[i+1]
	}
	return ""
}

// mergeAttrs adds the attributes n does not have yet (a second <html> or
// <body> tag). Each lookup scans n's attributes, up to 256 per attribute and
// creating no node, so it counts as work like a stack walk.
func (b *webDOMParseBuilder) mergeAttrs(n *webDOMParseNode, attrs []string) {
	if n == nil {
		return
	}
	for i := 0; i+1 < len(attrs) && len(n.attrs) < 2*webDOMParseMaxAttrs; i += 2 {
		b.work += len(n.attrs) / 2
		if webDOMParseAttrIndex(n.attrs, attrs[i]) < 0 {
			n.attrs = append(n.attrs, attrs[i], attrs[i+1])
		}
	}
}

// webDOMParseSVGName is the case-adjusted SVG element name.
func webDOMParseSVGName(name string) string {
	if v, ok := webDOMParseSVGTagNames()[name]; ok {
		return v
	}
	return name
}

// webDOMParseCaseTable maps the lower-case form of each name to the name.
func webDOMParseCaseTable(names string) map[string]string {
	m := make(map[string]string)
	for _, name := range strings.Fields(names) {
		m[webDOMParseLower(name)] = name
	}
	return m
}

// webDOMParseIsSpace reports HTML whitespace (tab, LF, FF, CR, space).
func webDOMParseIsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// webDOMParseIsLetter reports an ASCII letter.
func webDOMParseIsLetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// webDOMParseOpensMarkup reports whether c after '<' starts a tag, comment,
// doctype or bogus comment; otherwise the '<' is text.
func webDOMParseOpensMarkup(c byte) bool {
	return webDOMParseIsLetter(c) || c == '/' || c == '!' || c == '?'
}

// webDOMParseTagEnd reports the characters that may follow a tag name.
func webDOMParseTagEnd(c byte) bool {
	return webDOMParseIsSpace(c) || c == '/' || c == '>'
}

// webDOMParseLeadingSpace is the length of the whitespace prefix of s.
func webDOMParseLeadingSpace(s string) int {
	i := 0
	for i < len(s) && webDOMParseIsSpace(s[i]) {
		i++
	}
	return i
}

// webDOMParseLower lower-cases ASCII letters only, as the tokenizer does.
func webDOMParseLower(s string) string {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// webDOMParseEqualFold compares ASCII case-insensitively.
func webDOMParseEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------- JSON

// webDOMParseEncode writes nodes as the JSON tree array.
func webDOMParseEncode(nodes []*webDOMParseNode, sizeHint int) string {
	var sb strings.Builder
	sb.Grow(sizeHint + 64)
	sb.WriteByte('[')
	for i, n := range nodes {
		if i > 0 {
			sb.WriteByte(',')
		}
		webDOMParseEncodeNode(&sb, n)
	}
	sb.WriteByte(']')
	return sb.String()
}

// webDOMParseEncodeNode writes one node. The recursion is bounded by the
// tree depth, which the stack cap keeps near webDOMParseMaxDepth.
func webDOMParseEncodeNode(sb *strings.Builder, n *webDOMParseNode) {
	switch n.kind {
	case webDOMParseTextNode:
		webDOMParseWriteString(sb, string(n.data))
	case webDOMParseCommentNode:
		sb.WriteString("[8,")
		webDOMParseWriteString(sb, string(n.data))
		sb.WriteByte(']')
	case webDOMParseDoctypeNode:
		sb.WriteString("[10,")
		webDOMParseWriteString(sb, n.name)
		for i := 0; i < 2; i++ {
			sb.WriteByte(',')
			id := ""
			if i < len(n.attrs) {
				id = n.attrs[i]
			}
			webDOMParseWriteString(sb, id)
		}
		sb.WriteByte(']')
	default:
		sb.WriteByte('[')
		webDOMParseWriteString(sb, n.name)
		sb.WriteByte(',')
		if len(n.attrs) == 0 {
			sb.WriteByte('0')
		} else {
			sb.WriteByte('[')
			for i, a := range n.attrs {
				if i > 0 {
					sb.WriteByte(',')
				}
				webDOMParseWriteString(sb, a)
			}
			sb.WriteByte(']')
		}
		sb.WriteByte(',')
		if len(n.children) == 0 {
			sb.WriteByte('0')
		} else {
			sb.WriteByte('[')
			for i, c := range n.children {
				if i > 0 {
					sb.WriteByte(',')
				}
				webDOMParseEncodeNode(sb, c)
			}
			sb.WriteByte(']')
		}
		if n.ns != 0 {
			sb.WriteByte(',')
			sb.WriteByte('0' + n.ns)
		}
		sb.WriteByte(']')
	}
}

// webDOMParseWriteString writes s as a JSON string: '"', '\\' and control
// characters escaped (as \u00XX), U+2028/U+2029 escaped so the result is
// also a valid JavaScript literal, and invalid UTF-8 replaced by U+FFFD.
func webDOMParseWriteString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			sb.WriteString(s[start:i])
			switch c {
			case '"':
				sb.WriteString(`\"`)
			case '\\':
				sb.WriteString(`\\`)
			default:
				sb.WriteString(`\u00`)
				sb.WriteByte(webDOMParseHex[c>>4])
				sb.WriteByte(webDOMParseHex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			sb.WriteString(s[start:i])
			sb.WriteString("\uFFFD")
		case r == '\u2028':
			sb.WriteString(s[start:i])
			sb.WriteString(`\u2028`)
		case r == '\u2029':
			sb.WriteString(s[start:i])
			sb.WriteString(`\u2029`)
		default:
			i += size
			continue
		}
		i += size
		start = i
	}
	sb.WriteString(s[start:])
	sb.WriteByte('"')
}
