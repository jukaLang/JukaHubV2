package main

// emuautoupdate_handheld.go — the emulators JukaHub sets up on the handheld
// itself, kept current by the daily Emulator updates pass.
//
// Juka, 2026-11-26: "Make sure all emulators get auto updated....make sure all
// emulators listed can run on Trimui Smart pro". The pass already brought the
// libretro cores forward, and JukaHub's PPSSPP, a RetroArch JukaHub installed,
// hakuX and PortMaster (each registered by its own file). Four emulators
// JukaHub sets up on the handheld moved only on a press: Dolphin for GameCube
// and Wii and ARMSX2 for PlayStation 2 — the cores' pass leaves both to their
// own setups (dolphinManagesCore, ps2ManagesCore), since the buildbot's builds
// of them do not load here as they are — the Godot port, and FreeJ2ME-Plus,
// the J2ME core and jar pair. Each now registers an emuRuntimeUpdater that runs
// the install its press runs, under the same Emulator updates setting and at
// the same quiet moment as everything else:
//
//   - Dolphin: JukaHub's own build for TrimUI (the cores-aarch64 release)
//     whenever a new one is published there; where the build in use is the
//     libretro buildbot's nightly, the next nightly a week after it at the
//     earliest — it is among the buildbot's largest downloads, with a new
//     build nearly every day. dolphinSetup with dolphinGetNewest installs it,
//     so a build that cannot load here never replaces the one there, and
//     JukaHub's own build is only ever replaced by a newer one of its own.
//   - ARMSX2: the release ps2PickRelease picks — the newest stable one with a
//     Linux arm64 libretro core, else the newest nightly — with whatever else
//     that release needs beside the core (ps2Setup with update: its resources
//     folder, for a build that does not carry it). A stable release is taken
//     as soon as it is out, a nightly a week after the release in use at the
//     earliest (each one is a download of tens of megabytes at least).
//   - The Godot port: the standalone port's repository, when its newest commit
//     is newer than the install; godot-port.json records the commit an install
//     came from (Commit; an install from before carries none, and its time
//     against the commit's says whether it already has it).
//   - FreeJ2ME-Plus: a newer core-and-jar pair in JukaHub's cores-aarch64
//     release (j2meInstallPair), into RetroArch's own system folder, where the
//     pair's jar is now.
//
// Only what JukaHub set up and nobody changed since is replaced: a Dolphin or
// ARMSX2 core copied in by hand, a Godot port put on the card some other way, a
// FreeJ2ME pair from elsewhere keep their own way of updating (a press on their
// tile still updates them). The Emulators page's Update (L1) runs the same four
// after the cores, hakuX and PPSSPP, without the week's wait
// (emuHandheldUpdateRun).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// emuHandheldNightlyGap is the least time between two nightly builds the
	// pass installs by itself (Dolphin's buildbot core, ARMSX2's nightlies).
	emuHandheldNightlyGap = 7 * 24 * time.Hour
	// godotCommitsURL lists a repository's newest commit on its default
	// branch (owner, repository).
	godotCommitsURL = "https://api.github.com/repos/%s/%s/commits?per_page=1"
)

// emuHandheldKeys are the updaters this file registers, in the order the
// Emulators page's Update runs them.
var emuHandheldKeys = []string{"dolphin", "armsx2", "godot", "j2me"}

// emuHandheldKey reports one of them (emuAutoUpdateCovers names them apart
// from the runtimes).
func emuHandheldKey(key string) bool {
	for _, k := range emuHandheldKeys {
		if k == key {
			return true
		}
	}
	return false
}

