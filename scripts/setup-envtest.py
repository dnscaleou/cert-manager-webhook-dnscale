#!/usr/bin/env python3
"""Download pinned upstream envtest binaries, verify SHA-512, print their path."""

import hashlib
import io
import platform
import sys
import tarfile
import tempfile
import time
from pathlib import Path
from urllib.error import URLError
from urllib.request import urlopen


def download(url):
    for attempt in range(3):
        try:
            with urlopen(url, timeout=60) as response:
                return response.read()
        except (URLError, TimeoutError, ConnectionError):
            if attempt == 2:
                raise
            print("envtest download interrupted; retrying", file=sys.stderr)
            time.sleep(2 ** attempt)


def main():
    version = "1.35.0"
    operating_system = platform.system().lower()
    architecture = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    if operating_system not in {"linux", "darwin"} or architecture is None:
        raise SystemExit("envtest setup supports Linux/macOS on AMD64 or ARM64")
    root = Path(__file__).resolve().parents[1] / ".cache" / "envtest"
    target = root / f"{version}-{operating_system}-{architecture}"
    binaries = ("etcd", "kube-apiserver", "kubectl")
    if not all((target / binary).is_file() for binary in binaries):
        filename = f"envtest-v{version}-{operating_system}-{architecture}.tar.gz"
        url = f"https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v{version}/{filename}"
        expected = download(url + ".sha512").decode().split()[0]
        archive = download(url)
        if hashlib.sha512(archive).hexdigest() != expected:
            raise SystemExit("envtest archive checksum mismatch")
        root.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=root) as temporary:
            stage = Path(temporary)
            # Copy only the three expected regular files; never extract archive
            # paths, symlinks, or arbitrary files onto the host filesystem.
            with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:
                for binary in binaries:
                    matches = [m for m in bundle.getmembers() if m.isfile() and Path(m.name).name == binary]
                    if len(matches) != 1:
                        raise SystemExit(f"envtest archive must contain exactly one {binary}")
                    with bundle.extractfile(matches[0]) as source:
                        (stage / binary).write_bytes(source.read())
                    (stage / binary).chmod(0o755)
            target.mkdir(exist_ok=True)
            for binary in binaries:
                (stage / binary).replace(target / binary)
    print(target)


if __name__ == "__main__":
    main()
