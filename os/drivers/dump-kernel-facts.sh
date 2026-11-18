#!/bin/sh
# Save what a kernel-module pack for this TrimUI handheld has to match.
#
# Runs on the Smart Pro (TG5040), Brick (TG3040), Brick Pro (TG4040) and
# Smart Pro S (TG5050), on the stock firmware, CrossMix, NextUI or spruceOS:
#   sh /mnt/SDCARD/dump-kernel-facts.sh        -> /mnt/SDCARD/kernel-facts/<MODEL>-<release>/
#   sh dump-kernel-facts.sh /some/folder       -> /some/folder/<MODEL>-<release>/
#
# It only READS: nothing on the device is changed, and it writes only into the
# output folder on the card. What it saves (no personal data, no firmware
# images):
#   facts.txt      MODEL, DEVICE, RELEASE, MACHINE, VERMAGIC, GCC, the config
#                  switches a module build depends on (MODVERSIONS, signing)
#   config.gz      the running kernel's configuration (/proc/config.gz), when
#                  the kernel exposes it: without it no module can be built
#   version.txt    /proc/version (the compiler the kernel was built with)
#   uname.txt, cpuinfo.txt, modules.txt (/proc/modules), lib-modules.txt
#   input.txt      /proc/bus/input/devices
#   dmesg.txt      the kernel log's driver lines (USB, HID, input, Wi-Fi, BT, GPU)
#   ko/            up to three small stock .ko files: their vermagic and symbol
#                  CRCs (__versions) are what a built module is compared with
# Copy the folder to os/drivers/facts/ in the JukaHub source and run the
# "OS driver pack" workflow (.github/workflows/os-drivers.yml) with it.
set -u
PATH=/usr/sbin:/usr/bin:/sbin:/bin:$PATH
export PATH

say() { echo "dump-kernel-facts: $*"; }

oneline() { tr '\0\n' '  ' 2>/dev/null; }

DT_MODEL=$(oneline 2>/dev/null < /proc/device-tree/model)
DT_COMPAT=$(oneline 2>/dev/null < /proc/device-tree/compatible)
HOST=$(cat /etc/hostname 2>/dev/null)
MODEL=$(printf '%s %s %s' "$DT_MODEL" "$DT_COMPAT" "$HOST" | grep -o -i 'tg[0-9][0-9][0-9][0-9]' | head -n 1 | tr 'a-z' 'A-Z')
if [ -z "$MODEL" ] && [ -r /usr/trimui/bin/MainUI ]; then
	case "$(grep -a -o 'Trimui Smart Pro S\|Trimui Smart Pro\|Trimui Brick Pro\|Trimui Brick' /usr/trimui/bin/MainUI 2>/dev/null | head -n 1)" in
	"Trimui Smart Pro S") MODEL=TG5050 ;;
	"Trimui Smart Pro") MODEL=TG5040 ;;
	"Trimui Brick Pro") MODEL=TG4040 ;;
	"Trimui Brick") MODEL=TG3040 ;;
	esac
fi
case "$MODEL" in
TG5040) DEVICE=trimui-smart-pro ;;
TG5050) DEVICE=trimui-smart-pro-s ;;
TG3040) DEVICE=trimui-brick ;;
TG4040) DEVICE=trimui-brick-pro ;;
*) MODEL=unknown DEVICE=unknown ;;
esac
RELEASE=$(uname -r)
MACHINE=$(uname -m)
case "$RELEASE" in
*[!A-Za-z0-9._+-]*|"") say "odd kernel release '$RELEASE'"; exit 1 ;;
esac

BASE=${1:-$(cd "$(dirname "$0")" 2>/dev/null && pwd)/kernel-facts}
OUT=$BASE/$MODEL-$RELEASE
if [ -e "$OUT" ]; then
	say "$OUT already exists; move it away first"
	exit 1
fi
mkdir -p "$OUT/ko" || { say "cannot write $OUT"; exit 1; }

