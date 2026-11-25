package main

// webpage_fonts.go — the faces the reader measures and draws a run in.
//
// The reader used to draw every run in the one regular UI face. Bold was the
// same glyphs drawn twice a pixel apart (and measured without the extra
// weight), code sat in proportional Inter so ASCII art and aligned columns fell
// apart, and icon fonts showed their private code points or their ligature
// words as text.
//
// A bold run now uses a real bold cut: the bundled Inter SemiBold while the UI
// draws in the bundled Inter, or the picked or legible face's own bold file
// when it ships one; any other face is emboldened by SDL_ttf. Italic runs use a
// slanted copy of the face they would use upright, bold italic included. Every
// face is opened at exactly the size getFontSized opens the regular one at
// (legibility floor and accessibility scale applied), so a run's style never
// changes its size.
//
// Monospace runs (<pre>, <code>, <kbd>, <samp>, <tt> and a monospace
// font-family) use the monospace face the terminal finds (termPtyMonoFontPath:
// JukaHub's fonts folder, Consolas or Cascadia Mono on Windows, DejaVu,
// Liberation or Noto Sans Mono on Linux). A machine without one keeps drawing
// them in the UI face.
//
// Serif runs (a serif font-family: Georgia, Times, Wikipedia's Linux
// Libertine headings, "Source Serif"...) use a serif face: a *Serif*.ttf in
// JukaHub's fonts folder, Georgia or Times New Roman on Windows, DejaVu,
// Liberation or Noto Serif on Linux, and otherwise — on every handheld — the
// Library theme's embedded serif (Lora, OFL). A word the serif cannot set
// stays in the UI face, and Legible reading font or Bold text keep everything
// in the UI face.
//
// Icon fonts: Private Use Area code points (Font Awesome's ::before content)
// are dropped before a word is measured — Inter maps parts of that area to its
// own alternates, so they used to draw as stray letters — and the ligature
// words of Material Icons/Symbols ("menu", "arrow_forward") become the nearest
// glyph Inter really has, or nothing.

