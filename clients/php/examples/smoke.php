<?php

declare(strict_types=1);

/**
 * Live smoke test of the PHP client against a running filekeeper stack.
 *
 * Env: FILEKEEPER_URL, FILEKEEPER_TOKEN, FILEKEEPER_SIGN_SECRET,
 *      IMGPROXY_URL, IMGPROXY_KEY, IMGPROXY_SALT (optional trio),
 *      FILEKEEPER_UPLOAD_TOKEN (optional, upload-scoped)
 */

require __DIR__ . '/../src/FilekeeperException.php';
require __DIR__ . '/../src/Filekeeper.php';

use Filekeeper\Filekeeper;
use Filekeeper\FilekeeperException;
use Filekeeper\NotFoundException;
use Filekeeper\UnsupportedTypeException;

function check(bool $ok, string $what): void
{
    if (!$ok) {
        fwrite(STDERR, "FAIL: {$what}\n");
        exit(1);
    }
    echo "ok: {$what}\n";
}

function fetch(string $url): array
{
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true]);
    $body = curl_exec($ch);
    $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);

    return [$status, (string) $body];
}

$baseUrl = getenv('FILEKEEPER_URL') ?: 'http://filekeeper:8080';

$fk = new Filekeeper(
    baseUrl: $baseUrl,
    token: getenv('FILEKEEPER_TOKEN') ?: '',
    signSecret: getenv('FILEKEEPER_SIGN_SECRET') ?: null,
    imgproxyUrl: getenv('IMGPROXY_URL') ?: null,
    imgproxyKey: getenv('IMGPROXY_KEY') ?: null,
    imgproxySalt: getenv('IMGPROXY_SALT') ?: null,
);

// 1x1 transparent PNG
$png = base64_decode(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGMAAQAABQAB'
    . 'DQottAAAAABJRU5ErkJggg=='
);

// upload public
$pub = $fk->uploadString($png, 'smoke.png');
check(($pub['mime'] ?? '') === 'image/png', 'upload public: mime sniffed as png');
check(($pub['size'] ?? 0) === strlen($png), 'upload public: size matches');

// serve public
[$status, $body] = fetch($fk->publicUrl($pub['id']));
check($status === 200 && $body === $png, 'public serve: bytes match');

// meta
$meta = $fk->meta($pub['id']);
check(($meta['sha256'] ?? '') === $pub['sha256'], 'meta: sha256 matches');

// upload private
$priv = $fk->uploadString($png, 'secret.png', private: true);
check(($priv['visibility'] ?? '') === 'private', 'upload private: visibility set');

[$status] = fetch($fk->publicUrl($priv['id']));
check($status === 403, 'private serve without signature: 403');

[$status, $body] = fetch($fk->signedUrl($priv['id'], 60));
check($status === 200 && $body === $png, 'private serve with local signature: 200');

// dedup: same content, same sha
check($pub['sha256'] === $priv['sha256'], 'dedup: identical content shares sha256');

// imgproxy variant (only when configured)
if (getenv('IMGPROXY_URL')) {
    [$status, $body] = fetch($fk->imageUrl($pub['sha256'], 'rs:fill:8:8/f:webp'));
    check($status === 200 && str_starts_with($body, 'RIFF'), 'imgproxy: webp variant served');
}

// upload-scoped token: the credential a client app ships with — may add files,
// must not be able to read or destroy anything
if ($appToken = getenv('FILEKEEPER_UPLOAD_TOKEN')) {
    $app = new Filekeeper(baseUrl: $baseUrl, token: $appToken);

    $fromApp = $app->uploadString($png, 'from-app.png');
    check(($fromApp['id'] ?? '') !== '', 'upload scope: upload allowed');
    check(($fromApp['service'] ?? '') === 'demo', 'upload scope: attributed to the shared tenant');

    foreach (['delete' => fn() => $app->delete($fromApp['id']),
              'meta' => fn() => $app->meta($fromApp['id'])] as $what => $call) {
        try {
            $call();
            check(false, "upload scope must not {$what}");
        } catch (FilekeeperException $e) {
            check($e->status === 403, "upload scope: {$what} refused with 403");
        }
    }

    // The UC-44 shape: the backend's full token deletes what the app uploaded,
    // which only works because both credentials name the same service.
    $fk->delete($fromApp['id']);
    check(true, 'upload scope: backend deletes its app\'s upload');
}

// rejected type
try {
    $fk->uploadString('<html><script>alert(1)</script></html>', 'evil.html');
    check(false, 'html upload must be rejected');
} catch (UnsupportedTypeException) {
    check(true, 'html upload rejected with typed exception');
}

// delete both; second delete → NotFoundException
$fk->delete($pub['id']);
$fk->delete($priv['id']);
try {
    $fk->meta($pub['id']);
    check(false, 'meta after delete must throw');
} catch (NotFoundException) {
    check(true, 'meta after delete throws NotFoundException');
}

echo "\nALL SMOKE TESTS PASSED\n";
