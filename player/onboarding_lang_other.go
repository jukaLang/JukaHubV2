//go:build !windows

package main

// onboarding_lang_other.go — off Windows the locale is the LANG family of
// variables, which uiDetectSystemLanguage already reads (onboarding_lang.go).

// uiSystemLocale has nothing beyond the environment to add here.
func uiSystemLocale() string { return "" }