uname -a > "$OUT/uname.txt" 2>/dev/null
cat /proc/version > "$OUT/version.txt" 2>/dev/null
cat /proc/cpuinfo > "$OUT/cpuinfo.txt" 2>/dev/null
cat /proc/modules > "$OUT/modules.txt" 2>/dev/null
cat /proc/bus/input/devices > "$OUT/input.txt" 2>/dev/null
CONFIG=no
if [ -r /proc/config.gz ]; then
	cat /proc/config.gz > "$OUT/config.gz" && CONFIG=yes
fi
ls -lR "/lib/modules/$RELEASE" > "$OUT/lib-modules.txt" 2>/dev/null ||
	ls -lR /lib/modules > "$OUT/lib-modules.txt" 2>/dev/null
dmesg 2>/dev/null | grep -i -E 'usb|hid|input|joystick|xpad|uinput|bluetooth|hci|wlan|wifi|aic|xradio|rtl|sdio|mmc|mali|pvr|gpu|devfreq|thermal|fan' |
	tail -n 600 > "$OUT/dmesg.txt"

# A stock module's vermagic: modinfo when there is one, else the string in the
# file. The three smallest modules are copied for their symbol CRCs.
VERMAGIC=""
KOS=$(find /lib/modules -name '*.ko' -type f 2>/dev/null | while read -r f; do
	printf '%s %s\n' "$(wc -c < "$f" 2>/dev/null)" "$f"
done | sort -n | head -n 3 | cut -d' ' -f2-)
MODVERSIONED=no
for ko in $KOS; do
	cp "$ko" "$OUT/ko/" 2>/dev/null
	v=""
	command -v modinfo >/dev/null 2>&1 && v=$(modinfo -F vermagic "$ko" 2>/dev/null)
	[ -n "$v" ] || v=$(tr '\0' '\n' < "$ko" 2>/dev/null | grep -m 1 '^vermagic=' | cut -d= -f2-)
	[ -n "$VERMAGIC" ] || VERMAGIC=$v
	tr '\0' '\n' < "$ko" 2>/dev/null | grep -q -x '__versions' && MODVERSIONED=yes
done

cfg() {
	[ "$CONFIG" = yes ] || return 0
	zcat "$OUT/config.gz" 2>/dev/null | grep -E "^$1=" | head -n 1 | cut -d= -f2-
}
{
	echo "FORMAT=jukahub-kernel-facts-v1"
	echo "MODEL=$MODEL"
	echo "DEVICE=$DEVICE"
	echo "RELEASE=$RELEASE"
	echo "MACHINE=$MACHINE"
	echo "VERMAGIC=$VERMAGIC"
	echo "GCC=$(grep -o 'gcc[^)]*[0-9][.][0-9][^ )]*' /proc/version 2>/dev/null | head -n 1)"
	echo "DT_MODEL=$DT_MODEL"
	echo "DT_COMPATIBLE=$DT_COMPAT"
	echo "CONFIG_GZ=$CONFIG"
	echo "STOCK_MODULES_VERSIONED=$MODVERSIONED"
	for key in CONFIG_LOCALVERSION CONFIG_MODVERSIONS CONFIG_MODULE_SIG CONFIG_MODULE_SIG_FORCE \
		CONFIG_MODULES CONFIG_INPUT_UINPUT CONFIG_HID CONFIG_USB_HID CONFIG_HIDRAW CONFIG_UHID \
		CONFIG_JOYSTICK_XPAD CONFIG_HID_SONY CONFIG_HID_PLAYSTATION CONFIG_HID_NINTENDO \
		CONFIG_HID_MICROSOFT CONFIG_BT CONFIG_BT_HIDP CONFIG_CFG80211 CONFIG_MAC80211; do
		echo "$key=$(cfg "$key")"
	done
	echo "DEV_UINPUT=$([ -e /dev/uinput ] && echo yes || echo no)"
} > "$OUT/facts.txt"
sync

say "saved to $OUT"
say "model $MODEL, kernel $RELEASE, vermagic '${VERMAGIC:-unknown}', config ${CONFIG}"
[ "$CONFIG" = yes ] || say "this kernel has no /proc/config.gz: a module pack cannot be built for it"
exit 0
