package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// dictAsync carries the network work off the event thread. Suggestions used
// to be fetched from the render function — one blocking HTTP request per
// frame once two letters were typed — and a lookup goroutine wrote dict.result
// and dict.history while the frame was reading them.
var dictAsync struct {
	mu       sync.Mutex
	gen      uint64      // bumped per lookup; a stale answer is dropped
	done     *dictResult // a finished lookup waiting for dictDrain
	sugg     map[string][]string
	suggBusy bool
}

// dictStartLookup looks a word up in the background; dictDrain applies it.
func dictStartLookup(word string) {
	dict.searching = true
	dictAsync.mu.Lock()
	dictAsync.gen++
	gen := dictAsync.gen
	dictAsync.mu.Unlock()
	go func() {
		result := dictLookup(word)
		dictAsync.mu.Lock()
		if gen == dictAsync.gen {
			dictAsync.done = &result
		}
		dictAsync.mu.Unlock()
	}()
}

// dictCancelLookup drops a lookup still in flight (the query was cleared).
func dictCancelLookup() {
	dictAsync.mu.Lock()
	dictAsync.gen++
	dictAsync.done = nil
	dictAsync.mu.Unlock()
	dict.searching = false
}

// dictDrain applies a finished lookup. Render thread.
func dictDrain() {
	dictAsync.mu.Lock()
	r := dictAsync.done
	dictAsync.done = nil
	dictAsync.mu.Unlock()
	if r == nil {
		return
	}
	dict.result = *r
	dict.searching = false
	if r.Found {
		dict.history = append([]string{r.Query}, dict.history...)
		if len(dict.history) > 20 {
			dict.history = dict.history[:20]
		}
	}
}

// dictSuggestions returns the suggestions already fetched for query, and starts
// the fetch the first time a query is seen. One request at a time: while one
// is out, the newer queries simply wait for the next frame. Render thread.
func dictSuggestions(query string) []string {
	dictAsync.mu.Lock()
	defer dictAsync.mu.Unlock()
	if s, ok := dictAsync.sugg[query]; ok {
		return s
	}
	if !dictAsync.suggBusy {
		dictAsync.suggBusy = true
		go func() {
			s := dictSuggest(query)
			dictAsync.mu.Lock()
			if dictAsync.sugg == nil || len(dictAsync.sugg) > 200 {
				dictAsync.sugg = map[string][]string{}
			}
			dictAsync.sugg[query] = s
			dictAsync.suggBusy = false
			dictAsync.mu.Unlock()
		}()
	}
	return nil
}

