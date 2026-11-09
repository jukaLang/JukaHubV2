package main

import "testing"

func TestExternalURLAllowed(t *testing.T) {
	cases := []struct {
		raw    string
		want   string
		accept bool
	}{
		// The case SEC-4 was filed for: cmd.exe used to interpret "&calc".
		{"https://x/?a=1&calc", "https://x/?a=1&calc", true},
		{"http://example.com/path?q=a&b=(c)", "http://example.com/path?q=a&b=(c)", true},
		{"  https://example.com/page  ", "https://example.com/page", true},
		{"mailto:someone@example.com?subject=Hi there", "mailto:someone@example.com?subject=Hi there", true},
		{"javascript:alert(1)", "", false},
		{"file:///C:/Windows/System32/calc.exe", "", false},
		{"cmd:/c/calc", "", false},
		{"https://", "", false},
		{"", "", false},
		{"   ", "", false},
		{"not a url", "", false},
	}
	for _, c := range cases {
		got, ok := externalURLAllowed(c.raw)
		if ok != c.accept {
			t.Errorf("externalURLAllowed(%q) accepted=%v, want %v", c.raw, ok, c.accept)
			continue
		}
		if ok && got != c.want {
			t.Errorf("externalURLAllowed(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
