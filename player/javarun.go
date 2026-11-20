package main

// javarun.go — A on a Java file in Files: a .jar, a .java source file or a
// compiled .class.
//
// What the file is decides what happens, and finding out reads the file, so
// it runs on a goroutine:
//
//   - A .jar's manifest (META-INF/MANIFEST.MF) names a Main-Class: a program,
//     started with `java -jar`. One that names a MIDlet is a phone game (J2ME):
//     it goes to javaMIDletOpener, the J2ME player, when there is one. One
//     that names neither is a library, which has nothing to start.
//   - A .java file runs straight from its source (`java Hello.java`), which
//     needs a JDK of Java 11 or newer.
//   - A .class file says its own name in its constant pool (this_class), so
//     com/example/Hello.class runs as `java -cp <the folder above com>
//     com.example.Hello`.
//
// The class file's version says which Java it needs at least (a class built
// for Java 21 cannot run on Java 17), and javaEnsure offers that Java when the
// one at hand is older.
//
// A program that opens windows — it uses AWT, Swing, JavaFX or LWJGL, found in
// the constant pools of a bounded number of its classes, or in a source
// file's text — starts on its own on a PC with a desktop (javaw on Windows)
// while JukaHub stays. Every other program runs in the Terminal, where what
// the user types reaches it (terminal.go's job input). The handheld has no
// desktop: a windowed program there says so first, and a second press runs it
// in the Terminal without a window (java.awt.headless).
//
// A Java file in Downloads may have come from anyone in the chat: like a
// script there (chatConfirmRun), it runs on a second press only.

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// javaFileKind is what a Java file turned out to be.
type javaFileKind int

const (
	javaFileNone    javaFileKind = iota
	javaFileApp                  // a .jar whose manifest names a Main-Class
	javaFileMIDlet               // a .jar for phones (J2ME)
	javaFileLibrary              // a .jar with neither
	javaFileSource               // a .java source file
	javaFileClass                // a compiled .class
)

const (
	// javaManifestMax bounds the manifest read from a jar.
	javaManifestMax = 64 << 10
	// javaClassMax bounds one class file read for its name and version.
	javaClassMax = 4 << 20
	// javaScanClasses and javaScanBytes bound the look for a window toolkit
	// in a jar's classes.
	javaScanClasses = 400
	javaScanBytes   = 24 << 20
	// javaSourceMax bounds a .java file read for its imports.
	javaSourceMax = 1 << 20
	// javaJarMaxEntries refuses a jar whose directory is absurdly long.
	javaJarMaxEntries = 200000
)

// javaFilePlan is what A on a Java file does.
type javaFilePlan struct {
	path     string // the file
	kind     javaFileKind
	main     string // the Main-Class (jar) or the class's name (.class)
	dir      string // where it runs: the file's folder, or a .class's classpath root
	minMajor int    // the oldest Java that runs it, from the class file version
	windowed bool   // it opens windows (AWT, Swing, JavaFX, LWJGL)
}

// javaIsFile reports whether A on name is a Java program's.
func javaIsFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jar", ".java", ".class":
		return true
	}
	return false
}

// javaWindowMarkers are constant-pool strings of the window toolkits.
var javaWindowMarkers = [][]byte{[]byte("java/awt/"), []byte("javax/swing/"), []byte("javafx/"), []byte("org/lwjgl/")}

// javaSourceWindowMarkers are the same toolkits as a source file names them.
var javaSourceWindowMarkers = [][]byte{[]byte("java.awt"), []byte("javax.swing"), []byte("javafx."), []byte("org.lwjgl")}

func javaHasMarker(data []byte, markers [][]byte) bool {
	for _, m := range markers {
		if bytes.Contains(data, m) {
			return true
		}
	}
	return false
}

