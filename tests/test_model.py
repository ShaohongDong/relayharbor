"""Policy and secret-handling tests for the shared control-plane model."""
import copy
import unittest

from gost_deploy import model, native
from gost_deploy.lifecycle import initial_config


def candidate():
    cfg = model.normalize(initial_config("127.0.0.1:1080", True))
    cfg["profiles"].append({"name": "mixed", "type": "pool", "nodes": [
        {"id": "a", "type": "http", "addr": "proxy.example:80", "auth": {"username": "up", "password": "private"}},
        {"id": "b", "type": "socks5", "addr": "[::1]:1080"}],
        "probe": {"type": "tcp", "addr": "target.example:443"}})
    return cfg


class ModelTests(unittest.TestCase):
    def test_legacy_import_and_native_generation(self):
        cfg = model.normalize(candidate())
        self.assertEqual(cfg["schemaVersion"], 2)
        self.assertEqual(cfg["profiles"][1]["probe"]["interval"], 30)
        options = {"gateway": "127.0.0.1:20000", "auth": {"username": "internal", "password": "secret"}}
        front = native.frontend(cfg, options)
        self.assertTrue(front["services"][0]["handler"]["metadata"]["udp"])
        for n in cfg["profiles"][1]["nodes"]:
            worker = native.worker("127.0.0.1:20001", options["auth"], n)
            self.assertFalse(worker["services"][0]["handler"]["metadata"]["udp"])
            self.assertEqual(len(worker["chains"][0]["hops"][0]["nodes"]), 1)

    def test_rejects_unsafe_or_incomplete_config(self):
        changes = [lambda c: c.update(activeProfile="missing"),
                   lambda c: c["profiles"].append(copy.deepcopy(c["profiles"][0])),
                   lambda c: c["profiles"][1].update(nodes=[]),
                   lambda c: c["profiles"][1]["nodes"][0].update(type="https"),
                   lambda c: c["profiles"][1]["nodes"][0].update(addr="proxy.example:0"),
                   lambda c: c["profiles"][1].update(probe={"type": "cmd", "command": "true"}),
                   lambda c: c["profiles"][1]["probe"].update(timeout=True),
                   lambda c: c["profiles"][1]["probe"].update(interval=float("inf")),
                   lambda c: c["listener"].update(users=[]),
                   lambda c: c["listener"].update(addr="proxy.example:1080"),
                   lambda c: c.update(api={"addr": "0.0.0.0:18080"})]
        for change in changes:
            cfg = candidate()
            change(cfg)
            with self.subTest(change=change), self.assertRaises(model.Error):
                model.normalize(cfg)

    def test_redacted_roundtrip_and_stable_identity(self):
        cfg = model.normalize(candidate())
        redacted = model.redact(cfg)
        self.assertNotIn("password", redacted["listener"]["users"][0])
        self.assertNotIn("password", redacted["profiles"][1]["nodes"][0]["auth"])
        self.assertEqual(model.merge_secrets(redacted, cfg), cfg)
        renamed = copy.deepcopy(redacted)
        renamed["profiles"][1]["nodes"][0]["id"] = "new-identity"
        with self.assertRaises(model.Error):
            model.merge_secrets(renamed, cfg)
        renamed = copy.deepcopy(redacted)
        renamed["listener"]["users"][0]["username"] = "different-user"
        with self.assertRaises(model.Error):
            model.merge_secrets(renamed, cfg)
        redacted["profiles"][1]["nodes"][0]["auth"] = None
        self.assertIsNone(model.merge_secrets(redacted, cfg)["profiles"][1]["nodes"][0]["auth"])

    def test_http_probe_policy(self):
        cfg = candidate()
        for url in ("http://probe.example/ready", "https://[::1]:443/ready?ok=1"):
            cfg["profiles"][1]["probe"] = {"type": "http", "url": url}
            self.assertEqual(model.normalize(cfg)["profiles"][1]["probe"]["expectedStatus"], 200)
        for url in ("ftp://example.com/", "http://u:p@example.com/", "http://example.com/#fragment", "http://example.com:0/", "http://例子.test/"):
            cfg["profiles"][1]["probe"] = {"type": "http", "url": url}
            with self.subTest(url=url), self.assertRaises(model.Error):
                model.normalize(cfg)
