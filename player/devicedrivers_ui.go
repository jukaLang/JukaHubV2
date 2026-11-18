package main

// devicedrivers_ui.go — Settings → About → Drivers & hardware.
//
// One line per part of the handheld — the model and how it was named, the
// display and GPU, the backlight, the kernel, the firmware, and whatever the
// subsystems registered (registerDriverStatus) — each with "OK", "not found"
// or "not used on this model" and the driver and node behind it underneath.
// It is the Settings list sheet (setsheet.go), so it scrolls and closes like
// every other sheet; it is read-only, and A or B closes it.
//
// The answers are collected off the render thread (collectDriverStatus, each
// part under its own deadline): the sheet opens at once with "Checking…" and
// fills in through the UI queue (tbxPost). Every collected table is also
// written to the log, so a tester's log carries what the page showed.

import (
	"strings"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

// driversSheetTitle is the sheet's title, and how a late answer finds it.
const driversSheetTitle = "Drivers & hardware"

// driversCollecting is up while a collection runs, so a second press does not
// start another.
var driversCollecting atomic.Bool

// driversOpenSheet is the Settings row's press.
func driversOpenSheet(c *Config) {
	setSheetOpen(&setHub.sheet, driversSheetTitle, driversSheetNote(),
		[]setOption{{Value: "checking", Label: "Checking the hardware…"}}, "", nil)
	if !driversCollecting.CompareAndSwap(false, true) {
		return
	}
	// SDL is asked here, on the main thread; the rest runs on its own.
	env := driverStatusEnv{}
	if drv, err := sdl.GetCurrentVideoDriver(); err == nil {
		env.VideoDriver = drv
	}
	go func() {
		defer driversCollecting.Store(false)
		rows := collectDriverStatus(env)
		driversLogRows(rows)
		opts := driversSheetOptions(rows)
		tbxPost(func() {
			if setHub.sheet.open && setHub.sheet.title == driversSheetTitle {
				setHub.sheet.options = opts
				setHub.sheet.cursor = 0
				setHub.sheet.scroll = 0
			}
		})
	}()
}

// driversSheetNote is the line under the title: the model in a few words.
func driversSheetNote() string {
	hw := deviceHardwareCurrent()
	parts := []string{deviceDisplayName(hw.Model)}
	if hw.SoC != "" {
		parts = append(parts, "Allwinner "+hw.SoC)
	}
	return strings.Join(parts, " · ") + " · read-only"
}

// driversSheetOptions turns the rows into the sheet's lines.
func driversSheetOptions(rows []driverStatusRow) []setOption {
	opts := make([]setOption, 0, len(rows))
	for _, r := range rows {
		opts = append(opts, setOption{
			Value: r.Name,
			Label: r.Name + " · " + driverStateLabel(r.Status.State),
			Note:  driverStatusLine(r.Status),
		})
	}
	return opts
}

// driverStatusLine is a status's driver, node and detail on one line.
func driverStatusLine(st driverStatus) string {
	var parts []string
	for _, p := range []string{st.Driver, st.Node, st.Detail} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// driversLogRows writes the table to the log, one line per part.
func driversLogRows(rows []driverStatusRow) {
	for _, r := range rows {
		logInfo("drivers", "%s: %s · %s", r.Name, driverStateLabel(r.Status.State), driverStatusLine(r.Status))
	}
}
