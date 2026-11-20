package main

// linuxpkg_ui.go — the Linux packages page (Tools → Linux packages, Home
// search), drawn like the other tool screens (auditscene.go).
//
// Top to bottom: what is installed and how much of the card it takes, when the
// index was last updated and whether anything has a newer version (A checks;
// "Update all" installs them); a search with the on-screen keyboard; a short
// list of useful programs with one-press install; and the packages installed
// by name, each with Remove. A on any package opens a sheet that first works
// out what the install would bring in and how big it is. The Terminal's apt is
// the power path; the page says so. Every change runs on a goroutine with the
// install bar showing it, and only one runs at a time (linuxPkgBegin).

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const linuxPkgSceneName = "LinuxPackages"

// linuxPkgPopular is the page's short list. ffmpeg is left out on purpose:
// Ubuntu's build pulls in a desktop's worth of audio, video and GPU libraries
// (hundreds of megabytes), and JukaHub already installs a static ffmpeg for
// itself.
var linuxPkgPopular = []struct{ pkg, title, about string }{
	{"python3", "Python 3", "The Python language; sqlmap and many scripts need it"},
	{"perl", "Perl", "The Perl language; Nikto needs it"},
	{"curl", "curl", "Fetch and test web addresses from the command line"},
	{"git", "git", "Clone and update code repositories"},
	{"htop", "htop", "Live processes, CPU and memory (in an SSH session)"},
	{"nano", "nano", "A simple text editor (in an SSH session)"},
	{"rsync", "rsync", "Copy and mirror folders, also to and from a PC"},
	{"openssh-client", "OpenSSH client", "ssh, scp and sftp to other machines"},
	{"nmap", "Nmap", "Hosts, ports and services on networks you may scan"},
	{"john", "John the Ripper", "Password audits of hashes you are allowed to test"},
	{"aircrack-ng", "Aircrack-ng", "Analyse Wi-Fi captures (this Wi-Fi chip cannot capture)"},
	{"sqlite3", "SQLite", "Open and query SQLite databases"},
}

// ── Summary ──────────────────────────────────────────────────────────────

// linuxPkgSummary is what the page and the Tools hub tile show, read from
// status.json and meta.json off the main thread.
type linuxPkgSummary struct {
	loaded     bool
	installed  int
	bytes      int64
	indexed    int
	upgradable int
	updated    string
	versions   map[string]string // every installed package
	manual     []string          // installed by name, sorted
	broken     int
}

var linuxPkgSummaryCache struct {
	mu      sync.Mutex
	s       linuxPkgSummary
	at      time.Time
	loading bool
}

// linuxPkgSummaryForget makes the next read come from the card.
func linuxPkgSummaryForget() {
	linuxPkgSummaryCache.mu.Lock()
	linuxPkgSummaryCache.at = time.Time{}
	linuxPkgSummaryCache.mu.Unlock()
}

func linuxPkgReadSummary() linuxPkgSummary {
	s := linuxPkgSummary{loaded: true, versions: map[string]string{}}
	lay, err := linuxPkgDirs()
	if err != nil {
		return s
	}
	st := linuxPkgLoadStatus(lay)
	for n, in := range st.Packages {
		s.installed++
		s.bytes += in.Bytes
		s.versions[n] = in.Version
		if !in.Auto {
			s.manual = append(s.manual, n)
		}
		if in.State != "installed" {
			s.broken++
		}
	}
	sort.Strings(s.manual)
	meta := linuxPkgLoadMeta(lay)
	s.indexed, s.upgradable, s.updated = meta.Packages, meta.Upgradable, meta.Updated
	return s
}

// linuxPkgSummaryNow is the cached summary; one older than a few seconds is
// refreshed in the background (the next frame has it).
func linuxPkgSummaryNow() linuxPkgSummary {
	linuxPkgSummaryCache.mu.Lock()
	defer linuxPkgSummaryCache.mu.Unlock()
	if !linuxPkgSummaryCache.loading && time.Since(linuxPkgSummaryCache.at) > 5*time.Second {
		linuxPkgSummaryCache.loading = true
		go func() {
			s := linuxPkgReadSummary()
			linuxPkgSummaryCache.mu.Lock()
			linuxPkgSummaryCache.s, linuxPkgSummaryCache.at, linuxPkgSummaryCache.loading = s, time.Now(), false
			linuxPkgSummaryCache.mu.Unlock()
		}()
	}
	return linuxPkgSummaryCache.s
}

