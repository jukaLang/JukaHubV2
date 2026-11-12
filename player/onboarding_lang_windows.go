//go:build windows

package main

// onboarding_lang_windows.go — Windows' user locale for the tour's language
// page (onboarding_lang.go). Windows sets no LANG, so the locale comes from
// GetUserDefaultLocaleName ("de-DE", "pt-BR").

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// uiSystemLocale is the user's locale name, or "" when Windows does not say.
func uiSystemLocale() string {
	if procGetUserDefaultLocaleName.Find() != nil {
		return ""
	}
	var buf [85]uint16 // LOCALE_NAME_MAX_LENGTH
	n, _, _ := procGetUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}
