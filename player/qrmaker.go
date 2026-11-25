package main

// qrmaker.go — Tools → Everyday apps → QR maker: type or paste text (a link,
// a Wi-Fi password, a note) and it fills the screen as a QR code a phone can
// scan, on the same full-screen card the pairing codes use (qrzoom.go: the
// largest square the screen allows, a proper quiet zone, the backlight at full
// while it is up). The keyboard opens with the last text, so a second code is
// an edit; its tool row pastes.
//
// X on the card (S on a keyboard) saves the code as a PNG — black modules on
// white with the four-module quiet zone, about 1000 px square — into the
// screenshots folder (screenshot.go screenshotDir: JukaHub/screenshots, the
// one the Photos and wallpaper pickers already list), named qr_<date>_<time>.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/skip2/go-qrcode"
)

// qrMakerMaxRunes is the longest text the field takes. A code that long is
// near the format's limit; a phone camera reads it best on a large screen.
const qrMakerMaxRunes = 1200

// qrMakerLast is the last text shown as a code. Main thread only.
var qrMakerLast string

// qrMakerOpen asks for the text (cross-area contract: the Tools hub and Home
// search call it).
func qrMakerOpen(config *Config) {
	_ = config
	osPromptOpen("QR code", "A link or any text to show as a QR code", "Show", qrMakerLast, qrMakerMaxRunes, qrMakerShow)
}

// qrMakerShow shows text as a code on the full-screen card.
func qrMakerShow(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		ShowToast("Type or paste something first", ToastKindInfo)
		return
	}
	modules := qrMakerModules(text)
	if len(modules) == 0 {
		ShowToast("Too long for one QR code — shorten it", ToastKindWarning)
		return
	}
	qrMakerLast = text
	if qrZoomOpenCardDetail(modules, "QR code", qrMakerCaption(text)) {
		qrZoom.save = func() { qrMakerSave(modules) }
	}
}

// qrMakerSaving is set while a save writes, so a held X does not write a
// dozen copies. Main thread.
var qrMakerSaving bool

// qrMakerSave writes the code on the card as a PNG into the screenshots
// folder, off the main thread (an SD card can take a moment), and says where.
func qrMakerSave(modules [][]bool) {
	if qrMakerSaving {
		return
	}
	data, err := qrMakerPNG(modules)
	if err != nil {
		PlayErrorSound()
		ShowToast("Could not draw the picture: "+err.Error(), ToastKindWarning)
		return
	}
	qrMakerSaving = true
	PlayActivateSound()
	now := time.Now()
	go func() {
		path, err := screenshotPath("qr", now)
		if err == nil {
			err = AtomicWrite(path, data, 0o644)
		}
		tbxPost(func() {
			qrMakerSaving = false
			if err != nil {
				logWarn("qrmaker", "could not save the QR code: %v", err)
				PlayErrorSound()
				ShowToast("Could not save the QR code: "+err.Error(), ToastKindWarning)
				return
			}
			logInfo("qrmaker", "saved %s", path)
			PlaySuccessSound()
			ShowToast("QR code saved to "+qrMakerSavedWhere(path), ToastKindSuccess)
		})
	}()
}

// qrMakerSavedWhere is the saved file as a person finds it: the folder's own
// name and the file ("screenshots/qr_2026-11-25_09-30-00.png").
func qrMakerSavedWhere(path string) string {
	return filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
}

// qrMakerPNG draws modules black on white with a four-module quiet zone, each
// module a whole number of pixels, about 1000 px across. Pure.
func qrMakerPNG(modules [][]bool) ([]byte, error) {
	n := len(modules)
	if n == 0 {
		return nil, os.ErrInvalid
	}
	const quiet = 4
	total := n + 2*quiet
	scale := max(4, min(24, 1000/total))
	size := total * scale
	img := image.NewGray(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	black := color.Gray{Y: 0}
	for y, row := range modules {
		for x, dark := range row {
			if !dark {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, black)
				}
			}
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// qrMakerModules encodes text without go-qrcode's own quiet zone (the card
// draws one sized to the code). A long text drops to the lower error
// correction, which keeps its modules larger on the screen. nil when even
// that cannot hold it.
func qrMakerModules(text string) [][]bool {
	level := qrcode.Medium
	if len(text) > 300 {
		level = qrcode.Low
	}
	code, err := qrcode.New(text, level)
	if err != nil && level != qrcode.Low {
		code, err = qrcode.New(text, qrcode.Low)
	}
	if err != nil || code == nil {
		return nil
	}
	return qrCoreModules(code.Bitmap())
}

// qrMakerCaption is the line under the code: the text itself, cut to fit one
// line, so the person holding the phone knows what it will open.
func qrMakerCaption(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > 60 {
		r := []rune(text)
		text = string(r[:59]) + "…"
	}
	return text
}

// qrMakerHubTile is the Tools hub's tile (Everyday apps).
func qrMakerHubTile() toolsHubTile {
	t := toolsHubTile{id: "qrmaker", label: "QR maker", icon: "qr", value: "Text or a link as a code for a phone"}
	if qrMakerLast != "" && !shotModeOn() {
		t.value = "Last: " + qrMakerCaption(qrMakerLast)
	}
	return t
}
