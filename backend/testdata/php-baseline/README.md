# Approved PHP oracle for the Go migration

`cases.json` stores **raw inputs and actual PHP outputs**, not expected values
written from the Go implementation. `manifest.json` binds the data to approved
PHP commit `665bd55134982b9fe107aa1cd00373ba8da8ff3c`, its Git tree, hashes of the
production source and original design fixtures, producer hashes, PHP version,
the fixed fixture clock, and the exact comparison rules.

All inputs are synthetic and offline. No platform request, browser download,
real Cookie, live media ticket, or production key is part of this evidence.
`tickets.json` separately stores actual PHP signing/verification results using
the explicitly public synthetic key of 32 `t` characters, including encoded
Unicode/query bytes, upstream expiry and invalid lifetimes. `catalog.json`
freezes the output of the approved `backend/bin/catalog.php` CLI. Both are
hashed by the same manifest and can be tested without PHP installed.

## Coverage

- All 29 approved design responses, including the compact single-file response,
  audio, live-photo pairs, deferred parts/candidates, DASH/HLS and segment
  structures, source expiry, explicit defaults and higher-quality references.
- Actual PHP finalization and invalid-contract mutations.
- Ordinary and debug public views: geometry plus the four protected capability
  categories, source expiry versus language facts, parent/resource audio facts,
  ambiguous multi-audio summaries, and conflicting evidence.
- Exact source IDs and URL identity behavior: known auth parameters, duplicate
  query order, percent escapes, `+`, empty query/fragment, observed Douyin CDN
  paths, host differences, Unicode private identities, and large integer IDs.
- Candidate metadata extraction, missing/agreeing/conflicting observations,
  explicit audio pairs, repeated combinations, deferred conflict isolation,
  and later FPS/HDR facts for an already observed pairing.

These last candidate inputs target the shared candidate layer. They are not a
claim that the Go platform formatter has been implemented or that a current
platform has returned those synthetic records.

## Normal test execution

Go consumes the committed JSON without PHP:

```sh
cd backend
go test ./internal/unified -run TestPHPBaseline
```

The separate integrity check also needs no PHP:

```sh
python scripts/go-migration/verify.py
```

While the unchanged PHP source is present, verify its recorded bytes too:

```sh
python scripts/go-migration/verify.py --source-root .
```

## Explicit regeneration

Regeneration is a development/review operation. It exports the **pinned Git
commit** into a temporary directory, calls its real PHP functions, and refuses
an unexpected commit tree. It does not use whatever PHP code happens to be in
the current working tree. PHP is never a Go production/runtime dependency.

```sh
python scripts/go-migration/produce.py --php /absolute/path/to/php
python scripts/go-migration/verify.py
```

Review fixture and manifest diffs together. Do not update expected results to
make a failing Go test pass. JSON object-key order and equivalent numeric
spellings are nonsemantic; array order, missing versus null, false versus zero,
exact integer values, URL bytes, stable IDs and selected source credentials are
compared. No blanket URL/signature/expiry stripping is permitted.

The manifest documents the only internal shape adaptation: PHP's empty arrays
returned by the two metadata helpers are serialized as metadata objects. This
does not normalize any public envelope. Validation checks accept/reject while
retaining the original PHP diagnostic as evidence; Go need not copy PHP error
wording. Sources use far-past/far-future expiry where PHP reads wall time
internally. Public generated timestamps use the explicit fixture clock.
