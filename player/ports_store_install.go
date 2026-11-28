package main

// ports_store_install.go — the one queue every PortMaster zip JukaHub
// installs goes through (the contract is in store_scenes.go).
//
// A job is a catalogue port to download (the Ports Store, the Ports panel)
// or a zip already downloaded and checked (portsInstallZip: GreenOvercast for
// Cloud Gaming). One worker takes them in order:
//
//  1. PortMaster must be on the card. When it is not, JukaHub installs it
//     first and the queue waits for that.
//  2. A catalogue port is downloaded into portsStagingDir() and checked
//     against the MD5 PortMaster's catalogue lists (portCatalogDownload).
//  3. JukaHub installs it itself (ports_native_install.go): the zip's
//     folders and scripts where PortMaster puts them, harbourmaster's status
//     block in port.json, the runtimes the port declares from PortMaster's
//     catalogue. No Python, no PortMaster app. A zip it refuses as not a port
//     it installs (PortMaster's own update, an unsafe path) goes on to 4.
//  4. PortMaster's own installer, headless: the PortMaster folder's
//     harbourmaster, run the way PortMaster's launcher would run it
//     (control.txt sourced, PortMaster's own HM_* paths), with
//     `--no-check install <zip>` — the documented way to install a local zip.
//     It counts as installed only when it exits 0 and the port's launcher is
//     in a ports folder afterwards.
//  5. Otherwise the zip goes into PortMaster's autoinstall folder, and once
//     the queue is empty PortMaster is opened once to install everything
//     waiting there (when the caller allows it and no game holds the
//     screen) — the path PortMaster documents for hand-copied zips.
//
// Custom setting portsStoreInstaller: "auto" (the default, as above),
// "jukahub" (3 only), "harbourmaster" (4, then 5) or "portmaster" (always 5).
// Every install is logged to data/gamediag/ports/store-install.log.
//
// None of this has run on a handheld yet (harbourmaster needs Python: on
// stock TrimUI firmware PortMaster brings its own through TRIMUI_EX; CrossMix
// ships one). A harbourmaster that cannot start just leads to
// step 5.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// pstoreHarbourTimeout bounds one headless install (runtimes included).
	pstoreHarbourTimeout = 10 * time.Minute
	// pstoreWaitPortMaster bounds the wait for PortMaster's own install.
	pstoreWaitPortMaster = 15 * time.Minute
	// pstoreWaitOtherDownload bounds the wait for another tool's download.
	pstoreWaitOtherDownload = 10 * time.Minute
	// pstoreKeepFinished is how many settled jobs stay listed for the cards.
	pstoreKeepFinished = 40
	// pstoreInstallLogMax caps store-install.log before it starts again.
	pstoreInstallLogMax = 512 << 10
)

// pstoreJobState is where a job is.
type pstoreJobState int

const (
	pstoreJobQueued pstoreJobState = iota
	pstoreJobWaiting
	pstoreJobDownloading
	pstoreJobInstalling
	pstoreJobInstalled
	pstoreJobPortMaster // waiting in PortMaster's autoinstall folder
	pstoreJobFailed
)

// pstoreJob is one install.
type pstoreJob struct {
	key    string         // lower-case zip name
	name   string         // shown in toasts and the status strip
	entry  mcCatalogEntry // a catalogue port (Zip set) …
	zip    string         // … or a zip already downloaded
	store  bool           // a catalogue port queued from a screen
	openPM bool           // the fallback may open PortMaster whatever is on screen
	keep   bool           // the zip is borrowed: removed only once installed
	done   func(portsInstallResult)
	state  pstoreJobState
	note   string // what it waits for, or why it failed

	// mode and env are snapshotted on the main loop when the job is queued
	// (store_scenes.go), so the worker never reads the live config's maps.
	mode string
	env  []string

	// gen is the status-strip run the worker began for the job (0 none) and
	// path the zip it is installing. Worker goroutine only.
	gen  uint64
	path string
}

// pstoreErrCancelled is a job the player stopped from the status strip.
var pstoreErrCancelled = errors.New("cancelled")

// pstoreStaleAfter is how old a file in the staging folder must be, with no
// job naming it, before the worker sweeps it away.
const pstoreStaleAfter = time.Hour

var pstoreQueue struct {
	mu      sync.Mutex
	jobs    []*pstoreJob
	running bool
}

