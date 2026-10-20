"""Exercise the real HTTP boundary with isolated lifecycle dependencies."""
import contextlib
import copy
import json
import threading
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

from gost_deploy import api, model
from test_model import candidate


class APITests(unittest.TestCase):
    def setUp(self):
        self.cfg = model.normalize(candidate())
        self.state = {"schema": 2, "revision": "revision-one", "current": {}}
        self.server = api.Server(("127.0.0.1", 0), "test-secret-token")
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.stack = contextlib.ExitStack()
        self.stack.enter_context(patch.object(api.life, "locked", contextlib.nullcontext))
        self.stack.enter_context(patch.object(api.life, "load_state", lambda: self.state))
        self.stack.enter_context(patch.object(api.life, "managed", lambda state: None))
        self.stack.enter_context(patch.object(api.life, "load_config", lambda: copy.deepcopy(self.cfg)))
        self.stack.enter_context(patch.object(api.life, "preflight_port", lambda *args: None))
        self.stack.enter_context(patch.object(api.life, "safe_status", lambda state: {"revision": state["revision"], "activeProfile": self.cfg["activeProfile"]}))
        def apply(state, release, cfg, **kwargs):
            self.cfg = cfg
            self.state["revision"] = "revision-two"
        self.stack.enter_context(patch.object(api.life, "apply", apply))

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.stack.close()

    def call(self, method, path, value=None, token="test-secret-token", revision=None, raw=None):
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + token}
        if revision:
            headers["If-Match"] = '"' + revision + '"'
        data = raw if raw is not None else json.dumps(value).encode() if value is not None else None
        request = urllib.request.Request("http://127.0.0.1:{}{}{}".format(self.server.server_port, api.PREFIX, path),
                                         data=data, headers=headers, method=method)
        try:
            response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, dict(response.headers), json.loads(response.read())

    def test_authentication_redaction_and_etag(self):
        self.assertEqual(self.call("GET", "/config", token="wrong")[0], 401)
        status, headers, result = self.call("GET", "/config")
        self.assertEqual(status, 200)
        self.assertEqual(headers["ETag"], '"revision-one"')
        self.assertNotIn("private", json.dumps(result))
        self.assertEqual(self.call("GET", "/config?token=test-secret-token")[0], 404)

    def test_revision_and_switch_acknowledgement(self):
        self.assertEqual(self.call("PUT", "/active-profile", {"name": "mixed"})[0], 428)
        self.assertEqual(self.call("PUT", "/active-profile", {"name": "mixed"}, revision="stale")[0], 409)
        status, headers, result = self.call("PUT", "/active-profile", {"name": "mixed"}, revision="revision-one")
        self.assertEqual(status, 200)
        self.assertEqual(result["revision"], "revision-two")
        self.assertEqual(result["data"]["activeProfile"], "mixed")

    def test_validation_never_mutates_and_redacted_update_keeps_passwords(self):
        redacted = model.redact(self.cfg)
        self.assertEqual(self.call("POST", "/config/validate", redacted)[0], 200)
        self.assertEqual(self.state["revision"], "revision-one")
        redacted["activeProfile"] = "mixed"
        self.assertEqual(self.call("PUT", "/config", redacted, revision="revision-one")[0], 200)
        self.assertEqual(self.cfg["profiles"][1]["nodes"][0]["auth"]["password"], "private")

    def test_shutdown_waits_for_inflight_transaction(self):
        started, release, closed = threading.Event(), threading.Event(), threading.Event()
        result = []
        def apply(state, binary, cfg, **kwargs):
            self.assertFalse(kwargs["manage_api"])
            started.set()
            if not release.wait(3):
                raise AssertionError("transaction was not released")
            self.cfg = cfg
            self.state["revision"] = "finished-before-shutdown"
        def request():
            result.append(self.call("PUT", "/active-profile", {"name": "mixed"}, revision="revision-one"))
        def shutdown():
            self.server.server_close()
            closed.set()
        with patch.object(api.life, "apply", apply):
            worker = threading.Thread(target=request)
            worker.start()
            self.assertTrue(started.wait(2))
            self.server.shutdown()
            closer = threading.Thread(target=shutdown)
            closer.start()
            try:
                self.assertFalse(closed.wait(0.05))
            finally:
                release.set()
                worker.join(3)
                closer.join(3)
        self.assertTrue(closed.is_set())
        self.assertEqual(result[0][0], 200)
        self.assertEqual(result[0][2]["revision"], "finished-before-shutdown")

    def test_malformed_inputs_are_structured_and_do_not_change_state(self):
        for raw in (b'{"name":"mixed","name":"direct"}', b'NaN', b'[]', b'{"listener":null}'):
            status, _, result = self.call("PUT", "/config", revision="revision-one", raw=raw)
            self.assertEqual(status, 400)
            self.assertIn("error", result)
        self.assertEqual(self.state["revision"], "revision-one")