func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "dolphin",
		Title:     "Dolphin",
		Supported: dolphinAutoSupported,
		Installed: func() bool { return emuHandheldQuietFirst("dolphin") && dolphinAutoInstalled() },
		InUse:     dolphinAutoInUse,
		Check:     dolphinAutoCheck,
		Install:   dolphinAutoInstall,
	})
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "armsx2",
		Title:     "ARMSX2",
		Supported: ps2FetchSupported,
		Installed: func() bool { return emuHandheldQuietFirst("armsx2") && ps2AutoInstalled() },
		InUse:     ps2AutoInUse,
		Check:     ps2AutoCheck,
		Install:   ps2AutoInstall,
	})
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "godot",
		Title:     "the Godot port",
		Supported: godotOffered,
		Installed: func() bool { return emuHandheldQuietFirst("godot") && godotAutoInstalled() },
		InUse:     gameScreenSuspended,
		Check:     godotAutoCheck,
		Install:   godotAutoInstall,
	})
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "j2me",
		Title:     "FreeJ2ME-Plus",
		Supported: coreFixSupported,
		Installed: func() bool { return emuHandheldQuietFirst("j2me") && j2meAutoInstalled() },
		InUse:     gameScreenSuspended,
		Check:     j2meAutoCheck,
		Install:   j2meAutoInstall,
	})
}

// emuHandheldQuietFirst keeps an updater the pass has never had an answer from
// out of it until the device charges or the user has stepped away: the pass
// counts such an updater as overdue (emuRuntimesOverdue), which on the first
// start of this build would otherwise begin a download of Dolphin or ARMSX2 a
// poll later, on battery and in use — the rule hakuX's first install keeps
// (hakuxUpdaterInstalled). After one answered check the usual moment applies.
func emuHandheldQuietFirst(key string) bool {
	if strings.TrimSpace(emuRuntimeRecordFor(key).CheckedAt) != "" {
		return true
	}
	return getBatteryCharging() || userInputAge(time.Now()) >= coreAutoUpdateIdle
}

// emuHandheldOwnInstalled is an updater's Installed without the first pass's
// wait: what a press asks.
func emuHandheldOwnInstalled(key string) bool {
	switch key {
	case "dolphin":
		return dolphinAutoInstalled()
	case "armsx2":
		return ps2AutoInstalled()
	case "godot":
		return godotAutoInstalled()
	case "j2me":
		return j2meAutoInstalled()
	}
	return false
}

// ── Dolphin (GameCube and Wii) ───────────────────────────────────────────────

// dolphinAutoSupported is the handheld whose Dolphin core JukaHub's setup
// picks (dolphinManagesCore), while GameCube & Wii play here.
func dolphinAutoSupported() bool {
	return dolphinManagesCore(dolphinCoreName) && dolphinPlaysHere()
}

// dolphinAutoOurs is the Dolphin core games start in and the setup's record;
// ours when the core is the file the setup last installed or found current
// (dolphin.json: the same path, size and time) — never the firmware's own
// copy, nor a file put there since by hand or by RetroArch's updater.
func dolphinAutoOurs() (core string, rec dolphinRecord, ours bool) {
	core = dolphinFindCore()
	if core == "" || coreDirIsSystem(filepath.Dir(core)) {
		return "", dolphinRecord{}, false
	}
	info, err := os.Stat(core)
	if err != nil || !info.Mode().IsRegular() {
		return "", dolphinRecord{}, false
	}
	rec = dolphinRecordGet()
	ours = rec.CorePath == filepath.Clean(core) && rec.CoreSize == info.Size() && rec.CoreTime == info.ModTime().Unix()
	return core, rec, ours
}

// dolphinAutoInstalled is the updater's Installed: a few stats and the record
// the setup keeps in memory.
func dolphinAutoInstalled() bool {
	_, _, ours := dolphinAutoOurs()
	return ours
}

// dolphinAutoInUse holds the update while a game is on screen or a setup runs.
func dolphinAutoInUse() bool {
	return gameScreenSuspended() || dolphinBusy.Load()
}

