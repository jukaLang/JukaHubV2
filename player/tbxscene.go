package main

// tbxscene.go — the Toolbox screen.
//
// The command-line tools of toolbox.go, as a list that loads its state on the
// way in: installed tools first with their version, a pill when a newer
// release exists, then the tools still to get. A on a tool opens its sheet —
// what it is, where it comes from, whether its download was checked against
// the project's published SHA-256 — with the actions that apply: Install, Run
// (output shown in a sheet), Arguments (typed on the keyboard), Terminal (the
// command typed into the Terminal for interactive use), Update and Remove.
//
// Installs and runs happen on goroutines; the install card in the status strip
// shows the progress, and the row says "Installing…" until it settles.

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// tbxToolRow is one prepared tool row.
type tbxToolRow struct {
	id        string
	spec      tbxRowSpec
	installed bool
}

// tbxModel is what the screen draws.
type tbxModel struct {
	builtAt   time.Time
	header    UIPageHeader
	installed []tbxToolRow
	available []tbxToolRow
	updates   int
}

// tbxScreenState is the Toolbox screen's state.
type tbxScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model tbxModel
	// busy names what is happening to a tool ("Installing…") while it runs.
	busy     map[string]string
	runToken int
}

var tbxScreen tbxScreenState

// tbxOpenScreen opens the Toolbox. Cross-area contract: the Tools hub calls it.
func tbxOpenScreen(config *Config) {
	tbxOpenScene(config, tbxSceneName)
}

type tbxToolboxScene struct{ sceneBase }

func (tbxToolboxScene) render(r *sdl.Renderer, c *Config) bool {
	tbxRenderScreen(r, c)
	return true
}

func (tbxToolboxScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), tbxToolboxAction)
}

func (tbxToolboxScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), tbxToolboxAction)
}

func init() { registerScene(tbxToolboxScene{sceneBase{name: tbxSceneName}}) }

// tbxScreenEnter runs once per visit: the install state is re-read (a tool may
// have been copied in or removed meanwhile) and, online, the update check runs
// in the background for the installed tools.
func tbxScreenEnter() {
	tbxScreen.model.builtAt = time.Time{}
	tbxScreen.nav.scroll = 0
	if tbxUpdateCheckEnabled && IsOnline() {
		runSceneTask(toolboxCheckUpdates)
	}
}

// tbxUpdateCheckEnabled is a test seam: render tests never call GitHub.
var tbxUpdateCheckEnabled = true

// ── Model ────────────────────────────────────────────────────────────────

func tbxBuildModel(now time.Time) tbxModel {
	m := tbxModel{builtAt: now}
	for _, t := range toolboxCatalog() {
		icon := "terminal"
		if t.NetworkScan {
			icon = "radar"
		}
		row := tbxToolRow{id: t.ID, spec: tbxRowSpec{icon: icon, title: t.Name, subtitle: toolboxRowDetail(t)}}
		busy := tbxScreen.busy[t.ID]
		path := toolboxInstalled(t)
		row.installed = path != ""
		switch {
		case busy != "":
			row.spec.pill, row.spec.tone = busy, tbxToneAccent
		case row.installed:
			if latest := toolboxUpdateAvailable(t.ID); latest != "" {
				row.spec.pill, row.spec.tone = "Update · "+latest, tbxToneWarn
				m.updates++
			} else {
				row.spec.pill, row.spec.tone = "Installed", tbxToneGood
			}
			if rec, ok := toolboxRecordFor(t.ID); ok && rec.Tag != "" {
				row.spec.note = rec.Tag
			}
		default:
			if _, err := toolboxAssetPatterns(t, runtime.GOOS, runtime.GOARCH); err != nil {
				row.spec.pill, row.spec.tone = "Not for this device", tbxToneNeutral
				row.spec.dim = true
			} else {
				row.spec.pill, row.spec.tone = "Install", tbxToneAccent
			}
		}
		if row.installed {
			m.installed = append(m.installed, row)
		} else {
			m.available = append(m.available, row)
		}
	}
	status := fmt.Sprintf("%d of %d installed", len(m.installed), len(m.installed)+len(m.available))
	tone := ColorTextTertiary()
	if m.updates > 0 {
		status += fmt.Sprintf(" · %d update", m.updates)
		if m.updates > 1 {
			status += "s"
		}
		tone = ToastWarnInk()
	}
	m.header = tbxHeader("Toolbox", "Command-line tools from their official releases", "toolbox", status, tone)
	return m
}

