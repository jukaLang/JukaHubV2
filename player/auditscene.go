package main

// auditscene.go — the Audit toolkit screen.
//
// The catalog of secaudit.go, sorted by what this device can do with it: the
// tools already here, the ones that install for this system, then the ones
// that cannot run here with the reason why. Detection (PATH walks and file
// checks) runs off the render thread when the screen opens. A on a tool opens
// its sheet: how it installs on this system and how that download is checked,
// any caveat, and the actions — Install, Open (a desktop tool starts in its own
// window), Terminal (a command-line tool's help command typed into the
// Terminal), Update, Remove, or the official download page.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// auditStatus is one tool as detected on this device.
type auditStatus struct {
	path   string
	ours   bool
	interp []string
	hasInt bool
}

// auditRowModel is one prepared row.
type auditRowModel struct {
	id   string
	spec tbxRowSpec
	gui  bool
}

// auditModel is what the screen draws.
type auditModel struct {
	builtAt     time.Time
	header      UIPageHeader
	platform    auditPlatform
	ready       []auditRowModel
	installable []auditRowModel
	unavailable []auditRowModel
	installHead string
	awayHead    string
}

// auditScreenState is the screen's state.
type auditScreenState struct {
	visit   tbxVisit
	nav     tbxNav
	model   auditModel
	status  map[string]auditStatus
	loading bool
	busy    map[string]string
	token   int
}

var auditScreen auditScreenState

// auditOpenScreen opens the Audit toolkit. Cross-area contract: the Tools hub
// calls it.
func auditOpenScreen(config *Config) {
	tbxOpenScene(config, auditSceneName)
}

type auditScene struct{ sceneBase }

func (auditScene) render(r *sdl.Renderer, c *Config) bool {
	auditRenderScreen(r, c)
	return true
}

func (auditScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), auditSceneAction)
}

func (auditScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), auditSceneAction)
}

func init() { registerScene(auditScene{sceneBase{name: auditSceneName}}) }

// auditDetectAll resolves every tool. It walks PATH and stats files, so it runs
// on a goroutine.
func auditDetectAll() map[string]auditStatus {
	secAuditForget()
	platform := auditCurrentPlatform()
	out := make(map[string]auditStatus)
	for _, t := range secAuditCatalog() {
		st := auditStatus{}
		if path, ok := secAuditResolve(t); ok {
			st.path = path
			if rec, ok := auditRecordFor(t.ID); ok && rec.Path == path {
				st.ours = true
			}
		}
		if r := t.Recipe(platform); r.Kind == auditKindScript {
			st.interp, st.hasInt = auditInterpreter(r.Needs)
		}
		out[t.ID] = st
	}
	return out
}

// auditRefresh re-detects in the background and rebuilds the rows.
func auditRefresh() {
	auditScreen.loading = true
	go func() {
		st := auditDetectAll()
		tbxPost(func() {
			auditScreen.status = st
			auditScreen.loading = false
			auditScreen.model.builtAt = time.Time{}
		})
	}()
}

func auditScreenEnter() {
	auditScreen.nav.scroll = 0
	auditScreen.model.builtAt = time.Time{}
	auditScreen.loading = true
	runSceneTask(func(ctx context.Context) {
		st := auditDetectAll()
		if ctx.Err() != nil {
			return
		}
		tbxPost(func() {
			auditScreen.status = st
			auditScreen.loading = false
			auditScreen.model.builtAt = time.Time{}
		})
	})
}

// ── Model ────────────────────────────────────────────────────────────────

