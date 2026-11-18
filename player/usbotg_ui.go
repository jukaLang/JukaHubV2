package main

// usbotg_ui.go — the USB & OTG screen, reached from the Tools hub.
//
// Three sections, top to bottom: the USB-C port's mode (and, where the
// firmware lets an app change it, Auto / Host / Device), the USB drives with
// their mount state, and every other device on the port with what it is. A on
// a drive opens its sheet: Mount, Open in Files, Import games and Eject. A on
// anything else explains it.
//
// The screen never reads the disk itself. Every two seconds while it is up it
// asks usbotg.go for a scan on a goroutine and redraws from the last snapshot;
// mount, eject, port switches and imports run on goroutines too and report
// back through tbxPost. On Windows it only says that these tools are for the
// handheld.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// usbOtgSceneName is the scene the Tools hub opens.
const usbOtgSceneName = "USBTools"

// usbOtgImportToken names the import progress sheet.
const usbOtgImportToken = "usbotg-import"

type usbOtgRow struct {
	id   string
	spec tbxRowSpec
}

type usbOtgModel struct {
	gen     uint64
	rev     uint64
	snap    usbOtgSnapshot
	header  UIPageHeader
	portRow tbxRowSpec
	mode    string
	autoOn  bool
	drives  []usbOtgRow
	devices []usbOtgRow
	noDev   tbxRowSpec
}

type usbOtgScreenState struct {
	visit     tbxVisit
	nav       tbxNav
	model     usbOtgModel
	built     bool
	refreshAt time.Time
}

var usbOtgScreen usbOtgScreenState

// usbOtgUI is the screen's own state, main thread only: the work running per
// disk (shown in its pill), the disks ejected but still plugged in (with when
// the eject finished), and the import (one at a time).
var usbOtgUI struct {
	rev        uint64
	busy       map[string]string
	ejected    map[string]time.Time
	importing  bool
	importDisk string
	importBody string
	importStop context.CancelFunc
}

// usbOtgModeOptions are the port mode buttons.
var usbOtgModeOptions = []struct{ mode, label string }{
	{"auto", "Auto"},
	{"host", "Host"},
	{"device", "Device"},
}

// usbOtgOpenScreen opens USB & OTG. Cross-area contract: the Tools hub calls
// it.
func usbOtgOpenScreen(config *Config) {
	tbxOpenScene(config, usbOtgSceneName)
}

type usbOtgScene struct{ sceneBase }

func (usbOtgScene) render(r *sdl.Renderer, c *Config) bool {
	if usbOtgScreen.visit.begin() {
		usbOtgScreen.nav.scroll = 0
		usbOtgScreen.built = false
		usbOtgScreen.refreshAt = time.Time{}
	}
	usbOtgRenderScreen(r, c)
	return true
}

func (usbOtgScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), usbOtgSceneAction)
}

func (usbOtgScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), usbOtgSceneAction)
}

func init() { registerScene(usbOtgScene{sceneBase{name: usbOtgSceneName}}) }

// usbOtgSetBusy shows (or, with "", clears) work running on a disk.
func usbOtgSetBusy(disk, what string) {
	if usbOtgUI.busy == nil {
		usbOtgUI.busy = map[string]string{}
	}
	if what == "" {
		delete(usbOtgUI.busy, disk)
	} else {
		usbOtgUI.busy[disk] = what
	}
	usbOtgUI.rev++
}

// ── Model ────────────────────────────────────────────────────────────────

// usbOtgPortText is the port row's explanation of a mode.
func usbOtgPortText(mode string) string {
	switch mode {
	case "host":
		return "Host: sticks, pads and DACs plugged in here work"
	case "device":
		return "Device: a computer sees the handheld; sticks and pads do not work"
	case "none":
		return "Off: nothing works through the port right now"
	}
	return "The firmware does not say which way the port works"
}

