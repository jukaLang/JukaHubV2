package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserChromeNavigationHeaders(t *testing.T) {
	h := http.Header{}
	browserChromeNavigate(h)
	want := map[string]string{
		"User-Agent":                browserChromeUserAgent,
		"Accept":                    browserAcceptDocument,
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-Ch-Ua":                 browserChromeBrands,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Windows"`,
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-User":            "?1",
		"Sec-Fetch-Dest":            "document",
		"Upgrade-Insecure-Requests": "1",
		"Priority":                  "u=0, i",
	}
	for name, value := range want {
		if got := h.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if wantUA := "Chrome/157.0.0.0"; !strings.Contains(browserChromeUserAgent, wantUA) {
		t.Errorf("User-Agent %q does not carry the reduced Chrome major %q", browserChromeUserAgent, wantUA)
	}
}

func TestBrowserChromeResourceHeadersUsePageOrigin(t *testing.T) {
	page := &url.URL{Scheme: "https", Host: "example.com", Path: "/watch"}
	same := &url.URL{Scheme: "https", Host: "example.com", Path: "/style.css"}
	h := http.Header{}
	browserChromeResource(h, "style", browserAcceptStyle, page, same)
	if got := h.Get("Sec-Fetch-Site"); got != "same-origin" {
		t.Errorf("same-origin Sec-Fetch-Site = %q", got)
	}
	if got := h.Get("Referer"); got != page.String() {
		t.Errorf("same-origin Referer = %q, want %q", got, page)
	}
	if got := h.Get("Sec-Fetch-Mode"); got != "no-cors" {
		t.Errorf("Sec-Fetch-Mode = %q", got)
	}
	if got := h.Get("Accept"); got != browserAcceptStyle {
		t.Errorf("Accept = %q", got)
	}

	cross := &url.URL{Scheme: "https", Host: "cdn.example.net", Path: "/pic.png"}
	h = http.Header{}
	browserChromeResource(h, "image", browserAcceptImage, page, cross)
	if got := h.Get("Sec-Fetch-Site"); got != "cross-site" {
		t.Errorf("cross-site Sec-Fetch-Site = %q", got)
	}
	if got := h.Get("Referer"); got != "https://example.com/" {
		t.Errorf("cross-site Referer = %q", got)
	}
	if got := h.Get("Sec-Fetch-Dest"); got != "image" {
		t.Errorf("Sec-Fetch-Dest = %q", got)
	}
}

func TestBrowserDocumentFetchSendsChromeHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != browserChromeUserAgent {
			t.Errorf("document User-Agent = %q", got)
		}
		if got := r.Header.Get("Sec-Fetch-Dest"); got != "document" {
			t.Errorf("document Sec-Fetch-Dest = %q", got)
		}
		if got := r.Header.Get("Sec-CH-UA"); got != browserChromeBrands {
			t.Errorf("document Sec-CH-UA = %q", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()

	doc, err := fetchTextBrowserOnce(context.Background(), browserHTTPClient(2*time.Second), srv.URL)
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if !strings.Contains(string(doc.Body), "ok") {
		t.Fatalf("document body = %q", doc.Body)
	}
}

func TestBrowserRejectsNonWebDocumentSchemes(t *testing.T) {
	if _, err := fetchTextBrowserOnce(context.Background(), nil, "ftp://example.com/file"); err == nil {
		t.Fatal("an FTP document was accepted as a web page")
	}
}

func TestBrowserTransportNegotiatesHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr := browserTransport()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS request: %v", err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("negotiated %s, want HTTP/2", resp.Proto)
	}
}
