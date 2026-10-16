#!/usr/bin/env python3
"""Run INSIDE a disposable systemd container, never on the host."""
import copy
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time

from protocol import auth_connect, exercise

ROOT = Path(__file__).resolve().parents[1]
CTL = str(ROOT / "gostctl")
CACHE = Path("/artifacts")
RESULTS = []


def command(*args, ok=True):
    result = subprocess.run([CTL, *map(str, args)], text=True, capture_output=True)
    if (result.returncode == 0) != ok:
        raise AssertionError(f"{args[0]} rc={result.returncode}: {result.stdout} {result.stderr}")
    return result


def record(name):
    RESULTS.append(name)
    print("PASS", name, flush=True)


def config(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)


def state_bytes():
    return (Path("/etc/gost/gost.json").read_bytes(),
            Path("/var/lib/gost-deploy/state.json").read_bytes(),
            os.readlink("/usr/local/bin/gost"))


def main():
    assert Path("/.dockerenv").exists() and os.environ.get("GOST_DEPLOY_TEST") == "1", "isolated Docker only"
    with open("/etc/hosts", "a") as hosts:
        hosts.write("\n127.0.0.1 gost-target.test\n")
    arch = subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()
    offline = ["--archive", CACHE / f"gost_3.3.0_linux_{arch}.tar.gz", "--checksums", CACHE / "checksums-3.3.0.txt"]
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        candidate = tmp / "gost.json"
        command("init", "--output", candidate)
        cfg = json.loads(candidate.read_text())
        cfg["authers"][0]["auths"].append({"username": "用户@x", "password": "special :$()'\"\\ 😀"})
        config(candidate, cfg)
        command("check", "--config", candidate)
        loader = importlib.machinery.SourceFileLoader("gostctl_test", CTL)
        spec = importlib.util.spec_from_loader(loader.name, loader)
        module = importlib.util.module_from_spec(spec)
        loader.exec_module(module)
        # Refuse unrelated binary/account/unit without touching their contents.
        foreign = Path("/usr/local/bin/gost")
        foreign.write_text("unrelated")
        command("install", "--config", candidate, *offline, ok=False)
        assert foreign.read_text() == "unrelated"
        foreign.unlink()
        subprocess.run(["useradd", "--system", "--user-group", "gost"], check=True)
        command("install", "--config", candidate, *offline, ok=False)
        subprocess.run(["userdel", "gost"], check=True)
        subprocess.run(["groupdel", "gost"], capture_output=True)
        foreign_unit = Path("/etc/systemd/system/gost.service")
        foreign_unit.write_text("[Service]\nExecStart=/bin/true\n")
        command("install", "--config", candidate, *offline, ok=False)
        assert foreign_unit.read_text().endswith("ExecStart=/bin/true\n")
        foreign_unit.unlink()
        subprocess.run(["systemctl", "daemon-reload"], check=True)
        record("foreign binary/account/unit conflicts preserved")
        # A bad archive must never create an active service.
        corrupt = tmp / "corrupt.tar.gz"
        corrupt.write_bytes(b"not an archive")
        command("install", "--config", candidate, "--archive", corrupt, "--checksums", offline[-1], ok=False)
        assert not Path("/etc/systemd/system/gost.service").exists()
        record("checksum failure before service creation")
        # Exercise cleanup after first installation reaches systemd but cannot execute.
        failing = tmp / "failing-gost"
        failing.write_text('#!/bin/sh\nif [ "$1" = "-C" ] && [ "$3" = "-O" ]; then exit 0; fi\nexit 42\n')
        failing.chmod(0o755)
        failed_release = {"binary": str(failing), "version": "3.99.0", "arch": arch, "sha256": "unused-test-value"}
        try:
            module.apply(None, failed_release, cfg, fresh=True)
        except module.Error as exc:
            assert "已撤销" in str(exc), exc
        else:
            raise AssertionError("first-install failure was not detected")
        assert not Path("/etc/systemd/system/gost.service").exists()
        assert not Path("/etc/gost/gost.json").exists()
        assert not Path("/usr/local/bin/gost").exists()
        assert not Path("/usr/local/sbin/gostctl").exists()
        assert subprocess.run(["id", "gost"], capture_output=True).returncode != 0
        record("first-install runtime failure removes new service/config/account")
        command("install", "--config", candidate, *offline)
        before = state_bytes()
        pid = subprocess.check_output(["systemctl", "show", "gost", "-p", "MainPID", "--value"])
        command("install", "--config", candidate, *offline)
        assert state_bytes() == before
        assert subprocess.check_output(["systemctl", "show", "gost", "-p", "MainPID", "--value"]) == pid
        record("install, enabled startup and idempotence")
        command("rollback", ok=False)
        assert state_bytes() == before
        assert Path("/etc/gost/gost.json").stat().st_mode & 0o777 == 0o640
        uid = int(subprocess.check_output(["id", "-u", "gost"]))
        assert uid != 0
        status = json.loads(command("status").stdout)
        assert status["active"] and status["enabled"]
        subprocess.run(["systemd-analyze", "verify", "/etc/systemd/system/gost.service"], check=True)
        # Installed CLI survives removal of dependence on source entry point.
        subprocess.run(["/usr/local/sbin/gostctl", "status"], check=True)
        exercise(cfg)
        logs = command("logs")
        for auth in cfg["authers"][0]["auths"]:
            assert auth["password"] not in logs.stdout + logs.stderr
        record("multiple users, special credentials, anonymous/wrong-password rejection, DNS and TCP; UDP disabled")
        for action in ("stop", "start", "restart"):
            command(action)
        exercise(cfg)
        subprocess.run(["systemctl", "is-enabled", "--quiet", "gost"], check=True)
        record("start/stop/restart and startup enablement")
        old_pid = subprocess.check_output(["systemctl", "show", "gost", "-p", "MainPID", "--value"]).strip()
        subprocess.run(["systemctl", "kill", "--signal=SIGKILL", "--kill-who=main", "gost"], check=True)
        for _ in range(80):
            new_pid = subprocess.check_output(["systemctl", "show", "gost", "-p", "MainPID", "--value"]).strip()
            if new_pid not in (b"0", old_pid):
                break
            time.sleep(0.1)
        else:
            raise AssertionError("systemd did not restart failed GOST")
        for _ in range(80):
            try:
                with auth_connect("127.0.0.1", 1080, cfg["authers"][0]["auths"][0]):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise AssertionError("restarted GOST did not become ready")
        exercise(cfg)
        record("systemd automatically restarts killed process")
        changed = copy.deepcopy(cfg)
        changed["services"][0]["handler"]["metadata"]["udp"] = True
        config(candidate, changed)
        command("configure", "--config", candidate)
        exercise(changed, udp=True)
        record("configuration update and actual UDP relay")
        command("rollback")
        assert json.loads(Path("/etc/gost/gost.json").read_text()) == cfg
        exercise(cfg)
        record("configuration rollback")
        before = state_bytes()
        invalid = copy.deepcopy(cfg)
        invalid["services"][0]["handler"]["auther"] = "does-not-exist"
        config(candidate, invalid)
        command("configure", "--config", candidate, ok=False)
        assert state_bytes() == before
        record("invalid configuration preserves live service")
        with socket.socket() as occupied:
            occupied.bind(("0.0.0.0", 11080))
            occupied.listen()
            bad_port = copy.deepcopy(cfg)
            bad_port["services"][0]["addr"] = "0.0.0.0:11080"
            config(candidate, bad_port)
            command("configure", "--config", candidate, ok=False)
            assert state_bytes() == before
        record("occupied port preserves live service")
        # Test failure AFTER replacement using a real systemd start and a candidate binary
        # which passes -V/-O but fails runtime startup. Inject only inside the test process.
        state = module.load_state()
        release = dict(state["current"], binary=str(failing), version="3.99.0")
        try:
            module.apply(state, release, cfg)
        except module.Error as exc:
            assert "已恢复" in str(exc), exc
        else:
            raise AssertionError("candidate failure was not detected")
        assert state_bytes() == before
        exercise(cfg)
        record("actual systemd startup failure restores binary/config/state")
        # Stopped and disabled services must remain so after a successful update.
        command("stop")
        subprocess.run(["systemctl", "disable", "gost"], check=True, capture_output=True)
        config(candidate, changed)
        command("configure", "--config", candidate)
        stopped = json.loads(command("status", ok=False).stdout)
        assert not stopped["active"] and not stopped["enabled"]
        command("rollback")
        stopped = json.loads(command("status", ok=False).stdout)
        assert not stopped["active"] and not stopped["enabled"]
        command("start")
        subprocess.run(["systemctl", "enable", "gost"], check=True, capture_output=True)
        record("configuration/rollback preserve stopped and disabled state")
        # Real release transition, then explicit binary rollback.
        config(candidate, changed)
        command("configure", "--config", candidate)
        old = ["--archive", CACHE / f"gost_3.2.6_linux_{arch}.tar.gz", "--checksums", CACHE / "checksums-3.2.6.txt"]
        command("upgrade", "--version", "3.2.6", *old)
        assert json.loads(command("status").stdout)["version"] == "3.2.6"
        exercise(changed, udp=True)
        command("rollback")
        assert json.loads(command("status").stdout)["version"] == "3.3.0"
        command("upgrade", "--version", "3.2.6", *old)
        command("upgrade", "--version", "3.3.0", *offline)
        record("real 3.3.0/3.2.6 release transitions and binary rollback")
        before = state_bytes()
        # Network failure is made deterministic without contacting a bogus external endpoint.
        original_download = module.download
        module.download = lambda *a: (_ for _ in ()).throw(module.Error("download failed"))
        try:
            try:
                module.prepare_release("3.99.0")
            except module.Error:
                pass
            else:
                raise AssertionError("download failure not propagated")
        finally:
            module.download = original_download
        assert state_bytes() == before
        record("download failure leaves live installation unchanged")
        # A systemd drop-in is an external override and must prevent mutation.
        drop = Path("/etc/systemd/system/gost.service.d")
        drop.mkdir()
        (drop / "external.conf").write_text("[Service]\nEnvironment=EXTERNAL=1\n")
        subprocess.run(["systemctl", "daemon-reload"], check=True)
        command("configure", "--config", candidate, ok=False)
        (drop / "external.conf").unlink()
        drop.rmdir()
        subprocess.run(["systemctl", "daemon-reload"], check=True)
        assert state_bytes() == before
        record("external systemd drop-in preserved")
        command("uninstall")
        assert Path("/etc/gost/gost.json").exists()
        assert not Path("/usr/local/bin/gost").exists()
        config(candidate, changed)
        command("install", "--config", candidate, *offline)
        exercise(changed, udp=True)
        command("uninstall", "--purge")
        assert not Path("/etc/gost/gost.json").exists()
        assert not Path("/var/lib/gost-deploy").exists()
        assert not Path("/usr/local/sbin/gostctl").exists()
        assert subprocess.run(["id", "gost"], capture_output=True).returncode != 0
        record("retaining uninstall, reinstall and complete purge")
    print(json.dumps({"passed": len(RESULTS), "scenarios": RESULTS}, ensure_ascii=False))


if __name__ == "__main__":
    main()
