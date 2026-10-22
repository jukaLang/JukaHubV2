package main

// changelog.go — release notes for the in-app changelog.
//
// The updater already talks to GitHub; the only extra data needed is the
// release body. It is cached in patch state before a swap so the notes survive
// the restart, and a seen-tag marker makes the screen appear exactly once per
// installed release.

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

var changelogNotesLoading atomic.Bool

// installedReleaseTag identifies the running build's release. The bundled
// release-info.json wins; patch state covers installs whose stamp was written
// by an older updater.
func installedReleaseTag() string {
	if info := readInstalledReleaseInfo(); strings.TrimSpace(info.Tag) != "" {
		return strings.TrimSpace(info.Tag)
	}
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	if patchState.InstalledReleaseTag != "" {
		return patchState.InstalledReleaseTag
	}
	return patchState.LastUpdateVer
}

// changelogDue reports whether this installed release has not been shown yet.
func changelogDue(installed, seen string) bool {
	installed = strings.TrimSpace(installed)
	return installed != "" && installed != strings.TrimSpace(seen)
}

// maybeShowChangelogAfterUpdate opens the changelog once, on the first launch
// of a newly installed release. It is called after InitPatchModule so the
// seen marker and cached notes are already loaded.
func maybeShowChangelogAfterUpdate(config *Config) {
	installed := installedReleaseTag()
	patchStateMu.Lock()
	seen := patchState.ChangelogSeenTag
	patchStateMu.Unlock()
	if !changelogDue(installed, seen) {
		return
	}
	openCommunityPanel(config, communityPageChangelog)
}

func markChangelogSeen(tag string) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return
	}
	patchStateMu.Lock()
	if patchState.ChangelogSeenTag == tag {
		patchStateMu.Unlock()
		return
	}
	patchState.ChangelogSeenTag = tag
	patchStateMu.Unlock()
	_ = savePatchState()
}

// startChangelog prepares the notes page: it marks this release seen and, when
// the cache does not cover it, fetches the latest notes in the background.
func startChangelog(config *Config) {
	tag := installedReleaseTag()
	markChangelogSeen(tag)
	refreshReleaseNotesAsync(config)
}

func cachedReleaseNotes() (string, string) {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return patchState.ReleaseNotesTag, patchState.ReleaseNotesText
}

// refreshReleaseNotesAsync avoids a blocking network call on the render path.
// The result is stored in patch state, which the changelog renderer reads every
// frame, so the page fills in when the request completes.
func refreshReleaseNotesAsync(config *Config) {
	if changelogNotesLoading.Load() {
		return
	}
	installed := installedReleaseTag()
	tag, notes := cachedReleaseNotes()
	if notes != "" && (installed == "" || tag == installed) {
		return
	}
	changelogNotesLoading.Store(true)
	go func() {
		defer changelogNotesLoading.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		rel, err := FetchLatestJukaHubRelease(ctx)
		if err != nil {
			logPatch("changelog: notes fetch failed: %v", err)
			return
		}
		storeReleaseNotes(rel)
	}()
}

// changelogVersionLabel is the short value shown in the hub row and header.
func changelogVersionLabel(config *Config) string {
	if tag := installedReleaseTag(); tag != "" {
		return tag
	}
	if info := readInstalledReleaseInfo(); strings.TrimSpace(info.Version) != "" {
		return info.Version
	}
	if config != nil {
		return versionString(config)
	}
	return ""
}

func changelogSubtitle(config *Config) string {
	installed := installedReleaseTag()
	tag, notes := cachedReleaseNotes()
	if notes == "" && changelogNotesLoading.Load() {
		return "Loading release notes…"
	}
	if tag != "" && installed != "" && tag != installed {
		return "Latest release " + tag + " (installed: " + installed + ")"
	}
	parts := make([]string, 0, 3)
	if label := changelogVersionLabel(config); label != "" {
		parts = append(parts, label)
	}
	if date := readInstalledReleaseInfo().Date; date != "" {
		if t, ok := parseReleaseTimestamp(date); ok {
			parts = append(parts, t.Format("2006-01-02"))
		}
	}
	if len(parts) == 0 {
		return "Release notes for this build"
	}
	return strings.Join(parts, " · ")
}