// javaParseManifest reads a jar manifest: "Name: value" lines, a line that
// starts with one space continuing the one before. Names are compared without
// case, as the format says, so the keys are lower-case. Pure.
func javaParseManifest(data []byte) map[string]string {
	out := map[string]string{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	last := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, " ") {
			if last != "" {
				out[last] += line[1:]
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			last = ""
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			last = ""
			continue
		}
		// The main section comes first; a later section's attribute of the
		// same name (a per-entry one) does not replace it.
		if _, seen := out[key]; seen {
			last = ""
			continue
		}
		out[key] = strings.TrimPrefix(val, " ")
		last = key
	}
	for k, v := range out {
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// javaManifestKind classifies a jar by its manifest: a MIDlet is a phone
// game, a Main-Class a program, neither a library. Pure.
func javaManifestKind(m map[string]string) (javaFileKind, string) {
	if m["midlet-1"] != "" || m["microedition-profile"] != "" || m["microedition-configuration"] != "" {
		return javaFileMIDlet, ""
	}
	if main := strings.TrimSpace(m["main-class"]); main != "" {
		return javaFileApp, main
	}
	return javaFileLibrary, ""
}

// javaClassInfo reads a class file's own name (this_class, in internal form:
// "com/example/Hello") and its major version. Pure; every length is checked
// against the data.
func javaClassInfo(data []byte) (string, int, error) {
	bad := errors.New("it is not a valid Java class file")
	if len(data) < 10 || binary.BigEndian.Uint32(data) != 0xCAFEBABE {
		return "", 0, bad
	}
	major := int(binary.BigEndian.Uint16(data[6:]))
	count := int(binary.BigEndian.Uint16(data[8:]))
	pos := 10
	// The constant pool: index → offset of the entry's tag.
	offsets := make([]int, count)
	for i := 1; i < count; i++ {
		if pos >= len(data) {
			return "", 0, bad
		}
		offsets[i] = pos
		tag := data[pos]
		pos++
		size := 0
		switch tag {
		case 1: // Utf8
			if pos+2 > len(data) {
				return "", 0, bad
			}
			size = 2 + int(binary.BigEndian.Uint16(data[pos:]))
		case 3, 4, 9, 10, 11, 12, 17, 18: // Integer, Float, refs, NameAndType, Dynamic, InvokeDynamic
			size = 4
		case 5, 6: // Long, Double take two slots
			size = 8
			i++
		case 7, 8, 16, 19, 20: // Class, String, MethodType, Module, Package
			size = 2
		case 15: // MethodHandle
			size = 3
		default:
			return "", 0, bad
		}
		pos += size
		if pos > len(data) {
			return "", 0, bad
		}
	}
	if pos+4 > len(data) {
		return "", 0, bad
	}
	this := int(binary.BigEndian.Uint16(data[pos+2:]))
	if this <= 0 || this >= count || offsets[this] == 0 || data[offsets[this]] != 7 {
		return "", 0, bad
	}
	at := offsets[this] + 1
	if at+2 > len(data) {
		return "", 0, bad
	}
	nameIdx := int(binary.BigEndian.Uint16(data[at:]))
	if nameIdx <= 0 || nameIdx >= count || offsets[nameIdx] == 0 || data[offsets[nameIdx]] != 1 {
		return "", 0, bad
	}
	at = offsets[nameIdx] + 1
	n := int(binary.BigEndian.Uint16(data[at:]))
	if at+2+n > len(data) {
		return "", 0, bad
	}
	name := string(data[at+2 : at+2+n])
	if !javaValidClassName(name) {
		return "", 0, errors.New("the class file has a name Java cannot start")
	}
	return name, major, nil
}

// javaValidClassName accepts an internal class name whose parts are Java
// names: nothing a launcher could take for an option or a path.
func javaValidClassName(name string) bool {
	if name == "" || len(name) > 1024 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			return false
		}
		for i, r := range part {
			switch {
			case r == '_' || r == '$' || unicode.IsLetter(r):
			case unicode.IsDigit(r) && i > 0:
			default:
				return false
			}
		}
	}
	return true
}

// javaMajorForClass is the Java a class file version needs: 52 is Java 8,
// 65 Java 21, 69 Java 25. Older class files run on Java 8.
func javaMajorForClass(major int) int {
	if v := major - 44; v > 8 {
		return v
	}
	return 8
}

// javaReadLimited reads at most max bytes of r.
func javaReadLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// javaReadZipEntry reads at most max bytes of one jar entry.
func javaReadZipEntry(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return javaReadLimited(rc, max)
}

