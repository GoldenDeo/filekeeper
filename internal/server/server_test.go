package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

// minimal valid PNG (1x1 transparent pixel)
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

const (
	testToken       = "tok_test"
	testUploadToken = "tok_upload_only"
)

func newTestServer(t *testing.T) (*httptest.Server, *blob.Memory) {
	t.Helper()
	dir := t.TempDir()
	metaStore, err := meta.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { metaStore.Close() })

	blobs := blob.NewMemory()
	h := New(Config{
		PublicBaseURL:  "http://files.test",
		SpoolDir:       dir,
		MaxUploadBytes: 1 << 20,
		AllowedMIME:    map[string]bool{"image/png": true, "image/gif": true},
		Tokens: map[string]Token{
			testToken:       {Service: "testsvc", Scope: ScopeFull},
			testUploadToken: {Service: "testapp", Scope: ScopeUpload},
		},
		SignSecret: []byte("0123456789abcdef0123456789abcdef"),
	}, metaStore, blobs, slog.New(slog.DiscardHandler))

	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, blobs
}

func upload(t *testing.T, ts *httptest.Server, content []byte, visibility string) uploadResponse {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if visibility != "" {
		mw.WriteField("visibility", visibility)
	}
	fw, _ := mw.CreateFormFile("file", "pic.png")
	fw.Write(content)
	mw.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Service-Token", testToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: got %d, body %s", res.StatusCode, body)
	}
	var out uploadResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUploadServeDeleteFlow(t *testing.T) {
	ts, blobs := newTestServer(t)

	up := upload(t, ts, pngBytes, "")
	if up.MIME != "image/png" {
		t.Errorf("mime: got %q, want image/png", up.MIME)
	}
	if up.Size != int64(len(pngBytes)) {
		t.Errorf("size: got %d, want %d", up.Size, len(pngBytes))
	}

	// Serve without auth (public file).
	res, err := ts.Client().Get(ts.URL + "/f/" + up.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, pngBytes) {
		t.Fatalf("serve: status %d, body match %v", res.StatusCode, bytes.Equal(body, pngBytes))
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("serve content-type: %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc == "" {
		t.Error("serve: missing Cache-Control")
	}

	// Conditional request → 304.
	req, _ := http.NewRequest("GET", ts.URL+"/f/"+up.ID, nil)
	req.Header.Set("If-None-Match", res.Header.Get("ETag"))
	res2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Errorf("conditional: got %d, want 304", res2.StatusCode)
	}

	// Range request → 206 with partial body.
	req, _ = http.NewRequest("GET", ts.URL+"/f/"+up.ID, nil)
	req.Header.Set("Range", "bytes=0-7")
	res3, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res3.Body)
	res3.Body.Close()
	if res3.StatusCode != http.StatusPartialContent || !bytes.Equal(part, pngBytes[:8]) {
		t.Errorf("range: status %d, body %v", res3.StatusCode, part)
	}

	// Delete → 204, then serve → 404, blob gone.
	req, _ = http.NewRequest("DELETE", ts.URL+"/v1/files/"+up.ID, nil)
	req.Header.Set("X-Service-Token", testToken)
	res4, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res4.Body.Close()
	if res4.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: got %d", res4.StatusCode)
	}
	res5, _ := ts.Client().Get(ts.URL + "/f/" + up.ID)
	res5.Body.Close()
	if res5.StatusCode != http.StatusNotFound {
		t.Errorf("serve after delete: got %d, want 404", res5.StatusCode)
	}
	if blobs.Len() != 0 {
		t.Errorf("blob store: %d objects left, want 0", blobs.Len())
	}
}

func TestDedupSharesBlobAcrossRecords(t *testing.T) {
	ts, blobs := newTestServer(t)

	a := upload(t, ts, pngBytes, "")
	b := upload(t, ts, pngBytes, "")
	if a.SHA256 != b.SHA256 {
		t.Fatal("same content must have same sha")
	}
	if blobs.Len() != 1 {
		t.Fatalf("dedup: %d blobs stored, want 1", blobs.Len())
	}

	// Deleting one record keeps the shared blob alive.
	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/files/"+a.ID, nil)
	req.Header.Set("X-Service-Token", testToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if blobs.Len() != 1 {
		t.Fatalf("after first delete: %d blobs, want 1", blobs.Len())
	}
	res2, _ := ts.Client().Get(ts.URL + "/f/" + b.ID)
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("second record must still serve, got %d", res2.StatusCode)
	}
}

