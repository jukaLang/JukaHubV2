package main

// javaui.go — Tools → Java: which Java JukaHub uses, installing, updating
// and removing its own (Eclipse Temurin, javart_install.go), and the ways to
// use it: Files on the Java folder, the Terminal, and what runs here at all.
//
// Top to bottom: the Java in use (JukaHub's, or one the computer has) with
// its version, kind, size and folder; an update check (by itself at most once
// a day, when the screen opens); the version and kind to install (Left/Right
// or A change them) and Install; Remove; then Run a Java program, Open the
// Terminal and What runs here. Installs run in the background with the
// install card; one at a time (javaInstallBegin).
//
// javaEnsure (javart.go) brings the user here when something needs a Java
// that is not there: the screen opens with an offer that says why, and the
// thing that asked runs once the install is done.
//
// The Java folder is where the user keeps programs: Java at the top of the SD
// card on the handheld, Documents/JukaHub/Java on a PC. It gets Hello.java (a
// first program that reads a name — the Terminal's input at work) and a
// README.txt the first time, never written over.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const javaSceneName = "JavaRuntime"

// javaOfferMaxAge drops an offer whose screen never opened in time.
const javaOfferMaxAge = 30 * time.Second

type javaRow struct {
	id   string
	spec tbxRowSpec
}

// javaOffer is what javaEnsure asked to install, and what runs after.
type javaOffer struct {
	why      string
	major    int
	kind     string
	minMajor int
	needJDK  bool
	then     func(rt javaRuntime)
	at       time.Time
}

// javaScreenInfo is what a background read found: the install marker, the
// folder's size and the Java folder.
type javaScreenInfo struct {
	managed    javaInstalled
	hasManaged bool
	size       int64
	userDir    string
}

// javaUpdateState is the last update check of JukaHub's Java.
type javaUpdateState struct {
	checking bool
	checked  time.Time
	latest   string
	newer    bool
	err      string
}

type javaScreenState struct {
	visit   tbxVisit
	nav     tbxNav
	builtAt time.Time
	header  UIPageHeader
	status  []javaRow
	setup   []javaRow
	use     []javaRow
	// major and kind are what Install installs; picked is true once the
	// user changed them here.
	major  int
	kind   string
	picked bool
	info   javaScreenInfo
	infoAt time.Time
	// infoBusy is a background read in flight.
	infoBusy bool
	update   javaUpdateState
	offer    *javaOffer
}

var javaScreen javaScreenState

// javaOpenScreen opens Tools → Java. The Tools hub and Home search call it.
func javaOpenScreen(config *Config) {
	tbxOpenScene(config, javaSceneName)
}

type javaScene struct{ sceneBase }

func (javaScene) render(r *sdl.Renderer, c *Config) bool {
	if javaScreen.visit.begin() {
		javaScreen.nav.scroll = 0
		javaScreenChanged()
		javaMaybeCheckUpdate()
		if o := javaScreen.offer; o != nil {
			javaScreen.offer = nil
			if time.Since(o.at) <= javaOfferMaxAge {
				javaOpenOfferSheet(c, o, true)
			}
		}
	}
	javaRenderScreen(r, c)
	return true
}

func (javaScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), javaSceneAction)
}

func (javaScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), javaSceneAction)
}

func init() { registerScene(javaScene{sceneBase{name: javaSceneName}}) }

// javaHubTile is the Tools hub's tile.
func javaHubTile() toolsHubTile {
	tile := toolsHubTile{id: "java", label: "Java", icon: "java", value: "Run .jar, .java and .class programs"}
	switch rt, ok := javaFind(); {
	case javaInstallBusy() != "":
		tile.value, tile.tone = "Working: "+javaInstallBusy(), toolsHubToneBusy
	case ok:
		tile.value, tile.tone = "Java "+strconv.Itoa(rt.Major)+" ready · run .jar and .java files", toolsHubToneGood
	}
	return tile
}

// javaScreenChanged makes the screen read the disk again (an install or a
// removal finished). Main thread.
func javaScreenChanged() {
	javaScreen.infoAt = time.Time{}
	javaScreen.builtAt = time.Time{}
}

