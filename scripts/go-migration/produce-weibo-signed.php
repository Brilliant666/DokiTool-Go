<?php
declare(strict_types=1);
// Development-only IO and clock substitutes; frozen PHP source is not edited.
if (PHP_VERSION_ID !== 80306) throw new RuntimeException('PHP 8.3.6 required');
date_default_timezone_set('UTC');
$fixtureNow = 0; $fixtureQueue = []; $fixtureRequests = []; $fixtureInternal = null; $fixtureMode = 'metadata';
function time(): int { return $GLOBALS['fixtureNow']; }
function fixtureURL(string $url): string { return preg_replace('/([?&]_rand=)[^&]*/', '$1<nonce>', $url); }
final class WeiboSignedFixtureHandle {
    public array $options = []; public array $response = []; public string $method = 'GET';
    public function __construct(public string $url) {}
}
function curl_init(?string $url = null): WeiboSignedFixtureHandle { return new WeiboSignedFixtureHandle($url ?? ''); }
function curl_setopt_array(WeiboSignedFixtureHandle $h, array $options): bool {
    foreach ($options as $key => $value) curl_setopt($h, $key, $value);
    return true;
}
function curl_setopt(WeiboSignedFixtureHandle $h, int $key, mixed $value): bool {
    $h->options[$key] = $value;
    if ($key === CURLOPT_URL) $h->url = $value;
    if ($key === CURLOPT_POST && $value) $h->method = 'POST';
    if ($key === CURLOPT_HTTPGET && $value) $h->method = 'GET';
    return true;
}
function curl_exec(WeiboSignedFixtureHandle $h): bool|string {
    global $fixtureMode, $fixtureQueue, $fixtureRequests, $fixtureInternal;
    if ($fixtureMode === 'gateway') {
        $parts = parse_url($h->url); parse_str($parts['query'] ?? '', $query);
        if (($parts['scheme'] ?? '') !== 'http' || ($parts['host'] ?? '') !== '127.0.0.1'
            || ($parts['port'] ?? 0) !== 9081 || ($parts['path'] ?? '') !== '/parse'
            || ($query['platform'] ?? '') !== 'weibo' || ($query['url'] ?? '') !== ($_GET['url'] ?? null)) {
            throw new RuntimeException('Unexpected internal gateway request');
        }
        $h->response = ['status' => snapEnvelopeStatus($fixtureInternal), 'body' => json_encode($fixtureInternal, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR)];
    } else {
        if (!$fixtureQueue) throw new RuntimeException('Unexpected metadata request: ' . $h->url);
        $wanted = array_shift($fixtureQueue);
        $post = $h->method === 'POST' ? ($h->options[CURLOPT_POSTFIELDS] ?? null) : null;
        if (fixtureURL($h->url) !== $wanted['url'] || $h->method !== $wanted['method'] || $post !== $wanted['post']) {
            throw new RuntimeException('Metadata request differs: ' . json_encode([$h->url, $h->method, $post, $wanted]));
        }
        $headers = [];
        foreach ($h->options[CURLOPT_HTTPHEADER] ?? [] as $line) {
            [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value);
        }
        $fixtureRequests[] = ['url' => fixtureURL($h->url), 'method' => $h->method, 'post' => $post, 'headers' => (object)$headers];
        if (($h->options[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($h->options[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2
            || ($h->options[CURLOPT_TIMEOUT] ?? 0) < 1 || ($h->options[CURLOPT_TIMEOUT] ?? 0) > 20) {
            throw new RuntimeException('Missing PHP TLS or deadline protection');
        }
        $h->response = $wanted;
    }
    if ($h->response['error'] ?? false) return false;
    $body = $h->response['body'];
    if (isset($h->options[CURLOPT_WRITEFUNCTION])) {
        if (($h->options[CURLOPT_WRITEFUNCTION])($h, $body) !== strlen($body)) return false;
        return true;
    }
    return $body;
}
function curl_getinfo(WeiboSignedFixtureHandle $h, ?int $option = null): mixed {
    return match ($option) {
        CURLINFO_HTTP_CODE => $h->response['status'] ?? 0,
        CURLINFO_EFFECTIVE_URL => $h->url,
        default => throw new RuntimeException('Unregistered curl_getinfo'),
    };
}
function curl_close(WeiboSignedFixtureHandle $h): void {}
function curl_error(WeiboSignedFixtureHandle $h): string { return ($h->response['error'] ?? false) ? 'Synthetic transport failure' : ''; }
function curl_errno(WeiboSignedFixtureHandle $h): int { return ($h->response['error'] ?? false) ? CURLE_COULDNT_CONNECT : CURLE_OK; }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Pinned PHP archive is required');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once $root . '/backend/api/weibo.php';
require_once $root . '/backend/lib/gateway.php';
set_error_handler(static function (int $level, string $message): never { throw new ErrorException($message, 0, $level); });
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] as $case) {
    $input = $case['input']; $fixtureNow = $input['now']; $fixtureMode = 'metadata';
    $fixtureQueue = $input['http_responses']; $fixtureRequests = [];
    $target = weiboTarget($input['url']);
    if ($target === null) throw new RuntimeException('Fixture must exercise a valid parser target');
    $legacy = weiboFetchTarget($target);
    if ($fixtureQueue) throw new RuntimeException('Unused PHP metadata response in ' . $case['id']);
    // Execute the original entry encoder, then the original internal adapter.
    ob_start(); outputSuccess($legacy); $wire = ob_get_clean();
    $decoded = json_decode($wire, true, 128, JSON_THROW_ON_ERROR);
    $fixtureInternal = snapAdaptParser('weibo', $decoded, $input['url']);
    snapValidateEnvelope($fixtureInternal);
    $expected = ['legacy_response' => $decoded, 'http_requests' => $fixtureRequests];
    foreach (['normal' => false, 'debug' => true] as $view => $debug) {
        $fixtureMode = 'gateway';
        $_GET = ['schema_version' => '1.0', 'url' => $input['url']];
        if ($debug) $_GET['debug'] = '1';
        if ($input['route'] === 'resolve') $_GET['item_id'] = $input['item_id'];
        $_POST = []; $_SERVER = ['REQUEST_METHOD' => 'GET', 'REQUEST_URI' => '/api/' . $input['route']];
        putenv('SNAP_INTERNAL_ORIGIN=http://127.0.0.1:9081');
        putenv('SNAP_INTERNAL_HOST=snap.dokitool.com');
        putenv('SNAP_MEDIA_SIGNING_KEY=' . $input['key']);
        http_response_code(200);
        ob_start(); snapHandle(); $response = ob_get_clean();
        $expected[$view] = json_decode($response, true, 128, JSON_THROW_ON_ERROR);
        $expected[$view . '_status'] = http_response_code();
        snapValidateEnvelope($expected[$view]);
    }
    $observed[] = ['id' => $case['id'], 'expected' => $expected];
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
