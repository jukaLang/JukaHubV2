package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const goodTestConfig = `{
  "appName": "JukaHub",
  "version": "0.4.0",
  "width": 1280,
  "height": 720,
  "scenes": [{"name": "Home", "elements": []}]
}`

// A damaged jukaconfig.json next to a good .bak must start the app, keep the
// damaged file for inspection and say so once. Before this, the backup was
// loaded and still reported as an error, so main.go logFatal()'d before safe
// mode or the recovery chord could help.
func TestLoadLastKnownGoodRecoversCorruptPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	writeTestConfig(t, path, `{"scenes": [`)
	writeTestConfig(t, path+".bak", goodTestConfig)

	cfg, err := LoadLastKnownGood(path)
	if err != nil {
		t.Fatalf("a good backup must be a clean launch, got error: %v", err)
	}
	if cfg == nil || len(cfg.Scenes) == 0 {
		t.Fatalf("recovered config is empty: %+v", cfg)
	}

	kept, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil {
		t.Fatalf("glob: %v", globErr)
	}
	if len(kept) != 1 {
		t.Fatalf("damaged config was not kept aside: %v", kept)
	}
	if data, readErr := os.ReadFile(kept[0]); readErr != nil || !strings.Contains(string(data), `{"scenes": [`) {
		t.Fatalf("kept file is not the damaged one: %q err=%v", data, readErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("damaged config still sits at the live path")
	}

	notice := takeConfigRecoveryNotice()
	if !strings.Contains(notice, "restored from the backup") {
		t.Errorf("recovery notice = %q, want an explanation", notice)
	}
	if !strings.Contains(notice, ".corrupt-") {
		t.Errorf("recovery notice = %q, want the kept file's name", notice)
	}
	if second := takeConfigRecoveryNotice(); second != "" {
		t.Errorf("the recovery notice repeated: %q", second)
	}
}

// A healthy config stays exactly where it is: no rename, no notice.
func TestLoadLastKnownGoodKeepsHealthyPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	writeTestConfig(t, path, goodTestConfig)

	if _, err := LoadLastKnownGood(path); err != nil {
		t.Fatalf("healthy config failed to load: %v", err)
	}
	if kept, _ := filepath.Glob(path + ".corrupt-*"); len(kept) != 0 {
		t.Errorf("healthy config was renamed aside: %v", kept)
	}
	if notice := takeConfigRecoveryNotice(); notice != "" {
		t.Errorf("healthy load produced a recovery notice: %q", notice)
	}
}
