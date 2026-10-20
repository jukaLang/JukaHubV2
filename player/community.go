package main

// community.go — the in-app community hub: release notes shown after an
// update, a prefilled GitHub bug report, and the anonymous telemetry switch.
//
// It is an overlay (like Tools and Quick Settings) rather than a scene so it
// works on every config, including configs that never define an About scene.
// The panel is reachable from Tools → Community & support, and the changelog
// page opens itself once after an update.

import (
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type communityPage int

const (
	communityPageHome communityPage = iota
	communityPageChangelog
)

type communityAction int

const (
	communityActionWhatsNew communityAction = iota
	communityActionReportBug
	communityActionTelemetry
	communityActionClose
)

type communityRow struct {
	label  string
	value  string
	detail string
	hint   string
	action communityAction
}

type communityPanelState struct {
	open   bool
	page   communityPage
	cursor int
	scroll int
}

var communityPanel communityPanelState

// communityRowRects is filled by renderCommunityPanel so clicks map back to
// rows. Render/event thread only.
var communityRowRects []sdl.Rect

var communityPanelRect sdl.Rect

// communityRows builds the hub's landing list. The telemetry row is the only
// place consent can be changed, so it states the default explicitly.
func communityRows(config *Config) []communityRow {
	version := changelogVersionLabel(config)
	detail := "Release notes for this build"
	if version != "" {
		detail = "Release notes for " + version
	}
	telemetryDetail := "Device model and crash counts only — off unless you opt in"
	if telemetryOptedIn() && telemetryEndpointURL() == "" {
		telemetryDetail = "No collector is configured in this build — nothing is sent"
	}
	return []communityRow{
		{
			label:  "What's new",
			value:  "Changelog",
			detail: detail,
			hint:   "A: read",
			action: communityActionWhatsNew,
		},
		{
			label:  "Report a bug",
			value:  "GitHub issue",
			detail: "Opens a prefilled report with redacted diagnostics",
			hint:   "A: open",
			action: communityActionReportBug,
		},
		{
			label:  "Anonymous diagnostics",
			value:  telemetryStatusValue(),
			detail: telemetryDetail,
			hint:   "A: toggle",
			action: communityActionTelemetry,
		},
		{
			label:  "Close",
			value:  "Back to the app",
			hint:   "B",
			action: communityActionClose,
		},
	}
}

// openCommunityPanel shows the hub. Opening the changelog marks it seen so the
// same release does not auto-open on every launch.
func openCommunityPanel(config *Config, page communityPage) {
	if config == nil {
		return
	}
	// Overlays never stack: the hub replaces Tools/Quick Settings.
	toolsPanel.open = false
	qs.open = false
	communityPanel.open = true
	communityPanel.page = page
	communityPanel.cursor = 0
	communityPanel.scroll = 0
	communityRowRects = nil
	communityPanelRect = sdl.Rect{}
	if page == communityPageChangelog {
		startChangelog(config)
	}
}

func closeCommunityPanel() {
	communityPanel.open = false
	PlayBackSound()
}

func communityPanelMove(delta int) {
	rows := communityRows(appConfig)
	if len(rows) == 0 {
		return
	}
	communityPanel.cursor += delta
	if communityPanel.cursor < 0 {
		communityPanel.cursor = len(rows) - 1
	}
	if communityPanel.cursor >= len(rows) {
		communityPanel.cursor = 0
	}
	PlayNavSound()
}

func communityPanelActivate(config *Config) {
	rows := communityRows(config)
	if communityPanel.cursor < 0 || communityPanel.cursor >= len(rows) {
		return
	}
	switch rows[communityPanel.cursor].action {
	case communityActionWhatsNew:
		communityPanel.page = communityPageChangelog
		communityPanel.scroll = 0
		startChangelog(config)
		PlayActivateSound()
	case communityActionReportBug:
		communityPanel.open = false
		reportBugFromUI(config)
	case communityActionTelemetry:
		on := !telemetryOptedIn()
		if err := setTelemetryOptedIn(config, on); err != nil {
			ShowToast("Could not save the telemetry setting", ToastKindError)
			return
		}
		if on {
			ShowToast("Anonymous diagnostics on — device model and crashes only", ToastKindSuccess)
			TelemetryBoot(config)
		} else {
			ShowToast("Anonymous diagnostics off — nothing is sent", ToastKindInfo)
		}
		PlayActivateSound()
	case communityActionClose:
		closeCommunityPanel()
	}
}

// reportBugFromUI writes a diagnostics copy, copies it to the clipboard and
// opens a prefilled GitHub issue. It runs off the render thread because opening
// a browser can block on some handheld desktops.
func reportBugFromUI(config *Config) {
	path, err := ReportBug(config)
	if err != nil {
		ShowToast("Couldn't open a browser — diagnostics copied to the clipboard", ToastKindWarning)
		logPatch("bug report: open failed: %v", err)
		return
	}
	if path != "" {
		ShowToast("GitHub issue opened — diagnostics copied and saved", ToastKindSuccess)
		return
	}
	ShowToast("GitHub issue opened — diagnostics copied", ToastKindSuccess)
}

// ── Bug report ──────────────────────────────────────────────────────────────

const jukaHubIssuesNew = "https://github.com/jukaLang/JukaHubV2/issues/new"

// BugReportURL builds the prefilled issue. The body carries the same redacted
// diagnostics the Patch export writes; no config values, tokens or paths are
// included. The body is trimmed so the whole URL stays inside what browsers
// and GitHub accept.
func BugReportURL(config *Config) string {
	diag := DiagnosticsText(config)
	if len(diag) > 5000 {
		diag = diag[:5000] + "\n… (truncated)\n"
	}
	body := "### What happened?\n" +
		"<!-- Describe the problem and what you expected instead. -->\n\n" +
		"### Steps to reproduce\n" +
		"1. \n2. \n3. \n\n" +
		"### Expected\n\n\n" +
		"### Diagnostics (redacted)\n" +
		"```\n" + strings.TrimRight(diag, "\n") + "\n```\n"
	return jukaHubIssuesNew +
		"?title=" + url.QueryEscape("[Bug] ") +
		"&body=" + url.QueryEscape(body)
}

// ReportBug saves a diagnostics file, copies the report and opens the issue.
func ReportBug(config *Config) (string, error) {
	path, _ := ExportDiagnostics(config)
	_ = sdl.SetClipboardText(DiagnosticsText(config))
	return path, openExternalURL(BugReportURL(config))
}

// openExternalURL hands a URL to the desktop without going through a shell.
func openExternalURL(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 avoids cmd.exe, so a query string full of & and % arrives
		// intact and cannot be parsed as shell syntax.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	case "darwin":
		cmd = exec.Command("open", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ── Input ──────────────────────────────────────────────────────────────────

func handleCommunityKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !communityPanel.open {
		return false
	}
	if communityPanel.page == communityPageChangelog {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			communityPanel.scroll -= 2
			if communityPanel.scroll < 0 {
				communityPanel.scroll = 0
			}
		case sdl.K_DOWN:
			communityPanel.scroll += 2
		case sdl.K_PAGEUP:
			communityPanel.scroll -= 10
			if communityPanel.scroll < 0 {
				communityPanel.scroll = 0
			}
		case sdl.K_PAGEDOWN:
			communityPanel.scroll += 10
		case sdl.K_HOME:
			communityPanel.scroll = 0
		case sdl.K_END:
			communityPanel.scroll = 1 << 20
		case sdl.K_RETURN, sdl.K_SPACE, sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
			communityPanel.page = communityPageHome
			communityPanel.cursor = 0
			communityPanel.scroll = 0
			PlayBackSound()
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		communityPanelMove(-1)
	case sdl.K_DOWN:
		communityPanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		communityPanelActivate(config)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeCommunityPanel()
	}
	return true
}

func ctrlCommunity(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !communityPanel.open {
		return false
	}
	if communityPanel.page == communityPageChangelog {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			communityPanel.scroll -= 2
			if communityPanel.scroll < 0 {
				communityPanel.scroll = 0
			}
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			communityPanel.scroll += 2
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			communityPanel.page = communityPageHome
			communityPanel.cursor = 0
			communityPanel.scroll = 0
			PlayBackSound()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		communityPanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		communityPanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		communityPanelActivate(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		closeCommunityPanel()
	}
	return true
}

func handleCommunityMouse(mx, my int32, config *Config) bool {
	if !communityPanel.open {
		return false
	}
	if communityPanel.page == communityPageChangelog {
		if communityPanelRect.W > 0 && !pointInRect(mx, my, communityPanelRect) {
			closeCommunityPanel()
			return true
		}
		// A click on the notes card returns to the list; scrolling has its own
		// wheel handler.
		communityPanel.page = communityPageHome
		communityPanel.cursor = 0
		communityPanel.scroll = 0
		return true
	}
	for i, r := range communityRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			communityPanel.cursor = i
			communityPanelActivate(config)
			return true
		}
	}
	if communityPanelRect.W > 0 && !pointInRect(mx, my, communityPanelRect) {
		closeCommunityPanel()
	}
	return true
}

func handleCommunityWheel(y int32) bool {
	if !communityPanel.open {
		return false
	}
	if communityPanel.page == communityPageChangelog {
		communityPanel.scroll -= int(y) * 3
		if communityPanel.scroll < 0 {
			communityPanel.scroll = 0
		}
		return true
	}
	return false
}

// ── Render ─────────────────────────────────────────────────────────────────

func communityPanelSize(page communityPage, rowH int32) (int32, int32) {
	panelW := int32(760)
	if panelW > screenWidth-40 {
		panelW = screenWidth - 40
	}
	var panelH int32
	if page == communityPageChangelog {
		panelH = 620
		if panelH > screenHeight-40 {
			panelH = screenHeight - 40
		}
	} else {
		panelH = 86 + int32(len(communityRows(appConfig)))*rowH + 34 + 34
		if panelH > screenHeight-20 {
			panelH = screenHeight - 20
		}
	}
	return panelW, panelH
}

func renderCommunityPanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !communityPanel.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())
	// The row height comes from the fonts. A fixed 76px stacked three lines of
	// 30px text on top of each other on a build whose default small font is
	// taller than 18px, which made every detail line overlap the next label.
	const rowPad = int32(10)
	rowH := 2*rowPad + smallH + 2 + medH + 2 + smallH
	panelW, panelH := communityPanelSize(communityPanel.page, rowH)
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	communityPanelRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	const padX = int32(24)

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 20, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 18, Y: panelY + 1, W: panelW - 36, H: 2})

	title := "Community & support"
	sub := "Updates, feedback and privacy"
	if communityPanel.page == communityPageChangelog {
		title = "What's new"
		sub = changelogSubtitle(config)
	}
	renderText(renderer, config, medium, title, ColorTextPrimary(), panelX+padX, panelY+18)
	renderText(renderer, config, font, ellipsize(font, sub, panelW-2*padX), ColorTextTertiary(), panelX+padX, panelY+20+medH+4)

	if communityPanel.page == communityPageChangelog {
		renderCommunityChangelog(renderer, config, font, panelX, panelY, panelW, panelH, padX, smallH)
		return
	}

	focusColor := installHighlight()
	rows := communityRows(config)
	if communityPanel.cursor >= len(rows) {
		communityPanel.cursor = len(rows) - 1
	}
	if communityPanel.cursor < 0 {
		communityPanel.cursor = 0
	}
	headerH := int32(86)
	rowW := panelW - 2*padX
	communityRowRects = make([]sdl.Rect, len(rows))
	for i, row := range rows {
		y := panelY + headerH + int32(i)*rowH
		r := sdl.Rect{X: panelX + padX, Y: y, W: rowW, H: rowH - 8}
		communityRowRects[i] = r
		focused := i == communityPanel.cursor
		rowBg := WithAlpha(ColorSurfaceRow, 190)
		if focused {
			rowBg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, rowBg)
		if focused {
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 12, 1, focusColor)
		}
		labelY := r.Y + rowPad
		valueY := labelY + smallH + 2
		detailY := valueY + medH + 2
		renderText(renderer, config, font, row.label, ColorTextSecondary(), r.X+16, labelY)
		valueColor := ColorTextPrimary()
		if row.action == communityActionTelemetry {
			if telemetryOptedIn() {
				valueColor = ColorToastSuccess
			} else {
				valueColor = ColorTextTertiary()
			}
		}
		renderText(renderer, config, medium, row.value, valueColor, r.X+16, valueY)
		if row.detail != "" {
			renderText(renderer, config, font, ellipsize(font, row.detail, r.W-180), ColorTextTertiary(), r.X+16, detailY)
		}
		if row.hint != "" {
			hintColor := ColorTextSecondary()
			if focused {
				hintColor = focusColor
			}
			hw, _, _ := font.SizeUTF8(row.hint)
			renderText(renderer, config, font, row.hint, hintColor, r.X+r.W-int32(hw)-16, valueY)
		}
	}

	noteY := panelY + headerH + int32(len(rows))*rowH + 2
	note := "Off by default. Never includes designs, files, links or account data."
	renderText(renderer, config, font, ellipsize(font, note, rowW), ColorTextTertiary(), panelX+padX, noteY)

	footer := "A select    B close"
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
