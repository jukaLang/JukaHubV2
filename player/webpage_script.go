package main

// webpage_script.go — words in a script the reader's face cannot draw.
//
// The reader draws in Inter, which has Latin, Greek and Cyrillic. Wikipedia's
// front page names its biggest editions in their own scripts — 日本語, 中文,
// فارسی — and the reader drew each as "…" (fonts.go's sanitizeForFont), as it
// did every Chinese, Japanese, Korean, Arabic, Hebrew or Indic word on a page.
//
// A word the run's face cannot draw is now set in a face that can
// (webScriptWord): the Noto CJK face a Japanese, Korean or Chinese interface
// downloads (uilangface.go), a Noto font in JukaHub's fonts folder, or one the
// machine has — Yu Gothic, Microsoft YaHei, Malgun Gothic, Segoe UI, Nirmala
// UI and the rest on Windows, Noto, WenQuanYi, Droid Sans Fallback or DejaVu on
// Linux — opened at the size and style of the run's own face. A TrimUI with
// none of these keeps the "…".
//
// SDL_ttf draws code points left to right and does no shaping. So a word of a
// right-to-left script is turned for it (webVisualWord): its letters reversed,
// combining marks kept on their letter, and Arabic letters first given the
// joined forms their neighbours call for (initial, medial, final, the lam-alef
// ligature) from Unicode's presentation forms, which the common Arabic faces
// carry. A phrase of several such words keeps its words in reading order left
// to right; the words themselves read right.

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unicode"

	"github.com/veandco/go-sdl2/ttf"
)

// --- Faces -------------------------------------------------------------------

// webFaceSizes remembers the pixel size each reader face was opened at, so a
// fallback face for a word it cannot draw opens at the same size.
var webFaceSizes = struct {
	sync.Mutex
	m map[*ttf.Font]int
}{m: map[*ttf.Font]int{}}

// webNoteFaceSize records that f was opened at size pixels.
func webNoteFaceSize(f *ttf.Font, size int) {
	if f == nil || size <= 0 {
		return
	}
	webFaceSizes.Lock()
	webFaceSizes.m[f] = size
	webFaceSizes.Unlock()
}

// webFaceSize is the pixel size f was opened at; a face the reader did not
// open itself is estimated from its height (Inter's is 1.21 em).
func webFaceSize(f *ttf.Font) int {
	webFaceSizes.Lock()
	size, ok := webFaceSizes.m[f]
	webFaceSizes.Unlock()
	if ok {
		return size
	}
	return max(int(math.Round(float64(f.Height())/1.21)), 8)
}

// webScriptFaceState is the fallback faces found on this machine, best first,
// looked for once.
var webScriptFaceState struct {
	sync.Mutex
	probed bool
	paths  []string
}

