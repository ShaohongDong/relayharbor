#!/usr/bin/env python3
"""Actual ARM64 GOST under QEMU, inside a normal isolated container."""
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile
import time

from protocol import exercise


def main():
    assert Path("/.dockerenv").exists() and os.environ.get("GOST_DEPLOY_TEST") == "1"
    for version in ("3.3.0", "3.2.6"):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            filename = f"gost_{version}_linux_arm64.tar.gz"
            archive = Path("/artifacts") / filename
            sums = dict(line.split()[::-1] for line in Path(f"/artifacts/checksums-{version}.txt").read_text().splitlines())
            assert hashlib.sha256(archive.read_bytes()).hexdigest() == sums[filename]
            with tarfile.open(archive) as tar, tar.extractfile("gost") as source:
                binary = tmp / "gost"
                binary.write_bytes(source.read())
                binary.chmod(0o755)
            reported = subprocess.check_output(["/qemu-aarch64-static", binary, "-V"], text=True)
            assert reported.split()[1].removeprefix("v") == version and "arm64" in reported, reported
            for udp in (False, True):
                with socket.socket() as reserve:
                    reserve.bind(("127.0.0.1", 0))
                    port = reserve.getsockname()[1]
                cfg = {"services": [{"name": "socks5", "addr": f"127.0.0.1:{port}",
                        "handler": {"type": "socks5", "auther": "users", "metadata": {"udp": udp, "notls": True}},
                        "listener": {"type": "tcp"}}],
                       "authers": [{"name": "users", "auths": [{"username": "arm64", "password": "special :$() 😀"}]}]}
                config = tmp / "gost.json"
                config.write_text(json.dumps(cfg))
                with (tmp / "runtime.log").open("w") as log:
                    process = subprocess.Popen(["/qemu-aarch64-static", binary, "-C", config], stdout=log, stderr=log)
                    try:
                        for _ in range(100):
                            assert process.poll() is None, "ARM64 GOST exited"
                            try:
                                with socket.create_connection(("127.0.0.1", port), 0.1):
                                    break
                            except OSError:
                                time.sleep(0.1)
                        else:
                            raise AssertionError("ARM64 startup timeout")
                        exercise(cfg, udp=udp)
                    finally:
                        process.terminate()
                        try:
                            process.wait(5)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                print(f"PASS arm64 QEMU {version} authentication/domain/TCP UDP={udp}", flush=True)

    subprocess.run([sys.executable, str(Path(__file__).with_name("runtime_integration.py")),
                    "/artifacts/gost_3.3.0_linux_arm64.tar.gz", "--arch", "arm64",
                    "--qemu", "/qemu-aarch64-static"], check=True)


if __name__ == "__main__":
    main()
