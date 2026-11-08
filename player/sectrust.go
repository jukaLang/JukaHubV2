package main

// sectrust.go — the Security & trust screen.
//
// One row per thing the app protects or exposes: secrets and the key that
// encrypts them, how downloads and updates are checked, the servers listening
// on the LAN (file servers with their ports, the Phone Companion, a Watch Party
// room), telemetry and bug reports, the browser's saved session and the user
// the app runs as. Every row opens a sheet that says what it means, what the
// state is right now, why it matters, and — where there is one — the action:
// stop a server, rotate the companion PIN, turn telemetry off, open the
// matching screen, or read the section of SECURITY.md that covers it. The
// document itself opens in a reader on the same screen (sectrustdoc.go).
//
// The posture is a summary, not a scanner: every row names the mechanism
// behind it. Facts that need the disk (the package index, the cookie file) are
// read on a goroutine and cached; the rest are reads of process state.

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// securityRow is one posture line: a label, the state, and why it matters.
type securityRow struct {
	// ID is the stable key the screen and the sheets use.
	ID     string
	Label  string
	Value  string
	Detail string
	// Warn marks a state the user can improve (a plaintext secret, the built-in
	// key, a server that is listening).
	Warn bool
}

// sectrustFacts are the posture facts that need the disk.
type sectrustFacts struct {
	at        time.Time
	loaded    bool
	packages  int
	pkgIndex  bool
	cookieLen int64
}

var sectrustFactsCache struct {
	mu       sync.Mutex
	facts    sectrustFacts
	inFlight bool
}

// sectrustReadFacts reads the disk facts. cookiePath is resolved by the caller
// on the main thread.
func sectrustReadFacts(cookiePath string) sectrustFacts {
	f := sectrustFacts{at: time.Now(), loaded: true}
	if repo, err := LoadPackageRepo(); err == nil {
		f.pkgIndex = true
		f.packages = len(repo.Packages)
	}
	if cookiePath != "" {
		if info, err := os.Stat(cookiePath); err == nil {
			f.cookieLen = info.Size()
		}
	}
	return f
}

// sectrustCurrentFacts returns the cached disk facts and refreshes them in the
// background when they are older than a few seconds.
func sectrustCurrentFacts() sectrustFacts {
	sectrustFactsCache.mu.Lock()
	f := sectrustFactsCache.facts
	stale := !f.loaded || time.Since(f.at) > 5*time.Second
	start := stale && !sectrustFactsCache.inFlight
	if start {
		sectrustFactsCache.inFlight = true
	}
	sectrustFactsCache.mu.Unlock()
	if start {
		cookiePath := browserCookiesPath()
		go func() {
			nf := sectrustReadFacts(cookiePath)
			sectrustFactsCache.mu.Lock()
			sectrustFactsCache.facts = nf
			sectrustFactsCache.inFlight = false
			sectrustFactsCache.mu.Unlock()
		}()
	}
	return f
}

