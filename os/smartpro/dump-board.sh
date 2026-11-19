#!/bin/sh
# Save this TrimUI Smart Pro's own boot files for the JukaHub OS image builder.
#
# Run it on the Smart Pro while it runs its stock (or CrossMix) system:
#   sh /mnt/SDCARD/dump-board.sh            -> /mnt/SDCARD/JukaHub-SmartPro-board
#
# It only READS the internal storage (with dd and tar) and writes only to the
# SD card. Nothing on the device is changed. What it saves is this device's
# own bootloader, kernel and system files: keep them to yourself, they are
# TrimUI's and are not to be redistributed.
#
# jukahub-board-format: 1
set -u
PATH=/usr/sbin:/usr/bin:/sbin:/bin:$PATH
export PATH

PARENT=${1:-/mnt/SDCARD}
OUT=$PARENT/JukaHub-SmartPro-board
BIND=/tmp/jukahub-board-root.$$
MAX_LEAD_SECTORS=262144      # 128 MiB before the first partition, at most
MAX_PART_BYTES=268435456     # 256 MiB per copied boot partition, at most

say() { echo "dump-board: $*"; }
fail() {
	say "FAILED: $*" >&2
	[ -d "$OUT" ] && echo "$*" > "$OUT/FAILED.txt" 2> /dev/null
	cleanup
	exit 1
}
cleanup() {
	if [ -d "$BIND" ]; then
		umount "$BIND" 2> /dev/null
		rmdir "$BIND" 2> /dev/null
	fi
}
trap cleanup EXIT
trap 'cleanup; exit 1' INT TERM

[ "$(id -u)" = 0 ] || fail "run as root (the stock system's ssh and apps already are)"
[ -d "$PARENT" ] || fail "no card folder at $PARENT"
[ ! -e "$OUT" ] || fail "$OUT already exists; move it away first"

# ------------------------------------------------------------ which device
# A Smart Pro is the Allwinner A133P (sun50iw10) with a 1280x720 panel. The
# Brick and Brick Pro share the chip with a 1024x768 panel; the Smart Pro S is
# another chip (A523, sun55iw3). The model code TrimUI stamps into the device
# tree (TG5040 Smart Pro, TG3040 Brick, TG4040 Brick Pro, TG5050 Smart Pro S)
# is recorded as MODEL. JUKAHUB_ANY_MODEL=1 saves another model's files too,
# for development only: the image builder refuses them without
# --experimental-model, because only the Smart Pro's boot is known.
ANY_MODEL=${JUKAHUB_ANY_MODEL:-0}
MODEL=$( { tr '\0' ' ' < /proc/device-tree/model; tr '\0' ' ' < /proc/device-tree/compatible; cat /etc/hostname; } 2> /dev/null |
	grep -o -i 'tg[0-9][0-9][0-9][0-9]' | head -n 1 | tr 'a-z' 'A-Z')
SOC=$(tr '\0' ' ' < /proc/device-tree/compatible 2> /dev/null)
grep -q 'sun50iw10\|sun50i-a100\|sun50i-a133' /proc/cpuinfo 2> /dev/null && SOC="$SOC sun50iw10"
case "$SOC" in
	*sun50iw10*|*sun50i-a100*|*sun50i-a133*) ;;
	*sun55iw3*|*sun55i-a523*|*sun55i-t527*)
		[ "$ANY_MODEL" = 1 ] || fail "this is an Allwinner A523 (Smart Pro S); only the Smart Pro (TG5040) is supported"
		[ -n "$MODEL" ] || MODEL=TG5050 ;;
	*) [ "$ANY_MODEL" = 1 ] || fail "this is not an Allwinner A133 device (Smart Pro S and other chips are not supported)" ;;
esac
case "${MODEL:-TG5040}" in
	TG5040) ;;
	*) [ "$ANY_MODEL" = 1 ] || fail "this is a $MODEL; only the Smart Pro (TG5040) is supported" ;;
