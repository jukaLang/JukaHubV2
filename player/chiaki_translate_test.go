package main

// Tests for the English menu patch. The fixtures below are the upstream lines
// verbatim (trimui-chiaki-ng v0.4.0-beta2 and v0.3.9 word them identically), so
// the table in chiaki_translate.go is checked against real source, not against
// itself.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chiakiTranslateFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	app := t.TempDir()
	files := map[string]string{
		"rh/i18n.py": `TEXTS = {
    "VI": {
        "pair_pin_label": "MÃ PIN",
        "pair_no_host": "Chưa chọn máy",
        "pair_running": "Đang ghép với %s...",
    },
    "EN": {
        "pair_pin_label": "PIN",
        "pair_no_host": "No host selected",
        "pair_running": "Pairing with %s...",
    },
}
`,
		"rh/screens/home.py": `    def _start_stream(self, host):
        if not self._is_paired(host):
            log.info("home: host %s chua pair, mo man pair", host.addr)
            self.toast = tr("pair_required") if "pair_required" in tr("pair_required") else "Chưa ghép - bấm Y để nhập PIN"
            self._open_pair(host)
`,
		"rh/screens/pair.py": `            self._report_error("pair_save_failed")
            self.status = tr("pair_failed") % "không lưu được khóa"
`,
		"rh/chiaki.py": `    if not os.path.isfile(helper):
        return False, "Thiếu bin/chiaki-stream"
    if not credentials:
        return False, "Khóa ghép nối không hợp lệ; hãy ghép lại PS4"
    if ps5_mismatch:
        return False, "Khóa PS5 không khớp máy; hãy ghép lại PS5"
    if old_pair:
        return False, "Khóa pair cũ; hãy ghép lại PS4 một lần"
    return True, "Đang mở Remote Play..."
    return False, "Không chuẩn bị được stream: %s"
    return {"error": "PS5 lỗi ở bước %s: %s"}
    return {"error": "PS5 lỗi ngoài dự kiến; xem Chiaki-loi.txt"}
`,
		"rh/ps5_regist.py": `def _account(raw):
    raise PS5RegistError("account", "thiếu PSN Account-ID Base64 12 ký tự (8 byte / 64 bit)")
    raise PS5RegistError("account", "PSN Account-ID không đúng Base64; cần 12 ký tự và thường kết thúc bằng =")
    raise PS5RegistError("account", "PSN Account-ID phải là Base64 của đúng 8 byte (64 bit), không phải PIN 8 số")
    raise PS5RegistError("helper", "thiếu helper PS5 chiaki-regist")
    raise PS5RegistError("helper", "helper PS5 không có quyền chạy")
    raise PS5RegistError("result", "helper PS5 không tạo kết quả")
    raise PS5RegistError("result", "helper PS5 trả khóa không hợp lệ")
    raise PS5RegistError("result", "helper PS5 trả kết quả thiếu hoặc sai định dạng")
    raise PS5RegistError("network", "PS5 không phản hồi đăng ký trong thời gian chờ")
    raise PS5RegistError("stage", "PS5 từ chối hoặc không hoàn tất đăng ký (bước %s, mã %d)")
`,
		"rh/protocol.py": "# protocol code must never be rewritten\nPACKET = b\"\\x00\\x01\"\n",
	}
	for rel, body := range files {
		writeChiakiTestFile(t, filepath.Join(app, filepath.FromSlash(rel)), body)
	}
	return app, files
}

