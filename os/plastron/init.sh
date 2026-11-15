#!/bin/sh
# JukaHub OS service. plastron's datapart has already mounted /mnt/SDCARD.
CARD=/mnt/SDCARD
SEED=/usr/share/jukahub/card
APP=$CARD/Apps/JukaHub
RA=$CARD/RetroArch
DATA=$CARD/.userdata/jukahub
SEEDED=$DATA/seeded-v1
PID=/tmp/jukahub-os-loop.pid

seed() {
	grep -q " $CARD " /proc/mounts || return 1
	mkdir -p "$APP" "$RA" "$CARD/Emus" "$DATA" || return 1
	# An interrupted first boot leaves no marker, so the next boot repairs
	# partially copied executables and launchers before trying to start them.
	cp -R "$SEED/Apps/JukaHub/." "$APP/" || return 1
	cp -R "$SEED/RetroArch/." "$RA/" || return 1
	cp -R "$SEED/Emus/." "$CARD/Emus/" || return 1
	for d in Roms Bios Saves Licenses; do
		[ -d "$SEED/$d" ] || continue
		[ -e "$CARD/$d" ] || cp -R "$SEED/$d" "$CARD/" || return 1
	done
	for d in "$CARD/Roms" "$CARD/Bios" "$CARD/Saves" "$CARD/Screenshots" "$CARD/Saves/RetroArch" \
		"$CARD/Saves/RetroArch/states"; do
		[ -d "$d" ] || mkdir -p "$d" || return 1
	done
	sync
	printf 'plastron-brick-v1\n' > "$SEEDED" || return 1
	sync
}

start() {
	if [ ! -f "$SEEDED" ] || [ ! -x "$APP/JukaHub" ] || [ ! -x "$RA/retroarch" ] || [ ! -d "$CARD/Emus" ]; then
		echo "jukahub: preparing the card from the image" > /dev/kmsg
		seed || {
			echo "jukahub: card preparation failed" > /dev/kmsg
			return 1
		}
	fi
	sh /usr/share/jukahub/launch.sh < /dev/null > /dev/null 2>&1 &
	echo $! > "$PID"
}

stop() {
	[ -f "$PID" ] && kill "$(cat "$PID")" 2>/dev/null
	# End the frontend and emulator before datapart unmounts the card.
	killall -TERM JukaHub retroarch 2>/dev/null
}

case "$1" in
	start) start ;;
	stop) stop ;;
	*) echo "usage: $0 start|stop" >&2; exit 1 ;;
esac