// webScriptFacePaths lists the faces a word may be set in when its run's face
// lacks its letters: the downloaded Noto CJK faces, Noto and collection files
// in JukaHub's fonts folder, then the machine's own. Japanese faces come
// before Chinese ones, so kana and the Han characters Japanese shares are set
// in a Japanese face; a word only a Chinese face has goes on down the list.
func webScriptFacePaths() []string {
	s := &webScriptFaceState
	s.Lock()
	defer s.Unlock()
	if s.probed {
		return s.paths
	}
	s.probed = true
	var cands []string
	for _, id := range []string{"notosansjp", "notosanssc", "notosanskr"} {
		if e, ok := uiFontEntryFor(id); ok {
			if p := uiScriptFacePath(e); p != "" {
				cands = append(cands, p)
			}
		}
	}
	if dir := uiFontDir(); dir != "" {
		for _, pat := range []string{"Noto*.ttf", "Noto*.otf", "noto*.ttf", "noto*.otf", "*.ttc"} {
			if m, err := filepath.Glob(filepath.Join(dir, pat)); err == nil {
				cands = append(cands, m...)
			}
		}
	}
	if runtime.GOOS == "windows" {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		for _, f := range []string{
			"YuGothR.ttc", "YuGothM.ttc", "meiryo.ttc", "msgothic.ttc", "msyh.ttc", "msjh.ttc", "simsun.ttc",
			"malgun.ttf", "segoeui.ttf", "arial.ttf", "tahoma.ttf", "Nirmala.ttf", "leelawui.ttf",
			"ebrima.ttf", "mmrtext.ttf", "sylfaen.ttf", "himalaya.ttf", "gadugi.ttf", "msyi.ttf",
			"javatext.ttf", "mvboli.ttf", "seguisym.ttf", "seguihis.ttf",
		} {
			cands = append(cands, filepath.Join(win, "Fonts", f))
		}
	} else {
		cands = append(cands,
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
			"/usr/share/fonts/wenquanyi/wqy-microhei/wqy-microhei.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
			"/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf",
			"/usr/share/fonts/truetype/droid/DroidSansFallback.ttf",
		)
		for _, pat := range []string{
			"/usr/share/fonts/truetype/noto/NotoSans*-Regular.ttf",
			"/usr/share/fonts/noto/NotoSans*-Regular.ttf",
			"/usr/share/fonts/opentype/noto/NotoSans*-Regular.otf",
		} {
			if m, err := filepath.Glob(pat); err == nil {
				cands = append(cands, m...)
			}
		}
		cands = append(cands,
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/TTF/DejaVuSans.ttf",
		)
	}
	seen := make(map[string]bool, len(cands))
	for _, p := range cands {
		if !seen[p] && regularFile(p) {
			seen[p] = true
			s.paths = append(s.paths, p)
		}
	}
	return s.paths
}

// webFaceHasAll reports whether the face at path has a glyph for every rune
// of need. Unlike fontCoversRune, a face whose character map cannot be read
// has none.
func webFaceHasAll(path string, need []rune) bool {
	ranges, ok := coverageFor(path)
	if !ok {
		return false
	}
	for _, r := range need {
		i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= r })
		if i >= len(ranges) || ranges[i].lo > r {
			return false
		}
	}
	return true
}

// webScriptWord is how the line builder sets word in main: the word to draw
// (webVisualWord) and the face to draw it in — main when it has every letter
// or no fallback face does, else the first face of webScriptFacePaths that has
// them all, at main's size and style. A shaped Arabic word whose presentation
// forms no face carries is tried again in its plain letters.
func webScriptWord(main *ttf.Font, word string) (*ttf.Font, string) {
	if main == nil || word == "" {
		return main, word
	}
	path, ok := fontPathFor(main)
	if !ok {
		return main, webVisualWord(word, true)
	}
	for _, shape := range []bool{true, false} {
		visual := webVisualWord(word, shape)
		need := webMissingRunes(path, visual)
		if len(need) == 0 {
			return main, visual
		}
		for _, p := range webScriptFacePaths() {
			if !webFaceHasAll(p, need) {
				continue
			}
			style := main.GetStyle()
			if webFontIsBold(main) {
				style |= ttf.STYLE_BOLD
			}
			if f := webOpenReaderFace(p, webFaceSize(main), style, false); f != nil {
				return f, visual
			}
		}
		if visual == word {
			// Nothing to shape: the plain letters are the same word.
			break
		}
	}
	return main, webVisualWord(word, true)
}

// webMissingRunes is the letters of word the face at path has no glyph for,
// each once, at most 32: spaces and invisible format characters aside.
func webMissingRunes(path, word string) []rune {
	var need []rune
	for _, r := range word {
		if r == ' ' || unicode.Is(unicode.Cf, r) || fontCoversRune(path, r) {
			continue
		}
		known := false
		for _, n := range need {
			if n == r {
				known = true
				break
			}
		}
		if !known {
			if need = append(need, r); len(need) >= 32 {
				break
			}
		}
	}
	return need
}

// --- Right to left -------------------------------------------------------------

