//go:build linux

package main

// tzdata_linux.go — the IANA zone database inside the binary (about 450 KB).
//
// The TrimUI image may ship without /usr/share/zoneinfo, and Go then knows no
// zone but UTC: time.LoadLocation fails, and a TZ="Europe/Berlin" exported by
// launch.sh silently falls back to UTC. With time/tzdata linked in, both read
// the embedded copy whenever the system directory is missing or lacks a zone
// (the system copy still wins when it has one). Windows does not need it: the
// zone there is the operating system's (datetime.go), and nothing on that path
// loads a zone by name.

import _ "time/tzdata"
