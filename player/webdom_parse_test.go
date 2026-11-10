package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The expected trees below were traced by hand through webdom_parse.go and
// cross-checked against parse5 (the reference HTML parser in tests/web): they
// match it except for the byte order mark (stripped here) and framesets (not
// supported: <frameset> opens body so the <noframes> content shows).

func TestWebDOMParseDocument(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		want  string
		nodes int // 0 = not checked
	}{
		{"empty document", "",
			`[["html",0,[["head",0,0],["body",0,0]]]]`, 3},
		{"whole document keeps whitespace like browsers", "<!DOCTYPE html>\n<html>\n<head>\n<title>T</title>\n</head>\n<body>\n<p>x</p>\n</body>\n</html>\n",
			`[[10,"html","",""],["html",0,[["head",0,["\u000a",["title",0,["T"]],"\u000a"]],"\u000a",["body",0,["\u000a",["p",0,["x"]],"\u000a\u000a\u000a"]]]]]`, 13},
		{"implied html head and body", "<title>a&amp;b</title><p>x",
			`[["html",0,[["head",0,[["title",0,["a&b"]]]],["body",0,[["p",0,["x"]]]]]]]`, 7},
		{"leading whitespace dropped, text opens body", "  hi <b>there</b>",
			`[["html",0,[["head",0,0],["body",0,["hi ",["b",0,["there"]]]]]]]`, 0},
		{"comments around html and doctype ids", "<!--a--><!DOCTYPE html PUBLIC \"-//W3C//DTD HTML 4.01//EN\" \"http://www.w3.org/TR/html4/strict.dtd\"><html><body></body></html><!--b-->",
			`[[8,"a"],[10,"html","-//W3C//DTD HTML 4.01//EN","http://www.w3.org/TR/html4/strict.dtd"],["html",0,[["head",0,0],["body",0,0]]],[8,"b"]]`, 0},
		{"comment after body goes to html", "<p>x</p></body><!--c-->",
			`[["html",0,[["head",0,0],["body",0,[["p",0,["x"]]]],[8,"c"]]]]`, 0},
		{"second html and body merge attributes", "<html lang=en><body class=a><html lang=fr dir=rtl><body class=b id=c>",
			`[["html",["lang","en","dir","rtl"],[["head",0,0],["body",["class","a","id","c"],0]]]]`, 0},
		{"head elements after head", "<head></head><meta charset=x><body><link rel=y></body>",
			`[["html",0,[["head",0,[["meta",["charset","x"],0]]],["body",0,[["link",["rel","y"],0]]]]]]`, 0},
		{"template in head", "<head><template><tr><td>a</td></tr></template></head><body>",
			`[["html",0,[["head",0,[["template",0,[["tr",0,[["td",0,["a"]]]]]]]],["body",0,0]]]]`, 0},
		{"byte order mark and CRLF", "\ufeff<!DOCTYPE html>\r\n<p>a\r\nb\rc</p>",
			`[[10,"html","",""],["html",0,[["head",0,0],["body",0,[["p",0,["a\u000ab\u000ac"]]]]]]]`, 0},
		{"frameset shows noframes", "<html><head></head><frameset><frame src=a><noframes><p>no</p></noframes></frameset></html>",
			`[["html",0,[["head",0,0],["body",0,[["noframes",0,["<p>no</p>"]]]]]]]`, 0},
	}
	for _, tc := range tests {
		got, nodes, truncated := webDOMParseDocument(tc.src, 0)
		if got != tc.want || truncated {
			t.Errorf("%s:\n got %s (truncated %v)\nwant %s", tc.name, got, truncated, tc.want)
		}
		if tc.nodes != 0 && nodes != tc.nodes {
			t.Errorf("%s: %d nodes, want %d", tc.name, nodes, tc.nodes)
		}
	}
}

