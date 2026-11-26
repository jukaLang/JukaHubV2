package main

// Box64's FreeType wrapper asks the native loader for libfreetype.so, even
// though TrimUI ships only the runtime SONAME libfreetype.so.6. A per-launch
// alias in /tmp gives Wine its fonts without changing firmware or copying an
// LGPL library onto the SD card.

import (
	"debug/elf"
	"slices"
)

const wineFreeTypeAlias = "libfreetype.so"

// wineFreeTypeSource uses the firmware's native FreeType. On the Smart Pro its
// dependency on zlib matches the firmware's libz. WestonPack also carries a
// FreeType, but its libpng needs ZLIB_1.2.9 while this firmware loads zlib
// 1.2.8; using that copy yields the same black, unreadable Wine window.
func wineFreeTypeSource() string {
	return wineFreeTypeFrom([]string{
		"/usr/lib/libfreetype.so.6",
		"/usr/lib64/libfreetype.so.6",
		"/usr/trimui/lib/libfreetype.so.6",
		"/lib/libfreetype.so.6",
	})
}

func wineFreeTypeFrom(paths []string) string {
	for _, path := range paths {
		f, err := elf.Open(path)
		if err != nil {
			continue
		}
		soname, sonameErr := f.DynString(elf.DT_SONAME)
		valid := f.Class == elf.ELFCLASS64 && f.Machine == elf.EM_AARCH64 &&
			f.Type == elf.ET_DYN && sonameErr == nil && slices.Contains(soname, "libfreetype.so.6")
		_ = f.Close()
		if valid {
			return path
		}
	}
	return ""
}
