package main

// mail_mime.go — turning a fetched message into what the Mail app shows
// (docs/EMAIL_CALENDAR.md).
//
// A message is walked as a MIME tree with the standard library (net/mail,
// mime/multipart, quoted-printable, base64): the first text/plain part is the
// body; a message with only HTML is flattened to text by the e-book reader's
// HTML pass (epubHTMLText). Everything that is not body text is listed as an
// attachment with its name, type and size, and one can be saved to Downloads.
//
// Charsets: UTF-8 and ASCII as they are; Latin-1 read as Windows-1252, the
// way browsers do, since mail labelled ISO-8859-1 is usually 1252, and so is
// unlabelled (or "ASCII") text that is not valid UTF-8; Latin-9,
// KOI8-R and Windows-1251 from small tables. Anything else is read as UTF-8
// when it is valid UTF-8 and otherwise with replacement characters — enough
// to see who wrote and what about.
//
// Every string bound for the screen passes mailClean or mailCleanText, which
// drop control and bidi-override characters (a subject must not be able to
// reorder the line it is drawn on). Bodies past 2 MB were never fetched
// (mailBodyCap) and are marked as cut.

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// mailBodyCap is the most of one message JukaHub fetches.
	mailBodyCap = 2 << 20
	// mailTextCap is the most body text the reader keeps.
	mailTextCap  = 512 << 10
	mailMaxParts = 200
	mailMaxDepth = 8
)

// ── Charsets ────────────────────────────────────────────────────────────

// mailCharsetReader is the decoders' hook for charsets the standard library
// does not know.
func mailCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(io.LimitReader(input, mailBodyCap))
	if err != nil {
		return nil, err
	}
	return strings.NewReader(mailDecodeCharset(charset, data)), nil
}

// mailWordDecoder decodes RFC 2047 encoded words with mailCharsetReader.
var mailWordDecoder = &mime.WordDecoder{CharsetReader: mailCharsetReader}

// mailTable is the upper half (0x80–0xFF) of a single-byte charset.
type mailTable [128]rune

// mailLatin1Table is ISO-8859-1's upper half: each byte is its code point.
func mailLatin1Table() mailTable {
	var t mailTable
	for i := range t {
		t[i] = rune(0x80 + i)
	}
	return t
}

var (
	mailCP1252 = sync.OnceValue(func() *mailTable {
		t := mailLatin1Table()
		// 0x80–0x9F; the five holes become replacement characters.
		hi := [32]rune{
			0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
			0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
			0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
			0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178,
		}
		copy(t[:32], hi[:])
		return &t
	})
	mailLatin9 = sync.OnceValue(func() *mailTable {
		t := mailLatin1Table()
		for b, r := range map[byte]rune{0xA4: 0x20AC, 0xA6: 0x0160, 0xA8: 0x0161, 0xB4: 0x017D, 0xB8: 0x017E, 0xBC: 0x0152, 0xBD: 0x0153, 0xBE: 0x0178} {
			t[b-0x80] = r
		}
		return &t
	})
	mailKOI8R = sync.OnceValue(func() *mailTable {
		var t mailTable
		for i := range t {
			t[i] = 0xFFFD // box drawing: nothing a mail needs
		}
		t[0x9A-0x80], t[0xA3-0x80], t[0xB3-0x80] = 0x00A0, 'ё', 'Ё'
		lower := []rune("юабцдефгхийклмнопярстужвьызшэщчъ")
		upper := []rune("ЮАБЦДЕФГХИЙКЛМНОПЯРСТУЖВЬЫЗШЭЩЧЪ")
		copy(t[0x40:0x60], lower)
		copy(t[0x60:0x80], upper)
		return &t
	})
	mailCP1251 = sync.OnceValue(func() *mailTable {
		var t mailTable
		for i := 0; i < 64; i++ {
			t[0x40+i] = rune(0x0410 + i) // А … я
		}
		// 0x80–0xBF; 0x98 is the one hole.
		hi := [64]rune{
			0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021,
			0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
			0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
			0xFFFD, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
			0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7,
			0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
			0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7,
			0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
		}
		copy(t[:64], hi[:])
		return &t
	})
)

// mailDecodeTable reads single-byte text through a table.
func mailDecodeTable(b []byte, t *mailTable) string {
	var out strings.Builder
	out.Grow(len(b) + len(b)/4)
	for _, c := range b {
		if c < 0x80 {
			out.WriteByte(c)
		} else {
			out.WriteRune(t[c-0x80])
		}
	}
	return out.String()
}

