<?php

declare(strict_types=1);

namespace Filekeeper;

/**
 * Client for the filekeeper file service.
 *
 * A consuming service stores only the file id string (`f_…`) returned by
 * upload(). Serving URLs, private-file signatures and imgproxy variants are
 * computed locally — no HTTP round-trips.
 *
 * Requires ext-curl. No other dependencies.
 */
final readonly class Filekeeper
{
    private string $baseUrl;

    private ?string $imgproxyUrl;

    /**
     * @param string      $baseUrl        e.g. "http://filekeeper:8080"
     * @param string      $token          this service's token from FK_TOKENS
     * @param string|null $signSecret     FK_SIGN_SECRET, enables signedUrl()
     * @param string|null $imgproxyUrl    e.g. "http://imgproxy:8080", enables imageUrl()
     * @param string|null $imgproxyKey    IMGPROXY_KEY (hex)
     * @param string|null $imgproxySalt   IMGPROXY_SALT (hex)
     * @param string      $imgproxyBucket FK_S3_BUCKET filekeeper writes to
     */
    public function __construct(
        string $baseUrl,
        private string $token,
        #[\SensitiveParameter] private ?string $signSecret = null,
        ?string $imgproxyUrl = null,
        #[\SensitiveParameter] private ?string $imgproxyKey = null,
        #[\SensitiveParameter] private ?string $imgproxySalt = null,
        private string $imgproxyBucket = 'blobs',
    ) {
        $this->baseUrl = rtrim($baseUrl, '/');
        $this->imgproxyUrl = $imgproxyUrl !== null ? rtrim($imgproxyUrl, '/') : null;
    }

    /**
     * Upload a file from disk. Curl streams it — the file is never loaded
     * into PHP memory.
     *
     * @return array{id:string, sha256:string, size:int, mime:string,
     *               filename?:string, visibility:string, service:string,
     *               created_at:string, url:string}
     */
    public function upload(string $filePath, bool $private = false, ?string $filename = null): array
    {
        if (!is_readable($filePath)) {
            throw new FilekeeperException("file not readable: {$filePath}");
        }
        $fields = [
            'file' => new \CURLFile($filePath, posted_filename: $filename ?? basename($filePath)),
        ];
        if ($private) {
            // Field order matters: visibility must precede the file part,
            // fields after it are ignored by the streaming server.
            $fields = ['visibility' => 'private'] + $fields;
        }

        return $this->request('POST', '/v1/files', [
            CURLOPT_POSTFIELDS => $fields,
        ], 201) ?? throw new FilekeeperException('empty upload response');
    }

    /**
     * Upload in-memory content (spooled through a temp file for curl).
     *
     * @return array see upload()
     */
    public function uploadString(string $content, string $filename, bool $private = false): array
    {
        $tmp = tmpfile() ?: throw new FilekeeperException('cannot create temp file');
        try {
            fwrite($tmp, $content);
            $path = stream_get_meta_data($tmp)['uri'];

            return $this->upload($path, $private, $filename);
        } finally {
            fclose($tmp);
        }
    }

    /** @return array file record, see upload() (without url) */
    public function meta(string $id): array
    {
        return $this->request('GET', '/v1/files/' . rawurlencode($id), [], 200)
            ?? throw new FilekeeperException('empty meta response');
    }

    public function delete(string $id): void
    {
        $this->request('DELETE', '/v1/files/' . rawurlencode($id), [], 204);
    }

    /** Serving URL of a public file. */
    public function publicUrl(string $id): string
    {
        return $this->baseUrl . '/f/' . rawurlencode($id);
    }

    /**
     * Expiring URL for a private file. Pure local HMAC, no HTTP call.
     */
    public function signedUrl(string $id, int $ttlSeconds = 300): string
    {
        $secret = $this->signSecret
            ?? throw new FilekeeperException('signedUrl requires signSecret');
        $exp = time() + $ttlSeconds;
        $sig = hash_hmac('sha256', "{$id}:{$exp}", $secret);

        return $this->publicUrl($id) . "?exp={$exp}&sig={$sig}";
    }

    /**
     * Signed imgproxy URL for an image variant. $processing is the imgproxy
     * options path, e.g. "rs:fill:300:300/f:webp"; $sha256 comes from the
     * upload()/meta() response. Pure local computation.
     */
    public function imageUrl(string $sha256, string $processing): string
    {
        if ($this->imgproxyUrl === null || $this->imgproxyKey === null || $this->imgproxySalt === null) {
            throw new FilekeeperException('imageUrl requires imgproxy url/key/salt');
        }
        if (!preg_match('/^[0-9a-f]{64}$/', $sha256)) {
            throw new FilekeeperException("bad sha256: {$sha256}");
        }
        $key = hex2bin($this->imgproxyKey) ?: throw new FilekeeperException('imgproxy key is not hex');
        $salt = hex2bin($this->imgproxySalt) ?: throw new FilekeeperException('imgproxy salt is not hex');

        $path = sprintf(
            '/%s/plain/s3://%s/blobs/%s/%s/%s',
            $processing,
            $this->imgproxyBucket,
            substr($sha256, 0, 2),
            substr($sha256, 2, 2),
            $sha256,
        );
        $sig = rtrim(strtr(base64_encode(hash_hmac('sha256', $salt . $path, $key, true)), '+/', '-_'), '=');

        return "{$this->imgproxyUrl}/{$sig}{$path}";
    }

    /** @param array<int, mixed> $curlOpts */
    private function request(string $method, string $path, array $curlOpts, int $expect): ?array
    {
        $ch = curl_init($this->baseUrl . $path);
        if ($ch === false) {
            throw new FilekeeperException('curl_init failed');
        }
        curl_setopt_array($ch, $curlOpts + [
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_HTTPHEADER => ["X-Service-Token: {$this->token}"],
            CURLOPT_FOLLOWLOCATION => false,
        ]);

        $body = curl_exec($ch);
        if ($body === false) {
            throw new FilekeeperException('transport error: ' . curl_error($ch));
        }
        $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);

        if ($status !== $expect) {
            $decoded = json_decode((string) $body, true);
            $msg = is_array($decoded) ? ($decoded['error'] ?? 'unknown error') : 'unknown error';
            throw match ($status) {
                404 => new NotFoundException($msg, 404),
                401 => new UnauthorizedException($msg, 401),
                413 => new TooLargeException($msg, 413),
                415 => new UnsupportedTypeException($msg, 415),
                default => new FilekeeperException("HTTP {$status}: {$msg}", $status),
            };
        }
        if ($body === '' || $expect === 204) {
            return null;
        }
        $decoded = json_decode((string) $body, true, flags: JSON_THROW_ON_ERROR);

        return is_array($decoded) ? $decoded : null;
    }
}