// dolphinAutoCheck asks the sources in the order the setup tries them
// (dolphinSources) for a build other than the one there: the first source
// that lists a Dolphin build this device did not refuse answers. JukaHub's own
// build is only compared with its own source, and a buildbot nightly over a
// buildbot nightly counts only a week after it (emuHandheldTooSoon) unless a
// press asks.
func dolphinAutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	core, rec, ours := dolphinAutoOurs()
	if !ours {
		return false, "", nil
	}
	sum, err := emuCachedCRC32(core)
	if err != nil {
		return false, "", err
	}
	var lastErr error
	for _, src := range dolphinSources() {
		if !src.own && rec.CoreFrom == dolphinSourceOwn {
			// JukaHub's build loads on the firmware's own glibc: only a newer
			// one of its own replaces it (dolphinInstallCore).
			break
		}
		index, err := src.index(ctx, client)
		if errors.Is(err, errCoreFixNoBuild) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return false, "", ctx.Err()
			}
			lastErr = fmt.Errorf("%s: %w", src.name, err)
			continue
		}
		entry, ok := coreFixEntry(index, dolphinCoreName)
		if !ok {
			continue
		}
		if strings.EqualFold(sum, entry.CRC32) {
			return false, "", nil
		}
		if reason, refused := coreBuildRefused(dolphinCoreName, entry.CRC32); refused &&
			!raGlibcRefusalMayPass(reason, core) && !dolphinX11RefusalMayPass(reason, core) {
			// Not fetched again; the source's next build gets its own check.
			continue
		}
		if !src.own && rec.CoreFrom == src.name && !emuHandheldPressed(ctx) && emuHandheldTooSoon(rec.CoreDate, entry.Date) {
			logInfo("emulators", "Dolphin: the buildbot's %s build waits until a week after the %s build in use", entry.Date, rec.CoreDate)
			return false, "", nil
		}
		return true, entry.Date + " (" + src.name + ")", nil
	}
	return false, "", lastErr
}

// dolphinAutoInstall runs the setup a press on a Dolphin tile runs
// (dolphinSetup with dolphinGetNewest) on the pass's card. A newer build that
// could not be installed leaves the one there and is the error; a setup that
// found the build there current after all is errEmuUpdaterCurrent.
func dolphinAutoInstall(ctx context.Context, _ *http.Client, progress func(string)) error {
	if !dolphinBusy.CompareAndSwap(false, true) {
		return errRelPackBusy
	}
	defer dolphinBusy.Store(false)
	core, _, ours := dolphinAutoOurs()
	if !ours {
		return errEmuUpdaterCurrent
	}
	before := dolphinCoreIdentity(core)
	phaseFn, bytesFn := emuHandheldProgress(ctx, progress)
	got, kept, err := dolphinSetup(ctx, "", "", dolphinGetNewest, phaseFn, bytesFn)
	if err != nil {
		return err
	}
	if kept != nil {
		return kept
	}
	if dolphinCoreIdentity(got) == before {
		return errEmuUpdaterCurrent
	}
	logInfo("emulators", "Dolphin: the Emulator updates pass installed %s", got)
	return nil
}

// ── ARMSX2 (PlayStation 2) ───────────────────────────────────────────────────

// ps2AutoOurs is the ARMSX2 core games start in and the setup's record; ours
// when the core is the file the setup installed (armsx2.json).
func ps2AutoOurs() (core string, rec ps2Record, ours bool) {
	core = ps2FindCore()
	if core == "" {
		return "", ps2Record{}, false
	}
	rec = ps2RecordGet()
	return core, rec, ps2CoreIsOurs(core, rec)
}

// ps2AutoInstalled is the updater's Installed: a few stats and the record the
// setup keeps in memory.
func ps2AutoInstalled() bool {
	_, _, ours := ps2AutoOurs()
	return ours
}

// ps2AutoInUse holds the update while a game is on screen or a setup runs.
func ps2AutoInUse() bool {
	return gameScreenSuspended() || ps2Busy.Load()
}

// ps2Nightly reports a release ps2PickRelease counts as a nightly.
func ps2Nightly(r ps2Release) bool {
	return r.Prerelease || strings.Contains(strings.ToLower(r.Tag), "nightly")
}