// mailDecodeCharset turns bytes in a named charset into UTF-8. Pure.
func mailDecodeCharset(charset string, b []byte) string {
	cs := strings.ToLower(strings.Trim(strings.TrimSpace(charset), `"'`))
	switch cs {
	case "utf-8", "utf8", "unicode-1-1-utf-8":
		return strings.ToValidUTF8(string(b), "�")
	case "", "us-ascii", "ascii", "ansi_x3.4-1968":
		// No charset, or ASCII with 8-bit bytes: old mail programs wrote
		// Latin-1 that way.
		if utf8.Valid(b) {
			return string(b)
		}
		return mailDecodeTable(b, mailCP1252())
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1", "windows-1252", "cp1252", "x-cp1252":
		return mailDecodeTable(b, mailCP1252())
	case "iso-8859-15", "iso8859-15", "iso_8859-15", "latin-9", "latin9":
		return mailDecodeTable(b, mailLatin9())
	case "koi8-r", "koi8r", "koi8-u":
		return mailDecodeTable(b, mailKOI8R())
	case "windows-1251", "cp1251", "x-cp1251":
		return mailDecodeTable(b, mailCP1251())
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// ── Cleaning ────────────────────────────────────────────────────────────

// mailDropRune reports characters no mail string may draw: controls and the
// bidi overrides and isolates that reorder text around them.
func mailDropRune(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0xFEFF:
		return true
	}
	return unicode.IsControl(r)
}

// mailClean is a header value made safe for one line: controls removed,
// whitespace collapsed, at most max characters (0 = no limit).
func mailClean(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if mailDropRune(r) {
			continue
		}
		if max > 0 && n >= max {
			b.WriteString("…")
			return b.String()
		}
		if space {
			b.WriteByte(' ')
			space = false
			n++
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// mailCleanText is a body made safe to draw: line breaks kept, tabs as
// spaces, other controls dropped, trailing spaces trimmed, runs of blank
// lines folded into one, capped at mailTextCap.
func mailCleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if len(s) > mailTextCap {
		cut := mailTextCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "\n\n[The rest of this message is too long to show here.]"
	}
	var out strings.Builder
	out.Grow(len(s))
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		var b strings.Builder
		for _, r := range line {
			switch {
			case r == '\t':
				b.WriteString("    ")
			case r == 0x00A0:
				b.WriteByte(' ')
			case mailDropRune(r):
			default:
				b.WriteRune(r)
			}
		}
		l := strings.TrimRight(b.String(), " ")
		if l == "" {
			blank++
			if blank > 1 || out.Len() == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// ── Headers ─────────────────────────────────────────────────────────────

// mailDecodeHeader decodes encoded words and raw 8-bit bytes in a header
// value and cleans it for one line.
func mailDecodeHeader(s string) string {
	if strings.Contains(s, "=?") {
		if d, err := mailWordDecoder.DecodeHeader(s); err == nil {
			s = d
		}
	}
	if !utf8.ValidString(s) {
		s = mailDecodeCharset("windows-1252", []byte(s))
	}
	return mailClean(s, 400)
}

// mailReadHeader parses a header block (as FETCH BODY[HEADER.FIELDS …]
// returns it).
func mailReadHeader(block []byte) mail.Header {
	b := bytes.TrimRight(block, "\r\n")
	buf := make([]byte, 0, len(b)+4)
	buf = append(append(buf, b...), "\r\n\r\n"...)
	msg, err := mail.ReadMessage(bytes.NewReader(buf))
	if err != nil {
		return mail.Header{}
	}
	return msg.Header
}

// mailAddrParser parses addresses with the charset-aware word decoder.
var mailAddrParser = &mail.AddressParser{WordDecoder: mailWordDecoder}

// mailParseAddr is the display name and address of the first address in a
// From/To value. A value net/mail cannot parse becomes the name.
func mailParseAddr(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if list, err := mailAddrParser.ParseList(raw); err == nil && len(list) > 0 {
		return mailClean(list[0].Name, 120), mailClean(list[0].Address, 200)
	}
	return mailDecodeHeader(raw), ""
}

// mailAddrList is every address in a To/Cc value as "Name <addr>" text for
// the reader, decoded and cleaned.
func mailAddrList(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	list, err := mailAddrParser.ParseList(raw)
	if err != nil {
		return mailDecodeHeader(raw)
	}
	parts := make([]string, 0, len(list))
	for _, a := range list {
		name, addr := mailClean(a.Name, 80), mailClean(a.Address, 200)
		if name != "" && addr != "" {
			parts = append(parts, name+" <"+addr+">")
		} else {
			parts = append(parts, name+addr)
		}
	}
	return mailClean(strings.Join(parts, ", "), 600)
}

// ── Bodies ──────────────────────────────────────────────────────────────

// mailAttachment is one part that is not the body.
type mailAttachment struct {
	Name    string
	Type    string
	Size    int
	Partial bool // the message was cut before this part ended
	index   int
}

// mailBody is a message as the reader shows it.
type mailBody struct {
	Text        string
	FromHTML    bool
	Attachments []mailAttachment
	Truncated   bool
}

// mailWalker walks one message's MIME tree.
type mailWalker struct {
	parts     int
	plain     string
	html      string
	havePlain bool
	haveHTML  bool
	atts      []mailAttachment
	want      int
	got       []byte
	truncated bool
}

// mailParseBody reads a fetched message (truncated: it was cut at
// mailBodyCap). Pure: outside data, covered by mail_parse_test.go.
func mailParseBody(raw []byte, truncated bool) mailBody {
	w := mailWalker{want: -1, truncated: truncated}
	w.run(raw)
	b := mailBody{Attachments: w.atts, Truncated: truncated}
	switch {
	case w.havePlain && strings.TrimSpace(w.plain) != "":
		b.Text = w.plain
	case w.haveHTML:
		_, b.Text = epubHTMLText([]byte(w.html))
		b.FromHTML = true
	default:
		b.Text = w.plain
	}
	b.Text = mailCleanText(b.Text)
	if truncated {
		b.Text += "\n\n[This message is larger than 2 MB; only its beginning was downloaded.]"
	}
	return b
}

// mailExtractAttachment returns the decoded bytes of attachment index.
func mailExtractAttachment(raw []byte, index int) ([]byte, bool) {
	w := mailWalker{want: index}
	w.run(raw)
	return w.got, w.got != nil
}

func (w *mailWalker) run(raw []byte) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		// Not a message we can parse: show the bytes as text.
		w.plain, w.havePlain = mailDecodeCharset("", raw), true
		return
	}
	w.walk(textproto.MIMEHeader(msg.Header), msg.Body, 0)
}

func (w *mailWalker) walk(h textproto.MIMEHeader, body io.Reader, depth int) {
	w.parts++
	if w.parts > mailMaxParts || depth > mailMaxDepth {
		return
	}
	mt, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
	if mt == "" {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return
			}
			w.walk(p.Header, p, depth+1)
		}
	}
	disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	name = mailDecodeHeader(name)
	attach := disp == "attachment" || (mt != "text/plain" && mt != "text/html")
	data := mailDecodeTransfer(h.Get("Content-Transfer-Encoding"), body)
	if attach {
		idx := len(w.atts)
		if idx < 100 {
			w.atts = append(w.atts, mailAttachment{Name: setOr(name, "attachment"), Type: mt, Size: len(data), Partial: w.truncated, index: idx})
		}
		if idx == w.want {
			w.got = data
		}
		return
	}
	text := mailDecodeCharset(params["charset"], data)
	if mt == "text/html" {
		if !w.haveHTML {
			w.html, w.haveHTML = text, true
		}
		return
	}
	if !w.havePlain {
		if strings.EqualFold(params["format"], "flowed") {
			text = mailUnflow(text, strings.EqualFold(params["delsp"], "yes"))
		}
		w.plain, w.havePlain = text, true
	}
}