// linuxPkgInstalledVersion is the installed version of name, or "".
func linuxPkgInstalledVersion(name string) string {
	lay, err := linuxPkgDirs()
	if err != nil {
		return ""
	}
	return linuxPkgLoadStatus(lay).Packages[name].Version
}

// linuxPkgShortVersion is a version for a row: "7.94" for
// "7.94+git20230807.3be01efb1+dfsg-3build2".
func linuxPkgShortVersion(v string) string {
	up := linuxPkgParseVersion(v).upstream
	if i := strings.IndexAny(up, "+~"); i > 0 {
		up = up[:i]
	}
	return up
}

// linuxPkgHubTile is the Tools hub's tile (only on the handheld).
func linuxPkgHubTile() toolsHubTile {
	tile := toolsHubTile{id: "linuxpkg", label: "Linux packages", icon: "packages", value: "Ubuntu programs: Python, Perl, git, nmap…"}
	s := linuxPkgSummaryNow()
	switch {
	case linuxPkgBusyLabel() != "":
		tile.value, tile.tone = "Working: "+linuxPkgBusyLabel(), toolsHubToneBusy
	case s.upgradable > 0:
		tile.value, tile.tone = pluralizeCount(s.upgradable, "update", "updates")+" available", toolsHubToneWarn
	case s.installed > 0:
		tile.value, tile.tone = pluralizeCount(s.installed, "package", "packages")+" · "+formatSize(s.bytes), toolsHubToneGood
	}
	return tile
}

// ── Progress on the install bar ──────────────────────────────────────────

// linuxPkgPageLine is the last line an operation printed, for the status row.
var linuxPkgPageLine struct {
	mu   sync.Mutex
	text string
}

func linuxPkgSetPageLine(s string) {
	linuxPkgPageLine.mu.Lock()
	linuxPkgPageLine.text = s
	linuxPkgPageLine.mu.Unlock()
}

func linuxPkgPageLineNow() string {
	linuxPkgPageLine.mu.Lock()
	defer linuxPkgPageLine.mu.Unlock()
	return linuxPkgPageLine.text
}

// linuxPkgBarIO reports an operation on install bar run: its phase from the
// lines apt prints, its bytes from the downloads. The Audit toolkit uses it
// for the interpreter it installs first.
func linuxPkgBarIO(run uint64) *linuxPkgIO {
	var mu sync.Mutex
	phase := installPhaseResolving
	var done, total int64
	return &linuxPkgIO{
		out: func(s string) {
			linuxPkgSetPageLine(s)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.HasPrefix(s, "Get:"):
				phase = installPhaseDownload
			case strings.HasPrefix(s, "Unpacking"), strings.HasPrefix(s, "Upgrading"), strings.HasPrefix(s, "Removing"):
				phase = installPhaseExtract
			case strings.HasPrefix(s, "Reading package lists"):
				phase = installPhaseVerify
			default:
				return
			}
			installBarUpdateRun(run, phase, done, total)
		},
		progress: func(d, t int64) {
			mu.Lock()
			defer mu.Unlock()
			done, total = d, t
			installBarUpdateRun(run, phase, done, total)
		},
	}
}

// linuxPkgTail keeps the last lines an operation printed, so a failure's sheet
// shows what was being fetched or unpacked when it stopped.
type linuxPkgTail struct {
	mu    sync.Mutex
	lines []string
}

// linuxPkgTailMax is how many lines a tail keeps.
const linuxPkgTailMax = 24

func (t *linuxPkgTail) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > linuxPkgTailMax {
		t.lines = append(t.lines[:0], t.lines[len(t.lines)-linuxPkgTailMax:]...)
	}
}

