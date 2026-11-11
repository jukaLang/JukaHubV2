package main

// apkstore_waydroid.go — handing an APK to a real Android, where there is one.
//
// Waydroid runs a whole Android in a container on the device's kernel. A stock
// TrimUI kernel has no binder device, so on the handheld this is almost always
// absent; on a Linux PC (or a community firmware) with Waydroid set up it is
// the one way to run ordinary Java apps, and installing an APK into it is a
// single command. The store offers exactly that — "Install into Android
// (Waydroid)" — and nothing more: Waydroid draws in its own window, so the app
// is opened from Waydroid, not from JukaHub.
//
// The earlier Waydroid engine (android.go, removed from the tree) launched
// container apps through a display bridge; that part is not brought back here.
// The path that reaches argv is validated first: absolute, a regular .apk
// file, and nothing that could read as an option.
//
// Waydroid's exit status does not prove an install: with the session stopped,
// `waydroid app install` logs the failure and still exits 0, and the install
// itself is handed to the container without waiting. So the store installs
// only into a running session (checked again just before), reads the
// command's own words for a failure, and then looks for the package in
// `waydroid app list`. Only a package that shows up there is "installed";
// otherwise the APK was only handed over, and the toast says so.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apkStoreWaydroidProbeTimeout   = 4 * time.Second
	apkStoreWaydroidInstallTimeout = 3 * time.Minute
	// After the install, the app list is read a few times: the container
	// finishes the install on its own schedule.
	apkStoreWaydroidListTries = 5
	// A failure toast carries at most this much of the command's output.
	apkStoreWaydroidMessageMax = 200
)

// apkStoreWaydroidListPause is the wait between two reads of the app list (a
// variable so tests do not wait).
var apkStoreWaydroidListPause = 2 * time.Second

// apkStoreBinderPaths are the device nodes Waydroid's kernel support appears
// as (a plain node, or one inside a binderfs mount).
var apkStoreBinderPaths = []string{
	"/dev/binder",
	"/dev/binderfs/binder",
	"/dev/binderfs/anbox-binder",
	"/dev/anbox-binder",
}

// apkStoreWaydroid is the probe's answer.
type apkStoreWaydroid struct {
	binary  string // the waydroid command, "" when not installed
	binder  bool   // the kernel exposes a binder device
	running bool   // `waydroid status` says the session is up
	reason  string // why installing is not offered, "" when it is
}

// usable reports whether Waydroid is there at all (the Runtime tab's row).
func (w apkStoreWaydroid) usable() bool { return w.binary != "" && w.binder }

// canInstall reports whether "Install into Android" can be offered: Waydroid
// is there and its session was running when it was last looked at.
func (w apkStoreWaydroid) canInstall() bool { return w.usable() && w.running }

// apkStoreWaydroidHost is the environment the probe and the install use, a
// seam for tests: run executes the waydroid binary with arguments and returns
// its combined output.
type apkStoreWaydroidHost struct {
	lookPath func(string) (string, error)
	exists   func(string) bool
	run      func(ctx context.Context, bin string, args ...string) (string, error)
}

func apkStoreCurrentWaydroidHost() apkStoreWaydroidHost {
	return apkStoreWaydroidHost{
		lookPath: exec.LookPath,
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		run: func(ctx context.Context, bin string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
			return string(out), err
		},
	}
}

// apkStoreWaydroidStatus asks whether the session is running.
func apkStoreWaydroidStatus(host apkStoreWaydroidHost, bin string) bool {
	if host.run == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), apkStoreWaydroidProbeTimeout)
	defer cancel()
	out, _ := host.run(ctx, bin, "status")
	return apkStoreWaydroidRunning(out)
}

// apkStoreProbeWaydroid checks the two hard requirements and the session.
func apkStoreProbeWaydroid(host apkStoreWaydroidHost) apkStoreWaydroid {
	var w apkStoreWaydroid
	if IsWindows() {
		w.reason = "Waydroid is a Linux container; Windows has no binder device for it."
		return w
	}
	if host.lookPath != nil {
		if bin, err := host.lookPath("waydroid"); err == nil {
			w.binary = strings.TrimSpace(bin)
		}
	}
	for _, p := range apkStoreBinderPaths {
		if host.exists != nil && host.exists(p) {
			w.binder = true
			break
		}
	}
	switch {
	case w.binary == "":
		w.reason = "Waydroid is not installed."
	case !w.binder:
		w.reason = "The kernel exposes no binder device (/dev/binder), which Waydroid needs; stock TrimUI firmware has none."
	}
	if w.usable() {
		w.running = apkStoreWaydroidStatus(host, w.binary)
	}
	return w
}

