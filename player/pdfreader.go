package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Reader — PDF and EPUB
// ──────────────────────────────────────────────────────────────────────
//
// The scene keeps its historical name ("PDFReader") because favourites, the
// Start menu and the playtime achievements reference it. It reads both
// formats: a PDF is drawn as pages (pdfv_doc.go reads it, pdfv_view.go draws
// it) with zoom, fit width / fit page and page N of M, and its text can be
// reflowed instead (X); an EPUB is paginated text (reader.go). Opening runs off
// the render thread, so a large document shows a loading page instead of a
// frozen screen. This file owns the reader's state, the page chrome and the
// input.

type pdfPage struct {
	Text   string
	Width  float64
	Height float64
}

type pdfDoc struct {
	Path     string
	Format   string // "PDF" or "EPUB"
	Pages    []pdfPage
	Chapters []pdfChapter
	Title    string
	Author   string
	Subject  string
	NumPages int
	Loaded   bool
	// Loading is true from the moment a document is picked until the
	// background open has finished (then Loaded or Error is set).
	Loading bool
	Error   string
}

type pdfState struct {
	doc       pdfDoc
	page      int
	scrollY   float64
	zoom      float64
	viewMode  int // 0=text, 1=fit-width
	bookmarks []int
}

var pdf pdfState

// pdfInit empties the reader.
func pdfInit() {
	pdf = pdfState{page: 1, zoom: 1.0, viewMode: 1}
}

// pdfSceneEnter runs when the scene is entered. It only resets an empty reader:
// Files opens a document and then navigates here, so wiping the state on entry
// would throw away the file that was just opened (or is still opening).
func pdfSceneEnter() {
	if pdf.doc.Loaded || pdf.doc.Error != "" || pdf.doc.Loading {
		return
	}
	pdfInit()
}

// pdfvSceneName is the reader's scene. The name is historical (favourites, the
// Start menu and the playtime achievements all spell it this way).
const pdfvSceneName = "PDFReader"

// pdfvEnsureScene gives the reader a scene in memory when the loaded config has
// none — and neither shipped config has one. Opening a PDF from Files used to
// look the scene up with findSceneIndex, which answers 0 (the Home scene) for a
// name it cannot find, so every PDF "opened" by dropping the user on Home. Like
// the Alarm and Browser scenes it is injected, never written back.
func pdfvEnsureScene(config *Config) {
	if config == nil || sceneIndexExact(config, pdfvSceneName) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        pdfvSceneName,
		Icon:        "R",
		Description: "Read PDF and EPUB documents",
		Background:  "#0B0F17",
	})
}

// pdfvOpenDocument opens a PDF or EPUB and shows it in the reader. It reports
// whether the reader is on its way; the scene is found by its exact name, so a
// config without it can never send the user somewhere else.
func pdfvOpenDocument(config *Config, path string) bool {
	if config == nil || !pdfIsDocumentPath(path) {
		return false
	}
	pdfvEnsureScene(config)
	idx := sceneIndexExact(config, pdfvSceneName)
	if idx < 0 {
		showToast("The reader is not available", ToastError())
		return false
	}
	pdfOpenFile(path)
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
	return true
}

// pdfvSceneLeft releases the reader's textures when another scene takes over:
// a zoomed page holds several megabytes of tiles.
func pdfvSceneLeft() {
	pdfSaveReadingState()
	pdfvReleaseTextures()
	pdfvView.menu.open = false
}

// pdfIsDocumentPath reports whether a path is one the reader can open.
func pdfIsDocumentPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".pdf") || strings.HasSuffix(lower, ".epub")
}

