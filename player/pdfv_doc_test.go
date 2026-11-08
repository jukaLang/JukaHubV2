package main

// pdfv_doc_test.go — the PDF reader's parser on documents built here, one
// feature at a time: objects packed in an object stream, a composite font that
// is only readable through its ToUnicode map, an RC4-protected file that opens
// without a password, text placement, PNG predictors, ASCII85 and a raw grey
// picture.

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"crypto/rc4"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"testing"
)

// pdfvTestFile assembles a PDF from numbered bodies; a body given as a stream
// gets its dictionary and stream keywords written around it.
type pdfvTestFile struct {
	buf bytes.Buffer
}

func newPDFVTestFile() *pdfvTestFile {
	f := &pdfvTestFile{}
	f.buf.WriteString("%PDF-1.5\n")
	return f
}

func (f *pdfvTestFile) obj(num int, body string) {
	f.buf.WriteString(strconv.Itoa(num) + " 0 obj\n" + body + "\nendobj\n")
}

func (f *pdfvTestFile) stream(num int, dict string, data []byte) {
	f.buf.WriteString(strconv.Itoa(num) + " 0 obj\n<< " + dict + " /Length " + strconv.Itoa(len(data)) + " >>\nstream\n")
	f.buf.Write(data)
	f.buf.WriteString("\nendstream\nendobj\n")
}

func (f *pdfvTestFile) finish(trailer string) []byte {
	f.buf.WriteString("trailer\n" + trailer + "\n%%EOF\n")
	return f.buf.Bytes()
}

func pdfvDeflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPDFVObjectStreamAndToUnicode(t *testing.T) {
	// Objects 2 (the page tree) and 3 (the page) live inside object stream 5,
	// the way Word and Chrome write PDFs; the old extractor never saw them.
	objA := "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>"
	objB := "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>"
	header := "2 0 3 " + strconv.Itoa(len(objA)+1) + " "
	packed := header + objA + " " + objB
	first := len(header)

	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"2 beginbfchar\n<0001> <0048>\n<0002> <0069>\nendbfchar\n" +
		"1 beginbfrange\n<0010> <0012> <0061>\nendbfrange\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end"

	f := newPDFVTestFile()
	f.obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	f.stream(4, "", []byte("BT /F1 12 Tf 100 700 Td <0001000200100011> Tj ET"))
	f.stream(5, "/Type /ObjStm /N 2 /First "+strconv.Itoa(first)+" /Filter /FlateDecode", pdfvDeflate(t, []byte(packed)))
	f.obj(6, "<< /Type /Font /Subtype /Type0 /BaseFont /ABCDEF+Body /Encoding /Identity-H /ToUnicode 7 0 R /DescendantFonts [8 0 R] >>")
	f.stream(7, "", []byte(cmap))
	f.obj(8, "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /ABCDEF+Body /DW 500 /W [1 [600 300]] >>")
	data := f.finish("<< /Root 1 0 R >>")

	texts, _, _, err := pdfvExtract(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 {
		t.Fatalf("pages = %d, want 1", len(texts))
	}
	if texts[0] != "Hiab" {
		t.Fatalf("text = %q, want Hiab (two bfchar codes, two from a bfrange)", texts[0])
	}
}

func TestPDFVOpensRC4FileWithoutPassword(t *testing.T) {
	owner := bytes.Repeat([]byte{0x5a}, 32)
	user := bytes.Repeat([]byte{0x11}, 32)
	id := []byte("0123456789abcdef")
	p := int32(-4)
	// Algorithm 2 (revision 2): MD5 of the padded empty password, O, P and the
	// first file ID; five bytes of key.
	h := md5.New()
	h.Write(pdfvPasswordPad)
	h.Write(owner)
	h.Write([]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)})
	h.Write(id)
	key := h.Sum(nil)[:5]
	objKey := md5.Sum(append(append([]byte(nil), key...), 4, 0, 0, 0, 0))
	content := []byte("BT /F1 11 Tf 50 650 Td (Secret page) Tj ET")
	c, err := rc4.NewCipher(objKey[:10])
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, len(content))
	c.XORKeyStream(enc, content)

	f := newPDFVTestFile()
	f.obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	f.obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	f.obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>")
	f.stream(4, "", enc)
	f.obj(9, "<< /Filter /Standard /V 1 /R 2 /O <"+hex.EncodeToString(owner)+"> /U <"+hex.EncodeToString(user)+"> /P -4 >>")
	data := f.finish("<< /Root 1 0 R /Encrypt 9 0 R /ID [<" + hex.EncodeToString(id) + "> <" + hex.EncodeToString(id) + ">] >>")

	texts, _, _, err := pdfvExtract(data)
	if err != nil {
		t.Fatalf("a password-free protected PDF was refused: %v", err)
	}
	if len(texts) != 1 || texts[0] != "Secret page" {
		t.Fatalf("text = %q, want the decrypted page", texts)
	}

	// A file that needs an AES-256 password is reported as locked.
	g := newPDFVTestFile()
	g.obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	g.obj(9, "<< /Filter /Standard /V 5 /R 6 /O <00> /U <00> /P -4 >>")
	if _, _, _, err := pdfvExtract(g.finish("<< /Root 1 0 R /Encrypt 9 0 R >>")); err != errPDFVLocked {
		t.Fatalf("an AES-256 file returned %v, want errPDFVLocked", err)
	}
}

