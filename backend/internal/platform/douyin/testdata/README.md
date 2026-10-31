# PHP integer boundary oracle

`php-integer-boundaries.json` freezes 13 complete Parse scenarios (26 normal/debug
envelopes) and 12 private video/audio metadata calls. The synthetic inputs cover
exact integers above JavaScript's safe-integer range, values close to PHP's signed
64-bit limit, numeric strings, fractional truncation, and upstream status codes.
The expected values are produced by PHP, not calculated from Go results.

The immutable PHP baseline is commit
`665bd55134982b9fe107aa1cd00373ba8da8ff3c`, tree
`93744d4d718c3299f82640d2888e93252106db70`, executed with PHP 8.3.6. The JSON records
the SHA-256 of 12 archived source/schema files and both development producers.
`TestPHPIntegerBoundaryBaseline` pins the complete fixture hash, checks producer
bytes and inventories, and compares actual Go Parse/formatter outputs exactly.
Go tests require neither a PHP executable nor platform access.

## Reproduce the PHP expectations

From the repository root, use Python 3.12+ and a PHP 8.3.6 installation with the
cURL constants available. The optional `PHPRC` selects that installation's config.
Generate to a temporary file before comparing with the checked-in oracle:

```sh
PHPRC=/path/to/php.ini python3 backend/internal/platform/douyin/testdata/produce_integer.py --php /path/to/php8.3 --output /tmp/php-integer-boundaries.json
cmp backend/internal/platform/douyin/testdata/php-integer-boundaries.json /tmp/php-integer-boundaries.json
```

The producer uses `git archive` of the pinned commit; it does not load current PHP
working-tree code. It disables native cURL/get_headers and URL file access, then
uses the existing development Parse harness with registered HTTP/probe fixtures.
The archived PHP formatter, adapter, default selection, normal/debug policy, and
validator generate/check all 26 public envelopes. Private cases use reflection
on the same archived PHP class. There are no real platform requests.

## Export actual Go responses

From `backend`, use an absolute destination path:

```sh
DOKITOOL_GO_INTEGER_OUTPUT=/tmp/go-integer-responses.json go test ./internal/platform/douyin -run '^TestPHPIntegerBoundaryBaseline$' -count=1
```

The export has the shape `{ "cases": [{ "id", "normal", "debug" }] }`. It is
written only after all Parse views, formatter checks and private metadata cases
execute successfully. Partial `-run` filters cannot produce an incomplete export.
The existing 70-case Douyin fixture and its separate actual-response export are
unchanged.

These cases establish PHP-to-Go equivalence. They do not establish that every
legacy PHP boundary value is accepted by the independent Python validator or
the existing JavaScript frontend. Those checks must run separately; any rejection
is evidence to retain, not a reason to rewrite expectations or weaken validators.