// javaScreenRefresh reads the marker, the folder size and the Java folder on
// a goroutine; the next frame has them.
func javaScreenRefresh() {
	if javaScreen.infoBusy {
		return
	}
	javaScreen.infoBusy = true
	go func() {
		var in javaScreenInfo
		in.managed, in.hasManaged = javaManagedInfo()
		if in.hasManaged {
			in.size = javaDirSize(javaManagedDir())
		}
		in.userDir = javaUserDir()
		tbxPost(func() {
			javaScreen.info, javaScreen.infoAt, javaScreen.infoBusy = in, time.Now(), false
			javaScreen.builtAt = time.Time{}
		})
	}()
}

// javaDirSize adds up the files below dir (bounded: a runtime has a few
// thousand).
func javaDirSize(dir string) int64 {
	var total int64
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > javaMaxFiles {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// javaPrefKind is the kind an offer installs when nothing asks for a JDK:
// the kind JukaHub's Java already is, else the one picked here, else the JRE.
func javaPrefKind() string {
	if rt, ok := javaFind(); ok && rt.Managed {
		if rt.Javac != "" {
			return javaKindJDK
		}
		return javaKindJRE
	}
	if javaScreen.picked && javaScreen.kind != "" {
		return javaScreen.kind
	}
	return javaKindJRE
}

// javaOfferInstall brings up the Java screen with an install offer that says
// why (javaEnsure). Main thread.
func javaOfferInstall(config *Config, why string, major int, kind string, minMajor int, needJDK bool, then func(rt javaRuntime)) {
	if config == nil {
		config = appConfig
	}
	o := &javaOffer{why: why, major: major, kind: kind, minMajor: minMajor, needJDK: needJDK, then: then, at: time.Now()}
	if tbxCurrentScene(config) == javaSceneName {
		javaOpenOfferSheet(config, o, false)
		return
	}
	javaScreen.offer = o
	tbxOpenScene(config, javaSceneName)
	if idx := sceneIndexExact(config, javaSceneName); idx < 0 || pendingSceneIndex != idx {
		javaScreen.offer = nil
		ShowToast(why+" — install it in Tools → Java", ToastKindWarning)
	}
}

// javaOpenOfferSheet asks to install what an offer needs. back is true when
// the offer brought the user here: "Not now" then returns to where they were.
func javaOpenOfferSheet(config *Config, o *javaOffer, back bool) {
	what := "Java " + strconv.Itoa(o.major) + " (" + javaKindLabel(o.kind) + ")"
	body := o.why + ".\n\n" + "JukaHub installs " + what + ": Eclipse Temurin from Adoptium, " + javaSizeText(o.kind) +
		" to download, used only when it matches the SHA-256 Adoptium publishes for it. It goes in JukaHub's own folder, and Tools → Java removes it again."
	if o.needJDK {
		body += "\n\nThe JDK is the JRE plus the tools that build programs: javac, jshell, and running a .java file straight from its source."
	}
	if javaScreen.info.hasManaged {
		m := javaScreen.info.managed
		if m.Major != o.major || m.Kind != o.kind {
			body += "\n\nIt replaces JukaHub's Java " + strconv.Itoa(m.Major) + " (" + javaKindLabel(m.Kind) + ") once the new one has started here."
		}
	}
	var notNow func()
	if back {
		notNow = func() { goBackScene(config) }
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Install " + what + "?",
		subtitle: o.why,
		icon:     "java",
		body:     body,
		buttons: []tbxSheetButton{
			{label: "Not now", run: notNow},
			{label: "Install", primary: true, run: func() { javaStartInstall(o.major, o.kind, o.then) }},
		},
		focus: 1,
	})
}

// ── Java folder ──────────────────────────────────────────────────────────

// javaUserDir is the user's Java folder: Java at the top of the SD card on
// the handheld, Documents/JukaHub/Java on a PC. It reads the disk (a stat).
func javaUserDir() string {
	if IsTrimuiDevice() {
		root := strings.TrimSpace(defaultTrimuiSDRoot())
		if root == "" {
			root = "/mnt/SDCARD"
		}
		return filepath.Join(root, "Java")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if data, derr := P().DataDir(); derr == nil && data != "" {
			return filepath.Join(data, "Java")
		}
		return ""
	}
	if dirExists(filepath.Join(home, "Documents")) {
		return filepath.Join(home, "Documents", "JukaHub", "Java")
	}
	return filepath.Join(home, "JukaHub", "Java")
}

// javaHelloSource is the sample program: it prints where it runs and reads a
// name, which shows the Terminal passing typed lines to a program. ASCII
// only, so every Java's compiler reads it the same.
const javaHelloSource = `// Hello.java - a first Java program for JukaHub.
//
// Run it: press A on this file in Files (it needs the JDK: Tools -> Java,
// Kind: JDK), or type  java Hello.java  in the Terminal. It asks for your
// name: type it and press Enter (Start on a pad). The Terminal sends what
// you type to the program while it runs.

import java.util.Scanner;

public class Hello {
    public static void main(String[] args) {
        System.out.println("Hello from Java " + System.getProperty("java.version")
                + " on " + System.getProperty("os.name") + " (" + System.getProperty("os.arch") + ")!");
        System.out.print("What is your name? ");
        System.out.flush();
        Scanner in = new Scanner(System.in);
        String name = in.hasNextLine() ? in.nextLine().trim() : "";
        if (name.isEmpty()) {
            name = "friend";
        }
        System.out.println("Nice to meet you, " + name + ". Have fun with Java!");
    }
}
`

// javaReadmeText is the folder's README.txt.
const javaReadmeText = `Java programs for JukaHub
=========================

Keep Java programs here (or anywhere) and press A on them in Files:

  .jar    A program in one file. It starts when its manifest names a
          Main-Class. Phone games (J2ME) go in Roms/J2ME and play from Games.
  .java   Source code. It runs straight from the source with the JDK
          (Tools -> Java -> Kind: JDK). Try Hello.java.
  .class  A compiled class with a main method. Its folders must match its
          package name (com/example/Hello.class for com.example.Hello).

Programs that print text run in JukaHub's Terminal, where what you type
reaches them. Programs with windows (Swing, JavaFX, LWJGL games) open in
their own window on a PC; the handheld has no desktop for windows.

Tools -> Java installs, updates and removes Java (Eclipse Temurin).
Android apps (.apk) are not this kind of Java program and do not run here.
`

// javaEnsureUserDir makes the Java folder and writes the sample and the
// README into it when they are missing. Off the main thread.
func javaEnsureUserDir() (string, error) {
	dir := javaUserDir()
	if dir == "" {
		return "", errors.New("no folder for Java programs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for name, text := range map[string]string{"Hello.java": javaHelloSource, "README.txt": javaReadmeText} {
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			continue // there already, or the folder is read-only
		}
		_, werr := f.WriteString(text)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(p)
		}
	}
	return dir, nil
}

