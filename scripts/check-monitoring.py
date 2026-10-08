#!/usr/bin/env python3
"""Read-only checks of live monitoring. Never print response bodies or secrets."""
import argparse
import json
import math
import sys
import urllib.parse
import urllib.request

REQUIRED_RULES = {
    "HankCloudScrapeMissing", "HankAlertmanagerScrapeMissing",
    "HankAlertDeliveryUnconfigured", "HankHostMetricsMissing",
    "HankAgentOffline", "HankFileJobsRequireRollback", "HankDiskUsageHigh",
}


def fetch(base_url, path, query=None):
    url = base_url.rstrip("/") + path
    if query is not None:
        url += "?" + urllib.parse.urlencode({"query": query})
    with urllib.request.urlopen(url, timeout=5) as response:
        body = response.read(2_000_001)
    if len(body) > 2_000_000:
        raise ValueError("oversized monitoring response")
    payload = json.loads(body)
    if payload.get("status") != "success":
        raise ValueError("monitoring API failure")
    return payload["data"]


def positive_vector(data):
    if data.get("resultType") != "vector" or not data.get("result"):
        return False
    values = [float(item["value"][1]) for item in data["result"]]
    return all(math.isfinite(value) and value > 0 for value in values)


def check(base_url, get=fetch):
    failures = []
    queries = [(f"{job} scrape is healthy", f'up{{job="{job}"}}')
               for job in ("hank-cloud", "prometheus", "alertmanager", "hank-host")]
    queries += [
        ("Alertmanager has an active delivery integration",
         'alertmanager_integrations{job="alertmanager"}'),
        ("host root filesystem metrics are present",
         'node_filesystem_size_bytes{job="hank-host",mountpoint="/"}'),
        ("host root filesystem availability is reported",
         'count(node_filesystem_avail_bytes{job="hank-host",mountpoint="/"})'),
        ("primary-agent metric is present",
         'count(hank_primary_agent_online{job="hank-cloud"})'),
    ]
    for description, query in queries:
        try:
            ok = positive_vector(get(base_url, "/api/v1/query", query))
        except (OSError, ValueError, KeyError, TypeError, IndexError):
            ok = False
        if not ok:
            failures.append(description)
    try:
        groups = get(base_url, "/api/v1/rules")["groups"]
        rules = [rule for group in groups for rule in group["rules"]]
        if not REQUIRED_RULES.issubset({rule["name"] for rule in rules}):
            failures.append("required alert rules are loaded")
        if not rules or any(rule.get("health") != "ok" for rule in rules):
            failures.append("alert rules evaluate successfully")
    except (OSError, ValueError, KeyError, TypeError):
        failures.append("alert rules are readable")
    return failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prometheus-url", default="http://127.0.0.1:9090")
    args = parser.parse_args()
    failures = check(args.prometheus_url)
    for failure in failures:
        print(f"[monitoring] fail: {failure}", file=sys.stderr)
    if failures:
        return 1
    print("[monitoring] healthy scrapes, filesystem metrics, rules, and configured delivery")
    print("[monitoring] actual recipient delivery still requires a test alert")
    return 0


if __name__ == "__main__":
    sys.exit(main())