// ps2AutoCheck asks ARMSX2's release list for the release a setup would
// install now: newer when it is not the one the core came from. A nightly
// counts only a week after the release in use, while GitHub still lists that
// one, unless a press asks.
func ps2AutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	_, rec, ours := ps2AutoOurs()
	if !ours {
		return false, "", nil
	}
	list, err := ps2Releases(ctx, client)
	if err != nil {
		return false, "", err
	}
	pick, ok := ps2PickRelease(list, rec.Refused)
	if !ok || pick.Release.Tag == rec.Release {
		return false, "", nil
	}
	label := pick.Release.Tag
	if ps2Nightly(pick.Release) {
		if have, listed := ps2PickTag(list, rec.Release); listed && !emuHandheldPressed(ctx) &&
			pick.Release.Published.Sub(have.Release.Published) < emuHandheldNightlyGap {
			logInfo("emulators", "ARMSX2: %s waits until a week after %s, the build in use", pick.Release.Tag, rec.Release)
			return false, "", nil
		}
		label += " (" + pick.ps2Date() + ")"
	}
	return true, label, nil
}

// ps2AutoInstall runs the setup a press on the PlayStation 2 tile runs
// (ps2Setup with update: the newest release's core, and its resources folder
// where that build needs one) on the pass's card. A setup that left the
// release in use is errEmuUpdaterCurrent.
func ps2AutoInstall(ctx context.Context, _ *http.Client, progress func(string)) error {
	if !ps2Busy.CompareAndSwap(false, true) {
		return errRelPackBusy
	}
	defer ps2Busy.Store(false)
	_, rec, ours := ps2AutoOurs()
	if !ours {
		return errEmuUpdaterCurrent
	}
	phaseFn, bytesFn := emuHandheldProgress(ctx, progress)
	if _, err := ps2Setup(ctx, "", "", true, true, phaseFn, bytesFn); err != nil {
		return err
	}
	after := ps2RecordGet().Release
	if after == rec.Release {
		return errEmuUpdaterCurrent
	}
	logInfo("emulators", "ARMSX2: the Emulator updates pass installed %s (was %s)", after, rec.Release)
	return nil
}

// ── The Godot port ───────────────────────────────────────────────────────────

// godotCommit is a repository's newest commit on its default branch.
type godotCommit struct {
	SHA  string
	When time.Time
}

// label names the commit for the pass: its short hash and its day.
func (c godotCommit) label() string {
	label := c.SHA
	if len(label) > 7 {
		label = label[:7]
	}
	if !c.When.IsZero() {
		label += " (" + c.When.UTC().Format("2006-01-02") + ")"
	}
	return label
}

// godotAutoSeen is the commit the last check found, which the install that
// follows records.
var godotAutoSeen struct {
	mu sync.Mutex
	c  godotCommit
}

// godotAutoMemo keeps godot-port.json between the pass's looks by its size and
// time: the record lists a hash per file of the port.
var godotAutoMemo struct {
	mu    sync.Mutex
	stamp string
	m     godotMarker
	ok    bool
}

// godotAutoMarker is godot-port.json, read again only when it changed.
func godotAutoMarker() (godotMarker, bool) {
	p := godotMarkerPath()
	if p == "" {
		return godotMarker{}, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return godotMarker{}, false
	}
	stamp := fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano())
	godotAutoMemo.mu.Lock()
	defer godotAutoMemo.mu.Unlock()
	if godotAutoMemo.stamp != stamp {
		godotAutoMemo.m, godotAutoMemo.ok = godotReadMarker()
		godotAutoMemo.stamp = stamp
	}
	return godotAutoMemo.m, godotAutoMemo.ok
}

// godotAutoEmus is the Emus folder the Godot tile installs into.
func godotAutoEmus() string {
	return filepath.Join(filepath.Dir(deviceRomsRoot), "Emus")
}

// godotAutoOurs is the record of a port JukaHub installed that still has its
// launcher on the card.
func godotAutoOurs() (godotMarker, bool) {
	m, ok := godotAutoMarker()
	if !ok || godotLauncherIn(godotPortFolder([]string{godotAutoEmus()})) == "" {
		return godotMarker{}, false
	}
	return m, true
}

