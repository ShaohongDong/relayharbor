"""Versioned management configuration, independent of generated GOST JSON."""
import copy
import ipaddress
import re
import urllib.parse


class Error(Exception):
    """A safe, user-visible error; never include credentials."""


def fields(value, allowed, required=()):
    if not isinstance(value, dict) or set(value) - set(allowed) or set(required) - set(value):
        raise Error("配置包含缺失或不支持的字段。")


def text(value, label, limit=255):
    try:
        valid = isinstance(value, str) and 1 <= len(value.encode("utf-8")) <= limit
    except UnicodeError:
        valid = False
    if not valid or any(ord(c) < 32 or ord(c) == 127 for c in value):
        raise Error(label + "格式无效。")
    return value


def endpoint(value, *, listen=False):
    text(value, "地址", 1024)
    match = re.fullmatch(r"(?:\[([^\]]+)\]|([^:\s/]+)):(\d+)", value)
    if not match:
        raise Error("地址必须为 host:port；IPv6 使用 [IP]:port。")
    host, port = match[1] or match[2], int(match[3])
    if not (1024 if listen else 1) <= port <= 65535:
        raise Error("端口超出允许范围。")
    try:
        ipaddress.ip_address(host)
    except ValueError:
        if listen or match[1] or not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?", host):
            raise Error("IP 或主机名无效。")
    return host, port


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", value):
        raise Error("名称/ID 必须为 1–64 位字母、数字、下划线或连字符。")


def auth(value, *, placeholders=False):
    fields(value, {"username", "password"}, {"username", "password"})
    for key in ("username", "password"):
        text(value[key], "账号或密码")
        if not placeholders and value[key].startswith("CHANGE_ME"):
            raise Error("请替换示例中的 CHANGE_ME 占位凭据。")


def number(value, low, high, label):
    if type(value) not in (int, float) or not low <= value <= high:
        raise Error(label + "超出允许范围。")


