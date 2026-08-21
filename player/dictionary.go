package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Dictionary — word lookup with definitions, synonyms, pronunciation
// Uses Datamuse API (free, no key) + offline fallback
// ──────────────────────────────────────────────────────────────────────

type dictDef struct {
	Word     string   `json:"word"`
	Score    int      `json:"score"`
	Defs     []string `json:"defs"` // "part_of_speech\tdefinition"
	Synonyms []string `json:"synonyms"`
	Antonyms []string `json:"antonyms"`
	Related  []string `json:"rel_syn"`
	IPA      string   `json:"ipa"`
	Tags     []string `json:"tags"`
}

type dictResult struct {
	Query     string
	Word      string
	Defs      []string
	Synonyms  []string
	Antonyms  []string
	Parts     []string
	Pronounce string
	Found     bool
	Error     string
	Cached    bool
}

type dictState struct {
	query       string
	cursor      int
	result      dictResult
	history     []string
	histIdx     int
	searching   bool
	results     []dictResult
	showResults bool
}

var dict dictState

func dictInit() {
	dict = dictState{}
}

// dictLookup fetches a word definition from the Datamuse API.
func dictLookup(word string) dictResult {
	word = strings.TrimSpace(strings.ToLower(word))
	if word == "" {
		return dictResult{Error: "Enter a word to look up"}
	}

	// Check cache first.
	cacheKey := "dict:" + word
	if db, err := cacheOpen("jukahub.cache"); err == nil {
		if cached, cErr := cacheGet(db, cacheKey); cErr == nil && cached != nil {
			var result dictResult
			if json.Unmarshal(cached, &result) == nil && result.Found {
				result.Cached = true
				db.Close()
				return result
			}
		}
		db.Close()
	}

	// Fetch definitions.
	url := fmt.Sprintf("https://api.datamuse.com/words?sp=%s&md=dps&max=1", word)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return dictResult{Query: word, Error: "Network error: " + err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return dictResult{Query: word, Error: "Read error"}
	}

	var words []dictDef
	if err := json.Unmarshal(body, &words); err != nil {
		return dictResult{Query: word, Error: "Parse error"}
	}

	if len(words) == 0 {
		// Try related words.
		return dictResult{Query: word, Error: "Word not found: " + word}
	}

	w := words[0]
	result := dictResult{
		Query:     word,
		Word:      w.Word,
		Found:     true,
		Pronounce: w.IPA,
	}

	for _, d := range w.Defs {
		parts := strings.SplitN(d, "\t", 2)
		if len(parts) == 2 {
			result.Defs = append(result.Defs, fmt.Sprintf("(%s) %s", parts[0], parts[1]))
			result.Parts = append(result.Parts, parts[0])
		}
	}

	// Fetch synonyms.
	synURL := fmt.Sprintf("https://api.datamuse.com/words?rel_syn=%s&max=8", word)
	if synResp, err := client.Get(synURL); err == nil {
		defer synResp.Body.Close()
		if synBody, err := io.ReadAll(synResp.Body); err == nil {
			var syns []dictDef
			if json.Unmarshal(synBody, &syns) == nil {
				for _, s := range syns {
					result.Synonyms = append(result.Synonyms, s.Word)
				}
			}
		}
	}

	// Fetch antonyms.
	antURL := fmt.Sprintf("https://api.datamuse.com/words?rel_ant=%s&max=5", word)
	if antResp, err := client.Get(antURL); err == nil {
		defer antResp.Body.Close()
		if antBody, err := io.ReadAll(antResp.Body); err == nil {
			var ants []dictDef
			if json.Unmarshal(antBody, &ants) == nil {
				for _, a := range ants {
					result.Antonyms = append(result.Antonyms, a.Word)
				}
			}
		}
	}

	// Cache the result.
	if data, err := json.Marshal(result); err == nil {
		if db, err := cacheOpen("jukahub.cache"); err == nil {
			_ = cacheSet(db, cacheKey, data, 24*time.Hour)
			db.Close()
		}
	}

	return result
}

