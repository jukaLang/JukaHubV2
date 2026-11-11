package main

// emuautoupdate_hakux.go — hakuX on the Smart Pro, kept current by the daily
// Emulator updates pass.
//
// The Xbox tile installs hakuX on a press (hakuxpage.go) and L1 updates it
// with everything else. This registers the same install with the automatic
// pass (emuautoupdate_runtimes.go), so a hakuX that JukaHub installed follows
// the setting the way the cores do:
//
//   - Supported: the device is offered the install at all (hakuxOffered: the
//     Smart Pro, and the Brick Pro marked untested);
//   - Installed: an Xbox launcher JukaHub installed is on the card — one put
//     there by hand is launched but never replaced;
//   - InUse: hakuX is running. While JukaHub is up it only runs as a game
//     JukaHub started, and the pass never runs over a game anyway; this is the
//     last word before files are replaced;
//   - Check: the newest release against the provenance file, answered from the
//     Emulators page's own background check while that answer is fresh;
//   - Install: hakuxInstall, which checks the download against the published
//     digests, merges file by file, keeps what it replaces for R1 and copies
//     out any file the user had changed (the toast says where).

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       "hakux",
		Title:     hakuxName,
		Supported: hakuxOffered,
		Installed: hakuxManagedOnCard,
		InUse:     hakuxRunning,
		Check:     hakuxAutoCheck,
		Install:   hakuxAutoInstall,
	})
}

// hakuxRunning reports an Xbox game that JukaHub started and that is still
// running from the hakuX launcher folder.
func hakuxRunning() bool {
	if !gameScreenSuspended() {
		return false
	}
	folder, _, _ := hakuxInstalled(deviceEmuRoots)
	return folder != "" && emuScreenChildUnder(folder)
}

// hakuxAutoCheck is the pass's question: is there a newer package than the
// one JukaHub installed? A fresh "no" from the Emulators page's check is taken
// as it is; anything else is asked, so the label names the release the
// install will fetch (the pass's client answers the install's look from this
// one). The answer also badges the Xbox tile.
func hakuxAutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	_, m, managed := hakuxInstalled(deviceEmuRoots)
	if !managed {
		return false, "", nil
	}
	if _, newer, fresh := emuWinCachedAnswer(emuXboxEntryName); fresh && !newer {
		return false, "", nil
	}
	rel, newer, err := hakuxUpdateFor(ctx, client)
	if err != nil || !newer {
		return false, "", err
	}
	emuNoteWinUpdate(emuXboxEntryName, rel)
	return true, emuReleaseLabel(m.Tag, rel), nil
}

// hakuxAutoInstall is the pass's install: the one X and L1 run, with the
// tile's "Installing…" pill while it works.
func hakuxAutoInstall(ctx context.Context, client *http.Client, progress func(string)) error {
	emuSetInstalling(emuXboxEntryName)
	defer emuSetInstalling("")
	result, err := hakuxInstall(ctx, client, hakuxDevicePaths(), hakuxDevice(), emuAutoWinProgress(progress))
	if err != nil {
		return err
	}
	invalidateEmulatorCache()
	emuForgetUpdate(emuXboxEntryName)
	if emuXboxSheetOpen() {
		emuXboxRescan()
	}
	if result.Edited > 0 {
		logInfo("emulators", "hakuX: %d changed file(s) replaced; copies in %s", result.Edited, result.EditedDir)
		emuRuntimeLeaveNote(fmt.Sprintf("hakuX replaced %s you had changed · your copies are in %s",
			pluralizeCount(result.Edited, "file", "files"), result.EditedDir))
	}
	if !result.Updated {
		// Already the newest (or only missing files put back): no new version.
		return errEmuUpdaterCurrent
	}
	if !result.Kept {
		logWarn("emulators", "hakuX %s: the files it replaced could not be kept for R1", result.Tag)
	}
	return nil
}

// ── Shared by the emulator updaters ─────────────────────────────────────────

// emuAutoWinProgress drives the pass's card from an emulator install's
// progress (emuWinProgress): the phase and the bytes go to the bar the way the
// Emulators page's own installs report them, and each new phase is also a
// line for the pass.
func emuAutoWinProgress(progress func(string)) emuWinProgress {
	last := ""
	return func(phase string, done, total int64) {
		emuInstallBarReport(phase, done, total)
		if phase != "" && phase != last {
			last = phase
			if progress != nil {
				progress(phase)
			}
		}
	}
}

// emuLabelVersion matches a tag that carries a version number ("v0.1.5",
// "2.6", "v0.0.43-17000"), as opposed to a rolling name ("continuous",
// "latest") or a commit.
var emuLabelVersion = regexp.MustCompile(`[0-9]+\.[0-9]+`)

// emuReleaseLabel names a release in the toast and in the pass's record: its
// tag, plus the asset's upload time when the tag alone does not tell it from
// the installed build — a rolling tag, a commit, or the same version published
// again. The record needs that: a label the pass installed and finds offered
// again is taken for a rollback and left alone.
func emuReleaseLabel(installedTag string, rel emuWinRelease) string {
	label := strings.TrimSpace(rel.Tag)
	if label == "" {
		label = "new build"
	}
	if strings.EqualFold(label, strings.TrimSpace(installedTag)) || !emuLabelVersion.MatchString(label) {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(rel.AssetDate)); err == nil {
			label += " (" + t.UTC().Format("2006-01-02 15:04") + ")"
		}
	}
	return label
}

// emuScreenChildUnder reports a game or emulator JukaHub started that is still
// running from inside dir: its program, its working folder or one of its
// arguments is there.
func emuScreenChildUnder(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, cmd := range liveScreenChildren() {
		if emuPathWithin(cmd.Path, dir) || emuPathWithin(cmd.Dir, dir) {
			return true
		}
		for _, arg := range cmd.Args {
			if emuPathWithin(arg, dir) {
				return true
			}
		}
	}
	return false
}

// emuPathWithin reports whether the absolute path p is dir or inside it.
// Case is ignored: Windows paths have none, and on the card it only widens the
// match, which errs on the side of not replacing files.
func emuPathWithin(p, dir string) bool {
	p, dir = strings.TrimSpace(p), strings.TrimSpace(dir)
	if p == "" || dir == "" || !filepath.IsAbs(p) {
		return false
	}
	p = strings.ToLower(filepath.Clean(p))
	dir = strings.ToLower(filepath.Clean(dir))
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
