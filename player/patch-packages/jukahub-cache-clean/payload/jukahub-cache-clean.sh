#!/bin/sh
# JukaHub cache cleaner (installed by the Patch package manager).
# Removes only JukaHub-owned temporary files; never touches user data.
set -eu
CACHE_DIR="${1:-.cache}"
if [ -d "$CACHE_DIR" ]; then
    BEFORE=$(du -sk "$CACHE_DIR" 2>/dev/null | awk '{print $1}')
    find "$CACHE_DIR" -type f \( -name '*.part' -o -name '*.tmp' \) -delete 2>/dev/null || true
    AFTER=$(du -sk "$CACHE_DIR" 2>/dev/null | awk '{print $1}')
    echo "cache cleaned: ${BEFORE:-0} KB -> ${AFTER:-0} KB"
else
    echo "no cache directory found"
fi
