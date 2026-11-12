package main

// gamediag_bios.go — which firmware files a game's core cannot start without,
// and the one folder this device's RetroArch reads them from.
//
// The table is per core, not per console, on purpose. "Saturn needs a BIOS"
// is only true for Beetle Saturn; YabaSanshiro boots without one. "PlayStation
// needs a BIOS" is false for PCSX ReARMed, the handheld's usual core, and true
// for Beetle PSX and SwanStation. A diagnosis that names a file the core does
// not want sends the player hunting for nothing, so every row says which cores
// REQUIRE its files (gameDiagSure: "the game cannot start without it") and
// which cores only MAY want them (gameDiagMaybe: a built-in replacement boots
// most games). A core in neither list is not told about the row at all.
//
// Where the files go is read, not guessed: RetroArch's system_directory from
// the retroarch.cfg the launcher's RetroArch uses (the same config search the
// launch-time edits use, racfgscope.go), resolved the way RetroArch resolves
// it — ":" is the folder of the RetroArch binary, "~" its HOME (the TrimUI
// launch scripts set HOME to the RetroArch folder), "default" or nothing is
// the game's own folder. CrossMix points it at /mnt/SDCARD/BIOS; the stock
// firmware's config decides for itself. When no config is readable the
// diagnosis says "probably" instead of pretending.
//
// BIOS Doctor (biosdoctor.go) lists the same table, so the two never disagree
// about what a console needs.

import (
	"os"
	"path/filepath"
	"strings"
)

// gameDiagLevel is how certain a finding is. Lower sorts first.
type gameDiagLevel int

const (
	gameDiagSure gameDiagLevel = iota
	gameDiagLikely
	gameDiagMaybe
)

// gameDiagBIOS is one row of the required-firmware table. Required lists the
// cores that cannot start without the files; "*" is every core not in Maybe
// or Skip. Maybe lists cores that boot most games without them; "*" is every
// other core. Skip cores are never told about the row.
type gameDiagBIOS struct {
	System   string     // the console, as the dialog names it
	Keys     []string   // system names and ROM folder names that select the row (lower case)
	Exts     []string   // when set, only ROMs with one of these extensions
	Required []string   // cores that need the files
	Maybe    []string   // cores that may want them
	Skip     []string   // cores the row is not about
	Groups   [][]string // each group needs one of its names
	Sub      string     // the folder under the system directory ("" for itself)
	RomDir   bool       // the core also looks in the game's own folder
	Note     string     // one sentence for the dialog
}