// pdfOpenFile opens a PDF or EPUB from the File Explorer. The path is resolved
// against the explorer root first; the reading itself happens in the
// background and the reader shows "Opening…" until it lands.
func pdfOpenFile(path string) {
	if !pdfIsDocumentPath(path) {
		return
	}
	pdfSaveReadingState()
	pdfInit()
	pdfvReleaseTextures()
	textSize := pdfvView.textSize
	pdfvView = pdfvViewState{textSize: textSize}
	format := "PDF"
	epub := strings.HasSuffix(strings.ToLower(path), ".epub")
	if epub {
		format = "EPUB"
	}
	resolved, err := resolveExplorerEntry(appConfig, path)
	if err != nil {
		pdfvStopLoad()
		pdf.doc = pdfDoc{Path: path, Format: format, Loaded: true, Error: "Cannot open file: " + filesFriendlyError(err)}
		return
	}
	pdf.doc = pdfDoc{
		Path:    resolved,
		Format:  format,
		Title:   strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved)),
		Loading: true,
	}
	if saved, ok := readerBookFor(resolved); ok {
		pdf.page = saved.Page
		pdf.scrollY = saved.Scroll
		pdf.bookmarks = append([]int(nil), saved.Bookmarks...)
		if saved.TextSize > 0 {
			pdfvView.textSize = saved.TextSize
		}
		pdfvView.textMode = saved.TextMode
		if saved.Fit >= int(pdfvFitWidth) && saved.Fit <= int(pdfvFitCustom) {
			pdfvView.fit = pdfvFitMode(saved.Fit)
		}
		if saved.Zoom > 0 {
			pdfvView.zoom = saved.Zoom
		}
	} else {
		pdf.page = 1
		pdf.scrollY = 0
	}
	if epub {
		pdfvLoadEPUB(resolved)
	} else {
		pdfvStartLoad(resolved)
	}
}

// pdfvLoadEPUB reads a book in the background and hands it over through the
// same slot a PDF uses.
func pdfvLoadEPUB(path string) {
	pdfvStopLoad()
	pdfvDoc.mu.Lock()
	gen := pdfvDoc.gen
	pdfvDoc.mu.Unlock()
	go func() {
		var doc pdfDoc
		data, err := os.ReadFile(path)
		if err != nil {
			doc = pdfDoc{Path: path, Format: "EPUB", Loaded: true, Error: "Cannot open file: " + filesFriendlyError(err)}
		} else {
			doc = epubParseBytes(data, path)
		}
		pdfvDoc.mu.Lock()
		if pdfvDoc.gen == gen {
			pdfvDoc.loaded = &doc
		}
		pdfvDoc.mu.Unlock()
	}()
}

// ── Navigation ─────────────────────────────────────────────────────────────

func pdfPageCount() int {
	if pdf.doc.NumPages > 0 {
		return pdf.doc.NumPages
	}
	return len(pdf.doc.Pages)
}

func pdfGoToPage(page int) bool {
	count := pdfPageCount()
	if count == 0 {
		return false
	}
	if page < 1 {
		page = 1
	}
	if page > count {
		page = count
	}
	if page == pdf.page {
		return false
	}
	pdf.page = page
	pdf.scrollY = 0
	pdfvView.scrollY = 0
	pdfSaveReadingState()
	trophyEvent(tkPdfPage)
	PlayNavSound()
	return true
}

func pdfNextPage() bool { return pdfGoToPage(pdf.page + 1) }
func pdfPrevPage() bool { return pdfGoToPage(pdf.page - 1) }

// pdfJumpChapter moves to the start of the previous or next chapter. It returns
// false when the document has no chapter structure.
// pdfSaveReadingState stores the page, scroll, text size and bookmarks of the
// open document. Scroll position is whichever view is on screen; the other
// view starts at the top of the page.
func pdfSaveReadingState() {
	if !pdf.doc.Loaded || pdf.doc.Error != "" || strings.TrimSpace(pdf.doc.Path) == "" {
		return
	}
	scroll := pdf.scrollY
	if pdfvIsPagedPDF() && !pdfvView.textMode {
		scroll = pdfvView.scrollY
	}
	readerSaveBook(readerBookState{
		Path:      pdf.doc.Path,
		Page:      pdf.page,
		Pages:     pdfPageCount(),
		Scroll:    scroll,
		TextSize:  pdfvView.textSize,
		TextMode:  pdfvView.textMode,
		Fit:       int(pdfvView.fit),
		Zoom:      pdfvView.zoom,
		Bookmarks: pdf.bookmarks,
	})
}

func pdfPageBookmarked(page int) bool {
	for _, p := range pdf.bookmarks {
		if p == page {
			return true
		}
	}
	return false
}

// pdfBookmarkToggle marks or unmarks the page under the reader.
func pdfBookmarkToggle() {
	bookmarks, added := readerBookmarkToggle(pdf.bookmarks, pdf.page)
	if len(bookmarks) == len(pdf.bookmarks) && !added {
		return
	}
	pdf.bookmarks = bookmarks
	pdfSaveReadingState()
	PlayActivateSound()
	if added {
		showToast(fmt.Sprintf("Bookmarked page %d", pdf.page), ToastSuccess())
	} else {
		showToast(fmt.Sprintf("Bookmark removed from page %d", pdf.page), ToastInfo())
	}
}

