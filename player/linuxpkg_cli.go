package main

// linuxpkg_cli.go — the apt commands of Linux packages, shared by the three
// places they run from: the Terminal's `apt` built-in (a Terminal job with
// streamed lines that X / Ctrl+C stops), `JukaHub apt …` before SDL starts (an
// SSH shell, through <data>/linux/bin/apt), and the Linux packages page and
// the Audit toolkit (linuxPkgEnsure, linuxPkgRemoveNames).
//
//	apt update | upgrade | install <pkg…> | remove <pkg…> | autoremove |
//	    search <words> | show <pkg> | list --installed | list --upgradable | clean
//
// Like apt, a change that brings in packages that were not named, or any
// upgrade, is described first and needs a yes: typed at the prompt over SSH,
// or `-y` in the Terminal, which has no input for a running command (the
// command with -y is typed into its prompt, ready for Start).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// linuxPkgTermTimeout bounds an apt run in the Terminal: a first Python
// install over a slow Wi-Fi takes minutes, not the five a shell command gets.
const linuxPkgTermTimeout = 2 * time.Hour

// linuxPkgIO is where a command reports.
type linuxPkgIO struct {
	out func(string)
	// progress is the byte progress of the current downloads (may be nil).
	progress func(done, total int64)
	// confirm asks the question and reads the answer; nil where nothing can
	// be asked (the Terminal), and then the change needs -y.
	confirm func(question string) bool
	// retype offers the -y command line for the user to run (the Terminal
	// types it into its prompt).
	retype func(cmd string)
}

func (o *linuxPkgIO) line(s string) {
	if o != nil && o.out != nil {
		o.out(s)
	}
}

// linuxPkgUnsupportedText is what apt says off the handheld.
func linuxPkgUnsupportedText() string {
	return "apt here is the TrimUI handheld's package manager (Ubuntu's arm64 programs on its SD card). This system is not one: use its own package manager."
}

func linuxPkgUsage(o *linuxPkgIO) {
	for _, l := range []string{
		"Linux packages: Ubuntu 24.04 arm64 programs for this handheld, checked against Ubuntu's signing key.",
		"  apt update                 download the package index",
		"  apt upgrade                install newer versions of what is installed",
		"  apt install <pkg…>         install packages and what they need (-y: no question)",
		"  apt remove <pkg…>          remove packages (and what needs them)",
		"  apt autoremove             remove dependencies nothing needs any more",
		"  apt search <words>         find packages by name or description",
		"  apt show <pkg>             details of one package",
		"  apt list --installed       what is installed (--upgradable: what has updates)",
		"  apt clean                  delete downloaded package files",
		"Programs run from the card with Ubuntu's own C library; no services, no maintainer scripts.",
	} {
		o.line(l)
	}
}

// linuxPkgRun runs one apt command (args after "apt").
func linuxPkgRun(ctx context.Context, args []string, o *linuxPkgIO) error {
	yes := false
	var flags, words []string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes" || a == "--assume-yes" || a == "-qy" || a == "-yq":
			yes = true
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			words = append(words, a)
		}
	}
	if len(words) == 0 {
		linuxPkgUsage(o)
		return nil
	}
	cmd, names := strings.ToLower(words[0]), words[1:]
	if !linuxPkgSupported() {
		return errLinuxPkgUnsupported
	}
	switch cmd {
	case "help":
		linuxPkgUsage(o)
		return nil
	case "update":
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
		if n := linuxPkgLoadMeta(lay).Upgradable; n > 0 {
			o.line(fmt.Sprintf("%s can be upgraded. Run 'apt list --upgradable' to see them, 'apt upgrade' to install them.", pluralizeCount(n, "package", "packages")))
		} else {
			o.line("All installed packages are up to date.")
		}
		return nil
	case "upgrade", "full-upgrade", "dist-upgrade":
		return linuxPkgCmdChange(ctx, o, yes, "upgrade", nil)
	case "install", "reinstall":
		if len(names) == 0 {
			return errors.New("name the packages to install, e.g. apt install python3")
		}
		return linuxPkgCmdChange(ctx, o, yes, "install", names)
	case "remove", "purge":
		if len(names) == 0 {
			return errors.New("name the packages to remove")
		}
		return linuxPkgCmdRemove(ctx, o, yes, names, false)
	case "autoremove":
		return linuxPkgCmdRemove(ctx, o, yes, nil, true)
	case "search":
		return linuxPkgCmdSearch(ctx, o, names)
	case "show":
		return linuxPkgCmdShow(o, names)
	case "list":
		return linuxPkgCmdList(o, flags, names)
	case "clean", "autoclean":
		return linuxPkgCmdClean(o)
	}
	return fmt.Errorf("unknown command %q (apt help lists them)", cmd)
}