def normalize(value, *, placeholders=False):
    if isinstance(value, dict) and "services" in value:
        from .lifecycle import validate
        validate(value, placeholders=placeholders)
        service = value["services"][0]
        value = {"schemaVersion": 2, "listener": {
            "addr": service["addr"], "udp": service["handler"]["metadata"]["udp"],
            "users": value["authers"][0]["auths"]},
            "activeProfile": "direct", "profiles": [{"name": "direct", "type": "direct"}]}
    cfg = copy.deepcopy(value)
    fields(cfg, {"schemaVersion", "listener", "activeProfile", "profiles"},
           {"schemaVersion", "listener", "activeProfile", "profiles"})
    if type(cfg["schemaVersion"]) is not int or cfg["schemaVersion"] != 2:
        raise Error("仅支持 schemaVersion=2 或旧版 SOCKS5 配置。")
    listener = cfg["listener"]
    fields(listener, {"addr", "udp", "users"}, {"addr", "udp", "users"})
    endpoint(listener["addr"], listen=True)
    if type(listener["udp"]) is not bool or not isinstance(listener["users"], list) or not listener["users"]:
        raise Error("入口必须配置认证用户及布尔值 udp。")
    names = set()
    for user in listener["users"]:
        auth(user, placeholders=placeholders)
        if user["username"] in names:
            raise Error("用户名不能重复。")
        names.add(user["username"])
    if not isinstance(cfg["profiles"], list) or not cfg["profiles"]:
        raise Error("至少需要一个 profile。")
    names = set()
    for profile in cfg["profiles"]:
        fields(profile, {"name", "type", "nodes", "probe"}, {"name", "type"})
        identifier(profile["name"])
        if profile["name"] in names:
            raise Error("profile 名称不能重复。")
        names.add(profile["name"])
        if profile["type"] == "direct":
            fields(profile, {"name", "type"}, {"name", "type"})
            continue
        if profile["type"] != "pool":
            raise Error("profile type 必须为 direct 或 pool。")
        fields(profile, {"name", "type", "nodes", "probe"}, {"name", "type", "nodes", "probe"})
        if not isinstance(profile["nodes"], list) or not profile["nodes"]:
            raise Error("代理池不能为空。")
        ids = set()
        for node in profile["nodes"]:
            fields(node, {"id", "type", "addr", "auth"}, {"id", "type", "addr"})
            identifier(node["id"])
            if node["id"] in ids:
                raise Error("池内节点 ID 不能重复。")
            ids.add(node["id"])
            if node["type"] not in ("http", "socks5"):
                raise Error("上游代理仅支持 http 和 socks5。")
            endpoint(node["addr"])
            if node.get("auth") is not None:
                auth(node["auth"], placeholders=placeholders)
        probe = profile["probe"]
        fields(probe, {"type", "addr", "url", "interval", "timeout", "expectedStatus"}, {"type"})
        probe.setdefault("interval", 30)
        probe.setdefault("timeout", 5)
        number(probe["interval"], 1, 86400, "探测间隔")
        number(probe["timeout"], 0.1, 60, "探测超时")
        if probe["type"] == "tcp":
            fields(probe, {"type", "addr", "interval", "timeout"}, {"addr"})
            endpoint(probe["addr"])
        elif probe["type"] == "http":
            fields(probe, {"type", "url", "interval", "timeout", "expectedStatus"}, {"url"})
            text(probe["url"], "探测 URL", 4096)
            try:
                url = urllib.parse.urlsplit(probe["url"])
                valid = (url.scheme in ("http", "https") and url.hostname and not url.username and
                         not url.password and not url.fragment and (url.port is None or 1 <= url.port <= 65535))
                if not valid:
                    raise ValueError()
                endpoint(format_endpoint(url.hostname, url.port or (443 if url.scheme == "https" else 80)))
                probe["url"].encode("ascii")
            except (ValueError, UnicodeError):
                raise Error("探测 URL 必须为无凭据、无片段的 HTTP/HTTPS ASCII URL。") from None
            probe.setdefault("expectedStatus", 200)
            if type(probe["expectedStatus"]) is not int or not 100 <= probe["expectedStatus"] <= 599:
                raise Error("HTTP 探测状态码无效。")
        else:
            raise Error("探测类型必须为 tcp 或 http。")
    if not isinstance(cfg["activeProfile"], str) or cfg["activeProfile"] not in names:
        raise Error("activeProfile 必须引用已有 profile。")
    return cfg


def format_endpoint(host, port):
    return "[{}]:{}".format(host, port) if ":" in host else "{}:{}".format(host, port)


def redact(cfg):
    result = copy.deepcopy(cfg)
    for user in result["listener"]["users"]:
        user["passwordSet"] = bool(user.pop("password", None))
    for profile in result["profiles"]:
        for node in profile.get("nodes", []):
            if node.get("auth"):
                node["auth"]["passwordSet"] = bool(node["auth"].pop("password", None))
    return result


def merge_secrets(candidate, current):
    """A missing password preserves only the same stable identity and username."""
    result = copy.deepcopy(candidate)
    if not isinstance(result, dict):
        raise Error("配置必须为对象。")
    old_users = {u["username"]: u for u in current["listener"]["users"]}
    users = result.get("listener", {}).get("users", [])
    if isinstance(users, list):
        for user in users:
            if isinstance(user, dict):
                user.pop("passwordSet", None)
                if "password" not in user and user.get("username") in old_users:
                    user["password"] = old_users[user["username"]]["password"]
    old = {(p["name"], n["id"]): n for p in current["profiles"] for n in p.get("nodes", [])}
    profiles = result.get("profiles", [])
    if isinstance(profiles, list):
        for p in profiles:
            if not isinstance(p, dict) or not isinstance(p.get("nodes", []), list):
                continue
            for n in p.get("nodes", []):
                if not isinstance(n, dict) or not isinstance(n.get("auth"), dict):
                    continue
                value = n["auth"]
                value.pop("passwordSet", None)
                previous = old.get((p.get("name"), n.get("id")), {}).get("auth")
                if "password" not in value and previous and value.get("username") == previous["username"]:
                    value["password"] = previous["password"]
    return normalize(result)