import (
	htmlstd "html"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// webFaceRequest is one face computeLayout asks for: the reader's pixel size
// (before getFontSized's legibility floor and accessibility scale) and the
// run's weight, slant and family.
type webFaceRequest struct {
	size   int
	bold   bool
	italic bool
	mono   bool
	serif  bool
}

// webBoldFaces remembers every face opened as a bold cut or emboldened copy,
// so drawing does not add the old pixel of fake weight on top of real weight.
// Entries are never removed: faces are dropped by the font resets, not closed,
// and a layout cached before a reset may still draw with one.
var webBoldFaces = struct {
	sync.Mutex
	fonts map[*ttf.Font]bool
}{fonts: map[*ttf.Font]bool{}}

// webFailedFaces are reader faces that would not open (a missing bold cut, an
// unreadable monospace file), so a page does not read the card again for every
// size. The run falls back to the face it would otherwise use.
var webFailedFaces = map[string]bool{}

// webFontIsBold reports whether font carries real weight: a bold cut or an
// SDL_ttf-emboldened copy the reader opened.
func webFontIsBold(font *ttf.Font) bool {
	if font == nil {
		return false
	}
	webBoldFaces.Lock()
	bold := webBoldFaces.fonts[font]
	webBoldFaces.Unlock()
	return bold
}

// webFaceStyle is the SDL_ttf style flags for a run's weight and slant.
func webFaceStyle(bold, italic bool) int {
	style := ttf.STYLE_NORMAL
	if bold {
		style |= ttf.STYLE_BOLD
	}
	if italic {
		style |= ttf.STYLE_ITALIC
	}
	return style
}

// webReaderFace is the face a bold, italic, monospace or serif run is measured
// and drawn in, opened at the pixel size the regular face is (px is the
// reader's size before the legibility floor and accessibility scale, as fontAt
// passes it to getFontSized). nil keeps the run in the regular face. Render
// thread.
func webReaderFace(config *Config, role string, px int, bold, italic, mono, serif bool) *ttf.Font {
	size := a11yScaleFontSize(uiLegibleFontSize(px))
	if mono {
		if path := termPtyMonoFontPath(); path != "" {
			// Bold text (accessibility) thickens every face, the monospace one
			// included.
			if font := webOpenReaderFace(path, size, webFaceStyle(bold || a11y.boldText, italic), false); font != nil {
				return font
			}
		}
	} else if serif {
		if font := webSerifFace(size, bold, italic); font != nil {
			return font
		}
	}
	if !bold && !italic {
		return nil
	}
	regular, cut := webReaderFaceFiles(config, role)
	if bold && cut != "" {
		if font := webOpenReaderFace(cut, uiFontScaledSize(cut, size), webFaceStyle(false, italic), true); font != nil {
			return font
		}
	}
	// No bold cut: SDL_ttf emboldens the face itself. A face that will not
	// open at all falls back to the bundled Inter, as loadCachedFont does.
	for _, path := range []string{regular, homeUIFontFile} {
		if font := webOpenReaderFace(path, uiFontScaledSize(path, size), webFaceStyle(bold, italic), false); font != nil {
			return font
		}
	}
	return nil
}

// webReaderFaceFiles names the file the reader's regular text is drawn from
// (the resolution loadRoleFont makes) and that face's own bold cut, "" when it
// has none and bold has to be synthesised. With Bold text on, the regular text
// already uses the bold cut, so a bold run is that cut emboldened.
func webReaderFaceFiles(config *Config, role string) (regular, bold string) {
	rolePath := homeUIFontFile
	if config != nil {
		for k, p := range config.Variables.Fonts {
			if strings.EqualFold(k, role) {
				rolePath = homeUIFontPath(p)
				break
			}
		}
	}
	regular = homeUIFontForRole(homeUIFontPath(rolePath))
	switch {
	case a11y.boldText:
		return regular, ""
	case legibleFontFile() != "":
		return regular, currentLegibleFont().Bold
	case uiFontFile() != "":
		return regular, webUIFontBoldCut()
	}
	return regular, homeUIFontBoldFile
}

// webUIFontBoldCut is the bold file of the face picked in Settings → Display →
// Font, "" when it ships none. uiFontFile has resolved the choice just before.
func webUIFontBoldCut() string {
	uiFontState.Lock()
	defer uiFontState.Unlock()
	if uiFontState.choice == "" || !uiFontState.resolved {
		return ""
	}
	return uiFontState.bold
}

// webOpenReaderFace opens path at size with SDL_ttf style flags, once. The
// faces live in fontStyleCache under keys of their own, so the resets that
// follow a new UI or legible face (uiFontApply, legibleFontRedraw) drop them
// too; like every face there they are dropped, never closed. cut marks a file
// that is itself a bold cut.
func webOpenReaderFace(path string, size, style int, cut bool) *ttf.Font {
	if path == "" || size <= 0 {
		return nil
	}
	key := "web\n" + path + "\n" + strconv.Itoa(size) + "\n" + strconv.Itoa(style)
	if font, ok := fontStyleCache[key]; ok {
		return font
	}
	if webFailedFaces[key] {
		return nil
	}
	font, err := webOpenFaceFile(path, size)
	if err != nil {
		webFailedFaces[key] = true
		logWarn("browser", "Error loading the reader face %s at %d px: %v", path, size, err)
		return nil
	}
	if style != ttf.STYLE_NORMAL {
		font.SetStyle(style)
	}
	// sanitizeForFont reads the coverage of the file actually drawn: a
	// monospace face has box drawing and arrows Inter lacks.
	registerFontPath(font, path)
	webNoteFaceSize(font, size)
	fontStyleCache[key] = font
	if cut || style&ttf.STYLE_BOLD != 0 {
		webBoldFaces.Lock()
		webBoldFaces.fonts[font] = true
		webBoldFaces.Unlock()
	}
	return font
}

// webOpenFaceFile opens path at size from the bytes the UI font cache keeps
// for the file (home_fonts.go), so another size or style does not read the
// card again. Unlike openUIFontFile it does not record the face for the start
// frame cache, which names a face by file and size only and would mistake a
// styled copy for the plain one.
func webOpenFaceFile(path string, size int) (*ttf.Font, error) {
	source, err := homeUIFontSourceFor(path)
	if err != nil {
		return nil, err
	}
	rw, err := sdl.RWFromMem(source.data)
	if err != nil {
		return nil, err
	}
	font, err := ttf.OpenFontRW(rw, 1, size)
	runtime.KeepAlive(source)
	return font, err
}

// styledFont is the face a run with style is measured in, for the intrinsic
// size passes that ask before layoutBlocks does: a bold label measured in the
// regular face would wrap inside the box sized for it.
func (l *webLayouter) styledFont(style webStyle) *ttf.Font {
	if l.fontAtStyle != nil {
		return l.fontAtStyle(style)
	}
	if l.fontAt != nil {
		return l.fontAt(style.FontPx)
	}
	return nil
}

// --- font-family -------------------------------------------------------------

// What the reader makes of one font-family name.
const (
	webFamilyUnknown = iota
	webFamilyText
	webFamilyMono
	webFamilyIcon
	webFamilySerif
)

// webApplyFontFamily reads a font-family list (from the longhand or the font
// shorthand). The reader draws in its UI face, a monospace face or a serif
// face, so the list only decides which of the three a run uses and whether its
// words are icon names: the first family the reader recognises decides. A list
// of families it has never heard of ("Brand Sans Pro", "FontAwesome") leaves
// the inherited choice alone, so a site's own code font keeps <code>
// monospaced.
func webApplyFontFamily(st *webStyle, value string) {
	v := strings.TrimSpace(value)
	switch strings.ToLower(v) {
	case "", "inherit", "unset", "revert", "revert-layer":
		return
	case "initial":
		st.Mono, st.IconFont, st.Serif = false, false, false
		return
	}
	for _, family := range strings.Split(v, ",") {
		switch webFontFamilyKind(family) {
		case webFamilyMono:
			st.Mono, st.IconFont, st.Serif = true, false, false
			return
		case webFamilyIcon:
			st.Mono, st.IconFont, st.Serif = false, true, false
			return
		case webFamilySerif:
			st.Mono, st.IconFont, st.Serif = false, false, true
			return
		case webFamilyText:
			st.Mono, st.IconFont, st.Serif = false, false, false
			return
		}
	}
}

// webFontFamilyKind classifies one family name: the generic families, the
// monospace, serif and sans-serif faces pages name most, Material
// Icons/Symbols, and otherwise the words in the name ("Noto Sans Mono", "Brand
// Serif", "Roboto Slab").
func webFontFamilyKind(name string) int {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), `"'`))
	name = strings.Join(strings.Fields(name), " ")
	switch name {
	case "":
		return webFamilyUnknown
	case "monospace", "ui-monospace", "-moz-fixed", "sfmono-regular", "sf mono", "menlo", "monaco",
		"consolas", "courier new", "courier", "lucida console", "lucida sans typewriter",
		"andale mono", "inconsolata", "hack", "iosevka", "anonymous pro", "fixedsys", "terminal":
		return webFamilyMono
	case "serif", "ui-serif", "fangsong", "georgia", "georgia pro", "times", "times new roman",
		"cambria", "garamond", "eb garamond", "palatino", "palatino linotype", "book antiqua",
		"charter", "bitstream charter", "tinos", "merriweather", "linux libertine", "libertine",
		"linux libertine o", "lora", "playfair display", "crimson text", "crimson pro", "baskerville",
		"libre baskerville", "didot", "bodoni 72", "constantia", "iowan old style", "new york",
		"sitka text", "literata", "spectral", "cormorant", "cormorant garamond", "bitter", "arvo",
		"rockwell", "domine", "noticia text", "nyt-cheltenham", "nyt-imperial", "tiempos text",
		"tiempos headline", "guardian text egyptian web", "minion pro", "big caslon", "hoefler text":
		return webFamilySerif
	case "sans-serif", "system-ui", "ui-sans-serif", "ui-rounded",
		"cursive", "fantasy", "math", "-apple-system", "blinkmacsystemfont",
		"segoe ui", "segoe ui variable", "roboto", "helvetica", "helvetica neue", "arial",
		"inter", "inter var", "verdana", "tahoma",
		"trebuchet ms", "open sans", "lato", "ubuntu", "cantarell", "oxygen", "montserrat",
		"poppins", "raleway", "nunito", "lucida grande", "calibri",
		"optima", "avenir", "avenir next",
		"futura", "geneva", "arimo", "manrope", "rubik", "karla", "heebo",
		"barlow", "figtree", "outfit", "lexend", "quicksand", "oswald", "geist":
		return webFamilyText
	}
	if strings.HasPrefix(name, "material icons") || strings.HasPrefix(name, "material symbols") {
		return webFamilyIcon
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '-' || r == '_' })
	for _, word := range words {
		switch word {
		case "mono", "monospace", "monospaced", "code":
			return webFamilyMono
		}
	}
	for _, word := range words {
		switch word {
		case "sans", "grotesk", "grotesque":
			return webFamilyText
		}
	}
	for _, word := range words {
		switch word {
		case "serif", "slab":
			// "IBM Plex Serif", "Source Serif 4", "Roboto Slab" — and not
			// "Noto Sans", which the loop above already took.
			return webFamilySerif
		}
	}
	return webFamilyUnknown
}

