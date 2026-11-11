package main

// reader.go — document text for the reader scene: EPUB and PDF.
//
// The PDF path used to scan every BT…ET block in the whole file and return the
// same string for every page, so a fifty-page document showed its entire text
// on page one (and repeated it on page two). This file reads the page tree,
// decompresses each page's content streams and extracts that page's text, and
// it adds EPUB support: container → OPF → spine, one chapter per spine
// document, paginated for the same paper UI.
//
// Both parsers are deliberately dependency-free. The handheld ships no PDF or
// EPUB library, and adding one would cost more memory than the reader is worth.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"html"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// pdfChapter marks where a chapter starts in the flat page list the renderer
// walks. A PDF without a parsed outline has none; an EPUB always has at least
// one.
type pdfChapter struct {
	Title     string
	FirstPage int
}

// readerPageChars is how much text one EPUB page holds. The paper card shows
// about twenty lines, and a page that takes three screens of scrolling is not a
// page any more.
const readerPageChars = 1200

// ── EPUB ───────────────────────────────────────────────────────────────────

var (
	reEPUBRootfile = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)full-path\s*=\s*["']([^"']+)["']`) })
	reEPUBTitle    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<dc:title[^>]*>(.*?)</dc:title>`) })
	reEPUBCreator  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<dc:creator[^>]*>(.*?)</dc:creator>`) })
	reEPUBItem     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<item\b[^>]*>`) })
	reEPUBItemref  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<itemref\b[^>]*>`) })
	reEPUBHTMLAttr = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)([a-z:-]+)\s*=\s*["']([^"']*)["']`) })
	reStyleBlock   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`) })
	reCommentBlock = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)<!--.*?-->`) })
	reHeadBlock    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<head\b.*?</head>`) })
	reTag          = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)<[^>]*>`) })
	reLineBreakTag = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)</?(p|div|section|article|li|ul|ol|h[1-6]|tr|table|blockquote|pre|figure|figcaption)\b[^>]*>|<br\s*/?>`)
	})
	reBulletTag   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)<li\b[^>]*>`) })
	reHeading     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<h[12][^>]*>(.*?)</h[12]>`) })
	reDocTitleTag = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`) })
)

// epubOpenFile validates and loads one EPUB from the File Explorer. The path
// comes from a list row, so it is resolved against the explorer root first.
func epubOpenFile(path string) pdfDoc {
	resolved, err := resolveExplorerEntry(appConfig, path)
	if err != nil {
		return pdfDoc{Path: path, Format: "EPUB", Loaded: true, Error: "Cannot open file: " + err.Error()}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return pdfDoc{Path: resolved, Format: "EPUB", Loaded: true, Error: "Cannot open file: " + err.Error()}
	}
	return epubParseBytes(data, resolved)
}

