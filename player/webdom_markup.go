package main

// webdom_markup.go — pages without scripts reach the reader in the same shape
// as pages with them.
//
// The script pass (webdom_runtime.go) hands the reader the markup its document
// writes out (serializeForRenderer in webdom/35_serialize.js): every element
// closed, tables with their tbody and rows, a <p> ended by the block that
// follows it, stray text in a table moved before it. A page with nothing to
// run — or every page while JavaScript is switched off — went to the reader's
// parser as it arrived, and that parser closes only what the markup closes:
// `<td>a<td>b` put the second cell inside the first (indented, and its row one
// column short), a list of unclosed <li> items stepped further right with
// every item, and a <tr> left open took the next row's cells. Such a page now
// goes through the same HTML5 tree builder the script pass uses
// (webdom_parse.go), written back with the same rules, so the reader's parser
// sees one kind of markup whatever the page's scripts did.

import "strings"

// webDOMRendererVoid is VOID_ELEMENTS of webdom/35_serialize.js: HTML elements
// written without an end tag.
var webDOMRendererVoid = map[string]bool{
	"area": true, "base": true, "basefont": true, "bgsound": true, "br": true, "col": true,
	"embed": true, "frame": true, "hr": true, "img": true, "input": true, "keygen": true,
	"link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true,
}

// webDOMRendererRawParent is RAW_TEXT_PARENTS of webdom/35_serialize.js: the
// HTML elements whose text is written as it is, not escaped.
var webDOMRendererRawParent = map[string]bool{
	"style": true, "script": true, "xmp": true, "iframe": true, "noembed": true,
	"noframes": true, "plaintext": true, "noscript": true,
}

// webDOMRendererMarkup is source rebuilt by the DOM pass's tree builder and
// written back for the reader, or source itself when the page is larger than
// the DOM pass takes on or the builder's node or work budget stopped it: a tree
// cut short would lose the rest of the page.
func webDOMRendererMarkup(source string) string {
	if strings.TrimSpace(source) == "" || len(source) > webDOMMaxSourceBytes {
		return source
	}
	src := strings.TrimPrefix(webDOMParseNormalize(source), "\uFEFF")
	b := &webDOMParseBuilder{
		tz: webDOMParseTokenizer{src: src}, doc: &webDOMParseNode{},
		maxWork: webDOMParseWorkPerByte*len(src) + webDOMParseWorkBase,
		// Three nodes are held back for html, head and body, as
		// webDOMParseDocument does.
		maxNodes: max(webDOMMaxNodes-3, 1),
	}
	b.run()
	if b.stopped {
		return source
	}
	b.maxNodes = 0
	b.finishDocument()
	var sb strings.Builder
	sb.Grow(len(src) + len(src)/8 + 64)
	for _, n := range b.doc.children {
		webDOMRendererWrite(&sb, n, false)
	}
	return sb.String()
}

// webDOMRendererWrite writes one node the way serializeWalk does in render
// mode: comments are left out, and so are the children of script, noscript
// and template, which the reader never draws; raw text keeps a "</" inside it
// from closing its element early (rawTextForRenderer). The recursion is
// bounded by the tree depth, which the builder's stack cap keeps near
// webDOMParseMaxDepth.
func webDOMRendererWrite(sb *strings.Builder, n *webDOMParseNode, raw bool) {
	switch n.kind {
	case webDOMParseTextNode:
		if raw {
			webDOMRendererRawText(sb, n.data)
		} else {
			webDOMRendererEscape(sb, n.data, false)
		}
	case webDOMParseDoctypeNode:
		sb.WriteString("<!DOCTYPE ")
		sb.WriteString(n.name)
		sb.WriteByte('>')
	case webDOMParseElementNode:
		sb.WriteByte('<')
		sb.WriteString(n.name)
		for i := 0; i+1 < len(n.attrs); i += 2 {
			if !webDOMRendererSafeName(n.attrs[i]) {
				continue
			}
			sb.WriteByte(' ')
			sb.WriteString(n.attrs[i])
			sb.WriteString(`="`)
			webDOMRendererEscape(sb, []byte(n.attrs[i+1]), true)
			sb.WriteByte('"')
		}
		sb.WriteByte('>')
		html := n.ns == 0
		if html && webDOMRendererVoid[n.name] {
			return
		}
		if !html || (n.name != "script" && n.name != "noscript" && n.name != "template") {
			rawKids := html && webDOMRendererRawParent[n.name]
			for _, c := range n.children {
				webDOMRendererWrite(sb, c, rawKids)
			}
		}
		sb.WriteString("</")
		sb.WriteString(n.name)
		sb.WriteByte('>')
	}
}

// webDOMRendererSafeName is renderNameSafe: an attribute name holding a quote,
// '<', '>', '/', '=', white space or a control character would make the
// reader's tag scanner misread the rest of the tag, and the reader has no use
// for such names.
func webDOMRendererSafeName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c <= 0x20, c == 0x7f, c == '"', c == '\'', c == '<', c == '>', c == '/', c == '=':
			return false
		}
	}
	return true
}

// webDOMRendererEscape writes text or an attribute value escaped as escapeText
// and escapeAttr do: &, <, > and the no-break space (as &nbsp;, which the
// reader's table cells tell from a plain space), and in a value also '"'.
func webDOMRendererEscape(sb *strings.Builder, data []byte, attr bool) {
	start := 0
	for i := 0; i < len(data); i++ {
		var rep string
		width := 1
		switch c := data[i]; {
		case c == '&':
			rep = "&amp;"
		case c == '<':
			rep = "&lt;"
		case c == '>':
			rep = "&gt;"
		case c == '"' && attr:
			rep = "&quot;"
		case c == 0xC2 && i+1 < len(data) && data[i+1] == 0xA0:
			rep, width = "&nbsp;", 2
		default:
			continue
		}
		sb.Write(data[start:i])
		sb.WriteString(rep)
		start = i + width
		i += width - 1
	}
	sb.Write(data[start:])
}

// webDOMRendererRawText writes raw text (a <style> body) with every "</"
// followed by a letter written "<\/", which means the same to CSS but cannot
// end the element in the reader's scanner (rawTextForRenderer).
func webDOMRendererRawText(sb *strings.Builder, data []byte) {
	start := 0
	for i := 0; i+2 < len(data); i++ {
		if data[i] != '<' || data[i+1] != '/' {
			continue
		}
		if c := data[i+2] | 0x20; c < 'a' || c > 'z' {
			continue
		}
		sb.Write(data[start : i+1])
		sb.WriteByte('\\')
		start = i + 1
	}
	sb.Write(data[start:])
}
