#!/bin/sh
# jukahub-usbstorage-helper: 1
#
# USB storage mode's helper: lends the SD card to a computer as a USB drive
# and gives it back. JukaHub carries this file (usbstorage.go embeds it) and
# writes it to /tmp with its request before it quits; launch.sh starts it
# there, detached, and ends. Nothing here runs from the card.
#
#   usbstorage_helper.sh REQUEST APP_DIR PID...
#
# REQUEST is JukaHub's request file ("key=value" lines, read, never run),
# APP_DIR the folder launch.sh ran from, and the PIDs are launch.sh and the
# processes above it: the shells among them still hold the card (the script
# file, the folder they were started in) until they finish, which they do now
# that launch.sh has ended; the firmware's launcher loop among them was paused
# by launch.sh (written down in /tmp/jukahub-paused) so the stock menu stays
# closed until the launch.sh started at the end hands the screen back.
#
# The steps, and why:
#   1. The picture JukaHub left in /tmp goes on the panel at once.
#   2. Nothing may use the card while a computer writes to it, or both write
#      the same file system and the card is damaged. So every process is
#      looked at (working folder, root, program, open files, mapped files).
#      JukaHub's own leftover helpers (a program from its folder) are asked
#      to stop; anything else in the way calls the whole thing off. Nothing
#      of the firmware is ever killed. A swap file on the card is switched
#      off (and on again afterwards). Then the card is unmounted - never
#      lazily: a lazy unmount would lend out a card that is still in use -
#      and must be mounted nowhere else (a bind mount, another namespace).
#   3. A USB gadget of its own ("jukastor") with one mass-storage function
#      backed by the card's partition takes the port from the firmware's
#      gadget (ADB), which is only unbound, never changed. Kernels without
#      the configfs function get the legacy g_mass_storage module instead.
#   4. It waits for the first of: the computer ejecting the drive (the
#      kernel closes the LUN's file on a SCSI eject), a button press on the
#      handheld, the cable going after the computer had set the drive up,
#      ten minutes with no computer, or two hours.
#   5. The gadget goes, the firmware's is bound again, the file system is
#      checked when the drive was not ejected (and a checker exists), and the
#      card is mounted with the options it had. JukaHub starts again through
#      launch.sh, which reads how it went from /tmp/jukahub-usbstorage.result.
# Every way out mounts the card and starts JukaHub again; a card that will
# not mount any more restarts the handheld, whose firmware mounts it at boot.
# A stop signal (the firmware's power handling) ends the lending early and
# hands the screen back to the firmware instead of starting JukaHub.
#
# POSIX sh for busybox ash and dash; one command per step, so a missing tool
# fails that step only.

REQ="${1:-}"
APP="${2:-}"
if [ "$#" -ge 2 ]; then
	shift 2
else
	set --
fi
WAIT_PIDS="$*"

ST=/tmp/jukahub-usbstorage
RESULT="$ST.result"
LOG="$ST.log"
KEYFLAG="$ST.key"
QUIT="$ST.quit"
PAUSED=/tmp/jukahub-paused
GADGETS=/sys/kernel/config/usb_gadget
OUR="$GADGETS/jukastor"

exec </dev/null >>"$LOG" 2>&1
cd / || :
umask 077

# What JukaHub's environment had before launch.sh changed it, for the start
# at the end (launch.sh keeps the JUKAHUB_ORIG_* values it finds and builds
# its own on them). The steps here use the firmware's tools only: a PATH or
# library path into the card would make this script itself hold the card.
SAVED_PATH="${JUKAHUB_ORIG_PATH-$PATH}"
PATH=/usr/sbin:/usr/bin:/sbin:/bin
unset LD_LIBRARY_PATH
export PATH

STOP=0
trap '' HUP
trap 'STOP=1' INT TERM

say() {
	echo "[$(date '+%Y-%m-%d %H:%M:%S')] usb storage: $*"
}

nap() {
	sleep "$1" 2>/dev/null || sleep 1
}

up_seconds() {
	UP=0
	read -r UP _ </proc/uptime 2>/dev/null
	UP="${UP%%.*}"
	case "$UP" in "" | *[!0-9]*) UP=0 ;; esac
}

