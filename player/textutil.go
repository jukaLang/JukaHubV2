package main

// textutil.go — byte-preserving text helpers.

// asciiLower lowercases only A–Z and leaves every other byte alone, so the
// result has exactly the length of its input. Index-based scanners search a
// lowercased copy and slice the original with the offsets they find; with
// strings.ToLower that is only safe for ASCII. ToLower turns each invalid
// UTF-8 byte (a Latin-1 page the reader did not transcode) into a three-byte
// U+FFFD and changes the length of some valid letters too ("İ", the Kelvin
// sign), so the offsets drifted — wrong cuts at best, a slice-bounds panic at
// worst. HTML tag names and CSS keywords are ASCII, so nothing is lost.
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c >= 'A' && c <= 'Z' {
					b[j] = c + ('a' - 'A')
				}
			}
			return string(b)
		}
	}
	return s
}