func usbOtgBuildModel(snap usbOtgSnapshot) usbOtgModel {
	m := usbOtgModel{gen: snap.gen, rev: usbOtgUI.rev, snap: snap, mode: "auto"}
	if snap.gen > 0 {
		// The scan loaded the settings file on its goroutine already.
		s := usbOtgLoadedSettings()
		m.mode, m.autoOn = s.Mode, s.AutoMount
	}

	status, statusColor := "Looking…", ColorTextMuted()
	if runtime.GOOS != "linux" {
		status = usbOtgTileValue()
	} else if snap.gen > 0 {
		status = usbOtgTileText(snap)
		if status != "Nothing plugged in" {
			statusColor = ToastSuccessInk()
		}
	}
	m.header = tbxHeader("USB & OTG", "Sticks, pads and DACs on the USB-C port", "plug", status, statusColor)

	port := snap.port
	m.portRow = tbxRowSpec{icon: "plug", title: "USB-C port", pill: usbModeLabel(port.Mode), tone: tbxToneNeutral}
	switch {
	case !port.Found:
		m.portRow.subtitle = "Fixed by the firmware"
	case !port.Switchable:
		m.portRow.subtitle = "Fixed by the firmware · " + usbOtgPortText(port.Mode)
	case m.mode == "auto":
		m.portRow.subtitle = usbOtgPortText(port.Mode) + " · Auto"
	default:
		m.portRow.subtitle = usbOtgPortText(port.Mode) + " · restored at every start"
	}
	switch port.Mode {
	case "host":
		m.portRow.tone = tbxToneGood
	case "device":
		m.portRow.tone = tbxToneAccent
	}

	// A disk that was ejected stays "Safe to remove" until it is unplugged
	// or mounted again. Only a scan that started after the eject finished
	// counts: one already running read the mounts while they were still up.
	present, mounted := map[string]bool{}, map[string]bool{}
	for _, v := range snap.volumes {
		present[v.Disk] = true
		if v.MountedAt != "" {
			mounted[v.Disk] = true
		}
	}
	if snap.gen > 0 {
		for disk, at := range usbOtgUI.ejected {
			if snap.at.After(at) && (!present[disk] || mounted[disk]) {
				delete(usbOtgUI.ejected, disk)
			}
		}
	}

	for _, v := range snap.volumes {
		spec := tbxRowSpec{icon: "disk", title: usbOtgVolumeTitle(v), subtitle: usbOtgVolumeSubtitle(v), pill: "Not mounted", tone: tbxToneNeutral}
		switch {
		case usbOtgUI.busy[v.Disk] != "":
			spec.pill, spec.tone = usbOtgUI.busy[v.Disk], tbxToneAccent
		case !usbOtgUI.ejected[v.Disk].IsZero():
			spec.pill, spec.tone = "Safe to remove", tbxToneGood
		case v.MountedAt != "":
			spec.pill, spec.tone = "Mounted", tbxToneGood
		}
		if v.RomsDir != "" {
			spec.note = "Games inside"
		}
		m.drives = append(m.drives, usbOtgRow{id: "vol:" + v.Name, spec: spec})
	}

	for _, d := range snap.devices {
		var bits []string
		if d.Manufacturer != "" {
			bits = append(bits, d.Manufacturer)
		}
		if sp := usbSpeedLabel(d.Speed); sp != "" {
			bits = append(bits, sp)
		}
		bits = append(bits, d.VendorID+":"+d.ProductID)
		if d.Fixed {
			bits = append(bits, "built in")
		}
		m.devices = append(m.devices, usbOtgRow{
			id:   "dev:" + d.Name,
			spec: tbxRowSpec{icon: d.Kind.icon(), title: d.title(), subtitle: strings.Join(bits, " · "), pill: d.Kind.label(), tone: tbxToneNeutral},
		})
	}
	m.noDev = tbxRowSpec{icon: "plug", title: "Nothing plugged in", subtitle: "Plug a pad, keyboard, stick or USB-C DAC into the port", dim: true}
	if port.Mode == "device" {
		m.noDev.subtitle = "The port is in Device mode; switch it to Host to use sticks and pads"
	}
	return m
}

// usbOtgVolumeTitle names a drive: its label, else its maker and model.
func usbOtgVolumeTitle(v usbVolume) string {
	if v.Label != "" {
		return v.Label
	}
	if name := strings.TrimSpace(v.Vendor + " " + v.Model); name != "" {
		return name
	}
	return "USB drive"
}

func usbOtgVolumeSubtitle(v usbVolume) string {
	bits := []string{v.Name, humanBytes(v.Size)}
	if fs := usbFSName(v.FSType); fs != "" {
		bits = append(bits, fs)
	}
	if v.MountedAt != "" {
		bits = append(bits, "at "+v.MountedAt)
	}
	return strings.Join(bits, " · ")
}

