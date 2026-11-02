# Frozen Kuaishou PHP oracle

The independent producer runs the unmodified PHP parser and shared runtime from
`25beaee107f8c1a208ced9deb3c637068445e316` using PHP 8.3.6. Native cURL IO is
disabled and replaced by the reviewed HTML/HEAD response tape. No expected
payload is produced by Go.

The 40 cases cover INIT_STATE precedence and its limited repair path, APOLLO
fallback and work lookup, HEAD effective URL handling, video representations and
backup sources, atlas order, single pictures, music, known zero values, the
entrypoint's numeric conversion, and explicit failures. The current oracle has
79 logical cURL requests, 30 successful cases, and 80 normal/debug envelopes.
These are synthetic contract observations, not current platform availability or
media-download evidence.

The private parser output is encoded with the original `ksjx.php` flags `480`
before adapting it. In particular, JSON_NUMERIC_CHECK remains observable.
Unknown `avgBitrate`/`maxBitrate` units are not interpreted as bits per second.
The platform catalog's video/image scope is unchanged; no Live capability is
introduced.

The request comparison retains exact method, URL bytes, explicit headers, body,
automatic following, timeout, redirect limit and automatic Referer configuration.
Only the HEAD `Accept: */*` header implicitly supplied by libcurl is added to the
comparison view because Go declares it explicitly. The frozen PHP record stays
unchanged. GET retains the original empty configured Cookie.

Both HEAD and GET have a 20-second deadline and up to five redirects. Go applies
the shared platform-domain, DNS, public-IP and TLS policy on every hop. The
combined parse deadline is 40 seconds. Only GET opts into automatic Referer;
the shared executor's default remains disabled. The
[libcurl option specification](https://github.com/curl/curl/blob/master/docs/libcurl/opts/CURLOPT_AUTOREFERER.md)
states that automatic Referer also applies to approved cross-origin and HTTPS
to HTTP redirects. Go strips fragments and userinfo from that value and does
not forward authentication on a cross-host or opted-in insecure redirect.

Reproduce with the fixed PHP runtime:

```sh
python scripts/go-migration/produce-kuaishou.py --php /path/to/php8.3.6 --repo /path/to/repository --output /absolute/output
python scripts/go-migration/verify-kuaishou.py --directory /absolute/output --check-git-source
```

The Go test export uses `DOKITOOL_GO_KUAISHOU_OUTPUT` and is written only when
all independent cases and both public views pass. Formal HTTP tests execute the
real catalog, parser, adapter, parse handler, item resolve and missing-item path;
their expected payloads come from this frozen PHP oracle.
