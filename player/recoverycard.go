package main

// recoverycard.go — the recovery card: one modal glass card, shown over Home
// when the launch needs to say something about how the last ones ended.
//
//   - The recovery chord was held (safeboot.go): "Recovery" offers the
//     previous version (the one released before this in Version history, or
//     on older installs the build an update backup kept), the last good
//     settings (what the last healthy start read, then the .bak of the
//     settings files or the newest weekly backup, newest first), staying in
//     safe mode, and resetting the settings while keeping games and saves.
//     The ones that change something ask first; settings are backed up before
//     anything is written (recoverysettings.go).
//   - The boot guard started safe mode after failed starts (bootguard.go): the
//     card says why, what is off and how to leave safe mode.
//   - The last session crashed (crashreport.go): "JukaHub closed
//     unexpectedly", with Send report (Report a bug, prefilled, nothing is sent
//     before Submit) and Restart in safe mode.
//
// The card is a list of option rows in Home's glass (the tool screens' rows and
// sheet chrome), driven by the D-pad or the arrow keys, A/Enter and B/Esc, or a
// tap. Work it starts runs off the render thread and reports back through the
// tool screens' UI queue (tbxPost).

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// recoveryOption is one row of the card.
type recoveryOption struct {
	icon     string
	title    string
	detail   string
	disabled bool
	run      func(config *Config)
}

// recoveryCard is the open card. Main thread only.
var recoveryCard struct {
	open     bool
	gen      int // bumped by every card shown, so late results find out they are stale
	title    string
	subtitle string
	icon     string
	body     string
	options  []recoveryOption
	focus    int
	back     func(config *Config) // B / Esc; nil closes the card
	backText string
	busy     bool
	wrap     tbxWrapCache
	rows     []sdl.Rect
	card     sdl.Rect
	// Built once per card (and per hint vocabulary), not per frame.
	header    UIPageHeader
	headerGen int
	hints     string
	hintsGen  int
	hintsKb   hintStamp
}

// recoveryCardSpec is what recoveryCardShow puts up.
type recoveryCardSpec struct {
	title, subtitle, icon, body string
	options                     []recoveryOption
	back                        func(config *Config)
	backText                    string
	busy                        bool
}

// recoveryCardShow replaces whatever the card shows.
func recoveryCardShow(spec recoveryCardSpec) {
	recoveryCard.open = true
	recoveryCard.gen++
	recoveryCard.title, recoveryCard.subtitle, recoveryCard.icon = spec.title, spec.subtitle, spec.icon
	recoveryCard.body = spec.body
	recoveryCard.options = spec.options
	recoveryCard.back = spec.back
	recoveryCard.backText = spec.backText
	if recoveryCard.backText == "" {
		recoveryCard.backText = "Close"
	}
	recoveryCard.busy = spec.busy
	recoveryCard.focus = 0
	for i, o := range spec.options {
		if !o.disabled {
			recoveryCard.focus = i
			break
		}
	}
}

// recoveryCardClose puts the card away.
func recoveryCardClose() {
	recoveryCard.open = false
	recoveryCard.gen++
	recoveryCard.options = nil
	recoveryCard.back = nil
	recoveryCard.busy = false
}

// recoveryChordName names the recovery chord in this frame's vocabulary.
func recoveryChordName() string {
	if hintsUseKeyboard() {
		return "Ctrl + Alt + R"
	}
	return "L + R + Start"
}

// ── What the card says at boot ─────────────────────────────────────────────

// recoveryCardOpenAtBoot opens the card the launch calls for, if any. main.go
// calls it once, before the loop starts.
func recoveryCardOpenAtBoot(config *Config) {
	report, at := crashLastReport()
	switch {
	case safeboot.Triggered:
		recoveryOpenMenu(config, nil)
	case safeboot.AutoRevert || bootGuardOutcome.SafeMode:
		recoveryOpenSafeCard(config, report)
	case report != "":
		recoveryOpenCrashCard(config, report, at)
	case safeModeActive():
		ShowToast("Safe mode: plugins, updates and network are off. "+safeModeLeaveHint(), ToastKindInfo)
	}
}

