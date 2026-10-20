"""Failure recovery and publication ordering for routing-only updates."""
import contextlib
import copy
import os
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from gost_deploy import lifecycle as life, model
from test_model import candidate


class Transactions(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.cfg = model.normalize(candidate())
        self.changed = copy.deepcopy(self.cfg)
        self.changed["activeProfile"] = "mixed"
        self.state = {"schema": 2, "installed": True, "current": {"binary": "gost", "version": "3.3.0"},
                      "revision": "before", "runtime": {"gateway": "127.0.0.1:20000", "auth": {"username": "a", "password": "b"}}}
        for name in ("CONFIG", "MANIFEST", "RUNTIME_INPUT", "UNIT", "API_UNIT", "CONTROL", "STATE", "TOKEN", "CODE_LINK"):
            path = Path(self.temp.name) / name
            self.stack.enter_context(patch.object(life, name, path))
            if name == "CODE_LINK":
                path.symlink_to(self.temp.name)
            else:
                path.write_bytes(b"original-" + name.encode())
        self.stack.enter_context(patch.object(life, "validate_native", lambda *a: None))
        self.stack.enter_context(patch.object(life, "load_config", lambda: self.cfg))
        self.stack.enter_context(patch.object(life, "active", lambda: True))
        self.stack.enter_context(patch.object(life, "manager_current", lambda state: True))
        self.stack.enter_context(patch.object(life, "snapshot", lambda state: {"config": self.cfg, "release": state["current"], "active": True, "enabled": False}))
        self.stack.enter_context(patch.object(life.grp, "getgrnam", lambda name: SimpleNamespace(gr_gid=os.getgid())))
        original_atomic = life.atomic_bytes
        self.stack.enter_context(patch.object(life, "atomic_bytes", lambda p, data, mode=0o600, gid=None: original_atomic(p, data, mode)))
        self.stack.enter_context(patch.object(life, "write_managed", self.write_managed))
        self.systemctl = self.stack.enter_context(patch.object(life, "systemctl"))
        self.observed = "before"
        self.calls = []

    def write_managed(self, cfg, release, options, revision):
        for name in ("CONFIG", "MANIFEST", "RUNTIME_INPUT"):
            getattr(life, name).write_bytes(life.json_bytes(cfg))

    def rpc(self, command, **values):
        self.calls.append(command)
        if command == "commit":
            # The durable state must exist before a generation becomes visible.
            self.assertEqual(life.read_json(life.STATE)["revision"], values["revision"])
            self.observed = values["revision"]
        return {"revision": self.observed}

    def test_hot_change_never_stops_service_and_publishes_after_persistence(self):
        with patch.object(life.control, "request", self.rpc):
            life.apply(self.state, self.state["current"], self.changed)
        self.systemctl.assert_not_called()
        self.assertEqual(self.calls, ["prepare", "commit"])
        self.assertEqual(life.read_json(life.STATE)["previous"]["config"], self.cfg)

    def test_lost_commit_ack_restores_disk_and_runtime_without_restart(self):
        original = {p: p.read_bytes() for p in (life.CONFIG, life.MANIFEST, life.RUNTIME_INPUT, life.STATE)}
        failed_once = [False]
        def rpc(command, **values):
            if command == "commit" and not failed_once[0]:
                failed_once[0] = True
                self.observed = values["revision"]
                raise model.Error("lost acknowledgement")
            # Recovery commits the old snapshot after restoring its durable state.
            if command == "commit":
                self.observed = values["revision"]
            return {"revision": self.observed}
        with patch.object(life.control, "request", rpc), self.assertRaisesRegex(model.Error, "已恢复"):
            life.apply(self.state, self.state["current"], self.changed)
        self.assertEqual(self.observed, "before")
        for path, value in original.items():
            self.assertEqual(path.read_bytes(), value)
        self.systemctl.assert_not_called()
