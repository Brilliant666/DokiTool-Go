#!/usr/bin/env python3
"""Check public download framing responses; this is not browser acceptance."""

import argparse
import datetime
import hashlib
import ipaddress
import json
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        return None


def origin(value):
    parsed = urllib.parse.urlsplit(value)
    loopback = parsed.hostname == "localhost"
    try:
        loopback = loopback or ipaddress.ip_address(parsed.hostname or "").is_loopback
    except ValueError:
        pass
    if (not parsed.hostname or parsed.username is not None or parsed.password is not None
            or parsed.path not in ("", "/") or parsed.query or parsed.fragment
            or not (parsed.scheme == "https" or parsed.scheme == "http" and loopback)):
        raise argparse.ArgumentTypeError("Use an HTTPS origin, or HTTP loopback, without credentials, path or query")
    return value.rstrip("/")


def framing_csp(headers):
    """Keep only embedding directives, never unrelated nonce/script values."""
    relevant = {"frame-ancestors", "frame-src", "child-src", "default-src"}
    return {
        name: [{"embedding_directives": [part.strip() for part in policy.split(";")
                 if part.strip() and part.strip().split()[0].lower() in relevant]}
               for policy in headers.get_all(name, [])]
        for name in ("Content-Security-Policy", "Content-Security-Policy-Report-Only")
    }


def check(opener, base, path, expected_status, frame_policy, timeout, *,
          expected_cache=None, conditions=None, expected_sha256=None):
    expected_statuses = (expected_status,) if isinstance(expected_status, int) else expected_status
    result = {"path": path, "expected_status": expected_status,
              "expected_x_frame_options": frame_policy, "failures": []}
    if conditions:
        result["conditions"] = conditions
    request = urllib.request.Request(base + path, headers={
        "Accept-Encoding": "identity", "Cache-Control": "no-cache",
        "User-Agent": "DokiTool-download-framing-check",
        **(conditions or {}),
    })
    try:
        try:
            response = opener.open(request, timeout=timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read(1024 * 1024 + 1)
            headers = response.headers
            status = response.status
            final_url = response.geturl()
        result.update(status=status, x_frame_options=headers.get_all("X-Frame-Options", []),
                      content_type=headers.get("Content-Type", ""),
                      cache_control=headers.get_all("Cache-Control", []),
                      etag=headers.get("ETag"), last_modified=headers.get("Last-Modified"),
                      bytes=len(body), sha256=hashlib.sha256(body).hexdigest(),
                      csp=framing_csp(headers))
        if status not in expected_statuses:
            result["failures"].append("Unexpected HTTP status")
        if final_url != base + path or status in (301, 302, 303, 307, 308):
            result["failures"].append("Redirects are not allowed")
        if [value.strip().upper() for value in result["x_frame_options"]] != [frame_policy]:
            result["failures"].append("Expected exactly one matching X-Frame-Options value")
        if expected_cache and [value.strip().lower() for value in result["cache_control"]] != [expected_cache]:
            result["failures"].append("Expected exactly one Cache-Control: " + expected_cache)
        if expected_sha256 and result["sha256"] != expected_sha256:
            result["failures"].append("Conditional request did not return the same complete document")
        if len(body) > 1024 * 1024:
            result["failures"].append("Response exceeds the 1 MiB inspection limit")
        if path.startswith("/downloads/bridge.html?") or path == "/downloads/bridge.html":
            if headers.get_content_type() != "text/html" or b'id="dokitool-download-bridge"' not in body:
                result["failures"].append("Missing real HTML bridge document")
        if all(code >= 400 for code in expected_statuses) and b"<html" in body.lower():
            result["failures"].append("Missing download path returned an HTML document")
    except (OSError, urllib.error.URLError) as error:
        # Do not echo exception URLs, proxy details or response bodies.
        result["failures"].append("Request failed: " + type(error).__name__)
    result["passed"] = not result["failures"]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, type=origin)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--timeout", type=float, default=20)
    parser.add_argument("--previous-etag", help="ETag from the original cached bridge response")
    parser.add_argument("--previous-last-modified", help="Last-Modified from the original cached bridge response")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    paths = [
        ("/", 200, "DENY", None),
        ("/downloads/bridge.html", 200, "SAMEORIGIN", "no-store"),
        ("/downloads/bridge.html?frame-policy-check=1", 200, "SAMEORIGIN", "no-store"),
        ("/downloads/worker.js", 200, "DENY", "no-cache"),
        ("/downloads/task-store.js", 200, "DENY", "no-cache"),
        ("/downloads/decoded-length.js", 200, "DENY", "no-cache"),
        ("/downloads/media-format.js", 200, "DENY", "no-cache"),
        ("/downloads/bridge-check-missing.html", 404, "DENY", None),
        # Caddy may reject a child path beneath a regular file as malformed.
        ("/downloads/bridge.html/missing", (400, 404), "DENY", None),
    ]
    opener = urllib.request.build_opener(NoRedirect())
    checks = [check(opener, args.base_url, path, status, policy, args.timeout, expected_cache=cache)
              for path, status, policy, cache in paths]
    bridge = checks[1]
    # The fallback validators also match an existing document on an unfixed server.
    etag = args.previous_etag or bridge.get("etag") or "*"
    modified = args.previous_last_modified or bridge.get("last_modified") or "Fri, 31 Dec 9999 23:59:59 GMT"
    for conditions in ({"If-None-Match": etag}, {"If-Modified-Since": modified},
                       {"If-None-Match": etag, "If-Modified-Since": modified}):
        checks.append(check(opener, args.base_url, "/downloads/bridge.html", 200, "SAMEORIGIN", args.timeout,
                            expected_cache="no-store", conditions=conditions, expected_sha256=bridge.get("sha256")))
    report = {
        "checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "base_url": args.base_url, "checks": checks,
        "passed": all(item["passed"] for item in checks),
        "csp_review_required": any(any(item.get("csp", {}).values()) for item in checks),
        "scope": "Public GET response framing only; CSP needs review when present. No browser control, Worker handshake or file-save verification.",
    }
    rendered = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