// --- Serif ---------------------------------------------------------------------

// webSerifState remembers the serif file found on this machine. Render thread.
var webSerifState struct {
	probed bool
	path   string
}

// webSerifFacePath is a serif font file on this machine, "" when there is
// none: a *Serif*.ttf in JukaHub's fonts folder (not a sans-serif one), then
// Georgia or Times New Roman on Windows, DejaVu, Liberation or Noto Serif on
// Linux. A TrimUI handheld has none of these and uses the embedded Library
// serif instead (webSerifFace). Render thread.
func webSerifFacePath() string {
	s := &webSerifState
	if s.probed {
		return s.path
	}
	s.probed = true
	var cands []string
	if dir := uiFontDir(); dir != "" {
		for _, pat := range []string{"*Serif*.ttf", "*serif*.ttf"} {
			if m, err := filepath.Glob(filepath.Join(dir, pat)); err == nil {
				for _, p := range m {
					if !strings.Contains(strings.ToLower(filepath.Base(p)), "sans") {
						cands = append(cands, p)
					}
				}
			}
		}
	}
	if runtime.GOOS == "windows" {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		for _, f := range []string{"georgia.ttf", "times.ttf", "cambria.ttc"} {
			cands = append(cands, filepath.Join(win, "Fonts", f))
		}
	} else {
		cands = append(cands,
			"/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf",
			"/usr/share/fonts/dejavu/DejaVuSerif.ttf",
			"/usr/share/fonts/TTF/DejaVuSerif.ttf",
			"/usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf",
			"/usr/share/fonts/truetype/liberation2/LiberationSerif-Regular.ttf",
			"/usr/share/fonts/truetype/noto/NotoSerif-Regular.ttf",
			"/usr/share/fonts/noto/NotoSerif-Regular.ttf",
		)
	}
	for _, p := range cands {
		if regularFile(p) {
			s.path = p
			break
		}
	}
	return s.path
}