// communityChangelogText turns the cached markdown into display lines, with a
// useful fallback for offline or externally installed builds.
func communityChangelogText(config *Config) []string {
	_, body := cachedReleaseNotes()
	lines := plainReleaseNotes(body)
	if len(lines) > 0 {
		return lines
	}
	label := changelogVersionLabel(config)
	if label == "" {
		label = "this build"
	}
	out := []string{
		"Release notes aren't cached on this device.",
		"",
		"Installed: " + label,
	}
	if changelogNotesLoading.Load() {
		out = append(out, "", "Fetching release notes…")
	} else {
		out = append(out, "", "The full changelog is on the JukaHub GitHub releases page.")
	}
	return out
}

// plainReleaseNotes removes the markdown decoration a GitHub release body uses
// so it reads cleanly in SDL's plain text renderer. It is intentionally lossy:
// code fences, images, HTML comments and emphasis markers carry no meaning here.
func plainReleaseNotes(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var out []string
	inComment := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			if !strings.Contains(trimmed, "-->") {
				inComment = true
			}
			continue
		}
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			continue
		}
		if strings.HasPrefix(trimmed, "![") {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			trimmed = "• " + strings.TrimSpace(trimmed[2:])
		}
		trimmed = stripInlineMarkdown(trimmed)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
		if len(out) >= 500 {
			out = append(out, "…")
			break
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func stripInlineMarkdown(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	// [label](url) -> label; release notes often link issue numbers, and the
	// raw URL is long enough to wreck wrapping on a 720p screen.
	for {
		start := strings.Index(s, "[")
		if start < 0 {
			break
		}
		mid := strings.Index(s[start:], "](")
		if mid < 0 {
			break
		}
		mid += start
		end := strings.Index(s[mid:], ")")
		if end < 0 {
			break
		}
		end += mid
		s = s[:start] + s[start+1:mid] + s[end+1:]
	}
	// Drop simple inline HTML tags (<details>, <summary>, <br>).
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// communityChangelogLines wraps the plain notes to the panel width.
func communityChangelogLines(font *ttf.Font, width int32) []string {
	text := communityChangelogText(appConfig)
	lines := make([]string, 0, len(text))
	for _, line := range text {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapPackageText(line, font, width)...)
	}
	return lines
}

// renderCommunityChangelog draws the wrapped notes and a scrollbar. The panel
// chrome and title are drawn by renderCommunityPanel.
func renderCommunityChangelog(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, smallH int32) {
	contentX := panelX + padX
	contentY := panelY + 86
	contentW := panelW - 2*padX - 12
	contentBottom := panelY + panelH - 38
	lineH := smallH + 3
	lines := communityChangelogLines(font, contentW)
	visible := int((contentBottom - contentY) / lineH)
	if visible < 1 {
		visible = 1
	}
	maxScroll := len(lines) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if communityPanel.scroll > maxScroll {
		communityPanel.scroll = maxScroll
	}
	if communityPanel.scroll < 0 {
		communityPanel.scroll = 0
	}
	for i := 0; i < visible; i++ {
		idx := communityPanel.scroll + i
		if idx >= len(lines) {
			break
		}
		if lines[idx] == "" {
			continue
		}
		renderText(renderer, config, font, lines[idx], ColorTextSecondary(), contentX, contentY+int32(i)*lineH)
	}

	if maxScroll > 0 {
		trackX := panelX + panelW - 10
		trackY := contentY
		trackH := contentBottom - contentY
		track := sdl.Rect{X: trackX, Y: trackY, W: 4, H: trackH}
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, 2, WithAlpha(ColorBorderDefault, 90))
		thumbH := int32(float64(trackH) * float64(visible) / float64(len(lines)))
		if thumbH < 24 {
			thumbH = 24
		}
		thumbY := trackY
		if maxScroll > 0 {
			thumbY += int32(float64(trackH-thumbH) * float64(communityPanel.scroll) / float64(maxScroll))
		}
		fillRoundedRect(renderer, track.X, thumbY, track.W, thumbH, 2, WithAlpha(accentColor, 170))
	}

	footer := hintLine(FooterHint{"Up/Down", "Scroll"}, FooterHint{"B", "Back"})
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