func TestWebDOMParseFragment(t *testing.T) {
	tests := []struct {
		name    string
		context string
		ns      int
		src     string
		want    string
		nodes   int // 0 = not checked
	}{
		{"attributes: quoting, case, duplicates", "div", 0, "<div id=a class=\"b c\" data-x='1' hidden ID=dup>t</div>",
			`[["div",["id","a","class","b c","data-x","1","hidden",""],["t"]]]`, 0},
		{"attribute character references", "div", 0, "<a href=\"?a=1&copy=2&amp;b=&lt;&notit;&#x41;&#65&amp\">",
			`[["a",["href","?a=1&copy=2&b=<&notit;AA&"],0]]`, 0},
		{"upper-case bare references in attributes", "div", 0, "<p title=\"&AMP; &AMP &QUOT &GT=1 &REGx &COPY\">",
			`[["p",["title","& & \" &GT=1 &REGx ©"],0]]`, 0},
		{"text character references", "div", 0, "a &lt;b&gt; &amp;amp; &copy2 &#x26;",
			`["a <b> &amp; ©2 &"]`, 0},
		{"numeric references in text and RCDATA like browsers", "div", 0, "a&#x;b&#9xc&#4294967361;d&#65<title>&#x;&#9x</title>",
			`["a&#x;b\u0009xc�dA",["title",0,["&#x;\u0009x"]]]`, 0},
		{"CR, NUL and invalid UTF-8", "div", 0, "a\r\nb\rc\x00d\xffe",
			`["a\u000ab\u000ac�d�e"]`, 0},
		{"comments and bogus comments", "div", 0, "<!--a--><!----><!--><!---><!--b--!><?pi?><!x></>z</ 1>",
			`[[8,"a"],[8,""],[8,""],[8,""],[8,"b"],[8,"?pi?"],[8,"x"],"z",[8," 1"]]`, 9},
		{"unterminated comment", "div", 0, "x<!--y--",
			`["x",[8,"y"]]`, 0},
		{"a lone < is text, a cut-off tag is dropped", "div", 0, "a < b <3 <<c d",
			`["a < b <3 <"]`, 0},
		{"raw text and RCDATA", "div", 0, "<script>if (a<b) x='</div>';</script><style>p{}</style><textarea>\n&lt;x</textarea>",
			`[["script",0,["if (a<b) x='</div>';"]],["style",0,["p{}"]],["textarea",0,["<x"]]]`, 0},
		{"script escape states", "div", 0, "<script><!--\ndocument.write(\"<script>x</script>\");\n//--></script>y",
			`[["script",0,["<!--\u000adocument.write(\"<script>x</script>\");\u000a//-->"]],"y"]`, 0},
		{"plaintext", "div", 0, "<plaintext>a<b>&amp;",
			`[["plaintext",0,["a<b>&amp;"]]]`, 0},
		{"pre drops one leading newline", "div", 0, "<pre>\n\nx</pre>",
			`[["pre",0,["\u000ax"]]]`, 0},
		{"implied end tags: p, li, headings", "div", 0, "<p>a<div>b</div><p>c<ul><li>d<li>e</ul><h1>f<h2>g</h1>h",
			`[["p",0,["a"]],["div",0,["b"]],["p",0,["c"]],["ul",0,[["li",0,["d"]],["li",0,["e"]]]],["h1",0,["f"]],["h2",0,["g"]],"h"]`, 0},
		{"void elements and self-closing", "div", 0, "<br/><img src=x><div/>a<input>b</br>",
			`[["br",0,0],["img",["src","x"],0],["div",0,["a",["input",0,0],"b",["br",0,0]]]]`, 0},
		{"stray end tag stops at a special element", "div", 0, "<span><div>a</span>b</div>c",
			`[["span",0,[["div",0,["ab"]],"c"]]]`, 0},
		{"second a closes the first", "div", 0, "<a href=1>x<a href=2>y",
			`[["a",["href","1"],["x"]],["a",["href","2"],["y"]]]`, 0},
		{"select and options", "div", 0, "<select><option>a<option>b<optgroup><option>c</select>d",
			`[["select",0,[["option",0,["a"]],["option",0,["b"]],["optgroup",0,[["option",0,["c"]]]]]],"d"]`, 0},
		{"ruby annotations close each other only inside ruby", "div", 0, "<rp>a<rp>b<ruby>x<rp>c<rp>d</ruby>",
			`[["rp",0,["a",["rp",0,["b",["ruby",0,["x",["rp",0,["c"]],["rp",0,["d"]]]]]]]]]`, 0},
		{"context form ignores a nested form", "form", 0, "<form>x",
			`["x"]`, 1},
		{"context form: </form> clears the form pointer", "form", 0, "<form>x</form>y<form>z",
			`["xy",["form",0,["z"]]]`, 0},
		{"table with implied tbody", "div", 0, "<table><tr><td>a<td>b</table>",
			`[["table",0,[["tbody",0,[["tr",0,[["td",0,["a"]],["td",0,["b"]]]]]]]]]`, 0},
		{"foster-parented text merges", "div", 0, "<table>x<tr><td>y</td></tr>z</table>",
			`["xz",["table",0,[["tbody",0,[["tr",0,[["td",0,["y"]]]]]]]]]`, 0},
		{"foster-parented element", "div", 0, "<table><b>x</b><tr><td>y</table>",
			`[["b",0,["x"]],["table",0,[["tbody",0,[["tr",0,[["td",0,["y"]]]]]]]]]`, 0},
		{"whitespace stays in table parts", "div", 0, "<table> <tr> <td>a</td> </tr> </table>",
			`[["table",0,[" ",["tbody",0,[["tr",0,[" ",["td",0,["a"]]," "]]," "]]]]]`, 0},
		{"context tr", "tr", 0, "<td>a<td>b",
			`[["td",0,["a"]],["td",0,["b"]]]`, 0},
		{"context table", "table", 0, "<tr><td>c",
			`[["tbody",0,[["tr",0,[["td",0,["c"]]]]]]]`, 0},
		{"context tbody", "tbody", 0, "<tr><td>c",
			`[["tr",0,[["td",0,["c"]]]]]`, 0},
		{"context td drops rows", "td", 0, "<tr>x",
			`["x"]`, 0},
		{"context html", "html", 0, "<title>t</title>x",
			`[["head",0,[["title",0,["t"]]]],["body",0,["x"]]]`, 5},
		{"context textarea", "textarea", 0, "a<b>&amp;",
			`["a<b>&"]`, 1},
		{"context script", "script", 0, "</script>x",
			`["</script>x"]`, 1},
		{"svg with foreignObject and case tables", "div", 0, "<svg viewbox=\"0 0 1 1\"><foreignobject><p>x</p></foreignobject><lineargradient/><circle r=1></circle></svg><p>y",
			`[["svg",["viewBox","0 0 1 1"],[["foreignObject",0,[["p",0,["x"]]],1],["linearGradient",0,0,1],["circle",["r","1"],0,1]],1],["p",0,["y"]]]`, 0},
		{"HTML tag breaks out of svg", "div", 0, "<svg><g><div>x</div></g></svg>",
			`[["svg",0,[["g",0,0,1]],1],["div",0,["x"]]]`, 0},
		{"MathML integration points and CDATA", "div", 0, "<math><mi>x</mi><annotation-xml encoding=\"text/html\"><b>y</b></annotation-xml></math><svg><![CDATA[a<b]]></svg>",
			`[["math",0,[["mi",0,["x"],2],["annotation-xml",["encoding","text/html"],[["b",0,["y"]]],2]],2],["svg",0,["a<b"],1]]`, 0},
		{"CDATA in HTML is a bogus comment", "div", 0, "<![CDATA[x]]>",
			`[[8,"[CDATA[x]]"]]`, 0},
		{"context svg", "svg", 1, "<circle/><p>x",
			`[["circle",0,0,1],["p",0,["x"]]]`, 0},
		{"JSON escapes", "div", 0, "<p title='\"\\'>\t\u2028",
			`[["p",["title","\"\\"],["\u0009\u2028"]]]`, 0},
	}
	for _, tc := range tests {
		got, nodes, truncated := webDOMParseFragment(tc.src, tc.context, tc.ns, 0)
		if got != tc.want || truncated {
			t.Errorf("%s:\n got %s (truncated %v)\nwant %s", tc.name, got, truncated, tc.want)
		}
		if tc.nodes != 0 && nodes != tc.nodes {
			t.Errorf("%s: %d nodes, want %d", tc.name, nodes, tc.nodes)
		}
	}
}

