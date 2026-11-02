<?php
declare(strict_types=1);
// Only native IO and time are replaced. The pinned parser/entry/adapter/public
// PHP source remains byte-for-byte unchanged, with all expected values from PHP.
if (PHP_VERSION_ID !== 80306 || PHP_INT_SIZE !== 8) throw new RuntimeException('64-bit PHP 8.3.6 required');
date_default_timezone_set('UTC');
$fixtureNow = 0; $fixtureQueue = []; $fixtureRequests = []; $fixtureWarnings = []; $fixtureMode = 'metadata';
$fixtureInternal = null; $fixtureForwarded = false;
function time(): int { return $GLOBALS['fixtureNow']; }
final class ZuiyouFixtureHandle {
    public array $options = []; public array $response = [];
    public function __construct(public string $url) {}
}
function curl_init(?string $url = null): ZuiyouFixtureHandle { return new ZuiyouFixtureHandle($url ?? ''); }
function curl_setopt_array(ZuiyouFixtureHandle $handle, array $options): bool {
    foreach ($options as $key => $value) curl_setopt($handle, $key, $value);
    return true;
}
function curl_setopt(ZuiyouFixtureHandle $handle, int $key, mixed $value): bool {
    $handle->options[$key] = $value;
    if ($key === CURLOPT_URL) $handle->url = $value;
    return true;
}
function curl_exec(ZuiyouFixtureHandle $handle): bool|string {
    global $fixtureMode, $fixtureQueue, $fixtureRequests, $fixtureInternal, $fixtureForwarded;
    $options = $handle->options;
    if ($fixtureMode === 'gateway') {
        $parts = parse_url($handle->url); parse_str($parts['query'] ?? '', $query);
        if (($parts['scheme'] ?? '') !== 'http' || ($parts['host'] ?? '') !== '127.0.0.1'
            || ($parts['port'] ?? 0) !== 9081 || ($parts['path'] ?? '') !== '/parse'
            || ($query['platform'] ?? '') !== 'zuiyou' || ($query['url'] ?? '') !== ($_GET['url'] ?? null)) {
            throw new RuntimeException('Unexpected internal gateway request');
        }
        $fixtureForwarded = true;
        $handle->response = ['status' => snapEnvelopeStatus($fixtureInternal), 'body' => json_encode($fixtureInternal, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR)];
    } else {
        $method = !empty($options[CURLOPT_POST]) ? 'POST' : (!empty($options[CURLOPT_NOBODY]) ? 'HEAD' : 'GET');
        $headers = [];
        foreach ($options[CURLOPT_HTTPHEADER] ?? [] as $line) {
            [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value);
        }
        if (isset($options[CURLOPT_USERAGENT])) $headers['user-agent'] = $options[CURLOPT_USERAGENT];
        $request = ['url' => $handle->url, 'method' => $method, 'headers' => (object)$headers,
            'body' => $options[CURLOPT_POSTFIELDS] ?? '', 'follow_redirects' => (bool)($options[CURLOPT_FOLLOWLOCATION] ?? false),
            'timeout_seconds' => $options[CURLOPT_TIMEOUT] ?? null];
        $fixtureRequests[] = $request;
        if (!$fixtureQueue) throw new RuntimeException('Unexpected metadata request');
        $wanted = array_shift($fixtureQueue);
        foreach (['url', 'method', 'follow_redirects'] as $key) {
            if ($request[$key] !== $wanted[$key]) throw new RuntimeException('Metadata request differs: ' . $key);
        }
        if (($options[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($options[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2
            || ($options[CURLOPT_CONNECTTIMEOUT] ?? null) !== 5 || ($options[CURLOPT_MAXREDIRS] ?? null) !== 5) {
            throw new RuntimeException('Missing original TLS/connect/redirect protection');
        }
        if ($request['timeout_seconds'] !== ($method === 'HEAD' ? 12 : 20)) throw new RuntimeException('Original timeout changed');
        $handle->response = $wanted;
    }
    if (isset($handle->response['error'])) return false;
    $body = $handle->response['body'];
    if (!isset($options[CURLOPT_WRITEFUNCTION])) throw new RuntimeException('Missing original bounded response writer');
    if (($options[CURLOPT_WRITEFUNCTION])($handle, $body) !== strlen($body)) return false;
    return true;
}
function curl_getinfo(ZuiyouFixtureHandle $handle, ?int $option = null): mixed {
    return match ($option) { CURLINFO_HTTP_CODE => $handle->response['status'] ?? 0,
        default => throw new RuntimeException('Unregistered curl_getinfo') };
}
function curl_close(ZuiyouFixtureHandle $handle): void {}
function curl_error(ZuiyouFixtureHandle $handle): string { return $handle->response['error'] ?? ''; }
function curl_errno(ZuiyouFixtureHandle $handle): int { return isset($handle->response['error']) ? CURLE_COULDNT_CONNECT : CURLE_OK; }
function get_headers(string $url, bool|int $associative = false, mixed $context = null): never { throw new RuntimeException('Unexpected get_headers'); }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Pinned PHP archive required');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once $root . '/backend/api/zuiyou.php';
require_once $root . '/backend/lib/gateway.php';
set_error_handler(static function (int $severity, string $message) use (&$fixtureWarnings): bool {
    if ($severity === E_USER_WARNING && $message === 'cURL error: Synthetic transport failure') {
        $fixtureWarnings[] = $message; return true;
    }
    throw new ErrorException($message, 0, $severity);
});
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] as $case) {
    $input = $case['input']; $fixtureNow = $input['now']; $fixtureMode = 'metadata';
    $fixtureQueue = $input['http_responses']; $fixtureRequests = []; $fixtureWarnings = [];
    try {
        $parser = parseZuiyou($input['url']);
        // Exactly the frozen entrypoint's numeric-check/pretty/unescaped flags.
        $wire = json_decode(json_encode($parser, 480 | JSON_THROW_ON_ERROR), true, 128, JSON_THROW_ON_ERROR);
        if ($fixtureQueue) throw new RuntimeException('Unused controlled metadata response');
        $fixtureInternal = snapAdaptParser('zuiyou', $wire, $input['url']);
        snapValidateEnvelope($fixtureInternal);
        $expected = ['parser_response' => $parser, 'legacy_response' => $wire,
            'http_requests' => $fixtureRequests, 'php_warnings' => $fixtureWarnings];
        foreach (['normal' => false, 'debug' => true] as $view => $debug) {
            $fixtureMode = 'gateway'; $fixtureForwarded = false;
            $_GET = ['schema_version' => '1.0', 'url' => $input['url']];
            if ($debug) $_GET['debug'] = '1';
            if ($input['route'] === 'resolve') $_GET['item_id'] = $input['item_id'];
            $_POST = []; $_SERVER = ['REQUEST_METHOD' => 'GET', 'REQUEST_URI' => '/api/' . $input['route']];
            putenv('SNAP_INTERNAL_ORIGIN=http://127.0.0.1:9081');
            putenv('SNAP_INTERNAL_HOST=snap.dokitool.com');
            http_response_code(200);
            ob_start(); snapHandle(); $body = ob_get_clean();
            $expected[$view] = json_decode($body, true, 128, JSON_THROW_ON_ERROR);
            $expected[$view . '_status'] = http_response_code();
            $expected[$view . '_forwarded'] = $fixtureForwarded;
            snapValidateEnvelope($expected[$view]);
        }
        $observed[] = ['id' => $case['id'], 'expected' => $expected];
    } catch (Throwable $error) {
        throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error);
    }
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