// text is the kept lines, oldest first.
func (t *linuxPkgTail) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// linuxPkgBarIOTail is linuxPkgBarIO that also keeps the last lines in tail.
func linuxPkgBarIOTail(run uint64, tail *linuxPkgTail) *linuxPkgIO {
	o := linuxPkgBarIO(run)
	out := o.out
	o.out = func(s string) {
		tail.add(s)
		out(s)
	}
	return o
}

// linuxPkgMissing is the names among names that are not installed (or were
// left half-installed by a run that died). Off the frame loop: it reads
// status.json.
func linuxPkgMissing(names []string) []string {
	lay, err := linuxPkgDirs()
	if err != nil {
		return nil
	}
	st := linuxPkgLoadStatus(lay)
	var out []string
	for _, n := range names {
		if in, ok := st.Packages[n]; !ok || in.State != "installed" {
			out = append(out, n)
		}
	}
	return out
}

// ── Screen ───────────────────────────────────────────────────────────────

type linuxPkgRow struct {
	id   string
	pkg  string
	spec tbxRowSpec
}

type linuxPkgScreenState struct {
	visit     tbxVisit
	nav       tbxNav
	builtAt   time.Time
	status    []linuxPkgRow
	popular   []linuxPkgRow
	results   []linuxPkgRow
	installed []linuxPkgRow
	header    UIPageHeader
	// busy is what this page started and is still running ("" when idle).
	busy      string
	query     string
	found     []linuxPkgEntry
	foundAll  int
	searching bool
	token     int
}

var linuxPkgScreen linuxPkgScreenState

// linuxPkgOpenScreen opens the page. Cross-area contract: the Tools hub and
// Home search call it.
func linuxPkgOpenScreen(config *Config) {
	tbxOpenScene(config, linuxPkgSceneName)
}

type linuxPkgScene struct{ sceneBase }

func (linuxPkgScene) render(r *sdl.Renderer, c *Config) bool {
	if linuxPkgScreen.visit.begin() {
		linuxPkgScreen.nav.scroll = 0
		linuxPkgSummaryForget()
	}
	linuxPkgRenderScreen(r, c)
	return true
}

func (linuxPkgScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), linuxPkgSceneAction)
}

func (linuxPkgScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), linuxPkgSceneAction)
}

func init() { registerScene(linuxPkgScene{sceneBase{name: linuxPkgSceneName}}) }