// webSerifFace is the serif face for a run at size (already scaled), bold and
// italic applied, or nil when the run keeps the UI face: Legible reading font
// or Bold text is on (legibility wins over the page's choice), or no serif
// opens. A machine's own serif file comes first; otherwise the Library theme's
// embedded serif (themelibrary.go: Lora, OFL), which every JukaHub carries.
// That cut is SemiBold, so a bold run is it emboldened once more. Render
// thread.
func webSerifFace(size int, bold, italic bool) *ttf.Font {
	if size <= 0 || a11y.boldText || legibleFontFile() != "" {
		return nil
	}
	style := webFaceStyle(bold, italic)
	if path := webSerifFacePath(); path != "" {
		if font := webOpenReaderFace(path, size, style, false); font != nil {
			return font
		}
	}
	// The plain embedded face is the Library theme's own (it also registers
	// the face's character map); a styled one is a copy of its own.
	plain := libThemeSerifFont(size)
	webNoteFaceSize(plain, size)
	if plain == nil || style == ttf.STYLE_NORMAL {
		return plain
	}
	key := "web\n" + libThemeSerifName + "\n" + strconv.Itoa(size) + "\n" + strconv.Itoa(style)
	if font, ok := fontStyleCache[key]; ok {
		return font
	}
	if webFailedFaces[key] {
		return plain
	}
	rw, err := sdl.RWFromMem(libThemeSerifTTF)
	if err != nil {
		webFailedFaces[key] = true
		return plain
	}
	font, err := ttf.OpenFontRW(rw, 1, size)
	if err != nil || font == nil {
		webFailedFaces[key] = true
		logWarn("browser", "Error loading the reader serif at %d px: %v", size, err)
		return plain
	}
	font.SetStyle(style)
	registerFontPath(font, libThemeSerifName)
	webNoteFaceSize(font, size)
	fontStyleCache[key] = font
	if bold {
		webBoldFaces.Lock()
		webBoldFaces.fonts[font] = true
		webBoldFaces.Unlock()
	}
	return font
}

