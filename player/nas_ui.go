package main

// nas_ui.go — Settings → Network → Network drives: the page that adds, tests
// and edits the drives games come from (nas.go), drawn like the other tool
// screens (hotspot_ui.go, romm_cloud_ui.go).
//
// Four views on one scene:
//   - the list: each drive with whether it answers and how many games it
//     has, Add a network drive, Find shared folders on this network (mDNS
//     _smb._tcp, the browse moonlight_discovery.go does for PCs) and Refresh;
//   - a drive: its kind, address, shared folder, user, password, games
//     folder, Test connection, Consoles, Save and Remove. Nothing is stored
//     until Save; B with changes asks first;
//   - the folder picker: the shares of an SMB server, then folders, with
//     "Use this folder";
//   - Consoles: each folder of the games folder with the console it holds,
//     read from its name (GBA, SFC …) or chosen with Left/Right, or typed.
//
// Every network step runs on a goroutine and posts back (tbxPost); a
// generation drops answers for a view that is gone. Text fields use the
// shared prompt (tbxOpenPrompt, submit only); the password has its own
// masked keyboard target (nasPassVKTarget), saved on submit only and sealed
// at Save.

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/veandco/go-sdl2/sdl"
)

// nasSceneName is the scene Settings opens.
const nasSceneName = "NetworkDrives"

// nasPassVKTarget aims the keyboard at a drive's password (masked, submit
// only: vkeyboard.go treats it like the itch.io key).
const nasPassVKTarget vkTargetKind = 2731

// nasPassBuffer is the password being typed; cleared when the keyboard closes.
var nasPassBuffer string

const (
	nasViewList = iota
	nasViewEdit
	nasViewBrowse
	nasViewMap
)

// nasFound is one server mDNS found.
type nasFound struct {
	Name string
	Addr string
}

var nasScreen struct {
	visit tbxVisit
	nav   tbxNav
	view  int
	gen   uint64

	// The drive being added or edited, and whether it differs from the
	// stored one. pass is a newly typed password (plain, in memory only).
	edit        nasSource
	editNew     bool
	dirty       bool
	pass        string
	passChanged bool
	busy        bool
	note        string

	finding bool
	found   []nasFound

	// The folder picker: shares (SMB with no share chosen) or folders.
	fs          nasFS
	browseDir   string
	browseItems []string
	browseShare bool
	browseNote  string

	// Consoles: the games folder's folders.
	mapFolders []string
}

// nasSettingsRow is Settings → Network's door to the page.
func nasSettingsRow() setRow {
	return setRow{
		ID: "networkDrives", Label: "Network drives", Kind: setKindAction,
		Detail: setStatic("Games on a NAS or a PC at home: browse them in the library, copy one to the card to play"),
		Value: func(*Config) string {
			n := len(nasSettingsGet().Sources)
			if n == 0 {
				return "None"
			}
			return pluralizeCount(n, "drive", "drives")
		},
		Open: nasOpenScreen,
	}
}

// nasOpenScreen opens the page on the list.
func nasOpenScreen(config *Config) {
	tbxOpenScene(config, nasSceneName)
}

type nasScene struct{ sceneBase }

func (nasScene) render(r *sdl.Renderer, c *Config) bool {
	if nasScreen.visit.begin() {
		nasEnterList()
	}
	nasRenderScreen(r, c)
	return true
}

func (nasScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), nasSceneAction)
}

func (nasScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), nasSceneAction)
}

func init() { registerScene(nasScene{sceneBase{name: nasSceneName}}) }

// nasEnterList shows the list and asks every drive whether it answers.
func nasEnterList() {
	nasCloseBrowse()
	nasScreen.view = nasViewList
	nasScreen.gen++
	nasScreen.note = ""
	nasScreen.busy = false
	// A typed password lives only as long as the drive's page (Save sealed it).
	nasScreen.pass, nasScreen.passChanged = "", false
	nasScreen.nav.scroll = 0
	nasRecordsPreload()
	nasCachePreload()
	nasReachKick(true)
}

// ── Drawing ──────────────────────────────────────────────────────────────────

