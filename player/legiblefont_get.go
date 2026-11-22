package main

// legiblefont_get.go — Legible reading font downloads its readable face.
//
// Turning Settings → Accessibility → Legible reading font on is the opt-in:
// when no readable face for the interface language is on the machine, the
// setting fetches one into the fonts folder with Settings → Display → Font's
// own downloader (uifont.go: the same mirrors, size cap, font check and
// progress toasts) and draws in it as soon as it lands. Atkinson Hyperlegible
// is the face for Latin-script languages; Russian needs Cyrillic, which it
// lacks, so a Russian interface gets SIL's Andika. A face picked in the
// Readable font row's full list (a11y_settings.go) is fetched the same way.
//
// When it looks:
//   - on the switch and on a Readable font pick, at once, with toasts (only
//     the switch and a list pick of a face to download count as asking again
//     after a removal or failed tries; a Left/Right step does not);
//   - at start with the setting on, once, 20 seconds in, off the cold-start
//     path (legibleFontStartup);
//   - when the interface language changes under the setting
//     (currentLegibleFont notices; legibleFontLanguageChanged);
//   - while it waits — offline, or another font downloading — every 30
//     seconds on a sleeping ticker that ends as soon as there is nothing left
//     to wait for (legibleFontArm). A failed download is tried three times a
//     run per face, then the next face in the order is tried.
//
// Removing the face by hand (Display → Remove a font) stops the fetching for
// the run, so the removal is not undone a minute later; turning the setting
// on again or picking a face to download from the Readable font list asks
// again. Downloads never start under go test.
//
// All of the state below is the main thread's: the ticker goroutine only
// sleeps and hands each look to the main loop (tbxPost).

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/ttf"
)

const (
	// legibleFontFirstLook is how long after start the first look waits.
	legibleFontFirstLook = 20 * time.Second
	// legibleFontLookEvery is the ticker's interval while a face is wanted
	// and cannot be fetched yet.
	legibleFontLookEvery = 30 * time.Second
	// legibleFontMaxTries is how many failed downloads of one face a run
	// accepts before the next face is tried.
	legibleFontMaxTries = 3
	// legibleFaceAutoValue is the Readable font row's Auto stop.
	legibleFaceAutoValue = "auto"
)

// legibleFontAuto is the order the setting downloads faces in: Atkinson
// Hyperlegible (Latin), then Andika (Latin and Cyrillic). Both are
// uiFontCatalog ids.
var legibleFontAuto = []string{"atkinson", "andika"}

// legibleKeepStatus is what one look did.
type legibleKeepStatus int

const (
	// legibleKeepNone: nothing to fetch — a readable face is here, the
	// setting is off, or nothing more can be had this run.
	legibleKeepNone legibleKeepStatus = iota
	// legibleKeepDownloading: a download of the face started.
	legibleKeepDownloading
	// legibleKeepJoined: Display → Font is already fetching this very face.
	legibleKeepJoined
	// legibleKeepOffline: wanted, waiting for the network.
	legibleKeepOffline
	// legibleKeepBusy: wanted, waiting for another font's download.
	legibleKeepBusy
)

// legibleGet is the keeper's state (main thread only).
var legibleGet struct {
	cfg     *Config
	ticking bool
	// paused: a face the setting downloads was removed by hand, so nothing
	// is fetched until the owner asks again (the switch, a pick).
	paused bool
	failed map[string]int  // failed downloads per face this run
	got    map[string]bool // faces downloaded this run, never fetched twice
	// waiting names the face waiting for the network, for the row's detail.
	waiting string
}

// legibleFontIsAuto reports whether id is a face the setting downloads.
func legibleFontIsAuto(id string) bool {
	for _, a := range legibleFontAuto {
		if a == id {
			return true
		}
	}
	return false
}

// legibleEntryDraws reports whether a catalogue face that is not on the card
// yet can draw lang, as far as the catalogue knows: every catalogue face has
// the Latin letters, and only Russian and Ukrainian need more
// (uiFontEntry.Cyrillic). A face on the card is checked against its own
// character map instead.
func legibleEntryDraws(e uiFontEntry, lang string) bool {
	return (lang != "ru" && lang != "uk") || e.Cyrillic
}