esac
PANEL=""
for f in /sys/class/graphics/fb0/modes /sys/class/graphics/fb0/virtual_size; do
	[ -r "$f" ] || continue
	case "$(cat "$f")" in
		*1280x720*|1280,720*|1280,1440*) PANEL=1280x720 ;;
		*1024x768*|1024,768*|1024,1536*) PANEL=1024x768 ;;
	esac
	[ -n "$PANEL" ] && break
done
[ "$PANEL" = 1280x720 ] || [ "$ANY_MODEL" = 1 ] || fail "panel is '${PANEL:-unknown}', not the Smart Pro's 1280x720"
FIRMWARE=""
if [ -r /usr/trimui/bin/MainUI ]; then
	FIRMWARE=$(grep -a -o 'Trimui Smart Pro S\|Trimui Smart Pro\|Trimui Brick' /usr/trimui/bin/MainUI 2> /dev/null | head -n 1)
fi
if [ "$FIRMWARE" = "Trimui Smart Pro S" ]; then
	[ "$ANY_MODEL" = 1 ] || fail "this is a Smart Pro S; only the Smart Pro (TG5040) is supported"
	MODEL=TG5050
fi
# Every check passed without a code in the device tree: an A133 with the
# Smart Pro's panel is a Smart Pro, as before.
[ -n "$MODEL" ] || MODEL=TG5040
case "$MODEL" in
	TG5040) DEVICE=trimui-smart-pro ;;
	TG5050) DEVICE=trimui-smart-pro-s ;;
	TG3040) DEVICE=trimui-brick ;;
	TG4040) DEVICE=trimui-brick-pro ;;
	*) DEVICE=unknown ;;
esac

