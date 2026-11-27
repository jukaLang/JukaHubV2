package main

// offline_ref.go — offline reference reading. A Kiwix ZIM is a packaged
// encyclopaedia with its own index and compression; this is the smaller,
// dependency-free first step: put text, Markdown or saved HTML pages in
// <data>/reference and read them from the Community hub with no network.
//
// Only regular files under that folder are listed, with a size cap, and HTML
// is reduced to its text. Nothing is fetched, and no script in a saved page
// runs — the card shows words, not a web page.

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	offlineRefDirRel     = "reference"
	offlineRefFileMax    = 8 << 20
	offlineRefMaxLines   = 4000
	offlineRefMaxCharLen = 400
)

var offlineRefHTMLTag = regexp.MustCompile(`<[^>]*>`)
var offlineRefSpace = regexp.MustCompile(`[ \t]+`)
var offlineRefSkipBlocks = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`),
	regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`),
	regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`),
}

// offlineRefDir is replaceable in tests.
var offlineRefDir = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, offlineRefDirRel)
}

type offlineRefDoc struct {
	Name string
	Path string
	Size int64
}

// offlineRefScan lists the readable reference documents, A to Z.
func offlineRefScan() []offlineRefDoc {
	root := offlineRefDir()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []offlineRefDoc
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") ||
			(ext != ".txt" && ext != ".md" && ext != ".html" && ext != ".htm") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > offlineRefFileMax {
			continue
		}
		out = append(out, offlineRefDoc{Name: e.Name(), Path: filepath.Join(root, e.Name()), Size: info.Size()})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// offlineRefUI is the card's list focus and the open document.
var offlineRefUI struct {
	cursor int
	open   bool
	path   string
	title  string
	lines  []string
	err    string
}

func offlineRefMove(delta int) {
	items := offlineRefScan()
	if len(items) == 0 {
		return
	}
	offlineRefUI.cursor = (offlineRefUI.cursor + delta + len(items)) % len(items)
	PlayNavSound()
}

// offlineRefText turns a document's bytes into wrapped plain lines.
func offlineRefText(name string, data []byte) []string {
	text := string(data)
	if ext := strings.ToLower(filepath.Ext(name)); ext == ".html" || ext == ".htm" {
		for _, block := range offlineRefSkipBlocks {
			text = block.ReplaceAllString(text, " ")
		}
		text = offlineRefHTMLTag.ReplaceAllString(text, " ")
		text = html.UnescapeString(text)
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(offlineRefSpace.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
		if len(out) >= offlineRefMaxLines {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, "This document has no readable text.")
	}
	return out
}

// offlineRefOpen reads the highlighted document. A file swapped between the
// scan and the read is reported instead of half shown.
func offlineRefOpen() {
	items := offlineRefScan()
	if offlineRefUI.cursor < 0 || offlineRefUI.cursor >= len(items) {
		return
	}
	doc := items[offlineRefUI.cursor]
	data, err := os.ReadFile(doc.Path)
	if err != nil {
		offlineRefUI.open, offlineRefUI.path, offlineRefUI.lines = true, doc.Path, nil
		offlineRefUI.err = firstLine(err.Error())
		return
	}
	if int64(len(data)) > offlineRefFileMax {
		offlineRefUI.open, offlineRefUI.path, offlineRefUI.lines = true, doc.Path, nil
		offlineRefUI.err = fmt.Sprintf("That document is over %d MB", offlineRefFileMax>>20)
		return
	}
	offlineRefUI.open, offlineRefUI.path, offlineRefUI.lines = true, doc.Path, offlineRefText(doc.Name, data)
	offlineRefUI.title, offlineRefUI.err = doc.Name, ""
}

// offlineRefClose returns to the document list.
func offlineRefClose() {
	offlineRefUI.open, offlineRefUI.path, offlineRefUI.lines, offlineRefUI.err = false, "", nil, ""
	PlayBackSound()
}

// offlineRefLines is the card as text: either the list or the open document.
func offlineRefLines() []string {
	if offlineRefUI.open {
		lines := []string{offlineRefUI.title, ""}
		if offlineRefUI.err != "" {
			return append(lines, offlineRefUI.err)
		}
		return append(lines, offlineRefUI.lines...)
	}
	lines := []string{
		"Reading offline, from <data>/" + offlineRefDirRel + ".",
		"Put .txt, .md or saved .html pages there; no download and no scripts.",
		"",
	}
	items := offlineRefScan()
	if len(items) == 0 {
		return append(lines, "No documents there yet.")
	}
	lines = append(lines, "Up/Down pick · A read · B back", "")
	for i, doc := range items {
		mark := " "
		if i == offlineRefUI.cursor {
			mark = "›"
		}
		lines = append(lines, mark+" "+doc.Name+" ("+itoa(int(doc.Size>>10))+" KB)")
	}
	return lines
}
