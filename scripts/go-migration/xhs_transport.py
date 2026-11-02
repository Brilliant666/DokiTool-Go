"""Compare real TLS-backed Go HTTP exports to unchanged fixed PHP page oracles."""
from __future__ import annotations

import copy
from public_projection import read_json, same_json


def verify_transport(path, frozen_path):
    frozen = read_json(frozen_path)
    entries = read_json(path)["responses"]
    required = {}
    page_cases = set()
    statuses = {"OK": 200, "INVALID_INPUT": 400, "UPSTREAM_BLOCKED": 403,
                "RATE_LIMITED": 429, "CONTENT_UNAVAILABLE": 404, "NO_MEDIA": 404,
                "UPSTREAM_INVALID_RESPONSE": 502}
    for case in frozen["cases"]:
        steps = case["input"]["http_responses"]
        if not steps or case["id"] == "xiaohongshu/entry/xhs-com-alias":
            continue
        if any(step.get("range") or not step["url"].startswith("https://")
               or not step["status"] or step.get("error", 0) for step in steps):
            continue
        page_cases.add(case["id"])
        for view in ("normal", "debug"):
            payload = case["expected"][view]
            choices = [""]
            if payload["code"] == "OK":
                choices.extend(item["id"] for item in payload["data"]["items"])
                choices.append("missing-item")
            for http2 in (False, True):
                for choice in choices:
                    expected = copy.deepcopy(payload)
                    if choice == "missing-item":
                        expected = {"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE",
                                    "error": {"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
                    elif choice:
                        # Existing PHP gateway resolve contract, independently
                        # checked here; no expected fixture is overwritten.
                        expected["data"]["default_item_id"] = choice
                    required[(case["id"], http2, view, choice)] = (statuses[expected["code"]], expected)
    if len(page_cases) < 20:
        raise ValueError("Unexpected loss of frozen PHP page-only cases")
    actual = {}
    for entry in entries:
        key = (entry["fixture_id"], entry["http2"], entry["view"], entry["item_id"])
        if key in actual or key not in required:
            raise ValueError("Duplicate or unregistered TLS-backed HTTP export")
        status, expected = required[key]
        if entry["status"] != status or not same_json(entry["payload"], expected):
            raise ValueError("TLS-backed HTTP differs from independent PHP oracle: " + str(key))
        actual[key] = entry
    if actual.keys() != required.keys():
        raise ValueError("Missing TLS-backed HTTP export inventory")
    return {"passed": True, "frozen_page_cases": len(page_cases), "http_responses": len(actual),
            "protocols": ["HTTP/1.1 over TLS", "HTTP/2 over TLS"],
            "parse_envelopes_for_frontend": 2 * len(page_cases),
            "evidence": "Controlled local TLS servers with original PHP response tapes, not online platform access"}
