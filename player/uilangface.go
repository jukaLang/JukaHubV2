package main

// uilangface.go — Japanese, Korean and Chinese need a face that has their
// characters.
//
// JukaHub draws in the bundled Inter, which has Latin, Greek and Cyrillic and
// no CJK: a Japanese label in Inter would be a row of "…" (fonts.go). So each
// of the three languages comes with a face it is drawn in — the subset Noto
// Sans of that language from notofonts/noto-cjk (Japanese 4.5 MB, Korean
// 4.6 MB, Simplified Chinese 8.4 MB; Latin and Cyrillic included), fetched by
// Settings → Display → Font's downloader into the fonts folder, never bundled
// in the binary.
//
// How it behaves:
//   - Picking 日本語, 한국어 or 简体中文 (Settings → General → Language, the
//     first-run tour, the iPod layout's language row) with the face already on
//     the card switches at once.
//   - Without it, the choice is kept, the face downloads (online), and the
//     interface switches when it lands; offline, it waits and looks again
//     every 30 seconds. Until then the interface stays in the language it was
//     in (English at first start), never in boxes and "…".
//   - The face draws only while the language is one of the three: legible
//     reading font and Settings → Display → Font still win when the face they
//     name can draw the language (uifont.go's letter check).
//   - The language rows name these three in English until their face is
//     here ("Japanese"), in their own script after (uiLanguageLabel).

import (
	"strings"
	"testing"
	"time"
)

// uiScriptFaces maps a language Inter cannot draw to the catalogue face
// (uifont.go) that draws it.
var uiScriptFaces = map[string]string{
	"ja":    "notosansjp",
	"ko":    "notosanskr",
	"zh-CN": "notosanssc",
}

// uiLanguageScriptFace is the catalogue face code needs, when it needs one.
func uiLanguageScriptFace(code string) (uiFontEntry, bool) {
	id, ok := uiScriptFaces[code]
	if !ok {
		return uiFontEntry{}, false
	}
	return uiFontEntryFor(id)
}

// uiScriptFacePath is the regular cut of e on the card, or "".
func uiScriptFacePath(e uiFontEntry) string {
	if installed, _ := uiFontInstalled(e); !installed {
		return ""
	}
	regular, _, _ := uiFontChoiceFiles(e.ID)
	return regular
}

// uiLanguageFaceFile is the face homeUIFontForRole draws the interface in for
// a language that needs one, or "" (Inter, or the faces picked before it).
func uiLanguageFaceFile() string {
	e, ok := uiLanguageScriptFace(uiLanguageCode())
	if !ok {
		return ""
	}
	return uiScriptFacePath(e)
}

// uiLanguageDrawable reports whether code can be drawn now: it needs no face,
// or the face it needs is on the card.
func uiLanguageDrawable(code string) bool {
	e, ok := uiLanguageScriptFace(code)
	if !ok {
		return true
	}
	return uiScriptFacePath(e) != ""
}

// uiSetLanguageDrawable puts code in force when it can be drawn, and English
// otherwise; it reports whether code itself is in force.
func uiSetLanguageDrawable(code string) bool {
	code = uiNormalizeLanguage(code)
	before := uiLanguageCode()
	ok := uiLanguageDrawable(code)
	if ok {
		uiSetLanguage(code)
	} else {
		uiSetLanguage(uiLanguageDefault)
	}
	uiLanguageFaceSwitched(before)
	return ok
}

// uiLanguageFaceSwitched reopens the fonts on the main loop when the language
// in force moved onto or off a language with its own face: the fonts opened so
// far are the other face's. Any goroutine.
func uiLanguageFaceSwitched(before string) {
	after := uiLanguageCode()
	if after == before {
		return
	}
	_, oldFace := uiScriptFaces[before]
	_, newFace := uiScriptFaces[after]
	if oldFace || newFace {
		tbxPost(func() { uiLanguageFontsChanged(appConfig) })
	}
}

// uiLangFace is the waiting language's state (main thread).
var uiLangFace struct {
	cfg     *Config
	want    string // the stored language whose face is missing
	ticking bool
	tries   int
}

