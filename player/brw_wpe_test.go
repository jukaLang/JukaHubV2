package main

// brw_wpe_test.go — what reaches launch.sh as WPE Browser's address: only a
// plain http(s) URL, never a file, a script, credentials or an option.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWpeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBrwWpeTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.google.com/search?q=trimui+brick", "https://www.google.com/search?q=trimui+brick"},
		{"  HTTPS://Example.com/a b  ", "https://Example.com/a%20b"},
		{"http://example.com", "http://example.com"},
		{"", ""},
		{"about:blank", ""},
		{"file:///etc/passwd", ""},
		{"javascript:alert(1)", ""},
		{"https://user:pass@example.com/", ""},
		{"https:///no-host", ""},
		{"https://example.com/\nrm -rf /", ""},
		{"https://example.com/" + strings.Repeat("a", brwWpeMaxURL), ""},
		{"-rf", ""},
	}
	for _, c := range cases {
		if got := brwWpeTarget(c.in); got != c.want {
			t.Errorf("brwWpeTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