# The process's state and start time from /proc/<pid>/stat (fields 3 and 22,
# read after the name's last ") ").
stat_of() {
	SO_L=""
	read -r SO_L 2>/dev/null <"/proc/$1/stat" || return 1
	# shellcheck disable=SC2086
	set -- ${SO_L##*) }
	SO_STATE="$1"
	SO_START="${20}"
	[ -n "$SO_STATE" ]
}

name_of() {
	NO_C=""
	read -r NO_C 2>/dev/null <"/proc/$1/comm"
	echo "${NO_C:-?} (pid $1)"
}

# ------------------------------------------------------------ the request
MOUNT=""
DEV=""
NAME="JukaHub"
SERIAL=""
PICTURE=""
if [ -f "$REQ" ]; then
	while IFS= read -r RQ_L; do
		case "$RQ_L" in
			mount=*) MOUNT="${RQ_L#mount=}" ;;
			dev=*) DEV="${RQ_L#dev=}" ;;
			name=*) NAME="${RQ_L#name=}" ;;
			serial=*) SERIAL="${RQ_L#serial=}" ;;
			picture=*) PICTURE="${RQ_L#picture=}" ;;
		esac
	done <"$REQ"
fi
rm -f "$REQ" "$KEYFLAG" "$QUIT" "$RESULT"
# Words for the gadget's strings only: printable ASCII, bounded.
NAME="$(printf '%s' "$NAME" | tr -cd 'A-Za-z0-9 ._-' | cut -c1-40)"
SERIAL="$(printf '%s' "$SERIAL" | tr -cd 'A-Za-z0-9' | cut -c1-32)"
[ -n "$NAME" ] || NAME=JukaHub
[ -n "$SERIAL" ] || SERIAL=0123456789

