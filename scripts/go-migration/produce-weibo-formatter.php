<?php
declare(strict_types=1);
// Only the frozen PHP production functions may write expected values.
// No media requests are performed; all inputs are synthetic and public.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
date_default_timezone_set('UTC');
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$path = __DIR__ . '/../../backend/internal/platform/weibo/testdata/formatter-inputs.json';
$input = json_decode(file_get_contents($path), true, 512, JSON_THROW_ON_ERROR);
if (($input['schema'] ?? null) !== 1
    || ($input['php_source_commit'] ?? null) !== '25beaee107f8c1a208ced9deb3c637068445e316') {
    throw new RuntimeException('Frozen PHP provenance missing');
}
$cases = [];
foreach ($input['cases'] as $case) {
    $params = $case['input'];
    if ($case['operation'] === 'status') {
        $result = formatWeiboStatusResponse($params['status']);
    } elseif ($case['operation'] === 'component') {
        $result = formatWeiboResponse($params['component'], $params['status']);
    } else {
        throw new RuntimeException('Unexpected formatter operation');
    }
    $cases[] = $case + ['expected' => $result];
}
echo json_encode([
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Original frozen PHP formatWeiboStatusResponse/formatWeiboResponse/weiboVideoResult; synthetic offline fixtures',
    'cases' => $cases,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
