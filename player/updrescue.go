package main

// updrescue.go — an update that does not start is undone by the launcher.
//
// The boot guard (bootguard.go) rolls a new build back after three failed
// starts, but only once the Go process reaches main. The 2026-11-19 release
// deadlocked in package init, before main: no output, a black screen on every
// boot, and launch.sh waiting on it for ever until the build was replaced over
// SSH. A crash in package init has the same blind spot. So the first starts of
// an update are watched from outside the process, by launch.sh, which runs
// whatever the build does:
//
//   - Before the swap helper runs, the build being replaced writes the
//     probation plan, APP_DIR/.update-probation (updProbationNoteSwap): the
//     release it replaces and the one it installs, its journal, the size of
//     the new JukaHub, the folders the backups can be in, and one line per
//     replaced file. Plain lines, so busybox sh reads it without tools.
//   - While the plan exists, the new build writes its start check to
//     APP_DIR/.start-check (startcheck.go): "frame <pid>" at its first frame,
//     "ready <pid>" once its main loop has kept running 10 s past it and
//     input works, and then deletes the plan; "noinput <pid>" when a handheld
//     has had no pad and no input event 30 s after the first frame.
//   - launch.sh gives each start under the plan 120 s (45 s from the second
//     start of the same update) to say "ready". A start that does not — it
//     hangs, crashes or has no input — is stopped with SIGABRT, so the Go
//     runtime prints every goroutine's stack into errors.txt first; the plan's
//     backups are copied back over the installation and the restored build
//     starts at once, finding APP_DIR/.update-rescued. Once per plan: the plan
//     goes with the restore, and a restore that cannot be done in full is not
//     started at all.
//   - The restored build settles that as it starts (updRescueSettle): the
//     release that failed goes on the not-again list (uhistSkipped) until a
//     newer one exists, the update's half-done bookkeeping is undone (its
//     pending version record, its staged copy, the patch state's tags), the
//     boot guard takes the failed start as settled (bootGuardRescuedPID), and
//     the recovery card says what happened until it is acknowledged.
//
// Files the update added stay, as after a Version history rollback, except a
// release-info.json the update added: the restored build must not report the
// release it replaced. Until the new build proves itself, the backups are kept
// where the plan says even when no previous versions are kept
// (patch/probation/<journal>) or the history would prune them.
//
// Only for the handheld's launch.sh: nothing is written on Windows, and no
// plan is made unless the launcher exports JUKAHUB_UPDATE_PROBATION=1 and the
// release being installed writes the start check (its launch.sh has the
// updProbationToken): an older one installed from a file would never say
// "ready".

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// updProbationName is the plan, beside the executable as launch.sh's other
	// markers are.
	updProbationName = ".update-probation"
	// updProbationTriesName is launch.sh's count of the plan's starts.
	updProbationTriesName = ".update-probation-tries"
	// updStartCheckName is the new build's "<stage> <pid>" line.
	updStartCheckName = ".start-check"
	// updRescuedName is what launch.sh leaves after it undid an update.
	updRescuedName = ".update-rescued"
	// updProbationDir (in the Patch state folder) keeps a plan's backups that
	// neither the journal nor Version history keeps.
	updProbationDir = "probation"
	// updProbationEnv is exported by a launch.sh that watches the plan.
	updProbationEnv = "JUKAHUB_UPDATE_PROBATION"
	// updProbationToken is in the header of a launch.sh from a release whose
	// JukaHub writes the start check.
	updProbationToken = "jukahub-update-probation: 1"
	// updRescueFactName (in versions/) is the last update launch.sh undid.
	updRescueFactName = "rescued.json"
	// updPlanVersion is the plan's format; launch.sh ignores any other.
	updPlanVersion = "1"
	// updPlanMax and updMarkerMax bound what is read of the plan and of the
	// launcher's marker; updRescueLogMax what of errors.txt becomes a report.
	updPlanMax      = 1 << 20
	updMarkerMax    = 4 << 10
	updRescueLogMax = 256 << 10
	// updLogName is the log launch.sh appends JukaHub's output to.
	updLogName = "errors.txt"
)

// updPlanFile is one replaced file: Name its path in the installation (slash
// form), Stored the backup's file name, Size the backup's size and Mode the
// replaced file's permissions (0: leave as copied).
type updPlanFile struct {
	Name   string
	Stored string
	Size   int64
	Mode   uint32
}