# JukaHub's folder on the card, for the start at the end. launch.sh names it;
# the usual place otherwise.
case "$APP" in
	/*) ;;
	*) APP=/mnt/SDCARD/Apps/JukaHub ;;
esac

# ------------------------------------------------------------- the ending
# How it went, for JukaHub's next start (usbstorage.go): "ok <how>" or
# "failed <reason>", then any notes.
NOTES=""
note() {
	NOTES="$NOTES
$*"
	say "$*"
}

# Swap files on the card this run switched off, one per line.
SWAPS_OFF=""
swaps_back() {
	[ -n "$SWAPS_OFF" ] || return 0
	echo "$SWAPS_OFF" | while IFS= read -r SB_F; do
		[ -n "$SB_F" ] || continue
		swapon "$SB_F" 2>/dev/null && say "swap file $SB_F on again"
	done
	SWAPS_OFF=""
}

# Everything paused for the session runs again, exactly those processes (the
# launch.sh hand-back's resume_paused): for a way out without JukaHub.
resume_paused() {
	[ -f "$PAUSED" ] || return 0
	while read -r RP_P RP_START RP_NAME; do
		case "$RP_P" in "" | *[!0-9]*) continue ;; esac
		if stat_of "$RP_P" && [ "$SO_START" = "$RP_START" ]; then
			kill -CONT "$RP_P" 2>/dev/null && say "let the firmware launcher run again: ${RP_NAME:-?} (pid $RP_P)"
		fi
	done <"$PAUSED"
	rm -f "$PAUSED"
}

# The last step of every way out. $1 is the result line. The card is mounted
# (the callers make sure); JukaHub starts again through launch.sh, unless a
# stop signal came, or JukaHub is gone from the card (deleted on the
# computer): the firmware gets the screen back then.
finish() {
	printf '%s%s\n' "$1" "$NOTES" >"$RESULT"
	swaps_back
	say "$1"
	if [ -d "$APP" ] && [ -w "$APP" ]; then
		cat "$LOG" >>"$APP/errors.txt" 2>/dev/null && printf '' >"$LOG"
	fi
	sync
	trap - INT TERM HUP
	if [ "$STOP" = 0 ] && [ -f "$APP/launch.sh" ]; then
		if [ -n "${JUKAHUB_ORIG_ENV+x}" ]; then
			PATH="$SAVED_PATH"
			if [ -n "${JUKAHUB_ORIG_LD_LIBRARY_PATH+x}" ]; then
				LD_LIBRARY_PATH="$JUKAHUB_ORIG_LD_LIBRARY_PATH"
				export LD_LIBRARY_PATH
			fi
			if [ -n "${JUKAHUB_ORIG_HOME+x}" ]; then
				HOME="$JUKAHUB_ORIG_HOME"
			else
				unset HOME
			fi
		fi
		export PATH
		# stdin is not one a loop below had redirected when it called this.
		exec /bin/sh "$APP/launch.sh" </dev/null
	fi
	[ "$STOP" = 0 ] && say "launch.sh is gone from $APP; handing the screen to the firmware"
	resume_paused
	exit 0
}

# --------------------------------------------------------------- 1. picture
# The picture JukaHub drew (its framebuffer page, row by row at the panel's
# stride, and "width height bpp stride" in .info), into every page of the
# buffer, only when it matches the panel exactly.
show_picture() {
	[ -n "$PICTURE" ] && [ -f "$PICTURE" ] && [ -f "$PICTURE.info" ] && [ -c /dev/fb0 ] || return 0
	read -r SP_W SP_H SP_BPP SP_STRIDE _ <"$PICTURE.info" || return 0
	case ":$SP_W:$SP_H:$SP_BPP:$SP_STRIDE:" in
		*[!0-9:]* | *::* | *:0* | *[0-9][0-9][0-9][0-9][0-9][0-9][0-9]*) return 0 ;;
	esac
	SP_FB=/sys/class/graphics/fb0
	SP_V=""
	read -r SP_V <"$SP_FB/virtual_size" 2>/dev/null
	SP_VW="${SP_V%%,*}"
	SP_VH="${SP_V##*,}"
	case ":$SP_VW:$SP_VH:" in
		*[!0-9:]* | *::* | *:0* | *[0-9][0-9][0-9][0-9][0-9][0-9][0-9]*) return 0 ;;
	esac
	SP_X=""
	read -r SP_X <"$SP_FB/bits_per_pixel" 2>/dev/null
	[ "$SP_X" = "$SP_BPP" ] || return 0
	SP_X=""
	read -r SP_X <"$SP_FB/stride" 2>/dev/null
	[ "$SP_X" = "$SP_STRIDE" ] || return 0
	[ "$SP_VW" = "$SP_W" ] && [ "$SP_VH" -ge "$SP_H" ] && [ $((SP_VH % SP_H)) -eq 0 ] || return 0
	SP_SIZE="$(wc -c <"$PICTURE" 2>/dev/null | tr -d ' ')"
	[ "$SP_SIZE" = $((SP_STRIDE * SP_H)) ] || return 0
	SP_N=0
	{
		while [ "$SP_N" -lt $((SP_VH / SP_H)) ]; do
			cat "$PICTURE"
			SP_N=$((SP_N + 1))
		done
	} >/dev/fb0 2>/dev/null
	return 0
}
show_picture

# ----------------------------------------------- 2. the shells above finish
# launch.sh and the shells that ran it end now; the firmware's loop above
# them stays paused (state T). Ten seconds at most. First of all: a way out
# below starts launch.sh again, which must not run beside the one ending.
WP_N=0
while [ "$WP_N" -lt 50 ]; do
	WP_LEFT=""
	for WP_P in $WAIT_PIDS; do
		case "$WP_P" in "" | 1 | *[!0-9]*) continue ;; esac
		stat_of "$WP_P" || continue
		case "$SO_STATE" in Z | T | t) continue ;; esac
		WP_LEFT="$WP_LEFT $WP_P"
	done
	[ -z "$WP_LEFT" ] && break
	WP_N=$((WP_N + 1))
	nap 0.2
done
[ -n "$WP_LEFT" ] && say "still running after 10 s:$WP_LEFT"

# ------------------------------------------------- the card and what it is
# The card's mount line: device, type and options, so it can be mounted back
# exactly as it was.
mount_line() {
	ML_DEV=""
	while read -r ML_S ML_M ML_T ML_O _; do
		if [ "$ML_M" = "$MOUNT" ]; then
			ML_DEV="$ML_S"
			ML_TYPE="$ML_T"
			ML_OPTS="$ML_O"
		fi
	done </proc/mounts
	[ -n "$ML_DEV" ]
}

case "$MOUNT" in
	/mnt/?* | /media/?* | /sdcard) ;;
	*) MOUNT="" ;;
esac
case "$MOUNT" in *[!A-Za-z0-9/_.-]* | */ | *..*) MOUNT="" ;; esac
case "$APP" in
	"$MOUNT"/*) ;;
	*) MOUNT="" ;;
esac
if [ -z "$MOUNT" ] || ! mount_line; then
	finish "failed JukaHub's SD card is not a mount of its own here"
fi
[ "$ML_DEV" = "$DEV" ] || say "the card is $ML_DEV (the request said ${DEV:-nothing})"
DEV="$ML_DEV"
FSTYPE="$ML_TYPE"
OPTS="$ML_OPTS"
case "$DEV" in
	/dev/mmcblk[0-9]*p[0-9]* | /dev/sd[a-z]*[0-9]*) ;;
	*) finish "failed The SD card is not a disk partition ($DEV)" ;;
esac
[ -b "$DEV" ] || finish "failed $DEV is not a block device"
MAJMIN=""
read -r MAJMIN <"/sys/class/block/${DEV##*/}/dev" 2>/dev/null
case "$MAJMIN" in
	[0-9]*:[0-9]*) ;;
	*) finish "failed The kernel does not describe $DEV" ;;
