package main

// gdrive_api.go — the few Google Drive v3 calls save sync needs, on net/http
// and encoding/json alone (docs/CLOUD_SYNC.md).
//
// Layout in the account: one folder "JukaHub Saves" in My Drive, a folder per
// save-folder key inside it ("retroarch-saves", "ppsspp-savedata"), and the
// files by their relative path below that. Every folder JukaHub makes carries
// appProperties {jhkind: dir, jhdir: "<key>/<sub>"} and every save file
// {jhkind: save, jhsha: <SHA-256>, jhmtime: <unix>}, so two list calls (one
// for folders, one for files, both paged) describe the whole tree however the
// folders were moved or renamed in the Drive app. An existing file is updated
// in place, so Drive keeps its earlier versions (version history) for free.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	gdriveAPI        = "https://www.googleapis.com/drive/v3"
	gdriveUploadAPI  = "https://www.googleapis.com/upload/drive/v3"
	gdriveFolderMime = "application/vnd.google-apps.folder"
	// gdriveTopFolder is the folder in My Drive that holds everything.
	gdriveTopFolder = "JukaHub Saves"
	// gdriveTopKey is the top folder's jhdir.
	gdriveTopKey = "."
	// gdriveMultipartMax is the largest file sent in one multipart request;
	// larger ones use a resumable session (Google's advice is 5 MB).
	gdriveMultipartMax = 5 << 20
	// gdriveListLimit bounds one page of a list answer.
	gdriveListLimit = 32 << 20
	// gdriveMaxPropBytes is Drive's limit for one appProperty, key and value
	// together, in bytes.
	gdriveMaxPropBytes = 124
	// gdriveFileFields are the fields every answer about a file carries.
	gdriveFileFields = "id,name,mimeType,parents,size,sha256Checksum,md5Checksum,modifiedTime,createdTime,appProperties"
)

// gdriveFile is one file or folder as Drive describes it.
type gdriveFile struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	Parents       []string          `json:"parents"`
	Size          string            `json:"size"`
	SHA256        string            `json:"sha256Checksum"`
	MD5           string            `json:"md5Checksum"`
	ModifiedTime  string            `json:"modifiedTime"`
	CreatedTime   string            `json:"createdTime"`
	AppProperties map[string]string `json:"appProperties"`
}

// gdriveFileList is one page of files.list.
type gdriveFileList struct {
	NextPageToken    string       `json:"nextPageToken"`
	IncompleteSearch bool         `json:"incompleteSearch"`
	Files            []gdriveFile `json:"files"`
}

// gdriveParseList reads one page of files.list (outside data).
func gdriveParseList(data []byte) (gdriveFileList, error) {
	var list gdriveFileList
	if err := json.Unmarshal(data, &list); err != nil {
		return gdriveFileList{}, errors.New("Google Drive sent a file list this version cannot read")
	}
	if list.IncompleteSearch {
		// Never true for one user's own Drive; a partial list would read as
		// files deleted there, so it is refused rather than half-used.
		return gdriveFileList{}, errors.New("Google Drive could only list part of the files — try again later")
	}
	return list, nil
}

// gdriveAPIError is a Drive error answer.
type gdriveAPIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *gdriveAPIError) Error() string {
	switch {
	case e.Reason == "storageQuotaExceeded":
		return "your Google Drive is full"
	case e.Status == http.StatusNotFound:
		return "the file is no longer on Google Drive"
	case e.Reason == "accessNotConfigured", e.Reason == "SERVICE_DISABLED":
		return "the Google Drive API is not enabled for this build's Google Cloud project (docs/CLOUD_SYNC.md)"
	case e.Message != "":
		return fmt.Sprintf("Google Drive answered HTTP %d: %s", e.Status, firstLine(e.Message))
	}
	return fmt.Sprintf("Google Drive answered HTTP %d", e.Status)
}

