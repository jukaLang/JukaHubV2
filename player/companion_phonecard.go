package main

// companion_phonecard.go — "send it from your phone": a full-screen QR code
// (qrzoom.go) that opens the Phone Companion page at one of its cards, so the
// phone lands on the field the paste goes into.
//
// The link is the companion's own pairing link (companionURL: the LAN address
// and the PIN) with the card's name as its fragment — "#groq" or "#vpn". A
// fragment never reaches the server; the page reads it, moves that card to the
// top under its screen bar and puts the cursor in the card's field
// (companion.go). The code on the handheld waits for what it asked for: when
// the key or the configuration arrives, phoneCardArrived closes it.

import (
	"strings"

	"github.com/skip2/go-qrcode"
)

// phoneCardKind names a card of the companion page: the fragment its link
// carries, which is also the start of the card's element id ("groqCard").
type phoneCardKind string

const (
	phoneCardGroq phoneCardKind = "groq"
	phoneCardVPN  phoneCardKind = "vpn"
)

// phoneCard is the code on screen: the card it opens and the title it was
// shown under, so something arriving after the user closed it (or opened
// another code) closes nothing. Main thread only.
var phoneCard struct {
	kind  phoneCardKind
	title string
}

// companionCardURL is the pairing link with the card's fragment. No link, no
// card or a link that already has a fragment comes back as it is. Pure.
func companionCardURL(link string, kind phoneCardKind) string {
	if link == "" || kind == "" || strings.Contains(link, "#") {
		return link
	}
	return link + "#" + string(kind)
}

// phoneCardOpen starts the companion when it is not up and shows the link to
// the card full screen, with detail (what to do on the phone) and the PIN and
// address under it for a phone that cannot scan. It reports whether the code
// is on screen; without Wi-Fi (or in safe mode) a toast says why instead.
func phoneCardOpen(kind phoneCardKind, title, detail string) bool {
	companionStart()
	if companion.url == "" {
		msg := companion.err
		if msg == "" {
			msg = "Your phone sends it over Wi-Fi: connect this device first"
		}
		ShowToast(msg, ToastKindWarning)
		return false
	}
	code, err := qrcode.New(companionCardURL(companion.url, kind), qrcode.Low)
	if err != nil {
		ShowToast("Cannot draw the QR code: "+err.Error(), ToastKindWarning)
		return false
	}
	if caption := companionPairingCaption(companion.url); caption != "" {
		detail += " · " + caption
	}
	if !qrZoomOpenCardDetail(code.Bitmap(), title, detail) {
		return false
	}
	phoneCard.kind, phoneCard.title = kind, title
	return true
}

// phoneCardArrived closes the code for kind while it is still the one on
// screen: what it asked the phone for has arrived.
func phoneCardArrived(kind phoneCardKind) {
	if kind == "" || phoneCard.kind != kind {
		return
	}
	title := phoneCard.title
	phoneCard.kind, phoneCard.title = "", ""
	if qrZoom.open && qrZoom.title == title {
		qrZoomCloseCard()
	}
}