func auditBuildModel(now time.Time) auditModel {
	m := auditModel{builtAt: now, platform: auditCurrentPlatform()}
	label := auditPlatformLabel(m.platform)
	m.header = tbxHeader("Audit toolkit", "Security tools for systems you own or may test", "audit", label, ColorTextTertiary())
	m.installHead = "Install on " + label
	m.awayHead = "Not available on this device"
	if m.platform == auditPlatformWindows {
		m.awayHead = "Not available on Windows"
	}
	for _, t := range secAuditCatalog() {
		r := t.Recipe(m.platform)
		st, known := auditScreen.status[t.ID]
		icon := "terminal"
		if t.GUI() {
			icon = "shield"
		}
		spec := tbxRowSpec{icon: icon, title: t.Name, subtitle: t.Category + " · " + t.Summary}
		row := auditRowModel{id: t.ID, spec: spec, gui: t.GUI()}
		if busy := auditScreen.busy[t.ID]; busy != "" {
			row.spec.pill, row.spec.tone = busy, tbxToneAccent
		}
		switch {
		case !known && auditScreen.loading:
			if row.spec.pill == "" {
				row.spec.pill, row.spec.tone = "Checking…", tbxToneNeutral
			}
			m.installable = append(m.installable, row)
		case st.path != "":
			if row.spec.pill == "" {
				row.spec.pill, row.spec.tone = "Installed", tbxToneGood
			}
			if rec, ok := auditRecordFor(t.ID); ok && st.ours && rec.Version != "" && !strings.HasPrefix(rec.Version, "sha256") {
				row.spec.note = rec.Version
			}
			m.ready = append(m.ready, row)
		case r.Kind == auditKindNone:
			row.spec.subtitle = r.Reason
			row.spec.dim = true
			if row.spec.pill == "" {
				row.spec.pill, row.spec.tone = "Not available", tbxToneNeutral
			}
			m.unavailable = append(m.unavailable, row)
		default:
			if row.spec.pill == "" {
				switch {
				case r.Kind == auditKindScript && !st.hasInt:
					row.spec.pill, row.spec.tone = "Needs "+auditInterpreterLabel(r.Needs), tbxToneWarn
				case r.Kind == auditKindManual:
					row.spec.pill, row.spec.tone = "Download page", tbxToneNeutral
				default:
					row.spec.pill, row.spec.tone = "Install", tbxToneAccent
				}
			}
			m.installable = append(m.installable, row)
		}
	}
	return m
}

// ── Render ───────────────────────────────────────────────────────────────

