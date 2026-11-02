<?php
declare(strict_types=1);
// Offline product-path oracle. The caller loads a pinned Git archive and disables
// native network functions; only the probe's existing transport seam is replaced.
$root = $argv[1] ?? '';
require $root . '/backend/api/douyin/DouyinParser.php';
require $root . '/backend/lib/media-adapters.php';
require $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $level, string $message): never { throw new ErrorException($message, 0, $level); });

function oracleBox(string $kind, string $data): string { return pack('N', strlen($data) + 8) . $kind . $data; }
function oracleMovie(): string
{
    $entry = substr_replace(str_repeat("\0", 78), pack('nn', 2160, 3840), 24, 4);
    $stsd = oracleBox('stsd', pack('NN', 0, 1) . oracleBox('hvc1', $entry));
    $stsz = oracleBox('stsz', pack('NNN', 0, 500000, 10));
    $mdia = oracleBox('mdia', oracleBox('mdhd', pack('NNNNN', 0, 0, 0, 1000, 10000))
        . oracleBox('hdlr', str_repeat("\0", 8) . 'vide') . oracleBox('minf', oracleBox('stbl', $stsd . $stsz)));
    return oracleBox('moov', oracleBox('trak', $mdia));
}

$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
if (isset($document['integer_direct'])) {
    $parser = new DouyinParser(static fn () => null);
    $cases = $document['integer_direct'];
    foreach ($cases as &$case) {
        $case['expected'] = $case['operation'] === 'video_metadata'
            ? (new ReflectionMethod($parser, 'videoMetadata'))->invoke($parser, $case['input'])
            : (new ReflectionMethod($parser, 'audioCandidates'))->invoke($parser, $case['input']['entries'], $case['input']['expires_at']);
    }
    unset($case);
    echo json_encode(['cases' => $cases], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR);
    exit;
}
$original = 'https://aweme.snssdk.com/aweme/v1/play/?video_id=fixture&ratio=default&line=0';
$cdn = 'https://v5-test.douyinvod.com/fixture.mp4';
$file = oracleBox('ftyp', 'qt  ' . pack('N', 0) . 'qt  ') . oracleBox('mdat', str_repeat('x', 6000000)) . oracleMovie();
$detail = ['aweme_id' => '1234567890123456', 'desc' => 'Versioned PHP probe fixture', 'statistics' => ['digg_count' => 0],
    'video' => ['play_addr' => ['uri' => 'fixture'], 'bit_rate' => [
        ['bit_rate' => 1700000, 'is_h265' => 1, 'play_addr' => ['width' => 2160, 'height' => 3840,
            'url_list' => ['https://v3-test.douyinvod.com/standard.mp4']]],
    ]]];
$observed = [];
foreach ($document['cases'] as $spec) {
    $reads = []; $probeResult = null; $probeCalls = 0;
    $transport = static function (string $url, int $offset, int $length) use ($spec, $original, $cdn, $file, &$reads): ?array {
        $destination = $spec['destination'] ?? $cdn;
        if ($url === $original) $response = ['status' => 302, 'headers' => ['location' => $spec['first_location'] ?? $cdn], 'body' => ''];
        elseif ($url === $cdn && $destination !== $cdn && (!($spec['later'] ?? false) || $offset > 0)) {
            $response = ['status' => 302, 'headers' => ['location' => $destination], 'body' => ''];
        } elseif (($spec['unavailable'] ?? false) && $url === $destination) $response = null;
        else {
            $body = substr($file, $offset, $length);
            $response = ['status' => 206, 'headers' => ['content-type' => $spec['mime'],
                'content-range' => 'bytes ' . $offset . '-' . ($offset + strlen($body) - 1) . '/' . strlen($file)], 'body' => $body];
        }
        $record = ['url' => $url, 'offset' => $offset, 'length' => $length, 'response' => $response];
        if ($response !== null) { $record['response']['body_base64'] = base64_encode($response['body']); unset($record['response']['body']); }
        $reads[] = $record;
        return $response;
    };
    $parser = new DouyinParser(static function (string $url) use ($spec, $original, $transport, &$probeResult, &$probeCalls): ?array {
        if ($url !== $original) throw new RuntimeException('Unexpected original probe URL');
        $probeCalls++;
        $probeResult = (new SnapDouyinMediaProbe($transport))->probe($spec['entry'] ?? $url);
        return $probeResult;
    });
    $raw = json_decode((new ReflectionMethod($parser, 'formatData'))->invoke($parser, $detail, $detail['aweme_id']), true, 512, JSON_THROW_ON_ERROR);
    if ($probeCalls !== 1) throw new RuntimeException('Product path did not probe exactly once');
    $source = 'https://www.douyin.com/video/' . $detail['aweme_id'];
    $full = snapAdaptParser('douyin', $raw, $source);
    $full = snapFinalizeData($full['data'], $document['now']);
    $normal = snapPublicMediaView($full, false, $document['now']);
    $debug = snapPublicMediaView($full, true, $document['now']);
    snapValidateEnvelope($normal); snapValidateEnvelope($debug);
    $observed[] = ['id' => $spec['id'], 'input' => ['spec' => $spec, 'detail' => $detail, 'video_id' => $detail['aweme_id'],
        'url' => $source, 'probe_url' => $original, 'now' => $document['now']],
        'expected' => ['probe' => $probeResult, 'transport' => $reads, 'formatter' => $raw['data'], 'normal' => $normal, 'debug' => $debug]];
}
echo json_encode(['format_version' => 1, 'cases' => $observed], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR) . PHP_EOL;