# ------------------------------------------------------- disks and the root
EMMC=""
SD=""
for d in /sys/block/mmcblk*; do
	[ -d "$d" ] || continue
	case "$(cat "$d/device/type" 2> /dev/null)" in
		MMC) [ -z "$EMMC" ] || fail "two internal eMMC disks"; EMMC=${d##*/} ;;
		SD) SD=${d##*/} ;;
	esac
done
[ -n "$EMMC" ] || fail "no internal eMMC found"

# The stock root may be an overlay on top of a read-only image (/rom).
ROOT_LINE=$(awk '$5 == "/" { print }' /proc/self/mountinfo | tail -n 1)
ROOT_FSTYPE=$(echo "$ROOT_LINE" | awk '{ for (i = 7; i <= NF; i++) if ($i == "-") { print $(i + 1); exit } }')
ROOT_DEVNUM=$(echo "$ROOT_LINE" | awk '{ print $3 }')
if [ "$ROOT_FSTYPE" = overlay ]; then
	ROM_LINE=$(awk '$5 == "/rom" { print }' /proc/self/mountinfo | tail -n 1)
	[ -n "$ROM_LINE" ] || fail "the root is an overlay without /rom"
	ROOT_FSTYPE=$(echo "$ROM_LINE" | awk '{ for (i = 7; i <= NF; i++) if ($i == "-") { print $(i + 1); exit } }')
	ROOT_DEVNUM=$(echo "$ROM_LINE" | awk '{ print $3 }')
fi
ROOT_PART=""
for p in /sys/class/block/"$EMMC"p*; do
	[ -r "$p/dev" ] || continue
	[ "$(cat "$p/dev")" = "$ROOT_DEVNUM" ] && ROOT_PART=${p##*/}
done
[ -n "$ROOT_PART" ] || fail "the running system is not on the internal eMMC (run this on the stock system, not from an SD-card OS)"
partname() { sed -n 's/^PARTNAME=//p' "/sys/class/block/$1/uevent" 2> /dev/null; }
ROOT_NAME=$(partname "$ROOT_PART")
[ -n "$ROOT_NAME" ] || fail "the eMMC has no named (GPT) partitions"
ROOT_START=$(cat "/sys/class/block/$ROOT_PART/start")

# ------------------------------------------------- the output is on the card
# Everything is written under $PARENT, so it must be a partition of the SD
# card: with no card in, /mnt/SDCARD is a plain folder on the internal storage.
[ -n "$SD" ] || fail "no SD card found"
PARENT_DEV=""
PARENT_FSTYPE=""
BEST=0
while read -r _ _ devnum _ mnt rest; do
	case "$PARENT/" in
		"${mnt%/}/"*) ;;
		*) continue ;;
	esac
	[ "${#mnt}" -ge "$BEST" ] || continue
	BEST=${#mnt}
	PARENT_DEV=$devnum
	PARENT_FSTYPE=$(echo "$rest" | awk '{ for (i = 1; i <= NF; i++) if ($i == "-") { print $(i + 1); exit } }')
done < /proc/self/mountinfo
ON_CARD=""
for p in /sys/class/block/"$SD"p*; do
	[ -r "$p/dev" ] && [ "$(cat "$p/dev")" = "$PARENT_DEV" ] && ON_CARD=1
done
[ -n "$ON_CARD" ] || fail "$PARENT is not on the SD card; nothing is written to the internal storage"

# ------------------------------------------------------------- free space
say "measuring the system files"
NEED_KB=$(du -skx / 2> /dev/null | awk '{ print $1 }')
[ -n "$NEED_KB" ] || NEED_KB=$(df -Pk / | awk 'NR == 2 { print $3 }')
FREE_KB=$(df -Pk "$PARENT" | awk 'NR == 2 { print $4 }')
[ -n "$NEED_KB" ] && [ -n "$FREE_KB" ] || fail "cannot read free space"
[ "$FREE_KB" -gt $((NEED_KB + 262144)) ] || fail "the card needs $(((NEED_KB + 262144) / 1024)) MB free, has $((FREE_KB / 1024)) MB"
if [ "$PARENT_FSTYPE" = vfat ] && [ "$NEED_KB" -gt 4000000 ]; then
	fail "the system files are over 4 GB, more than one file on a FAT32 card can hold"
fi
pidof wpa_supplicant > /dev/null 2>&1 ||
	say "note: Wi-Fi is off; turn it on and run this again if JukaHub OS should start Wi-Fi"

mkdir -p "$OUT" "$OUT/parts" "$OUT/udisk" || fail "cannot write to $PARENT"
say "saving to $OUT"

# ------------------------------------------------------ partition table
FIRST=""
: > "$OUT/partitions.txt"
for p in /sys/class/block/"$EMMC"p*; do
	[ -r "$p/start" ] && [ -r "$p/partition" ] || continue
	n=$(cat "$p/partition")
	s=$(cat "$p/start")
	z=$(cat "$p/size")
	name=$(partname "${p##*/}")
	printf '%s %s %s %s\n' "$n" "${name:--}" "$s" "$z" >> "$OUT/partitions.txt"
	if [ -z "$FIRST" ] || [ "$s" -lt "$FIRST" ]; then FIRST=$s; fi
done
[ -n "$FIRST" ] || fail "no partitions on /dev/$EMMC"
[ "$FIRST" -le "$MAX_LEAD_SECTORS" ] || fail "first partition starts unexpectedly late ($FIRST)"

# Everything before the first partition: the partition table, boot0 (8 KiB)
# and the boot package (16400 KiB), with their backup copies.
say "boot area: $((FIRST / 2048)) MiB"
dd if="/dev/$EMMC" of="$OUT/lead.bin" bs=512 count="$FIRST" 2> /dev/null || fail "reading the boot area failed"

# Every partition in front of the system: boot-resource (logo), env, boot
# (kernel) and the like. The system and user partitions are not copied raw.
while read -r n name s z; do
	[ "$s" -lt "$ROOT_START" ] || continue
	case "$name" in -|*[!A-Za-z0-9_.-]*) fail "partition $n has an unusable name '$name'" ;; esac
	[ $((z * 512)) -le "$MAX_PART_BYTES" ] || fail "partition $name is unexpectedly large"
	say "partition $n $name: $((z / 2)) KiB"
	dd if="/dev/${EMMC}p$n" of="$OUT/parts/$name.img" bs=65536 2> /dev/null || fail "reading partition $name failed"
done < "$OUT/partitions.txt"