// pdfBookmarkJump moves to the nearest bookmark in dir.
func pdfBookmarkJump(dir int) bool {
	page, ok := readerBookmarkNext(pdf.bookmarks, pdf.page, dir)
	if !ok {
		if dir < 0 {
			showToast("No earlier bookmark", ToastInfo())
		} else {
			showToast("No later bookmark", ToastInfo())
		}
		return false
	}
	return pdfGoToPage(page)
}

func pdfJumpChapter(delta int) bool {
	if len(pdf.doc.Chapters) == 0 {
		return false
	}
	chapter, idx := pdfChapterForPage(pdf.page)
	if idx < 0 {
		return false
	}
	target := idx + delta
	// Standing at the top of a chapter, "previous" should land at the top of
	// that chapter, not the one before it.
	if delta < 0 && pdf.page > chapter.FirstPage {
		target = idx
	}
	if target < 0 {
		target = 0
	}
	if target >= len(pdf.doc.Chapters) {
		target = len(pdf.doc.Chapters) - 1
	}
	return pdfGoToPage(pdf.doc.Chapters[target].FirstPage)
}

// pdfScrollBy moves the text view and stops at the top.
func pdfScrollBy(delta float64) {
	pdf.scrollY += delta
	if pdf.scrollY < 0 {
		pdf.scrollY = 0
	}
}

// pdfvTextMaxScroll is how far the text view's page scrolls (set by the last
// frame that drew it).
var pdfvTextMaxScroll float64

// pdfScrollText scrolls the text view and turns the page at either end.
func pdfScrollText(delta float64) {
	switch {
	case delta > 0 && pdf.scrollY >= pdfvTextMaxScroll-0.5:
		pdfNextPage()
	case delta < 0 && pdf.scrollY <= 0.5:
		if pdfPrevPage() {
			pdf.scrollY = 1 << 30
		}
	default:
		pdfScrollBy(delta)
		if pdf.scrollY > pdfvTextMaxScroll {
			pdf.scrollY = pdfvTextMaxScroll
		}
	}
}

// pdfCurrentPage is the page under the reader, guarded against a page index that
// outlived a reloaded document.
func pdfCurrentPage() (pdfPage, bool) {
	idx := pdf.page - 1
	if idx < 0 || idx >= len(pdf.doc.Pages) {
		return pdfPage{}, false
	}
	return pdf.doc.Pages[idx], true
}

// pdfChapterLabel is the read-out for the current chapter, e.g.
// "Chapter 2 of 7 · The Long Dark". Empty for a chapterless document.
func pdfChapterLabel() string {
	chapter, idx := pdfChapterForPage(pdf.page)
	if idx < 0 {
		return ""
	}
	title := strings.TrimSpace(chapter.Title)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", idx+1)
	}
	return fmt.Sprintf("Chapter %d of %d · %s", idx+1, len(pdf.doc.Chapters), title)
}

// ── Rendering ──────────────────────────────────────────────────────────────

