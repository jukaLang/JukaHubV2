package main

// chiaki_translate.go — the English menu patch for the TrimUI Chiaki-ng port.
//
// The port keeps its own VI/EN catalogue in rh/i18n.py and switches it with
// "language" in settings.json, which is what JukaHub's Interface language row
// writes. That catalogue does not cover four places written before it existed:
//
//   - rh/screens/home.py builds the "not paired" toast from a ternary that can
//     never take its fallback, because "pair_required" is missing from both
//     catalogues — the toast reads "pair_required" instead of a sentence;
//   - rh/chiaki.py returns Vietnamese error and status text straight to the
//     home and pair screens ("Đang mở Remote Play...");
//   - rh/screens/pair.py appends a Vietnamese reason to a translated failure;
//   - rh/ps5_regist.py raises Vietnamese messages that the PS5 screen prints
//     verbatim.
//
// This file rewrites those literals in place, in both directions, so the port
// follows the same language as its catalogue without a fork. English is applied
// before every launch, and switching the row back to Vietnamese restores the
// original wording. The catalogue repair (the toast and its missing key) is
// language-independent and runs either way.
//
// Every rewrite is an exact, full-literal match: protocol code, settings and
// pairing keys are never touched, and a release that words a line differently
// is left alone instead of being edited by a fuzzy rule.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// chiakiUILiteral is one hardcoded string in the port and its translation.
// Neither side is a substring of another pair in the same file, so the order of
// the table cannot change the result.
type chiakiUILiteral struct {
	file string // slash-separated path inside the app folder
	vi   string
	en   string
}

var chiakiUILiterals = []chiakiUILiteral{
	// rh/chiaki.py — prepare_stream_launch results, shown as the home toast.
	{"rh/chiaki.py", "Thiếu bin/chiaki-stream", "Missing bin/chiaki-stream"},
	{"rh/chiaki.py", "Khóa ghép nối không hợp lệ; hãy ghép lại PS4", "Invalid pairing key; pair the PS4 again"},
	{"rh/chiaki.py", "Khóa PS5 không khớp máy; hãy ghép lại PS5", "The PS5 key does not match this console; pair the PS5 again"},
	{"rh/chiaki.py", "Khóa pair cũ; hãy ghép lại PS4 một lần", "Old pairing key; pair the PS4 once more"},
	{"rh/chiaki.py", "Đang mở Remote Play...", "Starting Remote Play..."},
	{"rh/chiaki.py", "Không chuẩn bị được stream: %s", "Could not prepare the stream: %s"},
	{"rh/chiaki.py", "PS5 lỗi ở bước %s: %s", "PS5 failed at stage %s: %s"},
	{"rh/chiaki.py", "PS5 lỗi ngoài dự kiến; xem Chiaki-loi.txt", "Unexpected PS5 error; see Chiaki-loi.txt"},

	// rh/screens/pair.py — the reason appended to "Pair failed: %s".
	{"rh/screens/pair.py", "không lưu được khóa", "could not save the key"},

	// rh/ps5_regist.py — PS5RegistError text, printed by the pairing screen.
	{"rh/ps5_regist.py", "thiếu PSN Account-ID Base64 12 ký tự (8 byte / 64 bit)",
		"missing a 12-character Base64 PSN Account-ID (8 bytes / 64 bits)"},
	{"rh/ps5_regist.py", "PSN Account-ID không đúng Base64; cần 12 ký tự và thường kết thúc bằng =",
		"PSN Account-ID is not valid Base64; it needs 12 characters and usually ends with ="},
	{"rh/ps5_regist.py", "PSN Account-ID phải là Base64 của đúng 8 byte (64 bit), không phải PIN 8 số",
		"PSN Account-ID must be Base64 for exactly 8 bytes (64 bits), not the 8-digit PIN"},
	{"rh/ps5_regist.py", "thiếu helper PS5 chiaki-regist", "missing the PS5 chiaki-regist helper"},
	{"rh/ps5_regist.py", "helper PS5 không có quyền chạy", "the PS5 helper is not executable"},
	{"rh/ps5_regist.py", "helper PS5 không tạo kết quả", "the PS5 helper produced no result"},
	{"rh/ps5_regist.py", "helper PS5 trả khóa không hợp lệ", "the PS5 helper returned an invalid key"},
	{"rh/ps5_regist.py", "helper PS5 trả kết quả thiếu hoặc sai định dạng",
		"the PS5 helper returned a missing or malformed result"},
	{"rh/ps5_regist.py", "PS5 không phản hồi đăng ký trong thời gian chờ",
		"the PS5 did not answer the registration in time"},
	{"rh/ps5_regist.py", "PS5 từ chối hoặc không hoàn tất đăng ký (bước %s, mã %d)",
		"the PS5 refused or did not finish registration (stage %s, code %d)"},
}

// chiakiTextRepair is a language-independent fix: upstream text that is wrong
// in both catalogues, or a catalogue key that is missing from both.
type chiakiTextRepair struct {
	file string
	from string
	to   string
}