// updPlan is the probation plan. Dirs are where a backup may be, in order:
// relative to the app folder, or absolute.
type updPlan struct {
	From, To, Journal, Saved string
	ExeSize                  int64
	Dirs                     []string
	Files                    []updPlanFile
	Drop                     []string
}

// updRescue is launch.sh's .update-rescued: the update it undid and how that
// start ended. LogAt is the size errors.txt had when the start began (-1:
// unknown).
type updRescue struct {
	From, To, Journal, Saved string
	Reason, Stage            string
	RC, PID                  int
	LogAt                    int64
}

// updRescueFact is versions/rescued.json: the last update launch.sh undid,
// for the version history; Notice until the card about it was acknowledged.
type updRescueFact struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	Stage  string `json:"stage,omitempty"`
	RC     int    `json:"rc,omitempty"`
	Model  string `json:"model,omitempty"`
	Date   string `json:"date,omitempty"`
	Report string `json:"report,omitempty"`
	Notice bool   `json:"notice"`
}

// updAppDirFn is the folder launch.sh and its markers are in: the
// executable's. A variable so a test can point it at a temporary folder.
var updAppDirFn = func() (string, error) { return P().ExecutableDir() }

// updRescueState is what this start learned from launch.sh. Filled at startup
// (the boot guard asks first, updRescueSettle next); the card reads fact.
var updRescueState struct {
	mu     sync.Mutex
	loaded bool
	marker updRescue
	ok     bool
	fact   *updRescueFact
}

// ── The plan's format ──────────────────────────────────────────────────────

// updPlanClean keeps a value on one line.
func updPlanClean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// updPlanSafeName accepts a relative slash path with no empty, "." or ".."
// part and nothing launch.sh could misread.
func updPlanSafeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\t\r\n\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// updPlanSafeStored accepts a plain file or folder name.
func updPlanSafeStored(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "/\\\t\r\n ")
}

// updPlanSafeDir accepts a folder path without "..".
func updPlanSafeDir(d string) bool {
	if d == "" || strings.ContainsAny(d, "\t\r\n\\") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(d, "/"), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// updPlanText writes the plan as launch.sh reads it: "key value" lines, and
// "file <name>\t<stored>\t<size>[\t<mode>]" per replaced file, the release
// stamp last.
func updPlanText(p updPlan) string {
	var b strings.Builder
	b.WriteString("# JukaHub update check (updrescue.go): launch.sh puts these files back if the update does not\n")
	b.WriteString("# reach a working screen. The new build deletes this file once it has.\n")
	b.WriteString("version " + updPlanVersion + "\n")
	for _, kv := range [][2]string{{"from", p.From}, {"to", p.To}, {"journal", p.Journal}, {"saved", p.Saved}} {
		if v := updPlanClean(kv[1]); v != "" {
			b.WriteString(kv[0] + " " + v + "\n")
		}
	}
	b.WriteString("exe " + strconv.FormatInt(p.ExeSize, 10) + "\n")
	for _, d := range p.Dirs {
		b.WriteString("dir " + d + "\n")
	}
	for _, f := range p.Files {
		b.WriteString("file " + f.Name + "\t" + f.Stored + "\t" + strconv.FormatInt(f.Size, 10))
		if f.Mode&0o400 != 0 {
			b.WriteString(fmt.Sprintf("\t%04o", f.Mode&0o777))
		}
		b.WriteByte('\n')
	}
	for _, d := range p.Drop {
		b.WriteString("drop " + d + "\n")
	}
	return b.String()
}

// updPlanParse reads a plan with launch.sh's rules: version 1, a JukaHub
// size, at least one file, and no path that leaves the folders it names.
func updPlanParse(data []byte) (updPlan, error) {
	var p updPlan
	version, exe := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "version":
			version = val
		case "from":
			p.From = val
		case "to":
			p.To = val
		case "journal":
			p.Journal = val
		case "saved":
			p.Saved = val
		case "exe":
			exe = val
		case "dir":
			if !updPlanSafeDir(val) {
				return p, fmt.Errorf("unusable folder %q", val)
			}
			p.Dirs = append(p.Dirs, val)
		case "file":
			fields := strings.Split(val, "\t")
			if len(fields) < 2 || len(fields) > 4 {
				return p, fmt.Errorf("unusable file line %q", val)
			}
			f := updPlanFile{Name: fields[0], Stored: fields[1]}
			if !updPlanSafeName(f.Name) || !updPlanSafeStored(f.Stored) {
				return p, fmt.Errorf("unusable file %q", f.Name)
			}
			if len(fields) > 2 {
				n, err := strconv.ParseInt(fields[2], 10, 64)
				if err != nil || n < 0 {
					return p, fmt.Errorf("unusable size for %q", f.Name)
				}
				f.Size = n
			}
			if len(fields) > 3 {
				m, err := strconv.ParseUint(fields[3], 8, 32)
				if err != nil || m > 0o777 {
					return p, fmt.Errorf("unusable mode for %q", f.Name)
				}
				f.Mode = uint32(m)
			}
			p.Files = append(p.Files, f)
		case "drop":
			if !updPlanSafeName(val) {
				return p, fmt.Errorf("unusable file %q", val)
			}
			p.Drop = append(p.Drop, val)
		}
	}
	if version != updPlanVersion {
		return p, fmt.Errorf("plan version %q", version)
	}
	n, err := strconv.ParseInt(exe, 10, 64)
	if err != nil || n <= 0 {
		return p, errors.New("no JukaHub size")
	}
	p.ExeSize = n
	if len(p.Files) == 0 {
		return p, errors.New("no files")
	}
	return p, nil
}

