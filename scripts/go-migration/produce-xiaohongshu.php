<?php
declare(strict_types=1);
// Original frozen PHP parser and container reader run without native IO.
if (PHP_VERSION_ID !== 80306) throw new RuntimeException('PHP 8.3.6 required');
date_default_timezone_set('UTC');
$fixtureNow = 0; $httpQueue = []; $httpRequests = []; $violations = [];
function time(): int { return $GLOBALS['fixtureNow']; }
function microtime(bool $asFloat = false): float|string { return $asFloat ? 100.0 : '0.00000000 100'; }
function dns_get_record(string $hostname, int $type = DNS_ANY, ?array &$auth = null, ?array &$additional = null, bool $raw = false): array {
    return [['host' => $hostname, 'type' => 'A', 'ip' => '8.8.8.8']];
}
function xhsFixtureFail(string $message): never { $GLOBALS['violations'][] = $message; throw new RuntimeException($message); }
final class XhsFixtureHandle {
    public array $options = []; public array $response = []; public int $error = 0;
    public function __construct(public string $url) {}
}
function curl_init(?string $url = null): XhsFixtureHandle { return new XhsFixtureHandle($url ?? ''); }
function curl_setopt_array(XhsFixtureHandle $handle, array $options): bool {
    foreach ($options as $key => $value) { $handle->options[$key] = $value; if ($key === CURLOPT_URL) $handle->url = $value; }
    return true;
}
function curl_setopt(XhsFixtureHandle $h, int $key, mixed $value): bool { return curl_setopt_array($h, [$key => $value]); }
function curl_exec(XhsFixtureHandle $h): bool|string {
    global $httpQueue, $httpRequests;
    if (!$httpQueue) xhsFixtureFail('Unexpected request ' . $h->url);
    $step = array_shift($httpQueue); $o = $h->options;
    $range = $o[CURLOPT_RANGE] ?? '';
    if ($step['url'] !== $h->url || $step['method'] !== 'GET' || $step['range'] !== $range) xhsFixtureFail('Request/tape differs: ' . $h->url . ' Range ' . $range);
    if (($o[CURLOPT_FOLLOWLOCATION] ?? false) || ($o[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($o[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2) xhsFixtureFail('Redirect/TLS boundary lost');
    $headers = [];
    foreach ($o[CURLOPT_HTTPHEADER] ?? [] as $line) { [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value); }
    if (isset($o[CURLOPT_USERAGENT])) $headers['user-agent'] = $o[CURLOPT_USERAGENT];
    if ($range !== '') {
        $headers['range'] = 'bytes=' . $range;
        if (($o[CURLOPT_RESOLVE] ?? []) !== [parse_url($h->url, PHP_URL_HOST) . ':443:8.8.8.8']) xhsFixtureFail('Media socket was not pinned to reviewed DNS');
        if (($o[CURLOPT_PROXY] ?? null) !== '' || ($o[CURLOPT_NOPROXY] ?? null) !== '*') xhsFixtureFail('Media probe lost direct transport policy');
    }
    if (isset($o[CURLOPT_ENCODING])) $headers['accept-encoding'] = $o[CURLOPT_ENCODING];
    if (isset($o[CURLOPT_COOKIE]) && $o[CURLOPT_COOKIE] !== '') xhsFixtureFail('Unexpected cookie');
    $timeout = $o[CURLOPT_TIMEOUT_MS] ?? 0;
    if ($timeout < 1 || $timeout > ($range === '' ? 10000 : 6000)) xhsFixtureFail('Request time budget lost');
    $httpRequests[] = ['method' => 'GET', 'url' => $h->url, 'headers' => (object)$headers,
        'body' => '', 'follow_redirects' => false, 'timeout_ms' => $timeout, 'range' => $range];
    $h->response = $step; $h->error = $step['error'];
    if ($h->error) return false;
    $callback = $o[CURLOPT_HEADERFUNCTION] ?? null;
    if (!$callback) xhsFixtureFail('Original header callback missing');
    $lines = ['HTTP/1.1 ' . $step['status'] . " Synthetic\r\n"];
    foreach ($step['headers'] as $name => $values) foreach ((array)$values as $value) $lines[] = $name . ': ' . $value . "\r\n";
    $lines[] = "\r\n";
    foreach ($lines as $line) if ($callback($h, $line) !== strlen($line)) { $h->error = CURLE_WRITE_ERROR; return false; }
    $body = base64_decode($step['body_b64'], true);
    if ($body === false || !isset($o[CURLOPT_WRITEFUNCTION])) xhsFixtureFail('Invalid controlled body or missing bounded writer');
    if ($o[CURLOPT_WRITEFUNCTION]($h, $body) !== strlen($body)) { $h->error = CURLE_WRITE_ERROR; return false; }
    return true;
}
function curl_getinfo(XhsFixtureHandle $h, ?int $option = null): mixed {
    return match ($option) { CURLINFO_HTTP_CODE => $h->response['status'] ?? 0, CURLINFO_EFFECTIVE_URL => $h->url,
        default => xhsFixtureFail('Unregistered curl_getinfo option') };
}
function curl_close(XhsFixtureHandle $h): void {}
function curl_errno(XhsFixtureHandle $h): int { return $h->error; }
function curl_error(XhsFixtureHandle $h): string { return $h->error ? 'Synthetic transfer failure' : ''; }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Pinned PHP archive required');
require_once $root . '/backend/api/xiaohongshu/XiaohongshuParser.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $level, string $message): never { throw new ErrorException($message, 0, $level); });
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] as $case) {
    $input = $case['input']; $fixtureNow = $input['now']; $httpQueue = $input['http_responses']; $httpRequests = []; $violations = [];
    try {
        $parser = new XiaohongshuParser(); $parser->setCookie('');
        $wire = $parser->parse($input['url']);
        $legacy = json_decode($wire, true, 128, JSON_THROW_ON_ERROR);
        if ($httpQueue || $violations) throw new RuntimeException('Unconsumed/violated HTTP tape');
        $full = snapAdaptParser('xiaohongshu', $legacy, $input['url']);
        $normal = snapPublicMediaView($full, false, $input['now']);
        $debug = snapPublicMediaView($full, true, $input['now']);
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => $case['id'], 'expected' => ['legacy_response' => $legacy,
            'normal' => $normal, 'debug' => $debug, 'http_requests' => $httpRequests]];
    } catch (Throwable $error) { throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error); }
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