// pstoreJobView is a job as the cards see it.
type pstoreJobView struct {
	state pstoreJobState
	note  string
	store bool // a catalogue job (pstoreCancel can drop it while queued)
}

// pstoreJobLookup returns the newest job for a zip name.
func pstoreJobLookup(zipName string) (pstoreJobView, bool) {
	key := strings.ToLower(zipName)
	pstoreQueue.mu.Lock()
	defer pstoreQueue.mu.Unlock()
	for i := len(pstoreQueue.jobs) - 1; i >= 0; i-- {
		if j := pstoreQueue.jobs[i]; j.key == key {
			return pstoreJobView{state: j.state, note: j.note, store: j.store}, true
		}
	}
	return pstoreJobView{}, false
}

// pstoreJobActive reports whether a state still has work ahead.
func pstoreJobActive(s pstoreJobState) bool {
	return s == pstoreJobQueued || s == pstoreJobWaiting || s == pstoreJobDownloading || s == pstoreJobInstalling
}

// pstoreQueueCounts is (waiting or running jobs, the job running now's name).
func pstoreQueueCounts() (int, string) {
	pstoreQueue.mu.Lock()
	defer pstoreQueue.mu.Unlock()
	n, now := 0, ""
	for _, j := range pstoreQueue.jobs {
		if pstoreJobActive(j.state) {
			n++
			if j.state != pstoreJobQueued && now == "" {
				now = j.name
			}
		}
	}
	return n, now
}

// pstoreEnqueue queues a catalogue port from the store: a fallback install
// opens PortMaster only while the store or the Ports panel is up. Main
// thread.
func pstoreEnqueue(e mcCatalogEntry) error { return pstoreEnqueueWith(e, false) }

// pstoreEnqueueWith queues a catalogue port; openPM lets the fallback open
// PortMaster from any screen (portCatalogInstallStart's callers expect it).
func pstoreEnqueueWith(e mcCatalogEntry, openPM bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("ports install on the handheld — browse here, install there")
	}
	if e.Zip == "" || filepath.Base(e.Zip) != e.Zip || !strings.EqualFold(filepath.Ext(e.Zip), ".zip") {
		return errors.New("this catalogue entry has no valid archive")
	}
	name := pstoreTitle(e)
	if v, ok := pstoreJobLookup(e.Zip); ok && pstoreJobActive(v.state) {
		return errors.New(name + " is already in the queue")
	}
	if portMasterLauncher(portMasterSDRoot) == "" && !portMasterInstalling() && !IsOnline() {
		return errors.New("connect to Wi-Fi — PortMaster and the port download first")
	}
	pstoreAddJob(&pstoreJob{key: strings.ToLower(e.Zip), name: name, entry: e, store: true, openPM: openPM,
		mode: pstoreInstallerMode(), env: portBaseEnv(appConfig)})
	return nil
}

// pstoreCancel drops a catalogue job that has not started. A prepared zip
// (portsInstallZip) is never dropped here: its caller waits for Done. Main
// thread.
func pstoreCancel(zipName string) bool {
	key := strings.ToLower(zipName)
	pstoreQueue.mu.Lock()
	defer pstoreQueue.mu.Unlock()
	for i, j := range pstoreQueue.jobs {
		if j.key == key && j.state == pstoreJobQueued && j.store {
			pstoreQueue.jobs = append(pstoreQueue.jobs[:i], pstoreQueue.jobs[i+1:]...)
			return true
		}
	}
	return false
}

// portsInstallZip installs req.Zip through the queue (store_scenes.go).
// Main loop only: it snapshots the installer setting and the firmware
// environment from the live config. req.Done runs on the main loop.
func portsInstallZip(req portsInstallRequest) {
	if runtime.GOOS != "linux" {
		if !req.Keep {
			_ = os.Remove(req.Zip)
		}
		if req.Done != nil {
			done := req.Done
			tbxPost(func() { done(portsInstallResult{Err: errors.New("ports install on the handheld")}) })
		}
		return
	}
	name := setOr(req.Name, strings.TrimSuffix(filepath.Base(req.Zip), filepath.Ext(req.Zip)))
	pstoreAddJob(&pstoreJob{
		key: strings.ToLower(filepath.Base(req.Zip)), name: name, zip: req.Zip,
		openPM: req.OpenPortMaster, keep: req.Keep, done: req.Done,
		mode: pstoreInstallerMode(), env: portBaseEnv(appConfig),
	})
}