// webRTLLetter reports a letter of a right-to-left script: Hebrew, Arabic,
// Syriac, Thaana, N'Ko and their presentation forms. Digits (Arabic-Indic
// ones too) are not letters, so a number keeps its order.
func webRTLLetter(r rune) bool {
	if !unicode.IsLetter(r) {
		return false
	}
	return (r >= 0x0590 && r <= 0x08FF) || (r >= 0xFB1D && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF) ||
		(r >= 0x10800 && r <= 0x10FFF) || (r >= 0x1E800 && r <= 0x1EFFF)
}

// webVisualWord is word as SDL_ttf has to be given it to read right. A word
// whose letters belong to a right-to-left script — punctuation round it aside
// — has those letters reversed, each with its combining marks, and (shape) its
// Arabic letters joined first (webShapeArabic). Invisible joiners are dropped:
// the joined forms already say what they did. Any other word, and one that
// mixes a right-to-left script with digits or other letters inside, is
// returned as it is.
func webVisualWord(word string, shape bool) string {
	rtl := false
	for _, r := range word {
		if webRTLLetter(r) {
			rtl = true
			break
		}
	}
	if !rtl {
		return word
	}
	rs := []rune(word)
	first, last := -1, -1
	for i, r := range rs {
		if webRTLLetter(r) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	for last+1 < len(rs) && (unicode.Is(unicode.Mn, rs[last+1]) || unicode.Is(unicode.Cf, rs[last+1])) {
		last++
	}
	core := rs[first : last+1]
	for _, r := range core {
		if !webRTLLetter(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Cf, r) && r != 0x0640 {
			return word
		}
	}
	if shape {
		core = webShapeArabic(core)
	}
	// Reverse letter by letter, each letter's marks after it.
	type cluster struct{ from, to int }
	var clusters []cluster
	for i := 0; i < len(core); i++ {
		r := core[i]
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.Is(unicode.Mn, r) && len(clusters) > 0 && clusters[len(clusters)-1].to == i {
			clusters[len(clusters)-1].to = i + 1
			continue
		}
		clusters = append(clusters, cluster{i, i + 1})
	}
	out := make([]rune, 0, len(rs))
	out = append(out, rs[:first]...)
	for k := len(clusters) - 1; k >= 0; k-- {
		out = append(out, core[clusters[k].from:clusters[k].to]...)
	}
	out = append(out, rs[last+1:]...)
	return string(out)
}

// webArabicJoins reports which neighbours an Arabic letter joins in reading
// order: the next one (it has an initial form; tatweel and the zero-width
// joiner join both ways) and the one before (it has a final form).
func webArabicJoins(r rune) (next, prev bool) {
	if r == 0x0640 || r == 0x200D {
		return true, true
	}
	f, ok := webArabicForms[r]
	if !ok {
		return false, false
	}
	return f[2] != 0, f[1] != 0
}

// webNextLetter is the index of the first rune from i on that is not a
// combining mark, or -1.
func webNextLetter(rs []rune, i int) int {
	for ; i < len(rs); i++ {
		if !unicode.Is(unicode.Mn, rs[i]) {
			return i
		}
	}
	return -1
}

// webShapeArabic gives each Arabic letter of rs (in reading order) the
// presentation form its neighbours call for — isolated, final, initial or
// medial — and a lam before an alef the lam-alef ligature, as an Arabic
// shaping engine would. Combining marks do not break a join and stay after
// their letter; any other character does.
func webShapeArabic(rs []rune) []rune {
	out := make([]rune, 0, len(rs))
	joinedPrev := false // the letter before joins this one
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if unicode.Is(unicode.Mn, r) {
			out = append(out, r)
			continue
		}
		if r == 0x0644 {
			if j := webNextLetter(rs, i+1); j >= 0 {
				if lig, ok := webLamAlef[rs[j]]; ok {
					form := lig[0]
					if joinedPrev {
						form = lig[1]
					}
					out = append(out, form)
					out = append(out, rs[i+1:j]...)
					i, joinedPrev = j, false
					continue
				}
			}
		}
		f, ok := webArabicForms[r]
		if !ok {
			out = append(out, r)
			next, _ := webArabicJoins(r)
			joinedPrev = next && webJoinsNext(rs, i)
			continue
		}
		joinsPrev := joinedPrev && f[1] != 0
		joinsNext := f[2] != 0 && webJoinsNext(rs, i)
		form := f[0]
		switch {
		case joinsPrev && joinsNext && f[3] != 0:
			form = f[3]
		case joinsPrev && !joinsNext && f[1] != 0:
			form = f[1]
		case !joinsPrev && joinsNext && f[2] != 0:
			form = f[2]
		case joinsPrev && f[1] != 0:
			form = f[1]
		}
		if form == 0 {
			form = r
		}
		out = append(out, form)
		joinedPrev = joinsNext
	}
	return out
}

