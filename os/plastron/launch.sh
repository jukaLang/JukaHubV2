#!/bin/sh
# JukaHub OS's own Brick launcher. plastron starts it after mounting the card.
# jukahub-restart-protocol: 1
# jukahub-restart-now: 1
# jukahub-screen-handoff: 1
CARD=/mnt/SDCARD
APP=$CARD/Apps/JukaHub
# The folder JukaHub and this launcher share (USERDATA_PATH below): boot.env,
# boot.log and the boot frame. player/jukaos_native.go uses the same path.
DATA=$CARD/.userdata/jukahub
LOG=$DATA/boot.log
# JukaHub writes "<uptime seconds>" here once its first frame is up.
FIRST_FRAME=/tmp/jukahub-first-frame

read -r LAUNCH_UP REST < /proc/uptime
# Everything before JukaHub's start is on the boot's path: a fork only where
# it is needed (the folders exist after the first boot).
[ -d "$DATA/logs" ] || mkdir -p "$DATA" "$DATA/logs" || exit 1
[ -f "$LOG" ] && mv -f "$LOG" "$LOG.1"
: > "$LOG"
CR=$(printf '\r')

# Radios start once JukaHub's first frame is up, off its critical path. JukaHub
# keeps WIFI= and BLUETOOTH= in boot.env as the player last switched them
# (jukaos_native.go); the file may also be edited by hand. READAHEAD_KB is the
# card's read-ahead (card_readahead; 0 leaves the kernel's).
WIFI=0
BLUETOOTH=0
READAHEAD_KB=1024
if [ -f "$DATA/boot.env" ]; then
	while IFS='=' read -r KEY VALUE; do
		# The file may have been edited on Windows.
		KEY=${KEY%"$CR"}
		VALUE=${VALUE%"$CR"}
		case "$KEY:$VALUE" in
			WIFI:0|WIFI:1) WIFI=$VALUE ;;
			BLUETOOTH:0|BLUETOOTH:1) BLUETOOTH=$VALUE ;;
			READAHEAD_KB:0) READAHEAD_KB=0 ;;
			READAHEAD_KB:[1-9]|READAHEAD_KB:[1-9][0-9]|READAHEAD_KB:[1-9][0-9][0-9]|READAHEAD_KB:[1-9][0-9][0-9][0-9]) READAHEAD_KB=$VALUE ;;
		esac
	done < "$DATA/boot.env"
fi

# card_readahead: a larger read-ahead for the SD card's disk, only upwards and
# only for the disk the card's data partition is on (an SD card by sysfs, never
# the internal eMMC). JukaHub's binary is tens of megabytes read by page
# faults; each fault then reads a bigger piece around it in one card request
# instead of several (the card's cost is per request, not per byte), and the
# boot frame below is one long read. Says what it did in RA_NOTE.
card_readahead() {
	RA_NOTE=""
	case "$READAHEAD_KB" in "" | 0 | *[!0-9]*) return 0 ;; esac
	CARD_DEV=""
	while read -r M_DEV M_DIR M_REST; do
		[ "$M_DIR" = "$CARD" ] && CARD_DEV=$M_DEV
	done < /proc/mounts
	disk=${CARD_DEV#/dev/}
	case "$disk" in
		mmcblk[0-9]*p[0-9]*) disk=${disk%p*} ;;
		*) return 0 ;;
	esac
	kind=""
	read -r kind 2> /dev/null < "/sys/block/$disk/device/type"
	[ "$kind" = SD ] || return 0
	q=/sys/block/$disk/queue/read_ahead_kb
	[ -w "$q" ] || return 0
	cur=""
	read -r cur 2> /dev/null < "$q"
	case "$cur" in "" | *[!0-9]*) return 0 ;; esac
	[ "$cur" -lt "$READAHEAD_KB" ] || return 0
	echo "$READAHEAD_KB" > "$q" 2> /dev/null && RA_NOTE="read-ahead of $disk ${cur} -> ${READAHEAD_KB} KB"
}
card_readahead

uptime_s() {
	read -r UP REST < /proc/uptime
	NOW=${UP%%.*}
}

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
# on the panel at once, in place of the splash. Only when the saved picture
# has exactly the panel's geometry; anything else leaves the splash alone.
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
	# plastron's splash leaves first, so it cannot draw over the picture.
	if pidof splash > /dev/null 2>&1; then
		killall splash 2> /dev/null
		i=0
		while [ "$i" -lt 10 ] && pidof splash > /dev/null 2>&1; do
			sleep 0.05
			i=$((i + 1))
		done
		pidof splash > /dev/null 2>&1 && killall -KILL splash 2> /dev/null
	fi
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
	[ -x /usr/lib/brick/backlight ] && /usr/lib/brick/backlight on
	return 0
}

# log_bootstage: U-Boot's own timings (plastron builds U-Boot with bootstage
# and hands them to the kernel in the device tree), microseconds since reset.
log_bootstage() {
	BS=/proc/device-tree/bootstage
	[ -d "$BS" ] || return 0
	for n in $(ls "$BS" 2> /dev/null | sort -n); do
		[ -f "$BS/$n/name" ] || continue
		NAME=$(tr -d '\000' < "$BS/$n/name")
		for KIND in mark accum; do
			[ -f "$BS/$n/$KIND" ] || continue
			HEX=$(od -An -v -tx1 "$BS/$n/$KIND" 2> /dev/null | tr -d ' \n')
			case "$HEX" in
				????????) echo "bootstage $NAME $KIND $((0x$HEX)) us" >> "$LOG" ;;
				*) echo "bootstage $NAME $KIND" >> "$LOG" ;;
			esac
		done
	done
}

