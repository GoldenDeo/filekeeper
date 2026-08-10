// Package server is the HTTP transport of filekeeper.
//
// Layout follows Mat Ryer's "how I write HTTP services": New returns a plain
// http.Handler, routes.go is the single map of the API, handlers are maker
// functions that close over exactly the dependencies they need.
package server

import (
	"log/slog"
	"net/http"

	"github.com/GoldenDeo/filekeeper/internal/blob"
	"github.com/GoldenDeo/filekeeper/internal/meta"
)

type Config struct {
	PublicBaseURL  string
	SpoolDir       string // temp dir for upload spooling, same volume as DataDir
	MaxUploadBytes int64
	AllowedMIME    map[string]bool
	Tokens         map[string]Token // token string -> service + scope
	SignSecret     []byte
	CORSOrigins    []string // browser origins allowed to call the API; empty = no CORS
}

func New(cfg Config, metaStore *meta.Store, blobs blob.Store, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	addRoutes(mux, cfg, metaStore, blobs, logger)

	var h http.Handler = mux
	h = withCORS(cfg.CORSOrigins, h)
	h = withRecover(logger, h)
	h = withLogging(logger, h)
	return h
}
