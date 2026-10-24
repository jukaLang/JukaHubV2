package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The reader's <img> support (webpage_image.go): loadable sources become real
// image blocks, the layout reserves their boxes, and data URIs (which need no
// network) decode with a natural size the layout scales from.

func tinyTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	pic := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			pic.SetRGBA(x, y, color.RGBA{R: 0x22, G: 0x66, B: 0xAA, A: 0xFF})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, pic); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return raw.Bytes()
}

// Regression: the layouter reads an image's natural size while layoutFor holds
// the document lock. When one mutex guarded both, the first page with an image
// whose markup gave no size deadlocked the render thread — the browser (and the
// whole app) froze while opening it. The image maps now have their own lock, so
// this has to return.
func TestWebLayoutUnsizedImageDoesNotDeadlock(t *testing.T) {
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	doc := parseWebPage(`<body><p>Before the picture</p>`+
		`<img src="https://cdn.example.com/hero.png">`+
		`<p>After the picture</p></body>`, "", "https://example.com/")
	if doc == nil {
		t.Fatal("page did not parse")
	}
	elem := Element{Type: "textbrowser", Font: "small"}
	done := make(chan *webLayout, 1)
	go func() { done <- doc.layoutFor(config, elem, 900) }()
	select {
	case layout := <-done:
		if layout == nil || len(layout.Rows) == 0 {
			t.Fatalf("layout = %+v, want laid-out rows", layout)
		}
		if len(layout.Images) != 1 {
			t.Fatalf("reserved %d image boxes, want 1", len(layout.Images))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("layoutFor deadlocked on an image without a recorded natural size")
	}
}

func TestParseWebImageBlocks(t *testing.T) {
	doc := parseWebPage(`<body>
		<img src="https://cdn.example.com/hero.png" width="320" height="180">
		<img src="/img/relative.png">
		<img src="ftp://old/fail.png" alt="dead src alt">
		<img>
	</body>`, "", "https://example.com/page")

	var found int
	for _, b := range doc.Blocks {
		if !b.Image {
			continue
		}
		found++
		switch b.ImageSrc {
		case "https://cdn.example.com/hero.png":
		case "https://example.com/img/relative.png":
		default:
			t.Errorf("unexpected image src %q", b.ImageSrc)
		}
		if b.ImageSrc == "https://cdn.example.com/hero.png" {
			if b.ImageW != 320 || b.ImageH != 180 {
				t.Errorf("hero dims = %v/%v, want 320/180", b.ImageW, b.ImageH)
			}
		}
	}
	if found != 2 {
		t.Fatalf("image blocks = %d, want 2 (http + relative)", found)
	}

	// A dead src keeps its alt text in the flow instead of vanishing.
	keptAlt := false
	for _, b := range doc.Blocks {
		for _, r := range b.Runs {
			if strings.TrimSpace(r.Text) == "dead src alt" {
				keptAlt = true
			}
		}
	}
	if !keptAlt {
		t.Error("alt text for a dead src was dropped")
	}
}

func TestWebImageLayoutReservesRows(t *testing.T) {
	doc := parseWebPage(`<body><img src="https://cdn.example.com/a.png" width="300" height="150"></body>`, "", "")
	// No base URL: the relative resolution fails and alt path keeps nothing;
	// use a direct absolute src page instead via parseWebPage with base.
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks parsed")
	}
	layout := &webLayout{RowH: 24}
	l := &webLayouter{
		doc:      doc,
		layout:   layout,
		rowH:     24,
		baseSize: 16, // CSS px map 1:1 at baseSize 16/scale 1
	}
	l.layoutBlocks(append([]webBlock(nil), doc.Blocks...), 0, 640)
	if len(layout.Images) != 1 {
		t.Fatalf("layout images = %d, want 1", len(layout.Images))
	}
	im := layout.Images[0]
	if im.Rect.W != 300 || im.Rect.H != 150 {
		t.Errorf("image box = %dx%d, want 300x150", im.Rect.W, im.Rect.H)
	}
	// The reserved rows must cover the box so text never overlaps it.
	rows := computeWebImageRowSpan(layout.Images[0].Rect, 24)
	if int32(len(layout.Rows)) < rows {
		t.Errorf("rows reserved = %d, want >= %d", len(layout.Rows), rows)
	}
}

func computeWebImageRowSpan(rect sdl.Rect, rowH int32) int32 {
	return (rect.H + rowH - 1) / rowH
}

func TestWebImageDataURIFetch(t *testing.T) {
	pngBytes := tinyTestPNG(t, 3, 2)
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	doc := parseWebPage(`<body><img src="`+dataURI+`"></body>`, "", "")
	var imgKey string
	for _, b := range doc.Blocks {
		if b.Image {
			imgKey = b.ImageSrc
		}
	}
	if imgKey != dataURI {
		t.Fatalf("data URI block not preserved (%d bytes …)", len(imgKey))
	}
	fetchWebImages(doc)
	if got := doc.imageBytes(imgKey); !bytes.Equal(got, pngBytes) {
		t.Fatalf("data URI bytes lost (got %d bytes, want %d)", len(got), len(pngBytes))
	}
	meta, ok := doc.imageMeta(imgKey)
	if !ok || !meta.HasSize || meta.W != 3 || meta.H != 2 {
		t.Errorf("data URI meta = %+v, want 3x2", meta)
	}
}

func TestWebImageLayoutUsesNaturalSize(t *testing.T) {
	pngBytes := tinyTestPNG(t, 96, 48)
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	doc := parseWebPage(`<body><img src="`+dataURI+`"></body>`, "", "")
	fetchWebImages(doc)

	layout := &webLayout{RowH: 24}
	l := &webLayouter{doc: doc, layout: layout, rowH: 24, baseSize: 16}
	l.layoutBlocks(doc.Blocks, 0, 640)
	if len(layout.Images) != 1 {
		t.Fatalf("layout images = %d, want 1", len(layout.Images))
	}
	im := layout.Images[0]
	// scale factor is baseSize/16 = 1, so the 96x48 natural size wins.
	if im.Rect.W != 96 || im.Rect.H != 48 {
		t.Errorf("natural-size box = %dx%d, want 96x48", im.Rect.W, im.Rect.H)
	}
}

func TestWebImageFetchMissingIsPlaceholderReady(t *testing.T) {
	doc := parseWebPage(`<body><img src="https://127.0.0.1:1/nope.png"></body>`, "", "")
	fetchWebImages(doc)
	if len(doc.ImageMeta) == 0 {
		t.Fatal("no meta recorded for an unfetchable image")
	}
	var key string
	for k := range doc.ImageMeta {
		key = k
	}
	if meta, _ := doc.imageMeta(key); meta.HasSize {
		t.Error("unfetchable image decoded a size")
	}
	if bytes := doc.imageBytes(key); len(bytes) != 0 {
		t.Errorf("unfetchable image has %d bytes", len(bytes))
	}
}