BOOTFRAME=0
show_bootframe && BOOTFRAME=1
read -r FRAME_UP REST < /proc/uptime
echo "launcher at ${LAUNCH_UP}s since boot" >> "$LOG"
[ -n "$RA_NOTE" ] && echo "$RA_NOTE" >> "$LOG"
[ "$BOOTFRAME" = 1 ] && echo "boot frame on screen at ${FRAME_UP}s" >> "$LOG"

# Keep the clean plastron environment for games and RetroArch. JukaHub's
# bundled libraries and helpers apply only to JukaHub itself.
JUKAHUB_ORIG_ENV="HOME PATH LD_LIBRARY_PATH"
[ "${HOME+x}" ] && JUKAHUB_ORIG_HOME=$HOME && export JUKAHUB_ORIG_HOME
[ "${PATH+x}" ] && JUKAHUB_ORIG_PATH=$PATH && export JUKAHUB_ORIG_PATH
[ "${LD_LIBRARY_PATH+x}" ] && JUKAHUB_ORIG_LD_LIBRARY_PATH=$LD_LIBRARY_PATH && export JUKAHUB_ORIG_LD_LIBRARY_PATH
export JUKAHUB_ORIG_ENV
LD_LIBRARY_PATH="$APP:$APP/libs:/usr/lib:/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
PATH="$APP:$APP/required:$PATH"
export LD_LIBRARY_PATH PATH
export HOME=$DATA
export SDL_VIDEODRIVER=pvrfb
export SDL_RENDER_DRIVER=opengles2
export JUKAHUB_LAUNCHED_BY=launch.sh
export JUKAHUB_SCREEN_HANDOFF=1
export JUKAHUB_DEVICE_MODEL=trimui-brick
# 1: the panel already shows Home (the boot frame); JukaHub's first frame
# replaces the same picture.
export JUKAHUB_BOOTFRAME=$BOOTFRAME
export BIOS_PATH=$CARD/Bios
export ROMS_PATH=$CARD/Roms
export SAVES_PATH=$CARD/Saves
export USERDATA_PATH=$DATA

# After the first frame: U-Boot's timings for the log (forty-odd small forks,
# so not before JukaHub starts), then the radios as boot.env says (read above).
after_first_frame() {
	# plastron's services get plastron's environment, not JukaHub's libraries.
	if [ "${JUKAHUB_ORIG_LD_LIBRARY_PATH+x}" ]; then
		LD_LIBRARY_PATH=$JUKAHUB_ORIG_LD_LIBRARY_PATH
	else
		unset LD_LIBRARY_PATH
	fi
	[ "${JUKAHUB_ORIG_PATH+x}" ] && PATH=$JUKAHUB_ORIG_PATH
	# Twenty seconds at most: a JukaHub that never draws still gets its radios.
	i=0
	while [ "$i" -lt 200 ] && [ ! -f "$FIRST_FRAME" ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if [ -f "$FIRST_FRAME" ]; then
		read -r FF REST < "$FIRST_FRAME"
		echo "JukaHub first frame at ${FF}s since boot" >> "$LOG"
	else
		echo "no first frame after 20 s; starting the radios anyway" >> "$LOG"
	fi
	log_bootstage
	if [ "$WIFI" = 1 ]; then
		if /etc/init.d/wpa_supplicant start >> "$LOG" 2>&1; then
			i=0
			while [ "$i" -lt 25 ]; do
				wpa_cli -i wlan0 status 2> /dev/null | grep -q '^wpa_state=COMPLETED' && break
				sleep 1
				i=$((i + 1))
			done
			[ "$i" -ge 25 ] || udhcpc -i wlan0 -S -t 5 -T 7 -b -p /var/run/udhcpc.wlan0.pid >> "$LOG" 2>&1
		fi
	fi
	[ "$BLUETOOTH" = 1 ] && /etc/init.d/bluetooth start >> "$LOG" 2>&1
}
after_first_frame < /dev/null > /dev/null 2>&1 &

FAILS=0
while :; do
	if [ ! -x "$APP/JukaHub" ]; then
		echo "JukaHub is missing from $APP" >> "$LOG"
		break
	fi
	[ -x /usr/lib/brick/backlight ] && /usr/lib/brick/backlight on
	read -r UP REST < /proc/uptime
	START=${UP%%.*}
	echo "JukaHub starting at ${UP}s since boot" >> "$LOG"
	cd "$APP" || break
	./JukaHub >> "$LOG" 2>&1 &
	CHILD=$!
	# Only the first start of a boot finds the boot frame on the panel.
	JUKAHUB_BOOTFRAME=0
	wait "$CHILD"
	STATUS=$?
	printf '%s %s\n' "$STATUS" "$CHILD" > "$APP/.last-exit"
	if [ -f "$APP/.screen-handoff" ]; then
		read HANDOFF_PID < "$APP/.screen-handoff"
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
if [ -f /tmp/jukahub-native-reboot ]; then reboot; else poweroff; fi