// securityPostureRows builds the posture from the live config and the process
// state. It reads no file itself: disk facts come from the background cache.
func securityPostureRows(config *Config) []securityRow {
	rows := make([]securityRow, 0, 13)
	facts := sectrustCurrentFacts()

	// 1. Secrets in the config. SecurityCheck also reports the storage key and
	// the Discord token type; only the plaintext findings belong on this row.
	warnings := (&ConfigValidator{}).SecurityCheck(config)
	plaintext := 0
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "plaintext") {
			plaintext++
		}
	}
	secrets := securityRow{
		ID:     "secrets",
		Label:  "Config secrets",
		Value:  "No plaintext secret",
		Detail: "API keys and tokens are encrypted with AES-256-GCM",
	}
	if n := plaintext; n == 1 {
		secrets.Value = "1 stored in plaintext"
		secrets.Detail = "Set it again with -set-secret, or run the secret CLI"
		secrets.Warn = true
	} else if n > 1 {
		secrets.Value = fmt.Sprintf("%d stored in plaintext", n)
		secrets.Detail = "Each one is readable by anything that reads the config"
		secrets.Warn = true
	}
	rows = append(rows, secrets)

	// 2. Which key encrypts them.
	key := securityRow{
		ID:     "key",
		Label:  "Encryption key",
		Value:  "Built-in default",
		Detail: "Anyone with the binary can derive it; set JUKAHUB_CRYPTO_KEY",
		Warn:   true,
	}
	if os.Getenv("JUKAHUB_CRYPTO_KEY") != "" {
		key.Value = "JUKAHUB_CRYPTO_KEY"
		key.Detail = "Derived with HMAC-SHA256, never written to the config"
		key.Warn = false
	}
	rows = append(rows, key)

	// 3. Where downloads may come from, and how they are checked.
	rows = append(rows, securityRow{
		ID:     "downloads",
		Label:  "Tool downloads",
		Value:  fmt.Sprintf("%d approved repos", len(allowedToolRepos)),
		Detail: "Curated releases only, checked against published SHA-256 when there is one",
	})

	// 4. Packages and patches: they install signed, code-reviewed files.
	packages := "No package index"
	switch {
	case !facts.loaded:
		packages = "Checking…"
	case facts.pkgIndex:
		packages = fmt.Sprintf("%d package(s) available", facts.packages)
	}
	rows = append(rows, securityRow{
		ID:     "packages",
		Label:  "Packages & patches",
		Value:  packages,
		Detail: "Digest-checked, signature-checked, rollback copy kept",
	})

	// 5. The key updates are checked with.
	rows = append(rows, securityRow{
		ID:     "signing",
		Label:  "Update signing",
		Value:  "Ed25519 key built in",
		Detail: "The repository index must carry a valid signature before anything installs",
	})

	// 6. Listening servers are capabilities, not vulnerabilities — say so.
	servers := "None listening"
	warn := false
	if ftpSrv.Running() || sshSrv.Running() {
		names := make([]string, 0, 2)
		if ftpSrv.Running() {
			names = append(names, fmt.Sprintf("FTP :%d", ftpSrv.Port()))
		}
		if sshSrv.Running() {
			names = append(names, fmt.Sprintf("SFTP :%d", sshSrv.Port()))
		}
		servers = strings.Join(names, " · ")
		warn = true
	}
	rows = append(rows, securityRow{
		ID:     "servers",
		Label:  "File servers",
		Value:  servers,
		Detail: "Reachable by any device on this network while running",
		Warn:   warn,
	})

	// 7. The Phone Companion listens whenever there is Wi-Fi.
	companionValue := "Off"
	if companion.srv != nil && companion.srv.Running() {
		companionValue = fmt.Sprintf("On · port %d · PIN", companion.srv.Port())
	}
	rows = append(rows, securityRow{
		ID:     "companion",
		Label:  "Phone Companion",
		Value:  companionValue,
		Detail: "A phone on this Wi-Fi can drive the handheld after entering the on-screen PIN",
	})

	// 8. A Watch Party room this device hosts.
	partyValue := "Not hosting"
	if partyIsHosting() {
		partyValue = fmt.Sprintf("Hosting · port %d", partyServerPort())
	}
	rows = append(rows, securityRow{
		ID:     "party",
		Label:  "Watch Party",
		Value:  partyValue,
		Detail: "A hosted room answers devices on this Wi-Fi that know its code",
	})

	// 9. The browser's own data.
	browser := securityRow{
		ID:     "browser",
		Label:  "Browser session",
		Value:  "No saved session",
		Detail: "The cookie jar lives in the app's data folder",
	}
	if facts.cookieLen > 0 {
		browser.Value = powerHumanSize(facts.cookieLen) + " on the card"
		browser.Detail = "Browser → Forget empties it and deletes the file"
	}
	rows = append(rows, browser)

	// 10. Telemetry is off unless asked for.
	rows = append(rows, securityRow{
		ID:     "telemetry",
		Label:  "Telemetry",
		Value:  telemetryStatusValue(),
		Detail: "Off by default; a single boot/crash ping when enabled",
	})

	// 11. Bug reports go out only when the user sends one.
	reports := "Through your phone"
	if bugrepEndpointURL() != "" {
		reports = "Anonymous collector"
	}
	if n := bugrepOutboxCount(); n > 0 {
		reports += fmt.Sprintf(" · %d queued", n)
	}
	rows = append(rows, securityRow{
		ID:     "reports",
		Label:  "Bug reports",
		Value:  reports,
		Detail: "Only what you review and send; diagnostics are redacted first",
	})

	// 12. The account the app runs as.
	process := "Your user account"
	processDetail := "Tools and plugins run with the same access as JukaHub"
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		process = "root"
		processDetail = "The handheld's firmware starts apps as root; tools and plugins inherit it"
	}
	rows = append(rows, securityRow{
		ID:     "process",
		Label:  "App permissions",
		Value:  process,
		Detail: processDetail,
	})

	// 13. Where the long form is.
	rows = append(rows, securityRow{
		ID:     "doc",
		Label:  "Threat model & details",
		Value:  "SECURITY.md",
		Detail: "What is protected, what is not, and how to report a problem",
	})
	return rows
}

