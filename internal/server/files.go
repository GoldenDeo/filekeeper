package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

func handleGetMeta(metaStore *meta.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := metaStore.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, meta.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, f)
	})
}

func handleDelete(metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		f, err := metaStore.Delete(ctx, id, serviceFrom(ctx))
		if errors.Is(err, meta.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			logger.Error("meta delete", "id", id, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Blob GC: drop the object once the last reference is gone. A failed
		// blob delete leaves a harmless orphan (space, not correctness) — the
		// record is already gone, so respond 204 either way.
		refs, err := metaStore.CountBySHA(ctx, f.SHA256)
		if err == nil && refs == 0 {
			if err := blobs.Delete(ctx, blobKey(f.SHA256)); err != nil {
				logger.Warn("orphaned blob: delete failed", "sha", f.SHA256, "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func handleSign(cfg Config, metaStore *meta.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := metaStore.Get(r.Context(), id); err != nil {
			if errors.Is(err, meta.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		ttl := int64(300)
		if v := r.URL.Query().Get("ttl"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 || n > 86400 {
				writeError(w, http.StatusBadRequest, "ttl must be 1..86400 seconds")
				return
			}
			ttl = n
		}
		exp := time.Now().Unix() + ttl
		sig := fileSignature(cfg.SignSecret, id, exp)
		writeJSON(w, http.StatusOK, map[string]any{
			"url":        cfg.PublicBaseURL + "/f/" + id + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig,
			"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339),
		})
	})
}
