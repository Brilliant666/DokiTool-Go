# Bilibili PHP oracle

`inputs.json` contains synthetic upstream metadata and exact ordered HTTP response tapes. It does not contain Go-generated expectations or downloaded media. Case IDs begin with `bilibili/` so combined platform exports remain unambiguous.

`php-baseline.json` and `manifest.json` are generated only by PHP **8.3.6** from the immutable PHP commit `25beaee107f8c1a208ced9deb3c637068445e316` (tree `e4b9d40f5e6c12a86fff629c105553886b434dcd`). The producer runs `parseBilibili`, the real entry's `json_encode(..., 480)` / gateway decode boundary, `snapAdaptParser`, finalization, ordinary/debug policy, and the PHP contract validator. Numeric strings therefore pass through `JSON_NUMERIC_CHECK` before adaptation, exactly as in the entrypoint.

## Generate and verify

Review or regenerate only the synthetic inputs without PHP:

```sh
python3 -B scripts/go-migration/produce-bilibili.py --write-inputs
```

Generate observations with PHP 8.3.6 and the curl extension loaded (constants are needed; native network functions are disabled):

```sh
python3 -B scripts/go-migration/produce-bilibili.py --php /path/to/php --output /tmp/bilibili-php
python3 -B scripts/go-migration/verify-bilibili.py --directory /tmp/bilibili-php --check-git-source
```

Copy both generated JSON files into this directory after inspecting the real PHP execution result. Do not manufacture expectations, replace the pinned PHP source with the working tree, or edit expectations to fit Go. The manifest hashes the immutable PHP sources, reviewed inputs, producer scripts, and generated output. Existing PHP baseline fixtures are unaffected.

## Coverage

The fixture covers 51 parser cases / 102 public responses:

- Exact P selection, invalid/array/duplicate P queries, missing P/cid, short-link HEAD behavior and explicit original P precedence.
- Mobile and HTML-meta entry paths, including the known suppressed PHP warning on failed HTML transport.
- View-stage restriction/deletion/transport/JSON failures and strict integer success codes; selected-play failure remains an unavailable current item in a successful multipart work.
- Current stream facts without inventing quality from the requested qn or uploaded view dimensions; ordinary and numeric-string metadata.
- One durl as a file, multiple durl entries as ordered remux segments, each legal DASH video/audio pairing, and deferred missing audio.
- Covered mux filtering, higher resolution, catalogue HDR profiles, fractional/high FPS, alternate language, multichannel audio, and an expired alternative source.
- Collection membership, stable P identities, per-section deduplication, deferred related works, and incomplete collection groups.
- Strict string/float versus integer upstream success codes, and signed-source renewal with reversed primary/backup order while preserving source and variant identities.

The expiry case deliberately supplies a source-object shape accepted by the existing normalizer; it is synthetic coverage rather than an observed live Bilibili response. Expiry values are far from both the wall clock and fixed fixture clock. Signed URL query bytes remain present and exact.

## Observation contract

Each case contains `input.url`, `input.now`, and `input.http_responses`. Expectations contain:

- `parser_response`: the PHP function result before entry encoding.
- `legacy_response`: the result after numeric-check entry encoding and gateway decoding.
- `normal` / `debug`: fully finalized public envelopes, both validated by PHP.
- `http_requests`: ordered method, URL, headers, redirect policy and PHP transport label.
- `php_warnings`: the known HTML transport warning, captured without changing its production behavior; unexpected warnings fail generation.

All arrays and their order, exact integers, missing/null, booleans, URL bytes, stable IDs, metadata and defaults remain significant. JSON object order and equivalent numeric spelling are not semantic. `transport: curl` is evidence about the PHP implementation, not a requirement that Go use cURL. PHP's empty `accept-encoding` option means all cURL-supported encodings; Go explicitly requests only the encodings its executor decodes. This is the sole declared request-header comparison adjustment. View/play requests must not acquire a browser UA, Cookie or Authorization header.

These fixtures do not establish current platform availability, maximum online quality, browser playback, file saving or throughput. DASH execution remains unsupported.