func TestWebDOMParseLimits(t *testing.T) {
	// The node budget stops parsing and keeps what was built. A document
	// holds three nodes back for html, head and body (budget 8: the parse
	// stops at the second <p>, the 6th node), so a stopped parse still has
	// them; so does the html context (two held back: head and body).
	got, nodes, truncated := webDOMParseDocument("<p>a</p><p>b</p>", 8)
	if want := `[["html",0,[["head",0,0],["body",0,[["p",0,["a"]]]]]]]`; got != want || nodes != 5 || !truncated {
		t.Errorf("document limit: got %s, %d nodes, truncated %v", got, nodes, truncated)
	}
	got, nodes, truncated = webDOMParseDocument("<!--a--><!--b--><!--c-->", 5)
	if want := `[[8,"a"],[8,"b"],["html",0,[["head",0,0],["body",0,0]]]]`; got != want || nodes != 5 || !truncated {
		t.Errorf("document limit before html: got %s, %d nodes, truncated %v", got, nodes, truncated)
	}
	got, nodes, truncated = webDOMParseFragment("<!--a--><!--b--><!--c-->", "html", 0, 4)
	if want := `[[8,"a"],[8,"b"],["head",0,0],["body",0,0]]`; got != want || nodes != 4 || !truncated {
		t.Errorf("html context limit: got %s, %d nodes, truncated %v", got, nodes, truncated)
	}
	got, nodes, truncated = webDOMParseFragment("<b>1</b><b>2</b>", "div", 0, 3)
	if want := `[["b",0,["1"]],["b",0,0]]`; got != want || nodes != 3 || !truncated {
		t.Errorf("fragment limit: got %s, %d nodes, truncated %v", got, nodes, truncated)
	}

	// Stack walks that create no node (stray end tags under 500 open
	// elements, a nested <form>) count against the work budget, so hostile
	// markup stops as truncated, still with html, head and body, instead of
	// costing hundreds of steps per byte. An ordinary page nested 40 deep
	// (about 4 steps per byte) stays far below the budget.
	spans := strings.Repeat("<span>", 520)
	for i, src := range []string{
		spans + strings.Repeat("</x>", 20000),
		"<svg>" + strings.Repeat("<g>", 520) + strings.Repeat("</x>", 20000),
		spans + strings.Repeat("</li>", 20000),
		"<form>" + spans + strings.Repeat("<form>", 20000),
		spans + strings.Repeat("</body>", 20000),
	} {
		out, _, cut := webDOMParseDocument(src, 0)
		if !cut || !webDOMParseTestSkeleton(out) {
			t.Errorf("hostile markup %d: truncated %v, skeleton %v", i, cut, webDOMParseTestSkeleton(out))
		}
	}
	if _, _, cut := webDOMParseFragment(spans+strings.Repeat("</x>", 20000), "div", 0, 0); !cut {
		t.Errorf("hostile fragment: not truncated by the work budget")
	}
	page := strings.Repeat("<div>", 40) + strings.Repeat("<p>x <a href=y>z</a></p>", 20000)
	if _, _, cut := webDOMParseDocument(page, 0); cut {
		t.Errorf("ordinary page nested 40 deep: truncated by the work budget")
	}

	// Past webDOMParseAttrSetAt kept names the duplicate check uses a set:
	// the first of duplicates still wins, for names kept before and after
	// the switch (and case-insensitively).
	var dup, wantDup strings.Builder
	dup.WriteString("<p")
	wantDup.WriteString(`[["p",[`)
	for i := 0; i < webDOMParseAttrSetAt+8; i++ {
		fmt.Fprintf(&dup, " a%d=%d", i, i)
		fmt.Fprintf(&wantDup, `"a%d","%d",`, i, i)
	}
	fmt.Fprintf(&dup, " a5=x A%d=x b=1>", webDOMParseAttrSetAt+7)
	wantDup.WriteString(`"b","1"],0]]`)
	if out, _, _ := webDOMParseFragment(dup.String(), "div", 0, 0); out != wantDup.String() {
		t.Errorf("duplicate attributes past the set switch:\n got %s\nwant %s", out, wantDup.String())
	}

	// Attributes past webDOMParseMaxAttrs are dropped.
	var sb strings.Builder
	sb.WriteString("<p")
	for i := 0; i < webDOMParseMaxAttrs+50; i++ {
		fmt.Fprintf(&sb, " a%d=%d", i, i)
	}
	sb.WriteString(">")
	got, _, _ = webDOMParseFragment(sb.String(), "div", 0, 0)
	var tree []any
	if err := json.Unmarshal([]byte(got), &tree); err != nil || len(tree) != 1 {
		t.Fatalf("many attributes: %v %.80s", err, got)
	}
	p, _ := tree[0].([]any)
	if len(p) < 2 {
		t.Fatalf("many attributes: %.80s", got)
	}
	if attrs, _ := p[1].([]any); len(attrs) != 2*webDOMParseMaxAttrs {
		t.Errorf("many attributes: kept %d names and values, want %d", len(attrs), 2*webDOMParseMaxAttrs)
	}

	// Nesting past webDOMParseMaxDepth is flattened, so the JSON (and the
	// encoder's recursion) stays shallow whatever the markup does.
	deep := strings.Repeat("<div>", 3*webDOMParseMaxDepth) + "x" + strings.Repeat("<table><tr><td>", 200) +
		strings.Repeat("<svg><g>", 200) + strings.Repeat("<form><span></form>", 200)
	for _, out := range []string{webDOMParseTestTree(webDOMParseDocument(deep, 0)), webDOMParseTestTree(webDOMParseFragment(deep, "td", 0, 0))} {
		if d := webDOMParseTestDepth(out); d > 2*webDOMParseMaxDepth+8 {
			t.Errorf("deep markup: JSON depth %d", d)
		}
	}
}