// pstoreAddJob appends a job and starts the worker when it is idle.
func pstoreAddJob(j *pstoreJob) {
	pstoreQueue.mu.Lock()
	// Settled jobs for the same zip go: the newest one is the card's.
	kept := make([]*pstoreJob, 0, len(pstoreQueue.jobs)+1)
	finished := 0
	for i := len(pstoreQueue.jobs) - 1; i >= 0; i-- {
		old := pstoreQueue.jobs[i]
		if !pstoreJobActive(old.state) && (old.key == j.key || finished >= pstoreKeepFinished) {
			continue
		}
		if !pstoreJobActive(old.state) {
			finished++
		}
		kept = append(kept, old)
	}
	// kept was filled newest first; put it back in order.
	for a, b := 0, len(kept)-1; a < b; a, b = a+1, b-1 {
		kept[a], kept[b] = kept[b], kept[a]
	}
	pstoreQueue.jobs = append(kept, j)
	start := !pstoreQueue.running
	pstoreQueue.running = true
	pstoreQueue.mu.Unlock()
	if start {
		go pstoreWorker()
	}
}

// pstoreSetState records a job's state under the lock.
func pstoreSetState(j *pstoreJob, s pstoreJobState, note string) {
	pstoreQueue.mu.Lock()
	j.state, j.note = s, note
	pstoreQueue.mu.Unlock()
}

// pstoreNextJob takes the next queued job, or marks the worker idle and
// returns nil — under one lock, so a job added meanwhile is never stranded.
func pstoreNextJob() *pstoreJob {
	pstoreQueue.mu.Lock()
	defer pstoreQueue.mu.Unlock()
	for _, j := range pstoreQueue.jobs {
		if j.state == pstoreJobQueued {
			j.state = pstoreJobWaiting
			return j
		}
	}
	pstoreQueue.running = false
	return nil
}

// pstoreWorker runs the queue until it is empty, then opens PortMaster once
// for whatever it left in the autoinstall folder.
func pstoreWorker() {
	// queued counts zips left for PortMaster; quiet ones have no Done to
	// tell the player, so the toast below does.
	queued, quiet, onScreen, open := 0, 0, 0, false
	pstoreSweepStaging()
	for {
		j := pstoreNextJob()
		if j == nil {
			break
		}
		res := pstoreRunGuarded(j)
		switch {
		case res.Installed:
			pstoreSetState(j, pstoreJobInstalled, "")
		case res.Queued:
			pstoreSetState(j, pstoreJobPortMaster, "")
			queued++
			if j.done == nil {
				quiet++
			}
			if j.openPM {
				open = true
			} else if j.store {
				onScreen++
			}
		default:
			note := "failed"
			if res.Err != nil {
				note = firstLine(res.Err.Error())
			}
			pstoreSetState(j, pstoreJobFailed, note)
		}
		name, done := j.name, j.done
		tbxPost(func() {
			pstoreAfterChange()
			switch {
			case res.Installed:
				if done == nil {
					PlaySuccessSound()
					ShowToast(name+" is installed — it is in Ports and the Games library", ToastKindSuccess)
				}
			case res.Err != nil && done == nil && !errors.Is(res.Err, pstoreErrCancelled):
				ShowToast("Could not install "+name+": "+firstLine(res.Err.Error()), ToastKindError)
			}
			if done != nil {
				done(res)
			}
		})
	}
	if queued == 0 {
		return
	}
	tbxPost(func() {
		launcher := portMasterLauncher(portMasterSDRoot)
		openNow := open || (onScreen > 0 && (portsStoreOnScreen(appConfig) || portsPanel.open))
		if launcher == "" || !openNow || gameScreenSuspended() {
			// Said when nobody else will: jobs without a Done, or a caller
			// that asked for PortMaster to open and it could not.
			if n := quiet; n > 0 || open {
				if n == 0 {
					n = queued
				}
				ShowToast(pluralizeCount(n, "port waits", "ports wait")+
					" in PortMaster's install folder — open PortMaster to finish", ToastKindInfo)
			}
			return
		}
		portsPanel.open = false
		launchPortScript(portMasterAppName, launcher)
		ShowToast("PortMaster is installing what JukaHub downloaded — close it when it is done", ToastKindInfo)
	})
}

