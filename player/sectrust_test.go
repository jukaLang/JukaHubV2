package main

// Tests for Security & trust's SECURITY.md reader: the Markdown the reader
// understands, where the document is found (and the built-in summary when it
// is not), and that every "Read more" button on a posture row lands on a
// section the shipped document really has.

import (
	"strings"
	"testing"
)

func TestSectrustParseMarkdown(t *testing.T) {
	src := "# Title\r\n\r\n" +
		"Intro line one\ncontinues here.\n\n" +
		"## Section A\n" +
		"- first **bold** item\n" +
		"  continued\n" +
		"  - nested [link](https://example.com/x)\n" +
		"1. numbered\n\n" +
		"```\ncode line\n\tindented\n```\n\n" +
		"| Col | Two |\n|---|:---:|\n| a | `b` |\n\n" +
		"---\n" +
		"> quoted <br> text\n" +
		"### Small\n" +
		"<!-- hidden -->Visible\n"
	want := []sectrustMDBlock{
		{kind: sectrustMDH1, text: "Title"},
		{kind: sectrustMDPara, text: "Intro line one continues here."},
		{kind: sectrustMDH2, text: "Section A"},
		{kind: sectrustMDBullet, text: "first bold item continued"},
		{kind: sectrustMDBullet, text: "nested link", depth: 1},
		{kind: sectrustMDBullet, text: "numbered"},
		{kind: sectrustMDCode, text: "code line\n    indented"},
		{kind: sectrustMDTable, text: "Col  ·  Two", header: true},
		{kind: sectrustMDTable, text: "a  ·  b"},
		{kind: sectrustMDRule},
		{kind: sectrustMDQuote, text: "quoted text"},
		{kind: sectrustMDH3, text: "Small"},
		{kind: sectrustMDPara, text: "Visible"},
	}
	got := sectrustParseMarkdown(src)
	if len(got) != len(want) {
		t.Fatalf("parsed %d blocks, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A fence that is never closed still shows its code, and shell comments inside
// a fence are not headings.
func TestSectrustParseMarkdownCodeFences(t *testing.T) {
	got := sectrustParseMarkdown("```sh\n# not a heading\necho hi")
	if len(got) != 1 || got[0].kind != sectrustMDCode || got[0].text != "# not a heading\necho hi" {
		t.Fatalf("unclosed fence = %+v", got)
	}
	// SECURITY.md has a closing fence glued to the next sentence; the
	// sentence must survive as a paragraph.
	got = sectrustParseMarkdown("```bash\nexport KEY=1\n```If you pass a key\nit is used.\n\n## Next\n")
	want := []sectrustMDBlock{
		{kind: sectrustMDCode, text: "export KEY=1"},
		{kind: sectrustMDPara, text: "If you pass a key it is used."},
		{kind: sectrustMDH2, text: "Next"},
	}
	if len(got) != len(want) {
		t.Fatalf("glued fence = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("glued fence block %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSectrustInline(t *testing.T) {
	for in, want := range map[string]string{
		"See [the guide](docs/x.md) and ![logo](a.png)": "See the guide and logo",
		"**Bold** and `code`":                           "Bold and code",
		"Keep <keyID> placeholders":                     "Keep <keyID> placeholders",
		"one<br/>two":                                   "one two",
		"  spaced   out  ":                              "spaced out",
	} {
		if got := sectrustInline(in); got != want {
			t.Errorf("sectrustInline(%q) = %q, want %q", in, got, want)
		}
	}
}

// The tests run in player/, one folder under the repository root, which is
// where a development build finds the document; every posture row's section
// must exist there, or "Read more" would open the reader at the top.
func TestSectrustSectionsExistInSecurityMD(t *testing.T) {
	text, source := sectrustLoadDoc()
	if source == "" {
		t.Fatal("SECURITY.md was not found one folder up from player/; the reader would show only the summary")
	}
	headings := map[string]bool{}
	h2 := 0
	for _, b := range sectrustParseMarkdown(text) {
		switch b.kind {
		case sectrustMDH1, sectrustMDH3:
			headings[strings.ToLower(b.text)] = true
		case sectrustMDH2:
			headings[strings.ToLower(b.text)] = true
			h2++
		}
	}
	if h2 < 5 {
		t.Fatalf("the reader found only %d sections in %s", h2, source)
	}
	for _, row := range securityPostureRows(&Config{}) {
		_, _, section := sectrustExplain(row.ID)
		if section == "" {
			continue
		}
		if !headings[strings.ToLower(section)] {
			t.Errorf("row %q points at section %q, which %s does not have", row.ID, section, source)
		}
	}
}

// Without the file — an old package, a copied binary — the built-in summary is
// shown, and it still parses into a readable document with sections.
func TestSectrustFallbackSummary(t *testing.T) {
	t.Chdir(t.TempDir())
	text, source := sectrustLoadDoc()
	if source != "" {
		t.Skipf("a SECURITY.md next to the test binary was found at %s", source)
	}
	if text != sectrustDocFallback {
		t.Fatal("the fallback is not the built-in summary")
	}
	h2 := 0
	for _, b := range sectrustParseMarkdown(text) {
		if b.kind == sectrustMDH2 {
			h2++
		}
	}
	if h2 < 3 || !strings.Contains(text, "github.com/jukaLang/JukaHubV2") {
		t.Fatalf("the summary has %d sections and must name where the full document is", h2)
	}
}

func TestSectrustItoa(t *testing.T) {
	for n, want := range map[int32]string{0: "0", 7: "7", 42: "42", 1234: "1234", -5: "-5"} {
		if got := sectrustItoa(n); got != want {
			t.Errorf("sectrustItoa(%d) = %q, want %q", n, got, want)
		}
	}
}

// Every posture row has an explanation, and every row but the document itself
// says why it matters.
func TestSectrustEveryRowIsExplained(t *testing.T) {
	for _, row := range securityPostureRows(&Config{}) {
		what, why, _ := sectrustExplain(row.ID)
		if what == "" {
			t.Errorf("row %q has no explanation", row.ID)
		}
		if row.ID != "doc" && why == "" {
			t.Errorf("row %q does not say why it matters", row.ID)
		}
		if sectrustRowIcon(row.ID) == "" {
			t.Errorf("row %q has no icon", row.ID)
		}
	}
}