// recoveryOpenCrashCard is "JukaHub closed unexpectedly".
func recoveryOpenCrashCard(config *Config, report string, at time.Time) {
	name := filepath.Base(report)
	reopen := func(c *Config) { recoveryOpenCrashCard(c, report, at) }
	safe := recoveryOption{icon: "shield", title: "Restart in safe mode",
		detail: "Plugins, updates and network stay off until the next restart"}
	if recoveryRestartKind() == "" {
		safe.title = "Safe mode next time"
		safe.detail = "JukaHub closes; open it again and it starts in safe mode"
	}
	safe.run = func(c *Config) { recoveryRestartSafe(c, reopen) }
	recoveryCardShow(recoveryCardSpec{
		title:    "JukaHub closed unexpectedly",
		subtitle: "Last session" + recoveryWhen(at),
		icon:     "warning",
		body: "Something went wrong and JukaHub had to stop. A crash report was saved on this device as " + name + ".\n\n" +
			"Sending it helps get it fixed. You see exactly what would be sent first, and nothing leaves the device until you press Submit.",
		options: []recoveryOption{
			{icon: "bug", title: "Send report", detail: "Opens Report a bug with the crash filled in",
				run: func(c *Config) { recoverySendReport(c, report, at) }},
			safe,
			{icon: "restore", title: "Recovery options", detail: "Previous version, last good settings or reset settings",
				run: func(c *Config) { recoveryOpenMenu(c, reopen) }},
			{icon: "check", title: "Continue", detail: "Use JukaHub as usual", run: func(*Config) { recoveryCardClose() }},
		},
	})
}

// recoveryOpenSafeCard says why the boot guard started safe mode.
func recoveryOpenSafeCard(config *Config, report string) {
	_, at := crashLastReport()
	reopen := func(c *Config) { recoveryOpenSafeCard(c, report) }
	subtitle := fmt.Sprintf("JukaHub did not finish starting %d times in a row", bootGuardOutcome.Streak)
	var body strings.Builder
	var options []recoveryOption
	switch {
	case safeboot.AutoRevert && safeboot.Err == nil:
		version := safeboot.Version
		if version == "" {
			version = "the previous version"
		}
		subtitle = "Going back to " + version
		if safeboot.Restarts {
			// Version history: the swap helper restarts JukaHub, normally
			// before this card is ever drawn.
			body.WriteString("This version stopped during its first starts, so the one before it is being put back from Version history. " +
				"JukaHub restarts by itself when it is ready.\n\n")
			break
		}
		// A backup set: the file is in place, nothing restarts on its own.
		subtitle = version + " takes over at the next start"
		body.WriteString("This version stopped during its first starts, so the one saved before it was put back. " +
			"It takes over the next time JukaHub starts.\n\n")
		options = append(options, recoveryRestartNowOption("Start "+version+" now"))
	case safeboot.AutoRevert:
		body.WriteString("This version stopped during its first starts, and the previous one could not be put back: " +
			firstLine(safeboot.Err.Error()) + ".\n\n")
	case bootGuardOutcome.Reverts > 0:
		body.WriteString("An earlier version was already put back after failed starts, and starting still fails, " +
			"so the version was left alone this time: the cause is probably not the update.\n\n")
	}
	body.WriteString("This launch skips plugins, updates and network features, so whatever stopped the last starts cannot run.\n\n")
	body.WriteString(safeModeLeaveHint() + " If it keeps stopping, Recovery options can put back your last good settings or reset them, " +
		"and holding " + recoveryChordName() + " while JukaHub starts opens them too.")
	options = append(options, recoveryOption{icon: "check", title: "Continue in safe mode",
		detail: "Plugins, updates and network stay off until JukaHub restarts", run: func(*Config) { recoveryCardClose() }})
	if report != "" {
		options = append(options, recoveryOption{icon: "bug", title: "Send report", detail: "Opens Report a bug with the last crash filled in",
			run: func(c *Config) { recoverySendReport(c, report, at) }})
	}
	options = append(options, recoveryOption{icon: "restore", title: "Recovery options",
		detail: "Previous version, last good settings or reset settings", run: func(c *Config) { recoveryOpenMenu(c, reopen) }})
	recoveryCardShow(recoveryCardSpec{title: "Safe mode", subtitle: subtitle, icon: "shield", body: body.String(), options: options})
}