// javaOpenUserDir opens Files on the Java folder, the sample under the
// cursor.
func javaOpenUserDir(config *Config) {
	go func() {
		dir, err := javaEnsureUserDir()
		tbxPost(func() {
			if err != nil {
				ShowToast("The Java folder could not be made: "+firstLine(err.Error()), ToastKindError)
				return
			}
			chatShowInFiles(config, filepath.Join(dir, "Hello.java"))
		})
	}()
}

// ── Update check ─────────────────────────────────────────────────────────

// javaMaybeCheckUpdate checks GitHub for a newer build of JukaHub's Java when
// the last check is a day old. The screen calls it on each visit.
func javaMaybeCheckUpdate() {
	if javaScreen.update.checking {
		return
	}
	// A check that failed this session is not repeated on every visit: the
	// row says it failed, and A on it asks again.
	prev := javaScreen.update
	javaScreen.update.checking = true
	go func() {
		last := javaLoadUpdateCheck()
		m, ok := javaManagedInfo()
		st := javaUpdateState{}
		if t, err := time.Parse(time.RFC3339, last.Checked); err == nil && ok && last.Have == m.Name {
			st.checked, st.latest, st.newer = t, last.Latest, last.Latest != "" && last.Latest != m.Name
		}
		if ok && IsOnline() && time.Since(st.checked) > 24*time.Hour {
			if prev.err != "" && time.Since(prev.checked) <= 24*time.Hour {
				st = prev
			} else {
				st = javaRunUpdateCheck()
			}
		}
		tbxPost(func() {
			javaScreen.update = st
			javaScreen.builtAt = time.Time{}
		})
	}()
}