// epubParseBytes turns an EPUB archive into paged chapters.
func epubParseBytes(data []byte, path string) pdfDoc {
	doc := pdfDoc{Path: path, Format: "EPUB", Loaded: true}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		doc.Error = "Not a valid EPUB: " + err.Error()
		return doc
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "./")] = f
	}
	readFile := func(name string) []byte {
		name = strings.TrimPrefix(strings.TrimSpace(name), "./")
		f, ok := files[name]
		if !ok {
			// Some archives spell names URL-encoded; try the decoded form.
			if decoded, err := url.PathUnescape(name); err == nil {
				f, ok = files[decoded]
			}
		}
		if !ok || f.UncompressedSize64 > 8<<20 {
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		body, err := io.ReadAll(io.LimitReader(rc, 8<<20))
		if err != nil {
			return nil
		}
		return body
	}

	opfPath := ""
	if container := readFile("META-INF/container.xml"); container != nil {
		if m := reEPUBRootfile().FindSubmatch(container); m != nil {
			opfPath = string(m[1])
		}
	}
	if opfPath == "" {
		names := make([]string, 0, len(files))
		for name := range files {
			if strings.EqualFold(filepath.Ext(name), ".opf") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			opfPath = names[0]
		}
	}
	if opfPath == "" {
		doc.Error = "This EPUB has no package document (OPF)"
		return doc
	}
	opf := readFile(opfPath)
	if opf == nil {
		doc.Error = "This EPUB's package document is missing"
		return doc
	}
	// A package document without <dc:title> is legal; the file name below
	// stands in (indexing the missing match used to crash the reader).
	if m := reEPUBTitle().FindSubmatch(opf); m != nil {
		doc.Title = html.UnescapeString(strings.TrimSpace(string(m[1])))
	}
	if m := reEPUBCreator().FindSubmatch(opf); m != nil {
		doc.Author = cleanHTMLInline(string(m[1]))
	}
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	// Manifest: id -> href for HTML documents.
	hrefByID := map[string]string{}
	for _, tag := range reEPUBItem().FindAll(opf, -1) {
		attrs := epubAttrs(string(tag))
		id, href := attrs["id"], attrs["href"]
		media := strings.ToLower(attrs["media-type"])
		if id == "" || href == "" {
			continue
		}
		if media == "" || strings.Contains(media, "html") || strings.Contains(media, "xml") {
			hrefByID[id] = href
		}
	}
	opfDir := filepath.ToSlash(filepath.Dir(opfPath))
	spine := []string{}
	for _, tag := range reEPUBItemref().FindAll(opf, -1) {
		attrs := epubAttrs(string(tag))
		if strings.EqualFold(attrs["linear"], "no") {
			continue
		}
		if href, ok := hrefByID[attrs["idref"]]; ok {
			spine = append(spine, epubResolveHref(opfDir, href))
		}
	}
	if len(spine) == 0 {
		ids := make([]string, 0, len(hrefByID))
		for id := range hrefByID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			spine = append(spine, epubResolveHref(opfDir, hrefByID[id]))
		}
	}
	if len(spine) == 0 {
		doc.Error = "This EPUB has no reading order"
		return doc
	}

	for i, href := range spine {
		raw := readFile(href)
		if raw == nil {
			continue
		}
		title, text := epubHTMLText(raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		pages := readerPaginate(text, readerPageChars)
		if len(pages) == 0 {
			continue
		}
		doc.Chapters = append(doc.Chapters, pdfChapter{Title: title, FirstPage: len(doc.Pages) + 1})
		for _, page := range pages {
			doc.Pages = append(doc.Pages, pdfPage{Text: page, Width: 612, Height: 792})
		}
	}
	if len(doc.Pages) == 0 {
		doc.Error = "This EPUB has no readable text"
		return doc
	}
	doc.NumPages = len(doc.Pages)
	return doc
}

// epubAttrs reads the attributes out of one HTML/OPF tag.
func epubAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	for _, m := range reEPUBHTMLAttr().FindAllStringSubmatch(tag, -1) {
		attrs[strings.ToLower(m[1])] = html.UnescapeString(m[2])
	}
	return attrs
}

// epubResolveHref joins an OPF-relative href to the archive directory, dropping
// any fragment so a chapter link points at the file itself.
func epubResolveHref(opfDir, href string) string {
	href = strings.TrimSpace(href)
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href = href[:i]
	}
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	if opfDir == "." || opfDir == "" || opfDir == "/" {
		return strings.TrimPrefix(href, "./")
	}
	return pathJoinSlash(opfDir, href)
}

func pathJoinSlash(dir, name string) string {
	if strings.HasPrefix(name, "/") {
		return strings.TrimPrefix(name, "/")
	}
	return strings.TrimSuffix(dir, "/") + "/" + strings.TrimPrefix(name, "./")
}