// ── Recovery choices ───────────────────────────────────────────────────────

// The recovery menu's rows whose content arrives after the card is up.
const (
	recoveryMenuPrevRow     = 0
	recoveryMenuSettingsRow = 1
)

// recoveryOpenMenu is the recovery chord's card. back is where B goes (nil:
// close, staying in safe mode).
func recoveryOpenMenu(config *Config, back func(*Config)) {
	reopen := func(c *Config) { recoveryOpenMenu(c, back) }
	prev := recoveryOption{icon: "history", title: "Previous version"}
	lookForSet := false
	if entries, _ := uhistEntriesSnapshot(); len(entries) > 0 {
		// The version released before this one, not the newest saved: after a
		// rollback that is the build just left (rollbackPickVersion).
		if e, ok := rollbackPreviousEntry(entries); ok {
			label := uhistLabel(e.Tag, e.Date, e.Version)
			prev.detail = "Go back to " + label + " from Version history · JukaHub restarts"
			prev.run = func(c *Config) { recoveryConfirmPrevious(c, e.ID, label, reopen) }
		} else {
			prev.detail = "No version older than this one is saved on this device"
			prev.disabled = true
		}
	} else {
		// Installs from before Version history kept the replaced build in a
		// Patch backup set; finding it reads the card, below.
		prev.detail = "Looking for an earlier version…"
		prev.disabled = true
		lookForSet = true
	}
	settings := recoveryOption{icon: "backup", title: "Last good settings", detail: "Looking for saved settings…", disabled: true}
	stay := recoveryOption{icon: "shield", title: "Safe mode",
		detail: "Keep going with plugins, updates and network off until JukaHub restarts",
		run:    func(*Config) { recoveryCardClose() }}
	if !safeModeActive() {
		stay.title = "Restart in safe mode"
		stay.detail = "Plugins, updates and network stay off until the next restart"
		stay.run = func(c *Config) { recoveryRestartSafe(c, reopen) }
	}
	reset := recoveryOption{icon: "refresh", title: "Reset settings (keep games and saves)",
		detail: "Shipped settings and home layout; games, saves and play history stay",
		run:    func(c *Config) { recoveryConfirmReset(c, reopen) }}
	subtitle := "Safe mode is on · nothing has been changed"
	if !safeModeActive() {
		subtitle = "Nothing has been changed yet"
	}
	backText := "Stay in safe mode"
	if back != nil {
		backText = "Back"
	} else if !safeModeActive() {
		backText = "Close"
	}
	body := "Choose what to put back. Games, saves and your library are never touched, and settings are backed up before they change."
	offered := bootGuardOutcome.RollbackOffered
	if offered {
		// The boot guard would have gone back by itself; the chord held it.
		subtitle = fmt.Sprintf("This version did not finish its last %d starts", bootGuardOutcome.Streak)
		body = "JukaHub did not go back by itself because " + recoveryChordName() +
			" was held: Previous version puts back the one before this.\n\n" + body
	}
	options := []recoveryOption{prev, settings, stay, reset}
	// A crash report filed at this start is offered here too: with the chord
	// held, the crash card never opens.
	if report, at := crashLastReport(); back == nil && report != "" {
		options = append(options, recoveryOption{icon: "bug", title: "Send report", detail: "Opens Report a bug with the last crash filled in",
			run: func(c *Config) { recoverySendReport(c, report, at) }})
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Recovery",
		subtitle: subtitle,
		icon:     "restore",
		body:     body,
		options:  options,
		back:     back,
		backText: backText,
	})
	// Finding the saved settings (and, without Version history, a backed-up
	// build) reads the card's folders: off the render thread, and the rows
	// fill in when the answer arrives.
	gen := recoveryCard.gen
	appDir := bkupAppDir()
	go func() {
		lastGood, _ := bootGuardLastGoodPath()
		sources := recoveryFindSettingsSources(appDir, bkupExportDir(), lastGood)
		setVersion, setOK := "", false
		if lookForSet {
			_, setVersion, setOK = rollbackBackupSetCandidate()
		}
		tbxPost(func() {
			if !recoveryCard.open || recoveryCard.gen != gen || len(recoveryCard.options) <= recoveryMenuSettingsRow {
				return
			}
			if lookForSet {
				row := &recoveryCard.options[recoveryMenuPrevRow]
				if setOK {
					label := setVersion
					if label == "" {
						label = "the build saved before the last update"
					}
					row.detail = "Put back " + label + " from the update backups · it starts next time"
					row.disabled = false
					row.run = func(c *Config) { recoveryConfirmBackupSet(c, label, reopen) }
				} else {
					row.detail = "No earlier version is saved on this device"
				}
			}
			row := &recoveryCard.options[recoveryMenuSettingsRow]
			if len(sources) == 0 {
				row.detail = "No saved copy of your settings was found"
			} else {
				row.detail = recoverySourceLabel(sources[0])
				if more := len(sources) - 1; more == 1 {
					row.detail += " · one more copy"
				} else if more > 1 {
					row.detail += fmt.Sprintf(" · %d more copies", more)
				}
				row.disabled = false
				row.run = func(c *Config) { recoveryChooseSource(c, sources, reopen) }
			}
			switch {
			case offered && !recoveryCard.options[recoveryMenuPrevRow].disabled:
				recoveryCard.focus = recoveryMenuPrevRow
			case recoveryCard.options[recoveryCard.focus].disabled:
				for i, o := range recoveryCard.options {
					if !o.disabled {
						recoveryCard.focus = i
						break
					}
				}
			}
		})
	}()
}