func auditRenderScreen(renderer *sdl.Renderer, config *Config) {
	if auditScreen.visit.begin() {
		auditScreenEnter()
	}
	now := time.Now()
	if now.Sub(auditScreen.model.builtAt) > 2*time.Second {
		auditScreen.model = auditBuildModel(now)
	}
	m := &auditScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &auditScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	group := func(title, icon string, rows []auditRowModel) {
		if len(rows) == 0 {
			return
		}
		if y > 0 {
			y += 14
		}
		tbxDrawSection(renderer, config, title, icon, x, top+y, w)
		y += tbxSectionHeight
		for _, r := range rows {
			rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
			screen := rect
			screen.Y += top
			tbxDrawRow(renderer, config, fonts, screen, r.spec, nav.focused(r.id))
			nav.add(r.id, rect, row, r.spec.dim)
			row++
			y += rowH + 8
		}
	}
	group("Ready on this device", "check", m.ready)
	group(m.installHead, "download", m.installable)
	group(m.awayHead, "info", m.unavailable)
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func auditSceneAction(config *Config, act tbxAction) bool {
	nav := &auditScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus != "" {
			PlayActivateSound()
			auditOpenToolSheet(config, nav.focus)
		}
		return true
	case tbxActX, tbxActY:
		if t, ok := secAuditFind(nav.focus); ok {
			if st := auditScreen.status[t.ID]; st.path != "" {
				auditLaunch(config, t, st.path)
			}
		}
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func auditSceneClick(mx, my int32, config *Config) bool {
	it, ok := auditScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	auditScreen.nav.focus = it.id
	PlayActivateSound()
	auditOpenToolSheet(config, it.id)
	return true
}

func auditSceneHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Details"}}
	// Asked every frame: the "Ready" rows already say what is installed and
	// how it opens, so the catalog is not rebuilt here.
	for _, r := range auditScreen.model.ready {
		if r.id == auditScreen.nav.focus {
			label := "Terminal"
			if r.gui {
				label = "Open"
			}
			hints = append(hints, FooterHint{Button: "X", Label: label})
			break
		}
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Sheet ────────────────────────────────────────────────────────────────

// auditRecipeText says, in plain words, how the tool gets onto this system and
// how the download is checked.
func auditRecipeText(t secAuditTool, r auditRecipe, st auditStatus) string {
	switch r.Kind {
	case auditKindWinget:
		return "Installs " + r.WingetID + " with winget, the Windows package manager. winget downloads the vendor's own installer and checks it against the SHA-256 in its package manifest. Windows may ask for permission while it installs."
	case auditKindGitHub:
		return "Downloads the latest release of github.com/" + r.Repo + " built for this system and checks it against the SHA-256 list the project publishes with the release."
	case auditKindArchive:
		host := r.URL
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		return "Downloads the project's official portable build from " + host + " and unpacks it into JukaHub's tools folder. The project publishes no machine-readable checksum, so HTTPS from its own server is the check."
	case auditKindScript:
		text := "Downloads the current source from " + strings.TrimSuffix(strings.TrimPrefix(r.URL, "https://"), "/archive/refs/heads/master.zip") +
			" and runs it with " + auditInterpreterLabel(r.Needs) + "."
		if st.hasInt && len(st.interp) > 0 {
			text += " " + auditInterpreterLabel(r.Needs) + " was found at " + st.interp[0] + "."
		} else {
			text += " " + auditInterpreterLabel(r.Needs) + " is not installed on this system."
		}
		return text
	case auditKindManual:
		text := "Install it from the official page: " + r.Page
		if r.Hint != "" {
			text += "\n\nOr run: " + r.Hint
		}
		return text
	}
	return r.Reason
}

// auditOpenToolSheet shows one tool and the actions that apply here.
func auditOpenToolSheet(config *Config, id string) {
	t, ok := secAuditFind(id)
	if !ok {
		return
	}
	platform := auditCurrentPlatform()
	r := t.Recipe(platform)
	st := auditScreen.status[id]
	var body strings.Builder
	body.WriteString(t.Summary + ".\n\n")
	if st.path != "" {
		body.WriteString("## On this device\n")
		body.WriteString("Installed at " + st.path + ".\n")
		if st.ours {
			if rec, ok := auditRecordFor(id); ok && rec.Version != "" && !strings.HasPrefix(rec.Version, "sha256") {
				body.WriteString("Version " + rec.Version + ", installed by JukaHub.\n")
			} else {
				body.WriteString("Installed by JukaHub.\n")
			}
		} else {
			body.WriteString("Installed outside JukaHub — update or remove it the way it was installed.\n")
		}
		body.WriteString("\n")
	}
	if st.path == "" || r.Kind == auditKindWinget {
		heading := "How it installs here"
		if r.Kind == auditKindNone {
			heading = "Why it is not available"
		}
		body.WriteString("## " + heading + "\n" + auditRecipeText(t, r, st) + "\n\n")
	}
	if r.Note != "" {
		body.WriteString("## Good to know\n" + r.Note + "\n\n")
	}
	body.WriteString("## Use\n")
	if t.SendsTraffic {
		body.WriteString("• Only test systems you own or have written permission to test.\n")
	}
	if t.GUI() {
		body.WriteString("• Opens in its own window.\n")
	} else {
		body.WriteString("• Opens in the Terminal with its help command typed in; nothing runs until you press Enter.\n")
	}

	var buttons []tbxSheetButton
	busy := auditScreen.busy[id] != ""
	switch {
	case busy:
		buttons = []tbxSheetButton{{label: "Close", primary: true}}
	case st.path != "":
		canManage := st.ours || r.Kind == auditKindWinget
		if canManage {
			buttons = append(buttons,
				tbxSheetButton{label: "Remove", run: func() { auditConfirmRemove(config, t, r) }},
				tbxSheetButton{label: "Update", run: func() { auditRunInstall(config, t, r, true) }},
			)
		}
		if !t.GUI() {
			buttons = append(buttons, tbxSheetButton{label: "Version", run: func() { auditCheckVersion(config, t) }})
		}
		openLabel := "Terminal"
		if t.GUI() {
			openLabel = "Open"
		}
		path := st.path
		buttons = append(buttons, tbxSheetButton{label: openLabel, primary: true, run: func() { auditLaunch(config, t, path) }})
	case r.Kind == auditKindNone:
		buttons = []tbxSheetButton{{label: "Close", primary: true}}
	case r.Kind == auditKindManual:
		buttons = append(buttons, tbxSheetButton{label: "Close"})
		if r.Hint != "" {
			hint := r.Hint
			buttons = append(buttons, tbxSheetButton{label: "Copy command", run: func() {
				if err := copyTextToClipboard(hint, "Audit toolkit"); err != nil {
					ShowToast("Could not copy the command", ToastKindError)
					return
				}
				ShowToast("Copied: "+hint, ToastKindSuccess)
			}})
		}
		if r.Page != "" {
			page := r.Page
			buttons = append(buttons, tbxSheetButton{label: "Download page", primary: true, run: func() { auditOpenPage(config, t.Name, page) }})
		}
	case r.Kind == auditKindScript && !st.hasInt:
		buttons = append(buttons, tbxSheetButton{label: "Close"})
		if platform == auditPlatformWindows {
			needs := r.Needs
			buttons = append(buttons, tbxSheetButton{label: "Install " + auditInterpreterLabel(needs), primary: true, run: func() {
				auditInstallInterpreter(config, needs)
			}})
		} else {
			body.WriteString("\n## Missing " + auditInterpreterLabel(r.Needs) + "\nThis firmware does not include " +
				auditInterpreterLabel(r.Needs) + ", so " + t.Name + " cannot run here.\n")
		}
	default:
		buttons = append(buttons, tbxSheetButton{label: "Close"})
		if r.Page != "" && platform == auditPlatformWindows {
			page := r.Page
			buttons = append(buttons, tbxSheetButton{label: "Download page", run: func() { auditOpenPage(config, t.Name, page) }})
		}
		buttons = append(buttons, tbxSheetButton{label: "Install", primary: true, run: func() { auditRunInstall(config, t, r, false) }})
	}
	subtitle := t.Category
	switch {
	case st.path != "":
		subtitle += " · installed"
	case r.Kind == auditKindNone:
		subtitle += " · not available here"
	}
	icon := "terminal"
	if t.GUI() {
		icon = "shield"
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    t.Name,
		subtitle: subtitle,
		icon:     icon,
		body:     body.String(),
		buttons:  buttons,
		focus:    len(buttons) - 1,
	})
}

// ── Actions ──────────────────────────────────────────────────────────────

// auditRunInstall installs (or updates) a tool on a goroutine and reports.
func auditRunInstall(config *Config, t secAuditTool, r auditRecipe, update bool) {
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if auditScreen.busy == nil {
		auditScreen.busy = map[string]string{}
	}
	if auditScreen.busy[t.ID] != "" {
		return
	}
	label := "Installing…"
	if update {
		label = "Updating…"
	}
	auditScreen.busy[t.ID] = label
	auditScreen.model.builtAt = time.Time{}
	if r.Kind == auditKindWinget {
		ShowToast("Installing "+t.Name+" with winget — Windows may ask for permission", ToastKindInfo)
	}
	go func() {
		var report string
		var err error
		if update {
			report, err = auditUpdate(t, r)
		} else {
			report, err = auditInstall(t, r)
		}
		tbxPost(func() {
			delete(auditScreen.busy, t.ID)
			auditRefresh()
			if err != nil {
				msg := plainNetworkError(err)
				ShowToast(t.Name+": "+msg, ToastKindError)
				if strings.TrimSpace(report) != "" && tbxCurrentScene(config) == auditSceneName {
					auditShowOutput(config, t.Name+" — install failed", report+"\n\n"+msg)
				}
				return
			}
			PlaySuccessSound()
			ShowToast(t.Name+" is ready", ToastKindSuccess)
		})
	}()
}

// auditInstallInterpreter installs Python or Perl with winget (Windows).
func auditInstallInterpreter(config *Config, needs string) {
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	label := auditInterpreterLabel(needs)
	ShowToast("Installing "+label+" with winget…", ToastKindInfo)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		out, err := auditWingetRun(ctx, "install", "--id", auditInterpreterWinget(needs), "--exact",
			"--accept-source-agreements", "--accept-package-agreements")
		tbxPost(func() {
			auditRefresh()
			if err != nil {
				ShowToast(label+": "+plainNetworkError(err), ToastKindError)
				if strings.TrimSpace(out) != "" && tbxCurrentScene(config) == auditSceneName {
					auditShowOutput(config, label+" — install failed", out)
				}
				return
			}
			PlaySuccessSound()
			ShowToast(label+" installed — the script tools can be installed now", ToastKindSuccess)
		})
	}()
}

// auditConfirmRemove asks before removing a tool.
func auditConfirmRemove(config *Config, t secAuditTool, r auditRecipe) {
	body := t.Name + " is deleted from JukaHub's tools folder."
	if r.Kind == auditKindWinget {
		body = "winget runs " + t.Name + "'s own uninstaller. Windows may ask for permission."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Remove " + t.Name + "?",
		subtitle: "You can install it again at any time",
		icon:     "trash",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Remove", primary: true, run: func() {
				if auditScreen.busy == nil {
					auditScreen.busy = map[string]string{}
				}
				auditScreen.busy[t.ID] = "Removing…"
				auditScreen.model.builtAt = time.Time{}
				go func() {
					_, err := auditRemove(t, r)
					tbxPost(func() {
						delete(auditScreen.busy, t.ID)
						auditRefresh()
						if err != nil {
							ShowToast(t.Name+": "+firstLine(err.Error()), ToastKindError)
							return
						}
						PlayBackSound()
						ShowToast(t.Name+" removed", ToastKindInfo)
					})
				}()
			}},
		},
	})
}