// updRescueParse reads launch.sh's .update-rescued. ok is false for anything
// that does not name a release and a known reason.
func updRescueParse(data []byte) (updRescue, bool) {
	r := updRescue{LogAt: -1}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "from":
			r.From = val
		case "to":
			r.To = val
		case "journal":
			r.Journal = val
		case "saved":
			r.Saved = val
		case "reason":
			r.Reason = val
		case "stage":
			r.Stage = val
		case "rc":
			r.RC, _ = strconv.Atoi(val)
		case "pid":
			r.PID, _ = strconv.Atoi(val)
		case "log":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n >= 0 {
				r.LogAt = n
			}
		}
	}
	switch r.Reason {
	case "hang", "crash", "noinput":
	default:
		return r, false
	}
	return r, r.To != ""
}

// ── The plan, as an update is prepared ────────────────────────────────────

// updProbationNoteSwap writes the plan for an update about to be swapped in,
// or withdraws an earlier one: a rollback puts a version back on purpose, and
// a launcher that does not watch would never act on it. uhistNoteSwap calls it
// once the journal has copied every file the swap overwrites.
func updProbationNoteSwap(j *journal, exeDir string, p uhistPending) {
	if runtime.GOOS == "windows" || j == nil || exeDir == "" {
		return
	}
	if p.Reason != uhistReasonUpdate || strings.TrimSpace(os.Getenv(updProbationEnv)) != "1" || !launchedByDeviceLauncher() {
		updProbationWithdraw(exeDir)
		return
	}
	plan, err := updProbationPlanFor(j, exeDir, p)
	if err != nil {
		logPatch("update check: this update cannot be undone by launch.sh if it does not start: %v", err)
		updProbationWithdraw(exeDir)
		return
	}
	// What an earlier plan kept aside, and its starts, belong to that plan.
	updProbationWithdraw(exeDir)
	if err := bootGuardWriteDurable(filepath.Join(exeDir, updProbationName), []byte(updPlanText(plan))); err != nil {
		logPatch("update check: the plan could not be written (%v); launch.sh will not undo this update", err)
		return
	}
	logPatch("update check: if %s does not start, launch.sh puts %s back (%d files)", plan.To, uhistLabel(plan.From, "", ""), len(plan.Files))
}