func chiakiReadTestFile(t *testing.T, app, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(app, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func TestChiakiSetLiteralLanguageSwitchesBothWays(t *testing.T) {
	app, original := chiakiTranslateFixture(t)

	n, err := chiakiSetLiteralLanguage(app, "EN")
	if err != nil {
		t.Fatalf("switch to EN: %v", err)
	}
	if n != len(chiakiUILiterals) {
		t.Fatalf("rewrote %d literals, want %d", n, len(chiakiUILiterals))
	}
	for _, lit := range chiakiUILiterals {
		body := chiakiReadTestFile(t, app, lit.file)
		if strings.Contains(body, lit.vi) {
			t.Errorf("%s still has Vietnamese %q", lit.file, lit.vi)
		}
		if !strings.Contains(body, lit.en) {
			t.Errorf("%s missing English %q", lit.file, lit.en)
		}
	}
	// Protocol code and the English catalogue must be byte-identical.
	if got := chiakiReadTestFile(t, app, "rh/protocol.py"); got != original["rh/protocol.py"] {
		t.Errorf("unrelated module changed: %q", got)
	}
	if got := chiakiReadTestFile(t, app, "rh/i18n.py"); got != original["rh/i18n.py"] {
		t.Errorf("language switch touched the catalogue: %q", got)
	}
	if _, err := os.Stat(filepath.Join(app, "rh", "chiaki.py.jukahub-tmp")); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}

	// Back to Vietnamese must restore the upstream bytes exactly.
	n, err = chiakiSetLiteralLanguage(app, "VI")
	if err != nil {
		t.Fatalf("switch to VI: %v", err)
	}
	if n != len(chiakiUILiterals) {
		t.Fatalf("restored %d literals, want %d", n, len(chiakiUILiterals))
	}
	for rel, body := range original {
		if got := chiakiReadTestFile(t, app, rel); got != body {
			t.Errorf("%s was not restored:\n%s", rel, got)
		}
	}
}

func TestChiakiSetLiteralLanguageIsIdempotent(t *testing.T) {
	app, _ := chiakiTranslateFixture(t)
	if _, err := chiakiSetLiteralLanguage(app, "EN"); err != nil {
		t.Fatal(err)
	}
	before := chiakiReadTestFile(t, app, "rh/chiaki.py")
	info, err := os.Stat(filepath.Join(app, "rh", "screens", "home.py"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := chiakiSetLiteralLanguage(app, "EN")
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 0 {
		t.Fatalf("second pass rewrote %d literals, want 0", n)
	}
	if got := chiakiReadTestFile(t, app, "rh/chiaki.py"); got != before {
		t.Error("second pass changed the file")
	}
	after, err := os.Stat(filepath.Join(app, "rh", "screens", "home.py"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != info.Mode().Perm() {
		t.Errorf("mode changed from %v to %v", info.Mode().Perm(), after.Mode().Perm())
	}
}

func TestChiakiSetLiteralLanguageSkipsUnknownRelease(t *testing.T) {
	app := t.TempDir()
	writeChiakiTestFile(t, filepath.Join(app, "rh", "chiaki.py"), "print('a release that words things differently')\n")

	n, err := chiakiSetLiteralLanguage(app, "EN")
	if err != nil {
		t.Fatalf("unknown release should be skipped, got %v", err)
	}
	if n != 0 {
		t.Fatalf("rewrote %d strings in an unrelated file", n)
	}
	if got := chiakiReadTestFile(t, app, "rh/chiaki.py"); got != "print('a release that words things differently')\n" {
		t.Fatalf("unrelated file changed: %q", got)
	}
	if n, err := chiakiSetLiteralLanguage("", "EN"); n != 0 || err != nil {
		t.Fatalf("empty app dir: n=%d err=%v", n, err)
	}
}

func TestChiakiCatalogueRepairAddsPairRequiredKey(t *testing.T) {
	app, _ := chiakiTranslateFixture(t)
	n, err := chiakiRepairCatalogue(app)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if n != 3 {
		t.Fatalf("repair applied %d edits, want 3", n)
	}
	i18n := chiakiReadTestFile(t, app, "rh/i18n.py")
	viAt := strings.Index(i18n, `"VI"`)
	enAt := strings.Index(i18n, `"EN"`)
	viKey := strings.Index(i18n, `"pair_required": "Chưa ghép - bấm Y để nhập PIN"`)
	enKey := strings.Index(i18n, `"pair_required": "Not paired - press Y to enter a PIN"`)
	if viKey < viAt || viKey > enAt {
		t.Errorf("Vietnamese key is not in the VI catalogue: %d (VI %d, EN %d)", viKey, viAt, enAt)
	}
	if enKey < enAt {
		t.Errorf("English key is not in the EN catalogue: %d (EN %d)", enKey, enAt)
	}
	if !strings.Contains(i18n, `"pair_no_host": "Chưa chọn máy",`+"\n"+`        "pair_required"`) {
		t.Errorf("key was not inserted beside the VI pair keys:\n%s", i18n)
	}

	home := chiakiReadTestFile(t, app, "rh/screens/home.py")
	if strings.Contains(home, `"pair_required" in tr("pair_required")`) {
		t.Errorf("the toast ternary survived:\n%s", home)
	}
	if !strings.Contains(home, `self.toast = tr("pair_required")`) {
		t.Errorf("the toast was not simplified:\n%s", home)
	}

	before := chiakiReadTestFile(t, app, "rh/i18n.py")
	if n, err := chiakiRepairCatalogue(app); n != 0 || err != nil {
		t.Fatalf("second repair: n=%d err=%v", n, err)
	}
	if got := chiakiReadTestFile(t, app, "rh/i18n.py"); got != before {
		t.Error("second repair changed the catalogue")
	}
}

func TestChiakiPrepareUILanguageFollowsSettings(t *testing.T) {
	fresh, _ := chiakiTranslateFixture(t)
	chiakiEnsureEnglish(fresh)
	if got := chiakiSettingsLanguage(fresh); got != "EN" {
		t.Fatalf("fresh install language = %q, want EN", got)
	}
	if body := chiakiReadTestFile(t, fresh, "rh/chiaki.py"); strings.Contains(body, "Đang mở Remote Play") {
		t.Error("fresh install left Vietnamese stream text behind")
	}
	if body := chiakiReadTestFile(t, fresh, "rh/screens/home.py"); strings.Contains(body, "Chưa ghép") {
		t.Error("fresh install left the Vietnamese toast fallback behind")
	}

	// An install where the user chose Vietnamese must stay Vietnamese.
	vi, original := chiakiTranslateFixture(t)
	writeChiakiTestFile(t, filepath.Join(vi, "settings.json"), `{"language":"VI"}`)
	chiakiEnsureEnglish(vi)
	if got := chiakiSettingsLanguage(vi); got != "VI" {
		t.Fatalf("existing language was overwritten: %q", got)
	}
	if body := chiakiReadTestFile(t, vi, "rh/chiaki.py"); body != original["rh/chiaki.py"] {
		t.Error("Vietnamese install was translated to English")
	}
}

func TestChiakiFreshInstallStartsInEnglish(t *testing.T) {
	// Every release ZIP ships settings.json with "language": "VI"; a fresh
	// install must still open in English, while keeping the shipped defaults.
	app, _ := chiakiTranslateFixture(t)
	writeChiakiTestFile(t, filepath.Join(app, "settings.json"), `{"language":"VI","video_bitrate":8000}`+"\n")

	chiakiInstallLanguageDefault(app, false)

	if got := chiakiSettingsLanguage(app); got != "EN" {
		t.Fatalf("fresh install language = %q, want EN", got)
	}
	settings := chiakiReadTestFile(t, app, "settings.json")
	if !strings.Contains(settings, "8000") {
		t.Fatalf("shipped defaults were lost: %s", settings)
	}
	if body := chiakiReadTestFile(t, app, "rh/chiaki.py"); strings.Contains(body, "Đang mở Remote Play") {
		t.Error("fresh install kept Vietnamese stream text")
	}
}

func TestChiakiReinstallKeepsUserLanguage(t *testing.T) {
	app, original := chiakiTranslateFixture(t)
	writeChiakiTestFile(t, filepath.Join(app, "settings.json"), `{"language":"VI"}`+"\n")

	chiakiInstallLanguageDefault(app, true)

	if got := chiakiSettingsLanguage(app); got != "VI" {
		t.Fatalf("reinstall overwrote the user's language: %q", got)
	}
	if body := chiakiReadTestFile(t, app, "rh/chiaki.py"); body != original["rh/chiaki.py"] {
		t.Error("reinstall translated a Vietnamese install")
	}
}

func TestChiakiToggleLanguageAlsoSwitchesLiterals(t *testing.T) {
	app, original := chiakiTranslateFixture(t)
	writeChiakiTestFile(t, filepath.Join(app, "settings.json"), `{"language":"EN","bitrate":12000}`)

	next, err := chiakiToggleLanguage(app)
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if next != "VI" {
		t.Fatalf("next = %q, want VI", next)
	}
	if body := chiakiReadTestFile(t, app, "rh/chiaki.py"); strings.Contains(body, "Starting Remote Play...") {
		t.Error("English stream text survived the switch to Vietnamese")
	}
	if body := chiakiReadTestFile(t, app, "rh/screens/pair.py"); body != original["rh/screens/pair.py"] {
		t.Error("pair screen did not return to Vietnamese")
	}
	// The other settings must survive the flip.
	if got := chiakiReadTestFile(t, app, "settings.json"); !strings.Contains(got, "12000") {
		t.Fatalf("settings lost: %s", got)
	}
}