// gdriveParseAPIError reads a Drive error body (outside data).
func gdriveParseAPIError(status int, body []byte) *gdriveAPIError {
	var doc struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	e := &gdriveAPIError{Status: status}
	if json.Unmarshal(body, &doc) == nil {
		e.Message = doc.Error.Message
		if len(doc.Error.Errors) > 0 {
			e.Reason = doc.Error.Errors[0].Reason
		} else if len(doc.Error.Details) > 0 {
			e.Reason = doc.Error.Details[0].Reason
		}
	}
	return e
}

// retryable reports whether the same request may simply be sent again later.
func (e *gdriveAPIError) retryable() bool {
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusForbidden:
		return e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
	}
	return false
}

// gdriveReq is one Drive call. body is called once per attempt so a retry
// sends the whole body again; once turns retries off (a resumable upload's
// bytes, which cannot simply be sent twice).
type gdriveReq struct {
	method      string
	url         string
	contentType string
	body        func() (io.Reader, error)
	size        int64
	header      http.Header
	once        bool
}

// gdriveRetryAfter reads a Retry-After header given in seconds. The date form
// is ignored: it depends on this device's clock.
func gdriveRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	if n > 60 {
		n = 60
	}
	return time.Duration(n) * time.Second
}

// gdriveDo sends r with a fresh access token. 401 refreshes the token once;
// 429, 5xx and rate-limit 403s back off (honouring Retry-After) and try again
// up to five times; a transport error is retried the same way. A 2xx answer
// is returned open; anything else as *gdriveAPIError.
func gdriveDo(ctx context.Context, r gdriveReq) (*http.Response, error) {
	const attempts = 5
	backoff := time.Second
	refreshed := false
	for attempt := 1; ; attempt++ {
		access, err := gdriveAccessToken(ctx)
		if err != nil {
			return nil, err
		}
		var body io.Reader
		if r.body != nil {
			if body, err = r.body(); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, r.method, r.url, body)
		if err != nil {
			return nil, err
		}
		if body != nil && r.size >= 0 {
			req.ContentLength = r.size
		}
		for k, vs := range r.header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("Authorization", "Bearer "+access)
		if r.contentType != "" {
			req.Header.Set("Content-Type", r.contentType)
		}
		resp, err := gdriveHTTPClient().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if r.once || attempt >= attempts {
				return nil, gdriveNetError(err)
			}
			if err := gdriveSleep(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, gdriveAnswerLimit))
		resp.Body.Close()
		apiErr := gdriveParseAPIError(resp.StatusCode, data)
		// A once-only body (a resumable upload's bytes) is spent: not re-sent.
		if resp.StatusCode == http.StatusUnauthorized && !refreshed && !r.once {
			refreshed = true
			gdriveDropAccessToken()
			continue
		}
		if !apiErr.retryable() || r.once || attempt >= attempts {
			return nil, apiErr
		}
		wait := backoff
		if ra := gdriveRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			wait = ra
		}
		if err := gdriveSleep(ctx, wait); err != nil {
			return nil, err
		}
		backoff *= 2
	}
}

// gdriveReadJSON decodes a 2xx answer and closes it.
func gdriveReadJSON(resp *http.Response, limit int64, out any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return gdriveNetError(err)
	}
	if int64(len(data)) > limit {
		return errors.New("Google Drive's answer is larger than expected")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("Google Drive sent an answer this version cannot read")
	}
	return nil
}

// gdriveListAll lists every file matching q, all pages.
func gdriveListAll(ctx context.Context, q string) ([]gdriveFile, error) {
	var out []gdriveFile
	token := ""
	for page := 0; page < 200; page++ {
		v := url.Values{}
		v.Set("q", q)
		v.Set("fields", "nextPageToken,incompleteSearch,files("+gdriveFileFields+")")
		v.Set("pageSize", "1000")
		v.Set("spaces", "drive")
		if token != "" {
			v.Set("pageToken", token)
		}
		resp, err := gdriveDo(ctx, gdriveReq{method: http.MethodGet, url: gdriveAPI + "/files?" + v.Encode()})
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, gdriveListLimit+1))
		resp.Body.Close()
		if err != nil {
			return nil, gdriveNetError(err)
		}
		if len(data) > gdriveListLimit {
			return nil, errors.New("Google Drive's file list is larger than one sync can read")
		}
		list, err := gdriveParseList(data)
		if err != nil {
			return nil, err
		}
		out = append(out, list.Files...)
		if len(out) > 2*ssyncMaxFiles {
			return nil, errSsyncTooMany
		}
		if list.NextPageToken == "" {
			return out, nil
		}
		token = list.NextPageToken
	}
	return nil, errors.New("Google Drive's file list did not end")
}

