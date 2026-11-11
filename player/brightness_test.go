package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The firmware's own level: read from system.json in tenths, not while a
// level of JukaHub's is on its way there, written with every other key left
// exactly as it was, and left alone altogether when the file keeps it on some
// other scale.
func TestTrimuiSystemBrightness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	doc := "{\n\t\"vol\":\t7,\n\t\"brightness\":\t6,\n\t\"lumination\":\t7,\n\t\"hue\":\t10\n}\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = path
	defer func() {
		trimuiSystemJSONPath = prev
		trimuiBrightnessScaleBad.Store(false)
	}()
	trimuiBrightnessScaleBad.Store(false)
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 6 {
		t.Fatalf("the firmware's level reads %d, %v; want 6", step, ok)
	}
	trimuiBrightnessSyncLater(30)
	if _, ok := trimuiSystemBrightnessStep(); ok {
		t.Fatal("while a level of JukaHub's is on its way the file is stale")
	}
	trimuiBrightnessFlush()
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 3 {
		t.Fatalf("after the write the level reads %d, %v; want 3", step, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(doc, "\"brightness\":\t6", "\"brightness\":\t3", 1); string(data) != want {
		t.Fatalf("the rewrite touched more than brightness:\n%s", data)
	}
	// The volume's sync shares the writer and leaves the brightness alone.
	if err := trimuiSystemVolumeWrite(12); err != nil {
		t.Fatal(err)
	}
	if step, ok := trimuiSystemBrightnessStep(); !ok || step != 3 {
		t.Fatalf("a volume write moved the brightness to %d, %v", step, ok)
	}

	if err := os.WriteFile(path, []byte(`{"vol": 7, "brightness": 15}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := trimuiSystemBrightnessLevel(); ok || !trimuiBrightnessScaleBad.Load() {
		t.Fatal("a level outside 0..10 is some other scale: neither read nor trusted")
	}
	trimuiBrightnessSyncLater(50)
	if pending, _ := trimuiBrightnessSyncState(); pending {
		t.Fatal("nothing may be written to a file on an unknown scale")
	}
	for _, c := range []struct{ pct, step int }{{0, 0}, {10, 1}, {44, 4}, {45, 5}, {100, 10}, {130, 10}} {
		if got := trimuiBrightnessStepFor(c.pct); got != c.step {
			t.Errorf("trimuiBrightnessStepFor(%d) = %d, want %d", c.pct, got, c.step)
		}
	}
}

// The firmware's file is rewritten on its own inode (keymon's inotify watch is
// on it), never left shorter than it was with a stale tail, and not written at
// all while it does not parse (the firmware may be mid-write).
func TestTrimuiSystemJSONRewriteInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.json")
	doc := "{\"brightness\": 10, \"vol\": 15}\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = path
	defer func() { trimuiSystemJSONPath = prev }()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	set := func(v int) error {
		return trimuiSystemJSONRewrite(func(data []byte) ([]byte, bool) {
			return trimuiSystemJSONWithInt(data, "brightness", v, trimuiBrightnessSteps)
		})
	}
	if err := set(4); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the file was replaced: keymon's watch on it would be lost")
	}
	data, _ := os.ReadFile(path)
	if len(data) != len(doc) || !strings.HasPrefix(string(data), "{\"brightness\": 4, \"vol\": 15}\n") {
		t.Fatalf("a shorter document must be padded to the old length, got %q", data)
	}
	if v, ok, _ := trimuiSystemJSONIntFrom(data, "brightness", trimuiBrightnessSteps); !ok || v != 4 {
		t.Fatalf("the padded file reads brightness %d, %v", v, ok)
	}
	// Growing again uses up the padding instead of growing the file.
	if err := set(10); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != doc {
		t.Fatalf("back at 10 the file is %q, want %q", data, doc)
	}

	if err := os.WriteFile(path, []byte(`{"brightness": 6, "vol`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := set(3); !errors.Is(err, errTrimuiSystemJSONChanged) {
		t.Fatalf("a half-written file must be retried, got %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"brightness": 6, "vol` {
		t.Fatalf("a half-written file was written over: %q", data)
	}
	if got := string(trimuiFitLength([]byte("ab"), 4)); got != "ab  " {
		t.Fatalf("trimuiFitLength pad = %q", got)
	}
	if got := string(trimuiFitLength([]byte("abc  "), 4)); got != "abc " {
		t.Fatalf("trimuiFitLength trim = %q", got)
	}
	if got := string(trimuiFitLength([]byte("abcde"), 4)); got != "abcde" {
		t.Fatalf("trimuiFitLength must not cut the document itself, got %q", got)
	}
}

func TestTrimuiSystemJSONWithInt(t *testing.T) {
	doc := []byte(`{"screen_brightness": 2, "brightness": 6, "lumination": 7}`)
	out, changed := trimuiSystemJSONWithInt(doc, "brightness", 4, trimuiBrightnessSteps)
	if !changed || string(out) != `{"screen_brightness": 2, "brightness": 4, "lumination": 7}` {
		t.Fatalf("rewrite = %s, %v", out, changed)
	}
	if _, changed := trimuiSystemJSONWithInt(out, "brightness", 4, trimuiBrightnessSteps); changed {
		t.Fatal("the same level must not rewrite the file")
	}
	if _, changed := trimuiSystemJSONWithInt([]byte(`{"brightness": 3, "x": {"brightness": 4}}`), "brightness", 5, 10); changed {
		t.Fatal("two \"brightness\" members are ambiguous and must be left alone")
	}
	if _, changed := trimuiSystemJSONWithInt([]byte(`{"vol": 3}`), "brightness", 5, 10); changed {
		t.Fatal("a file without the key gets no key added")
	}
	if v, ok, inRange := trimuiSystemJSONIntFrom([]byte(`{"brightness": 12.6}`), "brightness", 10); !ok || inRange || v != 10 {
		t.Fatalf("12.6 on a 0..10 scale read as %d, ok %v, inRange %v", v, ok, inRange)
	}
}
