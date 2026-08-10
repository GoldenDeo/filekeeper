package server

import (
	"log/slog"
	"net/http"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

// addRoutes is the single place the whole API surface is visible.
func addRoutes(mux *http.ServeMux, cfg Config, metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) {
	auth := withAuth(cfg.Tokens)

	// Service API (authenticated with X-Service-Token).
	mux.Handle("POST /v1/files", auth(handleUpload(cfg, metaStore, blobs, logger)))
	mux.Handle("GET /v1/files/{id}", auth(handleGetMeta(metaStore)))
	mux.Handle("DELETE /v1/files/{id}", auth(handleDelete(metaStore, blobs, logger)))
	mux.Handle("POST /v1/files/{id}/sign", auth(handleSign(cfg, metaStore)))

	// Public serving (access to private files via signed URLs).
	mux.Handle("GET /f/{id}", handleServe(cfg, metaStore, blobs, logger))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}