// gdriveCreateFolder makes one folder with its jhdir label.
func gdriveCreateFolder(ctx context.Context, name, parentID, key string) (string, error) {
	if len("jhdir")+len(key) > gdriveMaxPropBytes {
		return "", errors.New("the folder path is too long for Google Drive's labels")
	}
	meta, err := json.Marshal(map[string]any{
		"name":          name,
		"mimeType":      gdriveFolderMime,
		"parents":       []string{parentID},
		"appProperties": map[string]string{"jhkind": "dir", "jhdir": key},
	})
	if err != nil {
		return "", err
	}
	resp, err := gdriveDo(ctx, gdriveReq{
		method:      http.MethodPost,
		url:         gdriveAPI + "/files?fields=id",
		contentType: "application/json; charset=UTF-8",
		body:        func() (io.Reader, error) { return bytes.NewReader(meta), nil },
		size:        int64(len(meta)),
	})
	if err != nil {
		return "", err
	}
	var got gdriveFile
	if err := gdriveReadJSON(resp, gdriveAnswerLimit, &got); err != nil {
		return "", err
	}
	if got.ID == "" {
		return "", errors.New("Google Drive made the folder but did not say where")
	}
	return got.ID, nil
}

// gdriveSaveProps are the labels a save file carries.
func gdriveSaveProps(sha string, mtime int64) map[string]string {
	return map[string]string{"jhkind": "save", "jhsha": strings.ToLower(sha), "jhmtime": strconv.FormatInt(mtime, 10)}
}

// gdriveUploadMultipart sends a small file and its labels in one request.
// target is .../files (create, POST) or .../files/<id> (update in place,
// PATCH). The bytes are read into memory first and checked against f, so
// what lands on Drive is exactly the version the plan was made for.
func gdriveUploadMultipart(ctx context.Context, method, target string, meta map[string]any, src string, f ssyncFile) (gdriveFile, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return gdriveFile{}, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != f.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA) {
		return gdriveFile{}, errSsyncChanged
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return gdriveFile{}, err
	}
	boundary := "jukahub-" + ssyncNewSession()
	var buf bytes.Buffer
	buf.Grow(len(data) + len(metaJSON) + 256)
	buf.WriteString("--" + boundary + "\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n")
	buf.Write(metaJSON)
	buf.WriteString("\r\n--" + boundary + "\r\nContent-Type: application/octet-stream\r\n\r\n")
	buf.Write(data)
	buf.WriteString("\r\n--" + boundary + "--\r\n")
	body := buf.Bytes()
	resp, err := gdriveDo(ctx, gdriveReq{
		method:      method,
		url:         target + "?uploadType=multipart&fields=" + url.QueryEscape(gdriveFileFields),
		contentType: "multipart/related; boundary=" + boundary,
		body:        func() (io.Reader, error) { return bytes.NewReader(body), nil },
		size:        int64(len(body)),
	})
	if err != nil {
		return gdriveFile{}, err
	}
	var got gdriveFile
	if err := gdriveReadJSON(resp, gdriveAnswerLimit, &got); err != nil {
		return gdriveFile{}, err
	}
	return got, gdriveCheckUploaded(got, f)
}

