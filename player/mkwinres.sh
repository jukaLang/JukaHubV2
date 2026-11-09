#!/usr/bin/env bash
# Rebuild the Windows icon resource from JukaHub.ico.
#
# Windows takes a program's icon from a resource compiled into the .exe; a file
# sitting next to it is ignored. So the artwork becomes:
#
#   branding/mklogo.py  --(numpy, Pillow)-->  JukaHub.ico (16-256 px, hinted)
#   JukaHub.ico + JukaHub.rc  --(windres)-->  rsrc_windows_amd64.syso
#
# The Go linker links any *.syso sitting in the package directory, so a plain
# `go build` on windows/amd64 picks the icon up with no extra flags, and so does
# CI. Both files are committed, and branding/mklogo.py writes the .syso too (it
# lays the object out the way windres does), so this script is only needed to
# check that windres agrees, or after editing JukaHub.rc:
#
#   ./mkwinres.sh
#
# It no longer makes the .ico from JukaPlayer.png: the small sizes in the .ico
# are drawn for their pixel grid (a simpler mark, snapped edges), which a
# downscale of the 300 px artwork would throw away. To change the artwork,
# edit and run branding/mklogo.py instead.
#
# Requires windres from MinGW-w64 (build.bat installs one under
# %LOCALAPPDATA%\JukaHub\tools\w64devkit).
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -f JukaHub.ico ]; then
    echo "error: JukaHub.ico is missing; run python3 ../branding/mklogo.py" >&2
    exit 1
fi

WINDRES="${WINDRES:-}"
if [ -z "$WINDRES" ]; then
    for candidate in \
        "$(command -v windres || true)" \
        "${LOCALAPPDATA:-$HOME/AppData/Local}/JukaHub/tools/w64devkit/bin/windres.exe" \
        /c/msys64/ucrt64/bin/windres.exe \
        /c/msys64/mingw64/bin/windres.exe
    do
        if [ -n "$candidate" ] && [ -x "$candidate" ]; then
            WINDRES="$candidate"
            break
        fi
    done
fi
if [ -z "$WINDRES" ]; then
    echo "error: windres not found; set WINDRES=/path/to/windres" >&2
    echo "       (python3 ../branding/mklogo.py also writes the .syso)" >&2
    exit 1
fi

# -O coff writes a COFF object; the target architecture comes from windres
# itself (pe-x86-64 here), which is what the file name promises Go.
"$WINDRES" -i JukaHub.rc -o rsrc_windows_amd64.syso -O coff
echo "rsrc_windows_amd64.syso: $(wc -c < rsrc_windows_amd64.syso) bytes"