esac
say "lending $DEV ($FSTYPE, $OPTS) from $MOUNT"

# ------------------------------------------------- 2. who still uses it
# One "pid what" line per process using the card: its working folder, root
# or program, an open file, or a mapped file (a library). One ls for all the
# links and one grep for all the maps, not a program per file.
card_users() {
	{
		ls -l /proc/[0-9]*/cwd /proc/[0-9]*/root /proc/[0-9]*/exe 2>/dev/null | while IFS= read -r CU_L; do
			case "$CU_L" in
				*" -> $MOUNT" | *" -> $MOUNT/"*) ;;
				*) continue ;;
			esac
			CU_X="${CU_L%% -> *}"
			CU_X="${CU_X##* /proc/}"
			echo "${CU_X%%/*} ${CU_X#*/}"
		done
		ls -l /proc/[0-9]*/fd 2>/dev/null | {
			CU_P=""
			while IFS= read -r CU_L; do
				case "$CU_L" in
					/proc/*/fd:)
						CU_P="${CU_L#/proc/}"
						CU_P="${CU_P%/fd:}"
						;;
					*" -> $MOUNT" | *" -> $MOUNT/"*) [ -n "$CU_P" ] && echo "$CU_P file" ;;
				esac
			done
		}
		for CU_F in $(grep -lF " $MOUNT/" /proc/[0-9]*/maps 2>/dev/null); do
			CU_F="${CU_F#/proc/}"
			echo "${CU_F%/maps} map"
		done
	} | while read -r CU_P CU_W; do
		case "$CU_P" in "" | *[!0-9]*) continue ;; esac
		[ "$CU_P" = "$$" ] && continue
		stat_of "$CU_P" || continue
		[ "$SO_STATE" = Z ] && continue
		echo "$CU_P $CU_W"
	done | sort -u -n
}

# The users as "name (pid N)" words, at most four, for the result.
users_text() {
	UT_T=""
	UT_N=0
	UT_LAST=""
	for UT_P in $1; do
		[ "$UT_P" = "$UT_LAST" ] && continue
		UT_LAST="$UT_P"
		UT_N=$((UT_N + 1))
		[ "$UT_N" -le 4 ] && UT_T="$UT_T${UT_T:+, }$(name_of "$UT_P")"
	done
	[ "$UT_N" -gt 4 ] && UT_T="$UT_T and $((UT_N - 4)) more"
	echo "$UT_T"
}

