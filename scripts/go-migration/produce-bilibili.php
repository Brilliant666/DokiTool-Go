<?php
declare(strict_types=1);
// Development-only oracle. Native network functions are disabled by the caller.
// The pinned archive is loaded unchanged; only cURL IO is substituted.
final class BilibiliFixtureHandle
{
    public array $options = [];
    public array $response = [];
    public string $error = '';
    public function __construct(public string $url) {}
}
$httpQueue = []; $httpRequests = []; $fixtureViolations = []; $fixtureWarnings = [];
function fixtureFail(string $message): never
{
    global $fixtureViolations;
    $fixtureViolations[] = $message;
    throw new RuntimeException($message);
}
function curl_init(?string $url = null): BilibiliFixtureHandle { return new BilibiliFixtureHandle($url ?? ''); }
function curl_setopt_array(BilibiliFixtureHandle $handle, array $options): bool
{
    foreach ($options as $key => $value) {
        $handle->options[$key] = $value;
        if ($key === CURLOPT_URL) $handle->url = $value;
    }
    return true;
}
function curl_setopt(BilibiliFixtureHandle $handle, int $option, mixed $value): bool
{
    return curl_setopt_array($handle, [$option => $value]);
}
function curl_exec(BilibiliFixtureHandle $handle): string|bool
{
    global $httpQueue, $httpRequests;
    $options = $handle->options;
    $headers = [];
    foreach ($options[CURLOPT_HTTPHEADER] ?? [] as $line) {
        [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value);
    }
    if (isset($options[CURLOPT_USERAGENT])) $headers['user-agent'] = $options[CURLOPT_USERAGENT];
    if (isset($options[CURLOPT_ENCODING])) $headers['accept-encoding'] = $options[CURLOPT_ENCODING];
    $method = !empty($options[CURLOPT_NOBODY]) ? 'HEAD' : 'GET';
    $follow = (bool)($options[CURLOPT_FOLLOWLOCATION] ?? false);
    $httpRequests[] = ['method' => $method, 'url' => $handle->url, 'headers' => (object)$headers,
        'follow_redirects' => $follow, 'transport' => 'curl'];
    if (!$httpQueue) fixtureFail('Unregistered HTTP request: ' . $method . ' ' . $handle->url);
    $entry = array_shift($httpQueue);
    if ($entry['method'] !== $method || $entry['url'] !== $handle->url) fixtureFail('HTTP request differs from fixture: ' . $method . ' ' . $handle->url);
    if ($entry['follow_redirects'] !== $follow) fixtureFail('Unexpected redirect policy');
    if (($options[CURLOPT_SSL_VERIFYPEER] ?? null) !== true || ($options[CURLOPT_SSL_VERIFYHOST] ?? null) !== 2) fixtureFail('TLS checks missing');
    if (($options[CURLOPT_TIMEOUT] ?? 0) <= 0 || ($options[CURLOPT_CONNECTTIMEOUT] ?? 0) <= 0) fixtureFail('Request deadline missing');
    $handle->response = $entry;
    if (isset($entry['error'])) { $handle->error = $entry['error']; return false; }
    $body = $method === 'HEAD' ? '' : ($entry['body'] ?? '');
    if (isset($options[CURLOPT_WRITEFUNCTION])) {
        $written = $options[CURLOPT_WRITEFUNCTION]($handle, $body);
        if ($written !== strlen($body)) { $handle->error = 'Synthetic response exceeded writer limit'; return false; }
        return true;
    }
    return $body;
}
function curl_getinfo(BilibiliFixtureHandle $handle, ?int $option = null): mixed
{
    return match ($option) {
        CURLINFO_HTTP_CODE => $handle->response['status'] ?? 0,
        CURLINFO_EFFECTIVE_URL => $handle->response['final_url'] ?? $handle->url,
        default => fixtureFail('Unregistered curl_getinfo option'),
    };
}
function curl_close(BilibiliFixtureHandle $handle): void {}
function curl_error(BilibiliFixtureHandle $handle): string { return $handle->error; }
function curl_errno(BilibiliFixtureHandle $handle): int { return $handle->error !== '' ? CURLE_COULDNT_CONNECT : CURLE_OK; }
function get_headers(string $url, bool|int $associative = false, mixed $context = null): never { fixtureFail('Unexpected get_headers request: ' . $url); }

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Approved PHP archive required');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once $root . '/backend/api/bilibili/bilibili.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $severity, string $message): bool {
    global $fixtureWarnings;
    // snapRunParser suppresses warning display. This exact known warning is
    // emitted by the HTML transport-failure case and must not turn into a
    // different application result merely because this oracle is strict.
    if ($severity === E_USER_WARNING && $message === 'curl请求错误: Synthetic connection failure') {
        $fixtureWarnings[] = $message;
        return true;
    }
    throw new ErrorException($message, 0, $severity);
});
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] ?? [] as $case) {
    $input = $case['input'];
    $httpQueue = $input['http_responses']; $httpRequests = []; $fixtureViolations = []; $fixtureWarnings = [];
    try {
        $parser = parseBilibili($input['url']);
        // The real Bilibili entry echoes json_encode($result, 480). The gateway
        // decodes that wire body before adaptation, including NUMERIC_CHECK.
        $raw = json_decode(json_encode($parser, 480 | JSON_THROW_ON_ERROR), true, 128, JSON_THROW_ON_ERROR);
        if ($httpQueue || $fixtureViolations) throw new RuntimeException('Unused or violated HTTP fixture: ' . json_encode([$httpQueue, $fixtureViolations]));
        $full = snapAdaptParser('bilibili', $raw, $input['url']);
        if ($full['code'] === 'OK') $full = snapFinalizeData($full['data'], $input['now']);
        $normal = snapPublicMediaView($full, false, $input['now']);
        $debug = snapPublicMediaView($full, true, $input['now']);
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => $case['id'], 'expected' => [
            'parser_response' => $parser, 'legacy_response' => $raw,
            'normal' => $normal, 'debug' => $debug, 'http_requests' => $httpRequests, 'php_warnings' => $fixtureWarnings,
        ]];
    } catch (Throwable $error) {
        throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error);
    }
}
echo json_encode(['cases' => $observed], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR) . PHP_EOL;