// godotAutoInstalled is the updater's Installed.
func godotAutoInstalled() bool {
	_, ok := godotAutoOurs()
	return ok
}

// godotIsSHA reports a full commit hash.
func godotIsSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// godotNewestCommit asks GitHub for src's newest commit. A repository that is
// gone answers found false and no error: there is nothing to update from.
func godotNewestCommit(ctx context.Context, client *http.Client, src godotSource) (godotCommit, bool, error) {
	data, err := fetchUpdateBytes(ctx, client, fmt.Sprintf(godotCommitsURL, src.Owner, src.Repo), patchMaxMetadata)
	if err != nil {
		if godotTryNextSource(err) {
			return godotCommit{}, false, nil
		}
		return godotCommit{}, false, fmt.Errorf("%s's commits on GitHub: %w", src.name(), err)
	}
	var list []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return godotCommit{}, false, fmt.Errorf("%s's commits on GitHub: %w", src.name(), err)
	}
	if len(list) == 0 || !godotIsSHA(list[0].SHA) {
		return godotCommit{}, false, nil
	}
	when, _ := time.Parse(time.RFC3339, strings.TrimSpace(list[0].Commit.Committer.Date))
	return godotCommit{SHA: strings.ToLower(list[0].SHA), When: when}, true, nil
}

// godotAutoCheck compares the standalone port's newest commit with the one the
// install recorded. An install that recorded none (a press before 2026-11-26)
// already has a commit made before it was installed, by a clock that was set:
// the commit is recorded and nothing is downloaded. A port from the Kiwi
// collection (the standalone repository was out of reach then) is replaced by
// the standalone port at its next commit; the commit recorded on such a port
// is the standalone one its last install tried, so a standalone commit that
// still could not be used is not downloaded again every day.
func godotAutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	m, ok := godotAutoOurs()
	if !ok {
		return false, "", nil
	}
	src := godotSources[0]
	c, found, err := godotNewestCommit(ctx, client, src)
	if err != nil || !found {
		return false, "", err
	}
	godotAutoSeen.mu.Lock()
	godotAutoSeen.c = c
	godotAutoSeen.mu.Unlock()
	switch {
	case m.Commit == c.SHA:
		return false, "", nil
	case m.Source == src.name() && m.Commit == "" && !c.When.IsZero() && m.Installed.Year() >= 2024 && !c.When.After(m.Installed):
		m.Commit = c.SHA
		godotWriteMarker(m)
		return false, "", nil
	}
	return true, c.label(), nil
}

// godotAutoInstall runs the install the Godot tile's X runs (godotInstall: the
// port laid over Emus/GODOT, the files the player changed set aside) on the
// pass's card, and records the commit the check found.
func godotAutoInstall(ctx context.Context, _ *http.Client, progress func(string)) error {
	if _, ok := godotAutoOurs(); !ok {
		return errEmuUpdaterCurrent
	}
	godotAutoSeen.mu.Lock()
	seen := godotAutoSeen.c
	godotAutoSeen.mu.Unlock()
	emuSetInstalling(godotEntryName)
	defer emuSetInstalling("")
	res, err := godotInstall(ctx, emuWinClient(), godotAutoEmus(), emuAutoWinProgress(progress))
	if err != nil {
		return err
	}
	invalidateEmulatorCache()
	if seen.SHA != "" {
		// The standalone port's commit this install came from — or, when it
		// fell back to the Kiwi collection, the one it tried (godotAutoCheck).
		if m, ok := godotReadMarker(); ok {
			m.Commit = seen.SHA
			godotWriteMarker(m)
		}
	}
	logInfo("godot", "port from %s updated by the Emulator updates pass in %s (engines: %s)", res.Source, res.Folder, godotVersionsText(res.Engines))
	if res.Kept > 0 {
		emuRuntimeLeaveNote(fmt.Sprintf("the Godot port replaced %s you had changed · your copies are in %s",
			pluralizeCount(res.Kept, "file", "files"), res.KeptDir))
	}
	return nil
}

