package main

// launch_errors.go — the one card that says why something did not start.
//
// Two shapes share it. A launch that failed before its child ran (a missing
// profile executable, a runtime that is not installed) shows a reason and a
// suggested fix (showLaunchFailureDialog). A game that started and quit within
// seconds gets the game diagnosis's findings (gamediag.go,
// showLaunchDiagnosis): a list of issues, each with the file names that are
// missing and the full folder to copy them into, plus the last relevant lines
// of the emulator's log. Folder paths on the handheld are long
// ("/mnt/SDCARD/RetroArch/.retroarch/system/dc"), so every line is wrapped —
// at spaces first, then after a path separator — instead of being ellipsized
// into "…/system/d", and a card taller than the screen scrolls with Up/Down.
//
// A card about a launch that failed also carries its bug report
// (launchreport.go): the report's full text under the findings, and buttons
// Cancel and Submit report. Submit is focused when the card opens, so A files
// the report as a GitHub issue; B or Cancel closes the card and sends nothing.
// Left/Right move between the buttons, Up/Down scroll, L1/R1 a page. A port
// that met BusyBox's sh because the firmware's launcher ran it gets a third
// button, Try it directly (gameDiagReport.TryDirect): A remembers that choice
// for this port and starts it again under JukaHub's bash, and it opens
// focused. A card that reports no failure — a check the player asked for
// ("Check why it won't start"), Splore without PICO-8 — keeps its single
// Dismiss, unless it can offer the retry too.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// launchIssue is one finding on the card: what is wrong, the files that are
// missing, the folder they go in and a sentence of explanation. Level orders
// the list (gameDiagLevel: 0 certain, 1 likely, 2 maybe).
type launchIssue struct {
	Level  int
	Title  string
	Files  []string
	Where  string
	Detail string
}

type launchFailureState struct {
	open   bool
	target string
	reason string
	fix    string
	log    string
	// The diagnosis form (showLaunchDiagnosis): a title of its own, one line
	// under it, the findings and a log excerpt.
	title    string
	summary  string
	issues   []launchIssue
	logLines []string
	// tryDirect is the port a "Try it directly" button starts again under
	// JukaHub's bash (ports_env.go); nil when the card does not offer it.
	tryDirect *gameDiagTryDirect
	// scroll is the first body line drawn; maxScroll is set by the renderer.
	scroll    int
	maxScroll int
	// lines is the wrapped body, built once per card width.
	lines  []launchFailureLine
	linesW int32
	// report is the bug report the card offers (launchreport.go); nil on a
	// card that reports no failure, whose only answer is Dismiss.
	report *launchReportDraft
	// focus is the button A presses (launchFocusTry when the card offers the
	// retry, else launchFocusSubmit where it opens, or launchFocusCancel).
	focus int
	// page is how many lines L1/R1 scroll; card and the buttons are the
	// renderer's hit targets for a tap or a click.
	page       int
	card       sdl.Rect
	tryRect    sdl.Rect
	submitRect sdl.Rect
	cancelRect sdl.Rect
	// openedAt arms the buttons launchFailureArmDelay later.
	openedAt time.Time
}

var launchFailure launchFailureState

// The report card's buttons, in the order Left/Right step through them.
const (
	launchFocusTry = iota
	launchFocusCancel
	launchFocusSubmit
)

// launchFailureArmDelay is how long a report card's buttons ignore A, B and
// clicks after it opens: a card that comes up as a game drops back to JukaHub
// can meet a press meant for the game, which must not send or drop a report
// nobody has seen yet.
const launchFailureArmDelay = 700 * time.Millisecond

// launchFailureArmed reports whether the report card's buttons take presses.
func launchFailureArmed() bool {
	return time.Since(launchFailure.openedAt) >= launchFailureArmDelay
}

// launchFailureLineKind picks a body line's font and colour.
type launchFailureLineKind uint8