// javaRunUpdateCheck asks GitHub now. Off the main thread.
func javaRunUpdateCheck() javaUpdateState {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	asset, newer, err := javaCheckForUpdate(ctx)
	st := javaUpdateState{checked: time.Now()}
	if err != nil {
		st.err = plainNetworkError(err)
		return st
	}
	st.latest, st.newer = asset.Name, newer
	return st
}

// javaCheckNow is A on the update row with nothing newer known.
func javaCheckNow() {
	if javaScreen.update.checking {
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	javaScreen.update.checking = true
	javaScreen.builtAt = time.Time{}
	go func() {
		st := javaRunUpdateCheck()
		tbxPost(func() {
			javaScreen.update = st
			javaScreen.builtAt = time.Time{}
			switch {
			case st.err != "":
				ShowToast("Update check: "+st.err, ToastKindWarning)
			case !st.newer:
				ShowToast("Java is up to date", ToastKindSuccess)
			}
		})
	}()
}

// ── Model ────────────────────────────────────────────────────────────────

// javaMajorNote says what a version is for, in a few words.
func javaMajorNote(major int) string {
	switch major {
	case 8:
		return "the old classic; some older programs need it"
	case 11, 17:
		return "an older long-term release"
	case 21:
		return "the previous long-term release; Minecraft servers use it"
	case 25:
		return "the current long-term release (recommended)"
	}
	return ""
}

func javaBuildModel(now time.Time) {
	m := &javaScreen
	m.builtAt = now
	if m.infoAt.IsZero() || now.Sub(m.infoAt) > 15*time.Second {
		javaScreenRefresh()
	}
	in := m.info
	if !m.picked {
		m.major, m.kind = javaDefaultMajor, javaKindJRE
		if in.hasManaged {
			m.major, m.kind = in.managed.Major, in.managed.Kind
		}
	}
	all := javaKnownRuntimes()
	scanned := javaScanFresh() || len(all) > 0
	busy := javaInstallBusy()

	status, ink := "No Java yet", ColorTextTertiary()
	m.status = m.status[:0]
	cur := tbxRowSpec{icon: "java"}
	switch {
	case len(all) > 0 && all[0].Managed:
		rt := all[0]
		status, ink = "Java "+strconv.Itoa(rt.Major), ColorToastSuccess
		cur.title = javaDescribe(rt)
		cur.subtitle = rt.Home
		if in.hasManaged && in.size > 0 {
			cur.subtitle = formatSize(in.size) + " · " + rt.Home
		}
		cur.pill, cur.tone = "Ready", tbxToneGood
	case len(all) > 0:
		rt := all[0]
		status, ink = "Java "+strconv.Itoa(rt.Major), ColorTextSecondary()
		cur.title = javaDescribe(rt)
		cur.subtitle = rt.Home + " · JukaHub uses it until it installs a Java of its own"
		cur.pill, cur.tone = "In use", tbxToneGood
	case !scanned:
		cur.title, cur.subtitle = "Looking for Java…", "JAVA_HOME, the PATH and the usual folders"
		cur.pill, cur.tone = "…", tbxToneNeutral
	default:
		cur.title = "No Java yet"
		cur.subtitle = "Install one below: the JRE runs programs (" + javaSizeText(javaKindJRE) + ")"
		if note := javaScanNote(); note != "" {
			cur.subtitle = "A Java was found but does not start here: " + note
		}
		cur.pill, cur.tone = "Not installed", tbxToneWarn
	}
	m.status = append(m.status, javaRow{id: "current", spec: cur})
	for i := 1; i < len(all); i++ {
		rt := all[i]
		m.status = append(m.status, javaRow{id: "other:" + rt.Java, spec: tbxRowSpec{icon: "code", title: javaDescribe(rt),
			subtitle: rt.Home, pill: "Also here", tone: tbxToneNeutral, dim: true}})
	}
	if busy != "" {
		status, ink = "Working…", glassAccent()
		m.status = append(m.status, javaRow{id: "busy", spec: tbxRowSpec{icon: "download", title: busy,
			subtitle: "The install card shows how far it is; it can be stopped there", pill: "Working…", tone: tbxToneAccent}})
	}
	if in.hasManaged {
		up := tbxRowSpec{icon: "update", title: "Check for updates", subtitle: "A newer build of Java " + strconv.Itoa(in.managed.Major) + " with the latest fixes"}
		u := m.update
		switch {
		case u.checking:
			up.pill, up.tone = "Checking…", tbxToneNeutral
		case u.err != "":
			up.subtitle = "The last check failed: " + u.err
			up.pill, up.tone = "Check", tbxToneWarn
		case u.newer:
			up.title = "Update Java " + strconv.Itoa(in.managed.Major)
			up.subtitle = "Newer build: " + u.latest
			up.pill, up.tone = "Update", tbxToneAccent
		case !u.checked.IsZero():
			up.subtitle = "Checked " + u.checked.Local().Format("2 Jan 15:04") + " · " + in.managed.Name
			up.pill, up.tone = "Up to date", tbxToneGood
		default:
			up.pill, up.tone = "Check", tbxToneNeutral
		}
		m.status = append(m.status, javaRow{id: "update", spec: up})
	}
	m.header = tbxHeader("Java", "Run .jar, .java and .class programs · Eclipse Temurin", "java", status, ink)

	m.setup = m.setup[:0]
	m.setup = append(m.setup, javaRow{id: "major", spec: tbxRowSpec{icon: "java", title: "Version",
		subtitle: "Java " + strconv.Itoa(m.major) + ": " + javaMajorNote(m.major), pill: "Java " + strconv.Itoa(m.major), tone: tbxToneAccent}})
	kindSub := "JRE: runs programs (" + javaSizeText(javaKindJRE) + ")"
	if m.kind == javaKindJDK {
		kindSub = "JDK: also javac, jshell and java Hello.java (" + javaSizeText(javaKindJDK) + ")"
	}
	m.setup = append(m.setup, javaRow{id: "kind", spec: tbxRowSpec{icon: "code", title: "Kind", subtitle: kindSub,
		pill: javaKindLabel(m.kind), tone: tbxToneAccent}})
	what := "Java " + strconv.Itoa(m.major) + " (" + javaKindLabel(m.kind) + ")"
	inst := tbxRowSpec{icon: "download", title: "Install " + what,
		subtitle: "From Adoptium on GitHub, checked against its SHA-256 · " + javaSizeText(m.kind), pill: "Install", tone: tbxToneAccent}
	if in.hasManaged {
		if in.managed.Major == m.major && in.managed.Kind == m.kind {
			inst.title, inst.pill, inst.tone = "Reinstall "+what, "Reinstall", tbxToneNeutral
		} else {
			inst.title = "Switch to " + what
			inst.subtitle = "Replaces Java " + strconv.Itoa(in.managed.Major) + " (" + javaKindLabel(in.managed.Kind) + ") once the new one starts · " + javaSizeText(m.kind)
		}
	}
	if busy != "" {
		inst.pill, inst.tone, inst.dim = "Busy", tbxToneNeutral, true
	}
	m.setup = append(m.setup, javaRow{id: "install", spec: inst})
	if in.hasManaged {
		m.setup = append(m.setup, javaRow{id: "remove", spec: tbxRowSpec{icon: "trash", title: "Remove JukaHub's Java",
			subtitle: "Frees " + formatSize(in.size) + " · a Java the computer has stays", pill: "Remove", tone: tbxToneBad}})
	}

	m.use = m.use[:0]
	folder := in.userDir
	if folder == "" {
		folder = "the Java folder"
	}
	m.use = append(m.use,
		javaRow{id: "files", spec: tbxRowSpec{icon: "files", title: "Run a Java program",
			subtitle: "Files opens " + folder + " · A on a .jar, .java or .class runs it", pill: "Open", tone: tbxToneNeutral}},
		javaRow{id: "terminal", spec: tbxRowSpec{icon: "terminal", title: "Open the Terminal",
			subtitle: "java, javac, jshell and jar · programs read what you type", pill: "Open", tone: tbxToneNeutral}},
		javaRow{id: "about", spec: tbxRowSpec{icon: "info", title: "What runs here",
			subtitle: "Desktop Java programs and J2ME phone games — not Android apps", pill: "Read", tone: tbxToneNeutral}},
	)
}

func javaRenderScreen(renderer *sdl.Renderer, config *Config) {
	now := time.Now()
	if now.Sub(javaScreen.builtAt) > time.Second {
		javaBuildModel(now)
	}
	m := &javaScreen
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
	group := func(title, icon string, rows []javaRow) {
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
	place := "This computer"
	if IsTrimuiDevice() {
		place = "This handheld"
	}
	group(place, "java", m.status)
	group("Install", "download", m.setup)
	group("Use it", "terminal", m.use)
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// ── Input ────────────────────────────────────────────────────────────────

func javaSceneAction(config *Config, act tbxAction) bool {
	nav := &javaScreen.nav
	switch act {
	case tbxActConfirm:
		if nav.focus != "" {
			PlayActivateSound()
			javaActivate(config, nav.focus)
		}
		return true
	case tbxActLeft, tbxActRight:
		dir := 1
		if act == tbxActLeft {
			dir = -1
		}
		if javaCycle(nav.focus, dir) {
			PlayNavSound()
			return true
		}
	case tbxActBack:
		return false
	}
	return tbxNavAction(nav, act)
}

func javaSceneClick(mx, my int32, config *Config) bool {
	it, ok := javaScreen.nav.hit(mx, my)
	if !ok {
		return false
	}
	javaScreen.nav.focus = it.id
	PlayActivateSound()
	javaActivate(config, it.id)
	return true
}

func javaSceneHints() []FooterHint {
	switch javaScreen.nav.focus {
	case "major", "kind":
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "Left/Right", Label: "Change"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, {Button: "B", Label: "Back"}}
}

// javaCycle steps the version or the kind row.
func javaCycle(id string, dir int) bool {
	m := &javaScreen
	switch id {
	case "major":
		i := 0
		for k, v := range javaMajors {
			if v == m.major {
				i = k
			}
		}
		i = (i + dir + len(javaMajors)) % len(javaMajors)
		m.major = javaMajors[i]
	case "kind":
		if m.kind == javaKindJDK {
			m.kind = javaKindJRE
		} else {
			m.kind = javaKindJDK
		}
	default:
		return false
	}
	m.picked = true
	m.builtAt = time.Time{}
	return true
}

func javaActivate(config *Config, id string) {
	m := &javaScreen
	switch id {
	case "major", "kind":
		javaCycle(id, 1)
	case "install":
		if busy := javaInstallBusy(); busy != "" {
			ShowToast("Busy: "+busy+" — one moment", ToastKindInfo)
			return
		}
		javaConfirmInstall(config, m.major, m.kind)
	case "update":
		if m.update.newer && m.info.hasManaged {
			javaConfirmInstall(config, m.info.managed.Major, m.info.managed.Kind)
			return
		}
		javaCheckNow()
	case "remove":
		javaConfirmRemove(config)
	case "files":
		javaOpenUserDir(config)
	case "terminal":
		termOpenWithCommand(config, "java -version")
	case "about":
		javaOpenAboutSheet(config)
	case "current":
		if rt, ok := javaFind(); ok {
			tbxOpenSheet(config, tbxSheetSpec{title: javaDescribe(rt), subtitle: "The Java JukaHub uses", icon: "java",
				body: "## Folder\n" + rt.Home + "\n\n## Programs\n• java: " + rt.Java + javaToolLine("javac", rt.Javac) + javaToolLine("javaw", rt.Javaw) +
					"\n\nThe Terminal puts this Java first on its PATH and sets JAVA_HOME to its folder."})
		}
	}
}

// javaToolLine is a bullet for one tool when rt has it.
func javaToolLine(name, path string) string {
	if path == "" {
		return ""
	}
	return "\n• " + name + ": " + path
}

// javaConfirmInstall asks before an install (or an update) starts.
func javaConfirmInstall(config *Config, major int, kind string) {
	what := "Java " + strconv.Itoa(major) + " (" + javaKindLabel(kind) + ")"
	body := "Eclipse Temurin " + strconv.Itoa(major) + " from Adoptium's releases on GitHub: " + javaSizeText(kind) +
		" to download. It is used only when it matches the SHA-256 Adoptium publishes, and only after it has started here once."
	if javaScreen.info.hasManaged {
		old := javaScreen.info.managed
		body += "\n\nIt replaces JukaHub's Java " + strconv.Itoa(old.Major) + " (" + javaKindLabel(old.Kind) + "); close running Java programs first."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Install " + what + "?",
		subtitle: "Eclipse Temurin · " + javaSizeText(kind),
		icon:     "download",
		body:     body,
		buttons: []tbxSheetButton{{label: "Not now"}, {label: "Install", primary: true, run: func() {
			javaStartInstall(major, kind, nil)
		}}},
		focus: 1,
	})
}

// javaConfirmRemove asks before JukaHub's Java is deleted.
func javaConfirmRemove(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Remove JukaHub's Java?",
		subtitle: "You can install it again at any time",
		icon:     "trash",
		body:     "Its folder is deleted (" + formatSize(javaScreen.info.size) + "). Your Java programs and the Java folder stay. A Java the computer has is not touched.",
		buttons: []tbxSheetButton{{label: "Keep"}, {label: "Remove", primary: true, run: func() {
			if !javaInstallBegin(0, javaKindRemove) {
				ShowToast("Busy: "+javaInstallBusy()+" — one moment", ToastKindInfo)
				return
			}
			go func() {
				err := javaRemove()
				javaInstallEnd()
				tbxPost(func() {
					javaScreenChanged()
					if err != nil {
						PlayErrorSound()
						ShowToast("Java was not removed: "+firstLine(err.Error()), ToastKindError)
						return
					}
					ShowToast("JukaHub's Java is removed", ToastKindSuccess)
				})
			}()
		}}},
	})
}