// gameDiagBIOSTable is the firmware the TrimUI cores and RetroArch's common
// cores ask for by name (libretro core documentation, "BIOS" sections).
var gameDiagBIOSTable = []gameDiagBIOS{
	{
		System:   "Sega CD",
		Keys:     []string{"sega cd", "segacd", "mega cd", "megacd", "mega-cd", "scd", "sega - mega-cd - sega cd"},
		Required: []string{"genesis_plus_gx", "genesis_plus_gx_wide", "picodrive"},
		Maybe:    []string{"*"},
		Groups:   [][]string{{"bios_CD_U.bin", "bios_CD_E.bin", "bios_CD_J.bin"}},
		Note:     "One file is enough: the one for the disc's region (U = USA, E = Europe, J = Japan).",
	},
	{
		System:   "PC Engine CD",
		Keys:     []string{"pc engine cd", "pcenginecd", "pcecd", "pce-cd", "pce cd", "turbografx-cd", "turbografx cd", "tgcd", "tg-cd"},
		Required: []string{"mednafen_pce_fast", "mednafen_pce", "beetle_pce_fast", "beetle_pce", "pce_fast", "pce"},
		Maybe:    []string{"*"},
		Groups:   [][]string{{"syscard3.pce"}},
		Note:     "CD games boot through the System Card 3.0 image.",
	},
	{
		// PC Engine CD games kept in the PC Engine folder.
		System:   "PC Engine CD",
		Keys:     []string{"pc engine", "pce", "tg16", "turbografx-16", "turbografx 16", "nec - pc engine - turbografx 16"},
		Exts:     []string{".cue", ".chd", ".ccd", ".m3u", ".toc", ".iso"},
		Required: []string{"mednafen_pce_fast", "mednafen_pce", "beetle_pce_fast", "beetle_pce", "pce_fast", "pce"},
		Maybe:    []string{"*"},
		Groups:   [][]string{{"syscard3.pce"}},
		Note:     "CD games boot through the System Card 3.0 image.",
	},
	{
		System:   "Neo Geo",
		Keys:     []string{"neo geo", "neogeo", "mvs", "aes", "snk - neo geo"},
		Required: []string{"*"},
		Groups:   [][]string{{"neogeo.zip"}},
		RomDir:   true,
		Note:     "Copy neogeo.zip as it is — do not unzip it. FBNeo also finds it beside the game.",
	},
	{
		System:   "Neo Geo CD",
		Keys:     []string{"neo geo cd", "neogeocd", "ngcd", "snk - neo geo cd"},
		Required: []string{"neocd"},
		Maybe:    []string{"*"},
		Groups: [][]string{
			{"neocd_f.rom", "neocd_sf.rom", "front-sp1.bin", "neocd_t.rom", "neocd_st.rom", "top-sp1.bin", "neocd_z.rom", "neocd_sz.rom", "neocd.bin", "uni-bioscd.rom"},
			{"000-lo.lo", "ng-lo.rom"},
		},
		Sub:  "neocd",
		Note: "NeoCD needs one console BIOS and the zoom table (000-lo.lo).",
	},
	{
		System:   "Famicom Disk System",
		Keys:     []string{"famicom disk system", "fds", "famicom disk", "nintendo - family computer disk system"},
		Exts:     []string{".fds"},
		Required: []string{"*"},
		Groups:   [][]string{{"disksys.rom"}},
		Note:     "Disk games (.fds) boot through the Disk System BIOS.",
	},
	{
		// .fds games kept in the NES folder.
		System:   "Famicom Disk System",
		Keys:     []string{"nes", "fc", "famicom", "nintendo - nintendo entertainment system"},
		Exts:     []string{".fds"},
		Required: []string{"*"},
		Groups:   [][]string{{"disksys.rom"}},
		Note:     "Disk games (.fds) boot through the Disk System BIOS.",
	},
	{
		System:   "Atari Lynx",
		Keys:     []string{"atari lynx", "lynx", "atari - lynx"},
		Required: []string{"handy", "mednafen_lynx", "beetle_lynx"},
		Groups:   [][]string{{"lynxboot.img"}},
	},
	{
		System: "Dreamcast",
		Keys:   []string{"dreamcast", "dc", "sega - dreamcast"},
		Maybe:  []string{"*"},
		Groups: [][]string{{"dc_boot.bin", "dc_bios.bin"}},
		Sub:    "dc",
		Note:   "Flycast boots most games with its built-in BIOS; a few need the real one.",
	},
	{
		System:   "NAOMI",
		Keys:     []string{"naomi", "sega - naomi"},
		Required: []string{"*"},
		Groups:   [][]string{{"naomi.zip"}},
		Sub:      "dc",
		RomDir:   true,
	},
	{
		System:   "Atomiswave",
		Keys:     []string{"atomiswave", "sammy atomiswave"},
		Required: []string{"*"},
		Groups:   [][]string{{"awbios.zip"}},
		Sub:      "dc",
		RomDir:   true,
	},
	{
		System:   "Saturn",
		Keys:     []string{"saturn", "sega saturn", "ss", "sega - saturn"},
		Required: []string{"mednafen_saturn", "beetle_saturn"},
		Groups:   [][]string{{"sega_101.bin", "mpr-17933.bin"}},
		Note:     "sega_101.bin is the Japanese BIOS, mpr-17933.bin the US/European one.",
	},
	{
		System: "Saturn",
		Keys:   []string{"saturn", "sega saturn", "ss", "sega - saturn"},
		Maybe:  []string{"*"},
		Skip:   []string{"mednafen_saturn", "beetle_saturn"},
		Groups: [][]string{{"saturn_bios.bin", "sega_101.bin", "mpr-17933.bin"}},
		Note:   "Yabause and YabaSanshiro boot most games with their built-in BIOS.",
	},
	{
		System:   "PlayStation",
		Keys:     []string{"playstation", "ps", "ps1", "psx", "sony - playstation"},
		Required: []string{"mednafen_psx", "mednafen_psx_hw", "beetle_psx", "beetle_psx_hw"},
		Groups:   [][]string{{"scph5501.bin", "scph5500.bin", "scph5502.bin", "psxonpsp660.bin"}},
		Note:     "scph5501 is the US BIOS, scph5500 the Japanese, scph5502 the European. PCSX ReARMed does not need one.",
	},
	{
		System:   "PlayStation",
		Keys:     []string{"playstation", "ps", "ps1", "psx", "sony - playstation"},
		Required: []string{"swanstation", "duckstation"},
		Groups:   [][]string{{"scph1001.bin", "scph5501.bin", "scph7001.bin", "scph5500.bin", "scph5502.bin", "scph7003.bin", "scph1000.bin", "scph7502.bin", "psxonpsp660.bin"}},
		Note:     "Any PlayStation BIOS image works; scph1001/scph5501 are the US ones.",
	},
	{
		System: "Game Boy Advance",
		Keys:   []string{"game boy advance", "gba", "nintendo - game boy advance"},
		Maybe:  []string{"gpsp"},
		Groups: [][]string{{"gba_bios.bin"}},
		Note:   "gpSP runs most games with its built-in BIOS; mGBA never needs one.",
	},
	{
		System: "Nintendo DS",
		Keys:   []string{"nintendo ds", "nds", "nintendo - nintendo ds"},
		Maybe:  []string{"melonds", "melondsds"},
		Groups: [][]string{{"bios7.bin"}, {"bios9.bin"}, {"firmware.bin"}},
		Note:   "melonDS needs them only when its external-BIOS option is on.",
	},
	{
		System:   "Atari 5200",
		Keys:     []string{"atari 5200", "5200", "a5200", "atari - 5200"},
		Required: []string{"a5200", "atari800"},
		Groups:   [][]string{{"5200.rom", "ATARI5200.ROM"}},
	},
	{
		System:   "Intellivision",
		Keys:     []string{"intellivision", "intv", "mattel - intellivision"},
		Required: []string{"*"},
		Groups:   [][]string{{"exec.bin"}, {"grom.bin"}},
	},
	{
		System:   "Odyssey 2",
		Keys:     []string{"odyssey2", "odyssey 2", "videopac", "magnavox - odyssey2"},
		Required: []string{"o2em"},
		Groups:   [][]string{{"o2rom.bin"}},
	},
	{
		System:   "ColecoVision",
		Keys:     []string{"colecovision", "coleco", "coleco - colecovision"},
		Required: []string{"gearcoleco"},
		Groups:   [][]string{{"colecovision.rom", "coleco.rom"}},
	},
	{
		System:   "MSX / ColecoVision",
		Keys:     []string{"msx", "msx1", "msx2", "msx2+", "colecovision", "coleco", "microsoft - msx", "microsoft - msx2"},
		Required: []string{"bluemsx"},
		Groups:   [][]string{{"Machines"}, {"Databases"}},
		Note:     "blueMSX needs its Machines and Databases folders (from the blueMSX system pack).",
	},
	{
		System: "MSX",
		Keys:   []string{"msx", "msx1", "msx2", "msx2+", "microsoft - msx", "microsoft - msx2"},
		Maybe:  []string{"fmsx"},
		Groups: [][]string{{"MSX.ROM"}, {"MSX2.ROM"}, {"MSX2EXT.ROM"}},
	},
	{
		System:   "3DO",
		Keys:     []string{"3do", "panasonic 3do", "the 3do company - 3do"},
		Required: []string{"*"},
		Groups:   [][]string{{"panafz10.bin", "panafz10-norsa.bin", "panafz1.bin", "panafz1j.bin", "panafz1j-norsa.bin", "panafz10e-anvil.bin", "panafz10e-anvil-norsa.bin", "goldstar.bin", "sanyotry.bin"}},
	},
	{
		System:   "PC-FX",
		Keys:     []string{"pc-fx", "pcfx", "nec - pc-fx"},
		Required: []string{"*"},
		Groups:   [][]string{{"pcfx.rom"}},
	},
	{
		System:   "X68000",
		Keys:     []string{"x68000", "x68k", "sharp - x68000"},
		Required: []string{"*"},
		Groups:   [][]string{{"iplrom.dat"}, {"cgrom.dat"}},
		Sub:      "keropi",
	},
	{
		System: "Amiga",
		Keys:   []string{"amiga", "commodore - amiga"},
		Maybe:  []string{"*"},
		Groups: [][]string{{"kick34005.A500", "kick40063.A600", "kick40068.A1200"}},
		Note:   "PUAE falls back to the built-in AROS Kickstart, which not every game accepts.",
	},
	{
		System: "Atari ST",
		Keys:   []string{"atari st", "atarist", "atari - st"},
		Maybe:  []string{"*"},
		Groups: [][]string{{"tos.img"}},
	},
}