// recoveryConfirmPrevious asks before the rollback.
func recoveryConfirmPrevious(config *Config, id, label string, back func(*Config)) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Go back to " + label + "?",
		subtitle: "From Version history",
		icon:     "history",
		body: "JukaHub restarts into the saved version. The version you are on now is kept in Version history, " +
			"so this can be undone the same way.\n\nYour settings, games and saves are not touched.",
		options: []recoveryOption{
			{icon: "history", title: "Go back and restart", detail: "Installs " + label + " and restarts JukaHub",
				run: func(*Config) {
					recoveryCardClose()
					ShowToast("Going back to "+label+" — JukaHub restarts by itself", ToastKindInfo)
					RunPatchOp("Rolling back JukaHub", func(ctx context.Context) error {
						return uhistRollbackOp(ctx, appConfig, id)
					})
				}},
			{icon: "close", title: "Cancel", detail: "Nothing changes", run: back},
		},
		back:     back,
		backText: "Cancel",
	})
}

// recoveryChooseSource lists the saved settings when there is more than one
// copy: the last good start's first, then newest first.
func recoveryChooseSource(config *Config, sources []recoverySettingsSource, back func(*Config)) {
	if len(sources) == 1 {
		recoveryConfirmSource(config, sources[0], back)
		return
	}
	reopen := func(c *Config) { recoveryChooseSource(c, sources, back) }
	options := make([]recoveryOption, 0, len(sources))
	for _, s := range sources {
		src := s
		options = append(options, recoveryOption{icon: "backup", title: recoverySourceLabel(src), detail: recoverySourceDetail(src),
			run: func(c *Config) { recoveryConfirmSource(c, src, reopen) }})
	}
	subtitle := "Newest first"
	if sources[0].Kind == recoverySourceLastGood {
		subtitle = "The last good start first, then newest first"
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Last good settings",
		subtitle: subtitle,
		icon:     "backup",
		body:     "Choose the copy to put back.",
		options:  options,
		back:     back,
		backText: "Back",
	})
}

