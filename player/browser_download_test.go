package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browserDownloadsTestReset gives a test the registry to itself and cancels any
// context a test row still holds.
func browserDownloadsTestReset(t *testing.T) {
	t.Helper()
	browserDownloads.mu.Lock()
	for _, it := range browserDownloads.items {
		if it.cancel != nil {
			it.cancel()
		}
	}
	browserDownloads.items = nil
	browserDownloads.nextID = 0
	browserDownloads.mu.Unlock()
}

func TestBrowserDownloadName(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		disposition string
		contentType string
		hint        string
		wantBase    string
		wantExt     string
	}{
		{
			name:        "header wins over the address",
			url:         "https://example.com/download?id=7",
			disposition: `attachment; filename="Quarterly Report.pdf"`,
			contentType: "application/octet-stream",
			wantBase:    "Quarterly Report",
			wantExt:     ".pdf",
		},
		{
			name:        "header decodes an escaped name",
			url:         "https://example.com/download",
			disposition: `attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`,
			contentType: "application/pdf",
			wantBase:    "résumé",
			wantExt:     ".pdf",
		},
		{
			name:        "address path is decoded",
			url:         "https://example.com/files/Report%202026.pdf",
			contentType: "application/pdf",
			wantBase:    "Report 2026",
			wantExt:     ".pdf",
		},
		{
			name:        "page title gets an extension from its type",
			url:         "https://example.com/",
			contentType: "text/html; charset=utf-8",
			hint:        "Community notes",
			wantBase:    "Community notes",
			wantExt:     ".html",
		},
		{
			name:        "the address keeps an extension the type does not know",
			url:         "https://example.com/files/archive.tar.gz",
			contentType: "application/octet-stream",
			wantBase:    "archive.tar",
			wantExt:     ".gz",
		},
		{
			name:        "a path cannot escape Downloads",
			url:         "https://example.com/%2e%2e%2f%2e%2e%2fsecret.txt",
			contentType: "text/plain",
			wantBase:    "secret",
			wantExt:     ".txt",
		},
		{
			name:        "a header filename stays a name",
			url:         "https://example.com/download",
			disposition: `attachment; filename="../../etc/passwd"`,
			contentType: "application/octet-stream",
			wantBase:    "passwd",
			wantExt:     ".bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.contentType != "" {
				h.Set("Content-Type", tt.contentType)
			}
			if tt.disposition != "" {
				h.Set("Content-Disposition", tt.disposition)
			}
			base, ext := browserDownloadName(tt.url, h, tt.hint)
			if base != tt.wantBase || ext != tt.wantExt {
				t.Fatalf("browserDownloadName(%q, %q, %q) = %q, %q; want %q, %q",
					tt.url, tt.disposition, tt.hint, base, ext, tt.wantBase, tt.wantExt)
			}
			if strings.ContainsAny(base, `/\`) {
				t.Fatalf("download name contains a path separator: %q", base)
			}
		})
	}
}

func TestBrowserFetchToDirSavesAFile(t *testing.T) {
	const body = "%PDF-1.7\nnot really a pdf, but enough to save\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file", http.StatusFound)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
		io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()

	var progress []browserDownloadProgress
	path, name, err := browserFetchToDir(context.Background(), srv.Client(), srv.URL+"/start", dir, "", func(p browserDownloadProgress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("browserFetchToDir: %v", err)
	}
	if name != "report.pdf" {
		t.Fatalf("reported name = %q, want report.pdf", name)
	}
	if filepath.Base(path) != "report.pdf" {
		t.Fatalf("saved as %q, want report.pdf", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("saved body = %q, want %q", got, body)
	}
	if len(progress) == 0 {
		t.Fatal("browserFetchToDir reported no progress")
	}
	last := progress[len(progress)-1]
	if last.Done != int64(len(body)) || last.Total != int64(len(body)) || last.Name != "report.pdf" {
		t.Fatalf("last progress = %+v, want the whole report.pdf", last)
	}

	second, _, err := browserFetchToDir(context.Background(), srv.Client(), srv.URL+"/file", dir, "", nil)
	if err != nil {
		t.Fatalf("second browserFetchToDir: %v", err)
	}
	if filepath.Base(second) != "report (2).pdf" {
		t.Fatalf("second save = %q, want report (2).pdf", filepath.Base(second))
	}
}

func TestBrowserFetchToDirRejectsAnErrorPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, _, err := browserFetchToDir(context.Background(), srv.Client(), srv.URL+"/missing", dir, "", nil); err == nil {
		t.Fatal("browserFetchToDir accepted a 404")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed download left %d file(s) in Downloads", len(entries))
	}
}

func TestBrowserContentCardOffersDownload(t *testing.T) {
	buttons := brwButtonsFor(brwFailure{Kind: brwFailContent}, false)
	if len(buttons) == 0 || buttons[0].action != brwActDownload {
		t.Fatalf("content card buttons = %+v, want Download first", buttons)
	}
}

func TestBrowserDownloadPanelRowsAndCancel(t *testing.T) {
	downloadsTestReset(t)
	browserDownloadsTestReset(t)
	ctx, cancel := context.WithCancel(context.Background())
	browserDownloads.mu.Lock()
	browserDownloads.items = []*browserDownloadItem{{
		ID: 7, URL: "https://example.com/report.pdf", Title: "report.pdf",
		Status: downloadActive, Done: 40, Total: 100, cancel: cancel,
	}}
	browserDownloads.mu.Unlock()

	rows := downloadRowsFor(downloadTabAll)
	found := false
	for _, row := range rows {
		if row.browser && row.item.ID == 7 {
			found = true
			if row.item.Kind != downloadKindFile || row.item.percent() != 0.4 {
				t.Fatalf("browser row = %+v, want a 40%% file row", row.item)
			}
		}
	}
	if !found {
		t.Fatal("the Downloads panel omitted the browser save")
	}
	if !browserDownloadCancel(7) {
		t.Fatal("browserDownloadCancel refused a running save")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel did not stop the save's context")
	}
	if items := browserDownloadsSnapshot(); len(items) != 0 {
		t.Fatalf("canceled rows left in the panel: %+v", items)
	}
}

func TestBrowserDownloadPanelShowsProgressAndCompletion(t *testing.T) {
	active := downloadItem{Kind: downloadKindFile, Status: downloadActive, Done: 512, Total: 1024}
	if status, _ := downloadRowStatus(active); status != "50%" {
		t.Fatalf("active browser status = %q, want 50%%", status)
	}
	done := downloadItem{Kind: downloadKindFile, Status: downloadDone, Done: 1024, Total: 1024}
	if status, _ := downloadRowStatus(done); status != "Saved" {
		t.Fatalf("finished browser status = %q, want Saved", status)
	}
	if detail := downloadRowDetail(done); !strings.Contains(detail, "Saved in Downloads") || !strings.Contains(detail, "A opens it") {
		t.Fatalf("finished browser detail = %q, want its Downloads completion text", detail)
	}
}
