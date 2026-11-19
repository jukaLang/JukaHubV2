#!/bin/sh
# JukaHub OS's own Smart Pro launcher. /jukahub/init starts it once the card
# is mounted and powers the device off (or restarts it) when it returns.
# jukaboot, the compiled process 1 (player/cmd/jukaboot/launch_linux.go), runs
# the same loop in Go instead: a change here belongs there too.
# jukahub-restart-protocol: 1
# jukahub-restart-now: 1
# jukahub-screen-handoff: 1
CARD=/mnt/SDCARD
APP=$CARD/Apps/JukaHub
# The folder JukaHub and this launcher share (USERDATA_PATH below): boot.env,
# boot.log and the boot frame. player/jukaos_native.go uses the same path.
DATA=$CARD/.userdata/jukahub
LOG=$DATA/boot.log

read -r LAUNCH_UP REST < /proc/uptime
# A fork only where it is needed: the folders exist after the first boot.
[ -d "$DATA/logs" ] || mkdir -p "$DATA" "$DATA/logs" || exit 1
[ -f "$LOG" ] && mv -f "$LOG" "$LOG.1"
: > "$LOG"

# The size of a file in bytes (busybox wc may pad the number).
file_size() {
	SIZE=$(wc -c < "$1" 2> /dev/null)
	SIZE=${SIZE##* }
}

# sysfs_word FILE: its first line, or nothing.
sysfs_word() {
	WORD=""
	[ -r "$1" ] && read -r WORD < "$1"
}

# show_bootframe: the Home JukaHub kept at its last power-off (bootframe.go),
# on the panel at once, over the boot logo. Only when the saved picture has
# exactly the panel's geometry; anything else leaves the logo alone. (The
# same function as os/plastron/launch.sh, where it also stops the splash.)
show_bootframe() {
	FB=/sys/class/graphics/fb0
	BF=$DATA/bootframe.fb
	[ -f "$DATA/bootframe.info" ] && [ -f "$BF" ] && [ -c /dev/fb0 ] || return 1
	read -r BF_W BF_H BF_BPP BF_STRIDE BF_REST < "$DATA/bootframe.info" || return 1
	# Plain numbers only: a leading 0 (octal) or a huge one would stop the
	# shell's arithmetic below, and with it this launcher, before JukaHub.
	case ":$BF_W:$BF_H:$BF_BPP:$BF_STRIDE:" in
		*[!0-9:]* | *::* | *:0* | *[0-9][0-9][0-9][0-9][0-9][0-9][0-9]*) return 1 ;;
	esac
	sysfs_word "$FB/virtual_size"
	VW=${WORD%%,*}
	VH=${WORD##*,}
	case ":$VW:$VH:" in
		*[!0-9:]* | *::* | *:0* | *[0-9][0-9][0-9][0-9][0-9][0-9][0-9]*) return 1 ;;
	esac
	sysfs_word "$FB/bits_per_pixel"
	[ "$WORD" = "$BF_BPP" ] || return 1
	sysfs_word "$FB/stride"
	[ "$WORD" = "$BF_STRIDE" ] || return 1
	[ "$VW" = "$BF_W" ] && [ "$BF_H" -gt 0 ] && [ "$VH" -ge "$BF_H" ] && [ $((VH % BF_H)) -eq 0 ] || return 1
	# A mode line, where the driver gives one, names the visible size.
	sysfs_word "$FB/modes"
	case "$WORD" in
		"" | *"${BF_W}x${BF_H}"*) ;;
		*) return 1 ;;
	esac
	file_size "$BF"
	[ "$SIZE" = $((BF_STRIDE * BF_H)) ] || return 1
	[ -w /sys/class/graphics/fbcon/cursor_blink ] && echo 0 > /sys/class/graphics/fbcon/cursor_blink 2> /dev/null
	# Every page of the buffer, so whichever one is panned to shows it.
	PAGES=$((VH / BF_H))
	i=0
	{
		while [ "$i" -lt "$PAGES" ]; do
			cat "$BF"
			i=$((i + 1))
		done
	} > /dev/fb0 2> /dev/null
	return 0
}

BOOTFRAME=0
show_bootframe && BOOTFRAME=1
read -r FRAME_UP REST < /proc/uptime
echo "launcher at ${LAUNCH_UP}s since boot" >> "$LOG"
[ "$BOOTFRAME" = 1 ] && echo "boot frame on screen at ${FRAME_UP}s" >> "$LOG"
# The init starts this launcher while the drivers still load (it names the
# marker it writes when they are in): a display driver that is a module is not
# up yet, and the boot frame waits for no one.
if [ "$BOOTFRAME" = 0 ] && [ -f "$DATA/bootframe.info" ] && [ ! -c /dev/fb0 ]; then
	echo "boot frame skipped: no /dev/fb0 yet (the display driver is still loading)" >> "$LOG"
fi

