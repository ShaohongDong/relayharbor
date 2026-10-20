"""Policy tests for untrusted configuration, not snapshots of generated JSON."""
import copy
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
from gost_deploy import lifecycle as g



class Policy(unittest.TestCase):
    def setUp(self):
        self.cfg = g.initial_config("0.0.0.0:1080", False)

    def rejects(self, change):
        cfg = copy.deepcopy(self.cfg)
        change(cfg)
        with self.assertRaises(g.Error):
            g.validate(cfg)

    def test_required_authenticator(self):
        self.rejects(lambda c: c["services"][0]["handler"].update(auther="missing"))
        self.rejects(lambda c: c["authers"][0].update(auths=[]))

    def test_no_auth_bypass_or_management_services(self):
        self.rejects(lambda c: c["services"][0]["handler"]["metadata"].update(skipauth="0.0.0.0/0"))
        self.rejects(lambda c: c.update(api={"addr": ":18080"}))
        self.rejects(lambda c: c["services"].append(copy.deepcopy(c["services"][0])))
        self.rejects(lambda c: c["services"][0]["handler"].update(auth={"username": "x", "password": "x"}))

    def test_empty_and_duplicate_credentials(self):
        for field in ("username", "password"):
            self.rejects(lambda c: c["authers"][0]["auths"][0].update({field: ""}))
        self.rejects(lambda c: c["authers"][0]["auths"].append(copy.deepcopy(c["authers"][0]["auths"][0])))

    def test_wire_limits_and_special_characters(self):
        auth = self.cfg["authers"][0]["auths"][0]
        auth.update(username="用户@x", password="space :@$() ' \" \\ 😀")
        g.validate(self.cfg)
        auth["password"] = "é" * 128
        with self.assertRaises(g.Error):
            g.validate(self.cfg)
        for value in ("a\nb", "a\x00b", "CHANGE_ME_password", "\ud800"):
            auth["password"] = value
            with self.assertRaises(g.Error):
                g.validate(self.cfg)

    def test_address_port_and_boolean_validation(self):
        for addr in ("example.com:1080", ":1080", "127.0.0.1:80", "0.0.0.0:65536", "::1:1080"):
            self.rejects(lambda c: c["services"][0].update(addr=addr))
        g.validate(g.initial_config("[::1]:1080", True))
        self.rejects(lambda c: c["services"][0]["handler"]["metadata"].update(udp="false"))
        self.rejects(lambda c: c["services"][0]["handler"]["metadata"].update(notls=False))

    def test_duplicate_json_keys_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "duplicate.json"
            path.write_text('{"services": [], "services": []}')
            with self.assertRaises(g.Error):
                g.read_json(path)

    def test_init_permissions_randomness_no_overwrite_or_password_output(self):
        with tempfile.TemporaryDirectory() as temp:
            one, two = (Path(temp) / name for name in ("one.json", "two.json"))
            result = subprocess.run([str(ROOT / "gostctl"), "init", "--output", str(one)],
                                    capture_output=True, text=True, check=True)
            subprocess.run([str(ROOT / "gostctl"), "init", "--output", str(two)],
                           capture_output=True, check=True)
            password = g.credentials(g.read_json(one))[0]["password"]
            self.assertNotIn(password, result.stdout + result.stderr)
            self.assertEqual(one.stat().st_mode & 0o777, 0o600)
            self.assertNotEqual(password, g.credentials(g.read_json(two))[0]["password"])
            before = one.read_bytes()
            again = subprocess.run([str(ROOT / "gostctl"), "init", "--output", str(one)], capture_output=True)
            self.assertNotEqual(again.returncode, 0)
            self.assertEqual(one.read_bytes(), before)

    def test_example_cannot_be_installed(self):
        with self.assertRaises(g.Error):
            g.validate(g.read_json(ROOT / "examples/socks5.json"))

    def test_version_argument_with_python38(self):
        for value in ("3.3.0", "v3.3.0"):
            self.assertEqual(g.version_arg(value), "3.3.0")
        for value in ("vv3.3.0", "latest", "3.3.0-rc1"):
            with self.assertRaises(g.argparse.ArgumentTypeError):
                g.version_arg(value)

    def test_release_verification_with_python38(self):
        with tempfile.TemporaryDirectory() as temp:
            releases = Path(temp) / "releases"
            binary = releases / "3.3.0" / "gost"
            binary.parent.mkdir(parents=True)
            binary.write_bytes(b"test binary")
            release = {"binary": str(binary), "sha256": g.digest(binary)}
            with patch.object(g, "RELEASES", releases):
                g.verify_release(release)
                binary.write_bytes(b"changed")
                with self.assertRaises(g.Error):
                    g.verify_release(release)
                outside = Path(temp) / "outside"
                outside.write_bytes(b"test binary")
                with self.assertRaises(g.Error):
                    g.verify_release(dict(release, binary=str(outside)))


if __name__ == "__main__":
    unittest.main()
