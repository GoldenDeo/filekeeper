package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SignedURL builds an expiring URL for a private file. Pure local HMAC — no
// network call. Requires WithSignSecret.
func (c *Client) SignedURL(id string, ttl time.Duration) (string, error) {
	if len(c.signSecret) == 0 {
		return "", errors.New("filekeeper: SignedURL requires WithSignSecret")
	}
	exp := time.Now().Add(ttl).Unix()
	mac := hmac.New(sha256.New, c.signSecret)
	fmt.Fprintf(mac, "%s:%d", id, exp)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s/f/%s?exp=%d&sig=%s", c.baseURL, id, exp, sig), nil
}

// ImageURL builds a signed imgproxy URL for an image variant. processing is
// the imgproxy options path, e.g. "rs:fill:300:300/f:webp". sha256sum comes
// from File.SHA256. Pure local computation — no network call. Requires
// WithImgproxy.
//
// Note: imgproxy reads the blob straight from S3, so this works only for
// content imgproxy can decode (images, animations).
func (c *Client) ImageURL(sha256sum, processing string) (string, error) {
	if c.imgproxyErr != nil {
		return "", c.imgproxyErr
	}
	if c.imgproxyURL == "" {
		return "", errors.New("filekeeper: ImageURL requires WithImgproxy")
	}
	if len(sha256sum) != 64 {
		return "", fmt.Errorf("filekeeper: bad sha256 %q", sha256sum)
	}
	path := fmt.Sprintf("/%s/plain/s3://%s/blobs/%s/%s/%s",
		processing, c.imgproxyBucket, sha256sum[:2], sha256sum[2:4], sha256sum)
	mac := hmac.New(sha256.New, c.imgproxyKey)
	mac.Write(c.imgproxySalt)
	mac.Write([]byte(path))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return c.imgproxyURL + "/" + sig + path, nil
}