const (
	launchLineBody launchFailureLineKind = iota
	launchLineHeading
	launchLinePath
	launchLineDim
	launchLineGap
	// launchLineMono is a line of the bug report's details: the smallest type,
	// wrapped without losing its indentation (launchWrapKeep).
	launchLineMono
)

type launchFailureLine struct {
	kind launchFailureLineKind
	text string
}

type launchFailureKind uint8

const (
	launchFailureGameFolderMissing launchFailureKind = iota + 1
	launchFailureProfileExecutableMissing
	launchFailureUnsupportedLauncherType
	launchFailureRuntimeMissing
)

// launchFailureCause adds a stable category while preserving the underlying
// error for errors.Is/As and for the reason shown in the dialog.
type launchFailureCause struct {
	kind launchFailureKind
	err  error
}

func (e *launchFailureCause) Error() string { return e.err.Error() }
func (e *launchFailureCause) Unwrap() error { return e.err }

func withLaunchFailureKind(kind launchFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &launchFailureCause{kind: kind, err: err}
}

func launchFailureSuggestion(err error) string {
	// A display that stopped on a library older than the program needs: the
	// cause is the firmware's file, not anything the player can install from
	// JukaHub.
	var old *wineOldLibraryError
	if errors.As(err, &old) {
		lib := old.lib
		if lib == "" {
			lib = "the library it links"
		}
		return "Update this firmware if it carries a newer " + lib + ": JukaHub answers only the libraries it can safely complete (zlib), so this one needs the firmware's own newer copy."
	}
	var categorized *launchFailureCause
	if errors.As(err, &categorized) {
		switch categorized.kind {
		case launchFailureGameFolderMissing:
			return "Put the game folder under one of the configured game roots, or correct the profile’s folder path."
		case launchFailureProfileExecutableMissing:
			return "Check the profile’s executable path and make sure the game files are installed."
		case launchFailureUnsupportedLauncherType:
			return "Use a supported launcher file (.exe, .bat, .cmd, .lnk, .url or .sh), then rescan."
		case launchFailureRuntimeMissing:
			return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
		}
	}
	// An unclassified exec.ErrNotFound comes from the command or runtime. A
	// missing profile executable has its own category above.
	if errors.Is(err, exec.ErrNotFound) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrNotExist) {
		return "The launcher or a required runtime is missing. Reinstall the emulator, then check its BIOS/runtime setup."
	}
	if errors.Is(err, os.ErrPermission) {
		return "The launcher is not executable. Check file permissions or reinstall the emulator."
	}
	return "Check the emulator's required BIOS, runtime, and launcher configuration. The game log may contain more detail."
}

// showLaunchFailureDialog opens the card for a launch that failed before its
// program ran, with its bug report. target names what was started; the
// record holds only that, the error and the log — a caller that knows the
// program uses showLaunchFailureRecord.
func showLaunchFailureDialog(target string, err error, logPath string) {
	if err == nil {
		return
	}
	rec := launchRecordNew("Launch", target, "")
	rec.Reason = err.Error()
	rec.LogPath = strings.TrimSpace(logPath)
	showLaunchFailureRecord(rec, err, logPath)
}

// showLaunchFailureRecord is showLaunchFailureDialog for a launch whose record
// says what was run (launchreport.go). Main thread.
func showLaunchFailureRecord(rec *launchRecord, err error, logPath string) {
	if err == nil {
		return
	}
	target := ""
	issues := launchFailureIssues(err)
	if rec != nil {
		target = rec.Title
		if rec.Reason == "" {
			rec.Reason = err.Error()
		}
	}
	launchFailure = launchFailureState{
		open:     true,
		target:   strings.TrimSpace(target),
		reason:   strings.TrimSpace(err.Error()),
		fix:      launchFailureSuggestion(err),
		issues:   issues,
		log:      strings.TrimSpace(logPath),
		openedAt: time.Now(),
	}
	if rec != nil {
		title := "Couldn't launch"
		if launchFailure.target != "" {
			title += " " + launchFailure.target
		}
		launchFailure.report = launchReportNewDraft(rec, title, "", issues, "")
	}
}

