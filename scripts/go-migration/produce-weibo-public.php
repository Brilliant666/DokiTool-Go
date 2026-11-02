<?php
declare(strict_types=1);

// Real frozen PHP adapter/public-view functions. Input is the separately
// frozen PHP 8.3.6 status/TV formatter oracle, never a Go-produced result.
// This is the pre-sign baseline; production signing uses the independently
// validated PHP snapWeiboMediaUrl / Go Sign protocol.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
date_default_timezone_set('UTC');
require_once __DIR__ . '/../../backend/lib/media-adapters.php';
require_once __DIR__ . '/../../backend/lib/media-policy.php';
$input = json_decode(file_get_contents(__DIR__ . '/../../backend/internal/platform/weibo/testdata/php-formatter-baseline.json'), true, 512, JSON_THROW_ON_ERROR);
if (($input['schema'] ?? null) !== 1 || ($input['php_source_commit'] ?? '') !== '25beaee107f8c1a208ced9deb3c637068445e316'
    || count($input['cases'] ?? []) !== 29) {
    throw new RuntimeException('Invalid original PHP formatter provenance');
}
$now = 1730000000;
$source = 'https://weibo.com/1234567890/A1B2C3D4';
$cases = [];
foreach ($input['cases'] as $entry) {
    $legacy = $entry['expected'];
    if (($legacy['code'] ?? null) === 200 && is_array($legacy['data'] ?? null)) {
        $raw = $legacy['data'];
        $data = snapAdaptWeibo($raw, $source);
        $internal = $data['items']
            ? snapFinalizeData($data, $now)
            : snapContractError('NO_MEDIA', '作品未提供可用媒体');
    } else {
        $internal = snapAdaptParser('weibo', $legacy, $source);
    }
    snapValidateEnvelope($internal);
    $normal = snapPublicMediaView($internal, false, $now);
    $debug = snapPublicMediaView($internal, true, $now);
    snapValidateEnvelope($normal);
    snapValidateEnvelope($debug);
    $cases[] = [
        'id' => $entry['id'],
        'input' => ['legacy' => $legacy, 'source' => $source, 'now' => $now],
        'expected' => ['internal' => $internal, 'normal' => $normal, 'debug' => $debug],
    ];
}
echo json_encode([
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Original frozen PHP snapAdaptWeibo/snapFinalizeData/snapPublicMediaView pre-sign outputs on 29 separately frozen PHP parser results',
    'cases' => $cases,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR), "\n";