# ------------------------------------------------------ the system itself
# The running root, bound without the mounts on top of it (/dev, /proc, the
# card), so the archive holds exactly the system files.
say "system files (this takes a few minutes)"
mkdir -p "$BIND" && mount -o bind / "$BIND" || fail "cannot bind the root"
(cd "$BIND" && tar -cf "$OUT/rootfs.tar" .) || fail "archiving the system failed"
umount "$BIND" && rmdir "$BIND"
# The start of the system partition: its superblock names the compression
# the kernel already reads (a squashfs root).
dd if="/dev/$ROOT_PART" of="$OUT/root-head.bin" bs=4096 count=1 2> /dev/null

# --------------------------------------------------------------- facts
# The delay loop the stock kernel calibrated at boot, if it did ("skipped"
# means a timer gave it, which costs nothing): lpj= on the image's command
# line saves the calibration. Only while the boot messages are still in dmesg.
LPJ=$(dmesg 2> /dev/null | grep 'Calibrating delay loop' | grep -v skipped |
	grep -o 'lpj=[0-9]*' | head -n 1 | sed 's/^lpj=//')
{
	echo "FORMAT=jukahub-board-v1"
	echo "DEVICE=$DEVICE"
	echo "MODEL=$MODEL"
	[ "$ANY_MODEL" = 1 ] && echo "EXPERIMENTAL=1"
	echo "PANEL=$PANEL"
	echo "FIRMWARE=$FIRMWARE"
	echo "KERNEL=$(uname -r)"
	echo "MACHINE=$(uname -m)"
	echo "EMMC=$EMMC"
	echo "SD=$SD"
	echo "ROOT_PART=$ROOT_PART"
	echo "ROOT_NAME=$ROOT_NAME"
	echo "ROOT_FSTYPE=$ROOT_FSTYPE"
	echo "FIRST_LBA=$FIRST"
	echo "LEAD_SECTORS=$FIRST"
	[ -n "$LPJ" ] && echo "LPJ=$LPJ"
} > "$OUT/facts.txt"
cat /proc/cmdline > "$OUT/cmdline.txt"
cat /proc/modules > "$OUT/modules.txt"
cat /proc/filesystems > "$OUT/filesystems.txt"
cat /proc/self/mountinfo > "$OUT/mountinfo.txt"
[ -r /proc/config.gz ] && cat /proc/config.gz > "$OUT/config.gz"

# Processes: the hardware daemons the stock system runs next to its menu.
# pid <TAB> exe <TAB> cwd <TAB> LD_LIBRARY_PATH <TAB> argv joined by 0x1f.
SELF=$$
: > "$OUT/procs.txt"
for p in /proc/[0-9]*; do
	pid=${p#/proc/}
	[ "$pid" = "$SELF" ] && continue
	cmd=$(tr '\000' '\037' < "$p/cmdline" 2> /dev/null)
	[ -n "$cmd" ] || continue
	exe=$(readlink "$p/exe" 2> /dev/null)
	cwd=$(readlink "$p/cwd" 2> /dev/null)
	ld=$(tr '\000' '\n' < "$p/environ" 2> /dev/null | sed -n 's/^LD_LIBRARY_PATH=//p' | head -n 1)
	printf '%s\t%s\t%s\t%s\t%s\n' "$pid" "$exe" "$cwd" "$ld" "$cmd" >> "$OUT/procs.txt"
done

# GPIO lines the stock system drives (the sticks' power, for one).
: > "$OUT/gpio.txt"
for g in /sys/class/gpio/gpio[0-9]*; do
	[ -r "$g/direction" ] || continue
	printf '%s %s %s\n' "${g##*gpio}" "$(cat "$g/direction")" "$(cat "$g/value" 2> /dev/null)" >> "$OUT/gpio.txt"
done

# Stick calibration, kept by the stock system on the internal user partition.
for f in /mnt/UDISK/joypad.config /mnt/UDISK/joypad_right.config; do
	[ -f "$f" ] && cp "$f" "$OUT/udisk/"
done
command -v alsactl > /dev/null 2>&1 && alsactl -f "$OUT/asound.state" store 2> /dev/null

if command -v sha256sum > /dev/null 2>&1; then
	(cd "$OUT" && find . -type f ! -name SHA256SUMS | sort | xargs sha256sum > SHA256SUMS)
fi
sync
date > "$OUT/COMPLETE"
sync
say "done. Copy $OUT to the Linux computer that builds the image."
