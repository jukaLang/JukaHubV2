package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Reader — PDF and EPUB, one paper page at a time
// ──────────────────────────────────────────────────────────────────────
//
// The scene keeps its historical name ("PDFReader") because favourites, the
// scene list and the playtime achievements reference it, but it reads both
// formats now. pdfReaderText (reader.go) does the parsing; this file owns the
// state, the input and the paper UI.

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
	Error    string
}

type pdfState struct {
	doc      pdfDoc
	page     int
	scrollY  float64
	zoom     float64
	viewMode int // 0=text, 1=fit-width
}

var pdf pdfState

// pdfInit empties the reader.
func pdfInit() {
	pdf = pdfState{page: 1, zoom: 1.0, viewMode: 1}
}

// pdfSceneEnter runs when the scene is entered. It only resets an empty reader:
// Files opens a document and then navigates here, so wiping the state on entry
// would throw away the file that was just opened.
func pdfSceneEnter() {
	if pdf.doc.Loaded || pdf.doc.Error != "" {
		return
	}
	pdfInit()
}

// pdfIsDocumentPath reports whether a path is one the reader can open.
func pdfIsDocumentPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".pdf") || strings.HasSuffix(lower, ".epub")
}

// pdfOpenFile opens a PDF or EPUB from the File Explorer. Both loaders resolve
// the path against the explorer root before reading it.
func pdfOpenFile(path string) {
	if !pdfIsDocumentPath(path) {
		return
	}
	pdfInit()
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".epub") {
		pdf.doc = epubOpenFile(path)
	} else {
		pdf.doc = pdfExtractText(path)
	}
	pdf.page = 1
	pdf.scrollY = 0
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
	// Snap the scroll position back only; the page itself is what moved.
	if pdf.page > 0 {
		PlayNavSound()
	}
	return true
}

func pdfNextPage() bool { return pdfGoToPage(pdf.page + 1) }
func pdfPrevPage() bool { return pdfGoToPage(pdf.page - 1) }

// pdfJumpChapter moves to the start of the previous or next chapter. It returns
// false when the document has no chapter structure.
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

