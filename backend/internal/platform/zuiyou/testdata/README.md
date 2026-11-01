# Frozen Zuiyou product oracle

`php-baseline.json` contains **48 parser scenarios and 96 public HTTP envelopes**,
generated with unmodified PHP commit
`25beaee107f8c1a208ced9deb3c637068445e316` (tree
`e4b9d40f5e6c12a86fff629c105553886b434dcd`) under **PHP 8.3.6, 64-bit**.
The fixed clock is `2026-10-30T12:00:00Z` (`1793361600`). Inputs contain synthetic
metadata and reserved example media hosts; generation does not contact platforms
or download media.

The producer runs original `parseZuiyou`, the original entry encoding flags
**480** (`JSON_NUMERIC_CHECK` included), `snapAdaptParser`, and actual `snapHandle`
normal/debug parse or resolve handling. It substitutes only network I/O and
`time()` in an extracted, untouched baseline archive. The manifest pins every
source, producer, input and expected-output hash. The producer verifies PHP's
version and the archive's tree before generating anything.

## Covered boundaries

- `imgs` sets video display order; original `videos` mapping keys set stable item
  identity. Reversing order, duplicate image IDs, integer/list keys, an unlisted
  mapping entry, and removing or invalidating the first video are covered.
- `resolve` retains the inventory and selects the requested surviving item by
  `default_item_id`. A missing requested video returns an explicit 404 instead
  of selecting another video. The expected response is produced by real PHP
  gateway handling; tests do not patch a parse response to manufacture resolve
  expectations.
- Entry encoding preserves exact integer IDs above `2^53` and the original
  numeric-string conversion before adapter IDs are computed.
- Direct/query IDs, repeated/array parameters, path fallback, integer overflow,
  one-hop short-link HEAD requests, and the fixed `post/detail_h5` POST are
  recorded with exact URL, method, body, explicit headers, timeout and redirect
  options. A foreign `Location` may contribute its `pid`; it is never requested.
- Single/multiple video sources, image albums and their original-size fallback
  order, duplicate/invalid images, malformed or missing media, API transport and
  decoding errors are covered. HLS/DASH stay explicit unsupported deliveries
  with a null default and `processing_required`; no merging is implemented.

The only request-comparison normalization adds libcurl's implicit `Accept: */*`
when comparing a HEAD request to the recorded explicit PHP options. No public
response fields, source choices, signed URL bytes, quality facts or stable IDs
are excluded from comparison.

## Reproduction and verification

From the repository root, after making the fixed PHP source commit available:

```sh
python3 -B scripts/go-migration/produce-zuiyou.py --php /path/to/php-8.3.6 --output /tmp/zuiyou-php-oracle
python3 -B scripts/go-migration/verify-zuiyou.py --check-git-source
python3 -B scripts/go-migration/verify-zuiyou.py --directory /tmp/zuiyou-php-oracle --check-git-source
```

`verify-zuiyou.py` independently checks source provenance, request inventory,
public statuses, clock, refresh/resolve context, usable-default references,
source membership, normal/debug consistency and cross-scenario identity/order.
It does not execute Go or compute the frozen PHP expectations.

Go package tests compare the private parser result and entry-480 wire result
against those real PHP observations. `api.TestZuiyouHTTPPHPOracle` compares the
full response and HTTP status through the actual Go handler for normal/debug,
parse/resolve, and exports `{ "cases": [{ "id", "normal", "debug" }] }` to
`DOKITOOL_GO_ZUIYOU_OUTPUT` when that environment variable is set and all cases
pass. The existing independent schema and frontend checks consume that export.

These are controlled offline migration checks. They do not demonstrate current
upstream availability, real CDN download throughput, browser saving, or a
production rollout.