// dictLookup fetches a word definition from the Datamuse API.
func dictLookup(word string) dictResult {
	word = strings.TrimSpace(strings.ToLower(word))
	if word == "" {
		return dictResult{Error: "Enter a word to look up"}
	}

	// Check cache first.
	cacheKey := "dict:" + word
	// The handle is the app's shared cache: it is not closed here. Closing it
	// shut the database every other cache user holds (thumbnails, feeds), and
	// they failed for the rest of the session.
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		if cached, cErr := cacheGet(db, cacheKey); cErr == nil && cached != nil {
			var result dictResult
			if json.Unmarshal(cached, &result) == nil && result.Found {
				result.Cached = true
				return result
			}
		}
	}

	// Fetch definitions. The word is escaped: a space or an "&" in it used to
	// end or split the query.
	q := url.QueryEscape(word)
	defsURL := fmt.Sprintf("https://api.datamuse.com/words?sp=%s&md=dps&max=1", q)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(defsURL)
	if err != nil {
		return dictResult{Query: word, Error: "Network error: " + err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	synURL := fmt.Sprintf("https://api.datamuse.com/words?rel_syn=%s&max=8", q)
	if synResp, err := client.Get(synURL); err == nil {
		defer synResp.Body.Close()
		if synBody, err := io.ReadAll(io.LimitReader(synResp.Body, 1<<20)); err == nil {
			var syns []dictDef
			if json.Unmarshal(synBody, &syns) == nil {
				for _, s := range syns {
					result.Synonyms = append(result.Synonyms, s.Word)
				}
			}
		}
	}

	// Fetch antonyms.
	antURL := fmt.Sprintf("https://api.datamuse.com/words?rel_ant=%s&max=5", q)
	if antResp, err := client.Get(antURL); err == nil {
		defer antResp.Body.Close()
		if antBody, err := io.ReadAll(io.LimitReader(antResp.Body, 1<<20)); err == nil {
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
		if db, err := openSharedCache("jukahub.cache"); err == nil {
			_ = cacheSet(db, cacheKey, data, 24*time.Hour)
		}
	}

	return result
}

// dictSuggest fetches autocomplete suggestions.
func dictSuggest(prefix string) []string {
	if len(prefix) < 2 {
		return nil
	}
	sugURL := fmt.Sprintf("https://api.datamuse.com/sug?s=%s&max=6", url.QueryEscape(prefix))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(sugURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	dictDrain()
	// Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

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
		WithAlpha(ColorTextTertiary(), 180), searchX+12, searchY+10)

	// Search text with cursor.
	searchText := dict.query
	if dict.cursor == 0 && searchText == "" {
		searchText = "Type a word..."
		renderText(renderer, config, font, searchText,
			WithAlpha(ColorTextTertiary(), 150), searchX+36, searchY+12)
	} else {
		renderText(renderer, config, font, searchText,
			ColorTextPrimary(), searchX+36, searchY+12)
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
		suggestions := dictSuggestions(dict.query)
		if len(suggestions) > 0 {
			sugY := searchY + searchH + 8
			sugW := searchW
			sugH := int32(len(suggestions)*30 + 8)
			fillRoundedRect(renderer, searchX, sugY, sugW, sugH, 10,
				overlayCardFill())
			strokeRoundedRect(renderer, searchX, sugY, sugW, sugH, 10, 1, ColorBorder)

			for i, s := range suggestions {
				sy := sugY + 4 + int32(i)*30
				// Highlight if matches query exactly.
				col := WithAlpha(ColorTextSecondary(), 200)
				if strings.EqualFold(s, dict.query) {
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
				WithAlpha(ColorTextSecondary(), 200),
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
				WithAlpha(ColorTextTertiary(), 150),
				(screenWidth-int32(hw))/2, resultY+70)
		}
	} else if dict.result.Found {
		r := dict.result
		ry := resultY + 20
		contentX := resultX + 24

		// Word.
		if medFont != nil {
			wordCol := ColorTextPrimary()
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
					WithAlpha(ColorTextSecondary(), 220),
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
				WithAlpha(ColorTextTertiary(), 150),
				(screenWidth-int32(hw))/2, resultY+40)
		}
		if font != nil {
			features := "Definitions • Synonyms • Antonyms • Pronunciation"
			fw, _, _ := font.SizeUTF8(features)
			renderText(renderer, config, font, features,
				WithAlpha(ColorTextTertiary(), 120),
				(screenWidth-int32(fw))/2, resultY+70)
		}
	}

	// Controls.
	if font != nil {
		controls := hintLine(FooterHint{Label: "Type to Search"}, FooterHint{"Enter", "Look Up"}, FooterHint{"Esc", "Back"})
		cw, _, _ := font.SizeUTF8(controls)
		renderText(renderer, config, font, controls,
			WithAlpha(ColorTextTertiary(), 120),
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
			dictStartLookup(dict.query)
		}
	case sdl.K_BACKSPACE:
		dict.query = trimLastRune(dict.query)
	case sdl.K_ESCAPE, sdl.K_b:
		if dict.query != "" {
			dict.query = ""
			dict.result = dictResult{}
			dictCancelLookup()
		} else {
			goBackScene(config)
			PlayBackSound()
		}
	}
}

func handleDictTextInput(e *sdl.TextInputEvent) {
	for _, r := range e.GetText() {
		if unicode.IsLetter(r) || r == ' ' || r == '-' {
			if len(dict.query) < 60 {
				dict.query += string(r)
			}
		}
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlDictionary(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if dict.cursor > 0 {
			dict.cursor--
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		if dict.cursor < len(dict.results)-1 {
			dict.cursor++
		}
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		if dict.cursor >= 0 && dict.cursor < len(dict.results) {
			word := dict.results[dict.cursor].Word
			dict.query = word
			dictStartLookup(word)
			PlayActivateSound()
		}
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(appConfig)
	default:
		return false
	}
	return true
}

// Scene registration.
type dictionaryScene struct{ sceneBase }

func (dictionaryScene) render(r *sdl.Renderer, c *Config) bool { renderDictionary(r, c); return true }

func (dictionaryScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleDictInput(e, c)
	return true
}

func (dictionaryScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlDictionary(e)
}

func init() { registerScene(dictionaryScene{sceneBase{name: "Dictionary"}}) }