// recoveryConfirmSource asks before putting saved settings back.
func recoveryConfirmSource(config *Config, src recoverySettingsSource, back func(*Config)) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Put back these settings?",
		subtitle: recoverySourceLabel(src),
		icon:     "backup",
		body: "Your current settings and layout are saved first as a safety backup (Tools → Your setup → Backup & restore), " +
			"then the saved ones are put back.\n\nGames, saves, favourites and play history are not touched.",
		options: []recoveryOption{
			{icon: "backup", title: "Put back", detail: "Backs up the current settings, then restores these",
				run: func(c *Config) {
					appDir, version := bkupAppDir(), versionString(c)
					recoveryRunSettings(c, "Putting back your settings…", false, func(ctx context.Context) bkupRestoreResult {
						return recoveryRestoreSource(ctx, src, appDir, version)
					})
				}},
			{icon: "close", title: "Cancel", detail: "Nothing changes", run: back},
		},
		back:     back,
		backText: "Cancel",
	})
}

// recoveryConfirmReset asks before the reset.
func recoveryConfirmReset(config *Config, back func(*Config)) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Restore all defaults?",
		subtitle: "Games and saves are kept",
		icon:     "refresh",
		body: "Settings and the home layout go back to how JukaHub ships. Games, saves, favourites, play history, " +
			"themes and plugins stay.\n\nYour current settings are saved first as a safety backup " +
			"(Tools → Your setup → Backup & restore), so this can be undone.",
		options: []recoveryOption{
			{icon: "close", title: "Cancel", detail: "Nothing changes", run: back},
			{icon: "refresh", title: "Restore all defaults", detail: "Backs up the current settings, then resets them",
				run: func(c *Config) {
					appDir, version := bkupAppDir(), versionString(c)
					recoveryRunSettings(c, "Resetting your settings…", true, func(ctx context.Context) bkupRestoreResult {
						return recoveryResetSettings(ctx, appDir, version)
					})
				}},
		},
		back:     back,
		backText: "Cancel",
	})
}

// recoveryRunSettings runs a settings change off the render thread and installs
// the result on it.
func recoveryRunSettings(config *Config, working string, reset bool, work func(ctx context.Context) bkupRestoreResult) {
	// A debounced settings save still waiting is written first, here on the
	// main thread that owns the settings: the safety backup then holds the
	// settings as they are now, and the timer cannot land the old ones on top
	// of the restored files afterwards.
	flushPendingUserConfig()
	recoveryCardShow(recoveryCardSpec{
		title:    "Working",
		subtitle: working,
		icon:     "backup",
		body:     working + "\n\nThis takes a few seconds. JukaHub keeps a safety backup of the settings it replaces.",
		busy:     true,
		backText: "Hide",
	})
	gen := recoveryCard.gen
	go func() {
		res := work(context.Background())
		tbxPost(func() { recoveryFinishSettings(config, res, reset, gen) })
	}()
}

// recoveryFinishSettings installs a finished settings change (main thread).
func recoveryFinishSettings(config *Config, res bkupRestoreResult, reset bool, gen int) {
	if res.Err == nil && reset {
		if res.Next == nil {
			res.Err = fmt.Errorf("the shipped settings could not be read")
		} else if data, err := recoveryResetUser(res.Next); err != nil {
			res.Err = err
		} else {
			res.UserData, res.HasUser = data, true
		}
	}
	if res.Err == nil {
		res.Err = bkupCommit(config, res)
	}
	shown := recoveryCard.open && recoveryCard.gen == gen
	if res.Err != nil {
		logWarn("recovery", "settings change stopped: %v", res.Err)
		msg := firstLine(res.Err.Error())
		if res.SafetyPath != "" {
			msg += ". Your settings before this are in " + filepath.Base(res.SafetyPath) + "."
		}
		if !shown {
			ShowToast("Recovery stopped: "+msg, ToastKindWarning)
			return
		}
		recoveryCardShow(recoveryCardSpec{
			title: "Recovery stopped", subtitle: "Nothing more was changed", icon: "warning",
			body:    msg,
			options: []recoveryOption{{icon: "check", title: "OK", detail: "Back to JukaHub", run: func(*Config) { recoveryCardClose() }}},
		})
		PlayErrorSound()
		return
	}
	if shown {
		recoveryCardClose()
	}
	msg := "Settings put back · safety backup saved"
	if reset {
		msg = "Settings reset · safety backup saved"
	}
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf(" · %d left out", len(res.Skipped))
		LogScene("recovery").Info("settings change left files out", "files", strings.Join(res.Skipped, "; "))
	}
	PlaySuccessSound()
	ShowToast(msg, ToastKindSuccess)
}