// mailDecodeTransfer undoes a part's Content-Transfer-Encoding, bounded. A
// part cut short keeps what decoded.
func mailDecodeTransfer(cte string, r io.Reader) []byte {
	r = io.LimitReader(r, mailBodyCap+1)
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		src, _ := io.ReadAll(r)
		clean := src[:0]
		for _, c := range src {
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
				clean = append(clean, c)
			}
		}
		clean = clean[:len(clean)-len(clean)%4]
		dst := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, _ := base64.StdEncoding.Decode(dst, clean)
		return dst[:n]
	case "quoted-printable":
		data, _ := io.ReadAll(quotedprintable.NewReader(r))
		return data
	}
	data, _ := io.ReadAll(r)
	return data
}

// mailUnflow joins format=flowed soft line breaks (RFC 3676).
func mailUnflow(s string, delsp bool) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var b strings.Builder
	b.Grow(len(s))
	for i, line := range lines {
		soft := strings.HasSuffix(line, " ") && line != "-- " && i < len(lines)-1
		if soft {
			if delsp {
				line = line[:len(line)-1]
			}
			b.WriteString(line)
			continue
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// mailSnippet is the first words of a message from its header block and the
// beginning of its text (FETCH BODY.PEEK[TEXT]<0.n>): quoted lines skipped,
// one line of at most 160 characters.
func mailSnippet(header, textPrefix []byte) string {
	h := bytes.TrimRight(header, "\r\n")
	raw := make([]byte, 0, len(h)+4+len(textPrefix))
	raw = append(append(append(raw, h...), "\r\n\r\n"...), textPrefix...)
	w := mailWalker{want: -1, truncated: true}
	w.run(raw)
	text := w.plain
	if !w.havePlain || strings.TrimSpace(text) == "" {
		if w.haveHTML {
			_, text = epubHTMLText([]byte(w.html))
		}
	}
	var parts []string
	total := 0
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, ">") {
			continue
		}
		parts = append(parts, l)
		if total += len(l); total > 400 {
			break
		}
	}
	return mailClean(strings.Join(parts, " "), 160)
}
