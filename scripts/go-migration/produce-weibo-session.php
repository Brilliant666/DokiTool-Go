<?php
declare(strict_types=1);
// Independent frozen-product oracle: neither Go nor its expected outputs are
// involved. It calls the original PHP transport/visitor/JSONP helpers.
if (PHP_VERSION_ID !== 80306 && getenv('DOKITOOL_LOCAL_ORACLE_PREVIEW') !== '1') {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$raw = file_get_contents(__DIR__ . '/../../backend/internal/platform/weibo/testdata/session-inputs.json.gz');
if (!is_string($raw) || ($decoded = gzdecode($raw)) === false) {
    throw new RuntimeException('Missing fixed Weibo guest inputs');
}
$inputs = json_decode($decoded, true, 512, JSON_THROW_ON_ERROR);
if (($inputs['schema'] ?? null) !== 1
    || ($inputs['php_source_commit'] ?? '') !== '25beaee107f8c1a208ced9deb3c637068445e316') {
    throw new RuntimeException('Wrong PHP guest oracle source');
}
$cases = [];
foreach ($inputs['cases'] as $case) {
    $input = $case['input'];
    $expected = match ($case['operation']) {
        'transport' => weiboTransportFailure($input['response']),
        'visitor' => weiboIsVisitor($input['response']),
        'jsonp' => weiboJsonp($input['body'], $input['callback']),
        default => throw new RuntimeException('Unknown operation'),
    };
    $cases[] = ['id' => $case['id'], 'operation' => $case['operation'],
        'input' => $input, 'expected' => $expected];
}
$document = [
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Production PHP weiboTransportFailure, weiboIsVisitor and weiboJsonp; fixed inputs, no network or Go-produced expected',
    'cases' => $cases,
];
echo json_encode($document, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES
    | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";