// linuxPkgWrapNames lays names out in indented lines.
func linuxPkgWrapNames(o *linuxPkgIO, names []string) {
	line := " "
	for _, n := range names {
		if len(line)+len(n)+1 > 72 && line != " " {
			o.line(line)
			line = " "
		}
		line += " " + n
	}
	if line != " " {
		o.line(line)
	}
}

// linuxPkgDescribe prints a plan the way apt does and reports whether apt
// would ask before going ahead.
func linuxPkgDescribe(o *linuxPkgIO, plan *linuxPkgPlan, asked []string) bool {
	var fresh, upgraded []string
	for _, e := range plan.install {
		if _, ok := plan.upgrades[e.Name]; ok {
			upgraded = append(upgraded, e.Name)
		} else {
			fresh = append(fresh, e.Name)
		}
	}
	if len(fresh) > 0 {
		o.line("The following NEW packages will be installed:")
		linuxPkgWrapNames(o, fresh)
	}
	if len(upgraded) > 0 {
		o.line("The following packages will be upgraded:")
		linuxPkgWrapNames(o, upgraded)
	}
	if len(plan.remove) > 0 {
		o.line("The following packages will be REMOVED:")
		linuxPkgWrapNames(o, plan.remove)
	}
	o.line(fmt.Sprintf("%d upgraded, %d newly installed, %d to remove.", len(upgraded), len(fresh), len(plan.remove)))
	if plan.download > 0 {
		o.line("Need to get " + formatSize(plan.download) + " of archives.")
	}
	switch {
	case plan.addBytes > 0:
		o.line("After this operation, " + formatSize(plan.addBytes) + " of additional disk space will be used.")
	case plan.addBytes < 0:
		o.line("After this operation, " + formatSize(-plan.addBytes) + " of disk space will be freed.")
	}
	named := map[string]bool{}
	for _, n := range asked {
		named[strings.ToLower(n)] = true
	}
	for _, n := range fresh {
		if !named[n] {
			return true
		}
	}
	for _, n := range plan.remove {
		if !named[n] {
			return true
		}
	}
	if len(upgraded) > 0 && len(asked) == 0 {
		return true
	}
	return plan.download > 50<<20
}

// linuxPkgAgree is apt's "Do you want to continue?".
func linuxPkgAgree(o *linuxPkgIO, yes bool, cmdLine string) bool {
	if yes {
		return true
	}
	if o.confirm != nil {
		if o.confirm("Do you want to continue? [Y/n] ") {
			return true
		}
		o.line("Abort.")
		return false
	}
	o.line("Nothing was changed. To go ahead, run:")
	o.line("  " + cmdLine)
	if o.retype != nil {
		o.retype(cmdLine)
	}
	return false
}