// cleanHTMLInline strips tags and collapses whitespace for a metadata value.
func cleanHTMLInline(s string) string {
	s = reTag().ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// epubHTMLText is one spine document as a chapter title plus running text.
// Paragraph and heading boundaries become newlines before the tags are dropped;
// dropping every tag first glued the last word of one paragraph to the first of
// the next.
func epubHTMLText(raw []byte) (string, string) {
	s := string(raw)
	s = reCommentBlock().ReplaceAllString(s, " ")
	title := ""
	if m := reDocTitleTag().FindStringSubmatch(s); m != nil {
		title = cleanHTMLInline(m[1])
	}
	body := s
	if i := strings.Index(asciiLower(body), "<body"); i >= 0 {
		body = body[i:]
	}
	body = reStyleBlock().ReplaceAllString(body, " ")
	body = reHeadBlock().ReplaceAllString(body, " ")
	if title == "" {
		if m := reHeading().FindStringSubmatch(body); m != nil {
			title = cleanHTMLInline(m[1])
		}
	}
	body = reBulletTag().ReplaceAllString(body, "\n• ")
	body = reLineBreakTag().ReplaceAllString(body, "\n")
	body = reTag().ReplaceAllString(body, "")
	body = html.UnescapeString(body)
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")

	lines := make([]string, 0, 64)
	for _, line := range strings.Split(body, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return title, strings.Join(lines, "\n")
}

// ── Pagination ─────────────────────────────────────────────────────────────

// readerPaginate splits text into pages of at most maxChars, breaking at a line
// boundary near the limit so a paragraph is not cut mid-word.
func readerPaginate(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChars < 200 {
		maxChars = 200
	}
	lines := strings.Split(text, "\n")
	var pages []string
	var current strings.Builder
	flush := func() {
		page := strings.TrimSpace(current.String())
		if page != "" {
			pages = append(pages, page)
		}
		current.Reset()
	}
	for _, line := range lines {
		// A single huge line (a paragraph with no breaks) is hard-wrapped.
		for len(line) > maxChars {
			cut := maxChars
			if idx := strings.LastIndexByte(line[:maxChars], ' '); idx > maxChars/2 {
				cut = idx
			}
			if current.Len() > 0 {
				flush()
			}
			pages = append(pages, strings.TrimSpace(line[:cut]))
			line = strings.TrimSpace(line[cut:])
		}
		if line == "" {
			continue
		}
		if current.Len() > 0 && current.Len()+1+len(line) > maxChars {
			flush()
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	flush()
	return pages
}

// ── PDF ────────────────────────────────────────────────────────────────────

var (
	rePDFObject    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)(\d+)\s+\d+\s+obj\b(.*?)endobj`) })
	rePDFRef       = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(\d+)\s+\d+\s+R`) })
	rePDFPagesRef  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Pages\s+(\d+)\s+\d+\s+R`) })
	rePDFKids      = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Kids\s*\[(.*?)\]`) })
	rePDFContents  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Contents\s*(\d+)\s+\d+\s+R`) })
	rePDFContentsA = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Contents\s*\[(.*?)\]`) })
	rePDFTitle     = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Title\s*\(((?:\\.|[^\\)])*)\)`) })
	rePDFAuthor    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)/Author\s*\(((?:\\.|[^\\)])*)\)`) })
)

// pdfPageObjects maps an object number to its body.
func pdfPageObjects(data []byte) map[int][]byte {
	objs := map[int][]byte{}
	for _, m := range rePDFObject().FindAllSubmatch(data, -1) {
		num := 0
		for _, c := range m[1] {
			num = num*10 + int(c-'0')
		}
		objs[num] = m[2]
	}
	return objs
}

// pdfBodyIsPage reports whether a page-tree object is a leaf page and not a
// /Pages node. Go's regexp has no lookahead, so this is checked by hand: the
// name after /Type must be /Page and the next character must not be a letter.
func pdfBodyIsPage(body []byte) bool {
	for i := 0; i+5 <= len(body); i++ {
		if !bytes.EqualFold(body[i:i+5], []byte("/Type")) {
			continue
		}
		j := i + 5
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\r' || body[j] == '\n') {
			j++
		}
		if j+5 > len(body) || !bytes.EqualFold(body[j:j+5], []byte("/Page")) {
			continue
		}
		k := j + 5
		if k >= len(body) || !isASCIINameByte(body[k]) {
			return true
		}
	}
	return false
}

func isASCIINameByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// pdfOrderedPageObjects walks the page tree from the catalog and returns the
// page objects in reading order. A file whose catalog or /Kids cannot be read
// falls back to object order, which is the usual layout even when the tree is
// stored in an object stream.
func pdfOrderedPageObjects(objs map[int][]byte) [][]byte {
	numbers := make([]int, 0, len(objs))
	for num := range objs {
		numbers = append(numbers, num)
	}
	sort.Ints(numbers)

	catalog := -1
	for _, num := range numbers {
		if bytes.Contains(objs[num], []byte("/Catalog")) {
			catalog = num
			break
		}
	}
	var pages [][]byte
	seen := map[int]bool{}
	var walk func(num, depth int)
	walk = func(num, depth int) {
		if depth > 32 || seen[num] {
			return
		}
		seen[num] = true
		body, ok := objs[num]
		if !ok {
			return
		}
		if pdfBodyIsPage(body) {
			pages = append(pages, body)
			return
		}
		kids := rePDFKids().FindSubmatch(body)
		if kids == nil {
			return
		}
		for _, ref := range rePDFRef().FindAllSubmatch(kids[1], -1) {
			child := 0
			for _, c := range ref[1] {
				child = child*10 + int(c-'0')
			}
			walk(child, depth+1)
		}
	}
	if catalog >= 0 {
		if m := rePDFPagesRef().FindSubmatch(objs[catalog]); m != nil {
			root := 0
			for _, c := range m[1] {
				root = root*10 + int(c-'0')
			}
			walk(root, 0)
		}
	}
	if len(pages) > 0 {
		return pages
	}
	for _, num := range numbers {
		if pdfBodyIsPage(objs[num]) {
			pages = append(pages, objs[num])
		}
	}
	return pages
}

// pdfContentObjects returns the content-stream objects referenced by a page, in
// the order the page lists them.
func pdfContentObjects(objs map[int][]byte, page []byte) [][]byte {
	refs := []int{}
	if m := rePDFContentsA().FindSubmatch(page); m != nil {
		for _, ref := range rePDFRef().FindAllSubmatch(m[1], -1) {
			n := 0
			for _, c := range ref[1] {
				n = n*10 + int(c-'0')
			}
			refs = append(refs, n)
		}
	}
	if len(refs) == 0 {
		if m := rePDFContents().FindSubmatch(page); m != nil {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			refs = append(refs, n)
		}
	}
	out := make([][]byte, 0, len(refs))
	for _, ref := range refs {
		if body, ok := objs[ref]; ok {
			out = append(out, body)
		}
	}
	return out
}

// pdfDecodeStream returns the bytes between stream and endstream, inflated when
// the object says FlateDecode. An unsupported filter returns nil rather than
// feeding compressed image data to the text lexer.
func pdfDecodeStream(body []byte) []byte {
	idx := bytes.Index(body, []byte("stream"))
	if idx < 0 {
		return nil
	}
	dict := body[:idx]
	rest := body[idx+len("stream"):]
	rest = bytes.TrimPrefix(rest, []byte("\r\n"))
	rest = bytes.TrimPrefix(rest, []byte("\n"))
	rest = bytes.TrimPrefix(rest, []byte("\r"))
	end := bytes.Index(rest, []byte("endstream"))
	if end < 0 {
		return nil
	}
	raw := bytes.TrimRight(rest[:end], "\r\n")
	if len(raw) == 0 {
		return nil
	}
	if bytes.Contains(dict, []byte("FlateDecode")) {
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			defer zr.Close()
			if out, err := io.ReadAll(io.LimitReader(zr, 32<<20)); err == nil {
				return out
			}
		}
		// Some writers use a raw deflate stream with a FlateDecode label.
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		if out, err := io.ReadAll(io.LimitReader(fr, 32<<20)); err == nil {
			return out
		}
		return nil
	}
	if bytes.Contains(dict, []byte("/Filter")) {
		return nil
	}
	return raw
}