// gdriveUploadResumable sends a large file through a resumable session: the
// labels first, then the bytes in one PUT. A PUT that fails is not resumed;
// the next sync starts a new session (save files are rarely past a few MB).
func gdriveUploadResumable(ctx context.Context, method, target string, meta map[string]any, src string, f ssyncFile) (gdriveFile, error) {
	file, err := os.Open(src)
	if err != nil {
		return gdriveFile{}, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Size() != f.Size {
		return gdriveFile{}, errSsyncChanged
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return gdriveFile{}, err
	}
	hdr := http.Header{}
	hdr.Set("X-Upload-Content-Type", "application/octet-stream")
	hdr.Set("X-Upload-Content-Length", strconv.FormatInt(f.Size, 10))
	// fields here shapes the final PUT's answer too, so Drive's own
	// sha256Checksum and size come back for gdriveCheckUploaded.
	resp, err := gdriveDo(ctx, gdriveReq{
		method:      method,
		url:         target + "?uploadType=resumable&fields=" + url.QueryEscape(gdriveFileFields),
		contentType: "application/json; charset=UTF-8",
		body:        func() (io.Reader, error) { return bytes.NewReader(metaJSON), nil },
		size:        int64(len(metaJSON)),
		header:      hdr,
	})
	if err != nil {
		return gdriveFile{}, err
	}
	session := resp.Header.Get("Location")
	_ = gdriveReadJSON(resp, gdriveAnswerLimit, nil)
	if !strings.HasPrefix(session, "https://www.googleapis.com/") {
		return gdriveFile{}, errors.New("Google Drive did not open an upload session")
	}
	h := sha256.New()
	resp, err = gdriveDo(ctx, gdriveReq{
		method:      http.MethodPut,
		url:         session,
		contentType: "application/octet-stream",
		body: func() (io.Reader, error) {
			return io.TeeReader(io.LimitReader(file, f.Size), h), nil
		},
		size: f.Size,
		once: true,
	})
	if err != nil {
		return gdriveFile{}, err
	}
	var got gdriveFile
	if err := gdriveReadJSON(resp, gdriveAnswerLimit, &got); err != nil {
		return gdriveFile{}, err
	}
	if sent := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(sent, f.SHA) && got.ID != "" {
		// The file changed while it was read: the copy on Drive is labelled
		// with what was really sent, and this file waits for the next sync.
		fix, _ := json.Marshal(map[string]any{"appProperties": map[string]string{"jhsha": sent}})
		if resp, err := gdriveDo(ctx, gdriveReq{
			method:      http.MethodPatch,
			url:         gdriveAPI + "/files/" + url.PathEscape(got.ID) + "?fields=id",
			contentType: "application/json; charset=UTF-8",
			body:        func() (io.Reader, error) { return bytes.NewReader(fix), nil },
			size:        int64(len(fix)),
		}); err == nil {
			_ = gdriveReadJSON(resp, gdriveAnswerLimit, nil)
		}
		return got, errSsyncChanged
	}
	return got, gdriveCheckUploaded(got, f)
}

// gdriveCheckUploaded compares Drive's own checksum of what it stored with
// the file that was meant to go up.
func gdriveCheckUploaded(got gdriveFile, f ssyncFile) error {
	if got.ID == "" {
		return errors.New("Google Drive stored the file but did not say where")
	}
	if got.SHA256 != "" && !strings.EqualFold(got.SHA256, f.SHA) {
		return errSsyncChecksum
	}
	if got.Size != "" {
		if n, err := strconv.ParseInt(got.Size, 10, 64); err == nil && n != f.Size {
			return errSsyncChecksum
		}
	}
	return nil
}

// gdriveDownload opens a file's bytes.
func gdriveDownload(ctx context.Context, id string) (*http.Response, error) {
	return gdriveDo(ctx, gdriveReq{method: http.MethodGet, url: gdriveAPI + "/files/" + url.PathEscape(id) + "?alt=media"})
}

// gdriveDirKeyOf is the jhdir of the folder holding rel inside root.
func gdriveDirKeyOf(root, rel string) string {
	if d := path.Dir(rel); d != "." && d != "/" {
		return root + "/" + d
	}
	return root
}
