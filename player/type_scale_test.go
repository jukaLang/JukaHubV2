package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

func TestTypeScaleIsFiveRoles(t *testing.T) {
	want := []struct {
		role TypeRole
		size int32
	}{
		{TypeCaption, 14},
		{TypeLabel, 16},
		{TypeBody, 18},
		{TypeHeading, 22},
		{TypeTitle, 28},
	}
	for _, w := range want {
		if got := w.role.Size(); got != w.size {
			t.Errorf("role %v draws at %d px, want %d", w.role, got, w.size)
		}
	}
}

func TestTypeScaleKeepsTheCompactCanvasReadable(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	defer func() {
		screenWidth, screenHeight = oldW, oldH
		a11yRefreshFonts()
	}()
	screenWidth, screenHeight = 960, 720
	for _, role := range []TypeRole{TypeCaption, TypeLabel, TypeBody, TypeHeading, TypeTitle} {
		if got := uiLegibleFontSize(int(role.Size())); got < uiMinTextSizeCompact {
			t.Fatalf("role %v draws at %d px on the compact canvas, below its %d px floor", role, got, uiMinTextSizeCompact)
		}
	}
}

// TestFontForRoleResolvesThroughTheFontPath drives the real resolver: each
// role opens a face, and the accessibility text size still changes it.
func TestFontForRoleResolvesThroughTheFontPath(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	old := a11y
	defer func() {
		a11y = old
		a11yRefreshFonts()
	}()
	resetA11yForTest()

	cfg := &Config{}
	roles := []TypeRole{TypeCaption, TypeLabel, TypeBody, TypeHeading, TypeTitle}
	for _, role := range roles {
		if FontForRole(cfg, role) == nil {
			t.Fatalf("role %v resolved no face", role)
		}
	}
	if FontForRole(nil, TypeBody) != nil {
		t.Fatal("a nil config resolved a face")
	}

	// The scale is live: the accessibility text size multiplies through
	// getFontSized, so the same role is taller at 145% than at 100%.
	a11y.fontScale = 1.0
	a11yRefreshFonts()
	small := FontForRole(cfg, TypeBody)
	a11y.fontScale = 1.45
	a11yRefreshFonts()
	large := FontForRole(cfg, TypeBody)
	if small == nil || large == nil {
		t.Fatal("the body role resolved no face at one of the text sizes")
	}
	if large.Height() <= small.Height() {
		t.Fatalf("body height %d at 145%% is not taller than %d at 100%%", large.Height(), small.Height())
	}
}

// TestNoLiteralFontSizesOutsideDesign is UX-1's acceptance rule: screens pick a
// role, never a pixel size. design.go owns the scale.
func TestNoLiteralFontSizesOutsideDesign(t *testing.T) {
	literal := regexp.MustCompile(`getFontSized\([^,()]+,\s*"[^"]+",\s*\d+\)`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || name == "design.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if literal.Match(data) {
			line := 1
			if at := strings.Index(string(data), literal.FindString(string(data))); at > 0 {
				line += strings.Count(string(data)[:at], "\n")
			}
			t.Fatalf("%s:%d passes a literal font size; use FontForRole", name, line)
		}
	}
}
