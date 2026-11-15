#!/bin/sh
# JukaHub OS's own Smart Pro launcher. /jukahub/init starts it once the card
# is mounted and powers the device off (or restarts it) when it returns.
# jukahub-restart-protocol: 1
# jukahub-restart-now: 1
# jukahub-screen-handoff: 1
CARD=/mnt/SDCARD
APP=$CARD/Apps/JukaHub
DATA=$CARD/.userdata/jukahub
LOG=$DATA/boot.log

mkdir -p "$DATA" "$DATA/logs" || exit 1
[ -f "$LOG" ] && mv -f "$LOG" "$LOG.1"
: > "$LOG"

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
while :; do
	if [ ! -x "$APP/JukaHub" ]; then
		echo "JukaHub is missing from $APP" >> "$LOG"
		break
	fi
	uptime_s; START=$NOW
	echo "JukaHub starting at ${START}s since boot" >> "$LOG"
	cd "$APP" || break
	./JukaHub >> "$LOG" 2>&1 &
	CHILD=$!
	wait "$CHILD"
	STATUS=$?
	printf '%s %s\n' "$STATUS" "$CHILD" > "$APP/.last-exit"
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
