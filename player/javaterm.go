package main

// javaterm.go — Java in the Terminal.
//
// java, javac, jshell, jar and javaw are the Terminal's own commands. A line
// without shell syntax runs the Java JukaHub uses directly, with its standard
// input open: what the user types while the program runs reaches it
// (terminal.go, termLaunchInput), so a program that asks for a name gets one.
// A line with pipes, redirects or variables goes to the shell as before, with
// that Java first on its PATH and JAVA_HOME set (javaShellEnv); the shell's
// programs get no typed input. java runs get UTF-8 output (the Terminal reads
// UTF-8; Windows would otherwise use its code page) and, where there is no
// desktop (the handheld), java.awt.headless, so a program that only touches
// AWT does not die looking for a display.
//
// `java install [8|11|17|21|25] [jre|jdk]`, `java update`, `java remove` and
// `java status` manage JukaHub's own Java as a Terminal job: the install
// prints its steps and progress, and leaving the Terminal stops it, like any
// command there. Tools → Java (javaui.go) does the same in the background.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// javaTermTimeout bounds one Java program in the Terminal. A program there
// often waits for input or keeps running (a server, a game), and leaving the
// Terminal stops it anyway, so this only catches one nobody watches.
const javaTermTimeout = 6 * time.Hour

// javaRemoveTimeout bounds `java remove` (deleting a JDK from a slow card).
const javaRemoveTimeout = 10 * time.Minute

// javaTermTool reports whether command is one of the Terminal's Java tools.
func javaTermTool(command string) bool {
	switch command {
	case "java", "javac", "jshell", "jar", "javaw":
		return true
	}
	return false
}

// javaTermProps are the options every java run from the Terminal or Files
// gets: UTF-8 output (stdout.encoding is Java 19's name, sun.* the older
// one) and no windows where there is no desktop.
func javaTermProps() []string {
	props := []string{"-Dstdout.encoding=UTF-8", "-Dstderr.encoding=UTF-8", "-Dsun.stdout.encoding=UTF-8", "-Dsun.stderr.encoding=UTF-8"}
	if !javaDesktopAvailable() {
		props = append(props, "-Djava.awt.headless=true")
	}
	return props
}

// termJavaCommand is the Terminal's java, javac, jshell, jar and javaw.
func termJavaCommand(command, cmdline string, args []string) {
	if command == "java" && len(args) > 0 {
		switch args[0] {
		case "install", "update", "remove", "status":
			termJavaManage(args[0], args[1:], cmdline)
			return
		}
	}
	if term.job != nil {
		termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
		return
	}
	if !javaScanFresh() {
		termAppend("Looking for Java…", termKindSystem)
	}
	javaWhenScanned(func() { termJavaRunLine(command, cmdline) })
}

// termJavaHere reports whether a result that waited for the Java scan still
// belongs on screen: the Terminal is up and idle.
func termJavaHere() bool {
	return tbxCurrentScene(appConfig) == "Terminal" && term.job == nil
}

// termJavaRunLine runs a Java tool once the scan has answered. Main thread.
func termJavaRunLine(tool, cmdline string) {
	if !termJavaHere() {
		return
	}
	rt, ok := javaFind()
	if !ok {
		termJavaMissing()
		return
	}
	// javac, jshell and jar come with a JDK: one the computer has is used
	// when JukaHub's own Java is a JRE.
	if tool == "javac" || tool == "jshell" || tool == "jar" {
		if rt.Javac == "" {
			if jdk, found := javaFindFor(0, true); found {
				rt = jdk
			}
		}
	}
	exe := javaToolPath(rt, tool)
	if exe == "" {
		major := javaDefaultMajor
		if rt.Managed && javaSupportedMajor(rt.Major) {
			major = rt.Major
		}
		termAppend(tool+" comes with the JDK, and this Java is a JRE. Type java install "+strconv.Itoa(major)+" jdk to get it ("+javaSizeText(javaKindJDK)+").", termKindError)
		return
	}
	argv, direct := termJavaSplit(cmdline)
	if !direct || len(argv) == 0 {
		termRunExternal(cmdline)
		return
	}
	args := argv[1:]
	if tool == "javaw" && rt.Javaw != "" {
		// javaw has no console: its program opens on the desktop.
		javaStartDetached(rt, term.cwd, args, "javaw", func(err error) {
			if tbxCurrentScene(appConfig) != "Terminal" {
				if err != nil {
					showToast("Cannot start javaw: "+filesFriendlyError(err), ToastError())
				}
				return
			}
			if err != nil {
				termAppend("Cannot start javaw: "+filesFriendlyError(err), termKindError)
				return
			}
			termAppend("Started with javaw — it has no console, so nothing it prints shows here.", termKindSystem)
		})
		return
	}
	if tool == "java" || tool == "javaw" {
		args = append(javaTermProps(), args...)
	}
	termRunJavaArgv(rt, exe, args, cmdline, term.cwd)
}

