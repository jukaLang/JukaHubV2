#!/bin/sh
# The action id is $1; custom variables follow as --name=value.
case "$1" in
    hello) echo "Hello from hello-plugin" ;;
    *) echo "Unknown action: $1" >&2; exit 1 ;;
esac