// updProbationPlanFor builds the plan from the swap's journal: every file it
// replaces with its backup, the release stamp last, and the release stamp
// dropped again when the update adds one.
func updProbationPlanFor(j *journal, exeDir string, p uhistPending) (updPlan, error) {
	plan := updPlan{From: p.FromTag, To: p.ToTag, Journal: j.ID, Saved: uhistEntryID(p.FromTag, p.CreatedAt)}
	if !updPlanSafeStored(plan.Journal) || !updPlanSafeStored(plan.Saved) {
		return plan, errors.New("unusable journal name")
	}
	if updPlanClean(plan.To) == "" {
		return plan, errors.New("the update has no release name")
	}
	base, err := PatchStateDir()
	if err != nil {
		return plan, err
	}
	versions, err := uhistDir()
	if err != nil {
		return plan, err
	}
	// launch.sh checks the JukaHub it is about to start is the one this
	// update installs: a build copied over by hand later is not on probation.
	staging, err := stagingDirFor(p.ToTag)
	if err != nil {
		return plan, err
	}
	bin := findStagedBinary(staging)
	if bin == "" {
		return plan, errors.New("the new JukaHub is not in the staged release")
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return plan, errors.New("the new JukaHub is not in the staged release")
	}
	plan.ExeSize = info.Size()
	// The new build has to answer the start check, or every start of it is
	// undone: an older release installed from a file does not. Its launch.sh
	// carries the same feature's token.
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "launch.sh")); err != nil || !strings.Contains(string(data), updProbationToken) {
		return plan, errors.New("the new release does not report how its start went (it predates the update check)")
	}
	exe := false
	for _, e := range j.Entries {
		rel, err := filepath.Rel(exeDir, e.Path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		name := filepath.ToSlash(rel)
		switch e.Kind {
		case "replace":
			if isPreservedInstallPath(rel) {
				continue
			}
			if e.Backup == "" {
				return plan, fmt.Errorf("%s has no backup", name)
			}
			st, err := os.Stat(e.Backup)
			if err != nil || !st.Mode().IsRegular() {
				return plan, fmt.Errorf("the backup of %s is missing", name)
			}
			f := updPlanFile{Name: name, Stored: filepath.Base(e.Backup), Size: st.Size()}
			if cur, err := os.Stat(e.Path); err == nil {
				f.Mode = uint32(cur.Mode().Perm())
			}
			if !updPlanSafeName(f.Name) || !updPlanSafeStored(f.Stored) {
				return plan, fmt.Errorf("%q cannot be listed for launch.sh", name)
			}
			plan.Files = append(plan.Files, f)
			exe = exe || name == "JukaHub"
		case "remove":
			if name == releaseInfoFileName {
				plan.Drop = append(plan.Drop, name)
			}
		}
	}
	if !exe {
		return plan, errors.New("the update does not replace JukaHub itself")
	}
	sort.SliceStable(plan.Files, func(a, b int) bool {
		return plan.Files[a].Name != releaseInfoFileName && plan.Files[b].Name == releaseInfoFileName
	})
	for _, d := range []string{
		filepath.Join(base, "journals", "files", j.ID), // until the next start adopts them
		filepath.Join(versions, plan.Saved),            // adopted as a saved version
		filepath.Join(base, updProbationDir, j.ID),     // kept aside (no versions kept)
	} {
		d = updPlanDirFor(exeDir, d)
		if !updPlanSafeDir(d) {
			return plan, fmt.Errorf("unusable folder %q", d)
		}
		plan.Dirs = append(plan.Dirs, d)
	}
	return plan, nil
}

// updPlanDirFor is d relative to the app folder when it is inside it (the
// card's mount point may differ between the two firmwares), else absolute.
func updPlanDirFor(exeDir, d string) string {
	if rel, err := filepath.Rel(exeDir, d); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(d)
}

// updProbationWithdraw ends a plan: the plan, launch.sh's count of its
// starts, the start check, and the backups kept aside for it.
func updProbationWithdraw(exeDir string) {
	for _, name := range []string{updProbationName, updProbationTriesName, updStartCheckName} {
		_ = os.Remove(filepath.Join(exeDir, name))
	}
	if base, err := PatchStateDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(base, updProbationDir))
	}
}

// updProbationForget withdraws the plan of a swap whose helper never started
// (uhistForgetSwap).
func updProbationForget(j *journal) {
	if runtime.GOOS == "windows" || j == nil {
		return
	}
	dir, err := updAppDirFn()
	if err != nil || dir == "" {
		return
	}
	if plan, ok := updProbationRead(dir); ok && plan.Journal == j.ID {
		updProbationWithdraw(dir)
	}
}

// updProbationRead reads the plan in force in dir.
func updProbationRead(dir string) (updPlan, bool) {
	data, err := crashReadHead(filepath.Join(dir, updProbationName), updPlanMax)
	if err != nil {
		return updPlan{}, false
	}
	plan, err := updPlanParse(data)
	return plan, err == nil
}

// updProbationHeld names the journal and the saved version the plan in force
// restores from; "" without one. The version history never prunes them.
func updProbationHeld() (journalID, savedID string) {
	if runtime.GOOS == "windows" {
		return "", ""
	}
	dir, err := updAppDirFn()
	if err != nil || dir == "" {
		return "", ""
	}
	plan, ok := updProbationRead(dir)
	if !ok {
		return "", ""
	}
	return plan.Journal, plan.Saved
}

