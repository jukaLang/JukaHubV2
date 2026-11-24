package main

// filecheck_ui.go — the Verify a file screen (Tools → System, Home search,
// and Files → Y → Verify checksum), drawn like the other tool screens.
//
// Top to bottom: the file (A picks another), the read's progress, the four
// checksums (A copies one), and the comparison: paste or type the checksum a
// download page lists, and the screen says whether the file matches it —
// whichever of the four it is, told by its length. Opened from the Tools hub
// with nothing chosen yet, the screen starts in its file picker: the places
// Files starts from, then folders, then a file.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// fileCheckUI is the screen's state. Main thread only.
var fileCheckUI struct {
	nav tbxNav
	// pending is a file handed over by the opener (Files), read on entry.
	pending string
	// The picker replaces the page while a file is chosen. pickDir "" is
	// the list of places (pickLocs).
	picking  bool
	pickDir  string
	pickLocs []filesLocation
	pickRows []FileEntry
	pickErr  string
	pickBusy bool
	pickGen  uint64
	pickNav  tbxNav
	// lastDir is where the picker opens next time.
	lastDir string
}

// fileCheckOpen opens the screen on path, or on its picker ("" — cross-area
// contract: the Tools hub, Home search and Files call it).
func fileCheckOpen(config *Config, path string) {
	fileCheckUI.pending = strings.TrimSpace(path)
	toolsPagesOpen(config, fileCheckSceneName)
}

// fileCheckEnter runs once per visit: a file from Files is read at once; with
// nothing read yet, the picker comes first.
func fileCheckEnter(config *Config) {
	fileCheckUI.nav.scroll = 0
	if p := fileCheckUI.pending; p != "" {
		fileCheckUI.pending = ""
		fileCheckUI.picking = false
		fileCheckChoose(p)
		return
	}
	if fileCheckSnapshot().path == "" {
		fileCheckPickOpen(config, fileCheckUI.lastDir)
	}
}

// fileCheckLeave closes the picker with the screen.
func fileCheckLeave() {
	fileCheckUI.picking = false
	fileCheckUI.pickGen++
}

// fileCheckActiveNav is the focus list on screen: the picker's or the page's.
func fileCheckActiveNav() *tbxNav {
	if fileCheckUI.picking {
		return &fileCheckUI.pickNav
	}
	return &fileCheckUI.nav
}

type fileCheckScene struct{ sceneBase }

func (fileCheckScene) render(r *sdl.Renderer, c *Config) bool {
	if fileCheckUI.picking {
		fileCheckRenderPicker(r, c)
	} else {
		fileCheckRender(r, c)
	}
	return true
}

func (fileCheckScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), fileCheckAction)
}

func (fileCheckScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), fileCheckAction)
}

func init() { registerScene(fileCheckScene{sceneBase{name: fileCheckSceneName}}) }

// fileCheckHubTile is the Tools hub's tile (System).
func fileCheckHubTile() toolsHubTile {
	t := toolsHubTile{id: "filecheck", label: "Verify a file", icon: "check", value: "SHA-256, MD5 and CRC32 of any file"}
	run := fileCheckSnapshot()
	name := filepath.Base(run.path)
	switch {
	case run.running:
		t.value, t.tone = "Reading "+name+"… "+spacePct(run.done, run.size), toolsHubToneBusy
	case run.path != "" && run.err == "" && !run.stopped:
		t.value = "Last: " + name
		if _, match, known := fileCheckCompare(run.sums, run.expect); known {
			if match {
				t.value, t.tone = name+" matches its checksum", toolsHubToneGood
			} else {
				t.value, t.tone = name+" does not match", toolsHubToneWarn
			}
		}
	}
	return t
}

// feActChecksum is the Files sheet's "Verify checksum" row, numbered past the
// other add-on rows (shelf 1000, ZIP 1010, Open with 1100, two panes 1200).
const feActChecksum feActionKind = 1300