var chiakiCatalogueRepairs = []chiakiTextRepair{
	// The VI catalogue is missing "pair_required" entirely; add it beside the
	// neighbouring pair keys. Anchored on the VI-only wording so the EN block
	// is never touched by this rule.
	// Both anchors span two lines (the key plus the next entry) so a second run
	// cannot insert the key again: after the repair the two lines are no longer
	// adjacent and the rule stops matching.
	{
		file: "rh/i18n.py",
		from: "        \"pair_no_host\": \"Chưa chọn máy\",\n        \"pair_running\":",
		to: "        \"pair_no_host\": \"Chưa chọn máy\",\n" +
			"        \"pair_required\": \"Chưa ghép - bấm Y để nhập PIN\",\n" +
			"        \"pair_running\":",
	},
	{
		file: "rh/i18n.py",
		from: "        \"pair_no_host\": \"No host selected\",\n        \"pair_running\":",
		to: "        \"pair_no_host\": \"No host selected\",\n" +
			"        \"pair_required\": \"Not paired - press Y to enter a PIN\",\n" +
			"        \"pair_running\":",
	},
	// home.py asks whether the key is inside its own name, which is always
	// true, so the fallback never runs and the toast shows the raw key. With
	// the key now in both catalogues, tr() alone is correct in either language.
	{
		file: "rh/screens/home.py",
		from: `self.toast = tr("pair_required") if "pair_required" in tr("pair_required") else "Chưa ghép - bấm Y để nhập PIN"`,
		to:   `self.toast = tr("pair_required")`,
	},
}

// chiakiSetLiteralLanguage rewrites the port's hardcoded UI strings into lang
// ("EN" or "VI"). It reports how many literals changed, and only writes a file
// when something matched, so an already-correct install costs one read per file
// and no SD-card writes.
func chiakiSetLiteralLanguage(appDir, lang string) (int, error) {
	if appDir == "" || (lang != "EN" && lang != "VI") {
		return 0, nil
	}
	order := make([]string, 0, 4)
	byFile := make(map[string][][2]string, 4)
	for _, lit := range chiakiUILiterals {
		if _, ok := byFile[lit.file]; !ok {
			order = append(order, lit.file)
		}
		from, to := lit.vi, lit.en
		if lang == "VI" {
			from, to = lit.en, lit.vi
		}
		byFile[lit.file] = append(byFile[lit.file], [2]string{from, to})
	}

	changed := 0
	for _, rel := range order {
		path := filepath.Join(appDir, filepath.FromSlash(rel))
		n, err := chiakiRewriteFile(path, byFile[rel])
		if err != nil {
			if os.IsNotExist(err) {
				// The stable build may not ship ps5_regist.py, and a future
				// release can split a module: a missing file is not a failure.
				continue
			}
			return changed, fmt.Errorf("%s: %w", rel, err)
		}
		changed += n
	}
	return changed, nil
}

// chiakiRepairCatalogue fixes the untranslated toast and adds its missing key.
// It is safe to run in either language and on every release: the rules are
// exact strings, so a fixed upstream build simply stops matching.
func chiakiRepairCatalogue(appDir string) (int, error) {
	if appDir == "" {
		return 0, nil
	}
	changed := 0
	for _, rel := range []string{"rh/i18n.py", "rh/screens/home.py"} {
		var pairs [][2]string
		for _, r := range chiakiCatalogueRepairs {
			if r.file == rel {
				pairs = append(pairs, [2]string{r.from, r.to})
			}
		}
		if len(pairs) == 0 {
			continue
		}
		path := filepath.Join(appDir, filepath.FromSlash(rel))
		n, err := chiakiRewriteFile(path, pairs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, fmt.Errorf("%s: %w", rel, err)
		}
		changed += n
	}
	return changed, nil
}

// chiakiPrepareUILanguage points the port's own copy at the language its
// settings select. It runs before a launch and after the language row flips,
// which also repairs an install that was downloaded outside JukaHub.
func chiakiPrepareUILanguage(appDir string) {
	if appDir == "" {
		return
	}
	if n, err := chiakiRepairCatalogue(appDir); err != nil {
		log.Printf("[chiaki] could not repair the menu catalogue: %v", err)
	} else if n > 0 {
		log.Printf("[chiaki] repaired %d untranslated menu string(s)", n)
	}
	lang := chiakiCurrentLanguage(appDir)
	n, err := chiakiSetLiteralLanguage(appDir, lang)
	if err != nil {
		log.Printf("[chiaki] could not set the menu language: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[chiaki] rewrote %d menu string(s) to %s", n, chiakiLanguageName(lang))
	}
}

// chiakiRewriteFile applies literal replacements to one file and writes it
// atomically, preserving the executable bit the port's launcher expects on its
// Python entry points. No match means no write.
func chiakiRewriteFile(path string, replacements [][2]string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	text := string(data)
	total := 0
	for _, r := range replacements {
		n := strings.Count(text, r[0])
		if n == 0 {
			continue
		}
		text = strings.ReplaceAll(text, r[0], r[1])
		total += n
	}
	if total == 0 {
		return 0, nil
	}
	if err := chiakiWriteTextFile(path, text); err != nil {
		return 0, err
	}
	return total, nil
}

// chiakiWriteTextFile replaces path through a temporary file in the same folder,
// so a card yanked mid-write cannot leave a truncated Python module that the
// port would fail to import.
func chiakiWriteTextFile(path, text string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".jukahub-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