func TestPDFVPlacesTextRuns(t *testing.T) {
	f := newPDFVTestFile()
	f.obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	f.obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 600 800] >>")
	f.obj(3, "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>")
	f.stream(4, "", []byte("0 0 1 rg 10 20 30 40 re f "+
		"BT /F1 10 Tf 1 0 0 1 50 600 Tm [(Hel) -20 (lo)] TJ ( world) Tj 0 -14 Td (Next line) Tj ET"))
	file, err := pdfvOpenBytes(f.finish("<< /Root 1 0 R >>"))
	if err != nil {
		t.Fatal(err)
	}
	refs := file.pages()
	if len(refs) != 1 || refs[0].box != [4]float64{0, 0, 600, 800} {
		t.Fatalf("pages = %+v, want one with the inherited box", refs)
	}
	page := file.loadPage(refs[0])
	var texts []pdfvItem
	fills := 0
	for _, item := range page.items {
		switch item.kind {
		case pdfvItemText:
			texts = append(texts, item)
		case pdfvItemFill:
			fills++
			if item.color != [3]uint8{0, 0, 255} {
				t.Errorf("the rectangle's colour is %v, want blue", item.color)
			}
		}
	}
	if fills != 1 {
		t.Fatalf("fills = %d, want the one rectangle", fills)
	}
	if len(texts) != 2 {
		t.Fatalf("runs = %d (%+v), want two lines", len(texts), texts)
	}
	if texts[0].text != "Hello world" || math.Abs(texts[0].x-50) > 0.01 || math.Abs(texts[0].y-600) > 0.01 || math.Abs(texts[0].size-10) > 0.01 {
		t.Fatalf("first run = %+v, want \"Hello world\" at 50,600 size 10", texts[0])
	}
	if texts[1].text != "Next line" || math.Abs(texts[1].y-586) > 0.01 {
		t.Fatalf("second run = %+v, want \"Next line\" 14 points lower", texts[1])
	}
	if page.text != "Hello world\nNext line" {
		t.Fatalf("page text = %q", page.text)
	}
}

func TestPDFVFiltersAndPredictor(t *testing.T) {
	if got := string(pdfvASCII85([]byte(`87cURD]i,"Ebo7~>`))); got != "Hello World" {
		t.Fatalf("ASCII85 = %q", got)
	}
	if got := string(pdfvASCIIHex([]byte("48 65 6c6C 6f>"))); got != "Hello" {
		t.Fatalf("ASCIIHex = %q", got)
	}
	if got := string(pdfvRunLength([]byte{2, 'a', 'b', 'c', 254, 'z', 128})); got != "abczzz" {
		t.Fatalf("RunLength = %q", got)
	}
	file := &pdfvFile{objs: map[int]*pdfvIndirect{}}
	parms := map[string]pdfvObj{
		"Predictor": {kind: pdfvNum, num: 12},
		"Columns":   {kind: pdfvNum, num: 3},
	}
	// Row 1 "None", row 2 "Up": each byte adds the one above it.
	out := pdfvUnpredict([]byte{0, 1, 2, 3, 2, 1, 1, 1}, parms, file)
	if !bytes.Equal(out, []byte{1, 2, 3, 2, 3, 4}) {
		t.Fatalf("PNG predictor = %v", out)
	}
}

func TestPDFVDecodesARawGreyImage(t *testing.T) {
	f := newPDFVTestFile()
	f.obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	f.obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>")
	f.obj(3, "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im1 5 0 R >> >> /Contents 4 0 R >>")
	f.stream(4, "", []byte("q 50 0 0 40 10 20 cm /Im1 Do Q"))
	f.stream(5, "/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode",
		pdfvDeflate(t, []byte{0, 255, 128, 64}))
	file, err := pdfvOpenBytes(f.finish("<< /Root 1 0 R >>"))
	if err != nil {
		t.Fatal(err)
	}
	page := file.loadPage(file.pages()[0])
	if len(page.images) != 1 || len(page.items) != 1 || page.items[0].kind != pdfvItemImage {
		t.Fatalf("items = %+v, want one image", page.items)
	}
	item := page.items[0]
	if item.x != 10 || item.y != 20 || item.w != 50 || item.h != 40 {
		t.Fatalf("image box = %v,%v %vx%v, want 10,20 50x40", item.x, item.y, item.w, item.h)
	}
	pic, err := file.decodeImage(page.images[0].num, 64)
	if err != nil {
		t.Fatal(err)
	}
	if pic.Rect.Dx() != 2 || pic.Rect.Dy() != 2 {
		t.Fatalf("decoded %v", pic.Rect)
	}
	if pic.Pix[0] != 0 || pic.Pix[4] != 255 || pic.Pix[8] != 128 || pic.Pix[12] != 64 || pic.Pix[3] != 255 {
		t.Fatalf("pixels = %v", pic.Pix)
	}
}

func TestPDFVRejectsNonPDF(t *testing.T) {
	if _, err := pdfvOpenBytes([]byte("hello")); err == nil {
		t.Fatal("five bytes of text opened as a PDF")
	}
	if _, _, _, err := pdfvExtract([]byte(strings.Repeat("not a pdf ", 20))); err == nil {
		t.Fatal("text without a single object opened as a PDF")
	}
}