// ── Drawing ──────────────────────────────────────────────────────────────

func usbOtgRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(usbOtgScreen.refreshAt) >= 2*time.Second {
		usbOtgScreen.refreshAt = now
		usbOtgRefresh()
	}
	snap := usbOtgCurrent()
	if !usbOtgScreen.built || snap.gen != usbOtgScreen.model.gen || usbOtgUI.rev != usbOtgScreen.model.rev {
		usbOtgScreen.model = usbOtgBuildModel(snap)
		usbOtgScreen.built = true
	}
	m := &usbOtgScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &usbOtgScreen.nav
	nav.begin(body.Y, body.H)

	if runtime.GOOS != "linux" {
		tbxDrawEmpty(renderer, config, body, "plug", "For the handheld",
			"USB & OTG shows what is plugged into the handheld's USB-C port, mounts USB sticks and copies games from them. On a computer the system does all of that itself.")
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}
	if m.gen == 0 {
		tbxDrawEmpty(renderer, config, body, "plug", "Looking at the USB port…", "")
		nav.end()
		tbxEndFrame(renderer, config)
		return
	}

	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawRow := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}

	// Port mode.
	tbxDrawSection(renderer, config, "Port mode", "plug", x, top+y, w)
	y += tbxSectionHeight
	drawRow("port", m.portRow)
	if m.snap.port.Switchable {
		const btnH, gap = int32(48), int32(12)
		bw := (w - 2*gap) / 3
		for i, opt := range usbOtgModeOptions {
			id := "mode:" + opt.mode
			rect := sdl.Rect{X: x + int32(i)*(bw+gap), Y: y, W: bw, H: btnH}
			screen := rect
			screen.Y += top
			tbxDrawButton(renderer, config, fonts.body, screen, opt.label, m.mode == opt.mode, nav.focused(id), false)
			nav.add(id, rect, row, false)
		}
		row++
		y += btnH + 8
	}
	y += 14

	// Drives.
	tbxDrawSection(renderer, config, "Drives", "disk", x, top+y, w)
	y += tbxSectionHeight
	for _, r := range m.drives {
		drawRow(r.id, r.spec)
	}
	if len(m.drives) == 0 {
		drawRow("", tbxRowSpec{icon: "disk", title: "No USB drive", subtitle: "Sticks and card readers show here when the port is in Host mode", dim: true})
	}
	auto := sdl.Rect{X: x, Y: y, W: w, H: rowH}
	autoScreen := auto
	autoScreen.Y += top
	usbOtgDrawToggleRow(renderer, config, fonts, autoScreen, "Mount drives when plugged in", "Checks every 3 seconds while JukaHub is open", m.autoOn, nav.focused("automount"))
	nav.add("automount", auto, row, false)
	row++
	y += rowH + 8 + 14

	// Devices.
	tbxDrawSection(renderer, config, "Devices", "devices", x, top+y, w)
	y += tbxSectionHeight
	for _, r := range m.devices {
		drawRow(r.id, r.spec)
	}
	if len(m.devices) == 0 {
		drawRow("", m.noDev)
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// usbOtgDrawToggleRow draws a labelled switch row.
func usbOtgDrawToggleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect, title, sub string, on, focused bool) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	toggle := tbxDrawToggle(renderer, r.X+r.W-18, r.Y+r.H/2, on)
	textW := toggle.X - r.X - 34
	block := fonts.titleH + 3 + fonts.smallH
	ty := r.Y + (r.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, title, textW), ColorTextPrimary(), r.X+18, ty)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, sub, textW), ColorTextMuted(), r.X+18, ty+fonts.titleH+3)
}

// ── Input ────────────────────────────────────────────────────────────────

