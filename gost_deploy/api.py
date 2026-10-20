"""Authenticated local JSON API; CLI and future browser UI share these operations."""
import copy
import hmac
import http.server
import json
import socket
import urllib.error
import urllib.parse
import urllib.request

from . import control, lifecycle as life, model
from .model import Error

ADDRESS = ("127.0.0.1", 18080)
PREFIX = "/api/v1"
MAX_BODY = 1024 * 1024


class APIError(Error):
    def __init__(self, status, code, message):
        super().__init__(message)
        self.status, self.code = status, code


def decode(data):
    def pairs(values):
        result = {}
        for key, value in values:
            if key in result:
                raise Error("JSON 包含重复字段。")
            result[key] = value
        return result
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=pairs,
                          parse_constant=lambda value: (_ for _ in ()).throw(ValueError()))
    except (UnicodeError, ValueError):
        raise Error("请求不是有效的 JSON。") from None


class Handler(http.server.BaseHTTPRequestHandler):
    server_version = "gost-deploy"
    sys_version = ""

    def log_message(self, *args):
        # URLs, bodies and Authorization headers must never enter logs.
        pass

    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def reply(self, status, value, revision=None):
        data = life.json_bytes(value)
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        if revision:
            self.send_header("ETag", '"' + revision + '"')
        self.end_headers()
        self.wfile.write(data)

    def body(self):
        if self.headers.get("Transfer-Encoding") or len(self.headers.get_all("Content-Length", [])) != 1:
            raise APIError(400, "invalid_request", "需要唯一 Content-Length。")
        try:
            length = int(self.headers.get("Content-Length", ""))
        except ValueError:
            raise APIError(400, "invalid_request", "Content-Length 无效。") from None
        if not 0 < length <= MAX_BODY:
            raise APIError(413, "request_too_large", "请求大小必须为 1–1048576 字节。")
        if self.headers.get_content_type() != "application/json":
            raise APIError(415, "unsupported_media_type", "仅支持 application/json。")
        raw = self.rfile.read(length)
        if len(raw) != length:
            raise APIError(400, "invalid_request", "请求不完整。")
        return decode(raw)

    def dispatch(self):
        token = "Bearer " + self.server.token
        supplied = self.headers.get("Authorization", "")
        if not hmac.compare_digest(supplied.encode("utf-8"), token.encode("utf-8")):
            raise APIError(401, "unauthorized", "需要有效的 Bearer token。")
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.query or parsed.fragment or not parsed.path.startswith(PREFIX + "/"):
            raise APIError(404, "not_found", "接口不存在。")
        path = urllib.parse.unquote(parsed.path[len(PREFIX):])
        value = self.body() if self.command in ("POST", "PUT") else None
        with life.locked():
            state = life.load_state()
            life.managed(state)
            if state.get("schema") != 2:
                raise APIError(409, "migration_required", "请先通过 install 迁移旧安装。")
            cfg = life.load_config()
            revision = state["revision"]
            if self.command == "GET":
                if path == "/config":
                    data = model.redact(cfg)
                elif path == "/status":
                    data = life.safe_status(state)
                elif path in ("/profiles", "/nodes"):
                    runtime = control.request("status") if life.active() else {"nodes": [
                        {"profile": p["name"], "id": n["id"], "type": n["type"], "addr": n["addr"],
                         "healthy": None, "lastProbe": None, "latencyMs": None, "failures": 0}
                        for p in cfg["profiles"] for n in p.get("nodes", [])]}
                    if path == "/nodes":
                        data = runtime["nodes"]
                    else:
                        data = [{"name": p["name"], "type": p["type"],
                                 "active": p["name"] == cfg["activeProfile"],
                                 "nodes": [n for n in runtime["nodes"] if n["profile"] == p["name"]]}
                                for p in cfg["profiles"]]
                else:
                    raise APIError(404, "not_found", "接口不存在。")
            elif self.command == "POST" and path == "/config/validate":
                model.merge_secrets(value, cfg)
                data = {"valid": True, "runtimeValidated": False}
            elif self.command == "POST" and path.startswith("/profiles/") and path.endswith("/probe"):
                name = path[len("/profiles/"):-len("/probe")]
                model.identifier(name)
                if not any(p["name"] == name for p in cfg["profiles"]):
                    raise APIError(404, "profile_not_found", "profile 不存在。")
                if not life.active():
                    raise APIError(409, "service_stopped", "服务停止时无法探测。")
                data = control.request("probe", profile=name)
            elif self.command == "PUT" and path in ("/config", "/active-profile"):
                expected = self.headers.get("If-Match")
                if expected is None:
                    raise APIError(428, "revision_required", "写操作需要 If-Match revision。")
                if expected != '"' + revision + '"':
                    raise APIError(409, "revision_conflict", "配置已变化，请重新读取后更新。")
                if path == "/active-profile":
                    model.fields(value, {"name"}, {"name"})
                    candidate = copy.deepcopy(cfg)
                    candidate["activeProfile"] = value["name"]
                    candidate = model.normalize(candidate)
                else:
                    candidate = model.merge_secrets(value, cfg)
                if candidate != cfg:
                    life.preflight_port(candidate, cfg)
                    life.apply(state, state["current"], candidate, manage_api=False)
                new_state = life.load_state()
                revision = new_state["revision"]
                data = life.safe_status(new_state)
            else:
                raise APIError(404, "not_found", "接口不存在。")
        return {"data": data, "revision": revision}, revision

    def handle_request(self):
        try:
            result, revision = self.dispatch()
            self.reply(200, result, revision)
        except APIError as exc:
            self.reply(exc.status, {"error": {"code": exc.code, "message": str(exc)}})
        except Error as exc:
            self.reply(400, {"error": {"code": "operation_failed", "message": str(exc)}})
        except (OSError, KeyError, TypeError, ValueError, AttributeError):
            self.reply(400, {"error": {"code": "invalid_request", "message": "请求或服务状态无效。"}})

    do_GET = handle_request
    do_POST = handle_request
    do_PUT = handle_request

    def do_OPTIONS(self):
        self.reply(405, {"error": {"code": "method_not_allowed", "message": "跨域访问未启用。"}})


class Server(http.server.ThreadingHTTPServer):
    # SIGTERM closes the listener and waits for in-flight transactions to finish.
    daemon_threads = False
    block_on_close = True

    def __init__(self, address, token):
        if not token:
            raise Error("API token 不能为空。")
        self.token = token
        super().__init__(address, Handler)


def main():
    life.require_host()
    token = life.TOKEN.read_text().strip()
    with Server(ADDRESS, token) as server:
        server.serve_forever()


def client(method, path, value=None, revision=None):
    token = life.TOKEN.read_text().strip()
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
    if revision:
        headers["If-Match"] = '"' + revision + '"'
    url = "http://{}:{}{}{}".format(ADDRESS[0], ADDRESS[1], PREFIX, path)
    req = urllib.request.Request(url, data=life.json_bytes(value) if value is not None else None,
                                 headers=headers, method=method)
    # A privileged local management request must never traverse environment proxies.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=180) as response:
            return decode(response.read(MAX_BODY + 1))
    except urllib.error.HTTPError as exc:
        try:
            message = decode(exc.read(MAX_BODY))["error"]["message"]
        except (Error, KeyError, TypeError):
            message = "管理 API 请求失败。"
        raise Error(message) from None
    except (OSError, urllib.error.URLError):
        raise Error("无法联系本机管理 API；请检查 gost-deploy-api.service。") from None