// legibleFontWanted is the catalogue face the setting should download now, or
// false when a readable face that draws the interface language is already on
// the machine, a system face was picked and draws it, or nothing more can be
// fetched this run. Main thread.
func legibleFontWanted() (uiFontEntry, bool) {
	lang := uiLanguageCode()
	order := legibleFontAuto
	if pref := a11y.legibleFace; pref != "" {
		if legibleFontIsAuto(pref) {
			order = append([]string{pref}, legibleFontAuto...)
		} else if path := legibleFontFacePath(pref); path != "" && uiFontLanguageGap(path) == 0 {
			return uiFontEntry{}, false
		}
	}
	for _, id := range order {
		e, ok := uiFontEntryFor(id)
		if !ok {
			continue
		}
		if path := legibleFontFacePath(id); path != "" {
			if uiFontLanguageGap(path) == 0 {
				return uiFontEntry{}, false
			}
			continue
		}
		if !legibleEntryDraws(e, lang) || legibleGet.got[id] || legibleGet.failed[id] >= legibleFontMaxTries {
			continue
		}
		return e, true
	}
	return uiFontEntry{}, false
}

// legibleFontKeep is one look: when the setting is on and wants a face that is
// not here, it starts the download, or arms the ticker to wait for the
// network or for another font's download. explicit is a press that asks for a
// download (the switch, a "get:" pick in the Readable font list): earlier
// failures, a removal and this run's downloads are forgotten
// (got only stops a background look from fetching a face again whose file
// the index cannot see). It returns what it did and the face's name. Main
// thread.
func legibleFontKeep(c *Config, explicit bool) (legibleKeepStatus, string) {
	if c != nil {
		legibleGet.cfg = c
	}
	if testing.Testing() {
		return legibleKeepNone, ""
	}
	if explicit {
		legibleGet.paused = false
		legibleGet.failed = nil
		legibleGet.got = nil
	}
	legibleGet.waiting = ""
	if !a11y.legibleFont || legibleGet.paused {
		return legibleKeepNone, ""
	}
	e, ok := legibleFontWanted()
	if !ok {
		return legibleKeepNone, ""
	}
	if busy, _ := uiFontDownloadStatus(); busy != "" {
		// Look again after it: a Display → Font download of this very face
		// redraws the legible face when it lands (uiFontStartDownload), and
		// one that fails is retried here.
		legibleFontArm(false)
		if busy == e.ID {
			return legibleKeepJoined, e.Name
		}
		return legibleKeepBusy, e.Name
	}
	if !ttsAutoOnline() {
		legibleGet.waiting = e.Name
		legibleFontArm(false)
		return legibleKeepOffline, e.Name
	}
	logInfo("font", "legible reading font: downloading %s for %s", e.Name, uiLanguageCode())
	if !uiFontStartDownload(e, func(err error) { legibleFontLanded(e, explicit, err) }) {
		legibleFontArm(false)
		return legibleKeepBusy, e.Name
	}
	return legibleKeepDownloading, e.Name
}

// legibleFontLanded is a finished download (main thread, from
// uiFontStartDownload, which has already redrawn the screen when the face
// arrived). A failure is retried on the ticker; a press hears about it, a
// background fetch only after its last try.
func legibleFontLanded(e uiFontEntry, explicit bool, err error) {
	if err != nil {
		if legibleGet.failed == nil {
			legibleGet.failed = map[string]int{}
		}
		legibleGet.failed[e.ID]++
		tries := legibleGet.failed[e.ID]
		retry := tries < legibleFontMaxTries
		logWarn("font", "legible reading font: %s did not download (try %d of %d): %v", e.Name, tries, legibleFontMaxTries, err)
		if explicit || !retry {
			msg := "Could not download " + e.Name + ": " + uiFontProblem(err)
			if retry {
				msg += " · tries again soon"
			}
			ShowToast(msg, ToastKindWarning)
		}
		if a11y.legibleFont {
			// The next look retries, or moves on to the next face.
			legibleFontArm(false)
		}
		legibleFontHubRefresh()
		return
	}
	if legibleGet.got == nil {
		legibleGet.got = map[string]bool{}
	}
	legibleGet.got[e.ID] = true
	logInfo("font", "legible reading font: %s is in %s", e.Name, uiFontDir())
	if face := legibleFontFaceName(); a11y.legibleFont && face != "" {
		ShowToast("Legible reading font: "+face, ToastKindSuccess)
	} else {
		ShowToast(e.Name+" installed", ToastKindSuccess)
	}
	legibleFontHubRefresh()
	// A face fetched for one language may lack a letter another needs: one
	// more look, which ends at once when the face draws the language.
	legibleFontKeep(nil, false)
}

