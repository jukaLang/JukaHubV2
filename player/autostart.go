package main

// autostart.go implements the "Start JukaHub at login" setting.
//
// On a desktop the setting is off by default and never changes anything on
// disk until the user turns it on; on a TrimUI the first start turns it on once
// (autostart_firstrun.go). Enabling it writes a launcher into the platform's
// standard startup location, disabling it removes that launcher again. Only
// JukaHub's own file is ever written or removed.
//
// Two kinds of hook are supported:
//   - Desktop: a freedesktop .desktop file in the per-user autostart folder,
//     or (on Windows) a value in the current user's Run key.
//   - TrimUI: an SD-card boot script in System/starts, which the firmware's
//     runtrimui.sh runs at boot. That directory is the community-documented
//     hook for stock and CrossMix firmware; the app creates it when missing and
//     never touches any other script in it.
//
// How the TrimUI hook starts JukaHub (trimuiAutostartTemplate). The first hook
// waited for the stock menu (MainUI) and then ran launch.sh, which paused the
// firmware's launcher loop and killed MainUI. That never worked: with its
// parent paused, the killed MainUI stays a zombie, busybox pidof keeps listing
// it, and launch.sh gave up after eight kills without starting JukaHub. It also
// needed pkill to find the loop by name, which depends on the busybox build.
// The hook now uses the firmware's own way of starting an app instead: it
// queues JukaHub in /tmp/cmd_to_run.sh, the file MainUI writes when an app is
// picked, and closes MainUI once it is up; the launcher loop then runs the
// queue with its own environment and services, and brings MainUI back when
// JukaHub exits, exactly as after an app started from the menu. A loop that
// reads the queue before opening MainUI starts JukaHub without ever showing the
// menu. Only a loop that ignores the queue gets the old direct start, with the
// loop paused by pid (MainUI's parent) rather than by name.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Autostart file names. One launcher per platform, named so the app can find
// and remove exactly its own entry and nothing else.
const (
	autostartWindowsFile = "JukaHub.cmd"
	autostartLinuxFile   = "jukahub.desktop"
	// Windows starts JukaHub from the per-user Run key (no console), not a
	// .cmd. The file name above is still the old launcher the setting cleans
	// up when it changes.
	autostartWindowsRunKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartWindowsRunValue = "JukaHub"
	// trimuiAutostartFile lives in <SD>/System/starts. The name marks it as
	// ours so the setting can remove exactly this file later.
	trimuiAutostartFile = "jukahub-autostart.sh"
	// trimuiAutostartPendingFile sits in the app folder while a start the boot
	// hook made has not yet reached JukaHub's screen. Still there at the next
	// boot, it makes that boot stay on the stock menu (see the template).
	// bootos.go reads the same file as "this start is the power-on".
	trimuiAutostartPendingFile = ".autostart-pending"
	// trimuiAutostartLogFile is where the boot hook says what it did.
	trimuiAutostartLogFile = "autostart.log"
	// trimuiAutostartKeepMenuFile sits in the app folder while Skip the stock
	// menu at boot is off: the hook then lets the stock menu open first and
	// closes it once it is up (its KEEP_MENU). A file, not a value written into
	// the hook, so the hook stays the same text for every choice (the OS
	// package ships it ready-made, and reconcileAutoStart compares it whole).
	trimuiAutostartKeepMenuFile = ".autostart-keep-menu"
	// trimuiAutostartStagingFile is the hook's next version while it is being
	// written, in <SD>/System: the same card as System/starts, so the rename is
	// atomic, but never inside it, because the firmware may run every file
	// there.
	trimuiAutostartStagingFile = ".jukahub-autostart.tmp"
)

// Overridable for tests: where the launcher lives, which executable it runs,
// whether this is a handheld, and where its SD card is mounted.
var (
	autostartDirFn    = defaultAutostartDir
	autostartExeFn    = func() (string, error) { return os.Executable() }
	autostartIsTrimui = func() bool { return IsTrimuiDevice() }
	trimuiSDRootFn    = defaultTrimuiSDRoot
	// trimuiAutostartLockDir is created by the boot hook the first time it
	// runs after a boot (/tmp is empty at every boot), so its absence tells a
	// later JukaHub start that the firmware did not run the hook this time.
	trimuiAutostartLockDir = "/tmp/jukahub-autostart.lock"
	// trimuiLauncherScript is the firmware's launcher loop on stock and
	// CrossMix firmware, read only for the boot report.
	trimuiLauncherScript = "/usr/trimui/bin/runtrimui.sh"
)

// defaultTrimuiSDRoot finds the SD card the app is installed on. JukaHub lives
// in <SD>/Apps/JukaHub, so the executable path already names the root; the
// environment override exists for a symlinked binary or a test.
func defaultTrimuiSDRoot() string {
	if root := strings.TrimSpace(os.Getenv("JUKAHUB_SD_ROOT")); root != "" {
		return root
	}
	exe, err := autostartExeFn()
	if err != nil {
		return ""
	}
	appDir := filepath.Dir(exe)
	appsDir := filepath.Dir(appDir)
	if !strings.EqualFold(filepath.Base(appsDir), "Apps") {
		return ""
	}
	return filepath.Dir(appsDir)
}

// trimuiAutostartPath is the boot hook's location on the SD card.
func trimuiAutostartPath() (string, error) {
	root := trimuiSDRootFn()
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("cannot find the SD card")
	}
	return filepath.Join(root, "System", "starts", trimuiAutostartFile), nil
}

// trimuiAppDir is the folder JukaHub runs from, where launch.sh, the hook's
// log and its pending marker live.
func trimuiAppDir() (string, error) {
	exe, err := autostartExeFn()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(exe) == "" {
		return "", fmt.Errorf("cannot locate the JukaHub executable")
	}
	return filepath.Dir(exe), nil
}

