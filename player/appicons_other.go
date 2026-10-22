//go:build !windows

package main

// appicons_other.go covers the platforms that have no icon extractor: on
// Linux/handheld builds an installed app ships its own image inside its app
// folder, which apps.go already picks up, so there is nothing to extract.

// appIconCacheFile has no cache outside Windows.
func appIconCacheFile(string) string { return "" }

// extractedAppIcon never finds an icon outside Windows.
func extractedAppIcon(string) string { return "" }

// requestAppIconExtraction is a no-op outside Windows.
func requestAppIconExtraction([]string) {}
