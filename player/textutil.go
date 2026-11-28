package main

// textutil.go keeps the old name as a thin alias over the cacheable leaf
// package internal/textutil (ARCH-1).

import jhtext "jukahub/JukaHub/internal/textutil"

func asciiLower(s string) string { return jhtext.AsciiLower(s) }