// securityWarningCount is the number of posture rows the user can improve.
func securityWarningCount(config *Config) int {
	n := 0
	for _, row := range securityPostureRows(config) {
		if row.Warn {
			n++
		}
	}
	return n
}

// securityPostureValue is the Tools hub value for this screen, plus the flag
// that turns its dot amber.
func securityPostureValue(config *Config) (string, bool) {
	n := securityWarningCount(config)
	switch n {
	case 0:
		return "No warnings", false
	case 1:
		return "1 thing to review", true
	}
	return fmt.Sprintf("%d things to review", n), true
}

// ── Screen ───────────────────────────────────────────────────────────────

type sectrustRowModel struct {
	id   string
	spec tbxRowSpec
}

type sectrustModel struct {
	builtAt  time.Time
	header   UIPageHeader
	rows     []sectrustRowModel
	posture  []securityRow
	warnings int
	summary  string
	sub      string
}

type sectrustScreenState struct {
	visit tbxVisit
	nav   tbxNav
	model sectrustModel
	wrap  tbxWrapCache
}

var sectrustScreen sectrustScreenState

// sectrustOpenScreen opens Security & trust. Cross-area contract: the Tools hub
// calls it.
func sectrustOpenScreen(config *Config) {
	tbxOpenScene(config, sectrustSceneName)
}

type sectrustScene struct{ sceneBase }

func (sectrustScene) render(r *sdl.Renderer, c *Config) bool {
	if sectrustScreen.visit.begin() {
		// A fresh visit starts on the list, not inside the reader.
		sectrustDoc.open = false
		sectrustScreen.nav.scroll = 0
		sectrustScreen.model.builtAt = time.Time{}
	}
	if sectrustDoc.open {
		sectrustRenderDoc(r, c)
		return true
	}
	sectrustRenderScreen(r, c)
	return true
}

func (sectrustScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), sectrustSceneAction)
}

func (sectrustScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), sectrustSceneAction)
}

func init() { registerScene(sectrustScene{sceneBase{name: sectrustSceneName}}) }

// sectrustActiveNav is the focus list the mouse wheel scrolls: the reader's
// while it is open.
func sectrustActiveNav() *tbxNav {
	if sectrustDoc.open {
		if sectrustDoc.toc {
			return &sectrustDoc.tocNav
		}
		return &sectrustDoc.nav
	}
	return &sectrustScreen.nav
}

func sectrustRowIcon(id string) string {
	switch id {
	case "secrets":
		return "key"
	case "key":
		return "lock"
	case "downloads":
		return "download"
	case "packages":
		return "packages"
	case "signing":
		return "check"
	case "servers":
		return "server"
	case "companion":
		return "phone"
	case "party":
		return "party"
	case "browser":
		return "browser"
	case "telemetry":
		return "sync"
	case "reports":
		return "bug"
	case "process":
		return "user"
	}
	return "shield"
}

