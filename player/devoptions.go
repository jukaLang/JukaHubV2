package main

// devoptions.go — Settings → Developer: the few
// switches and actions someone writing scripts, paks or bug reports wants in
// one place.
//
//   - Debug logging: debug lines in the log while on (logging.go's handler
//     asks logDebugForced beside its level, so it changes at once and needs no
//     restart); kept across starts, off by default. Off means the normal level
//     — info on a handheld, or whatever JUKAHUB_LOG_LEVEL says. The extra
//     lines go to the in-memory log only (the Logs screen, Copy logs); the
//     card's errors.txt still gets warnings and errors alone.
//   - Copy logs to the card: one zip, jukahub-logs-<date>.zip in JukaHub-logs
//     at the top of the card (on a PC, in the app's log folder): the in-app
//     log and the log files the phone's "Download logs" takes, with the same
//     masking of names, networks and addresses (companion_logs.go).
//   - Launch scripts (launchscripts.go).
//   - Links to the developer rows that live elsewhere and stay there: the Logs
//     screen, Config and Drivers & hardware.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// devDebugLogKey is the Custom setting behind Debug logging; unset is Off.
	devDebugLogKey = "debugLogging"
	// devLogsFolder is the folder Copy logs writes to at the top of the card.
	devLogsFolder = "JukaHub-logs"
)

// logDebugForced makes the log take debug lines whatever its level
// (logRingHandler.Enabled). Any goroutine.
var logDebugForced atomic.Bool

// devLogsCopyBusy lets one copy run at a time.
var devLogsCopyBusy atomic.Bool

func init() {
	deferStartupTask("debug-logging", func() {
		if devDebugLogEnabled(appConfig) {
			logDebugForced.Store(true)
			logInfo("developer", "debug logging is on (Settings → Developer)")
		}
	})
}

// devDebugLogEnabled reports the setting; unset is Off.
func devDebugLogEnabled(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, devDebugLogKey); ok {
			return coerceBool(v)
		}
	}
	return false
}

// devOptionsSettingsSection is Settings → Developer.
func devOptionsSettingsSection() setSection {
	copyLabel := "Copy logs to the card"
	if !IsTrimuiDevice() {
		copyLabel = "Save logs as a zip"
	}
	return setSection{
		ID: "developer", Title: "Developer", Glyph: "code",
		Subtitle: "Debug logging, logs and your own launch scripts",
		Rows: []setRow{
			{
				ID: devDebugLogKey, Label: "Debug logging", Kind: setKindToggle,
				Detail: func(c *Config) string {
					if devDebugLogEnabled(c) {
						return "Every detail goes into the log · a little slower; turn off when done"
					}
					return "Off: the log keeps information, warnings and errors"
				},
				Get: devDebugLogEnabled,
				Set: func(c *Config, on bool) {
					setStoreBool(c, devDebugLogKey, on)
					logDebugForced.Store(on)
					logInfo("developer", "debug logging %s", strings.ToLower(setOnOff(on)))
					setToast("Debug logging: " + setOnOff(on))
				},
			},
			{
				ID: "devCopyLogs", Label: copyLabel, Kind: setKindAction,
				Detail: func(*Config) string {
					if IsTrimuiDevice() {
						return "One zip in " + devLogsFolder + " on the card · names and networks masked"
					}
					return "One zip in " + logsFolderSummary() + " · names and networks masked"
				},
				Open: devOptionsCopyLogs,
			},
			// Your own scripts before and after games and apps (launchscripts.go).
			launchScriptsSettingsRow(),
			{
				// The live log (systool_logs.go); also in Tools.
				ID: "devLogs", Label: "Logs", Kind: setKindAction,
				Detail: setStatic("The app's log as it is written · also in Tools"),
				Open:   setOpenScene("LogExporter"),
			},
			{
				// The settings file inspector (cfgdoc.go); also in About.
				ID: "devConfig", Label: "Config", Kind: setKindAction,
				Detail: setStatic("What you changed from the defaults · also in About"),
				Open:   cfgdocOpenInspector,
			},
			{
				// Every part of the handheld and its driver; also in About.
				ID: "devDrivers", Label: "Drivers & hardware", Kind: setKindAction,
				Detail: setStatic("What drives the screen, sound, radios and lights · also in About"),
				Open:   driversOpenSheet,
			},
		},
	}
}

// devOptionsCopyLogs is Copy logs to the card: the zip is written on a
// goroutine, a toast says where. Main thread.
func devOptionsCopyLogs(*Config) {
	if !devLogsCopyBusy.CompareAndSwap(false, true) {
		setToast("The logs are already being copied")
		return
	}
	setToast("Copying the logs…")
	go func() {
		defer devLogsCopyBusy.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logWarn("developer", "copying the logs stopped: %v", r)
				ShowToast("Logs not copied", ToastKindError)
			}
		}()
		path, n, err := devOptionsWriteLogsZip()
		if err != nil {
			logWarn("developer", "logs not copied: %v", err)
			ShowToast("Logs not copied: "+firstLine(err.Error()), ToastKindError)
			return
		}
		logInfo("developer", "copied %d log files to %s", n, path)
		ShowToast(fmt.Sprintf("Copied %d log files to %s", n, path), ToastKindSuccess)
	}()
}

// devOptionsLogsDir is where the zip goes: JukaHub-logs at the top of the
// card on a TrimUI, the app's log folder elsewhere.
func devOptionsLogsDir() (string, error) {
	if IsTrimuiDevice() {
		if root := defaultTrimuiSDRoot(); root != "" {
			return filepath.Join(root, devLogsFolder), nil
		}
	}
	return crashLogDir()
}

// devOptionsWriteLogsZip writes the zip and returns its path and how many
// files it holds. Not on the main thread: the masking asks it for the
// account names (companionLogPersonalNames).
func devOptionsWriteLogsZip() (string, int, error) {
	dir, err := devOptionsLogsDir()
	if err != nil {
		return "", 0, err
	}
	if dir == "" {
		return "", 0, errors.New("no folder for the logs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	names := companionLogPersonalNames()
	files := companionLogFiles()
	now := time.Now()
	path := filepath.Join(dir, "jukahub-logs-"+now.Format("2006-01-02-150405")+".zip")
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	fail := func(err error) (string, int, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", 0, err
	}
	zw := zip.NewWriter(f)
	readme := fmt.Sprintf("JukaHub logs, %s\n\nPasswords, keys and tokens, Wi-Fi network names, account and display names,\ne-mail addresses, Bluetooth and Wi-Fi hardware addresses and LAN addresses\nare masked. Game names and folders stay, so the logs still explain the problem:\nread them before you share them.\n", now.Format("2006-01-02 15:04"))
	if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "README.txt", Method: zip.Deflate, Modified: now}); err == nil {
		_, _ = io.WriteString(fw, readme)
	}
	budget := int64(companionLogsMaxBytes)
	taken := 0
	add := func(name string, mod time.Time, data []byte) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod})
		if err != nil {
			return err
		}
		budget -= int64(len(data))
		taken++
		return companionLogWriteMasked(fw, data, names)
	}
	if lines, _ := appLog.Snapshot(); len(lines) > 0 {
		if err := add("live-log.txt", now, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			return fail(err)
		}
	}
	for _, lf := range files {
		if budget < 4<<10 {
			break
		}
		limit := int64(companionLogsFileMax)
		if limit > budget {
			limit = budget
		}
		data, ok := companionLogReadTail(lf.path, limit)
		if !ok || len(data) == 0 {
			continue
		}
		if err := add(lf.name, lf.mod, data); err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	return path, taken, nil
}
