package main

// audit_interp_auto.go — Python and Perl for the Audit toolkit, fetched at a
// quiet moment instead of on the first install press.
//
// On the handheld the script tools (sqlmap, Nikto) run on Ubuntu's Python and
// Perl from Linux packages, and installing them is most of a first install's
// wait. Juka asked (2026-11-19) for them to arrive by themselves when the
// device is not doing anything. So the daily Emulator updates pass
// (emuautoupdate_registry.go) carries one more updater: when python3, perl or
// a Perl module the toolkit's script tools need is not installed, it installs
// them through Linux packages — never over a game or another install, on the
// charger or after the user stepped away, and on the first start after this
// update (the pass's on-start window: a key the pass has never checked is
// due). A failed install is not stamped, so a later pass tries again (an
// hour, then doubling, at most a day); a card with too little free space is
// skipped with a log line and looked at again the same way.
//
// It runs in Install automatically only, like the extra cores: Notify me
// announces new builds of what is installed, and a first install of
// something that is not is no such news (with no build to name, it would be
// announced at every pass). Off stops it with everything else.
//
// Like the extra cores it remembers what it has had (audit-interp.json beside
// the pass's record): every package it installed, or found installed, or the
// toolkit installed. Only a name never had before is installed by the pass,
// so Python or Perl the user removes stays removed (Install in the toolkit
// brings it back), while a module a recipe names later still arrives. A
// package left half-installed by a run that died is no removal and is
// installed again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// auditInterpAutoMinFree is the free space the card needs before the pass
// fetches them: the package index (when there is none yet), Python and Perl,
// with room to spare.
const auditInterpAutoMinFree = int64(400 << 20)

// auditInterpStateName is the record of the packages had, in the patch state
// folder.
const auditInterpStateName = "audit-interp.json"

// auditInterpFile is audit-interp.json.
type auditInterpFile struct {
	Schema int `json:"schema"`
	// Had are the packages the pass or the toolkit installed, or the pass
	// found installed.
	Had []string `json:"had,omitempty"`
}

// auditInterpState is the record and the session's bookkeeping.
var auditInterpState struct {
	mu     sync.Mutex
	loaded bool
	path   string
	had    map[string]bool
	// told: the packages left out after a removal were logged this session.
	told bool
}

// auditInterpStatePath is where the record lives; a variable so a test can
// use a temporary folder. "" keeps it in memory only.
var auditInterpStatePath = func() string {
	dir, err := PatchStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, auditInterpStateName)
}

// auditInterpLoadLocked reads the record the first time it is needed. A
// missing or unreadable file is an empty record: what is installed is then
// recorded again at the next check.
func auditInterpLoadLocked() {
	if auditInterpState.loaded {
		return
	}
	auditInterpState.loaded = true
	auditInterpState.path = auditInterpStatePath()
	auditInterpState.had = map[string]bool{}
	if auditInterpState.path == "" {
		return
	}
	data, err := os.ReadFile(auditInterpState.path)
	if err != nil || len(data) > 1<<20 {
		return
	}
	var f auditInterpFile
	if json.Unmarshal(data, &f) != nil || f.Schema != 1 {
		return
	}
	for _, n := range f.Had {
		auditInterpState.had[n] = true
	}
}

// auditInterpAutoRemember records names as had, and saves the record when one
// is new. Callers have installed them, or seen them installed.
func auditInterpAutoRemember(names []string) {
	if len(names) == 0 {
		return
	}
	auditInterpState.mu.Lock()
	defer auditInterpState.mu.Unlock()
	auditInterpLoadLocked()
	added := false
	for _, n := range names {
		if n != "" && !auditInterpState.had[n] {
			auditInterpState.had[n] = true
			added = true
		}
	}
	if !added || auditInterpState.path == "" {
		return
	}
	f := auditInterpFile{Schema: 1}
	for n := range auditInterpState.had {
		f.Had = append(f.Had, n)
	}
	slices.Sort(f.Had)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(auditInterpState.path, append(data, '\n'), 0o644); err != nil {
		logWarn("audit", "could not save %s: %v", auditInterpStateName, err)
	}
}

// auditInterpAutoDue is what the pass installs now: of the packages it keeps,
// each one never had that is not installed, and each one left half-installed.
// The installed ones are recorded as had. It reads status.json and walks PATH:
// the pass's goroutine only.
func auditInterpAutoDue() []string {
	wanted := auditInterpAutoPackages()
	if len(wanted) == 0 {
		return nil
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return nil
	}
	st := linuxPkgLoadStatus(lay)
	var have []string
	for _, n := range wanted {
		if in, ok := st.Packages[n]; ok && in.State == "installed" {
			have = append(have, n)
		}
	}
	auditInterpAutoRemember(have)
	auditInterpState.mu.Lock()
	defer auditInterpState.mu.Unlock()
	auditInterpLoadLocked()
	var due, removed []string
	for _, n := range wanted {
		in, ok := st.Packages[n]
		switch {
		case ok && in.State == "installed":
		case ok, !auditInterpState.had[n]:
			due = append(due, n)
		default:
			removed = append(removed, n)
		}
	}
	if len(removed) > 0 && !auditInterpState.told {
		auditInterpState.told = true
		logInfo("audit", "%s removed since; the quiet-moment pass leaves them out (Install in the Audit toolkit brings them back)", strings.Join(removed, ", "))
	}
	return due
}