// recoveryRestartSafe schedules safe mode for the next start and restarts, or
// says that JukaHub has to be opened again where it cannot restart itself.
func recoveryRestartSafe(config *Config, back func(*Config)) {
	if !bootGuardRequestSafeNext("chosen on the recovery card") {
		recoveryCardShow(recoveryCardSpec{
			title: "Safe mode could not be set up", subtitle: "The boot record is not available", icon: "warning",
			body: "Hold " + recoveryChordName() + " while JukaHub starts to open it in safe mode, or create a file named " +
				safeModeFlagPath + " next to JukaHub.",
			options: []recoveryOption{{icon: "check", title: "OK", detail: "Back", run: back}},
			back:    back,
		})
		return
	}
	if recoveryRestartKind() != "" {
		recoveryCardClose()
		recoveryRestartApp()
		return
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Close JukaHub now?",
		subtitle: "Safe mode starts with the next launch",
		icon:     "shield",
		body:     "JukaHub cannot start itself again here. Close it, then open JukaHub again: it starts in safe mode.",
		options: []recoveryOption{
			{icon: "power", title: "Close JukaHub", detail: "Open it again to continue in safe mode",
				run: func(*Config) {
					recoveryCardClose()
					recoveryRestartApp()
				}},
			{icon: "close", title: "Later", detail: "Safe mode still starts with the next launch",
				run: func(*Config) { recoveryCardClose() }},
		},
	})
}

// recoveryRestartNowOption is the row that starts JukaHub again so a restored
// build takes over, or closes it where it cannot start itself again.
func recoveryRestartNowOption(detail string) recoveryOption {
	o := recoveryOption{icon: "refresh", title: "Restart now", detail: detail}
	if recoveryRestartKind() == "" {
		o.icon, o.title, o.detail = "power", "Close JukaHub", "Open it again to start the version that was put back"
	}
	o.run = func(*Config) {
		recoveryCardClose()
		recoveryRestartApp()
	}
	return o
}

// recoveryConfirmBackupSet asks before putting back the build an older update
// kept in a Patch backup set (installs whose Version history is empty).
func recoveryConfirmBackupSet(config *Config, label string, back func(*Config)) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Go back to " + label + "?",
		subtitle: "From the update backups",
		icon:     "history",
		body: "The JukaHub that an earlier update replaced is put back in place of this one. It takes over the next time " +
			"JukaHub starts.\n\nYour settings, games and saves are not touched.",
		options: []recoveryOption{
			{icon: "history", title: "Put it back", detail: "Restores the saved JukaHub; it starts next time",
				run: func(c *Config) { recoveryRunBackupSet(c, label) }},
			{icon: "close", title: "Cancel", detail: "Nothing changes", run: back},
		},
		back:     back,
		backText: "Cancel",
	})
}

// recoveryRunBackupSet copies that build back off the render thread.
func recoveryRunBackupSet(config *Config, label string) {
	recoveryCardShow(recoveryCardSpec{
		title:    "Working",
		subtitle: "Putting back " + label + "…",
		icon:     "history",
		body:     "Copying the saved JukaHub into place. This takes a few seconds.",
		busy:     true,
		backText: "Hide",
	})
	gen := recoveryCard.gen
	go func() {
		err := rollbackBackupSetNow()
		tbxPost(func() { recoveryFinishBackupSet(label, err, gen) })
	}()
}