// termJavaSplit splits a typed Java line into its words without a shell, or
// reports false when it uses shell syntax (pipes, redirects, variables,
// wildcards). Quotes group words; on Windows a backslash is a path's.
func termJavaSplit(line string) ([]string, bool) {
	var argv []string
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(line, "|&<>^%") {
			return nil, false
		}
		argv = splitShellArgs(line)
	} else {
		if strings.ContainsAny(line, "|&;<>()$`*?[") {
			return nil, false
		}
		argv = splitToolArgs(line)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for i, a := range argv {
			if a == "~" {
				argv[i] = home
			} else if rest, ok := strings.CutPrefix(a, "~/"); ok {
				argv[i] = filepath.Join(home, rest)
			}
		}
	}
	return argv, true
}

// termRunJavaArgv runs one Java tool as the Terminal's job, with its input
// open and the Java's own folder first on PATH.
func termRunJavaArgv(rt javaRuntime, exe string, args []string, label, dir string) {
	ctx, cancel := context.WithTimeout(termJobParent(), javaTermTimeout)
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = javaEnvFor(rt, linuxPkgShellEnv(append(os.Environ(), "TERM=dumb")))
	// No console window flashes up over JukaHub on Windows.
	auditConfigureHidden(cmd)
	termLaunchInput(ctx, cancel, cmd, label, javaTermTimeout)
}

// termJavaMissing says how to get Java.
func termJavaMissing() {
	if busy := javaInstallBusy(); busy != "" {
		termAppend("Busy: "+busy+" — try again when it is done.", termKindSystem)
		return
	}
	if note := javaScanNote(); note != "" {
		termAppend("A Java was found, but it does not start here: "+note, termKindError)
	}
	v := strconv.Itoa(javaDefaultMajor)
	termAppend("Java is not installed. Type java install for Java "+v+" (the JRE, "+javaSizeText(javaKindJRE)+"),", termKindSystem)
	termAppend("or java install "+v+" jdk for javac, jshell and java Hello.java ("+javaSizeText(javaKindJDK)+"). Tools → Java does the same.", termKindSystem)
}

// termJavaManage is java install / update / remove / status.
func termJavaManage(sub string, args []string, cmdline string) {
	switch sub {
	case "status":
		if !javaScanFresh() {
			termAppend("Looking for Java…", termKindSystem)
		}
		javaWhenScanned(termJavaStatus)
	case "install":
		major, kind := javaDefaultMajor, javaKindJRE
		for _, a := range args {
			switch strings.ToLower(a) {
			case javaKindJRE:
				kind = javaKindJRE
			case javaKindJDK:
				kind = javaKindJDK
			default:
				n, err := strconv.Atoi(a)
				if err != nil || !javaSupportedMajor(n) {
					termAppend("Usage: java install [8|11|17|21|25] [jre|jdk] — e.g. java install 25 jdk", termKindError)
					return
				}
				major = n
			}
		}
		termJavaInstall(major, kind, cmdline, false)
	case "update", "remove":
		// Both ask which Java JukaHub has, which the scan knows: right after
		// JukaHub starts it may still be looking.
		if !javaScanFresh() {
			termAppend("Looking for Java…", termKindSystem)
		}
		javaWhenScanned(func() {
			if !termJavaHere() {
				return
			}
			if sub == "remove" {
				termJavaRemove(cmdline)
				return
			}
			rt, ok := javaFind()
			if !ok || !rt.Managed {
				termAppend("JukaHub has not installed a Java to update — java install adds one.", termKindSystem)
				return
			}
			kind := javaKindJRE
			if rt.Javac != "" {
				kind = javaKindJDK
			}
			termJavaInstall(rt.Major, kind, cmdline, true)
		})
	}
}