// updProbationKeepAside moves a probation journal's backups out of the
// startup purge's way when no saved version took them (none are kept, or
// keeping one failed): launch.sh still needs them until the update started.
func updProbationKeepAside(base, filesDir, journalID string) {
	if !pathExists(filesDir) || !updPlanSafeStored(journalID) {
		return
	}
	dest := filepath.Join(base, updProbationDir, journalID)
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err == nil {
		err = os.Rename(filesDir, dest)
		if err == nil {
			logPatch("update check: kept the replaced files until the update has started fine")
			return
		}
		logPatch("update check: could not keep the replaced files (%v); launch.sh cannot undo this update", err)
	}
}

// ── The restored build's start ────────────────────────────────────────────

// updRescueRead reads launch.sh's marker beside the executable.
func updRescueRead() (updRescue, bool) {
	if runtime.GOOS == "windows" {
		return updRescue{}, false
	}
	dir, err := updAppDirFn()
	if err != nil || dir == "" {
		return updRescue{}, false
	}
	data, err := crashReadHead(filepath.Join(dir, updRescuedName), updMarkerMax)
	if err != nil {
		return updRescue{}, false
	}
	return updRescueParse(data)
}

// updRescuePending is launch.sh's marker for this start, read once. The boot
// guard asks before the Patch module has loaded (initBootGuard).
func updRescuePending() (updRescue, bool) {
	updRescueState.mu.Lock()
	defer updRescueState.mu.Unlock()
	if !updRescueState.loaded {
		updRescueState.loaded = true
		updRescueState.marker, updRescueState.ok = updRescueRead()
	}
	return updRescueState.marker, updRescueState.ok
}

// updRescueSettle runs at startup with the version history's folders
// (uhistAdoptCommittedSwaps, before any pending swap is adopted): it settles
// an update launch.sh undid, drops what a finished plan left, and loads the
// last rescue for the card.
func updRescueSettle(base, versionsDir string) {
	if runtime.GOOS == "windows" {
		return
	}
	appDir, err := updAppDirFn()
	if err != nil || appDir == "" {
		return
	}
	if !pathExists(filepath.Join(appDir, updProbationName)) {
		_ = os.RemoveAll(filepath.Join(base, updProbationDir))
		_ = os.Remove(filepath.Join(appDir, updProbationTriesName))
		_ = os.Remove(filepath.Join(appDir, updStartCheckName))
		// The hardware check's flag acts on the next update of the session it
		// was put down in, never on a real update weeks later (startcheck.go).
		if err := os.Remove(filepath.Join(appDir, startCheckTestName)); err == nil {
			logPatch("update check: removed %s: no update was on probation", startCheckTestName)
		}
	}
	if r, ok := updRescuePending(); ok {
		updRescueApply(appDir, base, versionsDir, r)
		updRescueState.mu.Lock()
		updRescueState.ok = false
		updRescueState.mu.Unlock()
	}
	var fact *updRescueFact
	if data, err := os.ReadFile(filepath.Join(versionsDir, updRescueFactName)); err == nil {
		var f updRescueFact
		if json.Unmarshal(data, &f) == nil && f.To != "" {
			fact = &f
		}
	}
	updRescueState.mu.Lock()
	updRescueState.fact = fact
	updRescueState.mu.Unlock()
}