func TestPrivateFileRequiresSignature(t *testing.T) {
	ts, _ := newTestServer(t)
	up := upload(t, ts, pngBytes, "private")

	res, _ := ts.Client().Get(ts.URL + "/f/" + up.ID)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned private: got %d, want 403", res.StatusCode)
	}

	// Sign via API, then fetch.
	req, _ := http.NewRequest("POST", ts.URL+"/v1/files/"+up.ID+"/sign?ttl=60", nil)
	req.Header.Set("X-Service-Token", testToken)
	res2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var signed struct {
		URL string `json:"url"`
	}
	json.NewDecoder(res2.Body).Decode(&signed)
	res2.Body.Close()

	// The signed URL uses PublicBaseURL; rewrite host to the test server.
	req3, _ := http.NewRequest("GET", ts.URL+signed.URL[len("http://files.test"):], nil)
	res3, err := ts.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Errorf("signed private: got %d, want 200", res3.StatusCode)
	}

	// Tampered signature must fail.
	res4, _ := ts.Client().Get(ts.URL + "/f/" + up.ID + "?exp=9999999999&sig=deadbeef")
	res4.Body.Close()
	if res4.StatusCode != http.StatusForbidden {
		t.Errorf("tampered sig: got %d, want 403", res4.StatusCode)
	}
}

func TestRejectsDisallowedType(t *testing.T) {
	ts, blobs := newTestServer(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "evil.svg")
	fw.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	mw.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Service-Token", testToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("svg upload: got %d, want 415", res.StatusCode)
	}
	if blobs.Len() != 0 {
		t.Errorf("rejected upload must not reach storage, got %d blobs", blobs.Len())
	}
}

func TestAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, tc := range []struct {
		method, path, token string
	}{
		{"POST", "/v1/files", ""},
		{"POST", "/v1/files", "wrong"},
		{"DELETE", "/v1/files/f_x", "wrong"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		if tc.token != "" {
			req.Header.Set("X-Service-Token", tc.token)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s token=%q: got %d, want 401", tc.method, tc.path, tc.token, res.StatusCode)
		}
	}
}

// An upload-scoped token is the one that ships inside client apps: it must be
// able to add files and nothing else, so leaking it costs storage, not data.
func TestUploadScopeMayOnlyUpload(t *testing.T) {
	ts, _ := newTestServer(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "pic.png")
	fw.Write(pngBytes)
	mw.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Service-Token", testUploadToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("upload with upload scope: got %d, body %s", res.StatusCode, body)
	}
	var up uploadResponse
	if err := json.NewDecoder(res.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	if up.Service != "testapp" {
		t.Errorf("service: got %q, want testapp", up.Service)
	}

	// Everything that reads or destroys is closed to it.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/files/" + up.ID},
		{"DELETE", "/v1/files/" + up.ID},
		{"POST", "/v1/files/" + up.ID + "/sign"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		req.Header.Set("X-Service-Token", testUploadToken)
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with upload scope: got %d, want 403", tc.method, tc.path, res.StatusCode)
		}
	}

	// The file survived the refused delete.
	req, _ = http.NewRequest("GET", ts.URL+"/v1/files/"+up.ID, nil)
	req.Header.Set("X-Service-Token", testToken)
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("file after refused delete: got %d, want 200", res.StatusCode)
	}
}