// pstoreRunGuarded runs one job; a panic fails the job, not JukaHub.
func pstoreRunGuarded(j *pstoreJob) (res portsInstallResult) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("portsstore", "installing %s failed: %v", j.name, r)
			res = portsInstallResult{Err: fmt.Errorf("internal error: %v", r)}
			// The strip must not stay "working" (it would block every other
			// download), and the zip is this job's to clean up.
			if j.gen != 0 {
				installBarFailRun(j.gen, "internal error")
			}
			// A borrowed zip (j.keep) stays where the player put it.
			if j.path != "" && !j.keep {
				_ = os.Remove(j.path)
			} else if j.zip != "" && !j.keep {
				_ = os.Remove(j.zip)
			}
		}
	}()
	return pstoreRunJob(j)
}

// pstoreRunJob does one job.
func pstoreRunJob(j *pstoreJob) portsInstallResult {
	fail := func(err error) portsInstallResult {
		if j.zip != "" && !j.keep {
			_ = os.Remove(j.zip)
		}
		return portsInstallResult{Err: err}
	}
	launcher, err := pstoreEnsurePortMaster(j)
	if err != nil {
		return fail(err)
	}
	// The status strip shows one run at a time, and a begin retires the run
	// before it: another tool's download goes first.
	if !pstoreWaitForBar(j) {
		return fail(errors.New("another download is still running — try again when it finishes"))
	}
	zipPath := j.zip
	gen, card := installBarBeginRun(j.name)
	j.gen = gen
	if zipPath == "" {
		pstoreSetState(j, pstoreJobDownloading, "")
		if !IsOnline() {
			installBarFailRun(gen, "No internet")
			return portsInstallResult{Err: errors.New("no internet — connect Wi-Fi and try again")}
		}
		staging := portsStagingDir()
		// A leftover with the same name is from an earlier, interrupted run —
		// unless a job still waiting in the queue owns that very file.
		if target := filepath.Join(staging, j.entry.Zip); !pstoreStagingInUse(target, j) {
			_ = os.Remove(target)
		}
		ctx, cancel := mcRunContext(gen, card)
		path, derr := portCatalogDownload(ctx, gen, j.entry, staging)
		stopped := ctx.Err() != nil
		cancel()
		if derr != nil {
			if stopped {
				installBarAbortRun(gen)
				return portsInstallResult{Err: pstoreErrCancelled}
			}
			installBarFailRun(gen, firstLine(derr.Error()))
			return portsInstallResult{Err: derr}
		}
		zipPath = path
	}
	j.path = zipPath
	pstoreSetState(j, pstoreJobInstalling, "")
	installBarUpdateRun(gen, installPhaseExtract, 0, 0)
	res := pstoreInstallFile(j, launcher, zipPath, card)
	switch {
	case errors.Is(res.Err, pstoreErrCancelled):
		installBarAbortRun(gen)
	case res.Err != nil:
		installBarFailRun(gen, firstLine(res.Err.Error()))
	default:
		installBarDoneRun(gen)
	}
	return res
}

// pstoreWaitForBar lets another tool's running download finish first: the
// status strip shows one operation at a time. false when it is still
// running after pstoreWaitOtherDownload.
func pstoreWaitForBar(j *pstoreJob) bool {
	if !installBarActive() {
		return true
	}
	pstoreSetState(j, pstoreJobWaiting, "Waiting for another download")
	deadline := time.Now().Add(pstoreWaitOtherDownload)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if !installBarActive() {
			return true
		}
	}
	return false
}

// pstoreStagingInUse reports whether a job other than self, still ahead in
// the queue, was handed path.
func pstoreStagingInUse(path string, self *pstoreJob) bool {
	clean := filepath.Clean(path)
	pstoreQueue.mu.Lock()
	defer pstoreQueue.mu.Unlock()
	for _, j := range pstoreQueue.jobs {
		if j != self && pstoreJobActive(j.state) && j.zip != "" && filepath.Clean(j.zip) == clean {
			return true
		}
	}
	return false
}