// fileCheckFilesAction is the Files sheet's row: one file under the cursor or
// marked, never a folder.
func fileCheckFilesAction(targets []FileEntry) (feAction, bool) {
	if len(targets) != 1 || targets[0].IsDir || targets[0].Name == ".." {
		return feAction{}, false
	}
	return feAction{Kind: feActChecksum, Label: "Verify checksum", Detail: "SHA-256, SHA-1, MD5 and CRC32", Enabled: true}, true
}

// fileCheckFilesActivate opens the screen on the file the row was for.
func fileCheckFilesActivate(config *Config, targets []FileEntry) {
	if len(targets) != 1 || targets[0].IsDir {
		ShowToast("Verify one file at a time", ToastKindInfo)
		return
	}
	feSheetForget()
	PlayActivateSound()
	fileCheckOpen(config, targets[0].Path)
}

// fileCheckChoose reads path and shows the page.
func fileCheckChoose(path string) {
	if err := fileCheckStart(path); err != nil {
		ShowToast("Cannot read that file: "+filesFriendlyError(err), ToastKindWarning)
		return
	}
	fileCheckUI.lastDir = filepath.Dir(path)
	fileCheckUI.picking = false
	fileCheckUI.pickGen++
	fileCheckUI.nav.focus, fileCheckUI.nav.scroll = "", 0
}

// fileCheckEta says how long a read has left: "about 40 s left".
func fileCheckEta(run fileCheckRun) string {
	elapsed := time.Since(run.started)
	if run.done <= 0 || elapsed < 2*time.Second || run.size <= run.done {
		return ""
	}
	rate := float64(run.done) / elapsed.Seconds()
	left := time.Duration(float64(run.size-run.done)/rate) * time.Second
	switch {
	case left < 5*time.Second:
		return "a few seconds left"
	case left < time.Minute:
		return "about " + strconv.Itoa(int(left.Seconds())) + " s left"
	}
	return "about " + strconv.Itoa(int(left.Minutes()+0.5)) + " min left"
}

// ── The page ─────────────────────────────────────────────────────────────

