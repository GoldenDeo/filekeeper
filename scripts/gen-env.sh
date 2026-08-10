#!/bin/sh
# Generates a .env with fresh secrets for docker compose. Run once: make env
set -eu

if [ -f .env ]; then
  echo ".env already exists — refusing to overwrite" >&2
  exit 1
fi

cat > .env <<EOF
# --- filekeeper ---
FK_HOST_PORT=8090
FK_PUBLIC_BASE_URL=http://localhost:8090
FK_MAX_UPLOAD_BYTES=104857600
# Service tokens: service:token[:scope], comma-separated. Add one per microservice.
# Scope is full (default: upload+read+sign+delete) or upload (create only) —
# give client apps an upload-scoped token, so a leaked one can add but never
# read or destroy.
FK_TOKENS=demo:tok_$(openssl rand -hex 24),demo-app:tok_$(openssl rand -hex 24):upload
# Shared secret for signed private-file URLs (services holding it can sign locally).
FK_SIGN_SECRET=$(openssl rand -hex 32)
# Browser origins allowed to call the API (comma-separated; empty = no CORS at
# all). Only web apps need this — native ones are unaffected. A dev server must
# listen on a fixed port for its origin to be nameable here.
FK_CORS_ORIGINS=http://localhost:18080

# --- Garage S3 (also used by imgproxy) ---
S3_BUCKET=blobs
S3_ACCESS_KEY=GK$(openssl rand -hex 12)
S3_SECRET_KEY=$(openssl rand -hex 32)

# --- imgproxy URL signing ---
IMGPROXY_KEY=$(openssl rand -hex 32)
IMGPROXY_SALT=$(openssl rand -hex 32)
EOF

echo "wrote .env"