// gameDiagNormKey is a system or folder name as the table's Keys spell them.
func gameDiagNormKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}

// gameDiagSystemKeys are the names a game answers to: its library system, the
// canonical compatibility name and the folder the ROM sits in.
func gameDiagSystemKeys(system, romPath string) map[string]bool {
	keys := map[string]bool{}
	for _, s := range []string{system, canonicalCompatSystem(system), filepath.Base(filepath.Dir(romPath))} {
		if k := gameDiagNormKey(s); k != "" && k != "." {
			keys[k] = true
		}
	}
	return keys
}

// gameDiagCoreIn reports whether core is in list ("*" never matches here).
func gameDiagCoreIn(core string, list []string) bool {
	for _, c := range list {
		if c == core {
			return true
		}
	}
	return false
}

// gameDiagWildcard reports whether list holds "*".
func gameDiagWildcard(list []string) bool { return gameDiagCoreIn("*", list) }

// level is how much core needs the row's files; ok is false when the row
// says nothing to that core.
func (b gameDiagBIOS) level(core string) (gameDiagLevel, bool) {
	if gameDiagCoreIn(core, b.Skip) {
		return 0, false
	}
	if gameDiagCoreIn(core, b.Required) {
		return gameDiagSure, true
	}
	if gameDiagCoreIn(core, b.Maybe) {
		return gameDiagMaybe, true
	}
	if gameDiagWildcard(b.Required) {
		return gameDiagSure, true
	}
	if gameDiagWildcard(b.Maybe) {
		return gameDiagMaybe, true
	}
	return 0, false
}

