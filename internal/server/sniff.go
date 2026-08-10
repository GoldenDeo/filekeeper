package server

import (
	"bytes"
	"net/http"
	"strings"
)

// detectMIME determines the real content type from the first bytes of the
// file. The client-declared Content-Type is never trusted (OWASP file upload
// guidance): extension spoofing and SVG/HTML-in-image uploads are classic
// stored-XSS vectors.
func detectMIME(head []byte) string {
	// AVIF/HEIF live in an ISO-BMFF "ftyp" box that stdlib sniffing does not
	// know; without this they fall through to application/octet-stream.
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		switch string(head[8:12]) {
		case "avif", "avis":
			return "image/avif"
		}
	}
	mime := http.DetectContentType(head)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return mime
}
