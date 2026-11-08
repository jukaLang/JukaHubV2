#!/bin/sh
# padprofiles — adds the Couch remote control profile. The profile is
# couch-remote.jkinput, declared in plugin.json ("inputProfiles"), so it is in
# Settings > Controls without running anything; this script only describes it.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$action" in
    about)
        printf '%s\n' \
            'Couch remote: L1 and R1 page through lists,' \
            'hold Y to search, hold Start for Quick Settings,' \
            'double-tap Select for a screenshot.'
        ;;
    *)
        printf 'Unknown action: %s (try about)\n' "$action" >&2
        exit 1
        ;;
esac