# JukaHub's own leftovers (a program from its folder: a VPN tunnel, an
# update or download helper) are asked to stop - TERM, never KILL; whatever
# else uses the card stays and the lending is called off.
USERS="$(card_users)"
if [ -n "$USERS" ]; then
	OWN=""
	for OW_P in $(echo "$USERS" | while read -r OW_P _; do echo "$OW_P"; done | sort -u -n); do
		case "$(readlink "/proc/$OW_P/exe" 2>/dev/null)" in
			"$APP"/*)
				OWN="$OWN $OW_P"
				say "asking JukaHub's leftover $(name_of "$OW_P") to stop"
				kill -TERM "$OW_P" 2>/dev/null
				;;
		esac
	done
	if [ -n "$OWN" ]; then
		OW_N=0
		while [ "$OW_N" -lt 25 ]; do
			OW_LEFT=""
			for OW_P in $OWN; do
				stat_of "$OW_P" && [ "$SO_STATE" != Z ] && OW_LEFT=1
			done
			[ -z "$OW_LEFT" ] && break
			OW_N=$((OW_N + 1))
			nap 0.2
		done
		USERS="$(card_users)"
	fi
fi
if [ -n "$USERS" ]; then
	echo "$USERS" | while read -r UL_P UL_W; do say "uses the card: $(name_of "$UL_P") ($UL_W)"; done
	finish "failed Still using the SD card: $(users_text "$(echo "$USERS" | while read -r UL_P _; do echo "$UL_P"; done)")"
fi

# Something mounted inside the card, or a disk image on it in use, would keep
# it busy or be cut off under its feet: called off, never unmounted here.
while read -r SM_S SM_M _; do
	case "$SM_M" in
		"$MOUNT"/*) finish "failed Something is mounted inside the SD card ($SM_M)" ;;
	esac
done </proc/mounts
for LB_F in /sys/block/loop*/loop/backing_file; do
	[ -f "$LB_F" ] || continue
	LB_B=""
	read -r LB_B <"$LB_F" 2>/dev/null
	case "$LB_B" in
		"$MOUNT"/*) finish "failed A disk image on the SD card is in use ($LB_B)" ;;
	esac
done

# Swap files on the card go off (their pages move back into memory) and on
# again at the end.
while read -r SW_F SW_T _; do
	case "$SW_F" in
		"$MOUNT"/*)
			if swapoff "$SW_F" 2>/dev/null; then
				SWAPS_OFF="$SWAPS_OFF$SW_F
"
				say "swap file $SW_F off"
			else
				finish "failed The swap file on the SD card could not be switched off (not enough free memory?)"
			fi
			;;
	esac
done </proc/swaps

# --------------------------------------------- mounting it back, every way
# Mounted anywhere (this namespace, a bind mount, another namespace): the
# kernel would then still write to it. ME_F is the first mountinfo naming it.
mounted_elsewhere() {
	ME_F="$(grep -lF " $MAJMIN " /proc/[0-9]*/mountinfo 2>/dev/null | head -n 1)"
	[ -n "$ME_F" ]
}

mount_card() {
	if mount_line && [ "$ML_DEV" = "$DEV" ]; then
		return 0
	fi
	[ -d "$MOUNT" ] || mkdir -p "$MOUNT" 2>/dev/null
	mount -t "$FSTYPE" -o "$OPTS" "$DEV" "$MOUNT" && return 0
	say "mount with the old options failed; trying the defaults"
	mount -t "$FSTYPE" "$DEV" "$MOUNT" && return 0
	mount "$DEV" "$MOUNT"
}

# The file system check, when a checker for it exists. Automatic repairs
# only; how long it takes depends on the card.
CHECKED=0
check_card() {
	[ "$CHECKED" = 0 ] || return 0
	CHECKED=1
	CK_T=""
	case "$FSTYPE" in
		vfat | msdos | fat)
			for CK_C in fsck.vfat fsck.fat dosfsck; do
				command -v "$CK_C" >/dev/null 2>&1 && CK_T="$CK_C -a" && break
			done
			;;
		exfat)
			command -v fsck.exfat >/dev/null 2>&1 && CK_T="fsck.exfat -p"
			;;
	esac
	if [ -z "$CK_T" ]; then
		say "no checker for $FSTYPE on this firmware; not checked"
		return 0
	fi
	# A checker writing to a mounted file system damages it: an automount
	# that came first (the LUN closing) means no check.
	if mounted_elsewhere; then
		say "the card is mounted again already; not checked"
		return 0
	fi
	say "checking the card: $CK_T $DEV"
	# shellcheck disable=SC2086
	$CK_T "$DEV"
	CK_RC=$?
	case "$CK_RC" in
		0) note "fsck clean" ;;
		1) note "fsck repaired" ;;
		*) note "fsck failed $CK_RC" ;;
	esac
}

# The card back where it was, or - when it will not mount even after a
# check - a restart, so the firmware mounts it at boot.
remount_or_reboot() {
	mount_card && return 0
	check_card
	mount_card && return 0
	printf 'failed The SD card would not mount again; the handheld restarted\n' >"$RESULT"
	say "the card will not mount again; restarting the handheld"
	sync
	reboot
	nap 10
	reboot -f
	exit 1
}