// trimuiAutostartTemplate is the boot hook. @APP_DIR@, @LAUNCH@ and @PENDING@
// become the shell-quoted app folder, launch.sh and pending marker paths. It is
// written for busybox ash and plain POSIX sh (dash and bash parse it too), and
// it was run against simulated launcher loops of every shape the hook allows
// for: one that opens MainUI first, one that reads the queue first, one that
// ignores the queue after a kill, slow ones of both kinds (one with a script
// name too long for /proc's comm), one that moves the queue away before
// running it, one that sources it, one that dies with MainUI, a non-shell
// supervisor that respawns MainUI; the hook run, sourced, backgrounded, run
// twice, or stopped while JukaHub runs; a MainUI that clears or truncates the
// queue, an app picked in the first seconds or just before the menu is closed,
// a queued resume, a saved copy of the command put back on a skipped boot, no
// MainUI at all, a shell without fractional sleep. The rules it keeps:
//   - Safe sourced: everything is inside one subshell, so nothing out here
//     exits or sets a variable, and errexit/nounset/noglob/IFS from the
//     firmware's shell are reset inside it. The queued command is as safe
//     sourced.
//   - Never blocks the boot: only the queueing (a few file checks) runs before
//     the hook returns; the waiting runs in a background subshell.
//   - Once per boot (a lock directory in /tmp, which also carries the boot id
//     in case /tmp survives a reboot), and JukaHub starts at most once per boot
//     from it: the queued command and the direct start both claim the start
//     with mkdir, and the queued command needs a go token in the lock, so a
//     copy of it put back later (a resume) or left from a boot the hook called
//     off does nothing.
//   - Never two programs on the screen and never none: the queue is only ever
//     created where it is empty (a hard link, so a command written at the same
//     moment is never overwritten); it backs off when JukaHub is already
//     running, when another command is queued (an app picked in the menu, a
//     CrossMix resume), when the menu closes and takes the queue with it, and
//     when MainUI never appears. MainUI is closed only once it has been up two
//     seconds and has stopped writing (eight seconds at most), so its settings
//     are not cut off mid-write. The direct start pauses the launcher loops
//     behind MainUI (shell scripts only, by name or executable) before it
//     checks again, refuses when something it cannot pause restarts MainUI or
//     a loop is already starting an app, and lets the loops go when JukaHub
//     exits, or when the hook is told to stop.
//   - A start that hangs (the pending marker is still there at the next boot)
//     makes the next boot stay on the stock menu, so a JukaHub that hangs at
//     start can never lock the device up at boot. The marker goes once JukaHub
//     exits (the device is then back on the menu) or has been up for
//     autoStartSettleDelay.
//   - The hand-over is one picture change, not three. Closing the menu leaves
//     "menu" in the lock (trimuiAutostartMenuShown): the queued command puts
//     black on the framebuffer before launch.sh runs, instead of the closed
//     menu's frozen last picture, and JukaHub's power-on boot fades in from
//     that black (bootos.go) rather than jumping to the boot logo, which is
//     only on screen when the menu never opened. A loop the direct start
//     pauses is written down in /tmp/jukahub-paused, which launch.sh resumes
//     as it hands the screen back; the log names each process by pid, name
//     and script, so a report shows how this firmware's loop looks.
//   - Every boot skips the menu (JukaHub OS since 2026-11-17, the app too
//     since 2026-11-18): unless Settings → General → Skip the stock menu at
//     boot is off (the app folder then holds .autostart-keep-menu) or, in OS
//     mode, os.json says "fastBoot": false, a stand-in is bind-mounted over
//     /usr/trimui/bin/MainUI before the hook returns. Its first run with
//     JukaHub's command queued takes itself off and returns, so the loop runs
//     the queue in place of the menu; any other run is the real menu (through
//     a second bind kept for that, named MainUI so pidof still finds the
//     menu). The hook takes both binds off once JukaHub started, when it
//     gives up, when the real menu is up, and when the stand-in has not run
//     within 20 s (a loop that could not exec it). Simulated with
//     the O-* and F-* scenarios of tmp/autostart-sim (loops A, B, C, K, S
//     and a supervisor that ignores the queue; F-* without OS mode).
//   - The path to JukaHub is short: before it returns the hook forks only
//     what the queue and the stand-in need (one date for its log lines, no
//     command substitution for the boot id or the tool checks); the log's
//     size check and the flush that makes the pending marker durable run in
//     the background copy. Nothing on that path sleeps or polls.
//   - Each boot is measured (2026-11-18): the hook, the stand-in and the
//     queued command note the uptime in the lock (t-slot, t-launch; reads and
//     builtins only), and once JukaHub has written its record
//     (/tmp/jukahub-boot-timing, startup.go) the background copy logs one
//     indented "boot times" line from the firmware launcher's start to Home,
//     and who ran the hook. The firmware's launcher script, its init.d order
//     and the card's mount line are copied into the log once per log, after
//     Home (never while JukaHub starts and the card is busy).
//   - Why System/starts and nothing earlier: it is the first point at which
//     the stock boot runs anything from the card (runtrimui.sh runs it once
//     the card is mounted, about 6 s into a Brick's boot as Slot measured).
//     .tmp_update/updater is read only by the runtrimui.sh that MinUI,
//     NextUI and TortOS 1.x put on the eMMC in place of the stock one, and
//     an .awimg is a firmware flash: both write the device. The card's
//     trimui/app/MainUI (reported for the Brick Pro's stock runtrimui.sh)
//     takes the menu's place in the loop, the same moment the stand-in
//     already takes, and belongs to MinUI/NextUI/CrossMix cards.
//
// Go raw string: the script must not contain a backquote.
const trimuiAutostartTemplate = `#!/bin/sh
# Written by JukaHub's "Start JukaHub at login" setting (boot hook 2).
# Delete this file, or turn the setting off, to stop JukaHub opening at boot.
# What happened at each boot is written to autostart.log in the JukaHub folder.
#
# The firmware runs this early in its boot. Everything happens inside one
# subshell: nothing out here exits or sets a variable, so the file is as safe
# sourced as it is run. The subshell only queues JukaHub (a few file checks)
# and leaves the waiting to a background copy of itself, so the boot carries
# on at once.
#
# JukaHub is started the way the stock menu starts an app: a command queued in
# /tmp/cmd_to_run.sh, which the firmware's launcher loop runs with its own
# environment and services, bringing the menu back when JukaHub exits. A
# stand-in for the menu's program lets the loop run the queue at once, so the
# menu never shows (fast_boot). Where that cannot be, or Skip the stock menu at
# boot is off, the menu is closed once it is up, and the loop then runs the
# queue exactly as if JukaHub had been picked in it.
(
set +e +u +f
unset IFS
trap '' HUP
PATH="${PATH:+$PATH:}/usr/sbin:/usr/bin:/sbin:/bin"
APP_DIR=@APP_DIR@
LOG="$APP_DIR/autostart.log"
PENDING="$APP_DIR/.autostart-pending"
Q=/tmp/cmd_to_run.sh
LOCK=/tmp/jukahub-autostart.lock
# The queued command starts JukaHub only while this exists, so a copy of it
# put back later (a resume feature), or one left from a boot the hook called
# off, does nothing.
GO="$LOCK/go"
# Left by the moment the menu is closed below: the queued command then puts
# black on the screen instead of the closed menu's last picture, and JukaHub's
# boot fades in from that black rather than starting from the boot logo.
MENU="$LOCK/menu"
RAN=/tmp/jukahub-autostart.ran
TAG=jukahub-autostart-cmd
# The launcher loops the direct start pauses, written down ("<pid> <start
# time> <name>") for launch.sh, which resumes exactly those as it hands the
# screen back (its paused record).
PAUSED=/tmp/jukahub-paused
# Left in the app folder by Settings -> General -> Skip the stock menu at boot
# switched off: the stock menu then opens first at every boot and is closed
# once it is up (also in JukaHub OS mode).
KEEP_MENU="$APP_DIR/.autostart-keep-menu"
# What the log calls the start: "JukaHub OS" in OS mode (fast_boot).
WHAT=JukaHub
# JukaHub's own record of this boot, written once it has drawn Home: the boot
# id, then the uptime at JukaHub's start and at its first frame.
TIMING=/tmp/jukahub-boot-timing

# One log line. SAY_DATE, while set, is the time for every line (the part
# before the hook returns runs in a moment, and each date is a fork on the
# boot's path); the background copy reads the clock for each line. SAY_TO,
# while set, is a file in /tmp the lines go to first: the lines said before
# the hook returns are copied to the card by the background copy (or at once
# when JukaHub is not started), because on a card mounted "sync" (the Brick's)
# every line written there is a wait for the card.
SAY_TO=
say() {
	up=
	read -r up _ 2>/dev/null </proc/uptime
	printf '[%s boot+%ss] %s\n' "${SAY_DATE:-$(date '+%Y-%m-%d %H:%M:%S')}" "${up:-?}" "$*" >>"${SAY_TO:-$LOG}" 2>/dev/null
}
# Copy the lines kept in /tmp to the log; later lines go straight there.
flush_said() {
	if [ -n "$SAY_TO" ]; then
		[ -s "$SAY_TO" ] && cat "$SAY_TO" >>"$LOG" 2>/dev/null
		rm -f "$SAY_TO"
		SAY_TO=
	fi
}

# A process's state, parent and start time (st_state, st_ppid, st_start) from
# /proc/<pid>/stat. The name in brackets may hold spaces, so the fields are
# read after its last ") ".
stat_of() {
	line=
	read -r line 2>/dev/null <"/proc/$1/stat" || return 1
	set -- ${line##*) }
	st_state="$1"
	st_ppid="$2"
	st_start="${20}"
}

# A start time in clock ticks (st_start) as seconds since the kernel started:
# /proc counts in hundredths on every Linux JukaHub runs on.
secs() {
	case "$1" in
	"" | *[!0-9]*) printf '?' ;;
	*) printf '%d.%02d' $(($1 / 100)) $(($1 % 100)) ;;
	esac
}

# One process for the log: its pid, its name and, for a shell, the script it
# runs (a busybox may name every script it runs plain "sh").
who() {
	wn=
	read -r wn 2>/dev/null <"/proc/$1/comm"
	wa=
	case "$wn" in
	sh | ash | bash | dash | busybox) wa=" $(tr '\0\n\r\t' '    ' <"/proc/$1/cmdline" 2>/dev/null | cut -c1-100)" ;;
	esac
	printf '%s %s%s' "$1" "${wn:-?}" "${wa% }"
}

# Write paused pids down in $PAUSED with their start times.
record() {
	for p in "$@"; do
		stat_of "$p" || continue
		rn=
		read -r rn 2>/dev/null <"/proc/$p/comm"
		printf '%s %s %s\n' "$p" "$st_start" "${rn:-?}" >>"$PAUSED" 2>/dev/null
	done
}

# Process ids of a running program (busybox pidof, else pgrep). A zombie is
# not running: busybox pidof lists one until its parent collects it.
pids() {
	for p in $(
		if command -v pidof >/dev/null 2>&1; then
			pidof "$1" 2>/dev/null
		else
			pgrep -x "$1" 2>/dev/null
		fi
	); do
		stat_of "$p" || continue
		[ "$st_state" = Z ] || printf '%s ' "$p"
	done
}

# The parents of the given pids, each once, never init.
parents() {
	out=
	for p in "$@"; do
		stat_of "$p" || continue
		case "$st_ppid" in
		"" | 0 | 1 | *[!0-9]*) continue ;;
		esac
		case " $out " in
		*" $st_ppid "*) ;;
		*) out="$out $st_ppid" ;;
		esac
	done
	printf '%s' "$out"
}

# Those of the given pids that are shell scripts (a launcher loop) or the
# firmware's launcher, by name or by executable (a long script name is cut to
# 15 characters): only those are ever paused, never a daemon that reads keys.
shells() {
	out=
	for p in "$@"; do
		c=
		read -r c 2>/dev/null <"/proc/$p/comm"
		case "$c" in
		sh | ash | bash | dash | busybox | *.sh | trimui_launcher)
			out="$out $p"
			continue
			;;
		esac
		case "$(readlink "/proc/$p/exe" 2>/dev/null)" in
		*/sh | */ash | */bash | */dash | */busybox) out="$out $p" ;;
		esac
	done
	printf '%s' "$out"
}

# Those of the given pids that are still running, each once.
alive() {
	out=
	for p in "$@"; do
		case " $out " in
		*" $p "*) continue ;;
		esac
		stat_of "$p" || continue
		[ "$st_state" = Z ] || out="$out $p"
	done
	printf '%s' "$out"
}

# Whether one of the given (paused) loops is running something other than
# MainUI or a sleep: an app it was already starting.
busy() {
	for d in /proc/[0-9]*; do
		stat_of "${d#/proc/}" || continue
		case " $* " in
		*" $st_ppid "*) ;;
		*) continue ;;
		esac
		[ "$st_state" = Z ] && continue
		c=
		read -r c 2>/dev/null <"$d/comm"
		case "$c" in
		MainUI | sleep) ;;
		*) return 0 ;;
		esac
	done
	return 1
}

# How much the given processes have written so far: a MainUI that has stopped
# writing (its settings, its caches) has finished starting.
written() {
	for p in "$@"; do
		while read -r k v; do
			[ "$k" = wchar: ] && printf '%s ' "$v"
		done 2>/dev/null <"/proc/$p/io"
	done
}

# What the firmware's queue holds: ours, other (an app or a resumed game) or none.
queued() {
	q=
	[ -f "$Q" ] && q="$(cat "$Q" 2>/dev/null)"
	case "$q" in
	"") echo none ;;
	*"$TAG"*) echo ours ;;
	*) echo other ;;
	esac
}

# Write JukaHub's command beside the queue and link it into place. The link
# fails if anything got there first (an app picked, a resume), so the queue is
# never overwritten and never read half written; an empty queue counts as
# none. The command needs the go token, claims the start with mkdir so that
# JukaHub starts at most once per boot however often it is run, and drops the
# pending marker once JukaHub has exited: the device is back on the menu,
# nothing hung.
queue_ours() {
	rm -f "$Q.jukahub"
	cat >"$Q.jukahub" <<'JUKAHUB_QUEUE' || return 1
#!/bin/sh
# jukahub-autostart-cmd: queued at boot by SDCARD/System/starts/jukahub-autostart.sh
if [ -e /tmp/jukahub-autostart.lock/go ] && mkdir /tmp/jukahub-autostart.ran 2>/dev/null; then
	# When the firmware ran this, for the boot times in autostart.log.
	read -r JUKAHUB_UP JUKAHUB_REST </proc/uptime 2>/dev/null && printf '%s\n' "$JUKAHUB_UP" >/tmp/jukahub-autostart.lock/t-launch 2>/dev/null || :
	unset JUKAHUB_UP JUKAHUB_REST
	rm -f /tmp/cmd_to_run.sh
	# Black, not the closed menu's last picture, until JukaHub draws.
	if [ -e /tmp/jukahub-autostart.lock/menu ]; then
		dd if=/dev/zero of=/dev/fb0 bs=65536 count=512 2>/dev/null || :
	fi
	/bin/sh @LAUNCH@ || :
	rm -f @PENDING@
else
	rm -f /tmp/cmd_to_run.sh
fi
JUKAHUB_QUEUE
	chmod 755 "$Q.jukahub" 2>/dev/null
	[ -f "$Q" ] && [ ! -s "$Q" ] && rm -f "$Q"
	ln "$Q.jukahub" "$Q" 2>/dev/null
	ok=$?
	rm -f "$Q.jukahub"
	return "$ok"
}

# Queue JukaHub unless something is queued: 0 queued (or it already was), 1 the
# queued command has run, 2 another command is queued, 3 no queue could be written.
enqueue() {
	[ -e "$RAN" ] && return 1
	case "$(queued)" in
	ours) return 0 ;;
	other) return 2 ;;
	esac
	queue_ours && return 0
	[ -e "$RAN" ] && return 1
	case "$(queued)" in
	ours) return 0 ;;
	other) return 2 ;;
	esac
	return 3
}

# JukaHub boots without the stock menu. Unless Skip the stock menu at boot is
# off (KEEP_MENU, or "fastBoot": false in os/os.json in OS mode), a small
# stand-in is bind-mounted over the menu's program for this boot only (RAM, no
# file of the firmware is written): when the launcher loop runs "MainUI" the
# first time, the stand-in takes itself off again and returns at once, and the
# loop runs the queue - JukaHub, or JukaHub OS - with its own environment and
# services, as if JukaHub had been picked. Every other run of it (the queue
# gone, an app queued instead, JukaHub already started) is the real menu, so
# the loop can never end up without one. A loop that ignores the queue gets
# the real menu on its next round, and the wait below carries on as without
# the stand-in.
MAINUI=/usr/trimui/bin/MainUI
SHIM="$LOCK/MainUI.jukahub"
# Named MainUI, so a menu run through it is still "MainUI" to pidof, killall
# and this hook (its name in /proc is the file's).
REAL="$LOCK/real/MainUI"
SKIPPED="$LOCK/skipped"
# Whether the stand-in is what MAINUI shows now (not a menu another system
# bound there).
shim_on() {
	dd if="$MAINUI" bs=256 count=1 2>/dev/null | grep -q jukahub-mainui-shim
}
# Take the stand-in and the real menu's handle off, whatever happened; 1 when
# the stand-in would not come off (the handle then stays for it).
unshim() {
	if shim_on; then
		umount -l "$MAINUI" 2>/dev/null || umount "$MAINUI" 2>/dev/null
		if shim_on; then
			# Still on: its later runs need the real menu's handle, or the
			# loop would run a stand-in with no menu to hand over to.
			say "could not take the stand-in off $MAINUI"
			return 1
		fi
	fi
	# Tried whether or not /proc/mounts lists it under this path (a /tmp
	# reached through a symlink is listed under its real path): umount of a
	# path that is no mount point only fails.
	[ -e "$REAL" ] && { umount -l "$REAL" 2>/dev/null || umount "$REAL" 2>/dev/null; }
	:
}
fast_boot() {
	if [ -f "$APP_DIR/os/os.json" ] && grep -q '"enabled"[[:space:]]*:[[:space:]]*true' "$APP_DIR/os/os.json" 2>/dev/null; then
		WHAT="JukaHub OS"
		if grep -q '"fastBoot"[[:space:]]*:[[:space:]]*false' "$APP_DIR/os/os.json" 2>/dev/null; then
			say "JukaHub OS is on; the stock menu opens first (Skip the stock menu is off)"
			return 1
		fi
	fi
	if [ -e "$KEEP_MENU" ]; then
		say "the stock menu opens first and is closed once it is up (Skip the stock menu at boot is off)"
		return 1
	fi
	# dd and grep tell the stand-in from the menu (shim_on, and the stand-in's
	# own check): without them it could not take itself off.
	if [ ! -f "$MAINUI" ] || ! command -v mount >/dev/null 2>&1 || ! command -v umount >/dev/null 2>&1 ||
		! command -v dd >/dev/null 2>&1 || ! command -v grep >/dev/null 2>&1; then
		say "the stock menu cannot be skipped here (no $MAINUI, mount, dd or grep); it is closed once it is up"
		return 1
	fi
	shim_on && return 0
	rm -f "$SHIM"
	cat >"$SHIM" <<'JUKAHUB_SHIM' || return 1
#!/bin/sh
# jukahub-mainui-shim: stands in for the stock menu once at boot (Start JukaHub at login)
[ "$1" = --jukahub-probe ] && exit 0
M=/usr/trimui/bin/MainUI
L=/tmp/jukahub-autostart.lock
# When the launcher loop reached the menu, for the boot times in autostart.log.
[ -e "$L/t-slot" ] || { read -r t _ </proc/uptime && printf '%s\n' "$t" >"$L/t-slot"; } 2>/dev/null
on() { dd if="$M" bs=256 count=1 2>/dev/null | grep -q jukahub-mainui-shim; }
on && { umount -l "$M" 2>/dev/null || umount "$M" 2>/dev/null; }
if [ -e "$L/go" ] && [ ! -e /tmp/jukahub-autostart.ran ] && grep -q jukahub-autostart-cmd /tmp/cmd_to_run.sh 2>/dev/null && mkdir "$L/skipped" 2>/dev/null; then
	exit 0
fi
on && exec "$L/real/MainUI" "$@"
exec "$M" "$@"
JUKAHUB_SHIM
	chmod 755 "$SHIM" 2>/dev/null
	if ! "$SHIM" --jukahub-probe </dev/null >/dev/null 2>&1; then
		rm -f "$SHIM"
		say "/tmp cannot run the stand-in, so the stock menu is closed once it is up"
		return 1
	fi
	# The real menu stays reachable however the stand-in ends.
	mkdir -p "$LOCK/real" 2>/dev/null
	: >"$REAL" 2>/dev/null
	if ! mount --bind "$MAINUI" "$REAL" 2>/dev/null && ! mount -o bind "$MAINUI" "$REAL" 2>/dev/null; then
		rm -f "$REAL" "$SHIM"
		say "mount --bind failed, so the stock menu is closed once it is up"
		return 1
	fi
	if ! { mount --bind "$SHIM" "$MAINUI" 2>/dev/null || mount -o bind "$SHIM" "$MAINUI" 2>/dev/null; } || ! shim_on; then
		unshim
		rm -f "$SHIM"
		say "the stand-in could not be put over $MAINUI, so the stock menu is closed once it is up"
		return 1
	fi
	return 0
}

# Take the start back: no queued copy can start JukaHub any more, and nothing
# is pending.
call_off() {
	rm -f "$GO" "$PENDING"
	[ "$(queued)" = ours ] && rm -f "$Q"
	unshim
}

if [ ! -f "$APP_DIR/launch.sh" ]; then
	say "launch.sh is missing from the JukaHub folder, so JukaHub is not started"
	exit 0
fi
# Once per boot, however often this file is run. /tmp starts empty at every
# boot; a lock an earlier boot left (a /tmp that survives a reboot) carries
# that boot's id and is cleared.
BOOT=
read -r BOOT 2>/dev/null </proc/sys/kernel/random/boot_id
if ! mkdir "$LOCK" 2>/dev/null; then
	old=
	read -r old 2>/dev/null <"$LOCK/boot"
	{ [ -n "$BOOT" ] && [ -n "$old" ] && [ "$old" != "$BOOT" ]; } || exit 0
	rm -rf "$LOCK" "$RAN"
	mkdir "$LOCK" 2>/dev/null || exit 0
fi
printf '%s\n' "$BOOT" >"$LOCK/boot" 2>/dev/null
# For the boot times logged once JukaHub is up: when this hook started, and
# the processes above it with their start times ("pid:ticks"; one read each,
# no program run), named later by the background copy.
HOOK_UP=
read -r HOOK_UP _ 2>/dev/null </proc/uptime
CHAIN=
p=$$
i=0
while [ "$i" -lt 6 ] && [ "$p" -gt 1 ] 2>/dev/null && stat_of "$p"; do
	CHAIN="$CHAIN $p:$st_start"
	p="$st_ppid"
	i=$((i + 1))
done

SAY_TO="$LOCK/said"
SAY_DATE="$(date '+%Y-%m-%d %H:%M:%S')"
say "boot hook started (run as $0)"

if ! command -v pidof >/dev/null 2>&1 && ! command -v pgrep >/dev/null 2>&1; then
	say "cannot see running programs (no pidof or pgrep), so JukaHub is not started"
	flush_said
	exit 0
fi
# Left behind by a start that hung (JukaHub deletes it once it has been up a
# little while, and the queued command once JukaHub exits): this boot stays on
# the stock menu, so a JukaHub that hangs at start can never lock the device up.
if [ -f "$PENDING" ]; then
	rm -f "$PENDING"
	[ "$(queued)" = ours ] && rm -f "$Q"
	say "JukaHub did not finish starting after the last boot, so this boot stays on the stock menu; the next boot tries again"
	flush_said
	exit 0
fi
if [ -n "$(pids JukaHub)" ]; then
	say "JukaHub is already running"
	flush_said
	exit 0
fi
: >"$PENDING" 2>/dev/null
: >"$GO" 2>/dev/null
enqueue
case $? in
1)
	call_off
	say "JukaHub was already started at this boot"
	flush_said
	exit 0
	;;
2)
	call_off
	say "another app is already queued to start (resume at boot?), so JukaHub is not started"
	flush_said
	exit 0
	;;
3)
	call_off
	say "could not queue JukaHub in /tmp, so it is not started"
	flush_said
	exit 0
	;;
esac
say "queued JukaHub for the firmware's launcher"
# Before the hook returns: the loop runs the menu right after the boot scripts.
if fast_boot; then
	say "$WHAT: the stock menu is skipped at this boot (a stand-in for $MAINUI until the queue runs)"
fi
# The moment the boot goes on (for the boot times).
RET_UP=
read -r RET_UP _ 2>/dev/null </proc/uptime

# The rest waits, so it runs in the background and the boot goes on.
(
cd / 2>/dev/null
SAY_DATE=
if [ -f "$LOG" ]; then
	size="$(wc -c <"$LOG" 2>/dev/null | tr -d ' ')"
	[ "${size:-0}" -gt 65536 ] 2>/dev/null && mv -f "$LOG" "$LOG.1" 2>/dev/null
fi
# The lines said before the hook returned, onto the card.
flush_said
# Told to stop before JukaHub started: call the start off.
withdraw() {
	if [ ! -e "$RAN" ]; then
		call_off
		say "told to stop before JukaHub started, so the start is called off"
	fi
	exit 0
}
trap withdraw TERM INT
# The pending marker must be on the card before JukaHub could hang. Only the
# card's file system where busybox can (sync -f); elsewhere sync ignores the
# name and flushes everything, as before.
sync -f "$PENDING" 2>/dev/null || sync

# Steps per second: busybox sleeps in fractions only when it was built to.
if sleep 0.1 2>/dev/null; then STEP=0.5 PER=2; else STEP=1 PER=1; fi
nap() { sleep "$STEP" 2>/dev/null || sleep 1; }

# Indented lines are notes, not steps: JukaHub's report of the hook's last
# line (errors.txt) skips them.
note() {
	printf '    %s\n' "$*" >>"$LOG" 2>/dev/null
}
# How this firmware boots and starts apps, once per log: its launcher script,
# the order of its boot services and how the SD card is mounted (a "sync"
# card writes slowly). For a report if JukaHub does not open, and to see what
# runs before this hook. Written once JukaHub is up or not started, never
# while it is starting (the card is busy then).
boot_setup() {
	grep -q '^    boot setup:' "$LOG" 2>/dev/null && return 0
	{
		echo "    boot setup: the firmware's launcher script, boot services and SD card mount (once per log)"
		grep -n '' /usr/trimui/bin/runtrimui.sh 2>/dev/null | head -n 300 | cut -c1-160 | sed 's/^/    runtrimui.sh:/'
		grep -H 'START=' /etc/init.d/* 2>/dev/null | head -n 80 | cut -c1-160 | sed 's/^/    init.d:/'
		grep ' /mnt/SDCARD ' /proc/mounts 2>/dev/null | head -n 2 | cut -c1-200 | sed 's/^/    card:/'
	} >>"$LOG" 2>/dev/null
}
# The processes above this hook that still run, nearest first, with their
# start times. FW_UP becomes the firmware launcher's (runtrimui.sh) start.
FW_UP=
chain() {
	out=
	for e in $CHAIN; do
		# Gone, or the pid is another process's now.
		stat_of "${e%%:*}" && [ "$st_start" = "${e#*:}" ] || continue
		t="$(secs "${e#*:}")"
		w="$(who "${e%%:*}")"
		case "$w" in
		*" ?") continue ;;
		*runtrimui*) [ -n "$FW_UP" ] || FW_UP="$t" ;;
		esac
		out="$out < $w from $t s"
	done
	[ -n "$out" ] && note "run by: ${out# < }"
}
# This boot's times in one line, in seconds since the kernel started, once
# JukaHub has drawn Home (a minute at most): the firmware's launcher script
# starting, this hook from its start to handing the boot back, the loop
# reaching the menu's place (the stand-in ran there), the queued command
# starting launch.sh, and JukaHub's own start and first frame (its record in
# /tmp). The gaps between them are the stock boot's share; "-" did not happen.
boot_times() {
	i=0
	while [ ! -s "$TIMING" ] && [ "$i" -lt $((60 * PER)) ]; do
		nap
		i=$((i + 1))
	done
	chain
	slot=
	launch=
	id=
	jstart=
	jhome=
	read -r slot _ 2>/dev/null <"$LOCK/t-slot"
	read -r launch _ 2>/dev/null <"$LOCK/t-launch"
	read -r id jstart jhome _ 2>/dev/null <"$TIMING"
	[ "$id" = "$BOOT" ] || { jstart= jhome=; }
	note "boot times (seconds since the kernel started): firmware launcher ${FW_UP:--}, this hook ${HOOK_UP:-?} to ${RET_UP:-?}, menu's place ${slot:--}, launch.sh ${launch:--}, JukaHub ${jstart:--}, Home ${jhome:--}"
	boot_setup
}

started() {
	unshim
	if [ -e "$SKIPPED" ]; then
		say "the firmware started $WHAT in place of the stock menu"
	else
		say "the firmware started JukaHub${1:+ ($1)}"
	fi
	boot_times
	exit 0
}
give_up() {
	[ -e "$RAN" ] && started
	call_off
	say "$*"
	chain
	boot_setup
	exit 0
}
requeue() {
	enqueue
	case $? in
	1) started ;;
	2) give_up "another app was queued to start, so JukaHub is not started" ;;
	3) give_up "could not queue JukaHub in /tmp, so it is not started" ;;
	esac
}

# Wait (two minutes at most) for the loop to run the queue, in which case the
# menu never opens, or for the menu to have been up two seconds and to have
# stopped writing (eight seconds at most). A menu that closes by itself and
# takes the queue with it had an app picked in it.
n=0
held=0
gone=0
seen=
last=
last_io=
while :; do
	[ -e "$RAN" ] && started "before the menu opened"
	[ -n "$(pids JukaHub)" ] && give_up "JukaHub was started some other way"
	state="$(queued)"
	[ "$state" = other ] && give_up "another app was queued to start, so JukaHub is not started"
	ui="$(pids MainUI)"
	if [ -n "$ui" ]; then
		seen=1
		gone=0
		io="$(written $ui)"
		if [ "$ui" = "$last" ]; then
			held=$((held + 1))
			if [ "$held" -ge $((2 * PER)) ]; then
				[ "$io" = "$last_io" ] && break
				[ "$held" -ge $((8 * PER)) ] && break
			fi
		else
			held=0
		fi
		last_io="$io"
	else
		held=0
		if [ -n "$seen" ] && [ "$state" = none ]; then
			gone=$((gone + 1))
			[ "$gone" -ge $((2 * PER)) ] && give_up "the stock menu closed and took the queue with it (an app was picked?), so JukaHub is not started"
		fi
	fi
	last="$ui"
	n=$((n + 1))
	# The stand-in still on after 20 s: the loop has not run MainUI yet, or
	# could not run the stand-in (it would retry without a menu until the give
	# up below). It comes off, and the menu is closed once it is up, as without.
	if [ "$n" -eq $((20 * PER)) ] && shim_on; then
		unshim && say "the firmware did not run the stand-in for $MAINUI within 20 s, so it is taken off"
	fi
	[ "$n" -ge $((120 * PER)) ] && give_up "the stock menu did not appear within two minutes, so JukaHub is not started"
	nap
done
# The real menu is up: the stand-in, if there was one, has had its turn.
unshim

# The menu is up. Close it (it ignores SIGTERM): the loop then runs the queue,
# as when an app is picked in the menu. If the menu comes back instead, it may
# have been opened outside the loop, so there is one more try. What started
# each menu is remembered for the direct start below.
try=0
back=0
seen_parents=
while [ "$try" -lt 2 ]; do
	try=$((try + 1))
	requeue
	ui="$(pids MainUI)"
	killed=
	if [ -n "$ui" ]; then
		from=
		for p in $(parents $ui); do from="$from; $(who "$p")"; done
		seen_parents="$seen_parents $(parents $ui)"
		: >"$MENU" 2>/dev/null
		kill -9 $ui 2>/dev/null
		killed=" $ui "
		say "closed the stock menu (pid ${ui% }${from:+, started by ${from#; }}) so the firmware starts JukaHub (try $try)"
	fi
	n=0
	back=0
	while [ "$n" -lt $((10 * PER)) ]; do
		nap
		n=$((n + 1))
		[ -e "$RAN" ] && started "after the menu was closed"
		[ "$(queued)" = other ] && give_up "another app was queued to start, so JukaHub is not started"
		again=
		for p in $(pids MainUI); do
			case "$killed" in
			*" $p "*) ;;
			*) again="$again $p" ;;
			esac
		done
		if [ -n "$again" ]; then
			back=$((back + 1))
			[ "$back" -ge $((2 * PER)) ] && break
		else
			back=0
		fi
	done
	# Neither the queue nor the menu came back: start it below.
	[ "$back" -ge $((2 * PER)) ] || break
	# The loop that opened the menu opened it again and left the queue as it
	# was: it ignores the queue after a kill, and one more try would only show
	# the menu a third time. A menu from somewhere else, or a queue the loop
	# did run (an app picked at the last moment took its place), is retried.
	same=
	[ "$(queued)" = ours ] && same=1
	for p in $(parents $again); do
		case " $seen_parents " in
		*" $p "*) ;;
		*) same= ;;
		esac
	done
	[ -n "$same" ] && break
done

# The loop did not run the queue. Start JukaHub here the way launch.sh is run
# by hand, but only with whatever reopens the menu paused: the launcher loops
# behind it that are shell scripts. When something else restarts it (init, a
# daemon), a start here would put two UIs on the screen, so there is none.
# Everything is checked again once the loops are paused, including that none
# of them is already starting an app, and mkdir claims the start, as the
# queued command does. The loops go on once JukaHub has exited, and also when
# this is told to stop meanwhile.
restarters="$(alive $seen_parents $(parents $(pids MainUI)))"
paused="$(shells $restarters)"
loose=
for p in $restarters; do
	case " $paused " in
	*" $p "*) ;;
	*) loose="$loose $p" ;;
	esac
done
[ -n "$loose" ] && give_up "the stock menu is started by something that cannot be paused (pid$loose), so JukaHub is not started"
[ -n "$(pids MainUI)" ] && [ -z "$paused" ] && give_up "the stock menu keeps coming back, so JukaHub is not started"
resume() {
	[ -n "$paused" ] && kill -CONT $paused 2>/dev/null
}
trap 'resume; withdraw' TERM INT
[ -n "$paused" ] && kill -STOP $paused 2>/dev/null
record $paused
[ -e "$RAN" ] && { resume; started; }
[ -n "$(pids JukaHub)" ] && { resume; give_up "JukaHub was started some other way"; }
[ "$(queued)" = other ] && { resume; give_up "another app was queued to start, so JukaHub is not started"; }
[ -n "$paused" ] && busy $paused && { resume; give_up "the launcher was already starting another app, so JukaHub is not started"; }
mkdir "$RAN" 2>/dev/null || { resume; started; }
[ "$(queued)" = ours ] && rm -f "$Q"
trap 'rm -f "$PENDING"; resume; say "JukaHub exited (the hook was told to stop)"; exit 0' TERM INT
pw=
for p in $paused; do pw="$pw; $(who "$p")"; done
say "the firmware did not run the queue, so JukaHub is started directly${pw:+ (paused the launcher loop: ${pw#; })}"
up=
read -r up _ 2>/dev/null </proc/uptime && printf '%s\n' "$up" >"$LOCK/t-launch" 2>/dev/null
/bin/sh "$APP_DIR/launch.sh" >>"$LOG" 2>&1 || :
rm -f "$PENDING"
resume
say "JukaHub exited"
boot_times
) </dev/null >/dev/null 2>&1 &
) </dev/null >/dev/null 2>&1
`