// recoveryFinishBackupSet says how it went and offers the restart (main
// thread).
func recoveryFinishBackupSet(label string, err error, gen int) {
	shown := recoveryCard.open && recoveryCard.gen == gen
	if err != nil {
		logWarn("recovery", "putting back the backed-up build failed: %v", err)
		if !shown {
			ShowToast("Going back stopped: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		recoveryCardShow(recoveryCardSpec{
			title: "The earlier version was not put back", subtitle: "Nothing was changed", icon: "warning",
			body:    firstLine(err.Error()) + ".",
			options: []recoveryOption{{icon: "check", title: "OK", detail: "Back to JukaHub", run: func(*Config) { recoveryCardClose() }}},
		})
		PlayErrorSound()
		return
	}
	PlaySuccessSound()
	if !shown {
		ShowToast("JukaHub "+label+" takes over at the next start", ToastKindSuccess)
		return
	}
	recoveryCardShow(recoveryCardSpec{
		title:    "Put back",
		subtitle: label + " takes over at the next start",
		icon:     "history",
		body:     "The saved JukaHub is in place. It starts the next time JukaHub opens; this session keeps running until then.",
		options: []recoveryOption{
			recoveryRestartNowOption("Start " + label + " now"),
			{icon: "close", title: "Later", detail: "It starts with the next launch", run: func(*Config) { recoveryCardClose() }},
		},
	})
}

// recoverySendReport opens Report a bug with the crash filled in. Reading the
// report happens off the render thread; nothing is sent from here.
func recoverySendReport(config *Config, report string, at time.Time) {
	recoveryCardClose()
	go func() {
		summary := ""
		if data, err := crashReadHead(report, crashReadMax); err == nil {
			summary = crashSummary(string(data))
		}
		tbxPost(func() {
			var desc strings.Builder
			desc.WriteString("JukaHub closed unexpectedly")
			if when := recoveryWhen(at); when != "" {
				desc.WriteString(" (" + strings.TrimPrefix(when, " · ") + ")")
			}
			desc.WriteString(". Crash report " + filepath.Base(report))
			if summary != "" {
				desc.WriteString(":\n\n" + summary)
			} else {
				desc.WriteString(".")
			}
			bugrepScreen.form = bugrepForm{
				Category:    0, // "Crash"
				Title:       "JukaHub closed unexpectedly",
				Description: desc.String(),
				Diagnostics: true,
				Log:         true,
			}
			bugrepScreen.touched = true
			bugrepOpenScreen(config)
		})
	}()
}

// ── Input ─────────────────────────────────────────────────────────────────

// recoveryCardAction applies one action to the open card.
func recoveryCardAction(config *Config, act tbxAction) {
	if recoveryCard.busy {
		if act == tbxActBack {
			// The work goes on; its result arrives as a toast.
			recoveryCardClose()
			PlayBackSound()
		}
		return
	}
	n := len(recoveryCard.options)
	switch act {
	case tbxActUp, tbxActLeft:
		if recoveryCard.focus > 0 {
			recoveryCard.focus--
			PlayNavSound()
		}
	case tbxActDown, tbxActRight:
		if recoveryCard.focus < n-1 {
			recoveryCard.focus++
			PlayNavSound()
		}
	case tbxActConfirm:
		recoveryCardActivate(config, recoveryCard.focus)
	case tbxActBack:
		PlayBackSound()
		if back := recoveryCard.back; back != nil {
			back(config)
			return
		}
		recoveryCardClose()
	}
}

// recoveryCardActivate presses option i.
func recoveryCardActivate(config *Config, i int) {
	if i < 0 || i >= len(recoveryCard.options) {
		return
	}
	o := recoveryCard.options[i]
	if o.disabled || o.run == nil {
		PlayErrorSound()
		return
	}
	PlayActivateSound()
	o.run(config)
}

// recoveryCardHandleKey routes a key while the card is up; it owns them all.
func recoveryCardHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !recoveryCard.open || e == nil {
		return false
	}
	if e.Type == sdl.KEYDOWN {
		recoveryCardAction(config, tbxKeyAction(e))
	}
	return true
}

// recoveryCardHandleController routes a pad press while the card is up.
func recoveryCardHandleController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !recoveryCard.open || e == nil {
		return false
	}
	recoveryCardAction(config, tbxPadAction(e))
	return true
}

// recoveryCardHandleClick presses the row under a tap; a tap elsewhere is
// swallowed, so nothing behind the card is pressed.
func recoveryCardHandleClick(mx, my int32, config *Config) bool {
	if !recoveryCard.open {
		return false
	}
	if recoveryCard.busy {
		return true
	}
	for i, r := range recoveryCard.rows {
		if i < len(recoveryCard.options) && r.W > 0 && pointInRect(mx, my, r) {
			recoveryCard.focus = i
			recoveryCardActivate(config, i)
			return true
		}
	}
	return true
}