# ------------------------------------------------------------- unmount
# A stop that came meanwhile (the firmware's power handling): nothing lent.
[ "$STOP" = 0 ] || finish "failed The handheld asked it to stop before the card was lent out"
sync
UM_N=0
UM_OK=0
while [ "$UM_N" -lt 3 ]; do
	if umount "$MOUNT" 2>/dev/null; then
		UM_OK=1
		break
	fi
	UM_N=$((UM_N + 1))
	nap 1
done
if [ "$UM_OK" = 0 ]; then
	# Usually still mounted; an umount that failed after all is mounted back
	# like any other way out.
	remount_or_reboot
	USERS="$(card_users)"
	finish "failed The SD card could not be unmounted (busy${USERS:+: $(users_text "$(echo "$USERS" | while read -r UL_P _; do echo "$UL_P"; done)")})"
fi
say "card unmounted"

# Mounted nowhere else now (a bind mount, a mount in another namespace).
if mounted_elsewhere; then
	ME_P="${ME_F#/proc/}"
	ME_P="${ME_P%/mountinfo}"
	remount_or_reboot
	finish "failed The SD card is also mounted elsewhere (seen by $(name_of "$ME_P"))"
fi

# ------------------------------------------------------------- 3. gadget
UDC=""
FW=""
FW_UDC=""
LEGACY=0
LUNFILE=""

gadget_down() {
	if [ "$LEGACY" = 1 ]; then
		modprobe -r g_mass_storage 2>/dev/null || rmmod g_mass_storage 2>/dev/null
	elif [ -d "$OUR" ]; then
		[ -n "$(cat "$OUR/UDC" 2>/dev/null)" ] && echo "" >"$OUR/UDC" 2>/dev/null
		# Unbound, the LUN can let go of the card at once.
		[ -f "$OUR/functions/mass_storage.0/lun.0/file" ] && echo "" >"$OUR/functions/mass_storage.0/lun.0/file" 2>/dev/null
		rm -f "$OUR/configs/c.1/mass_storage.0" 2>/dev/null
		rmdir "$OUR/configs/c.1/strings/0x409" 2>/dev/null
		rmdir "$OUR/configs/c.1" 2>/dev/null
		rmdir "$OUR/functions/mass_storage.0" 2>/dev/null
		rmdir "$OUR/strings/0x409" 2>/dev/null
		rmdir "$OUR" 2>/dev/null || say "could not remove the USB gadget jukastor"
	fi
	# The firmware's gadget gets the port back; JukaHub's own gadgets are
	# removed by its next start instead (usbmtp_linux.go, usbpad_linux.go).
	if [ -n "$FW" ] && [ -n "$FW_UDC" ]; then
		GD_N=0
		while [ "$GD_N" -lt 25 ]; do
			echo "$FW_UDC" >"$FW/UDC" 2>/dev/null && break
			[ "$(cat "$FW/UDC" 2>/dev/null)" = "$FW_UDC" ] && break
			GD_N=$((GD_N + 1))
			nap 0.2
		done
		[ "$(cat "$FW/UDC" 2>/dev/null)" = "$FW_UDC" ] || say "could not give the USB port back to ${FW##*/}"
	fi
	if command -v udevadm >/dev/null 2>&1; then
		udevadm settle --timeout=5 2>/dev/null || nap 1
	else
		nap 1
	fi
}

# Called off after the unmount: everything back, then the ending.
call_off() {
	gadget_down
	remount_or_reboot
	finish "failed $1"
}

if [ ! -d "$GADGETS" ]; then
	mount -t configfs none /sys/kernel/config 2>/dev/null
	[ -d "$GADGETS" ] || modprobe libcomposite 2>/dev/null
fi
for GU_U in /sys/class/udc/*; do
	[ -e "$GU_U" ] || continue
	UDC="${GU_U##*/}"
	break
