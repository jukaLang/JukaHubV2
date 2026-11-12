//go:build !linux && !windows

package main

// memstat_other.go — systems JukaHub does not ship for report nothing; the
// read-outs leave the line out (memstat.go).

// readSelfMemory has no reading here.
func readSelfMemory() selfMemStat { return selfMemStat{} }