// pdfPageTexts extracts per-page text. An empty result means the page tree could
// not be read, and the caller falls back to a whole-document scan.
func pdfPageTexts(data []byte) []string {
	objs := pdfPageObjects(data)
	pages := pdfOrderedPageObjects(objs)
	if len(pages) == 0 {
		return nil
	}
	out := make([]string, 0, len(pages))
	for _, page := range pages {
		var sb strings.Builder
		for _, stream := range pdfContentObjects(objs, page) {
			content := pdfDecodeStream(stream)
			if content == nil {
				continue
			}
			text := pdfTextFromContent(content)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(text)
		}
		out = append(out, strings.TrimSpace(sb.String()))
	}
	return out
}

// ── PDF content-stream lexer ───────────────────────────────────────────────

// pdfPiece is one string inside a TJ array, with the kerning number that
// precedes it (negative pulls the next glyph closer, a large negative value is
// how a PDF spells a word space).
type pdfPiece struct {
	Text string
	Kern float64
}

type pdfToken struct {
	Op       string
	Strings  []string
	Pieces   []pdfPiece
	Numbers  []float64
	IsString bool
	IsNumber bool
	IsArray  bool
}

func isPDFSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f', 0:
		return true
	}
	return false
}

func isPDFDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// pdfLexToken reads one token. Parentheses may nest, an escaped parenthesis is
// part of the string, and octal escapes are decoded; a hex string is decoded the
// same way a simple font's bytes are.
func pdfLexToken(data []byte, i int) (pdfToken, int) {
	for i < len(data) && isPDFSpace(data[i]) {
		i++
	}
	if i >= len(data) {
		return pdfToken{}, i
	}
	if data[i] == '%' {
		for i < len(data) && data[i] != '\n' {
			i++
		}
		return pdfLexToken(data, i)
	}
	switch c := data[i]; {
	case c == '(':
		var raw []byte
		depth := 1
		i++
		for i < len(data) && depth > 0 {
			ch := data[i]
			if ch == '\\' && i+1 < len(data) {
				next := data[i+1]
				switch next {
				case 'n':
					raw = append(raw, '\n')
				case 'r':
					raw = append(raw, '\r')
				case 't':
					raw = append(raw, '\t')
				case 'b':
					raw = append(raw, '\b')
				case 'f':
					raw = append(raw, '\f')
				case '(', ')', '\\':
					raw = append(raw, next)
				default:
					if next >= '0' && next <= '7' {
						val := 0
						n := 0
						j := i + 1
						for j < len(data) && n < 3 && data[j] >= '0' && data[j] <= '7' {
							val = val*8 + int(data[j]-'0')
							j++
							n++
						}
						raw = append(raw, byte(val))
						i = j - 2
					} else {
						raw = append(raw, next)
					}
				}
				i += 2
				continue
			}
			if ch == '(' {
				depth++
			} else if ch == ')' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
			raw = append(raw, ch)
			i++
		}
		return pdfToken{IsString: true, Strings: []string{pdfDecodeTextBytes(raw)}}, i
	case c == '<':
		if i+1 < len(data) && data[i+1] == '<' {
			depth := 0
			for i < len(data)-1 {
				if data[i] == '<' && data[i+1] == '<' {
					depth++
					i += 2
					continue
				}
				if data[i] == '>' && data[i+1] == '>' {
					depth--
					i += 2
					if depth <= 0 {
						break
					}
					continue
				}
				i++
			}
			return pdfToken{Op: "dict"}, i
		}
		raw := []byte{}
		i++
		high := -1
		for i < len(data) && data[i] != '>' {
			b := data[i]
			v := -1
			switch {
			case b >= '0' && b <= '9':
				v = int(b - '0')
			case b >= 'a' && b <= 'f':
				v = int(b-'a') + 10
			case b >= 'A' && b <= 'F':
				v = int(b-'A') + 10
			}
			if v >= 0 {
				if high < 0 {
					high = v
				} else {
					raw = append(raw, byte(high<<4|v))
					high = -1
				}
			}
			i++
		}
		if i < len(data) {
			i++
		}
		if high >= 0 {
			raw = append(raw, byte(high<<4))
		}
		return pdfToken{IsString: true, Strings: []string{pdfDecodeTextBytes(raw)}}, i
	case c == '[':
		i++
		var pieces []pdfPiece
		var lastNumber = math.NaN()
		var texts []string
		for i < len(data) && data[i] != ']' {
			if isPDFSpace(data[i]) {
				i++
				continue
			}
			ch := data[i]
			if ch == '(' || ch == '<' {
				tok, next := pdfLexToken(data, i)
				i = next
				if len(tok.Strings) > 0 {
					pieces = append(pieces, pdfPiece{Text: tok.Strings[0], Kern: lastNumber})
					texts = append(texts, tok.Strings[0])
				}
				lastNumber = math.NaN()
				continue
			}
			if ch == '-' || ch == '+' || ch == '.' || (ch >= '0' && ch <= '9') {
				start := i
				i++
				for i < len(data) && (data[i] == '.' || data[i] == '-' || data[i] == '+' || data[i] == 'e' || data[i] == 'E' || (data[i] >= '0' && data[i] <= '9')) {
					i++
				}
				if v, ok := parsePDFNumber(string(data[start:i])); ok {
					lastNumber = v
				}
				continue
			}
			i++
		}
		if i < len(data) {
			i++
		}
		return pdfToken{IsArray: true, Pieces: pieces, Strings: texts}, i
	case c == '/':
		i++
		for i < len(data) && !isPDFSpace(data[i]) && !isPDFDelim(data[i]) {
			i++
		}
		return pdfToken{Op: "/name"}, i
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		start := i
		i++
		for i < len(data) && (data[i] == '.' || data[i] == '-' || data[i] == '+' || data[i] == 'e' || data[i] == 'E' || (data[i] >= '0' && data[i] <= '9')) {
			i++
		}
		v, _ := parsePDFNumber(string(data[start:i]))
		return pdfToken{IsNumber: true, Numbers: []float64{v}}, i
	case c == '\'' || c == '"':
		return pdfToken{Op: string(c)}, i + 1
	case c == '*':
		// The text-positioning operator is a bare `*`.
		if i+1 < len(data) && data[i+1] == '*' {
			return pdfToken{Op: "**"}, i + 2
		}
		return pdfToken{Op: "*"}, i + 1
	default:
		start := i
		for i < len(data) && !isPDFSpace(data[i]) && !isPDFDelim(data[i]) {
			i++
		}
		if start == i {
			i++
			return pdfToken{}, i
		}
		return pdfToken{Op: string(data[start:i])}, i
	}
}