// pstoreSweepStaging removes what an interrupted run left in the staging
// folder (a panic, a power cut, a killed installer): files older than
// pstoreStaleAfter that no queued job names. Worker goroutine.
func pstoreSweepStaging() {
	dir := portsStagingDir()
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.IsDir() {
			continue
		}
		info, err := it.Info()
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < pstoreStaleAfter {
			continue
		}
		p := filepath.Join(dir, it.Name())
		if pstoreStagingInUse(p, nil) {
			continue
		}
		if os.Remove(p) == nil {
			logDebug("portsstore", "removed a leftover download: %s", it.Name())
		}
	}
}

// pstoreEnsurePortMaster returns PortMaster's launcher, installing PortMaster
// first when the card has none.
func pstoreEnsurePortMaster(j *pstoreJob) (string, error) {
	if l := portMasterLauncher(portMasterSDRoot); l != "" {
		return l, nil
	}
	pstoreSetState(j, pstoreJobWaiting, "Installing PortMaster first")
	if !portMasterInstalling() {
		if !pstoreWaitForBar(j) {
			return "", errors.New("another download is still running — try again when it finishes")
		}
		pstoreSetState(j, pstoreJobWaiting, "Installing PortMaster first")
		if err := startPortMasterInstall(); err != nil {
			return "", fmt.Errorf("PortMaster is needed first: %w", err)
		}
	}
	deadline := time.Now().Add(pstoreWaitPortMaster)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if portMasterInstalling() {
			continue
		}
		if l := portMasterLauncher(portMasterSDRoot); l != "" {
			return l, nil
		}
		return "", errors.New("PortMaster could not be installed — the status strip said why")
	}
	return "", errors.New("PortMaster's install took too long")
}

// pstoreInstallFile installs a checked zip: JukaHub's own installer, else
// PortMaster's headless, else the autoinstall folder. The zip is consumed
// either way (a borrowed one, j.keep, only once installed). card is the
// status-strip run's token, so the strip's X stops the installer too.
func pstoreInstallFile(j *pstoreJob, launcher, zipPath string, card context.Context) portsInstallResult {
	name := j.name
	scripts := pstoreZipScripts(zipPath)
	// drop removes the zip on a failure — not one the job only borrows.
	drop := func() {
		if !j.keep {
			_ = os.Remove(zipPath)
		}
	}
	// JukaHub's own installer first: no Python, no PortMaster app
	// (ports_native_install.go). Only a zip it leaves to PortMaster goes on.
	if j.mode == "auto" || j.mode == "jukahub" {
		ctx, cancel := mcRunContext(j.gen, card)
		res, err := pnInstall(ctx, j.gen, zipPath, j.entry.Zip)
		stopped := ctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			_ = os.Remove(zipPath)
			if res.RuntimeErr != nil {
				msg := name + " is installed, but a runtime it needs could not be downloaded (" + firstLine(res.RuntimeErr.Error()) +
					") — JukaHub tries again when you start it"
				tbxPost(func() { ShowToast(msg, ToastKindWarning) })
			}
			return portsInstallResult{Installed: true}
		case stopped:
			drop()
			return portsInstallResult{Err: pstoreErrCancelled}
		case j.mode == "jukahub" || !errors.Is(err, errPnNotAPort):
			// A failure halfway (the card, the network) is not one PortMaster's
			// installer would get past.
			drop()
			return portsInstallResult{Err: err}
		}
		logInfo("portsstore", "%s: JukaHub's installer leaves it to PortMaster (%v)", name, err)
	}
	// A borrowed zip already waits in PortMaster's autoinstall folder, and
	// harbourmaster may delete it: PortMaster's app takes it from there.
	if j.mode != "portmaster" && !j.keep {
		ok, err := pstoreHarbourInstall(zipPath, scripts, j.env, j.gen, card)
		if ok {
			_ = os.Remove(zipPath)
			logInfo("portsstore", "%s installed by harbourmaster", name)
			return portsInstallResult{Installed: true}
		}
		if errors.Is(err, pstoreErrCancelled) {
			drop()
			return portsInstallResult{Err: pstoreErrCancelled}
		}
		logInfo("portsstore", "%s: headless install not used (%v); handing it to the PortMaster app", name, err)
	}
	if !regularFile(zipPath) {
		return portsInstallResult{Err: errors.New("the download is gone — try again (Settings key portsStoreInstaller: portmaster skips the headless installer)")}
	}
	inbox := mcAutoinstallDir(launcher)
	if inbox == "" {
		drop()
		return portsInstallResult{Err: errors.New("PortMaster has no autoinstall folder; install the port from PortMaster")}
	}
	if err := pstoreMoveFile(zipPath, inbox); err != nil {
		drop()
		return portsInstallResult{Err: fmt.Errorf("PortMaster's autoinstall folder: %w", err)}
	}
	return portsInstallResult{Queued: true}
}

