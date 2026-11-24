package main

// qrmaker.go — Tools → Everyday apps → QR maker: type or paste text (a link,
// a Wi-Fi password, a note) and it fills the screen as a QR code a phone can
// scan, on the same full-screen card the pairing codes use (qrzoom.go: the
// largest square the screen allows, a proper quiet zone, the backlight at full
// while it is up). The keyboard opens with the last text, so a second code is
// an edit; its tool row pastes.

import (
	"strings"
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
	qrZoomOpenCardDetail(modules, "QR code", qrMakerCaption(text))
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