// legibleFontArm starts the look-again ticker unless it runs: a goroutine that
// sleeps, then hands one look to the main loop (legibleFontTick), until a look
// finds nothing left to wait for. One wake-up every 30 seconds while a face is
// wanted and cannot be fetched yet, none otherwise. Main thread.
func legibleFontArm(startup bool) {
	if legibleGet.ticking || testing.Testing() {
		return
	}
	legibleGet.ticking = true
	wait := legibleFontLookEvery
	if startup {
		wait = legibleFontFirstLook
	}
	go func() {
		for {
			time.Sleep(wait)
			wait = legibleFontLookEvery
			again := make(chan bool, 1)
			tbxPost(func() { again <- legibleFontTick() })
			if !<-again {
				return
			}
		}
	}()
}

// legibleFontTick is one of the ticker's looks; false ends the ticker. Main
// thread.
func legibleFontTick() bool {
	switch status, _ := legibleFontKeep(nil, false); status {
	case legibleKeepOffline, legibleKeepBusy, legibleKeepJoined:
		return true
	}
	legibleGet.ticking = false
	return false
}

// legibleFontStartup is a11yInit's part: with the setting on, the first look
// is armed for 20 seconds after start, so a face missing from the card (a
// fresh card, a copied config) is fetched without a press. Main thread.
func legibleFontStartup(c *Config) {
	if c != nil {
		legibleGet.cfg = c
	}
	if a11y.legibleFont {
		legibleFontArm(true)
	}
}

// legibleFontLanguageChanged runs when currentLegibleFont resolved the face
// again for a new interface language (whatever goroutine asked). On the main
// loop it redraws — Home's fonts are opened once and would keep a face that
// cannot draw the new language — and looks for the new language's face
// (Russian wants Andika's Cyrillic). Once per language change: the redraw's
// reset keeps the language, so the next resolution is not a change.
func legibleFontLanguageChanged() {
	tbxPost(func() {
		legibleFontRedraw()
		legibleFontHubRefresh()
		legibleFontKeep(nil, false)
	})
}

// legibleFontFolderChanged follows a font installed into or removed from the
// fonts folder (uifont.go): the folder is indexed again and, while the setting
// is on, the screen is redrawn in whatever face resolves now. removedID is a
// catalogue face removed by hand; when the setting downloads it, fetching
// stops for the run. Main thread.
func legibleFontFolderChanged(removedID string) {
	if legibleFontIsAuto(removedID) {
		legibleGet.paused = true
		delete(legibleGet.got, removedID)
	}
	if !a11y.legibleFont {
		legibleFontReset()
		return
	}
	legibleFontRedraw()
}

// legibleFontRedraw drops the legible resolution and every font-dependent
// cache, so the next frame draws in the face the setting resolves to now.
// Main thread.
func legibleFontRedraw() {
	legibleFontReset()
	a11yRefreshFonts()
	// The italic reader faces are cached apart (core.go) and a11yRefreshFonts
	// keeps them; they must follow the face too.
	fontStyleCache = make(map[string]*ttf.Font)
}

// legibleFontHubRefresh re-reads Settings' rows when they are on screen, after
// something changed behind them (a download landed). Main thread.
func legibleFontHubRefresh() {
	if c := legibleGet.cfg; c != nil && setIsHubScene(setHubSceneName(c)) {
		setHubRefresh(c)
	}
}

// legibleFontAnnounce looks for a press (the switch, a pick) and says what the
// screen is drawn in now, or what is being fetched. explicit is legibleFontKeep's:
// the switch and a "get:" pick from the full list; a Left/Right step (Auto
// included) is not, so it never undoes a removal or retries a failed face.
// Main thread.
func legibleFontAnnounce(c *Config, explicit bool) {
	status, name := legibleFontKeep(c, explicit)
	switch status {
	case legibleKeepDownloading, legibleKeepJoined:
		// The download says so itself: "Downloading Andika… 40%".
		return
	case legibleKeepOffline:
		ShowToast("Legible reading font on · "+name+" downloads when JukaHub is online", ToastKindInfo)
		return
	case legibleKeepBusy:
		ShowToast("Legible reading font on · "+name+" downloads after the current font", ToastKindInfo)
		return
	}
	if face := legibleFontFaceName(); face != "" {
		ShowToast("Legible reading font: "+face, ToastKindInfo)
	} else {
		ShowToast("Legible reading font on · no readable face for this language, using Inter", ToastKindInfo)
	}
}

// ── The Readable font row (a11y_settings.go) ──────────────────────────────

