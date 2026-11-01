# Frozen Toutiao PHP product oracle

The synthetic metadata response tapes exercise the unchanged PHP `toutiao`,
`toutiaoRequest`, `toutiaoResponseFromPage`, entry JSON boundary, adapter and
normal/debug public policy from published commit
`25beaee107f8c1a208ced9deb3c637068445e316`, using PHP **8.3.6**.

`inputs.json` is generated without PHP by the reviewed Python producer. The
expected `php-baseline.json` and `manifest.json` are generated only by executing
the pinned PHP archive. Go never generates expected values. Native cURL and
`get_headers` are disabled, URL streams are disabled, and the container has no
network. Original request header/cookie handling and bounded response writers
still execute. An unknown, reordered or unused request fails generation.

The 48 scenarios cover actual quality and source ordering, object and array
video lists, base64/percent/plus URL bytes, large IDs, source and author identity,
description, zero statistics, canonical/query/short-link entry points,
anonymous `ttwid` handling, exact error branches, and the single 35-second
metadata budget. The entry flags preserve textual numbers; unlike Bilibili,
Toutiao does not use `JSON_NUMERIC_CHECK`.

```sh
python3 -B scripts/go-migration/produce-toutiao.py --write-inputs
python3 -B scripts/go-migration/produce-toutiao.py --php /path/to/php-8.3.6 --output /tmp/toutiao-php
python3 -B scripts/go-migration/verify-toutiao.py --directory /tmp/toutiao-php --check-git-source
cmp backend/internal/platform/toutiao/testdata/php-baseline.json /tmp/toutiao-php/php-baseline.json
cmp backend/internal/platform/toutiao/testdata/manifest.json /tmp/toutiao-php/manifest.json
```

Normal verification is read-only and requires neither PHP nor live platform
access. `TestPHPToutiaoBaseline` executes the real Go parser and exports every
normal/debug result when `DOKITOOL_GO_TOUTIAO_OUTPUT` names an absolute file.
The shared gate can feed those actual outputs to the independent validator and
the unchanged frontend consumer. The response tape is synthetic; this fixture
does not claim current Toutiao availability, media downloading or browser save
acceptance.