// termJavaStatus prints which Java is used and which others were found.
func termJavaStatus() {
	if tbxCurrentScene(appConfig) != "Terminal" {
		return
	}
	if busy := javaInstallBusy(); busy != "" {
		termAppend("Working now: "+busy, termKindSystem)
	}
	all := javaKnownRuntimes()
	if len(all) == 0 {
		termJavaMissing()
		return
	}
	termAppend("In use: "+javaDescribe(all[0]), termKindOutput)
	termAppend("  JAVA_HOME "+all[0].Home, termKindOutput)
	for _, rt := range all[1:] {
		termAppend("Also here: "+javaDescribe(rt)+" — "+rt.Home, termKindOutput)
	}
	if !all[0].Managed {
		termAppend("JukaHub has not installed a Java of its own; java install adds one, and it then comes first.", termKindSystem)
	}
}

// termJavaInstall installs (or, with onlyNewer, updates) JukaHub's Java as a
// Terminal job.
func termJavaInstall(major int, kind, cmdline string, onlyNewer bool) {
	if term.job != nil {
		termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
		return
	}
	if !IsOnline() {
		termAppend(offlinePlainNotice, termKindError)
		return
	}
	if !javaInstallBegin(major, kind) {
		termAppend("Busy: "+javaInstallBusy()+" — one at a time.", termKindError)
		return
	}
	termAppend("Leaving the Terminal stops this; Tools → Java installs in the background instead.", termKindSystem)
	ctx, cancel := context.WithTimeout(termJobParent(), javaInstallTimeout)
	termLaunchFunc(ctx, cancel, cmdline, javaInstallTimeout, func(ctx context.Context, w io.Writer) (err error) {
		var rt javaRuntime
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
			javaForget()
			javaFinishTermInstall(rt, err)
		}()
		say := func(s string) { _, _ = io.WriteString(w, s+"\n") }
		if onlyNewer {
			say("Checking for a newer build of Java " + strconv.Itoa(major) + " (" + javaKindLabel(kind) + ")…")
			asset, newer, cerr := javaCheckForUpdate(ctx)
			if cerr != nil {
				return errors.New(plainNetworkError(cerr))
			}
			if !newer {
				rt, _ = javaManagedRuntime()
				say("Java is up to date: " + asset.Name)
				return nil
			}
			say("A newer build is out: " + asset.Name)
		}
		rt, err = javaInstall(ctx, major, kind, javaInstallIO{say: say, progress: javaTermProgress(w)})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			return errors.New(plainNetworkError(err))
		}
		say("Type java -version to try it.")
		return nil
	})
}

// javaTermProgress prints a download's progress as one line that redraws
// itself (the Terminal keeps the last frame of a \r line).
func javaTermProgress(w io.Writer) func(done, total int64) {
	last := -1
	finished := false
	return func(done, total int64) {
		if finished {
			return
		}
		step := int(done >> 20)
		if total > 0 {
			step = int(done * 100 / total)
		}
		if step == last {
			return
		}
		last = step
		line := "\r  " + formatSize(done)
		if total > 0 {
			line = fmt.Sprintf("\r  %d%% · %s of %s", step, formatSize(done), formatSize(total))
			if done >= total {
				line += "\n"
				finished = true
			}
		}
		_, _ = io.WriteString(w, line)
	}
}

// termJavaRemove deletes JukaHub's Java as a Terminal job.
func termJavaRemove(cmdline string) {
	if term.job != nil {
		termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
		return
	}
	rt, ok := javaFind()
	if !ok || !rt.Managed {
		termAppend("JukaHub has not installed a Java. A Java the computer has is removed with its own uninstaller.", termKindSystem)
		return
	}
	if !javaInstallBegin(0, javaKindRemove) {
		termAppend(javaInstallBusy()+" — wait for it to finish.", termKindError)
		return
	}
	ctx, cancel := context.WithTimeout(termJobParent(), javaRemoveTimeout)
	termLaunchFunc(ctx, cancel, cmdline, javaRemoveTimeout, func(ctx context.Context, w io.Writer) error {
		defer func() {
			javaInstallEnd()
			tbxPost(javaScreenChanged)
		}()
		if err := javaRemove(); err != nil {
			return err
		}
		_, _ = io.WriteString(w, "JukaHub's Java is removed.\n")
		return nil
	})
}
