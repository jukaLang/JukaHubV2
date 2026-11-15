package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCertSummaryReportsUntrustedCertificateWithoutAcceptingIt(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("certificate inspection sent an HTTP request")
	}))
	defer server.Close()

	summary, err := certSummary(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Subject:", "Issuer:", "Trust:   not trusted:"} {
		if !strings.Contains(summary, want) {
			t.Errorf("certificate report is missing %q: %s", want, summary)
		}
	}
}
