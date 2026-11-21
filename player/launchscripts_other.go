//go:build !linux

package main

// launchscripts_other.go — launch scripts and the screenshot request file are
// a Linux handheld feature (launchscripts.go, launchscripts_linux.go): a
// desktop has no /bin/sh hooks folder to run and no /tmp/jukahub to watch.

import "time"

// launchScriptRunOne runs nothing here.
func launchScriptRunOne(path string, env []string) (int, time.Duration, []byte, error) {
	return 0, 0, nil, nil
}

// shotRequestChildEnv leaves the environment as it is here.
func shotRequestChildEnv(env []string) []string { return env }

// shotRequestWatch has nothing to watch here.
func shotRequestWatch() {}
