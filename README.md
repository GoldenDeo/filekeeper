# filekeeper

Centralized file service for a microservice fleet. Services upload a file once,
store a single string (`f_…` id) in their own database, and never think about
storage again. filekeeper owns the bytes, dedup, access control and serving;
[Garage](https://garagehq.deuxfleurs.fr/) (S3-compatible) owns persistence;
[imgproxy](https://imgproxy.net/) does on-the-fly image transforms.

```
browser / service ──POST /v1/files──▶ filekeeper ──S3──▶ Garage
        ▲                               │ SQLite (metadata)
        └──────GET /f/{id}──────────────┘
        └──────GET :8081/{sig}/rs:fill:300:300/...──▶ imgproxy ──S3──▶ Garage
```

## Design in one paragraph

Uploads are streamed (never buffered in RAM), sniffed by magic bytes (client
Content-Type is ignored), hashed with SHA-256 on the fly and stored
**content-addressed** (`blobs/ab/cd/<sha256>`): identical files are stored once
and blobs are immutable, so public files are served with
`Cache-Control: immutable`. A file record ("attachment") in SQLite references a
blob; deleting the last record garbage-collects the blob. Private files are
served only with an expiring HMAC signature that any trusted service can
compute locally. Storage is behind a 4-method interface — Garage today, any
S3 or another backend tomorrow, without touching handlers.

## Quick start

```sh
make env    # generates .env with fresh secrets (once)
make up     # builds and starts filekeeper + garage + imgproxy
```

Smoke test:

```sh
TOKEN=<token from FK_TOKENS in .env>
curl -X POST http://localhost:8090/v1/files \
     -H "X-Service-Token: $TOKEN" -F file=@picture.png
# → {"id":"f_…","sha256":"…","mime":"image/png","url":"http://localhost:8090/f/f_…"}
curl http://localhost:8090/f/f_…          # serves the file
```

## API

All `/v1/*` endpoints require `X-Service-Token: <token>` (one token per
consuming service, configured in `FK_TOKENS`). `/f/{id}` is public.

| Method & path             | What it does |
|---------------------------|--------------|
| `POST /v1/files`          | multipart upload; parts: `file` (required), `visibility` = `public` (default) \| `private`. Returns `201` with the file record + `url`. `413` over limit, `415` for disallowed type. |
| `GET /f/{id}`             | serve the file. Supports `Range`, `If-None-Match`/`ETag` (=sha256), correct `Content-Type`, `nosniff`, inline for media / attachment for the rest. Private files need `?exp=&sig=`. |
| `GET /v1/files/{id}`      | metadata JSON. |
| `POST /v1/files/{id}/sign?ttl=300` | mint a signed URL for a private file (ttl ≤ 86400 s). |
| `DELETE /v1/files/{id}`   | remove the record; blob is deleted when the last record referencing it is gone. `204`. |
| `GET /healthz`            | liveness. |

### Integrating a microservice

1. Get a token: add `myservice:tok_…` to `FK_TOKENS`.
2. On upload, `POST /v1/files`, store **only** `id` from the response.
3. To show the file, render `<base>/f/<id>` (public) or sign it (private).
4. On entity deletion, `DELETE /v1/files/<id>`.

### Signed URLs for private files (no HTTP call needed)

Any service holding `FK_SIGN_SECRET` can sign locally:

```
exp = unix_now + ttl
sig = hex(hmac_sha256(FK_SIGN_SECRET, "<id>:<exp>"))
url = <base>/f/<id>?exp=<exp>&sig=<sig>
```

### Image variants (imgproxy)

Originals stay untouched; thumbnails/format conversion happen on request.
Source URL for a file with hash `<sha>` is
`s3://<bucket>/blobs/<sha[0:2]>/<sha[2:4]>/<sha>` (take `sha256` from the
upload response or `GET /v1/files/{id}`). Build a signed imgproxy URL
(`IMGPROXY_KEY`/`IMGPROXY_SALT`) per [imgproxy docs](https://docs.imgproxy.net/usage/signing_url), e.g.:

```
http://localhost:8081/{signature}/rs:fill:300:300/f:webp/plain/s3://blobs/blobs/ab/cd/<sha>
```

In production put a CDN or nginx cache in front of imgproxy and serve files
from a separate ("sandbox") domain, not from your app's origin.

## Configuration (env, all `FK_*`)

| Var | Default | Meaning |
|-----|---------|---------|
| `FK_ADDR` | `:8080` | listen address |
| `FK_PUBLIC_BASE_URL` | `http://localhost:8080` | external base for returned URLs |
| `FK_DATA_DIR` | `./data` | SQLite db + upload spool |
| `FK_MAX_UPLOAD_BYTES` | `104857600` (100 MB) | per-file limit |
| `FK_ALLOWED_MIME` | images + mp4/webm | comma-separated sniffed-MIME allowlist |
| `FK_TOKENS` | — (required) | `service:token,service2:token2` |
| `FK_SIGN_SECRET` | — (required, ≥32 chars) | HMAC secret for private-file URLs |
| `FK_S3_ENDPOINT/REGION/BUCKET/ACCESS_KEY/SECRET_KEY/PATH_STYLE` | Garage defaults | any S3-compatible target |

## Operational notes

- **Timeouts**: the server deliberately has no `ReadTimeout` (it would kill
  slow uploads); slowloris is bounded by `ReadHeaderTimeout`, body size by
  `MaxBytesReader`. If you front this with nginx/ingress, raise
  `client_max_body_size` / `proxy-body-size` accordingly.
- **Shutdown**: SIGTERM waits up to 30 s for in-flight uploads — give the
  container at least that much grace period.
- **Backups**: everything is two things — the `fk-data` volume (SQLite) and
  Garage's volumes. Content-addressing makes blob storage trivially rsyncable.
- **Garage secrets**: `garage/garage.toml` ships a dev `rpc_secret`; change it
  for production. Bucket + key are auto-created on first start
  (`--single-node --default-bucket`, Garage ≥ 2.3.0).
- **SVG is rejected** by default (stored-XSS vector). If you ever need it,
  sanitize server-side before allowlisting.

## Extension path (deliberately cheap)

- **Resumable / >100 MB uploads**: add a [tusd](https://github.com/tus/tusd)
  handler as a second front-end over the same storage; the domain layer
  (records, dedup, signing) does not change.
- **Cloud migration**: point `FK_S3_*` at AWS/R2/GCS-interop, `rclone sync`
  the bucket, done.
- **Processing pipeline** (AV scan, transcode): hook after insert; records
  already carry sha256 identity for idempotent workers.

## Development

```sh
make test   # unit + e2e over in-memory blob store
make vet
make build  # bin/filekeeper
```