func linuxPkgBuildModel(now time.Time) {
	m := &linuxPkgScreen
	m.builtAt = now
	s := linuxPkgSummaryNow()
	status := "Not set up"
	ink := ColorTextTertiary()
	if s.installed > 0 {
		status = pluralizeCount(s.installed, "package", "packages")
	}
	m.header = tbxHeader("Linux packages", "Ubuntu 24.04 arm64 programs, checked against Ubuntu's signing key", "packages", status, ink)

	m.status = m.status[:0]
	idx := tbxRowSpec{icon: "update", title: "Check for updates"}
	switch {
	case !s.loaded:
		idx.subtitle = "Reading…"
	case s.indexed == 0:
		idx.subtitle = "No package index yet: A downloads it (about 20 MB)"
		idx.pill, idx.tone = "Get index", tbxToneAccent
	default:
		idx.subtitle = strconv.Itoa(s.indexed) + " packages in the index"
		if t, err := time.Parse(time.RFC3339, s.updated); err == nil {
			idx.subtitle += " · updated " + t.Local().Format("2 Jan 15:04")
		}
		if s.upgradable > 0 {
			idx.pill, idx.tone = pluralizeCount(s.upgradable, "update", "updates"), tbxToneWarn
		} else {
			idx.pill, idx.tone = "Up to date", tbxToneGood
		}
	}
	if m.busy != "" {
		idx.pill, idx.tone = "Working…", tbxToneAccent
		if line := linuxPkgPageLineNow(); line != "" {
			idx.subtitle = line
		}
	}
	m.status = append(m.status, linuxPkgRow{id: "index", spec: idx})
	if s.upgradable > 0 && m.busy == "" {
		m.status = append(m.status, linuxPkgRow{id: "upgrade", spec: tbxRowSpec{icon: "download", title: "Update all",
			subtitle: "Install the newer versions of " + pluralizeCount(s.upgradable, "package", "packages"), pill: "Update", tone: tbxToneAccent}})
	}
	search := tbxRowSpec{icon: "search", title: "Search packages", subtitle: "Find any of Ubuntu's arm64 programs by name or description"}
	if m.query != "" {
		search.subtitle = "“" + m.query + "”"
		switch {
		case m.searching:
			search.pill, search.tone = "Searching…", tbxToneNeutral
		default:
			search.pill, search.tone = pluralizeCount(m.foundAll, "match", "matches"), tbxToneNeutral
		}
	}
	m.status = append(m.status, linuxPkgRow{id: "search", spec: search})
	used := "Nothing installed yet"
	if s.installed > 0 {
		used = pluralizeCount(s.installed, "package", "packages") + " (" + strconv.Itoa(len(s.manual)) + " chosen by you) · " + formatSize(s.bytes) + " on the card"
	}
	m.status = append(m.status, linuxPkgRow{id: "terminal", spec: tbxRowSpec{icon: "terminal", title: "apt in the Terminal",
		subtitle: used + " · the full apt commands are there", pill: "Open", tone: tbxToneNeutral}})

	pkgRow := func(prefix, pkg, title, about string) linuxPkgRow {
		spec := tbxRowSpec{icon: "packages", title: title, subtitle: about}
		if v, ok := s.versions[pkg]; ok {
			spec.pill, spec.tone = "Installed", tbxToneGood
			spec.note = linuxPkgShortVersion(v)
		} else {
			spec.pill, spec.tone = "Install", tbxToneAccent
		}
		if m.busy == pkg {
			spec.pill, spec.tone = "Working…", tbxToneAccent
		}
		return linuxPkgRow{id: prefix + pkg, pkg: pkg, spec: spec}
	}
	m.popular = m.popular[:0]
	for _, p := range linuxPkgPopular {
		m.popular = append(m.popular, pkgRow("pop:", p.pkg, p.title, p.about))
	}
	m.results = m.results[:0]
	for _, e := range m.found {
		m.results = append(m.results, pkgRow("res:", e.Name, e.Name, e.Desc))
	}
	m.installed = m.installed[:0]
	for _, n := range s.manual {
		r := pkgRow("inst:", n, n, "Installed by you")
		r.spec.subtitle = "Version " + s.versions[n]
		m.installed = append(m.installed, r)
	}
}

func linuxPkgRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(linuxPkgScreen.builtAt) > time.Second {
		linuxPkgBuildModel(now)
	}
	m := &linuxPkgScreen
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	nav := &m.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)
	row := 0
	rowH := tbxRowHeight(fonts)
	group := func(title, icon string, rows []linuxPkgRow) {
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
			nav.add(r.id, rect, row, false)
			row++
			y += rowH + 8
		}
	}
	group("This handheld", "packages", m.status)
	group("Search results", "search", m.results)
	group("Popular", "star", m.popular)
	group("Installed by you", "check", m.installed)
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func linuxPkgSceneAction(config *Config, act tbxAction) bool {
	nav := &linuxPkgScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus != "" {
			PlayActivateSound()
			linuxPkgActivate(config, nav.focus)
		}
		return true
	case tbxActY:
		PlayActivateSound()
		linuxPkgAskSearch()
		return true
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func linuxPkgSceneClick(mx, my int32, config *Config) bool {
	it, ok := linuxPkgScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	linuxPkgScreen.nav.focus = it.id
	PlayActivateSound()
	linuxPkgActivate(config, it.id)
	return true
}

func linuxPkgSceneHints() []FooterHint {
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"},
		{Button: "Y", Label: "Search"}, {Button: "B", Label: "Back"}}
}

