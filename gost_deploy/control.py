"""Root manager to unprivileged runtime RPC over a mode-0600 Unix socket."""
import json
import socket
from pathlib import Path
from .model import Error

SOCKET = Path("/run/gost-deploy/control.sock")


def request(command, *, timeout=120, **values):
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as stream:
            stream.settimeout(timeout)
            stream.connect(str(SOCKET))
            stream.sendall((json.dumps({"command": command, **values}, ensure_ascii=False) + "\n").encode())
            with stream.makefile("rb") as incoming:
                data = incoming.readline(1024 * 1024 + 1)
            if len(data) > 1024 * 1024:
                raise Error("运行时响应过大。")
            result = json.loads(data)
            if "error" in result:
                raise Error("运行时操作失败。")
            return result["data"]
    except (OSError, ValueError, KeyError) as exc:
        raise Error("无法联系运行时或确认操作结果。") from exc
