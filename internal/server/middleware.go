package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type ctxKey int

const ctxService ctxKey = iota

// Scope bounds what a token may do. A token that ships inside a client app is
// reachable by anyone holding the binary, so it must not be able to read or
// destroy anything — only add.
type Scope string

const (
	// ScopeFull may upload, read metadata, sign and delete: for backends.
	ScopeFull Scope = "full"
	// ScopeUpload may only create files.
	ScopeUpload Scope = "upload"
)

// allows reports whether a token carrying this scope may reach an endpoint
// requiring `required`.
func (s Scope) allows(required Scope) bool {
	return s == ScopeFull || s == required
}

// Token is a configured credential: which service it speaks for, and how far
// it reaches.
type Token struct {
	Service string
	Scope   Scope
}

// serviceFrom returns the authenticated service name set by withAuth.
func serviceFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxService).(string)
	return s
}

// withAuth authenticates X-Service-Token and enforces that its scope reaches
// the endpoint it guards.
func withAuth(tokens map[string]Token, required Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-Service-Token")
			if got == "" {
				writeError(w, http.StatusUnauthorized, "missing X-Service-Token")
				return
			}
			for token, t := range tokens {
				if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
					if !t.Scope.allows(required) {
						writeError(w, http.StatusForbidden,
							"token scope "+string(t.Scope)+" may not use this endpoint")
						return
					}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxService, t.Service)))
					return
				}
			}
			writeError(w, http.StatusUnauthorized, "invalid X-Service-Token")
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"service", serviceFrom(r.Context()),
		)
	})
}

func withRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				logger.Error("panic", "path", r.URL.Path, "panic", p)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