// TestWebDOMParseRobust feeds broken and hostile markup through every kind of
// context: the parser must not panic, must return valid UTF-8 JSON, must stay
// within the node budget, and a document must always have html, head, body —
// also when the budget truncated it.
func TestWebDOMParseRobust(t *testing.T) {
	inputs := []string{
		"", "<", "</", "<!", "<!-", "<!--", "<!---", "<?", "<a", "<a b", "<a b=", "<a b='", "<a b=\"c", "</a",
		"</a b='>", "<!DOCTYPE", "<!doctype html public \"x", "<![CDATA[", "<svg><![CDATA[x", "&", "&#", "&#x",
		"&#xZ;", "&#99999999999999;", "<a href='&#0;&#xD800;&#x110000;&#128;&#99999999999999'>",
		"<script>", "<script><!--<script>", "<style></sty", "<textarea>&", "<plaintext></plaintext>",
		"<table><tr><td><svg><foreignObject><table><caption><select><option><optgroup>",
		"<form><table><form></table></form></form>", "<template><col><td><caption></template>",
		"<math><mi><svg><desc><p></math></p>", "<table><colgroup><col><template></template>x</colgroup></table>",
		"</html></body><!--x--></html>  <p>", "<frameset><frame>", "\x00\r\n\xff\xfe<p\x00>\x00</p\x00>",
		"<p><b><i><u></p></u></i></b>", "<ul><li><ul><li></ul></li></ul></li>", "<h1><h2></h1></h2>",
		"<select><select><input><keygen><textarea></select>", "<a><table><a></table></a>",
		"<head><template><body></template><frameset>", "<table><td><table><tr><th></caption></colgroup>",
		"<svg><title><svg><foreignObject><math><annotation-xml encoding=TEXT/HTML><td><table>",
	}
	inputs = append(inputs, strings.Join(inputs, ""))
	contexts := []struct {
		name string
		ns   int
	}{
		{"div", 0}, {"table", 0}, {"tbody", 0}, {"tr", 0}, {"td", 0}, {"caption", 0}, {"colgroup", 0},
		{"select", 0}, {"template", 0}, {"html", 0}, {"head", 0}, {"body", 0}, {"textarea", 0}, {"script", 0},
		{"svg", 1}, {"foreignObject", 1}, {"math", 2}, {"mi", 2}, {"annotation-xml", 2}, {"", 3},
	}
	check := func(what, tree string, nodes, maxNodes int) {
		if !json.Valid([]byte(tree)) || !utf8.ValidString(tree) {
			t.Errorf("%s: invalid JSON %q", what, tree)
		}
		if maxNodes > 0 && nodes > maxNodes {
			t.Errorf("%s: %d nodes past the limit %d", what, nodes, maxNodes)
		}
	}
	for _, src := range inputs {
		for _, maxNodes := range []int{0, 4} {
			tree, nodes, _ := webDOMParseDocument(src, maxNodes)
			check(fmt.Sprintf("document %q", src), tree, nodes, maxNodes)
			if !webDOMParseTestSkeleton(tree) {
				t.Errorf("document %q: no html/head/body skeleton in %s", src, tree)
			}
			for _, c := range contexts {
				tree, nodes, _ = webDOMParseFragment(src, c.name, c.ns, maxNodes)
				check(fmt.Sprintf("%s %q", c.name, src), tree, nodes, maxNodes)
			}
		}
	}
}

