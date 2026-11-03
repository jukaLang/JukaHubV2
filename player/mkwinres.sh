#!/usr/bin/env bash
# Rebuild the Windows icon resource from JukaPlayer.png.
#
# Windows takes a program's icon from a resource compiled into the .exe; a file
# sitting next to it is ignored. So the artwork becomes:
#
#   JukaPlayer.png  --(this script, Pillow)-->  JukaHub.ico
#   JukaHub.ico + JukaHub.rc  --(windres)-->    rsrc_windows_amd64.syso
#
# The Go linker links any *.syso sitting in the package directory, so a plain
# `go build` on windows/amd64 picks the icon up with no extra flags, and so does
# CI. Both generated files are committed, which means this script only needs to
# run when the artwork changes:
#
#   ./mkwinres.sh
#
# Requires Python 3 with Pillow, and windres from MinGW-w64 (build.bat installs
# one under %LOCALAPPDATA%\JukaHub\tools\w64devkit).
set -euo pipefail
cd "$(dirname "$0")"

PYTHON="${PYTHON:-python3}"
if ! command -v "$PYTHON" >/dev/null 2>&1; then
    PYTHON=python
fi
if ! command -v "$PYTHON" >/dev/null 2>&1; then
    echo "error: no python3/python on PATH (needed for the .ico)" >&2
    exit 1
fi

"$PYTHON" - <<'PY'
from PIL import Image

# The sizes Windows actually asks for: Explorer's small and large views, the
# taskbar, Alt-Tab, large icons and the 256px jumbo tile.
SIZES = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]

src = Image.open("JukaPlayer.png").convert("RGBA")
src.save("JukaHub.ico", format="ICO", sizes=SIZES)
print("JukaHub.ico: %d sizes from %dx%d source" % (len(SIZES), src.width, src.height))
PY

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
    exit 1
fi

# -O coff writes a COFF object; the target architecture comes from windres
# itself (pe-x86-64 here), which is what the file name promises Go.
"$WINDRES" -i JukaHub.rc -o rsrc_windows_amd64.syso -O coff
echo "rsrc_windows_amd64.syso: $(wc -c < rsrc_windows_amd64.syso) bytes"