func linuxPkgActivate(config *Config, id string) {
	switch {
	case id == "index":
		linuxPkgRunFromPage(config, "index", "Package index", true, func(ctx context.Context, o *linuxPkgIO) error {
			release, err := linuxPkgBegin("updating the package index")
			if err != nil {
				return err
			}
			defer release()
			if err := linuxPkgUpdate(ctx, o); err != nil {
				return err
			}
			lay, _ := linuxPkgDirs()
			linuxPkgRefreshCounts(lay, linuxPkgLoadStatus(lay))
			return nil
		}, "Package index is up to date")
	case id == "upgrade":
		n := linuxPkgSummaryNow().upgradable
		tbxOpenSheet(config, tbxSheetSpec{
			title:    "Update all?",
			subtitle: pluralizeCount(n, "package has", "packages have") + " a newer version",
			icon:     "update",
			body:     "The index is checked once more, then every installed package with a newer version is downloaded, checked against Ubuntu's signed index and unpacked. Programs that are running keep their old files until they restart.",
			buttons: []tbxSheetButton{{label: "Not now"}, {label: "Update all", primary: true, run: func() {
				linuxPkgRunFromPage(config, "upgrade", "Linux packages", true, linuxPkgUpgradeAll, "Everything is up to date")
			}}},
		})
	case id == "search":
		linuxPkgAskSearch()
	case id == "terminal":
		termOpenWithCommand(config, "apt ")
	default:
		_, pkg, ok := strings.Cut(id, ":")
		if ok && pkg != "" {
			linuxPkgOpenSheet(config, pkg)
		}
	}
}

// linuxPkgAskSearch opens the keyboard for a search.
func linuxPkgAskSearch() {
	tbxOpenPrompt("Search packages", "e.g. sqlite, editor, python3-requests", linuxPkgScreen.query, func(text string) {
		text = strings.TrimSpace(text)
		linuxPkgScreen.query = text
		linuxPkgScreen.found, linuxPkgScreen.foundAll = nil, 0
		linuxPkgScreen.builtAt = time.Time{}
		if text == "" {
			return
		}
		linuxPkgScreen.searching = true
		go func() {
			lay, err := linuxPkgDirs()
			var hits []linuxPkgEntry
			total := 0
			if err == nil {
				hits, total, err = linuxPkgSearch(context.Background(), lay, text, 30)
			}
			tbxPost(func() {
				if linuxPkgScreen.query != text {
					return
				}
				linuxPkgScreen.searching = false
				linuxPkgScreen.found, linuxPkgScreen.foundAll = hits, total
				linuxPkgScreen.builtAt = time.Time{}
				if err != nil {
					ShowToast("Search: "+firstLine(err.Error()), ToastKindWarning)
				}
			})
		}()
	})
}

// linuxPkgOpenSheet shows one package and what installing it would mean,
// worked out on a goroutine (the index is read from the card).
func linuxPkgOpenSheet(config *Config, pkg string) {
	s := linuxPkgSummaryNow()
	version, installed := s.versions[pkg]
	title := pkg
	about := ""
	for _, p := range linuxPkgPopular {
		if p.pkg == pkg {
			title, about = p.title, p.about
		}
	}
	linuxPkgScreen.token++
	token := "linuxpkg-" + strconv.Itoa(linuxPkgScreen.token)
	intro := ""
	if about != "" {
		intro = about + ".\n\n"
	}
	if installed {
		body := intro + "## On this handheld\nVersion " + version + ", run from the card with Ubuntu's own C library.\n\n" +
			"## Use\n• Type " + pkg + " commands in the Terminal (Tools → Terminal).\n• Remove deletes its files and the packages that only it needed."
		tbxOpenSheet(config, tbxSheetSpec{
			token: token, title: title, subtitle: "Installed · " + linuxPkgShortVersion(version), icon: "packages", body: body,
			buttons: []tbxSheetButton{
				{label: "Remove", run: func() { linuxPkgConfirmRemove(config, pkg, title) }},
				{label: "Terminal", run: func() { termOpenWithCommand(config, "apt show "+pkg) }},
				{label: "Close", primary: true},
			},
			focus: 2,
		})
		return
	}
	install := tbxSheetButton{label: "Install", primary: true, run: func() {
		linuxPkgRunFromPage(config, pkg, title, true, func(ctx context.Context, o *linuxPkgIO) error {
			// The Audit toolkit's set for the same package (Aircrack-ng
			// without the capture helpers), so the two installs agree.
			return linuxPkgEnsureSkipping(ctx, []string{pkg}, auditSkipDepsFor(pkg), false, o)
		}, title+" is installed")
	}}
	tbxOpenSheet(config, tbxSheetSpec{
		token: token, title: title, subtitle: "Ubuntu 24.04 · arm64", icon: "packages",
		body:    intro + "Working out what it needs…",
		buttons: []tbxSheetButton{{label: "Close"}, install},
		focus:   1,
	})
	go func() {
		text := linuxPkgPreview(pkg)
		tbxPost(func() { tbxUpdateSheet(token, intro+text, nil) })
	}()
}