func renderPDFReader(renderer *sdl.Renderer, config *Config) {
	// Home's backdrop, the one every screen stands on (ui_glass.go). The
	// reader is a full-screen page with its own header, so the shared bar's
	// click targets from the screen before are cleared rather than left to
	// answer a tap here.
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	sceneTitleBackRect, statusClockRect, statusWiFiRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	weatherStatusRect, statusMediaRect, statusVolumeRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	statusBrightnessRect, statusBluetoothRect = sdl.Rect{}, sdl.Rect{}
	tb.startRect = sdl.Rect{}
	updatePrompt.badge = sdl.Rect{}
	headerTrophyClear()
	headerBugClear()
	pdfvPollLoaded()
	doc := pdf.doc

	margin := scaleX(homeSideMargin, screenWidth)
	top := int32(14)
	paged := pdfvIsPagedPDF() && !pdfvView.textMode
	chips := pdfvView.chips[:0]
	if doc.Loaded && doc.Error == "" && pdfPageCount() > 1 {
		chips = append(chips, filesChip{ID: "prev", Label: "‹"}, filesChip{ID: "next", Label: "›"})
	}
	if doc.Loaded && doc.Error == "" {
		chips = append(chips, filesChip{ID: "zoomout", Label: "−"}, filesChip{ID: "zoomin", Label: "+"})
		if paged {
			chips = append(chips, filesChip{ID: "fit", Label: pdfvFitLabel()})
		}
		if doc.Format == "PDF" {
			label := "Text"
			if pdfvView.textMode {
				label = "Page"
			}
			chips = append(chips, filesChip{ID: "text", Label: label, On: pdfvView.textMode})
		}
		chips = append(chips, filesChip{ID: "options", Label: "Options"})
	} else if doc.Error != "" && filesSystemOpenAvailable() && doc.Path != "" {
		chips = append(chips, filesChip{ID: "system", Label: "Open with Windows"})
	}
	chips = append(chips, filesChip{ID: "back", Label: "Back"})
	chips, toolbarX := filesDrawToolbar(renderer, config, chips, screenWidth-margin, top+4, 34)
	pdfvView.chips = chips

	title := pdfDocumentTitle(doc)
	if title == "" {
		title = "Reader"
	}
	glyph := "pdf"
	if doc.Format == "EPUB" {
		glyph = "text"
	}
	y := filesDrawHeader(renderer, config, glyph, title, pdfvSubtitle(), margin, top, toolbarX-margin-24, sdl.Color{})
	y += 10
	footerTop := filesDrawFooter(renderer, config, pdfvFooterHints())
	view := sdl.Rect{X: margin, Y: y, W: screenWidth - 2*margin, H: footerTop - 10 - y}
	if view.H < 80 {
		view.H = 80
	}
	pdfvView.viewport = view

	caption := FontForRole(config, TypeCaption)
	fitBody := func(s string) string {
		if caption == nil {
			return s
		}
		return ellipsize(caption, s, view.W-48)
	}
	switch {
	case doc.Loading:
		UIEmptyState(renderer, config, view, "@"+glyph, "Opening "+title+"…", "A large document takes a moment")
	case !doc.Loaded:
		UIEmptyState(renderer, config, view, "@pdf", "No document open", "Open a PDF or EPUB from Files")
	case doc.Error != "":
		body := doc.Error
		if filesSystemOpenAvailable() {
			body += " · Open with Windows is above"
		}
		UIErrorState(renderer, config, view, "@warning", "This document cannot be shown", fitBody(body))
	case paged:
		pdfvRenderPages(renderer, config, view)
	default:
		pdfvRenderText(renderer, config, view)
	}
	filesMenuRender(renderer, config, &pdfvView.menu)
}

// pdfvSubtitle is the header's status line: where the reader is.
func pdfvSubtitle() string {
	doc := pdf.doc
	switch {
	case doc.Loading:
		return "Opening…"
	case !doc.Loaded || doc.Error != "":
		return doc.Format
	}
	parts := []string{fmt.Sprintf("Page %d of %d", pdf.page, pdfPageCount())}
	if chapter := pdfChapterLabel(); chapter != "" && len(doc.Chapters) > 1 {
		parts = append([]string{chapter}, parts...)
	}
	if pdfvIsPagedPDF() && !pdfvView.textMode {
		parts = append(parts, pdfvFitLabel())
	} else if doc.Format == "PDF" {
		parts = append(parts, "Text view")
	}
	if author := strings.TrimSpace(doc.Author); author != "" {
		parts = append(parts, author)
	}
	return strings.Join(parts, " · ")
}