// ── FreeJ2ME-Plus (J2ME) ─────────────────────────────────────────────────────

// j2meAutoOurs is the pair JukaHub installed (j2me/install.json) while the
// J2ME core games start in is its core, unchanged.
func j2meAutoOurs() (j2meInstalled, bool) {
	rec, ok := j2meLoadInstalled()
	if !ok || rec.Core != j2meCoreName {
		return j2meInstalled{}, false
	}
	core, name := j2meFindCore()
	if name != j2meCoreName || !j2meSamePath(core, rec.CorePath) {
		return j2meInstalled{}, false
	}
	if crc, err := emuCachedCRC32(core); err != nil || !strings.EqualFold(crc, rec.CoreCRC) {
		return j2meInstalled{}, false
	}
	return rec, true
}

// j2meAutoInstalled is the updater's Installed (the core's CRC32 is cached by
// size and time).
func j2meAutoInstalled() bool {
	_, ok := j2meAutoOurs()
	return ok
}

// j2meAutoSysDir reports whether an install with no game puts the jar where
// the pair's jar is now: RetroArch's own system folder. One set to "default"
// reads system files from each game's folder, which only a game's start names.
func j2meAutoSysDir(rec j2meInstalled) bool {
	sysDir, _, _ := gameDiagSystemDir(rec.CorePath, "")
	return filepath.IsAbs(sysDir) && j2meSamePath(sysDir, filepath.Dir(rec.JarPath))
}

// j2meAutoCheck reads JukaHub's cores-aarch64 index: newer when its pair is
// not the one installed and its core was not refused here before.
func j2meAutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	rec, ok := j2meAutoOurs()
	if !ok {
		return false, "", nil
	}
	if !j2meAutoSysDir(rec) {
		logDebug("emulators", "J2ME: the pair's jar is not in RetroArch's own system folder; it is not updated by itself")
		return false, "", nil
	}
	data, err := fetchUpdateBytes(ctx, client, coreFixIndexURL, patchMaxMetadata)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return false, "", nil
		}
		return false, "", fmt.Errorf("JukaHub's builds for this device: %w", err)
	}
	entry, jar, err := j2meIndexPair(string(data), j2meCoreName)
	if err != nil {
		// Not published, or the run's jar is not up yet: nothing to install.
		return false, "", nil
	}
	if strings.EqualFold(entry.CRC32, rec.CoreCRC) && jar.SHA256 == rec.JarSHA {
		return false, "", nil
	}
	if _, refused := coreBuildRefused(j2meCoreName, entry.CRC32); refused {
		return false, "", nil
	}
	return true, entry.Date, nil
}

// j2meAutoInstall installs the release's pair (j2meInstallPair) on the pass's
// card. One that left the installed pair as it was is errEmuUpdaterCurrent.
func j2meAutoInstall(ctx context.Context, _ *http.Client, progress func(string)) error {
	rec, ok := j2meAutoOurs()
	if !ok || !j2meAutoSysDir(rec) {
		return errEmuUpdaterCurrent
	}
	phaseFn, bytesFn := emuHandheldProgress(ctx, progress)
	if _, err := j2meInstallPair(ctx, "", func(p string) { phaseFn(p, "FreeJ2ME-Plus core and jar") }, bytesFn); err != nil {
		return err
	}
	after, _ := j2meLoadInstalled()
	if after.Date == rec.Date && strings.EqualFold(after.CoreCRC, rec.CoreCRC) && after.JarSHA == rec.JarSHA {
		return errEmuUpdaterCurrent
	}
	logInfo("emulators", "J2ME: the Emulator updates pass installed JukaHub's %s pair", after.Date)
	return nil
}

// ── Shared ───────────────────────────────────────────────────────────────────

// emuHandheldPressKey marks the context of a press of the Emulators page's
// Update.
type emuHandheldPressKey struct{}

