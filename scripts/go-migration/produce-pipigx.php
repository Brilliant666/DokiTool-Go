<?php
declare(strict_types=1);
// Independent development oracle: unmodified frozen PHP product functions run
// with native cURL disabled; only their external IO and monotonic clock vary.
if (PHP_VERSION_ID !== 80306) throw new RuntimeException('Expected PHP 8.3.6');
date_default_timezone_set('UTC');
final class PipigxFixtureHandle
{
    public array $options = [];
    public array $response = [];
    public string $error = '';
    public function __construct(public string $url) {}
}
$httpQueue = []; $httpRequests = []; $violations = [];
function pipigxFixtureFail(string $message): never
{
    global $violations;
    $violations[] = $message;
    throw new RuntimeException($message);
}
function curl_init(?string $url = null): PipigxFixtureHandle { return new PipigxFixtureHandle($url ?? ''); }
function curl_setopt_array(PipigxFixtureHandle $handle, array $options): bool
{
    foreach ($options as $key => $value) {
        $handle->options[$key] = $value;
        if ($key === CURLOPT_URL) $handle->url = $value;
    }
    return true;
}
function curl_setopt(PipigxFixtureHandle $handle, int $option, mixed $value): bool { return curl_setopt_array($handle, [$option => $value]); }
function curl_exec(PipigxFixtureHandle $handle): string|bool
{
    global $httpQueue, $httpRequests;
    $options = $handle->options;
    $headers = [];
    foreach ($options[CURLOPT_HTTPHEADER] ?? [] as $line) {
        [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value);
    }
    if (isset($options[CURLOPT_USERAGENT])) $headers['user-agent'] = $options[CURLOPT_USERAGENT];
    if (isset($options[CURLOPT_COOKIE])) $headers['cookie'] = $options[CURLOPT_COOKIE];
    $method = !empty($options[CURLOPT_POST]) ? 'POST' : 'GET';
    $follow = (bool)($options[CURLOPT_FOLLOWLOCATION] ?? false);
    $httpRequests[] = ['method' => $method, 'url' => $handle->url, 'headers' => (object)$headers,
        'body' => $options[CURLOPT_POSTFIELDS] ?? '', 'follow_redirects' => $follow,
        'timeout_seconds' => $options[CURLOPT_TIMEOUT] ?? null];
    if (!$httpQueue) pipigxFixtureFail('Unregistered request: ' . $method . ' ' . $handle->url);
    $entry = array_shift($httpQueue);
    if ($entry['method'] !== $method || $entry['url'] !== $handle->url || $entry['follow_redirects'] !== $follow) pipigxFixtureFail('Request differs: ' . $method . ' ' . $handle->url);
    if (($options[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($options[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2) pipigxFixtureFail('Missing TLS verification');
    if (($options[CURLOPT_TIMEOUT] ?? 0) < 1 || $options[CURLOPT_TIMEOUT] > 20 || ($options[CURLOPT_CONNECTTIMEOUT] ?? 0) !== 5) pipigxFixtureFail('Missing bounded request timeout');
    $handle->response = $entry;
    if (isset($entry['error'])) { $handle->error = $entry['error']; return false; }
    foreach ($entry['headers'] as $name => $values) {
        foreach ((array)$values as $value) {
            $line = $name . ': ' . $value . "\r\n";
            if (!isset($options[CURLOPT_HEADERFUNCTION]) || $options[CURLOPT_HEADERFUNCTION]($handle, $line) !== strlen($line)) pipigxFixtureFail('Header callback failed');
        }
    }
    $body = $entry['body'];
    if (!isset($options[CURLOPT_WRITEFUNCTION])) pipigxFixtureFail('Original bounded writer missing');
    if ($options[CURLOPT_WRITEFUNCTION]($handle, $body) !== strlen($body)) { $handle->error = 'Response exceeded writer limit'; return false; }
    return true;
}
function curl_getinfo(PipigxFixtureHandle $handle, ?int $option = null): mixed
{
    return match ($option) { CURLINFO_HTTP_CODE => $handle->response['status'] ?? 0,
        default => pipigxFixtureFail('Unregistered curl_getinfo option') };
}
function curl_close(PipigxFixtureHandle $handle): void {}
function curl_error(PipigxFixtureHandle $handle): string { return $handle->error; }
function curl_errno(PipigxFixtureHandle $handle): int { return $handle->error !== '' ? ($handle->response['errno'] ?? CURLE_COULDNT_CONNECT) : CURLE_OK; }
function get_headers(string $url, bool|int $associative = false, mixed $context = null): never { pipigxFixtureFail('Unexpected get_headers'); }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Pinned PHP archive required');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once $root . '/backend/api/pipigx.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $severity, string $message): never { throw new ErrorException($message, 0, $severity); });
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] as $case) {
    $input = $case['input']; $httpQueue = $input['http_responses']; $httpRequests = []; $violations = [];
    $clockCalls = 0;
    try {
        // Execute the original entry sequence with its original helper functions.
        $params = extractParamsFromUrl($input['url']);
        if ($params === false) {
            $parser = formatResponse(400, '提取参数出错');
        } else {
            $payload = ['pid' => (int)$params['pid'], 'mid' => (int)$params['mid'], 'type' => 'post'];
            $parser = processApiResponse(sendPostRequest('https://h5.pipigx.com/ppapi/share/fetch_content', $payload));
        }
        $flags = 480; // Original public parser entry flags, including JSON_NUMERIC_CHECK.
        $wire = json_decode(json_encode($parser, $flags | JSON_THROW_ON_ERROR), true, 128, JSON_THROW_ON_ERROR);
        if ($httpQueue || $violations) throw new RuntimeException('Unconsumed/violated HTTP tape');
        $full = snapAdaptParser('pipigx', $wire, $input['url']);
        if ($full['code'] === 'OK') $full = snapFinalizeData($full['data'], $input['now']);
        $normal = snapPublicMediaView($full, false, $input['now']);
        $debug = snapPublicMediaView($full, true, $input['now']);
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => $case['id'], 'expected' => ['parser_response' => $parser, 'legacy_response' => $wire,
            'normal' => $normal, 'debug' => $debug, 'http_requests' => $httpRequests, 'clock_calls' => $clockCalls]];
    } catch (Throwable $error) {
        throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error);
    }
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR) . PHP_EOL;
