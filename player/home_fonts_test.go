package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// TestHomeUIFontPathKeepsOnlyInter pins the single-typeface rule: a role that
// names DejaVu, Arial, Roboto or a system font is drawn in the bundled Inter,
// while a cut of Inter is honoured as written.
func TestHomeUIFontPathKeepsOnlyInter(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", homeUIFontFile},
		{"   ", homeUIFontFile},
		{"DejaVuSans.ttf", homeUIFontFile},
		{"DejaVuSans-Bold.ttf", homeUIFontFile},
		{"arial.ttf", homeUIFontFile},
		{"Roboto-Black.ttf", homeUIFontFile},
		{`C:\Windows\Fonts\segoeui.ttf`, homeUIFontFile},
		{"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", homeUIFontFile},
		{"Interstate.ttf", homeUIFontFile},
		{"Inter-Regular.woff2", homeUIFontFile},
		{"Inter-Regular.ttf", "Inter-Regular.ttf"},
		{"Inter-Bold.ttf", "Inter-Bold.ttf"},
		{"fonts/InterVariable.ttf", "fonts/InterVariable.ttf"},
		{`fonts\Inter.otf`, `fonts\Inter.otf`},
		{"INTER-REGULAR.TTF", "INTER-REGULAR.TTF"},
	}
	for _, tc := range cases {
		if got := homeUIFontPath(tc.in); got != tc.want {
			t.Errorf("homeUIFontPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestShippedConfigsUseInterOnly keeps both shipped configs on the bundled
// typeface: FontPath names Inter and no font role points at another file.
func TestShippedConfigsUseInterOnly(t *testing.T) {
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var cfg Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			if cfg.FontPath != homeUIFontFile {
				t.Errorf("%s FontPath = %q, want %q", path, cfg.FontPath, homeUIFontFile)
			}
			for role, p := range cfg.Variables.Fonts {
				if homeUIFontPath(p) != p {
					t.Errorf("%s font role %q names %q, which is not Inter", path, role, p)
				}
			}
		})
	}
}

// TestFontRolesOpenOnlyInter proves the runtime side: a role pointed at DejaVu
// opens the bundled Inter (the same cached face), and every Home size is Inter.
func TestFontRolesOpenOnlyInter(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Skipf("SDL_ttf unavailable: %v", err)
	}
	inter := loadCachedFont(homeUIFontFile, 17)
	if inter == nil {
		t.Fatalf("no usable font in this environment: the shipped %s did not load, so this check cannot run", homeUIFontFile)
	}
	if got := loadCachedFont("DejaVuSans.ttf", 17); got != inter {
		t.Fatalf("a DejaVu request opened %p, want the cached Inter face %p", got, inter)
	}
	if p, ok := fontPathFor(inter); !ok || p != homeUIFontFile {
		t.Fatalf("the Inter face is registered as %q (%v), want %q", p, ok, homeUIFontFile)
	}

	cfg := &Config{}
	cfg.Variables.Fonts = map[string]string{"small": "arial.ttf"}
	cfg.Variables.FontSizes = map[string]int{"small": 17}
	if f, _ := getCachedFont(cfg, "small"); f != nil {
		if p, _ := fontPathFor(f); p != homeUIFontFile {
			t.Errorf("the small role resolved to %q, want %q", p, homeUIFontFile)
		}
	}

	resetHomeFonts()
	t.Cleanup(resetHomeFonts)
	fonts := loadHomeFonts()
	for name, f := range map[string]*ttf.Font{
		"Brand": fonts.Brand, "Greeting": fonts.Greeting, "Subtitle": fonts.Subtitle,
		"Card": fonts.Card, "Tile": fonts.Tile, "Meta": fonts.Meta,
		"Helper": fonts.Helper, "Small": fonts.Small,
	} {
		if f == nil {
			t.Errorf("Home font %s did not open", name)
			continue
		}
		if p, _ := fontPathFor(f); p != homeUIFontFile {
			t.Errorf("Home font %s is %q, want %q", name, p, homeUIFontFile)
		}
	}
}
