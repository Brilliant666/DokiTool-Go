<?php
declare(strict_types=1);
// Independent offline oracle: the original PHP weiboJsonRequest/weiboStartGuest
// product functions are invoked with an injected request tape; no network or Go.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$raw = file_get_contents(__DIR__ . '/../../backend/internal/platform/weibo/testdata/request-inputs.json.gz');
if (!is_string($raw) || ($decoded = gzdecode($raw)) === false) {
    throw new RuntimeException('Missing fixed Weibo request inputs');
}
$inputs = json_decode($decoded, true, 512, JSON_THROW_ON_ERROR);
if (($inputs['schema'] ?? null) !== 1
    || ($inputs['php_source_commit'] ?? '') !== '25beaee107f8c1a208ced9deb3c637068445e316') {
    throw new RuntimeException('Unexpected frozen PHP source');
}
$cases = [];
foreach ($inputs['cases'] as $case) {
    $input = $case['input'];
    $responses = $input['responses'];
    $sent = [];
    $index = 0;
    $request = static function (string $url, ?string $post, string $referer) use ($responses, &$sent, &$index): array {
        // random_int() contributes only a non-identity cache buster. Preserve
        // every other URL byte, including query order and encoded parameters.
        $normalized = preg_replace('/([?&]_rand=)[^&]*/', '$1<nonce>', $url);
        $sent[] = ['url' => $normalized, 'post' => $post, 'referer' => $referer];
        if (!isset($responses[$index])) { throw new RuntimeException('Unexpected extra product request'); }
        return $responses[$index++];
    };
    $attempted = (bool)$input['guest_attempted'];
    $result = weiboJsonRequest($request, $input['url'], $input['post'], $input['referer'], $attempted);
    if ($index !== count($responses)) { throw new RuntimeException('Unconsumed product response for ' . $case['id']); }
    $cases[] = ['id' => $case['id'], 'input' => $input,
        'expected' => ['result' => $result, 'guest_attempted' => $attempted, 'requests' => $sent]];
}
echo json_encode(['schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Unmodified product weiboJsonRequest/weiboStartGuest with fixed transport replay and nonce normalization; no network or Go expected',
    'cases' => $cases], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";
