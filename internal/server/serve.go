package server

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

func handleServe(cfg Config, metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		f, err := metaStore.Get(ctx, id)
		if errors.Is(err, meta.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			logger.Error("meta get", "id", id, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if f.Visibility == "private" {
			q := r.URL.Query()
			if !verifySignature(cfg.SignSecret, id, q.Get("exp"), q.Get("sig"), time.Now().Unix()) {
				writeError(w, http.StatusForbidden, "missing or invalid signature")
				return
			}
		}

		etag := `"` + f.SHA256 + `"`
		// A file record never changes its content, so ETag matching is exact.
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}

		obj, err := blobs.Get(ctx, blobKey(f.SHA256), r.Header.Get("Range"))
		if err != nil && r.Header.Get("Range") != "" && !errors.Is(err, blob.ErrNotFound) {
			// Bad or unsatisfiable range — fall back to the full object rather
			// than guessing backend-specific range errors.
			obj, err = blobs.Get(ctx, blobKey(f.SHA256), "")
		}
		if err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				logger.Error("blob missing for existing record", "id", id, "sha", f.SHA256)
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			logger.Error("blob get", "id", id, "err", err)
			writeError(w, http.StatusBadGateway, "storage unavailable")
			return
		}
		defer obj.Body.Close()

		h := w.Header()
		h.Set("Content-Type", f.MIME)
		h.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		h.Set("ETag", etag)
		h.Set("Accept-Ranges", "bytes")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Disposition", contentDisposition(f))
		if f.Visibility == "public" {
			// Content behind an id is immutable; cache hard.
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "private, no-store")
		}

		if obj.ContentRange != "" {
			h.Set("Content-Range", obj.ContentRange)
			w.WriteHeader(http.StatusPartialContent)
		}
		if _, err := io.Copy(w, obj.Body); err != nil {
			// Client went away mid-transfer; nothing useful to send.
			logger.Debug("serve copy aborted", "id", id, "err", err)
		}
	})
}

// contentDisposition renders media inline and forces everything else to
// download — uploaded content must never execute in the files origin.
func contentDisposition(f meta.File) string {
	disp := "attachment"
	if strings.HasPrefix(f.MIME, "image/") || strings.HasPrefix(f.MIME, "video/") ||
		strings.HasPrefix(f.MIME, "audio/") {
		disp = "inline"
	}
	if f.Filename == "" {
		return disp
	}
	return mime.FormatMediaType(disp, map[string]string{"filename": f.Filename})
}