// uiLanguageFaceKeep looks for the face of the stored language: when it is
// missing, the download starts (online) or a look-again ticker waits for the
// network. explicit says so in a toast. Main thread.
func uiLanguageFaceKeep(c *Config, code string, explicit bool) {
	if c != nil {
		uiLangFace.cfg = c
	}
	e, ok := uiLanguageScriptFace(code)
	if !ok || uiScriptFacePath(e) != "" || testing.Testing() {
		uiLangFace.want = ""
		return
	}
	uiLangFace.want = code
	if busy, _ := uiFontDownloadStatus(); busy != "" {
		uiLanguageFaceArm()
		return
	}
	if !ttsAutoOnline() {
		if explicit {
			ShowToast(uiLanguageEnglishName(code)+" needs the "+e.Name+" font ("+uiFontSizeLabel(e.KB)+"): it downloads when JukaHub is online", ToastKindInfo)
		}
		uiLanguageFaceArm()
		return
	}
	if explicit {
		ShowToast(uiLanguageEnglishName(code)+" needs the "+e.Name+" font ("+uiFontSizeLabel(e.KB)+"): downloading it now", ToastKindInfo)
	}
	logInfo("font", "language %s: downloading %s", code, e.Name)
	if !uiFontStartDownload(e, func(err error) { uiLanguageFaceLanded(code, e, err) }) {
		uiLanguageFaceArm()
	}
}

// uiLanguageFaceLanded switches to the language once its face is here.
func uiLanguageFaceLanded(code string, e uiFontEntry, err error) {
	if err != nil {
		uiLangFace.tries++
		logWarn("font", "language %s: %s did not download (try %d): %v", code, e.Name, uiLangFace.tries, err)
		ShowToast("Could not download "+e.Name+": "+uiFontProblem(err), ToastKindWarning)
		if uiLangFace.tries < 3 {
			uiLanguageFaceArm()
		}
		return
	}
	uiLangFace.tries = 0
	if uiLangFace.want != code {
		return
	}
	uiLangFace.want = ""
	c := uiLangFace.cfg
	if c == nil {
		c = appConfig
	}
	// Still the stored language: it goes into force, drawn in its face.
	if v, ok := customString(c.Variables.Custom, uiLanguageKey); ok && uiNormalizeLanguage(v) == code {
		uiSetLanguage(code)
		uiLanguageFontsChanged(c)
		ShowToast(uiT("Language")+": "+uiLanguageNative(code), ToastKindSuccess)
	}
}

// uiLanguageFontsChanged drops every font-dependent cache, so the next frame
// draws in the face the language resolves to now. Main thread.
func uiLanguageFontsChanged(c *Config) {
	legibleFontRedraw()
	if c != nil && setIsHubScene(setHubSceneName(c)) {
		setHubRefresh(c)
	}
}

// uiLanguageFaceArm looks again in 30 seconds while a face is wanted.
func uiLanguageFaceArm() {
	if uiLangFace.ticking || testing.Testing() {
		return
	}
	uiLangFace.ticking = true
	go func() {
		time.Sleep(30 * time.Second)
		tbxPost(func() {
			uiLangFace.ticking = false
			if code := uiLangFace.want; code != "" {
				uiLanguageFaceKeep(nil, code, false)
			}
		})
	}()
}

// uiLanguageEnglishName is a language's name in English.
func uiLanguageEnglishName(code string) string {
	for _, l := range uiLanguages {
		if l.Code == code {
			return l.English
		}
	}
	return code
}

// uiLanguageLabel is how a language is named in a list: in itself when the
// face that draws the list can draw that name, else in English.
func uiLanguageLabel(l uiLanguage) string {
	if _, needs := uiScriptFaces[l.Code]; !needs {
		return l.Native
	}
	face := homeUIFontForRole(homeUIFontFile)
	for _, r := range l.Native {
		if r > 0x7f && !fontCoversRune(face, r) {
			return l.English
		}
	}
	return l.Native + " · " + l.English
}

// uiLanguageWordsDrawable keeps the words the interface's face can draw: the
// first-run tour's "Language · Idioma · …" line.
func uiLanguageWordsDrawable(words []string) []string {
	face := homeUIFontForRole(homeUIFontFile)
	out := words[:0]
	for _, w := range words {
		ok := true
		for _, r := range w {
			if r > 0x7f && !fontCoversRune(face, r) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, w)
		}
	}
	return out
}

// uiLanguageLocaleAlias maps the Chinese locale names onto the one Chinese
// offered (Simplified), and nothing else.
func uiLanguageLocaleAlias(s string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-")) {
	case "zh", "zh-cn", "zh-sg", "zh-hans", "zh-hans-cn", "chinese", "简体中文", "中文":
		return "zh-CN"
	case "japanese", "日本語":
		return "ja"
	case "korean", "한국어":
		return "ko"
	}
	return ""
}
