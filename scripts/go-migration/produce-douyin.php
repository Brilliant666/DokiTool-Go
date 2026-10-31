<?php
declare(strict_types=1);
// Development-only oracle. The CLI disables the listed native network functions
// before this process loads the unmodified, immutable PHP archive.
final class FixtureHandle
{
    public array $options = [];
    public array $response = [];
    public int $headerSize = 0;
    public string $error = '';
    public function __construct(public string $url) {}
}

$httpQueue = []; $httpRequests = []; $fixtureViolations = [];
function fixtureFail(string $message): never
{
    global $fixtureViolations;
    $fixtureViolations[] = $message;
    throw new RuntimeException($message);
}
function fixtureResponse(string $method, string $url, array $headers, bool $follow, string $transport): array
{
    global $httpQueue, $httpRequests;
    $httpRequests[] = ['method' => $method, 'url' => $url, 'headers' => (object)$headers,
        'follow_redirects' => $follow, 'transport' => $transport];
    if (!$httpQueue) fixtureFail('Unregistered HTTP request: ' . $method . ' ' . $url);
    $entry = array_shift($httpQueue);
    if ($entry['method'] !== $method || $entry['url'] !== $url) {
        fixtureFail('HTTP request differs from registered fixture: ' . $method . ' ' . $url);
    }
    if (isset($entry['follow_redirects']) && $entry['follow_redirects'] !== $follow) fixtureFail('Unexpected redirect policy');
    return $entry;
}
function curl_init(?string $url = null): FixtureHandle { return new FixtureHandle($url ?? ''); }
function curl_setopt_array(FixtureHandle $handle, array $options): bool
{
    foreach ($options as $key => $value) $handle->options[$key] = $value;
    return true;
}
function curl_setopt(FixtureHandle $handle, int $option, mixed $value): bool { $handle->options[$option] = $value; return true; }
function curl_exec(FixtureHandle $handle): string|bool
{
    $options = $handle->options;
    $headers = [];
    foreach ($options[CURLOPT_HTTPHEADER] ?? [] as $line) {
        [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value);
    }
    if (isset($options[CURLOPT_USERAGENT])) $headers['user-agent'] = $options[CURLOPT_USERAGENT];
    if (isset($options[CURLOPT_ENCODING])) $headers['accept-encoding'] = $options[CURLOPT_ENCODING];
    if (isset($options[CURLOPT_RANGE])) $headers['range'] = 'bytes=' . $options[CURLOPT_RANGE];
    $method = !empty($options[CURLOPT_NOBODY]) ? 'HEAD' : (!empty($options[CURLOPT_POST]) ? 'POST' : 'GET');
    $response = fixtureResponse($method, $handle->url, $headers, (bool)($options[CURLOPT_FOLLOWLOCATION] ?? false), 'curl');
    $handle->response = $response;
    if (isset($response['error'])) { $handle->error = $response['error']; return false; }
    $header = 'HTTP/1.1 ' . $response['status'] . " Fixture\r\n";
    foreach ($response['headers'] ?? [] as $name => $value) {
        foreach (is_array($value) ? $value : [$value] as $entry) $header .= $name . ': ' . $entry . "\r\n";
    }
    $header .= "\r\n";
    $handle->headerSize = strlen($header);
    $body = !empty($options[CURLOPT_NOBODY]) ? '' : ($response['body'] ?? '');
    $bytes = (!empty($options[CURLOPT_HEADER]) ? $header : '') . $body;
    if (isset($options[CURLOPT_WRITEFUNCTION])) {
        $written = $options[CURLOPT_WRITEFUNCTION]($handle, $bytes);
        if ($written !== strlen($bytes)) { $handle->error = 'Synthetic response exceeded writer limit'; return false; }
        return true;
    }
    return $bytes;
}
function curl_getinfo(FixtureHandle $handle, ?int $option = null): mixed
{
    return match ($option) {
        CURLINFO_HTTP_CODE => $handle->response['status'] ?? 0,
        CURLINFO_EFFECTIVE_URL => $handle->response['final_url'] ?? $handle->url,
        CURLINFO_HEADER_SIZE => $handle->headerSize,
        CURLINFO_CONTENT_LENGTH_DOWNLOAD => (float)($handle->response['headers']['Content-Length'] ?? -1),
        default => fixtureFail('Unregistered curl_getinfo option'),
    };
}
function curl_close(FixtureHandle $handle): void {}
function curl_error(FixtureHandle $handle): string { return $handle->error; }
function curl_errno(FixtureHandle $handle): int { return $handle->error !== '' ? CURLE_COULDNT_CONNECT : CURLE_OK; }
function get_headers(string $url, bool|int $associative = false, mixed $context = null): array|false
{
    $options = stream_context_get_options(stream_context_get_default());
    $headers = [];
    foreach (explode("\r\n", $options['http']['header'] ?? '') as $line) {
        if (str_contains($line, ':')) { [$name, $value] = explode(':', $line, 2); $headers[strtolower(trim($name))] = trim($value); }
    }
    $response = fixtureResponse('GET', $url, $headers, true, 'get_headers');
    if (isset($response['error'])) return false;
    $result = ['HTTP/1.1 ' . $response['status'] . ' Fixture'];
    foreach ($response['headers'] ?? [] as $name => $value) $result[$name] = $value;
    if ($response['history'] ?? []) $result['Location'] = $response['history'];
    return $result;
}

