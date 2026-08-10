package server

import (
	"log/slog"
	"net/http"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

// addRoutes is the single place the whole API surface is visible.
func addRoutes(mux *http.ServeMux, cfg Config, metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) {
	// Upload is reachable by upload-scoped tokens too (client apps); every
	// endpoint that reads or destroys demands a full token.
	canUpload := withAuth(cfg.Tokens, ScopeUpload)
	canManage := withAuth(cfg.Tokens, ScopeFull)

	// Service API (authenticated with X-Service-Token).
	mux.Handle("POST /v1/files", canUpload(handleUpload(cfg, metaStore, blobs, logger)))
	mux.Handle("GET /v1/files/{id}", canManage(handleGetMeta(metaStore)))
	mux.Handle("DELETE /v1/files/{id}", canManage(handleDelete(metaStore, blobs, logger)))
	mux.Handle("POST /v1/files/{id}/sign", canManage(handleSign(cfg, metaStore)))

	// Public serving (access to private files via signed URLs).
	mux.Handle("GET /f/{id}", handleServe(cfg, metaStore, blobs, logger))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}
