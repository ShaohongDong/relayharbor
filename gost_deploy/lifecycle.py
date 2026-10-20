#!/usr/bin/env python3
"""Native GOST v3 lifecycle manager for Ubuntu and Debian. Python standard library only."""

import argparse
import uuid
import contextlib
import copy
import fcntl
import grp
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import pwd
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

DEFAULT_VERSION = "3.3.0"
PINNED = {
    "amd64": "676fb7f78d267b6ae73df719c0c7f2b565dde7147da935cfafbc1e1da558b6d5",
    "arm64": "d03699e3f385d4ff5dad68046712adfcc7515325a064d2ab046e0bece30f8f8f",
}
CONFIG = Path("/etc/gost/gost.json")
UNIT = Path("/etc/systemd/system/gost.service")
LINK = Path("/usr/local/bin/gost")
CONTROL = Path("/usr/local/sbin/gostctl")
RELEASES = Path("/opt/gost-deploy/releases")
STATE_DIR = Path("/var/lib/gost-deploy")
STATE = STATE_DIR / "state.json"
UNIT_TEXT = """[Unit]
Description=GOST SOCKS5 proxy (gost-deploy)
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
User=gost
Group=gost
ExecStart=/usr/local/bin/gost -C /etc/gost/gost.json
Restart=on-failure
RestartSec=2
TimeoutStopSec=15
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=
StandardOutput=journal
StandardError=journal
SyslogIdentifier=gost

[Install]
WantedBy=multi-user.target
"""


from .model import Error
from . import model, native, control

MANIFEST = Path("/etc/gost-deploy/config.json")
RUNTIME_INPUT = Path("/etc/gost/runtime.json")
TOKEN = STATE_DIR / "api-token"
API_UNIT = Path("/etc/systemd/system/gost-deploy-api.service")
CODE_LINK = Path("/usr/local/lib/gost-deploy")
LEGACY_UNIT_TEXT = UNIT_TEXT
UNIT_TEXT = UNIT_TEXT.replace(
    "ExecStart=/usr/local/bin/gost -C /etc/gost/gost.json",
    "ExecStart=/usr/local/sbin/gostctl _runtime\nRuntimeDirectory=gost-deploy\nRuntimeDirectoryMode=0700")
UNIT_TEXT = UNIT_TEXT.replace("Description=GOST SOCKS5 proxy (gost-deploy)",
                              "Description=GOST proxy supervisor (gost-deploy)")
API_UNIT_TEXT = """[Unit]
Description=GOST deployment local management API
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/gostctl _api
Restart=on-failure
RestartSec=2
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
"""


def run(argv, *, check=True, capture=True):
    result = subprocess.run([str(v) for v in argv], text=True,
                            stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None)
    if check and result.returncode:
        # Avoid echoing subprocess output: GOST diagnostics may include configuration.
        raise Error(f"命令失败：{argv[0]} {argv[1] if len(argv) > 1 else ''}（退出码 {result.returncode}）")
    return result


def systemctl(*args, check=True):
    return run(["systemctl", *args], check=check)


def active():
    return systemctl("is-active", "--quiet", "gost.service", check=False).returncode == 0


def enabled():
    return systemctl("is-enabled", "gost.service", check=False).stdout.strip() == "enabled"


