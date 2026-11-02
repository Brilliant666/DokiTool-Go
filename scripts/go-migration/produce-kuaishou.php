<?php
declare(strict_types=1);
// Independent development oracle: unmodified PHP parser and runtime execute.
// Only native cURL IO is substituted; no Go code or expected response is read.
if (PHP_VERSION_ID !== 80306) throw new RuntimeException('Expected PHP 8.3.6');
date_default_timezone_set('UTC');
final class KuaishouFixtureHandle
{
    public array $options = [];
    public array $response = [];
    public string $error = '';
    public function __construct(public string $url) {}
}
$httpQueue = []; $httpRequests = []; $violations = [];
function kuaishouFixtureFail(string $message): never
{
    global $violations;
    $violations[] = $message;
    throw new RuntimeException($message);
}
function curl_init(?string $url = null): KuaishouFixtureHandle { return new KuaishouFixtureHandle($url ?? ''); }
function curl_setopt_array(KuaishouFixtureHandle $handle, array $options): bool
{
    foreach ($options as $key => $value) {
        $handle->options[$key] = $value;
        if ($key === CURLOPT_URL) $handle->url = $value;
    }
    return true;
}
function curl_setopt(KuaishouFixtureHandle $handle, int $option, mixed $value): bool { return curl_setopt_array($handle, [$option => $value]); }
function curl_exec(KuaishouFixtureHandle $handle): string|bool
{
    global $httpQueue, $httpRequests;
    $options = $handle->options; $headers = [];
    foreach ($options[CURLOPT_HTTPHEADER] ?? [] as $line) {
        [$name, $value] = explode(':', $line, 2);
        $headers[strtolower(trim($name))] = trim($value);
    }
    if (isset($options[CURLOPT_USERAGENT])) $headers['user-agent'] = $options[CURLOPT_USERAGENT];
    $method = !empty($options[CURLOPT_NOBODY]) ? 'HEAD' : 'GET';
    $follow = (bool)($options[CURLOPT_FOLLOWLOCATION] ?? false);
    $httpRequests[] = ['method' => $method, 'url' => $handle->url, 'headers' => (object)$headers,
        'body' => '', 'follow_redirects' => $follow, 'auto_referer' => (bool)($options[CURLOPT_AUTOREFERER] ?? false),
        'timeout_seconds' => $options[CURLOPT_TIMEOUT] ?? null, 'max_redirects' => $options[CURLOPT_MAXREDIRS] ?? null];
    if (!$httpQueue) kuaishouFixtureFail('Unregistered request: ' . $method . ' ' . $handle->url);
    $entry = array_shift($httpQueue);
    if ($entry['method'] !== $method || $entry['url'] !== $handle->url || $entry['follow_redirects'] !== $follow) kuaishouFixtureFail('Unexpected request: ' . $method . ' ' . $handle->url);
    if (($options[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($options[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2) kuaishouFixtureFail('TLS checks missing');
    if (($options[CURLOPT_TIMEOUT] ?? 0) !== 20 || ($options[CURLOPT_CONNECTTIMEOUT] ?? 0) !== 5 || ($options[CURLOPT_MAXREDIRS] ?? 0) !== 5) kuaishouFixtureFail('Bounded request policy missing');
    $handle->response = $entry;
    if (isset($entry['error'])) { $handle->error = $entry['error']; return false; }
    $body = $method === 'HEAD' ? '' : $entry['body'];
    if (!isset($options[CURLOPT_WRITEFUNCTION])) kuaishouFixtureFail('Original bounded response writer missing');
    if ($options[CURLOPT_WRITEFUNCTION]($handle, $body) !== strlen($body)) {
        $handle->error = 'Synthetic response exceeded writer limit'; return false;
    }
    return true;
}
function curl_getinfo(KuaishouFixtureHandle $handle, ?int $option = null): mixed
{
    return match ($option) {
        CURLINFO_EFFECTIVE_URL => $handle->response['final_url'] ?? $handle->url,
        CURLINFO_HTTP_CODE => $handle->response['status'] ?? 0,
        default => kuaishouFixtureFail('Unexpected curl_getinfo option'),
    };
}
function curl_close(KuaishouFixtureHandle $handle): void {}
function curl_error(KuaishouFixtureHandle $handle): string { return $handle->error; }
function curl_errno(KuaishouFixtureHandle $handle): int { return $handle->error !== '' ? CURLE_COULDNT_CONNECT : CURLE_OK; }
function get_headers(string $url, bool|int $associative = false, mixed $context = null): never { kuaishouFixtureFail('Unexpected get_headers'); }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Immutable PHP archive required');
require_once $root . '/backend/api/kuaishou/KuaishouSpider.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
// Read the frozen entry's literal configuration rather than inventing a UA or
// authentication configuration in the fixture producer.
$entrySource = file_get_contents($root . '/backend/api/kuaishou/ksjx.php');
if (!preg_match("/define\\('USER_AGENT', '([^']+)'\\)/", $entrySource, $ua)
    || !str_contains($entrySource, "define('JSON_OPTIONS', 480)")
    || !str_contains($entrySource, "new KuaishouSpider('', USER_AGENT)")) throw new RuntimeException('Frozen entry boundary changed');
set_error_handler(static function (int $severity, string $message): never { throw new ErrorException($message, 0, $severity); });
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] as $case) {
    $input = $case['input']; $httpQueue = $input['http_responses']; $httpRequests = []; $violations = [];
    try {
        $parser = (new KuaishouSpider('', $ua[1]))->analyze($input['url']);
        // ksjx.php echoes json_encode($result, 480), then the real gateway
        // decodes it. NUMERIC_CHECK is intentionally part of this boundary.
        $wire = json_decode(json_encode($parser, 480 | JSON_THROW_ON_ERROR), true, 128, JSON_THROW_ON_ERROR);
        if ($httpQueue || $violations) throw new RuntimeException('Unconsumed or violated HTTP tape');
        $full = snapAdaptParser('kuaishou', $wire, $input['url']);
        if ($full['code'] === 'OK') $full = snapFinalizeData($full['data'], $input['now']);
        $normal = snapPublicMediaView($full, false, $input['now']);
        $debug = snapPublicMediaView($full, true, $input['now']);
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => $case['id'], 'expected' => ['parser_response' => $parser, 'legacy_response' => $wire,
            'normal' => $normal, 'debug' => $debug, 'http_requests' => $httpRequests]];
    } catch (Throwable $error) {
        throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error);
    }
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR) . PHP_EOL;
