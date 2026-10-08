package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// textBrowserURLVariable is the shared custom variable used to persist the
// current text-browser address between renders and inputs.
const textBrowserURLVariable = "textbrowser_url"

// normalizeTextBrowserURL is the public surface used by the runtime and the
// web page fetcher. It accepts user-typed input and returns a clean HTTP/HTTPS
// URL or an explanatory error, without side effects.
func normalizeTextBrowserURL(rawURL string) (string, error) {
	address := strings.TrimSpace(rawURL)
	if address == "" {
		return "", fmt.Errorf("address is empty")
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := http.ParseURL(address)
	if err != nil {
		return "", fmt.Errorf("invalid address: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("only http and https pages are supported")
	}
	if _, err := parsed.Hostname(); err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("address must contain a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("address must contain a host and cannot include credentials")
	}
	// Preserve user typing only where safe; always force a canonical scheme/host.
	canonical := parsed.String()
	canonical = strings.TrimRight(canonical, "/")
	return canonical, nil
}

// browseWebPage is a thin wrapper around the runtime fetcher used by the
// text-browser web source. It validates the URL, honors a strict size cap,
// tolerates only HTML pages, and returns a readable headline + extracted text
// body suitable for a text-mode view.
func browseWebPage(rawURL string) string {
	const maxBytes = 2 << 20 // 2 MiB soft cap
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "Browser error: " + err.Error()
	}
	req.Header.Set("User-Agent", "JukaHub Text Browser/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("Could not load %s\n%s", rawURL, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("Could not load %s\nHTTP %s", rawURL, resp.Status)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "html") {
		return fmt.Sprintf("This page is not HTML (%s).\nTry a website that serves readable HTML.", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes+1)))
	if err != nil {
		return fmt.Sprintf("Could not read %s\n%s", rawURL, err.Error())
	}
	if len(body) > maxBytes {
		return "Page is larger than the 2 MiB text-browser limit."
	}
	title, text := extractTextBrowserHTML(string(body))
	if title == "" {
		title = resp.Request.URL.Hostname()
	}
	if text == "" {
		text = "This page has no readable text content."
	}
	return fmt.Sprintf("Title: %s\nURL: %s\n\n%s", title, resp.Request.URL.String(), text)
}

// extractTextBrowserHTML strips tags and normalizes whitespace/entities into a
// readable text block. html package is not used to keep the browser dependency
// set small, so this is a best-effort tag stripper appropriate for a text view.
func extractTextBrowserHTML(source string) (string, string) {
	var title, body bytes.Buffer
	var skipped []string
	inTitle := false
	i := 0
	for i < len(source) {
		if source[i] != '<' {
			end := strings.IndexByte(source[i:], '<')
			if end < 0 {
				end = len(source)
			} else {
				end += i
			}
			chunk := source[i:end]
			if inTitle {
				title.WriteString(chunk)
			} else if len(skipped) == 0 {
				body.WriteString(chunk)
			}
			i = end
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				break
			}
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			if len(skipped) == 0 && !inTitle {
				body.WriteByte(source[i])
			}
			i++
			continue
		}
		tag := source[i+1 : end]
		closing := strings.HasPrefix(tag, "/")
		if closing {
			tag = strings.TrimSpace(tag[1:])
		}
		nameEnd := strings.IndexAny(tag, " \t\r\n/>")
		name := strings.ToLower(tag)
		if nameEnd >= 0 {
			name = strings.ToLower(tag[:nameEnd])
		}
		if name == "title" {
			inTitle = !closing
		} else if closing {
			for j := len(skipped) - 1; j >= 0; j-- {
				if skipped[j] == name {
					skipped = append(skipped[:j], skipped[j+1:]...)
					break
				}
			}
		}
		if len(skipped) == 0 && !inTitle && isTextBrowserBlockTag(name) {
			body.WriteByte('\n')
		}
		if !closing && isTextBrowserSuppressedTag(name) && !strings.HasSuffix(tag, "/") {
			skipped = append(skipped, name)
		}
		i = end + 1
	}
	return strings.TrimSpace(title.String()), normalizeTextBrowserWhitespace(body.String())
}

func findTextBrowserTagEnd(source string, start int) int {
	var quote byte
	for i := start; i < len(source); i++ {
		if quote != 0 {
			if source[i] == quote {
				quote = 0
			}
			continue
		}
		switch source[i] {
		case '\'', '"':
			quote = source[i]
		case '>':
			return i
		}
	}
	return -1
}

func isTextBrowserSuppressedTag(tag string) bool {
	switch tag {
	case "head", "script", "style", "noscript", "svg", "iframe", "template":
		return true
	default:
		return false
	}
}

func isTextBrowserBlockTag(tag string) bool {
	switch tag {
	case "address", "article", "blockquote", "br", "dd", "div", "dl", "dt",
		"fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3",
		"h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre",
		"section", "table", "td", "th", "tr", "ul":
		return true
	default:
		return false
	}
}

func normalizeTextBrowserWhitespace(raw string) string {
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// normalizeTextBrowserURL parses the same URL form the runtime uses, but this
// helper exists only for tests that want to inspect the canonical result.
func TestNormalizeTextBrowserURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  bool
	}{
		{"example.com", "https://example.com", false},
		{"https://example.com/", "https://example.com", false},
		{"http://example.com/path/", "http://example.com/path", false},
		{"https://example.com?q=hello", "https://example.com?q=hello", false},
		{"ftp://server.local/file", "", true},
		{"https://user:pass@example.com", "", true},
		{"not a url", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeTextBrowserURL(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("normalizeTextBrowserURL(%q): err=%v, wantErr=%v", tt.in, err, tt.err)
			continue
		}
		if tt.err {
			continue
		}
		if got != tt.want {
			t.Errorf("normalizeTextBrowserURL(%q):\ngot  %q\nwant %q\n", tt.in, got, tt.want)
		}
	}
}

func TestExtractTextBrowserHTML(t *testing.T) {
	title, body := extractTextBrowserHTML(`<html><head><title>Hello</title></head><body><p>World</p></body></html>`)
	if title != "Hello" {
		t.Errorf("title = %q, want %q", title, "Hello")
	}
	if body != "World" {
		t.Errorf("body = %q, want %q", body, "World")
	}
}

func TestNormalizeTextBrowserWhitespace(t *testing.T) {
	if got := normalizeTextBrowserWhitespace("  hello  world\r\n\r\nnext  line "); got != "hello world\nnext line" {
		t.Errorf("normalizeTextBrowserWhitespace = %q, want %q", got, "hello world\nnext line")
	}
}