func TestCORS(t *testing.T) {
	const allowedOrigin = "http://localhost:18080"
	dir := t.TempDir()
	metaStore, err := meta.Open(filepath.Join(dir, "cors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { metaStore.Close() })

	newServer := func(origins []string) *httptest.Server {
		h := New(Config{
			PublicBaseURL:  "http://files.test",
			SpoolDir:       dir,
			MaxUploadBytes: 1 << 20,
			AllowedMIME:    map[string]bool{"image/png": true},
			Tokens:         map[string]Token{testToken: {Service: "testsvc", Scope: ScopeFull}},
			SignSecret:     []byte("0123456789abcdef0123456789abcdef"),
			CORSOrigins:    origins,
		}, metaStore, blob.NewMemory(), slog.New(slog.DiscardHandler))
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		return ts
	}

	t.Run("preflight is answered", func(t *testing.T) {
		ts := newServer([]string{allowedOrigin})
		req, _ := http.NewRequest("OPTIONS", ts.URL+"/v1/files", nil)
		req.Header.Set("Origin", allowedOrigin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "X-Service-Token")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		// Without the middleware ServeMux would answer 405: no OPTIONS route.
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight: got %d, want 204", res.StatusCode)
		}
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
			t.Errorf("allow-origin: got %q, want %q", got, allowedOrigin)
		}
		if h := res.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(h, "X-Service-Token") {
			t.Errorf("allow-headers must cover the token header, got %q", h)
		}
	})

	t.Run("real request carries the header", func(t *testing.T) {
		ts := newServer([]string{allowedOrigin})
		req, _ := http.NewRequest("GET", ts.URL+"/healthz", nil)
		req.Header.Set("Origin", allowedOrigin)
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
			t.Errorf("allow-origin: got %q, want %q", got, allowedOrigin)
		}
		if v := res.Header.Get("Vary"); !strings.Contains(v, "Origin") {
			t.Errorf("Vary must mention Origin, got %q", v)
		}
	})

	t.Run("unknown origin gets nothing", func(t *testing.T) {
		ts := newServer([]string{allowedOrigin})
		req, _ := http.NewRequest("GET", ts.URL+"/healthz", nil)
		req.Header.Set("Origin", "http://evil.test")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("allow-origin for unknown origin: got %q, want empty", got)
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		ts := newServer(nil)
		req, _ := http.NewRequest("GET", ts.URL+"/healthz", nil)
		req.Header.Set("Origin", allowedOrigin)
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("CORS off: got %q, want empty", got)
		}
	})
}

func TestScopeAllows(t *testing.T) {
	for _, tc := range []struct {
		have, required Scope
		want           bool
	}{
		{ScopeFull, ScopeFull, true},
		{ScopeFull, ScopeUpload, true},
		{ScopeUpload, ScopeUpload, true},
		{ScopeUpload, ScopeFull, false},
	} {
		if got := tc.have.allows(tc.required); got != tc.want {
			t.Errorf("%s.allows(%s): got %v, want %v", tc.have, tc.required, got, tc.want)
		}
	}
}

func TestUploadTooLarge(t *testing.T) {
	ts, blobs := newTestServer(t)

	big := make([]byte, 2<<20) // 2 MB > 1 MB limit
	copy(big, pngBytes)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "big.png")
	fw.Write(big)
	mw.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Service-Token", testToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: got %d (%s), want 413", res.StatusCode, body)
	}
	if blobs.Len() != 0 {
		t.Errorf("oversized upload must not reach storage")
	}
}

func TestDetectMIME(t *testing.T) {
	avif := append([]byte{0, 0, 0, 0x1c}, []byte("ftypavif")...)
	avif = append(avif, make([]byte, 16)...)
	for _, tc := range []struct {
		name string
		head []byte
		want string
	}{
		{"png", pngBytes, "image/png"},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), "image/gif"},
		{"avif", avif, "image/avif"},
		{"html", []byte("<!DOCTYPE html><html>"), "text/html"},
	} {
		if got := detectMIME(tc.head); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	sig := fileSignature(secret, "f_abc", 1700000000)
	if len(sig) != 64 {
		t.Fatalf("sig length: %d", len(sig))
	}
	if !verifySignature(secret, "f_abc", "1700000000", sig, 1699999999) {
		t.Error("valid signature rejected")
	}
	if verifySignature(secret, "f_abc", "1700000000", sig, 1700000001) {
		t.Error("expired signature accepted")
	}
	if verifySignature(secret, "f_other", "1700000000", sig, 1699999999) {
		t.Error("signature for other id accepted")
	}
}
