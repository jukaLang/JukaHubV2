package main

// brw_elsewhere.go — the Browser menu's "take this page elsewhere" row.
//
// JukaHub's built-in browser is the only browser it opens (browser.go): no
// WPE, no WebKit. When a page needs more than the built-in browser can draw,
// the Select menu still has one way out on every device, in the row's fixed
// place: on a PC the desktop's own web browser, and on a handheld — which has
// no other browser JukaHub starts — the address as a full-screen QR code
// (qrzoom.go), so a phone can open it.

// brwElsewhereQRTitle heads the handheld's QR card.
const brwElsewhereQRTitle = "Open on your phone"

// brwElsewhereEntry is the row's words, glyph and read-out on this device.
func brwElsewhereEntry() (label, icon, value string) {
	if brwHasDesktopBrowser() {
		return "Open in web browser", "link", ""
	}
	return brwElsewhereQRTitle, "phone", "QR code"
}

// brwElsewhereActivate runs the row for the page on screen: the desktop's
// browser opens it on a PC, the QR card shows it on a handheld. Main thread.
func brwElsewhereActivate(config *Config) {
	page := brwInfoPageURL(config)
	if page == "" {
		PlayNavSound()
		ShowToast("Open a page first", ToastKindInfo)
		return
	}
	if brwHasDesktopBrowser() {
		if err := openExternalURL(page); err != nil {
			ShowToast("Couldn't open your web browser: "+err.Error(), ToastKindWarning)
			return
		}
		PlayActivateSound()
		ShowToast("Opened in your web browser", ToastKindInfo)
		return
	}
	modules := tbxQRModules(page)
	if len(modules) == 0 {
		PlayNavSound()
		ShowToast("This address is too long for a QR code", ToastKindInfo)
		return
	}
	// The caption names the host only: a search address carries what was
	// searched for, and the code itself already holds the whole address.
	qrZoomOpenCardDetail(modules, brwElsewhereQRTitle, "Scan to open "+brwHostOf(page))
}
