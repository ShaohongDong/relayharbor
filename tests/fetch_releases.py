#!/usr/bin/env python3
"""Cache checksum-verified upstream assets used by reproducible integration tests."""
import concurrent.futures
import hashlib
from pathlib import Path
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
CACHE = ROOT / ".cache/releases"


def fetch(item):
    version, name = item
    path = CACHE / (f"checksums-{version}.txt" if name == "checksums.txt" else name)
    if not path.exists():
        request = urllib.request.Request(
            f"https://github.com/go-gost/gost/releases/download/v{version}/{name}",
            headers={"User-Agent": "gost-deploy-tests"})
        temporary = path.with_suffix(".partial")
        try:
            with urllib.request.urlopen(request, timeout=30) as response, temporary.open("wb") as output:
                while block := response.read(1024 * 1024):
                    output.write(block)
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)
    return path


def main():
    CACHE.mkdir(parents=True, exist_ok=True)
    items = [(version, name) for version in ("3.3.0", "3.2.6") for name in (
        "checksums.txt", f"gost_{version}_linux_amd64.tar.gz", f"gost_{version}_linux_arm64.tar.gz")]
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(fetch, items))
    for version in ("3.3.0", "3.2.6"):
        checksums = dict(line.split()[::-1] for line in (CACHE / f"checksums-{version}.txt").read_text().splitlines())
        for arch in ("amd64", "arm64"):
            name = f"gost_{version}_linux_{arch}.tar.gz"
            assert hashlib.sha256((CACHE / name).read_bytes()).hexdigest() == checksums[name], name
            print("verified", name)


if __name__ == "__main__":
    main()