// javaInspectJar reads a jar's manifest and, for a program, which Java its
// main class needs and whether it opens windows.
func javaInspectJar(path string) (javaFilePlan, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return javaFilePlan{}, errors.New("it is not a readable .jar (a zip archive)")
	}
	defer r.Close()
	if len(r.File) > javaJarMaxEntries {
		return javaFilePlan{}, errors.New("the .jar has more files than JukaHub reads")
	}
	manifest := map[string]string{}
	for _, f := range r.File {
		if strings.EqualFold(f.Name, "META-INF/MANIFEST.MF") {
			if data, err := javaReadZipEntry(f, javaManifestMax); err == nil {
				manifest = javaParseManifest(data)
			}
			break
		}
	}
	kind, main := javaManifestKind(manifest)
	plan := javaFilePlan{path: path, kind: kind, main: main, dir: filepath.Dir(path), minMajor: 8}
	if kind != javaFileApp {
		return plan, nil
	}
	// Toolkits a program carries inside its jar name themselves.
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "org/lwjgl/") || strings.HasPrefix(f.Name, "javafx/") || strings.HasPrefix(f.Name, "com/badlogic/gdx/") {
			plan.windowed = true
			break
		}
	}
	mainEntry := strings.ReplaceAll(main, ".", "/") + ".class"
	budget := int64(javaScanBytes)
	scanned := 0
	look := func(f *zip.File) []byte {
		max := int64(javaClassMax)
		if budget < max {
			max = budget
		}
		data, err := javaReadZipEntry(f, max)
		if err != nil {
			return nil
		}
		budget -= int64(len(data))
		scanned++
		if !plan.windowed && javaHasMarker(data, javaWindowMarkers) {
			plan.windowed = true
		}
		return data
	}
	// The main class first: its version is the program's.
	for _, f := range r.File {
		if f.Name == mainEntry {
			if data := look(f); data != nil {
				if _, major, err := javaClassInfo(data); err == nil {
					plan.minMajor = javaMajorForClass(major)
				}
			}
			break
		}
	}
	for _, f := range r.File {
		if plan.windowed || scanned >= javaScanClasses || budget <= 0 {
			break
		}
		if f.Name == mainEntry || !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		look(f)
	}
	return plan, nil
}

// javaInspectClass reads a .class file's name and finds the folder its
// package path starts in.
func javaInspectClass(path string) (javaFilePlan, error) {
	f, err := os.Open(path)
	if err != nil {
		return javaFilePlan{}, err
	}
	data, err := javaReadLimited(f, javaClassMax+1)
	f.Close()
	if err != nil {
		return javaFilePlan{}, err
	}
	if len(data) > javaClassMax {
		return javaFilePlan{}, errors.New("the class file is larger than JukaHub reads")
	}
	name, major, err := javaClassInfo(data)
	if err != nil {
		return javaFilePlan{}, err
	}
	root, err := javaClassRoot(path, name)
	if err != nil {
		return javaFilePlan{}, err
	}
	return javaFilePlan{path: path, kind: javaFileClass, main: strings.ReplaceAll(name, "/", "."), dir: root,
		minMajor: javaMajorForClass(major), windowed: javaHasMarker(data, javaWindowMarkers)}, nil
}

// javaClassRoot is the classpath folder for the class file at path whose
// internal name is name: com/example/Hello.class sits in <root>/com/example.
// Names are compared without case (the SD card and Windows ignore it).
func javaClassRoot(path, name string) (string, error) {
	parts := strings.Split(name, "/")
	simple := parts[len(parts)-1]
	dotted := strings.ReplaceAll(name, "/", ".")
	if !strings.EqualFold(filepath.Base(path), simple+".class") {
		return "", fmt.Errorf("the file holds the class %s — it must be called %s.class", dotted, simple)
	}
	root := filepath.Dir(path)
	for i := len(parts) - 2; i >= 0; i-- {
		if !strings.EqualFold(filepath.Base(root), parts[i]) {
			return "", fmt.Errorf("the class is %s, so it must be in a %s folder", dotted, strings.Join(parts[:len(parts)-1], "/"))
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("the class is %s, so it must be in a %s folder", dotted, strings.Join(parts[:len(parts)-1], "/"))
		}
		root = parent
	}
	return root, nil
}

