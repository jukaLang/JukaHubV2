#!/bin/sh
# classicpack — adds the Pocket Classic experience pack. The pack is
# pocket-classic.json, declared in plugin.json ("packs"), so it is in Tools >
# Experience packs without running anything; this script only describes it.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$action" in
    about)
        printf '%s\n' \
            'Pocket Classic: the Game Boy palette, the hero-tile Home,' \
            'the Gaming controls and an Arcade Corner screen.' \
            'Apply it in Tools > Experience packs; Y there undoes it.'
        ;;
    *)
        printf 'Unknown action: %s (try about)\n' "$action" >&2
        exit 1
        ;;
esac
