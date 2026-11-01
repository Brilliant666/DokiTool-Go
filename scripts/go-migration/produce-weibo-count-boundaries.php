<?php
declare(strict_types=1);
// Additional independent oracle; old Weibo candidate fixtures stay frozen.
// Run only in the fixed PHP runtime against the verified PHP source archive.
// No requests are made and no Go implementation produces expected values.
if (PHP_VERSION_ID !== 80306 || PHP_INT_SIZE !== 8) {
    fwrite(STDERR, 'Expected 64-bit PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$inputPath = __DIR__ . '/../../backend/internal/platform/weibo/testdata/count-boundary-inputs.json';
$input = json_decode(file_get_contents($inputPath), true, 512, JSON_THROW_ON_ERROR);
if (($input['schema'] ?? null) !== 1 || ($input['php_source_commit'] ?? '') !== '25beaee107f8c1a208ced9deb3c637068445e316') {
    throw new RuntimeException('Invalid frozen PHP count oracle provenance');
}
$cases = [];
$seen = [];
foreach ($input['cases'] as $case) {
    if (!is_string($case['id'] ?? null) || isset($seen[$case['id']]) || !is_string($case['raw_json'] ?? null)) {
        throw new RuntimeException('Invalid or duplicate count boundary case');
    }
    $seen[$case['id']] = true;
    $value = json_decode($case['raw_json'], true, 512, JSON_THROW_ON_ERROR);
    $cases[] = ['id' => $case['id'], 'raw_json' => $case['raw_json'],
        'php_type' => gettype($value), 'expected' => weiboCount($value)];
}
echo json_encode([
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'php_runtime' => PHP_VERSION,
    'description' => 'Additional frozen PHP weiboCount exact-integer, float, numeric-string and invalid-input boundaries',
    'cases' => $cases,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
