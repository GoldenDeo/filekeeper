package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

// blobKey maps a content hash to its S3 object key. Content-addressed layout:
// identical uploads share one object, and objects are immutable by
// construction (safe for aggressive CDN caching).
func blobKey(sha string) string {
	return "blobs/" + sha[:2] + "/" + sha[2:4] + "/" + sha
}

func newFileID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "f_" + hex.EncodeToString(b)
}

type uploadResponse struct {
	meta.File
	URL string `json:"url"`
}

func handleUpload(cfg Config, metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// Hard cap on the whole request; the extra megabyte covers multipart
		// framing and small text fields.
		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadBytes+1<<20)

		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, http.StatusBadRequest, "expected multipart/form-data")
			return
		}

		visibility := "public"
		var filePart *multipart.Part
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				uploadReadError(w, err)
				return
			}
			switch part.FormName() {
			case "visibility":
				v, err := io.ReadAll(io.LimitReader(part, 32))
				if err != nil {
					uploadReadError(w, err)
					return
				}
				visibility = strings.TrimSpace(string(v))
			case "file":
				filePart = part
			}
			if filePart != nil {
				break // fields after the file are ignored; stream the file now
			}
		}
		if filePart == nil {
			writeError(w, http.StatusBadRequest, `missing "file" part`)
			return
		}
		if visibility != "public" && visibility != "private" {
			writeError(w, http.StatusBadRequest, `visibility must be "public" or "private"`)
			return
		}

		// Sniff real MIME from the first 512 bytes, then reattach them.
		head := make([]byte, 512)
		n, err := io.ReadFull(filePart, head)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			uploadReadError(w, err)
			return
		}
		head = head[:n]
		if n == 0 {
			writeError(w, http.StatusBadRequest, "empty file")
			return
		}
		mimeType := detectMIME(head)
		if !cfg.AllowedMIME[mimeType] {
			writeError(w, http.StatusUnsupportedMediaType,
				fmt.Sprintf("type %s is not allowed", mimeType))
			return
		}

		// Spool to disk while hashing: S3 needs a known length and a seekable
		// body, and the hash lets us dedup before touching storage at all.
		spool, err := os.CreateTemp(cfg.SpoolDir, "upload-*")
		if err != nil {
			logger.Error("spool create", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer os.Remove(spool.Name())
		defer spool.Close()

		hasher := sha256.New()
		size, err := io.Copy(io.MultiWriter(spool, hasher),
			io.MultiReader(strings.NewReader(string(head)), filePart))
		if err != nil {
			uploadReadError(w, err)
			return
		}
		if size > cfg.MaxUploadBytes {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file exceeds limit of %d bytes", cfg.MaxUploadBytes))
			return
		}
		sha := hex.EncodeToString(hasher.Sum(nil))
		key := blobKey(sha)

		// Dedup: skip the S3 write when this exact content is already stored.
		// The meta refcount alone is not enough — verify the object really
		// exists to heal from a lost GC race or manual bucket cleanup.
		refs, err := metaStore.CountBySHA(ctx, sha)
		if err != nil {
			logger.Error("count sha", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		stored := false
		if refs > 0 {
			if ok, err := blobs.Exists(ctx, key); err == nil && ok {
				stored = true
			}
		}
		if !stored {
			if _, err := spool.Seek(0, io.SeekStart); err != nil {
				logger.Error("spool seek", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if err := blobs.Put(ctx, key, spool, size, mimeType); err != nil {
				logger.Error("blob put", "key", key, "err", err)
				writeError(w, http.StatusBadGateway, "storage unavailable")
				return
			}
		}

		f := meta.File{
			ID:         newFileID(),
			SHA256:     sha,
			Size:       size,
			MIME:       mimeType,
			Filename:   sanitizeFilename(filePart.FileName()),
			Visibility: visibility,
			Service:    serviceFrom(ctx),
			CreatedAt:  time.Now().UTC(),
		}
		if err := metaStore.Insert(ctx, f); err != nil {
			logger.Error("meta insert", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusCreated, uploadResponse{
			File: f,
			URL:  cfg.PublicBaseURL + "/f/" + f.ID,
		})
	})
}

func uploadReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "malformed upload")
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	// Strip control characters and quotes that would need header escaping.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}
		return r
	}, name)
	if len(name) > 255 {
		name = name[len(name)-255:]
	}
	return name
}
