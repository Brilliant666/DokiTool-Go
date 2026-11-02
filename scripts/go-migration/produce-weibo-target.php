<?php
declare(strict_types=1);
// Independent Weibo target oracle: calls frozen production PHP weiboTarget()
// directly, with no network and without changing PHP parsing behavior.
if (PHP_VERSION_ID !== 80306 && getenv('DOKITOOL_LOCAL_ORACLE_PREVIEW') !== '1') {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';

$fid = '1034:1234567890123456';
$long = '12345678:0123456789abcdef0123456789abcdef';
$base = 'https://video.weibo.com/show?fid=' . $fid;
$cases = [
    ['component-direct', $base],
    ['component-trailing-slashes', 'https://video.weibo.com/show////?fid=' . $fid],
    ['component-fid-encoded', 'https://video.weibo.com/show?fid=1034%3A1234567890123456'],
    ['component-md5', 'https://video.weibo.com/show?fid=' . $long],
    ['weibo-tv-show', 'https://weibo.com/tv/show/' . $fid],
    ['weibo-tv-v', 'https://www.weibo.com/tv/v/' . $fid . '/'],
    ['weibo-tv-show-query', 'https://weibo.com/tv/show?fid=' . $fid],
    ['weibo-tv-v-query', 'https://weibo.com/tv/v/?fid=' . $fid],
    ['weibo-tv-path-urlencoded-slash', 'https://weibo.com/tv%2Fshow%2F' . $fid],
    ['status-desktop', 'https://weibo.com/123456/AbC09'],
    ['status-desktop-www', 'https://www.weibo.com/000/xyzZ'],
    ['status-mobile', 'https://m.weibo.cn/status/AbCdE'],
    ['detail-mobile', 'https://m.weibo.cn/detail/012345'],
    ['http-80', 'http://video.weibo.com:80/show?fid=' . $fid],
    ['https-80', 'https://video.weibo.com:80/show?fid=' . $fid],
    ['http-443', 'http://video.weibo.com:443/show?fid=' . $fid],
    ['host-uppercase', 'HTTPS://VIDEO.WEIBO.COM/show?fid=' . $fid],
    ['repeated-scalar-last', $base . '&fid=1034:1111111111111111'],
    ['repeated-scalar-first', 'https://video.weibo.com/show?fid=wrong&fid=' . $fid],
    ['array-last', $base . '&fid[]=bad'],
    ['array-first-scalar-last', 'https://video.weibo.com/show?fid[]=bad&fid=' . $fid],
    ['nested-last', $base . '&fid[a]=bad'],
    ['unclosed-bracket', $base . '&fid[=bad'],
    ['fid-dot-key', $base . '&fi.d=bad'],
    ['fid-nul-key', 'https://video.weibo.com/show?fid%00=' . rawurlencode($fid)],
    ['fid-nul-value', $base . '%00trailing'],
    ['fid-whitespace', 'https://video.weibo.com/show?fid=' . $fid . '%20'],
    ['fid-encoded-hash', 'https://video.weibo.com/show?fid=' . $fid . '%23'],
    ['fid-negative', 'https://video.weibo.com/show?fid=-1034:1234567890123456'],
    ['fid-short', 'https://video.weibo.com/show?fid=1:123456789012345'],
    ['fid-long-prefix', 'https://video.weibo.com/show?fid=123456789:1234567890123456'],
    ['status-too-long', 'https://m.weibo.cn/status/' . str_repeat('a', 33)],
    ['status-invalid-underscore', 'https://weibo.com/123/a_b'],
    ['status-invalid-path', 'https://weibo.com/123/a-b'],
    ['invalid-scheme', 'ftp://video.weibo.com/show?fid=' . $fid],
    ['invalid-host-suffix', 'https://video.weibo.com.evil.example/show?fid=' . $fid],
    ['invalid-localhost', 'https://127.0.0.1/show?fid=' . $fid],
    ['invalid-userinfo', 'https://user@video.weibo.com/show?fid=' . $fid],
    ['invalid-port', 'https://video.weibo.com:444/show?fid=' . $fid],
    ['invalid-control', "https://video.weibo.com/show?fid=$fid\n"],
    ['invalid-backslash', 'https://video.weibo.com\\@evil.example/show?fid=' . $fid],
    ['status-path-query', 'https://weibo.com/123/ab?foo=1'],
    ['query-not-status', 'https://weibo.com/123/?fid=' . $fid],
    ['fragment', $base . '#seen'],
    ['null-input', null],
    ['array-input', ['url' => $base]],
    ['too-long', $base . str_repeat('A', 8200)],
];
$output = [];
foreach ($cases as [$id, $input]) {
    $output[] = ['id' => $id, 'input' => ['url' => $input], 'expected' => weiboTarget($input)];
}
echo json_encode(['schema' => 1,
    'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Unmodified PHP weiboTarget from the pinned production parser, executed with fixed synthetic inputs; no network',
    'cases' => $output],
    JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR), "\n";