func usbOtgSceneAction(config *Config, act tbxAction) bool {
	nav := &usbOtgScreen.nav
	switch act {
	case tbxActConfirm:
		usbOtgActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func usbOtgSceneClick(mx, my int32, config *Config) bool {
	it, ok := usbOtgScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	usbOtgScreen.nav.focus = it.id
	usbOtgActivate(config, it.id)
	return true
}

func usbOtgSceneHints() []FooterHint {
	if runtime.GOOS != "linux" {
		return []FooterHint{{Button: "B", Label: "Back"}}
	}
	label := "Details"
	focus := usbOtgScreen.nav.focus
	switch {
	case strings.HasPrefix(focus, "mode:"):
		label = "Choose"
	case focus == "automount":
		label = "Switch"
	case strings.HasPrefix(focus, "vol:"):
		label = "Options"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: "Back"}}
}

func usbOtgActivate(config *Config, id string) {
	m := &usbOtgScreen.model
	switch {
	case id == "":
		return
	case id == "port":
		PlayActivateSound()
		usbOtgOpenPortSheet(config, m.snap.port, m.mode)
	case strings.HasPrefix(id, "mode:"):
		usbOtgChooseMode(config, strings.TrimPrefix(id, "mode:"))
	case id == "automount":
		PlayActivateSound()
		on := !usbOtgAutoMount.Load()
		usbOtgSaveSettings(func(s *usbOtgSettings) { s.AutoMount = on })
		usbOtgUI.rev++
		if on {
			usbOtgStartWatcher()
			ShowToast("New USB drives mount by themselves at /mnt/usb", ToastKindSuccess)
		} else {
			ShowToast("USB drives wait for you to mount them", ToastKindInfo)
		}
	case strings.HasPrefix(id, "vol:"):
		name := strings.TrimPrefix(id, "vol:")
		for _, v := range m.snap.volumes {
			if v.Name == name {
				PlayActivateSound()
				usbOtgOpenDriveSheet(config, v)
				return
			}
		}
	case strings.HasPrefix(id, "dev:"):
		name := strings.TrimPrefix(id, "dev:")
		for _, d := range m.snap.devices {
			if d.Name == name {
				PlayActivateSound()
				usbOtgOpenDeviceSheet(config, d)
				return
			}
		}
	}
}

// ── Port mode ────────────────────────────────────────────────────────────

func usbOtgOpenPortSheet(config *Config, port usbPortState, saved string) {
	var b strings.Builder
	b.WriteString("## Right now\n" + usbOtgPortText(port.Mode) + ".\n\n")
	b.WriteString("## The modes\n")
	b.WriteString("• Host: the handheld powers the port and talks to what is plugged in — USB sticks, card readers, pads, keyboards, USB-C DACs and headsets, wired network adapters.\n")
	b.WriteString("• Device: the handheld is the one plugged in, for a computer to see — only if the firmware set up something for the computer to use (ADB or a drive).\n")
	b.WriteString("• Auto: the firmware decides, usually from the cable or adapter you plug in.\n\n")
	switch {
	case !port.Found || !port.Switchable:
		b.WriteString("## Fixed by the firmware\nThis firmware has no switch an app may use, so the port stays the way the firmware set it up. Most OTG adapters still work in Host mode by themselves.")
	default:
		b.WriteString("## How JukaHub switches it\n" + port.Source + " (" + port.rolePath + ").\n\n")
		if saved == "auto" {
			b.WriteString("Auto is chosen: JukaHub never changes the port at startup.")
		} else {
			b.WriteString(usbModeLabel(saved) + " is chosen: JukaHub switches the port back to it every time it starts. Choose Auto to stop that.")
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "USB-C port",
		subtitle: usbModeLabel(port.Mode),
		icon:     "plug",
		body:     b.String(),
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}

func usbOtgChooseMode(config *Config, mode string) {
	PlayActivateSound()
	switch mode {
	case "auto":
		usbOtgSaveSettings(func(s *usbOtgSettings) { s.Mode = "auto" })
		usbOtgUI.rev++
		// A switch made earlier in this run stays until the firmware
		// changes the port again; the BSP only re-detects after a restart.
		ShowToast("Auto: from the next start the firmware picks the mode", ToastKindInfo)
	case "host":
		usbOtgSwitchPort("host")
	case "device":
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "Switch to Device mode?",
			subtitle: "USB-C port",
			icon:     "plug",
			body: "In Device mode the handheld no longer powers the port: USB sticks, pads, keyboards and DACs stop working until you choose Host or Auto.\n\n" +
				"A computer only sees the handheld if the firmware set something up for it (ADB or a drive). JukaHub switches back to Device at every start until you choose another mode.",
			buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Switch to Device", primary: true, run: func() { usbOtgSwitchPort("device") }}},
			focus:   0,
		})
	}
}

// usbOtgSwitchPort switches the port on a goroutine and remembers the choice
// once the switch went through.
func usbOtgSwitchPort(mode string) {
	go func() {
		st := usbDetectPort()
		var err error
		// Already there: only the choice is remembered.
		if st.Mode != mode {
			err = usbApplyPortMode(st, mode)
		}
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not switch the port: "+firstLine(err.Error()), ToastKindError)
				return
			}
			usbOtgSaveSettings(func(s *usbOtgSettings) { s.Mode = mode })
			usbOtgUI.rev++
			ShowToast("USB-C port: "+usbModeLabel(mode)+" — kept at every start", ToastKindSuccess)
			usbOtgRefresh()
		})
	}()
}