// webFontCoversText reports whether font has a glyph for every letter of s
// (spaces aside). A serif run falls back to the UI face for a word the serif
// cannot set — a Greek word in a Lora paragraph — instead of drawing it as
// substitutes.
func webFontCoversText(font *ttf.Font, s string) bool {
	path, ok := fontPathFor(font)
	if !ok {
		return true
	}
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if !fontCoversRune(path, r) {
			return false
		}
	}
	return true
}

// webApplyIconFontClass marks an element whose class is a Material icon font
// (Google's stylesheet sets the family on these classes, and a page that
// failed to load it still means icon names by the words inside). An author
// font-family on the element is applied after this and wins.
func webApplyIconFontClass(st *webStyle, classes []string) {
	for _, class := range classes {
		switch strings.ToLower(class) {
		case "material-icons", "material-icons-outlined", "material-icons-round", "material-icons-sharp",
			"material-icons-two-tone", "material-symbols-outlined", "material-symbols-rounded",
			"material-symbols-sharp", "material-symbols", "google-material-icons":
			st.IconFont, st.Mono = true, false
			return
		}
	}
}

// --- Icon fonts --------------------------------------------------------------

// webIsPrivateUse reports whether r is a Private Use Area code point: where
// Font Awesome, Bootstrap Icons, Material Icons' code points and the like put
// their glyphs.
func webIsPrivateUse(r rune) bool {
	return (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0x10FFFD)
}