func parsePDFNumber(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// pdfDecodeTextBytes turns a PDF string's bytes into text. Simple fonts are
// single-byte (WinAnsi in practice); a UTF-16BE string starts with a BOM, which
// some writers emit even without a Unicode font.
func pdfDecodeTextBytes(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		var sb strings.Builder
		for i := 2; i+1 < len(raw); i += 2 {
			sb.WriteRune(rune(uint16(raw[i])<<8 | uint16(raw[i+1])))
		}
		return sb.String()
	}
	var sb strings.Builder
	for _, b := range raw {
		switch {
		case b == '\t':
			sb.WriteByte(' ')
		case b < 0x20:
			// Control bytes are positioning noise, not characters.
		case b < 0x7f:
			sb.WriteByte(b)
		default:
			sb.WriteRune(winAnsiRune(b))
		}
	}
	return sb.String()
}

// winAnsiRune maps the 0x80–0x9F band, which differs from Latin-1, and passes
// the rest of the upper half through as Latin-1.
func winAnsiRune(b byte) rune {
	switch b {
	case 0x80:
		return '€'
	case 0x82:
		return '‚'
	case 0x83:
		return 'ƒ'
	case 0x84:
		return '„'
	case 0x85:
		return '…'
	case 0x86:
		return '†'
	case 0x87:
		return '‡'
	case 0x88:
		return 'ˆ'
	case 0x89:
		return '‰'
	case 0x8a:
		return 'Š'
	case 0x8b:
		return '‹'
	case 0x8c:
		return 'Œ'
	case 0x8e:
		return 'Ž'
	case 0x91:
		return '‘'
	case 0x92:
		return '’'
	case 0x93:
		return '“'
	case 0x94:
		return '”'
	case 0x95:
		return '•'
	case 0x96:
		return '–'
	case 0x97:
		return '—'
	case 0x98:
		return '˜'
	case 0x99:
		return '™'
	case 0x9a:
		return 'š'
	case 0x9b:
		return '›'
	case 0x9c:
		return 'œ'
	case 0x9e:
		return 'ž'
	case 0x9f:
		return 'Ÿ'
	}
	return rune(b)
}