// ── Drives ───────────────────────────────────────────────────────────────

func usbOtgOpenDriveSheet(config *Config, v usbVolume) {
	var b strings.Builder
	b.WriteString("## Drive\n")
	b.WriteString("• Device: /dev/" + v.Name + "\n")
	b.WriteString("• Size: " + humanBytes(v.Size) + "\n")
	if fs := usbFSName(v.FSType); fs != "" {
		b.WriteString("• Format: " + fs + "\n")
	}
	if maker := strings.TrimSpace(v.Vendor + " " + v.Model); maker != "" {
		b.WriteString("• Made by: " + maker + "\n")
	}
	busy := usbOtgUI.busy[v.Disk]
	var buttons []tbxSheetButton
	switch {
	case busy != "":
		b.WriteString("\n## Working\n" + busy + "\n")
		if usbOtgUI.importing && usbOtgUI.importDisk == v.Disk {
			buttons = append(buttons, tbxSheetButton{label: "Show progress", primary: true, run: func() { usbOtgShowImport(config, v) }})
		}
	case v.MountedAt != "":
		b.WriteString("\n## Mounted at\n" + v.MountedAt + "\n")
		if v.RomsDir != "" || v.BIOSDir != "" {
			b.WriteString("\n## Games on this drive\n")
			if v.RomsDir != "" {
				b.WriteString("Import games copies each system folder in " + filepath.Base(v.RomsDir) + "/ into the card's Roms folder")
				if v.BIOSDir != "" {
					b.WriteString(", and " + filepath.Base(v.BIOSDir) + "/ into the BIOS folder")
				}
				b.WriteString(". ")
			} else {
				b.WriteString("Import copies " + filepath.Base(v.BIOSDir) + "/ into the BIOS folder. ")
			}
			b.WriteString("Files already on the card are skipped; nothing is replaced or deleted.\n")
		}
		b.WriteString("\nEject before you unplug it, so nothing written is lost.")
		point := v.MountedAt
		buttons = append(buttons, tbxSheetButton{label: "Open in Files", primary: true, run: func() { usbOtgOpenInFiles(config, point) }})
		if v.RomsDir != "" || v.BIOSDir != "" {
			buttons = append(buttons, tbxSheetButton{label: "Import games", run: func() { usbOtgStartImport(config, v) }})
		}
		buttons = append(buttons, tbxSheetButton{label: "Eject", run: func() { usbOtgEject(v) }})
	default:
		if !usbOtgUI.ejected[v.Disk].IsZero() {
			b.WriteString("\nEjected: safe to remove.\n")
		}
		b.WriteString("\nMount makes the drive's files available at " + usbOtgMountRoot + "/" + v.Name + ".")
		buttons = append(buttons, tbxSheetButton{label: "Mount", primary: true, run: func() { usbOtgMount(v) }})
	}
	buttons = append(buttons, tbxSheetButton{label: "Close"})
	focus := 0
	for i, btn := range buttons {
		if btn.primary {
			focus = i
			break
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    usbOtgVolumeTitle(v),
		subtitle: usbOtgVolumeSubtitle(v),
		icon:     "disk",
		body:     b.String(),
		buttons:  buttons,
		focus:    focus,
	})
}