// linuxPkgCmdChange is install and upgrade.
func linuxPkgCmdChange(ctx context.Context, o *linuxPkgIO, yes bool, verb string, names []string) error {
	release, err := linuxPkgBegin(verb + " " + strings.Join(names, " "))
	if err != nil {
		return err
	}
	defer release()
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	o.line("Reading package lists…")
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return err
	}
	st := linuxPkgLoadStatus(lay)
	var plan *linuxPkgPlan
	var notes []string
	if verb == "upgrade" {
		plan, notes, err = linuxPkgPlanUpgrade(x, st)
	} else {
		plan, notes, err = linuxPkgPlanInstall(x, st, names)
	}
	x.Close()
	if err != nil {
		return err
	}
	for _, n := range notes {
		o.line(n)
	}
	for _, a := range plan.already {
		o.line(a)
	}
	linuxPkgMarkManual(lay, plan, o)
	if len(plan.install) == 0 {
		o.line("0 upgraded, 0 newly installed, 0 to remove.")
		return nil
	}
	ask := linuxPkgDescribe(o, plan, names)
	cmdLine := "apt " + verb + " -y"
	if len(names) > 0 {
		cmdLine += " " + strings.Join(names, " ")
	}
	if ask && !linuxPkgAgree(o, yes, cmdLine) {
		return nil
	}
	err = linuxPkgApply(ctx, lay, plan, o)
	var he *linuxPkgHTTPError
	if errors.As(err, &he) && he.code == 404 {
		return fmt.Errorf("%w — the package index is older than the archive; run apt update and try again", err)
	}
	if err == nil {
		o.line("Done.")
		logInfo("linuxpkg", "%s: %d packages, %s downloaded", verb, len(plan.install), formatSize(plan.download))
	}
	return err
}

// linuxPkgMarkManual records that a package asked for by name stays when
// what pulled it in goes (apt's "set to manually installed").
func linuxPkgMarkManual(lay linuxPkgLayout, plan *linuxPkgPlan, o *linuxPkgIO) {
	st := linuxPkgLoadStatus(lay)
	changed := false
	for n := range plan.manual {
		if in, ok := st.Packages[n]; ok && in.Auto {
			in.Auto = false
			st.Packages[n] = in
			changed = true
			o.line(n + " set to manually installed.")
		}
	}
	if changed {
		_ = linuxPkgSaveStatus(lay, st)
	}
}

// linuxPkgCmdRemove is remove and autoremove.
func linuxPkgCmdRemove(ctx context.Context, o *linuxPkgIO, yes bool, names []string, auto bool) error {
	release, err := linuxPkgBegin("removing packages")
	if err != nil {
		return err
	}
	defer release()
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	st := linuxPkgLoadStatus(lay)
	plan := newLinuxPkgPlan()
	cmdLine := "apt autoremove -y"
	if auto {
		plan.remove = linuxPkgAutoremovable(st)
	} else {
		if plan.remove, err = linuxPkgPlanRemove(st, names); err != nil {
			return err
		}
		cmdLine = "apt remove -y " + strings.Join(names, " ")
	}
	if len(plan.remove) == 0 {
		o.line("0 upgraded, 0 newly installed, 0 to remove.")
		return nil
	}
	for _, n := range plan.remove {
		plan.addBytes -= st.Packages[n].Bytes
	}
	ask := linuxPkgDescribe(o, plan, names) || auto
	if ask && !linuxPkgAgree(o, yes, cmdLine) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := linuxPkgRemove(lay, plan.remove, o); err != nil {
		return err
	}
	if left := linuxPkgAutoremovable(linuxPkgLoadStatus(lay)); len(left) > 0 && !auto {
		o.line(fmt.Sprintf("%s no longer needed: apt autoremove removes them.", pluralizeCount(len(left), "package is", "packages are")))
	}
	o.line("Done.")
	return nil
}