// pdfvFooterHints are the reader's keys in its current view.
func pdfvFooterHints() []FooterHint {
	if !pdf.doc.Loaded || pdf.doc.Error != "" {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	// The keyboard's halves (handlePDFInput): [ and ] step chapters, - and +
	// zoom; PgUp/PgDn page, which the hint layer names from "Page" itself.
	pageLabel, pageButton := "Page", "L1/R1"
	if len(pdf.doc.Chapters) > 0 {
		pageLabel, pageButton = "Chapter", "L1/R1 / [ ]"
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Scroll"},
		{Button: pageButton, Label: pageLabel},
	}
	if pdfvIsPagedPDF() && !pdfvView.textMode {
		hints = append(hints,
			FooterHint{Button: "L2/R2 / - / +", Label: "Zoom"},
			FooterHint{Button: "A", Label: "Fit"},
			FooterHint{Button: "X", Label: "Text"},
		)
	} else {
		hints = append(hints, FooterHint{Button: "L2/R2 / - / +", Label: "Text size"})
		if pdf.doc.Format == "PDF" {
			hints = append(hints, FooterHint{Button: "X", Label: "Page view"})
		}
	}
	return append(hints,
		FooterHint{Button: "Y", Label: "Options"},
		FooterHint{Button: "B", Label: "Back"},
	)
}

// pdfvRenderText draws the current page's text on a paper card that scrolls
// inside the viewport: a book's page, or a PDF page reflowed.
func pdfvRenderText(renderer *sdl.Renderer, config *Config, view sdl.Rect) {
	size := pdfvView.textSize
	if size == 0 {
		size = 22
	}
	font := getFontSized(config, "small", size)
	if font == nil {
		return
	}
	text := ""
	if pdf.doc.Format == "PDF" {
		pdfvRequest(pdf.page - 1)
		if content := pdfvPageContentFor(pdf.page - 1); content != nil {
			text = content.text
		} else {
			UIEmptyState(renderer, config, view, "@pdf", "Reading page "+filesNumberString(int64(pdf.page))+"…", "")
			return
		}
	} else if page, ok := pdfCurrentPage(); ok {
		text = page.Text
	}
	if strings.TrimSpace(text) == "" {
		body := "It may be a scanned picture."
		if pdf.doc.Format == "PDF" {
			body = "It may be a scanned picture — X shows the page itself."
		}
		UIEmptyState(renderer, config, view, "@text", "This page has no text", body)
		return
	}
	paperW := view.W - 48
	if paperW > 900 {
		paperW = 900
	}
	paper := sdl.Rect{X: view.X + (view.W-paperW)/2, Y: view.Y, W: paperW, H: view.H}
	// Paper: the page reads as a page in either theme.
	fillRoundedRect(renderer, paper.X+3, paper.Y+3, paper.W, paper.H, 10, sdl.Color{R: 0, G: 0, B: 0, A: 50})
	fillRoundedRect(renderer, paper.X, paper.Y, paper.W, paper.H, 10, sdl.Color{R: 246, G: 244, B: 238, A: 255})
	lines := pdfvTextWrapped(font, pdf.page, text, paper.W-56)
	lineH := int32(font.Height()) + 4
	inner := paper.H - 40
	pdfvTextMaxScroll = float64(int32(len(lines))*lineH - inner)
	if pdfvTextMaxScroll < 0 {
		pdfvTextMaxScroll = 0
	}
	if pdf.scrollY > pdfvTextMaxScroll {
		pdf.scrollY = pdfvTextMaxScroll
	}
	first := int(pdf.scrollY / float64(lineH))
	offset := int32(pdf.scrollY) - int32(first)*lineH
	ink := sdl.Color{R: 28, G: 30, B: 36, A: 255}
	renderWithClip(renderer, paper.X+28, paper.Y+20, paper.W-56, inner, func(r *sdl.Renderer) {
		y := paper.Y + 20 - offset
		for i := first; i < len(lines) && y < paper.Y+20+inner; i++ {
			if lines[i] != "" {
				filesDrawPlainText(r, config, font, lines[i], ink, paper.X+28, y)
			}
			y += lineH
		}
	})
	if pdfvTextMaxScroll > 0 {
		thumb := float64(inner) / float64(int32(len(lines))*lineH)
		drawScrollbar(renderer, paper.X+paper.W-10, paper.Y+20, 3, inner, thumb, pdf.scrollY/pdfvTextMaxScroll)
	}
}

// pdfDocumentTitle is the title line under the bar: the document's own title
// when the extractor found one, otherwise the file's name so an untitled
// document still says what is open.
func pdfDocumentTitle(doc pdfDoc) string {
	if title := strings.TrimSpace(doc.Title); title != "" {
		return title
	}
	if path := strings.TrimSpace(doc.Path); path != "" {
		return filepath.Base(path)
	}
	return ""
}

// pdfWordWrap wraps text on words only.
func pdfWordWrap(font *ttf.Font, text string, maxWidth int32) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	var lines []string
	var currentLine strings.Builder
	var currentWidth int32

	for _, word := range words {
		ww, _, _ := font.SizeUTF8(word + " ")
		if currentWidth+int32(ww) > maxWidth && currentLine.Len() > 0 {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentWidth = 0
		}
		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
			currentWidth += 8 // space width
		}
		currentLine.WriteString(word)
		currentWidth += int32(ww)
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

// pdfWrapLines wraps text while preserving a blank line between paragraphs.
// strings.Fields destroyed those gaps, which is why a book read as one solid
// wall of text.
func pdfWrapLines(font *ttf.Font, text string, maxWidth int32) []string {
	var lines []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimRight(paragraph, " \t")
		if strings.TrimSpace(trimmed) == "" {
			// Consecutive blank lines collapse: the gap is inserted before the
			// next paragraph, so a page never ends on an empty line.
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, pdfWordWrap(font, trimmed, maxWidth)...)
	}
	return lines
}

