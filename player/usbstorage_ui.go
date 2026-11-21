package main

// usbstorage_ui.go — what USB storage mode (usbstorage.go) shows: the sheet
// after the checks (why it cannot go, or what will happen and a Start), and
// the full-screen card that stays on the panel while the card is lent out.
//
// The full-screen card is drawn by JukaHub and kept: once it has been on the
// panel for a few frames, its framebuffer page is read (bootFrameGrab) and
// written to /tmp, and the helper puts that picture back after JukaHub has
// quit. So it is plain words, no button hints of JukaHub's own (nothing of
// JukaHub answers the buttons then; any button is the helper's "come back").

import (
	"github.com/veandco/go-sdl2/sdl"
)

// usbStorageCardFrames is how many frames the card is drawn before its
// picture is read: the panel shows the second presented frame by then,
// whichever page of a double buffer it is on.
const usbStorageCardFrames = 3

// usbStorageShowPlan answers the checks with a sheet. Main thread.
func usbStorageShowPlan(config *Config, plan usbStoragePlan) {
	if plan.problem != "" {
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "USB storage mode can't start now",
			subtitle: "The SD card stays with JukaHub",
			icon:     "warning",
			body: plan.problem + "\n\nUSB file transfer (the row above) works while JukaHub runs: " +
				"the computer opens the card as a device, like a phone's storage.",
			buttons: []tbxSheetButton{{label: "OK", primary: true}},
		})
		return
	}
	what := "your SD card"
	if plan.size != "" {
		what = "your " + plan.size + " SD card"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Lend the SD card to a computer?",
		subtitle: plan.dev + " · " + plan.fsType,
		icon:     "disk",
		body: "JukaHub and any game close, and " + what + " turns into a USB drive on the computer, like a card " +
			"reader: copy anything to and from it with any program.\n\n" +
			"• Connect the handheld to the computer with a USB data cable (before or after starting).\n" +
			"• When you are done, eject the drive on the computer (Eject, or Safely Remove Hardware). " +
			"JukaHub comes back by itself.\n" +
			"• Or press any button on the handheld to come back. Eject first: a button pressed while the " +
			"computer is still writing can cut that file short.\n\n" +
			"While the card is lent out, nothing on the handheld can use it. With no computer after 10 " +
			"minutes, JukaHub comes back on its own.",
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "Start", primary: true, run: func() { usbStorageBegin(plan) }},
		},
	})
}

// usbStorageRender draws the full-screen card while JukaHub leaves for the
// helper, over everything (main.go calls it last among the overlays), and
// starts the save once the panel shows it. Main thread.
func usbStorageRender(renderer *sdl.Renderer, config *Config) {
	if !usbStorage.leaving || renderer == nil || config == nil {
		return
	}
	// Frames keep coming while the card waits for its picture to be read.
	requestFrameEvery(16)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_NONE)
	setDrawColor(renderer, sdl.Color{R: 9, G: 13, B: 24, A: 255})
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fonts, ok := tbxLoadFonts(config)
	if ok {
		usbStorageDrawCard(renderer, config, fonts)
	}
	usbStorage.frames++
	if usbStorage.frames == usbStorageCardFrames {
		go usbStorageSave(usbStorage.plan)
	}
}

// usbStorageDrawCard is the card itself: a header and four short paragraphs.
func usbStorageDrawCard(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	cardW := int32(820)
	if cardW > screenWidth-48 {
		cardW = screenWidth - 48
	}
	paras := []string{
		"Copy anything to and from it on the computer, as with a card reader.",
		"Done? Eject the drive on the computer (Eject, or Safely Remove Hardware) and JukaHub comes back by itself.",
		"Or press any button here to come back. Eject first, or a copy still running on the computer can be cut short.",
		"Nothing happening? Connect a USB data cable to the computer.",
	}
	measure := func(s string) int32 {
		w, _ := measureText(fonts.body, s)
		return w
	}
	var lines []string
	for i, p := range paras {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, tbxWrapByMeasure(p, cardW-2*OverlayPadX, measure)...)
	}
	header := UIPanelHeader("USB storage mode", "Your SD card is a USB drive on the computer", "disk")
	headerH := UIPanelHeaderBand(config, header, 8)
	lineH := fonts.bodyH + 4
	gap := lineH / 2
	bodyH := int32(0)
	for _, l := range lines {
		if l == "" {
			bodyH += gap
		} else {
			bodyH += lineH
		}
	}
	footH := fonts.smallH + 28
	cardH := headerH + bodyH + 12 + footH
	if cardH > screenHeight-32 {
		cardH = screenHeight - 32
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))
	y := card.Y + headerH
	for _, l := range lines {
		if l == "" {
			y += gap
			continue
		}
		if y+lineH > card.Y+card.H-footH {
			break
		}
		renderText(renderer, config, fonts.body, l, ColorTextSecondary(), card.X+OverlayPadX, y)
		y += lineH
	}
	foot := ellipsize(fonts.small, "JukaHub is closed meanwhile. Coming back can take a minute when the card needs a check.",
		card.W-2*OverlayPadX)
	renderText(renderer, config, fonts.small, foot, ColorTextTertiary(), card.X+OverlayPadX, card.Y+card.H-footH+12)
}