// webDropPrivateUse removes Private Use Area code points from a word. No face
// the reader has draws those icons, and Inter maps part of the area to its own
// alternates, so an icon drew as a stray letter (or an omission mark).
func webDropPrivateUse(text string) string {
	for i, r := range text {
		if !webIsPrivateUse(r) {
			continue
		}
		var b strings.Builder
		b.Grow(len(text))
		b.WriteString(text[:i])
		for _, r := range text[i:] {
			if !webIsPrivateUse(r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return text
}

// webIconFontText rewrites the text of a ligature icon font: each word that
// names an icon becomes its glyph and every other word is dropped. Whitespace
// at either end is kept as one space, so the icon still stands apart from the
// words around it. The result holds no markup, so unescaping it again is
// harmless.
func webIconFontText(raw string) string {
	text := htmlstd.UnescapeString(raw)
	var b strings.Builder
	for _, word := range strings.Fields(text) {
		glyph := webIconGlyph(strings.ToLower(word))
		if glyph == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(glyph)
	}
	leads := strings.TrimLeftFunc(text, unicode.IsSpace) != text
	trails := strings.TrimRightFunc(text, unicode.IsSpace) != text
	out := b.String()
	if out == "" {
		if leads || trails {
			return " "
		}
		return ""
	}
	if leads {
		out = " " + out
	}
	if trails {
		out += " "
	}
	return out
}

// webIconGlyph is the glyph the reader draws for a Material Icons/Symbols
// ligature name, "" when the bundled Inter has nothing close. Every glyph
// below is in Inter's character map; a missing search or settings icon reads
// better than the word "search" in a toolbar.
func webIconGlyph(name string) string {
	switch name {
	case "menu", "menu_open", "dehaze":
		return "Ξ"
	case "close", "clear":
		return "×"
	case "cancel", "highlight_off":
		return "⊗"
	case "check", "done", "check_circle", "check_circle_outline", "task_alt":
		return "✓"
	case "done_all":
		return "✓✓"
	case "add":
		return "+"
	case "add_circle", "add_circle_outline", "control_point":
		return "⊕"
	case "remove", "horizontal_rule":
		return "−"
	case "remove_circle", "remove_circle_outline", "do_not_disturb_on":
		return "⊖"
	case "block", "do_not_disturb", "not_interested":
		return "⊘"
	case "arrow_forward", "east", "trending_flat", "arrow_right_alt":
		return "→"
	case "arrow_back", "west", "keyboard_backspace":
		return "←"
	case "arrow_upward", "north":
		return "↑"
	case "arrow_downward", "south":
		return "↓"
	case "north_east", "open_in_new", "launch", "call_made", "trending_up", "arrow_outward":
		return "↗"
	case "north_west":
		return "↖"
	case "south_east", "trending_down":
		return "↘"
	case "south_west", "call_received":
		return "↙"
	case "chevron_right", "navigate_next", "keyboard_arrow_right", "arrow_forward_ios":
		return "›"
	case "chevron_left", "navigate_before", "keyboard_arrow_left", "arrow_back_ios", "arrow_back_ios_new":
		return "‹"
	case "keyboard_double_arrow_right", "double_arrow":
		return "»"
	case "keyboard_double_arrow_left":
		return "«"
	case "expand_more", "keyboard_arrow_down", "expand_circle_down":
		return "⌄"
	case "expand_less", "keyboard_arrow_up":
		return "⌃"
	case "arrow_drop_down", "arrow_drop_down_circle":
		return "▼"
	case "arrow_drop_up":
		return "▲"
	case "arrow_right", "play_arrow", "play_circle", "play_circle_filled", "play_circle_outline":
		return "▶"
	case "arrow_left":
		return "◀"
	case "unfold_more", "swap_vert", "height", "import_export":
		return "↕"
	case "swap_horiz", "compare_arrows", "sync_alt":
		return "↔"
	case "refresh", "sync", "autorenew", "cached", "loop", "redo", "rotate_right":
		return "↻"
	case "undo", "replay", "history", "restore", "rotate_left":
		return "↺"
	case "reply":
		return "↩"
	case "forward", "shortcut":
		return "↪"
	case "subdirectory_arrow_right":
		return "↳"
	case "keyboard_return", "subdirectory_arrow_left":
		return "↵"
	case "download", "file_download", "get_app", "save_alt":
		return "⤓"
	case "upload", "file_upload", "publish":
		return "⬆"
	case "more_horiz", "more_vert":
		return "…"
	case "star", "grade", "star_rate":
		return "★"
	case "star_border", "star_outline":
		return "☆"
	case "favorite":
		return "♥"
	case "favorite_border":
		return "♡"
	case "warning", "warning_amber", "report_problem":
		return "⚠"
	case "circle", "fiber_manual_record", "lens", "radio_button_checked":
		return "●"
	case "radio_button_unchecked", "panorama_fish_eye":
		return "○"
	case "stop", "square":
		return "■"
	case "check_box_outline_blank", "crop_square":
		return "□"
	case "change_history":
		return "△"
	case "diamond":
		return "◆"
	case "light_mode", "wb_sunny", "sunny":
		return "☀"
	case "all_inclusive":
		return "∞"
	case "functions":
		return "∑"
	case "copyright":
		return "©"
	case "percent":
		return "%"
	case "tag", "numbers":
		return "#"
	case "alternate_email":
		return "@"
	case "euro", "euro_symbol":
		return "€"
	case "attach_money":
		return "$"
	case "currency_pound":
		return "£"
	case "currency_yen":
		return "¥"
	case "drag_handle":
		return "="
	case "keyboard_capslock":
		return "⇪"
	case "backspace":
		return "⌫"
	case "keyboard_tab":
		return "⇥"
	case "keyboard_command_key":
		return "⌘"
	case "keyboard_option_key":
		return "⌥"
	case "eject":
		return "⏏"
	}
	return ""
}