// ── Input ──────────────────────────────────────────────────────────────────

// pdfvVertical is Up/Down: scroll the page (or its text), turning the page at
// either end.
func pdfvVertical(dir int) {
	if pdfvIsPagedPDF() && !pdfvView.textMode {
		pdfvScroll(float64(dir) * float64(pdfvView.viewport.H) / 5)
		return
	}
	pdfScrollText(float64(dir) * 90)
}

// pdfvShoulder answers L1/R1 (page, or chapter in a book) and L2/R2 (zoom, or
// the text size in the text view).
func pdfvShoulder(s padShoulder) {
	switch s {
	case padL1:
		if len(pdf.doc.Chapters) > 0 {
			pdfJumpChapter(-1)
		} else {
			pdfPrevPage()
		}
	case padR1:
		if len(pdf.doc.Chapters) > 0 {
			pdfJumpChapter(1)
		} else {
			pdfNextPage()
		}
	case padL2:
		pdfvZoomStep(-1)
	case padR2:
		pdfvZoomStep(1)
	}
}

func handlePDFInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	if pdfvView.menu.open {
		pdfvMenuApply(filesMenuKey(&pdfvView.menu, e.Keysym.Sym))
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		pdfvSide(-1)
	case sdl.K_RIGHT:
		pdfvSide(1)
	case sdl.K_UP:
		pdfvVertical(-1)
	case sdl.K_DOWN:
		pdfvVertical(1)
	case sdl.K_PAGEUP:
		pdfPrevPage()
	case sdl.K_PAGEDOWN, sdl.K_SPACE:
		pdfNextPage()
	case sdl.K_HOME:
		pdfGoToPage(1)
	case sdl.K_END:
		pdfGoToPage(pdfPageCount())
	case sdl.K_LEFTBRACKET:
		pdfJumpChapter(-1)
	case sdl.K_RIGHTBRACKET:
		pdfJumpChapter(1)
	case sdl.K_PLUS, sdl.K_EQUALS, sdl.K_KP_PLUS:
		pdfvZoomStep(1)
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		pdfvZoomStep(-1)
	case sdl.K_0, sdl.K_f, sdl.K_a:
		if pdfvIsPagedPDF() && !pdfvView.textMode {
			pdfvCycleFit()
		}
	case sdl.K_t, sdl.K_x:
		pdfvToggleText()
	case sdl.K_m:
		pdfBookmarkToggle()
	case sdl.K_o, sdl.K_y, sdl.K_TAB:
		if pdf.doc.Loaded && pdf.doc.Error == "" {
			pdfvOpenMenu()
		}
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		goBackScene(config)
	}
}

// ctrlPDFReader is the reader's pad map; the scene owns its own input.
func ctrlPDFReader(e *sdl.ControllerButtonEvent) bool {
	if e == nil {
		return false
	}
	if pdfvView.menu.open {
		pdfvMenuApply(filesMenuController(&pdfvView.menu, e.Button))
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		pdfvVertical(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		pdfvVertical(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		pdfvSide(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		pdfvSide(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		pdfvShoulder(padL1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		pdfvShoulder(padR1)
	case sdl.CONTROLLER_BUTTON_A:
		if pdfvIsPagedPDF() && !pdfvView.textMode {
			pdfvCycleFit()
		}
	case sdl.CONTROLLER_BUTTON_X:
		pdfvToggleText()
	case sdl.CONTROLLER_BUTTON_Y:
		if pdf.doc.Loaded && pdf.doc.Error == "" {
			pdfvOpenMenu()
		} else if pdf.doc.Error != "" && filesSystemOpenAvailable() {
			filesOpenWithSystem(pdf.doc.Path)
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(appConfig)
	default:
		return false
	}
	return true
}

// Scene registration.
type pdfScene struct{ sceneBase }

func (pdfScene) render(r *sdl.Renderer, c *Config) bool { renderPDFReader(r, c); return true }

func (pdfScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handlePDFInput(e, c)
	return true
}

func (pdfScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlPDFReader(e)
}

func init() { registerScene(pdfScene{sceneBase{name: "PDFReader"}}) }