// matches reports whether the row is about this game.
func (b gameDiagBIOS) matches(keys map[string]bool, romPath string) bool {
	if len(b.Exts) > 0 {
		ext := strings.ToLower(filepath.Ext(romPath))
		found := false
		for _, e := range b.Exts {
			if e == ext {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, k := range b.Keys {
		if keys[k] {
			return true
		}
	}
	return false
}

// gameDiagSegaCDRegion narrows the Sega CD row to the disc's region when the
// file name says it (No-Intro "(USA)", "(Europe)", "(Japan)"): Genesis Plus
// GX loads only the BIOS of the disc's region.
func gameDiagSegaCDRegion(romPath string) string {
	name := strings.ToLower(filepath.Base(romPath))
	switch {
	case strings.Contains(name, "(usa"), strings.Contains(name, "(u)"), strings.Contains(name, ", usa"):
		return "bios_CD_U.bin"
	case strings.Contains(name, "(europe"), strings.Contains(name, "(e)"), strings.Contains(name, ", europe"):
		return "bios_CD_E.bin"
	case strings.Contains(name, "(japan"), strings.Contains(name, "(j)"):
		return "bios_CD_J.bin"
	}
	return ""
}

// gameDiagFileIn finds name in dir: the exact spelling first, then any
// capitalisation (a FAT card ignores case; an ext4 card or Windows share may
// not, and the dialog then names the spelling that is there).
func gameDiagFileIn(dir, name string) (string, bool) {
	if dir == "" {
		return "", false
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, it := range items {
		if strings.EqualFold(it.Name(), name) {
			return filepath.Join(dir, it.Name()), true
		}
	}
	return "", false
}

// gameDiagSystemDir is the folder the launcher's RetroArch reads firmware
// from. sure is false when no config was readable and the folder is the
// firmware's usual one; cfgPath is the config it came from.
func gameDiagSystemDir(launcher, romPath string) (dir string, sure bool, cfgPath string) {
	if dir, cfgPath, ok := gameDiagConfiguredSystemDir(launcher, romPath); ok {
		return dir, true, cfgPath
	}
	// No config: the folder CrossMix and JukaHub's own BIOS Doctor use, the
	// first that exists.
	for _, root := range biosDoctorRoots() {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root, false, ""
		}
	}
	if IsWindows() {
		return resolvePath("BIOS"), false, ""
	}
	return filepath.Join(filepath.Dir(deviceRomsRoot), "BIOS"), false, ""
}

// gameDiagConfiguredSystemDir is system_directory from the first readable
// config of the launcher's RetroArch. It never falls back (biosDoctorRoots
// asks it for the folder, and gameDiagSystemDir falls back to those roots).
func gameDiagConfiguredSystemDir(launcher, romPath string) (dir, cfgPath string, ok bool) {
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, _ := raConfigGet(string(data), "system_directory")
		return gameDiagResolveRADir(value, path, romPath), path, true
	}
	return "", "", false
}

// gameDiagResolveRADir resolves a RetroArch directory setting the way
// RetroArch does for the config at cfgPath.
func gameDiagResolveRADir(value, cfgPath, romPath string) string {
	value = strings.TrimSpace(value)
	install := raInstallDir(cfgPath)
	if value == "" || strings.EqualFold(value, "default") {
		// RetroArch falls back to the content's folder.
		return filepath.Dir(romPath)
	}
	rest := ""
	switch {
	case strings.HasPrefix(value, ":"):
		rest = value[1:]
	case strings.HasPrefix(value, "~"):
		rest = value[1:]
		if IsWindows() {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				return filepath.Clean(filepath.Join(home, filepath.FromSlash(strings.TrimLeft(rest, `/\`))))
			}
		}
		// The handheld launch scripts start RetroArch with HOME set to its
		// own folder.
	case filepath.IsAbs(value) || strings.HasPrefix(value, "/"):
		return filepath.Clean(filepath.FromSlash(value))
	default:
		rest = value
	}
	rest = strings.TrimLeft(strings.ReplaceAll(rest, `\`, "/"), "/")
	return filepath.Clean(filepath.Join(install, filepath.FromSlash(rest)))
}

// gameDiagFindElsewhere looks for name in the other BIOS folders JukaHub
// knows and in the game's own folder, so the dialog can say "it is in X; this
// RetroArch reads Y".
func gameDiagFindElsewhere(name, sub, romDir, sysDir string) string {
	roots := append(biosDoctorRoots(), romDir)
	for _, root := range roots {
		if root == "" || filepath.Clean(root) == filepath.Clean(sysDir) {
			continue
		}
		for _, dir := range []string{filepath.Join(root, sub), root} {
			if path, ok := gameDiagFileIn(dir, name); ok {
				return path
			}
		}
	}
	return ""
}

// gameDiagBIOSIssues lists the firmware the game's core lacks. core is the
// core the launcher starts; coreKnown is false when that is the system's
// usual core rather than one the launcher names, which turns a "sure" into a
// "likely".
func gameDiagBIOSIssues(system, romPath, launcher, core string, coreKnown bool) []launchIssue {
	keys := gameDiagSystemKeys(system, romPath)
	sysDir, dirSure, cfgPath := "", false, ""
	romDir := filepath.Dir(romPath)
	var out []launchIssue
	seen := map[string]bool{}
	for _, row := range gameDiagBIOSTable {
		if !row.matches(keys, romPath) {
			continue
		}
		level, ok := row.level(core)
		if !ok {
			continue
		}
		if sysDir == "" {
			sysDir, dirSure, cfgPath = gameDiagSystemDir(launcher, romPath)
		}
		groups := row.Groups
		note := row.Note
		if row.System == "Sega CD" && (core == "genesis_plus_gx" || core == "genesis_plus_gx_wide") {
			if want := gameDiagSegaCDRegion(romPath); want != "" {
				groups = [][]string{{want}}
				note = "Genesis Plus GX loads only the BIOS of the disc's region."
			}
		}
		dirs := []string{filepath.Join(sysDir, row.Sub)}
		if row.Sub == "" && strings.HasPrefix(core, "fbneo") {
			dirs = append(dirs, filepath.Join(sysDir, "fbneo"))
		}
		if row.RomDir {
			dirs = append(dirs, romDir)
		}
		var missing []string
		var elsewhere []string
		for _, group := range groups {
			found := false
			for _, name := range group {
				for _, dir := range dirs {
					if _, ok := gameDiagFileIn(dir, name); ok {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				continue
			}
			label := group[0]
			if len(group) > 1 {
				label += " (or " + strings.Join(group[1:], ", ") + ")"
			}
			missing = append(missing, label)
			for _, name := range group {
				if path := gameDiagFindElsewhere(name, row.Sub, romDir, sysDir); path != "" {
					elsewhere = append(elsewhere, path)
					break
				}
			}
		}
		if len(missing) == 0 || seen[row.System] {
			continue
		}
		seen[row.System] = true
		if level == gameDiagSure && !coreKnown {
			level = gameDiagLikely
		}
		title := row.System + " BIOS is missing"
		if level == gameDiagMaybe {
			title = row.System + " BIOS may be needed"
		}
		coreName := coreConfigDisplayName(core)
		var detail []string
		if coreName != "" {
			detail = append(detail, "Core: "+coreName+".")
		}
		if note != "" {
			detail = append(detail, note)
		}
		for _, path := range elsewhere {
			detail = append(detail, "Found "+path+" — this RetroArch does not look there, so copy it to the folder above.")
		}
		switch {
		case !dirSure:
			detail = append(detail, "No RetroArch config was readable, so this is the usual BIOS folder, not one read from RetroArch's settings.")
		case cfgPath != "":
			detail = append(detail, "Folder read from system_directory in "+cfgPath+".")
		}
		if row.RomDir {
			detail = append(detail, "The game's own folder works too: "+romDir)
		}
		detail = append(detail, "File names must match exactly.")
		out = append(out, launchIssue{
			Level:  int(level),
			Title:  title,
			Files:  missing,
			Where:  filepath.Join(sysDir, row.Sub),
			Detail: strings.Join(detail, " "),
		})
	}
	return out
}

// biosDoctorGroupsFromTable is BIOS Doctor's system list: one line per
// console and core family of the diagnosis table, so the page and a failed
// launch name the same files.
func biosDoctorGroupsFromTable() []biosDoctorGroup {
	var out []biosDoctorGroup
	for _, row := range gameDiagBIOSTable {
		label := row.System
		var cores []string
		for _, c := range row.Required {
			if name := coreConfigDisplayName(c); c != "*" && !gameDiagCoreIn(name, cores) {
				cores = append(cores, name)
			}
		}
		optional := len(row.Required) == 0
		if len(cores) > 0 && len(cores) <= 3 {
			label += " (" + strings.Join(cores, ", ") + ")"
		} else if optional {
			label += " (optional)"
		}
		accepted := make([][]string, 0, len(row.Groups))
		folders := false
		for _, g := range row.Groups {
			names := make([]string, 0, len(g))
			for _, n := range g {
				// blueMSX's Machines/Databases are folders; the doctor
				// fingerprints files only.
				folders = folders || !strings.Contains(n, ".")
				if row.Sub != "" {
					n = row.Sub + "/" + n
				}
				names = append(names, n)
			}
			accepted = append(accepted, names)
		}
		dup := folders
		for _, g := range out {
			if g.name == label {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, biosDoctorGroup{name: label, accepted: accepted, optional: optional})
		}
	}
	return out
}