// javaInspectSource looks at a .java file's imports for a window toolkit.
func javaInspectSource(path string) (javaFilePlan, error) {
	f, err := os.Open(path)
	if err != nil {
		return javaFilePlan{}, err
	}
	data, err := javaReadLimited(f, javaSourceMax)
	f.Close()
	if err != nil {
		return javaFilePlan{}, err
	}
	return javaFilePlan{path: path, kind: javaFileSource, dir: filepath.Dir(path), minMajor: 11,
		windowed: javaHasMarker(data, javaSourceWindowMarkers)}, nil
}

// javaInspect works out what A on path does. Off the main thread.
func javaInspect(path string) (javaFilePlan, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jar":
		return javaInspectJar(path)
	case ".class":
		return javaInspectClass(path)
	case ".java":
		return javaInspectSource(path)
	}
	return javaFilePlan{}, errors.New("it is not a Java file")
}

// javaArm is a press waiting for its second one: a program from Downloads,
// or a windowed program on the handheld.
type javaArm struct {
	path string
	at   time.Time
}

var javaRunArm, javaHeadlessArm javaArm

// javaArmWindow is how long a first press waits for its second one.
const javaArmWindow = 6 * time.Second

// javaArmedNow reports whether a holds path from a recent press.
func javaArmedNow(a *javaArm, path string) bool {
	return a.path != "" && filesSamePath(a.path, path) && time.Since(a.at) <= javaArmWindow
}

// javaArmed reports whether path was pressed within the last few seconds,
// and arms it otherwise.
func javaArmed(a *javaArm, path string) bool {
	if javaArmedNow(a, path) {
		a.path = ""
		return true
	}
	a.path, a.at = path, time.Now()
	return false
}

// javaConfirmRun guards A on a Java file in Downloads, the way chatConfirmRun
// guards a script there.
func javaConfirmRun(path string) bool {
	dir := chatSaveDir()
	if dir == "" || !isPathWithin(path, dir) {
		return true
	}
	// The press that answers "no desktop, press again" (javaLaunchPlan) got
	// past this question on the press before; it is not asked twice.
	if javaArmedNow(&javaHeadlessArm, path) {
		return true
	}
	if javaArmed(&javaRunArm, path) {
		return true
	}
	PlayNavSound()
	showToast(hintSentence("This Java program is in Downloads — it may have come from the chat. Press %s again to run it", "A", "Run"), ToastWarn())
	return false
}

// javaFilesOpen is A on a .jar, .java or .class in Files. It reports whether
// the file was a Java one (and so handled).
func javaFilesOpen(config *Config, entry FileEntry) bool {
	if entry.IsDir || !javaIsFile(entry.Name) {
		return false
	}
	path, err := resolveExplorerEntry(config, entry.Path)
	if err != nil {
		showToast("Cannot open "+entry.Name+": "+filesFriendlyError(err), ToastError())
		return true
	}
	// A press while a file is still being read (a big jar on the card) would
	// start the program twice.
	if javaInspecting {
		return true
	}
	if !javaConfirmRun(path) {
		return true
	}
	name := entry.Name
	javaInspecting = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("java", "reading %s failed: %v", path, r)
				tbxPost(func() { javaFilesRun(config, name, javaFilePlan{}, errors.New("JukaHub could not read it")) })
			}
		}()
		plan, err := javaInspect(path)
		tbxPost(func() { javaFilesRun(config, name, plan, err) })
	}()
	return true
}

// javaInspecting is true while javaFilesOpen reads a file. Main thread.
var javaInspecting bool

