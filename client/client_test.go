package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
	"github.com/GoldenDeo/filekeeper/internal/server"
)

var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

const (
	testToken  = "tok_client_test"
	testSecret = "0123456789abcdef0123456789abcdef"
)

// newStack runs the real filekeeper HTTP server over an in-memory blob store,
// so the client is tested against the same contract the service ships.
func newStack(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	metaStore, err := meta.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { metaStore.Close() })

	ts := httptest.NewServer(server.New(server.Config{
		PublicBaseURL:  "http://files.test",
		SpoolDir:       dir,
		MaxUploadBytes: 1 << 20,
		AllowedMIME:    map[string]bool{"image/png": true},
		Tokens:         map[string]server.Token{testToken: {Service: "clientsvc", Scope: server.ScopeFull}},
		SignSecret:     []byte(testSecret),
	}, metaStore, blob.NewMemory(), slog.New(slog.DiscardHandler)))
	t.Cleanup(ts.Close)

	return New(ts.URL, testToken,
		WithSignSecret(testSecret),
		WithImgproxy("http://imgproxy.test", "aabbcc", "ddeeff", "blobs"),
	)
}

func TestUploadMetaDeleteFlow(t *testing.T) {
	fk := newStack(t)
	ctx := context.Background()

	f, err := fk.Upload(ctx, bytes.NewReader(pngBytes), UploadOptions{Filename: "pic.png"})
	if err != nil {
		t.Fatal(err)
	}
	if f.MIME != "image/png" || f.Size != int64(len(pngBytes)) || f.Visibility != "public" {
		t.Fatalf("unexpected file: %+v", f)
	}

	got, err := fk.Meta(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != f.SHA256 {
		t.Errorf("meta sha mismatch")
	}

	// Public file is fetchable at PublicURL (rewrite test-server host).
	res, err := http.Get(strings.Replace(fk.PublicURL(f.ID), "http://files.test", fk.baseURL, 1))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, pngBytes) {
		t.Fatalf("public fetch: %d", res.StatusCode)
	}

	if err := fk.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fk.Meta(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: got %v, want ErrNotFound", err)
	}
	if err := fk.Delete(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete: got %v, want ErrNotFound", err)
	}
}

func TestPrivateSignedURL(t *testing.T) {
	fk := newStack(t)
	ctx := context.Background()

	f, err := fk.Upload(ctx, bytes.NewReader(pngBytes), UploadOptions{Private: true})
	if err != nil {
		t.Fatal(err)
	}

	// Unsigned access must fail.
	res, _ := http.Get(fk.baseURL + "/f/" + f.ID)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned: got %d, want 403", res.StatusCode)
	}

	// Locally signed URL must work.
	u, err := fk.SignedURL(f.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := http.Get(strings.Replace(u, "http://files.test", fk.baseURL, 1))
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("signed: got %d, want 200", res2.StatusCode)
	}
}

func TestErrorMapping(t *testing.T) {
	fk := newStack(t)
	ctx := context.Background()

	if _, err := fk.Upload(ctx, strings.NewReader("<html></html>"), UploadOptions{}); !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("html upload: got %v, want ErrUnsupportedType", err)
	}

	big := make([]byte, 2<<20)
	copy(big, pngBytes)
	if _, err := fk.Upload(ctx, bytes.NewReader(big), UploadOptions{}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big upload: got %v, want ErrTooLarge", err)
	}

	bad := New(fk.baseURL, "wrong-token")
	if _, err := bad.Upload(ctx, bytes.NewReader(pngBytes), UploadOptions{}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad token: got %v, want ErrUnauthorized", err)
	}
}

func TestImageURLShape(t *testing.T) {
	fk := newStack(t)
	sha := strings.Repeat("ab", 32)
	u, err := fk.ImageURL(sha, "rs:fill:300:300/f:webp")
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "/rs:fill:300:300/f:webp/plain/s3://blobs/blobs/ab/ab/" + sha
	if !strings.HasSuffix(u, wantSuffix) || !strings.HasPrefix(u, "http://imgproxy.test/") {
		t.Errorf("bad image url: %s", u)
	}

	if _, err := fk.ImageURL("short", "rs:fit:100:100"); err == nil {
		t.Error("bad sha must error")
	}
	plain := New("http://x", "t")
	if _, err := plain.ImageURL(sha, "rs:fit:100:100"); err == nil {
		t.Error("ImageURL without WithImgproxy must error")
	}
}