// errAuditInterpLowSpace is the check's answer on a nearly full card: a check
// that failed, so the pass logs it and looks again later.
var errAuditInterpLowSpace = errors.New("not enough free space on the card")

// auditInterpAutoSupported reports whether the pass keeps them: the handheld
// with Linux packages, in Install automatically.
func auditInterpAutoSupported() bool {
	return auditCurrentPlatform() == auditPlatformHandheld && linuxPkgSupported() &&
		emuCoreUpdateModeNow() == coreUpdateInstall
}

// auditInterpAutoPackages are what the pass keeps installed: each interpreter
// the toolkit runs from Linux packages (none where the firmware has its own),
// with every module the catalog's handheld script recipes name. It walks PATH:
// the pass's goroutine only.
func auditInterpAutoPackages() []string {
	var out []string
	add := func(names ...string) {
		for _, n := range names {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	for _, needs := range []string{"python", "perl"} {
		if !auditUsesLinuxPkgInterpreter(needs) {
			continue
		}
		add(auditInterpreterPackages(needs)...)
		for _, t := range secAuditCatalog() {
			if r := t.Recipes[auditPlatformHandheld]; r.Kind == auditKindScript && r.Needs == needs {
				add(r.Modules...)
			}
		}
	}
	return out
}

// auditInterpAutoCheck is the updater's Check, without the network: is any of
// them due (never had and missing, or half-installed), and is there room.
func auditInterpAutoCheck(ctx context.Context, _ *http.Client) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	missing := auditInterpAutoDue()
	if len(missing) == 0 {
		return false, "", nil
	}
	lay, err := linuxPkgDirs()
	if err != nil {
		return false, "", err
	}
	// <data>/linux may not exist before the first package; its parent does.
	if free, err := filesFreeSpace(filepath.Dir(lay.base)); err == nil && free < auditInterpAutoMinFree {
		logInfo("audit", "Python and Perl wait for space: %s missing, %s free on the card, %s needed",
			strings.Join(missing, ", "), formatSize(free), formatSize(auditInterpAutoMinFree))
		return false, "", fmt.Errorf("%w (%s free, %s needed)", errAuditInterpLowSpace, formatSize(free), formatSize(auditInterpAutoMinFree))
	}
	return true, "", nil
}

// auditInterpAutoInstall is the updater's Install: Linux packages installs the
// due ones on the pass's card, and they are recorded as had.
func auditInterpAutoInstall(ctx context.Context, _ *http.Client, progress func(line string)) error {
	missing := auditInterpAutoDue()
	if len(missing) == 0 {
		return errEmuUpdaterCurrent
	}
	if linuxPkgBusyLabel() != "" {
		// A press is changing Linux packages right now: nothing failed, and
		// the next pass looks again.
		return errRelPackBusy
	}
	o := &linuxPkgIO{}
	if card := emuRuntimeCardFrom(ctx); card != nil {
		o = linuxPkgBarIO(card.gen)
	}
	bar := o.out
	o.out = func(s string) {
		if bar != nil {
			bar(s)
		}
		if progress == nil {
			return
		}
		// "Get:3 perl 5.38.2-3.2build2 [231 kB]" → "Downloading perl".
		f := strings.Fields(s)
		switch {
		case len(f) < 2:
		case strings.HasPrefix(f[0], "Get:") && f[1] == linuxPkgHost:
			progress("Downloading the package index")
		case strings.HasPrefix(f[0], "Get:") && f[0] != "Get:":
			progress("Downloading " + f[1])
		case f[0] == "Unpacking":
			progress("Unpacking " + f[1])
		}
	}
	logInfo("audit", "installing %s for the Audit toolkit at a quiet moment", strings.Join(missing, ", "))
	if err := linuxPkgEnsure(ctx, missing, false, o); err != nil {
		logWarn("audit", "Python and Perl install failed: %v", err)
		return err
	}
	auditInterpAutoRemember(missing)
	linuxPkgSummaryForget()
	secAuditForget()
	return nil
}

func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "auditinterp",
		Title:     "Python and Perl",
		Supported: auditInterpAutoSupported,
		Installed: func() bool { return true },
		InUse:     func() bool { return linuxPkgBusyLabel() != "" },
		Check:     auditInterpAutoCheck,
		Install:   auditInterpAutoInstall,
	})
}