// dictSuggest fetches autocomplete suggestions.
func dictSuggest(prefix string) []string {
	if len(prefix) < 2 {
		return nil
	}
	url := fmt.Sprintf("https://api.datamuse.com/sug?s=%s&max=6", prefix)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var suggestions []struct {
		Word string `json:"word"`
	}
	if err := json.Unmarshal(body, &suggestions); err != nil {
		return nil
	}

	var words []string
	for _, s := range suggestions {
		words = append(words, s.Word)
	}
	return words
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

func renderDictionary(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}

	// Search bar at top.
	searchW := int32(600)
	searchH := int32(44)
	searchX := (screenWidth - searchW) / 2
	searchY := int32(30)

	drawCard(renderer, searchX, searchY, searchW, searchH, 12)

	// Search icon.
	renderText(renderer, config, font, "[S]",
		sdl.Color{R: 100, G: 115, B: 140, A: 180}, searchX+12, searchY+10)

	// Search text with cursor.
	searchText := dict.query
	if dict.cursor == 0 && searchText == "" {
		searchText = "Type a word..."
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 80, G: 90, B: 110, A: 150}, searchX+36, searchY+12)
	} else {
		renderText(renderer, config, font, searchText,
			sdl.Color{R: 220, G: 230, B: 245, A: 255}, searchX+36, searchY+12)
		// Blinking cursor.
		if time.Now().UnixMilli()%1000 < 500 {
			cw, _, _ := font.SizeUTF8(searchText)
			cursorX := searchX + 36 + int32(cw)
			renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 200)
			renderer.FillRect(&sdl.Rect{X: cursorX, Y: searchY + 8, W: 2, H: searchH - 16})
		}
	}

	// Autocomplete suggestions.
	if len(dict.query) >= 2 && !dict.result.Found {
		suggestions := dictSuggest(dict.query)
		if len(suggestions) > 0 {
			sugY := searchY + searchH + 8
			sugW := searchW
			sugH := int32(len(suggestions)*30 + 8)
			fillRoundedRect(renderer, searchX, sugY, sugW, sugH, 10,
				sdl.Color{R: 16, G: 20, B: 30, A: 245})
			strokeRoundedRect(renderer, searchX, sugY, sugW, sugH, 10, 1, ColorBorder)

			for i, s := range suggestions {
				sy := sugY + 4 + int32(i)*30
				// Highlight if matches query exactly.
				col := sdl.Color{R: 160, G: 175, B: 200, A: 200}
				if strings.ToLower(s) == strings.ToLower(dict.query) {
					col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 220}
				}
				renderText(renderer, config, font, s, col, searchX+16, sy)
			}
		}
	}

	// Result area.
	resultY := searchY + searchH + 20
	resultX := int32(60)
	resultW := screenWidth - 120
	resultH := screenHeight - resultY - 60

	drawCard(renderer, resultX, resultY, resultW, resultH, 16)

	if dict.searching {
		// Loading indicator.
		if medFont != nil {
			dots := strings.Repeat(".", int(time.Now().UnixMilli()/400)%4)
			loading := "Looking up" + dots
			lw, _, _ := medFont.SizeUTF8(loading)
			renderText(renderer, config, medFont, loading,
				sdl.Color{R: 140, G: 155, B: 180, A: 200},
				(screenWidth-int32(lw))/2, resultY+40)
		}
	} else if dict.result.Error != "" {
		// Error message.
		if medFont != nil {
			ew, _, _ := medFont.SizeUTF8(dict.result.Error)
			renderText(renderer, config, medFont, dict.result.Error,
				sdl.Color{R: 200, G: 120, B: 100, A: 220},
				(screenWidth-int32(ew))/2, resultY+40)
		}
		// Suggestion.
		if font != nil {
			hint := "Try another word, or check your spelling"
			hw, _, _ := font.SizeUTF8(hint)
			renderText(renderer, config, font, hint,
				sdl.Color{R: 100, G: 110, B: 130, A: 150},
				(screenWidth-int32(hw))/2, resultY+70)
		}
	} else if dict.result.Found {
		r := dict.result
		ry := resultY + 20
		contentX := resultX + 24

		// Word.
		if medFont != nil {
			wordCol := sdl.Color{R: 240, G: 245, B: 255, A: 255}
			renderText(renderer, config, medFont, r.Word, wordCol, contentX, ry)
			ry += 32
		}

		// Pronunciation.
		if r.Pronounce != "" && font != nil {
			pronStr := "/" + r.Pronounce + "/"
			renderText(renderer, config, font, pronStr,
				sdl.Color{R: 140, G: 160, B: 200, A: 180}, contentX, ry)
			ry += 22
		}

		// Parts of speech badge.
		if len(r.Parts) > 0 && font != nil {
			unique := make(map[string]bool)
			for _, p := range r.Parts {
				unique[p] = true
			}
			badgeX := contentX
			for p := range unique {
				badgeW := int32(len(p)*8 + 16)
				fillRoundedRect(renderer, badgeX, ry, badgeW, 20, 10,
					sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 40})
				renderText(renderer, config, font, p,
					sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 200},
					badgeX+8, ry+2)
				badgeX += badgeW + 8
			}
			ry += 30
		}

		// Separator.
		renderer.SetDrawColor(40, 50, 65, 80)
		renderer.FillRect(&sdl.Rect{X: contentX, Y: ry, W: resultW - 48, H: 1})
		ry += 12

		// Definitions.
		if font != nil {
			for i, def := range r.Defs {
				if ry > resultY+resultH-40 {
					break
				}
				// Definition number.
				numStr := fmt.Sprintf("%d.", i+1)
				numW, _, _ := font.SizeUTF8(numStr)
				renderText(renderer, config, font, numStr,
					sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 180},
					contentX, ry)
				// Definition text.
				renderText(renderer, config, font, def,
					sdl.Color{R: 180, G: 190, B: 210, A: 220},
					contentX+int32(numW)+8, ry)
				ry += 24
			}
		}

		// Synonyms.
		if len(r.Synonyms) > 0 && font != nil {
			ry += 8
			if ry > resultY+resultH-40 {
				return
			}
			synLabel := "Synonyms:"
			renderText(renderer, config, font, synLabel,
				sdl.Color{R: 100, G: 200, B: 140, A: 200}, contentX, ry)
			ry += 22

			synX := contentX
			for _, s := range r.Synonyms {
				if synX > resultX+resultW-100 {
					synX = contentX
					ry += 22
				}
				if ry > resultY+resultH-20 {
					break
				}
				// Pill.
				sw, _, _ := font.SizeUTF8(s)
				pillW := int32(sw) + 16
				fillRoundedRect(renderer, synX, ry, pillW, 20, 10,
					sdl.Color{R: 40, G: 60, B: 50, A: 150})
				renderText(renderer, config, font, s,
					sdl.Color{R: 120, G: 200, B: 140, A: 220}, synX+8, ry+2)
				synX += pillW + 8
			}
		}

		// Antonyms.
		if len(r.Antonyms) > 0 && font != nil {
			ry += 26
			if ry > resultY+resultH-40 {
				return
			}
			antLabel := "Antonyms:"
			renderText(renderer, config, font, antLabel,
				sdl.Color{R: 220, G: 140, B: 120, A: 200}, contentX, ry)
			ry += 22

			antX := contentX
			for _, a := range r.Antonyms {
				if antX > resultX+resultW-100 {
					antX = contentX
					ry += 22
				}
				if ry > resultY+resultH-20 {
					break
				}
				aw, _, _ := font.SizeUTF8(a)
				pillW := int32(aw) + 16
				fillRoundedRect(renderer, antX, ry, pillW, 20, 10,
					sdl.Color{R: 60, G: 35, B: 35, A: 150})
				renderText(renderer, config, font, a,
					sdl.Color{R: 220, G: 140, B: 120, A: 220}, antX+8, ry+2)
				antX += pillW + 8
			}
		}
	} else {
		// Empty state.
		if medFont != nil {
			hint := "Look up any English word"
			hw, _, _ := medFont.SizeUTF8(hint)
			renderText(renderer, config, medFont, hint,
				sdl.Color{R: 100, G: 115, B: 140, A: 150},
				(screenWidth-int32(hw))/2, resultY+40)
		}
		if font != nil {
			features := "Definitions • Synonyms • Antonyms • Pronunciation"
			fw, _, _ := font.SizeUTF8(features)
			renderText(renderer, config, font, features,
				sdl.Color{R: 80, G: 90, B: 110, A: 120},
				(screenWidth-int32(fw))/2, resultY+70)
		}
	}

	// Controls.
	if font != nil {
		controls := "Type to search | Enter: Look up | Esc: Back"
		cw, _, _ := font.SizeUTF8(controls)
		renderText(renderer, config, font, controls,
			sdl.Color{R: 80, G: 90, B: 110, A: 120},
			(screenWidth-int32(cw))/2, screenHeight-30)
	}
}

func handleDictInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_RETURN:
		if dict.query != "" {
			dict.searching = true
			go func() {
				result := dictLookup(dict.query)
				dict.result = result
				dict.searching = false
				// Add to history.
				if result.Found {
					dict.history = append([]string{dict.query}, dict.history...)
					if len(dict.history) > 20 {
						dict.history = dict.history[:20]
					}
				}
			}()
		}
	case sdl.K_BACKSPACE:
		if len(dict.query) > 0 {
			dict.query = dict.query[:len(dict.query)-1]
		}
	case sdl.K_ESCAPE, sdl.K_b:
		if dict.query != "" {
			dict.query = ""
			dict.result = dictResult{}
		} else {
			goBackScene(config)
			PlayBackSound()
		}
	}
}

func handleDictTextInput(e *sdl.TextInputEvent) {
	for _, r := range string(e.Text[:]) {
		if unicode.IsLetter(r) || r == ' ' || r == '-' {
			if len(dict.query) < 60 {
				dict.query += string(r)
			}
		}
	}
}