// webJoinsNext reports whether the letter after rs[i] (marks skipped) joins
// back to it.
func webJoinsNext(rs []rune, i int) bool {
	j := webNextLetter(rs, i+1)
	if j < 0 {
		return false
	}
	_, prev := webArabicJoins(rs[j])
	return prev
}

// webArabicForms is each Arabic letter's presentation forms, from Unicode's
// Arabic Presentation Forms-A and -B (the <isolated>, <final>, <initial> and
// <medial> decompositions, generated from the Unicode 13 data): isolated,
// final, initial, medial; 0 where the letter has none.
var webArabicForms = map[rune][4]rune{
	0x0621: {0xFE80, 0, 0, 0},
	0x0622: {0xFE81, 0xFE82, 0, 0},
	0x0623: {0xFE83, 0xFE84, 0, 0},
	0x0624: {0xFE85, 0xFE86, 0, 0},
	0x0625: {0xFE87, 0xFE88, 0, 0},
	0x0626: {0xFE89, 0xFE8A, 0xFE8B, 0xFE8C},
	0x0627: {0xFE8D, 0xFE8E, 0, 0},
	0x0628: {0xFE8F, 0xFE90, 0xFE91, 0xFE92},
	0x0629: {0xFE93, 0xFE94, 0, 0},
	0x062A: {0xFE95, 0xFE96, 0xFE97, 0xFE98},
	0x062B: {0xFE99, 0xFE9A, 0xFE9B, 0xFE9C},
	0x062C: {0xFE9D, 0xFE9E, 0xFE9F, 0xFEA0},
	0x062D: {0xFEA1, 0xFEA2, 0xFEA3, 0xFEA4},
	0x062E: {0xFEA5, 0xFEA6, 0xFEA7, 0xFEA8},
	0x062F: {0xFEA9, 0xFEAA, 0, 0},
	0x0630: {0xFEAB, 0xFEAC, 0, 0},
	0x0631: {0xFEAD, 0xFEAE, 0, 0},
	0x0632: {0xFEAF, 0xFEB0, 0, 0},
	0x0633: {0xFEB1, 0xFEB2, 0xFEB3, 0xFEB4},
	0x0634: {0xFEB5, 0xFEB6, 0xFEB7, 0xFEB8},
	0x0635: {0xFEB9, 0xFEBA, 0xFEBB, 0xFEBC},
	0x0636: {0xFEBD, 0xFEBE, 0xFEBF, 0xFEC0},
	0x0637: {0xFEC1, 0xFEC2, 0xFEC3, 0xFEC4},
	0x0638: {0xFEC5, 0xFEC6, 0xFEC7, 0xFEC8},
	0x0639: {0xFEC9, 0xFECA, 0xFECB, 0xFECC},
	0x063A: {0xFECD, 0xFECE, 0xFECF, 0xFED0},
	0x0641: {0xFED1, 0xFED2, 0xFED3, 0xFED4},
	0x0642: {0xFED5, 0xFED6, 0xFED7, 0xFED8},
	0x0643: {0xFED9, 0xFEDA, 0xFEDB, 0xFEDC},
	0x0644: {0xFEDD, 0xFEDE, 0xFEDF, 0xFEE0},
	0x0645: {0xFEE1, 0xFEE2, 0xFEE3, 0xFEE4},
	0x0646: {0xFEE5, 0xFEE6, 0xFEE7, 0xFEE8},
	0x0647: {0xFEE9, 0xFEEA, 0xFEEB, 0xFEEC},
	0x0648: {0xFEED, 0xFEEE, 0, 0},
	0x0649: {0xFEEF, 0xFEF0, 0xFBE8, 0xFBE9},
	0x064A: {0xFEF1, 0xFEF2, 0xFEF3, 0xFEF4},
	0x0671: {0xFB50, 0xFB51, 0, 0},
	0x0677: {0xFBDD, 0, 0, 0},
	0x0679: {0xFB66, 0xFB67, 0xFB68, 0xFB69},
	0x067A: {0xFB5E, 0xFB5F, 0xFB60, 0xFB61},
	0x067B: {0xFB52, 0xFB53, 0xFB54, 0xFB55},
	0x067E: {0xFB56, 0xFB57, 0xFB58, 0xFB59},
	0x067F: {0xFB62, 0xFB63, 0xFB64, 0xFB65},
	0x0680: {0xFB5A, 0xFB5B, 0xFB5C, 0xFB5D},
	0x0683: {0xFB76, 0xFB77, 0xFB78, 0xFB79},
	0x0684: {0xFB72, 0xFB73, 0xFB74, 0xFB75},
	0x0686: {0xFB7A, 0xFB7B, 0xFB7C, 0xFB7D},
	0x0687: {0xFB7E, 0xFB7F, 0xFB80, 0xFB81},
	0x0688: {0xFB88, 0xFB89, 0, 0},
	0x068C: {0xFB84, 0xFB85, 0, 0},
	0x068D: {0xFB82, 0xFB83, 0, 0},
	0x068E: {0xFB86, 0xFB87, 0, 0},
	0x0691: {0xFB8C, 0xFB8D, 0, 0},
	0x0698: {0xFB8A, 0xFB8B, 0, 0},
	0x06A4: {0xFB6A, 0xFB6B, 0xFB6C, 0xFB6D},
	0x06A6: {0xFB6E, 0xFB6F, 0xFB70, 0xFB71},
	0x06A9: {0xFB8E, 0xFB8F, 0xFB90, 0xFB91},
	0x06AD: {0xFBD3, 0xFBD4, 0xFBD5, 0xFBD6},
	0x06AF: {0xFB92, 0xFB93, 0xFB94, 0xFB95},
	0x06B1: {0xFB9A, 0xFB9B, 0xFB9C, 0xFB9D},
	0x06B3: {0xFB96, 0xFB97, 0xFB98, 0xFB99},
	0x06BA: {0xFB9E, 0xFB9F, 0, 0},
	0x06BB: {0xFBA0, 0xFBA1, 0xFBA2, 0xFBA3},
	0x06BE: {0xFBAA, 0xFBAB, 0xFBAC, 0xFBAD},
	0x06C0: {0xFBA4, 0xFBA5, 0, 0},
	0x06C1: {0xFBA6, 0xFBA7, 0xFBA8, 0xFBA9},
	0x06C5: {0xFBE0, 0xFBE1, 0, 0},
	0x06C6: {0xFBD9, 0xFBDA, 0, 0},
	0x06C7: {0xFBD7, 0xFBD8, 0, 0},
	0x06C8: {0xFBDB, 0xFBDC, 0, 0},
	0x06C9: {0xFBE2, 0xFBE3, 0, 0},
	0x06CB: {0xFBDE, 0xFBDF, 0, 0},
	0x06CC: {0xFBFC, 0xFBFD, 0xFBFE, 0xFBFF},
	0x06D0: {0xFBE4, 0xFBE5, 0xFBE6, 0xFBE7},
	0x06D2: {0xFBAE, 0xFBAF, 0, 0},
	0x06D3: {0xFBB0, 0xFBB1, 0, 0},
}

// webLamAlef is the lam-alef ligature for each alef a lam joins: isolated,
// final.
var webLamAlef = map[rune][2]rune{
	0x0622: {0xFEF5, 0xFEF6},
	0x0623: {0xFEF7, 0xFEF8},
	0x0625: {0xFEF9, 0xFEFA},
	0x0627: {0xFEFB, 0xFEFC},
}
