#!/usr/bin/env python3
"""Bind integration results to an unchanged source snapshot."""
import datetime
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
RESULTS = ROOT / "test-results"
BASELINE = RESULTS / "source-baseline.json"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sources():
    files = [ROOT / name for name in ("gostctl", "install.sh", "README.md", ".gitignore", ".dockerignore")]
    for directory in ("tests", "examples", ".github"):
        files += [p for p in (ROOT / directory).rglob("*") if p.is_file() and "__pycache__" not in p.parts]
    return {str(p.relative_to(ROOT)): sha(p) for p in sorted(files)}


if sys.argv[1] == "start":
    BASELINE.write_text(json.dumps(sources(), indent=2) + "\n")
elif sys.argv[1] == "finish":
    baseline = json.loads(BASELINE.read_text())
    assert baseline == sources(), "source changed during tests; rerun against final sources"
    matrix = {}
    for name in ("ubuntu-22-04", "ubuntu-24-04", "debian-12-slim", "debian-13-slim"):
        path = RESULTS / f"{name}.log"
        content = path.read_text()
        summary = [json.loads(line) for line in content.splitlines() if line.startswith('{"passed":')]
        assert len(summary) == 1 and summary[0]["passed"] == 17, name
        assert "PASS automatic service startup after container reboot, TCP/UDP relay" in content, name
        arch = subprocess.check_output(["docker", "image", "inspect", f"gost-deploy-test:{name}",
                                        "--format", "{{.Architecture}}"], text=True).strip()
        matrix[name] = {"architecture": arch, "scenarios": summary[0]["passed"],
                        "container_reboot": True, "log_sha256": sha(path)}
    arm64 = {}
    for name in ("ubuntu-24-04", "debian-12-slim"):
        path = RESULTS / f"{name}-arm64-qemu.log"
        assert len([line for line in path.read_text().splitlines() if line.startswith("PASS arm64 QEMU")]) == 4, name
        arm64[name] = {"emulator": "qemu-aarch64-static", "cases": 4, "log_sha256": sha(path)}
    report = {"created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "sources": baseline, "systemd_containers": matrix, "arm64_protocol_emulation": arm64}
    (RESULTS / "evidence.json").write_text(json.dumps(report, indent=2) + "\n")
    print("PASS source-bound evidence: test-results/evidence.json")
else:
    raise AssertionError("use start or finish")
