# Frozen PHP Douyin product path

This fixture group is independent of `../php-baseline` and preserves the approved
PHP implementation at commit `665bd55134982b9fe107aa1cd00373ba8da8ff3c` (tree
`93744d4d718c3299f82640d2888e93252106db70`). It is a development oracle; Go runtime
code must not call PHP or require these producer scripts.

## Evidence and coverage

`cases.json` contains **70** synthetic upstream scenarios executed through the
actual archived `DouyinParser::parse`, its formatter, the public adapter, the
default selector, and both ordinary/debug views. Every one of the **140** public
envelopes passed the archived PHP contract validator while being produced.

The synthetic inputs derive from the approved parser, Douyin probe, observation,
identity and metadata-merge regression suites listed and hashed in the manifest.
They cover complete 4K/1080p/720p/576p files, raw tracks, exact A/B audio pairing,
duplicates, missing and unrelated audio, alternate languages and multichannel
audio, later FPS/HDR facts and conflicts,
cross-field audio contradictions, CDN and play-endpoint renewal, successful and
failed original-route probes, image/Live albums, background audio, short links,
HEAD fallback, and detail-response failures. They are **not** live platform
captures, online download evidence or a claim of current platform availability.

`probe.json` contains **13** ISO BMFF atom cases and **10** calls through the actual
PHP bounded reader. The reader cases include tail `moov` after a large synthetic
`mdat`, manual redirects, a complete small `200` response, later redirect failure,
bad ranges, gzip rejection, unsafe redirects, HTTP-to-HTTPS upgrade, redirect
limits and an oversized `moov`. Only requested synthetic byte slices are stored,
encoded as base64; the large synthetic full file is recreated only in memory.
Three atoms carry explicit `mdhd` timescale/tick ratios of 5005/10000, 5015/10000
and 10005/10000 to freeze actual PHP rounding at millisecond duration boundaries.

`numeric.json` contains **72** actual PHP `round(value, precision)` observations
at precisions 0, 2 and 3. The inputs cover positive/negative near-ties and finite
small/large values. These are inputs and PHP runtime results, not an independent
decimal arithmetic specification or expectations calculated using Go.

## Fixture shape

Each parser case has an ID, a scenario description, raw `input.detail`, the Parse
input URL and fixed `now`, an ordered `http_responses` queue, and an ordered
`probe_results` queue. HTTP records contain method, URL, status, headers, body and
redirect policy; applicable records also have `error`, `final_url` or `history`.
An `error` denotes an intentional transport failure. Unknown, reordered or unused
requests are producer failures and cannot become silent successful fixtures.

`expected.formatter` is the legacy data returned by actual Parse, or `null` for a
parser error. `expected.legacy_response` preserves the complete private response.
`expected.normal` and `expected.debug` are complete public unified-v1 envelopes.
`expected.http_requests` and `expected.probe_requests` contain the exact calls
observed by the PHP test transport. The `transport` label (`curl` or `get_headers`)
describes PHP internals; Go consumers use their injected `fetch.Executor` while
checking method, URL, redirect policy and relevant headers.

For `probe.json`, operation `metadata` takes `{moov_base64, file_size}`. Operation
`probe` takes a starting `url` and ordered `transport` records, each with the URL,
offset, length, and a response of `null` or `{status, headers, body_base64}`.
The sole private shape allowance is PHP's unknown probe metadata `[]` becoming an
empty Go metadata object `{}`. It does not apply to public envelopes or other
empty arrays.

## Renewal pairs for frontend integration

| Group | Before | After | Changes |
| --- | --- | --- | --- |
| CDN | `renewal/cdn/before` | `renewal/cdn/after` | 32-hex path signature, 8-hex path expiry, source order, `expires_at` |
| Play endpoint | `renewal/play/before` | `renewal/play/after` | `sign`, `biz_sign`, source order, `expires_at` |

Both credentials are usable at the fixed clock. Each pair has the same item,
variant and two source identities; URLs and expiry come from their own observed
response. The source order changes in the upstream input. Consumers can export
actual Go responses for the existing frontend default, explicit-source and task
snapshot renewal tests. They must not replace these outputs with hand-built
frontend fixtures.

## Reproduce and compare

```sh
PHPRC=/path/to/php.ini python3 scripts/go-migration/produce-douyin.py \
  --php /path/to/php --output /tmp/dokitool-php-douyin-reproduced
diff -u backend/testdata/php-douyin/cases.json /tmp/dokitool-php-douyin-reproduced/cases.json
diff -u backend/testdata/php-douyin/probe.json /tmp/dokitool-php-douyin-reproduced/probe.json
diff -u backend/testdata/php-douyin/numeric.json /tmp/dokitool-php-douyin-reproduced/numeric.json
diff -u backend/testdata/php-douyin/manifest.json /tmp/dokitool-php-douyin-reproduced/manifest.json
```

The producer uses `git archive` of the pinned commit and never loads production
PHP from the mutable Go checkout. The child PHP process disables its native
network functions and substitutes exact in-memory responses before loading the
unchanged archive. The complete disabled-function list and CLI template appear
in `manifest.json`. The probe uses the approved implementation's existing
transport injection. No platform request or DNS lookup is made.

The fixed clock is `2026-10-30T12:00:00Z`. After the actual adapter runs, its
successful data is finalized again through the unchanged PHP function with that
explicit clock; ordinary/debug policy uses the same clock. Expiry credentials
use 2100 and 2101 because one approved PHP source-merging helper reads wall time
internally. `manifest.json` records this limit and hashes all fixture payloads,
producer scripts and approved PHP sources.

Compare every public field and array order. Do not remove URLs, IDs, signatures,
expiry, navigation links, metadata, defaults or source order to make a mismatch
pass. Only JSON object-key order and equivalent number spellings are nonsemantic;
exact integers, missing versus null, and false versus zero remain meaningful.