done
[ -n "$UDC" ] || call_off "This handheld's USB port has no device controller"
if [ -d "$GADGETS" ]; then
	for GU_G in "$GADGETS"/*; do
		[ -d "$GU_G" ] || continue
		GU_B=""
		read -r GU_B <"$GU_G/UDC" 2>/dev/null
		[ -n "$GU_B" ] || continue
		case "${GU_G##*/}" in
			jukastor) continue ;;
			jukahub | jukapad | jukapad-probe)
				# JukaHub's own, left bound by a run that ended badly: unbound
				# only.
				echo "" >"$GU_G/UDC" 2>/dev/null
				continue
				;;
		esac
		FW="$GU_G"
		FW_UDC="$GU_B"
		UDC="$GU_B"
		break
	done
fi

gadget_configfs() {
	[ -d "$GADGETS" ] || return 1
	[ -d "$OUR" ] && gadget_down_ours_only
	mkdir "$OUR" 2>/dev/null || return 1
	if ! mkdir "$OUR/functions/mass_storage.0" 2>/dev/null; then
		rmdir "$OUR" 2>/dev/null
		return 1
	fi
	# The IDs Linux's own mass-storage gadget uses: every computer has a
	# driver for them and none expects anything but a drive behind them.
	echo 0x0525 >"$OUR/idVendor"
	echo 0xa4a5 >"$OUR/idProduct"
	echo 0x0100 >"$OUR/bcdDevice"
	echo 0x0200 >"$OUR/bcdUSB"
	mkdir -p "$OUR/strings/0x409"
	echo "JukaHub" >"$OUR/strings/0x409/manufacturer"
	echo "$NAME SD card" >"$OUR/strings/0x409/product"
	echo "$SERIAL" >"$OUR/strings/0x409/serialnumber"
	L="$OUR/functions/mass_storage.0/lun.0"
	echo 1 >"$L/removable" 2>/dev/null
	echo 0 >"$L/cdrom" 2>/dev/null
	echo 0 >"$L/ro" 2>/dev/null
	echo "JukaHub SD card" >"$L/inquiry_string" 2>/dev/null
	if ! echo "$DEV" >"$L/file"; then
		say "the mass-storage function would not take $DEV"
		return 2
	fi
	LUNFILE="$L/file"
	mkdir -p "$OUR/configs/c.1/strings/0x409"
	echo "Mass Storage" >"$OUR/configs/c.1/strings/0x409/configuration"
	echo 500 >"$OUR/configs/c.1/MaxPower"
	ln -s "$OUR/functions/mass_storage.0" "$OUR/configs/c.1/mass_storage.0" || return 2
	[ -n "$FW" ] && echo "" >"$FW/UDC" 2>/dev/null
	GC_N=0
	while [ "$GC_N" -lt 25 ]; do
		echo "$UDC" >"$OUR/UDC" 2>/dev/null && return 0
		[ "$(cat "$OUR/UDC" 2>/dev/null)" = "$UDC" ] && return 0
		GC_N=$((GC_N + 1))
		nap 0.2
	done
	say "the USB port would not take the gadget"
	return 2
}

# A gadget an earlier run of this script left (it was killed): taken apart.
gadget_down_ours_only() {
	GO_FW="$FW"
	FW=""
	gadget_down
	FW="$GO_FW"
}

gadget_legacy() {
	command -v modprobe >/dev/null 2>&1 || return 1
	[ -n "$FW" ] && echo "" >"$FW/UDC" 2>/dev/null
	# Module parameters are split at spaces: no spaces in the values.
	if modprobe g_mass_storage file="$DEV" removable=1 stall=0 iManufacturer=JukaHub iProduct=JukaHub-SD-card 2>/dev/null ||
		modprobe g_mass_storage file="$DEV" removable=1 2>/dev/null; then
		LEGACY=1
		# Where the legacy gadget's LUN shows its file, for the eject; without
		# it only a button, the cable or the time ends the lending.
		[ -f "/sys/class/udc/$UDC/device/gadget/lun0/file" ] && LUNFILE="/sys/class/udc/$UDC/device/gadget/lun0/file"
		return 0
	fi
	return 1
}

gadget_configfs
GC_RC=$?
if [ "$GC_RC" != 0 ]; then
	gadget_down_ours_only
	if [ "$GC_RC" = 2 ] || ! gadget_legacy; then
		call_off "This firmware's kernel would not lend the card over USB"
	fi
	say "using the kernel's g_mass_storage module"
fi
say "the card is a USB drive now (port $UDC${FW:+, ${FW##*/} unbound})"

