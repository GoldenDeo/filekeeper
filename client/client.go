// Package client is the Go SDK for filekeeper. A consuming microservice
// stores only the returned file ID; everything else (serving URLs, signing,
// image variants) is derived from it locally where possible.
package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Sentinel errors mapped from API status codes.
var (
	ErrNotFound        = errors.New("filekeeper: not found")
	ErrUnauthorized    = errors.New("filekeeper: invalid or missing service token")
	ErrTooLarge        = errors.New("filekeeper: file exceeds size limit")
	ErrUnsupportedType = errors.New("filekeeper: file type not allowed")
)

// APIError is returned for unexpected non-2xx responses.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("filekeeper: HTTP %d: %s", e.Status, e.Message)
}

// File mirrors the server's file record.
type File struct {
	ID         string    `json:"id"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	Filename   string    `json:"filename,omitempty"`
	Visibility string    `json:"visibility"`
	Service    string    `json:"service"`
	CreatedAt  time.Time `json:"created_at"`
	URL        string    `json:"url,omitempty"`
}

type Client struct {
	baseURL    string
	token      string
	httpc      *http.Client
	signSecret []byte

	imgproxyURL    string
	imgproxyKey    []byte
	imgproxySalt   []byte
	imgproxyBucket string
	imgproxyErr    error
}

type Option func(*Client)

// WithHTTPClient replaces the default http.Client (no global timeout: upload
// duration is bounded by the passed context, not a wall-clock cap).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpc = h }
}

// WithSignSecret enables local signing of private-file URLs (FK_SIGN_SECRET).
func WithSignSecret(secret string) Option {
	return func(c *Client) { c.signSecret = []byte(secret) }
}

// WithImgproxy enables ImageURL. keyHex/saltHex are IMGPROXY_KEY/IMGPROXY_SALT;
// bucket is the S3 bucket filekeeper stores blobs in (FK_S3_BUCKET, "blobs" by
// default).
func WithImgproxy(baseURL, keyHex, saltHex, bucket string) Option {
	return func(c *Client) {
		c.imgproxyURL = strings.TrimSuffix(baseURL, "/")
		c.imgproxyBucket = bucket
		var err error
		if c.imgproxyKey, err = hex.DecodeString(keyHex); err != nil {
			c.imgproxyErr = fmt.Errorf("filekeeper: bad imgproxy key: %w", err)
		}
		if c.imgproxySalt, err = hex.DecodeString(saltHex); err != nil {
			c.imgproxyErr = fmt.Errorf("filekeeper: bad imgproxy salt: %w", err)
		}
	}
}

func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		baseURL:        strings.TrimSuffix(baseURL, "/"),
		token:          token,
		httpc:          &http.Client{},
		imgproxyBucket: "blobs",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// UploadOptions controls a single upload.
type UploadOptions struct {
	Filename string
	Private  bool
}

// Upload streams r to filekeeper without buffering it in memory.
func (c *Client) Upload(ctx context.Context, r io.Reader, opts UploadOptions) (*File, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		err := func() error {
			if opts.Private {
				if err := mw.WriteField("visibility", "private"); err != nil {
					return err
				}
			}
			name := opts.Filename
			if name == "" {
				name = "file"
			}
			fw, err := mw.CreateFormFile("file", name)
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, r); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/files", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Service-Token", c.token)

	var f File
	if err := c.do(req, http.StatusCreated, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Meta fetches the file record.
func (c *Client) Meta(ctx context.Context, id string) (*File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/files/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Token", c.token)
	var f File
	if err := c.do(req, http.StatusOK, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Delete removes the file record (the blob is GC'd server-side when the last
// record referencing it is gone).
func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/files/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Token", c.token)
	return c.do(req, http.StatusNoContent, nil)
}

// PublicURL returns the serving URL of a public file.
func (c *Client) PublicURL(id string) string {
	return c.baseURL + "/f/" + id
}

func (c *Client) do(req *http.Request, wantStatus int, out any) error {
	res, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != wantStatus {
		switch res.StatusCode {
		case http.StatusNotFound:
			return ErrNotFound
		case http.StatusUnauthorized:
			return ErrUnauthorized
		case http.StatusRequestEntityTooLarge:
			return ErrTooLarge
		case http.StatusUnsupportedMediaType:
			return ErrUnsupportedType
		}
		var body struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body)
		return &APIError{Status: res.StatusCode, Message: body.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