// auditLaunch opens a tool: a desktop tool in its own window, a command-line
// tool in the Terminal with its help command typed in. A tool that talks to
// other hosts asks for the one-time acknowledgement first.
func auditLaunch(config *Config, t secAuditTool, path string) {
	if t.SendsTraffic && !toolboxAuthorizedUseAccepted(config) {
		tbxScanConsent(config, t.Name, func() { auditLaunch(config, t, path) })
		return
	}
	if t.GUI() {
		if _, err := secAuditOpen(path); err != nil {
			ShowToast("Could not open "+t.Name+": "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlaySuccessSound()
		ShowToast("Opened "+t.Name, ToastKindSuccess)
		return
	}
	PlayActivateSound()
	termOpenWithCommand(config, auditTerminalCommand(t, path))
}

// auditCheckVersion runs the tool's own version report and shows it.
func auditCheckVersion(config *Config, t secAuditTool) {
	auditScreen.token++
	token := "audit-version-" + strconv.Itoa(auditScreen.token)
	tbxOpenSheet(config, tbxSheetSpec{
		token:    token,
		title:    t.Name,
		subtitle: "Version report",
		icon:     "terminal",
		body:     "Asking " + t.Name + " for its version…",
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
	id := t.ID
	go func() {
		out, err := secAuditProbe(id, powerTimeoutScript)
		text := strings.TrimSpace(out)
		if err != nil && text == "" {
			text = "Error: " + err.Error()
		}
		if text == "" {
			text = "(no output)"
		}
		tbxPost(func() { tbxUpdateSheet(token, text, nil) })
	}()
}

// auditShowOutput shows a command's output in a tall sheet.
func auditShowOutput(config *Config, title, text string) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:   title,
		icon:    "terminal",
		body:    text,
		tall:    true,
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
	})
}

// auditOpenPage opens a download page: in the browser on a desktop, as a QR
// code for a phone on the handheld (it has no desktop browser).
func auditOpenPage(config *Config, name, page string) {
	if auditCurrentPlatform() != auditPlatformHandheld {
		if err := openExternalURL(page); err == nil {
			ShowToast("Opened the "+name+" download page", ToastKindInfo)
			return
		}
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    name,
		subtitle: "Official download page",
		icon:     "globe",
		body:     "Scan the code with a phone to open the page:\n\n" + page,
		qr:       tbxQRModules(page),
		qrLabel:  "Tap it or press Y to enlarge",
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
}