func linuxPkgCmdSearch(ctx context.Context, o *linuxPkgIO, words []string) error {
	if len(words) == 0 {
		return errors.New("give words to search for, e.g. apt search sqlite")
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	hits, total, err := linuxPkgSearch(ctx, lay, strings.Join(words, " "), 200)
	if err != nil {
		return err
	}
	st := linuxPkgLoadStatus(lay)
	for _, e := range hits {
		mark := ""
		if in, ok := st.Packages[e.Name]; ok {
			mark = " [installed]"
			if linuxPkgVersionCompare(e.Version, in.Version) > 0 {
				mark = " [installed, upgradable from " + in.Version + "]"
			}
		}
		o.line(e.Name + "/" + e.Version + mark)
		o.line("  " + e.Desc)
	}
	switch {
	case total == 0:
		o.line("No packages match.")
	case total > len(hits):
		o.line(fmt.Sprintf("… and %d more; add words to narrow it down.", total-len(hits)))
	}
	return nil
}

// linuxPkgSearch finds packages whose name or description has every word;
// names that start with the first word come first.
func linuxPkgSearch(ctx context.Context, lay linuxPkgLayout, query string, max int) ([]linuxPkgEntry, int, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, 0, nil
	}
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return nil, 0, err
	}
	defer x.Close()
	var hits []linuxPkgEntry
	total := 0
	err = x.Scan(ctx, func(e linuxPkgEntry) bool {
		hay := e.Name + " " + strings.ToLower(e.Desc)
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return true
			}
		}
		total++
		if len(hits) < 4*max {
			hits = append(hits, e)
		}
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	first := words[0]
	rank := func(e linuxPkgEntry) int {
		switch {
		case e.Name == first:
			return 0
		case strings.HasPrefix(e.Name, first):
			return 1
		case strings.Contains(e.Name, first):
			return 2
		}
		return 3
	}
	sort.SliceStable(hits, func(i, j int) bool {
		ri, rj := rank(hits[i]), rank(hits[j])
		if ri != rj {
			return ri < rj
		}
		return hits[i].Name < hits[j].Name
	})
	if len(hits) > max {
		hits = hits[:max]
	}
	return hits, total, nil
}

func linuxPkgCmdShow(o *linuxPkgIO, names []string) error {
	if len(names) == 0 {
		return errors.New("name a package, e.g. apt show nmap")
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return err
	}
	defer x.Close()
	st := linuxPkgLoadStatus(lay)
	for i, n := range names {
		n = strings.ToLower(n)
		if i > 0 {
			o.line("")
		}
		e, ok := x.Get(n)
		if !ok {
			if p := x.Providers(n); len(p) > 0 {
				o.line(n + " is a virtual package provided by: " + strings.Join(p, ", "))
				continue
			}
			return fmt.Errorf("no package named %s", n)
		}
		o.line("Package: " + e.Name)
		o.line("Version: " + e.Version)
		o.line("Section: " + e.Section)
		o.line("Download-Size: " + formatSize(e.Size))
		o.line("Installed-Size: " + formatSize(e.InstalledKB*1024))
		if e.PreDepends != "" {
			o.line("Pre-Depends: " + e.PreDepends)
		}
		if e.Depends != "" {
			o.line("Depends: " + e.Depends)
		}
		if e.Provides != "" {
			o.line("Provides: " + e.Provides)
		}
		if in, ok := st.Packages[e.Name]; ok {
			how := "manually"
			if in.Auto {
				how = "as a dependency"
			}
			o.line("Installed: " + in.Version + " (" + how + ", " + formatSize(in.Bytes) + " on the card)")
			if len(in.SkipDeps) > 0 {
				o.line("Installed without: " + strings.Join(in.SkipDeps, ", ") + " (not needed here)")
			}
		}
		o.line("Description: " + e.Desc)
	}
	return nil
}

func linuxPkgCmdList(o *linuxPkgIO, flags, patterns []string) error {
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	st := linuxPkgLoadStatus(lay)
	installed, upgradable := false, false
	for _, f := range flags {
		switch f {
		case "--installed", "-i":
			installed = true
		case "--upgradable", "--upgradeable", "-u":
			upgradable = true
		}
	}
	match := func(name string) bool {
		if len(patterns) == 0 {
			return true
		}
		for _, p := range patterns {
			if strings.Contains(name, strings.Trim(strings.ToLower(p), "*")) {
				return true
			}
		}
		return false
	}
	switch {
	case upgradable:
		x, err := linuxPkgOpenIndex(lay)
		if err != nil {
			return err
		}
		defer x.Close()
		names := linuxPkgUpgradable(x, st)
		for _, n := range names {
			if e, ok := x.Get(n); ok && match(n) {
				o.line(n + "/" + e.Version + " [upgradable from: " + st.Packages[n].Version + "]")
			}
		}
		if len(names) == 0 {
			o.line("All installed packages are up to date.")
		}
	case installed || len(patterns) == 0:
		names := make([]string, 0, len(st.Packages))
		for n := range st.Packages {
			if match(n) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		var total int64
		for _, n := range names {
			in := st.Packages[n]
			tag := "[installed]"
			if in.Auto {
				tag = "[installed,automatic]"
			}
			if in.State != "installed" {
				tag = "[half-installed: the next install or upgrade repairs it]"
			}
			o.line(n + "/" + in.Version + " " + tag)
			total += in.Bytes
		}
		o.line(fmt.Sprintf("%s, %s on the card.", pluralizeCount(len(names), "package", "packages"), formatSize(total)))
	default:
		return linuxPkgCmdSearch(context.Background(), o, patterns)
	}
	return nil
}

func linuxPkgCmdClean(o *linuxPkgIO) error {
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	entries, _ := os.ReadDir(lay.cache)
	var freed int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			if os.Remove(filepath.Join(lay.cache, e.Name())) == nil {
				freed += info.Size()
			}
		}
	}
	o.line("Deleted " + formatSize(freed) + " of downloaded package files.")
	return nil
}