// pdfScrollBy moves the paper's text and stops at the top.
func pdfScrollBy(delta float64) {
	pdf.scrollY += delta
	if pdf.scrollY < 0 {
		pdf.scrollY = 0
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
	renderer.SetDrawColor(12, 14, 22, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	doc := pdf.doc
	hasChapters := len(doc.Chapters) > 1

	// The app's one bar owns the top of the screen. The document's own title
	// sits under it as a quiet line, with the page count at its right edge: the
	// bar's caption already names the screen, so the file being read gets its
	// own read-out instead of a second header.
	barH := headerBarHeight()
	contentY := barH + 8
	if titleFont := headerCaptionFont(config); titleFont != nil {
		if title := pdfDocumentTitle(doc); title != "" {
			label := fitTextWidth(renderer, config, titleFont, title, screenWidth-32-220)
			renderText(renderer, config, titleFont, label, ColorTextSecondary(), 16, contentY)
		}
		if doc.Loaded && pdfPageCount() > 0 {
			pageStr := fmt.Sprintf("Page %d / %d", pdf.page, pdfPageCount())
			if doc.Format != "" {
				pageStr = doc.Format + " · " + pageStr
			}
			pw, _ := measureText(titleFont, pageStr)
			renderText(renderer, config, titleFont, pageStr, ColorTextTertiary(),
				screenWidth-pw-16, contentY)
		}
		contentY += int32(titleFont.Height()) + 4

		// The chapter read-out is its own quiet line: a book's chapter name is
		// long, and sharing the title line ellipsised both of them.
		if doc.Loaded && doc.Error == "" && hasChapters {
			if chapter := pdfChapterLabel(); chapter != "" {
				chapter = fitTextWidth(renderer, config, titleFont, chapter, screenWidth-32)
				renderText(renderer, config, titleFont, chapter, ColorTextTertiary(), 16, contentY)
				contentY += int32(titleFont.Height()) + 4
			}
		}
		contentY += 4
	}

	// Navigation rows sit just above the shared footer rather than on top of it,
	// so both stay readable. A book gets a second row for chapters.
	navH := int32(32)
	if hasChapters {
		navH = 68
	}
	navY := screenHeight - footerBarHeight() - navH - 10

	if !doc.Loaded {
		// No document loaded.
		if medFont != nil {
			msg := "No document open"
			mw, _, _ := medFont.SizeUTF8(msg)
			renderText(renderer, config, medFont, msg,
				sdl.Color{R: 140, G: 155, B: 180, A: 200},
				(screenWidth-int32(mw))/2, contentY+60)

			hint := "Open a PDF or EPUB from Files"
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint,
				sdl.Color{R: 100, G: 110, B: 130, A: 150},
				(screenWidth-int32(hw))/2, contentY+90)
		}
	} else if doc.Error != "" {
		if medFont != nil {
			// Loader errors name the file and the cause; wrap them so the
			// reason is not pushed off the right edge.
			lines := pdfWordWrap(medFont, doc.Error, screenWidth-120)
			if len(lines) > 4 {
				lines = lines[:4]
			}
			y := contentY + 40
			for _, line := range lines {
				lw, _, _ := medFont.SizeUTF8(line)
				renderText(renderer, config, medFont, line,
					sdl.Color{R: 200, G: 120, B: 100, A: 220},
					(screenWidth-int32(lw))/2, y)
				y += int32(medFont.Height()) + 2
			}
		}
	} else {
		// Page content.
		if page, ok := pdfCurrentPage(); ok {
			// Page card (simulated paper). Its height is whatever is left
			// between the title lines and the page buttons, so the sheet always
			// fits on screen instead of running under the footer.
			pageW := int32(520)
			if pageW > screenWidth-32 {
				pageW = screenWidth - 32
			}
			pageH := navY - contentY - 8
			if pageH < 200 {
				pageH = 200
			}
			pageX := (screenWidth - pageW) / 2
			pageY := contentY

			// Paper shadow.
			fillRoundedRect(renderer, pageX+4, pageY+4, pageW, pageH, 8,
				sdl.Color{R: 0, G: 0, B: 0, A: 60})

			// Paper background.
			fillRoundedRect(renderer, pageX, pageY, pageW, pageH, 8,
				sdl.Color{R: 240, G: 238, B: 232, A: 255})

			// Text content.
			if textFont, _ := getCachedFont(config, "small"); textFont != nil {
				text := page.Text
				if text == "" {
					if doc.Format == "PDF" {
						text = "(This page has no extractable text — it may be a scanned image.)"
					} else {
						text = "(This page could not be read.)"
					}
				}

				// Word wrap and render. One line box per line and a line count
				// taken from the clipped inner height: at a fixed 18px spacing the
				// 30px text ran through itself and the last line was cut in half
				// against the paper's bottom edge. Blank source lines keep their
				// paragraph gaps instead of collapsing into the next paragraph.
				lines := pdfWrapLines(textFont, text, pageW-48)
				lineH := int32(textFont.Height())
				if lineH < 18 {
					lineH = 18
				}
				startLine := int(pdf.scrollY / float64(lineH))
				maxLines := int((pageH - 48) / lineH)
				if startLine > len(lines) {
					startLine = len(lines)
				}

				renderWithClip(renderer, pageX+24, pageY+24, pageW-48, pageH-48,
					func(r *sdl.Renderer) {
						for i := startLine; i < len(lines) && i < startLine+maxLines; i++ {
							if lines[i] == "" {
								continue
							}
							ly := pageY + 24 + int32(i-startLine)*lineH
							// Dark text on light background.
							renderText(renderer, config, textFont, lines[i],
								sdl.Color{R: 30, G: 30, B: 35, A: 255},
								pageX+24, ly)
						}
					})
			}
		}
	}

	// Navigation controls at bottom.
	if doc.Loaded && doc.Error == "" && pdfPageCount() > 1 {
		ctrlW := int32(360)
		ctrlX := (screenWidth - ctrlW) / 2
		ctrlY := screenHeight - footerBarHeight() - 32 - 10

		if hasChapters {
			ctrlY -= 36
			renderPDFChapterRow(renderer, config, font, ctrlX, ctrlY, ctrlW)
		}
		renderPDFPageRow(renderer, config, font, ctrlX, ctrlY, ctrlW)
	}

	// The shared bars last, exactly as every other screen draws them.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// renderPDFPageRow draws Prev / page count / Next.
func renderPDFPageRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, ctrlX, ctrlY, ctrlW int32) {
	const btnW = int32(108)
	const btnH = int32(32)
	count := pdfPageCount()

	prevFill := sdl.Color{R: 30, G: 36, B: 50, A: 200}
	if pdf.page <= 1 {
		prevFill = sdl.Color{R: 20, G: 24, B: 30, A: 150}
	}
	UIButton(renderer, config, sdl.Rect{X: ctrlX, Y: ctrlY, W: btnW, H: btnH}, "< Prev", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       prevFill,
		HoverFill:  prevFill,
		FocusFill:  prevFill,
		Border:     ColorBorder,
		LabelColor: sdl.Color{R: 180, G: 190, B: 210, A: 200},
		Font:       font,
		Radius:     RadiusSM,
		NoShadow:   true,
	}, UIButtonState{Disabled: pdf.page <= 1})

	numStr := fmt.Sprintf("%d / %d", pdf.page, count)
	nw, _, _ := font.SizeUTF8(numStr)
	renderText(renderer, config, font, numStr,
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
		ctrlX+(ctrlW-int32(nw))/2, ctrlY+8)

	nextFill := sdl.Color{R: 30, G: 36, B: 50, A: 200}
	if pdf.page >= count {
		nextFill = sdl.Color{R: 20, G: 24, B: 30, A: 150}
	}
	UIButton(renderer, config, sdl.Rect{X: ctrlX + ctrlW - btnW, Y: ctrlY, W: btnW, H: btnH}, "Next >", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       nextFill,
		HoverFill:  nextFill,
		FocusFill:  nextFill,
		Border:     ColorBorder,
		LabelColor: sdl.Color{R: 180, G: 190, B: 210, A: 200},
		Font:       font,
		Radius:     RadiusSM,
		NoShadow:   true,
	}, UIButtonState{Disabled: pdf.page >= count})
}

