package main

// spaceuse_ui.go — the "What's using space" screen (Tools → System, Home
// search, and A on the Storage screen), drawn like the other tool screens.
//
// Top to bottom: the drive (Left/Right picks another; A scans it again, or
// stops a scan), the scan's progress while it runs, then the folder on screen
// — its folders and largest files, biggest first, each with its size, its
// share of the folder as a bar, and A to open it — or, on Y, the drive's
// largest files wherever they are. X shows any row in Files; B climbs back up.
// On the handheld the card is scanned as the screen opens; a computer's drive
// can hold millions of files, so there the scan waits for A unless the
// Storage screen asked for it.

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// spaceRowKind is what a row of the list is.
type spaceRowKind int

const (
	spaceRowDir spaceRowKind = iota
	spaceRowFile
	spaceRowOther
	spaceRowTop
)

// spaceRow is one row of the folder or largest-files list.
type spaceRow struct {
	id    string
	kind  spaceRowKind
	name  string
	path  string
	sub   string
	size  int64
	files int
	node  *spaceNode
}

// spaceUI is the screen's state. Main thread only.
var spaceUI struct {
	nav    tbxNav
	vols   []sysToolVolume
	volIdx int
	// want is the drive the opener asked for ("" for JukaHub's own).
	want string
	// result is the scan the folder stack belongs to; stack the folders
	// opened below its root; largest swaps the folder for the drive's
	// largest files.
	result  *spaceResult
	stack   []*spaceNode
	largest bool
	// rows is the list for rowsFor (nil with rowsTop: the largest files).
	rows    []spaceRow
	rowsFor *spaceNode
	rowsTop bool
	rowsRes *spaceResult
}

// spaceListMax is the most rows a folder lists; the rest are summed in its
// "smaller files" row or left to Files.
const spaceListMax = 200

// spaceUseOpen opens the screen on the drive holding path ("" for the drive
// JukaHub lives on). Cross-area contract: the Tools hub, Home search and the
// Storage screen call it.
func spaceUseOpen(config *Config, path string) {
	spaceUI.want = strings.TrimSpace(path)
	toolsPagesOpen(config, spaceUseSceneName)
}

// spaceUseEnter runs once per visit: the drives are read off the main thread,
// then the screen picks one and, where it should, scans it. Coming back from
// Files keeps the folder and the row that were open.
func spaceUseEnter(config *Config) {
	_ = config
	want := spaceUI.want
	spaceUI.want = ""
	go func() {
		vols := sysToolVolumes()
		tbxPost(func() { spaceUseVolumesRead(vols, want) })
	}()
}

// spaceUseVolumesRead takes the drive list and starts the scan the visit
// wants: the card on the handheld, or the drive the Storage screen named.
func spaceUseVolumesRead(vols []sysToolVolume, want string) {
	spaceUI.vols = vols
	running, _, _, res, _ := spaceScanSnapshot()
	spaceUI.volIdx = spaceUsePickVolume(vols, want, res)
	vol, ok := spaceUseVolume()
	if !ok || running || toolsPagesLive != spaceUseSceneName {
		return
	}
	if want == "" && res != nil && res.root != nil && filepath.Clean(res.volume.Path) == filepath.Clean(vol.Path) {
		return
	}
	if IsTrimuiDevice() || want != "" {
		spaceScanStart(vol)
	}
}