// emuHandheldPressed reports a check a press asked for: it does not wait out
// the week between nightlies.
func emuHandheldPressed(ctx context.Context) bool {
	pressed, _ := ctx.Value(emuHandheldPressKey{}).(bool)
	return pressed
}

// emuHandheldTooSoon reports an offered nightly ("2006-01-02", an index's
// day) less than emuHandheldNightlyGap after the installed one. A day that
// does not parse is never too soon.
func emuHandheldTooSoon(installed, offered string) bool {
	have, err := time.Parse("2006-01-02", strings.TrimSpace(installed))
	if err != nil {
		return false
	}
	next, err := time.Parse("2006-01-02", strings.TrimSpace(offered))
	if err != nil {
		return false
	}
	return next.Sub(have) < emuHandheldNightlyGap
}

// emuHandheldProgress turns a setup's phase and byte reports into the pass's
// card (emuRuntimeCardFrom) and one line per detail for the pass, or — from a
// press, whose context carries no card — into the install bar the press holds.
func emuHandheldProgress(ctx context.Context, progress func(string)) (func(phase, detail string), func(done, total int64)) {
	card := emuRuntimeCardFrom(ctx)
	lastPhase, lastLine := "", ""
	phaseFn := func(phase, detail string) {
		if phase != "" && phase != lastPhase {
			lastPhase = phase
			if card != nil {
				card.restart(phase)
			} else {
				emuInstallBarReport(phase, 0, 0)
			}
		}
		if line := strings.TrimSpace(detail); line != "" && line != lastLine && progress != nil {
			lastLine = line
			progress(line)
		}
	}
	bytesFn := func(done, total int64) {
		if card == nil {
			emuInstallBarReport("", done, total)
			return
		}
		phase := lastPhase
		if phase == "" {
			phase = installPhaseDownload
		}
		card.bytes(phase, done, total)
	}
	return phaseFn, bytesFn
}

// emuHandheldUpdatersReady is this file's updaters with something of
// JukaHub's to update here now, for the Emulators page's Update: a press does
// not wait for the charger (emuHandheldOwnInstalled, not Installed).
func emuHandheldUpdatersReady() []emuRuntimeUpdater {
	byKey := map[string]emuRuntimeUpdater{}
	for _, u := range emuRuntimeUpdatersSnapshot() {
		byKey[u.Key] = u
	}
	var out []emuRuntimeUpdater
	for _, key := range emuHandheldKeys {
		u, ok := byKey[key]
		if !ok || (u.Supported != nil && !u.Supported()) || !emuHandheldOwnInstalled(key) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// emuHandheldUpdateRun is the Emulators page's Update for list
// (startEmulatorUpdateInstalled, which holds the patch slot and the install
// bar): each one with a newer build installs it now, the week between
// nightlies waived. What it did joins report.
func emuHandheldUpdateRun(ctx context.Context, list []emuRuntimeUpdater, report *emuInstallReport) {
	ctx = context.WithValue(ctx, emuHandheldPressKey{}, true)
	client := emuPassClient(emuWinClient())
	for _, u := range list {
		if ctx.Err() != nil {
			return
		}
		if u.busy() {
			continue
		}
		installBarResetProgress()
		installBarDetail(u.Title)
		newer, label, err := u.Check(ctx, client)
		if err != nil {
			if ctx.Err() == nil {
				report.Errors = append(report.Errors, u.Title+": "+emuFriendlyError(err))
			}
			continue
		}
		if !newer {
			report.Current++
			continue
		}
		title := u.Title
		err = u.Install(ctx, client, func(line string) {
			if !strings.Contains(strings.ToLower(line), strings.ToLower(title)) {
				line = title + " · " + line
			}
			installBarDetail(line)
		})
		switch {
		case err == nil:
			report.Updated = append(report.Updated, emuRuntimeBuildName(title, label))
		case errors.Is(err, errEmuUpdaterCurrent):
			report.Current++
		case errors.Is(err, errRelPackBusy), ctx.Err() != nil:
		default:
			report.Errors = append(report.Errors, title+": "+emuFriendlyError(err))
		}
	}
}