def atomic_bytes(path, data, mode=0o600, gid=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=".gost-deploy-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), mode)
            if gid is not None:
                os.fchown(stream.fileno(), 0, gid)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def json_bytes(value):
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def read_json(path):
    try:
        # Reject duplicate keys instead of allowing an unnoticed credential override.
        def pairs(values):
            result = {}
            for key, value in values:
                if key in result:
                    raise Error("JSON 中存在重复字段。")
                result[key] = value
            return result
        return json.loads(Path(path).read_text(), object_pairs_hook=pairs)
    except (ValueError, UnicodeError) as exc:
        raise Error("配置不是有效的 UTF-8 JSON。") from exc


def fields(value, allowed, required=()):
    if not isinstance(value, dict) or set(value) - set(allowed) or set(required) - set(value):
        raise Error("配置包含缺失或不支持的字段；请参照 examples/socks5.json。")


def parse_addr(value):
    if not isinstance(value, str):
        raise Error("监听地址必须为 IP:端口；IPv6 使用 [IP]:端口。")
    match = re.fullmatch(r"(?:\[([^\]]+)\]|([^:]+)):(\d+)", value)
    if not match:
        raise Error("监听地址必须为 IP:端口；IPv6 使用 [IP]:端口。")
    try:
        host = str(ipaddress.ip_address(match[1] or match[2]))
        port = int(match[3])
    except ValueError as exc:
        raise Error("监听 IP 或端口无效。") from exc
    if not 1024 <= port <= 65535:
        raise Error("非特权服务要求端口为 1024–65535。")
    return host, port


def validate(cfg, *, placeholders=False):
    fields(cfg, {"services", "authers"}, {"services", "authers"})
    if not isinstance(cfg["services"], list) or len(cfg["services"]) != 1:
        raise Error("首版要求且仅支持一个 SOCKS5 服务。")
    svc = cfg["services"][0]
    fields(svc, {"name", "addr", "handler", "listener"}, {"name", "addr", "handler", "listener"})
    if not isinstance(svc["name"], str) or not svc["name"]:
        raise Error("服务名不能为空。")
    parse_addr(svc["addr"])
    fields(svc["listener"], {"type"}, {"type"})
    if svc["listener"]["type"] != "tcp":
        raise Error("SOCKS5 监听器必须为 tcp。")
    handler = svc["handler"]
    fields(handler, {"type", "auther", "metadata"}, {"type", "auther", "metadata"})
    if handler["type"] != "socks5":
        raise Error("首版仅支持 SOCKS5。")
    metadata = handler["metadata"]
    fields(metadata, {"udp", "notls"}, {"udp", "notls"})
    if type(metadata["udp"]) is not bool or metadata["notls"] is not True:
        raise Error("udp 必须为布尔值；标准 SOCKS5 配置要求 notls=true。")
    if not isinstance(cfg["authers"], list) or len(cfg["authers"]) != 1:
        raise Error("必须配置一个包含至少一个用户的认证器。")
    auther = cfg["authers"][0]
    fields(auther, {"name", "auths"}, {"name", "auths"})
    if not isinstance(auther["name"], str) or not auther["name"] or handler["auther"] != auther["name"]:
        raise Error("服务必须引用配置中的认证器。")
    if not isinstance(auther["auths"], list) or not auther["auths"]:
        raise Error("认证用户列表不能为空。")
    names = set()
    for auth in auther["auths"]:
        fields(auth, {"username", "password"}, {"username", "password"})
        for key in ("username", "password"):
            value = auth[key]
            try:
                valid_length = isinstance(value, str) and 1 <= len(value.encode()) <= 255
            except UnicodeError:
                valid_length = False
            if not valid_length or any(ord(c) < 32 or ord(c) == 127 for c in value):
                raise Error("账号和密码必须为 1–255 字节且不包含控制字符。")
            if not placeholders and value.startswith("CHANGE_ME"):
                raise Error("请替换示例中的 CHANGE_ME 占位凭据。")
        if auth["username"] in names:
            raise Error("用户名不能重复。")
        names.add(auth["username"])
    return cfg


def credentials(cfg):
    return model.normalize(cfg)["listener"]["users"]


def initial_config(addr, udp):
    return {"services": [{"name": "socks5", "addr": addr,
                          "handler": {"type": "socks5", "auther": "users",
                                      "metadata": {"udp": udp, "notls": True}},
                          "listener": {"type": "tcp"}}],
            "authers": [{"name": "users", "auths": [{"username": "proxy", "password": secrets.token_urlsafe(24)}]}]}


def architecture():
    machine = platform.machine()
    try:
        return {"x86_64": "amd64", "aarch64": "arm64"}[machine]
    except KeyError as exc:
        raise Error("仅支持 amd64 和 arm64。") from exc


def require_host():
    if os.geteuid() != 0:
        raise Error("此命令需要 root 权限，请使用 sudo。")
    info = {}
    for line in Path("/etc/os-release").read_text().splitlines():
        key, sep, val = line.partition("=")
        if sep:
            info[key] = val.strip('"')
    if (info.get("ID"), info.get("VERSION_ID")) not in {
            ("ubuntu", "20.04"), ("ubuntu", "22.04"), ("ubuntu", "24.04"),
            ("debian", "12"), ("debian", "13")}:
        raise Error("支持 Ubuntu 20.04/22.04/24.04、Debian 12/13。")
    architecture()
    if not Path("/run/systemd/system").is_dir():
        raise Error("需要正在运行的 systemd；普通 Docker 容器不适合作为部署目标。")


@contextlib.contextmanager
def locked():
    fd = os.open("/run/lock/gost-deploy.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise Error("另一个 gostctl 管理操作正在执行。") from exc
        yield


def download(url, dest):
    for attempt in range(3):
        try:
            request = urllib.request.Request(url, headers={"User-Agent": "gost-deploy/1"})
            with urllib.request.urlopen(request, timeout=30) as response, open(dest, "wb") as stream:
                shutil.copyfileobj(response, stream)
            return
        except (urllib.error.URLError, OSError, TimeoutError):
            if attempt == 2:
                raise Error("下载官方发布文件失败；当前安装未被替换。") from None
            time.sleep(attempt + 1)


def version_arg(value):
    value = value[1:] if value.startswith("v") else value
    if not re.fullmatch(r"3\.\d+\.\d+", value):
        raise argparse.ArgumentTypeError("版本必须是 3.x.y 格式的正式版本。")
    return value


def prepare_release(version, archive=None, checksums=None):
    arch = architecture()
    filename = f"gost_{version}_linux_{arch}.tar.gz"
    if bool(archive) != bool(checksums):
        raise Error("离线安装必须同时指定 --archive 和 --checksums。")
    with tempfile.TemporaryDirectory(prefix="gost-download-") as temp:
        temp = Path(temp)
        if not archive:
            base = f"https://github.com/go-gost/gost/releases/download/v{version}"
            archive, checksums = temp / filename, temp / "checksums.txt"
            download(f"{base}/{filename}", archive)
            download(f"{base}/checksums.txt", checksums)
        sums = []
        for line in Path(checksums).read_text().splitlines():
            parts = line.split()
            if len(parts) == 2 and parts[1].lstrip("*") == filename:
                sums.append(parts[0].lower())
        if len(sums) != 1 or not re.fullmatch(r"[0-9a-f]{64}", sums[0]):
            raise Error("校验清单缺少唯一有效的目标文件记录。")
        if version == DEFAULT_VERSION and sums[0] != PINNED[arch]:
            raise Error("校验清单与仓库锁定的官方 SHA-256 不符。")
        if digest(archive) != sums[0]:
            raise Error("发布包 SHA-256 校验失败。")
        executable = temp / "gost"
        try:
            with tarfile.open(archive, "r:gz") as tar:
                # Never extract archive paths, links or arbitrary members to disk.
                members = [m for m in tar.getmembers() if m.name == "gost" and m.isfile()]
                if len(members) != 1 or not 0 < members[0].size <= 256 * 1024 * 1024:
                    raise Error("发布包中没有唯一有效的 gost 二进制。")
                with tar.extractfile(members[0]) as source, open(executable, "wb") as target:
                    shutil.copyfileobj(source, target)
        except tarfile.TarError as exc:
            raise Error("发布包解包失败。") from exc
        executable.chmod(0o755)
        result = run([executable, "-V"])
        if (not re.match(rf"gost v?{re.escape(version)}(?:\s|$)", result.stdout) or
                f"linux/{arch})" not in result.stdout):
            raise Error("发布包的 GOST 版本或架构与请求不一致。")
        binary_hash = digest(executable)
        for parent in (RELEASES.parent, RELEASES, RELEASES / version, RELEASES / version / arch,
                       RELEASES / version / arch / binary_hash):
            parent.mkdir(parents=True, exist_ok=True)
            parent.chmod(0o755)
        destination = RELEASES / version / arch / binary_hash / "gost"
        if destination.exists():
            if destination.is_symlink() or digest(destination) != binary_hash:
                raise Error("已缓存的二进制内容异常。")
        else:
            atomic_bytes(destination, executable.read_bytes(), 0o755)
        return {"version": version, "arch": arch, "binary": str(destination), "sha256": binary_hash}


def validate_native(release, cfg):
    # -O only parses/prints configuration; it does not bind sockets or start services.
    with tempfile.TemporaryDirectory(prefix="gost-config-") as temp:
        candidate = Path(temp) / "gost.json"
        atomic_bytes(candidate, json_bytes(cfg))
        run([release["binary"], "-C", candidate, "-O", "json"])


def verify_release(release):
    try:
        binary = Path(release["binary"])
        binary.relative_to(RELEASES)
        if binary.is_symlink() or digest(binary) != release["sha256"]:
            raise Error("受管二进制完整性检查失败。")
    except (KeyError, OSError, TypeError, ValueError) as exc:
        raise Error("受管二进制缺失或安装状态损坏。") from exc


def load_state(required=True):
    if not STATE.exists():
        if required:
            raise Error("尚未安装；请先执行 install。")
        return None
    state = read_json(STATE)
    if not isinstance(state, dict) or state.get("schema") not in (1, 2) or not isinstance(state.get("installed"), bool):
        raise Error("安装状态格式无效。")
    return state


def managed(state):
    if not state or not state["installed"]:
        raise Error("服务已卸载或尚未安装。")
    verify_release(state["current"])
    if not LINK.is_symlink() or os.readlink(LINK) != state["current"]["binary"]:
        raise Error("gost 路径不再属于本工具，拒绝覆盖。")
    expected_unit = LEGACY_UNIT_TEXT if state.get("schema") == 1 else UNIT_TEXT
    if UNIT.is_symlink() or not UNIT.is_file() or UNIT.read_text() != expected_unit:
        raise Error("gost.service 被外部修改，拒绝覆盖。")
    # A drop-in or alternative loaded unit can change what systemd actually runs.
    if systemctl("show", "gost.service", "-p", "FragmentPath", "--value").stdout.strip() != str(UNIT):
        raise Error("systemd 实际加载的 unit 与受管 unit 不一致。")
    if systemctl("show", "gost.service", "-p", "DropInPaths", "--value").stdout.strip():
        raise Error("检测到外部 systemd drop-in，拒绝修改受管服务。")

    if state.get("schema") == 2:
        if not CODE_LINK.is_symlink() or os.readlink(CODE_LINK) != state.get("manager"):
            raise Error("管理模块路径不再属于本工具，拒绝覆盖。")
        if API_UNIT.is_symlink() or not API_UNIT.is_file() or API_UNIT.read_text() != API_UNIT_TEXT:
            raise Error("管理 API unit 被外部修改，拒绝覆盖。")
        if systemctl("show", "gost-deploy-api.service", "-p", "FragmentPath", "--value").stdout.strip() != str(API_UNIT):
            raise Error("管理 API 实际 unit 不一致。")
        if systemctl("show", "gost-deploy-api.service", "-p", "DropInPaths", "--value").stdout.strip():
            raise Error("管理 API 存在外部 drop-in，拒绝修改。")


def preflight_api():
    with socket.socket() as stream:
        stream.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            stream.bind(("127.0.0.1", 18080))
        except OSError as exc:
            raise Error("本机管理 API 端口 18080 已占用。") from exc


def api_health():
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if systemctl("is-active", "--quiet", "gost-deploy-api.service", check=False).returncode == 0:
            try:
                with socket.create_connection(("127.0.0.1", 18080), timeout=0.5):
                    return
            except OSError:
                pass
        time.sleep(0.1)
    raise Error("本机管理 API 未通过启动检查。")


def no_conflicts(state):
    if state and state["installed"]:
        managed(state)
        if state.get("schema") == 1 and any(os.path.lexists(p) for p in (API_UNIT, CODE_LINK, MANIFEST, RUNTIME_INPUT)):
            raise Error("迁移目标已存在外部管理资源，拒绝接管。")
        if state.get("schema") == 1:
            preflight_api()
        return
    if (os.path.lexists(API_UNIT) or Path(str(API_UNIT) + ".d").exists() or
            (os.path.lexists(CODE_LINK) and not (state and CODE_LINK.is_symlink() and
             os.readlink(CODE_LINK) == state.get("manager"))) or (MANIFEST.exists() and not state)):
        raise Error("发现已有管理 API、模块路径或管理配置，拒绝接管。")
    preflight_api()
    if shutil.which("gost"):
        raise Error("PATH 中存在已有 gost 二进制，拒绝接管。")
    if (os.path.lexists(LINK) or os.path.lexists(UNIT) or
            Path("/etc/systemd/system/gost.service.d").exists() or
            (os.path.lexists(CONTROL) and not state)):
        raise Error("发现已有 gost 二进制或服务，拒绝接管。")
    if systemctl("show", "gost.service", "-p", "LoadState", "--value").stdout.strip() != "not-found":
        raise Error("发现已有同名 systemd 服务，拒绝接管。")
    if CONFIG.exists() and not state:
        raise Error("发现已有 GOST 配置，拒绝覆盖。")
    # A retained account may be reused only with this tool's persisted ownership record.
    for lookup in (pwd.getpwnam, grp.getgrnam):
        try:
            lookup("gost")
        except KeyError:
            continue
        if not state or not state.get("owns_account"):
            raise Error("发现已有 gost 用户或组，拒绝接管。")


def load_config():
    return model.normalize(read_json(MANIFEST if MANIFEST.exists() else CONFIG))


def snapshot(state):
    return {"release": copy.deepcopy(state["current"]), "config": load_config(),
            "active": active(), "enabled": enabled(),
            "revision": state.get("revision"), "runtime": state.get("runtime"),
            "legacy": state.get("schema") == 1,
            "native_config": read_json(CONFIG), "unit": UNIT.read_text(),
            "control": CONTROL.read_text() if CONTROL.exists() else None,
            "code": os.readlink(CODE_LINK) if CODE_LINK.is_symlink() else None}


def manager_target():
    checksum = hashlib.sha256()
    for path in sorted(Path(__file__).resolve().parent.glob("*.py")):
        checksum.update(path.name.encode())
        checksum.update(path.read_bytes())
    return RELEASES.parent / "managers" / checksum.hexdigest()


def manager_current(state):
    return state.get("manager") == str(manager_target())


def deploy_code():
    source = Path(__file__).resolve().parent
    files = sorted(source.glob("*.py"))
    directory = manager_target()
    package = directory / "gost_deploy"
    for parent in (RELEASES.parent, RELEASES.parent / "managers", directory, package):
        parent.mkdir(parents=True, exist_ok=True)
        parent.chmod(0o755)
    for path in files:
        atomic_bytes(package / path.name, path.read_bytes(), 0o644)
    launcher = source.parent / "gostctl"
    if not launcher.exists():
        launcher = CONTROL
    atomic_bytes(directory / "gostctl", launcher.read_bytes(), 0o755)
    CODE_LINK.parent.mkdir(parents=True, exist_ok=True)
    temporary = CODE_LINK.with_name(".gost-deploy-code-{}".format(os.getpid()))
    try:
        temporary.symlink_to(directory)
        os.replace(temporary, CODE_LINK)
    finally:
        temporary.unlink(missing_ok=True)
    atomic_bytes(CONTROL, (directory / "gostctl").read_bytes(), 0o755)


def runtime_options(state):
    if state and state.get("runtime"):
        return state["runtime"]
    from .runtime import free_addr
    return {"gateway": free_addr(), "auth": {"username": "gost-internal",
            "password": secrets.token_urlsafe(32)}}


def write_managed(cfg, release, options, revision):
    atomic_bytes(MANIFEST, json_bytes(cfg))
    write_config(native.frontend(cfg, options))
    atomic_bytes(RUNTIME_INPUT, json_bytes({"config": cfg, "binary": str(LINK),
                 "options": options, "revision": revision}), 0o640, grp.getgrnam("gost").gr_gid)


def routing_only(state, cfg, release):
    return (state and state.get("schema") == 2 and state["current"] == release and
            load_config()["listener"] == cfg["listener"] and manager_current(state))


def safe_status(state):
    result = {"version": state["current"]["version"], "arch": state["current"]["arch"],
              "active": active(), "enabled": enabled(), "revision": state.get("revision"),
              "activeProfile": load_config()["activeProfile"], "available": False}
    if result["active"] and state.get("schema") == 2:
        result.update(control.request("status"))
    return result


def set_link(binary):
    temporary = LINK.with_name(f".gost-deploy-link-{os.getpid()}")
    LINK.parent.mkdir(parents=True, exist_ok=True)
    try:
        temporary.symlink_to(binary)
        os.replace(temporary, LINK)
    finally:
        temporary.unlink(missing_ok=True)


def write_config(cfg):
    CONFIG.parent.mkdir(parents=True, exist_ok=True)
    CONFIG.parent.chmod(0o750)
    os.chown(CONFIG.parent, 0, grp.getgrnam("gost").gr_gid)
    atomic_bytes(CONFIG, json_bytes(cfg), 0o640, grp.getgrnam("gost").gr_gid)


def read_exact(stream, size):
    result = b""
    while len(result) < size:
        part = stream.recv(size - len(result))
        if not part:
            raise Error("SOCKS5 服务提前关闭连接。")
        result += part
    return result


def authenticate(cfg, auth):
    host, port = model.endpoint(model.normalize(cfg)["listener"]["addr"], listen=True)
    host = {"0.0.0.0": "127.0.0.1", "::": "::1"}.get(host, host)
    with socket.create_connection((host, port), timeout=1) as stream:
        # Offer both methods, so accidental no-auth takes precedence and is detected.
        stream.sendall(b"\x05\x02\x00\x02")
        if read_exact(stream, 2) != b"\x05\x02":
            raise Error("服务没有要求 SOCKS5 用户名密码认证。")
        user, password = auth["username"].encode(), auth["password"].encode()
        stream.sendall(b"\x01" + bytes([len(user)]) + user + bytes([len(password)]) + password)
        if read_exact(stream, 2) != b"\x01\x00":
            raise Error("SOCKS5 账号认证检查失败。")


def health(cfg):
    cfg = model.normalize(cfg)
    probe_budget = sum((len(p["nodes"]) / 16 + 1) * p["probe"]["timeout"]
                       for p in cfg["profiles"] if p["type"] == "pool")
    deadline = time.monotonic() + 12 + probe_budget
    while time.monotonic() < deadline:
        try:
            if active():
                for auth in model.normalize(cfg)["listener"]["users"]:
                    authenticate(cfg, auth)
                return
        except (OSError, Error):
            pass
        time.sleep(0.25)
    raise Error("服务未通过启动和 SOCKS5 认证检查；请查看 journalctl -u gost。")


def restore_service(was_active, was_enabled):
    systemctl("enable" if was_enabled else "disable", "gost.service")
    if not was_active:
        systemctl("stop", "gost.service")


def apply(state, release, cfg, *, fresh=False, desired=None, manage_api=True):
    cfg = model.normalize(cfg)
    options = runtime_options(state)
    revision = str(uuid.uuid4())
    validate_native(release, native.frontend(cfg, options))
    for profile in cfg["profiles"]:
        for node in profile.get("nodes", []):
            validate_native(release, native.worker("127.0.0.1:41080", options["auth"], node))
    previous = snapshot(state) if not fresh else None
    paths = (CONFIG, MANIFEST, RUNTIME_INPUT, UNIT, API_UNIT, CONTROL, STATE, TOKEN)
    originals = {p: p.read_bytes() if p.exists() else None for p in paths}
    original_code = os.readlink(CODE_LINK) if CODE_LINK.is_symlink() else None
    created_account = False
    manager_changed = bool(state and state.get("schema") == 2 and not manager_current(state))
    hot = (not fresh and routing_only(state, cfg, release) and
           (desired is None or all(desired[k] == previous[k] for k in ("active", "enabled"))))
    desired = desired or ({"active": True, "enabled": True} if fresh else previous)
    prepared = False
    try:
        if hot:
            if active():
                control.request("prepare", config=cfg, revision=revision)
                prepared = True
        else:
            if fresh:
                try:
                    pwd.getpwnam("gost")
                except KeyError:
                    run(["useradd", "--system", "--user-group", "--home-dir", "/nonexistent",
                         "--no-create-home", "--shell", "/usr/sbin/nologin", "gost"])
                    created_account = True
            else:
                systemctl("stop", "gost.service")
            STATE_DIR.mkdir(parents=True, exist_ok=True)
            STATE_DIR.chmod(0o700)
            deploy_code()
            atomic_bytes(UNIT, UNIT_TEXT.encode(), 0o644)
            atomic_bytes(API_UNIT, API_UNIT_TEXT.encode(), 0o644)
            if not TOKEN.exists():
                atomic_bytes(TOKEN, (secrets.token_urlsafe(32) + "\n").encode())
        write_managed(cfg, release, options, revision)
        if not hot:
            set_link(release["binary"])
            systemctl("daemon-reload")
            systemctl("reset-failed", "gost.service", check=False)
            systemctl("start", "gost.service")
            health(cfg)
            restore_service(desired["active"], desired["enabled"])
        new_state = {"schema": 2, "installed": True, "owns_account": True,
                     "current": release, "previous": previous, "revision": revision, "runtime": options,
                     "manager": os.readlink(CODE_LINK)}
        # Durable desired state precedes publishing the runtime generation.
        atomic_bytes(STATE, json_bytes(new_state))
        if prepared:
            result = control.request("commit", revision=revision)
            if result.get("revision") != revision:
                raise Error("运行时代次确认失败。")
        if not hot and manage_api:
            systemctl("enable", "gost-deploy-api.service")
            systemctl("restart" if manager_changed else "start", "gost-deploy-api.service")
            api_health()
    except BaseException as exc:
        try:
            if hot:
                for path in (CONFIG, MANIFEST, RUNTIME_INPUT, STATE):
                    data = originals[path]
                    if data is None:
                        path.unlink(missing_ok=True)
                    elif path in (CONFIG, RUNTIME_INPUT):
                        atomic_bytes(path, data, 0o640, grp.getgrnam("gost").gr_gid)
                    else:
                        atomic_bytes(path, data)
                if prepared:
                    observed = control.request("status")
                    if observed.get("revision") != state.get("revision"):
                        control.request("prepare", config=previous["config"], revision=state["revision"])
                        control.request("commit", revision=state["revision"])
                    else:
                        control.request("abort")
            else:
                systemctl("stop", "gost.service", check=False)
                if fresh:
                    systemctl("disable", "--now", "gost-deploy-api.service", check=False)
                for path, data in originals.items():
                    if data is None:
                        path.unlink(missing_ok=True)
                    else:
                        mode = 0o755 if path == CONTROL else 0o644 if path in (UNIT, API_UNIT) else 0o600
                        gid = grp.getgrnam("gost").gr_gid if path in (CONFIG, RUNTIME_INPUT) else None
                        atomic_bytes(path, data, 0o640 if gid is not None else mode, gid)
                if original_code:
                    temporary = CODE_LINK.with_name(".gost-deploy-restore-{}".format(os.getpid()))
                    temporary.symlink_to(original_code)
                    os.replace(temporary, CODE_LINK)
                else:
                    CODE_LINK.unlink(missing_ok=True)
                if previous:
                    set_link(previous["release"]["binary"])
                else:
                    LINK.unlink(missing_ok=True)
                    systemctl("disable", "gost.service", check=False)
                systemctl("daemon-reload")
                if previous:
                    systemctl("reset-failed", "gost.service", check=False)
                    if previous["active"]:
                        systemctl("start", "gost.service")
                        health(previous["config"])
                    restore_service(previous["active"], previous["enabled"])
                    if manage_api and manager_changed and originals[API_UNIT] is not None:
                        systemctl("restart", "gost-deploy-api.service")
                        api_health()
                elif created_account:
                    run(["userdel", "gost"])
                    run(["groupdel", "gost"], check=False)
        except BaseException:
            raise Error("更新失败，自动恢复也未完成；请检查服务和受限状态文件。") from exc
        raise Error("操作失败，已恢复原配置、二进制及服务状态。" if previous else
                    "安装失败，已撤销新服务；下载缓存可能保留。") from exc


def preflight_port(cfg, current=None):
    host, port = model.endpoint(model.normalize(cfg)["listener"]["addr"], listen=True)
    if current and active() and model.endpoint(model.normalize(current)["listener"]["addr"], listen=True)[1] == port:
        # Address changes on the same owned port are checked by the transactional start.
        return
    with socket.socket(socket.AF_INET6 if ":" in host else socket.AF_INET, socket.SOCK_STREAM) as stream:
        stream.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            stream.bind((host, port))
        except OSError as exc:
            raise Error("监听 IP 不属于本机或 TCP 端口已占用。") from exc


def uninstall(state, purge):
    if state["installed"]:
        managed(state)
        if state.get("schema") == 2:
            systemctl("disable", "--now", "gost-deploy-api.service")
            API_UNIT.unlink(missing_ok=True)
        systemctl("disable", "--now", "gost.service")
        LINK.unlink()
        UNIT.unlink()
        systemctl("daemon-reload")
        systemctl("reset-failed", "gost.service", check=False)
        state["installed"] = False
        atomic_bytes(STATE, json_bytes(state))
    if purge:
        CONFIG.unlink(missing_ok=True)
        RUNTIME_INPUT.unlink(missing_ok=True)
        MANIFEST.unlink(missing_ok=True)
        CODE_LINK.unlink(missing_ok=True)
        if MANIFEST.parent.exists() and not any(MANIFEST.parent.iterdir()):
            MANIFEST.parent.rmdir()
        if CONFIG.parent.exists() and not any(CONFIG.parent.iterdir()):
            CONFIG.parent.rmdir()
        shutil.rmtree(RELEASES.parent, ignore_errors=False)
        if state.get("owns_account"):
            run(["userdel", "gost"])
            run(["groupdel", "gost"], check=False)
        shutil.rmtree(STATE_DIR)
        CONTROL.unlink(missing_ok=True)
    print("卸载完成。" + ("已清除受管配置和状态。" if purge else "配置、二进制缓存和恢复资料已保留。"))


def parser():
    cli = argparse.ArgumentParser(description="Ubuntu/Debian GOST v3 原生服务管理")
    commands = cli.add_subparsers(dest="command", required=True)
    init = commands.add_parser("init", help="生成随机密码的管理配置")
    init.add_argument("--output", type=Path, required=True)
    init.add_argument("--listen", default="0.0.0.0:1080")
    init.add_argument("--udp", action="store_true")
    check = commands.add_parser("check", help="静态检查配置，不启动服务")
    check.add_argument("--config", type=Path, required=True)
    for name in ("install", "upgrade"):
        command = commands.add_parser(name)
        command.add_argument("--version", type=version_arg, default=DEFAULT_VERSION,
                             required=name == "upgrade")
        command.add_argument("--archive", type=Path)
        command.add_argument("--checksums", type=Path)
        if name == "install":
            command.add_argument("--config", type=Path, required=True)
    configure = commands.add_parser("configure")
    configure.add_argument("--config", type=Path, required=True)
    for name in ("rollback", "start", "stop", "restart", "status"):
        commands.add_parser(name)
    profile = commands.add_parser("profile")
    actions = profile.add_subparsers(dest="action", required=True)
    actions.add_parser("list")
    for name in ("use", "probe"):
        actions.add_parser(name).add_argument("name")
    logs = commands.add_parser("logs")
    logs.add_argument("--follow", action="store_true")
    remove = commands.add_parser("uninstall")
    remove.add_argument("--purge", action="store_true")
    for name in ("_runtime", "_api"):
        commands.add_parser(name, help=argparse.SUPPRESS)
    return cli


def main(argv=None):
    args = parser().parse_args(argv)
    if args.command == "_runtime":
        from .runtime import main as runtime_main
        return runtime_main()
    if args.command == "_api":
        from .api import main as api_main
        return api_main()
    if args.command == "init":
        cfg = model.normalize(initial_config(args.listen, args.udp))
        fd = os.open(args.output, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as output:
            output.write(json_bytes(cfg))
        print("配置已生成（权限 0600），账号 proxy；请在文件内查看随机密码。")
        return
    if args.command == "check":
        model.normalize(read_json(args.config))
        print("配置静态检查通过；尚未验证运行、网络和 UDP。")
        return
    require_host()
    # Installed config/profile operations share the same API and revision checks.
    if args.command in ("configure", "profile"):
        from .api import client
        if args.command == "configure":
            current = client("GET", "/config")
            result = client("PUT", "/config", read_json(args.config), current["revision"])
        elif args.action == "list":
            result = client("GET", "/profiles")
        elif args.action == "probe":
            import urllib.parse
            result = client("POST", "/profiles/" + urllib.parse.quote(args.name, safe="") + "/probe", {})
        else:
            current = client("GET", "/config")
            result = client("PUT", "/active-profile", {"name": args.name}, current["revision"])
        print(json.dumps(result, ensure_ascii=False))
        return
    if args.command == "logs":
        managed(load_state())
        os.execvp("journalctl", ["journalctl", "-u", "gost.service", "--no-pager",
                                *(["-f"] if args.follow else ["-n", "100"])])
    with locked():
        state = load_state(required=args.command != "install")
        if args.command == "install":
            cfg = model.normalize(read_json(args.config))
            no_conflicts(state)
            if state and state["installed"] and state.get("schema") == 2:
                if state["current"]["version"] != args.version or load_config() != cfg:
                    raise Error("已经安装；更改配置请用 configure，更改版本请用 upgrade。")
                if manager_current(state):
                    print("安装已存在且配置一致，无需修改。")
                    return
            preflight_port(cfg, load_config() if state and state["installed"] else None)
            release = prepare_release(args.version, args.archive, args.checksums)
            apply(state, release, cfg, fresh=not (state and state["installed"]))
        elif args.command == "uninstall":
            uninstall(state, args.purge)
            return
        else:
            managed(state)
            if args.command == "status":
                result = safe_status(state)
                print(json.dumps(result, ensure_ascii=False))
                if not result["active"]:
                    raise Error("服务当前未运行。")
                return
            if args.command in ("start", "stop", "restart"):
                systemctl(args.command, "gost.service")
                if args.command != "stop":
                    health(load_config())
                print("服务操作完成。")
                return
            if args.command == "upgrade":
                cfg = load_config()
                if (args.version == state["current"]["version"] and state.get("schema") == 2 and
                        manager_current(state)):
                    print("已经是指定版本，无需修改。")
                    return
                release = (state["current"] if args.version == state["current"]["version"] else
                           prepare_release(args.version, args.archive, args.checksums))
                apply(state, release, cfg)
            elif args.command == "rollback":
                previous = state.get("previous")
                if not previous:
                    raise Error("没有上一份可恢复版本。")
                verify_release(previous["release"])
                cfg = model.normalize(previous["config"])
                preflight_port(cfg, load_config())
                apply(state, previous["release"], cfg, desired=previous)
        print("操作完成，服务及 SOCKS5 认证检查通过。")


def entrypoint():
    def interrupted(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    try:
        main()
    except (Error, OSError, KeyboardInterrupt) as exc:
        if isinstance(exc, KeyboardInterrupt):
            message = "操作被中断。"
        elif isinstance(exc, OSError):
            message = "文件或系统操作失败（{}）。".format(exc.strerror or type(exc).__name__)
        else:
            message = str(exc)
        print("错误：" + message, file=sys.stderr)
        sys.exit(1)
