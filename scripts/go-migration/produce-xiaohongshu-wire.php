<?php
declare(strict_types=1);
// A separate, offline observation of the frozen PHP parser's real JSON output
// boundary. This does not alter or regenerate the original 37-case baseline.
// Run with PHP 8.3.6, allow_url_fopen=0 and the native functions listed below
// disabled. The only substituted dependency is the page callback accepted by
// the original parser; its formatNoteData/output and gateway adapter are intact.
if (PHP_VERSION_ID !== 80306) throw new RuntimeException('PHP 8.3.6 required');
date_default_timezone_set('UTC');
function time(): int { return 1793361600; }

$disabledNetwork = ['curl_exec', 'curl_multi_exec', 'dns_get_record', 'fsockopen', 'pfsockopen', 'stream_socket_client'];
if ((bool)ini_get('allow_url_fopen')) throw new RuntimeException('allow_url_fopen=0 required');
foreach ($disabledNetwork as $name) if (function_exists($name)) throw new RuntimeException('Native IO must be disabled: ' . $name);
$root = realpath($argv[1] ?? '');
if ($root === false || !is_dir($root)) throw new RuntimeException('Pinned PHP archive directory required');
$sources = [
    'backend/api/xiaohongshu/XiaohongshuParser.php' => '2940a55c57ac104565331cdda86af1c1a358a3b454b40c2af835f37af3dbc426',
    'backend/api/xiaohongshu/XiaohongshuMediaProbe.php' => '9447cb8c3fd5d03086a5f6e66de8136b9c7ca82763bfceaebcaeba51ccd58769',
    'backend/api/xiaohongshu/xhsjx.php' => 'd7d2e6e1653ef74ff7e71c2f3e9d3867ddef79ee58c134b669503787df20a2f2',
    'backend/lib/runtime.php' => '0c90dca5e4e90498e904a69bb891625c7f66cbcf0723c15fedd72524d8134358',
    'backend/lib/media-probe.php' => 'ad4265e3e86b6d3fb2138c8e9c717cbfb17da1b11cb4d1326ea61aac8fbb1703',
    'backend/lib/media-adapters.php' => '0257081ef5f4fe40bf1f288145405a56e05dfff6c887d2e73b117c6f27ea27db',
    'backend/lib/media-contract.php' => '39e2e218917ad2ddc329cfbd2d7b4e6130e240bced8e4af6526656ad6b243817',
    'backend/lib/media-policy.php' => 'ccc523c6c8e53c4f8af18f51c2204a4ba14c124ed9aeb86448dafa7cf30840d9',
    'backend/lib/media-metadata.php' => 'd96a408e91029580085ef530cb99511887e165dd11063570dbb75fee58a3483e',
    'backend/lib/platforms.php' => '5b82407a1fb6a1b7a849154fd65a44f030b2298466561dc289b60b182c359074',
    'backend/lib/weibo-media.php' => '5530988ce434497251aef118af2dc0833b45640da84ad32450657aa7b86adb18',
    'backend/lib/gateway.php' => '74f150b294220fd0c63e9851b31830c4ba2d11a8f8b58b1bf603c6a674e7a970',
    'backend/config/platforms.json' => 'a900b8eadfdefdf1401371bdce9bfe09b4f330e24c1b733b18adec438c70b487',
    'docs/schema/unified-v1/unified-v1.schema.json' => 'bde334422f63488da67b8a5a40f3081f143e031f9849f2df282a175a8df6165a',
];
$sourceRecords = [];
foreach ($sources as $relative => $expectedHash) {
    $actual = hash_file('sha256', $root . '/' . $relative);
    if (!hash_equals($expectedHash, $actual)) throw new RuntimeException('Pinned PHP source differs: ' . $relative);
    $sourceRecords[] = ['path' => $relative, 'sha256' => $actual];
}
require_once $root . '/backend/api/xiaohongshu/XiaohongshuParser.php';
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
set_error_handler(static function (int $level, string $message): never { throw new ErrorException($message, 0, $level); });