// javaOpenAboutSheet says what this Java runs and what it does not.
func javaOpenAboutSheet(config *Config) {
	windows := "On a PC, a program with windows (Swing, JavaFX, LWJGL games) opens in its own window and JukaHub stays."
	if !javaDesktopAvailable() {
		windows = "This handheld has no desktop, so programs with windows (Swing, JavaFX, LWJGL games) cannot show them; Files offers to run such a program in the Terminal without a window, which works for programs that only touch a little AWT."
	}
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "What runs here",
		subtitle: "Java SE: desktop Java programs",
		icon:     "info",
		tall:     true,
		body: "## Runs\n" +
			"• .jar programs whose manifest names a Main-Class: A in Files, or java -jar in the Terminal.\n" +
			"• .java source files, straight from the source (needs the JDK).\n" +
			"• .class files with a main method, from the folder their package starts in.\n" +
			"• J2ME phone games (.jar with a MIDlet): they go in Roms/J2ME and play from Games.\n\n" +
			"## Text programs\nThey run in the Terminal. What you type while one runs goes to it (Enter, or Start on a pad); Ctrl+D ends its input and X (Ctrl+C) stops it.\n\n" +
			"## Windows\n" + windows + "\n\n" +
			"## Not Android apps\nAndroid apps are written in Java too, but they are built for Android: their code is DEX for Android's runtime (ART), and they need Android's own libraries. This Java cannot run an .apk; JukaHub's Android support runs native Android games only.",
	})
}