$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Approved PHP archive required');
require_once $root . '/backend/api/douyin/DouyinParser.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $severity, string $message): never { throw new ErrorException($message, 0, $severity); });
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$observed = [];
foreach ($document['cases'] ?? [] as $case) {
    $input = $case['input'];
    $httpQueue = $input['http_responses']; $httpRequests = []; $fixtureViolations = [];
    $probeQueue = $input['probe_results']; $probeRequests = [];
    $parser = new DouyinParser(static function (string $url) use (&$probeQueue, &$probeRequests): ?array {
        $probeRequests[] = $url;
        if (!$probeQueue) fixtureFail('Unregistered metadata probe: ' . $url);
        $entry = array_shift($probeQueue);
        if ($entry['url'] !== $url) fixtureFail('Metadata probe URL differs from fixture');
        if (isset($entry['error'])) throw new RuntimeException($entry['error']);
        return $entry['result'];
    });
    try {
        $raw = json_decode($parser->parse($input['url']), true, 512, JSON_THROW_ON_ERROR);
        if ($httpQueue || $probeQueue || $fixtureViolations) throw new RuntimeException('Unused or violated request fixture: ' . json_encode([$httpQueue, $probeQueue, $fixtureViolations]));
        $full = snapAdaptParser('douyin', $raw, $input['url']);
        if ($full['code'] === 'OK') $full = snapFinalizeData($full['data'], $input['now']);
        $normal = snapPublicMediaView($full, false, $input['now']);
        $debug = snapPublicMediaView($full, true, $input['now']);
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => $case['id'], 'expected' => [
            'formatter' => $raw['code'] === 200 ? $raw['data'] : null,
            'legacy_response' => $raw, 'normal' => $normal, 'debug' => $debug,
            'http_requests' => $httpRequests, 'probe_requests' => $probeRequests,
        ]];
    } catch (Throwable $error) {
        throw new RuntimeException('Case ' . $case['id'] . ': ' . $error->getMessage(), 0, $error);
    }
}