// updRescueApply undoes what the update launch.sh put back had already
// recorded, so the next steps of the start neither finish it nor offer it
// again: its pending version record (it never committed), the saved copy of
// the build that runs again, its staged copy, and the patch state's tags.
func updRescueApply(appDir, base, versionsDir string, r updRescue) {
	restored := readInstalledReleaseInfo()
	notes := ""
	if updPlanSafeStored(r.Journal) {
		pp := uhistPendingPath(versionsDir, r.Journal)
		if data, err := os.ReadFile(pp); err == nil {
			var p uhistPending
			if json.Unmarshal(data, &p) == nil {
				notes = p.Notes
			}
			_ = os.Remove(pp)
		}
		// Its backups: kept aside ones go now; journals/files/<id> without a
		// journal goes with the startup purge, and with one the journal check
		// undoes what is left.
		_ = os.RemoveAll(filepath.Join(base, updProbationDir, r.Journal))
	}
	if updPlanSafeStored(r.Saved) {
		folder := filepath.Join(versionsDir, r.Saved)
		if data, err := os.ReadFile(filepath.Join(folder, "version.json")); err == nil {
			var e uhistEntry
			if json.Unmarshal(data, &e) == nil && notes == "" {
				notes = e.Notes
			}
		}
		// A copy of the build that runs again.
		_ = os.RemoveAll(folder)
	}
	changed := false
	patchStateMu.Lock()
	if strings.EqualFold(patchState.LastUpdateVer, r.To) {
		patchState.LastUpdateVer = ""
		changed = true
	}
	if strings.EqualFold(patchState.AvailableUpdateTag, r.To) {
		patchState.AvailableUpdateTag, patchState.AvailableUpdateAt, patchState.AvailableUpdateAsset = "", "", ""
		patchState.AvailableUpdateDigest, patchState.AvailableUpdateSize = "", 0
		changed = true
	}
	// The restored stamp, or none when the update had added the stamp (the
	// plan's drop): never the release that was undone, which delta updates
	// and "is this installed?" would otherwise take for the installation.
	if strings.EqualFold(patchState.InstalledReleaseTag, r.To) && !strings.EqualFold(restored.Tag, r.To) {
		patchState.InstalledReleaseTag, patchState.InstalledReleaseAt = restored.Tag, restored.Date
		changed = true
	}
	patchStateMu.Unlock()
	if changed {
		if err := savePatchState(); err != nil {
			logPatch("update check: %v", err)
		}
	}
	if staging, err := stagingDirFor(r.To); err == nil {
		_ = os.RemoveAll(staging)
		_ = os.Remove(stagedReadyMarker(staging))
	}
	// Not offered again until a newer release is published.
	st := uhistReadState(versionsDir)
	st.SkipTag = r.To
	uhistWriteState(versionsDir, st)
	if from := strings.TrimSpace(restored.Tag); from != "" && notes != "" {
		uhistWriteInstalledNotes(versionsDir, from, notes)
	}
	fact := updRescueFact{
		From:   r.From,
		To:     r.To,
		Reason: r.Reason,
		Stage:  r.Stage,
		RC:     r.RC,
		Model:  string(DetectDevice()),
		Date:   vfDateNow(),
		Report: updRescueReport(appDir, r),
		Notice: true,
	}
	if err := upchWriteJSON(filepath.Join(versionsDir, updRescueFactName), fact); err != nil {
		logPatch("update check: %v", err)
	}
	for _, name := range []string{updRescuedName, updStartCheckName, startCheckTestName} {
		_ = os.Remove(filepath.Join(appDir, name))
	}
	logPatch("update check: launch.sh put %s back because %s did not start (%s, stage %s, exit code %d); %s is not offered again",
		uhistLabel(r.From, "", ""), r.To, r.Reason, r.Stage, r.RC, r.To)
	update, _ := updRescueNames(fact)
	notifRecord("The "+update+" didn't start, so JukaHub put back the version you had", notifWarning)
}

// updRescueReport is the crash report for the start launch.sh stopped: the one
// this start filed from that process's crash output, or else what errors.txt
// got from it (a start that never reached main has no crash output). "" when
// there is nothing.
func updRescueReport(appDir string, r updRescue) string {
	if report, _ := crashLastReport(); report != "" {
		return report
	}
	if r.LogAt < 0 {
		return ""
	}
	f, err := os.Open(filepath.Join(appDir, updLogName))
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(r.LogAt, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, updRescueLogMax))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return ""
	}
	dir, err := crashLogDir()
	if err != nil || dir == "" {
		return ""
	}
	path := crashArchiveName(dir, time.Now())
	head := fmt.Sprintf("JukaHub update check: %s did not start (%s, exit code %d) and launch.sh put %s back. "+
		"Its output follows, the goroutine stacks last.\n", r.To, r.Reason, r.RC, r.From)
	if err := os.WriteFile(path, append([]byte(head), data...), 0o644); err != nil {
		return ""
	}
	return path
}

// ── The card ─────────────────────────────────────────────────────────────────

// updRescueNames names the two builds for people: "2026-11-20 update" and
// "2026-11-19 version" for dated releases.
func updRescueNames(f updRescueFact) (update, version string) {
	to, from := vfVersionLabel(f.To), vfVersionLabel(f.From)
	update = "update to " + to
	if d, ok := strings.CutSuffix(to, " build"); ok {
		update = d + " update"
	}
	version = "the version you had before"
	if d, ok := strings.CutSuffix(from, " build"); ok {
		version = "the " + d + " version you had"
	} else if strings.TrimSpace(from) != "" {
		version = from + ", the version you had"
	}
	return update, version
}

