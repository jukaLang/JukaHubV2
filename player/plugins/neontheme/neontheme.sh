#!/bin/sh
# neontheme — adds the Neon Grid theme. The theme is neon-grid.json, declared
# in plugin.json ("themes"), so it is in the theme gallery without running
# anything; this script only describes it for the Plugins page.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$action" in
    about)
        printf '%s\n' \
            'Neon Grid: lime light on a deep teal night.' \
            'Pick it in Settings > Display & theme > Theme presets.'
        ;;
    *)
        printf 'Unknown action: %s (try about)\n' "$action" >&2
        exit 1
        ;;
esac