// ── Drawing ───────────────────────────────────────────────────────────────

// renderRecoveryCard draws the card over everything but the alarm and the
// toasts.
func renderRecoveryCard(renderer *sdl.Renderer, config *Config) {
	if !recoveryCard.open || renderer == nil {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	cardW := int32(820)
	if cardW > screenWidth-48 {
		cardW = screenWidth - 48
	}
	if recoveryCard.headerGen != recoveryCard.gen {
		recoveryCard.header = UIPanelHeader(recoveryCard.title, recoveryCard.subtitle, recoveryCard.icon)
		recoveryCard.headerGen = recoveryCard.gen
	}
	header := recoveryCard.header
	headerH := UIPanelHeaderBand(config, header, 8)
	textW := cardW - 2*OverlayPadX
	lines := recoveryCard.wrap.get(fonts.body, recoveryCard.body, textW-20)
	lineH := fonts.bodyH + 4
	rowH := tbxRowHeight(fonts)
	const rowGap = int32(8)
	n := int32(len(recoveryCard.options))
	rowsH := n * (rowH + rowGap)
	footerH := fonts.smallH + 24
	maxH := screenHeight - 32
	// The rows always fit; the explanation gives way first on a short canvas.
	room := maxH - headerH - rowsH - footerH - 24
	maxLines := int(room / lineH)
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	bodyH := int32(len(lines)) * lineH
	cardH := headerH + bodyH + 16 + rowsH + footerH + 8
	if cardH > maxH {
		cardH = maxH
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	recoveryCard.card = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	x := card.X + OverlayPadX
	y := card.Y + headerH
	for _, line := range lines {
		tbxDrawSheetLine(renderer, config, fonts, line, x, y, textW)
		y += lineH
	}
	y += 16
	if cap(recoveryCard.rows) < len(recoveryCard.options) {
		recoveryCard.rows = make([]sdl.Rect, len(recoveryCard.options))
	}
	recoveryCard.rows = recoveryCard.rows[:len(recoveryCard.options)]
	for i, o := range recoveryCard.options {
		r := sdl.Rect{X: x, Y: y, W: textW, H: rowH}
		if r.Y+r.H > card.Y+card.H-footerH {
			recoveryCard.rows[i] = sdl.Rect{}
			continue
		}
		recoveryCard.rows[i] = r
		tbxDrawRow(renderer, config, fonts, r, tbxRowSpec{icon: o.icon, title: o.title, subtitle: o.detail, dim: o.disabled},
			i == recoveryCard.focus && !recoveryCard.busy)
		y += rowH + rowGap
	}

	if kb := hintStampNow(); recoveryCard.hintsGen != recoveryCard.gen || recoveryCard.hintsKb != kb || recoveryCard.hints == "" {
		recoveryCard.hintsGen, recoveryCard.hintsKb = recoveryCard.gen, kb
		switch {
		case recoveryCard.busy:
			recoveryCard.hints = hintLine(FooterHint{Button: "B", Label: recoveryCard.backText})
		case len(recoveryCard.options) > 1:
			recoveryCard.hints = hintLine(FooterHint{Button: "Up/Down", Label: "Choose"}, FooterHint{Button: "A", Label: "Select"},
				FooterHint{Button: "B", Label: recoveryCard.backText})
		default:
			recoveryCard.hints = hintLine(FooterHint{Button: "A", Label: "Select"}, FooterHint{Button: "B", Label: recoveryCard.backText})
		}
	}
	// Centred by the width of what is drawn: a line that had to be shortened
	// measured at its full width started left of the card.
	hints := ellipsize(fonts.small, recoveryCard.hints, card.W-2*OverlayPadX)
	hw, hh := measureText(fonts.small, hints)
	renderText(renderer, config, fonts.small, hints, ColorTextTertiary(),
		card.X+(card.W-hw)/2, card.Y+card.H-footerH+(footerH-hh)/2)
}