// webDOMParseTestTree keeps the tree of a parse result.
func webDOMParseTestTree(tree string, _ int, _ bool) string { return tree }

// webDOMParseTestDepth is the bracket nesting depth of a JSON text.
func webDOMParseTestDepth(s string) int {
	depth, deepest, inString := 0, 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inString && c == '\\':
			i++
		case c == '"':
			inString = !inString
		case !inString && c == '[':
			depth++
			deepest = max(deepest, depth)
		case !inString && c == ']':
			depth--
		}
	}
	return deepest
}

// webDOMParseTestSkeleton reports whether a document tree has exactly one
// html element whose element children are head and then body.
func webDOMParseTestSkeleton(tree string) bool {
	var top []any
	if json.Unmarshal([]byte(tree), &top) != nil {
		return false
	}
	htmls, ok := 0, false
	for _, n := range top {
		el, _ := n.([]any)
		if len(el) < 3 || el[0] != "html" {
			continue
		}
		htmls++
		kids, _ := el[2].([]any)
		var names []string
		for _, k := range kids {
			if e, _ := k.([]any); len(e) >= 3 {
				if name, isName := e[0].(string); isName {
					names = append(names, name)
				}
			}
		}
		ok = len(names) == 2 && names[0] == "head" && names[1] == "body"
	}
	return htmls == 1 && ok
}
