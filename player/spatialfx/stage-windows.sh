#!/bin/sh
# stage-windows.sh — put the spatial game-audio plugin into a Windows release
# folder, at lib/jukahub-spatial.dll beside JukaHub.exe.
#
#   CROSS=x86_64-w64-mingw32- sh player/spatialfx/stage-windows.sh <release-dir>
#
# defaultSpatialPluginLocate (player/spatial_games.go) looks in lib/ beside the
# binary, then in the binary's own folder. build.sh checks the DLL's
# architecture, imported DLLs and exported filter symbols; a failed build leaves
# the folder without a library rather than with an unchecked old one.
set -eu
if [ "$#" -ne 1 ] || [ -z "$1" ]; then
    echo "usage: $0 <release-dir>" >&2
    exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
dest=$1
mkdir -p "$dest/lib"
rm -f "$dest/lib/jukahub-spatial.dll"
work="$dest/.spatial-build"
rm -rf "$work"
trap 'rm -rf "$work"' EXIT
TARGET=windows OUT="$work" sh "$here/build.sh"
cp "$work/jukahub-spatial.dll" "$dest/lib/jukahub-spatial.dll"