// apkStoreWaydroidRunning reads `waydroid status`: "Session: RUNNING".
func apkStoreWaydroidRunning(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "session") {
			return strings.EqualFold(strings.TrimSpace(value), "running")
		}
	}
	return false
}

// apkStoreWaydroidArgs validates the APK path and builds the install argv.
func apkStoreWaydroidArgs(apk string) ([]string, error) {
	clean := filepath.Clean(apk)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("the APK path is not absolute")
	}
	if strings.HasPrefix(filepath.Base(clean), "-") || strings.ContainsAny(clean, "\x00\r\n") {
		return nil, errors.New("the APK's name cannot be passed to waydroid safely — rename it")
	}
	if !strings.EqualFold(filepath.Ext(clean), ".apk") {
		return nil, errors.New("not an .apk file")
	}
	st, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return []string{"app", "install", clean}, nil
}

// apkStoreWaydroidResult is how an install ended when it did not fail.
type apkStoreWaydroidResult int

const (
	apkWaydroidInstalled  apkStoreWaydroidResult = iota // the package is in Waydroid's app list
	apkWaydroidHandedOver                               // accepted, but not (yet) in the list
)

// apkStoreWaydroidFailureWords are what `waydroid app install` prints when it
// fails and still exits 0.
var apkStoreWaydroidFailureWords = []string{"session is stopped", "error", "failure", "failed", "exception"}

// apkStoreWaydroidLastLines keeps the end of a command's output short enough
// for a toast: its last two lines, at most apkStoreWaydroidMessageMax bytes.
func apkStoreWaydroidLastLines(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 2 {
		lines = lines[len(lines)-2:]
	}
	msg := strings.TrimSpace(strings.Join(lines, " "))
	if len(msg) > apkStoreWaydroidMessageMax {
		cut := len(msg) - apkStoreWaydroidMessageMax
		for cut < len(msg) && !utf8.RuneStart(msg[cut]) {
			cut++
		}
		msg = "…" + msg[cut:]
	}
	return msg
}

// apkStoreWaydroidListed reports whether `waydroid app list` names pkg
// ("packageName: com.example.game").
func apkStoreWaydroidListed(list, pkg string) bool {
	for _, line := range strings.Split(list, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "packageName") && strings.TrimSpace(value) == pkg {
			return true
		}
	}
	return false
}

// apkStoreWaydroidInstall installs apk into Waydroid's Android. It refuses
// when the session is not running, treats failure words in the output as a
// failure even after exit status 0, and reports Installed only when pkg (the
// APK's package, "" when unknown) appears in the app list afterwards.
func apkStoreWaydroidInstall(ctx context.Context, host apkStoreWaydroidHost, bin, apk, pkg string) (apkStoreWaydroidResult, error) {
	args, err := apkStoreWaydroidArgs(apk)
	if err != nil {
		return 0, err
	}
	if host.run == nil {
		return 0, errors.New("waydroid cannot be run here")
	}
	if !apkStoreWaydroidStatus(host, bin) {
		return 0, errors.New("waydroid's session is not running — start it with `waydroid session start`, then try again")
	}
	ictx, cancel := context.WithTimeout(ctx, apkStoreWaydroidInstallTimeout)
	out, err := host.run(ictx, bin, args...)
	cancel()
	lower := strings.ToLower(out)
	failed := err != nil
	for _, w := range apkStoreWaydroidFailureWords {
		failed = failed || strings.Contains(lower, w)
	}
	if failed {
		msg := apkStoreWaydroidLastLines(out)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "it gave no reason"
		}
		return 0, errors.New("waydroid did not install it: " + msg)
	}
	if pkg == "" {
		return apkWaydroidHandedOver, nil
	}
	for try := 0; try < apkStoreWaydroidListTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return apkWaydroidHandedOver, nil
			case <-time.After(apkStoreWaydroidListPause):
			}
		}
		lctx, lcancel := context.WithTimeout(ctx, apkStoreWaydroidProbeTimeout)
		list, _ := host.run(lctx, bin, "app", "list")
		lcancel()
		if apkStoreWaydroidListed(list, pkg) {
			return apkWaydroidInstalled, nil
		}
	}
	return apkWaydroidHandedOver, nil
}
