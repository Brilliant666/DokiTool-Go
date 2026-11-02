<?php
declare(strict_types=1);
// Frozen PHP product helper oracle, unrelated to the Go transport implementation.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';
$references = [
    'root' => 'https://weibo.com/',
    'component' => 'https://weibo.com/tv/show/1034:1234567890123456',
    'visitor' => 'https://passport.weibo.com/visitor/visitor',
    'status' => 'https://weibo.com/ajax/statuses/show?id=9007199254740993',
];
$cases = [];
foreach ($references as $id => $referer) {
    $cases[] = ['id' => $id, 'referer' => $referer, 'expected' => getRequestHeaders($referer)];
}
echo json_encode([
    'schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'cases' => $cases,
], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT | JSON_THROW_ON_ERROR), "\n";