// ── Render ───────────────────────────────────────────────────────────────

func tbxRenderScreen(renderer *sdl.Renderer, config *Config) {
	if tbxScreen.visit.begin() {
		tbxScreenEnter()
	}
	now := time.Now()
	if now.Sub(tbxScreen.model.builtAt) > 2*time.Second {
		tbxScreen.model = tbxBuildModel(now)
	}
	m := &tbxScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &tbxScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	drawGroup := func(title, icon string, rows []tbxToolRow) {
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
	drawGroup("Installed", "check", m.installed)
	drawGroup("Get more tools", "download", m.available)
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func tbxToolboxAction(config *Config, act tbxAction) bool {
	nav := &tbxScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus != "" {
			PlayActivateSound()
			tbxOpenToolSheet(config, nav.focus)
		}
		return true
	case tbxActX:
		if t, ok := toolboxFind(nav.focus); ok && toolboxInstalled(t) != "" {
			PlayActivateSound()
			vkOpenToolArgs(t.ID)
		}
		return true
	case tbxActY:
		if t, ok := toolboxFind(nav.focus); ok && toolboxInstalled(t) != "" {
			tbxOpenInTerminal(config, t)
		}
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func tbxToolboxClick(mx, my int32, config *Config) bool {
	it, ok := tbxScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	tbxScreen.nav.focus = it.id
	PlayActivateSound()
	tbxOpenToolSheet(config, it.id)
	return true
}

func tbxToolboxHints() []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Details"}}
	// The footer is asked every frame: the model already knows what is
	// installed, so no catalog is rebuilt and no file is checked here.
	for _, r := range tbxScreen.model.installed {
		if r.id == tbxScreen.nav.focus {
			hints = append(hints, FooterHint{Button: "X", Label: "Run with…"}, FooterHint{Button: "Y", Label: "Terminal"})
			break
		}
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Sheets and actions ───────────────────────────────────────────────────

// tbxOpenToolSheet shows one tool: what it is, where it comes from, and the
// actions that apply to its state.
func tbxOpenToolSheet(config *Config, id string) {
	t, ok := toolboxFind(id)
	if !ok {
		return
	}
	path := toolboxInstalled(t)
	rec, hasRec := toolboxRecordFor(id)
	var body strings.Builder
	body.WriteString(t.Summary + ".\n\n")
	body.WriteString("## Source\n")
	body.WriteString("Official releases of github.com/" + t.Repo + ", downloaded over HTTPS. Licence: " + t.Licence + ".\n\n")
	body.WriteString("## Verification\n")
	switch {
	case path == "":
		body.WriteString("The download is checked against the SHA-256 the project publishes with the release when it publishes one; otherwise TLS and the approved-repository list are the check.\n\n")
	case hasRec && rec.Verified:
		body.WriteString("Matched the project's published SHA-256 when it was installed.\n\n")
	case hasRec:
		body.WriteString("The release published no checksum for this file, so it was checked by TLS and the approved-repository list only.\n\n")
	default:
		body.WriteString("Installed before JukaHub recorded checksums, or copied in by hand. Update it to get a verified copy.\n\n")
	}
	if path != "" {
		body.WriteString("## Installed\n")
		version := "version unknown"
		if hasRec && rec.Tag != "" {
			version = rec.Tag
		}
		body.WriteString(version + " · " + path + "\n")
		if latest := toolboxUpdateAvailable(id); latest != "" {
			body.WriteString("Update available: " + latest + "\n")
		}
		args := toolboxArgsFor(id)
		line, _ := toolboxCommandLine(t, args)
		body.WriteString("\n## Run\n" + line + "\n")
	}
	if t.NetworkScan {
		body.WriteString("\n## Scans other hosts\n" +
			"Only scan networks you own or have permission to test. The first run asks you to confirm this once.\n")
	}

	var buttons []tbxSheetButton
	if busy := tbxScreen.busy[id]; busy != "" {
		buttons = []tbxSheetButton{{label: "Close", primary: true}}
	} else if path == "" {
		if _, err := toolboxAssetPatterns(t, runtime.GOOS, runtime.GOARCH); err != nil {
			body.WriteString("\n## Not available here\n" + t.Name + " publishes no build for this device.\n")
			buttons = []tbxSheetButton{{label: "Close", primary: true}}
		} else {
			buttons = []tbxSheetButton{
				{label: "Close"},
				{label: "Install", primary: true, run: func() { tbxInstallFlow(config, id, false) }},
			}
		}
	} else {
		buttons = []tbxSheetButton{
			{label: "Remove", run: func() { tbxConfirmRemove(config, id) }},
		}
		updateLabel := "Reinstall"
		if toolboxUpdateAvailable(id) != "" {
			updateLabel = "Update"
		}
		buttons = append(buttons,
			tbxSheetButton{label: updateLabel, run: func() { tbxInstallFlow(config, id, true) }},
			tbxSheetButton{label: "Terminal", run: func() { tbxOpenInTerminal(config, t) }},
			tbxSheetButton{label: "Run with…", run: func() { vkOpenToolArgs(id) }},
			tbxSheetButton{label: "Run", primary: true, run: func() { tbxRunTool(config, id) }},
		)
	}
	subtitle := "Not installed"
	if path != "" {
		subtitle = "Installed"
		if hasRec && rec.Tag != "" {
			subtitle = "Installed · " + rec.Tag
		}
	}
	icon := "terminal"
	if t.NetworkScan {
		icon = "radar"
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

// tbxInstallFlow installs (or, with update, replaces) a tool off the render
// thread.
func tbxInstallFlow(config *Config, id string, update bool) {
	t, ok := toolboxFind(id)
	if !ok {
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if tbxScreen.busy == nil {
		tbxScreen.busy = map[string]string{}
	}
	if tbxScreen.busy[id] != "" {
		return
	}
	label := "Installing…"
	if update {
		label = "Updating…"
	}
	tbxScreen.busy[id] = label
	tbxScreen.model.builtAt = time.Time{}
	go func() {
		var err error
		if update {
			err = toolboxUpdate(id, config)
		} else {
			err = toolboxInstall(id, config)
		}
		tbxPost(func() {
			delete(tbxScreen.busy, id)
			tbxScreen.model.builtAt = time.Time{}
			if err != nil {
				ShowToast(t.Name+": "+plainNetworkError(err), ToastKindError)
				return
			}
			PlaySuccessSound()
			if update {
				ShowToast(t.Name+" is up to date", ToastKindSuccess)
			} else {
				ShowToast(t.Name+" installed — press A to run it", ToastKindSuccess)
			}
		})
	}()
}

// tbxConfirmRemove asks before deleting a tool.
func tbxConfirmRemove(config *Config, id string) {
	t, ok := toolboxFind(id)
	if !ok {
		return
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Remove " + t.Name + "?",
		subtitle: "You can install it again at any time",
		icon:     "trash",
		body:     "The program is deleted from the tools folder. Nothing else on the card is touched.",
		buttons: []tbxSheetButton{
			{label: "Keep"},
			{label: "Remove", primary: true, run: func() {
				if err := toolboxRemove(id); err != nil {
					ShowToast(err.Error(), ToastKindError)
					return
				}
				tbxScreen.model.builtAt = time.Time{}
				PlayBackSound()
				ShowToast(t.Name+" removed", ToastKindInfo)
			}},
		},
	})
}

// tbxNeedsConsent reports whether starting t must ask the one-time
// authorized-use question first: only scanners, and only until it is answered.
func tbxNeedsConsent(t toolboxTool, config *Config) bool {
	return t.NetworkScan && !toolboxAuthorizedUseAccepted(config)
}

// tbxScanConsent asks, once, before the first run of a tool that sends traffic
// to other hosts; then runs next.
func tbxScanConsent(config *Config, name string, next func()) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Before you scan",
		subtitle: name + " sends traffic to other devices",
		icon:     "shield",
		body: "Only scan networks and hosts you own or have written permission to test. " +
			"Scanning other people's devices can be illegal and can disrupt their network.\n\n" +
			"You are asked once; the answer covers every scanning and audit tool on this device.",
		buttons: []tbxSheetButton{
			{label: "Cancel"},
			{label: "I understand", primary: true, run: func() {
				toolboxAcceptAuthorizedUse(config)
				if next != nil {
					next()
				}
			}},
		},
	})
}

// tbxRunTool runs an installed tool with its remembered arguments (or the
// catalog example) and shows the output in a sheet that fills in when the run
// finishes.
func tbxRunTool(config *Config, id string) {
	t, ok := toolboxFind(id)
	if !ok {
		return
	}
	if toolboxInstalled(t) == "" {
		ShowToast(t.Name+" is not installed", ToastKindWarning)
		return
	}
	if tbxNeedsConsent(t, config) {
		tbxScanConsent(config, t.Name, func() { tbxRunTool(config, id) })
		return
	}
	args := toolboxArgsFor(id)
	line, _ := toolboxCommandLine(t, args)
	tbxScreen.runToken++
	token := "toolbox-run-" + strconv.Itoa(tbxScreen.runToken)
	tbxOpenSheet(config, tbxSheetSpec{
		token:    token,
		title:    t.Name,
		subtitle: line,
		icon:     "terminal",
		body:     "Running " + line + " …",
		tall:     true,
		buttons:  []tbxSheetButton{{label: "Close", primary: true}},
	})
	go func() {
		out, err := toolboxRun(id, args, powerTimeoutScript)
		text := strings.TrimRight(out, "\n")
		if err != nil {
			if strings.TrimSpace(text) == "" {
				text = "Error: " + err.Error()
			} else {
				text += "\n\nExit: " + err.Error()
			}
		}
		if strings.TrimSpace(text) == "" {
			text = "(no output)"
		}
		tbxPost(func() {
			tbxUpdateSheet(token, text, []tbxSheetButton{
				{label: "Copy", keepOpen: true, run: func() {
					if err := copyTextToClipboard(text, "Toolbox"); err != nil {
						ShowToast("Could not copy the output", ToastKindError)
						return
					}
					ShowToast("Output copied", ToastKindSuccess)
				}},
				{label: "Run again", run: func() { tbxRunTool(config, id) }},
				{label: "Close", primary: true},
			})
		})
	}()
}

// tbxOpenInTerminal types the tool's command into the Terminal for interactive
// use; nothing runs until Enter.
func tbxOpenInTerminal(config *Config, t toolboxTool) {
	path := toolboxInstalled(t)
	if path == "" {
		ShowToast(t.Name+" is not installed", ToastKindWarning)
		return
	}
	if tbxNeedsConsent(t, config) {
		tbxScanConsent(config, t.Name, func() { tbxOpenInTerminal(config, t) })
		return
	}
	PlayActivateSound()
	termOpenWithCommand(config, toolboxTerminalCommand(t, path, toolboxArgsFor(t.ID)))
}

// powerRunToolFromArgs is where the keyboard's tool-argument field submits to:
// the line was saved by the keyboard, and the run happens on the Toolbox screen
// so its output lands in the output sheet.
func powerRunToolFromArgs(id string) {
	config := appConfig
	if tbxCurrentScene(config) != tbxSceneName {
		tbxOpenScene(config, tbxSceneName)
		return
	}
	tbxRunTool(config, id)
}
