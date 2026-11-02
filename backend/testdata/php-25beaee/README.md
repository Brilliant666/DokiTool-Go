# PHP 25beaee versioned comparison baseline

This directory adds the published PHP commit
`25beaee107f8c1a208ced9deb3c637068445e316` (tree
`e4b9d40f5e6c12a86fff629c105553886b434dcd`). It does not replace the
`665bd55134982b9fe107aa1cd00373ba8da8ff3c` oracle, its inputs, expectations,
producer bytes or manifests.

## Scope and reproducibility

`produce-php-25beaee.py` exports both pinned Git archives. The matching PHP
producer calls the actual `SnapDouyinMediaProbe`, `DouyinParser::formatData`,
adapter, server default selection and normal/debug public policy. Native cURL,
`get_headers` and URL fopen are disabled. Only synthetic bounded byte slices are
supplied through the probe's existing injected transport. No URL is fetched,
and no Go output becomes a PHP expectation.

The comparison runtime is PHP **8.3.6** on both archives, matching the original
frozen oracle's PHP version. Clock is `2026-10-30T12:00:00Z`. This is an offline
code-behavior baseline, not production-runtime or real-browser acceptance.
An initial replay with local PHP 8.4.27 exposed rounding changes in historical
near-tie `round()` samples; those results were not silently reused or written
over the 8.3.6 oracle. Runtime migration is a separate comparison dimension.

```sh
python -X utf8 scripts/go-migration/produce-php-25beaee.py --php /path/to/php-8.3.6
python -X utf8 scripts/go-migration/verify-php-25beaee.py --check-git-source
cd backend
go test ./internal/platform/douyin -run TestPHP25ProbeProductPath -count=1
```

The manifest pins producer/source/fixture hashes. Git-source verification reads
the immutable blobs, so Windows checkout newline conversion is not mistaken for
a product-code change. The producer fails on any unexplained difference in a
reused group; regeneration does not accept Go-derived or manually edited results.

## Compared changes

`cases.json` contains 12 fixed input scenarios, their **old and new actual PHP
observations**, exact requested Range traces and 24 new public envelopes.
`differences.json` lists every changed field and each reused fixture's hash.

| Scenarios | New PHP behavior versus 665bd55 |
| --- | --- |
| QuickTime; case/parameter MIME variant | Real container metadata is read and the complete original can become the server default. |
| New public CDN with MP4 or QuickTime | A trusted initial platform entry may redirect to a safe public CDN without a hard-coded CDN hostname. |
| Later Range moved to a new public CDN | Read remaining metadata from the new safe source and report only the successfully read URL. |
| HTTP CDN Location | Upgrade to HTTPS before any request, then apply the same safe delivery rules. |
| Existing MP4, HTML rejection, unsafe first/later hop, unavailable later source, unauthorized initial CDN | Unchanged; media and entry authorization remain distinct, and a failed later read retains the last confirmed URL with unknown metadata. |

Exactly 6 scenarios change and 6 do not. Complete candidates, stable identities,
source references and server defaults are compared in both public views; a
successful probe is not substituted for formatter/adapter output.

The new PHP archive also re-executes all existing fixed groups before reusing
their original bytes: 208 public/helper scenarios, 21 tickets, 1 catalog, 70
Douyin Parse scenarios/140 public views, 23 probe observations, 72 numeric cases,
13 integer Parse scenarios/26 public views and 12 private integer calls. Their
observed values remain identical under PHP 8.3.6. These are overlapping evidence
groups, not a sum of distinct end-user tests.

## Go and independent consumers

`TestPHP25ProbeProductPath` runs the actual Go probe with the captured byte
responses, then the Go formatter, adapter/default and public views. It compares
every output and requested byte range with the new PHP archive's observations.
Set absolute `DOKITOOL_GO_PHP25_OUTPUT` to export its 24 actual public envelopes
for the existing independent Schema/semantics validator and frontend consumer.

The only private-shape accommodation remains an unknown PHP probe metadata `[]`
represented as an empty Go object; public wire objects are not normalized.

The original Live Photo fragment conflict and unsafe-in-JavaScript `size_bytes`
conflicts remain in their original fixture groups. No scene, assertion or
frontend/independent contract is deleted or relaxed by this baseline. Passing
the 24 added envelopes does not turn those historical failures into passes.
