#!/usr/bin/env python3
"""Check actual systemd boot activation across a full disposable container restart."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

from protocol import auth_connect, exercise

assert Path("/.dockerenv").exists() and os.environ.get("GOST_DEPLOY_TEST") == "1"
if sys.argv[1] == "prepare":
    arch = subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()
    subprocess.run(["/src/gostctl", "init", "--output", "/root/boot.local.json", "--udp"], check=True)
    subprocess.run(["/src/install.sh", "--config", "/root/boot.local.json",
                    "--archive", f"/artifacts/gost_3.3.0_linux_{arch}.tar.gz",
                    "--checksums", "/artifacts/checksums-3.3.0.txt"], check=True)
    print("PASS Bash installer and prepare enabled service for reboot", flush=True)
elif sys.argv[1] == "verify":
    cfg = json.loads(Path("/etc/gost/gost.json").read_text())
    for _ in range(100):
        try:
            with auth_connect("127.0.0.1", 1080, cfg["authers"][0]["auths"][0]):
                break
        except OSError:
            time.sleep(0.1)
    else:
        raise AssertionError("GOST did not auto-start at container boot")
    subprocess.run(["/usr/local/sbin/gostctl", "status"], check=True)
    exercise(cfg, udp=True)
    subprocess.run(["/usr/local/sbin/gostctl", "uninstall", "--purge"], check=True)
    print("PASS automatic service startup after container reboot, TCP/UDP relay", flush=True)
else:
    raise AssertionError("invalid boot test phase")
