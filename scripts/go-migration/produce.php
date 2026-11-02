<?php
declare(strict_types=1);
// Development-only oracle: load the immutable git archive, never the Go checkout's PHP.
$root = $argv[1] ?? '';
if (!is_dir($root)) throw new RuntimeException('Approved PHP archive required');
require_once $root . '/backend/lib/media-adapters.php';
require_once $root . '/backend/lib/media-policy.php';
require_once $root . '/backend/lib/weibo-media.php';
set_error_handler(static function (int $severity, string $message): never {
    throw new ErrorException($message, 0, $severity);
});
$document = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$cases = [];
foreach ($document['cases'] as $case) {
    $i = $case['input'];
    try {
        $value = match ($case['operation']) {
            'validate_envelope' => (static function (array $payload): array { snapValidateEnvelope($payload); return ['valid' => true]; })($i['payload']),
            'finalize_data' => snapFinalizeData($i['data'], $i['now']),
            'finalize_and_public_view' => snapPublicMediaView(snapFinalizeData($i['data'], $i['now']), $i['debug'], $i['now']),
            'quality_compare' => snapQualityCompare($i['left'], $i['right']),
            'same_frame_rate' => snapSameFrameRate((float)$i['a'], (float)$i['b']),
            'source_identity' => snapSourceIdentity($i['url']),
            'sources' => snapSources($i['sources']),
            'merge_sources' => snapMergeSources($i['existing'], $i['additional']),
            'candidate_metadata' => (object)snapCandidateMetadata($i['raw']),
            'merge_metadata' => (object)snapMergeMetadata($i['existing'], $i['additional']),
            'file_variants' => snapFileVariants($i['candidates']),
            'ticket_sign' => snapWeiboMediaUrl($i['url'], $i['key'], $i['now'], $i['expires_at'] ?? null),
            'ticket_verify' => snapWeiboMediaVerifyTicket($i['ticket'], $i['key'], $i['now']),
            default => throw new RuntimeException('Unknown baseline operation'),
        };
        $case['expected'] = $value;
    } catch (Throwable $error) {
        $case['expected'] = ['valid' => false];
        $case['php_error'] = $error->getMessage();
    }
    $cases[] = $case;
}
echo json_encode(['cases' => $cases], JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR) . PHP_EOL;