// linuxPkgPreview describes what installing pkg brings in.
func linuxPkgPreview(pkg string) string {
	lay, err := linuxPkgDirs()
	if err != nil {
		return err.Error()
	}
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return "The package index (about 20 MB) is downloaded first; then what " + pkg + " needs is installed with it.\n\n" + linuxPkgTrustText()
	}
	plan, _, err := linuxPkgPlanInstallSkipping(x, linuxPkgLoadStatus(lay), []string{pkg}, auditSkipDepsFor(pkg))
	var desc string
	if e, ok := x.Get(pkg); ok {
		desc = e.Desc
	}
	x.Close()
	if err != nil {
		return "It cannot be installed: " + err.Error()
	}
	var b strings.Builder
	if desc != "" {
		b.WriteString(desc + ".\n\n")
	}
	b.WriteString("## What it brings\n")
	b.WriteString(pluralizeCount(len(plan.install), "package", "packages") + ": " + formatSize(plan.download) + " to download, about " +
		formatSize(plan.addBytes) + " on the card.\n")
	if len(plan.install) > 1 {
		names := make([]string, 0, len(plan.install))
		for _, e := range plan.install {
			if e.Name != pkg {
				names = append(names, e.Name)
			}
		}
		b.WriteString("With: " + strings.Join(names, ", ") + ".\n")
	}
	b.WriteString("\n" + linuxPkgTrustText())
	return b.String()
}

// linuxPkgTrustText is how the page explains the check, in one paragraph.
func linuxPkgTrustText() string {
	return "## How it is checked\nUbuntu signs its package index; JukaHub accepts only Ubuntu's archive key, and every download must match the SHA-256 in that signed index before it is unpacked. Nothing in the firmware changes: the programs live on the card and run with Ubuntu's own C library."
}

func linuxPkgConfirmRemove(config *Config, pkg, title string) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Remove " + title + "?",
		subtitle: "You can install it again at any time",
		icon:     "trash",
		body:     "Its files are deleted from the card, with any installed package that needs it and the dependencies nothing else uses.",
		buttons: []tbxSheetButton{{label: "Keep"}, {label: "Remove", primary: true, run: func() {
			linuxPkgRunFromPage(config, pkg, "Remove "+title, false, func(ctx context.Context, o *linuxPkgIO) error {
				return linuxPkgRemoveNames([]string{pkg}, o)
			}, title+" removed")
		}}},
	})
}

// linuxPkgRunFromPage runs one change from the page with the install bar;
// needNet is false for a removal, which works offline.
func linuxPkgRunFromPage(config *Config, key, label string, needNet bool, fn func(ctx context.Context, o *linuxPkgIO) error, done string) {
	if linuxPkgScreen.busy != "" || linuxPkgBusyLabel() != "" {
		ShowToast("Linux packages are busy — one moment", ToastKindInfo)
		return
	}
	if needNet && !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	linuxPkgScreen.busy = key
	linuxPkgScreen.builtAt = time.Time{}
	linuxPkgSetPageLine("")
	run, ctx := installBarBeginRun(label)
	o := linuxPkgBarIO(run)
	go func() {
		err := fn(ctx, o)
		linuxPkgSummaryForget()
		secAuditForget()
		tbxPost(func() {
			linuxPkgScreen.busy = ""
			linuxPkgScreen.builtAt = time.Time{}
			linuxPkgSetPageLine("")
			if err != nil {
				installBarFailRun(run, installCardDetail(err.Error()))
				ShowToast(label+": "+plainNetworkError(err), ToastKindError)
				return
			}
			installBarDoneRun(run)
			PlaySuccessSound()
			ShowToast(done, ToastKindSuccess)
		})
	}()
}