func nasRenderScreen(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	title, sub, status := "Network drives", "Games on a NAS or a PC at home", nasListStatus()
	switch nasScreen.view {
	case nasViewEdit:
		title = nasDisplayName(nasScreen.edit)
		if nasScreen.editNew {
			title = "Add a network drive"
		}
		sub, status = nasKindLabel(nasScreen.edit.Kind), nasScreen.note
	case nasViewBrowse:
		title, sub, status = "Choose the games folder", nasDisplayName(nasScreen.edit), nasScreen.browseNote
	case nasViewMap:
		title, sub, status = "Consoles", "Which console each folder holds", nasScreen.note
	}
	if nasScreen.busy {
		status = "Working…"
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader(title, sub, "network", status, ColorTextTertiary()))
	body := frame.bodyRect()
	nav := &nasScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	draw := func(id string, spec tbxRowSpec) {
		rect := sdl.Rect{X: frame.x, Y: y, W: frame.w, H: rowH}
		screen := rect
		screen.Y += top
		if screen.Y+screen.H >= body.Y && screen.Y <= body.Y+body.H {
			tbxDrawRow(renderer, config, fonts, screen, spec, id != "" && nav.focused(id))
		}
		if id != "" {
			nav.add(id, rect, row, false)
			row++
		}
		y += rowH + 6
	}
	section := func(text, icon string) {
		y += 6
		tbxDrawSection(renderer, config, text, icon, frame.x, top+y, frame.w)
		y += tbxSectionHeight
	}
	switch nasScreen.view {
	case nasViewList:
		nasDrawList(draw, section)
	case nasViewEdit:
		nasDrawEdit(draw)
	case nasViewBrowse:
		nasDrawBrowse(draw)
	case nasViewMap:
		nasDrawMap(draw)
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// nasListStatus is the list's header line: the download, or how many drives
// answer.
func nasListStatus() string {
	if title, pct, ok := nasDownloading(); ok {
		return "Downloading " + title + " · " + strconv.Itoa(pct) + "%"
	}
	sources := nasSettingsGet().Sources
	if len(sources) == 0 {
		return ""
	}
	up, known := 0, 0
	for _, src := range sources {
		if ok, k := nasReachable(src.ID); k {
			known++
			if ok {
				up++
			}
		}
	}
	if known == 0 {
		return "Checking the drives…"
	}
	return strconv.Itoa(up) + " of " + pluralizeCount(len(sources), "drive answers", "drives answer")
}

func nasDrawList(draw func(string, tbxRowSpec), section func(string, string)) {
	sources := nasSettingsGet().Sources
	if title, pct, ok := nasDownloading(); ok {
		draw("dl", tbxRowSpec{icon: "download", title: "Downloading " + title, subtitle: "A stops it · it continues from there next time", pill: strconv.Itoa(pct) + "%", tone: tbxToneAccent})
	}
	if len(sources) > 0 {
		section("Your drives", "network")
	}
	for i, src := range sources {
		spec := tbxRowSpec{icon: "network", title: nasDisplayName(src), subtitle: nasWhere(src)}
		games := -1
		if l := nasCachePeek(src); l != nil {
			games = len(l.Games)
		}
		running, errText := nasRefreshStatus(src.ID)
		ok, known := nasReachable(src.ID)
		switch {
		case running:
			spec.pill, spec.tone = "Listing…", tbxToneNeutral
		case known && !ok:
			spec.pill, spec.tone = "Offline", tbxToneWarn
			if why := nasReachError(src.ID); why != "" {
				spec.subtitle = why
			}
		case errText != "":
			spec.pill, spec.tone = "Problem", tbxToneWarn
			spec.subtitle = errText
		case known:
			spec.pill, spec.tone = "Online", tbxToneGood
		}
		if games >= 0 {
			spec.note = pluralizeCount(games, "game", "games")
		}
		draw("src"+strconv.Itoa(i), spec)
	}
	section("Add", "plus")
	draw("add", tbxRowSpec{icon: "plus", title: "Add a network drive", subtitle: "A shared folder (Windows, most NAS boxes), WebDAV or SFTP"})
	findSub := "Looks for shared folders (SMB) on this Wi-Fi"
	if nasScreen.finding {
		findSub = "Looking…"
	} else if nasScreen.found != nil && len(nasScreen.found) == 0 {
		findSub = "None answered — add yours by its address"
	}
	draw("find", tbxRowSpec{icon: "radar", title: "Find shared folders on this network", subtitle: findSub})
	for i, f := range nasScreen.found {
		draw("found"+strconv.Itoa(i), tbxRowSpec{icon: "network", title: f.Name, subtitle: f.Addr + " · A adds it", pill: "Add", tone: tbxToneAccent})
	}
	if len(sources) > 0 {
		draw("refresh", tbxRowSpec{icon: "refresh", title: "Refresh game lists", subtitle: "Look at every drive again now (the library also does it every 15 minutes)"})
	}
}

func nasDrawEdit(draw func(string, tbxRowSpec)) {
	e := nasScreen.edit
	or := func(v, empty string) string {
		if strings.TrimSpace(v) == "" {
			return empty
		}
		return v
	}
	draw("kind", tbxRowSpec{icon: "network", title: "Kind", subtitle: nasKindHelp(e.Kind), pill: nasKindLabel(e.Kind), tone: tbxToneAccent})
	draw("name", tbxRowSpec{icon: "edit", title: "Name", subtitle: or(e.Name, "Optional — "+nasDisplayName(e))})
	switch e.Kind {
	case nasKindWebDAV:
		draw("host", tbxRowSpec{icon: "network", title: "WebDAV address", subtitle: or(e.Host, "Like https://nas.local:5006/ or http://192.168.1.20/dav/")})
	case nasKindFolder:
		draw("host", tbxRowSpec{icon: "folder", title: "Folder", subtitle: or(e.Host, `Like \\NAS\Games or Z:\Games`)})
	default:
		draw("host", tbxRowSpec{icon: "network", title: "Server", subtitle: or(e.Host, "Its name or address, like nas.local or 192.168.1.20")})
	}
	if e.Kind == nasKindSFTP {
		port := "22"
		if e.Port > 0 {
			port = strconv.Itoa(e.Port)
		}
		draw("port", tbxRowSpec{icon: "network", title: "Port", subtitle: port})
	}
	if e.Kind == nasKindSMB {
		draw("share", tbxRowSpec{icon: "folder", title: "Shared folder", subtitle: or(e.Share, "A picks it from the server's list")})
	}
	if e.Kind != nasKindFolder {
		user := e.User
		if e.Domain != "" {
			user = e.Domain + `\` + e.User
		}
		empty := "Not set"
		if e.Kind == nasKindSMB {
			empty = "Not set — signs in as a guest"
		}
		draw("user", tbxRowSpec{icon: "user", title: "User name", subtitle: or(user, empty)})
		pass := "Not set"
		if nasScreen.passChanged && nasScreen.pass != "" {
			pass = "Typed · hidden"
		} else if !nasScreen.passChanged && e.Password != "" {
			pass = "Saved · hidden"
		}
		draw("pass", tbxRowSpec{icon: "key", title: "Password", subtitle: pass})
	}
	if e.Kind == nasKindSFTP && e.HostKey != "" {
		draw("hostkey", tbxRowSpec{icon: "key", title: "Trusted server key", subtitle: e.HostKey})
	}
	draw("root", tbxRowSpec{icon: "folder", title: "Games folder", subtitle: or(e.Root, "The top of the drive") + " · A browses"})
	draw("test", tbxRowSpec{icon: "refresh", title: "Test connection", subtitle: or(nasScreen.note, "Signs in and looks at the games folder"), pill: "Test", tone: tbxToneAccent})
	draw("map", tbxRowSpec{icon: "game", title: "Consoles", subtitle: nasMapSummary(e)})
	label := "Save"
	if nasScreen.editNew {
		label = "Add this drive"
	}
	saveSpec := tbxRowSpec{icon: "check", title: label, subtitle: "Its games join the library's shelves", pill: "Save", tone: tbxToneGood}
	if !nasScreen.dirty && !nasScreen.editNew {
		saveSpec.subtitle, saveSpec.pill, saveSpec.tone = "Nothing changed", "", tbxToneNeutral
	}
	draw("save", saveSpec)
	if !nasScreen.editNew {
		draw("remove", tbxRowSpec{icon: "trash", title: "Remove this drive", subtitle: "Its games leave the library; copies on the card stay", pill: "Remove", tone: tbxToneWarn})
	}
}

func nasKindHelp(kind string) string {
	switch kind {
	case nasKindSMB:
		return "Windows shares and most NAS boxes (Synology, QNAP, TrueNAS, a router's USB disk) · A changes"
	case nasKindWebDAV:
		return "Nextcloud, a NAS's WebDAV server · A changes"
	case nasKindSFTP:
		return "A NAS or computer with SSH switched on · A changes"
	case nasKindFolder:
		return "A folder Windows already reaches, \\\\server\\share included · A changes"
	}
	return "A changes"
}

// nasMapSummary is the Consoles row's line.
func nasMapSummary(src nasSource) string {
	if l := nasCachePeek(src); l != nil && len(l.Folders) > 0 {
		parts := make([]string, 0, len(l.Folders))
		for _, f := range l.Folders {
			name := f.Name
			if name == "." {
				name = "the games folder"
			}
			parts = append(parts, name+" → "+f.System)
		}
		return strings.Join(parts, " · ")
	}
	if len(src.Map) > 0 {
		return pluralizeCount(len(src.Map), "folder assigned", "folders assigned") + " · A shows them"
	}
	return "Read from the folders' names (GBA, SFC, PS …) · A shows them"
}

func nasDrawBrowse(draw func(string, tbxRowSpec)) {
	if nasScreen.browseShare {
		for i, name := range nasScreen.browseItems {
			draw("item"+strconv.Itoa(i), tbxRowSpec{icon: "folder", title: name, subtitle: "Shared folder · A opens it"})
		}
		if !nasScreen.busy {
			draw("typeshare", tbxRowSpec{icon: "edit", title: "Type the shared folder's name", subtitle: "When yours is not listed"})
		}
		return
	}
	where := nasScreen.browseDir
	if where == "" {
		where = "The top of the drive"
	}
	draw("use", tbxRowSpec{icon: "check", title: "Use this folder", subtitle: where + nasBrowseConsoles(), pill: "Use", tone: tbxToneGood})
	if nasScreen.browseDir != "" || (nasScreen.edit.Kind == nasKindSMB && !nasScreen.busy) {
		draw("up", tbxRowSpec{icon: "folder", title: "Up one folder", subtitle: "Back to the folder above"})
	}
	for i, name := range nasScreen.browseItems {
		spec := tbxRowSpec{icon: "folder", title: name, subtitle: "A opens it"}
		if sys, ok := nasFolderSystem(name, nil, nil); ok {
			spec.pill, spec.tone = sys.Name, tbxToneAccent
		}
		draw("item"+strconv.Itoa(i), spec)
	}
}

// nasBrowseConsoles names the console folders the picker sees here.
func nasBrowseConsoles() string {
	n := 0
	for _, name := range nasScreen.browseItems {
		if _, ok := nasFolderSystem(name, nil, nil); ok {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return " · " + pluralizeCount(n, "console folder", "console folders")
}

func nasDrawMap(draw func(string, tbxRowSpec)) {
	e := nasScreen.edit
	draw("mapdone", tbxRowSpec{icon: "check", title: "Done", subtitle: "Back to the drive (Save keeps the changes)"})
	draw("map.", nasMapRow(e, ".", "This folder itself"))
	for i, name := range nasScreen.mapFolders {
		draw("map"+strconv.Itoa(i), nasMapRow(e, name, name))
	}
}

func nasMapRow(e nasSource, folder, title string) tbxRowSpec {
	spec := tbxRowSpec{icon: "folder", title: title}
	v, assigned := e.Map[folder]
	sys, ok := nasFolderSystem(folder, e.Map, nil)
	switch {
	case assigned && v == nasSkipMapping:
		spec.pill, spec.tone, spec.subtitle = "Not games", tbxToneNeutral, "Left/Right picks a console · A types one"
	case ok && assigned:
		spec.pill, spec.tone, spec.subtitle = sys.Name, tbxToneGood, "Chosen by you · Left/Right changes · A types one"
	case ok:
		spec.pill, spec.tone, spec.subtitle = sys.Name, tbxToneAccent, "From the folder's name · Left/Right changes"
	default:
		spec.pill, spec.tone, spec.subtitle = "—", tbxToneNeutral, "No console from its name · Left/Right picks one · A types one"
	}
	return spec
}

// ── Input ────────────────────────────────────────────────────────────────────

func nasSceneAction(config *Config, act tbxAction) bool {
	nav := &nasScreen.nav
	switch act {
	case tbxActConfirm:
		nasActivate(config, nav.focus)
		return true
	case tbxActBack:
		return nasBack(config)
	case tbxActY:
		if nasScreen.view == nasViewList {
			nasRefreshAll()
		}
		return true
	case tbxActX:
		if nasScreen.view == nasViewBrowse && !nasScreen.browseShare {
			nasUseBrowseFolder()
		}
		return true
	case tbxActLeft, tbxActRight:
		if nasScreen.view == nasViewMap {
			if folder, ok := nasMapFolderOf(nav.focus); ok {
				step := 1
				if act == tbxActLeft {
					step = -1
				}
				nasMapCycle(folder, step)
				return true
			}
		}
		if nasScreen.view == nasViewEdit && nav.focus == "kind" {
			nasCycleKind(map[bool]int{true: -1, false: 1}[act == tbxActLeft])
			return true
		}
	}
	return tbxNavAction(nav, act)
}

func nasSceneClick(mx, my int32, config *Config) bool {
	it, ok := nasScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	nasScreen.nav.focus = it.id
	nasActivate(config, it.id)
	return true
}

func nasSceneHints() []FooterHint {
	switch nasScreen.view {
	case nasViewEdit:
		hints := []FooterHint{{Button: "A", Label: "Change"}}
		if nasScreen.nav.focus == "kind" {
			hints = append(hints, FooterHint{Button: "Left/Right", Label: "Kind"})
		}
		return append(hints, FooterHint{Button: "B", Label: "Back"})
	case nasViewBrowse:
		if nasScreen.browseShare {
			return []FooterHint{{Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
		}
		return []FooterHint{{Button: "A", Label: "Open"}, {Button: "X", Label: "Use this folder"}, {Button: "B", Label: "Up"}}
	case nasViewMap:
		return []FooterHint{{Button: "Left/Right", Label: "Console"}, {Button: "A", Label: "Type a console"}, {Button: "B", Label: "Done"}}
	}
	return []FooterHint{{Button: "A", Label: "Open"}, {Button: "Y", Label: "Refresh"}, {Button: "B", Label: "Back"}}
}

// nasBack is B: one view back; leaving a drive with changes asks first.
func nasBack(config *Config) bool {
	switch nasScreen.view {
	case nasViewBrowse:
		if nasScreen.busy {
			nasScreen.gen++
			nasScreen.busy = false
		}
		if !nasScreen.browseShare && nasScreen.browseDir != "" {
			nasBrowseUp()
			return true
		}
		if !nasScreen.browseShare && nasScreen.edit.Kind == nasKindSMB && nasScreen.edit.Share != "" && nasScreen.browseDir == "" {
			nasBrowseUp()
			return true
		}
		nasCloseBrowse()
		nasScreen.view = nasViewEdit
		nasScreen.nav.setFocus("root")
		return true
	case nasViewMap:
		nasScreen.view = nasViewEdit
		nasScreen.nav.setFocus("map")
		return true
	case nasViewEdit:
		if nasScreen.dirty || (nasScreen.editNew && nasScreen.edit.Host != "") {
			tbxOpenSheet(config, tbxSheetSpec{
				title: "Keep the changes?", icon: "network",
				body: "This drive has changes that are not saved yet.",
				buttons: []tbxSheetButton{
					{label: "Discard", run: func() { nasEnterList() }},
					{label: "Save", primary: true, run: nasSave},
				},
				focus: 1,
			})
			return true
		}
		nasEnterList()
		return true
	}
	return false
}

func nasActivate(config *Config, id string) {
	switch nasScreen.view {
	case nasViewList:
		nasActivateList(config, id)
	case nasViewEdit:
		nasActivateEdit(config, id)
	case nasViewBrowse:
		nasActivateBrowse(id)
	case nasViewMap:
		nasActivateMap(id)
	}
}

func nasActivateList(config *Config, id string) {
	switch id {
	case "dl":
		if title, _, ok := nasDownloading(); ok {
			tbxOpenSheet(config, tbxSheetSpec{
				title: "Stop the download?", subtitle: title, icon: "download",
				body:    "What was copied stays on the card; starting the game's download again continues from there.",
				buttons: []tbxSheetButton{{label: "Stop", run: nasDownloadCancel}, {label: "Keep going", primary: true}},
				focus:   1,
			})
		}
		return
	case "add":
		nasStartEdit(nasSource{ID: nasNewID(), Kind: nasKindSMB}, true)
		return
	case "find":
		nasFindServers()
		return
	case "refresh":
		nasRefreshAll()
		return
	}
	if strings.HasPrefix(id, "src") {
		i, err := strconv.Atoi(strings.TrimPrefix(id, "src"))
		sources := nasSettingsGet().Sources
		if err == nil && i >= 0 && i < len(sources) {
			nasStartEdit(sources[i], false)
		}
		return
	}
	if strings.HasPrefix(id, "found") {
		i, err := strconv.Atoi(strings.TrimPrefix(id, "found"))
		if err == nil && i >= 0 && i < len(nasScreen.found) {
			f := nasScreen.found[i]
			nasStartEdit(nasSource{ID: nasNewID(), Kind: nasKindSMB, Name: f.Name, Host: f.Addr}, true)
			nasScreen.dirty = true
			nasScreen.nav.setFocus("share")
		}
	}
}

func nasStartEdit(src nasSource, isNew bool) {
	if len(nasSettingsGet().Sources) >= nasMaxSources && isNew {
		ShowToast("JukaHub keeps up to "+strconv.Itoa(nasMaxSources)+" drives — remove one first", ToastKindInfo)
		return
	}
	nasScreen.view = nasViewEdit
	nasScreen.edit = nasCopySource(src)
	nasScreen.editNew = isNew
	nasScreen.dirty = false
	nasScreen.pass = ""
	nasScreen.passChanged = false
	nasScreen.note = ""
	nasScreen.busy = false
	nasScreen.gen++
	nasScreen.nav.scroll = 0
	nasScreen.nav.setFocus("host")
	if !isNew {
		nasScreen.nav.setFocus("test")
	}
}

// nasCycleKind steps the drive's kind (Folder only on Windows).
func nasCycleKind(dir int) {
	kinds := []string{nasKindSMB, nasKindWebDAV, nasKindSFTP}
	if IsWindows() {
		kinds = append(kinds, nasKindFolder)
	}
	i := 0
	for k, kind := range kinds {
		if kind == nasScreen.edit.Kind {
			i = k
		}
	}
	i = (i + dir + len(kinds)) % len(kinds)
	nasScreen.edit.Kind = kinds[i]
	nasScreen.edit.Port = 0
	nasScreen.edit.HostKey = ""
	nasScreen.dirty = true
	nasScreen.note = ""
	PlayNavSound()
}

func nasActivateEdit(config *Config, id string) {
	if nasScreen.busy && id != "kind" {
		ShowToast("Still working — a moment", ToastKindInfo)
		return
	}
	e := &nasScreen.edit
	switch id {
	case "kind":
		nasCycleKind(1)
	case "name":
		tbxOpenPrompt("Drive name", "Like Home NAS", e.Name, func(text string) {
			if len(text) > 60 {
				text = text[:60]
			}
			if text != nasScreen.edit.Name {
				nasScreen.edit.Name, nasScreen.dirty = text, true
			}
		})
	case "host":
		nasPromptHost()
	case "port":
		tbxOpenPrompt("SFTP port", "22", strconv.Itoa(max(e.Port, 22)), func(text string) {
			n, err := strconv.Atoi(strings.TrimSpace(text))
			if text == "" {
				n, err = 0, nil
			}
			if err != nil || n < 0 || n > 65535 {
				ShowToast("A port is a number from 1 to 65535", ToastKindWarning)
				return
			}
			nasScreen.edit.Port, nasScreen.dirty = n, true
		})
	case "share", "root":
		if strings.TrimSpace(e.Host) == "" {
			ShowToast("Type the server's address first", ToastKindInfo)
			nasScreen.nav.setFocus("host")
			return
		}
		nasOpenBrowse(id == "share" || (e.Kind == nasKindSMB && e.Share == ""))
	case "user":
		seed := e.User
		if e.Domain != "" {
			seed = e.Domain + `\` + e.User
		}
		tbxOpenPrompt("User name", `Your user on the drive (DOMAIN\user works)`, seed, func(text string) {
			text = strings.TrimSpace(text)
			if len(text) > nasFieldMax {
				return
			}
			domain, user := "", text
			if i := strings.IndexByte(text, '\\'); i > 0 {
				domain, user = text[:i], text[i+1:]
			}
			if user != nasScreen.edit.User || domain != nasScreen.edit.Domain {
				nasScreen.edit.User, nasScreen.edit.Domain, nasScreen.dirty = user, domain, true
			}
		})
	case "pass":
		nasOpenPassKeyboard()
	case "hostkey":
		tbxOpenSheet(config, tbxSheetSpec{
			title: "Trusted server key", icon: "key",
			body: "JukaHub signs in to this SFTP server only while it shows this key:\n\n" + e.HostKey +
				"\n\nForget it if the server was reinstalled; the next test shows the new key to trust.",
			buttons: []tbxSheetButton{{label: "Forget the key", run: func() { nasScreen.edit.HostKey, nasScreen.dirty = "", true }}, {label: "Keep", primary: true}},
			focus:   1,
		})
	case "test":
		nasTest(config)
	case "map":
		nasOpenMap()
	case "save":
		nasSave()
	case "remove":
		name := nasDisplayName(*e)
		srcID := e.ID
		tbxOpenSheet(config, tbxSheetSpec{
			title: "Remove " + name + "?", icon: "trash",
			body:    "Its games leave the library. Games already copied to the card stay there, and nothing on the drive changes.",
			buttons: []tbxSheetButton{{label: "Remove", run: func() { nasRemove(srcID, name) }}, {label: "Keep", primary: true}},
			focus:   1,
		})
	}
}

// nasPromptHost asks for the address; an SMB address pasted whole
// (\\nas\share\folder, smb://nas/share/folder) fills the share and folder too.
func nasPromptHost() {
	e := nasScreen.edit
	title, hint := "Server", "nas.local or 192.168.1.20"
	switch e.Kind {
	case nasKindWebDAV:
		title, hint = "WebDAV address", "https://nas.local:5006/"
	case nasKindFolder:
		title, hint = "Folder", `\\NAS\Games`
	case nasKindSMB:
		hint = `nas.local, 192.168.1.20 or \\NAS\Games`
	}
	tbxOpenPrompt(title, hint, e.Host, func(text string) {
		text = strings.TrimSpace(text)
		if len(text) > nasFieldMax {
			ShowToast("That address is too long", ToastKindWarning)
			return
		}
		next := nasScreen.edit
		switch next.Kind {
		case nasKindSMB:
			host, port, share, root, ok := nasParseSMBAddress(text)
			if !ok {
				ShowToast("That is not a server name or address", ToastKindWarning)
				return
			}
			next.Host = host
			if port > 0 {
				next.Port = port
			}
			if share != "" {
				next.Share, next.Root = share, root
			}
		case nasKindSFTP:
			host, port, root, user, ok := nasParseSFTPAddress(text)
			if !ok {
				ShowToast("That is not a server name or address", ToastKindWarning)
				return
			}
			next.Host = host
			if port > 0 {
				next.Port = port
			}
			if root != "" {
				next.Root = root
			}
			if user != "" && len(user) <= nasFieldMax {
				next.User, next.Domain = user, ""
			}
		case nasKindWebDAV:
			if text != "" {
				u, err := webdavCleanURL(text)
				if err != nil {
					ShowToast(firstLine(err.Error()), ToastKindWarning)
					return
				}
				text = u.String()
			}
			next.Host = text
		default:
			next.Host = text
		}
		if next.Host != nasScreen.edit.Host || next.Share != nasScreen.edit.Share || next.Root != nasScreen.edit.Root || next.Port != nasScreen.edit.Port ||
			next.User != nasScreen.edit.User {
			if next.Host != nasScreen.edit.Host {
				next.HostKey = ""
			}
			nasScreen.edit, nasScreen.dirty, nasScreen.note = next, true, ""
		}
	})
}

// nasParseSMBAddress reads "nas", "192.168.1.20", "nas:4455", "[fe80::1]:445",
// "\\nas\share\folder" or "smb://nas/share/folder" (outside data: typed or
// pasted). port is 0 when the text names none.
func nasParseSMBAddress(text string) (host string, port int, share, root string, ok bool) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(strings.ToLower(s), "smb://") {
		s = s[len("smb://"):]
	}
	s = strings.TrimLeft(strings.ReplaceAll(s, `\`, "/"), "/")
	parts := strings.SplitN(s, "/", 3)
	host = strings.TrimSpace(parts[0])
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	// A port: "name:445" or "[v6]:445". A bare IPv6 address (two or more
	// colons, no brackets) has none.
	if strings.HasPrefix(host, "[") || strings.Count(host, ":") == 1 {
		h, p, err := net.SplitHostPort(host)
		if err != nil {
			if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
				return "", 0, "", "", false
			}
			h = host[1 : len(host)-1]
		} else {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return "", 0, "", "", false
			}
			port = n
		}
		host = h
	}
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\"'<>|*?") {
		return "", 0, "", "", false
	}
	if len(parts) > 1 {
		share = strings.TrimSpace(parts[1])
		if !nasSafeName(share) {
			share = ""
		}
	}
	if len(parts) > 2 && share != "" {
		if r, good := nasCleanRoot(parts[2], false); good {
			root = r
		}
	}
	return host, port, share, root, true
}

// nasParseSFTPAddress reads "host", "host:2222", "user@host:2222/folder" or
// the same with sftp:// in front. A password in the address is ignored: it
// has its own masked field.
func nasParseSFTPAddress(text string) (host string, port int, root, user string, ok bool) {
	s := strings.TrimSpace(text)
	if !strings.Contains(s, "://") {
		s = "sftp://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || len(u.Hostname()) > 253 {
		return "", 0, "", "", false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return "", 0, "", "", false
		}
		port = n
	}
	if u.Path != "" && u.Path != "/" {
		if r, good := nasCleanRoot(u.Path, true); good {
			root = r
		}
	}
	if u.User != nil {
		user = strings.TrimSpace(u.User.Username())
	}
	return u.Hostname(), port, root, user, true
}

// nasDraftSource is the drive as typed, with a newly typed password in plain
// text (nasOpenSecret takes it as it is) for a test or a browse.
func nasDraftSource() nasSource {
	src := nasCopySource(nasScreen.edit)
	if nasScreen.passChanged {
		src.Password = nasScreen.pass
	}
	return src
}

// nasDraftReady says what a drive still needs before it can be reached, "".
func nasDraftReady(src nasSource) string {
	switch {
	case strings.TrimSpace(src.Host) == "":
		return "Type the server's address first"
	case src.Kind == nasKindSMB && strings.TrimSpace(src.Share) == "":
		return "Choose the shared folder first"
	case src.Kind == nasKindSFTP && strings.TrimSpace(src.User) == "":
		return "Type the user name first"
	}
	return ""
}

// nasTest signs in and looks at the games folder.
func nasTest(config *Config) {
	src := nasDraftSource()
	if why := nasDraftReady(src); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	nasScreen.gen++
	gen := nasScreen.gen
	nasScreen.busy = true
	nasScreen.note = "Connecting…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		var seen string
		var fs nasFS
		var err error
		if src.Kind == nasKindSFTP {
			fs, err = nasDialSFTP(ctx, src, &seen)
		} else {
			fs, err = nasDial(ctx, src)
		}
		var consoles []string
		folders := 0
		if err == nil {
			var entries []nasEntry
			entries, err = fs.List(ctx, src.Root)
			fs.Close()
			for _, e := range entries {
				if !e.IsDir || skipScanEntryName(e.Name) {
					continue
				}
				folders++
				if _, ok := nasFolderSystem(e.Name, src.Map, nil); ok {
					consoles = append(consoles, e.Name)
				}
			}
		}
		tbxPost(func() {
			if gen != nasScreen.gen {
				return
			}
			nasScreen.busy = false
			var changed *nasHostKeyChanged
			switch {
			case errors.Is(err, errNasHostKeyUnknown) && seen != "":
				nasScreen.note = "Check the server's key"
				nasAskTrust(config, src, seen, "")
				return
			case errors.As(err, &changed):
				nasScreen.note = "The server's key changed"
				nasAskTrust(config, src, changed.Now, changed.Trusted)
				return
			case err != nil:
				nasScreen.note = nasErrorText(src, err)
				nasReachSet(src.ID, false, nasScreen.note)
				return
			}
			nasReachSet(src.ID, true, "")
			switch {
			case len(consoles) > 0:
				sort.Strings(consoles)
				shown := consoles
				if len(shown) > 6 {
					shown = shown[:6]
				}
				nasScreen.note = "Connected · " + pluralizeCount(len(consoles), "console folder", "console folders") + ": " + strings.Join(shown, ", ")
				if len(consoles) > len(shown) {
					nasScreen.note += " …"
				}
			case folders > 0:
				nasScreen.note = "Connected · no folder is named after a console — open Consoles to choose"
			default:
				nasScreen.note = "Connected · the games folder is empty — choose another"
			}
			PlaySuccessSound()
		})
	}()
}

// nasAskTrust shows an SFTP server's key and asks before trusting it.
func nasAskTrust(config *Config, src nasSource, now, was string) {
	body := "This is the first connection to " + src.Host + ". The server shows this key:\n\n" + now +
		"\n\nTrust it when it matches the server's own (on the server: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub). JukaHub then refuses any other key from this drive."
	title := "Trust this server?"
	button := "Trust"
	if was != "" {
		title = "The server's key changed"
		body = "The key " + src.Host + " shows is not the one you trusted.\n\nTrusted: " + was + "\nNow: " + now +
			"\n\nThat happens when the server was reinstalled — and when something on the network pretends to be it. Trust the new key only if you know why it changed."
		button = "Trust the new key"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title: title, subtitle: src.Host, icon: "key", body: body,
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: button, run: func() {
				nasScreen.edit.HostKey = now
				nasScreen.dirty = true
				nasScreen.note = "Key trusted — Save keeps it"
				nasTest(config)
			}},
		},
		focus: 0,
	})
}

// nasSave checks and stores the drive, then lists its games.
func nasSave() {
	src := nasCopySource(nasScreen.edit)
	if why := nasDraftReady(src); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	if src.Kind == nasKindWebDAV {
		if _, err := webdavCleanURL(src.Host); err != nil {
			ShowToast(firstLine(err.Error()), ToastKindWarning)
			return
		}
	}
	if nasScreen.passChanged {
		sealed, err := nasSeal(nasScreen.pass)
		if err != nil {
			ShowToast("The password could not be stored: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		src.Password = sealed
	}
	if err := nasSaveSource(src); err != nil {
		ShowToast("Not saved: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	nasScreen.pass, nasScreen.passChanged, nasScreen.dirty, nasScreen.editNew = "", false, false, false
	nasScreen.edit = src
	if src.Kind == nasKindSFTP && src.HostKey == "" {
		ShowToast("Saved — test the connection to trust the server's key", ToastKindInfo)
	} else {
		ShowToast("Saved — "+nasDisplayName(src)+"'s games are being listed", ToastKindSuccess)
	}
	nasRefreshStart(src, true, func(err error) {
		if err == nil {
			ShowToast(nasDisplayName(src)+": games listed", ToastKindSuccess)
		}
	})
	nasReachKick(true)
	nasEnterList()
}

// nasRemove forgets a drive.
func nasRemove(id, name string) {
	if job := nasDL.job; job != nil && job.ref.Source == id {
		nasDownloadCancel()
	}
	if err := nasRemoveSource(id); err != nil {
		ShowToast("Not removed: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	nasCacheStore(id, nil)
	ShowToast(name+" removed", ToastKindSuccess)
	nasEnterList()
	nasLibraryChanged()
}

// nasRefreshAll walks every drive again now.
func nasRefreshAll() {
	sources := nasSettingsGet().Sources
	if len(sources) == 0 {
		return
	}
	nasReachKick(true)
	started := 0
	for _, src := range sources {
		if nasRefreshStart(src, true, nil) {
			started++
		}
	}
	if started > 0 {
		ShowToast("Looking at "+pluralizeCount(started, "drive", "drives")+" again…", ToastKindInfo)
	}
}

// nasFindServers browses mDNS for SMB servers for a few seconds.
func nasFindServers() {
	if nasScreen.finding {
		return
	}
	nasScreen.finding = true
	go func() {
		found := nasDiscoverSMB(4 * time.Second)
		tbxPost(func() {
			nasScreen.finding = false
			nasScreen.found = found
		})
	}()
}

// nasDiscoverSMB lists the servers that announce shared folders.
func nasDiscoverSMB(timeout time.Duration) []nasFound {
	found := []nasFound{}
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
	if err != nil {
		logInfo("nas", "mDNS browse not possible here: %v", err)
		return found
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := resolver.Browse(ctx, "_smb._tcp", "local.", entries); err != nil {
		logInfo("nas", "mDNS browse failed: %v", err)
		return found
	}
	seen := map[string]bool{}
	// The resolver closes entries when ctx ends.
	for e := range entries {
		if e == nil {
			continue
		}
		name := mlMDNSName(e.Instance, e.HostName)
		for _, ip := range e.AddrIPv4 {
			if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			found = append(found, nasFound{Name: name, Addr: ip.String()})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return strings.ToLower(found[i].Name) < strings.ToLower(found[j].Name) })
	return found
}

// ── The folder picker ────────────────────────────────────────────────────────

// nasOpenBrowse opens the picker: the server's shares (shares true), or the
// folders from the games folder chosen so far.
func nasOpenBrowse(shares bool) {
	src := nasDraftSource()
	nasCloseBrowse()
	nasScreen.view = nasViewBrowse
	nasScreen.browseShare = shares && src.Kind == nasKindSMB
	nasScreen.browseDir = src.Root
	if nasScreen.browseShare {
		nasScreen.browseDir = ""
	}
	nasScreen.browseItems = nil
	nasScreen.nav.scroll = 0
	nasScreen.nav.focus = ""
	if nasScreen.browseShare {
		nasLoadShares(src)
		return
	}
	nasBrowseLoad(src, nasScreen.browseDir)
}

// nasCloseBrowse ends the picker's connection.
func nasCloseBrowse() {
	if fs := nasScreen.fs; fs != nil {
		nasScreen.fs = nil
		go fs.Close()
	}
}

func nasLoadShares(src nasSource) {
	nasScreen.gen++
	gen := nasScreen.gen
	nasScreen.busy = true
	nasScreen.browseNote = "Asking " + src.Host + " for its shared folders…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shares, listed, err := nasSMBShares(ctx, src)
		tbxPost(func() {
			if gen != nasScreen.gen || nasScreen.view != nasViewBrowse {
				return
			}
			nasScreen.busy = false
			switch {
			case err != nil:
				nasScreen.browseNote = nasErrorText(src, err)
			case !listed:
				nasScreen.browseNote = "The server does not list its shared folders — type the name"
			case len(shares) == 0:
				nasScreen.browseNote = "No shared folders are open to this user — type the name"
			default:
				nasScreen.browseNote = pluralizeCount(len(shares), "shared folder", "shared folders")
			}
			nasScreen.browseItems = shares
		})
	}()
}

// nasBrowseLoad lists dir (connecting first when the picker has no
// connection yet).
func nasBrowseLoad(src nasSource, dir string) {
	nasScreen.gen++
	gen := nasScreen.gen
	nasScreen.busy = true
	nasScreen.browseNote = "Opening…"
	fs := nasScreen.fs
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		opened := false
		if fs == nil {
			fs, err = nasDial(ctx, src)
			opened = err == nil
		}
		var names []string
		if err == nil {
			var entries []nasEntry
			entries, err = fs.List(ctx, dir)
			for _, e := range entries {
				if e.IsDir && !skipScanEntryName(e.Name) && nasSafeName(e.Name) {
					names = append(names, e.Name)
				}
			}
		}
		sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
		tbxPost(func() {
			if gen != nasScreen.gen || nasScreen.view != nasViewBrowse {
				if opened {
					go fs.Close()
				}
				return
			}
			if opened {
				nasScreen.fs = fs
			}
			nasScreen.busy = false
			if err != nil {
				nasScreen.browseNote = nasErrorText(src, err)
				return
			}
			nasScreen.browseDir = dir
			nasScreen.browseItems = names
			nasScreen.browseNote = pluralizeCount(len(names), "folder", "folders")
			nasScreen.nav.setFocus("use")
		})
	}()
}

func nasActivateBrowse(id string) {
	if nasScreen.busy {
		return
	}
	if nasScreen.browseShare {
		if id == "typeshare" {
			tbxOpenPrompt("Shared folder", "Like Games", nasScreen.edit.Share, func(text string) {
				text = strings.TrimSpace(strings.Trim(text, `\/`))
				if !nasSafeName(text) {
					ShowToast("That is not a shared folder's name", ToastKindWarning)
					return
				}
				nasPickShare(text)
			})
			return
		}
		if i, ok := nasItemIndex(id); ok {
			nasPickShare(nasScreen.browseItems[i])
		}
		return
	}
	switch id {
	case "use":
		nasUseBrowseFolder()
		return
	case "up":
		nasBrowseUp()
		return
	}
	if i, ok := nasItemIndex(id); ok {
		nasBrowseLoad(nasDraftSource(), nasJoin(nasScreen.browseDir, nasScreen.browseItems[i]))
	}
}

func nasItemIndex(id string) (int, bool) {
	if !strings.HasPrefix(id, "item") {
		return 0, false
	}
	i, err := strconv.Atoi(strings.TrimPrefix(id, "item"))
	if err != nil || i < 0 || i >= len(nasScreen.browseItems) {
		return 0, false
	}
	return i, true
}

// nasPickShare chooses an SMB share and browses inside it.
func nasPickShare(share string) {
	if share != nasScreen.edit.Share {
		nasScreen.edit.Share, nasScreen.edit.Root, nasScreen.dirty = share, "", true
	}
	nasCloseBrowse()
	nasScreen.browseShare = false
	nasBrowseLoad(nasDraftSource(), "")
}

// nasBrowseUp goes to the folder above; at an SMB share's top, back to the
// shares.
func nasBrowseUp() {
	if nasScreen.browseDir == "" || nasScreen.browseDir == "/" {
		if nasScreen.edit.Kind == nasKindSMB {
			nasCloseBrowse()
			nasScreen.browseShare = true
			nasScreen.browseItems = nil
			nasLoadShares(nasDraftSource())
		}
		return
	}
	up := nasScreen.browseDir
	if i := strings.LastIndexByte(up, '/'); i >= 0 {
		up = up[:i]
		if up == "" && strings.HasPrefix(nasScreen.browseDir, "/") {
			up = "/"
		}
	} else {
		up = ""
	}
	nasBrowseLoad(nasDraftSource(), up)
}

// nasUseBrowseFolder makes the picker's folder the games folder.
func nasUseBrowseFolder() {
	if nasScreen.busy {
		return
	}
	dir := nasScreen.browseDir
	if dir != nasScreen.edit.Root {
		nasScreen.edit.Root, nasScreen.dirty = dir, true
		nasScreen.edit.Map = nil
	}
	nasCloseBrowse()
	nasScreen.view = nasViewEdit
	nasScreen.note = ""
	nasScreen.nav.setFocus("map")
	if nasBrowseConsoles() == "" {
		ShowToast("No folder here is named after a console — open Consoles to choose", ToastKindInfo)
	}
}

// ── Consoles ─────────────────────────────────────────────────────────────────

// nasOpenMap lists the games folder's folders for the console choices.
func nasOpenMap() {
	src := nasDraftSource()
	if why := nasDraftReady(src); why != "" {
		ShowToast(why, ToastKindInfo)
		return
	}
	nasScreen.view = nasViewMap
	nasScreen.mapFolders = nil
	nasScreen.nav.scroll = 0
	nasScreen.nav.focus = "mapdone"
	nasScreen.gen++
	gen := nasScreen.gen
	nasScreen.busy = true
	nasScreen.note = "Looking at the games folder…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var names []string
		fs, err := nasDial(ctx, src)
		if err == nil {
			var entries []nasEntry
			entries, err = fs.List(ctx, src.Root)
			fs.Close()
			for _, e := range entries {
				if e.IsDir && !skipScanEntryName(e.Name) && nasSafeName(e.Name) {
					names = append(names, e.Name)
				}
			}
		}
		sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
		tbxPost(func() {
			if gen != nasScreen.gen || nasScreen.view != nasViewMap {
				return
			}
			nasScreen.busy = false
			if err != nil {
				nasScreen.note = nasErrorText(src, err)
				return
			}
			nasScreen.note = pluralizeCount(len(names), "folder", "folders")
			nasScreen.mapFolders = names
		})
	}()
}

// nasMapFolderOf is the folder a Consoles row is about.
func nasMapFolderOf(id string) (string, bool) {
	if id == "map." {
		return ".", true
	}
	if !strings.HasPrefix(id, "map") || id == "mapdone" {
		return "", false
	}
	i, err := strconv.Atoi(strings.TrimPrefix(id, "map"))
	if err != nil || i < 0 || i >= len(nasScreen.mapFolders) {
		return "", false
	}
	return nasScreen.mapFolders[i], true
}

// nasMapCycle steps a folder's console: from its name, then every console,
// then "Not games".
func nasMapCycle(folder string, step int) {
	choices := append([]string{""}, nasSystemChoices()...)
	choices = append(choices, nasSkipMapping)
	cur := 0
	if v, ok := nasScreen.edit.Map[folder]; ok {
		for i, c := range choices {
			if c == v {
				cur = i
			}
		}
	}
	next := choices[(cur+step+len(choices))%len(choices)]
	nasMapSet(folder, next)
	PlayNavSound()
}

// nasMapSet assigns a folder ("" goes back to reading its name).
func nasMapSet(folder, value string) {
	m := map[string]string{}
	for k, v := range nasScreen.edit.Map {
		m[k] = v
	}
	if value == "" {
		delete(m, folder)
	} else {
		m[folder] = value
	}
	if len(m) == 0 {
		m = nil
	}
	nasScreen.edit.Map = m
	nasScreen.dirty = true
}

func nasActivateMap(id string) {
	if id == "mapdone" {
		nasScreen.view = nasViewEdit
		nasScreen.note = ""
		nasScreen.nav.setFocus("save")
		return
	}
	folder, ok := nasMapFolderOf(id)
	if !ok {
		return
	}
	tbxOpenPrompt("Console in "+folder, "Like GBA, SNES, PS or none", "", func(text string) {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "":
			return
		case "none", "no", "-":
			nasMapSet(folder, nasSkipMapping)
			return
		case "auto":
			nasMapSet(folder, "")
			return
		}
		sys, ok := nasSystemFromTyped(text)
		if !ok {
			ShowToast("No console called "+text+" — try its folder name (GBA, SFC, PS)", ToastKindWarning)
			return
		}
		nasMapSet(folder, sys)
	})
}

// ── The password keyboard ────────────────────────────────────────────────────

// nasOpenPassKeyboard opens the masked keyboard for the password. It starts
// empty: a saved password is never shown.
func nasOpenPassKeyboard() {
	chatInputActive = false
	unitInputActive = false
	vkTarget = nasPassVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	nasPassBuffer = ""
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// nasPassSubmit takes the typed password (vkSubmit, after the keyboard
// closed). An empty submit keeps the stored one.
func nasPassSubmit(text string) {
	if text == "" || len(text) > nasFieldMax {
		return
	}
	nasScreen.pass, nasScreen.passChanged, nasScreen.dirty = text, true, true
	nasScreen.note = ""
}