// pstoreMoveFile moves src into dir under its own name, replacing a file of
// that name: a rename, or a copy and delete across file systems.
func pstoreMoveFile(src, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if filepath.Clean(dst) == filepath.Clean(src) {
		return nil
	}
	_ = os.Remove(dst)
	if os.Rename(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	part := filepath.Join(dir, "."+filepath.Base(src)+".part")
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(part)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := mcSyncFile(part); err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		_ = os.Remove(part)
		return err
	}
	_ = os.Remove(src)
	return nil
}

// pstoreZipScripts is the launcher scripts at the top of a port zip — what
// harbourmaster puts in the ports folder.
func pstoreZipScripts(p string) []string {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "./")
		if strings.Contains(name, "/") || !strings.EqualFold(filepath.Ext(name), ".sh") {
			continue
		}
		if !strings.EqualFold(name, "PortMaster.sh") {
			out = append(out, name)
		}
		if len(out) >= 16 {
			break
		}
	}
	return out
}

// pstoreInstalledFrom reports whether one of scripts is in a ports folder as
// this install left it: its port.json records the zip's md5 (sum), or the
// script was written since the run began (since). An update or a reinstall
// starts with the old launcher in place, so presence alone proves nothing.
// The md5 field is unverified on a device, so a different one falls through
// to the time check rather than failing the install.
func pstoreInstalledFrom(scripts []string, sum string, since time.Time) bool {
	portMetaForget()
	for _, root := range portRoots(portMasterSDRoot) {
		for _, s := range scripts {
			p := filepath.Join(root, s)
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if m := portMetaFor(p).MD5; m != "" && sum != "" && strings.EqualFold(m, sum) {
				return true
			}
			// exFAT keeps times to two seconds.
			if !info.ModTime().Before(since.Add(-2 * time.Second)) {
				return true
			}
		}
	}
	return false
}

// pstoreHMLine matches an `export HM_NAME=value` line of PortMaster's
// launcher whose value is a plain path: letters, digits and _ . / : @ + - plus
// $NAME / ${NAME} expansions, optionally in double quotes. Anything else
// (command substitution, quotes inside, a backslash, spaces) is skipped, so
// nothing from the file can change the wrapper's shape.
var pstoreHMLine = regexp.MustCompile(`(?m)^[ \t]*export[ \t]+(HM_[A-Z0-9_]+)=("?)([A-Za-z0-9_./:@+${}-]*)("?)[ \t]*\r?$`)

// pstoreHMExports are the HM_* exports PortMaster's own launcher scripts make
// (they tell harbourmaster where ports and scripts go on this firmware), in
// file order, at most 16.
func pstoreHMExports(control string) []string {
	files := []string{portMasterLauncher(portMasterSDRoot)}
	if control != "" {
		files = append(files, filepath.Join(control, "PortMaster.sh"), filepath.Join(filepath.Dir(control), "PortMaster.sh"))
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		data, ok := portReadSmall(f)
		if !ok {
			continue
		}
		for _, m := range pstoreHMLine.FindAllStringSubmatch(string(data), -1) {
			if len(out) < 16 && m[2] == m[4] {
				out = append(out, "export "+m[1]+`="`+m[3]+`"`)
			}
		}
	}
	return out
}

