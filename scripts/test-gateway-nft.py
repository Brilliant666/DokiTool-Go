#!/usr/bin/env python3
"""Linux-only network-rule fixture; never changes the host network namespace.

Run with sudo on a disposable Linux test runner. Root only provisions namespaces
and nft rules. All traffic generators and listeners run with an unprivileged UID.
This tests the rule delta, not yt-dlp, platform availability, or media delivery.
"""
import argparse
import http.client
import http.server
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import threading
import uuid

UID = 23147  # Fixture identity; not a deployment UID.
PORTS = (20080, 20081)
UPSTREAM = "93.184.216.2"  # Synthetic address in an unconnected test namespace.


def run(*args, input=None):
    return subprocess.check_output(args, input=input, text=True, stderr=subprocess.STDOUT)


class Echo(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_CONNECT(self):
        self.send_response(200)
        self.end_headers()
        body = self.rfile.read(4)
        self.wfile.write(body)
        self.wfile.flush()
        self.close_connection = True


def server(kind):
    assert os.getuid() == UID, "traffic fixture must not run as root"
    endpoints = ([("127.0.0.1", p) for p in (*PORTS, 20082, 20091, 20090)]
                 + [("127.0.0.2", PORTS[0])]) if kind == "front" else [
                     (UPSTREAM, p) for p in (80, 443, 81)]
    servers = []
    for endpoint in endpoints:
        instance = http.server.ThreadingHTTPServer(endpoint, Echo)
        servers.append(instance)
        threading.Thread(target=instance.serve_forever, daemon=True).start()
    print(json.dumps({"ready": True, "uid": os.getuid(), "listeners": endpoints}), flush=True)
    signal.pause()


def client(host, port, method):
    assert os.getuid() == UID, "traffic fixture must not run as root"
    try:
        conn = http.client.HTTPConnection(host, port, timeout=2)
        if method == "POST":
            body = b"controlled-gateway-body"
            conn.request("POST", "http://fixture.example/echo", body)
            response = conn.getresponse()
            good = response.status == 200 and response.read() == body
        else:
            conn.set_tunnel("fixture.example", 443)
            conn.connect()
            conn.sock.sendall(b"ping")
            good = conn.sock.recv(4) == b"ping"
        conn.close()
        return {"connected": good, "uid": os.getuid(), "method": method}
    except OSError as error:
        return {"connected": False, "uid": os.getuid(), "errno": error.errno}


def check_fixture(output):
    if os.geteuid() != 0 or sys.platform != "linux":
        raise SystemExit("requires root provisioner on an isolated Linux runner")
    prefix = "doki-gate-" + uuid.uuid4().hex[:8]
    front, back = prefix + "-a", prefix + "-b"
    created, processes, evidence = [], [], []
    script = str(Path(__file__).resolve())
    def cancelled(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, cancelled)

    def ns(name, *args, input=None):
        return run("ip", "netns", "exec", name, *args, input=input)

    def unprivileged(name, *args):
        return ["ip", "netns", "exec", name, "setpriv", "--reuid", str(UID),
                "--regid", str(UID), "--clear-groups", "--no-new-privs",
                "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", *args]

    def rules():
        return json.loads(ns(front, "nft", "-j", "list", "chain", "inet", "fixture", "limited"))["nftables"]

    def counters():
        return {r["rule"].get("comment"): next((e["counter"]["packets"]
                for e in r["rule"]["expr"] if "counter" in e), 0)
                for r in rules() if "rule" in r}

    def rule_shape():
        # Keep ordering, selectors and verdicts; ignore only mutable counters.
        shape = []
        for row in rules():
            if "rule" not in row:
                continue
            rule = row["rule"]
            shape.append({"comment": rule.get("comment"),
                          "expr": [e for e in rule["expr"] if "counter" not in e]})
        return shape

    def probe(label, host, port, expected, method="POST", expected_rule=None, namespace=None):
        before = counters()
        result = json.loads(run(*unprivileged(namespace or front, sys.executable, script,
                                            "--client", host, str(port), method)))
        after = counters()
        assert result["connected"] == expected, (label, result)
        if expected_rule:
            assert after[expected_rule] > before[expected_rule], (label, before, after)
        evidence.append({"case": label, "result": result,
                         "counter_delta": {k: after[k] - before[k] for k in after if after[k] != before[k]}})

    try:
        for name in (front, back):
            run("ip", "netns", "add", name)
            created.append(name)
            ns(name, "ip", "link", "set", "lo", "up")
        run("ip", "link", "add", "gate-a", "netns", front, "type", "veth", "peer", "name", "gate-b", "netns", back)
        for name, device, address in ((front, "gate-a", "93.184.216.1/30"), (back, "gate-b", UPSTREAM + "/30")):
            ns(name, "ip", "address", "add", address, "dev", device)
            ns(name, "ip", "link", "set", device, "up")
        # No default route or physical uplink is present in either namespace.
        ns(back, "sysctl", "-q", "-w", "net.ipv4.ip_unprivileged_port_start=0")
        for name, kind in ((front, "front"), (back, "back")):
            process = subprocess.Popen(unprivileged(name, sys.executable, script, "--server", kind),
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            processes.append(process)
            ready = json.loads(process.stdout.readline())
            assert ready["ready"] and ready["uid"] == UID
            evidence.append({"case": kind + "-listeners", **ready})
        baseline = f'''table inet fixture {{
 chain output {{ type filter hook output priority 0; policy accept; meta skuid {UID} jump limited; }}
 chain limited {{
  ip saddr 127.0.0.1 ip daddr 127.0.0.1 tcp sport 20090 ct direction reply ct state established counter accept comment "old-api-reply"
  ip daddr 127.0.0.53 udp dport 53 counter accept comment "old-dns-udp"
  ip daddr 127.0.0.53 tcp dport 53 counter accept comment "old-dns-tcp"
  ip daddr 127.0.0.1 tcp dport 20091 counter accept comment "old-internal"
  fib daddr type local counter reject comment "reject-local"
  ip daddr {{ 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.88.99.0/24, 192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/3 }} counter reject comment "reject-special"
  ip6 daddr != 2000::/3 counter reject comment "reject-v6"
  ip6 daddr {{ 2001::/23, 2001:db8::/32, 2002::/16, 3fff::/20 }} counter reject comment "reject-v6-special"
  tcp dport {{ 80, 443 }} counter accept comment "old-public-web"
  counter reject comment "reject-other"
 }}
}}'''
        ns(front, "nft", "--check", "-f", "-", input=baseline)
        ns(front, "nft", "-f", "-", input=baseline)
        original_shape = rule_shape()
        probe("baseline-self-connect-denied", "127.0.0.1", PORTS[0], False, expected_rule="reject-local")
        # Root client is only a controlled analogue of the existing ingress; listener stays nonroot.
        root_api = "import http.client;c=http.client.HTTPConnection('127.0.0.1',20090,timeout=2);c.request('POST','/',b'old');r=c.getresponse();assert r.read()==b'old'"
        before = counters()["old-api-reply"]
        ns(front, sys.executable, "-c", root_api)
        assert counters()["old-api-reply"] > before
        evidence.append({"case": "old-api-reply-before", "passed": True})
        probe("old-public-web-before", UPSTREAM, 80, True, expected_rule="old-public-web")
        anchor = next(r["rule"]["handle"] for r in rules() if r.get("rule", {}).get("comment") == "reject-local")
        delta = f'''insert rule inet fixture limited position {anchor} meta skuid {UID} oifname "lo" ip saddr 127.0.0.1 ip daddr 127.0.0.1 tcp dport {{ 20080, 20081 }} ct direction original ct state {{ new, established }} counter accept comment "gateway-original"
insert rule inet fixture limited position {anchor} meta skuid {UID} oifname "lo" ip saddr 127.0.0.1 ip daddr 127.0.0.1 tcp sport {{ 20080, 20081 }} ct direction reply ct state established counter accept comment "gateway-reply"
'''
        ns(front, "nft", "--check", "-f", "-", input=delta)
        ns(front, "nft", "-f", "-", input=delta)
        for port in PORTS:
            probe("precise-port-http-body-" + str(port), "127.0.0.1", port, True, expected_rule="gateway-original")
            probe("precise-port-connect-" + str(port), "127.0.0.1", port, True, "CONNECT", "gateway-reply")
        probe("adjacent-listening-port-denied", "127.0.0.1", 20082, False, expected_rule="reject-local")
        probe("other-listening-loopback-address-denied", "127.0.0.2", PORTS[0], False, expected_rule="reject-local")
        # Route these synthetic destinations to the isolated peer, so rejection is a
        # measured firewall verdict rather than ENETUNREACH or an absent listener.
        for address in ("169.254.169.254", "10.20.30.40"):
            ns(front, "ip", "route", "add", address + "/32", "via", UPSTREAM)
            probe("restricted-target-" + address, address, 80, False, expected_rule="reject-special")
        probe("nonweb-port-with-live-listener-denied", UPSTREAM, 81, False, expected_rule="reject-other")
        probe("old-public-web-after", UPSTREAM, 443, True, expected_rule="old-public-web")
        ns(front, sys.executable, "-c", root_api)
        evidence.append({"case": "old-api-reply-after", "passed": True})
        # Delete only the two identified fixture rules; never flush any ruleset.
        for row in rules():
            rule = row.get("rule", {})
            if rule.get("comment") in ("gateway-original", "gateway-reply"):
                ns(front, "nft", "delete", "rule", "inet", "fixture", "limited", "handle", str(rule["handle"]))
        assert rule_shape() == original_shape, "withdrawal changed original policy"
        evidence.append({"case": "withdrawal-original-rule-order-and-selectors-identical", "passed": True})
        probe("withdrawal-restores-denial", "127.0.0.1", PORTS[0], False, expected_rule="reject-local")
        probe("old-public-web-after-withdrawal", UPSTREAM, 80, True, expected_rule="old-public-web")
        ns(front, sys.executable, "-c", root_api)
        evidence.append({"case": "old-api-reply-after-withdrawal", "passed": True})
    finally:
        for process in processes:
            process.terminate()
        for process in processes:
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        for name in reversed(created):
            run("ip", "netns", "delete", name)
        if output:
            Path(output).write_text(json.dumps({"scope": "isolated network-rule fixture, no media or Internet",
                                               "namespaces_removed": True, "evidence": evidence}, indent=2) + "\n")
    print(json.dumps({"passed": True, "cases": len(evidence), "namespaces_removed": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", choices=("front", "back"))
    parser.add_argument("--client", nargs=3, metavar=("HOST", "PORT", "METHOD"))
    parser.add_argument("--output")
    arguments = parser.parse_args()
    if arguments.server:
        server(arguments.server)
    elif arguments.client:
        print(json.dumps(client(arguments.client[0], int(arguments.client[1]), arguments.client[2])))
    else:
        check_fixture(arguments.output)