// launchFailureIssues are the findings a failed launch's report carries besides
// the reason: a display that stopped because a library this firmware has is too
// old names that library, the function and the version there too
// (wine_weston_cause.go), so a report like JH-6G5TZF says why instead of
// "Weston stopped while it was starting".
func launchFailureIssues(err error) []launchIssue {
	var old *wineOldLibraryError
	if errors.As(err, &old) {
		return []launchIssue{old.issue()}
	}
	return nil
}

// showLaunchDiagnosis opens the card with a diagnosis: title ("Sonic CD
// didn't start"), one summary line, the issues most certain first, the log
// lines worth reading and the log's path. It offers no report: the player
// asked for the check. Main thread only.
func showLaunchDiagnosis(title, summary string, issues []launchIssue, logLines []string, logPath string) {
	launchFailure = launchFailureState{
		open:     true,
		title:    strings.TrimSpace(title),
		summary:  strings.TrimSpace(summary),
		issues:   append([]launchIssue(nil), issues...),
		logLines: append([]string(nil), logLines...),
		log:      strings.TrimSpace(logPath),
		focus:    launchFocusSubmit,
		openedAt: time.Now(),
	}
}

// showLaunchDiagnosisReport is showLaunchDiagnosis for a launch that failed:
// the card also carries the bug report the diagnosis wrote (report.Launch,
// report.Details) and, for a port that met BusyBox through the firmware's
// launcher, the "Try it directly" button, which opens focused because it is
// the fix. Main thread only.
func showLaunchDiagnosisReport(report gameDiagReport) {
	showLaunchDiagnosis(report.Title, report.Summary, report.Issues, report.LogLines, report.LogPath)
	if report.TryDirect != nil {
		launchFailure.tryDirect = report.TryDirect
		launchFailure.focus = launchFocusTry
	}
	if report.Launch != nil {
		launchFailure.report = launchReportNewDraft(report.Launch, launchFailure.title, launchFailure.summary,
			launchFailure.issues, report.Details)
	}
}

func closeLaunchFailureDialog() {
	launchFailure = launchFailureState{}
	PlayBackSound()
}

// launchFailureCancel is Cancel on a report card: the card closes and nothing
// is sent.
func launchFailureCancel() {
	if d := launchFailure.report; d != nil {
		logInfo("launch", "launch report %s not sent: Cancel", d.ref)
	}
	closeLaunchFailureDialog()
}