// pstoreHarbourScript is the wrapper: $1 is PortMaster's folder, $2 the zip.
// It sets up what PortMaster's launcher sets up before its menu, then runs
// harbourmaster (directly, or through python3 when the file cannot be
// executed as it is).
func pstoreHarbourScript(exports []string) string {
	var b strings.Builder
	// $1 and $2 are kept first: control.txt is a script and may reset them.
	b.WriteString("JH_PM_DIR=\"$1\"\nJH_PM_ZIP=\"$2\"\n")
	b.WriteString("cd \"$JH_PM_DIR\" || exit 90\n")
	b.WriteString("controlfolder=\"$JH_PM_DIR\"\nexport controlfolder\n")
	b.WriteString("[ -f ./control.txt ] && . ./control.txt >/dev/null 2>&1\n")
	b.WriteString("cd \"$JH_PM_DIR\" || exit 90\n")
	for _, e := range exports {
		b.WriteString(e + "\n")
	}
	b.WriteString("echo \"HM_TOOLS_DIR=$HM_TOOLS_DIR HM_PORTS_DIR=$HM_PORTS_DIR HM_SCRIPTS_DIR=$HM_SCRIPTS_DIR\"\n")
	b.WriteString("./harbourmaster --no-check install \"$JH_PM_ZIP\"\nrc=$?\n")
	b.WriteString("if [ $rc -eq 126 ] || [ $rc -eq 127 ]; then python3 ./harbourmaster --no-check install \"$JH_PM_ZIP\"; rc=$?; fi\n")
	b.WriteString("exit $rc\n")
	return b.String()
}

// pstoreHarbourInstall runs PortMaster's installer on zipPath without its
// menu, in env (the firmware environment the job snapshotted). ok means it
// exited 0 and the port's launcher is in a ports folder as this run left it;
// err says why not (pstoreErrCancelled when the strip's X stopped it).
func pstoreHarbourInstall(zipPath string, scripts, env []string, gen uint64, card context.Context) (bool, error) {
	if runtime.GOOS != "linux" {
		return false, errors.New("not on the handheld")
	}
	if len(env) == 0 {
		return false, errors.New("no environment was recorded for the job")
	}
	control := portRealControlFolder()
	if control == "" || !regularFile(filepath.Join(control, "harbourmaster")) {
		return false, errors.New("PortMaster's folder has no harbourmaster")
	}
	abs, err := filepath.Abs(zipPath)
	if err != nil {
		return false, err
	}
	env, _, _, _ = portEnsureControl(append([]string(nil), env...))
	exports := pstoreHMExports(control)
	// harbourmaster may delete the zip; its md5 is taken first.
	sum, _ := mcFileMD5(abs)
	hctx, hcancel := mcRunContext(gen, card)
	defer hcancel()
	ctx, cancel := context.WithTimeout(hctx, pstoreHarbourTimeout)
	defer cancel()
	cmd := hiddenCommandContext(ctx, portShell(env), "-c", pstoreHarbourScript(exports), "jukahub-ports-store", control, abs)
	cmd.Dir = control
	cmd.Env = env
	processGroupConfigure(cmd)
	cmd.Cancel = func() error { return screenChildKill(cmd) }
	cmd.WaitDelay = 5 * time.Second
	logf := pstoreOpenInstallLog(abs, exports)
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	start := time.Now()
	runErr := cmd.Run()
	if logf != nil {
		fmt.Fprintf(logf, "# exit: %v after %s\n\n", runErr, time.Since(start).Round(100*time.Millisecond))
	}
	switch {
	case hctx.Err() != nil:
		return false, pstoreErrCancelled
	case ctx.Err() != nil:
		return false, errors.New("harbourmaster took longer than " + pstoreHarbourTimeout.String())
	case runErr != nil:
		return false, fmt.Errorf("harbourmaster: %w", runErr)
	case len(scripts) > 0 && !pstoreInstalledFrom(scripts, sum, start):
		return false, errors.New("harbourmaster finished but left no new launcher in a ports folder")
	}
	return true, nil
}

// pstoreOpenInstallLog opens data/gamediag/ports/store-install.log for
// appending (started again past pstoreInstallLogMax) and writes the header.
func pstoreOpenInstallLog(zipPath string, exports []string) *os.File {
	dir := gameDiagLogDir()
	if dir == "" {
		return nil
	}
	dir = filepath.Join(dir, portLogDirName)
	if os.MkdirAll(dir, 0o755) != nil {
		return nil
	}
	p := filepath.Join(dir, "store-install.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(p); err == nil && info.Size() > pstoreInstallLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(p, flags, 0o644)
	if err != nil {
		return nil
	}
	fmt.Fprintf(f, "# JukaHub Ports Store install %s\n# zip: %s\n", time.Now().Format(time.RFC3339), zipPath)
	for _, e := range exports {
		fmt.Fprintf(f, "# from PortMaster's launcher: %s\n", e)
	}
	return f
}
