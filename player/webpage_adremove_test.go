package main

// webpage_adremove_test.go covers what the ad cut leaves behind. Cutting an ad
// out of the markup is only half the job (webpage_adremove.go): the box it sat
// in, and the padding, border and margin the page styles that box with, would
// otherwise still be drawn as the gap the ad left.

import (
	"strings"
	"testing"
)

const webAdTestPage = "https://example.com/story"

func TestWebRemoveAdsClosesUpWhatTheAdLeft(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// want / notWant are substrings the cleaned markup must / must not hold.
		want, notWant []string
		// count is the number of ads the page reports having blocked: a box
		// dropped for holding nothing but its ads is not one of them.
		count int
	}{
		{
			name:    "the ad slot's own frame goes, the story stays",
			in:      `<div class="page"><div class="ad-slot"><ins class="adsbygoogle"></ins></div><p>Story text</p></div>`,
			want:    []string{`class="page"`, `<p>Story text</p>`, webAdCutMarker},
			notWant: []string{"ad-slot", "adsbygoogle"},
			count:   1,
		},
		{
			name:    "a box the ad was the whole of goes",
			in:      `<div class="story"><div class="frame"><div class="ad-slot"></div></div><p>Body</p></div>`,
			want:    []string{`class="story"`, `<p>Body</p>`},
			notWant: []string{`class="frame"`, "ad-slot"},
			count:   1,
		},
		{
			name:    "a sponsored line and its ad do not leave an empty frame",
			in:      `<div class="frame"><span class="sponsor-label">Sponsored</span><div class="ad-slot"></div></div>`,
			want:    nil,
			notWant: []string{`class="frame"`, "Sponsored", "ad-slot"},
			count:   1,
		},
		{
			name:    "a break does not hold an emptied box open",
			in:      `<div class="frame"><br><div class="ad-slot"></div></div>`,
			want:    nil,
			notWant: []string{`class="frame"`, "<br>"},
			count:   1,
		},
		{
			name:    "a box that still holds the page's text stays",
			in:      `<div class="frame"><div class="ad-slot"></div><p>Read on</p></div>`,
			want:    []string{`class="frame"`, `<p>Read on</p>`},
			notWant: []string{"ad-slot"},
			count:   1,
		},
		{
			name:    "a box that still holds a picture of the page's own stays",
			in:      `<div class="frame"><div class="ad-slot"></div><img src="/photo.jpg" alt="A tree"></div>`,
			want:    []string{`class="frame"`, `<img src="/photo.jpg"`},
			notWant: []string{"ad-slot"},
			count:   1,
		},
		{
			name: "a page with no ads is left exactly as it was",
			in:   `<div class="spacer"></div><p>Hello<br>there</p><div class="frame"></div>`,
			want: []string{`class="spacer"`, `class="frame"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, count := webRemoveAds(tc.in, webAdTestPage)
			if count != tc.count {
				t.Fatalf("blocked %d, want %d:\n%s", count, tc.count, out)
			}
			for _, s := range tc.want {
				if !strings.Contains(out, s) {
					t.Errorf("cleaned markup lost %q:\n%s", s, out)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(out, s) {
					t.Errorf("cleaned markup still holds %q:\n%s", s, out)
				}
			}
			if tc.in != "" && tc.count == 0 && out != tc.in {
				t.Errorf("a page with no ads was rewritten:\n got %s\nwant %s", out, tc.in)
			}
		})
	}
}

// TestWebRemoveAdsDropsAnAdOnlyContainerTooBigForTheWrapperRule covers the box
// the wrapper cut leaves because it is wider than the wrapper bound: every
// element in it was an ad, so nothing but the markers where they stood is left.
func TestWebRemoveAdsDropsAnAdOnlyContainerTooBigForTheWrapperRule(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<div class="frame">`)
	for i := 0; i < webAdWrapElems+10; i++ {
		b.WriteString(`<div class="ad-slot"></div>`)
	}
	b.WriteString(`</div>`)

	out, count := webRemoveAds(b.String(), webAdTestPage)
	if strings.Contains(out, `class="frame"`) || strings.Contains(out, "ad-slot") {
		t.Fatalf("the ad-only container is still drawn:\n%s", out)
	}
	if got := strings.TrimSpace(out); got != webAdCutMarker {
		t.Fatalf("cleaned markup = %q, want just the cut marker", got)
	}
	if want := webAdWrapElems + 10; count != want {
		t.Fatalf("blocked %d, want %d", count, want)
	}
}

// TestWebRemoveAdsKeepsABoxThatStillHoldsAnything checks the other side of the
// rule: only a box every one of whose elements was cut goes, so a container
// with one element the pass kept keeps drawing exactly as the page wrote it,
// even when it is too wide for the wrapper cut.
func TestWebRemoveAdsKeepsABoxThatStillHoldsAnything(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<div class="frame">`)
	for i := 0; i < webAdWrapElems+10; i++ {
		b.WriteString(`<div class="ad-slot"></div>`)
	}
	b.WriteString(`<div class="note"></div></div>`)

	out, count := webRemoveAds(b.String(), webAdTestPage)
	if want := webAdWrapElems + 10; count != want {
		t.Fatalf("blocked %d, want %d:\n%s", count, want, out)
	}
	if !strings.Contains(out, `class="frame"`) || !strings.Contains(out, `class="note"`) {
		t.Fatalf("a box with an element of the page's own was cut:\n%s", out)
	}
}
