<?php
declare(strict_types=1);
// Deterministic, independent PHP product oracle. Run against the frozen
// 25beaee... source tree with the exact PHP 8.3.6 runtime in CI.
if (PHP_VERSION_ID !== 80306 && getenv('DOKITOOL_LOCAL_ORACLE_PREVIEW') !== '1') {
    fwrite(STDERR, 'Expected PHP 8.3.6, got ' . PHP_VERSION . "\n");
    exit(2);
}
require_once __DIR__ . '/../../backend/lib/weibo-media.php';

$ranges = [
    ['absent', null], ['empty', ''], ['open', 'bytes=0-'],
    ['bounded', 'bytes=0-7'], ['suffix', 'bytes=-8'],
    ['leading-zero', 'bytes=0001-0010'], ['whitespace', ' bytes=1-9 '],
    ['invalid-multiple', 'bytes=0-1,3-4'], ['invalid-order', 'bytes=9-2'],
    ['invalid-start-limit', 'bytes=1073741824-'],
    ['invalid-end-limit', 'bytes=1-1073741824'],
    ['invalid-empty-suffix', 'bytes=-0'],
    ['invalid-suffix-too-long', 'bytes=-99999999999'],
    ['invalid-unit', 'bytes =0-4'],
];
$cases=[];
foreach ($ranges as [$id,$header]) {
    try { $expected=['ok'=>true,'value'=>snapWeiboMediaRange($header)]; }
    catch (InvalidArgumentException $error) { $expected=['ok'=>false,'message'=>$error->getMessage()]; }
    $cases[]=['id'=>'range-'.$id,'operation'=>'range','input'=>['header'=>$header],'expected'=>$expected];
}
$full=['content-type'=>'video/mp4','content-length'=>'128'];
$partial=['content-type'=>'video/mp4','content-length'=>'8','content-range'=>'bytes 0-7/128'];
$plans=[
    ['full-200',200,$full,null],
    ['valid-partial',206,$partial,'bytes=0-7'],
    ['valid-suffix',206,['content-type'=>'video/mp4','content-length'=>'8','content-range'=>'bytes 120-127/128'],'bytes=-8'],
    ['unsatisfied',416,['content-range'=>'bytes */128'],'bytes=0-7'],
    ['not-found',404,$full,null],
    ['empty',200,['content-type'=>'video/mp4','content-length'=>'0'],null],
    ['short-ftyp',200,['content-type'=>'video/mp4','content-length'=>'7'],null],
    ['missing-length',200,['content-type'=>'video/mp4'],null],
    ['too-large',200,['content-type'=>'video/mp4','content-length'=>'1073741825'],null],
    ['unexpected-partial',206,$partial,null],
    ['missing-range',200,$full,'bytes=0-7'],
    ['mismatched-range',206,$partial,'bytes=8-15'],
    ['invalid-length',206,['content-type'=>'video/mp4','content-length'=>'7','content-range'=>'bytes 0-7/128'],'bytes=0-7'],
    ['invalid-total',206,['content-type'=>'video/mp4','content-length'=>'8','content-range'=>'bytes 0-7/1073741825'],'bytes=0-7'],
    ['wrong-mime',200,['content-type'=>'text/html','content-length'=>'128'],null],
    ['wrong-encoding',200,$full+['content-encoding'=>'gzip'],null],
    ['duplicate-length',200,['content-type'=>'video/mp4','content-length'=>'128, 128'],null],
    ['duplicate-mime',200,['content-type'=>'video/mp4, video/mp4','content-length'=>'128'],null],
    ['unsatisfied-without-range',416,['content-range'=>'bytes */128'],null],
];
foreach ($plans as [$id,$status,$headers,$rangeHeader]) {
    $range=$rangeHeader === null ? null : snapWeiboMediaRange($rangeHeader);
    $cases[]=['id'=>'plan-'.$id,'operation'=>'plan','input'=>['status'=>$status,'headers'=>$headers,'range'=>$rangeHeader],
        'expected'=>snapWeiboMediaResponsePlan($status,$headers,$range)];
}
$data=['schema'=>1,'php_source_commit'=>'25beaee107f8c1a208ced9deb3c637068445e316',
    'description'=>'Unmodified PHP media-range and upstream response-plan product functions; fixed inputs, no network, no Go-produced expected',
    'cases'=>$cases];
echo json_encode($data,JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE|JSON_PRETTY_PRINT|JSON_THROW_ON_ERROR),"\n";