func usbOtgMount(v usbVolume) {
	if usbOtgUI.busy[v.Disk] != "" {
		return
	}
	usbOtgSetBusy(v.Disk, "Mounting…")
	go func() {
		point, err := usbOtgMountVolume(v.Name, v.FSType)
		tbxPost(func() {
			usbOtgSetBusy(v.Disk, "")
			if err != nil {
				ShowToast("Could not mount: "+firstLine(err.Error()), ToastKindError)
				return
			}
			delete(usbOtgUI.ejected, v.Disk)
			ShowToast("Mounted at "+point, ToastKindSuccess)
			usbOtgRefresh()
		})
	}()
}

func usbOtgEject(v usbVolume) {
	if usbOtgUI.importing && usbOtgUI.importDisk == v.Disk {
		ShowToast("Wait for the import to finish, or cancel it, first", ToastKindWarning)
		return
	}
	if usbOtgUI.busy[v.Disk] != "" {
		return
	}
	usbOtgSetBusy(v.Disk, "Ejecting…")
	go func() {
		lazy, err := usbOtgEjectDisk(v.Disk)
		tbxPost(func() {
			usbOtgSetBusy(v.Disk, "")
			usbOtgRefresh()
			if err != nil {
				ShowToast("Could not eject: "+firstLine(err.Error()), ToastKindError)
				return
			}
			if usbOtgUI.ejected == nil {
				usbOtgUI.ejected = map[string]time.Time{}
			}
			usbOtgUI.ejected[v.Disk] = time.Now()
			usbOtgUI.rev++
			if lazy {
				ShowToast("Ejected, but a program still had a file open — wait a few seconds before unplugging", ToastKindWarning)
				return
			}
			ShowToast("Safe to remove the drive", ToastKindSuccess)
		})
	}()
}

// usbOtgOpenInFiles opens Files on dir, the way Chat's "show in Files" does.
func usbOtgOpenInFiles(config *Config, dir string) {
	idx := sceneIndexExact(config, "FileExplorer")
	if config == nil || idx < 0 {
		ShowToast("Files is not part of this layout — the drive is at "+dir, ToastKindInfo)
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	// The location that holds the folder becomes the one Files browses, so
	// the folder is inside its root and the listing is allowed.
	locs := filesBuildLocations(config)
	if i := filesLocationIndex(locs, dir); i >= 0 {
		filesNav.location = locs[i].Path
	} else {
		filesNav.location = filesVolumeRoot(dir)
	}
	filesNav.atLocations = false
	config.Variables.Custom["fe_path"] = dir
	filesNav.restore = true
	feSelectionClear()
	if currentSceneIndex == idx {
		feListDirectory(config)
		return
	}
	changeSceneTo(config, idx)
}

// ── Import ───────────────────────────────────────────────────────────────

// usbOtgShowImport opens the progress sheet. Hide leaves the copy running;
// Cancel stops it and keeps the sheet up for the summary.
func usbOtgShowImport(config *Config, v usbVolume) {
	body := usbOtgUI.importBody
	if body == "" {
		body = "Looking at the drive…"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		token:    usbOtgImportToken,
		title:    "Import games",
		subtitle: usbOtgVolumeTitle(v),
		icon:     "download",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Cancel import", keepOpen: true, run: func() {
				if usbOtgUI.importStop != nil {
					usbOtgUI.importStop()
				}
			}},
			{label: "Hide", primary: true},
		},
		focus: 1,
	})
}

