package main

import (
	"strings"
	"testing"
)

func TestPlugxVideoHostsAreValidatedAndRegistered(t *testing.T) {
	manifest := `{"videoHosts":[
		"vimeo.com", "*.dailymotion.com", "VIMEO.COM",
		"https://example.org/video", "example.org:8080", "evil.com/path",
		"not a host"
	]}`
	ext := plugxParse("video-sites", t.TempDir(), []byte(manifest))
	if len(ext.VideoHosts) != 2 || ext.VideoHosts[0] != "vimeo.com" || ext.VideoHosts[1] != "*.dailymotion.com" {
		t.Fatalf("hosts = %v, problems = %v", ext.VideoHosts, ext.Problems)
	}
	if len(ext.Problems) < 3 {
		t.Fatalf("bad hosts were not reported: %v", ext.Problems)
	}
	if !ext.hasAny() {
		t.Fatal("a video-host-only plugin was treated as empty")
	}
	summary, lines := plugxDescribe(ext)
	if !strings.Contains(summary, "2 video sites") || !strings.Contains(strings.Join(lines, "\n"), "Video site: vimeo.com") {
		t.Fatalf("card = %q %v", summary, lines)
	}

	plugxRegistry.mu.Lock()
	old := plugxRegistry.videoHosts
	plugxRegistry.mu.Unlock()
	defer func() {
		plugxRegistry.mu.Lock()
		plugxRegistry.videoHosts = old
		plugxRegistry.mu.Unlock()
	}()
	plugxInstall(nil, []plugxExtensions{ext}, nil)
	if got := plugxVideoHostSummary(); !strings.Contains(got, "vimeo.com (video-sites)") {
		t.Fatalf("settings summary = %q", got)
	}
	// Turning the plugin off drops the hosts with the next scan.
	plugxInstall(nil, nil, nil)
	if got := plugxVideoHostSummary(); got != "" {
		t.Fatalf("hosts survived a rescan without the plugin: %q", got)
	}
}
