package main

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserFormPOSTKeepsFieldsOutOfURLAndFollowsCookieRedirect(t *testing.T) {
	var posted, landed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/form":
			http.SetCookie(w, &http.Cookie{Name: "start", Value: "ready", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<form method="POST" action="/submit?fixed=1"><input type="hidden" name="csrf" value="token"><input name="user"><input type="password" name="password"><button type="submit" name="go" value="yes">Sign in</button></form>`)
		case "/submit":
			posted.Store(true)
			if r.Method != http.MethodPost || r.URL.RawQuery != "fixed=1" {
				t.Errorf("submit request = %s %s", r.Method, r.URL.String())
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("content type = %q", r.Header.Get("Content-Type"))
			}
			if got := r.Header.Get("Origin"); got != "http://"+r.Host {
				t.Errorf("origin = %q", got)
			}
			if got := r.Header.Get("Referer"); got != "http://"+r.Host+"/form" {
				t.Errorf("referer = %q", got)
			}
			if got := r.Header.Get("Sec-Fetch-Site"); got != "same-origin" {
				t.Errorf("fetch site = %q", got)
			}
			if c, err := r.Cookie("start"); err != nil || c.Value != "ready" {
				t.Errorf("initial cookie = %v, %v", c, err)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			} else {
				for name, want := range map[string]string{
					"csrf": "token", "user": "Ada", "password": " space pass ", "go": "yes",
				} {
					if got := r.PostForm.Get(name); got != want {
						t.Errorf("posted %s = %q, want %q", name, got, want)
					}
				}
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "signed-in", Path: "/"})
			http.Redirect(w, r, "/done", http.StatusSeeOther)
		case "/done":
			landed.Store(true)
			if r.Method != http.MethodGet {
				t.Errorf("redirect method = %s", r.Method)
			}
			if got := r.Header.Get("Origin"); got != "" {
				t.Errorf("redirected GET retained Origin %q", got)
			}
			if strings.Contains(r.URL.String(), "password") {
				t.Errorf("password reached redirect URL %q", r.URL.String())
			}
			if c, err := r.Cookie("session"); err != nil || c.Value != "signed-in" {
				t.Errorf("session cookie = %v, %v", c, err)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html><body>signed in</body></html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &http.Client{
		Timeout:       5 * time.Second,
		Jar:           newBrowserCookieJar(filepath.Join(t.TempDir(), "cookies.json")),
		CheckRedirect: textBrowserRedirectPolicy,
	}
	ctx := context.Background()
	formPage, err := fetchTextBrowserDocument(ctx, client, srv.URL+"/form")
	if err != nil {
		t.Fatalf("get form: %v", err)
	}
	doc := parseWebPage(string(formPage.Body), "", formPage.URL.String())
	if doc == nil {
		t.Fatal("form page did not parse")
	}
	const variable = "browser_form_post_test"
	setBrowserDocument(variable, doc)
	defer setBrowserDocument(variable, nil)
	var form webForm
	for _, ctrl := range browserFormControls(Element{Variable: variable}) {
		if ctrl.Kind == "submit" {
			form = ctrl.Form
			if ctrl.Display() != " Sign in " || ctrl.Value != "yes" {
				t.Errorf("button label/value = %q/%q", ctrl.Display(), ctrl.Value)
			}
		}
	}
	if form.Method != "post" || form.Action != "/submit?fixed=1" {
		t.Fatalf("parsed form = %+v", form)
	}
	if len(doc.FormHidden[form.ID]) != 1 {
		t.Fatalf("hidden fields = %v", doc.FormHidden[form.ID])
	}
	values := url.Values{
		"csrf": {doc.FormHidden[form.ID][0].Value},
		"user": {"Ada"}, "password": {" space pass "}, "go": {"yes"},
	}
	nav, err := browserFormNavigation(form, formPage.URL.String(), values)
	if err != nil {
		t.Fatalf("build form request: %v", err)
	}
	if nav.Method != http.MethodPost || strings.Contains(nav.URL, "password") ||
		!strings.Contains(string(nav.Body), "password=") {
		t.Fatalf("form request method=%q URL=%q body=%q", nav.Method, nav.URL, nav.Body)
	}
	result, err := fetchTextBrowserDocumentRequest(ctx, client, nav)
	if err != nil {
		t.Fatalf("post form: %v", err)
	}
	if !posted.Load() || !landed.Load() || result.URL.String() != srv.URL+"/done" ||
		!strings.Contains(string(result.Body), "signed in") {
		t.Fatalf("POST result: posted=%v landed=%v URL=%s body=%q", posted.Load(), landed.Load(), result.URL, result.Body)
	}
}

func TestBrowserMultipartFormSendsTextFields(t *testing.T) {
	form := webForm{Method: "post", Enctype: "multipart/form-data", Action: "/upload"}
	nav, err := browserFormNavigation(form, "https://example.com/form", url.Values{"title": {"A note"}, "tag": {"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(nav.ContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type = %q: %v", nav.ContentType, err)
	}
	parts, err := multipart.NewReader(strings.NewReader(string(nav.Body)), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("read multipart body: %v", err)
	}
	defer parts.RemoveAll()
	if got := parts.Value["title"]; len(got) != 1 || got[0] != "A note" {
		t.Errorf("title = %v", got)
	}
	if got := parts.Value["tag"]; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("tags = %v", got)
	}
}

func TestBrowserGETFormKeepsItsQueryAndRejectsNonWebAction(t *testing.T) {
	nav, err := browserFormNavigation(webForm{Action: "/search?lang=en"},
		"https://example.com/", url.Values{"q": {"handheld browser"}})
	if err != nil {
		t.Fatal(err)
	}
	if nav.Method != "" || len(nav.Body) != 0 ||
		nav.URL != "https://example.com/search?lang=en&q=handheld+browser" {
		t.Fatalf("GET form request = %+v", nav)
	}
	if _, err := browserFormNavigation(webForm{Action: "javascript:alert(1)"},
		"https://example.com/", url.Values{"q": {"x"}}); err == nil {
		t.Fatal("a non-web form action was accepted")
	}
}

func TestBrowserPOSTRedirectKeepsBodyAndRefusesDowngrade(t *testing.T) {
	var received atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/finish", http.StatusTemporaryRedirect)
			return
		}
		received.Store(true)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || string(body) != "answer=42" {
			t.Errorf("307 result: method=%s body=%q error=%v", r.Method, body, err)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<p>done</p>")
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: textBrowserRedirectPolicy}
	nav := browserNavigation{
		URL: srv.URL + "/start", Method: http.MethodPost,
		Body: []byte("answer=42"), ContentType: "application/x-www-form-urlencoded",
	}
	if _, err := fetchTextBrowserDocumentRequest(context.Background(), client, nav); err != nil || !received.Load() {
		t.Fatalf("307 POST: received=%v error=%v", received.Load(), err)
	}
	from := httptest.NewRequest(http.MethodPost, "https://example.com/start", nil)
	to := httptest.NewRequest(http.MethodPost, "http://example.com/finish", nil)
	if err := textBrowserRedirectPolicy(to, []*http.Request{from}); err == nil {
		t.Fatal("HTTPS POST was allowed to redirect to plain HTTP")
	}
}
