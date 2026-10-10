#!/usr/bin/env python3
"""Issue and renew a disposable normal/wildcard certificate with Let's Encrypt staging.

Requires an explicit Kubernetes context, installed webhook, cmctl, dig, and a
scoped DNScale token file. Credentials and certificate private keys are not logged.
"""

import argparse
import json
import re
import secrets
import subprocess
import time
from pathlib import Path


def authoritative_txt_absent(output):
    status = re.search(r"status: (\w+),", output)
    flags = re.search(r";; flags: ([^;]+);", output)
    if not status or status[1] not in {"NOERROR", "NXDOMAIN"}:
        return False
    if not flags or "aa" not in flags[1].split():
        return False
    # With +noall +comments +answer, any non-comment line is an answer.
    # A CNAME also prevents proving that this challenge owner is absent.
    return not any(line.strip() and not line.startswith(";") for line in output.splitlines())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", required=True)
    parser.add_argument("--zone", required=True)
    parser.add_argument("--email", required=True)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--cert-manager-namespace", default="cert-manager")
    parser.add_argument("--webhook-service-account", default="cert-manager-webhook-dnscale")
    parser.add_argument("--timeout", type=int, default=900)
    args = parser.parse_args()
    zone = args.zone.lower().rstrip(".")
    if "." not in zone or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789-." for c in zone):
        parser.error("--zone must be a disposable publicly delegated DNS zone")
    token = args.token_file.read_text().strip()
    if not token:
        parser.error("the token file is empty")

    def run(command, *, body=None):
        result = subprocess.run(command, input=body, text=True, capture_output=True, timeout=60)
        if result.returncode:
            # kubectl validation errors may include a submitted Secret. Keep all
            # command output private; inspect resource status separately on failure.
            raise RuntimeError(f"{command[0]} {command[1]} failed (exit {result.returncode})")
        return result.stdout

    def kubectl(*command, body=None):
        return run(["kubectl", "--context", args.context, *command], body=body)

    def apply(resource):
        kubectl("apply", "-f", "-", body=json.dumps(resource))

    def until(description, check):
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            if check():
                print(f"PASS: {description}", flush=True)
                return
            time.sleep(5)
        raise RuntimeError(f"timed out: {description}")

    nameservers = run(["dig", "+short", "NS", zone]).splitlines()
    if not nameservers:
        raise RuntimeError("the test zone must have public NS delegation")
    run(["cmctl", "version", "--client"])
    kubectl("wait", "apiservice/v1alpha1.acme.dnscale.eu", "--for=condition=Available", "--timeout=60s")
    name = "dnscale-check-" + secrets.token_hex(6)
    hostname = name + "." + zone
    owner = "_acme-challenge." + hostname
    cm_namespace = args.cert_manager_namespace
    metadata = {"name": name, "namespace": cm_namespace}

    def txt_absent():
        return all(authoritative_txt_absent(run([
            "dig", "+noall", "+comments", "+answer", "+time=5", "+tries=1", "@" + ns, owner, "TXT"
        ])) for ns in nameservers)

    if not txt_absent():
        raise RuntimeError("the random test owner unexpectedly exists; no changes made")
    print(f"Test namespace: {name}; test hostname: {hostname}", flush=True)
    created_namespace = False
    try:
        apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": name}})
        created_namespace = True
        apply({"apiVersion": "v1", "kind": "Secret", "metadata": metadata, "stringData": {"api-token": token}})
        apply({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata,
               "rules": [{"apiGroups": [""], "resources": ["secrets"], "resourceNames": [name], "verbs": ["get"]}]})
        apply({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata,
               "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name},
               "subjects": [{"kind": "ServiceAccount", "name": args.webhook_service_account, "namespace": cm_namespace}]})
        apply({"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": {"name": name},
               "spec": {"acme": {"email": args.email,
                   "server": "https://acme-staging-v02.api.letsencrypt.org/directory",
                   "privateKeySecretRef": {"name": name + "-account"},
                   "solvers": [{"selector": {"dnsZones": [zone]}, "dns01": {"webhook": {
                       "groupName": "acme.dnscale.eu", "solverName": "dnscale",
                       "config": {"apiTokenSecretRef": {"name": name, "key": "api-token"}}}}}]}}})
        apply({"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": {"name": name, "namespace": name},
               "spec": {"secretName": name, "issuerRef": {"name": name, "kind": "ClusterIssuer"},
                        "dnsNames": [hostname, "*." + hostname]}})

        def certificate():
            return json.loads(kubectl("get", "certificate", name, "-n", name, "-o", "json")).get("status", {})

        def ready(status):
            return any(c["type"] == "Ready" and c["status"] == "True" for c in status.get("conditions", []))

        until("normal and wildcard staging issuance", lambda: ready(certificate()))
        revision = certificate()["revision"]
        original_cert = kubectl("get", "secret", name, "-n", name, "-o", "jsonpath={.data.tls\\.crt}")
        until("issuance TXT cleanup on every authoritative nameserver", txt_absent)
        run(["cmctl", "renew", name, "--namespace", name, "--context", args.context])

        def renewed():
            status = certificate()
            if not ready(status) or status.get("revision", 0) <= revision:
                return False
            return kubectl("get", "secret", name, "-n", name, "-o", "jsonpath={.data.tls\\.crt}") != original_cert

        until("renewal with a new certificate and increased revision", renewed)
        until("renewal TXT cleanup on every authoritative nameserver", txt_absent)
    finally:
        # Keep the issuer and its credentials until cert-manager can finish DNS
        # cleanup during namespace deletion. If cleanup fails, retain them for repair.
        if created_namespace:
            kubectl("delete", "namespace", name, "--ignore-not-found", "--wait=false")
            until("test namespace cleanup", lambda: not kubectl("get", "namespace", name, "--ignore-not-found", "-o", "name").strip())
            until("final TXT cleanup", txt_absent)
        kubectl("delete", "clusterissuer", name, "--ignore-not-found")
        kubectl("delete", "role,rolebinding", name, "-n", cm_namespace, "--ignore-not-found")
        kubectl("delete", "secret", name, name + "-account", "-n", cm_namespace, "--ignore-not-found")


if __name__ == "__main__":
    main()