// launchFailureTryDirect is "Try it directly": it remembers the choice for this
// port alone (ports_env.go) and starts the port again under the bash JukaHub
// supplies, instead of through the firmware's Ports launcher. Main thread.
func launchFailureTryDirect() {
	d := launchFailure.tryDirect
	if d == nil {
		return
	}
	if err := portDirectRemember(d.Script); err != nil {
		ShowToast("Could not remember the choice for "+d.Name+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	closeLaunchFailureDialog()
	logInfo("portmaster", "%s: Try it directly chosen; this port runs under JukaHub's bash from now on", d.Name)
	ShowToast("Starting "+d.Name+" under JukaHub's bash", ToastKindInfo)
	launchPortScript(d.Name, d.Script)
}

// launchFailureFocusMove moves the focused button one step in the card's order:
// Try it directly, Cancel, Submit report.
func launchFailureFocusMove(delta int) {
	order := make([]int, 0, 3)
	if launchFailure.tryDirect != nil {
		order = append(order, launchFocusTry)
	}
	if launchFailure.report != nil {
		order = append(order, launchFocusCancel, launchFocusSubmit)
	}
	if len(order) < 2 {
		return
	}
	at := 0
	for i, f := range order {
		if f == launchFailure.focus {
			at = i
		}
	}
	at = (at + delta%len(order) + len(order)) % len(order)
	if next := order[at]; next != launchFailure.focus {
		launchFailure.focus = next
		PlayNavSound()
	}
}

// launchFailureScroll moves a tall card by delta lines.
func launchFailureScroll(delta int) {
	next := launchFailure.scroll + delta
	if next > launchFailure.maxScroll {
		next = launchFailure.maxScroll
	}
	if next < 0 {
		next = 0
	}
	if next != launchFailure.scroll {
		launchFailure.scroll = next
		PlayNavSound()
	}
}

// launchFailurePage is how far one L1/R1 scrolls.
func launchFailurePage() int {
	if launchFailure.page < 2 {
		return 8
	}
	return launchFailure.page
}

// launchFailureAction is one press on a report card. Only the card's own
// buttons answer: a stray press neither sends the report nor closes the card.
func launchFailureAction(act tbxAction) {
	switch act {
	case tbxActUp:
		launchFailureScroll(-2)
	case tbxActDown:
		launchFailureScroll(2)
	case tbxActPageUp:
		launchFailureScroll(-launchFailurePage())
	case tbxActPageDown:
		launchFailureScroll(launchFailurePage())
	case tbxActHome:
		launchFailureScroll(-launchFailure.scroll)
	case tbxActEnd:
		launchFailureScroll(launchFailure.maxScroll - launchFailure.scroll)
	case tbxActLeft:
		launchFailureFocusMove(-1)
	case tbxActRight:
		launchFailureFocusMove(1)
	case tbxActConfirm:
		if !launchFailureArmed() {
			return
		}
		switch launchFailure.focus {
		case launchFocusTry:
			launchFailureTryDirect()
		case launchFocusCancel:
			launchFailureCancel()
		default:
			launchReportSubmit()
		}
	case tbxActBack:
		if launchFailureArmed() {
			launchFailureCancel()
		}
	}
}

// Up and Down scroll the card (and never dismiss it, so reading a long
// diagnosis cannot close it by accident); on a card without a report every
// other key dismisses.
func handleLaunchFailureKey(e *sdl.KeyboardEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if launchFailure.report != nil || launchFailure.tryDirect != nil {
		launchFailureAction(tbxKeyAction(e))
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		launchFailureScroll(-2)
		return true
	case sdl.K_DOWN:
		launchFailureScroll(2)
		return true
	}
	closeLaunchFailureDialog()
	return true
}

func handleLaunchFailureController(e *sdl.ControllerButtonEvent) bool {
	if !launchFailure.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if launchFailure.report != nil || launchFailure.tryDirect != nil {
		launchFailureAction(tbxPadAction(e))
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		launchFailureScroll(-2)
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		launchFailureScroll(2)
		return true
	}
	closeLaunchFailureDialog()
	return true
}

// handleLaunchFailureMouse is a tap or a click while the card is up. On a
// report card only the two buttons answer; any other tap is swallowed, so
// nothing behind the card is pressed. A card without a report closes.
func handleLaunchFailureMouse(mx, my int32) bool {
	if !launchFailure.open {
		return false
	}
	if launchFailure.report == nil && launchFailure.tryDirect == nil {
		closeLaunchFailureDialog()
		return true
	}
	switch {
	case !launchFailureArmed():
	case launchFailure.tryRect.W > 0 && pointInRect(mx, my, launchFailure.tryRect):
		launchFailure.focus = launchFocusTry
		launchFailureTryDirect()
	case launchFailure.submitRect.W > 0 && pointInRect(mx, my, launchFailure.submitRect):
		launchFailure.focus = launchFocusSubmit
		launchReportSubmit()
	case launchFailure.cancelRect.W > 0 && pointInRect(mx, my, launchFailure.cancelRect):
		launchFailureCancel()
	}
	return true
}

// launchFailureWheel scrolls the card with the mouse wheel, and keeps the
// wheel from scrolling what is behind it.
func launchFailureWheel(dy int32) bool {
	if !launchFailure.open {
		return false
	}
	switch {
	case dy > 0:
		launchFailureScroll(-3)
	case dy < 0:
		launchFailureScroll(3)
	}
	return true
}

// launchSplitLong cuts one word wider than maxW into pieces that fit, each
// ending after a path separator when there is one, so a folder path breaks
// between its folders.
func launchSplitLong(measure func(string) int32, word string, maxW int32) []string {
	if measure(word) <= maxW {
		return []string{word}
	}
	var out []string
	runes := []rune(word)
	for len(runes) > 0 {
		n, cut := 0, 0
		for n < len(runes) && measure(string(runes[:n+1])) <= maxW {
			if runes[n] == '/' || runes[n] == '\\' {
				cut = n + 1
			}
			n++
		}
		if n >= len(runes) {
			out = append(out, string(runes))
			break
		}
		if cut == 0 {
			cut = n
		}
		if cut == 0 {
			cut = 1 // a single rune wider than the card
		}
		out = append(out, string(runes[:cut]))
		runes = runes[cut:]
	}
	return out
}

// launchWrapText word-wraps text to maxW, keeping explicit line breaks and
// splitting long words (paths) with launchSplitLong. Nothing is cut off.
func launchWrapText(measure func(string) int32, text string, maxW int32) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			pieces := launchSplitLong(measure, word, maxW)
			if len(pieces) > 1 {
				// A path wider than the card starts on its own line.
				if cur != "" {
					out = append(out, cur)
				}
				out = append(out, pieces[:len(pieces)-1]...)
				cur = pieces[len(pieces)-1]
				continue
			}
			cand := word
			if cur != "" {
				cand = cur + " " + word
			}
			if cur == "" || measure(cand) <= maxW {
				cur = cand
				continue
			}
			out = append(out, cur)
			cur = word
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// launchWrapKeep wraps one line of the report's details to maxW without
// touching its spacing: the line is cut at the last space or separator that
// fits (or anywhere in a run without one), and the rest goes on indented under
// it. Each cut costs a binary search of measure, not a call per rune.
func launchWrapKeep(measure func(string) int32, text string, maxW int32) []string {
	if measure(text) <= maxW {
		return []string{text}
	}
	indent := text[:len(text)-len(strings.TrimLeft(text, " "))] + "    "
	var out []string
	rest := []rune(text)
	prefix := ""
	for len(rest) > 0 {
		fits := func(n int) bool { return measure(prefix+string(rest[:n])) <= maxW }
		if fits(len(rest)) {
			out = append(out, prefix+string(rest))
			break
		}
		lo, hi := 0, len(rest) // fits(lo); !fits(hi)
		for hi-lo > 1 {
			mid := (lo + hi) / 2
			if fits(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		cut := lo
		for i := lo; i > lo/2 && i > 0; i-- {
			if r := rest[i-1]; r == ' ' || r == '/' || r == '\\' || r == ':' || r == ',' || r == '=' {
				cut = i
				break
			}
		}
		if cut < 1 {
			cut = 1 // nothing fits: one rune per line rather than a loop
		}
		out = append(out, prefix+strings.TrimRight(string(rest[:cut]), " "))
		rest = []rune(strings.TrimLeft(string(rest[cut:]), " "))
		prefix = indent
	}
	return out
}

// launchFailureParagraphs is the card's body before wrapping.
func launchFailureParagraphs(st *launchFailureState) []launchFailureLine {
	var p []launchFailureLine
	add := func(kind launchFailureLineKind, text string) {
		if kind == launchLineGap || strings.TrimSpace(text) != "" {
			p = append(p, launchFailureLine{kind: kind, text: text})
		}
	}
	if st.title == "" {
		// The classic form: why it failed and what to do.
		add(launchLineHeading, "Reason")
		add(launchLineBody, st.reason)
		add(launchLineGap, "")
		add(launchLineHeading, "Suggested fix")
		add(launchLineBody, st.fix)
		if st.log != "" {
			add(launchLineGap, "")
			add(launchLineDim, "Log: "+st.log)
		}
		launchFailureReportParagraphs(st, add)
		return p
	}
	if st.summary != "" {
		add(launchLineBody, st.summary)
		add(launchLineGap, "")
	}
	for i, issue := range st.issues {
		heading := issue.Title
		if len(st.issues) > 1 {
			heading = strconv.Itoa(i+1) + ". " + heading
		}
		add(launchLineHeading, heading)
		if len(issue.Files) > 0 {
			add(launchLineDim, "Missing:")
			for _, f := range issue.Files {
				add(launchLinePath, "• "+f)
			}
		}
		if issue.Where != "" {
			// The folder the missing files go in. A finding about one file
			// (a core that cannot load, a game file) names that file, and
			// "Copy to: …/mgba_libretro.so" read as an instruction; a file is
			// told from a folder by its extension (the card is drawn every
			// frame, so nothing is asked of the disk).
			if len(issue.Files) == 0 && filepath.Ext(issue.Where) != "" {
				add(launchLineDim, "Location:")
			} else {
				add(launchLineDim, "Copy to:")
			}
			add(launchLinePath, issue.Where)
		}
		add(launchLineBody, issue.Detail)
		add(launchLineGap, "")
	}
	if len(st.logLines) > 0 {
		add(launchLineHeading, "From the log")
		for _, line := range st.logLines {
			add(launchLineDim, line)
		}
		add(launchLineGap, "")
	}
	if st.log != "" {
		add(launchLineDim, "Full log: "+st.log)
	}
	launchFailureReportParagraphs(st, add)
	return p
}

// launchFailureReportParagraphs adds the bug report to the card's body: what
// Submit sends, and every line of the details it sends.
func launchFailureReportParagraphs(st *launchFailureState, add func(launchFailureLineKind, string)) {
	d := st.report
	if d == nil {
		return
	}
	add(launchLineGap, "")
	add(launchLineHeading, "Bug report")
	add(launchLineBody, "Submit files this failure as a public issue on JukaHub's GitHub: the details below, this "+
		"device's diagnostics and JukaHub's recent log. Account names, keys and network addresses are taken out; "+
		"folder and file names stay, because a launch bug turns on them. Cancel sends nothing.")
	if d.seenRef != "" {
		add(launchLineBody, "You reported this same failure at "+d.seenAt.Format("15:04")+" ("+d.seenRef+"); Submit files it again.")
	}
	add(launchLineDim, "Reference "+d.ref)
	add(launchLineGap, "")
	if !d.ready {
		add(launchLineDim, "Collecting the launch details…")
		return
	}
	for _, line := range strings.Split(strings.TrimRight(d.details, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			add(launchLineGap, "")
			continue
		}
		add(launchLineMono, strings.TrimRight(line, " "))
	}
}

// launchFailureFonts are the card's four sizes.
type launchFailureFonts struct {
	title, head, body, mono     *ttf.Font
	titleH, headH, bodyH, monoH int32
}

// launchFailureLoadFonts opens the card's type through the role scale
// (design.go): a heading for the title, body for the findings' titles, label
// for their text and caption for the report's details. ok is false when not
// even the theme's small font loads.
func launchFailureLoadFonts(config *Config) (launchFailureFonts, bool) {
	f := launchFailureFonts{
		title: FontForRole(config, TypeHeading),
		head:  FontForRole(config, TypeBody),
		body:  FontForRole(config, TypeLabel),
		mono:  FontForRole(config, TypeCaption),
	}
	fallback, _ := getCachedFont(config, "small")
	for _, font := range []**ttf.Font{&f.title, &f.head, &f.body, &f.mono} {
		if *font == nil {
			*font = fallback
		}
		if *font == nil {
			return f, false
		}
	}
	f.titleH = int32(f.title.Height())
	f.headH = int32(f.head.Height())
	f.bodyH = int32(f.body.Height())
	f.monoH = int32(f.mono.Height())
	return f, true
}

// launchFailureWrap wraps the card's body to textW, in the card's fonts.
func launchFailureWrap(st *launchFailureState, f launchFailureFonts, textW int32) {
	measure := func(font *ttf.Font) func(string) int32 {
		return func(s string) int32 {
			w, _, _ := font.SizeUTF8(s)
			return int32(w)
		}
	}
	bodyW, headW, monoW := measure(f.body), measure(f.head), measure(f.mono)
	lines := make([]launchFailureLine, 0, 64)
	for _, para := range launchFailureParagraphs(st) {
		switch para.kind {
		case launchLineGap:
			lines = append(lines, para)
			continue
		case launchLineMono:
			for _, line := range launchWrapKeep(monoW, para.text, textW) {
				lines = append(lines, launchFailureLine{kind: para.kind, text: line})
			}
			continue
		}
		m := bodyW
		if para.kind == launchLineHeading {
			m = headW
		}
		for _, line := range launchWrapText(m, para.text, textW) {
			lines = append(lines, launchFailureLine{kind: para.kind, text: line})
		}
	}
	st.lines = lines
	st.linesW = textW
}

func renderLaunchFailure(renderer *sdl.Renderer, config *Config) {
	if !launchFailure.open || renderer == nil || config == nil {
		return
	}
	// A Submit that waited too long for its details goes now (launchreport.go).
	launchReportWaitTick()
	if !launchFailure.open {
		return
	}
	f, ok := launchFailureLoadFonts(config)
	if !ok {
		return
	}
	st := &launchFailure
	panelW := int32(860)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	const padX = int32(24)
	// Room on the right for the scroll bar.
	textW := panelW - 2*padX - 12
	if st.lines == nil || st.linesW != textW {
		launchFailureWrap(st, f, textW)
	}
	lineH := func(kind launchFailureLineKind) int32 {
		switch kind {
		case launchLineHeading:
			return f.headH + 6
		case launchLineGap:
			return 10
		case launchLineMono:
			return f.monoH + 2
		}
		return f.bodyH + 4
	}
	contentH := int32(0)
	for _, line := range st.lines {
		contentH += lineH(line.kind)
	}
	headTop := 20 + f.titleH + 14
	footH := int32(56)
	if st.report != nil || st.tryDirect != nil {
		footH = 72
	}
	panelH := headTop + contentH + footH
	if panelH < 300 {
		panelH = 300
	}
	if panelH > screenHeight-32 {
		panelH = screenHeight - 32
	}
	bodyH := panelH - headTop - footH
	// The furthest the card scrolls: the first line from which the rest fits.
	st.maxScroll = 0
	rest := int32(0)
	for i := len(st.lines) - 1; i >= 0; i-- {
		rest += lineH(st.lines[i].kind)
		if rest > bodyH {
			st.maxScroll = i + 1
			break
		}
	}
	if st.scroll > st.maxScroll {
		st.scroll = st.maxScroll
	}
	st.page = int(bodyH/(f.monoH+2)) - 2

	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	st.card = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, st.card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	title := st.title
	if title == "" {
		title = "Couldn't launch"
		if st.target != "" {
			title += " " + st.target
		}
	}
	renderText(renderer, config, f.title, ellipsize(f.title, title, panelW-2*padX), ColorTextPrimary(), panelX+padX, panelY+20)

	y := panelY + headTop
	bottom := y + bodyH
	scrolledH := int32(0)
	for i := 0; i < st.scroll && i < len(st.lines); i++ {
		scrolledH += lineH(st.lines[i].kind)
	}
	for i := st.scroll; i < len(st.lines); i++ {
		line := st.lines[i]
		h := lineH(line.kind)
		if y+h > bottom {
			break
		}
		switch line.kind {
		case launchLineHeading:
			renderText(renderer, config, f.head, line.text, ColorTextPrimary(), panelX+padX, y)
		case launchLinePath:
			renderText(renderer, config, f.body, line.text, ColorTextAccent(), panelX+padX, y)
		case launchLineDim:
			renderText(renderer, config, f.body, line.text, ColorTextTertiary(), panelX+padX, y)
		case launchLineBody:
			renderText(renderer, config, f.body, line.text, ColorTextSecondary(), panelX+padX, y)
		case launchLineMono:
			renderText(renderer, config, f.mono, line.text, ColorTextSecondary(), panelX+padX, y)
		}
		y += h
	}
	if st.maxScroll > 0 {
		if over := contentH - bodyH; scrolledH > over {
			scrolledH = over
		}
		tbxDrawScrollbar(renderer, panelX+panelW-padX+6, panelY+headTop, bodyH, contentH, scrolledH)
	}

	footY := panelY + panelH - footH
	if st.report == nil && st.tryDirect == nil {
		st.tryRect, st.submitRect, st.cancelRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		hints := []FooterHint{{"A", "Dismiss"}}
		if st.maxScroll > 0 {
			hints = append([]FooterHint{{"Up/Down", "Scroll"}}, hints...)
		}
		footer := ellipsize(f.body, hintLine(hints...), panelW-2*padX)
		fw, fh := measureText(f.body, footer)
		renderText(renderer, config, f.body, footer, ColorTextTertiary(), panelX+(panelW-fw)/2, footY+(footH-fh)/2)
		return
	}
	// The buttons, right to left: Submit report, Cancel, and for a port that
	// met BusyBox through the firmware's launcher, Try it directly.
	st.tryRect, st.submitRect, st.cancelRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	const buttonH = int32(46)
	by := footY + (footH-buttonH)/2
	left := panelX + panelW - padX
	collecting, label := false, ""
	if st.report != nil {
		collecting = st.report.waiting && !st.report.ready
		label = "Submit report"
		if collecting {
			label = "Collecting details…"
		}
		sw := tbxButtonWidth(f.head, label, 170)
		cw := tbxButtonWidth(f.head, "Cancel", 120)
		st.submitRect = sdl.Rect{X: left - sw, Y: by, W: sw, H: buttonH}
		st.cancelRect = sdl.Rect{X: st.submitRect.X - 12 - cw, Y: by, W: cw, H: buttonH}
		left = st.cancelRect.X - 12
	}
	if st.tryDirect != nil {
		tw := tbxButtonWidth(f.head, "Try it directly", 190)
		st.tryRect = sdl.Rect{X: left - tw, Y: by, W: tw, H: buttonH}
		left = st.tryRect.X - 12
	}
	if st.tryDirect != nil {
		tbxDrawButton(renderer, config, f.head, st.tryRect, "Try it directly", true, st.focus == launchFocusTry, false)
	}
	if st.report != nil {
		tbxDrawButton(renderer, config, f.head, st.cancelRect, "Cancel", false, st.focus == launchFocusCancel, false)
		tbxDrawButton(renderer, config, f.head, st.submitRect, label, true, st.focus == launchFocusSubmit, collecting)
	}
	var hints []FooterHint
	if st.report != nil {
		hints = []FooterHint{{"Left/Right", "Choose"}, {"B", "Cancel"}}
	} else if st.tryDirect != nil {
		hints = []FooterHint{{"B", "Cancel"}}
	}
	if st.maxScroll > 0 {
		hints = append([]FooterHint{{"Up/Down", "Scroll"}, {"L1/R1", "Page"}}, hints...)
	}
	hintW := left - panelX - padX - 16
	if len(hints) > 0 && hintW > 40 {
		hint := ellipsize(f.mono, hintLine(hints...), hintW)
		_, hh := measureText(f.mono, hint)
		renderText(renderer, config, f.mono, hint, ColorTextTertiary(), panelX+padX, footY+(footH-hh)/2)
	}
}