// ── For the page and the Audit toolkit ───────────────────────────────────

// linuxPkgEnsure installs names and what they need, without a question (the
// press that asked for it is the consent); an installed one with a newer
// version is upgraded. It updates the index first when there is none or when
// refresh asks for it (an Update button), and once more when the archive no
// longer has a file the index names.
func linuxPkgEnsure(ctx context.Context, names []string, refresh bool, o *linuxPkgIO) error {
	return linuxPkgEnsureSkipping(ctx, names, nil, refresh, o)
}

// linuxPkgEnsureSkipping is linuxPkgEnsure for packages that go without some
// of their dependencies here (an Audit toolkit recipe that needs a program's
// own binaries, not the helper programs its package brings for other uses):
// skip is left out for names, and recorded so upgrade, repair, remove and
// autoremove leave it out too (linuxPkgPlanInstallSkipping).
func linuxPkgEnsureSkipping(ctx context.Context, names, skip []string, refresh bool, o *linuxPkgIO) error {
	release, err := linuxPkgBegin("installing " + strings.Join(names, ", "))
	if err != nil {
		return err
	}
	defer release()
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 || refresh || !regularFile(filepath.Join(lay.index, "packages.tsv")) {
			if err := linuxPkgUpdate(ctx, o); err != nil {
				return err
			}
		}
		x, err := linuxPkgOpenIndex(lay)
		if err != nil {
			return err
		}
		plan, notes, err := linuxPkgPlanInstallSkipping(x, linuxPkgLoadStatus(lay), names, skip)
		x.Close()
		if err != nil {
			return err
		}
		for _, n := range notes {
			o.line(n)
		}
		linuxPkgMarkManual(lay, plan, o)
		err = linuxPkgApply(ctx, lay, plan, o)
		var he *linuxPkgHTTPError
		if attempt == 0 && errors.As(err, &he) && he.code == 404 {
			o.line("The package index is out of date; updating it and trying again…")
			continue
		}
		if err == nil && len(plan.install) > 0 {
			logInfo("linuxpkg", "installed %s: %d packages, %s", strings.Join(names, " "), len(plan.install), formatSize(plan.download))
		}
		return err
	}
	return nil
}

// linuxPkgUpgradeAll is the page's "Update all": update, then upgrade.
func linuxPkgUpgradeAll(ctx context.Context, o *linuxPkgIO) error {
	release, err := linuxPkgBegin("updating everything")
	if err != nil {
		return err
	}
	defer release()
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	if err := linuxPkgUpdate(ctx, o); err != nil {
		return err
	}
	x, err := linuxPkgOpenIndex(lay)
	if err != nil {
		return err
	}
	plan, _, err := linuxPkgPlanUpgrade(x, linuxPkgLoadStatus(lay))
	x.Close()
	if err != nil {
		return err
	}
	if len(plan.install) == 0 {
		linuxPkgRefreshCounts(lay, linuxPkgLoadStatus(lay))
		o.line("Everything is up to date.")
		return nil
	}
	return linuxPkgApply(ctx, lay, plan, o)
}