// legibleFaceOptions are the Readable font row's stops: Auto, then the faces
// this machine has that draw the interface language (Left/Right steps through
// these, so a step never downloads); all adds the faces that cannot, and the
// faces JukaHub can download, marked "get:" as in Display → Font, for the
// list A opens.
func legibleFaceOptions(all bool) []setOption {
	lang := uiLanguageCode()
	auto := "Auto"
	if a11y.legibleFace == "" {
		if face := legibleFontFaceName(); face != "" {
			auto = "Auto · " + face
		}
	}
	out := []setOption{{Value: legibleFaceAutoValue, Label: auto,
		Note: "Atkinson Hyperlegible, or Andika for Russian"}}
	have := map[string]string{}
	for _, f := range legibleFontInstalledFaces() {
		have[f.ID] = legibleFontFacePath(f.ID)
	}
	busy, pct := uiFontDownloadStatus()
	for _, face := range legibleFontFaces {
		e, own := uiFontEntryFor(face.ID)
		if path, ok := have[face.ID]; ok {
			note := "On this device"
			if own {
				note = "Installed"
			}
			if uiFontLanguageGap(path) != 0 {
				if !all {
					// legibleFontChoose refuses it, so a Left/Right stop on it
					// would never move off it: only the full list names it.
					continue
				}
				note += " · " + legibleNoLettersNote(lang)
			}
			out = append(out, setOption{Value: face.ID, Label: face.Name, Note: note})
			continue
		}
		if !all || !own || !legibleFontIsAuto(face.ID) {
			continue
		}
		note := "Download " + uiFontSizeLabel(e.KB)
		if busy == face.ID {
			note = "Downloading… " + strconv.Itoa(pct) + "%"
		}
		if !legibleEntryDraws(e, lang) {
			note += " · " + legibleNoLettersNote(lang)
		}
		out = append(out, setOption{Value: uiFontGetPrefix + face.ID, Label: face.Name, Note: note})
	}
	return out
}

// legibleNoLettersNote is the list's note on a face that cannot draw lang.
func legibleNoLettersNote(lang string) string {
	if lang == "ru" || lang == "uk" {
		return "Latin letters only"
	}
	return "missing letters for this language"
}

// legibleFaceValue is the picked face as the row's options name it.
func legibleFaceValue() string {
	if a11y.legibleFace == "" {
		return legibleFaceAutoValue
	}
	return a11y.legibleFace
}

// legibleFaceDownloading is the setting's face being downloaded ("" when
// none) and how far along it is.
func legibleFaceDownloading() (string, int) {
	busy, pct := uiFontDownloadStatus()
	if !legibleFontIsAuto(busy) {
		return "", 0
	}
	return busy, pct
}

// legibleFaceReadout is the row's read-out when no stop is current: the
// download's progress, else the face the screen is drawn in.
func legibleFaceReadout() string {
	if busy, pct := legibleFaceDownloading(); busy != "" {
		return "Downloading " + strconv.Itoa(pct) + "%"
	}
	if face := legibleFontFaceName(); face != "" {
		return face
	}
	return "Inter"
}

// legibleFaceDetail is the row's line under the label.
func legibleFaceDetail() string {
	if busy, pct := legibleFaceDownloading(); busy != "" {
		return "Downloading " + uiFontChoiceName(busy) + "… " + strconv.Itoa(pct) + "%"
	}
	if legibleGet.waiting != "" {
		return legibleGet.waiting + " downloads when JukaHub is online · Inter until then"
	}
	return "Auto downloads Atkinson Hyperlegible, or Andika for Russian, the first time it is needed"
}

// legibleFontChoose is a pick in the Readable font row. A face that is here
// applies at once; a face JukaHub downloads is fetched first, but only from
// the full list's "get:" entry, so Left/Right never start a download. A face
// that cannot draw the interface language is refused. Main thread.
func legibleFontChoose(c *Config, value string) {
	id, get := strings.CutPrefix(strings.TrimSpace(value), uiFontGetPrefix)
	if id == legibleFaceAutoValue {
		id = ""
	}
	if id != "" {
		face, ok := legibleFontFaceFor(id)
		if !ok {
			return
		}
		lang := uiLanguageCode()
		refusal := face.Name + " has no letters for " + uiLanguageNative(lang)
		if path := legibleFontFacePath(id); path != "" {
			if uiFontLanguageGap(path) != 0 {
				ShowToast(refusal, ToastKindWarning)
				return
			}
		} else {
			e, own := uiFontEntryFor(id)
			if !own || !get || !legibleFontIsAuto(id) {
				ShowToast("That font is no longer on this device", ToastKindWarning)
				return
			}
			if !legibleEntryDraws(e, lang) {
				ShowToast(refusal, ToastKindWarning)
				return
			}
		}
	}
	if id != a11y.legibleFace {
		a11y.legibleFace = id
		a11ySave(c)
	}
	legibleFontRedraw()
	legibleFontAnnounce(c, get)
}