func sectrustBuildModel(config *Config, now time.Time) sectrustModel {
	m := sectrustModel{builtAt: now}
	m.posture = securityPostureRows(config)
	for _, r := range m.posture {
		tone := tbxToneGood
		switch {
		case r.Warn:
			tone = tbxToneWarn
			m.warnings++
		case r.ID == "doc" || r.ID == "process" || r.ID == "telemetry" || r.ID == "reports" || r.ID == "party" || r.ID == "companion" || r.ID == "browser":
			tone = tbxToneNeutral
		}
		m.rows = append(m.rows, sectrustRowModel{
			id:   r.ID,
			spec: tbxRowSpec{icon: sectrustRowIcon(r.ID), title: r.Label, subtitle: r.Detail, pill: r.Value, tone: tone},
		})
	}
	status, statusColor := "No warnings", ToastSuccessInk()
	switch m.warnings {
	case 0:
		m.summary = "Nothing needs your attention"
		m.sub = "Every row below explains what it covers. Press A on one for the details."
	case 1:
		status, statusColor = "1 thing to review", ToastWarnInk()
		m.summary = "1 thing to review"
		m.sub = "The amber row has a fix. Press A on it to see what to do."
	default:
		status, statusColor = fmt.Sprintf("%d things to review", m.warnings), ToastWarnInk()
		m.summary = status
		m.sub = "The amber rows have fixes. Press A on one to see what to do."
	}
	m.header = tbxHeader("Security & trust", "What this device protects, and what it exposes", "shield", status, statusColor)
	return m
}

func sectrustRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(sectrustScreen.model.builtAt) > time.Second {
		sectrustScreen.model = sectrustBuildModel(config, now)
	}
	m := &sectrustScreen.model
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &sectrustScreen.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0

	// The summary card: the verdict, what to do about it, and the document.
	heroH := int32(120)
	hero := sdl.Rect{X: x, Y: top + y, W: w, H: heroH}
	UICard(renderer, hero, DefaultUICardStyle())
	badge := int32(56)
	box := drawGlassBadge(renderer, sdl.Rect{X: hero.X + 22, Y: hero.Y + (heroH-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, "shield", box, false)
	label := "Read SECURITY.md"
	bw := tbxButtonWidth(fonts.body, label, 200)
	btn := sdl.Rect{X: x + w - 22 - bw, Y: y + (heroH-48)/2, W: bw, H: 48}
	textX := hero.X + 22 + badge + 18
	textW := btn.X - 22 - textX
	if textW < 120 {
		textW = 120
	}
	lines := sectrustScreen.wrap.get(fonts.small, m.sub, textW)
	if len(lines) > 2 {
		lines = lines[:2]
	}
	ty := hero.Y + (heroH-(fonts.titleH+6+int32(len(lines))*(fonts.smallH+2)))/2
	tone := ToastSuccessInk()
	if m.warnings > 0 {
		tone = ToastWarnInk()
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.summary, textW), tone, textX, ty)
	ty += fonts.titleH + 6
	for _, line := range lines {
		renderText(renderer, config, fonts.small, line, ColorTextMuted(), textX, ty)
		ty += fonts.smallH + 2
	}
	screenBtn := btn
	screenBtn.Y += top
	tbxDrawButton(renderer, config, fonts.body, screenBtn, label, true, nav.focused("readdoc"), false)
	nav.add("readdoc", btn, row, false)
	row++
	y += heroH + 22

	tbxDrawSection(renderer, config, "What this device protects", "shield", x, top+y, w)
	y += tbxSectionHeight
	rowH := tbxRowHeight(fonts)
	for _, r := range m.rows {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		screen := rect
		screen.Y += top
		tbxDrawRow(renderer, config, fonts, screen, r.spec, nav.focused(r.id))
		nav.add(r.id, rect, row, false)
		row++
		y += rowH + 8
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func sectrustSceneAction(config *Config, act tbxAction) bool {
	if sectrustDoc.open {
		return sectrustDocAction(config, act)
	}
	nav := &sectrustScreen.nav
	switch act {
	case tbxActConfirm:
		sectrustActivate(config, nav.focus)
		return true
	case tbxActX, tbxActY:
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func sectrustSceneClick(mx, my int32, config *Config) bool {
	if sectrustDoc.open {
		return sectrustDocClick(mx, my, config)
	}
	it, ok := sectrustScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	sectrustScreen.nav.focus = it.id
	sectrustActivate(config, it.id)
	return true
}

func sectrustSceneHints() []FooterHint {
	if sectrustDoc.open {
		return sectrustDocHints()
	}
	label := "Details"
	if sectrustScreen.nav.focus == "readdoc" || sectrustScreen.nav.focus == "doc" {
		label = "Read"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: label}, {Button: "B", Label: "Back"}}
}

func sectrustActivate(config *Config, id string) {
	PlayActivateSound()
	if id == "readdoc" || id == "doc" {
		sectrustOpenDoc("")
		return
	}
	for _, r := range sectrustScreen.model.posture {
		if r.ID == id {
			sectrustOpenRowSheet(config, r)
			return
		}
	}
}

// sectrustExplain is the text of a row's sheet: what it is, why it matters,
// and the SECURITY.md section that covers it.
func sectrustExplain(id string) (what, why, section string) {
	switch id {
	case "secrets":
		return "API keys and tokens in jukaconfig.json (Groq, Google, Discord and the like) are stored as AES-256-GCM ciphertext, never as the key itself.",
			"A plaintext key can be read by anyone who copies the card or opens the config in the web builder.",
			"Setting a secret from the command line"
	case "key":
		return "The key that encrypts those secrets. Without JUKAHUB_CRYPTO_KEY the app derives one built into every copy of JukaHub.",
			"With the built-in key, anyone who has the app and your config can decrypt the secrets. Your own key keeps them yours.",
			"Setting a Custom Encryption Key"
	case "downloads":
		return "Helper tools, Toolbox tools and the Audit toolkit only download from a short list of approved project releases written into the app, and check the file against the SHA-256 the project publishes whenever it publishes one.",
			"A download source that could be typed in, or a file that is not checked, is how a fake tool would get onto the device.",
			"Toolbox and helper-tool downloads"
	case "packages":
		return "Packages and patches install from a repository index signed with Ed25519; every file is checked against the SHA-256 in that index, and a rollback copy is kept.",
			"HTTPS alone would trust whoever runs the server. The signature means only the maintainers' key can publish a package.",
			"Patch / signed repositories"
	case "signing":
		return "The public key that verifies the signed repository is compiled into the app, so it cannot be swapped by editing a file on the card.",
			"Updates and packages are the most powerful thing the app installs; they must come from the people who hold the private key.",
			"Patch / signed repositories"
	case "servers":
		return "The FTP and SFTP servers share the card with other devices on the network while they run. FTP has no encryption; SFTP uses a generated password and a host key.",
			"A server left running on a café or hotel network is open to everyone on it.",
			"SSH/SFTP server"
	case "companion":
		return "The Phone Companion lets a phone on the same Wi-Fi act as a remote, send text and drop files, after it enters the 6-digit PIN shown on this screen.",
			"Anyone who can see the PIN on your screen can pair. A new PIN locks out every phone that paired before.",
			"Threat model"
	case "party":
		return "While you host a Watch Party, devices on this Wi-Fi that know the room code can join, add videos and control playback for the room.",
			"The room is meant for friends on a home network; close it when you are done.",
			"Threat model"
	case "browser":
		return "The built-in browser keeps your sign-ins across restarts in a cookie file in the app's data folder (mode 0600).",
			"Anyone with the card can copy the file and the sessions in it. Forget clears it.",
			"Browser data on the card"
	case "telemetry":
		return "When turned on, the app sends one tiny anonymous event at boot and after a crash: build, device model and platform. No identifier, no files, no names.",
			"It is off unless you turn it on, and it needs a collector configured in the build.",
			"In-app posture view"
	case "reports":
		return "Report a bug sends only what you review: your text, and, if you leave it on, the redacted diagnostics report. Tokens, account names and paths are removed first.",
			"Diagnostics help fix a bug in one round trip; you decide whether they go along.",
			"Reporting a vulnerability"
	case "process":
		return "Everything JukaHub starts — tools, emulators, plugins, terminal commands — runs with the same account and the same access as the app.",
			"On the handheld that account is root, because the firmware starts apps that way. Only run tools and plugins you trust.",
			"Threat model"
	}
	return "The long form of everything on this screen.", "", ""
}

// sectrustOpenRowSheet shows one row's details and its action.
func sectrustOpenRowSheet(config *Config, r securityRow) {
	what, why, section := sectrustExplain(r.ID)
	var body strings.Builder
	body.WriteString("## What it is\n" + what + "\n\n")
	body.WriteString("## Right now\n" + r.Value + " — " + r.Detail + "\n\n")
	if why != "" {
		body.WriteString("## Why it matters\n" + why + "\n")
	}
	if r.ID == "key" && r.Warn {
		body.WriteString("\n## How to fix it\nSet the environment variable JUKAHUB_CRYPTO_KEY to a long random value before JukaHub starts " +
			"(for example in launch.sh on the handheld), then save each secret again.\n")
	}
	if r.ID == "secrets" && r.Warn {
		body.WriteString("\n## How to fix it\nSet each key again from Settings, or run JukaHub with -set-secret <name>; the value is then stored encrypted.\n")
	}

	var buttons []tbxSheetButton
	if section != "" {
		sec := section
		buttons = append(buttons, tbxSheetButton{label: "Read more", run: func() { sectrustOpenDoc(sec) }})
	}
	switch r.ID {
	case "servers":
		if ftpSrv.Running() {
			buttons = append(buttons, tbxSheetButton{label: "Stop FTP", primary: true, run: func() {
				ftpSrv.Stop()
				sectrustScreen.model.builtAt = time.Time{}
				ShowToast("FTP server stopped", ToastKindInfo)
			}})
		}
		if sshSrv.Running() {
			buttons = append(buttons, tbxSheetButton{label: "Stop SFTP", primary: true, run: func() {
				sshSrv.Stop()
				sectrustScreen.model.builtAt = time.Time{}
				ShowToast("SFTP server stopped", ToastKindInfo)
			}})
		}
	case "companion":
		if companion.srv != nil && companion.srv.Running() {
			buttons = append(buttons, tbxSheetButton{label: "New PIN", primary: true, run: func() {
				companionRestart()
				sectrustScreen.model.builtAt = time.Time{}
				ShowToast("New PIN — phones paired before must pair again", ToastKindSuccess)
			}})
		}
	case "party":
		if partyIsHosting() {
			buttons = append(buttons, tbxSheetButton{label: "Watch Party", primary: true, run: func() { partyOpenScreen(config) }})
		}
	case "browser":
		if strings.Contains(r.Value, "on the card") {
			buttons = append(buttons, tbxSheetButton{label: "Forget cookies", primary: true, run: func() {
				n, err := browserForgetCookies()
				if err != nil {
					ShowToast("Could not clear the cookies: "+firstLine(err.Error()), ToastKindError)
					return
				}
				sectrustScreen.model.builtAt = time.Time{}
				ShowToast(fmt.Sprintf("Forgot %d cookie(s)", n), ToastKindSuccess)
			}})
		}
	case "telemetry":
		label := "Turn on"
		if telemetryOptedIn() {
			label = "Turn off"
		}
		buttons = append(buttons, tbxSheetButton{label: label, primary: true, run: func() {
			on := !telemetryOptedIn()
			if err := setTelemetryOptedIn(config, on); err != nil {
				ShowToast("Could not save the telemetry setting", ToastKindError)
				return
			}
			sectrustScreen.model.builtAt = time.Time{}
			if on {
				TelemetryBoot(config)
				ShowToast("Telemetry on — device model and crashes only", ToastKindSuccess)
			} else {
				ShowToast("Telemetry off — nothing is sent", ToastKindInfo)
			}
		}})
	case "reports":
		buttons = append(buttons, tbxSheetButton{label: "Report a bug", primary: true, run: func() { bugrepOpenScreen(config) }})
	case "downloads":
		buttons = append(buttons, tbxSheetButton{label: "Toolbox", primary: true, run: func() { tbxOpenScreen(config) }})
	case "packages":
		if idx := sceneIndexExact(config, "Packages"); idx >= 0 {
			buttons = append(buttons, tbxSheetButton{label: "Packages", primary: true, run: func() { changeSceneTo(config, idx) }})
		}
	}
	buttons = append(buttons, tbxSheetButton{label: "Close", primary: len(buttons) == 0 || !sectrustHasPrimary(buttons)})
	tbxOpenSheet(config, tbxSheetSpec{
		title:    r.Label,
		subtitle: r.Value,
		icon:     sectrustRowIcon(r.ID),
		body:     body.String(),
		buttons:  buttons,
		focus:    sectrustPrimaryIndex(buttons),
	})
}

func sectrustHasPrimary(buttons []tbxSheetButton) bool {
	for _, b := range buttons {
		if b.primary {
			return true
		}
	}
	return false
}

// sectrustPrimaryIndex is the button a sheet opens focused on: the action when
// there is one, Close otherwise.
func sectrustPrimaryIndex(buttons []tbxSheetButton) int {
	for i, b := range buttons {
		if b.primary {
			return i
		}
	}
	return len(buttons) - 1
}