// linuxPkgRemoveNames removes names, what needs them, and the dependencies
// nothing needs afterwards (the page's and the Audit toolkit's Remove).
func linuxPkgRemoveNames(names []string, o *linuxPkgIO) error {
	release, err := linuxPkgBegin("removing " + strings.Join(names, ", "))
	if err != nil {
		return err
	}
	defer release()
	lay, err := linuxPkgDirs()
	if err != nil {
		return err
	}
	st := linuxPkgLoadStatus(lay)
	var present []string
	for _, n := range names {
		if _, ok := st.Packages[n]; ok {
			present = append(present, n)
		}
	}
	if len(present) == 0 {
		return nil
	}
	gone, err := linuxPkgPlanRemove(st, present)
	if err != nil {
		return err
	}
	if err := linuxPkgRemove(lay, gone, o); err != nil {
		return err
	}
	if left := linuxPkgAutoremovable(linuxPkgLoadStatus(lay)); len(left) > 0 {
		return linuxPkgRemove(lay, left, o)
	}
	return nil
}

// ── Entry points ─────────────────────────────────────────────────────────

// handleLinuxPkgCommand runs `JukaHub apt …` (or jpkg) before SDL starts, so an
// SSH shell on the handheld has the package manager.
func handleLinuxPkgCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || (args[0] != "apt" && args[0] != "apt-get" && args[0] != "jpkg") {
		return false, 0
	}
	// The logger before device detection: detection logs from inside its
	// sync.Once, and a first log line with no logger would re-enter it.
	InitLogging()
	if !linuxPkgSupported() {
		fmt.Fprintln(os.Stderr, linuxPkgUnsupportedText())
		return true, 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdin := bufio.NewReader(os.Stdin)
	last := time.Time{}
	o := &linuxPkgIO{
		out: func(s string) { fmt.Println(s) },
		progress: func(done, total int64) {
			if time.Since(last) < 2*time.Second || total <= 0 {
				return
			}
			last = time.Now()
			fmt.Printf("  %s of %s\n", formatSize(done), formatSize(total))
		},
		confirm: func(q string) bool {
			fmt.Print(q)
			answer, err := stdin.ReadString('\n')
			if err != nil && answer == "" {
				fmt.Println()
				return false
			}
			a := strings.ToLower(strings.TrimSpace(answer))
			return a == "" || a == "y" || a == "yes"
		},
	}
	if err := linuxPkgRun(ctx, args[1:], o); err != nil {
		fmt.Fprintln(os.Stderr, "E: "+err.Error())
		return true, 1
	}
	return true, 0
}

// termLinuxPkg is the Terminal's apt built-in: a Terminal job whose lines
// stream into the console and that X / Ctrl+C stops. A line with shell syntax
// (apt search x | grep y) goes to the shell, where bin/apt runs `JukaHub apt`.
func termLinuxPkg(command, cmdline string, args []string) {
	if !linuxPkgSupported() {
		if command != "jpkg" && termHasProgram(command) {
			termRunExternal(cmdline)
			return
		}
		termAppend(linuxPkgUnsupportedText(), termKindSystem)
		return
	}
	if strings.ContainsAny(cmdline, termShellMetaChars()) {
		termRunExternal(cmdline)
		return
	}
	if term.job != nil {
		termAppend("A command is still running — press X (or Ctrl+C) to stop it first.", termKindError)
		return
	}
	ctx, cancel := context.WithTimeout(termJobParent(), linuxPkgTermTimeout)
	termLaunchFunc(ctx, cancel, cmdline, linuxPkgTermTimeout, func(ctx context.Context, w io.Writer) error {
		o := &linuxPkgIO{
			out: func(s string) { _, _ = io.WriteString(w, s+"\n") },
			retype: func(cmd string) {
				tbxPost(func() {
					if tbxCurrentScene(appConfig) == "Terminal" && strings.TrimSpace(term.input) == "" {
						termInsertText(cmd)
					}
				})
			},
		}
		err := linuxPkgRun(ctx, args, o)
		linuxPkgSummaryForget()
		return err
	})
}