// trimuiAutostartScript is the boot hook for an app installed in appDir. A
// folder name with a control character is refused: the path sits inside a
// here-document and the hook's log lines, which a line break would cut short.
func trimuiAutostartScript(appDir string) (string, error) {
	if strings.TrimSpace(appDir) == "" {
		return "", fmt.Errorf("cannot locate the JukaHub folder")
	}
	if strings.IndexFunc(appDir, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("the JukaHub folder name contains a control character")
	}
	base := strings.TrimRight(appDir, "/") + "/"
	return strings.NewReplacer(
		"@APP_DIR@", shellQuote(appDir),
		"@LAUNCH@", shellQuote(base+"launch.sh"),
		"@PENDING@", shellQuote(base+trimuiAutostartPendingFile),
	).Replace(trimuiAutostartTemplate), nil
}

// shellQuote wraps a path for a POSIX shell. Single quotes are the only form
// every sh understands, and a literal quote is closed and escaped around.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeTrimuiHook puts the boot hook on the card so that the firmware never
// finds half of it: a cut-off hook would be a syntax error, and a firmware
// that sources its boot scripts stops at one. The firmware may run every file
// in System/starts, so the new text is staged one folder up, in <SD>/System —
// the same card, so the rename is atomic — and no backup copy is kept beside
// the hook either. On any failure the hook on the card stays as it was.
func writeTrimuiHook(path string, data []byte) error {
	starts := filepath.Dir(path)
	if err := os.MkdirAll(starts, 0o755); err != nil {
		return err
	}
	if err := renameIntoPlace(filepath.Join(filepath.Dir(starts), trimuiAutostartStagingFile), path, data); err != nil {
		return err
	}
	// Executable for a firmware that checks. A FAT card shows every file as
	// executable and may refuse the call, which is fine.
	_ = os.Chmod(path, 0o755)
	syncDirectory(starts)
	return nil
}