// Build binary inputs in PHP from the already established regression builder.
// The exported atom/base64 and response bytes become the durable Go test inputs.
function fixtureBox(string $type, string $payload): string { return pack('N', strlen($payload) + 8) . $type . $payload; }
function fixtureMovie(int $width = 2160, int $height = 3840, string $codec = 'hvc1', int $sampleSize = 500000, int $version = 0, bool $variable = false, int $scale = 1000, int $ticks = 10000): string
{
    $entry = str_repeat("\0", 78); $entry = substr_replace($entry, pack('nn', $width, $height), 24, 4);
    $stsd = fixtureBox('stsd', pack('NN', 0, 1) . fixtureBox($codec, $entry));
    $stsz = fixtureBox('stsz', pack('NNN', 0, $variable ? 0 : $sampleSize, 10) . ($variable ? str_repeat(pack('N', $sampleSize), 10) : ''));
    $mdhd = $version === 0 ? pack('NNNNN', 0, 0, 0, $scale, $ticks) : pack('N*', 0x01000000, 0, 0, 0, 0, $scale, 0, $ticks);
    $mdia = fixtureBox('mdia', fixtureBox('mdhd', $mdhd) . fixtureBox('hdlr', str_repeat("\0", 8) . 'vide') . fixtureBox('minf', fixtureBox('stbl', $stsd . $stsz)));
    return fixtureBox('moov', fixtureBox('trak', $mdia));
}
$probeCases = [];
$moov = fixtureMovie();
foreach ([
    ['hevc-portrait-fixed', $moov, 6000000],
    ['h264-mdhd-v1-variable', fixtureMovie(1920, 1080, 'avc1', 200000, 1, true), 6000000],
    ['av1-small-samples', fixtureMovie(1280, 720, 'av01', 1), 10000],
    ['truncated', substr($moov, 0, -1), 6000000],
    ['sample-bytes-exceed-file', $moov, 100],
    ['unknown-codec', fixtureMovie(2160, 3840, 'zzzz'), 6000000],
    ['zero-width', fixtureMovie(0), 6000000],
    ['multiple-video-tracks', fixtureBox('moov', substr($moov, 8) . substr($moov, 8)), 6000000],
    ['extended-moov', pack('N', 1) . 'moov' . pack('NN', 0, strlen($moov) + 8) . substr($moov, 8), 6000000],
    ['unsafe-extended-size', pack('N', 1) . 'moov' . pack('NN', 0x200000, 0) . substr($moov, 8), 6000000],
    ['duration-round-5005-10000', fixtureMovie(720, 1280, 'avc1', 1, 0, false, 10000, 5005), 1000],
    ['duration-round-5015-10000', fixtureMovie(720, 1280, 'avc1', 1, 0, false, 10000, 5015), 1000],
    ['duration-round-10005-10000', fixtureMovie(720, 1280, 'avc1', 1, 0, false, 10000, 10005), 1000],
] as [$name, $bytes, $size]) {
    $probeCases[] = ['id' => 'metadata/' . $name, 'operation' => 'metadata',
        'input' => ['moov_base64' => base64_encode($bytes), 'file_size' => $size],
        'expected' => SnapDouyinMediaProbe::metadata($bytes, $size)];
}
$original = 'https://aweme.snssdk.com/aweme/v1/play/?video_id=fixture&ratio=default&line=0';
$cdn = 'https://v5-test.douyinvod.com/fixture.mp4';
$ftyp = fixtureBox('ftyp', 'isom' . pack('N', 0) . 'isom');
$large = $ftyp . fixtureBox('mdat', str_repeat('x', 6000000)) . $moov;
$small = $ftyp . fixtureBox('mdat', str_repeat('x', 100)) . fixtureMovie(720, 1280, 'avc1', 1);
foreach (['tail-moov-and-redirect', 'small-complete-200', 'later-redirect-fails', 'unavailable', 'wrong-range', 'gzip', 'unsafe-redirect', 'http-upgrade', 'redirect-loop', 'oversized-moov'] as $name) {
    $records = [];
    $start = in_array($name, ['small-complete-200', 'wrong-range', 'gzip', 'oversized-moov'], true) ? $cdn : $original;
    $probe = new SnapDouyinMediaProbe(static function (string $url, int $offset, int $length) use ($name, $original, $cdn, $large, $small, $ftyp, &$records): ?array {
        $response = null;
        if ($name === 'unavailable') $response = null;
        elseif ($name === 'unsafe-redirect') $response = ['status' => 302, 'headers' => ['location' => 'https://127.0.0.1/private.mp4'], 'body' => ''];
        elseif ($name === 'redirect-loop') $response = ['status' => 302, 'headers' => ['location' => $url === $original ? $cdn : $original], 'body' => ''];
        elseif ($url === $original) $response = ['status' => 302, 'headers' => ['location' => $name === 'http-upgrade' ? str_replace('https://', 'http://', $cdn) : $cdn], 'body' => ''];
        elseif ($name === 'later-redirect-fails' && $url !== $cdn) $response = null;
        elseif ($name === 'later-redirect-fails' && $offset > 0) $response = ['status' => 302, 'headers' => ['location' => 'https://v5-unavailable.douyinvod.com/failed.mp4'], 'body' => ''];
        elseif ($name === 'small-complete-200') $response = ['status' => 200, 'headers' => ['content-type' => 'video/mp4', 'content-length' => (string)strlen($small)], 'body' => $small];
        else {
            $file = $name === 'oversized-moov' ? $ftyp . pack('N', 2 * 1024 * 1024) . 'moov' . str_repeat('x', 65536) : $large;
            $size = $name === 'oversized-moov' ? strlen($ftyp) + 2 * 1024 * 1024 : strlen($file);
            $body = substr($file, $offset, $length);
            $response = ['status' => 206, 'headers' => ['content-type' => 'video/mp4',
                'content-range' => 'bytes ' . ($name === 'wrong-range' ? $offset + 1 : $offset) . '-' . ($offset + strlen($body) - 1) . '/' . $size], 'body' => $body];
            if ($name === 'gzip') $response['headers']['content-encoding'] = 'gzip';
        }
        $record = ['url' => $url, 'offset' => $offset, 'length' => $length, 'response' => $response];
        if ($response !== null) { $record['response']['body_base64'] = base64_encode($response['body']); unset($record['response']['body']); }
        $records[] = $record;
        return $response;
    });
    $value = $probe->probe($start);
    $probeCases[] = ['id' => 'probe/' . $name, 'operation' => 'probe',
        'input' => ['url' => $start, 'transport' => $records], 'expected' => $value];
}
$numericCases = [];
foreach ($document['numeric_cases'] ?? [] as $case) {
    $case['expected'] = round($case['input']['value'], $case['input']['precision']);
    $numericCases[] = $case;
}
echo json_encode(['cases' => $observed, 'probe_cases' => $probeCases, 'numeric_cases' => $numericCases], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR) . PHP_EOL;
