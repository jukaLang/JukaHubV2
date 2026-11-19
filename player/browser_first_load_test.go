package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type firstLoadTransport func(*http.Request) (*http.Response, error)

func (f firstLoadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBrowserFirstGETRetriesDroppedConnection(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: firstLoadTransport(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, io.EOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("<html><body>Ready to search</body></html>")),
			Request:    req,
		}, nil
	})}
	doc, err := fetchTextBrowserDocumentWithRetry(context.Background(), client, browserNavigation{URL: "https://lite.duckduckgo.com/lite/"})
	if err != nil || !strings.Contains(string(doc.Body), "Ready to search") {
		t.Fatalf("first navigation did not recover: err=%v body=%q", err, doc.Body)
	}
	if attempts != 2 {
		t.Fatalf("made %d requests, want one failed GET and one retry", attempts)
	}
}

func TestBrowserFirstPOSTDoesNotRetryDroppedConnection(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: firstLoadTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, io.EOF
	})}
	_, err := fetchTextBrowserDocumentWithRetry(context.Background(), client, browserNavigation{URL: "https://example.com/search", Method: http.MethodPost, Body: []byte("q=test")})
	if err == nil || attempts != 1 {
		t.Fatalf("POST error=%v attempts=%d, want one failed request", err, attempts)
	}
}
