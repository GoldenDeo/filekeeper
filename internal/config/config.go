// Package config loads filekeeper settings from environment variables.
// All variables are prefixed FK_; see .env.example for the full list.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/GoldenDeo/filekeeper/internal/blob"
)

// Token is one parsed FK_TOKENS entry. Scope is "full" or "upload"; the
// server package turns it into its own enum.
type Token struct {
	Service string
	Scope   string
}

type Config struct {
	Addr          string
	PublicBaseURL string // external URL files are served from, no trailing slash
	DataDir       string // sqlite db + upload spool live here

	MaxUploadBytes int64
	AllowedMIME    map[string]bool

	// Tokens maps a bearer token to the service it speaks for and its scope.
	Tokens      map[string]Token
	SignSecret  []byte
	CORSOrigins []string

	S3 blob.S3Config
}

func Load() (Config, error) {
	cfg := Config{
		Addr:           env("FK_ADDR", ":8080"),
		PublicBaseURL:  strings.TrimSuffix(env("FK_PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DataDir:        env("FK_DATA_DIR", "./data"),
		MaxUploadBytes: envInt64("FK_MAX_UPLOAD_BYTES", 100<<20),
		AllowedMIME:    map[string]bool{},
		Tokens:         map[string]Token{},
		S3: blob.S3Config{
			Endpoint:  os.Getenv("FK_S3_ENDPOINT"),
			Region:    env("FK_S3_REGION", "garage"),
			Bucket:    env("FK_S3_BUCKET", "blobs"),
			AccessKey: os.Getenv("FK_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("FK_S3_SECRET_KEY"),
			PathStyle: envBool("FK_S3_PATH_STYLE", true),
		},
	}

	mimes := env("FK_ALLOWED_MIME",
		"image/jpeg,image/png,image/webp,image/gif,image/avif,video/mp4,video/webm")
	for _, m := range strings.Split(mimes, ",") {
		if m = strings.TrimSpace(m); m != "" {
			cfg.AllowedMIME[m] = true
		}
	}

	// FK_TOKENS="billing:tok_abc,gallery-app:tok_def:upload"
	// Scope defaults to full when omitted.
	for _, entry := range strings.Split(os.Getenv("FK_TOKENS"), ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
			return cfg, fmt.Errorf("FK_TOKENS: bad entry %q, want service:token[:scope]", entry)
		}
		scope := "full"
		if len(parts) == 3 {
			scope = parts[2]
		}
		if scope != "full" && scope != "upload" {
			return cfg, fmt.Errorf("FK_TOKENS: unknown scope %q in %q, want full or upload", scope, entry)
		}
		cfg.Tokens[parts[1]] = Token{Service: parts[0], Scope: scope}
	}
	if len(cfg.Tokens) == 0 {
		return cfg, fmt.Errorf("FK_TOKENS is required (format: service:token,service2:token2)")
	}

	// FK_CORS_ORIGINS="http://localhost:18080,https://app.example.com" ("*" allowed).
	for _, o := range strings.Split(os.Getenv("FK_CORS_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, strings.TrimSuffix(o, "/"))
		}
	}

	secret := os.Getenv("FK_SIGN_SECRET")
	if len(secret) < 32 {
		return cfg, fmt.Errorf("FK_SIGN_SECRET must be at least 32 characters")
	}
	cfg.SignSecret = []byte(secret)

	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return cfg, fmt.Errorf("FK_S3_ACCESS_KEY and FK_S3_SECRET_KEY are required")
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
