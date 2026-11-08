#!/bin/sh
# retroformats — teaches JukaHub's game library formats it does not know out
# of the box. The formats are declared in plugin.json ("romParsers"), so the
# library recognises them without running anything; this script only lists
# them for the Plugins page.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$action" in
    list)
        printf '%s\n' \
            'Pokemon mini (.min)' \
            'Watara Supervision (.sv)' \
            'Vectrex (.vec, .gam; checks the GCE header)' \
            'Atari Lynx (.lnx; checks the LYNX header)' \
            'Arduboy (.arduboy; checks the archive header)' \
            'WASM-4 (.wasm; checks the WebAssembly header)'
        ;;
    *)
        printf 'Unknown action: %s (try list)\n' "$action" >&2
        exit 1
        ;;
esac
