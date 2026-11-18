//go:build linux

package main

import "syscall"

// feVolumeOf reads what the file system at dir means for an extraction's plan
// (feVolume): statfs's block size is the cluster on vfat, exFAT (kernel and
// FUSE drivers) and the block on ext4, and its magic tells FAT from exFAT. A
// volume that cannot be read is a zero feVolume, counted in 4 KiB units.
func feVolumeOf(dir string) feVolume {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return feVolume{}
	}
	v := feVolume{cluster: int64(st.Bsize)}
	switch int64(st.Type) {
	case 0x4d44: // MSDOS_SUPER_MAGIC: vfat and msdos, FAT12/16/32
		v.fat, v.fatLike = true, true
	case 0x2011bab0, 0x5ec5dfa4: // EXFAT_SUPER_MAGIC; Samsung's sdfat (FAT32 or exFAT)
		v.fatLike = true
	}
	return v
}
