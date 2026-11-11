package main

import (
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// TestStatusBarNamesTheBrandOnly is UX-2's contract: the bar already names the
// brand, so a scene must not repeat its own name there.
func TestStatusBarNamesTheBrandOnly(t *testing.T) {
	scenes := []SceneConfig{
		{Name: "Settings"},
		{Name: "Tube"},
		{Name: "FileExplorer"},
		{Layout: "home"},
	}
	for _, scene := range scenes {
		if got := statusBarCaption(scene); got != "" {
			t.Fatalf("the bar adds %q for scene %q; the page header owns the name", got, scene.Name)
		}
	}
	if headerBrandText != "JukaHub" {
		t.Fatalf("the wordmark is %q", headerBrandText)
	}
	// The composed caption the bar draws is empty, whatever the scene is.
	if got := headerCaptionText("", statusBarCaption(scenes[0])); got != "" {
		t.Fatalf("the bar caption is %q", got)
	}
}

// TestPageHeaderTitlesAreAtLeastHeading drives the real title resolver: the
// default page title is a Heading, and a larger role or size is a Title.
func TestPageHeaderTitlesAreAtLeastHeading(t *testing.T) {
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
	heading := FontForRole(cfg, TypeHeading)
	title := FontForRole(cfg, TypeTitle)
	if heading == nil || title == nil {
		t.Fatal("the role scale resolved no faces")
	}
	if got := uiPageHeaderTitleFont(cfg, UIPageHeader{}); got != heading {
		t.Fatal("a default page title is not drawn at Heading")
	}
	if got := uiPageHeaderTitleFont(cfg, UIPageHeader{TitleRole: "large"}); got != title {
		t.Fatal("a large page title is not drawn at Title")
	}
	if got := uiPageHeaderTitleFont(cfg, UIPageHeader{TitleSize: int(TypeTitle.Size())}); got != title {
		t.Fatal("a Title-sized page title is not drawn at Title")
	}
	// A small override can never take a page title below the Heading floor.
	if got := uiPageHeaderTitleFont(cfg, UIPageHeader{TitleSize: 12}); got != heading {
		t.Fatal("a 12 px page title was allowed below Heading")
	}
	// The band reserves the same face the draw uses.
	if m := uiPageHeaderMetricsFor(cfg, UIPageHeader{}); m.TitleH != int32(heading.Height()) {
		t.Fatalf("the header band reserves %d px for a %d px Heading title", m.TitleH, heading.Height())
	}
	if uiPageHeaderTitleFont(nil, UIPageHeader{}) != nil {
		t.Fatal("a nil config resolved a page title face")
	}
}