# ------------------------------------------------------------- 4. waiting
# input_event is 24 bytes for a 64-bit reader (16 for a 32-bit one): two
# 64-bit time words, then type, code and value. The reader is this busybox.
EV_SIZE=24
case "$(dd if=/bin/busybox bs=1 skip=4 count=1 2>/dev/null | od -An -tu1 2>/dev/null)" in
	*1*) EV_SIZE=16 ;;
esac

# One reader per input device: the first key press (EV_KEY, value 1 - not a
# release, not a held repeat) leaves $KEYFLAG. A release of the button that
# confirmed in JukaHub, a stick's drift and the like are not presses.
key_watch() {
	while [ ! -e "$KEYFLAG" ] && [ ! -e "$QUIT" ]; do
		# shellcheck disable=SC2046
		set -- $(dd if="$KW_DEV" bs="$EV_SIZE" count=1 2>/dev/null | od -An -v -tu2 2>/dev/null)
		[ -e "$QUIT" ] && return 0
		if [ "$EV_SIZE" = 24 ]; then
			[ "$#" -ge 11 ] || { nap 1; continue; }
			shift 8
		else
			[ "$#" -ge 7 ] || { nap 1; continue; }
			shift 4
		fi
		if [ "$1" = 1 ] && [ "$3" = 1 ] && [ "$4" = 0 ]; then
			: >"$KEYFLAG"
			return 0
		fi
	done
}
WATCHERS=""
for KW_DEV in /dev/input/event*; do
	[ -c "$KW_DEV" ] || continue
	key_watch &
	WATCHERS="$WATCHERS $!"
done

up_seconds
T0="$UP"
SEEN=0
HOW=""
while [ -z "$HOW" ]; do
	nap 1
	up_seconds
	EL=$((UP - T0))
	US_S=""
	read -r US_S <"/sys/class/udc/$UDC/state" 2>/dev/null
	case "$US_S" in configured) SEEN=1 ;; esac
	# The card mounted on the handheld while the computer has it (an
	# automount): both would write it, so the lending ends at once.
	grep -qsF " $MAJMIN " /proc/1/mountinfo /proc/self/mountinfo && HOW=mounted
	if [ -z "$HOW" ] && [ -n "$LUNFILE" ]; then
		# An open LUN reads as its file's path, a closed one as nothing at
		# all - no newline either, so read's status cannot tell it from a
		# failed read: the dot comes only after a read that worked.
		US_F="$(cat "$LUNFILE" 2>/dev/null && echo .)"
		[ "$US_F" = . ] && HOW=ejected
	fi
	[ -z "$HOW" ] && [ -e "$KEYFLAG" ] && HOW=button
	[ -z "$HOW" ] && [ "$SEEN" = 1 ] && [ "$US_S" = "not attached" ] && HOW=unplugged
	[ -z "$HOW" ] && [ "$STOP" = 1 ] && HOW=stopped
	# Idle only where the port's state can be read: a computer the state
	# cannot show may be writing.
	[ -z "$HOW" ] && [ "$SEEN" = 0 ] && [ -n "$US_S" ] && [ "$EL" -ge 600 ] && HOW=idle
	[ -z "$HOW" ] && [ "$EL" -ge 7200 ] && HOW=timeout
done
say "the lending ends: $HOW after ${EL}s"

# The readers go; one still waiting in dd ends at the next press.
printf '' >"$QUIT"
for KW_F in /proc/[0-9]*/stat; do
	read -r KW_L 2>/dev/null <"$KW_F" || continue
	# shellcheck disable=SC2086
	set -- ${KW_L##*) }
	case " $WATCHERS " in
		*" $2 "*)
			KW_C="${KW_F#/proc/}"
			kill "${KW_C%/stat}" 2>/dev/null
			;;
	esac
done
for KW_P in $WATCHERS; do
	kill "$KW_P" 2>/dev/null
done
rm -f "$KEYFLAG"

# ------------------------------------------------------------- 5. back
gadget_down
# A drive the computer ejected was flushed and closed by it, one that was
# never set up was not written; a button, a pulled cable or the time limit
# may have cut a write short. A stop (a power-off) does not wait for a check.
case "$HOW" in
	button | unplugged | timeout) check_card ;;
esac
remount_or_reboot
rm -f "$QUIT"
finish "ok $HOW"