// pdfTextFromContent walks one content stream and returns its visible text.
// Text-positioning operators become line breaks, TJ kerning above the word-space
// threshold becomes a space, and everything else (graphics, colours, paths) is
// ignored.
func pdfTextFromContent(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	var sb strings.Builder
	var operands []pdfToken
	atLineStart := true
	lastY := math.NaN()
	space := func() {
		if sb.Len() == 0 || atLineStart {
			return
		}
		s := sb.String()
		if len(s) == 0 || s[len(s)-1] == ' ' || s[len(s)-1] == '\n' {
			return
		}
		sb.WriteByte(' ')
	}
	newline := func() {
		if sb.Len() == 0 {
			return
		}
		s := sb.String()
		if s[len(s)-1] != '\n' {
			sb.WriteByte('\n')
		}
		atLineStart = true
	}
	writeText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		sb.WriteString(text)
		atLineStart = false
	}
	// Numbers are collected as separate operand tokens; Td/TD/Tm need the last
	// two or six in order, so gather them from the tail of the operand list.
	gatherNumbers := func() []float64 {
		var nums []float64
		for i := len(operands) - 1; i >= 0; i-- {
			if operands[i].IsNumber {
				nums = append([]float64{operands[i].Numbers[0]}, nums...)
				continue
			}
			if operands[i].IsString || operands[i].IsArray {
				break
			}
		}
		return nums
	}
	lastString := func() string {
		for i := len(operands) - 1; i >= 0; i-- {
			if operands[i].IsString && len(operands[i].Strings) > 0 {
				return operands[i].Strings[0]
			}
			if operands[i].IsArray {
				return strings.Join(operands[i].Strings, "")
			}
		}
		return ""
	}
	lastArray := func() []pdfPiece {
		for i := len(operands) - 1; i >= 0; i-- {
			if operands[i].IsArray {
				return operands[i].Pieces
			}
		}
		return nil
	}

	i := 0
	for i < len(content) {
		tok, next := pdfLexToken(content, i)
		if next <= i {
			next = i + 1
		}
		i = next
		if tok.Op == "" {
			if len(operands) < 16 {
				operands = append(operands, tok)
			}
			continue
		}
		switch tok.Op {
		case "Tj":
			writeText(lastString())
		case "TJ":
			for _, piece := range lastArray() {
				if !math.IsNaN(piece.Kern) && piece.Kern < -180 {
					space()
				}
				writeText(piece.Text)
			}
		case "'", "\"":
			newline()
			writeText(lastString())
		case "Td", "TD":
			nums := gatherNumbers()
			if len(nums) >= 2 {
				if nums[1] != 0 {
					newline()
				} else if nums[0] > 1.5 {
					space()
				}
			}
		case "T*":
			newline()
		case "Tm":
			nums := gatherNumbers()
			if len(nums) >= 6 {
				y := nums[5]
				if !math.IsNaN(lastY) && y != lastY {
					newline()
				}
				lastY = y
			}
		case "BT":
			lastY = math.NaN()
		case "ET", "q", "Q", "cm", "re", "f", "S", "Do", "gs", "rg", "RG", "g", "k", "K":
			// No text: nothing to append.
		}
		operands = operands[:0]
	}

	// Collapse runs of blank lines and trailing spaces, then drop leading and
	// trailing whitespace from the page.
	var lines []string
	for _, line := range strings.Split(sb.String(), "\n") {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ── Opening and metadata ───────────────────────────────────────────────────

// pdfExtractTextFromBytes is the PDF text parser, kept separate from any file
// read so the extractor tests can feed it bytes.
func pdfExtractTextFromBytes(data []byte, path string) pdfDoc {
	doc := pdfDoc{Path: path, Format: "PDF", Loaded: true}
	// The full reader (pdfv_doc.go) first: it unpacks object streams, follows
	// fonts' ToUnicode maps and opens files protected without a password,
	// which is most of what this extractor read as "no text".
	if pages, title, author, err := pdfvExtract(data); err == nil {
		for _, text := range pages {
			if strings.TrimSpace(text) != "" {
				doc.Title, doc.Author = title, author
				doc.Pages = make([]pdfPage, 0, len(pages))
				for _, t := range pages {
					doc.Pages = append(doc.Pages, pdfPage{Text: t, Width: 612, Height: 792})
				}
				doc.NumPages = len(doc.Pages)
				if doc.Title == "" {
					doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
				}
				return doc
			}
		}
	}
	if m := rePDFTitle().FindSubmatch(data); m != nil {
		doc.Title = strings.TrimSpace(pdfDecodeTextBytes(pdfUnescapeLiteral(m[1])))
	}
	if m := rePDFAuthor().FindSubmatch(data); m != nil {
		doc.Author = strings.TrimSpace(pdfDecodeTextBytes(pdfUnescapeLiteral(m[1])))
	}

	texts := pdfPageTexts(data)
	if len(texts) == 0 {
		// Fallback: the file's BT…ET text as one page. It is not page-accurate,
		// but it is better than an empty reader for a file we cannot tree-walk.
		whole := pdfFallbackText(data)
		if strings.TrimSpace(whole) != "" {
			texts = readerPaginate(whole, readerPageChars)
		}
	}
	if len(texts) == 0 {
		texts = []string{""}
	}
	doc.Pages = make([]pdfPage, 0, len(texts))
	for _, text := range texts {
		doc.Pages = append(doc.Pages, pdfPage{Text: text, Width: 612, Height: 792})
	}
	doc.NumPages = len(doc.Pages)
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return doc
}

// pdfUnescapeLiteral undoes the escapes inside a PDF literal string before it is
// decoded as text.
func pdfUnescapeLiteral(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			out = append(out, raw[i])
			continue
		}
		i++
		switch raw[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		default:
			out = append(out, raw[i])
		}
	}
	return out
}

// pdfFallbackText is the pre-page-tree extractor: every BT…ET block in object
// order. Kept for files whose page contents are not reachable at the object
// level, and used only when pdfPageTexts found nothing.
func pdfFallbackText(data []byte) string {
	var texts []string
	upper := data
	i := 0
	for i < len(upper)-2 {
		if upper[i] == 'B' && upper[i+1] == 'T' {
			j := i + 2
			for j < len(upper)-1 {
				if upper[j] == 'E' && upper[j+1] == 'T' {
					text := pdfTextFromContent(upper[i : j+2])
					if strings.TrimSpace(text) != "" {
						texts = append(texts, text)
					}
					i = j + 2
					break
				}
				j++
			}
			if j >= len(upper)-1 {
				break
			}
			continue
		}
		i++
	}
	return strings.Join(texts, "\n")
}

// pdfChapterForPage returns the chapter containing a page and its index. A
// document without chapters (a PDF) returns an empty title and -1.
func pdfChapterForPage(page int) (pdfChapter, int) {
	for i := len(pdf.doc.Chapters) - 1; i >= 0; i-- {
		if page >= pdf.doc.Chapters[i].FirstPage {
			return pdf.doc.Chapters[i], i
		}
	}
	return pdfChapter{}, -1
}
