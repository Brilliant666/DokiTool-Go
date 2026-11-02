<?php
declare(strict_types=1);
// A second independent oracle. The PHP product path is unchanged, the
// resolver is injected, and neither real DNS nor real media is accessed.
if (PHP_VERSION_ID !== 80306 && getenv('DOKITOOL_LOCAL_ORACLE_PREVIEW') !== '1') {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
require_once __DIR__ . '/../../backend/lib/weibo-media.php';

$archive = json_decode(file_get_contents(__DIR__ . '/../../backend/testdata/php-baseline/tickets.json'), true, 512, JSON_THROW_ON_ERROR);
$canonical = null;
foreach ($archive['cases'] as $entry) {
    if ($entry['id'] === 'ticket/sign/canonical') { $canonical = $entry; break; }
}
if ($canonical === null) { throw new RuntimeException('Frozen PHP signed ticket not found'); }
$key = $canonical['input']['key'];
$now = $canonical['input']['now'];
$signed = snapWeiboMediaUrl($canonical['input']['url'], $key, $now);
if ($signed !== $canonical['expected']) { throw new RuntimeException('PHP ticket provenance changed'); }
$ticket = substr($signed, strlen('/api/media/weibo?ticket='));
$cases = [];
$specs = [
    ['method-post', 'POST', ['ticket' => $ticket], null, $key, $now, ['8.8.8.8']],
    ['key-missing', 'GET', ['ticket' => $ticket], null, 'short', $now, ['8.8.8.8']],
    ['ticket-missing', 'GET', [], null, $key, $now, ['8.8.8.8']],
    ['ticket-array', 'GET', ['ticket' => [$ticket]], null, $key, $now, ['8.8.8.8']],
    ['extra-query', 'GET', ['ticket' => $ticket, 'debug' => '1'], null, $key, $now, ['8.8.8.8']],
    ['invalid-signature', 'GET', ['ticket' => 'invalid'], null, $key, $now, ['8.8.8.8']],
    ['expired', 'GET', ['ticket' => $ticket], null, $key, $now + 1800, ['8.8.8.8']],
    ['future-issued', 'GET', ['ticket' => $ticket], null, $key, $now - 1, ['8.8.8.8']],
    ['invalid-range', 'GET', ['ticket' => $ticket], 'bytes=0-1,3-4', $key, $now, ['8.8.8.8']],
    ['head-ignores-range', 'HEAD', ['ticket' => $ticket], 'bytes=0-1,3-4', $key, $now, ['8.8.8.8']],
    ['get-full', 'GET', ['ticket' => $ticket], null, $key, $now, ['8.8.8.8']],
    ['get-bound-range', 'GET', ['ticket' => $ticket], 'bytes=1-7', $key, $now, ['8.8.8.8']],
    ['get-suffix-range', 'GET', ['ticket' => $ticket], 'bytes=-8', $key, $now, ['8.8.8.8']],
    ['dns-empty', 'GET', ['ticket' => $ticket], null, $key, $now, []],
    ['dns-loopback', 'GET', ['ticket' => $ticket], null, $key, $now, ['127.0.0.1']],
    ['dns-mixed', 'GET', ['ticket' => $ticket], null, $key, $now, ['8.8.8.8', '10.0.0.1']],
    ['dns-v4-preferred', 'GET', ['ticket' => $ticket], null, $key, $now, ['2001:4860:4860::8888', '8.8.8.8']],
    ['dns-v6-only', 'GET', ['ticket' => $ticket], null, $key, $now, ['2001:4860:4860::8888']],
    ['dns-invalid', 'GET', ['ticket' => $ticket], null, $key, $now, ['not-an-ip']],
    ['dns-mapped', 'GET', ['ticket' => $ticket], null, $key, $now, ['::ffff:8.8.8.8']],
    ['dns-documentation', 'GET', ['ticket' => $ticket], null, $key, $now, ['2001:db8::1']],
];
foreach ($specs as [$id, $method, $query, $range, $signingKey, $clock, $ips]) {
    $expected = snapWeiboMediaPrepare(
        ['REQUEST_METHOD' => $method, 'HTTP_RANGE' => $range],
        $query, $signingKey, $clock,
        static fn (string $host): array => $ips
    );
    $cases[] = ['id' => 'prepare-' . $id, 'operation' => 'prepare',
        'input' => ['method' => $method, 'query' => (object)$query, 'range' => $range,
            'key' => $signingKey, 'now' => $clock, 'ips' => $ips],
        'expected' => $expected];
}
$data = ['schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Fixed inputs passed to the unmodified PHP snapWeiboMediaPrepare product function; frozen PHP 8.3.6 CI replay must match byte-for-byte',
    'cases' => $cases];
echo json_encode($data, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";