// updRescueSentence is what happened, in one paragraph.
func updRescueSentence(f updRescueFact) string {
	update, version := updRescueNames(f)
	where := "on this " + vfModelShort(f.Model)
	if strings.HasPrefix(vfModelShort(f.Model), "this ") {
		where = "on " + vfModelShort(f.Model)
	}
	what := "it stopped responding before its first screen"
	switch {
	case f.Reason == "noinput":
		what = "its screen came up, but the buttons did nothing"
	case f.Reason == "crash" && f.Stage == "frame":
		what = "it closed as it started"
	case f.Reason == "crash":
		what = "it closed before its first screen"
	case f.Stage == "frame":
		what = "it stopped responding as it started"
	}
	return "The " + update + " didn't start properly " + where + " — " + what + ". JukaHub put back " + version +
		". Your games, saves and settings were not changed."
}

// updRescueOpenAtBoot shows the card about an update launch.sh undid, until
// it is acknowledged. main.go calls it right after the recovery card's own
// boot choice, which it replaces: it says more than "closed unexpectedly". The
// recovery chord's menu wins; the card waits for the next start then.
func updRescueOpenAtBoot(config *Config) {
	updRescueState.mu.Lock()
	fact := updRescueState.fact
	updRescueState.mu.Unlock()
	if fact == nil || !fact.Notice || safeboot.Triggered {
		return
	}
	f := *fact
	options := []recoveryOption{{icon: "check", title: "OK", detail: "Back to JukaHub",
		run: func(*Config) { updRescueAck(); recoveryCardClose() }}}
	if f.Report != "" {
		options = append(options, recoveryOption{icon: "bug", title: "Send report",
			detail: "Opens Report a bug with what that start left; nothing is sent before Submit",
			run:    func(c *Config) { updRescueSendReport(c, f) }})
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Update undone",
		subtitle: "JukaHub fixed itself after an update",
		icon:     "restore",
		body:     updRescueSentence(f) + "\n\nThat update won't be offered again. The next newer one will be.",
		options:  options,
		back:     func(*Config) { updRescueAck(); recoveryCardClose() },
	})
}

// updRescueAck marks the card as seen (the history keeps the fact).
func updRescueAck() {
	updRescueState.mu.Lock()
	fact := updRescueState.fact
	if fact == nil || !fact.Notice {
		updRescueState.mu.Unlock()
		return
	}
	f := *fact
	f.Notice = false
	updRescueState.fact = &f
	updRescueState.mu.Unlock()
	go func() {
		dir, err := uhistDir()
		if err != nil {
			return
		}
		if err := upchWriteJSON(filepath.Join(dir, updRescueFactName), f); err != nil {
			logPatch("update check: %v", err)
		}
	}()
}

// updRescueSendReport opens Report a bug with what the stopped start left.
// Reading the report happens off the render thread; nothing is sent here.
func updRescueSendReport(config *Config, f updRescueFact) {
	updRescueAck()
	recoveryCardClose()
	go func() {
		summary := ""
		if data, err := crashReadHead(f.Report, crashReadMax); err == nil {
			summary = updRescueSummary(string(data))
		}
		tbxPost(func() {
			desc := updRescueSentence(f) + " Report " + filepath.Base(f.Report)
			if summary != "" {
				desc += ":\n\n" + summary
			} else {
				desc += "."
			}
			bugrepScreen.form = bugrepForm{
				Category:    0, // "Crash"
				Title:       "An update didn't start",
				Description: desc,
				Diagnostics: true,
				Log:         true,
			}
			bugrepScreen.touched = true
			bugrepOpenScreen(config)
		})
	}()
}

// updRescueSummary is the part of the stopped start's output a report
// carries: the main goroutine's stack (a hang in package init or main is
// there), else what crashSummary picks.
func updRescueSummary(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if i := strings.Index(text, "\ngoroutine 1 ["); i >= 0 {
		block := text[i+1:]
		if end := strings.Index(block, "\n\ngoroutine "); end >= 0 {
			block = block[:end]
		}
		return bugrepClip(strings.TrimSpace(block), crashSummaryMax)
	}
	return crashSummary(text)
}
