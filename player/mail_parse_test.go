package main

// mail_parse_test.go — the Mail app's parsers of outside data: IMAP
// responses (mail_imap.go) and MIME messages (mail_mime.go).

import (
	"strings"
	"testing"
)

func TestImapParseResp(t *testing.T) {
	// A FETCH with a literal: readResp hands the text before "{n}" and the
	// literal's bytes separately.
	r, err := imapParseResp([]string{`* 12 FETCH (UID 34 FLAGS (\Seen) BODY[HEADER.FIELDS (FROM)] `, ")"},
		[][]byte{[]byte("From: a@b.c\r\n\r\n")})
	if err != nil || r.tag != "*" || r.num != 12 || r.kind != "FETCH" {
		t.Fatalf("fetch: %+v %v", r, err)
	}
	items := imapFetchItems(r)
	if items["UID"].num() != 34 || !imapHasFlag(imapFlags(items["FLAGS"]), `\Seen`) || items["HEADER"].str() != "From: a@b.c\r\n\r\n" {
		t.Fatalf("fetch items: %+v", items)
	}

	r, err = imapParseResp([]string{`* LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"`}, nil)
	if err != nil || r.kind != "LIST" || len(r.fields) != 3 || r.fields[2].str() != "[Gmail]/Trash" || !imapHasFlag(imapFlags(r.fields[0]), `\trash`) {
		t.Fatalf("list: %+v %v", r, err)
	}

	r, err = imapParseResp([]string{`* LIST (\Noselect) NIL "Foo \"bar\" \\ baz"`}, nil)
	if err != nil || r.fields[1].kind != imapNil || r.fields[2].str() != `Foo "bar" \ baz` {
		t.Fatalf("nil and escapes: %+v %v", r, err)
	}

	r, err = imapParseResp([]string{`J1 NO [AUTHENTICATIONFAILED] Invalid credentials (Failure`}, nil)
	if err != nil || r.tag != "J1" || r.status != "NO" || r.code != "AUTHENTICATIONFAILED" || r.text != "Invalid credentials (Failure" {
		t.Fatalf("tagged status: %+v %v", r, err)
	}

	r, err = imapParseResp([]string{`* OK [UIDVALIDITY 3857529045] UIDs valid`}, nil)
	if err != nil || r.status != "OK" || r.code != "UIDVALIDITY 3857529045" {
		t.Fatalf("untagged status: %+v %v", r, err)
	}

	r, err = imapParseResp([]string{`* SEARCH 4 7 9`}, nil)
	if err != nil || r.kind != "SEARCH" || len(r.fields) != 3 || r.fields[2].num() != 9 {
		t.Fatalf("search: %+v %v", r, err)
	}

	r, err = imapParseResp([]string{`* 3 FETCH (UID 5 FLAGS ((\Seen)))`}, nil)
	if err != nil || len(r.fields) != 1 || len(r.fields[0].list) != 4 || len(r.fields[0].list[3].list) != 1 {
		t.Fatalf("nested: %+v %v", r, err)
	}

	for _, bad := range []string{`* LIST (\Foo "/" x`, `* SEARCH "abc`, `* FLAGS a)`} {
		if _, err := imapParseResp([]string{bad}, nil); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}

	if n, at, ok := imapLiteralSuffix("A1 LOGIN {12}"); !ok || n != 12 || at != 9 {
		t.Errorf("literal suffix: %d %d %v", n, at, ok)
	}
	if _, _, ok := imapLiteralSuffix(`* OK done`); ok {
		t.Error("no literal expected")
	}
	if got := imapUIDSet([]uint32{9, 4, 7, 8, 12}); got != "4,7:9,12" {
		t.Errorf("uid set: %q", got)
	}
	if q, ok := imapQuote(`a"b\c`); !ok || q != `"a\"b\\c"` {
		t.Errorf("quote: %q %v", q, ok)
	}
	if _, ok := imapQuote("päss"); ok {
		t.Error("non-ASCII must go as a literal")
	}
}

func TestMailDecode(t *testing.T) {
	headers := []struct{ in, want string }{
		{"=?utf-8?q?Caf=C3=A9_ol=C3=A9?=", "Café olé"},
		{"=?windows-1252?Q?=93Hi=94_=80?=", "“Hi” €"},
		{"=?iso-8859-15?q?=A4?=", "€"},
		{"Plain\tsubject\r\n  folded", "Plain subject folded"},
		{"evil\u202eright", "evilright"},
	}
	for _, h := range headers {
		if got := mailDecodeHeader(h.in); got != h.want {
			t.Errorf("header %q: got %q, want %q", h.in, got, h.want)
		}
	}
	if got := mailDecodeCharset("koi8-r", []byte{0xF0, 0xD2, 0xC9, 0xD7, 0xC5, 0xD4}); got != "Привет" {
		t.Errorf("koi8-r: %q", got)
	}

	qp := "Content-Type: text/plain; charset=windows-1252\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nPrix: 5=80 soft=\r\nbreak\r\n"
	if b := mailParseBody([]byte(qp), false); b.Text != "Prix: 5€ softbreak" || b.FromHTML || len(b.Attachments) != 0 {
		t.Errorf("quoted-printable: %+v", b)
	}

	mixed := "Content-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHi there\r\n" +
		"--XX\r\nContent-Type: text/plain; name=\"a.txt\"\r\nContent-Disposition: attachment; filename=\"a.txt\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\naGVs\r\nbG8=\r\n--XX--\r\n"
	b := mailParseBody([]byte(mixed), false)
	if b.Text != "Hi there" || len(b.Attachments) != 1 || b.Attachments[0].Name != "a.txt" || b.Attachments[0].Size != 5 {
		t.Errorf("multipart: %+v", b)
	}
	if data, ok := mailExtractAttachment([]byte(mixed), 0); !ok || string(data) != "hello" {
		t.Errorf("attachment: %q %v", data, ok)
	}

	html := "Content-Type: text/html; charset=utf-8\r\n\r\n<html><body><p>Hello <b>World</b></p><p>Bye</p></body></html>"
	if b := mailParseBody([]byte(html), false); b.Text != "Hello World\nBye" || !b.FromHTML || !strings.Contains(b.HTML, "<b>World</b>") {
		t.Errorf("html: %+v", b)
	}
}

func TestMailSafeHTML(t *testing.T) {
	in := `<p onclick="evil()">Hi <a href="javascript:evil()">there</a></p>` +
		`<script>alert(1)</script><iframe src="https://evil/"></iframe>` +
		`<img src="https://tracker/pixel"><div style="background:url(https://tracker/bg)">x</div>`
	got := mailSafeHTML(in, false)
	for _, banned := range []string{"<script", "onclick", "javascript:", "<iframe", "<img", "tracker/pixel", "tracker/bg"} {
		if strings.Contains(got, banned) {
			t.Errorf("unsafe HTML kept %q: %s", banned, got)
		}
	}
	if !strings.Contains(got, "Hi") || !strings.Contains(got, "there") {
		t.Errorf("safe text lost: %s", got)
	}
	withImages := mailSafeHTML(`<img src="https://example.com/a.png">`, true)
	if !strings.Contains(withImages, "<img") {
		t.Errorf("Show images dropped the picture: %s", withImages)
	}
}
