//go:build !linux && !windows

package main

// feVolumeOf has no file system to ask here: the extraction counts in 4 KiB
// units and a FAT card's 4 GiB limit is met when the write fails.
func feVolumeOf(string) feVolume {
	return feVolume{}
}
