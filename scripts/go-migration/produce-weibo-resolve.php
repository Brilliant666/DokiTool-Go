<?php
declare(strict_types=1);
// Invoke the unchanged PHP weiboResolve product path against synthetic tapes.
// No Go expected, no live requests, and only _rand nonce is normalized.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
date_default_timezone_set('UTC');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$input = json_decode(file_get_contents(__DIR__ . '/../../backend/internal/platform/weibo/testdata/resolve-inputs.json'), true, 512, JSON_THROW_ON_ERROR);
if (($input['schema'] ?? null) !== 1 || ($input['php_source_commit'] ?? '') !== '25beaee107f8c1a208ced9deb3c637068445e316') {
    throw new RuntimeException('Invalid frozen PHP oracle provenance');
}
$cases = [];
foreach ($input['cases'] as $case) {
    $sent = [];
    $offset = 0;
    $tape = $case['responses'];
    $exchange = static function (string $url, ?string $post, string $referer) use (&$sent, &$offset, $tape): array {
        $sent[] = ['url' => preg_replace('/([?&]_rand=)[^&]*/', '$1<nonce>', $url),
            'post' => $post, 'referer' => $referer];
        if (!array_key_exists($offset, $tape)) { throw new RuntimeException('Unexpected extra HTTP request'); }
        return $tape[$offset++];
    };
    $result = weiboResolve($case['target'], $exchange);
    if ($offset !== count($tape)) { throw new RuntimeException('Unused canned HTTP response for ' . $case['id']); }
    $cases[] = [
        'id' => $case['id'], 'input' => ['target' => $case['target'], 'responses' => $tape],
        'expected' => ['result' => $result, 'requests' => $sent],
    ];
}
echo json_encode([
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Frozen PHP weiboResolve/formatters and guest request choreography against synthetic responses',
    'cases' => $cases,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