func usbOtgStartImport(config *Config, v usbVolume) {
	if usbOtgUI.importing {
		if usbOtgUI.importDisk != v.Disk {
			ShowToast("Another drive's import is still running — one at a time", ToastKindWarning)
			return
		}
		usbOtgShowImport(config, v)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	usbOtgUI.importing, usbOtgUI.importDisk, usbOtgUI.importStop = true, v.Disk, cancel
	usbOtgUI.importBody = "Looking at the drive…"
	usbOtgSetBusy(v.Disk, "Importing…")
	usbOtgShowImport(config, v)
	romsDir, biosDir := v.RomsDir, v.BIOSDir
	go func() {
		defer cancel()
		res := usbRunImport(ctx, romsDir, biosDir, func(doneFiles, totalFiles int, doneBytes, totalBytes int64, current string) {
			body := fmt.Sprintf("Copying file %d of %d\n%s of %s\n\n%s\n\nHide keeps the copy going. Cancel stops it; files already copied stay on the card.",
				doneFiles+1, totalFiles, humanBytes(doneBytes), humanBytes(totalBytes), current)
			tbxPost(func() {
				if !usbOtgUI.importing {
					return
				}
				usbOtgUI.importBody = body
				tbxUpdateSheet(usbOtgImportToken, body, nil)
			})
		})
		summary := usbImportSummary(res)
		tbxPost(func() {
			usbOtgUI.importing = false
			usbOtgUI.importStop = nil
			usbOtgUI.importBody = ""
			usbOtgSetBusy(v.Disk, "")
			if !tbxUpdateSheet(usbOtgImportToken, summary, []tbxSheetButton{{label: "Close", primary: true}}) {
				switch {
				case res.err != nil && !res.canceled:
					ShowToast("Import failed: "+firstLine(res.err.Error()), ToastKindError)
				case res.canceled:
					ShowToast("Import stopped — "+pluralizeCount(res.copied, "file", "files")+" copied", ToastKindInfo)
				case res.failed > 0:
					ShowToast(fmt.Sprintf("Imported %s; %d could not be copied", pluralizeCount(res.copied, "file", "files"), res.failed), ToastKindWarning)
				default:
					ShowToast("Imported "+pluralizeCount(res.copied, "file", "files")+" from USB", ToastKindSuccess)
				}
			}
			// The Games library lists the new games on its next scan; an
			// open one rescans now.
			if res.copied > 0 && gameLib.open {
				refreshGameLibrary()
			}
		})
	}()
}

// ── Devices ──────────────────────────────────────────────────────────────

// usbOtgKindHelp is the "what now" line of a device's sheet.
func usbOtgKindHelp(k usbKind) string {
	switch k {
	case usbKindController:
		return "Most USB pads work in games right away. A pad the emulator does not know may need its buttons mapped in that emulator's menu."
	case usbKindKeyboard:
		return "Type into any text field instead of the on-screen keyboard — handy in the browser, chat and the terminal."
	case usbKindMouse:
		return "Clicks work on JukaHub's screens."
	case usbKindStorage:
		return "Its volumes are listed under Drives on this screen."
	case usbKindAudio:
		return "A USB sound card: a USB-C DAC, headset or speaker. JukaHub moves its sound to a USB DAC while one is plugged in, once the kernel's USB audio driver has given it a sound card."
	case usbKindNetwork:
		return "A wired network adapter. The firmware has to bring it up before apps can use it."
	case usbKindWiFi:
		return "A Wi-Fi chip on USB. Handhelds often have theirs on the internal USB bus, so this may be the built-in one."
	case usbKindBluetooth:
		return "A Bluetooth chip on USB; it may be the handheld's own."
	case usbKindHub:
		return "Whatever is plugged into the hub is listed on its own."
	}
	return "The system sees it; JukaHub has nothing special to do with it."
}

func usbOtgOpenDeviceSheet(config *Config, d usbDevice) {
	var b strings.Builder
	b.WriteString("## What it is\n" + d.Kind.label())
	if d.Fixed {
		b.WriteString(" (built into the handheld)")
	}
	b.WriteString("\n\n## Details\n")
	if d.Manufacturer != "" {
		b.WriteString("• Made by: " + d.Manufacturer + "\n")
	}
	b.WriteString("• ID: " + d.VendorID + ":" + d.ProductID + "\n")
	if sp := usbSpeedLabel(d.Speed); sp != "" {
		b.WriteString("• Speed: " + sp + "\n")
	}
	b.WriteString("• Port: " + d.Name + "\n")
	var drivers []string
	seen := map[string]bool{}
	for _, i := range d.Ifaces {
		if i.Driver != "" && !seen[i.Driver] {
			seen[i.Driver] = true
			drivers = append(drivers, i.Driver)
		}
	}
	if len(drivers) > 0 {
		b.WriteString("• Drivers: " + strings.Join(drivers, ", ") + "\n")
	} else {
		b.WriteString("• Drivers: none bound — the kernel has no driver for it\n")
	}
	b.WriteString("\n## What it is for\n" + usbOtgKindHelp(d.Kind))
	tbxOpenSheet(config, tbxSheetSpec{
		title:    d.title(),
		subtitle: d.Kind.label(),
		icon:     d.Kind.icon(),
		body:     b.String(),
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}