// renderPDFChapterRow draws ‹ Chapter / name / Chapter › for a book.
func renderPDFChapterRow(renderer *sdl.Renderer, config *Config, font *ttf.Font, ctrlX, ctrlY, ctrlW int32) {
	if len(pdf.doc.Chapters) == 0 {
		return
	}
	const btnW = int32(108)
	const btnH = int32(28)
	_, idx := pdfChapterForPage(pdf.page)

	fill := sdl.Color{R: 26, G: 32, B: 44, A: 200}
	label := sdl.Color{R: 170, G: 182, B: 205, A: 200}
	UIButton(renderer, config, sdl.Rect{X: ctrlX, Y: ctrlY, W: btnW, H: btnH}, "< Chapter", UIButtonStyle{
		Variant: UIButtonCustom, Fill: fill, HoverFill: fill, FocusFill: fill,
		Border: ColorBorder, LabelColor: label, Font: font, Radius: RadiusSM, NoShadow: true,
	}, UIButtonState{Disabled: idx <= 0})

	chapterStr := fmt.Sprintf("Chapter %d / %d", idx+1, len(pdf.doc.Chapters))
	cw, _, _ := font.SizeUTF8(chapterStr)
	renderText(renderer, config, font, chapterStr,
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
		ctrlX+(ctrlW-int32(cw))/2, ctrlY+6)

	UIButton(renderer, config, sdl.Rect{X: ctrlX + ctrlW - btnW, Y: ctrlY, W: btnW, H: btnH}, "Chapter >", UIButtonStyle{
		Variant: UIButtonCustom, Fill: fill, HoverFill: fill, FocusFill: fill,
		Border: ColorBorder, LabelColor: label, Font: font, Radius: RadiusSM, NoShadow: true,
	}, UIButtonState{Disabled: idx < 0 || idx >= len(pdf.doc.Chapters)-1})
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

func handlePDFInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}

	// Pages with more text than the paper shows scroll first, then turn: a key
	// press on a long page should not skip the half of it that was off screen.
	scrolled := func(delta float64) bool {
		page, ok := pdfCurrentPage()
		if !ok {
			return false
		}
		font, _ := getCachedFont(config, "small")
		if font == nil {
			return false
		}
		lineH := int32(font.Height())
		if lineH < 18 {
			lineH = 18
		}
		lines := pdfWrapLines(font, page.Text, int32(520)-48)
		visible := int((screenHeight - footerBarHeight() - 60) / lineH)
		maxScroll := float64((len(lines) - visible) * int(lineH))
		if maxScroll < 0 {
			maxScroll = 0
		}
		if delta > 0 && pdf.scrollY+delta > maxScroll {
			return false
		}
		if delta < 0 && pdf.scrollY <= 0 {
			return false
		}
		pdf.scrollY += delta
		if pdf.scrollY < 0 {
			pdf.scrollY = 0
		}
		return true
	}

	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		pdfPrevPage()
	case sdl.K_RIGHT:
		pdfNextPage()
	case sdl.K_PAGEUP:
		if !scrolled(-240) {
			pdfPrevPage()
		}
	case sdl.K_PAGEDOWN:
		if !scrolled(240) {
			pdfNextPage()
		}
	case sdl.K_UP:
		pdfScrollBy(-60)
	case sdl.K_DOWN:
		pdfScrollBy(60)
	case sdl.K_HOME:
		pdfGoToPage(1)
	case sdl.K_END:
		pdfGoToPage(pdfPageCount())
	case sdl.K_LEFTBRACKET:
		pdfJumpChapter(-1)
	case sdl.K_RIGHTBRACKET:
		pdfJumpChapter(1)
	case sdl.K_ESCAPE, sdl.K_b:
		goBackScene(config)
		PlayBackSound()
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlPDFReader(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		pdfScrollBy(-60)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		pdfScrollBy(60)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		pdfPrevPage()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		pdfNextPage()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// A book's shoulders turn chapters; a PDF has no chapters, so they turn
		// pages there and nothing feels dead.
		if len(pdf.doc.Chapters) > 0 {
			pdfJumpChapter(-1)
		} else {
			pdfPrevPage()
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if len(pdf.doc.Chapters) > 0 {
			pdfJumpChapter(1)
		} else {
			pdfNextPage()
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
