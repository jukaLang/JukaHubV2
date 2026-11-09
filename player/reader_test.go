package main

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildTestPDF writes a two-page PDF whose content streams are Flate-compressed
// and whose pages live in a real page tree, so the extractor cannot succeed by
// reading the objects in file order.
func buildTestPDF(t *testing.T) []byte {
	t.Helper()
	stream := func(text string) []byte {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		content := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
		if _, err := zw.Write([]byte(content)); err != nil {
			t.Fatalf("compress: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("compress close: %v", err)
		}
		return buf.Bytes()
	}

	one := stream("Page one text")
	two := stream("Page two text")

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	obj := func(num int, body string) {
		pdf.WriteString(strconv.Itoa(num) + " 0 obj\n" + body + "\nendobj\n")
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>")
	pdf.WriteString("4 0 obj\n<< /Length " + strconv.Itoa(len(one)) + " /Filter /FlateDecode >>\nstream\n")
	pdf.Write(one)
	pdf.WriteString("\nendstream\nendobj\n")
	obj(6, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 7 0 R >>")
	pdf.WriteString("7 0 obj\n<< /Length " + strconv.Itoa(len(two)) + " /Filter /FlateDecode >>\nstream\n")
	pdf.Write(two)
	pdf.WriteString("\nendstream\nendobj\n")
	pdf.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return pdf.Bytes()
}

func TestPDFExtractsTextPerPage(t *testing.T) {
	doc := pdfExtractTextFromBytes(buildTestPDF(t), "test.pdf")
	if doc.Error != "" {
		t.Fatalf("unexpected error: %s", doc.Error)
	}
	if len(doc.Pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(doc.Pages))
	}
	if !strings.Contains(doc.Pages[0].Text, "Page one text") {
		t.Errorf("page 1 = %q, want its own text", doc.Pages[0].Text)
	}
	if strings.Contains(doc.Pages[0].Text, "Page two") {
		t.Error("page 1 must not show page 2's text")
	}
	if !strings.Contains(doc.Pages[1].Text, "Page two text") {
		t.Errorf("page 2 = %q, want its own text", doc.Pages[1].Text)
	}
	if strings.Contains(doc.Pages[1].Text, "Page one") {
		t.Error("page 2 must not show page 1's text")
	}
}

func TestPDFTextBlockLineBreaksAndKerning(t *testing.T) {
	content := []byte("BT /F1 12 Tf 72 720 Td (First line) Tj 0 -14 Td [(Word) -280 (gap)] TJ T* (Last line) Tj ET")
	text := pdfTextFromContent(content)
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		t.Fatalf("text = %q, want at least three lines", text)
	}
	if lines[0] != "First line" {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(text, "Word gap") {
		t.Errorf("a TJ word space should be a space, got %q", text)
	}
	if !strings.Contains(text, "Last line") {
		t.Errorf("text = %q, missing the T* line", text)
	}
}

func TestPDFLexerDecodesEscapesAndHex(t *testing.T) {
	content := []byte("BT (a\\(b\\)c) Tj <48656C6C6F> Tj ET")
	text := pdfTextFromContent(content)
	if !strings.Contains(text, "a(b)c") {
		t.Errorf("escaped parentheses = %q", text)
	}
	if !strings.Contains(text, "Hello") {
		t.Errorf("hex string = %q", text)
	}
}

func TestPDFFallbackWhenNoPageTree(t *testing.T) {
	// A document with no /Catalog or /Pages still shows its text instead of an
	// empty reader; it is not page-accurate, but it is readable.
	raw := []byte("%PDF-1.4\n1 0 obj\n<< /Length 8 >>\nstream\nBT (Loose text) Tj ET\nendstream\nendobj\n%%EOF\n")
	doc := pdfExtractTextFromBytes(raw, "loose.pdf")
	if doc.Error != "" {
		t.Fatalf("unexpected error: %s", doc.Error)
	}
	joined := ""
	for _, page := range doc.Pages {
		joined += page.Text + "\n"
	}
	if !strings.Contains(joined, "Loose text") {
		t.Fatalf("text = %q, want the loose BT block", joined)
	}
}

// ── EPUB ───────────────────────────────────────────────────────────────────

func buildTestEPUB(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:creator>Ada Writer</dc:creator>
  </metadata>
  <manifest>
    <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`)
	add("OEBPS/ch1.xhtml", `<html><head><title>First Chapter</title></head><body>
<h1>First Chapter</h1>
<p>The first paragraph of the book.</p>
<p>The second paragraph. It has &amp; an entity.</p>
</body></html>`)
	add("OEBPS/ch2.xhtml", `<html><head></head><body>
<h1>Second Chapter</h1>
<p>Another chapter, with <em>emphasis</em> and a little text.</p>
</body></html>`)
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestEPUBParsesSpineIntoChapters(t *testing.T) {
	doc := epubParseBytes(buildTestEPUB(t), "book.epub")
	if doc.Error != "" {
		t.Fatalf("unexpected error: %s", doc.Error)
	}
	if doc.Title != "Test Book" || doc.Author != "Ada Writer" {
		t.Fatalf("metadata = %q by %q", doc.Title, doc.Author)
	}
	if len(doc.Chapters) != 2 {
		t.Fatalf("chapters = %d, want 2", len(doc.Chapters))
	}
	if doc.Chapters[0].Title != "First Chapter" || doc.Chapters[1].Title != "Second Chapter" {
		t.Errorf("chapter titles = %q, %q", doc.Chapters[0].Title, doc.Chapters[1].Title)
	}
	if doc.Chapters[0].FirstPage != 1 || doc.Chapters[1].FirstPage != 2 {
		t.Errorf("chapter pages = %d, %d", doc.Chapters[0].FirstPage, doc.Chapters[1].FirstPage)
	}
	if doc.NumPages != len(doc.Pages) || doc.NumPages < 2 {
		t.Fatalf("numPages = %d, pages = %d", doc.NumPages, len(doc.Pages))
	}
	first := doc.Pages[0].Text
	if !strings.Contains(first, "The first paragraph of the book.") {
		t.Errorf("chapter 1 = %q", first)
	}
	if !strings.Contains(first, "It has & an entity.") {
		t.Errorf("entities should be decoded: %q", first)
	}
	if strings.Contains(first, "<") || strings.Contains(first, "&amp;") {
		t.Errorf("markup leaked into text: %q", first)
	}
	// Paragraph boundaries become line breaks rather than one run-on line.
	if !strings.Contains(first, "\n") {
		t.Errorf("paragraphs should be separate lines: %q", first)
	}
}

func TestEPUBRejectsNonArchive(t *testing.T) {
	doc := epubParseBytes([]byte("not a zip"), "broken.epub")
	if doc.Error == "" {
		t.Fatal("a non-archive EPUB must report why it cannot open")
	}
	if !doc.Loaded {
		t.Error("a failed open is still a loaded state with an error to show")
	}
}

func TestEPUBOpenFileGuardsExplorerRoot(t *testing.T) {
	// A path outside the explorer root must not be read, even if it exists.
	dir := t.TempDir()
	outside := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(outside, buildTestEPUB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := epubOpenFile(outside)
	if doc.Error == "" {
		t.Fatal("a file outside the explorer root must be refused")
	}
}
