#!/bin/sh
# JukaHub OS's own Brick launcher. plastron starts it after mounting the card.
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
export BIOS_PATH=$CARD/Bios
export ROMS_PATH=$CARD/Roms
export SAVES_PATH=$CARD/Saves
export USERDATA_PATH=$DATA

uptime_s() {
	read UP REST < /proc/uptime
	NOW=${UP%%.*}
}

# Radios are started after the splash has gone, off the display's critical
# path. Wi-Fi and Bluetooth are opt-in through boot.env until JukaHub's native
# settings can persist them here.
WIFI=0
BLUETOOTH=0
if [ -f "$DATA/boot.env" ]; then
	while IFS='=' read -r KEY VALUE; do
		# The file may have been edited on Windows.
		KEY=${KEY%"$(printf '\r')"}
		VALUE=${VALUE%"$(printf '\r')"}
		case "$KEY:$VALUE" in
			WIFI:0|WIFI:1) WIFI=$VALUE ;;
			BLUETOOTH:0|BLUETOOTH:1) BLUETOOTH=$VALUE ;;
		esac
	done < "$DATA/boot.env"
fi
radios() {
	i=0
	while [ "$i" -lt 200 ] && pidof splash > /dev/null; do
		sleep 0.1
		i=$((i + 1))
	done
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
{ [ "$WIFI" = 1 ] || [ "$BLUETOOTH" = 1 ]; } && radios &

FAILS=0
while :; do
	if [ ! -x "$APP/JukaHub" ]; then
		echo "JukaHub is missing from $APP" >> "$LOG"
		break
	fi
	[ -x /usr/lib/brick/backlight ] && /usr/lib/brick/backlight on
	uptime_s; START=$NOW
	echo "JukaHub starting at ${START}s since boot" >> "$LOG"
	cd "$APP" || break
	./JukaHub >> "$LOG" 2>&1 &
	CHILD=$!
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
