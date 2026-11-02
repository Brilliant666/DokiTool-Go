<?php
declare(strict_types=1);
// Frozen-product oracle: invoke the unchanged PHP Weibo candidate functions.
// All input URLs and metadata are synthetic; no outbound requests are made.
if (PHP_VERSION_ID !== 80306) {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
define('DOKITOOL_PARSER_LIBRARY_ONLY', true);
require_once __DIR__ . '/../../backend/api/weibo.php';

$cdn = 'https://f.video.weibocdn.com/';
$candidateCases = [
    ['empty-url', null, '1080p', []],
    ['blank-url', '  ', '1080p', []],
    ['invalid-scheme', 'javascript:alert(1)', '高清', []],
    ['invalid-userinfo', 'https://user:pass@f.video.weibocdn.com/v.mp4', '720p', []],
    ['normal-unknown', $cdn . 'a.mp4', '高清', []],
    ['label-1080p', $cdn . 'a.mp4', '1080P 清晰', []],
    ['label-4k', $cdn . 'a.mp4', '超清 4K', []],
    ['label-2k', $cdn . 'a.mp4', '2k', []],
    ['label-8k', $cdn . 'a.mp4', '8K', []],
    ['label-boundary', $cdn . 'a.mp4', 'abc4kdef', []],
    ['protocol-relative', '//f.video.weibocdn.com/a.mp4', '标清', []],
    ['trimmed-url', '  ' . $cdn . 'a.mp4  ', 'SD', []],
    ['explicit-dimensions', $cdn . 'a.mp4', '360p', ['width' => 1920, 'height' => 1080]],
    ['one-dimension-template', $cdn . 'a.mp4?template=1920x1080.a', '1080p', ['width' => 640]],
    ['template-from-query', $cdn . 'a.mp4?template=3840x2160.mp4', '普通', []],
    ['template-repeated', $cdn . 'a.mp4?template=640x360&template=1280x720', '普通', []],
    ['zero-dimensions', $cdn . 'a.mp4', '360p', ['width' => 0, 'height' => '0']],
    ['signed-source-metadata', $cdn . 'a.mp4?token=abc%2Fdef&x=1', '高码率', ['width' => '1,920', 'height' => '1,080', 'bitrate' => '2,000,000', 'size' => '3,500', 'fps' => '59.94', 'video_codecs' => 'h265']],
    ['untrusted-metadata', $cdn . 'a.mp4', '720p', ['width' => '-1', 'height' => 'NaN', 'bitrate' => -50, 'size' => false, 'fps' => null]],
    ['width-only', $cdn . 'a.mp4', '中画质', ['width' => 1920]],
    ['codec-string', $cdn . 'a.mp4', '未知', ['video_codecs' => 'avc1.640028']],
    ['codec-empty', $cdn . 'a.mp4', '未知', ['video_codecs' => '']],
    ['http-video', 'http://f.video.weibocdn.com/file.mp4', '540p', []],
];
$countCases = [
    ['count-null', null], ['count-bool', true], ['count-zero', 0],
    ['count-string-zero', '0'], ['count-commas', '12,345'],
    ['count-fraction', '29.97'], ['count-exponent', '2.5e2'],
    ['count-negative', '-1'], ['count-nan', 'NaN'],
    ['count-empty', ''], ['count-bad-comma', '1,00'],
    ['count-leading-space', ' 10 '], ['count-float-zero', 0.0],
];
$aggregateCases = [
    ['none', [], []],
    ['video-single', ['urls' => ['高清' => $cdn . 'a.mp4']], []],
    ['video-sorted', ['urls' => ['低清' => $cdn . 'lo.mp4?template=640x360', '高画质' => $cdn . 'hi.mp4?template=1920x1080']], []],
    ['video-dedup-first', ['urls' => ['1080p' => $cdn . 'x.mp4', '720p' => $cdn . 'x.mp4']], []],
    ['playback-only', [], ['page_info' => ['media_info' => ['playback_list' => [
        ['play_info' => ['mime' => 'video/mp4', 'url' => $cdn . 'p.mp4', 'label' => 'HD', 'width' => 1280, 'height' => 720, 'bitrate' => 1500000]],
    ]]]]],
    ['playback-ignores-image', [], ['page_info' => ['media_info' => ['playback_list' => [
        ['play_info' => ['mime' => 'image/jpeg', 'url' => $cdn . 'nope.jpg', 'label' => '1080p']],
        ['play_info' => ['mime' => 'video/mp4', 'url' => $cdn . 'yes.mp4', 'label' => '720p']],
    ]]]]],
    ['playback-over-video-duplicate', ['urls' => ['lower' => $cdn . 'same.mp4', 'other' => $cdn . 'other.mp4']], ['page_info' => ['media_info' => ['playback_list' => [
        ['play_info' => ['mime' => 'video/mp4', 'url' => $cdn . 'same.mp4', 'label' => '1080p', 'width' => 1920, 'height' => 1080]],
    ]]]]],
    ['fallback-mp4', [], ['page_info' => ['media_info' => [
        'mp4_720p_mp4' => $cdn . 'f720.mp4', 'mp4_1080p_mp4' => $cdn . 'f1080.mp4',
        'stream_url' => $cdn . 'flow.mp4',
    ]]]],
    ['fallback-skipped', ['urls' => ['source' => $cdn . 'source.mp4']], ['page_info' => ['media_info' => ['mp4_1080p_mp4' => $cdn . 'f1080.mp4']]]],
    ['fallback-duplicate', [], ['page_info' => ['media_info' => ['mp4_1080p_mp4' => $cdn . 'same.mp4', 'mp4_hd_url' => $cdn . 'same.mp4']]]],
    ['playback-invalid-mime', [], ['page_info' => ['media_info' => ['playback_list' => [
        ['play_info' => ['mime' => 'video/x-flv', 'url' => $cdn . 'flv.mp4', 'label' => '480p']],
        ['play_info' => ['mime' => null, 'url' => $cdn . 'none.mp4']],
    ]]]]],
    ['playback-conflicting-resolution', [], ['page_info' => ['media_info' => ['playback_list' => [
        ['play_info' => ['mime' => 'video/mp4', 'url' => $cdn . 'one.mp4?template=3840x2160', 'label' => 'SD', 'width' => 854, 'height' => 480]],
        ['play_info' => ['mime' => 'video/mp4', 'url' => $cdn . 'two.mp4', 'label' => '720p', 'bitrate' => 3000000]],
    ]]]]],
];
$cases = [];
foreach ($candidateCases as [$id, $url, $label, $info]) {
    $cases[] = ['id' => $id, 'kind' => 'candidate',
        'input' => ['url' => $url, 'label' => $label, 'info' => $info],
        'expected' => weiboCandidate($url, $label, $info)];
}
foreach ($countCases as [$id, $value]) {
    $cases[] = ['id' => $id, 'kind' => 'count',
        'input' => ['value' => $value], 'expected' => weiboCount($value)];
}
foreach ($aggregateCases as [$id, $video, $status]) {
    $cases[] = ['id' => $id, 'kind' => 'candidates',
        'input' => ['video_info' => $video, 'status' => $status,
            'url_keys' => array_keys($video['urls'] ?? [])],
        'expected' => weiboCandidates($video, $status)];
}
echo json_encode([
    'schema' => 1, 'php_source_commit' => '25beaee107f8c1a208ced9deb3c637068445e316',
    'description' => 'Frozen PHP Weibo count, candidate and ordered candidates production helpers, synthetic offline inputs',
    'cases' => $cases,
], JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR), "\n";