// renameIntoPlace writes data to tmp, flushes it and renames it onto path. On
// any failure tmp is removed and path is left as it was.
func renameIntoPlace(tmp, path string, data []byte) error {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// defaultAutostartDir returns the platform's per-user startup directory.
func defaultAutostartDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), nil
	case "linux", "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "autostart"), nil
	}
	return "", fmt.Errorf("autostart is not supported on %s", runtime.GOOS)
}

// autostartSupported reports whether this platform has a startup location the
// app can write to. On the handheld that is the SD card's boot script folder;
// if the card cannot be located the setting honestly reports itself unusable
// rather than writing somewhere arbitrary.
func autostartSupported() bool {
	if jukaosNativeActive() {
		return false // plastron's init already starts JukaHub on every boot
	}
	if autostartIsTrimui() {
		return strings.TrimSpace(trimuiSDRootFn()) != ""
	}
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
		return true
	}
	return false
}

// autoStartEnabled reads the persisted setting (default off).
func autoStartEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, "autoStart"); ok {
		return coerceBool(v)
	}
	return false
}

// autoStartLauncherPath returns the launcher file for this platform.
func autoStartLauncherPath() (string, error) {
	if autostartIsTrimui() {
		return trimuiAutostartPath()
	}
	return autoStartLauncherPathFor(runtime.GOOS)
}