// Keep numeric spellings in HTML, rather than decode/re-encode the inputs in
// the producer. In particular, 1e3, 12.0, -0.0 and 1e-5 reach json_decode intact.
$cover = '[{"url":"https://images.example/wire-cover.jpg"}]';
$stream = '{"masterUrl":"https://media.example/wire-video.mp4?sig=synthetic%2Bwire","width":1920,"height":1080,"duration":12345,"avgBitrate":5000000,"size":7715625}';
$live = '{"h264":[' . $stream . ']}';
$specs = [
    ['integral-numbers', '1e3', '12.0', $cover, 'video'],
    ['negative-zero', '-0.0', '-0', $cover, 'video'],
    ['fractional-numbers', '1.25', '12.5', $cover, 'video'],
    ['exponent-numbers', '1e-5', '1e21', $cover, 'video'],
    ['numeric-strings', '"1e3"', '"12.0"', $cover, 'video'],
    ['large-integers', '9007199254740993', '9223372036854775808', $cover, 'video'],
    ['empty-array-cover', '"empty array"', '"author"', '[{"urlPre":[],"urlDefault":"https://images.example/fallback.jpg"}]', 'video'],
    ['empty-object-cover', '"empty object"', '"author"', '[{"urlPre":{},"urlDefault":"https://images.example/fallback.jpg"}]', 'video'],
    ['empty-image-live', '"live association"', '"author"', '[{"url":[],"stream":' . $live . '},{"url":"https://images.example/static.jpg"},{"url":"https://images.example/live.jpg","stream":' . $live . '}]', 'normal'],
    ['associative-image-key-zero', '"numeric image keys"', '"author"', '{"1":{"urlPre":"https://images.example/one-cover.jpg","url":"https://images.example/one.jpg"},"0":{"urlPre":"https://images.example/zero-cover.jpg","url":"https://images.example/zero.jpg"}}', 'normal'],
];
$url = 'https://www.xiaohongshu.com/explore/wirefixture01?xsec_token=synthetic%2Bwire&keep=a+b%2525';
$pageUrl = str_replace('www.xiaohongshu.com', 'www.rednote.com', $url);
$observed = [];
foreach ($specs as [$name, $title, $nickname, $images, $type]) {
    $note = '{"noteId":"wirefixture01","type":"' . $type . '","title":' . $title . ',"desc":"PHP wire boundary","user":{"nickname":' . $nickname . ',"userId":"007"},"imageList":' . $images . ',"video":{"media":{"stream":' . $live . '}}}';
    $html = '<script>window.__INITIAL_STATE__={"note":{"noteDetailMap":{"wirefixture01":{"note":' . $note . '}}}};</script>';
    $pageRequests = [];
    $page = static function (string $address) use ($pageUrl, $html, &$pageRequests): array {
        if ($address !== $pageUrl || $pageRequests) throw new RuntimeException('Unexpected page request ' . $address);
        $pageRequests[] = $address;
        return ['status' => 200, 'body' => $html, 'url' => $address];
    };
    $mediaRequests = [];
    $media = static function (string $address, float $deadline) use (&$mediaRequests): never {
        $mediaRequests[] = $address;
        throw new RuntimeException('Unexpected media request ' . $address);
    };
    $parser = new XiaohongshuParser($page, $media);
    $parser->setCookie('');
    try {
        $wire = $parser->parse($url);
        if (count($pageRequests) !== 1) throw new RuntimeException('Page request was not consumed');
        if ($mediaRequests) throw new RuntimeException('Wire fixture invoked a media probe');
        $legacy = json_decode($wire, true, 128, JSON_THROW_ON_ERROR);
        $full = snapAdaptParser('xiaohongshu', $legacy, $url);
        $normal = snapPublicMediaView($full, false, time());
        $debug = snapPublicMediaView($full, true, time());
        snapValidateEnvelope($normal); snapValidateEnvelope($debug);
        $observed[] = ['id' => 'xiaohongshu-wire/' . $name,
            'input' => ['url' => $url, 'now' => time(), 'page_url' => $pageUrl, 'html' => $html],
            'expected' => ['legacy_wire' => $wire, 'legacy_response' => $legacy, 'normal' => $normal, 'debug' => $debug, 'page_requests' => $pageRequests]];
    } catch (Throwable $error) { throw new RuntimeException('Case ' . $name . ': ' . $error->getMessage(), 0, $error); }
}
$output = ['format_version' => 1, 'baseline_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'baseline_tree' => 'e4b9d40f5e6c12a86fff629c105553886b434dcd', 'php_version' => PHP_VERSION,
    'entry_json_flags' => 2097472, 'fixed_clock' => ['unix_seconds' => time(), 'rfc3339' => '2026-10-30T12:00:00Z'],
    'source_files' => $sourceRecords,
    'producer_files' => [['path' => 'scripts/go-migration/produce-xiaohongshu-wire.php', 'sha256' => hash_file('sha256', __FILE__)]],
    'transport_substitution' => ['mechanism' => 'Only the immutable parser constructor page callback is supplied with reviewed literal HTML. Original parse/format/output, JSON decode, adapter and public views execute. Any media callback or native network use fails.',
        'disabled_native_functions' => array_merge(['time'], $disabledNetwork), 'recorded_fields' => ['page URL sequence']],
    'evidence_scope' => 'Offline JSON wire and formatter compatibility only; no live platform, CDN or media evidence and no Go-generated expected values.',
    'parse_case_count' => count($observed), 'public_envelope_count' => count($observed) * 2, 'cases' => $observed];
echo json_encode($output, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