// javaFilesRun acts on what the file turned out to be. Main thread.
func javaFilesRun(config *Config, name string, plan javaFilePlan, err error) {
	javaInspecting = false
	if err != nil {
		PlayErrorSound()
		showToast("Cannot run "+name+": "+err.Error(), ToastError())
		return
	}
	switch plan.kind {
	case javaFileMIDlet:
		if opener := javaMIDletOpener; opener != nil && opener(config, plan.path) {
			return
		}
		showToast(name+" is a phone game (J2ME) — put it in Roms/J2ME and play it from Games", ToastInfo())
		return
	case javaFileLibrary:
		showToast(name+" is a Java library, not a program — its manifest names no Main-Class to start", ToastInfo())
		return
	}
	needJDK := plan.kind == javaFileSource
	minMajor := plan.minMajor
	why := "Java programs need Java to run"
	if needJDK {
		why = "A .java file runs from its source with the JDK"
		if minMajor < 11 {
			minMajor = 11
		}
	}
	javaEnsure(config, why, minMajor, needJDK, func(rt javaRuntime) { javaLaunchPlan(config, rt, plan) })
}

// javaPlanArgs is the java command line for plan, relative to plan.dir.
func javaPlanArgs(plan javaFilePlan) []string {
	switch plan.kind {
	case javaFileApp:
		return []string{"-jar", filepath.Base(plan.path)}
	case javaFileClass:
		return []string{"-cp", ".", plan.main}
	case javaFileSource:
		// A file called "-x.java" is a path, not an option.
		name := filepath.Base(plan.path)
		if strings.HasPrefix(name, "-") {
			name = "." + string(filepath.Separator) + name
		}
		return []string{name}
	}
	return nil
}

// javaLaunchPlan starts a Java file with rt: a windowed program on its own on
// a desktop, anything else in the Terminal. Main thread.
func javaLaunchPlan(config *Config, rt javaRuntime, plan javaFilePlan) {
	args := javaPlanArgs(plan)
	if len(args) == 0 {
		return
	}
	name := filepath.Base(plan.path)
	if plan.windowed {
		if javaDesktopAvailable() {
			javaStartDetached(rt, plan.dir, args, name, nil)
			return
		}
		if !javaArmed(&javaHeadlessArm, plan.path) {
			PlayNavSound()
			showToast(name+" opens windows, and this handheld has no desktop for them. "+
				hintSentence("Press %s again to run it in the Terminal without a window", "A", "Run"), ToastWarn())
			return
		}
	}
	termOpenAndRun(config, func() {
		if term.job != nil {
			termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
			return
		}
		term.prevCwd, term.cwd = term.cwd, plan.dir
		line := "java " + javaQuoteArgs(args)
		termRecordHistory(line)
		term.histIdx = -1
		termFollow()
		termAppend(termPrompt+line, termKindEcho)
		termRunJavaArgv(rt, rt.Java, append(javaTermProps(), args...), line, plan.dir)
	})
}

// javaStartDetached starts a windowed program on the desktop, on its own:
// javaw on Windows (no console), java elsewhere, in its own process group.
// The process starts on a goroutine, which then reaps it; report (nil: a
// toast) hears on the main thread whether it started. Main thread.
func javaStartDetached(rt javaRuntime, dir string, args []string, name string, report func(err error)) {
	exe := rt.Java
	if rt.Javaw != "" {
		exe = rt.Javaw
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = javaEnvFor(rt, os.Environ())
	auditConfigureDetached(cmd)
	if report == nil {
		report = func(err error) {
			if err != nil {
				PlayErrorSound()
				showToast("Cannot start "+name+": "+filesFriendlyError(err), ToastError())
				return
			}
			PlayActivateSound()
			showToast("Started "+name+" — it opens in its own window", ToastSuccess())
		}
	}
	go func() {
		err := cmd.Start()
		tbxPost(func() { report(err) })
		if err != nil {
			logWarn("java", "starting %s with %s: %v", name, exe, err)
			return
		}
		logInfo("java", "started %s with %s", name, exe)
		_ = cmd.Wait()
	}()
}

// javaQuoteArgs joins args for the Terminal's echo and history, quoted the
// way the platform's shell reads them back.
func javaQuoteArgs(args []string) string {
	style := cmdQuoteSh
	if runtime.GOOS == "windows" {
		style = cmdQuoteCmd
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if q, ok := cmdDeckQuote(a, style); ok {
			out = append(out, q)
		} else {
			out = append(out, a)
		}
	}
	return strings.Join(out, " ")
}