// autoStartLauncherPathFor is the platform-parameterised form, so both formats
// stay testable from any host.
func autoStartLauncherPathFor(goos string) (string, error) {
	dir, err := autostartDirFn()
	if err != nil {
		return "", err
	}
	name := autostartWindowsFile
	if goos != "windows" {
		name = autostartLinuxFile
	}
	return filepath.Join(dir, name), nil
}

// autoStartLauncher writes what the platform starts at login.
func autoStartLauncher(exe string) string {
	return autoStartLauncherFor(runtime.GOOS, exe)
}

// autoStartLauncherFor builds the launcher for a named OS.
func autoStartLauncherFor(goos, exe string) string {
	// --boot asks for the full power-on boot (bootos.go): a start at login is
	// the machine starting into JukaHub, not an app being opened.
	if goos == "windows" {
		// `start "" "<exe>"` keeps the console window from sticking around.
		return "@echo off\r\nstart \"\" " + quoteWindowsArg(exe) + " --boot\r\n"
	}
	// Freedesktop autostart entry (GNOME/KDE/XFCE and the TrimUI desktop all
	// honour this file).
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=JukaHub\n" +
		"Comment=Launch JukaHub at login\n" +
		"Exec=" + quoteDesktopExecArg(exe) + " --boot\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// quoteDesktopExecArg quotes a path for the Exec key of a .desktop file. The
// freedesktop spec understands double quotes only (a single-quoted argument is
// not shell-parsed), and inside them " ` $ \ have to be escaped. A literal %
// must be doubled, otherwise it is read as a field code.
func quoteDesktopExecArg(p string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setAutoStart installs or removes the startup launcher. It reports a friendly
// error the settings toggle can show without leaving the switch in the wrong
// state.
func setAutoStart(on bool) error {
	if !autostartSupported() {
		return fmt.Errorf("this device manages startup apps itself")
	}
	if handled, err := setPlatformAutoStart(on); handled {
		return err
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if autostartIsTrimui() {
			// A start the hook made earlier no longer matters.
			if appDir, err := trimuiAppDir(); err == nil {
				_ = os.Remove(filepath.Join(appDir, trimuiAutostartPendingFile))
			}
		}
		return nil
	}
	exe, err := autostartExeFn()
	if err != nil {
		return err
	}
	if strings.TrimSpace(exe) == "" {
		return fmt.Errorf("cannot locate the JukaHub executable")
	}
	if autostartIsTrimui() {
		script, err := trimuiAutostartScript(filepath.Dir(exe))
		if err != nil {
			return err
		}
		return writeTrimuiHook(path, []byte(script))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(autoStartLauncher(exe)), 0o644)
}

// autoStartInstalled reports whether the launcher exists on disk, which is the
// ground truth the setting is reconciled against at startup.
func autoStartInstalled() bool {
	if handled, installed := platformAutoStartInstalled(); handled {
		return installed
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// autoStartStaleLauncher reports a launcher from the old Windows .cmd format
// that must be replaced by the Run value (or removed when the setting is off).
func autoStartStaleLauncher() bool {
	if handled, stale := platformAutoStartStale(); handled {
		return stale
	}
	return false
}

// autoStartLocation is what to show for JukaHub's own entry on the Scheduled
// tasks screen: the Windows Run value, or the launcher file elsewhere.
func autoStartLocation() (command, source string, err error) {
	if command, source, handled := platformAutoStartCommand(); handled {
		return command, source, nil
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		return "", "", err
	}
	return path, filepath.Dir(path), nil
}

// trimuiAutostartHookStale reports whether the boot hook on the card is not
// this build's (an older hook, or a hand edit), so that reconcileAutoStart
// replaces it: a card that still carries the first hook would otherwise keep
// failing at every boot. A hook that cannot be read is left alone, since
// writing it would fail too.
func trimuiAutostartHookStale() bool {
	path, err := trimuiAutostartPath()
	if err != nil {
		return false
	}
	appDir, err := trimuiAppDir()
	if err != nil {
		return false
	}
	want, err := trimuiAutostartScript(appDir)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Size() != int64(len(want)) {
		return true
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(got) != want
}

// trimuiAutostartBootNote says, for JukaHub's log (errors.txt on the
// handheld), what the boot hook did at this boot, so a report from a device
// that did not boot into JukaHub says why: the hook's last line, or — when its
// lock is missing — that the firmware never ran it, and whether the firmware's
// launcher loop mentions the System/starts folder at all. Empty when there is
// nothing to say.
func trimuiAutostartBootNote() string {
	appDir, err := trimuiAppDir()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(trimuiAutostartLockDir); err != nil {
		mentions := "cannot be read"
		if data, err := readFileHead(trimuiLauncherScript, 64<<10); err == nil {
			mentions = "does not mention System/starts"
			if strings.Contains(string(data), "System/starts") {
				mentions = "mentions System/starts"
			}
		}
		return fmt.Sprintf("the boot hook did not run at this boot (%s %s)", trimuiLauncherScript, mentions)
	}
	if line := lastLogLine(filepath.Join(appDir, trimuiAutostartLogFile)); line != "" {
		return "boot hook: " + line
	}
	return ""
}

// trimuiAutostartMenuMarker is left in the boot hook's lock the moment the
// hook closes the stock menu (trimuiAutostartTemplate).
const trimuiAutostartMenuMarker = "menu"

// trimuiAutostartMenuShown reports whether the boot hook had to close the
// stock menu at this boot: the firmware's boot logo is then no longer on the
// screen when JukaHub starts — the queued command left black there — so the
// power-on boot fades in instead of continuing from the logo (bootos.go).
func trimuiAutostartMenuShown() bool {
	_, err := os.Stat(filepath.Join(trimuiAutostartLockDir, trimuiAutostartMenuMarker))
	return err == nil
}

// readFileHead reads at most limit bytes of a file.
func readFileHead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// lastLogLine returns the last non-empty line of a log, read from its last
// 16 KiB, without the timestamp prefix the boot hook writes. Indented lines
// are the hook's notes (its boot times, the firmware's boot setup), not a
// step it took, and are skipped.
func lastLogLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 16 << 10
	start := info.Size() - tail
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	n, _ := f.ReadAt(buf, start)
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf[:n]), "\r\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // read from the middle of a line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		raw := lines[i]
		if raw != "" && (raw[0] == ' ' || raw[0] == '\t') {
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "runtrimui.sh:") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if end := strings.Index(line, "] "); end >= 0 {
				line = line[end+2:]
			}
		}
		if len(line) > 200 {
			// Cut on a character boundary.
			cut := 200
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			line = line[:cut]
		}
		return line
	}
	return ""
}

// autoStartSettleDelay is how long JukaHub's main loop has to have run before
// a start made by the boot hook counts as having worked.
const autoStartSettleDelay = 15 * time.Second

// The boot hook's pending marker is cleared once, after autoStartSettleDelay.
// Main goroutine only (widgetsTick).
var (
	autoStartSettleSince time.Time
	autoStartSettled     bool
)

// autoStartBootSettleTick runs from widgetsTick: once JukaHub has been up for
// autoStartSettleDelay it deletes the boot hook's pending marker, so the next
// boot starts JukaHub again. A JukaHub that hangs before that leaves the
// marker, and the next boot stays on the stock menu instead. Leaving JukaHub
// (quit, restart, power off) clears it too, through a Cleanup hook: a JukaHub
// that exits hands the device back, it did not lock it up.
func autoStartBootSettleTick() { autoStartBootSettleAt(time.Now()) }

// autoStartBootSettleAt is autoStartBootSettleTick with the clock passed in.
func autoStartBootSettleAt(now time.Time) {
	if autoStartSettled {
		return
	}
	if autoStartSettleSince.IsZero() {
		autoStartSettleSince = now
		Cleanup = append(Cleanup, autoStartClearPending)
		return
	}
	if now.Sub(autoStartSettleSince) < autoStartSettleDelay {
		return
	}
	autoStartSettled = true
	autoStartClearPending()
}

// autoStartClearPending deletes the boot hook's pending marker, so the next
// boot starts JukaHub again.
func autoStartClearPending() {
	if !autostartIsTrimui() {
		return
	}
	appDir, err := trimuiAppDir()
	if err != nil {
		return
	}
	if err := os.Remove(filepath.Join(appDir, trimuiAutostartPendingFile)); err == nil {
		logPatch("autostart: JukaHub got going, so the next boot starts it again")
	}
}

// autoStartSkipMenuKey is the Custom setting behind Settings → General → Skip
// the stock menu at boot (TrimUI): on unless switched off.
const autoStartSkipMenuKey = "autoStartSkipMenu"

// autoStartSkipMenuOn reads it: at power-on the boot hook starts JukaHub in
// place of the stock menu (its fast_boot), in OS mode and as the app alike.
func autoStartSkipMenuOn(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, autoStartSkipMenuKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// autoStartSkipMenuVisible shows the switch where the TrimUI boot hook runs:
// stock firmware, not JukaHub OS's own images (no stock menu there).
func autoStartSkipMenuVisible(*Config) bool {
	return autostartIsTrimui() && !jukaosNativeActive()
}

// autoStartSkipMenuDetail is the line under the switch.
func autoStartSkipMenuDetail(c *Config) string {
	switch {
	case !autoStartEnabled(c):
		return "Turn Start JukaHub at login on to use it"
	case autoStartSkipMenuOn(c):
		return "At power-on the firmware starts JukaHub in place of its menu, so the menu never shows"
	}
	return "The stock menu opens for a moment at power-on and is then closed. Try this if Wi-Fi, brightness or volume are not set up after a boot"
}

// autoStartSetSkipMenu is the switch: the choice is stored at once and the
// hook's marker follows on a goroutine (the card is slow).
func autoStartSetSkipMenu(c *Config, on bool) {
	setStoreBool(c, autoStartSkipMenuKey, on)
	trimuiKeepMenuApply(!on, true)
	if on {
		setToast("From the next power-on JukaHub starts without the stock menu")
	} else {
		setToast("From the next power-on the stock menu opens first and is then closed")
	}
}

// trimuiKeepMenu serialises the marker's writes; want is the newest choice, so
// two quick flips of the switch can never land in the wrong order.
var trimuiKeepMenu struct {
	mu   sync.Mutex
	want atomic.Bool
}

// trimuiKeepMenuText is what the marker says to someone who finds it.
const trimuiKeepMenuText = "Written by JukaHub: Settings > General > Skip the stock menu at boot is off,\n" +
	"so the stock menu opens first at power-on. The switch writes and removes this file.\n"

// trimuiKeepMenuApply leaves the hook's marker in the app folder (keep) or
// takes it away, on a goroutine when async.
func trimuiKeepMenuApply(keep, async bool) {
	trimuiKeepMenu.want.Store(keep)
	if async {
		go trimuiKeepMenuSync()
		return
	}
	trimuiKeepMenuSync()
}

// trimuiKeepMenuSync makes the marker match the newest choice. The folder is
// flushed after a change: the hook reads the marker at the very next boot.
func trimuiKeepMenuSync() {
	trimuiKeepMenu.mu.Lock()
	defer trimuiKeepMenu.mu.Unlock()
	appDir, err := trimuiAppDir()
	if err != nil {
		return
	}
	path := filepath.Join(appDir, trimuiAutostartKeepMenuFile)
	keep := trimuiKeepMenu.want.Load()
	_, statErr := os.Stat(path)
	switch {
	case keep && statErr != nil:
		if err := os.WriteFile(path, []byte(trimuiKeepMenuText), 0o644); err != nil {
			logPatch("autostart: could not keep the stock menu at boot (%v)", err)
			return
		}
	case !keep && statErr == nil:
		if err := os.Remove(path); err != nil {
			logPatch("autostart: could not stop keeping the stock menu at boot (%v)", err)
			return
		}
	default:
		return
	}
	syncDirectory(appDir)
}

// trimuiOSEditionCard reports whether JukaHub on this card was installed from
// the OS package (os/os.json: edition "os") with OS mode on.
func trimuiOSEditionCard() bool {
	appDir, err := trimuiAppDir()
	if err != nil {
		return false
	}
	return jukaosLoadState(appDir).jukaosOSEdition()
}

// reconcileAutoStart applies the saved setting on startup: if the user enabled
// it and the launcher is missing (new install, moved SD card), it is written
// again; if the user disabled it, a leftover launcher is removed. On a TrimUI
// a hook written by another build is replaced with this build's, and what the
// hook did at this boot goes to the log.
func reconcileAutoStart(config *Config) {
	if !autostartSupported() {
		return
	}
	// A TrimUI's first start switches it on (autostart_firstrun.go) and says
	// so once: in the tour's own step when the tour gets that far, otherwise
	// in a toast once the tour is out of the way (it can be skipped early).
	if applyAutoStartFirstRunDefault(config) {
		autoStartNoticePending = true
	}
	// The hook's KEEP_MENU marker follows Skip the stock menu at boot (a
	// restored backup, a card moved between devices): one stat when it agrees.
	if autostartIsTrimui() {
		trimuiKeepMenuApply(!autoStartSkipMenuOn(config), false)
	}
	want := autoStartEnabled(config)
	installed := autoStartInstalled()
	if autoStartStaleLauncher() {
		if err := setAutoStart(want); err != nil {
			logPatch("autostart: could not replace the old launcher (%v)", err)
		} else {
			installed = autoStartInstalled()
		}
	}
	if !want && installed && autostartIsTrimui() && trimuiOSEditionCard() {
		// The OS package put the boot hook on the card, over settings that
		// had "Start JukaHub at login" off (an app install that was there
		// before). Removing the hook here undid the install: the next boot was
		// the stock menu again. The hook is the newer choice; turning Boot
		// into JukaHub OS off removes it, and that sticks.
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom["autoStart"] = boolString(true)
		saveUserConfigDebounced(userConfigFromVariables(config))
		logPatch("autostart: JukaHub OS package: kept its boot hook and switched Start JukaHub at login on (it was off in this card's settings)")
		want = true
	}
	if want && installed && autostartIsTrimui() {
		if trimuiAutostartHookStale() {
			// Another build's hook ran at this boot (an older one never left
			// the lock the report looks for), so there is no report to make.
			if err := setAutoStart(true); err != nil {
				logPatch("autostart: could not replace the boot hook written by another build (%v)", err)
			} else {
				logPatch("autostart: replaced the boot hook written by another build")
			}
			return
		}
		if note := trimuiAutostartBootNote(); note != "" {
			logPatch("autostart: %s", note)
		}
		return
	}
	if want == installed {
		return
	}
	if err := setAutoStart(want); err != nil {
		logPatch("autostart: could not reconcile (%v)", err)
	}
}
