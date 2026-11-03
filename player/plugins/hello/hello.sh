#!/bin/sh
# JukaHub passes the action first, followed by --name=value custom variables.
action=${1-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$action" in
    greet)
        name=friend
        for arg in "$@"; do
            case "$arg" in
                --plugin_name=*) name=${arg#*=} ;;
            esac
        done
        [ -n "$name" ] || name=friend
        printf 'Hello, %s!\n' "$name"
        ;;
    *)
        printf 'Unknown action: %s (try greet)\n' "$action" >&2
        exit 1
        ;;
esac
