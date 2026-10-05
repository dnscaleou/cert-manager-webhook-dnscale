#!/usr/bin/env python3
"""Verify the installed webhook against a disposable owner in a real DNScale zone.

Uses the current kubectl context (or KUBECONFIG). Does not issue certificates.
The credential Secret must already exist. No API tokens are printed or saved.
"""

import argparse
import json
import secrets
import subprocess
import urllib.parse
import urllib.request
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zone", required=True)
    parser.add_argument("--token-file", required=True, type=Path)
    parser.add_argument("--namespace", default="cert-manager")
    parser.add_argument("--service-account", default="cert-manager")
    parser.add_argument("--service-account-namespace", default="cert-manager")
    parser.add_argument("--secret", default="dnscale-api-token")
    parser.add_argument("--secret-key", default="api-token")
    parser.add_argument("--api-url", default="https://api.dnscale.eu")
    parser.add_argument("--group-name", default="acme.dnscale.eu")
    parser.add_argument("--record-name", default="_acme-challenge")
    args = parser.parse_args()
    token = args.token_file.read_text().strip()
    if not token or "\n" in token:
        raise SystemExit("The token file must contain only the API token")
    zone_name = args.zone.rstrip(".")
    fqdn = f"{args.record_name}.{zone_name}."

    def api(path):
        req = urllib.request.Request(
            args.api_url.rstrip("/") + path,
            headers={"Authorization": "Bearer " + token},
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.load(response)["data"]

    def pages(path, field):
        offset = 0
        while True:
            data = api(f"{path}?offset={offset}&limit=100")
            page = data[field]
            yield from page
            if not data.get("pagination", {}).get("has_more", len(page) == 100):
                return
            if not page:
                raise RuntimeError("Empty API page with has_more=true")
            offset += len(page)

    zone = next((z for z in pages("/v1/zones", "zones") if z["name"].rstrip(".") == zone_name), None)
    if zone is None:
        raise SystemExit("Test zone is not accessible to this API token")
    zone_id = urllib.parse.quote(zone["id"], safe="")

    def values():
        result = set()
        for record in pages(f"/v1/zones/{zone_id}/records", "records"):
            if record["name"].lower() != fqdn.lower() or record["type"] != "TXT":
                continue
            content = record["content"]
            if content.startswith('"'):
                content = json.loads(content)
            result.add(content)
        return result

    def challenge(action, key):
        payload = {
            "apiVersion": "webhook.acme.cert-manager.io/v1alpha1",
            "kind": "ChallengePayload",
            "request": {
                "uid": secrets.token_hex(12),
                "action": action,
                "type": "dns-01",
                "dnsName": zone_name,
                "key": key,
                "resourceNamespace": args.namespace,
                "resolvedFQDN": fqdn,
                "resolvedZone": zone_name + ".",
                "config": {
                    "apiUrl": args.api_url,
                    "apiTokenSecretRef": {"name": args.secret, "key": args.secret_key},
                },
            },
        }
        command = [
            "kubectl", "--as",
            f"system:serviceaccount:{args.service_account_namespace}:{args.service_account}",
            "create", "--raw", f"/apis/{args.group_name}/v1alpha1/dnscale", "-f", "-",
        ]
        proc = subprocess.run(command, input=json.dumps(payload), capture_output=True, text=True, timeout=90)
        if proc.returncode:
            raise RuntimeError(proc.stderr.strip())
        response = json.loads(proc.stdout).get("response", {})
        if not response.get("success"):
            raise RuntimeError(response.get("status", {}).get("message", "Webhook request failed"))
        print(f"{action}: success", flush=True)

    before = values()
    keys = ["webhook-audit-" + secrets.token_urlsafe(24) for _ in range(2)]
    try:
        for key in (keys[0], keys[0], keys[1]):
            challenge("Present", key)
        if values() != before | set(keys):
            raise RuntimeError("Repeated Present did not preserve both challenge values")
        print("PASS: repeated Present and simultaneous TXT values", flush=True)
        for _ in range(2):
            challenge("CleanUp", keys[0])
        if values() != before | {keys[1]}:
            raise RuntimeError("Cleanup did not preserve the sibling TXT value")
        print("PASS: repeated CleanUp preserves other TXT values", flush=True)
    finally:
        errors = []
        for key in keys:
            try:
                challenge("CleanUp", key)
            except Exception as exc:
                errors.append(str(exc))
        if values() != before:
            raise RuntimeError("Original TXT state was not restored; check the disposable test owner")
        if errors:
            raise RuntimeError("Cleanup reported errors: " + "; ".join(errors))
        print("PASS: original TXT values restored", flush=True)


if __name__ == "__main__":
    main()
