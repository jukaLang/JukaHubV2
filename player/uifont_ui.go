package main

// uifont_ui.go — Settings → Display → Font and Remove a font (uifont.go).
//
// Font is a choice row whose Left/Right step only through faces already on
// the card (Inter, downloaded ones, copied ones), so stepping never starts a
// download; A opens the whole list, where a face that is not installed says
// how big its download is and is fetched, installed and applied when picked
// (its value carries the "get:" mark, the only value that downloads). Remove a
// font lists what can be deleted, asks before deleting, and shows only when
// there is something to delete.

import (
	"strconv"
	"strings"
)

// uiFontInstalledOptions are the faces that apply without a download: Inter,
// then the downloaded catalogue faces, then the copied ones.
func uiFontInstalledOptions(*Config) []setOption {
	out := []setOption{{Value: uiFontDefaultValue, Label: "Inter"}}
	for _, e := range uiFontCatalog {
		if ok, _ := uiFontInstalled(e); ok {
			out = append(out, setOption{Value: e.ID, Label: e.Name})
		}
	}
	for _, f := range uiFontCardFiles() {
		out = append(out, setOption{Value: uiFontFilePrefix + f.Name, Label: uiFontCardLabel(f.Name)})
	}
	return out
}

// uiFontSizeLabel is an approximate download, "about 150 KB" / "about 4.7 MB".
func uiFontSizeLabel(kb int) string {
	if kb >= 1000 {
		mb := strconv.FormatFloat(float64(kb)/1000, 'f', 1, 64)
		return "about " + strings.TrimSuffix(mb, ".0") + " MB"
	}
	return "about " + strconv.Itoa(kb) + " KB"
}

// uiFontSheetOptions is the whole list: Inter, every catalogue face with
// whether it is installed or how big its download is, and the copied faces.
func uiFontSheetOptions(*Config) []setOption {
	busy, pct := uiFontDownloadStatus()
	russian := uiLanguageCode() == "ru"
	out := []setOption{{Value: uiFontDefaultValue, Label: "Inter", Note: "Built in · the default"}}
	for _, e := range uiFontCatalog {
		note := ""
		value := e.ID
		switch installed, _ := uiFontInstalled(e); {
		case busy == e.ID:
			note = "Downloading… " + strconv.Itoa(pct) + "%"
			value = uiFontGetPrefix + e.ID
		case installed:
			note = "Installed · " + e.Note
		default:
			note = "Download " + uiFontSizeLabel(e.KB) + " · " + e.Note
			value = uiFontGetPrefix + e.ID
		}
		if russian && !e.Cyrillic {
			// A Latin-only face cannot draw a Russian interface
			// (uiFontLanguageGap refuses it): say so before the download.
			note += " · Latin letters only"
		}
		out = append(out, setOption{Value: value, Label: e.Name, Note: note})
	}
	for _, f := range uiFontCardFiles() {
		out = append(out, setOption{Value: uiFontFilePrefix + f.Name, Label: uiFontCardLabel(f.Name),
			Note: "From your card · " + f.Name})
	}
	return out
}

// uiFontCurrentValue is the choice as the lists name it ("inter" for the
// bundled face).
func uiFontCurrentValue(*Config) string {
	if choice := uiFontChoice(); choice != "" {
		return choice
	}
	return uiFontDefaultValue
}

// uiFontRemovable lists what Remove a font can delete, with the space each
// takes.
func uiFontRemovable(*Config) []setOption {
	var out []setOption
	for _, e := range uiFontCatalog {
		if ok, size := uiFontInstalled(e); ok {
			out = append(out, setOption{Value: e.ID, Label: e.Name, Note: humanBytes(size)})
		}
	}
	for _, f := range uiFontCardFiles() {
		out = append(out, setOption{Value: uiFontFilePrefix + f.Name, Label: uiFontCardLabel(f.Name),
			Note: "From your card · " + humanBytes(f.Size)})
	}
	return out
}

// uiFontSettingsRows are the Display page's font rows.
func uiFontSettingsRows() []setRow {
	return []setRow{
		{
			ID: "uiFont", Label: "Font", Kind: setKindChoice,
			Detail: func(*Config) string {
				if busy, pct := uiFontDownloadStatus(); busy != "" {
					return "Downloading " + uiFontChoiceName(busy) + "… " + strconv.Itoa(pct) + "%"
				}
				if a11y.legibleFont {
					return "Legible reading text is on (Accessibility), so it picks the face"
				}
				return "Inter is built in · only the fonts you pick are downloaded"
			},
			Options:    uiFontInstalledOptions,
			Sheet:      uiFontSheetOptions,
			SheetValue: uiFontCurrentValue,
			SheetTitle: "Font",
			// No stop while a download runs or while the stored face is not
			// what the screen draws: the read-out (Value) says which.
			Current: func(c *Config) int {
				if busy, _ := uiFontDownloadStatus(); busy != "" || uiFontFallbackNote() != "" {
					return -1
				}
				return ledOptionIndex(uiFontInstalledOptions(c), uiFontCurrentValue(c))
			},
			Value: func(*Config) string {
				if busy, pct := uiFontDownloadStatus(); busy != "" {
					return "Downloading " + strconv.Itoa(pct) + "%"
				}
				if note := uiFontFallbackNote(); note != "" {
					return note
				}
				return uiFontChoiceName(uiFontChoice())
			},
			Choose: uiFontChoose,
		},
		{
			ID: "uiFontRemove", Label: "Remove a font", Kind: setKindChoice,
			Detail:     setStatic("Deletes a downloaded font; the one in use goes back to Inter"),
			Visible:    func(c *Config) bool { return len(uiFontRemovable(c)) > 0 },
			Sheet:      uiFontRemovable,
			SheetValue: func(*Config) string { return "" },
			SheetTitle: "Remove a font",
			Value: func(c *Config) string {
				var total int64
				n := 0
				for _, e := range uiFontCatalog {
					if ok, size := uiFontInstalled(e); ok {
						total += size
						n++
					}
				}
				for _, f := range uiFontCardFiles() {
					total += f.Size
					n++
				}
				if n == 1 {
					return "1 font · " + humanBytes(total)
				}
				return strconv.Itoa(n) + " fonts · " + humanBytes(total)
			},
			// Asked first: the sheet's cursor starts on the first font, and a
			// habitual second A must not delete it.
			Choose: func(c *Config, value string) {
				name := uiFontChoiceName(value)
				setConfirm(c, "Remove "+name+"?",
					"The font file is deleted from the fonts folder. If it is the one in use, JukaHub goes back to Inter.",
					"trash", "Remove", func(c *Config) { uiFontRemove(c, value) })
			},
		},
	}
}
