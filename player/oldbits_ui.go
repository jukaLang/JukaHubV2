package main

// oldbits_ui.go — the small native touches for the OldBits console: the
// author credit on the game's detail page and a row that opens the OldBits
// catalog in JukaHub's own browser. The hub itself still runs the downloads
// and installs; JukaHub supplies the entry point and the credit.

func init() {
	gamePageRegisterExtra(gamePageExtra{
		ID: "oldbits-catalog", Order: 62, Icon: "packages",
		Row:  oldBitsCatalogRow,
		Open: oldBitsCatalogOpen,
	})
	gamePageRegisterStatus(gamePageStatus{
		ID: "oldbits-credit", Order: 11, Chip: oldBitsCreditChip,
	})
}

func oldBitsIsEntry(game libraryGame) bool {
	return game.System == oldBitsLibrarySystem
}

func oldBitsCatalogRow(game libraryGame) (string, string, bool) {
	if !oldBitsIsEntry(game) {
		return "", "", false
	}
	return "OldBits catalog", "Browse games on oldbits.net", true
}

func oldBitsCatalogOpen(game libraryGame) {
	if !oldBitsIsEntry(game) {
		return
	}
	browserOpenURL(appConfig, "https://oldbits.net/catalog")
}

// oldBitsCreditChip credits the author on the entry's detail page.
func oldBitsCreditChip(game libraryGame) (string, int, bool) {
	if !oldBitsIsEntry(game) {
		return "", gamePageToneNeutral, false
	}
	return "by OldBits", gamePageToneNeutral, true
}