// spaceUsePickVolume is the drive to show: the one holding want, else the one
// the last scan was of, else JukaHub's, else the first. Pure.
func spaceUsePickVolume(vols []sysToolVolume, want string, res *spaceResult) int {
	if want != "" {
		best, bestLen := -1, -1
		w := filepath.Clean(want)
		for i, v := range vols {
			p := filepath.Clean(v.Path)
			if strings.EqualFold(p, w) {
				return i
			}
			prefix := strings.TrimRight(p, `/\`) + string(filepath.Separator)
			if strings.HasPrefix(strings.ToLower(w), strings.ToLower(prefix)) && len(p) > bestLen {
				best, bestLen = i, len(p)
			}
		}
		if best >= 0 {
			return best
		}
	}
	if res != nil {
		for i, v := range vols {
			if filepath.Clean(v.Path) == filepath.Clean(res.volume.Path) {
				return i
			}
		}
	}
	for i, v := range vols {
		if v.App {
			return i
		}
	}
	return 0
}

// spaceUseVolume is the drive on screen.
func spaceUseVolume() (sysToolVolume, bool) {
	if spaceUI.volIdx < 0 || spaceUI.volIdx >= len(spaceUI.vols) {
		return sysToolVolume{}, false
	}
	return spaceUI.vols[spaceUI.volIdx], true
}

type spaceUseScene struct{ sceneBase }

func (spaceUseScene) render(r *sdl.Renderer, c *Config) bool {
	spaceUseRender(r, c)
	return true
}

func (spaceUseScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), spaceUseAction)
}

func (spaceUseScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), spaceUseAction)
}

func init() { registerScene(spaceUseScene{sceneBase{name: spaceUseSceneName}}) }

// spaceUseHubTile is the Tools hub's tile (System).
func spaceUseHubTile() toolsHubTile {
	t := toolsHubTile{id: "spaceuse", label: "What's using space", icon: "chart", value: "The biggest folders and files on the card"}
	if !IsTrimuiDevice() {
		t.value = "The biggest folders and files on a drive"
	}
	running, vol, prog, res, _ := spaceScanSnapshot()
	switch {
	case running:
		t.value, t.tone = "Scanning "+vol.Name+"… "+formatSize(prog.bytes), toolsHubToneBusy
	case res != nil && res.root != nil && len(res.root.dirs) > 0:
		big := res.root.dirs[0]
		t.value = res.volume.Name + ": " + big.name + " is the biggest (" + formatSize(big.size) + ")"
	}
	return t
}

// spaceShowing reports whether res is a finished tree of the drive on screen.
func spaceShowing(res *spaceResult) bool {
	vol, ok := spaceUseVolume()
	return ok && res != nil && res.root != nil && filepath.Clean(res.volume.Path) == filepath.Clean(vol.Path)
}

// spaceFolder is the folder on screen (the root when none is open).
func spaceFolder(res *spaceResult) *spaceNode {
	if n := len(spaceUI.stack); n > 0 {
		return spaceUI.stack[n-1]
	}
	if res == nil {
		return nil
	}
	return res.root
}

// spacePct writes a share: "<1%", "37%".
func spacePct(part, whole int64) string {
	if whole <= 0 || part <= 0 {
		return "0%"
	}
	p := float64(part) * 100 / float64(whole)
	if p < 0.5 {
		return "<1%"
	}
	return strconv.Itoa(int(p+0.5)) + "%"
}

// spaceFolderRows lists a folder: its folders and largest files merged by size,
// then one row for its other, smaller files.
func spaceFolderRows(node *spaceNode) []spaceRow {
	rows := make([]spaceRow, 0, len(node.dirs)+len(node.big)+1)
	di, fi := 0, 0
	var shownOwn int64
	for (di < len(node.dirs) || fi < len(node.big)) && len(rows) < spaceListMax {
		if fi >= len(node.big) || (di < len(node.dirs) && node.dirs[di].size >= node.big[fi].size) {
			d := node.dirs[di]
			sub := pluralizeCount(d.files, "file", "files") + " · " + spacePct(d.size, node.size)
			if d.unread {
				sub = "Could not be read"
			}
			rows = append(rows, spaceRow{id: "d:" + d.name, kind: spaceRowDir, name: d.name, path: d.path, sub: sub, size: d.size, files: d.files, node: d})
			di++
			continue
		}
		f := node.big[fi]
		shownOwn += f.size
		rows = append(rows, spaceRow{id: "f:" + f.name, kind: spaceRowFile, name: f.name, path: filepath.Join(node.path, f.name),
			sub: "File · " + spacePct(f.size, node.size), size: f.size, files: 1})
		fi++
	}
	if rest := node.ownN - fi; rest > 0 {
		size := node.own - shownOwn
		rows = append(rows, spaceRow{id: "other", kind: spaceRowOther, name: pluralizeCount(rest, "other file", "other files"),
			path: node.path, sub: "Smaller files in this folder · " + spacePct(size, node.size), size: size, files: rest})
	}
	if di < len(node.dirs) {
		var size int64
		files := 0
		for _, d := range node.dirs[di:] {
			size += d.size
			files += d.files
		}
		rows = append(rows, spaceRow{id: "more", kind: spaceRowOther, name: pluralizeCount(len(node.dirs)-di, "more folder", "more folders"),
			path: node.path, sub: "Smaller folders · open this folder in Files to see them", size: size, files: files})
	}
	return rows
}

// spaceTopRows lists the drive's largest files.
func spaceTopRows(res *spaceResult) []spaceRow {
	rows := make([]spaceRow, 0, len(res.top))
	for i, f := range res.top {
		dir, err := filepath.Rel(res.root.path, filepath.Dir(f.path))
		if err != nil || dir == "." {
			dir = res.volume.Name
		} else {
			dir = res.volume.Name + " / " + strings.ReplaceAll(filepath.ToSlash(dir), "/", " / ")
		}
		rows = append(rows, spaceRow{id: "t:" + strconv.Itoa(i), kind: spaceRowTop, name: filepath.Base(f.path), path: f.path,
			sub: dir, size: f.size, files: 1})
	}
	return rows
}

// spaceRowsNow is the list on screen, rebuilt only when the folder, the view
// or the scan changes.
func spaceRowsNow(res *spaceResult) []spaceRow {
	if spaceUI.largest {
		if !spaceUI.rowsTop || spaceUI.rowsRes != res {
			spaceUI.rows = spaceTopRows(res)
			spaceUI.rowsTop, spaceUI.rowsFor, spaceUI.rowsRes = true, nil, res
		}
		return spaceUI.rows
	}
	node := spaceFolder(res)
	if spaceUI.rowsTop || spaceUI.rowsFor != node || spaceUI.rowsRes != res {
		spaceUI.rows = spaceFolderRows(node)
		spaceUI.rowsTop, spaceUI.rowsFor, spaceUI.rowsRes = false, node, res
	}
	return spaceUI.rows
}

// spaceRowIcon is a row's glyph: a folder, or the kind of file by its name.
func spaceRowIcon(r spaceRow) string {
	switch r.kind {
	case spaceRowDir:
		return "folder"
	case spaceRowOther:
		return "files"
	}
	ext := strings.ToLower(filepath.Ext(r.name))
	switch {
	case isImageExtension(ext):
		return "image"
	case isAudioFile(r.name):
		return "music"
	case hasMediaExtension(r.name):
		return "video"
	}
	switch ext {
	case ".zip", ".7z", ".rar", ".gz", ".xz", ".tar", ".zst":
		return "box"
	case ".iso", ".chd", ".cso", ".bin", ".cue", ".pbp", ".rvz", ".gcz", ".wbfs":
		return "disc"
	}
	return "file"
}

// spaceCrumb is the folder on screen as a path from the drive: "SD card / Roms".
func spaceCrumb(res *spaceResult) string {
	parts := []string{res.volume.Name}
	for _, n := range spaceUI.stack {
		parts = append(parts, n.name)
	}
	return strings.Join(parts, " / ")
}

// spaceAge says how long ago a scan finished.
func spaceAge(at time.Time) string {
	d := time.Since(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	}
	return at.Format("2 Jan")
}

func spaceUseRender(renderer *sdl.Renderer, config *Config) {
	running, scanVol, prog, res, _ := spaceScanSnapshot()
	if res != spaceUI.result {
		// A new scan: the folders opened in the old tree are gone.
		spaceUI.result = res
		spaceUI.stack = spaceUI.stack[:0]
		spaceUI.largest = false
		spaceUI.nav.focus = ""
		spaceUI.nav.scroll = 0
	}
	vol, hasVol := spaceUseVolume()
	showing := spaceShowing(res)
	sub := "The folders and files that fill a drive"
	if showing {
		sub = spaceCrumb(res)
		if spaceUI.largest {
			sub = res.volume.Name + " / the largest files"
		}
	}
	status, statusColor := "", ColorTextMuted()
	switch {
	case running:
		status, statusColor = "Scanning…", ColorTextAccent()
	case showing && res.stopped:
		status, statusColor = "Stopped early", ToastWarnInk()
	case showing:
		status = "Scanned " + spaceAge(res.finished)
	}
	header := tbxHeader("What's using space", sub, "chart", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &spaceUI.nav
	nav.begin(body.Y, body.H)

	if !hasVol {
		msg := "Looking for drives…"
		if spaceUI.vols != nil {
			msg = "This system did not report its drives."
		}
		tbxDrawEmpty(renderer, config, body, "disk", "What's using space", msg)
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
	drawRow := func(id string, spec tbxRowSpec) sdl.Rect {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		if screen.Y+rowH >= body.Y && screen.Y <= body.Y+body.H {
			tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		}
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
		return screen
	}

	// The drive.
	driveTitle := vol.Name
	if len(spaceUI.vols) > 1 {
		driveTitle += "  (" + strconv.Itoa(spaceUI.volIdx+1) + " of " + strconv.Itoa(len(spaceUI.vols)) + ")"
	}
	driveSub := formatBytes(vol.used()) + " used of " + formatBytes(vol.Total) + " · " + formatBytes(vol.Free) + " free"
	pill, tone := "Scan", tbxToneAccent
	switch {
	case running && filepath.Clean(scanVol.Path) == filepath.Clean(vol.Path):
		pill, tone = "Stop", tbxToneWarn
	case running:
		pill, tone = "Busy", tbxToneNeutral
	case showing:
		pill, tone = "Scan again", tbxToneNeutral
	}
	drawRow("drive", tbxRowSpec{icon: "disk", title: driveTitle, subtitle: driveSub, pill: pill, tone: tone})

	if running {
		line := pluralizeCount(prog.files, "file", "files") + " · " + formatSize(prog.bytes)
		if prog.at != "" {
			line += " · " + prog.at
		}
		title := "Scanning " + scanVol.Name + "…"
		drawRow("", tbxRowSpec{icon: "chart", title: title, subtitle: line, pill: "Working", tone: tbxToneAccent})
	}

	switch {
	case showing:
		y += 6
		rows := spaceRowsNow(res)
		folder := spaceFolder(res)
		heading := folder.name + " · " + formatSize(folder.size) + " in " + pluralizeCount(folder.files, "file", "files")
		icon := "folder"
		whole := folder.size
		if spaceUI.largest {
			heading, icon = "Largest files on "+res.volume.Name, "files"
			whole = res.root.size
		}
		tbxDrawSection(renderer, config, heading, icon, x, top+y, w)
		y += tbxSectionHeight
		if !spaceUI.largest && len(spaceUI.stack) == 0 {
			// What the drive counts as used beyond its files.
			if extra := spaceUnaccounted(res); extra > 64<<20 && extra*50 > int64(res.volume.used()) {
				title, line := "Other used space", "Not in any folder this scan could read"
				if res.mountRoot {
					line = "The file system's own records, cluster slack and unreadable folders"
				} else {
					title, line = "Outside this folder", "Used elsewhere on the same drive"
				}
				drawRow("", tbxRowSpec{icon: "info", title: title, subtitle: line, note: formatSize(extra), dim: true})
			}
		}
		if res.stopped && !running {
			drawRow("", tbxRowSpec{icon: "warning", title: "The scan was stopped", subtitle: "Sizes count only what was read before it stopped", pill: "Partial", tone: tbxToneWarn, dim: true})
		}
		if len(spaceUI.stack) > 0 && !spaceUI.largest {
			parent := res.volume.Name
			if n := len(spaceUI.stack); n > 1 {
				parent = spaceUI.stack[n-2].name
			}
			drawRow("up", tbxRowSpec{icon: "folder-up", title: "Up to " + parent, subtitle: "B does the same"})
		}
		if len(rows) == 0 {
			drawRow("", tbxRowSpec{icon: "folder-open", title: "Nothing here", subtitle: "This folder holds no files", dim: true})
		}
		for _, r := range rows {
			screen := drawRow(r.id, tbxRowSpec{icon: spaceRowIcon(r), title: r.name, subtitle: r.sub, note: formatSize(r.size)})
			if screen.Y+rowH >= body.Y && screen.Y <= body.Y+body.H && whole > 0 {
				spaceDrawShare(renderer, screen, float64(r.size)/float64(whole))
			}
		}
	case !running:
		y += 6
		hint := "Press A on the drive to see which folders and files fill it."
		if res != nil && res.err != "" && filepath.Clean(res.volume.Path) == filepath.Clean(vol.Path) {
			hint = res.err + " — press A on the drive to try again."
		}
		tbxDrawEmpty(renderer, config, sdl.Rect{X: x, Y: top + y, W: w, H: 180}, "chart", "Not scanned yet", hint)
		y += 180
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// spaceDrawShare draws a row's share of its folder as a thin bar under its
// text, from the text's left edge (where tbxDrawRow puts it) to the row's right.
func spaceDrawShare(renderer *sdl.Renderer, row sdl.Rect, frac float64) {
	badge := row.H - 24
	if badge > 44 {
		badge = 44
	}
	if badge < 28 {
		badge = 28
	}
	x := row.X + 14 + badge + 14
	w := row.X + row.W - 16 - x
	if w < 40 {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	UIProgressBar(renderer, sdl.Rect{X: x, Y: row.Y + row.H - 9, W: w, H: 3}, frac, glassA(16), WithAlpha(glassAccent(), 200))
}

// ── Input ────────────────────────────────────────────────────────────────

// spaceRowByID finds a row of the list on screen.
func spaceRowByID(id string) (spaceRow, bool) {
	for _, r := range spaceUI.rows {
		if r.id == id {
			return r, true
		}
	}
	return spaceRow{}, false
}

func spaceUseAction(config *Config, act tbxAction) bool {
	nav := &spaceUI.nav
	_, _, _, res, _ := spaceScanSnapshot()
	switch act {
	case tbxActConfirm:
		spaceUseActivate(config, nav.focus)
		return true
	case tbxActX:
		spaceUseShowInFiles(config, nav.focus)
		return true
	case tbxActY:
		if !spaceShowing(res) {
			return true
		}
		PlayActivateSound()
		spaceUI.largest = !spaceUI.largest
		nav.focus, nav.scroll = "", 0
		return true
	case tbxActLeft, tbxActRight:
		if nav.focus == "drive" && len(spaceUI.vols) > 1 {
			step := 1
			if act == tbxActLeft {
				step = len(spaceUI.vols) - 1
			}
			spaceUI.volIdx = (spaceUI.volIdx + step) % len(spaceUI.vols)
			spaceUI.stack = spaceUI.stack[:0]
			spaceUI.largest = false
			PlayNavSound()
			return true
		}
	case tbxActBack:
		return spaceUseBack()
	}
	return tbxNavAction(nav, act)
}

// spaceUseBack is B: the largest files back to the folder, a folder up to its
// parent; at the drive's top it leaves the screen (false).
func spaceUseBack() bool {
	if spaceUI.largest {
		spaceUI.largest = false
		spaceUI.nav.focus, spaceUI.nav.scroll = "", 0
		PlayBackSound()
		return true
	}
	n := len(spaceUI.stack)
	if n == 0 {
		return false
	}
	left := spaceUI.stack[n-1]
	spaceUI.stack = spaceUI.stack[:n-1]
	spaceUI.nav.scroll = 0
	// The folder that was open keeps the focus in its parent's list.
	spaceUI.nav.focus = "d:" + left.name
	PlayBackSound()
	return true
}

func spaceUseClick(mx, my int32, config *Config) bool {
	it, ok := spaceUI.nav.hit(mx, my)
	if !ok {
		return false
	}
	spaceUI.nav.focus = it.id
	spaceUseActivate(config, it.id)
	return true
}

func spaceUseHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	focus := spaceUI.nav.focus
	_, _, _, res, _ := spaceScanSnapshot()
	showing := spaceShowing(res)
	switch {
	case focus == "drive":
		hints = append(hints, FooterHint{Button: "A", Label: "Scan"})
		if len(spaceUI.vols) > 1 {
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Drive"})
		}
	case strings.HasPrefix(focus, "d:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Open"}, FooterHint{Button: "X", Label: "Show in Files"})
	case focus == "up":
		hints = append(hints, FooterHint{Button: "A", Label: "Up"})
	case focus != "":
		hints = append(hints, FooterHint{Button: "A", Label: "Show in Files"})
	}
	if showing {
		label := "Largest files"
		if spaceUI.largest {
			label = "Folders"
		}
		hints = append(hints, FooterHint{Button: "Y", Label: label})
	}
	back := "Back"
	if spaceUI.largest || len(spaceUI.stack) > 0 {
		back = "Up"
	}
	return append(hints, FooterHint{Button: "B", Label: back})
}

func spaceUseActivate(config *Config, id string) {
	switch {
	case id == "drive":
		spaceUseDrivePress()
	case id == "up":
		spaceUseBack()
	case strings.HasPrefix(id, "d:"):
		r, ok := spaceRowByID(id)
		if !ok || r.node == nil {
			return
		}
		PlayActivateSound()
		spaceUI.stack = append(spaceUI.stack, r.node)
		spaceUI.nav.focus, spaceUI.nav.scroll = "", 0
	case id != "":
		spaceUseShowInFiles(config, id)
	}
}

// spaceUseDrivePress is A on the drive: stop the scan of it, or scan it.
func spaceUseDrivePress() {
	vol, ok := spaceUseVolume()
	if !ok {
		return
	}
	running, scanVol, _, _, _ := spaceScanSnapshot()
	if running {
		if filepath.Clean(scanVol.Path) == filepath.Clean(vol.Path) {
			PlayActivateSound()
			spaceScanStop()
			return
		}
		ShowToast("Already scanning "+scanVol.Name+" — wait for it, or stop it there", ToastKindInfo)
		return
	}
	PlayActivateSound()
	spaceScanStart(vol)
}

// spaceUseShowInFiles is X (and A on a file): the row in Files.
func spaceUseShowInFiles(config *Config, id string) {
	if id == "drive" {
		if vol, ok := spaceUseVolume(); ok {
			toolsShowInFiles(config, vol.Path, true)
		}
		return
	}
	if id == "up" {
		if n := len(spaceUI.stack); n > 0 && spaceUI.stack[n-1].parent != nil {
			toolsShowInFiles(config, spaceUI.stack[n-1].parent.path, true)
		}
		return
	}
	r, ok := spaceRowByID(id)
	if !ok {
		return
	}
	switch r.kind {
	case spaceRowDir, spaceRowOther:
		toolsShowInFiles(config, r.path, true)
	default:
		toolsShowInFiles(config, r.path, false)
	}
}
