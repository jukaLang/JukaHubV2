package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// PDF Reader — lightweight text extraction and navigation
// ──────────────────────────────────────────────────────────────────────

type pdfPage struct {
	Text    string
	Width   float64
	Height  float64
	Objects []pdfObject
}

type pdfObject struct {
	Type string // "text", "path", "image"
	Text string
	X, Y float64
	Font string
	Size float64
}

type pdfDoc struct {
	Path     string
	Pages    []pdfPage
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

func pdfInit() {
	pdf = pdfState{page: 1, zoom: 1.0, viewMode: 1}
}

// pdfExtractText extracts text from a PDF file using raw parsing.
// This is a simplified extractor that handles most common PDFs.
func pdfExtractText(path string) pdfDoc {
	data, err := os.ReadFile(path)
	if err != nil {
		return pdfDoc{Path: path, Error: "Cannot open file: " + err.Error()}
	}

	doc := pdfDoc{Path: path, Loaded: true}

	// Extract metadata.
	if idx := strings.Index(string(data), "/Title"); idx >= 0 {
		end := strings.IndexByte(string(data[idx:]), '\n')
		if end > 0 {
			doc.Title = strings.Trim(string(data[idx:idx+end]), "/Title ()\r\n")
		}
	}
	if idx := strings.Index(string(data), "/Author"); idx >= 0 {
		end := strings.IndexByte(string(data[idx:]), '\n')
		if end > 0 {
			doc.Author = strings.Trim(string(data[idx:idx+end]), "/Author ()\r\n")
		}
	}

	// Count pages.
	pageCount := 0
	for i := 0; i < len(data)-5; i++ {
		if string(data[i:i+5]) == "/Type" {
			// Look for /Page
			j := i + 5
			for j < len(data) && data[j] == ' ' {
				j++
			}
			if j+4 <= len(data) && string(data[j:j+4]) == "/Pag" {
				// Check if it's /Pages (parent) or /Page (leaf)
				k := j + 4
				if k < len(data) && data[k] != 's' {
					pageCount++
				}
			}
		}
	}
	if pageCount == 0 {
		pageCount = 1
	}
	doc.NumPages = pageCount

	// Extract text from each page.
	doc.Pages = make([]pdfPage, pageCount)
	for i := 0; i < pageCount; i++ {
		doc.Pages[i] = pdfExtractPage(data, i+1)
	}

	// Set title from filename if not found.
	if doc.Title == "" {
		parts := strings.Split(path, "/\\")
		doc.Title = parts[len(parts)-1]
	}

	return doc
}

func pdfExtractPage(data []byte, pageNum int) pdfPage {
	page := pdfPage{
		Width:  612, // Default letter size
		Height: 792,
	}

	// Find page boundaries (simplified).
	str := string(data)

	// Look for page content stream.
	text := pdfExtractStreamText(str, pageNum)
	page.Text = text

	return page
}

func pdfExtractStreamText(data string, pageNum int) string {
	var texts []string

	// Look for BT...ET blocks (text objects).
	i := 0
	pageCount := 0
	for i < len(data)-3 {
		// Count page references.
		if i < len(data)-5 && data[i:i+5] == "/Page" {
			if i+5 >= len(data) || data[i+5] != 's' {
				pageCount++
			}
			i += 5
			continue
		}

		// Find BT (Begin Text).
		if i < len(data)-2 && data[i:i+2] == "BT" {
			// Find matching ET.
			j := i + 2
			for j < len(data)-1 {
				if data[j:j+2] == "ET" {
					// Extract text from this block.
					block := data[i : j+2]
					text := pdfParseTextBlock(block)
					if text != "" {
						texts = append(texts, text)
					}
					i = j + 2
					break
				}
				j++
			}
			if j >= len(data)-1 {
				break
			}
			continue
		}
		i++
	}

	return strings.Join(texts, "\n")
}

func pdfParseTextBlock(block string) string {
	var parts []string
	lines := strings.Split(block, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for Tj (show text) or TJ (show text array) operators.
		if strings.HasSuffix(line, "Tj") {
			// Single string: (text) Tj
			text := strings.TrimSuffix(line, "Tj")
			text = strings.TrimSpace(text)
			text = strings.Trim(text, "()")
			if text != "" {
				parts = append(parts, text)
			}
		} else if strings.HasSuffix(line, "TJ") {
			// Array: [(text) (text)] TJ
			text := strings.TrimSuffix(line, "TJ")
			text = strings.TrimSpace(text)
			// Extract strings from array.
			for len(text) > 0 {
				start := strings.IndexByte(text, '(')
				if start < 0 {
					break
				}
				end := strings.IndexByte(text[start:], ')')
				if end < 0 {
					break
				}
				str := text[start+1 : start+end]
				if str != "" {
					parts = append(parts, str)
				}
				text = text[start+end+1:]
			}
		}
	}

	return strings.Join(parts, " ")
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderPDFReader(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(12, 14, 22, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	doc := pdf.doc

	// Header bar.
	barH := int32(44)
	renderer.SetDrawColor(14, 18, 28, 240)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: barH})
	renderer.SetDrawColor(40, 50, 65, 120)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	// Title.
	title := doc.Title
	if len(title) > 40 {
		title = title[:40] + "..."
	}
	if medFont != nil {
		renderText(renderer, config, medFont, title,
			sdl.Color{R: 220, G: 230, B: 245, A: 240}, 16, 10)
	}

	// Page indicator.
	if doc.Loaded && doc.NumPages > 0 {
		pageStr := fmt.Sprintf("Page %d / %d", pdf.page, doc.NumPages)
		pw, _, _ := font.SizeUTF8(pageStr)
		renderText(renderer, config, font, pageStr,
			sdl.Color{R: 160, G: 175, B: 200, A: 200},
			screenWidth-int32(pw)-16, 14)
	}

	// Content area.
	contentY := barH + 8

	if !doc.Loaded {
		// No document loaded.
		if medFont != nil {
			msg := "No PDF loaded"
			mw, _, _ := medFont.SizeUTF8(msg)
			renderText(renderer, config, medFont, msg,
				sdl.Color{R: 140, G: 155, B: 180, A: 200},
				(screenWidth-int32(mw))/2, contentY+60)

			hint := "Open a PDF from File Explorer"
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint,
				sdl.Color{R: 100, G: 110, B: 130, A: 150},
				(screenWidth-int32(hw))/2, contentY+90)
		}
	} else if doc.Error != "" {
		if medFont != nil {
			ew, _, _ := medFont.SizeUTF8(doc.Error)
			renderText(renderer, config, medFont, doc.Error,
				sdl.Color{R: 200, G: 120, B: 100, A: 220},
				(screenWidth-int32(ew))/2, contentY+60)
		}
	} else {
		// Page content.
		pageIdx := pdf.page - 1
		if pageIdx >= 0 && pageIdx < len(doc.Pages) {
			page := doc.Pages[pageIdx]

			// Page card (simulated paper).
			pageW := int32(520)
			pageH := int32(640)
			pageX := (screenWidth - pageW) / 2
			pageY := contentY

			// Paper shadow.
			fillRoundedRect(renderer, pageX+4, pageY+4, pageW, pageH, 8,
				sdl.Color{R: 0, G: 0, B: 0, A: 60})

			// Paper background.
			fillRoundedRect(renderer, pageX, pageY, pageW, pageH, 8,
				sdl.Color{R: 240, G: 238, B: 232, A: 255})

			// Text content.
			textFont, _ := getCachedFont(config, "small")
			if textFont != nil {
				text := page.Text
				if text == "" {
					text = "(Page content could not be extracted)"
				}

				// Word wrap and render.
				lines := pdfWordWrap(textFont, text, pageW-48)
				lineH := int32(18)
				startLine := int(pdf.scrollY / float64(lineH))
				maxLines := int(pageH / lineH)

				renderWithClip(renderer, pageX+24, pageY+24, pageW-48, pageH-48,
					func(r *sdl.Renderer) {
						for i, line := range lines {
							if i < startLine {
								continue
							}
							if i >= startLine+maxLines {
								break
							}
							ly := pageY + 24 + int32(i-startLine)*lineH
							// Dark text on light background.
							renderText(renderer, config, textFont, line,
								sdl.Color{R: 30, G: 30, B: 35, A: 255},
								pageX+24, ly)
						}
					})
			}

			// Scroll indicator.
			if len(page.Text) > 500 {
				scrollStr := fmt.Sprintf("Scroll: PgUp/PgDn")
				sw, _, _ := font.SizeUTF8(scrollStr)
				renderText(renderer, config, font, scrollStr,
					sdl.Color{R: 100, G: 110, B: 130, A: 140},
					(screenWidth-int32(sw))/2, pageY+pageH+8)
			}
		}
	}

	// Navigation controls at bottom.
	ctrlY := screenHeight - 48
	if doc.Loaded && doc.NumPages > 1 {
		ctrlW := int32(300)
		ctrlX := (screenWidth - ctrlW) / 2

		// Prev button.
		prevW := int32(80)
		prevH := int32(32)
		prevX := ctrlX
		prevFill := sdl.Color{R: 30, G: 36, B: 50, A: 200}
		if pdf.page <= 1 {
			prevFill = sdl.Color{R: 20, G: 24, B: 30, A: 150}
		}
		fillRoundedRect(renderer, prevX, ctrlY, prevW, prevH, 8, prevFill)
		strokeRoundedRect(renderer, prevX, ctrlY, prevW, prevH, 8, 1, ColorBorder)
		prevStr := "< Prev"
		pnw, _, _ := font.SizeUTF8(prevStr)
		renderText(renderer, config, font, prevStr,
			sdl.Color{R: 180, G: 190, B: 210, A: 200},
			prevX+(prevW-int32(pnw))/2, ctrlY+6)

		// Page number.
		numStr := fmt.Sprintf("%d / %d", pdf.page, doc.NumPages)
		nw, _, _ := font.SizeUTF8(numStr)
		renderText(renderer, config, font, numStr,
			sdl.Color{R: 200, G: 210, B: 230, A: 220},
			ctrlX+(ctrlW-int32(nw))/2, ctrlY+8)

		// Next button.
		nextW := int32(80)
		nextX := ctrlX + ctrlW - nextW
		nextFill := sdl.Color{R: 30, G: 36, B: 50, A: 200}
		if pdf.page >= doc.NumPages {
			nextFill = sdl.Color{R: 20, G: 24, B: 30, A: 150}
		}
		fillRoundedRect(renderer, nextX, ctrlY, nextW, prevH, 8, nextFill)
		strokeRoundedRect(renderer, nextX, ctrlY, nextW, prevH, 8, 1, ColorBorder)
		nextStr := "Next >"
		nnw, _, _ := font.SizeUTF8(nextStr)
		renderText(renderer, config, font, nextStr,
			sdl.Color{R: 180, G: 190, B: 210, A: 200},
			nextX+(nextW-int32(nnw))/2, ctrlY+6)

		tb.startRect = sdl.Rect{} // Avoid conflict
	}

	// Controls hint.
	if font != nil {
		controls := "← Prev Page | → Next Page | PgUp/PgDn: Scroll | B: Back"
		cw, _, _ := font.SizeUTF8(controls)
		renderText(renderer, config, font, controls,
			sdl.Color{R: 80, G: 90, B: 110, A: 100},
			(screenWidth-int32(cw))/2, screenHeight-22)
	}
}