func fileCheckRender(renderer *sdl.Renderer, config *Config) {
	run := fileCheckSnapshot()
	kind, match, known := fileCheckCompare(run.sums, run.expect)
	status, statusColor := "", ColorTextMuted()
	switch {
	case run.running:
		status, statusColor = "Reading… "+spacePct(run.done, run.size), ColorTextAccent()
	case known && match:
		status, statusColor = "Matches", ToastSuccessInk()
	case known:
		status, statusColor = "Does not match", ToastWarnInk()
	case run.err != "":
		status, statusColor = "Could not read", ToastWarnInk()
	case run.sums[fileSumSHA256] != "":
		status = "Done"
	}
	header := tbxHeader("Verify a file", "Its checksums, and whether it matches the one you were given", "check", status, statusColor)
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &fileCheckUI.nav
	nav.begin(body.Y, body.H)
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

	// The file.
	tbxDrawSection(renderer, config, "File", "files", x, top+y, w)
	y += tbxSectionHeight
	if run.path == "" {
		drawRow("file", tbxRowSpec{icon: "file", title: "Choose a file", subtitle: "A download, a game, a disc image…", pill: "Choose", tone: tbxToneAccent})
	} else {
		sub := formatSize(run.size) + " · " + filepath.Dir(run.path)
		drawRow("file", tbxRowSpec{icon: spaceRowIcon(spaceRow{kind: spaceRowFile, name: run.path}), title: filepath.Base(run.path), subtitle: sub, pill: "Change", tone: tbxToneNeutral})
	}
	switch {
	case run.running:
		line := formatSize(run.done) + " of " + formatSize(run.size)
		if elapsed := time.Since(run.started).Seconds(); elapsed > 1 && run.done > 0 {
			line += " · " + formatSize(int64(float64(run.done)/elapsed)) + "/s"
		}
		if eta := fileCheckEta(run); eta != "" {
			line += " · " + eta
		}
		drawRow("stop", tbxRowSpec{icon: "file", title: "Reading the file… " + spacePct(run.done, run.size), subtitle: line, pill: "Stop", tone: tbxToneWarn})
	case run.stopped:
		drawRow("again", tbxRowSpec{icon: "warning", title: "Stopped before the end", subtitle: "Read the whole file to get its checksums", pill: "Read again", tone: tbxToneWarn})
	case run.err != "":
		drawRow("again", tbxRowSpec{icon: "warning", title: "Could not read the file", subtitle: run.err, pill: "Try again", tone: tbxToneWarn})
	}
	y += 6

	// The four sums.
	tbxDrawSection(renderer, config, "Checksums", "key", x, top+y, w)
	y += tbxSectionHeight
	for k := fileSumKind(0); k < fileSumCount; k++ {
		sum := run.sums[k]
		spec := tbxRowSpec{icon: "key", title: fileSumName(k), subtitle: sum}
		switch {
		case sum != "":
			spec.pill, spec.tone = "Copy", tbxToneNeutral
			if known && kind == k {
				spec.pill, spec.tone = "Different", tbxToneBad
				if match {
					spec.pill, spec.tone = "Match", tbxToneGood
				}
			}
		case run.running:
			spec.subtitle, spec.dim = "Working…", true
		default:
			spec.subtitle, spec.dim = "Not read yet", true
		}
		drawRow("sum:"+strconv.Itoa(int(k)), spec)
	}
	if run.sums[fileSumSHA256] != "" {
		drawRow("copyall", tbxRowSpec{icon: "copy", title: "Copy all four", subtitle: "With the file's name and size, to paste in a message or a forum post", pill: "Copy", tone: tbxToneNeutral})
	}
	y += 6

	// The comparison.
	tbxDrawSection(renderer, config, "Compare", "check", x, top+y, w)
	y += tbxSectionHeight
	cmp := tbxRowSpec{icon: "paste", title: "Compare with a checksum", subtitle: "Paste or type the checksum the download page lists", pill: "Paste", tone: tbxToneAccent}
	if run.expect != "" {
		name := fileSumName(fileSumKind(0))
		for k := fileSumKind(0); k < fileSumCount; k++ {
			if len(run.expect) == fileSumHexLen(k) {
				name = fileSumName(k)
			}
		}
		switch {
		case known && match:
			cmp.icon, cmp.title = "check", "The file matches"
			cmp.subtitle = "Its " + name + " is the one you gave: the file is intact"
			cmp.pill, cmp.tone = "Match", tbxToneGood
		case known:
			cmp.icon, cmp.title = "warning", "The file does not match"
			cmp.subtitle = "Its " + name + " differs from the one you gave: a different or damaged copy"
			cmp.pill, cmp.tone = "Different", tbxToneBad
		default:
			cmp.subtitle = "Your " + name + " is checked when the read finishes"
			cmp.pill, cmp.tone = "Waiting", tbxToneNeutral
		}
	}
	drawRow("compare", cmp)

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── The picker ───────────────────────────────────────────────────────────

// fileCheckPickOpen shows the picker at dir, or at the list of places.
func fileCheckPickOpen(config *Config, dir string) {
	fileCheckUI.picking = true
	if dir == "" || !dirExists(dir) {
		fileCheckPickPlaces(config)
		return
	}
	fileCheckPickRead(config, dir, "")
}

// fileCheckPickPlaces lists the places Files starts from.
func fileCheckPickPlaces(config *Config) {
	fileCheckUI.pickGen++
	fileCheckUI.pickDir = ""
	fileCheckUI.pickLocs = filesBuildLocations(config)
	fileCheckUI.pickRows = nil
	fileCheckUI.pickErr = ""
	fileCheckUI.pickBusy = false
	fileCheckUI.pickNav.focus, fileCheckUI.pickNav.scroll = "", 0
}

// fileCheckPickRead lists dir off the main thread, focusing focus when it
// lands (the folder just climbed out of).
func fileCheckPickRead(config *Config, dir, focus string) {
	fileCheckUI.pickGen++
	gen := fileCheckUI.pickGen
	fileCheckUI.pickDir = dir
	fileCheckUI.pickRows = nil
	fileCheckUI.pickErr = ""
	fileCheckUI.pickBusy = true
	fileCheckUI.pickNav.focus, fileCheckUI.pickNav.scroll = "", 0
	showHidden := filesShowHidden(config)
	go func() {
		list, err := fileCheckListDir(dir, showHidden)
		tbxPost(func() {
			if gen != fileCheckUI.pickGen {
				return
			}
			fileCheckUI.pickBusy = false
			fileCheckUI.pickRows = list
			if err != nil {
				fileCheckUI.pickErr = filesFriendlyError(err)
			}
			fileCheckUI.pickNav.focus = focus
		})
	}()
}

// fileCheckListDir lists dir for the picker: folders first, then files, by
// name, without hidden entries unless Files shows them.
func fileCheckListDir(dir string, showHidden bool) ([]FileEntry, error) {
	entries, err := os.ReadDir(dir)
	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		if !showHidden && filesEntryHidden(e) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if info, statErr := os.Stat(full); statErr == nil && info.IsDir() {
				isDir = true
			}
		}
		entry := FileEntry{Name: e.Name(), Path: full, IsDir: isDir}
		if !isDir {
			if info, infoErr := e.Info(); infoErr == nil {
				entry.Size = info.Size()
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if err != nil && len(entries) > 0 {
		err = nil
	}
	return out, err
}

// fileCheckPickIsPlace reports whether dir is one of the places, where Up goes
// back to the list of places.
func fileCheckPickIsPlace(dir string) bool {
	for _, loc := range fileCheckUI.pickLocs {
		if filesSamePath(loc.Path, dir) {
			return true
		}
	}
	return false
}

func fileCheckRenderPicker(renderer *sdl.Renderer, config *Config) {
	sub := "Where the file is"
	if fileCheckUI.pickDir != "" {
		sub = fileCheckUI.pickDir
	}
	header := tbxHeader("Choose a file", sub, "files", "", ColorTextMuted())
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, header)
	body := frame.bodyRect()
	nav := &fileCheckUI.pickNav
	nav.begin(body.Y, body.H)
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
		if screen.Y+rowH >= body.Y && screen.Y <= body.Y+body.H {
			tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		}
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 8
	}

	if fileCheckUI.pickDir == "" {
		if len(fileCheckUI.pickLocs) == 0 {
			drawRow("", tbxRowSpec{icon: "warning", title: "No places to start from", subtitle: "Files lists none on this device", dim: true})
		}
		for i, loc := range fileCheckUI.pickLocs {
			detail := loc.Detail
			if detail == "" {
				detail = loc.Path
			}
			drawRow("l:"+strconv.Itoa(i), tbxRowSpec{icon: "folder", title: loc.Label, subtitle: detail})
		}
	} else {
		up := "All places"
		if !fileCheckPickIsPlace(fileCheckUI.pickDir) {
			if parent := filepath.Dir(fileCheckUI.pickDir); parent != fileCheckUI.pickDir {
				up = "Up to " + filepath.Base(parent)
			}
		}
		drawRow("up", tbxRowSpec{icon: "folder-up", title: up, subtitle: "B does the same"})
		switch {
		case fileCheckUI.pickBusy:
			drawRow("", tbxRowSpec{icon: "folder-open", title: "Reading the folder…", dim: true})
		case fileCheckUI.pickErr != "":
			drawRow("", tbxRowSpec{icon: "warning", title: "Cannot open this folder", subtitle: fileCheckUI.pickErr, dim: true})
		case len(fileCheckUI.pickRows) == 0:
			drawRow("", tbxRowSpec{icon: "folder-open", title: "Nothing here", subtitle: "This folder is empty", dim: true})
		}
		for _, e := range fileCheckUI.pickRows {
			if e.IsDir {
				drawRow("p:"+e.Name, tbxRowSpec{icon: "folder", title: e.Name})
				continue
			}
			drawRow("e:"+e.Name, tbxRowSpec{icon: spaceRowIcon(spaceRow{kind: spaceRowFile, name: e.Name}), title: e.Name, note: formatSize(e.Size)})
		}
	}

	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// fileCheckPickUp is B in the picker: up a folder, to the places, then out of
// the picker (to the page when a file was read before, else off the screen).
func fileCheckPickUp(config *Config) bool {
	dir := fileCheckUI.pickDir
	switch {
	case dir == "":
		fileCheckUI.picking = false
		fileCheckUI.pickGen++
		if fileCheckSnapshot().path == "" {
			return false
		}
		PlayBackSound()
		return true
	case fileCheckPickIsPlace(dir) || filepath.Dir(dir) == dir:
		PlayBackSound()
		fileCheckPickPlaces(config)
		for i, loc := range fileCheckUI.pickLocs {
			if filesSamePath(loc.Path, dir) {
				fileCheckUI.pickNav.focus = "l:" + strconv.Itoa(i)
			}
		}
		return true
	}
	PlayBackSound()
	fileCheckPickRead(config, filepath.Dir(dir), "p:"+filepath.Base(dir))
	return true
}

// fileCheckPickActivate is A on a picker row.
func fileCheckPickActivate(config *Config, id string) {
	switch {
	case id == "up":
		fileCheckPickUp(config)
	case strings.HasPrefix(id, "l:"):
		i, err := strconv.Atoi(strings.TrimPrefix(id, "l:"))
		if err != nil || i < 0 || i >= len(fileCheckUI.pickLocs) {
			return
		}
		PlayActivateSound()
		fileCheckPickRead(config, fileCheckUI.pickLocs[i].Path, "")
	case strings.HasPrefix(id, "p:"):
		PlayActivateSound()
		fileCheckPickRead(config, filepath.Join(fileCheckUI.pickDir, strings.TrimPrefix(id, "p:")), "")
	case strings.HasPrefix(id, "e:"):
		PlayActivateSound()
		fileCheckChoose(filepath.Join(fileCheckUI.pickDir, strings.TrimPrefix(id, "e:")))
	}
}

// ── Input ────────────────────────────────────────────────────────────────

func fileCheckAction(config *Config, act tbxAction) bool {
	if fileCheckUI.picking {
		nav := &fileCheckUI.pickNav
		switch act {
		case tbxActConfirm:
			fileCheckPickActivate(config, nav.focus)
			return true
		case tbxActBack:
			return fileCheckPickUp(config)
		case tbxActX, tbxActY:
			return true
		}
		return tbxNavAction(nav, act)
	}
	nav := &fileCheckUI.nav
	switch act {
	case tbxActConfirm:
		fileCheckActivate(config, nav.focus)
		return true
	case tbxActX:
		fileCheckActivate(config, "sum:0")
		return true
	case tbxActY:
		fileCheckActivate(config, "file")
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func fileCheckClick(mx, my int32, config *Config) bool {
	nav := fileCheckActiveNav()
	it, ok := nav.hit(mx, my)
	if !ok {
		return false
	}
	nav.focus = it.id
	if fileCheckUI.picking {
		fileCheckPickActivate(config, it.id)
	} else {
		fileCheckActivate(config, it.id)
	}
	return true
}

func fileCheckHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if fileCheckUI.picking {
		focus := fileCheckUI.pickNav.focus
		switch {
		case strings.HasPrefix(focus, "e:"):
			hints = append(hints, FooterHint{Button: "A", Label: "Choose"})
		case focus != "":
			hints = append(hints, FooterHint{Button: "A", Label: "Open"})
		}
		back := "Up"
		if fileCheckUI.pickDir == "" {
			back = "Cancel"
		}
		return append(hints, FooterHint{Button: "B", Label: back})
	}
	switch focus := fileCheckUI.nav.focus; {
	case strings.HasPrefix(focus, "sum:"), focus == "copyall":
		hints = append(hints, FooterHint{Button: "A", Label: "Copy"})
	case focus != "":
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	return append(hints,
		FooterHint{Button: "X", Label: "Copy SHA-256"},
		FooterHint{Button: "Y", Label: "Choose file"},
		FooterHint{Button: "B", Label: "Back"})
}

func fileCheckActivate(config *Config, id string) {
	run := fileCheckSnapshot()
	switch {
	case id == "file":
		PlayActivateSound()
		dir := fileCheckUI.lastDir
		if run.path != "" {
			dir = filepath.Dir(run.path)
		}
		fileCheckPickOpen(config, dir)
	case id == "stop":
		PlayActivateSound()
		fileCheckStop()
	case id == "again":
		if run.path == "" {
			return
		}
		PlayActivateSound()
		fileCheckChoose(run.path)
	case strings.HasPrefix(id, "sum:"):
		k, err := strconv.Atoi(strings.TrimPrefix(id, "sum:"))
		if err != nil || k < 0 || k >= int(fileSumCount) {
			return
		}
		sum := run.sums[k]
		if sum == "" {
			ShowToast("Not read yet — wait for the read to finish", ToastKindInfo)
			return
		}
		PlayActivateSound()
		fileCheckCopy(sum, fileSumName(fileSumKind(k))+" copied")
	case id == "copyall":
		PlayActivateSound()
		fileCheckCopy(fileCheckReport(run), "All four copied")
	case id == "compare":
		fileCheckCompareSheet(config, run)
	}
}

// fileCheckCopy puts text on the clipboard with a toast.
func fileCheckCopy(text, done string) {
	if err := copyTextToClipboard(text, "Verify a file"); err != nil {
		ShowToast("Could not copy: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast(done, ToastKindSuccess)
}

// fileCheckCompareSheet asks for the checksum: from the clipboard, typed, or
// cleared.
func fileCheckCompareSheet(config *Config, run fileCheckRun) {
	PlayActivateSound()
	buttons := make([]tbxSheetButton, 0, 4)
	if run.expect != "" {
		buttons = append(buttons, tbxSheetButton{label: "Clear", run: func() {
			fileCheckClearExpected()
			ShowToast("Comparison cleared", ToastKindInfo)
		}})
	}
	buttons = append(buttons,
		tbxSheetButton{label: "Type", run: func() {
			osPromptOpen("Checksum", "Paste or type the checksum", "Compare", run.expect, 200, fileCheckTyped)
		}},
		tbxSheetButton{label: "Paste", primary: true, run: fileCheckPasted},
	)
	body := "Download pages list a file's checksum so you can tell a good copy from a damaged or different one. " +
		"Copy it there (on a phone, the Phone companion can send text), then paste it here.\n\n" +
		"SHA-256, SHA-1, MD5 and CRC32 are told apart by their length; a line like \"<checksum>  name.zip\" works too."
	if run.expect != "" {
		body += "\n\n## Comparing with\n" + run.expect
	}
	tbxOpenSheet(config, tbxSheetSpec{title: "Compare with a checksum", subtitle: filepath.Base(run.path), icon: "paste", body: body,
		buttons: buttons, focus: len(buttons) - 1})
}

// fileCheckPasted takes the checksum off the clipboard.
func fileCheckPasted() {
	text, err := powerClipboardText()
	if err != nil || strings.TrimSpace(text) == "" {
		ShowToast("The clipboard is empty — copy the checksum first", ToastKindWarning)
		return
	}
	fileCheckTyped(text)
}

// fileCheckTyped takes a typed or pasted checksum.
func fileCheckTyped(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if !fileCheckSetExpected(text) {
		ShowToast("That is not a SHA-256, SHA-1, MD5 or CRC32 checksum", ToastKindWarning)
		return
	}
	run := fileCheckSnapshot()
	kind, match, known := fileCheckCompare(run.sums, run.expect)
	switch {
	case !known:
		ShowToast("Got it — checked when the read finishes", ToastKindInfo)
	case match:
		ShowToast("The file matches its "+fileSumName(kind), ToastKindSuccess)
	default:
		ShowToast("The file does NOT match its "+fileSumName(kind), ToastKindWarning)
	}
	fileCheckUI.nav.setFocus("compare")
}
