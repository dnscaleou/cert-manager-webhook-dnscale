#!/usr/bin/env python3
"""Verify public GHCR manifests without credentials (including both image CPUs)."""

import hashlib
import json
import sys
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def manifest(package, version):
    query = urlencode({"service": "ghcr.io", "scope": f"repository:{package}:pull"})
    with urlopen(f"https://ghcr.io/token?{query}", timeout=30) as response:
        token = json.load(response)["token"]
    request = Request(
        f"https://ghcr.io/v2/{package}/manifests/{version}",
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": ", ".join([
                "application/vnd.oci.image.index.v1+json",
                "application/vnd.docker.distribution.manifest.list.v2+json",
                "application/vnd.oci.image.manifest.v1+json",
                "application/vnd.docker.distribution.manifest.v2+json",
            ]),
        },
    )
    with urlopen(request, timeout=30) as response:
        body = response.read()
    digest = "sha256:" + hashlib.sha256(body).hexdigest()
    print(f"Public artifact: {package}:{version} ({digest})")
    return json.loads(body), digest


def main():
    if len(sys.argv) not in (2, 3):
        sys.exit("Usage: verify-release.py vX.Y.Z [expected-image-digest]")
    tag = sys.argv[1]
    image, digest = manifest("dnscaleou/cert-manager-webhook-dnscale", tag)
    if len(sys.argv) == 3 and digest != sys.argv[2]:
        sys.exit("Published image digest differs from the build output")
    platforms = {
        (entry.get("platform", {}).get("os"), entry.get("platform", {}).get("architecture"))
        for entry in image.get("manifests", [])
    }
    if not {("linux", "amd64"), ("linux", "arm64")}.issubset(platforms):
        sys.exit("Published image must contain Linux AMD64 and ARM64 manifests")
    chart, _ = manifest("dnscaleou/charts/cert-manager-webhook-dnscale", tag.removeprefix("v"))
    if chart.get("config", {}).get("mediaType") != "application/vnd.cncf.helm.config.v1+json":
        sys.exit("Published chart is not a Helm OCI artifact")


if __name__ == "__main__":
    main()