func pdfWordWrap(font *ttf.Font, text string, maxWidth int32) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	var lines []string
	var currentLine strings.Builder
	var currentWidth int

	for _, word := range words {
		ww, _, _ := font.SizeUTF8(word + " ")
		if int32(currentWidth)+int32(ww) > maxWidth && currentLine.Len() > 0 {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentWidth = 0
		}
		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
			currentWidth += 8 // space width
		}
		currentLine.WriteString(word)
		currentWidth += ww
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

func handlePDFInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}

	doc := pdf.doc
	switch e.Keysym.Sym {
	case sdl.K_LEFT, sdl.K_PAGEUP:
		if pdf.page > 1 {
			pdf.page--
			pdf.scrollY = 0
			PlayNavSound()
		}
	case sdl.K_RIGHT, sdl.K_PAGEDOWN:
		if pdf.page < doc.NumPages {
			pdf.page++
			pdf.scrollY = 0
			PlayNavSound()
		}
	case sdl.K_UP:
		pdf.scrollY -= 60
		if pdf.scrollY < 0 {
			pdf.scrollY = 0
		}
	case sdl.K_DOWN:
		pdf.scrollY += 60
	case sdl.K_HOME:
		pdf.page = 1
		pdf.scrollY = 0
	case sdl.K_END:
		pdf.page = doc.NumPages
		pdf.scrollY = 0
	case sdl.K_ESCAPE, sdl.K_b:
		goBackScene(config)
		PlayBackSound()
	}
}

// pdfOpenFile opens a PDF file from the file explorer.
func pdfOpenFile(path string) {
	if !strings.HasSuffix(strings.ToLower(path), ".pdf") {
		return
	}
	pdfInit()
	pdf.doc = pdfExtractText(path)
	pdf.page = 1
	pdf.scrollY = 0
}