# Games and RetroArch get the stock system's environment; JukaHub's bundled
# libraries and helpers apply only to JukaHub itself.
JUKAHUB_ORIG_ENV="HOME PATH LD_LIBRARY_PATH"
[ "${HOME+x}" ] && JUKAHUB_ORIG_HOME=$HOME && export JUKAHUB_ORIG_HOME
[ "${PATH+x}" ] && JUKAHUB_ORIG_PATH=$PATH && export JUKAHUB_ORIG_PATH
[ "${LD_LIBRARY_PATH+x}" ] && JUKAHUB_ORIG_LD_LIBRARY_PATH=$LD_LIBRARY_PATH && export JUKAHUB_ORIG_LD_LIBRARY_PATH
export JUKAHUB_ORIG_ENV
# The stock SDL2 in /usr/lib draws through the PowerVR's fbdev EGL ("mali"
# backend), exactly as when JukaHub runs from the stock menu.
LD_LIBRARY_PATH="$APP:$APP/libs:/usr/trimui/lib:/usr/lib:/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
PATH="$APP:$APP/required:$PATH"
export LD_LIBRARY_PATH PATH
export HOME=$DATA
export JUKAHUB_LAUNCHED_BY=launch.sh
export JUKAHUB_SCREEN_HANDOFF=1
export JUKAHUB_DEVICE_MODEL=trimui-smart-pro
# 1: the panel already shows Home (the boot frame); JukaHub's first frame
# replaces the same picture.
export JUKAHUB_BOOTFRAME=$BOOTFRAME
export CLR_OPENSSL_VERSION_OVERRIDE=1.1
export BIOS_PATH=$CARD/Bios
export ROMS_PATH=$CARD/Roms
export SAVES_PATH=$CARD/Saves
export USERDATA_PATH=$DATA

# The zone chosen in Settings -> Date & time (same rule as player/launch.sh:
# a plain IANA name only, at most 64 characters).
if [ -f "$APP/jukatimezone" ]; then
	JUKA_TZ=$(dd if="$APP/jukatimezone" bs=128 count=1 2> /dev/null | head -n 1 | tr -d '\r\n')
	case "$JUKA_TZ" in
		"" | /* | */ | *..* | *[!A-Za-z0-9_+/-]*) ;;
		*) [ "${#JUKA_TZ}" -le 64 ] && TZ=$JUKA_TZ && export TZ ;;
	esac
fi

uptime_s() {
	read -r UP REST < /proc/uptime
	NOW=${UP%%.*}
}

FAILS=0
EARLY_RETRIES=0
while :; do
	if [ ! -x "$APP/JukaHub" ]; then
		echo "JukaHub is missing from $APP" >> "$LOG"
		break
	fi
	read -r UP REST < /proc/uptime
	START=${UP%%.*}
	echo "JukaHub starting at ${UP}s since boot" >> "$LOG"
	# Started before the drivers were in (JUKAHUB_WAIT_DISPLAY's marker not
	# there yet)?
	EARLY=0
	[ -n "${JUKAHUB_WAIT_DISPLAY:-}" ] && [ ! -e "$JUKAHUB_WAIT_DISPLAY" ] && EARLY=1
	cd "$APP" || break
	./JukaHub >> "$LOG" 2>&1 &
	CHILD=$!
	# Only the first start of a boot finds the boot frame on the panel.
	JUKAHUB_BOOTFRAME=0
	wait "$CHILD"
	STATUS=$?
	printf '%s %s\n' "$STATUS" "$CHILD" > "$APP/.last-exit"
	# A JukaHub from before the display wait opens the display at once and
	# fails while the drivers still load (a newer one waits; one whose wait
	# timed out fails the same way). That start does not count as a failure:
	# the next one waits for the drivers' marker, 10 s at most (twice a boot
	# at most, so a marker that never comes ends in the usual failure rule).
	if [ "$STATUS" -ne 0 ] && [ "$EARLY" = 1 ] && [ "$EARLY_RETRIES" -lt 2 ]; then
		EARLY_RETRIES=$((EARLY_RETRIES + 1))
		echo "JukaHub exited with status $STATUS while the drivers were loading; starting it again once they are in" >> "$LOG"
		i=0
		while [ "$i" -lt 100 ] && [ ! -e "$JUKAHUB_WAIT_DISPLAY" ]; do
			# Tenths of a second; a sleep without fractions counts ten.
			if sleep 0.1 2> /dev/null; then i=$((i + 1)); else sleep 1; i=$((i + 10)); fi
		done
		continue
	fi
	if [ -f "$APP/.screen-handoff" ]; then
		read -r HANDOFF_PID < "$APP/.screen-handoff"
		rm -f "$APP/.screen-handoff"
		case "$HANDOFF_PID" in
			''|*[!0-9]*) ;;
			*) while [ -d "/proc/$HANDOFF_PID" ]; do sleep 1; done ;;
		esac
		FAILS=0
		continue
	fi
	if [ -f "$APP/.restart-pending" ] || [ -f "$APP/.restart-now" ]; then
		rm -f "$APP/.restart-pending" "$APP/.restart-now"
		FAILS=0
		continue
	fi
	[ "$STATUS" -eq 0 ] && break
	uptime_s; END=$NOW
	if [ $((END - START)) -lt 5 ]; then
		FAILS=$((FAILS + 1))
	else
		FAILS=0
	fi
	echo "JukaHub exited with status $STATUS ($FAILS quick failures)" >> "$LOG"
	[ "$FAILS" -ge 3 ] && break
	sleep 1
done
sync
